package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/token"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"fold/internal/guardkit"

	"fold/internal/audit"
	"fold/models"

	"gorm.io/gorm"
)

func runAuditErasure(t *testing.T, db *gorm.DB, s erasureSubject) auditErasureCounts {
	t.Helper()
	var out auditErasureCounts
	if err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		out, err = eraseAuditDetailsIn(tx, s)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func auditErasureLog(t *testing.T, db *gorm.DB, action, target string, id uint, actor *uint, meta map[string]any) models.AuditLog {
	t.Helper()
	if err := audit.Write(db, audit.Entry{Action: action, TargetType: target, TargetID: id, Actor: actor, IP: "192.0.2.19", Meta: meta}); err != nil {
		t.Fatal(err)
	}
	var row models.AuditLog
	if err := db.Order("id DESC").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row
}

func assertAuditErasureRows(t *testing.T, db *gorm.DB, before []models.AuditLog, blank map[uint]bool, removed map[uint][]string) {
	t.Helper()
	for _, old := range before {
		want := old
		if blank[old.ID] {
			want.IP = ""
		}
		if keys := removed[old.ID]; len(keys) > 0 {
			// Compare against Postgres' own canonical jsonb bytes, not a Go re-encoding.
			if err := db.Raw(`SELECT CAST(? AS jsonb) - CAST(? AS text[])`, old.MetaJSON, auditStringArray(keys)).Scan(&want.MetaJSON).Error; err != nil {
				t.Fatal(err)
			}
		}
		var got models.AuditLog
		if err := db.First(&got, old.ID).Error; err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(want, got) {
			t.Errorf("audit %d (%s) changed beyond its IP/subject keys:\nwant %+v\ngot %+v", old.ID, old.Action, want, got)
		}
	}
}

func auditStringArray(keys []string) string { return "{" + strings.Join(keys, ",") + "}" }

func TestAuditErasureAttributionAndSubjectMetadata(t *testing.T) {
	app := testApp(t)
	u := newTestUser(t, app, "audit-erased", erasurePW, models.RoleAdmin)
	living := newTestUser(t, app, "audit-living", erasurePW, models.RoleAdmin)
	s := erasureSubject{ID: u.ID, Requests: []uint{301}, Invitations: []uint{401}, People: []uint{501}}
	var before []models.AuditLog
	blank := map[uint]bool{}
	removed := map[uint][]string{}
	add := func(action, target string, id uint, actor *uint, meta map[string]any, clear bool, keys ...string) {
		row := auditErasureLog(t, app.DB, action, target, id, actor, meta)
		before = append(before, row)
		blank[row.ID] = clear
		removed[row.ID] = keys
	}
	add(audit.UserCreated, "user", living.ID, &u.ID, map[string]any{"role": "member"}, true)
	add(audit.UserCreated, "user", u.ID, &living.ID, map[string]any{"role": "member"}, false)
	add(audit.AccountApproved, "account_request", 301, &living.ID, map[string]any{"userId": u.ID}, false)
	add(audit.AccountApproved, "account_request", 302, &u.ID, map[string]any{"userId": living.ID}, true)
	add(audit.InvitationAccepted, "invitation", 401, &living.ID, map[string]any{"userId": u.ID}, true)
	add(audit.InvitationAccepted, "invitation", 402, &u.ID, map[string]any{"userId": living.ID}, false)
	add(audit.InvitationAccepted, "invitation", 401, &living.ID, map[string]any{"personId": 501}, true)
	add(audit.InvitationAccepted, "invitation", 402, &u.ID, map[string]any{"personId": 502}, false)
	add(audit.AddedOverLimit, "group", 1, &u.ID, map[string]any{"userId": living.ID, "count": 5}, true)
	add(audit.AddedOverLimit, "group", 1, &living.ID, map[string]any{"userId": u.ID, "count": 5}, false)
	add(audit.AddedOverLimit, "group", 1, &living.ID, map[string]any{"userId": u.ID, "invitationId": 401}, true)
	add(audit.AddedOverLimit, "group", 1, &u.ID, map[string]any{"userId": living.ID, "invitationId": 402}, false)
	add(audit.AccountRequested, "account_request", 301, &living.ID, map[string]any{"source": "member_invite"}, false)
	add(audit.AccountRequested, "account_request", 302, &u.ID, map[string]any{"source": "member_invite"}, true)
	for _, source := range []string{"self_signup", "guest_upgrade", "household_conversion"} {
		add(audit.AccountRequested, "account_request", 301, &living.ID, map[string]any{"source": source}, true)
		add(audit.AccountRequested, "account_request", 302, &u.ID, map[string]any{"source": source}, false)
	}
	add(audit.PasswordResetRequested, "user", u.ID, &living.ID, nil, true)
	add(audit.PasswordResetRequested, "user", living.ID, &u.ID, nil, false)
	add(audit.RegistrationCancelled, "event", 1, nil, map[string]any{"subjectType": "user", "subjectId": u.ID, "userId": u.ID, "personId": nil}, true)
	add(audit.RegistrationCancelled, "event", 1, nil, map[string]any{"subjectType": "household_person", "subjectId": 501, "personId": 501, "userId": nil}, true)
	add(audit.RegistrationCancelled, "event", 1, nil, map[string]any{"subjectType": "household_person", "subjectId": 502, "personId": 502, "registeredBy": u.ID}, false)
	add(audit.RegistrationCancelled, "event", 1, nil, map[string]any{"subjectType": "user", "subjectId": u.ID, "personId": 501, "userId": u.ID}, false)
	add(audit.RegistrationCancelled, "event", 1, nil, map[string]any{"subjectType": "user", "subjectId": living.ID, "userId": u.ID}, false)
	add(audit.RegistrationCancelled, "event", 1, nil, map[string]any{"userId": living.ID}, false)
	add(audit.RegistrationCancelled, "event", 1, &living.ID, map[string]any{"userId": u.ID}, false)
	add(audit.RegistrationChanged, "event", 1, &u.ID, map[string]any{"userId": living.ID}, true)
	add(audit.RegistrationChanged, "event", 1, &living.ID, map[string]any{"userId": u.ID}, false)
	// Conversion discriminators take precedence even on an anomalous event target.
	add(audit.RegistrationChanged, "event", 1, &u.ID, map[string]any{"invitationId": 401}, false)
	add(audit.RegistrationChanged, "registration", 1, &u.ID, map[string]any{"personId": 501, "userId": u.ID}, false)
	add(audit.HouseholdPersonConverted, "household", 1, &u.ID, map[string]any{"personId": 501, "userId": u.ID}, false)
	for _, action := range []string{audit.GuardianLinkConfirmed, audit.GuardianLinkRejected, audit.GuardianLinkRevoked, audit.TeenConsentGranted, audit.TeenConsentRevoked, audit.OccurrenceAccessAdded, audit.OccurrenceAccessEnded, audit.OccurrenceBlocked} {
		meta := map[string]any{"teenUserId": living.ID, "checkNote": "Keep this safeguarding record", "evidenceText": "Keep evidence", "registrationId": 71}
		add(action, "guardian_link", 71, &u.ID, meta, true)
		add(action, "guardian_link", 72, &living.ID, map[string]any{"teenUserId": u.ID, "checkNote": "Keep", "evidenceText": "Keep"}, false)
	}
	// Household erasure supplies the deleting admin's actor and IP. Neither
	// the account_deleted reason nor a linked guardian, teen or person owns it.
	for _, meta := range []map[string]any{
		{"reason": "account_deleted", "guardianUserId": u.ID, "personId": 501},
		{"reason": "account_deleted", "guardianUserId": living.ID, "teenUserId": u.ID},
	} {
		add(audit.GuardianLinkRevoked, "guardian_link", 73, &living.ID, meta, false)
		add(audit.GuardianLinkRevoked, "guardian_link", 74, nil, meta, false)
	}
	add(audit.GuardianLinkRevoked, "guardian_link", 75, &u.ID, map[string]any{"reason": "account_deleted", "guardianUserId": living.ID, "personId": 502}, true)
	add(audit.GuardianLinkRevoked, "guardian_link", 76, &u.ID, map[string]any{"reason": "account_deleted", "guardianUserId": living.ID, "teenUserId": living.ID}, true)
	add(audit.GuardianLinkRequested, "guardian_link", 1, &u.ID, map[string]any{"source": "claim", "teenUserId": living.ID}, true)
	add(audit.GuardianLinkRequested, "guardian_link", 2, &living.ID, map[string]any{"source": "claim", "teenUserId": u.ID}, false)
	for _, action := range []string{audit.UserEmailChanged, audit.EmailChangeRequested, audit.EmailChanged} {
		meta := map[string]any{"oldEmail": "prior@example.com", "newEmail": "new@example.com", "userId": u.ID}
		add(action, "user", u.ID, &living.ID, meta, false, "oldEmail", "newEmail")
		add(action, "user", living.ID, &u.ID, meta, true)
	}
	add(audit.InvitationCreated, "invitation", 401, &living.ID, map[string]any{"email": "old@example.com", "groupId": 22}, false, "email")
	add(audit.InvitationCreated, "invitation", 402, &u.ID, map[string]any{"email": "living@example.com"}, true)
	add(audit.MemberRemoved, "group", 1, &living.ID, map[string]any{"userId": u.ID, "note": "subject detail"}, false, "note")
	add(audit.MemberRemoved, "group", 1, &u.ID, map[string]any{"userId": living.ID, "note": "living detail"}, true)
	add(audit.RegistrationRemoved, "event", 1, &living.ID, map[string]any{"personId": 501, "note": "subject detail"}, false, "note")
	add(audit.RegistrationRemoved, "event", 1, &living.ID, map[string]any{"userId": u.ID, "note": "subject detail"}, false, "note")
	add(audit.RegistrationRemoved, "event", 1, &u.ID, map[string]any{"personId": 502, "note": "living detail"}, true)
	add(audit.PushDeviceOn, "user", u.ID, &u.ID, map[string]any{"label": "My device", "deviceId": 21}, true, "label")
	add(audit.GroupApproved, "group", 1, &u.ID, map[string]any{"message": "Keep church record"}, true)
	add(audit.HouseholdPersonUpdated, "household_person", 501, nil, map[string]any{"reason": "age_rollover", "kind": "adult", "previousKind": "teen"}, false)
	// Labelled historical/anomalous shapes. No inferred ACTOR fallback.
	for _, meta := range []map[string]any{{}, {"userId": "1"}, {"userId": u.ID, "personId": 501}, {"personId": nil}} {
		add(audit.InvitationAccepted, "invitation", 401, &u.ID, meta, false)
	}
	add(audit.AccountRequested, "account_request", 301, &u.ID, map[string]any{"source": "unknown"}, false)
	add(audit.GuardianLinkRequested, "guardian_link", 1, &u.ID, map[string]any{"teenUserId": u.ID, "source": "unknown"}, false)
	add("legacy.unknown", "user", u.ID, &u.ID, map[string]any{"userId": u.ID}, false)
	out := runAuditErasure(t, app.DB, s)
	assertAuditErasureRows(t, app.DB, before, blank, removed)
	var wantIPs int64
	for _, yes := range blank {
		if yes {
			wantIPs++
		}
	}
	if out.IPsBlanked != wantIPs || out.MetadataRowsScrubbed != int64(len(removedNonempty(removed))) {
		t.Fatalf("counts: %+v want IP=%d", out, wantIPs)
	}
	if out.KeptByClassAndAction[auditIPRetainUnattributed][audit.InvitationAccepted] != 4 {
		t.Fatalf("malformed acceptances not counted: %+v", out)
	}
	// Every seeded writer shape matches at most one attribution predicate.
	for action, rules := range auditIPAttribution {
		if len(rules) < 2 {
			continue
		}
		expressions := make([]string, len(rules))
		for i, rule := range rules {
			expressions[i] = "CASE WHEN (" + rule.Shape + ") THEN 1 ELSE 0 END"
		}
		var overlaps int64
		if err := app.DB.Raw("SELECT count(*) FROM audit_logs WHERE action = ? AND ("+strings.Join(expressions, " + ")+") > 1", action).Scan(&overlaps).Error; err != nil {
			t.Fatal(err)
		}
		if overlaps != 0 {
			t.Fatalf("%s has overlapping rules", action)
		}
	}
	snapshot := dumpRows(t, app.DB, "audit_logs", "id", "true")
	again := runAuditErasure(t, app.DB, s)
	if again.IPsBlanked != 0 || again.MetadataRowsScrubbed != 0 || snapshot != dumpRows(t, app.DB, "audit_logs", "id", "true") {
		t.Fatalf("second run mutated audit: %+v", again)
	}
	if !reflect.DeepEqual(out.KeptByClassAndAction, again.KeptByClassAndAction) {
		t.Fatal("retry lost retention report")
	}
}

func removedNonempty(m map[uint][]string) []uint {
	var ids []uint
	for id, keys := range m {
		if len(keys) > 0 {
			ids = append(ids, id)
		}
	}
	return ids
}

func TestAuditErasureReportRetainedTotals(t *testing.T) {
	h := householdErasureCounts{HouseholdsKept: 1, HouseholdsScrubbed: 2, PeopleAnonymized: 3, GuardianLinksRevoked: 4}
	a := auditErasureCounts{
		IPsBlanked: 90, MetadataRowsScrubbed: 80, MalformedConversionLinksRetained: 2,
		KeptByClassAndAction: map[auditIPClass]map[string]int64{
			auditIPRetainEssential:    {"fixture.essential": 3},
			auditIPRetainUnattributed: {audit.GuardianLinkRequested: 2, "fixture.unknown": 4},
		},
		Conversion: auditConversionCounts{Retained: 5,
			// Diagnostics can overlap across groups; only the distinct total
			// belongs in the report. Already-empty conversion IPs still count.
			ByReason: map[string]int64{"completion": 5, "malformed_candidate": 5}},
	}
	want := erasureReportJSON{HouseholdsKept: 1, HouseholdsScrubbed: 2, PeopleAnonymized: 3, GuardianLinksRevoked: 4, ActivityEntriesKept: 14}
	if got := newErasureReport(h, a); got != want {
		t.Fatalf("report %+v, want %+v", got, want)
	}
	if got := newErasureReport(householdErasureCounts{}, auditErasureCounts{}); got != (erasureReportJSON{}) {
		t.Fatalf("empty report %+v", got)
	}
}

func TestAuditErasureRetentionReportIsSubjectScoped(t *testing.T) {
	app := testApp(t)
	u := newTestUser(t, app, "audit-report-subject", erasurePW, models.RoleMember)
	other := newTestUser(t, app, "audit-report-other", erasurePW, models.RoleMember)
	s := erasureSubject{ID: u.ID, People: []uint{900001}}
	const action = "legacy.audit_retention"
	var before []models.AuditLog
	var want int64
	for _, tc := range []struct {
		target string
		id     uint
		actor  *uint
		meta   map[string]any
		kept   bool
	}{
		{"group", 1, &u.ID, nil, true},
		{"user", u.ID, &other.ID, nil, true},
		{"group", 1, nil, map[string]any{"userId": u.ID}, true},
		{"group", 1, nil, map[string]any{"userId": itoa(u.ID)}, true},
		{"group", 1, nil, map[string]any{"teenUserId": u.ID}, true},
		{"group", 1, nil, map[string]any{"teenUserId": itoa(u.ID)}, true},
		{"group", 1, nil, map[string]any{"personId": 900001}, true},
		{"group", 1, nil, map[string]any{"personId": "900001"}, true},
		{"group", 1, nil, map[string]any{"userId": json.Number(itoa(u.ID) + ".0")}, true},
		{"group", 1, &other.ID, nil, false},
		{"user", other.ID, nil, nil, false},
		{"group", u.ID, nil, nil, false},
		{"group", 1, nil, map[string]any{"userId": other.ID, "teenUserId": other.ID, "personId": 900002}, false},
		{"group", 1, nil, map[string]any{"userId": []any{u.ID}, "teenUserId": nil}, false},
		{"group", 1, nil, map[string]any{"reason": itoa(u.ID)}, false},
	} {
		before = append(before, auditErasureLog(t, app.DB, action, tc.target, tc.id, tc.actor, tc.meta))
		if tc.kept {
			want++
		}
	}
	// An already empty IP is not retained personal-IP residue.
	row := auditErasureLog(t, app.DB, action, "user", u.ID, &u.ID, nil)
	if err := app.DB.Model(&row).Update("ip", "").Error; err != nil {
		t.Fatal(err)
	}
	row.IP = ""
	before = append(before, row)
	for i := 0; i < 2; i++ {
		out := runAuditErasure(t, app.DB, s)
		if got := out.KeptByClassAndAction[auditIPRetainUnattributed][action]; got != want {
			t.Fatalf("run %d: retained %d, want %d subject-linked rows: %+v", i, got, want, out)
		}
		assertAuditErasureRows(t, app.DB, before, nil, nil)
	}
}

func TestAuditErasureSignupGuardianUsesOriginalSubmitter(t *testing.T) {
	for _, source := range []string{"self_signup", "member_invite"} {
		for _, anomaly := range []string{"none", "two_requests", "null_requester"} {
			t.Run(source+"/"+anomaly, func(t *testing.T) {
				app := testApp(t)
				admin := newTestUser(t, app, "audit-admin", erasurePW, models.RoleAdmin)
				guardian := newTestUser(t, app, "audit-guardian", erasurePW, models.RoleMember)
				inviter := newTestUser(t, app, "audit-inviter", erasurePW, models.RoleMember)
				ac := loginAPI(t, app, admin.Email, erasurePW)
				ac.ip = "192.0.2.2"
				body := signupBody("Teen", "audit-teen@example.com", "teen", teenBirthDate())
				body["guardianEmail"] = guardian.Email
				submit := anonClient(t, app, "192.0.2.3")
				path := "/api/v1/account-requests"
				if source == "member_invite" {
					submit = loginAPI(t, app, inviter.Email, erasurePW)
					submit.ip = "192.0.2.1"
					path += "/invite-friend"
				}
				apiDo(t, submit, "POST", path, body, 200)
				req := pendingRequest(t, app, "audit-teen@example.com")
				apiDo(t, ac, "POST", "/api/v1/admin/account-requests/"+itoa(req.ID)+"/approve", map[string]any{}, 200)
				if err := app.DB.First(&req, req.ID).Error; err != nil {
					t.Fatal(err)
				}
				var row models.AuditLog
				if err := app.DB.Where("action = ?", audit.GuardianLinkRequested).First(&row).Error; err != nil {
					t.Fatal(err)
				}
				if row.ActorID != nil || row.IP != submit.ip {
					t.Fatalf("nomination didn't copy submitter: %+v", row)
				}
				// Labelled fallback anomalies, starting from the real approval writer.
				if anomaly == "null_requester" {
					if err := app.DB.Model(&req).Update("requested_by", nil).Error; err != nil {
						t.Fatal(err)
					}
				}
				if anomaly == "two_requests" {
					copy := req
					copy.ID = 0
					copy.Email = "another@example.com"
					if err := app.DB.Create(&copy).Error; err != nil {
						t.Fatal(err)
					}
				}
				expectedOwner := *req.ApprovedUserID
				if source == "member_invite" {
					expectedOwner = inviter.ID
				}
				for _, uid := range []uint{admin.ID, guardian.ID, *req.ApprovedUserID, inviter.ID} {
					// Restore only the IP between independent ownership probes.
					if err := app.DB.Model(&row).Update("ip", row.IP).Error; err != nil {
						t.Fatal(err)
					}
					out := runAuditErasure(t, app.DB, erasureSubject{ID: uid})
					var after models.AuditLog
					if err := app.DB.First(&after, row.ID).Error; err != nil {
						t.Fatal(err)
					}
					valid := anomaly != "two_requests" && !(source == "member_invite" && anomaly == "null_requester")
					want := row
					if valid && uid == expectedOwner {
						want.IP = ""
					}
					if !reflect.DeepEqual(after, want) {
						t.Fatalf("erasing %d: want %+v got %+v", uid, want, after)
					}
					var wantKept int64
					if !valid && uid == *req.ApprovedUserID {
						wantKept = 1
					}
					if out.KeptByClassAndAction[auditIPRetainUnattributed][audit.GuardianLinkRequested] != wantKept {
						t.Fatalf("nomination report for %d, want %d subject-linked rows: %+v", uid, wantKept, out)
					}
				}
			})
		}
	}
}

// Real adult approval with two transfers. Fallback fixtures below mutate only
// after the actual redeem and approval handlers have produced their audit rows.
type auditAdultFixture struct {
	app     *App
	admin   *models.User
	inviter *models.User
	user    models.User
	request models.AccountRequest
	person  householdPersonJSON
	rows    []models.AuditLog
}

func newAuditAdultFixture(t *testing.T) auditAdultFixture {
	t.Helper()
	return newAuditAdultFixtureWithInviter(t, false)
}

func newAuditAdultFixtureWithInviter(t *testing.T, approverInvites bool) auditAdultFixture {
	t.Helper()
	app := testApp(t)
	admin, gid := openGroup(t, app, "Audit conversion")
	admin.ip = "192.0.2.2"
	var d models.User
	if err := app.DB.Where("role = ?", models.RoleAdmin).First(&d).Error; err != nil {
		t.Fatal(err)
	}
	holder, parent, person, _ := householdFamily(t, app, admin, "audit-convert")
	parent.ip = "192.0.2.1"
	inviter := holder
	if approverInvites {
		parent = admin
		inviter = &d
	}
	eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(10), true)
	// The API has already generated the event's occurrence. Reuse it, then
	// add the following date so both registrations have distinct occurrences.
	var first models.EventOccurrence
	if err := app.DB.Where("event_id = ? AND original_date = ?", eventID, date).First(&first).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		o := first
		if i != 0 {
			at := first.StartsAt.AddDate(0, 0, i)
			o = models.EventOccurrence{EventID: eventID, OriginalDate: calDate(at.Format("2006-01-02")), StartsAt: at, EndsAt: at.Add(time.Hour), Status: models.OccurrenceScheduled}
			if err := app.DB.Create(&o).Error; err != nil {
				t.Fatal(err)
			}
		}
		r := models.Registration{OccurrenceID: o.ID, HouseholdPersonID: &person.ID, RegisteredBy: &holder.ID, Status: models.RegGoing, AttendanceMode: models.AttendanceInPerson, Source: "admin"}
		if err := app.DB.Create(&r).Error; err != nil {
			t.Fatal(err)
		}
	}
	token := inviteHouseholdPerson(t, app, parent, person.ID, "audit-independent@example.com")
	redeemer := anonClient(t, app, "192.0.2.3")
	apiDo(t, redeemer, "POST", "/api/v1/invitations/accept", map[string]any{"ageBand": "adult", "token": token, "password": erasurePW}, 200)
	req := pendingRequest(t, app, "audit-independent@example.com")
	apiDo(t, admin, "POST", "/api/v1/admin/account-requests/"+itoa(req.ID)+"/approve", map[string]any{}, 200)
	if err := app.DB.First(&req, req.ID).Error; err != nil {
		t.Fatal(err)
	}
	var user models.User
	if err := app.DB.First(&user, *req.ApprovedUserID).Error; err != nil {
		t.Fatal(err)
	}
	var rows []models.AuditLog
	if err := app.DB.Where("action = ? OR (action = ? AND target_type = 'registration')", audit.HouseholdPersonConverted, audit.RegistrationChanged).Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("L1 writer rows: %+v", rows)
	}
	for _, r := range rows {
		var meta map[string]json.RawMessage
		if err := json.Unmarshal([]byte(r.MetaJSON), &meta); err != nil {
			t.Fatal(err)
		}
		keys := make([]string, 0, len(meta))
		for k := range meta {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		want := []string{"clearedChildcareRequested", "formerRegisteredBy", "invitationId", "personId", "userId"}
		if r.Action == audit.HouseholdPersonConverted {
			want = []string{"accountType", "householdId", "invitationId", "personId", "transferCount", "userId"}
		}
		if !reflect.DeepEqual(keys, want) || r.IP != "192.0.2.2" || r.ActorID == nil || *r.ActorID != inviter.ID {
			t.Fatalf("real conversion shape: %+v keys %v", r, keys)
		}
	}
	return auditAdultFixture{app, &d, inviter, user, req, person, rows}
}

