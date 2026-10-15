package main

import (
	"fold/internal/audit"
	"fold/models"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
