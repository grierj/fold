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

	// A private (members-only) group's event is Reserved too: a feed has no
	// viewer, so only public dates are described. A private series' one date
	// opened to public is described; a public series' date made private is
	// Reserved.
	members := newTestGroup(t, f.app, f.leaderUser, "Members circle", models.GroupActive)
	f.app.DB.Model(members).Updates(map[string]any{"visibility": models.VisibilityPrivate, "location": "Members' den"})
	membersNight := createFeedEvent(t, f.leader, members.ID, "Members night", f.startOn.Add(-9*time.Hour), "")
	f.app.DB.Model(&models.Event{}).Where("id = ?", membersNight.ID).Update("description", "Members notes")
	membersOpened := createFeedEvent(t, f.leader, members.ID, "Members opened night", f.startOn.Add(-12*time.Hour), "")
	f.app.DB.Model(&models.Event{}).Where("id = ?", membersOpened.ID).Update("description", "Members series secret")
	pubNarrowed := createFeedEvent(t, f.leader, f.group, "Members only date", f.startOn.Add(-15*time.Hour), "")
	for _, ev := range []eventJSON{membersNight, membersOpened, pubNarrowed} {
		if code, body := f.book(t, f.leader, ev, map[string]any{"roomId": f.hall.ID}); code != http.StatusOK {
			t.Fatalf("book %s: %d %s", ev.Title, code, body)
		}
	}
	f.app.DB.Model(&models.EventOccurrence{}).Where("event_id = ?", membersOpened.ID).Update("visibility_override", models.VisibilityPublic)
	f.app.DB.Model(&models.EventOccurrence{}).Where("event_id = ?", pubNarrowed.ID).Update("visibility_override", models.VisibilityPrivate)

	w := fetchFeed(t, newAPIClient(t, f.app), f.roomFeed(t, f.hall.ID))
	if w.Code != http.StatusOK || w.Header().Get("ETag") == "" || w.Header().Get("Cache-Control") != "private, max-age=900" {
		t.Fatalf("feed: %d %v", w.Code, w.Header())
	}
	body := w.Body.String()
	for _, want := range []string{"SUMMARY:Event A", "DESCRIPTION:Bring a friend", "LOCATION:Fellowship Hall", "SUMMARY:Opened night", "SUMMARY:Members opened night",
		"X-WR-CALNAME:Fellowship Hall", "UID:room-" + itoa(f.hall.ID) + "-booking-"} {
		if !strings.Contains(body, want) {
			t.Errorf("feed lacks %q:\n%s", want, body)
		}
	}
	// A described booking's description ends with its date page link, with
	// YYYY-MM-DD in the path (unfolded: iCalendar wraps long lines).
	if unfolded := strings.ReplaceAll(body, "\r\n ", ""); !strings.Contains(unfolded, "/events/"+itoa(f.eventA.ID)+"/dates/"+feedDate(f.startOn)) {
		t.Errorf("feed lacks Event A's date link for %s:\n%s", feedDate(f.startOn), body)
	}
	if n := strings.Count(body, "SUMMARY:Reserved"); n != 4 {
		t.Errorf("want 4 Reserved bookings, got %d:\n%s", n, body)
	}
	for _, leak := range []string{"Quiet circle", "Private notes", "Narrowed night", "Pastor's study", "Series secret",
		"Members night", "Members notes", "Members only date", "Members series secret", "Members' den",
		"/events/" + itoa(membersNight.ID) + "/", "/events/" + itoa(pubNarrowed.ID) + "/",
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
	if strings.Contains(body, "Event A") || strings.Contains(body, "Opened night") || strings.Count(body, "SUMMARY:Reserved") != 5 {
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

// feedVEvent returns the VEVENT block whose UID starts with uidPrefix.
func feedVEvent(t *testing.T, body, uidPrefix string) string {
	t.Helper()
	for _, block := range strings.Split(unfoldFeed(body), "BEGIN:VEVENT\r\n") {
		if strings.Contains(block, "UID:"+uidPrefix) {
			return block
		}
	}
	t.Fatalf("feed has no VEVENT with UID %q:\n%s", uidPrefix, body)
	return ""
}

func feedLine(block, prefix string) string {
	for _, line := range strings.Split(block, "\r\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	return ""
}

// A booking made while its group was active turns Reserved when the group is
// archived or set back to pending, because a signed-out viewer can't see the
// date of a group that isn't active (#357). The busy block, its UID and
// times stay, its DTSTAMP and the feed's ETag move so subscribed calendars
// replace the old title, and reactivating the group brings the details back.
func TestRoomFeedHidesEventsOfInactiveGroups(t *testing.T) {
	f := newBookingFixture(t)
	archived := newTestGroup(t, f.app, f.leaderUser, "Soon archived", models.GroupActive)
	pending := newTestGroup(t, f.app, f.leaderUser, "Soon pending", models.GroupActive)
	f.app.DB.Model(&models.Group{}).Where("id IN ?", []uint{archived.ID, pending.ID}).Update("location", "Annex lounge")

	type booked struct {
		ev    eventJSON
		title string
		desc  string
	}
	mk := func(gid uint, title, desc string, at time.Time) booked {
		ev := createFeedEvent(t, f.leader, gid, title, at, "")
		f.app.DB.Model(&models.Event{}).Where("id = ?", ev.ID).Update("description", desc)
		return booked{ev: ev, title: title, desc: desc}
	}
	soonArchived := mk(archived.ID, "Archived night", "Archived notes", f.startOn.Add(-3*time.Hour))
	soonPending := mk(pending.ID, "Pending night", "Pending notes", f.startOn.Add(-6*time.Hour))
	w := f.admin.do("POST", "/api/v1/events", map[string]any{
		"title": "Church night", "startsLocal": f.startOn.Add(-9 * time.Hour).Format("2006-01-02T15:04"),
		"durationMinutes": 60, "tzid": "UTC", "visibility": "public",
	}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("church event: %d %s", w.Code, w.Body.String())
	}
	var made struct {
		Event eventJSON `json:"event"`
	}
	decodeJSON(t, w, &made)
	church := booked{ev: made.Event, title: "Church night"}
	f.app.DB.Model(&models.Event{}).Where("id = ?", church.ev.ID).Update("description", "Church notes")
	church.desc = "Church notes"

	all := []booked{soonArchived, soonPending, church}
	for _, b := range all {
		if code, body := f.book(t, f.admin, b.ev, map[string]any{"roomId": f.hall.ID}); code != http.StatusOK {
			t.Fatalf("book %s: %d %s", b.title, code, body)
		}
	}
	// A public date override must not expose a date once its group is archived.
	if err := f.app.DB.Model(&models.Event{}).Where("id = ?", soonArchived.ev.ID).Update("visibility", models.VisibilityInviteOnly).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.app.DB.Model(&models.EventOccurrence{}).Where("event_id = ?", soonArchived.ev.ID).Update("visibility_override", models.VisibilityPublic).Error; err != nil {
		t.Fatal(err)
	}
	// DTSTAMP has second resolution; backdate the fixture instead of waiting.
	old := time.Now().Add(-5 * time.Second)
	for _, q := range []struct {
		sql string
		ids []uint
	}{
		{"UPDATE groups SET updated_at = ? WHERE id IN ?", []uint{archived.ID, pending.ID}},
		{"UPDATE events SET updated_at = ? WHERE id IN ?", []uint{soonArchived.ev.ID, soonPending.ev.ID}},
		{"UPDATE event_occurrences SET updated_at = ? WHERE event_id IN ?", []uint{soonArchived.ev.ID, soonPending.ev.ID}},
		{"UPDATE room_bookings SET updated_at = ? WHERE occurrence_id IN (SELECT id FROM event_occurrences WHERE event_id IN ?)", []uint{soonArchived.ev.ID, soonPending.ev.ID}},
	} {
		if err := f.app.DB.Exec(q.sql, old, q.ids).Error; err != nil {
			t.Fatal(err)
		}
	}

	link := f.roomFeed(t, f.hall.ID)
	anon := newAPIClient(t, f.app)
	first := fetchFeed(t, anon, link)
	before := first.Body.String()
	for _, b := range all {
		if !strings.Contains(before, "SUMMARY:"+b.title) || (b.ev.ID != soonArchived.ev.ID && !strings.Contains(before, "DESCRIPTION:"+b.desc)) {
			t.Fatalf("while active, %s should be described:\n%s", b.title, before)
		}
	}
	reservedBefore := strings.Count(before, "SUMMARY:Reserved")

	if w := f.leader.do("PUT", "/api/v1/groups/"+itoa(archived.ID), map[string]any{"status": models.GroupArchived}, true); w.Code != http.StatusOK {
		t.Fatalf("archive: %d %s", w.Code, w.Body.String())
	}
	if w := f.admin.do("PUT", "/api/v1/groups/"+itoa(pending.ID), map[string]any{"status": models.GroupPending}, true); w.Code != http.StatusOK {
		t.Fatalf("set pending: %d %s", w.Code, w.Body.String())
	}

	second := fetchFeed(t, anon, link)
	after := second.Body.String()
	for _, leak := range []string{"Archived night", "Archived notes", "Pending night", "Pending notes", "Annex lounge",
		"/events/" + itoa(soonArchived.ev.ID) + "/", "/events/" + itoa(soonPending.ev.ID) + "/"} {
		if strings.Contains(after, leak) {
			t.Errorf("feed leaked %q after the groups stopped being active:\n%s", leak, after)
		}
	}
	if n := strings.Count(after, "SUMMARY:Reserved"); n != reservedBefore+2 {
		t.Errorf("want %d Reserved blocks, got %d:\n%s", reservedBefore+2, n, after)
	}
	if !strings.Contains(after, "SUMMARY:Church night") || !strings.Contains(after, "DESCRIPTION:Church notes") {
		t.Errorf("the church-wide event should still be described:\n%s", after)
	}
	if second.Header().Get("ETag") == first.Header().Get("ETag") {
		t.Error("the ETag should change when bookings turn Reserved")
	}
	for _, b := range []booked{soonArchived, soonPending} {
		var bk models.RoomBooking
		if err := f.app.DB.Joins("JOIN event_occurrences o ON o.id = room_bookings.occurrence_id").
			Where("o.event_id = ?", b.ev.ID).First(&bk).Error; err != nil {
			t.Fatalf("booking of %s: %v", b.title, err)
		}
		uid := "room-" + itoa(f.hall.ID) + "-booking-" + itoa(bk.ID) + "@"
		was, now := feedVEvent(t, before, uid), feedVEvent(t, after, uid)
		if !strings.Contains(now, "SUMMARY:Reserved") {
			t.Errorf("%s should be a Reserved block:\n%s", b.title, now)
		}
		for _, keep := range []string{"DTSTART", "DTEND"} {
			if feedLine(was, keep) != feedLine(now, keep) {
				t.Errorf("%s: %s changed: %q -> %q", b.title, keep, feedLine(was, keep), feedLine(now, keep))
			}
		}
		if feedLine(was, "DTSTAMP:") == feedLine(now, "DTSTAMP:") {
			t.Errorf("%s: DTSTAMP should move when it turns Reserved: %q", b.title, feedLine(now, "DTSTAMP:"))
		}
	}

	// Approving or reactivating the groups brings the details back.
	if w := f.admin.do("PUT", "/api/v1/groups/"+itoa(archived.ID), map[string]any{"status": models.GroupActive}, true); w.Code != http.StatusOK {
		t.Fatalf("reactivate: %d %s", w.Code, w.Body.String())
	}
	if err := f.app.DB.Model(&models.Group{}).Where("id = ?", pending.ID).UpdateColumn("updated_at", old).Error; err != nil {
		t.Fatal(err)
	}
	beforeApproval := fetchFeed(t, anon, link).Body.String()
	if w := f.admin.do("POST", "/api/v1/admin/groups/"+itoa(pending.ID)+"/approve", map[string]any{}, true); w.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", w.Code, w.Body.String())
	}
	back := fetchFeed(t, anon, link).Body.String()
	var pendingBooking models.RoomBooking
	if err := f.app.DB.Joins("JOIN event_occurrences o ON o.id = room_bookings.occurrence_id").
		Where("o.event_id = ?", soonPending.ev.ID).First(&pendingBooking).Error; err != nil {
		t.Fatal(err)
	}
	uid := "room-" + itoa(f.hall.ID) + "-booking-" + itoa(pendingBooking.ID) + "@"
	if feedLine(feedVEvent(t, beforeApproval, uid), "DTSTAMP:") == feedLine(feedVEvent(t, back, uid), "DTSTAMP:") {
		t.Error("approval should refresh the booking DTSTAMP")
	}
	for _, b := range []booked{soonArchived, soonPending} {
		if !strings.Contains(back, "SUMMARY:"+b.title) || (b.ev.ID != soonArchived.ev.ID && !strings.Contains(back, "DESCRIPTION:"+b.desc)) {
			t.Errorf("after reactivating, %s should be described again:\n%s", b.title, back)
		}
	}
}
