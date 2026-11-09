package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"fold/internal/audit"
	"fold/internal/jobs"
	"fold/internal/notify"
	"fold/internal/policy"
	"fold/internal/testdb"
	"fold/models"

	"gorm.io/gorm"
)

// Every base table, every column, ordered by its primary key. This also covers
// tables added later, rather than silently proving only a hand-picked subset.
func tombstoneDatabase(t *testing.T, db *gorm.DB) map[string]string {
	t.Helper()
	var tables []struct{ Name, Ordering string }
	if err := db.Raw(`SELECT c.relname AS name,
		coalesce((SELECT string_agg(format('r.%I', a.attname), ', ' ORDER BY k.ord)
		FROM pg_index i CROSS JOIN LATERAL unnest(i.indkey) WITH ORDINALITY k(attnum, ord)
		JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = k.attnum
		WHERE i.indrelid = c.oid AND i.indisprimary), 'to_jsonb(r)') AS ordering
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p') ORDER BY c.relname`).Scan(&tables).Error; err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, table := range tables {
		var rows string
		q := fmt.Sprintf(`SELECT coalesce(jsonb_agg(to_jsonb(r) ORDER BY %s)::text, '[]') FROM %q r`, table.Ordering, table.Name)
		if err := db.Raw(q).Scan(&rows).Error; err != nil {
			t.Fatal(err)
		}
		out[table.Name] = rows
	}
	return out
}

func tombstoneSameDatabase(t *testing.T, before, after map[string]string) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("table count changed: %d -> %d", len(before), len(after))
	}
	for table, rows := range before {
		if after[table] != rows {
			t.Errorf("%s changed:\nbefore %s\nafter %s", table, rows, after[table])
		}
	}
}

func tombstoneLegacy(t *testing.T, db *gorm.DB, id uint) {
	t.Helper()
	// Emulate the old handler's loss of the address, preserving the personal
	// columns it forgot. Devices are intentionally left for L2 to remove.
	if err := db.Exec(`UPDATE users SET email = 'deleted-' || id || '@deleted.invalid', deleted_at = now() WHERE id = ?`, id).Error; err != nil {
		t.Fatal(err)
	}
}

func tombstoneRun(t *testing.T, a *App, id uint, version int) tombstoneReport {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	r, err := a.eraseTombstoneOnce(ctx, id, version)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func tombstoneMaintenanceDelta(t *testing.T, db *gorm.DB, before map[string]string, uid uint, version int) {
	t.Helper()
	after := tombstoneDatabase(t, db)
	for _, table := range []string{"audit_logs", "jobs"} {
		var old, now []map[string]any
		if err := json.Unmarshal([]byte(before[table]), &old); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(after[table]), &now); err != nil {
			t.Fatal(err)
		}
		oldIDs := map[any]map[string]any{}
		for _, row := range old {
			oldIDs[row["id"]] = row
		}
		added := 0
		for _, row := range now {
			if prev, ok := oldIDs[row["id"]]; ok {
				if !reflect.DeepEqual(row, prev) {
					t.Errorf("%s existing row %v changed", table, row["id"])
				}
				delete(oldIDs, row["id"])
				continue
			}
			added++
			if table == "audit_logs" {
				meta, ok := row["meta_json"].(map[string]any)
				if !ok || row["action"] != audit.UserErasureSwept || row["target_type"] != "user" || row["target_id"] != float64(uid) || row["actor_id"] != nil || row["ip"] != "" || len(meta) != 3 || meta["version"] != float64(version) || meta["householdsKept"] != float64(0) || meta["activityEntriesKept"] != float64(0) {
					t.Errorf("unexpected maintenance marker: %+v", row)
				}
			} else if row["kind"] != kindAvatarCleanup || !reflect.DeepEqual(row["payload"], map[string]any{"userId": float64(uid)}) {
				t.Errorf("unexpected maintenance job: %+v", row)
			}
		}
		if len(oldIDs) != 0 || (table == "audit_logs" && added != 1) || (table == "jobs" && added > 1) {
			t.Errorf("%s: removed %d rows, added %d", table, len(oldIDs), added)
		}
		delete(before, table)
		delete(after, table)
	}
	tombstoneSameDatabase(t, before, after)
}

func TestTombstoneFreshErasureChangesOnlyMaintenanceRows(t *testing.T) {
	s := newCoreErasureScene(t)
	db := s.app.DB
	u := newTestUser(t, s.app, "sweep-fresh", erasurePW, models.RoleMember)
	h := hrHouseholdOf(t, s, "Fresh family", "1 Fresh St")
	hrSeat(t, s, h, u.ID, models.HouseholdRelationAdult)
	hrPerson(t, s, h, "Fresh", models.HouseholdAdult, &u.ID, false)
	seedErasureTokens(t, db, u.ID, "fresh", "fresh-new@x.test")
	seedErasureCalendar(t, db, u.ID, "fresh-calendar")
	newPushDevice(t, s.app, u, "fresh-device")
	erasureSeedConsentEvidence(t, db, u.ID, "fresh-consent")
	inv := erasureInvitation(t, db, s.admin.ID, u.Email, true, nil)
	erasureAcceptedAudit(t, db, inv, fmt.Sprintf(`{"userId":%d}`, u.ID))
	erasureRequest(t, db, u.Email, models.RequestSelfSignup, models.RequestApproved, &u.ID, nil, &inv)
	eventID, occ, groupID := erasureEventFixture(t, s)
	var group models.Group
	if err := db.First(&group, groupID).Error; err != nil {
		t.Fatal(err)
	}
	addMember(t, s.app, u, &group, models.MembershipMember)
	erasureMakeRequest(t, db, policy.RequestOccurrenceAdd, policy.ContactDenied, s.admin.ID, u.ID, groupID, eventID, occ.ID, 1, "fresh ages")
	erasureMustCreate(t, db, &models.EventHost{EventID: eventID, UserID: u.ID, AddedBy: s.admin.ID})
	auditErasureLog(t, db, audit.UserEmailChanged, "user", u.ID, &u.ID, map[string]any{"oldEmail": "fresh-original@x.test", "newEmail": u.Email})
	s.ac.holdNotices = true
	s.erase(t, u.ID)
	if n := countRows(t, db, "memberships", "user_id = ?", u.ID); n != 0 {
		t.Fatalf("fresh deletion left %d memberships", n)
	}
	before := tombstoneDatabase(t, db)
	tombstoneRun(t, s.app, u.ID, 1)
	tombstoneMaintenanceDelta(t, db, before, u.ID, 1)
	before = tombstoneDatabase(t, db)
	tombstoneRun(t, s.app, u.ID, 1)
	tombstoneSameDatabase(t, before, tombstoneDatabase(t, db))
	// Both coalescing and adding a new cleanup are named maintenance effects.
	if err := db.Model(&models.Job{}).Where("kind = ? AND status = ?", kindAvatarCleanup, models.JobQueued).Update("status", models.JobDone).Error; err != nil {
		t.Fatal(err)
	}
	before = tombstoneDatabase(t, db)
	tombstoneRun(t, s.app, u.ID, 2)
	tombstoneMaintenanceDelta(t, db, before, u.ID, 2)
	before = tombstoneDatabase(t, db)
	tombstoneRun(t, s.app, u.ID, 1) // a newer numeric marker covers older versions
	tombstoneSameDatabase(t, before, tombstoneDatabase(t, db))
}

