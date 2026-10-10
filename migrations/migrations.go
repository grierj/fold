// Package migrations holds the schema's source of truth: versioned goose SQL
// files embedded into the binary (C1, DEC-22).
package migrations

import (
	"database/sql"
	"embed"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

// Files are the embedded migrations.
//
//go:embed *.sql
var Files embed.FS

// LockID is the Postgres advisory lock held while migrations run, so two
// app containers starting together never migrate at the same time.
const LockID = lock.DefaultLockID

// NewProvider returns a goose provider over the embedded migrations that
// takes the advisory lock around every run. lockOpts tune the lock wait
// (tests use a short one).
func NewProvider(db *sql.DB, lockOpts ...lock.SessionLockerOption) (*goose.Provider, error) {
	locker, err := lock.NewPostgresSessionLocker(append([]lock.SessionLockerOption{lock.WithLockID(LockID)}, lockOpts...)...)
	if err != nil {
		return nil, err
	}
	return goose.NewProvider(goose.DialectPostgres, db, Files, goose.WithSessionLocker(locker))
}
