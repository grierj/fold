package main

import (
	"errors"
	"fmt"
	"fold/internal/audit"
	"fold/internal/mail"
	"fold/internal/notify"
	"fold/internal/policy"
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
	memberClient := loginAPI(t, app, "grier", "password1234")
	if w := memberClient.do("POST", "/api/v1/groups", map[string]any{"name": "Pending"}, true); w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	adminClient := loginAPI(t, app, "admin", "password1234")
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
	if w := confirmDeparture(t, admin, "POST", "/api/v1/groups/"+itoa(gid)+"/leave", "/api/v1/groups/"+itoa(gid)+"/departure-preview", ""); w.Code != http.StatusOK {
		t.Fatalf("leave: %d %s", w.Code, w.Body.String())
	}

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
	for _, p := range []string{"/api/v1/notifications/read-all", "/api/v1/notifications/archive-read"} {
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
	// The provider's id for the message is kept for its delivery and bounce
	// reports (M3.2): the devoutbox row here.
	var d models.NotificationDelivery
	app.DB.Where("channel = ? AND status = ?", models.DeliveryEmail, models.DeliverySent).First(&d)
	if d.ProviderMessageID == nil || *d.ProviderMessageID != fmt.Sprintf("devoutbox-%d", m.ID) {
		t.Fatalf("provider message id: %v, want devoutbox-%d", d.ProviderMessageID, m.ID)
	}
}

// A member's new group gives every admin its own task, resolved when it is
// approved; the next group is a new row, so nothing earlier can hide it.
func TestMemberGroupNotifiesAdmins(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	newTestUser(t, app, "sam", "password1234", models.RoleMember)
	admin := loginAPI(t, app, "admin", "password1234")
	sam := loginAPI(t, app, "sam", "password1234")

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
	zone := func(id uint) string {
		loc, err := recipientZone(app.DB, id)
		if err != nil {
			t.Fatal(err)
		}
		return loc.String()
	}
	if got := zone(unset.ID); got != "UTC" {
		t.Fatalf("no church zone: %s, want UTC", got)
	}
	app.DB.Create(&models.Setting{Key: settingTimezone, Value: "America/Los_Angeles"})
	for _, tc := range []struct {
		id   uint
		want string
	}{{own.ID, "America/New_York"}, {unset.ID, "America/Los_Angeles"}, {999999, "America/Los_Angeles"}} {
		if got := zone(tc.id); got != tc.want {
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
			UserID: &users[i].ID, OccurrenceID: occ.ID,
			Status: models.RegGoing, Source: models.RegSourceWeb,
		}).Error; err != nil {
			t.Fatalf("register %d: %v", i, err)
		}
	}

	before := maxNoticeID(t, app.DB)
	counter := countDBReads(t, app.DB)
	err := app.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.EventOccurrence{}).Where("id = ?", occ.ID).
			Update("status", models.OccurrenceCancelled).Error; err != nil {
			return err
		}
		return notifyOccurrences(tx, leader.ID, []uint{occ.ID}, notify.OccurrenceCancelled)
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
	ids := noticeIDsAfter(t, app.DB, before)
	if len(ids) != 30 {
		t.Fatalf("notices sent = %d, want 30", len(ids))
	}

	// Delivery now runs once per notice. Keep the cancellation query bound
	// above, and check the durable fan-out and outcomes separately.
	var queued int64
	if err := app.DB.Model(&models.NotificationDelivery{}).
		Where("notification_id IN ? AND channel = ? AND status = ?", ids, models.DeliveryEmail, models.DeliveryQueued).Count(&queued).Error; err != nil {
		t.Fatal(err)
	}
	if queued != 30 {
		t.Fatalf("queued delivery intents = %d, want 30", queued)
	}
	drainNotices(t, app)
	var sent int64
	if err := app.DB.Model(&models.NotificationDelivery{}).
		Where("notification_id IN ? AND channel = ? AND status = ?", ids, models.DeliveryEmail, models.DeliverySent).Count(&sent).Error; err != nil {
		t.Fatal(err)
	}
	if sent != 30 {
		t.Fatalf("sent deliveries = %d, want 30", sent)
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

// A failed recipient read leaves durable queued intent and a failed job.
// Making that job due again sends without a caller retaining the notice ID.
func TestDeliveryRecipientsReadFailureLeavesRowsPending(t *testing.T) {
	app := testApp(t)
	u := newTestUser(t, app, "pat", "password1234", models.RoleMember)
	id := sendNotice(t, app, notify.OccurrenceCancelled, u)
	const name = "test:fail_users_read"
	if err := app.DB.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			_ = tx.AddError(errors.New("users table unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.DB.Callback().Query().Remove(name) })
	drainNotices(t, app)
	_ = app.DB.Callback().Query().Remove(name)
	if d := emailDeliveryOf(t, app, id); d.Status != models.DeliveryQueued {
		t.Fatalf("recipient read failed but delivery is %+v", d)
	}
	var job models.Job
	if err := app.DB.Where("unique_key = ?", notify.DeliverKey(id)).First(&job).Error; err != nil {
		t.Fatal(err)
	}
	if job.Status != models.JobFailed || job.Attempts != 1 || !strings.Contains(job.LastError, "users table unavailable") {
		t.Fatalf("read failure job: %+v", job)
	}
	if n := mailCount(t, app, ""); n != 0 {
		t.Fatalf("recipient read failed but %d emails went out", n)
	}
	if err := app.DB.Model(&job).Update("run_at", time.Now()).Error; err != nil {
		t.Fatal(err)
	}
	drainNotices(t, app)
	if n := mailCount(t, app, ""); n != 1 {
		t.Fatalf("retry sent %d emails, want 1", n)
	}
	if d := emailDeliveryOf(t, app, id); d.Status != models.DeliverySent {
		t.Fatalf("retried delivery: %+v", d)
	}
	if got := reloadJob(t, app, job.ID); got.Status != models.JobDone || got.Attempts != 2 {
		t.Fatalf("retried job: %+v", got)
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

func TestHouseholdEventNoticesDeduplicateByHolder(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "FamilyNotices")
	eventID, date := seedCappedEvent(t, app, admin, gid, nil, true)
	holder, c, spouse, child := householdFamily(t, app, admin, "noticeholder")
	batchAnswer(t, c, eventID, date, map[string]any{"people": []map[string]any{{"personId": spouse.ID, "status": "going"}, {"personId": child.ID, "status": "going"}}}, http.StatusOK)
	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d/occurrences/%s", eventID, date), map[string]any{"location": "New place"}, http.StatusOK)
	var notices []models.Notification
	app.DB.Where("kind = ?", notify.EventChanged).Find(&notices)
	if len(notices) != 1 || notices[0].UserID != holder.ID {
		t.Fatalf("event notices %+v", notices)
	}
	if got := noticeDataDate(t, notices[0]); got != date {
		t.Fatalf("event notice date = %q, want %q", got, date)
	}
	apiDo(t, admin, "POST", fmt.Sprintf("/api/v1/events/%d/cancel", eventID), map[string]any{"date": date}, http.StatusOK)
	notices = nil
	app.DB.Where("kind = ?", notify.OccurrenceCancelled).Find(&notices)
	if len(notices) != 1 || notices[0].UserID != holder.ID {
		t.Fatalf("cancellation notices %+v", notices)
	}
	if got := noticeDataDate(t, notices[0]); got != date {
		t.Fatalf("cancellation notice date = %q, want %q", got, date)
	}
}

func TestNotificationFeedReadBudgetAndResolverNames(t *testing.T) {
	app := testApp(t)
	me := newTestUser(t, app, "reader", "pw", models.RoleMember)
	resolver := newTestUser(t, app, "resolver", "pw", models.RoleAdmin)
	if err := app.DB.Model(resolver).Update("display_name", "Pat").Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Delete(resolver).Error; err != nil {
		t.Fatal(err)
	}
	client := loginAPI(t, app, "reader", "pw")
	now := time.Now().UTC()
	var baseline int64
	for i := 1; i <= 30; i++ {
		n := models.Notification{UserID: me.ID, Kind: "group.pending", Category: "admin", Title: "Handled", Link: "/admin",
			ResolvedAt: &now, ResolvedByID: &resolver.ID, Resolution: "approved"}
		if err := app.DB.Create(&n).Error; err != nil {
			t.Fatal(err)
		}
		if i != 3 && i != 30 {
			continue
		}
		counter := countDBReads(t, app.DB)
		w := client.do("GET", "/api/v1/notifications?filter=all&limit=40", nil, false)
		reads := counter.Reads()
		counter.Stop()
		if w.Code != http.StatusOK {
			t.Fatalf("feed: %d %s", w.Code, w.Body.String())
		}
		var body struct {
			Notifications []notificationJSON `json:"notifications"`
		}
		decodeJSON(t, w, &body)
		if len(body.Notifications) != i {
			t.Fatalf("got %d notifications, want %d", len(body.Notifications), i)
		}
		for _, n := range body.Notifications {
			if n.Resolution == nil || n.Resolution.By != "Pat" || n.Resolution.ByMe {
				t.Fatalf("historical resolver: %+v", n)
			}
		}
		if i == 3 {
			baseline = reads
		}
		if reads != baseline || reads > 6 {
			t.Fatalf("%d notifications: %d reads, baseline=%d, cap=6", i, reads, baseline)
		}
		t.Logf("%d notifications: %d reads", i, reads)
	}
	missing := uint(999999)
	names := publicNamesIn(app.DB, []uint{missing, me.ID})
	for _, tt := range []struct {
		id   *uint
		by   string
		byMe bool
	}{
		{nil, "Fold", false}, {&missing, "Someone", false}, {&me.ID, me.PublicName(), true},
	} {
		n := models.Notification{ResolvedAt: &now, ResolvedByID: tt.id}
		got := toNotificationJSON(&n, me.ID, names)
		if got.Resolution.By != tt.by || got.Resolution.ByMe != tt.byMe {
			t.Fatalf("resolution: %+v, want %+v", got.Resolution, tt)
		}
	}
}

func TestPublicNamesInBatches(t *testing.T) {
	app := testApp(t)
	active := newTestUser(t, app, "active name", "pw", models.RoleMember)
	deleted := newTestUser(t, app, "historical name", "pw", models.RoleMember)
	if err := app.DB.Model(deleted).Update("display_name", "Historical display").Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Delete(deleted).Error; err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{0, 1, lookupChunk - 1, lookupChunk, lookupChunk + 1, 2*lookupChunk + 1, 65536} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			ids := make([]uint, size)
			for i := range ids {
				ids[i] = uint(1000000 + i) // Missing people do not require seeded rows.
			}
			if size > 0 {
				ids[0] = active.ID
			}
			if size > 1 {
				ids[size-1] = deleted.ID
			}
			// Repeated references must not consume more binds or queries.
			input := append(append([]uint{}, ids...), ids...)
			counter := countDBReads(t, app.DB)
			names := publicNamesIn(app.DB, input)
			reads := counter.Reads()
			counter.Stop()
			if want := int64((size + lookupChunk - 1) / lookupChunk); reads != want {
				t.Fatalf("%d unique IDs: %d reads, want %d", size, reads, want)
			}
			if len(names) != size {
				t.Fatalf("%d names, want %d", len(names), size)
			}
			for _, id := range ids {
				want := "Someone"
				if id == active.ID {
					want = active.PublicName()
				} else if id == deleted.ID {
					want = "Historical display"
				}
				if got := names.name(id); got != want {
					t.Fatalf("id %d: name %q, want %q", id, got, want)
				}
			}
		})
	}
}

func TestPublicNamesInBatchFailureUsesOnlyFallbacks(t *testing.T) {
	app := testApp(t)
	person := newTestUser(t, app, "historical actor", "pw", models.RoleMember)
	ids := make([]uint, 2*lookupChunk+1)
	for i := range ids {
		ids[i] = uint(1000000 + i)
	}
	ids[0] = person.ID
	for _, failedBatch := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(failedBatch), func(t *testing.T) {
			reads := 0
			const callback = "test:fail_public_names_batch"
			if err := app.DB.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table == "users" {
					reads++
					if reads == failedBatch {
						_ = tx.AddError(errors.New("names unavailable"))
					}
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = app.DB.Callback().Query().Remove(callback) })
			names := publicNamesIn(app.DB, ids)
			_ = app.DB.Callback().Query().Remove(callback)
			if reads != failedBatch {
				t.Fatalf("continued after failed batch: %d reads, want %d", reads, failedBatch)
			}
			for _, id := range ids {
				if got := names.name(id); got != "Someone" {
					t.Fatalf("batch failure published partial historical name for %d: %q", id, got)
				}
			}
			if got := publicNamesIn(app.DB, ids).name(person.ID); got != person.PublicName() {
				t.Fatalf("fresh lookup did not recover: %q", got)
			}
		})
	}
}