func TestTombstoneLegacyErasureScrubsRetainedIDLinks(t *testing.T) {
	s := newCoreErasureScene(t)
	db := s.app.DB
	u := newTestUser(t, s.app, "sweep-legacy", erasurePW, models.RoleMember)
	h := hrHouseholdOf(t, s, "Legacy family", "2 Legacy St")
	hrSeat(t, s, h, u.ID, models.HouseholdRelationAdult)
	person := hrPerson(t, s, h, "Legacy", models.HouseholdAdult, &u.ID, false)
	seedErasureTokens(t, db, u.ID, "legacy", "legacy-new@x.test")
	seedErasureCalendar(t, db, u.ID, "legacy-calendar")
	newPushDevice(t, s.app, u, "legacy-device")
	erasureSeedConsentEvidence(t, db, u.ID, "legacy-consent")
	inv := erasureInvitation(t, db, s.admin.ID, "legacy-original@x.test", true, &person)
	erasureAcceptedAudit(t, db, inv, fmt.Sprintf(`{"userId":%d}`, u.ID))
	req := erasureRequest(t, db, "legacy-original@x.test", models.RequestSelfSignup, models.RequestPending, &u.ID, nil, &inv)
	erasureSeedTasks(t, db, &models.AccountRequest{ID: req})
	tasksBefore := countRows(t, db, "notifications", "kind = ? AND subject_id = ? AND waiting AND resolved_at IS NULL", notify.AccountRequested, req)
	if tasksBefore == 0 {
		t.Fatal("legacy request has no open admin tasks to resolve")
	}
	eventID, occ, groupID := erasureEventFixture(t, s)
	erasureMakeRequest(t, db, policy.RequestOccurrenceAdd, policy.ContactDenied, s.admin.ID, u.ID, groupID, eventID, occ.ID, 1, "legacy ages")
	erasureMustCreate(t, db, &models.EventHost{EventID: eventID, UserID: u.ID, AddedBy: s.admin.ID})
	auditErasureLog(t, db, audit.UserEmailChanged, "user", u.ID, &u.ID, map[string]any{"oldEmail": "legacy-original@x.test", "newEmail": u.Email})
	if err := db.Model(u).Updates(map[string]any{"avatar_key": fmt.Sprintf("avatars/%d/old.png", u.ID), "display_name": "Legacy private", "phone": "555-1234", "timezone": "America/Boise"}).Error; err != nil {
		t.Fatal(err)
	}
	seedThrottleRows(t, db, ownedThrottleKeys(u.ID, heldAddress{}))
	tombstoneLegacy(t, db, u.ID)
	var before models.User
	if err := db.Unscoped().First(&before, u.ID).Error; err != nil {
		t.Fatal(err)
	}
	report := tombstoneRun(t, s.app, u.ID, 1)
	if report != (tombstoneReport{}) {
		t.Errorf("unexpected residue counts: %+v", report)
	}
	var after models.User
	if err := db.Unscoped().First(&after, u.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.Name != "" || after.DisplayName != "" || after.Phone != "" || after.Timezone != "" || after.AvatarKey != "" || after.PasswordHash != nil || after.BirthDate != nil || after.Status != models.UserDisabled || !after.AdministrativelyDisabled || after.SessionVersion != before.SessionVersion || after.Email != before.Email || after.LogoutVersion != before.LogoutVersion || !after.DeletedAt.Time.Equal(before.DeletedAt.Time) {
		t.Errorf("legacy tombstone not scrubbed without identity changes: %+v", after)
	}
	for _, table := range []string{"account_tokens", "calendar_tokens", "push_subscriptions", "household_accounts", "notifications", "event_hosts", "memberships"} {
		if n := countRows(t, db, table, "user_id = ?", u.ID); n != 0 {
			t.Errorf("%s left %d id-linked rows", table, n)
		}
	}
	hrAssertAnonymized(t, db, person, &u.ID)
	if home := hrHouseholdRow(t, db, h); home.Name != removedHouseholdName || home.Address != "" {
		t.Errorf("sole-subject household not scrubbed: %+v", home)
	}
	r := erasureRequestRow(t, db, req)
	if r.Status != models.RequestWithdrawn || r.Name != "" || r.Message != "" || r.DecisionNote != "" || r.Email != fmt.Sprintf("deleted-account_requests-%d@deleted.invalid", req) {
		t.Errorf("legacy request not scrubbed: %+v", r)
	}
	// Waiting identifies task notifications even after resolution. An open
	// task also has no resolved_at; kind scopes the request's numeric id.
	if n := countRows(t, db, "notifications", "kind = ? AND subject_id = ? AND waiting AND resolved_at IS NULL", notify.AccountRequested, req); n != 0 {
		t.Error("legacy request left a waiting admin task")
	}
	if n := countRows(t, db, "notifications", "kind = ? AND subject_id = ?", notify.AccountRequested, req); n != tasksBefore {
		t.Errorf("legacy request retained %d admin tasks, want %d", n, tasksBefore)
	}
	if n := countRows(t, db, "notifications", "kind = ? AND subject_id = ? AND waiting AND resolved_at IS NOT NULL AND resolved_by_id IS NULL AND resolution = 'withdrawn'", notify.AccountRequested, req); n != tasksBefore {
		t.Errorf("system withdrawal resolved %d of %d admin tasks", n, tasksBefore)
	}
	gotInvitation := erasureInvitationRow(t, db, inv)
	if gotInvitation.Email != fmt.Sprintf("deleted-invitations-%d@deleted.invalid", inv) || gotInvitation.TokenHash != fmt.Sprintf("erased-invitations-%d", inv) || gotInvitation.Name != "" || gotInvitation.Phone != "" || gotInvitation.AcceptedAt == nil || gotInvitation.HouseholdPersonID == nil || *gotInvitation.HouseholdPersonID != person {
		t.Errorf("id-linked invitation was not scrubbed with its provenance preserved: %+v", gotInvitation)
	}
	if n := countRows(t, db, "jobs", "kind = ? AND status = ? AND payload = ?::jsonb", kindAvatarCleanup, models.JobQueued, fmt.Sprintf(`{"userId":%d}`, u.ID)); n != 1 {
		t.Errorf("legacy blob maintenance jobs = %d, want 1", n)
	}
	if n := countRows(t, db, "consents", "user_id = ? AND (ip <> '' OR user_agent <> '' OR evidence_text <> '')", u.ID); n != 0 {
		t.Error("legacy consent evidence survived")
	}
	if n := countRows(t, db, "contact_requests", "subject_user_id = ? AND add_childcare_ages_note <> ''", u.ID); n != 0 {
		t.Error("legacy ages note survived")
	}
	if n := countRows(t, db, "audit_logs", "action = ? AND (actor_id IS NOT NULL OR ip <> '' OR meta_json->>'reason' <> 'deleted_host_cleanup')", audit.EventHostRemoved); n != 0 {
		t.Error("stale-host removal was not a system action")
	}
	if n := countRows(t, db, "audit_logs", "target_type = 'user' AND target_id = ? AND (jsonb_exists(meta_json, 'oldEmail') OR jsonb_exists(meta_json, 'newEmail'))", u.ID); n != 0 {
		t.Error("legacy email audit metadata survived")
	}
	if n := countRows(t, db, "jobs", "kind = 'push.devices_off'"); n != 0 {
		t.Error("the user trigger queued an unwanted device notice")
	}
	if n := throttleCount(t, db, ownedThrottleKeys(u.ID, heldAddress{})); n != 0 {
		t.Error("legacy id throttle keys survived")
	}
	beforeDB := tombstoneDatabase(t, db)
	tombstoneRun(t, s.app, u.ID, 1)
	tombstoneSameDatabase(t, beforeDB, tombstoneDatabase(t, db))
	beforeDB = tombstoneDatabase(t, db)
	tombstoneRun(t, s.app, u.ID, 2)
	tombstoneMaintenanceDelta(t, db, beforeDB, u.ID, 2)
}

// A newer version traverses the helpers again. Seed dirty legacy
// rows first, prove their first-run transitions, then compare every table and
// column so an unconditional UPDATE or repeated notice cannot hide behind
// the same-version marker check.
func TestTombstoneLegacyVersionBumpChangesOnlyMaintenanceRows(t *testing.T) {
	s := newCoreErasureScene(t)
	db := s.app.DB
	u := newTestUser(t, s.app, "sweep-legacy-bump@x.test", erasurePW, models.RoleMember)
	h := hrHouseholdOf(t, s, "Legacy staged family", "7 Legacy St")
	hrSeat(t, s, h, u.ID, models.HouseholdRelationAdult)
	person := hrPerson(t, s, h, "Staged", models.HouseholdAdult, nil, false)
	inv := erasureInvitation(t, db, s.admin.ID, u.Email, true, &person)
	req := erasureRequest(t, db, u.Email, models.RequestHouseholdConversion, models.RequestApproved, &u.ID, &person, &inv)
	erasureAcceptedAudit(t, db, inv, fmt.Sprintf(`{"userId":%d}`, u.ID))
	seedErasureTokens(t, db, u.ID, "legacy-bump", "legacy-bump-new@x.test")
	seedErasureCalendar(t, db, u.ID, "legacy-bump-calendar")
	newPushDevice(t, s.app, u, "legacy-bump-device")
	erasureSeedConsentEvidence(t, db, u.ID, "legacy-bump-consent")
	group := newTestGroup(t, s.app, s.admin, "Legacy staged club", models.GroupActive)
	event, occ := seedEvent(t, s.app, group, s.admin.ID, hlFuture)
	eventID, groupID := event.ID, group.ID
	staged := models.Registration{OccurrenceID: occ.ID, HouseholdPersonID: &person, RegisteredBy: &s.admin.ID,
		Status: models.RegGoing, Source: models.RegSourceWeb, AttendanceMode: models.AttendanceInPerson,
		ManageTokenHash: "legacy-staged-manage", RemovalNote: "staged removal"}
	erasureMustCreate(t, db, &staged)
	owned := models.Registration{OccurrenceID: occ.ID, UserID: &u.ID,
		Status: models.RegGoing, Source: models.RegSourceGuestForm, AttendanceMode: models.AttendanceInPerson,
		ManageTokenHash: "legacy-owned-manage", ChildcareAgesNote: "owned ages", RemovalNote: "owned removal"}
	erasureMustCreate(t, db, &owned)
	erasureMakeRequest(t, db, policy.RequestOccurrenceAdd, policy.ContactDenied, s.admin.ID, u.ID, groupID, eventID, occ.ID, 1, "legacy bump ages")
	erasureMustCreate(t, db, &models.EventHost{EventID: eventID, UserID: u.ID, AddedBy: s.admin.ID})
	at := time.Now().Add(24 * time.Hour)
	message := models.Message{AuthorID: &u.ID, Status: models.MessageScheduled,
		Subject: "Legacy scheduled", BodyHTML: "<p>Legacy</p>", BodyText: "Legacy", ScheduledAt: &at}
	erasureMustCreate(t, db, &message)
	recipient := models.MessageRecipient{MessageID: message.ID, UserID: u.ID, Channel: models.DeliveryEmail,
		Address: u.Email, Error: "legacy recipient error", Status: models.RecipientQueued}
	erasureMustCreate(t, db, &recipient)
	// An address-only legacy signup has no retained id link. Even this
	// account's former address must stay untouched on both versions (O-17).
	signup := models.GuestSignup{Name: "Legacy address only", Email: u.Email, OccurrenceID: occ.ID,
		TokenHash: "legacy-bump-signup", ExpiresAt: at, IP: "203.0.113.8", Timezone: "UTC"}
	erasureMustCreate(t, db, &signup)
	keptSignup := dumpRows(t, db, "guest_signups", "id", "id = ?", signup.ID)
	tombstoneLegacy(t, db, u.ID)
	if report := tombstoneRun(t, s.app, u.ID, 1); report != (tombstoneReport{}) {
		t.Fatalf("unexpected legacy residue counts: %+v", report)
	}
	hrAssertAnonymized(t, db, person, nil) // never claim conversion completed
	if home := hrHouseholdRow(t, db, h); home.Name != removedHouseholdName || home.Address != "" {
		t.Error("legacy staged household was not scrubbed")
	}
	if n := countRows(t, db, "registrations", "id = ? AND user_id IS NULL AND registered_by = ? AND status = ? AND manage_token_hash = '' AND childcare_ages_note = '' AND removal_note = ''", staged.ID, s.admin.ID, models.RegCancelled); n != 1 {
		t.Error("staged registration was not cancelled and scrubbed with identity preserved")
	}
	if n := countRows(t, db, "notifications", "kind = ? AND user_id = ? AND subject_id = ? AND actor_id IS NULL", notify.RegistrationRemoved, s.admin.ID, occ.ID); n != 1 {
		t.Error("staged registration cancellation did not send one system notice to its living holder")
	}
	if n := countRows(t, db, "registrations", "id = ? AND manage_token_hash = '' AND childcare_ages_note = '' AND removal_note = ''", owned.ID); n != 1 {
		t.Error("id-linked registration was not scrubbed")
	}
	if n := countRows(t, db, "messages", "id = ? AND status = ?", message.ID, models.MessageCancelled); n != 1 {
		t.Error("legacy scheduled message was not cancelled")
	}
	if n := countRows(t, db, "audit_logs", "action = ? AND target_id = ? AND actor_id IS NULL AND ip = ''", audit.MessageCancelled, message.ID); n != 1 {
		t.Error("legacy message cancellation was not audited once as a system action")
	}
	if n := countRows(t, db, "message_recipients", "id = ? AND address = '' AND error = '' AND status = ? AND reason = ?", recipient.ID, models.RecipientSuppressed, skipNotActive); n != 1 {
		t.Error("legacy queued recipient was not suppressed and scrubbed")
	}
	if r := erasureRequestRow(t, db, req); r.Name != "" || r.Message != "" || r.DecisionNote != "" {
		t.Error("legacy conversion request was not scrubbed")
	}
	if got := erasureInvitationRow(t, db, inv); got.Name != "" || got.Phone != "" || got.TokenHash != fmt.Sprintf("erased-invitations-%d", inv) {
		t.Error("legacy id-linked invitation was not scrubbed")
	}
	if n := countRows(t, db, "consents", "user_id = ? AND (ip <> '' OR user_agent <> '' OR evidence_text <> '')", u.ID); n != 0 {
		t.Error("legacy consent evidence survived")
	}
	if got := dumpRows(t, db, "guest_signups", "id", "id = ?", signup.ID); got != keptSignup {
		t.Error("address-only legacy signup changed")
	}
	if n := countRows(t, db, "memberships", "user_id = ?", u.ID); n != 0 {
		t.Errorf("legacy tombstone left %d memberships", n)
	}
	before := tombstoneDatabase(t, db)
	tombstoneRun(t, s.app, u.ID, 1)
	tombstoneSameDatabase(t, before, tombstoneDatabase(t, db))
	before = tombstoneDatabase(t, db)
	tombstoneRun(t, s.app, u.ID, 2)
	tombstoneMaintenanceDelta(t, db, before, u.ID, 2)
	before = tombstoneDatabase(t, db)
	tombstoneRun(t, s.app, u.ID, 2)
	tombstoneSameDatabase(t, before, tombstoneDatabase(t, db))
}

func TestTombstoneUnavailableAddressNeverBindsAnAddress(t *testing.T) {
	s := newCoreErasureScene(t)
	db := s.app.DB
	u := newTestUser(t, s.app, "sweep-unavailable", erasurePW, models.RoleMember)
	newTestUser(t, s.app, "living@x.test", erasurePW, models.RoleMember)
	_, occ, _ := erasureEventFixture(t, s)
	// Include the tombstone placeholder itself and the empty outbox address:
	// neither is evidence of a lost original address, even if rows match it.
	for i, addr := range []string{"living@x.test", fmt.Sprintf("deleted-%d@deleted.invalid", u.ID)} {
		erasureMustCreate(t, db, &models.GuestSignup{Email: addr, Name: "Living", OccurrenceID: occ.ID, TokenHash: fmt.Sprintf("unknown-%d", i), ExpiresAt: time.Now().Add(time.Hour)})
		erasureInvitation(t, db, s.admin.ID, addr, false, nil)
		erasureRequest(t, db, addr, models.RequestSelfSignup, models.RequestPending, nil, nil, nil)
		erasureMustCreate(t, db, &models.MailOutbox{ToAddress: addr, Subject: "Living", Body: "Keep this"})
	}
	erasureMustCreate(t, db, &models.MailOutbox{ToAddress: "", Subject: "Empty address", Body: "Keep this too"})
	tombstoneLegacy(t, db, u.ID)
	before := tombstoneDatabase(t, db)
	var mu sync.Mutex
	var binds []any
	var statements []string
	capture := func(tx *gorm.DB) {
		mu.Lock()
		binds = append(binds, tx.Statement.Vars...)
		statements = append(statements, tx.Statement.SQL.String())
		mu.Unlock()
	}
	const name = "test:tombstone_address_binds"
	if err := db.Callback().Raw().After("gorm:raw").Register(name, capture); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Raw().Remove(name) })
	if err := db.Callback().Row().After("gorm:row").Register(name, capture); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Row().Remove(name) })
	if err := db.Transaction(func(tx *gorm.DB) error {
		subject := erasureSubject{ID: u.ID}
		// Poison the unavailable value to catch a helper that ignores ok.
		subject.Address.value = "poison@x.test"
		if err := captureErasureSetsIn(tx, &subject); err != nil {
			return err
		}
		if len(subject.Requests) != 0 || len(subject.Invitations) != 0 {
			t.Errorf("address-only rows selected: %+v", subject)
		}
		if err := eraseAccountRequestsIn(tx, subject, nil); err != nil {
			return err
		}
		if err := eraseDependentRecordsIn(tx, subject, nil, ""); err != nil {
			return err
		}
		if err := eraseConsentEvidenceIn(tx, subject); err != nil {
			return err
		}
		return eraseOwnedThrottleKeysIn(tx, subject)
	}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	for _, statement := range statements {
		if strings.Contains(statement, "email_address_optouts") {
			t.Error("unavailable address issued an address opt-out statement")
		}
	}
	for _, bind := range binds {
		if str, ok := bind.(string); ok && (str == "" || strings.Contains(str, "@")) {
			t.Errorf("unavailable address helper bound %q", str)
		}
	}
	mu.Unlock()
	tombstoneSameDatabase(t, before, tombstoneDatabase(t, db))
}

