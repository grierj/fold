package main

// The token module: every capability Fold mails or hands out (account tokens,
// invitations, guest verifications) is made, found, claimed, rotated and
// revoked here, once. Lock order for account tokens is the contact gate, then
// the user, then the token (AGENTS "Direct account mail"); token -> user
// locking would deadlock against bulk revocation.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"fold/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

// tokenLifetime is tokenTTL as the emails and answers that mention it say it
// ("24 hours"), so the words follow the number.
var tokenLifetime = strconv.Itoa(int(tokenTTL/time.Hour)) + " hours"

// sqlStillLive is the deadline test for a token or invitation. now() is fixed
// when the transaction begins, so a request that waited for a lock could pass
// it after the deadline. Every deadline check after a lock wait, whether a
// lookup, a rotation or the single-use claim, reads the wall clock instead.
const sqlStillLive = "expires_at > clock_timestamp()"

// A raw token is rawTokenBytes of randomness; base64url without padding turns
// n bytes into ceil(8n/6) characters. A stored hash (SHA-256) and an HMAC
// signature have the second length. The guardian email link is two of them.
const (
	rawTokenBytes = 32
	rawTokenLen   = (rawTokenBytes*8 + 5) / 6
	digestLen     = (sha256.Size*8 + 5) / 6
)

// newRawToken makes a 256-bit token, encoded for URLs.
func newRawToken() (string, error) {
	b := make([]byte, rawTokenBytes)
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

// newTokenAndHash makes a raw capability and its stored hash, for invitations,
// guest verifications, registrations and conversions.
func newTokenAndHash() (raw, hash string, err error) {
	raw, err = newRawToken()
	if err != nil {
		return "", "", err
	}
	return raw, hashToken(raw), nil
}

// errTokenInvalid: the token is unknown, expired, or already used. Callers
// answer the same way for all three, so tokens can't be probed.
var errTokenInvalid = errors.New("invalid or expired token")

// liveAccountToken scopes tx to the unused, unexpired account token of kind
// whose stored hash matches raw. Every lookup, claim and probe of a presented
// capability goes through it.
func liveAccountToken(tx *gorm.DB, kind, raw string) *gorm.DB {
	return tx.Where("kind = ? AND token_hash = ? AND used_at IS NULL AND "+sqlStillLive, kind, hashToken(raw))
}

// findAccountToken returns the live token for raw: errTokenInvalid when there
// is none, any other error when the read failed.
func findAccountToken(tx *gorm.DB, kind, raw string) (*models.AccountToken, error) {
	var tok models.AccountToken
	res := liveAccountToken(tx, kind, raw).Limit(1).Find(&tok)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, errTokenInvalid
	}
	return &tok, nil
}

// claimAccountToken is the one single-use claim: an UPDATE that rechecks the
// hash, the unused state and the deadline, so a stale read cannot claim a
// rotated token and a racing second redeemer gets errTokenInvalid.
func claimAccountToken(tx *gorm.DB, tok *models.AccountToken, raw string) error {
	now := time.Now().UTC()
	res := liveAccountToken(tx.Model(&models.AccountToken{}).Where("id = ?", tok.ID), tok.Kind, raw).
		Update("used_at", now)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errTokenInvalid // lost the race: someone else used it
	}
	tok.UsedAt = &now
	return nil
}

// consumeAccountToken finds a live token by its raw value and marks it used.
// The caller already holds the locks of its flow (the contact gate and the
// rows the token authorizes). Single use: a second call fails.
func consumeAccountToken(tx *gorm.DB, kind, raw string) (*models.AccountToken, error) {
	tok, err := findAccountToken(tx, kind, raw)
	if err != nil {
		return nil, err
	}
	if err := claimAccountToken(tx, tok, raw); err != nil {
		return nil, err
	}
	return tok, nil
}

