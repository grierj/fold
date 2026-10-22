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
// needs: its id, the seats its party takes (1 + guest_count), childcare, and the
// pool it waits in (its attendance mode; "" is the in-person pool for
// callers that only have one).
type WaitEntry struct {
	ID        uint
	Seats     int
	Mode      string
	Childcare int
}

// Pool is an attendance or childcare pool for promotion: a capacity
// (nil is unlimited) and the spots already used in it.
type Pool struct {
	Capacity *int
	Used     int
}

// PickPromotionsByPool returns the waitlist entries that fit in their
// own pool, in line order (M1.8 slice 2). There is one waitlist per
// occurrence; each entry keeps its mode, and promotion fills each pool
// from entries of that mode in line order, skipping a party too big
// for its pool's free seats without blocking the other pool. A mode
// with no pool given is unlimited. Childcare is shared across modes. A
// request that does not fit is skipped without consuming attendance seats
// or childcare spots (DEC-80). The caller checks the cutoff and supplies
// an unlimited childcare pool when childcare is unavailable or has no limit.
func PickPromotionsByPool(waitlist []WaitEntry, pools map[string]Pool, childcare Pool) []WaitEntry {
	// Free seats per capped pool; a mode missing here is unlimited.
	free := map[string]int{}
	for mode, p := range pools {
		if p.Capacity != nil {
			free[mode] = max(*p.Capacity-p.Used, 0)
		}
	}
	var out []WaitEntry
	for _, e := range waitlist {
		if e.Seats <= 0 {
			continue
		}
		f, capped := free[e.Mode]
		if capped && e.Seats > f {
			continue
		}
		if e.Childcare > 0 && !Fits(childcare.Capacity, childcare.Used, e.Childcare) {
			continue
		}
		out = append(out, e)
		childcare.Used += e.Childcare
		if capped {
			free[e.Mode] = f - e.Seats
		}
	}
	return out
}

// InCutoff reports whether auto-promotion is stopped: now is at or
// after startsAt minus the fixed cutoff window.
func InCutoff(now, startsAt time.Time) bool {
	return !now.Before(startsAt.Add(-CutoffWindow))
}
