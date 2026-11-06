package migrations_test

// Final-schema tests (R9 S2, DEC-138).
//
// The chain of 63 files is one generated baseline, so a test can no longer
// walk to an intermediate version. Every test here runs against the baseline
// and keeps what describes the final schema: its CHECKs, indexes, triggers and
// refusals. What went away, by old test:
//
//	TestContactRequestsConstraints             DownTo(29) tail removed; matrix kept.
//	TestOccurrenceAccessDepartureOriginMigration DownTo(33) x2, UpTo(34) removed; matrix kept.
//	TestDepartureOccurrenceAccessExpiryMigration UpTo(34/35), DownTo(34) x2 removed; the occurrence_ended end state is asserted directly.
//	TestDepartureAccessSupersessionMigration   UpTo(36/37), DownTo(36) x2 removed; departure_superseded asserted directly.
//	TestMembershipTenure* (3 tests, shared seed) UpTo(35/36), DownTo(35) x3 removed; the tenure backfill and the Down guard
//	                                           were history; the trigger's rules are asserted on the final table.
//	TestSeriesUndoReceiptMigrationAndRollback  DownTo(32) removed; receipt CHECKs kept.
//	TestOccurrenceCancelSourceMigration        DownTo(42) removed; default and values kept (DEC-91).
//	TestSimplicityMigrations                   UpTo(7/9), DownTo(8) removed; the pending_return backfill and the Down
//	                                           mapping were history; the status and church-wide CHECKs are asserted.
//	TestNotificationsBackfill                  UpTo(9/10), DownTo(9): a backfill-only scenario, removed.
//	TestMigration15DownMapsStatuses            UpTo(15), DownTo(14): a Down mapping, removed; the status vocabulary kept.
//	TestHouseholdMigration22                   UpTo(22), DownTo(19) removed; constraints kept.
//	TestChildcareMigration23                   UpTo(23), DownTo(22) removed; defaults and CHECKs kept.
//	TestHouseholdRegistrationMigration24       UpTo(23/24), DownTo(23) x3 removed; the identity matrix kept.
//	TestHouseholdConversionMigration25         UpTo(25), DownTo(24) x3 removed; invitation constraints kept.
//	TestTeenConsentMigrationAppendOnlyAndRollback UpTo(25/26), DownTo(25) x2 removed; the disabled-user backfill was
//	                                           history; the append-only matrix kept.
//	TestContactAddRequestsMigration            UpTo(30), DownTo(30) x2: data preservation across a version, removed;
//	                                           the contact default is asserted instead.
//	TestContactAddRequestsConstraints          DownTo(30) tail removed; matrix kept.
//	TestMembershipAndRegistrationLookupPlans   DownTo(40) removed; plans kept, index existence asserted.
//	TestPushSubscriptionsMigration             UpTo(58), DownTo(57) x4 removed; constraints, consent and the
//	                                           session trigger (00059) kept.
//
// R9 S4 folded occurrence_accesses into registrations.access_*: the departure
// origin, expiry end-state, supersession end-state and contact-add tests now
// write a registration's own columns and keep their refusal matrices (blocked,
// teen direct, repeat, stale, forged issuer/request/target/event/subject, wrong
// departure actor or membership, sole leader, household rows, malformed end
// states, identity transfer, immutable terminal fields). The DELETE, replacement,
// snapshot, same-row ordering and trigger-shape cases are new.
//
// The baseline Down is new and tested below: it refuses while a user exists,
// destroys nothing then, and on an empty database drops everything and Up
// restores it. The same disposition for the other test files is recorded in
// docs/plan/briefs/audit-2026-11/R9-baseline-squash.md (section 9, S2).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"fold/internal/mail"
	"fold/internal/notify"
	"fold/internal/testdb"
	"fold/migrations"
	"fold/models"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

func openSQL(t *testing.T, name string) *sql.DB {
	t.Helper()
	db, err := testdb.OpenSQL(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// appliedRows reads goose's own bookkeeping, not `migrate status`: a database
// migrated with the old chain also reports version 1 applied there.
func appliedRows(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.Query(`SELECT version_id, is_applied FROM goose_db_version ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var version int64
		var applied bool
		if err := rows.Scan(&version, &applied); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%d:%t", version, applied))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(got, ",")
}

// TestContactRequestsConstraints checks the contact request tables on real Postgres, including the
// retained safeguarding identity and exact successful-grant provenance.
func TestContactRequestsConstraints(t *testing.T) {
	t.Parallel()
	db := openSQL(t, testdb.Fresh(t, true))
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	insertRequest := func() int {
		t.Helper()
		var id int
		err := db.QueryRow(`INSERT INTO contact_requests (requester_id, subject_user_id, context_type, context_id, fields, needs_admin, admin_decision, created_at, expires_at)
			VALUES (101, 102, 'group', 103, 'email', true, 'pending', '2026-10-24 12:00Z', '2026-11-07 12:00Z') RETURNING id`).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	for _, tt := range []struct{ name, mutation string }{
		{"invalid requester", "requester_id = 0"},
		{"self request", "subject_user_id = requester_id"},
		{"invalid context", "context_type = 'occurrence'"},
		{"zero context", "context_id = 0"},
		{"invalid fields", "fields = 'address'"},
		{"long reason", "reason = repeat('x', 1001)"},
		{"unknown status", "status = 'approved'"},
		{"unknown decision", "subject_decision = 'yes'"},
		{"teen skips admin", "admin_decision = 'not_needed'"},
		{"adult requires admin", "needs_admin = false"},
		{"decision missing actor and time", "subject_decision = 'approved'"},
		{"pending carries decision facts", "subject_decided_by = 102, subject_decided_at = '2026-10-25 12:00Z'"},
		{"wrong subject actor", "subject_decision = 'approved', subject_decided_by = 101, subject_decided_at = '2026-10-25 12:00Z'"},
		{"admin missing actor", "admin_decision = 'approved', admin_decided_at = '2026-10-25 12:00Z'"},
		{"admin zero actor", "admin_decision = 'approved', admin_decided_by = 0, admin_decided_at = '2026-10-25 12:00Z'"},
		{"decision before creation", "subject_decision = 'approved', subject_decided_by = 102, subject_decided_at = '2026-10-23 12:00Z'"},
		{"decision at expiry", "subject_decision = 'approved', subject_decided_by = 102, subject_decided_at = expires_at"},
		{"reversed lifetime", "expires_at = created_at"},
		{"completion on pending", "completed_at = created_at"},
		{"granted missing approvals", "status = 'granted', completed_at = '2026-10-25 12:00Z'"},
		{"denied missing denial", "status = 'denied', completed_at = '2026-10-25 12:00Z'"},
		{"withdraw missing actor", "status = 'withdrawn', completed_at = '2026-10-25 12:00Z'"},
		{"wrong withdrawal actor", "status = 'withdrawn', completed_at = '2026-10-25 12:00Z', withdrawn_by_id = 102"},
		{"premature expiry", "status = 'expired', completed_at = '2026-10-25 12:00Z'"},
		{"ended missing reason", "status = 'ended', completed_at = '2026-10-25 12:00Z'"},
		{"unknown end reason", "status = 'ended', completed_at = '2026-10-25 12:00Z', end_reason = 'other'"},
		{"nonterminal denial", "subject_decision = 'denied', subject_decided_by = 102, subject_decided_at = '2026-10-25 12:00Z'"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			id := insertRequest()
			if _, err := db.Exec("UPDATE contact_requests SET "+tt.mutation+" WHERE id = $1", id); err == nil {
				t.Fatal("malformed request accepted")
			}
		})
	}
	// Unicode limit is characters, not bytes; both contexts and all field sets work.
	for _, fields := range []string{"email", "phone", "both"} {
		id := insertRequest()
		exec(`UPDATE contact_requests SET reason = repeat('é', 1000), fields = $1, context_type = 'event', needs_admin = false, admin_decision = 'not_needed' WHERE id = $2`, fields, id)
	}
	for _, state := range []string{"withdrawn", "expired", "ended", "denied"} {
		id := insertRequest()
		q := "status = '" + state + "', completed_at = '2026-10-25 12:00Z'"
		switch state {
		case "withdrawn":
			q += ", withdrawn_by_id = 101"
		case "expired":
			q = "status = 'expired', completed_at = expires_at"
		case "ended":
			q += ", end_reason = 'left_context'"
		case "denied":
			q += ", admin_decision = 'denied', admin_decided_by = 104, admin_decided_at = '2026-10-25 12:00Z'"
		}
		exec("UPDATE contact_requests SET "+q+" WHERE id = $1", id)
	}

	grantSQL := `INSERT INTO contact_grants (request_id, leader_id, subject_user_id, context_type, context_id, fields, granted_at)
		VALUES ($1, 101, 102, 'group', 103, 'email', '2026-10-25 12:00Z')`
	pending := insertRequest()
	if _, err := db.Exec(grantSQL, pending); err == nil {
		t.Fatal("grant from pending request accepted")
	}
	successfulRequest := func() int {
		id := insertRequest()
		exec(`UPDATE contact_requests SET subject_decision = 'approved', subject_decided_by = 102, subject_decided_at = '2026-10-25 12:00Z',
			admin_decision = 'approved', admin_decided_by = 104, admin_decided_at = '2026-10-25 12:00Z', status = 'granted', completed_at = '2026-10-25 12:00Z' WHERE id = $1`, id)
		return id
	}
	id := successfulRequest()
	if _, err := db.Exec(`INSERT INTO contact_grants (request_id, leader_id, subject_user_id, context_type, context_id, fields, granted_at)
		VALUES ($1, 101, 102, 'group', 103, 'email', '2026-10-25 11:59Z')`, id); err == nil {
		t.Fatal("grant predating approval accepted")
	}
	for _, mutation := range []string{
		"leader_id = 105", "subject_user_id = 105", "context_type = 'event'", "context_id = 105", "fields = 'both'",
	} {
		// Seed exactly once, then test composite provenance on updates.
		exec(grantSQL, id)
		if _, err := db.Exec("UPDATE contact_grants SET "+mutation+" WHERE request_id = $1", id); err == nil {
			t.Fatalf("mismatched grant accepted: %s", mutation)
		}
		exec("DELETE FROM contact_grants WHERE request_id = $1", id)
	}
	exec(grantSQL, id)
	if _, err := db.Exec(`UPDATE contact_grants SET request_id = $1 WHERE request_id = $2`, pending, id); err == nil {
		t.Fatal("grant retargeted to pending request")
	}
	if _, err := db.Exec(grantSQL, id); err == nil {
		t.Fatal("duplicate grant accepted")
	}
	if _, err := db.Exec("DELETE FROM contact_requests WHERE id = $1", id); err == nil {
		t.Fatal("grant provenance can be deleted")
	}
	for _, mutation := range []string{
		"end_reason = 'left_role'", "ended_at = '2026-10-24 12:00Z', end_reason = 'left_role'",
		"ended_at = '2026-10-26 12:00Z', end_reason = 'unknown'",
		"ended_at = '2026-10-26 12:00Z', end_reason = 'revoked_by_admin'",
		"ended_at = '2026-10-26 12:00Z', end_reason = 'revoked_by_subject', ended_by = 101",
		"ended_at = '2026-10-26 12:00Z', end_reason = 'left_role', ended_by = 101",
	} {
		if _, err := db.Exec("UPDATE contact_grants SET "+mutation+" WHERE request_id = $1", id); err == nil {
			t.Fatalf("malformed end accepted: %s", mutation)
		}
	}
	exec(`UPDATE contact_grants SET ended_at = '2026-10-26 12:00Z', end_reason = 'revoked_by_subject', ended_by = 102 WHERE request_id = $1`, id)
	// No user/group/event row was needed for retained IDs. Deleting live records
	// with those IDs must likewise leave both history rows untouched.
	exec(`INSERT INTO users (id, email) VALUES (101, 'requester@example.com'), (102, 'subject@example.com')`)
	exec(`DELETE FROM users WHERE id IN (101, 102)`)
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM contact_grants g JOIN contact_requests r ON r.id = g.request_id WHERE r.id = $1 AND r.status = 'granted'`, id).Scan(&count); err != nil || count != 1 {
		t.Fatalf("retained history = %d, %v", count, err)
	}
}

// accessDB is a migrated scratch database holding the shared departure and
// manager data of the exact-date access tests (R9 S4): users 1-7, group 10 with
// its memberships, one series with dates 30-36 and registrations 40-46. A grant
// is a write to a registration's access_* columns, so every case below is a
// direct SQL statement against the trigger and CHECKs that replaced
// occurrence_accesses.
type accessDB struct {
	t  *testing.T
	db *sql.DB
}

func newAccessDB(t *testing.T) accessDB {
	t.Helper()
	a := accessDB{t: t, db: openSQL(t, testdb.Fresh(t, true))}
	for _, q := range []string{
		`INSERT INTO users(id,email,status,role,account_type) VALUES
		 (1,'departure-leader@example.com','active','member','adult'),
		 (2,'departure-subject@example.com','active','member','adult'),
		 (3,'departure-member@example.com','active','member','adult'),
		 (4,'departure-admin@example.com','active','admin','adult'),
		 (5,'departure-coleader@example.com','active','member','adult'),
		 (6,'departure-teen@example.com','active','member','teen'),
		 (7,'departure-other-admin@example.com','active','admin','adult')`,
		`INSERT INTO groups(id,name,leader_id,status,visibility) VALUES(10,'Departure group',1,'active','invite_only')`,
		`INSERT INTO memberships(id,user_id,group_id,role,status) VALUES
		 (50,2,10,'member','active'),(51,1,10,'leader','active'),(52,3,10,'member','active'),
		 (53,5,10,'leader','active'),(54,4,10,'member','active'),(55,6,10,'member','active')`,
		`INSERT INTO events(id,group_id,title,description,starts_local,duration_minutes,tzid,rrule,location,visibility,created_by)
		 VALUES(20,10,'Departure series','','2026-12-01T18:00',60,'UTC','','','invite_only',1)`,
		`INSERT INTO event_occurrences(id,event_id,original_date,starts_at,ends_at,status) VALUES
		 (30,20,'2026-12-01','2026-12-01 18:00Z','2026-12-01 19:00Z','scheduled'),
		 (31,20,'2026-12-02','2026-12-02 18:00Z','2026-12-02 19:00Z','scheduled'),
		 (32,20,'2026-12-03','2026-12-03 18:00Z','2026-12-03 19:00Z','scheduled'),
		 (33,20,'2026-12-04','2026-12-04 18:00Z','2026-12-04 19:00Z','scheduled'),
		 (34,20,'2026-12-05','2026-12-05 18:00Z','2026-12-05 19:00Z','scheduled'),
		 (35,20,'2026-12-06','2026-12-06 18:00Z','2026-12-06 19:00Z','scheduled'),
		 (36,20,'2026-12-07','2026-12-07 18:00Z','2026-12-07 19:00Z','scheduled')`,
		`INSERT INTO registrations(id,occurrence_id,user_id,status,source) VALUES
		 (40,30,2,'maybe','web'),(41,31,2,'waitlisted','web'),(42,32,3,'going','admin'),
		 (43,33,3,'going','admin'),(44,34,6,'going','admin'),(45,35,4,'going','web'),
		 (46,36,3,'maybe','web')`,
		`INSERT INTO contact_requests(id,requester_id,subject_user_id,context_type,context_id,fields,needs_admin,admin_decision,created_at,expires_at)
		 VALUES(60,1,4,'group',10,'email',false,'not_needed','2026-10-20 12:00Z','2026-11-03 12:00Z')`,
		`INSERT INTO manager_add_blocks(subject_user_id,organizer_type,organizer_id) VALUES(2,'group',10),(3,'group',10)`,
	} {
		a.exec(q)
	}
	return a
}

func (a accessDB) exec(q string, args ...any) {
	a.t.Helper()
	if _, err := a.db.Exec(q, args...); err != nil {
		a.t.Fatalf("%s: %v", q, err)
	}
}

func (a accessDB) refuse(name, q string, args ...any) {
	a.t.Helper()
	if _, err := a.db.Exec(q, args...); err == nil {
		a.t.Fatalf("%s accepted", name)
	}
}

// micro is the canonical stamp Postgres derives for a timestamp literal: the
// integer microseconds the application writes into every snapshot.
func (a accessDB) micro(ts string) int64 {
	a.t.Helper()
	var m int64
	if err := a.db.QueryRow(`SELECT (extract(epoch FROM $1::timestamptz) * 1000000)::bigint`, ts).Scan(&m); err != nil {
		a.t.Fatal(err)
	}
	return m
}

// snapshot writes one access audit row exactly as the application does: the
// registration is the target, the metadata carries the complete grant.
func (a accessDB) snapshot(action string, registration, subject, occurrence int, origin, grantedAt string, extra map[string]any) {
	a.t.Helper()
	meta := map[string]any{"subjectUserId": subject, "occurrenceId": occurrence, "eventId": 20, "organizerType": "group", "organizerId": 10,
		"registrationId": registration, "origin": origin, "grantedAtMicro": a.micro(grantedAt)}
	for k, v := range extra {
		meta[k] = v
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		a.t.Fatal(err)
	}
	a.exec(`INSERT INTO audit_logs(action, target_type, target_id, meta_json) VALUES($1, 'registration', $2, $3::jsonb)`, action, registration, string(raw))
}

func (a accessDB) recordAdded(registration, subject, occurrence int, origin, grantedAt string, extra map[string]any) {
	a.t.Helper()
	a.snapshot("occurrence_access.added", registration, subject, occurrence, origin, grantedAt, extra)
}

// recordEnded adds the ended snapshot of a grant; endedBy 0 means no actor.
func (a accessDB) recordEnded(registration, subject, occurrence int, origin, grantedAt, endedAt, reason string, endedBy int, extra map[string]any) {
	a.t.Helper()
	merged := map[string]any{"endedAtMicro": a.micro(endedAt), "endReason": reason, "outcome": "ended"}
	if endedBy != 0 {
		merged["endedBy"] = endedBy
	}
	for k, v := range extra {
		merged[k] = v
	}
	a.snapshot("occurrence_access.ended", registration, subject, occurrence, origin, grantedAt, merged)
}

func (a accessDB) count(q string, args ...any) int {
	a.t.Helper()
	var n int
	if err := a.db.QueryRow(q, args...).Scan(&n); err != nil {
		a.t.Fatal(err)
	}
	return n
}

func TestRegistrationAccessDepartureOrigin(t *testing.T) {
	t.Parallel()
	a := newAccessDB(t)

	// Blocks govern new manager adds only. They do not invalidate standing
	// Maybe/Waitlisted registrations retained by an authorized departure.
	a.exec(`UPDATE registrations SET access_origin='departure_self',access_departure_membership_id=50,access_departure_actor_id=2,access_granted_at='2026-10-26 12:00Z' WHERE id=40`)
	a.exec(`UPDATE registrations SET access_origin='departure_removal',access_departure_membership_id=50,access_departure_actor_id=1,access_granted_at='2026-10-26 12:00Z' WHERE id=41`)

	// A manager add keeps its issuer requirement. A block, a teen direct add
	// and a non-Going manager add fail; a plain attendee row never needed a
	// provenance column to be written.
	a.exec(`DELETE FROM manager_add_blocks WHERE subject_user_id=3`)
	a.exec(`UPDATE registrations SET access_origin='manager_add',access_issuer_id=1,access_granted_at='2026-10-26 12:00Z' WHERE id=42`)
	a.refuse("manager add for Maybe", `UPDATE registrations SET access_origin='manager_add',access_issuer_id=1,access_granted_at='2026-10-26 12:00Z' WHERE id=46`)
	var origin string
	if err := a.db.QueryRow(`SELECT access_origin FROM registrations WHERE id=42`).Scan(&origin); err != nil || origin != "manager_add" {
		t.Fatalf("manager origin = %q, %v", origin, err)
	}
	a.exec(`INSERT INTO manager_add_blocks(subject_user_id,organizer_type,organizer_id) VALUES(3,'group',10)`)
	a.refuse("blocked manager add", `UPDATE registrations SET access_origin='manager_add',access_issuer_id=1,access_granted_at='2026-10-26 12:00Z' WHERE id=43`)
	a.refuse("direct teen manager add", `UPDATE registrations SET access_origin='manager_add',access_issuer_id=1,access_granted_at='2026-10-26 12:00Z' WHERE id=44`)
	a.refuse("manager origin without issuer", `UPDATE registrations SET access_origin='manager_add',access_granted_at='2026-10-26 12:00Z' WHERE id=45`)
	a.refuse("manager issuer is the subject", `UPDATE registrations SET access_origin='manager_add',access_issuer_id=4,access_granted_at='2026-10-26 12:00Z' WHERE id=45`)
	a.refuse("unknown origin", `UPDATE registrations SET access_origin='invited',access_issuer_id=1,access_granted_at='2026-10-26 12:00Z' WHERE id=45`)
	a.refuse("provenance without a grant stamp", `UPDATE registrations SET access_origin='manager_add',access_issuer_id=1 WHERE id=45`)
	a.refuse("stamp without provenance", `UPDATE registrations SET access_granted_at='2026-10-26 12:00Z' WHERE id=45`)

	for _, tt := range []struct{ name, set string }{
		{"self leave by another actor", `access_origin='departure_self',access_departure_membership_id=54,access_departure_actor_id=1`},
		{"self leave without an actor", `access_origin='departure_self',access_departure_membership_id=54`},
		{"removal by plain member", `access_origin='departure_removal',access_departure_membership_id=54,access_departure_actor_id=3`},
		{"leader removes admin", `access_origin='departure_removal',access_departure_membership_id=54,access_departure_actor_id=1`},
		{"wrong membership", `access_origin='departure_removal',access_departure_membership_id=52,access_departure_actor_id=7`},
		{"removal by the subject", `access_origin='departure_removal',access_departure_membership_id=54,access_departure_actor_id=4`},
		{"departure forged issuer", `access_origin='departure_removal',access_issuer_id=1,access_departure_membership_id=54,access_departure_actor_id=7`},
		{"departure forged request", `access_origin='departure_removal',access_request_id=60,access_departure_membership_id=54,access_departure_actor_id=7`},
		{"departure without a membership", `access_origin='departure_removal',access_departure_actor_id=7`},
		{"non-positive membership", `access_origin='departure_removal',access_departure_membership_id=0,access_departure_actor_id=7`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a.refuse(tt.name, `UPDATE registrations SET `+tt.set+`,access_granted_at='2026-10-26 12:00Z' WHERE id=45`)
		})
	}
	// An admin can authorize removal of an admin; this records the remover as
	// departure actor, never as a fictitious invitation issuer.
	a.exec(`UPDATE registrations SET access_origin='departure_removal',access_departure_membership_id=54,access_departure_actor_id=7,access_granted_at='2026-10-26 12:00Z' WHERE id=45`)

	// A live grant's provenance is immutable, and the subject and date it names
	// cannot move to another row of the table while it exists.
	for _, mutation := range []string{
		`access_origin='manager_add'`, `access_departure_actor_id=4`, `access_departure_membership_id=52`, `access_issuer_id=1`,
		`access_request_id=60`, `access_granted_at=access_granted_at+interval '1 second'`, `access_origin=''`,
	} {
		a.refuse("live provenance change "+mutation, `UPDATE registrations SET `+mutation+` WHERE id=40`)
	}
	a.refuse("identity transfer to another person", `UPDATE registrations SET user_id=3 WHERE id=40`)
	a.refuse("identity transfer to another date", `UPDATE registrations SET occurrence_id=36 WHERE id=40`)
	a.refuse("household takeover of a granted row", `UPDATE registrations SET user_id=NULL,household_person_id=1,registered_by=2 WHERE id=40`)
	// An ordinary answer on a granted row passes without any provenance check.
	a.exec(`UPDATE registrations SET status='going',guest_count=1,childcare_count=1,attendance_mode='remote',updated_at=now() WHERE id=40`)
	a.exec(`UPDATE registrations SET source='admin',waitlisted_at=now(),promoted_at=now(),removal_note='kept' WHERE id=40`)
}

// A leader who is the group's only leader cannot be the subject of a kept date
// on leaving or removal; a second leader makes it lawful. Household rows are not
// people with accounts and can never carry a grant.
func TestRegistrationAccessSoleLeaderAndHouseholdProvenance(t *testing.T) {
	t.Parallel()
	a := newAccessDB(t)
	a.exec(`INSERT INTO users(id,email,status,role,account_type) VALUES(8,'sole-leader@example.com','active','member','adult')`)
	a.exec(`INSERT INTO groups(id,name,leader_id,status,visibility) VALUES(11,'Sole leader group',8,'active','invite_only')`)
	a.exec(`INSERT INTO memberships(id,user_id,group_id,role,status) VALUES(56,8,11,'leader','active')`)
	a.exec(`INSERT INTO events(id,group_id,title,description,starts_local,duration_minutes,tzid,rrule,location,visibility,created_by)
	 VALUES(21,11,'Sole leader series','','2026-12-01T18:00',60,'UTC','','','invite_only',8)`)
	a.exec(`INSERT INTO event_occurrences(id,event_id,original_date,starts_at,ends_at,status) VALUES(37,21,'2026-12-01','2026-12-01 18:00Z','2026-12-01 19:00Z','scheduled')`)
	a.exec(`INSERT INTO registrations(id,occurrence_id,user_id,status,source) VALUES(47,37,8,'going','web')`)
	self := `UPDATE registrations SET access_origin='departure_self',access_departure_membership_id=56,access_departure_actor_id=8,access_granted_at='2026-10-26 12:00Z' WHERE id=47`
	removal := `UPDATE registrations SET access_origin='departure_removal',access_departure_membership_id=56,access_departure_actor_id=7,access_granted_at='2026-10-26 12:00Z' WHERE id=47`
	a.refuse("sole leader leaves", self)
	a.refuse("sole leader removed", removal)
	a.exec(`INSERT INTO memberships(id,user_id,group_id,role,status) VALUES(57,5,11,'leader','active')`)
	a.exec(self)
	if a.count(`SELECT count(*) FROM registrations WHERE id=47 AND access_origin='departure_self'`) != 1 {
		t.Fatal("departure with a second leader was not recorded")
	}

	// Household rows hold no account, so provenance is refused by CHECK for
	// every origin, and a grant cannot be moved onto one.
	a.exec(`INSERT INTO households(id,name,created_by) VALUES(1,'Household',2)`)
	a.exec(`INSERT INTO household_people(id,household_id,first_name,kind,created_by) VALUES(1,1,'Kit','adult',2)`)
	a.exec(`INSERT INTO registrations(id,occurrence_id,household_person_id,registered_by,status,source) VALUES(48,33,1,2,'going','web')`)
	for _, set := range []string{
		`access_origin='manager_add',access_issuer_id=1,access_granted_at='2026-10-26 12:00Z'`,
		`access_origin='departure_self',access_departure_membership_id=50,access_departure_actor_id=2,access_granted_at='2026-10-26 12:00Z'`,
		`access_origin='departure_removal',access_departure_membership_id=50,access_departure_actor_id=7,access_granted_at='2026-10-26 12:00Z'`,
	} {
		a.refuse("household provenance "+set, `UPDATE registrations SET `+set+` WHERE id=48`)
	}
	a.refuse("household row inserted with provenance", `INSERT INTO registrations(occurrence_id,household_person_id,registered_by,status,access_origin,access_issuer_id,access_granted_at)
	 VALUES(34,1,2,'going','manager_add',1,'2026-10-26 12:00Z')`)
}

// The end-state CHECK accepts occurrence_ended for a kept departure date and
// refuses the malformed shapes around it. Ending is final.
func TestRegistrationAccessDepartureExpiryEndState(t *testing.T) {
	t.Parallel()
	a := newAccessDB(t)
	a.exec(`UPDATE registrations SET access_origin='departure_self',access_departure_membership_id=50,access_departure_actor_id=2,access_granted_at='2026-10-26 12:00Z',status='going' WHERE id=40`)
	for _, tt := range []struct{ name, mutation string }{
		{"expiry with an actor", `access_ended_at='2026-12-01 19:00Z',access_end_reason='occurrence_ended',access_ended_by=2`},
		{"expiry before the grant", `access_ended_at='2026-10-25 12:00Z',access_end_reason='occurrence_ended'`},
		{"ended without a time", `access_end_reason='occurrence_ended'`},
		{"reason on a live row", `access_end_reason='occurrence_ended'`},
		{"actor on a live row", `access_ended_by=2`},
		{"unknown reason", `access_ended_at='2026-12-01 19:00Z',access_end_reason='expired'`},
		{"declined by someone else", `access_ended_at='2026-12-01 19:00Z',access_end_reason='declined',access_ended_by=1`},
		{"declined without an actor", `access_ended_at='2026-12-01 19:00Z',access_end_reason='declined'`},
		{"removed without an actor", `access_ended_at='2026-12-01 19:00Z',access_end_reason='removed'`},
		{"system reason with an actor", `access_ended_at='2026-12-01 19:00Z',access_end_reason='account_disabled',access_ended_by=1`},
		{"non-positive actor", `access_ended_at='2026-12-01 19:00Z',access_end_reason='removed',access_ended_by=0`},
		{"end time without a reason", `access_ended_at='2026-12-01 19:00Z'`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a.refuse(tt.name, `UPDATE registrations SET `+tt.mutation+` WHERE id=40`)
		})
	}
	a.exec(`UPDATE registrations SET access_ended_at='2026-12-01 19:00Z',access_end_reason='occurrence_ended' WHERE id=40`)
	// Terminal fields cannot change, and the end cannot be undone: not without
	// snapshots, and not as a "replacement" that is the same grant.
	for _, mutation := range []string{
		`access_ended_at=NULL,access_end_reason='',access_ended_by=NULL`,
		`access_end_reason='context_changed'`,
		`access_ended_at='2026-12-01 20:00Z'`,
		`access_ended_by=2`,
		`access_origin=''`,
		`access_origin='',access_issuer_id=NULL,access_departure_membership_id=NULL,access_departure_actor_id=NULL,access_granted_at=NULL,access_ended_at=NULL,access_end_reason='',access_ended_by=NULL`,
	} {
		a.refuse("terminal change "+mutation, `UPDATE registrations SET `+mutation+` WHERE id=40`)
	}
	a.recordAdded(40, 2, 30, "departure_self", "2026-10-26 12:00Z", map[string]any{"departureMembershipId": 50, "departureActorId": 2})
	a.recordEnded(40, 2, 30, "departure_self", "2026-10-26 12:00Z", "2026-12-01 19:00Z", "occurrence_ended", 0, map[string]any{"departureMembershipId": 50, "departureActorId": 2})
	a.refuse("an ended departure date came back to life", `UPDATE registrations SET access_ended_at=NULL,access_end_reason='',access_ended_by=NULL WHERE id=40`)
}

