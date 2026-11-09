package main

import (
	"errors"
	"strconv"
	"time"

	"fold/internal/audit"
	"fold/internal/notify"
	"fold/internal/policy"
	"fold/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Exact-date access provenance lives on the registration row (R9 S4, DEC-138).
// This file is the only writer of the access_* columns and of the audit
// snapshots that outlive them:
//
//   - grantRegistrationAccess starts a grant (a first one, or one that replaces
//     an ended grant on the same row) and writes occurrence_access.added.
//   - endRegistrationAccess (and endRegistrationAccessQuietly, which sends no
//     notice) ends a live grant for good and writes occurrence_access.ended with
//     the complete terminal state.
//
// registration_access_provenance() refuses to replace or delete provenance whose
// two snapshots are missing, and it reads the same snapshots for the repeat and
// stale-request rules, so a deleted or overwritten row never erases them.

var (
	// errAccessClockOrder stops a replacement that does not move forward on the
	// same row. The code never advances the clock to make it succeed.
	errAccessClockOrder = errors.New("exact-date access must move forward in time on the same registration")
	// errAccessHistoryCorrupt is the closed failure for a malformed access
	// snapshot; it is an internal error, never shown to a person.
	errAccessHistoryCorrupt = errors.New("occurrence access history is corrupt")
)

// accessTargetType is the audit target of the three access actions.
const accessTargetType = "registration"

// registrationAccessLiveSQL is models.Registration.AccessLive as a condition on
// the registrations table: provenance set, grant stamp present, not ended.
const registrationAccessLiveSQL = "registrations.access_origin <> '' AND registrations.access_granted_at IS NOT NULL AND registrations.access_ended_at IS NULL"

// registrationAccessColumns are written only by this file. Ordinary answer
// writes omit them, so a stale struct can never revert a grant or an end.
var registrationAccessColumns = []string{
	"access_origin", "access_issuer_id", "access_request_id", "access_departure_membership_id",
	"access_departure_actor_id", "access_granted_at", "access_ended_at", "access_end_reason", "access_ended_by",
}

// registrationSaveOmits lists what an ordinary Save of a registration leaves alone.
func registrationSaveOmits() []string {
	return append([]string{"User", "Occurrence"}, registrationAccessColumns...)
}

// accessInstant is the canonical operation time: UTC, whole microseconds, the
// precision Postgres stores. Every stamp, key and comparison uses it.
func accessInstant(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond) }

func accessMicro(t time.Time) int64 { return accessInstant(t).UnixMicro() }

func accessFromMicro(m int64) time.Time { return time.UnixMicro(m).UTC() }

// accessScope is what a grant's registration derives from its date: the event
// and its organizer (the group, or the event itself when church-wide).
type accessScope struct {
	EventID   uint
	Organizer policy.OrganizerScope
}

func accessScopeOf(ev *models.Event) accessScope {
	if ev.GroupID != nil {
		return groupAccessScope(ev.ID, *ev.GroupID)
	}
	return accessScope{EventID: ev.ID, Organizer: policy.OrganizerScope{Type: policy.ContactEvent, ID: ev.ID}}
}

func groupAccessScope(eventID, groupID uint) accessScope {
	return accessScope{EventID: eventID, Organizer: policy.OrganizerScope{Type: policy.ContactGroup, ID: groupID}}
}

// accessScopesIn derives the scope of each date from the live schema. It reads
// the event even when it is soft-deleted: ending a grant must still be able to
// name its event.
func accessScopesIn(tx *gorm.DB, occurrenceIDs []uint) (map[uint]accessScope, error) {
	out := map[uint]accessScope{}
	if len(occurrenceIDs) == 0 {
		return out, nil
	}
	var rows []struct {
		ID      uint
		EventID uint
		GroupID *uint
	}
	if err := tx.Raw("SELECT o.id, o.event_id, e.group_id FROM event_occurrences o JOIN events e ON e.id = o.event_id WHERE o.id IN ?", occurrenceIDs).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		s := accessScope{EventID: row.EventID, Organizer: policy.OrganizerScope{Type: policy.ContactEvent, ID: row.EventID}}
		if row.GroupID != nil {
			s.Organizer = policy.OrganizerScope{Type: policy.ContactGroup, ID: *row.GroupID}
		}
		out[row.ID] = s
	}
	return out, nil
}

func accessScopeIn(tx *gorm.DB, occurrenceID uint) (accessScope, error) {
	scopes, err := accessScopesIn(tx, []uint{occurrenceID})
	return scopes[occurrenceID], err
}

