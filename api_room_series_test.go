package main

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"fold/internal/audit"
	"fold/internal/events"
	"fold/internal/notify"
	"fold/models"
)

func (f bookingFixture) seriesRoom(t *testing.T, c *apiClient, ev eventJSON, body map[string]any) (int, seriesBookingJSON) {
	t.Helper()
	w := c.do("PUT", "/api/v1/events/"+itoa(ev.ID)+"/room", body, true)
	var out seriesBookingJSON
	if w.Code == http.StatusOK {
		decodeJSON(t, w, &out)
	}
	return w.Code, out
}

// seriesBookings counts an event's bookings by status.
func seriesBookings(t *testing.T, app *App, eventID uint, status string) int64 {
	t.Helper()
	return contactCount(t, app, &models.RoomBooking{},
		"status = ? AND occurrence_id IN (SELECT id FROM event_occurrences WHERE event_id = ?)", status, eventID)
}

func TestRoomSeriesBooksFreeDatesAndFlagsBusyOnes(t *testing.T) {
	f := newBookingFixture(t)
	// A holds the hall on the first date, padded [18:30, 20:00).
	if code, body := f.book(t, f.leader, f.eventA, map[string]any{"roomId": f.hall.ID}); code != http.StatusOK {
		t.Fatalf("book A: %d %s", code, body)
	}
	// A weekly series of 10 at 19:30: the hall is busy on its first date only.
	series := createFeedEvent(t, f.leader, f.group, "Weekly", f.startOn.Add(30*time.Minute), "FREQ=WEEKLY;COUNT=10")
	chapel := func() roomJSON {
		var out struct {
			Room roomJSON `json:"room"`
		}
		decodeJSON(t, f.admin.do("POST", "/api/v1/admin/rooms", map[string]any{"name": "Chapel"}, true), &out)
		return out.Room
	}()
	// The third date gets its own room first; the series room leaves it.
	third := feedDate(f.startOn.AddDate(0, 0, 14))
	if w := f.leader.do("PUT", "/api/v1/events/"+itoa(series.ID)+"/dates/"+third+"/room", map[string]any{"roomId": chapel.ID}, true); w.Code != http.StatusOK {
		t.Fatalf("book the chapel on one date: %d %s", w.Code, w.Body.String())
	}

	var picker struct {
		Rooms       []seriesRoomJSON    `json:"rooms"`
		Dates       int                 `json:"dates"`
		DefaultRoom *occurrenceRoomJSON `json:"defaultRoom"`
		NeedsRoom   []needsRoomJSON     `json:"needsRoom"`
	}
	decodeJSON(t, f.leader.do("GET", "/api/v1/events/"+itoa(series.ID)+"/rooms", nil, false), &picker)
	busy := map[uint]int{}
	for _, r := range picker.Rooms {
		busy[r.ID] = r.BusyDates
		if r.ID == f.nursery.ID {
			t.Fatal("a leader's series picker lists the restricted nursery")
		}
	}
	if picker.Dates != 10 || busy[f.hall.ID] != 1 || busy[chapel.ID] != 0 || picker.DefaultRoom != nil {
		t.Fatalf("series picker: %+v", picker)
	}

	code, out := f.seriesRoom(t, f.leader, series, map[string]any{"roomId": f.hall.ID})
	if code != http.StatusOK || out.Booked != 8 || out.Kept != 1 || len(out.Lost) != 1 || out.Lost[0] != feedDate(f.startOn) ||
		out.DefaultRoom == nil || out.DefaultRoom.ID != f.hall.ID {
		t.Fatalf("book the series: %d %+v", code, out)
	}
	if n := seriesBookings(t, f.app, series.ID, models.BookingConfirmed); n != 9 { // 8 in the hall, 1 in the chapel
		t.Fatalf("%d confirmed bookings", n)
	}
	if contactCount(t, f.app, &models.AuditLog{}, "action = ? AND target_id = ?", audit.RoomBookingSeries, series.ID) != 1 {
		t.Fatal("booking a series room is audited")
	}
	decodeJSON(t, f.leader.do("GET", "/api/v1/events/"+itoa(series.ID)+"/rooms", nil, false), &picker)
	if len(picker.NeedsRoom) != 1 || picker.NeedsRoom[0].Date != feedDate(f.startOn) || picker.NeedsRoom[0].RoomName != "Fellowship Hall" ||
		picker.DefaultRoom == nil || picker.DefaultRoom.ID != f.hall.ID {
		t.Fatalf("needs a room: %+v", picker)
	}

	// Booking it again changes nothing.
	if code, again := f.seriesRoom(t, f.leader, series, map[string]any{"roomId": f.hall.ID}); code != http.StatusOK || again.Booked != 0 || len(again.Lost) != 1 {
		t.Fatalf("book the series again: %d %+v", code, again)
	}

	// Editing the series time moves every booking.
	later := f.startOn.Add(90 * time.Minute)
	if w := f.leader.do("PUT", "/api/v1/events/"+itoa(series.ID), map[string]any{"startsLocal": later.Format("2006-01-02T15:04")}, true); w.Code != http.StatusOK {
		t.Fatalf("series edit: %d %s", w.Code, w.Body.String())
	}
	if n := contactCount(t, f.app, &models.RoomBooking{},
		"status = ? AND occurrence_id IN (SELECT id FROM event_occurrences WHERE event_id = ? AND starts_at = room_bookings.starts_at) AND extract(hour FROM starts_at AT TIME ZONE 'UTC') = 20",
		models.BookingConfirmed, series.ID); n != 9 {
		t.Fatalf("%d bookings moved to 20:30", n)
	}

	// Stopping the series room frees its bookings and keeps the chapel.
	if code, stop := f.seriesRoom(t, f.leader, series, map[string]any{"roomId": nil}); code != http.StatusOK || stop.DefaultRoom != nil {
		t.Fatalf("stop the series room: %d %+v", code, stop)
	}
	if n := contactCount(t, f.app, &models.RoomBooking{}, "occurrence_id IN (SELECT id FROM event_occurrences WHERE event_id = ?)", series.ID); n != 1 {
		t.Fatalf("%d bookings left; only the chapel should stay", n)
	}
}