// A manager grant superseded by a departure keeps its manager provenance and
// ends with the dedicated reason and an actor.
func TestRegistrationAccessSupersessionEndState(t *testing.T) {
	t.Parallel()
	a := newAccessDB(t)
	a.exec(`DELETE FROM manager_add_blocks`)
	a.exec(`UPDATE registrations SET status='going',access_origin='manager_add',access_issuer_id=1,access_granted_at='2026-11-01 12:00Z' WHERE id=40`)
	for _, tt := range []struct{ name, mutation string }{
		{"superseded without an actor", `access_ended_at='2026-11-02 12:00Z',access_end_reason='departure_superseded'`},
		{"superseded before the grant", `access_ended_at='2026-10-31 12:00Z',access_end_reason='departure_superseded',access_ended_by=2`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a.refuse(tt.name, `UPDATE registrations SET `+tt.mutation+` WHERE id=40`)
		})
	}
	a.exec(`UPDATE registrations SET access_ended_at='2026-11-02 12:00Z',access_end_reason='departure_superseded',access_ended_by=2 WHERE id=40`)
	var origin string
	var issuer int64
	if err := a.db.QueryRow(`SELECT access_origin,access_issuer_id FROM registrations WHERE id=40`).Scan(&origin, &issuer); err != nil || origin != "manager_add" || issuer != 1 {
		t.Fatalf("manager provenance changed: origin=%q issuer=%d err=%v", origin, issuer, err)
	}
}

// DELETE: the audit log is the only record of an ended grant once its row is
// gone, so the trigger demands the complete, matching snapshots first. A row
// with no provenance deletes freely.
func TestRegistrationAccessDeleteNeedsMatchingSnapshots(t *testing.T) {
	t.Parallel()
	a := newAccessDB(t)
	a.exec(`DELETE FROM manager_add_blocks`)
	a.exec(`UPDATE registrations SET status='going',access_origin='manager_add',access_issuer_id=1,access_granted_at='2026-11-01 12:00Z' WHERE id=40`)
	a.refuse("deleting a live grant", `DELETE FROM registrations WHERE id=40`)
	a.exec(`UPDATE registrations SET access_ended_at='2026-11-02 12:00Z',access_end_reason='declined',access_ended_by=2 WHERE id=40`)
	a.refuse("deleting an ended grant with no snapshots", `DELETE FROM registrations WHERE id=40`)

	extra := map[string]any{"issuerId": 1}
	a.recordAdded(40, 2, 30, "manager_add", "2026-11-01 12:00Z", extra)
	a.refuse("deleting with only the added snapshot", `DELETE FROM registrations WHERE id=40`)
	// Each mismatch of the ended snapshot leaves it unmatched.
	for name, tc := range map[string]struct {
		subject, occurrence int
		origin, grantedAt   string
		endedAt, reason     string
		endedBy             int
		extra               map[string]any
	}{
		"other subject":    {3, 30, "manager_add", "2026-11-01 12:00Z", "2026-11-02 12:00Z", "declined", 2, extra},
		"other date":       {2, 31, "manager_add", "2026-11-01 12:00Z", "2026-11-02 12:00Z", "declined", 2, extra},
		"other origin":     {2, 30, "departure_self", "2026-11-01 12:00Z", "2026-11-02 12:00Z", "declined", 2, extra},
		"other grant":      {2, 30, "manager_add", "2026-11-01 12:00:01Z", "2026-11-02 12:00Z", "declined", 2, extra},
		"other end":        {2, 30, "manager_add", "2026-11-01 12:00Z", "2026-11-02 12:00:01Z", "declined", 2, extra},
		"other reason":     {2, 30, "manager_add", "2026-11-01 12:00Z", "2026-11-02 12:00Z", "removed", 2, extra},
		"other actor":      {2, 30, "manager_add", "2026-11-01 12:00Z", "2026-11-02 12:00Z", "declined", 1, extra},
		"missing actor":    {2, 30, "manager_add", "2026-11-01 12:00Z", "2026-11-02 12:00Z", "declined", 0, extra},
		"other issuer":     {2, 30, "manager_add", "2026-11-01 12:00Z", "2026-11-02 12:00Z", "declined", 2, map[string]any{"issuerId": 3}},
		"invented request": {2, 30, "manager_add", "2026-11-01 12:00Z", "2026-11-02 12:00Z", "declined", 2, map[string]any{"issuerId": 1, "requestId": 60}},
	} {
		a.recordEnded(40, tc.subject, tc.occurrence, tc.origin, tc.grantedAt, tc.endedAt, tc.reason, tc.endedBy, tc.extra)
		a.refuse("mismatched ended snapshot: "+name, `DELETE FROM registrations WHERE id=40`)
	}
	// A snapshot recorded for another registration does not count either.
	a.exec(`INSERT INTO audit_logs(action, target_type, target_id, meta_json)
	 SELECT action, target_type, 41, meta_json FROM audit_logs WHERE action='occurrence_access.added' AND target_id=40`)
	a.refuse("snapshots of a different registration", `DELETE FROM registrations WHERE id=40`)
	a.recordEnded(40, 2, 30, "manager_add", "2026-11-01 12:00Z", "2026-11-02 12:00Z", "declined", 2, extra)
	before := a.count(`SELECT count(*) FROM audit_logs`)
	a.exec(`DELETE FROM registrations WHERE id=40`)
	if a.count(`SELECT count(*) FROM audit_logs`) != before {
		t.Fatal("deleting the row touched the audit history")
	}
	if a.count(`SELECT count(*) FROM audit_logs WHERE target_id=40 AND action IN ('occurrence_access.added','occurrence_access.ended')`) < 2 {
		t.Fatal("the snapshots did not outlive the row")
	}
	// No provenance, no requirement.
	a.exec(`DELETE FROM registrations WHERE id=42`)
	a.exec(`DELETE FROM registrations WHERE id=46`)
}

// Replacing an ended grant destroys the provenance it carried, so the same
// snapshots are demanded first, and a replacement must itself start live.
func TestRegistrationAccessReplacementNeedsHistoryAndMovesForward(t *testing.T) {
	t.Parallel()
	a := newAccessDB(t)
	a.exec(`DELETE FROM manager_add_blocks`)
	a.exec(`UPDATE registrations SET status='going',access_origin='departure_self',access_departure_membership_id=50,access_departure_actor_id=2,access_granted_at='2026-11-01 12:00Z' WHERE id=40`)
	a.exec(`UPDATE registrations SET access_ended_at='2026-11-02 12:00Z',access_end_reason='declined',access_ended_by=2 WHERE id=40`)
	replace := func(grantedAt string) string {
		return `UPDATE registrations SET access_origin='departure_removal',access_departure_membership_id=50,access_departure_actor_id=1,access_granted_at='` + grantedAt + `',
		 access_ended_at=NULL,access_end_reason='',access_ended_by=NULL WHERE id=40`
	}
	a.refuse("overwriting an ended grant without history", replace("2026-11-03 12:00Z"))
	extra := map[string]any{"departureMembershipId": 50, "departureActorId": 2}
	a.recordAdded(40, 2, 30, "departure_self", "2026-11-01 12:00Z", extra)
	a.refuse("overwriting with only the added snapshot", replace("2026-11-03 12:00Z"))
	a.recordEnded(40, 2, 30, "departure_self", "2026-11-01 12:00Z", "2026-11-02 12:00Z", "declined", 2, extra)

	// Same-row ordering: a later stamp than the old grant, and no earlier than the old end.
	a.refuse("equal to the old grant stamp", replace("2026-11-01 12:00Z"))
	a.refuse("before the old grant stamp", replace("2026-10-31 12:00Z"))
	a.refuse("between the old grant and its end", replace("2026-11-02 11:59Z"))
	// A replacement is a grant: it leaves no end fields, and it is validated like one.
	a.refuse("replacement that is already ended", `UPDATE registrations SET access_origin='departure_removal',access_departure_membership_id=50,access_departure_actor_id=1,
	 access_granted_at='2026-11-03 12:00Z',access_ended_at='2026-11-04 12:00Z',access_end_reason='removed',access_ended_by=1 WHERE id=40`)
	a.refuse("replacement by a plain member", `UPDATE registrations SET access_origin='departure_removal',access_departure_membership_id=50,access_departure_actor_id=3,
	 access_granted_at='2026-11-03 12:00Z',access_ended_at=NULL,access_end_reason='',access_ended_by=NULL WHERE id=40`)
	// Equal to the old end is allowed (the old end is not after the new grant).
	a.exec(replace("2026-11-02 12:00Z"))
	var origin string
	if err := a.db.QueryRow(`SELECT access_origin FROM registrations WHERE id=40`).Scan(&origin); err != nil || origin != "departure_removal" {
		t.Fatalf("valid replacement not applied: %q %v", origin, err)
	}
	// Nothing about an ordinary answer needs any of this.
	a.exec(`UPDATE registrations SET guest_count=2 WHERE id=40`)
}

// Repeat and stale-request checks read the audit snapshots, so they survive the
// deletion of the registration row they were first recorded on.
func TestRegistrationAccessRepeatAndStaleChecksSurviveDeletion(t *testing.T) {
	t.Parallel()
	a := newAccessDB(t)
	a.exec(`DELETE FROM manager_add_blocks`)
	a.exec(`UPDATE registrations SET status='going',access_origin='manager_add',access_issuer_id=1,access_granted_at='2026-11-01 12:00Z' WHERE id=40`)
	a.exec(`UPDATE registrations SET access_ended_at='2026-11-02 12:00Z',access_end_reason='declined',access_ended_by=2 WHERE id=40`)
	extra := map[string]any{"issuerId": 1}
	a.recordAdded(40, 2, 30, "manager_add", "2026-11-01 12:00Z", extra)
	a.recordEnded(40, 2, 30, "manager_add", "2026-11-01 12:00Z", "2026-11-02 12:00Z", "declined", 2, extra)
	a.exec(`DELETE FROM registrations WHERE id=40`)
	// The same person is registered again on a newly allocated registration.
	a.exec(`INSERT INTO registrations(id,occurrence_id,user_id,status,source) VALUES(70,30,2,'going','admin')`)
	direct := `UPDATE registrations SET access_origin='manager_add',access_issuer_id=1,access_granted_at='2026-11-05 12:00Z' WHERE id=70`
	a.refuse("direct re-add after the row was deleted", direct)
	a.exec(`DELETE FROM registrations WHERE id=70`)
	a.refuse("direct re-add as an insert", `INSERT INTO registrations(id,occurrence_id,user_id,status,source,access_origin,access_issuer_id,access_granted_at)
	 VALUES(72,30,2,'going','admin','manager_add',1,'2026-11-05 12:00Z')`)
	a.exec(`INSERT INTO registrations(id,occurrence_id,user_id,status,source) VALUES(70,30,2,'going','admin')`)

	request := func(id int, created string) {
		a.exec(`INSERT INTO contact_requests(id,requester_id,subject_user_id,context_type,context_id,fields,purpose,occurrence_id,add_attendance_mode,needs_admin,admin_decision,created_at,expires_at)
		 VALUES($1,1,2,'event',20,'','occurrence_add',30,'in_person',false,'not_needed',$2::timestamptz,$2::timestamptz + interval '14 days')`, id, created)
	}
	complete := func(id int, grantedAt string) error {
		tx, err := a.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err = tx.Exec(`UPDATE contact_requests SET subject_decision='approved',subject_decided_by=2,subject_decided_at=created_at + interval '1 hour',
		 status='granted',completed_at=created_at + interval '2 hours',completed_target_id=70 WHERE id=$1`, id); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE registrations SET access_origin='manager_add',access_issuer_id=1,access_request_id=$1,access_granted_at=$2::timestamptz WHERE id=70`, id, grantedAt); err != nil {
			return err
		}
		return tx.Commit()
	}
	// A request created before the latest end is stale; equality is allowed.
	request(80, "2026-11-01 13:00Z")
	if err := complete(80, "2026-11-05 12:00Z"); err == nil {
		t.Fatal("a stale approved request resurrected access after the row was deleted")
	}
	request(81, "2026-11-02 12:00Z")
	if err := complete(81, "2026-11-05 12:00Z"); err != nil {
		t.Fatalf("a request created exactly at the old end was refused: %v", err)
	}
	if a.count(`SELECT count(*) FROM registrations WHERE id=70 AND access_request_id=81 AND access_ended_at IS NULL`) != 1 {
		t.Fatal("approved fresh request did not grant")
	}
}

// Every request can authorize one grant only, even on the same registration.
func TestRegistrationAccessRequestIsSingleUse(t *testing.T) {
	t.Parallel()
	a := newAccessDB(t)
	a.exec(`DELETE FROM manager_add_blocks`)
	a.exec(`INSERT INTO contact_requests(id,requester_id,subject_user_id,context_type,context_id,fields,purpose,occurrence_id,add_attendance_mode,needs_admin,admin_decision,created_at,expires_at)
	 VALUES(80,1,2,'event',20,'','occurrence_add',30,'in_person',false,'not_needed','2026-11-01 14:00Z','2026-11-15 12:00Z')`)
	a.exec(`UPDATE registrations SET status='going' WHERE id=40`)
	tx, err := a.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE contact_requests SET subject_decision='approved',subject_decided_by=2,subject_decided_at='2026-11-01 14:00Z',status='granted',completed_at='2026-11-01 14:00Z',completed_target_id=40 WHERE id=80`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE registrations SET access_origin='manager_add',access_issuer_id=1,access_request_id=80,access_granted_at='2026-11-01 14:00Z' WHERE id=40`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	a.exec(`UPDATE registrations SET access_ended_at='2026-11-01 14:00Z',access_end_reason='declined',access_ended_by=2 WHERE id=40`)
	a.recordAdded(40, 2, 30, "manager_add", "2026-11-01 14:00Z", map[string]any{"issuerId": 1, "requestId": 80})
	a.recordEnded(40, 2, 30, "manager_add", "2026-11-01 14:00Z", "2026-11-01 14:00Z", "declined", 2, map[string]any{"issuerId": 1, "requestId": 80})
	a.refuse("the same request twice", `UPDATE registrations SET access_origin='manager_add',access_issuer_id=1,access_request_id=80,access_granted_at='2026-11-02 12:00Z',
	 access_ended_at=NULL,access_end_reason='',access_ended_by=NULL WHERE id=40`)
	a.refuse("another registration claiming the request", `UPDATE registrations SET access_origin='manager_add',access_issuer_id=1,access_request_id=80,access_granted_at='2026-11-02 12:00Z' WHERE id=42`)
}

// The trigger's rules are declared once and cover every write: it fires before
// INSERT, UPDATE and DELETE; the registration foreign keys cannot cascade a
// delete behind it; and the structures R9 retires are gone.
func TestRegistrationAccessSchemaShape(t *testing.T) {
	t.Parallel()
	a := newAccessDB(t)
	var def string
	if err := a.db.QueryRow(`SELECT pg_get_triggerdef(oid) FROM pg_trigger WHERE tgname='registration_access_provenance' AND tgrelid='registrations'::regclass`).Scan(&def); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"BEFORE INSERT OR DELETE OR UPDATE ON public.registrations", "FOR EACH ROW", "registration_access_provenance()"} {
		if !strings.Contains(def, want) {
			t.Fatalf("trigger %q lacks %q", def, want)
		}
	}
	rows, err := a.db.Query(`SELECT conname, confdeltype::text FROM pg_constraint WHERE conrelid='registrations'::regclass AND contype='f' AND conname IN ('fk_registrations_occurrence','fk_registrations_user')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var name, action string
		if err := rows.Scan(&name, &action); err != nil {
			t.Fatal(err)
		}
		if action != "a" {
			t.Fatalf("%s deletes by %q; only NO ACTION keeps the delete trigger in the way", name, action)
		}
		seen++
	}
	if seen != 2 {
		t.Fatalf("registration foreign keys found: %d", seen)
	}
	for _, gone := range []string{"occurrence_accesses", "occurrence_access_provenance"} {
		if a.count(`SELECT count(*) FROM pg_class WHERE relname=$1`, gone)+a.count(`SELECT count(*) FROM pg_proc WHERE proname=$1`, gone) != 0 {
			t.Fatalf("%s survived the fold", gone)
		}
	}
	for _, idx := range []string{"idx_registrations_access_request", "idx_registrations_live_access"} {
		if a.count(`SELECT count(*) FROM pg_indexes WHERE indexname=$1 AND tablename='registrations'`, idx) != 1 {
			t.Fatalf("index %s missing", idx)
		}
	}
	// The partial unique index lets one request ground one registration only.
	var tables int
	if err := a.db.QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE' AND table_name <> 'goose_db_version'`).Scan(&tables); err != nil || tables != len(models.All()) || tables != 39 {
		t.Fatalf("tables = %d (%v), models = %d, want 39", tables, err, len(models.All()))
	}
}

// membership tenure: each active stretch is a distinct authorization
// lifecycle. The trigger advances it when a row becomes active again and
// refuses any direct write; role changes never advance it.
func TestMembershipTenureGuard(t *testing.T) {
	t.Parallel()
	db := openSQL(t, testdb.Fresh(t, true))
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	tenure := func(id int) (int64, time.Time) {
		t.Helper()
		var n int64
		var joined time.Time
		if err := db.QueryRow(`SELECT tenure, joined_at FROM memberships WHERE id=$1`, id).Scan(&n, &joined); err != nil {
			t.Fatal(err)
		}
		return n, joined
	}
	joined := time.Date(2026, time.January, 2, 3, 4, 0, 0, time.UTC)
	exec(`INSERT INTO users(id,email,status,role) VALUES(1,'tenure@example.com','active','member'),(2,'tenure-two@example.com','active','member')`)
	exec(`INSERT INTO groups(id,name,leader_id,status,visibility) VALUES(10,'Tenure group',1,'active','public'),(11,'Second tenure group',1,'active','public')`)
	exec(`INSERT INTO memberships(id,user_id,group_id,role,status,joined_at) VALUES(50,1,10,'member','active',$1)`, joined)
	if n, j := tenure(50); n != 1 || !j.Equal(joined) {
		t.Fatalf("new membership tenure=%d joined=%s, want 1 and %s", n, j, joined)
	}
	exec(`UPDATE memberships SET role='leader' WHERE id=50`)
	exec(`UPDATE memberships SET status='active' WHERE id=50`)
	if n, _ := tenure(50); n != 1 {
		t.Fatalf("a role change or a no-op status write advanced tenure to %d", n)
	}
	exec(`UPDATE memberships SET status='removed' WHERE id=50`)
	exec(`UPDATE memberships SET status='active' WHERE id=50`)
	if n, j := tenure(50); n != 2 || !j.Equal(joined) {
		t.Fatalf("after removal and return tenure=%d joined=%s, want 2 and the first-ever join %s", n, j, joined)
	}
	exec(`UPDATE memberships SET status='left' WHERE id=50`)
	exec(`UPDATE memberships SET status='active' WHERE id=50`)
	if n, _ := tenure(50); n != 3 {
		t.Fatalf("after leaving and returning tenure=%d, want 3", n)
	}
	// A row created inactive starts a lifecycle when it first becomes active.
	exec(`INSERT INTO memberships(id,user_id,group_id,role,status,joined_at) VALUES(51,2,10,'member','removed',$1)`, joined)
	exec(`UPDATE memberships SET status='active' WHERE id=51`)
	if n, _ := tenure(51); n != 2 {
		t.Fatalf("reactivating a removed row gave tenure=%d, want 2", n)
	}
	for _, q := range []string{
		`UPDATE memberships SET tenure=9 WHERE id=50`,
		`UPDATE memberships SET tenure=1, status='removed' WHERE id=50`,
		`INSERT INTO memberships(id,user_id,group_id,role,status,tenure) VALUES(52,1,11,'member','active',0)`,
	} {
		if _, err := db.Exec(q); err == nil {
			t.Fatalf("direct tenure write accepted: %s", q)
		}
	}
	var trigger, function bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid='memberships'::regclass AND tgname='memberships_tenure_guard' AND NOT tgisinternal)`).Scan(&trigger); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT to_regprocedure('guard_membership_tenure()') IS NOT NULL`).Scan(&function); err != nil {
		t.Fatal(err)
	}
	if !trigger || !function {
		t.Fatalf("tenure guard trigger=%v function=%v", trigger, function)
	}
}

func TestSeriesUndoReceiptConstraints(t *testing.T) {
	t.Parallel()
	db := openSQL(t, testdb.Fresh(t, true))
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO users(id,email) VALUES(91001,'undo-migration@example.com')`)
	exec(`INSERT INTO events(id,title,description,visibility,starts_local,duration_minutes,tzid,rrule,location,created_by) VALUES(91002,'Undo','','public','2026-10-01T10:00',60,'UTC','','',91001)`)
	exec(`INSERT INTO series_undo_receipts(token_hash,actor_id,event_id,snapshot,created_at,expires_at) VALUES('ok',91001,91002,'[]','2026-10-01 10:00Z','2026-10-01 10:00:10Z')`)
	for _, q := range []string{
		`INSERT INTO series_undo_receipts(token_hash,actor_id,event_id,snapshot,created_at,expires_at) VALUES('object',91001,91002,'{}','2026-10-01 10:00Z','2026-10-01 10:00:10Z')`,
		`INSERT INTO series_undo_receipts(token_hash,actor_id,event_id,snapshot,created_at,expires_at) VALUES('expired',91001,91002,'[]','2026-10-01 10:00Z','2026-10-01 10:00Z')`,
	} {
		if _, err := db.Exec(q); err == nil {
			t.Fatalf("invalid receipt accepted: %s", q)
		}
	}
}

// TestOccurrenceCancelSource checks the cancel_source column (DEC-91): it defaults to
// empty (a cancelled date with no recorded source), only the three values are
// accepted.
func TestOccurrenceCancelSource(t *testing.T) {
	t.Parallel()
	db := openSQL(t, testdb.Fresh(t, true))
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO users(id,email) VALUES(92001,'cancel-source@example.com')`)
	exec(`INSERT INTO events(id,title,description,visibility,starts_local,duration_minutes,tzid,rrule,location,created_by) VALUES(92002,'Source','','public','2026-10-01T10:00',60,'UTC','','',92001)`)
	exec(`INSERT INTO event_occurrences(id,event_id,original_date,starts_at,ends_at,status) VALUES(92003,92002,'2026-12-01','2026-12-01 10:00Z','2026-12-01 11:00Z','cancelled')`)
	var source string
	if err := db.QueryRow(`SELECT cancel_source FROM event_occurrences WHERE id = 92003`).Scan(&source); err != nil || source != "" {
		t.Fatalf("default cancel_source = %q, %v", source, err)
	}
	for _, v := range []string{"series", "manual", ""} {
		exec(`UPDATE event_occurrences SET cancel_source = $1 WHERE id = 92003`, v)
	}
	if _, err := db.Exec(`UPDATE event_occurrences SET cancel_source = 'bogus' WHERE id = 92003`); err == nil {
		t.Fatal("unknown cancel_source accepted")
	}
}

// wantAppliedRows is what goose_db_version holds after every migration up to
// latest applied on an empty database: goose's version 0 row, then each
// version, all applied (appliedRows' format).
func wantAppliedRows(latest int64) string {
	var got []string
	for v := int64(0); v <= latest; v++ {
		got = append(got, fmt.Sprintf("%d:true", v))
	}
	return strings.Join(got, ",")
}

// TestMigrateEmptyDatabase applies every migration to a database with nothing
// in it. The stored bookkeeping must be goose's version 0 row and every
// version applied: `migrate status` alone cannot tell this from an old-chain
// database.
func TestMigrateEmptyDatabase(t *testing.T) {
	t.Parallel()
	gdb, err := testdb.Open(testdb.Fresh(t, false))
	if err != nil {
		t.Fatal(err)
	}
	db, err := gdb.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	p, err := migrations.NewProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	if pending, err := p.HasPending(ctx); err != nil || pending {
		t.Fatalf("pending after up: %v %v", pending, err)
	}
	latest := latestVersion(t)
	if version, err := p.GetDBVersion(ctx); err != nil || version != latest {
		t.Fatalf("version after up = %d, %v, want %d", version, err, latest)
	}
	if got, want := appliedRows(t, db), wantAppliedRows(latest); got != want {
		t.Fatalf("goose_db_version = %s, want exactly %s (version 0 and every migration applied)", got, want)
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name IN ('users','groups','events','event_hosts','event_occurrences','registrations','memberships','settings')").Scan(&n); err != nil || n != 8 {
		t.Fatalf("baseline tables = %d (%v), want 8", n, err)
	}
	for _, m := range models.All() {
		if !gdb.Migrator().HasTable(m) {
			t.Fatalf("the baseline lacks the table for %T", m)
		}
	}
}

// TestNoDrift is the C1 drift check: AutoMigrate run for real against a
// freshly migrated database must not want to CREATE or ALTER anything.
func TestNoDrift(t *testing.T) {
	t.Parallel()
	db, err := testdb.Open(testdb.Fresh(t, true))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if s, err := db.DB(); err == nil {
			s.Close()
		}
	})
	changes, err := Drift(db, models.All()...)
	if err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	if len(changes) > 0 {
		t.Fatalf("models drift from migrations; add a migration for:\n%s", strings.Join(changes, "\n"))
	}
}

// driftProbe is the settings table plus a field no migration created.
type driftProbe struct {
	models.Setting
	Unmigrated string
}

func (driftProbe) TableName() string { return "settings" }

// TestDriftCheckCatchesUnmigratedField proves the check fails when a model
// gains a field without a migration.
func TestDriftCheckCatchesUnmigratedField(t *testing.T) {
	t.Parallel()
	db, err := testdb.Open(testdb.Fresh(t, true))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if s, err := db.DB(); err == nil {
			s.Close()
		}
	})
	changes, err := Drift(db, &driftProbe{})
	if err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	if len(changes) != 1 || !strings.Contains(changes[0], "ALTER TABLE") || !strings.Contains(changes[0], "unmigrated") {
		t.Fatalf("want one ALTER adding unmigrated, got %q", changes)
	}
}

// TestMigrationsTakeAdvisoryLock holds the migration lock from another
// session and checks that a second migrator waits instead of migrating.
func TestMigrationsTakeAdvisoryLock(t *testing.T) {
	t.Parallel()
	name := testdb.Fresh(t, false)
	holder := openSQL(t, name)
	ctx := context.Background()
	conn, err := holder.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", migrations.LockID); err != nil {
		t.Fatal(err)
	}

	db := openSQL(t, name)
	p, err := migrations.NewProvider(db, lock.WithLockTimeout(1, 2))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := p.Up(ctx); err == nil {
		t.Fatal("migrated while another session held the lock")
	}
	if time.Since(start) < time.Second {
		t.Fatal("gave up without waiting for the lock")
	}
	var exists bool
	if err := db.QueryRow("SELECT to_regclass('public.users') IS NOT NULL").Scan(&exists); err != nil || exists {
		t.Fatalf("users table exists = %v (%v); nothing should have run", exists, err)
	}

	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", migrations.LockID); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("migrate after unlock: %v", err)
	}
}

// Membership status has three values (DEC-35 removed, DEC-86 left); a church-wide
// event is never leader-opened and is only public or private (DEC-48).
func TestMembershipStatusAndLeaderOpenedConstraints(t *testing.T) {
	t.Parallel()
	db := openSQL(t, testdb.Fresh(t, true))
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO users (id, email) VALUES (1, 'a@example.com'), (2, 'b@example.com'), (3, 'c@example.com')`)
	exec(`INSERT INTO groups (id, name, leader_id, visibility) VALUES (1, 'Open', 1, 'public')`)
	exec(`INSERT INTO memberships (user_id, group_id, role, status) VALUES (1, 1, 'leader', 'active')`)
	exec(`INSERT INTO memberships (user_id, group_id, role, status) VALUES (2, 1, 'member', 'removed'), (3, 1, 'member', 'left')`)
	for _, status := range []string{"pending_return", "pending", "banned", ""} {
		if _, err := db.Exec(`UPDATE memberships SET status = $1 WHERE user_id = 1`, status); err == nil {
			t.Fatalf("membership status %q accepted", status)
		}
	}

	ins := `INSERT INTO events (id, group_id, title, description, starts_local, duration_minutes, tzid, rrule, location, created_by, visibility, leader_opened)
		VALUES ($1, $2, 'E', '', '2026-12-01T19:00', 60, 'UTC', '', '', 1, $3, $4)`
	exec(ins, 1, 1, "public", false)
	exec(ins, 2, 1, "public", true) // a leader may open a group event wider
	exec(ins, 3, nil, "public", false)
	exec(ins, 4, nil, "private", false)
	for name, args := range map[string][]any{
		"church-wide event opened by a leader": {5, nil, "public", true},
		"church-wide invite-only event":        {6, nil, "invite_only", false},
	} {
		if _, err := db.Exec(ins, args...); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if _, err := db.Exec(`UPDATE events SET leader_opened = true WHERE id = 3`); err == nil {
		t.Fatal("a church-wide event can't be leader-opened")
	}
	var opened bool
	if err := db.QueryRow(`SELECT leader_opened FROM events WHERE id = 1`).Scan(&opened); err != nil || opened {
		t.Fatalf("leader_opened default = %v (%v), want false", opened, err)
	}
}

// The registration status vocabulary of the final schema (M1.5): waitlisted and
// removed are real states, and nothing else is.
func TestRegistrationStatusVocabulary(t *testing.T) {
	t.Parallel()
	db := openSQL(t, testdb.Fresh(t, true))
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO users (id, email) SELECT n, 'reg-' || n || '@example.com' FROM generate_series(1, 8) n`)
	exec(`INSERT INTO events (id, title, starts_local, duration_minutes, tzid, rrule, location, description, visibility, created_by)
		VALUES (1, 'E', '2026-10-20T10:00', 60, 'America/Denver', '', 'Hall', '', 'public', 1)`)
	exec(`INSERT INTO event_occurrences (id, event_id, original_date, starts_at, ends_at, status)
		VALUES (1, 1, '2026-10-20', '2026-10-20 16:00+00', '2026-10-20 17:00+00', 'scheduled')`)
	for i, status := range []string{"going", "maybe", "declined", "cancelled", "waitlisted", "removed"} {
		var waitlistedAt any
		if status == "waitlisted" {
			waitlistedAt = time.Now()
		}
		exec(`INSERT INTO registrations (occurrence_id, user_id, status, waitlisted_at) VALUES (1, $1, $2, $3)`, i+1, status, waitlistedAt)
	}
	if _, err := db.Exec(`INSERT INTO registrations (occurrence_id, user_id, status) VALUES (1, 7, 'bogus')`); err == nil {
		t.Fatal("an unknown registration status was accepted")
	}
	if _, err := db.Exec(`INSERT INTO registrations (occurrence_id, user_id, status, source) VALUES (1, 8, 'going', 'carrier_pigeon')`); err == nil {
		t.Fatal("an unknown registration source was accepted")
	}
}

