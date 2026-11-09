package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"fold/internal/audit"
	"fold/internal/jobs"
	"fold/models"

	"gorm.io/gorm"
)

// A confirmed rollback makes a no-op skip contactMutationCtx's reconciliation
// too. A COMMIT error or unacknowledged ROLLBACK is never hidden by this sentinel.
var errTombstoneNoop = errors.New("tombstone erasure has no work")

// tombstoneReport is a count-only report, never an identity reconstruction.
// Memberships are unexpected old-handler residue: report them, but never
// invent a successor or hand off leadership during maintenance.
type tombstoneReport struct {
	HouseholdsKept      int
	ActivityEntriesKept int64
	MembershipsKept     int64
}

// tombstoneErasureSubject deliberately has no address. The old handler lost
// it, and neither audit text nor a later holder proves ownership (O-17).
func tombstoneErasureSubject(id uint) erasureSubject {
	return erasureSubject{ID: id}
}

// lockTombstoneIn is L0, after the contact gate. Raw SQL is unscoped. An
// arbitrary soft-deleted user or a lookalike placeholder is never selected.
func lockTombstoneIn(tx *gorm.DB, id uint) (bool, error) {
	var ids []uint
	err := tx.Raw(`SELECT id FROM users WHERE id = ? AND deleted_at IS NOT NULL
		AND email = 'deleted-' || id || '@deleted.invalid' FOR UPDATE`, id).Scan(&ids).Error
	return len(ids) == 1, err
}

// scrubTombstoneRowIn is L3. Unlike tombstoneUserIn it changes neither email
// nor session_version, and a clean row is not rewritten (including updated_at).
// The only users trigger is push_devices_end_with_sessions, AFTER UPDATE OF
// session_version, status, conditional on a version change or a transition
// away from active. L2 has already deleted devices, so even a legacy active
// tombstone's status transition cannot queue push.devices_off. The trigger
// census and whole-database tests pin this absence of side effects.
func scrubTombstoneRowIn(tx *gorm.DB, id uint) error {
	return tx.Exec(`UPDATE users SET name = '', display_name = '', phone = '', timezone = '',
		password_hash = NULL, birth_date = NULL, avatar_focal_x = 50, avatar_focal_y = 50,
		hide_contact_from_leaders = false, avatar_key = '', status = 'disabled',
		administratively_disabled = true, updated_at = now()
		WHERE id = ? AND deleted_at IS NOT NULL AND email = 'deleted-' || id || '@deleted.invalid'
		AND (name IS DISTINCT FROM '' OR display_name IS DISTINCT FROM '' OR phone IS DISTINCT FROM '' OR timezone <> ''
		OR password_hash IS NOT NULL OR birth_date IS NOT NULL OR avatar_focal_x IS DISTINCT FROM 50
		OR avatar_focal_y IS DISTINCT FROM 50 OR hide_contact_from_leaders OR avatar_key IS DISTINCT FROM ''
		OR status <> 'disabled' OR NOT administratively_disabled)`, id).Error
}

