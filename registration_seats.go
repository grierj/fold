package main

import (
	"time"

	"fold/internal/events"
	"fold/models"

	"gorm.io/gorm"
)

// The seat ledger. Every registration writer, and promotion and restore, decide
// who fits through this file: a party's size, the pool its attendance mode puts
// it in, its childcare demand, and "my own seat doesn't count" (an
// exceptRegistration id). Callers hold the occurrence row lock.

// partySeatsSQL is a registration's party size in SQL; partySeats is the same
// in Go.
const partySeatsSQL = "1 + registrations.guest_count"

func partySeats(r models.Registration) int { return 1 + r.GuestCount }

// childcareDemandSQL is the childcare places one registration asks for in SQL.
// effectiveChildcare is the same rule in Go, for a registration in hand.
const childcareDemandSQL = "registrations.childcare_count + CASE WHEN registrations.childcare_requested AND registrations.attendance_mode = 'in_person' THEN 1 ELSE 0 END"

// attendanceModeOf is a registration's pool: "" (a row written before
// the column had a value in Go) reads as in_person, the DB default.
func attendanceModeOf(mode string) string {
	if mode == models.AttendanceRemote {
		return models.AttendanceRemote
	}
	return models.AttendanceInPerson
}

// remoteUnavailable is whether mode asks for remote attendance the event does
// not offer.
func remoteUnavailable(ev *models.Event, mode string) bool {
	return attendanceModeOf(mode) == models.AttendanceRemote && !ev.RemoteAvailable
}

// capacityForMode is the effective cap of one pool on one date: the
// per-date override wins, nil is unlimited.
func capacityForMode(ev *models.Event, o *models.EventOccurrence, mode string) *int {
	if attendanceModeOf(mode) == models.AttendanceRemote {
		return events.EffectiveCapacity(ev.RemoteCapacity, o.RemoteCapacityOverride)
	}
	return events.EffectiveCapacity(ev.Capacity, o.CapacityOverride)
}

// seatsUsedForMode counts seats in one pool on an occurrence: one per
// going registration of that mode from a listed account plus its
// guest_count. Disabled and deleted accounts use no seats in either
// pool (DEC-38). exceptRegistration (0 for none) leaves that row out, so a
// replacement of someone's own seat is judged against everyone else. This is
// the one counted query shape for the lock path and the read path.
func seatsUsedForMode(db *gorm.DB, occurrenceID uint, mode string, exceptRegistration uint) (int, error) {
	var n int64
	err := db.Model(&models.Registration{}).
		Joins(listedRegistrations()).
		Where("registrations.occurrence_id = ? AND registrations.status = ? AND registrations.attendance_mode = ? AND registrations.id <> ?",
			occurrenceID, models.RegGoing, attendanceModeOf(mode), exceptRegistration).
		Select("COALESCE(SUM(" + partySeatsSQL + "), 0)").
		Scan(&n).Error
	return int(n), err
}

// seatsUsedMapForMode counts one pool's seats for many occurrences with
// the same query shape as seatsUsedForMode (GROUP BY instead of one id).
func seatsUsedMapForMode(db *gorm.DB, occurrenceIDs []uint, mode string) (map[uint]int, error) {
	out := map[uint]int{}
	if len(occurrenceIDs) == 0 {
		return out, nil
	}
	type row struct {
		OccurrenceID uint
		Seats        int64
	}
	var rows []row
	err := db.Model(&models.Registration{}).
		Joins(listedRegistrations()).
		Where("registrations.occurrence_id IN ? AND registrations.status = ? AND registrations.attendance_mode = ?",
			occurrenceIDs, models.RegGoing, attendanceModeOf(mode)).
		Select("registrations.occurrence_id, COALESCE(SUM(" + partySeatsSQL + "), 0) AS seats").
		Group("registrations.occurrence_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.OccurrenceID] = int(r.Seats)
	}
	return out, nil
}

// spotsLeftFor is capacity minus used, never below 0; nil when the pool
// is unlimited.
func spotsLeftFor(cap *int, used int) *int {
	if cap == nil {
		return nil
	}
	left := *cap - used
	if left < 0 {
		left = 0
	}
	return &left
}

// Use the same DEC-38 account filter as attendance pools, excluding this
// registration before checking a replacement request under the date lock.
func childcareUsedFor(db *gorm.DB, occurrenceID, exceptRegistration uint) (int, error) {
	var n int64
	err := db.Model(&models.Registration{}).Joins(listedRegistrations()).
		Joins("JOIN event_occurrences AS care_occurrence ON care_occurrence.id = registrations.occurrence_id JOIN events AS care_event ON care_event.id = care_occurrence.event_id AND care_event.childcare_available").
		Where("registrations.occurrence_id = ? AND registrations.status = ? AND registrations.id <> ?", occurrenceID, models.RegGoing, exceptRegistration).
		Select("COALESCE(SUM(" + childcareDemandSQL + "), 0)").Scan(&n).Error
	return int(n), err
}

func childcareUsedMap(db *gorm.DB, occurrenceIDs []uint) (map[uint]int, error) {
	out := map[uint]int{}
	if len(occurrenceIDs) == 0 {
		return out, nil
	}
	var rows []struct {
		OccurrenceID uint
		Children     int
	}
	err := db.Model(&models.Registration{}).Joins(listedRegistrations()).
		Joins("JOIN event_occurrences AS care_occurrence ON care_occurrence.id = registrations.occurrence_id JOIN events AS care_event ON care_event.id = care_occurrence.event_id AND care_event.childcare_available").
		Where("registrations.occurrence_id IN ? AND registrations.status = ?", occurrenceIDs, models.RegGoing).
		Select("registrations.occurrence_id, COALESCE(SUM(" + childcareDemandSQL + "), 0) AS children").
		Group("registrations.occurrence_id").Scan(&rows).Error
	for _, r := range rows {
		out[r.OccurrenceID] = r.Children
	}
	return out, err
}

