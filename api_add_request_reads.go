package main

import (
	"fold/internal/policy"
	"fold/models"
	"gorm.io/gorm"
)

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
	users, accounts, err := occurrenceAccountsInZone(tx, userIDs, op.now, op.zone)
	if err != nil {
		return nil, err
	}
	names := publicNamesIn(tx, nameIDs)
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
	rels := map[userContext]policy.Rel{}
	if len(groupIDs) > 0 {
		var rows []models.Membership
		if err := tx.Where("group_id IN ? AND user_id IN ?", groupIDs, append(userIDs, op.actor.ID)).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, m := range rows {
			rels[userContext{m.UserID, m.GroupID}] = membershipRel(m)
		}
	}
	hosts := map[userContext]bool{}
	if len(eventIDs) > 0 && len(userIDs) > 0 {
		var rows []models.EventHost
		if err := tx.Where("event_id IN ? AND user_id IN ?", eventIDs, userIDs).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, h := range rows {
			hosts[userContext{h.UserID, h.EventID}] = true
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
	regRows := map[userContext]models.Registration{}
	var history map[accessKey]accessHistory
	if len(pendingOccs) > 0 {
		var bs []models.ManagerAddBlock
		if err := tx.Where("subject_user_id IN ? AND ((organizer_type = ? AND organizer_id IN ?) OR (organizer_type = ? AND organizer_id IN ?))", pendingSubjects, policy.ContactGroup, groupIDs, policy.ContactEvent, eventIDs).Find(&bs).Error; err != nil {
			return nil, err
		}
		for _, b := range bs {
			organizer := policy.OrganizerScope{Type: b.OrganizerType, ID: b.OrganizerID}
			blocks[addRequestBlockKey{b.SubjectUserID, organizer}] = &policy.ManagerAddBlock{SubjectUserID: b.SubjectUserID, Organizer: organizer}
		}
		// The live test reads each subject's current registration; the latest
		// end also reads the validated audit snapshots, and a corrupt one
		// fails closed for the whole page.
		var rows []models.Registration
		if err := tx.Where("user_id IN ? AND occurrence_id IN ?", pendingSubjects, pendingOccs).Order("id").Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			regRows[userContext{*row.UserID, row.OccurrenceID}] = row
		}
		var err error
		if history, err = loadAccessHistory(tx, pendingSubjects, pendingOccs); err != nil {
			return nil, err
		}
	}
	contexts := &requestContexts{groups: groups, events: evs, rels: rels, hosts: hosts}
	actor := contactAccount(op.actor, op.now, op.zone)
	for _, r := range rs {
		p := addRequestPolicy(r)
		f := policy.AddFacts{Now: op.now, ContextType: r.ContextType, ContextID: r.ContextID}
		if r.Status == policy.ContactPending {
			if r.Purpose == policy.RequestGroupAdd && r.ContextType == policy.ContactGroup {
				// Group asks intentionally retain their existing account (not consent) rule.
				g := groups[r.ContextID]
				f = groupAddFacts(r, op.now, op.zone, users[r.RequesterID], users[r.SubjectUserID], g, rels[userContext{r.RequesterID, r.ContextID}])
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
							requesterRel = rels[userContext{r.RequesterID, g.ID}]
							subjectRel = rels[userContext{r.SubjectUserID, g.ID}]
						}
					}
					f.Occurrence.Organizer, f.Occurrence.ContextActive = organizer, live
					if occ := occs[*r.OccurrenceID]; occ.ID != 0 && occ.EventID == ev.ID {
						f.Occurrence.Scheduled = occ.Status == models.OccurrenceScheduled
					}
					f.ContextActive = live
					f.Manager = policy.CanManageEvent(asPerson(users[r.RequesterID]), viewOf(&ev, hosts[userContext{r.RequesterID, ev.ID}]), requesterRel)
					subject := users[r.SubjectUserID]
					view := viewOf(&ev, hosts[userContext{r.SubjectUserID, ev.ID}])
					occ := occs[*r.OccurrenceID]
					view.Visibility = policy.EffectiveVisibility(ev.Visibility, occ.VisibilityOverride)
					f.OrdinaryEligible = policy.CanRegisterEvent(asPerson(subject), view, gp, subjectRel)
					if subject != nil && subject.Status == models.UserGuest {
						f.OrdinaryEligible = policy.CanRegisterGuest(view, gp)
					}
					f.Block = blocks[addRequestBlockKey{r.SubjectUserID, organizer}]
					f.ExistingAccessActive, f.LatestAccessEndedAt = accessAddFacts(regRows[userContext{r.SubjectUserID, *r.OccurrenceID}], history[accessKey{r.SubjectUserID, *r.OccurrenceID}])
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
					j.Date, j.StartsAt, j.EndsAt, j.TZID = models.FormatCalendarDate(occ.OriginalDate), &occ.StartsAt, &occ.EndsAt, ev.TZID
				}
			}
		} else if r.Purpose == policy.RequestGroupAdd {
			if label, ok := contexts.shown(a, op.actor, contextRef{policy.ContactGroup, r.ContextID}); ok {
				id := r.ContextID
				j.ContextID = &id
				j.GroupName = label
				j.RequesterName, j.SubjectName = names.name(r.RequesterID), names.name(r.SubjectUserID)
			}
		}
		out = append(out, j)
	}
	return out, nil
}

// presentAddRequest is the page of one request.
func (a *App) presentAddRequest(tx *gorm.DB, op *contactOperation, r models.ContactRequest) (addRequestJSON, error) {
	rows, err := a.presentAddRequests(tx, op, []models.ContactRequest{r})
	if err != nil {
		return addRequestJSON{}, err
	}
	return rows[0], nil
}