// Households: one household per account, people of a known kind, and guardian
// links naming exactly one of a teen account or a household person, with at
// most one live link per guardian and teen.
func TestHouseholdTablesConstraints(t *testing.T) {
	t.Parallel()
	db := openSQL(t, testdb.Fresh(t, true))
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO users (id, email, name) VALUES (1, 'a@example.com', 'Ann'), (2, 'b@example.com', 'Bob')`)
	exec(`INSERT INTO households (id, name, created_by) VALUES (1, 'Ann household', 1)`)
	exec(`INSERT INTO household_accounts (household_id, user_id, relation) VALUES (1, 1, 'guardian')`)
	if _, err := db.Exec(`INSERT INTO household_accounts (household_id, user_id, relation) VALUES (1, 1, 'adult')`); err == nil {
		t.Fatal("an account joined a second household row")
	}
	exec(`INSERT INTO household_people (id, household_id, first_name, kind, created_by) VALUES (1, 1, 'Kid', 'child', 1)`)
	exec(`INSERT INTO guardian_links (guardian_user_id, person_id) VALUES (1, 1)`)
	if _, err := db.Exec(`INSERT INTO guardian_links (guardian_user_id, teen_user_id, person_id) VALUES (1, 2, 1)`); err == nil {
		t.Fatal("a guardian link named both a teen and a person")
	}
	if _, err := db.Exec(`INSERT INTO guardian_links (guardian_user_id) VALUES (1)`); err == nil {
		t.Fatal("a guardian link named nobody")
	}
	if _, err := db.Exec(`INSERT INTO household_people (household_id, first_name, kind, created_by) VALUES (1, 'X', 'alien', 1)`); err == nil {
		t.Fatal("an unknown household person kind was accepted")
	}
	var photo bool
	if err := db.QueryRow(`SELECT photo_consent FROM household_people WHERE id = 1`).Scan(&photo); err != nil || photo {
		t.Fatalf("photo consent default = %v (%v), want false", photo, err)
	}
	exec(`INSERT INTO guardian_links (guardian_user_id, teen_user_id) VALUES (1, 2)`)
	if _, err := db.Exec(`INSERT INTO guardian_links (guardian_user_id, teen_user_id) VALUES (1, 2)`); err == nil {
		t.Fatal("a second live guardian link for the same teen was accepted")
	}
	exec(`UPDATE guardian_links SET status = 'revoked' WHERE teen_user_id = 2`)
	exec(`INSERT INTO guardian_links (guardian_user_id, teen_user_id) VALUES (1, 2)`)
}

// #78: the guest manage-link lookup uses a partial unique index, not a scan.
func TestManageTokenIndex(t *testing.T) {
	t.Parallel()
	db := openSQL(t, testdb.Fresh(t, false))
	p, err := migrations.NewProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	var def string
	if err := db.QueryRow(`SELECT indexdef FROM pg_indexes WHERE indexname = 'idx_registrations_manage_token'`).Scan(&def); err != nil {
		t.Fatalf("idx_registrations_manage_token missing: %v", err)
	}
	if !strings.Contains(def, "UNIQUE") || !strings.Contains(def, "manage_token_hash <> ''") {
		t.Fatalf("index is not the partial unique one: %s", def)
	}
}

func TestChildcareColumnDefaultsAndChecks(t *testing.T) {
	t.Parallel()
	db := openSQL(t, testdb.Fresh(t, true))
	var err error
	for _, q := range []string{
		`INSERT INTO users (id,email,name) VALUES (1,'parent@example.com','Parent')`,
		`INSERT INTO events (id,title,starts_local,duration_minutes,tzid,rrule,location,description,visibility,created_by) VALUES (1,'Family Night','2026-12-01T19:00',60,'UTC','','','','public',1)`,
		`INSERT INTO event_occurrences (id,event_id,original_date,starts_at,ends_at,status) VALUES (1,1,'2026-12-01','2026-12-01 19:00+00','2026-12-01 20:00+00','scheduled')`,
		`INSERT INTO registrations (occurrence_id,user_id,status) VALUES (1,1,'going')`,
	} {
		if _, err = db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var available bool
	var notes, ages, ageNote string
	var cap sql.NullInt64
	var count int
	if err = db.QueryRow(`SELECT childcare_available,childcare_notes,childcare_capacity,childcare_ages FROM events WHERE id=1`).Scan(&available, &notes, &cap, &ages); err != nil {
		t.Fatal(err)
	}
	if available || notes != "" || cap.Valid || ages != "" {
		t.Fatal("incorrect childcare defaults")
	}
	if err = db.QueryRow(`SELECT childcare_count,childcare_ages_note FROM registrations WHERE occurrence_id=1`).Scan(&count, &ageNote); err != nil || count != 0 || ageNote != "" {
		t.Fatalf("registration defaults: %d %q %v", count, ageNote, err)
	}
	for _, q := range []string{`UPDATE events SET childcare_capacity=-1 WHERE id=1`, `UPDATE registrations SET childcare_count=-1 WHERE occurrence_id=1`} {
		if _, err = db.Exec(q); err == nil {
			t.Fatalf("accepted negative: %s", q)
		}
	}
}

func TestHouseholdRegistrationIdentityConstraints(t *testing.T) {
	t.Parallel()
	db := openSQL(t, testdb.Fresh(t, true))
	exec := func(q string) {
		t.Helper()
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO users(id,email,status) VALUES(1,'holder@example.com','active'),(2,'other@example.com','active')`)
	exec(`INSERT INTO households(id,name,created_by) VALUES(1,'Household',1)`)
	exec(`INSERT INTO household_people(id,household_id,first_name,kind,created_by) VALUES(1,1,'Kit','child',1),(2,1,'Mary','adult',1)`)
	exec(`INSERT INTO events(id,title,description,starts_local,duration_minutes,tzid,rrule,location,visibility,created_by) VALUES(1,'Event','','2026-12-01T18:00',60,'UTC','','','public',1)`)
	exec(`INSERT INTO event_occurrences(id,event_id,original_date,starts_at,ends_at) VALUES(1,1,'2026-12-01','2026-12-01 18:00+00','2026-12-01 19:00+00')`)
	exec(`INSERT INTO registrations(occurrence_id,user_id,status) VALUES(1,1,'going')`)
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM registrations WHERE user_id=1 AND registered_by IS NULL AND household_person_id IS NULL AND NOT childcare_requested`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("account row defaults: %d %v", n, err)
	}
	exec(`INSERT INTO registrations(occurrence_id,household_person_id,registered_by,status,childcare_requested) VALUES(1,1,1,'going',true)`)
	for name, q := range map[string]string{
		"no identity":                `INSERT INTO registrations(occurrence_id,status) VALUES(1,'going')`,
		"both identities":            `INSERT INTO registrations(occurrence_id,user_id,household_person_id,registered_by,status) VALUES(1,2,2,1,'going')`,
		"no holder":                  `INSERT INTO registrations(occurrence_id,household_person_id,status) VALUES(1,2,'going')`,
		"holder on user":             `INSERT INTO registrations(occurrence_id,user_id,registered_by,status) VALUES(1,2,1,'going')`,
		"named guests":               `INSERT INTO registrations(occurrence_id,household_person_id,registered_by,status,guest_count) VALUES(1,2,1,'going',1)`,
		"named count":                `INSERT INTO registrations(occurrence_id,household_person_id,registered_by,status,childcare_count) VALUES(1,2,1,'going',1)`,
		"named ages":                 `INSERT INTO registrations(occurrence_id,household_person_id,registered_by,status,childcare_ages_note) VALUES(1,2,1,'going','3')`,
		"remote childcare":           `INSERT INTO registrations(occurrence_id,household_person_id,registered_by,status,attendance_mode,childcare_requested) VALUES(1,2,1,'going','remote',true)`,
		"user childcare flag":        `INSERT INTO registrations(occurrence_id,user_id,status,childcare_requested) VALUES(1,2,'going',true)`,
		"duplicate account":          `INSERT INTO registrations(occurrence_id,user_id,status) VALUES(1,1,'removed')`,
		"duplicate household person": `INSERT INTO registrations(occurrence_id,household_person_id,registered_by,status) VALUES(1,1,2,'cancelled')`,
		"person foreign key":         `INSERT INTO registrations(occurrence_id,household_person_id,registered_by,status) VALUES(1,9999,1,'going')`,
		"holder foreign key":         `INSERT INTO registrations(occurrence_id,household_person_id,registered_by,status) VALUES(1,2,9999,'going')`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := db.Exec(q); err == nil {
				t.Fatal("invalid registration accepted")
			}
		})
	}
	if err := db.QueryRow(`SELECT count(*) FROM registrations WHERE occurrence_id=1`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("user/person namespaces %d %v", n, err)
	}
}

func TestHouseholdConversionInvitationConstraints(t *testing.T) {
	t.Parallel()
	db := openSQL(t, testdb.Fresh(t, true))
	exec := func(q string) {
		t.Helper()
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO users(id,email,status) VALUES(1,'holder@example.com','active')`)
	exec(`INSERT INTO households(id,name,created_by) VALUES(1,'Family',1)`)
	exec(`INSERT INTO household_people(id,household_id,first_name,kind,created_by) VALUES(1,1,'Mary','adult',1)`)
	exec(`INSERT INTO invitations(id,email,invited_by,household_person_id,token_hash,expires_at) VALUES(1,'new@example.com',1,1,'first',now() - interval '1 hour')`)
	for name, q := range map[string]string{
		"expired still reserves person": `INSERT INTO invitations(email,invited_by,household_person_id,token_hash,expires_at) VALUES('other@example.com',1,1,'second',now())`,
		"no over limit":                 `UPDATE invitations SET over_limit=true WHERE id=1`,
		"member only":                   `UPDATE invitations SET role='leader' WHERE id=1`,
		"person foreign key":            `UPDATE invitations SET household_person_id=999 WHERE id=1`,
	} {
		if _, err := db.Exec(q); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	exec(`UPDATE invitations SET revoked_at=now() WHERE id=1`)
	exec(`INSERT INTO invitations(email,invited_by,household_person_id,token_hash,expires_at) VALUES('other@example.com',1,1,'second',now())`)
}

func TestTeenConsentAppendOnly(t *testing.T) {
	t.Parallel()
	db := openSQL(t, testdb.Fresh(t, true))
	exec := func(query string) {
		t.Helper()
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO users(id,email,status) VALUES(1,'admin@example.com','active'),(2,'teen@example.com','disabled')`)
	exec(`INSERT INTO teen_consents(teen_user_id,scope,status,granted_by,granter_role,evidence_text) VALUES(2,'account','granted',1,'admin','Paper consent')`)
	for name, query := range map[string]string{
		"update":         `UPDATE teen_consents SET status='revoked'`,
		"delete":         `DELETE FROM teen_consents`,
		"scope":          `INSERT INTO teen_consents(teen_user_id,scope,status,granted_by,granter_role) VALUES(2,'all','revoked',1,'admin')`,
		"admin evidence": `INSERT INTO teen_consents(teen_user_id,scope,status,granted_by,granter_role) VALUES(2,'sms','granted',1,'admin')`,
		"teen FK":        `INSERT INTO teen_consents(teen_user_id,scope,status,granted_by,granter_role) VALUES(999,'sms','revoked',1,'admin')`,
	} {
		if _, err := db.Exec(query); err == nil {
			t.Errorf("%s allowed", name)
		}
	}
	// The test helper clears tables between tests. A row trigger does not fire
	// for TRUNCATE, so history can be cleared for cleanup but never edited.
	exec(`TRUNCATE teen_consents`)
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM teen_consents`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("teen_consents after truncate: %d %v", n, err)
	}
}

// A request that names no purpose is a contact request (the column's default),
// so the original contact flow keeps working beside add requests.
func TestContactRequestPurposeDefaultsToContact(t *testing.T) {
	t.Parallel()
	db := openSQL(t, testdb.Fresh(t, true))
	var purpose string
	var occurrence sql.NullInt64
	err := db.QueryRow(`INSERT INTO contact_requests (requester_id, subject_user_id, context_type, context_id, fields, needs_admin, admin_decision, created_at, expires_at)
		VALUES (101, 102, 'group', 103, 'both', true, 'pending', '2026-10-24 12:00Z', '2026-11-07 12:00Z') RETURNING purpose, occurrence_id`).Scan(&purpose, &occurrence)
	if err != nil || purpose != "contact" || occurrence.Valid {
		t.Fatalf("purpose=%q occurrence=%v err=%v, want contact and no occurrence", purpose, occurrence, err)
	}
}

func TestContactAddRequestsConstraints(t *testing.T) {
	t.Parallel()
	a := newAccessDB(t)
	db := a.db
	exec := a.exec
	// The shared fixture has blocks and other grants; this walk wants a clean
	// organizer scope and a plain going answer for the recipient.
	exec(`DELETE FROM manager_add_blocks`)
	exec(`UPDATE registrations SET status='going' WHERE id IN (40,41)`)
	exec(`INSERT INTO events(id,group_id,title,description,starts_local,duration_minutes,tzid,rrule,location,visibility,created_by)
	 VALUES(21,10,'Other series','','2026-12-01T18:00',60,'UTC','','','invite_only',1)`)
	requestCreated := "2026-10-24 12:00Z"
	insertRequestFor := func(purpose string, requester, subject, event, occurrence int) int {
		t.Helper()
		var id int
		contextType, contextID, mode := "event", event, "in_person"
		var occ any = occurrence
		if purpose == "group_add" {
			contextType, contextID, mode, occ = "group", 10, "", nil
		}
		err := db.QueryRow(`INSERT INTO contact_requests(requester_id,subject_user_id,context_type,context_id,fields,purpose,occurrence_id,
		 add_attendance_mode,needs_admin,admin_decision,created_at,expires_at)
		 VALUES($7,$8,$1,$2,'',$3,$4,$5,true,'pending',$6::timestamptz,$6::timestamptz + interval '14 days') RETURNING id`, contextType, contextID, purpose, occ, mode, requestCreated, requester, subject).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	insertRequest := func(purpose string) int { return insertRequestFor(purpose, 1, 2, 20, 30) }
	for _, tt := range []struct{ name, mutation string }{
		{"unknown purpose", `purpose='other'`}, {"contact cannot be retargeted", `purpose='contact', fields='email'`},
		{"add has no contact fields", `fields='both'`}, {"exact occurrence required", `occurrence_id=NULL`},
		{"positive occurrence", `occurrence_id=0`}, {"event context only", `context_type='group'`},
		{"resolved mode required", `add_attendance_mode=''`}, {"invalid mode", `add_attendance_mode='phone'`},
		{"guest max", `add_guest_count=6`}, {"negative guests", `add_guest_count=-1`},
		{"care max", `add_childcare_count=21`}, {"negative care", `add_childcare_count=-1`},
		{"care note max", `add_childcare_ages_note=repeat('é',201)`},
		{"date cannot override group limit", `add_over_limit=true`},
		{"pending cannot claim completed target", `completed_target_id=40`},
		{"granted requires decisions and effect", `status='granted', completed_at='2026-10-25 12:00Z'`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			id := insertRequest("occurrence_add")
			if _, err := db.Exec("UPDATE contact_requests SET "+tt.mutation+" WHERE id=$1", id); err == nil {
				t.Fatal("malformed request accepted")
			}
		})
	}
	// Exercise insertion CHECKs too, independently of immutable-update guards.
	base := insertRequest("occurrence_add")
	for _, bad := range []string{
		`{"purpose":"other"}`, `{"purpose":"contact","fields":"email"}`, `{"fields":"both"}`,
		`{"occurrence_id":null}`, `{"occurrence_id":0}`, `{"context_type":"group"}`,
		`{"add_attendance_mode":""}`, `{"add_attendance_mode":"phone"}`, `{"add_guest_count":6}`,
		`{"add_guest_count":-1}`, `{"add_childcare_count":21}`, `{"add_childcare_count":-1}`,
		`{"add_over_limit":true}`, `{"completed_target_id":40}`,
	} {
		if _, err := db.Exec(`INSERT INTO contact_requests SELECT (jsonb_populate_record(NULL::contact_requests,
			to_jsonb(r) || $2::jsonb || jsonb_build_object('id',nextval('contact_requests_id_seq')))).*
			FROM contact_requests r WHERE r.id=$1`, base, bad); err == nil {
			t.Fatalf("malformed insertion accepted: %s", bad)
		}
	}
	// Resolved options are immutable even before first approval; forged input
	// and changing an already-reviewed capacity override must both fail.
	id := insertRequest("occurrence_add")
	if _, err := db.Exec(`UPDATE contact_requests SET add_over_capacity=true WHERE id=$1`, id); err == nil {
		t.Fatal("request option changed")
	}
	// Fully approved but incomplete is valid/retryable; still no grant/access.
	approveSQL := `UPDATE contact_requests SET subject_decision='approved',subject_decided_by=subject_user_id,subject_decided_at='2026-10-25 12:00Z',
	 admin_decision='approved',admin_decided_by=1,admin_decided_at='2026-10-25 12:00Z' WHERE id=$1`
	exec(approveSQL, id)
	if _, err := db.Exec(`INSERT INTO contact_grants(request_id,leader_id,subject_user_id,context_type,context_id,fields,granted_at)
	 VALUES($1,1,2,'event',20,'','2026-10-25 12:00Z')`, id); err == nil {
		t.Fatal("add created contact grant")
	}
	// The deferred completion trigger needs the exact effect on the registration
	// at commit: a granted request with no grant on the named target fails.
	if _, err := db.Exec(`UPDATE contact_requests SET status='granted',completed_at='2026-10-25 12:00Z',completed_target_id=40 WHERE id=$1`, id); err == nil {
		t.Fatal("successful add without occurrence effect accepted")
	}
	exec(`UPDATE contact_requests SET status='expired',completed_at=expires_at WHERE id=$1`, id)
	if _, err := db.Exec(`UPDATE contact_requests SET status='pending',completed_at=NULL WHERE id=$1`, id); err == nil {
		t.Fatal("expired add resurrected")
	}
	blocked := insertRequest("occurrence_add")
	exec(`UPDATE contact_requests SET status='ended',completed_at='2026-10-25 12:00Z',end_reason='blocked' WHERE id=$1`, blocked)
	group := insertRequest("group_add")
	exec(approveSQL, group)
	exec(`UPDATE contact_requests SET status='granted',completed_at='2026-10-25 12:00Z',completed_target_id=50 WHERE id=$1`, group)

	// Exact request/issuer/date/registration provenance is checked in addition
	// to the completed target, all in one deferred completion transaction.
	complete := func(request, target int, grantSQL string) error {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err = tx.Exec(approveSQL, request); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE contact_requests SET status='granted',completed_at='2026-10-25 12:00Z',completed_target_id=$2 WHERE id=$1`, request, target); err != nil {
			return err
		}
		if grantSQL != "" {
			if _, err = tx.Exec(grantSQL, request); err != nil {
				return err
			}
		}
		return tx.Commit()
	}
	grantAt := func(registration int, at string) string {
		return fmt.Sprintf(`UPDATE registrations SET access_origin='manager_add',access_issuer_id=1,access_request_id=$1,access_granted_at='%s' WHERE id=%d`, at, registration)
	}
	// Forged provenance: the request names a different subject, date, event,
	// issuer or completed target than the registration it is used on.
	forged := map[string]int{
		"subject": insertRequestFor("occurrence_add", 1, 3, 20, 30),
		"date":    insertRequestFor("occurrence_add", 1, 2, 20, 31),
		"event":   insertRequestFor("occurrence_add", 1, 2, 21, 30),
		"issuer":  insertRequestFor("occurrence_add", 3, 2, 20, 30),
	}
	for name, r := range forged {
		if err := complete(r, 40, grantAt(40, "2026-10-25 12:00Z")); err == nil {
			t.Fatalf("forged %s accepted", name)
		}
	}
	if err := complete(insertRequest("occurrence_add"), 41, grantAt(40, "2026-10-25 12:00Z")); err == nil {
		t.Fatal("forged completed target accepted")
	}
	// A request completed on another registration of the subject also fails at
	// commit when the effect is missing from the target it names.
	if err := complete(insertRequest("occurrence_add"), 40, ""); err == nil {
		t.Fatal("granted request without its effect committed")
	}
	if err := complete(insertRequestFor("occurrence_add", 1, 2, 21, 30), 40, ""); err == nil {
		t.Fatal("granted request for the wrong event committed")
	}
	request := insertRequest("occurrence_add")
	exec(`UPDATE users SET account_type='teen',birth_date='2010-01-01' WHERE id=2`)
	if _, err := db.Exec(`UPDATE registrations SET access_origin='manager_add',access_issuer_id=1,access_granted_at='2026-10-25 12:00Z' WHERE id=40`); err == nil {
		t.Fatal("direct teen manager add accepted")
	}
	if err := complete(request, 40, grantAt(40, "2026-10-25 12:00Z")); err != nil {
		t.Fatal(err)
	}
	// A granted request is terminal: its effect cannot be rewritten, and a later
	// end and re-grant on the registration cannot disturb it.
	if _, err := db.Exec(`UPDATE contact_requests SET completed_target_id=41, reason='x' WHERE id=$1`, request); err == nil {
		t.Fatal("granted request modified")
	}
	if _, err := db.Exec(`UPDATE contact_requests SET status='pending', completed_at=NULL, completed_target_id=NULL WHERE id=$1`, request); err == nil {
		t.Fatal("granted request reopened")
	}
	if err := complete(insertRequest("occurrence_add"), 40, grantAt(40, "2026-10-25 12:00:01Z")); err == nil {
		t.Fatal("second live subject/date grant accepted")
	}
	for _, mutation := range []string{
		`access_end_reason='removed'`, `access_ended_by=2`,
		`access_ended_at='2026-10-24 12:00Z',access_end_reason='declined',access_ended_by=2`,
		`access_ended_at='2026-10-26 12:00Z',access_end_reason='declined',access_ended_by=1`,
		`access_ended_at='2026-10-26 12:00Z',access_end_reason='unknown'`,
		`access_ended_at='2026-10-26 12:00Z',access_end_reason='account_disabled',access_ended_by=1`,
	} {
		if _, err := db.Exec("UPDATE registrations SET " + mutation + " WHERE id=40"); err == nil {
			t.Fatalf("malformed access end accepted: %s", mutation)
		}
	}
	if _, err := db.Exec(`INSERT INTO contact_grants(request_id,leader_id,subject_user_id,context_type,context_id,fields,granted_at)
	 VALUES($1,1,2,'event',20,'','2026-10-25 12:00Z')`, request); err == nil {
		t.Fatal("approved add created ContactGrant")
	}
	for _, mutation := range []string{`access_issuer_id=3`, `access_request_id=NULL`, `access_granted_at=access_granted_at+interval '1 second'`, `user_id=3`, `occurrence_id=31`} {
		if _, err := db.Exec("UPDATE registrations SET " + mutation + " WHERE id=40"); err == nil {
			t.Fatalf("immutable provenance changed: %s", mutation)
		}
	}
	// Other accepted dates survive an organizer block; new dates cannot be added.
	exec(`UPDATE users SET account_type='adult' WHERE id=2`)
	exec(`UPDATE registrations SET access_origin='manager_add',access_issuer_id=1,access_granted_at='2026-10-25 12:00Z' WHERE id=41`)
	staleAsk := insertRequest("occurrence_add")
	exec(`UPDATE registrations SET access_ended_at='2026-10-25 12:00Z',access_end_reason='declined',access_ended_by=2 WHERE id=40`)
	// Without snapshots the ended grant cannot even be replaced; with them the
	// replacement is judged as a repeat or a stale request, from the audit log.
	replace := func(at string) string {
		return fmt.Sprintf(`UPDATE registrations SET access_origin='manager_add',access_issuer_id=1,access_request_id=$1,access_granted_at='%s',
		 access_ended_at=NULL,access_end_reason='',access_ended_by=NULL WHERE id=40`, at)
	}
	if err := complete(staleAsk, 40, replace("2026-10-25 12:00:01Z")); err == nil {
		t.Fatal("replacement without recorded history accepted")
	}
	a.recordAdded(40, 2, 30, "manager_add", "2026-10-25 12:00Z", map[string]any{"issuerId": 1, "requestId": request})
	a.recordEnded(40, 2, 30, "manager_add", "2026-10-25 12:00Z", "2026-10-25 12:00Z", "declined", 2, map[string]any{"issuerId": 1, "requestId": request})
	if _, err := db.Exec(`UPDATE registrations SET access_origin='manager_add',access_issuer_id=1,access_granted_at='2026-10-26 12:00Z',
	 access_ended_at=NULL,access_end_reason='',access_ended_by=NULL WHERE id=40`); err == nil {
		t.Fatal("direct re-add after end accepted")
	}
	if err := complete(staleAsk, 40, replace("2026-10-25 12:00:01Z")); err == nil {
		t.Fatal("pending ask predating removal resurrected access")
	}
	requestCreated = "2026-10-25 12:00Z"
	freshAsk := insertRequest("occurrence_add")
	if err := complete(freshAsk, 40, replace("2026-10-25 12:00:01Z")); err != nil {
		t.Fatalf("genuinely new approved ask refused: %v", err)
	}
	// The earlier committed request is untouched by the later end and re-grant.
	var status string
	if err := db.QueryRow(`SELECT status FROM contact_requests WHERE id=$1`, request).Scan(&status); err != nil || status != "granted" {
		t.Fatalf("earlier request = %q, %v", status, err)
	}
	if _, err := db.Exec(`DELETE FROM registrations WHERE id=40`); err == nil {
		t.Fatal("live grant deleted")
	}
	exec(`INSERT INTO manager_add_blocks(subject_user_id,organizer_type,organizer_id) VALUES(2,'group',10)`)
	exec(`INSERT INTO registrations(id,occurrence_id,user_id,status,source) VALUES(49,34,2,'going','admin')`)
	if _, err := db.Exec(`UPDATE registrations SET access_origin='manager_add',access_issuer_id=1,access_granted_at='2026-10-27 12:00Z' WHERE id=49`); err == nil {
		t.Fatal("new date add bypassed organizer block")
	}
	exec(`UPDATE registrations SET access_ended_at='2026-10-26 12:00Z',access_end_reason='blocked',access_ended_by=2 WHERE id=40 AND access_ended_at IS NULL`)
	if _, err := db.Exec(`UPDATE registrations SET access_ended_at=NULL,access_end_reason='',access_ended_by=NULL WHERE id=40`); err == nil {
		t.Fatal("access tombstone resurrected")
	}
	if _, err := db.Exec(`UPDATE registrations SET access_origin='manager_add',access_issuer_id=1,access_granted_at='2026-10-27 12:00Z',
	 access_ended_at=NULL,access_end_reason='',access_ended_by=NULL WHERE id=40`); err == nil {
		t.Fatal("duplicate/readded ended date accepted")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM registrations WHERE access_origin<>'' AND access_ended_at IS NULL AND occurrence_id=31`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("block ended unrelated accepted date: %d %v", count, err)
	}
	// History survives actual hard deletion, independent of live-domain joins:
	// end what is live, record what the application records, then delete.
	exec(`UPDATE registrations SET access_ended_at='2026-10-26 12:00Z',access_end_reason='context_changed' WHERE id=41`)
	a.recordAdded(40, 2, 30, "manager_add", "2026-10-25 12:00:01Z", map[string]any{"issuerId": 1, "requestId": freshAsk})
	a.recordEnded(40, 2, 30, "manager_add", "2026-10-25 12:00:01Z", "2026-10-26 12:00Z", "blocked", 2, map[string]any{"issuerId": 1, "requestId": freshAsk})
	a.recordAdded(41, 2, 31, "manager_add", "2026-10-25 12:00Z", map[string]any{"issuerId": 1})
	a.recordEnded(41, 2, 31, "manager_add", "2026-10-25 12:00Z", "2026-10-26 12:00Z", "context_changed", 0, map[string]any{"issuerId": 1})
	snapshots := a.count(`SELECT count(*) FROM audit_logs WHERE action IN ('occurrence_access.added','occurrence_access.ended')`)
	for _, q := range []string{`DELETE FROM registrations`, `DELETE FROM event_occurrences`, `DELETE FROM events`, `DELETE FROM memberships`, `DELETE FROM groups`, `DELETE FROM users`} {
		exec(q)
	}
	if got := a.count(`SELECT count(*) FROM audit_logs WHERE action IN ('occurrence_access.added','occurrence_access.ended')`); got != snapshots || snapshots < 6 {
		t.Fatalf("access history lost: %d of %d", got, snapshots)
	}
	if err := db.QueryRow(`SELECT count(*) FROM manager_add_blocks`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("block history lost: %d %v", count, err)
	}
}

