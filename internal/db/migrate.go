package db

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

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

// Migrate brings the database forward. Before changing a database that
// already holds data it copies it to copyDir (docs/DECISIONS.md R11, P9).
// An empty copyDir skips the copy (a fresh staging file during restore).
func Migrate(ctx context.Context, w *sql.DB, copyDir string, now time.Time) error {
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
	if have == len(ms) {
		return nil
	}
	if have > 0 && copyDir != "" {
		if err := os.MkdirAll(copyDir, 0o700); err != nil {
			return err
		}
		dst := filepath.Join(copyDir, fmt.Sprintf("before-schema-%d-%s.db", have+1, now.UTC().Format("20060102-150405")))
		if _, err := w.ExecContext(ctx, "VACUUM INTO ?", dst); err != nil {
			return fmt.Errorf("db: copy before migrating: %w", err)
		}
	}
	for _, m := range ms[have:] {
		tx, err := w.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			tx.Rollback()
			return fmt.Errorf("db: migration %s: %w", m.name, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("db: migration %s: %w", m.name, err)
		}
	}
	return nil
}
