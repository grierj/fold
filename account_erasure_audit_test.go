package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/token"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"fold/internal/guardkit"

	"fold/internal/audit"
	"fold/models"

	"gorm.io/gorm"
)

// malformedConversionLinksRetained counts the malformed conversion
// nominations reported under admin uid that still hold an IP: a subset of
// RETAIN_UNATTRIBUTED, read here so the erasure itself sends no extra query.
func malformedConversionLinksRetained(t *testing.T, db *gorm.DB, uid uint) int64 {
	t.Helper()
	var n int64
	if err := db.Raw("SELECT count(*) FROM audit_logs WHERE ip <> '' AND ("+auditMalformedConversionLink+")", sql.Named("uid", uid)).Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

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
	// Unregistered action, erased actor: S5 clears the IP and reports no kept row.
	add("legacy.unknown", "user", u.ID, &u.ID, map[string]any{"userId": u.ID}, true)
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
		IPsBlanked: 90, MetadataRowsScrubbed: 80,
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
	// An unregistered action whose actor is the erased person is cleared (S5), so
	// it is neither kept nor reported.
	blank := map[uint]bool{}
	for _, tc := range []struct {
		target string
		id     uint
		actor  *uint
		meta   map[string]any
		kept   bool
	}{
		{"group", 1, &u.ID, nil, false},
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
		row := auditErasureLog(t, app.DB, action, tc.target, tc.id, tc.actor, tc.meta)
		before = append(before, row)
		blank[row.ID] = tc.actor != nil && *tc.actor == u.ID
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
		assertAuditErasureRows(t, app.DB, before, blank, nil)
	}
}

// auditRawLog inserts one audit row with exactly the metadata text given, so a
// test can seed bytes no current writer produces. A nil ip stores NULL.
func auditRawLog(t *testing.T, db *gorm.DB, actor *uint, action, target string, id uint, meta string, ip *string) models.AuditLog {
	t.Helper()
	var rowID uint
	if err := db.Raw(`INSERT INTO audit_logs (actor_id, action, target_type, target_id, meta_json, ip)
 VALUES (?, ?, ?, ?, CAST(? AS jsonb), ?) RETURNING id`, actor, action, target, id, meta, ip).Scan(&rowID).Error; err != nil {
		t.Fatal(err)
	}
	var row models.AuditLog
	if err := db.First(&row, rowID).Error; err != nil {
		t.Fatal(err)
	}
	return row
}

// historicalActorFixture seeds S5's reach for erased user u: every row in
// cleared is an unregistered action with u as actor and a nonempty IP, and every
// row in kept must come through byte for byte.
type historicalActorFixture struct {
	before   []models.AuditLog
	cleared  map[uint]bool
	keptMany int64 // rows of keptAction that mention u and so reach the report
}

const (
	historicalClearedAction = "legacy.cleared"
	historicalKeptAction    = "legacy.kept"
)

func seedHistoricalActorRows(t *testing.T, db *gorm.DB, u, living, other *models.User) *historicalActorFixture {
	t.Helper()
	f := &historicalActorFixture{cleared: map[uint]bool{}}
	ip := "192.0.2.19"
	empty := ""
	add := func(action string, actor *uint, target string, id uint, meta string, ip *string, cleared bool) {
		row := auditRawLog(t, db, actor, action, target, id, meta, ip)
		f.before = append(f.before, row)
		f.cleared[row.ID] = cleared
	}
	// meta_json and target are never read: garbage of every JSON type, a target
	// that names nobody, another person, or the subject all clear alike.
	for _, meta := range []string{`{}`, `[]`, `"text"`, `7`, `null`, `{"userId":"abc"}`, `{"userId":{"a":[1]}}`,
		`{"teenUserId":99999999999999999999,"personId":false}`, `{"userId":` + itoa(living.ID) + `}`, `{"checkNote":"Keep this note","evidenceText":"Keep"}`} {
		add(historicalClearedAction, &u.ID, "group", 1, meta, &ip, true)
	}
	for _, target := range []struct {
		kind string
		id   uint
	}{{"", 0}, {"user", u.ID}, {"user", other.ID}, {"registration", 9}, {"guardian_link", 71}} {
		add(historicalClearedAction, &u.ID, target.kind, target.id, `{}`, &ip, true)
	}
	// The registry compares whole action strings, so near misses are unregistered.
	for _, action := range []string{"user.created.v0", "USER.CREATED", " user.created", "invitation.accepted.legacy"} {
		add(action, &u.ID, "user", u.ID, `{"userId":1}`, &ip, true)
	}
	// Not the erased person's rows: a living or other actor, however much the
	// target or metadata mention u, and a row with no actor (S3).
	add(historicalKeptAction, &living.ID, "user", u.ID, `{"userId":`+itoa(u.ID)+`}`, &ip, false)
	add(historicalKeptAction, &other.ID, "user", u.ID, `{"teenUserId":`+itoa(u.ID)+`}`, &ip, false)
	add(historicalKeptAction, nil, "user", u.ID, `{"userId":`+itoa(u.ID)+`}`, &ip, false)
	add(historicalKeptAction, nil, "group", 1, `{"userId":`+itoa(u.ID)+`}`, &ip, false)
	f.keptMany = 4
	add(historicalKeptAction, &living.ID, "group", 1, `{}`, &ip, false)
	// Nothing to clear: an empty or NULL address is neither rewritten nor counted.
	add(historicalClearedAction, &u.ID, "user", u.ID, `{}`, &empty, false)
	add(historicalClearedAction, &u.ID, "user", u.ID, `{}`, nil, false)
	// Registered actions with u as actor are S2's rows, however malformed: S5
	// never reaches them, including a registered system action with an IP.
	add(audit.InvitationAccepted, &u.ID, "invitation", 401, `{"userId":`+itoa(u.ID)+`,"personId":501}`, &ip, false)
	add(audit.InvitationAccepted, &u.ID, "invitation", 401, `{}`, &ip, false)
	add(audit.AccountRequested, &u.ID, "account_request", 301, `{"source":"unknown"}`, &ip, false)
	add(audit.GuardianLinkRequested, &u.ID, "guardian_link", 1, `{"teenUserId":`+itoa(u.ID)+`,"source":"unknown"}`, &ip, false)
	add(audit.GuardianLinkRequested, &u.ID, "guardian_link", 2, `[]`, &ip, false)
	add(audit.UserTurned18, &u.ID, "user", u.ID, `{}`, &ip, false)
	add(audit.HouseholdPersonConverted, &u.ID, "household", 1, `{"personId":501,"userId":`+itoa(u.ID)+`}`, &ip, false)
	add(audit.RegistrationChanged, &u.ID, "registration", 1, `{"invitationId":401}`, &ip, false)
	return f
}

func TestAuditErasureClearsHistoricalActorRows(t *testing.T) {
	app := testApp(t)
	u := newTestUser(t, app, "audit-hist-erased", erasurePW, models.RoleMember)
	living := newTestUser(t, app, "audit-hist-living", erasurePW, models.RoleAdmin)
	other := newTestUser(t, app, "audit-hist-other", erasurePW, models.RoleMember)
	f := seedHistoricalActorRows(t, app.DB, u, living, other)
	var wantCleared int64
	for _, yes := range f.cleared {
		if yes {
			wantCleared++
		}
	}
	out := runAuditErasure(t, app.DB, erasureSubject{ID: u.ID})
	// No class rule owns any seeded row, so S5 is the only clearing here and only
	// ip changes.
	assertAuditErasureRows(t, app.DB, f.before, f.cleared, nil)
	if got := out.BlankedByClass[auditIPHistoricalActor]; got != wantCleared {
		t.Fatalf("HISTORICAL_ACTOR blanked %d, want %d: %+v", got, wantCleared, out)
	}
	for class, n := range out.BlankedByClass {
		if class != auditIPHistoricalActor && n != 0 {
			t.Fatalf("class %s blanked %d rows; only S5 owns the seeded rows: %+v", class, n, out.BlankedByClass)
		}
	}
	if out.IPsBlanked != wantCleared {
		t.Fatalf("IPsBlanked %d, want only S5's %d", out.IPsBlanked, wantCleared)
	}
	// The retention report runs after S5: cleared rows are not "kept", the rows
	// S5 does not own still are.
	if got := out.KeptByClassAndAction[auditIPRetainUnattributed][historicalClearedAction]; got != 0 {
		t.Fatalf("cleared rows still reported as kept: %d", got)
	}
	if got := out.KeptByClassAndAction[auditIPRetainUnattributed][historicalKeptAction]; got != f.keptMany {
		t.Fatalf("kept %d rows of %s, want %d", got, historicalKeptAction, f.keptMany)
	}
	for _, action := range []string{audit.InvitationAccepted, audit.AccountRequested, audit.GuardianLinkRequested, audit.UserTurned18} {
		if out.KeptByClassAndAction[auditIPRetainUnattributed][action] == 0 {
			t.Fatalf("registered %s row with actor %d vanished from the report: %+v", action, u.ID, out.KeptByClassAndAction)
		}
	}
	if out.MetadataRowsScrubbed != 0 {
		t.Fatalf("S5 must not touch metadata: %+v", out)
	}
	// The public report counts what remains.
	var kept int64
	for _, byAction := range out.KeptByClassAndAction {
		for _, n := range byAction {
			kept += n
		}
	}
	if got := newErasureReport(householdErasureCounts{}, out).ActivityEntriesKept; got != kept {
		t.Fatalf("report counts %d kept rows, want %d", got, kept)
	}
	// A second run changes nothing and reports the same rows.
	snapshot := dumpRows(t, app.DB, "audit_logs", "id", "true")
	again := runAuditErasure(t, app.DB, erasureSubject{ID: u.ID})
	if again.IPsBlanked != 0 || again.BlankedByClass[auditIPHistoricalActor] != 0 || snapshot != dumpRows(t, app.DB, "audit_logs", "id", "true") {
		t.Fatalf("second run mutated audit: %+v", again)
	}
	if !reflect.DeepEqual(out.KeptByClassAndAction, again.KeptByClassAndAction) {
		t.Fatal("rerun lost the retention report")
	}
	// HISTORICAL_ACTOR is internal: it is neither a rule class, a report class
	// nor a key in the public report.
	for _, rules := range auditIPAttribution {
		for _, rule := range rules {
			if rule.Class == auditIPHistoricalActor {
				t.Fatal("HISTORICAL_ACTOR is not an attribution rule class")
			}
		}
	}
	if _, ok := out.KeptByClassAndAction[auditIPHistoricalActor]; ok {
		t.Fatal("HISTORICAL_ACTOR rows are never kept")
	}
}