func TestTombstonePipelineNeverExportsAddressChoices(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		t.Run(fmt.Sprintf("fresh=%v", fresh), func(t *testing.T) {
			s := newCoreErasureScene(t)
			s.ac.holdNotices = true
			u := newTestUser(t, s.app, "no-tombstone-export@example.com", erasurePW, models.RoleMember)
			erasureSeedConsentEvidence(t, s.app.DB, u.ID, "no-address")
			if fresh {
				s.erase(t, u.ID)
				// A later turn-on by the new holder removes the record. A sweep
				// must not recreate it from the old holder's retained consents.
				holder := newTestUser(t, s.app, u.Email, erasurePW, models.RoleMember)
				c := loginAPI(t, s.app, holder.Email, erasurePW)
				c.holdNotices = true
				wantEmailSettingsStatus(t, c.do("PUT", "/api/v1/me/notification-settings", map[string]any{"category": "groups", "off": false}, true), http.StatusOK)
			} else {
				tombstoneLegacy(t, s.app.DB, u.ID)
			}
			before := retainedEmailChoices(t, s.app)
			seen := recordEmailOptOutStatements(t, s.app.DB)
			var mu sync.Mutex
			var addressBinds []string
			capture := func(tx *gorm.DB) {
				mu.Lock()
				defer mu.Unlock()
				for _, bind := range tx.Statement.Vars {
					if value, ok := bind.(string); ok && strings.Contains(value, "@") {
						addressBinds = append(addressBinds, value)
					}
				}
			}
			name := fmt.Sprintf("test:tombstone_pipeline_binds:%d", queryCounterSeq.Add(1))
			if err := s.app.DB.Callback().Raw().After("gorm:raw").Register(name, capture); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.app.DB.Callback().Raw().Remove(name) })
			if err := s.app.DB.Callback().Row().After("gorm:row").Register(name, capture); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.app.DB.Callback().Row().Remove(name) })
			for _, version := range []int{1, 1, 2} {
				tombstoneRun(t, s.app, u.ID, version)
			}
			if statements := seen(); len(statements) != 0 {
				t.Fatalf("tombstone pipeline issued address opt-out SQL: %+v", statements)
			}
			mu.Lock()
			if len(addressBinds) != 0 {
				t.Errorf("tombstone pipeline bound an address: %v", addressBinds)
			}
			mu.Unlock()
			if !reflect.DeepEqual(before, retainedEmailChoices(t, s.app)) {
				t.Fatal("tombstone sweep changed retained address choices")
			}
		})
	}
}

