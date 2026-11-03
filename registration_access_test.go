package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"fold/internal/audit"
	"fold/internal/notify"
	"fold/internal/policy"
	"fold/models"

	"gorm.io/gorm"
)

// accessRow is the test view of one registration's exact-date grant. The
// production model keeps the grant on the registration row (R9 S4); this struct
// gives the scenario tests the fields they assert on, with ID equal to the
// registration ID (the audit target and notice subject of a grant).
type accessRow struct {
	ID                    uint
	SubjectUserID         uint
	OccurrenceID          uint
	EventID               uint
	OrganizerType         string
	OrganizerID           uint
	Origin                string
	IssuerID              *uint
	RequestID             *uint
	RegistrationID        uint
	DepartureMembershipID *uint
	DepartureActorID      *uint
	Status                string
	GrantedAt             time.Time
	EndedAt               *time.Time
	EndReason             string
	EndedBy               *uint
}

func accessRowOf(r models.Registration, scope accessScope) accessRow {
	out := accessRow{ID: r.ID, RegistrationID: r.ID, OccurrenceID: r.OccurrenceID, EventID: scope.EventID,
		OrganizerType: scope.Organizer.Type, OrganizerID: scope.Organizer.ID, Origin: r.AccessOrigin,
		IssuerID: r.AccessIssuerID, RequestID: r.AccessRequestID, DepartureMembershipID: r.AccessDepartureMembershipID,
		DepartureActorID: r.AccessDepartureActorID, EndedAt: r.AccessEndedAt, EndReason: r.AccessEndReason, EndedBy: r.AccessEndedBy}
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
	return out
}

func loadAccessRow(t testing.TB, db *gorm.DB, registrationID uint) accessRow {
	t.Helper()
	var r models.Registration
	if err := db.First(&r, registrationID).Error; err != nil {
		t.Fatal(err)
	}
	scope, err := accessScopeIn(db, r.OccurrenceID)
	if err != nil {
		t.Fatal(err)
	}
	return accessRowOf(r, scope)
}

// seedAccessGrantIn starts a grant through the production writer (so the real
// trigger validates it and the added snapshot exists), at the given instant.
func seedAccessGrantIn(tx *gorm.DB, registrationID uint, g accessGrant, actor *uint, at time.Time) (accessRow, error) {
	var r models.Registration
	if err := tx.First(&r, registrationID).Error; err != nil {
		return accessRow{}, err
	}
	scope, err := accessScopeIn(tx, r.OccurrenceID)
	if err != nil {
		return accessRow{}, err
	}
	next, _, err := grantRegistrationAccess(tx, registrationID, scope, g, actor, at, "")
	if err != nil {
		return accessRow{}, err
	}
	return accessRowOf(next, scope), nil
}

func seedAccessGrant(t testing.TB, db *gorm.DB, registrationID uint, g accessGrant, actor *uint, at time.Time) accessRow {
	t.Helper()
	row, err := seedAccessGrantIn(db, registrationID, g, actor, at)
	if err != nil {
		t.Fatal("seed exact-date access:", err)
	}
	return row
}

