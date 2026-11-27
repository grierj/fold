package write_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"fold/internal/audit"
	"fold/internal/testenv"
	"fold/internal/write"
)

type fn struct {
	action string
	apply  func(tx *write.Tx) error
}

func (f fn) Action() string           { return f.action }
func (f fn) Apply(tx *write.Tx) error { return f.apply(tx) }

func TestEveryChangeWritesOneAuditRow(t *testing.T) {
	env := testenv.New(t)
	ctx := context.Background()
	err := env.Exec.Run(ctx, write.System(), fn{"settings.save", func(tx *write.Tx) error {
		tx.Detail("key", "church.name")
		_, err := tx.Exec(`INSERT INTO settings (key, value, updated_at) VALUES ('church.name', 'Cloverdale', ?)`, tx.Now.Unix())
		return err
	}})
	if err != nil {
		t.Fatal(err)
	}
	var action, actor, details string
	env.Writer.QueryRow(`SELECT action, actor, details FROM audit_log`).Scan(&action, &actor, &details)
	if action != "settings.save" || actor != audit.System || details != `{"key":"church.name"}` {
		t.Fatalf("audit row = %s %s %s", action, actor, details)
	}
}

func TestFailedOrEmptyCommandWritesNothing(t *testing.T) {
	env := testenv.New(t)
	ctx := context.Background()
	boom := errors.New("boom")
	err := env.Exec.Run(ctx, write.System(), fn{"settings.save", func(tx *write.Tx) error {
		tx.Exec(`INSERT INTO settings (key, value, updated_at) VALUES ('a', 'b', 0)`)
		return boom
	}})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	err = env.Exec.Run(ctx, write.System(), fn{"settings.save", func(tx *write.Tx) error {
		tx.Exec(`INSERT INTO settings (key, value, updated_at) VALUES ('a', 'b', 0)`)
		return write.ErrNothing
	}})
	if err != nil {
		t.Fatal(err)
	}
	if n := env.Count(t, `SELECT count(*) FROM settings`) + env.Count(t, `SELECT count(*) FROM audit_log`); n != 0 {
		t.Fatalf("%d rows written", n)
	}
}

func TestActorIsLoadedInsideTheWrite(t *testing.T) {
	env := testenv.New(t)
	ctx := context.Background()
	now := env.Clock.Now().Unix()
	hash := bytes.Repeat([]byte{1}, 32)
	if _, err := env.Writer.Exec(`INSERT INTO people (id, uid, name, email, status, created_at, updated_at) VALUES (1, 'u1', 'Pat', 'pat@example.org', 'active', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Writer.Exec(`INSERT INTO sessions (token_hash, person_id, created_at, renewed_at, expires_at) VALUES (?, 1, ?, ?, ?)`, hash, now, now, now+3600); err != nil {
		t.Fatal(err)
	}
	var seen string
	ok := fn{"test.act", func(tx *write.Tx) error { seen = tx.Actor.Person.UID; return nil }}
	if err := env.Exec.Run(ctx, write.Session(hash), ok); err != nil || seen != "u1" {
		t.Fatalf("Run = %v, actor %q", err, seen)
	}
	// The account is disabled after the page was drawn and before the post.
	env.Writer.Exec(`UPDATE people SET status = 'disabled' WHERE id = 1`)
	if err := env.Exec.Run(ctx, write.Session(hash), ok); !errors.Is(err, write.ErrSignedOut) {
		t.Fatalf("disabled actor: %v", err)
	}
	env.Writer.Exec(`UPDATE people SET status = 'active' WHERE id = 1`)
	env.Clock.Advance(2 * time.Hour)
	if err := env.Exec.Run(ctx, write.Session(hash), ok); !errors.Is(err, write.ErrSignedOut) {
		t.Fatalf("expired session: %v", err)
	}
}