// TestMembershipAndRegistrationLookupPlans keeps the group/user-leading
// indexes useful for the live memberCount and promoteOnDisable query shapes.
func TestMembershipAndRegistrationLookupPlans(t *testing.T) {
	t.Parallel()
	db := openSQL(t, testdb.Fresh(t, true))
	for _, q := range []string{
		`INSERT INTO users (id, email) SELECT n, 'lookup-' || n || '@example.com' FROM generate_series(1, 2000) n`,
		`INSERT INTO groups (id, name, leader_id) SELECT n, 'Group ' || n, 1 FROM generate_series(1, 150) n`,
		`INSERT INTO memberships (user_id, group_id, status) SELECT ((g * 40 + n) % 2000) + 1, g, 'active' FROM generate_series(1, 150) g CROSS JOIN generate_series(1, 40) n`,
		`INSERT INTO events (id, group_id, title, starts_local, duration_minutes, tzid, rrule, location, created_by, visibility, description) VALUES (1, 1, 'Lookup', '2030-01-01T12:00', 60, 'UTC', '', '', 1, 'public', '')`,
		`INSERT INTO event_occurrences (id, event_id, original_date, starts_at, ends_at) SELECT n, 1, '2030-01-01'::date + n, now() + n * interval '1 day', now() + n * interval '1 day' + interval '1 hour' FROM generate_series(1, 64) n`,
		`INSERT INTO registrations (occurrence_id, user_id, registered_by, status) SELECT o, u, NULL, 'going' FROM generate_series(1, 64) o CROSS JOIN generate_series(1, 1950) u`,
		`ANALYZE users`, `ANALYZE memberships`, `ANALYZE registrations`, `ANALYZE event_occurrences`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	for _, tt := range []struct{ name, query, index, forbidden string }{
		{"member count", `SELECT count(*) FROM memberships
		 JOIN users ON users.id = memberships.user_id AND users.deleted_at IS NULL AND users.status <> 'disabled'
		 WHERE memberships.group_id = 1 AND memberships.status = 'active'`, "idx_memberships_group_id", "Seq Scan on memberships"},
		{"disable promotion", `SELECT DISTINCT registrations.occurrence_id FROM registrations
		 JOIN event_occurrences ON event_occurrences.id = registrations.occurrence_id
		 WHERE (registrations.user_id = 1 OR registrations.registered_by = 1)
		 AND registrations.status = 'going' AND event_occurrences.status = 'scheduled'
		 AND event_occurrences.starts_at > now() ORDER BY registrations.occurrence_id`, "idx_registrations_user_id", "Bitmap Index Scan on idx_registration ("},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rows, err := db.Query("EXPLAIN (ANALYZE, COSTS OFF) " + tt.query)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var plan strings.Builder
			for rows.Next() {
				var line string
				if err := rows.Scan(&line); err != nil {
					t.Fatal(err)
				}
				plan.WriteString(line + "\n")
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			t.Log(plan.String())
			if !strings.Contains(plan.String(), tt.index) || strings.Contains(plan.String(), tt.forbidden) {
				t.Fatalf("expected %s without %s:\n%s", tt.index, tt.forbidden, plan.String())
			}
		})
	}
	// Both lookup indexes are part of the baseline.
	for _, index := range []string{"idx_memberships_group_id", "idx_registrations_user_id"} {
		var present bool
		if err := db.QueryRow("SELECT to_regclass($1) IS NOT NULL", index).Scan(&present); err != nil || !present {
			t.Fatalf("index %s missing: present=%v err=%v", index, present, err)
		}
	}
}

