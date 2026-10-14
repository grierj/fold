package main

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"fold/models"
)

// Guard for the "Sun 1:00 AM UTC" bug: every group summary that carries a
// next event must also carry its zone and wall clock, on the list API as
// well as the detail API, and an event created without a zone takes the
// church's zone, never UTC.
func TestGroupListCarriesNextEventZone(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	if w := admin.do("PUT", "/api/v1/admin/timezone", map[string]any{"timezone": "America/Denver"}, true); w.Code != http.StatusOK {
		t.Fatalf("set tz: %d %s", w.Code, w.Body.String())
	}
	w := admin.do("POST", "/api/v1/groups", map[string]any{"name": "Choir"}, true)
	var created struct {
		Group groupSummaryJSON `json:"group"`
	}
	decodeJSON(t, w, &created)
	gid := itoa(created.Group.ID)
	admin.do("POST", "/api/v1/admin/groups/"+gid+"/approve", map[string]any{}, true)

	// Next Saturday at 7 PM, no tzid sent: the church zone applies.
	den, _ := time.LoadLocation("America/Denver")
	d := time.Now().In(den).AddDate(0, 0, 1)
	for d.Weekday() != time.Saturday {
		d = d.AddDate(0, 0, 1)
	}
	local := d.Format("2006-01-02") + "T19:00"
	if w := admin.do("POST", "/api/v1/groups/"+gid+"/events", map[string]any{
		"startsLocal": local, "durationMinutes": 60,
	}, true); w.Code != http.StatusCreated {
		t.Fatalf("create event: %d %s", w.Code, w.Body.String())
	}

	check := func(where string, g groupSummaryJSON) {
		t.Helper()
		if g.NextEventAt == nil {
			t.Fatalf("%s: no next event", where)
		}
		if g.NextEventTzid != "America/Denver" || g.NextEventLocal != local {
			t.Fatalf("%s: tzid=%q local=%q, want America/Denver %s", where, g.NextEventTzid, g.NextEventLocal, local)
		}
	}
	w = admin.do("GET", "/api/v1/groups", nil, false)
	var list struct {
		Groups []groupSummaryJSON `json:"groups"`
	}
	decodeJSON(t, w, &list)
	found := false
	for _, g := range list.Groups {
		if g.ID == created.Group.ID {
			check("list", g)
			found = true
		}
	}
	if !found {
		t.Fatalf("group not listed: %s", w.Body.String())
	}
	w = admin.do("GET", "/api/v1/groups/"+gid, nil, false)
	var detail struct {
		Group groupSummaryJSON `json:"group"`
	}
	decodeJSON(t, w, &detail)
	check("detail", detail.Group)
}

func TestLocalWhenNamesTheZone(t *testing.T) {
	at := time.Date(2035, 10, 20, 1, 0, 0, 0, time.UTC)
	got := localWhen(at, "America/Denver")
	if !strings.Contains(got, "Fri, Oct 19") || !strings.Contains(got, "7:00 PM") || !strings.Contains(got, "MDT") {
		t.Fatalf("localWhen: %q", got)
	}
}
