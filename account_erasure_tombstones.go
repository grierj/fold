package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

// lockTombstoneIn is L0, after the contact gate. Raw SQL is unscoped. An
// arbitrary soft-deleted user or a lookalike placeholder is never selected.
func lockTombstoneIn(tx *gorm.DB, id uint) (bool, error) {
	var ids []uint
	err := tx.Raw(`SELECT id FROM users WHERE id = ? AND deleted_at IS NOT NULL
		AND email = `+tombstoneEmailSQL("id")+` FOR UPDATE`, id).Scan(&ids).Error
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
	return tx.Exec(scrubTombstoneRowSQL, id).Error
}

// scrubTombstoneRowSQL sets every erasedUserColumns value, and only on a row
// where one of them differs (IS DISTINCT FROM, so a NULL counts as dirty).
var scrubTombstoneRowSQL = func() string {
	set := make([]string, 0, len(erasedUserColumns)+1)
	dirty := make([]string, 0, len(erasedUserColumns))
	for _, c := range erasedUserColumns {
		set = append(set, c.name+" = "+c.sql)
		dirty = append(dirty, c.name+" IS DISTINCT FROM "+c.sql)
	}
	set = append(set, "updated_at = now()")
	return "UPDATE users SET " + strings.Join(set, ", ") +
		" WHERE id = ? AND deleted_at IS NOT NULL AND email = " + tombstoneEmailSQL("id") +
		" AND (" + strings.Join(dirty, " OR ") + ")"
}()

// tombstoneSweptSQL is true when target (a SQL expression for a user id) has a
// sweep marker at version ? or above; it binds the marker action, then (when target
// is "?") the user id, then the version. The CASE deliberately excludes string, null and malformed
// historical versions without ever casting them.
func tombstoneSweptSQL(target string) string {
	return `EXISTS (SELECT 1 FROM audit_logs l
		WHERE l.action = ? AND l.target_type = 'user' AND l.target_id = ` + target + `
		AND CASE WHEN jsonb_typeof(l.meta_json->'version') = 'number'
		THEN (l.meta_json->>'version')::numeric ELSE 0 END >= ?)`
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
// L12 audits only host rows actually deleted. L13 blanks only attributable
// (class, S5 and conversion-group), nonempty IPs and subtracts only present
// metadata keys. L15 adds the marker only for an unmarked version. No step
// rewrites memberships.
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
		if err := tx.Raw("SELECT "+tombstoneSweptSQL("?"),
			audit.UserErasureSwept, id, version).Scan(&marked).Error; err != nil {
			return err
		}
		if marked {
			return errTombstoneNoop
		}
		// L1-L14, with a system actor and no address: the old handler lost it,
		// and neither audit text nor a later holder proves ownership (O-17).
		// A NOWAIT 55P03 on a household rolls back whole; the job retries later.
		counts, err := a.eraseAccountIn(tx, erasureSubject{ID: id}, erasureOptions{})
		if err != nil {
			return err
		}
		report.HouseholdsKept = counts.Households.HouseholdsKept
		report.ActivityEntriesKept = newErasureReport(counts.Households, counts.Audit).ActivityEntriesKept
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

// removeTombstoneHostRowsIn is L12: hostIDs and EventKeepsHost already ignore
// tombstones. There can be no successor choice here, so each stale host row is
// removed with R4-H's audit, without changing the event or inventing a
// handoff. Lock-order review: handOffHosting (including removeDeletedHostRows),
// apiRemoveEventHost, apiDeleteEvent and deleteGroup all hold the same
// exclusive contact gate as this sweep, so their multi-row deletes cannot
// interleave here. The ungated apiAddEventHost cleanup holds live users FOR
// SHARE and one event FOR KEY SHARE, then deletes only that event's stale
// seats. It holds no tombstone user lock and cannot hold another event's seat
// while waiting for this user's seat. There is at most one (event_id, user_id)
// row. Creation inserts live hosts only. No event lock is added after the
// sweep's user lock: doing so would reverse deletionHostPlan's event-before-user
// order. This does not claim the whole erasure is deadlock-free (throttle limit
// D2); any 40P01 still returns unchanged for the job runner to retry.
func removeTombstoneHostRowsIn(tx *gorm.DB, id uint) error {
	return deleteHostRowsAudited(tx, nil, "", hostRemovedDeleted, hostRowsOfUserSQL, id)
}

// Bump the version only when a reviewed erasure slice adds work. Markers,
// rather than retained jobs, decide whether a subject needs that work.
// Version 2 is the conversion-address clearing of R4-4b and S5's clearing of
// unregistered actions' addresses: a tombstone swept at version 1 may still hold
// either, so each is found again once. No version-1 marker is removed.
const (
	kindTombstoneSweep    = "erasure.tombstone_sweep"
	kindTombstonePage     = "erasure.tombstone_page"
	kindTombstone         = "erasure.tombstone"
	tombstoneSweepVersion = 2
	tombstonePageTimeout  = 30 * time.Second
	tombstoneJobTimeout   = 2 * time.Minute
)

// tombstoneSweepPage is the most subjects one discovery page queues. A var so
// tests can shorten it; a test that sets it stays serial.
var tombstoneSweepPage = 200

// The erasure job types; each takes the default attempts.
var (
	tombstoneSweepType = jobs.Type{Name: kindTombstoneSweep}
	tombstonePageType  = jobs.Type{Name: kindTombstonePage}
	tombstoneType      = jobs.Type{Name: kindTombstone}
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
		{Type: tombstonePageType, Label: "Finding more deleted accounts to finish erasing", Run: a.runTombstonePageJob},
		{Type: tombstoneType, Label: "Finishing the erasure of a deleted account",
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

// tombstoneCandidatesSQL binds the cursor, the marker action, the version and
// the page size.
var tombstoneCandidatesSQL = `SELECT u.id FROM users u
 WHERE u.deleted_at IS NOT NULL AND u.email = ` + tombstoneEmailSQL("u.id") + `
 AND u.id > ? AND NOT ` + tombstoneSweptSQL("u.id") + `
 ORDER BY u.id LIMIT ?`

// One page owns one transaction and one INSERT of at most 201 jobs. Discovery
// never waits for erasure. A poisoned subject dies alone and alerts on System;
// until fixed it gets another job, and potentially another alert, each day.
// A dead page is recovered by the next day's chain starting at zero. A crash
// after commit leaves its continuation durable. Address-only residue remains
// unknown per subject: neither this query nor the worker reconstructs O-17.
// After restore, a tombstone missing its marker is rediscovered under the next
// date key. An active account restored from a backup is outside this sweep.
func (a *App) runSweepPage(ctx context.Context, afterID uint, day string) error {
	pageSize := tombstoneSweepPage
	if _, err := time.Parse("2006-01-02", day); err != nil {
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
			specs = append(specs, tombstoneType.Spec(tombstoneJob{UserID: id}, now,
				fmt.Sprintf("%s:%d:v%d:%s", kindTombstone, id, tombstoneSweepVersion, day)))
		}
		if len(ids) == pageSize {
			last := ids[len(ids)-1]
			specs = append(specs, tombstonePageType.Spec(tombstonePageJob{AfterID: last, Day: day}, now,
				fmt.Sprintf("%s:v%d:%s:%d", kindTombstonePage, tombstoneSweepVersion, day, last)))
		}
		_, err := jobs.EnqueueMany(tx, specs)
		return err
	})
}
