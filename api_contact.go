package main

import (
	"fold/internal/policy"
	"fold/models"
)

// contactPrivacyChanged records a person's own preference change, without
// copying their contact details into audit metadata.
const contactPrivacyChanged = "contact.privacy_changed"

// contactDetails never returns hidden values, even as empty JSON keys. Context
// facts come from the collection being rendered, not the viewer's other roles.
func contactDetails(viewer, subject *models.User, context policy.Contact) (email, phone *string) {
	if subject == nil {
		return nil, nil
	}
	context.Teen = subject.AccountType == models.AccountTeen
	context.HideFromLeaders = subject.HideContactFromLeaders
	if !policy.CanSeeContact(asPerson(viewer), viewer != nil && viewer.ID == subject.ID, context) {
		return nil, nil
	}
	return &subject.Email, &subject.Phone
}

// confirmedContactGuardians loads only confirmed links for this viewer and
// these teens. Query failure yields no guardian authority. Household relation
// alone and pending/rejected/revoked links never authorize contact disclosure.
func (a *App) confirmedContactGuardians(viewer *models.User, users map[uint]*models.User) map[uint]bool {
	out := map[uint]bool{}
	if viewer == nil {
		return out
	}
	var ids []uint
	for id, u := range users {
		if u.AccountType == models.AccountTeen {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return out
	}
	var links []models.GuardianLink
	if err := a.DB.Where("guardian_user_id = ? AND teen_user_id IN ? AND status = ?", viewer.ID, ids, models.GuardianConfirmed).Find(&links).Error; err != nil {
		return out
	}
	for _, link := range links {
		if link.TeenUserID != nil {
			out[*link.TeenUserID] = true
		}
	}
	return out
}

// presentAttendeeContacts uses the already loaded subjects. A household person
// has no contact details of their own: never substitute the account holder's.
func presentAttendeeContacts(rows []attendeeJSON, viewer *models.User, users map[uint]*models.User, guardians map[uint]bool, manager bool) {
	for i := range rows {
		r := &rows[i]
		if r.UserID == nil || r.HouseholdPerson != nil {
			continue
		}
		r.Email, r.Phone = contactDetails(viewer, users[*r.UserID], policy.Contact{
			EventManager:      manager,
			EventRegistrant:   r.Status != models.RegRemoved && r.Status != models.RegCancelled,
			ConfirmedGuardian: guardians[*r.UserID],
		})
	}
}
