package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
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
// notification rows. Each notice carries its own email delivery
// intent in notify.Send's transaction. M1.8 slice 2 adds the second seat pool:
// remote registrations count against the remote capacity only.

var (
	// errRemoteNotAvailable: a remote request on an event that does
	// not offer remote attendance.
	errRemoteNotAvailable = apiErr(http.StatusBadRequest, "remote_not_available", "remote attendance isn't available for this event")
	// errRemovedBlocked: a pruned registration blocks self-registration.
	errRemovedBlocked = apiErr(http.StatusConflict, "registration_removed", "a leader or host removed your registration for this date; ask them to add you back")
	// errRegNotFound: no matching registration row. A handler whose person is
	// not a household person says so in its own words before it calls fail.
	errRegNotFound = apiErr(http.StatusNotFound, "", "household person not found")
	// errAddTargetGone: the person a manager is adding was erased while the add
	// waited for the contact gate.
	errAddTargetGone = apiErr(http.StatusNotFound, "", "person not found")
	// errOccurrenceNotFound: no occurrence for the event and date.
	errOccurrenceNotFound  = apiErr(http.StatusNotFound, "", "that date is not on this event")
	errOccurrenceGateRetry = errors.New("retry invitation mutation under contact gate")
)

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
	reminder        int
	reminderSet     bool
}

// parseCapacityEdits parses the M1.5 capacity fields; the error is the 400 to
// answer with.
func parseCapacityEdits(body eventBody) (capacityEdits, error) {
	var ed capacityEdits
	var err error
	if ed.cap, ed.capSet, err = parseCapacity(body.Capacity); err != nil {
		return ed, err
	}
	if ed.guests, ed.guestsSet, err = parseGuestsAllowed(body.GuestsAllowed); err != nil {
		return ed, err
	}
	if ed.remoteCap, ed.remoteCapSet, err = parseRemoteCapacity(body.RemoteCapacity, false); err != nil {
		return ed, err
	}
	if err := parseChildcareEdits(body, &ed); err != nil {
		return ed, err
	}
	if body.ReminderEmailMinutes != nil {
		if !validReminderMinutes(*body.ReminderEmailMinutes) {
			return ed, errReminderTime
		}
		ed.reminder, ed.reminderSet = *body.ReminderEmailMinutes, true
	}
	return ed, nil
}

// intPtrEqual reports whether two optional ints hold the same value.
func intPtrEqual(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
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
	var target models.Registration
	if err := db.Joins(listedRegistrations()).
		Where("registrations.id = ? AND registrations.occurrence_id = ? AND registrations.status = ?",
			regID, occurrenceID, models.RegWaitlisted).
		Limit(1).Find(&target).Error; err != nil {
		return 0, err
	}
	if target.ID == 0 {
		return 0, nil
	}
	q := db.Model(&models.Registration{}).Joins(listedRegistrations()).
		Where("registrations.occurrence_id = ? AND registrations.status = ?", occurrenceID, models.RegWaitlisted)
	if target.WaitlistedAt != nil {
		// NULL waitlisted_at sorts last, so it never precedes a dated row.
		q = q.Where("registrations.waitlisted_at IS NOT NULL AND (registrations.waitlisted_at, registrations.id) <= (?, ?)",
			target.WaitlistedAt, target.ID)
	} else {
		q = q.Where("(registrations.waitlisted_at IS NOT NULL OR registrations.id <= ?)", target.ID)
	}
	var n int64
	err := q.Count(&n).Error
	return int(n), err
}

// eventRunnerIDs are the people who run an event: its group's leaders, or a
// church-wide event's hosts whose accounts still exist (a deleted account's
// leftover host row is not a host), in ascending id.
func eventRunnerIDs(tx *gorm.DB, ev *models.Event) ([]uint, error) {
	if ev.GroupID != nil {
		return leaderIDs(tx, *ev.GroupID)
	}
	var ids []uint
	err := tx.Model(&models.EventHost{}).
		Joins("JOIN users u ON u.id = event_hosts.user_id AND u.deleted_at IS NULL").
		Where("event_hosts.event_id = ?", ev.ID).Order("event_hosts.user_id").
		Pluck("event_hosts.user_id", &ids).Error
	return ids, err
}

// eventGroupIDs is the event's group as the list loadNoticeFacts takes.
func eventGroupIDs(ev *models.Event) []uint {
	if ev.GroupID == nil {
		return nil
	}
	return []uint{*ev.GroupID}
}

// noticeEnd is when a date's notices stop mattering: its end, or its start
// when it has none.
func noticeEnd(o *models.EventOccurrence) time.Time {
	if o.EndsAt.IsZero() {
		return o.StartsAt
	}
	return o.EndsAt
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
	return eventNoticeVars(ev, o, userID, loadNoticeFacts(tx, eventGroupIDs(ev), []uint{userID}))
}

// addPersonVar names a household registration's person in the notice.
func addPersonVar(tx *gorm.DB, vars *notify.Vars, reg models.Registration) error {
	if reg.HouseholdPersonID == nil {
		return nil
	}
	name, err := householdPersonName(tx, *reg.HouseholdPersonID)
	vars.Person = name
	return err
}

// notifyRegistrationPromoted tells one person they are in off the
// waitlist. The email carries the rotated manage link with the one-click
// "I can't come" cancel; the bell never shows the token.
func notifyRegistrationPromoted(tx *gorm.DB, ev *models.Event, o *models.EventOccurrence,
	reg models.Registration, manageURL string, actor *uint) error {
	userID := registrationHolder(reg)
	accesses, err := occurrenceNoticeAccesses(tx, []uint{o.ID})
	if err != nil {
		return err
	}
	if invitation, ok := accesses[o.ID][userID]; ok && reg.UserID != nil && invitation.ID == reg.ID {
		return sendOccurrenceNotice(tx, invitation, o, notify.OccurrenceChanged, "promoted:"+strconv.FormatUint(uint64(invitation.ID), 10)+":"+time.Now().UTC().Format(time.RFC3339Nano), nil)
	}
	vars, link := eventNoticeVarsIn(tx, ev, o, userID)
	if err := addPersonVar(tx, &vars, reg); err != nil {
		return err
	}
	if actor != nil && *actor == userID {
		actor = nil
	}
	vars.ManageURL = manageURL
	// A promoted person is going now, so a video link set to
	// registered_only may reach them (5.8, M1.8); the email carries it,
	// the bell doesn't.
	if video := videoLinkOf(ev); video != "" &&
		policy.CanSeeVideoLink(videoRevealOf(ev), true, false) && (reg.HouseholdPersonID == nil || reg.AttendanceMode == models.AttendanceRemote) {
		vars.VideoURL = video
	}
	end := noticeEnd(o)
	_, err = notify.Send(tx, notify.Msg{
		Kind: notify.RegistrationPromoted, To: []uint{userID}, Actor: actor,
		SubjectType: "occurrence", SubjectID: o.ID, Link: link, Vars: vars,
		Data:      map[string]any{"eventId": ev.ID, "date": models.FormatCalendarDate(o.OriginalDate)},
		ExpiresAt: &end,
	})
	return err
}

