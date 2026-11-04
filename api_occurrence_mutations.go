package main

import (
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"strconv"
	"time"
	"unicode/utf8"

	"fold/internal/audit"
	"fold/internal/notify"
	"fold/internal/policy"
	"fold/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Mutation facts are loaded through the caller's transaction AFTER the contact
// gate. The unlocked scopedOccurrenceReads helper is never mutation authority.
func occurrenceFactsIn(tx *gorm.DB, eventID, occurrenceID uint) (policy.OccurrenceFacts, error) {
	f := policy.OccurrenceFacts{ID: occurrenceID, EventID: eventID}
	var ev models.Event
	if err := tx.First(&ev, eventID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return f, nil
		}
		return f, err
	}
	f.ContextActive = true
	f.Organizer = policy.OrganizerScope{Type: policy.ContactEvent, ID: ev.ID}
	if ev.GroupID != nil {
		f.Organizer = policy.OrganizerScope{Type: policy.ContactGroup, ID: *ev.GroupID}
		var g models.Group
		if err := tx.First(&g, *ev.GroupID).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return f, err
			}
			f.ContextActive = false
		} else {
			f.ContextActive = g.Status == models.GroupActive
		}
	}
	var occ models.EventOccurrence
	if err := tx.Where("id = ? AND event_id = ?", occurrenceID, eventID).First(&occ).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return f, nil
		}
		return f, err
	}
	f.Scheduled = occ.Status == models.OccurrenceScheduled
	f.EndsAt = occ.EndsAt
	return f, nil
}

func occurrenceAccountIn(tx *gorm.DB, userID uint, now time.Time, zone *time.Location) (policy.ContactAccount, error) {
	u, err := contactUser(tx, userID)
	if err != nil {
		return policy.ContactAccount{}, err
	}
	account := contactAccount(u, now, zone)
	if u != nil && u.AccountType == models.AccountTeen {
		consent, err := latestTeenConsentIn(tx, u.ID, models.TeenConsentAccount)
		if err != nil {
			return account, err
		}
		account.Active = account.Active && consent
	}
	return account, nil
}

func (a *App) loadOccurrenceAddFacts(tx *gorm.DB, r models.ContactRequest, now time.Time) (policy.AddFacts, error) {
	f := policy.AddFacts{Now: now, ContextType: r.ContextType, ContextID: r.ContextID}
	if r.Purpose != policy.RequestOccurrenceAdd || r.ContextType != policy.ContactEvent || r.OccurrenceID == nil {
		return f, nil
	}
	zone, err := contactZone(tx)
	if err != nil {
		return f, err
	}
	if f.Requester, err = occurrenceAccountIn(tx, r.RequesterID, now, zone); err != nil {
		return f, err
	}
	if f.Subject, err = occurrenceAccountIn(tx, r.SubjectUserID, now, zone); err != nil {
		return f, err
	}
	if f.Occurrence, err = occurrenceFactsIn(tx, r.ContextID, *r.OccurrenceID); err != nil {
		return f, err
	}
	f.ContextActive = f.Occurrence.ContextActive
	var ev models.Event
	if err := tx.First(&ev, r.ContextID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return f, nil
		}
		return f, err
	}
	requester, err := contactUser(tx, r.RequesterID)
	if err != nil {
		return f, err
	}
	subject, err := contactUser(tx, r.SubjectUserID)
	if err != nil {
		return f, err
	}
	facts := a.factsForIn(tx, requester, &ev)
	f.Manager = policy.CanManageEvent(asPerson(requester), facts.view, facts.rel)
	f.OrdinaryEligible = a.canRegisterDateIn(tx, subject, &ev, *r.OccurrenceID)
	var block models.ManagerAddBlock
	if err := tx.Where("subject_user_id = ? AND organizer_type = ? AND organizer_id = ?", r.SubjectUserID, f.Occurrence.Organizer.Type, f.Occurrence.Organizer.ID).Limit(1).Find(&block).Error; err != nil {
		return f, err
	}
	if block.ID != 0 {
		f.Block = &policy.ManagerAddBlock{SubjectUserID: block.SubjectUserID, Organizer: f.Occurrence.Organizer}
	}
	// The live test reads the subject's current registration. The latest end
	// also reads the validated audit snapshots across every registration that
	// ever held a grant for this date, so a deleted row cannot hide a repeat.
	// A corrupt snapshot anywhere fails closed before any filtering.
	var row models.Registration
	if err := tx.Where("user_id = ? AND occurrence_id = ?", r.SubjectUserID, *r.OccurrenceID).Limit(1).Find(&row).Error; err != nil {
		return f, err
	}
	history, err := loadAccessHistory(tx, []uint{r.SubjectUserID}, []uint{*r.OccurrenceID})
	if err != nil {
		return f, err
	}
	f.ExistingAccessActive, f.LatestAccessEndedAt = accessAddFacts(row, history[accessKey{r.SubjectUserID, *r.OccurrenceID}])
	return f, nil
}