func TestTombstoneHouseholdMarkersDoNotSuppressOwnershipReevaluation(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverse=%v", reverse), func(t *testing.T) {
			s := newCoreErasureScene(t)
			u := newTestUser(t, s.app, "sweep-owner", erasurePW, models.RoleMember)
			v := newTestUser(t, s.app, "sweep-seat", erasurePW, models.RoleMember)
			h := hrHouseholdOf(t, s, "Two legacy seats", "3 Legacy St")
			hrSeat(t, s, h, u.ID, models.HouseholdRelationAdult)
			hrSeat(t, s, h, v.ID, models.HouseholdRelationAdult)
			person := hrPerson(t, s, h, "Owner", models.HouseholdAdult, &u.ID, false)
			tombstoneLegacy(t, s.app.DB, u.ID)
			tombstoneLegacy(t, s.app.DB, v.ID)
			first, second := u.ID, v.ID
			if reverse {
				first, second = second, first
			}
			if report := tombstoneRun(t, s.app, first, 1); report.HouseholdsKept != 1 {
				t.Fatalf("first seat must retain the household, got %+v", report)
			}
			if hrow := hrHouseholdRow(t, s.app.DB, h); hrow.Name != "Two legacy seats" || hrow.Address != "3 Legacy St" {
				t.Fatal("household scrubbed while another seat remained")
			}
			if report := tombstoneRun(t, s.app, second, 1); report.HouseholdsKept != 0 {
				t.Errorf("second sweep did not recognize the single erased owner: %+v", report)
			}
			if hrow := hrHouseholdRow(t, s.app.DB, h); hrow.Name != removedHouseholdName || hrow.Address != "" {
				t.Error("last seat removal did not scrub household of the already-marked owner")
			}
			hrAssertAnonymized(t, s.app.DB, person, &u.ID)
			before := tombstoneDatabase(t, s.app.DB)
			tombstoneRun(t, s.app, first, 1)
			tombstoneSameDatabase(t, before, tombstoneDatabase(t, s.app.DB))
		})
	}
}

func TestTombstoneSharedHouseholdRetainedAndCounted(t *testing.T) {
	s := newCoreErasureScene(t)
	u := newTestUser(t, s.app, "sweep-shared", erasurePW, models.RoleMember)
	h := hrHouseholdOf(t, s, "Living family", "4 Living St")
	hrSeat(t, s, h, u.ID, models.HouseholdRelationAdult)
	hrSeat(t, s, h, s.second.ID, models.HouseholdRelationAdult)
	tombstoneLegacy(t, s.app.DB, u.ID)
	before := dumpRows(t, s.app.DB, "households", "id", "id = ?", h)
	if r := tombstoneRun(t, s.app, u.ID, 1); r.HouseholdsKept != 1 {
		t.Errorf("shared-household count %+v", r)
	}
	if after := dumpRows(t, s.app.DB, "households", "id", "id = ?", h); after != before {
		t.Error("shared household changed")
	}
	if n := countRows(t, s.app.DB, "audit_logs", "action = ? AND target_id = ? AND meta_json->'householdsKept' = '1'::jsonb", audit.UserErasureSwept, u.ID); n != 1 {
		t.Error("durable marker did not report the retained household")
	}
}

func TestTombstoneHouseholdBusyRollsBackEverything(t *testing.T) {
	s := newCoreErasureScene(t)
	u := newTestUser(t, s.app, "sweep-busy", erasurePW, models.RoleMember)
	h := hrHouseholdOf(t, s, "Busy family", "5 Busy St")
	hrSeat(t, s, h, u.ID, models.HouseholdRelationAdult)
	tombstoneLegacy(t, s.app.DB, u.ID)
	before := tombstoneDatabase(t, s.app.DB)
	blocker := holdHousehold(t, s.app.DB, h)
	states := erasureTestSQLStates(t, s.app)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := s.app.eraseTombstoneOnce(ctx, u.ID, 1); !isHouseholdBusy(err) || !rollbackConfirmed(err) {
		t.Fatalf("NOWAIT must return a confirmed 55P03, got %v", err)
	}
	states.Only("55P03")
	tombstoneSameDatabase(t, before, tombstoneDatabase(t, s.app.DB))
	if err := blocker.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	tombstoneRun(t, s.app, u.ID, 1)
}

func TestTombstoneStagedPersonUsesSystemActorAndKeepsSafeguarding(t *testing.T) {
	f := newStagedFixture(t, true)
	db := f.s.app.DB
	before := dumpRows(t, db, "teen_consents", "id", "true")
	kept := dumpRows(t, db, "guardian_links", "id", "id IN ?", []uint{f.siblingLink, f.rejectedLink, f.revokedLink})
	tombstoneLegacy(t, db, f.teen.ID)
	if report := tombstoneRun(t, f.s.app, f.teen.ID, 1); report.HouseholdsKept != 1 {
		t.Errorf("parent's household must be kept: %+v", report)
	}
	if after := dumpRows(t, db, "teen_consents", "id", "true"); after != before {
		t.Error("safeguarding consent evidence changed")
	}
	if after := dumpRows(t, db, "guardian_links", "id", "id IN ?", []uint{f.siblingLink, f.rejectedLink, f.revokedLink}); after != kept {
		t.Error("another person's or finished guardian links changed")
	}
	if n := countRows(t, db, "guardian_links", "id = ? AND check_note = 'met the family' AND status = ?", f.teenLink, models.GuardianRevoked); n != 1 {
		t.Error("guardian evidence was lost or live link not revoked")
	}
	if n := countRows(t, db, "registrations", "id = ? AND user_id IS NULL AND registered_by = ? AND status = ?", f.personReg, f.parent.ID, models.RegCancelled); n != 1 {
		t.Error("staged registration was not cancelled with identity links preserved")
	}
	if n := countRows(t, db, "audit_logs", "action = ? AND actor_id IS NULL AND ip = ''", audit.GuardianLinkRevoked); n != 2 {
		t.Errorf("system guardian revocations = %d, want 2", n)
	}
	if n := countRows(t, db, "notifications", "kind = ? AND user_id = ? AND actor_id IS NULL", notify.RegistrationRemoved, f.parent.ID); n != 1 {
		t.Error("the surviving holder did not receive a system removal notice")
	}
	beforeDB := tombstoneDatabase(t, db)
	tombstoneRun(t, f.s.app, f.teen.ID, 2)
	// A version bump has no new domain work, even for a staged conversion.
	beforeDB["audit_logs"] = tombstoneDatabase(t, db)["audit_logs"]
	tombstoneSameDatabase(t, beforeDB, tombstoneDatabase(t, db))
}

func TestTombstoneStructuralMatchAndNumericMarker(t *testing.T) {
	for _, tc := range []struct {
		name, email, meta string
		deleted, skip     bool
	}{
		{name: "living account", deleted: false, skip: true},
		{name: "lookalike deleted row", email: "deleted-999999@deleted.invalid", deleted: true, skip: true},
		{name: "string version", deleted: true, meta: `{"version":"999"}`},
		{name: "null version", deleted: true, meta: `{"version":null}`},
		{name: "object version", deleted: true, meta: `{"version":{}}`},
		{name: "numeric marker", deleted: true, meta: `{"version":1}`, skip: true},
		{name: "newer numeric marker", deleted: true, meta: `{"version":2.5}`, skip: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newCoreErasureScene(t)
			u := newTestUser(t, s.app, "sweep-shape", erasurePW, models.RoleMember)
			if tc.deleted {
				tombstoneLegacy(t, s.app.DB, u.ID)
			}
			if tc.email != "" {
				if err := s.app.DB.Unscoped().Model(u).Update("email", tc.email).Error; err != nil {
					t.Fatal(err)
				}
			}
			if tc.meta != "" {
				erasureMustCreate(t, s.app.DB, &models.AuditLog{Action: audit.UserErasureSwept, TargetType: "user", TargetID: u.ID, MetaJSON: tc.meta})
			}
			before := tombstoneDatabase(t, s.app.DB)
			tombstoneRun(t, s.app, u.ID, 1)
			if tc.skip {
				tombstoneSameDatabase(t, before, tombstoneDatabase(t, s.app.DB))
			} else if n := countRows(t, s.app.DB, "audit_logs", "action = ? AND target_id = ? AND meta_json->'version' = '1'::jsonb", audit.UserErasureSwept, u.ID); n != 1 {
				t.Errorf("non-numeric marker suppressed work: %d", n)
			}
		})
	}
}

func TestTombstoneUsersTriggerCensus(t *testing.T) {
	s := newCoreErasureScene(t)
	var triggers []struct{ Name, Definition string }
	if err := s.app.DB.Raw(`SELECT tgname AS name, pg_get_triggerdef(oid) AS definition FROM pg_trigger
		WHERE tgrelid = 'users'::regclass AND NOT tgisinternal ORDER BY tgname`).Scan(&triggers).Error; err != nil {
		t.Fatal(err)
	}
	if len(triggers) != 1 || triggers[0].Name != "push_devices_end_with_sessions" || !strings.Contains(triggers[0].Definition, "AFTER UPDATE OF session_version, status") {
		t.Fatalf("revisit L2/L3 effects for every users trigger: %+v", triggers)
	}
}

func tombstoneDeadlineCall(r *ctxRig, budget time.Duration, uid uint) <-chan *ctxCall {
	ch := make(chan *ctxCall, 1)
	c := &ctxCall{start: time.Now()}
	c.deadline = c.start.Add(budget)
	go func() {
		ctx, cancel := context.WithDeadline(context.Background(), c.deadline)
		defer cancel()
		_, c.err = r.papp.eraseTombstoneOnce(ctx, uid, 1)
		c.returned = time.Now()
		ch <- c
	}()
	return ch
}

