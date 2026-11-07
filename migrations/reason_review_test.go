package migrations_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"fold/migrations"
)

const (
	reasonReviewTriggerTestVersion = 7
	reasonReviewMigrationFile      = "00007_reason_review_erasure_scrub.sql"
	reasonReviewBlankSQL           = `UPDATE contact_requests SET reason = '' WHERE id = $1`
)

// Build every fixture with the preceding schema and the previous writer's
// statements. 200-205 are occurrence_add and 210-215 group_add, each in status
// order pending, denied, withdrawn, expired, ended, granted. User 1 requests for
// user 2. Controls 300-303 are terminal rows with living or missing parties.
func newReasonReviewDB(t *testing.T, version int64) agesNoteDB {
	t.Helper()
	a := newAgesNoteDB(t, reasonReviewTriggerTestVersion-1)
	for kind, purpose := range []string{"occurrence_add", "group_add"} {
		for i, status := range []string{"pending", "denied", "withdrawn", "expired", "ended", "granted"} {
			id := 200 + kind*10 + i
			initial := status
			if initial == "granted" {
				initial = "pending"
			}
			reason := fmt.Sprintf("Reason for request %d", id)
			if purpose == "occurrence_add" {
				a.addRequest(id, 1, 2, 40+i, initial, reason, agesNoteText)
				if status == "granted" {
					a.register(505, 45, 2)
					if err := a.complete(id, 505); err != nil {
						t.Fatalf("complete occurrence request: %v", err)
					}
				}
				continue
			}
			a.exec(fmt.Sprintf(`INSERT INTO contact_requests(id,requester_id,subject_user_id,context_type,context_id,fields,reason,needs_admin,
				subject_decision,subject_decided_by,subject_decided_at,completed_at,withdrawn_by_id,end_reason,
				admin_decision,status,created_at,expires_at,purpose)
				VALUES(%d,1,2,'group',10,'',$1,true,%s,'pending','%s','2026-10-24 12:00Z','2026-11-07 12:00Z','group_add')`,
				id, agesNoteOutcome(initial, 1, 2), initial), reason)
			if status == "granted" {
				a.exec(`INSERT INTO memberships(id,user_id,group_id,role,status) VALUES(600,2,10,'member','active')`)
				a.exec(`UPDATE contact_requests SET subject_decision='approved',subject_decided_by=2,subject_decided_at='2026-10-25 12:00Z',
					admin_decision='approved',admin_decided_by=1,admin_decided_at='2026-10-25 12:00Z',
					status='granted',completed_at='2026-10-26 12:00Z',completed_target_id=600 WHERE id=$1`, id)
			}
		}
	}
	a.addRequest(300, 1, 3, 30, "denied", "Living parties", agesNoteText)
	a.addRequest(301, 98, 99, 31, "denied", "Missing parties", agesNoteText)
	a.addRequest(302, 3, 4, 32, "denied", "Another living pair", agesNoteText)
	a.exec(`INSERT INTO contact_requests(id,requester_id,subject_user_id,context_type,context_id,fields,reason,needs_admin,
		subject_decision,subject_decided_by,subject_decided_at,admin_decision,status,created_at,expires_at,completed_at,purpose)
		VALUES(303,3,4,'group',10,'','Living group pair',true,'denied',4,'2026-10-25 12:00Z','pending','denied',
		'2026-10-24 12:00Z','2026-11-07 12:00Z','2026-10-25 12:00Z','group_add')`)
	if version != reasonReviewTriggerTestVersion-1 {
		a.migrateTo(version)
	}
	return a
}