// The registry is read when the erasure runs: an action that gains a rule stops
// being S5's, and one that is still absent is S5's. Serial: it edits a global.
func TestAuditErasureHistoricalActorFollowsTheLiveRegistry(t *testing.T) {
	app := testApp(t)
	u := newTestUser(t, app, "audit-hist-live", erasurePW, models.RoleMember)
	const action = "legacy.promoted_to_registry"
	ip := "192.0.2.19"
	row := auditRawLog(t, app.DB, &u.ID, action, "group", 1, `{"x":1}`, &ip)
	out := runAuditErasure(t, app.DB, erasureSubject{ID: u.ID})
	if out.BlankedByClass[auditIPHistoricalActor] != 1 {
		t.Fatalf("unregistered action not cleared: %+v", out)
	}
	if err := app.DB.Model(&row).Update("ip", ip).Error; err != nil {
		t.Fatal(err)
	}
	// A system rule never owns a row that has an IP, so with the key registered
	// the row falls to RETAIN_UNATTRIBUTED and stays.
	auditIPAttribution[action] = auditIPSystem
	t.Cleanup(func() { delete(auditIPAttribution, action) })
	out = runAuditErasure(t, app.DB, erasureSubject{ID: u.ID})
	var after models.AuditLog
	if err := app.DB.First(&after, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.IP != ip || out.BlankedByClass[auditIPHistoricalActor] != 0 || out.IPsBlanked != 0 {
		t.Fatalf("registered action reached by S5: %+v %+v", after, out)
	}
	if out.KeptByClassAndAction[auditIPRetainUnattributed][action] != 1 {
		t.Fatalf("registered row missing from the report: %+v", out.KeptByClassAndAction)
	}
}

// S5 sits inside the erasure transaction: a failure after it leaves every S5
// address as it was and returns no counts. The trigger names which side of S5
// the failing metadata scrub ran on.
func TestAuditErasureHistoricalActorRollsBack(t *testing.T) {
	app := testApp(t)
	u := newTestUser(t, app, "audit-hist-rollback", erasurePW, models.RoleMember)
	living := newTestUser(t, app, "audit-hist-rollback-living", erasurePW, models.RoleAdmin)
	other := newTestUser(t, app, "audit-hist-rollback-other", erasurePW, models.RoleMember)
	f := seedHistoricalActorRows(t, app.DB, u, living, other)
	// A subject-keyed metadata scrub is the first statement after S5 that changes a row.
	auditErasureLog(t, app.DB, audit.UserEmailChanged, "user", u.ID, &living.ID, map[string]any{"oldEmail": "old@example.com", "newEmail": "new@example.com"})
	snapshot := dumpRows(t, app.DB, "audit_logs", "id", "true")
	aborted := errors.New("interrupt before commit")
	var first auditErasureCounts
	err := app.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		first, err = eraseAuditDetailsIn(tx, erasureSubject{ID: u.ID})
		if err != nil {
			return err
		}
		return aborted
	})
	if !errors.Is(err, aborted) || first.BlankedByClass[auditIPHistoricalActor] == 0 || snapshot != dumpRows(t, app.DB, "audit_logs", "id", "true") {
		t.Fatalf("clean run didn't roll back: %v %+v", err, first)
	}
	for _, sql := range []string{
		fmt.Sprintf(`CREATE FUNCTION aaa_hist_actor_fault() RETURNS trigger LANGUAGE plpgsql AS $f$
			BEGIN IF NEW.action = 'user.email_changed' THEN
				IF EXISTS (SELECT 1 FROM audit_logs WHERE action = '%s' AND actor_id = %d AND ip <> '') THEN
					RAISE EXCEPTION 'fault before historical actor clearing';
				END IF;
				RAISE EXCEPTION 'fault after historical actor clearing';
			END IF; RETURN NEW; END $f$`, historicalClearedAction, u.ID),
		"CREATE TRIGGER aaa_hist_actor_fault BEFORE UPDATE ON audit_logs FOR EACH ROW EXECUTE FUNCTION aaa_hist_actor_fault()",
	} {
		if err := app.DB.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		app.DB.Exec("DROP TRIGGER IF EXISTS aaa_hist_actor_fault ON audit_logs")
		app.DB.Exec("DROP FUNCTION IF EXISTS aaa_hist_actor_fault()")
	})
	var counts auditErasureCounts
	err = app.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		counts, err = eraseAuditDetailsIn(tx, erasureSubject{ID: u.ID})
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "fault after historical actor clearing") {
		t.Fatalf("want the failure that follows S5, got %v", err)
	}
	if !reflect.DeepEqual(counts, auditErasureCounts{}) || snapshot != dumpRows(t, app.DB, "audit_logs", "id", "true") {
		t.Fatalf("failed run left changes or counts: %+v", counts)
	}
	assertAuditErasureRows(t, app.DB, f.before, nil, nil)
}

// S5's statement is the one place that reads no metadata and no target, binds
// only the subject and the live registry, and is the only other writer of
// audit_logs.ip beside the class updates.
func TestAuditHistoricalActorStatementShape(t *testing.T) {
	src := guardkit.Production(t)["account_erasure_audit.go"]
	m := regexp.MustCompile("const auditHistoricalActorSQL = `([^`]*)`").FindStringSubmatch(src)
	if m == nil {
		t.Fatal("auditHistoricalActorSQL is not declared as one constant")
	}
	const want = `UPDATE audit_logs SET ip = '' WHERE ip <> '' AND actor_id = @uid AND action <> ALL(CAST(@registered AS text[]))`
	if m[1] != want {
		t.Fatalf("S5 statement changed:\n%s", m[1])
	}
	for _, forbidden := range []string{"meta_json", "target_type", "target_id", "account_requests", "OR "} {
		if strings.Contains(m[1], forbidden) {
			t.Fatalf("S5 reads or widens beyond actor and action: %q", forbidden)
		}
	}
	if !strings.Contains(src, `sql.Named("registered", pgTextArray(actions))`) {
		t.Fatal("S5 must bind the registry key list built for the classification")
	}
	// No other production file writes audit_logs.ip, and this file has only the
	// class loop, S5 and the conversion step (TestAuditConversionClearingStatementShape).
	update := regexp.MustCompile(`(?i)UPDATE\s+audit_logs\s+SET\s+ip\b`)
	for path, text := range guardkit.Production(t) {
		n := len(update.FindAllString(text, -1))
		switch {
		case path == "account_erasure_audit.go" && n != 3:
			t.Fatalf("%s has %d audit_logs.ip writers, want the class loop, S5 and the conversion step", path, n)
		case path != "account_erasure_audit.go" && n != 0:
			t.Fatalf("%s writes audit_logs.ip", path)
		}
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
	return auditConversionRequestRow{ID: f.request.ID, PersonID: ptr(f.person.ID), InvitationID: ptr(*f.request.InvitationID), UserID: ptr(f.user.ID), HouseholdID: &h}
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
			const where = "action = ? OR (action = ? AND target_type IN ('registration','event'))"
			run := eraseProbe(t, f.app.DB, erasureSubject{ID: f.admin.ID}, true)
			out := run.out
			if out.Conversion.Groups != 1 || len(out.Conversion.Details) != 1 {
				t.Fatalf("discovery: %+v", out.Conversion)
			}
			// A group that passes V0-V2, G4 and G5 is identified and cleared whole
			// (P2). The rest, valid rows included, are retained as they always were.
			// Rows no key reaches (15d) keep their address beside a cleared group.
			wantRetainedC, wantRetainedT, wantBlanked := tc.c, tc.t, int64(0)
			var wantChanged []uint
			if clearedCases[tc.name] {
				wantRetainedC, wantRetainedT, wantBlanked = 0, 0, tc.c+tc.t
				for _, r := range f.rows {
					wantChanged = append(wantChanged, r.ID)
				}
			}
			detail := out.Conversion.Details[0]
			if detail.Reason != tc.reason || detail.ConvertedFound != tc.c || detail.TransfersFound != tc.t || detail.ConvertedRetained != wantRetainedC || detail.TransfersRetained != wantRetainedT || detail.Blanked != wantBlanked || detail.Identified != clearedCases[tc.name] ||
				out.Conversion.Retained != wantRetainedC+wantRetainedT || out.Conversion.Blanked != wantBlanked || out.BlankedByClass[auditIPConversion] != wantBlanked || out.Conversion.ByReason[tc.reason] != tc.c+tc.t {
				t.Fatalf("report: %+v detail %+v", out.Conversion, detail)
			}
			if got := changedIDs(run, conversionRowsOf(t, f.app.DB)); !reflect.DeepEqual(got, wantChanged) {
				t.Fatalf("changed conversion rows %v, want %v", got, wantChanged)
			}
			after := dumpRows(t, f.app.DB, "audit_logs", "id", where, audit.HouseholdPersonConverted, audit.RegistrationChanged)
			for _, uid := range []uint{f.admin.ID, f.inviter.ID, f.user.ID, f.admin.ID} {
				again := runAuditErasure(t, f.app.DB, erasureSubject{ID: uid})
				if uid == f.admin.ID && !reflect.DeepEqual(rerunShape(out.Conversion), again.Conversion) {
					t.Fatalf("15h retry discovery drifted: %+v -> %+v", out.Conversion, again.Conversion)
				}
				if after != dumpRows(t, f.app.DB, "audit_logs", "id", where, audit.HouseholdPersonConverted, audit.RegistrationChanged) {
					t.Fatal("role erasure changed conversion rows")
				}
			}
		})
	}
}

