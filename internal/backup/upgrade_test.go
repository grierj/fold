package backup_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"path/filepath"
	"strconv"
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
	now := c.Now().Unix()
	for _, q := range []string{
		`INSERT INTO people (id, uid, name, email, status, is_admin, created_at, updated_at) VALUES (1, 'u1', 'Pat', 'pat@example.org', 'active', 1, ?1, ?1)`,
		`INSERT INTO sessions (token_hash, person_id, created_at, renewed_at, expires_at) VALUES (x'` + bytesHex(hash) + `', 1, ?1, ?1, ?1 + 3600)`,
		`INSERT INTO links (purpose, person_id, token_hash, created_at, expires_at) VALUES ('signin', 1, x'01', ?1, ?1 + 3600)`,
	} {
		if _, err := w.Exec(q, now); err != nil {
			t.Fatal(err)
		}
	}
	// Three audit rows as a schema-1 binary wrote them: format 1, whose bytes
	// this test fixes independently of the audit package.
	prev := make([]byte, 32)
	for seq := int64(1); seq <= 3; seq++ {
		h := format1Hash(seq, now, "test.seed", "system", "[]", "{}", prev)
		if _, err := w.Exec(`INSERT INTO audit_log (seq, at, action, actor, subjects, details, format, prev_hash, hash) VALUES (?, ?, 'test.seed', 'system', '[]', '{}', 1, ?, ?)`,
			seq, now, prev, h); err != nil {
			t.Fatal(err)
		}
		prev = h
	}
	have, _ := db.Version(ctx, w)
	old, err := backup.BeforeMigrate(ctx, w, dir, c.Now(), have)
	if err != nil {
		t.Fatal(err)
	}
	c.Advance(time.Second)

	// The upgrade, as server.Open does it (the copy above was its first step).
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
	if rep, err := audit.Verify(ctx, w); err != nil || !rep.Intact() || rep.Rows != 3 {
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

// The active_memberships view counts only an active membership of an active
// person in a group that is not deleted.
func TestActiveMembershipsView(t *testing.T) {
	env := testenv.New(t)
	for _, q := range []string{
		`INSERT INTO people (id, uid, name, email, status, created_at, updated_at) VALUES (1, 'a', 'A', 'a@x.org', 'active', 0, 0), (2, 'b', 'B', 'b@x.org', 'disabled', 0, 0)`,
		`INSERT INTO groups (id, uid, name, visibility, status, created_at, updated_at) VALUES (1, 'g1', 'G1', 'public', 'active', 0, 0), (2, 'g2', 'G2', 'public', 'deleted', 0, 0), (3, 'g3', 'G3', 'public', 'pending', 0, 0)`,
		`INSERT INTO memberships (group_id, person_id, role, state, created_at, updated_at) VALUES
			(1, 1, 'leader', 'active', 0, 0), (1, 2, 'member', 'active', 0, 0),
			(2, 1, 'leader', 'active', 0, 0), (3, 1, 'leader', 'active', 0, 0)`,
	} {
		if _, err := env.Writer.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if n := env.Count(t, `SELECT count(*) FROM active_memberships`); n != 2 {
		t.Fatalf("%d active memberships, want 2 (not the disabled person, not the deleted group)", n)
	}
}

// format1Hash is the audit row hash of format 1, written out here so a
// change to the audit package cannot silently change what old rows prove.
func format1Hash(seq, at int64, action, actor, subjects, details string, prev []byte) []byte {
	h := sha256.New()
	field := func(b []byte) {
		var n [binary.MaxVarintLen64]byte
		h.Write(n[:binary.PutUvarint(n[:], uint64(len(b)))])
		h.Write(b)
	}
	field([]byte("audit-format-1"))
	field([]byte(strconv.FormatInt(seq, 10)))
	field([]byte(strconv.FormatInt(at, 10)))
	field([]byte(action))
	field([]byte(actor))
	field([]byte(subjects))
	field([]byte(details))
	field(prev)
	return h.Sum(nil)
}

// Migration 9 rebuilds people and links under the views and every table
// that points at them: rows in each come through, keys still hold, and the
// views count guests' answers but never make them members.
func TestUpgradeToGuests(t *testing.T) {
	ctx := context.Background()
	w, err := db.OpenWriter(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := db.MigrateTo(ctx, w, 8); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO people (id, uid, name, email, status, contact_hidden, created_at, updated_at) VALUES (1, 'a', 'A', 'a@x.org', 'active', 1, 0, 0)`,
		`INSERT INTO sessions (token_hash, person_id, created_at, renewed_at, expires_at) VALUES (x'01', 1, 0, 0, 9)`,
		`INSERT INTO links (purpose, person_id, token_hash, created_at, expires_at) VALUES ('signin', 1, x'02', 0, 9)`,
		`INSERT INTO groups (id, uid, name, visibility, status, created_at, updated_at) VALUES (1, 'g', 'G', 'public', 'active', 0, 0)`,
		`INSERT INTO memberships (group_id, person_id, role, state, created_at, updated_at) VALUES (1, 1, 'leader', 'active', 0, 0)`,
		`INSERT INTO events (id, uid, group_id, title, visibility, zone, starts_local, minutes, repeat, created_at, updated_at) VALUES (1, 'e', 1, 'E', 'public', 'UTC', '2026-12-01T19:00', 60, 'once', 0, 0)`,
		`INSERT INTO dates (id, uid, event_id, day, starts_at, ends_at, starts_local) VALUES (1, 'd', 1, '2026-12-01', 1, 2, '2026-12-01T19:00')`,
		`INSERT INTO answers (date_id, person_id, status, created_at, updated_at) VALUES (1, 1, 'going', 0, 0)`,
		`INSERT INTO notices (person_id, kind, date_id, created_at) VALUES (1, 'date.cancelled', 1, 0)`,
	} {
		if _, err := w.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := db.Migrate(ctx, w); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	count := func(q string) int {
		var n int
		if err := w.QueryRow(q).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	if count(`SELECT count(*) FROM people WHERE contact_hidden = 1`) != 1 || count(`SELECT count(*) FROM links WHERE person_id = 1`) != 1 ||
		count(`SELECT count(*) FROM sessions`) != 1 || count(`SELECT count(*) FROM notices`) != 1 || count(`SELECT count(*) FROM active_memberships`) != 1 {
		t.Fatal("rows lost in the rebuild")
	}
	if _, err := w.Exec(`INSERT INTO people (id, uid, name, email, status, created_at, updated_at) VALUES (2, 'b', 'B', 'b@x.org', 'guest', 0, 0)`); err != nil {
		t.Fatalf("guest refused: %v", err)
	}
	w.Exec(`INSERT INTO answers (date_id, person_id, status, created_at, updated_at) VALUES (1, 2, 'going', 0, 0)`)
	w.Exec(`INSERT INTO memberships (group_id, person_id, role, state, created_at, updated_at) VALUES (1, 2, 'member', 'active', 0, 0)`)
	if count(`SELECT count(*) FROM counted_answers`) != 2 || count(`SELECT count(*) FROM active_memberships`) != 1 {
		t.Fatal("a guest's answer doesn't count, or a guest is a member")
	}
	if _, err := w.Exec(`INSERT INTO links (purpose, token_hash, created_at, expires_at) VALUES ('guest', x'03', 0, 9)`); err == nil {
		t.Fatal("a confirm link without its sealed address and date was stored")
	}
	if _, err := w.Exec(`INSERT INTO sessions (token_hash, person_id, created_at, renewed_at, expires_at) VALUES (x'04', 99, 0, 0, 0)`); err == nil {
		t.Fatal("foreign keys are not enforced after the rebuild")
	}
}