func checkChildcare(tx *gorm.DB, ev *models.Event, occurrenceID, registrationID uint, count int) error {
	if !ev.ChildcareAvailable || count == 0 || ev.ChildcareCapacity == nil {
		return nil
	}
	used, err := childcareUsedFor(tx, occurrenceID, registrationID)
	if err != nil {
		return err
	}
	if !events.Fits(ev.ChildcareCapacity, used, count) {
		return errChildcareFull
	}
	return nil
}

// effectiveChildcare is the childcare places r asks for on ev: the same rule
// as childcareDemandSQL, which the aggregates use.
func effectiveChildcare(ev *models.Event, r models.Registration) int {
	if !ev.ChildcareAvailable {
		return 0
	}
	if r.HouseholdPersonID != nil {
		if r.ChildcareRequested && r.AttendanceMode != models.AttendanceRemote {
			return 1
		}
		return 0
	}
	return r.ChildcareCount
}

// poolFits is the ledger's one attendance decision: whether next's party fits
// in its pool, counting everyone but next's own current row. used is what
// everyone else holds in that pool.
func poolFits(tx *gorm.DB, ev *models.Event, o *models.EventOccurrence, next models.Registration) (fits bool, used int, cap *int, err error) {
	used, err = seatsUsedForMode(tx, o.ID, next.AttendanceMode, next.ID)
	if err != nil {
		return false, 0, nil, err
	}
	cap = capacityForMode(ev, o, next.AttendanceMode)
	return events.Fits(cap, used, partySeats(next)), used, cap, nil
}

// placeOnWaitlist makes next a waiter. A row keeps its place in line unless it
// has none or moved to the other pool.
func placeOnWaitlist(next *models.Registration, old models.Registration, now time.Time) {
	next.Status = models.RegWaitlisted
	if next.WaitlistedAt == nil || attendanceModeOf(old.AttendanceMode) != attendanceModeOf(next.AttendanceMode) {
		next.WaitlistedAt = &now
	}
}

// seatChange decides where a member's or guest's own answer lands. next wants
// to be going. It goes if it fits its pool, or if it already holds a seat in
// that pool and asks for no more; a party that holds a seat is never bumped to
// the waitlist, it just cannot grow (errEventFull). Otherwise it joins the
// waitlist when the event has one (next.Status becomes waitlisted), else
// errEventFull. A party that goes must also fit the childcare it adds.
func seatChange(tx *gorm.DB, ev *models.Event, o *models.EventOccurrence, old models.Registration, next *models.Registration, now time.Time) error {
	holds := old.ID != 0 && old.Status == models.RegGoing && attendanceModeOf(old.AttendanceMode) == attendanceModeOf(next.AttendanceMode)
	if !(holds && next.GuestCount <= old.GuestCount) {
		fits, _, _, err := poolFits(tx, ev, o, *next)
		if err != nil {
			return err
		}
		if !fits {
			if holds || !ev.WaitlistEnabled {
				return errEventFull
			}
			placeOnWaitlist(next, old, now)
			return nil
		}
	}
	if holds && effectiveChildcare(ev, *next) <= effectiveChildcare(ev, old) {
		return nil
	}
	return checkChildcare(tx, ev, o.ID, next.ID, effectiveChildcare(ev, *next))
}

// seatTally is an occurrence's pools as they stand under its lock: both
// attendance pools and the shared childcare pool. Promotion and restore walk a
// line of registrations against one tally, taking seats as they accept parties.
type seatTally struct {
	ev    *models.Event
	pools map[string]events.Pool
	care  events.Pool
}

func loadSeatTally(tx *gorm.DB, ev *models.Event, o *models.EventOccurrence) (*seatTally, error) {
	t := &seatTally{ev: ev, pools: map[string]events.Pool{}}
	for _, mode := range []string{models.AttendanceInPerson, models.AttendanceRemote} {
		used, err := seatsUsedForMode(tx, o.ID, mode, 0)
		if err != nil {
			return nil, err
		}
		t.pools[mode] = events.Pool{Capacity: capacityForMode(ev, o, mode), Used: used}
	}
	if ev.ChildcareAvailable && ev.ChildcareCapacity != nil {
		used, err := childcareUsedFor(tx, o.ID, 0)
		if err != nil {
			return nil, err
		}
		t.care = events.Pool{Capacity: ev.ChildcareCapacity, Used: used}
	}
	return t, nil
}

// entry is r as a waitlist entry.
func (t *seatTally) entry(r models.Registration) events.WaitEntry {
	return events.WaitEntry{ID: r.ID, Seats: partySeats(r), Mode: attendanceModeOf(r.AttendanceMode), Childcare: effectiveChildcare(t.ev, r)}
}

// pick is the waitlist entries that fit, in line order.
func (t *seatTally) pick(line []events.WaitEntry) []events.WaitEntry {
	return events.PickPromotionsByPool(line, t.pools, t.care)
}

// fits is whether a going r fits what is left.
func (t *seatTally) fits(r models.Registration) bool {
	e := t.entry(r)
	p := t.pools[e.Mode]
	return events.Fits(p.Capacity, p.Used, e.Seats) && (e.Childcare == 0 || events.Fits(t.care.Capacity, t.care.Used, e.Childcare))
}

// take counts r's party as seated.
func (t *seatTally) take(r models.Registration) {
	e := t.entry(r)
	p := t.pools[e.Mode]
	p.Used += e.Seats
	t.pools[e.Mode] = p
	t.care.Used += e.Childcare
}
