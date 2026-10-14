package main

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"fold/models"
)

// M0.3a: log in by email (C16, DEC-23), throttling in Postgres, no account
// enumeration, and sessions ended on password, email, role or status changes.

func tryLogin(t *testing.T, app *App, ip, email, password string) (int, string) {
	t.Helper()
	c := newAPIClient(t, app)
	c.ip = ip
	c.fetchCSRF()
	w := c.do("POST", "/api/v1/auth/login", map[string]string{"email": email, "password": password}, true)
	return w.Code, w.Body.String()
}

func TestEmailLoginAnyCaseAndOnlyWithPassword(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	newTestUser(t, app, "alice", "password1234", models.RoleMember)
	admin := loginAs(t, app, "admin", "password1234")

	for _, e := range []string{"alice@example.com", "ALICE@Example.Com", "  Alice@example.com "} {
		if code, body := tryLogin(t, app, "", e, "password1234"); code != http.StatusOK {
			t.Fatalf("login as %q: %d %s", e, code, body)
		}
	}

	// An account created without a password is invited and can't sign in.
	w := admin.do("POST", "/api/v1/admin/users", map[string]string{"email": "bob@example.com", "name": "Bob"}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create invited user: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		User userJSON `json:"user"`
	}
	decodeJSON(t, w, &created)
	if created.User.Status != models.UserInvited || created.User.HasPassword {
		t.Fatalf("want invited with no password, got %+v", created.User)
	}
	for _, pw := range []string{"", "password1234", "not a real password"} {
		if code, _ := tryLogin(t, app, "", "bob@example.com", pw); code == http.StatusOK {
			t.Fatalf("invited user signed in with %q", pw)
		}
	}
	// Setting a password activates the account.
	if w := admin.do("POST", "/api/v1/admin/users/"+itoa2(created.User.ID)+"/password",
		map[string]string{"password": "bobs-password"}, true); w.Code != http.StatusOK {
		t.Fatalf("set password: %d %s", w.Code, w.Body.String())
	}
	if code, body := tryLogin(t, app, "", "BOB@example.com", "bobs-password"); code != http.StatusOK {
		t.Fatalf("login after password set: %d %s", code, body)
	}

	// A second account with the same email in another case is refused,
	// by the API and by the database.
	w = admin.do("POST", "/api/v1/admin/users", map[string]string{"email": "Alice@EXAMPLE.com", "name": "Alice 2"}, true)
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate email: got %d, want 409", w.Code)
	}
	if err := app.DB.Create(&models.User{Email: "ALICE@example.com", Name: "x"}).Error; err == nil {
		t.Fatal("database accepted a duplicate email")
	}
	w = admin.do("PUT", "/api/v1/admin/users/"+itoa2(created.User.ID), map[string]string{"email": "alice@EXAMPLE.com"}, true)
	if w.Code != http.StatusConflict {
		t.Fatalf("change to taken email: got %d, want 409", w.Code)
	}

	// A deleted account frees its email.
	if w := admin.do("DELETE", "/api/v1/admin/users/"+itoa2(created.User.ID), nil, true); w.Code != http.StatusOK {
		t.Fatalf("delete: %d", w.Code)
	}
	if w := admin.do("POST", "/api/v1/admin/users", map[string]string{"email": "bob@example.com", "name": "Bob"}, true); w.Code != http.StatusCreated {
		t.Fatalf("reuse deleted email: %d %s", w.Code, w.Body.String())
	}
}

func TestUsersTableIsEmailFirst(t *testing.T) {
	app := testApp(t)
	var cols []string
	app.DB.Raw(`SELECT column_name FROM information_schema.columns
		WHERE table_name = 'users' ORDER BY column_name`).Scan(&cols)
	want := "avatar_focal_x avatar_focal_y avatar_key created_at deleted_at display_name email id " +
		"name password_hash phone role session_version status updated_at"
	if got := strings.Join(cols, " "); got != want {
		t.Fatalf("users columns:\n got %s\nwant %s", got, want)
	}
	var typ string
	app.DB.Raw(`SELECT data_type || ':' || udt_name FROM information_schema.columns
		WHERE table_name = 'users' AND column_name = 'email' AND is_nullable = 'NO'`).Scan(&typ)
	if typ != "USER-DEFINED:citext" {
		t.Fatalf("users.email: got %q, want NOT NULL citext", typ)
	}
}

