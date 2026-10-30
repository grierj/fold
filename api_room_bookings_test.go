package main

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"fold/internal/audit"
	"fold/internal/notify"
	"fold/models"
)

type bookingFixture struct {
	app            *App
	admin, leader  *apiClient
	leaderUser     *models.User
	hall, nursery  roomJSON
	group          uint
	startOn        time.Time
	eventA, eventB eventJSON
}

// newBookingFixture: rooms on; Fellowship Hall (30 minutes setup, no
// teardown, 20 seats) and a restricted Nursery; a leader's group with two
// single-date events on the same day, A at 19:00-20:00.
func newBookingFixture(t *testing.T) bookingFixture {
	t.Helper()
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	leader := newTestUser(t, app, "leader", "password1234", models.RoleMember)
	f := bookingFixture{app: app, leaderUser: leader,
		admin: loginAPI(t, app, "admin", "password1234"), leader: loginAPI(t, app, "leader", "password1234")}
	if w := f.admin.do("PUT", "/api/v1/admin/settings/rooms", map[string]any{"enabled": true}, true); w.Code != http.StatusOK {
		t.Fatalf("rooms on: %d", w.Code)
	}
	room := func(body map[string]any) roomJSON {
		var out struct {
			Room roomJSON `json:"room"`
		}
		w := f.admin.do("POST", "/api/v1/admin/rooms", body, true)
		if w.Code != http.StatusCreated {
			t.Fatalf("room: %d %s", w.Code, w.Body.String())
		}
		decodeJSON(t, w, &out)
		return out.Room
	}
	f.hall = room(map[string]any{"name": "Fellowship Hall", "capacity": 20, "defaultSetupMinutes": 30})
	f.nursery = room(map[string]any{"name": "Nursery", "restricted": true})
	g := newTestGroup(t, app, leader, "Bookers", models.GroupActive)
	f.group = g.ID
	f.startOn = feedStart(5)
	f.eventA = createFeedEvent(t, f.leader, g.ID, "Event A", f.startOn, "")
	f.eventB = createFeedEvent(t, f.leader, g.ID, "Event B", f.startOn.Add(80*time.Minute), "") // 20:20
	return f
}

func (f bookingFixture) path(ev eventJSON, rest string) string {
	return "/api/v1/events/" + itoa(ev.ID) + "/dates/" + feedDate(f.startOn) + rest
}

func (f bookingFixture) book(t *testing.T, c *apiClient, ev eventJSON, body map[string]any) (int, string) {
	t.Helper()
	w := c.do("PUT", f.path(ev, "/room"), body, true)
	return w.Code, w.Body.String()
}