// tombstoneBlockedBudget is the deadline for a tombstone call the test blocks
// on purpose. It only has to exceed the time the erasure needs to reach the
// blocked statement (about a dozen statements, tens of milliseconds locally,
// several hundred on a slow CI runner).
const tombstoneBlockedBudget = 3 * time.Second

func TestTombstoneDeadlineDuringGateAndReconciliation(t *testing.T) {
	for _, gate := range []bool{true, false} {
		t.Run(fmt.Sprintf("gate=%v", gate), func(t *testing.T) {
			setContactBounds(t, 100*time.Millisecond, contactServerCap)
			f := newContactAPIFixture(t, false)
			req := f.create(t, "email", "")
			tombstoneLegacy(t, f.app.DB, f.subject.ID)
			r := newCtxRigFor(t, f.app)
			var database string
			if err := f.app.DB.Raw("SELECT current_database()").Scan(&database).Error; err != nil {
				t.Fatal(err)
			}
			var states *erasureTestStateLog
			r.papp.DB, states = tracedProxyDB(t, r.proxy, database)
			defer func() {
				if t.Failed() {
					for _, statement := range r.statements() {
						t.Logf("backend %d statement: %s", statement.PID, statement.SQL)
					}
				}
				states.mu.Lock()
				defer states.mu.Unlock()
				for _, failure := range states.entries {
					t.Logf("statement SQLSTATE=%q: %s", failure.Code, failure.SQL)
					if failure.Code == "40P01" {
						t.Error("unexpected deadlock during the deadline test")
					}
				}
			}()
			before := tombstoneDatabase(t, f.app.DB)
			blocker, blockerPID := beginOwnedTx(t, f.app.DB)
			if gate {
				if err := lockContacts(blocker); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := blocker.Exec("SELECT id FROM contact_requests WHERE id = ? FOR UPDATE", req.ID).Error; err != nil {
					t.Fatal(err)
				}
			}
			// The deadline is fixed when the call starts (the erasure reads it
			// before its first statement), so it cannot start only once the
			// call is blocked. The budget is instead long against the dozen
			// statements before the blocked one, so the call reaches it at any
			// machine speed; the wait for the deadline is the test's cost.
			ch := tombstoneDeadlineCall(r, tombstoneBlockedBudget, f.subject.ID)
			needle := "FROM \"contact_requests\""
			if gate {
				needle = "pg_advisory_xact_lock"
			}
			// Observe this test's blocker, the exact statement and the gate
			// separately. A wait earlier in the erasure is not reconciliation.
			pid := waiterOn(t, f.app.DB, blockerPID)
			if statement := statementOf(t, f.app.DB, pid); !strings.Contains(statement, needle) {
				t.Fatalf("backend %d blocked before the intended statement: %s", pid, statement)
			}
			if proxyPID := r.pidOf(needle); pid != proxyPID {
				t.Fatalf("blocked backend %d, proxied backend %d", pid, proxyPID)
			}
			requireContactGate(t, f.app.DB, pid, !gate, "tombstone erasure")
			c := r.await(ch)
			if classifyContactErr(c.err) != outcomeTimedOut || !errors.Is(c.err, context.DeadlineExceeded) {
				t.Fatalf("deadline returned %v", c.err)
			}
			if late := c.returned.Sub(c.deadline); late > time.Second {
				t.Errorf("returned %s after its deadline, want within 1s", late)
			}
			// Return is not release. Observe this backend separately, within
			// D + S + I; no assertion makes the response time a release proof.
			end := r.awaitReleased(pid, time.Until(c.deadline.Add(contactServerCap+300*time.Millisecond))+time.Second)
			t.Logf("returned %s after D; backend released %s after D", c.returned.Sub(c.deadline), end.Sub(c.deadline))
			if gate {
				for _, statement := range r.statements() {
					if strings.Contains(statement.SQL, "FROM users WHERE id") {
						t.Error("callback ran while the gate was held")
					}
				}
			}
			tombstoneSameDatabase(t, before, tombstoneDatabase(t, f.app.DB))
			if err := blocker.Rollback().Error; err != nil {
				t.Fatal(err)
			}
			if !r.gateFree() {
				t.Error("the contact gate remained held after both backends released")
			}
		})
	}
}

func TestTombstoneDefaultReserveRefusesBeforeBegin(t *testing.T) {
	s := newCoreErasureScene(t)
	u := newTestUser(t, s.app, "sweep-reserve", erasurePW, models.RoleMember)
	tombstoneLegacy(t, s.app.DB, u.ID)
	r := newCtxRigFor(t, s.app)
	before := tombstoneDatabase(t, s.app.DB)
	eventsBefore := len(r.proxy.Events())
	c := r.await(tombstoneDeadlineCall(r, 1500*time.Millisecond, u.ID))
	if !errors.Is(c.err, errCommitReserve) {
		t.Fatalf("default reserve: %v", c.err)
	}
	if len(r.proxy.Events()) != eventsBefore {
		t.Error("reserve refusal sent a database statement")
	}
	tombstoneSameDatabase(t, before, tombstoneDatabase(t, s.app.DB))
}

