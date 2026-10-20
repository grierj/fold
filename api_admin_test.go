package main

import (
	"fold/internal/audit"
	"fold/models"
	"net/http"
	"testing"
	"time"
)

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
		"email": "Newbie@Example.com", "name": "Newbie", "password": "password1234", "role": "member",
		"displayName": "Newbie",
	}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create user: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		User userJSON `json:"user"`
	}
	decodeJSON(t, w, &created)
	if created.User.Email != "newbie@example.com" || created.User.DisplayName != "Newbie" {
		t.Fatalf("unexpected created user: %+v", created.User)
	}

	// Duplicate email (any case) conflicts.
	w = admin.do("POST", "/api/v1/admin/users", map[string]string{
		"email": "NEWBIE@example.com", "name": "Again", "password": "password1234",
	}, true)
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate email: got %d, want 409", w.Code)
	}

	// Short password rejected.
	w = admin.do("POST", "/api/v1/admin/users", map[string]string{
		"email": "shorty@example.com", "name": "Shorty", "password": "short",
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
	app.DB.Where("email = ?", testEmail("admin")).First(&adminUser)
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
	if err := app.DB.Where("email = ?", testEmail("newbie")).First(&gone).Error; err == nil {
		t.Fatalf("user still present after delete")
	}
}

func TestAdminResetPassword(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", "admin")
	newTestUser(t, app, "grier", "password1234", "member")
	admin := loginAs(t, app, "admin", "password1234")

	var u models.User
	app.DB.Where("email = ?", testEmail("grier")).First(&u)
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
	app.DB.Where("email = ?", testEmail("grier")).First(&oldLeader)
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

func TestAdminGroupsStartActive(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	adminC := loginAPI(t, app, "admin", "password1234")
	var body struct {
		Group groupSummaryJSON `json:"group"`
	}
	decodeJSON(t, adminC.do("POST", "/api/v1/groups", map[string]any{"name": "Staff"}, true), &body)
	if body.Group.Status != models.GroupActive {
		t.Fatalf("admin-created group status = %q", body.Group.Status)
	}
}

// DEC-38: a disabled account keeps its memberships and RSVPs, hidden and
// uncounted; re-enabling brings them back as plain member, never leader.
func TestDisableKeepsMembershipsHiddenAndReenableRestoresMember(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	pat := newTestUser(t, app, "pat", "password1234", models.RoleMember)
	co := newTestUser(t, app, "co", "password1234", models.RoleMember)
	viewer := newTestUser(t, app, "viewer", "password1234", models.RoleMember)
	led := newTestGroup(t, app, pat, "Led", models.GroupActive) // pat leads alone
	coled := newTestGroup(t, app, co, "Coled", models.GroupActive)
	addMember(t, app, pat, coled, models.MembershipLeader)
	joined := newTestGroup(t, app, co, "Joined", models.GroupActive)
	addMember(t, app, pat, joined, models.MembershipMember)
	for _, g := range []*models.Group{led, coled, joined} {
		addMember(t, app, viewer, g, models.MembershipMember)
	}
	start := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Hour)
	_, occ := seedEvent(t, app, joined, co.ID, start)
	app.DB.Create(&models.Registration{UserID: pat.ID, OccurrenceID: occ.ID, Status: models.RegGoing})
	app.DB.Create(&models.Registration{UserID: viewer.ID, OccurrenceID: occ.ID, Status: models.RegGoing})

	admin := loginAs(t, app, "admin", "password1234")
	other := loginAs(t, app, "viewer", "password1234")
	type detail struct {
		Group    groupSummaryJSON `json:"group"`
		Members  []memberJSON     `json:"members"`
		Upcoming []struct {
			Registrations map[string]int64 `json:"registrations"`
		} `json:"upcomingEvents"`
	}
	look := func(g *models.Group) (d detail) {
		t.Helper()
		decodeJSON(t, other.do("GET", "/api/v1/groups/"+itoa(g.ID), nil, false), &d)
		return d
	}
	listed := func(d detail) (role string) {
		for _, m := range d.Members {
			if m.ID == pat.ID {
				return m.Role
			}
		}
		return ""
	}
	before := look(joined)
	if listed(before) != "member" || before.Group.MemberCount != 3 || before.Upcoming[0].Registrations["going"] != 2 {
		t.Fatalf("before: %+v", before)
	}

	w := admin.do("PUT", "/api/v1/admin/users/"+itoa(pat.ID),
		map[string]any{"status": "disabled", "successors": map[string]uint{itoa(led.ID): viewer.ID}}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", w.Code, w.Body.String())
	}
	// Hidden from rosters, member counts and RSVP counts; rows kept.
	size := map[*models.Group]int64{led: 2, coled: 3, joined: 3} // with pat
	for _, g := range []*models.Group{led, coled, joined} {
		d := look(g)
		if listed(d) != "" || d.Group.MemberCount != size[g]-1 {
			t.Fatalf("%s while disabled: listed %q, count %d", g.Name, listed(d), d.Group.MemberCount)
		}
		if membershipStatus(app, pat, g) != "active" || relIn(app.DB, pat.ID, g.ID).Leader {
			t.Fatalf("%s: membership should be kept as plain member", g.Name)
		}
	}
	if d := look(joined); d.Upcoming[0].Registrations["going"] != 1 {
		t.Fatalf("disabled registration counted: %+v", d.Upcoming)
	}
	if leaderCount(app.DB, coled.ID) != 1 || !relIn(app.DB, viewer.ID, led.ID).Leader {
		t.Fatal("leadership should have moved")
	}

	// Re-enabled: back as plain member everywhere, RSVP counted, audited.
	if w := admin.do("PUT", "/api/v1/admin/users/"+itoa(pat.ID), map[string]string{"status": "active"}, true); w.Code != http.StatusOK {
		t.Fatalf("enable: %d %s", w.Code, w.Body.String())
	}
	for _, g := range []*models.Group{led, coled, joined} {
		if d := look(g); listed(d) != "member" || d.Group.MemberCount != size[g] {
			t.Fatalf("%s after enable: listed %q, count %d", g.Name, listed(d), d.Group.MemberCount)
		}
	}
	if d := look(joined); d.Upcoming[0].Registrations["going"] != 2 {
		t.Fatalf("registration not restored: %+v", d.Upcoming)
	}
	if rows, metas := auditRows(t, app, audit.MemberRestored); len(rows) != 3 || metas[0].UserID != pat.ID {
		t.Fatalf("restored rows: %+v", rows)
	}
	if rows, _ := auditRows(t, app, audit.UserEnabled); len(rows) != 1 {
		t.Fatalf("user.enabled rows: %d", len(rows))
	}
}

// Disabling a going account frees its seats at once: the next waitlisted
// party is promoted in the disable transaction.
func TestDisablePromotesWaitlistedParty(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "DisablePromo")
	eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(1), true)

	var g models.Group
	app.DB.Where("id = ?", gid).First(&g)
	u1 := newTestUser(t, app, "d1", "password1234", models.RoleMember)
	u2 := newTestUser(t, app, "d2", "password1234", models.RoleMember)
	addMember(t, app, u1, &g, models.MembershipMember)
	addMember(t, app, u2, &g, models.MembershipMember)
	c1 := loginAPI(t, app, "d1", "password1234")
	c2 := loginAPI(t, app, "d2", "password1234")
	if code, _ := registerAs(c1, eventID, date, "going", nil); code != http.StatusOK {
		t.Fatal("d1 going")
	}
	if code, _ := registerAs(c2, eventID, date, "going", nil); code != http.StatusOK {
		t.Fatal("d2 going->waitlisted")
	}
	var occ models.EventOccurrence
	app.DB.Where("event_id = ?", eventID).First(&occ)

	w := admin.do("PUT", "/api/v1/admin/users/"+itoa(u1.ID), map[string]any{"status": "disabled"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", w.Code, w.Body.String())
	}
	// The disabled account's row is kept but uses no seat; d2 is promoted.
	var r1 models.Registration
	app.DB.Where("user_id = ? AND occurrence_id = ?", u1.ID, occ.ID).First(&r1)
	if r1.Status != models.RegGoing {
		t.Fatalf("disabled row status = %q, want kept as going", r1.Status)
	}
	used, err := seatsUsed(app.DB, occ.ID)
	if err != nil {
		t.Fatal(err)
	}
	if used != 1 {
		t.Fatalf("seats used after disable = %d, want 1 (only d2)", used)
	}
	var r2 models.Registration
	app.DB.Where("user_id = ? AND occurrence_id = ?", u2.ID, occ.ID).First(&r2)
	if r2.Status != models.RegGoing || r2.PromotedAt == nil {
		t.Fatalf("d2 after disable: status=%q promoted_at=%v", r2.Status, r2.PromotedAt)
	}
}

