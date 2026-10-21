package main

import (
	"errors"
	"fmt"
	"fold/internal/audit"
	"fold/internal/notify"
	"fold/models"
	"net/http"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestNotifications(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", "admin")
	member := newTestUser(t, app, "grier", "password1234", "member")
	// Active group with a meeting in 2 days: no upcoming-event item (DEC-44).
	g := models.Group{Name: "Active", LeaderID: member.ID, Status: models.GroupActive}
	if err := app.DB.Create(&g).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	start := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Hour)
	seedEvent(t, app, &g, member.ID, start)
	app.DB.Create(&models.Membership{UserID: member.ID, GroupID: g.ID, Role: "member", Status: "active", JoinedAt: time.Now().UTC()})

	// A member's new group is a task for the admin.
	memberClient := loginAs(t, app, "grier", "password1234")
	if w := memberClient.do("POST", "/api/v1/groups", map[string]any{"name": "Pending"}, true); w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	adminClient := loginAs(t, app, "admin", "password1234")
	foundApproval := false
	for _, n := range feed(t, adminClient, "") {
		if n.Kind == "group.pending" && n.Waiting && strings.Contains(n.Title, "Pending") {
			foundApproval = true
		}
	}
	if !foundApproval {
		t.Fatalf("admin missing approval notification: %+v", feed(t, adminClient, ""))
	}
	if ns := feed(t, memberClient, ""); len(ns) != 0 {
		t.Fatalf("member has items (upcoming events left the bell): %+v", ns)
	}

	// Unauthenticated → 401.
	anon := newAPIClient(t, app)
	w := anon.do("GET", "/api/v1/notifications", nil, false)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anon notifications: got %d, want 401", w.Code)
	}
}

