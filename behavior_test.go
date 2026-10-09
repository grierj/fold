package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fold/models"
)

func TestMeetingInstancesAreChronological(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")

	w := admin.do("POST", "/api/v1/groups", map[string]any{"name": "Choir"}, true)
	var created struct {
		Group groupSummaryJSON `json:"group"`
	}
	decodeJSON(t, w, &created)
	gid := itoa(created.Group.ID)
	admin.do("POST", "/api/v1/admin/groups/"+gid+"/approve", map[string]any{}, true)

	now := time.Now().UTC().Truncate(time.Second)
	later := now.Add(30 * 24 * time.Hour)
	sooner := now.Add(48 * time.Hour)
	for _, start := range []time.Time{later, sooner} {
		w = admin.do("POST", "/api/v1/groups/"+gid+"/meetings", map[string]any{
			"startsAt": start.Format(time.RFC3339),
			"endsAt":   start.Add(time.Hour).Format(time.RFC3339),
		}, true)
		if w.Code != http.StatusCreated {
			t.Fatalf("create meeting: %d %s", w.Code, w.Body.String())
		}
	}

	from := now.Add(-time.Hour).Format("2006-01-02")
	to := now.Add(60 * 24 * time.Hour).Format("2006-01-02")
	w = admin.do("GET", "/api/v1/groups/"+gid+"/meetings?from="+from+"&to="+to, nil, false)
	var list struct {
		Meetings []meetingInstanceJSON `json:"meetings"`
	}
	decodeJSON(t, w, &list)
	if len(list.Meetings) != 2 {
		t.Fatalf("instances: got %d", len(list.Meetings))
	}
	if !list.Meetings[0].StartsAt.Before(list.Meetings[1].StartsAt) {
		t.Fatalf("not chronological: %s then %s", list.Meetings[0].StartsAt, list.Meetings[1].StartsAt)
	}

	w = admin.do("GET", "/api/v1/groups/"+gid, nil, false)
	var detail struct {
		Group groupSummaryJSON `json:"group"`
	}
	decodeJSON(t, w, &detail)
	if detail.Group.NextMeetingAt == nil || !detail.Group.NextMeetingAt.UTC().Equal(sooner) {
		t.Fatalf("nextMeetingAt: got %v want %s", detail.Group.NextMeetingAt, sooner)
	}
}

