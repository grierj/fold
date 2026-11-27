package events

import (
	"errors"
	"slices"
	"testing"
	"time"
)

func zone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := LoadZone(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func expand(t *testing.T, p Pattern, tz string, through time.Time) []Date {
	t.Helper()
	got, err := Expand(p, zone(t, tz), through)
	if err != nil {
		t.Fatalf("%+v: %v", p, err)
	}
	for _, d := range got {
		// Every stored instant reads back as the wall clock stored beside it.
		if d.StartsAt.In(zone(t, tz)).Format(WallLayout) != d.StartsLocal {
			t.Fatalf("%s: %s does not read back as %s", tz, d.StartsAt, d.StartsLocal)
		}
	}
	return got
}

func byDay(ds []Date) map[string]Date {
	m := map[string]Date{}
	for _, d := range ds {
		m[d.Day] = d
	}
	return m
}

var farOff = time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC)

// Check A: a weekly 7:00 pm event stays at 7:00 pm on both sides of each
// daylight-saving change, in the event's zone. Carried from Fold's
// expand_test.go, with Chicago added.
func TestWeeklyLocalTimeAcrossDST(t *testing.T) {
	type sample struct {
		day     string
		utcHour int
	}
	cases := []struct {
		tz      string
		samples []sample
	}{
		{"America/Boise", []sample{{"2026-10-28", 1}, {"2026-11-04", 2}, {"2027-03-10", 2}, {"2027-03-17", 1}}},
		{"America/Chicago", []sample{{"2026-10-28", 0}, {"2026-11-04", 1}, {"2027-03-10", 1}, {"2027-03-17", 0}}},
		{"America/New_York", []sample{{"2026-10-28", 23}, {"2026-11-04", 0}, {"2027-03-10", 0}, {"2027-03-17", 23}}},
		{"America/Phoenix", []sample{{"2026-10-28", 2}, {"2026-11-04", 2}, {"2027-03-10", 2}, {"2027-03-17", 2}}},
		{"Europe/London", []sample{{"2026-10-21", 18}, {"2026-10-28", 19}, {"2027-03-24", 19}, {"2027-03-31", 18}}},
	}
	for _, c := range cases {
		got := byDay(expand(t, Pattern{Start: c.samples[0].day + "T19:00", Minutes: 90, Repeat: Weekly}, c.tz, farOff))
		for _, s := range c.samples {
			d, ok := got[s.day]
			if !ok {
				t.Errorf("%s: no date on %s", c.tz, s.day)
				continue
			}
			if d.StartsLocal != s.day+"T19:00" || d.Moved || d.Doubled {
				t.Errorf("%s %s: starts %s moved %v doubled %v", c.tz, s.day, d.StartsLocal, d.Moved, d.Doubled)
			}
			if d.StartsAt.UTC().Hour() != s.utcHour {
				t.Errorf("%s %s: UTC hour %d, want %d", c.tz, s.day, d.StartsAt.UTC().Hour(), s.utcHour)
			}
			if d.EndsAt.Sub(d.StartsAt) != 90*time.Minute {
				t.Errorf("%s %s: lasts %s", c.tz, s.day, d.EndsAt.Sub(d.StartsAt))
			}
		}
	}
}

