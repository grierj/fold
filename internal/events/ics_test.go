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

// Stored dates are the entire wire contract. The cancelled November 17 date
// is absent; the moved November 24 date retains its original-date UID.
func TestICSStoredDatesWithDSTAndReschedule(t *testing.T) {
	boise, err := time.LoadLocation("America/Boise")
	if err != nil {
		t.Fatal(err)
	}
	date := func(original string, day, hour int, title, location string) FeedSingle {
		originalDay, err := time.Parse("2006-01-02", original)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Date(2026, originalDay.Month(), day, hour, 0, 0, 0, boise)
		return FeedSingle{EventID: 7, OriginalDate: original, Title: title, Description: "Bring a Bible.",
			Location: location, TZID: "America/Boise", StartsAt: start, EndsAt: start.Add(time.Hour),
			URL: "https://church.example.org/events/7/dates/" + original, UpdatedAt: utc(2026, 10, 20, 12, 0)}
	}
	cal := checkGolden(t, "weekly.ics", Feed{Name: "Fold", Host: "church.example.org",
		WindowStart: utc(2026, 10, 1, 0, 0), WindowEnd: utc(2027, 10, 1, 0, 0),
		Singles: []FeedSingle{
			date("2026-10-27", 27, 19, "Tuesday study", "Room 2"),
			date("2026-11-03", 3, 19, "Tuesday study", "Room 2"),
			date("2026-11-24", 25, 18, "Pizza night", ""),
		}})
	evs := cal.Events()
	if len(evs) != 3 {
		t.Fatalf("got %d dates, want 3", len(evs))
	}
	for _, ev := range evs {
		for _, p := range []ics.ComponentProperty{ics.ComponentPropertyRrule, ics.ComponentPropertyExdate, ics.ComponentPropertyRecurrenceId} {
			if ev.GetProperty(p) != nil {
				t.Fatalf("stored date has %s", p)
			}
		}
	}
	if got := prop(t, evs[2], ics.ComponentPropertyUniqueId); got != "event-7-20261124@church.example.org" {
		t.Fatal(got)
	}
	if got := prop(t, evs[2], ics.ComponentPropertyDtStart); got != "20261125T180000" {
		t.Fatal(got)
	}
	if evs[2].GetProperty(ics.ComponentPropertyLocation) != nil {
		t.Fatal("empty date location was replaced")
	}
	if len(cal.Timezones()) != 1 {
		t.Fatal("missing Boise timezone")
	}
	out := string(RenderFeed(Feed{WindowStart: utc(2026, 10, 1, 0, 0), WindowEnd: utc(2027, 10, 1, 0, 0), Singles: []FeedSingle{date("2026-11-03", 3, 19, "Study", "")}}))
	if !strings.Contains(out, "TZOFFSETFROM:-0600") || !strings.Contains(out, "TZOFFSETTO:-0700") {
		t.Fatal("missing daylight/standard offsets", out)
	}
}

func TestICSOneTime(t *testing.T) {
	cal := checkGolden(t, "one-time.ics", Feed{Name: "Fold", Host: "church.example.org",
		WindowStart: utc(2026, 11, 1, 0, 0), WindowEnd: utc(2027, 11, 1, 0, 0),
		Singles: []FeedSingle{{EventID: 3, OriginalDate: "2026-12-05", Title: "Church picnic", Location: "The park", TZID: "UTC",
			StartsAt: utc(2026, 12, 5, 12, 0), EndsAt: utc(2026, 12, 5, 14, 0), UpdatedAt: utc(2026, 11, 1, 8, 0)}}})
	if len(cal.Events()) != 1 {
		t.Fatal("expected one date")
	}
	if got := prop(t, cal.Events()[0], ics.ComponentPropertyUniqueId); got != "event-3-20261205@church.example.org" {
		t.Fatal(got)
	}
}