// Re-enabling into room restores the going row; re-enabling into a full
// occurrence deletes it, writes registration.not_restored, and sends the
// ask-a-leader notice.
func TestReenableRestoresOrDropsRegistration(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "Reenable")
	eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(1), true)

	var g models.Group
	app.DB.Where("id = ?", gid).First(&g)
	u1 := newTestUser(t, app, "e1", "password1234", models.RoleMember)
	u2 := newTestUser(t, app, "e2", "password1234", models.RoleMember)
	addMember(t, app, u1, &g, models.MembershipMember)
	addMember(t, app, u2, &g, models.MembershipMember)
	c1 := loginAPI(t, app, "e1", "password1234")
	c2 := loginAPI(t, app, "e2", "password1234")
	if code, _ := registerAs(c1, eventID, date, "going", nil); code != http.StatusOK {
		t.Fatal("e1 going")
	}
	var occ models.EventOccurrence
	app.DB.Where("event_id = ?", eventID).First(&occ)

	// Disable e1 (no waitlist, so nothing promotes), then re-enable into
	// room: the row is restored.
	w := admin.do("PUT", "/api/v1/admin/users/"+itoa(u1.ID), map[string]any{"status": "disabled"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", w.Code, w.Body.String())
	}
	w = admin.do("PUT", "/api/v1/admin/users/"+itoa(u1.ID), map[string]any{"status": "active"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("re-enable: %d %s", w.Code, w.Body.String())
	}
	if got := regStatus(t, app, u1.ID, occ.ID); got != models.RegGoing {
		t.Fatalf("restored status = %q, want going", got)
	}

	// Now e2 takes the seat, e1 is disabled again, and re-enabled into a
	// full occurrence: the row is dropped with a notice.
	if code, _ := registerAs(c2, eventID, date, "going", nil); code != http.StatusOK {
		t.Fatalf("e2 going: %d", code)
	}
	w = admin.do("PUT", "/api/v1/admin/users/"+itoa(u1.ID), map[string]any{"status": "disabled"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("disable again: %d %s", w.Code, w.Body.String())
	}
	// e2's seat is taken; fill it so e1 can't come back.
	w = admin.do("PUT", "/api/v1/admin/users/"+itoa(u1.ID), map[string]any{"status": "active"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("re-enable into full: %d %s", w.Code, w.Body.String())
	}
	var n int64
	app.DB.Model(&models.Registration{}).Where("user_id = ? AND occurrence_id = ?", u1.ID, occ.ID).Count(&n)
	if n != 0 {
		t.Fatalf("e1 row still exists after drop")
	}
	if c := auditCount(t, app, audit.RegistrationNotRestored); c != 1 {
		t.Fatalf("registration.not_restored rows = %d, want 1", c)
	}
	// Disabling bumped the session, so log back in to read the bell.
	c1b := loginAPI(t, app, "e1", "password1234")
	if got := findKind(feed(t, c1b, ""), "registration.not_restored"); got == nil {
		t.Fatalf("no not_restored notice for e1")
	}
}

// #94: an admin edit can't make a teen an admin, can't activate a teen
// waiting for consent (even one with a password), and re-enabling a
// disabled teen sends it back to waiting for consent.
func TestAdminEditCannotBypassTeenConsent(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "pw")
	teen := models.User{Email: "teen94@example.com", Name: "Teen", Role: models.RoleMember,
		Status: models.UserPendingConsent, AccountType: models.AccountTeen}
	if err := teen.SetPassword("password1234"); err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Create(&teen).Error; err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/admin/users/" + itoa2(teen.ID)
	reload := func() models.User {
		var u models.User
		app.DB.First(&u, teen.ID)
		return u
	}

	apiDo(t, admin, "PUT", path, map[string]string{"status": models.UserActive}, http.StatusBadRequest)
	apiDo(t, admin, "PUT", path, map[string]string{"role": models.RoleAdmin}, http.StatusBadRequest)
	if u := reload(); u.Status != models.UserPendingConsent || u.Role != models.RoleMember {
		t.Fatalf("teen changed: %s %s", u.Status, u.Role)
	}

	// Disable, then re-enable: back to pending_consent, not active.
	apiDo(t, admin, "PUT", path, map[string]string{"status": models.UserDisabled}, http.StatusOK)
	apiDo(t, admin, "PUT", path, map[string]string{"status": models.UserActive}, http.StatusOK)
	if u := reload(); u.Status != models.UserPendingConsent {
		t.Fatalf("re-enabled teen: %s, want pending_consent", u.Status)
	}
	if code, _ := tryLogin(t, app, "", teen.Email, "password1234"); code == http.StatusOK {
		t.Fatal("teen waiting for consent signed in")
	}

	// An adult member can still be promoted and re-enabled as before.
	adult := newTestUser(t, app, "adult94", "password1234", models.RoleMember)
	apiDo(t, admin, "PUT", "/api/v1/admin/users/"+itoa2(adult.ID), map[string]string{"role": models.RoleAdmin}, http.StatusOK)
}
