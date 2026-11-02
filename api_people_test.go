package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"fold/models"
)

func peopleTestApp(t *testing.T) (*App, *models.User, *apiClient) {
	t.Helper()
	app := testApp(t)
	app.Config.SessionSecret = []byte("people-selection-test-secret-32bytes")
	actor := newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	return app, actor, loginAPI(t, app, "admin", "pw")
}

func searchPeople(t *testing.T, c *apiClient, q string) peopleSearchJSON {
	t.Helper()
	w := c.do("GET", "/api/v1/people/search?q="+url.QueryEscape(q), nil, false)
	if w.Code != 200 {
		t.Fatalf("search %d %s", w.Code, w.Body.String())
	}
	var out peopleSearchJSON
	decodeJSON(t, w, &out)
	return out
}

func TestPeopleSearchSafeProjectionAndExclusions(t *testing.T) {
	app, actor, client := peopleTestApp(t)
	adult := newTestUser(t, app, "match adult", "pw", models.RoleMember)
	app.DB.Model(adult).Updates(map[string]any{"display_name": "Same match", "hide_contact_from_leaders": true, "avatar_key": "adult.png"})
	teen := newTestUser(t, app, "match teen", "pw", models.RoleMember)
	birth := time.Now().AddDate(-15, 0, 0)
	app.DB.Model(teen).Updates(map[string]any{"display_name": "Same match", "account_type": models.AccountTeen, "birth_date": birth, "avatar_key": "secret-teen.png"})
	for i, status := range []string{models.UserGuest, models.UserInvited, models.UserDisabled, models.UserPendingConsent} {
		u := newTestUser(t, app, fmt.Sprintf("match excluded%d", i), "pw", models.RoleMember)
		app.DB.Model(u).Update("status", status)
	}
	deleted := newTestUser(t, app, "match deleted", "pw", models.RoleMember)
	app.DB.Delete(deleted)
	leader := newTestUser(t, app, "leader", "pw", models.RoleMember)
	shared := newTestGroup(t, app, leader, "Shared", models.GroupActive)
	addMember(t, app, adult, shared, models.MembershipMember)
	other := newTestGroup(t, app, actor, "Visible", models.GroupActive)
	addMember(t, app, adult, other, models.MembershipMember)
	hidden := newTestGroup(t, app, actor, "SECRET invite group", models.GroupActive)
	app.DB.Model(hidden).Update("visibility", "invite_only")
	addMember(t, app, teen, hidden, models.MembershipMember)
	leadClient := loginAPI(t, app, "leader", "pw")
	qc := countDBReads(t, app.DB)
	w := leadClient.do("GET", "/api/v1/people/search?q=match", nil, false)
	qc.Stop()
	if w.Code != 200 {
		t.Fatalf("search %d %s", w.Code, w.Body.String())
	}
	if qc.Reads() > 12 {
		t.Fatalf("search N+1: %d reads", qc.Reads())
	}
	if strings.Contains(w.Body.String(), "SECRET") || strings.Contains(w.Body.String(), "secret-teen") || strings.Contains(w.Body.String(), "Visible") {
		t.Fatal(w.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 2 {
		t.Fatal(w.Body.String())
	}
	var out peopleSearchJSON
	decodeJSON(t, w, &out)
	if len(out.People) != 2 {
		t.Fatal(w.Body.String())
	}
	var rows []map[string]json.RawMessage
	json.Unmarshal(raw["people"], &rows)
	for i, row := range rows {
		keys := []string{}
		for k := range row {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		want := []string{"displayName", "otherGroups", "selectionId", "sharedGroups", "teen"}
		if !out.People[i].Teen {
			want = append(want, "avatarUrl", "avatarFocalX", "avatarFocalY")
		}
		sort.Strings(want)
		if !reflect.DeepEqual(keys, want) {
			t.Fatalf("keys %v want %v", keys, want)
		}
		if out.People[i].DisplayName != "Same match" {
			t.Fatal(out.People[i])
		}
	}
	if out.People[0].Teen || !reflect.DeepEqual(out.People[0].SharedGroups, []string{"Shared"}) || len(out.People[0].OtherGroups) != 0 {
		t.Fatal(out.People)
	}
	// Search reads never create contact or add audit rows.
	var audits int64
	app.DB.Model(&models.AuditLog{}).Count(&audits)
	if audits != 0 {
		t.Fatalf("search audited: %d", audits)
	}
	// Admins can see the invite-only label, just as on its group page.
	adminOut := searchPeople(t, client, "match")
	if len(adminOut.People[1].SharedGroups) != 1 {
		t.Fatal(adminOut)
	}
}

func TestPeopleSearchRolesBoundsAndThrottle(t *testing.T) {
	app, _, admin := peopleTestApp(t)
	member := newTestUser(t, app, "member", "pw", models.RoleMember)
	memberClient := loginAPI(t, app, "member", "pw")
	if w := memberClient.do("GET", "/api/v1/people/search?q=xx", nil, false); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := newAPIClient(t, app).do("GET", "/api/v1/people/search?q=xx", nil, false); w.Code != 401 {
		t.Fatal(w.Code)
	}
	for _, q := range []string{"", "x", "é"} {
		if got := searchPeople(t, admin, q); got.State != "short_query" || len(got.People) != 0 {
			t.Fatal(got)
		}
	}
	for i := 0; i < 12; i++ {
		newTestUser(t, app, fmt.Sprintf("bounded %02d", i), "pw", models.RoleMember)
	}
	qc := countDBReads(t, app.DB)
	if out := searchPeople(t, admin, "bounded"); len(out.People) != 10 {
		t.Fatal(out)
	}
	qc.Stop()
	if qc.Reads() > 12 {
		t.Fatalf("ten-result search is not batched: %d", qc.Reads())
	}
	if out := searchPeople(t, admin, "%_"); out.State != "no_results" {
		t.Fatal(out)
	}
	for i := 2; i < peopleSearchLimit; i++ {
		searchPeople(t, admin, "zz")
	}
	if out := searchPeople(t, admin, "bounded"); out.State != "throttled" || len(out.People) != 0 {
		t.Fatal(out)
	}
	// A host needs no group membership; a stale group-event host grants nothing.
	ev := models.Event{Title: "Hosted", Visibility: "public", StartsLocal: "2030-01-01T10:00", TZID: "UTC", DurationMinutes: 60, CreatedBy: member.ID}
	if err := app.DB.Create(&ev).Error; err != nil {
		t.Fatal(err)
	}
	app.DB.Create(&models.EventHost{EventID: ev.ID, UserID: member.ID, AddedBy: member.ID})
	if out := searchPeople(t, memberClient, "bounded"); len(out.People) != 10 {
		t.Fatal(out)
	}
	birth := time.Now().AddDate(-16, 0, 0)
	app.DB.Model(member).Updates(map[string]any{"account_type": models.AccountTeen, "birth_date": birth})
	if w := memberClient.do("GET", "/api/v1/people/search?q=bounded", nil, false); w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestPeopleSelectionFailsClosed(t *testing.T) {
	app, actor, client := peopleTestApp(t)
	target := newTestUser(t, app, "chosen", "pw", models.RoleMember)
	now := time.Now()
	token, err := app.newPeopleSelection(actor.ID, target.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		token string
		actor uint
		now   time.Time
	}{{"forged", actor.ID, now}, {token, actor.ID + 1, now}, {token, actor.ID, now.Add(peopleSelectionLifetime)}, {token, actor.ID, now.Add(-time.Minute)}} {
		if _, err := app.parsePeopleSelection(tt.token, tt.actor, tt.now); err == nil {
			t.Fatalf("accepted forged/stale selection: %+v", tt)
		}
	}
	g := newTestGroup(t, app, actor, "Add", models.GroupActive)
	app.DB.Model(target).Update("status", models.UserDisabled)
	w := client.do("POST", "/api/v1/groups/"+itoa2(g.ID)+"/members", map[string]any{"selectionId": token}, true)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "people_selection_unavailable") || strings.Contains(w.Body.String(), target.Email) {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestPeopleSearchGroupLabelsAreBounded(t *testing.T) {
	app, actor, client := peopleTestApp(t)
	target := newTestUser(t, app, "many-label match", "pw", models.RoleMember)
	for i := 0; i < 13; i++ {
		g := newTestGroup(t, app, actor, fmt.Sprintf("Visible %02d", i), models.GroupActive)
		addMember(t, app, target, g, models.MembershipMember)
	}
	out := searchPeople(t, client, "many-label")
	if len(out.People) != 1 || len(out.People[0].SharedGroups) != 10 || len(out.People[0].OtherGroups) != 0 {
		t.Fatal(out)
	}
}

func TestPeopleSelectionRevokedManagerAndWrongActor(t *testing.T) {
	app, _, _ := peopleTestApp(t)
	leader := newTestUser(t, app, "selection leader", "pw", models.RoleMember)
	other := newTestUser(t, app, "other leader", "pw", models.RoleMember)
	target := newTestUser(t, app, "selection subject", "pw", models.RoleMember)
	g := newTestGroup(t, app, leader, "Selected scope", models.GroupActive)
	addMember(t, app, other, g, models.MembershipLeader)
	token, _ := app.newPeopleSelection(leader.ID, target.ID, time.Now())
	otherClient := loginAPI(t, app, "other leader", "pw")
	path := "/api/v1/groups/" + itoa2(g.ID) + "/members"
	if w := otherClient.do("POST", path, map[string]any{"selectionId": token}, true); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	leaderClient := loginAPI(t, app, "selection leader", "pw")
	app.DB.Model(&models.Membership{}).Where("group_id = ? AND user_id = ?", g.ID, leader.ID).Update("role", models.MembershipMember)
	if w := leaderClient.do("POST", path, map[string]any{"selectionId": token}, true); w.Code != 403 && w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
}

// Each change leaves other eligibility facts intact so the gated refresh must
// check the actor's current role, active state, session and actual adult age.
func peopleAddActorRevocations(actor *models.User) []struct {
	name    string
	updates map[string]any
} {
	return []struct {
		name    string
		updates map[string]any
	}{
		{"demotion", map[string]any{"role": models.RoleMember}},
		{"disable", map[string]any{"status": models.UserDisabled}},
		{"session invalidation", map[string]any{"session_version": actor.SessionVersion + 1}},
		{"underage adult", map[string]any{"birth_date": time.Now().AddDate(-15, 0, 0)}},
		{"teen account", map[string]any{"account_type": models.AccountTeen, "birth_date": time.Now().AddDate(-15, 0, 0)}},
	}
}

// A person's groups are roster information: a leader who searches people sees
// only the groups they share with each match, never the match's other groups
// (policy.CanSeeRoster). Admins pass the roster rule everywhere.
func TestPeopleSearchGroupLabelsFollowRosterRule(t *testing.T) {
	app, admin, adminClient := peopleTestApp(t)
	leader := newTestUser(t, app, "roster leader", "pw", models.RoleMember)
	target := newTestUser(t, app, "roster subject", "pw", models.RoleMember)
	mine := newTestGroup(t, app, leader, "Leader group A", models.GroupActive)
	addMember(t, app, target, mine, models.MembershipMember)
	private := newTestGroup(t, app, admin, "Private recovery B", models.GroupActive)
	app.DB.Model(private).Update("visibility", models.VisibilityPrivate)
	addMember(t, app, target, private, models.MembershipMember)
	leaderClient := loginAPI(t, app, "roster leader", "pw")

	w := leaderClient.do("GET", "/api/v1/people/search?q=roster+subject", nil, false)
	if w.Code != 200 || strings.Contains(w.Body.String(), "Private recovery B") {
		t.Fatalf("leader outside B learned the membership: %d %s", w.Code, w.Body.String())
	}
	out := searchPeople(t, leaderClient, "roster subject")
	if len(out.People) != 1 || !reflect.DeepEqual(out.People[0].SharedGroups, []string{"Leader group A"}) || len(out.People[0].OtherGroups) != 0 {
		t.Fatal(out)
	}

	addMember(t, app, leader, private, models.MembershipMember)
	out = searchPeople(t, leaderClient, "roster subject")
	if len(out.People) != 1 || !reflect.DeepEqual(out.People[0].SharedGroups, []string{"Leader group A", "Private recovery B"}) || len(out.People[0].OtherGroups) != 0 {
		t.Fatal(out)
	}

	out = searchPeople(t, adminClient, "roster subject")
	if len(out.People) != 1 || !reflect.DeepEqual(out.People[0].SharedGroups, []string{"Private recovery B"}) || !reflect.DeepEqual(out.People[0].OtherGroups, []string{"Leader group A"}) {
		t.Fatal(out)
	}
}

// Hidden labels are filtered before the per-subject LIMIT. Ten archived groups
// the viewer and the match are both plain members of sort first (shared, then
// by name), and the roster rule hides them (only leaders see archived groups);
// they must not crowd out the active group the viewer shares.
func TestPeopleSearchHiddenLabelsDoNotCrowdOutShared(t *testing.T) {
	app, admin, _ := peopleTestApp(t)
	leader := newTestUser(t, app, "crowd leader", "pw", models.RoleMember)
	target := newTestUser(t, app, "crowd subject", "pw", models.RoleMember)
	for i := 0; i < 10; i++ {
		g := newTestGroup(t, app, admin, fmt.Sprintf("Aaa archived %02d", i), models.GroupArchived)
		addMember(t, app, target, g, models.MembershipMember)
		addMember(t, app, leader, g, models.MembershipMember)
	}
	shared := newTestGroup(t, app, leader, "Zzz shared", models.GroupActive)
	addMember(t, app, target, shared, models.MembershipMember)
	out := searchPeople(t, loginAPI(t, app, "crowd leader", "pw"), "crowd subject")
	if len(out.People) != 1 || !reflect.DeepEqual(out.People[0].SharedGroups, []string{"Zzz shared"}) || len(out.People[0].OtherGroups) != 0 {
		t.Fatal(out)
	}
}
