package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"fold/internal/audit"
	"fold/internal/events"
	"fold/internal/notify"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Registration seat management (5.4, M1.5). Every write that can change
// seats runs in one transaction that locks the occurrence row first
// (SELECT ... FOR UPDATE), recounts seats inside the lock, writes, runs
// promotion when a seat freed or capacity grew, and writes audit and
// notification rows. Email delivery runs after commit through
// deliverNotificationEmails. Decision 1 (owner, Oct 15): one seat
// bucket; remote capacity arrives with M1.8.

var (
	// errEventFull: a going request on a full event with the waitlist off.
	errEventFull = errors.New("this event is full")
	// errRemovedBlocked: a pruned registration blocks self-registration.
	errRemovedBlocked = errors.New("a leader or host removed your registration for this date; ask them to add you back")
	// errRegNotFound: no matching registration row.
	errRegNotFound = errors.New("registration not found")
	// errOccurrenceNotFound: no occurrence for the event and date.
	errOccurrenceNotFound = errors.New("occurrence not found")
)

// parseCapacity reads an event capacity from a body: null clears it to
// unlimited, a positive whole number sets it. It mirrors the group
// member limit's number-or-null JSON pattern (DEC-39). raw is nil when
// the field is absent.
func parseCapacity(raw *json.RawMessage) (cap *int, set bool, msg string) {
	if raw == nil {
		return nil, false, ""
	}
	if bytes.Equal(bytes.TrimSpace(*raw), []byte("null")) {
		return nil, true, ""
	}
	var n int
	if json.Unmarshal(*raw, &n) != nil || n < 1 || n > 100000 {
		return nil, false, "capacity must be a positive whole number, or empty for no limit"
	}
	return &n, true, ""
}

// capacityEdits holds the parsed M1.5 capacity fields from an event
// body: capacity (nil clears to unlimited), waitlistEnabled, and
// guestsAllowed (0-5, null clears to 0).
type capacityEdits struct {
	cap       *int
	capSet    bool
	guests    int
	guestsSet bool
}

// parseGuestsAllowed reads guestsAllowed from a body: null clears it to
// 0 (no extra guests), a whole number 0-5 sets the per-registration
// guest cap. raw is nil when the field is absent.
func parseGuestsAllowed(raw *json.RawMessage) (n int, set bool, msg string) {
	if raw == nil {
		return 0, false, ""
	}
	if bytes.Equal(bytes.TrimSpace(*raw), []byte("null")) {
		return 0, true, ""
	}
	var v int
	if json.Unmarshal(*raw, &v) != nil || v < 0 || v > 5 {
		return 0, false, "guestsAllowed must be a whole number from 0 to 5, or empty for none"
	}
	return v, true, ""
}

// parseCapacityEdits parses the M1.5 capacity fields, answering 400 on a
// bad value.
func parseCapacityEdits(c *gin.Context, body eventBody) (capacityEdits, bool) {
	var ed capacityEdits
	var msg string
	ed.cap, ed.capSet, msg = parseCapacity(body.Capacity)
	if msg != "" {
		jsonError(c, http.StatusBadRequest, msg)
		return ed, false
	}
	ed.guests, ed.guestsSet, msg = parseGuestsAllowed(body.GuestsAllowed)
	if msg != "" {
		jsonError(c, http.StatusBadRequest, msg)
		return ed, false
	}
	return ed, true
}