// Compare whole rows with only reason excluded, so a blank cannot silently
// change decisions, provenance, the ages note or a future equality-capable column.
func reasonReviewRows(t *testing.T, db *sql.DB) map[int]string {
	t.Helper()
	rs, err := db.Query(`SELECT id, (to_jsonb(c) - 'reason')::text FROM contact_requests c`)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	out := map[int]string{}
	for rs.Next() {
		var id int
		var doc string
		if err := rs.Scan(&id, &doc); err != nil {
			t.Fatal(err)
		}
		out[id] = doc
	}
	if err := rs.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func reasonReviewReason(t *testing.T, db *sql.DB, id int) string {
	t.Helper()
	var reason string
	if err := db.QueryRow(`SELECT reason FROM contact_requests WHERE id=$1`, id).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	return reason
}

func reasonReviewAssertUpdate(t *testing.T, tx *sql.Tx, result sql.Result, request int, want string) {
	t.Helper()
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("request %d affected %d: %v", request, n, err)
	}
	var got string
	if err := tx.QueryRow(`SELECT reason FROM contact_requests WHERE id=$1`, request).Scan(&got); err != nil || got != want {
		t.Fatalf("request %d reason=%q, want %q: %v", request, got, want, err)
	}
}

func TestReasonReviewTriggerAllowsOnlyTheErasureBlank(t *testing.T) {
	t.Parallel()
	for _, party := range []int{1, 2} {
		t.Run(fmt.Sprintf("deleted party %d", party), func(t *testing.T) {
			a := newReasonReviewDB(t, reasonReviewTriggerTestVersion)
			a.tombstone(party)
			before := a.rows(false)
			for _, tc := range []struct{ name, q string }{
				{"living occurrence pair", `UPDATE contact_requests SET reason='' WHERE id=302`},
				{"living group pair", `UPDATE contact_requests SET reason='' WHERE id=303`},
				{"missing parties", `UPDATE contact_requests SET reason='' WHERE id=301`},
				{"nonempty occurrence replacement", `UPDATE contact_requests SET reason='replacement' WHERE id=201`},
				{"nonempty group replacement", `UPDATE contact_requests SET reason='replacement' WHERE id=211`},
				{"reason and ages note", `UPDATE contact_requests SET reason='',add_childcare_ages_note='' WHERE id=201`},
				{"reason and requester", `UPDATE contact_requests SET reason='',requester_id=3 WHERE id=201`},
				{"reason and subject", `UPDATE contact_requests SET reason='',subject_user_id=3,subject_decided_by=3 WHERE id=201`},
				{"reason and admin decision", `UPDATE contact_requests SET reason='',admin_decision='approved',admin_decided_by=1,admin_decided_at='2026-10-25 12:00Z' WHERE id=201`},
				{"reason and nullable column", `UPDATE contact_requests SET reason='',completed_target_id=600 WHERE id=201`},
				{"reason and expiry", `UPDATE contact_requests SET reason='',expires_at=expires_at+interval '1 day' WHERE id=201`},
				{"reason and group outcome", `UPDATE contact_requests SET reason='',status='expired',completed_at=expires_at WHERE id=211`},
				{"reason and group options", `UPDATE contact_requests SET reason='',add_over_limit=true WHERE id=211`},
			} {
				_, err := a.db.Exec(tc.q)
				agesNoteWantError(t, tc.name, err, "23514", agesNoteIdentityMsg)
			}
			agesNoteSameRows(t, "refused changes", a.rows(false), before)

			stripped := reasonReviewRows(t, a.db)
			memberships, registrations, audit := a.digest("memberships"), a.digest("registrations"), a.digest("audit_logs")
			var blanked []int
			for _, base := range []int{200, 210} {
				for i := 0; i < 6; i++ {
					id := base + i
					res, err := a.db.Exec(reasonReviewBlankSQL, id)
					if err != nil {
						t.Fatalf("blank request %d: %v", id, err)
					}
					if n, err := res.RowsAffected(); n != 1 || err != nil {
						t.Fatalf("blank request %d affected %d rows: %v", id, n, err)
					}
					if got := reasonReviewReason(t, a.db, id); got != "" {
						t.Errorf("request %d reason=%q, want empty", id, got)
					}
					blanked = append(blanked, id)
				}
			}
			agesNoteSameRows(t, "only reason changed", reasonReviewRows(t, a.db), stripped)
			agesNoteSameRows(t, "unchosen requests", a.rows(false), before, blanked...)
			if a.digest("memberships") != memberships || a.digest("registrations") != registrations || a.digest("audit_logs") != audit {
				t.Error("the database exception changed a completion or audit row")
			}
			_, err := a.db.Exec(`UPDATE contact_requests SET reason='stale text' WHERE id=211`)
			agesNoteWantError(t, "restore blanked terminal reason", err, "23514", agesNoteIdentityMsg)
		})
	}
}

