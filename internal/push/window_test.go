package push

import (
	"testing"
	"time"
)

func mustZone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestWindowCheck(t *testing.T) {
	denver := mustZone(t, "America/Denver")
	ny := mustZone(t, "America/New_York")
	at := func(loc *time.Location, y int, m time.Month, d, h, min int) time.Time {
		return time.Date(y, m, d, h, min, 0, 0, loc)
	}
	for _, tc := range []struct {
		name     string
		t        time.Time
		tzid     string
		window   Window
		inside   bool
		opens    time.Time
		opensUTC string // the opening as an instant, to pin DST offsets
	}{
		{name: "exactly 8:00 is inside", t: at(denver, 2026, 10, 14, 8, 0), tzid: "America/Denver", inside: true},
		{name: "7:59 waits a minute", t: at(denver, 2026, 10, 14, 7, 59), tzid: "America/Denver", opens: at(denver, 2026, 10, 14, 8, 0)},
		{name: "19:59 is inside", t: at(denver, 2026, 10, 14, 19, 59), tzid: "America/Denver", inside: true},
		{name: "exactly 20:00 waits for tomorrow", t: at(denver, 2026, 10, 14, 20, 0), tzid: "America/Denver", opens: at(denver, 2026, 10, 15, 8, 0)},
		{name: "11 PM waits for 8 AM", t: at(denver, 2026, 10, 14, 23, 0), tzid: "America/Denver", opens: at(denver, 2026, 10, 15, 8, 0)},
		{name: "just before midnight waits for tomorrow", t: at(denver, 2026, 10, 14, 23, 59), tzid: "America/Denver", opens: at(denver, 2026, 10, 15, 8, 0)},
		{name: "just after midnight waits for today", t: at(denver, 2026, 10, 15, 0, 1), tzid: "America/Denver", opens: at(denver, 2026, 10, 15, 8, 0)},
		{name: "end of month rolls over", t: at(denver, 2026, 10, 31, 21, 0), tzid: "America/Denver", opens: at(denver, 2026, 11, 1, 8, 0)},
		// 11 PM in Denver is 2 PM the next day in Tokyo.
		{name: "a zone far from the church's", t: at(denver, 2026, 10, 14, 23, 0), tzid: "Asia/Tokyo", inside: true},
		// 9 AM in Denver is 1 AM in Tokyo: Tokyo's own 8 AM, not Denver's.
		{name: "the far zone's own morning", t: at(denver, 2026, 10, 14, 9, 0), tzid: "Asia/Tokyo",
			opens: time.Date(2026, 10, 15, 8, 0, 0, 0, mustZone(t, "Asia/Tokyo")), opensUTC: "2026-10-14T23:00:00Z"},
		{name: "an empty zone uses the church's", t: at(denver, 2026, 10, 14, 23, 0), tzid: "", opens: at(denver, 2026, 10, 15, 8, 0)},
		{name: "an unknown zone uses the church's", t: at(denver, 2026, 10, 14, 23, 0), tzid: "Mars/Olympus", opens: at(denver, 2026, 10, 15, 8, 0)},
		// Spring forward (Sun Mar 8, 2026, 2 AM EST becomes 3 AM EDT):
		// 1:30 AM EST opens at 8 AM EDT, 5.5 hours later, not 6.5.
		{name: "spring-forward night", t: at(ny, 2026, 3, 8, 1, 30), tzid: "America/New_York",
			opens: at(ny, 2026, 3, 8, 8, 0), opensUTC: "2026-03-08T12:00:00Z"},
		{name: "spring-forward evening", t: at(ny, 2026, 3, 7, 22, 0), tzid: "America/New_York",
			opens: at(ny, 2026, 3, 8, 8, 0), opensUTC: "2026-03-08T12:00:00Z"},
		// A window that starts inside the gap opens just after it.
		{name: "start hour in the gap", t: at(ny, 2026, 3, 8, 1, 0), tzid: "America/New_York", window: Window{StartHour: 2, EndHour: 20},
			opensUTC: "2026-03-08T07:00:00Z"},
		// Santiago springs forward at midnight (Sep 6, 2026, 00:00 -04
		// becomes 01:00 -03); time.Date puts the missing midnight an hour
		// early, on the evening before.
		{name: "a midnight gap", t: time.Date(2026, 9, 6, 1, 0, 0, 0, time.UTC), tzid: "America/Santiago", window: Window{StartHour: 0, EndHour: 20},
			opensUTC: "2026-09-06T04:00:00Z"},
		// Fall back (Sun Nov 1, 2026, 2 AM EDT becomes 1 AM EST): both
		// 1:30s wait for 8 AM EST.
		{name: "fall-back first 1:30", t: time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC), tzid: "America/New_York",
			opens: at(ny, 2026, 11, 1, 8, 0), opensUTC: "2026-11-01T13:00:00Z"},
		{name: "fall-back second 1:30", t: time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC), tzid: "America/New_York",
			opens: at(ny, 2026, 11, 1, 8, 0), opensUTC: "2026-11-01T13:00:00Z"},
		{name: "fall-back evening", t: at(ny, 2026, 10, 31, 23, 0), tzid: "America/New_York",
			opens: at(ny, 2026, 11, 1, 8, 0), opensUTC: "2026-11-01T13:00:00Z"},
		{name: "always open", t: at(denver, 2026, 10, 14, 3, 0), tzid: "America/Denver", window: AlwaysOpen, inside: true},
		{name: "a backwards window never holds a push", t: at(denver, 2026, 10, 14, 3, 0), tzid: "America/Denver", window: Window{StartHour: 20, EndHour: 8}, inside: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := tc.window
			if w == (Window{}) {
				w = DefaultWindow
			}
			inside, opens := w.Check(tc.t, tc.tzid, denver)
			if inside != tc.inside {
				t.Fatalf("inside = %v, want %v", inside, tc.inside)
			}
			if !tc.opens.IsZero() && !opens.Equal(tc.opens) {
				t.Errorf("opens %s, want %s", opens, tc.opens)
			}
			if tc.opensUTC != "" && opens.UTC().Format(time.RFC3339) != tc.opensUTC {
				t.Errorf("opens %s UTC, want %s", opens.UTC().Format(time.RFC3339), tc.opensUTC)
			}
			if inside != opens.IsZero() {
				t.Errorf("inside %v with opening %s", inside, opens)
			}
			if !inside && !opens.After(tc.t) {
				t.Errorf("opening %s is not after %s", opens, tc.t)
			}
		})
	}
}