// intPtrEqual reports whether two optional ints hold the same value.
func intPtrEqual(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// seatsUsed counts seats on an occurrence: one per going registration of
// a listed account plus its guest_count. Disabled and deleted accounts
// use no seats (DEC-38). This is the one counted query shape for the
// lock path and the read path.
func seatsUsed(db *gorm.DB, occurrenceID uint) (int, error) {
	var n int64
	err := db.Model(&models.Registration{}).
		Joins(listedUsers("registrations")).
		Where("registrations.occurrence_id = ? AND registrations.status = ?",
			occurrenceID, models.RegGoing).
		Select("COALESCE(SUM(1 + registrations.guest_count), 0)").
		Scan(&n).Error
	return int(n), err
}

// seatsUsedMap counts seats for many occurrences with the same query
// shape as seatsUsed (GROUP BY instead of one id).
func seatsUsedMap(db *gorm.DB, occurrenceIDs []uint) (map[uint]int, error) {
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
		Joins(listedUsers("registrations")).
		Where("registrations.occurrence_id IN ? AND registrations.status = ?",
			occurrenceIDs, models.RegGoing).
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

// loadWaitlist returns the waitlisted registrations of listed accounts in
// promotion order (waitlisted_at, then id). A disabled or deleted account
// keeps its row and its place; it is simply skipped in the pass.
func loadWaitlist(tx *gorm.DB, occurrenceID uint) ([]models.Registration, error) {
	var wl []models.Registration
	err := tx.Joins(listedUsers("registrations")).
		Where("registrations.occurrence_id = ? AND registrations.status = ?",
			occurrenceID, models.RegWaitlisted).
		Order("registrations.waitlisted_at ASC, registrations.id ASC").
		Find(&wl).Error
	return wl, err
}

// waitlistPosition is the 1-based position of regID in promotion order
// among listed accounts, computed on read (Decision 2: never stored).
// It is 0 when the registration is not waitlisted.
func waitlistPosition(db *gorm.DB, occurrenceID, regID uint) (int, error) {
	wl, err := loadWaitlist(db, occurrenceID)
	if err != nil {
		return 0, err
	}
	for i, r := range wl {
		if r.ID == regID {
			return i + 1, nil
		}
	}
	return 0, nil
}

// lockOccurrence locks one occurrence row for a seat-changing write.
func lockOccurrence(tx *gorm.DB, occurrenceID uint) (*models.EventOccurrence, error) {
	var o models.EventOccurrence
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&o, occurrenceID).Error; err != nil {
		return nil, err
	}
	return &o, nil
}

// hostIDs lists the hosts of a church-wide event.
func hostIDs(tx *gorm.DB, eventID uint) []uint {
	var ids []uint
	tx.Model(&models.EventHost{}).Where("event_id = ?", eventID).Pluck("user_id", &ids)
	return ids
}

// eventNoticeVars builds the guarded Vars and link for one recipient of
// an event notice, following the notifyOccurrencesCancelled shape: a
// registrant who cannot see the group gets no group name, place, or
// group link (PR #41).
func eventNoticeVars(tx *gorm.DB, ev *models.Event, o *models.EventOccurrence, userID uint) (notify.Vars, string) {
	var g *models.Group
	if ev.GroupID != nil {
		var row models.Group
		if tx.First(&row, *ev.GroupID).Error == nil && canSeeGroupIn(tx, viewerIn(tx, userID), &row) {
			g = &row
		}
	}
	// The place resolves through the date's override, like
	// occurrenceToJSON: a date with its own place keeps it even when
	// the viewer may see the group.
	place := ev.Location
	if o.LocationOverride != nil {
		place = *o.LocationOverride
	}
	vars := notify.Vars{Event: ev.Title, When: formatWhen(o.StartsAt, recipientZone(tx, userID)), Place: place}
	link := fmt.Sprintf("/events/%d", ev.ID)
	if g != nil {
		vars.Event = firstNonEmpty(ev.Title, g.Name) // group-ok: g is set only when canSeeGroupIn is true
		if o.LocationOverride == nil {
			vars.Place = firstNonEmpty(ev.Location, g.Location) // group-ok: g is set only when canSeeGroupIn is true
		}
		link = fmt.Sprintf("/groups/%d", g.ID)
	}
	return vars, link
}

// notifyRegistrationPromoted tells one person they are in off the
// waitlist. The email carries the rotated manage link with the one-click
// "I can't come" cancel; the bell never shows the token.
func notifyRegistrationPromoted(tx *gorm.DB, ev *models.Event, o *models.EventOccurrence,
	userID uint, manageRaw string, actor *uint, app *App) ([]uint, error) {
	vars, link := eventNoticeVars(tx, ev, o, userID)
	vars.ManageURL = app.emailBaseURL() + "/guest/manage/" + manageRaw
	end := o.EndsAt
	if end.IsZero() {
		end = o.StartsAt
	}
	return notify.Send(tx, notify.Msg{
		Kind: notify.RegistrationPromoted, To: []uint{userID}, Actor: actor,
		SubjectType: "occurrence", SubjectID: o.ID, Link: link, Vars: vars,
		Data:      map[string]any{"eventId": ev.ID, "date": o.OriginalDate},
		ExpiresAt: &end,
	})
}

// notifyWaitlistCutoff tells the leaders or hosts that a seat opened
// inside the 2-hour cutoff, so they can promote by hand. Admins get it
// only when they also lead or host.
func notifyWaitlistCutoff(tx *gorm.DB, ev *models.Event, o *models.EventOccurrence, actor *uint) ([]uint, error) {
	var to []uint
	if ev.GroupID != nil {
		to = leaderIDs(tx, *ev.GroupID)
	} else {
		to = hostIDs(tx, ev.ID)
	}
	if len(to) == 0 {
		return nil, nil
	}
	vars, link := eventNoticeVars(tx, ev, o, to[0])
	end := o.EndsAt
	if end.IsZero() {
		end = o.StartsAt
	}
	return notify.Send(tx, notify.Msg{
		Kind: notify.WaitlistCutoff, To: to, Actor: actor,
		SubjectType: "occurrence", SubjectID: o.ID, Link: link, Vars: vars,
		Data:      map[string]any{"eventId": ev.ID, "date": o.OriginalDate},
		ExpiresAt: &end,
	})
}

// notifyEventChanged tells everyone going or waitlisted on one occurrence
// that its time or place changed. One item per person per occurrence per
// change; the item is never batched.
func notifyEventChanged(tx *gorm.DB, ev *models.Event, o *models.EventOccurrence, variant string, actor uint) ([]uint, error) {
	if o.Status != models.OccurrenceScheduled {
		// No fan-out into a cancelled date: nobody is going, and the
		// waitlist must not be mailed about a dead event.
		return nil, nil
	}
	var userIDs []uint
	if err := tx.Table("registrations").Select("user_id").
		Joins(listedUsers("registrations")).
		Where("registrations.occurrence_id = ? AND registrations.status IN ?",
			o.ID, []string{models.RegGoing, models.RegWaitlisted}).
		Order("user_id").Pluck("user_id", &userIDs).Error; err != nil {
		return nil, err
	}
	var ids []uint
	for _, uid := range userIDs {
		vars, link := eventNoticeVars(tx, ev, o, uid)
		vars.Change = variant
		end := o.EndsAt
		if end.IsZero() {
			end = o.StartsAt
		}
		sent, err := notify.Send(tx, notify.Msg{
			Kind: notify.EventChanged, To: []uint{uid}, Actor: &actor,
			SubjectType: "occurrence", SubjectID: o.ID, Link: link, Vars: vars,
			Data:      map[string]any{"eventId": ev.ID, "date": o.OriginalDate, "change": variant},
			ExpiresAt: &end,
		})
		if err != nil {
			return nil, err
		}
		ids = append(ids, sent...)
	}
	return ids, nil
}

// notifyRegistrationRemoved tells a pruned person they were removed. The
// note reaches only them.
func notifyRegistrationRemoved(tx *gorm.DB, ev *models.Event, o *models.EventOccurrence,
	userID uint, note string, actor uint) ([]uint, error) {
	vars, link := eventNoticeVars(tx, ev, o, userID)
	vars.Note = note
	end := o.EndsAt
	if end.IsZero() {
		end = o.StartsAt
	}
	return notify.Send(tx, notify.Msg{
		Kind: notify.RegistrationRemoved, To: []uint{userID}, Actor: &actor,
		SubjectType: "occurrence", SubjectID: o.ID, Link: link, Vars: vars,
		Data:      map[string]any{"eventId": ev.ID, "date": o.OriginalDate},
		ExpiresAt: &end,
	})
}

// notifyRegistrationNotRestored tells a re-enabled person their
// registration was dropped from a full occurrence (DEC-47).
func notifyRegistrationNotRestored(tx *gorm.DB, ev *models.Event, o *models.EventOccurrence, userID uint, actor uint) ([]uint, error) {
	vars, link := eventNoticeVars(tx, ev, o, userID)
	end := o.EndsAt
	if end.IsZero() {
		end = o.StartsAt
	}
	return notify.Send(tx, notify.Msg{
		Kind: notify.RegistrationNotRestored, To: []uint{userID}, Actor: &actor,
		SubjectType: "occurrence", SubjectID: o.ID, Link: link, Vars: vars,
		Data:      map[string]any{"eventId": ev.ID, "date": o.OriginalDate},
		ExpiresAt: &end,
	})
}

// promoteLocked promotes waitlisted registrations that fit, in line
// order, under the caller's occurrence lock. now is the clock: handlers
// pass time.Now().UTC(), tests pass a fixed clock. Inside the 2-hour
// cutoff nobody is promoted; the leaders or hosts get one cutoff notice
// instead. A promoted row becomes going, keeps waitlisted_at as history,
// and gets a rotated manage token for the "I can't come" link. Returns
// notification ids to email after commit.
func (a *App) promoteLocked(tx *gorm.DB, ev *models.Event, o *models.EventOccurrence, now time.Time, actor *uint) ([]uint, error) {
	if o.Status != models.OccurrenceScheduled {
		// Nobody is promoted into a cancelled date.
		return nil, nil
	}
	cap := events.EffectiveCapacity(ev.Capacity, o.CapacityOverride)
	wl, err := loadWaitlist(tx, o.ID)
	if err != nil {
		return nil, err
	}
	if len(wl) == 0 {
		return nil, nil
	}
	if cap == nil {
		// Unlimited: everyone in line fits.
		return a.promoteRows(tx, ev, o, wl, now, actor)
	}
	used, err := seatsUsed(tx, o.ID)
	if err != nil {
		return nil, err
	}
	if events.InCutoff(now, o.StartsAt) {
		if used < *cap {
			return notifyWaitlistCutoff(tx, ev, o, actor)
		}
		return nil, nil
	}
	entries := make([]events.WaitEntry, len(wl))
	for i, r := range wl {
		entries[i] = events.WaitEntry{ID: r.ID, Seats: 1 + r.GuestCount}
	}
	picks := events.PickPromotions(*cap, used, entries, false)
	if len(picks) == 0 {
		return nil, nil
	}
	byID := make(map[uint]models.Registration, len(wl))
	for _, r := range wl {
		byID[r.ID] = r
	}
	toPromote := make([]models.Registration, 0, len(picks))
	for _, p := range picks {
		toPromote = append(toPromote, byID[p.ID])
	}
	return a.promoteRows(tx, ev, o, toPromote, now, actor)
}

func (a *App) promoteRows(tx *gorm.DB, ev *models.Event, o *models.EventOccurrence,
	rows []models.Registration, now time.Time, actor *uint) ([]uint, error) {
	var emailed []uint
	for _, r := range rows {
		raw, hash, err := createInvitationToken()
		if err != nil {
			return nil, err
		}
		res := tx.Model(&models.Registration{}).
			Where("id = ? AND status = ?", r.ID, models.RegWaitlisted).
			Updates(map[string]any{
				"status": models.RegGoing, "promoted_at": now, "manage_token_hash": hash,
			})
		if res.Error != nil {
			return nil, res.Error
		}
		if res.RowsAffected == 0 {
			continue // lost a race with another seat-changing write
		}
		ids, err := notifyRegistrationPromoted(tx, ev, o, r.UserID, raw, actor, a)
		if err != nil {
			return nil, err
		}
		emailed = append(emailed, ids...)
	}
	return emailed, nil
}

// ---------------------------------------------------------------------------
// Member registration (PUT /api/v1/events/:id/registrations), rewritten on
// the locked path (M1.5).
// ---------------------------------------------------------------------------

// registrationOutcome is what one locked register call decided.
type registrationOutcome struct {
	reg      models.Registration
	position *int // waitlist position; nil unless waitlisted
}

// registerLocked runs one member registration change under the
// occurrence lock. want is going, maybe, or declined.
func (a *App) registerLocked(tx *gorm.DB, ev *models.Event, o *models.EventOccurrence,
	userID uint, want string, guestCount int, now time.Time) (*registrationOutcome, []uint, error) {
	var existing models.Registration
	found := tx.Where("user_id = ? AND occurrence_id = ?", userID, o.ID).
		First(&existing).RowsAffected == 1
	if found && existing.Status == models.RegRemoved {
		return nil, nil, errRemovedBlocked
	}
	cap := events.EffectiveCapacity(ev.Capacity, o.CapacityOverride)
	used, err := seatsUsed(tx, o.ID)
	if err != nil {
		return nil, nil, err
	}
	fits := func(seats int) bool { return cap == nil || used+seats <= *cap }

	var emailed []uint
	out := &registrationOutcome{}
	setStatus := func(status string) error {
		updates := map[string]any{"status": status, "guest_count": guestCount}
		if status == models.RegWaitlisted {
			if existing.WaitlistedAt == nil {
				updates["waitlisted_at"] = now
			}
		} else {
			updates["waitlisted_at"] = nil
			updates["promoted_at"] = nil
			if status == models.RegMaybe || status == models.RegDeclined {
				// Leaving kills the emailed manage link, so an old
				// link can't cancel a future re-registration (M1.5).
				updates["manage_token_hash"] = ""
			}
		}
		return tx.Model(&models.Registration{}).Where("id = ?", existing.ID).Updates(updates).Error
	}

	switch want {
	case models.RegGoing:
		switch {
		case !found || existing.Status == models.RegMaybe || existing.Status == models.RegDeclined ||
			existing.Status == models.RegCancelled || existing.Status == models.RegWaitlisted:
			// A new going request at this moment (maybe/declined to going
			// counts as one; a waitlisted re-request is re-evaluated).
			seats := 1 + guestCount
			if fits(seats) {
				if !found {
					existing = models.Registration{
						UserID: userID, OccurrenceID: o.ID,
						Status: models.RegGoing, GuestCount: guestCount, Source: models.RegSourceWeb,
					}
					if err := tx.Create(&existing).Error; err != nil {
						return nil, nil, err
					}
				} else {
					if err := setStatus(models.RegGoing); err != nil {
						return nil, nil, err
					}
					existing.Status = models.RegGoing
					existing.GuestCount = guestCount
				}
				out.reg = existing
				return out, emailed, nil
			}
			if !ev.WaitlistEnabled {
				return nil, nil, errEventFull
			}
			if !found {
				existing = models.Registration{
					UserID: userID, OccurrenceID: o.ID,
					Status: models.RegWaitlisted, GuestCount: guestCount,
					Source:       models.RegSourceWeb,
					WaitlistedAt: &now,
				}
				if err := tx.Create(&existing).Error; err != nil {
					return nil, nil, err
				}
			} else {
				updates := map[string]any{
					"status": models.RegWaitlisted, "guest_count": guestCount,
				}
				if existing.WaitlistedAt == nil {
					updates["waitlisted_at"] = now
				}
				if err := tx.Model(&models.Registration{}).Where("id = ?", existing.ID).
					Updates(updates).Error; err != nil {
					return nil, nil, err
				}
				existing.Status = models.RegWaitlisted
				existing.GuestCount = guestCount
				if existing.WaitlistedAt == nil {
					existing.WaitlistedAt = &now
				}
			}
			pos, err := waitlistPosition(tx, o.ID, existing.ID)
			if err != nil {
				return nil, nil, err
			}
			out.reg, out.position = existing, &pos
			return out, emailed, nil
		default:
			// existing.Status == going: a guest-count change.
			oldSeats := 1 + existing.GuestCount
			newSeats := 1 + guestCount
			if newSeats > oldSeats && !fits(newSeats-oldSeats) {
				// Never demote a seated party to the waitlist; the old
				// registration stays unchanged.
				return nil, nil, errEventFull
			}
			if err := tx.Model(&models.Registration{}).Where("id = ?", existing.ID).
				Update("guest_count", guestCount).Error; err != nil {
				return nil, nil, err
			}
			existing.GuestCount = guestCount
			out.reg = existing
			if newSeats < oldSeats {
				ids, err := a.promoteLocked(tx, ev, o, now, nil)
				if err != nil {
					return nil, nil, err
				}
				emailed = append(emailed, ids...)
			}
			return out, emailed, nil
		}
	default: // maybe, declined
		if !found {
			existing = models.Registration{
				UserID: userID, OccurrenceID: o.ID,
				Status: want, GuestCount: guestCount, Source: models.RegSourceWeb,
			}
			if err := tx.Create(&existing).Error; err != nil {
				return nil, nil, err
			}
			out.reg = existing
			return out, emailed, nil
		}
		wasGoing := existing.Status == models.RegGoing
		if err := setStatus(want); err != nil {
			return nil, nil, err
		}
		existing.Status = want
		existing.GuestCount = guestCount
		out.reg = existing
		if wasGoing {
			ids, err := a.promoteLocked(tx, ev, o, now, nil)
			if err != nil {
				return nil, nil, err
			}
			emailed = append(emailed, ids...)
		}
		return out, emailed, nil
	}
}

// registrationJSON is the register endpoint's registration object
// (M1.5): the final status, the guest count, and the waitlist position
// (null unless the person is waitlisted).
type registrationJSON struct {
	UserID           uint   `json:"userId"`
	EventID          uint   `json:"eventId"`
	OccurrenceID     uint   `json:"occurrenceId"`
	Date             string `json:"date"`
	Status           string `json:"status"`
	GuestCount       int    `json:"guestCount"`
	WaitlistPosition *int   `json:"waitlistPosition"`
}

func (a *App) apiRegister(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var ev models.Event
	if err := a.DB.First(&ev, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "event not found")
		return
	}
	f := a.factsFor(apiUser(c), &ev)
	gp := policy.Group{}
	if f.group != nil {
		gp = asGroup(f.group)
	}
	if !policy.CanRegisterEvent(asPerson(apiUser(c)), f.view, gp, f.rel) {
		jsonError(c, http.StatusNotFound, "event not found")
		return
	}
	var body struct {
		Status       string `json:"status"`
		Date         string `json:"date"`
		GuestCount   *int   `json:"guestCount"`
		OverCapacity *bool  `json:"overCapacity"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "status and date are required")
		return
	}
	if body.OverCapacity != nil && *body.OverCapacity {
		// overCapacity is a leader/host choice on the add action, never
		// a self-registration flag.
		jsonError(c, http.StatusBadRequest, "overCapacity is not allowed here")
		return
	}
	switch body.Status {
	case models.RegGoing, models.RegMaybe, models.RegDeclined:
	default:
		jsonError(c, http.StatusBadRequest, "status must be going, maybe, or declined")
		return
	}
	guestCount := 0
	if body.GuestCount != nil {
		guestCount = *body.GuestCount
	}
	if guestCount < 0 || guestCount > ev.GuestsAllowed {
		jsonErrorCode(c, http.StatusBadRequest, "too_many_guests",
			fmt.Sprintf("you can bring at most %d guests", ev.GuestsAllowed))
		return
	}
	var o models.EventOccurrence
	if err := a.DB.Where("event_id = ? AND original_date = ? AND status = ?",
		ev.ID, body.Date, models.OccurrenceScheduled).First(&o).Error; err != nil {
		jsonError(c, http.StatusNotFound, "that date is not on this event")
		return
	}
	u := apiUser(c)
	var outcome *registrationOutcome
	var emailed []uint
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		locked, err := lockOccurrence(tx, o.ID)
		if err != nil {
			return err
		}
		out, ids, err := a.registerLocked(tx, &ev, locked, u.ID, body.Status, guestCount, time.Now().UTC())
		if err != nil {
			return err
		}
		outcome, emailed = out, ids
		return nil
	})
	switch {
	case errors.Is(err, errEventFull):
		jsonError(c, http.StatusConflict, "This event is full")
		return
	case errors.Is(err, errRemovedBlocked):
		jsonError(c, http.StatusConflict, errRemovedBlocked.Error())
		return
	case err != nil:
		jsonError(c, http.StatusInternalServerError, "could not save registration")
		return
	}
	a.deliverNotificationEmails(emailed)
	resp := registrationJSON{
		UserID: u.ID, EventID: ev.ID, OccurrenceID: o.ID,
		Date: body.Date, Status: outcome.reg.Status,
		GuestCount: outcome.reg.GuestCount, WaitlistPosition: outcome.position,
	}
	c.JSON(http.StatusOK, gin.H{"registration": resp})
}

// ---------------------------------------------------------------------------
// Leader and host actions (DEC-20, DEC-30, M1.5). Every action is gated by
// policy.CanManageEvent, runs on the locked path, and writes its audit row
// in the same transaction as the change and its notification.
// ---------------------------------------------------------------------------

func (a *App) apiRemoveRegistration(c *gin.Context) {
	ev, ok := a.loadManagedEvent(c)
	if !ok {
		return
	}
	var body struct {
		UserID uint   `json:"userId"`
		Date   string `json:"date"`
		Note   string `json:"note"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.UserID == 0 || body.Date == "" {
		jsonError(c, http.StatusBadRequest, "userId and date are required")
		return
	}
	note := strings.TrimSpace(body.Note)
	if len(note) > 500 {
		jsonError(c, http.StatusBadRequest, "note is too long")
		return
	}
	var o models.EventOccurrence
	if err := a.DB.Where("event_id = ? AND original_date = ? AND status = ?",
		ev.ID, body.Date, models.OccurrenceScheduled).First(&o).Error; err != nil {
		jsonError(c, http.StatusNotFound, "that date is not on this event")
		return
	}
	me := apiUser(c)
	var emailed []uint
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		locked, err := lockOccurrence(tx, o.ID)
		if err != nil {
			return err
		}
		var r models.Registration
		if err := tx.Where("user_id = ? AND occurrence_id = ? AND status NOT IN ?",
			body.UserID, locked.ID, []string{models.RegRemoved, models.RegCancelled}).First(&r).Error; err != nil {
			return errRegNotFound
		}
		if err := tx.Model(&models.Registration{}).Where("id = ?", r.ID).Updates(map[string]any{
			"status": models.RegRemoved, "removed_by": me.ID, "removal_note": note,
			"waitlisted_at": nil, "promoted_at": nil, "manage_token_hash": "",
		}).Error; err != nil {
			return err
		}
		if err := audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.RegistrationRemoved,
			TargetType: "event", TargetID: ev.ID,
			Meta: map[string]any{"userId": r.UserID, "occurrenceId": locked.ID, "note": note},
			IP:   c.ClientIP(),
		}); err != nil {
			return err
		}
		ids, err := notifyRegistrationRemoved(tx, ev, locked, r.UserID, note, me.ID)
		if err != nil {
			return err
		}
		emailed = append(emailed, ids...)
		// No auto-promotion here: pruning is deliberate, and the leader
		// may be freeing the seat for a specific add or hand promotion.
		return nil
	})
	if errors.Is(err, errRegNotFound) {
		jsonError(c, http.StatusNotFound, "no registration to remove")
		return
	}
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not remove registration")
		return
	}
	a.deliverNotificationEmails(emailed)
	c.JSON(http.StatusOK, gin.H{})
}

