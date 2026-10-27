package main

import (
	"bytes"
	"log"
	"net/http"
	"strings"
	"testing"

	"fold/internal/policy"
	"fold/models"

	"gorm.io/gorm"
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
		f.app.DB = f.app.DB.Session(&gorm.Session{Logger: databaseLogger(log.New(output, "\r\n", log.LstdFlags))})
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
