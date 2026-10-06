package main

import (
	"net/http"
	"testing"
	"time"

	"church-groups/models"
)

func loginAs(t *testing.T, app *App, username, password string) *apiClient {
	t.Helper()
	return loginAPI(t, app, username, password)
}

func TestAdminUserCRUD(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", "admin")
	newTestUser(t, app, "grier", "password1234", "member")
	admin := loginAs(t, app, "admin", "password1234")

	// Non-admin is forbidden.
	member := loginAs(t, app, "grier", "password1234")
	w := member.do("GET", "/api/v1/admin/users", nil, false)
	if w.Code != http.StatusForbidden {
		t.Fatalf("member list users: got %d, want 403", w.Code)
	}

	// Create.
	w = admin.do("POST", "/api/v1/admin/users", map[string]string{
		"username": "newbie", "password": "password1234", "role": "member",
		"displayName": "Newbie",
	}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create user: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		User userJSON `json:"user"`
	}
	decodeJSON(t, w, &created)
	if created.User.Username != "newbie" || created.User.DisplayName != "Newbie" {
		t.Fatalf("unexpected created user: %+v", created.User)
	}

	// Duplicate username conflicts.
	w = admin.do("POST", "/api/v1/admin/users", map[string]string{
		"username": "newbie", "password": "password1234",
	}, true)
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate username: got %d, want 409", w.Code)
	}

	// Short password rejected.
	w = admin.do("POST", "/api/v1/admin/users", map[string]string{
		"username": "shorty", "password": "short",
	}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("short password: got %d, want 400", w.Code)
	}

	// List includes the new user.
	w = admin.do("GET", "/api/v1/admin/users", nil, false)
	var list struct {
		Users []userJSON `json:"users"`
	}
	decodeJSON(t, w, &list)
	if len(list.Users) != 3 {
		t.Fatalf("list users: got %d, want 3", len(list.Users))
	}

	// Update profile + promote.
	uid := created.User.ID
	w = admin.do("PUT", "/api/v1/admin/users/"+itoa2(uid), map[string]string{
		"displayName": "Newbie Renamed", "role": "admin", "email": "n@example.org",
	}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("update user: %d %s", w.Code, w.Body.String())
	}

	// Cannot demote the last admin... (there are 2 admins now, so demote newbie back)
	w = admin.do("PUT", "/api/v1/admin/users/"+itoa2(uid), map[string]string{"role": "member"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("demote second admin: %d %s", w.Code, w.Body.String())
	}
	// Now only "admin" is admin: demoting them fails.
	var adminUser models.User
	app.DB.Where("username = ?", "admin").First(&adminUser)
	w = admin.do("PUT", "/api/v1/admin/users/"+itoa2(adminUser.ID), map[string]string{"role": "member"}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("demote last admin: got %d, want 400", w.Code)
	}
	// Cannot delete self.
	w = admin.do("DELETE", "/api/v1/admin/users/"+itoa2(adminUser.ID), nil, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("delete self: got %d, want 400", w.Code)
	}

	// Delete the new user.
	w = admin.do("DELETE", "/api/v1/admin/users/"+itoa2(uid), nil, true)
	if w.Code != http.StatusOK {
		t.Fatalf("delete user: %d %s", w.Code, w.Body.String())
	}
	var gone models.User
	if err := app.DB.Where("username = ?", "newbie").First(&gone).Error; err == nil {
		t.Fatalf("user still present after delete")
	}
}

