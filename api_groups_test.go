package main

import (
	"encoding/json"
	"fmt"
	"fold/internal/audit"
	"fold/internal/policy"
	"fold/models"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestGroupApprovalFlow(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	newTestUser(t, app, "leader", "pw", models.RoleMember)

	leader := loginAPI(t, app, "leader", "pw")

	// Create → pending, creator becomes leader-member.
	w := leader.do("POST", "/api/v1/groups", map[string]any{
		"name": "Hiking", "description": "trails", "tags": []string{"outdoors"},
	}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create group: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Group groupSummaryJSON `json:"group"`
	}
	decodeJSON(t, w, &created)
	if created.Group.Status != models.GroupPending {
		t.Fatalf("expected pending, got %q", created.Group.Status)
	}

	// Pending group is hidden from the public list.
	w = leader.do("GET", "/api/v1/groups", nil, false)
	var list struct {
		Groups []groupSummaryJSON `json:"groups"`
	}
	decodeJSON(t, w, &list)
	if len(list.Groups) != 0 {
		t.Fatalf("pending group visible publicly: %d", len(list.Groups))
	}

	// Non-admin cannot approve.
	w = leader.do("POST", "/api/v1/admin/groups/1/approve", map[string]any{}, true)
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-admin approve: got %d", w.Code)
	}

	// Admin approves → group goes live.
	admin := loginAPI(t, app, "admin", "pw")
	w = admin.do("GET", "/api/v1/admin/pending", nil, false)
	var pending struct {
		Groups []groupSummaryJSON `json:"groups"`
	}
	decodeJSON(t, w, &pending)
	if len(pending.Groups) != 1 {
		t.Fatalf("pending queue: %d", len(pending.Groups))
	}
	w = admin.do("POST", "/api/v1/admin/groups/1/approve",
		map[string]any{"tags": []string{"outdoors", "fitness"}}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", w.Code, w.Body.String())
	}

	// Now visible publicly.
	w = leader.do("GET", "/api/v1/groups", nil, false)
	decodeJSON(t, w, &list)
	if len(list.Groups) != 1 || list.Groups[0].Status != models.GroupActive {
		t.Fatalf("approved group not listed: %+v", list.Groups)
	}
	if len(list.Groups[0].Tags) != 2 {
		t.Fatalf("tags not saved: %v", list.Groups[0].Tags)
	}
}

