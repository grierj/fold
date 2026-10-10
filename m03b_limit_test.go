package main

import (
	"net/http"
	"strings"
	"sync"
	"testing"

	"fold/internal/audit"
	"fold/models"
)

// DEC-39: an optional group member limit.

func setLimit(t *testing.T, c *apiClient, g *models.Group, limit any) {
	t.Helper()
	if w := c.do("PUT", "/api/v1/groups/"+itoa(g.ID), map[string]any{"memberLimit": limit}, true); w.Code != http.StatusOK {
		t.Fatalf("set limit %v: %d %s", limit, w.Code, w.Body.String())
	}
}

func activeCount(app *App, g *models.Group) int64 { return memberCount(app.DB, g.ID) }

func TestGroupMemberLimitJoinOverrideAndLowering(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	lead := newTestUser(t, app, "lead", "password1234", models.RoleMember)
	ann := newTestUser(t, app, "ann", "password1234", models.RoleMember)
	for _, n := range []string{"bo", "cy", "di", "ed"} {
		newTestUser(t, app, n, "password1234", models.RoleMember)
	}
	g := newTestGroup(t, app, lead, "Class", models.GroupActive)
	leader := loginAs(t, app, "lead", "password1234")
	admin := loginAs(t, app, "admin", "password1234")
	path := "/api/v1/groups/" + itoa(g.ID)

	// Set by the leader; must be a positive whole number or null.
	for _, bad := range []any{0, -3, 1.5, "two"} {
		if w := leader.do("PUT", path, map[string]any{"memberLimit": bad}, true); w.Code != http.StatusBadRequest {
			t.Fatalf("limit %v: %d", bad, w.Code)
		}
	}
	setLimit(t, leader, g, 2)
	var d struct {
		Group groupSummaryJSON `json:"group"`
	}
	decodeJSON(t, leader.do("GET", path, nil, false), &d)
	if d.Group.MemberLimit == nil || *d.Group.MemberLimit != 2 || d.Group.MemberCount != 1 {
		t.Fatalf("summary: %+v", d.Group)
	}

	join := func(name string) int {
		return loginAs(t, app, name, "password1234").do("POST", path+"/join", nil, true).Code
	}
	if c := join("ann"); c != http.StatusOK {
		t.Fatalf("ann join: %d", c)
	}
	w := loginAs(t, app, "bo", "password1234").do("POST", path+"/join", nil, true)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "This group is full") {
		t.Fatalf("full join: %d %s", w.Code, w.Body.String())
	}

	// Disabled accounts don't count: with ann disabled there is room.
	app.DB.Model(ann).Update("status", models.UserDisabled)
	if activeCount(app, g) != 1 {
		t.Fatalf("disabled counted: %d", activeCount(app, g))
	}
	if c := join("bo"); c != http.StatusOK {
		t.Fatalf("bo join with ann disabled: %d", c)
	}
	app.DB.Model(ann).Update("status", models.UserActive) // now 3 of 2

	// Leader add needs the explicit override, which is audited.
	add := map[string]any{"email": testEmail("cy")}
	if w := leader.do("POST", path+"/members", add, true); w.Code != http.StatusConflict ||
		!strings.Contains(w.Body.String(), `"full":true`) {
		t.Fatalf("leader add when full: %d %s", w.Code, w.Body.String())
	}
	add["overLimit"] = true
	if w := leader.do("POST", path+"/members", add, true); w.Code != http.StatusOK {
		t.Fatalf("leader override: %d %s", w.Code, w.Body.String())
	}
	// Admin add, same rule.
	var di models.User
	app.DB.Where("email = ?", testEmail("di")).First(&di)
	adminAdd := map[string]any{"userId": di.ID, "role": "member"}
	if w := admin.do("POST", "/api/v1/admin/groups/"+itoa(g.ID)+"/members", adminAdd, true); w.Code != http.StatusConflict {
		t.Fatalf("admin add when full: %d %s", w.Code, w.Body.String())
	}
	adminAdd["overLimit"] = true
	if w := admin.do("POST", "/api/v1/admin/groups/"+itoa(g.ID)+"/members", adminAdd, true); w.Code != http.StatusOK {
		t.Fatalf("admin override: %d %s", w.Code, w.Body.String())
	}
	rows, metas := auditRows(t, app, audit.AddedOverLimit)
	if len(rows) != 2 || metas[1].UserID != di.ID || metas[0].Limit != 2 || rows[0].ActorID == nil || *rows[0].ActorID != lead.ID {
		t.Fatalf("over-limit audit: %+v %+v", rows, metas)
	}
	if activeCount(app, g) != 5 {
		t.Fatalf("count after overrides: %d", activeCount(app, g))
	}

	// Lowering below the count keeps everyone and blocks joins.
	setLimit(t, leader, g, 1)
	if activeCount(app, g) != 5 {
		t.Fatalf("lowering removed people: %d", activeCount(app, g))
	}
	if c := join("ed"); c != http.StatusConflict {
		t.Fatalf("join over lowered limit: %d", c)
	}
	setLimit(t, admin, g, nil) // null clears it
	if c := join("ed"); c != http.StatusOK {
		t.Fatalf("join with no limit: %d", c)
	}
}