func TestAdminResetPassword(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", "admin")
	newTestUser(t, app, "grier", "password1234", "member")
	admin := loginAs(t, app, "admin", "password1234")

	var u models.User
	app.DB.Where("username = ?", "grier").First(&u)
	w := admin.do("POST", "/api/v1/admin/users/"+itoa2(u.ID)+"/password",
		map[string]string{"password": "brand-new-password"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("reset password: %d %s", w.Code, w.Body.String())
	}
	// Old password fails, new one works.
	bad := loginAPIExpectFail(t, app, "grier", "password1234")
	if bad {
		t.Fatalf("old password still works after reset")
	}
	loginAs(t, app, "grier", "brand-new-password")

	// Short password rejected.
	w = admin.do("POST", "/api/v1/admin/users/"+itoa2(u.ID)+"/password",
		map[string]string{"password": "short"}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("short reset password: got %d, want 400", w.Code)
	}
}

// loginAPIExpectFail reports whether login succeeded (it shouldn't).
func loginAPIExpectFail(t *testing.T, app *App, username, password string) bool {
	t.Helper()
	c := newAPIClient(t, app)
	c.fetchCSRF()
	w := c.do("POST", "/api/v1/auth/login",
		map[string]string{"username": username, "password": password}, true)
	return w.Code == http.StatusOK
}

func TestAdminGroupMembers(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", "admin")
	leader := newTestUser(t, app, "grier", "password1234", "member")
	other := newTestUser(t, app, "other", "password1234", "member")
	g := models.Group{Name: "Test Group", LeaderID: leader.ID, Status: models.GroupActive}
	if err := app.DB.Create(&g).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	admin := loginAs(t, app, "admin", "password1234")
	membersPath := "/api/v1/admin/groups/" + itoa2(g.ID) + "/members"

	// Add a member.
	w := admin.do("POST", membersPath, map[string]interface{}{
		"userId": other.ID, "role": "member",
	}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("add member: %d %s", w.Code, w.Body.String())
	}
	// Re-adding is idempotent-ish (200).
	w = admin.do("POST", membersPath, map[string]interface{}{
		"userId": other.ID, "role": "member",
	}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("re-add member: %d %s", w.Code, w.Body.String())
	}
	// Promote to leader reassigns group leadership.
	w = admin.do("POST", membersPath, map[string]interface{}{
		"userId": other.ID, "role": "leader",
	}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("promote to leader: %d %s", w.Code, w.Body.String())
	}
	var got models.Group
	app.DB.First(&got, g.ID)
	if got.LeaderID != other.ID {
		t.Fatalf("leader not reassigned: %d", got.LeaderID)
	}
	// Cannot remove the current leader.
	w = admin.do("DELETE", membersPath+"/"+itoa2(other.ID), nil, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("remove leader: got %d, want 400", w.Code)
	}
	// Removing a regular member works.
	var oldLeader models.User
	app.DB.Where("username = ?", "grier").First(&oldLeader)
	// grier's membership role was demoted to member on reassignment; add first
	w = admin.do("POST", membersPath, map[string]interface{}{
		"userId": oldLeader.ID, "role": "member",
	}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("add old leader as member: %d %s", w.Code, w.Body.String())
	}
	w = admin.do("DELETE", membersPath+"/"+itoa2(oldLeader.ID), nil, true)
	if w.Code != http.StatusOK {
		t.Fatalf("remove member: %d %s", w.Code, w.Body.String())
	}
	// Non-admin forbidden.
	memberClient := loginAs(t, app, "other", "password1234")
	w = memberClient.do("POST", membersPath, map[string]interface{}{"userId": oldLeader.ID}, true)
	if w.Code != http.StatusForbidden {
		t.Fatalf("member add: got %d, want 403", w.Code)
	}
}

func TestAdminListGroups(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", "admin")
	leader := newTestUser(t, app, "grier", "password1234", "member")
	for _, st := range []string{models.GroupActive, models.GroupPending} {
		g := models.Group{Name: "G " + st, LeaderID: leader.ID, Status: st}
		if err := app.DB.Create(&g).Error; err != nil {
			t.Fatalf("create group: %v", err)
		}
	}
	admin := loginAs(t, app, "admin", "password1234")
	w := admin.do("GET", "/api/v1/admin/groups", nil, false)
	if w.Code != http.StatusOK {
		t.Fatalf("list groups: %d", w.Code)
	}
	var resp struct {
		Groups []groupSummaryJSON `json:"groups"`
	}
	decodeJSON(t, w, &resp)
	if len(resp.Groups) != 2 {
		t.Fatalf("got %d groups, want 2 (all statuses)", len(resp.Groups))
	}
}

func TestNotifications(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", "admin")
	member := newTestUser(t, app, "grier", "password1234", "member")
	// Pending group → admin sees an approval notification.
	pg := models.Group{Name: "Pending", LeaderID: member.ID, Status: models.GroupPending}
	if err := app.DB.Create(&pg).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	// Active group with a meeting in 2 days → member sees an event notification.
	g := models.Group{Name: "Active", LeaderID: member.ID, Status: models.GroupActive}
	if err := app.DB.Create(&g).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	start := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Hour)
	mt := models.Meeting{GroupID: g.ID, StartsAt: start, EndsAt: start.Add(time.Hour)}
	if err := app.DB.Create(&mt).Error; err != nil {
		t.Fatalf("create meeting: %v", err)
	}
	app.DB.Create(&models.Membership{UserID: member.ID, GroupID: g.ID, Role: "member", Status: "active", JoinedAt: time.Now().UTC()})

	adminClient := loginAs(t, app, "admin", "password1234")
	w := adminClient.do("GET", "/api/v1/notifications", nil, false)
	if w.Code != http.StatusOK {
		t.Fatalf("admin notifications: %d", w.Code)
	}
	var an struct {
		Notifications []notificationJSON `json:"notifications"`
	}
	decodeJSON(t, w, &an)
	foundApproval := false
	for _, n := range an.Notifications {
		if n.Type == "approval" {
			foundApproval = true
		}
	}
	if !foundApproval {
		t.Fatalf("admin missing approval notification: %+v", an.Notifications)
	}

	memberClient := loginAs(t, app, "grier", "password1234")
	w = memberClient.do("GET", "/api/v1/notifications", nil, false)
	var mn struct {
		Notifications []notificationJSON `json:"notifications"`
	}
	decodeJSON(t, w, &mn)
	foundEvent := false
	for _, n := range mn.Notifications {
		if n.Type == "event" && n.Title == "Active" {
			foundEvent = true
		}
	}
	if !foundEvent {
		t.Fatalf("member missing event notification: %+v", mn.Notifications)
	}

	// Unauthenticated → 401.
	anon := newAPIClient(t, app)
	w = anon.do("GET", "/api/v1/notifications", nil, false)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anon notifications: got %d, want 401", w.Code)
	}
}