func TestRoomBookingOneDate(t *testing.T) {
	f := newBookingFixture(t)

	// The picker lists what the leader may book: no restricted room.
	var picker struct {
		Rooms   []roomPickerJSON `json:"rooms"`
		Booking *roomBookingJSON `json:"booking"`
	}
	decodeJSON(t, f.leader.do("GET", f.path(f.eventA, "/rooms"), nil, false), &picker)
	if len(picker.Rooms) != 1 || picker.Rooms[0].ID != f.hall.ID || picker.Rooms[0].Busy || picker.Booking != nil {
		t.Fatalf("leader picker: %+v", picker)
	}
	if code, body := f.book(t, f.leader, f.eventA, map[string]any{"roomId": f.nursery.ID}); code != http.StatusForbidden || !strings.Contains(body, `"code":"room_restricted"`) {
		t.Fatalf("leader booking the nursery: %d %s", code, body)
	}
	if code, _ := f.book(t, f.leader, f.eventA, map[string]any{"roomId": f.hall.ID, "setupMinutes": 0}); code != http.StatusForbidden {
		t.Fatalf("leader overriding padding: %d", code)
	}

	// A books the hall: padded [18:30, 20:00).
	if code, body := f.book(t, f.leader, f.eventA, map[string]any{"roomId": f.hall.ID}); code != http.StatusOK || !strings.Contains(body, `"status":"confirmed"`) || !strings.Contains(body, `"setupMinutes":30`) {
		t.Fatalf("book A: %d %s", code, body)
	}
	if !strings.Contains(f.leader.do("GET", "/api/v1/events/"+itoa(f.eventA.ID), nil, false).Body.String(), `"room":{"id":`+itoa(f.hall.ID)+`,"name":"Fellowship Hall"`) {
		t.Fatal("the event page should show the room as the place")
	}
	if contactCount(t, f.app, &models.AuditLog{}, "action = ?", audit.RoomBookingBooked) != 1 {
		t.Fatal("the booking should be audited")
	}

	// B at 20:20 wants [19:50, 21:20): busy, and refused without a reason.
	decodeJSON(t, f.leader.do("GET", f.path(f.eventB, "/rooms"), nil, false), &picker)
	if !picker.Rooms[0].Busy {
		t.Fatal("the hall should be busy for B")
	}
	if code, body := f.book(t, f.leader, f.eventB, map[string]any{"roomId": f.hall.ID, "overlapReason": "We'll share"}); code != http.StatusConflict || !strings.Contains(body, `"code":"room_busy"`) || strings.Contains(body, "Event A") {
		t.Fatalf("leader double-booking: %d %s", code, body)
	}
	// An admin double-books with a reason, audited.
	if code, body := f.book(t, f.admin, f.eventB, map[string]any{"roomId": f.hall.ID, "overlapReason": "Joint night"}); code != http.StatusOK || !strings.Contains(body, `"doubleBooked":true`) {
		t.Fatalf("admin double-booking: %d %s", code, body)
	}
	if contactCount(t, f.app, &models.AuditLog{}, "action = ? AND meta_json->>'reason' = ?", audit.RoomBookingOverlap, "Joint night") != 1 {
		t.Fatal("the double-booking should be audited with its reason")
	}

	// With A released, the admin's double-booking still holds the hall: a
	// leader can't book over it.
	if code, _ := f.book(t, f.leader, f.eventA, map[string]any{"roomId": nil}); code != http.StatusOK {
		t.Fatalf("release A: %d", code)
	}
	if code, body := f.book(t, f.leader, f.eventA, map[string]any{"roomId": f.hall.ID}); code != http.StatusConflict {
		t.Fatalf("booking over a double-booking: %d %s", code, body)
	}
	decodeJSON(t, f.leader.do("GET", f.path(f.eventA, "/rooms"), nil, false), &picker)
	if !picker.Rooms[0].Busy {
		t.Fatal("a double-booked hall should show busy")
	}

	// Releasing frees the room; cancelling a date frees it too.
	if code, _ := f.book(t, f.admin, f.eventB, map[string]any{"roomId": nil}); code != http.StatusOK {
		t.Fatalf("release B: %d", code)
	}
	f.book(t, f.leader, f.eventA, map[string]any{"roomId": f.hall.ID})
	if w := f.leader.do("POST", "/api/v1/events/"+itoa(f.eventA.ID)+"/cancel", map[string]any{"date": feedDate(f.startOn)}, true); w.Code != http.StatusOK {
		t.Fatalf("cancel A: %d %s", w.Code, w.Body.String())
	}
	if n := contactCount(t, f.app, &models.RoomBooking{}, "1=1"); n != 0 {
		t.Fatalf("%d bookings left after release and cancel", n)
	}

	// Rooms off: the booking routes are gone.
	f.admin.do("PUT", "/api/v1/admin/settings/rooms", map[string]any{"enabled": false}, true)
	if w := f.leader.do("GET", f.path(f.eventB, "/rooms"), nil, false); w.Code != http.StatusNotFound {
		t.Fatalf("rooms off: %d", w.Code)
	}
}

// Padded ranges are half-open: a booking that starts exactly where another's
// padding ends doesn't conflict (9.2).
func TestRoomBookingTouchingRangesFit(t *testing.T) {
	f := newBookingFixture(t)
	f.book(t, f.leader, f.eventA, map[string]any{"roomId": f.hall.ID})
	// 20:30 with 30 minutes setup starts holding the hall at 20:00, exactly
	// when A lets go of it.
	late := createFeedEvent(t, f.leader, f.group, "Late", f.startOn.Add(90*time.Minute), "")
	if code, body := f.book(t, f.leader, late, map[string]any{"roomId": f.hall.ID}); code != http.StatusOK {
		t.Fatalf("touching booking: %d %s", code, body)
	}
}

