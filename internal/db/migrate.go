package db

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"fold/migrations"
)

// The schema version is SQLite's user_version, so the migration runner needs
// no table of its own and no dependency (docs/PLAN.md, first agent checks).

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		num, _, ok := strings.Cut(e.Name(), "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil || v <= 0 {
			return nil, fmt.Errorf("db: migration %q must start with a number and an underscore", e.Name())
		}
		body, err := fs.ReadFile(migrations.FS, e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, migration{version: v, name: e.Name(), sql: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i, m := range out {
		if m.version != i+1 {
			return nil, fmt.Errorf("db: migrations must be numbered 1, 2, 3 without gaps; found %s", m.name)
		}
	}
	return out, nil
}

// Latest is the schema version this binary writes.
func Latest() int {
	ms, err := loadMigrations()
	if err != nil {
		panic(err)
	}
	return len(ms)
}

// Version reads the schema version of a database.
func Version(ctx context.Context, q Querier) (int, error) {
	var v int
	err := q.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v)
	return v, err
}

// ErrTooNew is a database written by a newer release. Going back is the old
// image plus the copy taken before the upgrade (docs/OPERATIONS.md).
type ErrTooNew struct{ Have, Know int }

func (e ErrTooNew) Error() string {
	return fmt.Sprintf("db: the database is at schema %d but this release knows only up to %d; run the newer release, or restore the copy taken before the upgrade", e.Have, e.Know)
}

// Migrate brings the database forward. The caller keeps a copy of a
// database that holds data first (backup.BeforeMigrate).
func Migrate(ctx context.Context, w *sql.DB) error { return MigrateTo(ctx, w, Latest()) }

// MigrateTo brings the database forward to schema version n. Tests use it to
// build an older database and rehearse an upgrade from it.
func MigrateTo(ctx context.Context, w *sql.DB, n int) error {
	ms, err := loadMigrations()
	if err != nil {
		return err
	}
	have, err := Version(ctx, w)
	if err != nil {
		return err
	}
	if have > len(ms) {
		return ErrTooNew{Have: have, Know: len(ms)}
	}
	if n > len(ms) {
		n = len(ms)
	}
	if have >= n {
		return nil
	}
	// A migration may rebuild a table others point at (SQLite cannot alter a
	// constraint in place). That only works with foreign keys off, which
	// must be set outside a transaction, so the migrations run on one
	// connection with them off, and each checks every foreign key itself
	// before it commits.
	conn, err := w.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		return err
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), "PRAGMA foreign_keys = ON")
	for _, m := range ms[have:n] {
		if err := apply(ctx, conn, m); err != nil {
			return err
		}
	}
	return nil
}

func apply(ctx context.Context, conn *sql.Conn, m migration) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("db: migration %s: %w", m.name, err)
	}
	rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	bad := rows.Next()
	rows.Close()
	if bad {
		return fmt.Errorf("db: migration %s leaves rows pointing at nothing", m.name)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("db: migration %s: %w", m.name, err)
	}
	return nil
}
