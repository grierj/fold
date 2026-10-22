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
// deliverNotificationEmails. M1.8 slice 2 adds the second seat pool:
// remote registrations count against the remote capacity only.

var (
	// errEventFull: a going request on a full event with the waitlist off.
	errEventFull = errors.New("this event is full")
	// errRemoteNotAvailable: a remote request on an event that does
	// not offer remote attendance.
	errRemoteNotAvailable = errors.New("remote attendance isn't available for this event")
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

// parseRemoteCapacity permits 0 only for a per-date override, to close
// remote seats on that date. For an event, uncheck remote attendance.
func parseRemoteCapacity(raw *json.RawMessage, allowZero bool) (cap *int, set bool, msg string) {
	if raw == nil {
		return nil, false, ""
	}
	if bytes.Equal(bytes.TrimSpace(*raw), []byte("null")) {
		return nil, true, ""
	}
	var n int
	if json.Unmarshal(*raw, &n) != nil || n < 0 || n > 100000 {
		if allowZero {
			return nil, false, "remote seat limit for this date must be a whole number from 0 to 100000, or empty"
		}
		return nil, false, "remote seat limit must be a whole number from 1 to 100000, or empty for no limit"
	}
	if n == 0 && !allowZero {
		return nil, false, "Uncheck remote attendance instead of a 0 remote seat limit"
	}
	return &n, true, ""
}

// capacityEdits holds the parsed M1.5 capacity fields from an event
// body: capacity (nil clears to unlimited), waitlistEnabled, and
// guestsAllowed (0-5, null clears to 0), plus the remote pool's cap
// (M1.8 slice 2; nil clears to unlimited).
type capacityEdits struct {
	cap             *int
	capSet          bool
	guests          int
	guestsSet       bool
	remoteCap       *int
	remoteCapSet    bool
	childcareCap    *int
	childcareCapSet bool
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
	ed.remoteCap, ed.remoteCapSet, msg = parseRemoteCapacity(body.RemoteCapacity, false)
	if msg != "" {
		jsonError(c, http.StatusBadRequest, msg)
		return ed, false
	}
	if !parseChildcareEdits(c, body, &ed) {
		return ed, false
	}
	return ed, true
}

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
		return events.EffectiveRemoteCapacity(ev.RemoteCapacity, o.RemoteCapacityOverride)
	}
	return events.EffectiveCapacity(ev.Capacity, o.CapacityOverride)
}

