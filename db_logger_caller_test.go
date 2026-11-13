package main

import (
	"bytes"
	"context"
	"log"
	"regexp"
	"strings"
	"testing"

	"fold/internal/dbopen"

	"gorm.io/gorm"
)

// TestProductionLoggerKeepsRealCallerLocations pins the T301-S4-R2 fix: the
// production logger reports the real application query caller, not its own
// forwarding frame, on an unrelated error and a successful slow trace outside
// any scope, and on an unexpected failure inside the legacy save scope.
func TestProductionLoggerKeepsRealCallerLocations(t *testing.T) {
	f := newContactAPIFixture(t, false)
	session := func(output *bytes.Buffer) *gorm.DB {
		return f.app.DB.Session(&gorm.Session{Logger: dbopen.Logger(log.New(output, "\r\n", log.LstdFlags))})
	}
	// Each probe issues its query from its own line, so equal reported
	// locations would mean the logger stopped distinguishing callers.
	unrelatedError := func(db *gorm.DB) error {
		return db.Exec("SELECT * FROM legacy_caller_missing_table_xyz").Error
	}
	slowSuccess := func(db *gorm.DB) error {
		return db.Exec("SELECT pg_sleep(0.25)").Error
	}
	scopedUnexpectedError := func(db *gorm.DB) error {
		return db.WithContext(dbopen.WithLegacySaveBusyScope(context.Background())).Exec("SELECT * FROM legacy_caller_other_missing_xyz").Error
	}
	callerLine := regexp.MustCompile(`db_logger_caller_test\.go:\d+`)
	location := func(t *testing.T, out, want string) string {
		t.Helper()
		if !strings.Contains(out, want) {
			t.Fatalf("diagnostic missing %q (captured output withheld)", want)
		}
		if strings.Contains(out, "dbopen.go") {
			t.Fatal("diagnostic reports the forwarding logger instead of the query caller")
		}
		line := callerLine.FindString(out)
		if line == "" {
			t.Fatal("diagnostic reports no caller location in this file")
		}
		return line
	}
	var errorLoc, slowLoc, scopedLoc string
	t.Run("unrelated error outside the scope", func(t *testing.T) {
		var logs bytes.Buffer
		if err := unrelatedError(session(&logs)); err == nil {
			t.Fatal("missing-table query succeeded")
		}
		errorLoc = location(t, logs.String(), "legacy_caller_missing_table_xyz")
	})
	t.Run("successful slow trace outside the scope", func(t *testing.T) {
		var logs bytes.Buffer
		if err := slowSuccess(session(&logs)); err != nil {
			t.Fatalf("pg_sleep: %v", err)
		}
		out := logs.String()
		if !strings.Contains(out, "SLOW SQL >= 200ms") {
			t.Fatal("slow query produced no slow diagnostic (captured output withheld)")
		}
		slowLoc = location(t, out, "pg_sleep")
	})
	t.Run("unexpected error inside the scope", func(t *testing.T) {
		var logs bytes.Buffer
		if err := scopedUnexpectedError(session(&logs)); err == nil {
			t.Fatal("missing-table query succeeded")
		}
		scopedLoc = location(t, logs.String(), "legacy_caller_other_missing_xyz")
	})
	if errorLoc == "" || slowLoc == "" || scopedLoc == "" {
		t.Fatal("a probe reported no caller location")
	}
	if errorLoc == slowLoc || slowLoc == scopedLoc || errorLoc == scopedLoc {
		t.Fatalf("probes share a reported caller (%s %s %s): distinct callers are not distinguished", errorLoc, slowLoc, scopedLoc)
	}
}