func TestReenableIntoFullGroupWaitsForLeader(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	lead := newTestUser(t, app, "lead", "password1234", models.RoleMember)
	pat := newTestUser(t, app, "pat", "password1234", models.RoleMember)
	sam := newTestUser(t, app, "sam", "password1234", models.RoleMember)
	newTestUser(t, app, "newbie", "password1234", models.RoleMember)
	full := newTestGroup(t, app, lead, "Full", models.GroupActive)
	roomy := newTestGroup(t, app, lead, "Roomy", models.GroupActive)
	for _, g := range []*models.Group{full, roomy} {
		addMember(t, app, pat, g, models.MembershipMember)
		addMember(t, app, sam, g, models.MembershipMember)
	}
	admin := loginAs(t, app, "admin", "password1234")
	leader := loginAs(t, app, "lead", "password1234")
	setLimit(t, leader, full, 3)
	setLimit(t, leader, roomy, 10)

	cycle := func(u *models.User) {
		t.Helper()
		for _, s := range []string{"disabled", "active"} {
			if w := admin.do("PUT", "/api/v1/admin/users/"+itoa(u.ID), map[string]any{"status": s}, true); w.Code != http.StatusOK {
				t.Fatalf("%s %s: %d %s", s, u.Email, w.Code, w.Body.String())
			}
			if s == "disabled" && u == pat {
				// While pat is away, someone takes the seat.
				if c := loginAs(t, app, "newbie", "password1234").do("POST", "/api/v1/groups/"+itoa(full.ID)+"/join", nil, true).Code; c != http.StatusOK {
					t.Fatalf("newbie join: %d", c)
				}
			}
		}
	}
	cycle(pat)
	if membershipStatus(app, pat, full) != memberPendingReturn || membershipStatus(app, pat, roomy) != "active" {
		t.Fatalf("after enable: full %s, roomy %s", membershipStatus(app, pat, full), membershipStatus(app, pat, roomy))
	}
	_, metas := auditRows(t, app, audit.MemberRestored)
	if len(metas) != 2 || metas[0].Status != memberPendingReturn || metas[1].Status != "active" {
		t.Fatalf("restore audit (stable group order): %+v", metas)
	}
	if activeCount(app, full) != 3 {
		t.Fatalf("pending_return counted: %d", activeCount(app, full))
	}

	// The leader sees it on the roster and as a notice; admins too.
	var d struct {
		Members        []memberJSON `json:"members"`
		PendingReturns []memberJSON `json:"pendingReturns"`
	}
	decodeJSON(t, leader.do("GET", "/api/v1/groups/"+itoa(full.ID), nil, false), &d)
	if len(d.PendingReturns) != 1 || d.PendingReturns[0].ID != pat.ID {
		t.Fatalf("roster pending: %+v", d.PendingReturns)
	}
	for _, m := range d.Members {
		if m.ID == pat.ID {
			t.Fatal("pending return listed as a member")
		}
	}
	for _, c := range []*apiClient{leader, admin} {
		if b := c.do("GET", "/api/v1/notifications", nil, false).Body.String(); !strings.Contains(b, "waiting to return to Full") {
			t.Fatalf("notice missing: %s", b)
		}
	}
	var ar struct {
		Returns []pendingReturnJSON `json:"returns"`
	}
	decodeJSON(t, admin.do("GET", "/api/v1/admin/pending-returns", nil, false), &ar)
	if len(ar.Returns) != 1 || ar.Returns[0].Member.ID != pat.ID || ar.Returns[0].Group.ID != full.ID {
		t.Fatalf("admin pending: %+v", ar.Returns)
	}
	// Members can't decide, and pat can't self-join around it.
	patC := loginAs(t, app, "pat", "password1234")
	if c := patC.do("POST", "/api/v1/groups/"+itoa(full.ID)+"/join", nil, true).Code; c != http.StatusConflict {
		t.Fatalf("pending self-join: %d", c)
	}
	decide := "/api/v1/groups/" + itoa(full.ID) + "/members/"
	samC := loginAs(t, app, "sam", "password1234")
	if c := samC.do("POST", decide+itoa(pat.ID)+"/return", map[string]any{"decision": "approve"}, true).Code; c != http.StatusForbidden {
		t.Fatalf("member decided: %d", c)
	}

	// Approve: an explicit over-limit choice.
	if w := leader.do("POST", decide+itoa(pat.ID)+"/return", map[string]any{"decision": "approve"}, true); w.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", w.Code, w.Body.String())
	}
	if membershipStatus(app, pat, full) != "active" || activeCount(app, full) != 4 || relIn(app.DB, pat.ID, full.ID).Leader {
		t.Fatalf("after approve: %s %d", membershipStatus(app, pat, full), activeCount(app, full))
	}
	if rows, m := auditRows(t, app, audit.ReturnApproved); len(rows) != 1 || m[0].UserID != pat.ID || m[0].Limit != 3 {
		t.Fatalf("approve audit: %+v", m)
	}

	// Decline: sam comes back to a still-full group; an admin declines.
	cycle(sam)
	if membershipStatus(app, sam, full) != memberPendingReturn || membershipStatus(app, sam, roomy) != "active" {
		t.Fatal("sam should wait in Full and be back in Roomy")
	}
	if w := admin.do("POST", decide+itoa(sam.ID)+"/return", map[string]any{"decision": "decline"}, true); w.Code != http.StatusOK {
		t.Fatalf("decline: %d %s", w.Code, w.Body.String())
	}
	if membershipStatus(app, sam, full) != memberRemoved {
		t.Fatalf("after decline: %s", membershipStatus(app, sam, full))
	}
	if rows, m := auditRows(t, app, audit.ReturnDeclined); len(rows) != 1 || m[0].UserID != sam.ID {
		t.Fatalf("decline audit: %+v", m)
	}
	if c := leader.do("POST", decide+itoa(sam.ID)+"/return", map[string]any{"decision": "approve"}, true).Code; c != http.StatusNotFound {
		t.Fatalf("decide twice: %d", c)
	}
	if b := leader.do("GET", "/api/v1/notifications", nil, false).Body.String(); strings.Contains(b, "waiting to return") {
		t.Fatalf("stale notice: %s", b)
	}
}

