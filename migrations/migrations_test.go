package migrations_test

import (
	"context"
	"database/sql"
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

func TestMigrateEmptyDatabase(t *testing.T) {
	db := openSQL(t, testdb.Fresh(t, false))
	p, err := migrations.NewProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Up(context.Background()); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	if pending, err := p.HasPending(context.Background()); err != nil || pending {
		t.Fatalf("pending after up: %v %v", pending, err)
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name IN ('users','groups','events','event_hosts','event_occurrences','registrations','memberships','settings')").Scan(&n); err != nil || n != 8 {
		t.Fatalf("baseline tables = %d (%v), want 8", n, err)
	}
}

// TestNoDrift is the C1 drift check: AutoMigrate run for real against a
// freshly migrated database must not want to CREATE or ALTER anything.
func TestNoDrift(t *testing.T) {
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

// TestSimplicityMigrations checks 00008 (DEC-47) and 00009 (DEC-48) on
// existing rows, and that 00009 goes back down cleanly.
func TestSimplicityMigrations(t *testing.T) {
	db := openSQL(t, testdb.Fresh(t, false))
	ctx := context.Background()
	p, err := migrations.NewProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 7); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO users (id, email) VALUES (1, 'a@example.com'), (2, 'b@example.com')`)
	exec(`INSERT INTO groups (id, name, leader_id, visibility) VALUES (1, 'Open', 1, 'public'), (2, 'Closed', 1, 'invite_only')`)
	exec(`INSERT INTO memberships (user_id, group_id, role, status) VALUES (1, 1, 'leader', 'active'), (2, 1, 'member', 'pending_return')`)
	ins := `INSERT INTO events (id, group_id, title, description, starts_local, duration_minutes, tzid, rrule, location, created_by, visibility, visibility_source)
		VALUES ($1, $2, '', '', '2026-12-01T19:00', 60, 'UTC', '', '', 1, $3, $4)`
	exec(ins, 1, 1, "private", "group")   // stale: the group is public now
	exec(ins, 2, 2, "public", "custom")   // opened wider by a leader
	exec(ins, 3, 1, "private", "custom")  // stricter than the group
	exec(ins, 4, nil, "public", "custom") // church-wide

	if _, err := p.UpTo(ctx, 9); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM memberships WHERE status = 'pending_return'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("pending_return rows left: %d %v", n, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action = 'membership.not_restored' AND target_id = 1 AND meta_json->>'userId' = '2'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("not_restored audit: %d %v", n, err)
	}
	if _, err := db.Exec(`INSERT INTO memberships (user_id, group_id, role, status) VALUES (2, 2, 'member', 'pending_return')`); err == nil {
		t.Fatal("pending_return is still allowed")
	}
	want := map[int]struct {
		vis    string
		opened bool
	}{1: {"public", false}, 2: {"public", true}, 3: {"private", false}, 4: {"public", false}}
	for id, w := range want {
		var vis string
		var opened bool
		if err := db.QueryRow(`SELECT visibility, leader_opened FROM events WHERE id = $1`, id).Scan(&vis, &opened); err != nil {
			t.Fatal(err)
		}
		if vis != w.vis || opened != w.opened {
			t.Fatalf("event %d: %s %v, want %+v", id, vis, opened, w)
		}
	}
	if _, err := db.Exec(`UPDATE events SET leader_opened = true WHERE id = 4`); err == nil {
		t.Fatal("a church-wide event can't be leader-opened")
	}

	if _, err := p.DownTo(ctx, 8); err != nil {
		t.Fatalf("down: %v", err)
	}
	src := map[int]string{1: "group", 2: "custom", 3: "custom", 4: "custom"}
	for id, w := range src {
		var got string
		if err := db.QueryRow(`SELECT visibility_source FROM events WHERE id = $1`, id).Scan(&got); err != nil || got != w {
			t.Fatalf("down event %d: %s %v, want %s", id, got, err, w)
		}
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
}