// The push device table (00058), push as a consent and delivery channel, and
// the trigger of 00059 that ends a person's devices with their sessions.
func TestPushSubscriptionsConstraints(t *testing.T) {
	t.Parallel()
	db := openSQL(t, testdb.Fresh(t, true))
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	exec(`INSERT INTO users(id,email,status) VALUES(1,'pat@example.com','active'),(2,'sam@example.com','active')`)
	exec(`INSERT INTO push_subscriptions(user_id,endpoint,p256dh,auth,label) VALUES(1,'https://fcm.googleapis.com/a','k','a','Chrome on Linux')`)
	for name, query := range map[string]string{
		"duplicate endpoint": `INSERT INTO push_subscriptions(user_id,endpoint,p256dh,auth) VALUES(1,'https://fcm.googleapis.com/a','k','a')`,
		"long endpoint":      `INSERT INTO push_subscriptions(user_id,endpoint,p256dh,auth) VALUES(1,'https://x/` + strings.Repeat("a", 2048) + `','k','a')`,
		"long label":         `INSERT INTO push_subscriptions(user_id,endpoint,p256dh,auth,label) VALUES(1,'https://fcm.googleapis.com/b','k','a','` + strings.Repeat("a", 81) + `')`,
		"no user":            `INSERT INTO push_subscriptions(user_id,endpoint,p256dh,auth) VALUES(99,'https://fcm.googleapis.com/c','k','a')`,
		"other channel":      `INSERT INTO consents(user_id,channel,scope,status,source) VALUES(1,'fax','all','opted_in','web_form')`,
	} {
		if _, err := db.Exec(query); err == nil {
			t.Errorf("%s allowed", name)
		}
	}
	// Push is a consent channel and a delivery channel.
	exec(`INSERT INTO consents(user_id,channel,scope,status,source) VALUES(1,'push','all','opted_in','web_form')`)
	exec(`INSERT INTO notifications(user_id,kind,category,title,link) VALUES(1,'group.approved','groups','t','/')`)
	exec(`INSERT INTO notification_deliveries(notification_id,user_id,channel,status) SELECT id,1,'push','sent' FROM notifications`)

	// Disabling an account, or bumping its session version, ends its devices
	// and queues exactly one notice job; an ordinary update leaves them alone.
	exec(`INSERT INTO push_subscriptions(user_id,endpoint,p256dh,auth) VALUES(2,'https://fcm.googleapis.com/d','k','a'),(2,'https://fcm.googleapis.com/e','k','a')`)
	exec(`UPDATE users SET name = 'Sam' WHERE id = 2`)
	var devices, jobs int
	if err := db.QueryRow(`SELECT count(*) FROM push_subscriptions WHERE user_id = 2`).Scan(&devices); err != nil || devices != 2 {
		t.Fatalf("devices after an ordinary update = %d (%v), want 2", devices, err)
	}
	exec(`UPDATE users SET session_version = session_version + 1 WHERE id = 2`)
	if err := db.QueryRow(`SELECT count(*) FROM push_subscriptions WHERE user_id = 2`).Scan(&devices); err != nil || devices != 0 {
		t.Fatalf("devices after the sessions ended = %d (%v), want 0", devices, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM jobs WHERE kind = 'push.devices_off' AND payload->>'userId' = '2'`).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("push.devices_off jobs = %d (%v), want 1", jobs, err)
	}
	exec(`UPDATE users SET session_version = session_version + 1 WHERE id = 2`)
	if err := db.QueryRow(`SELECT count(*) FROM jobs WHERE kind = 'push.devices_off' AND payload->>'userId' = '2'`).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("no devices were left, yet jobs = %d (%v), want still 1", jobs, err)
	}

	// Deleting a user removes their devices.
	exec(`TRUNCATE consents`)
	exec(`DELETE FROM notifications`)
	exec(`DELETE FROM users WHERE id=1`)
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM push_subscriptions WHERE user_id = 1`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("devices after the user went: %d %v", n, err)
	}
}

// The baseline Down destroys the schema (R9 section 5.4). It exists for scratch
// databases and these tests, and it refuses while any user exists, so an
// absent-minded `fold migrate down-one` on a real install is an error.
const baselineDownRefusal = "baseline down would destroy data; refusing"

func publicTableCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_type = 'BASE TABLE'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestEmailAddressOptOutsMigration checks the dormant additive table and proves
// that its scratch-only Down cannot discard a retained choice.
func TestEmailAddressOptOutsMigration(t *testing.T) {
	t.Parallel()
	db := openSQL(t, testdb.Fresh(t, false))
	ctx := context.Background()
	p, err := migrations.NewProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	const version int64 = 5
	if _, err := p.UpTo(ctx, version-1); err != nil {
		t.Fatal(err)
	}
	var exists bool
	if err := db.QueryRow(`SELECT to_regclass('public.email_address_optouts') IS NOT NULL`).Scan(&exists); err != nil || exists {
		t.Fatalf("table before 00005: exists=%t, err=%v", exists, err)
	}
	if _, err := p.UpTo(ctx, version); err != nil {
		t.Fatal(err)
	}
	var columns string
	if err := db.QueryRow(`SELECT string_agg(column_name || ':' || data_type || ':' || is_nullable, ',' ORDER BY ordinal_position)
		FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'email_address_optouts'`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if want := "address_hash:text:NO,scope:text:NO,opted_out_at:timestamp with time zone:NO"; columns != want {
		t.Fatalf("columns = %q, want %q", columns, want)
	}
	var primaryKey, scopeCheck string
	if err := db.QueryRow(`SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conrelid = 'email_address_optouts'::regclass AND contype = 'p'`).Scan(&primaryKey); err != nil || primaryKey != "PRIMARY KEY (address_hash, scope)" {
		t.Fatalf("primary key = %q, err=%v", primaryKey, err)
	}
	var indexes int
	if err := db.QueryRow(`SELECT count(*) FROM pg_indexes WHERE schemaname = 'public' AND tablename = 'email_address_optouts'`).Scan(&indexes); err != nil || indexes != 1 {
		t.Fatalf("indexes = %d, err=%v, want only the primary key", indexes, err)
	}
	if err := db.QueryRow(`SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conrelid = 'email_address_optouts'::regclass AND conname = 'email_address_optouts_scope_check'`).Scan(&scopeCheck); err != nil {
		t.Fatal(err)
	}
	gotScopes := regexp.MustCompile(`category:[a-z]+`).FindAllString(scopeCheck, -1)
	var wantScopes []string
	for _, category := range notify.OptionalEmailCategories() {
		wantScopes = append(wantScopes, "category:"+category)
	}
	sort.Strings(gotScopes)
	sort.Strings(wantScopes)
	if strings.Join(gotScopes, ",") != strings.Join(wantScopes, ",") {
		t.Fatalf("scope CHECK = %v, want optional email scopes %v", gotScopes, wantScopes)
	}
	const insert = `INSERT INTO email_address_optouts (address_hash, scope, opted_out_at) VALUES ($1, $2, $3)`
	hash := strings.Repeat("a", 64)
	at := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name       string
		hash       any
		scope      any
		at         any
		code       string
		constraint string
	}{
		{"uppercase hash", strings.Repeat("A", 64), "category:admin", at, "23514", "email_address_optouts_hash_check"},
		{"short hash", strings.Repeat("a", 63), "category:admin", at, "23514", "email_address_optouts_hash_check"},
		{"nonhex hash", strings.Repeat("g", 64), "category:admin", at, "23514", "email_address_optouts_hash_check"},
		{"required scope", hash, "category:safety", at, "23514", "email_address_optouts_scope_check"},
		{"group scope", hash, "group:1", at, "23514", "email_address_optouts_scope_check"},
		{"unknown scope", hash, "category:unknown", at, "23514", "email_address_optouts_scope_check"},
		{"null hash", nil, "category:admin", at, "23502", ""},
		{"null scope", hash, nil, at, "23502", ""},
		{"null time", hash, "category:admin", nil, "23502", ""},
	} {
		_, err := db.Exec(insert, tc.hash, tc.scope, tc.at)
		var pgerr *pgconn.PgError
		if !errors.As(err, &pgerr) || pgerr.Code != tc.code || pgerr.ConstraintName != tc.constraint {
			t.Fatalf("%s: err=%v, want SQLSTATE %s, constraint %q", tc.name, err, tc.code, tc.constraint)
		}
	}
	// All four scopes can share one address; only the pair is unique.
	for _, scope := range wantScopes {
		if _, err := db.Exec(insert, hash, scope, at); err != nil {
			t.Fatalf("insert %s: %v", scope, err)
		}
	}
	_, err = db.Exec(insert, hash, "category:admin", at.Add(time.Hour))
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != "23505" || pgerr.ConstraintName != "email_address_optouts_pkey" {
		t.Fatalf("duplicate pair: %v, want primary key refusal", err)
	}
	// Leave exactly one retained record for the Down guard.
	if _, err := db.Exec(`DELETE FROM email_address_optouts WHERE scope <> 'category:admin'`); err != nil {
		t.Fatal(err)
	}
	before := appliedRows(t, db)
	const refusal = "email_address_optouts has retained opt-outs; restore the upgrade.sh dump instead"
	if _, err := p.Down(ctx); err == nil || !strings.Contains(err.Error(), refusal) {
		t.Fatalf("Down with a retained opt-out: %v, want %q", err, refusal)
	}
	if got := appliedRows(t, db); got != before {
		t.Fatalf("refused Down changed migration bookkeeping: %s, want %s", got, before)
	}
	var keptHash, keptScope string
	var keptAt time.Time
	if err := db.QueryRow(`SELECT address_hash, scope, opted_out_at FROM email_address_optouts`).Scan(&keptHash, &keptScope, &keptAt); err != nil || keptHash != hash || keptScope != "category:admin" || !keptAt.Equal(at) {
		t.Fatalf("refused Down changed retained choice: %q %q %v, err=%v", keptHash, keptScope, keptAt, err)
	}
	if _, err := db.Exec(`DELETE FROM email_address_optouts`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Down(ctx); err != nil {
		t.Fatalf("Down on an empty table: %v", err)
	}
	if v, err := p.GetDBVersion(ctx); err != nil || v != version-1 {
		t.Fatalf("version after Down = %d, err=%v, want %d", v, err, version-1)
	}
	if err := db.QueryRow(`SELECT to_regclass('public.email_address_optouts') IS NOT NULL`).Scan(&exists); err != nil || exists {
		t.Fatalf("table after Down: exists=%t, err=%v", exists, err)
	}
	if _, err := p.UpTo(ctx, version); err != nil {
		t.Fatalf("Up after Down: %v", err)
	}
	if v, err := p.GetDBVersion(ctx); err != nil || v != version {
		t.Fatalf("version after Up = %d, err=%v, want %d", v, err, version)
	}
	if _, err := db.Exec(insert, hash, "category:admin", at); err != nil {
		t.Fatalf("insert after round trip: %v", err)
	}
}

// TestAdminOperationAttemptsMigration checks the dormant receipt shape and
// proves Down discards only receipts and Up recreates the empty table.
func TestAdminOperationAttemptsMigration(t *testing.T) {
	t.Parallel()
	db := openSQL(t, testdb.Fresh(t, false))
	ctx := context.Background()
	p, err := migrations.NewProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	const version int64 = 6
	if _, err := p.UpTo(ctx, version-1); err != nil {
		t.Fatal(err)
	}
	var exists bool
	if err := db.QueryRow(`SELECT to_regclass('public.admin_operation_attempts') IS NOT NULL`).Scan(&exists); err != nil || exists {
		t.Fatalf("table before 00006: exists=%t, err=%v", exists, err)
	}
	if _, err := p.UpTo(ctx, version); err != nil {
		t.Fatal(err)
	}
	var columns, primaryKey, defaultValue string
	if err := db.QueryRow(`SELECT string_agg(column_name || ':' || data_type || ':' || is_nullable, ',' ORDER BY ordinal_position)
		FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'admin_operation_attempts'`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if want := "attempt_id:bytea:NO,created_at:timestamp with time zone:NO"; columns != want {
		t.Fatalf("columns = %q, want %q", columns, want)
	}
	if err := db.QueryRow(`SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conrelid = 'admin_operation_attempts'::regclass AND contype = 'p'`).Scan(&primaryKey); err != nil || primaryKey != "PRIMARY KEY (attempt_id)" {
		t.Fatalf("primary key = %q, err=%v", primaryKey, err)
	}
	if err := db.QueryRow(`SELECT column_default FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'admin_operation_attempts' AND column_name = 'created_at'`).Scan(&defaultValue); err != nil || defaultValue != "now()" {
		t.Fatalf("created_at default = %q, err=%v", defaultValue, err)
	}
	var indexes, foreignKeys int
	var indexDef string
	if err := db.QueryRow(`SELECT count(*) FROM pg_indexes WHERE schemaname = 'public' AND tablename = 'admin_operation_attempts'`).Scan(&indexes); err != nil || indexes != 2 {
		t.Fatalf("indexes = %d, err=%v, want primary key and created_at", indexes, err)
	}
	if err := db.QueryRow(`SELECT indexdef FROM pg_indexes WHERE schemaname = 'public'
		AND tablename = 'admin_operation_attempts' AND indexname = 'admin_operation_attempts_created_at'`).Scan(&indexDef); err != nil || !strings.HasPrefix(indexDef, "CREATE INDEX ") || !strings.HasSuffix(indexDef, "USING btree (created_at)") {
		t.Fatalf("created_at index = %q, err=%v", indexDef, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM pg_constraint WHERE conrelid = 'admin_operation_attempts'::regclass AND contype = 'f'`).Scan(&foreignKeys); err != nil || foreignKeys != 0 {
		t.Fatalf("foreign keys = %d, err=%v, want none", foreignKeys, err)
	}
	const insert = `INSERT INTO admin_operation_attempts (attempt_id) VALUES ($1)`
	for _, size := range []int{15, 17} {
		_, err := db.Exec(insert, make([]byte, size))
		var pgerr *pgconn.PgError
		if !errors.As(err, &pgerr) || pgerr.Code != "23514" {
			t.Fatalf("%d-byte nonce: %v, want CHECK refusal", size, err)
		}
	}
	nonce := make([]byte, 16)
	var at time.Time
	if err := db.QueryRow(insert+` RETURNING created_at`, nonce).Scan(&at); err != nil || at.IsZero() {
		t.Fatalf("16-byte nonce with default time: at=%v, err=%v", at, err)
	}
	for _, tc := range []struct {
		name string
		q    string
		arg  any
		code string
	}{
		{"duplicate nonce", insert, nonce, "23505"},
		{"null nonce", insert, nil, "23502"},
		{"null time", `INSERT INTO admin_operation_attempts (attempt_id, created_at) VALUES ($1, NULL)`, make([]byte, 16), "23502"},
	} {
		_, err := db.Exec(tc.q, tc.arg)
		var pgerr *pgconn.PgError
		if !errors.As(err, &pgerr) || pgerr.Code != tc.code {
			t.Fatalf("%s: %v, want SQLSTATE %s", tc.name, err, tc.code)
		}
	}
	// Unlike retained opt-outs, receipts may be dropped even when populated.
	if _, err := p.Down(ctx); err != nil {
		t.Fatalf("Down with a receipt: %v", err)
	}
	if v, err := p.GetDBVersion(ctx); err != nil || v != version-1 {
		t.Fatalf("version after Down = %d, err=%v, want %d", v, err, version-1)
	}
	if err := db.QueryRow(`SELECT to_regclass('public.admin_operation_attempts') IS NOT NULL`).Scan(&exists); err != nil || exists {
		t.Fatalf("table after Down: exists=%t, err=%v", exists, err)
	}
	if _, err := p.UpTo(ctx, version); err != nil {
		t.Fatalf("Up after Down: %v", err)
	}
	var receipts int
	if err := db.QueryRow(`SELECT count(*) FROM admin_operation_attempts`).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("receipts after round trip = %d, err=%v, want none", receipts, err)
	}
	if _, err := db.Exec(insert, nonce); err != nil {
		t.Fatalf("insert after round trip: %v", err)
	}
}

func TestBaselineDownRefusesWhileAUserExists(t *testing.T) {
	t.Parallel()
	gdb, err := testdb.Open(testdb.Fresh(t, true))
	if err != nil {
		t.Fatal(err)
	}
	db, err := gdb.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	p, err := migrations.NewProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, email) VALUES (1, 'down-guard@example.com')`); err != nil {
		t.Fatal(err)
	}
	// Every migration after the baseline rolls back with a user present; only
	// the baseline's own Down refuses.
	if _, err := p.DownTo(ctx, 1); err != nil {
		t.Fatalf("DownTo(1) with a user row: %v", err)
	}
	tables := publicTableCount(t, db)
	for name, down := range map[string]func() error{
		"DownTo(0)": func() error { _, err := p.DownTo(ctx, 0); return err },
		"Down":      func() error { _, err := p.Down(ctx); return err },
	} {
		if err := down(); err == nil || !strings.Contains(err.Error(), baselineDownRefusal) {
			t.Fatalf("%s with a user row: %v, want %q", name, err, baselineDownRefusal)
		}
	}
	var email string
	if err := db.QueryRow(`SELECT email FROM users WHERE id = 1`).Scan(&email); err != nil || email != "down-guard@example.com" {
		t.Fatalf("the refused Down changed the user row: %q %v", email, err)
	}
	if version, err := p.GetDBVersion(ctx); err != nil || version != 1 {
		t.Fatalf("version after the refusal = %d, %v, want 1", version, err)
	}
	if got := publicTableCount(t, db); got != tables {
		t.Fatalf("tables after the refusal = %d, want %d", got, tables)
	}
	// Back at the latest version the models match the schema again. (At
	// version 1 they do not: the later migrations are rolled back.)
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up after the refusal: %v", err)
	}
	if version, err := p.GetDBVersion(ctx); err != nil || version != latestVersion(t) {
		t.Fatalf("version after up = %d, %v, want %d", version, err, latestVersion(t))
	}
	if changes, err := Drift(gdb, models.All()...); err != nil || len(changes) > 0 {
		t.Fatalf("the refused Down left the schema changed: %v %v", changes, err)
	}
}

func TestBaselineDownAndUpOnAnEmptyDatabase(t *testing.T) {
	t.Parallel()
	gdb, err := testdb.Open(testdb.Fresh(t, true))
	if err != nil {
		t.Fatal(err)
	}
	db, err := gdb.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	p, err := migrations.NewProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	tables := publicTableCount(t, db)
	if _, err := p.DownTo(ctx, 0); err != nil {
		t.Fatalf("down on an empty database: %v", err)
	}
	if version, err := p.GetDBVersion(ctx); err != nil || version != 0 {
		t.Fatalf("version after down = %d, %v, want 0", version, err)
	}
	var left int
	if err := db.QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name <> 'goose_db_version'`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("tables left after down: %d %v", left, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname = 'public'`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("functions left after down: %d %v", left, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM pg_extension WHERE extname IN ('citext', 'btree_gist')`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("extensions left after down: %d %v", left, err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up after down: %v", err)
	}
	if pending, err := p.HasPending(ctx); err != nil || pending {
		t.Fatalf("pending after up: %v %v", pending, err)
	}
	if version, err := p.GetDBVersion(ctx); err != nil || version != latestVersion(t) {
		t.Fatalf("version after up = %d, %v, want %d", version, err, latestVersion(t))
	}
	if got := publicTableCount(t, db); got != tables {
		t.Fatalf("tables after up = %d, want %d", got, tables)
	}
	if changes, err := Drift(gdb, models.All()...); err != nil || len(changes) > 0 {
		t.Fatalf("the restored schema drifts from the models: %v %v", changes, err)
	}
}

var migrationFileName = regexp.MustCompile(`^([0-9]{5})_[a-z0-9_]+\.sql$`)

// migrationFileShapeProblems checks the migration files in fsys against the
// rules that keep the chain in order (PLAN 14.1, the AGENTS migration rule and
// DEC-138), and returns the highest version with every problem found. It reads
// only fsys. Non-.sql entries are ignored, the way the embed pattern `*.sql`
// (migrations.go) ignores them.
//
// Rules: 00001_baseline.sql exists and is the lowest version; every other .sql
// is named NNNNN_lower_snake.sql; versions are unique and consecutive from
// 00002 (no gap, no repeated number); each file starts with "-- +goose Up" and
// contains "-- +goose Down".
func migrationFileShapeProblems(fsys fs.FS) (latest int64, problems []string) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return 0, []string{"cannot read the migrations directory: " + err.Error()}
	}
	byVersion := map[int64][]string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		m := migrationFileName.FindStringSubmatch(name)
		if m == nil {
			problems = append(problems, fmt.Sprintf("%s does not match NNNNN_lower_snake_name.sql", name))
		} else {
			v, _ := strconv.ParseInt(m[1], 10, 64)
			byVersion[v] = append(byVersion[v], name)
		}
		b, err := fs.ReadFile(fsys, name)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s cannot be read: %v", name, err))
			continue
		}
		if !strings.HasPrefix(string(b), "-- +goose Up") {
			problems = append(problems, fmt.Sprintf("%s does not start with \"-- +goose Up\"", name))
		}
		if !strings.Contains(string(b), "-- +goose Down") {
			problems = append(problems, fmt.Sprintf("%s has no \"-- +goose Down\"", name))
		}
	}
	versions := make([]int64, 0, len(byVersion))
	for v := range byVersion {
		versions = append(versions, v)
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] < versions[j] })
	if names := byVersion[1]; len(names) == 0 {
		problems = append(problems, "00001_baseline.sql is missing")
	} else if len(names) != 1 || names[0] != "00001_baseline.sql" {
		problems = append(problems, fmt.Sprintf("version 00001 must be 00001_baseline.sql, found %v", names))
	}
	next := int64(2)
	for _, v := range versions {
		latest = v
		if names := byVersion[v]; len(names) > 1 {
			problems = append(problems, fmt.Sprintf("duplicate version %05d: %s", v, strings.Join(names, ", ")))
		}
		switch {
		case v < 1:
			problems = append(problems, fmt.Sprintf("%s has a version lower than the baseline", byVersion[v][0]))
		case v >= 2 && v > next:
			problems = append(problems, fmt.Sprintf("gap: version %05d is missing before %05d", next, v))
			next = v + 1
		case v >= 2:
			next = v + 1
		}
	}
	return latest, problems
}

// latestVersion is the highest migration version in the embedded files, the
// version a fully migrated database reports. TestBaselineFileShape fails on a
// tree whose files are out of order, so this need not.
func latestVersion(t testing.TB) int64 {
	t.Helper()
	latest, _ := migrationFileShapeProblems(migrations.Files)
	if latest < 1 {
		t.Fatal("no migrations found in migrations.Files")
	}
	return latest
}

// TestMigrationFileShapeProblems covers the rules in
// migrationFileShapeProblems with in-memory trees: what passes, and one
// fixture for each rejection.
func TestMigrationFileShapeProblems(t *testing.T) {
	t.Parallel()
	file := func(body string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(body)} }
	const ok = "-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 2;\n"
	for _, tc := range []struct {
		name       string
		fsys       fstest.MapFS
		wantLatest int64
	}{
		{"baseline only", fstest.MapFS{"00001_baseline.sql": file(ok)}, 1},
		{"baseline and 00002", fstest.MapFS{"00001_baseline.sql": file(ok), "00002_x.sql": file(ok)}, 2},
		{"three in order", fstest.MapFS{"00001_baseline.sql": file(ok), "00002_x.sql": file(ok), "00003_y_2.sql": file(ok)}, 3},
		{"non-sql entries are ignored", fstest.MapFS{"00001_baseline.sql": file(ok), "README.md": file("notes"), "00009_x.txt": file("x"), "sub/00005_x.sql": file("x")}, 1},
	} {
		latest, problems := migrationFileShapeProblems(tc.fsys)
		if len(problems) != 0 || latest != tc.wantLatest {
			t.Errorf("%s: latest=%d problems=%q, want latest=%d and none", tc.name, latest, problems, tc.wantLatest)
		}
	}
	for _, tc := range []struct {
		name string
		fsys fstest.MapFS
		want string
	}{
		{"no baseline", fstest.MapFS{"00002_x.sql": file(ok)}, "00001_baseline.sql is missing"},
		{"empty", fstest.MapFS{}, "00001_baseline.sql is missing"},
		{"another name for 00001", fstest.MapFS{"00001_start.sql": file(ok)}, "must be 00001_baseline.sql"},
		{"a gap", fstest.MapFS{"00001_baseline.sql": file(ok), "00003_x.sql": file(ok)}, "gap: version 00002 is missing before 00003"},
		{"a gap in the middle", fstest.MapFS{"00001_baseline.sql": file(ok), "00002_x.sql": file(ok), "00004_y.sql": file(ok)}, "gap: version 00003 is missing before 00004"},
		{"a duplicate number", fstest.MapFS{"00001_baseline.sql": file(ok), "00002_x.sql": file(ok), "00002_y.sql": file(ok)}, "duplicate version 00002"},
		{"a bad name", fstest.MapFS{"00001_baseline.sql": file(ok), "00002_Bad-Name.sql": file(ok)}, "00002_Bad-Name.sql does not match"},
		{"a short number", fstest.MapFS{"00001_baseline.sql": file(ok), "2_x.sql": file(ok)}, "2_x.sql does not match"},
		{"a version below the baseline", fstest.MapFS{"00000_x.sql": file(ok), "00001_baseline.sql": file(ok)}, "lower than the baseline"},
		{"no Down", fstest.MapFS{"00001_baseline.sql": file(ok), "00002_x.sql": file("-- +goose Up\nSELECT 1;\n")}, "00002_x.sql has no \"-- +goose Down\""},
		{"Up is not first", fstest.MapFS{"00001_baseline.sql": file(ok), "00002_x.sql": file("SELECT 1;\n-- +goose Up\n-- +goose Down\n")}, "00002_x.sql does not start with \"-- +goose Up\""},
		{"a leading blank line", fstest.MapFS{"00001_baseline.sql": file("\n" + ok)}, "00001_baseline.sql does not start with"},
	} {
		_, problems := migrationFileShapeProblems(tc.fsys)
		if !strings.Contains(strings.Join(problems, "\n"), tc.want) {
			t.Errorf("%s: problems = %q, want one containing %q", tc.name, problems, tc.want)
		}
	}
}

// TestDuplicateVersionNeverBoots pins the fail-closed half of DEC-146: when
// two pull requests merge the same next number, goose refuses to build a
// provider over that tree, so `fold serve`, `fold migrate` and the release
// smoke, which all go through migrations.NewProvider (the same
// goose.NewProvider call over migrations.Files), exit non-zero before touching
// a database. The same tree with the later file renumbered builds. No
// database is contacted.
func TestDuplicateVersionNeverBoots(t *testing.T) {
	t.Parallel()
	db, err := sql.Open("pgx", "postgres://unused@127.0.0.1:1/unused")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	file := func(body string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(body)} }
	const ok = "-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 2;\n"
	raced := fstest.MapFS{
		"00001_baseline.sql": file(ok),
		"00002_alpha.sql":    file(ok),
		"00002_beta.sql":     file(ok),
	}
	if _, err := goose.NewProvider(goose.DialectPostgres, db, raced); err == nil || !strings.Contains(err.Error(), "duplicate migration version 2") {
		t.Fatalf("provider over a duplicate version: err = %v, want goose's duplicate version error", err)
	}
	renumbered := fstest.MapFS{
		"00001_baseline.sql": file(ok),
		"00002_alpha.sql":    file(ok),
		"00003_beta.sql":     file(ok),
	}
	if _, problems := migrationFileShapeProblems(renumbered); len(problems) > 0 {
		t.Fatalf("the renumbered tree is out of shape: %q", problems)
	}
	if _, err := goose.NewProvider(goose.DialectPostgres, db, renumbered); err != nil {
		t.Fatalf("provider over the renumbered tree: %v", err)
	}
}

// TestBaselineFileShape pins what the generator guarantees about the baseline
// file (R9 section 5.2 and 5.4) and the shape of the whole chain (PLAN 14.1,
// the AGENTS migration rule, DEC-138): the baseline is the lowest version and
// later migrations are consecutive new files, each with an Up and a Down. The
// baseline-content checks below read 00001_baseline.sql only.
func TestBaselineFileShape(t *testing.T) {
	t.Parallel()
	if _, problems := migrationFileShapeProblems(migrations.Files); len(problems) > 0 {
		t.Fatalf("the migration files are out of shape:\n%s", strings.Join(problems, "\n"))
	}
	b, err := migrations.Files.ReadFile("00001_baseline.sql")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	up, down := strings.Index(src, "-- +goose Up"), strings.Index(src, "-- +goose Down")
	if up != 0 || down < 0 || down < up {
		t.Fatalf("goose Up must open the file and Down must follow it: up=%d down=%d", up, down)
	}
	upPart, downPart := src[:down], src[down:]
	for _, want := range []string{"DEC-138", "CREATE EXTENSION IF NOT EXISTS citext", "CREATE EXTENSION IF NOT EXISTS btree_gist"} {
		if !strings.Contains(upPart, want) {
			t.Errorf("the Up section lacks %q", want)
		}
	}
	if strings.Index(upPart, "citext") > strings.Index(upPart, "btree_gist") {
		t.Error("the extensions must be created citext first, then btree_gist")
	}
	for _, forbidden := range []string{"goose_db_version", "COMMENT ON EXTENSION", "CONCURRENTLY", "-- +goose NO TRANSACTION", "DROP SCHEMA"} {
		if strings.Contains(src, forbidden) {
			t.Errorf("the baseline contains %q", forbidden)
		}
	}
	// Every function body is wrapped for goose, plus the Down guard.
	functions := strings.Count(upPart, "CREATE FUNCTION")
	if begins, ends := strings.Count(upPart, "-- +goose StatementBegin"), strings.Count(upPart, "-- +goose StatementEnd"); functions == 0 || begins != functions || ends != functions {
		t.Errorf("functions=%d StatementBegin=%d StatementEnd=%d in Up", functions, begins, ends)
	}
	if !strings.Contains(downPart, baselineDownRefusal) || !strings.Contains(downPart, "DROP TABLE public.users;") {
		t.Error("the Down section lacks its guard or its explicit drops")
	}
	if strings.Index(downPart, baselineDownRefusal) > strings.Index(downPart, "DROP TABLE") {
		t.Error("the guard must come before the first drop")
	}
	if strings.Contains(downPart, "CASCADE") {
		t.Error("the Down section must list drops explicitly, never CASCADE")
	}
}

// The 00002 expand tests (M7.4 R4-S2). The migration adds suppressions.address_hash
// beside the plaintext address, for the previous release to keep working.

// hasSchemaObject runs a one-row EXISTS query and returns its answer.
func hasSchemaObject(t *testing.T, db *sql.DB, query string, args ...any) bool {
	t.Helper()
	var ok bool
	if err := db.QueryRow(`SELECT EXISTS (`+query+`)`, args...).Scan(&ok); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return ok
}

// suppressionHashObjects reports whether the 00002 column, CHECK and unique
// index exist, and whether the baseline's own CHECK and index are still there.
func suppressionHashObjects(t *testing.T, db *sql.DB) (column, check, index, oldCheck, oldIndex bool) {
	t.Helper()
	column = hasSchemaObject(t, db, `SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'suppressions' AND column_name = 'address_hash'`)
	check = hasSchemaObject(t, db, `SELECT 1 FROM pg_constraint WHERE conrelid = 'public.suppressions'::regclass AND conname = 'suppressions_address_hash_check'`)
	index = hasSchemaObject(t, db, `SELECT 1 FROM pg_indexes WHERE schemaname = 'public' AND tablename = 'suppressions' AND indexname = 'idx_suppressions_open_hash'`)
	oldCheck = hasSchemaObject(t, db, `SELECT 1 FROM pg_constraint WHERE conrelid = 'public.suppressions'::regclass AND conname = 'suppressions_address_check'`)
	oldIndex = hasSchemaObject(t, db, `SELECT 1 FROM pg_indexes WHERE schemaname = 'public' AND tablename = 'suppressions' AND indexname = 'idx_suppressions_open'`)
	return
}

func pgErrorCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// TestSuppressionAddressHashExpand runs 00002 over rows written by the
// baseline, then checks the column, the CHECK that ties the hash to the
// address, the unique index and the way down.
func TestSuppressionAddressHashExpand(t *testing.T) {
	t.Parallel()
	name := testdb.Fresh(t, false)
	gdb, err := testdb.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	db, err := gdb.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	p, err := migrations.NewProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 1); err != nil {
		t.Fatalf("up to the baseline: %v", err)
	}
	if column, _, _, oldCheck, oldIndex := suppressionHashObjects(t, db); column || !oldCheck || !oldIndex {
		t.Fatalf("at version 1: address_hash=%v suppressions_address_check=%v idx_suppressions_open=%v", column, oldCheck, oldIndex)
	}

	// Rows the baseline's CHECK accepts: btrim trims spaces only, so a
	// non-breaking space and tabs are part of the stored address.
	const pat = "pat@example.com"
	seed := func(address, reason string, cleared bool) {
		t.Helper()
		q := `INSERT INTO suppressions (channel, address, reason) VALUES ('email', $1, $2)`
		if cleared {
			q = `INSERT INTO suppressions (channel, address, reason, cleared_at) VALUES ('email', $1, $2, now())`
		}
		if _, err := db.Exec(q, address, reason); err != nil {
			t.Fatalf("seed %q: %v", address, err)
		}
	}
	for _, reason := range []string{"hard_bounce", "complaint"} {
		seed(pat, reason, false)
		seed(pat, reason, true)
	}
	seed("åsa@example.com", "hard_bounce", false)
	nbsp, tabs := pat+" ", pat+"\t\t\t"
	seed(nbsp, "hard_bounce", false)
	seed(tabs, "hard_bounce", false)
	if _, err := db.Exec(`INSERT INTO suppressions (channel, address, reason, cleared_at)
		SELECT 'email', 'u' || g || '@example.com', CASE WHEN g % 2 = 0 THEN 'hard_bounce' ELSE 'complaint' END, CASE WHEN g % 10 <> 0 THEN now() END
		FROM generate_series(1, 1000) g`); err != nil {
		t.Fatal(err)
	}

	if _, err := p.UpTo(ctx, 2); err != nil {
		t.Fatalf("migrate 00002 over existing rows: %v", err)
	}
	column, check, index, oldCheck, oldIndex := suppressionHashObjects(t, db)
	if !column || !check || !index || !oldCheck || !oldIndex {
		t.Fatalf("at version 2: address_hash=%v hash check=%v hash index=%v old check=%v old index=%v, want all present", column, check, index, oldCheck, oldIndex)
	}

	rows, err := db.Query(`SELECT address, address_hash FROM suppressions`)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for rows.Next() {
		var address string
		var hash *string
		if err := rows.Scan(&address, &hash); err != nil {
			t.Fatal(err)
		}
		n++
		if hash == nil || *hash != mail.StoredAddressHash(address) {
			t.Errorf("address %q has hash %v, want %s", address, hash, mail.StoredAddressHash(address))
		}
		switch address {
		case pat:
			if hash != nil && *hash != mail.AddressHash(" Pat@Example.COM ") {
				t.Errorf("%q hash %s differs from the hash of the normalized input", address, *hash)
			}
		case nbsp, tabs:
			// Their own bytes, not the trimmed address's.
			if hash != nil && *hash == mail.AddressHash(pat) {
				t.Errorf("%q took the hash of %q", address, pat)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if n != 7+1000 {
		t.Fatalf("rows after the migration = %d, want %d", n, 7+1000)
	}

	// The CHECK ties the hash to the address, and accepts NULL (rows the
	// previous release writes).
	var id int64
	if err := db.QueryRow(`SELECT id FROM suppressions WHERE address = $1 AND cleared_at IS NULL AND reason = 'hard_bounce'`, pat).Scan(&id); err != nil {
		t.Fatal(err)
	}
	good := mail.StoredAddressHash(pat)
	for name, bad := range map[string]string{
		"uppercase":  strings.ToUpper(good),
		"63 chars":   good[:63],
		"mismatched": mail.StoredAddressHash("other@example.com"),
	} {
		_, err := db.Exec(`UPDATE suppressions SET address_hash = $1 WHERE id = $2`, bad, id)
		if code := pgErrorCode(err); code != "23514" {
			t.Errorf("%s hash: error %v (code %q), want a check violation", name, err, code)
		}
	}
	if _, err := db.Exec(`UPDATE suppressions SET address_hash = NULL WHERE id = $1`, id); err != nil {
		t.Fatalf("NULL hash refused: %v", err)
	}
	if _, err := db.Exec(`UPDATE suppressions SET address_hash = $1 WHERE id = $2`, good, id); err != nil {
		t.Fatalf("restoring the hash: %v", err)
	}

	// A second open (channel, hash, reason) row is refused by the new index.
	// idx_suppressions_open would refuse the same row first, so the test drops
	// it inside a transaction it rolls back.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DROP INDEX idx_suppressions_open`); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(`INSERT INTO suppressions (channel, address, address_hash, reason) VALUES ('email', $1, $2, 'hard_bounce')`, pat, good)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" || pgErr.ConstraintName != "idx_suppressions_open_hash" {
		t.Errorf("second open hash row: %v, want a unique violation of idx_suppressions_open_hash", err)
	}
	_ = tx.Rollback()
	// A cleared row with the same hash is fine.
	if _, err := db.Exec(`INSERT INTO suppressions (channel, address, address_hash, reason, cleared_at) VALUES ('email', $1, $2, 'hard_bounce', now())`, pat, good); err != nil {
		t.Errorf("a cleared row with an open row's hash: %v", err)
	}

	// Down: only what 00002 added goes.
	if _, err := p.DownTo(ctx, 1); err != nil {
		t.Fatalf("down to 1: %v", err)
	}
	if column, check, index, oldCheck, oldIndex := suppressionHashObjects(t, db); column || check || index || !oldCheck || !oldIndex {
		t.Fatalf("after down: address_hash=%v hash check=%v hash index=%v old check=%v old index=%v, want only the old two", column, check, index, oldCheck, oldIndex)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if column, check, index, oldCheck, oldIndex := suppressionHashObjects(t, db); !column || !check || !index || !oldCheck || !oldIndex {
		t.Fatalf("after up again: address_hash=%v hash check=%v hash index=%v old check=%v old index=%v, want all present", column, check, index, oldCheck, oldIndex)
	}
	var nulls int
	if err := db.QueryRow(`SELECT count(*) FROM suppressions WHERE address_hash IS NULL`).Scan(&nulls); err != nil || nulls != 0 {
		t.Fatalf("NULL hashes after up again = %d (%v), want 0", nulls, err)
	}
	if version, err := p.GetDBVersion(ctx); err != nil || version != latestVersion(t) {
		t.Fatalf("version = %d, %v, want %d", version, err, latestVersion(t))
	}
	if changes, err := Drift(gdb, models.All()...); err != nil || len(changes) > 0 {
		t.Fatalf("the models drift from the migrated schema: %v %v", changes, err)
	}
}

// TestSuppressionMatchingBasis checks the facts R4-S's matching proof rests on
// (inventory 7.4): the stored address bytes are the bytes SQL hashes, and the
// columns compare by bytes. The new hash arm in the readers matches the same
// rows the old SQL `=` matched only on this basis.
func TestSuppressionMatchingBasis(t *testing.T) {
	t.Parallel()
	db := openSQL(t, testdb.Fresh(t, true))
	const revisit = "R4-S's matching proof (inventory 7.4) assumes a UTF8 database with the libc provider and default column collations, so SQL and Go agree on stored address bytes. Revisit the proof, the migration CHECK and the readers before changing this."
	var encoding string
	if err := db.QueryRow(`SHOW server_encoding`).Scan(&encoding); err != nil || encoding != "UTF8" {
		t.Fatalf("server_encoding = %q (%v), want UTF8. %s", encoding, err, revisit)
	}
	var provider string
	if err := db.QueryRow(`SELECT datlocprovider::text FROM pg_database WHERE datname = current_database()`).Scan(&provider); err != nil || provider != "c" {
		t.Fatalf("datlocprovider = %q (%v), want c (libc, whose collations are deterministic). %s", provider, err, revisit)
	}
	for _, col := range []struct{ table, column string }{{"suppressions", "address"}, {"users", "email"}} {
		var oid int64
		var collname string
		err := db.QueryRow(`SELECT a.attcollation::int8, c.collname FROM pg_attribute a JOIN pg_collation c ON c.oid = a.attcollation
			WHERE a.attrelid = ('public.' || $1::text)::regclass AND a.attname = $2`, col.table, col.column).Scan(&oid, &collname)
		if err != nil || oid != 100 || collname != "default" {
			t.Fatalf("%s.%s collation = %d %q (%v), want the default collation (oid 100). %s", col.table, col.column, oid, collname, err, revisit)
		}
	}
	for _, s := range []string{"pat@example.com", "åsa@example.com", "pat@example.com "} {
		var got string
		if err := db.QueryRow(`SELECT encode(sha256(convert_to($1::text, 'UTF8')), 'hex')`, s).Scan(&got); err != nil || got != mail.StoredAddressHash(s) {
			t.Fatalf("SQL hash of %q = %q (%v), Go hash = %s. %s", s, got, err, mail.StoredAddressHash(s), revisit)
		}
	}
}

// TestSuppressionExpandWaitsAtMostFiveSecondsForTheLock holds a lock that
// conflicts with 00002's ALTER TABLE. The migration gives up after its 5s
// lock_timeout and applies nothing. This bounds the wait to get the lock, not
// the time the lock is held once it is taken.
func TestSuppressionExpandWaitsAtMostFiveSecondsForTheLock(t *testing.T) {
	t.Parallel()
	name := testdb.Fresh(t, false)
	db := openSQL(t, name)
	ctx := context.Background()
	p, err := migrations.NewProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 1); err != nil {
		t.Fatalf("up to the baseline: %v", err)
	}
	holder, err := openSQL(t, name).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback()
	if _, err := holder.Exec(`LOCK suppressions IN ACCESS SHARE MODE`); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	_, err = p.UpTo(ctx, 2)
	elapsed := time.Since(start)
	if code := pgErrorCode(err); code != "55P03" {
		t.Fatalf("UpTo(2) with the table locked: %v (code %q), want lock_not_available 55P03", err, code)
	}
	if elapsed < 5*time.Second || elapsed > 30*time.Second {
		t.Fatalf("UpTo(2) gave up after %s, want at least 5s and well under 30s", elapsed)
	}
	if version, err := p.GetDBVersion(ctx); err != nil || version != 1 {
		t.Fatalf("version after the timeout = %d, %v, want 1", version, err)
	}
	if column, _, _, _, _ := suppressionHashObjects(t, db); column {
		t.Fatal("address_hash exists after the timed-out migration")
	}

	if err := holder.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 2); err != nil {
		t.Fatalf("UpTo(2) after the lock was released: %v", err)
	}
	if column, _, _, _, _ := suppressionHashObjects(t, db); !column {
		t.Fatal("address_hash is missing after the migration")
	}
}

// TestSuppressionAddressHashExpandScale times 00002 over a large table. It is
// opt-in: set FOLD_SUPPRESSION_SCALE_ROWS to the number of rows (1 to
// 1,000,000). It asserts no timing; the PR records the numbers as
// measurements on that host, not bounds. It also times one call of
// mail.OpenSuppressionReasons with 500 inputs, half of them present, so the
// measured SQL is the SQL the readers run.
func TestSuppressionAddressHashExpandScale(t *testing.T) {
	raw := os.Getenv("FOLD_SUPPRESSION_SCALE_ROWS")
	if raw == "" {
		t.Skip("set FOLD_SUPPRESSION_SCALE_ROWS (1 to 1000000) to time migration 00002 on a large suppressions table")
	}
	rowsWanted, err := strconv.Atoi(raw)
	if err != nil || rowsWanted < 1 || rowsWanted > 1_000_000 {
		t.Fatalf("FOLD_SUPPRESSION_SCALE_ROWS = %q, want an integer from 1 to 1000000", raw)
	}
	dbName := testdb.Fresh(t, false)
	db := openSQL(t, dbName)
	ctx := context.Background()
	p, err := migrations.NewProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 1); err != nil {
		t.Fatalf("up to the baseline: %v", err)
	}
	// 90% of the rows are cleared, and every row has a distinct address.
	if _, err := db.Exec(`INSERT INTO suppressions (channel, address, reason, created_at, cleared_at)
		SELECT 'email', 'u' || g || '@example.com', CASE WHEN g % 2 = 0 THEN 'hard_bounce' ELSE 'complaint' END, now(), CASE WHEN g % 10 <> 0 THEN now() END
		FROM generate_series(1, $1::int) g`, rowsWanted); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := db.Exec(`ANALYZE suppressions`); err != nil {
		t.Fatal(err)
	}
	// The file is one transaction, so this is the whole span of the lock.
	start := time.Now()
	if _, err := p.UpTo(ctx, 2); err != nil {
		t.Fatalf("migrate 00002: %v", err)
	}
	elapsed := time.Since(start)
	var nulls, total int
	if err := db.QueryRow(`SELECT count(*) FILTER (WHERE address_hash IS NULL), count(*) FROM suppressions`).Scan(&nulls, &total); err != nil {
		t.Fatal(err)
	}
	if nulls != 0 || total != rowsWanted {
		t.Fatalf("after the migration: %d NULL hashes, %d rows, want 0 and %d", nulls, total, rowsWanted)
	}
	var version string
	if err := db.QueryRow(`SELECT version()`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	t.Logf("rows=%d migrate_00002=%s postgres=%q", rowsWanted, elapsed, version)

	// One lookup of 500 addresses through the real function: up to 250 open
	// rows (every tenth address is open) and the rest absent.
	gdb, err := testdb.Open(dbName)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := gdb.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	var inputs []string
	for g := 10; g <= rowsWanted && len(inputs) < 250; g += 10 {
		inputs = append(inputs, fmt.Sprintf("u%d@example.com", g))
	}
	present := len(inputs)
	for i := 0; len(inputs) < 500; i++ {
		inputs = append(inputs, fmt.Sprintf("absent%d@example.com", i))
	}
	start = time.Now()
	found, err := mail.OpenSuppressionReasons(gdb, inputs)
	lookup := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != present {
		t.Fatalf("lookup found %d addresses, want %d", len(found), present)
	}
	t.Logf("rows=%d open_suppression_lookup_500_inputs=%s present=%d", rowsWanted, lookup, present)
}

// The statements below are deliberate frozen copies of what the previous
// release (2aea60a, before R4-S) sent to the suppressions table. They are
// fixtures for the old binary, not stand-ins for current code: the point is to
// show that an old binary running against a version-2 database still works.

// oldSuppression is the previous release's models.Suppression: no AddressHash.
type oldSuppression struct {
	ID        uint `gorm:"primaryKey"`
	Channel   string
	Address   string
	Reason    string
	Provider  string
	Detail    string
	CreatedAt time.Time
	ClearedAt *time.Time
	ClearedBy *uint
}

func (oldSuppression) TableName() string { return "suppressions" }

const (
	// api_email_webhooks.go:159-160 at 2aea60a.
	oldInsertSuppressionSQL = `INSERT INTO suppressions (channel, address, reason, provider, detail) VALUES ('email', ?, ?, ?, ?)
			ON CONFLICT (channel, address, reason) WHERE cleared_at IS NULL DO NOTHING`
	// api_admin_suppressions.go:60-61 at 2aea60a.
	oldClearSuppressionSQL = `UPDATE suppressions SET cleared_at = ?, cleared_by = ?
			WHERE channel = 'email' AND address = ? AND cleared_at IS NULL RETURNING reason`
	// api_notifications.go:833-834 at 2aea60a, run with the account's email
	// supplied as a one-row relation named users.
	oldRecipientSuppressionSQL = `SELECT COALESCE((SELECT string_agg(s.reason, ',') FROM suppressions s
			WHERE s.channel = 'email' AND s.address = lower(btrim(users.email)) AND s.cleared_at IS NULL), '')
		FROM (SELECT CAST(? AS text) AS email) users`
	// The insert in api_email_webhooks.go as R4-S3 changed it. Unlike the
	// frozen copies above this one is tied to the production source below, so
	// the rows this test writes are the rows the webhook writes.
	newInsertSuppressionSQL = `INSERT INTO suppressions (channel, address, address_hash, reason, provider, detail) VALUES ('email', ?, ?, ?, ?, ?)
			ON CONFLICT (channel, address, reason) WHERE cleared_at IS NULL
			DO UPDATE SET address_hash = EXCLUDED.address_hash WHERE suppressions.address_hash IS NULL`
)

// TestSuppressionExpandKeepsThePreviousBinaryWorking proves the expand step
// (Route B, DEC-138) is safe for a rollback or a mixed window: every
// statement the previous binary sends recognizes every row the new code
// writes, the new code recognizes every row the old binary writes, and the
// previous goose runner treats a version-2 database as current.
func TestSuppressionExpandKeepsThePreviousBinaryWorking(t *testing.T) {
	t.Parallel()
	gdb, err := testdb.Open(testdb.Fresh(t, true))
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	ctx := context.Background()
	if latest := latestVersion(t); latest < 2 {
		t.Fatalf("latest migration = %d, want at least 2 (the suppression expand)", latest)
	}

	// The new insert is a copy, so tie it to the source it copies.
	src, err := os.ReadFile("../api_email_webhooks.go")
	if err != nil {
		t.Fatal(err)
	}
	collapse := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	if !strings.Contains(collapse(string(src)), collapse(newInsertSuppressionSQL)) {
		t.Fatal("api_email_webhooks.go no longer holds newInsertSuppressionSQL; update this fixture to the webhook's insert so the test keeps writing the rows the webhook writes")
	}

	const pat, asa = "pat@example.com", "åsa@example.com"
	newInsert := func(address, reason string) int64 {
		t.Helper()
		res := gdb.Exec(newInsertSuppressionSQL, address, mail.StoredAddressHash(address), reason, "postmark", "new detail")
		if res.Error != nil {
			t.Fatalf("new insert of %q: %v", address, res.Error)
		}
		return res.RowsAffected
	}
	oldInsert := func(address, reason string) int64 {
		t.Helper()
		res := gdb.Exec(oldInsertSuppressionSQL, address, reason, "legacy", "old detail")
		if res.Error != nil {
			t.Fatalf("old insert of %q: %v", address, res.Error)
		}
		return res.RowsAffected
	}
	// The old readers.
	oldSend := func(address string) []string { // suppress.go:55 at 2aea60a
		t.Helper()
		var reasons []string
		if err := gdb.Model(&oldSuppression{}).
			Where("channel = ? AND address = ? AND cleared_at IS NULL", "email", strings.ToLower(strings.TrimSpace(address))).
			Pluck("reason", &reasons).Error; err != nil {
			t.Fatal(err)
		}
		sort.Strings(reasons)
		return reasons
	}
	oldRecipient := func(email string) []string {
		t.Helper()
		var joined string
		if err := gdb.Raw(oldRecipientSuppressionSQL, email).Scan(&joined).Error; err != nil {
			t.Fatal(err)
		}
		if joined == "" {
			return nil
		}
		reasons := strings.Split(joined, ",")
		sort.Strings(reasons)
		return reasons
	}
	oldBatch := func(addresses ...string) map[string][]string { // messages_send.go:597, messages_audience.go:361
		t.Helper()
		var sups []oldSuppression
		if err := gdb.Where("channel = 'email' AND cleared_at IS NULL AND address IN ?", addresses).Find(&sups).Error; err != nil {
			t.Fatal(err)
		}
		out := map[string][]string{}
		for _, s := range sups {
			out[s.Address] = append(out[s.Address], s.Reason)
		}
		for _, reasons := range out {
			sort.Strings(reasons)
		}
		return out
	}
	oldClear := func(address string) []string {
		t.Helper()
		var reasons []string
		if err := gdb.Raw(oldClearSuppressionSQL, time.Now(), nil, address).Scan(&reasons).Error; err != nil {
			t.Fatal(err)
		}
		sort.Strings(reasons)
		return reasons
	}
	// The new reader.
	newLookup := func(address string) []string {
		t.Helper()
		found, err := mail.OpenSuppressionReasons(gdb, []string{address})
		if err != nil {
			t.Fatal(err)
		}
		return found[mail.NormalizeAddress(address)]
	}
	type stored struct {
		hash      *string
		provider  string
		detail    string
		createdAt time.Time
		clearedAt *time.Time
	}
	rowsFor := func(address, reason string, open bool) []stored {
		t.Helper()
		q := `SELECT address_hash, provider, detail, created_at, cleared_at FROM suppressions WHERE address = $1 AND reason = $2 AND cleared_at IS `
		if open {
			q += `NULL`
		} else {
			q += `NOT NULL`
		}
		rows, err := sqlDB.Query(q, address, reason)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []stored
		for rows.Next() {
			var s stored
			if err := rows.Scan(&s.hash, &s.provider, &s.detail, &s.createdAt, &s.clearedAt); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	same := func(label string, got, want []string) {
		t.Helper()
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s = %v, want %v", label, got, want)
		}
	}

	// 1. Rows the new webhook insert writes, retried and redelivered.
	for _, r := range []struct{ address, reason string }{{pat, "hard_bounce"}, {pat, "complaint"}, {asa, "hard_bounce"}} {
		if n := newInsert(r.address, r.reason); n != 1 {
			t.Fatalf("new insert of %q %s affected %d rows, want 1", r.address, r.reason, n)
		}
	}
	for i := 0; i < 2; i++ {
		for _, r := range []struct{ address, reason string }{{pat, "hard_bounce"}, {pat, "complaint"}, {asa, "hard_bounce"}} {
			if n := newInsert(r.address, r.reason); n != 0 {
				t.Fatalf("redelivery of %q %s affected %d rows, want 0 (the hash is already set)", r.address, r.reason, n)
			}
		}
	}
	var total int
	if err := sqlDB.QueryRow(`SELECT count(*) FROM suppressions`).Scan(&total); err != nil || total != 3 {
		t.Fatalf("rows after retries = %d (%v), want 3", total, err)
	}
	for _, r := range []struct{ address, reason string }{{pat, "hard_bounce"}, {pat, "complaint"}, {asa, "hard_bounce"}} {
		got := rowsFor(r.address, r.reason, true)
		if len(got) != 1 || got[0].hash == nil || *got[0].hash != mail.StoredAddressHash(r.address) {
			t.Fatalf("new-written row %q %s = %+v, want one row with its hash", r.address, r.reason, got)
		}
	}

	// 2. The old readers see them.
	same("old send lookup of pat", oldSend(" Pat@Example.com "), []string{"complaint", "hard_bounce"})
	same("old send lookup of åsa", oldSend(asa), []string{"hard_bounce"})
	same("old notify.deliver subquery for pat", oldRecipient("  PAT@example.com"), []string{"complaint", "hard_bounce"})
	batch := oldBatch(pat, asa, "nobody@example.com")
	if len(batch) != 2 {
		t.Errorf("old batch lookup = %v, want pat and åsa", batch)
	}
	same("old batch reasons for pat", batch[pat], []string{"complaint", "hard_bounce"})
	same("old batch reasons for åsa", batch[asa], []string{"hard_bounce"})

	// 3. The old model scans the new column set without error: GORM drops a
	// column the struct lacks.
	var old []oldSuppression
	if err := gdb.Find(&old).Error; err != nil || len(old) != 3 {
		t.Fatalf("old model Find = %d rows, %v, want 3 and no error", len(old), err)
	}
	var viaStar []oldSuppression
	if err := gdb.Raw(`SELECT * FROM suppressions`).Scan(&viaStar).Error; err != nil || len(viaStar) != 3 {
		t.Fatalf("SELECT * into the old model = %d rows, %v, want 3 and no error", len(viaStar), err)
	}

	// 4. The old insert meets a hashed open row: nothing added, hash intact.
	if n := oldInsert(pat, "hard_bounce"); n != 0 {
		t.Fatalf("old insert beside a hashed open row affected %d rows, want 0", n)
	}
	if got := rowsFor(pat, "hard_bounce", true); len(got) != 1 || got[0].hash == nil {
		t.Fatalf("after the old insert: %+v, want the one hashed row", got)
	}

	// 5. The old clear clears rows the new code wrote, and only open ones.
	const gone = "gone@example.com"
	newInsert(gone, "hard_bounce")
	newInsert(gone, "complaint")
	same("new lookup before the old clear", newLookup(gone), []string{"hard_bounce", "complaint"})
	same("old clear", oldClear(gone), []string{"complaint", "hard_bounce"})
	if len(newLookup(gone)) != 0 || len(oldSend(gone)) != 0 {
		t.Error("a cleared hashed row still blocks")
	}
	if cleared := rowsFor(gone, "hard_bounce", false); len(cleared) != 1 || cleared[0].hash == nil {
		t.Errorf("cleared row = %+v, want it kept with its hash", cleared)
	}
	if n := newInsert(gone, "hard_bounce"); n != 1 {
		t.Errorf("new insert beside a cleared row affected %d rows, want 1", n)
	}
	same("new lookup after a fresh report", newLookup(gone), []string{"hard_bounce"})

	// 6. An old-style insert leaves the hash NULL and passes the CHECK. The
	// new lookup finds it through the address arm.
	const legacy = "legacy@example.com"
	if n := oldInsert(legacy, "hard_bounce"); n != 1 {
		t.Fatalf("old insert of a new address affected %d rows, want 1", n)
	}
	if n := oldInsert(legacy, "hard_bounce"); n != 0 {
		t.Fatalf("old insert retried affected %d rows, want 0", n)
	}
	before := rowsFor(legacy, "hard_bounce", true)
	if len(before) != 1 || before[0].hash != nil {
		t.Fatalf("old-written row = %+v, want one row with a NULL hash", before)
	}
	same("new lookup of an old-written row", newLookup(" LEGACY@example.com "), []string{"hard_bounce"})

	// 7. A redelivery through the new insert fills the hash and nothing else.
	if n := newInsert(legacy, "hard_bounce"); n != 1 {
		t.Fatalf("redelivery over a NULL hash affected %d rows, want 1", n)
	}
	after := rowsFor(legacy, "hard_bounce", true)
	if len(after) != 1 || after[0].hash == nil || *after[0].hash != mail.StoredAddressHash(legacy) {
		t.Fatalf("after the redelivery: %+v, want the hash filled", after)
	}
	if after[0].provider != "legacy" || after[0].detail != "old detail" || !after[0].createdAt.Equal(before[0].createdAt) || after[0].clearedAt != nil {
		t.Errorf("the redelivery changed more than the hash: before %+v, after %+v", before[0], after[0])
	}
	if n := newInsert(legacy, "hard_bounce"); n != 0 {
		t.Errorf("second redelivery affected %d rows, want 0", n)
	}
	same("new lookup after the hash was filled", newLookup(legacy), []string{"hard_bounce"})
	same("old send lookup after the hash was filled", oldSend(legacy), []string{"hard_bounce"})

	// 8. The previous goose runner holds only the baseline. Against a database
	// recorded at a newer version it must not refuse, fail or change anything,
	// so a rollback to the previous image boots. This result decides the
	// rollback wording in the docs (R4-S5): goose v3.29.0 (gooseutil.UpVersions)
	// counts only source files missing below the database's highest version, so
	// a database ahead of its source is current. If a goose upgrade ever refuses
	// here, the docs must say a rollback needs `fold migrate down-one` with the
	// new binary first; change this assertion in the same commit.
	baseline, err := fs.ReadFile(migrations.Files, "00001_baseline.sql")
	if err != nil {
		t.Fatal(err)
	}
	locker, err := lock.NewPostgresSessionLocker(lock.WithLockID(migrations.LockID))
	if err != nil {
		t.Fatal(err)
	}
	previous, err := goose.NewProvider(goose.DialectPostgres, sqlDB, fstest.MapFS{"00001_baseline.sql": &fstest.MapFile{Data: baseline}}, goose.WithSessionLocker(locker))
	if err != nil {
		t.Fatal(err)
	}
	applied := appliedRows(t, sqlDB)
	results, err := previous.Up(ctx)
	if err != nil || len(results) != 0 {
		t.Fatalf("previous goose runner Up = %d results, %v: want nil and nothing applied (if goose now refuses, R4-S5's rollback text must name `fold migrate down-one` with the new binary first)", len(results), err)
	}
	if got := appliedRows(t, sqlDB); got != applied {
		t.Fatalf("previous goose runner changed goose_db_version: before %s, after %s", applied, got)
	}
	if column, check, index, _, _ := suppressionHashObjects(t, sqlDB); !column || !check || !index {
		t.Fatal("previous goose runner removed what 00002 added")
	}
	t.Logf("previous goose runner against version %d: Up returned nil and applied nothing; a rollback to the previous image needs no migrate down", latestVersion(t))
}

// The 00003 consent-erasure-scrub tests (M7.4 R4-2d1, owner decision 1). The
// migration lets reject_consent_change() allow exactly one UPDATE: blank ip,
// user_agent and evidence_text on a soft-deleted owner's row, every other
// column unchanged. Everything else, and every DELETE, keeps the baseline's
// refusal.

const (
	consentScrubTestConsentScrubVersion  = 3
	consentScrubTestConsentAppendOnlyMsg = "consent history is append-only"
)

// consentScrubTestWantConsentRefused checks the trigger's refusal exactly: SQLSTATE P0001 and
// the baseline's message.
func consentScrubTestWantConsentRefused(t *testing.T, label string, err error) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: allowed, want P0001 %q", label, consentScrubTestConsentAppendOnlyMsg)
		return
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "P0001" || pgErr.Message != consentScrubTestConsentAppendOnlyMsg {
		t.Errorf("%s: %v, want P0001 %q", label, err, consentScrubTestConsentAppendOnlyMsg)
	}
}

// consentScrubTestConsentOwners are the ids of the seeded rows: each owner has an email row and
// a push row carrying an address, a user agent and evidence text.
type consentScrubTestConsentOwners struct{ living, deleted []int64 }

// consentScrubTestSeedConsentOwners adds a living owner (user 1) and a soft-deleted owner
// (user 2), each with an email and a push consent row.
func consentScrubTestSeedConsentOwners(t *testing.T, db *sql.DB) consentScrubTestConsentOwners {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO users(id,email) VALUES (1,'living@example.com')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,deleted_at) VALUES (2,'erased@example.com',now())`); err != nil {
		t.Fatal(err)
	}
	var out consentScrubTestConsentOwners
	for _, owner := range []struct {
		id  int64
		ids *[]int64
	}{{1, &out.living}, {2, &out.deleted}} {
		for _, channel := range []string{"email", "push"} {
			var id int64
			err := db.QueryRow(`INSERT INTO consents(user_id,channel,scope,status,source,evidence_text,ip,user_agent)
				VALUES ($1,$2,'all','opted_in','web_form','Ticked the box on the form','203.0.113.7','Mozilla/5.0 (test)') RETURNING id`,
				owner.id, channel).Scan(&id)
			if err != nil {
				t.Fatalf("seed %s consent for user %d: %v", channel, owner.id, err)
			}
			*owner.ids = append(*owner.ids, id)
		}
	}
	return out
}

// consentScrubTestConsentSnapshot returns every consents row as jsonb text by id. With strip
// it leaves out ip, user_agent and evidence_text, the three fields the scrub
// may change.
func consentScrubTestConsentSnapshot(t *testing.T, db *sql.DB, strip bool) map[int64]string {
	t.Helper()
	q := `SELECT id, to_jsonb(c)::text FROM consents c`
	if strip {
		q = `SELECT id, (to_jsonb(c) - 'ip' - 'user_agent' - 'evidence_text')::text FROM consents c`
	}
	rows, err := db.Query(q)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var doc string
		if err := rows.Scan(&id, &doc); err != nil {
			t.Fatal(err)
		}
		out[id] = doc
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// consentScrubTestSameConsentRows reports every id whose row differs between two snapshots.
// skip names ids to leave out of the comparison.
func consentScrubTestSameConsentRows(t *testing.T, label string, got, want map[int64]string, skip ...int64) {
	t.Helper()
	skipped := map[int64]bool{}
	for _, id := range skip {
		skipped[id] = true
	}
	if len(got) != len(want) {
		t.Errorf("%s: %d rows, want %d", label, len(got), len(want))
	}
	for id, doc := range want {
		if skipped[id] {
			continue
		}
		if got[id] != doc {
			t.Errorf("%s: row %d is %s, want %s", label, id, got[id], doc)
		}
	}
}

// consentScrubTestConsentFields reads the three fields the scrub blanks.
func consentScrubTestConsentFields(t *testing.T, db *sql.DB, id int64) (ip, userAgent, evidence string) {
	t.Helper()
	if err := db.QueryRow(`SELECT ip, user_agent, evidence_text FROM consents WHERE id = $1`, id).Scan(&ip, &userAgent, &evidence); err != nil {
		t.Fatal(err)
	}
	return
}

// TestConsentTriggerAllowsOnlyTheErasureScrub runs the trigger at the latest
// version. The scrub is allowed for a soft-deleted owner and for nobody else;
// no other change and no DELETE gets through.
func TestConsentTriggerAllowsOnlyTheErasureScrub(t *testing.T) {
	t.Parallel()
	if latest := latestVersion(t); latest < consentScrubTestConsentScrubVersion {
		t.Fatalf("latest migration = %d, want at least %d (the consent erasure scrub)", latest, consentScrubTestConsentScrubVersion)
	}
	db := openSQL(t, testdb.Fresh(t, true))
	owners := consentScrubTestSeedConsentOwners(t, db)
	livingRow, goneRow, spareRow := owners.living[0], owners.deleted[0], owners.deleted[1]
	const blank = `ip = '', user_agent = '', evidence_text = ''`
	update := func(set string, id int64) error {
		_, err := db.Exec(`UPDATE consents SET `+set+` WHERE id = $1`, id)
		return err
	}
	remove := func(id int64) error {
		_, err := db.Exec(`DELETE FROM consents WHERE id = $1`, id)
		return err
	}
	before := consentScrubTestConsentSnapshot(t, db, false)

	// A living owner's rows are append-only, scrub included.
	consentScrubTestWantConsentRefused(t, "scrub of a living owner's row", update(blank, livingRow))
	consentScrubTestWantConsentRefused(t, "ordinary update of a living owner's row", update(`status = 'opted_out'`, livingRow))
	consentScrubTestWantConsentRefused(t, "no-op update of a living owner's row", update(`ip = ip`, livingRow))
	consentScrubTestWantConsentRefused(t, "delete of a living owner's row", remove(livingRow))

	// A soft-deleted owner's rows allow the scrub and nothing else.
	for _, c := range []struct{ label, set string }{
		{"scrub leaving ip", `user_agent = '', evidence_text = ''`},
		{"scrub leaving user_agent", `ip = '', evidence_text = ''`},
		{"scrub leaving evidence_text", `ip = '', user_agent = ''`},
		{"blanking only ip", `ip = ''`},
		{"blanking only user_agent", `user_agent = ''`},
		{"blanking only evidence_text", `evidence_text = ''`},
		{"status changed with the three blanked", blank + `, status = 'opted_out'`},
		{"scope changed with the three blanked", blank + `, scope = 'category:events'`},
		{"source changed with the three blanked", blank + `, source = 'admin'`},
		{"channel changed with the three blanked", blank + `, channel = 'sms'`},
		{"at changed with the three blanked", blank + `, at = at + interval '1 second'`},
		{"user_id changed with the three blanked", blank + `, user_id = 1`},
		{"id changed with the three blanked", blank + `, id = id + 1000`},
		{"non-empty ip replacement", `ip = 'REDACTED', user_agent = '', evidence_text = ''`},
		{"non-empty user_agent replacement", `ip = '', user_agent = 'REDACTED', evidence_text = ''`},
		{"non-empty evidence_text replacement", `ip = '', user_agent = '', evidence_text = 'REDACTED'`},
		{"edit of the three fields to other text", `ip = '198.51.100.1', user_agent = 'other', evidence_text = 'other'`},
	} {
		consentScrubTestWantConsentRefused(t, c.label, update(c.set, goneRow))
	}
	consentScrubTestWantConsentRefused(t, "delete of a soft-deleted owner's row", remove(goneRow))
	consentScrubTestSameConsentRows(t, "rows after the refusals", consentScrubTestConsentSnapshot(t, db, false), before)

	// The scrub itself.
	strippedBefore := consentScrubTestConsentSnapshot(t, db, true)
	res, err := db.Exec(`UPDATE consents SET `+blank+` WHERE id = $1`, goneRow)
	if err != nil {
		t.Fatalf("scrub of a soft-deleted owner's row: %v", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("scrub affected %d rows (%v), want 1", n, err)
	}
	if ip, ua, ev := consentScrubTestConsentFields(t, db, goneRow); ip != "" || ua != "" || ev != "" {
		t.Fatalf("after the scrub: ip=%q user_agent=%q evidence_text=%q, want all empty", ip, ua, ev)
	}
	after := consentScrubTestConsentSnapshot(t, db, false)
	consentScrubTestSameConsentRows(t, "every column but the three after the scrub", consentScrubTestConsentSnapshot(t, db, true), strippedBefore)
	consentScrubTestSameConsentRows(t, "rows the scrub did not touch", after, before, goneRow)
	if err := update(blank, goneRow); err != nil {
		t.Fatalf("scrubbing an already scrubbed row: %v", err)
	}
	// A scrubbed row is still append-only for everything else.
	consentScrubTestWantConsentRefused(t, "status change on a scrubbed row", update(`status = 'opted_out'`, goneRow))
	consentScrubTestWantConsentRefused(t, "text added back to a scrubbed row", update(`evidence_text = 'again'`, goneRow))
	consentScrubTestWantConsentRefused(t, "delete of a scrubbed row", remove(goneRow))
	if ip, ua, ev := consentScrubTestConsentFields(t, db, spareRow); ip == "" || ua == "" || ev == "" {
		t.Fatalf("the owner's other row was touched: ip=%q user_agent=%q evidence_text=%q", ip, ua, ev)
	}
}

// TestConsentErasureScrubDownAndUp walks 00003 away and back over seeded rows.
// Down restores the baseline body, so the scrub is refused again, and neither
// direction touches a row.
func TestConsentErasureScrubDownAndUp(t *testing.T) {
	t.Parallel()
	db := openSQL(t, testdb.Fresh(t, false))
	ctx := context.Background()
	p, err := migrations.NewProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, consentScrubTestConsentScrubVersion-1); err != nil {
		t.Fatalf("up to %d: %v", consentScrubTestConsentScrubVersion-1, err)
	}
	owners := consentScrubTestSeedConsentOwners(t, db)
	goneRow, otherRow := owners.deleted[0], owners.deleted[1]
	const blank = `ip = '', user_agent = '', evidence_text = ''`
	scrub := func(id int64) error {
		_, err := db.Exec(`UPDATE consents SET `+blank+` WHERE id = $1`, id)
		return err
	}
	functionSource := func() string {
		t.Helper()
		var src string
		if err := db.QueryRow(`SELECT prosrc FROM pg_proc WHERE oid = 'public.reject_consent_change()'::regprocedure`).Scan(&src); err != nil {
			t.Fatal(err)
		}
		return src
	}
	baselineSource := functionSource()
	consentScrubTestWantConsentRefused(t, "scrub before 00003", scrub(goneRow))
	seeded := consentScrubTestConsentSnapshot(t, db, false)

	if _, err := p.UpTo(ctx, consentScrubTestConsentScrubVersion); err != nil {
		t.Fatalf("up to %d: %v", consentScrubTestConsentScrubVersion, err)
	}
	if functionSource() == baselineSource {
		t.Fatal("00003 left the baseline function body in place")
	}
	consentScrubTestSameConsentRows(t, "rows after 00003", consentScrubTestConsentSnapshot(t, db, false), seeded)
	if err := scrub(goneRow); err != nil {
		t.Fatalf("scrub of the soft-deleted owner's row at version %d: %v", consentScrubTestConsentScrubVersion, err)
	}
	consentScrubTestWantConsentRefused(t, "scrub of a living owner's row at the new version", scrub(owners.living[0]))
	scrubbed := consentScrubTestConsentSnapshot(t, db, false)

	if _, err := p.DownTo(ctx, consentScrubTestConsentScrubVersion-1); err != nil {
		t.Fatalf("down to %d: %v", consentScrubTestConsentScrubVersion-1, err)
	}
	if version, err := p.GetDBVersion(ctx); err != nil || version != consentScrubTestConsentScrubVersion-1 {
		t.Fatalf("version after the down = %d, %v, want %d", version, err, consentScrubTestConsentScrubVersion-1)
	}
	if got := functionSource(); got != baselineSource {
		t.Fatalf("function body after the down is not the baseline's:\n%q\nwant\n%q", got, baselineSource)
	}
	consentScrubTestSameConsentRows(t, "rows after the down", consentScrubTestConsentSnapshot(t, db, false), scrubbed)
	consentScrubTestWantConsentRefused(t, "scrub of the deleted owner's other row after the down", scrub(otherRow))

	if _, err := p.UpTo(ctx, consentScrubTestConsentScrubVersion); err != nil {
		t.Fatalf("up again to %d: %v", consentScrubTestConsentScrubVersion, err)
	}
	if err := scrub(otherRow); err != nil {
		t.Fatalf("scrub after up again: %v", err)
	}
	consentScrubTestSameConsentRows(t, "rows after up again", consentScrubTestConsentSnapshot(t, db, false), scrubbed, otherRow)
	if ip, ua, ev := consentScrubTestConsentFields(t, db, otherRow); ip != "" || ua != "" || ev != "" {
		t.Fatalf("after the second scrub: ip=%q user_agent=%q evidence_text=%q, want all empty", ip, ua, ev)
	}
}

// TestConsentScrubMigrationTakesNoConsentsLock holds the strongest lock on
// consents while 00003 runs. CREATE OR REPLACE FUNCTION does not touch the
// table, so the migration finishes at once instead of waiting for the 5s
// lock_timeout. This proves the header's lock claim.
func TestConsentScrubMigrationTakesNoConsentsLock(t *testing.T) {
	t.Parallel()
	name := testdb.Fresh(t, false)
	db := openSQL(t, name)
	ctx := context.Background()
	p, err := migrations.NewProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, consentScrubTestConsentScrubVersion-1); err != nil {
		t.Fatalf("up to %d: %v", consentScrubTestConsentScrubVersion-1, err)
	}
	holder, err := openSQL(t, name).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback()
	if _, err := holder.Exec(`LOCK TABLE consents IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	// Control: the lock really blocks another session.
	probe, err := openSQL(t, name).Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	if _, err := probe.ExecContext(ctx, `SET lock_timeout = '200ms'`); err != nil {
		t.Fatal(err)
	}
	if _, err := probe.ExecContext(ctx, `SELECT count(*) FROM consents`); pgErrorCode(err) != "55P03" {
		t.Fatalf("a read of consents under the held lock: %v (code %q), want lock_not_available 55P03", err, pgErrorCode(err))
	}

	// Well inside the migration's own 5s lock_timeout, so a wait on consents
	// fails here rather than passing by timing out later.
	mctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	start := time.Now()
	_, err = p.UpTo(mctx, consentScrubTestConsentScrubVersion)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("UpTo(%d) with consents locked ACCESS EXCLUSIVE: %v after %s; the migration waits on consents, so correct its header's lock claim", consentScrubTestConsentScrubVersion, err, elapsed)
	}
	if version, err := p.GetDBVersion(ctx); err != nil || version != consentScrubTestConsentScrubVersion {
		t.Fatalf("version after the migration = %d, %v, want %d", version, err, consentScrubTestConsentScrubVersion)
	}
}