// Contact and pending add reasons were already mutable. Compare those exact
// statement outcomes and final rows across the expansion; no new restriction on
// these writers is part of the schema slice.
func reasonReviewPreviousWriter(t *testing.T, version int64) ([]string, map[int]string) {
	t.Helper()
	a := newReasonReviewDB(t, version)
	a.exec(`INSERT INTO contact_requests(id,requester_id,subject_user_id,context_type,context_id,fields,reason,needs_admin,admin_decision,created_at,expires_at)
		VALUES(400,1,3,'group',10,'email','Contact reason',false,'not_needed','2026-10-24 12:00Z','2026-11-07 12:00Z')`)
	var out []string
	for _, id := range []int{200, 210, 400} {
		for _, text := range []string{"replacement", "", "restored"} {
			_, err := a.db.Exec(`UPDATE contact_requests SET reason=$2 WHERE id=$1`, id, text)
			if err != nil {
				t.Fatalf("previous reason writer at version %d: %v", version, err)
			}
			out = append(out, fmt.Sprintf("%d: %s", id, reasonReviewReason(t, a.db, id)))
		}
	}
	a.exec(`UPDATE contact_requests SET reason='',status='withdrawn',withdrawn_by_id=1,completed_at='2026-10-25 12:00Z' WHERE id=200`)
	// A no-op of a valid granted row still rechecks the completion.
	a.exec(`UPDATE contact_requests SET reason=reason WHERE id=215`)
	return out, a.rows(false)
}

func TestReasonReviewMigrationKeepsThePreviousWriterWorking(t *testing.T) {
	t.Parallel()
	for _, writer := range []struct {
		name string
		run  func(*testing.T, int64) ([]string, map[int]string)
	}{{"request and lifecycle", agesNotePreviousWriter}, {"mutable reasons", reasonReviewPreviousWriter}} {
		t.Run(writer.name, func(t *testing.T) {
			previousOut, previousRows := writer.run(t, reasonReviewTriggerTestVersion-1)
			currentOut, currentRows := writer.run(t, reasonReviewTriggerTestVersion)
			if strings.Join(previousOut, "\n") != strings.Join(currentOut, "\n") {
				t.Errorf("previous writer outcomes differ:\n%v\nwant\n%v", currentOut, previousOut)
			}
			agesNoteSameRows(t, "previous writer rows", currentRows, previousRows)
		})
	}
}

func TestReasonReviewTriggerHistoricalCompletion(t *testing.T) {
	t.Parallel()
	for _, historical := range []string{"replaced", "deleted"} {
		for _, immediate := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s immediate=%v", historical, immediate), func(t *testing.T) {
				a := newReasonReviewDB(t, reasonReviewTriggerTestVersion)
				a.endAccess(505, "2026-10-27 12:00Z")
				a.recordHistory(205, 505, 2, 45, "2026-10-27 12:00Z")
				if historical == "deleted" {
					a.exec(`DELETE FROM registrations WHERE id=505`)
				} else {
					a.pendingAt(400, 1, 2, 45, "2026-10-28 12:00Z", agesNoteText)
					if err := a.complete(400, 505); err != nil {
						t.Fatal(err)
					}
				}
				a.tombstone(2)
				for _, tc := range []struct {
					id  int
					msg string
				}{{205, agesNoteCompletionMsg}, {215, "group add requires completed membership"}} {
					_, err := a.db.Exec(`UPDATE contact_requests SET reason=reason WHERE id=$1`, tc.id)
					agesNoteWantError(t, "ordinary completion recheck", err, "23514", tc.msg)
					before := reasonReviewRows(t, a.db)
					failed, err := a.runTx(immediate, agesNoteStep{label: "blank", q: reasonReviewBlankSQL, args: []any{tc.id}})
					if err != nil {
						t.Fatalf("reason-only blank failed at %s: %v", failed, err)
					}
					if reasonReviewReason(t, a.db, tc.id) != "" {
						t.Error("the reason was not blanked")
					}
					agesNoteSameRows(t, "historical blank preserves every other column", reasonReviewRows(t, a.db), before)
				}
			})
		}
	}
}