func (a *App) reconcileOccurrenceAsk(tx *gorm.DB, r *models.ContactRequest, now time.Time) ([]uint, error) {
	if r.Status != policy.ContactPending {
		return nil, nil
	}
	if policy.AddRequestExpired(addRequestPolicy(*r), now) {
		return finishContactRequest(tx, r, policy.ContactExpired, "", now)
	}
	f, err := a.loadOccurrenceAddFacts(tx, *r, now)
	if err != nil {
		return nil, err
	}
	if reason := policy.AddIneligibility(addRequestPolicy(*r).AddScope, f, r.NeedsAdmin); reason != "" {
		return finishContactRequest(tx, r, policy.ContactEnded, reason, now)
	}
	return nil, nil
}

// Accepted attendance deliberately does not reload issuer authority or blocks.
// Only the recipient and the exact live domain/provenance rows can end it.
func (a *App) reconcileOccurrenceAccesses(tx *gorm.DB, now time.Time, change contactChange) ([]uint, error) {
	q := tx.Where("registrations.user_id IS NOT NULL AND " + registrationAccessLiveSQL)
	switch {
	case change.UserID != 0:
		q = q.Where("registrations.user_id = ?", change.UserID)
	case change.EventID != 0:
		q = q.Where("registrations.occurrence_id IN (SELECT o.id FROM event_occurrences o WHERE o.event_id = ?)", change.EventID)
	case change.GroupID != 0:
		q = q.Where("registrations.occurrence_id IN (SELECT o.id FROM event_occurrences o JOIN events e ON e.id = o.event_id WHERE e.group_id = ?)", change.GroupID)
	}
	var rows []models.Registration
	if err := q.Clauses(clause.Locking{Strength: "UPDATE"}).Order("registrations.id").Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	facts, err := loadOccurrenceInvitationFacts(tx, rows, now)
	if err != nil {
		return nil, err
	}
	occurrenceIDs := make([]uint, 0, len(rows))
	for _, row := range rows {
		occurrenceIDs = append(occurrenceIDs, row.OccurrenceID)
	}
	scopes, err := accessScopesIn(tx, occurrenceIDs)
	if err != nil {
		return nil, err
	}
	var mailed []uint
	for i := range rows {
		row := &rows[i]
		reason := facts.endReason(*row, now)
		if reason == "" {
			continue
		}
		ids, err := endOccurrenceAccessIn(tx, row, scopes[row.OccurrenceID], reason, nil, now, "")
		if err != nil {
			return nil, err
		}
		mailed = append(mailed, ids...)
	}
	return mailed, nil
}

func endOccurrenceAccessIn(tx *gorm.DB, row *models.Registration, scope accessScope, reason string, actor *uint, now time.Time, ip string) ([]uint, error) {
	return endRegistrationAccess(tx, row, scope, reason, actor, now, ip, true)
}