// Check A and R34: a time a daylight-saving change skips moves forward by
// the gap and says so; a time that happens twice is the first. Real
// transition days: the US on 2026-11-01 and 2027-03-14, London on
// 2026-10-25 and 2027-03-28, Lord Howe's half-hour change on 2026-10-04 and
// 2027-04-04, and Havana, whose clocks jump at midnight.
func TestDaylightSavingGapsAndOverlaps(t *testing.T) {
	cases := []struct {
		tz, wall       string
		local          string // the wall clock it runs at
		utc            string
		moved, doubled bool
	}{
		{"America/Boise", "2027-03-14T02:30", "2027-03-14T03:30", "2027-03-14T09:30", true, false},
		{"America/Boise", "2026-11-01T01:30", "2026-11-01T01:30", "2026-11-01T07:30", false, true},
		{"America/Boise", "2026-11-01T02:30", "2026-11-01T02:30", "2026-11-01T09:30", false, false},
		{"America/Chicago", "2027-03-14T02:30", "2027-03-14T03:30", "2027-03-14T08:30", true, false},
		{"America/Chicago", "2026-11-01T01:30", "2026-11-01T01:30", "2026-11-01T06:30", false, true},
		{"Europe/London", "2027-03-28T01:30", "2027-03-28T02:30", "2027-03-28T01:30", true, false},
		{"Europe/London", "2026-10-25T01:30", "2026-10-25T01:30", "2026-10-25T00:30", false, true},
		{"Australia/Lord_Howe", "2026-10-04T02:15", "2026-10-04T02:45", "2026-10-03T15:45", true, false},
		{"Australia/Lord_Howe", "2027-04-04T01:45", "2027-04-04T01:45", "2027-04-03T14:45", false, true},
		{"America/Havana", "2027-03-14T00:30", "2027-03-14T01:30", "2027-03-14T05:30", true, false},
	}
	for _, c := range cases {
		got := expand(t, Pattern{Start: c.wall, Minutes: 60, Repeat: Once}, c.tz, farOff)
		if len(got) != 1 {
			t.Fatalf("%s %s: %d dates", c.tz, c.wall, len(got))
		}
		d := got[0]
		if d.Day != c.wall[:10] || d.StartsLocal != c.local || d.StartsAt.UTC().Format(WallLayout) != c.utc || d.Moved != c.moved || d.Doubled != c.doubled {
			t.Errorf("%s %s: day %s local %s utc %s moved %v doubled %v; want %s %s %v %v",
				c.tz, c.wall, d.Day, d.StartsLocal, d.StartsAt.UTC().Format(WallLayout), d.Moved, d.Doubled, c.local, c.utc, c.moved, c.doubled)
		}
		if d.EndsAt.Sub(d.StartsAt) != time.Hour {
			t.Errorf("%s %s: lasts %s", c.tz, c.wall, d.EndsAt.Sub(d.StartsAt))
		}
	}
	// In a weekly series only the night of the change moves.
	got := byDay(expand(t, Pattern{Start: "2027-03-07T02:30", Minutes: 60, Repeat: Weekly}, "America/Boise", farOff))
	if got["2027-03-07"].StartsLocal != "2027-03-07T02:30" || !got["2027-03-14"].Moved || got["2027-03-21"].StartsLocal != "2027-03-21T02:30" || got["2027-03-21"].Moved {
		t.Errorf("weekly 2:30 am around the spring change: %+v %+v %+v", got["2027-03-07"], got["2027-03-14"], got["2027-03-21"])
	}
}

func days(ds []Date, n int) []string {
	var out []string
	for i := 0; i < n && i < len(ds); i++ {
		out = append(out, ds[i].Day)
	}
	return out
}

// R33 and R37: monthly on the Nth weekday, or the last.
func TestMonthly(t *testing.T) {
	second := expand(t, Pattern{Start: "2026-12-12T09:00", Minutes: 120, Repeat: Monthly, MonthWeek: 2}, "America/Boise", farOff)
	if want := []string{"2026-12-12", "2027-01-09", "2027-02-13", "2027-03-13"}; !slices.Equal(days(second, 4), want) {
		t.Errorf("2nd Saturday: %v, want %v", days(second, 4), want)
	}
	last := expand(t, Pattern{Start: "2026-12-29T19:00", Minutes: 60, Repeat: Monthly, MonthWeek: Last}, "America/Boise", farOff)
	if want := []string{"2026-12-29", "2027-01-26", "2027-02-23", "2027-03-30"}; !slices.Equal(days(last, 4), want) {
		t.Errorf("last Tuesday: %v, want %v", days(last, 4), want)
	}
	// Every month through 2027, across both changes, all at 9:00.
	if len(second) != 13 {
		t.Errorf("%d monthly dates", len(second))
	}
	for _, d := range second {
		if d.StartsLocal[11:] != "09:00" {
			t.Errorf("%s at %s", d.Day, d.StartsLocal)
		}
	}
}

// A 5th weekday is only "last"; a 4th that ends its month is either.
func TestWeeksOf(t *testing.T) {
	cases := map[string][]int{
		"2026-12-01": {1}, "2026-12-12": {2}, "2026-12-15": {3}, "2026-12-22": {4},
		"2026-12-29": {Last},    // the 5th Tuesday
		"2027-02-23": {4, Last}, // February 2027 has four Tuesdays
		"2026-12-31": {Last},
	}
	for day, want := range cases {
		d, _ := time.Parse(DayLayout, day)
		if got := WeeksOf(d); !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v", day, got, want)
		}
	}
	for _, week := range []int{1, 2, 3, 4} {
		err := Pattern{Start: "2026-12-29T19:00", Minutes: 60, Repeat: Monthly, MonthWeek: week}.Check()
		if !errors.Is(err, ErrWeekMismatch) {
			t.Errorf("5th Tuesday as the %s: %v", Ordinal(week), err)
		}
	}
	for _, week := range []int{4, Last} {
		if err := (Pattern{Start: "2027-02-23T19:00", Minutes: 60, Repeat: Monthly, MonthWeek: week}).Check(); err != nil {
			t.Errorf("Feb 23 as the %s: %v", Ordinal(week), err)
		}
	}
}

