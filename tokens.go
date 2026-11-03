package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"fold/models"

	"gorm.io/gorm"
)

// jsonString marshals v, falling back to "{}" (payloads are small maps).
func jsonString(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// tokenTTL: password reset and invite tokens are single use, hashed, and
// valid 24 hours (10.1).
const tokenTTL = 24 * time.Hour

// sqlStillLive is the deadline test for a token or invitation. now() is fixed
// when the transaction begins, so a request that waited for a lock could pass
// it after the deadline. Every deadline check after a lock wait, whether a
// lookup, a rotation or the single-use claim, reads the wall clock instead.
const sqlStillLive = "expires_at > clock_timestamp()"

// newRawToken makes a 256-bit token, encoded for URLs.
func newRawToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hashToken is the SHA-256 of the raw token: what is stored.
func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// createAccountToken stores a single-use hashed token of kind for userID.
// payload carries kind-specific data (for example the new email address).
// It returns the raw token, which goes to the person's email. Issuing a
// token revokes every still-unused token of the same kind for the user, so
// at most one live link exists per kind: requesting a fresh reset link
// kills the previous one instead of leaving it usable until expiry.
func createAccountToken(tx *gorm.DB, kind string, userID uint, payload map[string]any, ttl time.Duration) (string, error) {
	raw, _, err := issueAccountToken(tx, kind, userID, payload, ttl)
	return raw, err
}

// issueAccountToken is createAccountToken for callers that also need the new
// row's ID, such as a job that rotates the capability at delivery time.
func issueAccountToken(tx *gorm.DB, kind string, userID uint, payload map[string]any, ttl time.Duration) (string, uint, error) {
	raw, err := newRawToken()
	if err != nil {
		return "", 0, err
	}
	if err := tx.Where("kind = ? AND user_id = ? AND used_at IS NULL", kind, userID).
		Delete(&models.AccountToken{}).Error; err != nil {
		return "", 0, err
	}
	var payloadJSON string
	if len(payload) > 0 {
		payloadJSON = jsonString(payload)
	} else {
		payloadJSON = "{}"
	}
	tok := models.AccountToken{
		Kind: kind, UserID: userID, TokenHash: hashToken(raw),
		Payload: payloadJSON, ExpiresAt: time.Now().UTC().Add(ttl),
	}
	if err := tx.Create(&tok).Error; err != nil {
		return "", 0, err
	}
	return raw, tok.ID, nil
}

// rotateAccountToken replaces only the capability of one stored token, never
// its payload or deadline. It changes nothing unless that exact row, of that
// kind and user, is still unused and unexpired; changed is false then, so a
// stale caller can neither revive a closed request nor extend its window.
// The caller holds the contact gate and the user lock.
func rotateAccountToken(tx *gorm.DB, id uint, kind string, userID uint) (raw string, changed bool, err error) {
	raw, err = newRawToken()
	if err != nil {
		return "", false, err
	}
	res := tx.Model(&models.AccountToken{}).
		Where("id = ? AND kind = ? AND user_id = ? AND used_at IS NULL AND "+sqlStillLive, id, kind, userID).
		Update("token_hash", hashToken(raw))
	if res.Error != nil {
		return "", false, res.Error
	}
	if res.RowsAffected != 1 {
		return "", false, nil
	}
	return raw, true, nil
}

// errTokenInvalid: the token is unknown, expired, or already used. Callers
// answer the same way for all three, so tokens can't be probed.
var errTokenInvalid = errors.New("invalid or expired token")

// consumeAccountToken finds a live token by its raw value and marks it
// used, atomically. Single use: a second call with the same token fails.
func consumeAccountToken(tx *gorm.DB, kind, raw string) (*models.AccountToken, error) {
	var tok models.AccountToken
	res := tx.Where("kind = ? AND token_hash = ? AND used_at IS NULL AND "+sqlStillLive,
		kind, hashToken(raw)).First(&tok)
	if res.Error != nil {
		return nil, errTokenInvalid
	}
	now := time.Now().UTC()
	res = tx.Model(&models.AccountToken{}).Where("id = ? AND token_hash = ? AND used_at IS NULL AND "+sqlStillLive, tok.ID, hashToken(raw)).
		Update("used_at", now)
	if res.Error != nil || res.RowsAffected == 0 {
		return nil, errTokenInvalid // lost the race: someone else used it
	}
	tok.UsedAt = &now
	return &tok, nil
}

// createInvitationToken makes a raw invitation token and its hash.
func createInvitationToken() (raw, hash string, err error) {
	raw, err = newRawToken()
	if err != nil {
		return "", "", err
	}
	return raw, hashToken(raw), nil
}

// rotateInvitationToken replaces only the capability, never its deadline.
// The caller locks the invitation before rechecking the facts that allow mail.
func rotateInvitationToken(tx *gorm.DB, id uint) (string, error) {
	raw, hash, err := createInvitationToken()
	if err != nil {
		return "", err
	}
	res := tx.Model(&models.Invitation{}).
		Where("id = ? AND accepted_at IS NULL AND revoked_at IS NULL AND "+sqlStillLive, id).
		Update("token_hash", hash)
	if res.Error != nil {
		return "", res.Error
	}
	if res.RowsAffected != 1 {
		return "", errTokenInvalid
	}
	return raw, nil
}

// liveInvitation finds an invitation by its raw token: not accepted and
// not expired.
func liveInvitation(tx *gorm.DB, raw string) (*models.Invitation, error) {
	var inv models.Invitation
	res := tx.Where("token_hash = ? AND accepted_at IS NULL AND revoked_at IS NULL AND "+sqlStillLive,
		hashToken(raw)).First(&inv)
	if res.Error != nil {
		return nil, errTokenInvalid
	}
	return &inv, nil
}
