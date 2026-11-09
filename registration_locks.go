package main

import (
	"errors"

	foldmail "fold/internal/mail"
	"fold/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Lock order for registration writes. Every writer takes its locks in this
// order; the helpers below are the only places a registration path locks a
// holder, a household or a date.
//
//   - Contact gate first (lockContacts) when the write must serialize with
//     account, leadership and host revocation: manager adds, removal, manual
//     promotion, token cancel. A self-service RSVP takes it only on the retry
//     that finds a live invitation (errOccurrenceGateRetry).
//   - Then the responsible accounts, user rows FOR NO KEY UPDATE in ascending id
//     (the member's own row, or the holder of a household answer:
//     lockRegistrationHolder, lockRedeemingGuest).
//   - Then the household (lockHousehold).
//   - Then the date (lockOccurrence). An account erasure holds the account's row
//     and then wants the date (promoteOnDisable), so a writer that held the date
//     first and wrote the account last could be one side of a cycle.
//   - A guest's signup row is claimed last, in its own statement.
//   - A series write locks contact gate, actor, group, event, then its dates;
//     rooms lock the event, then the date, then the room.
//
// TestErasureHouseholdLockCensus pins who calls lockHousehold.

// lockOccurrence locks one occurrence row for a seat-changing write.
func lockOccurrence(tx *gorm.DB, occurrenceID uint) (*models.EventOccurrence, error) {
	var o models.EventOccurrence
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&o, occurrenceID).Error; err != nil {
		return nil, err
	}
	return &o, nil
}

func lockHousehold(tx *gorm.DB, id uint) error {
	var h models.Household
	return tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&h, id).Error
}

// lockRegistrationHolder binds the row to this occurrence before locking its
// responsible account. It never treats a person ID as a user ID.
func lockRegistrationHolder(tx *gorm.DB, registrationID, occurrenceID uint) error {
	var r models.Registration
	if tx.Where("id = ? AND occurrence_id = ?", registrationID, occurrenceID).First(&r).Error != nil {
		return errRegNotFound
	}
	var holder models.User
	return tx.Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).First(&holder, registrationHolder(r)).Error
}

// lockRedeemingGuest locks the account that already owns a guest signup's
// address, FOR NO KEY UPDATE and read again under that lock, before the date: an
// erased or re-addressed account no longer redeems. found is false when no
// account has the address yet.
func lockRedeemingGuest(tx *gorm.DB, email string) (user models.User, found bool, err error) {
	found = tx.Where("email = ?", email).Limit(1).Find(&user).RowsAffected == 1
	if !found {
		return user, false, nil
	}
	if err := tx.Unscoped().Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).
		Select("id, email, status, deleted_at, name, timezone").First(&user, user.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return user, false, errTokenInvalid
		}
		return user, false, err
	}
	if user.DeletedAt.Valid || foldmail.NormalizeAddress(user.Email) != foldmail.NormalizeAddress(email) {
		return user, false, errTokenInvalid
	}
	return user, true, nil
}