func TestTombstoneScrubRowHasNoTriggerEffects(t *testing.T) {
	s := newCoreErasureScene(t)
	u := newTestUser(t, s.app, "sweep-trigger", erasurePW, models.RoleMember)
	newPushDevice(t, s.app, u, "trigger-device")
	tombstoneLegacy(t, s.app.DB, u.ID)
	// Nullable legacy personal columns must also reach the fresh scrub's
	// actual empty values, rather than being missed by SQL's NULL logic.
	if err := s.app.DB.Exec(`UPDATE users SET name = NULL, display_name = NULL, phone = NULL,
		avatar_key = NULL, avatar_focal_x = NULL, avatar_focal_y = NULL WHERE id = ?`, u.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.app.DB.Transaction(func(tx *gorm.DB) error {
		if err := eraseAccountCredentialsIn(tx, erasureSubject{ID: u.ID}); err != nil {
			return err
		}
		before := tombstoneDatabase(t, tx)
		if err := scrubTombstoneRowIn(tx, u.ID); err != nil {
			return err
		}
		after := tombstoneDatabase(t, tx)
		delete(before, "users")
		delete(after, "users")
		tombstoneSameDatabase(t, before, after)
		if n := countRows(t, tx, "users", "id = ? AND name = '' AND display_name = '' AND phone = '' AND avatar_key = '' AND avatar_focal_x = 50 AND avatar_focal_y = 50 AND status = ?", u.ID, models.UserDisabled); n != 1 {
			t.Error("nullable legacy row did not reach the fresh scrub's values")
		}
		before = tombstoneDatabase(t, tx)
		if err := scrubTombstoneRowIn(tx, u.ID); err != nil {
			return err
		}
		tombstoneSameDatabase(t, before, tombstoneDatabase(t, tx))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTombstoneUnexpectedMembershipIsReportedWithoutHandoff(t *testing.T) {
	s := newCoreErasureScene(t)
	u := newTestUser(t, s.app, "sweep-membership", erasurePW, models.RoleMember)
	g := newTestGroup(t, s.app, s.admin, "Old leadership", models.GroupActive)
	addMember(t, s.app, u, g, models.MembershipLeader)
	tombstoneLegacy(t, s.app.DB, u.ID)
	before := dumpRows(t, s.app.DB, "memberships", "id", "true")
	if report := tombstoneRun(t, s.app, u.ID, 1); report.MembershipsKept != 1 {
		t.Errorf("unexpected membership not reported: %+v", report)
	}
	if after := dumpRows(t, s.app.DB, "memberships", "id", "true"); after != before {
		t.Error("maintenance changed memberships without a successor decision")
	}
}

func TestTombstoneContradictoryPersStopsBeforeAnyWrite(t *testing.T) {
	s := newCoreErasureScene(t)
	u := newTestUser(t, s.app, "sweep-integrity", erasurePW, models.RoleMember)
	h := hrHouseholdOf(t, s, "Ambiguous family", "6 Ambiguous St")
	hrSeat(t, s, h, u.ID, models.HouseholdRelationAdult)
	hrPerson(t, s, h, "First", models.HouseholdAdult, &u.ID, false)
	hrPerson(t, s, h, "Second", models.HouseholdAdult, &u.ID, true)
	tombstoneLegacy(t, s.app.DB, u.ID)
	before := tombstoneDatabase(t, s.app.DB)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	report, err := s.app.eraseTombstoneOnce(ctx, u.ID, 1)
	if !errors.Is(err, errErasureIntegrity) || report != (tombstoneReport{}) {
		t.Fatalf("contradictory PERS: report %+v, error %v", report, err)
	}
	tombstoneSameDatabase(t, before, tombstoneDatabase(t, s.app.DB))
}

func TestTombstoneDoesNotRepeatCutoffNotices(t *testing.T) {
	s := newCoreErasureScene(t)
	db := s.app.DB
	u := newTestUser(t, s.app, "sweep-cutoff", erasurePW, models.RoleMember)
	waiter := newTestUser(t, s.app, "sweep-waiter", erasurePW, models.RoleMember)
	g := newTestGroup(t, s.app, s.admin, "Cutoff club", models.GroupActive)
	addMember(t, s.app, u, g, models.MembershipMember)
	addMember(t, s.app, waiter, g, models.MembershipMember)
	ev, occ := seedEvent(t, s.app, g, s.admin.ID, time.Now().UTC().Add(time.Hour))
	if err := db.Model(&ev).Updates(map[string]any{"capacity": 1, "waitlist_enabled": true}).Error; err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-time.Hour)
	erasureMustCreate(t, db, &models.Registration{OccurrenceID: occ.ID, UserID: &u.ID, Status: models.RegGoing, Source: models.RegSourceWeb, AttendanceMode: models.AttendanceInPerson})
	erasureMustCreate(t, db, &models.Registration{OccurrenceID: occ.ID, UserID: &waiter.ID, Status: models.RegWaitlisted, WaitlistedAt: &at, Source: models.RegSourceWeb, AttendanceMode: models.AttendanceInPerson})
	s.ac.holdNotices = true
	s.erase(t, u.ID)
	if n := countRows(t, db, "notifications", "kind = ? AND subject_id = ?", notify.WaitlistCutoff, occ.ID); n != 1 {
		t.Fatalf("admin deletion must still send one cutoff notice, got %d", n)
	}
	before := tombstoneDatabase(t, db)
	tombstoneRun(t, s.app, u.ID, 1)
	tombstoneMaintenanceDelta(t, db, before, u.ID, 1)
	before = tombstoneDatabase(t, db)
	tombstoneRun(t, s.app, u.ID, 2)
	tombstoneMaintenanceDelta(t, db, before, u.ID, 2)
}

func TestTombstoneCommitErrorsAreReturnedAndMarkerDecidesTheRetry(t *testing.T) {
	for _, fault := range []testdb.CommitFault{testdb.CommitForwardThenClose, testdb.CommitCloseUnforwarded} {
		t.Run(fmt.Sprintf("commit=%d", fault), func(t *testing.T) {
			s := newCoreErasureScene(t)
			u := newTestUser(t, s.app, "sweep-commit", erasurePW, models.RoleMember)
			tombstoneLegacy(t, s.app.DB, u.ID)
			r := newCtxRigFor(t, s.app)
			r.proxy.Arm(testdb.Faults{Match: "SELECT id FROM users WHERE id", Commit: fault})
			c := r.await(tombstoneDeadlineCall(r, 2*time.Minute, u.ID))
			if !errors.Is(c.err, errContactCommit) || classifyContactErr(c.err) != outcomeUnknown {
				t.Fatalf("commit failure was hidden: %v", c.err)
			}
			pid := r.pidOf("SELECT id FROM users WHERE id")
			r.awaitReleased(pid, contactServerCap+2*time.Minute+time.Second)
			// A runner retry must work whether COMMIT reached PostgreSQL or
			// not; it never guesses the outcome from the transport error.
			tombstoneRun(t, s.app, u.ID, 1)
			if n := countRows(t, s.app.DB, "audit_logs", "action = ? AND target_id = ?", audit.UserErasureSwept, u.ID); n != 1 {
				t.Errorf("retry left %d sweep markers, want 1", n)
			}
			before := tombstoneDatabase(t, s.app.DB)
			tombstoneRun(t, s.app, u.ID, 1)
			tombstoneSameDatabase(t, before, tombstoneDatabase(t, s.app.DB))
		})
	}
}

func TestTombstoneNoopDoesNotHideAnUnacknowledgedRollback(t *testing.T) {
	s := newCoreErasureScene(t)
	u := newTestUser(t, s.app, "sweep-rollback", erasurePW, models.RoleMember)
	tombstoneLegacy(t, s.app.DB, u.ID)
	tombstoneRun(t, s.app, u.ID, 1)
	before := tombstoneDatabase(t, s.app.DB)
	r := newCtxRigFor(t, s.app)
	r.proxy.Arm(testdb.Faults{Match: "SELECT id FROM users WHERE id", Rollback: testdb.RollbackCloseUnforwarded})
	c := r.await(tombstoneDeadlineCall(r, 2*time.Minute, u.ID))
	var rollback *contactRollbackError
	if !errors.As(c.err, &rollback) || !errors.Is(c.err, errContactRollback) || rollbackConfirmed(c.err) {
		t.Fatalf("no-op hid an unacknowledged rollback: %v", c.err)
	}
	r.awaitReleased(r.pidOf("SELECT id FROM users WHERE id"), contactServerCap+2*time.Minute+time.Second)
	tombstoneSameDatabase(t, before, tombstoneDatabase(t, s.app.DB))
}

// The small page exercises the production transaction and INSERT; only the
// bound changes. Each runner still claims and finishes real persisted jobs.
func tombstoneRunner(t *testing.T, a *App, pageSize int) *jobs.Runner {
	t.Helper()
	kinds := a.erasureKinds()
	for i := range kinds {
		if kinds[i].Name == kindTombstonePage {
			kinds[i].Run = func(ctx context.Context, raw json.RawMessage) error {
				var p tombstonePageJob
				if err := json.Unmarshal(raw, &p); err != nil {
					return err
				}
				return a.runSweepPageSize(ctx, p.AfterID, p.Day, pageSize)
			}
		}
	}
	return a.newRunner(kinds...)
}

func tombstoneDrain(t *testing.T, a *App, r *jobs.Runner) {
	t.Helper()
	for i := 0; i < 100; i++ {
		// Keep unrelated avatar/notice delivery out of these runner assertions.
		if err := a.DB.Exec(`UPDATE jobs SET run_at = now() + interval '1 day'
   WHERE kind NOT IN (?, ?) AND status IN ('queued', 'failed')`, kindTombstone, kindTombstonePage).Error; err != nil {
			t.Fatal(err)
		}
		ran, err := r.RunOnce(context.Background(), "tombstone-test")
		if err != nil {
			t.Fatal(err)
		}
		if !ran {
			return
		}
	}
	t.Fatal("tombstone runner failed to drain within its bound")
}

func tombstoneSeedUsers(t *testing.T, a *App, n int) []uint {
	t.Helper()
	ids := make([]uint, 0, n)
	for i := 0; i < n; i++ {
		u := newTestUser(t, a, fmt.Sprintf("legacy-page-%d", i), erasurePW, models.RoleMember)
		tombstoneLegacy(t, a.DB, u.ID)
		ids = append(ids, u.ID)
	}
	return ids
}

func TestTombstoneDiscoveryPagesAreBoundedAndAtomic(t *testing.T) {
	a := testApp(t)
	ids := tombstoneSeedUsers(t, a, 10)
	living := newTestUser(t, a, "living-page", erasurePW, models.RoleMember)
	const day = "2026-11-06"
	// Failing the continuation must undo every subject job in that INSERT.
	ddl := `CREATE FUNCTION test_tombstone_page_failure() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN IF NEW.kind = 'erasure.tombstone_page' THEN RAISE EXCEPTION 'test continuation failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER test_tombstone_page_failure BEFORE INSERT ON jobs FOR EACH ROW EXECUTE FUNCTION test_tombstone_page_failure()`
	if err := a.DB.Exec(ddl).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		a.DB.Exec(`DROP TRIGGER IF EXISTS test_tombstone_page_failure ON jobs; DROP FUNCTION IF EXISTS test_tombstone_page_failure()`)
	})
	if err := a.runSweepPageSize(context.Background(), 0, day, 3); err == nil {
		t.Fatal("continuation failure succeeded")
	}
	if n := countRows(t, a.DB, "jobs", "kind IN ?", []string{kindTombstone, kindTombstonePage}); n != 0 {
		t.Fatalf("failed page left %d jobs", n)
	}
	if err := a.DB.Exec(`DROP TRIGGER test_tombstone_page_failure ON jobs`).Error; err != nil {
		t.Fatal(err)
	}
	after := uint(0)
	for page := 0; page < 4; page++ {
		for retry := 0; retry < 2; retry++ {
			if err := a.runSweepPageSize(context.Background(), after, day, 3); err != nil {
				t.Fatal(err)
			}
		}
		want := min(3*(page+1), 10)
		if n := countRows(t, a.DB, "jobs", "kind = ?", kindTombstone); n != int64(want) {
			t.Fatalf("page %d subjects %d want %d", page, n, want)
		}
		if n := countRows(t, a.DB, "jobs", "kind = ?", kindTombstonePage); n != int64(min(page+1, 3)) {
			t.Fatalf("page %d continuations %d", page, n)
		}
		if page < 3 {
			var j models.Job
			if err := a.DB.Where("kind = ?", kindTombstonePage).Order("id DESC").First(&j).Error; err != nil {
				t.Fatal(err)
			}
			var p tombstonePageJob
			if err := json.Unmarshal([]byte(j.Payload), &p); err != nil {
				t.Fatal(err)
			}
			if p.Day != day || p.AfterID != ids[want-1] {
				t.Fatalf("continuation %+v", p)
			}
			after = p.AfterID
		}
	}
	if n := countRows(t, a.DB, "jobs", "kind = ? AND payload->>'userId' = ?", kindTombstone, fmt.Sprint(living.ID)); n != 0 {
		t.Fatal("selected living account")
	}
	var rows []models.Job
	if err := a.DB.Where("kind = ?", kindTombstone).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	for _, j := range rows {
		var p tombstoneJob
		if err := json.Unmarshal([]byte(j.Payload), &p); err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf("%s:%d:v%d:%s", kindTombstone, p.UserID, tombstoneSweepVersion, day)
		if j.UniqueKey == nil || *j.UniqueKey != want || j.MaxAttempts != jobs.DefaultMaxAttempts {
			t.Fatalf("bad subject job %+v", j)
		}
	}
}

func TestTombstoneDiscoveryProgressesPastDeadSubject(t *testing.T) {
	a := testApp(t)
	admin := newTestUser(t, a, "sweep-admin", erasurePW, models.RoleAdmin)
	ids := tombstoneSeedUsers(t, a, 10)
	ddl := fmt.Sprintf(`CREATE FUNCTION test_tombstone_subject_failure() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN IF NEW.id = %d THEN RAISE EXCEPTION 'test poisoned subject'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER test_tombstone_subject_failure BEFORE UPDATE ON users FOR EACH ROW EXECUTE FUNCTION test_tombstone_subject_failure()`, ids[0])
	if err := a.DB.Exec(ddl).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		a.DB.Exec(`DROP TRIGGER IF EXISTS test_tombstone_subject_failure ON users; DROP FUNCTION IF EXISTS test_tombstone_subject_failure()`)
	})
	if err := a.runSweepPageSize(context.Background(), 0, "2026-11-06", 3); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Exec(`UPDATE jobs SET max_attempts = 1 WHERE kind = ? AND payload->>'userId' = ?`, kindTombstone, fmt.Sprint(ids[0])).Error; err != nil {
		t.Fatal(err)
	}
	r := tombstoneRunner(t, a, 3)
	tombstoneDrain(t, a, r)
	if n := countRows(t, a.DB, "audit_logs", "action = ?", audit.UserErasureSwept); n != 9 {
		t.Fatalf("later subjects did not progress: %d markers", n)
	}
	var dead models.Job
	if err := a.DB.Where("kind = ? AND status = ?", kindTombstone, models.JobDead).First(&dead).Error; err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, a.DB, "notifications", "user_id = ? AND kind = ? AND subject_id = ?", admin.ID, notify.SystemJobDead, dead.ID); n != 1 {
		t.Fatalf("dead subject alert count %d", n)
	}
	if err := a.DB.Exec(`DROP TRIGGER test_tombstone_subject_failure ON users`).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.runSweepPageSize(context.Background(), 0, "2026-11-07", 3); err != nil {
		t.Fatal(err)
	}
	tombstoneDrain(t, a, r)
	if n := countRows(t, a.DB, "audit_logs", "action = ?", audit.UserErasureSwept); n != 10 {
		t.Fatalf("next day failed to recover: %d markers", n)
	}
	if got := reloadJob(t, a, dead.ID); got.Status != models.JobDead {
		t.Fatal("next day reused yesterday's dead key")
	}
}

