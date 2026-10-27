package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"fold/internal/accounts"
	"fold/internal/audit"
	"fold/internal/notify"
	"fold/internal/policy"
	"fold/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Contact operations use one deliberately coarse transaction advisory gate.
// Every contact-affecting domain transaction takes it FIRST, before its existing
// user/group/occurrence/notification locks. Contact code then locks requests by
// ID, then grants by ID. Contact-only operations never take domain row locks;
// group ask completion then keeps the existing group-first domain lock prefix.
// The gate stays held
// through audit, task resolution and commit (including reveal). This preserves
// both group-first membership and user-first deletion/consent lock prefixes.
const contactLockID int64 = 0x464f4c44434f4e

func lockContacts(tx *gorm.DB) error {
	return tx.Exec("SELECT pg_advisory_xact_lock(?)", contactLockID).Error
}

type contactChange struct {
	UserID  uint
	EventID uint
	GroupID uint
}

// contactMutation retains the caller's transaction boundary, and ends access
// using its own writes before commit. A failed audit rolls back the domain
// mutation too. Notifications are delivered only after the joint commit.
func (a *App) contactMutation(change contactChange, fn func(*gorm.DB) error) error {
	var mailed []uint
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := lockContacts(tx); err != nil {
			return err
		}
		// Event edits can move a retained departure date after its old end.
		// Persist expiry under the shared gate before the event/occurrence rows
		// change, so a move cannot resurrect authority that already elapsed.
		if change.EventID != 0 {
			ids, err := a.reconcileOccurrenceAccesses(tx, time.Now().UTC(), change)
			if err != nil {
				return err
			}
			mailed = append(mailed, ids...)
		}
		if err := fn(tx); err != nil {
			return err
		}
		ids, err := a.reconcileContacts(tx, time.Now().UTC(), change)
		mailed = append(mailed, ids...)
		return err
	})
	if err == nil {
		a.deliverNotificationEmails(mailed)
	}
	return err
}

func contactScope(r models.ContactRequest) policy.ContactScope {
	if r.Purpose != policy.RequestContact {
		return policy.ContactScope{}
	}
	return policy.ContactScope{RequesterID: r.RequesterID, SubjectUserID: r.SubjectUserID, ContextType: r.ContextType, ContextID: r.ContextID, Fields: r.Fields}
}
func contactRequestPolicy(r models.ContactRequest) policy.ContactRequest {
	if r.Purpose != policy.RequestContact {
		return policy.ContactRequest{}
	}
	return policy.ContactRequest{ContactScope: contactScope(r), NeedsAdmin: r.NeedsAdmin, SubjectDecision: r.SubjectDecision, AdminDecision: r.AdminDecision, Status: r.Status, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt}
}
func contactGrantPolicy(g models.ContactGrant, r models.ContactRequest) policy.ContactGrant {
	if r.Purpose != policy.RequestContact {
		return policy.ContactGrant{}
	}
	return policy.ContactGrant{ContactScope: contactScope(r), RequestID: g.RequestID, NeedsAdmin: r.NeedsAdmin, GrantedAt: g.GrantedAt, EndedAt: g.EndedAt}
}
func contactAccount(u *models.User, now time.Time, zone *time.Location) policy.ContactAccount {
	if u == nil {
		return policy.ContactAccount{}
	}
	out := policy.ContactAccount{Person: asPerson(u), Active: !u.DeletedAt.Valid && u.CanSignIn(), HideFromLeaders: u.HideContactFromLeaders}
	if u.BirthDate != nil {
		age := accounts.AgeOn(*u.BirthDate, now.In(zone))
		out.AgeYears = &age
	}
	return out
}
func contactZone(tx *gorm.DB) (*time.Location, error) {
	var setting models.Setting
	err := tx.Where("key = ?", settingTimezone).First(&setting).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return time.UTC, nil
	}
	if err != nil {
		return nil, err
	}
	return time.LoadLocation(setting.Value)
}