// occurrenceAccessPolicy adapts a registration's provenance to the pure policy
// struct. Status is synthesized from the end stamp: there is no status column.
func occurrenceAccessPolicy(r models.Registration, scope accessScope) policy.OccurrenceAccess {
	out := policy.OccurrenceAccess{OccurrenceID: r.OccurrenceID, EventID: scope.EventID, Organizer: scope.Organizer,
		Origin: r.AccessOrigin, RegistrationID: r.ID, EndedAt: r.AccessEndedAt}
	if r.UserID != nil {
		out.SubjectUserID = *r.UserID
	}
	if r.AccessGrantedAt != nil {
		out.GrantedAt = *r.AccessGrantedAt
	}
	switch {
	case r.AccessOrigin == "":
	case r.AccessEndedAt == nil:
		out.Status = policy.OccurrenceAccessActive
	default:
		out.Status = policy.OccurrenceAccessEnded
	}
	out.IssuerID, out.RequestID = ptrUint(r.AccessIssuerID), ptrUint(r.AccessRequestID)
	out.DepartureMembershipID, out.DepartureActorID = ptrUint(r.AccessDepartureMembershipID), ptrUint(r.AccessDepartureActorID)
	return out
}

// accessSnapshotMeta is the complete, immutable audit record of a grant (and,
// once ended, of its end). Optional IDs are present only when set; the stamps
// are canonical integer microseconds, the only timestamps read for authority.
func accessSnapshotMeta(r models.Registration, scope accessScope, ended bool) map[string]any {
	m := map[string]any{
		"subjectUserId": ptrUint(r.UserID), "occurrenceId": r.OccurrenceID, "eventId": scope.EventID,
		"organizerType": scope.Organizer.Type, "organizerId": scope.Organizer.ID,
		"registrationId": r.ID, "origin": r.AccessOrigin,
	}
	if r.AccessGrantedAt != nil {
		m["grantedAtMicro"] = accessMicro(*r.AccessGrantedAt)
	}
	optional := func(key string, v *uint) {
		if v != nil {
			m[key] = *v
		}
	}
	optional("issuerId", r.AccessIssuerID)
	optional("requestId", r.AccessRequestID)
	optional("departureMembershipId", r.AccessDepartureMembershipID)
	optional("departureActorId", r.AccessDepartureActorID)
	if ended {
		if r.AccessEndedAt != nil {
			m["endedAtMicro"] = accessMicro(*r.AccessEndedAt)
		}
		m["endReason"] = r.AccessEndReason
		optional("endedBy", r.AccessEndedBy)
		m["outcome"] = policy.OccurrenceAccessEnded
	}
	return m
}

func writeAccessSnapshot(tx *gorm.DB, action string, actor *uint, r models.Registration, scope accessScope, ended bool, ip string) error {
	return audit.Write(tx, audit.Entry{Actor: actor, Action: action, TargetType: accessTargetType, TargetID: r.ID, IP: ip,
		Meta: accessSnapshotMeta(r, scope, ended)})
}

// Dedupe keys are built only from persisted values, so a re-grant on the same
// row and a retry of a committed transition are told apart exactly.
func accessAddedKey(r models.Registration) string {
	return "occurrence-added:" + strconv.FormatUint(uint64(r.ID), 10) + ":" + strconv.FormatInt(accessMicro(*r.AccessGrantedAt), 10)
}

func accessEndedKey(r models.Registration) string {
	return "occurrence-ended:" + strconv.FormatUint(uint64(r.ID), 10) + ":" + strconv.FormatInt(accessMicro(*r.AccessGrantedAt), 10) +
		":" + strconv.FormatInt(accessMicro(*r.AccessEndedAt), 10)
}

func optionalUint(v *uint) any {
	if v == nil {
		return nil
	}
	return *v
}

type accessGrant struct {
	Origin                string
	IssuerID              *uint
	RequestID             *uint
	DepartureMembershipID *uint
	DepartureActorID      *uint
}

