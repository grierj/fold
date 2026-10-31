package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"fold/models"
)

type roomCalendarFixture struct {
	bookingFixture
	member     *apiClient
	memberUser *models.User
	secret     eventJSON // an invite-only group's event, booked in the hall
}

// newRoomCalendarFixture: the booking fixture with church time in UTC, a
// member outside every group, Event A booked in the hall at 19:00 and an
// invite-only group's event booked in the hall at 21:00.
func newRoomCalendarFixture(t *testing.T) roomCalendarFixture {
	t.Helper()
	f := roomCalendarFixture{bookingFixture: newBookingFixture(t)}
	if err := f.app.DB.Where("key = ?", settingTimezone).Assign(models.Setting{Key: settingTimezone, Value: "UTC"}).
		FirstOrCreate(&models.Setting{}).Error; err != nil {
		t.Fatal(err)
	}
	f.memberUser = newTestUser(t, f.app, "member", "password1234", models.RoleMember)
	f.member = loginAPI(t, f.app, "member", "password1234")
	if code, body := f.book(t, f.leader, f.eventA, map[string]any{"roomId": f.hall.ID}); code != http.StatusOK {
		t.Fatalf("book A: %d %s", code, body)
	}
	hidden := newTestGroup(t, f.app, f.leaderUser, "Quiet circle", models.GroupActive)
	f.app.DB.Model(hidden).Update("visibility", models.VisibilityInviteOnly)
	f.secret = createFeedEvent(t, f.leader, hidden.ID, "Quiet circle night", f.startOn.Add(2*time.Hour), "")
	w := f.leader.do("PUT", "/api/v1/events/"+itoa(f.secret.ID)+"/dates/"+feedDate(f.startOn)+"/room", map[string]any{"roomId": f.hall.ID}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("book the secret event: %d %s", w.Code, w.Body.String())
	}
	return f
}

func (f roomCalendarFixture) calendar(t *testing.T, c *apiClient, query string) (roomCalendarJSON, string) {
	t.Helper()
	w := c.do("GET", "/api/v1/calendar/rooms?"+query, nil, false)
	if w.Code != http.StatusOK {
		t.Fatalf("room calendar %s: %d %s", query, w.Code, w.Body.String())
	}
	var out roomCalendarJSON
	decodeJSON(t, w, &out)
	return out, w.Body.String()
}

func roomByName(cal roomCalendarJSON, name string) *roomCalendarRoomJSON {
	for i := range cal.Rooms {
		if cal.Rooms[i].Name == name {
			return &cal.Rooms[i]
		}
	}
	return nil
}

func TestRoomCalendarWhoMaySee(t *testing.T) {
	f := newRoomCalendarFixture(t)
	if w := newAPIClient(t, f.app).do("GET", "/api/v1/calendar/rooms", nil, false); w.Code != http.StatusUnauthorized {
		t.Fatalf("signed out: %d", w.Code)
	}
	birth := time.Now().AddDate(-15, 0, 0)
	teen := newTestUser(t, f.app, "teen", "password1234", models.RoleMember)
	f.app.DB.Model(teen).Updates(map[string]any{"account_type": models.AccountTeen, "birth_date": birth})
	if w := loginAPI(t, f.app, "teen", "password1234").do("GET", "/api/v1/calendar/rooms", nil, false); w.Code != http.StatusNotFound {
		t.Fatalf("teen: %d", w.Code)
	}
	if w := f.member.do("GET", "/api/v1/calendar/rooms?view=month", nil, false); w.Code != http.StatusBadRequest {
		t.Fatalf("a month view: %d", w.Code)
	}
	if w := f.member.do("GET", "/api/v1/calendar/rooms?date=tomorrow", nil, false); w.Code != http.StatusBadRequest {
		t.Fatalf("a bad date: %d", w.Code)
	}
	if w := f.admin.do("PUT", "/api/v1/admin/settings/rooms", map[string]any{"enabled": false}, true); w.Code != http.StatusOK {
		t.Fatalf("rooms off: %d", w.Code)
	}
	for name, c := range map[string]*apiClient{"admin": f.admin, "member": f.member} {
		if w := c.do("GET", "/api/v1/calendar/rooms", nil, false); w.Code != http.StatusNotFound {
			t.Fatalf("%s with rooms off: %d", name, w.Code)
		}
	}
}

