package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"fold/internal/audit"
	"fold/models"
)

// M0.3b: leaders remove members (DEC-35), sole-leader handoff (DEC-36),
// security headers and dependency scanning (DEC-37).

type auditMeta struct {
	UserID          uint   `json:"userId"`
	OldRole         string `json:"oldRole"`
	NewRole         string `json:"newRole"`
	Note            string `json:"note"`
	Reason          string `json:"reason"`
	DepartingUserID uint   `json:"departingUserId"`
	Status          string `json:"status"`
	Limit           int    `json:"limit"`
}

func auditRows(t *testing.T, app *App, action string) ([]models.AuditLog, []auditMeta) {
	t.Helper()
	var rows []models.AuditLog
	if err := app.DB.Where("action = ?", action).Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	metas := make([]auditMeta, len(rows))
	for i, r := range rows {
		if err := json.Unmarshal([]byte(r.MetaJSON), &metas[i]); err != nil {
			t.Fatal(err)
		}
	}
	return rows, metas
}

func membershipStatus(app *App, u *models.User, g *models.Group) string {
	var m models.Membership
	if app.DB.Where("user_id = ? AND group_id = ?", u.ID, g.ID).Limit(1).Find(&m).RowsAffected == 0 {
		return "none"
	}
	return m.Status
}

