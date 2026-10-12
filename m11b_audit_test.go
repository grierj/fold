package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"fold/models"
)

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
	setVisibility(t, admin, ev.ID, models.VisibilityPublic, models.VisibilitySourceCustom, "", http.StatusOK)

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
		if !strings.HasPrefix(n.ID, "event-cancelled:") {
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