func contactUser(tx *gorm.DB, id uint) (*models.User, error) {
	var u models.User
	err := tx.First(&u, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &u, err
}

// loadContactFacts must run under the contact gate, after lock acquisition's
// clock. Every query error fails closed rather than becoming an eligibility fact.
func (a *App) loadContactFacts(tx *gorm.DB, s policy.ContactScope, now time.Time) (policy.ContactAccessFacts, *models.User, error) {
	f := policy.ContactAccessFacts{Now: now, Context: policy.ContactContext{Type: s.ContextType, ID: s.ContextID}}
	requester, err := contactUser(tx, s.RequesterID)
	if err != nil {
		return f, nil, err
	}
	subject, err := contactUser(tx, s.SubjectUserID)
	if err != nil {
		return f, nil, err
	}
	zone, err := contactZone(tx)
	if err != nil {
		return f, nil, err
	}
	f.Requester, f.Subject = contactAccount(requester, now, zone), contactAccount(subject, now, zone)
	switch s.ContextType {
	case policy.ContactGroup:
		var g models.Group
		err = tx.First(&g, s.ContextID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return f, subject, nil
		}
		if err != nil {
			return f, nil, err
		}
		f.Context.Active = g.Status == models.GroupActive
		var ms []models.Membership
		if err = tx.Where("group_id = ? AND user_id IN ?", g.ID, []uint{s.RequesterID, s.SubjectUserID}).Find(&ms).Error; err != nil {
			return f, nil, err
		}
		for _, m := range ms {
			rel := membershipRel(m)
			if m.UserID == s.RequesterID {
				f.Context.GroupLeader = rel.Leader
			}
			if m.UserID == s.SubjectUserID {
				f.Context.GroupMember = rel.Member
			}
		}
	case policy.ContactEvent:
		var ev models.Event
		err = tx.First(&ev, s.ContextID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return f, subject, nil
		}
		if err != nil {
			return f, nil, err
		}
		f.Context.Active = true
		rel := policy.Rel{}
		if ev.GroupID != nil {
			var g models.Group
			err = tx.First(&g, *ev.GroupID).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				f.Context.Active = false
			} else if err != nil {
				return f, nil, err
			} else {
				f.Context.Active = g.Status == models.GroupActive
			}
			var m models.Membership
			if err = tx.Where("group_id = ? AND user_id = ?", *ev.GroupID, s.RequesterID).Limit(1).Find(&m).Error; err != nil {
				return f, nil, err
			}
			rel = membershipRel(m)
		}
		var hosts int64
		if err = tx.Model(&models.EventHost{}).Where("event_id = ? AND user_id = ?", ev.ID, s.RequesterID).Count(&hosts).Error; err != nil {
			return f, nil, err
		}
		f.Context.EventManager = policy.CanManageEvent(asPerson(requester), viewOf(&ev, hosts > 0), rel)
		var count int64
		if err = tx.Model(&models.Registration{}).Joins("JOIN event_occurrences ON event_occurrences.id = registrations.occurrence_id").Where("event_occurrences.event_id = ? AND registrations.user_id = ? AND registrations.status NOT IN ?", ev.ID, s.SubjectUserID, []string{models.RegRemoved, models.RegCancelled}).Count(&count).Error; err != nil {
			return f, nil, err
		}
		f.Context.EventRegistrant = count > 0
		var end struct{ Last *time.Time }
		if err = tx.Model(&models.EventOccurrence{}).Select("max(ends_at) AS last").Where("event_id = ?", ev.ID).Scan(&end).Error; err != nil {
			return f, nil, err
		}
		f.Context.LastOccurrenceEnd = end.Last
	}
	return f, subject, nil
}

