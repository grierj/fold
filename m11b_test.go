package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"fold/internal/audit"
	"fold/models"
)

func apiDo(t *testing.T, c *apiClient, method, path string, body any, want int) string {
	t.Helper()
	w := c.do(method, path, body, method != "GET")
	if w.Code != want {
		t.Fatalf("%s %s: %d %s, want %d", method, path, w.Code, w.Body.String(), want)
	}
	return w.Body.String()
}

func createGroupAs(t *testing.T, c *apiClient, name, visibility string) uint {
	t.Helper()
	w := c.do("POST", "/api/v1/groups", map[string]any{
		"name": name, "visibility": visibility, "location": "Hall",
	}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create group: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Group groupSummaryJSON `json:"group"`
	}
	decodeJSON(t, w, &created)
	return created.Group.ID
}

func addMemberEmail(t *testing.T, c *apiClient, gid uint, email string) uint {
	t.Helper()
	w := c.do("POST", "/api/v1/groups/"+itoa(gid)+"/members", map[string]any{"email": email}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("add member: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Membership struct {
			UserID uint `json:"userId"`
		} `json:"membership"`
	}
	decodeJSON(t, w, &body)
	return body.Membership.UserID
}

func scheduleGroupEvent(t *testing.T, c *apiClient, gid uint, title string) eventJSON {
	t.Helper()
	w := c.do("POST", "/api/v1/groups/"+itoa(gid)+"/events", map[string]any{
		"title": title, "startsLocal": "2026-12-01T19:00", "durationMinutes": 60, "tzid": "UTC",
	}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create event: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Event eventJSON `json:"event"`
	}
	decodeJSON(t, w, &created)
	return created.Event
}

func setVisibility(t *testing.T, c *apiClient, eventID uint, visibility, reason string, want int) string {
	t.Helper()
	return apiDo(t, c, "PUT", fmt.Sprintf("/api/v1/events/%d/visibility", eventID), map[string]any{
		"visibility": visibility, "reason": reason,
	}, want)
}

type eventPage struct {
	Event            eventJSON        `json:"event"`
	Occurrences      []occurrenceJSON `json:"occurrences"`
	CanManage        bool             `json:"canManage"`
	CanSetVisibility bool             `json:"canSetVisibility"`
}

func getEvent(t *testing.T, c *apiClient, id uint, want int) (eventPage, string) {
	t.Helper()
	w := c.do("GET", fmt.Sprintf("/api/v1/events/%d", id), nil, false)
	if w.Code != want {
		t.Fatalf("get event: %d %s, want %d", w.Code, w.Body.String(), want)
	}
	var page eventPage
	if want == http.StatusOK {
		decodeJSON(t, w, &page)
	}
	return page, w.Body.String()
}

func noticeIDs(t *testing.T, c *apiClient) []notificationJSON {
	t.Helper()
	return feed(t, c, "")
}

func hasVisibilityNotice(ns []notificationJSON, titlePart, reason string) bool {
	for _, n := range ns {
		if n.Kind == "event.visibility_override" && strings.Contains(n.Title, titlePart) && strings.Contains(n.Body, reason) {
			return true
		}
	}
	return false
}

