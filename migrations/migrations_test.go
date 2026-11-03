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
// The baseline Down is new and tested below: it refuses while a user exists,
// destroys nothing then, and on an empty database drops everything and Up
// restores it. The same disposition for the other test files is recorded in
// docs/plan/briefs/audit-2026-11/R9-baseline-squash.md (section 9, S2).

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"fold/internal/testdb"
	"fold/migrations"
	"fold/models"

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

func TestOccurrenceAccessDepartureOrigin(t *testing.T) {
	t.Parallel()
	db := openSQL(t, testdb.Fresh(t, true))
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	refuse := func(name, q string) {
		t.Helper()
		if _, err := db.Exec(q); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
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
		exec(q)
	}

	// Blocks govern new manager adds only. They do not invalidate standing
	// Maybe/Waitlisted registrations retained by an authorized departure.
	exec(`INSERT INTO occurrence_accesses(subject_user_id,occurrence_id,event_id,organizer_type,organizer_id,origin,
	 registration_id,departure_membership_id,departure_actor_id,granted_at)
	 VALUES(2,30,20,'group',10,'departure_self',40,50,2,'2026-10-26 12:00Z')`)
	exec(`INSERT INTO occurrence_accesses(subject_user_id,occurrence_id,event_id,organizer_type,organizer_id,origin,
	 registration_id,departure_membership_id,departure_actor_id,granted_at)
	 VALUES(2,31,20,'group',10,'departure_removal',41,50,1,'2026-10-26 12:00Z')`)

	// Existing manager rows receive the explicit default and keep their issuer
	// requirements. A block, teen direct add, and non-Going manager add fail.
	exec(`DELETE FROM manager_add_blocks WHERE subject_user_id=3`)
	exec(`INSERT INTO occurrence_accesses(subject_user_id,occurrence_id,event_id,organizer_type,organizer_id,issuer_id,registration_id)
	 VALUES(3,32,20,'group',10,1,42)`)
	refuse("manager add for Maybe", `INSERT INTO occurrence_accesses(subject_user_id,occurrence_id,event_id,organizer_type,organizer_id,issuer_id,registration_id)
	 VALUES(3,36,20,'group',10,1,46)`)
	var origin string
	if err := db.QueryRow(`SELECT origin FROM occurrence_accesses WHERE occurrence_id=32`).Scan(&origin); err != nil || origin != "manager_add" {
		t.Fatalf("manager origin default = %q, %v", origin, err)
	}
	exec(`INSERT INTO manager_add_blocks(subject_user_id,organizer_type,organizer_id) VALUES(3,'group',10)`)
	refuse("blocked manager add", `INSERT INTO occurrence_accesses(subject_user_id,occurrence_id,event_id,organizer_type,organizer_id,issuer_id,registration_id)
	 VALUES(3,33,20,'group',10,1,43)`)
	refuse("direct teen manager add", `INSERT INTO occurrence_accesses(subject_user_id,occurrence_id,event_id,organizer_type,organizer_id,issuer_id,registration_id)
	 VALUES(6,34,20,'group',10,1,44)`)
	refuse("manager origin without issuer", `INSERT INTO occurrence_accesses(subject_user_id,occurrence_id,event_id,organizer_type,organizer_id,registration_id)
	 VALUES(4,35,20,'group',10,45)`)

	for _, tt := range []struct{ name, sql string }{
		{"self leave by another actor", `INSERT INTO occurrence_accesses(subject_user_id,occurrence_id,event_id,organizer_type,organizer_id,origin,registration_id,departure_membership_id,departure_actor_id) VALUES(4,35,20,'group',10,'departure_self',45,54,1)`},
		{"removal by plain member", `INSERT INTO occurrence_accesses(subject_user_id,occurrence_id,event_id,organizer_type,organizer_id,origin,registration_id,departure_membership_id,departure_actor_id) VALUES(4,35,20,'group',10,'departure_removal',45,54,3)`},
		{"leader removes admin", `INSERT INTO occurrence_accesses(subject_user_id,occurrence_id,event_id,organizer_type,organizer_id,origin,registration_id,departure_membership_id,departure_actor_id) VALUES(4,35,20,'group',10,'departure_removal',45,54,1)`},
		{"wrong membership", `INSERT INTO occurrence_accesses(subject_user_id,occurrence_id,event_id,organizer_type,organizer_id,origin,registration_id,departure_membership_id,departure_actor_id) VALUES(4,35,20,'group',10,'departure_removal',45,52,4)`},
		{"departure forged issuer", `INSERT INTO occurrence_accesses(subject_user_id,occurrence_id,event_id,organizer_type,organizer_id,origin,issuer_id,registration_id,departure_membership_id,departure_actor_id) VALUES(4,35,20,'group',10,'departure_removal',1,45,54,4)`},
		{"departure forged request", `INSERT INTO occurrence_accesses(subject_user_id,occurrence_id,event_id,organizer_type,organizer_id,origin,request_id,registration_id,departure_membership_id,departure_actor_id) VALUES(4,35,20,'group',10,'departure_removal',60,45,54,4)`},
	} {
		t.Run(tt.name, func(t *testing.T) { refuse(tt.name, tt.sql) })
	}
	// An admin can authorize removal of an admin; this records the remover as
	// departure actor, never as a fictitious invitation issuer.
	exec(`INSERT INTO occurrence_accesses(subject_user_id,occurrence_id,event_id,organizer_type,organizer_id,origin,
	 registration_id,departure_membership_id,departure_actor_id)
	 VALUES(4,35,20,'group',10,'departure_removal',45,54,7)`)

	for _, mutation := range []string{
		`origin='manager_add'`, `departure_actor_id=4`, `departure_membership_id=52`, `issuer_id=1`,
	} {
		if _, err := db.Exec(`UPDATE occurrence_accesses SET ` + mutation + ` WHERE occurrence_id=30`); err == nil {
			t.Fatalf("immutable departure provenance changed: %s", mutation)
		}
	}
	refuse("second active access", `INSERT INTO occurrence_accesses(subject_user_id,occurrence_id,event_id,organizer_type,organizer_id,origin,registration_id,departure_membership_id,departure_actor_id) VALUES(2,30,20,'group',10,'departure_self',40,50,2)`)
}