// grantRegistrationAccess starts a grant on one registration at now and writes
// its added snapshot, all in the caller's transaction. A live grant is left
// untouched (changed=false). A grant that replaces an ended one must move
// forward on this row: a later stamp, no earlier than the old end. A violation
// is an error that rolls the caller back; nothing here advances the clock.
func grantRegistrationAccess(tx *gorm.DB, registrationID uint, scope accessScope, g accessGrant, actor *uint, now time.Time, ip string) (models.Registration, bool, error) {
	now = accessInstant(now)
	var cur models.Registration
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&cur, registrationID).Error; err != nil {
		return cur, false, err
	}
	if cur.AccessLive() {
		return cur, false, nil
	}
	if cur.AccessGrantedAt != nil && (!now.After(*cur.AccessGrantedAt) || (cur.AccessEndedAt != nil && now.Before(*cur.AccessEndedAt))) {
		return cur, false, errAccessClockOrder
	}
	res := tx.Model(&models.Registration{}).Where("id = ?", registrationID).UpdateColumns(map[string]any{
		"access_origin": g.Origin, "access_issuer_id": optionalUint(g.IssuerID), "access_request_id": optionalUint(g.RequestID),
		"access_departure_membership_id": optionalUint(g.DepartureMembershipID), "access_departure_actor_id": optionalUint(g.DepartureActorID),
		"access_granted_at": now, "access_ended_at": nil, "access_end_reason": "", "access_ended_by": nil,
	})
	if res.Error != nil {
		return cur, false, res.Error
	}
	if res.RowsAffected != 1 {
		return cur, false, gorm.ErrRecordNotFound
	}
	var next models.Registration
	if err := tx.First(&next, registrationID).Error; err != nil {
		return cur, false, err
	}
	if next.AccessGrantedAt == nil || accessMicro(*next.AccessGrantedAt) != accessMicro(now) {
		return cur, false, errors.New("exact-date access stamp was not persisted")
	}
	if err := writeAccessSnapshot(tx, audit.OccurrenceAccessAdded, actor, next, scope, false, ip); err != nil {
		return cur, false, err
	}
	return next, true, nil
}

// endAccess ends a live grant (irreversibly) and writes its ended snapshot. It
// reloads *row and reports whether a live grant was ended: a grant that is not
// live is a no-op, which makes a retry add nothing.
func endAccess(tx *gorm.DB, row *models.Registration, scope accessScope, reason string, actor *uint, now time.Time, ip string) (bool, error) {
	if row.AccessOrigin == "" || row.UserID == nil {
		return false, nil
	}
	now = accessInstant(now)
	res := tx.Model(&models.Registration{}).Where("id = ? AND "+registrationAccessLiveSQL, row.ID).UpdateColumns(map[string]any{
		"access_ended_at": now, "access_end_reason": reason, "access_ended_by": optionalUint(actor),
	})
	if res.Error != nil {
		return false, res.Error
	}
	if res.RowsAffected == 0 {
		return false, nil
	}
	if err := tx.First(row, row.ID).Error; err != nil {
		return false, err
	}
	if err := writeAccessSnapshot(tx, audit.OccurrenceAccessEnded, actor, *row, scope, true, ip); err != nil {
		return false, err
	}
	return true, nil
}

func endRegistrationAccess(tx *gorm.DB, row *models.Registration, scope accessScope, reason string, actor *uint, now time.Time, ip string) error {
	ended, err := endAccess(tx, row, scope, reason, actor, now, ip)
	if err != nil || !ended {
		return err
	}
	var subject models.User
	if err := tx.Where("id = ? AND status = ? AND password_hash IS NOT NULL", *row.UserID, models.UserActive).Limit(1).Find(&subject).Error; err != nil {
		return err
	}
	if subject.ID == 0 {
		return nil
	}
	var occ models.EventOccurrence
	if err := tx.Where("id = ?", row.OccurrenceID).Limit(1).Find(&occ).Error; err != nil {
		return err
	}
	// Deleted dates have no valid navigation target; the retained audit is enough.
	if occ.ID == 0 {
		return nil
	}
	_, err = notify.Send(tx, notify.Msg{Kind: notify.OccurrenceUpdated, To: []uint{subject.ID}, SubjectType: accessTargetType, SubjectID: row.ID,
		Link: occurrenceDatePath(scope.EventID, models.FormatCalendarDate(occ.OriginalDate)), DedupeKey: accessEndedKey(*row)})
	return err
}

// endRegistrationAccessQuietly is endRegistrationAccess for a larger summarized
// operation, such as group departure or deleting dates: it keeps the irreversible
// end stamp and its audit snapshot without one recipient notice per date.
func endRegistrationAccessQuietly(tx *gorm.DB, row *models.Registration, scope accessScope, reason string, actor *uint, now time.Time, ip string) error {
	_, err := endAccess(tx, row, scope, reason, actor, now, ip)
	return err
}

