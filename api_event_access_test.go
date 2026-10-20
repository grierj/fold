package main

import (
	"encoding/json"
	"fmt"
	"fold/internal/audit"
	"fold/internal/notify"
	"fold/models"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestEventMorePrivateCancelsShutOutRegistrants(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	gid := createGroupAs(t, admin, "Garden Club", models.VisibilityPublic)
	mem := newTestUser(t, app, "mem", "password1234", models.RoleMember)
	addMemberEmail(t, admin, gid, mem.Email)
	memC := loginAPI(t, app, "mem", "password1234")
	out := newTestUser(t, app, "out", "password1234", models.RoleMember)
	outC := loginAPI(t, app, "out", "password1234")
	ev := scheduleGroupEvent(t, admin, gid, "Picnic")
	register(t, memC, ev.ID)
	register(t, outC, ev.ID)

	// Preview first, like a group change: the outsider is counted.
	var impact visibilityImpactJSON
	decodeJSON(t, admin.do("GET", fmt.Sprintf("/api/v1/events/%d/visibility-impact?visibility=invite_only", ev.ID), nil, false), &impact)
	if len(impact.Tightened) != 1 || impact.Tightened[0].Registrants != 1 || len(impact.LeaderOpened) != 0 {
		t.Fatalf("impact: %+v", impact)
	}
	apiDo(t, memC, "GET", fmt.Sprintf("/api/v1/events/%d/visibility-impact?visibility=invite_only", ev.ID), nil, http.StatusForbidden)
	// Without confirm nothing is saved.
	setVisibility(t, admin, ev.ID, models.VisibilityInviteOnly, "", http.StatusConflict)
	if e := eventRow(t, app, ev.ID); e.Visibility != models.VisibilityPublic || regCount(app, out, ev.ID) != 1 {
		t.Fatalf("saved without confirm: %+v", e)
	}
	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d/visibility", ev.ID), map[string]any{"visibility": models.VisibilityInviteOnly, "confirm": true}, http.StatusOK)
	if regCount(app, out, ev.ID) != 0 || regCount(app, mem, ev.ID) != 1 {
		t.Fatalf("registrations: out %d, mem %d", regCount(app, out, ev.ID), regCount(app, mem, ev.ID))
	}
	found := false
	for _, n := range noticeIDs(t, outC) {
		if n.Kind == "registration.cancelled" {
			found = true
		}
	}
	if !found {
		t.Fatal("outsider was not told their registration was cancelled")
	}
}

// An opened invite-only group's event must not reveal the group's meeting
// place (the default event location) or, in a cancellation notice, the
// group's name, place, or page to someone outside the group.
func TestOpenedEventHidesGroupLocation(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	gid := createGroupAs(t, admin, "Hidden Care", models.VisibilityInviteOnly)
	if err := app.DB.Model(&models.Group{}).Where("id = ?", gid).Update("location", "12 Elm St").Error; err != nil {
		t.Fatal(err)
	}
	ev := scheduleGroupEvent(t, admin, gid, "Open House")
	setVisibility(t, admin, ev.ID, models.VisibilityPublic, "", http.StatusOK)

	newTestUser(t, app, "outsider", "password1234", models.RoleMember)
	outsider := loginAPI(t, app, "outsider", "password1234")
	_, raw := getEvent(t, outsider, ev.ID, http.StatusOK)
	if strings.Contains(raw, "12 Elm St") {
		t.Fatalf("outsider saw the group's location: %s", raw)
	}
	if list := outsider.do("GET", "/api/v1/events", nil, false).Body.String(); strings.Contains(list, "12 Elm St") {
		t.Fatalf("open list leaked the group's location: %s", list)
	}
	_, adminRaw := getEvent(t, admin, ev.ID, http.StatusOK)
	if !strings.Contains(adminRaw, "12 Elm St") {
		t.Fatalf("admin should still see the default location: %s", adminRaw)
	}

	apiDo(t, outsider, "PUT", fmt.Sprintf("/api/v1/events/%d/registrations", ev.ID), map[string]any{
		"status": "going", "date": "2026-12-01",
	}, http.StatusOK)
	if err := app.DB.Model(&models.Event{}).Where("id = ?", ev.ID).Update("title", "").Error; err != nil {
		t.Fatal(err)
	}
	apiDo(t, admin, "POST", fmt.Sprintf("/api/v1/events/%d/cancel", ev.ID), map[string]any{"date": "2026-12-01"}, http.StatusOK)

	var found bool
	for _, n := range noticeIDs(t, outsider) {
		if n.Kind != "event.cancelled" {
			continue
		}
		found = true
		b, _ := json.Marshal(n)
		s := string(b)
		if strings.Contains(s, "Hidden Care") || strings.Contains(s, "12 Elm St") || strings.Contains(s, "/groups/") {
			t.Fatalf("cancel notice revealed the group: %s", s)
		}
		if n.Link != fmt.Sprintf("/events/%d", ev.ID) {
			t.Fatalf("link: %s", n.Link)
		}
	}
	if !found {
		t.Fatal("outsider got no cancellation notice")
	}
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

// M1.2 acceptance: a visibility-override notice reaches the other leaders
// and the admins in the bell and by email, with one notification_deliveries
// row each.
func TestVisibilityOverrideEmailsAdminsAndLeaders(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "root", "password1234", models.RoleAdmin)
	lead1 := newTestUser(t, app, "lead1", "password1234", models.RoleMember)
	lead2 := newTestUser(t, app, "lead2", "password1234", models.RoleMember)
	lc1 := loginAPI(t, app, "lead1", "password1234")

	g := newTestGroup(t, app, lead1, "Youth", models.GroupActive)
	app.DB.Model(g).Update("visibility", models.VisibilityPrivate)
	addMemberEmail(t, lc1, g.ID, "lead2@example.com")
	apiDo(t, lc1, "PUT", "/api/v1/groups/"+itoa(g.ID)+"/members/"+itoa(lead2.ID)+"/role",
		map[string]string{"role": "leader"}, http.StatusOK)

	ev := scheduleGroupEvent(t, lc1, g.ID, "Lock-in")
	setVisibility(t, lc1, ev.ID, models.VisibilityPublic, "invite the neighbors", http.StatusOK)

	for _, who := range []struct{ name, email string }{
		{"root", "root@example.com"},
		{"lead2", "lead2@example.com"},
	} {
		c := loginAPI(t, app, who.name, "password1234")
		found := false
		for _, n := range feed(t, c, "") {
			if n.Kind == "event.visibility_override" {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: no bell item", who.name)
		}
		m := latestEmail(t, app, who.email)
		if !strings.Contains(m.Subject, "Event opened up") {
			t.Fatalf("%s: subject %q", who.name, m.Subject)
		}
		if !strings.Contains(m.Body, "/groups/") {
			t.Fatalf("%s: no group link in email", who.name)
		}
	}

	var n int64
	app.DB.Model(&models.NotificationDelivery{}).
		Joins("JOIN notifications ON notifications.id = notification_deliveries.notification_id").
		Where("notifications.kind = ? AND notification_deliveries.channel = ? AND notification_deliveries.status = ?",
			notify.VisibilityOverride, models.DeliveryEmail, models.DeliverySent).
		Count(&n)
	// The actor (lead1) is skipped: root and lead2 only.
	if n != 2 {
		t.Fatalf("email delivery rows: %d, want 2", n)
	}
}

// visibleEvents is the one list filter (#75): a soft-deleted group's
// events are never listed, not even for an admin, and a church-wide
// public event is listed for a signed-out visitor.
func TestVisibleEventsSkipsDeletedGroups(t *testing.T) {
	app := testApp(t)
	admin := newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	g := models.Group{Name: "Gone", Status: models.GroupActive, Visibility: models.VisibilityPublic, LeaderID: admin.ID}
	if err := app.DB.Create(&g).Error; err != nil {
		t.Fatal(err)
	}
	ev, _ := seedEvent(t, app, &g, admin.ID, time.Now().Add(48*time.Hour))
	cw := models.Event{Visibility: models.VisibilityPublic, StartsLocal: "2030-01-01T10:00", DurationMinutes: 60, TZID: "UTC", CreatedBy: admin.ID, Title: "All church"}
	if err := app.DB.Create(&cw).Error; err != nil {
		t.Fatal(err)
	}
	if vs, _ := app.visibleEvents(admin, app.DB); vs.ByID[ev.ID] == nil || vs.ByID[cw.ID] == nil {
		t.Fatalf("admin should see both before the delete: %v", vs.IDs)
	}
	if err := app.DB.Delete(&g).Error; err != nil {
		t.Fatal(err)
	}
	vs, err := app.visibleEvents(admin, app.DB)
	if err != nil || vs.ByID[ev.ID] != nil {
		t.Fatalf("deleted group's event listed for an admin: %v %v", vs.IDs, err)
	}
	if anon, _ := app.visibleEvents(nil, app.DB); anon.ByID[cw.ID] == nil {
		t.Fatalf("public church-wide event missing for a visitor: %v", anon.IDs)
	}
}