// intPtrEqual reports whether two optional ints hold the same value.
func intPtrEqual(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// seatsUsedForMode counts seats in one pool on an occurrence: one per
// going registration of that mode from a listed account plus its
// guest_count. Disabled and deleted accounts use no seats in either
// pool (DEC-38). This is the one counted query shape for the lock path
// and the read path.
func seatsUsedForMode(db *gorm.DB, occurrenceID uint, mode string) (int, error) {
	var n int64
	err := db.Model(&models.Registration{}).
		Joins(listedUsers("registrations")).
		Where("registrations.occurrence_id = ? AND registrations.status = ? AND registrations.attendance_mode = ?",
			occurrenceID, models.RegGoing, attendanceModeOf(mode)).
		Select("COALESCE(SUM(1 + registrations.guest_count), 0)").
		Scan(&n).Error
	return int(n), err
}

// seatsUsed counts in-person seats (the pool guests and every pre-remote
// caller mean).
func seatsUsed(db *gorm.DB, occurrenceID uint) (int, error) {
	return seatsUsedForMode(db, occurrenceID, models.AttendanceInPerson)
}

// remoteSeatsUsed counts remote seats.
func remoteSeatsUsed(db *gorm.DB, occurrenceID uint) (int, error) {
	return seatsUsedForMode(db, occurrenceID, models.AttendanceRemote)
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
		Joins(listedUsers("registrations")).
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

// seatsUsedMap counts in-person seats for many occurrences.
func seatsUsedMap(db *gorm.DB, occurrenceIDs []uint) (map[uint]int, error) {
	return seatsUsedMapForMode(db, occurrenceIDs, models.AttendanceInPerson)
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
// an event notice from preloaded facts (issue #76): a registrant who
// cannot see the group gets no group name, place, or group link
// (PR #41). It reads nothing; loadNoticeFacts did the reading once for
// every recipient.
func eventNoticeVars(ev *models.Event, o *models.EventOccurrence, userID uint, facts *noticeFacts) (notify.Vars, string) {
	var g *models.Group
	if ev.GroupID != nil {
		if row := facts.groups[*ev.GroupID]; row != nil && facts.canSeeGroup(userID, row) {
			g = row
		}
	}
	// The place resolves through the date's override, like
	// occurrenceToJSON: a date with its own place keeps it even when
	// the viewer may see the group.
	place := ev.Location
	if o.LocationOverride != nil {
		place = *o.LocationOverride
	}
	vars := notify.Vars{Event: ev.Title, When: formatWhen(o.StartsAt, facts.zone(userID)), Place: place}
	link := fmt.Sprintf("/events/%d", ev.ID)
	if g != nil {
		vars.Event = firstNonEmpty(ev.Title, g.Name) // group-ok: g is set only when facts.canSeeGroup is true
		if o.LocationOverride == nil {
			vars.Place = firstNonEmpty(ev.Location, g.Location) // group-ok: g is set only when facts.canSeeGroup is true
		}
		link = fmt.Sprintf("/groups/%d", g.ID)
	}
	return vars, link
}

// eventNoticeVarsIn is eventNoticeVars for a single recipient: it loads
// that one person's facts in tx, then builds the same Vars.
func eventNoticeVarsIn(tx *gorm.DB, ev *models.Event, o *models.EventOccurrence, userID uint) (notify.Vars, string) {
	var groupIDs []uint
	if ev.GroupID != nil {
		groupIDs = []uint{*ev.GroupID}
	}
	return eventNoticeVars(ev, o, userID, loadNoticeFacts(tx, groupIDs, []uint{userID}))
}

// notifyRegistrationPromoted tells one person they are in off the
// waitlist. The email carries the rotated manage link with the one-click
// "I can't come" cancel; the bell never shows the token.
func notifyRegistrationPromoted(tx *gorm.DB, ev *models.Event, o *models.EventOccurrence,
	userID uint, manageRaw string, actor *uint, app *App) ([]uint, error) {
	vars, link := eventNoticeVarsIn(tx, ev, o, userID)
	vars.ManageURL = app.emailBaseURL() + "/guest/manage/" + manageRaw
	// A promoted person is going now, so a video link set to
	// registered_only may reach them (5.8, M1.8); the email carries it,
	// the bell doesn't.
	if video := videoLinkOf(ev); video != "" &&
		policy.CanSeeVideoLink(videoRevealOf(ev), true, false) {
		vars.VideoURL = video
	}
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
	vars, link := eventNoticeVarsIn(tx, ev, o, to[0])
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
	var groupIDs []uint
	if ev.GroupID != nil {
		groupIDs = []uint{*ev.GroupID}
	}
	facts := loadNoticeFacts(tx, groupIDs, userIDs)
	var ids []uint
	for _, uid := range userIDs {
		vars, link := eventNoticeVars(ev, o, uid, facts)
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
	vars, link := eventNoticeVarsIn(tx, ev, o, userID)
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
	vars, link := eventNoticeVarsIn(tx, ev, o, userID)
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
	wl, err := loadWaitlist(tx, o.ID)
	if err != nil {
		return nil, err
	}
	if len(wl) == 0 {
		return nil, nil
	}
	// One waitlist, two pools: each entry keeps its mode and is filled
	// from its own pool in line order (M1.8 slice 2).
	pools := map[string]events.Pool{}
	for _, mode := range []string{models.AttendanceInPerson, models.AttendanceRemote} {
		used, err := seatsUsedForMode(tx, o.ID, mode)
		if err != nil {
			return nil, err
		}
		pools[mode] = events.Pool{Capacity: capacityForMode(ev, o, mode), Used: used}
	}
	childcare := events.Pool{}
	if ev.ChildcareAvailable && ev.ChildcareCapacity != nil {
		used, err := childcareUsedFor(tx, o.ID, 0)
		if err != nil {
			return nil, err
		}
		childcare = events.Pool{Capacity: ev.ChildcareCapacity, Used: used}
	}
	entries := make([]events.WaitEntry, len(wl))
	for i, r := range wl {
		entries[i] = events.WaitEntry{ID: r.ID, Seats: 1 + r.GuestCount, Mode: attendanceModeOf(r.AttendanceMode), Childcare: r.ChildcareCount}
	}
	if events.InCutoff(now, o.StartsAt) {
		// Nobody is auto-promoted; the leaders or hosts are told only
		// when someone in line could actually take a freed seat.
		if len(events.PickPromotionsByPool(entries, pools, childcare)) > 0 {
			return notifyWaitlistCutoff(tx, ev, o, actor)
		}
		return nil, nil
	}
	picks := events.PickPromotionsByPool(entries, pools, childcare)
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
// occurrence lock. want is going, maybe, or declined. requestedMode is
// the attendanceMode from the body, or "" when absent: an absent mode
// keeps an existing registration's pool and defaults a new one to
// in_person. An explicit remote request is refused when the event does
// not offer remote attendance. Switching mode on a going registration
// is a re-register in the new pool and may waitlist (M1.8 slice 2).
func (a *App) registerLocked(tx *gorm.DB, ev *models.Event, o *models.EventOccurrence,
	userID uint, want string, guestCount int, requestedMode string, childcare childcareRequest, now time.Time) (*registrationOutcome, []uint, error) {
	var existing models.Registration
	found := tx.Where("user_id = ? AND occurrence_id = ?", userID, o.ID).
		First(&existing).RowsAffected == 1
	if found && existing.Status == models.RegRemoved {
		return nil, nil, errRemovedBlocked
	}
	if requestedMode == models.AttendanceRemote && !ev.RemoteAvailable {
		return nil, nil, errRemoteNotAvailable
	}
	mode := models.AttendanceInPerson
	if found {
		mode = attendanceModeOf(existing.AttendanceMode)
	}
	if requestedMode != "" {
		mode = attendanceModeOf(requestedMode)
	}
	oldMode := mode
	if found {
		oldMode = attendanceModeOf(existing.AttendanceMode)
	}
	poolCap := capacityForMode(ev, o, mode)
	used, err := seatsUsedForMode(tx, o.ID, mode)
	if err != nil {
		return nil, nil, err
	}
	fits := func(seats int) bool { return events.Fits(poolCap, used, seats) }
	count, ages := childcare.values(existing)
	childcareFreed := found && existing.Status == models.RegGoing && count < existing.ChildcareCount
	if childcare.ChildcareCount != nil && count > 0 && !ev.ChildcareAvailable {
		return nil, nil, errChildcareUnavailable
	}
	willGo := want == models.RegGoing && ((found && existing.Status == models.RegGoing && oldMode == mode) || fits(1+guestCount))
	if willGo {
		if err := checkChildcare(tx, ev, o.ID, existing.ID, count); err != nil {
			return nil, nil, err
		}
	}
	if found {
		if err := tx.Model(&existing).Updates(map[string]any{"childcare_count": count, "childcare_ages_note": ages}).Error; err != nil {
			return nil, nil, err
		}
		existing.ChildcareCount, existing.ChildcareAgesNote = count, ages
	}

	var emailed []uint
	out := &registrationOutcome{}
	setStatus := func(status string) error {
		updates := map[string]any{"status": status, "guest_count": guestCount, "attendance_mode": mode}
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
		if err := tx.Model(&models.Registration{}).Where("id = ?", existing.ID).Updates(updates).Error; err != nil {
			return err
		}
		existing.AttendanceMode = mode
		return nil
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
						UserID: userID, OccurrenceID: o.ID, ChildcareCount: count, ChildcareAgesNote: ages,
						Status: models.RegGoing, GuestCount: guestCount, Source: models.RegSourceWeb,
						AttendanceMode: mode,
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
					UserID: userID, OccurrenceID: o.ID, ChildcareCount: count, ChildcareAgesNote: ages,
					Status: models.RegWaitlisted, GuestCount: guestCount,
					Source:       models.RegSourceWeb,
					WaitlistedAt: &now, AttendanceMode: mode,
				}
				if err := tx.Create(&existing).Error; err != nil {
					return nil, nil, err
				}
			} else {
				updates := map[string]any{
					"status": models.RegWaitlisted, "guest_count": guestCount, "attendance_mode": mode,
				}
				// A waiter switching pools starts a fresh wait in the new
				// pool, like the going-switch path below (Whittler, #105).
				restamp := existing.WaitlistedAt == nil ||
					(existing.Status == models.RegWaitlisted && oldMode != mode)
				if restamp {
					updates["waitlisted_at"] = now
				}
				if err := tx.Model(&models.Registration{}).Where("id = ?", existing.ID).
					Updates(updates).Error; err != nil {
					return nil, nil, err
				}
				existing.Status = models.RegWaitlisted
				existing.GuestCount = guestCount
				existing.AttendanceMode = mode
				if restamp {
					existing.WaitlistedAt = &now
				}
			}
			pos, err := waitlistPosition(tx, o.ID, existing.ID)
			if err != nil {
				return nil, nil, err
			}
			out.reg, out.position = existing, &pos
			return out, emailed, nil
		case existing.Status == models.RegGoing && oldMode != mode:
			// Switching pools while going is a re-register in the new
			// pool (M1.8 slice 2): the new pool's used seats do not
			// include this party, so it must fit whole. With no waitlist
			// a full new pool refuses and the old seat stays, as with a
			// guest-count increase.
			seats := 1 + guestCount
			if !fits(seats) && !ev.WaitlistEnabled {
				return nil, nil, errEventFull
			}
			if fits(seats) {
				if err := setStatus(models.RegGoing); err != nil {
					return nil, nil, err
				}
				existing.Status = models.RegGoing
				existing.GuestCount = guestCount
				out.reg = existing
				// The party freed a seat in its old pool.
				ids, err := a.promoteLocked(tx, ev, o, now, nil)
				if err != nil {
					return nil, nil, err
				}
				emailed = append(emailed, ids...)
				return out, emailed, nil
			}
			// A fresh wait in the new pool: drop any waitlisted_at kept
			// as history from an earlier promotion so setStatus stamps
			// the new wait's start.
			existing.WaitlistedAt = nil
			if err := setStatus(models.RegWaitlisted); err != nil {
				return nil, nil, err
			}
			existing.Status = models.RegWaitlisted
			existing.GuestCount = guestCount
			existing.WaitlistedAt = &now
			// The party freed a seat in its old pool; it cannot be
			// re-promoted here because its new pool is the full one.
			ids, err := a.promoteLocked(tx, ev, o, now, nil)
			if err != nil {
				return nil, nil, err
			}
			emailed = append(emailed, ids...)
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
			if newSeats < oldSeats || childcareFreed {
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
				UserID: userID, OccurrenceID: o.ID, ChildcareCount: count, ChildcareAgesNote: ages,
				Status: want, GuestCount: guestCount, Source: models.RegSourceWeb,
				AttendanceMode: mode,
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
	// AttendanceMode is the pool the registration landed in (M1.8
	// slice 2): in_person or remote.
	AttendanceMode    string `json:"attendanceMode"`
	ChildcareCount    int    `json:"childcareCount"`
	ChildcareAgesNote string `json:"childcareAgesNote"`
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
		// AttendanceMode is in_person or remote (M1.8 slice 2); absent
		// keeps an existing registration's pool (in_person for a new one).
		AttendanceMode *string `json:"attendanceMode"`
		childcareRequest
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "status and date are required")
		return
	}
	if !body.childcareRequest.validate(c, &ev) {
		return
	}
	requestedMode := ""
	if body.AttendanceMode != nil {
		requestedMode = strings.TrimSpace(*body.AttendanceMode)
		switch requestedMode {
		case models.AttendanceInPerson, models.AttendanceRemote:
		default:
			jsonError(c, http.StatusBadRequest, "attendanceMode must be in_person or remote")
			return
		}
		if requestedMode == models.AttendanceRemote && !ev.RemoteAvailable {
			jsonError(c, http.StatusBadRequest, "remote attendance isn't available for this event")
			return
		}
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
		out, ids, err := a.registerLocked(tx, &ev, locked, u.ID, body.Status, guestCount, requestedMode, body.childcareRequest, time.Now().UTC())
		if err != nil {
			return err
		}
		outcome, emailed = out, ids
		return nil
	})
	switch {
	case errors.Is(err, errChildcareFull):
		jsonError(c, http.StatusConflict, errChildcareFull.Error())
		return
	case errors.Is(err, errChildcareUnavailable):
		jsonError(c, http.StatusBadRequest, errChildcareUnavailable.Error())
		return
	case errors.Is(err, errRemoteNotAvailable):
		jsonError(c, http.StatusBadRequest, "remote attendance isn't available for this event")
		return
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
		AttendanceMode: attendanceModeOf(outcome.reg.AttendanceMode),
		ChildcareCount: outcome.reg.ChildcareCount, ChildcareAgesNote: outcome.reg.ChildcareAgesNote,
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
		// DEC-80 retries the waitlist when pruning frees childcare.
		// Seat-only pruning keeps the seat for a specific add or hand promotion.
		if r.Status == models.RegGoing && r.ChildcareCount > 0 && ev.ChildcareAvailable {
			ids, err := a.promoteLocked(tx, ev, locked, time.Now().UTC(), &me.ID)
			if err != nil {
				return err
			}
			emailed = append(emailed, ids...)
		}
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
		// A waitlisted entry promotes into its own pool (M1.8 slice 2).
		cap := capacityForMode(ev, locked, r.AttendanceMode)
		used, err := seatsUsedForMode(tx, locked.ID, r.AttendanceMode)
		if err != nil {
			return err
		}
		seats := 1 + r.GuestCount
		if !events.Fits(cap, used, seats) {
			return errEventFull
		}
		// Automatic picks are childcare-checked by PickPromotionsByPool; a
		// manual promote checks here (DEC-80).
		if err := checkChildcare(tx, ev, locked.ID, r.ID, r.ChildcareCount); err != nil {
			return err
		}
		if err := audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.RegistrationPromoted,
			TargetType: "event", TargetID: ev.ID,
			Meta: map[string]any{"userId": r.UserID, "occurrenceId": locked.ID},
			IP:   c.ClientIP(),
		}); err != nil {
			return err
		}
		ids, err := a.promoteRows(tx, ev, locked, []models.Registration{r}, time.Now().UTC(), &me.ID)
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
	case errors.Is(err, errChildcareFull):
		jsonError(c, http.StatusConflict, errChildcareFull.Error())
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
		// AttendanceMode seats the person in that pool; absent keeps an
		// existing registration's mode (in_person for a new one).
		AttendanceMode *string `json:"attendanceMode"`
		childcareRequest
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Date == "" || (body.UserID == 0 && body.Email == "") {
		jsonError(c, http.StatusBadRequest, "userId or email, and date, are required")
		return
	}
	if !body.childcareRequest.validate(c, ev) {
		return
	}
	addMode := ""
	if body.AttendanceMode != nil {
		addMode = strings.TrimSpace(*body.AttendanceMode)
		switch addMode {
		case models.AttendanceInPerson, models.AttendanceRemote:
		default:
			jsonError(c, http.StatusBadRequest, "attendanceMode must be in_person or remote")
			return
		}
		if addMode == models.AttendanceRemote && !ev.RemoteAvailable {
			jsonError(c, http.StatusBadRequest, "remote attendance isn't available for this event")
			return
		}
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
		var r models.Registration
		found := tx.Where("user_id = ? AND occurrence_id = ?", target.ID, locked.ID).
			First(&r).RowsAffected == 1
		count, ages := body.childcareRequest.values(r)
		if err := checkChildcare(tx, ev, locked.ID, r.ID, count); err != nil {
			return err
		}
		mode := models.AttendanceInPerson
		if found {
			mode = attendanceModeOf(r.AttendanceMode)
		}
		if addMode != "" {
			mode = addMode
		}
		// The add seats the person in their pool; over-capacity is
		// judged against that pool only (M1.8 slice 2).
		cap := capacityForMode(ev, locked, mode)
		used, err := seatsUsedForMode(tx, locked.ID, mode)
		if err != nil {
			return err
		}
		if found && r.Status == models.RegGoing && attendanceModeOf(r.AttendanceMode) == mode {
			used -= 1 + r.GuestCount
		}
		seats := 1 + guestCount
		over := !events.Fits(cap, used, seats)
		if over && !overCap {
			return errEventFull
		}
		updates := map[string]any{
			"status": models.RegGoing, "guest_count": guestCount, "attendance_mode": mode,
			"removed_by": nil, "removal_note": "", "waitlisted_at": nil, "promoted_at": nil,
			"childcare_count": count, "childcare_ages_note": ages,
		}
		if found {
			if err := tx.Model(&models.Registration{}).Where("id = ?", r.ID).Updates(updates).Error; err != nil {
				return err
			}
		} else {
			r = models.Registration{
				UserID: target.ID, OccurrenceID: locked.ID, ChildcareCount: count, ChildcareAgesNote: ages,
				Status: models.RegGoing, GuestCount: guestCount, Source: models.RegSourceAdmin,
				AttendanceMode: mode,
			}
			if err := tx.Create(&r).Error; err != nil {
				return err
			}
		}
		action := audit.RegistrationAdded
		meta := map[string]any{"userId": target.ID, "occurrenceId": locked.ID, "attendanceMode": mode}
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
		if found && r.Status == models.RegGoing && count < r.ChildcareCount {
			ids, err := a.promoteLocked(tx, ev, locked, time.Now().UTC(), &me.ID)
			if err != nil {
				return err
			}
			emailed = append(emailed, ids...)
		}
		return nil
	})
	switch {
	case errors.Is(err, errChildcareFull):
		jsonError(c, http.StatusConflict, errChildcareFull.Error())
		return
	case errors.Is(err, errChildcareUnavailable):
		jsonError(c, http.StatusBadRequest, errChildcareUnavailable.Error())
		return
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
		// Left out when the restore would exceed its own mode's
		// capacity (DEC-47, M1.8 slice 2): a remote row is judged
		// against the remote pool only. The account is still disabled
		// here, so its seats are not in used yet.
		mode := attendanceModeOf(r.AttendanceMode)
		cap := capacityForMode(&ev, o, mode)

		used, err := seatsUsedForMode(tx, o.ID, mode)
		if err != nil {
			return nil, err
		}
		seats := 1 + r.GuestCount
		childcareErr := checkChildcare(tx, &ev, o.ID, r.ID, r.ChildcareCount)
		if childcareErr != nil && !errors.Is(childcareErr, errChildcareFull) {
			return nil, childcareErr
		}
		if events.Fits(cap, used, seats) && childcareErr == nil {
			continue // fits: stays going, counts again once enabled
		}
		childcareUsed, err := childcareUsedFor(tx, o.ID, r.ID)
		if err != nil {
			return nil, err
		}
		if err := tx.Delete(&r).Error; err != nil {
			return nil, err
		}
		if err := audit.Write(tx, audit.Entry{Actor: &actor.ID, Action: audit.RegistrationNotRestored,
			TargetType: "event", TargetID: ev.ID,
			Meta: map[string]any{"userId": userID, "occurrenceId": o.ID, "seats": used, "capacity": cap, "attendanceMode": mode, "childcareUsed": childcareUsed, "childcareCapacity": ev.ChildcareCapacity},
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
