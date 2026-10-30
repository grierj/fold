package events

import "time"

// Existing is one stored occurrence, reduced to what a series edit needs.
type Existing struct {
	ID           uint
	OriginalDate string
	StartsAt     time.Time
	EndsAt       time.Time
	// TimeFrozen: a per-date clock override. The series edit does not move it.
	TimeFrozen bool
	// Keep: a per-date override or a person's cancellation (models
	// Customized). The series edit does not drop the date when the new rule
	// no longer includes it, does not move its clock, and does not resurrect a
	// cancellation. A date the series itself cancelled is not Keep on that
	// account alone.
	Keep bool
	// SeriesCancelled: the date is cancelled and an earlier series edit did
	// it (cancel_source = series). It is restored when the new rule includes
	// it again; a person's cancellation never is.
	SeriesCancelled bool
}

// Action is one row change from a series edit.
type Action struct {
	Kind string // "move" | "cancel" | "restore" | "add"
	ID   uint   // set for move, cancel and restore
	When When   // set for move, restore and add
	// Retime is set on restore: move the date onto When as a plain date
	// would be moved. It is false when the row has another per-date override
	// (the existing Keep semantics: such a row keeps its own clock).
	Retime bool
}

// ApplySeries edits a series onto all future occurrences (5.3).
//
//   - Future means StartsAt is strictly after now. A date already underway
//     is history and stays.
//   - Future dates without a per-date override move onto the new times.
//     Registrations stay because the row id does not change.
//   - A future date the new rule no longer contains is cancelled, unless it
//     is a per-date override or already cancelled (Keep, SeriesCancelled).
//   - A future date an earlier series edit cancelled, and that the new rule
//     contains again, is restored to scheduled. Its times follow the new rule
//     like a plain date's. If the row also carries another per-date override
//     (title, place, clock, visibility) it is restored with those values
//     intact and its clock is not moved, because an override already keeps a
//     scheduled date out of the series move. A person's cancellation is
//     never restored. Past dates are never touched.
//   - Per-date overrides are left alone, including their clock time.
//   - Dates the new rule adds, and that are still in the future, are inserted.
func ApplySeries(existing []Existing, next []When, now time.Time) []Action {
	byDate := make(map[string]When, len(next))
	for _, w := range next {
		byDate[w.OriginalDate] = w
	}
	seen := make(map[string]bool, len(existing))
	var out []Action
	for _, e := range existing {
		seen[e.OriginalDate] = true
		if !e.StartsAt.After(now) {
			continue
		}
		w, still := byDate[e.OriginalDate]
		if !still {
			if !e.Keep && !e.SeriesCancelled {
				out = append(out, Action{Kind: "cancel", ID: e.ID})
			}
			continue
		}
		if e.SeriesCancelled {
			out = append(out, Action{Kind: "restore", ID: e.ID, When: w, Retime: !e.TimeFrozen && !e.Keep})
			continue
		}
		if e.TimeFrozen || e.Keep {
			continue
		}
		if e.StartsAt.Equal(w.StartsAt) && e.EndsAt.Equal(w.EndsAt) {
			continue
		}
		out = append(out, Action{Kind: "move", ID: e.ID, When: w})
	}
	for _, w := range next {
		if seen[w.OriginalDate] || !w.StartsAt.After(now) {
			continue
		}
		out = append(out, Action{Kind: "add", When: w})
	}
	return out
}