func contactAudit(tx *gorm.DB, actor *uint, action string, r models.ContactRequest, reason, ip string) error {
	if r.ContextType != policy.ContactGroup && r.ContextType != policy.ContactEvent {
		r.ContextType = ""
	}
	if r.Purpose == policy.RequestGroupAdd || r.Purpose == policy.RequestOccurrenceAdd {
		meta := map[string]any{"purpose": r.Purpose, "requesterId": r.RequesterID, "subjectUserId": r.SubjectUserID, "contextType": r.ContextType, "contextId": r.ContextID, "needsAdmin": r.NeedsAdmin, "outcome": r.Status, "reason": reason}
		if r.Purpose == policy.RequestOccurrenceAdd {
			meta["occurrenceId"] = r.OccurrenceID
		}
		return audit.Write(tx, audit.Entry{Actor: actor, Action: addAuditAction(action), TargetType: "add_request", TargetID: r.ID, IP: ip, Meta: meta})
	}
	// Empty purpose is reserved for malformed/not-found contact refusals.
	if r.Purpose != policy.RequestContact && r.Purpose != "" {
		return errors.New("unknown request audit purpose")
	}
	// A refused malformed input must not smuggle arbitrary text into audit.
	if !policy.ValidContactFields(r.Fields) {
		r.Fields = ""
	}
	return audit.Write(tx, audit.Entry{Actor: actor, Action: action, TargetType: "contact_request", TargetID: r.ID, IP: ip, Meta: map[string]any{"requesterId": r.RequesterID, "subjectUserId": r.SubjectUserID, "contextType": r.ContextType, "contextId": r.ContextID, "fields": r.Fields, "needsAdmin": r.NeedsAdmin, "outcome": r.Status, "reason": reason}})
}
func contactGuardians(tx *gorm.DB, teen uint) ([]uint, error) {
	var ids []uint
	err := tx.Model(&models.GuardianLink{}).Joins("JOIN users ON users.id = guardian_links.guardian_user_id AND users.deleted_at IS NULL AND users.status = ? AND users.account_type = ? AND users.password_hash IS NOT NULL", models.UserActive, models.AccountAdult).Where("teen_user_id = ? AND guardian_links.status = ?", teen, models.GuardianConfirmed).Order("guardian_user_id").Pluck("guardian_user_id", &ids).Error
	return ids, err
}
func contactConfirmedGuardian(tx *gorm.DB, actor, teen uint) (bool, error) {
	var n int64
	err := tx.Model(&models.GuardianLink{}).Where("guardian_user_id = ? AND teen_user_id = ? AND status = ?", actor, teen, models.GuardianConfirmed).Count(&n).Error
	return n > 0, err
}

func contactNotice(tx *gorm.DB, r models.ContactRequest, kind string, recipients []uint, key string) ([]uint, error) {
	if r.Purpose != policy.RequestContact && r.Purpose != policy.RequestGroupAdd && r.Purpose != policy.RequestOccurrenceAdd {
		return nil, errors.New("unknown request notice purpose")
	}
	// Deleted/disabled parties retain history but receive no new feed/email.
	var active []uint
	if len(recipients) == 0 {
		return nil, nil
	}
	if err := tx.Model(&models.User{}).Where("id IN ? AND status = ? AND password_hash IS NOT NULL", recipients, models.UserActive).Order("id").Pluck("id", &active).Error; err != nil {
		return nil, err
	}
	subjectType, link, prefix := "contact_request", "contact-requests", "contact"
	if r.Purpose == policy.RequestGroupAdd || r.Purpose == policy.RequestOccurrenceAdd {
		subjectType, link, prefix = "add_request", "add-requests", "add"
		kind = addNoticeKind(kind)
	}
	return notify.Send(tx, notify.Msg{Kind: kind, To: active, SubjectType: subjectType, SubjectID: r.ID, Link: fmt.Sprintf("/%s/%d", link, r.ID), DedupeKey: fmt.Sprintf("%s:%d:%s", prefix, r.ID, key), Vars: notify.Vars{Teen: r.NeedsAdmin}})
}
func resolveContactTasks(tx *gorm.DB, r models.ContactRequest, actor *uint, outcome string) error {
	if r.Purpose != policy.RequestContact && r.Purpose != policy.RequestGroupAdd && r.Purpose != policy.RequestOccurrenceAdd {
		return errors.New("unknown request task purpose")
	}
	kinds := []string{notify.ContactSubjectReview, notify.ContactAdminReview}
	if r.Purpose == policy.RequestGroupAdd || r.Purpose == policy.RequestOccurrenceAdd {
		kinds = []string{notify.AddSubjectReview, notify.AddAdminReview}
	}
	for _, kind := range kinds {
		if _, err := notify.Resolve(tx, kind, r.ID, actor, outcome); err != nil {
			return err
		}
	}
	return nil
}
func finishContactRequest(tx *gorm.DB, r *models.ContactRequest, status, reason string, now time.Time) ([]uint, error) {
	if r.Purpose != policy.RequestContact && r.Purpose != policy.RequestGroupAdd && r.Purpose != policy.RequestOccurrenceAdd {
		return nil, errors.New("unknown request lifecycle purpose")
	}
	r.Status, r.EndReason, r.CompletedAt = status, reason, &now
	if err := tx.Save(r).Error; err != nil {
		return nil, err
	}
	action := audit.ContactEnded
	if status == policy.ContactExpired {
		action = audit.ContactExpired
	}
	if err := contactAudit(tx, nil, action, *r, reason, ""); err != nil {
		return nil, err
	}
	if err := resolveContactTasks(tx, *r, nil, status); err != nil {
		return nil, err
	}
	return contactNotice(tx, *r, notify.ContactUpdated, []uint{r.RequesterID, r.SubjectUserID}, status)
}
func endContactGrant(tx *gorm.DB, g *models.ContactGrant, r models.ContactRequest, reason string, actor *uint, now time.Time, ip string) ([]uint, error) {
	if r.Purpose != policy.RequestContact {
		return nil, errors.New("not a contact grant")
	}
	if g.EndedAt != nil {
		return nil, nil
	}
	g.EndedAt, g.EndReason, g.EndedBy = &now, reason, actor
	if err := tx.Save(g).Error; err != nil {
		return nil, err
	}
	action := audit.ContactEnded
	if actor != nil {
		action = audit.ContactRevoked
	}
	if err := contactAudit(tx, actor, action, r, reason, ip); err != nil {
		return nil, err
	}
	recipients := []uint{r.RequesterID, r.SubjectUserID}
	if r.NeedsAdmin {
		ids, err := contactGuardians(tx, r.SubjectUserID)
		if err != nil {
			return nil, err
		}
		recipients = append(recipients, ids...)
	}
	return contactNotice(tx, r, notify.ContactUpdated, recipients, "grant-ended")
}

