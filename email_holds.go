package main

import (
	"fold/internal/mail"
	"fold/models"

	"gorm.io/gorm"
)

// lockInvitationEmail serializes everything that can claim one address:
// ordinary and household invitations reserve the same email namespace.
func lockInvitationEmail(tx *gorm.DB, email string) error {
	return tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, ?))", mail.NormalizeAddress(email), invitationEmailLockSeed).Error
}

// What can already hold an address (the audited reason an invitation was refused).
const (
	holdAccount           = "existing_account"
	holdPendingConversion = "pending_conversion"
	holdPendingInvitation = "pending_invitation"
)

// emailHeldBy says what already claims email, or "" when it is free. The
// caller holds lockInvitationEmail, and the deadline test reads the wall clock.
// A soft-deleted account holds its address. A guest row holds it too unless
// guestUpgradable: an ordinary invitation upgrades a guest on the same row
// (C5), but a household conversion creates a new row and would collide with
// the guest. exceptInvitation and exceptRequest name the invitation and
// conversion request being worked on, which do not hold the address against
// themselves.
func emailHeldBy(tx *gorm.DB, email string, exceptInvitation, exceptRequest uint, guestUpgradable bool) (string, error) {
	var existing models.User
	if err := tx.Unscoped().Where("email = ?", email).Limit(1).Find(&existing).Error; err != nil {
		return "", err
	}
	if existing.ID != 0 && !(guestUpgradable && existing.Status == models.UserGuest && !existing.DeletedAt.Valid) {
		return holdAccount, nil
	}
	var n int64
	if err := tx.Model(&models.AccountRequest{}).
		Where("email = ? AND source = ? AND status = ? AND id <> ?", email, models.RequestHouseholdConversion, models.RequestPending, exceptRequest).
		Count(&n).Error; err != nil {
		return "", err
	}
	if n != 0 {
		return holdPendingConversion, nil
	}
	if err := tx.Model(&models.Invitation{}).
		Where("email = ? AND id <> ? AND "+sqlInvitationOpen+" AND "+sqlStillLive, email, exceptInvitation).
		Count(&n).Error; err != nil {
		return "", err
	}
	if n != 0 {
		return holdPendingInvitation, nil
	}
	return "", nil
}
