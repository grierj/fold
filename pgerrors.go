package main

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// SQLSTATEs the application branches on.
const (
	pgLockNotAvailable   = "55P03" // NOWAIT found the row locked
	pgQueryCanceled      = "57014" // statement_timeout or a cancel
	pgUniqueViolation    = "23505"
	pgExclusionViolation = "23P01"

	// pgCharacterNotInRepertoire is how Postgres refuses a text value's bytes or
	// characters: a NUL, or invalid UTF-8.
	pgCharacterNotInRepertoire = "22021"
)

func pgError(err error) *pgconn.PgError {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr
	}
	return nil
}

func pgCode(err error) string {
	if pgErr := pgError(err); pgErr != nil {
		return pgErr.Code
	}
	return ""
}

// pgConstraint reports whether err is a violation with the SQLSTATE code of
// the named constraint.
func pgConstraint(err error, code, constraint string) bool {
	pgErr := pgError(err)
	return pgErr != nil && pgErr.Code == code && pgErr.ConstraintName == constraint
}

// pgInputRejected reports whether Postgres refused a text value's bytes or
// characters (22021). It is the only store failure visitor text can cause on
// the two public doors (guest registration and account requests), whose
// visitor-text columns are unbounded text or citext with no CHECK. A public
// door may therefore answer it with its usual thanks (DEC-52), since a person
// cannot act on it and it must not differ by whether an account exists.
// 22001 and 22P02 are deliberately excluded: visitor text cannot cause them
// here, so they mean a code or schema bug, and 22P02's message echoes the
// rejected value. Every other class-22 code, every integrity, lock, timeout and
// internal code, and every non-pg error stays an honest failure.
func pgInputRejected(err error) bool { return pgCode(err) == pgCharacterNotInRepertoire }