// endLiveAccessOnDates ends every live grant on the given dates with an explicit
// reason before the caller hard-deletes the registrations, so the delete trigger
// finds an ended grant and its snapshots. Rows already ended keep their original
// reason. It runs while the dates still exist, so the scope can be read.
func endLiveAccessOnDates(tx *gorm.DB, occurrenceIDs []uint, reason string, now time.Time) error {
	if len(occurrenceIDs) == 0 {
		return nil
	}
	var rows []models.Registration
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("occurrence_id IN ? AND user_id IS NOT NULL AND "+registrationAccessLiveSQL, occurrenceIDs).Order("id").Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	scopes, err := accessScopesIn(tx, occurrenceIDs)
	if err != nil {
		return err
	}
	for i := range rows {
		if err := endRegistrationAccessQuietly(tx, &rows[i], scopes[rows[i].OccurrenceID], reason, nil, now, ""); err != nil {
			return err
		}
	}
	return nil
}

// accessHistory is what the validated snapshots say about one subject and date,
// across every registration that ever held a grant for it.
type accessHistory struct {
	Granted   bool       // a validated occurrence_access.added snapshot exists
	LatestEnd *time.Time // the newest validated occurrence_access.ended stamp
}

type accessKey struct{ Subject, Occurrence uint }

const accessSnapshotScopeSQL = "l.action IN ('" + audit.OccurrenceAccessAdded + "', '" + audit.OccurrenceAccessEnded + "') AND l.target_type = '" + accessTargetType + "'"

// accessSnapshotValidSQL is the one corruption predicate, shared with
// registration_access_provenance(): subjectUserId, occurrenceId and
// grantedAtMicro are required, and an ended snapshot also needs endedAtMicro,
// each passing historyMetaID's exact positive-integer check. The baseline holds
// the same text (TestRegistrationAccessCorruptionPredicateMatchesSQL).
func accessSnapshotValidSQL() string {
	return "(" + historyMetaID("subjectUserId") + " IS NOT NULL AND " + historyMetaID("occurrenceId") + " IS NOT NULL AND " +
		historyMetaID("grantedAtMicro") + " IS NOT NULL AND (l.action <> '" + audit.OccurrenceAccessEnded + "' OR " +
		historyMetaID("endedAtMicro") + " IS NOT NULL))"
}

// loadAccessHistory reads the access snapshots for the given subjects and dates
// in one query. The same scan carries every corrupt row anywhere in the action
// and target scope (whatever its subject and date), so corruption fails closed
// before any matching row is used; valid rows are kept only for the given
// subjects and dates, so a healthy table returns just the matches.
func loadAccessHistory(tx *gorm.DB, subjects, occurrences []uint) (map[accessKey]accessHistory, error) {
	out := map[accessKey]accessHistory{}
	var rows []struct {
		Action     string
		Corrupt    bool
		Subject    *uint
		Occurrence *uint
		Ended      *int64
	}
	valid := accessSnapshotValidSQL()
	query := "SELECT l.action AS action, NOT " + valid + " AS corrupt, " + historyMetaID("subjectUserId") + " AS subject, " + historyMetaID("occurrenceId") + " AS occurrence, " +
		"CASE WHEN l.action = '" + audit.OccurrenceAccessEnded + "' THEN " + historyMetaID("endedAtMicro") + " END AS ended " +
		"FROM audit_logs l WHERE " + accessSnapshotScopeSQL + " AND (NOT " + valid +
		" OR ((" + historyMetaID("subjectUserId") + ") IN ? AND (" + historyMetaID("occurrenceId") + ") IN ?))"
	if err := tx.Raw(query, subjects, occurrences).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		if r.Corrupt {
			return nil, errAccessHistoryCorrupt
		}
	}
	for _, r := range rows {
		key := accessKey{*r.Subject, *r.Occurrence}
		h := out[key]
		if r.Action == audit.OccurrenceAccessAdded {
			h.Granted = true
		}
		if r.Ended != nil {
			t := accessFromMicro(*r.Ended)
			if h.LatestEnd == nil || t.After(*h.LatestEnd) {
				h.LatestEnd = &t
			}
		}
		out[key] = h
	}
	return out, nil
}

// accessAddFacts builds the two policy.AddFacts values from the subject's
// current registration (nil ID when none) and the validated history: the live
// test reads the row, never an audit entry; the latest end is the newer of the
// row's own end and every validated ended snapshot for the subject and date.
func accessAddFacts(row models.Registration, h accessHistory) (active bool, latestEnd *time.Time) {
	active = row.ID != 0 && row.AccessLive()
	latestEnd = h.LatestEnd
	if row.ID != 0 && row.AccessEndedAt != nil {
		own := accessInstant(*row.AccessEndedAt)
		if latestEnd == nil || own.After(*latestEnd) {
			latestEnd = &own
		}
	}
	return active, latestEnd
}