// M1.1c acceptance: tasks resolve for every admin, inline Approve is the
// only allow-listed action and loses races cleanly, rows are private to
// their owner, archive survives sign-out, and the badge agrees with
// read-all.
func TestStoredNotificationsAcceptance(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "pat", "password1234", models.RoleAdmin)
	newTestUser(t, app, "kim", "password1234", models.RoleAdmin)
	newTestUser(t, app, "sam", "password1234", models.RoleMember)
	pat := loginAPI(t, app, "pat", "password1234")
	kim := loginAPI(t, app, "kim", "password1234")
	sam := loginAPI(t, app, "sam", "password1234")

	gid := createGroupAs(t, sam, "Breakfast", models.VisibilityPublic)
	patTask, kimTask := findKind(feed(t, pat, ""), "group.pending"), findKind(feed(t, kim, ""), "group.pending")
	if patTask == nil || kimTask == nil || !patTask.Waiting || len(patTask.Actions) != 1 || patTask.Actions[0].Key != "approve" {
		t.Fatalf("tasks: %+v %+v", patTask, kimTask)
	}
	if ns := feed(t, sam, ""); len(ns) != 0 {
		t.Fatalf("creator notified about their own group: %+v", ns)
	}

	// Badge: one waiting item; 304 when unchanged.
	code, unread, waiting, tag := badge(t, pat, "")
	if code != http.StatusOK || unread != 0 || waiting != 1 || tag == "" {
		t.Fatalf("badge %d %d %d %q", code, unread, waiting, tag)
	}
	if code, _, _, _ = badge(t, pat, tag); code != http.StatusNotModified {
		t.Fatalf("badge with etag: %d", code)
	}
	// Mark all as read leaves the task counted.
	apiDo(t, pat, "POST", "/api/v1/notifications/read-all", map[string]any{}, http.StatusOK)
	if _, _, waiting, _ = badge(t, pat, ""); waiting != 1 {
		t.Fatalf("read-all cleared the task: %d", waiting)
	}

	// Someone else's row is 404, for every verb.
	for _, p := range []string{"read", "archive"} {
		for _, m := range []string{"POST", "DELETE"} {
			apiDo(t, sam, m, fmt.Sprintf("/api/v1/notifications/%d/%s", patTask.ID, p), nil, http.StatusNotFound)
		}
	}
	// Approving via kim's row with sam's session is refused.
	apiDo(t, kim, "POST", "/api/v1/admin/groups/"+itoa(gid)+"/approve", map[string]any{"fromNotification": patTask.ID}, http.StatusNotFound)
	// Reject is not allow-listed inline.
	apiDo(t, pat, "POST", "/api/v1/admin/groups/"+itoa(gid)+"/reject", map[string]any{"fromNotification": patTask.ID}, http.StatusForbidden)

	// Kim approves inline; Pat's copy is resolved in the same transaction.
	apiDo(t, kim, "POST", "/api/v1/admin/groups/"+itoa(gid)+"/approve", map[string]any{"fromNotification": kimTask.ID}, http.StatusOK)
	got := findKind(feed(t, pat, ""), "group.pending")
	if got == nil || got.Waiting || len(got.Actions) != 0 || got.Resolution == nil || got.Resolution.By != "kim" || got.Resolution.ByMe || got.Resolution.Outcome != "approved" {
		t.Fatalf("pat's copy: %+v", got)
	}
	if _, _, waiting, _ = badge(t, pat, ""); waiting != 0 {
		t.Fatalf("waiting after approve: %d", waiting)
	}
	// Pat's inline Approve lost the race: 409 with who did it.
	body := apiDo(t, pat, "POST", "/api/v1/admin/groups/"+itoa(gid)+"/approve", map[string]any{"fromNotification": patTask.ID}, http.StatusConflict)
	if !strings.Contains(body, `"resolvedBy":"kim"`) || !strings.Contains(body, `"resolution":"approved"`) {
		t.Fatalf("409 body: %s", body)
	}
	var audits int64
	app.DB.Model(&models.AuditLog{}).Where("action = ? AND target_id = ?", audit.GroupApproved, gid).Count(&audits)
	if audits != 1 {
		t.Fatalf("approval audit rows: %d", audits)
	}

	// A non-allow-listed kind can't carry an action: a leadership notice.
	patID := addMemberEmail(t, pat, gid, "pat@example.com")
	apiDo(t, sam, "PUT", fmt.Sprintf("/api/v1/groups/%d/members/%d/role", gid, patID), map[string]any{"role": "leader"}, http.StatusOK)
	lead := findKind(feed(t, kim, ""), "group.leadership_changed")
	if lead == nil || len(lead.Actions) != 0 {
		t.Fatalf("leadership notice: %+v", lead)
	}
	apiDo(t, kim, "POST", "/api/v1/admin/groups/"+itoa(gid)+"/approve", map[string]any{"fromNotification": lead.ID}, http.StatusForbidden)

	// Archive survives sign-out and shows on another session; Undo restores.
	apiDo(t, kim, "POST", fmt.Sprintf("/api/v1/notifications/%d/archive", lead.ID), nil, http.StatusOK)
	kim2 := loginAPI(t, app, "kim", "password1234")
	if findKind(feed(t, kim2, ""), "group.leadership_changed") != nil || findKind(feed(t, kim2, "archived"), "group.leadership_changed") == nil {
		t.Fatal("archive did not carry to a new session")
	}
	apiDo(t, kim2, "DELETE", fmt.Sprintf("/api/v1/notifications/%d/archive", lead.ID), nil, http.StatusOK)
	if findKind(feed(t, kim, ""), "group.leadership_changed") == nil {
		t.Fatal("undo did not restore")
	}
	apiDo(t, kim, "GET", "/api/v1/notifications?filter=nope", nil, http.StatusBadRequest)
}

// A deletion request is a task that resolves when the leader withdraws it,
// and a fresh request is a new task.
func TestDeletionRequestTask(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	newTestUser(t, app, "lead", "password1234", models.RoleMember)
	admin := loginAPI(t, app, "admin", "password1234")
	lead := loginAPI(t, app, "lead", "password1234")
	gid := createGroupAs(t, admin, "Solo", models.VisibilityPublic)
	leadID := addMemberEmail(t, admin, gid, "lead@example.com")
	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/groups/%d/members/%d/role", gid, leadID), map[string]any{"role": "leader"}, http.StatusOK)
	apiDo(t, admin, "POST", "/api/v1/groups/"+itoa(gid)+"/leave", map[string]any{}, http.StatusOK)

	apiDo(t, lead, "POST", "/api/v1/groups/"+itoa(gid)+"/deletion-request", map[string]any{}, http.StatusOK)
	apiDo(t, lead, "POST", "/api/v1/groups/"+itoa(gid)+"/deletion-request", map[string]any{}, http.StatusOK)
	var open int64
	app.DB.Model(&models.Notification{}).Where("kind = 'group.deletion' AND resolved_at IS NULL").Count(&open)
	if open != 1 {
		t.Fatalf("open deletion tasks: %d", open)
	}
	apiDo(t, lead, "DELETE", "/api/v1/groups/"+itoa(gid)+"/deletion-request", nil, http.StatusOK)
	task := findKind(feed(t, admin, ""), "group.deletion")
	if task == nil || task.Waiting || task.Resolution == nil || task.Resolution.Outcome != "withdrawn" || task.Resolution.By != "lead" {
		t.Fatalf("after withdraw: %+v", task)
	}
}