func TestLeadersRemoveMembers(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	lead := newTestUser(t, app, "lead", "password1234", models.RoleMember)
	co := newTestUser(t, app, "co", "password1234", models.RoleMember)
	sam := newTestUser(t, app, "sam", "password1234", models.RoleMember)
	pat := newTestUser(t, app, "pat", "password1234", models.RoleMember)
	other := newTestUser(t, app, "other", "password1234", models.RoleMember)
	g := newTestGroup(t, app, lead, "Choir", models.GroupActive)
	app.DB.Model(g).Update("visibility", models.VisibilityInviteOnly)
	addMember(t, app, co, g, models.MembershipLeader)
	addMember(t, app, sam, g, models.MembershipMember)
	addMember(t, app, pat, g, models.MembershipMember)
	adminUser := newTestUser(t, app, "admin2", "password1234", models.RoleAdmin)
	addMember(t, app, adminUser, g, models.MembershipLeader)
	// A public group lead doesn't lead, for the 403 case.
	otherGroup := newTestGroup(t, app, other, "Other", models.GroupActive)

	leader := loginAs(t, app, "lead", "password1234")
	path := "/api/v1/groups/" + itoa(g.ID) + "/members/"

	// A member and a co-leader, each audited with actor, group, person,
	// old role and IP; the note stays in the audit row.
	if w := leader.do("DELETE", path+itoa(sam.ID), map[string]string{"note": "asked to leave the choir"}, true); w.Code != http.StatusOK {
		t.Fatalf("remove member: %d %s", w.Code, w.Body.String())
	}
	if w := leader.do("DELETE", path+itoa(co.ID), nil, true); w.Code != http.StatusOK {
		t.Fatalf("remove co-leader: %d %s", w.Code, w.Body.String())
	}
	rows, metas := auditRows(t, app, audit.MemberRemoved)
	if len(rows) != 2 {
		t.Fatalf("want 2 removal rows, got %d", len(rows))
	}
	if *rows[0].ActorID != lead.ID || rows[0].TargetID != g.ID || rows[0].IP == "" ||
		metas[0].UserID != sam.ID || metas[0].OldRole != "member" || metas[0].Note != "asked to leave the choir" {
		t.Fatalf("member removal row: %+v %+v", rows[0], metas[0])
	}
	if metas[1].UserID != co.ID || metas[1].OldRole != "leader" {
		t.Fatalf("co-leader removal row: %+v", metas[1])
	}
	if membershipStatus(app, sam, g) != memberRemoved {
		t.Fatalf("sam's membership: %s", membershipStatus(app, sam, g))
	}

	// A leader can't remove or demote an admin, or remove themselves.
	if w := leader.do("DELETE", path+itoa(adminUser.ID), nil, true); w.Code != http.StatusBadRequest {
		t.Fatalf("remove admin: got %d, want 400", w.Code)
	}
	if w := leader.do("PUT", path+itoa(adminUser.ID)+"/role", map[string]string{"role": "member"}, true); w.Code != http.StatusBadRequest {
		t.Fatalf("demote admin: got %d, want 400", w.Code)
	}
	if w := leader.do("DELETE", path+itoa(lead.ID), nil, true); w.Code != http.StatusBadRequest {
		t.Fatalf("remove self: got %d, want 400", w.Code)
	}
	// Nobody removes the last leader: the admin removes lead after the
	// admin co-leader steps down.
	admin := loginAs(t, app, "admin", "password1234")
	if w := admin.do("PUT", path+itoa(adminUser.ID)+"/role", map[string]string{"role": "member"}, true); w.Code != http.StatusOK {
		t.Fatalf("admin demotes admin co-leader: %d %s", w.Code, w.Body.String())
	}
	if w := admin.do("DELETE", path+itoa(lead.ID), nil, true); w.Code != http.StatusBadRequest {
		t.Fatalf("remove last leader: got %d, want 400", w.Code)
	}
	if w := admin.do("DELETE", "/api/v1/admin/groups/"+itoa(g.ID)+"/members/"+itoa(lead.ID), nil, true); w.Code != http.StatusBadRequest {
		t.Fatalf("admin route, last leader: got %d, want 400", w.Code)
	}

	// 404 for a group the caller can't see, 403 for a visible one they don't lead.
	outsider := loginAs(t, app, "other", "password1234")
	if w := outsider.do("DELETE", path+itoa(pat.ID), nil, true); w.Code != http.StatusNotFound {
		t.Fatalf("invisible group: got %d, want 404", w.Code)
	}
	if w := leader.do("DELETE", "/api/v1/groups/"+itoa(otherGroup.ID)+"/members/"+itoa(other.ID), nil, true); w.Code != http.StatusForbidden {
		t.Fatalf("group they don't lead: got %d, want 403", w.Code)
	}
	member := loginAs(t, app, "pat", "password1234")
	if w := member.do("DELETE", path+itoa(adminUser.ID), nil, true); w.Code != http.StatusForbidden {
		t.Fatalf("member removes: got %d, want 403", w.Code)
	}

	// The removed member: invite-only is 404 at once; in a public group they
	// can't rejoin by themselves; a leader can add them back.
	removed := loginAs(t, app, "sam", "password1234")
	if w := removed.do("GET", "/api/v1/groups/"+itoa(g.ID), nil, false); w.Code != http.StatusNotFound {
		t.Fatalf("invite-only after removal: got %d, want 404", w.Code)
	}
	app.DB.Model(g).Update("visibility", models.VisibilityPublic)
	w := removed.do("POST", "/api/v1/groups/"+itoa(g.ID)+"/join", nil, true)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "removed you") {
		t.Fatalf("rejoin: %d %s", w.Code, w.Body.String())
	}
	for _, p := range []string{"/api/v1/auth/me", "/api/v1/me/groups", "/api/v1/groups/" + itoa(g.ID), "/api/v1/notifications"} {
		if body := removed.do("GET", p, nil, false).Body.String(); strings.Contains(body, "asked to leave") {
			t.Fatalf("%s shows the removal note: %s", p, body)
		}
	}
	if w := leader.do("POST", "/api/v1/groups/"+itoa(g.ID)+"/members", map[string]string{"email": "sam@example.com"}, true); w.Code != http.StatusOK {
		t.Fatalf("add back: %d %s", w.Code, w.Body.String())
	}
	if membershipStatus(app, sam, g) != "active" {
		t.Fatalf("after add back: %s", membershipStatus(app, sam, g))
	}

	// Admins hear about the removed leader, not the removed member.
	titles := strings.Join(notificationTitles(t, admin), "\n")
	if !strings.Contains(titles, "removed co (a leader)") || strings.Contains(titles, "removed sam") {
		t.Fatalf("admin notices:\n%s", titles)
	}
}

