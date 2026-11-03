package main

import (
	"errors"
	"fold/models"
	"net/http"
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
			raw, err := createAccountToken(app.DB, models.TokenPasswordReset, user.ID, nil, tokenTTL)
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