// endOccurrenceAccessWithoutNoticeIn is for a larger summarized operation,
// such as group departure. It preserves the irreversible end stamp and its audit
// snapshot without generating one recipient notice per date.
func endOccurrenceAccessWithoutNoticeIn(tx *gorm.DB, row *models.Registration, scope accessScope, reason string, actor *uint, now time.Time, ip string) error {
	_, err := endRegistrationAccess(tx, row, scope, reason, actor, now, ip, false)
	return err
}

// liveOccurrenceInvitationIn is transaction authority only while the contact
// gate and the relevant holder/occurrence locks are held by the caller. The
// registration carries the provenance; its answer must still be a current one.
func liveOccurrenceInvitationIn(tx *gorm.DB, row models.Registration, now time.Time) (bool, error) {
	if row.UserID == nil || !row.AccessLive() {
		return false, nil
	}
	scope, err := accessScopeIn(tx, row.OccurrenceID)
	if err != nil {
		return false, err
	}
	account, err := occurrenceAccountIn(tx, *row.UserID, now, time.UTC)
	if err != nil {
		return false, err
	}
	facts, err := occurrenceFactsIn(tx, scope.EventID, row.OccurrenceID)
	if err != nil {
		return false, err
	}
	if !policy.OccurrenceAccessCurrent(account, occurrenceAccessPolicy(row, scope), facts, now) {
		return false, nil
	}
	var n int64
	err = tx.Model(&models.Registration{}).Where("id = ? AND occurrence_id = ? AND user_id = ? AND status IN ? AND "+registrationAccessLiveSQL, row.ID, row.OccurrenceID, *row.UserID, []string{models.RegGoing, models.RegMaybe, models.RegWaitlisted}).Count(&n).Error
	return n == 1, err
}

func (a *App) apiDeclineOccurrence(c *gin.Context)  { a.mutateOwnOccurrence(c, "decline") }
func (a *App) apiBlockOccurrence(c *gin.Context)    { a.mutateOwnOccurrence(c, "block") }
func (a *App) apiRegisterOccurrence(c *gin.Context) { a.mutateOwnOccurrence(c, "registration") }

