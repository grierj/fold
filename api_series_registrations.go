package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"time"
	"unicode/utf8"

	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Series answers intentionally store no recurring rule. The candidate
// occurrence IDs are frozen once, ordered by (original_date, id), and only
// those rows are locked and changed. A later occurrences.extend run therefore
// creates unanswered dates.
const (
	seriesScopeAllFuture        = "all_future"
	seriesScopeThisAndFollowing = "this_and_following"
)

type seriesRegistrationInput struct {
	Date              string  `json:"date"`
	Scope             string  `json:"scope"`
	Status            string  `json:"status"`
	GuestCount        *int    `json:"guestCount"`
	AttendanceMode    *string `json:"attendanceMode"`
	ChildcareCount    *int    `json:"childcareCount"`
	ChildcareAgesNote *string `json:"childcareAgesNote"`
}

type seriesRegistrationState struct {
	RegistrationID    uint   `json:"registrationId,omitempty"`
	Status            string `json:"status,omitempty"`
	GuestCount        int    `json:"guestCount,omitempty"`
	AttendanceMode    string `json:"attendanceMode,omitempty"`
	ChildcareCount    int    `json:"childcareCount,omitempty"`
	ChildcareAgesNote string `json:"childcareAgesNote,omitempty"`
	WaitlistPosition  *int   `json:"waitlistPosition,omitempty"`
}

type seriesRegistrationResult struct {
	OccurrenceID uint                    `json:"occurrenceId"`
	Date         string                  `json:"date"`
	Before       seriesRegistrationState `json:"before"`
	After        seriesRegistrationState `json:"after"`
}

type seriesRegistrationRefusal struct {
	OccurrenceID uint   `json:"occurrenceId"`
	Date         string `json:"date"`
	Code         string `json:"code"`
}

type seriesRegistrationResponse struct {
	EventID  uint                        `json:"eventId"`
	Scope    string                      `json:"scope"`
	Date     string                      `json:"date,omitempty"`
	Status   string                      `json:"status"`
	Results  []seriesRegistrationResult  `json:"results"`
	Refusals []seriesRegistrationRefusal `json:"refusals"`
}

// registrationState is deliberately actor-only. Keeping the exact before and
// after boundary in one value lets the Undo checkpoint persist a trustworthy
// snapshot without re-reading mutable registrations after this transaction.
func registrationState(r models.Registration) seriesRegistrationState {
	if r.ID == 0 {
		return seriesRegistrationState{}
	}
	return seriesRegistrationState{RegistrationID: r.ID, Status: r.Status, GuestCount: r.GuestCount,
		AttendanceMode: attendanceModeOf(r.AttendanceMode), ChildcareCount: r.ChildcareCount,
		ChildcareAgesNote: r.ChildcareAgesNote}
}

func seriesRefusalCode(err error) string {
	switch {
	case errors.Is(err, errRemovedBlocked):
		return "registration_removed"
	case errors.Is(err, errEventFull):
		return "event_full"
	case errors.Is(err, errChildcareFull):
		return "childcare_full"
	case errors.Is(err, errChildcareUnavailable):
		return "childcare_not_available"
	case errors.Is(err, errRemoteNotAvailable):
		return "remote_not_available"
	default:
		return ""
	}
}

