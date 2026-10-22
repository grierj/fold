package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"fold/internal/events"
	"fold/models"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var (
	errChildcareUnavailable = errors.New("this event doesn't offer childcare")
	errChildcareFull        = errors.New("childcare is full for this date")
)

// A value RawMessage distinguishes an omitted capacity from explicit null.
func parseChildcareCapacity(raw json.RawMessage) (*int, bool, string) {
	if len(raw) == 0 {
		return nil, false, ""
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, true, ""
	}
	var n int
	if json.Unmarshal(raw, &n) != nil || n < 0 || n > 100000 {
		return nil, false, "childcare limit must be a whole number from 1 to 100000, or empty for no limit"
	}
	if n == 0 {
		return nil, false, "Uncheck childcare instead of a 0 childcare limit"
	}
	return &n, true, ""
}

func parseChildcareEdits(c *gin.Context, body eventBody, ed *capacityEdits) bool {
	var msg string
	ed.childcareCap, ed.childcareCapSet, msg = parseChildcareCapacity(body.ChildcareCapacity)
	if msg != "" {
		jsonError(c, http.StatusBadRequest, msg)
		return false
	}
	if body.ChildcareNotes != nil && utf8.RuneCountInString(strings.TrimSpace(*body.ChildcareNotes)) > 500 {
		jsonError(c, http.StatusBadRequest, "childcare notes must be 500 characters or fewer")
		return false
	}
	if body.ChildcareAges != nil && utf8.RuneCountInString(strings.TrimSpace(*body.ChildcareAges)) > 100 {
		jsonError(c, http.StatusBadRequest, "childcare ages must be 100 characters or fewer")
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

func (r childcareRequest) validate(c *gin.Context, ev *models.Event) bool {
	if r.ChildcareCount != nil {
		if *r.ChildcareCount < 0 || *r.ChildcareCount > 20 {
			jsonError(c, http.StatusBadRequest, "childcareCount must be a whole number from 0 to 20")
			return false
		}
		if *r.ChildcareCount > 0 && !ev.ChildcareAvailable {
			jsonError(c, http.StatusBadRequest, errChildcareUnavailable.Error())
			return false
		}
	}
	if r.ChildcareAgesNote != nil && utf8.RuneCountInString(strings.TrimSpace(*r.ChildcareAgesNote)) > 200 {
		jsonError(c, http.StatusBadRequest, "childcare age note must be 200 characters or fewer")
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
	err := db.Model(&models.Registration{}).Joins(listedUsers("registrations")).
		Where("registrations.occurrence_id = ? AND registrations.status = ? AND registrations.id <> ?", occurrenceID, models.RegGoing, exceptRegistration).
		Select("COALESCE(SUM(registrations.childcare_count), 0)").Scan(&n).Error
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
	err := db.Model(&models.Registration{}).Joins(listedUsers("registrations")).
		Where("registrations.occurrence_id IN ? AND registrations.status = ?", occurrenceIDs, models.RegGoing).
		Select("registrations.occurrence_id, COALESCE(SUM(registrations.childcare_count), 0) AS children").
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