// A deleted person's notifications go with them.
func TestDeletedUserLosesNotifications(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	other := newTestUser(t, app, "other", "password1234", models.RoleAdmin)
	newTestUser(t, app, "sam", "password1234", models.RoleMember)
	admin := loginAPI(t, app, "admin", "password1234")
	createGroupAs(t, loginAPI(t, app, "sam", "password1234"), "Pending", models.VisibilityPublic)
	apiDo(t, admin, "DELETE", fmt.Sprintf("/api/v1/admin/users/%d", other.ID), map[string]any{}, http.StatusOK)
	var n int64
	app.DB.Model(&models.Notification{}).Where("user_id = ?", other.ID).Count(&n)
	if n != 0 {
		t.Fatalf("rows left: %d", n)
	}
}

// Notification writes need the CSRF token like every other write.
func TestNotificationWritesNeedCSRF(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "pat", "password1234", models.RoleAdmin)
	pat := loginAPI(t, app, "pat", "password1234")
	for _, p := range []string{"/api/v1/notifications/read-all", "/api/v1/notifications/archive-read", "/api/v1/notifications/seen"} {
		if w := pat.do("POST", p, map[string]any{"ids": []uint{}}, false); w.Code != http.StatusForbidden {
			t.Fatalf("%s without CSRF: %d", p, w.Code)
		}
	}
}

// M1.2 acceptance: a pending group emails the admins a task, recorded in
// notification_deliveries.
func TestPendingGroupEmailsAdmins(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "root", "password1234", models.RoleAdmin)
	newTestUser(t, app, "sam", "password1234", models.RoleMember)
	sam := loginAPI(t, app, "sam", "password1234")

	_ = createGroupAs(t, sam, "Breakfast", models.VisibilityPublic) // pending
	m := latestEmail(t, app, "root@example.com")
	if !strings.Contains(m.Subject, "started a group") {
		t.Fatalf("subject: %q", m.Subject)
	}
	var n int64
	app.DB.Model(&models.NotificationDelivery{}).
		Where("channel = ? AND status = ?", models.DeliveryEmail, models.DeliverySent).
		Count(&n)
	if n != 1 {
		t.Fatalf("email delivery rows: %d, want 1", n)
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

// recipientZone reads the person's own zone, falls back to the church's,
// and then to UTC (DEC-58).
func TestRecipientZone(t *testing.T) {
	app := testApp(t)
	own := newTestUser(t, app, "own", "password1234", models.RoleMember)
	unset := newTestUser(t, app, "unset", "password1234", models.RoleMember)
	app.DB.Model(own).Update("timezone", "America/New_York")
	if got := recipientZone(app.DB, unset.ID).String(); got != "UTC" {
		t.Fatalf("no church zone: %s, want UTC", got)
	}
	app.DB.Create(&models.Setting{Key: settingTimezone, Value: "America/Los_Angeles"})
	for _, tc := range []struct {
		id   uint
		want string
	}{{own.ID, "America/New_York"}, {unset.ID, "America/Los_Angeles"}, {999999, "America/Los_Angeles"}} {
		if got := recipientZone(app.DB, tc.id).String(); got != tc.want {
			t.Fatalf("user %d: %s, want %s", tc.id, got, tc.want)
		}
	}
}

// A cancellation for a 10:30 PM Los Angeles event reads in each
// recipient's own zone, in the email and in the bell, including the next
// date for New York. A person with no zone of their own gets the church's.
func TestCancellationTimesUseRecipientZone(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "ZoneNotify")
	app.DB.Where(models.Setting{Key: settingTimezone}).Assign(models.Setting{Value: "America/Chicago"}).
		FirstOrCreate(&models.Setting{})
	w := admin.do("POST", "/api/v1/groups/"+gid+"/events", map[string]any{
		"title": "Late", "startsLocal": "2035-07-14T22:30", "durationMinutes": 60, "tzid": "America/Los_Angeles",
	}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Event eventJSON `json:"event"`
	}
	decodeJSON(t, w, &created)
	var g models.Group
	app.DB.Where("id = ?", gid).First(&g)
	people := []struct{ name, zone, want string }{
		{"boise", "America/Boise", "Sat, Jul 14 · 11:30 PM MDT"},
		{"nyc", "America/New_York", "Sun, Jul 15 · 1:30 AM EDT"},
		{"church", "", "Sun, Jul 15 · 12:30 AM CDT"},
	}
	clients := map[string]*apiClient{}
	for _, p := range people {
		u, c := mkUser(t, app, &g, p.name)
		app.DB.Model(u).Update("timezone", p.zone)
		if code, body := registerAs(c, created.Event.ID, "2035-07-14", "going", nil); code != http.StatusOK {
			t.Fatalf("%s going: %d %s", p.name, code, body)
		}
		clients[p.name] = c
	}
	if w := admin.do("POST", fmt.Sprintf("/api/v1/events/%d/cancel", created.Event.ID),
		map[string]any{"date": "2035-07-14"}, true); w.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	for _, p := range people {
		m, err := devOutbox(t, app).LatestTo(testEmail(p.name))
		if err != nil || !strings.Contains(m.Body, p.want) {
			t.Fatalf("%s email: want %q in %q (%v)", p.name, p.want, m.Body, err)
		}
		n := findKind(feed(t, clients[p.name], ""), "event.cancelled")
		if n == nil || !strings.Contains(n.Body, p.want) {
			t.Fatalf("%s bell: want %q in %+v", p.name, p.want, n)
		}
	}
}