func auditGroupInput(f auditAdultFixture) auditConversionRequestRow {
	h := f.person.HouseholdID
	return auditConversionRequestRow{ID: f.request.ID, PersonID: f.person.ID, InvitationID: *f.request.InvitationID, UserID: f.user.ID, HouseholdID: &h}
}

func TestAuditErasureConversionDiscoveryFallbacks(t *testing.T) {
	// Each case starts with L1, then simulates an anomaly no current writer emits.
	cases := []struct {
		name, reason string
		c, t         int64
		mutate       func(*testing.T, auditAdultFixture)
	}{
		{"L1_G8_unestablished", "history_unestablished", 1, 2, nil},
		{"11a_duplicate_C", "duplicate_converted_row", 2, 2, func(t *testing.T, f auditAdultFixture) {
			cloneAuditCandidate(t, f, audit.HouseholdPersonConverted, func(*models.AuditLog, map[string]any) {})
		}},
		{"11b_missing_C", "no_converted_row", 0, 2, func(t *testing.T, f auditAdultFixture) {
			if err := f.app.DB.Where("action = ?", audit.HouseholdPersonConverted).Delete(&models.AuditLog{}).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"11c_missing_T", "count_mismatch", 1, 1, func(t *testing.T, f auditAdultFixture) {
			if err := f.app.DB.Delete(&models.AuditLog{}, f.rows[0].ID).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"11d_string_count", "count_mismatch", 1, 2, func(t *testing.T, f auditAdultFixture) {
			editAuditCandidate(t, f, audit.HouseholdPersonConverted, func(_ *models.AuditLog, m map[string]any) { m["transferCount"] = "2" })
		}},
		{"11d_missing_count", "count_mismatch", 1, 2, func(t *testing.T, f auditAdultFixture) {
			editAuditCandidate(t, f, audit.HouseholdPersonConverted, func(_ *models.AuditLog, m map[string]any) { delete(m, "transferCount") })
		}},
		{"15a_bad_meta_household", "malformed_candidate", 2, 2, func(t *testing.T, f auditAdultFixture) {
			cloneAuditCandidate(t, f, audit.HouseholdPersonConverted, func(_ *models.AuditLog, m map[string]any) { m["householdId"] = 999999 })
		}},
		{"15a_wrong_target_household", "malformed_candidate", 2, 2, func(t *testing.T, f auditAdultFixture) {
			cloneAuditCandidate(t, f, audit.HouseholdPersonConverted, func(r *models.AuditLog, _ map[string]any) { r.TargetID = 999999 })
		}},
		{"15b_missing_former", "malformed_candidate", 1, 3, func(t *testing.T, f auditAdultFixture) {
			cloneAuditCandidate(t, f, audit.RegistrationChanged, func(_ *models.AuditLog, m map[string]any) { delete(m, "formerRegisteredBy") })
		}},
		{"15b_missing_cleared", "malformed_candidate", 1, 3, func(t *testing.T, f auditAdultFixture) {
			cloneAuditCandidate(t, f, audit.RegistrationChanged, func(_ *models.AuditLog, m map[string]any) { delete(m, "clearedChildcareRequested") })
		}},
		{"15b_duplicate_target", "count_mismatch", 1, 3, func(t *testing.T, f auditAdultFixture) {
			cloneAuditCandidate(t, f, audit.RegistrationChanged, func(*models.AuditLog, map[string]any) {})
			editAuditCandidate(t, f, audit.HouseholdPersonConverted, func(_ *models.AuditLog, m map[string]any) { m["transferCount"] = 3 })
		}},
		{"15c_C_missing_invitation", "malformed_candidate", 2, 2, func(t *testing.T, f auditAdultFixture) {
			cloneAuditCandidate(t, f, audit.HouseholdPersonConverted, func(_ *models.AuditLog, m map[string]any) { delete(m, "invitationId") })
		}},
		{"15c_C_all_missing_arm_d", "malformed_candidate", 2, 2, func(t *testing.T, f auditAdultFixture) {
			cloneAuditCandidate(t, f, audit.HouseholdPersonConverted, removeConversionIdentity)
		}},
		{"15c_T_string_user", "malformed_candidate", 1, 2, func(t *testing.T, f auditAdultFixture) {
			editAuditCandidate(t, f, audit.RegistrationChanged, func(_ *models.AuditLog, m map[string]any) { m["userId"] = itoa(f.user.ID) })
		}},
		{"15c_T_null_person", "malformed_candidate", 1, 2, func(t *testing.T, f auditAdultFixture) {
			editAuditCandidate(t, f, audit.RegistrationChanged, func(_ *models.AuditLog, m map[string]any) { m["personId"] = nil })
		}},
		{"15d_unreachable_T", "history_unestablished", 1, 2, func(t *testing.T, f auditAdultFixture) {
			cloneAuditCandidate(t, f, audit.RegistrationChanged, removeConversionIdentity)
		}},
		{"15d_wrong_target_T", "history_unestablished", 1, 2, func(t *testing.T, f auditAdultFixture) {
			cloneAuditCandidate(t, f, audit.RegistrationChanged, func(r *models.AuditLog, _ map[string]any) { r.TargetType = "event" })
		}},
		{"15e_other_numeric_tuple", "history_unestablished", 1, 2, func(t *testing.T, f auditAdultFixture) {
			cloneAuditCandidate(t, f, audit.HouseholdPersonConverted, func(_ *models.AuditLog, m map[string]any) {
				m["personId"] = 999991
				m["userId"] = 999992
				m["invitationId"] = 999993
			})
		}},
		{"15i_all_wrong_types_arm_d", "malformed_candidate", 2, 2, func(t *testing.T, f auditAdultFixture) {
			cloneAuditCandidate(t, f, audit.HouseholdPersonConverted, func(_ *models.AuditLog, m map[string]any) {
				m["personId"] = map[string]any{}
				m["userId"] = []any{}
				m["invitationId"] = false
			})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAuditAdultFixture(t)
			if tc.mutate != nil {
				tc.mutate(t, f)
			}
			before := dumpRows(t, f.app.DB, "audit_logs", "id", "action = ? OR (action = ? AND target_type IN ('registration','event'))", audit.HouseholdPersonConverted, audit.RegistrationChanged)
			requestBefore := dumpRows(t, f.app.DB, "account_requests", "id", "id = ?", f.request.ID)
			out := runAuditErasure(t, f.app.DB, erasureSubject{ID: f.admin.ID})
			if out.Conversion.Groups != 1 || len(out.Conversion.Details) != 1 {
				t.Fatalf("discovery: %+v", out.Conversion)
			}
			detail := out.Conversion.Details[0]
			if detail.Reason != tc.reason || detail.ConvertedFound != tc.c || detail.TransfersFound != tc.t || detail.ConvertedRetained != tc.c || detail.TransfersRetained != tc.t || detail.Blanked != 0 || out.Conversion.Retained != tc.c+tc.t || out.Conversion.ByReason[tc.reason] != tc.c+tc.t {
				t.Fatalf("report: %+v detail %+v", out.Conversion, detail)
			}
			if before != dumpRows(t, f.app.DB, "audit_logs", "id", "action = ? OR (action = ? AND target_type IN ('registration','event'))", audit.HouseholdPersonConverted, audit.RegistrationChanged) {
				t.Fatal("retained conversion candidates changed")
			}
			if requestBefore != dumpRows(t, f.app.DB, "account_requests", "id", "id = ?", f.request.ID) {
				t.Fatal("selector inputs changed")
			}
			for _, uid := range []uint{f.admin.ID, f.inviter.ID, f.user.ID, f.admin.ID} {
				again := runAuditErasure(t, f.app.DB, erasureSubject{ID: uid})
				if uid == f.admin.ID && !reflect.DeepEqual(out.Conversion, again.Conversion) {
					t.Fatalf("15h retry discovery drifted: %+v -> %+v", out.Conversion, again.Conversion)
				}
				if before != dumpRows(t, f.app.DB, "audit_logs", "id", "action = ? OR (action = ? AND target_type IN ('registration','event'))", audit.HouseholdPersonConverted, audit.RegistrationChanged) {
					t.Fatal("role erasure changed conversion rows")
				}
			}
		})
	}
}

func TestAuditErasureConversionTextDiscoveryLimit(t *testing.T) {
	f := newAuditAdultFixture(t)
	// The plan's text arms find malformed strings with canonical digits, but
	// not numeric IDs whose stored decimal scale makes ->> differ from id::text.
	// No arm d fallback applies because all three identity keys are numbers.
	cloneAuditCandidate(t, f, audit.RegistrationChanged, func(_ *models.AuditLog, m map[string]any) {
		m["personId"] = json.Number(itoa(f.person.ID) + ".0")
		m["userId"] = json.Number(itoa(f.user.ID) + ".0e0")
		m["invitationId"] = json.Number(itoa(*f.request.InvitationID) + ".0")
	})
	cloneAuditCandidate(t, f, audit.HouseholdPersonConverted, func(_ *models.AuditLog, m map[string]any) {
		m["personId"] = json.Number(itoa(f.person.ID) + ".0")
		m["userId"] = json.Number(itoa(f.user.ID) + ".0e0")
		m["invitationId"] = json.Number(itoa(*f.request.InvitationID) + ".0")
	})
	before := dumpRows(t, f.app.DB, "audit_logs", "id", "action = ? OR (action = ? AND target_type = 'registration')", audit.HouseholdPersonConverted, audit.RegistrationChanged)
	out := runAuditErasure(t, f.app.DB, erasureSubject{ID: f.admin.ID})
	if out.Conversion.Discovered != 3 || out.Conversion.Retained != 3 || out.Conversion.Blanked != 0 || out.Conversion.ByReason["history_unestablished"] != 3 {
		t.Fatalf("text discovery limit or G-8 default changed: %+v", out.Conversion)
	}
	if before != dumpRows(t, f.app.DB, "audit_logs", "id", "action = ? OR (action = ? AND target_type = 'registration')", audit.HouseholdPersonConverted, audit.RegistrationChanged) {
		t.Fatal("unreachable numeric spellings or retained candidates changed")
	}
}

func removeConversionIdentity(_ *models.AuditLog, m map[string]any) {
	delete(m, "personId")
	delete(m, "userId")
	delete(m, "invitationId")
}

func changeAuditCandidate(t *testing.T, f auditAdultFixture, action string, clone bool, mutate func(*models.AuditLog, map[string]any)) {
	t.Helper()
	var row models.AuditLog
	if err := f.app.DB.Where("action = ?", action).Order("id").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(row.MetaJSON), &meta); err != nil {
		t.Fatal(err)
	}
	mutate(&row, meta)
	raw, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	row.MetaJSON = string(raw)
	if clone {
		row.ID = 0
		if err := f.app.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	} else if err := f.app.DB.Model(&models.AuditLog{}).Where("id = ?", row.ID).Updates(map[string]any{"meta_json": row.MetaJSON, "target_type": row.TargetType, "target_id": row.TargetID}).Error; err != nil {
		t.Fatal(err)
	}
}
func cloneAuditCandidate(t *testing.T, f auditAdultFixture, action string, mutate func(*models.AuditLog, map[string]any)) {
	t.Helper()
	changeAuditCandidate(t, f, action, true, mutate)
}
func editAuditCandidate(t *testing.T, f auditAdultFixture, action string, mutate func(*models.AuditLog, map[string]any)) {
	t.Helper()
	changeAuditCandidate(t, f, action, false, mutate)
}

func TestAuditConversionV0ThroughV4Classification(t *testing.T) {
	f := newAuditAdultFixture(t)
	input := auditGroupInput(f)
	for _, history := range []bool{false, true} {
		got := classifyAuditConversion(input, f.rows, history)
		want := "history_unestablished"
		if history {
			want = "approval"
		}
		if got.Reason != want || got.TransferCount != 2 || got.ConvertedFound != 1 || got.TransfersFound != 2 {
			t.Fatalf("V3/V4: %+v", got)
		}
	}
	// V4 is exercised in pure classification only. No caller can establish G-8
	// in the staged production scrub, and none of these tests enables writes.
	for _, tc := range []struct {
		name, account, reason string
		marker                bool
	}{
		{"L2_teen", "teen", "completion", false},
		{"L4_birthday_marker", "adult", "completion", true},
		{"11e_missing_type", "", "unclassified", false},
		{"11e_unknown_type", "unknown", "unclassified", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := append([]models.AuditLog(nil), f.rows...)
			for i := range rows {
				if rows[i].Action == audit.HouseholdPersonConverted {
					var meta map[string]any
					if err := json.Unmarshal([]byte(rows[i].MetaJSON), &meta); err != nil {
						t.Fatal(err)
					}
					if tc.account == "" {
						delete(meta, "accountType")
					} else {
						meta["accountType"] = tc.account
					}
					raw, err := json.Marshal(meta)
					if err != nil {
						t.Fatal(err)
					}
					rows[i].MetaJSON = string(raw)
				}
			}
			r := input
			r.Marker = tc.marker
			got := classifyAuditConversion(r, rows, true)
			if got.Reason != tc.reason {
				t.Fatalf("V4 %+v", got)
			}
			if got := classifyAuditConversion(r, rows, false); got.Reason != "history_unestablished" {
				t.Fatalf("V3 must precede V4: %+v", got)
			}
		})
	}
	// Unknown household skips only arm d, not shape validation or identity arms.
	input.HouseholdID = nil
	got := classifyAuditConversion(input, f.rows, false)
	if got.HouseholdFallbackSkipped != 1 || got.Reason != "history_unestablished" {
		t.Fatalf("missing household: %+v", got)
	}
}