func (a *App) mutateOwnOccurrence(c *gin.Context, operation string) {
	eventID, parseErr := strconv.ParseUint(c.Param("id"), 10, 64)
	date := c.Param("date")
	_, dateErr := models.ParseCalendarDate(date)
	var input struct {
		Status            string  `json:"status"`
		GuestCount        *int    `json:"guestCount"`
		AttendanceMode    *string `json:"attendanceMode"`
		ChildcareCount    *int    `json:"childcareCount"`
		ChildcareAgesNote *string `json:"childcareAgesNote"`
	}
	valid := parseErr == nil && eventID > 0 && dateErr == nil
	if operation == "registration" {
		decoder := json.NewDecoder(c.Request.Body)
		decoder.DisallowUnknownFields()
		valid = valid && decoder.Decode(&input) == nil && validAnswer(input.Status)
	}
	var result registrationJSON
	refused := false
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := lockContacts(tx); err != nil {
			return err
		}
		now := time.Now().UTC()
		refuse := func() error {
			refused = true
			return audit.Write(tx, audit.Entry{Actor: &apiUser(c).ID, Action: audit.OccurrenceRefused, TargetType: "occurrence_endpoint", IP: c.ClientIP(), Meta: map[string]any{"operation": operation}})
		}
		if !valid {
			return refuse()
		}
		var actor models.User
		if err := tx.Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).Where("id = ?", apiUser(c).ID).First(&actor).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return refuse()
			}
			return err
		}
		if !actor.CanSignIn() || actor.SessionVersion != apiUser(c).SessionVersion {
			return refuse()
		}
		var occ models.EventOccurrence
		if err := tx.Where("event_id = ? AND original_date = ?", eventID, date).First(&occ).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return refuse()
			}
			return err
		}
		locked, err := lockOccurrence(tx, occ.ID)
		if err != nil {
			return err
		}
		var reg models.Registration
		if err := tx.Where("registrations.user_id = ? AND registrations.occurrence_id = ? AND "+registrationAccessLiveSQL, actor.ID, locked.ID).First(&reg).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return refuse()
			}
			return err
		}
		live, err := liveOccurrenceInvitationIn(tx, reg, now)
		if err != nil {
			return err
		}
		account, err := occurrenceAccountIn(tx, actor.ID, now, time.UTC)
		if err != nil {
			return err
		}
		scope, err := accessScopeIn(tx, locked.ID)
		if err != nil {
			return err
		}
		if !live || !policy.CanEndOccurrenceAccess(account, false, occurrenceAccessPolicy(reg, scope)) {
			return refuse()
		}
		var ev models.Event
		if err := tx.First(&ev, eventID).Error; err != nil {
			return err
		}
		want, reason := input.Status, ""
		if operation == "decline" || operation == "block" || input.Status == models.RegDeclined {
			want, reason = models.RegDeclined, policy.OccurrenceDeclined
		}
		if operation == "block" {
			if !policy.CanBlockManagerAdds(account, occurrenceAccessPolicy(reg, scope)) {
				return refuse()
			}
			block := models.ManagerAddBlock{SubjectUserID: actor.ID, OrganizerType: scope.Organizer.Type, OrganizerID: scope.Organizer.ID}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&block).Error; err != nil {
				return err
			}
			if err := writeAccessSnapshot(tx, audit.OccurrenceBlocked, &actor.ID, reg, scope, false, c.ClientIP()); err != nil {
				return err
			}
			reason = policy.OccurrenceBlocked
		}
		guests, mode := reg.GuestCount, attendanceModeOf(reg.AttendanceMode)
		if input.GuestCount != nil {
			guests = *input.GuestCount
		}
		if input.AttendanceMode != nil {
			mode = *input.AttendanceMode
		}
		if reason == "" && (guests < 0 || guests > ev.GuestsAllowed || !validMode(mode) || mode == "") {
			return refuse()
		}
		care := childcareRequest{ChildcareCount: input.ChildcareCount, ChildcareAgesNote: input.ChildcareAgesNote}
		count, ages := care.values(reg)
		if reason == "" && (count < 0 || count > 20 || utf8.RuneCountInString(ages) > 200 || (input.ChildcareCount != nil && count > 0 && !ev.ChildcareAvailable)) {
			return refuse()
		}
		change, err := prepareRegistration(reg, locked, userSubject(actor.ID), want, guests, mode, care, nil, now)
		if err != nil {
			return err
		}
		_, err = a.applyRegistrationBatch(tx, &ev, locked, []registrationChange{change}, now, actor.ID, c.ClientIP())
		if err != nil {
			return err
		}
		if reason != "" {
			_, err = endOccurrenceAccessIn(tx, &reg, scope, reason, &actor.ID, now, c.ClientIP())
			if err != nil {
				return err
			}
		}
		if operation == "block" {
			change := contactChange{EventID: scope.EventID}
			if scope.Organizer.Type == policy.ContactGroup {
				change = contactChange{GroupID: scope.Organizer.ID}
			}
			_, err = a.reconcileContacts(tx, now, change)
			if err != nil {
				return err
			}
		}
		if err := tx.First(&reg, reg.ID).Error; err != nil {
			return err
		}
		result = registrationJSON{RegistrationID: reg.ID, UserID: actor.ID, EventID: ev.ID, OccurrenceID: locked.ID, Date: models.FormatCalendarDate(locked.OriginalDate), Status: reg.Status, GuestCount: reg.GuestCount, AttendanceMode: reg.AttendanceMode, ChildcareCount: reg.ChildcareCount, ChildcareAgesNote: reg.ChildcareAgesNote}
		if reg.Status == models.RegWaitlisted {
			pos, err := waitlistPosition(tx, locked.ID, reg.ID)
			if err != nil {
				return err
			}
			result.WaitlistPosition = &pos
		}
		return nil
	})
	if err != nil {
		auditErr := a.DB.Transaction(func(tx *gorm.DB) error {
			if err := lockContacts(tx); err != nil {
				return err
			}
			return audit.Write(tx, audit.Entry{Actor: &apiUser(c).ID, Action: audit.OccurrenceRefused, TargetType: "occurrence_endpoint", IP: c.ClientIP(), Meta: map[string]any{"operation": operation}})
		})
		if auditErr != nil {
			jsonError(c, 500, "could not save registration")
			return
		}
		registrationError(c, err)
		return
	}
	if refused {
		jsonError(c, 404, "event not found")
		return
	}
	if operation == "registration" {
		c.JSON(200, gin.H{"registration": result})
		return
	}
	c.JSON(200, gin.H{})
}