// Cancelling an occurrence writes one unbatched bell item and one email
// per going or waitlisted person. A maybe person gets neither.
func TestOccurrenceCancellationNotifiesGoingAndWaitlisted(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "CancelNotify")
	eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(1), true)

	var g models.Group
	app.DB.Where("id = ?", gid).First(&g)
	mk := func(name string) (*models.User, *apiClient) {
		u := newTestUser(t, app, name, "password1234", models.RoleMember)
		addMember(t, app, u, &g, models.MembershipMember)
		return u, loginAPI(t, app, name, "password1234")
	}
	_, cGoing := mk("n1")
	_, cWait := mk("n2")
	_, cMaybe := mk("n3")
	if code, _ := registerAs(cGoing, eventID, date, "going", nil); code != http.StatusOK {
		t.Fatal("n1 going")
	}
	if code, _ := registerAs(cWait, eventID, date, "going", nil); code != http.StatusOK {
		t.Fatal("n2 going->waitlisted")
	}
	if code, _ := registerAs(cMaybe, eventID, date, "maybe", nil); code != http.StatusOK {
		t.Fatal("n3 maybe")
	}

	w := admin.do("POST", fmt.Sprintf("/api/v1/events/%d/cancel", eventID),
		map[string]any{"date": date}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	for _, tc := range []struct {
		c    *apiClient
		name string
		want int
	}{
		{cGoing, "going", 1}, {cWait, "waitlisted", 1}, {cMaybe, "maybe", 0},
	} {
		got := findKind(feed(t, tc.c, ""), "event.cancelled")
		if tc.want == 0 && got != nil {
			t.Fatalf("%s person got a cancellation notice", tc.name)
		}
		if tc.want == 1 && got == nil {
			t.Fatalf("no cancellation notice for %s person", tc.name)
		}
	}
	// One email per notified person in the devoutbox.
	sent := 0
	for _, to := range []string{testEmail("n1"), testEmail("n2"), testEmail("n3")} {
		if _, err := devOutbox(t, app).LatestTo(to); err == nil {
			sent++
		}
	}
	if sent != 2 {
		t.Fatalf("cancellation emails sent = %d, want 2", sent)
	}
}

