package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sort"
	"strconv"
	"time"

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
	EventID       uint                        `json:"eventId"`
	Scope         string                      `json:"scope"`
	Date          string                      `json:"date,omitempty"`
	Status        string                      `json:"status"`
	Results       []seriesRegistrationResult  `json:"results"`
	Refusals      []seriesRegistrationRefusal `json:"refusals"`
	UndoToken     string                      `json:"undoToken,omitempty"`
	UndoExpiresAt *time.Time                  `json:"undoExpiresAt,omitempty"`
	CapacityLoss  []string                    `json:"capacityLoss,omitempty"`
}

const (
	seriesUndoTTL        = 10 * time.Second
	seriesUndoPendingTTL = 24 * time.Hour
)

func (a *App) seriesTime() time.Time {
	if a.seriesClock != nil {
		return a.seriesClock().UTC()
	}
	return time.Now().UTC()
}

type seriesUndoState struct {
	Exists             bool       `json:"exists"`
	RegistrationID     uint       `json:"registrationId,omitempty"`
	Status             string     `json:"status,omitempty"`
	GuestCount         int        `json:"guestCount,omitempty"`
	AttendanceMode     string     `json:"attendanceMode,omitempty"`
	ChildcareCount     int        `json:"childcareCount,omitempty"`
	ChildcareAgesNote  string     `json:"childcareAgesNote,omitempty"`
	ChildcareRequested bool       `json:"childcareRequested,omitempty"`
	Source             string     `json:"source,omitempty"`
	RegisteredBy       *uint      `json:"registeredBy,omitempty"`
	WaitlistedAt       *time.Time `json:"waitlistedAt,omitempty"`
	PromotedAt         *time.Time `json:"promotedAt,omitempty"`
	UpdatedAt          time.Time  `json:"updatedAt,omitempty"`
}

type seriesUndoItem struct {
	OccurrenceID uint            `json:"occurrenceId"`
	Date         string          `json:"date"`
	Before       seriesUndoState `json:"before"`
	After        seriesUndoState `json:"after"`
}

func undoState(r models.Registration) seriesUndoState {
	if r.ID == 0 {
		return seriesUndoState{}
	}
	return seriesUndoState{Exists: true, RegistrationID: r.ID, Status: r.Status, GuestCount: r.GuestCount,
		AttendanceMode: attendanceModeOf(r.AttendanceMode), ChildcareCount: r.ChildcareCount,
		ChildcareAgesNote: r.ChildcareAgesNote, ChildcareRequested: r.ChildcareRequested, Source: r.Source,
		RegisteredBy: r.RegisteredBy, WaitlistedAt: r.WaitlistedAt, PromotedAt: r.PromotedAt, UpdatedAt: r.UpdatedAt}
}

