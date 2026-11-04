package main

import (
	"errors"
	"time"

	"fold/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errSecuritySession = errors.New("account credentials changed")

// lockCurrentCredentials binds a password proof to the freshly locked account.
// Callers check expected.CheckPassword before entering the transaction, keeping
// expensive bcrypt work outside the global contact gate. Equality of the hash
// and session epoch makes that proof valid for this exact locked credential.
func lockCurrentCredentials(tx *gorm.DB, expected *models.User) (*models.User, error) {
	if err := lockContacts(tx); err != nil {
		return nil, err
	}
	var fresh models.User
	if err := tx.Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).First(&fresh, expected.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errSecuritySession
		}
		return nil, err
	}
	if !fresh.CanSignIn() || fresh.SessionVersion != expected.SessionVersion ||
		fresh.PasswordHash == nil || expected.PasswordHash == nil || *fresh.PasswordHash != *expected.PasswordHash {
		return nil, errSecuritySession
	}
	return &fresh, nil
}

// lockSessionCredentials is lockCurrentCredentials for a credential writer
// that holds no fresh password proof, only a session. After the same gate,
// user lock and checks, it also requires the locked row's logout_version to
// match the session's, so the row agrees with every value
// authenticatedSessionUser checks (api.go:371), as checkAvatarSession does.
// A session loaded before an erasure, a disable, a credential change or a
// logout therefore answers errSecuritySession and the caller saves nothing.
func lockSessionCredentials(tx *gorm.DB, me *models.User) (*models.User, error) {
	fresh, err := lockCurrentCredentials(tx, me)
	if err != nil {
		return nil, err
	}
	if fresh.LogoutVersion != me.LogoutVersion {
		return nil, errSecuritySession
	}
	return fresh, nil
}

// lockAccountTokenUser always obtains the contact gate, then the user, before
// any token is claimed/deleted. Credential writers and token issuers use the
// same order; token -> user locking would deadlock against bulk revocation.
func lockAccountTokenUser(tx *gorm.DB, kind, raw string) (*models.User, error) {
	if err := lockContacts(tx); err != nil {
		return nil, err
	}
	var initial models.AccountToken
	if err := tx.Where("kind = ? AND token_hash = ? AND used_at IS NULL AND "+sqlStillLive, kind, hashToken(raw)).First(&initial).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errTokenInvalid
		}
		return nil, err
	}
	var user models.User
	if err := tx.Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).First(&user, initial.UserID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errTokenInvalid
		}
		return nil, err
	}
	if user.Status == models.UserDisabled {
		return nil, errTokenInvalid
	}
	return &user, nil
}

// lockAndConsumeAccountToken is lockAccountTokenUser followed by the atomic
// single-use claim, with one token lookup instead of two. The order is the
// same: contact gate, then the user lock, then the claim. The claim is an
// UPDATE rechecks the hash, expiry and unused state, so a stale read cannot
// claim a rotated token and a racing second redeemer gets errTokenInvalid.
func lockAndConsumeAccountToken(tx *gorm.DB, kind, raw string) (*models.User, *models.AccountToken, error) {
	if err := lockContacts(tx); err != nil {
		return nil, nil, err
	}
	var tok models.AccountToken
	if err := tx.Where("kind = ? AND token_hash = ? AND used_at IS NULL AND "+sqlStillLive, kind, hashToken(raw)).First(&tok).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, errTokenInvalid
		}
		return nil, nil, err
	}
	var user models.User
	if err := tx.Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).First(&user, tok.UserID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, errTokenInvalid
		}
		return nil, nil, err
	}
	if user.Status == models.UserDisabled {
		return nil, nil, errTokenInvalid
	}
	now := time.Now().UTC()
	res := tx.Model(&models.AccountToken{}).
		Where("id = ? AND token_hash = ? AND used_at IS NULL AND "+sqlStillLive, tok.ID, hashToken(raw)).
		Update("used_at", now)
	if res.Error != nil {
		return nil, nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, nil, errTokenInvalid // lost the race: someone else used it
	}
	tok.UsedAt = &now
	return &user, &tok, nil
}

// prepareTokenPassword rejects bogus capabilities before paying bcrypt's cost,
// but does no authorization or consumption. The write transaction must still
// revalidate/claim the token under contact -> user -> token locks. Hashing here
// keeps expensive work outside the global contact gate.
func prepareTokenPassword(db *gorm.DB, kind, raw, plaintext string) (string, error) {
	var token models.AccountToken
	if err := db.Select("id").Where("kind = ? AND token_hash = ? AND used_at IS NULL AND "+sqlStillLive, kind, hashToken(raw)).First(&token).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", errTokenInvalid
		}
		return "", err
	}
	var prepared models.User
	if err := prepared.SetPassword(plaintext); err != nil {
		return "", err
	}
	return *prepared.PasswordHash, nil
}

// revokeAccountTokens runs after the contact gate and user lock. Used rows can
// remain as history; every outstanding capability, of every kind, is revoked.
func revokeAccountTokens(tx *gorm.DB, userID uint) error {
	return tx.Where("user_id = ? AND used_at IS NULL", userID).Delete(&models.AccountToken{}).Error
}

func replacePasswordIn(tx *gorm.DB, user *models.User, hash string) error {
	if err := tx.Model(user).Updates(map[string]any{
		"password_hash":   hash,
		"status":          gorm.Expr("CASE WHEN status = ? THEN ? ELSE status END", models.UserInvited, models.UserActive),
		"session_version": gorm.Expr("session_version + 1"),
	}).Error; err != nil {
		return err
	}
	if err := revokeAccountTokens(tx, user.ID); err != nil {
		return err
	}
	// Keep the exact epoch written by this transaction. Reloading after
	// commit could accidentally sign a browser into a later reset's epoch.
	return tx.First(user, user.ID).Error
}