func TestICSEscaping(t *testing.T) {
	cal := checkGolden(t, "escaping.ics", Feed{Name: "Fold, main", Host: "church.example.org",
		WindowStart: utc(2026, 11, 1, 0, 0), WindowEnd: utc(2027, 11, 1, 0, 0),
		Singles: []FeedSingle{{EventID: 9, OriginalDate: "2026-12-01", Title: "Dinner, study; prayer\\worship",
			Description: "First line\nSecond, line; with \\ backslash", Location: "Smith home, 12 Oak St; ring twice", TZID: "UTC",
			StartsAt: utc(2026, 12, 1, 18, 0), EndsAt: utc(2026, 12, 1, 19, 30), UpdatedAt: utc(2026, 11, 2, 8, 0)}}})
	ev := cal.Events()[0]
	if got := ics.FromText(prop(t, ev, ics.ComponentPropertySummary)); got != "Dinner, study; prayer\\worship" {
		t.Fatal(got)
	}
	if got := ics.FromText(prop(t, ev, ics.ComponentPropertyLocation)); got != "Smith home, 12 Oak St; ring twice" {
		t.Fatal(got)
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

func TestICSPersonalExpandedSeriesUsesStableDateUIDs(t *testing.T) {
	cal := checkGolden(t, "personal-expanded.ics", Feed{
		Name:        "My Fold calendar",
		Host:        "church.example.org",
		WindowStart: utc(2026, 11, 1, 0, 0),
		WindowEnd:   utc(2027, 11, 1, 0, 0),
		Singles: []FeedSingle{
			{
				EventID: 17, OriginalDate: "2026-12-01", Title: "Tuesday study", TZID: "America/Boise",
				StartsAt:  time.Date(2026, 12, 1, 19, 0, 0, 0, time.FixedZone("MST", -7*60*60)),
				EndsAt:    time.Date(2026, 12, 1, 20, 0, 0, 0, time.FixedZone("MST", -7*60*60)),
				UpdatedAt: utc(2026, 11, 20, 8, 0), Status: "CONFIRMED",
			},
			{
				EventID: 17, OriginalDate: "2026-12-08", Title: "Tuesday study", TZID: "America/Boise",
				StartsAt:  time.Date(2026, 12, 8, 19, 0, 0, 0, time.FixedZone("MST", -7*60*60)),
				EndsAt:    time.Date(2026, 12, 8, 20, 0, 0, 0, time.FixedZone("MST", -7*60*60)),
				UpdatedAt: utc(2026, 11, 20, 8, 0), Status: "TENTATIVE",
			},
		},
	})
	evs := cal.Events()
	if len(evs) != 2 {
		t.Fatalf("parsed %d events, want two standalone dates", len(evs))
	}
	for i, wantUID := range []string{"event-17-20261201@church.example.org", "event-17-20261208@church.example.org"} {
		if got := prop(t, evs[i], ics.ComponentPropertyUniqueId); got != wantUID {
			t.Fatalf("standalone UID = %q, want %q", got, wantUID)
		}
		if evs[i].GetProperty(ics.ComponentPropertyRecurrenceId) != nil {
			t.Fatal("expanded personal date carries a RECURRENCE-ID")
		}
		if evs[i].GetProperty(ics.ComponentPropertyRrule) != nil {
			t.Fatal("expanded personal date carries an RRULE")
		}
	}
}

func TestICSRenderIsDeterministic(t *testing.T) {
	f := Feed{Name: "Fold", Host: "church.example.org", WindowStart: utc(2026, 10, 1, 0, 0), WindowEnd: utc(2027, 10, 1, 0, 0),
		Singles: []FeedSingle{
			{EventID: 2, OriginalDate: "2026-12-01", Title: strings.Repeat("é", 60), TZID: "UTC", StartsAt: utc(2026, 12, 1, 19, 0)},
			{EventID: 1, OriginalDate: "2026-12-01", Title: "Earlier id", TZID: "UTC", StartsAt: utc(2026, 12, 1, 19, 0)},
		}}
	a := RenderFeed(f)
	f.Singles[0], f.Singles[1] = f.Singles[1], f.Singles[0]
	if !bytes.Equal(a, RenderFeed(f)) {
		t.Fatal("input order changed rendered bytes")
	}
	if strings.Index(string(a), "event-1-") > strings.Index(string(a), "event-2-") {
		t.Fatal("dates are not sorted")
	}
	for _, line := range strings.Split(string(a), "\r\n") {
		if len(line) > 75 {
			t.Fatalf("line over 75 octets: %q", line)
		}
	}
	if _, err := ics.ParseCalendar(bytes.NewReader(a)); err != nil {
		t.Fatal(err)
	}
}

func TestICSDateParticipationDetails(t *testing.T) {
	link := "https://meet.example.org/choir-abc123def4"
	f := Feed{Host: "church.example.org", Singles: []FeedSingle{
		{EventID: 1, OriginalDate: "2026-12-01", Title: "Family Night", TZID: "UTC", StartsAt: utc(2026, 12, 1, 19, 0),
			RemoteAvailable: true, ChildcareAvailable: true, ChildcareAges: "Ages 2–5", VideoURL: link},
		{EventID: 1, OriginalDate: "2026-12-08", Title: "In person only", TZID: "UTC", StartsAt: utc(2026, 12, 8, 19, 0)},
	}}
	out := strings.ReplaceAll(string(RenderFeed(f)), "\r\n ", "")
	for _, text := range []string{"Remote attendance available", "Childcare available (Ages 2–5)", "Join by video: " + link} {
		if strings.Count(out, text) != 1 {
			t.Fatalf("per-date detail %q: %s", text, out)
		}
	}
	f.Singles[0].RemoteAvailable = false
	f.Singles[0].ChildcareAvailable = false
	f.Singles[0].VideoURL = ""
	out = string(RenderFeed(f))
	for _, text := range []string{"Remote attendance", "Childcare", "Join by video", link} {
		if strings.Contains(out, text) {
			t.Fatalf("unset %q reached feed", text)
		}
	}
}

func TestEscapeTextDropsControlCharacters(t *testing.T) {
	got := escapeText("a\x00b\x1bc\x7fd\te;f,g\\h\r\ni")
	want := `abcd` + "\t" + `e\;f\,g\\h\ni`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
