package app

import (
	"strconv"
	"strings"
	"time"

	"fold/internal/events"
	"fold/internal/settings"
)

// Every date and time a page shows is worded here, in one place
// (docs/HAZARDS.md section 1).

// viewerZone is where a page's times are shown: the person's own zone when
// it is set, else the church's (DEC-57, DEC-58). Signed-out visitors get the
// church's.
func viewerZone(f pageFacts) *time.Location {
	if f.session != nil {
		if loc, err := events.LoadZone(f.session.Person.Zone); err == nil {
			return loc
		}
	}
	return churchZone(f)
}

// churchZone is the church's time zone.
func churchZone(f pageFacts) *time.Location {
	if loc, err := events.LoadZone(f.setup[settings.ChurchZone]); err == nil {
		return loc
	}
	return time.UTC
}

// Dates are written the American way (R38).

// stamp words when something happened: "Mon, Jan 2, 3:04 pm".
func stamp(t time.Time, loc *time.Location) string { return t.In(loc).Format("Mon, Jan 2, 3:04 pm") }

// dayWords is "Tue, Dec 8", with the year when it is not this year's.
func dayWords(t time.Time, now time.Time) string {
	if t.Year() != now.In(t.Location()).Year() {
		return t.Format("Mon, Jan 2, 2006")
	}
	return t.Format("Mon, Jan 2")
}

// longDay is "Tuesday, December 8, 2026", for a date page's heading.
func longDay(t time.Time) string { return t.Format("Monday, January 2, 2006") }

func clockWords(t time.Time) string { return t.Format("3:04 pm") }

// clockSpan is "7:00 to 8:30 pm", or "11:00 am to 1:00 pm" across noon.
func clockSpan(start, end time.Time) string {
	if start.Format("pm") == end.Format("pm") {
		return start.Format("3:04") + " to " + clockWords(end)
	}
	return clockWords(start) + " to " + clockWords(end)
}

// timeSpan is a date's times on its own day: "7:00 to 8:30 pm", or "11:00
// pm to Wed, Dec 2, 1:00 am" when it ends on another. start and end are in
// the viewer's zone.
func timeSpan(start, end, now time.Time) string {
	if start.Format(events.DayLayout) == end.Format(events.DayLayout) {
		return clockSpan(start, end)
	}
	return clockWords(start) + " to " + dayWords(end, now) + ", " + clockWords(end)
}

// monthWords is "December 2026", for the calendar's month.
func monthWords(t time.Time) string { return t.Format("January 2006") }

// span words a date's time in loc: "Tue, Dec 8, 7:00 to 8:30 pm", or with
// both days when it ends on another.
func span(start, end time.Time, loc *time.Location, now time.Time) string {
	start, end = start.In(loc), end.In(loc)
	if start.YearDay() == end.YearDay() && start.Year() == end.Year() {
		return dayWords(start, now) + ", " + clockSpan(start, end)
	}
	return dayWords(start, now) + ", " + clockWords(start) + " to " + dayWords(end, now) + ", " + clockWords(end)
}

// pattern words an event's repeat on its own clock: "Every other Tuesday,
// 7:00 to 8:30 pm", "The 2nd Saturday of each month, 9:00 to 11:00 am".
func pattern(e events.Event, now time.Time) string {
	start, err := time.Parse(events.WallLayout, e.StartsLocal)
	if err != nil {
		return ""
	}
	end := start.Add(time.Duration(e.Minutes) * time.Minute)
	var when string
	if start.Format(events.DayLayout) == end.Format(events.DayLayout) {
		when = clockSpan(start, end)
	} else {
		when = clockWords(start) + ", for " + length(e.Minutes)
	}
	day := start.Weekday().String()
	var s string
	switch e.Repeat {
	case events.Weekly:
		s = "Every " + day + ", " + when
	case events.Biweekly:
		s = "Every other " + day + ", " + when
	case events.Monthly:
		s = "The " + events.Ordinal(e.MonthWeek) + " " + day + " of each month, " + when
	default:
		return dayWords(start, now) + ", " + when
	}
	if e.Until != "" {
		if u, err := time.Parse(events.DayLayout, e.Until); err == nil {
			s += ", until " + dayWords(u, now)
		}
	}
	return s
}

// length is "1 hour 30 minutes", "2 days".
func length(minutes int) string {
	plural := func(n int, one string) string {
		if n == 1 {
			return "1 " + one
		}
		return strconv.Itoa(n) + " " + one + "s"
	}
	var parts []string
	if d := minutes / (24 * 60); d > 0 {
		parts = append(parts, plural(d, "day"))
	}
	if h := minutes / 60 % 24; h > 0 {
		parts = append(parts, plural(h, "hour"))
	}
	if m := minutes % 60; m > 0 {
		parts = append(parts, plural(m, "minute"))
	}
	return strings.Join(parts, " ")
}

// zoneLine is the one quiet line a page adds when an event keeps a clock
// other than the viewer's (DEC-57), or "" when they agree.
func zoneLine(eventZone string, viewer *time.Location) string {
	if eventZone == viewer.String() {
		return ""
	}
	return "This event keeps " + events.ZoneCity(eventZone) + " time. Times here are shown in " + events.ZoneCity(viewer.String()) + " time."
}
