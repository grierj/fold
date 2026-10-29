package main

import (
	"errors"
	"time"

	"fold/internal/policy"
	"fold/models"

	"gorm.io/gorm"
)

type contactReadPair struct{ user, context uint }

// contactPageReads holds only this page's facts, loaded under the contact gate.
// Live eligibility and unscoped historical names deliberately use separate maps.
type contactPageReads struct {
	users      map[uint]*models.User
	names      map[uint]string
	groups     map[uint]*models.Group
	events     map[uint]models.Event
	rels       map[contactReadPair]policy.Rel
	hosts      map[contactReadPair]bool
	registered map[contactReadPair]bool
	lastEnds   map[uint]*time.Time
	guardians  map[uint]bool
}

func loadContactPageReads(tx *gorm.DB, op *contactOperation, rs []models.ContactRequest, guardians bool) (*contactPageReads, error) {
	p := &contactPageReads{
		users: map[uint]*models.User{}, names: map[uint]string{}, groups: map[uint]*models.Group{},
		events: map[uint]models.Event{}, rels: map[contactReadPair]policy.Rel{}, hosts: map[contactReadPair]bool{},
		registered: map[contactReadPair]bool{}, lastEnds: map[uint]*time.Time{}, guardians: map[uint]bool{},
	}
	userIDs, groupIDs, eventIDs, subjectIDs := []uint{op.actor.ID}, []uint{}, []uint{}, []uint{}
	for _, r := range rs {
		if r.Purpose != policy.RequestContact {
			return nil, errors.New("not a contact request")
		}
		userIDs = append(userIDs, r.RequesterID, r.SubjectUserID)
		subjectIDs = append(subjectIDs, r.SubjectUserID)
		if r.ContextType == policy.ContactEvent {
			eventIDs = append(eventIDs, r.ContextID)
		} else {
			groupIDs = append(groupIDs, r.ContextID)
		}
	}
	var users []models.User
	if err := tx.Unscoped().Where("id IN ?", userIDs).Find(&users).Error; err != nil {
		return nil, err
	}
	for i := range users {
		u := &users[i]
		p.names[u.ID] = u.PublicName()
		if !u.DeletedAt.Valid {
			p.users[u.ID] = u
		}
	}
	if len(eventIDs) > 0 {
		var rows []models.Event
		if err := tx.Where("id IN ?", eventIDs).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, ev := range rows {
			p.events[ev.ID] = ev
			if ev.GroupID != nil {
				groupIDs = append(groupIDs, *ev.GroupID)
			}
		}
	}
	if len(groupIDs) > 0 {
		var groups []models.Group
		if err := tx.Where("id IN ?", groupIDs).Find(&groups).Error; err != nil {
			return nil, err
		}
		for i := range groups {
			p.groups[groups[i].ID] = &groups[i]
		}
		var memberships []models.Membership
		if err := tx.Where("group_id IN ? AND user_id IN ?", groupIDs, userIDs).Find(&memberships).Error; err != nil {
			return nil, err
		}
		for _, m := range memberships {
			p.rels[contactReadPair{m.UserID, m.GroupID}] = membershipRel(m)
		}
	}
	if len(eventIDs) > 0 {
		var hosts []models.EventHost
		if err := tx.Where("event_id IN ? AND user_id IN ?", eventIDs, userIDs).Find(&hosts).Error; err != nil {
			return nil, err
		}
		for _, h := range hosts {
			p.hosts[contactReadPair{h.UserID, h.EventID}] = true
		}
		var registrations []struct {
			EventID uint
			UserID  uint
		}
		if err := tx.Model(&models.Registration{}).
			Select("event_occurrences.event_id, registrations.user_id").
			Joins("JOIN event_occurrences ON event_occurrences.id = registrations.occurrence_id").
			Where("event_occurrences.event_id IN ? AND registrations.user_id IN ? AND registrations.status NOT IN ?", eventIDs, userIDs, []string{models.RegRemoved, models.RegCancelled}).
			Group("event_occurrences.event_id, registrations.user_id").Scan(&registrations).Error; err != nil {
			return nil, err
		}
		for _, r := range registrations {
			p.registered[contactReadPair{r.UserID, r.EventID}] = true
		}
		var ends []struct {
			EventID uint
			Last    *time.Time
		}
		// The final occurrence, not the next scheduled date, owns the grace
		// window. Match scalar facts even for cancelled or historical dates.
		if err := tx.Model(&models.EventOccurrence{}).Select("event_id, max(ends_at) AS last").
			Where("event_id IN ?", eventIDs).Group("event_id").Scan(&ends).Error; err != nil {
			return nil, err
		}
		for _, end := range ends {
			p.lastEnds[end.EventID] = end.Last
		}
	}
	if guardians {
		var links []models.GuardianLink
		if err := tx.Where("guardian_user_id = ? AND teen_user_id IN ? AND status = ?", op.actor.ID, subjectIDs, models.GuardianConfirmed).Find(&links).Error; err != nil {
			return nil, err
		}
		for _, link := range links {
			if link.TeenUserID != nil {
				p.guardians[*link.TeenUserID] = true
			}
		}
	}
	return p, nil
}