// The end-state CHECK accepts occurrence_ended for a kept departure date and
// refuses the malformed shapes around it. Ending is final.
func TestDepartureOccurrenceAccessExpiryEndState(t *testing.T) {
	t.Parallel()
	db := openSQL(t, testdb.Fresh(t, true))
	exec := func(q string) {
		t.Helper()
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for _, q := range []string{
		`INSERT INTO users(id,email,status,role) VALUES(1,'expiry-actor@example.com','active','admin'),(2,'expiry-subject@example.com','active','member')`,
		`INSERT INTO groups(id,name,leader_id,status,visibility) VALUES(10,'Expiry group',1,'active','invite_only')`,
		`INSERT INTO memberships(id,user_id,group_id,role,status) VALUES(50,2,10,'member','active')`,
		`INSERT INTO events(id,group_id,title,description,starts_local,duration_minutes,tzid,rrule,location,visibility,created_by) VALUES(20,10,'Expiry','','2026-12-01T18:00',60,'UTC','','','invite_only',1)`,
		`INSERT INTO event_occurrences(id,event_id,original_date,starts_at,ends_at,status) VALUES(30,20,'2026-12-01','2026-12-01 18:00Z','2026-12-01 19:00Z','scheduled')`,
		`INSERT INTO registrations(id,occurrence_id,user_id,status,source) VALUES(40,30,2,'going','web')`,
		`INSERT INTO occurrence_accesses(subject_user_id,occurrence_id,event_id,organizer_type,organizer_id,origin,registration_id,departure_membership_id,departure_actor_id,granted_at) VALUES(2,30,20,'group',10,'departure_self',40,50,2,'2026-10-26 12:00Z')`,
	} {
		exec(q)
	}
	for _, tt := range []struct{ name, mutation string }{
		{"expiry with an actor", `status='ended',ended_at='2026-12-01 19:00Z',end_reason='occurrence_ended',ended_by=2`},
		{"expiry before the grant", `status='ended',ended_at='2026-10-25 12:00Z',end_reason='occurrence_ended'`},
		{"ended without a time", `status='ended',end_reason='occurrence_ended'`},
		{"reason on a live row", `end_reason='occurrence_ended'`},
		{"unknown reason", `status='ended',ended_at='2026-12-01 19:00Z',end_reason='expired'`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := db.Exec(`UPDATE occurrence_accesses SET ` + tt.mutation + ` WHERE occurrence_id=30`); err == nil {
				t.Fatal("malformed end state accepted")
			}
		})
	}
	exec(`UPDATE occurrence_accesses SET status='ended',ended_at='2026-12-01 19:00Z',end_reason='occurrence_ended' WHERE occurrence_id=30`)
	if _, err := db.Exec(`UPDATE occurrence_accesses SET status='active',ended_at=NULL,end_reason='' WHERE occurrence_id=30`); err == nil {
		t.Fatal("an ended departure date came back to life")
	}
}

