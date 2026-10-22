package main

import (
	"fold/internal/audit"
	"fold/models"
	"net/http"
	"strings"
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
	app.DB.Create(&models.Registration{UserID: &pat.ID, OccurrenceID: occ.ID, Status: models.RegGoing})
	app.DB.Create(&models.Registration{UserID: &viewer.ID, OccurrenceID: occ.ID, Status: models.RegGoing})

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

// DEC-38/47 for the remote pool: a disabled account's remote
// registration counts in neither pool, and re-enabling into a full
// remote pool leaves the registration out, even with in-person seats
// to spare.
func TestDisabledRemoteRegistrationUncountedAndLeftOutWhenFull(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "ReenableRemote")
	eventID, date := seedRemoteEvent(t, app, admin, gid, intPtr(10), intPtr(1), true)

	var g models.Group
	app.DB.Where("id = ?", gid).First(&g)
	u1 := newTestUser(t, app, "re1", "password1234", models.RoleMember)
	u2 := newTestUser(t, app, "re2", "password1234", models.RoleMember)
	addMember(t, app, u1, &g, models.MembershipMember)
	addMember(t, app, u2, &g, models.MembershipMember)
	c1 := loginAPI(t, app, "re1", "password1234")
	c2 := loginAPI(t, app, "re2", "password1234")
	if code, body := registerModeAs(c1, eventID, date, "going", models.AttendanceRemote); code != http.StatusOK {
		t.Fatalf("re1 remote going: %d %s", code, body)
	}
	if code, body := registerModeAs(c2, eventID, date, "going", models.AttendanceRemote); code != http.StatusOK {
		t.Fatalf("re2 remote going->waitlisted: %d %s", code, body)
	}
	var occ models.EventOccurrence
	app.DB.Where("event_id = ?", eventID).First(&occ)
	if used, err := remoteSeatsUsed(app.DB, occ.ID); err != nil || used != 1 {
		t.Fatalf("remote seats before disable = %d (%v), want 1", used, err)
	}

	// Disable re1: the row is kept as going but uses no remote seat,
	// so re2 is promoted into the freed seat and the count stays 1.
	w := admin.do("PUT", "/api/v1/admin/users/"+itoa(u1.ID), map[string]any{"status": "disabled"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", w.Code, w.Body.String())
	}
	var r1 models.Registration
	app.DB.Where("user_id = ? AND occurrence_id = ?", u1.ID, occ.ID).First(&r1)
	if r1.Status != models.RegGoing || r1.AttendanceMode != models.AttendanceRemote {
		t.Fatalf("disabled remote row: status=%q mode=%q, want kept going remote", r1.Status, r1.AttendanceMode)
	}
	if used, err := remoteSeatsUsed(app.DB, occ.ID); err != nil || used != 1 {
		t.Fatalf("remote seats after disable = %d (%v), want 1 (only re2; re1 uncounted)", used, err)
	}
	var r2 models.Registration
	app.DB.Where("user_id = ? AND occurrence_id = ?", u2.ID, occ.ID).First(&r2)
	if r2.Status != models.RegGoing || r2.PromotedAt == nil {
		t.Fatalf("re2 after disable: status=%q promoted_at=%v", r2.Status, r2.PromotedAt)
	}

	// Re-enable re1: the remote pool is full (re2), so re1's row is
	// dropped with the audit row and notice, despite empty in-person seats.
	w = admin.do("PUT", "/api/v1/admin/users/"+itoa(u1.ID), map[string]any{"status": "active"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("re-enable: %d %s", w.Code, w.Body.String())
	}
	var n int64
	app.DB.Model(&models.Registration{}).Where("user_id = ? AND occurrence_id = ?", u1.ID, occ.ID).Count(&n)
	if n != 0 {
		t.Fatal("re1 row still exists after re-enable into a full remote pool")
	}
	if c := auditCount(t, app, audit.RegistrationNotRestored); c != 1 {
		t.Fatalf("registration.not_restored rows = %d, want 1", c)
	}
	c1b := loginAPI(t, app, "re1", "password1234")
	if got := findKind(feed(t, c1b, ""), "registration.not_restored"); got == nil {
		t.Fatalf("no not_restored notice for re1")
	}
	if got := regStatus(t, app, u2.ID, occ.ID); got != models.RegGoing {
		t.Fatalf("re2 = %q, want still going", got)
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

// Guardian links (M1.9 slice 1, DEC-32): a child or teen added to a
// household waits for an admin, who confirms it only with a check note.
func TestAdminGuardianLinksConfirmReject(t *testing.T) {
	app := testApp(t)
	adminUser := newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	newTestUser(t, app, "pat", "password1234", models.RoleMember)
	admin := loginAPI(t, app, "admin", "password1234")
	pat := loginAPI(t, app, "pat", "password1234")

	child := addMyPerson(t, pat, map[string]any{"firstName": "Kid", "lastName": "Smith", "kind": "child", "birthDate": "2020-05-01"})
	teen := addMyPerson(t, pat, map[string]any{"firstName": "Tess", "kind": "teen", "birthDate": "2012-03-04"})
	addMyPerson(t, pat, map[string]any{"firstName": "Mary", "kind": "adult"})

	// Members never see or decide the queue.
	if w := pat.do("GET", "/api/v1/admin/guardian-links?status=pending", nil, false); w.Code != http.StatusForbidden {
		t.Fatalf("member lists guardian links: %d, want 403", w.Code)
	}

	listLinks := func(status string) []guardianLinkJSON {
		t.Helper()
		w := admin.do("GET", "/api/v1/admin/guardian-links?status="+status, nil, false)
		if w.Code != http.StatusOK {
			t.Fatalf("list guardian links %s: %d %s", status, w.Code, w.Body.String())
		}
		var body struct {
			GuardianLinks []guardianLinkJSON `json:"guardianLinks"`
		}
		decodeJSON(t, w, &body)
		return body.GuardianLinks
	}
	pending := listLinks("pending")
	if len(pending) != 2 {
		t.Fatalf("pending links: %+v", pending)
	}
	var childLink, teenLink guardianLinkJSON
	for _, l := range pending {
		if l.PersonID != nil && *l.PersonID == child.ID {
			childLink = l
		}
		if l.PersonID != nil && *l.PersonID == teen.ID {
			teenLink = l
		}
	}
	if childLink.PersonName != "Kid Smith" || childLink.PersonKind != "child" || childLink.BirthDate != "2020-05-01" || childLink.GuardianName == "" {
		t.Fatalf("child link row: %+v", childLink)
	}
	if teenLink.PersonKind != "teen" {
		t.Fatalf("teen link row: %+v", teenLink)
	}
	if w := admin.do("GET", "/api/v1/admin/guardian-links?status=bogus", nil, false); w.Code != http.StatusBadRequest {
		t.Fatalf("bogus status: %d, want 400", w.Code)
	}

	confirmPath := "/api/v1/admin/guardian-links/" + itoa2(childLink.ID) + "/confirm"
	// No check note, no confirmation (DEC-32), and nothing changes.
	w := admin.do("POST", confirmPath, map[string]string{}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("confirm without a note: %d %s, want 400", w.Code, w.Body.String())
	}
	var link models.GuardianLink
	app.DB.First(&link, childLink.ID)
	if link.Status != models.GuardianPending {
		t.Fatalf("link after noteless confirm: %+v", link)
	}

	// With a note: confirmed, by this admin, decided now, task resolved.
	w = admin.do("POST", confirmPath, map[string]string{"checkNote": "Known family; parent came to the office"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("confirm link: %d %s", w.Code, w.Body.String())
	}
	var decided struct {
		GuardianLink guardianLinkJSON `json:"guardianLink"`
	}
	decodeJSON(t, w, &decided)
	if decided.GuardianLink.Status != models.GuardianConfirmed || decided.GuardianLink.ConfirmedBy == nil ||
		*decided.GuardianLink.ConfirmedBy != adminUser.ID || decided.GuardianLink.CheckNote == "" || decided.GuardianLink.DecidedAt == nil {
		t.Fatalf("confirmed link: %+v", decided.GuardianLink)
	}
	if rows, _ := auditRows(t, app, audit.GuardianLinkConfirmed); len(rows) != 1 || rows[0].TargetType != "guardian_link" || rows[0].TargetID != childLink.ID {
		t.Fatalf("guardian_link.confirmed audit: %+v", rows)
	}
	if _, _, waiting, _ := badge(t, admin, ""); waiting != 1 {
		t.Fatalf("admin waiting after one confirm: %d, want 1 (the teen)", waiting)
	}
	if h := getMyHousehold(t, pat); h.People[0].LinkStatus != models.GuardianConfirmed {
		found := ""
		for _, p := range h.People {
			if p.ID == child.ID {
				found = p.LinkStatus
			}
		}
		t.Fatalf("holder sees child link as %q, want confirmed", found)
	}
	// Deciding again loses the race cleanly.
	if w := admin.do("POST", confirmPath, map[string]string{"checkNote": "again"}, true); w.Code != http.StatusConflict {
		t.Fatalf("second confirm: %d %s, want 409", w.Code, w.Body.String())
	}

	// Rejecting needs no note; the holder sees the rejection.
	rejectPath := "/api/v1/admin/guardian-links/" + itoa2(teenLink.ID) + "/reject"
	w = admin.do("POST", rejectPath, map[string]string{}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("reject link: %d %s", w.Code, w.Body.String())
	}
	decodeJSON(t, w, &decided)
	if decided.GuardianLink.Status != models.GuardianRejected {
		t.Fatalf("rejected link: %+v", decided.GuardianLink)
	}
	if rows, _ := auditRows(t, app, audit.GuardianLinkRejected); len(rows) != 1 {
		t.Fatalf("guardian_link.rejected audit rows: %d", len(rows))
	}
	if _, _, waiting, _ := badge(t, admin, ""); waiting != 0 {
		t.Fatalf("admin waiting after both decisions: %d", waiting)
	}
	if links := listLinks("pending"); len(links) != 0 {
		t.Fatalf("pending after decisions: %+v", links)
	}
	if links := listLinks("confirmed"); len(links) != 1 || links[0].ID != childLink.ID {
		t.Fatalf("confirmed list: %+v", links)
	}
	for _, p := range getMyHousehold(t, pat).People {
		if p.ID == teen.ID && p.LinkStatus != models.GuardianRejected {
			t.Fatalf("holder sees teen link as %q, want rejected", p.LinkStatus)
		}
	}
	// A missing link is a 404 for an admin too.
	if w := admin.do("POST", "/api/v1/admin/guardian-links/999999/confirm", map[string]string{"checkNote": "x"}, true); w.Code != http.StatusNotFound {
		t.Fatalf("missing link: %d, want 404", w.Code)
	}
}

// DEC-79: an admin cannot confirm their own guardian link while another
// active admin exists; another admin can. A sole admin may confirm their
// own link, and the audit row marks it as a self-confirm.
func TestGuardianLinkSelfConfirm(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "ada", "password1234", models.RoleAdmin)
	ada := loginAPI(t, app, "ada", "password1234")
	pendingFor := func(c *apiClient, name string) uint {
		t.Helper()
		p := addMyPerson(t, c, map[string]any{"firstName": name, "kind": "child", "birthDate": "2020-05-01"})
		var link models.GuardianLink
		if err := app.DB.Where("person_id = ?", p.ID).First(&link).Error; err != nil {
			t.Fatal(err)
		}
		return link.ID
	}
	note := map[string]string{"checkNote": "Known family"}

	// Sole admin: allowed and audited as a self-confirm.
	solo := pendingFor(ada, "Solo")
	if w := ada.do("POST", "/api/v1/admin/guardian-links/"+itoa2(solo)+"/confirm", note, true); w.Code != http.StatusOK {
		t.Fatalf("sole admin self-confirm: %d %s", w.Code, w.Body.String())
	}
	rows, metas := auditRows(t, app, audit.GuardianLinkConfirmed)
	if len(rows) != 1 || rows[0].TargetID != solo || !metas[0].SelfConfirm {
		t.Fatalf("self-confirm audit: %+v %+v", rows, metas)
	}

	// With a second active admin, self-confirm is refused and nothing changes.
	newTestUser(t, app, "bea", "password1234", models.RoleAdmin)
	bea := loginAPI(t, app, "bea", "password1234")
	second := pendingFor(ada, "Second")
	path := "/api/v1/admin/guardian-links/" + itoa2(second) + "/confirm"
	if w := ada.do("POST", path, note, true); w.Code != http.StatusForbidden {
		t.Fatalf("multi-admin self-confirm: %d %s, want 403", w.Code, w.Body.String())
	}
	var link models.GuardianLink
	app.DB.First(&link, second)
	if link.Status != models.GuardianPending {
		t.Fatalf("link after refused self-confirm: %+v", link)
	}
	// The other admin can confirm it, and it is not a self-confirm.
	if w := bea.do("POST", path, note, true); w.Code != http.StatusOK {
		t.Fatalf("other admin confirm: %d %s", w.Code, w.Body.String())
	}
	rows, metas = auditRows(t, app, audit.GuardianLinkConfirmed)
	if len(rows) != 2 || rows[1].TargetID != second || metas[1].SelfConfirm {
		t.Fatalf("other-admin audit: %+v %+v", rows, metas)
	}
}

// DEC-79: only other *active* admins block a self-confirm. A disabled or
// not-yet-active second admin does not (Warden, #106).
func TestGuardianLinkSelfConfirmIgnoresInactiveAdmins(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "ada", "password1234", models.RoleAdmin)
	off := newTestUser(t, app, "off", "password1234", models.RoleAdmin)
	waiting := newTestUser(t, app, "waiting", "password1234", models.RoleAdmin)
	app.DB.Model(&models.User{}).Where("id = ?", off.ID).Update("status", models.UserDisabled)
	app.DB.Model(&models.User{}).Where("id = ?", waiting.ID).Update("status", models.UserInvited)
	ada := loginAPI(t, app, "ada", "password1234")

	p := addMyPerson(t, ada, map[string]any{"firstName": "Kid", "kind": "child", "birthDate": "2020-05-01"})
	var link models.GuardianLink
	if err := app.DB.Where("person_id = ?", p.ID).First(&link).Error; err != nil {
		t.Fatal(err)
	}
	w := ada.do("POST", "/api/v1/admin/guardian-links/"+itoa2(link.ID)+"/confirm", map[string]string{"checkNote": "Known family"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("self-confirm with only inactive other admins: %d %s", w.Code, w.Body.String())
	}
	if _, metas := auditRows(t, app, audit.GuardianLinkConfirmed); len(metas) != 1 || !metas[0].SelfConfirm {
		t.Fatalf("self-confirm audit: %+v", metas)
	}
}

func TestDisabledChildcareUncountedAndLeftOutWhenFull(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "ChildcareRestore")
	id, date := seedChildcareEvent(t, app, admin, gid, nil, intPtr(1), false)
	u := newTestUser(t, app, "restoreparent", "password1234", models.RoleMember)
	c := loginAPI(t, app, "restoreparent", "password1234")
	r := childcareAs(t, c, id, date, "going", 1, http.StatusOK)
	apiDo(t, admin, "PUT", "/api/v1/admin/users/"+itoa(u.ID), map[string]any{"status": "disabled"}, http.StatusOK)
	if used, err := childcareUsedFor(app.DB, r.OccurrenceID, 0); err != nil || used != 0 {
		t.Fatalf("disabled childcare counts: %d %v", used, err)
	}
	// This going request fits only because the disabled request is ignored.
	childcareAs(t, admin, id, date, "going", 1, http.StatusOK)
	apiDo(t, admin, "PUT", "/api/v1/admin/users/"+itoa(u.ID), map[string]any{"status": "active"}, http.StatusOK)
	var n int64
	app.DB.Model(&models.Registration{}).Where("user_id = ? AND occurrence_id = ?", u.ID, r.OccurrenceID).Count(&n)
	if n != 0 {
		t.Fatal("restored over childcare capacity with unlimited attendance")
	}
	if auditCount(t, app, audit.RegistrationNotRestored) != 1 {
		t.Fatal("missing not-restored audit")
	}
	if findKind(feed(t, loginAPI(t, app, "restoreparent", "password1234"), ""), "registration.not_restored") == nil {
		t.Fatal("missing ask-a-leader notice")
	}
}

// Deleting an account frees its seats, so the next waiter is promoted in
// the same transaction (Whittler, PR 109).
func TestAdminDeleteUserPromotesWaiter(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "DeletePromote")
	eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(1), true)
	var g models.Group
	app.DB.Where("id = ?", gid).First(&g)
	goer, cGoer := mkUser(t, app, &g, "del-goer")
	waiter, cWaiter := mkUser(t, app, &g, "del-waiter")
	for _, c := range []*apiClient{cGoer, cWaiter} {
		if code, body := registerModeAs(c, eventID, date, "going", models.AttendanceInPerson); code != http.StatusOK {
			t.Fatalf("register: %d %s", code, body)
		}
	}
	var occ models.EventOccurrence
	app.DB.Where("event_id = ?", eventID).First(&occ)
	if got := regStatus(t, app, waiter.ID, occ.ID); got != models.RegWaitlisted {
		t.Fatalf("waiter = %q, want waitlisted", got)
	}
	if w := admin.do("DELETE", "/api/v1/admin/users/"+itoa2(goer.ID), nil, true); w.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if got := regStatus(t, app, waiter.ID, occ.ID); got != models.RegGoing {
		t.Fatalf("waiter after delete = %q, want going", got)
	}
}

func TestHouseholdRestoreUsesCumulativePools(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "HouseholdRestore")
	eventID, date := seedChildcareEvent(t, app, admin, gid, intPtr(2), intPtr(1), true)
	apiDo(t, admin, "PUT", "/api/v1/events/"+itoa(eventID), map[string]any{"remoteAvailable": true, "remoteCapacity": 1}, http.StatusOK)
	holder, c, spouse, child := householdFamily(t, app, admin, "returningfamily")
	result := batchAnswer(t, c, eventID, date, map[string]any{"status": "going", "attendanceMode": "in_person", "people": []map[string]any{{"personId": spouse.ID, "status": "going", "attendanceMode": "remote"}, {"personId": child.ID, "status": "going", "childcareRequested": true}}}, http.StatusOK)
	occurrenceID := result.Registration.OccurrenceID
	apiDo(t, admin, "PUT", "/api/v1/admin/users/"+itoa(holder.ID), map[string]any{"status": "disabled"}, http.StatusOK)
	if used, err := seatsUsed(app.DB, occurrenceID); err != nil || used != 0 {
		t.Fatalf("disabled seats: %d %v", used, err)
	}
	if used, err := remoteSeatsUsed(app.DB, occurrenceID); err != nil || used != 0 {
		t.Fatalf("disabled remote seats: %d %v", used, err)
	}
	if used, err := childcareUsedFor(app.DB, occurrenceID, 0); err != nil || used != 0 {
		t.Fatalf("disabled care: %d %v", used, err)
	}
	filler := newTestUser(t, app, "filler", "password1234", models.RoleMember)
	code, raw := registerAs(loginAPI(t, app, "filler", "password1234"), eventID, date, "going", nil)
	if code != 200 {
		t.Fatal(raw)
	}
	apiDo(t, admin, "PUT", "/api/v1/admin/users/"+itoa(holder.ID), map[string]any{"status": "active"}, http.StatusOK)
	if got := regStatus(t, app, holder.ID, occurrenceID); got != models.RegGoing {
		t.Fatalf("own answer %s", got)
	}
	if got := regStatus(t, app, filler.ID, occurrenceID); got != models.RegGoing {
		t.Fatalf("filler answer %s", got)
	}
	var rows []models.Registration
	app.DB.Where("registered_by = ?", holder.ID).Find(&rows)
	if len(rows) != 1 || rows[0].HouseholdPersonID == nil || *rows[0].HouseholdPersonID != spouse.ID || rows[0].Status != models.RegGoing {
		t.Fatalf("cumulative restoration %+v", rows)
	}
	page, _ := getEvent(t, admin, eventID, http.StatusOK)
	if len(page.Occurrences[0].RemovedRegistrations) != 1 || page.Occurrences[0].RemovedRegistrations[0].HouseholdPerson == nil || page.Occurrences[0].RemovedRegistrations[0].HouseholdPerson.PersonID != child.ID {
		t.Fatalf("recovery %+v", page.Occurrences[0].RemovedRegistrations)
	}
	apiDo(t, admin, "POST", "/api/v1/events/"+itoa(eventID)+"/registrations/add", map[string]any{"date": date, "personId": child.ID, "registeredBy": holder.ID, "overCapacity": true}, http.StatusOK)
	page, _ = getEvent(t, admin, eventID, http.StatusOK)
	if len(page.Occurrences[0].RemovedRegistrations) != 0 {
		t.Fatal("restored person still appears in recovery")
	}
	if got := findKind(feed(t, loginAPI(t, app, "returningfamily", "password1234"), ""), "registration.not_restored"); got == nil || !strings.Contains(got.Title, "Kit") {
		t.Fatalf("holder notice %+v", got)
	}
}