func (p *contactPageReads) facts(op *contactOperation, r models.ContactRequest) policy.ContactAccessFacts {
	f := policy.ContactAccessFacts{Now: op.now,
		Requester: contactAccount(p.users[r.RequesterID], op.now, op.zone),
		Subject:   contactAccount(p.users[r.SubjectUserID], op.now, op.zone),
		Context:   policy.ContactContext{Type: r.ContextType, ID: r.ContextID},
	}
	switch r.ContextType {
	case policy.ContactGroup:
		if g := p.groups[r.ContextID]; g != nil {
			f.Context.Active = g.Status == models.GroupActive
			f.Context.GroupLeader = p.rels[contactReadPair{r.RequesterID, g.ID}].Leader
			f.Context.GroupMember = p.rels[contactReadPair{r.SubjectUserID, g.ID}].Member
		}
	case policy.ContactEvent:
		if ev := p.events[r.ContextID]; ev.ID != 0 {
			f.Context.Active = true
			rel := policy.Rel{}
			if ev.GroupID != nil {
				g := p.groups[*ev.GroupID]
				f.Context.Active = g != nil && g.Status == models.GroupActive
				rel = p.rels[contactReadPair{r.RequesterID, *ev.GroupID}]
			}
			f.Context.EventManager = policy.CanManageEvent(asPerson(p.users[r.RequesterID]), viewOf(&ev, p.hosts[contactReadPair{r.RequesterID, ev.ID}]), rel)
			f.Context.EventRegistrant = p.registered[contactReadPair{r.SubjectUserID, ev.ID}]
			f.Context.LastOccurrenceEnd = p.lastEnds[ev.ID]
		}
	}
	return f
}

func (p *contactPageReads) name(id uint) string {
	if name, ok := p.names[id]; ok {
		return name
	}
	return "Someone"
}

func (p *contactPageReads) contextID(a *App, op *contactOperation, r models.ContactRequest) *uint {
	if r.ContextType == policy.ContactEvent {
		ev := p.events[r.ContextID]
		if ev.ID == 0 {
			return nil
		}
		group, rel := policy.Group{}, policy.Rel{}
		if ev.GroupID != nil {
			if g := p.groups[*ev.GroupID]; g != nil {
				group = asGroup(g)
				rel = p.rels[contactReadPair{op.actor.ID, g.ID}]
			}
		}
		// Date invitations/overrides never authorize the parent series.
		if !policy.CanSeeEvent(asPerson(op.actor), viewOf(&ev, p.hosts[contactReadPair{op.actor.ID, ev.ID}]), group, rel) {
			return nil
		}
	} else if a.groupShownTo(op.actor, p.groups[r.ContextID], p.rels[contactReadPair{op.actor.ID, r.ContextID}]) == nil {
		return nil
	}
	id := r.ContextID
	return &id
}

