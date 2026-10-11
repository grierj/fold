// Package events expands a series in the church's (or the event's) time
// zone and applies the v1 series-edit rule. Occurrence rows and HTTP live
// in the handlers; this package is the clock.
package events

import (
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
	tzid = strings.TrimSpace(tzid)
	loc, err := time.LoadLocation(tzid)
	if err != nil || tzid == "" {
		return time.Time{}, nil, fmt.Errorf("unknown time zone %q", tzid)
	}
	t, err := time.ParseInLocation(WallLayout, strings.TrimSpace(value), loc)
	if err != nil {
		return time.Time{}, nil, fmt.Errorf("startsLocal must be YYYY-MM-DDTHH:MM")
	}
	return t, loc, nil
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
	if horizonEnd.Before(start) {
		horizonEnd = start
	}
	times := r.Between(start.Add(-time.Second), horizonEnd, true)
	out := make([]When, 0, len(times))
	for _, t := range times {
		w := whenOf(t.In(loc), dur, loc)
		if until != "" && w.OriginalDate > until {
			continue
		}
		out = append(out, w)
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