// "Turn off all optional email" (DEC-97) appends one consent row per optional
// category, is idempotent, and stops optional notification email while
// required email (a task waiting on the person) still sends.
func TestOptionalEmailSwitch(t *testing.T) {
	app := testApp(t)
	admin := newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	leader := newTestUser(t, app, "leader", "password1234", models.RoleMember)
	adminClient := loginAPI(t, app, "admin", "password1234")
	lc := loginAPI(t, app, "leader", "password1234")
	settings := func(c *apiClient) bool {
		t.Helper()
		var body struct {
			Settings notificationSettingsJSON `json:"settings"`
		}
		decodeJSON(t, c.do("GET", "/api/v1/me/notification-settings", nil, false), &body)
		return body.Settings.OptionalEmailOff
	}
	set := func(c *apiClient, off bool) {
		t.Helper()
		if w := c.do("PUT", "/api/v1/me/notification-settings", map[string]any{"optionalEmailOff": off}, true); w.Code != http.StatusOK {
			t.Fatalf("PUT %v: %d %s", off, w.Code, w.Body.String())
		}
	}
	if settings(lc) {
		t.Fatal("optional email starts on")
	}
	if w := lc.do("PUT", "/api/v1/me/notification-settings", map[string]any{}, true); w.Code != http.StatusBadRequest {
		t.Fatalf("missing field: %d", w.Code)
	}
	set(lc, true)
	set(lc, true)
	if !settings(lc) {
		t.Fatal("switch didn't stick")
	}
	// Every optional kind, even admin notices for a member (DEC-97).
	optional := len(notify.OptionalEmailCategories())
	if n := contactCount(t, app, &models.Consent{}, "user_id = ? AND channel = 'email' AND status = 'opted_out' AND source = 'web_form'", leader.ID); n != int64(optional) {
		t.Fatalf("%d opt-out rows, want one per optional category (%d), even after a repeat", n, optional)
	}

	// An optional kind (the leader's group was approved) is suppressed...
	var body struct {
		Group groupSummaryJSON `json:"group"`
	}
	decodeJSON(t, lc.do("POST", "/api/v1/groups", map[string]any{"name": "Quiet group"}, true), &body)
	if w := adminClient.do("POST", "/api/v1/admin/groups/"+itoa(body.Group.ID)+"/approve", map[string]any{}, true); w.Code != http.StatusOK {
		t.Fatalf("approve: %d", w.Code)
	}
	var approved models.Notification
	if err := app.DB.Where("user_id = ? AND kind = ?", leader.ID, notify.GroupApproved).First(&approved).Error; err != nil {
		t.Fatal(err)
	}
	if n := contactCount(t, app, &models.NotificationDelivery{}, "notification_id = ? AND status = ? AND reason = ?", approved.ID, models.DeliverySuppressed, "optional email turned off"); n != 1 {
		t.Fatalf("optional email not suppressed: %d", n)
	}

	// ...while required email still sends: an admin who turned optional
	// email off still gets the new-group task.
	set(adminClient, true)
	decodeJSON(t, lc.do("POST", "/api/v1/groups", map[string]any{"name": "Second group"}, true), &body)
	var task models.Notification
	if err := app.DB.Where("user_id = ? AND kind = ?", admin.ID, notify.GroupPending).Order("id DESC").First(&task).Error; err != nil {
		t.Fatal(err)
	}
	if n := contactCount(t, app, &models.NotificationDelivery{}, "notification_id = ? AND status = ?", task.ID, models.DeliverySent); n != 1 {
		t.Fatalf("required task email not sent: %d", n)
	}

	set(lc, false)
	if settings(lc) {
		t.Fatal("switch didn't turn back on")
	}
	if n := contactCount(t, app, &models.Consent{}, "user_id = ? AND status = 'opted_in'", leader.ID); n != int64(optional) {
		t.Fatalf("%d opt-in rows, want %d", n, optional)
	}

	// Back on, the latest row wins: the next optional email is sent.
	decodeJSON(t, lc.do("POST", "/api/v1/groups", map[string]any{"name": "Loud group"}, true), &body)
	if w := adminClient.do("POST", "/api/v1/admin/groups/"+itoa(body.Group.ID)+"/reject", map[string]any{}, true); w.Code != http.StatusOK {
		t.Fatalf("reject: %d", w.Code)
	}
	var rejected models.Notification
	if err := app.DB.Where("user_id = ? AND kind = ?", leader.ID, notify.GroupRejected).First(&rejected).Error; err != nil {
		t.Fatal(err)
	}
	if n := contactCount(t, app, &models.NotificationDelivery{}, "notification_id = ? AND status = ?", rejected.ID, models.DeliverySent); n != 1 {
		t.Fatalf("optional email after turning it back on: %d sent", n)
	}

	// Consent history is evidence: it can't be edited or removed.
	if err := app.DB.Exec("UPDATE consents SET status = 'opted_in' WHERE user_id = ?", leader.ID).Error; err == nil {
		t.Fatal("consent rows accepted an UPDATE")
	}
	if err := app.DB.Exec("DELETE FROM consents WHERE user_id = ?", leader.ID).Error; err == nil {
		t.Fatal("consent rows accepted a DELETE")
	}
}