// reconcileContacts is idempotent: only pending requests and unended grants
// are candidates. History stays granted after its grant ends. Scoped mutation
// hooks include group events so leadership loss cannot be undone by rejoining.
func (a *App) reconcileContacts(tx *gorm.DB, now time.Time, change contactChange) ([]uint, error) {
	q := tx.Where("purpose IN ?", []string{policy.RequestContact, policy.RequestGroupAdd, policy.RequestOccurrenceAdd}).Where("status = ? OR id IN (SELECT request_id FROM contact_grants WHERE ended_at IS NULL)", policy.ContactPending)
	switch {
	case change.UserID != 0:
		q = q.Where("requester_id = ? OR subject_user_id = ?", change.UserID, change.UserID)
	case change.EventID != 0:
		q = q.Where("context_type = ? AND context_id = ?", policy.ContactEvent, change.EventID)
	case change.GroupID != 0:
		q = q.Where("(context_type = 'group' AND context_id = ?) OR (context_type = 'event' AND context_id IN (SELECT id FROM events WHERE group_id = ?))", change.GroupID, change.GroupID)
	}
	var rs []models.ContactRequest
	if err := q.Clauses(clause.Locking{Strength: "UPDATE"}).Order("id").Find(&rs).Error; err != nil {
		return nil, err
	}
	var mailed []uint
	for i := range rs {
		r := &rs[i]
		if r.Purpose == policy.RequestOccurrenceAdd {
			ids, err := a.reconcileOccurrenceAsk(tx, r, now)
			if err != nil {
				return nil, err
			}
			mailed = append(mailed, ids...)
			continue
		}
		if r.Purpose == policy.RequestGroupAdd {
			ids, err := a.reconcileGroupAsk(tx, r, now)
			if err != nil {
				return nil, err
			}
			mailed = append(mailed, ids...)
			continue
		}
		if r.Purpose != policy.RequestContact {
			continue
		}
		f, _, err := a.loadContactFacts(tx, contactScope(*r), now)
		if err != nil {
			return nil, err
		}
		reason := policy.ContactIneligibility(f, r.NeedsAdmin)
		if r.Status == policy.ContactPending {
			status := ""
			if policy.ContactRequestExpired(contactRequestPolicy(*r), now) {
				status = policy.ContactExpired
				reason = ""
			} else if reason != "" {
				status = policy.ContactEnded
			}
			if status != "" {
				ids, err := finishContactRequest(tx, r, status, reason, now)
				if err != nil {
					return nil, err
				}
				mailed = append(mailed, ids...)
			}
		} else if reason != "" {
			var gs []models.ContactGrant
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("request_id = ? AND ended_at IS NULL", r.ID).Order("id").Find(&gs).Error; err != nil {
				return nil, err
			}
			for j := range gs {
				ids, err := endContactGrant(tx, &gs[j], *r, reason, nil, now, "")
				if err != nil {
					return nil, err
				}
				mailed = append(mailed, ids...)
			}
		}
	}
	ids, err := a.reconcileOccurrenceAccesses(tx, now, change)
	if err != nil {
		return nil, err
	}
	return append(mailed, ids...), nil
}