func (a *App) apiPromoteRegistration(c *gin.Context) {
	ev, ok := a.loadManagedEvent(c)
	if !ok {
		return
	}
	var body struct {
		UserID uint   `json:"userId"`
		Date   string `json:"date"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.UserID == 0 || body.Date == "" {
		jsonError(c, http.StatusBadRequest, "userId and date are required")
		return
	}
	var o models.EventOccurrence
	if err := a.DB.Where("event_id = ? AND original_date = ? AND status = ?",
		ev.ID, body.Date, models.OccurrenceScheduled).First(&o).Error; err != nil {
		jsonError(c, http.StatusNotFound, "that date is not on this event")
		return
	}
	me := apiUser(c)
	var emailed []uint
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		locked, err := lockOccurrence(tx, o.ID)
		if err != nil {
			return err
		}
		var r models.Registration
		if err := tx.Where("user_id = ? AND occurrence_id = ? AND status = ?",
			body.UserID, locked.ID, models.RegWaitlisted).First(&r).Error; err != nil {
			return errRegNotFound
		}
		cap := events.EffectiveCapacity(ev.Capacity, locked.CapacityOverride)
		used, err := seatsUsed(tx, locked.ID)
		if err != nil {
			return err
		}
		seats := 1 + r.GuestCount
		if cap != nil && used+seats > *cap {
			return errEventFull
		}
		raw, hash, err := createInvitationToken()
		if err != nil {
			return err
		}
		if err := tx.Model(&models.Registration{}).Where("id = ?", r.ID).Updates(map[string]any{
			"status": models.RegGoing, "promoted_at": time.Now().UTC(), "manage_token_hash": hash,
		}).Error; err != nil {
			return err
		}
		if err := audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.RegistrationPromoted,
			TargetType: "event", TargetID: ev.ID,
			Meta: map[string]any{"userId": r.UserID, "occurrenceId": locked.ID},
			IP:   c.ClientIP(),
		}); err != nil {
			return err
		}
		ids, err := notifyRegistrationPromoted(tx, ev, locked, r.UserID, raw, &me.ID, a)
		if err != nil {
			return err
		}
		emailed = append(emailed, ids...)
		return nil
	})
	switch {
	case errors.Is(err, errRegNotFound):
		jsonError(c, http.StatusNotFound, "no waitlisted registration for that person")
		return
	case errors.Is(err, errEventFull):
		jsonError(c, http.StatusConflict, "that party doesn't fit in the seats left")
		return
	case err != nil:
		jsonError(c, http.StatusInternalServerError, "could not promote registration")
		return
	}
	a.deliverNotificationEmails(emailed)
	c.JSON(http.StatusOK, gin.H{})
}

func (a *App) apiAddRegistration(c *gin.Context) {
	ev, ok := a.loadManagedEvent(c)
	if !ok {
		return
	}
	var body struct {
		UserID       uint   `json:"userId"`
		Email        string `json:"email"`
		Date         string `json:"date"`
		GuestCount   *int   `json:"guestCount"`
		OverCapacity *bool  `json:"overCapacity"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Date == "" || (body.UserID == 0 && body.Email == "") {
		jsonError(c, http.StatusBadRequest, "userId or email, and date, are required")
		return
	}
	guestCount := 0
	if body.GuestCount != nil {
		guestCount = *body.GuestCount
	}
	if guestCount < 0 || guestCount > ev.GuestsAllowed {
		jsonErrorCode(c, http.StatusBadRequest, "too_many_guests",
			fmt.Sprintf("at most %d guests are allowed", ev.GuestsAllowed))
		return
	}
	var target models.User
	// A pruned guest keeps status=guest (never active); the add action is
	// their way back in, so both statuses resolve here.
	if body.UserID != 0 {
		if err := a.DB.Where("id = ? AND status IN ?", body.UserID,
			[]string{models.UserActive, models.UserGuest}).First(&target).Error; err != nil {
			jsonError(c, http.StatusNotFound, "person not found")
			return
		}
	} else {
		// The leader UI adds by email; resolve it to the account.
		if err := a.DB.Where("email = ? AND status IN ?", strings.TrimSpace(body.Email),
			[]string{models.UserActive, models.UserGuest}).First(&target).Error; err != nil {
			jsonError(c, http.StatusNotFound, "person not found")
			return
		}
	}
	// The leader can't seat someone on an event the person couldn't see
	// themselves: that would bypass the audited visibility path and
	// mail them the event's place. Mirrors the register route's check,
	// run for the target instead of the actor.
	tf := a.factsFor(&target, ev)
	tgp := policy.Group{}
	if tf.group != nil {
		tgp = asGroup(tf.group)
	}
	if !policy.CanRegisterEvent(asPerson(&target), tf.view, tgp, tf.rel) {
		jsonError(c, http.StatusNotFound, "event not found")
		return
	}
	var o models.EventOccurrence
	if err := a.DB.Where("event_id = ? AND original_date = ? AND status = ?",
		ev.ID, body.Date, models.OccurrenceScheduled).First(&o).Error; err != nil {
		jsonError(c, http.StatusNotFound, "that date is not on this event")
		return
	}
	me := apiUser(c)
	overCap := body.OverCapacity != nil && *body.OverCapacity
	var emailed []uint
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		locked, err := lockOccurrence(tx, o.ID)
		if err != nil {
			return err
		}
		cap := events.EffectiveCapacity(ev.Capacity, locked.CapacityOverride)
		used, err := seatsUsed(tx, locked.ID)
		if err != nil {
			return err
		}
		seats := 1 + guestCount
		over := cap != nil && used+seats > *cap
		if over && !overCap {
			return errEventFull
		}
		var r models.Registration
		found := tx.Where("user_id = ? AND occurrence_id = ?", target.ID, locked.ID).
			First(&r).RowsAffected == 1
		updates := map[string]any{
			"status": models.RegGoing, "guest_count": guestCount,
			"removed_by": nil, "removal_note": "", "waitlisted_at": nil, "promoted_at": nil,
		}
		if found {
			if err := tx.Model(&models.Registration{}).Where("id = ?", r.ID).Updates(updates).Error; err != nil {
				return err
			}
		} else {
			r = models.Registration{
				UserID: target.ID, OccurrenceID: locked.ID,
				Status: models.RegGoing, GuestCount: guestCount, Source: models.RegSourceAdmin,
			}
			if err := tx.Create(&r).Error; err != nil {
				return err
			}
		}
		action := audit.RegistrationAdded
		meta := map[string]any{"userId": target.ID, "occurrenceId": locked.ID}
		if over {
			action = audit.RegistrationAddedOverCapacity
			meta["seats"] = used + seats
			meta["capacity"] = *cap
		}
		if err := audit.Write(tx, audit.Entry{Actor: &me.ID, Action: action,
			TargetType: "event", TargetID: ev.ID, Meta: meta, IP: c.ClientIP(),
		}); err != nil {
			return err
		}
		return nil
	})
	switch {
	case errors.Is(err, errEventFull):
		body := errorBody(http.StatusConflict, "This event is full")
		body["full"] = true
		c.JSON(http.StatusConflict, body)
		return
	case err != nil:
		jsonError(c, http.StatusInternalServerError, "could not add registration")
		return
	}
	a.deliverNotificationEmails(emailed)
	c.JSON(http.StatusOK, gin.H{})
}

