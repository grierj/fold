package main

import (
	"encoding/json"
	"fmt"
	"fold/internal/audit"
	"fold/models"
	"net/http"
	"strings"
	"testing"
)

func TestGroupMorePrivateTightensAndCancels(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	gid := createGroupAs(t, admin, "Garden Club", models.VisibilityPublic)
	apiDo(t, admin, "PUT", "/api/v1/groups/"+itoa(gid), map[string]any{"location": "12 Elm Street"}, http.StatusOK)
	mem := newTestUser(t, app, "mem", "password1234", models.RoleMember)
	addMemberEmail(t, admin, gid, mem.Email)
	memC := loginAPI(t, app, "mem", "password1234")
	out := newTestUser(t, app, "out", "password1234", models.RoleMember)
	outC := loginAPI(t, app, "out", "password1234")

	ev := scheduleGroupEvent(t, admin, gid, "Picnic")
	register(t, memC, ev.ID)
	register(t, outC, ev.ID)

	// Public -> private: everyone registered can still see it.
	apiDo(t, admin, "PUT", "/api/v1/groups/"+itoa(gid), map[string]any{"visibility": models.VisibilityPrivate}, http.StatusOK)
	if e := eventRow(t, app, ev.ID); e.Visibility != models.VisibilityPrivate || e.LeaderOpened {
		t.Fatalf("private: %+v", e)
	}
	if regCount(app, out, ev.ID) != 1 {
		t.Fatal("private shut out a signed-in registrant")
	}

	// Preview before saving.
	var impact visibilityImpactJSON
	decodeJSON(t, admin.do("GET", "/api/v1/groups/"+itoa(gid)+"/visibility-impact?visibility=invite_only", nil, false), &impact)
	if len(impact.Tightened) != 1 || impact.Tightened[0].ID != ev.ID || impact.Tightened[0].Registrants != 1 || len(impact.LeaderOpened) != 0 {
		t.Fatalf("impact: %+v", impact)
	}
	apiDo(t, memC, "GET", "/api/v1/groups/"+itoa(gid)+"/visibility-impact?visibility=invite_only", nil, http.StatusForbidden)

	// Private -> invite-only: tightened, the outsider is cancelled and told.
	apiDo(t, admin, "PUT", "/api/v1/groups/"+itoa(gid), map[string]any{"visibility": models.VisibilityInviteOnly}, http.StatusOK)
	if e := eventRow(t, app, ev.ID); e.Visibility != models.VisibilityInviteOnly {
		t.Fatalf("invite-only: %+v", e)
	}
	if regCount(app, out, ev.ID) != 0 || regCount(app, mem, ev.ID) != 1 {
		t.Fatalf("registrations: out %d, mem %d", regCount(app, out, ev.ID), regCount(app, mem, ev.ID))
	}
	rows, metas := auditRows(t, app, audit.RegistrationCancelled)
	if len(rows) != 1 || rows[0].TargetID != ev.ID || metas[0].UserID != out.ID {
		t.Fatalf("cancel audit: %+v", metas)
	}
	var changes []models.AuditLog
	app.DB.Where("action = ? AND target_id = ?", audit.VisibilityChanged, ev.ID).Order("id").Find(&changes)
	if len(changes) != 2 || !strings.Contains(changes[1].MetaJSON, "group_more_private") {
		t.Fatalf("tighten audit: %+v", changes)
	}

	found := false
	for _, n := range noticeIDs(t, outC) {
		if n.Kind == "registration.cancelled" {
			found = true
			raw, _ := json.Marshal(n)
			if !strings.Contains(n.Title, "Picnic") || strings.Contains(string(raw), "Garden Club") ||
				strings.Contains(string(raw), "Elm Street") || strings.Contains(n.Link, "/groups/") || strings.Contains(n.Link, "/events/") {
				t.Fatalf("notice leaks the group: %s", raw)
			}
		}
	}
	if !found {
		t.Fatalf("outsider not told: %+v", noticeIDs(t, outC))
	}
	for _, n := range noticeIDs(t, memC) {
		if n.Kind == "registration.cancelled" {
			t.Fatalf("member told: %+v", n)
		}
	}
	getEvent(t, outC, ev.ID, http.StatusNotFound)
}