func TestRoomCalendarShowsWhatEachViewerMaySee(t *testing.T) {
	f := newRoomCalendarFixture(t)
	day := feedDate(f.startOn)

	// A member sees Event A by name and the invite-only booking as Reserved,
	// and never the restricted nursery.
	cal, body := f.calendar(t, f.member, "date="+day)
	if cal.From != day || cal.To != day || cal.Timezone != "UTC" {
		t.Fatalf("window: %+v", cal)
	}
	if roomByName(cal, "Nursery") != nil || strings.Contains(body, "Nursery") {
		t.Fatal("a member must not see the restricted nursery listed")
	}
	hall := roomByName(cal, "Fellowship Hall")
	if hall == nil || len(hall.Bookings) != 2 {
		t.Fatalf("hall: %+v", hall)
	}
	a, secret := hall.Bookings[0], hall.Bookings[1]
	if a.Reserved || a.Title != "Event A" || a.EventID != f.eventA.ID || a.Date != day || a.GroupName != "Bookers" {
		t.Fatalf("Event A: %+v", a)
	}
	if !secret.Reserved || secret.Title != "" || secret.EventID != 0 {
		t.Fatalf("the invite-only booking should be Reserved: %+v", secret)
	}
	if strings.Contains(body, "Quiet circle") {
		t.Fatal("the invite-only event or group leaked into the response")
	}

	// The leader of the invite-only group sees it; an admin sees the nursery.
	cal, _ = f.calendar(t, f.leader, "date="+day)
	if b := roomByName(cal, "Fellowship Hall").Bookings[1]; b.Reserved || b.Title != "Quiet circle night" || b.GroupName != "Quiet circle" {
		t.Fatalf("leader's view of their own invite-only event: %+v", b)
	}
	cal, _ = f.calendar(t, f.admin, "date="+day)
	if n := roomByName(cal, "Nursery"); n == nil || !n.Restricted {
		t.Fatalf("admin should see the restricted nursery: %+v", cal.Rooms)
	}

	// A different day has nothing booked.
	cal, _ = f.calendar(t, f.member, "date="+feedDate(f.startOn.AddDate(0, 0, 1)))
	if len(roomByName(cal, "Fellowship Hall").Bookings) != 0 {
		t.Fatal("the next day should be free")
	}
}

