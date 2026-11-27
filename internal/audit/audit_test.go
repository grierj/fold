package audit_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"fold/internal/audit"
	"fold/internal/testenv"
)

func appendN(t *testing.T, env *testenv.Env, n int) {
	t.Helper()
	ctx := context.Background()
	for i := range n {
		tx, err := env.Writer.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := audit.Append(ctx, tx, time.Unix(int64(1000+i), 0), audit.Entry{
			Action: "test.thing", Actor: audit.System, Subjects: []string{"abc"}, Details: map[string]string{"n": "1"},
		}); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestChainVerifies(t *testing.T) {
	env := testenv.New(t)
	appendN(t, env, 5)
	rep, err := audit.Verify(context.Background(), env.Writer)
	if err != nil || !rep.Intact() || rep.Rows != 5 || rep.Last.Seq != 5 {
		t.Fatalf("Verify = %+v, %v", rep, err)
	}
}

func TestEditingOrDeletingIsRefused(t *testing.T) {
	env := testenv.New(t)
	appendN(t, env, 3)
	for _, q := range []string{
		`UPDATE audit_log SET action = 'x.y' WHERE seq = 2`,
		`UPDATE OR REPLACE audit_log SET seq = 1 WHERE seq = 2`,
		`DELETE FROM audit_log WHERE seq = 3`,
		`DELETE FROM audit_log`,
		`INSERT OR REPLACE INTO audit_log (seq, at, action, actor, subjects, details, format, prev_hash, hash)
		   SELECT seq, at, 'x.y', actor, subjects, details, format, prev_hash, hash FROM audit_log WHERE seq = 2`,
		`INSERT INTO audit_log (seq, at, action, actor, subjects, details, format, prev_hash, hash)
		   VALUES (9, 0, 'x.y', 'system', '[]', '{}', 1, zeroblob(32), zeroblob(32))`,
		`INSERT INTO audit_log (seq, at, action, actor, subjects, details, format, prev_hash, hash)
		   VALUES (4, 0, 'x.y', 'system', '[]', '{}', 1, zeroblob(32), zeroblob(32))`,
	} {
		if _, err := env.Writer.Exec(q); err == nil {
			t.Errorf("accepted: %s", q)
		}
	}
	if n := env.Count(t, `SELECT count(*) FROM audit_log`); n != 3 {
		t.Fatalf("%d rows, want 3", n)
	}
}

func TestChangedRowIsReported(t *testing.T) {
	env := testenv.New(t)
	appendN(t, env, 4)
	ctx := context.Background()
	before, _ := audit.CheckpointOf(ctx, env.Writer)
	// Someone with the file drops the guard and edits a row.
	mustExec(t, env.Writer, `DROP TRIGGER audit_log_no_update`)
	mustExec(t, env.Writer, `UPDATE audit_log SET details = '{"n":"2"}' WHERE seq = 2`)
	rep, err := audit.Verify(ctx, env.Writer)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Intact() || rep.BrokenAt != 2 || !strings.Contains(rep.Problem, "changed") {
		t.Fatalf("Verify = %+v", rep)
	}
	// Rewriting every hash after it makes the chain look whole again, but a
	// checkpoint taken earlier (in a backup or an email) no longer holds.
	rewriteFrom(t, env, 2)
	rep, _ = audit.Verify(ctx, env.Writer)
	if !rep.Intact() {
		t.Fatalf("rewrite did not produce a whole chain: %+v", rep)
	}
	ok, err := audit.Holds(ctx, env.Writer, before)
	if err != nil || ok {
		t.Fatalf("Holds(old checkpoint) = %v, %v; want false", ok, err)
	}
}

func TestEntryRefusesFreeText(t *testing.T) {
	env := testenv.New(t)
	ctx := context.Background()
	for _, e := range []audit.Entry{
		{Action: "a.b", Actor: "Pat Smith"},
		{Action: "a.b", Actor: audit.System, Subjects: []string{"pat@example.org"}},
		{Action: "a.b", Actor: audit.System, Details: map[string]string{"why": "because I said so"}},
		{Action: "nodots", Actor: audit.System},
	} {
		tx, _ := env.Writer.BeginTx(ctx, nil)
		if _, err := audit.Append(ctx, tx, time.Unix(0, 0), e); err == nil {
			t.Errorf("accepted %+v", e)
		}
		tx.Rollback()
	}
}

func mustExec(t *testing.T, w *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := w.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

// rewriteFrom recomputes every hash from seq on, as a careful attacker would.
func rewriteFrom(t *testing.T, env *testenv.Env, from int64) {
	t.Helper()
	ctx := context.Background()
	mustExec(t, env.Writer, `DROP TRIGGER IF EXISTS audit_log_no_update`)
	mustExec(t, env.Writer, `DROP TRIGGER IF EXISTS audit_log_no_delete`)
	mustExec(t, env.Writer, `DROP TRIGGER IF EXISTS audit_log_append_only`)
	type row struct {
		seq, at                          int64
		action, actor, subjects, details string
	}
	var rows []row
	r, err := env.Writer.Query(`SELECT seq, at, action, actor, subjects, details FROM audit_log WHERE seq >= ? ORDER BY seq`, from)
	if err != nil {
		t.Fatal(err)
	}
	for r.Next() {
		var x row
		r.Scan(&x.seq, &x.at, &x.action, &x.actor, &x.subjects, &x.details)
		rows = append(rows, x)
	}
	r.Close()
	mustExec(t, env.Writer, `DELETE FROM audit_log WHERE seq >= ?`, from)
	mustExec(t, env.Writer, `CREATE TRIGGER audit_log_append_only BEFORE INSERT ON audit_log
		WHEN NEW.seq IS NOT (SELECT coalesce(max(seq), 0) + 1 FROM audit_log) BEGIN SELECT RAISE(ABORT, 'x'); END`)
	for _, x := range rows {
		tx, _ := env.Writer.BeginTx(ctx, nil)
		// Append recomputes the hash over the edited fields.
		var subjects []string
		if x.subjects != "[]" {
			subjects = []string{strings.Trim(x.subjects, `[]"`)}
		}
		details := map[string]string{}
		if strings.Contains(x.details, `"2"`) {
			details["n"] = "2"
		} else {
			details["n"] = "1"
		}
		if _, err := audit.Append(ctx, tx, time.Unix(x.at, 0), audit.Entry{Action: x.action, Actor: x.actor, Subjects: subjects, Details: details}); err != nil {
			t.Fatal(err)
		}
		tx.Commit()
	}
}

// R30: a row keeps the exact text sent, the hash covers it, and a changed
// word breaks the chain.
func TestTextIsKeptAndSealed(t *testing.T) {
	env := testenv.New(t)
	ctx := context.Background()
	tx, _ := env.Writer.BeginTx(ctx, nil)
	if _, err := audit.Append(ctx, tx, time.Unix(1, 0), audit.Entry{Action: "group.reject", Actor: audit.System, Text: "Please talk to the office first."}); err != nil {
		t.Fatal(err)
	}
	tx.Commit()
	var text string
	env.Writer.QueryRow(`SELECT text FROM audit_log WHERE seq = 1`).Scan(&text)
	if text != "Please talk to the office first." {
		t.Fatalf("text %q", text)
	}
	if rep, _ := audit.Verify(ctx, env.Writer); !rep.Intact() {
		t.Fatalf("chain: %+v", rep)
	}
	mustExec(t, env.Writer, `DROP TRIGGER audit_log_no_update`)
	mustExec(t, env.Writer, `UPDATE audit_log SET text = 'Please talk to the office.' WHERE seq = 1`)
	if rep, _ := audit.Verify(ctx, env.Writer); rep.Intact() {
		t.Fatal("a changed word went unnoticed")
	}
}

func TestTextLimits(t *testing.T) {
	env := testenv.New(t)
	ctx := context.Background()
	tx, _ := env.Writer.BeginTx(ctx, nil)
	defer tx.Rollback()
	if _, err := audit.Append(ctx, tx, time.Unix(1, 0), audit.Entry{Action: "a.b", Actor: audit.System, Text: strings.Repeat("x", audit.MaxText+1)}); err == nil {
		t.Fatal("an overlong text was kept")
	}
	if _, err := audit.Append(ctx, tx, time.Unix(1, 0), audit.Entry{Action: "a.b", Actor: audit.System, Text: "\xff\xfe"}); err == nil {
		t.Fatal("text that is not UTF-8 was kept")
	}
}