// Concurrent joins and a re-enable can't push a group past its limit.
func TestConcurrentJoinsAndRestoreRespectLimit(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	lead := newTestUser(t, app, "lead", "password1234", models.RoleMember)
	pat := newTestUser(t, app, "pat", "password1234", models.RoleMember)
	g := newTestGroup(t, app, lead, "Room", models.GroupActive)
	addMember(t, app, pat, g, models.MembershipMember)
	app.DB.Model(pat).Update("status", models.UserDisabled)
	limit := 3
	app.DB.Model(g).Update("member_limit", limit)

	admin := loginAs(t, app, "admin", "password1234")
	names := []string{"j1", "j2", "j3", "j4", "j5"}
	clients := []*apiClient{}
	for _, n := range names {
		newTestUser(t, app, n, "password1234", models.RoleMember)
		clients = append(clients, loginAs(t, app, n, "password1234"))
	}
	var wg sync.WaitGroup
	codes := make([]int, len(clients))
	for i, c := range clients {
		wg.Add(1)
		go func(i int, c *apiClient) {
			defer wg.Done()
			codes[i] = c.do("POST", "/api/v1/groups/"+itoa(g.ID)+"/join", nil, true).Code
		}(i, c)
	}
	wg.Add(1)
	enabled := 0
	go func() {
		defer wg.Done()
		enabled = admin.do("PUT", "/api/v1/admin/users/"+itoa(pat.ID), map[string]any{"status": "active"}, true).Code
	}()
	wg.Wait()
	if enabled != http.StatusOK {
		t.Fatalf("enable: %d", enabled)
	}
	joined := 0
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			joined++
		case http.StatusConflict:
		default:
			t.Fatalf("join code %d", c)
		}
	}
	patBack := membershipStatus(app, pat, g) == "active"
	if n := activeCount(app, g); n != int64(limit) || joined+1+boolInt(patBack) != limit {
		t.Fatalf("count %d (limit %d), joined %d, pat %s", n, limit, joined, membershipStatus(app, pat, g))
	}
	if !patBack && membershipStatus(app, pat, g) != memberPendingReturn {
		t.Fatalf("pat: %s", membershipStatus(app, pat, g))
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