func TestUntilAndBiweekly(t *testing.T) {
	got := expand(t, Pattern{Start: "2026-12-01T19:00", Minutes: 60, Repeat: Weekly, Until: "2026-12-15"}, "America/Boise", farOff)
	if want := []string{"2026-12-01", "2026-12-08", "2026-12-15"}; !slices.Equal(days(got, 10), want) {
		t.Errorf("until: %v", days(got, 10))
	}
	// Every other week across the spring change keeps 7:00 pm.
	bi := expand(t, Pattern{Start: "2027-03-02T19:00", Minutes: 60, Repeat: Biweekly}, "America/Boise", farOff)
	if want := []string{"2027-03-02", "2027-03-16", "2027-03-30"}; !slices.Equal(days(bi, 3), want) {
		t.Errorf("biweekly: %v", days(bi, 3))
	}
	if bi[0].StartsAt.UTC().Hour() != 2 || bi[1].StartsAt.UTC().Hour() != 1 || bi[1].StartsLocal != "2027-03-16T19:00" {
		t.Errorf("biweekly across the change: %+v %+v", bi[0], bi[1])
	}
}

func TestHorizonAndOnce(t *testing.T) {
	now := time.Date(2026, 11, 25, 17, 0, 0, 0, time.UTC)
	p := Pattern{Start: "2026-12-01T19:00", Minutes: 60, Repeat: Weekly}
	first, _, _ := Resolve(2026, 12, 1, 19, 0, zone(t, "America/Boise"))
	through := Through(first, now)
	got := expand(t, p, "America/Boise", through)
	if n := len(got); n < 75 || n > 80 {
		t.Errorf("weekly over 18 months: %d dates", n)
	}
	if last := got[len(got)-1].StartsAt; last.After(through) || last.Before(through.AddDate(0, 0, -7)) {
		t.Errorf("last date %s, through %s", last, through)
	}
	// A one-time date is stored wherever it falls.
	once := expand(t, Pattern{Start: "2028-06-01T19:00", Minutes: 60, Repeat: Once}, "America/Boise", now)
	if len(once) != 1 {
		t.Errorf("once: %d dates", len(once))
	}
	// The cap holds even for an absurd horizon.
	if _, err := Expand(p, zone(t, "America/Boise"), now.AddDate(60, 0, 0)); !errors.Is(err, ErrTooManyDates) {
		t.Errorf("a 60-year weekly series: %v", err)
	}
}

func TestCheckRefusesWhatR33DoesNotAllow(t *testing.T) {
	ok := Pattern{Start: "2026-12-01T19:00", Minutes: 60, Repeat: Weekly}
	cases := []struct {
		name string
		edit func(*Pattern)
		want error
	}{
		{"no leading zero", func(p *Pattern) { p.Start = "2026-12-01T7:00" }, ErrBadStart},
		{"an offset", func(p *Pattern) { p.Start = "2026-12-01T19:00Z" }, ErrBadStart},
		{"no such day", func(p *Pattern) { p.Start = "2027-02-30T19:00" }, ErrBadStart},
		{"no length", func(p *Pattern) { p.Minutes = 0 }, ErrBadMinutes},
		{"longer than a week", func(p *Pattern) { p.Minutes = MaxMinutes + 1 }, ErrBadMinutes},
		{"daily", func(p *Pattern) { p.Repeat = "daily" }, ErrBadRepeat},
		{"a rule string", func(p *Pattern) { p.Repeat = "FREQ=HOURLY" }, ErrBadRepeat},
		{"a week on a weekly repeat", func(p *Pattern) { p.MonthWeek = 2 }, ErrBadRepeat},
		{"a 5th week", func(p *Pattern) { p.Repeat, p.MonthWeek = Monthly, 5 }, ErrBadRepeat},
		{"monthly with no week", func(p *Pattern) { p.Repeat = Monthly }, ErrBadRepeat},
		{"until before the start", func(p *Pattern) { p.Until = "2026-11-30" }, ErrBadUntil},
		{"until not a day", func(p *Pattern) { p.Until = "soon" }, ErrBadUntil},
	}
	if err := ok.Check(); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		p := ok
		c.edit(&p)
		if err := p.Check(); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
}

func TestCheckStart(t *testing.T) {
	now := time.Date(2026, 11, 25, 17, 0, 0, 0, time.UTC)
	loc := zone(t, "America/Boise")
	for start, want := range map[string]error{
		"2025-11-25T19:00": nil, "2025-11-24T19:00": ErrStartRange,
		"2028-11-25T19:00": nil, "2028-11-26T19:00": ErrStartRange,
	} {
		if err := CheckStart(Pattern{Start: start}, loc, now); !errors.Is(err, want) {
			t.Errorf("%s: %v, want %v", start, err, want)
		}
	}
}

func TestLoadZone(t *testing.T) {
	for _, bad := range []string{"", "Local", "Nowhere/Town", "America/Boise\n"} {
		if _, err := LoadZone(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if ZoneCity("America/Argentina/Buenos_Aires") != "Buenos Aires" || ZoneCity("UTC") != "UTC" {
		t.Error("zone names")
	}
}