// Only an internal request finalizer may supply this proof. It is never decoded
// from HTTP. The registration core reloads purpose, exact scope, immutable final
// options, approvals, expiry and current eligibility before using it.
type verifiedOccurrenceAdd struct {
	requestID uint
}

func (a *App) verifiedOccurrenceAddIn(tx *gorm.DB, proof *verifiedOccurrenceAdd, requester, subject, eventID, occurrenceID uint, options policy.AddOptions) (bool, error) {
	if proof == nil {
		return false, nil
	}
	var r models.ContactRequest
	err := tx.Where("id = ? AND purpose = ? AND requester_id = ? AND subject_user_id = ? AND context_id = ? AND occurrence_id = ?", proof.requestID, policy.RequestOccurrenceAdd, requester, subject, eventID, occurrenceID).First(&r).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	request := addRequestPolicy(r)
	if request.Options != options {
		return false, nil
	}
	facts, err := a.loadOccurrenceAddFacts(tx, r, time.Now().UTC())
	if err != nil {
		return false, err
	}
	return policy.CanFinalizeAddRequest(request, facts), nil
}

const managerDateAddLimit = 30

var errDateAddApproval = errors.New("adding this person needs approval; send an ask to add for this date")
var errDateAddThrottled = errors.New("too many date additions; wait a minute before trying again")

func managerDateAddAllowedIn(tx *gorm.DB, actor uint) (bool, error) {
	var count int
	err := tx.Raw(`INSERT INTO login_throttles (key, failures, window_start) VALUES (?, 1, now())
ON CONFLICT (key) DO UPDATE SET
 failures = CASE WHEN login_throttles.window_start < now() - interval '1 minute' THEN 1 ELSE login_throttles.failures + 1 END,
 window_start = CASE WHEN login_throttles.window_start < now() - interval '1 minute' THEN now() ELSE login_throttles.window_start END
RETURNING failures`, "manager-date-add:"+strconv.FormatUint(uint64(actor), 10)).Scan(&count).Error
	return count <= managerDateAddLimit, err
}

func refuseManagerDateLimit(tx *gorm.DB, op *contactOperation, r models.ContactRequest, ip string) error {
	op.refused, op.refusalStatus, op.refusalMessage = true, 409, errDateAddThrottled.Error()
	return contactAudit(tx, &op.actor.ID, audit.ContactRefused, r, "rate_limit", ip)
}

func (a *App) loadAddFacts(tx *gorm.DB, r models.ContactRequest, now time.Time) (policy.AddFacts, error) {
	if r.Purpose == policy.RequestOccurrenceAdd {
		return a.loadOccurrenceAddFacts(tx, r, now)
	}
	return a.loadGroupAddFacts(tx, r, now)
}