// Two people booking overlapping times at once: exactly one wins, because
// the database refuses the other (9.2).
func TestRoomBookingConcurrentExactlyOneWins(t *testing.T) {
	f := newBookingFixture(t)
	codes := make([]int, 2)
	var wg sync.WaitGroup
	for i, ev := range []eventJSON{f.eventA, f.eventB} {
		wg.Add(1)
		go func(i int, ev eventJSON) {
			defer wg.Done()
			codes[i], _ = f.book(t, f.leader, ev, map[string]any{"roomId": f.hall.ID})
		}(i, ev)
	}
	wg.Wait()
	ok, busy := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
			busy++
		}
	}
	if ok != 1 || busy != 1 {
		t.Fatalf("concurrent bookings: %v", codes)
	}
	if n := contactCount(t, f.app, &models.RoomBooking{}, "status = ?", models.BookingConfirmed); n != 1 {
		t.Fatalf("%d confirmed bookings, want 1", n)
	}
}

// Moving a date moves its booking; a move into a conflict keeps it as
// requested (holding nothing) and tells the leader once (9.2).
func TestRoomBookingMovesWithItsDate(t *testing.T) {
	f := newBookingFixture(t)
	f.book(t, f.leader, f.eventA, map[string]any{"roomId": f.hall.ID})
	other := createFeedEvent(t, f.leader, f.group, "Other", f.startOn.Add(4*time.Hour), "") // 23:00
	f.book(t, f.leader, other, map[string]any{"roomId": f.hall.ID})

	// Move A an hour later: free, so it stays confirmed at the new time.
	newStart := f.startOn.Add(time.Hour).Format("2006-01-02T15:04")
	if w := f.leader.do("PUT", "/api/v1/events/"+itoa(f.eventA.ID)+"/occurrences/"+feedDate(f.startOn), map[string]any{"startsLocal": newStart}, true); w.Code != http.StatusOK {
		t.Fatalf("move A: %d %s", w.Code, w.Body.String())
	}
	var b models.RoomBooking
	f.app.DB.Joins("JOIN event_occurrences o ON o.id = room_bookings.occurrence_id").Where("o.event_id = ?", f.eventA.ID).First(&b)
	if b.Status != models.BookingConfirmed || !b.StartsAt.Equal(f.startOn.Add(time.Hour)) {
		t.Fatalf("moved booking: %+v", b)
	}

	// An admin moves A onto Other: the hall is taken, so A's booking is
	// requested and the leader hears about it (the person who moved it
	// isn't notified of their own change).
	clash := f.startOn.Add(4 * time.Hour).Format("2006-01-02T15:04")
	if w := f.admin.do("PUT", "/api/v1/events/"+itoa(f.eventA.ID)+"/occurrences/"+feedDate(f.startOn), map[string]any{"startsLocal": clash}, true); w.Code != http.StatusOK {
		t.Fatalf("move A onto Other: %d %s", w.Code, w.Body.String())
	}
	f.app.DB.First(&b, b.ID)
	if b.Status != models.BookingRequested {
		t.Fatalf("a booking moved into a conflict should be requested: %+v", b)
	}
	if strings.Contains(f.leader.do("GET", "/api/v1/events/"+itoa(f.eventA.ID), nil, false).Body.String(), `"room":`) {
		t.Fatal("a requested booking isn't the date's place")
	}
	var n models.Notification
	if err := f.app.DB.Where("user_id = ? AND kind = ?", f.leaderUser.ID, notify.RoomLost).First(&n).Error; err != nil {
		t.Fatal("the leader should be told the room was lost:", err)
	}
	if n.Title != "Room lost on 1 date: Event A" {
		t.Fatalf("notice title %q", n.Title)
	}
}
