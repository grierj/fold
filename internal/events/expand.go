// Package events expands a series in the church's (or the event's) time
// zone and applies the v1 series-edit rule. Occurrence rows and HTTP live
// in the handlers; this package is the clock.
package events

import (
	"errors"
	"fmt"
	"strings"
	"time"
	_ "time/tzdata" // distroless has no zoneinfo; the binary carries it

	"github.com/teambition/rrule-go"
)

// HorizonMonths is how far ahead occurrences are stored (5.3, 5.5).
const HorizonMonths = 18

// WallLayout is a local clock time with no offset: 2006-01-02T15:04.
const WallLayout = "2006-01-02T15:04"

// MaxDurationMinutes is seven days. Longer than a retreat, shorter than a
// series.
const MaxDurationMinutes = 7 * 24 * 60

// MaxOccurrences is the most dates a series may have in the two years before
// the horizon (a daily event has about 730), so it measures how dense a rule
// is, not how old the series is. MaxExpandSteps bounds the whole walk from
// the series start (a daily series begun in the 1750s). Together they stop a
// rule such as FREQ=DAILY;BYHOUR=0,...,23 or an absurd start date from
// exhausting memory or CPU.
const (
	MaxOccurrences = 2000
	MaxExpandSteps = 100000
)

// ErrTooManyDates means the rule makes more dates than MaxOccurrences. The
// wording is sent to the person as is and has copy in the SPA (DEC-61).
var ErrTooManyDates = errors.New("that repeat rule makes too many dates")

// ErrTooFrequent means the rule repeats more than once a day (HOURLY,
// MINUTELY or SECONDLY).
var ErrTooFrequent = errors.New("that repeat rule repeats more than once a day")

// Spec is the series rule. StartsLocal is a wall clock in TZID.
type Spec struct {
	StartsLocal     string
	DurationMinutes int
	TZID            string
	RRule           string
	Until           string // YYYY-MM-DD inclusive, or empty
}

// When is one generated occurrence.
type When struct {
	OriginalDate string
	StartsAt     time.Time // UTC
	EndsAt       time.Time // UTC
	StartsLocal  string
	EndsLocal    string
}

// ParseWall reads a wall-clock time in an IANA zone. The string must not
// carry an offset: the zone, not the offset, is what stays put across DST.
func ParseWall(value, tzid string) (time.Time, *time.Location, error) {
	loc, err := LoadZone(tzid)
	if err != nil {
		return time.Time{}, nil, err
	}
	t, err := time.ParseInLocation(WallLayout, strings.TrimSpace(value), loc)
	if err != nil {
		return time.Time{}, nil, fmt.Errorf("startsLocal must be YYYY-MM-DDTHH:MM")
	}
	return t, loc, nil
}

// LoadZone parses an IANA zone name. It refuses "" and "Local", which
// time.LoadLocation accepts as UTC and the server's own zone.
func LoadZone(tzid string) (*time.Location, error) {
	tzid = strings.TrimSpace(tzid)
	if tzid == "" || tzid == "Local" || len(tzid) > 64 {
		return nil, fmt.Errorf("unknown time zone %q", tzid)
	}
	loc, err := time.LoadLocation(tzid)
	if err != nil {
		return nil, fmt.Errorf("unknown time zone %q", tzid)
	}
	return loc, nil
}

// FormatWall renders t in loc with no offset.
func FormatWall(t time.Time, loc *time.Location) string {
	return t.In(loc).Format(WallLayout)
}

// WindowEnd is the last instant to generate through: 18 months after now,
// or 18 months after the series start when that is later (a series that
// begins next year still has its dates).
func WindowEnd(start, now time.Time) time.Time {
	end := now.AddDate(0, HorizonMonths, 0)
	fromStart := start.AddDate(0, HorizonMonths, 0)
	if fromStart.After(end) {
		return fromStart
	}
	return end
}

// Expand returns every occurrence from the series start through horizonEnd.
// A one-time event is its single date, even when that date is past the
// horizon. Times stay on the local clock: 19:00 in America/Boise is 19:00
// on both sides of a daylight-saving change.
func Expand(spec Spec, horizonEnd time.Time) ([]When, error) {
	start, loc, err := ParseWall(spec.StartsLocal, spec.TZID)
	if err != nil {
		return nil, err
	}
	if spec.DurationMinutes < 1 || spec.DurationMinutes > MaxDurationMinutes {
		return nil, fmt.Errorf("duration must be between 1 minute and 7 days")
	}
	until := strings.TrimSpace(spec.Until)
	if until != "" {
		if _, err := time.Parse("2006-01-02", until); err != nil {
			return nil, fmt.Errorf("rruleUntil must be YYYY-MM-DD")
		}
	}
	dur := time.Duration(spec.DurationMinutes) * time.Minute
	rule := strings.TrimSpace(spec.RRule)
	if rule == "" {
		w := whenOf(start, dur, loc)
		if until != "" && w.OriginalDate > until {
			return nil, nil
		}
		return []When{w}, nil
	}
	if strings.Contains(strings.ToUpper(rule), "DTSTART") || strings.Contains(rule, "\n") {
		return nil, fmt.Errorf("invalid recurrence rule")
	}
	dt := start.Format("20060102T150405")
	ics := fmt.Sprintf("DTSTART;TZID=%s:%s\nRRULE:%s", loc.String(), dt, rule)
	r, err := rrule.StrToRRule(ics)
	if err != nil {
		return nil, fmt.Errorf("invalid recurrence rule")
	}
	// Events repeat at most once a day (Grier, Oct 29, 2026). BYHOUR and the
	// like are still held by MaxOccurrences.
	if r.OrigOptions.Freq >= rrule.HOURLY {
		return nil, ErrTooFrequent
	}
	if horizonEnd.Before(start) {
		horizonEnd = start
	}
	// Walk the rule with the iterator instead of Between so a rule such as
	// FREQ=SECONDLY stops at the cap instead of building millions of times
	// in memory first. Occurrences arrive in order, so the first one past
	// the end date or the horizon ends the walk.
	next := r.Iterator()
	var out []When
	denseFrom, dense := horizonEnd.AddDate(-2, 0, 0), 0
	for steps := 0; ; steps++ {
		if steps >= MaxExpandSteps {
			return nil, ErrTooManyDates
		}
		t, ok := next()
		if !ok || t.After(horizonEnd) {
			break
		}
		w := whenOf(t.In(loc), dur, loc)
		if until != "" && w.OriginalDate > until {
			break
		}
		if !t.Before(denseFrom) {
			if dense++; dense > MaxOccurrences {
				return nil, ErrTooManyDates
			}
		}
		out = append(out, w)
	}
	if out == nil {
		out = []When{}
	}
	return out, nil
}

func whenOf(start time.Time, dur time.Duration, loc *time.Location) When {
	local := start.In(loc)
	end := local.Add(dur)
	return When{
		OriginalDate: local.Format("2006-01-02"),
		StartsAt:     local.UTC(),
		EndsAt:       end.UTC(),
		StartsLocal:  local.Format(WallLayout),
		EndsLocal:    end.Format(WallLayout),
	}
}