func TestAuditErasureConversionRollbackAndOrder(t *testing.T) {
	f := newAuditAdultFixture(t)
	snapshot := dumpRows(t, f.app.DB, "audit_logs", "id", "true")
	aborted := errors.New("interrupt before commit")
	var first auditErasureCounts
	err := f.app.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		first, err = eraseAuditDetailsIn(tx, erasureSubject{ID: f.admin.ID})
		if err != nil {
			return err
		}
		return aborted
	})
	if !errors.Is(err, aborted) || snapshot != dumpRows(t, f.app.DB, "audit_logs", "id", "true") {
		t.Fatal("audit transaction didn't roll back", err)
	}
	again := runAuditErasure(t, f.app.DB, erasureSubject{ID: f.admin.ID})
	if !reflect.DeepEqual(first, again) {
		t.Fatalf("retry changed counts: %+v %+v", first, again)
	}
	// Roll back each order so both traverse the very same real-writer history.
	var finals []string
	for _, order := range [][]uint{{f.inviter.ID, f.user.ID, f.admin.ID}, {f.admin.ID, f.inviter.ID, f.user.ID}, {f.user.ID, f.admin.ID, f.inviter.ID}} {
		err := f.app.DB.Transaction(func(tx *gorm.DB) error {
			for _, uid := range order {
				s := erasureSubject{ID: uid}
				if uid == f.user.ID {
					s.Requests = []uint{f.request.ID}
					s.Invitations = []uint{*f.request.InvitationID}
					s.People = []uint{f.person.ID}
				}
				if _, err := eraseAuditDetailsIn(tx, s); err != nil {
					return err
				}
			}
			finals = append(finals, dumpRows(t, tx, "audit_logs", "id", "true"))
			return aborted
		})
		if !errors.Is(err, aborted) {
			t.Fatal(err)
		}
	}
	if finals[0] != finals[1] || finals[1] != finals[2] {
		t.Fatal("erasure order changed final audit rows")
	}
}