func TestSoleLeaderHandoff(t *testing.T) {
	for _, action := range []string{"delete", "disable"} {
		t.Run(action, func(t *testing.T) {
			app := testApp(t)
			me := newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
			other := newTestUser(t, app, "admin2", "password1234", models.RoleAdmin)
			leaving := newTestUser(t, app, "leaving", "password1234", models.RoleMember)
			ann := newTestUser(t, app, "ann", "password1234", models.RoleMember)
			stranger := newTestUser(t, app, "stranger", "password1234", models.RoleMember)
			g1 := newTestGroup(t, app, leaving, "Alpha", models.GroupActive)
			g2 := newTestGroup(t, app, leaving, "Beta", models.GroupPending)
			shared := newTestGroup(t, app, leaving, "Gamma", models.GroupActive)
			addMember(t, app, ann, g1, models.MembershipMember)
			addMember(t, app, ann, shared, models.MembershipLeader) // not sole-led
			admin := loginAs(t, app, "admin", "password1234")

			send := func(successors map[string]uint) *httptest.ResponseRecorder {
				body := map[string]any{"successors": successors}
				if action == "delete" {
					return admin.do("DELETE", "/api/v1/admin/users/"+itoa(leaving.ID), body, true)
				}
				body["status"] = "disabled"
				return admin.do("PUT", "/api/v1/admin/users/"+itoa(leaving.ID), body, true)
			}

			// Both sole-led groups are listed, with candidates.
			var list struct {
				Groups []soleLedGroupJSON `json:"groups"`
			}
			decodeJSON(t, admin.do("GET", "/api/v1/admin/users/"+itoa(leaving.ID)+"/sole-leader-groups", nil, false), &list)
			if len(list.Groups) != 2 || list.Groups[0].Name != "Alpha" || list.Groups[1].Name != "Beta" {
				t.Fatalf("sole-led groups: %+v", list.Groups)
			}
			ids := func(cs []successorJSON) map[uint]bool {
				m := map[uint]bool{}
				for _, c := range cs {
					m[c.ID] = true
				}
				return m
			}
			if c := ids(list.Groups[0].Candidates); !c[ann.ID] || !c[me.ID] || !c[other.ID] || c[stranger.ID] || c[leaving.ID] {
				t.Fatalf("Alpha candidates: %+v", list.Groups[0].Candidates)
			}

			// Refused without successors, listing both groups.
			w := send(nil)
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Alpha") || !strings.Contains(w.Body.String(), "Beta") {
				t.Fatalf("no successors: %d %s", w.Code, w.Body.String())
			}
			// An outsider who isn't an admin, or the person leaving, can't take over.
			for _, bad := range []uint{stranger.ID, leaving.ID} {
				if w := send(map[string]uint{itoa(g1.ID): bad, itoa(g2.ID): me.ID}); w.Code != http.StatusBadRequest {
					t.Fatalf("successor %d: got %d, want 400", bad, w.Code)
				}
			}
			var u models.User
			app.DB.First(&u, leaving.ID)
			if u.Status != models.UserActive {
				t.Fatalf("refused request changed the account: %s", u.Status)
			}

			// A member of Alpha, and the deleting admin (not a member) for Beta.
			if w := send(map[string]uint{itoa(g1.ID): ann.ID, itoa(g2.ID): me.ID}); w.Code != http.StatusOK {
				t.Fatalf("%s with successors: %d %s", action, w.Code, w.Body.String())
			}
			for g, want := range map[*models.Group]*models.User{g1: ann, g2: me, shared: ann} {
				var fresh models.Group
				app.DB.First(&fresh, g.ID)
				if !relIn(app.DB, want.ID, g.ID).Leader || fresh.LeaderID != want.ID || leaderCount(app.DB, g.ID) != 1 {
					t.Fatalf("%s: leader %d, contact %d, leaders %d", g.Name, want.ID, fresh.LeaderID, leaderCount(app.DB, g.ID))
				}
			}
			// Deletion ends memberships; disabling keeps them as plain member (DEC-38).
			want := map[string]string{"delete": "none", "disable": "active"}[action]
			for _, g := range []*models.Group{g1, g2, shared} {
				if got := membershipStatus(app, leaving, g); got != want || relIn(app.DB, leaving.ID, g.ID).Leader {
					t.Fatalf("%s: membership %s (leader %v), want %s and not leader", g.Name, got, relIn(app.DB, leaving.ID, g.ID).Leader, want)
				}
			}
			allRows, allMetas := auditRows(t, app, audit.RoleChanged)
			var rows []models.AuditLog
			var metas []auditMeta
			demotions := 0
			for i := range allRows {
				if allMetas[i].NewRole == "leader" {
					rows, metas = append(rows, allRows[i]), append(metas, allMetas[i])
				} else if allMetas[i].UserID == leaving.ID {
					demotions++
				}
			}
			if want := map[string]int{"delete": 0, "disable": 3}[action]; demotions != want {
				t.Fatalf("demotion rows: %d, want %d", demotions, want)
			}
			reason := "account_" + map[string]string{"delete": "deleted", "disable": "disabled"}[action]
			if len(rows) != 2 || metas[0].Reason != reason || metas[0].DepartingUserID != leaving.ID ||
				metas[0].NewRole != "leader" || *rows[0].ActorID != me.ID {
				t.Fatalf("handoff rows: %+v", metas)
			}
			userAction := map[string]string{"delete": audit.UserDeleted, "disable": audit.UserDisabled}[action]
			if rows, _ := auditRows(t, app, userAction); len(rows) != 1 || rows[0].TargetID != leaving.ID {
				t.Fatalf("%s rows: %+v", userAction, rows)
			}
		})
	}
}

