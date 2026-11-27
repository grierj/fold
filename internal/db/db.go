// Package db opens the one SQLite file: a single writer connection, handed
// only to package write, and a pool of read-only connections for pages.
// Nothing else opens the database.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"

	_ "modernc.org/sqlite" // the pure-Go driver, so the binary builds with CGO_ENABLED=0
)

// Pragmas set on every connection through the DSN, so a connection the pool
// opens later gets them too. Foreign keys are off in SQLite unless asked for.
var pragmas = []string{
	"journal_mode(WAL)",
	"foreign_keys(1)",
	"busy_timeout(10000)",
	"synchronous(NORMAL)",
}

func dsn(path string, readOnly bool) string {
	q := url.Values{}
	for _, p := range pragmas {
		q.Add("_pragma", p)
	}
	if readOnly {
		q.Add("_pragma", "query_only(1)")
	} else {
		// BEGIN IMMEDIATE takes the write lock at the start, so a second
		// process (an offline restore) waits instead of failing midway.
		q.Set("_txlock", "immediate")
	}
	return "file:" + path + "?" + q.Encode()
}

// OpenWriter opens the single writer connection.
func OpenWriter(path string) (*sql.DB, error) {
	w, err := sql.Open("sqlite", dsn(path, false))
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	w.SetConnMaxLifetime(0)
	w.SetConnMaxIdleTime(0)
	if err := w.Ping(); err != nil {
		w.Close()
		return nil, fmt.Errorf("db: open %s: %w", path, err)
	}
	return w, nil
}

// Reader is the read-only pool. Its connections refuse writes
// (query_only), so a page cannot change anything even by mistake.
type Reader struct {
	db *sql.DB
}

func OpenReader(path string) (*Reader, error) {
	r, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		return nil, err
	}
	r.SetMaxOpenConns(4)
	r.SetMaxIdleConns(4)
	if err := r.Ping(); err != nil {
		r.Close()
		return nil, fmt.Errorf("db: open %s for reading: %w", path, err)
	}
	return &Reader{db: r}, nil
}

func (r *Reader) Close() error { return r.db.Close() }

// Read runs fn in one read transaction: one consistent snapshot for the whole
// page (AGENTS.md, "One read per page").
func (r *Reader) Read(ctx context.Context, fn func(q *sql.Tx) error) error {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// Querier is what both a read and a write transaction offer.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}