func TestLeaderOpenedEventsAskBeforeTightening(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	newTestUser(t, app, "admin2", "password1234", models.RoleAdmin)
	admin2 := loginAPI(t, app, "admin2", "password1234")
	gid := createGroupAs(t, admin, "Youth", models.VisibilityPrivate)
	leadUser := newTestUser(t, app, "lead", "password1234", models.RoleMember)
	lid := addMemberEmail(t, admin, gid, leadUser.Email)
	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/groups/%d/members/%d/role", gid, lid), map[string]any{"role": "leader"}, http.StatusOK)
	lead := loginAPI(t, app, "lead", "password1234")
	out := newTestUser(t, app, "out", "password1234", models.RoleMember)
	outC := loginAPI(t, app, "out", "password1234")

	inherited := scheduleGroupEvent(t, lead, gid, "Bible study")
	kept := scheduleGroupEvent(t, lead, gid, "Car wash")
	tightened := scheduleGroupEvent(t, lead, gid, "Movie night")
	for _, id := range []uint{kept.ID, tightened.ID} {
		setVisibility(t, lead, id, models.VisibilityPublic, "", http.StatusOK)
		if !eventRow(t, app, id).LeaderOpened {
			t.Fatal("opening wider should set the marker")
		}
	}
	register(t, outC, kept.ID)
	register(t, outC, tightened.ID)

	// Opening the group further changes nothing.
	apiDo(t, lead, "PUT", "/api/v1/groups/"+itoa(gid), map[string]any{"visibility": models.VisibilityPublic}, http.StatusOK)
	if e := eventRow(t, app, inherited.ID); e.Visibility != models.VisibilityPrivate {
		t.Fatalf("opening the group moved an inherited event: %+v", e)
	}

	// No decision: 409 with the list, and nothing is saved.
	w := lead.do("PUT", "/api/v1/groups/"+itoa(gid), map[string]any{"visibility": models.VisibilityInviteOnly}, true)
	if w.Code != http.StatusConflict {
		t.Fatalf("want 409: %d %s", w.Code, w.Body.String())
	}
	var conflict struct {
		NeedsDecision bool                 `json:"needsDecision"`
		Impact        visibilityImpactJSON `json:"impact"`
	}
	decodeJSON(t, w, &conflict)
	if !conflict.NeedsDecision || len(conflict.Impact.LeaderOpened) != 2 || len(conflict.Impact.Tightened) != 1 ||
		conflict.Impact.Tightened[0].ID != inherited.ID || conflict.Impact.LeaderOpened[0].Registrants != 1 {
		t.Fatalf("conflict: %+v", conflict)
	}
	var g models.Group
	app.DB.First(&g, gid)
	if g.Visibility != models.VisibilityPublic || eventRow(t, app, inherited.ID).Visibility != models.VisibilityPrivate {
		t.Fatal("a refused change saved something")
	}
	// A bad or partial decision is refused the same way.
	if c := lead.do("PUT", "/api/v1/groups/"+itoa(gid), map[string]any{"visibility": models.VisibilityInviteOnly,
		"openedEvents": map[string]string{itoa(kept.ID): "keep", itoa(tightened.ID): "maybe"}}, true).Code; c != http.StatusConflict {
		t.Fatalf("bad decision: %d", c)
	}

	apiDo(t, lead, "PUT", "/api/v1/groups/"+itoa(gid), map[string]any{"visibility": models.VisibilityInviteOnly,
		"openedEvents": map[string]string{itoa(kept.ID): "keep", itoa(tightened.ID): "tighten"}}, http.StatusOK)
	if e := eventRow(t, app, inherited.ID); e.Visibility != models.VisibilityInviteOnly {
		t.Fatalf("inherited: %+v", e)
	}
	if e := eventRow(t, app, kept.ID); e.Visibility != models.VisibilityPublic || !e.LeaderOpened {
		t.Fatalf("kept: %+v", e)
	}
	if e := eventRow(t, app, tightened.ID); e.Visibility != models.VisibilityInviteOnly || e.LeaderOpened {
		t.Fatalf("tightened: %+v", e)
	}
	if regCount(app, out, kept.ID) != 1 || regCount(app, out, tightened.ID) != 0 {
		t.Fatal("only the tightened event cancels outsiders")
	}
	var keptRows []models.AuditLog
	app.DB.Where("action = ? AND target_id = ?", audit.VisibilityOverride, kept.ID).Order("id").Find(&keptRows)
	if len(keptRows) != 2 || !strings.Contains(keptRows[1].MetaJSON, "Kept open") {
		t.Fatalf("keep is a logged override: %+v", keptRows)
	}
	if !hasVisibilityNotice(noticeIDs(t, admin2), "Car wash", "Kept open") {
		t.Fatalf("other admins see the kept override: %+v", noticeIDs(t, admin2))
	}
	// The outsider still sees the kept event, with a count only.
	page, raw := getEvent(t, outC, kept.ID, http.StatusOK)
	if page.Occurrences[0].AttendeesVisible || strings.Contains(raw, "Youth") {
		t.Fatalf("kept event for an outsider: %s", raw)
	}
}

func TestVisibilityImpactCountsHouseholdSubjectsAcrossDates(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "HouseholdVisibility")
	eventID, date := seedCappedEvent(t, app, admin, gid, nil, true)
	holder, c, spouse, child := householdFamily(t, app, admin, "visibilityholder")
	answer := func(date string) {
		batchAnswer(t, c, eventID, date, map[string]any{"status": "going", "people": []map[string]any{{"personId": spouse.ID, "status": "going"}, {"personId": child.ID, "status": "going"}}}, http.StatusOK)
	}
	answer(date)
	var o models.EventOccurrence
	if err := app.DB.Where("event_id = ?", eventID).First(&o).Error; err != nil {
		t.Fatal(err)
	}
	o.ID = 0
	o.StartsAt = o.StartsAt.AddDate(0, 0, 7)
	o.EndsAt = o.EndsAt.AddDate(0, 0, 7)
	o.OriginalDate = o.StartsAt.Format("2006-01-02")
	if err := app.DB.Omit("Event").Create(&o).Error; err != nil {
		t.Fatal(err)
	}
	answer(o.OriginalDate)
	var ev models.Event
	app.DB.First(&ev, eventID)
	impact := eventImpactOf(app.DB, &ev, models.VisibilityInviteOnly)
	if len(impact.Tightened) != 1 || impact.Tightened[0].Registrants != 3 {
		t.Fatalf("subject impact %+v", impact)
	}
	apiDo(t, admin, "PUT", "/api/v1/admin/users/"+itoa(holder.ID), map[string]any{"status": "disabled"}, http.StatusOK)
	apiDo(t, admin, "PUT", "/api/v1/events/"+itoa(eventID)+"/visibility", map[string]any{"visibility": "invite_only", "confirm": true}, http.StatusOK)
	var n int64
	app.DB.Model(&models.Registration{}).Where("user_id = ? OR registered_by = ?", holder.ID, holder.ID).Count(&n)
	if n != 0 {
		t.Fatalf("disabled answers can resurface: %d", n)
	}
}