func sameUndoState(a, b seriesUndoState) bool {
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	return string(aj) == string(bj)
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
	// Input checks that don't depend on the event run before any lock is
	// taken. Guest and childcare availability need the event, so they wait
	// until the actor is known to be allowed to see it.
	mode := ""
	if body.AttendanceMode != nil {
		mode = *body.AttendanceMode
	}
	if !validMode(mode) {
		jsonError(c, http.StatusBadRequest, "attendanceMode must be in_person or remote")
		return
	}
	care := childcareRequest{ChildcareCount: body.ChildcareCount, ChildcareAgesNote: body.ChildcareAgesNote}
	if msg := care.problem(nil); msg != "" {
		jsonError(c, http.StatusBadRequest, msg)
		return
	}
	guests := 0
	if body.GuestCount != nil {
		guests = *body.GuestCount
	}
	eventID := uint(eventID64)
	actorAtRequest := apiUser(c)
	response := seriesRegistrationResponse{EventID: eventID, Scope: body.Scope, Date: body.Date, Status: body.Status,
		Results: []seriesRegistrationResult{}, Refusals: []seriesRegistrationRefusal{}}
	var mailed []uint
	undoRaw, tokenErr := newRawToken()
	if tokenErr != nil {
		log.Printf("series registration: %v", tokenErr)
		jsonError(c, http.StatusInternalServerError, "could not save registration")
		return
	}
	undoHash := hashToken(undoRaw)
	var undoItems []seriesUndoItem
	var undoReceiptID uint

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

		// The series scope is chosen only after the contact, actor, group, and
		// event locks are held. A request that waited across a start boundary
		// must not freeze a date that has already begun.
		selectionNow := a.seriesTime()
		q := tx.Model(&models.EventOccurrence{}).
			Where("event_id = ? AND status = ? AND starts_at > ?", ev.ID, models.OccurrenceScheduled, selectionNow)
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

		if guests < 0 || guests > ev.GuestsAllowed {
			return errSeriesGuests
		}
		if care.problem(&ev) != "" {
			return errSeriesChildcareUnavailable
		}

		// One read for the actor's answers on every selected date.
		lockedIDs := make([]uint, len(locked))
		for i := range locked {
			lockedIDs[i] = locked[i].ID
		}
		var priorRows []models.Registration
		if err := tx.Where("occurrence_id IN ? AND user_id = ?", lockedIDs, actor.ID).Find(&priorRows).Error; err != nil {
			return err
		}
		prior := make(map[uint]models.Registration, len(priorRows))
		for _, r := range priorRows {
			prior[r.OccurrenceID] = r
		}

		for i := range locked {
			o := &locked[i]
			// Refresh after every selected occurrence is locked. This timestamp
			// governs both date eligibility and the shared promotion cutoff path.
			dateNow := a.seriesTime()
			if o.Status != models.OccurrenceScheduled || !o.StartsAt.After(dateNow) ||
				(body.Scope == seriesScopeThisAndFollowing && o.OriginalDate < body.Date) {
				continue
			}
			// A hidden per-date override is omitted rather than returned as a
			// refusal, because even its date is not authorized result data.
			dateView := viewOf(dateEvent(&ev, o), false)
			if !policy.CanRegisterEvent(asPerson(&actor), dateView, gp, rel) {
				continue
			}
			old := prior[o.ID]
			change, err := prepareRegistration(old, o, userSubject(actor.ID), body.Status, guests, mode, care, nil, dateNow)
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
			ids, err := a.applyRegistrationBatch(tx, &ev, o, []registrationChange{change}, dateNow, actor.ID, c.ClientIP())
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
			undoItems = append(undoItems, seriesUndoItem{OccurrenceID: o.ID, Date: o.OriginalDate, Before: undoState(old), After: undoState(after)})

			// An ordinarily-authorized bulk Can't go still ends exact-date
			// provenance on that date, matching the single-date path. The
			// provenance never helped authorize this series mutation.
			if body.Status == models.RegDeclined && old.ID != 0 {
				var access models.OccurrenceAccess
				if err := tx.Where("registration_id = ? AND subject_user_id = ? AND occurrence_id = ? AND status = ?", old.ID, actor.ID, o.ID, policy.OccurrenceAccessActive).Limit(1).Find(&access).Error; err != nil {
					return err
				}
				if access.ID != 0 {
					ids, err := endOccurrenceAccessIn(tx, &access, policy.OccurrenceDeclined, &actor.ID, dateNow, c.ClientIP())
					if err != nil {
						return err
					}
					mailed = append(mailed, ids...)
				}
			}
		}
		if len(undoItems) > 0 {
			snapshot, err := json.Marshal(undoItems)
			if err != nil {
				return err
			}
			receiptNow := a.seriesTime()
			// Only the latest receipt for an actor can still match their current
			// state. Removing older receipts also bounds retained snapshot data;
			// expired rows for every actor are opportunistically swept here.
			if err := tx.Where("expires_at <= ? OR actor_id = ?", receiptNow, actor.ID).Delete(&models.SeriesUndoReceipt{}).Error; err != nil {
				return err
			}
			// The raw token is not returned until post-commit delivery finishes.
			// A bounded pending expiry keeps the row valid while that work runs;
			// finalizeSeriesUndoReceipt replaces it with the promised response-time
			// window, or suppresses Undo if a later action already replaced it.
			receipt := models.SeriesUndoReceipt{TokenHash: undoHash, ActorID: actor.ID, EventID: ev.ID,
				Snapshot: string(snapshot), ExpiresAt: receiptNow.Add(seriesUndoPendingTTL), CreatedAt: receiptNow}
			if err := tx.Create(&receipt).Error; err != nil {
				return err
			}
			undoReceiptID = receipt.ID
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
		case errors.Is(err, errSeriesChildcareUnavailable):
			jsonError(c, http.StatusBadRequest, errChildcareUnavailable.Error())
		default:
			log.Printf("series registration: %v", err)
			jsonError(c, http.StatusInternalServerError, "could not save registration")
		}
		return
	}
	// Stable ordering is part of the mutation/result boundary even if future
	// implementation changes build the slices concurrently.
	sort.Slice(response.Results, func(i, j int) bool { return response.Results[i].Date < response.Results[j].Date })
	sort.Slice(response.Refusals, func(i, j int) bool { return response.Refusals[i].Date < response.Refusals[j].Date })
	a.deliverNotificationEmails(mailed)
	if undoReceiptID != 0 {
		if expires, ok := a.finalizeSeriesUndoReceipt(undoReceiptID, undoHash, actorAtRequest.ID, eventID); ok {
			response.UndoToken, response.UndoExpiresAt = undoRaw, &expires
		}
	}
	c.JSON(http.StatusOK, response)
}

