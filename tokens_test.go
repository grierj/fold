package main

import (
	"fold/models"
	"net/http"
	"testing"
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
	w = ac.do("PUT", "/api/v1/me", map[string]string{"email": "alice.one@example.com"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("first change request: %d %s", w.Code, w.Body.String())
	}
	stale := tokenInEmail(t, latestEmail(t, app, "alice.one@example.com"))
	w = ac.do("PUT", "/api/v1/me", map[string]string{"email": "alice.two@example.com"}, true)
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
