package main

import (
	"fold/internal/events"
	"fold/models"

	"gorm.io/gorm"
)

// Seat accounting for registrations: who counts toward a pool and how a
// party's size is read. Writers decide through this file only.

// attendanceModeOf is a registration's pool: "" (a row written before
// the column had a value in Go) reads as in_person, the DB default.
func attendanceModeOf(mode string) string {
	if mode == models.AttendanceRemote {
		return models.AttendanceRemote
	}
	return models.AttendanceInPerson
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
// pool (DEC-38). This is the one counted query shape for the lock path
// and the read path.
func seatsUsedForMode(db *gorm.DB, occurrenceID uint, mode string) (int, error) {
	var n int64
	err := db.Model(&models.Registration{}).
		Joins(listedRegistrations()).
		Where("registrations.occurrence_id = ? AND registrations.status = ? AND registrations.attendance_mode = ?",
			occurrenceID, models.RegGoing, attendanceModeOf(mode)).
		Select("COALESCE(SUM(1 + registrations.guest_count), 0)").
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
		Select("registrations.occurrence_id, COALESCE(SUM(1 + registrations.guest_count), 0) AS seats").
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
		Select("COALESCE(SUM(registrations.childcare_count + CASE WHEN registrations.childcare_requested AND registrations.attendance_mode = 'in_person' THEN 1 ELSE 0 END), 0)").Scan(&n).Error
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
		Select("registrations.occurrence_id, COALESCE(SUM(registrations.childcare_count + CASE WHEN registrations.childcare_requested AND registrations.attendance_mode = 'in_person' THEN 1 ELSE 0 END), 0) AS children").
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

// effectiveChildcare is shared by row decisions and the aggregate above.
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
