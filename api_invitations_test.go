package main

import (
	"fmt"
	"fold/internal/audit"
	"fold/models"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// M1.2 acceptance: admin invites with no group, the invitee sets a password
// on acceptance and gets an active account, the token is single use, and the
// flow is audited.
func TestInvitationAcceptanceRechecksInviterAuthority(t *testing.T) {
	for _, state := range []string{"leader", "demoted", "removed", "disabled", "deleted"} {
		t.Run(state, func(t *testing.T) {
			app := testApp(t)
			leader := newTestUser(t, app, "leader", "pw", models.RoleMember)
			g := newTestGroup(t, app, leader, "Invitation authority", models.GroupActive)
			if err := app.DB.Model(g).Updates(map[string]any{"visibility": models.VisibilityInviteOnly, "member_limit": 1}).Error; err != nil {
				t.Fatal(err)
			}
			client := loginAPI(t, app, "leader", "pw")
			apiDo(t, client, "POST", "/api/v1/invitations", map[string]any{"email": "new@example.com", "name": "New Person", "groupId": g.ID, "overLimit": true}, http.StatusCreated)
			token := tokenInEmail(t, latestEmail(t, app, "new@example.com"))
			switch state {
			case "demoted":
				app.DB.Model(&models.Membership{}).Where("user_id = ? AND group_id = ?", leader.ID, g.ID).Update("role", models.MembershipMember)
			case "removed":
				app.DB.Model(&models.Membership{}).Where("user_id = ? AND group_id = ?", leader.ID, g.ID).Update("status", memberRemoved)
			case "disabled":
				app.DB.Model(leader).Update("status", models.UserDisabled)
			case "deleted":
				app.DB.Delete(leader)
			}
			anon := anonClient(t, app, "192.0.2.2")
			w := anon.do("POST", "/api/v1/invitations/accept", map[string]any{"token": token, "name": "New Person", "password": "password1234", "ageBand": "adult"}, true)
			want := http.StatusBadRequest
			if state == "leader" {
				want = http.StatusOK
			}
			if w.Code != want {
				t.Fatalf("accept: %d %s want %d", w.Code, w.Body.String(), want)
			}
			var n int64
			app.DB.Model(&models.User{}).Where("email = ?", "new@example.com").Count(&n)
			if (n == 1) != (state == "leader") {
				t.Fatalf("unauthorized account created, n=%d", n)
			}
			if state != "leader" && auditCount(t, app, audit.InvitationAccepted) != 0 {
				t.Fatal("unauthorized accept audited as success")
			}
		})
	}
}

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
		map[string]string{"ageBand": "adult", "token": token, "name": "New Person", "password": "password1234"}, true)
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
		map[string]string{"ageBand": "adult", "token": token, "name": "New Person", "password": "password1234"}, true)
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
		map[string]string{"ageBand": "adult", "token": token, "name": "L One", "password": "password1234"}, true)
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

func TestLeaderInviteRefusalsConsumeDailyLimit(t *testing.T) {
	app := testApp(t)
	leader := newTestUser(t, app, "lead", "password1234", models.RoleMember)
	newTestGroup(t, app, leader, "Crew", models.GroupActive)
	existing := newTestUser(t, app, "existing", "password1234", models.RoleMember)
	lc := loginAPI(t, app, "lead", "password1234")

	for i := 0; i < leaderInviteDailyLimit; i++ {
		w := lc.do("POST", "/api/v1/invitations", map[string]any{"email": existing.Email}, true)
		if w.Code != http.StatusConflict {
			t.Fatalf("existing-account refusal %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	for _, address := range []string{existing.Email, "new@example.com"} {
		w := lc.do("POST", "/api/v1/invitations", map[string]any{"email": address}, true)
		if w.Code != http.StatusTooManyRequests {
			t.Fatalf("limited invite for %s: %d %s, want 429", address, w.Code, w.Body.String())
		}
	}

	var refused, created, resent, invitations, mails int64
	app.DB.Model(&models.AuditLog{}).Where("action = ? AND actor_id = ?", audit.InvitationRefused, leader.ID).Count(&refused)
	app.DB.Model(&models.AuditLog{}).Where("action = ? AND actor_id = ?", audit.InvitationCreated, leader.ID).Count(&created)
	app.DB.Model(&models.AuditLog{}).Where("action = ? AND actor_id = ?", audit.InvitationResent, leader.ID).Count(&resent)
	app.DB.Model(&models.Invitation{}).Where("email IN ?", []string{existing.Email, "new@example.com"}).Count(&invitations)
	app.DB.Model(&models.MailOutbox{}).Where("to_address IN ?", []string{existing.Email, "new@example.com"}).Count(&mails)
	if refused != leaderInviteDailyLimit {
		t.Fatalf("durable refusal audits: got %d, want %d", refused, leaderInviteDailyLimit)
	}
	if created != 0 || resent != 0 || invitations != 0 || mails != 0 {
		t.Fatalf("refusals created side effects: created=%d resent=%d invitations=%d mails=%d", created, resent, invitations, mails)
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
			map[string]string{"ageBand": "adult", "token": token, "name": "Full", "password": "password1234"}, true)
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
