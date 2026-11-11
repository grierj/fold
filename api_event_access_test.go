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
	if regCount(app, out, ev.ID) != 1 || regCount(app, mem, ev.ID) != 1 {
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

// eventCandidates is the shared candidate loader (#75): a soft-deleted group's
// events are never listed, not even for an admin, and a church-wide
// public event is listed for a signed-out visitor.
func TestEventCandidatesSkipsDeletedGroups(t *testing.T) {
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
	if vs, _ := app.eventCandidates(admin, app.DB); vs.ByID[ev.ID] == nil || vs.ByID[cw.ID] == nil {
		t.Fatalf("admin should see both before the delete: %v", vs.IDs)
	}
	if err := app.DB.Delete(&g).Error; err != nil {
		t.Fatal(err)
	}
	vs, err := app.eventCandidates(admin, app.DB)
	if err != nil || vs.ByID[ev.ID] != nil {
		t.Fatalf("deleted group's event listed for an admin: %v %v", vs.IDs, err)
	}
	if anon, _ := app.eventCandidates(nil, app.DB); anon.ByID[cw.ID] == nil {
		t.Fatalf("public church-wide event missing for a visitor: %v", anon.IDs)
	}
	// The admin "show deleted events" switch lists the deleted group's event
	// for an admin and for nobody else (DEC-68).
	if withDeleted, err := app.deletedEventCandidates(admin, app.DB); err != nil || withDeleted.ByID[ev.ID] == nil {
		t.Fatalf("admin with the deleted switch should see the deleted group's event: %v %v", withDeleted.IDs, err)
	}
	member := newTestUser(t, app, "member", "password1234", models.RoleMember)
	if other, err := app.deletedEventCandidates(member, app.DB); err != nil || other.ByID[ev.ID] != nil {
		t.Fatalf("a non-admin must not get deleted groups' events: %v %v", other.IDs, err)
	}
}

// N-17 (inventory 9.2.2, a gated entry): making an event more private cancels
// the sign-ups of people who could not see it any more and tells each of them.
// The real operation is PUT /events/:id/visibility {visibility: invite_only,
// confirm} -> apiSetEventVisibility -> contactMutation (gate first, with the
// event id) -> the event row FOR UPDATE -> cancelLosers: the losing registrants
// are found (a non-member of a group whose event is now invite-only), their
// registrations cancelled with an audit row, and notifyRegistrationCancelled
// tells each one, preceded by a read of the person that skips someone who no
// longer exists or is disabled. Tightening with a confirm answers 200.
//
// The two outsiders (the victim and the kept one) lose; the group member does
// not. Sender first: the tightening stops after it wrote the victim's notice;
// the real delete waits on the gate and removes it. Deletion first: losersForDate
// joins users with a raw JOIN (no deleted_at filter), so the erased holder's
// sign-up can still be listed, cancelled and audited; notifyRegistrationCancelled
// then reads the holder with userIn, which returns nil for a soft-deleted user,
// and returns before Send. This order therefore proves the outcome (200, the
// event tightened, the kept outsider told, no notice recorded for the victim)
// but not the recipient lock inside Send, which the sender-first order and the
// plain-entry tests exercise. Not covered: the group-level tightening
// (apiUpdateGroup) and the per-date override (apiUpdateOccurrence), which reach
// the same cancelLosers.
func TestRegistrationCancelledNoticeSerializesWithRealAdminDelete(t *testing.T) {
	runGatedErasureBarriers(t, 7402125, 7402126, func(t *testing.T) gatedErasureScene {
		f := newRegistrantRace(t)
		return gatedErasureScene{app: f.app, victim: f.victim, sends: []string{notify.RegistrationCancelled},
			run: func() error {
				return doExpect(f.admin, "PUT", "/api/v1/events/"+itoa(f.eventID)+"/visibility",
					map[string]any{"visibility": models.VisibilityInviteOnly, "confirm": true}, http.StatusOK)
			},
			erase: eraseVia(f.eraser, f.victim),
			told: func(t *testing.T) {
				if e := eventRow(t, f.app, f.eventID); e.Visibility != models.VisibilityInviteOnly {
					t.Fatalf("event visibility %q, want invite_only", e.Visibility)
				}
				wantNotices(t, f.app, notify.RegistrationCancelled, map[*models.User]int64{f.kept: 1, f.member: 0})
				if regCount(f.app, f.member, f.eventID) != 1 {
					t.Fatal("the group member's sign-up should have stayed")
				}
			},
		}
	})
}

// eventWriterRace keeps notice jobs queued so refusal snapshots cover every
// effect of the writer, without a delivery worker changing rows between reads.
type eventWriterRace struct {
	app          *App
	admin, actor *apiClient
	user         *models.User
	group        *models.Group
	event        models.Event
	date         string
}

func newEventWriterRace(t *testing.T, adminActor bool) eventWriterRace {
	t.Helper()
	app := testApp(t)
	owner := newTestUser(t, app, "owner", "password1234", models.RoleAdmin)
	admin := loginHeld(t, app, owner.Email, "password1234")
	g := activeGroup(t, app, owner, "Choir", models.VisibilityPublic, "")
	role := models.RoleMember
	if adminActor {
		role = models.RoleAdmin
	}
	actor := newTestUser(t, app, "writer", "password1234", role)
	if !adminActor {
		addMember(t, app, actor, &g, models.MembershipLeader)
	}
	client := loginHeld(t, app, actor.Email, "password1234")
	ev := requireCreatedEvent(t, app, admin.do("POST", "/api/v1/groups/"+itoa(g.ID)+"/events", createEventBodyForTest(), true))
	var occ models.EventOccurrence
	if err := app.DB.Where("event_id = ?", ev.ID).First(&occ).Error; err != nil {
		t.Fatal(err)
	}
	return eventWriterRace{app: app, admin: admin, actor: client, user: actor, group: &g, event: ev,
		date: models.FormatCalendarDate(occ.OriginalDate)}
}

func (f eventWriterRace) revoke(t *testing.T, change string) {
	t.Helper()
	switch change {
	case "demoted admin":
		apiDo(t, f.admin, "PUT", "/api/v1/admin/users/"+itoa(f.user.ID), map[string]any{"role": models.RoleMember}, http.StatusOK)
	case "disabled account":
		apiDo(t, f.admin, "PUT", "/api/v1/admin/users/"+itoa(f.user.ID), map[string]any{"status": models.UserDisabled}, http.StatusOK)
	case "removed leader":
		path := "/api/v1/groups/" + itoa(f.group.ID) + "/members/" + itoa(f.user.ID) + "/departure"
		preview := getDeparturePreview(t, f.admin, path+"-preview", http.StatusOK)
		apiDo(t, f.admin, "POST", path, map[string]any{"snapshot": preview.Snapshot, "keepOccurrenceIds": []uint{}}, http.StatusOK)
	default:
		t.Fatalf("unknown event writer revocation %q", change)
	}
}

func eventWriterState(t *testing.T, app *App, id uint) string {
	t.Helper()
	var state struct {
		Event         models.Event
		Dates         []models.EventOccurrence
		Registrations []models.Registration
		Bookings      []models.RoomBooking
		Counts        map[string]int64
	}
	if err := app.DB.Unscoped().First(&state.Event, id).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Where("event_id = ?", id).Order("id").Find(&state.Dates).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Where("occurrence_id IN (SELECT id FROM event_occurrences WHERE event_id = ?)", id).
		Order("id").Find(&state.Registrations).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Order("id").Find(&state.Bookings).Error; err != nil {
		t.Fatal(err)
	}
	state.Counts = createEventWriteCounts(t, app)
	b, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestEventVisibilityRechecksAfterHeldBody(t *testing.T) {
	for _, change := range []string{"demoted admin", "removed leader", "disabled account"} {
		for _, noOp := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/no-op=%v", change, noOp), func(t *testing.T) {
				f := newEventWriterRace(t, change == "demoted admin")
				path := "/api/v1/events/" + itoa(f.event.ID) + "/visibility"
				apiDo(t, f.admin, "PUT", path, map[string]any{"visibility": models.VisibilityInviteOnly}, http.StatusOK)
				vis := models.VisibilityPublic
				if noOp {
					vis = models.VisibilityInviteOnly
				}
				held := f.actor.doHeld("PUT", path, map[string]any{"visibility": vis, "confirm": true})
				f.revoke(t, change)
				before := eventWriterState(t, f.app, f.event.ID)
				held.release()
				w := held.wait(t)
				want := http.StatusUnauthorized
				if change == "removed leader" {
					want = http.StatusForbidden
				}
				if w.Code != want {
					t.Fatalf("visibility after %s: %d %s, want %d", change, w.Code, w.Body.String(), want)
				}
				if after := eventWriterState(t, f.app, f.event.ID); before != after {
					t.Fatalf("refused visibility write changed rows:\nbefore %s\nafter %s", before, after)
				}
			})
		}
	}
}

