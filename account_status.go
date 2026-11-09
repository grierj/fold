package main

import (
	"fold/models"

	"gorm.io/gorm"
)

// newAccountStatus is the status a new account starts in: a teen waits for
// account consent, an adult without a password waits for the person to set one
// (a set-password link or an invitation), and anyone else is active.
func newAccountStatus(accountType string, hasPassword bool) string {
	switch {
	case accountType == models.AccountTeen:
		return models.UserPendingConsent
	case hasPassword:
		return models.UserActive
	default:
		return models.UserInvited
	}
}

// setAccountStatusIn is the one place an existing account's status changes
// (an admin edit, a teen's account consent granted or revoked). updates holds
// the new "status" and any other columns the same edit writes; the caller holds
// the contact gate and the user's lock.
//
// Order is fixed by what each step needs to see:
//   - Enabling (disabled -> listed): memberships and registrations come back
//     first, while the row still says disabled, so the kept rows and seats are
//     not counted twice and each registration is judged against the status it
//     is coming back to (DEC-38, DEC-47).
//   - The write itself ends every old session when the status changes, and
//     records the new status.
//   - Disabling (listed -> disabled): the write comes first, so the rest of the
//     transaction (seat counts, the waitlist) sees a disabled account that
//     uses no seats (DEC-38); the account's seats on future dates are then
//     freed and promoted into, and every outstanding token is revoked.
//
// Leadership is not here: an admin handing off a sole leader's groups does it
// before this call, because leaderCount counts listed users, and a teen never
// leads (DEC-18), so account consent has nothing to hand off. Audit rows stay
// with the caller, which knows why the status changed. u.Status is updated.
func (a *App) setAccountStatusIn(tx *gorm.DB, u *models.User, updates map[string]any, actor uint, ip string) error {
	status, _ := updates["status"].(string)
	changing := status != u.Status
	wasDisabled := u.Status == models.UserDisabled
	if wasDisabled && status != models.UserDisabled {
		if err := restoreMemberships(tx, u.ID, actor, ip); err != nil {
			return err
		}
		if err := a.restoreRegistrations(tx, u.ID, status, &actor, ip, a.now().UTC()); err != nil {
			return err
		}
	}
	if changing {
		updates["session_version"] = sessionVersionBump()
	}
	if err := tx.Model(u).Updates(updates).Error; err != nil {
		return err
	}
	u.Status = status
	if status == models.UserDisabled {
		if err := revokeAccountTokens(tx, u.ID); err != nil {
			return err
		}
		if !wasDisabled {
			return a.promoteOnDisable(tx, u.ID, true)
		}
	}
	return nil
}

// upgradeGuestIn turns a guest row into an account on the same row (C5), so
// every guest registration stays theirs. The caller has set the new values on
// guest; cols names the columns to write. The guest's outstanding capabilities
// end with the guest.
func upgradeGuestIn(tx *gorm.DB, guest *models.User, cols ...string) error {
	if err := tx.Model(guest).Select(cols).Updates(guest).Error; err != nil {
		return err
	}
	return revokeAccountTokens(tx, guest.ID)
}