func TestSecurityHeaders(t *testing.T) {
	for _, secure := range []bool{false, true} {
		app := testApp(t)
		app.Config.CookieSecure = secure
		r := app.setupRouter()
		for _, path := range []string{"/api/v1/auth/csrf", "/", "/groups/1", "/setup"} {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
			h := w.Header()
			csp := h.Get("Content-Security-Policy")
			if !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "frame-ancestors 'self'") ||
				!strings.Contains(csp, "object-src 'none'") || strings.Contains(csp, "unsafe") {
				t.Fatalf("%s CSP: %q", path, csp)
			}
			for k, v := range map[string]string{
				"X-Content-Type-Options": "nosniff",
				"Referrer-Policy":        "same-origin",
				"X-Frame-Options":        "SAMEORIGIN",
				"Permissions-Policy":     "camera=(), microphone=(), geolocation=()",
			} {
				if h.Get(k) != v {
					t.Fatalf("%s %s: %q", path, k, h.Get(k))
				}
			}
			if hsts := h.Get("Strict-Transport-Security"); (hsts != "") != secure {
				t.Fatalf("%s HSTS with secure=%v: %q", path, secure, hsts)
			}
		}
	}
}

func TestDependabotCoversEveryEcosystem(t *testing.T) {
	b, err := os.ReadFile(".github/dependabot.yml")
	if err != nil {
		t.Fatal(err)
	}
	for _, eco := range []string{"gomod", "npm", "github-actions", "docker"} {
		if !strings.Contains(string(b), "package-ecosystem: "+eco) {
			t.Errorf("dependabot.yml has no %s entry", eco)
		}
	}
	ci, err := os.ReadFile(".github/workflows/test.yml")
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"govulncheck@v", "npm audit --omit=dev --audit-level=high"} {
		if !strings.Contains(string(ci), step) {
			t.Errorf("CI has no %q step", step)
		}
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

// DEC-38: rosters flag church admins, so leaders see the label and no
// Remove button; the server still refuses (TestLeadersRemoveMembers).
func TestRosterFlagsAdmins(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	lead := newTestUser(t, app, "lead", "password1234", models.RoleMember)
	adminUser := newTestUser(t, app, "admin2", "password1234", models.RoleAdmin)
	sam := newTestUser(t, app, "sam", "password1234", models.RoleMember)
	g := newTestGroup(t, app, lead, "G", models.GroupActive)
	addMember(t, app, adminUser, g, models.MembershipMember)
	addMember(t, app, sam, g, models.MembershipMember)
	for _, path := range []string{"/api/v1/groups/" + itoa(g.ID), "/api/v1/groups/" + itoa(g.ID) + "/members"} {
		var body struct {
			Members []memberJSON `json:"members"`
		}
		decodeJSON(t, loginAs(t, app, "lead", "password1234").do("GET", path, nil, false), &body)
		flags := map[uint]bool{}
		for _, m := range body.Members {
			flags[m.ID] = m.Admin
		}
		if len(flags) != 3 || !flags[adminUser.ID] || flags[sam.ID] || flags[lead.ID] {
			t.Fatalf("%s admin flags: %+v", path, body.Members)
		}
	}
}
