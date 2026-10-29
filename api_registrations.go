package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

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
	errOccurrenceNotFound  = errors.New("occurrence not found")
	errOccurrenceGateRetry = errors.New("retry invitation mutation under contact gate")
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
		Joins(listedRegistrations()).
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
	err := tx.Joins(listedRegistrations()).
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
func effectiveOccurrenceTitle(ev *models.Event, o *models.EventOccurrence) string {
	if o.TitleOverride != nil {
		return *o.TitleOverride
	}
	return ev.Title
}

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
	vars := notify.Vars{Event: effectiveOccurrenceTitle(ev, o), When: formatWhen(o.StartsAt, facts.zone(userID)), Place: place}
	link := fmt.Sprintf("/events/%d", ev.ID)
	if g != nil {
		vars.Event = firstNonEmpty(effectiveOccurrenceTitle(ev, o), g.Name) // group-ok: g is set only when facts.canSeeGroup is true
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
	reg models.Registration, manageRaw string, actor *uint, app *App) ([]uint, error) {
	userID := registrationHolder(reg)
	accesses, err := occurrenceNoticeAccesses(tx, []uint{o.ID})
	if err != nil {
		return nil, err
	}
	if invitation, ok := accesses[o.ID][userID]; ok && reg.UserID != nil && invitation.RegistrationID == reg.ID {
		return sendOccurrenceNotice(tx, invitation, o, notify.OccurrenceChanged, "promoted:"+strconv.FormatUint(uint64(invitation.ID), 10)+":"+time.Now().UTC().Format(time.RFC3339Nano))
	}
	vars, link := eventNoticeVarsIn(tx, ev, o, userID)
	if reg.HouseholdPersonID != nil {
		vars.Person = householdPersonName(tx, *reg.HouseholdPersonID)
	}
	if actor != nil && *actor == userID {
		actor = nil
	}
	vars.ManageURL = app.emailBaseURL() + "/guest/manage/" + manageRaw
	// A promoted person is going now, so a video link set to
	// registered_only may reach them (5.8, M1.8); the email carries it,
	// the bell doesn't.
	if video := videoLinkOf(ev); video != "" &&
		policy.CanSeeVideoLink(videoRevealOf(ev), true, false) && (reg.HouseholdPersonID == nil || reg.AttendanceMode == models.AttendanceRemote) {
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
	if err := tx.Table("registrations").Distinct("users.id").
		Joins(listedRegistrations()).
		Where("registrations.occurrence_id = ? AND registrations.status IN ?",
			o.ID, []string{models.RegGoing, models.RegWaitlisted}).
		Order("users.id").Pluck("users.id", &userIDs).Error; err != nil {
		return nil, err
	}
	var groupIDs []uint
	if ev.GroupID != nil {
		groupIDs = []uint{*ev.GroupID}
	}
	facts := loadNoticeFacts(tx, groupIDs, userIDs)
	accesses, err := occurrenceNoticeAccesses(tx, []uint{o.ID})
	if err != nil {
		return nil, err
	}
	var ids []uint
	for _, uid := range userIDs {
		if invitation, ok := accesses[o.ID][uid]; ok {
			sent, err := sendOccurrenceNotice(tx, invitation, o, notify.OccurrenceChanged, "")
			if err != nil {
				return nil, err
			}
			ids = append(ids, sent...)
			continue
		}
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
	reg models.Registration, note string, actor uint) ([]uint, error) {
	userID := registrationHolder(reg)
	u := userIn(tx, userID)
	if u == nil || u.Status == models.UserDisabled {
		return nil, nil
	}
	vars, link := eventNoticeVarsIn(tx, ev, o, userID)
	if reg.HouseholdPersonID != nil {
		vars.Person = householdPersonName(tx, *reg.HouseholdPersonID)
	}
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
func notifyRegistrationNotRestored(tx *gorm.DB, ev *models.Event, o *models.EventOccurrence, reg models.Registration, actor *uint) ([]uint, error) {
	userID := registrationHolder(reg)
	vars, link := eventNoticeVarsIn(tx, ev, o, userID)
	if reg.HouseholdPersonID != nil {
		vars.Person = householdPersonName(tx, *reg.HouseholdPersonID)
	}
	end := o.EndsAt
	if end.IsZero() {
		end = o.StartsAt
	}
	return notify.Send(tx, notify.Msg{
		Kind: notify.RegistrationNotRestored, To: []uint{userID}, Actor: actor,
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
	facts := a.loadSubjectFacts(tx, wl, nil)
	access := map[uint]bool{}
	facts.clock = func() time.Time { return a.householdClock(now) }
	entries := make([]events.WaitEntry, 0, len(wl))
	for _, r := range wl {
		if !facts.eligible(a, tx, r, ev, access) {
			continue
		}
		entries = append(entries, events.WaitEntry{ID: r.ID, Seats: 1 + r.GuestCount, Mode: attendanceModeOf(r.AttendanceMode), Childcare: effectiveChildcare(ev, r)})
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
		next := r
		next.Status = models.RegGoing
		actorID := uint(0)
		if actor != nil {
			actorID = *actor
		}
		if err := auditRegistration(tx, actorID, audit.RegistrationChanged, ev.ID, r, next, ""); err != nil {
			return nil, err
		}
		ids, err := notifyRegistrationPromoted(tx, ev, o, r, raw, actor, a)
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

// registrationChange is prepared before the batch writes anything.
type registrationChange struct {
	old         models.Registration
	next        models.Registration
	found       bool
	auditAction string
}

func prepareRegistration(old models.Registration, o *models.EventOccurrence, subject registrationSubject, want string, guests int, mode string, care childcareRequest, namedCare *bool, now time.Time) (registrationChange, error) {
	ch := registrationChange{old: old, found: old.ID != 0}
	if ch.found && ch.old.Status == models.RegRemoved {
		return ch, errRemovedBlocked
	}
	ch.next = ch.old
	if !ch.found {
		ch.next = subject.row(o.ID)
		ch.next.Source = models.RegSourceWeb
	}
	ch.next.Status, ch.next.GuestCount = want, guests
	if mode != "" {
		ch.next.AttendanceMode = mode
	} else {
		ch.next.AttendanceMode = attendanceModeOf(ch.next.AttendanceMode)
	}
	ch.next.ChildcareCount, ch.next.ChildcareAgesNote = care.values(ch.old)
	if namedCare != nil {
		ch.next.ChildcareRequested = *namedCare
	}
	if ch.next.HouseholdPersonID != nil && ch.next.ChildcareRequested && ch.next.AttendanceMode == models.AttendanceRemote {
		return ch, householdRegistrationError{code: "household_remote_childcare", personID: *ch.next.HouseholdPersonID}
	}
	if want == models.RegGoing && ch.old.Status == models.RegWaitlisted && ch.old.AttendanceMode == ch.next.AttendanceMode {
		ch.next.Status = models.RegWaitlisted
	}
	if ch.next.Status == models.RegWaitlisted && (ch.old.WaitlistedAt == nil || ch.old.AttendanceMode != ch.next.AttendanceMode) {
		ch.next.WaitlistedAt = &now
	}
	return ch, nil
}

func saveRegistration(tx *gorm.DB, r *models.Registration) error {
	if r.Status != models.RegWaitlisted {
		r.WaitlistedAt, r.PromotedAt = nil, nil
		if r.Status != models.RegGoing {
			r.ManageTokenHash = ""
		}
	}
	return tx.Omit("User", "Occurrence").Save(r).Error
}

// registerLocked allocates one final transition. Batch departures and
// promotion scheduling belong to the caller, never to this per-row function.
func (a *App) registerLocked(tx *gorm.DB, ev *models.Event, o *models.EventOccurrence, ch *registrationChange, now time.Time) error {
	r := &ch.next
	if r.Status != models.RegDeclined && r.AttendanceMode == models.AttendanceRemote && !ev.RemoteAvailable {
		return errRemoteNotAvailable
	}
	if r.Status == models.RegGoing {
		used, err := seatsUsedForMode(tx, o.ID, r.AttendanceMode)
		if err != nil {
			return err
		}
		var current models.Registration
		if r.ID != 0 && tx.First(&current, r.ID).Error == nil && current.Status == models.RegGoing && current.AttendanceMode == r.AttendanceMode {
			used -= 1 + current.GuestCount
		}
		sameGoing := current.Status == models.RegGoing && current.AttendanceMode == r.AttendanceMode
		if !(sameGoing && r.GuestCount <= current.GuestCount) && !events.Fits(capacityForMode(ev, o, r.AttendanceMode), used, 1+r.GuestCount) {
			if ch.old.Status == models.RegGoing && ch.old.AttendanceMode == r.AttendanceMode {
				return errEventFull
			}
			if !ev.WaitlistEnabled {
				return errEventFull
			}
			r.Status = models.RegWaitlisted
			if r.WaitlistedAt == nil || ch.old.AttendanceMode != r.AttendanceMode {
				r.WaitlistedAt = &now
			}
		} else if !(sameGoing && effectiveChildcare(ev, *r) <= effectiveChildcare(ev, current)) {
			if err := checkChildcare(tx, ev, o.ID, r.ID, effectiveChildcare(ev, *r)); err != nil {
				return err
			}
		}
	}
	return saveRegistration(tx, r)
}

// applyRegistrationBatch releases resources before filling new requests so
// existing waiters can use those releases first. Every write rolls back together.
func (a *App) applyRegistrationBatch(tx *gorm.DB, ev *models.Event, o *models.EventOccurrence, changes []registrationChange, now time.Time, actor uint, ip string) ([]uint, error) {
	released, editedWaiter := false, false
	for i := range changes {
		ch := &changes[i]
		if ch.old.Status == models.RegWaitlisted && ch.next.Status == models.RegWaitlisted {
			if err := saveRegistration(tx, &ch.next); err != nil {
				return nil, err
			}
			editedWaiter = true
		}
		if ch.old.Status == models.RegWaitlisted && ch.next.Status != models.RegWaitlisted {
			interim := ch.next
			if interim.Status == models.RegGoing {
				interim.Status = models.RegDeclined
			}
			if err := saveRegistration(tx, &interim); err != nil {
				return nil, err
			}
		}
		if ch.old.Status != models.RegGoing {
			continue
		}
		interim := ch.old
		switch {
		case ch.next.Status != models.RegGoing || ch.next.AttendanceMode != ch.old.AttendanceMode:
			interim.Status = models.RegDeclined
			released = true
		default:
			if ch.next.GuestCount < interim.GuestCount {
				interim.GuestCount = ch.next.GuestCount
				released = true
			}
			if effectiveChildcare(ev, ch.next) < effectiveChildcare(ev, interim) {
				interim.ChildcareCount, interim.ChildcareRequested = ch.next.ChildcareCount, ch.next.ChildcareRequested
				released = true
			}
		}
		if err := saveRegistration(tx, &interim); err != nil {
			return nil, err
		}
	}
	var ids []uint
	if released || editedWaiter {
		sent, err := a.promoteLocked(tx, ev, o, now, nil)
		if err != nil {
			return nil, err
		}
		ids = append(ids, sent...)
	}
	for i := range changes {
		ch := &changes[i]
		if ch.old.Status == models.RegWaitlisted && ch.next.Status == models.RegWaitlisted {
			if err := tx.First(&ch.next, ch.next.ID).Error; err != nil {
				return nil, err
			}
		} else if err := a.registerLocked(tx, ev, o, ch, now); err != nil {
			return nil, err
		}
		action := ch.auditAction
		if action == "" {
			action = audit.RegistrationChanged
		}
		if err := auditRegistration(tx, actor, action, ev.ID, ch.old, ch.next, ip); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

func auditRegistration(tx *gorm.DB, actor uint, action string, eventID uint, old, next models.Registration, ip string) error {
	meta := registrationMeta(next)
	meta["oldStatus"], meta["newStatus"] = old.Status, next.Status
	meta["oldAttendanceMode"], meta["attendanceMode"] = old.AttendanceMode, next.AttendanceMode
	meta["oldGuestCount"], meta["guestCount"] = old.GuestCount, next.GuestCount
	meta["oldChildcareCount"], meta["childcareCount"] = old.ChildcareCount, next.ChildcareCount
	meta["oldChildcareRequested"], meta["childcareRequested"] = old.ChildcareRequested, next.ChildcareRequested
	var actorID *uint
	if actor != 0 {
		actorID = &actor
	}
	return audit.Write(tx, audit.Entry{Actor: actorID, Action: action, TargetType: "event", TargetID: eventID, Meta: meta, IP: ip})
}

func registrationMeta(r models.Registration) map[string]any {
	typ := "user"
	id := r.UserID
	if r.HouseholdPersonID != nil {
		typ, id = "household_person", r.HouseholdPersonID
	}
	return map[string]any{"registrationId": r.ID, "subjectType": typ, "subjectId": id, "userId": r.UserID, "personId": r.HouseholdPersonID, "registeredBy": r.RegisteredBy, "occurrenceId": r.OccurrenceID, "attendanceMode": attendanceModeOf(r.AttendanceMode), "status": r.Status, "guestCount": r.GuestCount, "childcareCount": r.ChildcareCount, "childcareRequested": r.ChildcareRequested}
}

// registrationJSON is the register endpoint's registration object
// (M1.5): the final status, the guest count, and the waitlist position
// (null unless the person is waitlisted).
type registrationJSON struct {
	RegistrationID   uint   `json:"registrationId"`
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

type householdRegistrationJSON struct {
	RegistrationID     uint   `json:"registrationId"`
	PersonID           uint   `json:"personId"`
	RegisteredBy       uint   `json:"registeredBy"`
	Status             string `json:"status"`
	AttendanceMode     string `json:"attendanceMode"`
	WaitlistPosition   *int   `json:"waitlistPosition"`
	ChildcareRequested bool   `json:"childcareRequested"`
}

type registrationBatchJSON struct {
	Registration *registrationJSON           `json:"registration"`
	People       []householdRegistrationJSON `json:"people"`
}

type householdAnswer struct {
	PersonID           uint   `json:"personId"`
	Status             string `json:"status"`
	AttendanceMode     string `json:"attendanceMode"`
	ChildcareRequested *bool  `json:"childcareRequested"`
}

type householdRegistrationError struct {
	code     string
	personID uint
}

func (e householdRegistrationError) Error() string { return e.code }

func registrationError(c *gin.Context, err error) {
	var own householdRegistrationError
	switch {
	case errors.Is(err, errConversionUnavailable):
		conversionError(c, err)
	case errors.As(err, &own):
		body := errorBody(http.StatusConflict, householdRegistrationMessage(own.code))
		body["code"] = own.code
		if own.personID != 0 {
			body["personId"] = own.personID
		}
		c.JSON(http.StatusConflict, body)
	case errors.Is(err, errRegNotFound), errors.Is(err, gorm.ErrRecordNotFound):
		jsonError(c, 404, "household person not found")
	case errors.Is(err, errOccurrenceNotFound):
		jsonError(c, 404, "that date is not on this event")
	case errors.Is(err, errChildcareFull):
		jsonError(c, 409, errChildcareFull.Error())
	case errors.Is(err, errChildcareUnavailable):
		jsonError(c, 400, errChildcareUnavailable.Error())
	case errors.Is(err, errRemoteNotAvailable):
		jsonError(c, 400, errRemoteNotAvailable.Error())
	case errors.Is(err, errEventFull):
		jsonError(c, 409, "This event is full")
	case errors.Is(err, errRemovedBlocked):
		jsonError(c, 409, errRemovedBlocked.Error())
	default:
		jsonError(c, 500, "could not save registration")
	}
}

func validAnswer(status string) bool {
	return status == models.RegGoing || status == models.RegMaybe || status == models.RegDeclined
}
func validMode(mode string) bool {
	return mode == "" || mode == models.AttendanceInPerson || mode == models.AttendanceRemote
}

type registrationBatchInput struct {
	Date              string            `json:"date"`
	Status            string            `json:"status"`
	GuestCount        *int              `json:"guestCount"`
	AttendanceMode    *string           `json:"attendanceMode"`
	ChildcareCount    *int              `json:"childcareCount"`
	ChildcareAgesNote *string           `json:"childcareAgesNote"`
	People            []householdAnswer `json:"people"`
}

func (a *App) apiRegister(c *gin.Context) {
	var evRow models.Event
	id, parseErr := strconv.ParseUint(c.Param("id"), 10, 64)
	if parseErr != nil || id == 0 || a.DB.First(&evRow, id).Error != nil {
		jsonError(c, 404, "event not found")
		return
	}
	ev := &evRow
	u := apiUser(c)
	var body registrationBatchInput
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil || (body.Status == "" && len(body.People) == 0) || (body.Status != "" && !validAnswer(body.Status)) {
		jsonError(c, 400, "status and date are required")
		return
	}
	if body.Status == "" && (body.GuestCount != nil || body.AttendanceMode != nil || body.ChildcareCount != nil || body.ChildcareAgesNote != nil) {
		jsonError(c, 400, "holder fields need status")
		return
	}
	care := childcareRequest{ChildcareCount: body.ChildcareCount, ChildcareAgesNote: body.ChildcareAgesNote}
	if !care.validate(c, ev) {
		return
	}
	mode := ""
	if body.AttendanceMode != nil {
		mode = *body.AttendanceMode
	}
	if !validMode(mode) {
		jsonError(c, 400, "attendanceMode must be in_person or remote")
		return
	}
	guests := 0
	if body.GuestCount != nil {
		guests = *body.GuestCount
	}
	if guests < 0 || guests > ev.GuestsAllowed {
		jsonErrorCode(c, 400, "too_many_guests", "too many guests")
		return
	}
	seen := map[uint]bool{}
	for _, answer := range body.People {
		if answer.PersonID == 0 || seen[answer.PersonID] || !validAnswer(answer.Status) || !validMode(answer.AttendanceMode) {
			jsonError(c, 400, "invalid household answer")
			return
		}
		seen[answer.PersonID] = true
	}
	// Household facts are read only when the answer names people, so a
	// solo RSVP costs what it did before slice 2 (Whittler, PR 110).
	var acct models.HouseholdAccount
	if len(body.People) > 0 {
		var found bool
		acct, found = householdAccountIn(a.DB, u.ID)
		if !policy.CanManageHousehold(asPerson(u), householdRelOf(acct, found)) {
			jsonError(c, 403, "forbidden")
			return
		}
	}
	var o models.EventOccurrence
	if a.DB.Where("event_id = ? AND original_date = ? AND status = ?", ev.ID, body.Date, models.OccurrenceScheduled).First(&o).Error != nil {
		jsonError(c, 404, "that date is not on this event")
		return
	}
	if !a.canRegisterIn(a.DB, u, dateEvent(ev, &o)) {
		jsonError(c, 404, "event not found")
		return
	}
	// Discover responsible accounts before acquiring any household/date lock.
	// Reloaded eligibility below handles a concurrent ownership/lifecycle edit.
	holderIDs := []uint{u.ID}
	if len(body.People) > 0 {
		personIDs := []uint{}
		for _, answer := range body.People {
			personIDs = append(personIDs, answer.PersonID)
		}
		var existingHolders []uint
		a.DB.Model(&models.Registration{}).Where("occurrence_id = ? AND household_person_id IN ? AND registered_by IS NOT NULL", o.ID, personIDs).Pluck("registered_by", &existingHolders)
		holderIDs = append(holderIDs, existingHolders...)
	}
	sort.Slice(holderIDs, func(i, j int) bool { return holderIDs[i] < holderIDs[j] })
	now := time.Now().UTC()
	response := registrationBatchJSON{People: []householdRegistrationJSON{}}
	var emailed []uint
	gated := false
	mutate := func(tx *gorm.DB) error {
		if gated {
			if err := lockContacts(tx); err != nil {
				return err
			}
		}
		var holder models.User
		for i, id := range holderIDs {
			if i > 0 && holderIDs[i-1] == id {
				continue
			}
			var lockedHolder models.User
			if err := tx.Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).First(&lockedHolder, id).Error; err != nil {
				return err
			}
			if id == u.ID {
				holder = lockedHolder
			}
		}
		if holder.Status != models.UserActive {
			return householdRegistrationError{code: "household_registration_holder_unavailable"}
		}
		if len(body.People) > 0 {
			if err := lockHousehold(tx, acct.HouseholdID); err != nil {
				return err
			}
		}
		locked, err := lockOccurrence(tx, o.ID)
		if err != nil {
			return err
		}
		if locked.Status != models.OccurrenceScheduled {
			return errOccurrenceNotFound
		}
		// A gated retry may have waited behind an invitation ending or a
		// visibility/session change. Never fall back to preflight authority.
		if gated && (!holder.CanSignIn() || holder.SessionVersion != u.SessionVersion) {
			return errOccurrenceNotFound
		}
		var today time.Time
		if err := tx.First(ev, ev.ID).Error; err != nil {
			return err
		}
		if len(body.People) > 0 {
			if !a.canRegisterIn(tx, &holder, dateEvent(ev, locked)) {
				return errOccurrenceNotFound
			}
			today = a.householdClock(now)
		}
		personIDs := make([]uint, 0, len(body.People))
		for _, answer := range body.People {
			personIDs = append(personIDs, answer.PersonID)
		}
		var existingRows []struct {
			models.Registration
			Invitation bool
		}
		if err := tx.Model(&models.Registration{}).Select("registrations.*, EXISTS (SELECT 1 FROM occurrence_accesses oa WHERE oa.registration_id = registrations.id AND oa.status = 'active') AS invitation").Where("occurrence_id = ? AND (user_id = ? OR household_person_id IN ?)", o.ID, holder.ID, personIDs).Find(&existingRows).Error; err != nil {
			return err
		}
		var ownRow models.Registration
		personRows := map[uint]models.Registration{}
		hasInvitation := false
		for _, row := range existingRows {
			r := row.Registration
			if row.Invitation && r.UserID != nil && *r.UserID == holder.ID {
				hasInvitation = true
			}
			if r.UserID != nil && *r.UserID == holder.ID {
				ownRow = r
			} else if r.HouseholdPersonID != nil {
				personRows[*r.HouseholdPersonID] = r
			}
		}
		if hasInvitation && !gated {
			return errOccurrenceGateRetry
		}
		var invitation models.OccurrenceAccess
		if hasInvitation {
			if err := tx.Where("registration_id = ? AND status = ?", ownRow.ID, policy.OccurrenceAccessActive).First(&invitation).Error; err != nil {
				return err
			}
			live, err := liveOccurrenceInvitationIn(tx, invitation, time.Now().UTC())
			if err != nil {
				return err
			}
			hasInvitation = live
		}
		if !hasInvitation && !a.canRegisterIn(tx, &holder, dateEvent(ev, locked)) {
			return errOccurrenceNotFound
		}
		changes := []registrationChange{}
		if body.Status != "" {
			ch, err := prepareRegistration(ownRow, locked, userSubject(holder.ID), body.Status, guests, mode, care, nil, now)
			if err != nil {
				return err
			}
			changes = append(changes, ch)
		}
		var people []models.HouseholdPerson
		if len(personIDs) > 0 {
			if err := tx.Where("id IN ? AND household_id = ? AND converted_user_id IS NULL", personIDs, acct.HouseholdID).Find(&people).Error; err != nil {
				return err
			}
		}
		byID := map[uint]*models.HouseholdPerson{}
		for i := range people {
			byID[people[i].ID] = &people[i]
		}
		links := guardianLinksForPeople(tx, personIDs)
		for _, answer := range body.People {
			person := byID[answer.PersonID]
			if person == nil {
				return errRegNotFound
			}
			subject, err := householdSubjectFrom(person, acct)
			if err != nil {
				return err
			}
			ch, err := prepareRegistration(personRows[person.ID], locked, subject, answer.Status, 0, answer.AttendanceMode, childcareRequest{}, answer.ChildcareRequested, now)
			if err != nil {
				return err
			}
			// Existing answers retain their original responsible holder.
			if problem := policy.HouseholdRegistrationProblem(householdFacts(person, links[person.ID], today)); problem != "" {
				return householdRegistrationError{code: problem, personID: person.ID}
			}
			if answer.ChildcareRequested != nil && *answer.ChildcareRequested && !ev.ChildcareAvailable && !ch.old.ChildcareRequested {
				return errChildcareUnavailable
			}
			if ch.next.ChildcareRequested && person.Kind != models.HouseholdChild {
				return householdRegistrationError{code: "household_age_mismatch", personID: person.ID}
			}
			changes = append(changes, ch)
		}
		candidates := []models.Registration{}
		for _, ch := range changes {
			candidates = append(candidates, ch.next)
		}
		var facts subjectFacts
		if len(body.People) > 0 {
			facts = a.loadSubjectFacts(tx, candidates, &holder)
			facts.today = today
		}
		access := map[uint]bool{}
		for _, r := range candidates {
			if r.HouseholdPersonID != nil && !facts.eligible(a, tx, r, ev, access) {
				return householdRegistrationError{code: "household_registration_holder_unavailable", personID: *r.HouseholdPersonID}
			}
		}
		emailed, err = a.applyRegistrationBatch(tx, ev, locked, changes, now, holder.ID, c.ClientIP())
		if err != nil {
			return err
		}
		if hasInvitation && body.Status == models.RegDeclined {
			ids, err := endOccurrenceAccessIn(tx, &invitation, policy.OccurrenceDeclined, &holder.ID, time.Now().UTC(), c.ClientIP())
			if err != nil {
				return err
			}
			emailed = append(emailed, ids...)
		}
		for _, ch := range changes {
			var r models.Registration
			if err := tx.First(&r, ch.next.ID).Error; err != nil {
				return err
			}
			var pos *int
			if r.Status == models.RegWaitlisted {
				n, err := waitlistPosition(tx, o.ID, r.ID)
				if err != nil {
					return err
				}
				pos = &n
			}
			if r.UserID != nil {
				response.Registration = &registrationJSON{RegistrationID: r.ID, UserID: *r.UserID, EventID: ev.ID, OccurrenceID: o.ID, Date: body.Date, Status: r.Status, GuestCount: r.GuestCount, WaitlistPosition: pos, AttendanceMode: r.AttendanceMode, ChildcareCount: r.ChildcareCount, ChildcareAgesNote: r.ChildcareAgesNote}
			} else {
				response.People = append(response.People, householdRegistrationJSON{RegistrationID: r.ID, PersonID: *r.HouseholdPersonID, RegisteredBy: *r.RegisteredBy, Status: r.Status, AttendanceMode: r.AttendanceMode, WaitlistPosition: pos, ChildcareRequested: r.ChildcareRequested})
			}
		}
		return nil
	}
	err := a.DB.Transaction(mutate)
	if errors.Is(err, errOccurrenceGateRetry) {
		gated = true
		err = a.DB.Transaction(mutate)
	}
	if err != nil {
		registrationError(c, err)
		return
	}
	a.deliverNotificationEmails(emailed)
	c.JSON(http.StatusOK, response)
}

func (a *App) canRegisterIn(tx *gorm.DB, u *models.User, ev *models.Event) bool {
	gp := policy.Group{}
	rel := policy.Rel{}
	if ev.GroupID != nil {
		var group models.Group
		if tx.First(&group, *ev.GroupID).Error != nil {
			return false
		}
		gp = asGroup(&group)
		// Host facts are irrelevant to visibility. An active public/private group
		// needs no membership read; invitation-only or inactive contexts do.
		if u != nil && !policy.IsAdmin(asPerson(u)) && (ev.Visibility == models.VisibilityInviteOnly || group.Status != models.GroupActive) {
			rel = relIn(tx, u.ID, group.ID)
		}
	}
	view := viewOf(ev, false)
	if u != nil && u.Status == models.UserGuest {
		return policy.CanRegisterGuest(view, gp)
	}
	return policy.CanRegisterEvent(asPerson(u), view, gp, rel)
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
		RegistrationID uint   `json:"registrationId"`
		Date           string `json:"date"`
		Note           string `json:"note"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.RegistrationID == 0 || body.Date == "" {
		jsonError(c, http.StatusBadRequest, "registrationId and date are required")
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
	err := a.contactMutation(contactChange{EventID: ev.ID}, func(tx *gorm.DB) error {
		actor, err := peopleAddActor(tx, me)
		if err != nil {
			return err
		}
		if err := tx.First(ev, ev.ID).Error; err != nil {
			return err
		}
		facts := a.factsForIn(tx, actor, ev)
		if !policy.CanManageEvent(asPerson(actor), facts.view, facts.rel) {
			return errPeopleAddForbidden
		}
		me = actor
		if err := lockRegistrationHolder(tx, body.RegistrationID, o.ID); err != nil {
			return err
		}
		locked, err := lockOccurrence(tx, o.ID)
		if err != nil {
			return err
		}
		var r models.Registration
		if err := tx.Where("id = ? AND occurrence_id = ? AND status NOT IN ?",
			body.RegistrationID, locked.ID, []string{models.RegRemoved, models.RegCancelled}).First(&r).Error; err != nil {
			return errRegNotFound
		}
		var invitation models.OccurrenceAccess
		if err := tx.Where("registration_id = ? AND status = ?", r.ID, policy.OccurrenceAccessActive).Limit(1).Find(&invitation).Error; err != nil {
			return err
		}
		if invitation.ID != 0 {
			account, err := occurrenceAccountIn(tx, me.ID, time.Now().UTC(), time.UTC)
			if err != nil {
				return err
			}
			live, err := liveOccurrenceInvitationIn(tx, invitation, time.Now().UTC())
			if err != nil {
				return err
			}
			if !live || !policy.CanEndOccurrenceAccess(account, true, occurrenceAccessPolicy(invitation)) {
				return errPeopleAddForbidden
			}
		}
		if err := tx.Model(&models.Registration{}).Where("id = ?", r.ID).Updates(map[string]any{
			"status": models.RegRemoved, "removed_by": me.ID, "removal_note": note,
			"waitlisted_at": nil, "promoted_at": nil, "manage_token_hash": "",
		}).Error; err != nil {
			return err
		}
		meta := registrationMeta(r)
		if invitation.ID == 0 {
			meta["note"] = note
		}
		meta["oldStatus"] = r.Status
		meta["newStatus"] = models.RegRemoved
		if err := audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.RegistrationRemoved,
			TargetType: "event", TargetID: ev.ID,
			Meta: meta,
			IP:   c.ClientIP(),
		}); err != nil {
			return err
		}
		if r.UserID != nil {
			var pending []models.ContactRequest
			if err := tx.Where("purpose = ? AND subject_user_id = ? AND occurrence_id = ? AND status = ?", policy.RequestOccurrenceAdd, *r.UserID, locked.ID, policy.ContactPending).Order("id").Find(&pending).Error; err != nil {
				return err
			}
			for i := range pending {
				ids, err := finishContactRequest(tx, &pending[i], policy.ContactEnded, policy.OccurrenceRemoved, time.Now().UTC())
				if err != nil {
					return err
				}
				emailed = append(emailed, ids...)
			}
		}
		var ids []uint
		if invitation.ID != 0 {
			ids, err = endOccurrenceAccessIn(tx, &invitation, policy.OccurrenceRemoved, &me.ID, time.Now().UTC(), c.ClientIP())
		} else {
			ids, err = notifyRegistrationRemoved(tx, ev, locked, r, note, me.ID)
		}
		if err != nil {
			return err
		}
		emailed = append(emailed, ids...)
		// Removing a going party retries promotion under the occurrence lock.
		// promoteLocked checks remaining attendance/childcare room and cutoff;
		// being at or over capacity after removal never seats another party.
		if r.Status == models.RegGoing {
			ids, err := a.promoteLocked(tx, ev, locked, time.Now().UTC(), &me.ID)
			if err != nil {
				return err
			}
			emailed = append(emailed, ids...)
		}
		return nil
	})
	if errors.Is(err, errPeopleAddForbidden) {
		jsonError(c, 403, "forbidden")
		return
	}
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
		RegistrationID uint   `json:"registrationId"`
		Date           string `json:"date"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.RegistrationID == 0 || body.Date == "" {
		jsonError(c, http.StatusBadRequest, "registrationId and date are required")
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
		if err := lockContacts(tx); err != nil {
			return err
		}
		actor, err := peopleAddActor(tx, me)
		if err != nil {
			return err
		}
		if err := tx.First(ev, ev.ID).Error; err != nil {
			return err
		}
		facts := a.factsForIn(tx, actor, ev)
		if !policy.CanManageEvent(asPerson(actor), facts.view, facts.rel) {
			return errPeopleAddForbidden
		}
		me = actor
		if err := lockRegistrationHolder(tx, body.RegistrationID, o.ID); err != nil {
			return err
		}
		locked, err := lockOccurrence(tx, o.ID)
		if err != nil {
			return err
		}
		var r models.Registration
		if err := tx.Where("id = ? AND occurrence_id = ? AND status = ?",
			body.RegistrationID, locked.ID, models.RegWaitlisted).First(&r).Error; err != nil {
			return errRegNotFound
		}
		if !a.eligibleRegistration(tx, r, ev, time.Now().UTC()) {
			return householdRegistrationError{code: "household_registration_holder_unavailable"}
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
		if err := checkChildcare(tx, ev, locked.ID, r.ID, effectiveChildcare(ev, r)); err != nil {
			return err
		}
		if err := audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.RegistrationPromoted,
			TargetType: "event", TargetID: ev.ID,
			Meta: registrationMeta(r),
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
		registrationError(c, err)
		return
	}
	a.deliverNotificationEmails(emailed)
	c.JSON(http.StatusOK, gin.H{})
}

func (a *App) apiAddRegistration(c *gin.Context) {
	ev, ok := a.loadManagedDateAddEvent(c)
	if !ok {
		return
	}
	var body struct {
		UserID             uint   `json:"userId"`
		SelectionID        string `json:"selectionId"`
		PersonID           uint   `json:"personId"`
		RegisteredBy       uint   `json:"registeredBy"`
		ChildcareRequested *bool  `json:"childcareRequested"`
		Email              string `json:"email"`
		Date               string `json:"date"`
		GuestCount         *int   `json:"guestCount"`
		OverCapacity       *bool  `json:"overCapacity"`
		// AttendanceMode seats the person in that pool; absent keeps an
		// existing registration's mode (in_person for a new one).
		AttendanceMode *string `json:"attendanceMode"`
		childcareRequest
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Date == "" || (body.UserID == 0 && body.Email == "" && body.PersonID == 0 && body.SelectionID == "") {
		a.managerDateAddError(c, http.StatusBadRequest, "userId or email, and date, are required")
		return
	}
	if body.SelectionID != "" && (body.UserID != 0 || body.Email != "" || body.PersonID != 0) {
		a.managerDateAddError(c, 400, "choose one registration target")
		return
	}
	if body.PersonID != 0 {
		if body.RegisteredBy == 0 || body.UserID != 0 || body.Email != "" || body.GuestCount != nil || body.ChildcareCount != nil || body.ChildcareAgesNote != nil {
			a.managerDateAddError(c, 400, "choose one registration target")
			return
		}
		mode := ""
		if body.AttendanceMode != nil {
			mode = *body.AttendanceMode
		}
		if !validMode(mode) {
			a.managerDateAddError(c, 400, "attendanceMode must be in_person or remote")
			return
		}
		a.addHouseholdRegistration(c, ev, body.Date, body.PersonID, body.RegisteredBy, mode, body.ChildcareRequested, body.OverCapacity != nil && *body.OverCapacity)
		return
	}
	if body.RegisteredBy != 0 || body.ChildcareRequested != nil {
		a.managerDateAddError(c, 400, "choose one registration target")
		return
	}
	if body.ChildcareCount != nil {
		if *body.ChildcareCount < 0 || *body.ChildcareCount > 20 {
			a.managerDateAddError(c, 400, "childcareCount must be a whole number from 0 to 20")
			return
		}
		if *body.ChildcareCount > 0 && !ev.ChildcareAvailable {
			a.managerDateAddError(c, 400, errChildcareUnavailable.Error())
			return
		}
	}
	if body.ChildcareAgesNote != nil && utf8.RuneCountInString(strings.TrimSpace(*body.ChildcareAgesNote)) > 200 {
		a.managerDateAddError(c, 400, "childcare age note must be 200 characters or fewer")
		return
	}
	addMode := ""
	if body.AttendanceMode != nil {
		addMode = strings.TrimSpace(*body.AttendanceMode)
		switch addMode {
		case models.AttendanceInPerson, models.AttendanceRemote:
		default:
			a.managerDateAddError(c, http.StatusBadRequest, "attendanceMode must be in_person or remote")
			return
		}
		if addMode == models.AttendanceRemote && !ev.RemoteAvailable {
			a.managerDateAddError(c, http.StatusBadRequest, "remote attendance isn't available for this event")
			return
		}
	}
	guestCount := 0
	if body.GuestCount != nil {
		guestCount = *body.GuestCount
	}
	if guestCount < 0 || guestCount > ev.GuestsAllowed {
		if err := a.auditManagerDateRefusal(c); err != nil {
			jsonError(c, 500, "could not add registration")
			return
		}
		jsonErrorCode(c, http.StatusBadRequest, "too_many_guests",
			fmt.Sprintf("at most %d guests are allowed", ev.GuestsAllowed))
		return
	}
	var target models.User
	// A pruned guest keeps status=guest (never active); the add action is
	// their way back in, so both statuses resolve here.
	if body.SelectionID != "" {
		id, err := a.parsePeopleSelection(body.SelectionID, apiUser(c).ID, time.Now())
		if err != nil || a.DB.Where("id = ? AND status = ? AND account_type IN ?", id, models.UserActive, []string{models.AccountAdult, models.AccountTeen}).First(&target).Error != nil {
			a.managerDateAddError(c, 400, "that selection is unavailable; search for the person again")
			return
		}
	} else if body.UserID != 0 {
		if err := a.DB.Where("id = ? AND status IN ?", body.UserID,
			[]string{models.UserActive, models.UserGuest}).First(&target).Error; err != nil {
			a.managerDateAddError(c, http.StatusNotFound, "person not found")
			return
		}
	} else {
		// The leader UI adds by email; resolve it to the account.
		if err := a.DB.Where("email = ? AND status IN ?", strings.TrimSpace(body.Email),
			[]string{models.UserActive, models.UserGuest}).First(&target).Error; err != nil {
			a.managerDateAddError(c, http.StatusNotFound, "person not found")
			return
		}
	}
	var o models.EventOccurrence
	if err := a.DB.Where("event_id = ? AND original_date = ? AND status = ?",
		ev.ID, body.Date, models.OccurrenceScheduled).First(&o).Error; err != nil {
		a.managerDateAddError(c, http.StatusNotFound, "that date is not on this event")
		return
	}
	me := apiUser(c)
	overCap := body.OverCapacity != nil && *body.OverCapacity
	var emailed []uint
	var businessErr error
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		// Manager adds serialize with account, leadership and host revocation.
		// Take the existing contact gate FIRST, before any domain row lock.
		if err := lockContacts(tx); err != nil {
			return err
		}
		actor, err := peopleAddActor(tx, me)
		if err != nil {
			return err
		}
		if err := tx.First(ev, ev.ID).Error; err != nil {
			return errPeopleAddForbidden
		}
		facts := a.factsForIn(tx, actor, ev)
		if !policy.CanManageEvent(asPerson(actor), facts.view, facts.rel) {
			return errPeopleAddForbidden
		}
		me = actor
		allowed, err := managerDateAddAllowedIn(tx, actor.ID)
		if err != nil {
			return err
		}
		refuse := func(err error) error {
			businessErr = err
			return audit.Write(tx, audit.Entry{Actor: &actor.ID, Action: audit.OccurrenceRefused, TargetType: "occurrence_endpoint", IP: c.ClientIP(), Meta: map[string]any{"operation": "manager_add"}})
		}
		if !allowed {
			return refuse(errDateAddThrottled)
		}
		if target.ID == actor.ID {
			return refuse(errPeopleAddForbidden)
		}
		var holder models.User
		if err := tx.Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).First(&holder, target.ID).Error; err != nil {
			return err
		}
		if holder.Status != models.UserActive && holder.Status != models.UserGuest {
			return errRegNotFound
		}
		if policy.TeenAddNeedsApproval(asPerson(&holder)) {
			return refuse(errDateAddApproval)
		}
		locked, err := lockOccurrence(tx, o.ID)
		if err != nil {
			return err
		}
		if locked.Status != models.OccurrenceScheduled {
			return errOccurrenceNotFound
		}
		// Recipient controls apply to every account-add sibling, even before
		// the final request/provenance stage switches ordinary adds over.
		f, err := a.loadOccurrenceAddFacts(tx, models.ContactRequest{Purpose: policy.RequestOccurrenceAdd, RequesterID: me.ID, SubjectUserID: holder.ID, ContextType: policy.ContactEvent, ContextID: ev.ID, OccurrenceID: &locked.ID}, time.Now().UTC())
		if err != nil {
			return err
		}
		if policy.ManagerAddBlocked(f.Block, holder.ID, f.Occurrence.Organizer) {
			return refuse(errPeopleAddForbidden)
		}
		if (f.LatestAccessEndedAt != nil && !f.ExistingAccessActive) || !f.OrdinaryEligible {
			return refuse(errDateAddApproval)
		}
		if holder.Status != models.UserGuest && (!f.Subject.Active || (f.Subject.AgeYears != nil && *f.Subject.AgeYears < 18)) {
			return refuse(errDateAddApproval)
		}
		registration, ids, err := a.addAccountRegistrationLocked(tx, ev, locked, target.ID, me.ID, managerRegistrationOptions{mode: addMode, guests: guestCount, overCapacity: overCap, care: body.childcareRequest}, time.Now().UTC(), c.ClientIP())
		emailed = append(emailed, ids...)
		if errors.Is(err, errEventFull) || errors.Is(err, errChildcareFull) || errors.Is(err, errChildcareUnavailable) || errors.Is(err, errRemoteNotAvailable) {
			return refuse(err)
		}
		if err != nil {
			return err
		}
		if holder.Status == models.UserActive {
			ids, err = a.recordManagerOccurrenceAdd(tx, ev, locked, registration, actor.ID, nil, false, time.Now().UTC(), c.ClientIP())
			if err != nil {
				return err
			}
			emailed = append(emailed, ids...)
		}
		return nil
	})
	if err != nil {
		if auditErr := a.auditManagerDateRefusal(c); auditErr != nil {
			jsonError(c, 500, "could not add registration")
			return
		}
	}
	if err == nil {
		err = businessErr
	}
	switch {
	case errors.Is(err, errDateAddApproval), errors.Is(err, errDateAddThrottled):
		jsonError(c, 409, err.Error())
		return
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
	case errors.Is(err, errPeopleAddForbidden):
		jsonError(c, http.StatusForbidden, "forbidden")
		return
	case errors.Is(err, errTeenAddApproval):
		jsonError(c, 409, errTeenAddApproval.Error())
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
		Where("(registrations.user_id = ? OR registrations.registered_by = ?) AND registrations.status = ? AND event_occurrences.status = ? AND event_occurrences.starts_at > ?",
			userID, userID, models.RegGoing, models.OccurrenceScheduled, time.Now().UTC()).
		Distinct().Order("registrations.occurrence_id").Pluck("registrations.occurrence_id", &occIDs).Error; err != nil {
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

// restoreRegistrations checks future answers in occurrence/registration ID
// order while the holder is disabled. Accepted going rows accumulate usage
// across both seat pools and care; ineligible or non-fitting rows are
// audited and dropped. Eligible waiters retain their original place.
func (a *App) restoreRegistrations(tx *gorm.DB, userID uint, intendedStatus string, actor uint, ip string, now time.Time) ([]uint, error) {
	var actorID *uint
	if actor != 0 {
		actorID = &actor
	}
	var holder models.User
	if err := tx.First(&holder, userID).Error; err != nil {
		return nil, err
	}
	holder.Status = intendedStatus
	var regs []models.Registration
	if err := tx.Joins("JOIN event_occurrences ON event_occurrences.id = registrations.occurrence_id").Where("(registrations.user_id = ? OR registrations.registered_by = ?) AND registrations.status IN ? AND event_occurrences.status = ? AND event_occurrences.starts_at > ?", userID, userID, []string{models.RegGoing, models.RegWaitlisted}, models.OccurrenceScheduled, now).Order("registrations.occurrence_id, registrations.id").Find(&regs).Error; err != nil {
		return nil, err
	}
	var mailed []uint
	var currentID uint
	var o *models.EventOccurrence
	var ev models.Event
	used := map[string]int{}
	careUsed := 0
	var facts subjectFacts
	var access map[uint]bool
	for _, r := range regs {
		if currentID != r.OccurrenceID {
			var err error
			o, err = lockOccurrence(tx, r.OccurrenceID)
			if err != nil {
				return nil, err
			}
			currentID = r.OccurrenceID
			ev = models.Event{}
			if err := tx.First(&ev, o.EventID).Error; err != nil {
				return nil, err
			}
			for _, mode := range []string{models.AttendanceInPerson, models.AttendanceRemote} {
				n, err := seatsUsedForMode(tx, o.ID, mode)
				if err != nil {
					return nil, err
				}
				used[mode] = n
			}
			n, err := childcareUsedFor(tx, o.ID, 0)
			if err != nil {
				return nil, err
			}
			careUsed = n
			currentRegs := []models.Registration{}
			for _, candidate := range regs {
				if candidate.OccurrenceID == o.ID {
					currentRegs = append(currentRegs, candidate)
				}
			}
			facts = a.loadSubjectFacts(tx, currentRegs, nil)
			facts.clock = func() time.Time { return a.householdClock(now) }
			facts.users[holder.ID] = &holder
			access = map[uint]bool{}
		}
		fits := true
		if r.HouseholdPersonID != nil {
			fits = facts.eligible(a, tx, r, &ev, access)
		} else {
			fits = a.canRegisterDateIn(tx, &holder, &ev, o.ID)
		}
		mode := attendanceModeOf(r.AttendanceMode)
		demand := effectiveChildcare(&ev, r)
		if r.Status == models.RegGoing {
			fits = fits && events.Fits(capacityForMode(&ev, o, mode), used[mode], 1+r.GuestCount) && (demand == 0 || !ev.ChildcareAvailable || events.Fits(ev.ChildcareCapacity, careUsed, demand))
		}
		if fits {
			if r.Status == models.RegGoing {
				used[mode] += 1 + r.GuestCount
				careUsed += demand
			}
			if err := auditRegistration(tx, actor, audit.RegistrationRestored, ev.ID, r, r, ip); err != nil {
				return nil, err
			}
			continue
		}
		if err := tx.Delete(&r).Error; err != nil {
			return nil, err
		}
		if err := auditRegistration(tx, actor, audit.RegistrationNotRestored, ev.ID, r, r, ip); err != nil {
			return nil, err
		}
		ids, err := notifyRegistrationNotRestored(tx, &ev, o, r, actorID)
		if err != nil {
			return nil, err
		}
		mailed = append(mailed, ids...)
	}
	return mailed, nil
}

// lockRegistrationHolder binds the row to this occurrence before locking its
// responsible account. It never treats a person ID as a user ID.
func lockRegistrationHolder(tx *gorm.DB, registrationID, occurrenceID uint) error {
	var r models.Registration
	if tx.Where("id = ? AND occurrence_id = ?", registrationID, occurrenceID).First(&r).Error != nil {
		return errRegNotFound
	}
	var holder models.User
	return tx.Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).First(&holder, registrationHolder(r)).Error
}

func previouslyRegisteredPerson(tx *gorm.DB, eventID, occurrenceID, personID, holderID uint) bool {
	var n int64
	tx.Model(&models.Registration{}).Where("occurrence_id = ? AND household_person_id = ? AND registered_by = ?", occurrenceID, personID, holderID).Count(&n)
	if n > 0 {
		return true
	}
	tx.Model(&models.AuditLog{}).Where("action = ? AND target_type = 'event' AND target_id = ? AND meta_json->>'personId' = ? AND meta_json->>'registeredBy' = ? AND meta_json->>'occurrenceId' = ?", audit.RegistrationNotRestored, eventID, strconv.FormatUint(uint64(personID), 10), strconv.FormatUint(uint64(holderID), 10), strconv.FormatUint(uint64(occurrenceID), 10)).Count(&n)
	return n > 0
}

func (a *App) addHouseholdRegistration(c *gin.Context, ev *models.Event, date string, personID, holderID uint, mode string, namedCare *bool, overCapacity bool) {
	var o models.EventOccurrence
	if a.DB.Where("event_id = ? AND original_date = ? AND status = ?", ev.ID, date, models.OccurrenceScheduled).First(&o).Error != nil {
		a.managerDateAddError(c, 404, "that date is not on this event")
		return
	}
	actor := apiUser(c)
	now := time.Now().UTC()
	var mailed []uint
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		var holder models.User
		if err := tx.Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).First(&holder, holderID).Error; err != nil {
			return errRegNotFound
		}
		subject, err := householdSubject(tx, personID, holderID)
		if err != nil {
			return err
		}
		var person models.HouseholdPerson
		if err := tx.First(&person, personID).Error; err != nil {
			return errRegNotFound
		}
		if err := lockHousehold(tx, person.HouseholdID); err != nil {
			return err
		}
		if err := tx.First(&person, personID).Error; err != nil {
			return errRegNotFound
		}
		locked, err := lockOccurrence(tx, o.ID)
		if err != nil {
			return err
		}
		if locked.Status != models.OccurrenceScheduled {
			return errOccurrenceNotFound
		}
		if err := tx.First(ev, ev.ID).Error; err != nil {
			return err
		}
		acct, found := householdAccountIn(tx, holderID)
		links := guardianLinksForPeople(tx, []uint{personID})
		f := a.factsForIn(tx, actor, ev)
		problem := policy.CanManageHouseholdRegistration(asPerson(actor), asPerson(&holder), policy.CanManageEvent(asPerson(actor), f.view, f.rel), previouslyRegisteredPerson(tx, ev.ID, o.ID, personID, holderID), householdRelOf(acct, found && acct.HouseholdID == person.HouseholdID), householdFacts(&person, links[personID], a.householdClock(now)), holder.Status == models.UserActive, a.canRegisterIn(tx, &holder, dateEvent(ev, &o)))
		if problem != "" {
			return householdRegistrationError{code: problem}
		}
		var old models.Registration
		subject.query(tx, o.ID).First(&old)
		// Re-adding keeps ownership; another adult cannot transfer this answer.
		if old.ID != 0 && registrationHolder(old) != holderID {
			return errRegNotFound
		}
		r := old
		if r.ID == 0 {
			r = subject.row(o.ID)
			r.Source = models.RegSourceAdmin
		}
		r.Status = models.RegGoing
		r.RemovedBy = nil
		r.RemovalNote = ""
		r.ManageTokenHash = ""
		if mode != "" {
			r.AttendanceMode = mode
		} else {
			r.AttendanceMode = attendanceModeOf(r.AttendanceMode)
		}
		if namedCare != nil {
			r.ChildcareRequested = *namedCare
		}
		if r.ChildcareRequested && r.AttendanceMode == models.AttendanceRemote {
			return householdRegistrationError{code: "household_remote_childcare", personID: personID}
		}
		if r.ChildcareRequested && person.Kind != models.HouseholdChild {
			return householdRegistrationError{code: "household_age_mismatch", personID: personID}
		}
		if namedCare != nil && *namedCare && !ev.ChildcareAvailable && !old.ChildcareRequested {
			return errChildcareUnavailable
		}
		if r.AttendanceMode == models.AttendanceRemote && !ev.RemoteAvailable {
			return errRemoteNotAvailable
		}
		used, err := seatsUsedForMode(tx, o.ID, r.AttendanceMode)
		if err != nil {
			return err
		}
		// Keep the existing user re-add arithmetic; replacement-delta cleanup is
		// deferred. A household add is normally a removed or dropped row.
		over := !events.Fits(capacityForMode(ev, locked, r.AttendanceMode), used, 1)
		if over && !overCapacity {
			return errEventFull
		}
		if err := checkChildcare(tx, ev, o.ID, r.ID, effectiveChildcare(ev, r)); err != nil {
			return err
		}
		if err := saveRegistration(tx, &r); err != nil {
			return err
		}
		action := audit.RegistrationAdded
		if over {
			action = audit.RegistrationAddedOverCapacity
		}
		if err := auditRegistration(tx, actor.ID, action, ev.ID, old, r, c.ClientIP()); err != nil {
			return err
		}
		if old.Status == models.RegGoing && effectiveChildcare(ev, r) < effectiveChildcare(ev, old) {
			mailed, err = a.promoteLocked(tx, ev, locked, now, &actor.ID)
			return err
		}
		return nil
	})
	if err != nil {
		if auditErr := a.auditManagerDateRefusal(c); auditErr != nil {
			jsonError(c, 500, "could not add registration")
			return
		}
		registrationError(c, err)
		return
	}
	a.deliverNotificationEmails(mailed)
	c.JSON(http.StatusOK, gin.H{})
}

func householdRegistrationMessage(code string) string {
	switch code {
	case "household_link_pending":
		return "waiting for an admin to confirm; ask your church office to check the household link"
	case "household_link_unconfirmed":
		return "this household link is not confirmed; ask your church office to check it"
	case "household_birth_date_required":
		return "add a birth date in profile before signing this person up"
	case "household_age_mismatch":
		return "the birth date does not match an adult; correct it or choose child or teen in profile"
	case "household_remote_childcare":
		return "remote attendees cannot request childcare; clear the childcare choice first"
	case "household_registration_holder_unavailable":
		return "the responsible account cannot register this person; ask your church office to check their account and household"
	default:
		return "household registration needs review"
	}
}

// managerRegistrationOptions retains the original PATCH resolution for ordinary
// manager adds. A request finalizer must supply its persisted final values.
type managerRegistrationOptions struct {
	approved     *verifiedOccurrenceAdd
	mode         string
	guests       int
	overCapacity bool
	care         childcareRequest
}

// addAccountRegistrationLocked is the original account seat-write core. Caller
// owns the transaction, has taken the contact gate FIRST then the account and
// occurrence locks, and has revalidated current management and target authority.
// Teen/out-of-visibility authority must come from a reloaded approved request,
// never an HTTP boolean. This core neither grants access nor commits or mails.
func (a *App) addAccountRegistrationLocked(tx *gorm.DB, ev *models.Event, locked *models.EventOccurrence, targetID, actor uint, options managerRegistrationOptions, now time.Time, ip string) (models.Registration, []uint, error) {
	if locked.Status != models.OccurrenceScheduled || locked.EventID != ev.ID {
		return models.Registration{}, nil, errOccurrenceNotFound
	}
	if options.guests < 0 || options.guests > ev.GuestsAllowed {
		return models.Registration{}, nil, errPeopleAddForbidden
	}
	if options.mode == models.AttendanceRemote && !ev.RemoteAvailable {
		return models.Registration{}, nil, errRemoteNotAvailable
	}

	var mailed []uint
	var r models.Registration
	lookup := tx.Where("user_id = ? AND occurrence_id = ?", targetID, locked.ID).First(&r)
	if lookup.Error != nil && !errors.Is(lookup.Error, gorm.ErrRecordNotFound) {
		return models.Registration{}, nil, lookup.Error
	}
	found := lookup.RowsAffected == 1
	before := r
	count, ages := options.care.values(r)
	if err := checkChildcare(tx, ev, locked.ID, r.ID, count); err != nil {
		return models.Registration{}, nil, err
	}
	mode := models.AttendanceInPerson
	if found {
		mode = attendanceModeOf(r.AttendanceMode)
	}
	if options.mode != "" {
		mode = options.mode
	}
	if mode == models.AttendanceRemote && !ev.RemoteAvailable {
		return models.Registration{}, nil, errRemoteNotAvailable
	}
	if options.care.ChildcareCount != nil && count > 0 && !ev.ChildcareAvailable {
		return models.Registration{}, nil, errChildcareUnavailable
	}
	var subject models.User
	if err := tx.First(&subject, targetID).Error; err != nil {
		return models.Registration{}, nil, err
	}
	if options.approved != nil || policy.TeenAddNeedsApproval(asPerson(&subject)) || !a.canRegisterIn(tx, &subject, dateEvent(ev, locked)) {
		approved, err := a.verifiedOccurrenceAddIn(tx, options.approved, actor, targetID, ev.ID, locked.ID, policy.AddOptions{OverCapacity: options.overCapacity, AttendanceMode: mode, GuestCount: options.guests, ChildcareCount: count, ChildcareAgesNote: ages})
		if err != nil {
			return models.Registration{}, nil, err
		}
		if !approved {
			return models.Registration{}, nil, errPeopleAddForbidden
		}
	}

	// The add seats the person in their pool; over-capacity is
	// judged against that pool only (M1.8 slice 2).
	cap := capacityForMode(ev, locked, mode)
	used, err := seatsUsedForMode(tx, locked.ID, mode)
	if err != nil {
		return models.Registration{}, nil, err
	}
	if found && r.Status == models.RegGoing && attendanceModeOf(r.AttendanceMode) == mode {
		used -= 1 + r.GuestCount
	}
	seats := 1 + options.guests
	over := !events.Fits(cap, used, seats)
	if over && !options.overCapacity {
		return models.Registration{}, nil, errEventFull
	}
	updates := map[string]any{
		"status": models.RegGoing, "guest_count": options.guests, "attendance_mode": mode,
		"removed_by": nil, "removal_note": "", "waitlisted_at": nil, "promoted_at": nil,
		"childcare_count": count, "childcare_ages_note": ages,
	}
	if found {
		if err := tx.Model(&models.Registration{}).Where("id = ?", r.ID).Updates(updates).Error; err != nil {
			return models.Registration{}, nil, err
		}
	} else {
		r = models.Registration{
			UserID: &targetID, OccurrenceID: locked.ID, ChildcareCount: count, ChildcareAgesNote: ages,
			Status: models.RegGoing, GuestCount: options.guests, Source: models.RegSourceAdmin,
			AttendanceMode: mode,
		}
		if err := tx.Create(&r).Error; err != nil {
			return models.Registration{}, nil, err
		}
	}
	action := audit.RegistrationAdded
	after := r
	after.Status = models.RegGoing
	after.GuestCount = options.guests
	after.AttendanceMode = mode
	after.ChildcareCount = count
	after.ChildcareAgesNote = ages
	after.RemovedBy, after.RemovalNote, after.WaitlistedAt, after.PromotedAt = nil, "", nil, nil
	meta := registrationMeta(after)
	meta["oldStatus"] = before.Status
	meta["newStatus"] = models.RegGoing
	if over {
		action = audit.RegistrationAddedOverCapacity
		meta["seats"] = used + seats
		meta["capacity"] = *cap
	}
	if err := audit.Write(tx, audit.Entry{Actor: &actor, Action: action,
		TargetType: "event", TargetID: ev.ID, Meta: meta, IP: ip,
	}); err != nil {
		return models.Registration{}, nil, err
	}
	if found && before.Status == models.RegGoing && (mode != attendanceModeOf(before.AttendanceMode) || options.guests < before.GuestCount || count < before.ChildcareCount) {
		ids, err := a.promoteLocked(tx, ev, locked, now, &actor)
		if err != nil {
			return models.Registration{}, nil, err
		}
		mailed = append(mailed, ids...)
	}
	return after, mailed, nil
}