// A time change and a place change each write one event.changed item per
// going or waitlisted person.
func TestOccurrenceTimeAndPlaceChangesNotify(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "ChangedNotify")
	eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(10), true)

	var g models.Group
	app.DB.Where("id = ?", gid).First(&g)
	_, cGoing := mkUser(t, app, &g, "c1")
	_, cWait := mkUser(t, app, &g, "c2")
	if code, _ := registerAs(cGoing, eventID, date, "going", nil); code != http.StatusOK {
		t.Fatal("c1 going")
	}
	// Fill the event so c2 waitlists: capacity 10, add 9 more goings.
	for i := 0; i < 9; i++ {
		_, cc := mkUser(t, app, &g, fmt.Sprintf("filler%d", i))
		if code, _ := registerAs(cc, eventID, date, "going", nil); code != http.StatusOK {
			t.Fatalf("filler %d: %d", i, code)
		}
	}
	if code, _ := registerAs(cWait, eventID, date, "going", nil); code != http.StatusOK {
		t.Fatal("c2 going->waitlisted")
	}

	// Time change on the date.
	w := admin.do("PUT", fmt.Sprintf("/api/v1/events/%d/occurrences/%s", eventID, date),
		map[string]any{"startsLocal": date + "T21:00"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("time change: %d %s", w.Code, w.Body.String())
	}
	// Place change on the date.
	w = admin.do("PUT", fmt.Sprintf("/api/v1/events/%d/occurrences/%s", eventID, date),
		map[string]any{"location": "New Hall"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("place change: %d %s", w.Code, w.Body.String())
	}
	for _, tc := range []struct {
		c    *apiClient
		name string
	}{{cGoing, "going"}, {cWait, "waitlisted"}} {
		var changed int
		for _, n := range feed(t, tc.c, "") {
			if n.Kind == "event.changed" {
				changed++
			}
		}
		if changed != 2 {
			t.Fatalf("%s person: %d event.changed items, want 2 (time + place)", tc.name, changed)
		}
	}
}

// A series edit that moves a date fans out event.changed (time) for that
// date only; frozen dates are left alone.
func TestSeriesEditMovesNotifyChanged(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "SeriesChanged")
	start := time.Now().UTC().AddDate(0, 0, 30).Truncate(24 * time.Hour).Add(18 * time.Hour)
	w := admin.do("POST", "/api/v1/groups/"+gid+"/events", map[string]any{
		"title": "Weekly", "startsLocal": start.Format("2006-01-02T15:04"),
		"durationMinutes": 60, "tzid": "UTC",
		"recurrenceRule": "FREQ=WEEKLY;BYDAY=" + weekdayCode(start.Format("2006-01-02")),
		"capacity":       10, "waitlistEnabled": true,
	}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Event eventJSON `json:"event"`
	}
	decodeJSON(t, w, &created)
	eventID := created.Event.ID

	var g models.Group
	app.DB.Where("id = ?", gid).First(&g)
	_, c1 := mkUser(t, app, &g, "s1")
	d1 := start.Format("2006-01-02")
	d2 := start.AddDate(0, 0, 7).Format("2006-01-02")
	if code, _ := registerAs(c1, eventID, d1, "going", nil); code != http.StatusOK {
		t.Fatal("s1 going d1")
	}
	if code, _ := registerAs(c1, eventID, d2, "going", nil); code != http.StatusOK {
		t.Fatal("s1 going d2")
	}
	// Move the series start time: both future dates move.
	w = admin.do("PUT", fmt.Sprintf("/api/v1/events/%d", eventID),
		map[string]any{"startsLocal": start.Add(time.Hour).Format("2006-01-02T15:04")}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("series move: %d %s", w.Code, w.Body.String())
	}
	var changed int
	for _, n := range feed(t, c1, "") {
		if n.Kind == "event.changed" {
			changed++
		}
	}
	if changed != 2 {
		t.Fatalf("event.changed items = %d, want 2 (one per moved date)", changed)
	}
}

func mkUser(t *testing.T, app *App, g *models.Group, name string) (*models.User, *apiClient) {
	t.Helper()
	u := newTestUser(t, app, name, "password1234", models.RoleMember)
	addMember(t, app, u, g, models.MembershipMember)
	return u, loginAPI(t, app, name, "password1234")
}

func weekdayCode(date string) string {
	// date is YYYY-MM-DD; return the iCal weekday code.
	d, _ := time.Parse("2006-01-02", date)
	return []string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}[d.Weekday()]
}