func TestReasonReviewTriggerDoesNotHideEarlierDeferredEvents(t *testing.T) {
	t.Parallel()
	for _, immediate := range []bool{false, true} {
		t.Run(fmt.Sprintf("immediate=%v", immediate), func(t *testing.T) {
			a := newReasonReviewDB(t, reasonReviewTriggerTestVersion)
			a.tombstone(2)
			before := a.rows(false)
			steps := []agesNoteStep{
				{label: "approve", q: `UPDATE contact_requests SET subject_decision='approved',subject_decided_by=2,subject_decided_at='2026-10-25 12:00Z',
					admin_decision='approved',admin_decided_by=1,admin_decided_at='2026-10-25 12:00Z' WHERE id=200`},
				{label: "forged grant", q: `UPDATE contact_requests SET status='granted',completed_at='2026-10-26 12:00Z',completed_target_id=999 WHERE id=200`},
				{label: "blank", q: reasonReviewBlankSQL, args: []any{200}},
			}
			failed, err := a.runTx(immediate, steps...)
			want := "COMMIT"
			if immediate {
				want = "forged grant"
			}
			if failed != want {
				t.Fatalf("failed at %q: %v, want %s", failed, err, want)
			}
			agesNoteWantError(t, "forged grant then blank", err, "23514", agesNoteCompletionMsg)
			agesNoteSameRows(t, "failed transaction", a.rows(false), before)
		})
	}
}

// The adopted branches are checked separately from runtime behavior. Removing
// the completion comparison or OLD.status guard can be behaviorally equivalent
// with the identity trigger installed, so those changes need a shape assertion.
const (
	reasonReviewIdentityBranch  = `IF TG_OP = 'UPDATE' AND OLD.purpose <> 'contact' AND OLD.status <> 'pending' AND OLD.reason <> '' AND NEW.reason = '' THEN v := NEW; v.reason := OLD.reason; IF v IS NOT DISTINCT FROM OLD THEN PERFORM 1 FROM public.users u WHERE u.id IN (OLD.requester_id, OLD.subject_user_id) AND u.deleted_at IS NOT NULL FOR SHARE OF u NOWAIT; IF FOUND THEN RETURN NEW; END IF; END IF; END IF;`
	reasonReviewCompletedBranch = `IF TG_OP = 'UPDATE' AND OLD.purpose IN ('group_add', 'occurrence_add') AND OLD.status = 'granted' AND OLD.reason <> '' AND NEW.reason = '' THEN v := NEW; v.reason := OLD.reason; IF v IS NOT DISTINCT FROM OLD THEN RETURN NULL; END IF; END IF;`
)

func reasonReviewFidelity(t *testing.T, previous, current, anchor, branch string) {
	t.Helper()
	i, j := strings.Index(previous, anchor), strings.Index(current, anchor)
	if i < 0 || j < 0 {
		t.Fatalf("missing previous statement %q", anchor)
	}
	prefix := previous[:i]
	if !strings.HasPrefix(current, prefix) || current[j:] != previous[i:] {
		t.Error("the preexisting body, including the ages-note branch, changed")
		return
	}
	if got := agesNoteCollapse(current[len(prefix):j]); got != branch {
		t.Errorf("added branch:\n%s\nwant\n%s", got, branch)
	}
}