func TestAuditErasureTeenRealLifecycles(t *testing.T) {
	for _, lifecycle := range []string{"L2", "L3", "L4", "L5", "L5_D_is_E"} {
		t.Run(lifecycle, func(t *testing.T) {
			app := testApp(t)
			admin, gid := openGroup(t, app, "Teen audit lifecycle")
			admin.ip = "192.0.2.2"
			enabler := newTestUser(t, app, "audit-enable", erasurePW, models.RoleAdmin)
			ec := loginAPI(t, app, enabler.Email, erasurePW)
			ec.ip = "192.0.2.5"
			confirmer := newTestUser(t, app, "audit-confirm", erasurePW, models.RoleAdmin)
			lc := loginAPI(t, app, confirmer.Email, erasurePW)
			lc.ip = "192.0.2.6"
			consenter := newTestUser(t, app, "audit-consent", erasurePW, models.RoleAdmin)
			kc := loginAPI(t, app, consenter.Email, erasurePW)
			kc.ip = "192.0.2.7"
			f := newAuditTeenFixture(t, app, admin, gid, "audit-"+strings.ToLower(lifecycle))
			// Approvals and staged completion are real writers; only setting the birthday
			// is clock setup, shared with the age-rollover tests.
			switch lifecycle {
			case "L2":
				apiDo(t, kc, "POST", consentPath(&f.user), map[string]string{"scope": "account", "status": "granted", "evidenceText": "Signed consent"}, 200)
				apiDo(t, lc, "POST", "/api/v1/admin/guardian-links/"+itoa(f.link.ID)+"/confirm", map[string]string{"checkNote": "Relationship verified"}, 200)
			case "L3":
				if n, err := app.rolloverAges(setStagedBirthdayToday(t, app, f)); err != nil || n != 1 {
					t.Fatalf("birthday: %d %v", n, err)
				}
			case "L4":
				apiDo(t, admin, "PUT", "/api/v1/admin/users/"+itoa(f.user.ID), map[string]string{"status": "disabled"}, 200)
				if n, err := app.rolloverAges(setStagedBirthdayToday(t, app, f)); err != nil || n != 1 {
					t.Fatalf("suspended birthday: %d %v", n, err)
				}
				apiDo(t, ec, "PUT", "/api/v1/admin/users/"+itoa(f.user.ID), map[string]string{"status": "active"}, 200)
			case "L5", "L5_D_is_E":
				if lifecycle == "L5_D_is_E" {
					ec = admin
				}
				apiDo(t, admin, "PUT", "/api/v1/admin/users/"+itoa(f.user.ID), map[string]string{"status": "disabled"}, 200)
				apiDo(t, kc, "POST", consentPath(&f.user), map[string]string{"scope": "account", "status": "granted", "evidenceText": "Signed consent"}, 200)
				apiDo(t, lc, "POST", "/api/v1/admin/guardian-links/"+itoa(f.link.ID)+"/confirm", map[string]string{"checkNote": "Relationship verified"}, 200)
				apiDo(t, ec, "PUT", "/api/v1/admin/users/"+itoa(f.user.ID), map[string]string{"status": "active"}, 200)
			}
			var rows []models.AuditLog
			if err := app.DB.Where("action = ? OR (action = ? AND target_type = 'registration')", audit.HouseholdPersonConverted, audit.RegistrationChanged).Order("id").Find(&rows).Error; err != nil {
				t.Fatal(err)
			}
			if len(rows) != len(f.rows)+1 {
				t.Fatalf("lifecycle transfer rows %d, want %d", len(rows), len(f.rows)+1)
			}
			var d models.User
			if err := app.DB.Where("email = ?", "admin@example.com").First(&d).Error; err != nil {
				t.Fatal(err)
			}
			h := f.person.HouseholdID
			r := auditConversionRequestRow{PersonID: f.person.ID, InvitationID: *f.request.InvitationID, UserID: f.user.ID, HouseholdID: &h, Marker: lifecycle == "L3" || lifecycle == "L4"}
			if got := classifyAuditConversion(r, rows, true); got.Reason != "completion" {
				t.Fatalf("current-history classification %+v", got)
			}
			before := dumpRows(t, app.DB, "audit_logs", "id", "action = ? OR (action = ? AND target_type = 'registration')", audit.HouseholdPersonConverted, audit.RegistrationChanged)
			for _, uid := range []uint{*f.request.RequestedBy, f.holder.ID, enabler.ID, confirmer.ID, consenter.ID, f.user.ID, d.ID, d.ID} {
				out := runAuditErasure(t, app.DB, erasureSubject{ID: uid})
				if uid == d.ID && (out.Conversion.ByReason["history_unestablished"] != int64(len(rows)) || out.Conversion.Blanked != 0) {
					t.Fatalf("default G-8 report %+v", out.Conversion)
				}
				if before != dumpRows(t, app.DB, "audit_logs", "id", "action = ? OR (action = ? AND target_type = 'registration')", audit.HouseholdPersonConverted, audit.RegistrationChanged) {
					t.Fatal("completion IP changed")
				}
			}
		})
	}
}