// The person whose role changed hears it (DEC-134): promoted, "You're now
// a leader"; demoted by someone else, "You're no longer a leader". Their
// own step down tells them nothing. The admins' notice stays, and the
// person's notice reaches their phone.
func TestYourRoleNoticeOnPromoteAndDemote(t *testing.T) {
	app, f := pushTestApp(t)
	newTestUser(t, app, "admin", "password123456", models.RoleAdmin)
	patUser := newTestUser(t, app, "pat", "password123456", models.RoleMember)
	g := newTestGroup(t, app, patUser, "Supper club", models.GroupActive)
	app.DB.Model(g).Update("visibility", models.VisibilityInviteOnly)
	sam := pushMember(t, app, "sam")
	addMember(t, app, sam, g, models.MembershipMember)
	phone := newPushDevice(t, app, sam, "sam-phone")
	pat := loginAPI(t, app, "pat", "password123456")
	path := "/api/v1/groups/" + itoa(g.ID) + "/members/" + itoa(sam.ID) + "/role"
	mine := func() []models.Notification {
		var out []models.Notification
		app.DB.Where("user_id = ? AND kind = ?", sam.ID, notify.YourRole).Order("id").Find(&out)
		return out
	}

	apiDo(t, pat, "PUT", path, map[string]any{"role": "leader"}, http.StatusOK)
	notes := mine()
	if len(notes) != 1 || notes[0].Title != "You're now a leader of Supper club" || notes[0].Body != "You can manage its members and events in Fold." ||
		notes[0].Link != "/groups/"+itoa(g.ID) || notes[0].Category != notify.CatGroups {
		t.Fatalf("promote: %+v", notes)
	}
	drainJobs(t, app)
	if f.count(phone.path) != 1 || notes[0].PushTitle != notes[0].Title {
		t.Fatalf("promote push: %d sends, push title %q", f.count(phone.path), notes[0].PushTitle)
	}

	apiDo(t, pat, "PUT", path, map[string]any{"role": "member"}, http.StatusOK)
	notes = mine()
	if len(notes) != 2 || notes[1].Title != "You're no longer a leader of Supper club" || notes[1].Body != "Thanks for leading. You're still a member." {
		t.Fatalf("demote: %+v", notes)
	}
	var adminNotices int64
	app.DB.Model(&models.Notification{}).Where("kind = ?", notify.LeadershipChanged).Count(&adminNotices)
	if adminNotices != 2 {
		t.Fatalf("%d admin notices, want one per change", adminNotices)
	}

	// Stepping down yourself says nothing to you.
	apiDo(t, pat, "PUT", path, map[string]any{"role": "leader"}, http.StatusOK)
	samClient := loginAPI(t, app, "sam", "password123456")
	apiDo(t, samClient, "PUT", path, map[string]any{"role": "member"}, http.StatusOK)
	if n := len(mine()); n != 3 {
		t.Fatalf("%d notices after stepping down yourself, want 3", n)
	}
}

