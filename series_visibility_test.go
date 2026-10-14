package main

import (
	"net/http"
	"testing"
	"time"

	"fold/models"
)

// Regression (Grier, Oct 14): a second series in a group must copy only the
// group's visibility, never a sibling series' override.
func TestNewSeriesCopiesOnlyGroupVisibility(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	w := admin.do("POST", "/api/v1/groups", map[string]any{"name": "Choir", "visibility": "private"}, true)
	var created struct {
		Group groupSummaryJSON `json:"group"`
	}
	decodeJSON(t, w, &created)
	gid := itoa(created.Group.ID)
	admin.do("POST", "/api/v1/admin/groups/"+gid+"/approve", map[string]any{}, true)

	start := time.Now().AddDate(0, 0, 3).Format("2006-01-02") + "T19:00"
	mk := func(title string) uint {
		w := admin.do("POST", "/api/v1/groups/"+gid+"/events", map[string]any{
			"title": title, "startsLocal": start, "durationMinutes": 60, "tzid": "America/Boise",
			"recurrenceRule": "FREQ=WEEKLY;BYDAY=MO",
		}, true)
		if w.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", title, w.Code, w.Body.String())
		}
		var r struct {
			Event struct {
				ID uint `json:"id"`
			} `json:"event"`
		}
		decodeJSON(t, w, &r)
		return r.Event.ID
	}
	first := mk("First")
	if w := admin.do("PUT", "/api/v1/events/"+itoa(first)+"/visibility", map[string]any{"visibility": "public", "reason": "open house"}, true); w.Code != http.StatusOK {
		t.Fatalf("open first: %d %s", w.Code, w.Body.String())
	}
	second := mk("Second")

	var ev models.Event
	app.DB.First(&ev, second)
	if ev.Visibility != "private" || ev.LeaderOpened {
		t.Fatalf("second series: visibility=%s leaderOpened=%v", ev.Visibility, ev.LeaderOpened)
	}
	w = admin.do("GET", "/api/v1/groups/"+gid, nil, false)
	var detail struct {
		UpcomingEvents []occurrenceJSON `json:"upcomingEvents"`
	}
	decodeJSON(t, w, &detail)
	seen := false
	for _, o := range detail.UpcomingEvents {
		if o.EventID == second {
			seen = true
			if o.Visibility != "private" || o.LeaderOpened {
				t.Fatalf("second series date %s: visibility=%s leaderOpened=%v", o.Date, o.Visibility, o.LeaderOpened)
			}
		}
	}
	if !seen {
		t.Fatal("second series not listed")
	}
}

// DEC-51: visibility is chosen at creation. Wider than the group goes
// through the override path (audit row, notice, leader_opened).
func TestCreateEventWithVisibility(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	newTestUser(t, app, "admin2", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	w := admin.do("POST", "/api/v1/groups", map[string]any{"name": "Choir", "visibility": "private"}, true)
	var created struct {
		Group groupSummaryJSON `json:"group"`
	}
	decodeJSON(t, w, &created)
	gid := itoa(created.Group.ID)
	start := time.Now().AddDate(0, 0, 3).Format("2006-01-02") + "T19:00"
	mk := func(body map[string]any) models.Event {
		body["startsLocal"], body["durationMinutes"], body["tzid"] = start, 60, "America/Boise"
		w := admin.do("POST", "/api/v1/groups/"+gid+"/events", body, true)
		if w.Code != http.StatusCreated {
			t.Fatalf("create: %d %s", w.Code, w.Body.String())
		}
		var r struct {
			Event struct {
				ID uint `json:"id"`
			} `json:"event"`
		}
		decodeJSON(t, w, &r)
		var ev models.Event
		app.DB.First(&ev, r.Event.ID)
		return ev
	}
	plain := mk(map[string]any{"title": "Plain"})
	if plain.Visibility != "private" || plain.LeaderOpened {
		t.Fatalf("default: %s %v", plain.Visibility, plain.LeaderOpened)
	}
	opened := mk(map[string]any{"title": "Open house", "visibility": "public", "visibilityReason": "guests welcome"})
	if opened.Visibility != "public" || !opened.LeaderOpened {
		t.Fatalf("opened: %s %v", opened.Visibility, opened.LeaderOpened)
	}
	var n int64
	app.DB.Model(&models.AuditLog{}).Where("action = ? AND target_id = ?", "event.visibility_override", opened.ID).Count(&n)
	if n != 1 {
		t.Fatalf("override audit rows: %d", n)
	}
	app.DB.Model(&models.Notification{}).Where("kind = ?", "event.visibility_override").Count(&n)
	if n == 0 {
		t.Fatal("no override notice")
	}
	tight := mk(map[string]any{"title": "Members", "visibility": "invite_only"})
	if tight.Visibility != "invite_only" || tight.LeaderOpened {
		t.Fatalf("tightened: %s %v", tight.Visibility, tight.LeaderOpened)
	}
	if w := admin.do("POST", "/api/v1/groups/"+gid+"/events", map[string]any{"startsLocal": start, "durationMinutes": 60, "visibility": "secret"}, true); w.Code != http.StatusBadRequest {
		t.Fatalf("bad visibility: %d", w.Code)
	}
}
