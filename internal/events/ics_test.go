package events

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ics "github.com/arran4/golang-ical"
)

// Golden files pin the exact wire format. Regenerate them with
// UPDATE_GOLDEN=1 go test ./internal/events/ after looking at the diff;
// every golden is also parsed back below, so a refresh cannot bless
// output a calendar parser rejects.

func utc(y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, time.UTC)
}

func checkGolden(t *testing.T, name string, f Feed) *ics.Calendar {
	t.Helper()
	got := RenderFeed(f)
	path := filepath.Join("testdata", name)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden: %v (run with UPDATE_GOLDEN=1 to create it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs from the golden file\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
	cal, err := ics.ParseCalendar(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("%s does not parse: %v\n%s", name, err, got)
	}
	return cal
}

func prop(t *testing.T, ev *ics.VEvent, p ics.ComponentProperty) string {
	t.Helper()
	got := ev.GetProperty(p)
	if got == nil {
		t.Fatalf("event %s has no %s", ev.GetProperty(ics.ComponentPropertyUniqueId), p)
	}
	return got.Value
}

func TestICSWeeklySeriesWithExdateAndOverride(t *testing.T) {
	boise, err := time.LoadLocation("America/Boise")
	if err != nil {
		t.Fatal(err)
	}
	cal := checkGolden(t, "weekly.ics", Feed{
		Name:        "Fold",
		Host:        "church.example.org",
		WindowStart: utc(2026, 10, 1, 0, 0),
		WindowEnd:   utc(2027, 10, 1, 0, 0),
		Events: []FeedEvent{{
			ID: 7, Title: "Tuesday study", Description: "Bring a Bible.",
			Location: "Room 2", TZID: "America/Boise", StartsLocal: "2026-11-03T19:00",
			DurationMinutes: 60, RRule: "FREQ=WEEKLY;BYDAY=TU",
			URL:            "https://church.example.org/events/7",
			UpdatedAt:      utc(2026, 10, 20, 12, 0),
			CancelledDates: []string{"2026-11-17"},
			Overrides: []FeedOccurrence{{
				OriginalDate: "2026-11-24", Title: "Pizza night", Location: "Hall",
				StartsAt:  time.Date(2026, 11, 24, 18, 30, 0, 0, boise),
				EndsAt:    time.Date(2026, 11, 24, 19, 30, 0, 0, boise),
				UpdatedAt: utc(2026, 10, 21, 9, 0),
			}},
		}},
	})
	evs := cal.Events()
	if len(evs) != 2 {
		t.Fatalf("parsed %d events, want the series and its override", len(evs))
	}
	series, over := evs[0], evs[1]
	if got := prop(t, series, ics.ComponentPropertyUniqueId); got != "event-7@church.example.org" {
		t.Fatalf("series UID = %q", got)
	}
	if got := prop(t, series, ics.ComponentPropertyRrule); got != "FREQ=WEEKLY;BYDAY=TU" {
		t.Fatalf("RRULE = %q", got)
	}
	if got := prop(t, series, ics.ComponentPropertyExdate); got != "20261117T190000" {
		t.Fatalf("EXDATE = %q", got)
	}
	if got := prop(t, series, ics.ComponentPropertyDtStart); got != "20261103T190000" {
		t.Fatalf("DTSTART = %q", got)
	}
	if got := prop(t, over, ics.ComponentPropertyUniqueId); got != "event-7@church.example.org" {
		t.Fatalf("override UID = %q, want the series UID", got)
	}
	if got := prop(t, over, ics.ComponentPropertyRecurrenceId); got != "20261124T190000" {
		t.Fatalf("RECURRENCE-ID = %q", got)
	}
	if got := prop(t, over, ics.ComponentPropertyDtStart); got != "20261124T183000" {
		t.Fatalf("override DTSTART = %q", got)
	}
	if got := prop(t, over, ics.ComponentPropertySummary); got != "Pizza night" {
		t.Fatalf("override SUMMARY = %q", got)
	}
	desc := prop(t, series, ics.ComponentPropertyDescription)
	if !strings.Contains(desc, "Bring a Bible.") || !strings.Contains(desc, "https://church.example.org/events/7") {
		t.Fatalf("DESCRIPTION = %q, want the text and the event URL", desc)
	}
	if len(cal.Timezones()) != 1 {
		t.Fatalf("parsed %d VTIMEZONEs, want 1", len(cal.Timezones()))
	}
}

func TestICSOneTimeAndCancelledOneTimeOmitted(t *testing.T) {
	cal := checkGolden(t, "one-time.ics", Feed{
		Name:        "Fold",
		Host:        "church.example.org",
		WindowStart: utc(2026, 11, 1, 0, 0),
		WindowEnd:   utc(2027, 11, 1, 0, 0),
		Events: []FeedEvent{
			{
				ID: 3, Title: "Church picnic", Location: "The park", TZID: "UTC",
				StartsLocal: "2026-12-05T12:00", DurationMinutes: 120,
				UpdatedAt: utc(2026, 11, 1, 8, 0),
			},
			{
				ID: 4, Title: "Cancelled picnic", TZID: "UTC",
				StartsLocal: "2026-12-06T12:00", DurationMinutes: 60,
				UpdatedAt:      utc(2026, 11, 1, 8, 0),
				CancelledDates: []string{"2026-12-06"},
			},
		},
	})
	evs := cal.Events()
	if len(evs) != 1 {
		t.Fatalf("parsed %d events, want only the picnic that is still on", len(evs))
	}
	if got := prop(t, evs[0], ics.ComponentPropertySummary); got != "Church picnic" {
		t.Fatalf("SUMMARY = %q", got)
	}
	if evs[0].GetProperty(ics.ComponentPropertyRrule) != nil {
		t.Fatal("a one-time event carries an RRULE")
	}
}

func TestICSEscaping(t *testing.T) {
	cal := checkGolden(t, "escaping.ics", Feed{
		Name:        "Fold, main",
		Host:        "church.example.org",
		WindowStart: utc(2026, 11, 1, 0, 0),
		WindowEnd:   utc(2027, 11, 1, 0, 0),
		Events: []FeedEvent{{
			ID: 9, Title: "Dinner, study; prayer\\worship",
			Description: "First line\nSecond, line; with \\ backslash",
			Location:    "Smith home, 12 Oak St; ring twice", TZID: "UTC",
			StartsLocal: "2026-12-01T18:00", DurationMinutes: 90,
			UpdatedAt: utc(2026, 11, 2, 8, 0),
		}},
	})
	ev := cal.Events()[0]
	if got := ics.FromText(prop(t, ev, ics.ComponentPropertySummary)); got != "Dinner, study; prayer\\worship" {
		t.Fatalf("unescaped SUMMARY = %q", got)
	}
	if got := ics.FromText(prop(t, ev, ics.ComponentPropertyLocation)); got != "Smith home, 12 Oak St; ring twice" {
		t.Fatalf("unescaped LOCATION = %q", got)
	}
}

func TestICSUntilConversion(t *testing.T) {
	cal := checkGolden(t, "until.ics", Feed{
		Name:        "Fold",
		Host:        "church.example.org",
		WindowStart: utc(2026, 10, 1, 0, 0),
		WindowEnd:   utc(2027, 10, 1, 0, 0),
		Events: []FeedEvent{{
			ID: 5, Title: "Advent study", TZID: "America/Boise", StartsLocal: "2026-12-01T19:00",
			DurationMinutes: 60, RRule: "FREQ=WEEKLY;BYDAY=TU", RRuleUntil: "2026-12-29",
			UpdatedAt: utc(2026, 10, 20, 12, 0),
		}},
	})
	got := prop(t, cal.Events()[0], ics.ComponentPropertyRrule)
	want := "FREQ=WEEKLY;BYDAY=TU;UNTIL=20261230T065959Z"
	if got != want {
		t.Fatalf("RRULE = %q, want %q (end of 2026-12-29 in Boise, in UTC)", got, want)
	}
}

func TestICSStandaloneSingles(t *testing.T) {
	cal := checkGolden(t, "singles.ics", Feed{
		Name:        "Fold",
		Host:        "church.example.org",
		WindowStart: utc(2026, 11, 1, 0, 0),
		WindowEnd:   utc(2027, 11, 1, 0, 0),
		Singles: []FeedSingle{
			{
				EventID: 11, OriginalDate: "2026-12-02", Title: "Choir practice",
				Location: "Sanctuary", TZID: "UTC",
				StartsAt: utc(2026, 12, 2, 19, 0), EndsAt: utc(2026, 12, 2, 20, 0),
				URL:       "https://church.example.org/events/11",
				UpdatedAt: utc(2026, 11, 1, 8, 0), Status: "TENTATIVE",
			},
			{
				EventID: 11, OriginalDate: "2026-12-09", Title: "Choir practice",
				Location: "Sanctuary", TZID: "UTC",
				StartsAt: utc(2026, 12, 9, 19, 0), EndsAt: utc(2026, 12, 9, 20, 0),
				URL:       "https://church.example.org/events/11",
				UpdatedAt: utc(2026, 11, 1, 8, 0), Status: "CONFIRMED",
			},
		},
	})
	evs := cal.Events()
	if len(evs) != 2 {
		t.Fatalf("parsed %d events, want 2", len(evs))
	}
	if got := prop(t, evs[0], ics.ComponentPropertyUniqueId); got != "event-11-20261202@church.example.org" {
		t.Fatalf("single UID = %q", got)
	}
	if got := prop(t, evs[0], ics.ComponentPropertyStatus); got != "TENTATIVE" {
		t.Fatalf("waitlisted STATUS = %q", got)
	}
	if got := prop(t, evs[1], ics.ComponentPropertyStatus); got != "CONFIRMED" {
		t.Fatalf("going STATUS = %q", got)
	}
	for _, ev := range evs {
		if ev.GetProperty(ics.ComponentPropertyRrule) != nil {
			t.Fatal("a standalone date carries the whole series rule")
		}
	}
}

func TestICSUnsafeRuleFallsBackToSingleDates(t *testing.T) {
	cal := checkGolden(t, "fallback.ics", Feed{
		Name:        "Fold",
		Host:        "church.example.org",
		WindowStart: utc(2026, 11, 1, 0, 0),
		WindowEnd:   utc(2027, 11, 1, 0, 0),
		Events: []FeedEvent{{
			ID: 13, Title: "Odd series", TZID: "UTC", StartsLocal: "2026-12-01T19:00",
			DurationMinutes: 60, RRule: "FREQ=FORTNIGHTLY", UpdatedAt: utc(2026, 11, 1, 8, 0),
			Occurrences: []FeedOccurrence{
				{OriginalDate: "2026-12-01", StartsAt: utc(2026, 12, 1, 19, 0),
					EndsAt: utc(2026, 12, 1, 20, 0), UpdatedAt: utc(2026, 11, 1, 8, 0)},
				{OriginalDate: "2026-12-15", StartsAt: utc(2026, 12, 15, 19, 0),
					EndsAt: utc(2026, 12, 15, 20, 0), UpdatedAt: utc(2026, 11, 1, 8, 0)},
			},
		}},
	})
	evs := cal.Events()
	if len(evs) != 2 {
		t.Fatalf("parsed %d events, want the 2 stored dates", len(evs))
	}
	for _, ev := range evs {
		if ev.GetProperty(ics.ComponentPropertyRrule) != nil {
			t.Fatal("the unsafe rule reached the feed")
		}
	}
}

func TestICSRenderIsDeterministic(t *testing.T) {
	f := Feed{
		Name: "Fold", Host: "church.example.org",
		WindowStart: utc(2026, 10, 1, 0, 0), WindowEnd: utc(2027, 10, 1, 0, 0),
		Events: []FeedEvent{
			{ID: 2, Title: "Later id", TZID: "UTC", StartsLocal: "2026-12-01T19:00",
				DurationMinutes: 60, UpdatedAt: utc(2026, 11, 1, 8, 0)},
			{ID: 1, Title: "Earlier id", TZID: "UTC", StartsLocal: "2026-12-01T19:00",
				DurationMinutes: 60, UpdatedAt: utc(2026, 11, 1, 8, 0)},
		},
	}
	a, b := RenderFeed(f), RenderFeed(f)
	if !bytes.Equal(a, b) {
		t.Fatal("two renders of the same feed differ (the ETag would never match)")
	}
	if strings.Index(string(a), "event-1@") > strings.Index(string(a), "event-2@") {
		t.Fatal("events are not sorted by id")
	}
	if strings.Contains(string(a), "\n") && !strings.Contains(string(a), "\r\n") {
		t.Fatal("lines do not end with CRLF")
	}
	for _, line := range strings.Split(string(a), "\r\n") {
		if len(line) > 75 {
			t.Fatalf("line over 75 octets: %q", line)
		}
	}
}
