package main

// Tests for M1.2: email basics, password reset, invitations, verified email
// change, and notification emails. Everything reads its links from the
// devoutbox; nothing here touches a live SMTP server.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"fold/internal/audit"
	"fold/internal/mail"
	"fold/internal/notify"
	"fold/models"
)

func devOutbox(t *testing.T, app *App) *mail.DevOutbox {
	t.Helper()
	ob, ok := app.Mailer.(*mail.DevOutbox)
	if !ok {
		t.Fatal("test app has no devoutbox mailer")
	}
	return ob
}

func latestEmail(t *testing.T, app *App, to string) mail.OutboxMessage {
	t.Helper()
	m, err := devOutbox(t, app).LatestTo(to)
	if err != nil {
		t.Fatalf("no email to %s: %v", to, err)
	}
	return m
}

var tokenRe = regexp.MustCompile(`token=([A-Za-z0-9_-]+)`)

func tokenInEmail(t *testing.T, m mail.OutboxMessage) string {
	t.Helper()
	mt := tokenRe.FindStringSubmatch(m.Body)
	if len(mt) != 2 {
		t.Fatalf("no token in email to %s: %q", m.To, m.Body)
	}
	return mt[1]
}

func auditCount(t *testing.T, app *App, action string) int64 {
	t.Helper()
	var n int64
	if err := app.DB.Model(&models.AuditLog{}).Where("action = ?", action).Count(&n).Error; err != nil {
		t.Fatalf("audit count: %v", err)
	}
	return n
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
	w = anon.do("POST", "/api/v1/auth/password-reset", map[string]string{"email": "alice@example.com"}, true)
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

// M1.2 acceptance: admin invites with no group, the invitee sets a password
// on acceptance and gets an active account, the token is single use, and the
// flow is audited.
func TestInvitationAcceptance(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "root", "password1234", models.RoleAdmin)
	ac := loginAPI(t, app, "root", "password1234")

	w := ac.do("POST", "/api/v1/invitations",
		map[string]any{"email": "new@example.com", "name": "New Person"}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Invitation invitationJSON `json:"invitation"`
	}
	decodeJSON(t, w, &created)
	if created.Invitation.Email != "new@example.com" {
		t.Fatalf("invitation email: %q", created.Invitation.Email)
	}

	m := latestEmail(t, app, "new@example.com")
	if !strings.Contains(m.Subject, "invited") {
		t.Fatalf("subject: %q", m.Subject)
	}
	token := tokenInEmail(t, m)

	// Public accept-page details.
	anon := newAPIClient(t, app)
	anon.fetchCSRF()
	w = anon.do("GET", "/api/v1/invitations/"+token, nil, false)
	if w.Code != http.StatusOK {
		t.Fatalf("invite details: %d %s", w.Code, w.Body.String())
	}
	var details struct {
		Invitation invitationJSON `json:"invitation"`
		Site       string         `json:"site"`
	}
	decodeJSON(t, w, &details)
	if details.Invitation.Email != "new@example.com" {
		t.Fatalf("details email: %q", details.Invitation.Email)
	}

	// Accept: an active account, signed in.
	w = anon.do("POST", "/api/v1/invitations/accept",
		map[string]string{"token": token, "name": "New Person", "password": "password1234"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", w.Code, w.Body.String())
	}
	var u models.User
	if err := app.DB.Where("email = ?", "new@example.com").First(&u).Error; err != nil {
		t.Fatalf("user not created: %v", err)
	}
	if u.Status != models.UserActive || u.PasswordHash == nil {
		t.Fatalf("user not active with password: status=%q", u.Status)
	}
	if u.Name != "New Person" {
		t.Fatalf("name: %q", u.Name)
	}

	// Single use: the same token fails now.
	w = anon.do("POST", "/api/v1/invitations/accept",
		map[string]string{"token": token, "name": "New Person", "password": "password1234"}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("reuse: %d %s", w.Code, w.Body.String())
	}
	if auditCount(t, app, audit.InvitationCreated) != 1 ||
		auditCount(t, app, audit.InvitationAccepted) != 1 {
		t.Fatalf("invitation not audited")
	}

	// The password works for login.
	loginAPI(t, app, "new", "password1234")
}

// M1.2 acceptance: admins invite into any group or none; leaders invite only
// into groups they lead or none; anyone else gets 403, guests 401. Accepting
// a group invitation creates the membership.
func TestInvitationPolicy(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "root", "password1234", models.RoleAdmin)
	leader := newTestUser(t, app, "lead", "password1234", models.RoleMember)
	other := newTestUser(t, app, "other", "password1234", models.RoleMember)
	newTestUser(t, app, "mem", "password1234", models.RoleMember)
	g := newTestGroup(t, app, leader, "Alpha", models.GroupActive)
	g2 := newTestGroup(t, app, other, "Beta", models.GroupActive)

	ac := loginAPI(t, app, "root", "password1234")
	lc := loginAPI(t, app, "lead", "password1234")
	mc := loginAPI(t, app, "mem", "password1234")
	anon := newAPIClient(t, app)
	anon.fetchCSRF()

	invite := func(c *apiClient, email string, gid *uint) int {
		body := map[string]any{"email": email}
		if gid != nil {
			body["groupId"] = *gid
		}
		return c.do("POST", "/api/v1/invitations", body, true).Code
	}
	// Admin: anywhere, including no group.
	if code := invite(ac, "a1@example.com", &g.ID); code != http.StatusCreated {
		t.Errorf("admin into group: %d", code)
	}
	if code := invite(ac, "a2@example.com", nil); code != http.StatusCreated {
		t.Errorf("admin no group: %d", code)
	}
	// Leader: own group or none.
	if code := invite(lc, "l1@example.com", &g.ID); code != http.StatusCreated {
		t.Errorf("leader into own group: %d", code)
	}
	if code := invite(lc, "l2@example.com", nil); code != http.StatusCreated {
		t.Errorf("leader no group: %d", code)
	}
	// Leader: someone else's group -> 403.
	if code := invite(lc, "l3@example.com", &g2.ID); code != http.StatusForbidden {
		t.Errorf("leader into another's group: %d, want 403", code)
	}
	// Member: 403 everywhere.
	if code := invite(mc, "m1@example.com", nil); code != http.StatusForbidden {
		t.Errorf("member no group: %d, want 403", code)
	}
	if code := invite(mc, "m2@example.com", &g.ID); code != http.StatusForbidden {
		t.Errorf("member into group: %d, want 403", code)
	}
	// Guest: 401.
	if code := invite(anon, "g1@example.com", nil); code != http.StatusUnauthorized {
		t.Errorf("guest: %d, want 401", code)
	}
	// A pending invite or an existing account can't be invited again.
	if code := invite(ac, "l1@example.com", nil); code != http.StatusConflict {
		t.Errorf("dup invite: %d, want 409", code)
	}

	// Accepting the leader's group invitation creates the membership.
	token := tokenInEmail(t, latestEmail(t, app, "l1@example.com"))
	w := anon.do("POST", "/api/v1/invitations/accept",
		map[string]string{"token": token, "name": "L One", "password": "password1234"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", w.Code, w.Body.String())
	}
	var u models.User
	app.DB.Where("email = ?", "l1@example.com").First(&u)
	var m models.Membership
	if err := app.DB.Where("user_id = ? AND group_id = ?", u.ID, g.ID).First(&m).Error; err != nil {
		t.Fatalf("no membership after accept: %v", err)
	}
	if m.Status != "active" || m.Role != models.MembershipMember {
		t.Fatalf("membership: status=%q role=%q", m.Status, m.Role)
	}
}

// M1.2 acceptance: a leader gets 20 invitations per day; the 21st is 429
// and audited.
func TestLeaderInviteDailyLimit(t *testing.T) {
	app := testApp(t)
	leader := newTestUser(t, app, "lead", "password1234", models.RoleMember)
	newTestGroup(t, app, leader, "Crew", models.GroupActive)
	lc := loginAPI(t, app, "lead", "password1234")
	for i := 0; i < 20; i++ {
		w := lc.do("POST", "/api/v1/invitations",
			map[string]any{"email": fmt.Sprintf("i%d@example.com", i)}, true)
		if w.Code != http.StatusCreated {
			t.Fatalf("invite %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	w := lc.do("POST", "/api/v1/invitations", map[string]any{"email": "i20@example.com"}, true)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("21st invite: %d %s, want 429", w.Code, w.Body.String())
	}
	var n int64
	app.DB.Model(&models.AuditLog{}).
		Where("action = ? AND actor_id = ?", audit.InvitationRateLimited, leader.ID).
		Count(&n)
	if n != 1 {
		t.Fatalf("rate limit not audited: %d", n)
	}
	// The admin's own limit is not affected by a different inviter's count.
	newTestUser(t, app, "root", "password1234", models.RoleAdmin)
	ac := loginAPI(t, app, "root", "password1234")
	w = ac.do("POST", "/api/v1/invitations", map[string]any{"email": "a@example.com"}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("admin invite after leader limit: %d", w.Code)
	}
}

// M1.2 acceptance (DEC-39): accepting an invitation into a full group joins
// over member_limit only when the inviter explicitly chose to go over it,
// and the over-limit join is audited.
func TestInvitationIntoFullGroup(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "root", "password1234", models.RoleAdmin)
	leader := newTestUser(t, app, "lead", "password1234", models.RoleMember)
	g := newTestGroup(t, app, leader, "Tiny", models.GroupActive)
	app.DB.Model(g).Update("member_limit", 1) // the leader fills it
	lc := loginAPI(t, app, "lead", "password1234")
	anon := newAPIClient(t, app)
	anon.fetchCSRF()

	accept := func(email, token string) *httptest.ResponseRecorder {
		return anon.do("POST", "/api/v1/invitations/accept",
			map[string]string{"token": token, "name": "Full", "password": "password1234"}, true)
	}

	// Without the over-limit choice: 409, no account, token stays live.
	w := lc.do("POST", "/api/v1/invitations",
		map[string]any{"email": "full1@example.com", "groupId": g.ID}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("invite: %d %s", w.Code, w.Body.String())
	}
	token := tokenInEmail(t, latestEmail(t, app, "full1@example.com"))
	w = accept("full1@example.com", token)
	if w.Code != http.StatusConflict {
		t.Fatalf("accept into full group: %d %s, want 409", w.Code, w.Body.String())
	}
	var fullBody struct {
		Full bool `json:"full"`
	}
	decodeJSON(t, w, &fullBody)
	if !fullBody.Full {
		t.Fatalf("no full:true in 409 body: %s", w.Body.String())
	}
	var n int64
	app.DB.Model(&models.User{}).Where("email = ?", "full1@example.com").Count(&n)
	if n != 0 {
		t.Fatalf("account created despite full group")
	}
	// The token wasn't consumed: the invitation is still pending.
	var inv models.Invitation
	app.DB.Where("email = ?", "full1@example.com").First(&inv)
	if inv.AcceptedAt != nil {
		t.Fatalf("token consumed by the failed accept")
	}

	// With the explicit over-limit choice: joins, and it's audited.
	w = lc.do("POST", "/api/v1/invitations",
		map[string]any{"email": "full2@example.com", "groupId": g.ID, "overLimit": true}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("invite over limit: %d %s", w.Code, w.Body.String())
	}
	token2 := tokenInEmail(t, latestEmail(t, app, "full2@example.com"))
	w = accept("full2@example.com", token2)
	if w.Code != http.StatusOK {
		t.Fatalf("accept over limit: %d %s", w.Code, w.Body.String())
	}
	var u models.User
	app.DB.Where("email = ?", "full2@example.com").First(&u)
	var m models.Membership
	if err := app.DB.Where("user_id = ? AND group_id = ?", u.ID, g.ID).First(&m).Error; err != nil {
		t.Fatalf("no membership after over-limit accept: %v", err)
	}
	if auditCount(t, app, audit.AddedOverLimit) != 1 {
		t.Fatalf("over-limit join not audited")
	}
}

// M1.2 acceptance: changing the sign-in email sends a verification link to
// the new address and a notice to the old one; the change applies only after
// verification and ends every other session.
func TestVerifiedEmailChange(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "alice", "password1234", models.RoleMember)
	ac := loginAPI(t, app, "alice", "password1234")
	other := loginAPI(t, app, "alice", "password1234") // a second session

	w := ac.do("PUT", "/api/v1/me", map[string]string{"email": "alice.new@example.com"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("request change: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		VerificationSent bool `json:"verificationSent"`
	}
	decodeJSON(t, w, &body)
	if !body.VerificationSent {
		t.Fatalf("no verificationSent: %s", w.Body.String())
	}
	// The address hasn't changed yet.
	var u models.User
	if err := app.DB.Where("email = ?", "alice@example.com").First(&u).Error; err != nil {
		t.Fatalf("email changed before verification: %v", err)
	}

	// Verification link to the new address, notice to the old one.
	verifyMail := latestEmail(t, app, "alice.new@example.com")
	if !strings.Contains(verifyMail.Subject, "Verify") {
		t.Fatalf("verify subject: %q", verifyMail.Subject)
	}
	noticeMail := latestEmail(t, app, "alice@example.com")
	if !strings.Contains(noticeMail.Subject, "changing") {
		t.Fatalf("notice subject: %q", noticeMail.Subject)
	}
	token := tokenInEmail(t, verifyMail)

	// Verify: the change applies.
	w = ac.do("POST", "/api/v1/me/email/verify", map[string]string{"token": token}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("verify: %d %s", w.Code, w.Body.String())
	}
	if err := app.DB.Where("email = ?", "alice.new@example.com").First(&u).Error; err != nil {
		t.Fatalf("email not changed: %v", err)
	}
	if auditCount(t, app, audit.EmailChangeRequested) != 1 ||
		auditCount(t, app, audit.EmailChanged) != 1 {
		t.Fatalf("email change not audited")
	}

	// Every other session ended...
	w = other.do("GET", "/api/v1/auth/me", nil, false)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("other session alive: %d", w.Code)
	}
	// ...but this one still works.
	w = ac.do("GET", "/api/v1/auth/me", nil, false)
	if w.Code != http.StatusOK {
		t.Fatalf("own session ended: %d", w.Code)
	}
	// Single use.
	w = ac.do("POST", "/api/v1/me/email/verify", map[string]string{"token": token}, true)
	if w.Code == http.StatusOK {
		t.Fatalf("verification token reused")
	}

	// Login works with the new address.
	loginAPI(t, app, "alice.new", "password1234")
}

// M1.2 acceptance: a visibility-override notice reaches the other leaders
// and the admins in the bell and by email, with one notification_deliveries
// row each.
func TestVisibilityOverrideEmailsAdminsAndLeaders(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "root", "password1234", models.RoleAdmin)
	lead1 := newTestUser(t, app, "lead1", "password1234", models.RoleMember)
	lead2 := newTestUser(t, app, "lead2", "password1234", models.RoleMember)
	lc1 := loginAPI(t, app, "lead1", "password1234")

	g := newTestGroup(t, app, lead1, "Youth", models.GroupActive)
	app.DB.Model(g).Update("visibility", models.VisibilityPrivate)
	addMemberEmail(t, lc1, g.ID, "lead2@example.com")
	apiDo(t, lc1, "PUT", "/api/v1/groups/"+itoa(g.ID)+"/members/"+itoa(lead2.ID)+"/role",
		map[string]string{"role": "leader"}, http.StatusOK)

	ev := scheduleGroupEvent(t, lc1, g.ID, "Lock-in")
	setVisibility(t, lc1, ev.ID, models.VisibilityPublic, "invite the neighbors", http.StatusOK)

	for _, who := range []struct{ name, email string }{
		{"root", "root@example.com"},
		{"lead2", "lead2@example.com"},
	} {
		c := loginAPI(t, app, who.name, "password1234")
		found := false
		for _, n := range feed(t, c, "") {
			if n.Kind == "event.visibility_override" {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: no bell item", who.name)
		}
		m := latestEmail(t, app, who.email)
		if !strings.Contains(m.Subject, "Event opened up") {
			t.Fatalf("%s: subject %q", who.name, m.Subject)
		}
		if !strings.Contains(m.Body, "/groups/") {
			t.Fatalf("%s: no group link in email", who.name)
		}
	}

	var n int64
	app.DB.Model(&models.NotificationDelivery{}).
		Joins("JOIN notifications ON notifications.id = notification_deliveries.notification_id").
		Where("notifications.kind = ? AND notification_deliveries.channel = ? AND notification_deliveries.status = ?",
			notify.VisibilityOverride, models.DeliveryEmail, models.DeliverySent).
		Count(&n)
	// The actor (lead1) is skipped: root and lead2 only.
	if n != 2 {
		t.Fatalf("email delivery rows: %d, want 2", n)
	}
}

// M1.2 acceptance: a pending group emails the admins a task, recorded in
// notification_deliveries.
func TestPendingGroupEmailsAdmins(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "root", "password1234", models.RoleAdmin)
	newTestUser(t, app, "sam", "password1234", models.RoleMember)
	sam := loginAPI(t, app, "sam", "password1234")

	_ = createGroupAs(t, sam, "Breakfast", models.VisibilityPublic) // pending
	m := latestEmail(t, app, "root@example.com")
	if !strings.Contains(m.Subject, "started a group") {
		t.Fatalf("subject: %q", m.Subject)
	}
	var n int64
	app.DB.Model(&models.NotificationDelivery{}).
		Where("channel = ? AND status = ?", models.DeliveryEmail, models.DeliverySent).
		Count(&n)
	if n != 1 {
		t.Fatalf("email delivery rows: %d, want 1", n)
	}
}

// M1.2: /dev/outbox is unreachable outside dev.
func TestDevOutboxUnreachableOutsideDev(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "alice", "password1234", models.RoleMember)

	r := app.setupRouter()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/dev/outbox", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("dev: %d", w.Code)
	}

	prod := testApp(t)
	newTestUser(t, prod, "alice", "password1234", models.RoleMember)
	prod.Config.Env = "prod"
	rp := prod.setupRouter()
	w = httptest.NewRecorder()
	rp.ServeHTTP(w, httptest.NewRequest("GET", "/dev/outbox", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("prod: %d, want 404", w.Code)
	}
}