// The census pins normalized audit inputs and guardian helper calls. Comments,
// formatting and unrelated statements in the enclosing functions are ignored.
// Metadata maps pin their key sets, including keys added after construction.
// After an input changes, review its attribution rule before updating its pin.
// No files are parsed by executing them or by loading application state.
var auditWriterCensus = map[string]string{
	// System maintenance: no actor or IP, fixed host-removal metadata and a
	// version/count-only sweep marker. The two existing erasure writers now
	// accept a nil system actor, preserving their nonzero admin attribution.
	"account_erasure_tombstones.go:eraseTombstoneOnce":      "330d5e241aaacac7e1006fac835afd5b17c2a60b3a98b20dc872f94801529504",
	"account_erasure.go:eraseMessagesIn":                    "74a2cf34c5b654969f31847517ec142532eeed3759e3d9592f2b3b31bb862f2d",
	"account_erasure_households.go:eraseHouseholdRecordsIn": "d8b7f12b5e706b009ed3a108794de19320ae4a0e2b2704d010c6e0e624a45c5e",
	// T07B-2b: the page-view row of the reason review (no IP, actor the admin;
	// meta keys count and source). Decision rows go through audit.WriteAll.
	"account_erasure_reasons.go:reasonPageAttempt":                "082539d5d3b5760bdd1da6e34182135498af4857c2b0d630042e62e263d16b23",
	"age_rollover.go:flagHouseholdChild13":                        "1804d1f969590562b8e2282f42635834e8f5a3841b0c9679f4b400a4efe5d866",
	"age_rollover.go:rolloverTeen":                                "4139500705a3da764c067accd4f73fefed8e9c88aa696be6dbc26978d7dbe20b",
	"api_account_requests.go:apiConfirmAccountSetup":              "f97b48dcb56a3e178e94d2c297f260fa526f176e68252f55b8085d4a774e0acb",
	"api_account_requests.go:apiSetSelfSignup":                    "46f34cd7e73d9cf344b3e9378c76b811b88b06d10a20b7f495aadac78da63e82",
	"api_account_requests.go:decideAccountRequest":                "18bb2e5e2f8af270aef775f8a7cdbdf8f7eaa0f5d151615576dc938bd4d21fe9",
	"api_account_requests.go:expirePendingRequests":               "b30d60d413fdc04420d5ef85fed5141945bbad23cd3c882a19a4e160d067446f",
	"api_account_requests.go:storeAccountRequest":                 "a2a12aeb0ea4183b87c6773c1b5ba0f75b74d9bc2296be8c6edf2c4070aef3ec",
	"api_admin.go:apiAdminCreateUser":                             "6d94b5f538e0ac21ef2812e65b5bf9dd6da4434ebf5635c0fbeb6e8df06ec1c4",
	"api_admin.go:apiAdminDeleteUser":                             "beda70946e599d9df50effed52b98044534ae7c33f90272ec47c9c3b7d371db2",
	"api_admin.go:apiAdminResetPassword":                          "a5986e9dbf1710c87fec4483407977aa9f587bc64c8619363aef2f26d72c514f",
	"api_admin.go:apiAdminUpdateUser":                             "393506599017bf070142cef9cf59a88b9bd08cb73a34937d8257c64da2abffc2",
	"api_admin.go:decideGroup":                                    "faa6da1ee041b4d39f734458a51662ca5be8359d0ebc31c4a8e811ac897c1f2f",
	"api_admin.go:decideGuardianLink":                             "0d2559e7a9ccefe3156c283990dc4653610534becb9e47c4a52a0ab473c50cf5",
	"api_admin_household.go:apiAdminAddHouseholdPerson":           "8cf7bd69171a89be85dc1274e7086c190bcf9ad4b70f9ab22d10b0dc81c4c385",
	"api_admin_household.go:apiAdminCreateHousehold":              "94247b2de1067ca36bff02c0f6b171ca43b0d7ef09ea9677c58633c5315a341c",
	"api_admin_jobs.go:decideDeadJob":                             "04e9851ad53e2151ac2bc13599645fe06b1025aa77319bd291be047403485d08",
	"api_admin_suppressions.go:apiAdminClearSuppression":          "9044a4250a72408b0ccb7354305dd22e93dd2082f630b5993b7586c79035b4ea",
	"api_auth.go:apiChangePassword":                               "09345a72be2ea9e67394b3ee2bec5e8c127451fb3fd6e21978d7498994fffc18",
	"api_auth.go:apiConfirmPasswordReset":                         "ca2949268c280855e19fb399c0a1a707a9d9a827fcfcf552b0e6bd255de50fc2",
	"api_auth.go:apiRequestPasswordReset":                         "dc3a3d5d37afce904a46dd6dd87342be9b49ea3b9379a2d2556f0f889158683e",
	"api_contact.go:addAuditAction":                               "ea9a9c1e495429e447216252cea05dc7ac323e24fa12a30a4191429f7de7aaf8",
	"api_contact.go:auditRefusal":                                 "bfe884ae54cfeb1f8224d9cf33ad758168262cbc65502778d8d0561890191f6d",
	"api_contact_history.go:apiExportContactHistory":              "5df09267b49abc795ee0f42a03f5d376bd233f87e77455d53e529b8068a97a24",
	"api_departure.go:applyDeparture":                             "835354a095c0a480e8499c4ecb04f74092dfd2bce845699898fb141b9b9ab9a2",
	"api_email_change_cancel.go:apiCancelEmailChange":             "edb9bc16427aa877d8e6cea6227d100ff8c6d3124e9ee1aea9abc3ecc48ce1da",
	"api_event_access.go:apiAddEventHost":                         "3c75c6258e7fb4f2157ed6881f387010f61f5f7200b05a4d6f18a714e7af3e48",
	"api_event_access.go:apiCreateChurchEvent":                    "c1c78b809e4b9667e3c10965cbe64968bbfe75dc0465c96f5cbb696f90795b90",
	"api_event_access.go:apiRemoveEventHost":                      "ad9c92d06c87eba8f11e447dbb48a98c0f1b6501976a3ece6c8ec8fe0db58e8d",
	"api_event_access.go:apiSetEventVisibility":                   "e02eb8a1db6b35cd9b3bfc7e4f3fd8139a2c4205b264890122cb16327487d3d5",
	"api_events.go:apiCreateEvent":                                "e2e4bad27965d7ff77e3cc1d09469e6b8843cb91eff5d5c145c872fac2b92c9d",
	"api_events.go:apiDeleteEvent":                                "04cdd2b9eb8a0e77200e2c155a81950a03161c9e5868278dad9de6cbeac64b54",
	"api_events.go:restoreSeriesDates":                            "ee6a331db732803f1fb43f4be87f742df27e444cfbfb01967be4c40b063180f1",
	"api_feeds.go:apiCreateMyCalendarFeed":                        "4804978e0c2abafcb9ea1234cde1fc604bf1c3cce5bb3ad79c87f8b5fa1f1510",
	"api_feeds.go:apiUpdateMyCalendarFeed":                        "9ea28d336e14eea85b7fadb379e258108df222066d72b736f387d91672712106",
	"api_feeds.go:revokeCalendarToken":                            "83f1472c1958c30af21ed73ce001befedb879dcb35d2c0fc3fbfb531140505e9",
	"api_group_visibility.go:applyGroupTightening":                "97e7cb46187560511825bffe7b5386fbbc68feabc48bb439dac411abef869e07",
	"api_group_visibility.go:cancelDateLosers":                    "fb0f531082cb9e5a6d2ed6d2ebef78d9f7bd846f491ce5be9f6b2b23743b0e62",
	"api_groups.go:apiUpdateGroup":                                "066a15def11b07f00492ad420206068b7ff977b43f8d492b9be3f8f976c9192d",
	"api_groups.go:auditAndNotifyLeadership":                      "10cc718888efbfe112b1f9d198a48a9a9c7e8c74ba90ec721ad35ba8d555316c",
	"api_groups.go:changeRoleIn":                                  "6c81ce1bd5f4f91603428940c43e97b1d3d30ef4c5f5dc52441dc0126e23d208",
	"api_groups.go:deleteGroup":                                   "fec7d685ed8b3700c060f747c0c0bc2a1b758fef582f539ec837dd198058edbd",
	"api_guest.go:apiCancelGuestRegistration":                     "d92867ce53e9aa1d9e3f635bee6ae5946c7aa0fed892bebc76beba28bb017fbb",
	"api_guest.go:createGuestUpgradeRequest":                      "d4efcca0c23809550c5f9d8996e03fad7134231186eb151392530f8a17e6341c",
	"api_handoff.go:demoteEverywhere":                             "3942c7789f1b83307e31f2d2cda7354ea0f7e59338749e79a4f46afca124c159",
	"api_handoff.go:handOffLeadership":                            "393728be02db620976ea271722aca2900d1fd6fbf558b85cb4c7d3959f265c08",
	"api_handoff.go:restoreMemberships":                           "7e5274985450108d91a71e530fe4d6256b0a330c3361cff5a271bfb0cf037773",
	"api_host_handoff.go:handOffHosting":                          "53914ec57672b0fb64eb4f599b0c84df94da0f98985fb4ac5541c1081831a925",
	"api_host_handoff.go:removeDeletedHostRows":                   "911f1f768d2205b4e9a240a9cf89a1a62455be0e420f152285a9917e52edc79d",
	"api_household_conversion.go:acceptHouseholdConversion":       "6cc4414eef3eb20cd4c91809c39acac928e40ff1cfe0fedb0bc0b24d9c603e26",
	"api_household_conversion.go:approveHouseholdConversion":      "cfcbf9731a399602d784713f4057943e87cba061ccc8bdabacb603d5b2574d3e",
	"api_household_conversion.go:createConversionApprovalRequest": "b3446035530b204e0fd55527ba5da2ddd14c3e619cf3c50141aaec152b9fdd29",
	"api_household_conversion.go:issueHouseholdConversion":        "9f6f439dcead0603cd7951269d59d2ee82732c3609c9b37bb7ff37db4e44e679",
	"api_household_conversion.go:revokeConversionInvitation":      "0fefea42145a8ecba0cd41cf715a9856d9865a070b65d847987d1d3526dd1d29",
	"api_household_conversion.go:transferHouseholdConversion":     "e1908caf0ce2fcf2d264c520c2f9c4adda50c7625277295c99d6678b8eef369f",
	"api_invitations.go:apiAcceptInvitation":                      "60f7cc3dd7e17625a05e396cae326b52bd06a2fc9672e65a7d60e5d50c62353f",
	"api_invitations.go:apiCreateInvitation":                      "45bbb658cdec5c7de60681c0d505544e2c25a011e8fa9bbb38b9e978c79d1a7e",
	"api_invitations.go:writeInvitationRefusal":                   "7b02d5b25e7c2b17d5c628c82fa102075c93ba82255b0a21b4eecb4c7dae6e0f",
	"api_message_history.go:apiRetryMessage":                      "28f49307508e5f77df4a6aaebc4c63d59a94aca0cccb26e86b207e8d4024ce19",
	"api_message_preview.go:apiTestMessage":                       "2ad44cd111947e52717b8a4d257d3fd6c307ab0b8be1d938139a9425f62c952a",
	"api_message_settings.go:saveSetting":                         "94b67b44538247bff4e69419b7c8e347b1a204eb0bdef8b7626194279525c245",
	"api_messages.go:apiCancelMessage":                            "072e942199cda3fee26b59da2a7cd384b18bb75c04052b6873cd0ded6b108fd7",
	"api_messages.go:apiScheduleMessage":                          "c3812387e4ed2d94a8099978724a83523a859e25f6edd41167d14c35be69f5ca",
	"api_messages.go:apiUpdateMessage":                            "488f229db14d0f858b82d153bd8a76dbe2e3d4f1d46a6c57564a9e9c73df9928",
	"api_occurrence_mutations.go:auditManagerDateRefusal":         "165df28b1240be82f52433d7966d107c1e010ffeab8d6dbabbfa99e8a4cd4086",
	"api_occurrence_mutations.go:mutateOwnOccurrence":             "6bd6d9b4625c83e59bcce3f140718bba4cb650d2c44048d222191f4fec848a54",
	"api_occurrence_visibility.go:changeDateVisibility":           "f78c0e667a997d0c1b44f616eba82000f7e398550811a27d6301dafe778eda6e",
	"api_profile.go:apiAddHouseholdPerson":                        "83c8ef22c21d627b9ef84d66838ad7dc8491b5dbc5de3d0ea4dd2e77a203609a",
	"api_profile.go:apiDeleteHouseholdPerson":                     "0988a9ba35deecc848bebbda7af43b773f91042364c274cf13d5972f56cb85de",
	"api_profile.go:apiRequestEmailChange":                        "47dde4ece9b10109a2a709861dd8ede7b41f840030b00f94e5e2c268efbae924",
	"api_profile.go:apiUpdateHouseholdPerson":                     "91057afc72a767e94c4ff02c02a890b1bbd9f72c182f173c1f587e584c88aa26",
	"api_profile.go:apiUpdateMe":                                  "6df73176956edcdffc542634fdd38ed1c316384fa629b31d68f49779ec20cb95",
	"api_profile.go:apiUpdateMyHousehold":                         "2c02ddc4bf65e78173d88a57d0be58945b0dff62e6530b09d2748101589b4582",
	"api_profile.go:apiVerifyEmailChange":                         "ed76d52dcbe727a70cb2f4dbce8a3271b06e396d7be527dbde1361c165a47950",
	"api_profile.go:ensureHousehold":                              "9236b5e942983ff2454573eae5fb0b2aec6146c461772d6723207f5ed544cbf8",
	"api_profile.go:revokeGuardianLinks":                          "72f5343ffde6bf01d84532da5a2e7f5e927e5385b992a3032a86ff0d01f933da",
	"api_push.go:apiPushSubscribe":                                "83847c8032633da5bab06adb3c85a625a493d7d2611846e6b518027f9f22cead",
	"api_push.go:apiPushTest":                                     "72c9c905b24bb81242cff2694dbdee3833e47cb298c7e1ca5dd9494b4e6b403a",
	"api_registrations.go:addAccountRegistrationLocked":           "e846bc3c56efea3f161d41ac9eb3594308b3493a3a9bdfc1d0a875929a0b76ff",
	"api_registrations.go:apiAddRegistration":                     "f0f0e9f448c5f0827c38a1aa17338174cd4ac6cd6bbca2f94f334679df3b90a5",
	"api_registrations.go:apiPromoteRegistration":                 "bfae49fa5d285bd4a62dfa54bf07cce99018741ccae89e6f278fe721a7a266a8",
	"api_registrations.go:apiRemoveRegistration":                  "421058046fa2065b397cf0ff8aacd77eb404f316cc849840247abf0cd5da5467",
	"api_registrations.go:auditRegistration":                      "011710eca9426fd208c2fdb6e0a05ce1d0e5ec0a1ed6782306d219f634afdd14",
	"api_registrations.go:registrationMeta":                       "8236e28a52344add82f99db6ef37c4957bf2019ef8cf20563e76e13442a0a795",
	"api_room_bookings.go:apiSetDateRoom":                         "be47016c5047112df855bb542f0c456c5e16df55e2f31efef8c60042e3bd183b",
	"api_room_feeds.go:apiAdminCreateRoomFeed":                    "e8017f2ddcb5315ab98d24dd76d573f7370388622ed80718017febe611a3645f",
	"api_room_feeds.go:apiAdminRevokeRoomFeed":                    "005bdd3c42b72a9573ed1a99b50daacd3ee179774dfa72b0dc5bb20d09127c39",
	"api_room_locations.go:apiAdminMatchLocation":                 "41d378115f2b99224020e0f4111a0680c603e43c79cdba3de188db6e3b9bc649",
	"api_room_requests.go:apiConfirmRoomRequest":                  "62159e14c4f4f61d298da229012e101b0610513eb562705d5093ee2893d9acd7",
	"api_room_requests.go:apiRejectRoomRequest":                   "c456ce9c686358af5eb186fb835d3892975779cc85ba6bc59ab0fe2e77418228",
	"api_room_requests.go:apiSetRoomApproval":                     "89b78efc5767a2233bfef9b1bc4904070032b9a37d15dc97672c14da95cadece",
	"api_room_series.go:bookWholeSeries":                          "6a35ca593ce32d5d8a02f816bdbda89d2f18f38ad7cdaf242007480e7f6c6dc2",
	"api_rooms.go:apiAdminCreateRoom":                             "00c14cd2b3e4d506f59158cf6a676fe89be5a8183760e27310b12b9e183b8f15",
	"api_rooms.go:apiAdminUpdateRoom":                             "408c699d62a155af44bde0954c969b70965c0c9c583ded3ffc5a0464ffa5a30d",
	"api_rooms.go:apiSetRoomsEnabled":                             "62a0585d873b48fbea49ece1118cfcff60cac9aecbd7bc6494e95bf0603c0cf4",
	"api_teen_consent.go:changeTeenConsent":                       "f7124d9ee26a3b16b8f67a5fa0fadb7867bcd7b5d9e213e83f3efa8556d9449b",
	"api_teen_guardians.go:apiClaimTeenGuardian":                  "059457aa963c16581146d2802d7bff428783101588e5a44a0cd3041cd868f55a",
	"api_teen_guardians.go:nominateTeenGuardian":                  "c1ef0fab390bdda6d4522037a4a396e79108b8121784f33c9ac6c20058244672",
	"api_teen_guardians.go:requestTeenGuardian":                   "0dae6b14daea71f5b6b03337a3c51bf2d6c01fc043d48addc835eaed49e25597",
	"contact_lifecycle.go:contactAudit":                           "20ffcca62a0ddd77d99d37eb8b6f073b0d306926150da38db7cc55f6d3ad54cf",
	"internal/audit/audit.go:Write":                               "79ad063a312631bd946658228d06e4514b85d95cad77c3ac226e7b665717a4fa",
	"messages_send.go:refuseMessage":                              "51b149eb634bdc1277ecb6f96ff24904257859070f3f32b0512ae982e502419a",
	"messages_send.go:settleMessage":                              "d062aef9c6cbfb58aa679300d726b57bcd6096f3d250c39ec23c80ebdfc12332",
	"push_keys.go:ensureVAPIDKeys":                                "d94cd4d219fe7c7184552981dfb2a3ae2438fe4edf876c61e179f5ce2c1c655b",
	"push_keys.go:rotateVAPIDKeys":                                "6bb369832f0c2b9c52667a519cedfa210088ac3f2503af03d8effc56c21b4ce7",
	"registration_access.go:accessSnapshotMeta":                   "f9a7ba0c51c85fb68d838a607612b697752b99e01ffd896593e72465702a7ab0",
	"registration_access.go:writeAccessSnapshot":                  "a47511c490d32cfc8c87792af6da140156e8ce79a2b567c3c1df594991c09714",
}

