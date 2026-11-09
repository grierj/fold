package main

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"fold/internal/events"
	"fold/models"
)

func TestRoomLocationMatching(t *testing.T) {
	f := newBookingFixture(t)
	create := func(title string, start time.Time, rule, location string) eventJSON {
		t.Helper()
		body := map[string]any{"title": title, "startsLocal": start.Format("2006-01-02T15:04"), "durationMinutes": 60, "tzid": "UTC", "location": location}
		if rule != "" {
			body["recurrenceRule"] = rule
		}
		w := f.leader.do("POST", "/api/v1/groups/"+itoa(f.group)+"/events", body, true)
		if w.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", title, w.Code, w.Body.String())
		}
		var out struct {
			Event eventJSON `json:"event"`
		}
		decodeJSON(t, w, &out)
		return out.Event
	}
	start := f.startOn.AddDate(0, 0, 1)
	weekly := create("Weekly", start, "FREQ=WEEKLY;COUNT=3", "Fellowship Hall")
	create("Supper", start.AddDate(0, 0, 1), "", " fellowship hall ")
	create("Choir", start.AddDate(0, 0, 2), "", "Chapel")
	// Another event already holds the hall on the weekly series' second date.
	second := start.AddDate(0, 0, 7)
	holder := create("Holder", second, "", "")
	if w := f.leader.do("PUT", "/api/v1/events/"+itoa(holder.ID)+"/dates/"+feedDate(second)+"/room", map[string]any{"roomId": f.hall.ID}, true); w.Code != http.StatusOK {
		t.Fatalf("holder booking: %d", w.Code)
	}

	var list struct {
		Locations []typedLocationJSON `json:"locations"`
	}
	decodeJSON(t, f.admin.do("GET", "/api/v1/admin/rooms/locations", nil, false), &list)
	if len(list.Locations) != 2 || strings.ToLower(list.Locations[0].Location) != "fellowship hall" ||
		list.Locations[0].Dates != 4 || list.Locations[0].Events != 2 || list.Locations[1].Location != "Chapel" {
		t.Fatalf("typed locations: %+v", list.Locations)
	}
	if w := f.leader.do("GET", "/api/v1/admin/rooms/locations", nil, false); w.Code != http.StatusForbidden {
		t.Fatalf("leader listing locations: %d", w.Code)
	}

	match := func() (int, map[string]any) {
		w := f.admin.do("POST", "/api/v1/admin/rooms/locations/match", map[string]any{"location": "FELLOWSHIP HALL", "roomId": f.hall.ID}, true)
		var out map[string]any
		decodeJSON(t, w, &out)
		return w.Code, out
	}
	code, out := match()
	if code != http.StatusOK || out["booked"] != float64(3) || out["busyTotal"] != float64(1) || out["series"] != float64(1) {
		t.Fatalf("match: %d %+v", code, out)
	}
	busy := out["busy"].([]any)[0].(map[string]any)
	if busy["date"] != feedDate(second) || busy["eventTitle"] != "Weekly" {
		t.Fatalf("busy date: %+v", busy)
	}
	var ev models.Event
	f.app.DB.First(&ev, weekly.ID)
	if ev.DefaultRoomID == nil || *ev.DefaultRoomID != f.hall.ID || ev.Location != "Fellowship Hall" {
		t.Fatalf("the weekly series gets the hall as its series room and keeps its typed place: %+v", ev)
	}

	// Again: nothing is booked twice.
	if code, again := match(); code != http.StatusOK || again["booked"] != float64(0) || again["series"] != float64(0) {
		t.Fatalf("match again: %d %+v", code, again)
	}
	if n := contactCount(t, f.app, &models.RoomBooking{}, "room_id = ?", f.hall.ID); n != 4 {
		t.Fatalf("%d hall bookings; want 3 matched plus the holder", n)
	}

	// The calendar names the room once, not "Fellowship Hall · Fellowship Hall".
	w := f.leader.do("GET", "/api/v1/events/"+itoa(weekly.ID)+"/dates/"+feedDate(start)+"/calendar.ics", nil, false)
	if !strings.Contains(w.Body.String(), "LOCATION:Fellowship Hall\r\n") {
		t.Fatalf("calendar LOCATION: %s", w.Body.String())
	}
}

