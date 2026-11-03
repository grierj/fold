package models

import (
	"fmt"
	"time"
)

// CalendarDateLayout is the one text form of a bare calendar date: the
// wire form of `date` fields, the URL date segment and the `date` column
// when it is bound as text.
const CalendarDateLayout = "2006-01-02"

// ParseCalendarDate reads a strict YYYY-MM-DD (leap days and month lengths
// checked) for a `date` column such as events.rrule_until or
// event_occurrences.original_date. The result is midnight UTC; only its year,
// month and day mean anything. Postgres has no year 0, so one is refused here
// rather than failing as a 500 inside the database.
func ParseCalendarDate(s string) (time.Time, error) {
	t, err := time.Parse(CalendarDateLayout, s)
	if err != nil {
		return time.Time{}, err
	}
	if t.Year() < 1 {
		return time.Time{}, fmt.Errorf("parsing time %q: year out of range", s)
	}
	return t, nil
}

// FormatCalendarDate renders the year, month and day of t as YYYY-MM-DD. It
// reads t's own calendar fields and never converts the zone: a `date` column
// scans as midnight in whatever zone the driver chose, and converting that
// instant to UTC or the server's zone would move the date.
func FormatCalendarDate(t time.Time) string {
	return t.Format(CalendarDateLayout)
}
