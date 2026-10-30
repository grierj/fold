package main

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"fold/models"
)

func TestPasswordRuleError(t *testing.T) {
	for _, tc := range []struct {
		name string
		pw   string
		want string
	}{
		{"too short", strings.Repeat("a", minPasswordLength-1), "password must be at least 10 characters"},
		{"minimum", strings.Repeat("a", minPasswordLength), ""},
		{"72 bytes", strings.Repeat("a", 72), ""},
		{"73 bytes", strings.Repeat("a", 73), "password must be at most 72 bytes"},
		// 36 two-byte letters are 72 bytes; 37 are 74.
		{"72 bytes of accents", strings.Repeat("é", 36), ""},
		{"74 bytes of accents", strings.Repeat("é", 37), "password must be at most 72 bytes"},
	} {
		if got := passwordRuleError(tc.pw); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func assertTooLong(t *testing.T, code int, body string) {
	t.Helper()
	if code != http.StatusBadRequest || !strings.Contains(body, `"code":"password_too_long"`) {
		t.Fatalf("got %d %s, want 400 password_too_long", code, body)
	}
}

func TestPasswordChangeRejectsMoreThan72Bytes(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "alice", "password1234", models.RoleMember)
	c := loginAPI(t, app, "alice", "password1234")
	w := c.do("POST", "/api/v1/me/password", map[string]string{"current": "password1234", "password": strings.Repeat("a", 73)}, true)
	assertTooLong(t, w.Code, w.Body.String())
	// The old password still works, and exactly 72 bytes is accepted.
	loginAPI(t, app, "alice", "password1234")
	apiDo(t, c, "POST", "/api/v1/me/password", map[string]string{"current": "password1234", "password": strings.Repeat("b", 72)}, http.StatusOK)
}

func TestPasswordResetRejectsMoreThan72Bytes(t *testing.T) {
	app := testApp(t)
	user := newTestUser(t, app, "alice", "password1234", models.RoleMember)
	anon := newAPIClient(t, app)
	anon.fetchCSRF()
	raw, err := createAccountToken(app.DB, models.TokenPasswordReset, user.ID, nil, tokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	w := anon.do("POST", "/api/v1/auth/password-reset/confirm", map[string]string{"token": raw, "password": strings.Repeat("a", 73)}, true)
	assertTooLong(t, w.Code, w.Body.String())
	// The token was not spent by the refusal.
	apiDo(t, anon, "POST", "/api/v1/auth/password-reset/confirm", map[string]string{"token": raw, "password": strings.Repeat("a", 72)}, http.StatusOK)
}

func TestAccountSetupRejectsMoreThan72Bytes(t *testing.T) {
	app := testApp(t)
	user := newTestUser(t, app, "alice", "password1234", models.RoleMember)
	anon := newAPIClient(t, app)
	anon.fetchCSRF()
	raw, err := createAccountToken(app.DB, models.TokenAccountSetup, user.ID, nil, tokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	w := anon.do("POST", "/api/v1/account-requests/confirm", map[string]string{"token": raw, "password": strings.Repeat("a", 73)}, true)
	assertTooLong(t, w.Code, w.Body.String())
	apiDo(t, anon, "POST", "/api/v1/account-requests/confirm", map[string]string{"token": raw, "password": strings.Repeat("a", 72)}, http.StatusOK)
}

func TestInvitationAcceptRejectsMoreThan72Bytes(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "root", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "root", "password1234")
	apiDo(t, admin, "POST", "/api/v1/invitations", map[string]any{"email": "new@example.com"}, http.StatusCreated)
	token := tokenInEmail(t, latestEmail(t, app, "new@example.com"))
	anon := anonClient(t, app, "192.0.2.9")
	accept := func(pw string) (int, string) {
		w := anon.do("POST", "/api/v1/invitations/accept", map[string]any{"token": token, "name": "New Person", "password": pw, "ageBand": "adult"}, true)
		return w.Code, w.Body.String()
	}
	code, body := accept(strings.Repeat("a", 73))
	assertTooLong(t, code, body)
	if code, body = accept(strings.Repeat("a", 72)); code != http.StatusOK {
		t.Fatalf("72 bytes: %d %s", code, body)
	}
}

func TestSetupRejectsMoreThan72Bytes(t *testing.T) {
	app := testApp(t)
	post := func(pw string) *struct {
		code int
		body string
	} {
		out := postSetupForm(t, app, url.Values{
			"site_name": {"Grace Church"}, "timezone": {"America/Denver"},
			"name": {"Root"}, "email": {"root@example.com"}, "age_band": {"adult"},
			"password": {pw}, "password_confirm": {pw},
		})
		return &struct {
			code int
			body string
		}{out.Code, out.Body.String()}
	}
	long := strings.Repeat("a", 73)
	got := post(long)
	if got.code != http.StatusBadRequest || !strings.Contains(got.body, "Use at most 72 bytes") {
		t.Fatalf("73 bytes: %d %s", got.code, got.body)
	}
	if strings.Contains(got.body, long) {
		t.Fatal("setup page echoed the password")
	}
	if !strings.Contains(got.body, "at most 72 bytes") {
		t.Fatal("setup page does not state the limit next to the field")
	}
	if got = post(strings.Repeat("a", 72)); got.code == http.StatusBadRequest || got.code >= 500 {
		t.Fatalf("72 bytes: %d %s", got.code, got.body)
	}
}

func TestAdminPasswordPathsRejectMoreThan72Bytes(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	alice := newTestUser(t, app, "alice", "password1234", models.RoleMember)
	admin := loginAPI(t, app, "admin", "password1234")
	w := admin.do("POST", "/api/v1/admin/users/"+itoa2(alice.ID)+"/password", map[string]string{"password": strings.Repeat("a", 73)}, true)
	assertTooLong(t, w.Code, w.Body.String())
	apiDo(t, admin, "POST", "/api/v1/admin/users/"+itoa2(alice.ID)+"/password", map[string]string{"password": strings.Repeat("a", 72)}, http.StatusOK)
	w = admin.do("POST", "/api/v1/admin/users", map[string]any{"ageBand": "adult", "email": "bob@example.com", "name": "Bob", "password": strings.Repeat("a", 73)}, true)
	assertTooLong(t, w.Code, w.Body.String())
}