func TestEventVisibilityUsesGroupAfterHeldBody(t *testing.T) {
	f := newEventWriterRace(t, false)
	path := "/api/v1/events/" + itoa(f.event.ID) + "/visibility"
	held := f.actor.doHeld("PUT", path, map[string]any{"visibility": models.VisibilityPrivate, "confirm": true, "reason": "Welcome friends"})
	apiDo(t, f.admin, "PUT", "/api/v1/groups/"+itoa(f.group.ID), map[string]any{"visibility": models.VisibilityInviteOnly, "confirm": true}, http.StatusOK)
	held.release()
	w := held.wait(t)
	if w.Code != http.StatusOK {
		t.Fatalf("visibility after group tightening: %d %s", w.Code, w.Body.String())
	}
	var answer eventPage
	decodeJSON(t, w, &answer)
	ev := eventRow(t, f.app, f.event.ID)
	if ev.Visibility != models.VisibilityPrivate || !ev.LeaderOpened || answer.Event.Visibility != ev.Visibility || !answer.Event.LeaderOpened {
		t.Fatalf("fresh group visibility not used in saved row or response: %+v %+v", ev, answer.Event)
	}
	var row models.AuditLog
	if err := f.app.DB.Where("target_type = 'event' AND target_id = ? AND action = ?", ev.ID, audit.VisibilityOverride).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(row.MetaJSON), &meta); err != nil {
		t.Fatal(err)
	}
	if meta["groupVisibility"] != models.VisibilityInviteOnly || meta["leaderOpened"] != true || row.ActorID == nil || *row.ActorID != f.user.ID {
		t.Fatalf("override audit used stale facts: %+v %v", row, meta)
	}
	if n := contactCount(t, f.app, &models.Notification{}, "user_id = ? AND kind = ? AND subject_id = ?", f.group.LeaderID, notify.VisibilityOverride, ev.ID); n != 1 {
		t.Fatalf("override notices to the other leader = %d, want 1", n)
	}
}