func TestJoinLeaveAndRSVP(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	newTestUser(t, app, "sam", "pw", models.RoleMember)

	admin := loginAPI(t, app, "admin", "pw")
	w := admin.do("POST", "/api/v1/groups",
		map[string]any{"name": "Runners", "location": "Park"}, true)
	var created struct {
		Group groupSummaryJSON `json:"group"`
	}
	decodeJSON(t, w, &created)
	gid := itoa(created.Group.ID)
	admin.do("POST", "/api/v1/admin/groups/"+gid+"/approve", map[string]any{}, true)

	sam := loginAPI(t, app, "sam", "pw")

	// Join (idempotent).
	for i := 0; i < 2; i++ {
		w = sam.do("POST", "/api/v1/groups/"+gid+"/join", map[string]any{}, true)
		if w.Code != http.StatusOK {
			t.Fatalf("join: %d %s", w.Code, w.Body.String())
		}
	}
	var n int64
	app.DB.Model(&models.Membership{}).Where("group_id = ?", created.Group.ID).Count(&n)
	if n != 2 { // admin-leader + sam
		t.Fatalf("expected 2 memberships, got %d", n)
	}

	// Leader creates a weekly event. 2026-10-14 is a Wednesday.
	w = admin.do("POST", "/api/v1/groups/"+gid+"/events", map[string]any{
		"startsLocal": "2026-10-14T18:30", "durationMinutes": 90, "tzid": "UTC",
		"recurrenceRule": "FREQ=WEEKLY;BYDAY=WE",
	}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create event: %d %s", w.Code, w.Body.String())
	}
	var mcreated struct {
		Event eventJSON `json:"event"`
	}
	decodeJSON(t, w, &mcreated)
	mid := itoa(mcreated.Event.ID)

	// The stored series covers Oct 14, 21, 28 and Nov 4.
	w = sam.do("GET", "/api/v1/groups/"+gid+"/events?from=2026-10-06&to=2026-11-06", nil, false)
	var mlist struct {
		Events []occurrenceJSON `json:"events"`
	}
	decodeJSON(t, w, &mlist)
	if len(mlist.Events) != 4 {
		t.Fatalf("expected 4 occurrences, got %d", len(mlist.Events))
	}
	first := mlist.Events[0]

	// Sam registers going.
	w = sam.do("PUT", "/api/v1/events/"+mid+"/registrations", map[string]any{
		"status": "going", "date": first.Date,
	}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("register: %d %s", w.Code, w.Body.String())
	}

	w = sam.do("GET", "/api/v1/groups/"+gid+"/events?from=2026-10-06&to=2026-11-06", nil, false)
	decodeJSON(t, w, &mlist)
	if mlist.Events[0].Registrations["going"] != 1 {
		t.Fatalf("registration count: %+v", mlist.Events[0].Registrations)
	}
	if mlist.Events[0].MyRegistration == nil || *mlist.Events[0].MyRegistration != "going" {
		t.Fatalf("myRegistration missing: %+v", mlist.Events[0].MyRegistration)
	}

	// Cancel one date and it drops out of the scheduled list.
	w = admin.do("POST", "/api/v1/events/"+mid+"/cancel",
		map[string]any{"date": first.Date}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	w = sam.do("GET", "/api/v1/groups/"+gid+"/events?from=2026-10-06&to=2026-11-06", nil, false)
	decodeJSON(t, w, &mlist)
	if len(mlist.Events) != 3 {
		t.Fatalf("expected 3 after cancel, got %d", len(mlist.Events))
	}

	// Non-leader cannot create events.
	w = sam.do("POST", "/api/v1/groups/"+gid+"/events", map[string]any{
		"startsLocal": "2026-10-14T18:30", "durationMinutes": 60, "tzid": "UTC",
	}, true)
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-leader create event: %d", w.Code)
	}

	// Leave.
	w = confirmDeparture(t, sam, "POST", "/api/v1/groups/"+gid+"/leave", "/api/v1/groups/"+gid+"/departure-preview", "")
	if w.Code != http.StatusOK {
		t.Fatalf("leave: %d", w.Code)
	}
	app.DB.Model(&models.Membership{}).Where("group_id = ? AND status = ?", created.Group.ID, "active").Count(&n)
	if n != 1 {
		t.Fatalf("expected 1 active membership after leave, got %d", n)
	}
}