// Cancelling a date with 30 registrants must not re-read the occurrence,
// event, group, membership and zone per person (issue #76: 276 reads).
// The notices still say the same thing: a member gets the group's name,
// place and link in their own zone; an outsider gets none of them.
func TestCancelDateNoticeReadsStayBounded(t *testing.T) {
	app := testApp(t)
	app.DB.Create(&models.Setting{Key: settingTimezone, Value: "America/Chicago"})
	leader := newTestUser(t, app, "leader", "password1234", models.RoleMember)
	g := activeGroup(t, app, leader, "Query Group", models.VisibilityInviteOnly, "")
	if err := app.DB.Model(&g).Update("location", "Secret Hall").Error; err != nil {
		t.Fatal(err)
	}
	g.Location = "Secret Hall"
	start := time.Date(2035, 7, 14, 22, 30, 0, 0, time.UTC)
	ev, occ := seedEvent(t, app, &g, leader.ID, start)

	users := make([]models.User, 30)
	for i := range users {
		users[i] = models.User{
			Email: fmt.Sprintf("reg%d@example.com", i), Name: fmt.Sprintf("Reg %d", i),
			Role: models.RoleMember, Status: models.UserActive,
		}
		if i == 0 {
			users[i].Timezone = "America/New_York"
		}
		if err := app.DB.Create(&users[i]).Error; err != nil {
			t.Fatalf("create registrant %d: %v", i, err)
		}
		if i < 29 { // the last registrant is not a member of the invite-only group
			addMember(t, app, &users[i], &g, models.MembershipMember)
		}
		if err := app.DB.Create(&models.Registration{
			UserID: users[i].ID, OccurrenceID: occ.ID,
			Status: models.RegGoing, Source: models.RegSourceWeb,
		}).Error; err != nil {
			t.Fatalf("register %d: %v", i, err)
		}
	}

	counter := countDBReads(t, app.DB)
	var ids []uint
	err := app.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.EventOccurrence{}).Where("id = ?", occ.ID).
			Update("status", models.OccurrenceCancelled).Error; err != nil {
			return err
		}
		var err error
		ids, err = notifyOccurrencesCancelled(tx, leader.ID, []uint{occ.ID})
		return err
	})
	reads := counter.Reads()
	counter.Stop()
	if err != nil {
		t.Fatalf("cancel date: %v", err)
	}
	t.Logf("cancel-date reads for 30 registrants: %d", reads)
	if reads > 20 {
		t.Fatalf("cancel-date notices used %d reads for 30 registrants, want <= 20", reads)
	}
	if len(ids) != 30 {
		t.Fatalf("notices sent = %d, want 30", len(ids))
	}

	// Emailing them after commit reads the rows and their recipients once
	// (two reads), not twice per person.
	counter = countDBReads(t, app.DB)
	app.deliverNotificationEmails(ids)
	mailReads := counter.Reads()
	counter.Stop()
	t.Logf("cancel-date reads %d + email delivery reads %d", reads, mailReads)
	if mailReads > 2 || reads+mailReads > 12 {
		t.Fatalf("cancel plus delivery used %d + %d reads for 30 registrants, want delivery <= 2 and total <= 12", reads, mailReads)
	}

	var notes []models.Notification
	if err := app.DB.Where("kind = ?", "event.cancelled").Find(&notes).Error; err != nil {
		t.Fatal(err)
	}
	if len(notes) != 30 {
		t.Fatalf("notification rows = %d, want 30", len(notes))
	}
	byUser := map[uint]models.Notification{}
	for _, n := range notes {
		byUser[n.UserID] = n
	}
	member := byUser[users[0].ID]
	if member.Title != "Cancelled: Query Group" || member.Link != fmt.Sprintf("/groups/%d", g.ID) ||
		!strings.Contains(member.Body, "Sat, Jul 14 · 6:30 PM EDT") || !strings.Contains(member.Body, "Secret Hall") {
		t.Fatalf("member notice: %+v", member)
	}
	outsider := byUser[users[29].ID]
	if outsider.Title != "Cancelled: Church event" || outsider.Link != fmt.Sprintf("/events/%d", ev.ID) ||
		!strings.Contains(outsider.Body, "Sat, Jul 14 · 5:30 PM CDT") ||
		strings.Contains(outsider.Body, "Secret Hall") || strings.Contains(outsider.Title, "Query Group") {
		t.Fatalf("outsider notice: %+v", outsider)
	}
}

