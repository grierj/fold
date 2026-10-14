package main

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"fold/internal/audit"
	"fold/internal/policy"
	"fold/models"
)

func addMember(t *testing.T, app *App, u *models.User, g *models.Group, role string) {
	t.Helper()
	if err := app.DB.Create(&models.Membership{UserID: u.ID, GroupID: g.ID, Role: role, Status: "active"}).Error; err != nil {
		t.Fatal(err)
	}
}

func roleChangeRows(t *testing.T, app *App) []models.AuditLog {
	t.Helper()
	var rows []models.AuditLog
	if err := app.DB.Where("action = ?", audit.RoleChanged).Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

func notificationTitles(t *testing.T, c *apiClient) []string {
	t.Helper()
	var out []string
	for _, n := range feed(t, c, "") {
		kind := n.Kind
		if kind == "group.leadership_changed" {
			kind = "leadership"
		}
		out = append(out, kind+": "+n.Title)
	}
	return out
}

// M0.2 acceptance: several leaders, the last leader can't be removed,
// making or removing a leader writes an audit row and shows admins a notice.
func TestLeadershipChangesAreAuditedAndNotified(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	ann := newTestUser(t, app, "ann", "password1234", models.RoleMember)
	bob := newTestUser(t, app, "bob", "password1234", models.RoleMember)
	g := newTestGroup(t, app, ann, "Band", models.GroupActive)
	addMember(t, app, bob, g, models.MembershipMember)
	gid := itoa(g.ID)
	rolePath := func(u *models.User) string { return "/api/v1/groups/" + gid + "/members/" + itoa(u.ID) + "/role" }

	annC := loginAPI(t, app, "ann", "password1234")
	if w := annC.do("PUT", rolePath(bob), map[string]any{"role": "leader"}, true); w.Code != http.StatusOK {
		t.Fatalf("promote bob: %d %s", w.Code, w.Body.String())
	}
	rows := roleChangeRows(t, app)
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(rows))
	}
	r := rows[0]
	var meta map[string]any
	_ = json.Unmarshal([]byte(r.MetaJSON), &meta)
	if r.ActorID == nil || *r.ActorID != ann.ID || r.TargetType != "group" || r.TargetID != g.ID ||
		meta["userId"] != float64(bob.ID) || meta["oldRole"] != "member" || meta["newRole"] != "leader" || r.IP == "" {
		t.Fatalf("audit row = %+v meta=%v", r, meta)
	}

	// Both now lead: bob can manage.
	bobC := loginAPI(t, app, "bob", "password1234")
	var detail struct {
		CanManage bool `json:"canManage"`
	}
	decodeJSON(t, bobC.do("GET", "/api/v1/groups/"+gid, nil, false), &detail)
	if !detail.CanManage {
		t.Fatal("co-leader can't manage")
	}

	adminC := loginAPI(t, app, "admin", "password1234")
	if got := notificationTitles(t, adminC); !slices.Contains(got, "leadership: ann made bob a leader") {
		t.Fatalf("admin notices = %v", got)
	}
	if got := notificationTitles(t, annC); slices.ContainsFunc(got, func(s string) bool { return strings.HasPrefix(s, "leadership:") }) {
		t.Fatalf("non-admins must not get leadership notices: %v", got)
	}

	// Bob steps ann down; then nobody can remove bob, the last leader.
	if w := bobC.do("PUT", rolePath(ann), map[string]any{"role": "member"}, true); w.Code != http.StatusOK {
		t.Fatalf("demote ann: %d %s", w.Code, w.Body.String())
	}
	var after models.Group
	app.DB.First(&after, g.ID)
	if after.LeaderID != bob.ID {
		t.Fatalf("primary contact = %d, want bob (%d)", after.LeaderID, bob.ID)
	}
	if w := bobC.do("PUT", rolePath(bob), map[string]any{"role": "member"}, true); w.Code != http.StatusBadRequest {
		t.Fatalf("last leader demotes self: %d", w.Code)
	}
	if w := bobC.do("POST", "/api/v1/groups/"+gid+"/leave", map[string]any{}, true); w.Code != http.StatusBadRequest {
		t.Fatalf("last leader leaves: %d", w.Code)
	}
	if w := adminC.do("DELETE", "/api/v1/admin/groups/"+gid+"/members/"+itoa(bob.ID), nil, true); w.Code != http.StatusBadRequest {
		t.Fatalf("admin removes last leader: %d", w.Code)
	}
	// Ann (a member now) can't change roles.
	if w := annC.do("PUT", rolePath(ann), map[string]any{"role": "leader"}, true); w.Code != http.StatusForbidden {
		t.Fatalf("member promotes self: %d", w.Code)
	}
	if n := len(roleChangeRows(t, app)); n != 2 {
		t.Fatalf("audit rows = %d, want 2 (refused changes write nothing)", n)
	}
	if got := notificationTitles(t, adminC); !slices.Contains(got, "leadership: bob removed ann as a leader") {
		t.Fatalf("admin notices = %v", got)
	}

	// An admin adding a leader is audited too.
	cat := newTestUser(t, app, "cat", "password1234", models.RoleMember)
	if w := adminC.do("POST", "/api/v1/admin/groups/"+gid+"/members", map[string]any{"userId": cat.ID, "role": "leader"}, true); w.Code != http.StatusOK {
		t.Fatalf("admin adds leader: %d %s", w.Code, w.Body.String())
	}
	rows = roleChangeRows(t, app)
	if len(rows) != 3 || !strings.Contains(rows[2].MetaJSON, `"oldRole": "none"`) && !strings.Contains(rows[2].MetaJSON, `"oldRole":"none"`) {
		t.Fatalf("admin add row: %+v", rows)
	}
}