// lockAndConsumeAccountToken redeems a token for a flow whose authority is
// only the token's user: contact gate, then the user lock (a disabled account
// redeems nothing), then the claim.
func lockAndConsumeAccountToken(tx *gorm.DB, kind, raw string) (*models.User, *models.AccountToken, error) {
	if err := lockContacts(tx); err != nil {
		return nil, nil, err
	}
	tok, err := findAccountToken(tx, kind, raw)
	if err != nil {
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
	if err := claimAccountToken(tx, tok, raw); err != nil {
		return nil, nil, err
	}
	return &user, tok, nil
}

// prepareTokenPassword rejects bogus capabilities before paying bcrypt's cost,
// but does no authorization or consumption. The write transaction must still
// revalidate/claim the token under contact -> user -> token locks. Hashing here
// keeps expensive work outside the global contact gate.
func prepareTokenPassword(db *gorm.DB, kind, raw, plaintext string) (string, error) {
	if _, err := findAccountToken(db, kind, raw); err != nil {
		return "", err
	}
	var prepared models.User
	if err := prepared.SetPassword(plaintext); err != nil {
		return "", err
	}
	return *prepared.PasswordHash, nil
}

// storeAccountToken revokes the user's unused tokens of kind that match
// replaces (all of them when replaces is empty) and stores a new one. It
// returns the new row's id.
func storeAccountToken(tx *gorm.DB, raw, kind string, userID uint, payload string, expires time.Time, replaces string, replacesArgs ...any) (uint, error) {
	revoke := tx.Where("kind = ? AND user_id = ? AND used_at IS NULL", kind, userID)
	if replaces != "" {
		revoke = revoke.Where(replaces, replacesArgs...)
	}
	if err := revoke.Delete(&models.AccountToken{}).Error; err != nil {
		return 0, err
	}
	tok := models.AccountToken{Kind: kind, UserID: userID, TokenHash: hashToken(raw), Payload: payload, ExpiresAt: expires}
	if err := tx.Create(&tok).Error; err != nil {
		return 0, err
	}
	return tok.ID, nil
}

// issueAccountToken stores a single-use hashed token of kind for userID.
// payload carries kind-specific data (for example the new email address).
// It returns the raw token, which goes to the person's email, and the row's
// ID, which a job that rotates the capability at delivery time needs. Issuing
// a token revokes every still-unused token of the same kind for the user, so
// at most one live link exists per kind: requesting a fresh reset link kills
// the previous one instead of leaving it usable until expiry.
func issueAccountToken(tx *gorm.DB, kind string, userID uint, payload map[string]any, ttl time.Duration) (string, uint, error) {
	raw, err := newRawToken()
	if err != nil {
		return "", 0, err
	}
	payloadJSON := "{}"
	if len(payload) > 0 {
		payloadJSON = jsonString(payload)
	}
	id, err := storeAccountToken(tx, raw, kind, userID, payloadJSON, time.Now().UTC().Add(ttl), "")
	return raw, id, err
}

// revokeAccountTokens runs after the contact gate and user lock. Used rows can
// remain as history; every outstanding capability, of every kind, is revoked.
func revokeAccountTokens(tx *gorm.DB, userID uint) error {
	return tx.Where("user_id = ? AND used_at IS NULL", userID).Delete(&models.AccountToken{}).Error
}

// rotateTokenHash is the one rotation: it replaces only the capability of the
// row model/where selects (which carries the live-state test), never its
// payload or deadline, and reports whether that row was still live, so a
// stale caller can neither revive a closed request nor extend its window.
// The caller holds the row's lock.
func rotateTokenHash(tx *gorm.DB, model any, where string, args ...any) (raw string, changed bool, err error) {
	raw, hash, err := newTokenAndHash()
	if err != nil {
		return "", false, err
	}
	res := tx.Model(model).Where(where, args...).Update("token_hash", hash)
	if res.Error != nil {
		return "", false, res.Error
	}
	if res.RowsAffected != 1 {
		return "", false, nil
	}
	return raw, true, nil
}

// rotateAccountToken rotates one stored account token. changed is false unless
// that exact row, of that kind and user, is still unused and unexpired. The
// caller holds the contact gate and the user lock.
func rotateAccountToken(tx *gorm.DB, id uint, kind string, userID uint) (raw string, changed bool, err error) {
	return rotateTokenHash(tx, &models.AccountToken{},
		"id = ? AND kind = ? AND user_id = ? AND used_at IS NULL AND "+sqlStillLive, id, kind, userID)
}

// sqlInvitationOpen: not accepted, not revoked.
const sqlInvitationOpen = "accepted_at IS NULL AND revoked_at IS NULL"

// rotateInvitationToken replaces only the capability, never its deadline.
// The caller locks the invitation before rechecking the facts that allow mail.
func rotateInvitationToken(tx *gorm.DB, id uint) (string, error) {
	raw, changed, err := rotateTokenHash(tx, &models.Invitation{}, "id = ? AND "+sqlInvitationOpen+" AND "+sqlStillLive, id)
	if err != nil {
		return "", err
	}
	if !changed {
		return "", errTokenInvalid
	}
	return raw, nil
}

// rotateGuestSignupToken replaces only the capability of a pending guest
// verification, never its deadline. It changes nothing unless that row is
// still unconfirmed and unexpired. The caller holds the signup row lock.
func rotateGuestSignupToken(tx *gorm.DB, id uint) (string, error) {
	raw, changed, err := rotateTokenHash(tx, &models.GuestSignup{}, "id = ? AND confirmed_at IS NULL AND "+sqlStillLive, id)
	if err != nil {
		return "", err
	}
	if !changed {
		return "", errTokenInvalid
	}
	return raw, nil
}

// liveInvitation finds an invitation by its raw token: not accepted, not
// revoked and not expired.
func liveInvitation(tx *gorm.DB, raw string) (*models.Invitation, error) {
	var inv models.Invitation
	res := tx.Where("token_hash = ? AND "+sqlInvitationOpen+" AND "+sqlStillLive, hashToken(raw)).Limit(1).Find(&inv)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, errTokenInvalid
	}
	return &inv, nil
}

// claimInvitation is the single-use claim of an invitation: it marks the row
// accepted only if the presented capability is still its hash, open and live.
func claimInvitation(tx *gorm.DB, id uint, raw string, at time.Time) error {
	res := tx.Model(&models.Invitation{}).
		Where("id = ? AND token_hash = ? AND "+sqlInvitationOpen+" AND "+sqlStillLive, id, hashToken(raw)).
		Update("accepted_at", at)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return errTokenInvalid // rotated, revoked or claimed by someone else first
	}
	return nil
}