func (a *App) apiRegisterSeries(c *gin.Context) {
	eventID64, parseErr := strconv.ParseUint(c.Param("id"), 10, 64)
	var body seriesRegistrationInput
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if parseErr != nil || eventID64 == 0 || decoder.Decode(&body) != nil || !validAnswer(body.Status) {
		jsonError(c, http.StatusBadRequest, "status and scope are required")
		return
	}
	if body.Scope != seriesScopeAllFuture && body.Scope != seriesScopeThisAndFollowing {
		jsonError(c, http.StatusBadRequest, "scope must be all_future or this_and_following")
		return
	}
	if body.Scope == seriesScopeThisAndFollowing {
		if _, err := time.Parse("2006-01-02", body.Date); err != nil {
			jsonError(c, http.StatusBadRequest, "date must be YYYY-MM-DD")
			return
		}
	}
	eventID := uint(eventID64)
	actorAtRequest := apiUser(c)
	now := time.Now().UTC()
	response := seriesRegistrationResponse{EventID: eventID, Scope: body.Scope, Date: body.Date, Status: body.Status,
		Results: []seriesRegistrationResult{}, Refusals: []seriesRegistrationRefusal{}}
	var mailed []uint

	err := a.DB.Transaction(func(tx *gorm.DB) error {
		// Compatible order with membership, event, exact-date, and manager
		// registration mutations: contact gate -> actor -> group -> event ->
		// occurrence(s). Occurrences are always locked in stable scope order, so
		// overlapping bulk answers cannot invert locks.
		if err := lockContacts(tx); err != nil {
			return err
		}
		var actor models.User
		if err := tx.Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).First(&actor, actorAtRequest.ID).Error; err != nil {
			return err
		}
		if !actor.CanSignIn() || actor.SessionVersion != actorAtRequest.SessionVersion {
			return errSeriesUnavailable
		}
		var candidateEvent models.Event
		if err := tx.First(&candidateEvent, eventID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errSeriesUnavailable
			}
			return err
		}
		gp, rel := policy.Group{}, policy.Rel{}
		var lockedGroup models.Group
		if candidateEvent.GroupID != nil {
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&lockedGroup, *candidateEvent.GroupID).Error; err != nil {
				return errSeriesUnavailable
			}
			gp = asGroup(&lockedGroup)
			rel = relIn(tx, actor.ID, lockedGroup.ID)
		}
		var ev models.Event
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&ev, eventID).Error; err != nil {
			return errSeriesUnavailable
		}
		if ev.RRule == "" || (candidateEvent.GroupID == nil) != (ev.GroupID == nil) ||
			(ev.GroupID != nil && *ev.GroupID != lockedGroup.ID) {
			return errSeriesUnavailable
		}
		// Seeing one opened or explicitly invited date is not series authority.
		if !policy.CanRegisterEvent(asPerson(&actor), viewOf(&ev, false), gp, rel) {
			return errSeriesUnavailable
		}

		q := tx.Model(&models.EventOccurrence{}).
			Where("event_id = ? AND status = ? AND starts_at > ?", ev.ID, models.OccurrenceScheduled, now)
		if body.Scope == seriesScopeThisAndFollowing {
			q = q.Where("original_date >= ?", body.Date)
		}
		var candidates []models.EventOccurrence
		if err := q.Order("original_date, id").Find(&candidates).Error; err != nil {
			return err
		}
		if len(candidates) == 0 {
			return errSeriesNoDates
		}

		locked := make([]models.EventOccurrence, 0, len(candidates))
		for _, candidate := range candidates {
			o, err := lockOccurrence(tx, candidate.ID)
			if err != nil {
				return err
			}
			locked = append(locked, *o)
		}
		// Group, event, and membership authority remain stable behind the
		// group/event locks. The locked occurrence rows below are still checked
		// individually because their status or clock may have changed while the
		// candidate query waited.

		guests := 0
		if body.GuestCount != nil {
			guests = *body.GuestCount
		}
		if guests < 0 || guests > ev.GuestsAllowed {
			return errSeriesGuests
		}
		mode := ""
		if body.AttendanceMode != nil {
			mode = *body.AttendanceMode
		}
		if !validMode(mode) {
			return errSeriesMode
		}
		care := childcareRequest{ChildcareCount: body.ChildcareCount, ChildcareAgesNote: body.ChildcareAgesNote}
		if body.ChildcareCount != nil && (*body.ChildcareCount < 0 || *body.ChildcareCount > 20) {
			return errSeriesChildcareCount
		}
		if body.ChildcareAgesNote != nil && utf8.RuneCountInString(*body.ChildcareAgesNote) > 200 {
			return errSeriesChildcareNote
		}

		for i := range locked {
			o := &locked[i]
			if o.Status != models.OccurrenceScheduled || !o.StartsAt.After(now) ||
				(body.Scope == seriesScopeThisAndFollowing && o.OriginalDate < body.Date) {
				continue
			}
			// A hidden per-date override is omitted rather than returned as a
			// refusal, because even its date is not authorized result data.
			dateView := viewOf(dateEvent(&ev, o), false)
			if !policy.CanRegisterEvent(asPerson(&actor), dateView, gp, rel) {
				continue
			}
			var old models.Registration
			if err := tx.Where("occurrence_id = ? AND user_id = ?", o.ID, actor.ID).Limit(1).Find(&old).Error; err != nil {
				return err
			}
			if old.Status == models.RegRemoved {
				response.Refusals = append(response.Refusals, seriesRegistrationRefusal{OccurrenceID: o.ID, Date: o.OriginalDate, Code: "registration_removed"})
				continue
			}
			change, err := prepareRegistration(old, o, userSubject(actor.ID), body.Status, guests, mode, care, nil, now)
			if err != nil {
				if code := seriesRefusalCode(err); code != "" {
					response.Refusals = append(response.Refusals, seriesRegistrationRefusal{OccurrenceID: o.ID, Date: o.OriginalDate, Code: code})
					continue
				}
				return err
			}
			savepoint := "series_date_" + strconv.Itoa(i)
			if err := tx.SavePoint(savepoint).Error; err != nil {
				return err
			}
			ids, err := a.applyRegistrationBatch(tx, &ev, o, []registrationChange{change}, now, actor.ID, c.ClientIP())
			if err != nil {
				if code := seriesRefusalCode(err); code != "" {
					if rollbackErr := tx.RollbackTo(savepoint).Error; rollbackErr != nil {
						return rollbackErr
					}
					response.Refusals = append(response.Refusals, seriesRegistrationRefusal{OccurrenceID: o.ID, Date: o.OriginalDate, Code: code})
					continue
				}
				return err
			}
			mailed = append(mailed, ids...)
			var after models.Registration
			if err := tx.Where("occurrence_id = ? AND user_id = ?", o.ID, actor.ID).First(&after).Error; err != nil {
				return err
			}
			beforeState, afterState := registrationState(old), registrationState(after)
			if after.Status == models.RegWaitlisted {
				position, err := waitlistPosition(tx, o.ID, after.ID)
				if err != nil {
					return err
				}
				afterState.WaitlistPosition = &position
			}
			response.Results = append(response.Results, seriesRegistrationResult{OccurrenceID: o.ID, Date: o.OriginalDate, Before: beforeState, After: afterState})

			// An ordinarily-authorized bulk Can't go still ends exact-date
			// provenance on that date, matching the single-date path. The
			// provenance never helped authorize this series mutation.
			if body.Status == models.RegDeclined && old.ID != 0 {
				var access models.OccurrenceAccess
				if err := tx.Where("registration_id = ? AND subject_user_id = ? AND occurrence_id = ? AND status = ?", old.ID, actor.ID, o.ID, policy.OccurrenceAccessActive).Limit(1).Find(&access).Error; err != nil {
					return err
				}
				if access.ID != 0 {
					ids, err := endOccurrenceAccessIn(tx, &access, policy.OccurrenceDeclined, &actor.ID, now, c.ClientIP())
					if err != nil {
						return err
					}
					mailed = append(mailed, ids...)
				}
			}
		}
		return nil
	})
	if err != nil {
		switch {
		case errors.Is(err, errSeriesUnavailable):
			jsonError(c, http.StatusNotFound, "series not found")
		case errors.Is(err, errSeriesNoDates):
			jsonError(c, http.StatusConflict, "this series has no future dates to answer")
		case errors.Is(err, errSeriesGuests):
			jsonErrorCode(c, http.StatusBadRequest, "too_many_guests", "too many guests")
		case errors.Is(err, errSeriesMode):
			jsonError(c, http.StatusBadRequest, "attendanceMode must be in_person or remote")
		case errors.Is(err, errSeriesChildcareCount):
			jsonError(c, http.StatusBadRequest, "childcareCount must be a whole number from 0 to 20")
		case errors.Is(err, errSeriesChildcareNote):
			jsonError(c, http.StatusBadRequest, "childcare age note must be 200 characters or fewer")
		default:
			jsonError(c, http.StatusInternalServerError, err.Error())
		}
		return
	}
	// Stable ordering is part of the mutation/result boundary even if future
	// implementation changes build the slices concurrently.
	sort.Slice(response.Results, func(i, j int) bool { return response.Results[i].Date < response.Results[j].Date })
	sort.Slice(response.Refusals, func(i, j int) bool { return response.Refusals[i].Date < response.Refusals[j].Date })
	a.deliverNotificationEmails(mailed)
	c.JSON(http.StatusOK, response)
}

var (
	errSeriesUnavailable    = errors.New("series unavailable")
	errSeriesNoDates        = errors.New("series has no future dates")
	errSeriesGuests         = errors.New("series guest count invalid")
	errSeriesMode           = errors.New("series attendance mode invalid")
	errSeriesChildcareCount = errors.New("series childcare count invalid")
	errSeriesChildcareNote  = errors.New("series childcare note invalid")
)