func TestVisibilityOverrideNotice(t *testing.T) {
	app := testApp(t)
	adminUser := newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	_ = adminUser
	gid := createGroupAs(t, admin, "Care", models.VisibilityInviteOnly)

	leaderUser := newTestUser(t, app, "leader2", "password1234", models.RoleMember)
	leaderID := addMemberEmail(t, admin, gid, leaderUser.Email)
	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/groups/%d/members/%d/role", gid, leaderID), map[string]any{"role": "leader"}, http.StatusOK)
	leader := loginAPI(t, app, "leader2", "password1234")

	newTestUser(t, app, "otheradmin", "password1234", models.RoleAdmin)
	other := loginAPI(t, app, "otheradmin", "password1234")

	pat := newTestUser(t, app, "pat", "password1234", models.RoleMember)
	addMemberEmail(t, admin, gid, pat.Email)
	patC := loginAPI(t, app, "pat", "password1234")

	newTestUser(t, app, "rando", "password1234", models.RoleMember)
	randoC := loginAPI(t, app, "rando", "password1234")

	ev := scheduleGroupEvent(t, admin, gid, "Potluck")
	if ev.Visibility != models.VisibilityInviteOnly || ev.LeaderOpened || ev.ChurchWide {
		t.Fatalf("new group event: %+v", ev)
	}

	raw := setVisibility(t, leader, ev.ID, models.VisibilityPublic, "friends can come", http.StatusOK)
	if strings.Contains(raw, "@") {
		t.Fatalf("visibility response leaked contact: %s", raw)
	}

	var rows []models.AuditLog
	if err := app.DB.Where("action = ? AND target_id = ?", audit.VisibilityOverride, ev.ID).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ActorID == nil || *rows[0].ActorID != leaderUser.ID {
		t.Fatalf("override audit: %+v", rows)
	}
	var meta struct {
		Reason          string `json:"reason"`
		GroupVisibility string `json:"groupVisibility"`
		NewVisibility   string `json:"newVisibility"`
		OldVisibility   string `json:"oldVisibility"`
	}
	if err := json.Unmarshal([]byte(rows[0].MetaJSON), &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Reason != "friends can come" || meta.GroupVisibility != "invite_only" || meta.NewVisibility != "public" || meta.OldVisibility != "invite_only" {
		t.Fatalf("meta: %+v", meta)
	}

	for _, c := range []*apiClient{admin, other} {
		if !hasVisibilityNotice(noticeIDs(t, c), "Potluck", "friends can come") {
			t.Fatalf("missing notice: %+v", noticeIDs(t, c))
		}
	}
	for _, c := range []*apiClient{leader, patC, randoC} {
		if hasVisibilityNotice(noticeIDs(t, c), "Potluck", "friends can come") {
			t.Fatalf("unexpected notice: %+v", noticeIDs(t, c))
		}
	}
	adminNotes := noticeIDs(t, admin)
	for _, n := range adminNotes {
		if n.Kind == "event.visibility_override" && strings.Contains(n.Body, "@") {
			t.Fatalf("notice has contact: %+v", n)
		}
		if n.Kind == "event.visibility_override" && !strings.Contains(n.Body, "leader2") {
			t.Fatalf("notice should name the actor: %+v", n)
		}
	}

	if page, _ := getEvent(t, admin, ev.ID, http.StatusOK); !page.Event.LeaderOpened {
		t.Fatalf("opened wider should set the marker: %+v", page.Event)
	}

	// Going back to the group's value is audited, clears the marker, and
	// does not add another override notice.
	setVisibility(t, leader, ev.ID, models.VisibilityInviteOnly, "", http.StatusOK)
	var changed int64
	app.DB.Model(&models.AuditLog{}).Where("action = ? AND target_id = ?", audit.VisibilityChanged, ev.ID).Count(&changed)
	if changed != 1 {
		t.Fatalf("changed rows: %d", changed)
	}
	var overrides int64
	app.DB.Model(&models.AuditLog{}).Where("action = ? AND target_id = ?", audit.VisibilityOverride, ev.ID).Count(&overrides)
	if overrides != 1 {
		t.Fatalf("override rows: %d", overrides)
	}
	page, _ := getEvent(t, admin, ev.ID, http.StatusOK)
	if page.Event.Visibility != models.VisibilityInviteOnly || page.Event.LeaderOpened {
		t.Fatalf("back to group: %+v", page.Event)
	}
}

// DEC-48: an event copies the group's visibility at creation and keeps its
// own value. Opening the group never changes events; making it more
// private tightens inherited events.
func TestEventKeepsItsOwnVisibility(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	gid := createGroupAs(t, admin, "Choir", models.VisibilityPrivate)
	rehearsal := scheduleGroupEvent(t, admin, gid, "Rehearsal")
	if rehearsal.Visibility != models.VisibilityPrivate || rehearsal.LeaderOpened {
		t.Fatalf("copied at creation: %+v", rehearsal)
	}
	// Setting the group's own value is not an override.
	setVisibility(t, admin, rehearsal.ID, models.VisibilityPrivate, "", http.StatusOK)
	var overrides int64
	app.DB.Model(&models.AuditLog{}).Where("action = ? AND target_id = ?", audit.VisibilityOverride, rehearsal.ID).Count(&overrides)
	if overrides != 0 {
		t.Fatal("matching the group is not an override")
	}

	// More open group: events don't move.
	apiDo(t, admin, "PUT", "/api/v1/groups/"+itoa(gid), map[string]any{"visibility": models.VisibilityPublic}, http.StatusOK)
	page, _ := getEvent(t, admin, rehearsal.ID, http.StatusOK)
	if page.Event.Visibility != models.VisibilityPrivate {
		t.Fatalf("opening the group changed the event: %+v", page.Event)
	}
	guest := newAPIClient(t, app)
	getEvent(t, guest, rehearsal.ID, http.StatusNotFound)

	// A stricter value is audited without a notice.
	concert := scheduleGroupEvent(t, admin, gid, "Concert")
	setVisibility(t, admin, concert.ID, models.VisibilityPrivate, "", http.StatusOK)
	var changed int64
	app.DB.Model(&models.AuditLog{}).Where("action = ? AND target_id = ?", audit.VisibilityChanged, concert.ID).Count(&changed)
	if changed != 1 {
		t.Fatal("stricter change was not audited")
	}
	if hasVisibilityNotice(noticeIDs(t, admin), "Concert", "private") {
		t.Fatal("stricter change notified")
	}

	// More private group: inherited events tighten, no decision needed.
	apiDo(t, admin, "PUT", "/api/v1/groups/"+itoa(gid), map[string]any{"visibility": models.VisibilityInviteOnly}, http.StatusOK)
	for _, id := range []uint{rehearsal.ID, concert.ID} {
		page, _ = getEvent(t, admin, id, http.StatusOK)
		if page.Event.Visibility != models.VisibilityInviteOnly || page.Event.LeaderOpened {
			t.Fatalf("not tightened: %+v", page.Event)
		}
	}
}

func TestOpenedEventHidesGroupAndNames(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	gid := createGroupAs(t, admin, "Hidden Care", models.VisibilityInviteOnly)
	sam := newTestUser(t, app, "secretsam", "password1234", models.RoleMember)
	if err := app.DB.Model(sam).Update("name", "Secret Sam").Error; err != nil {
		t.Fatal(err)
	}
	addMemberEmail(t, admin, gid, sam.Email)
	samC := loginAPI(t, app, "secretsam", "password1234")

	ev := scheduleGroupEvent(t, admin, gid, "Open House")
	apiDo(t, samC, "PUT", fmt.Sprintf("/api/v1/events/%d/registrations", ev.ID), map[string]any{
		"status": "going", "date": "2026-12-01",
	}, http.StatusOK)
	setVisibility(t, admin, ev.ID, models.VisibilityPublic, "", http.StatusOK)

	outsiderUser := newTestUser(t, app, "outsider", "password1234", models.RoleMember)
	outsider := loginAPI(t, app, "outsider", "password1234")
	page, raw := getEvent(t, outsider, ev.ID, http.StatusOK)
	if page.Event.HostedBy != "Fold" || page.Event.GroupID != nil || page.Event.GroupName != "" {
		t.Fatalf("event identity: %+v", page.Event)
	}
	if strings.Contains(raw, "Secret Sam") || strings.Contains(raw, "Hidden Care") || strings.Contains(raw, "@") {
		t.Fatalf("outsider payload named the group or a member: %s", raw)
	}
	if len(page.Occurrences) != 1 || page.Occurrences[0].AttendeesVisible || page.Occurrences[0].HostedBy != "Fold" {
		t.Fatalf("occurrence: %+v", page.Occurrences)
	}
	if page.Occurrences[0].Registrations["going"] != 1 {
		t.Fatalf("count: %+v", page.Occurrences[0].Registrations)
	}

	// The group itself stays hidden, and registering does not join it.
	apiDo(t, outsider, "GET", "/api/v1/groups/"+itoa(gid), nil, http.StatusNotFound)
	apiDo(t, outsider, "PUT", fmt.Sprintf("/api/v1/events/%d/registrations", ev.ID), map[string]any{
		"status": "maybe", "date": "2026-12-01",
	}, http.StatusOK)
	var n int64
	app.DB.Model(&models.Membership{}).Where("user_id = ? AND group_id = ?", outsiderUser.ID, gid).Count(&n)
	if n != 0 {
		t.Fatalf("registering created a membership")
	}

	memberPage, memberRaw := getEvent(t, samC, ev.ID, http.StatusOK)
	if memberPage.Event.GroupID == nil || memberPage.Event.GroupName != "Hidden Care" {
		t.Fatalf("member should see the group: %+v", memberPage.Event)
	}
	if len(memberPage.Occurrences) != 1 || !memberPage.Occurrences[0].AttendeesVisible || !strings.Contains(memberRaw, "Secret Sam") {
		t.Fatalf("member names: %s", memberRaw)
	}

	// A signed-in outsider of an ordinary public event may see adult names,
	// and that event is not duplicated on the home list.
	pub := createGroupAs(t, admin, "Public Choir", models.VisibilityPublic)
	ordinary := scheduleGroupEvent(t, admin, pub, "Sunday")
	apiDo(t, outsider, "PUT", fmt.Sprintf("/api/v1/events/%d/registrations", ordinary.ID), map[string]any{
		"status": "going", "date": "2026-12-01",
	}, http.StatusOK)
	ordinaryPage, ordinaryRaw := getEvent(t, samC, ordinary.ID, http.StatusOK)
	if !ordinaryPage.Occurrences[0].AttendeesVisible || !strings.Contains(ordinaryRaw, "outsider") {
		t.Fatalf("ordinary public names: %s", ordinaryRaw)
	}

	w := outsider.do("GET", "/api/v1/events", nil, false)
	if w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	list := w.Body.String()
	if !strings.Contains(list, "Open House") || strings.Contains(list, "Sunday") || strings.Contains(list, "Hidden Care") {
		t.Fatalf("open list: %s", list)
	}

	guest := newAPIClient(t, app)
	guestPage, guestRaw := getEvent(t, guest, ev.ID, http.StatusOK)
	if guestPage.Occurrences[0].AttendeesVisible || strings.Contains(guestRaw, "Secret Sam") || guestPage.Event.HostedBy != "Fold" {
		t.Fatalf("guest: %s", guestRaw)
	}
	setVisibility(t, admin, ev.ID, models.VisibilityPrivate, "", http.StatusOK)
	getEvent(t, guest, ev.ID, http.StatusNotFound)
	privatePage, privateRaw := getEvent(t, outsider, ev.ID, http.StatusOK)
	if privatePage.Occurrences[0].AttendeesVisible || strings.Contains(privateRaw, "Secret Sam") {
		t.Fatalf("opened private names: %s", privateRaw)
	}
}

func TestChurchWideHosts(t *testing.T) {
	app := testApp(t)
	adminUser := newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	hostUser := newTestUser(t, app, "host", "password1234", models.RoleMember)
	host := loginAPI(t, app, "host", "password1234")
	guest := newAPIClient(t, app)

	apiDo(t, host, "POST", "/api/v1/events", map[string]any{
		"title": "Nope", "startsLocal": "2026-12-01T19:00", "durationMinutes": 60,
	}, http.StatusForbidden)
	apiDo(t, admin, "POST", "/api/v1/events", map[string]any{
		"title": "Nope", "startsLocal": "2026-12-01T19:00", "durationMinutes": 60,
		"visibility": "invite_only",
	}, http.StatusBadRequest)

	w := admin.do("POST", "/api/v1/events", map[string]any{
		"title": "Potluck", "startsLocal": "2026-12-01T19:00", "durationMinutes": 90,
		"tzid": "UTC", "hosts": []string{hostUser.Email},
	}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Event eventJSON `json:"event"`
	}
	decodeJSON(t, w, &created)
	ev := created.Event
	if !ev.ChurchWide || ev.GroupID != nil || ev.HostedBy != "Fold" || ev.Visibility != models.VisibilityPublic {
		t.Fatalf("church event: %+v", ev)
	}
	if len(ev.Hosts) != 2 {
		t.Fatalf("hosts: %+v", ev.Hosts)
	}
	got := map[uint]bool{}
	for _, h := range ev.Hosts {
		got[h.UserID] = true
		if strings.Contains(h.Email, " ") {
			t.Fatalf("host email: %+v", h)
		}
	}
	if !got[adminUser.ID] || !got[hostUser.ID] {
		t.Fatalf("creator was not kept as a host: %+v", ev.Hosts)
	}

	// The host list is an admin view. A guest sees the event and no names or emails.
	guestPage, guestRaw := getEvent(t, guest, ev.ID, http.StatusOK)
	if guestPage.Event.HostedBy != "Fold" || len(guestPage.Event.Hosts) != 0 || strings.Contains(guestRaw, "admin@example.com") {
		t.Fatalf("guest church event: %s", guestRaw)
	}
	if guestPage.CanManage || guestPage.CanSetVisibility {
		t.Fatalf("guest permissions: %+v", guestPage)
	}

	apiDo(t, admin, "DELETE", fmt.Sprintf("/api/v1/events/%d/hosts/%d", ev.ID, adminUser.ID), nil, http.StatusOK)
	apiDo(t, admin, "DELETE", fmt.Sprintf("/api/v1/events/%d/hosts/%d", ev.ID, hostUser.ID), nil, http.StatusBadRequest)

	page, _ := getEvent(t, host, ev.ID, http.StatusOK)
	if !page.CanManage || !page.CanSetVisibility {
		t.Fatalf("host permissions: %+v", page)
	}
	apiDo(t, host, "PUT", fmt.Sprintf("/api/v1/events/%d", ev.ID), map[string]any{"title": "Christmas Potluck"}, http.StatusOK)
	apiDo(t, host, "PUT", fmt.Sprintf("/api/v1/events/%d/visibility", ev.ID), map[string]any{
		"visibility": "private", "source": "custom",
	}, http.StatusOK)
	getEvent(t, guest, ev.ID, http.StatusNotFound)
	apiDo(t, host, "PUT", fmt.Sprintf("/api/v1/events/%d/visibility", ev.ID), map[string]any{
		"visibility": "invite_only", "source": "custom",
	}, http.StatusBadRequest)
	apiDo(t, host, "POST", fmt.Sprintf("/api/v1/events/%d/hosts", ev.ID), map[string]any{"email": adminUser.Email}, http.StatusForbidden)

	// A host of this event cannot manage another event or a group they can't see.
	pub := createGroupAs(t, admin, "Public Choir", models.VisibilityPublic)
	other := scheduleGroupEvent(t, admin, pub, "Rehearsal")
	apiDo(t, host, "PUT", fmt.Sprintf("/api/v1/events/%d", other.ID), map[string]any{"title": "Taken"}, http.StatusForbidden)
	hidden := createGroupAs(t, admin, "Hidden Care", models.VisibilityInviteOnly)
	secret := scheduleGroupEvent(t, admin, hidden, "Secret")
	apiDo(t, host, "GET", fmt.Sprintf("/api/v1/events/%d", secret.ID), nil, http.StatusNotFound)
	apiDo(t, host, "PUT", fmt.Sprintf("/api/v1/events/%d", secret.ID), map[string]any{"title": "Taken"}, http.StatusNotFound)

	// Switch back so a signed-in non-host can see registrants' names.
	apiDo(t, host, "PUT", fmt.Sprintf("/api/v1/events/%d/visibility", ev.ID), map[string]any{
		"visibility": "public", "source": "custom",
	}, http.StatusOK)
	apiDo(t, host, "PUT", fmt.Sprintf("/api/v1/events/%d/registrations", ev.ID), map[string]any{
		"status": "going", "date": "2026-12-01",
	}, http.StatusOK)
	reader := newTestUser(t, app, "reader", "password1234", models.RoleMember)
	_ = reader
	readerC := loginAPI(t, app, "reader", "password1234")
	seen, raw := getEvent(t, readerC, ev.ID, http.StatusOK)
	if !seen.Occurrences[0].AttendeesVisible || !strings.Contains(raw, `"name":"host"`) {
		t.Fatalf("signed-in names: %s", raw)
	}
	if seen.CanManage {
		t.Fatal("a non-host can manage the event")
	}
}

func TestEventDescription(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")

	apiDo(t, admin, "POST", "/api/v1/events", map[string]any{
		"title": "Too long", "startsLocal": "2026-12-02T19:00", "durationMinutes": 60,
		"description": strings.Repeat("a", maxDescriptionLen+1),
	}, http.StatusBadRequest)

	w := admin.do("POST", "/api/v1/events", map[string]any{
		"title": "Potluck", "startsLocal": "2026-12-02T19:00", "durationMinutes": 90,
		"description": "  Bring a salad.  ",
	}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Event eventJSON `json:"event"`
	}
	decodeJSON(t, w, &created)
	if created.Event.Description != "Bring a salad." {
		t.Fatalf("description: %q", created.Event.Description)
	}
	page, _ := getEvent(t, admin, created.Event.ID, http.StatusOK)
	if page.Event.Description != "Bring a salad." || page.Occurrences[0].Description != "Bring a salad." {
		t.Fatalf("page: %+v occ %+v", page.Event.Description, page.Occurrences[0].Description)
	}

	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d", created.Event.ID), map[string]any{
		"description": "Dessert too.",
	}, http.StatusOK)
	page, _ = getEvent(t, admin, created.Event.ID, http.StatusOK)
	if page.Event.Description != "Dessert too." || page.Event.Title != "Potluck" {
		t.Fatalf("update: %+v", page.Event)
	}

	gid := createGroupAs(t, admin, "Choir", "public")
	raw := apiDo(t, admin, "POST", fmt.Sprintf("/api/v1/groups/%d/events", gid), map[string]any{
		"title": "Rehearsal", "startsLocal": "2026-12-03T19:00", "durationMinutes": 60,
		"description": "Music stands are in the closet.",
	}, http.StatusCreated)
	if !strings.Contains(raw, "Music stands are in the closet.") {
		t.Fatalf("group event: %s", raw)
	}
}
