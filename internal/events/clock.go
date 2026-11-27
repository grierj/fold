// Package events is the event clock and the event and date rows. The clock
// (this file and series.go) is pure: it turns a repeat into dates in the
// event's own zone, so a weekly 7:00 pm meeting stays at 7:00 pm on both
// sides of a daylight-saving change (docs/HAZARDS.md section 1).
package events

import (
	"errors"
	"strings"
	"time"
	_ "time/tzdata" // every IANA zone, so tests and the binary agree on the rules
)

// Repeats (R33). A repeat is stored as these parts, never as a rule string.
const (
	Once     = "once"
	Weekly   = "weekly"
	Biweekly = "biweekly"
	Monthly  = "monthly"
)

// Last is month_week for the last weekday of the month (R37).
const Last = -1

const (
	WallLayout = "2006-01-02T15:04"
	DayLayout  = "2006-01-02"
	// HorizonMonths is how far ahead dates are stored; a nightly job keeps
	// them that far ahead.
	HorizonMonths = 18
	MaxMinutes    = 7 * 24 * 60
	// MaxDates refuses a series that would make more dates than this
	// (DEC-89). R33's repeats stay far below it; it is the last guard.
	MaxDates = 2000
)

var (
	ErrBadStart     = errors.New("events: the start must be YYYY-MM-DDTHH:MM")
	ErrBadMinutes   = errors.New("events: the length must be 1 minute to 7 days")
	ErrBadRepeat    = errors.New("events: not a repeat R33 allows")
	ErrBadUntil     = errors.New("events: the last day must be YYYY-MM-DD, on or after the first")
	ErrWeekMismatch = errors.New("events: the first date is not on that week of its month")
	ErrTooManyDates = errors.New("events: that repeat makes too many dates")
	ErrBadZone      = errors.New("events: unknown time zone")
)

// LoadZone reads an IANA zone name. It refuses "" and "Local", which
// time.LoadLocation reads as UTC and as the server's own zone.
func LoadZone(name string) (*time.Location, error) {
	if name == "" || name == "Local" || len(name) > 64 {
		return nil, ErrBadZone
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, ErrBadZone
	}
	return loc, nil
}

// Pattern is when an event happens, in its zone's wall clock.
type Pattern struct {
	Start     string // the first date's wall clock, YYYY-MM-DDTHH:MM
	Minutes   int
	Repeat    string
	MonthWeek int    // monthly only: 1 to 4, or Last
	Until     string // the last day it may fall on, or ""
}