// The notification email read joins each account with its open reasons,
// hashed or legacy, in ascending suppression id (R4-S3).
func TestReadEmailRecipientsSuppressions(t *testing.T) {
	app := testApp(t)
	users := suppressionFixtureUsers(t, app, "hashed", "legacy", "cleared", "complaintfirst", "bouncefirst", "clean")
	app.DB.Model(users["legacy"]).Update("email", "Legacy@Example.com ")
	suppressHashed(t, app, "hashed@example.com", models.SuppressHardBounce)
	suppress(t, app, "legacy@example.com", models.SuppressManual)
	h := mail.StoredAddressHash("cleared@example.com")
	now := time.Now()
	if err := app.DB.Create(&models.Suppression{Channel: "email", Address: "cleared@example.com", AddressHash: &h,
		Reason: models.SuppressHardBounce, ClearedAt: &now}).Error; err != nil {
		t.Fatal(err)
	}
	suppressHashed(t, app, "complaintfirst@example.com", models.SuppressComplaint)
	suppressHashed(t, app, "complaintfirst@example.com", models.SuppressHardBounce)
	suppress(t, app, "bouncefirst@example.com", models.SuppressHardBounce)
	suppress(t, app, "bouncefirst@example.com", models.SuppressComplaint)

	ids := []uint{}
	for _, u := range users {
		ids = append(ids, u.ID)
	}
	rows, _, err := readEmailRecipients(app.DB, ids, true)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range rows {
		got[r.Name] = r.EmailSuppressions
	}
	want := map[string]string{
		"hashed": "hard_bounce", "legacy": "manual", "cleared": "", "clean": "",
		"complaintfirst": "complaint,hard_bounce", "bouncefirst": "hard_bounce,complaint",
	}
	if len(got) != len(want) {
		t.Fatalf("rows = %v", got)
	}
	for name, reasons := range want {
		if got[name] != reasons {
			t.Errorf("%s: %q, want %q", name, got[name], reasons)
		}
	}
	// The single-account read splits the same string.
	_, _, reasons, _, err := readEmailRecipient(app.DB, users["bouncefirst"].ID, true)
	if err != nil || strings.Join(reasons, ",") != "hard_bounce,complaint" {
		t.Fatalf("readEmailRecipient: %v, %v", reasons, err)
	}
}