// ---------------------------------------------------------------------------
// Disabled and re-enabled accounts (DEC-38, DEC-47, M1.5).
// ---------------------------------------------------------------------------

// promoteOnDisable frees every seat a disabled account held on future
// scheduled occurrences and promotes into them. The rows stay; they are
// hidden and uncounted while the account is disabled.
func (a *App) promoteOnDisable(tx *gorm.DB, userID uint) ([]uint, error) {
	var occIDs []uint
	if err := tx.Model(&models.Registration{}).
		Joins("JOIN event_occurrences ON event_occurrences.id = registrations.occurrence_id").
		Where("registrations.user_id = ? AND registrations.status = ? AND event_occurrences.status = ? AND event_occurrences.starts_at > ?",
			userID, models.RegGoing, models.OccurrenceScheduled, time.Now().UTC()).
		Distinct().Pluck("registrations.occurrence_id", &occIDs).Error; err != nil {
		return nil, err
	}
	var emailed []uint
	now := time.Now().UTC()
	for _, id := range occIDs {
		o, err := lockOccurrence(tx, id)
		if err != nil {
			return nil, err
		}
		var ev models.Event
		if err := tx.First(&ev, o.EventID).Error; err != nil {
			return nil, err
		}
		ids, err := a.promoteLocked(tx, &ev, o, now, nil)
		if err != nil {
			return nil, err
		}
		emailed = append(emailed, ids...)
	}
	return emailed, nil
}