// The groups cleared are the ones that pass V0-V2 and G4-G5: L1, the ones with
// rows no key reaches (15d) and the one with another numeric tuple (15e).
// Every anomaly V0-V2 judge is retained whole, valid rows included.
var clearedCases = map[string]bool{"L1_G8_unestablished": true, "15d_unreachable_T": true, "15d_wrong_target_T": true, "15e_other_numeric_tuple": true}

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
	run := eraseProbe(t, f.app.DB, erasureSubject{ID: f.admin.ID}, true)
	out := run.out
	// The three rows the text arms reach are an identified group and are cleared;
	// the two spelled with a scale are never discovered, so they keep their address.
	if out.Conversion.Discovered != 3 || out.Conversion.Retained != 0 || out.Conversion.Blanked != 3 || out.Conversion.ByReason["history_unestablished"] != 3 {
		t.Fatalf("text discovery limit or the cleared group changed: %+v", out.Conversion)
	}
	var want []uint
	for _, r := range f.rows {
		want = append(want, r.ID)
	}
	if got := changedIDs(run, conversionRowsOf(t, f.app.DB)); !reflect.DeepEqual(got, want) {
		t.Fatalf("changed conversion rows %v, want only the discovered group %v", got, want)
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

// classifyEstablishedConversion is V4, which production never runs while G-8
// is disabled: a group that passes V0-V3 is classified by the one converted
// row's account type. Pure classification; nothing writes rows.
func classifyEstablishedConversion(r auditConversionRequestRow, rows []models.AuditLog) auditConversionGroupCounts {
	out := classifyAuditConversion(r, rows)
	if out.Reason != "history_unestablished" {
		return out
	}
	var accountType string
	for _, row := range rows {
		if row.Action == audit.HouseholdPersonConverted {
			var meta map[string]json.RawMessage
			_ = json.Unmarshal([]byte(row.MetaJSON), &meta)
			_ = json.Unmarshal(meta["accountType"], &accountType)
		}
	}
	switch {
	case accountType == "teen" || (accountType == "adult" && r.Marker):
		out.Reason = "completion"
	case accountType != "adult":
		out.Reason = "unclassified"
	default:
		out.Reason = "approval"
	}
	return out
}

func TestAuditConversionV0ThroughV4Classification(t *testing.T) {
	f := newAuditAdultFixture(t)
	input := auditGroupInput(f)
	for _, history := range []bool{false, true} {
		got := classifyAuditConversion(input, f.rows)
		want := "history_unestablished"
		if history {
			got, want = classifyEstablishedConversion(input, f.rows), "approval"
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
			got := classifyEstablishedConversion(r, rows)
			if got.Reason != tc.reason {
				t.Fatalf("V4 %+v", got)
			}
			if got := classifyAuditConversion(r, rows); got.Reason != "history_unestablished" {
				t.Fatalf("V3 must precede V4: %+v", got)
			}
		})
	}
	// Identified is G1 to G5, set by the classifier and not read off the reason.
	if got := classifyAuditConversion(input, f.rows); !got.Identified {
		t.Fatalf("a valid, fully linked group with its household read is identified: %+v", got)
	}
	// Unknown household skips only arm d, not shape validation or identity arms,
	// but the household check of the converted row was not real, so G4 fails: the
	// reason is the one it always was and the group is not identified (N-4).
	input.HouseholdID = nil
	got := classifyAuditConversion(input, f.rows)
	if got.HouseholdFallbackSkipped != 1 || got.Reason != "history_unestablished" || got.Identified {
		t.Fatalf("missing household: %+v", got)
	}
	// A missing identity link (G5, N-3b) is never identified, even when every row
	// passes every shape, and the group is reported malformed (R303-01).
	for name, mutate := range map[string]func(*auditConversionRequestRow){
		"person":     func(r *auditConversionRequestRow) { r.PersonID = nil },
		"invitation": func(r *auditConversionRequestRow) { r.InvitationID = nil },
		"user":       func(r *auditConversionRequestRow) { r.UserID = nil },
	} {
		linked := auditGroupInput(f)
		mutate(&linked)
		if got := classifyAuditConversion(linked, f.rows); got.Identified || got.Reason != "malformed_candidate" || got.ConvertedFound != 1 || got.TransfersFound != 2 || got.ConvertedRetained != 1 || got.TransfersRetained != 2 {
			t.Fatalf("NULL %s link: %+v", name, got)
		}
		// Even with no row at all the group is malformed, never "no_converted_row".
		if got := classifyAuditConversion(linked, nil); got.Identified || got.Reason != "malformed_candidate" {
			t.Fatalf("NULL %s link, no rows: %+v", name, got)
		}
	}
	// Each of V0 to V2 alone also keeps it out of the set.
	var converted, transfers []models.AuditLog
	for _, r := range f.rows {
		if r.Action == audit.HouseholdPersonConverted {
			converted = append(converted, r)
		} else {
			transfers = append(transfers, r)
		}
	}
	for name, rows := range map[string][]models.AuditLog{
		"no converted row": transfers,
		"missing transfer": append(append([]models.AuditLog(nil), converted...), transfers[:1]...),
		"duplicate C":      append(append(append([]models.AuditLog(nil), converted...), converted...), transfers...),
	} {
		if got := classifyAuditConversion(auditGroupInput(f), rows); got.Identified {
			t.Fatalf("%s: %+v", name, got)
		}
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
	// Step 5 ran inside the aborted transaction and its blanks went with it (I-3).
	if !errors.Is(err, aborted) || snapshot != dumpRows(t, f.app.DB, "audit_logs", "id", "true") || first.Conversion.Blanked != 3 || first.IPsBlanked < 3 {
		t.Fatal("audit transaction didn't roll back", err, first.Conversion)
	}
	// Roll back each order so both traverse the very same real-writer history.
	// Every order that includes a candidate ends in the same rows, and the group
	// is blanked exactly once, whichever candidate comes first (I-2).
	var finals []string
	for _, order := range [][]uint{{f.inviter.ID, f.user.ID, f.admin.ID}, {f.admin.ID, f.inviter.ID, f.user.ID}, {f.user.ID, f.admin.ID, f.inviter.ID}, {f.admin.ID, f.user.ID, f.inviter.ID}} {
		var blanked int64
		err := f.app.DB.Transaction(func(tx *gorm.DB) error {
			for _, uid := range order {
				s := erasureSubject{ID: uid}
				if uid == f.user.ID {
					s.Requests = []uint{f.request.ID}
					s.Invitations = []uint{*f.request.InvitationID}
					s.People = []uint{f.person.ID}
				}
				out, err := eraseAuditDetailsIn(tx, s)
				if err != nil {
					return err
				}
				blanked += out.Conversion.Blanked
			}
			finals = append(finals, dumpRows(t, tx, "audit_logs", "id", "true"))
			return aborted
		})
		if !errors.Is(err, aborted) || blanked != 3 {
			t.Fatal(err, blanked)
		}
	}
	for _, other := range finals[1:] {
		if finals[0] != other {
			t.Fatal("erasure order changed final audit rows")
		}
	}
	again := runAuditErasure(t, f.app.DB, erasureSubject{ID: f.admin.ID})
	if !reflect.DeepEqual(first, again) {
		t.Fatalf("retry changed counts: %+v %+v", first, again)
	}
	// The committed run blanked once; a second run finds the same group and no address (I-1).
	if next := runAuditErasure(t, f.app.DB, erasureSubject{ID: f.admin.ID}); next.Conversion.Blanked != 0 || next.Conversion.Retained != 0 || next.Conversion.Groups != 1 || next.BlankedByClass[auditIPConversion] != 0 || !reflect.DeepEqual(next.Conversion, rerunShape(again.Conversion)) {
		t.Fatalf("second run: %+v", next.Conversion)
	}
}

// auditLifecycle is one real conversion of a teen (L2 to L5 and variants), built
// through the real handlers with a distinct address per caller: the inviter .1,
// D .2, the redeemer .3, the guardian G .4, the enabler E .5, the link
// confirmer L .6 and the consenter K .7.
type auditLifecycle struct {
	app                                       *App
	f                                         teenConversionFixture
	d, inviter, enabler, confirmer, consenter *models.User
	rows                                      []models.AuditLog // the group once completed
	marker                                    bool
}

func newAuditLifecycle(t *testing.T, lifecycle string) auditLifecycle {
	t.Helper()
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
	case "L2", "L2_late_enable":
		apiDo(t, kc, "POST", consentPath(&f.user), map[string]string{"scope": "account", "status": "granted", "evidenceText": "Signed consent"}, 200)
		apiDo(t, lc, "POST", "/api/v1/admin/guardian-links/"+itoa(f.link.ID)+"/confirm", map[string]string{"checkNote": "Relationship verified"}, 200)
		if lifecycle == "L2_late_enable" {
			// An admin who enables the account long after completion is still a key (J5).
			apiDo(t, admin, "PUT", "/api/v1/admin/users/"+itoa(f.user.ID), map[string]string{"status": "disabled"}, 200)
			apiDo(t, ec, "PUT", "/api/v1/admin/users/"+itoa(f.user.ID), map[string]string{"status": "active"}, 200)
		}
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
	var d, inviter models.User
	if err := app.DB.Where("email = ?", "admin@example.com").First(&d).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.First(&inviter, *f.request.RequestedBy).Error; err != nil {
		t.Fatal(err)
	}
	return auditLifecycle{app: app, f: f, d: &d, inviter: &inviter, enabler: enabler, confirmer: confirmer, consenter: consenter, rows: rows, marker: lifecycle == "L3" || lifecycle == "L4"}
}

// Which of the people of a lifecycle a retained key names. G (the guardian who
// confirmed no link and granted no consent) and S (the converted account) are
// named by none, in every lifecycle. L3's group already holds no address, so
// its callers change nothing, but they are still callers.
var lifecycleCallers = map[string][]string{
	"L2":             {"D", "inviter", "L", "K"},
	"L2_late_enable": {"D", "inviter", "L", "K", "E"},
	"L3":             {"D", "inviter"},
	"L4":             {"D", "inviter", "E"},
	"L5":             {"D", "inviter", "E", "L", "K"},
	"L5_D_is_E":      {"D", "inviter", "L", "K"},
}

// P2-2 to P2-5 and P2-9, with the keys of K1 to K5 each alone: erasing a person
// a key names clears the group's addresses and nothing else; erasing anyone
// else changes no conversion row. Every case is tried against the same state,
// so each erasure is rolled back.
func TestAuditErasureTeenRealLifecycles(t *testing.T) {
	for _, lifecycle := range []string{"L2", "L2_late_enable", "L3", "L4", "L5", "L5_D_is_E"} {
		t.Run(lifecycle, func(t *testing.T) {
			l := newAuditLifecycle(t, lifecycle)
			h := l.f.person.HouseholdID
			r := auditConversionRequestRow{PersonID: ptr(l.f.person.ID), InvitationID: l.f.request.InvitationID, UserID: ptr(l.f.user.ID), HouseholdID: &h, Marker: l.marker}
			if got := classifyEstablishedConversion(r, l.rows); got.Reason != "completion" {
				t.Fatalf("current-history classification %+v", got)
			}
			if got := classifyAuditConversion(r, l.rows); !got.Identified {
				t.Fatalf("a real completed conversion is an identified group: %+v", got)
			}
			people := map[string]*models.User{"D": l.d, "inviter": l.inviter, "E": l.enabler, "L": l.confirmer, "K": l.consenter, "G": l.f.holder, "S": &l.f.user}
			callers := map[string]bool{}
			for _, name := range lifecycleCallers[lifecycle] {
				callers[name] = true
			}
			held := nonEmptyIDs(l.rows)
			for _, name := range []string{"D", "inviter", "E", "L", "K", "G", "S"} {
				run := eraseProbe(t, l.app.DB, erasureSubject{ID: people[name].ID}, false)
				var want []uint
				if callers[name] {
					want = held
				}
				if got := changedIDs(run, l.rows); !reflect.DeepEqual(got, want) {
					t.Fatalf("%s: changed conversion rows %v, want %v (held %v)", name, got, want, held)
				}
				requireLivingStepRowsKeepIP(t, run, people[name].ID)
				c := run.out.Conversion
				if !callers[name] {
					if c.Groups != 0 || c.Discovered != 0 || c.Retained != 0 || c.Blanked != 0 || len(c.Details) != 0 || run.out.BlankedByClass[auditIPConversion] != 0 {
						t.Fatalf("%s is no caller but the report found a group: %+v", name, c)
					}
					continue
				}
				// Identified groups are retained by nothing, whatever their reason; the reason
				// stays the diagnostic it always was (history_unestablished, G-8 unestablished).
				if c.Groups != 1 || c.Discovered != 3 || c.Retained != 0 || c.Blanked != int64(len(held)) || run.out.BlankedByClass[auditIPConversion] != int64(len(held)) ||
					c.ByReason["history_unestablished"] != 3 || len(c.Details) != 1 {
					t.Fatalf("%s: group counts %+v", name, c)
				}
				if d := c.Details[0]; !d.Identified || d.ConvertedFound != 1 || d.TransfersFound != 2 || d.ConvertedRetained != 0 || d.TransfersRetained != 0 || d.Blanked != int64(len(held)) || d.TransferCount != 2 || d.Reason != "history_unestablished" {
					t.Fatalf("%s: detail %+v", name, d)
				}
			}
			// E's own step row keeps its address when D is erased (P2-5), and D's own
			// step rows are blanked only by the existing attribution rules.
			if lifecycle == "L5" || lifecycle == "L4" {
				run := eraseProbe(t, l.app.DB, erasureSubject{ID: l.d.ID}, false)
				enabled := 0
				for id, b := range run.before {
					if b.Action == audit.UserEnabled && b.ActorID != nil && *b.ActorID == l.enabler.ID && b.IP != "" {
						enabled++
						if run.changed[id] {
							t.Fatalf("D's erasure blanked E's own user.enabled row %d", id)
						}
					}
				}
				if enabled != 1 {
					t.Fatalf("expected E's one user.enabled row, found %d", enabled)
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
	"account_erasure_tombstones.go:eraseTombstoneOnce":      "275b68295526163c3a5a0f45ca6cc5f1ac1a1c262e8ed8ebd8524be1bdd33a04",
	"account_erasure.go:eraseAccountIn":                     "1b081dbae07a9d40b613a3c0df8b072777f7e3653d98cb2d8dc28b06c4266d57",
	"account_erasure.go:eraseMessagesIn":                    "74a2cf34c5b654969f31847517ec142532eeed3759e3d9592f2b3b31bb862f2d",
	"account_erasure_households.go:eraseHouseholdRecordsIn": "d8b7f12b5e706b009ed3a108794de19320ae4a0e2b2704d010c6e0e624a45c5e",
	// T07B-2b: the page-view row of the reason review (no IP, actor the admin;
	// meta keys count and source). Decision rows go through audit.WriteAll.
	"account_erasure_reasons.go:reasonPageAttempt": "082539d5d3b5760bdd1da6e34182135498af4857c2b0d630042e62e263d16b23",
	// Legacy view: the admin is the actor; no IP or reason text is stored.
	"api_admin_reasons_legacy.go:legacyPageIn":                    "133083e02fdac6fc98ca70715bf597db2a87dd80bfec7b3491f18bf0b7417fd4",
	"age_rollover.go:flagHouseholdChild13":                        "1804d1f969590562b8e2282f42635834e8f5a3841b0c9679f4b400a4efe5d866",
	"age_rollover.go:rolloverTeen":                                "4139500705a3da764c067accd4f73fefed8e9c88aa696be6dbc26978d7dbe20b",
	"api_account_requests.go:apiSetSelfSignup":                    "46f34cd7e73d9cf344b3e9378c76b811b88b06d10a20b7f495aadac78da63e82",
	"api_account_requests.go:decideAccountRequest":                "18bb2e5e2f8af270aef775f8a7cdbdf8f7eaa0f5d151615576dc938bd4d21fe9",
	"api_account_requests.go:expirePendingRequests":               "b30d60d413fdc04420d5ef85fed5141945bbad23cd3c882a19a4e160d067446f",
	"api_account_requests.go:storeAccountRequest":                 "a2a12aeb0ea4183b87c6773c1b5ba0f75b74d9bc2296be8c6edf2c4070aef3ec",
	"api_admin.go:apiAdminCreateUser":                             "6d94b5f538e0ac21ef2812e65b5bf9dd6da4434ebf5635c0fbeb6e8df06ec1c4",
	"api_admin.go:apiAdminResetPassword":                          "a5986e9dbf1710c87fec4483407977aa9f587bc64c8619363aef2f26d72c514f",
	"api_admin.go:apiAdminUpdateUser":                             "aa7bd3e1f541a1b039858f6a2397cadde395e6932b29952892e4f8b5af0c808c",
	"api_admin_groups.go:decideGroup":                             "faa6da1ee041b4d39f734458a51662ca5be8359d0ebc31c4a8e811ac897c1f2f",
	"api_admin_guardian_links.go:decideGuardianLink":              "0d2559e7a9ccefe3156c283990dc4653610534becb9e47c4a52a0ab473c50cf5",
	"api_admin_household.go:apiAdminAddHouseholdPerson":           "8cf7bd69171a89be85dc1274e7086c190bcf9ad4b70f9ab22d10b0dc81c4c385",
	"api_admin_household.go:apiAdminCreateHousehold":              "94247b2de1067ca36bff02c0f6b171ca43b0d7ef09ea9677c58633c5315a341c",
	"api_admin_jobs.go:decideDeadJob":                             "04e9851ad53e2151ac2bc13599645fe06b1025aa77319bd291be047403485d08",
	"api_admin_suppressions.go:apiAdminClearSuppression":          "9044a4250a72408b0ccb7354305dd22e93dd2082f630b5993b7586c79035b4ea",
	"api_auth.go:apiChangePassword":                               "09345a72be2ea9e67394b3ee2bec5e8c127451fb3fd6e21978d7498994fffc18",
	"api_auth.go:apiRequestPasswordReset":                         "dc3a3d5d37afce904a46dd6dd87342be9b49ea3b9379a2d2556f0f889158683e",
	"api_auth.go:redeemPasswordLink":                              "841741c26844e3d9414a86b0128034760bc5057f9594496f59da2713855960b3",
	"api_contact.go:auditRefusal":                                 "bfe884ae54cfeb1f8224d9cf33ad758168262cbc65502778d8d0561890191f6d",
	"api_contact_history.go:apiExportContactHistory":              "5df09267b49abc795ee0f42a03f5d376bd233f87e77455d53e529b8068a97a24",
	"api_departure.go:applyDeparture":                             "835354a095c0a480e8499c4ecb04f74092dfd2bce845699898fb141b9b9ab9a2",
	"api_email_change_cancel.go:apiCancelEmailChange":             "edb9bc16427aa877d8e6cea6227d100ff8c6d3124e9ee1aea9abc3ecc48ce1da",
	"api_event_access.go:apiAddEventHost":                         "3c75c6258e7fb4f2157ed6881f387010f61f5f7200b05a4d6f18a714e7af3e48",
	"api_event_access.go:apiRemoveEventHost":                      "ad9c92d06c87eba8f11e447dbb48a98c0f1b6501976a3ece6c8ec8fe0db58e8d",
	"api_event_access.go:apiSetEventVisibility":                   "626cabf10d6c4ca7a7afac84935a07d9bbf4771f1bddeff97d1cdd7cdbb969e0",
	"api_events.go:apiCreateEvent":                                "6019122679de6e09b703d6c79239bddda6fd5ec00bdfbeec42f1fd40e83b2f4b",
	"api_events.go:apiDeleteEvent":                                "49605e07b47a70d91a057c264d22edafe81530cda07102d5c460d7d8ab893200",
	"api_events.go:restoreSeriesDates":                            "ee6a331db732803f1fb43f4be87f742df27e444cfbfb01967be4c40b063180f1",
	"api_feeds.go:apiCreateMyCalendarFeed":                        "4804978e0c2abafcb9ea1234cde1fc604bf1c3cce5bb3ad79c87f8b5fa1f1510",
	"api_feeds.go:apiUpdateMyCalendarFeed":                        "9ea28d336e14eea85b7fadb379e258108df222066d72b736f387d91672712106",
	"api_feeds.go:revokeCalendarToken":                            "83f1472c1958c30af21ed73ce001befedb879dcb35d2c0fc3fbfb531140505e9",
	"api_group_visibility.go:applyGroupTightening":                "bd332c610893d3e7016b90dbf3dc270ce352a0040b0e558eee8e8500235d6a42",
	"api_group_visibility.go:cancelDateLosers":                    "b559a56de6e1d185f9d67eb2c1b9bf40754de49713a0133ed067296b2f791bb9",
	"api_groups.go:apiUpdateGroup":                                "066a15def11b07f00492ad420206068b7ff977b43f8d492b9be3f8f976c9192d",
	"api_groups.go:auditAndNotifyLeadership":                      "10cc718888efbfe112b1f9d198a48a9a9c7e8c74ba90ec721ad35ba8d555316c",
	"api_groups.go:changeRoleIn":                                  "2bbe7152fb27b6fdc394965e6b8047c5cec5f0a6c7e7bab0e140bb9821076855",
	"api_groups.go:deleteGroup":                                   "fec7d685ed8b3700c060f747c0c0bc2a1b758fef582f539ec837dd198058edbd",
	"api_guest.go:apiCancelGuestRegistration":                     "d92867ce53e9aa1d9e3f635bee6ae5946c7aa0fed892bebc76beba28bb017fbb",
	"api_guest.go:createGuestUpgradeRequest":                      "d4efcca0c23809550c5f9d8996e03fad7134231186eb151392530f8a17e6341c",
	"api_handoff.go:demoteEverywhere":                             "f8c19b38c31de43cb159e734182104c37d48a604e794b65c09b7d22b3483ba38",
	"api_handoff.go:handOffLeadership":                            "e4c64e8d739a704a9ccc80a1aa8244ae996e392379b1d70f6b37e32b57f9ccda",
	"api_handoff.go:restoreMemberships":                           "7e5274985450108d91a71e530fe4d6256b0a330c3361cff5a271bfb0cf037773",
	"api_host_handoff.go:handOffHosting":                          "93a50176317d4e93ff841272aef57f58ebecc26f8aa682ce85787ebd38232772",
	"api_host_handoff.go:deleteHostRowsAudited":                   "e4840ae49c5b95f68150584f85c36abeed2bf195f0f91c7ae3f764bb2fe72277",
	"api_household_conversion.go:acceptHouseholdConversion":       "6cc4414eef3eb20cd4c91809c39acac928e40ff1cfe0fedb0bc0b24d9c603e26",
	"api_household_conversion.go:approveHouseholdConversion":      "c81973b1954313fe3adbec503e4d51f445e9403f1d1b5d029934acbce5962f5b",
	"api_household_conversion.go:createConversionApprovalRequest": "b3446035530b204e0fd55527ba5da2ddd14c3e619cf3c50141aaec152b9fdd29",
	"api_household_conversion.go:issueHouseholdConversion":        "9f6f439dcead0603cd7951269d59d2ee82732c3609c9b37bb7ff37db4e44e679",
	"api_household_conversion.go:revokeConversionInvitation":      "0fefea42145a8ecba0cd41cf715a9856d9865a070b65d847987d1d3526dd1d29",
	"api_household_conversion.go:transferHouseholdConversion":     "e1908caf0ce2fcf2d264c520c2f9c4adda50c7625277295c99d6678b8eef369f",
	"api_invitations.go:apiAcceptInvitation":                      "aefaf685053af673d24c4f48cb8cc6c94eeb2cb219db2bd91773e5d2c3ff9b99",
	"api_invitations.go:apiCreateInvitation":                      "45bbb658cdec5c7de60681c0d505544e2c25a011e8fa9bbb38b9e978c79d1a7e",
	"api_invitations.go:writeInvitationRefusal":                   "7b02d5b25e7c2b17d5c628c82fa102075c93ba82255b0a21b4eecb4c7dae6e0f",
	"api_message_history.go:apiRetryMessage":                      "28f49307508e5f77df4a6aaebc4c63d59a94aca0cccb26e86b207e8d4024ce19",
	"api_message_preview.go:apiTestMessage":                       "2ad44cd111947e52717b8a4d257d3fd6c307ab0b8be1d938139a9425f62c952a",
	"api_message_settings.go:saveSetting":                         "94b67b44538247bff4e69419b7c8e347b1a204eb0bdef8b7626194279525c245",
	"api_messages.go:apiCancelMessage":                            "072e942199cda3fee26b59da2a7cd384b18bb75c04052b6873cd0ded6b108fd7",
	"api_messages.go:apiScheduleMessage":                          "c3812387e4ed2d94a8099978724a83523a859e25f6edd41167d14c35be69f5ca",
	"api_messages.go:apiUpdateMessage":                            "488f229db14d0f858b82d153bd8a76dbe2e3d4f1d46a6c57564a9e9c73df9928",
	"api_households.go:apiAddHouseholdPerson":                     "83c8ef22c21d627b9ef84d66838ad7dc8491b5dbc5de3d0ea4dd2e77a203609a",
	"api_households.go:apiDeleteHouseholdPerson":                  "0988a9ba35deecc848bebbda7af43b773f91042364c274cf13d5972f56cb85de",
	"api_occurrence_visibility.go:changeDateVisibility":           "c6b8840390d62966740b633cc84d9ae61de734822f099bca9ef0301c3b780c62",
	"api_profile.go:apiRequestEmailChange":                        "47dde4ece9b10109a2a709861dd8ede7b41f840030b00f94e5e2c268efbae924",
	"api_households.go:apiUpdateHouseholdPerson":                  "91057afc72a767e94c4ff02c02a890b1bbd9f72c182f173c1f587e584c88aa26",
	"api_profile.go:apiUpdateMe":                                  "b3262ccbdcd1dcaca42e211131e510e4abf3ae042a90dcb55969393aeec23277",
	"api_households.go:apiUpdateMyHousehold":                      "2c02ddc4bf65e78173d88a57d0be58945b0dff62e6530b09d2748101589b4582",
	"api_profile.go:apiVerifyEmailChange":                         "ed76d52dcbe727a70cb2f4dbce8a3271b06e396d7be527dbde1361c165a47950",
	"api_households.go:ensureHousehold":                           "9236b5e942983ff2454573eae5fb0b2aec6146c461772d6723207f5ed544cbf8",
	"api_households.go:revokeGuardianLinks":                       "72f5343ffde6bf01d84532da5a2e7f5e927e5385b992a3032a86ff0d01f933da",
	"api_push.go:apiPushSubscribe":                                "83847c8032633da5bab06adb3c85a625a493d7d2611846e6b518027f9f22cead",
	"api_push.go:apiPushTest":                                     "72c9c905b24bb81242cff2694dbdee3833e47cb298c7e1ca5dd9494b4e6b403a",
	"api_registrations.go:addAccountRegistrationLocked":           "b364cc6c1089f5346833a229677b3c4942f65ba5df667539f742e27b696f5922",
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
	"api_room_series.go:bookWholeSeries":                          "6a35ca593ce32d5d8a02f816bdbda89d2f18f38ad7cdaf242007480e7f6c6dc2",
	"api_rooms.go:apiAdminCreateRoom":                             "00c14cd2b3e4d506f59158cf6a676fe89be5a8183760e27310b12b9e183b8f15",
	"api_rooms.go:apiAdminUpdateRoom":                             "408c699d62a155af44bde0954c969b70965c0c9c583ded3ffc5a0464ffa5a30d",
	"api_rooms.go:setRoomSwitch":                                  "a09e15ddbb19ba2435d0b35ce0e05fb57c75d2ffc0b55ac150bb25c937c9bd39",
	"api_teen_consent.go:changeTeenConsent":                       "f7124d9ee26a3b16b8f67a5fa0fadb7867bcd7b5d9e213e83f3efa8556d9449b",
	"api_teen_guardians.go:apiClaimTeenGuardian":                  "059457aa963c16581146d2802d7bff428783101588e5a44a0cd3041cd868f55a",
	"api_teen_guardians.go:nominateTeenGuardian":                  "c1ef0fab390bdda6d4522037a4a396e79108b8121784f33c9ac6c20058244672",
	"api_teen_guardians.go:requestTeenGuardian":                   "0dae6b14daea71f5b6b03337a3c51bf2d6c01fc043d48addc835eaed49e25597",
	"contact_lifecycle.go:contactAudit":                           "49b052659553931264b71744294d2145031f442425531d164462d7fc57850178",
	"internal/audit/audit.go:Write":                               "79ad063a312631bd946658228d06e4514b85d95cad77c3ac226e7b665717a4fa",
	"messages_send.go:refuseMessage":                              "51b149eb634bdc1277ecb6f96ff24904257859070f3f32b0512ae982e502419a",
	"messages_send.go:settleMessage":                              "d062aef9c6cbfb58aa679300d726b57bcd6096f3d250c39ec23c80ebdfc12332",
	"push_keys.go:ensureVAPIDKeys":                                "d94cd4d219fe7c7184552981dfb2a3ae2438fe4edf876c61e179f5ce2c1c655b",
	"push_keys.go:rotateVAPIDKeys":                                "6bb369832f0c2b9c52667a519cedfa210088ac3f2503af03d8effc56c21b4ce7",
	"registration_access.go:accessSnapshotMeta":                   "f9a7ba0c51c85fb68d838a607612b697752b99e01ffd896593e72465702a7ab0",
	"registration_access.go:writeAccessSnapshot":                  "a47511c490d32cfc8c87792af6da140156e8ce79a2b567c3c1df594991c09714",
	"api_events.go:createEventIn":                                 "d8645e5290d8a9fdfc53c51a538e9f3c184657805c158c4d5d44beb3912de494",
	"api_occurrence_mutations.go:writeOccurrenceRefusal":          "f7e046134daac6dcd2dbe3940f6563ff8cac02d7f1e1c89da8a11571a319f809",
	"request_kinds.go:addAuditAction":                             "ea9a9c1e495429e447216252cea05dc7ac323e24fa12a30a4191429f7de7aaf8",
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
		// The registry covers the named constants in internal/audit.
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
	if len(c.Sites) != 112 {
		t.Fatalf("audit.Write sites = %d, expected 112; review every changed site", len(c.Sites))
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
		{"actor expression", "api_profile.go", "apiUpdateMe", "Actor: &u.ID, Action: audit.ContactPrivacyChanged", "Actor: nil, Action: audit.ContactPrivacyChanged"},
		{"direct IP source", "api_profile.go", "apiUpdateMe", "IP: c.ClientIP()", `IP: ""`},
		{"legacy view actor", "api_admin_reasons_legacy.go", "legacyPageIn", "Actor: &actor.ID", "Actor: nil"},
		{"indirect metadata key", "api_households.go", "apiAddHouseholdPerson", `meta["guardianLinkId"]`, `meta["renamedGuardianLinkId"]`},
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
	if malformedConversionLinksRetained(t, app.DB, d.ID) != 1 || out.BlankedByClass[auditIPCApproval] != 1 || out.KeptByClassAndAction[auditIPRetainUnattributed][audit.GuardianLinkRequested] != 1 {
		t.Fatalf("sibling report: %+v", out)
	}
	assertAuditErasureRows(t, app.DB, []models.AuditLog{good, bad}, map[uint]bool{good.ID: true}, nil)
	again := runAuditErasure(t, app.DB, erasureSubject{ID: d.ID})
	if again.IPsBlanked != 0 || malformedConversionLinksRetained(t, app.DB, d.ID) != 1 {
		t.Fatalf("retry sibling counts %+v", again)
	}
	if err := app.DB.Model(&bad).Update("ip", "").Error; err != nil {
		t.Fatal(err)
	}
	again = runAuditErasure(t, app.DB, erasureSubject{ID: d.ID})
	if malformedConversionLinksRetained(t, app.DB, d.ID) != 0 || again.KeptByClassAndAction[auditIPRetainUnattributed][audit.GuardianLinkRequested] != 0 {
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
	// K1 and K2 both name the admin: one request, one group, found once and
	// cleared once, and nothing is counted twice (P2-6).
	run := eraseProbe(t, f.app.DB, erasureSubject{ID: f.admin.ID}, true)
	c := run.out.Conversion
	if c.Groups != 1 || len(c.Details) != 1 || c.Discovered != 3 || c.Retained != 0 || c.Blanked != 3 || c.ByReason["history_unestablished"] != 3 || run.out.BlankedByClass[auditIPConversion] != 3 || c.Details[0].Blanked != 3 {
		t.Fatalf("coincident roles: %+v", c)
	}
	if got := changedIDs(run, conversionRowsOf(t, f.app.DB)); len(got) != 3 {
		t.Fatalf("coincident approver and inviter: changed %v", got)
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
		if d.Identified || d.Blanked != 0 {
			t.Fatalf("a group with a malformed row in common is never identified or cleared: %+v", d)
		}
	}
	if groupRows != 6 {
		t.Fatalf("per-request overlap was lost: %+v", out.Conversion.Details)
	}
	// The inviter reaches the same two requests through K2 and gets the same totals.
	viaInviter := runAuditErasure(t, f.app.DB, erasureSubject{ID: f.inviter.ID})
	if viaInviter.Conversion.Groups != 2 || viaInviter.Conversion.Discovered != 5 || viaInviter.Conversion.Retained != 5 || viaInviter.Conversion.Blanked != 0 || viaInviter.Conversion.ByReason["malformed_candidate"] != 5 {
		t.Fatalf("K2 totals: %+v", viaInviter.Conversion)
	}
}

// --- P2: clearing the conversion IPs of the candidate callers ---------------

var errProbeRollback = errors.New("erasure probe rolled back")

// convRun is one run of eraseAuditDetailsIn: its counts, every audit row before
// and after, and the ids whose ip it changed.
type convRun struct {
	out     auditErasureCounts
	changed map[uint]bool
	before  map[uint]models.AuditLog
	after   map[uint]models.AuditLog
}

func isConversionRow(r models.AuditLog) bool {
	return r.Action == audit.HouseholdPersonConverted || (r.Action == audit.RegistrationChanged && r.TargetType == "registration")
}

// selectorInputs is every table a selector or a join reads, so that "the step
// changed no input" is one string comparison.
func selectorInputs(t *testing.T, db *gorm.DB) string {
	t.Helper()
	var b strings.Builder
	for _, table := range []string{"account_requests", "guardian_links", "teen_consents", "invitations", "household_people"} {
		b.WriteString(dumpRows(t, db, table, "id", "true"))
	}
	return b.String()
}

// eraseProbe runs the audit erasure of s and checks what no erasure may change
// (N-8): every column but ip of every audit row, the metadata of every
// conversion row, and every selector input. An ip only ever changes to "". With
// commit false the transaction is rolled back, so several subjects can be tried
// against one state.
func eraseProbe(t *testing.T, db *gorm.DB, s erasureSubject, commit bool) convRun {
	t.Helper()
	var run convRun
	err := db.Transaction(func(tx *gorm.DB) error {
		inputs := selectorInputs(t, tx)
		var before []models.AuditLog
		if err := tx.Order("id").Find(&before).Error; err != nil {
			return err
		}
		out, err := eraseAuditDetailsIn(tx, s)
		if err != nil {
			return err
		}
		var after []models.AuditLog
		if err := tx.Order("id").Find(&after).Error; err != nil {
			return err
		}
		if inputs != selectorInputs(t, tx) {
			t.Error("the erasure changed an input of the selectors")
		}
		if len(before) != len(after) {
			t.Fatalf("the erasure changed the number of audit rows: %d -> %d", len(before), len(after))
		}
		run = convRun{out: out, changed: map[uint]bool{}, before: map[uint]models.AuditLog{}, after: map[uint]models.AuditLog{}}
		for i := range before {
			b, a := before[i], after[i]
			run.before[b.ID], run.after[a.ID] = b, a
			if b.IP != a.IP {
				if a.IP != "" {
					t.Errorf("audit %d (%s): ip %q became %q, not blank", b.ID, b.Action, b.IP, a.IP)
				}
				run.changed[b.ID] = true
			}
			b.IP, a.IP = "", ""
			if !isConversionRow(b) {
				b.MetaJSON, a.MetaJSON = "", ""
			}
			if !reflect.DeepEqual(b, a) {
				t.Errorf("audit %d (%s) changed beyond its ip:\nwant %+v\ngot %+v", b.ID, b.Action, b, a)
			}
		}
		if commit {
			return nil
		}
		return errProbeRollback
	})
	if (commit && err != nil) || (!commit && !errors.Is(err, errProbeRollback)) {
		t.Fatal(err)
	}
	return run
}

func conversionRowsOf(t *testing.T, db *gorm.DB) []models.AuditLog {
	t.Helper()
	var rows []models.AuditLog
	if err := db.Where("action = ? OR (action = ? AND target_type = 'registration')", audit.HouseholdPersonConverted, audit.RegistrationChanged).Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

// nonEmptyIDs are the ids of rows that hold an address, ascending; nil for none.
func nonEmptyIDs(rows []models.AuditLog) []uint {
	var ids []uint
	for _, r := range rows {
		if r.IP != "" {
			ids = append(ids, r.ID)
		}
	}
	return ids
}

// changedIDs are the ids among rows whose ip the run blanked, ascending; nil for none.
func changedIDs(run convRun, rows []models.AuditLog) []uint {
	var ids []uint
	for _, r := range rows {
		if run.changed[r.ID] {
			ids = append(ids, r.ID)
		}
	}
	return ids
}

// requireLivingStepRowsKeepIP is N-1: the step rows of everyone but the erased
// person keep their address, whatever the conversion step blanks.
func requireLivingStepRowsKeepIP(t *testing.T, run convRun, uid uint) {
	t.Helper()
	for id, b := range run.before {
		switch b.Action {
		case audit.GuardianLinkConfirmed, audit.TeenConsentGranted, audit.UserEnabled, audit.AccountApproved:
			if b.IP != "" && b.ActorID != nil && *b.ActorID != uid && run.changed[id] {
				t.Errorf("another person's %s row %d lost its address when %d was erased", b.Action, id, uid)
			}
		}
	}
}

// rerunShape is what a second run of the same erasure reports: the same groups
// and counts, with no address left to blank (design section 7).
func rerunShape(c auditConversionCounts) auditConversionCounts {
	c.Blanked = 0
	c.Details = append([]auditConversionGroupCounts(nil), c.Details...)
	for i := range c.Details {
		c.Details[i].Blanked = 0
	}
	return c
}

// requireClearedGroup checks the section 7 counts of one identified group of
// found rows, whose held addresses were blanked.
func requireClearedGroup(t *testing.T, name string, c auditConversionCounts, byClass int64, found, held int64) {
	t.Helper()
	if c.Groups != 1 || len(c.Details) != 1 || c.Discovered != found || c.Retained != 0 || c.Blanked != held || byClass != held || c.ByReason["history_unestablished"] != found {
		t.Fatalf("%s: counts %+v (conversion class %d)", name, c, byClass)
	}
	if d := c.Details[0]; !d.Identified || d.ConvertedRetained != 0 || d.TransfersRetained != 0 || d.Blanked != held || d.ConvertedFound+d.TransfersFound != found {
		t.Fatalf("%s: detail %+v", name, c.Details[0])
	}
}

// P2-2: in an adult conversion the approving admin (K1) and the inviter (K2)
// are the candidates; the converted account and an unrelated admin are not.
func TestAuditErasureAdultConversionCallers(t *testing.T) {
	f := newAuditAdultFixture(t)
	stranger := newTestUser(t, f.app, "audit-stranger", erasurePW, models.RoleAdmin)
	held := nonEmptyIDs(f.rows)
	if len(held) != 3 {
		t.Fatalf("every real row holds an address: %v", held)
	}
	for name, who := range map[string]*models.User{"D (K1)": f.admin, "inviter (K2)": f.inviter, "S": &f.user, "stranger": stranger} {
		run := eraseProbe(t, f.app.DB, erasureSubject{ID: who.ID}, false)
		requireLivingStepRowsKeepIP(t, run, who.ID)
		got := changedIDs(run, f.rows)
		if name == "S" || name == "stranger" {
			if got != nil || run.out.Conversion.Groups != 0 || run.out.Conversion.Blanked != 0 || len(run.out.Conversion.Details) != 0 {
				t.Fatalf("%s is no caller: %v %+v", name, got, run.out.Conversion)
			}
			continue
		}
		if !reflect.DeepEqual(got, held) {
			t.Fatalf("%s: changed %v, want %v", name, got, held)
		}
		requireClearedGroup(t, name, run.out.Conversion, run.out.BlankedByClass[auditIPConversion], 3, 3)
		if run.out.IPsBlanked < 3 {
			t.Fatalf("%s: IPsBlanked %d does not include the conversion rows", name, run.out.IPsBlanked)
		}
		// The approval's own step row follows the existing ACTOR rule, no more.
		for id, b := range run.before {
			if b.Action == audit.AccountApproved && run.changed[id] != (b.ActorID != nil && *b.ActorID == who.ID && b.IP != "") {
				t.Fatalf("%s: account.approved row %d: %+v", name, id, b)
			}
		}
	}
}

// P2-7 and P2-8: two conversions in one household. A person who is a candidate
// of one request clears that group only; one who is a candidate of both clears
// both, with the actual discovered counts.
func TestAuditErasureConversionTwoRequests(t *testing.T) {
	f := newAuditAdultFixture(t)
	second := newTestUser(t, f.app, "audit-second-approver", erasurePW, models.RoleAdmin)
	parent := loginAPI(t, f.app, f.inviter.Email, erasurePW)
	person := addMyPerson(t, parent, map[string]any{"firstName": "Second adult", "kind": "adult", "birthDate": "1990-01-01"})
	acceptConversion(t, f.app, inviteHouseholdPerson(t, f.app, parent, person.ID, "audit-two-requests@example.com"), 200)
	req := pendingRequest(t, f.app, "audit-two-requests@example.com")
	apiDo(t, loginAPI(t, f.app, second.Email, erasurePW), "POST", "/api/v1/admin/account-requests/"+itoa(req.ID)+"/approve", map[string]any{}, 200)
	all := conversionRowsOf(t, f.app.DB)
	var r1, r2 []models.AuditLog
	first := map[uint]bool{}
	for _, r := range f.rows {
		first[r.ID] = true
	}
	for _, r := range all {
		if first[r.ID] {
			r1 = append(r1, r)
		} else {
			r2 = append(r2, r)
		}
	}
	if len(r1) != 3 || len(r2) != 1 || len(nonEmptyIDs(r2)) != 1 {
		t.Fatalf("two real conversions: %d + %d rows", len(r1), len(r2))
	}
	// P2-7: D is a candidate of R1 only, the second approver of R2 only.
	run := eraseProbe(t, f.app.DB, erasureSubject{ID: f.admin.ID}, false)
	if !reflect.DeepEqual(changedIDs(run, all), nonEmptyIDs(r1)) || !reflect.DeepEqual(changedIDs(run, r2), []uint(nil)) {
		t.Fatalf("D cleared %v, want only R1's %v", changedIDs(run, all), nonEmptyIDs(r1))
	}
	requireClearedGroup(t, "D", run.out.Conversion, run.out.BlankedByClass[auditIPConversion], 3, 3)
	run = eraseProbe(t, f.app.DB, erasureSubject{ID: second.ID}, false)
	if !reflect.DeepEqual(changedIDs(run, all), nonEmptyIDs(r2)) {
		t.Fatalf("second approver cleared %v, want only R2's %v", changedIDs(run, all), nonEmptyIDs(r2))
	}
	requireClearedGroup(t, "second approver", run.out.Conversion, run.out.BlankedByClass[auditIPConversion], 1, 1)
	// P2-8: the inviter invited both, so both groups are discovered, in request
	// order, with their own counts, and both are cleared.
	run = eraseProbe(t, f.app.DB, erasureSubject{ID: f.inviter.ID}, true)
	c := run.out.Conversion
	if c.Groups != 2 || len(c.Details) != 2 || c.Discovered != 4 || c.Retained != 0 || c.Blanked != 4 || run.out.BlankedByClass[auditIPConversion] != 4 {
		t.Fatalf("inviter of both: %+v", c)
	}
	if d := c.Details; d[0].ConvertedFound+d[0].TransfersFound != 3 || d[0].Blanked != 3 || d[1].ConvertedFound+d[1].TransfersFound != 1 || d[1].Blanked != 1 || !d[0].Identified || !d[1].Identified {
		t.Fatalf("details in request order: %+v", c.Details)
	}
	// I-1: a second run finds both groups again and has nothing left to blank.
	again := runAuditErasure(t, f.app.DB, erasureSubject{ID: f.inviter.ID})
	if !reflect.DeepEqual(again.Conversion, rerunShape(c)) || again.BlankedByClass[auditIPConversion] != 0 {
		t.Fatalf("rerun %+v", again.Conversion)
	}
}

// N-2 and the keys' edges. K4 is account scope and granted only; K3 is a link
// confirmed for the converted account; K5 is user.enabled for it. A guardian who
// recorded account consent is a caller (labelled fallback: no handler lets a
// guardian record it, only an admin).
func TestAuditErasureConversionKeysAndTheirEdges(t *testing.T) {
	l := newAuditLifecycle(t, "L3")
	db, s := l.app.DB, l.f.user.ID
	// L3's group holds no address; give it one so a clear shows.
	if err := db.Exec("UPDATE audit_logs SET ip = '192.0.2.8' WHERE action = ? OR (action = ? AND target_type = 'registration')", audit.HouseholdPersonConverted, audit.RegistrationChanged).Error; err != nil {
		t.Fatal(err)
	}
	rows := conversionRowsOf(t, db)
	held := nonEmptyIDs(rows)
	mk := func(name string) *models.User { return newTestUser(t, l.app, name, erasurePW, models.RoleMember) }
	revokedOnly, photos, pending, otherTeen, otherEnable, sms := mk("edge-revoked"), mk("edge-photos"), mk("edge-pending"), mk("edge-other-teen"), mk("edge-other-enable"), mk("edge-sms")
	guardian := mk("edge-guardian")
	someone := mk("edge-someone")
	seed := func(row any) {
		t.Helper()
		erasureMustCreate(t, db, row)
	}
	seed(&models.TeenConsent{TeenUserID: s, Scope: models.TeenConsentAccount, Status: "revoked", GrantedBy: revokedOnly.ID, GranterRole: "guardian"})
	seed(&models.TeenConsent{TeenUserID: s, Scope: "photos", Status: "granted", GrantedBy: photos.ID, GranterRole: "guardian"})
	seed(&models.TeenConsent{TeenUserID: s, Scope: "sms", Status: "granted", GrantedBy: sms.ID, GranterRole: "guardian"})
	seed(&models.GuardianLink{GuardianUserID: pending.ID, TeenUserID: &s, Status: "pending"})
	seed(&models.GuardianLink{GuardianUserID: guardian.ID, TeenUserID: &someone.ID, Status: "confirmed", ConfirmedBy: &otherTeen.ID})
	auditErasureLog(t, db, audit.UserEnabled, "user", someone.ID, &otherEnable.ID, nil)
	for name, who := range map[string]*models.User{"revoked-only consent": revokedOnly, "photos consent": photos, "sms consent": sms, "pending link": pending,
		"confirmer of another teen's link": otherTeen, "enabler of another user": otherEnable, "guardian of nobody here": guardian} {
		run := eraseProbe(t, db, erasureSubject{ID: who.ID}, false)
		if got := changedIDs(run, rows); got != nil || run.out.Conversion.Groups != 0 {
			t.Fatalf("%s is not a caller: changed %v, %+v", name, got, run.out.Conversion)
		}
	}
	// A confirmed guardian who recorded the account consent is a caller (K4).
	seed(&models.TeenConsent{TeenUserID: s, Scope: models.TeenConsentAccount, Status: "granted", GrantedBy: l.f.holder.ID, GranterRole: "guardian"})
	run := eraseProbe(t, db, erasureSubject{ID: l.f.holder.ID}, false)
	if got := changedIDs(run, rows); !reflect.DeepEqual(got, held) {
		t.Fatalf("guardian who recorded consent: changed %v, want %v", got, held)
	}
	requireClearedGroup(t, "guardian K4", run.out.Conversion, run.out.BlankedByClass[auditIPConversion], 3, 3)
}

// N-3b: an approved conversion whose identity link is missing (the schema
// permits both) is still discovered and counted, through K1 and K2, and never
// changed: its rows are reported malformed, valid ones included.
func TestAuditErasureConversionLinkMissing(t *testing.T) {
	for _, column := range []string{"approved_user_id", "household_person_id", "invitation_id"} {
		t.Run(column, func(t *testing.T) {
			f := newAuditAdultFixture(t)
			// Simulated anomaly: the user link is ON DELETE SET NULL, and no writer
			// clears the other two.
			if err := f.app.DB.Exec("UPDATE account_requests SET "+column+" = NULL WHERE id = ?", f.request.ID).Error; err != nil {
				t.Fatal(err)
			}
			for name, who := range map[string]*models.User{"decider (K1)": f.admin, "inviter (K2)": f.inviter} {
				if column == "invitation_id" && name == "inviter (K2)" {
					continue // without the invitation link only K1 reaches the request
				}
				run := eraseProbe(t, f.app.DB, erasureSubject{ID: who.ID}, false)
				c := run.out.Conversion
				if got := changedIDs(run, conversionRowsOf(t, f.app.DB)); got != nil {
					t.Fatalf("%s: a link-missing request changed rows %v", name, got)
				}
				if c.Groups != 1 || len(c.Details) != 1 || c.Discovered != 3 || c.Retained != 3 || c.Blanked != 0 || run.out.BlankedByClass[auditIPConversion] != 0 || c.ByReason["malformed_candidate"] != 3 {
					t.Fatalf("%s: counts %+v", name, c)
				}
				if d := c.Details[0]; d.Identified || d.Reason != "malformed_candidate" || d.ConvertedFound != 1 || d.TransfersFound != 2 || d.ConvertedRetained != 1 || d.TransfersRetained != 2 || d.Blanked != 0 {
					t.Fatalf("%s: detail %+v", name, d)
				}
				if report := newErasureReport(householdErasureCounts{}, run.out); report.ActivityEntriesKept < 3 {
					t.Fatalf("%s: the report does not count the retained rows: %+v", name, report)
				}
			}
		})
	}
}

// N-6, N-7 and N-9: neighbours of the group keep their address, and nothing
// about a kind, an account type, a marker, a row's time or an address value
// moves the cleared set.
func TestAuditErasureConversionNeighborsAndNoTimeOrKind(t *testing.T) {
	f := newAuditAdultFixture(t)
	db := f.app.DB
	other := newTestUser(t, f.app, "audit-neighbor", erasurePW, models.RoleAdmin)
	group := f.rows
	var d models.User
	if err := db.Where("email = ?", "admin@example.com").First(&d).Error; err != nil {
		t.Fatal(err)
	}
	neighbor := func(action, target string, id uint, actor *uint, meta map[string]any) uint {
		t.Helper()
		if err := audit.Write(db, audit.Entry{Action: action, TargetType: target, TargetID: id, Actor: actor, IP: "192.0.2.2", Meta: meta}); err != nil {
			t.Fatal(err)
		}
		var row models.AuditLog
		if err := db.Order("id DESC").First(&row).Error; err != nil {
			t.Fatal(err)
		}
		return row.ID
	}
	// Rows of this person and this request that are not the group, and rows of
	// other actions that happen to hold the very same address (never compared).
	kept := []uint{
		neighbor(audit.RegistrationChanged, "event", 1, &other.ID, map[string]any{"personId": f.person.ID, "invitationId": *f.request.InvitationID}),
		neighbor(audit.UserRoleChanged, "user", f.user.ID, &other.ID, nil),
		neighbor(audit.UserCreated, "user", f.user.ID, &other.ID, nil),
	}
	for _, status := range []string{"pending", "rejected", "expired", "withdrawn"} {
		req := models.AccountRequest{Name: "Neighbour", Email: status + "-conversion@example.com", Source: "household_conversion", Status: status,
			DecidedBy: &d.ID, HouseholdPersonID: &f.person.ID, InvitationID: f.request.InvitationID, ExpiresAt: time.Now().Add(time.Hour)}
		erasureMustCreate(t, db, &req)
	}
	// N-7: flipping what the cleared set must never read.
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{"UPDATE household_people SET kind = 'teen' WHERE id = ?", []any{f.person.ID}},
		{"UPDATE users SET account_type = 'teen' WHERE id = ?", []any{f.user.ID}},
		{"UPDATE audit_logs SET at = now() - (id * interval '1 hour') WHERE action = ? OR (action = ? AND target_type = 'registration')", []any{audit.HouseholdPersonConverted, audit.RegistrationChanged}},
	} {
		if err := db.Exec(stmt.sql, stmt.args...).Error; err != nil {
			t.Fatal(stmt.sql, err)
		}
	}
	neighbor(audit.HouseholdPersonUpdated, "household_person", f.person.ID, &other.ID, nil)
	run := eraseProbe(t, db, erasureSubject{ID: d.ID}, true)
	if got := changedIDs(run, group); !reflect.DeepEqual(got, nonEmptyIDs(group)) || len(got) != 3 {
		t.Fatalf("the group's rows changed %v", got)
	}
	for _, id := range kept {
		if run.changed[id] || run.after[id].IP != "192.0.2.2" {
			t.Fatalf("neighbour %d (%s) lost its address", id, run.after[id].Action)
		}
	}
	// The pending, rejected, expired and withdrawn conversions of the same person
	// are not discovered: one approved request, one group.
	requireClearedGroup(t, "neighbours", run.out.Conversion, run.out.BlankedByClass[auditIPConversion], 3, 3)
	requireLivingStepRowsKeepIP(t, run, d.ID)
	// No address value is compared: the group's address on a row with no such
	// shape, and a different address on a group row, change the result not at all.
	for _, r := range group {
		if run.after[r.ID].IP != "" {
			t.Fatalf("group row %d kept %q", r.ID, run.after[r.ID].IP)
		}
	}
}

// N-9 and N-11, the count contract of design section 7, on groups with mixed
// empty, NULL and held addresses, one retained malformed group and an overlap.
func TestAuditErasureConversionCountContract(t *testing.T) {
	retainSum := func(out auditErasureCounts) int64 {
		var n int64
		for _, class := range []auditIPClass{auditIPRetainEssential, auditIPRetainUnattributed} {
			for _, c := range out.KeptByClassAndAction[class] {
				n += c
			}
		}
		return n
	}
	t.Run("mixed_addresses", func(t *testing.T) {
		f := newAuditAdultFixture(t)
		var c, t1, t2 models.AuditLog
		for _, r := range f.rows {
			switch {
			case r.Action == audit.HouseholdPersonConverted:
				c = r
			case t1.ID == 0:
				t1 = r
			default:
				t2 = r
			}
		}
		if err := f.app.DB.Exec("UPDATE audit_logs SET ip = NULL WHERE id = ?", t1.ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := f.app.DB.Exec("UPDATE audit_logs SET ip = '' WHERE id = ?", t2.ID).Error; err != nil {
			t.Fatal(err)
		}
		run := eraseProbe(t, f.app.DB, erasureSubject{ID: f.admin.ID}, true)
		// A NULL or empty address is neither rewritten nor counted as blanked, yet
		// the row is cleared (not retained): Retained + |cleared| = Discovered.
		if got := changedIDs(run, f.rows); !reflect.DeepEqual(got, []uint{c.ID}) {
			t.Fatalf("changed %v, want only the converted row %d", got, c.ID)
		}
		requireClearedGroup(t, "mixed", run.out.Conversion, run.out.BlankedByClass[auditIPConversion], 3, 1)
		if report := newErasureReport(householdErasureCounts{}, run.out); report.ActivityEntriesKept != retainSum(run.out) {
			t.Fatalf("cleared rows are counted as kept: %+v", report)
		}
		after := dumpRows(t, f.app.DB, "audit_logs", "id", "id IN (?, ?)", t1.ID, t2.ID)
		again := runAuditErasure(t, f.app.DB, erasureSubject{ID: f.admin.ID})
		if !reflect.DeepEqual(again.Conversion, rerunShape(run.out.Conversion)) || again.IPsBlanked != 0 || after != dumpRows(t, f.app.DB, "audit_logs", "id", "id IN (?, ?)", t1.ID, t2.ID) {
			t.Fatalf("rerun: %+v", again.Conversion)
		}
	})
	t.Run("identified_and_retained_groups", func(t *testing.T) {
		f := newAuditAdultFixture(t)
		parent := loginAPI(t, f.app, f.inviter.Email, erasurePW)
		person := addMyPerson(t, parent, map[string]any{"firstName": "Second adult", "kind": "adult", "birthDate": "1990-01-01"})
		acceptConversion(t, f.app, inviteHouseholdPerson(t, f.app, parent, person.ID, "audit-contract-second@example.com"), 200)
		req := pendingRequest(t, f.app, "audit-contract-second@example.com")
		apiDo(t, loginAPI(t, f.app, f.admin.Email, erasurePW), "POST", "/api/v1/admin/account-requests/"+itoa(req.ID)+"/approve", map[string]any{}, 200)
		// Simulated anomaly: the second group's count disagrees with its transfers.
		if err := f.app.DB.Exec(`UPDATE audit_logs SET meta_json = jsonb_set(meta_json, '{transferCount}', '5') WHERE action = ? AND meta_json->>'personId' = ?`, audit.HouseholdPersonConverted, itoa(person.ID)).Error; err != nil {
			t.Fatal(err)
		}
		if err := f.app.DB.Exec("UPDATE audit_logs SET ip = '' WHERE id = ?", f.rows[0].ID).Error; err != nil {
			t.Fatal(err)
		}
		all := conversionRowsOf(t, f.app.DB)
		first := map[uint]bool{}
		for _, r := range f.rows {
			first[r.ID] = true
		}
		run := eraseProbe(t, f.app.DB, erasureSubject{ID: f.admin.ID}, true)
		c := run.out.Conversion
		// A: identified, cleared whole (two addresses held, one already empty). B: count_mismatch, retained whole.
		if c.Groups != 2 || c.Discovered != 4 || c.Retained != 1 || c.Blanked != 2 || run.out.BlankedByClass[auditIPConversion] != 2 || c.ByReason["history_unestablished"] != 3 || c.ByReason["count_mismatch"] != 1 || len(c.Details) != 2 {
			t.Fatalf("counts %+v", c)
		}
		a, b := c.Details[0], c.Details[1]
		if !a.Identified || a.ConvertedFound != 1 || a.TransfersFound != 2 || a.ConvertedRetained != 0 || a.TransfersRetained != 0 || a.Blanked != 2 ||
			b.Identified || b.Reason != "count_mismatch" || b.ConvertedFound != 1 || b.TransfersFound != 0 || b.ConvertedRetained != 1 || b.TransfersRetained != 0 || b.Blanked != 0 || a.Blanked+b.Blanked != c.Blanked {
			t.Fatalf("details %+v", c.Details)
		}
		var wantChanged []uint
		for _, r := range all {
			if first[r.ID] && r.IP != "" {
				wantChanged = append(wantChanged, r.ID)
			}
		}
		if got := changedIDs(run, all); !reflect.DeepEqual(got, wantChanged) || int64(len(got)) != c.Blanked {
			t.Fatalf("the rows changed %v are not the rows the report counts, want %v", got, wantChanged)
		}
		if report := newErasureReport(householdErasureCounts{}, run.out); report.ActivityEntriesKept != c.Retained+retainSum(run.out) {
			t.Fatalf("activityEntriesKept %+v does not follow Retained", report)
		}
		again := runAuditErasure(t, f.app.DB, erasureSubject{ID: f.admin.ID})
		if !reflect.DeepEqual(again.Conversion, rerunShape(c)) || again.BlankedByClass[auditIPConversion] != 0 {
			t.Fatalf("rerun %+v", again.Conversion)
		}
	})
	// A second approved request with the same person, no converted account and the
	// same decider is not identified; the rows it shares with the identified
	// request are retained in both details, counted once, and never blanked.
	t.Run("shared_rows_are_never_cleared", func(t *testing.T) {
		f := newAuditAdultFixture(t)
		dup := f.request
		dup.ID, dup.ApprovedUserID, dup.Email = 0, nil, "audit-contract-dup@example.com"
		erasureMustCreate(t, f.app.DB, &dup)
		run := eraseProbe(t, f.app.DB, erasureSubject{ID: f.admin.ID}, true)
		c := run.out.Conversion
		if got := changedIDs(run, f.rows); got != nil {
			t.Fatalf("rows shared with a retained group were blanked: %v", got)
		}
		if c.Groups != 2 || c.Discovered != 3 || c.Retained != 3 || c.Blanked != 0 || run.out.BlankedByClass[auditIPConversion] != 0 || c.ByReason["history_unestablished"] != 3 || c.ByReason["malformed_candidate"] != 3 || len(c.Details) != 2 {
			t.Fatalf("counts %+v", c)
		}
		for i, d := range c.Details {
			if d.ConvertedFound != 1 || d.TransfersFound != 2 || d.ConvertedRetained != 1 || d.TransfersRetained != 2 || d.Blanked != 0 || d.Identified != (i == 0) {
				t.Fatalf("detail %d: %+v", i, d)
			}
		}
		if report := newErasureReport(householdErasureCounts{}, run.out); report.ActivityEntriesKept != 3+retainSum(run.out) {
			t.Fatalf("the shared rows are counted once: %+v", report)
		}
	})
}

// I-3: a failure at the conversion step rolls back everything before it in the
// attempt, including the class blanks and S5, and returns no counts.
func TestAuditErasureConversionStepFailureRollsBack(t *testing.T) {
	f := newAuditAdultFixture(t)
	db := f.app.DB
	// An ordinary row of the admin's own is blanked by the class loop, before step 5.
	own := auditErasureLog(t, db, audit.UserCreated, "user", f.user.ID, &f.admin.ID, nil)
	historical := auditErasureLog(t, db, "legacy.fault_probe", "user", f.user.ID, &f.admin.ID, nil)
	snapshot := dumpRows(t, db, "audit_logs", "id", "true")
	for _, sql := range []string{
		`CREATE FUNCTION zzz_conversion_step_fault() RETURNS trigger LANGUAGE plpgsql AS $f$
			BEGIN IF OLD.action IN ('household.person_converted', 'registration.changed') AND OLD.ip <> '' AND NEW.ip = '' THEN
				RAISE EXCEPTION 'fault at the conversion step';
			END IF; RETURN NEW; END $f$`,
		"CREATE TRIGGER zzz_conversion_step_fault BEFORE UPDATE ON audit_logs FOR EACH ROW EXECUTE FUNCTION zzz_conversion_step_fault()",
	} {
		if err := db.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		db.Exec("DROP TRIGGER IF EXISTS zzz_conversion_step_fault ON audit_logs")
		db.Exec("DROP FUNCTION IF EXISTS zzz_conversion_step_fault()")
	})
	var counts auditErasureCounts
	err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		counts, err = eraseAuditDetailsIn(tx, erasureSubject{ID: f.admin.ID})
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "fault at the conversion step") {
		t.Fatalf("want the failure of the conversion step, got %v", err)
	}
	if !reflect.DeepEqual(counts, auditErasureCounts{}) || snapshot != dumpRows(t, db, "audit_logs", "id", "true") {
		t.Fatalf("a failed step left changes or counts: %+v", counts)
	}
	// Without the fault the same erasure blanks the earlier rows too, so they were
	// part of the transaction that failed.
	db.Exec("DROP TRIGGER zzz_conversion_step_fault ON audit_logs")
	run := eraseProbe(t, db, erasureSubject{ID: f.admin.ID}, true)
	if !run.changed[own.ID] || !run.changed[historical.ID] || run.out.Conversion.Blanked != 3 {
		t.Fatalf("the earlier blanks were not part of the failed attempt: %v %+v", run.changed, run.out.Conversion)
	}
}

// The statements the application sends, with their binds, as the QC-1 harness
// will see them. Discovery's first statement is the one the harness knows by its
// text: byte for byte what it was, with the deleted account its only parameter,
// directly after the retention report and the only block that begins as it does.
// The other callers' arms are a later statement of their own.
func TestAuditDiscoveryStatementsAndBinds(t *testing.T) {
	s := newCoreErasureScene(t)
	db, u, living := s.app.DB, s.second, s.admin
	type sentStatement struct {
		sql  string
		vars []any
	}
	var mu sync.Mutex
	var sent []sentStatement
	capture := func(tx *gorm.DB) {
		mu.Lock()
		sent = append(sent, sentStatement{tx.Statement.SQL.String(), append([]any(nil), tx.Statement.Vars...)})
		mu.Unlock()
	}
	name := fmt.Sprintf("test:audit_discovery:%d", queryCounterSeq.Add(1))
	if err := db.Callback().Raw().After("gorm:raw").Register(name, capture); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Raw().Remove(name) })
	if err := db.Callback().Row().After("gorm:row").Register(name, capture); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Row().Remove(name) })

	// Two conversions the erased admin is a candidate of by different keys: R1 he
	// decided (K1), R2 he invited (K2). Labelled seeds, as the other deletion tests.
	conversion := func(tag string, decidedBy, invitedBy uint, transfers int) {
		t.Helper()
		teen := newTestUser(t, s.app, tag+"-teen", erasurePW, models.RoleMember)
		h := models.Household{Name: tag + " household", CreatedBy: living.ID}
		erasureMustCreate(t, db, &h)
		p := models.HouseholdPerson{HouseholdID: h.ID, FirstName: tag + " person", Kind: models.HouseholdAdult, ConvertedUserID: &teen.ID, CreatedBy: living.ID}
		erasureMustCreate(t, db, &p)
		inv := erasureInvitation(t, db, invitedBy, teen.Email, true, &p.ID)
		erasureMustCreate(t, db, &models.AccountRequest{Name: tag, Email: tag + "@example.com", Source: "household_conversion", Status: "approved",
			ApprovedUserID: &teen.ID, DecidedBy: &decidedBy, HouseholdPersonID: &p.ID, InvitationID: &inv, ExpiresAt: time.Now().Add(time.Hour)})
		auditErasureLog(t, db, audit.HouseholdPersonConverted, "household", h.ID, &invitedBy, map[string]any{"personId": p.ID, "householdId": h.ID, "invitationId": inv, "userId": teen.ID,
			"accountType": "adult", "transferCount": transfers})
		for i := 0; i < transfers; i++ {
			auditErasureLog(t, db, audit.RegistrationChanged, "registration", uint(9000+i), &invitedBy, map[string]any{"personId": p.ID, "invitationId": inv, "userId": teen.ID,
				"formerRegisteredBy": living.ID, "clearedChildcareRequested": false})
		}
	}
	conversion("k1", u.ID, living.ID, 1)
	conversion("k2", living.ID, u.ID, 0)
	rows := conversionRowsOf(t, db)
	if len(rows) != 3 || len(nonEmptyIDs(rows)) != 3 {
		t.Fatalf("seeded rows %+v", rows)
	}
	mu.Lock()
	sent = nil
	mu.Unlock()
	s.erase(t, u.ID)
	// Stop capturing before anything else reads the database.
	mu.Lock()
	sent = append([]sentStatement(nil), sent...)
	mu.Unlock()
	_ = db.Callback().Raw().Remove(name)
	_ = db.Callback().Row().Remove(name)
	for _, r := range conversionRowsOf(t, db) {
		if r.IP != "" {
			t.Fatalf("a candidate's conversion row kept %q after the deletion: %+v", r.IP, r)
		}
	}
	for i, st := range sent {
		if strings.Contains(st.sql, "audit_logs") {
			t.Logf("[%d] %s\n     vars %v", i, strings.Join(strings.Fields(st.sql), " "), st.vars)
		}
	}
	uidVars := func(st sentStatement) bool {
		return len(st.vars) == 1 && fmt.Sprint(st.vars[0]) == itoa(u.ID)
	}
	report, s5 := -1, -1
	for i, st := range sent {
		switch {
		case report < 0 && strings.HasPrefix(st.sql, "SELECT class, action, count(*) FROM (SELECT action, "):
			report = i
		case s5 < 0 && strings.HasPrefix(st.sql, "UPDATE audit_logs SET ip = '' WHERE ip <> '' AND actor_id = $1 AND action <> ALL("):
			s5 = i
		}
	}
	if report < 0 || s5 < 0 || s5 > report {
		t.Fatalf("S5 must be sent before the retention report (S5 at %d, report at %d)", s5, report)
	}
	if len(sent) < report+5 {
		t.Fatalf("only %d statements, the report is at %d", len(sent), report)
	}
	k1 := strings.ReplaceAll(auditK1Text, "?", "$1")
	if sent[report+1].sql != k1 || !uidVars(sent[report+1]) {
		t.Fatalf("directly after the report the application must send discovery's first statement byte for byte, with the account as its only bind:\n%s\nvars %v", sent[report+1].sql, sent[report+1].vars)
	}
	later := sent[report+2]
	if !strings.HasPrefix(later.sql, "WITH subject AS (SELECT CAST($1 AS bigint) AS id)") || !uidVars(later) || strings.Contains(later.sql, "$2") {
		t.Fatalf("the other callers' arms are a statement of their own with the account as its only bind:\n%s\nvars %v", later.sql, later.vars)
	}
	var requests []uint
	if err := db.Raw("SELECT id FROM account_requests WHERE source = 'household_conversion' ORDER BY id").Scan(&requests).Error; err != nil {
		t.Fatal(err)
	}
	candidates := sent[report+3]
	if !strings.HasPrefix(candidates.sql, "WITH r AS (") || len(candidates.vars) != 1 || candidates.vars[0] != pgBigintArray(requests) {
		t.Fatalf("the candidate rows are read once for the union of both statements' requests %v:\n%s\nvars %v", requests, candidates.sql, candidates.vars)
	}
	clear := sent[report+4]
	var ids []uint
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	if !strings.HasPrefix(strings.Join(strings.Fields(clear.sql), " "), "UPDATE audit_logs SET ip = '' WHERE id = ANY(CAST($1 AS bigint[]))") || len(clear.vars) != 1 || clear.vars[0] != pgBigintArray(ids) {
		t.Fatalf("step 5 clears exactly the identified groups' ids %v:\n%s\nvars %v", ids, clear.sql, clear.vars)
	}
	// The harness counts every block that begins as discovery's first statement:
	// it must find exactly one in the whole deletion.
	prefix := k1[:90]
	n := 0
	for _, st := range sent {
		if strings.HasPrefix(st.sql, prefix) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d statements begin as discovery's first statement, want exactly 1", n)
	}
}

// auditK1Text is the first statement of discovery, byte for byte as the QC-1
// harness holds it (deploy/digitalocean/qc1/app-facts.json at the PR that made
// it a successor), with its bind written ?.
const auditK1Text = "SELECT r.id, r.household_person_id AS person_id, r.invitation_id, r.approved_user_id AS user_id, h.household_id,\n" +
	" EXISTS (SELECT 1 FROM audit_logs m WHERE m.action = 'household.person_updated' AND m.target_type = 'household_person' AND m.target_id = r.household_person_id) AS marker\n" +
	" FROM account_requests r LEFT JOIN household_people h ON h.id = r.household_person_id\n" +
	" WHERE r.source = 'household_conversion' AND r.status = 'approved' AND r.decided_by = ?"

func localConst(t *testing.T, src, name string) string {
	t.Helper()
	m := regexp.MustCompile("(?s)const " + name + " = `([^`]*)`").FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("account_erasure_audit.go declares no const %s", name)
	}
	return m[1]
}

// S-5: the request statements are exactly K1, then K2 to K5 as four EXISTS arms
// over the retained keys, with one bind each and no time, address, name, kind or
// sixth arm. A change here is a change of who may have caused a conversion:
// revisit docs/plan/design/r4-conversion-ip-minimization.md first.
func TestAuditConversionRequestStatementShape(t *testing.T) {
	src := guardkit.Production(t)["account_erasure_audit.go"]
	// K1 is written in discovery, or, once the QC-1 harness names it, as the
	// package constant discovery takes it from. Either way it is this text.
	name := "requests"
	if strings.Contains(src, "const requests = auditConversionRequestsSQL") {
		name = "auditConversionRequestsSQL"
	}
	k1 := localConst(t, src, name)
	later := localConst(t, src, "laterRequests")
	if k1 != auditK1Text {
		t.Fatalf("K1 is the statement the QC-1 harness holds; it changed:\n%s", k1)
	}
	if strings.Count(k1, "?") != 1 || strings.Count(later, "?") != 1 || strings.Contains(later, "@") {
		t.Fatalf("each request statement binds the account once: %q", later)
	}
	if !strings.HasPrefix(later, "WITH subject AS (SELECT CAST(? AS bigint) AS id)") || strings.HasPrefix(later, k1[:90]) {
		t.Fatalf("the later statement must start apart from K1 (QC-1 counts blocks by their first 90 characters): %q", later[:90])
	}
	flat := strings.Join(strings.Fields(later), " ")
	arms := []string{
		"EXISTS (SELECT 1 FROM invitations i WHERE i.id = r.invitation_id AND i.invited_by = s.id)",
		"EXISTS (SELECT 1 FROM guardian_links g WHERE g.teen_user_id = r.approved_user_id AND g.confirmed_by = s.id)",
		"EXISTS (SELECT 1 FROM teen_consents c WHERE c.teen_user_id = r.approved_user_id AND c.granted_by = s.id AND c.scope = 'account' AND c.status = 'granted')",
		"EXISTS (SELECT 1 FROM audit_logs e WHERE e.action = 'user.enabled' AND e.target_type = 'user' AND e.target_id = r.approved_user_id AND e.actor_id = s.id)",
	}
	where := flat[strings.Index(flat, " WHERE r.source"):]
	want := " WHERE r.source = 'household_conversion' AND r.status = 'approved' AND (" + strings.Join(arms, " OR ") + ")"
	if where != want {
		t.Fatalf("K2 to K5 are exactly the four arms of the design:\n got %s\nwant %s", where, want)
	}
	if strings.Count(flat, "EXISTS (SELECT 1 FROM") != 5 { // the marker and the four arms
		t.Fatalf("a sixth arm: %s", flat)
	}
	for _, forbidden := range []string{".ip", ".at ", "created_at", "decided_at", "kind", "account_type", "ORDER BY", "LIMIT", "interval", "now()", "decided_by", "NOT EXISTS"} {
		if strings.Contains(where, forbidden) {
			t.Fatalf("the callers' arms may read no %q", forbidden)
		}
	}
	// The union runs in Go and the candidate rows are read by the request ids.
	for _, want := range []string{"WHERE r.id = ANY(CAST(? AS bigint[])))", "pgBigintArray(ids)", "tx.Raw(requests, uid)", "tx.Raw(laterRequests, uid)"} {
		if !strings.Contains(src, want) {
			t.Fatalf("discovery lost %q", want)
		}
	}
	if strings.Contains(src, "WITH r AS (`+requests") || strings.Contains(src, "WITH r AS (`+laterRequests") {
		t.Fatal("the candidate query embeds a request statement again")
	}
}

// The one statement that writes ip on a conversion row, apart from none: the
// class loop cannot (a CONVERSION rule owns nothing), S5 cannot (a registry key),
// and this one reaches only the ids discovery cleared, by fixed action and
// target and key, never by an actor, a name, a time or an address.
func TestAuditConversionClearingStatementShape(t *testing.T) {
	src := guardkit.Production(t)["account_erasure_audit.go"]
	got := localConst(t, src, "auditConversionClearSQL")
	const want = `UPDATE audit_logs SET ip = ''
 WHERE id = ANY(CAST(? AS bigint[])) AND ip <> ''
 AND ((action = 'household.person_converted' AND target_type = 'household')
 OR (action = 'registration.changed' AND target_type = 'registration'
 AND (jsonb_exists(meta_json, 'invitationId') OR jsonb_exists(meta_json, 'formerRegisteredBy'))))
 RETURNING id`
	if got != want {
		t.Fatalf("the conversion step's statement changed:\n%s", got)
	}
	if strings.Count(got, "?") != 1 {
		t.Fatal("the statement binds the id array once")
	}
	for _, forbidden := range []string{"actor_id", "target_id", "ip =", "ip LIKE", " at ", "account_requests", "@"} {
		if strings.Contains(strings.Replace(got, "SET ip = ''", "", 1), forbidden) {
			t.Fatalf("the statement selects by %q", forbidden)
		}
	}
	if strings.Count(src, "const auditConversionClearSQL") != 1 || strings.Count(src, "tx.Raw(auditConversionClearSQL, ") != 1 || strings.Count(src, "auditConversionClearSQL,") != 1 {
		t.Fatal("auditConversionClearSQL must be declared once and sent once")
	}
	for action, rules := range auditIPAttribution {
		for _, rule := range rules {
			if rule.Class == auditIPConversion && rule.Owner != "FALSE" {
				t.Fatalf("the class loop would blank %s's conversion rows: %+v", action, rule)
			}
		}
	}
}