// finalizeSeriesUndoReceipt starts the usable Undo interval after committed
// notification delivery. Its row lock makes the refreshed time post-wait. The
// full identity predicate prevents a deleted or replaced receipt from being
// recreated or extended by an older response finishing late.
func (a *App) finalizeSeriesUndoReceipt(receiptID uint, tokenHash string, actorID, eventID uint) (time.Time, bool) {
	var expires time.Time
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		var receipt models.SeriesUndoReceipt
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND actor_id = ? AND event_id = ? AND used_at IS NULL", receiptID, actorID, eventID).
			First(&receipt).Error; err != nil {
			return err
		}
		if receipt.TokenHash != tokenHash {
			return gorm.ErrRecordNotFound
		}
		expires = a.seriesTime().Add(seriesUndoTTL)
		res := tx.Model(&models.SeriesUndoReceipt{}).
			Where("id = ? AND actor_id = ? AND event_id = ? AND used_at IS NULL", receiptID, actorID, eventID).
			Update("expires_at", expires)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
	return expires, err == nil
}

type seriesUndoInput struct {
	Token string `json:"token"`
}

func (a *App) apiUndoSeriesRegistration(c *gin.Context) {
	eventID64, parseErr := strconv.ParseUint(c.Param("id"), 10, 64)
	var body seriesUndoInput
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if parseErr != nil || eventID64 == 0 || decoder.Decode(&body) != nil || body.Token == "" {
		jsonError(c, http.StatusBadRequest, "undo token is required")
		return
	}
	actorAtRequest := apiUser(c)
	response := seriesRegistrationResponse{EventID: uint(eventID64), Results: []seriesRegistrationResult{}, Refusals: []seriesRegistrationRefusal{}, CapacityLoss: []string{}}
	var mailed []uint

	err := a.DB.Transaction(func(tx *gorm.DB) error {
		// Peek only to identify the actor/event. The actual single-use decision
		// happens after the standard contact -> actor lock order, matching normal
		// and bulk registration mutations and avoiding a receipt/contact inversion.
		var receipt models.SeriesUndoReceipt
		if err := tx.Where("token_hash = ?", hashToken(body.Token)).First(&receipt).Error; err != nil {
			return errSeriesUndoUnavailable
		}
		if receipt.ActorID != actorAtRequest.ID {
			return errSeriesUndoOtherActor
		}
		if receipt.EventID != uint(eventID64) {
			return errSeriesUndoUnavailable
		}
		if err := lockContacts(tx); err != nil {
			return err
		}
		var actor models.User
		if err := tx.Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).First(&actor, actorAtRequest.ID).Error; err != nil || !actor.CanSignIn() || actor.SessionVersion != actorAtRequest.SessionVersion {
			return errSeriesUndoAuthority
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", receipt.ID).First(&receipt).Error; err != nil {
			return errSeriesUndoUnavailable
		}
		if receipt.UsedAt != nil {
			return errSeriesUndoUsed
		}
		var items []seriesUndoItem
		if err := json.Unmarshal([]byte(receipt.Snapshot), &items); err != nil || len(items) == 0 {
			return errSeriesUndoUnavailable
		}
		var ev models.Event
		if err := tx.First(&ev, receipt.EventID).Error; err != nil || ev.RRule == "" {
			return errSeriesUndoAuthority
		}
		gp, rel := policy.Group{}, policy.Rel{}
		if ev.GroupID != nil {
			var group models.Group
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&group, *ev.GroupID).Error; err != nil {
				return errSeriesUndoAuthority
			}
			gp, rel = asGroup(&group), relIn(tx, actor.ID, group.ID)
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&ev, receipt.EventID).Error; err != nil ||
			!policy.CanRegisterEvent(asPerson(&actor), viewOf(&ev, false), gp, rel) {
			return errSeriesUndoAuthority
		}

		locked := make([]models.EventOccurrence, len(items))
		for i, item := range items {
			o, err := lockOccurrence(tx, item.OccurrenceID)
			if err != nil || o.EventID != ev.ID {
				return errSeriesUndoEnded
			}
			locked[i] = *o
			var current models.Registration
			if err := tx.Where("occurrence_id = ? AND user_id = ?", o.ID, actor.ID).Limit(1).Find(&current).Error; err != nil {
				return err
			}
			if !sameUndoState(undoState(current), item.After) {
				return errSeriesUndoChanged
			}
		}

		// Receipt and date clocks are checked only after every lock that can
		// make this request wait. The ten-second interval is half-open: exactly
		// ExpiresAt is expired.
		undoNow := a.seriesTime()
		if !undoNow.Before(receipt.ExpiresAt) {
			return errSeriesUndoExpired
		}
		for i := range locked {
			o := &locked[i]
			if o.Status != models.OccurrenceScheduled || !o.StartsAt.After(undoNow) ||
				!policy.CanRegisterEvent(asPerson(&actor), viewOf(dateEvent(&ev, o), false), gp, rel) {
				return errSeriesUndoEnded
			}
		}
		receipt.UsedAt = &undoNow
		if err := tx.Model(&receipt).Update("used_at", undoNow).Error; err != nil {
			return err
		}

		for i, item := range items {
			o := &locked[i]
			dateNow := a.seriesTime()
			if o.Status != models.OccurrenceScheduled || !o.StartsAt.After(dateNow) {
				return errSeriesUndoEnded
			}
			var current models.Registration
			if err := tx.Where("occurrence_id = ? AND user_id = ?", o.ID, actor.ID).First(&current).Error; err != nil {
				return err
			}
			beforePublic := registrationState(current)
			savepoint := "series_undo_" + strconv.Itoa(i)
			if err := tx.SavePoint(savepoint).Error; err != nil {
				return err
			}
			if !item.Before.Exists {
				zero, empty := 0, ""
				change, err := prepareRegistration(current, o, userSubject(actor.ID), models.RegDeclined, 0, current.AttendanceMode,
					childcareRequest{ChildcareCount: &zero, ChildcareAgesNote: &empty}, nil, dateNow)
				if err == nil {
					var ids []uint
					ids, err = a.applyRegistrationBatch(tx, &ev, o, []registrationChange{change}, dateNow, actor.ID, c.ClientIP())
					mailed = append(mailed, ids...)
				}
				if err == nil {
					err = tx.Delete(&models.Registration{}, current.ID).Error
				}
				if err != nil {
					return err
				}
				response.Results = append(response.Results, seriesRegistrationResult{OccurrenceID: o.ID, Date: item.Date, Before: beforePublic, After: seriesRegistrationState{}})
				continue
			}

			next := current
			next.Status, next.GuestCount, next.AttendanceMode = item.Before.Status, item.Before.GuestCount, item.Before.AttendanceMode
			next.ChildcareCount, next.ChildcareAgesNote, next.ChildcareRequested = item.Before.ChildcareCount, item.Before.ChildcareAgesNote, item.Before.ChildcareRequested
			next.Source, next.RegisteredBy, next.WaitlistedAt, next.PromotedAt = item.Before.Source, item.Before.RegisteredBy, item.Before.WaitlistedAt, item.Before.PromotedAt
			change := registrationChange{old: current, next: next, found: true}
			ids, err := a.applyRegistrationBatch(tx, &ev, o, []registrationChange{change}, dateNow, actor.ID, c.ClientIP())
			if err != nil {
				if code := seriesRefusalCode(err); code != "" {
					if rollbackErr := tx.RollbackTo(savepoint).Error; rollbackErr != nil {
						return rollbackErr
					}
					response.Refusals = append(response.Refusals, seriesRegistrationRefusal{OccurrenceID: o.ID, Date: item.Date, Code: code})
					if item.Before.Status == models.RegGoing {
						response.CapacityLoss = append(response.CapacityLoss, item.Date)
					}
					continue
				}
				return err
			}
			mailed = append(mailed, ids...)
			var restored models.Registration
			if err := tx.First(&restored, current.ID).Error; err != nil {
				return err
			}
			afterPublic := registrationState(restored)
			if restored.Status == models.RegWaitlisted {
				position, err := waitlistPosition(tx, o.ID, restored.ID)
				if err != nil {
					return err
				}
				afterPublic.WaitlistPosition = &position
			}
			if item.Before.Status == models.RegGoing && restored.Status != models.RegGoing {
				response.CapacityLoss = append(response.CapacityLoss, item.Date)
			}
			response.Results = append(response.Results, seriesRegistrationResult{OccurrenceID: o.ID, Date: item.Date, Before: beforePublic, After: afterPublic})
		}
		// Keep cleanup explicit and cheap; at most one live receipt per actor is
		// created, and spent/expired receipts disappear on subsequent use.
		return tx.Where("expires_at <= ?", a.seriesTime()).Delete(&models.SeriesUndoReceipt{}).Error
	})
	if err != nil {
		switch {
		case errors.Is(err, errSeriesUndoOtherActor):
			jsonError(c, http.StatusForbidden, "this Undo belongs to someone else")
		case errors.Is(err, errSeriesUndoExpired):
			jsonError(c, http.StatusConflict, "the Undo time has ended")
		case errors.Is(err, errSeriesUndoUsed):
			jsonError(c, http.StatusConflict, "this Undo was already used")
		case errors.Is(err, errSeriesUndoChanged):
			jsonError(c, http.StatusConflict, "an answer changed after this action; refresh before trying again")
		case errors.Is(err, errSeriesUndoAuthority):
			jsonError(c, http.StatusConflict, "you can no longer answer this series")
		case errors.Is(err, errSeriesUndoEnded):
			jsonError(c, http.StatusConflict, "one of these dates is no longer open")
		case errors.Is(err, errSeriesUndoUnavailable):
			jsonError(c, http.StatusNotFound, "Undo not found")
		default:
			log.Printf("series undo: %v", err)
			jsonError(c, http.StatusInternalServerError, "could not undo registration")
		}
		return
	}
	sort.Strings(response.CapacityLoss)
	a.deliverNotificationEmails(mailed)
	c.JSON(http.StatusOK, response)
}

var (
	errSeriesUnavailable          = errors.New("series unavailable")
	errSeriesNoDates              = errors.New("series has no future dates")
	errSeriesGuests               = errors.New("series guest count invalid")
	errSeriesChildcareUnavailable = errors.New("series childcare unavailable")
	errSeriesUndoUnavailable      = errors.New("series undo unavailable")
	errSeriesUndoOtherActor       = errors.New("series undo other actor")
	errSeriesUndoExpired          = errors.New("series undo expired")
	errSeriesUndoUsed             = errors.New("series undo used")
	errSeriesUndoChanged          = errors.New("series undo state changed")
	errSeriesUndoAuthority        = errors.New("series undo authority revoked")
	errSeriesUndoEnded            = errors.New("series undo occurrence ended")
)