// eraseTombstoneOnce performs one whole erasure under one contact transaction.
// The caller supplies the job's two-minute context. contactMutationCtx bounds
// the client by D and server lock release by D + S + I (2m + 20s + 2m), within
// the runner's 15-minute lease. It returns reserve, rollback and commit errors
// unchanged for the runner to retry. No retry or lease takeover bypasses the
// gate: only the marker committed with the scrub decides whether work remains.
//
// Write conditions, for the repeat/version-bump review:
// L2/L6/L14 delete only existing id-owned rows. L3 writes only a dirty user.
// L4 adds a cleanup only when no identical queued cleanup exists. L5 withdraws
// only pending REQ, scrubs only dirty REQ and blanks only nonempty inviter IPs;
// its guardian-address arm is skipped. L7 blanks only nonempty consent evidence;
// L8 locks and blanks only nonempty subject ages notes. L9 deletes existing
// seats and unused teen tokens, scrubs only dirty PERS/registration fields,
// reconciles only future active staged registrations, revokes only live links,
// and scrubs only dirty, proven sole-subject households. L10 promotes eligible
// waiters that fit, outside the cutoff; maintenance repeats no cutoff notice.
// L11 scrubs only dirty/open INV, cancels only scheduled messages, scrubs only
// dirty/queued recipient rows and dirty registration fields, and deletes existing
// id-owned activity rows; guest-signup and outbox address arms are skipped.
// L12 audits only host rows actually deleted. L13 blanks only attributable,
// nonempty IPs and subtracts only present metadata keys. L15 adds the marker
// only for an unmarked version. No step rewrites memberships.
func (a *App) eraseTombstoneOnce(ctx context.Context, id uint, version int) (tombstoneReport, error) {
	var report tombstoneReport
	err := a.contactMutationCtx(ctx, contactChange{UserID: id}, func(tx *gorm.DB) error {
		found, err := lockTombstoneIn(tx, id)
		if err != nil {
			return err
		}
		if !found {
			return errTombstoneNoop
		}
		var marked bool
		if err := tx.Raw(`SELECT EXISTS (SELECT 1 FROM audit_logs
			WHERE action = ? AND target_type = 'user' AND target_id = ?
			AND CASE WHEN jsonb_typeof(meta_json->'version') = 'number'
			THEN (meta_json->>'version')::numeric ELSE 0 END >= ?)`,
			audit.UserErasureSwept, id, version).Scan(&marked).Error; err != nil {
			return err
		}
		if marked {
			return errTombstoneNoop
		}
		// L1: only retained id links, validated before locks or writes.
		s := tombstoneErasureSubject(id)
		if err := captureErasureSetsIn(tx, &s); err != nil {
			return err
		}
		if err := validatePersIn(tx, s); err != nil {
			return err
		}
		homes, err := lockErasureHouseholdsIn(tx, s)
		if err != nil {
			return err // NOWAIT 55P03 rolls back whole; the job retries later.
		}
		// L2 before L3 prevents the users trigger from queuing device notices.
		if err := eraseAccountCredentialsIn(tx, s); err != nil {
			return err
		}
		if err := scrubTombstoneRowIn(tx, id); err != nil {
			return err
		}
		// L4 is named maintenance even when avatar_key is empty: only the
		// storage scan can find leftover blobs. At most one queued job.
		if err := queueAvatarCleanup(tx, id); err != nil {
			return err
		}
		// L5-L8. System actor; reasons remain under the separate T07B gate.
		if err := eraseAccountRequestsIn(tx, s, 0); err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", id).Delete(&models.Notification{}).Error; err != nil {
			return err
		}
		if err := eraseConsentEvidenceIn(tx, s); err != nil {
			return err
		}
		if err := eraseContactRequestTextIn(tx, s); err != nil {
			return err
		}
		// L9 re-evaluates every locked household, even when its other erased
		// owner already has a sweep marker. Markers never authorize ownership.
		hh, err := a.eraseHouseholdRecordsIn(tx, s, homes, 0, "")
		if err != nil {
			return err
		}
		report.HouseholdsKept = hh.HouseholdsKept
		// L10-L11 skip all address arms; each UPDATE selects unfinished work.
		if err := a.promoteOnDisable(tx, id, false); err != nil {
			return err
		}
		if err := eraseDependentRecordsIn(tx, s, 0, ""); err != nil {
			return err
		}
		// L12: hostIDs and EventKeepsHost already ignore tombstones. There
		// can be no successor choice here, so remove each stale host row with
		// R4-H's audit, without changing the event or inventing a handoff.
		// Lock-order review: handOffHosting (including removeDeletedHostRows),
		// apiRemoveEventHost, apiDeleteEvent and deleteGroup all hold the same
		// exclusive contact gate as this sweep, so their multi-row deletes
		// cannot interleave here. The ungated apiAddEventHost cleanup holds
		// live users FOR SHARE and one event FOR KEY SHARE, then deletes only
		// that event's stale seats. It holds no tombstone user lock and cannot
		// hold another event's seat while waiting for this user's seat. There
		// is at most one (event_id, user_id) row. Creation inserts live hosts
		// only. No event lock is added after the sweep's user lock: doing so
		// would reverse deletionHostPlan's event-before-user order. This does
		// not claim the whole erasure is deadlock-free (throttle limit D2);
		// any 40P01 still returns unchanged for the job runner to retry.
		var hosts []uint
		if err := tx.Raw(`DELETE FROM event_hosts WHERE user_id = ? RETURNING event_id`, id).Scan(&hosts).Error; err != nil {
			return err
		}
		sort.Slice(hosts, func(i, j int) bool { return hosts[i] < hosts[j] })
		for _, eventID := range hosts {
			if err := audit.Write(tx, audit.Entry{Action: audit.EventHostRemoved,
				TargetType: "event", TargetID: eventID,
				Meta: map[string]any{"userId": id, "reason": "deleted_host_cleanup"}}); err != nil {
				return err
			}
		}
		// L13-L14 preserve every audit/IP retention gate and delete id keys only.
		ac, err := eraseAuditDetailsIn(tx, s)
		if err != nil {
			return err
		}
		report.ActivityEntriesKept = newErasureReport(hh, ac).ActivityEntriesKept
		if err := eraseOwnedThrottleKeysIn(tx, s); err != nil {
			return err
		}
		// Old deletion hard-deleted memberships. Unexpected rows are reported,
		// never transferred or deleted by a sweep that cannot choose a leader.
		if err := tx.Model(&models.Membership{}).Where("user_id = ?", id).Count(&report.MembershipsKept).Error; err != nil {
			return err
		}
		// L15: marker and durable count report commit atomically with the scrub.
		return audit.Write(tx, audit.Entry{Action: audit.UserErasureSwept,
			TargetType: "user", TargetID: id, Meta: map[string]any{
				"version": version, "householdsKept": report.HouseholdsKept,
				"activityEntriesKept": report.ActivityEntriesKept,
			}})
	})
	if err == errTombstoneNoop {
		return tombstoneReport{}, nil
	}
	if err != nil {
		return tombstoneReport{}, err
	}
	return report, nil
}

