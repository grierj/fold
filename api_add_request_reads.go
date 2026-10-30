package main

import (
	"fold/internal/policy"
	"fold/models"
	"gorm.io/gorm"
)

type addRequestPair struct{ user, context uint }
type addRequestBlockKey struct {
	user      uint
	organizer policy.OrganizerScope
}

// presentAddRequests loads only this page's participants and destinations. All
// policy calls below are pure: no presenter performs a hidden per-row DB read.
// Terminal history needs previews, not mutable decision/completion facts.
func (a *App) presentAddRequests(tx *gorm.DB, op *contactOperation, rs []models.ContactRequest) ([]addRequestJSON, error) {
	out := []addRequestJSON{}
	if len(rs) == 0 {
		return out, nil
	}
	userIDs, eventIDs, groupIDs, occIDs := []uint{}, []uint{}, []uint{}, []uint{}
	pendingSubjects, pendingOccs, nameIDs := []uint{}, []uint{}, []uint{}
	pendingEvents := map[uint]bool{}
	for _, r := range rs {
		if r.Purpose == policy.RequestGroupAdd {
			groupIDs = append(groupIDs, r.ContextID)
			nameIDs = append(nameIDs, r.RequesterID, r.SubjectUserID)
		}
		if r.Purpose == policy.RequestOccurrenceAdd {
			eventIDs = append(eventIDs, r.ContextID)
			if r.OccurrenceID != nil {
				occIDs = append(occIDs, *r.OccurrenceID)
			}
		}
		if r.Status == policy.ContactPending {
			userIDs = append(userIDs, r.RequesterID, r.SubjectUserID)
			if r.Purpose == policy.RequestOccurrenceAdd && r.OccurrenceID != nil {
				pendingEvents[r.ContextID] = true
				pendingSubjects = append(pendingSubjects, r.SubjectUserID)
				pendingOccs = append(pendingOccs, *r.OccurrenceID)
			}
		}
	}
	users, accounts, err := occurrenceAccountsIn(tx, userIDs, op.now, op.zone)
	if err != nil {
		return nil, err
	}
	names := map[uint]string{}
	if len(nameIDs) > 0 {
		var rows []models.User
		if err := tx.Unscoped().Select("id", "name", "display_name").Where("id IN ?", nameIDs).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, u := range rows {
			names[u.ID] = u.PublicName()
		}
	}
	// Group asks intentionally retain their existing account (not consent) rule.
	groupAccounts := map[uint]policy.ContactAccount{}
	for id, u := range users {
		groupAccounts[id] = contactAccount(u, op.now, op.zone)
	}
	evs := map[uint]models.Event{}
	if len(eventIDs) > 0 {
		var rows []models.Event
		if err := tx.Where("id IN ?", eventIDs).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, ev := range rows {
			evs[ev.ID] = ev
			if ev.GroupID != nil && pendingEvents[ev.ID] {
				groupIDs = append(groupIDs, *ev.GroupID)
			}
		}
	}
	groups := map[uint]*models.Group{}
	if len(groupIDs) > 0 {
		var rows []models.Group
		if err := tx.Where("id IN ?", groupIDs).Find(&rows).Error; err != nil {
			return nil, err
		}
		for i := range rows {
			groups[rows[i].ID] = &rows[i]
		}
	}
	rels := map[addRequestPair]policy.Rel{}
	if len(groupIDs) > 0 {
		var rows []models.Membership
		if err := tx.Where("group_id IN ? AND user_id IN ?", groupIDs, append(userIDs, op.actor.ID)).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, m := range rows {
			rels[addRequestPair{m.UserID, m.GroupID}] = membershipRel(m)
		}
	}
	hosts := map[addRequestPair]bool{}
	if len(eventIDs) > 0 && len(userIDs) > 0 {
		var rows []models.EventHost
		if err := tx.Where("event_id IN ? AND user_id IN ?", eventIDs, userIDs).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, h := range rows {
			hosts[addRequestPair{h.UserID, h.EventID}] = true
		}
	}
	occs := map[uint]models.EventOccurrence{}
	if len(occIDs) > 0 {
		var rows []models.EventOccurrence
		if err := tx.Where("id IN ?", occIDs).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, o := range rows {
			occs[o.ID] = o
		}
	}
	blocks := map[addRequestBlockKey]*policy.ManagerAddBlock{}
	accesses := map[addRequestPair][]models.OccurrenceAccess{}
	if len(pendingOccs) > 0 {
		var bs []models.ManagerAddBlock
		if err := tx.Where("subject_user_id IN ? AND ((organizer_type = ? AND organizer_id IN ?) OR (organizer_type = ? AND organizer_id IN ?))", pendingSubjects, policy.ContactGroup, groupIDs, policy.ContactEvent, eventIDs).Find(&bs).Error; err != nil {
			return nil, err
		}
		for _, b := range bs {
			organizer := policy.OrganizerScope{Type: b.OrganizerType, ID: b.OrganizerID}
			blocks[addRequestBlockKey{b.SubjectUserID, organizer}] = &policy.ManagerAddBlock{SubjectUserID: b.SubjectUserID, Organizer: organizer}
		}
		var rows []models.OccurrenceAccess
		if err := tx.Where("subject_user_id IN ? AND occurrence_id IN ?", pendingSubjects, pendingOccs).Order("id").Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			key := addRequestPair{row.SubjectUserID, row.OccurrenceID}
			accesses[key] = append(accesses[key], row)
		}
	}
	actor := contactAccount(op.actor, op.now, op.zone)
	for _, r := range rs {
		p := addRequestPolicy(r)
		f := policy.AddFacts{Now: op.now, ContextType: r.ContextType, ContextID: r.ContextID}
		if r.Status == policy.ContactPending {
			if r.Purpose == policy.RequestGroupAdd && r.ContextType == policy.ContactGroup {
				f.Requester, f.Subject = groupAccounts[r.RequesterID], groupAccounts[r.SubjectUserID]
				if g := groups[r.ContextID]; g != nil {
					f.ContextActive = g.Status == models.GroupActive
					f.Manager = policy.CanManageGroup(asPerson(users[r.RequesterID]), rels[addRequestPair{r.RequesterID, g.ID}])
					f.OrdinaryEligible = users[r.SubjectUserID] != nil && !policy.TeenAddNeedsApproval(asPerson(users[r.SubjectUserID]))
				}
			} else if r.Purpose == policy.RequestOccurrenceAdd && r.ContextType == policy.ContactEvent && r.OccurrenceID != nil {
				f.Requester, f.Subject = accounts[r.RequesterID], accounts[r.SubjectUserID]
				f.Occurrence = policy.OccurrenceFacts{ID: *r.OccurrenceID, EventID: r.ContextID}
				if ev := evs[r.ContextID]; ev.ID != 0 {
					organizer := policy.OrganizerScope{Type: policy.ContactEvent, ID: ev.ID}
					live := true
					gp := policy.Group{}
					requesterRel, subjectRel := policy.Rel{}, policy.Rel{}
					if ev.GroupID != nil {
						organizer = policy.OrganizerScope{Type: policy.ContactGroup, ID: *ev.GroupID}
						g := groups[*ev.GroupID]
						live = g != nil && g.Status == models.GroupActive
						if g != nil {
							gp = asGroup(g)
							requesterRel = rels[addRequestPair{r.RequesterID, g.ID}]
							subjectRel = rels[addRequestPair{r.SubjectUserID, g.ID}]
						}
					}
					f.Occurrence.Organizer, f.Occurrence.ContextActive = organizer, live
					if occ := occs[*r.OccurrenceID]; occ.ID != 0 && occ.EventID == ev.ID {
						f.Occurrence.Scheduled = occ.Status == models.OccurrenceScheduled
					}
					f.ContextActive = live
					f.Manager = policy.CanManageEvent(asPerson(users[r.RequesterID]), viewOf(&ev, hosts[addRequestPair{r.RequesterID, ev.ID}]), requesterRel)
					subject := users[r.SubjectUserID]
					view := viewOf(&ev, hosts[addRequestPair{r.SubjectUserID, ev.ID}])
					occ := occs[*r.OccurrenceID]
					view.Visibility = policy.EffectiveVisibility(ev.Visibility, occ.VisibilityOverride)
					f.OrdinaryEligible = policy.CanRegisterEvent(asPerson(subject), view, gp, subjectRel)
					if subject != nil && subject.Status == models.UserGuest {
						f.OrdinaryEligible = policy.CanRegisterGuest(view, gp)
					}
					f.Block = blocks[addRequestBlockKey{r.SubjectUserID, organizer}]
					for _, access := range accesses[addRequestPair{r.SubjectUserID, *r.OccurrenceID}] {
						if access.Status == policy.OccurrenceAccessActive {
							f.ExistingAccessActive = true
						}
						if access.EndedAt != nil && (f.LatestAccessEndedAt == nil || access.EndedAt.After(*f.LatestAccessEndedAt)) {
							f.LatestAccessEndedAt = access.EndedAt
						}
					}
				}
			}
		}
		j := addRequestJSON{ID: r.ID, Purpose: r.Purpose, Reason: r.Reason, OverLimit: r.AddOverLimit, NeedsAdmin: r.NeedsAdmin, SubjectDecision: r.SubjectDecision, AdminDecision: r.AdminDecision, Status: r.Status, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, CompletedAt: r.CompletedAt, EndReason: r.EndReason,
			CanSubjectDecide: policy.AddRequestStatusAfterDecision(actor, policy.ContactSubject, policy.ContactApproved, p, f) != "", CanAdminDecide: policy.AddRequestStatusAfterDecision(actor, policy.ContactAdmin, policy.ContactApproved, p, f) != "", CanWithdraw: policy.CanWithdrawAddRequest(actor, p, op.now), CanComplete: policy.CanViewAddRequest(actor, p) && policy.CanFinalizeAddRequest(p, f)}
		if r.Purpose == policy.RequestOccurrenceAdd {
			j.Reason = ""
			j.CompletionProblem = op.completionProblem
			ev := evs[r.ContextID]
			if r.OccurrenceID != nil {
				occ := occs[*r.OccurrenceID]
				if ev.ID != 0 && occ.ID != 0 && occ.EventID == ev.ID {
					j.EventTitle = ev.Title
					if occ.TitleOverride != nil {
						j.EventTitle = *occ.TitleOverride
					}
					j.Date, j.StartsAt, j.EndsAt, j.TZID = occ.OriginalDate, &occ.StartsAt, &occ.EndsAt, ev.TZID
				}
			}
		} else if r.Purpose == policy.RequestGroupAdd {
			if shown := a.groupShownTo(op.actor, groups[r.ContextID], rels[addRequestPair{op.actor.ID, r.ContextID}]); shown != nil {
				id := r.ContextID
				j.ContextID = &id
				j.GroupName = shown.Name
				j.RequesterName, j.SubjectName = "Someone", "Someone"
				if name, ok := names[r.RequesterID]; ok {
					j.RequesterName = name
				}
				if name, ok := names[r.SubjectUserID]; ok {
					j.SubjectName = name
				}
			}
		}
		out = append(out, j)
	}
	return out, nil
}
