package db_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"fold/internal/db"
)

func TestPragmasOnEveryConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	w, err := db.OpenWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	r, err := db.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	// Hold several read transactions open at once, so each is its own pooled
	// connection, and check the pragmas on all of them.
	ctx := context.Background()
	done := make(chan struct{})
	errs := make(chan error, 3)
	for range 3 {
		go func() {
			errs <- r.Read(ctx, func(q *sql.Tx) error {
				var fk, busy, qo int
				var mode string
				if err := q.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
					return err
				}
				if err := q.QueryRow("PRAGMA busy_timeout").Scan(&busy); err != nil {
					return err
				}
				if err := q.QueryRow("PRAGMA query_only").Scan(&qo); err != nil {
					return err
				}
				if err := q.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
					return err
				}
				if fk != 1 || busy != 10000 || qo != 1 || mode != "wal" {
					return errors.New("pragmas not set on a pooled reader")
				}
				<-done
				return nil
			})
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(done)
	for range 3 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	var fk int
	if err := w.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("writer foreign_keys = %d, %v", fk, err)
	}
}

func TestReaderRefusesWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	w, _ := db.OpenWriter(path)
	defer w.Close()
	if err := db.Migrate(context.Background(), w, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	r, _ := db.OpenReader(path)
	defer r.Close()
	err := r.Read(context.Background(), func(q *sql.Tx) error {
		_, err := q.Exec(`INSERT INTO settings (key, value, updated_at) VALUES ('a', 'b', 0)`)
		return err
	})
	if err == nil {
		t.Fatal("a read connection wrote")
	}
}

func TestMigrateCopiesFirstAndRefusesNewer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.db")
	w, _ := db.OpenWriter(path)
	defer w.Close()
	ctx := context.Background()
	if err := db.Migrate(ctx, w, dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	if v, _ := db.Version(ctx, w); v != db.Latest() {
		t.Fatalf("version %d, want %d", v, db.Latest())
	}
	// A fresh database is not copied: there is nothing to keep.
	if m, _ := filepath.Glob(filepath.Join(dir, "before-schema-*")); len(m) != 0 {
		t.Fatalf("copied an empty database: %v", m)
	}
	if _, err := w.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	var tooNew db.ErrTooNew
	if err := db.Migrate(ctx, w, dir, time.Now()); !errors.As(err, &tooNew) {
		t.Fatalf("Migrate on a newer database = %v", err)
	}
}

func TestTablesAreStrict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	w, _ := db.OpenWriter(path)
	defer w.Close()
	if err := db.Migrate(context.Background(), w, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	rows, err := w.Query(`SELECT name, strict FROM pragma_table_list WHERE schema = 'main' AND type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var name string
		var strict int
		rows.Scan(&name, &strict)
		if strict != 1 {
			t.Errorf("table %s is not STRICT", name)
		}
		n++
	}
	if n == 0 {
		t.Fatal("no tables")
	}
}