func TestZoneFallsBack(t *testing.T) {
	denver := mustZone(t, "America/Denver")
	if got := Zone("Asia/Tokyo", denver).String(); got != "Asia/Tokyo" {
		t.Errorf("own zone: %s", got)
	}
	for _, tz := range []string{"", "  ", "Local", "Not/AZone"} {
		if got := Zone(tz, denver); got != denver {
			t.Errorf("%q: %s, want the church's", tz, got)
		}
	}
	if got := Zone("", nil); got != time.UTC {
		t.Errorf("no church zone: %s, want UTC", got)
	}
}

func TestWindowLastAllowed(t *testing.T) {
	denver := mustZone(t, "America/Denver")
	ny := mustZone(t, "America/New_York")
	at := func(loc *time.Location, y int, m time.Month, d, h, min int) time.Time {
		return time.Date(y, m, d, h, min, 0, 0, loc)
	}
	const within = 18 * time.Hour
	for _, tc := range []struct {
		name   string
		t      time.Time
		tzid   string
		window Window
		ok     bool
		want   time.Time
	}{
		{name: "inside stays put", t: at(denver, 2026, 10, 14, 10, 0), tzid: "America/Denver", ok: true, want: at(denver, 2026, 10, 14, 10, 0)},
		{name: "exactly 8:00 stays put", t: at(denver, 2026, 10, 14, 8, 0), tzid: "America/Denver", ok: true, want: at(denver, 2026, 10, 14, 8, 0)},
		// PLAN 7.7's example: a 9 AM event's 7 AM reminder goes at 7:59 PM
		// the night before.
		{name: "early morning moves to last night", t: at(denver, 2026, 10, 15, 7, 0), tzid: "America/Denver", ok: true, want: at(denver, 2026, 10, 14, 19, 59)},
		{name: "exactly 20:00 moves to 19:59", t: at(denver, 2026, 10, 14, 20, 0), tzid: "America/Denver", ok: true, want: at(denver, 2026, 10, 14, 19, 59)},
		{name: "late evening moves earlier that day", t: at(denver, 2026, 10, 14, 23, 30), tzid: "America/Denver", ok: true, want: at(denver, 2026, 10, 14, 19, 59)},
		{name: "just after midnight moves to last night", t: at(denver, 2026, 10, 15, 0, 1), tzid: "America/Denver", ok: true, want: at(denver, 2026, 10, 14, 19, 59)},
		{name: "first of the month moves to last month", t: at(denver, 2026, 11, 1, 6, 0), tzid: "America/Denver", ok: true, want: at(denver, 2026, 10, 31, 19, 59)},
		{name: "the recipient's zone, not the church's", t: at(denver, 2026, 10, 14, 13, 0), tzid: "Asia/Tokyo", ok: true,
			want: time.Date(2026, 10, 14, 19, 59, 0, 0, mustZone(t, "Asia/Tokyo"))},
		{name: "an empty zone uses the church's", t: at(denver, 2026, 10, 15, 7, 0), tzid: "", ok: true, want: at(denver, 2026, 10, 14, 19, 59)},
		// A narrow window leaves more than 18 hours to go back: skipped.
		{name: "more than 18 hours earlier is skipped", t: at(denver, 2026, 10, 15, 9, 0), tzid: "America/Denver", window: Window{StartHour: 10, EndHour: 14}},
		{name: "exactly 18 hours earlier still goes", t: at(denver, 2026, 10, 15, 7, 59), tzid: "America/Denver", window: Window{StartHour: 10, EndHour: 14}, ok: true,
			want: at(denver, 2026, 10, 14, 13, 59)},
		// Across spring forward the night is an hour shorter.
		{name: "spring-forward morning", t: at(ny, 2026, 3, 8, 7, 0), tzid: "America/New_York", ok: true, want: at(ny, 2026, 3, 7, 19, 59)},
		{name: "fall-back morning", t: at(ny, 2026, 11, 1, 7, 0), tzid: "America/New_York", ok: true, want: at(ny, 2026, 10, 31, 19, 59)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := tc.window
			if w == (Window{}) {
				w = DefaultWindow
			}
			got, ok := w.LastAllowed(tc.t, tc.tzid, denver, within)
			if ok != tc.ok {
				t.Fatalf("ok = %v (%s), want %v", ok, got, tc.ok)
			}
			if ok && !got.Equal(tc.want) {
				t.Errorf("got %s, want %s", got, tc.want)
			}
			if ok {
				if inside, _ := w.Check(got, tc.tzid, denver); !inside {
					t.Errorf("%s is outside the window", got)
				}
				if got.After(tc.t) || tc.t.Sub(got) > within {
					t.Errorf("%s is not within %s before %s", got, within, tc.t)
				}
			}
		})
	}
}