// TestConsentScrubKeepsThePreviousWriterWorking is the DEC-138 previous-release
// check. The previous binary writes consents with a GORM insert (setConsent)
// and never updates or deletes them, so at version 3 its insert still works and
// an ordinary UPDATE of a living owner's row is still refused.
func TestConsentScrubKeepsThePreviousWriterWorking(t *testing.T) {
	t.Parallel()
	if latest := latestVersion(t); latest < consentScrubTestConsentScrubVersion {
		t.Fatalf("latest migration = %d, want at least %d (the consent erasure scrub)", latest, consentScrubTestConsentScrubVersion)
	}
	gdb, err := testdb.Open(testdb.Fresh(t, true))
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	if _, err := sqlDB.Exec(`INSERT INTO users(id,email) VALUES (1,'living@example.com')`); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.Exec(`INSERT INTO users(id,email,deleted_at) VALUES (2,'erased@example.com',now())`); err != nil {
		t.Fatal(err)
	}

	// The previous writer: a plain insert, for a living owner and for a
	// soft-deleted one (the trigger only watches UPDATE and DELETE).
	var written []models.Consent
	for _, userID := range []uint{1, 2} {
		c := models.Consent{UserID: userID, Channel: models.ConsentEmail, Scope: "all", Status: models.ConsentOptedIn,
			Source: "web_form", EvidenceText: "Ticked the box", IP: "203.0.113.7", UserAgent: "Mozilla/5.0 (test)"}
		if err := gdb.Create(&c).Error; err != nil {
			t.Fatalf("insert of a consent row for user %d: %v", userID, err)
		}
		written = append(written, c)
	}
	var latest models.Consent
	if err := gdb.Where("user_id = ? AND channel = ? AND scope = ?", 1, models.ConsentEmail, "all").
		Order("at DESC, id DESC").First(&latest).Error; err != nil || latest.ID != written[0].ID {
		t.Fatalf("latest consent read = %d (%v), want %d", latest.ID, err, written[0].ID)
	}

	// The new body still refuses the updates and deletes the previous release
	// never sends.
	err = gdb.Model(&models.Consent{}).Where("id = ?", written[0].ID).Update("status", models.ConsentOptedOut).Error
	consentScrubTestWantConsentRefused(t, "ordinary update of a living owner's row", err)
	err = gdb.Model(&models.Consent{}).Where("id = ?", written[0].ID).Updates(map[string]any{"ip": "", "user_agent": "", "evidence_text": ""}).Error
	consentScrubTestWantConsentRefused(t, "scrub of a living owner's row", err)
	consentScrubTestWantConsentRefused(t, "delete of a living owner's row", gdb.Delete(&models.Consent{}, written[0].ID).Error)
	var kept models.Consent
	if err := gdb.First(&kept, written[0].ID).Error; err != nil || kept.Status != models.ConsentOptedIn || kept.IP != "203.0.113.7" {
		t.Fatalf("the living owner's row changed: %+v (%v)", kept, err)
	}
}

// The 00004 ages-note tests (M7.4 R4-T07A, DEC-141's Nov 3, 2026 amendment).
// The migration lets two functions allow exactly one blank: an occurrence_add
// row's ages note goes from non-empty to '' while every other column stays equal
// and the subject's users row is soft-deleted and can be share-locked without
// waiting. add_request_identity() admits the UPDATE; add_request_completed()
// skips the historical completion recheck for the deferred event of that same
// UPDATE on a granted row. Everything else runs the baseline body.
//
// Every test that claims the 00004 contract opens a fresh unmigrated database and
// runs UpTo(agesNoteTriggerTestVersion); none uses the latest version, so a later
// migration cannot change what these tests prove. The latest-schema checks
// (TestNoDrift, TestBaselineFileShape and the down/up tests) stay separate.
//
// Evidence kinds, as the mutation plan records them. A behavioral kill is a test
// that fails at run time because the database now does something else. A
// structural rejection is a text or shape assertion that fails with no change in
// what the database does. A behavioral equivalence is a written argument, never a
// kill.
//
//	M1  identity drops the users lookup          behavioral kill: TestAgesNoteTriggerAllowsOnlyTheErasureBlank
//	                                             (living subject, missing users row)
//	M2  identity drops the whole-row comparison  behavioral kill: TestAgesNoteTriggerAllowsOnlyTheErasureBlank
//	                                             (blank plus another column) and TestAgesNoteTriggerCoversAddedColumns (ii)
//	M3  completion drops the exception           behavioral kill: TestAgesNoteTriggerHistoricalCompletion a and b
//	                                             (replaced and deleted provenance fail at COMMIT); case c, a valid
//	                                             live completion, is a control and is not counted
//	M4a completion drops its row comparison      structural rejection: TestAgesNoteTriggerShape (prefix fidelity);
//	                                             behaviorally equivalent while add_request_identity is the only
//	                                             BEFORE UPDATE trigger (H2), because every UPDATE that reaches the
//	                                             completion function passed the identity comparison first
//	M4b completion drops OLD.status = 'granted' structural rejection: TestAgesNoteTriggerShape (prefix fidelity);
//	                                             behaviorally equivalent while the row comparison holds, since a
//	                                             skipped event then has NEW.status = OLD.status and a status other
//	                                             than granted checks nothing in the baseline body
//	M10 Down restores a different body           behavioral kill and structural rejection: TestAgesNoteMigrationDownAndUp
//	M12 identity locks FOR SHARE without NOWAIT  behavioral kill: TestAgesNoteTriggerLockOutcomes (CA-6)
//
// Nothing here disables, drops or creates a trigger, or changes the session's
// trigger firing mode: the reviewed configuration (both triggers enabled, firing
// as origin) is the one under test, and TestAgesNoteTriggerShape asserts it.

const (
	agesNoteTriggerTestVersion = 4
	agesNoteMigrationFile      = "00004_ages_note_erasure_scrub.sql"
	agesNoteIdentityMsg        = "request purpose, add options and terminal outcome are immutable"
	agesNoteCompletionMsg      = "date add requires completed occurrence access"
	agesNoteText               = "Ages 3 and 5, one needs a quiet corner"
)

// agesNoteDB is a database migrated to one version with a small fixture: user 1
// leads group 10 and requests; user 2 is the subject who is erased later; user 3
// is a living subject; user 4 is another subject. Event 20 has occurrences 30
// to 69. accessDB supplies the audit-snapshot helpers.
type agesNoteDB struct {
	accessDB
	p *goose.Provider
}

func newAgesNoteDB(t *testing.T, version int64) agesNoteDB {
	t.Helper()
	name := testdb.Fresh(t, false)
	db := openSQL(t, name)
	p, err := migrations.NewProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(context.Background(), version); err != nil {
		t.Fatalf("up to %d: %v", version, err)
	}
	a := agesNoteDB{accessDB: accessDB{t: t, db: db}, p: p}
	for _, q := range []string{
		`INSERT INTO users(id,email,status,role,account_type) VALUES
		 (1,'ages-leader@example.com','active','member','adult'),
		 (2,'ages-subject@example.com','active','member','adult'),
		 (3,'ages-living@example.com','active','member','adult'),
		 (4,'ages-other@example.com','active','member','adult')`,
		`INSERT INTO groups(id,name,leader_id,status,visibility) VALUES(10,'Ages group',1,'active','invite_only')`,
		`INSERT INTO events(id,group_id,title,description,starts_local,duration_minutes,tzid,rrule,location,visibility,created_by)
		 VALUES(20,10,'Ages series','','2026-12-01T18:00',60,'UTC','','','invite_only',1)`,
		`INSERT INTO event_occurrences(id,event_id,original_date,starts_at,ends_at,status)
		 SELECT 30 + n, 20, date '2026-12-01' + n, timestamptz '2026-12-01 18:00Z' + n * interval '1 day',
		        timestamptz '2026-12-01 19:00Z' + n * interval '1 day', 'scheduled'
		 FROM generate_series(0, 39) n`,
	} {
		a.exec(q)
	}
	return a
}

func (a agesNoteDB) migrateTo(version int64) {
	a.t.Helper()
	if _, err := a.p.UpTo(context.Background(), version); err != nil {
		a.t.Fatalf("up to %d: %v", version, err)
	}
}

func (a agesNoteDB) migrateDownTo(version int64) {
	a.t.Helper()
	if _, err := a.p.DownTo(context.Background(), version); err != nil {
		a.t.Fatalf("down to %d: %v", version, err)
	}
	if got, err := a.p.GetDBVersion(context.Background()); err != nil || got != version {
		a.t.Fatalf("version after the down = %d, %v, want %d", got, err, version)
	}
}

func (a agesNoteDB) register(id, occurrence, user int) {
	a.t.Helper()
	a.exec(`INSERT INTO registrations(id,occurrence_id,user_id,status,source) VALUES($1,$2,$3,'going','admin')`, id, occurrence, user)
}

// agesNoteOutcome gives the six columns that differ by status, in the order
// subject_decision, subject_decided_by, subject_decided_at, completed_at,
// withdrawn_by_id, end_reason. Every combination honors every CHECK of
// contact_requests. Granted rows are built by complete, never inserted.
func agesNoteOutcome(status string, requester, subject int) string {
	switch status {
	case "pending":
		return `'pending', NULL, NULL, NULL, NULL, ''`
	case "denied":
		return fmt.Sprintf(`'denied', %d, '2026-10-25 12:00Z', '2026-10-25 12:00Z', NULL, ''`, subject)
	case "withdrawn":
		return fmt.Sprintf(`'pending', NULL, NULL, '2026-10-25 12:00Z', %d, ''`, requester)
	case "expired":
		return `'pending', NULL, NULL, '2026-11-08 12:00Z', NULL, ''`
	case "ended":
		return `'pending', NULL, NULL, '2026-10-25 12:00Z', NULL, 'removed'`
	}
	panic("agesNoteOutcome: no fixture for status " + status)
}

// addRequest inserts an occurrence_add request created on 2026-10-24 12:00Z.
func (a agesNoteDB) addRequest(id, requester, subject, occurrence int, status, reason, note string) {
	a.t.Helper()
	a.exec(fmt.Sprintf(`INSERT INTO contact_requests(id,requester_id,subject_user_id,context_type,context_id,fields,reason,needs_admin,
		subject_decision,subject_decided_by,subject_decided_at,completed_at,withdrawn_by_id,end_reason,
		admin_decision,status,created_at,expires_at,purpose,occurrence_id,add_attendance_mode,add_childcare_ages_note)
		VALUES(%d,%d,%d,'event',20,'',$1,true,%s,'pending','%s','2026-10-24 12:00Z','2026-11-07 12:00Z','occurrence_add',%d,'in_person',$2)`,
		id, requester, subject, agesNoteOutcome(status, requester, subject), status, occurrence), reason, note)
}