func TestRoomSeriesGuards(t *testing.T) {
	f := newBookingFixture(t)
	series := createFeedEvent(t, f.leader, f.group, "Weekly", f.startOn, "FREQ=WEEKLY;COUNT=3")
	if code, _ := f.seriesRoom(t, f.leader, series, map[string]any{"roomId": f.nursery.ID}); code != http.StatusForbidden {
		t.Fatalf("leader booking the nursery for a series: %d", code)
	}
	newTestUser(t, f.app, "outsider", "password1234", models.RoleMember)
	outsider := loginAPI(t, f.app, "outsider", "password1234")
	if w := outsider.do("GET", "/api/v1/events/"+itoa(series.ID)+"/rooms", nil, false); w.Code != http.StatusForbidden && w.Code != http.StatusNotFound {
		t.Fatalf("outsider listing series rooms: %d", w.Code)
	}
	if code, _ := f.seriesRoom(t, outsider, series, map[string]any{"roomId": f.hall.ID}); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Fatalf("outsider booking a series room: %d", code)
	}
	if w := outsider.do("POST", "/api/v1/rooms/check", map[string]any{"groupId": f.group, "startsLocal": f.startOn.Format("2006-01-02T15:04"), "durationMinutes": 60}, true); w.Code != http.StatusForbidden && w.Code != http.StatusNotFound {
		t.Fatalf("outsider checking rooms for a group: %d", w.Code)
	}
	if w := outsider.do("POST", "/api/v1/rooms/check", map[string]any{"startsLocal": f.startOn.Format("2006-01-02T15:04"), "durationMinutes": 60}, true); w.Code != http.StatusForbidden {
		t.Fatalf("non-admin checking rooms for a church-wide event: %d", w.Code)
	}

	// An admin's reason double-books the busy dates instead.
	if code, body := f.book(t, f.leader, f.eventA, map[string]any{"roomId": f.hall.ID}); code != http.StatusOK {
		t.Fatalf("book A: %d %s", code, body)
	}
	code, out := f.seriesRoom(t, f.admin, series, map[string]any{"roomId": f.hall.ID, "overlapReason": "Shared with A"})
	if code != http.StatusOK || out.Booked != 3 || len(out.Lost) != 0 {
		t.Fatalf("admin double-booking the series: %d %+v", code, out)
	}
	// A leader can't stop a series room holding an admin's double-booking
	// on that date; the rest are released.
	if code, _ := f.seriesRoom(t, f.leader, series, map[string]any{"roomId": nil}); code != http.StatusOK {
		t.Fatalf("leader stopping the series room: %d", code)
	}
	if n := contactCount(t, f.app, &models.RoomBooking{}, "allow_overlap AND occurrence_id IN (SELECT id FROM event_occurrences WHERE event_id = ?)", series.ID); n != 1 {
		t.Fatalf("the admin's double-booking should stay: %d", n)
	}
}

