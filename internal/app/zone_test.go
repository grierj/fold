package app_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// DEC-58: the browser's zone is kept the first time, never over a choice;
// the profile changes it and says Saved; the audit row keeps no zone.
func TestPersonTimeZone(t *testing.T) {
	s := newSite(t)
	s.finishSetup()
	mia := s.member("Mia Member", "mia@example.org")
	if r := mia.get("/profile"); !strings.Contains(r.body, "zone.js") || !strings.Contains(r.body, `<option value="America/Boise" selected>`) {
		t.Fatal("a person with no zone isn't asked, or the church's isn't shown")
	}
	if r := mia.post("/profile", "/profile/zone", url.Values{"zone": {"America/Chicago"}, "first": {"1"}}); r.status != http.StatusNoContent {
		t.Fatalf("first zone: %d", r.status)
	}
	mia.post("/profile", "/profile/zone", url.Values{"zone": {"Europe/London"}, "first": {"1"}})
	var zone string
	s.Writer.QueryRow(`SELECT zone FROM people WHERE email = 'mia@example.org'`).Scan(&zone)
	if zone != "America/Chicago" {
		t.Fatalf("zone %q; a second guess overrode the first", zone)
	}
	r := mia.get("/profile")
	if strings.Contains(r.body, "zone.js") || !strings.Contains(r.body, `<option value="America/Chicago" selected>`) {
		t.Fatal("the profile doesn't show the zone, or still asks the browser")
	}
	page := mia.get("/profile")
	tok := csrfPattern.FindStringSubmatch(page.body)[1]
	r = mia.postRaw("/profile/zone", url.Values{"zone": {"America/Denver"}, "csrf": {tok}}, http.Header{"Hx-Request": {"true"}})
	if r.status != http.StatusOK || !strings.Contains(r.body, "Saved.") || strings.Contains(r.body, "<html") {
		t.Fatalf("changing it: %d", r.status)
	}
	if r := mia.post("/profile", "/profile/zone", url.Values{"zone": {"Local"}}); r.status != http.StatusUnprocessableEntity || !strings.Contains(r.body, "Choose a time zone from the list.") {
		t.Fatalf("a bogus zone: %d", r.status)
	}
	var details string
	s.Writer.QueryRow(`SELECT details FROM audit_log WHERE action = 'profile.zone' ORDER BY seq DESC LIMIT 1`).Scan(&details)
	if strings.Contains(details, "America") || s.Count(t, `SELECT count(*) FROM audit_log WHERE action = 'profile.zone'`) != 2 {
		t.Fatalf("audit: %s", details)
	}
	// An alias a browser may send loads, and the select still shows it.
	s.Writer.Exec(`UPDATE people SET zone = 'US/Mountain' WHERE email = 'mia@example.org'`)
	if r := mia.get("/profile"); !strings.Contains(r.body, `<option value="US/Mountain" selected>`) {
		t.Fatal("an alias zone isn't shown")
	}
}

// A person's own profile changes are limited, so one account cannot keep
// the one writer busy; the browser's guess never writes over a zone.
func TestProfileChangesAreLimited(t *testing.T) {
	s := newSite(t)
	s.finishSetup()
	mia := s.member("Mia Member", "mia@example.org")
	mia.post("/profile", "/profile/zone", url.Values{"zone": {"America/Chicago"}})
	var before int
	s.Writer.QueryRow(`SELECT count(*) FROM audit_log`).Scan(&before)
	if r := mia.post("/profile", "/profile/zone", url.Values{"zone": {"Europe/London"}, "first": {"1"}}); r.status != http.StatusNoContent {
		t.Fatalf("guess over a zone: %d", r.status)
	}
	var after int
	s.Writer.QueryRow(`SELECT count(*) FROM audit_log`).Scan(&after)
	if after != before {
		t.Fatal("a guess over a chosen zone still ran a write")
	}
	var last resp
	for range 30 {
		last = mia.post("/profile", "/profile/zone", url.Values{"zone": {"America/Denver"}})
	}
	if last.status != http.StatusUnprocessableEntity || !strings.Contains(last.body, "could not be saved") {
		t.Fatalf("change past the limit: %d", last.status)
	}
	if r := mia.post("/profile", "/profile/name", url.Values{"name": {"Mia M"}, "version": {"1"}}); !strings.Contains(r.body, "could not be saved") {
		t.Fatal("the name isn't limited with the rest of the profile")
	}
}
