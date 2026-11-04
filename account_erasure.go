package main

import (
	"fmt"

	"gorm.io/gorm"

	"fold/internal/mail"
	"fold/models"
)

// erasureSubject is what an account erasure needs to remember about the
// person after their row is scrubbed: the id and the address the account held
// at erasure. R4-2a extends it with the REQ, INV and PERS sets, computed in
// the same place before the first scrub.
//
// It lives only in memory inside the erasure transaction. It is never logged,
// audited, queued, serialized, put in an error or returned.
type erasureSubject struct {
	ID      uint
	Address string
}

// captureErasureSubject reads the subject from the row the erasure has just
// locked FOR UPDATE and reloaded into u (deletionHostPlan), so the address is
// the one the account holds now and never a past one.
func captureErasureSubject(u *models.User) erasureSubject {
	return erasureSubject{ID: u.ID, Address: mail.NormalizeAddress(u.Email)}
}

// eraseAccountCredentialsIn deletes everything that lets someone act as the
// erased account later: every account token (used or not, of every kind), the
// calendar link and the push devices. It runs after the contact gate and the
// account lock, in the slot revokeAccountTokens held, and deleting devices
// before the session bump means the push trigger finds none and queues no
// notice. It returns the first error unwrapped and logs nothing.
func eraseAccountCredentialsIn(tx *gorm.DB, s erasureSubject) error {
	if err := tx.Exec("DELETE FROM account_tokens WHERE user_id = ?", s.ID).Error; err != nil {
		return err
	}
	if err := tx.Exec("DELETE FROM calendar_tokens WHERE user_id = ?", s.ID).Error; err != nil {
		return err
	}
	return tx.Exec("DELETE FROM push_subscriptions WHERE user_id = ?", s.ID).Error
}

// eraseOwnedThrottleKeysIn deletes the sign-in and mail-budget rows the
// account owns (ownedThrottleKeys): one statement, a bound list, exact
// equality and never LIKE. It is separate from eraseAccountCredentialsIn only
// because it runs last, so the erasure holds throttle rows for the shortest
// time before commit.
//
// This is detected limit D2: the IN-list delete, reserveLoginAttempt,
// reauthenticate's refund and pruneLoginThrottles lock rows in scan or call
// order, not one key order, so Postgres can find a cycle and abort one side.
// The erasure is not deadlock-free. An aborted erasure rolls back whole and
// answers its usual 500. It returns the error unwrapped and logs nothing.
func eraseOwnedThrottleKeysIn(tx *gorm.DB, s erasureSubject) error {
	return tx.Exec("DELETE FROM login_throttles WHERE key IN ?", ownedThrottleKeys(s.ID, s.Address)).Error
}

// tombstoneUserIn scrubs the personal columns of the erased account in the
// one UPDATE that also bumps session_version, so every open session ends.
// The row keeps its id, role (O-11), account_type, created_at and
// logout_version; the caller soft-deletes it afterwards. The address becomes
// a placeholder that frees the real one for a new account.
func tombstoneUserIn(tx *gorm.DB, u *models.User) error {
	return tx.Model(u).Updates(map[string]any{
		"email":                     fmt.Sprintf("deleted-%d@deleted.invalid", u.ID),
		"name":                      "",
		"display_name":              "",
		"phone":                     "",
		"timezone":                  "",
		"password_hash":             gorm.Expr("NULL"),
		"birth_date":                gorm.Expr("NULL"),
		"avatar_focal_x":            50,
		"avatar_focal_y":            50,
		"hide_contact_from_leaders": false,
		"avatar_key":                "",
		"status":                    models.UserDisabled,
		"administratively_disabled": true,
		"session_version":           gorm.Expr("session_version + 1"),
	}).Error
}