// civil is a calendar day, held at midnight UTC so adding days never meets a
// clock change.
func civil(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

// StartDay is the first date's calendar day.
func (p Pattern) StartDay() (time.Time, error) {
	t, err := time.Parse(WallLayout, p.Start)
	if err != nil || t.Format(WallLayout) != p.Start {
		return time.Time{}, ErrBadStart
	}
	return civil(t.Date()), nil
}

// WeeksOf is the month_week values a day is on: its week of the month when
// that is the 1st to the 4th, and Last when it is the month's last such
// weekday. A 5th weekday is only Last, since not every month has one.
func WeeksOf(day time.Time) []int {
	var out []int
	if n := (day.Day()-1)/7 + 1; n <= 4 {
		out = append(out, n)
	}
	if day.AddDate(0, 0, 7).Month() != day.Month() {
		out = append(out, Last)
	}
	return out
}

// Check validates a pattern's shape. It reads no clock; the bounds on how
// far back or ahead a start may be are CheckStart's.
func (p Pattern) Check() error {
	day, err := p.StartDay()
	if err != nil {
		return err
	}
	if p.Minutes < 1 || p.Minutes > MaxMinutes {
		return ErrBadMinutes
	}
	switch p.Repeat {
	case Once, Weekly, Biweekly:
		if p.MonthWeek != 0 {
			return ErrBadRepeat
		}
	case Monthly:
		ok := false
		for _, w := range WeeksOf(day) {
			ok = ok || w == p.MonthWeek
		}
		if !ok {
			if p.MonthWeek < Last || p.MonthWeek == 0 || p.MonthWeek > 4 {
				return ErrBadRepeat
			}
			return ErrWeekMismatch
		}
	default:
		return ErrBadRepeat
	}
	if p.Until != "" {
		u, err := time.Parse(DayLayout, p.Until)
		if err != nil || u.Format(DayLayout) != p.Until || u.Before(day) {
			return ErrBadUntil
		}
	}
	return nil
}

// ErrStartRange is a first date more than a year ago or more than two
// years ahead.
var ErrStartRange = errors.New("events: the first date is too far back or too far ahead")

// CheckStart bounds a new or changed first date, before anything expands it
// (docs/HAZARDS.md section 4).
func CheckStart(p Pattern, loc *time.Location, now time.Time) error {
	day, err := p.StartDay()
	if err != nil {
		return err
	}
	today := civil(now.In(loc).Date())
	if day.Before(today.AddDate(-1, 0, 0)) || day.After(today.AddDate(2, 0, 0)) {
		return ErrStartRange
	}
	return nil
}

// Date is one date of an event.
type Date struct {
	Day         string // the day the repeat puts it on
	StartsAt    time.Time
	EndsAt      time.Time
	StartsLocal string // the wall clock it runs at
	Moved       bool   // R34: its time did not exist that day, so it moved later
	Doubled     bool   // R34: its time happened twice that day; it is the first
}

// Resolve finds the instant a wall clock names in loc, by R34 and never by
// time.Date's unspecified choice: a time a daylight-saving change skips moves
// forward by the length of the gap (2:30 becomes 3:30), and a time that
// happens twice is the first of the two. It assumes at most one change of
// offset within a day of the wall time, which holds for every zone today.
func Resolve(y int, m time.Month, d, hour, minute int, loc *time.Location) (t time.Time, moved, doubled bool) {
	wall := time.Date(y, m, d, hour, minute, 0, 0, time.UTC)
	_, before := wall.Add(-24 * time.Hour).In(loc).Zone()
	_, after := wall.Add(24 * time.Hour).In(loc).Zone()
	var hits []time.Time
	for i, off := range []int{before, after} {
		if i == 1 && off == before {
			break
		}
		c := wall.Add(-time.Duration(off) * time.Second)
		if _, o := c.In(loc).Zone(); o == off {
			hits = append(hits, c)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], false, false
	case 2:
		if hits[1].Before(hits[0]) {
			return hits[1], false, true
		}
		return hits[0], false, true
	}
	// In a gap the wall clock read with the offset before the change is the
	// same wall clock moved forward by the gap.
	return wall.Add(-time.Duration(before) * time.Second), true, false
}

// Through is the last instant to store dates for: 18 months from now, or
// from the first date when that is later.
func Through(first, now time.Time) time.Time {
	if first.After(now) {
		now = first
	}
	return now.AddDate(0, HorizonMonths, 0)
}

// Expand lists an event's dates from its first through the instant given.
// A one-time event is its one date, wherever it falls.
func Expand(p Pattern, loc *time.Location, through time.Time) ([]Date, error) {
	if err := p.Check(); err != nil {
		return nil, err
	}
	start, _ := time.Parse(WallLayout, p.Start)
	first := civil(start.Date())
	var out []Date
	for i := 0; ; i++ {
		day, ok := p.nth(first, i)
		if !ok {
			break
		}
		ds := day.Format(DayLayout)
		if p.Until != "" && ds > p.Until {
			break
		}
		at, moved, doubled := Resolve(day.Year(), day.Month(), day.Day(), start.Hour(), start.Minute(), loc)
		if i > 0 && at.After(through) {
			break
		}
		if len(out) == MaxDates {
			return nil, ErrTooManyDates
		}
		out = append(out, Date{Day: ds, StartsAt: at, EndsAt: at.Add(time.Duration(p.Minutes) * time.Minute),
			StartsLocal: at.In(loc).Format(WallLayout), Moved: moved, Doubled: doubled})
	}
	return out, nil
}

// nth is the i-th day the repeat falls on, counting the first as 0.
func (p Pattern) nth(first time.Time, i int) (time.Time, bool) {
	switch p.Repeat {
	case Weekly:
		return first.AddDate(0, 0, 7*i), true
	case Biweekly:
		return first.AddDate(0, 0, 14*i), true
	case Monthly:
		month := civil(first.Year(), first.Month()+time.Month(i), 1)
		return weekdayOf(month, first.Weekday(), p.MonthWeek), true
	}
	return first, i == 0
}

// weekdayOf is the week-th given weekday of the month starting at month, or
// its last when week is Last.
func weekdayOf(month time.Time, wd time.Weekday, week int) time.Time {
	if week == Last {
		end := month.AddDate(0, 1, -1)
		return end.AddDate(0, 0, -((int(end.Weekday()) - int(wd) + 7) % 7))
	}
	firstWD := month.AddDate(0, 0, (int(wd)-int(month.Weekday())+7)%7)
	return firstWD.AddDate(0, 0, 7*(week-1))
}

// Ordinal words a month_week, for pages: "2nd", "last".
func Ordinal(week int) string {
	switch week {
	case 1:
		return "1st"
	case 2:
		return "2nd"
	case 3:
		return "3rd"
	case 4:
		return "4th"
	case Last:
		return "last"
	}
	return ""
}

// ZoneCity names a zone for people: "America/Boise" is "Boise".
func ZoneCity(name string) string {
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	return strings.ReplaceAll(name, "_", " ")
}