func (a *App) completeAddAsk(tx *gorm.DB, op *contactOperation, r *models.ContactRequest, ip string) error {
	if r.Purpose != policy.RequestOccurrenceAdd {
		return a.completeGroupAsk(tx, op, r, ip)
	}
	facts, err := a.loadOccurrenceAddFacts(tx, *r, op.now)
	if err != nil {
		return err
	}
	if !policy.CanFinalizeAddRequest(addRequestPolicy(*r), facts) {
		return nil
	}
	allowed, err := managerDateAddAllowedIn(tx, r.RequesterID)
	if err != nil {
		return err
	}
	if !allowed {
		op.completionProblem = "throttled"
		return contactAudit(tx, &op.actor.ID, audit.ContactRefused, *r, "rate_limit", ip)
	}
	var holder models.User
	if err := tx.Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).First(&holder, r.SubjectUserID).Error; err != nil {
		return err
	}
	locked, err := lockOccurrence(tx, *r.OccurrenceID)
	if err != nil {
		return err
	}
	var ev models.Event
	if err := tx.First(&ev, r.ContextID).Error; err != nil {
		return err
	}
	// The domain locks may have waited across expiry or a birthday. Refresh
	// completion time and eligibility here, not just when the gate was taken.
	op.now = time.Now().UTC().Truncate(time.Microsecond)
	if _, err := a.reconcileOccurrenceAsk(tx, r, op.now); err != nil {
		return err
	}
	facts, err = a.loadOccurrenceAddFacts(tx, *r, op.now)
	if err != nil {
		return err
	}
	if !policy.CanFinalizeAddRequest(addRequestPolicy(*r), facts) {
		return nil
	}
	// All refusal paths below are before registration writes. Capacity/childcare
	// never require rolling back a valid party decision or a nested transaction.
	if r.AddGuestCount > ev.GuestsAllowed || (r.AddAttendanceMode == models.AttendanceRemote && !ev.RemoteAvailable) || (r.AddChildcareCount > 0 && !ev.ChildcareAvailable) {
		op.completionProblem = "options"
		return contactAudit(tx, &op.actor.ID, audit.ContactRefused, *r, "options_changed", ip)
	}
	count, ages := r.AddChildcareCount, r.AddChildcareAgesNote
	registration, _, err := a.addAccountRegistrationLocked(tx, &ev, locked, r.SubjectUserID, r.RequesterID,
		managerRegistrationOptions{approved: &verifiedOccurrenceAdd{requestID: r.ID}, mode: r.AddAttendanceMode, guests: r.AddGuestCount, overCapacity: r.AddOverCapacity, care: childcareRequest{ChildcareCount: &count, ChildcareAgesNote: &ages}}, op.now, ip)
	if errors.Is(err, errEventFull) || errors.Is(err, errChildcareFull) {
		op.completionProblem = "capacity"
		if errors.Is(err, errChildcareFull) {
			op.completionProblem = "childcare"
		}
		return contactAudit(tx, &op.actor.ID, audit.ContactRefused, *r, op.completionProblem, ip)
	}
	if err != nil {
		return err
	}
	r.Status, r.CompletedAt, r.CompletedTargetID = policy.ContactGranted, &op.now, &registration.ID
	if err := tx.Save(r).Error; err != nil {
		return err
	}
	if _, err := a.recordManagerOccurrenceAdd(tx, &ev, locked, registration, r.RequesterID, &r.ID, r.NeedsAdmin, op.now, ip); err != nil {
		return err
	}
	if err := contactAudit(tx, &op.actor.ID, audit.ContactGranted, *r, "", ip); err != nil {
		return err
	}
	if err := resolveContactTasks(tx, *r, &op.actor.ID, r.Status); err != nil {
		return err
	}
	_, err = contactNotice(tx, *r, notify.ContactUpdated, []uint{r.RequesterID}, "completed")
	return err
}

