package events

import "time"

// Capacity and waitlist rules (5.4, M1.5). These are pure functions:
// handlers load facts, call these, and write the results under the
// occurrence row lock (SELECT ... FOR UPDATE). M1.8 slice 2 adds the
// second seat pool: remote registrations count against the remote
// capacity, never the in-person one (see Pool and PickPromotionsByPool).

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

// EffectiveRemoteCapacity is EffectiveCapacity for the remote pool:
// the per-date remote override when set, else the event's remote
// capacity. Nil means unlimited; 0 is a real cap (no remote seats).
func EffectiveRemoteCapacity(eventRemoteCapacity *int, override *int) *int {
	return EffectiveCapacity(eventRemoteCapacity, override)
}

// Fits reports whether a party of seats fits in a pool: an unlimited
// pool (nil capacity) always fits, otherwise used+seats must stay at or
// under the cap.
func Fits(capacity *int, used, seats int) bool {
	return capacity == nil || used+seats <= *capacity
}

// WaitEntry is one waitlisted registration, reduced to what promotion
// needs: its id, the seats its party takes (1 + guest_count), and the
// pool it waits in (its attendance mode; "" is the in-person pool for
// callers that only have one).
type WaitEntry struct {
	ID    uint
	Seats int
	Mode  string
}

// Pool is one seat pool for promotion: a capacity (nil is unlimited)
// and the seats already used in it.
type Pool struct {
	Capacity *int
	Used     int
}

// PickPromotionsByPool returns the waitlist entries that fit in their
// own pool, in line order (M1.8 slice 2). There is one waitlist per
// occurrence; each entry keeps its mode, and promotion fills each pool
// from entries of that mode in line order, skipping a party too big
// for its pool's free seats without blocking the other pool. A mode
// with no pool given is unlimited. When cutoff is true, nobody is
// picked.
func PickPromotionsByPool(waitlist []WaitEntry, pools map[string]Pool, cutoff bool) []WaitEntry {
	if cutoff {
		return nil
	}
	free := map[string]int{}
	unlimited := map[string]bool{}
	freeOf := func(mode string) (int, bool) {
		if _, ok := free[mode]; ok {
			return free[mode], unlimited[mode]
		}
		p, given := pools[mode]
		if !given || p.Capacity == nil {
			free[mode], unlimited[mode] = 0, true
			return 0, true
		}
		f := *p.Capacity - p.Used
		if f < 0 {
			f = 0
		}
		free[mode] = f
		return f, false
	}
	var out []WaitEntry
	for _, e := range waitlist {
		f, unlim := freeOf(e.Mode)
		if e.Seats <= 0 {
			continue
		}
		if unlim || e.Seats <= f {
			out = append(out, e)
			if !unlim {
				free[e.Mode] = f - e.Seats
			}
		}
	}
	return out
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