func TestRoomSeriesOnCreate(t *testing.T) {
	f := newBookingFixture(t)
	start := f.startOn.AddDate(0, 0, 1)
	var check struct {
		Rooms []seriesRoomJSON `json:"rooms"`
		Dates int              `json:"dates"`
	}
	decodeJSON(t, f.leader.do("POST", "/api/v1/rooms/check", map[string]any{"groupId": f.group,
		"startsLocal": start.Format("2006-01-02T15:04"), "durationMinutes": 60, "tzid": "UTC", "recurrenceRule": "FREQ=WEEKLY;COUNT=3", "capacity": 30}, true), &check)
	if check.Dates != 3 || len(check.Rooms) != 1 || check.Rooms[0].BusyDates != 0 || !check.Rooms[0].TooSmall {
		t.Fatalf("rooms check: %+v", check)
	}

	create := func(title string, roomID uint) int {
		return f.leader.do("POST", "/api/v1/groups/"+itoa(f.group)+"/events", map[string]any{"title": title,
			"startsLocal": start.Format("2006-01-02T15:04"), "durationMinutes": 60, "tzid": "UTC",
			"recurrenceRule": "FREQ=WEEKLY;COUNT=3", "roomId": roomID}, true).Code
	}
	if code := create("Booked", f.hall.ID); code != http.StatusCreated {
		t.Fatalf("create with a room: %d", code)
	}
	var ev models.Event
	f.app.DB.Where("title = ?", "Booked").First(&ev)
	if ev.DefaultRoomID == nil || *ev.DefaultRoomID != f.hall.ID || seriesBookings(t, f.app, ev.ID, models.BookingConfirmed) != 3 {
		t.Fatalf("created series should book its 3 dates: %+v", ev)
	}
	if code := create("Nursery night", f.nursery.ID); code != http.StatusForbidden {
		t.Fatalf("leader creating with the nursery: %d", code)
	}
	if contactCount(t, f.app, &models.Event{}, "title = ?", "Nursery night") != 0 {
		t.Fatal("a refused room must not leave the event behind")
	}

	// The room is the place in calendars: "Fellowship Hall" leads LOCATION.
	w := f.leader.do("GET", "/api/v1/events/"+itoa(ev.ID)+"/dates/"+feedDate(start)+"/calendar.ics", nil, false)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "LOCATION:Fellowship Hall") {
		t.Fatalf("calendar LOCATION: %d %s", w.Code, w.Body.String())
	}
}

func TestRoomSeriesExtenderBooksNewDates(t *testing.T) {
	f := newBookingFixture(t)
	series := createFeedEvent(t, f.leader, f.group, "Weekly", f.startOn.AddDate(0, 0, 1), "FREQ=WEEKLY;COUNT=4")
	if code, out := f.seriesRoom(t, f.leader, series, map[string]any{"roomId": f.hall.ID}); code != http.StatusOK || out.Booked != 4 {
		t.Fatalf("book the series: %d %+v", code, out)
	}
	// The last two dates go missing (as if not yet generated), and another
	// event takes the hall on the last one.
	third, fourth := feedDate(f.startOn.AddDate(0, 0, 15)), feedDate(f.startOn.AddDate(0, 0, 22))
	if err := f.app.DB.Where("event_id = ? AND original_date IN ?", series.ID, []string{third, fourth}).Delete(&models.EventOccurrence{}).Error; err != nil {
		t.Fatal(err)
	}
	other := createFeedEvent(t, f.leader, f.group, "Other", f.startOn.AddDate(0, 0, 22), "")
	if w := f.leader.do("PUT", "/api/v1/events/"+itoa(other.ID)+"/dates/"+fourth+"/room", map[string]any{"roomId": f.hall.ID}, true); w.Code != http.StatusOK {
		t.Fatalf("book the other event: %d %s", w.Code, w.Body.String())
	}

	var emailed []uint
	if err := events.Extend(f.app.DB, time.Now().UTC(), f.app.afterExtend(&emailed)); err != nil {
		t.Fatal(err)
	}
	if n := seriesBookings(t, f.app, series.ID, models.BookingConfirmed); n != 3 {
		t.Fatalf("%d confirmed after the extender; want 3", n)
	}
	if n := seriesBookings(t, f.app, series.ID, models.BookingRequested); n != 1 {
		t.Fatalf("%d requested after the extender; want 1", n)
	}
	var n models.Notification
	if err := f.app.DB.Where("user_id = ? AND kind = ?", f.leaderUser.ID, notify.RoomLost).First(&n).Error; err != nil {
		t.Fatalf("the leader should hear the new date lost its room: %v", err)
	}
	// Running it again books nothing twice.
	if err := events.Extend(f.app.DB, time.Now().UTC(), f.app.afterExtend(&emailed)); err != nil {
		t.Fatal(err)
	}
	if n := contactCount(t, f.app, &models.RoomBooking{}, "occurrence_id IN (SELECT id FROM event_occurrences WHERE event_id = ?)", series.ID); n != 4 {
		t.Fatalf("%d bookings after a second run", n)
	}
}