// A reserved booking carries its times and nothing that says whose it is.
func TestRoomCalendarReservedKeys(t *testing.T) {
	f := newRoomCalendarFixture(t)
	w := f.member.do("GET", "/api/v1/calendar/rooms?date="+feedDate(f.startOn), nil, false)
	var raw struct {
		Rooms []struct {
			Name     string           `json:"name"`
			Bookings []map[string]any `json:"bookings"`
		} `json:"rooms"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range raw.Rooms {
		for _, b := range r.Bookings {
			if b["reserved"] != true {
				continue
			}
			found = true
			keys := []string{}
			for k := range b {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			if strings.Join(keys, ",") != "endsAt,reserved,startsAt" {
				t.Fatalf("a reserved booking carries %v", keys)
			}
		}
	}
	if !found {
		t.Fatal("expected a reserved booking")
	}
}

func TestRoomCalendarWeekRunsSundayToSaturday(t *testing.T) {
	f := newRoomCalendarFixture(t)
	day := f.startOn
	cal, _ := f.calendar(t, f.member, "view=week&date="+feedDate(day))
	sunday := day.AddDate(0, 0, -int(day.Weekday()))
	if cal.From != feedDate(sunday) || cal.To != feedDate(sunday.AddDate(0, 0, 6)) {
		t.Fatalf("week %s..%s for %s", cal.From, cal.To, feedDate(day))
	}
	if len(roomByName(cal, "Fellowship Hall").Bookings) != 2 {
		t.Fatal("the week should hold both hall bookings")
	}
}

// Bookings waiting for approval hold nothing: admins see them marked, and
// nobody else sees them at all (DEC-109). A booking that lost its room
// isn't shown either.
func TestRoomCalendarWaitingBookingsAreForAdmins(t *testing.T) {
	f := newRoomCalendarFixture(t)
	if w := f.admin.do("PUT", "/api/v1/admin/settings/room-approval", map[string]any{"enabled": true}, true); w.Code != http.StatusOK {
		t.Fatalf("approval on: %d %s", w.Code, w.Body.String())
	}
	if w := f.admin.do("POST", "/api/v1/admin/rooms", map[string]any{"name": "Chapel"}, true); w.Code != http.StatusCreated {
		t.Fatalf("chapel: %d", w.Code)
	}
	var chapel models.Room
	f.app.DB.Where("name = ?", "Chapel").First(&chapel)
	if code, body := f.book(t, f.leader, f.eventB, map[string]any{"roomId": chapel.ID}); code != http.StatusOK {
		t.Fatalf("book B: %d %s", code, body)
	}
	var b models.RoomBooking
	f.app.DB.Joins("JOIN event_occurrences o ON o.id = room_bookings.occurrence_id").Where("o.event_id = ?", f.eventB.ID).First(&b)
	if b.Status != models.BookingRequested || b.RequestID == nil {
		t.Fatalf("B should wait for approval: %+v", b)
	}
	day := "date=" + feedDate(f.startOn)
	cal, _ := f.calendar(t, f.admin, day)
	waiting := 0
	for _, s := range roomByName(cal, "Chapel").Bookings {
		if s.Waiting {
			waiting++
			if s.Title != "Event B" {
				t.Fatalf("waiting booking: %+v", s)
			}
		}
	}
	if waiting != 1 {
		t.Fatalf("admin should see one waiting booking, saw %d", waiting)
	}
	for name, c := range map[string]*apiClient{"leader": f.leader, "member": f.member} {
		cal, body := f.calendar(t, c, day)
		if len(roomByName(cal, "Chapel").Bookings) != 0 || strings.Contains(body, "Event B") || strings.Contains(body, `"waiting"`) {
			t.Fatalf("%s should not see the waiting booking: %s", name, body)
		}
	}
	// A requested booking with no pending request lost its room: not shown.
	f.app.DB.Model(&models.RoomBooking{}).Where("id = ?", b.ID).Update("request_id", nil)
	if cal, _ := f.calendar(t, f.admin, day); len(roomByName(cal, "Chapel").Bookings) != 0 {
		t.Fatal("a booking that lost its room holds nothing and isn't shown")
	}
}

// An exact-date invitation shows that date's title and its own link, never
// the series or its group.
func TestRoomCalendarExactDateInvitation(t *testing.T) {
	f := newRoomCalendarFixture(t)
	var occ models.EventOccurrence
	if err := f.app.DB.Where("event_id = ?", f.secret.ID).First(&occ).Error; err != nil {
		t.Fatal(err)
	}
	seedReadInvitation(t, f.app, f.leaderUser, f.memberUser, f.secret, occ)
	cal, body := f.calendar(t, f.member, "date="+feedDate(f.startOn))
	b := roomByName(cal, "Fellowship Hall").Bookings[1]
	if b.Reserved || b.Title != "Quiet circle night" || b.DateURL != occurrenceDatePath(f.secret.ID, occ.OriginalDate) {
		t.Fatalf("invited date: %+v", b)
	}
	if b.GroupID != nil || strings.Contains(body, `"groupName":"Quiet circle"`) {
		t.Fatalf("the invite-only group must stay hidden: %s", body)
	}
}

// One response reads a fixed number of times, however many bookings.
func TestRoomCalendarReadsAreBatched(t *testing.T) {
	f := newRoomCalendarFixture(t)
	day := "date=" + feedDate(f.startOn)
	count := func() int64 {
		qc := countDBReads(t, f.app.DB)
		defer qc.Stop()
		f.calendar(t, f.member, day)
		return qc.Reads()
	}
	before := count()
	for i := 0; i < 5; i++ {
		ev := createFeedEvent(t, f.leader, f.group, "Extra "+itoa(uint(i)), f.startOn.Add(-time.Duration(2*(i+1))*time.Hour), "")
		w := f.leader.do("PUT", "/api/v1/events/"+itoa(ev.ID)+"/dates/"+feedDate(f.startOn)+"/room", map[string]any{"roomId": f.hall.ID}, true)
		if w.Code != http.StatusOK {
			t.Fatalf("book extra: %d %s", w.Code, w.Body.String())
		}
	}
	if after := count(); after != before {
		t.Fatalf("reads grew with bookings: %d then %d", before, after)
	}
}
