package main

import (
	"context"
	"fold/internal/audit"
	foldmail "fold/internal/mail"
	"fold/models"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAPILoginFlow(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "alice", "secret123", models.RoleMember)

	c := newAPIClient(t, app)
	c.fetchCSRF()

	// wrong password → 401
	w := c.do("POST", "/api/v1/auth/login",
		map[string]string{"email": "alice@example.com", "password": "nope"}, true)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: got %d", w.Code)
	}

	// correct → 200 with user payload
	w = c.do("POST", "/api/v1/auth/login",
		map[string]string{"email": "Alice@Example.COM", "password": "secret123"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("login: got %d %s", w.Code, w.Body.String())
	}
	var loginBody struct {
		User userJSON `json:"user"`
	}
	decodeJSON(t, w, &loginBody)
	if loginBody.User.Email != "alice@example.com" {
		t.Fatalf("unexpected user: %+v", loginBody.User)
	}

	// me works with the session cookie
	w = c.do("GET", "/api/v1/auth/me", nil, false)
	if w.Code != http.StatusOK {
		t.Fatalf("me: got %d", w.Code)
	}

	// anonymous me → 401 JSON (not a redirect)
	anon := newAPIClient(t, app)
	w = anon.do("GET", "/api/v1/auth/me", nil, false)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anon me: got %d", w.Code)
	}
}