// A manager grant superseded by a departure keeps its manager provenance and
// ends with the dedicated reason and an actor.
func TestDepartureAccessSupersessionEndState(t *testing.T) {
	t.Parallel()
	db := openSQL(t, testdb.Fresh(t, true))
	exec := func(q string) {
		t.Helper()
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for _, q := range []string{
		`INSERT INTO users(id,email,status,role) VALUES(1,'supersede-issuer@example.com','active','member'),(2,'supersede-subject@example.com','active','member')`,
		`INSERT INTO groups(id,name,leader_id,status,visibility) VALUES(10,'Supersede group',1,'active','invite_only')`,
		`INSERT INTO memberships(id,user_id,group_id,role,status) VALUES(50,1,10,'leader','active'),(51,2,10,'member','active')`,
		`INSERT INTO events(id,group_id,title,description,starts_local,duration_minutes,tzid,rrule,location,visibility,created_by) VALUES(20,10,'Supersede','','2026-12-01T18:00',60,'UTC','','','invite_only',1)`,
		`INSERT INTO event_occurrences(id,event_id,original_date,starts_at,ends_at,status) VALUES(30,20,'2026-12-01','2026-12-01 18:00Z','2026-12-01 19:00Z','scheduled')`,
		`INSERT INTO registrations(id,occurrence_id,user_id,status,source) VALUES(40,30,2,'going','admin')`,
		`INSERT INTO occurrence_accesses(id,subject_user_id,occurrence_id,event_id,organizer_type,organizer_id,issuer_id,registration_id,granted_at) VALUES(60,2,30,20,'group',10,1,40,'2026-11-01 12:00Z')`,
	} {
		exec(q)
	}
	for _, tt := range []struct{ name, mutation string }{
		{"superseded without an actor", `status='ended',ended_at='2026-11-02 12:00Z',end_reason='departure_superseded'`},
		{"superseded before the grant", `status='ended',ended_at='2026-10-31 12:00Z',end_reason='departure_superseded',ended_by=2`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := db.Exec(`UPDATE occurrence_accesses SET ` + tt.mutation + ` WHERE id=60`); err == nil {
				t.Fatal("malformed supersession accepted")
			}
		})
	}
	exec(`UPDATE occurrence_accesses SET status='ended',ended_at='2026-11-02 12:00Z',end_reason='departure_superseded',ended_by=2 WHERE id=60`)
	var origin string
	var issuer, registration int64
	if err := db.QueryRow(`SELECT origin,issuer_id,registration_id FROM occurrence_accesses WHERE id=60`).Scan(&origin, &issuer, &registration); err != nil || origin != "manager_add" || issuer != 1 || registration != 40 {
		t.Fatalf("manager provenance changed: origin=%q issuer=%d registration=%d err=%v", origin, issuer, registration, err)
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

// TestMigrateEmptyDatabase applies the baseline to a database with nothing in
// it. The stored bookkeeping must be goose's version 0 row and version 1
// applied: `migrate status` alone cannot tell this from an old-chain database.
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
	if version, err := p.GetDBVersion(ctx); err != nil || version != 1 {
		t.Fatalf("version after up = %d, %v, want 1", version, err)
	}
	if got := appliedRows(t, db); got != "0:true,1:true" {
		t.Fatalf("goose_db_version = %s, want exactly version 0 and version 1 applied", got)
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
	db := openSQL(t, testdb.Fresh(t, true))
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for _, q := range []string{
		`INSERT INTO users(id,email,status) VALUES(1,'issuer@example.com','active'),(2,'recipient@example.com','active'),(3,'other@example.com','active')`,
		`INSERT INTO groups(id,name,leader_id,status,visibility) VALUES(10,'Hidden',1,'active','invite_only')`,
		`INSERT INTO events(id,group_id,title,description,starts_local,duration_minutes,tzid,rrule,location,visibility,created_by)
		 VALUES(20,10,'Hidden series','','2026-12-01T18:00',60,'UTC','','','invite_only',1)`,
		`INSERT INTO event_occurrences(id,event_id,original_date,starts_at,ends_at,status)
		 VALUES(30,20,'2026-12-01','2026-12-01 18:00Z','2026-12-01 19:00Z','scheduled'),
		 (31,20,'2026-12-02','2026-12-02 18:00Z','2026-12-02 19:00Z','scheduled')`,
		`INSERT INTO registrations(id,occurrence_id,user_id,status,source) VALUES(40,30,2,'going','admin'),(41,31,2,'going','admin')`,
		`INSERT INTO memberships(id,user_id,group_id,role,status) VALUES(50,2,10,'member','active')`,
	} {
		exec(q)
	}
	requestCreated := "2026-10-24 12:00Z"
	insertRequest := func(purpose string) int {
		t.Helper()
		var id int
		contextType, contextID, mode := "event", 20, "in_person"
		var occurrence any = 30
		if purpose == "group_add" {
			contextType, contextID, mode, occurrence = "group", 10, "", nil
		}
		err := db.QueryRow(`INSERT INTO contact_requests(requester_id,subject_user_id,context_type,context_id,fields,purpose,occurrence_id,
		 add_attendance_mode,needs_admin,admin_decision,created_at,expires_at)
		 VALUES(1,2,$1,$2,'',$3,$4,$5,true,'pending',$6::timestamptz,$6::timestamptz + interval '14 days') RETURNING id`, contextType, contextID, purpose, occurrence, mode, requestCreated).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
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
	approveSQL := `UPDATE contact_requests SET subject_decision='approved',subject_decided_by=2,subject_decided_at='2026-10-25 12:00Z',
	 admin_decision='approved',admin_decided_by=1,admin_decided_at='2026-10-25 12:00Z' WHERE id=$1`
	exec(approveSQL, id)
	if _, err := db.Exec(`INSERT INTO contact_grants(request_id,leader_id,subject_user_id,context_type,context_id,fields,granted_at)
	 VALUES($1,1,2,'event',20,'','2026-10-25 12:00Z')`, id); err == nil {
		t.Fatal("add created contact grant")
	}
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
	complete := func(request int, accessSQL string) error {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err = tx.Exec(approveSQL, request); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE contact_requests SET status='granted',completed_at='2026-10-25 12:00Z',completed_target_id=40 WHERE id=$1`, request); err != nil {
			return err
		}
		if _, err = tx.Exec(accessSQL, request); err != nil {
			return err
		}
		return tx.Commit()
	}
	accessSQL := `INSERT INTO occurrence_accesses(subject_user_id,occurrence_id,event_id,organizer_type,organizer_id,issuer_id,request_id,registration_id,granted_at)
	 VALUES(2,30,20,'group',10,1,$1,40,'2026-10-25 12:00Z')`
	for _, mutation := range []struct{ old, new string }{
		{"2,30,20", "3,30,20"}, {"2,30,20", "2,31,20"}, {"2,30,20", "2,30,21"},
		{"'group',10", "'group',11"}, {"10,1,$1", "10,3,$1"}, {"$1,40", "$1,41"},
	} {
		r := insertRequest("occurrence_add")
		if err := complete(r, strings.Replace(accessSQL, mutation.old, mutation.new, 1)); err == nil {
			t.Fatalf("mismatched provenance accepted: %s", mutation.new)
		}
	}
	request := insertRequest("occurrence_add")
	exec(`UPDATE users SET account_type='teen',birth_date='2010-01-01' WHERE id=2`)
	if _, err := db.Exec(strings.Replace(accessSQL, "$1", "NULL", 1)); err == nil {
		t.Fatal("direct teen manager add accepted")
	}
	if err := complete(request, accessSQL); err != nil {
		t.Fatal(err)
	}
	if err := complete(insertRequest("occurrence_add"), accessSQL); err == nil {
		t.Fatal("second live subject/date invitation accepted")
	}
	for _, mutation := range []string{
		`end_reason='removed'`, `status='ended'`,
		`status='ended',ended_at='2026-10-24 12:00Z',end_reason='declined',ended_by=2`,
		`status='ended',ended_at='2026-10-26 12:00Z',end_reason='declined',ended_by=1`,
		`status='ended',ended_at='2026-10-26 12:00Z',end_reason='unknown'`,
		`status='ended',ended_at='2026-10-26 12:00Z',end_reason='account_disabled',ended_by=1`,
	} {
		if _, err := db.Exec("UPDATE occurrence_accesses SET " + mutation + " WHERE occurrence_id=30"); err == nil {
			t.Fatalf("malformed access end accepted: %s", mutation)
		}
	}
	if _, err := db.Exec(`INSERT INTO contact_grants(request_id,leader_id,subject_user_id,context_type,context_id,fields,granted_at)
	 VALUES($1,1,2,'event',20,'','2026-10-25 12:00Z')`, request); err == nil {
		t.Fatal("approved add created ContactGrant")
	}
	for _, mutation := range []string{`issuer_id=3`, `occurrence_id=31`, `request_id=NULL`, `registration_id=41`, `granted_at=granted_at+interval '1 second'`} {
		if _, err := db.Exec("UPDATE occurrence_accesses SET " + mutation + " WHERE occurrence_id=30"); err == nil {
			t.Fatalf("immutable provenance changed: %s", mutation)
		}
	}
	// Other accepted dates survive organizer block; new dates cannot be added.
	exec(`UPDATE users SET account_type='adult' WHERE id=2`)
	exec(`INSERT INTO occurrence_accesses(subject_user_id,occurrence_id,event_id,organizer_type,organizer_id,issuer_id,registration_id)
	 VALUES(2,31,20,'group',10,1,41)`)
	staleAsk := insertRequest("occurrence_add")
	exec(`UPDATE occurrence_accesses SET status='ended',ended_at='2026-10-25 12:00Z',end_reason='declined',ended_by=2 WHERE occurrence_id=30`)
	if _, err := db.Exec(strings.Replace(accessSQL, "$1", "NULL", 1)); err == nil {
		t.Fatal("direct re-add after end accepted")
	}
	if err := complete(staleAsk, accessSQL); err == nil {
		t.Fatal("pending ask predating removal resurrected access")
	}
	requestCreated = "2026-10-25 12:00Z"
	freshAsk := insertRequest("occurrence_add")
	if err := complete(freshAsk, accessSQL); err != nil {
		t.Fatalf("genuinely new approved ask refused: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM occurrence_accesses WHERE occurrence_id=30`); err == nil {
		t.Fatal("retained invitation history deleted")
	}
	exec(`INSERT INTO manager_add_blocks(subject_user_id,organizer_type,organizer_id) VALUES(2,'group',10)`)
	exec(`INSERT INTO event_occurrences(id,event_id,original_date,starts_at,ends_at,status)
	 VALUES(32,20,'2026-12-03','2026-12-03 18:00Z','2026-12-03 19:00Z','scheduled')`)
	exec(`INSERT INTO registrations(id,occurrence_id,user_id,status,source) VALUES(42,32,2,'going','admin')`)
	if _, err := db.Exec(`INSERT INTO occurrence_accesses(subject_user_id,occurrence_id,event_id,organizer_type,organizer_id,issuer_id,registration_id)
	 VALUES(2,32,20,'group',10,1,42)`); err == nil {
		t.Fatal("new date add bypassed organizer block")
	}
	exec(`UPDATE occurrence_accesses SET status='ended',ended_at='2026-10-26 12:00Z',end_reason='blocked',ended_by=2 WHERE occurrence_id=30 AND status='active'`)
	if _, err := db.Exec(`UPDATE occurrence_accesses SET status='active',ended_at=NULL,end_reason='',ended_by=NULL WHERE occurrence_id=30`); err == nil {
		t.Fatal("access tombstone resurrected")
	}
	if _, err := db.Exec(strings.Replace(accessSQL, "$1", "NULL", 1)); err == nil {
		t.Fatal("duplicate/readded ended date accepted")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM occurrence_accesses WHERE status='active' AND occurrence_id=31`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("block ended unrelated accepted date: %d %v", count, err)
	}
	// History survives actual hard deletion, independent of live-domain joins.
	for _, q := range []string{`DELETE FROM registrations`, `DELETE FROM event_occurrences`, `DELETE FROM events`, `DELETE FROM memberships`, `DELETE FROM groups`, `DELETE FROM users`} {
		exec(q)
	}
	if err := db.QueryRow(`SELECT count(*) FROM occurrence_accesses`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("access history lost: %d %v", count, err)
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
	if version, err := p.GetDBVersion(ctx); err != nil || version != 1 {
		t.Fatalf("version after up = %d, %v, want 1", version, err)
	}
	if got := publicTableCount(t, db); got != tables {
		t.Fatalf("tables after up = %d, want %d", got, tables)
	}
	if changes, err := Drift(gdb, models.All()...); err != nil || len(changes) > 0 {
		t.Fatalf("the restored schema drifts from the models: %v %v", changes, err)
	}
}

// TestBaselineFileShape pins what the generator guarantees about the file
// (R9 section 5.2 and 5.4). During the DEC-138 window the baseline is the only
// migration; the first change after the window closes is 00002, and this test
// gets a clause for it then.
func TestBaselineFileShape(t *testing.T) {
	t.Parallel()
	entries, err := migrations.Files.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var sqlFiles []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			sqlFiles = append(sqlFiles, e.Name())
		}
	}
	if len(sqlFiles) != 1 || sqlFiles[0] != "00001_baseline.sql" {
		t.Fatalf("migrations = %v, want exactly 00001_baseline.sql until the DEC-138 window closes", sqlFiles)
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