// A notice about a teen or a child carries a Teen or Child badge for the
// people who already see them, never for the person themself, and a person
// with no access to the subject gets no row to badge at all (DEC-20, DEC-150).
func TestNotificationsBadgeTeenAndChildSubjects(t *testing.T) {
	t.Run("account requests badge a teen for admins only", func(t *testing.T) {
		app := testApp(t)
		newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
		admin := loginAPI(t, app, "admin", "password1234")
		anon := anonClient(t, app, "10.4.0.7")
		for _, body := range []map[string]any{
			signupBody("Teen Tess", "tess@example.com", "teen", teenBirthDate()),
			signupBody("Adult Alex", "alex@example.com", "adult", ""),
		} {
			if w := anon.do("POST", "/api/v1/account-requests", body, true); w.Code != http.StatusOK {
				t.Fatalf("signup: %d %s", w.Code, w.Body.String())
			}
		}
		var teen, adult *notificationJSON
		for _, n := range feed(t, admin, "") {
			n := n
			switch {
			case n.Kind == notify.AccountRequested && strings.Contains(n.Title, "Teen Tess"):
				teen = &n
			case n.Kind == notify.AccountRequested && strings.Contains(n.Title, "Adult Alex"):
				adult = &n
			}
		}
		if teen == nil || adult == nil {
			t.Fatalf("admin feed is missing a request: %+v", feed(t, admin, ""))
		}
		if teen.SubjectKind != "teen" || adult.SubjectKind != "" {
			t.Fatalf("badges: teen %q adult %q", teen.SubjectKind, adult.SubjectKind)
		}
		// The badge is a field, not title text: the title is the same for both.
		for _, n := range []*notificationJSON{teen, adult} {
			if strings.Contains(n.Title, "(teen)") || strings.Contains(n.Title+n.Body, "@") {
				t.Fatalf("badge or contact detail in text: %q %q", n.Title, n.Body)
			}
		}
	})

	t.Run("a leader's bell badges a teen who left, and the teen's own bell does not", func(t *testing.T) {
		f := newDepartureTestFixture(t)
		birth := time.Now().UTC().AddDate(-15, 0, 0)
		if err := f.app.DB.Model(f.subject).Updates(map[string]any{"account_type": models.AccountTeen, "birth_date": birth}).Error; err != nil {
			t.Fatal(err)
		}
		facts := loadDepartureFactsForTest(t, f, f.subject, f.subject, time.Now().UTC())
		if facts.Origin != policy.OccurrenceOriginDepartureSelf {
			t.Fatalf("origin %q", facts.Origin)
		}
		if err := f.app.DB.Transaction(func(tx *gorm.DB) error { return notifyDeparture(tx, facts, 0, 0) }); err != nil {
			t.Fatal(err)
		}
		var left []notificationJSON
		for _, n := range feed(t, f.leaderC, "") {
			if n.Kind == notify.MembershipLeft {
				left = append(left, n)
			}
		}
		if len(left) != 1 || left[0].SubjectKind != "teen" {
			t.Fatalf("leader's row: %+v", left)
		}
		for _, n := range feed(t, f.subjectC, "") {
			if n.SubjectKind != "" {
				t.Fatalf("the teen's own bell badges them: %+v", n)
			}
		}
		// A viewer with no access has no row: the feed is the owner's only.
		outsider := newTestUser(t, f.app, "outside reader", "password1234", models.RoleMember)
		if ns := feed(t, loginAPI(t, f.app, outsider.Email, "password1234"), ""); len(ns) != 0 {
			t.Fatalf("an outsider has rows: %+v", ns)
		}
	})

	t.Run("an adult who left is not badged", func(t *testing.T) {
		f := newDepartureTestFixture(t)
		facts := loadDepartureFactsForTest(t, f, f.subject, f.subject, time.Now().UTC())
		if err := f.app.DB.Transaction(func(tx *gorm.DB) error { return notifyDeparture(tx, facts, 0, 0) }); err != nil {
			t.Fatal(err)
		}
		for _, n := range feed(t, f.leaderC, "") {
			if n.SubjectKind != "" {
				t.Fatalf("an adult is badged: %+v", n)
			}
		}
	})

	t.Run("a stored row with no kind, or a damaged one, shows none", func(t *testing.T) {
		app := testApp(t)
		u := newTestUser(t, app, "reader", "password1234", models.RoleMember)
		c := loginAPI(t, app, "reader", "password1234")
		for i, data := range []string{`{}`, `{"subjectKind":"adult"}`, `{"subjectKind":9}`} {
			row := models.Notification{UserID: u.ID, Kind: notify.MembershipLeft, Category: notify.CatGroups,
				Title: fmt.Sprintf("Row %d", i), Link: "/", Actions: "[]", BatchCount: 1, Data: data}
			if err := app.DB.Create(&row).Error; err != nil {
				t.Fatal(err)
			}
		}
		if err := app.DB.Create(&models.Notification{UserID: u.ID, Kind: notify.MembershipLeft, Category: notify.CatGroups,
			Title: "Teen row", Link: "/", Actions: "[]", BatchCount: 1, Data: `{"subjectKind":"teen"}`}).Error; err != nil {
			t.Fatal(err)
		}
		for _, n := range feed(t, c, "") {
			want := ""
			if n.Title == "Teen row" {
				want = "teen"
			}
			if n.SubjectKind != want {
				t.Fatalf("%s: kind %q, want %q", n.Title, n.SubjectKind, want)
			}
		}
	})
}

