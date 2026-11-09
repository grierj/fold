package main

import (
	"fold/internal/policy"
	"fold/models"

	"gorm.io/gorm"
)

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

// confirmedGuardianLinks is the one question "which of these teens does this
// guardian have a confirmed link to". Household relation alone and pending,
// rejected or revoked links never count.
func confirmedGuardianLinks(tx *gorm.DB, guardian uint, teens []uint) (map[uint]bool, error) {
	out := map[uint]bool{}
	if len(teens) == 0 {
		return out, nil
	}
	var links []models.GuardianLink
	if err := tx.Where("guardian_user_id = ? AND teen_user_id IN ? AND status = ?", guardian, teens, models.GuardianConfirmed).Find(&links).Error; err != nil {
		return nil, err
	}
	for _, link := range links {
		if link.TeenUserID != nil {
			out[*link.TeenUserID] = true
		}
	}
	return out, nil
}

// confirmedContactGuardians loads only confirmed links for this viewer and
// these teens. Query failure yields no guardian authority.
func (a *App) confirmedContactGuardians(viewer *models.User, users map[uint]*models.User) map[uint]bool {
	if viewer == nil {
		return map[uint]bool{}
	}
	var ids []uint
	for id, u := range users {
		if u.AccountType == models.AccountTeen {
			ids = append(ids, id)
		}
	}
	out, err := confirmedGuardianLinks(a.DB, viewer.ID, ids)
	if err != nil {
		return map[uint]bool{}
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