func TestTombstoneRunnerReclaimsCommittedJobAndRecoversRestore(t *testing.T) {
	a := testApp(t)
	id := tombstoneSeedUsers(t, a, 1)[0]
	if err := a.runSweepPage(context.Background(), 0, "2026-11-06"); err != nil {
		t.Fatal(err)
	}
	r := tombstoneRunner(t, a, tombstoneSweepPage)
	if ran, err := r.RunOnce(context.Background(), "before-crash"); err != nil || !ran {
		t.Fatalf("first run %v %v", ran, err)
	}
	var j models.Job
	if err := a.DB.Where("kind = ?", kindTombstone).First(&j).Error; err != nil {
		t.Fatal(err)
	}
	// Model a process lost between domain COMMIT and runner.finish.
	if err := a.DB.Model(&j).Updates(map[string]any{"status": models.JobRunning, "locked_by": "lost", "locked_at": time.Now().Add(-2 * jobs.DefaultLease)}).Error; err != nil {
		t.Fatal(err)
	}
	before := tombstoneDatabase(t, a.DB)
	if ran, err := r.RunOnce(context.Background(), "reclaimed"); err != nil || !ran {
		t.Fatalf("reclaim %v %v", ran, err)
	}
	tombstoneAcceptRunnerTransition(t, a.DB, before, j.ID)
	tombstoneSameDatabase(t, before, tombstoneDatabase(t, a.DB))
	if got := reloadJob(t, a, j.ID); got.Status != models.JobDone || got.Attempts != 2 {
		t.Fatalf("reclaimed job %+v", got)
	}
	// A snapshot may contain today's completed job but no marker. Today's
	// key stays blocked; tomorrow's chain still recovers the missing work.
	if err := a.DB.Where("action = ? AND target_id = ?", audit.UserErasureSwept, id).Delete(&models.AuditLog{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.runSweepPage(context.Background(), 0, "2026-11-06"); err != nil {
		t.Fatal(err)
	}
	tombstoneDrain(t, a, r)
	if n := countRows(t, a.DB, "audit_logs", "action = ?", audit.UserErasureSwept); n != 0 {
		t.Fatal("reused blocked same-day key")
	}
	if err := a.runSweepPage(context.Background(), 0, "2026-11-07"); err != nil {
		t.Fatal(err)
	}
	tombstoneDrain(t, a, r)
	if n := countRows(t, a.DB, "audit_logs", "action = ?", audit.UserErasureSwept); n != 1 {
		t.Fatal("next-date restore recovery failed")
	}
	if n := countRows(t, a.DB, "jobs", "kind = ?", kindTombstone); n != 2 {
		t.Fatalf("restore job keys: %d", n)
	}
	// Cleanup pruning cannot invalidate the marker.
	if _, err := jobs.PruneDone(a.DB, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	before = tombstoneDatabase(t, a.DB)
	if err := a.runSweepPage(context.Background(), 0, "2026-11-08"); err != nil {
		t.Fatal(err)
	}
	tombstoneSameDatabase(t, before, tombstoneDatabase(t, a.DB))
}

func TestTombstoneLeaseTakeoverWaitsOnContactGate(t *testing.T) {
	a := testApp(t)
	id := tombstoneSeedUsers(t, a, 1)[0]
	const pauseKey int64 = 7460201
	release, blocker := holdAdvisory(t, a.DB, pauseKey)
	defer release()
	ddl := fmt.Sprintf(`CREATE TABLE test_tombstone_scrubs (user_id bigint);
 CREATE FUNCTION test_tombstone_pause() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN PERFORM pg_advisory_xact_lock(%d); INSERT INTO test_tombstone_scrubs VALUES (NEW.id); RETURN NEW; END $$;
 CREATE TRIGGER test_tombstone_pause BEFORE UPDATE ON users FOR EACH ROW EXECUTE FUNCTION test_tombstone_pause()`, pauseKey)
	if err := a.DB.Exec(ddl).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		a.DB.Exec(`DROP TRIGGER IF EXISTS test_tombstone_pause ON users; DROP FUNCTION IF EXISTS test_tombstone_pause(); DROP TABLE IF EXISTS test_tombstone_scrubs`)
	})
	if err := a.runSweepPage(context.Background(), 0, "2026-11-06"); err != nil {
		t.Fatal(err)
	}
	r := tombstoneRunner(t, a, tombstoneSweepPage)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	run := func(worker string) <-chan error {
		ch := make(chan error, 1)
		go func() {
			ran, err := r.RunOnce(ctx, worker)
			if err == nil && !ran {
				err = errors.New("runner claimed nothing")
			}
			ch <- err
		}()
		return ch
	}
	first := run("first")
	firstPID := waiterOn(t, a.DB, blocker)
	requireContactGate(t, a.DB, firstPID, true, "first worker")
	if err := a.DB.Exec(`UPDATE jobs SET locked_at = now() - interval '1 hour' WHERE kind = ? AND status = ?`, kindTombstone, models.JobRunning).Error; err != nil {
		t.Fatal(err)
	}
	second := run("takeover")
	agesRaceGateWaiter(t, a.DB, firstPID, "takeover worker")
	release()
	if err := awaitDBResult(t, first); err != nil {
		t.Fatal(err)
	}
	if err := awaitDBResult(t, second); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, a.DB, "test_tombstone_scrubs", "user_id = ?", id); n != 1 {
		t.Fatalf("scrubs %d want 1", n)
	}
	if n := countRows(t, a.DB, "audit_logs", "action = ? AND target_id = ?", audit.UserErasureSwept, id); n != 1 {
		t.Fatalf("markers %d want 1", n)
	}
	var j models.Job
	if err := a.DB.Where("kind = ?", kindTombstone).First(&j).Error; err != nil {
		t.Fatal(err)
	}
	if j.Status != models.JobDone || j.Attempts != 2 || j.LockedBy != "takeover" {
		t.Fatalf("takeover result %+v", j)
	}
}

// Normalize exactly the runner-owned row after checking its final state.
// Every other existing job and all added jobs remain in the database proof.
func tombstoneAcceptRunnerTransition(t *testing.T, db *gorm.DB, before map[string]string, jobID uint) {
	t.Helper()
	var oldJobs []map[string]any
	if err := json.Unmarshal([]byte(before["jobs"]), &oldJobs); err != nil {
		t.Fatal(err)
	}
	var current map[string]any
	var currentJSON string
	if err := db.Raw(`SELECT to_jsonb(j)::text FROM jobs j WHERE id = ?`, jobID).Scan(&currentJSON).Error; err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(currentJSON), &current); err != nil {
		t.Fatal(err)
	}
	for i, old := range oldJobs {
		if old["id"] == float64(jobID) {
			oldJobs[i] = current
		}
	}
	body, err := json.Marshal(oldJobs)
	if err != nil {
		t.Fatal(err)
	}
	var normalized string
	if err := db.Raw("SELECT ?::jsonb::text", string(body)).Scan(&normalized).Error; err != nil {
		t.Fatal(err)
	}
	before["jobs"] = normalized
}

