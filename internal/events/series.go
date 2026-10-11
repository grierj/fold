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
	// Keep: a per-date override or a cancellation. The series edit does not
	// drop the date when the new rule no longer includes it, and does not
	// resurrect a cancellation.
	Keep bool
}

// Action is one row change from a series edit.
type Action struct {
	Kind string // "move" | "cancel" | "add"
	ID   uint   // set for move and cancel
	When When   // set for move and add
}

// ApplySeries edits a series onto all future occurrences (5.3).
//
//   - Future means StartsAt is strictly after now. A date already underway
//     is history and stays.
//   - Future dates without a per-date override move onto the new times.
//     Registrations stay because the row id does not change.
//   - A future date the new rule no longer contains is cancelled, unless it
//     is a per-date override or already cancelled (Keep).
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
			if !e.Keep {
				out = append(out, Action{Kind: "cancel", ID: e.ID})
			}
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