func (a *App) sweepContacts(ctx context.Context, now time.Time) error {
	var mailed []uint
	err := a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var locked bool
		if err := tx.Raw("SELECT pg_try_advisory_xact_lock(?)", contactLockID).Scan(&locked).Error; err != nil {
			return err
		}
		if !locked {
			return nil
		}
		var err error
		mailed, err = a.reconcileContacts(tx, now, contactChange{})
		return err
	})
	if err == nil {
		a.deliverNotificationEmails(mailed)
	}
	return err
}

// addRequestPolicy is deliberately separate from contactScope: an ask never
// supplies contact authority, even after completion.
func addRequestPolicy(r models.ContactRequest) policy.AddRequest {
	occ := uint(0)
	if r.OccurrenceID != nil {
		occ = *r.OccurrenceID
	}
	return policy.AddRequest{AddScope: policy.AddScope{Purpose: r.Purpose, RequesterID: r.RequesterID, SubjectUserID: r.SubjectUserID, ContextType: r.ContextType, ContextID: r.ContextID, OccurrenceID: occ, Options: policy.AddOptions{OverLimit: r.AddOverLimit, OverCapacity: r.AddOverCapacity, AttendanceMode: r.AddAttendanceMode, GuestCount: r.AddGuestCount, ChildcareCount: r.AddChildcareCount, ChildcareAgesNote: r.AddChildcareAgesNote}}, NeedsAdmin: r.NeedsAdmin, SubjectDecision: r.SubjectDecision, AdminDecision: r.AdminDecision, Status: r.Status, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt}
}

func (a *App) loadGroupAddFacts(tx *gorm.DB, r models.ContactRequest, now time.Time) (policy.AddFacts, error) {
	f := policy.AddFacts{Now: now, ContextType: r.ContextType, ContextID: r.ContextID}
	if r.Purpose != policy.RequestGroupAdd || r.ContextType != policy.ContactGroup {
		return f, nil
	}
	zone, err := contactZone(tx)
	if err != nil {
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
	f.Requester, f.Subject = contactAccount(requester, now, zone), contactAccount(subject, now, zone)
	var g models.Group
	err = tx.First(&g, r.ContextID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	f.ContextActive = g.Status == models.GroupActive
	var m models.Membership
	if err = tx.Where("group_id = ? AND user_id = ?", g.ID, r.RequesterID).Limit(1).Find(&m).Error; err != nil {
		return f, err
	}
	f.Manager = policy.CanManageGroup(asPerson(requester), membershipRel(m))
	// Ordinary manager group additions allow active adults; teens alone need
	// this request contract. Hidden contact does not restrict membership.
	f.OrdinaryEligible = subject != nil && !policy.TeenAddNeedsApproval(asPerson(subject))
	return f, nil
}

func (a *App) reconcileGroupAsk(tx *gorm.DB, r *models.ContactRequest, now time.Time) ([]uint, error) {
	if r.Purpose != policy.RequestGroupAdd || r.Status != policy.ContactPending {
		return nil, nil
	}
	f, err := a.loadGroupAddFacts(tx, *r, now)
	if err != nil {
		return nil, err
	}
	if policy.AddRequestExpired(addRequestPolicy(*r), now) {
		return finishContactRequest(tx, r, policy.ContactExpired, "", now)
	}
	if reason := policy.AddIneligibility(addRequestPolicy(*r).AddScope, f, r.NeedsAdmin); reason != "" {
		return finishContactRequest(tx, r, policy.ContactEnded, reason, now)
	}
	return nil, nil
}

// Only the group request finalizer constructs this fact. The membership core
// reloads its persisted purpose, exact scope and dual approvals under the gate.
// No HTTP boolean, user ID or email can supply teen-add provenance.
type verifiedGroupAdd struct {
	requestID uint
	now       time.Time
}

func (a *App) verifiedGroupAddIn(tx *gorm.DB, proof *verifiedGroupAdd, requester, subject, group uint) bool {
	var r models.ContactRequest
	if tx.Where("id = ? AND purpose = ? AND requester_id = ? AND subject_user_id = ? AND context_id = ?", proof.requestID, policy.RequestGroupAdd, requester, subject, group).First(&r).Error != nil {
		return false
	}
	f, err := a.loadGroupAddFacts(tx, r, proof.now)
	return err == nil && policy.CanFinalizeAddRequest(addRequestPolicy(r), f)
}
