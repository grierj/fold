package backup_test

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"fold/internal/audit"
	"fold/internal/backup"
	"fold/internal/clock"
	"fold/internal/db"
	"fold/internal/keys"
	"fold/internal/people"
	"fold/internal/testenv"
	"fold/internal/write"
)

type sql1 struct{ q string }

func (c sql1) Action() string { return "test.seed" }
func (c sql1) Apply(tx *write.Tx) error {
	_, err := tx.Exec(c.q, tx.Now.Unix())
	return err
}

// Rehearses the first real upgrade: a schema-1 database with a person, a
// session, a link and audit rows is copied (BeforeMigrate), migrated to the
// latest schema, and still holds together. Then a schema-1 backup restores
// on the new binary.
func TestUpgradeFromSchema1(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "app.db")
	w, err := db.OpenWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := db.MigrateTo(ctx, w, 1); err != nil {
		t.Fatal(err)
	}
	c := clock.NewFake(testenv.Start)
	e := write.New(w, c, keys.FromBytes(bytes.Repeat([]byte{9}, 32)))
	hash := bytes.Repeat([]byte{7}, 32)
	for _, q := range []string{
		`INSERT INTO people (id, uid, name, email, status, is_admin, created_at, updated_at) VALUES (1, 'u1', 'Pat', 'pat@example.org', 'active', 1, ?1, ?1)`,
		`INSERT INTO sessions (token_hash, person_id, created_at, renewed_at, expires_at) VALUES (x'` + bytesHex(hash) + `', 1, ?1, ?1, ?1 + 3600)`,
		`INSERT INTO links (purpose, person_id, token_hash, created_at, expires_at) VALUES ('signin', 1, x'01', ?1, ?1 + 3600)`,
	} {
		if err := e.Run(ctx, write.System(), sql1{q}); err != nil {
			t.Fatal(err)
		}
	}
	old, err := backup.Take(ctx, e, dir)
	if err != nil {
		t.Fatal(err)
	}
	if old.Schema != db.Latest() {
		// Take records the binary's schema; this backup is really schema 1.
		t.Logf("backup schema field %d (binary's)", old.Schema)
	}

	// The upgrade, as server.Open does it.
	have, _ := db.Version(ctx, w)
	if _, err := backup.BeforeMigrate(ctx, w, dir, c.Now(), have); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, w); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var problems int
	rows, err := w.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		problems++
	}
	rows.Close()
	if problems != 0 {
		t.Fatalf("%d foreign key problems after the upgrade", problems)
	}
	if rep, err := audit.Verify(ctx, w); err != nil || !rep.Intact() || rep.Rows != 4 {
		t.Fatalf("chain after upgrade: %+v %v", rep, err)
	}
	s, err := people.BySession(ctx, w, hash, c.Now())
	if err != nil || s.Person.UID != "u1" || !s.Person.IsAdmin {
		t.Fatalf("session after upgrade: %+v %v", s, err)
	}
	var hidden int
	if err := w.QueryRow(`SELECT contact_hidden FROM people WHERE id = 1`).Scan(&hidden); err != nil || hidden != 0 {
		t.Fatalf("new column: %d %v", hidden, err)
	}
	if _, err := w.Exec(`INSERT INTO people (uid, name, email, status, created_at, updated_at) VALUES ('u2', 'Sam', 'sam@example.org', 'pending', 0, 0)`); err != nil {
		t.Fatalf("pending status refused: %v", err)
	}
	if _, err := w.Exec(`INSERT INTO sessions (token_hash, person_id, created_at, renewed_at, expires_at) VALUES (x'02', 99, 0, 0, 0)`); err == nil {
		t.Fatal("foreign keys are not enforced after the rebuild")
	}

	// A schema-1 backup restores on this binary: Prepare migrates it.
	c.Advance(time.Minute)
	if _, err := backup.Restore(ctx, e, dir, old.Name); err != nil {
		t.Fatalf("restoring a schema-1 backup: %v", err)
	}
	if v, _ := db.Version(ctx, w); v != db.Latest() {
		t.Fatalf("schema %d after restore", v)
	}
	if rep, _ := audit.Verify(ctx, w); !rep.Intact() {
		t.Fatalf("chain after restore: %+v", rep)
	}
}

func bytesHex(b []byte) string {
	const h = "0123456789abcdef"
	out := make([]byte, 0, 2*len(b))
	for _, x := range b {
		out = append(out, h[x>>4], h[x&15])
	}
	return string(out)
}