func TestFocalPoints(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "grier", "password1234", "member")
	leader := newTestUser(t, app, "leader", "password1234", "member")
	c := loginAs(t, app, "grier", "password1234")

	w := c.do("PUT", "/api/v1/me", map[string]interface{}{
		"avatarFocal": map[string]float64{"x": 30, "y": 70},
	}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT /me focal: %d %s", w.Code, w.Body.String())
	}
	var me struct {
		User userJSON `json:"user"`
	}
	decodeJSON(t, w, &me)
	if me.User.AvatarFocalX != 30 || me.User.AvatarFocalY != 70 {
		t.Fatalf("focal not saved: %+v", me.User)
	}

	// Clamping.
	w = c.do("PUT", "/api/v1/me", map[string]interface{}{
		"avatarFocal": map[string]float64{"x": 150, "y": -20},
	}, true)
	decodeJSON(t, w, &me)
	if me.User.AvatarFocalX != 100 || me.User.AvatarFocalY != 0 {
		t.Fatalf("focal not clamped: %+v", me.User)
	}

	// Group cover focal (leader).
	g := models.Group{Name: "G", LeaderID: leader.ID, Status: models.GroupActive}
	app.DB.Create(&g)
	lc := loginAs(t, app, "leader", "password1234")
	w = lc.do("PUT", "/api/v1/groups/"+itoa2(g.ID), map[string]interface{}{
		"coverFocal": map[string]float64{"x": 25, "y": 25},
	}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT group focal: %d %s", w.Code, w.Body.String())
	}
	var gr struct {
		Group groupSummaryJSON `json:"group"`
	}
	decodeJSON(t, w, &gr)
	if gr.Group.CoverFocalX != 25 || gr.Group.CoverFocalY != 25 {
		t.Fatalf("cover focal not saved: %+v", gr.Group)
	}
}

func itoa2(i uint) string {
	return uintToStringTest(i)
}

func uintToStringTest(u uint) string {
	if u == 0 {
		return "0"
	}
	var b [32]byte
	i := len(b)
	for u > 0 {
		i--
		b[i] = byte('0' + u%10)
		u /= 10
	}
	return string(b[i:])
}
