package events

import (
	"testing"
	"time"
)

// A weekly 7:00 pm event stays at 7:00 pm local on both sides of a
// daylight-saving change (C2). America/Boise's 2026 fall-back is Nov 1 and
// its 2027 spring-forward is Mar 14. Phoenix never changes. London's
// transitions are the last Sundays of October and March.
func TestWeeklyLocalTimeAcrossDST(t *testing.T) {
	type sample struct {
		date    string
		utcHour int
	}
	cases := []struct {
		tzid    string
		samples []sample
	}{
		{"America/Boise", []sample{{"2026-10-28", 1}, {"2026-11-04", 2}, {"2027-03-10", 2}, {"2027-03-17", 1}}},
		{"America/New_York", []sample{{"2026-10-28", 23}, {"2026-11-04", 0}, {"2027-03-10", 0}, {"2027-03-17", 23}}},
		{"America/Phoenix", []sample{{"2026-10-28", 2}, {"2026-11-04", 2}, {"2027-03-10", 2}, {"2027-03-17", 2}}},
		{"Europe/London", []sample{{"2026-10-21", 18}, {"2026-10-28", 19}, {"2027-03-24", 19}, {"2027-03-31", 18}}},
	}
	for _, c := range cases {
		got, err := Expand(Spec{
			StartsLocal: c.samples[0].date + "T19:00", DurationMinutes: 90,
			TZID: c.tzid, RRule: "FREQ=WEEKLY",
		}, time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatalf("%s: %v", c.tzid, err)
		}
		byDate := map[string]When{}
		for _, w := range got {
			byDate[w.OriginalDate] = w
		}
		for _, s := range c.samples {
			w, ok := byDate[s.date]
			if !ok {
				t.Errorf("%s: missing %s", c.tzid, s.date)
				continue
			}
			if w.StartsLocal != s.date+"T19:00" {
				t.Errorf("%s %s startsLocal = %s, want %sT19:00", c.tzid, s.date, w.StartsLocal, s.date)
			}
			if w.StartsAt.UTC().Hour() != s.utcHour {
				t.Errorf("%s %s UTC hour = %d, want %d (%s)", c.tzid, s.date, w.StartsAt.UTC().Hour(), s.utcHour, w.StartsAt.UTC())
			}
			if w.EndsLocal[11:] != "20:30" {
				t.Errorf("%s %s endsLocal = %s", c.tzid, s.date, w.EndsLocal)
			}
		}
	}
}

func TestOneTimeAndUntilAndBadZone(t *testing.T) {
	got, err := Expand(Spec{StartsLocal: "2026-12-24T18:00", DurationMinutes: 60, TZID: "America/Boise"}, time.Now())
	if err != nil || len(got) != 1 || got[0].StartsLocal != "2026-12-24T18:00" {
		t.Fatalf("one-time: %+v %v", got, err)
	}
	got, err = Expand(Spec{
		StartsLocal: "2026-10-07T19:00", DurationMinutes: 60,
		TZID: "America/Boise", RRule: "FREQ=WEEKLY", Until: "2026-10-20",
	}, time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range got {
		if w.OriginalDate > "2026-10-20" {
			t.Fatalf("past until: %s", w.OriginalDate)
		}
	}
	if len(got) != 2 { // Oct 7 and Oct 14; Oct 21 is after until
		t.Fatalf("until count: %d", len(got))
	}
	if _, err := Expand(Spec{StartsLocal: "2026-10-07T19:00", DurationMinutes: 60, TZID: "Not/AZone"}, time.Now()); err == nil {
		t.Fatal("bad zone accepted")
	}
	if _, err := Expand(Spec{StartsLocal: "2026-10-07T19:00Z", DurationMinutes: 60, TZID: "UTC"}, time.Now()); err == nil {
		t.Fatal("offset accepted")
	}
	if _, err := Expand(Spec{StartsLocal: "2026-10-07T19:00", DurationMinutes: 60, TZID: "UTC", RRule: "NOPE"}, time.Now()); err == nil {
		t.Fatal("bad rule accepted")
	}
}

func TestHorizonCoversEighteenMonths(t *testing.T) {
	start := time.Date(2026, 10, 14, 19, 0, 0, 0, time.UTC)
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	end := WindowEnd(start, now)
	got, err := Expand(Spec{
		StartsLocal: "2026-10-14T19:00", DurationMinutes: 60,
		TZID: "UTC", RRule: "FREQ=WEEKLY",
	}, end)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 70 || len(got) > 80 {
		t.Fatalf("weekly over 18 months: %d occurrences", len(got))
	}
	last := got[len(got)-1].StartsAt
	if last.After(end) || last.Before(end.AddDate(0, 0, -7)) {
		t.Fatalf("last occurrence %s, horizon %s", last, end)
	}
}

func TestLoadZone(t *testing.T) {
	if loc, err := LoadZone(" America/Boise "); err != nil || loc.String() != "America/Boise" {
		t.Fatalf("Boise: %v %v", loc, err)
	}
	for _, bad := range []string{"", "Local", "Nowhere/Town"} {
		if _, err := LoadZone(bad); err == nil {
			t.Fatalf("%q: no error", bad)
		}
	}
}