// notifyWaitlistCutoff tells the leaders or hosts that a seat opened
// inside the 2-hour cutoff, so they can promote by hand. Admins get it
// only when they also lead or host.
func notifyWaitlistCutoff(tx *gorm.DB, ev *models.Event, o *models.EventOccurrence, actor *uint) error {
	to, err := eventRunnerIDs(tx, ev)
	if err != nil {
		return err
	}
	if len(to) == 0 {
		return nil
	}
	// Vars are per recipient: the time renders in each leader's own zone.
	facts := loadNoticeFacts(tx, eventGroupIDs(ev), to)
	end := noticeEnd(o)
	// One bulk recipient lock for the whole fan-out (M7.4 R4-N), not one lock
	// query per leader or host.
	return notify.WithRecipients(tx, to, func(scope *notify.Scope) error {
		for _, uid := range to {
			vars, link := eventNoticeVars(ev, o, uid, facts)
			_, err := notify.Send(tx, notify.Msg{
				Kind: notify.WaitlistCutoff, To: []uint{uid}, Actor: actor,
				SubjectType: "occurrence", SubjectID: o.ID, Link: link, Vars: vars,
				Data:      map[string]any{"eventId": ev.ID, "date": models.FormatCalendarDate(o.OriginalDate)},
				ExpiresAt: &end, Scope: scope,
			})
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// notifyEventChanged tells everyone going or waitlisted on one occurrence
// that its time or place changed. One item per person per occurrence per
// change; the item is never batched.
func notifyEventChanged(tx *gorm.DB, ev *models.Event, o *models.EventOccurrence, variant string, actor *uint) error {
	if o.Status != models.OccurrenceScheduled {
		// No fan-out into a cancelled date: nobody is going, and the
		// waitlist must not be mailed about a dead event.
		return nil
	}
	var userIDs []uint
	if err := tx.Table("registrations").Distinct("users.id").
		Joins(listedRegistrations()).
		Where("registrations.occurrence_id = ? AND registrations.status IN ?",
			o.ID, []string{models.RegGoing, models.RegWaitlisted}).
		Order("users.id").Pluck("users.id", &userIDs).Error; err != nil {
		return err
	}
	facts := loadNoticeFacts(tx, eventGroupIDs(ev), userIDs)
	accesses, err := occurrenceNoticeAccesses(tx, []uint{o.ID})
	if err != nil {
		return err
	}
	// One bulk recipient lock for the whole fan-out (M7.4 R4-N), shared by the
	// invited and the ordinary notices below, not one lock query per person.
	return notify.WithRecipients(tx, userIDs, func(scope *notify.Scope) error {
		for _, uid := range userIDs {
			if invitation, ok := accesses[o.ID][uid]; ok {
				if err := sendOccurrenceNotice(tx, invitation, o, notify.OccurrenceChanged, "", scope); err != nil {
					return err
				}
				continue
			}
			vars, link := eventNoticeVars(ev, o, uid, facts)
			vars.Change = variant
			end := noticeEnd(o)
			_, err := notify.Send(tx, notify.Msg{
				Kind: notify.EventChanged, To: []uint{uid}, Actor: actor,
				SubjectType: "occurrence", SubjectID: o.ID, Link: link, Vars: vars,
				Data:      map[string]any{"eventId": ev.ID, "date": models.FormatCalendarDate(o.OriginalDate), "change": variant},
				ExpiresAt: &end, Scope: scope,
			})
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// notifyRegistrationRemoved tells a pruned person they were removed. The
// note reaches only them.
func notifyRegistrationRemoved(tx *gorm.DB, ev *models.Event, o *models.EventOccurrence,
	reg models.Registration, note string, actor *uint) error {
	userID := registrationHolder(reg)
	u := userIn(tx, userID)
	if u == nil || u.Status == models.UserDisabled {
		return nil
	}
	vars, link := eventNoticeVarsIn(tx, ev, o, userID)
	if err := addPersonVar(tx, &vars, reg); err != nil {
		return err
	}
	vars.Note = note
	end := noticeEnd(o)
	_, err := notify.Send(tx, notify.Msg{
		Kind: notify.RegistrationRemoved, To: []uint{userID}, Actor: actor,
		SubjectType: "occurrence", SubjectID: o.ID, Link: link, Vars: vars,
		Data:      map[string]any{"eventId": ev.ID, "date": models.FormatCalendarDate(o.OriginalDate)},
		ExpiresAt: &end,
	})
	return err
}

// notifyRegistrationNotRestored tells a re-enabled person their
// registration was dropped from a full occurrence (DEC-47).
func notifyRegistrationNotRestored(tx *gorm.DB, ev *models.Event, o *models.EventOccurrence, reg models.Registration, actor *uint) error {
	userID := registrationHolder(reg)
	vars, link := eventNoticeVarsIn(tx, ev, o, userID)
	if err := addPersonVar(tx, &vars, reg); err != nil {
		return err
	}
	end := noticeEnd(o)
	_, err := notify.Send(tx, notify.Msg{
		Kind: notify.RegistrationNotRestored, To: []uint{userID}, Actor: actor,
		SubjectType: "occurrence", SubjectID: o.ID, Link: link, Vars: vars,
		Data:      map[string]any{"eventId": ev.ID, "date": models.FormatCalendarDate(o.OriginalDate)},
		ExpiresAt: &end,
	})
	return err
}

// promoteLocked promotes waitlisted registrations that fit, in line
// order, under the caller's occurrence lock. now is the clock: handlers
// pass time.Now().UTC(), tests pass a fixed clock. Inside the 2-hour
// cutoff nobody is promoted; the leaders or hosts get one cutoff notice
// instead. A promoted row becomes going, keeps waitlisted_at as history,
// and gets a rotated manage token for the "I can't come" link.
func (a *App) promoteLocked(tx *gorm.DB, ev *models.Event, o *models.EventOccurrence, now time.Time, actor *uint) error {
	if o.Status != models.OccurrenceScheduled {
		// Nobody is promoted into a cancelled date.
		return nil
	}
	wl, err := loadWaitlist(tx, o.ID)
	if err != nil {
		return err
	}
	if len(wl) == 0 {
		return nil
	}
	// One waitlist, two pools: each entry keeps its mode and is filled
	// from its own pool in line order (M1.8 slice 2).
	tally, err := loadSeatTally(tx, ev, o)
	if err != nil {
		return err
	}
	// A greedy pass takes the first entry that fits an untouched pool, so if
	// none fits even before eligibility filters the line, nobody can be picked.
	line := make([]events.WaitEntry, 0, len(wl))
	for _, r := range wl {
		line = append(line, tally.entry(r))
	}
	if len(tally.pick(line)) == 0 {
		return nil
	}
	facts, err := a.loadEligibilityFacts(tx, wl, now, nil)
	if err != nil {
		return err
	}
	access := map[uint]bool{}
	entries := make([]events.WaitEntry, 0, len(wl))
	for _, r := range wl {
		ok, err := facts.eligible(a, tx, r, ev, access, now)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		entries = append(entries, tally.entry(r))
	}
	picks := tally.pick(entries)
	if events.InCutoff(now, o.StartsAt) {
		// Nobody is auto-promoted; the leaders or hosts are told only
		// when someone in line could actually take a freed seat.
		if len(picks) > 0 {
			return notifyWaitlistCutoff(tx, ev, o, actor)
		}
		return nil
	}
	if len(picks) == 0 {
		return nil
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
	rows []models.Registration, now time.Time, actor *uint) error {
	for _, r := range rows {
		raw, hash, err := createInvitationToken()
		if err != nil {
			return err
		}
		promoted, err := promoteRegistration(tx, r, hash, now)
		if err != nil {
			return err
		}
		if !promoted {
			continue // lost a race with another seat-changing write
		}
		next := r
		next.Status = models.RegGoing
		if err := auditRegistration(tx, actor, audit.RegistrationChanged, ev.ID, r, next, ""); err != nil {
			return err
		}
		if err := notifyRegistrationPromoted(tx, ev, o, r, a.emailBaseURL()+"/guest/manage/"+raw, actor); err != nil {
			return err
		}
	}
	return nil
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
	if ch.next.Status == models.RegWaitlisted {
		placeOnWaitlist(&ch.next, ch.old, now)
	}
	return ch, nil
}

// registerLocked allocates one final transition. Batch departures and
// promotion scheduling belong to the caller, never to this per-row function.
func (a *App) registerLocked(tx *gorm.DB, ev *models.Event, o *models.EventOccurrence, ch *registrationChange, now time.Time) error {
	r := &ch.next
	if r.Status != models.RegDeclined && remoteUnavailable(ev, r.AttendanceMode) {
		return errRemoteNotAvailable
	}
	if r.Status == models.RegGoing {
		if err := seatChange(tx, ev, o, ch.old, r, now); err != nil {
			return err
		}
	}
	return saveRegistration(tx, r)
}

// applyRegistrationBatch releases resources before filling new requests so
// existing waiters can use those releases first. Every write rolls back together.
func (a *App) applyRegistrationBatch(tx *gorm.DB, ev *models.Event, o *models.EventOccurrence, changes []registrationChange, now time.Time, actor *uint, ip string) error {
	released, editedWaiter := false, false
	for i := range changes {
		ch := &changes[i]
		if ch.old.Status == models.RegWaitlisted && ch.next.Status == models.RegWaitlisted {
			if err := saveRegistration(tx, &ch.next); err != nil {
				return err
			}
			editedWaiter = true
		}
		if ch.old.Status == models.RegWaitlisted && ch.next.Status != models.RegWaitlisted {
			interim := ch.next
			if interim.Status == models.RegGoing {
				interim.Status = models.RegDeclined
			}
			if err := saveRegistration(tx, &interim); err != nil {
				return err
			}
		}
		if ch.old.Status != models.RegGoing {
			continue
		}
		interim := ch.old
		changed := false
		switch {
		case ch.next.Status != models.RegGoing || ch.next.AttendanceMode != ch.old.AttendanceMode:
			interim.Status = models.RegDeclined
			changed = true
		default:
			if ch.next.GuestCount < interim.GuestCount {
				interim.GuestCount = ch.next.GuestCount
				changed = true
			}
			if effectiveChildcare(ev, ch.next) < effectiveChildcare(ev, interim) {
				interim.ChildcareCount, interim.ChildcareRequested = ch.next.ChildcareCount, ch.next.ChildcareRequested
				changed = true
			}
		}
		if !changed {
			continue // registerLocked writes the final row below
		}
		released = true
		if err := saveRegistration(tx, &interim); err != nil {
			return err
		}
	}
	if released || editedWaiter {
		if err := a.promoteLocked(tx, ev, o, now, nil); err != nil {
			return err
		}
	}
	for i := range changes {
		ch := &changes[i]
		if ch.old.Status == models.RegWaitlisted && ch.next.Status == models.RegWaitlisted {
			if err := tx.First(&ch.next, ch.next.ID).Error; err != nil {
				return err
			}
		} else if err := a.registerLocked(tx, ev, o, ch, now); err != nil {
			return err
		}
		action := ch.auditAction
		if action == "" {
			action = audit.RegistrationChanged
		}
		if err := auditRegistration(tx, actor, action, ev.ID, ch.old, ch.next, ip); err != nil {
			return err
		}
	}
	return nil
}

func auditRegistration(tx *gorm.DB, actor *uint, action string, eventID uint, old, next models.Registration, ip string) error {
	meta := registrationMeta(next)
	meta["oldStatus"], meta["newStatus"] = old.Status, next.Status
	meta["oldAttendanceMode"], meta["attendanceMode"] = old.AttendanceMode, next.AttendanceMode
	meta["oldGuestCount"], meta["guestCount"] = old.GuestCount, next.GuestCount
	meta["oldChildcareCount"], meta["childcareCount"] = old.ChildcareCount, next.ChildcareCount
	meta["oldChildcareRequested"], meta["childcareRequested"] = old.ChildcareRequested, next.ChildcareRequested
	return audit.Write(tx, audit.Entry{Actor: actor, Action: action, TargetType: "event", TargetID: eventID, Meta: meta, IP: ip})
}

// The keys of registrationMeta that the app reads back as domain state: the
// restore's dropped answers (appendRecoverableRegistrations,
// previouslyRegisteredPerson) are found only in the audit log. The writer keeps
// literal keys (the audit census pins them); TestRegistrationAuditKeysAreShared
// proves it writes every key the readers use, with the same names.
const (
	metaRegistrationID = "registrationId"
	metaUserID         = "userId"
	metaPersonID       = "personId"
	metaRegisteredBy   = "registeredBy"
	metaOccurrenceID   = "occurrenceId"
	metaAttendanceMode = "attendanceMode"
)

// droppedRegistrationMeta is registrationMeta as the restore's recovery reads it.
type droppedRegistrationMeta struct {
	RegistrationID uint   `json:"registrationId"`
	PersonID       *uint  `json:"personId"`
	UserID         *uint  `json:"userId"`
	RegisteredBy   *uint  `json:"registeredBy"`
	OccurrenceID   uint   `json:"occurrenceId"`
	AttendanceMode string `json:"attendanceMode"`
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

// householdRegistrationError is a policy refusal to sign a household person up
// (policy.HouseholdRegistrationProblem and CanRegisterHouseholdPerson return
// its code). Its answer is the 409 declared for the code, naming the person.
type householdRegistrationError struct {
	code     string
	personID uint
}

func (e householdRegistrationError) Error() string { return e.code }

func (e householdRegistrationError) apiAnswer() *apiError {
	base, ok := householdRegistrationRefusals[e.code]
	if !ok {
		base = &apiError{Status: http.StatusConflict, Code: e.code, Message: "household registration needs review"}
	}
	if e.personID == 0 {
		return base
	}
	return base.with(gin.H{"personId": e.personID})
}

// householdRegistrationRefusals are the answers for the codes policy returns.
var householdRegistrationRefusals = map[string]*apiError{
	"household_link_pending":                    errHouseholdLinkPending,
	"household_link_unconfirmed":                errHouseholdLinkUnconfirmed,
	"household_birth_date_required":             errHouseholdBirthDateRequired,
	"household_age_mismatch":                    errHouseholdAgeMismatch,
	"household_remote_childcare":                errHouseholdRemoteChildcare,
	"household_registration_holder_unavailable": errHouseholdRegistrationHolderUnavailable,
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
	mode := ""
	if body.AttendanceMode != nil {
		mode = *body.AttendanceMode
	}
	if !validMode(mode) {
		fail(c, errInvalidAttendanceMode)
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
	// A viewer who cannot register for the event gets the answer a missing
	// event gets, before any answer that depends on the event's settings or
	// dates (no oracle for hidden events).
	var o models.EventOccurrence
	if !isCalendarDate(body.Date) || a.DB.Where("event_id = ? AND original_date = ? AND status = ?", ev.ID, body.Date, models.OccurrenceScheduled).First(&o).Error != nil {
		if !a.canRegisterAnyDate(a.DB, u, ev) {
			jsonError(c, 404, "event not found")
			return
		}
		jsonError(c, 404, "that date is not on this event")
		return
	}
	if !a.canRegisterIn(a.DB, u, dateEvent(ev, &o)) {
		jsonError(c, 404, "event not found")
		return
	}
	care := childcareRequest{ChildcareCount: body.ChildcareCount, ChildcareAgesNote: body.ChildcareAgesNote}
	if !care.validate(c, ev) {
		return
	}
	guests := 0
	if body.GuestCount != nil {
		guests = *body.GuestCount
	}
	if guests < 0 || guests > ev.GuestsAllowed {
		fail(c, errTooManyGuests)
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
	response := registrationBatchJSON{People: []householdRegistrationJSON{}}
	gated := false
	mutate := func(tx *gorm.DB) error {
		// Read inside the closure: a gated retry runs it again, after lock waits.
		now := time.Now().UTC()
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
				if errors.Is(err, gorm.ErrRecordNotFound) {
					// The row is gone once its lock is granted: an erasure committed
					// while this request waited (on the row, or on the gate in the
					// gated repeat). Nothing has been written yet. The signed-in
					// account no longer exists, which is a signed-out session (401,
					// as apiAuth answers a deleted account's next request). Another
					// holder's account being gone is the unavailable-holder answer.
					if id == u.ID {
						return errSecuritySession
					}
					return householdRegistrationError{code: "household_registration_holder_unavailable"}
				}
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
		if gated && !sessionCurrent(&holder, u) {
			return errOccurrenceNotFound
		}
		var today time.Time
		if err := tx.First(ev, ev.ID).Error; err != nil {
			return err
		}
		// The holder, event and locked date are fixed from here, so the
		// answer is the same wherever it is asked.
		var registrableKnown, registrableOK bool
		registrable := func() bool {
			if !registrableKnown {
				registrableOK, registrableKnown = a.canRegisterIn(tx, &holder, dateEvent(ev, locked)), true
			}
			return registrableOK
		}
		if len(body.People) > 0 {
			if !registrable() {
				return errOccurrenceNotFound
			}
			var err error
			if today, err = churchClockIn(tx, now); err != nil {
				return err
			}
		}
		personIDs := make([]uint, 0, len(body.People))
		for _, answer := range body.People {
			personIDs = append(personIDs, answer.PersonID)
		}
		var existingRows []models.Registration
		if err := tx.Where("occurrence_id = ? AND (user_id = ? OR household_person_id IN ?)", o.ID, holder.ID, personIDs).Find(&existingRows).Error; err != nil {
			return err
		}
		var ownRow models.Registration
		personRows := map[uint]models.Registration{}
		hasInvitation := false
		for _, r := range existingRows {
			if r.UserID != nil && *r.UserID == holder.ID {
				ownRow = r
				hasInvitation = r.AccessLive()
			} else if r.HouseholdPersonID != nil {
				personRows[*r.HouseholdPersonID] = r
			}
		}
		if hasInvitation && !gated {
			return errOccurrenceGateRetry
		}
		if hasInvitation {
			live, err := liveOccurrenceInvitationIn(tx, ownRow, now)
			if err != nil {
				return err
			}
			hasInvitation = live
		}
		if !hasInvitation && !registrable() {
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
		facts := newSubjectFacts(today)
		if len(body.People) > 0 {
			if err := facts.load(tx, candidates, &holder); err != nil {
				return err
			}
		}
		access := map[uint]bool{}
		for _, r := range candidates {
			if r.HouseholdPersonID == nil {
				continue
			}
			ok, err := facts.eligible(a, tx, r, ev, access, now)
			if err != nil {
				return err
			}
			if !ok {
				return householdRegistrationError{code: "household_registration_holder_unavailable", personID: *r.HouseholdPersonID}
			}
		}
		err = a.applyRegistrationBatch(tx, ev, locked, changes, now, &holder.ID, c.ClientIP())
		if err != nil {
			return err
		}
		if hasInvitation && body.Status == models.RegDeclined {
			err := endRegistrationAccess(tx, &ownRow, accessScopeOf(ev), policy.OccurrenceDeclined, &holder.ID, now, c.ClientIP())
			if err != nil {
				return err
			}
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
	if answerStaleSession(c, err) {
		return
	}
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, response)
}

// canRegisterAnyDate says whether u could register for the event on the
// series' own visibility or on any scheduled date's override. Callers use it
// to tell "no such date on an event you can see" from "no such event".
func (a *App) canRegisterAnyDate(tx *gorm.DB, u *models.User, ev *models.Event) bool {
	if a.canRegisterIn(tx, u, ev) {
		return true
	}
	var overrides []string
	if tx.Model(&models.EventOccurrence{}).
		Where("event_id = ? AND status = ? AND visibility_override IS NOT NULL", ev.ID, models.OccurrenceScheduled).
		Distinct().Pluck("visibility_override", &overrides).Error != nil {
		return false
	}
	for i := range overrides {
		if a.canRegisterIn(tx, u, dateEvent(ev, &models.EventOccurrence{VisibilityOverride: &overrides[i]})) {
			return true
		}
	}
	return false
}

func (a *App) canRegisterIn(tx *gorm.DB, u *models.User, ev *models.Event) bool {
	// Host facts are irrelevant to visibility, and a failed read can only say no.
	f, err := eventFactsIn(tx, u, ev)
	if err != nil || (ev.GroupID != nil && f.group == nil) {
		return false
	}
	f.view = viewOf(ev, false)
	if u != nil && u.Status == models.UserGuest {
		return policy.CanRegisterGuest(f.view, asGroup(f.group))
	}
	return policy.CanRegisterEvent(asPerson(u), f.view, asGroup(f.group), f.rel)
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
		fail(c, errNoteTooLong)
		return
	}
	var o models.EventOccurrence
	if err := firstScheduledOn(a.DB, ev.ID, body.Date, &o); err != nil {
		jsonError(c, http.StatusNotFound, "that date is not on this event")
		return
	}
	me := apiUser(c)
	err := a.contactMutation(contactChange{EventID: ev.ID}, func(tx *gorm.DB) error {
		actor, err := peopleAddActor(tx, me)
		if err != nil {
			return err
		}
		if err := tx.First(ev, ev.ID).Error; err != nil {
			return err
		}
		facts, err := eventFactsIn(tx, actor, ev)
		if err != nil {
			return err
		}
		if !facts.canManage(actor) {
			return errPeopleAddForbidden
		}
		me = actor
		if err := lockRegistrationHolder(tx, body.RegistrationID, o.ID); err != nil {
			return err
		}
		now := time.Now().UTC()
		locked, err := lockOccurrence(tx, o.ID)
		if err != nil {
			return err
		}
		var r models.Registration
		if err := tx.Where("id = ? AND occurrence_id = ? AND status NOT IN ?",
			body.RegistrationID, locked.ID, []string{models.RegRemoved, models.RegCancelled}).First(&r).Error; err != nil {
			return errRegNotFound
		}
		hadGrant, priorStatus := r.AccessLive(), r.Status
		if hadGrant {
			account, err := occurrenceAccountIn(tx, me.ID, now)
			if err != nil {
				return err
			}
			live, err := liveOccurrenceInvitationIn(tx, r, now)
			if err != nil {
				return err
			}
			if !live || !policy.CanEndOccurrenceAccess(account, true, occurrenceAccessPolicy(r, accessScopeOf(ev))) {
				return errPeopleAddForbidden
			}
		}
		removed := r
		removed.Status, removed.RemovedBy, removed.RemovalNote = models.RegRemoved, &me.ID, note
		if err := saveRegistration(tx, &removed); err != nil {
			return err
		}
		meta := registrationMeta(r)
		if !hadGrant {
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
				err := finishContactRequest(tx, &pending[i], policy.ContactEnded, policy.OccurrenceRemoved, now)
				if err != nil {
					return err
				}
			}
		}
		if hadGrant {
			err = endRegistrationAccess(tx, &r, accessScopeOf(ev), policy.OccurrenceRemoved, &me.ID, now, c.ClientIP())
		} else {
			err = notifyRegistrationRemoved(tx, ev, locked, r, note, &me.ID)
		}
		if err != nil {
			return err
		}
		// Removing a going party retries promotion under the occurrence lock.
		// promoteLocked checks remaining attendance/childcare room and cutoff;
		// being at or over capacity after removal never seats another party.
		if priorStatus == models.RegGoing {
			err := a.promoteLocked(tx, ev, locked, now, &me.ID)
			if err != nil {
				return err
			}
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
	if err := firstScheduledOn(a.DB, ev.ID, body.Date, &o); err != nil {
		jsonError(c, http.StatusNotFound, "that date is not on this event")
		return
	}
	me := apiUser(c)
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
		facts, err := eventFactsIn(tx, actor, ev)
		if err != nil {
			return err
		}
		if !facts.canManage(actor) {
			return errPeopleAddForbidden
		}
		me = actor
		if err := lockRegistrationHolder(tx, body.RegistrationID, o.ID); err != nil {
			return err
		}
		now := time.Now().UTC()
		locked, err := lockOccurrence(tx, o.ID)
		if err != nil {
			return err
		}
		var r models.Registration
		if err := tx.Where("id = ? AND occurrence_id = ? AND status = ?",
			body.RegistrationID, locked.ID, models.RegWaitlisted).First(&r).Error; err != nil {
			return errRegNotFound
		}
		ok, err := a.eligibleRegistration(tx, r, ev, now)
		if err != nil {
			return err
		}
		if !ok {
			return householdRegistrationError{code: "household_registration_holder_unavailable"}
		}
		// A waitlisted entry promotes into its own pool (M1.8 slice 2).
		fits, _, _, err := poolFits(tx, ev, locked, r)
		if err != nil {
			return err
		}
		if !fits {
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
		err = a.promoteRows(tx, ev, locked, []models.Registration{r}, now, &me.ID)
		if err != nil {
			return err
		}
		return nil
	})
	switch {
	case errors.Is(err, errRegNotFound):
		jsonError(c, http.StatusNotFound, "no waitlisted registration for that person")
		return
	case errors.Is(err, errEventFull):
		fail(c, errPartyTooBig)
		return
	case err != nil:
		fail(c, err)
		return
	}
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
			a.managerDateAddRefusal(c, errInvalidAttendanceMode)
			return
		}
		a.addHouseholdRegistration(c, ev, body.Date, body.PersonID, body.RegisteredBy, mode, body.ChildcareRequested, body.OverCapacity != nil && *body.OverCapacity)
		return
	}
	if body.RegisteredBy != 0 || body.ChildcareRequested != nil {
		a.managerDateAddError(c, 400, "choose one registration target")
		return
	}
	if err := body.childcareRequest.problem(ev); err != nil {
		a.managerDateAddRefusal(c, err)
		return
	}
	addMode := ""
	if body.AttendanceMode != nil {
		addMode = strings.TrimSpace(*body.AttendanceMode)
		switch addMode {
		case models.AttendanceInPerson, models.AttendanceRemote:
		default:
			a.managerDateAddRefusal(c, errInvalidAttendanceMode)
			return
		}
		if remoteUnavailable(ev, addMode) {
			a.managerDateAddRefusal(c, errRemoteNotAvailable)
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
		fail(c, apiErrf(http.StatusBadRequest, "too_many_guests", "at most %d guests are allowed", ev.GuestsAllowed))
		return
	}
	var target models.User
	// A pruned guest keeps status=guest (never active); the add action is
	// their way back in, so both statuses resolve here.
	if body.SelectionID != "" {
		id, err := a.parsePeopleSelection(body.SelectionID, apiUser(c).ID, time.Now())
		if err != nil || a.DB.Where("id = ? AND status = ? AND account_type IN ?", id, models.UserActive, []string{models.AccountAdult, models.AccountTeen}).First(&target).Error != nil {
			a.managerDateAddRefusal(c, errPeopleSelectionUnavailable)
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
	if err := firstScheduledOn(a.DB, ev.ID, body.Date, &o); err != nil {
		a.managerDateAddError(c, http.StatusNotFound, "that date is not on this event")
		return
	}
	me := apiUser(c)
	overCap := body.OverCapacity != nil && *body.OverCapacity
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
		facts, err := eventFactsIn(tx, actor, ev)
		if err != nil {
			return err
		}
		if !facts.canManage(actor) {
			return errPeopleAddForbidden
		}
		me = actor
		allowed, err := managerDateAddAllowedIn(tx, actor.ID)
		if err != nil {
			return err
		}
		refuse := func(err error) error {
			businessErr = err
			return writeOccurrenceRefusal(tx, actor.ID, c.ClientIP(), operationManagerAdd)
		}
		if !allowed {
			return refuse(errDateAddThrottled)
		}
		if target.ID == actor.ID {
			return refuse(errPeopleAddForbidden)
		}
		var holder models.User
		if err := tx.Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).First(&holder, target.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				// Erased while this request waited for the contact gate; the lookup
				// above found them, the locked read does not.
				return errAddTargetGone
			}
			return err
		}
		if holder.Status != models.UserActive && holder.Status != models.UserGuest {
			return errAddTargetGone
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
		now := time.Now().UTC()
		// Recipient controls apply to every account-add sibling, even before
		// the final request/provenance stage switches ordinary adds over.
		f, err := a.loadOccurrenceAddFacts(tx, models.ContactRequest{Purpose: policy.RequestOccurrenceAdd, RequesterID: me.ID, SubjectUserID: holder.ID, ContextType: policy.ContactEvent, ContextID: ev.ID, OccurrenceID: &locked.ID}, now)
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
		registration, err := a.addAccountRegistrationLocked(tx, ev, locked, target.ID, me.ID, managerRegistrationOptions{mode: addMode, guests: guestCount, overCapacity: overCap, care: body.childcareRequest}, now, c.ClientIP())
		if errors.Is(err, errEventFull) || errors.Is(err, errChildcareFull) || errors.Is(err, errChildcareUnavailable) || errors.Is(err, errRemoteNotAvailable) {
			return refuse(err)
		}
		if err != nil {
			return err
		}
		if holder.Status == models.UserActive {
			err = a.recordManagerOccurrenceAdd(tx, ev, locked, registration, actor.ID, nil, false, now, c.ClientIP())
			if err != nil {
				return err
			}
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
	case errors.Is(err, errEventFull):
		fail(c, errEventFullNoWaitlist)
		return
	case err != nil:
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{})
}

// ---------------------------------------------------------------------------
// Disabled and re-enabled accounts (DEC-38, DEC-47, M1.5).
// ---------------------------------------------------------------------------

// promoteOnDisable frees every seat a disabled account held on future
// scheduled occurrences and promotes into them. The rows stay; they are
// hidden and uncounted while the account is disabled.
// notifyInsideCutoff is false for tombstone maintenance: deleted accounts
// already do not count, so revisiting their registrations must not announce
// another newly opened seat. Admin disable/delete keeps its usual notices.
func (a *App) promoteOnDisable(tx *gorm.DB, userID uint, notifyInsideCutoff bool) error {
	var occIDs []uint
	if err := tx.Model(&models.Registration{}).
		Joins("JOIN event_occurrences ON event_occurrences.id = registrations.occurrence_id").
		Where("(registrations.user_id = ? OR registrations.registered_by = ?) AND registrations.status = ? AND event_occurrences.status = ? AND event_occurrences.starts_at > ?",
			userID, userID, models.RegGoing, models.OccurrenceScheduled, time.Now().UTC()).
		Distinct().Order("registrations.occurrence_id").Pluck("registrations.occurrence_id", &occIDs).Error; err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, id := range occIDs {
		o, err := lockOccurrence(tx, id)
		if err != nil {
			return err
		}
		var ev models.Event
		if err := tx.First(&ev, o.EventID).Error; err != nil {
			return err
		}
		if !notifyInsideCutoff && events.InCutoff(now, o.StartsAt) {
			continue
		}
		if err := a.promoteLocked(tx, &ev, o, now, nil); err != nil {
			return err
		}
	}
	return nil
}

// restoreRegistrations checks future answers in occurrence/registration ID
// order while the holder is disabled. Accepted going rows accumulate usage
// across both seat pools and care; ineligible or non-fitting rows are
// audited and dropped. Eligible waiters retain their original place.
func (a *App) restoreRegistrations(tx *gorm.DB, userID uint, intendedStatus string, actor *uint, ip string, now time.Time) error {
	var holder models.User
	if err := tx.First(&holder, userID).Error; err != nil {
		return err
	}
	holder.Status = intendedStatus
	var regs []models.Registration
	if err := tx.Joins("JOIN event_occurrences ON event_occurrences.id = registrations.occurrence_id").Where("(registrations.user_id = ? OR registrations.registered_by = ?) AND registrations.status IN ? AND event_occurrences.status = ? AND event_occurrences.starts_at > ?", userID, userID, []string{models.RegGoing, models.RegWaitlisted}, models.OccurrenceScheduled, now).Order("registrations.occurrence_id, registrations.id").Find(&regs).Error; err != nil {
		return err
	}
	var currentID uint
	var o *models.EventOccurrence
	var ev models.Event
	var tally *seatTally
	var facts *subjectFacts
	var access map[uint]bool
	for _, r := range regs {
		if currentID != r.OccurrenceID {
			var err error
			o, err = lockOccurrence(tx, r.OccurrenceID)
			if err != nil {
				return err
			}
			currentID = r.OccurrenceID
			ev = models.Event{}
			if err := tx.First(&ev, o.EventID).Error; err != nil {
				return err
			}
			if tally, err = loadSeatTally(tx, &ev, o); err != nil {
				return err
			}
			currentRegs := []models.Registration{}
			for _, candidate := range regs {
				if candidate.OccurrenceID == o.ID {
					currentRegs = append(currentRegs, candidate)
				}
			}
			if facts, err = a.loadEligibilityFacts(tx, currentRegs, now, &holder); err != nil {
				return err
			}
			access = map[uint]bool{}
		}
		fits := true
		if r.HouseholdPersonID != nil {
			var err error
			if fits, err = facts.eligible(a, tx, r, &ev, access, now); err != nil {
				return err
			}
		} else {
			fits = a.canRegisterDate(tx, &holder, &ev, o)
		}
		if r.Status == models.RegGoing {
			fits = fits && tally.fits(r)
		}
		if fits {
			if r.Status == models.RegGoing {
				tally.take(r)
			}
			if err := auditRegistration(tx, actor, audit.RegistrationRestored, ev.ID, r, r, ip); err != nil {
				return err
			}
			continue
		}
		// A dropped row can carry a live exact-date grant. End it first, with an
		// explicit reason and the normal notice, so the delete trigger finds an
		// ended grant and the snapshots that outlive the row.
		if r.AccessOrigin != "" {
			if err := endRegistrationAccess(tx, &r, accessScopeOf(&ev), policy.OccurrenceContextChanged, nil, now, ip); err != nil {
				return err
			}
		}
		if err := tx.Delete(&r).Error; err != nil {
			return err
		}
		if err := auditRegistration(tx, actor, audit.RegistrationNotRestored, ev.ID, r, r, ip); err != nil {
			return err
		}
		if err := notifyRegistrationNotRestored(tx, &ev, o, r, actor); err != nil {
			return err
		}
	}
	return nil
}

// previouslyRegisteredPerson is whether holderID has registered personID for the
// date: a live row, or the audit row of an answer a restore dropped.
func previouslyRegisteredPerson(tx *gorm.DB, eventID, occurrenceID, personID, holderID uint) (bool, error) {
	var n int64
	if err := tx.Model(&models.Registration{}).Where("occurrence_id = ? AND household_person_id = ? AND registered_by = ?", occurrenceID, personID, holderID).Count(&n).Error; err != nil {
		return false, err
	}
	if n > 0 {
		return true, nil
	}
	err := tx.Model(&models.AuditLog{}).Where("action = ? AND target_type = 'event' AND target_id = ? AND "+
		"meta_json->>'"+metaPersonID+"' = ? AND meta_json->>'"+metaRegisteredBy+"' = ? AND meta_json->>'"+metaOccurrenceID+"' = ?",
		audit.RegistrationNotRestored, eventID, strconv.FormatUint(uint64(personID), 10), strconv.FormatUint(uint64(holderID), 10), strconv.FormatUint(uint64(occurrenceID), 10)).Count(&n).Error
	return n > 0, err
}

func (a *App) addHouseholdRegistration(c *gin.Context, ev *models.Event, date string, personID, holderID uint, mode string, namedCare *bool, overCapacity bool) {
	var o models.EventOccurrence
	if firstScheduledOn(a.DB, ev.ID, date, &o) != nil {
		a.managerDateAddError(c, 404, "that date is not on this event")
		return
	}
	actor := apiUser(c)
	now := time.Now().UTC()
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		// Like every manager add, serialize with account, leadership and host
		// revocation: the contact gate first, then the actor read again under it.
		if err := lockContacts(tx); err != nil {
			return err
		}
		fresh, err := peopleAddActor(tx, actor)
		if err != nil {
			return err
		}
		actor = fresh
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
		today, err := churchClockIn(tx, now)
		if err != nil {
			return err
		}
		facts := newSubjectFacts(today)
		if err := facts.load(tx, []models.Registration{subject.row(o.ID)}, &holder); err != nil {
			return err
		}
		previously, err := previouslyRegisteredPerson(tx, ev.ID, o.ID, personID, holderID)
		if err != nil {
			return err
		}
		f, err := eventFactsIn(tx, actor, ev)
		if err != nil {
			return err
		}
		acct, found := facts.accounts[holderID]
		manager := policy.HouseholdManager{Person: asPerson(actor), ManagesEvent: f.canManage(actor), PreviouslyRegistered: previously}
		holderFacts := householdHolderOf(&holder, acct, found, &person, a.canRegisterIn(tx, &holder, dateEvent(ev, locked)))
		if problem := policy.CanManageHouseholdRegistration(manager, holderFacts, householdFacts(&person, facts.links[personID], today)); problem != "" {
			return householdRegistrationError{code: problem}
		}
		var old models.Registration
		if err := subject.query(tx, o.ID).Limit(1).Find(&old).Error; err != nil {
			return err
		}
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
		// A manager's add carries no earlier link.
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
		if remoteUnavailable(ev, r.AttendanceMode) {
			return errRemoteNotAvailable
		}
		// An idempotent re-add of a row already going in this pool keeps its own
		// seat, as addAccountRegistrationLocked does.
		fits, _, _, err := poolFits(tx, ev, locked, r)
		if err != nil {
			return err
		}
		over := !fits
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
		if err := auditRegistration(tx, &actor.ID, action, ev.ID, old, r, c.ClientIP()); err != nil {
			return err
		}
		if old.Status == models.RegGoing && (attendanceModeOf(r.AttendanceMode) != attendanceModeOf(old.AttendanceMode) || effectiveChildcare(ev, r) < effectiveChildcare(ev, old)) {
			err = a.promoteLocked(tx, ev, locked, now, &actor.ID)
			return err
		}
		return nil
	})
	if err != nil {
		if auditErr := a.auditManagerDateRefusal(c); auditErr != nil {
			jsonError(c, 500, "could not add registration")
			return
		}
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{})
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
func (a *App) addAccountRegistrationLocked(tx *gorm.DB, ev *models.Event, locked *models.EventOccurrence, targetID, actor uint, options managerRegistrationOptions, now time.Time, ip string) (models.Registration, error) {
	if locked.Status != models.OccurrenceScheduled || locked.EventID != ev.ID {
		return models.Registration{}, errOccurrenceNotFound
	}
	if options.guests < 0 || options.guests > ev.GuestsAllowed {
		return models.Registration{}, errPeopleAddForbidden
	}

	var r models.Registration
	lookup := tx.Where("user_id = ? AND occurrence_id = ?", targetID, locked.ID).First(&r)
	if lookup.Error != nil && !errors.Is(lookup.Error, gorm.ErrRecordNotFound) {
		return models.Registration{}, lookup.Error
	}
	found := lookup.RowsAffected == 1
	before := r
	count, ages := options.care.values(r)
	if err := checkChildcare(tx, ev, locked.ID, r.ID, count); err != nil {
		return models.Registration{}, err
	}
	mode := models.AttendanceInPerson
	if found {
		mode = attendanceModeOf(r.AttendanceMode)
	}
	if options.mode != "" {
		mode = options.mode
	}
	if remoteUnavailable(ev, mode) {
		return models.Registration{}, errRemoteNotAvailable
	}
	if options.care.ChildcareCount != nil && count > 0 && !ev.ChildcareAvailable {
		return models.Registration{}, errChildcareUnavailable
	}
	var subject models.User
	if err := tx.First(&subject, targetID).Error; err != nil {
		return models.Registration{}, err
	}
	if options.approved != nil || policy.TeenAddNeedsApproval(asPerson(&subject)) || !a.canRegisterIn(tx, &subject, dateEvent(ev, locked)) {
		approved, err := a.verifiedOccurrenceAddIn(tx, options.approved, actor, targetID, ev.ID, locked.ID, policy.AddOptions{OverCapacity: options.overCapacity, AttendanceMode: mode, GuestCount: options.guests, ChildcareCount: count, ChildcareAgesNote: ages})
		if err != nil {
			return models.Registration{}, err
		}
		if !approved {
			return models.Registration{}, errPeopleAddForbidden
		}
	}

	// The add seats the person in their pool; over-capacity is
	// judged against that pool only (M1.8 slice 2).
	want := models.Registration{ID: r.ID, AttendanceMode: mode, GuestCount: options.guests}
	fits, used, cap, err := poolFits(tx, ev, locked, want)
	if err != nil {
		return models.Registration{}, err
	}
	seats := partySeats(want)
	over := !fits
	if over && !options.overCapacity {
		return models.Registration{}, errEventFull
	}
	after := r
	if !found {
		after = models.Registration{UserID: &targetID, OccurrenceID: locked.ID, Source: models.RegSourceAdmin}
	}
	after.Status, after.GuestCount, after.AttendanceMode = models.RegGoing, options.guests, mode
	after.ChildcareCount, after.ChildcareAgesNote = count, ages
	if err := saveRegistration(tx, &after); err != nil {
		return models.Registration{}, err
	}
	action := audit.RegistrationAdded
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
		return models.Registration{}, err
	}
	if found && before.Status == models.RegGoing && (mode != attendanceModeOf(before.AttendanceMode) || options.guests < before.GuestCount || count < before.ChildcareCount) {
		if err := a.promoteLocked(tx, ev, locked, now, &actor); err != nil {
			return models.Registration{}, err
		}
	}
	return after, nil
}