// Bump the version only when a reviewed erasure slice adds work. Markers,
// rather than retained jobs, decide whether a subject needs that work.
const (
	kindTombstoneSweep    = "erasure.tombstone_sweep"
	kindTombstonePage     = "erasure.tombstone_page"
	kindTombstone         = "erasure.tombstone"
	tombstoneSweepVersion = 1
	tombstoneSweepPage    = 200
	tombstonePageTimeout  = 30 * time.Second
	tombstoneJobTimeout   = 2 * time.Minute
)

type tombstoneJob struct {
	UserID uint `json:"userId"`
}
type tombstonePageJob struct {
	AfterID uint   `json:"afterId"`
	Day     string `json:"day"`
}

func (a *App) erasureKinds() []jobs.Kind {
	return []jobs.Kind{
		{Name: kindTombstonePage, Label: "Finding more deleted accounts to finish erasing", MaxAttempts: jobs.DefaultMaxAttempts, Run: a.runTombstonePageJob},
		{Name: kindTombstone, Label: "Finishing the erasure of a deleted account", MaxAttempts: jobs.DefaultMaxAttempts,
			Run: func(ctx context.Context, raw json.RawMessage) error {
				var p tombstoneJob
				if err := json.Unmarshal(raw, &p); err != nil || p.UserID == 0 {
					return errors.New("invalid deleted-account erasure task")
				}
				ctx, cancel := context.WithTimeout(ctx, tombstoneJobTimeout)
				defer cancel()
				_, err := a.eraseTombstoneOnce(ctx, p.UserID, tombstoneSweepVersion)
				return err
			}},
	}
}

func (a *App) runTombstonePageJob(ctx context.Context, raw json.RawMessage) error {
	var p tombstonePageJob
	if err := json.Unmarshal(raw, &p); err != nil {
		return errors.New("invalid deleted-account discovery task")
	}
	return a.runSweepPage(ctx, p.AfterID, p.Day)
}

// The CASE deliberately excludes string, null and malformed historical
// versions without ever casting them. Keep it aligned with eraseTombstoneOnce.
const tombstoneCandidatesSQL = `SELECT u.id FROM users u
 WHERE u.deleted_at IS NOT NULL AND u.email = 'deleted-' || u.id || '@deleted.invalid'
 AND u.id > ? AND NOT EXISTS (SELECT 1 FROM audit_logs l
 WHERE l.action = ? AND l.target_type = 'user' AND l.target_id = u.id
 AND CASE WHEN jsonb_typeof(l.meta_json->'version') = 'number'
 THEN (l.meta_json->>'version')::numeric ELSE 0 END >= ?)
 ORDER BY u.id LIMIT ?`

func (a *App) runSweepPage(ctx context.Context, afterID uint, day string) error {
	return a.runSweepPageSize(ctx, afterID, day, tombstoneSweepPage)
}

// One page owns one transaction and one INSERT of at most 201 jobs. Discovery
// never waits for erasure. A poisoned subject dies alone and alerts on System;
// until fixed it gets another job, and potentially another alert, each day.
// A dead page is recovered by the next day's chain starting at zero. A crash
// after commit leaves its continuation durable. Address-only residue remains
// unknown per subject: neither this query nor the worker reconstructs O-17.
// After restore, a tombstone missing its marker is rediscovered under the next
// date key. An active account restored from a backup is outside this sweep.
func (a *App) runSweepPageSize(ctx context.Context, afterID uint, day string, pageSize int) error {
	if _, err := time.Parse("2006-01-02", day); err != nil || pageSize < 1 || pageSize > tombstoneSweepPage {
		return errors.New("invalid deleted-account discovery task")
	}
	ctx, cancel := context.WithTimeout(ctx, tombstonePageTimeout)
	defer cancel()
	return a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var ids []uint
		if err := tx.Raw(tombstoneCandidatesSQL, afterID, audit.UserErasureSwept, tombstoneSweepVersion, pageSize).Scan(&ids).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		specs := make([]jobs.Spec, 0, len(ids)+1)
		for _, id := range ids {
			specs = append(specs, jobs.Spec{Kind: kindTombstone, Payload: tombstoneJob{UserID: id}, RunAt: now,
				UniqueKey: fmt.Sprintf("%s:%d:v%d:%s", kindTombstone, id, tombstoneSweepVersion, day), MaxAttempts: jobs.DefaultMaxAttempts})
		}
		if len(ids) == pageSize {
			last := ids[len(ids)-1]
			specs = append(specs, jobs.Spec{Kind: kindTombstonePage, Payload: tombstonePageJob{AfterID: last, Day: day}, RunAt: now,
				UniqueKey: fmt.Sprintf("%s:v%d:%s:%d", kindTombstonePage, tombstoneSweepVersion, day, last), MaxAttempts: jobs.DefaultMaxAttempts})
		}
		_, err := jobs.EnqueueMany(tx, specs)
		return err
	})
}