func (a *App) recordManagerOccurrenceAdd(tx *gorm.DB, ev *models.Event, occ *models.EventOccurrence, reg models.Registration, issuer uint, request *uint, teen bool, now time.Time, ip string) ([]uint, error) {
	if reg.UserID == nil {
		return nil, nil
	}
	// The grant is a write to the registration row that already seats the
	// person, so a live grant is the same "already added" answer as before: no
	// second grant, no second notice. An ended grant is replaced, and the
	// trigger decides whether this add may do so.
	row, granted, err := grantRegistrationAccess(tx, reg.ID, accessScopeOf(ev), accessGrant{Origin: policy.OccurrenceOriginManagerAdd, IssuerID: &issuer, RequestID: request}, &issuer, now, ip)
	if err != nil || !granted {
		return nil, err
	}
	// A new account invitation is fresh authority, not a revival of a
	// previous promotion link. Existing invitations and guest/proxy rows
	// never pass this new-provenance boundary.
	if err := tx.Model(&models.Registration{}).Where("id = ? AND user_id = ?", row.ID, *row.UserID).Update("manage_token_hash", "").Error; err != nil {
		return nil, err
	}
	recipients := []uint{*row.UserID}
	if teen {
		guardians, err := contactGuardians(tx, *row.UserID)
		if err != nil {
			return nil, err
		}
		recipients = append(recipients, guardians...)
	}
	return notify.Send(tx, notify.Msg{Kind: notify.OccurrenceAdded, To: recipients, SubjectType: accessTargetType, SubjectID: row.ID, Link: occurrenceDatePath(ev.ID, models.FormatCalendarDate(occ.OriginalDate)), DedupeKey: accessAddedKey(row)})
}

// occurrenceNoticeAccesses maps date -> person -> that person's live grant row,
// so a notice about the date can say "invited" instead of the ordinary wording.
func occurrenceNoticeAccesses(tx *gorm.DB, occurrenceIDs []uint) (map[uint]map[uint]models.Registration, error) {
	var rows []models.Registration
	if err := tx.Where("registrations.occurrence_id IN ? AND registrations.user_id IS NOT NULL AND "+registrationAccessLiveSQL, occurrenceIDs).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := map[uint]map[uint]models.Registration{}
	for _, row := range rows {
		if out[row.OccurrenceID] == nil {
			out[row.OccurrenceID] = map[uint]models.Registration{}
		}
		out[row.OccurrenceID][*row.UserID] = row
	}
	return out, nil
}

// sendOccurrenceNotice tells one access holder. A fan-out passes the scope it
// locked its recipients with (notify.WithRecipients); a single send passes nil.
func sendOccurrenceNotice(tx *gorm.DB, row models.Registration, occ *models.EventOccurrence, kind, key string, scope *notify.Scope) ([]uint, error) {
	return notify.Send(tx, notify.Msg{Kind: kind, To: []uint{*row.UserID}, SubjectType: accessTargetType, SubjectID: row.ID, Link: occurrenceDatePath(occ.EventID, models.FormatCalendarDate(occ.OriginalDate)), DedupeKey: key, Scope: scope})
}

// Unlike the general manager loader, this entry point must audit every
// refusal before writing its response, including hidden/missing destinations.
func (a *App) loadManagedDateAddEvent(c *gin.Context) (*models.Event, bool) {
	id, _ := strconv.Atoi(c.Param("id"))
	var ev models.Event
	if err := a.DB.First(&ev, id).Error; err != nil {
		a.managerDateAddError(c, 404, "event not found")
		return nil, false
	}
	f := a.factsFor(apiUser(c), &ev)
	gp := policy.Group{}
	if f.group != nil {
		gp = asGroup(f.group)
	}
	if !policy.CanSeeEvent(asPerson(apiUser(c)), f.view, gp, f.rel) {
		a.managerDateAddError(c, 404, "event not found")
		return nil, false
	}
	if !policy.CanManageEvent(asPerson(apiUser(c)), f.view, f.rel) {
		a.managerDateAddError(c, 403, "you can't change this event")
		return nil, false
	}
	return &ev, true
}

func (a *App) managerDateAddError(c *gin.Context, status int, message string) {
	if err := a.auditManagerDateRefusal(c); err != nil {
		jsonError(c, 500, "could not add registration")
		return
	}
	jsonError(c, status, message)
}
func (a *App) auditManagerDateRefusal(c *gin.Context) error {
	return a.DB.Transaction(func(tx *gorm.DB) error {
		if err := lockContacts(tx); err != nil {
			return err
		}
		return audit.Write(tx, audit.Entry{Actor: &apiUser(c).ID, Action: audit.OccurrenceRefused, TargetType: "occurrence_endpoint", IP: c.ClientIP(), Meta: map[string]any{"operation": "manager_add"}})
	})
}
