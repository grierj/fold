package main

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"fold/internal/events"
	"fold/internal/notify"
	"fold/models"
)

func approvalOn(t *testing.T, f bookingFixture) {
	t.Helper()
	if w := f.admin.do("PUT", "/api/v1/admin/settings/room-approval", map[string]any{"enabled": true}, true); w.Code != http.StatusOK {
		t.Fatalf("approval on: %d %s", w.Code, w.Body.String())
	}
}

func listRequests(t *testing.T, c *apiClient) []roomRequestJSON {
	t.Helper()
	var out struct {
		Requests []roomRequestJSON `json:"requests"`
	}
	w := c.do("GET", "/api/v1/admin/room-requests", nil, false)
	if w.Code != http.StatusOK {
		t.Fatalf("list requests: %d %s", w.Code, w.Body.String())
	}
	decodeJSON(t, w, &out)
	return out.Requests
}

func TestRoomApprovalOneDate(t *testing.T) {
	f := newBookingFixture(t)
	approvalOn(t, f)
	var rooms struct {
		NeedApproval bool `json:"needApproval"`
	}
	decodeJSON(t, f.admin.do("GET", "/api/v1/admin/rooms", nil, false), &rooms)
	if !rooms.NeedApproval {
		t.Fatal("the room list should say approval is on")
	}

	// A leader's booking waits, holding nothing, and every admin gets a task.
	code, body := f.book(t, f.leader, f.eventA, map[string]any{"roomId": f.hall.ID})
	if code != http.StatusOK || !strings.Contains(body, `"status":"requested"`) || !strings.Contains(body, `"pending":true`) {
		t.Fatalf("leader booking with approval on: %d %s", code, body)
	}
	if n := contactCount(t, f.app, &models.RoomBooking{}, "status = ?", models.BookingConfirmed); n != 0 {
		t.Fatalf("%d confirmed bookings before approval", n)
	}
	if n := contactCount(t, f.app, &models.Notification{}, "kind = ? AND waiting AND resolved_at IS NULL", notify.RoomRequested); n != 1 {
		t.Fatalf("%d open admin tasks", n)
	}
	reqs := listRequests(t, f.admin)
	if len(reqs) != 1 || reqs[0].Dates != 1 || reqs[0].BusyDates != 0 || reqs[0].RequestedBy != "leader" || reqs[0].EventTitle != "Event A" {
		t.Fatalf("requests: %+v", reqs)
	}
	if w := f.leader.do("GET", "/api/v1/admin/room-requests", nil, false); w.Code != http.StatusForbidden {
		t.Fatalf("leader listing requests: %d", w.Code)
	}
	if w := f.leader.do("POST", "/api/v1/admin/room-requests/"+itoa(reqs[0].ID)+"/confirm", map[string]any{}, true); w.Code != http.StatusForbidden {
		t.Fatalf("leader confirming: %d", w.Code)
	}

	// Re-picking replaces the request: the old one is withdrawn and its task closed.
	if code, _ := f.book(t, f.leader, f.eventA, map[string]any{"roomId": f.hall.ID}); code != http.StatusOK {
		t.Fatalf("re-pick: %d", code)
	}
	if n := contactCount(t, f.app, &models.RoomRequest{}, "status = ?", models.RoomRequestWithdrawn); n != 1 {
		t.Fatalf("%d withdrawn requests", n)
	}
	if n := contactCount(t, f.app, &models.Notification{}, "kind = ? AND waiting AND resolved_at IS NULL", notify.RoomRequested); n != 1 {
		t.Fatalf("%d open admin tasks after re-pick", n)
	}

	reqs = listRequests(t, f.admin)
	if len(reqs) != 1 {
		t.Fatalf("requests after re-pick: %+v", reqs)
	}
	if w := f.admin.do("POST", "/api/v1/admin/room-requests/"+itoa(reqs[0].ID)+"/confirm", map[string]any{}, true); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"confirmed":1`) {
		t.Fatalf("confirm: %d %s", w.Code, w.Body.String())
	}
	if n := contactCount(t, f.app, &models.RoomBooking{}, "status = ?", models.BookingConfirmed); n != 1 {
		t.Fatalf("%d confirmed after approval", n)
	}
	if contactCount(t, f.app, &models.Notification{}, "user_id = ? AND kind = ?", f.leaderUser.ID, notify.RoomRequestDecided) != 1 {
		t.Fatal("the leader should hear the room was approved")
	}
	if n := contactCount(t, f.app, &models.Notification{}, "kind = ? AND waiting AND resolved_at IS NULL", notify.RoomRequested); n != 0 {
		t.Fatalf("%d open admin tasks after confirming", n)
	}
	if w := f.admin.do("POST", "/api/v1/admin/room-requests/"+itoa(reqs[0].ID)+"/confirm", map[string]any{}, true); w.Code != http.StatusConflict {
		t.Fatalf("confirming twice: %d", w.Code)
	}

	// An admin's own booking confirms at once, approval or not.
	if code, body := f.book(t, f.admin, f.eventB, map[string]any{"roomId": f.nursery.ID}); code != http.StatusOK || !strings.Contains(body, `"status":"confirmed"`) {
		t.Fatalf("admin booking with approval on: %d %s", code, body)
	}
}

func TestRoomApprovalSeries(t *testing.T) {
	f := newBookingFixture(t)
	approvalOn(t, f)
	start := f.startOn.AddDate(0, 0, 1)
	series := createFeedEvent(t, f.leader, f.group, "Weekly", start, "FREQ=WEEKLY;COUNT=4")
	code, out := f.seriesRoom(t, f.leader, series, map[string]any{"roomId": f.hall.ID})
	if code != http.StatusOK || out.Pending != 4 || out.Booked != 0 {
		t.Fatalf("series with approval on: %d %+v", code, out)
	}
	// One request for the whole series (DEC-106), and the waiting dates are
	// not the leader's to re-pick (DEC-105).
	reqs := listRequests(t, f.admin)
	if len(reqs) != 1 || reqs[0].Dates != 4 || !reqs[0].Series {
		t.Fatalf("requests: %+v", reqs)
	}
	var picker struct {
		NeedsRoom []needsRoomJSON `json:"needsRoom"`
		Pending   int             `json:"pending"`
	}
	decodeJSON(t, f.leader.do("GET", "/api/v1/events/"+itoa(series.ID)+"/rooms", nil, false), &picker)
	if len(picker.NeedsRoom) != 0 || picker.Pending != 4 {
		t.Fatalf("leader view while waiting: %+v", picker)
	}

	// The extender adds a date to the waiting request.
	last := feedDate(start.AddDate(0, 0, 21))
	if err := f.app.DB.Where("event_id = ? AND original_date = ?", series.ID, last).Delete(&models.EventOccurrence{}).Error; err != nil {
		t.Fatal(err)
	}
	var emailed []uint
	if err := events.Extend(f.app.DB, time.Now().UTC(), f.app.afterExtend(&emailed)); err != nil {
		t.Fatal(err)
	}
	if reqs = listRequests(t, f.admin); len(reqs) != 1 || reqs[0].Dates != 4 {
		t.Fatalf("the extended date should join the request: %+v", reqs)
	}

	// An admin takes the hall on the second date; confirming without a
	// reason confirms 3 and hands the busy date back to the leader.
	second := start.AddDate(0, 0, 7)
	other := createFeedEvent(t, f.admin, f.group, "Admin night", second, "")
	if w := f.admin.do("PUT", "/api/v1/events/"+itoa(other.ID)+"/dates/"+feedDate(second)+"/room", map[string]any{"roomId": f.hall.ID}, true); w.Code != http.StatusOK {
		t.Fatalf("admin booking: %d %s", w.Code, w.Body.String())
	}
	if reqs = listRequests(t, f.admin); reqs[0].BusyDates != 1 {
		t.Fatalf("busy dates in the request: %+v", reqs)
	}
	w := f.admin.do("POST", "/api/v1/admin/room-requests/"+itoa(reqs[0].ID)+"/confirm", map[string]any{}, true)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"confirmed":3`) || !strings.Contains(w.Body.String(), feedDate(second)) {
		t.Fatalf("confirm the series: %d %s", w.Code, w.Body.String())
	}
	decodeJSON(t, f.leader.do("GET", "/api/v1/events/"+itoa(series.ID)+"/rooms", nil, false), &picker)
	if len(picker.NeedsRoom) != 1 || picker.NeedsRoom[0].Date != feedDate(second) || picker.NeedsRoom[0].Status != models.BookingRequested || picker.Pending != 0 {
		t.Fatalf("leader view after confirming: %+v", picker)
	}
}