// pendingAt inserts a pending occurrence_add request created at a chosen time.
func (a agesNoteDB) pendingAt(id, requester, subject, occurrence int, created, note string) {
	a.t.Helper()
	a.exec(`INSERT INTO contact_requests(id,requester_id,subject_user_id,context_type,context_id,fields,needs_admin,admin_decision,
		created_at,expires_at,purpose,occurrence_id,add_attendance_mode,add_childcare_ages_note)
		VALUES($1,$2,$3,'event',20,'',true,'pending',$4::timestamptz,$4::timestamptz + interval '14 days','occurrence_add',$5,'in_person',$6)`,
		id, requester, subject, created, occurrence, note)
}

// complete grants a pending request on a registration in one transaction, the way
// the application does: approve, move to granted, then write the access columns.
// Decisions come one day, the completion and the access two days, after creation.
func (a agesNoteDB) complete(request, registration int) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, s := range []struct {
		q    string
		args []any
	}{
		{`UPDATE contact_requests SET subject_decision='approved', subject_decided_by=subject_user_id, subject_decided_at=created_at + interval '1 day',
			admin_decision='approved', admin_decided_by=requester_id, admin_decided_at=created_at + interval '1 day' WHERE id=$1`, []any{request}},
		{`UPDATE contact_requests SET status='granted', completed_at=created_at + interval '2 days', completed_target_id=$2 WHERE id=$1`, []any{request, registration}},
		{`UPDATE registrations SET access_origin='manager_add', access_issuer_id=1, access_request_id=$1,
			access_granted_at=(SELECT created_at + interval '2 days' FROM contact_requests WHERE id=$1),
			access_ended_at=NULL, access_end_reason='', access_ended_by=NULL WHERE id=$2`, []any{request, registration}},
	} {
		if _, err := tx.Exec(s.q, s.args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (a agesNoteDB) endAccess(registration int, endedAt string) {
	a.t.Helper()
	a.exec(`UPDATE registrations SET access_ended_at=$2::timestamptz, access_end_reason='declined', access_ended_by=user_id WHERE id=$1`, registration, endedAt)
}

// recordHistory writes the added and ended audit snapshots the provenance
// trigger needs before a grant can be replaced or its registration deleted.
func (a agesNoteDB) recordHistory(request, registration, subject, occurrence int, endedAt string) {
	a.t.Helper()
	var granted string
	if err := a.db.QueryRow(`SELECT access_granted_at::text FROM registrations WHERE id=$1`, registration).Scan(&granted); err != nil {
		a.t.Fatal(err)
	}
	extra := map[string]any{"issuerId": 1, "requestId": request}
	a.recordAdded(registration, subject, occurrence, "manager_add", granted, extra)
	a.recordEnded(registration, subject, occurrence, "manager_add", granted, endedAt, "declined", subject, extra)
}

func (a agesNoteDB) tombstone(user int) {
	a.t.Helper()
	a.exec(`UPDATE users SET deleted_at = now() WHERE id = $1`, user)
}

// seedRows builds every row the tests use, while user 2 is still alive.
//
//	100-104        pending, subject 2 (103 has a nonempty reason, 104 is for the stale restore)
//	111-114        denied, withdrawn, expired, ended, subject 2
//	120            granted, live and valid, registration 140
//	121            granted, access ended and the registration kept (control c), registration 141
//	122 and 123    granted, request 122 replaced by 123 on registration 142 (case a)
//	124            granted, registration 143 ended with snapshots and deleted (case b)
//	130            pending, living subject 3
//	131            pending, subject 99 who has no users row
//	132            pending, requester 2 and living subject 3 (requester-only)
//	133            denied group_add with a reason, subject 2
//	134            pending contact request
//	135            pending, subject 3 (fresh completion), registration 151
func (a agesNoteDB) seedRows() {
	a.t.Helper()
	note := func(id int) string { return fmt.Sprintf("%s (%d)", agesNoteText, id) }
	for _, r := range []struct {
		id, requester, subject, occurrence int
		status, reason                     string
	}{
		{100, 1, 2, 30, "pending", ""}, {101, 1, 2, 31, "pending", ""}, {102, 1, 2, 32, "pending", ""},
		{103, 1, 2, 33, "pending", "Please add me, I was away"}, {104, 1, 2, 33, "pending", ""},
		{111, 1, 2, 30, "denied", ""}, {112, 1, 2, 31, "withdrawn", ""}, {113, 1, 2, 32, "expired", ""}, {114, 1, 2, 33, "ended", ""},
		{130, 1, 3, 30, "pending", ""}, {131, 1, 99, 30, "pending", ""}, {132, 2, 3, 30, "pending", ""}, {135, 1, 3, 31, "pending", ""},
	} {
		a.addRequest(r.id, r.requester, r.subject, r.occurrence, r.status, r.reason, note(r.id))
	}
	a.register(151, 31, 3)
	for _, g := range []struct{ request, registration, occurrence int }{{120, 140, 34}, {121, 141, 35}, {122, 142, 36}, {124, 143, 37}} {
		a.register(g.registration, g.occurrence, 2)
		a.addRequest(g.request, 1, 2, g.occurrence, "pending", "", note(g.request))
		if err := a.complete(g.request, g.registration); err != nil {
			a.t.Fatalf("grant request %d: %v", g.request, err)
		}
	}
	a.endAccess(141, "2026-10-27 12:00Z")
	a.endAccess(142, "2026-10-27 12:00Z")
	a.recordHistory(122, 142, 2, 36, "2026-10-27 12:00Z")
	a.pendingAt(123, 1, 2, 36, "2026-10-28 12:00Z", note(123))
	if err := a.complete(123, 142); err != nil {
		a.t.Fatalf("replace request 122 with 123: %v", err)
	}
	a.endAccess(143, "2026-10-27 12:00Z")
	a.recordHistory(124, 143, 2, 37, "2026-10-27 12:00Z")
	a.exec(`DELETE FROM registrations WHERE id = 143`)
	a.exec(`INSERT INTO contact_requests(id,requester_id,subject_user_id,context_type,context_id,fields,reason,needs_admin,
		subject_decision,subject_decided_by,subject_decided_at,admin_decision,status,created_at,expires_at,completed_at,purpose)
		VALUES(133,1,2,'group',10,'','Group reason',true,'denied',2,'2026-10-25 12:00Z','pending','denied',
		'2026-10-24 12:00Z','2026-11-07 12:00Z','2026-10-25 12:00Z','group_add')`)
	a.exec(`INSERT INTO contact_requests(id,requester_id,subject_user_id,context_type,context_id,fields,needs_admin,admin_decision,created_at,expires_at)
		VALUES(134,1,4,'group',10,'email',false,'not_needed','2026-10-24 12:00Z','2026-11-07 12:00Z')`)
}

// rows returns every contact_requests row as jsonb text by id. With strip it
// leaves out the ages note, the one column the blank may change.
func (a agesNoteDB) rows(strip bool) map[int]string {
	a.t.Helper()
	q := `SELECT id, to_jsonb(c)::text FROM contact_requests c`
	if strip {
		q = `SELECT id, (to_jsonb(c) - 'add_childcare_ages_note')::text FROM contact_requests c`
	}
	rs, err := a.db.Query(q)
	if err != nil {
		a.t.Fatal(err)
	}
	defer rs.Close()
	out := map[int]string{}
	for rs.Next() {
		var id int
		var doc string
		if err := rs.Scan(&id, &doc); err != nil {
			a.t.Fatal(err)
		}
		out[id] = doc
	}
	if err := rs.Err(); err != nil {
		a.t.Fatal(err)
	}
	return out
}

func (a agesNoteDB) noteOf(id int) string {
	a.t.Helper()
	var note string
	if err := a.db.QueryRow(`SELECT add_childcare_ages_note FROM contact_requests WHERE id = $1`, id).Scan(&note); err != nil {
		a.t.Fatal(err)
	}
	return note
}

// digest hashes a table's rows, so a test can show the blank touched nothing else.
func (a agesNoteDB) digest(table string) string {
	a.t.Helper()
	var sum string
	if err := a.db.QueryRow(`SELECT coalesce(md5(string_agg(to_jsonb(t)::text, '|' ORDER BY t.id)), '') FROM ` + table + ` t`).Scan(&sum); err != nil {
		a.t.Fatal(err)
	}
	return sum
}

type agesNoteStep struct {
	label string
	q     string
	args  []any
}

// runTx runs the steps in one transaction and returns the label of the step that
// failed ("COMMIT" for the commit) with its error. With immediate it first makes
// the completion trigger check at the end of each statement. It stops at the first
// failure, so no later statement runs against an aborted transaction.
func (a agesNoteDB) runTx(immediate bool, steps ...agesNoteStep) (string, error) {
	tx, err := a.db.Begin()
	if err != nil {
		return "begin", err
	}
	defer tx.Rollback()
	if immediate {
		if _, err := tx.Exec(`SET CONSTRAINTS add_request_completed IMMEDIATE`); err != nil {
			return "set constraints", err
		}
	}
	for _, s := range steps {
		if _, err := tx.Exec(s.q, s.args...); err != nil {
			return s.label, err
		}
	}
	if err := tx.Commit(); err != nil {
		return "COMMIT", err
	}
	return "", nil
}

// agesNoteWantError checks SQLSTATE and, when msg is not empty, the message.
func agesNoteWantError(t *testing.T, label string, err error, code, msg string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Errorf("%s: %v, want %s %q", label, err, code, msg)
		return
	}
	if pgErr.Code != code || (msg != "" && pgErr.Message != msg) {
		t.Errorf("%s: %s %q, want %s %q", label, pgErr.Code, pgErr.Message, code, msg)
	}
}

// agesNoteSameRows reports every id whose row differs between two snapshots.
func agesNoteSameRows(t *testing.T, label string, got, want map[int]string, skip ...int) {
	t.Helper()
	skipped := map[int]bool{}
	for _, id := range skip {
		skipped[id] = true
	}
	if len(got) != len(want) {
		t.Errorf("%s: %d rows, want %d", label, len(got), len(want))
	}
	for id, doc := range want {
		if skipped[id] {
			continue
		}
		if got[id] != doc {
			t.Errorf("%s: row %d is %s, want %s", label, id, got[id], doc)
		}
	}
}

const agesNoteBlankSQL = `UPDATE contact_requests SET add_childcare_ages_note = '' WHERE id = $1`

// TestAgesNoteTriggerAllowsOnlyTheErasureBlank runs the licence at 00004. The blank
// of a soft-deleted subject's occurrence_add note commits at every status,
// including a granted row with a valid registration. Every direct-SQL variation
// fails with SQLSTATE 23514 from the identity trigger and writes nothing.
func TestAgesNoteTriggerAllowsOnlyTheErasureBlank(t *testing.T) {
	t.Parallel()
	a := newAgesNoteDB(t, agesNoteTriggerTestVersion)
	a.seedRows()
	a.tombstone(2)
	before := a.rows(false)

	const blank = `UPDATE contact_requests SET add_childcare_ages_note = ''`
	for _, c := range []struct {
		label string
		q     string
	}{
		{"living subject", blank + ` WHERE id = 130`},
		{"subject whose users row is missing", blank + ` WHERE id = 131`},
		{"requester-only row, living subject", blank + ` WHERE id = 132`},
		{"nonempty replacement for a deleted subject", `UPDATE contact_requests SET add_childcare_ages_note = 'replacement' WHERE id = 100`},
		{"blank and reason cleared on a pending row that has a reason", blank + `, reason = '' WHERE id = 103`},
		{"blank and reason changed", blank + `, reason = 'changed' WHERE id = 100`},
		{"blank and withdrawal", blank + `, status = 'withdrawn', withdrawn_by_id = 1, completed_at = '2026-10-25 12:00Z' WHERE id = 100`},
		{"blank and denial", blank + `, subject_decision = 'denied', subject_decided_by = 2, subject_decided_at = '2026-10-25 12:00Z',
			status = 'denied', completed_at = '2026-10-25 12:00Z' WHERE id = 100`},
		{"blank and an admin decision", blank + `, admin_decision = 'approved', admin_decided_by = 1, admin_decided_at = '2026-10-25 12:00Z' WHERE id = 100`},
		{"blank and completed_target_id", blank + `, completed_target_id = 140 WHERE id = 100`},
		{"blank and expires_at", blank + `, expires_at = expires_at + interval '1 day' WHERE id = 100`},
		{"blank and completed_at", blank + `, completed_at = '2026-10-25 12:00Z' WHERE id = 100`},
		{"blank and the guest count", blank + `, add_guest_count = 1 WHERE id = 100`},
		{"blank and the childcare count", blank + `, add_childcare_count = 1 WHERE id = 100`},
		{"blank and a nullable column", blank + `, withdrawn_by_id = 1 WHERE id = 100`},
		{"blank and reason on a terminal row", blank + `, reason = 'x' WHERE id = 111`},
		{"blank and status on a terminal row", blank + `, status = 'expired', completed_at = '2026-11-08 12:00Z' WHERE id = 111`},
		{"reason change on a terminal group_add row", `UPDATE contact_requests SET reason = 'edited' WHERE id = 133`},
	} {
		_, err := a.db.Exec(c.q)
		agesNoteWantError(t, c.label, err, "23514", agesNoteIdentityMsg)
	}
	agesNoteSameRows(t, "rows after the refusals", a.rows(false), before)

	// The licensed blank, alone, at every status. The pending row 103 keeps its
	// reason, and the granted rows 120 to 124 cover a valid live grant, ended
	// access, a replaced request, its replacement and a deleted registration.
	blanked := []int{100, 103, 111, 112, 113, 114, 120, 121, 122, 123, 124}
	var statuses int
	if err := a.db.QueryRow(`SELECT count(DISTINCT status) FROM contact_requests WHERE id IN (100, 111, 112, 113, 114, 120)`).Scan(&statuses); err != nil || statuses != 6 {
		t.Fatalf("the blanked rows cover %d statuses (%v), want pending, granted, denied, withdrawn, expired and ended", statuses, err)
	}
	for _, id := range blanked {
		stripped := a.rows(true)
		res, err := a.db.Exec(agesNoteBlankSQL, id)
		if err != nil {
			t.Errorf("licensed blank of request %d: %v", id, err)
			continue
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			t.Errorf("blank of request %d affected %d rows (%v), want 1", id, n, err)
		}
		if got := a.noteOf(id); got != "" {
			t.Errorf("request %d note after the blank = %q, want empty", id, got)
		}
		agesNoteSameRows(t, fmt.Sprintf("every column but the note after blanking %d", id), a.rows(true), stripped)
	}
	agesNoteSameRows(t, "rows the blank did not touch", a.rows(false), before, blanked...)
	var reason string
	if err := a.db.QueryRow(`SELECT reason FROM contact_requests WHERE id = 103`).Scan(&reason); err != nil || reason != "Please add me, I was away" {
		t.Fatalf("reason of request 103 after the blank = %q (%v), want it unchanged", reason, err)
	}

	// A blank row cannot be restored: a stale save that puts the old text back is
	// a nonempty replacement.
	if _, err := a.db.Exec(agesNoteBlankSQL, 104); err != nil {
		t.Fatalf("blank of request 104: %v", err)
	}
	_, err := a.db.Exec(`UPDATE contact_requests SET add_childcare_ages_note = $2 WHERE id = $1`, 104, agesNoteText)
	agesNoteWantError(t, "stale restore after a blank", err, "23514", agesNoteIdentityMsg)
	if got := a.noteOf(104); got != "" {
		t.Errorf("request 104 note after the refused restore = %q, want empty", got)
	}

	// A contact request's reason is not protected by these triggers (the baseline
	// rule, documented here rather than prevented): the change commits.
	if _, err := a.db.Exec(`UPDATE contact_requests SET reason = 'changed' WHERE id = 134`); err != nil {
		t.Errorf("reason change on a contact request: %v", err)
	}
}

type agesNoteWant struct{ step, msg string }

// TestAgesNoteTriggerDeferredCases walks D1 to D6 through the deferred completion
// trigger, each in default deferral and in immediate mode. Each case records
// which call returns the error and its SQLSTATE, and checks that nothing
// persisted. A statement after a failing one never runs.
func TestAgesNoteTriggerDeferredCases(t *testing.T) {
	t.Parallel()
	a := newAgesNoteDB(t, agesNoteTriggerTestVersion)
	a.seedRows()
	a.tombstone(2)

	blank := func(id int) agesNoteStep {
		return agesNoteStep{label: "licensed blank", q: agesNoteBlankSQL, args: []any{id}}
	}
	approve := func(id int) agesNoteStep {
		return agesNoteStep{label: "approve", args: []any{id}, q: `UPDATE contact_requests SET subject_decision='approved', subject_decided_by=subject_user_id,
			subject_decided_at='2026-10-25 12:00Z', admin_decision='approved', admin_decided_by=1, admin_decided_at='2026-10-25 12:00Z' WHERE id=$1`}
	}
	grant := func(label string, id, target int) agesNoteStep {
		return agesNoteStep{label: label, args: []any{id, target},
			q: `UPDATE contact_requests SET status='granted', completed_at='2026-10-25 12:00Z', completed_target_id=$2 WHERE id=$1`}
	}
	forged := agesNoteStep{label: "forged granted insert", q: `INSERT INTO contact_requests(id,requester_id,subject_user_id,context_type,context_id,fields,needs_admin,
		subject_decision,subject_decided_by,subject_decided_at,admin_decision,admin_decided_by,admin_decided_at,
		status,created_at,expires_at,completed_at,purpose,occurrence_id,add_attendance_mode,add_childcare_ages_note,completed_target_id)
		VALUES(150,1,2,'event',20,'',true,'approved',2,'2026-10-25 12:00Z','approved',1,'2026-10-25 12:00Z',
		'granted','2026-10-24 12:00Z','2026-11-07 12:00Z','2026-10-25 12:00Z','occurrence_add',38,'in_person','forged note',999)`}
	setImmediate := agesNoteStep{label: "SET CONSTRAINTS", q: `SET CONSTRAINTS add_request_completed IMMEDIATE`}

	run := func(name string, immediate bool, steps []agesNoteStep, want agesNoteWant) {
		t.Helper()
		mode := "default deferral"
		if immediate {
			mode = "immediate"
		}
		label := name + ", " + mode
		before := a.rows(false)
		failed, err := a.runTx(immediate, steps...)
		if want.step == "" {
			if err != nil {
				t.Errorf("%s: failed at %q: %v, want it to commit", label, failed, err)
			}
			return
		}
		if failed != want.step {
			t.Errorf("%s: failed at %q (%v), want the failure at %q", label, failed, err, want.step)
			return
		}
		agesNoteWantError(t, label, err, "23514", want.msg)
		agesNoteSameRows(t, label+" left rows behind", a.rows(false), before)
	}

	// D1: a forged granted INSERT, then a licensed blank. Under default deferral
	// both statements succeed and COMMIT fails from the INSERT's own event; in
	// immediate mode the INSERT fails at the end of its statement and the blank
	// is never sent.
	d1 := []agesNoteStep{forged, blank(100)}
	run("D1", false, d1, agesNoteWant{"COMMIT", agesNoteCompletionMsg})
	run("D1", true, d1, agesNoteWant{"forged granted insert", agesNoteCompletionMsg})

	// D2: a move to granted without its completion, then a licensed blank of the
	// same row. Each deferred event keeps its own old and new row.
	d2 := []agesNoteStep{approve(101), grant("move to granted without completion", 101, 999), blank(101)}
	run("D2", false, d2, agesNoteWant{"COMMIT", agesNoteCompletionMsg})
	run("D2", true, d2, agesNoteWant{"move to granted without completion", agesNoteCompletionMsg})

	// D3: a licensed blank of a granted row, then any other change to it. The
	// second statement fails at once in both modes, from the identity trigger,
	// which is never deferred. Nothing is asserted about the blank's own event.
	d3 := []agesNoteStep{blank(120), {label: "later change", q: `UPDATE contact_requests SET reason = 'later change' WHERE id = 120`}}
	run("D3", false, d3, agesNoteWant{"later change", agesNoteIdentityMsg})
	run("D3", true, d3, agesNoteWant{"later change", agesNoteIdentityMsg})

	// D4: a licensed blank of a pending row, then an unlicensed move to granted.
	d4 := []agesNoteStep{blank(102), approve(102), grant("unlicensed move to granted", 102, 999)}
	run("D4", false, d4, agesNoteWant{"COMMIT", agesNoteCompletionMsg})
	run("D4", true, d4, agesNoteWant{"unlicensed move to granted", agesNoteCompletionMsg})

	// D5: the D1 INSERT or the D2 UPDATE, then a licensed blank, then SET
	// CONSTRAINTS IMMEDIATE. The SET statement fails from the queued forged
	// event; no order between the events is assumed.
	run("D5 after the D1 insert", false, append(append([]agesNoteStep{}, d1...), setImmediate), agesNoteWant{"SET CONSTRAINTS", agesNoteCompletionMsg})
	run("D5 after the D2 update", false, append(append([]agesNoteStep{}, d2...), setImmediate), agesNoteWant{"SET CONSTRAINTS", agesNoteCompletionMsg})

	// H6: a no-op UPDATE of an already valid granted request passes in both
	// modes. A fresh completion in immediate mode fails at the request write
	// (its request moves to granted before the registration names it), and under
	// default deferral without the registration write it fails at COMMIT. This is
	// recorded, not fixed: write order is unchanged.
	noop := []agesNoteStep{{label: "no-op update", q: `UPDATE contact_requests SET reason = reason WHERE id = 120`}}
	run("H6 no-op update of a valid granted request", false, noop, agesNoteWant{})
	run("H6 no-op update of a valid granted request", true, noop, agesNoteWant{})
	fresh := []agesNoteStep{approve(135), grant("fresh completion", 135, 151)}
	run("H6 fresh completion", true, fresh, agesNoteWant{"fresh completion", agesNoteCompletionMsg})
	run("H6 fresh completion without the registration write", false, fresh, agesNoteWant{"COMMIT", agesNoteCompletionMsg})

	// D6: a licensed blank alone commits in both modes and changes nothing but the
	// note. Request 122 has replaced provenance, 124 a deleted registration.
	for _, c := range []struct {
		immediate bool
		id        int
	}{{false, 122}, {true, 124}} {
		stripped := a.rows(true)
		registrations := a.digest("registrations")
		run("D6", c.immediate, []agesNoteStep{blank(c.id)}, agesNoteWant{})
		if got := a.noteOf(c.id); got != "" {
			t.Errorf("D6 request %d note = %q, want empty", c.id, got)
		}
		agesNoteSameRows(t, fmt.Sprintf("D6 every column but the note of request %d", c.id), a.rows(true), stripped)
		if got := a.digest("registrations"); got != registrations {
			t.Errorf("D6 request %d: the blank changed a registration", c.id)
		}
	}
}

// TestAgesNoteTriggerHistoricalCompletion blanks granted rows whose completion
// facts moved. The baseline recheck (a no-op UPDATE) fails for a and b, so the
// exception is what lets the blank commit; c is a valid live completion and a
// control for the lookup itself. Registrations and audit snapshots are untouched.
func TestAgesNoteTriggerHistoricalCompletion(t *testing.T) {
	t.Parallel()
	a := newAgesNoteDB(t, agesNoteTriggerTestVersion)
	a.seedRows()
	a.tombstone(2)
	for _, c := range []struct {
		name    string
		id      int
		control bool
	}{
		{"a: request replaced by a newer one on the same registration", 122, false},
		{"b: registration retired with snapshots and deleted", 124, false},
		{"c: access ended, registration still in place (control)", 121, true},
	} {
		_, err := a.db.Exec(`UPDATE contact_requests SET reason = reason WHERE id = $1`, c.id)
		if c.control {
			if err != nil {
				t.Errorf("%s: the baseline recheck of a valid completion failed: %v", c.name, err)
			}
		} else {
			agesNoteWantError(t, c.name+": the baseline recheck", err, "23514", agesNoteCompletionMsg)
		}
		stripped, registrations, audit := a.rows(true), a.digest("registrations"), a.digest("audit_logs")
		if _, err := a.db.Exec(agesNoteBlankSQL, c.id); err != nil {
			t.Errorf("%s: licensed blank: %v", c.name, err)
			continue
		}
		if got := a.noteOf(c.id); got != "" {
			t.Errorf("%s: note after the blank = %q, want empty", c.name, got)
		}
		agesNoteSameRows(t, c.name+": every column but the note", a.rows(true), stripped)
		if a.digest("registrations") != registrations || a.digest("audit_logs") != audit {
			t.Errorf("%s: the blank changed a registration or an audit row", c.name)
		}
	}
	var holder int
	if err := a.db.QueryRow(`SELECT access_request_id FROM registrations WHERE id = 142`).Scan(&holder); err != nil || holder != 123 {
		t.Fatalf("registration 142 names request %d (%v), want the replacement 123", holder, err)
	}
	if _, err := a.db.Exec(agesNoteBlankSQL, 123); err != nil {
		t.Errorf("licensed blank of the replacement: %v", err)
	}
}

// agesNoteBypassWords lists the RC-1(a) words found in a migration's text, case
// insensitively with whitespace collapsed.
func agesNoteBypassWords(src string) []string {
	text := strings.Join(strings.Fields(strings.ToLower(src)), " ")
	var found []string
	for _, w := range []string{"disable trigger", "enable replica trigger", "enable always trigger", "session_replication_role",
		"set_config", "current_setting", "drop trigger", "create trigger", "alter table"} {
		if strings.Contains(text, w) {
			found = append(found, w)
		}
	}
	return found
}

// TestAgesNoteMigrationFileHasNoTriggerBypass reads the migration's own text
// (hard constraint H8 as scoped by RC-1(a)), after showing the detector works.
func TestAgesNoteMigrationFileHasNoTriggerBypass(t *testing.T) {
	t.Parallel()
	if got := agesNoteBypassWords("ALTER  TABLE x\nDISABLE TRIGGER y; SELECT set_config('session_replication_role','replica',true);"); len(got) != 4 {
		t.Fatalf("detector found %q in its fixture, want alter table, disable trigger, session_replication_role and set_config", got)
	}
	b, err := migrations.Files.ReadFile(agesNoteMigrationFile)
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if found := agesNoteBypassWords(src); len(found) > 0 {
		t.Errorf("%s contains %q", agesNoteMigrationFile, found)
	}
	if strings.Contains(strings.ToLower(src), "-- contracts:") {
		t.Errorf("%s has a contracts line, but nothing is dropped or renamed (DEC-138)", agesNoteMigrationFile)
	}
	if !strings.Contains(src, "DEC-138") || strings.Count(src, "-- +goose StatementBegin") != 4 || strings.Count(src, "-- +goose StatementEnd") != 4 {
		t.Errorf("%s lacks its DEC-138 note or the four statement blocks (two in Up, two in Down)", agesNoteMigrationFile)
	}
}

// agesNoteCollapse collapses runs of whitespace to one space outside string
// literals, trims both ends and keeps case.
func agesNoteCollapse(s string) string {
	var b strings.Builder
	inQuote, pendingSpace := false, false
	for _, r := range s {
		if !inQuote && (r == ' ' || r == '\n' || r == '\t' || r == '\r') {
			pendingSpace = b.Len() > 0
			continue
		}
		if pendingSpace {
			b.WriteByte(' ')
			pendingSpace = false
		}
		if r == '\'' {
			inQuote = !inQuote
		}
		b.WriteRune(r)
	}
	return b.String()
}

// The adopted compact templates (inventory T07, "the two function shapes"): the
// text each new body adds in front of the previous body.
const (
	agesNoteIdentityTemplate  = `DECLARE v public.contact_requests%ROWTYPE; BEGIN IF TG_OP = 'UPDATE' THEN IF OLD.purpose = 'occurrence_add' AND OLD.add_childcare_ages_note <> '' AND NEW.add_childcare_ages_note = '' THEN v := NEW; v.add_childcare_ages_note := OLD.add_childcare_ages_note; IF v IS NOT DISTINCT FROM OLD THEN PERFORM 1 FROM public.users u WHERE u.id = OLD.subject_user_id AND u.deleted_at IS NOT NULL FOR SHARE OF u NOWAIT; IF FOUND THEN RETURN NEW; END IF; END IF; END IF; END IF;`
	agesNoteCompletedTemplate = `DECLARE v public.contact_requests%ROWTYPE; BEGIN IF TG_OP = 'UPDATE' THEN IF OLD.purpose = 'occurrence_add' AND OLD.status = 'granted' AND OLD.add_childcare_ages_note <> '' AND NEW.add_childcare_ages_note = '' THEN v := NEW; v.add_childcare_ages_note := OLD.add_childcare_ages_note; IF v IS NOT DISTINCT FROM OLD THEN RETURN NULL; END IF; END IF; END IF;`
)

// agesNoteFidelity proves a replaced body is the template followed by the
// previous body, byte for byte. It splits the new source at the first occurrence
// of the previous body's first statement; the part in front, whitespace
// collapsed outside string literals, must equal the template; the rest must
// equal the previous source after its BEGIN line, including the closing END.
// This is the structural rejection for M4a and M4b: changing a word of the new
// statements changes no runtime outcome the tests can see, but it does change
// this comparison.
func agesNoteFidelity(t *testing.T, label, previous, current, template string) {
	t.Helper()
	const begin = "\nBEGIN\n"
	i := strings.Index(previous, begin)
	if i < 0 {
		t.Errorf("%s: the previous source has no BEGIN line", label)
		return
	}
	rest := previous[i+len(begin):]
	first := rest[:strings.Index(rest, "\n")+1]
	j := strings.Index(current, first)
	if j < 0 {
		t.Errorf("%s: the new source does not contain the previous body's first statement %q", label, first)
		return
	}
	if current[j:] != rest {
		t.Errorf("%s: the new source from the previous first statement is not the previous body byte for byte:\n%q\nwant\n%q", label, current[j:], rest)
	}
	if got := agesNoteCollapse(current[:j]); got != template {
		t.Errorf("%s: the added prefix is\n%s\nwant\n%s", label, got, template)
	}
}

type agesNoteShapeSnapshot struct {
	triggers, constraints, others, attributes, identitySrc, completedSrc string
}

func agesNoteLines(t *testing.T, db *sql.DB, q string) string {
	t.Helper()
	rs, err := db.Query(q)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	defer rs.Close()
	var out []string
	for rs.Next() {
		var s string
		if err := rs.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	if err := rs.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(out, "\n")
}

func agesNoteShape(t *testing.T, db *sql.DB) agesNoteShapeSnapshot {
	t.Helper()
	return agesNoteShapeSnapshot{
		triggers: agesNoteLines(t, db, `SELECT concat_ws('|', t.tgname, t.tgtype::text, t.tgdeferrable::text, t.tginitdeferred::text, t.tgenabled::text, t.tgfoid::regprocedure::text)
			FROM pg_trigger t WHERE t.tgrelid = 'public.contact_requests'::regclass AND NOT t.tgisinternal ORDER BY t.tgname`),
		constraints: agesNoteLines(t, db, `SELECT concat_ws('|', c.conrelid::regclass::text, c.conname, pg_get_constraintdef(c.oid))
			FROM pg_constraint c WHERE c.conrelid IN ('public.contact_requests'::regclass, 'public.contact_grants'::regclass, 'public.registrations'::regclass)
			ORDER BY c.conrelid::regclass::text, c.conname`),
		others: agesNoteLines(t, db, `SELECT p.oid::regprocedure::text || E'\n' || pg_get_functiondef(p.oid)
			FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
			WHERE n.nspname = 'public' AND p.prokind = 'f' AND p.proname NOT IN ('add_request_identity', 'add_request_completed')
			AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_proc'::regclass AND d.objid = p.oid AND d.deptype = 'e')
			ORDER BY p.oid::regprocedure::text`),
		attributes: agesNoteLines(t, db, `SELECT concat_ws('|', p.proname, coalesce(p.proconfig::text, 'no config'), p.prosecdef::text, p.provolatile::text,
			l.lanname, pg_get_userbyid(p.proowner), p.prorettype::regtype::text)
			FROM pg_proc p JOIN pg_language l ON l.oid = p.prolang
			WHERE p.oid IN ('public.add_request_identity()'::regprocedure, 'public.add_request_completed()'::regprocedure) ORDER BY p.proname`),
		identitySrc:  agesNoteLines(t, db, `SELECT prosrc FROM pg_proc WHERE oid = 'public.add_request_identity()'::regprocedure`),
		completedSrc: agesNoteLines(t, db, `SELECT prosrc FROM pg_proc WHERE oid = 'public.add_request_completed()'::regprocedure`),
	}
}

// agesNoteSameShape compares everything but the two function sources, and the
// sources too when withSource is set.
func agesNoteSameShape(t *testing.T, label string, got, want agesNoteShapeSnapshot, withSource bool) {
	t.Helper()
	for _, c := range []struct{ what, got, want string }{
		{"trigger definitions (tgtype, tgdeferrable, tginitdeferred, tgenabled, tgfoid)", got.triggers, want.triggers},
		{"constraint definitions of contact_requests, contact_grants and registrations", got.constraints, want.constraints},
		{"definitions of every other public function", got.others, want.others},
		{"attributes of the two replaced functions (config, security, volatility, language, owner, result)", got.attributes, want.attributes},
	} {
		if c.got != c.want {
			t.Errorf("%s: %s changed:\n%s\nwant\n%s", label, c.what, c.got, c.want)
		}
	}
	if withSource && (got.identitySrc != want.identitySrc || got.completedSrc != want.completedSrc) {
		t.Errorf("%s: a replaced function body differs from the one it should equal", label)
	}
}

// TestAgesNoteTriggerShape holds H1 to H4 at the version before 00004, at 00004,
// after the way down, and after up again: the two triggers keep their timing,
// deferral and enablement (reviewed configuration: both enabled, firing as
// origin); no other trigger exists; constraints and every other function are
// untouched; and each replaced body is the adopted prefix plus the previous body
// byte for byte. The prefix comparison is the structural rejection for M4a and
// M4b.
func TestAgesNoteTriggerShape(t *testing.T) {
	t.Parallel()
	db := openSQL(t, testdb.Fresh(t, false))
	ctx := context.Background()
	p, err := migrations.NewProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, agesNoteTriggerTestVersion-1); err != nil {
		t.Fatal(err)
	}
	previous := agesNoteShape(t, db)
	if _, err := p.UpTo(ctx, agesNoteTriggerTestVersion); err != nil {
		t.Fatal(err)
	}
	current := agesNoteShape(t, db)
	agesNoteSameShape(t, "00004 against the version before it", current, previous, false)
	if current.identitySrc == previous.identitySrc || current.completedSrc == previous.completedSrc {
		t.Fatal("00004 left a baseline function body in place")
	}
	agesNoteFidelity(t, "add_request_identity", previous.identitySrc, current.identitySrc, agesNoteIdentityTemplate)
	agesNoteFidelity(t, "add_request_completed", previous.completedSrc, current.completedSrc, agesNoteCompletedTemplate)

	// H1 and H2 stated outright: exactly these two triggers, enabled as origin,
	// the identity one BEFORE UPDATE FOR EACH ROW and the only BEFORE UPDATE trigger,
	// the completion one an AFTER INSERT OR UPDATE FOR EACH ROW constraint trigger
	// deferrable and initially deferred.
	const wantTriggers = "add_request_completed|21|true|true|O\nadd_request_identity|19|false|false|O"
	if got := agesNoteLines(t, db, `SELECT concat_ws('|', t.tgname, t.tgtype::text, t.tgdeferrable::text, t.tginitdeferred::text, t.tgenabled::text)
			FROM pg_trigger t WHERE t.tgrelid = 'public.contact_requests'::regclass AND NOT t.tgisinternal ORDER BY t.tgname`); got != wantTriggers {
		t.Errorf("non-internal triggers on contact_requests:\n%s\nwant\n%s", got, wantTriggers)
	}
	if got := agesNoteLines(t, db, `SELECT t.tgname FROM pg_trigger t WHERE t.tgrelid = 'public.contact_requests'::regclass AND NOT t.tgisinternal
		AND (t.tgtype & 2) <> 0 AND (t.tgtype & 16) <> 0`); got != "add_request_identity" {
		t.Errorf("BEFORE UPDATE triggers on contact_requests = %q, want only add_request_identity", got)
	}
	if got := agesNoteLines(t, db, `SELECT count(*)::text FROM pg_trigger t JOIN pg_proc p ON p.oid = t.tgfoid
		WHERE t.tgrelid = 'public.contact_requests'::regclass AND NOT t.tgisinternal AND t.tgname = p.proname`); got != "2" {
		t.Errorf("triggers calling the function of their own name = %s, want 2", got)
	}

	moveTo := func(version int64) {
		t.Helper()
		var err error
		if version < agesNoteTriggerTestVersion {
			_, err = p.DownTo(ctx, version)
		} else {
			_, err = p.UpTo(ctx, version)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	moveTo(agesNoteTriggerTestVersion - 1)
	agesNoteSameShape(t, "after the way down", agesNoteShape(t, db), previous, true)
	moveTo(agesNoteTriggerTestVersion)
	agesNoteSameShape(t, "after up again", agesNoteShape(t, db), current, true)
}

// TestAgesNoteMigrationDownAndUp walks 00004 away and back over seeded rows.
// Down restores the previous bodies byte for byte, so the blank is refused again
// (M10), and neither direction touches a row or restores scrubbed text.
func TestAgesNoteMigrationDownAndUp(t *testing.T) {
	t.Parallel()
	a := newAgesNoteDB(t, agesNoteTriggerTestVersion-1)
	a.seedRows()
	a.tombstone(2)
	source := func() (identity, completed string) {
		t.Helper()
		return agesNoteLines(t, a.db, `SELECT prosrc FROM pg_proc WHERE oid = 'public.add_request_identity()'::regprocedure`),
			agesNoteLines(t, a.db, `SELECT prosrc FROM pg_proc WHERE oid = 'public.add_request_completed()'::regprocedure`)
	}
	baselineIdentity, baselineCompleted := source()
	_, err := a.db.Exec(agesNoteBlankSQL, 100)
	agesNoteWantError(t, "blank before 00004", err, "23514", agesNoteIdentityMsg)
	seeded := a.rows(false)

	a.migrateTo(agesNoteTriggerTestVersion)
	if identity, completed := source(); identity == baselineIdentity || completed == baselineCompleted {
		t.Fatal("00004 left a baseline function body in place")
	}
	agesNoteSameRows(t, "rows after 00004", a.rows(false), seeded)
	if _, err := a.db.Exec(agesNoteBlankSQL, 100); err != nil {
		t.Fatalf("blank at version %d: %v", agesNoteTriggerTestVersion, err)
	}
	_, err = a.db.Exec(agesNoteBlankSQL, 130)
	agesNoteWantError(t, "blank of a living subject's row at the new version", err, "23514", agesNoteIdentityMsg)
	blanked := a.rows(false)

	a.migrateDownTo(agesNoteTriggerTestVersion - 1)
	if identity, completed := source(); identity != baselineIdentity || completed != baselineCompleted {
		t.Fatalf("a function body after the down is not the previous one byte for byte:\n%q\n%q\nwant\n%q\n%q", identity, completed, baselineIdentity, baselineCompleted)
	}
	agesNoteSameRows(t, "rows after the down (scrubbed text is not restored)", a.rows(false), blanked)
	_, err = a.db.Exec(agesNoteBlankSQL, 101)
	agesNoteWantError(t, "blank after the down", err, "23514", agesNoteIdentityMsg)

	a.migrateTo(agesNoteTriggerTestVersion)
	if _, err := a.db.Exec(agesNoteBlankSQL, 101); err != nil {
		t.Fatalf("blank after up again: %v", err)
	}
	agesNoteSameRows(t, "rows after up again", a.rows(false), blanked, 101)
	if got := a.noteOf(101); got != "" {
		t.Fatalf("note of request 101 after the second blank = %q, want empty", got)
	}
}

// TestAgesNoteMigrationWaitsForExclusiveTableLock checks that the migration's
// lock timeout bounds its wait for a conflicting table lock, and that it can
// be applied after the holder releases that lock.
func TestAgesNoteMigrationWaitsForExclusiveTableLock(t *testing.T) {
	t.Parallel()
	name := testdb.Fresh(t, false)
	db := openSQL(t, name)
	ctx := context.Background()
	p, err := migrations.NewProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, agesNoteTriggerTestVersion-1); err != nil {
		t.Fatalf("up to %d: %v", agesNoteTriggerTestVersion-1, err)
	}
	holder, err := openSQL(t, name).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback()
	if _, err := holder.Exec(`LOCK TABLE contact_requests IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	probe, err := openSQL(t, name).Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	if _, err := probe.ExecContext(ctx, `SET lock_timeout = '200ms'`); err != nil {
		t.Fatal(err)
	}
	if _, err := probe.ExecContext(ctx, `SELECT count(*) FROM contact_requests`); pgErrorCode(err) != "55P03" {
		t.Fatalf("a read of contact_requests under the held lock: %v (code %q), want lock_not_available 55P03", err, pgErrorCode(err))
	}
	mctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	start := time.Now()
	_, err = p.UpTo(mctx, agesNoteTriggerTestVersion)
	elapsed := time.Since(start)
	if pgErrorCode(err) != "55P03" {
		t.Fatalf("UpTo(%d) with contact_requests locked ACCESS EXCLUSIVE: %v (code %q) after %s, want 55P03", agesNoteTriggerTestVersion, err, pgErrorCode(err), elapsed)
	}
	if err := holder.Rollback(); err != nil {
		t.Fatalf("release exclusive table lock: %v", err)
	}
	if _, err := p.UpTo(ctx, agesNoteTriggerTestVersion); err != nil {
		t.Fatalf("UpTo(%d) after releasing table lock: %v", agesNoteTriggerTestVersion, err)
	}
	if version, err := p.GetDBVersion(ctx); err != nil || version != agesNoteTriggerTestVersion {
		t.Fatalf("version after the migration = %d, %v, want %d", version, err, agesNoteTriggerTestVersion)
	}
}

// agesNotePreviousWriter plays the statement shapes the previous binary sends
// (create, decide, complete, a no-op save of a granted row, withdraw, expire,
// end on removal, and the changes the triggers must keep refusing) at one
// version and returns what each did, with the final rows. Every step names what
// it must do; the same script runs at both versions.
func agesNotePreviousWriter(t *testing.T, version int64) ([]string, map[int]string) {
	t.Helper()
	a := newAgesNoteDB(t, version)
	var out []string
	step := func(label string, err error, wantCode string) {
		t.Helper()
		got := "ok"
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			got = pgErr.Code + " " + pgErr.Message
		} else if err != nil {
			got = "error " + err.Error()
		}
		out = append(out, label+": "+got)
		if (wantCode == "" && got != "ok") || (wantCode != "" && !strings.HasPrefix(got, wantCode+" ")) {
			t.Errorf("version %d, %s: %s, want %q", version, label, got, wantCode)
		}
	}
	exec := func(label, wantCode, q string, args ...any) {
		t.Helper()
		_, err := a.db.Exec(q, args...)
		step(label, err, wantCode)
	}
	note := func(id int) string { return fmt.Sprintf("%s (%d)", agesNoteText, id) }
	a.register(150, 30, 3)
	a.register(151, 31, 3)
	for id := 200; id <= 206; id++ {
		subject := 3
		if id == 206 {
			subject = 4
		}
		a.addRequest(id, 1, subject, 30, "pending", "", note(id))
	}
	exec("decide (whole-row save)", "", `UPDATE contact_requests SET subject_decision='approved', subject_decided_by=3, subject_decided_at='2026-10-25 12:00Z',
		admin_decision='approved', admin_decided_by=1, admin_decided_at='2026-10-25 12:00Z', reason=reason, add_childcare_ages_note=add_childcare_ages_note WHERE id=200`)
	step("complete", a.complete(200, 150), "")
	exec("no-op save of a granted request", "", `UPDATE contact_requests SET reason = reason WHERE id = 200`)
	exec("change to a granted request", "23514", `UPDATE contact_requests SET reason = 'x' WHERE id = 200`)
	exec("withdraw", "", `UPDATE contact_requests SET status='withdrawn', withdrawn_by_id=1, completed_at='2026-10-25 12:00Z' WHERE id=201`)
	exec("expire", "", `UPDATE contact_requests SET status='expired', completed_at='2026-11-08 12:00Z' WHERE id=202`)
	exec("end on removal", "", `UPDATE contact_requests SET status='ended', end_reason='removed', completed_at='2026-10-25 12:00Z' WHERE id=203`)
	exec("blank of an ended row, living subject", "23514", agesNoteBlankSQL, 203)
	exec("blank of a pending row, living subject", "23514", agesNoteBlankSQL, 204)
	exec("note change on a pending row", "23514", `UPDATE contact_requests SET add_childcare_ages_note = 'changed' WHERE id = 204`)
	failed, err := a.runTx(false,
		agesNoteStep{label: "approve", q: `UPDATE contact_requests SET subject_decision='approved', subject_decided_by=3, subject_decided_at='2026-10-25 12:00Z',
			admin_decision='approved', admin_decided_by=1, admin_decided_at='2026-10-25 12:00Z' WHERE id=205`},
		agesNoteStep{label: "grant", q: `UPDATE contact_requests SET status='granted', completed_at='2026-10-25 12:00Z', completed_target_id=151 WHERE id=205`})
	out = append(out, "granted without its access: "+failed)
	step("granted without its access", err, "23514")
	a.tombstone(4)
	exec("end for a deleted subject", "", `UPDATE contact_requests SET status='ended', end_reason='account_disabled', completed_at='2026-10-25 12:00Z' WHERE id=206`)
	exec("whole-row save for a deleted subject", "", `UPDATE contact_requests SET reason = reason WHERE id = 206`)
	return out, a.rows(false)
}

// TestAgesNoteMigrationKeepsThePreviousWriterWorking is the DEC-138 previous-release
// check. The previous binary never sends a blank of the ages note, so at 00004
// every statement it can send behaves exactly as at the version before, with the
// same SQLSTATE and message, and leaves the same rows.
func TestAgesNoteMigrationKeepsThePreviousWriterWorking(t *testing.T) {
	t.Parallel()
	previousOut, previousRows := agesNotePreviousWriter(t, agesNoteTriggerTestVersion-1)
	currentOut, currentRows := agesNotePreviousWriter(t, agesNoteTriggerTestVersion)
	if p, c := strings.Join(previousOut, "\n"), strings.Join(currentOut, "\n"); p != c {
		t.Fatalf("the previous writer's outcomes differ at version %d:\n%s\nwant (version %d)\n%s", agesNoteTriggerTestVersion, c, agesNoteTriggerTestVersion-1, p)
	}
	agesNoteSameRows(t, "rows after the previous writer's script", currentRows, previousRows)
}

// agesNoteWatchLocks samples pg_locks for one backend until the returned function
// is called, which reports how many samples were taken and how many showed an
// ungranted lock. Sampling cannot prove "at no point"; it is evidence.
func agesNoteWatchLocks(ctx context.Context, db *sql.DB, pid int) func() (int, int) {
	stop := make(chan struct{})
	done := make(chan [2]int, 1)
	go func() {
		var res [2]int
		for {
			select {
			case <-stop:
				done <- res
				return
			default:
			}
			var n int
			if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pg_locks WHERE pid = $1 AND NOT granted`, pid).Scan(&n); err == nil {
				res[0]++
				if n > 0 {
					res[1]++
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	return func() (int, int) {
		close(stop)
		res := <-done
		return res[0], res[1]
	}
}

// TestAgesNoteTriggerLockOutcomes is CA-6 to CA-8 at the SQL level, with separate
// database/sql sessions. The licence takes FOR SHARE OF the tombstoned users row
// with NOWAIT. A holder with NO KEY UPDATE or FOR UPDATE makes the blank fail at
// once with 55P03 (M12 would wait instead); a holder with KEY SHARE or SHARE does
// not block it, and the share it takes then blocks a later NO KEY UPDATE until it
// ends; a living subject or an uncommitted deletion fails with 23514 without
// hanging. Waits are bounded with lock_timeout and real lock evidence is read
// from pg_locks and pg_blocking_pids.
func TestAgesNoteTriggerLockOutcomes(t *testing.T) {
	t.Parallel()
	a := newAgesNoteDB(t, agesNoteTriggerTestVersion)
	a.seedRows()
	a.tombstone(2)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	session := func(t *testing.T, label string) (*sql.Conn, int) {
		t.Helper()
		conn, err := a.db.Conn(ctx)
		if err != nil {
			t.Fatalf("%s session: %v", label, err)
		}
		t.Cleanup(func() { conn.Close() })
		var pid int
		if err := conn.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			t.Fatalf("%s session pid: %v", label, err)
		}
		return conn, pid
	}
	hold := func(t *testing.T, label, mode string) (*sql.Tx, int) {
		t.Helper()
		conn, pid := session(t, label)
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { tx.Rollback() })
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM users WHERE id = 2 `+mode).Scan(&one); err != nil {
			t.Fatalf("%s %s: %v", label, mode, err)
		}
		var granted int
		if err := a.db.QueryRowContext(ctx, `SELECT count(*) FROM pg_locks WHERE pid = $1 AND locktype = 'transactionid' AND granted`, pid).Scan(&granted); err != nil || granted < 1 {
			t.Fatalf("%s %s: its lock is not granted in pg_locks (%d rows, %v)", label, mode, granted, err)
		}
		return tx, pid
	}

	// CA-6: a holder with NO KEY UPDATE, and separately FOR UPDATE.
	for i, mode := range []string{"FOR NO KEY UPDATE", "FOR UPDATE"} {
		id := 101 + i
		t.Run("CA-6 "+mode, func(t *testing.T) {
			holder, _ := hold(t, "holder", mode)
			defer holder.Rollback()
			b, bPID := session(t, "blanker")
			if _, err := b.ExecContext(ctx, `SET lock_timeout = '2s'`); err != nil {
				t.Fatal(err)
			}
			stop := agesNoteWatchLocks(ctx, a.db, bPID)
			start := time.Now()
			_, err := b.ExecContext(ctx, agesNoteBlankSQL, id)
			elapsed := time.Since(start)
			samples, ungranted := stop()
			if pgErrorCode(err) != "55P03" {
				t.Fatalf("blank under %s: %v (code %q), want lock_not_available 55P03", mode, err, pgErrorCode(err))
			}
			if elapsed > 1500*time.Millisecond {
				t.Errorf("the blank took %s to fail, want well inside the 2s lock_timeout (it waited)", elapsed)
			}
			if ungranted != 0 {
				t.Errorf("%d of %d pg_locks samples showed an ungranted lock for the blanker", ungranted, samples)
			}
			t.Logf("CA-6 %s: 55P03 after %s, %d samples, %d ungranted", mode, elapsed, samples, ungranted)
			if got := a.noteOf(id); got == "" {
				t.Errorf("request %d was blanked although the blank failed", id)
			}
		})
	}

	// CA-7: a holder with KEY SHARE, and separately SHARE. The blank succeeds and
	// keeps a SHARE in its open transaction; the holder ends before the probe
	// starts, so the probe's NO KEY UPDATE waits on the blanker alone.
	for i, mode := range []string{"FOR KEY SHARE", "FOR SHARE"} {
		id := 101 + i
		t.Run("CA-7 "+mode, func(t *testing.T) {
			holder, _ := hold(t, "holder", mode)
			defer holder.Rollback()
			bConn, bPID := session(t, "blanker")
			btx, err := bConn.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer btx.Rollback()
			if _, err := btx.ExecContext(ctx, agesNoteBlankSQL, id); err != nil {
				t.Fatalf("blank under %s: %v, want it to succeed", mode, err)
			}
			var bXID string
			if err := btx.QueryRowContext(ctx, `SELECT pg_current_xact_id()::xid::text`).Scan(&bXID); err != nil {
				t.Fatal(err)
			}
			if err := holder.Rollback(); err != nil {
				t.Fatal(err)
			}

			dConn, dPID := session(t, "probe")
			dtx, err := dConn.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer dtx.Rollback()
			if _, err := dtx.ExecContext(ctx, `SET LOCAL lock_timeout = '15s'`); err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() {
				var one int
				result <- dtx.QueryRowContext(ctx, `SELECT 1 FROM users WHERE id = 2 FOR NO KEY UPDATE`).Scan(&one)
			}()
			want := fmt.Sprintf("{%d}", bPID)
			var blockers string
			deadline := time.Now().Add(10 * time.Second)
			for blockers != want && time.Now().Before(deadline) {
				if err := a.db.QueryRowContext(ctx, `SELECT pg_blocking_pids($1)::text`, dPID).Scan(&blockers); err != nil {
					t.Fatal(err)
				}
				select {
				case err := <-result:
					t.Fatalf("the probe returned (%v) before the blanker ended", err)
				case <-time.After(20 * time.Millisecond):
				}
			}
			if blockers != want {
				t.Fatalf("pg_blocking_pids(probe) = %s, want only the blanker %s", blockers, want)
			}
			var waiting int
			if err := a.db.QueryRowContext(ctx, `SELECT count(*) FROM pg_locks WHERE pid = $1 AND NOT granted AND locktype = 'transactionid' AND transactionid::text = $2`, dPID, bXID).Scan(&waiting); err != nil || waiting != 1 {
				t.Errorf("the probe has %d ungranted transaction-id locks on the blanker's xid %s (%v), want 1", waiting, bXID, err)
			}
			if err := btx.Commit(); err != nil {
				t.Fatalf("commit of the blank: %v", err)
			}
			select {
			case err := <-result:
				if err != nil {
					t.Fatalf("the probe after the blanker ended: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the probe still waits after the blanker ended")
			}
			if got := a.noteOf(id); got != "" {
				t.Errorf("request %d note after the committed blank = %q, want empty", id, got)
			}
		})
	}

	// CA-8: a living subject, and a deletion another session has not committed.
	// Both fail with 23514 at once; neither hangs.
	t.Run("CA-8 living subject and uncommitted deletion", func(t *testing.T) {
		b, _ := session(t, "blanker")
		if _, err := b.ExecContext(ctx, `SET lock_timeout = '2s'`); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		_, err := b.ExecContext(ctx, agesNoteBlankSQL, 130)
		agesNoteWantError(t, "living subject", err, "23514", agesNoteIdentityMsg)
		if time.Since(start) > 1500*time.Millisecond {
			t.Errorf("the living-subject refusal took %s", time.Since(start))
		}
		deleterConn, _ := session(t, "deleter")
		dtx, err := deleterConn.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer dtx.Rollback()
		if _, err := dtx.ExecContext(ctx, `UPDATE users SET deleted_at = now() WHERE id = 3`); err != nil {
			t.Fatal(err)
		}
		start = time.Now()
		_, err = b.ExecContext(ctx, agesNoteBlankSQL, 130)
		elapsed := time.Since(start)
		agesNoteWantError(t, "deletion not yet committed", err, "23514", agesNoteIdentityMsg)
		if elapsed > 1500*time.Millisecond {
			t.Errorf("the refusal under an uncommitted deletion took %s, want no wait", elapsed)
		}
		if got := a.noteOf(130); got == "" {
			t.Error("request 130 was blanked although the subject was not deleted")
		}
	})
}

// TestAgesNoteTriggerCoversAddedColumns shows that a column added after both
// functions compiled in a session is still compared (RC-3: for one int column
// here; a column type without an equality operator would fail closed and needs
// its own review). One session stays pinned for the whole test. It runs a blank
// that compiles both functions, a second session adds the column, and the first
// session then blanks (i) succeeds with the column still 0, (ii) a blank that
// also sets the column is refused with 23514 and writes nothing, and (iii) a
// granted row's blank still commits through the completion prefix. In (ii) the
// BEFORE trigger rejects first, so the completion comparison is not observed on
// its own there; it stays covered by the prefix fidelity in TestAgesNoteTriggerShape.
func TestAgesNoteTriggerCoversAddedColumns(t *testing.T) {
	t.Parallel()
	a := newAgesNoteDB(t, agesNoteTriggerTestVersion)
	for i, id := range []int{300, 301, 302} {
		a.addRequest(id, 1, 2, 30+i, "pending", "", fmt.Sprintf("%s (%d)", agesNoteText, id))
	}
	for i, id := range []int{310, 311} {
		a.register(320+i, 34+i, 2)
		a.addRequest(id, 1, 2, 34+i, "pending", "", fmt.Sprintf("%s (%d)", agesNoteText, id))
		if err := a.complete(id, 320+i); err != nil {
			t.Fatalf("grant request %d: %v", id, err)
		}
	}
	a.tombstone(2)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	conn1, err := a.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn1.Close()
	var pid1 int
	if err := conn1.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid1); err != nil {
		t.Fatal(err)
	}
	blankIn := func(tx *sql.Tx, ids ...int) error {
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, agesNoteBlankSQL, id); err != nil {
				return err
			}
		}
		return nil
	}
	inTx := func(f func(tx *sql.Tx) error) error {
		tx, err := conn1.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := f(tx); err != nil {
			return err
		}
		return tx.Commit()
	}

	// Compile both functions in this session, and release every lock.
	if err := inTx(func(tx *sql.Tx) error { return blankIn(tx, 300, 310) }); err != nil {
		t.Fatalf("first blanks on the pinned session: %v", err)
	}

	// Another session adds the column.
	conn2, err := a.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.Close()
	tx2, err := conn2.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx2.Rollback()
	if _, err := tx2.ExecContext(ctx, `SET LOCAL lock_timeout = '5s'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx2.ExecContext(ctx, `ALTER TABLE contact_requests ADD COLUMN t07a_probe int NOT NULL DEFAULT 0`); err != nil {
		t.Fatalf("add the probe column: %v", err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatal(err)
	}

	var pid int
	if err := conn1.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil || pid != pid1 {
		t.Fatalf("the pinned session is backend %d (%v), want %d", pid, err, pid1)
	}
	probeOf := func(id int) (note string, probe int) {
		t.Helper()
		if err := a.db.QueryRow(`SELECT add_childcare_ages_note, t07a_probe FROM contact_requests WHERE id = $1`, id).Scan(&note, &probe); err != nil {
			t.Fatal(err)
		}
		return
	}
	// (i) a plain blank after the DDL.
	if err := inTx(func(tx *sql.Tx) error { return blankIn(tx, 301) }); err != nil {
		t.Fatalf("(i) blank after the new column: %v", err)
	}
	if note, probe := probeOf(301); note != "" || probe != 0 {
		t.Errorf("(i) request 301 has note %q and probe %d, want empty and 0", note, probe)
	}
	// (ii) a blank that also sets the new column is refused and writes nothing.
	before, _ := probeOf(302)
	err = inTx(func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE contact_requests SET add_childcare_ages_note = '', t07a_probe = 1 WHERE id = 302`)
		return err
	})
	agesNoteWantError(t, "(ii) blank that also sets the new column", err, "23514", agesNoteIdentityMsg)
	if note, probe := probeOf(302); note != before || probe != 0 {
		t.Errorf("(ii) request 302 has note %q and probe %d after the refusal, want %q and 0", note, probe, before)
	}
	// (iii) the completion prefix after the DDL in the same session.
	if err := inTx(func(tx *sql.Tx) error { return blankIn(tx, 311) }); err != nil {
		t.Fatalf("(iii) blank of a granted row after the new column: %v", err)
	}
	if note, probe := probeOf(311); note != "" || probe != 0 {
		t.Errorf("(iii) request 311 has note %q and probe %d, want empty and 0", note, probe)
	}
}
