package events

import "time"

// Capacity and waitlist rules (5.4, M1.5). These are pure functions:
// handlers load facts, call these, and write the results under the
// occurrence row lock (SELECT ... FOR UPDATE). Decision 1 (owner, Oct
// 15): one seat bucket here; M1.8 may add a second bucket for remote
// capacity without rewriting this file.

// CutoffWindow is the fixed no-auto-promotion window before a start
// (5.4): inside it a freed seat notifies the leaders or hosts instead
// of promoting, and they promote by hand.
const CutoffWindow = 2 * time.Hour

// EffectiveCapacity is the occurrence override when set, else the event
// capacity. Nil means unlimited.
func EffectiveCapacity(eventCapacity *int, override *int) *int {
	if override != nil {
		return override
	}
	return eventCapacity
}

// WaitEntry is one waitlisted registration, reduced to what promotion
// needs: its id and the seats its party takes (1 + guest_count).
type WaitEntry struct {
	ID    uint
	Seats int
}

// PickPromotions returns the waitlist entries that fit in the free
// seats, in line order. An entry too big for the free seats is skipped
// and the next one is tried. Each promotion only uses more seats, so
// one pass over the order is enough. When cutoff is true (inside the
// 2-hour window), nobody is picked.
func PickPromotions(capacity, used int, waitlist []WaitEntry, cutoff bool) []WaitEntry {
	if cutoff {
		return nil
	}
	free := capacity - used
	var out []WaitEntry
	for _, e := range waitlist {
		if e.Seats > 0 && e.Seats <= free {
			out = append(out, e)
			free -= e.Seats
		}
	}
	return out
}

// InCutoff reports whether auto-promotion is stopped: now is at or
// after startsAt minus the fixed cutoff window.
func InCutoff(now, startsAt time.Time) bool {
	return !now.Before(startsAt.Add(-CutoffWindow))
}
