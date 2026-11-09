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
)

// pgError is the PostgreSQL error inside err, or nil.
func pgError(err error) *pgconn.PgError {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr
	}
	return nil
}

// pgCode is err's SQLSTATE, or "" when err holds no PostgreSQL error.
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