type auditSourceCensus struct {
	Writers map[string]string
	Sites   []string
	Actions map[string]bool
}

func auditCensusExpr(_ *token.FileSet, n ast.Node) string {
	var b bytes.Buffer
	// Discard source positions so line wrapping cannot change an input pin.
	_ = format.Node(&b, token.NewFileSet(), n)
	return b.String()
}

// This is parse-only, not data-flow analysis. Local metadata identifiers are
// followed to their literal keys and indexed writes within the same function.
// Cross-function metadata helpers have their own key-set pins below.
func auditCensusMeta(fset *token.FileSet, fn *ast.FuncDecl, expr ast.Expr) string {
	keys := map[string]bool{}
	addMap := func(expr ast.Expr) {
		if lit, ok := expr.(*ast.CompositeLit); ok {
			if _, ok := lit.Type.(*ast.MapType); ok {
				for _, elt := range lit.Elts {
					if kv, ok := elt.(*ast.KeyValueExpr); ok {
						keys[auditCensusExpr(fset, kv.Key)] = true
					}
				}
			}
		}
	}
	if expr != nil {
		addMap(expr)
	}
	id, local := expr.(*ast.Ident)
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if expr == nil {
			if e, ok := n.(ast.Expr); ok {
				addMap(e)
			}
		}
		if a, ok := n.(*ast.AssignStmt); ok {
			for i, lhs := range a.Lhs {
				if name, ok := lhs.(*ast.Ident); ok && local && name.Name == id.Name && i < len(a.Rhs) {
					addMap(a.Rhs[i])
				}
				if index, ok := lhs.(*ast.IndexExpr); ok && (expr == nil || (local && auditCensusExpr(fset, index.X) == id.Name)) {
					keys[auditCensusExpr(fset, index.Index)] = true
				}
			}
		}
		// accessSnapshotMeta adds optional keys through this local helper.
		if expr == nil {
			if c, ok := n.(*ast.CallExpr); ok && len(c.Args) > 0 {
				if name, ok := c.Fun.(*ast.Ident); ok && name.Name == "optional" {
					keys[auditCensusExpr(fset, c.Args[0])] = true
				}
			}
		}
		return true
	})
	list := make([]string, 0, len(keys))
	for key := range keys {
		list = append(list, key)
	}
	sort.Strings(list)
	shape := strings.Join(list, ",")
	if _, ok := expr.(*ast.CompositeLit); expr != nil && !ok {
		shape = auditCensusExpr(fset, expr) + ":" + shape
	}
	return shape
}

func auditCensusEntry(fset *token.FileSet, fn *ast.FuncDecl, lit *ast.CompositeLit) string {
	var fields []string
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			fields = append(fields, auditCensusExpr(fset, elt))
			continue
		}
		key := auditCensusExpr(fset, kv.Key)
		value := auditCensusExpr(fset, kv.Value)
		if key == "Meta" {
			value = auditCensusMeta(fset, fn, kv.Value)
		}
		fields = append(fields, key+"="+value)
		// Pin assignments of a locally selected action without hashing unrelated
		// statements in the function that makes the selection.
		if id, ok := kv.Value.(*ast.Ident); ok && key == "Action" {
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if a, ok := n.(*ast.AssignStmt); ok {
					for i, lhs := range a.Lhs {
						if name, ok := lhs.(*ast.Ident); ok && name.Name == id.Name && i < len(a.Rhs) {
							fields = append(fields, "Action="+auditCensusExpr(fset, a.Rhs[i]))
						}
					}
				}
				return true
			})
		}
	}
	sort.Strings(fields)
	return strings.Join(fields, "\n")
}