// The household helpers read the row's own kind, and a person's own id never
// keeps its badge on their own notice.
func TestSubjectKindHelpers(t *testing.T) {
	app := testApp(t)
	parent := newTestUser(t, app, "badge-parent", "pw", models.RoleMember)
	household := models.Household{Name: "Badge family", CreatedBy: parent.ID}
	if err := app.DB.Create(&household).Error; err != nil {
		t.Fatal(err)
	}
	person := func(first, kind string) uint {
		p := models.HouseholdPerson{HouseholdID: household.ID, FirstName: first, Kind: kind, CreatedBy: parent.ID}
		if err := app.DB.Create(&p).Error; err != nil {
			t.Fatal(err)
		}
		return p.ID
	}
	for _, tc := range []struct {
		id   uint
		want string
	}{
		{person("Kai", models.HouseholdChild), notify.SubjectChild},
		{person("Tess", models.HouseholdTeen), notify.SubjectTeen},
		{person("Sam", models.HouseholdAdult), ""},
		{999999, ""},
	} {
		got, err := subjectKindOfHousehold(app.DB, tc.id)
		if err != nil || got != tc.want {
			t.Errorf("household person %d: %q %v, want %q", tc.id, got, err, tc.want)
		}
	}
	if got, err := subjectKindOfUser(app.DB, parent.ID); err != nil || got != "" {
		t.Errorf("adult user: %q %v", got, err)
	}
	if got, err := subjectKindOfUser(app.DB, 999999); err != nil || got != "" {
		t.Errorf("missing user: %q %v", got, err)
	}
	self := uint(7)
	if got := badgeForRecipients(notify.SubjectTeen, &self, []uint{3, 7}); got != "" {
		t.Errorf("a teen's own recipient list keeps the badge: %q", got)
	}
	if got := badgeForRecipients(notify.SubjectTeen, &self, []uint{3, 8}); got != notify.SubjectTeen {
		t.Errorf("another recipient lost the badge: %q", got)
	}
	if got := badgeForRecipients(notify.SubjectChild, nil, []uint{3}); got != notify.SubjectChild {
		t.Errorf("a person with no account lost the badge: %q", got)
	}
}