func (a *App) presentContactRequests(tx *gorm.DB, op *contactOperation, rs []models.ContactRequest) ([]contactRequestJSON, error) {
	out := []contactRequestJSON{}
	if len(rs) == 0 {
		return out, nil
	}
	page, err := loadContactPageReads(tx, op, rs, false)
	if err != nil {
		return nil, err
	}
	actor := contactAccount(op.actor, op.now, op.zone)
	for _, r := range rs {
		p := contactRequestPolicy(r)
		if !policy.CanViewContactRequest(actor, p) {
			continue
		}
		f := page.facts(op, r)
		out = append(out, contactRequestJSON{ID: r.ID, RequesterID: r.RequesterID, RequesterName: page.name(r.RequesterID), SubjectUserID: r.SubjectUserID, SubjectName: page.name(r.SubjectUserID), ContextType: r.ContextType, ContextID: page.contextID(a, op, r), Fields: r.Fields, Reason: r.Reason, NeedsAdmin: r.NeedsAdmin, SubjectDecision: r.SubjectDecision, SubjectDecidedBy: r.SubjectDecidedBy, SubjectDecidedAt: r.SubjectDecidedAt, AdminDecision: r.AdminDecision, AdminDecidedBy: r.AdminDecidedBy, AdminDecidedAt: r.AdminDecidedAt, Status: r.Status, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, CompletedAt: r.CompletedAt, EndReason: r.EndReason,
			CanSubjectDecide: policy.ContactRequestStatusAfterDecision(actor, policy.ContactSubject, policy.ContactApproved, p, f) != "", CanAdminDecide: policy.ContactRequestStatusAfterDecision(actor, policy.ContactAdmin, policy.ContactApproved, p, f) != "", CanWithdraw: policy.CanWithdrawContactRequest(actor, p, op.now)})
	}
	return out, nil
}

func (a *App) presentContactGrants(tx *gorm.DB, op *contactOperation, gs []models.ContactGrant) ([]contactGrantJSON, error) {
	out := []contactGrantJSON{}
	if len(gs) == 0 {
		return out, nil
	}
	ids := make([]uint, 0, len(gs))
	for _, g := range gs {
		ids = append(ids, g.RequestID)
	}
	var rs []models.ContactRequest
	if err := tx.Where("id IN ? AND purpose = ?", ids, policy.RequestContact).Find(&rs).Error; err != nil {
		return nil, err
	}
	requests := map[uint]models.ContactRequest{}
	for _, r := range rs {
		requests[r.ID] = r
	}
	page, err := loadContactPageReads(tx, op, rs, true)
	if err != nil {
		return nil, err
	}
	actor := contactAccount(op.actor, op.now, op.zone)
	for _, g := range gs {
		r, ok := requests[g.RequestID]
		if !ok {
			return nil, gorm.ErrRecordNotFound
		}
		f := page.facts(op, r)
		guardian := page.guardians[r.SubjectUserID]
		p := contactGrantPolicy(g, r)
		teen := f.Subject.Person != nil && f.Subject.Person.Teen && f.Subject.AgeYears != nil && *f.Subject.AgeYears >= 13 && *f.Subject.AgeYears < 18
		if !policy.CanViewContactGrant(actor, guardian, teen, p) {
			continue
		}
		out = append(out, contactGrantJSON{ID: g.ID, RequestID: r.ID, RequesterID: r.RequesterID, RequesterName: page.name(r.RequesterID), SubjectUserID: r.SubjectUserID, SubjectName: page.name(r.SubjectUserID), ContextType: r.ContextType, ContextID: page.contextID(a, op, r), Fields: r.Fields, NeedsAdmin: r.NeedsAdmin, GrantedAt: g.GrantedAt, EndedAt: g.EndedAt, EndReason: g.EndReason, EndedBy: g.EndedBy,
			CanReveal: policy.CanRevealContactGrant(actor, contactScope(r), p, f), CanRevoke: policy.ContactGrantRevocationReason(actor, guardian, p) != ""})
	}
	return out, nil
}
