package main

import (
	"net/http"

	"fold/models"

	"gorm.io/gorm"
)

// errSecuritySession: the session the request carried is no longer current
// (credentials changed, account disabled, erased or signed out elsewhere), so
// it is signed out: 401.
var errSecuritySession = apiErr(http.StatusUnauthorized, "", "unauthorized")

// lockCurrentCredentials binds a password proof to the freshly locked account.
// Callers check expected.CheckPassword before entering the transaction, keeping
// expensive bcrypt work outside the global contact gate. Equality of the hash
// and session epoch makes that proof valid for this exact locked credential.
func lockCurrentCredentials(tx *gorm.DB, expected *models.User) (*models.User, error) {
	fresh, err := lockSessionCredentials(tx, expected)
	if err != nil {
		return nil, err
	}
	if fresh.PasswordHash == nil || expected.PasswordHash == nil || *fresh.PasswordHash != *expected.PasswordHash {
		return nil, errSecuritySession
	}
	return fresh, nil
}

// lockSessionCredentials is the gate and the user lock of a credential writer
// that holds a session: the actor's row FOR NO KEY UPDATE, current for the
// session by sessionCurrent (the same decision authenticatedSessionUser makes).
// A session loaded before an erasure, a disable, a credential change or a
// logout therefore answers errSecuritySession and the caller saves nothing.
func lockSessionCredentials(tx *gorm.DB, me *models.User) (*models.User, error) {
	if err := lockContacts(tx); err != nil {
		return nil, err
	}
	return lockActorIn(tx, me, actorNoKeyUpdate)
}

func replacePasswordIn(tx *gorm.DB, user *models.User, hash string) error {
	if err := endAllSessionsIn(tx, user, map[string]any{
		"password_hash": hash,
		"status":        gorm.Expr("CASE WHEN status = ? THEN ? ELSE status END", models.UserInvited, models.UserActive),
	}); err != nil {
		return err
	}
	// Keep the exact epoch written by this transaction. Reloading after
	// commit could accidentally sign a browser into a later reset's epoch.
	return tx.First(user, user.ID).Error
}
