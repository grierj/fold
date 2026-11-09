package main

import (
	"errors"
	"time"

	"fold/internal/policy"
	"fold/models"

	"gorm.io/gorm"
)

// contactPageReads holds only this page's facts, loaded under the contact gate.
// Live eligibility and unscoped historical names deliberately use separate maps.
// One request is a page of one: loadContactFacts reads through this too.
type contactPageReads struct {
	*requestContexts
	users      map[uint]*models.User
	names      map[uint]string
	registered map[userContext]bool
	lastEnds   map[uint]*time.Time
	guardians  map[uint]bool
}

// loadContactPageReads loads the facts of rs for one viewer. The viewer's own
// memberships and hosting are loaded with the requesters' and subjects'.
func loadContactPageReads(tx *gorm.DB, actor uint, rs []models.ContactRequest, guardians bool) (*contactPageReads, error) {
	p := &contactPageReads{
		names: map[uint]string{}, users: map[uint]*models.User{},
		registered: map[userContext]bool{}, lastEnds: map[uint]*time.Time{}, guardians: map[uint]bool{},
	}
	userIDs, refs, eventIDs, subjectIDs := []uint{actor}, []contextRef{}, []uint{}, []uint{}
	for _, r := range rs {
		if r.Purpose != policy.RequestContact {
			return nil, errors.New("not a contact request")
		}
		userIDs = append(userIDs, r.RequesterID, r.SubjectUserID)
		subjectIDs = append(subjectIDs, r.SubjectUserID)
		refs = append(refs, contextRef{r.ContextType, r.ContextID})
		if r.ContextType == policy.ContactEvent {
			eventIDs = append(eventIDs, r.ContextID)
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
	var err error
	if p.requestContexts, err = loadRequestContexts(tx, refs, userIDs); err != nil {
		return nil, err
	}
	if len(eventIDs) > 0 {
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
			p.registered[userContext{r.UserID, r.EventID}] = true
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
		if p.guardians, err = confirmedGuardianLinks(tx, actor, subjectIDs); err != nil {
			return nil, err
		}
	}
	return p, nil
}

func (p *contactPageReads) facts(now time.Time, zone *time.Location, r models.ContactRequest) policy.ContactAccessFacts {
	f := policy.ContactAccessFacts{Now: now,
		Requester: contactAccount(p.users[r.RequesterID], now, zone),
		Subject:   contactAccount(p.users[r.SubjectUserID], now, zone),
		Context:   policy.ContactContext{Type: r.ContextType, ID: r.ContextID},
	}
	switch r.ContextType {
	case policy.ContactGroup:
		if g := p.groups[r.ContextID]; g != nil {
			f.Context.Active = g.Status == models.GroupActive
			f.Context.GroupLeader = p.rels[userContext{r.RequesterID, g.ID}].Leader
			f.Context.GroupMember = p.rels[userContext{r.SubjectUserID, g.ID}].Member
		}
	case policy.ContactEvent:
		if ev := p.events[r.ContextID]; ev.ID != 0 {
			f.Context.Active = true
			rel := policy.Rel{}
			if ev.GroupID != nil {
				g := p.groups[*ev.GroupID]
				f.Context.Active = g != nil && g.Status == models.GroupActive
				rel = p.rels[userContext{r.RequesterID, *ev.GroupID}]
			}
			f.Context.EventManager = policy.CanManageEvent(asPerson(p.users[r.RequesterID]), viewOf(&ev, p.hosts[userContext{r.RequesterID, ev.ID}]), rel)
			f.Context.EventRegistrant = p.registered[userContext{r.SubjectUserID, ev.ID}]
			f.Context.LastOccurrenceEnd = p.lastEnds[ev.ID]
		}
	}
	return f
}

func (p *contactPageReads) name(id uint) string {
	if name, ok := p.names[id]; ok {
		return name
	}
	return unknownPersonName
}

func (p *contactPageReads) contextID(a *App, viewer *models.User, r models.ContactRequest) *uint {
	if _, ok := p.shown(a, viewer, contextRef{r.ContextType, r.ContextID}); !ok {
		return nil
	}
	id := r.ContextID
	return &id
}

// presentContactRequests presents rs, which the caller has already filtered to
// the ones the viewer may see. One request is a page of one.
func (a *App) presentContactRequests(tx *gorm.DB, op *contactOperation, rs []models.ContactRequest) ([]contactRequestJSON, error) {
	out := []contactRequestJSON{}
	if len(rs) == 0 {
		return out, nil
	}
	page, err := loadContactPageReads(tx, op.actor.ID, rs, false)
	if err != nil {
		return nil, err
	}
	actor := contactAccount(op.actor, op.now, op.zone)
	for _, r := range rs {
		p := contactRequestPolicy(r)
		f := page.facts(op.now, op.zone, r)
		out = append(out, contactRequestJSON{ID: r.ID, RequesterID: r.RequesterID, RequesterName: page.name(r.RequesterID), SubjectUserID: r.SubjectUserID, SubjectName: page.name(r.SubjectUserID), ContextType: r.ContextType, ContextID: page.contextID(a, op.actor, r), Fields: r.Fields, Reason: r.Reason, NeedsAdmin: r.NeedsAdmin, SubjectDecision: r.SubjectDecision, SubjectDecidedBy: r.SubjectDecidedBy, SubjectDecidedAt: r.SubjectDecidedAt, AdminDecision: r.AdminDecision, AdminDecidedBy: r.AdminDecidedBy, AdminDecidedAt: r.AdminDecidedAt, Status: r.Status, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, CompletedAt: r.CompletedAt, EndReason: r.EndReason,
			CanSubjectDecide: policy.ContactRequestStatusAfterDecision(actor, policy.ContactSubject, policy.ContactApproved, p, f) != "", CanAdminDecide: policy.ContactRequestStatusAfterDecision(actor, policy.ContactAdmin, policy.ContactApproved, p, f) != "", CanWithdraw: policy.CanWithdrawContactRequest(actor, p, op.now)})
	}
	return out, nil
}

func (a *App) presentContactRequest(tx *gorm.DB, op *contactOperation, r models.ContactRequest) (contactRequestJSON, error) {
	rows, err := a.presentContactRequests(tx, op, []models.ContactRequest{r})
	if err != nil {
		return contactRequestJSON{}, err
	}
	return rows[0], nil
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
	page, err := loadContactPageReads(tx, op.actor.ID, rs, true)
	if err != nil {
		return nil, err
	}
	actor := contactAccount(op.actor, op.now, op.zone)
	for _, g := range gs {
		r, ok := requests[g.RequestID]
		if !ok {
			return nil, gorm.ErrRecordNotFound
		}
		f := page.facts(op.now, op.zone, r)
		guardian := page.guardians[r.SubjectUserID]
		p := contactGrantPolicy(g, r)
		if !policy.CanViewContactGrant(actor, guardian, f.Subject.IsTeen(), p) {
			continue
		}
		out = append(out, contactGrantJSON{ID: g.ID, RequestID: r.ID, RequesterID: r.RequesterID, RequesterName: page.name(r.RequesterID), SubjectUserID: r.SubjectUserID, SubjectName: page.name(r.SubjectUserID), ContextType: r.ContextType, ContextID: page.contextID(a, op.actor, r), Fields: r.Fields, NeedsAdmin: r.NeedsAdmin, GrantedAt: g.GrantedAt, EndedAt: g.EndedAt, EndReason: g.EndReason, EndedBy: g.EndedBy,
			CanReveal: policy.CanRevealContactGrant(actor, contactScope(r), p, f), CanRevoke: policy.ContactGrantRevocationReason(actor, guardian, p) != ""})
	}
	return out, nil
}
