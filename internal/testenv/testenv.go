// Package testenv builds a migrated database, an executor and a fake clock
// for tests. Each test gets its own file under t.TempDir; no containers.
package testenv

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"fold/internal/clock"
	"fold/internal/db"
	"fold/internal/keys"
	"fold/internal/write"
)

// Start is the fake clock's first reading: a Wednesday in Boise.
var Start = time.Date(2026, 11, 25, 17, 0, 0, 0, time.UTC)

type Env struct {
	Dir    string
	Path   string
	Writer *sql.DB
	Reader *db.Reader
	Exec   *write.Executor
	Clock  *clock.Fake
	Keys   *keys.Keys
	Log    *slog.Logger
}

func New(t testing.TB) *Env {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "app.db")
	w, err := db.OpenWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	if err := db.Migrate(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	r, err := db.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	c := clock.NewFake(Start)
	k := keys.FromBytes(bytes.Repeat([]byte{42}, 32))
	return &Env{Dir: dir, Path: path, Writer: w, Reader: r, Exec: write.New(w, c, k), Clock: c, Keys: k,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// Count runs a COUNT(*) style query on the writer.
func (e *Env) Count(t testing.TB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := e.Writer.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
