package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"fold/internal/eventdates"
	"fold/internal/notify"
	"fold/models"

	"gorm.io/gorm"
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
	// firstDate is the earliest waiting date as plain YYYY-MM-DD, taken here
	// from the start the test chose, not from the code that renders it.
	if reqs[0].FirstDate != feedDate(start) {
		t.Fatalf("request firstDate = %q, want %q", reqs[0].FirstDate, feedDate(start))
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
	if err := eventdates.Extend(f.app.DB, time.Now().UTC(), f.app.afterExtend()); err != nil {
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
	// The dates that lost their room are strings, exactly the busy date.
	var confirmed struct {
		Lost []string `json:"lost"`
	}
	decodeJSON(t, w, &confirmed)
	if len(confirmed.Lost) != 1 || confirmed.Lost[0] != feedDate(second) {
		t.Fatalf("lost dates = %v, want [%s]", confirmed.Lost, feedDate(second))
	}
	if n := contactCount(t, f.app, &models.AuditLog{}, "meta_json->'lost' = ?::jsonb", `["`+feedDate(second)+`"]`); n != 1 {
		t.Fatalf("%d audit rows record the lost date %s as a string", n, feedDate(second))
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
	if err := eventdates.Extend(f.app.DB, time.Now().UTC(), f.app.afterExtend()); err != nil {
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
	if err := eventdates.Extend(f.app.DB, time.Now().UTC(), f.app.afterExtend()); err != nil {
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

// roomRequestsFixture returns a booking fixture with approval on and a second
// admin to erase. The leader's booking of the hall waits for approval.
func roomRequestsFixture(t *testing.T) (bookingFixture, *models.User) {
	t.Helper()
	f := newBookingFixture(t)
	approvalOn(t, f)
	victim := newTestUser(t, f.app, "admin2", "password1234", models.RoleAdmin)
	f.leader.holdNotices = true
	f.admin.holdNotices = true
	return f, victim
}

func adminOpenTasks(t *testing.T, f bookingFixture) int64 {
	t.Helper()
	return contactCount(t, f.app, &models.Notification{},
		"kind = ? AND user_id IN (SELECT id FROM users WHERE email = ?)", notify.RoomRequested, testEmail("admin"))
}

// N-22 (inventory 9.2.2, plain entries apiSetDateRoom and apiSetSeriesRoom): a
// leader's room booking that needs approval gives every admin a task, while the
// request holds the event row FOR UPDATE and no gate. An admin erased meanwhile
// is left out in either order, and the other admin still gets the task.
func TestRoomRequestNoticeSerializesWithAdminErasure(t *testing.T) {
	t.Run("one date", func(t *testing.T) {
		runErasureBarriers(t, 7402009, func(t *testing.T) erasureScene {
			f, victim := roomRequestsFixture(t)
			return erasureScene{app: f.app, victim: victim,
				run: func() error {
					return doExpect(f.leader, "PUT", f.path(f.eventA, "/room"), map[string]any{"roomId": f.hall.ID}, http.StatusOK)
				},
				told: func(t *testing.T) {
					if n := adminOpenTasks(t, f); n != 1 {
						t.Fatalf("the other admin has %d room tasks, want 1", n)
					}
				},
			}
		})
	})
	t.Run("series", func(t *testing.T) {
		runErasureBarriers(t, 7402010, func(t *testing.T) erasureScene {
			f, victim := roomRequestsFixture(t)
			series := createFeedEvent(t, f.leader, f.group, "Weekly", f.startOn.AddDate(0, 0, 1), "FREQ=WEEKLY;COUNT=4")
			return erasureScene{app: f.app, victim: victim,
				run: func() error {
					return doExpect(f.leader, "PUT", "/api/v1/events/"+itoa(series.ID)+"/room", map[string]any{"roomId": f.hall.ID}, http.StatusOK)
				},
				told: func(t *testing.T) {
					if n := adminOpenTasks(t, f); n != 1 {
						t.Fatalf("the other admin has %d room tasks, want 1", n)
					}
				},
			}
		})
	})
}

// N-23 (inventory 9.2.2, plain entries apiConfirmRoomRequest and
// apiRejectRoomRequest): an admin's decision tells the event's leaders and then
// closes the admins' tasks, holding the event row and the request row. A
// second leader erased meanwhile is left out in either order, and the first
// leader is still told. The erased leader here is not an admin, so they own none
// of the tasks the decision closes; the admin-and-leader case is
// TestRoomDecisionNoticeSerializesWithAdminLeaderErasure.
func TestRoomDecisionNoticeSerializesWithLeaderErasure(t *testing.T) {
	for i, decision := range []string{"confirm", "reject"} {
		t.Run(decision, func(t *testing.T) {
			runErasureBarriers(t, 7402011+int64(i), func(t *testing.T) erasureScene {
				f, _ := roomRequestsFixture(t)
				victim := newTestUser(t, f.app, "leader2", "password1234", models.RoleMember)
				addMember(t, f.app, victim, &models.Group{ID: f.group}, models.MembershipLeader)
				if code, body := f.book(t, f.leader, f.eventA, map[string]any{"roomId": f.hall.ID}); code != http.StatusOK {
					t.Fatalf("request the room: %d %s", code, body)
				}
				reqs := listRequests(t, f.admin)
				if len(reqs) != 1 {
					t.Fatalf("requests: %+v", reqs)
				}
				return erasureScene{app: f.app, victim: victim,
					run: func() error {
						return doExpect(f.admin, "POST", "/api/v1/admin/room-requests/"+itoa(reqs[0].ID)+"/"+decision, map[string]any{}, http.StatusOK)
					},
					told: func(t *testing.T) {
						if n := contactCount(t, f.app, &models.Notification{}, "user_id = ? AND kind = ?", f.leaderUser.ID, notify.RoomRequestDecided); n != 1 {
							t.Fatalf("the first leader has %d decision notices, want 1", n)
						}
					},
				}
			})
		})
	}
}

// pauseWhenTaskResolvedFor makes any transaction that closes uid's open room
// request task wait on a transaction-level advisory lock right after that
// update, still holding the task row and everything it took before. It drops
// itself at cleanup.
func pauseWhenTaskResolvedFor(t *testing.T, db *gorm.DB, uid uint, key int64) {
	t.Helper()
	name := fmt.Sprintf("fold_test_pause_task_%d", key)
	if err := db.Exec(fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN PERFORM pg_advisory_xact_lock(%d); RETURN NULL; END $$`, name, key)).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(fmt.Sprintf(`CREATE TRIGGER %s AFTER UPDATE ON notifications FOR EACH ROW
		WHEN (NEW.user_id = %d AND NEW.kind = '%s' AND OLD.resolved_at IS NULL AND NEW.resolved_at IS NOT NULL)
		EXECUTE FUNCTION %s()`, name, uid, notify.RoomRequested, name)).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec("DROP TRIGGER IF EXISTS " + name + " ON notifications")
		db.Exec("DROP FUNCTION IF EXISTS " + name + "()")
	})
}

// N-23, the case the plain test above leaves out: the erased person is both an
// admin, who holds an open "room requested" task, and a leader of the event's
// group, who is told of the decision. A decision that closed the task first
// held the task row while it waited for the leader's user row, and an erasure
// (user row FOR UPDATE, then the notices, task included) held the user row
// while it waited for the task row: a lock cycle PostgreSQL breaks by aborting
// one of them. The decision now tells the leaders first (settleRoomDecision), so
// the user row is the first thing both sides want.
//
// The real decision and the real DELETE /api/v1/admin/users/:id, confirm and
// reject, in both orders, with no sleeps:
//   - sender first: the decision is stopped right after it closed the victim's
//     task (holding the task row and the victim's user row); the delete must
//     queue on the user row, not on the task row, and both finish.
//   - deletion first: the delete is stopped holding the gate and the victim's
//     user row, before it removes their notices; the decision must queue on the
//     recipient lock, and both finish once released.
//
// By source trace, with the old order (resolve, then send) the barriers put
// both sides in the cycle in both orders, so one side would fail with SQLSTATE
// 40P01 after the deadlock timeout; that was not executed against the old code.
// Covers N-23 for a group event (leaders); a church-wide event's
// hosts take the same path through notifyRoomDecided and are not run.
func TestRoomDecisionNoticeSerializesWithAdminLeaderErasure(t *testing.T) {
	for i, decision := range []string{"confirm", "reject"} {
		keyA, keyB := int64(7402041+2*i), int64(7402042+2*i)
		wantStatus := map[string]string{"confirm": models.RoomRequestConfirmed, "reject": models.RoomRequestRejected}[decision]
		t.Run(decision, func(t *testing.T) {
			type scene struct {
				f      bookingFixture
				victim *models.User
				reqID  uint
				decide func() error
				erase  func() error
			}
			setup := func(t *testing.T) scene {
				f, victim := roomRequestsFixture(t) // the victim is an admin
				addMember(t, f.app, victim, &models.Group{ID: f.group}, models.MembershipLeader)
				// Log in before creating the task: loginAPI drains queued notices.
				deleter := loginAPI(t, f.app, "admin", "password1234")
				deleter.holdNotices = true
				if code, body := f.book(t, f.leader, f.eventA, map[string]any{"roomId": f.hall.ID}); code != http.StatusOK {
					t.Fatalf("request the room: %d %s", code, body)
				}
				reqs := listRequests(t, f.admin)
				if len(reqs) != 1 {
					t.Fatalf("requests: %+v", reqs)
				}
				if n := contactCount(t, f.app, &models.Notification{}, "user_id = ? AND kind = ? AND waiting AND resolved_at IS NULL", victim.ID, notify.RoomRequested); n != 1 {
					t.Fatalf("the victim has %d open room tasks, want 1", n)
				}
				if n := mailCount(t, f.app, victim.Email); n != 0 {
					t.Fatalf("fixture sent %d emails to the victim before the race", n)
				}
				return scene{f: f, victim: victim, reqID: reqs[0].ID,
					decide: func() error {
						return doExpect(f.admin, "POST", "/api/v1/admin/room-requests/"+itoa(reqs[0].ID)+"/"+decision, map[string]any{}, http.StatusOK)
					},
					erase: func() error {
						return doExpect(deleter, "DELETE", "/api/v1/admin/users/"+itoa(victim.ID), nil, http.StatusOK)
					},
				}
			}
			settle := func(t *testing.T, s scene) {
				settleErasure(t, erasureScene{app: s.f.app, victim: s.victim, told: func(t *testing.T) {
					if n := contactCount(t, s.f.app, &models.Notification{}, "user_id = ? AND kind = ?", s.f.leaderUser.ID, notify.RoomRequestDecided); n != 1 {
						t.Fatalf("the first leader has %d decision notices, want 1", n)
					}
					taskOwner := "user_id IN (SELECT id FROM users WHERE email = ?) AND kind = ? AND subject_id = ?"
					if n := contactCount(t, s.f.app, &models.Notification{}, taskOwner+" AND waiting AND resolved_at IS NULL", testEmail("admin"), notify.RoomRequested, s.reqID); n != 0 {
						t.Fatalf("the deciding admin still has %d open room tasks", n)
					}
					resolution := map[string]string{"confirm": "confirmed", "reject": "rejected"}[decision]
					if n := contactCount(t, s.f.app, &models.Notification{}, taskOwner+" AND resolved_at IS NOT NULL AND resolution = ?", testEmail("admin"), notify.RoomRequested, s.reqID, resolution); n != 1 {
						t.Fatalf("the deciding admin has %d tasks resolved as %s, want 1", n, resolution)
					}
					if n := contactCount(t, s.f.app, &models.RoomRequest{}, "id = ? AND status = ?", s.reqID, wantStatus); n != 1 {
						t.Fatalf("the request is not %s", wantStatus)
					}
				}})
			}
			t.Run("sender first", func(t *testing.T) {
				s := setup(t)
				db := s.f.app.DB
				pauseWhenTaskResolvedFor(t, db, s.victim.ID, keyA)
				release, hPID := holdAdvisory(t, db, keyA)
				done := startOperation(s.decide)
				opPID := waiterOn(t, db, hPID) // it told the leaders and closed the victim's task; its transaction is open
				erased := startOperation(s.erase)
				delPID := waiterOn(t, db, opPID)
				if q := statementOf(t, db, delPID); !strings.Contains(q, "users") || strings.Contains(q, "notifications") {
					t.Fatalf("the delete waits in %q, want the victim's user row, not their notices", q)
				}
				release()
				if err := awaitDBResult(t, done); err != nil {
					t.Fatalf("the decision: %v", err)
				}
				if err := awaitDBResult(t, erased); err != nil {
					t.Fatalf("the delete: %v", err)
				}
				settle(t, s)
			})
			t.Run("deletion first", func(t *testing.T) {
				s := setup(t)
				db := s.f.app.DB
				pauseOnStatement(t, db, "BEFORE DELETE", "notifications", keyB)
				release, hPID := holdAdvisory(t, db, keyB)
				erased := startOperation(s.erase)
				deleter := waiterOn(t, db, hPID) // it holds the gate and the victim's user row
				done := startOperation(s.decide)
				opPID := waiterOn(t, db, deleter)
				q := statementOf(t, db, opPID)
				for _, part := range recipientLockText {
					if !strings.Contains(q, part) {
						t.Fatalf("the decision waits in %q, want the recipient lock (%q)", q, part)
					}
				}
				release()
				if err := awaitDBResult(t, erased); err != nil {
					t.Fatalf("the delete: %v", err)
				}
				if err := awaitDBResult(t, done); err != nil {
					t.Fatalf("a decision that lost the race must finish: %v", err)
				}
				settle(t, s)
			})
		})
	}
}
