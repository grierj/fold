package events

import "time"

// Stored is a date row as the series edit needs it.
type Stored struct {
	ID          int64
	Day         string
	StartsAt    time.Time
	EndsAt      time.Time
	StartsLocal string
	Moved       bool
	Cancelled   bool
	// BySeries: an earlier series edit cancelled it, so a later one may
	// bring it back (DEC-91). A date a person cancelled stays cancelled.
	BySeries bool
}

// Change puts a stored date onto new times.
type Change struct {
	ID   int64
	Date Date
}

// Plan is what a series edit does to the stored dates.
type Plan struct {
	Move    []Change // scheduled dates onto their new times
	Cancel  []int64  // scheduled dates the new repeat no longer makes
	Restore []Change // dates the series cancelled that it makes again
	Add     []Date   // days no row has yet
}

// PlanSeries edits a series onto its future dates (DEC-91, DEC-161). A date
// that has started is history and is never touched. Dates are matched by the
// day the repeat puts them on, so a date keeps its row, and in time its
// answers, while its clock moves. A day gets a new row only when no row has
// it at all, past or cancelled.
func PlanSeries(stored []Stored, next []Date, now time.Time) Plan {
	byDay := make(map[string]Date, len(next))
	for _, d := range next {
		byDay[d.Day] = d
	}
	have := make(map[string]bool, len(stored))
	var p Plan
	for _, s := range stored {
		have[s.Day] = true
		if !s.StartsAt.After(now) {
			continue
		}
		d, still := byDay[s.Day]
		switch {
		case !still:
			if !s.Cancelled {
				p.Cancel = append(p.Cancel, s.ID)
			}
		case s.Cancelled:
			if s.BySeries {
				p.Restore = append(p.Restore, Change{s.ID, d})
			}
		case !s.StartsAt.Equal(d.StartsAt) || !s.EndsAt.Equal(d.EndsAt) || s.StartsLocal != d.StartsLocal || s.Moved != d.Moved:
			p.Move = append(p.Move, Change{s.ID, d})
		}
	}
	for _, d := range next {
		if !have[d.Day] && d.StartsAt.After(now) {
			p.Add = append(p.Add, d)
		}
	}
	return p
}