func readAuditSourceCensus(sources map[string][]byte) (auditSourceCensus, error) {
	out := auditSourceCensus{Writers: map[string]string{}, Actions: map[string]bool{}}
	text := guardkit.Sources{}
	for name, src := range sources {
		text[name] = string(src)
	}
	parsed := text.Parse(0)
	if len(parsed.Problems) > 0 {
		return out, errors.New(strings.Join(parsed.Problems, "; "))
	}
	fset := parsed.Fset
	for _, pf := range parsed.Files {
		name, file := pf.Path, pf.AST
		aliases := guardkit.ImportNames(file, "fold/internal/audit")
		// The registry covers both named constants and the one local action literal.
		if name == "internal/audit/audit.go" {
			for _, decl := range file.Decls {
				if d, ok := decl.(*ast.GenDecl); ok && d.Tok == token.CONST {
					for _, spec := range d.Specs {
						v := spec.(*ast.ValueSpec)
						for _, value := range v.Values {
							if lit, ok := value.(*ast.BasicLit); ok && lit.Kind == token.STRING {
								s, err := strconv.Unquote(lit.Value)
								if err != nil {
									return out, err
								}
								out.Actions[s] = true
							}
						}
					}
				}
			}
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			var inputs []string
			if fn.Name.Name == "registrationMeta" || fn.Name.Name == "accessSnapshotMeta" {
				inputs = append(inputs, "metadata keys="+auditCensusMeta(fset, fn, nil))
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				if c, ok := n.(*ast.CallExpr); ok {
					if id, ok := c.Fun.(*ast.Ident); ok && (id.Name == "requestTeenGuardian" || id.Name == "nominateTeenGuardian") {
						inputs = append(inputs, auditCensusExpr(fset, c))
					}
					sel, ok := c.Fun.(*ast.SelectorExpr)
					if ok {
						if id, ok := sel.X.(*ast.Ident); ok && aliases[id.Name] && sel.Sel.Name == "Write" {
							inputs = append(inputs, "audit.Write")
							var expr bytes.Buffer
							_ = format.Node(&expr, fset, c)
							out.Sites = append(out.Sites, fmt.Sprintf("%s:%d %s", name, fset.Position(c.Pos()).Line, expr.String()))
						}
					}
					if id, ok := c.Fun.(*ast.Ident); ok && aliases["."] && id.Name == "Write" {
						inputs = append(inputs, "audit.Write")
						out.Sites = append(out.Sites, fmt.Sprintf("%s:%d dot-import Write", name, fset.Position(c.Pos()).Line))
					}
				}
				if c, ok := n.(*ast.CompositeLit); ok {
					isEntry := false
					if sel, ok := c.Type.(*ast.SelectorExpr); ok {
						if id, ok := sel.X.(*ast.Ident); ok && aliases[id.Name] && sel.Sel.Name == "Entry" {
							isEntry = true
						}
					}
					if id, ok := c.Type.(*ast.Ident); ok && aliases["."] && id.Name == "Entry" {
						isEntry = true
					}
					if isEntry {
						inputs = append(inputs, auditCensusEntry(fset, fn, c))
						for _, e := range c.Elts {
							if kv, ok := e.(*ast.KeyValueExpr); ok {
								if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Action" {
									if lit, ok := kv.Value.(*ast.BasicLit); ok && lit.Kind == token.STRING {
										action, err := strconv.Unquote(lit.Value)
										if err == nil {
											out.Actions[action] = true
										}
									}
								}
							}
						}
					}
					// Pin the storage adapter's actual field mapping too.
					if name == "internal/audit/audit.go" && fn.Name.Name == "Write" {
						if sel, ok := c.Type.(*ast.SelectorExpr); ok && sel.Sel.Name == "AuditLog" {
							inputs = append(inputs, auditCensusEntry(fset, fn, c))
						}
					}
				}
				if fn.Name.Name == "addAuditAction" {
					if sel, ok := n.(*ast.SelectorExpr); ok {
						if id, ok := sel.X.(*ast.Ident); ok && aliases[id.Name] {
							inputs = append(inputs, auditCensusExpr(fset, sel))
						}
					}
				}
				return true
			})
			if len(inputs) > 0 {
				out.Writers[name+":"+fn.Name.Name] = fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(inputs, "\n"))))
			}
		}
	}
	// The privacy action is defined locally and used through an identifier.
	out.Actions[contactPrivacyChanged] = true
	return out, nil
}

func auditProductionSources(t *testing.T) map[string][]byte {
	t.Helper()
	sources := map[string][]byte{}
	for path, src := range guardkit.Production(t) {
		if path != "account_erasure_audit.go" {
			sources[path] = []byte(src)
		}
	}
	return sources
}

func auditCensusProblems(c auditSourceCensus) []string {
	var problems []string
	for action := range c.Actions {
		if _, ok := auditIPAttribution[action]; !ok {
			problems = append(problems, "unclassified action: "+action)
		}
	}
	for action := range auditIPAttribution {
		if !c.Actions[action] {
			problems = append(problems, "registry action absent from census: "+action)
		}
	}
	for site, digest := range c.Writers {
		if auditWriterCensus[site] != digest {
			problems = append(problems, "changed or new writer: "+site+"; revisit inventory 6.5.1, actor/IP attribution and metadata keys")
		}
	}
	for site := range auditWriterCensus {
		if _, ok := c.Writers[site]; !ok {
			problems = append(problems, "missing writer: "+site)
		}
	}
	sort.Strings(problems)
	return problems
}

func TestAuditErasureWriterCensus(t *testing.T) {
	c, err := readAuditSourceCensus(auditProductionSources(t))
	if err != nil {
		t.Fatal(err)
	}
	if p := auditCensusProblems(c); len(p) > 0 {
		t.Fatal(strings.Join(p, "\n"))
	}
	if len(c.Sites) != 121 {
		t.Fatalf("audit.Write sites = %d, expected 121; review every changed site", len(c.Sites))
	}
	for _, site := range c.Sites {
		t.Log(site)
	}
}

