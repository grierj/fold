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
