package models

import (
	"testing"
	"time"
)

func TestParseCalendarDate(t *testing.T) {
	for _, ok := range []string{"2026-11-03", "2028-02-29", "2000-02-29", "0001-01-01", "9999-12-31"} {
		d, err := ParseCalendarDate(ok)
		if err != nil {
			t.Errorf("%s: %v", ok, err)
			continue
		}
		if got := FormatCalendarDate(d); got != ok {
			t.Errorf("%s round-trips as %s", ok, got)
		}
	}
	for _, bad := range []string{
		"", " 2026-11-03", "2026-11-03 ", "2026-11-03T10:00", "2026-1-3", "26-11-03", "2026/11/03",
		"2027-02-29", "1900-02-29", "2026-02-30", "2026-04-31", "2026-13-01", "2026-00-10", "2026-11-00",
		"0000-01-01", "2026-11-03-b", "not-a-date",
	} {
		if d, err := ParseCalendarDate(bad); err == nil {
			t.Errorf("%q parsed as %v", bad, d)
		}
	}
}

// A `date` scans as midnight in whatever zone the driver picked, and a value
// built by a caller can be in any zone. The text must be the calendar date
// the value shows, never the date of the same instant in UTC.
func TestFormatCalendarDateReadsCalendarFields(t *testing.T) {
	kiritimati := time.FixedZone("Pacific/Kiritimati", 14*60*60)
	losAngeles := time.FixedZone("America/Los_Angeles", -8*60*60)
	for _, tc := range []struct {
		name string
		at   time.Time
		want string
	}{
		{"midnight UTC", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), "2026-03-01"},
		// 00:30 on March 1 at UTC+14 is 10:30 on February 28 in UTC.
		{"east of UTC, UTC date is the day before", time.Date(2026, 3, 1, 0, 30, 0, 0, kiritimati), "2026-03-01"},
		// 23:30 on March 1 at UTC-8 is 07:30 on March 2 in UTC.
		{"west of UTC, UTC date is the day after", time.Date(2026, 3, 1, 23, 30, 0, 0, losAngeles), "2026-03-01"},
		{"leap day", time.Date(2028, 2, 29, 12, 0, 0, 0, kiritimati), "2028-02-29"},
		{"new year's eve", time.Date(2026, 12, 31, 23, 59, 0, 0, losAngeles), "2026-12-31"},
	} {
		if got := FormatCalendarDate(tc.at); got != tc.want {
			t.Errorf("%s: %s, want %s (UTC says %s)", tc.name, got, tc.want, tc.at.UTC().Format(CalendarDateLayout))
		}
	}
	// Parsing then formatting never moves the date, whichever zone the
	// process runs in.
	prev := time.Local
	defer func() { time.Local = prev }()
	for _, zone := range []*time.Location{kiritimati, losAngeles, time.UTC} {
		time.Local = zone
		d, err := ParseCalendarDate("2026-03-01")
		if err != nil || FormatCalendarDate(d) != "2026-03-01" {
			t.Errorf("zone %s: %v %v", zone, d, err)
		}
	}
}