func reasonReviewFunctionDefs(t *testing.T, db *sql.DB) string {
	t.Helper()
	return agesNoteLines(t, db, `SELECT pg_get_functiondef(oid) FROM pg_proc
		WHERE oid IN ('public.add_request_identity()'::regprocedure,'public.add_request_completed()'::regprocedure) ORDER BY proname`)
}

func TestReasonReviewMigrationShapeDownAndUp(t *testing.T) {
	t.Parallel()
	a := newReasonReviewDB(t, reasonReviewTriggerTestVersion-1)
	a.tombstone(2)
	atFour := newAgesNoteDB(t, agesNoteTriggerTestVersion)
	previousDefs := reasonReviewFunctionDefs(t, a.db)
	if previousDefs != reasonReviewFunctionDefs(t, atFour.db) {
		t.Fatal("the immediate predecessor no longer has 00004's function definitions; re-review the migration")
	}
	previous := agesNoteShape(t, a.db)
	before := a.rows(false)
	// Terminal blanking is refused before the expansion, for both add purposes.
	for _, id := range []int{201, 211} {
		_, err := a.db.Exec(reasonReviewBlankSQL, id)
		agesNoteWantError(t, "previous schema", err, "23514", agesNoteIdentityMsg)
	}
	indexes := agesNoteLines(t, a.db, `SELECT indexdef FROM pg_indexes WHERE schemaname='public' ORDER BY tablename,indexname`)
	a.migrateTo(reasonReviewTriggerTestVersion)
	current := agesNoteShape(t, a.db)
	currentDefs := reasonReviewFunctionDefs(t, a.db)
	agesNoteSameShape(t, "expanded schema", current, previous, false)
	reasonReviewFidelity(t, previous.identitySrc, current.identitySrc, "    IF NEW.purpose <> OLD.purpose", reasonReviewIdentityBranch)
	reasonReviewFidelity(t, previous.completedSrc, current.completedSrc, "    IF NEW.purpose = 'group_add'", reasonReviewCompletedBranch)
	if got := agesNoteLines(t, a.db, `SELECT indexdef FROM pg_indexes WHERE schemaname='public' ORDER BY tablename,indexname`); got != indexes {
		t.Error("the expansion changed indexes")
	}
	agesNoteSameRows(t, "Up does not change data", a.rows(false), before)
	a.exec(reasonReviewBlankSQL, 201)
	blanked := a.rows(false)
	a.migrateDownTo(reasonReviewTriggerTestVersion - 1)
	if reasonReviewFunctionDefs(t, a.db) != previousDefs {
		t.Error("Down did not restore pg_get_functiondef byte for byte")
	}
	agesNoteSameShape(t, "Down", agesNoteShape(t, a.db), previous, true)
	agesNoteSameRows(t, "Down does not restore blanked text", a.rows(false), blanked)
	for _, id := range []int{202, 203, 204, 205, 211, 212, 213, 214, 215} {
		_, err := a.db.Exec(reasonReviewBlankSQL, id)
		agesNoteWantError(t, fmt.Sprintf("blank after Down, request %d", id), err, "23514", agesNoteIdentityMsg)
	}
	// 00004's ages-note licence survives Down, as do pending-reason updates.
	a.exec(agesNoteBlankSQL, 202)
	a.exec(`UPDATE contact_requests SET reason='changed' WHERE id=200`)
	a.migrateTo(reasonReviewTriggerTestVersion)
	if reasonReviewFunctionDefs(t, a.db) != currentDefs {
		t.Error("second Up changed the expanded definitions")
	}
	a.exec(reasonReviewBlankSQL, 211)
}