func TestRoomSeriesEditsBookAddedAndRestoredDates(t *testing.T) {
	f := newBookingFixture(t)
	start := f.startOn.AddDate(0, 0, 1)
	series := createFeedEvent(t, f.leader, f.group, "Weekly", start, "FREQ=WEEKLY;COUNT=3")
	if code, out := f.seriesRoom(t, f.leader, series, map[string]any{"roomId": f.hall.ID}); code != http.StatusOK || out.Booked != 3 {
		t.Fatalf("book the series: %d %+v", code, out)
	}
	edit := func(rule string) {
		t.Helper()
		if w := f.leader.do("PUT", "/api/v1/events/"+itoa(series.ID), map[string]any{"recurrenceRule": rule}, true); w.Code != http.StatusOK {
			t.Fatalf("series edit %s: %d %s", rule, w.Code, w.Body.String())
		}
	}
	// Shortening cancels and frees the third date; another event takes the
	// hall then.
	edit("FREQ=WEEKLY;COUNT=2")
	third := start.AddDate(0, 0, 14)
	other := createFeedEvent(t, f.leader, f.group, "Other", third, "")
	if w := f.leader.do("PUT", "/api/v1/events/"+itoa(other.ID)+"/dates/"+feedDate(third)+"/room", map[string]any{"roomId": f.hall.ID}, true); w.Code != http.StatusOK {
		t.Fatalf("book the other event: %d %s", w.Code, w.Body.String())
	}
	// Lengthening restores the third date (busy: requested, and the leader
	// is told) and adds a fourth (booked).
	edit("FREQ=WEEKLY;COUNT=4")
	if n := seriesBookings(t, f.app, series.ID, models.BookingConfirmed); n != 3 {
		t.Fatalf("%d confirmed; want the first, second and new fourth date", n)
	}
	if n := seriesBookings(t, f.app, series.ID, models.BookingRequested); n != 1 {
		t.Fatalf("%d requested; want the restored, busy third date", n)
	}
	if contactCount(t, f.app, &models.Notification{}, "user_id = ? AND kind = ?", f.leaderUser.ID, notify.RoomLost) != 1 {
		t.Fatal("the restored date losing its room should be announced once")
	}
}