// endSeededAccess ends a live grant through the production writer and returns
// the reloaded row.
func endSeededAccess(t testing.TB, db *gorm.DB, registrationID uint, reason string, actor *uint, at time.Time) accessRow {
	t.Helper()
	var r models.Registration
	if err := db.First(&r, registrationID).Error; err != nil {
		t.Fatal(err)
	}
	scope, err := accessScopeIn(db, r.OccurrenceID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := endRegistrationAccess(db, &r, scope, reason, actor, at, "", false); err != nil {
		t.Fatal("end exact-date access:", err)
	}
	return accessRowOf(r, scope)
}

// accessSnapshot is one decoded occurrence_access.added / .ended audit row. ID
// is the audit row's ID, the same one contact history shows as the entry ID.
type accessSnapshot struct {
	ID      uint
	Action  string
	ActorID *uint
	Meta    map[string]any
}

// accessSnapshotsFor returns a registration's access snapshots, oldest first.
// JSON numbers decode as float64; the stamps are below 2^53, so they are exact.
func accessSnapshotsFor(t testing.TB, db *gorm.DB, registrationID uint) []accessSnapshot {
	t.Helper()
	var rows []models.AuditLog
	if err := db.Where("target_type = ? AND target_id = ? AND action IN ?", accessTargetType, registrationID,
		[]string{audit.OccurrenceAccessAdded, audit.OccurrenceAccessEnded}).Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	out := make([]accessSnapshot, 0, len(rows))
	for _, row := range rows {
		meta := map[string]any{}
		if err := json.Unmarshal([]byte(row.MetaJSON), &meta); err != nil {
			t.Fatal(err)
		}
		out = append(out, accessSnapshot{ID: row.ID, Action: row.Action, ActorID: row.ActorID, Meta: meta})
	}
	return out
}

// metaUint reads a decoded snapshot number; an absent or non-number value is 0.
func metaUint(v any) uint {
	n, ok := v.(float64)
	if !ok {
		return 0
	}
	return uint(n)
}

func TestAccessInstantIsCanonicalMicroseconds(t *testing.T) {
	zone := time.FixedZone("far", 14*3600)
	in := time.Date(2026, 11, 3, 8, 30, 15, 123456789, zone)
	got := accessInstant(in)
	if got.Location() != time.UTC || got.Nanosecond() != 123456000 || !got.Equal(in.Truncate(time.Microsecond)) {
		t.Fatalf("accessInstant = %v", got)
	}
	if accessMicro(in) != got.UnixMicro() || !accessFromMicro(accessMicro(in)).Equal(got) {
		t.Fatalf("micro round trip: %d %v", accessMicro(in), accessFromMicro(accessMicro(in)))
	}
}

// The three-model fold keeps the baseline and the Go loader on one predicate.
func TestRegistrationAccessCorruptionPredicateMatchesSQL(t *testing.T) {
	raw, err := os.ReadFile("migrations/00001_baseline.sql")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	for _, key := range []string{"subjectUserId", "occurrenceId", "grantedAtMicro", "endedAtMicro"} {
		if !strings.Contains(src, historyMetaID(key)) {
			t.Errorf("registration_access_provenance() lacks the shared guarded predicate for %s:\n%s", key, historyMetaID(key))
		}
	}
	for _, scope := range []string{"'" + audit.OccurrenceAccessAdded + "', '" + audit.OccurrenceAccessEnded + "'", "l.target_type = '" + accessTargetType + "'"} {
		if !strings.Contains(src, scope) {
			t.Errorf("the baseline scope lacks %q", scope)
		}
	}
	if !strings.Contains(accessSnapshotValidSQL(), historyMetaID("endedAtMicro")) || !strings.Contains(accessSnapshotScopeSQL, accessTargetType) {
		t.Error("the Go scope and validity text drifted from the shared predicate")
	}
}

func TestAccessSnapshotsAreCompleteAndCanonical(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	added := accessSnapshotsFor(t, f.app.DB, f.access.ID)
	if len(added) != 1 || added[0].Action != audit.OccurrenceAccessAdded {
		t.Fatalf("seeded grant snapshots = %+v", added)
	}
	want := map[string]any{
		"subjectUserId": float64(f.recipient.ID), "occurrenceId": float64(f.dates[1].ID), "eventId": float64(f.event.ID),
		"organizerType": policy.ContactGroup, "organizerId": float64(f.group), "registrationId": float64(f.access.ID),
		"origin": policy.OccurrenceOriginManagerAdd, "issuerId": float64(f.issuer.ID), "requestId": float64(*f.access.RequestID),
		"grantedAtMicro": float64(f.access.GrantedAt.UTC().UnixMicro()),
	}
	for key, value := range want {
		if added[0].Meta[key] != value {
			t.Errorf("added snapshot %s = %v, want %v", key, added[0].Meta[key], value)
		}
	}
	for _, absent := range []string{"departureMembershipId", "departureActorId", "endedAtMicro", "endReason", "endedBy"} {
		if _, ok := added[0].Meta[absent]; ok {
			t.Errorf("added snapshot carries %s", absent)
		}
	}
	var stamp string
	if err := f.app.DB.Raw(`SELECT meta_json->>'grantedAtMicro' FROM audit_logs WHERE action = ? AND target_id = ?`, audit.OccurrenceAccessAdded, f.access.ID).Scan(&stamp).Error; err != nil ||
		stamp != strconv.FormatInt(f.access.GrantedAt.UTC().UnixMicro(), 10) {
		t.Fatalf("stored stamp %q (%v) is not the canonical integer", stamp, err)
	}

	apiDo(t, f.client, "POST", occurrenceMutationPath(f, "decline"), nil, 200)
	both := accessSnapshotsFor(t, f.app.DB, f.access.ID)
	row := loadAccessRow(t, f.app.DB, f.access.ID)
	if len(both) != 2 || both[1].Action != audit.OccurrenceAccessEnded || row.EndedAt == nil {
		t.Fatalf("snapshots after the end: %+v row=%+v", both, row)
	}
	ended := both[1]
	for key, value := range map[string]any{
		"endedAtMicro": float64(row.EndedAt.UTC().UnixMicro()), "endReason": policy.OccurrenceDeclined, "endedBy": float64(f.recipient.ID),
		"grantedAtMicro": want["grantedAtMicro"], "origin": want["origin"], "issuerId": want["issuerId"], "requestId": want["requestId"],
		"subjectUserId": want["subjectUserId"], "occurrenceId": want["occurrenceId"], "eventId": want["eventId"], "registrationId": want["registrationId"],
	} {
		if ended.Meta[key] != value {
			t.Errorf("ended snapshot %s = %v, want %v", key, ended.Meta[key], value)
		}
	}
	if ended.ActorID == nil || *ended.ActorID != f.recipient.ID {
		t.Errorf("ended snapshot actor = %v", ended.ActorID)
	}
}

// Same-row ordering with a frozen clock: an equal-microsecond or backward
// replacement fails and leaves no committed grant, audit or notice; a later one
// succeeds. Nothing advances the clock to manufacture success.
func TestAccessReplacementMustMoveForwardOnTheSameRow(t *testing.T) {
	f := newDepartureTestFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	occ, reg := addDepartureRegistration(t, f, f.subject, "Ordering date", models.VisibilityPublic, nil, now.Add(4*time.Hour), now.Add(5*time.Hour), models.OccurrenceScheduled, models.RegGoing)
	var membership models.Membership
	if err := f.app.DB.Where("user_id = ? AND group_id = ?", f.subject.ID, f.group.ID).First(&membership).Error; err != nil {
		t.Fatal(err)
	}
	scope, err := accessScopeIn(f.app.DB, occ.ID)
	if err != nil {
		t.Fatal(err)
	}
	subjectID, leaderID := f.subject.ID, f.leader.ID
	self := accessGrant{Origin: policy.OccurrenceOriginDepartureSelf, DepartureMembershipID: &membership.ID, DepartureActorID: &subjectID}
	removal := accessGrant{Origin: policy.OccurrenceOriginDepartureRemoval, DepartureMembershipID: &membership.ID, DepartureActorID: &leaderID}
	first := seedAccessGrant(t, f.app.DB, reg.ID, self, &subjectID, now)
	endSeededAccess(t, f.app.DB, reg.ID, policy.OccurrenceDeclined, &subjectID, now.Add(time.Second))
	attempt := func(at time.Time) error {
		return f.app.DB.Transaction(func(tx *gorm.DB) error {
			_, _, err := grantRegistrationAccess(tx, reg.ID, scope, removal, &leaderID, at, "")
			return err
		})
	}
	snapshots := func() int { return len(accessSnapshotsFor(t, f.app.DB, reg.ID)) }
	notices := func() int64 { return contactCount(t, f.app, &models.Notification{}, "1 = 1") }
	beforeSnapshots, beforeNotices := snapshots(), notices()
	for name, at := range map[string]time.Time{
		"equal to the old grant stamp": now, "before the old grant stamp": now.Add(-time.Second), "between the old grant and its end": now.Add(500 * time.Millisecond),
	} {
		if err := attempt(at); !errors.Is(err, errAccessClockOrder) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if row := loadAccessRow(t, f.app.DB, reg.ID); row.Status != policy.OccurrenceAccessEnded || !row.GrantedAt.Equal(first.GrantedAt) || snapshots() != beforeSnapshots || notices() != beforeNotices {
		t.Fatalf("a refused replacement left effects: %+v", row)
	}
	if err := attempt(now.Add(2 * time.Second)); err != nil {
		t.Fatalf("a later replacement was refused: %v", err)
	}
	row := loadAccessRow(t, f.app.DB, reg.ID)
	if row.Status != policy.OccurrenceAccessActive || row.Origin != policy.OccurrenceOriginDepartureRemoval || !row.GrantedAt.Equal(now.Add(2*time.Second)) || snapshots() != beforeSnapshots+1 {
		t.Fatalf("replacement not recorded: %+v", row)
	}
}

// A real-clock grant, end, re-grant, end inside one second makes two added and
// two ended notices keyed by registration and stamp; a retry adds none, and an
// old notice for the same registration never suppresses a new grant's notice.
func TestAccessNoticesAreKeyedByRegistrationAndGrantStamp(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	datePath := dateReadPath(f.event.ID, models.FormatCalendarDate(f.dates[0].OriginalDate))
	for round := 0; round < 2; round++ {
		r := createOccurrenceAsk(t, f, f.other, f.dates[0], nil)
		if w := addDecision(f.stranger, r.ID, "subject", "approved"); w.Code != 200 {
			t.Fatalf("round %d approval: %d %s", round, w.Code, w.Body.String())
		}
		apiDo(t, f.stranger, "POST", datePath+"/decline", nil, 200)
	}
	var reg models.Registration
	if err := f.app.DB.Where("user_id = ? AND occurrence_id = ?", f.other.ID, f.dates[0].ID).First(&reg).Error; err != nil {
		t.Fatal(err)
	}
	var rows []models.Notification
	if err := f.app.DB.Where("user_id = ? AND kind IN ?", f.other.ID, []string{notify.OccurrenceAdded, notify.OccurrenceUpdated}).Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	keys, added, ended := map[string]bool{}, 0, 0
	for _, n := range rows {
		if n.DedupeKey == nil || n.SubjectType != accessTargetType || n.SubjectID == nil || *n.SubjectID != reg.ID {
			t.Fatalf("notice not keyed to the registration: %+v", n)
		}
		keys[*n.DedupeKey] = true
		switch n.Kind {
		case notify.OccurrenceAdded:
			added++
			if !strings.HasPrefix(*n.DedupeKey, "occurrence-added:"+itoa(reg.ID)+":") {
				t.Fatalf("added key %q", *n.DedupeKey)
			}
		case notify.OccurrenceUpdated:
			ended++
			if !strings.HasPrefix(*n.DedupeKey, "occurrence-ended:"+itoa(reg.ID)+":") || strings.Count(*n.DedupeKey, ":") != 3 {
				t.Fatalf("ended key %q", *n.DedupeKey)
			}
		}
	}
	if added != 2 || ended != 2 || len(keys) != 4 {
		t.Fatalf("notices added=%d ended=%d distinct keys=%d, want 2, 2 and 4", added, ended, len(keys))
	}
	// Retrying a committed end changes nothing: no row, no audit, no notice.
	beforeAudits, beforeNotices := auditCount(t, f.app, audit.OccurrenceAccessEnded), contactCount(t, f.app, &models.Notification{}, "1 = 1")
	scope, err := accessScopeIn(f.app.DB, reg.OccurrenceID)
	if err != nil {
		t.Fatal(err)
	}
	err = f.app.DB.Transaction(func(tx *gorm.DB) error {
		ids, err := endRegistrationAccess(tx, &reg, scope, policy.OccurrenceRemoved, &f.issuer.ID, time.Now().UTC(), "", true)
		if len(ids) != 0 {
			t.Errorf("a retried end notified %v", ids)
		}
		return err
	})
	if err != nil || auditCount(t, f.app, audit.OccurrenceAccessEnded) != beforeAudits || contactCount(t, f.app, &models.Notification{}, "1 = 1") != beforeNotices {
		t.Fatalf("a retried end had effects: %v", err)
	}
}

// A registration row can only be hard-deleted once its grant has ended, so each
// of the three delete paths ends live grants with an explicit reason first and
// leaves the snapshots behind.
func TestHardDeletePathsEndLiveGrantsFirst(t *testing.T) {
	snapshotReasons := func(t *testing.T, db *gorm.DB, registrationID uint) []string {
		t.Helper()
		var reasons []string
		for _, s := range accessSnapshotsFor(t, db, registrationID) {
			if s.Action == audit.OccurrenceAccessEnded {
				reasons = append(reasons, fmt.Sprint(s.Meta["endReason"]))
			}
		}
		return reasons
	}
	t.Run("event deletion", func(t *testing.T) {
		f := newOccurrenceReadFixture(t)
		other := seedReadInvitation(t, f.app, f.issuer, f.recipient, f.event, f.dates[2])
		endSeededAccess(t, f.app.DB, other.ID, policy.OccurrenceDeclined, &f.recipient.ID, time.Now().UTC())
		if err := f.app.contactMutation(contactChange{EventID: f.event.ID}, func(tx *gorm.DB) error { return deleteEventRows(tx, []uint{f.event.ID}) }); err != nil {
			t.Fatal(err)
		}
		if contactCount(t, f.app, &models.Registration{}, "id IN ?", []uint{f.access.ID, other.ID}) != 0 {
			t.Fatal("registrations survived the event")
		}
		if got := snapshotReasons(t, f.app.DB, f.access.ID); len(got) != 1 || got[0] != policy.OccurrenceEventDeleted {
			t.Fatalf("live grant ended as %v, want event_deleted", got)
		}
		if got := snapshotReasons(t, f.app.DB, other.ID); len(got) != 1 || got[0] != policy.OccurrenceDeclined {
			t.Fatalf("already-ended grant ended again as %v, want its own declined", got)
		}
		if contactCount(t, f.app, &models.Notification{}, "kind = ?", notify.OccurrenceUpdated) != 0 {
			t.Fatal("deleting an event sent per-grant notices")
		}
	})
	t.Run("restoration drop", func(t *testing.T) {
		f := newOccurrenceReadFixture(t)
		err := f.app.DB.Transaction(func(tx *gorm.DB) error {
			_, err := f.app.restoreRegistrations(tx, f.recipient.ID, models.UserActive, f.issuer.ID, "", time.Now().UTC())
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if contactCount(t, f.app, &models.Registration{}, "id = ?", f.access.ID) != 0 {
			t.Fatal("an ineligible row was not dropped")
		}
		if got := snapshotReasons(t, f.app.DB, f.access.ID); len(got) != 1 || got[0] != policy.OccurrenceContextChanged {
			t.Fatalf("live grant ended as %v, want context_changed", got)
		}
		if contactCount(t, f.app, &models.Notification{}, "user_id = ? AND kind = ? AND dedupe_key LIKE ?", f.recipient.ID, notify.OccurrenceUpdated, "occurrence-ended:"+itoa(f.access.ID)+":%") != 1 {
			t.Fatal("the normal ended notice was not sent")
		}
	})
	t.Run("series undo", func(t *testing.T) {
		app := testApp(t)
		leader := newTestUser(t, app, "undo-delete-leader", "password1234", models.RoleMember)
		group := activeGroup(t, app, leader, "Undo delete", models.VisibilityPublic, "")
		actor := newTestUser(t, app, "undo-delete-actor", "password1234", models.RoleMember)
		addMember(t, app, actor, &group, models.MembershipMember)
		ev, occs := seedSeriesEvent(t, app, &group, leader.ID, nil, false, 1)
		client := loginAPI(t, app, "undo-delete-actor", "password1234")
		out := postSeriesAnswer(client, ev.ID, map[string]any{"scope": seriesScopeAllFuture, "status": models.RegGoing})
		if out == nil || out.UndoToken == "" {
			t.Fatal("series answer failed")
		}
		var reg models.Registration
		if err := app.DB.Where("user_id = ? AND occurrence_id = ?", actor.ID, occs[0].ID).First(&reg).Error; err != nil {
			t.Fatal(err)
		}
		issuer := leader.ID
		seedAccessGrant(t, app.DB, reg.ID, accessGrant{Origin: policy.OccurrenceOriginManagerAdd, IssuerID: &issuer}, &issuer, time.Now().UTC())
		if w := undoSeriesAnswer(client, ev.ID, out.UndoToken, true); w.Code != http.StatusOK {
			t.Fatalf("undo = %d %s", w.Code, w.Body.String())
		}
		if contactCount(t, app, &models.Registration{}, "id = ?", reg.ID) != 0 {
			t.Fatal("undo left the answer it created")
		}
		if got := snapshotReasons(t, app.DB, reg.ID); len(got) != 1 || got[0] != policy.OccurrenceContextChanged {
			t.Fatalf("live grant ended as %v, want context_changed", got)
		}
	})
}

// An audit or notice-enqueue failure anywhere in a departure rolls back the
// whole mutation, including the kept date's grant and its snapshot.
func TestDepartureSnapshotAndNoticeFailuresRollBackTheGrant(t *testing.T) {
	for _, scenario := range []string{"added snapshot", "notice enqueue"} {
		t.Run(scenario, func(t *testing.T) {
			f := newDepartureTestFixture(t)
			now := time.Now().UTC()
			keep, keepReg := addDepartureRegistration(t, f, f.subject, "Keep on failure", models.VisibilityPublic, nil, now.Add(4*time.Hour), now.Add(5*time.Hour), models.OccurrenceScheduled, models.RegGoing)
			preview := getDeparturePreview(t, f.subjectC, "/api/v1/groups/"+itoa(f.group.ID)+"/departure-preview", http.StatusOK)
			before := departureWriteCounts(t, f.app)
			name := "departure:snapshot_failure"
			if err := f.app.DB.Callback().Create().Before("gorm:create").Register(name, func(db *gorm.DB) {
				switch row := db.Statement.Dest.(type) {
				case *models.AuditLog:
					if scenario == "added snapshot" && row.Action == audit.OccurrenceAccessAdded {
						db.AddError(errors.New("injected snapshot failure"))
					}
				case *models.Notification:
					if scenario == "notice enqueue" && row.Kind == notify.MembershipLeft {
						db.AddError(errors.New("injected enqueue failure"))
					}
				}
			}); err != nil {
				t.Fatal(err)
			}
			w := confirmDeparturePreview(t, f.subjectC, "POST", "/api/v1/groups/"+itoa(f.group.ID)+"/leave", preview, []uint{keep.ID}, "")
			f.app.DB.Callback().Create().Remove(name)
			if w.Code != http.StatusInternalServerError {
				t.Fatalf("failure response: %d %s", w.Code, w.Body.String())
			}
			if after := departureWriteCounts(t, f.app); !mapsEqualCounts(before, after) || membershipStatus(f.app, f.subject, f.group) != "active" ||
				loadAccessRow(t, f.app.DB, keepReg.ID).Origin != "" {
				t.Fatalf("the failed departure left effects: before=%v after=%v", before, after)
			}
		})
	}
}

// createDateAsk sends an ask to add subject to one date through the real
// endpoint, as requester (who must be able to manage the event).
func createDateAsk(t *testing.T, app *App, client *apiClient, requester, subject *models.User, eventID, occurrenceID uint) addRequestJSON {
	t.Helper()
	if len(app.Config.SessionSecret) == 0 {
		app.Config.SessionSecret = []byte("occurrence-ask-test-secret")
	}
	selection, err := app.newPeopleSelection(requester.ID, subject.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	w := client.do("POST", "/api/v1/add-requests", map[string]any{"selectionId": selection, "contextType": "event", "contextId": eventID, "occurrenceId": occurrenceID}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create ask: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Request addRequestJSON `json:"request"`
	}
	decodeJSON(t, w, &body)
	return body.Request
}

// snapshotInstant reads a stamp out of a decoded access snapshot.
func snapshotInstant(t *testing.T, s accessSnapshot, key string) time.Time {
	t.Helper()
	micro, ok := s.Meta[key].(float64)
	if !ok || micro <= 0 {
		t.Fatalf("snapshot %d has no %s: %+v", s.ID, key, s.Meta)
	}
	return accessFromMicro(int64(micro))
}

// refuseStaleAsk is a deliberate refusal check. Production never leaves a
// pending ask that predates the person's latest end (it can only be created
// after that end, and reconciliation closes any ask a live grant overtakes), so
// this plants one with an INSERT (created_at cannot be updated) and proves the
// real decision endpoint still turns the approval down and changes nothing. The
// caller's client must hold a valid session, or the refusal would be the
// session's and not the ask's.
func refuseStaleAsk(t *testing.T, app *App, requester, subject *models.User, subjectClient *apiClient, eventID, occurrenceID uint, latestEnd time.Time) {
	t.Helper()
	stale := models.ContactRequest{RequesterID: requester.ID, SubjectUserID: subject.ID, Purpose: policy.RequestOccurrenceAdd,
		ContextType: policy.ContactEvent, ContextID: eventID, OccurrenceID: &occurrenceID, AddAttendanceMode: models.AttendanceInPerson,
		SubjectDecision: policy.ContactPending, AdminDecision: policy.ContactNotNeeded, Status: policy.ContactPending,
		CreatedAt: latestEnd.Add(-time.Hour), ExpiresAt: time.Now().UTC().Add(7 * 24 * time.Hour)}
	if err := app.DB.Create(&stale).Error; err != nil {
		t.Fatal(err)
	}
	snapshots := func() int64 {
		return contactCount(t, app, &models.AuditLog{}, "action IN ? AND target_type = ?", []string{audit.OccurrenceAccessAdded, audit.OccurrenceAccessEnded}, accessTargetType)
	}
	before := snapshots()
	if w := addDecision(subjectClient, stale.ID, "subject", "approved"); w.Code != http.StatusForbidden {
		t.Fatalf("a request created before the latest end was not refused: %d %s", w.Code, w.Body.String())
	}
	var saved models.ContactRequest
	if err := app.DB.First(&saved, stale.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Status != policy.ContactPending || saved.SubjectDecision != policy.ContactPending || saved.CompletedTargetID != nil ||
		contactCount(t, app, &models.Registration{}, "access_request_id = ?", stale.ID) != 0 || snapshots() != before {
		t.Fatalf("the refused stale ask left effects: %+v", saved)
	}
}

// subjectNotices returns a person's notices of one kind, oldest first.
func subjectNotices(t *testing.T, app *App, userID uint, kind string) []models.Notification {
	t.Helper()
	var rows []models.Notification
	if err := app.DB.Where("user_id = ? AND kind = ?", userID, kind).Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

// The whole production sequence behind the repeat and stale rules: a real ask
// grants access, an admin disables the account (the grant ends), re-enabling it
// cannot restore the registration (the date is invite-only and the person is not
// in the group), so the row is deleted. Only its two audit snapshots remain, and
// they alone keep a direct re-add refused on the person's fresh registration,
// refuse a request that predates the end, and let a new approved request grant on
// a newly allocated registration.
func TestRegistrationAccessRepeatRulesSurviveNotRestoredDeletion(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	subject, date := f.other, f.dates[0]
	day := models.FormatCalendarDate(date.OriginalDate)
	addPath := "/api/v1/events/" + itoa(f.event.ID) + "/registrations/add"

	first := createOccurrenceAsk(t, f, subject, date, nil)
	if w := addDecision(f.stranger, first.ID, "subject", "approved"); w.Code != http.StatusOK {
		t.Fatalf("first approval: %d %s", w.Code, w.Body.String())
	}
	var r1 models.Registration
	if err := f.app.DB.Where("access_request_id = ?", first.ID).First(&r1).Error; err != nil {
		t.Fatal(err)
	}
	if r1.AccessOrigin != policy.OccurrenceOriginManagerAdd || ptrUint(r1.UserID) != subject.ID || r1.OccurrenceID != date.ID || ptrUint(r1.AccessIssuerID) != f.issuer.ID || !r1.AccessLive() {
		t.Fatalf("first grant: %+v", r1)
	}

	// Disable: the grant ends, the registration stays.
	apiDo(t, f.admin, "PUT", "/api/v1/admin/users/"+itoa(subject.ID), map[string]any{"status": models.UserDisabled}, http.StatusOK)
	if row := loadAccessRow(t, f.app.DB, r1.ID); row.Status != policy.OccurrenceAccessEnded || row.EndReason != policy.ContactAccountDisabled {
		t.Fatalf("disable did not end the grant: %+v", row)
	}
	// Re-enable: the date is invite-only and the person is outside the group, so
	// the registration is not restored and is deleted with its ended grant.
	apiDo(t, f.admin, "PUT", "/api/v1/admin/users/"+itoa(subject.ID), map[string]any{"status": models.UserActive}, http.StatusOK)
	if contactCount(t, f.app, &models.Registration{}, "id = ?", r1.ID) != 0 {
		t.Fatal("the not-restored registration still exists")
	}
	if auditCount(t, f.app, audit.RegistrationNotRestored) != 1 || len(subjectNotices(t, f.app, subject.ID, notify.RegistrationNotRestored)) != 1 {
		t.Fatal("the not-restored audit or notice is missing")
	}
	kept := accessSnapshotsFor(t, f.app.DB, r1.ID)
	if len(kept) != 2 || kept[0].Action != audit.OccurrenceAccessAdded || kept[1].Action != audit.OccurrenceAccessEnded ||
		kept[1].Meta["endReason"] != policy.ContactAccountDisabled || kept[1].Meta["registrationId"] != float64(r1.ID) ||
		kept[0].Meta["requestId"] != float64(first.ID) || kept[1].Meta["requestId"] != float64(first.ID) || kept[1].Meta["issuerId"] != float64(f.issuer.ID) {
		t.Fatalf("history after the delete: %+v", kept)
	}
	ended := snapshotInstant(t, kept[1], "endedAtMicro")

	// The person comes back on a fresh registration (the date is now public).
	if err := f.app.DB.Model(&models.Event{}).Where("id = ?", f.event.ID).Update("visibility", models.VisibilityPublic).Error; err != nil {
		t.Fatal(err)
	}
	subjectC := loginAPI(t, f.app, subject.Name, "password1234")
	if code, body := registerAs(subjectC, f.event.ID, day, models.RegGoing, nil); code != http.StatusOK {
		t.Fatalf("fresh registration: %d %s", code, body)
	}
	var r2 models.Registration
	if err := f.app.DB.Where("user_id = ? AND occurrence_id = ?", subject.ID, date.ID).First(&r2).Error; err != nil {
		t.Fatal(err)
	}
	if r2.ID <= r1.ID || r2.AccessOrigin != "" {
		t.Fatalf("fresh registration: id %d (old %d), provenance %q", r2.ID, r1.ID, r2.AccessOrigin)
	}

	// A direct re-add is refused on history alone; a person with none is added.
	if w := f.admin.do("POST", addPath, map[string]any{"userId": subject.ID, "date": day}, true); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "date_add_approval_required") {
		t.Fatalf("direct re-add after the delete: %d %s", w.Code, w.Body.String())
	}
	if row := loadAccessRow(t, f.app.DB, r2.ID); row.Origin != "" || len(accessSnapshotsFor(t, f.app.DB, r2.ID)) != 0 {
		t.Fatalf("a refused re-add changed the fresh registration: %+v", row)
	}
	control := newTestUser(t, f.app, "restoration control", "password1234", models.RoleMember)
	apiDo(t, f.admin, "POST", addPath, map[string]any{"userId": control.ID, "date": day}, http.StatusOK)

	// A request that predates the latest end is refused with a working session.
	refuseStaleAsk(t, f.app, f.issuer, subject, subjectC, f.event.ID, date.ID, ended)

	// A new ask, created after the end, is approved and grants on the fresh row.
	fresh := createOccurrenceAsk(t, f, subject, date, nil)
	if w := addDecision(subjectC, fresh.ID, "subject", "approved"); w.Code != http.StatusOK {
		t.Fatalf("fresh approval: %d %s", w.Code, w.Body.String())
	}
	row := loadAccessRow(t, f.app.DB, r2.ID)
	if row.Status != policy.OccurrenceAccessActive || row.Origin != policy.OccurrenceOriginManagerAdd || ptrUint(row.RequestID) != fresh.ID || ptrUint(row.IssuerID) != f.issuer.ID {
		t.Fatalf("fresh grant: %+v", row)
	}
	added := accessSnapshotsFor(t, f.app.DB, r2.ID)
	if len(added) != 1 || added[0].Action != audit.OccurrenceAccessAdded || added[0].Meta["requestId"] != float64(fresh.ID) || added[0].Meta["registrationId"] != float64(r2.ID) ||
		snapshotInstant(t, added[0], "grantedAtMicro").Before(ended) {
		t.Fatalf("fresh snapshot: %+v", added)
	}
	if !reflect.DeepEqual(accessSnapshotsFor(t, f.app.DB, r1.ID), kept) {
		t.Fatal("the deleted registration's snapshots changed")
	}
	// Old requests stay used up.
	for _, path := range []string{"/decision", "/complete"} {
		var body any
		if path == "/decision" {
			body = map[string]string{"party": "subject", "decision": "approved"}
		}
		apiDo(t, subjectC, "POST", "/api/v1/add-requests/"+itoa(first.ID)+path, body, http.StatusForbidden)
	}
	for _, pair := range []struct{ id, target uint }{{first.ID, r1.ID}, {fresh.ID, r2.ID}} {
		var saved models.ContactRequest
		if err := f.app.DB.First(&saved, pair.id).Error; err != nil || saved.Status != policy.ContactGranted || ptrUint(saved.CompletedTargetID) != pair.target {
			t.Fatalf("request %d: %+v %v", pair.id, saved, err)
		}
	}
	// Two grants, two registrations, one end; one added notice for each.
	if contactCount(t, f.app, &models.AuditLog{}, "action = ? AND target_type = ? AND meta_json->>'subjectUserId' = ? AND meta_json->>'occurrenceId' = ?", audit.OccurrenceAccessAdded, accessTargetType, itoa(subject.ID), itoa(date.ID)) != 2 ||
		contactCount(t, f.app, &models.AuditLog{}, "action = ? AND target_type = ? AND meta_json->>'subjectUserId' = ? AND meta_json->>'occurrenceId' = ?", audit.OccurrenceAccessEnded, accessTargetType, itoa(subject.ID), itoa(date.ID)) != 1 {
		t.Fatal("history counts for the person and date are wrong")
	}
	notices := subjectNotices(t, f.app, subject.ID, notify.OccurrenceAdded)
	if len(notices) != 2 || notices[0].SubjectID == nil || notices[1].SubjectID == nil || *notices[0].SubjectID != r1.ID || *notices[1].SubjectID != r2.ID ||
		notices[0].DedupeKey == nil || notices[1].DedupeKey == nil || !strings.HasPrefix(*notices[0].DedupeKey, "occurrence-added:"+itoa(r1.ID)+":") ||
		!strings.HasPrefix(*notices[1].DedupeKey, "occurrence-added:"+itoa(r2.ID)+":") {
		t.Fatalf("added notices: %+v", notices)
	}
}

// The series-undo twin: undoing a series answer deletes its registrations, ending
// a live grant first (context_changed), and the snapshots alone keep the rules
// for the registration the person gets when they answer the series again. The
// grant is placed with the production writer because a real manager add changes
// the row, and undo refuses a row that changed since the answer.
func TestRegistrationAccessRepeatRulesSurviveSeriesUndoDeletion(t *testing.T) {
	app := testApp(t)
	leader := newTestUser(t, app, "relife leader", "password1234", models.RoleMember)
	group := activeGroup(t, app, leader, "Relife group", models.VisibilityPublic, "")
	actor := newTestUser(t, app, "relife actor", "password1234", models.RoleMember)
	control := newTestUser(t, app, "relife control", "password1234", models.RoleMember)
	addMember(t, app, actor, &group, models.MembershipMember)
	addMember(t, app, control, &group, models.MembershipMember)
	ev, occs := seedSeriesEvent(t, app, &group, leader.ID, nil, false, 2)
	leaderC := loginAPI(t, app, "relife leader", "password1234")
	actorC := loginAPI(t, app, "relife actor", "password1234")
	day := models.FormatCalendarDate(occs[0].OriginalDate)
	addPath := "/api/v1/events/" + itoa(ev.ID) + "/registrations/add"
	answer := func() *seriesRegistrationResponse {
		out := postSeriesAnswer(actorC, ev.ID, map[string]any{"scope": seriesScopeAllFuture, "status": models.RegGoing})
		if out == nil || out.UndoToken == "" {
			t.Fatal("series answer failed")
		}
		return out
	}
	registration := func() models.Registration {
		var r models.Registration
		if err := app.DB.Where("user_id = ? AND occurrence_id = ?", actor.ID, occs[0].ID).First(&r).Error; err != nil {
			t.Fatal(err)
		}
		return r
	}

	out := answer()
	r1 := registration()
	issuer := leader.ID
	seedAccessGrant(t, app.DB, r1.ID, accessGrant{Origin: policy.OccurrenceOriginManagerAdd, IssuerID: &issuer}, &issuer, time.Now().UTC())
	if w := undoSeriesAnswer(actorC, ev.ID, out.UndoToken, true); w.Code != http.StatusOK {
		t.Fatalf("undo: %d %s", w.Code, w.Body.String())
	}
	if contactCount(t, app, &models.Registration{}, "user_id = ?", actor.ID) != 0 {
		t.Fatal("undo left the answers it created")
	}
	kept := accessSnapshotsFor(t, app.DB, r1.ID)
	if len(kept) != 2 || kept[0].Action != audit.OccurrenceAccessAdded || kept[1].Action != audit.OccurrenceAccessEnded ||
		kept[1].Meta["endReason"] != policy.OccurrenceContextChanged || kept[1].Meta["registrationId"] != float64(r1.ID) || kept[1].Meta["issuerId"] != float64(issuer) {
		t.Fatalf("history after the undo: %+v", kept)
	}
	if contactCount(t, app, &models.Notification{}, "user_id = ? AND kind = ? AND dedupe_key LIKE ?", actor.ID, notify.OccurrenceUpdated, "occurrence-ended:"+itoa(r1.ID)+":%") != 1 {
		t.Fatal("the normal ended notice was not sent for the undone grant")
	}
	ended := snapshotInstant(t, kept[1], "endedAtMicro")

	// Answering the series again allocates new registrations.
	answer()
	r2 := registration()
	if r2.ID <= r1.ID || r2.AccessOrigin != "" {
		t.Fatalf("recreated registration: id %d (old %d), provenance %q", r2.ID, r1.ID, r2.AccessOrigin)
	}
	if w := leaderC.do("POST", addPath, map[string]any{"userId": actor.ID, "date": day}, true); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "date_add_approval_required") {
		t.Fatalf("direct re-add after the undo: %d %s", w.Code, w.Body.String())
	}
	if row := loadAccessRow(t, app.DB, r2.ID); row.Origin != "" || len(accessSnapshotsFor(t, app.DB, r2.ID)) != 0 {
		t.Fatalf("a refused re-add changed the recreated registration: %+v", row)
	}
	apiDo(t, leaderC, "POST", addPath, map[string]any{"userId": control.ID, "date": day}, http.StatusOK)

	refuseStaleAsk(t, app, leader, actor, actorC, ev.ID, occs[0].ID, ended)

	fresh := createDateAsk(t, app, leaderC, leader, actor, ev.ID, occs[0].ID)
	if w := addDecision(actorC, fresh.ID, "subject", "approved"); w.Code != http.StatusOK {
		t.Fatalf("fresh approval: %d %s", w.Code, w.Body.String())
	}
	row := loadAccessRow(t, app.DB, r2.ID)
	if row.Status != policy.OccurrenceAccessActive || ptrUint(row.RequestID) != fresh.ID || ptrUint(row.IssuerID) != leader.ID {
		t.Fatalf("fresh grant: %+v", row)
	}
	added := accessSnapshotsFor(t, app.DB, r2.ID)
	if len(added) != 1 || added[0].Action != audit.OccurrenceAccessAdded || added[0].Meta["requestId"] != float64(fresh.ID) ||
		snapshotInstant(t, added[0], "grantedAtMicro").Before(ended) {
		t.Fatalf("fresh snapshot: %+v", added)
	}
	if !reflect.DeepEqual(accessSnapshotsFor(t, app.DB, r1.ID), kept) {
		t.Fatal("the undone registration's snapshots changed")
	}
	notices := subjectNotices(t, app, actor.ID, notify.OccurrenceAdded)
	if len(notices) != 1 || notices[0].SubjectID == nil || *notices[0].SubjectID != r2.ID || notices[0].DedupeKey == nil ||
		!strings.HasPrefix(*notices[0].DedupeKey, "occurrence-added:"+itoa(r2.ID)+":") {
		t.Fatalf("added notices: %+v", notices)
	}
}