func TestReasonReviewTriggerDeletedPartyLocks(t *testing.T) {
	t.Parallel()
	for _, party := range []int{1, 2} {
		for _, mode := range []string{"FOR NO KEY UPDATE", "FOR UPDATE", "FOR KEY SHARE", "FOR SHARE"} {
			t.Run(fmt.Sprintf("party %d %s", party, mode), func(t *testing.T) {
				a := newReasonReviewDB(t, reasonReviewTriggerTestVersion)
				a.tombstone(party)
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				holder, err := a.db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer holder.Rollback()
				var id int
				if err := holder.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 `+mode, party).Scan(&id); err != nil {
					t.Fatal(err)
				}
				blanker, err := a.db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer blanker.Rollback()
				// lock_timeout is much larger than NOWAIT. Its distinct error
				// message proves this refusal came from NOWAIT, without sleeps.
				if _, err := blanker.ExecContext(ctx, `SET LOCAL lock_timeout='5s'`); err != nil {
					t.Fatal(err)
				}
				result, err := blanker.ExecContext(ctx, reasonReviewBlankSQL, 201)
				if mode == "FOR NO KEY UPDATE" || mode == "FOR UPDATE" {
					agesNoteWantError(t, "deleted party busy", err, "55P03", "could not obtain lock on row in relation \"users\"")
					if err := blanker.Rollback(); err != nil {
						t.Fatal(err)
					}
					if reasonReviewReason(t, a.db, 201) == "" {
						t.Error("a refused blank changed the row")
					}
					return
				}
				if err != nil {
					t.Fatalf("compatible holder: %v", err)
				}
				reasonReviewAssertUpdate(t, blanker, result, 201, "")
				if err := holder.Rollback(); err != nil {
					t.Fatal(err)
				}
				// Only the blanker's SHARE remains. A third connection proves
				// it excludes a concurrent restoration of deleted_at.
				probe, err := a.db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer probe.Rollback()
				err = probe.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 FOR NO KEY UPDATE NOWAIT`, party).Scan(&id)
				agesNoteWantError(t, "blank holds deleted licence", err, "55P03", "could not obtain lock on row in relation \"users\"")
				if err := blanker.Commit(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

// Previous writers may blank contact and pending requests without acquiring
// a deleted party's row lock, including while another transaction holds it.
func TestReasonReviewBaselineRowsDoNotLockDeletedParties(t *testing.T) {
	for _, version := range []int64{6, 7} {
		for _, party := range []int{1, 2, 0} {
			t.Run(fmt.Sprintf("version %d party %d", version, party), func(t *testing.T) {
				a := newReasonReviewDB(t, version)
				a.exec(`INSERT INTO contact_requests(id,requester_id,subject_user_id,context_type,context_id,fields,reason,needs_admin,admin_decision,created_at,expires_at) VALUES(400,1,2,'group',10,'email','Contact reason',false,'not_needed','2026-10-24 12:00Z','2026-11-07 12:00Z')`)
				a.exec(`INSERT INTO contact_requests(id,requester_id,subject_user_id,context_type,context_id,fields,reason,needs_admin,admin_decision,subject_decision,subject_decided_by,subject_decided_at,status,created_at,expires_at,completed_at) VALUES(401,1,2,'group',10,'email','Contact reason',false,'not_needed','approved',2,'2026-10-25 12:00Z','granted','2026-10-24 12:00Z','2026-11-07 12:00Z','2026-10-25 12:00Z')`)
				if party == 0 {
					a.tombstone(1)
					a.tombstone(2)
				} else {
					a.tombstone(party)
				}
				lockedParty := party
				if lockedParty == 0 {
					lockedParty = 2
				}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				for _, mode := range []string{"FOR UPDATE", "FOR NO KEY UPDATE", "FOR SHARE", "FOR KEY SHARE"} {
					for _, request := range []int{200, 210, 400, 401} {
						holder, err := a.db.BeginTx(ctx, nil)
						if err != nil {
							t.Fatal(err)
						}
						var id int
						if err := holder.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 `+mode, lockedParty).Scan(&id); err != nil {
							t.Fatal(err)
						}
						blanker, err := a.db.BeginTx(ctx, nil)
						if err != nil {
							t.Fatal(err)
						}
						if _, err := blanker.ExecContext(ctx, `SET LOCAL lock_timeout='200ms'`); err != nil {
							t.Fatal(err)
						}
						res, err := blanker.ExecContext(ctx, reasonReviewBlankSQL, request)
						if err != nil {
							t.Fatalf("%s request %d: %v", mode, request, err)
						}
						if n, err := res.RowsAffected(); err != nil || n != 1 {
							t.Fatalf("affected %d: %v", n, err)
						}
						var reason string
						if err := blanker.QueryRowContext(ctx, `SELECT reason FROM contact_requests WHERE id=$1`, request).Scan(&reason); err != nil || reason != "" {
							t.Fatalf("reason %q: %v", reason, err)
						}
						if err := holder.Rollback(); err != nil {
							t.Fatal(err)
						}
						probe, err := a.db.BeginTx(ctx, nil)
						if err != nil {
							t.Fatal(err)
						}
						if err := probe.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE NOWAIT`, lockedParty).Scan(&id); err != nil {
							t.Fatalf("blank held party lock: %v", err)
						}
						if err := probe.Rollback(); err != nil {
							t.Fatal(err)
						}
						if err := blanker.Rollback(); err != nil {
							t.Fatal(err)
						}
					}
				}
			})
		}
	}
}

func TestReasonReviewPreviousUpdatesWithDeletedPartyLocked(t *testing.T) {
	for _, version := range []int64{6, 7} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			a := newReasonReviewDB(t, version)
			a.exec(`INSERT INTO contact_requests(id,requester_id,subject_user_id,context_type,context_id,fields,reason,needs_admin,admin_decision,created_at,expires_at) VALUES(400,1,2,'group',10,'email','Contact reason',false,'not_needed','2026-10-24 12:00Z','2026-11-07 12:00Z')`)
			a.tombstone(2)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			holder, err := a.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer holder.Rollback()
			var id int
			if err := holder.QueryRowContext(ctx, `SELECT id FROM users WHERE id=2 FOR UPDATE`).Scan(&id); err != nil {
				t.Fatal(err)
			}
			for _, request := range []int{200, 210, 400} {
				for _, combined := range []bool{false, true} {
					tx, err := a.db.BeginTx(ctx, nil)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := tx.ExecContext(ctx, `SET LOCAL lock_timeout='200ms'`); err != nil {
						t.Fatal(err)
					}
					q := `UPDATE contact_requests SET reason='replacement' WHERE id=$1`
					if combined {
						q = `UPDATE contact_requests SET reason='',expires_at=expires_at+interval '1 day' WHERE id=$1`
					}
					result, err := tx.ExecContext(ctx, q, request)
					if combined && request != 400 {
						agesNoteWantError(t, "pending identity change", err, "23514", agesNoteIdentityMsg)
					} else if err != nil {
						t.Fatalf("request %d combined=%v: %v", request, combined, err)
					} else {
						want := "replacement"
						if combined {
							want = ""
						}
						reasonReviewAssertUpdate(t, tx, result, request, want)
					}
					if err := tx.Rollback(); err != nil {
						t.Fatal(err)
					}
				}
			}
		})
	}
}

func TestReasonReviewTerminalLockOwnership(t *testing.T) {
	for _, scenario := range []string{"other-deleted", "self-held", "living-locked", "both-unlocked", "both-self-held", "both-other-share"} {
		t.Run(scenario, func(t *testing.T) {
			a := newReasonReviewDB(t, 7)
			a.tombstone(2)
			if scenario == "other-deleted" || strings.HasPrefix(scenario, "both-") {
				a.tombstone(1)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			holder, err := a.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer holder.Rollback()
			var id int
			locked := 1
			if scenario == "self-held" || scenario == "both-self-held" {
				locked = 2
			}
			if scenario != "both-unlocked" {
				mode := "FOR UPDATE"
				if scenario == "both-other-share" {
					mode = "FOR SHARE"
				}
				if err := holder.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 `+mode, locked).Scan(&id); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "self-held" || scenario == "both-self-held" {
				result, err := holder.ExecContext(ctx, reasonReviewBlankSQL, 201)
				if err != nil {
					t.Fatal(err)
				}
				reasonReviewAssertUpdate(t, holder, result, 201, "")
				return
			}
			tx, err := a.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			result, err := tx.ExecContext(ctx, reasonReviewBlankSQL, 201)
			if scenario == "other-deleted" {
				agesNoteWantError(t, "other deleted party busy", err, "55P03", "could not obtain lock on row in relation \"users\"")
			} else if err != nil {
				t.Fatal(err)
			} else {
				reasonReviewAssertUpdate(t, tx, result, 201, "")
			}
		})
	}
}

// A future column must participate even when both functions were compiled in
// the same backend before the DDL. The dedicated ages-note matrix also runs
// its added-column case against 00007.
func TestReasonReviewTriggerCoversAddedColumns(t *testing.T) {
	t.Parallel()
	a := newReasonReviewDB(t, reasonReviewTriggerTestVersion)
	a.tombstone(2)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := a.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Compile both functions, including the granted completion branch.
	if _, err := conn.ExecContext(ctx, reasonReviewBlankSQL, 205); err != nil {
		t.Fatal(err)
	}
	a.exec(`ALTER TABLE contact_requests ADD COLUMN t07b_probe integer NOT NULL DEFAULT 0`)
	before := a.rows(false)
	_, err = conn.ExecContext(ctx, `UPDATE contact_requests SET reason='',t07b_probe=1 WHERE id=201`)
	agesNoteWantError(t, "reason plus future column", err, "23514", agesNoteIdentityMsg)
	agesNoteSameRows(t, "future-column refusal", a.rows(false), before)
	for _, id := range []int{201, 215} {
		if _, err := conn.ExecContext(ctx, reasonReviewBlankSQL, id); err != nil {
			t.Fatalf("plain blank after DDL, request %d: %v", id, err)
		}
		var probe int
		if err := a.db.QueryRow(`SELECT t07b_probe FROM contact_requests WHERE id=$1`, id).Scan(&probe); err != nil || probe != 0 {
			t.Fatalf("future column=%d (%v), want 0", probe, err)
		}
	}
}

// The file itself must contain only the adopted function replacements. This
// complements H8's no-bypass detector with exact Down-source preservation.
func TestReasonReviewMigrationFileRestoresPreviousDefinitions(t *testing.T) {
	t.Parallel()
	prior, err := migrations.Files.ReadFile(agesNoteMigrationFile)
	if err != nil {
		t.Fatal(err)
	}
	current, err := migrations.Files.ReadFile(reasonReviewMigrationFile)
	if err != nil {
		t.Fatal(err)
	}
	previousUp, _, ok := strings.Cut(string(prior), "-- +goose Down")
	if !ok {
		t.Fatal("00004 has no Down")
	}
	i := strings.Index(previousUp, "-- +goose StatementBegin")
	if i < 0 {
		t.Fatal("00004 has no function blocks")
	}
	up, down, ok := strings.Cut(string(current), "-- +goose Down")
	if !ok {
		t.Fatal("reason migration has no Down")
	}
	j := strings.Index(down, "-- +goose StatementBegin")
	if j < 0 || strings.TrimSpace(down[j:]) != strings.TrimSpace(previousUp[i:]) {
		t.Error("Down function definitions differ from 00004 Up")
	}
	if strings.Count(up, "SET LOCAL lock_timeout = '5s';") != 1 || strings.Count(down, "SET LOCAL lock_timeout = '5s';") != 1 {
		t.Error("both directions must bound lock acquisition to 5 seconds")
	}
}