func TestRoomSeriesSwitchRooms(t *testing.T) {
	f := newBookingFixture(t)
	var chapel struct {
		Room roomJSON `json:"room"`
	}
	decodeJSON(t, f.admin.do("POST", "/api/v1/admin/rooms", map[string]any{"name": "Chapel"}, true), &chapel)
	var annex struct {
		Room roomJSON `json:"room"`
	}
	decodeJSON(t, f.admin.do("POST", "/api/v1/admin/rooms", map[string]any{"name": "Annex"}, true), &annex)
	start := f.startOn.AddDate(0, 0, 1)
	series := createFeedEvent(t, f.leader, f.group, "Weekly", start, "FREQ=WEEKLY;COUNT=3")
	if code, _ := f.seriesRoom(t, f.leader, series, map[string]any{"roomId": f.hall.ID}); code != http.StatusOK {
		t.Fatalf("book the hall: %d", code)
	}
	// The second date gets the annex on its own; the chapel is busy on the third.
	second, third := feedDate(start.AddDate(0, 0, 7)), start.AddDate(0, 0, 14)
	if w := f.leader.do("PUT", "/api/v1/events/"+itoa(series.ID)+"/dates/"+second+"/room", map[string]any{"roomId": annex.Room.ID}, true); w.Code != http.StatusOK {
		t.Fatalf("annex on one date: %d", w.Code)
	}
	other := createFeedEvent(t, f.leader, f.group, "Other", third, "")
	if w := f.leader.do("PUT", "/api/v1/events/"+itoa(other.ID)+"/dates/"+feedDate(third)+"/room", map[string]any{"roomId": chapel.Room.ID}, true); w.Code != http.StatusOK {
		t.Fatalf("chapel for the other event: %d", w.Code)
	}

	code, out := f.seriesRoom(t, f.leader, series, map[string]any{"roomId": chapel.Room.ID})
	if code != http.StatusOK || out.Booked != 1 || out.Kept != 1 || len(out.Lost) != 1 || out.Lost[0] != feedDate(third) {
		t.Fatalf("switch to the chapel: %d %+v", code, out)
	}
	rooms := map[string]uint{}
	var rows []struct {
		OriginalDate string
		RoomID       uint
		Status       string
	}
	f.app.DB.Raw(`SELECT o.original_date, b.room_id, b.status FROM room_bookings b JOIN event_occurrences o ON o.id = b.occurrence_id
		WHERE o.event_id = ?`, series.ID).Scan(&rows)
	for _, r := range rows {
		rooms[r.OriginalDate+" "+r.Status] = r.RoomID
	}
	if rooms[feedDate(start)+" confirmed"] != chapel.Room.ID || rooms[second+" confirmed"] != annex.Room.ID ||
		rooms[feedDate(third)+" requested"] != chapel.Room.ID || len(rows) != 3 {
		t.Fatalf("after the switch: %+v", rows)
	}
	if contactCount(t, f.app, &models.RoomBooking{}, "room_id = ? AND occurrence_id IN (SELECT id FROM event_occurrences WHERE event_id = ?)", f.hall.ID, series.ID) != 0 {
		t.Fatal("switching rooms must free the old series room on every date")
	}
}

func TestRoomSeriesChurchWideAndRoomsOff(t *testing.T) {
	f := newBookingFixture(t)
	start := f.startOn.AddDate(0, 0, 2)
	body := map[string]any{"title": "Church night", "startsLocal": start.Format("2006-01-02T15:04"),
		"durationMinutes": 60, "tzid": "UTC", "recurrenceRule": "FREQ=WEEKLY;COUNT=2", "roomId": f.nursery.ID}
	if w := f.admin.do("POST", "/api/v1/events", body, true); w.Code != http.StatusCreated {
		t.Fatalf("admin creating a church-wide event in the nursery: %d %s", w.Code, w.Body.String())
	}
	var ev models.Event
	f.app.DB.Where("title = ?", "Church night").First(&ev)
	if seriesBookings(t, f.app, ev.ID, models.BookingConfirmed) != 2 {
		t.Fatal("the church-wide series should book both dates")
	}
	// The public feed names the room (DEC-104: a restricted room shows as the place).
	anon := newAPIClient(t, f.app)
	if w := fetchFeed(t, anon, "/cal/public.ics"); !strings.Contains(w.Body.String(), "LOCATION:Nursery") {
		t.Fatalf("public feed LOCATION: %s", w.Body.String())
	}

	// Rooms off: create with a room is refused, and the extender books nothing.
	if w := f.admin.do("PUT", "/api/v1/admin/settings/rooms", map[string]any{"enabled": false}, true); w.Code != http.StatusOK {
		t.Fatalf("rooms off: %d", w.Code)
	}
	body["title"] = "Off night"
	if w := f.admin.do("POST", "/api/v1/events", body, true); w.Code != http.StatusNotFound {
		t.Fatalf("create with a room while rooms are off: %d", w.Code)
	}
	if err := f.app.DB.Where("event_id = ?", ev.ID).Delete(&models.EventOccurrence{}).Error; err != nil {
		t.Fatal(err)
	}
	var emailed []uint
	if err := events.Extend(f.app.DB, time.Now().UTC(), f.app.afterExtend(&emailed)); err != nil {
		t.Fatal(err)
	}
	if n := contactCount(t, f.app, &models.EventOccurrence{}, "event_id = ?", ev.ID); n != 2 {
		t.Fatalf("%d dates regenerated", n)
	}
	if n := contactCount(t, f.app, &models.RoomBooking{}, "occurrence_id IN (SELECT id FROM event_occurrences WHERE event_id = ?)", ev.ID); n != 0 {
		t.Fatalf("%d bookings made while rooms are off", n)
	}
}
