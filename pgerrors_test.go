package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestPgInputRejected(t *testing.T) {
	pg := func(code string) error { return &pgconn.PgError{Code: code, Message: "m"} }
	rejected := pg("22021")
	for name, err := range map[string]error{
		"bare":          rejected,
		"wrapped once":  fmt.Errorf("create: %w", rejected),
		"wrapped twice": fmt.Errorf("outer: %w", fmt.Errorf("transaction: %w", rejected)),
	} {
		if !pgInputRejected(err) {
			t.Errorf("22021 (%s) is a rejected text value", name)
		}
	}
	for _, code := range []string{"22001", "22P02", "22003", "22008", "23514", "23505", "23503",
		"40001", "40P01", "55P03", "57014", "08006", "XX000"} {
		if pgInputRejected(pg(code)) || pgInputRejected(fmt.Errorf("create: %w", pg(code))) {
			t.Errorf("%s must stay an honest failure", code)
		}
	}
	if pgInputRejected(errors.New("x")) || pgInputRejected(nil) {
		t.Error("a non-pg error or nil must stay an honest failure")
	}
}