func TestPasswordChangeEndsOtherSessions(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	newTestUser(t, app, "alice", "password1234", models.RoleMember)
	alice := loginAPI(t, app, "alice", "password1234")
	stale := &apiClient{t: t, router: alice.router, jar: map[string]*http.Cookie{}, token: alice.token}
	for k, v := range alice.jar {
		stale.jar[k] = v
	}

	admin := loginAPI(t, app, "admin", "password1234")
	var users struct {
		Users []userJSON `json:"users"`
	}
	w := admin.do("GET", "/api/v1/admin/users", nil, false)
	decodeJSON(t, w, &users)
	var aliceID uint
	for _, u := range users.Users {
		if u.Name == "alice" {
			aliceID = u.ID
		}
	}
	w = admin.do("POST", "/api/v1/admin/users/"+itoa(aliceID)+"/password",
		map[string]any{"password": "newpassword1"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("reset: %d %s", w.Code, w.Body.String())
	}
	if w := alice.do("GET", "/api/v1/auth/me", nil, false); w.Code != http.StatusUnauthorized {
		t.Fatalf("old session after reset: got %d", w.Code)
	}

	alice = loginAPI(t, app, "alice", "newpassword1")
	for k, v := range alice.jar {
		stale.jar[k] = v
	}
	stale.token = alice.token
	w = alice.do("POST", "/api/v1/me/password", map[string]any{
		"current": "newpassword1", "password": "newerpassword1",
	}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("change: %d %s", w.Code, w.Body.String())
	}
	if w := alice.do("GET", "/api/v1/auth/me", nil, false); w.Code != http.StatusOK {
		t.Fatalf("current session after change: got %d %s", w.Code, w.Body.String())
	}
	if w := stale.do("GET", "/api/v1/auth/me", nil, false); w.Code != http.StatusUnauthorized {
		t.Fatalf("copied session after change: got %d", w.Code)
	}
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
	want := "account_type avatar_focal_x avatar_focal_y avatar_key birth_date created_at deleted_at display_name email id " +
		"name password_hash phone role session_version status timezone updated_at"
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

// M1.2 acceptance: password reset answers uniformly for known and unknown
// addresses, the token is single use and expires, the raw token is never
// stored, and confirming signs the user in with the new password.
func TestPasswordResetUniformAndSingleUse(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "alice", "password1234", models.RoleMember)
	anon := newAPIClient(t, app)
	anon.fetchCSRF()

	reqBody := map[string]string{"email": "nobody@example.com"}
	w := anon.do("POST", "/api/v1/auth/password-reset", reqBody, true)
	if w.Code != http.StatusOK {
		t.Fatalf("unknown address: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Message string `json:"message"`
	}
	decodeJSON(t, w, &body)
	if body.Message != resetRequestMessage {
		t.Fatalf("unknown address message: %q", body.Message)
	}
	var n int64
	app.DB.Model(&models.AccountToken{}).Count(&n)
	if n != 0 {
		t.Fatalf("token created for unknown address")
	}

	// Known address: the on-screen answer is identical...
	w = anon.do("POST", "/api/v1/auth/password-reset", map[string]string{"email": "alice@example.com"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("known address: %d %s", w.Code, w.Body.String())
	}
	var body2 struct {
		Message string `json:"message"`
	}
	decodeJSON(t, w, &body2)
	if body2.Message != body.Message {
		t.Fatalf("answers differ: %q vs %q", body.Message, body2.Message)
	}

	// ...but a hashed token and an email exist behind it.
	app.DB.Model(&models.AccountToken{}).Where("kind = ?", models.TokenPasswordReset).Count(&n)
	if n != 1 {
		t.Fatalf("expected one reset token, got %d", n)
	}
	m := latestEmail(t, app, "alice@example.com")
	if !strings.Contains(m.Subject, "Reset") {
		t.Fatalf("subject: %q", m.Subject)
	}
	token := tokenInEmail(t, m)
	var tok models.AccountToken
	if err := app.DB.Where("kind = ?", models.TokenPasswordReset).First(&tok).Error; err != nil {
		t.Fatalf("token row: %v", err)
	}
	if tok.TokenHash == token || strings.Contains(tok.TokenHash, token) {
		t.Fatalf("raw token appears to be stored")
	}

	// Confirm: sets the password and signs in.
	w = anon.do("POST", "/api/v1/auth/password-reset/confirm",
		map[string]string{"token": token, "password": "newpassword1234"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("confirm: %d %s", w.Code, w.Body.String())
	}
	// Single use: the same token fails now.
	w = anon.do("POST", "/api/v1/auth/password-reset/confirm",
		map[string]string{"token": token, "password": "anotherpassword123"}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("reuse: %d %s", w.Code, w.Body.String())
	}
	// The new password works, the old one doesn't.
	loginAPI(t, app, "alice", "newpassword1234")
	bad := newAPIClient(t, app)
	bad.fetchCSRF()
	w = bad.do("POST", "/api/v1/auth/login",
		map[string]string{"email": "alice@example.com", "password": "password1234"}, true)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("old password still works: %d", w.Code)
	}
	if auditCount(t, app, audit.PasswordResetCompleted) != 1 {
		t.Fatalf("password.reset_completed not audited")
	}

	// An expired token fails.
	apiDo(t, anon, "POST", "/api/v1/auth/password-reset", map[string]string{"email": "alice@example.com"}, http.StatusOK)
	m2 := latestEmail(t, app, "alice@example.com")
	token2 := tokenInEmail(t, m2)
	app.DB.Model(&models.AccountToken{}).Where("kind = ?", models.TokenPasswordReset).
		Update("expires_at", time.Now().UTC().Add(-time.Hour))
	w = anon.do("POST", "/api/v1/auth/password-reset/confirm",
		map[string]string{"token": token2, "password": "thirdpassword1234"}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expired token: %d %s", w.Code, w.Body.String())
	}
}

// Logout expires the session cookie, and the cookie it hands back carries
// no user, so even a client that keeps it is signed out.
func TestLogoutEndsSession(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "sam", "password1234", models.RoleMember)
	sam := loginAs(t, app, "sam", "password1234")
	w := sam.do("POST", "/api/v1/auth/logout", nil, true)
	if w.Code != http.StatusOK {
		t.Fatalf("logout: %d", w.Code)
	}
	expired := false
	for _, ck := range w.Result().Cookies() {
		if ck.Name == sessionName && ck.MaxAge < 0 {
			expired = true
		}
	}
	if !expired {
		t.Fatalf("session cookie not expired: %v", w.Header().Values("Set-Cookie"))
	}
	if c := sam.do("GET", "/api/v1/auth/me", nil, false).Code; c != http.StatusUnauthorized {
		t.Fatalf("me after logout: %d", c)
	}
}

// DEC-53: guests have no password reset. A guest's address gets the
// same answer as an unknown one, and no token or email.
func TestPasswordResetSkipsGuests(t *testing.T) {
	app := testApp(t)
	guest := models.User{Email: "guest@example.com", Name: "Gail",
		Role: models.RoleMember, Status: models.UserGuest, AccountType: models.AccountAdult}
	if err := app.DB.Create(&guest).Error; err != nil {
		t.Fatal(err)
	}
	anon := newAPIClient(t, app)
	anon.fetchCSRF()
	for _, email := range []string{"nobody@example.com", "guest@example.com"} {
		w := anon.do("POST", "/api/v1/auth/password-reset", map[string]string{"email": email}, true)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), resetRequestMessage) {
			t.Fatalf("%s: %d %s", email, w.Code, w.Body.String())
		}
	}
	var tokens, mails int64
	app.DB.Model(&models.AccountToken{}).Count(&tokens)
	app.DB.Model(&models.MailOutbox{}).Where("to_address = ?", "guest@example.com").Count(&mails)
	if tokens != 0 || mails != 0 {
		t.Fatalf("guest got a reset: tokens %d, emails %d", tokens, mails)
	}
}

