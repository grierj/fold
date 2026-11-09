package main

import (
	"errors"
	"fold/models"
	"net/http"
	"reflect"
	"testing"
	"time"

	"gorm.io/gorm"
)

// Issuing a token revokes the previous unused one of the same kind, so
// only one live link exists per person per kind: a fresh password-reset
// request kills the earlier link, and a second email-change request kills
// the first verification link (which would otherwise apply a stale
// address).
func TestSecondTokenRevokesFirst(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "alice", "password1234", models.RoleMember)
	anon := newAPIClient(t, app)
	anon.fetchCSRF()

	// Password reset: the second request revokes the first link.
	w := anon.do("POST", "/api/v1/auth/password-reset", map[string]string{"email": "alice@example.com"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("first reset request: %d %s", w.Code, w.Body.String())
	}
	first := tokenInEmail(t, latestEmail(t, app, "alice@example.com"))
	w = anon.do("POST", "/api/v1/auth/password-reset", map[string]string{"email": "alice@example.com"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("second reset request: %d %s", w.Code, w.Body.String())
	}
	second := tokenInEmail(t, latestEmail(t, app, "alice@example.com"))
	if first == second {
		t.Fatal("second request reused the first token")
	}
	w = anon.do("POST", "/api/v1/auth/password-reset/confirm",
		map[string]string{"token": first, "password": "newpassword1234"}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("revoked reset token confirmed: %d %s", w.Code, w.Body.String())
	}
	w = anon.do("POST", "/api/v1/auth/password-reset/confirm",
		map[string]string{"token": second, "password": "newpassword1234"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("live reset token refused: %d %s", w.Code, w.Body.String())
	}

	// Email change: the first verification link dies when a second change
	// is requested, so it cannot apply the stale address afterwards.
	ac := loginAPI(t, app, "alice", "newpassword1234")
	w = ac.do("PUT", "/api/v1/me", map[string]string{"email": "alice.one@example.com", "currentPassword": "newpassword1234"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("first change request: %d %s", w.Code, w.Body.String())
	}
	stale := tokenInEmail(t, latestEmail(t, app, "alice.one@example.com"))
	w = ac.do("PUT", "/api/v1/me", map[string]string{"email": "alice.two@example.com", "currentPassword": "newpassword1234"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("second change request: %d %s", w.Code, w.Body.String())
	}
	fresh := tokenInEmail(t, latestEmail(t, app, "alice.two@example.com"))
	w = ac.do("POST", "/api/v1/me/email/verify", map[string]string{"token": stale}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("stale verification token applied: %d %s", w.Code, w.Body.String())
	}
	w = ac.do("POST", "/api/v1/me/email/verify", map[string]string{"token": fresh}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("fresh verification token refused: %d %s", w.Code, w.Body.String())
	}
	var u models.User
	if err := app.DB.Where("email = ?", "alice.two@example.com").First(&u).Error; err != nil {
		t.Fatalf("email not changed to the second address: %v", err)
	}
}

// A capability may be rotated after the initial lookup. The final UPDATE must
// still match the presented hash, expiry and unused state, not just the row ID.
func TestConsumeAccountTokenRechecksAtClaim(t *testing.T) {
	for _, change := range []string{"hash", "expiry", "used"} {
		t.Run(change, func(t *testing.T) {
			app := testApp(t)
			user := newTestUser(t, app, "alice", "password1234", models.RoleMember)
			raw, _, err := issueAccountToken(app.DB, models.TokenPasswordReset, user.ID, nil, tokenTTL)
			if err != nil {
				t.Fatal(err)
			}
			reached, release := pauseDBRead(t, app.DB, "consume", "account_tokens")
			_, done := startDBSession(t, app.DB, "consume", func(db *gorm.DB) error {
				return db.Transaction(func(tx *gorm.DB) error {
					_, err := consumeAccountToken(tx, models.TokenPasswordReset, raw)
					return err
				})
			})
			awaitDBSignal(t, reached)
			updates := map[string]any{}
			switch change {
			case "hash":
				updates["token_hash"] = hashToken("replacement-capability")
			case "expiry":
				updates["expires_at"] = time.Now().Add(-time.Hour)
			case "used":
				updates["used_at"] = time.Now()
			}
			if err := app.DB.Model(&models.AccountToken{}).Where("token_hash = ?", hashToken(raw)).Updates(updates).Error; err != nil {
				t.Fatal(err)
			}
			release()
			if err := awaitDBResult(t, done); !errors.Is(err, errTokenInvalid) {
				t.Fatalf("stale %s claim succeeded: %v", change, err)
			}
			if change == "hash" {
				// The failed stale claim must leave the rotated capability usable.
				if _, err := consumeAccountToken(app.DB, models.TokenPasswordReset, "replacement-capability"); err != nil {
					t.Fatalf("stale claim consumed the replacement: %v", err)
				}
			}
		})
	}
}

func TestRotateInvitationTokenPreservesDeadlineAndRequiresLiveState(t *testing.T) {
	for _, state := range []string{"live", "accepted", "revoked", "expired", "missing"} {
		t.Run(state, func(t *testing.T) {
			app := testApp(t)
			inviter := newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
			inv := models.Invitation{Email: "new@example.com", InvitedBy: inviter.ID, TokenHash: hashToken("old"), ExpiresAt: time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)}
			now := time.Now().UTC()
			switch state {
			case "accepted":
				inv.AcceptedAt = &now
			case "revoked":
				inv.RevokedAt = &now
			case "expired":
				inv.ExpiresAt = now.Add(-time.Hour).Truncate(time.Microsecond)
			}
			if err := app.DB.Create(&inv).Error; err != nil {
				t.Fatal(err)
			}
			if state == "missing" {
				if err := app.DB.Delete(&inv).Error; err != nil {
					t.Fatal(err)
				}
			}
			raw, err := rotateInvitationToken(app.DB, inv.ID)
			if state != "live" {
				if !errors.Is(err, errTokenInvalid) || raw != "" {
					t.Fatalf("closed invitation rotation = %q, %v", raw, err)
				}
				if state != "missing" && invitationIn(t, app, inv.ID).TokenHash != inv.TokenHash {
					t.Fatal("ineligible invitation changed its hash")
				}
				return
			}
			if err != nil || raw == "" || raw == "old" {
				t.Fatalf("live rotation = %q, %v", raw, err)
			}
			got := invitationIn(t, app, inv.ID)
			if got.TokenHash != hashToken(raw) || !got.ExpiresAt.Equal(inv.ExpiresAt) {
				t.Fatal("rotation did not change only the hash")
			}
			if _, err := liveInvitation(app.DB, "old"); !errors.Is(err, errTokenInvalid) {
				t.Fatal("rotation kept the old capability alive")
			}
			if _, err := liveInvitation(app.DB, raw); err != nil {
				t.Fatalf("replacement capability is unusable: %v", err)
			}
		})
	}
}

func TestRotateAccountTokenPreservesPayloadAndDeadlineAndRequiresLiveState(t *testing.T) {
	for _, state := range []string{"live", "used", "expired", "deleted", "wrong kind", "wrong user"} {
		t.Run(state, func(t *testing.T) {
			app := testApp(t)
			user := newTestUser(t, app, "alice", "password1234", models.RoleMember)
			other := newTestUser(t, app, "bob", "password1234", models.RoleMember)
			if _, _, err := issueAccountToken(app.DB, models.TokenEmailVerify, user.ID, map[string]any{"new_email": "new@example.com"}, time.Hour); err != nil {
				t.Fatal(err)
			}
			var before models.AccountToken
			if err := app.DB.Where("user_id = ?", user.ID).First(&before).Error; err != nil {
				t.Fatal(err)
			}
			kind, userID := models.TokenEmailVerify, user.ID
			switch state {
			case "used":
				if err := app.DB.Model(&before).Update("used_at", time.Now()).Error; err != nil {
					t.Fatal(err)
				}
			case "expired":
				if err := app.DB.Model(&before).Update("expires_at", time.Now().Add(-time.Minute)).Error; err != nil {
					t.Fatal(err)
				}
			case "deleted":
				if err := app.DB.Delete(&before).Error; err != nil {
					t.Fatal(err)
				}
			case "wrong kind":
				kind = models.TokenEmailChangeCancel
			case "wrong user":
				userID = other.ID
			}
			var held models.AccountToken
			if state != "deleted" {
				if err := app.DB.First(&held, before.ID).Error; err != nil {
					t.Fatal(err)
				}
			}
			raw, changed, err := rotateAccountToken(app.DB, before.ID, kind, userID)
			if err != nil {
				t.Fatal(err)
			}
			if state != "live" {
				if changed || raw != "" {
					t.Fatalf("%s token rotated: %q %v", state, raw, changed)
				}
				if state != "deleted" {
					var got models.AccountToken
					if err := app.DB.First(&got, before.ID).Error; err != nil || !reflect.DeepEqual(got, held) {
						t.Fatalf("%s token changed: %+v vs %+v (%v)", state, got, held, err)
					}
				}
				return
			}
			if !changed || raw == "" {
				t.Fatalf("live rotation = %q, %v", raw, changed)
			}
			var got models.AccountToken
			if err := app.DB.First(&got, before.ID).Error; err != nil {
				t.Fatal(err)
			}
			if got.TokenHash != hashToken(raw) || got.TokenHash == before.TokenHash || !got.ExpiresAt.Equal(before.ExpiresAt) ||
				got.Payload != before.Payload || got.UsedAt != nil || got.UserID != before.UserID || got.Kind != before.Kind {
				t.Fatalf("rotation must change only the hash: %+v -> %+v", before, got)
			}
		})
	}
}

func TestRotateGuestSignupTokenPreservesDeadlineAndRequiresLiveState(t *testing.T) {
	for _, state := range []string{"live", "confirmed", "expired", "missing"} {
		t.Run(state, func(t *testing.T) {
			app := testApp(t)
			f := queueHeldGuestVerify(t, app, false)
			signup := f.signup
			switch state {
			case "confirmed":
				if err := app.DB.Model(&models.GuestSignup{}).Where("id = ?", signup.ID).Update("confirmed_at", time.Now()).Error; err != nil {
					t.Fatal(err)
				}
			case "expired":
				if err := app.DB.Model(&models.GuestSignup{}).Where("id = ?", signup.ID).Update("expires_at", time.Now().Add(-time.Minute)).Error; err != nil {
					t.Fatal(err)
				}
			case "missing":
				if err := app.DB.Delete(&models.GuestSignup{}, signup.ID).Error; err != nil {
					t.Fatal(err)
				}
			}
			before := models.GuestSignup{}
			app.DB.Where("id = ?", signup.ID).Limit(1).Find(&before)
			raw, err := rotateGuestSignupToken(app.DB, signup.ID)
			if state != "live" {
				if !errors.Is(err, errTokenInvalid) || raw != "" {
					t.Fatalf("closed signup rotation = %q, %v", raw, err)
				}
				if state != "missing" && guestSignupIn(t, app, signup.ID).TokenHash != before.TokenHash {
					t.Fatal("ineligible signup changed its hash")
				}
				return
			}
			if err != nil || raw == "" {
				t.Fatalf("live rotation = %q, %v", raw, err)
			}
			got := guestSignupIn(t, app, signup.ID)
			if got.TokenHash != hashToken(raw) || got.TokenHash == signup.TokenHash || !got.ExpiresAt.Equal(signup.ExpiresAt) ||
				got.ConfirmedAt != nil || got.Email != signup.Email || got.Name != signup.Name || got.OccurrenceID != signup.OccurrenceID {
				t.Fatalf("rotation did not change only the hash: %+v", got)
			}
		})
	}
}

// A claim made with a hash that was read before a rotation, or after the
// deadline, or twice, claims nothing and says so.
func TestClaimGuestSignupRequiresTheHashItReadAndALiveDeadline(t *testing.T) {
	app := testApp(t)
	f := queueHeldGuestVerify(t, app, false)
	stale := f.signup // carries the hash read before the rotation below
	if _, err := rotateGuestSignupToken(app.DB, stale.ID); err != nil {
		t.Fatal(err)
	}
	if err := claimGuestSignup(app.DB, &stale, time.Now()); !errors.Is(err, errTokenInvalid) {
		t.Fatalf("a stale hash claimed the rotated row: %v", err)
	}
	current := guestSignupIn(t, app, stale.ID)
	if current.ConfirmedAt != nil {
		t.Fatal("the stale claim marked the link used")
	}
	if err := app.DB.Model(&models.GuestSignup{}).Where("id = ?", current.ID).Update("expires_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	if err := claimGuestSignup(app.DB, &current, time.Now()); !errors.Is(err, errTokenInvalid) {
		t.Fatalf("an expired link was claimed: %v", err)
	}
	if err := app.DB.Model(&models.GuestSignup{}).Where("id = ?", current.ID).Update("expires_at", time.Now().Add(time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	if err := claimGuestSignup(app.DB, &current, time.Now()); err != nil {
		t.Fatalf("the current link could not be claimed: %v", err)
	}
	if err := claimGuestSignup(app.DB, &current, time.Now()); !errors.Is(err, errTokenInvalid) {
		t.Fatalf("a link was claimed twice: %v", err)
	}
}