// restoreRegistrations runs when a disabled account is enabled again,
// beside restoreMemberships. Only going rows on future scheduled
// occurrences are checked, ordered by occurrence start then registration
// id, locking one occurrence at a time. A row that fits stays going and
// counts again once the account is enabled. A row that would overfill is
// deleted, audited (registration.not_restored), and the person gets an
// ask-a-leader notice. Waitlisted rows keep their place and need no check.
func restoreRegistrations(c *gin.Context, tx *gorm.DB, userID uint) ([]uint, error) {
	actor := apiUser(c)
	var regs []models.Registration
	if err := tx.Joins("JOIN event_occurrences ON event_occurrences.id = registrations.occurrence_id").
		Where("registrations.user_id = ? AND registrations.status = ? AND event_occurrences.status = ? AND event_occurrences.starts_at > ?",
			userID, models.RegGoing, models.OccurrenceScheduled, time.Now().UTC()).
		Order("event_occurrences.starts_at, registrations.id").
		Find(&regs).Error; err != nil {
		return nil, err
	}
	var emailed []uint
	seen := map[uint]bool{}
	for _, r := range regs {
		if seen[r.OccurrenceID] {
			continue
		}
		seen[r.OccurrenceID] = true
		o, err := lockOccurrence(tx, r.OccurrenceID)
		if err != nil {
			return nil, err
		}
		var ev models.Event
		if err := tx.First(&ev, o.EventID).Error; err != nil {
			return nil, err
		}
		cap := events.EffectiveCapacity(ev.Capacity, o.CapacityOverride)
		if cap == nil {
			continue // unlimited: every row fits
		}
		used, err := seatsUsed(tx, o.ID)
		if err != nil {
			return nil, err
		}
		seats := 1 + r.GuestCount
		if used+seats <= *cap {
			continue // fits: stays going, counts again once enabled
		}
		if err := tx.Delete(&r).Error; err != nil {
			return nil, err
		}
		if err := audit.Write(tx, audit.Entry{Actor: &actor.ID, Action: audit.RegistrationNotRestored,
			TargetType: "event", TargetID: ev.ID,
			Meta: map[string]any{"userId": userID, "occurrenceId": o.ID, "seats": used, "capacity": *cap},
			IP:   c.ClientIP(),
		}); err != nil {
			return nil, err
		}
		ids, err := notifyRegistrationNotRestored(tx, &ev, o, userID, actor.ID)
		if err != nil {
			return nil, err
		}
		emailed = append(emailed, ids...)
	}
	return emailed, nil
}