func TestTombstoneFreshErasureThroughRunnerChangesOnlyMaintenance(t *testing.T) {
	s := newCoreErasureScene(t)
	a := s.app
	u := newTestUser(t, a, "fresh-runner", erasurePW, models.RoleMember)
	seedErasureTokens(t, a.DB, u.ID, "fresh-runner", "fresh-runner-new@x.test")
	erasureSeedConsentEvidence(t, a.DB, u.ID, "fresh-runner-consent")
	s.ac.holdNotices = true
	s.erase(t, u.ID)
	if err := a.runSweepPage(context.Background(), 0, "2026-11-06"); err != nil {
		t.Fatal(err)
	}
	// Move unrelated delivery out of the due queue before the snapshot.
	if err := a.DB.Exec(`UPDATE jobs SET run_at = now() + interval '1 day' WHERE kind <> ?`, kindTombstone).Error; err != nil {
		t.Fatal(err)
	}
	before := tombstoneDatabase(t, a.DB)
	r := tombstoneRunner(t, a, tombstoneSweepPage)
	if ran, err := r.RunOnce(context.Background(), "fresh"); err != nil || !ran {
		t.Fatalf("fresh run %v %v", ran, err)
	}
	// The runner's own state transition is the only existing job-row change.
	// Inspect it explicitly, then omit only that row from the whole DB proof.
	var j models.Job
	if err := a.DB.Where("kind = ?", kindTombstone).First(&j).Error; err != nil {
		t.Fatal(err)
	}
	if j.Status != models.JobDone || j.Attempts != 1 {
		t.Fatalf("fresh runner job %+v", j)
	}
	tombstoneAcceptRunnerTransition(t, a.DB, before, j.ID)
	tombstoneMaintenanceDelta(t, a.DB, before, u.ID, tombstoneSweepVersion)
	before = tombstoneDatabase(t, a.DB)
	if err := a.runSweepPage(context.Background(), 0, "2026-11-07"); err != nil {
		t.Fatal(err)
	}
	tombstoneSameDatabase(t, before, tombstoneDatabase(t, a.DB))
}

func TestTombstoneDiscoveryNumericMarkersAndShape(t *testing.T) {
	a := testApp(t)
	ids := tombstoneSeedUsers(t, a, 9)
	metas := []string{`{"version":"999"}`, `{"version":null}`, `{"version":{}}`, `{"version":[]}`, `{"version":true}`, `{}`, `{"version":0}`, `{"version":1}`, `{"version":2.5}`}
	for i, meta := range metas {
		erasureMustCreate(t, a.DB, &models.AuditLog{Action: audit.UserErasureSwept, TargetType: "user", TargetID: ids[i], MetaJSON: meta})
	}
	living := newTestUser(t, a, "living-marker", erasurePW, models.RoleMember)
	// Even the exact placeholder is ineligible without deleted_at.
	if err := a.DB.Exec(`UPDATE users SET email = 'deleted-' || id || '@deleted.invalid' WHERE id = ?`, living.ID).Error; err != nil {
		t.Fatal(err)
	}
	lookalike := newTestUser(t, a, "lookalike-marker", erasurePW, models.RoleMember)
	if err := a.DB.Exec(`UPDATE users SET deleted_at = now() WHERE id = ?`, lookalike.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.runSweepPage(context.Background(), 0, "2026-11-06"); err != nil {
		t.Fatal(err)
	}
	var selected []uint
	if err := a.DB.Raw(`SELECT (payload->>'userId')::bigint FROM jobs WHERE kind = ? ORDER BY (payload->>'userId')::bigint`, kindTombstone).Scan(&selected).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(selected, ids[:7]) {
		t.Fatalf("numeric/structural discovery = %v want %v", selected, ids[:7])
	}
}

func TestTombstoneDailyDatePersistsAndDeadPageRecovers(t *testing.T) {
	a := testApp(t)
	tombstoneSeedUsers(t, a, 4)
	var daily jobs.Kind
	for _, k := range a.dailyKinds() {
		if k.Name == kindTombstoneSweep {
			daily = k
		}
	}
	zone, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 31, 5, 30, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		if err := a.queueDaily([]jobs.Kind{daily}, now, zone); err != nil {
			t.Fatal(err)
		}
	}
	var sweep models.Job
	if err := a.DB.Where("kind = ?", kindTombstoneSweep).First(&sweep).Error; err != nil {
		t.Fatal(err)
	}
	if sweep.UniqueKey == nil || *sweep.UniqueKey != kindTombstoneSweep+":2026-10-30" {
		t.Fatalf("daily key %+v", sweep)
	}
	var p tombstonePageJob
	if err := json.Unmarshal([]byte(sweep.Payload), &p); err != nil {
		t.Fatal(err)
	}
	if p.Day != "2026-10-30" || p.AfterID != 0 {
		t.Fatalf("daily payload %+v", p)
	}
	if err := daily.Run(context.Background(), json.RawMessage(sweep.Payload)); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, a.DB, "jobs", "kind = ? AND unique_key LIKE ?", kindTombstone, "%:2026-10-30"); n != 4 {
		t.Fatalf("daily handler lost scheduled date: %d", n)
	}
	// A dead continuation leaves later ids for the next daily chain from zero.
	if err := a.DB.Where("kind = ?", kindTombstone).Delete(&models.Job{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.runSweepPageSize(context.Background(), 0, p.Day, 3); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Exec(`UPDATE jobs SET status = 'dead' WHERE kind IN (?, ?)`, kindTombstonePage, kindTombstoneSweep).Error; err != nil {
		t.Fatal(err)
	}
	tombstoneDrain(t, a, tombstoneRunner(t, a, 3))
	if n := countRows(t, a.DB, "audit_logs", "action = ?", audit.UserErasureSwept); n != 3 {
		t.Fatalf("dead continuation unexpectedly ran: %d", n)
	}
	if err := a.queueDaily([]jobs.Kind{daily}, now.Add(24*time.Hour), zone); err != nil {
		t.Fatal(err)
	}
	var next models.Job
	if err := a.DB.Where("kind = ? AND status = ?", kindTombstoneSweep, models.JobQueued).First(&next).Error; err != nil {
		t.Fatal(err)
	}
	r := a.newRunner(daily)
	if ran, err := r.RunOnce(context.Background(), "next-day"); err != nil || !ran {
		t.Fatalf("next day sweep %v %v", ran, err)
	}
	tombstoneDrain(t, a, tombstoneRunner(t, a, 3))
	if n := countRows(t, a.DB, "audit_logs", "action = ?", audit.UserErasureSwept); n != 4 {
		t.Fatalf("next day failed dead page recovery: %d", n)
	}
}

// Rerunnable performance evidence for review. The plan and elapsed time are
// logged by this test; no schema/index change is hidden in the measurement.
func TestTombstoneDiscoveryMeasuredPageBound(t *testing.T) {
	// Shared resets rows with DELETE, retaining planner statistics and table
	// storage. This fixture's ANALYZE would otherwise make later 300ms
	// deadline tests plan against 200,000 audit rows, even after a reset.
	db, err := testdb.Open(testdb.Fresh(t, true))
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	a := &App{DB: db}
	if err := db.Exec(`INSERT INTO users (id, email, name, deleted_at, status)
 SELECT n, 'deleted-' || n || '@deleted.invalid', '', now(), 'disabled' FROM generate_series(100001, 110000) n`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO audit_logs (action, target_type, target_id, meta_json)
 SELECT ?, 'user', n, jsonb_build_object('version', ?::integer) FROM generate_series(100001, 105000) n`, audit.UserErasureSwept, tombstoneSweepVersion).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO audit_logs (action, target_type, target_id, meta_json)
 SELECT 'test.unrelated', 'user', 100001 + n % 10000, '{}'::jsonb FROM generate_series(1, 195000) n`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`ANALYZE users; ANALYZE audit_logs`).Error; err != nil {
		t.Fatal(err)
	}
	var plan []string
	if err := db.Raw("EXPLAIN (ANALYZE, BUFFERS) "+tombstoneCandidatesSQL, uint(0), audit.UserErasureSwept, tombstoneSweepVersion, tombstoneSweepPage).Scan(&plan).Error; err != nil {
		t.Fatal(err)
	}
	t.Logf("10,000 tombstones, 5,000 marked, 200,000 audit rows:\n%s", strings.Join(plan, "\n"))
	var daily jobs.Kind
	for _, k := range a.dailyKinds() {
		if k.Name == kindTombstoneSweep {
			daily = k
		}
	}
	if _, err := enqueueJob(db, kindTombstoneSweep, tombstonePageJob{Day: "2026-11-06"}, time.Now(), "measured-sweep", jobs.DefaultMaxAttempts); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	ran, err := a.newRunner(daily).RunOnce(context.Background(), "measured-page")
	elapsed := time.Since(start)
	t.Logf("one real page job wall time: %s", elapsed)
	if err != nil || !ran {
		t.Fatalf("measured page %v %v", ran, err)
	}
	if elapsed >= time.Second {
		t.Fatalf("page took %s; arbitrate before adding an index or migration", elapsed)
	}
	if n := countRows(t, db, "jobs", "kind = ?", kindTombstone); n != int64(tombstoneSweepPage) {
		t.Fatalf("measured page queued %d subjects", n)
	}
	if n := countRows(t, db, "jobs", "kind = ?", kindTombstonePage); n != 1 {
		t.Fatalf("measured continuation count %d", n)
	}
}

// runSweepPageSize runs one discovery page with the page size shortened to
// pageSize (tombstoneSweepPage is a var for this; tests stay serial).
func (a *App) runSweepPageSize(ctx context.Context, afterID uint, day string, pageSize int) error {
	old := tombstoneSweepPage
	tombstoneSweepPage = pageSize
	defer func() { tombstoneSweepPage = old }()
	return a.runSweepPage(ctx, afterID, day)
}
