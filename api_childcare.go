package main

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"fold/models"
	"github.com/gin-gonic/gin"
)

var (
	errChildcareUnavailable = apiErr(http.StatusBadRequest, "childcare_not_available", "this event doesn't offer childcare")
	errChildcareFull        = apiErr(http.StatusConflict, "childcare_full", "childcare is full for this date")
)

func parseChildcareEdits(body eventBody, ed *capacityEdits) error {
	var err error
	ed.childcareCap, ed.childcareCapSet, err = parseChildcareCapacity(body.ChildcareCapacity)
	if err != nil {
		return err
	}
	if body.ChildcareNotes != nil && utf8.RuneCountInString(strings.TrimSpace(*body.ChildcareNotes)) > 500 {
		return errChildcareNotesTooLong
	}
	if body.ChildcareAges != nil && utf8.RuneCountInString(strings.TrimSpace(*body.ChildcareAges)) > 100 {
		return errChildcareAgesTooLong
	}
	return nil
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
		if *r.ChildcareCount < 0 || *r.ChildcareCount > maxChildcareCount {
			return errInvalidChildcareCount
		}
		if ev != nil && *r.ChildcareCount > 0 && !ev.ChildcareAvailable {
			return errChildcareUnavailable
		}
	}
	if r.ChildcareAgesNote != nil && utf8.RuneCountInString(strings.TrimSpace(*r.ChildcareAgesNote)) > maxChildcareAgesNote {
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