func TestRoomApprovalReject(t *testing.T) {
	f := newBookingFixture(t)
	approvalOn(t, f)
	series := createFeedEvent(t, f.leader, f.group, "Weekly", f.startOn.AddDate(0, 0, 1), "FREQ=WEEKLY;COUNT=2")
	if code, out := f.seriesRoom(t, f.leader, series, map[string]any{"roomId": f.hall.ID}); code != http.StatusOK || out.Pending != 2 {
		t.Fatalf("series: %d %+v", code, out)
	}
	reqs := listRequests(t, f.admin)
	if w := f.admin.do("POST", "/api/v1/admin/room-requests/"+itoa(reqs[0].ID)+"/reject", map[string]any{"reason": "The hall is being painted."}, true); w.Code != http.StatusOK {
		t.Fatalf("reject: %d %s", w.Code, w.Body.String())
	}
	if n := contactCount(t, f.app, &models.RoomBooking{}, "status = ?", models.BookingRejected); n != 2 {
		t.Fatalf("%d rejected bookings", n)
	}
	var ev models.Event
	f.app.DB.First(&ev, series.ID)
	if ev.DefaultRoomID != nil {
		t.Fatal("a rejected series request stops being the series room")
	}
	var n models.Notification
	if err := f.app.DB.Where("user_id = ? AND kind = ?", f.leaderUser.ID, notify.RoomRequestDecided).First(&n).Error; err != nil || !strings.Contains(n.Body, "painted") {
		t.Fatalf("the leader should hear why: %v %+v", err, n)
	}
	var picker struct {
		NeedsRoom []needsRoomJSON `json:"needsRoom"`
	}
	decodeJSON(t, f.leader.do("GET", "/api/v1/events/"+itoa(series.ID)+"/rooms", nil, false), &picker)
	if len(picker.NeedsRoom) != 2 || picker.NeedsRoom[0].Status != models.BookingRejected {
		t.Fatalf("rejected dates are the leader's to re-pick: %+v", picker)
	}
	if len(listRequests(t, f.admin)) != 0 {
		t.Fatal("a rejected request leaves the list")
	}
}

