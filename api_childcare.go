package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"unicode/utf8"

	"fold/internal/events"
	"fold/models"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var (
	errChildcareUnavailable = apiErr(http.StatusBadRequest, "childcare_not_available", "this event doesn't offer childcare")
	errChildcareFull        = apiErr(http.StatusConflict, "childcare_full", "childcare is full for this date")
)

// A value RawMessage distinguishes an omitted capacity from explicit null.
func parseChildcareCapacity(raw json.RawMessage) (*int, bool, error) {
	if len(raw) == 0 {
		return nil, false, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, true, nil
	}
	var n int
	if json.Unmarshal(raw, &n) != nil || n < 0 || n > 100000 {
		return nil, false, errInvalidChildcareCapacity
	}
	if n == 0 {
		return nil, false, errChildcareCapacityZero
	}
	return &n, true, nil
}

func parseChildcareEdits(c *gin.Context, body eventBody, ed *capacityEdits) bool {
	var err error
	ed.childcareCap, ed.childcareCapSet, err = parseChildcareCapacity(body.ChildcareCapacity)
	if err != nil {
		fail(c, err)
		return false
	}
	if body.ChildcareNotes != nil && utf8.RuneCountInString(strings.TrimSpace(*body.ChildcareNotes)) > 500 {
		fail(c, errChildcareNotesTooLong)
		return false
	}
	if body.ChildcareAges != nil && utf8.RuneCountInString(strings.TrimSpace(*body.ChildcareAges)) > 100 {
		fail(c, errChildcareAgesTooLong)
		return false
	}
	return true
}

// Absent request fields preserve the registration, including when childcare
// has since been turned off. Stored requests then use no childcare places.
type childcareRequest struct {
	ChildcareCount    *int    `json:"childcareCount"`
	ChildcareAgesNote *string `json:"childcareAgesNote"`
}

// problem is the first thing wrong with the request, or nil. A nil event
// skips the checks that depend on the event's childcare setting.
func (r childcareRequest) problem(ev *models.Event) error {
	if r.ChildcareCount != nil {
		if *r.ChildcareCount < 0 || *r.ChildcareCount > 20 {
			return errInvalidChildcareCount
		}
		if ev != nil && *r.ChildcareCount > 0 && !ev.ChildcareAvailable {
			return errChildcareUnavailable
		}
	}
	if r.ChildcareAgesNote != nil && utf8.RuneCountInString(strings.TrimSpace(*r.ChildcareAgesNote)) > 200 {
		return errChildcareAgeNoteTooLong
	}
	return nil
}

func (r childcareRequest) validate(c *gin.Context, ev *models.Event) bool {
	if err := r.problem(ev); err != nil {
		fail(c, err)
		return false
	}
	return true
}

func (r childcareRequest) values(old models.Registration) (int, string) {
	count, ages := old.ChildcareCount, old.ChildcareAgesNote
	if r.ChildcareCount != nil {
		count = *r.ChildcareCount
	}
	if r.ChildcareAgesNote != nil {
		ages = strings.TrimSpace(*r.ChildcareAgesNote)
	}
	return count, ages
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
