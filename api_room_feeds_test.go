package main

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"fold/internal/audit"
	"fold/models"
)

func (f bookingFixture) roomFeed(t *testing.T, roomID uint) string {
	t.Helper()
	w := f.admin.do("POST", "/api/v1/admin/rooms/"+itoa(roomID)+"/feed", nil, true)
	if w.Code != http.StatusOK {
		t.Fatalf("make room feed: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Feed roomFeedJSON `json:"feed"`
	}
	decodeJSON(t, w, &out)
	if !out.Feed.HasToken || out.Feed.URL == "" {
		t.Fatalf("feed: %+v", out.Feed)
	}
	return out.Feed.URL[strings.Index(out.Feed.URL, "/cal/rooms/"):]
}

func TestRoomFeedLinks(t *testing.T) {
	f := newBookingFixture(t)
	path := "/api/v1/admin/rooms/" + itoa(f.hall.ID) + "/feed"
	if w := f.leader.do("POST", path, nil, true); w.Code != http.StatusForbidden {
		t.Fatalf("leader making a room feed: %d", w.Code)
	}
	if w := f.admin.do("GET", path, nil, false); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"hasToken":false`) {
		t.Fatalf("no link yet: %d %s", w.Code, w.Body.String())
	}
	if w := f.admin.do("POST", "/api/v1/admin/rooms/99999/feed", nil, true); w.Code != http.StatusNotFound {
		t.Fatalf("missing room: %d", w.Code)
	}

	first := f.roomFeed(t, f.hall.ID)
	anon := newAPIClient(t, f.app)
	if w := fetchFeed(t, anon, first); w.Code != http.StatusOK {
		t.Fatalf("first link: %d", w.Code)
	}
	if w := f.admin.do("GET", path, nil, false); !strings.Contains(w.Body.String(), `"hasToken":true`) || strings.Contains(w.Body.String(), "/cal/rooms/") {
		t.Fatalf("the link is shown only once: %s", w.Body.String())
	}

	// A new link turns the old one off.
	second := f.roomFeed(t, f.hall.ID)
	if w := fetchFeed(t, anon, first); w.Code != http.StatusNotFound {
		t.Fatalf("replaced link: %d", w.Code)
	}
	if w := fetchFeed(t, anon, second); w.Code != http.StatusOK {
		t.Fatalf("new link: %d", w.Code)
	}
	// Another room's id with this token is a 404.
	if w := fetchFeed(t, anon, strings.Replace(second, "/cal/rooms/"+itoa(f.hall.ID)+"/", "/cal/rooms/"+itoa(f.nursery.ID)+"/", 1)); w.Code != http.StatusNotFound {
		t.Fatalf("token on another room: %d", w.Code)
	}
	if auditCount(t, f.app, audit.RoomFeedCreated) != 2 {
		t.Fatal("making each link should be audited")
	}

	// Rooms off, or the room deactivated: 404, and back again after.
	f.admin.do("PUT", "/api/v1/admin/settings/rooms", map[string]any{"enabled": false}, true)
	if w := fetchFeed(t, anon, second); w.Code != http.StatusNotFound {
		t.Fatalf("rooms off: %d", w.Code)
	}
	f.admin.do("PUT", "/api/v1/admin/settings/rooms", map[string]any{"enabled": true}, true)
	f.app.DB.Model(&models.Room{}).Where("id = ?", f.hall.ID).Update("active", false)
	if w := fetchFeed(t, anon, second); w.Code != http.StatusNotFound {
		t.Fatalf("deactivated room: %d", w.Code)
	}
	f.app.DB.Model(&models.Room{}).Where("id = ?", f.hall.ID).Update("active", true)

	// Turning it off.
	if w := f.admin.do("DELETE", path, nil, true); w.Code != http.StatusOK {
		t.Fatalf("turn off: %d", w.Code)
	}
	if w := fetchFeed(t, anon, second); w.Code != http.StatusNotFound {
		t.Fatalf("revoked link: %d", w.Code)
	}
	if auditCount(t, f.app, audit.RoomFeedRevoked) != 1 {
		t.Fatal("turning the link off should be audited")
	}
	var kept int64
	f.app.DB.Model(&models.RoomFeedToken{}).Where("room_id = ?", f.hall.ID).Count(&kept)
	if kept != 2 {
		t.Fatalf("revoked links stay as history: %d rows", kept)
	}
}

func TestRoomFeedContents(t *testing.T) {
	f := newBookingFixture(t)
	f.app.DB.Model(&models.Event{}).Where("id = ?", f.eventA.ID).Update("description", "Bring a friend")
	if code, body := f.book(t, f.leader, f.eventA, map[string]any{"roomId": f.hall.ID}); code != http.StatusOK {
		t.Fatalf("book A: %d %s", code, body)
	}

	// An invite-only group's event, and a public event whose date was made
	// invite-only, are Reserved; so is a deleted group's event.
	hidden := newTestGroup(t, f.app, f.leaderUser, "Quiet circle", models.GroupActive)
	f.app.DB.Model(hidden).Updates(map[string]any{"visibility": models.VisibilityInviteOnly, "location": "Pastor's study"})
	secret := createFeedEvent(t, f.leader, hidden.ID, "Quiet circle night", f.startOn.Add(3*time.Hour), "")
	f.app.DB.Model(&models.Event{}).Where("id = ?", secret.ID).Update("description", "Private notes")
	narrowed := createFeedEvent(t, f.leader, f.group, "Narrowed night", f.startOn.Add(-3*time.Hour), "")
	opened := createFeedEvent(t, f.leader, hidden.ID, "Opened night", f.startOn.Add(-6*time.Hour), "")
	f.app.DB.Model(&models.Event{}).Where("id = ?", opened.ID).Update("description", "Series secret")
	for _, ev := range []eventJSON{secret, narrowed, opened} {
		if code, body := f.book(t, f.leader, ev, map[string]any{"roomId": f.hall.ID}); code != http.StatusOK {
			t.Fatalf("book %s: %d %s", ev.Title, code, body)
		}
	}
	f.app.DB.Model(&models.EventOccurrence{}).Where("event_id = ?", narrowed.ID).Update("visibility_override", models.VisibilityInviteOnly)
	f.app.DB.Model(&models.EventOccurrence{}).Where("event_id = ?", opened.ID).Update("visibility_override", models.VisibilityPublic)

	w := fetchFeed(t, newAPIClient(t, f.app), f.roomFeed(t, f.hall.ID))
	if w.Code != http.StatusOK || w.Header().Get("ETag") == "" || w.Header().Get("Cache-Control") != "private, max-age=900" {
		t.Fatalf("feed: %d %v", w.Code, w.Header())
	}
	body := w.Body.String()
	for _, want := range []string{"SUMMARY:Event A", "DESCRIPTION:Bring a friend", "LOCATION:Fellowship Hall", "SUMMARY:Opened night",
		"X-WR-CALNAME:Fellowship Hall", "UID:room-" + itoa(f.hall.ID) + "-booking-"} {
		if !strings.Contains(body, want) {
			t.Errorf("feed lacks %q:\n%s", want, body)
		}
	}
	if n := strings.Count(body, "SUMMARY:Reserved"); n != 2 {
		t.Errorf("want 2 Reserved bookings, got %d:\n%s", n, body)
	}
	for _, leak := range []string{"Quiet circle", "Private notes", "Narrowed night", "Pastor's study", "Series secret",
		"event-" + itoa(secret.ID) + "-", "/events/" + itoa(secret.ID) + "/", "/events/" + itoa(narrowed.ID) + "/"} {
		if strings.Contains(body, leak) {
			t.Errorf("feed leaked %q:\n%s", leak, body)
		}
	}

	// A deleted group's dates are Reserved too; a waiting booking isn't in
	// the feed at all.
	f.app.DB.Delete(&models.Group{}, hidden.ID)
	f.app.DB.Model(&models.RoomBooking{}).Where("occurrence_id IN (SELECT id FROM event_occurrences WHERE event_id = ?)", f.eventA.ID).
		Update("status", models.BookingRequested)
	body = fetchFeed(t, newAPIClient(t, f.app), f.roomFeed(t, f.hall.ID)).Body.String()
	if strings.Contains(body, "Event A") || strings.Contains(body, "Opened night") || strings.Count(body, "SUMMARY:Reserved") != 3 {
		t.Errorf("after deleting the group and unconfirming A:\n%s", body)
	}
}

// The feed keeps its bytes while nothing changes, even with many bookings
// that tie on every event field (a 304 on If-None-Match); a cancelled date,
// one past 12 months and an unconfirmed booking are left out; a group's
// own place shows for a group that isn't invite-only.
func TestRoomFeedIsStableAndScoped(t *testing.T) {
	f := newBookingFixture(t)
	f.app.DB.Model(&models.Group{}).Where("id = ?", f.group).Update("location", "Church annex")
	for i := 0; i < 15; i++ {
		ev := createFeedEvent(t, f.leader, f.group, "Night "+itoa(uint(i)), f.startOn.AddDate(0, 0, i+1), "")
		if w := f.leader.do("PUT", "/api/v1/events/"+itoa(ev.ID)+"/dates/"+feedDate(f.startOn.AddDate(0, 0, i+1))+"/room", map[string]any{"roomId": f.hall.ID}, true); w.Code != http.StatusOK {
			t.Fatalf("book %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	link := f.roomFeed(t, f.hall.ID)
	anon := newAPIClient(t, f.app)
	first := fetchFeed(t, anon, link)
	for i := 0; i < 5; i++ {
		if again := fetchFeed(t, anon, link); again.Body.String() != first.Body.String() {
			t.Fatal("the feed's bytes changed with no change to the data")
		}
	}
	if w := fetchFeedIf(t, anon, link, first.Header().Get("ETag")); w.Code != http.StatusNotModified {
		t.Fatalf("If-None-Match: %d", w.Code)
	}
	if !strings.Contains(first.Body.String(), "Church annex") {
		t.Fatal("a non-invite-only group's place should show after the room")
	}

	// Cancel one date, push one past the window, and unconfirm one.
	var occs []models.EventOccurrence
	f.app.DB.Joins("JOIN room_bookings b ON b.occurrence_id = event_occurrences.id").Order("event_occurrences.starts_at").Find(&occs)
	f.app.DB.Model(&occs[0]).Update("status", models.OccurrenceCancelled)
	far := time.Now().UTC().AddDate(1, 0, 2)
	f.app.DB.Model(&occs[1]).Updates(map[string]any{"starts_at": far, "ends_at": far.Add(time.Hour)})
	f.app.DB.Model(&models.RoomBooking{}).Where("occurrence_id = ?", occs[2].ID).Update("status", models.BookingRejected)
	body := fetchFeed(t, anon, link).Body.String()
	if got, want := strings.Count(body, "BEGIN:VEVENT"), strings.Count(first.Body.String(), "BEGIN:VEVENT")-3; got != want {
		t.Fatalf("want %d entries after cancelling, moving and rejecting, got %d", want, got)
	}
}