// Invite-only groups are 404 to outsiders on every route, including the
// ones that manage the group, and the API speaks the three-value enum.
func TestInviteOnlyIs404AndVisibilityEnum(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	ann := newTestUser(t, app, "ann", "password1234", models.RoleMember)
	newTestUser(t, app, "sam", "password1234", models.RoleMember)
	g := newTestGroup(t, app, ann, "Recovery", models.GroupActive)
	app.DB.Model(g).Update("visibility", models.VisibilityInviteOnly)
	gid := itoa(g.ID)

	sam := loginAPI(t, app, "sam", "password1234")
	for _, req := range []struct{ method, path string }{
		{"GET", "/api/v1/groups/" + gid},
		{"GET", "/api/v1/groups/" + gid + "/members"},
		{"GET", "/api/v1/groups/" + gid + "/events"},
		{"PUT", "/api/v1/groups/" + gid},
		{"POST", "/api/v1/groups/" + gid + "/join"},
		{"POST", "/api/v1/groups/" + gid + "/members"},
		{"POST", "/api/v1/groups/" + gid + "/events"},
		{"PUT", "/api/v1/groups/" + gid + "/members/" + itoa(ann.ID) + "/role"},
		{"DELETE", "/api/v1/groups/" + gid + "/cover"},
	} {
		if w := sam.do(req.method, req.path, map[string]any{}, true); w.Code != http.StatusNotFound {
			t.Errorf("%s %s as outsider = %d, want 404", req.method, req.path, w.Code)
		}
	}

	annC := loginAPI(t, app, "ann", "password1234")
	w := annC.do("GET", "/api/v1/groups/"+gid, nil, false)
	if strings.Contains(w.Body.String(), "inviteOnly") || !strings.Contains(w.Body.String(), `"visibility":"invite_only"`) {
		t.Fatalf("group JSON: %s", w.Body.String())
	}
	for _, v := range []string{"public", "private", "invite_only"} {
		if w := annC.do("PUT", "/api/v1/groups/"+gid, map[string]any{"visibility": v}, true); w.Code != http.StatusOK {
			t.Fatalf("set %s: %d %s", v, w.Code, w.Body.String())
		}
	}
	// The database refuses anything else even if a handler slipped.
	if err := app.DB.Model(g).Update("visibility", "secret").Error; err == nil {
		t.Fatal("CHECK constraint missing on groups.visibility")
	}
}

// The directory SQL must agree with policy.CanSeeGroup.
func TestDirectoryMatchesPolicy(t *testing.T) {
	app := testApp(t)
	admin := newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	ann := newTestUser(t, app, "ann", "password1234", models.RoleMember)
	sam := newTestUser(t, app, "sam", "password1234", models.RoleMember)
	var groups []*models.Group
	for _, st := range []string{models.GroupActive, models.GroupPending, models.GroupArchived} {
		for _, v := range []string{models.VisibilityPublic, models.VisibilityPrivate, models.VisibilityInviteOnly} {
			g := newTestGroup(t, app, ann, st+"-"+v, st)
			app.DB.Model(g).Update("visibility", v)
			g.Visibility = v
			groups = append(groups, g)
		}
	}
	// sam belongs to the active invite-only group only.
	addMember(t, app, sam, groups[2], models.MembershipMember)

	for _, u := range []*models.User{nil, admin, ann, sam} {
		var got []string
		var listed []models.Group
		app.directoryQuery(u).Order("name").Find(&listed)
		for _, g := range listed {
			got = append(got, g.Name)
		}
		var want []string
		for _, g := range groups {
			if g.Status == models.GroupActive && policy.CanSeeGroup(asPerson(u), asGroup(g), app.relOf(u, g.ID)) {
				want = append(want, g.Name)
			}
		}
		slices.Sort(want)
		if !slices.Equal(got, want) {
			name := "guest"
			if u != nil {
				name = u.Name
			}
			t.Errorf("%s: directory %v, policy %v", name, got, want)
		}
	}
}

// 10.2: only people in the group (and admins) see its roster.
func TestRosterIsForMembers(t *testing.T) {
	app := testApp(t)
	ann := newTestUser(t, app, "ann", "password1234", models.RoleMember)
	newTestUser(t, app, "sam", "password1234", models.RoleMember)
	g := newTestGroup(t, app, ann, "Choir", models.GroupActive)
	gid := itoa(g.ID)

	sam := loginAPI(t, app, "sam", "password1234")
	if w := sam.do("GET", "/api/v1/groups/"+gid+"/members", nil, false); w.Code != http.StatusForbidden {
		t.Fatalf("outsider roster: %d", w.Code)
	}
	var detail struct {
		Members []memberJSON `json:"members"`
	}
	decodeJSON(t, sam.do("GET", "/api/v1/groups/"+gid, nil, false), &detail)
	if len(detail.Members) != 0 {
		t.Fatalf("outsider sees members in detail: %v", detail.Members)
	}
	if w := sam.do("POST", "/api/v1/groups/"+gid+"/join", map[string]any{}, true); w.Code != http.StatusOK {
		t.Fatalf("join: %d", w.Code)
	}
	if w := sam.do("GET", "/api/v1/groups/"+gid+"/members", nil, false); w.Code != http.StatusOK {
		t.Fatalf("member roster: %d", w.Code)
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
