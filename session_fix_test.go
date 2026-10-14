package main

import (
	"net/http"
	"strings"
	"testing"

	"fold/models"
)

// API responses are per-user: never cached, never replayed by back/forward.
func TestAPIResponsesAreNoStore(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "sam", "password1234", models.RoleMember)
	anon := newAPIClient(t, app)
	if got := anon.do("GET", "/api/v1/groups", nil, false).Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("anonymous groups: Cache-Control %q", got)
	}
	sam := loginAs(t, app, "sam", "password1234")
	for _, p := range []string{"/api/v1/auth/me", "/api/v1/me/groups", "/api/v1/notifications", "/api/v1/nope"} {
		if got := sam.do("GET", p, nil, false).Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s: Cache-Control %q", p, got)
		}
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

// A member's new group gives every admin its own task, resolved when it is
// approved; the next group is a new row, so nothing earlier can hide it.
func TestMemberGroupNotifiesAdmins(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	newTestUser(t, app, "sam", "password1234", models.RoleMember)
	admin := loginAs(t, app, "admin", "password1234")
	sam := loginAs(t, app, "sam", "password1234")

	approval := func(name string) (n notificationJSON) {
		t.Helper()
		for _, x := range feed(t, admin, "") {
			if x.Kind == "group.pending" && strings.HasSuffix(x.Title, ": "+name) && x.Link == "/admin?tab=approvals" {
				return x
			}
		}
		return
	}
	create := func(name string) (g groupSummaryJSON) {
		t.Helper()
		var body struct {
			Group groupSummaryJSON `json:"group"`
		}
		w := sam.do("POST", "/api/v1/groups", map[string]any{"name": name}, true)
		if w.Code != http.StatusCreated {
			t.Fatalf("create: %d %s", w.Code, w.Body.String())
		}
		decodeJSON(t, w, &body)
		return body.Group
	}

	if ns := feed(t, admin, ""); len(ns) != 0 {
		t.Fatalf("notice before any group: %+v", ns)
	}
	first := create("First")
	if first.Status != models.GroupPending {
		t.Fatalf("member group status %q", first.Status)
	}
	n1 := approval("First")
	if n1.ID == 0 || !n1.Waiting || n1.Title != "sam started a group: First" {
		t.Fatalf("first notice: %+v", n1)
	}
	if w := admin.do("POST", "/api/v1/admin/groups/"+itoa(first.ID)+"/approve", map[string]any{}, true); w.Code != http.StatusOK {
		t.Fatalf("approve: %d", w.Code)
	}
	if n := approval("First"); n.Waiting || n.Resolution == nil || n.Resolution.Outcome != "approved" || !n.Resolution.ByMe {
		t.Fatalf("first after approve: %+v", n)
	}
	create("Second")
	n2 := approval("Second")
	if !n2.Waiting || n2.ID == n1.ID {
		t.Fatalf("second notice: %+v vs %+v", n2, n1)
	}
}