func TestAnonymousGroupCreateRejected(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	c := newAPIClient(t, app)
	c.fetchCSRF() // real clients always hold a token; auth is checked after CSRF
	w := c.do("POST", "/api/v1/groups", map[string]any{"name": "X"}, true)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anon create: got %d", w.Code)
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
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"departure_unavailable"`) {
		t.Fatalf("leader leave: got %d %s", w.Code, w.Body.String())
	}

	sam := loginAPI(t, app, "sam", "password1234")
	sam.do("POST", "/api/v1/groups/"+gid+"/join", map[string]any{}, true)
	w = confirmDeparture(t, sam, "POST", "/api/v1/groups/"+gid+"/leave", "/api/v1/groups/"+gid+"/departure-preview", "")
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
	g := newTestGroup(t, app, leaderUser, "Hidden", models.GroupPending)
	start := time.Date(2026, 11, 1, 18, 0, 0, 0, time.UTC)
	ev, _ := seedEvent(t, app, g, leaderUser.ID, start)
	gid := itoa(g.ID)

	sam := loginAPI(t, app, "sam", "password1234")
	if w := sam.do("GET", "/api/v1/groups/"+gid+"/members", nil, false); w.Code != http.StatusNotFound {
		t.Fatalf("members: got %d", w.Code)
	}
	w := sam.do("PUT", "/api/v1/events/"+itoa(ev.ID)+"/registrations", map[string]any{
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

func TestDirectoryVisibility(t *testing.T) {
	app := testApp(t)
	leaderUser := newTestUser(t, app, "leader", "password1234", models.RoleMember)
	newTestUser(t, app, "sam", "password1234", models.RoleMember)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	activeGroup(t, app, leaderUser, "Public Choir", models.VisibilityPublic, "")
	activeGroup(t, app, leaderUser, "Private Band", models.VisibilityPrivate, "")
	invite := activeGroup(t, app, leaderUser, "Invite Prayer", models.VisibilityInviteOnly, "")

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
		"name": "Nope", "visibility": "secret",
	}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown visibility on create: %d %s", w.Code, w.Body.String())
	}
	w = leader.do("PUT", "/api/v1/groups/"+itoa(invite.ID), map[string]any{
		"visibility": "hidden",
	}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown visibility on update: %d %s", w.Code, w.Body.String())
	}

	w = leader.do("POST", "/api/v1/groups/"+itoa(invite.ID)+"/members", map[string]any{"email": "sam@example.com"}, true)
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
	activeGroup(t, app, leaderUser, "Youth Choir", models.VisibilityPublic, "choir,youth")
	activeGroup(t, app, leaderUser, "Youth Band", models.VisibilityPublic, "band,youth")
	activeGroup(t, app, leaderUser, "Sunday Choir", models.VisibilityPublic, "choir")
	activeGroup(t, app, leaderUser, "Women's Bible Study", models.VisibilityPublic, "Women,study")
	activeGroup(t, app, leaderUser, "Men's Breakfast", models.VisibilityPublic, "men, breakfast")
	activeGroup(t, app, leaderUser, "Hidden Choir", models.VisibilityInviteOnly, "choir,secret")

	anon := newAPIClient(t, app)
	cases := []struct {
		query string
		want  []string
	}{
		// Repeated tags are OR-ed: any selected tag matches.
		{"tag=choir&tag=band", []string{"Sunday Choir", "Youth Band", "Youth Choir"}},
		{"tag=choir", []string{"Sunday Choir", "Youth Choir"}},
		// Search still narrows the OR.
		{"tag=choir&tag=band&search=youth", []string{"Youth Band", "Youth Choir"}},
		// Exact tags: "men" is not a substring match for "women".
		{"tag=men", []string{"Men's Breakfast"}},
		// Case-insensitive both ways, trimmed, blanks ignored.
		{"tag=CHOIR", []string{"Sunday Choir", "Youth Choir"}},
		{"tag=+women+&tag=", []string{"Women's Bible Study"}},
		// Tags of groups the viewer can't see never match.
		{"tag=secret", nil},
	}
	for _, tc := range cases {
		got := groupNames(t, anon.do("GET", "/api/v1/groups?"+tc.query, nil, false))
		sort.Strings(got)
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Fatalf("%s: got %v, want %v", tc.query, got, tc.want)
		}
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
	if !strings.Contains(w.Body.String(), "asked to delete Solo") {
		t.Fatalf("notification: %s", w.Body.String())
	}
	if w := admin.do("POST", "/api/v1/admin/groups/"+gid+"/reject-deletion", map[string]any{}, true); w.Code != http.StatusOK {
		t.Fatalf("reject deletion: %d %s", w.Code, w.Body.String())
	}
	var still models.Group
	if err := app.DB.First(&still, created.Group.ID).Error; err != nil || still.DeletionRequested {
		t.Fatalf("group after reject: err=%v requested=%v", err, still.DeletionRequested)
	}

	if w := leader.do("POST", "/api/v1/groups/"+gid+"/members", map[string]any{"email": "sam@example.com"}, true); w.Code != http.StatusOK {
		t.Fatalf("add sam: %d %s", w.Code, w.Body.String())
	}
	if w := leader.do("POST", "/api/v1/groups/"+gid+"/deletion-request", map[string]any{}, true); w.Code != http.StatusBadRequest {
		t.Fatalf("delete with a member: %d %s", w.Code, w.Body.String())
	}
	w = leader.do("PUT", "/api/v1/groups/"+gid+"/members/"+itoa(samUser.ID)+"/role", map[string]any{"role": "leader"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("make sam a leader: %d %s", w.Code, w.Body.String())
	}
	if w := confirmDeparture(t, leader, "POST", "/api/v1/groups/"+gid+"/leave", "/api/v1/groups/"+gid+"/departure-preview", ""); w.Code != http.StatusOK {
		t.Fatalf("old leader leave: %d %s", w.Code, w.Body.String())
	}
	var detail struct {
		Group groupSummaryJSON `json:"group"`
	}
	w = leader.do("GET", "/api/v1/groups/"+gid, nil, false)
	decodeJSON(t, w, &detail)
	if detail.Group.LeaderID != samUser.ID {
		t.Fatalf("leader id: %d", detail.Group.LeaderID)
	}
	if w := sam.do("POST", "/api/v1/groups/"+gid+"/deletion-request", map[string]any{}, true); w.Code != http.StatusOK {
		t.Fatalf("new leader request: %d %s", w.Code, w.Body.String())
	}

	busy := activeGroup(t, app, leaderUser, "Busy", models.VisibilityPublic, "")
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

// Hardening review (Oct 2026): every list of event dates hides events the
// viewer can't see, and making an event itself more private cancels the
// registrations of people it now shuts out.

func TestNarrowEventHiddenFromGroupLists(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	gid := createGroupAs(t, admin, "Choir", models.VisibilityPublic)
	open := scheduleGroupEvent(t, admin, gid, "Open rehearsal")
	secret := scheduleGroupEvent(t, admin, gid, "Members-only planning")
	setVisibility(t, admin, secret.ID, models.VisibilityInviteOnly, "", http.StatusOK)
	priv := scheduleGroupEvent(t, admin, gid, "Signed-in social")
	setVisibility(t, admin, priv.ID, models.VisibilityPrivate, "", http.StatusOK)

	newTestUser(t, app, "out", "password1234", models.RoleMember)
	outC := loginAPI(t, app, "out", "password1234")
	anon := newAPIClient(t, app)

	titles := func(c *apiClient) string {
		var b strings.Builder
		for _, o := range listOccurrences(t, c, itoa(gid), "2026-11-01", "2026-12-31") {
			b.WriteString(o.Title + "|")
		}
		w := c.do("GET", "/api/v1/groups/"+itoa(gid), nil, false)
		b.WriteString(w.Body.String())
		return b.String()
	}
	a, o, ad := titles(anon), titles(outC), titles(admin)
	for _, s := range []string{a, o, ad} {
		if !strings.Contains(s, open.Title) {
			t.Fatalf("public event missing: %s", s)
		}
	}
	if strings.Contains(a, secret.Title) || strings.Contains(a, priv.Title) {
		t.Fatalf("anonymous visitor saw a narrower event: %s", a)
	}
	if strings.Contains(o, secret.Title) || !strings.Contains(o, priv.Title) {
		t.Fatalf("signed-in outsider: %s", o)
	}
	if !strings.Contains(ad, secret.Title) {
		t.Fatal("admin lost the invite-only event")
	}
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
	if w := bobC.do("POST", "/api/v1/groups/"+gid+"/leave", map[string]any{}, true); w.Code != http.StatusConflict {
		t.Fatalf("last leader leaves: %d", w.Code)
	}
	if w := adminC.do("DELETE", "/api/v1/admin/groups/"+gid+"/members/"+itoa(bob.ID), nil, true); w.Code != http.StatusConflict {
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
	if w := confirmDeparture(t, leader, "DELETE", path+itoa(sam.ID), path+itoa(sam.ID)+"/departure-preview", "asked to leave the choir"); w.Code != http.StatusOK {
		t.Fatalf("remove member: %d %s", w.Code, w.Body.String())
	}
	if w := confirmDeparture(t, leader, "DELETE", path+itoa(co.ID), path+itoa(co.ID)+"/departure-preview", ""); w.Code != http.StatusOK {
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
	if w := leader.do("DELETE", path+itoa(adminUser.ID), nil, true); w.Code != http.StatusConflict {
		t.Fatalf("remove admin: got %d, want 409", w.Code)
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
	if w := admin.do("DELETE", path+itoa(lead.ID), nil, true); w.Code != http.StatusConflict {
		t.Fatalf("remove last leader: got %d, want 409", w.Code)
	}
	if w := admin.do("DELETE", "/api/v1/admin/groups/"+itoa(g.ID)+"/members/"+itoa(lead.ID), nil, true); w.Code != http.StatusConflict {
		t.Fatalf("admin route, last leader: got %d, want 409", w.Code)
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

// Guard for the "Sun 1:00 AM UTC" bug: the list and detail APIs carry the
// next event's UTC instant, and an event created without a zone takes the
// church's zone, never UTC. The browser converts the instant (DEC-57).
func TestGroupListCarriesNextEventAt(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	if w := admin.do("PUT", "/api/v1/admin/timezone", map[string]any{"timezone": "America/Denver"}, true); w.Code != http.StatusOK {
		t.Fatalf("set tz: %d %s", w.Code, w.Body.String())
	}
	w := admin.do("POST", "/api/v1/groups", map[string]any{"name": "Choir"}, true)
	var created struct {
		Group groupSummaryJSON `json:"group"`
	}
	decodeJSON(t, w, &created)
	gid := itoa(created.Group.ID)
	admin.do("POST", "/api/v1/admin/groups/"+gid+"/approve", map[string]any{}, true)

	// Next Saturday at 7 PM, no tzid sent: the church zone applies.
	den, _ := time.LoadLocation("America/Denver")
	d := time.Now().In(den).AddDate(0, 0, 1)
	for d.Weekday() != time.Saturday {
		d = d.AddDate(0, 0, 1)
	}
	local := d.Format("2006-01-02") + "T19:00"
	if w := admin.do("POST", "/api/v1/groups/"+gid+"/events", map[string]any{
		"startsLocal": local, "durationMinutes": 60,
	}, true); w.Code != http.StatusCreated {
		t.Fatalf("create event: %d %s", w.Code, w.Body.String())
	}

	check := func(where string, g groupSummaryJSON) {
		t.Helper()
		if g.NextEventAt == nil {
			t.Fatalf("%s: no next event", where)
		}
		if want := time.Date(d.Year(), d.Month(), d.Day(), 19, 0, 0, 0, den); !g.NextEventAt.Equal(want) {
			t.Fatalf("%s: next event at %s, want %s (7 PM Denver)", where, g.NextEventAt, want)
		}
	}
	w = admin.do("GET", "/api/v1/groups", nil, false)
	var list struct {
		Groups []groupSummaryJSON `json:"groups"`
	}
	decodeJSON(t, w, &list)
	found := false
	for _, g := range list.Groups {
		if g.ID == created.Group.ID {
			check("list", g)
			found = true
		}
	}
	if !found {
		t.Fatalf("group not listed: %s", w.Body.String())
	}
	w = admin.do("GET", "/api/v1/groups/"+gid, nil, false)
	var detail struct {
		Group groupSummaryJSON `json:"group"`
	}
	decodeJSON(t, w, &detail)
	check("detail", detail.Group)
}

func TestSelectionGroupAddsCapacityAuditAndTeenGate(t *testing.T) {
	app, actor, client := peopleTestApp(t)
	g := newTestGroup(t, app, actor, "Selection", models.GroupActive)
	limit := 1
	app.DB.Model(g).Update("member_limit", limit)
	target := newTestUser(t, app, "selected adult", "pw", models.RoleMember)
	token, _ := app.newPeopleSelection(actor.ID, target.ID, time.Now())
	path := "/api/v1/groups/" + itoa2(g.ID) + "/members"
	if w := client.do("POST", path, map[string]any{"selectionId": token}, true); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := client.do("POST", path, map[string]any{"selectionId": token, "overLimit": true}, true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := client.do("POST", path, map[string]any{"selectionId": token}, true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var n int64
	app.DB.Model(&models.AuditLog{}).Where("action = ?", audit.MemberAdded).Count(&n)
	if n != 1 {
		t.Fatalf("add/no-op audits %d", n)
	}
	app.DB.Model(&models.AuditLog{}).Where("action = ?", audit.AddedOverLimit).Count(&n)
	if n != 1 {
		t.Fatalf("overlimit audits %d", n)
	}
	teen := newTestUser(t, app, "teen selected", "pw", models.RoleMember)
	birth := time.Now().AddDate(-15, 0, 0)
	app.DB.Model(teen).Updates(map[string]any{"account_type": models.AccountTeen, "birth_date": birth})
	teenToken, _ := app.newPeopleSelection(actor.ID, teen.ID, time.Now())
	for _, body := range []map[string]any{{"selectionId": teenToken, "overLimit": true}, {"email": teen.Email, "overLimit": true}} {
		w := client.do("POST", path, body, true)
		if w.Code != 409 || !strings.Contains(w.Body.String(), "teen_add_approval_required") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if membershipStatus(app, teen, g) != "none" {
		t.Fatal("teen bypassed approval")
	}
}

func TestSelectionGroupAddAuditFailureRollsBack(t *testing.T) {
	app, actor, client := peopleTestApp(t)
	g := newTestGroup(t, app, actor, "Rollback", models.GroupActive)
	target := newTestUser(t, app, "rollback target", "pw", models.RoleMember)
	token, _ := app.newPeopleSelection(actor.ID, target.ID, time.Now())
	name := "people:audit-failure"
	if err := app.DB.Callback().Create().Before("gorm:create").Register(name, func(db *gorm.DB) {
		if db.Statement.Table == "audit_logs" {
			db.AddError(fmt.Errorf("injected audit failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.DB.Callback().Create().Remove(name) })
	w := client.do("POST", "/api/v1/groups/"+itoa2(g.ID)+"/members", map[string]any{"selectionId": token}, true)
	if w.Code != 500 {
		t.Fatal(w.Code, w.Body.String())
	}
	if membershipStatus(app, target, g) != "none" {
		t.Fatal("audit failure committed membership")
	}
}

func TestMemberAddSiblingAuditsAndTeenSelfJoin(t *testing.T) {
	app, actor, client := peopleTestApp(t)
	leader := newTestUser(t, app, "sibling leader", "pw", models.RoleMember)
	g := newTestGroup(t, app, leader, "Sibling adds", models.GroupActive)
	emailTarget := newTestUser(t, app, "email sibling", "pw", models.RoleMember)
	rawTarget := newTestUser(t, app, "raw sibling", "pw", models.RoleMember)
	for _, tt := range []struct {
		path string
		body map[string]any
	}{
		{"/api/v1/groups/" + itoa2(g.ID) + "/members", map[string]any{"email": emailTarget.Email}},
		{"/api/v1/admin/groups/" + itoa2(g.ID) + "/members", map[string]any{"userId": rawTarget.ID}},
		{"/api/v1/admin/groups/" + itoa2(g.ID) + "/members", map[string]any{"userId": actor.ID}},
	} {
		if w := client.do("POST", tt.path, tt.body, true); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	var n int64
	app.DB.Model(&models.AuditLog{}).Where("action = ?", audit.MemberAdded).Count(&n)
	if n != 3 {
		t.Fatalf("email/raw/self manager audits: %d", n)
	}
	teen := newTestUser(t, app, "self teen", "pw", models.RoleMember)
	birth := time.Now().AddDate(-15, 0, 0)
	app.DB.Model(teen).Updates(map[string]any{"account_type": models.AccountTeen, "birth_date": birth})
	teenClient := loginAPI(t, app, "self teen", "pw")
	w := teenClient.do("POST", "/api/v1/groups/"+itoa2(g.ID)+"/join", map[string]any{}, true)
	if w.Code != 200 {
		t.Fatal("self join changed", w.Code, w.Body.String())
	}
}

func TestGroupManagerAddRefreshesActorAfterContactGate(t *testing.T) {
	for _, targetKind := range []string{"selection", "email"} {
		for change, revocation := range peopleAddActorRevocations(&models.User{}) {
			t.Run(targetKind+"/"+revocation.name, func(t *testing.T) {
				app, actor, client := peopleTestApp(t)
				leader := newTestUser(t, app, "other leader", "pw", models.RoleMember)
				g := newTestGroup(t, app, leader, "Fresh authority", models.GroupActive)
				target := newTestUser(t, app, "private add target", "pw", models.RoleMember)
				body := map[string]any{"email": target.Email}
				if targetKind == "selection" {
					token, err := app.newPeopleSelection(actor.ID, target.ID, time.Now())
					if err != nil {
						t.Fatal(err)
					}
					body = map[string]any{"selectionId": token}
				}
				gate := holdContactGate(t, app)
				response := queueContact(func() *httptest.ResponseRecorder {
					return client.do("POST", "/api/v1/groups/"+itoa2(g.ID)+"/members", body, true)
				})
				waitContactWaiters(t, app, 1) // authentication/selection already resolved
				revocation := peopleAddActorRevocations(actor)[change]
				if err := gate.Model(actor).Updates(revocation.updates).Error; err != nil {
					t.Fatal(err)
				}
				if err := gate.Commit().Error; err != nil {
					t.Fatal(err)
				}
				w := awaitContactResponse(t, response)
				if w.Code != 403 || strings.Contains(w.Body.String(), target.Email) {
					t.Fatalf("%s: %d %s", revocation.name, w.Code, w.Body.String())
				}
				if membershipStatus(app, target, g) != "none" || contactCount(t, app, &models.AuditLog{}, "action IN ?", []string{audit.MemberAdded, audit.AddedOverLimit, audit.RoleChanged}) != 0 {
					t.Fatal("revoked actor committed membership or add audit")
				}
			})
		}
	}
}

func TestGroupManagerAddRechecksLeaderAfterContactGate(t *testing.T) {
	for _, change := range []string{"demote", "remove"} {
		t.Run(change, func(t *testing.T) {
			app, admin, _ := peopleTestApp(t)
			leader := newTestUser(t, app, "queued leader", "pw", models.RoleMember)
			g := newTestGroup(t, app, leader, "Leadership revocation", models.GroupActive)
			addMember(t, app, admin, g, models.MembershipLeader)
			target := newTestUser(t, app, "leadership target", "pw", models.RoleMember)
			client := loginAPI(t, app, "queued leader", "pw")
			token, err := app.newPeopleSelection(leader.ID, target.ID, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			gate := holdContactGate(t, app)
			response := queueContact(func() *httptest.ResponseRecorder {
				return client.do("POST", "/api/v1/groups/"+itoa2(g.ID)+"/members", map[string]any{"selectionId": token}, true)
			})
			waitContactWaiters(t, app, 1)
			updates := map[string]any{"role": models.MembershipMember}
			if change == "remove" {
				updates["status"] = memberRemoved
			}
			if err := gate.Model(&models.Membership{}).Where("group_id = ? AND user_id = ?", g.ID, leader.ID).Updates(updates).Error; err != nil {
				t.Fatal(err)
			}
			if err := gate.Commit().Error; err != nil {
				t.Fatal(err)
			}
			w := awaitContactResponse(t, response)
			if w.Code != 403 || strings.Contains(w.Body.String(), target.Email) {
				t.Fatal(w.Code, w.Body.String())
			}
			if membershipStatus(app, target, g) != "none" || contactCount(t, app, &models.AuditLog{}, "action = ?", audit.MemberAdded) != 0 {
				t.Fatal("former leader committed membership or add audit")
			}
		})
	}
}

func TestGroupManagerAddExistingLeaderKeepsRoleUnderContactGate(t *testing.T) {
	for _, targetKind := range []string{"selection", "email"} {
		t.Run(targetKind, func(t *testing.T) {
			app, actor, client := peopleTestApp(t)
			leader := newTestUser(t, app, "existing sole leader", "pw", models.RoleMember)
			g := newTestGroup(t, app, leader, "Keep existing leadership", models.GroupActive)
			body := map[string]any{"email": leader.Email}
			if targetKind == "selection" {
				token, err := app.newPeopleSelection(actor.ID, leader.ID, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				body = map[string]any{"selectionId": token}
			}
			path := "/api/v1/groups/" + itoa2(g.ID) + "/members"
			if w := client.do("POST", path, body, true); w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			if !relIn(app.DB, leader.ID, g.ID).Leader || leaderCount(app.DB, g.ID) != 1 {
				t.Fatal("regular add demoted the sole existing leader")
			}
			gate := holdContactGate(t, app)
			response := queueContact(func() *httptest.ResponseRecorder {
				return client.do("POST", path, body, true)
			})
			waitContactWaiters(t, app, 1)
			if err := gate.Model(actor).Update("role", models.RoleMember).Error; err != nil {
				t.Fatal(err)
			}
			if err := gate.Commit().Error; err != nil {
				t.Fatal(err)
			}
			w := awaitContactResponse(t, response)
			if w.Code != 403 || strings.Contains(w.Body.String(), leader.Email) {
				t.Fatal(w.Code, w.Body.String())
			}
			if !relIn(app.DB, leader.ID, g.ID).Leader || contactCount(t, app, &models.AuditLog{}, "action IN ?", []string{audit.MemberAdded, audit.RoleChanged, audit.AddedOverLimit}) != 0 {
				t.Fatal("existing-member add changed leadership or wrote an add audit")
			}
		})
	}
}

func TestGroupDetailUpcomingCapAfterVisibility(t *testing.T) {
	app := testApp(t)
	leader := newTestUser(t, app, "leader", "pw", models.RoleMember)
	g := newTestGroup(t, app, leader, "Busy group", models.GroupActive)
	client := newAPIClient(t, app)
	start := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Minute)
	// Earlier hidden dates must neither consume the cap nor set truncated.
	for i := 0; i < 30; i++ {
		_, o := seedEvent(t, app, g, leader.ID, start.Add(time.Duration(i)*time.Minute))
		if err := app.DB.Model(&o).Update("visibility_override", models.VisibilityInviteOnly).Error; err != nil {
			t.Fatal(err)
		}
	}
	// A hidden date after the visible page must not imply another page.
	_, hiddenTail := seedEvent(t, app, g, leader.ID, start.Add(48*time.Hour))
	if err := app.DB.Model(&hiddenTail).Update("visibility_override", models.VisibilityInviteOnly).Error; err != nil {
		t.Fatal(err)
	}
	visibleIDs := []uint{}
	var cappedReads int64
	read := func(n int) {
		t.Helper()
		counter := countDBReads(t, app.DB)
		w := client.do("GET", "/api/v1/groups/"+itoa(g.ID), nil, false)
		reads := counter.Reads()
		counter.Stop()
		if w.Code != http.StatusOK {
			t.Fatalf("group: %d %s", w.Code, w.Body.String())
		}
		var body struct {
			UpcomingEvents []occurrenceJSON `json:"upcomingEvents"`
			Truncated      bool             `json:"truncated"`
		}
		decodeJSON(t, w, &body)
		want := min(n, maxGroupUpcomingOccurrences)
		if len(body.UpcomingEvents) != want || body.Truncated != (n > maxGroupUpcomingOccurrences) {
			t.Fatalf("%d visible dates: got %d, truncated=%v (want %d)", n, len(body.UpcomingEvents), body.Truncated, want)
		}
		for i, row := range body.UpcomingEvents {
			if row.ID != visibleIDs[i] {
				t.Fatalf("date %d: got %d, want %d", i, row.ID, visibleIDs[i])
			}
		}
		if n == maxGroupUpcomingOccurrences {
			cappedReads = reads
		}
		if n >= maxGroupUpcomingOccurrences && (reads != cappedReads || w.Body.Len() > 40000) {
			t.Fatalf("%d dates: %d reads (baseline=%d), %d bytes (cap=40000)", n, reads, cappedReads, w.Body.Len())
		}
		t.Logf("%d visible dates: returned=%d truncated=%v bytes=%d reads=%d", n, len(body.UpcomingEvents), body.Truncated, w.Body.Len(), reads)
	}
	read(0)
	for i := 1; i <= 50; i++ {
		_, o := seedEvent(t, app, g, leader.ID, start.Add(time.Duration(30+i)*time.Minute))
		visibleIDs = append(visibleIDs, o.ID)
		if i == maxGroupUpcomingOccurrences || i == maxGroupUpcomingOccurrences+1 || i == 50 {
			read(i)
		}
	}
	window := "?from=" + start.Format("2006-01-02") + "&to=" + start.AddDate(0, 0, 1).Format("2006-01-02")
	w := client.do("GET", "/api/v1/groups/"+itoa(g.ID)+"/events"+window, nil, false)
	if w.Code != http.StatusOK {
		t.Fatalf("events: %d %s", w.Code, w.Body.String())
	}
	var list struct {
		Events []occurrenceJSON `json:"events"`
	}
	decodeJSON(t, w, &list)
	if len(list.Events) != 50 {
		t.Fatalf("full events list: got %d, want 50", len(list.Events))
	}
	w = client.do("GET", "/api/v1/calendar"+window+"&tz=UTC&group="+itoa(g.ID), nil, false)
	if w.Code != http.StatusOK {
		t.Fatalf("calendar: %d %s", w.Code, w.Body.String())
	}
	var cal struct {
		Occurrences []calendarOccurrenceJSON `json:"occurrences"`
		Truncated   bool                     `json:"truncated"`
	}
	decodeJSON(t, w, &cal)
	if len(cal.Occurrences) != 50 || cal.Truncated {
		t.Fatalf("calendar: got %d, truncated=%v", len(cal.Occurrences), cal.Truncated)
	}
}
