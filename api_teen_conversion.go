package main

import (
	"errors"
	"sort"
	"time"

	"fold/internal/accounts"
	"fold/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errConversionConsent = errors.New("this approved account is waiting for consent and guardian confirmation; ask your church office before changing their identity or removing them")

type stagedTeenConversion struct {
	request    models.AccountRequest
	person     models.HouseholdPerson
	invitation models.Invitation
}

// ApprovedUserID is the identity of the account, including after its email changes.
func stagedTeenConversionIn(tx *gorm.DB, userID uint) (*stagedTeenConversion, error) {
	var stage stagedTeenConversion
	err := tx.Where("source = ? AND status = ? AND approved_user_id = ?", models.RequestHouseholdConversion, models.RequestApproved, userID).First(&stage.request).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if stage.request.HouseholdPersonID == nil || stage.request.InvitationID == nil {
		return nil, errConversionUnavailable
	}
	if err := tx.Unscoped().First(&stage.person, *stage.request.HouseholdPersonID).Error; err != nil {
		return nil, err
	}
	if stage.person.ConvertedUserID != nil {
		return nil, nil
	}
	if err := tx.First(&stage.invitation, *stage.request.InvitationID).Error; err != nil {
		return nil, err
	}
	return &stage, nil
}

func approvedConversionForPerson(tx *gorm.DB, personID uint) (bool, error) {
	var count int64
	err := tx.Model(&models.AccountRequest{}).Where("household_person_id = ? AND source = ? AND status = ? AND approved_user_id IS NOT NULL", personID, models.RequestHouseholdConversion, models.RequestApproved).Count(&count).Error
	return count != 0, err
}

// All conversion participants are discovered before any user lock is taken.
func lockConversionParticipants(tx *gorm.DB, inv *models.Invitation, person *models.HouseholdPerson, extraIDs ...uint) (map[uint]*models.User, []uint, error) {
	ids, err := conversionLockIDs(tx, inv, person)
	if err != nil {
		return nil, nil, err
	}
	ids = append(ids, extraIDs...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	unique := ids[:0]
	for _, id := range ids {
		if id != 0 && (len(unique) == 0 || unique[len(unique)-1] != id) {
			unique = append(unique, id)
		}
	}
	ids = unique
	var rows []models.User
	if err := tx.Unscoped().Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).Where("id IN ?", ids).Order("id").Find(&rows).Error; err != nil {
		return nil, nil, err
	}
	locked := make(map[uint]*models.User, len(rows))
	for i := range rows {
		locked[rows[i].ID] = &rows[i]
	}
	if err := lockHousehold(tx, person.HouseholdID); err != nil {
		return nil, nil, err
	}
	if err := tx.Unscoped().First(person, person.ID).Error; err != nil {
		return nil, nil, err
	}
	fresh, err := conversionLockIDs(tx, inv, person)
	if err != nil {
		return nil, nil, err
	}
	for _, id := range fresh {
		if locked[id] == nil {
			return nil, nil, errConversionRetry
		}
	}
	return locked, ids, nil
}

// lockTeenLifecycleUsers also locks a staged conversion's household before a
// caller locks a guardian link, a group or an occurrence.
func lockTeenLifecycleUsers(tx *gorm.DB, userID uint, extraIDs ...uint) (map[uint]*models.User, error) {
	stage, err := stagedTeenConversionIn(tx, userID)
	if err != nil {
		return nil, err
	}
	ids := append([]uint{userID}, extraIDs...)
	if stage != nil {
		locked, _, err := lockConversionParticipants(tx, &stage.invitation, &stage.person, ids...)
		return locked, err
	}
	var users []models.User
	if err := tx.Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).Where("id IN ?", ids).Order("id").Find(&users).Error; err != nil {
		return nil, err
	}
	locked := make(map[uint]*models.User, len(users))
	for i := range users {
		locked[users[i].ID] = &users[i]
	}
	return locked, nil
}

// completeTeenConversionIfReady is called with lockTeenLifecycleUsers held.
// At eighteen the same operation completes after the age job makes the user an
// adult. Consent and guardian requirements then end; an admin suspension does not.
func (a *App) completeTeenConversionIfReady(tx *gorm.DB, user *models.User, today time.Time, ip string, mailed *[]uint) error {
	stage, err := stagedTeenConversionIn(tx, user.ID)
	if err != nil || stage == nil {
		return err
	}
	person, req, inv := &stage.person, &stage.request, &stage.invitation
	if person.DeletedAt.Valid || person.ConvertedUserID != nil || req.ApprovedUserID == nil || *req.ApprovedUserID != user.ID {
		return errConversionPerson
	}
	if person.FullName() != req.Name || formatBirthDate(person.BirthDate) != formatBirthDate(req.BirthDate) || formatBirthDate(user.BirthDate) != formatBirthDate(req.BirthDate) {
		return errConversionDetails
	}
	if user.DeletedAt.Valid || user.AdministrativelyDisabled {
		return nil
	}
	ready := false
	if user.BirthDate != nil {
		age := accounts.AgeOn(*user.BirthDate, today)
		switch user.AccountType {
		case models.AccountAdult:
			ready = age >= 18
		case models.AccountTeen:
			granted, err := latestTeenConsentIn(tx, user.ID, models.TeenConsentAccount)
			if err != nil {
				return err
			}
			if age >= 13 && age < 18 && granted {
				var count int64
				err := tx.Table("guardian_links AS teen_link").Joins("JOIN guardian_links AS person_link ON person_link.guardian_user_id = teen_link.guardian_user_id AND person_link.person_id = ? AND person_link.status = ?", person.ID, models.GuardianConfirmed).
					Joins("JOIN users AS guardian ON guardian.id = teen_link.guardian_user_id AND guardian.status = ? AND guardian.account_type = ? AND guardian.deleted_at IS NULL", models.UserActive, models.AccountAdult).
					Where("teen_link.teen_user_id = ? AND teen_link.status = ?", user.ID, models.GuardianConfirmed).Count(&count).Error
				if err != nil {
					return err
				}
				ready = count > 0
			}
		}
	}
	if !ready {
		if user.Status == models.UserActive {
			if err := tx.Model(user).Update("status", models.UserPendingConsent).Error; err != nil {
				return err
			}
			user.Status = models.UserPendingConsent
		}
		return nil
	}
	ids, err := conversionLockIDs(tx, inv, person)
	if err != nil {
		return err
	}
	ids = append(ids, user.ID)
	var holders []models.User
	if err := tx.Unscoped().Where("id IN ?", ids).Find(&holders).Error; err != nil {
		return err
	}
	locked := make(map[uint]*models.User, len(holders))
	for i := range holders {
		locked[holders[i].ID] = &holders[i]
	}
	if err := tx.Model(user).Update("status", models.UserActive).Error; err != nil {
		return err
	}
	user.Status = models.UserActive
	return a.transferHouseholdConversion(tx, inv, person, locked, ids, user, ip, mailed)
}