func TestCoverFocalUpdateKeepsUpload(t *testing.T) {
	app := testApp(t)
	leader := newTestUser(t, app, "leader", "password1234", models.RoleMember)
	g := models.Group{Name: "Band", LeaderID: leader.ID, Status: models.GroupActive}
	if err := app.DB.Create(&g).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := app.DB.Create(&models.Membership{
		UserID: leader.ID, GroupID: g.ID,
		Role: models.MembershipLeader, Status: "active", JoinedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	c := loginAPI(t, app, "leader", "password1234")
	gid := itoa(g.ID)
	w := c.doMultipart("POST", "/api/v1/groups/"+gid+"/cover", "cover", "cover.png", tinyPNG(t))
	if w.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	var uploaded struct {
		ImageURL string `json:"imageUrl"`
	}
	decodeJSON(t, w, &uploaded)

	w = c.do("PUT", "/api/v1/groups/"+gid, map[string]any{
		"coverFocal": map[string]float64{"x": 10, "y": 20},
	}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("focal: %d %s", w.Code, w.Body.String())
	}
	var got models.Group
	if err := app.DB.First(&got, g.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.ImageURL != uploaded.ImageURL || !strings.HasPrefix(got.ImageURL, "/uploads/") {
		t.Fatalf("cover URL changed: %q", got.ImageURL)
	}
	if got.CoverFocalX != 10 || got.CoverFocalY != 20 {
		t.Fatalf("focal: %v %v", got.CoverFocalX, got.CoverFocalY)
	}
}

func TestLeaderCannotLeave(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	newTestUser(t, app, "sam", "password1234", models.RoleMember)
	admin := loginAPI(t, app, "admin", "password1234")
	w := admin.do("POST", "/api/v1/groups", map[string]any{"name": "Ushers"}, true)
	var created struct {
		Group groupSummaryJSON `json:"group"`
	}
	decodeJSON(t, w, &created)
	gid := itoa(created.Group.ID)
	admin.do("POST", "/api/v1/admin/groups/"+gid+"/approve", map[string]any{}, true)

	w = admin.do("POST", "/api/v1/groups/"+gid+"/leave", map[string]any{}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("leader leave: got %d %s", w.Code, w.Body.String())
	}

	sam := loginAPI(t, app, "sam", "password1234")
	sam.do("POST", "/api/v1/groups/"+gid+"/join", map[string]any{}, true)
	w = sam.do("POST", "/api/v1/groups/"+gid+"/leave", map[string]any{}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("member leave: got %d %s", w.Code, w.Body.String())
	}
}

func TestMyGroupsIncludesPending(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "leader", "password1234", models.RoleMember)
	leader := loginAPI(t, app, "leader", "password1234")
	w := leader.do("POST", "/api/v1/groups", map[string]any{"name": "Youth"}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	w = leader.do("GET", "/api/v1/me/groups", nil, false)
	var body struct {
		Groups []groupSummaryJSON `json:"groups"`
	}
	decodeJSON(t, w, &body)
	if len(body.Groups) != 1 || body.Groups[0].Status != models.GroupPending {
		t.Fatalf("my groups: %+v", body.Groups)
	}
}

func TestPendingGroupIsHiddenFromOtherMembers(t *testing.T) {
	app := testApp(t)
	leaderUser := newTestUser(t, app, "leader", "password1234", models.RoleMember)
	newTestUser(t, app, "sam", "password1234", models.RoleMember)
	g := models.Group{Name: "Hidden", LeaderID: leaderUser.ID, Status: models.GroupPending}
	if err := app.DB.Create(&g).Error; err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 1, 18, 0, 0, 0, time.UTC)
	m := models.Meeting{GroupID: g.ID, StartsAt: start, EndsAt: start.Add(time.Hour)}
	if err := app.DB.Create(&m).Error; err != nil {
		t.Fatal(err)
	}
	gid := itoa(g.ID)
	mid := itoa(m.ID)

	sam := loginAPI(t, app, "sam", "password1234")
	if w := sam.do("GET", "/api/v1/groups/"+gid+"/members", nil, false); w.Code != http.StatusNotFound {
		t.Fatalf("members: got %d", w.Code)
	}
	w := sam.do("PUT", "/api/v1/meetings/"+mid+"/rsvp", map[string]any{
		"status": "going", "date": "2026-11-01",
	}, true)
	if w.Code != http.StatusNotFound {
		t.Fatalf("rsvp: got %d %s", w.Code, w.Body.String())
	}

	leader := loginAPI(t, app, "leader", "password1234")
	if w := leader.do("GET", "/api/v1/groups/"+gid+"/members", nil, false); w.Code != http.StatusOK {
		t.Fatalf("leader members: got %d %s", w.Code, w.Body.String())
	}
}

func TestMeetingValidation(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	w := admin.do("POST", "/api/v1/groups", map[string]any{"name": "Study"}, true)
	var created struct {
		Group groupSummaryJSON `json:"group"`
	}
	decodeJSON(t, w, &created)
	gid := itoa(created.Group.ID)
	admin.do("POST", "/api/v1/admin/groups/"+gid+"/approve", map[string]any{}, true)

	start := time.Date(2026, 10, 20, 18, 0, 0, 0, time.UTC)
	w = admin.do("POST", "/api/v1/groups/"+gid+"/meetings", map[string]any{
		"startsAt":       start.Format(time.RFC3339),
		"endsAt":         start.Add(time.Hour).Format(time.RFC3339),
		"recurrenceRule": "NOT_A_RULE",
	}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad rule: got %d %s", w.Code, w.Body.String())
	}

	w = admin.do("GET", "/api/v1/groups/"+gid+"/meetings?from=2000-01-01&to=2026-01-01", nil, false)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("wide range: got %d %s", w.Code, w.Body.String())
	}
}

func TestGroupWriteValidation(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	w := admin.do("POST", "/api/v1/groups", map[string]any{"name": "Kitchen"}, true)
	var created struct {
		Group groupSummaryJSON `json:"group"`
	}
	decodeJSON(t, w, &created)
	gid := itoa(created.Group.ID)

	w = admin.do("PUT", "/api/v1/groups/"+gid, map[string]any{"name": "   "}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("blank name: got %d %s", w.Code, w.Body.String())
	}
	w = admin.do("PUT", "/api/v1/groups/"+gid, map[string]any{"status": "nope"}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad status: got %d %s", w.Code, w.Body.String())
	}
}

func TestSearchDoesNotTreatPercentAsWildcard(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	for _, name := range []string{"Alpha", "Beta"} {
		w := admin.do("POST", "/api/v1/groups", map[string]any{"name": name}, true)
		var created struct {
			Group groupSummaryJSON `json:"group"`
		}
		decodeJSON(t, w, &created)
		admin.do("POST", "/api/v1/admin/groups/"+itoa(created.Group.ID)+"/approve", map[string]any{}, true)
	}
	w := admin.do("GET", "/api/v1/groups?search=%25", nil, false)
	var body struct {
		Groups []groupSummaryJSON `json:"groups"`
	}
	decodeJSON(t, w, &body)
	if len(body.Groups) != 0 {
		t.Fatalf("percent matched %d groups", len(body.Groups))
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
		if u.Username == "alice" {
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

func TestHealthz(t *testing.T) {
	app := testApp(t)
	w := httptest.NewRecorder()
	app.setupRouter().ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("healthz: got %d", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("healthz body: %q", w.Body.String())
	}
}

func TestCSRFFailureIsJSON(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "bob", "password1234", models.RoleMember)
	c := loginAPI(t, app, "bob", "password1234")
	w := c.do("POST", "/api/v1/groups", map[string]any{"name": "X"}, false)
	if w.Code != http.StatusForbidden {
		t.Fatalf("csrf: got %d", w.Code)
	}
	var body struct {
		Error string `json:"error"`
	}
	decodeJSON(t, w, &body)
	if body.Error == "" {
		t.Fatal("expected JSON error")
	}
}

func activeGroup(t *testing.T, app *App, leader *models.User, name, visibility, tags string, invite bool) models.Group {
	t.Helper()
	g := models.Group{
		Name: name, LeaderID: leader.ID, Status: models.GroupActive,
		Visibility: visibility, InviteOnly: invite, Tags: tags,
	}
	if err := app.DB.Create(&g).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Create(&models.Membership{
		UserID: leader.ID, GroupID: g.ID,
		Role: models.MembershipLeader, Status: "active", JoinedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	return g
}

func groupNames(t *testing.T, w *httptest.ResponseRecorder) []string {
	t.Helper()
	var body struct {
		Groups []groupSummaryJSON `json:"groups"`
	}
	decodeJSON(t, w, &body)
	names := make([]string, 0, len(body.Groups))
	for _, g := range body.Groups {
		names = append(names, g.Name)
	}
	return names
}

func containsName(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

func TestDirectoryVisibility(t *testing.T) {
	app := testApp(t)
	leaderUser := newTestUser(t, app, "leader", "password1234", models.RoleMember)
	newTestUser(t, app, "sam", "password1234", models.RoleMember)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	activeGroup(t, app, leaderUser, "Public Choir", models.VisibilityPublic, "", false)
	activeGroup(t, app, leaderUser, "Private Band", models.VisibilityPrivate, "", false)
	invite := activeGroup(t, app, leaderUser, "Invite Prayer", models.VisibilityPrivate, "", true)

	anon := newAPIClient(t, app)
	got := groupNames(t, anon.do("GET", "/api/v1/groups", nil, false))
	if !containsName(got, "Public Choir") || containsName(got, "Private Band") || containsName(got, "Invite Prayer") {
		t.Fatalf("anonymous directory: %v", got)
	}
	if w := anon.do("GET", "/api/v1/groups/"+itoa(invite.ID), nil, false); w.Code != http.StatusNotFound {
		t.Fatalf("anonymous invite detail: %d", w.Code)
	}

	sam := loginAPI(t, app, "sam", "password1234")
	got = groupNames(t, sam.do("GET", "/api/v1/groups", nil, false))
	if !containsName(got, "Public Choir") || !containsName(got, "Private Band") || containsName(got, "Invite Prayer") {
		t.Fatalf("member directory: %v", got)
	}
	if w := sam.do("POST", "/api/v1/groups/"+itoa(invite.ID)+"/join", map[string]any{}, true); w.Code != http.StatusNotFound {
		t.Fatalf("join invite: %d %s", w.Code, w.Body.String())
	}

	admin := loginAPI(t, app, "admin", "password1234")
	got = groupNames(t, admin.do("GET", "/api/v1/groups", nil, false))
	if !containsName(got, "Public Choir") || !containsName(got, "Private Band") || !containsName(got, "Invite Prayer") {
		t.Fatalf("admin directory: %v", got)
	}
	if w := admin.do("GET", "/api/v1/groups/"+itoa(invite.ID), nil, false); w.Code != http.StatusOK {
		t.Fatalf("admin open invite: %d %s", w.Code, w.Body.String())
	}

	leader := loginAPI(t, app, "leader", "password1234")
	got = groupNames(t, leader.do("GET", "/api/v1/groups", nil, false))
	if !containsName(got, "Invite Prayer") {
		t.Fatalf("leader directory: %v", got)
	}
	w := leader.do("POST", "/api/v1/groups", map[string]any{
		"name": "Nope", "visibility": "public", "inviteOnly": true,
	}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("public invite-only create: %d %s", w.Code, w.Body.String())
	}
	w = leader.do("PUT", "/api/v1/groups/"+itoa(invite.ID), map[string]any{
		"visibility": "public", "inviteOnly": true,
	}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("public invite-only update: %d %s", w.Code, w.Body.String())
	}

	w = leader.do("POST", "/api/v1/groups/"+itoa(invite.ID)+"/members", map[string]any{"username": "sam"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("add member: %d %s", w.Code, w.Body.String())
	}
	got = groupNames(t, sam.do("GET", "/api/v1/groups", nil, false))
	if !containsName(got, "Invite Prayer") {
		t.Fatalf("member of invite-only group: %v", got)
	}
}

func TestTagFiltersCombine(t *testing.T) {
	app := testApp(t)
	leaderUser := newTestUser(t, app, "leader", "password1234", models.RoleMember)
	activeGroup(t, app, leaderUser, "Youth Choir", models.VisibilityPublic, "choir,youth", false)
	activeGroup(t, app, leaderUser, "Youth Band", models.VisibilityPublic, "band,youth", false)
	activeGroup(t, app, leaderUser, "Sunday Choir", models.VisibilityPublic, "choir", false)

	anon := newAPIClient(t, app)
	got := groupNames(t, anon.do("GET", "/api/v1/groups?tag=choir&tag=youth", nil, false))
	if len(got) != 1 || got[0] != "Youth Choir" {
		t.Fatalf("choir AND youth: %v", got)
	}
	got = groupNames(t, anon.do("GET", "/api/v1/groups?tag=choir", nil, false))
	if len(got) != 2 || !containsName(got, "Youth Choir") || !containsName(got, "Sunday Choir") {
		t.Fatalf("choir: %v", got)
	}
	got = groupNames(t, anon.do("GET", "/api/v1/groups?tag=choir&tag=band", nil, false))
	if len(got) != 0 {
		t.Fatalf("choir AND band: %v", got)
	}
}

func TestLeadershipAndDeletion(t *testing.T) {
	app := testApp(t)
	leaderUser := newTestUser(t, app, "leader", "password1234", models.RoleMember)
	samUser := newTestUser(t, app, "sam", "password1234", models.RoleMember)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	leader := loginAPI(t, app, "leader", "password1234")
	admin := loginAPI(t, app, "admin", "password1234")

	w := leader.do("POST", "/api/v1/groups", map[string]any{"name": "Solo"}, true)
	var created struct {
		Group groupSummaryJSON `json:"group"`
	}
	decodeJSON(t, w, &created)
	gid := itoa(created.Group.ID)
	admin.do("POST", "/api/v1/admin/groups/"+gid+"/approve", map[string]any{}, true)

	sam := loginAPI(t, app, "sam", "password1234")
	if w := sam.do("POST", "/api/v1/groups/"+gid+"/deletion-request", map[string]any{}, true); w.Code != http.StatusForbidden {
		t.Fatalf("non-leader delete request: %d", w.Code)
	}
	w = leader.do("POST", "/api/v1/groups/"+gid+"/deletion-request", map[string]any{}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("request deletion: %d %s", w.Code, w.Body.String())
	}

	w = admin.do("GET", "/api/v1/admin/deletions", nil, false)
	if !containsName(groupNames(t, w), "Solo") {
		t.Fatalf("deletion queue: %s", w.Body.String())
	}
	w = admin.do("GET", "/api/v1/notifications", nil, false)
	if !strings.Contains(w.Body.String(), "waiting to be deleted") {
		t.Fatalf("notification: %s", w.Body.String())
	}
	if w := admin.do("POST", "/api/v1/admin/groups/"+gid+"/reject-deletion", map[string]any{}, true); w.Code != http.StatusOK {
		t.Fatalf("reject deletion: %d %s", w.Code, w.Body.String())
	}
	var still models.Group
	if err := app.DB.First(&still, created.Group.ID).Error; err != nil || still.DeletionRequested {
		t.Fatalf("group after reject: err=%v requested=%v", err, still.DeletionRequested)
	}

	if w := leader.do("POST", "/api/v1/groups/"+gid+"/members", map[string]any{"username": "sam"}, true); w.Code != http.StatusOK {
		t.Fatalf("add sam: %d %s", w.Code, w.Body.String())
	}
	if w := leader.do("POST", "/api/v1/groups/"+gid+"/deletion-request", map[string]any{}, true); w.Code != http.StatusBadRequest {
		t.Fatalf("delete with a member: %d %s", w.Code, w.Body.String())
	}
	w = leader.do("POST", "/api/v1/groups/"+gid+"/leader", map[string]any{"userId": samUser.ID}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("transfer: %d %s", w.Code, w.Body.String())
	}
	var detail struct {
		Group groupSummaryJSON `json:"group"`
	}
	w = leader.do("GET", "/api/v1/groups/"+gid, nil, false)
	decodeJSON(t, w, &detail)
	if detail.Group.LeaderID != samUser.ID {
		t.Fatalf("leader id: %d", detail.Group.LeaderID)
	}
	if w := leader.do("POST", "/api/v1/groups/"+gid+"/leave", map[string]any{}, true); w.Code != http.StatusOK {
		t.Fatalf("old leader leave: %d %s", w.Code, w.Body.String())
	}
	if w := sam.do("POST", "/api/v1/groups/"+gid+"/deletion-request", map[string]any{}, true); w.Code != http.StatusOK {
		t.Fatalf("new leader request: %d %s", w.Code, w.Body.String())
	}

	busy := activeGroup(t, app, leaderUser, "Busy", models.VisibilityPublic, "", false)
	if err := app.DB.Create(&models.Membership{
		UserID: samUser.ID, GroupID: busy.ID,
		Role: models.MembershipMember, Status: "active", JoinedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if w := admin.do("DELETE", "/api/v1/admin/groups/"+itoa(busy.ID), nil, true); w.Code != http.StatusOK {
		t.Fatalf("admin delete: %d %s", w.Code, w.Body.String())
	}
	if w := admin.do("GET", "/api/v1/groups/"+itoa(busy.ID), nil, false); w.Code != http.StatusNotFound {
		t.Fatalf("deleted group: %d", w.Code)
	}
	if w := admin.do("DELETE", "/api/v1/admin/groups/"+gid, nil, true); w.Code != http.StatusOK {
		t.Fatalf("admin delete requested group: %d %s", w.Code, w.Body.String())
	}
}

func TestFetchAvatar(t *testing.T) {
	png := tinyPNG(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(png)
	}))
	defer srv.Close()

	app := testApp(t)
	newTestUser(t, app, "ada", "password1234", models.RoleMember)
	c := loginAPI(t, app, "ada", "password1234")
	w := c.do("POST", "/api/v1/me/avatar/url", map[string]string{"url": srv.URL + "/pic.png"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("fetch: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		AvatarURL string `json:"avatarUrl"`
	}
	decodeJSON(t, w, &body)
	if !strings.HasPrefix(body.AvatarURL, "/uploads/avatars/") {
		t.Fatalf("avatar url: %q", body.AvatarURL)
	}

	w = c.do("POST", "/api/v1/me/avatar/url", map[string]string{"url": "http://10.1.2.3/x.png"}, true)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "not allowed") {
		t.Fatalf("private address: %d %s", w.Code, w.Body.String())
	}
	w = c.do("POST", "/api/v1/me/avatar/url", map[string]string{"url": "file:///etc/passwd"}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("file url: %d %s", w.Code, w.Body.String())
	}

	app.Config.Env = "prod"
	w = c.do("POST", "/api/v1/me/avatar/url", map[string]string{"url": srv.URL + "/pic.png"}, true)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "not allowed") {
		t.Fatalf("prod loopback: %d %s", w.Code, w.Body.String())
	}
}