func TestAuditErasureCensusRejectsMutations(t *testing.T) {
	sources := auditProductionSources(t)
	baseline, err := readAuditSourceCensus(sources)
	if err != nil {
		t.Fatal(err)
	}
	// An unrelated stale pin must not make every mutation appear rejected.
	if p := auditCensusProblems(baseline); len(p) > 0 {
		t.Fatal(strings.Join(p, "\n"))
	}
	for _, tc := range []struct{ name, file, writer, old, new string }{
		{"household erasure actor", "account_erasure_households.go", "eraseHouseholdRecordsIn", "Actor: actor", "Actor: &s.ID"},
		{"household erasure IP", "account_erasure_households.go", "eraseHouseholdRecordsIn", "IP: ip", `IP: ""`},
		{"household erasure metadata key", "account_erasure_households.go", "eraseHouseholdRecordsIn", `meta["personId"]`, `meta["renamedPersonId"]`},
		{"unclassified action", "api_household_conversion.go", "transferHouseholdConversion", "Action: audit.HouseholdPersonConverted", `Action: "unclassified.test_action"`},
		{"conversion identity key rename", "api_household_conversion.go", "transferHouseholdConversion", `"invitationId": inv.ID`, `"renamedInvitationId": inv.ID`},
		{"signup IP source", "api_teen_guardians.go", "nominateTeenGuardian", `"signup", req.IP`, `"signup", ""`},
		{"new guardian source", "api_teen_guardians.go", "apiClaimTeenGuardian", `"claim", c.ClientIP()`, `"new_source", c.ClientIP()`},
		{"actor expression", "api_profile.go", "apiUpdateMe", "Actor: &u.ID, Action: contactPrivacyChanged", "Actor: nil, Action: contactPrivacyChanged"},
		{"direct IP source", "api_profile.go", "apiUpdateMe", "IP: c.ClientIP()", `IP: ""`},
		{"indirect metadata key", "api_profile.go", "apiAddHouseholdPerson", `meta["guardianLinkId"]`, `meta["renamedGuardianLinkId"]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mutated := make(map[string][]byte, len(sources))
			for name, src := range sources {
				mutated[name] = src
			}
			if !bytes.Contains(sources[tc.file], []byte(tc.old)) {
				t.Fatal("mutation did not match")
			}
			mutated[tc.file] = bytes.Replace(sources[tc.file], []byte(tc.old), []byte(tc.new), 1)
			c, err := readAuditSourceCensus(mutated)
			if err != nil {
				t.Fatal(err)
			}
			site := tc.file + ":" + tc.writer
			before, exists := baseline.Writers[site]
			if !exists {
				t.Fatalf("mutation writer missing: %s", site)
			}
			if after, exists := c.Writers[site]; !exists || after == before {
				t.Fatalf("mutation did not change the expected input pin: %s", site)
			}
			p := auditCensusProblems(c)
			if !strings.Contains(strings.Join(p, "\n"), "changed or new writer: "+site+";") {
				t.Fatalf("source guard accepted mutation at %s: %v", site, p)
			}
			if tc.name == "unclassified action" && !strings.Contains(strings.Join(p, "\n"), "unclassified action: unclassified.test_action") {
				t.Fatalf("action guard didn't reject literal: %v", p)
			}
			t.Logf("recorded rejected mutation: %s: %s", tc.name, strings.Join(p, "; "))
		})
	}
}

func TestAuditErasureCensusIgnoresUnrelatedEdits(t *testing.T) {
	const source = `package main
import "fold/internal/audit"
func writer() {
 label := "before"
 meta := map[string]any{"userId": user.ID}
 meta["source"] = "signup"
 audit.Write(tx, audit.Entry{Action: audit.UserCreated, Actor: &actor.ID, IP: ip, Meta: meta})
 requestTeenGuardian(tx, guardian.ID, teen.ID, nil, "signup", req.IP)
 _ = label
}`
	read := func(src string) auditSourceCensus {
		t.Helper()
		c, err := readAuditSourceCensus(map[string][]byte{"writer.go": []byte(src)})
		if err != nil {
			t.Fatal(err)
		}
		if len(c.Writers) != 1 || len(c.Sites) != 1 {
			t.Fatalf("fixture writer missing: %+v", c)
		}
		return c
	}
	want := read(source)
	for _, edit := range []string{
		strings.ReplaceAll(source, "label", "renamed"),
		strings.Replace(source, `"before"`, `"after"`, 1),
		strings.Replace(source, "meta :=", "// Unrelated comment.\n meta :=", 1),
		strings.Replace(source, "Action: audit.UserCreated, Actor:", "Action: audit.UserCreated,\n Actor:", 1),
		strings.Replace(source, "req.IP)", "\n req.IP)", 1),
	} {
		if got := read(edit); !reflect.DeepEqual(got.Writers, want.Writers) {
			t.Fatalf("unrelated edit changed input pins: %v -> %v", want.Writers, got.Writers)
		}
	}
}

// The seven caller roles use distinct addresses. The inviter is an admin who
// manages this household, separate from the household's confirmed guardian.
func newAuditTeenFixture(t *testing.T, app *App, approver *apiClient, gid, name string) teenConversionFixture {
	t.Helper()
	holder, parent, _, _ := householdFamily(t, app, approver, name)
	parent.ip = "192.0.2.4"
	person := addMyPerson(t, parent, map[string]any{"firstName": "Teen " + name, "kind": "teen", "birthDate": teenBirthDate()})
	apiDo(t, approver, "POST", "/api/v1/admin/guardian-links/"+itoa(*person.GuardianLinkID)+"/confirm", map[string]any{"checkNote": "Known household"}, 200)
	eventID, _ := seedCappedEvent(t, app, approver, gid, intPtr(10), true)
	var rows []models.Registration
	now := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 2; i++ {
		at := now.AddDate(0, 0, 60+i)
		o := models.EventOccurrence{EventID: eventID, OriginalDate: calDate(at.Format("2006-01-02")), StartsAt: at, EndsAt: at.Add(time.Hour), Status: models.OccurrenceScheduled}
		if err := app.DB.Create(&o).Error; err != nil {
			t.Fatal(err)
		}
		r := models.Registration{OccurrenceID: o.ID, HouseholdPersonID: &person.ID, RegisteredBy: &holder.ID, Status: models.RegGoing, AttendanceMode: models.AttendanceInPerson, Source: "admin"}
		if err := app.DB.Create(&r).Error; err != nil {
			t.Fatal(err)
		}
		rows = append(rows, r)
	}
	inviter := newTestUser(t, app, name+"-inviter", erasurePW, models.RoleAdmin)
	ic := loginAPI(t, app, inviter.Email, erasurePW)
	ic.ip = "192.0.2.1"
	token := inviteHouseholdPerson(t, app, ic, person.ID, name+"-own@example.com")
	redeemer := anonClient(t, app, "192.0.2.3")
	apiDo(t, redeemer, "POST", "/api/v1/invitations/accept", map[string]any{"ageBand": "adult", "token": token, "password": erasurePW}, 200)
	req := pendingRequest(t, app, name+"-own@example.com")
	apiDo(t, approver, "POST", "/api/v1/admin/account-requests/"+itoa(req.ID)+"/approve", map[string]any{}, 200)
	if err := app.DB.First(&req, req.ID).Error; err != nil {
		t.Fatal(err)
	}
	var user models.User
	if err := app.DB.First(&user, *req.ApprovedUserID).Error; err != nil {
		t.Fatal(err)
	}
	var link models.GuardianLink
	if err := app.DB.Where("teen_user_id = ? AND guardian_user_id = ?", user.ID, holder.ID).First(&link).Error; err != nil {
		t.Fatal(err)
	}
	return teenConversionFixture{holder: holder, parent: parent, person: person, request: req, user: user, link: link, rows: rows}
}

func TestAuditErasureMalformedConversionNominationSibling(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "Nomination sibling")
	admin.ip = "192.0.2.2"
	f := newAuditTeenFixture(t, app, admin, gid, "audit-sibling")
	var d models.User
	if err := app.DB.Where("email = ?", "admin@example.com").First(&d).Error; err != nil {
		t.Fatal(err)
	}
	var good models.AuditLog
	if err := app.DB.Where("action = ? AND meta_json->>'source' = 'household_conversion'", audit.GuardianLinkRequested).First(&good).Error; err != nil {
		t.Fatal(err)
	}
	// 15f simulated malformed sibling after the real approval writer. It is
	// discovered by text, retained alone, and never prevents the valid scrub.
	bad := good
	bad.ID = 0
	bad.TargetID += 100000
	var meta map[string]any
	if err := json.Unmarshal([]byte(good.MetaJSON), &meta); err != nil {
		t.Fatal(err)
	}
	meta["teenUserId"] = itoa(f.user.ID)
	raw, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	bad.MetaJSON = string(raw)
	if err := app.DB.Create(&bad).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.First(&bad, bad.ID).Error; err != nil {
		t.Fatal(err)
	}
	// An otherwise identical nomination with an already empty IP contributes
	// to neither retained-IP diagnostic, including on the first erasure.
	blanked := bad
	blanked.ID = 0
	blanked.IP = ""
	if err := app.DB.Create(&blanked).Error; err != nil {
		t.Fatal(err)
	}
	out := runAuditErasure(t, app.DB, erasureSubject{ID: d.ID})
	if out.MalformedConversionLinksRetained != 1 || out.BlankedByClass[auditIPCApproval] != 1 || out.KeptByClassAndAction[auditIPRetainUnattributed][audit.GuardianLinkRequested] != 1 {
		t.Fatalf("sibling report: %+v", out)
	}
	assertAuditErasureRows(t, app.DB, []models.AuditLog{good, bad}, map[uint]bool{good.ID: true}, nil)
	again := runAuditErasure(t, app.DB, erasureSubject{ID: d.ID})
	if again.IPsBlanked != 0 || again.MalformedConversionLinksRetained != 1 {
		t.Fatalf("retry sibling counts %+v", again)
	}
	if err := app.DB.Model(&bad).Update("ip", "").Error; err != nil {
		t.Fatal(err)
	}
	again = runAuditErasure(t, app.DB, erasureSubject{ID: d.ID})
	if again.MalformedConversionLinksRetained != 0 || again.KeptByClassAndAction[auditIPRetainUnattributed][audit.GuardianLinkRequested] != 0 {
		t.Fatalf("empty malformed IP still counted: %+v", again)
	}
}

func TestAuditErasurePreservesAccessProvenanceTrigger(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	f.client.ip = "192.0.2.4"
	apiDo(t, f.client, "POST", occurrenceMutationPath(f, "decline"), nil, 200)
	var snapshots []models.AuditLog
	if err := f.app.DB.Where("action IN ?", []string{audit.OccurrenceAccessAdded, audit.OccurrenceAccessEnded, audit.OccurrenceBlocked}).Order("id").Find(&snapshots).Error; err != nil {
		t.Fatal(err)
	}
	if len(snapshots) < 2 {
		t.Fatal("real access history missing")
	}
	runAuditErasure(t, f.app.DB, erasureSubject{ID: f.recipient.ID})
	runAuditErasure(t, f.app.DB, erasureSubject{ID: f.issuer.ID})
	blank := map[uint]bool{}
	for _, r := range snapshots {
		blank[r.ID] = r.ActorID != nil && (*r.ActorID == f.recipient.ID || *r.ActorID == f.issuer.ID)
	}
	assertAuditErasureRows(t, f.app.DB, snapshots, blank, nil)
	// The database requires the complete added and ended metadata before this
	// real grant can be deleted. Its IP has no part in that evidence check.
	if err := f.app.DB.Delete(&models.Registration{}, f.access.ID).Error; err != nil {
		t.Fatalf("registration_access_provenance after IP scrub: %v", err)
	}
}

func TestAuditErasureInvitationReachAfterAddressChange(t *testing.T) {
	app := testApp(t)
	inviter := newTestUser(t, app, "audit-inviter", erasurePW, models.RoleAdmin)
	first := newTestUser(t, app, "current@example.com", erasurePW, models.RoleMember)
	other := newTestUser(t, app, "previous@example.com", erasurePW, models.RoleMember)
	// Labelled ownership fixtures use the same acceptance and invitation writers
	// as the existing INV capture tests. The current address is deliberately B.
	old := erasureInvitation(t, app.DB, inviter.ID, "previous@example.com", true, nil)
	foreign := erasureInvitation(t, app.DB, inviter.ID, "previous@example.com", true, nil)
	accepted := auditErasureLog(t, app.DB, audit.InvitationAccepted, "invitation", old, &inviter.ID, map[string]any{"userId": first.ID})
	foreignAccepted := auditErasureLog(t, app.DB, audit.InvitationAccepted, "invitation", foreign, &inviter.ID, map[string]any{"userId": other.ID})
	created := auditErasureLog(t, app.DB, audit.InvitationCreated, "invitation", old, &inviter.ID, map[string]any{"email": "previous@example.com"})
	foreignCreated := auditErasureLog(t, app.DB, audit.InvitationCreated, "invitation", foreign, &inviter.ID, map[string]any{"email": "previous@example.com"})
	s := captureErasureSubject(first)
	if err := captureErasureSetsIn(app.DB, &s); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.Invitations, []uint{old}) {
		t.Fatalf("INV reach: %+v", s.Invitations)
	}
	runAuditErasure(t, app.DB, s)
	assertAuditErasureRows(t, app.DB, []models.AuditLog{accepted, foreignAccepted, created, foreignCreated}, map[uint]bool{accepted.ID: true}, map[uint][]string{created.ID: {"email"}})
}

func TestAuditErasureCoincidentApproverAndInviter(t *testing.T) {
	f := newAuditAdultFixtureWithInviter(t, true)
	if f.inviter.ID != f.admin.ID {
		t.Fatal("fixture must use the same inviter and approver")
	}
	before := dumpRows(t, f.app.DB, "audit_logs", "id", "action = ? OR (action = ? AND target_type = 'registration')", audit.HouseholdPersonConverted, audit.RegistrationChanged)
	out := runAuditErasure(t, f.app.DB, erasureSubject{ID: f.admin.ID})
	if out.Conversion.ByReason["history_unestablished"] != 3 {
		t.Fatalf("coincident roles: %+v", out.Conversion)
	}
	if before != dumpRows(t, f.app.DB, "audit_logs", "id", "action = ? OR (action = ? AND target_type = 'registration')", audit.HouseholdPersonConverted, audit.RegistrationChanged) {
		t.Fatal("coincident actor was treated as conversion caller")
	}
}

func TestAuditErasureConversionFallbackTotalsCountRowsOnce(t *testing.T) {
	f := newAuditAdultFixture(t)
	parent := loginAPI(t, f.app, f.inviter.Email, erasurePW)
	person := addMyPerson(t, parent, map[string]any{"firstName": "Second adult", "kind": "adult", "birthDate": "1990-01-01"})
	token := inviteHouseholdPerson(t, f.app, parent, person.ID, "audit-second-convert@example.com")
	acceptConversion(t, f.app, token, 200)
	ac := loginAPI(t, f.app, f.admin.Email, erasurePW)
	req := pendingRequest(t, f.app, "audit-second-convert@example.com")
	apiDo(t, ac, "POST", "/api/v1/admin/account-requests/"+itoa(req.ID)+"/approve", map[string]any{}, 200)
	// Simulated wrong-typed C row in their shared household is found by arm d
	// for both requests. It is one retained audit entry, not two entries.
	cloneAuditCandidate(t, f, audit.HouseholdPersonConverted, removeConversionIdentity)
	out := runAuditErasure(t, f.app.DB, erasureSubject{ID: f.admin.ID})
	if out.Conversion.Groups != 2 || out.Conversion.Discovered != 5 || out.Conversion.Retained != 5 || out.Conversion.ByReason["malformed_candidate"] != 5 {
		t.Fatalf("overlapping discovery totals: %+v", out.Conversion)
	}
	var groupRows int64
	for _, d := range out.Conversion.Details {
		groupRows += d.ConvertedFound + d.TransfersFound
	}
	if groupRows != 6 {
		t.Fatalf("per-request overlap was lost: %+v", out.Conversion.Details)
	}
}
