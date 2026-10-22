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
	raw, err := newRawToken()
	if err != nil {
		return "", err
	}
	if err := tx.Where("kind = ? AND user_id = ? AND used_at IS NULL", kind, userID).
		Delete(&models.AccountToken{}).Error; err != nil {
		return "", err
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
		return "", err
	}
	return raw, nil
}

// errTokenInvalid: the token is unknown, expired, or already used. Callers
// answer the same way for all three, so tokens can't be probed.
var errTokenInvalid = errors.New("invalid or expired token")

// consumeAccountToken finds a live token by its raw value and marks it
// used, atomically. Single use: a second call with the same token fails.
func consumeAccountToken(tx *gorm.DB, kind, raw string) (*models.AccountToken, error) {
	var tok models.AccountToken
	res := tx.Where("kind = ? AND token_hash = ? AND used_at IS NULL AND expires_at > now()",
		kind, hashToken(raw)).First(&tok)
	if res.Error != nil {
		return nil, errTokenInvalid
	}
	now := time.Now().UTC()
	res = tx.Model(&models.AccountToken{}).Where("id = ? AND used_at IS NULL", tok.ID).
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

// liveInvitation finds an invitation by its raw token: not accepted and
// not expired.
func liveInvitation(tx *gorm.DB, raw string) (*models.Invitation, error) {
	var inv models.Invitation
	res := tx.Where("token_hash = ? AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now()",
		hashToken(raw)).First(&inv)
	if res.Error != nil {
		return nil, errTokenInvalid
	}
	return &inv, nil
}