func TestLoginThrottlePerEmailAndIP(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "alice", "password1234", models.RoleMember)

	// Ten failures for one email, from different IPs, lock that email
	// everywhere, even with the right password.
	for i := 0; i < emailFailureLimit; i++ {
		ip := "198.51.100." + itoa2(uint(i+1))
		if code, _ := tryLogin(t, app, ip, "Alice@example.com", "wrong"); code != http.StatusUnauthorized {
			t.Fatalf("failure %d: got %d, want 401", i+1, code)
		}
	}
	if code, _ := tryLogin(t, app, "203.0.113.9", "alice@example.com", "password1234"); code != http.StatusTooManyRequests {
		t.Fatalf("locked email: got %d, want 429", code)
	}
	// Other emails from those IPs are unaffected.
	newTestUser(t, app, "bob", "password1234", models.RoleMember)
	if code, _ := tryLogin(t, app, "198.51.100.1", "bob@example.com", "password1234"); code != http.StatusOK {
		t.Fatalf("other email: got %d", code)
	}
	// The lock lasts 15 minutes.
	var row models.LoginThrottle
	app.DB.Where("key = ?", "email:alice@example.com").First(&row)
	if row.LockedUntil == nil || time.Until(*row.LockedUntil) < 14*time.Minute || time.Until(*row.LockedUntil) > 16*time.Minute {
		t.Fatalf("lock: %+v", row)
	}
	// It is stored in Postgres, so a new App (a restart) still sees it.
	restarted := &App{DB: app.DB, Store: app.Store, Config: app.Config, Uploads: app.Uploads, Mailer: app.Mailer}
	if code, _ := tryLogin(t, restarted, "203.0.113.9", "alice@example.com", "password1234"); code != http.StatusTooManyRequests {
		t.Fatalf("after restart: got %d, want 429", code)
	}
	// Once it expires the right password works again.
	app.DB.Model(&models.LoginThrottle{}).Where("key = ?", row.Key).Update("locked_until", time.Now().Add(-time.Second))
	if code, _ := tryLogin(t, app, "203.0.113.9", "alice@example.com", "password1234"); code != http.StatusOK {
		t.Fatalf("after expiry: got %d", code)
	}

	// Fifty failures from one IP, spread over many emails, lock that IP.
	ip := "192.0.2.77"
	for i := 0; i < ipFailureLimit; i++ {
		email := "nobody" + itoa2(uint(i%10)) + "@example.com"
		if code, _ := tryLogin(t, app, ip, email, "wrong"); code != http.StatusUnauthorized {
			t.Fatalf("ip failure %d: got %d, want 401", i+1, code)
		}
		// Keep the per-email counters below their limit.
		app.DB.Where("key LIKE 'email:nobody%'").Delete(&models.LoginThrottle{})
	}
	if code, _ := tryLogin(t, app, ip, "bob@example.com", "password1234"); code != http.StatusTooManyRequests {
		t.Fatalf("locked ip: got %d, want 429", code)
	}
	if code, _ := tryLogin(t, app, "192.0.2.78", "bob@example.com", "password1234"); code != http.StatusOK {
		t.Fatalf("other ip: got %d", code)
	}
}

func TestLoginDoesNotRevealAccounts(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "alice", "password1234", models.RoleMember)

	timeIt := func(email string) (int, string, time.Duration) {
		start := time.Now()
		code, body := tryLogin(t, app, "", email, "wrong-password")
		return code, body, time.Since(start)
	}
	var known, unknown time.Duration
	for i := 0; i < 3; i++ {
		c1, b1, d1 := timeIt("alice@example.com")
		c2, b2, d2 := timeIt("nobody@example.com")
		if c1 != c2 || b1 != b2 {
			t.Fatalf("responses differ: %d %s vs %d %s", c1, b1, c2, b2)
		}
		known += d1
		unknown += d2
	}
	// Both paths run one bcrypt comparison. Allow generous slack for CI.
	if unknown < known/3 || known < unknown/3 {
		t.Fatalf("timing differs too much: known %v, unknown %v", known, unknown)
	}
}

func TestChangesEndOtherSessions(t *testing.T) {
	cases := []struct {
		name string
		do   func(admin *apiClient, id uint) int
	}{
		{"password", func(a *apiClient, id uint) int {
			return a.do("POST", "/api/v1/admin/users/"+itoa2(id)+"/password", map[string]string{"password": "another-password"}, true).Code
		}},
		{"role", func(a *apiClient, id uint) int {
			return a.do("PUT", "/api/v1/admin/users/"+itoa2(id), map[string]string{"role": "admin"}, true).Code
		}},
		{"disable", func(a *apiClient, id uint) int {
			return a.do("PUT", "/api/v1/admin/users/"+itoa2(id), map[string]string{"status": "disabled"}, true).Code
		}},
		{"email", func(a *apiClient, id uint) int {
			return a.do("PUT", "/api/v1/admin/users/"+itoa2(id), map[string]string{"email": "new-alice@example.com"}, true).Code
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := testApp(t)
			newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
			alice := newTestUser(t, app, "alice", "password1234", models.RoleMember)
			admin := loginAs(t, app, "admin", "password1234")
			session := loginAs(t, app, "alice", "password1234")
			if w := session.do("GET", "/api/v1/auth/me", nil, false); w.Code != http.StatusOK {
				t.Fatalf("before: %d", w.Code)
			}
			if code := tc.do(admin, alice.ID); code != http.StatusOK {
				t.Fatalf("change: %d", code)
			}
			if w := session.do("GET", "/api/v1/auth/me", nil, false); w.Code != http.StatusUnauthorized {
				t.Fatalf("after %s change: got %d, want 401", tc.name, w.Code)
			}
		})
	}

	// A disabled account can't sign in, and an admin can't disable themselves
	// or the last admin.
	app := testApp(t)
	me := newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	alice := newTestUser(t, app, "alice", "password1234", models.RoleMember)
	admin := loginAs(t, app, "admin", "password1234")
	admin.do("PUT", "/api/v1/admin/users/"+itoa2(alice.ID), map[string]string{"status": "disabled"}, true)
	if code, _ := tryLogin(t, app, "", "alice@example.com", "password1234"); code != http.StatusUnauthorized {
		t.Fatalf("disabled login: got %d, want 401", code)
	}
	w := admin.do("PUT", "/api/v1/admin/users/"+itoa2(me.ID), map[string]string{"status": "disabled"}, true)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "yourself") && !strings.Contains(w.Body.String(), "own account") {
		t.Fatalf("disable self: %d %s", w.Code, w.Body.String())
	}
	// Re-enabling works.
	if w := admin.do("PUT", "/api/v1/admin/users/"+itoa2(alice.ID), map[string]string{"status": "active"}, true); w.Code != http.StatusOK {
		t.Fatalf("re-enable: %d", w.Code)
	}
	if code, _ := tryLogin(t, app, "", "alice@example.com", "password1234"); code != http.StatusOK {
		t.Fatalf("re-enabled login: got %d", code)
	}
}