func TestEventVisibilityImpactIncludesHeldBodyRegistrants(t *testing.T) {
	for _, confirm := range []bool{false, true} {
		t.Run(fmt.Sprintf("confirm=%v", confirm), func(t *testing.T) {
			f := newEventWriterRace(t, false)
			var people []*models.User
			for _, name := range []string{"first", "second"} {
				people = append(people, newTestUser(t, f.app, name, "password1234", models.RoleMember))
			}
			registerPerson := func(u *models.User) {
				t.Helper()
				c := loginHeld(t, f.app, u.Email, "password1234")
				if code, body := registerAs(c, f.event.ID, f.date, models.RegGoing, nil); code != http.StatusOK {
					t.Fatalf("register: %d %s", code, body)
				}
			}
			registerPerson(people[0])
			held := f.actor.doHeld("PUT", "/api/v1/events/"+itoa(f.event.ID)+"/visibility",
				map[string]any{"visibility": models.VisibilityInviteOnly, "confirm": confirm})
			registerPerson(people[1])
			before := eventWriterState(t, f.app, f.event.ID)
			held.release()
			w := held.wait(t)
			if !confirm {
				var answer struct {
					NeedsDecision bool                 `json:"needsDecision"`
					Impact        visibilityImpactJSON `json:"impact"`
				}
				decodeJSON(t, w, &answer)
				if w.Code != http.StatusConflict || !answer.NeedsDecision || len(answer.Impact.Tightened) != 1 || answer.Impact.Tightened[0].Registrants != 2 {
					t.Fatalf("preview missed a concurrent registrant: %d %s", w.Code, w.Body.String())
				}
				if after := eventWriterState(t, f.app, f.event.ID); before != after {
					t.Fatal("unconfirmed tightening changed rows")
				}
				return
			}
			if w.Code != http.StatusOK {
				t.Fatalf("confirmed tightening: %d %s", w.Code, w.Body.String())
			}
			var occ models.EventOccurrence
			if err := f.app.DB.Where("event_id = ?", f.event.ID).First(&occ).Error; err != nil {
				t.Fatal(err)
			}
			for _, u := range people {
				if got := regStatus(t, f.app, u.ID, occ.ID); got != models.RegCancelled {
					t.Fatalf("registration of %s = %s, want cancelled", u.Name, got)
				}
				if n := contactCount(t, f.app, &models.Notification{}, "user_id = ? AND kind = ?", u.ID, notify.RegistrationCancelled); n != 1 {
					t.Fatalf("cancellation notices for %s = %d, want 1", u.Name, n)
				}
			}
		})
	}
}

func TestChurchEventVisibilityRechecksRemovedHostAfterHeldBody(t *testing.T) {
	app := testApp(t)
	owner := newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	host := newTestUser(t, app, "host", "password1234", models.RoleMember)
	admin := loginHeld(t, app, owner.Email, "password1234")
	client := loginHeld(t, app, host.Email, "password1234")
	body := createEventBodyForTest()
	body["hosts"] = []string{owner.Email, host.Email}
	ev := requireCreatedEvent(t, app, admin.do("POST", "/api/v1/events", body, true))
	held := client.doHeld("PUT", "/api/v1/events/"+itoa(ev.ID)+"/visibility", map[string]any{"visibility": models.VisibilityPrivate, "confirm": true})
	apiDo(t, admin, "DELETE", "/api/v1/events/"+itoa(ev.ID)+"/hosts/"+itoa(host.ID), nil, http.StatusOK)
	before := eventWriterState(t, app, ev.ID)
	held.release()
	w := held.wait(t)
	if w.Code != http.StatusForbidden {
		t.Fatalf("removed host visibility: %d %s", w.Code, w.Body.String())
	}
	if after := eventWriterState(t, app, ev.ID); before != after {
		t.Fatal("removed host changed event rows")
	}
}
