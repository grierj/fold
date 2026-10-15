package main

import (
	"fmt"
	"fold/internal/audit"
	"fold/models"
	"net/http"
	"strings"
	"testing"
	"time"
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

func TestLocalWhenNamesTheZone(t *testing.T) {
	at := time.Date(2035, 10, 20, 1, 0, 0, 0, time.UTC)
	got := localWhen(at, "America/Denver")
	if !strings.Contains(got, "Fri, Oct 19") || !strings.Contains(got, "7:00 PM") || !strings.Contains(got, "MDT") {
		t.Fatalf("localWhen: %q", got)
	}
}