// When the recipients can't be read, delivery stops without recording
// anything: no email, no "suppressed" row, so the notification stays
// pending for a later pass (Whittler on #87).
func TestDeliveryRecipientsReadFailureLeavesRowsPending(t *testing.T) {
	app := testApp(t)
	u := newTestUser(t, app, "pat", "password1234", models.RoleMember)
	n := models.Notification{
		UserID: u.ID, Kind: notify.OccurrenceCancelled, Category: notify.CatEvents,
		Title: "Cancelled", EmailTitle: "Cancelled", Link: "/events/1",
	}
	if err := app.DB.Create(&n).Error; err != nil {
		t.Fatal(err)
	}
	const name = "test:fail_users_read"
	if err := app.DB.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			_ = tx.AddError(errors.New("users table unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	app.deliverNotificationEmails([]uint{n.ID})
	_ = app.DB.Callback().Query().Remove(name)

	var deliveries int64
	app.DB.Model(&models.NotificationDelivery{}).Where("notification_id = ?", n.ID).Count(&deliveries)
	if deliveries != 0 {
		t.Fatalf("recipients read failed but %d delivery rows were written", deliveries)
	}
	var sent int64
	app.DB.Model(&models.MailOutbox{}).Count(&sent)
	if sent != 0 {
		t.Fatalf("recipients read failed but %d emails went out", sent)
	}
	// The row was left pending: the next pass sends it.
	app.deliverNotificationEmails([]uint{n.ID})
	app.DB.Model(&models.MailOutbox{}).Count(&sent)
	if sent != 1 {
		t.Fatalf("retry after the failure sent %d emails, want 1", sent)
	}
}

// Time and place changes email each going or waitlisted person too, not
// only the bell.
func TestOccurrenceChangeEmailsGoingAndWaitlisted(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "ChangeMail")
	eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(1), true)
	var g models.Group
	app.DB.Where("id = ?", gid).First(&g)
	_, c1 := mkUser(t, app, &g, "cm1")
	u2, c2 := mkUser(t, app, &g, "cm2")
	_, c3 := mkUser(t, app, &g, "cm3")
	registerAs(c1, eventID, date, "going", nil)
	registerAs(c2, eventID, date, "going", nil)
	var occ models.EventOccurrence
	app.DB.Where("event_id = ?", eventID).First(&occ)
	if got := regStatus(t, app, u2.ID, occ.ID); got != models.RegWaitlisted {
		t.Fatalf("cm2 = %q, want waitlisted", got)
	}
	registerAs(c3, eventID, date, "maybe", nil)
	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d/occurrences/%s", eventID, date),
		map[string]any{"location": "Annex"}, http.StatusOK)
	for _, tc := range []struct {
		name string
		want bool
	}{{"cm1", true}, {"cm2", true}, {"cm3", false}} {
		m, err := devOutbox(t, app).LatestTo(testEmail(tc.name))
		got := err == nil && strings.HasPrefix(m.Subject, "Updated:")
		if got != tc.want {
			t.Fatalf("%s change email = %v, want %v (%+v)", tc.name, got, tc.want, m)
		}
	}
}

// Event items expire at the occurrence end, through the real API path,
// and then show under Archived only.
func TestEventItemsArchiveAfterOccurrenceEnds(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "Expire")
	eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(5), true)
	var g models.Group
	app.DB.Where("id = ?", gid).First(&g)
	u, c := mkUser(t, app, &g, "ex1")
	registerAs(c, eventID, date, "going", nil)
	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d/occurrences/%s", eventID, date),
		map[string]any{"location": "Annex"}, http.StatusOK)
	var occ models.EventOccurrence
	app.DB.Where("event_id = ?", eventID).First(&occ)
	var row models.Notification
	if err := app.DB.Where("user_id = ? AND kind = ?", u.ID, "event.changed").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.ExpiresAt == nil || !row.ExpiresAt.Equal(occ.EndsAt) {
		t.Fatalf("expires_at = %v, want occurrence end %v", row.ExpiresAt, occ.EndsAt)
	}
	if findKind(feed(t, c, ""), "event.changed") == nil {
		t.Fatal("not in the inbox before the end")
	}
	// Move the clock past the end by moving the expiry into the past.
	past := time.Now().Add(-time.Minute)
	app.DB.Model(&models.Notification{}).Where("id = ?", row.ID).Update("expires_at", past)
	if findKind(feed(t, c, ""), "event.changed") != nil {
		t.Fatal("still in the inbox after the end")
	}
	if findKind(feed(t, c, "archived"), "event.changed") == nil {
		t.Fatal("not under Archived after the end")
	}
	var kept int64
	app.DB.Model(&models.Notification{}).Where("id = ?", row.ID).Count(&kept)
	if kept != 1 {
		t.Fatal("expired item was deleted, want archived")
	}
}