// A series room no admin approved keeps sending new dates to an admin, even
// after its request was withdrawn (the leader released every date).
func TestRoomApprovalSeriesRoomStaysUnapproved(t *testing.T) {
	f := newBookingFixture(t)
	approvalOn(t, f)
	start := f.startOn.AddDate(0, 0, 1)
	series := createFeedEvent(t, f.leader, f.group, "Weekly", start, "FREQ=WEEKLY;COUNT=3")
	if code, out := f.seriesRoom(t, f.leader, series, map[string]any{"roomId": f.hall.ID}); code != http.StatusOK || out.Pending != 3 {
		t.Fatalf("series: %d %+v", code, out)
	}
	for i := 0; i < 3; i++ {
		d := feedDate(start.AddDate(0, 0, 7*i))
		if w := f.leader.do("PUT", "/api/v1/events/"+itoa(series.ID)+"/dates/"+d+"/room", map[string]any{"roomId": nil}, true); w.Code != http.StatusOK {
			t.Fatalf("release %s: %d", d, w.Code)
		}
	}
	if len(listRequests(t, f.admin)) != 0 || contactCount(t, f.app, &models.RoomRequest{}, "status = ?", models.RoomRequestWithdrawn) != 1 {
		t.Fatal("releasing every date withdraws the request")
	}
	// The extender adds the last date again: it waits on a new request.
	last := feedDate(start.AddDate(0, 0, 14))
	if err := f.app.DB.Where("event_id = ? AND original_date = ?", series.ID, last).Delete(&models.EventOccurrence{}).Error; err != nil {
		t.Fatal(err)
	}
	var emailed []uint
	if err := events.Extend(f.app.DB, time.Now().UTC(), f.app.afterExtend(&emailed)); err != nil {
		t.Fatal(err)
	}
	if n := seriesBookings(t, f.app, series.ID, models.BookingConfirmed); n != 0 {
		t.Fatalf("%d dates confirmed in a room no admin approved", n)
	}
	reqs := listRequests(t, f.admin)
	if len(reqs) != 1 || reqs[0].Dates != 1 || reqs[0].RequestedBy != "leader" {
		t.Fatalf("the new date should wait on a new request: %+v", reqs)
	}
	// Once an admin approves it, new dates confirm directly.
	if w := f.admin.do("POST", "/api/v1/admin/room-requests/"+itoa(reqs[0].ID)+"/confirm", nil, true); w.Code != http.StatusOK {
		t.Fatalf("confirm with no body: %d %s", w.Code, w.Body.String())
	}
	if err := f.app.DB.Where("event_id = ? AND original_date = ?", series.ID, last).Delete(&models.EventOccurrence{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := events.Extend(f.app.DB, time.Now().UTC(), f.app.afterExtend(&emailed)); err != nil {
		t.Fatal(err)
	}
	if n := seriesBookings(t, f.app, series.ID, models.BookingConfirmed); n != 1 {
		t.Fatalf("%d confirmed after approval; want the regenerated date", n)
	}
}

func TestRoomApprovalCreateDeleteAndGuards(t *testing.T) {
	f := newBookingFixture(t)
	approvalOn(t, f)
	start := f.startOn.AddDate(0, 0, 2)
	w := f.leader.do("POST", "/api/v1/groups/"+itoa(f.group)+"/events", map[string]any{"title": "Asked", "startsLocal": start.Format("2006-01-02T15:04"),
		"durationMinutes": 60, "tzid": "UTC", "recurrenceRule": "FREQ=WEEKLY;COUNT=2", "roomId": f.hall.ID}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create with a room under approval: %d %s", w.Code, w.Body.String())
	}
	reqs := listRequests(t, f.admin)
	if len(reqs) != 1 || reqs[0].Dates != 2 || reqs[0].FirstStartsAt.IsZero() {
		t.Fatalf("a create under approval opens one request: %+v", reqs)
	}
	for _, path := range []string{"/confirm", "/reject"} {
		if w := f.leader.do("POST", "/api/v1/admin/room-requests/"+itoa(reqs[0].ID)+path, nil, true); w.Code != http.StatusForbidden {
			t.Fatalf("leader %s: %d", path, w.Code)
		}
	}
	if w := f.leader.do("PUT", "/api/v1/admin/settings/room-approval", map[string]any{"enabled": false}, true); w.Code != http.StatusForbidden {
		t.Fatalf("leader turning approval off: %d", w.Code)
	}

	// Deleting the event withdraws its request and closes the admins' task.
	if w := f.leader.do("DELETE", "/api/v1/events/"+itoa(reqs[0].EventID), nil, true); w.Code != http.StatusOK && w.Code != http.StatusNoContent {
		t.Fatalf("delete the event: %d %s", w.Code, w.Body.String())
	}
	if n := contactCount(t, f.app, &models.RoomRequest{}, "status = ?", models.RoomRequestPending); n != 0 {
		t.Fatalf("%d requests still pending after the event was deleted", n)
	}
	if n := contactCount(t, f.app, &models.Notification{}, "kind = ? AND waiting AND resolved_at IS NULL", notify.RoomRequested); n != 0 {
		t.Fatalf("%d admin tasks left open", n)
	}
}

func TestRoomApprovalConfirmWithReason(t *testing.T) {
	f := newBookingFixture(t)
	approvalOn(t, f)
	if code, _ := f.book(t, f.leader, f.eventA, map[string]any{"roomId": f.hall.ID}); code != http.StatusOK {
		t.Fatalf("leader request: %d", code)
	}
	// The admin then books B over A's time (A's request holds nothing).
	if code, body := f.book(t, f.admin, f.eventB, map[string]any{"roomId": f.hall.ID}); code != http.StatusOK || !strings.Contains(body, `"status":"confirmed"`) {
		t.Fatalf("admin booking B: %d %s", code, body)
	}
	reqs := listRequests(t, f.admin)
	if len(reqs) != 1 || reqs[0].BusyDates != 1 {
		t.Fatalf("A's request should show its date busy: %+v", reqs)
	}
	w := f.admin.do("POST", "/api/v1/admin/room-requests/"+itoa(reqs[0].ID)+"/confirm", map[string]any{"overlapReason": "Both fit"}, true)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"overlapped":1`) {
		t.Fatalf("confirm with a reason: %d %s", w.Code, w.Body.String())
	}
	if n := contactCount(t, f.app, &models.RoomBooking{}, "status = ? AND allow_overlap AND overlap_reason = ?", models.BookingConfirmed, "Both fit"); n != 1 {
		t.Fatalf("%d double-bookings with the reason", n)
	}
}
