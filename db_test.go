package main

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"fold/internal/dbopen"
	"fold/internal/policy"
	"fold/models"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestDatabaseLoggerKeepsContactValuesOutOfSQL(t *testing.T) {
	const privateReason = "Private reason marker: sql-private@example.invalid +1-555-987-6543"
	assertLog := func(t *testing.T, output string, diagnostics ...string) {
		t.Helper()
		for _, secret := range []string{privateReason, "sql-private@example.invalid", "+1-555-987-6543"} {
			if strings.Contains(output, secret) {
				t.Fatal("SQL diagnostic contains private reason/contact value")
			}
		}
		for _, diagnostic := range diagnostics {
			if !strings.Contains(output, diagnostic) {
				t.Fatalf("SQL diagnostic missing %q (captured output withheld)", diagnostic)
			}
		}
	}
	capture := func(f *contactAPIFixture) *bytes.Buffer {
		output := new(bytes.Buffer)
		// The exact production logger configuration, changing only its writer.
		f.app.DB = f.app.DB.Session(&gorm.Session{Logger: dbopen.Logger(log.New(output, "\r\n", log.LstdFlags))})
		return output
	}
	t.Run("errored-insert", func(t *testing.T) {
		f := newContactAPIFixture(t, false)
		output := capture(f)
		w := f.lc.do("POST", "/api/v1/contact-requests", map[string]any{"subjectUserId": f.subject.ID, "contextType": "group", "contextId": f.group.ID, "fields": "both", "reason": privateReason + "\x00"}, true)
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("NUL insert: %d", w.Code)
		}
		assertLog(t, output.String(), `INSERT INTO "contact_requests"`, "$1", "SQLSTATE 22021")
		if contactCount(t, f.app, &models.ContactRequest{}, "1 = 1") != 0 {
			t.Fatal("invalid text insert committed")
		}
	})
	t.Run("slow-successful-writes", func(t *testing.T) {
		f := newContactAPIFixture(t, false)
		// Real PostgreSQL latency above the unchanged production 200ms threshold;
		// no fake logger clock or lower threshold can hide configuration drift.
		if err := f.app.DB.Exec(`CREATE FUNCTION test_slow_contact_write() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN PERFORM pg_sleep(0.25); RETURN NEW; END $$`).Error; err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := f.app.DB.Exec(`DROP FUNCTION test_slow_contact_write() CASCADE`).Error; err != nil {
				t.Error(err)
			}
		})
		if err := f.app.DB.Exec(`CREATE TRIGGER test_slow_contact_write BEFORE INSERT OR UPDATE ON contact_requests FOR EACH ROW EXECUTE FUNCTION test_slow_contact_write()`).Error; err != nil {
			t.Fatal(err)
		}
		output := capture(f)
		r := f.create(t, "both", privateReason)
		assertLog(t, output.String(), "SLOW SQL >= 200ms", `INSERT INTO "contact_requests"`, "$1")
		output.Reset()
		if w := contactDecision(f.sc, r.ID, "subject", "denied"); w.Code != http.StatusOK {
			t.Fatalf("decision: %d", w.Code)
		}
		assertLog(t, output.String(), "SLOW SQL >= 200ms", `UPDATE "contact_requests"`, "$1")
		// A domain lifecycle Save also persists the reason; central logging must
		// protect it, not just writes made directly by contact API endpoints.
		pending := f.create(t, "both", privateReason)
		output.Reset()
		if w := confirmDeparture(t, f.sc, "POST", "/api/v1/groups/"+itoa(f.group.ID)+"/leave", "/api/v1/groups/"+itoa(f.group.ID)+"/departure-preview", ""); w.Code != http.StatusOK {
			t.Fatalf("leave: %d", w.Code)
		}
		assertLog(t, output.String(), "SLOW SQL >= 200ms", `UPDATE "contact_requests"`, "$1")
		if contactCount(t, f.app, &models.ContactRequest{}, "id = ? AND status = ?", pending.ID, policy.ContactEnded) != 1 {
			t.Fatal("domain hook did not end pending request")
		}
	})
}

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
	callerLine := regexp.MustCompile(`db_test\.go:\d+`)
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
	t.Run("driver savepoint error", func(t *testing.T) {
		var logs bytes.Buffer
		tx := session(&logs).Begin()
		if tx.Error != nil {
			t.Fatal(tx.Error)
		}
		tx.SavePoint("invalid-name")
		location(t, logs.String(), "SAVEPOINT invalid-name")
		tx.Rollback()
	})
	t.Run("driver savepoint and nested transaction at info level", func(t *testing.T) {
		var logs bytes.Buffer
		db := f.app.DB.Session(&gorm.Session{Logger: dbopen.Logger(log.New(&logs, "\r\n", log.LstdFlags)).LogMode(logger.Info)})
		tx := db.Begin()
		if tx.Error != nil {
			t.Fatal(tx.Error)
		}
		t.Cleanup(func() { tx.Rollback() })
		tx.SavePoint("caller_check")
		location(t, logs.String(), "SAVEPOINT caller_check")
		logs.Reset()
		tx.RollbackTo("caller_check")
		location(t, logs.String(), "ROLLBACK TO SAVEPOINT caller_check")
		logs.Reset()
		if err := tx.Transaction(func(inner *gorm.DB) error { return inner.Exec("SELECT 1").Error }); err != nil {
			t.Fatal(err)
		}
		location(t, logs.String(), "SAVEPOINT")
	})
	if errorLoc == "" || slowLoc == "" || scopedLoc == "" {
		t.Fatal("a probe reported no caller location")
	}
	if errorLoc == slowLoc || slowLoc == scopedLoc || errorLoc == scopedLoc {
		t.Fatalf("probes share a reported caller (%s %s %s): distinct callers are not distinguished", errorLoc, slowLoc, scopedLoc)
	}
}