func TestRoomLocationMatchingEdges(t *testing.T) {
	f := newBookingFixture(t)
	if err := f.app.DB.Model(&models.Group{}).Where("id = ?", f.group).Update("location", "Fellowship Hall").Error; err != nil {
		t.Fatal(err)
	}
	start := f.startOn.AddDate(0, 0, 1)
	// No typed place: the group's ("Fellowship Hall") applies, as on its pages.
	fallback := createFeedEvent(t, f.leader, f.group, "Fallback", start, "FREQ=WEEKLY;COUNT=2")
	// One date of it moves to a typed "Annex": that date doesn't match.
	moved := feedDate(start.AddDate(0, 0, 7))
	if w := f.leader.do("PUT", "/api/v1/events/"+itoa(fallback.ID)+"/occurrences/"+moved, map[string]any{"location": "Annex"}, true); w.Code != http.StatusOK {
		t.Fatalf("date override: %d %s", w.Code, w.Body.String())
	}
	// eventA (the fixture's group event, also on the group's place) already
	// has the nursery: it is left as it is.
	if code, _ := f.book(t, f.admin, f.eventA, map[string]any{"roomId": f.nursery.ID}); code != http.StatusOK {
		t.Fatalf("nursery on A: %d", code)
	}

	for _, body := range []map[string]any{{"location": "  ", "roomId": f.hall.ID}, {"location": "Fellowship Hall", "roomId": 0}} {
		if w := f.admin.do("POST", "/api/v1/admin/rooms/locations/match", body, true); w.Code != http.StatusBadRequest {
			t.Fatalf("match %v: %d", body, w.Code)
		}
	}
	if w := f.leader.do("POST", "/api/v1/admin/rooms/locations/match", map[string]any{"location": "Fellowship Hall", "roomId": f.hall.ID}, true); w.Code != http.StatusForbidden {
		t.Fatalf("leader matching: %d", w.Code)
	}
	w := f.admin.do("POST", "/api/v1/admin/rooms/locations/match", map[string]any{"location": "fellowship hall", "roomId": f.hall.ID}, true)
	var out map[string]any
	decodeJSON(t, w, &out)
	// The fallback series' first date and event B (on the group's place).
	if w.Code != http.StatusOK || out["booked"] != float64(2) || out["series"] != float64(1) {
		t.Fatalf("match: %d %+v", w.Code, out)
	}
	var b models.RoomBooking
	f.app.DB.Joins("JOIN event_occurrences o ON o.id = room_bookings.occurrence_id").Where("o.event_id = ?", f.eventA.ID).First(&b)
	if b.RoomID != f.nursery.ID {
		t.Fatalf("A's own booking must stay: %+v", b)
	}
	var occ models.EventOccurrence
	f.app.DB.Where("event_id = ? AND original_date = ?", fallback.ID, moved).First(&occ)
	if occ.LocationOverride == nil || *occ.LocationOverride != "Annex" {
		t.Fatalf("the typed date place must stay: %+v", occ)
	}
	if n := contactCount(t, f.app, &models.RoomBooking{}, "occurrence_id = ?", occ.ID); n != 0 {
		t.Fatal("a date typed elsewhere isn't matched")
	}
	var g models.Group
	f.app.DB.First(&g, f.group)
	if g.Location != "Fellowship Hall" {
		t.Fatal("the group's place must stay")
	}
	if contactCount(t, f.app, &models.AuditLog{}, "action = ? AND target_id = ?", "room.location_matched", f.hall.ID) != 1 {
		t.Fatal("matching is audited")
	}

	if w := f.admin.do("PUT", "/api/v1/admin/settings/rooms", map[string]any{"enabled": false}, true); w.Code != http.StatusOK {
		t.Fatalf("rooms off: %d", w.Code)
	}
	if w := f.admin.do("GET", "/api/v1/admin/rooms/locations", nil, false); w.Code != http.StatusNotFound {
		t.Fatalf("locations while rooms are off: %d", w.Code)
	}
}

// The SQL and the Go form of the rule answer alike for every combination of
// a date's override, the event's place and the group's, and for a
// church-wide event (no group row at all).
func TestEffectivePlaceSQLAgreesWithGo(t *testing.T) {
	db := testApp(t).DB
	u := models.User{Email: "a@example.com", Name: "A", Role: models.RoleAdmin, Status: models.UserActive}
	if err := db.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	empty, own := "", "Room 4"
	overrides := []*string{nil, &empty, &own}
	start := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	for _, evPlace := range []string{"", "Hall"} {
		for _, grPlace := range []string{"", "Church"} {
			for _, churchWide := range []bool{false, true} {
				for _, over := range overrides {
					ev := models.Event{Title: "E", Location: evPlace, Visibility: "public", StartsLocal: "2026-01-01T10:00", DurationMinutes: 60, TZID: "UTC", CreatedBy: u.ID}
					if !churchWide {
						g := models.Group{Name: "G", LeaderID: u.ID, Status: "active", Visibility: "public", Location: grPlace}
						if err := db.Create(&g).Error; err != nil {
							t.Fatal(err)
						}
						ev.GroupID = &g.ID
					}
					if err := db.Create(&ev).Error; err != nil {
						t.Fatal(err)
					}
					occ := models.EventOccurrence{EventID: ev.ID, OriginalDate: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), StartsAt: start, EndsAt: start.Add(time.Hour),
						Status: models.OccurrenceScheduled, LocationOverride: over}
					if err := db.Create(&occ).Error; err != nil {
						t.Fatal(err)
					}
					groupPlace := grPlace
					if churchWide {
						groupPlace = ""
					}
					var effective, series, viaSubquery string
					if err := db.Raw(`SELECT `+events.EffectivePlaceSQL("o.location_override", "e.location", "g.location")+`, `+events.SeriesPlaceSQL("e.location", "g.location")+`,
						`+events.SeriesPlaceSQL("e.location", "(SELECT g2.location FROM groups g2 WHERE g2.id = e.group_id)")+` FROM event_occurrences o JOIN events e ON e.id = o.event_id
						LEFT JOIN groups g ON g.id = e.group_id WHERE o.id = ?`, occ.ID).Row().Scan(&effective, &series, &viaSubquery); err != nil {
						t.Fatal(err)
					}
					if want := events.EffectivePlace(evPlace, over, groupPlace); effective != want {
						t.Errorf("event %q group %q wide %v override %v: SQL %q, Go %q", evPlace, grPlace, churchWide, over, effective, want)
					}
					if want := events.SeriesPlace(evPlace, groupPlace); series != want {
						t.Errorf("event %q group %q wide %v: series SQL %q, Go %q", evPlace, grPlace, churchWide, series, want)
					} else if viaSubquery != want {
						t.Errorf("event %q group %q wide %v: subquery form %q, Go %q", evPlace, grPlace, churchWide, viaSubquery, want)
					}
				}
			}
		}
	}
}
