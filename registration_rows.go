package main

import (
	"time"

	"fold/models"

	"gorm.io/gorm"
)

// Registration rows are written here. saveRegistration is the one writer of an
// answer (insert or update); promoteRegistration is the one transition that
// keeps a waiter's history. Both leave the exact-date access columns alone:
// those are written only by registration_access.go. Callers set the fields
// that carry the decision (status, mode, guests, childcare, removal note) and
// the writer clears what that status cannot hold.

// saveRegistration writes r in full and returns it as stored, so a caller that
// reads r afterwards sees the cleared fields.
//   - waitlisted keeps its place in line and nothing else changes;
//   - going has no wait: waitlisted_at and promoted_at are cleared;
//   - cancelled clears them too and keeps an account row's manage link live,
//     because a second cancel from the same link is a no-op and reopening a
//     date can restore access; a household row's link dies with the cancel,
//     and so does any other status's;
//   - removal fields exist only on a removed row.
func saveRegistration(tx *gorm.DB, r *models.Registration) error {
	switch r.Status {
	case models.RegWaitlisted:
	case models.RegGoing:
		r.WaitlistedAt, r.PromotedAt = nil, nil
	case models.RegCancelled:
		r.WaitlistedAt, r.PromotedAt = nil, nil
		if r.HouseholdPersonID != nil {
			r.ManageTokenHash = ""
		}
	default:
		r.WaitlistedAt, r.PromotedAt, r.ManageTokenHash = nil, nil, ""
	}
	if r.Status != models.RegRemoved {
		r.RemovedBy, r.RemovalNote = nil, ""
	}
	return tx.Omit(registrationSaveOmits()...).Save(r).Error
}

// promoteRegistration moves a waiter to going: waitlisted_at stays as history,
// promoted_at is stamped and the rotated manage link is stored. A row that is no
// longer waitlisted (another seat-changing write got there first) is left alone
// and reported false.
func promoteRegistration(tx *gorm.DB, r models.Registration, manageHash string, now time.Time) (bool, error) {
	res := tx.Model(&models.Registration{}).
		Where("id = ? AND status = ?", r.ID, models.RegWaitlisted).
		Updates(map[string]any{"status": models.RegGoing, "promoted_at": now, "manage_token_hash": manageHash})
	return res.RowsAffected > 0, res.Error
}