// #90: a burst of parallel wrong passwords can't get past the lockout. The
// attempt is counted before bcrypt, so at most the limit are checked.
func TestLoginLockoutHoldsUnderParallelRequests(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "alice", "password1234", models.RoleMember)

	const burst = 3 * emailFailureLimit
	codes := make(chan int, burst)
	var wg sync.WaitGroup
	for i := 0; i < burst; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			code, _ := tryLogin(t, app, "198.51.100."+itoa2(uint(i+1)), "alice@example.com", "wrong")
			codes <- code
		}(i)
	}
	wg.Wait()
	close(codes)
	checked := 0
	for code := range codes {
		switch code {
		case http.StatusUnauthorized:
			checked++
		case http.StatusTooManyRequests:
		default:
			t.Fatalf("unexpected status %d", code)
		}
	}
	if checked > emailFailureLimit {
		t.Fatalf("%d passwords checked in a burst of %d, limit %d", checked, burst, emailFailureLimit)
	}
	if code, _ := tryLogin(t, app, "203.0.113.9", "alice@example.com", "password1234"); code != http.StatusTooManyRequests {
		t.Fatalf("after burst: got %d, want 429", code)
	}
}

// A successful sign-in doesn't use up the IP's failure budget.
func TestLoginSuccessDoesNotCountAgainstIP(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "alice", "password1234", models.RoleMember)
	for i := 0; i < ipFailureLimit+5; i++ {
		if code, _ := tryLogin(t, app, "192.0.2.10", "alice@example.com", "password1234"); code != http.StatusOK {
			t.Fatalf("sign-in %d: got %d", i+1, code)
		}
	}
}

// slowMailer stands in for a slow SMTP server.
type slowMailer struct {
	delay time.Duration
	sent  chan string
}

func (m *slowMailer) Send(_ context.Context, msg foldmail.Message) error {
	time.Sleep(m.delay)
	m.sent <- msg.To
	return nil
}

// #93: a known address doesn't wait on the email send, so response time
// doesn't reveal which addresses have accounts.
func TestPasswordResetDoesNotWaitOnEmail(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "alice", "password1234", models.RoleMember)
	mailer := &slowMailer{delay: 2 * time.Second, sent: make(chan string, 1)}
	app.Mailer = mailer
	app.background = nil // the real, asynchronous path

	anon := newAPIClient(t, app)
	anon.fetchCSRF()
	start := time.Now()
	w := anon.do("POST", "/api/v1/auth/password-reset", map[string]string{"email": "alice@example.com"}, true)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), resetRequestMessage) {
		t.Fatalf("reset: %d %s", w.Code, w.Body.String())
	}
	if d := time.Since(start); d >= mailer.delay {
		t.Fatalf("response waited on the email: %v", d)
	}
	select {
	case to := <-mailer.sent:
		if to != "alice@example.com" {
			t.Fatalf("sent to %q", to)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("reset email never sent")
	}
}

// A panic in background work is recovered, not fatal to the server.
func TestRunInBackgroundRecoversPanics(t *testing.T) {
	app := &App{}
	done := make(chan struct{})
	app.runInBackground(func() {
		defer close(done)
		panic("boom")
	})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("background task never ran")
	}
}

// deadlineMailer records whether the send carried a deadline.
type deadlineMailer struct{ deadline chan time.Duration }

func (m *deadlineMailer) Send(ctx context.Context, _ foldmail.Message) error {
	d, ok := ctx.Deadline()
	if !ok {
		m.deadline <- 0
		return nil
	}
	m.deadline <- time.Until(d)
	return nil
}

// The background reset send is bounded by resetMailTimeout.
func TestPasswordResetSendHasTimeout(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "alice", "password1234", models.RoleMember)
	mailer := &deadlineMailer{deadline: make(chan time.Duration, 1)}
	app.Mailer = mailer
	anon := newAPIClient(t, app)
	anon.fetchCSRF()
	apiDo(t, anon, "POST", "/api/v1/auth/password-reset", map[string]string{"email": "alice@example.com"}, http.StatusOK)
	if d := <-mailer.deadline; d <= 0 || d > resetMailTimeout {
		t.Fatalf("send deadline %v, want within %v", d, resetMailTimeout)
	}
}