// A registration notice about a household person is badged only for a holder
// who runs the event or is an admin; an ordinary holder gets the same notice
// without it (DEC-20, DEC-150).
func TestRegistrationNoticeBadgesHouseholdPersonOnlyForRunners(t *testing.T) {
	f := newDepartureTestFixture(t)
	admin := newTestUser(t, f.app, "badge admin", "password1234", models.RoleAdmin)
	start := time.Now().UTC().Add(72 * time.Hour).Truncate(time.Hour)
	occ, _ := addDepartureRegistration(t, f, f.subject, "Badge supper", models.VisibilityPublic, nil, start, start.Add(time.Hour), models.OccurrenceScheduled, models.RegGoing)
	var ev models.Event
	if err := f.app.DB.First(&ev, occ.EventID).Error; err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		holder *models.User
		kind   string
		want   string
	}{
		{"ordinary holder, child", f.subject, models.HouseholdChild, ""},
		{"ordinary holder, teen", f.subject, models.HouseholdTeen, ""},
		{"group leader, child", f.leader, models.HouseholdChild, notify.SubjectChild},
		{"group leader, teen", f.leader, models.HouseholdTeen, notify.SubjectTeen},
		{"admin, child", admin, models.HouseholdChild, notify.SubjectChild},
		{"admin, adult person", admin, models.HouseholdAdult, ""},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			household := models.Household{Name: fmt.Sprintf("Badge family %d", i), CreatedBy: tc.holder.ID}
			if err := f.app.DB.Create(&household).Error; err != nil {
				t.Fatal(err)
			}
			person := models.HouseholdPerson{HouseholdID: household.ID, FirstName: fmt.Sprintf("Kid%d", i), Kind: tc.kind, CreatedBy: tc.holder.ID}
			if err := f.app.DB.Create(&person).Error; err != nil {
				t.Fatal(err)
			}
			reg := models.Registration{OccurrenceID: occ.ID, HouseholdPersonID: &person.ID, RegisteredBy: &tc.holder.ID,
				Status: models.RegCancelled, Source: models.RegSourceWeb, AttendanceMode: models.AttendanceInPerson}
			if err := f.app.DB.Create(&reg).Error; err != nil {
				t.Fatal(err)
			}
			if err := f.app.DB.Transaction(func(tx *gorm.DB) error {
				return notifyRegistrationRemoved(tx, &ev, &occ, reg, "", nil)
			}); err != nil {
				t.Fatal(err)
			}
			c := loginAPI(t, f.app, tc.holder.Email, "password1234")
			var got *notificationJSON
			for _, n := range feed(t, c, "") {
				n := n
				if n.Kind == notify.RegistrationRemoved && strings.Contains(n.Body, person.FirstName+"'s") {
					got = &n
				}
			}
			if got == nil {
				t.Fatalf("holder has no removal notice for %s: %+v", person.FirstName, feed(t, c, ""))
			}
			if got.SubjectKind != tc.want {
				t.Fatalf("subjectKind %q, want %q", got.SubjectKind, tc.want)
			}
		})
	}
}

// Admins other than the teen get the Teen badge on a consent change; the
// teen's own bell never does.
func TestTeenConsentNoticeBadgesTeenForOtherAdmins(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	other := newTestUser(t, app, "other-admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	teen := consentTeen(t, app)
	// An admin account that is the teen is active once consent is granted.
	if err := app.DB.Model(teen).Update("role", models.RoleAdmin).Error; err != nil {
		t.Fatal(err)
	}
	apiDo(t, admin, "POST", consentPath(teen), map[string]string{"scope": "account", "status": "granted", "evidenceText": "Paper"}, 200)
	var rows []models.Notification
	if err := app.DB.Where("kind = ?", notify.TeenConsentChanged).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	badge := map[uint]string{}
	for _, n := range rows {
		badge[n.UserID] = notify.SubjectKindOf(n.Data)
	}
	if badge[other.ID] != notify.SubjectTeen {
		t.Errorf("the other admin's badge %q, want teen (%+v)", badge[other.ID], rows)
	}
	if got, ok := badge[teen.ID]; !ok || got != "" {
		t.Errorf("the teen's own row: present %v, badge %q, want present and none", ok, got)
	}
}
