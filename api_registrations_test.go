package main

import (
	"encoding/json"
	"fmt"
	"fold/internal/audit"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"fold/models"
)

func TestParseRemoteCapacity(t *testing.T) {
	for _, tt := range []struct {
		name, raw string
		allowZero bool
		want      *int
		wantSet   bool
		wantError string
	}{
		{name: "omitted"},
		{name: "unlimited", raw: "null", wantSet: true},
		{name: "positive", raw: "1", want: intPtr(1), wantSet: true},
		{name: "maximum", raw: "100000", want: intPtr(100000), wantSet: true},
		{name: "zero event", raw: "0", wantError: "Uncheck remote attendance instead of a 0 remote seat limit"},
		{name: "zero date", raw: "0", allowZero: true, want: intPtr(0), wantSet: true},
		{name: "negative", raw: "-2", wantError: "remote seat limit must be a whole number from 1 to 100000, or empty for no limit"},
		{name: "decimal", raw: "1.5", wantError: "remote seat limit must be a whole number from 1 to 100000, or empty for no limit"},
		{name: "invalid text", raw: `"1e"`, wantError: "remote seat limit must be a whole number from 1 to 100000, or empty for no limit"},
		{name: "too large", raw: "100001", wantError: "remote seat limit must be a whole number from 1 to 100000, or empty for no limit"},
		{name: "negative date", raw: "-2", allowZero: true, wantError: "remote seat limit for this date must be a whole number from 0 to 100000, or empty"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var raw *json.RawMessage
			if tt.raw != "" {
				value := json.RawMessage(tt.raw)
				raw = &value
			}
			got, set, msg := parseRemoteCapacity(raw, tt.allowZero)
			if !intPtrEqual(got, tt.want) || set != tt.wantSet || msg != tt.wantError {
				t.Fatalf("got (%v, %t, %q), want (%v, %t, %q)", got, set, msg, tt.want, tt.wantSet, tt.wantError)
			}
		})
	}
}

// seedCappedEvent creates a public group event with a capacity and the
// waitlist flag, on a date far enough out to dodge the promotion cutoff.
func seedCappedEvent(t *testing.T, app *App, admin *apiClient, gid string, capacity *int, waitlist bool) (uint, string) {
	t.Helper()
	return seedCappedEventWithGuests(t, app, admin, gid, capacity, waitlist, 5)
}

func seedCappedEventWithGuests(t *testing.T, app *App, admin *apiClient, gid string, capacity *int, waitlist bool, guestsAllowed int) (uint, string) {
	t.Helper()
	start := time.Now().UTC().AddDate(0, 0, 30).Truncate(24 * time.Hour).Add(18 * time.Hour)
	body := map[string]any{
		"title": "Capped", "startsLocal": start.Format("2006-01-02T15:04"),
		"durationMinutes": 60, "tzid": "UTC",
		"waitlistEnabled": waitlist, "guestsAllowed": guestsAllowed,
	}
	if capacity != nil {
		body["capacity"] = *capacity
	}
	w := admin.do("POST", "/api/v1/groups/"+gid+"/events", body, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create capped event: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Event eventJSON `json:"event"`
	}
	decodeJSON(t, w, &created)
	return created.Event.ID, start.Format("2006-01-02")
}

func intPtr(v int) *int { return &v }

// registerAs PUTs a registration and returns the status code and body.
func registerAs(c *apiClient, eventID uint, date, status string, guestCount *int) (int, string) {
	body := map[string]any{"date": date, "status": status}
	if guestCount != nil {
		body["guestCount"] = *guestCount
	}
	w := c.do("PUT", fmt.Sprintf("/api/v1/events/%d/registrations", eventID), body, true)
	return w.Code, w.Body.String()
}

func regStatus(t *testing.T, app *App, userID, occID uint) string {
	t.Helper()
	var r models.Registration
	if err := app.DB.Where("user_id = ? AND occurrence_id = ?", userID, occID).First(&r).Error; err != nil {
		t.Fatalf("load registration: %v", err)
	}
	return r.Status
}

func countRegs(t *testing.T, app *App, occID uint, status string) int64 {
	t.Helper()
	var n int64
	app.DB.Model(&models.Registration{}).Where("occurrence_id = ? AND status = ?", occID, status).Count(&n)
	return n
}

// TestConcurrentGoingRespectsCapacity is the milestone gate: 50 real
// requests race for 10 seats. Exactly 10 end up going and 40 waitlisted,
// with no seat overrun.
func TestConcurrentGoingRespectsCapacity(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "Race")
	// The admin created the group; make the event public via the group default.
	eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(10), true)

	var g models.Group
	if err := app.DB.Where("id = ?", gid).First(&g).Error; err != nil {
		t.Fatal(err)
	}
	const n = 50
	clients := make([]*apiClient, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("racer%02d", i)
		u := newTestUser(t, app, name, "password1234", models.RoleMember)
		addMember(t, app, u, &g, models.MembershipMember)
		clients[i] = loginAPI(t, app, name, "password1234")
	}

	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			code, _ := registerAs(clients[i], eventID, date, "going", nil)
			codes[i] = code
		}(i)
	}
	wg.Wait()
	for i, code := range codes {
		if code != http.StatusOK {
			t.Fatalf("racer %d: %d", i, code)
		}
	}

	var occ models.EventOccurrence
	if err := app.DB.Where("event_id = ?", eventID).First(&occ).Error; err != nil {
		t.Fatal(err)
	}
	if got := countRegs(t, app, occ.ID, models.RegGoing); got != 10 {
		t.Fatalf("going = %d, want 10", got)
	}
	if got := countRegs(t, app, occ.ID, models.RegWaitlisted); got != 40 {
		t.Fatalf("waitlisted = %d, want 40", got)
	}
	used, err := seatsUsed(app.DB, occ.ID)
	if err != nil {
		t.Fatal(err)
	}
	if used != 10 {
		t.Fatalf("seats used = %d, want 10", used)
	}
}

// Going with guests counts every seat: a party of 3 on a 2-seat
// remainder does not fit, so it waitlists (or gets 409 with the
// waitlist off).
func TestGoingWithGuestsCountsSeats(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "Guests")
	eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(10), true)

	var g models.Group
	app.DB.Where("id = ?", gid).First(&g)
	// Fill 8 seats: 8 solo goings.
	for i := 0; i < 8; i++ {
		name := fmt.Sprintf("g%02d", i)
		u := newTestUser(t, app, name, "password1234", models.RoleMember)
		addMember(t, app, u, &g, models.MembershipMember)
		c := loginAPI(t, app, name, "password1234")
		if code, body := registerAs(c, eventID, date, "going", nil); code != http.StatusOK {
			t.Fatalf("fill %d: %d %s", i, code, body)
		}
	}
	// A party of 3 needs 3 seats; only 2 are left.
	u := newTestUser(t, app, "party", "password1234", models.RoleMember)
	addMember(t, app, u, &g, models.MembershipMember)
	c := loginAPI(t, app, "party", "password1234")
	code, body := registerAs(c, eventID, date, "going", intPtr(2))
	if code != http.StatusOK {
		t.Fatalf("party register: %d %s", code, body)
	}
	var occ models.EventOccurrence
	app.DB.Where("event_id = ?", eventID).First(&occ)
	if got := regStatus(t, app, u.ID, occ.ID); got != models.RegWaitlisted {
		t.Fatalf("party status = %q, want waitlisted", got)
	}

	// Same shape with the waitlist off: 409, and the row is never made.
	eventID2, date2 := seedCappedEvent(t, app, admin, gid, intPtr(10), false)
	for i := 0; i < 8; i++ {
		name := fmt.Sprintf("h%02d", i)
		uu := newTestUser(t, app, name, "password1234", models.RoleMember)
		addMember(t, app, uu, &g, models.MembershipMember)
		cc := loginAPI(t, app, name, "password1234")
		if code, _ := registerAs(cc, eventID2, date2, "going", nil); code != http.StatusOK {
			t.Fatalf("fill2 %d: %d", i, code)
		}
	}
	u2 := newTestUser(t, app, "party2", "password1234", models.RoleMember)
	addMember(t, app, u2, &g, models.MembershipMember)
	c2 := loginAPI(t, app, "party2", "password1234")
	if code, body := registerAs(c2, eventID2, date2, "going", intPtr(2)); code != http.StatusConflict {
		t.Fatalf("party on full no-waitlist event: %d %s, want 409", code, body)
	}
	var occ2 models.EventOccurrence
	app.DB.Where("event_id = ?", eventID2).First(&occ2)
	var n int64
	app.DB.Model(&models.Registration{}).Where("user_id = ? AND occurrence_id = ?", u2.ID, occ2.ID).Count(&n)
	if n != 0 {
		t.Fatalf("registration row created on 409 path")
	}
}

// A full event with the waitlist off returns 409 and leaves an existing
// maybe row as maybe.
func TestFullEventWithoutWaitlistRefusesGoing(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "Full")
	eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(1), false)

	var g models.Group
	app.DB.Where("id = ?", gid).First(&g)
	u1 := newTestUser(t, app, "one", "password1234", models.RoleMember)
	u2 := newTestUser(t, app, "two", "password1234", models.RoleMember)
	addMember(t, app, u1, &g, models.MembershipMember)
	addMember(t, app, u2, &g, models.MembershipMember)
	c1 := loginAPI(t, app, "one", "password1234")
	c2 := loginAPI(t, app, "two", "password1234")

	if code, body := registerAs(c1, eventID, date, "going", nil); code != http.StatusOK {
		t.Fatalf("first going: %d %s", code, body)
	}
	if code, body := registerAs(c2, eventID, date, "maybe", nil); code != http.StatusOK {
		t.Fatalf("maybe: %d %s", code, body)
	}
	if code, _ := registerAs(c2, eventID, date, "going", nil); code != http.StatusConflict {
		t.Fatalf("going on full event: %d, want 409", code)
	}
	var occ models.EventOccurrence
	app.DB.Where("event_id = ?", eventID).First(&occ)
	if got := regStatus(t, app, u2.ID, occ.ID); got != models.RegMaybe {
		t.Fatalf("maybe row became %q", got)
	}
}

// Cancelling a going registration promotes the earliest party that
// fits, stamping promoted_at.
func TestCancelPromotesEarliestFittingParty(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "Promote")
	eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(2), true)

	var g models.Group
	app.DB.Where("id = ?", gid).First(&g)
	mk := func(name string) (*models.User, *apiClient) {
		u := newTestUser(t, app, name, "password1234", models.RoleMember)
		addMember(t, app, u, &g, models.MembershipMember)
		return u, loginAPI(t, app, name, "password1234")
	}
	_, c1 := mk("p1")
	_, c2 := mk("p2")
	u3, c3 := mk("p3")

	for _, tc := range []struct {
		c    *apiClient
		want int
	}{{c1, 200}, {c2, 200}, {c3, 200}} {
		if code, body := registerAs(tc.c, eventID, date, "going", nil); code != tc.want {
			t.Fatalf("register: %d %s", code, body)
		}
	}
	var occ models.EventOccurrence
	app.DB.Where("event_id = ?", eventID).First(&occ)
	if got := regStatus(t, app, u3.ID, occ.ID); got != models.RegWaitlisted {
		t.Fatalf("p3 = %q, want waitlisted", got)
	}
	// p1 cancels; p3 must be promoted with promoted_at set.
	if code, body := registerAs(c1, eventID, date, "declined", nil); code != http.StatusOK {
		t.Fatalf("cancel: %d %s", code, body)
	}
	var r models.Registration
	if err := app.DB.Where("user_id = ? AND occurrence_id = ?", u3.ID, occ.ID).First(&r).Error; err != nil {
		t.Fatal(err)
	}
	if r.Status != models.RegGoing || r.PromotedAt == nil {
		t.Fatalf("promoted row: status=%q promoted_at=%v", r.Status, r.PromotedAt)
	}
	if got := countRegs(t, app, occ.ID, models.RegGoing); got != 2 {
		t.Fatalf("going = %d, want 2 (p2 and promoted p3)", got)
	}
}

// A promotion email carries the video link to the person who just got
// in — they are going now, so registered_only allows it (5.8, M1.8). The
// bell item for the same promotion never shows it.
func TestPromotionEmailCarriesVideoLink(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "Promote Video")
	eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(1), true)
	if err := app.DB.Model(&models.Event{}).Where("id = ?", eventID).Updates(map[string]any{
		"video_provider": models.VideoProviderLink, "video_url": videoLink,
		"video_reveal": models.VideoRevealRegisteredOnly,
	}).Error; err != nil {
		t.Fatal(err)
	}

	var g models.Group
	app.DB.Where("id = ?", gid).First(&g)
	mk := func(name string) (*models.User, *apiClient) {
		u := newTestUser(t, app, name, "password1234", models.RoleMember)
		addMember(t, app, u, &g, models.MembershipMember)
		return u, loginAPI(t, app, name, "password1234")
	}
	_, c1 := mk("pv1")
	u2, c2 := mk("pv2")
	if code, body := registerAs(c1, eventID, date, "going", nil); code != http.StatusOK {
		t.Fatalf("first going: %d %s", code, body)
	}
	if code, body := registerAs(c2, eventID, date, "going", nil); code != http.StatusOK {
		t.Fatalf("waitlist: %d %s", code, body)
	}
	// Nothing was promoted yet, so pv2 has heard nothing.
	var sent int64
	app.DB.Model(&models.MailOutbox{}).Where("to_address = ?", u2.Email).Count(&sent)
	if sent != 0 {
		t.Fatalf("emails before promotion = %d, want 0", sent)
	}
	if code, body := registerAs(c1, eventID, date, "declined", nil); code != http.StatusOK {
		t.Fatalf("cancel: %d %s", code, body)
	}
	m := latestEmail(t, app, u2.Email)
	if !strings.Contains(m.Subject, "You're in") || !strings.Contains(m.Body, "Join by video: "+videoLink) {
		t.Fatalf("promotion email = %q / %q, want the video link", m.Subject, m.Body)
	}
	for _, n := range feed(t, c2, "") {
		if strings.Contains(n.Title+n.Body, videoLink) {
			t.Fatalf("bell leaked the video link: %q / %q", n.Title, n.Body)
		}
	}
}

// Remove sets removed, tells the person, and writes the audit row. The
// removed person gets 409 on self-registration; the add action seats
// them again.
func TestRemoveBlocksSelfRegisterUntilAdded(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "Prune")
	eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(10), true)

	var g models.Group
	app.DB.Where("id = ?", gid).First(&g)
	u := newTestUser(t, app, "pruned", "password1234", models.RoleMember)
	addMember(t, app, u, &g, models.MembershipMember)
	c := loginAPI(t, app, "pruned", "password1234")
	if code, body := registerAs(c, eventID, date, "going", nil); code != http.StatusOK {
		t.Fatalf("going: %d %s", code, body)
	}
	var occ models.EventOccurrence
	app.DB.Where("event_id = ?", eventID).First(&occ)

	// A non-manager cannot remove.
	w := c.do("POST", fmt.Sprintf("/api/v1/events/%d/registrations/remove", eventID),
		map[string]any{"date": date, "registrationId": registrationIDFor(t, app, u.ID, eventID, date)}, true)
	if w.Code != http.StatusForbidden {
		t.Fatalf("member remove: %d, want 403", w.Code)
	}
	// The leader removes with a note.
	w = admin.do("POST", fmt.Sprintf("/api/v1/events/%d/registrations/remove", eventID),
		map[string]any{"date": date, "registrationId": registrationIDFor(t, app, u.ID, eventID, date), "note": "no-show twice"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("leader remove: %d %s", w.Code, w.Body.String())
	}
	if got := regStatus(t, app, u.ID, occ.ID); got != models.RegRemoved {
		t.Fatalf("status = %q, want removed", got)
	}
	if n := auditCount(t, app, audit.RegistrationRemoved); n != 1 {
		t.Fatalf("audit registration.removed rows = %d, want 1", n)
	}
	// The removed person is told: one bell item.
	if titles := notificationTitles(t, c); len(titles) != 1 {
		t.Fatalf("notifications for removed person = %d, want 1 (%v)", len(titles), titles)
	}
	// Self-registration is blocked with 409.
	if code, _ := registerAs(c, eventID, date, "going", nil); code != http.StatusConflict {
		t.Fatalf("removed re-register: %d, want 409", code)
	}
	// The add action seats them again.
	w = admin.do("POST", fmt.Sprintf("/api/v1/events/%d/registrations/add", eventID),
		map[string]any{"date": date, "userId": u.ID}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("add back: %d %s", w.Code, w.Body.String())
	}
	if got := regStatus(t, app, u.ID, occ.ID); got != models.RegGoing {
		t.Fatalf("after add: %q, want going", got)
	}
}

// Add over capacity needs the explicit flag and writes its audit row.
// Self-registration with the flag gets 400.
func TestAddOverCapacityNeedsFlag(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "OverCap")
	eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(1), false)

	var g models.Group
	app.DB.Where("id = ?", gid).First(&g)
	u1 := newTestUser(t, app, "seat1", "password1234", models.RoleMember)
	u2 := newTestUser(t, app, "seat2", "password1234", models.RoleMember)
	addMember(t, app, u1, &g, models.MembershipMember)
	addMember(t, app, u2, &g, models.MembershipMember)
	c1 := loginAPI(t, app, "seat1", "password1234")
	c2 := loginAPI(t, app, "seat2", "password1234")
	if code, _ := registerAs(c1, eventID, date, "going", nil); code != http.StatusOK {
		t.Fatalf("first going: %d", code)
	}
	var occ models.EventOccurrence
	app.DB.Where("event_id = ?", eventID).First(&occ)

	// Without the flag: 409.
	w := admin.do("POST", fmt.Sprintf("/api/v1/events/%d/registrations/add", eventID),
		map[string]any{"date": date, "userId": u2.ID}, true)
	if w.Code != http.StatusConflict {
		t.Fatalf("add over capacity without flag: %d, want 409", w.Code)
	}
	// With the flag: seated, and the audit row says so.
	w = admin.do("POST", fmt.Sprintf("/api/v1/events/%d/registrations/add", eventID),
		map[string]any{"date": date, "userId": u2.ID, "overCapacity": true}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("add over capacity: %d %s", w.Code, w.Body.String())
	}
	if got := regStatus(t, app, u2.ID, occ.ID); got != models.RegGoing {
		t.Fatalf("added status = %q, want going", got)
	}
	rows, _ := auditRows(t, app, audit.RegistrationAddedOverCapacity)
	if len(rows) != 1 {
		t.Fatalf("registration.added_over_capacity rows = %d, want 1", len(rows))
	}
	// Self-registration never accepts the flag.
	if code, _ := registerAs(c2, eventID, date, "going", nil); code != http.StatusOK {
		t.Fatalf("control register: %d", code)
	}
	w = c2.do("PUT", fmt.Sprintf("/api/v1/events/%d/registrations", eventID),
		map[string]any{"date": date, "status": "going", "overCapacity": true}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("self-register with overCapacity: %d, want 400", w.Code)
	}
}

// Raising capacity promotes in line order; lowering removes nobody and
// blocks new going results.
func TestCapacityChangePromotesAndNeverRemoves(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "RaiseLower")
	eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(1), true)

	var g models.Group
	app.DB.Where("id = ?", gid).First(&g)
	mk := func(name string) (*models.User, *apiClient) {
		u := newTestUser(t, app, name, "password1234", models.RoleMember)
		addMember(t, app, u, &g, models.MembershipMember)
		return u, loginAPI(t, app, name, "password1234")
	}
	_, c1 := mk("r1")
	u2, c2 := mk("r2")
	u3, c3 := mk("r3")
	for _, c := range []*apiClient{c1, c2, c3} {
		if code, body := registerAs(c, eventID, date, "going", nil); code != http.StatusOK {
			t.Fatalf("register: %d %s", code, body)
		}
	}
	var occ models.EventOccurrence
	app.DB.Where("event_id = ?", eventID).First(&occ)
	if got := regStatus(t, app, u3.ID, occ.ID); got != models.RegWaitlisted {
		t.Fatalf("r3 = %q, want waitlisted", got)
	}

	// Raise to 3: the waitlist drains in line order.
	w := admin.do("PUT", fmt.Sprintf("/api/v1/events/%d", eventID), map[string]any{"capacity": 3}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("raise: %d %s", w.Code, w.Body.String())
	}
	for _, u := range []*models.User{u2, u3} {
		if got := regStatus(t, app, u.ID, occ.ID); got != models.RegGoing {
			t.Fatalf("after raise: %q, want going", got)
		}
	}
	// Lower to 1: nobody is removed; new going results are blocked.
	u4, c4 := mk("r4")
	w = admin.do("PUT", fmt.Sprintf("/api/v1/events/%d", eventID), map[string]any{"capacity": 1}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("lower: %d %s", w.Code, w.Body.String())
	}
	if got := countRegs(t, app, occ.ID, models.RegGoing); got != 3 {
		t.Fatalf("going after lower = %d, want 3 (nobody removed)", got)
	}
	if code, _ := registerAs(c4, eventID, date, "going", nil); code != http.StatusOK {
		t.Fatalf("r4 register: %d, want 200 (waitlisted)", code)
	}
	if got := regStatus(t, app, u4.ID, occ.ID); got != models.RegWaitlisted {
		t.Fatalf("r4 = %q, want waitlisted", got)
	}
}

// Hand promotion seats one waitlisted person when a seat is free, and
// tells them.
func TestPromoteActionSeatsWaitlisted(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "HandPromote")
	eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(1), true)

	var g models.Group
	app.DB.Where("id = ?", gid).First(&g)
	u1 := newTestUser(t, app, "hp1", "password1234", models.RoleMember)
	u2 := newTestUser(t, app, "hp2", "password1234", models.RoleMember)
	addMember(t, app, u1, &g, models.MembershipMember)
	addMember(t, app, u2, &g, models.MembershipMember)
	c1 := loginAPI(t, app, "hp1", "password1234")
	c2 := loginAPI(t, app, "hp2", "password1234")
	if code, _ := registerAs(c1, eventID, date, "going", nil); code != http.StatusOK {
		t.Fatal("hp1 going")
	}
	if code, _ := registerAs(c2, eventID, date, "going", nil); code != http.StatusOK {
		t.Fatal("hp2 going->waitlisted")
	}
	var occ models.EventOccurrence
	app.DB.Where("event_id = ?", eventID).First(&occ)

	// The leader prunes hp1 (no auto-promotion on remove), then hand
	// promotes hp2 into the freed seat.
	w := admin.do("POST", fmt.Sprintf("/api/v1/events/%d/registrations/remove", eventID),
		map[string]any{"date": date, "registrationId": registrationIDFor(t, app, u1.ID, eventID, date)}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("remove hp1: %d %s", w.Code, w.Body.String())
	}
	w = admin.do("POST", fmt.Sprintf("/api/v1/events/%d/registrations/promote", eventID),
		map[string]any{"date": date, "registrationId": registrationIDFor(t, app, u2.ID, eventID, date)}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("promote: %d %s", w.Code, w.Body.String())
	}
	var r models.Registration
	app.DB.Where("user_id = ? AND occurrence_id = ?", u2.ID, occ.ID).First(&r)
	if r.Status != models.RegGoing || r.PromotedAt == nil {
		t.Fatalf("promoted: status=%q promoted_at=%v", r.Status, r.PromotedAt)
	}
	if n := auditCount(t, app, audit.RegistrationPromoted); n != 1 {
		t.Fatalf("registration.promoted audit rows = %d, want 1", n)
	}
	// The promoted person is told.
	if titles := notificationTitles(t, c2); len(titles) != 1 {
		t.Fatalf("notifications for promoted person = %d, want 1 (%v)", len(titles), titles)
	}
}

// Inside the cutoff, a cancellation promotes nobody and writes one
// cutoff notice per leader or host. The promote action still seats the
// person.
func TestCutoffBlocksAutoPromotionButNotHandPromotion(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "Cutoff")
	// An event starting in one hour: inside the 2-hour cutoff.
	start := time.Now().UTC().Add(time.Hour).Truncate(time.Minute)
	w := admin.do("POST", "/api/v1/groups/"+gid+"/events", map[string]any{
		"title": "Soon", "startsLocal": start.Format("2006-01-02T15:04"),
		"durationMinutes": 60, "tzid": "UTC",
		"capacity": 1, "waitlistEnabled": true,
	}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Event eventJSON `json:"event"`
	}
	decodeJSON(t, w, &created)
	eventID := created.Event.ID
	date := start.Format("2006-01-02")

	var g models.Group
	app.DB.Where("id = ?", gid).First(&g)
	u1 := newTestUser(t, app, "co1", "password1234", models.RoleMember)
	u2 := newTestUser(t, app, "co2", "password1234", models.RoleMember)
	addMember(t, app, u1, &g, models.MembershipMember)
	addMember(t, app, u2, &g, models.MembershipMember)
	c1 := loginAPI(t, app, "co1", "password1234")
	c2 := loginAPI(t, app, "co2", "password1234")
	if code, _ := registerAs(c1, eventID, date, "going", nil); code != http.StatusOK {
		t.Fatal("co1 going")
	}
	if code, _ := registerAs(c2, eventID, date, "going", nil); code != http.StatusOK {
		t.Fatal("co2 going->waitlisted")
	}
	var occ models.EventOccurrence
	app.DB.Where("event_id = ?", eventID).First(&occ)

	// co1 cancels inside the cutoff: nobody is promoted.
	if code, body := registerAs(c1, eventID, date, "declined", nil); code != http.StatusOK {
		t.Fatalf("cancel: %d %s", code, body)
	}
	if got := regStatus(t, app, u2.ID, occ.ID); got != models.RegWaitlisted {
		t.Fatalf("co2 = %q, want still waitlisted", got)
	}
	// One cutoff notice per leader or host: the admin made the group, so
	// the admin's bell holds one waitlist.cutoff item.
	if got := findKind(feed(t, admin, ""), "waitlist.cutoff"); got == nil {
		t.Fatalf("no waitlist.cutoff notice for the leader: %v", notificationTitles(t, admin))
	}
	// The promote action still seats the person.
	w = admin.do("POST", fmt.Sprintf("/api/v1/events/%d/registrations/promote", eventID),
		map[string]any{"date": date, "registrationId": registrationIDFor(t, app, u2.ID, eventID, date)}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("hand promote in cutoff: %d %s", w.Code, w.Body.String())
	}
	if got := regStatus(t, app, u2.ID, occ.ID); got != models.RegGoing {
		t.Fatalf("co2 after hand promote = %q, want going", got)
	}
}

// A promoted member gets a "You're in" bell item and email, and the
// email's one-click "I can't come" link cancels the seat.
func TestPromotedMemberGetsYoureInWithCantComeLink(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "PromoMail")
	eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(1), true)
	var g models.Group
	app.DB.Where("id = ?", gid).First(&g)
	_, c1 := mkUser(t, app, &g, "pm1")
	u2, c2 := mkUser(t, app, &g, "pm2")
	if code, _ := registerAs(c1, eventID, date, "going", nil); code != http.StatusOK {
		t.Fatal("pm1 going")
	}
	if code, _ := registerAs(c2, eventID, date, "going", nil); code != http.StatusOK {
		t.Fatal("pm2 waitlisted")
	}
	var occ models.EventOccurrence
	app.DB.Where("event_id = ?", eventID).First(&occ)
	if code, _ := registerAs(c1, eventID, date, "declined", nil); code != http.StatusOK {
		t.Fatal("pm1 cancel")
	}
	n := findKind(feed(t, c2, ""), "registration.promoted")
	if n == nil || !strings.HasPrefix(n.Title, "You're in") {
		t.Fatalf("bell: %+v", n)
	}
	if strings.Contains(n.Body, "/guest/manage/") {
		t.Fatal("bell item shows the manage token")
	}
	m := latestEmail(t, app, testEmail("pm2"))
	if !strings.Contains(m.Subject, "You're in") || !strings.Contains(m.Body, "Can't come") {
		t.Fatalf("promotion email: %q %q", m.Subject, m.Body)
	}
	tok := manageTokenInEmail(t, m)
	anon := anonClient(t, app, "10.4.0.9")
	apiDo(t, anon, "POST", "/api/v1/guest-registrations/manage/"+tok+"/cancel", nil, http.StatusOK)
	if got := regStatus(t, app, u2.ID, occ.ID); got != models.RegCancelled {
		t.Fatalf("after I can't come: %q, want cancelled", got)
	}
}

// A disabled account's going row is kept but is not named on the event
// page, not counted, and frees its seat (DEC-38).
func TestDisabledRegistrationHiddenFromEventPage(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "HideDisabled")
	eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(1), false)
	var g models.Group
	app.DB.Where("id = ?", gid).First(&g)
	u1, c1 := mkUser(t, app, &g, "hd1")
	_, c2 := mkUser(t, app, &g, "hd2")
	if code, _ := registerAs(c1, eventID, date, "going", nil); code != http.StatusOK {
		t.Fatal("hd1 going")
	}
	apiDo(t, admin, "PUT", "/api/v1/admin/users/"+itoa(u1.ID), map[string]any{"status": "disabled"}, http.StatusOK)
	page, _ := getEvent(t, admin, eventID, http.StatusOK)
	o := page.Occurrences[0]
	for _, a := range o.Attendees {
		if a.UserID != nil && *a.UserID == u1.ID {
			t.Fatalf("disabled account listed: %+v", o.Attendees)
		}
	}
	if o.Registrations["going"] != 0 {
		t.Fatalf("disabled registration counted: %+v", o.Registrations)
	}
	// Its seat is free: a capacity-1 event without a waitlist takes hd2.
	if code, body := registerAs(c2, eventID, date, "going", nil); code != http.StatusOK || !strings.Contains(body, `"going"`) {
		t.Fatalf("hd2 into freed seat: %d %s", code, body)
	}
}

// After a re-enable drops the registration, a leader can still add the
// person over capacity by explicit choice (DEC-47).
func TestDroppedOnReenableCanBeAddedOverCapacity(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "DropAdd")
	eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(1), false)
	var g models.Group
	app.DB.Where("id = ?", gid).First(&g)
	u1, c1 := mkUser(t, app, &g, "da1")
	_, c2 := mkUser(t, app, &g, "da2")
	registerAs(c1, eventID, date, "going", nil)
	apiDo(t, admin, "PUT", "/api/v1/admin/users/"+itoa(u1.ID), map[string]any{"status": "disabled"}, http.StatusOK)
	if code, _ := registerAs(c2, eventID, date, "going", nil); code != http.StatusOK {
		t.Fatal("da2 going")
	}
	apiDo(t, admin, "PUT", "/api/v1/admin/users/"+itoa(u1.ID), map[string]any{"status": "active"}, http.StatusOK)
	var occ models.EventOccurrence
	app.DB.Where("event_id = ?", eventID).First(&occ)
	var n int64
	app.DB.Model(&models.Registration{}).Where("user_id = ? AND occurrence_id = ?", u1.ID, occ.ID).Count(&n)
	if n != 0 {
		t.Fatal("registration not dropped")
	}
	apiDo(t, admin, "POST", fmt.Sprintf("/api/v1/events/%d/registrations/add", eventID),
		map[string]any{"date": date, "userId": u1.ID, "overCapacity": true}, http.StatusOK)
	if got := regStatus(t, app, u1.ID, occ.ID); got != models.RegGoing {
		t.Fatalf("added back: %q", got)
	}
}

// --- Remote attendance (M1.8 slice 2) ---

// seedRemoteEvent creates a group event with remote attendance on, the
// given per-pool caps (nil is unlimited), and the waitlist flag.
func seedRemoteEvent(t *testing.T, app *App, admin *apiClient, gid string, capacity, remoteCapacity *int, waitlist bool) (uint, string) {
	t.Helper()
	start := time.Now().UTC().AddDate(0, 0, 30).Truncate(24 * time.Hour).Add(18 * time.Hour)
	body := map[string]any{
		"title": "Hybrid", "startsLocal": start.Format("2006-01-02T15:04"),
		"durationMinutes": 60, "tzid": "UTC",
		"waitlistEnabled": waitlist, "guestsAllowed": 5,
		"remoteAvailable": true,
	}
	if capacity != nil {
		body["capacity"] = *capacity
	}
	if remoteCapacity != nil {
		body["remoteCapacity"] = *remoteCapacity
	}
	w := admin.do("POST", "/api/v1/groups/"+gid+"/events", body, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create remote event: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Event eventJSON `json:"event"`
	}
	decodeJSON(t, w, &created)
	if !created.Event.RemoteAvailable {
		t.Fatalf("created event lost remoteAvailable: %+v", created.Event)
	}
	return created.Event.ID, start.Format("2006-01-02")
}

// registerModeAs PUTs a registration in one attendance mode.
func registerModeAs(c *apiClient, eventID uint, date, status, mode string) (int, string) {
	body := map[string]any{"date": date, "status": status, "attendanceMode": mode}
	w := c.do("PUT", fmt.Sprintf("/api/v1/events/%d/registrations", eventID), body, true)
	return w.Code, w.Body.String()
}

func regMode(t *testing.T, app *App, userID, occID uint) string {
	t.Helper()
	var r models.Registration
	if err := app.DB.Where("user_id = ? AND occurrence_id = ?", userID, occID).First(&r).Error; err != nil {
		t.Fatalf("load registration: %v", err)
	}
	return r.AttendanceMode
}

// The remote pool fills on its own: a full in-person pool does not
// block a remote seat, and each pool waitlists separately. The
// occurrence JSON carries both pools and each attendee's mode.
func TestRemotePoolFillsSeparatelyFromInPerson(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "HybridPools")
	eventID, date := seedRemoteEvent(t, app, admin, gid, intPtr(1), intPtr(1), true)

	var g models.Group
	app.DB.Where("id = ?", gid).First(&g)
	uIn, cIn := mkUser(t, app, &g, "rp-in")
	uRe, cRe := mkUser(t, app, &g, "rp-re")
	uIn2, cIn2 := mkUser(t, app, &g, "rp-in2")
	uRe2, cRe2 := mkUser(t, app, &g, "rp-re2")

	if code, body := registerModeAs(cIn, eventID, date, "going", models.AttendanceInPerson); code != http.StatusOK {
		t.Fatalf("in-person going: %d %s", code, body)
	}
	// In-person is now full; the remote seat is still open.
	if code, body := registerModeAs(cRe, eventID, date, "going", models.AttendanceRemote); code != http.StatusOK {
		t.Fatalf("remote going: %d %s", code, body)
	}
	if code, body := registerModeAs(cIn2, eventID, date, "going", models.AttendanceInPerson); code != http.StatusOK {
		t.Fatalf("in-person overflow: %d %s", code, body)
	}
	if code, body := registerModeAs(cRe2, eventID, date, "going", models.AttendanceRemote); code != http.StatusOK {
		t.Fatalf("remote overflow: %d %s", code, body)
	}

	var occ models.EventOccurrence
	app.DB.Where("event_id = ?", eventID).First(&occ)
	for u, want := range map[*models.User]string{uIn: models.RegGoing, uRe: models.RegGoing, uIn2: models.RegWaitlisted, uRe2: models.RegWaitlisted} {
		if got := regStatus(t, app, u.ID, occ.ID); got != want {
			t.Fatalf("user %d status = %q, want %q", u.ID, got, want)
		}
	}
	if got := regMode(t, app, uRe.ID, occ.ID); got != models.AttendanceRemote {
		t.Fatalf("remote registration mode = %q", got)
	}
	if used, err := seatsUsed(app.DB, occ.ID); err != nil || used != 1 {
		t.Fatalf("in-person seats = %d (%v), want 1", used, err)
	}
	if used, err := remoteSeatsUsed(app.DB, occ.ID); err != nil || used != 1 {
		t.Fatalf("remote seats = %d (%v), want 1", used, err)
	}

	// The occurrence JSON reports both pools and labels remote people.
	list := listOccurrences(t, admin, gid, date, date)
	inst := occurrenceOn(t, list, date)
	if !inst.RemoteAvailable || inst.RemoteCapacity == nil || *inst.RemoteCapacity != 1 ||
		inst.RemoteSeatsUsed != 1 || inst.RemoteSpotsLeft == nil || *inst.RemoteSpotsLeft != 0 {
		t.Fatalf("occurrence remote fields: %+v", inst)
	}
	modes := map[uint]string{}
	for _, at := range inst.Attendees {
		if at.UserID != nil {
			modes[*at.UserID] = at.AttendanceMode
		}
	}
	if modes[uRe.ID] != models.AttendanceRemote || modes[uIn.ID] != models.AttendanceInPerson {
		t.Fatalf("attendee modes = %v", modes)
	}
}

// A full remote pool waitlists a remote party, and cancelling a remote
// going promotes the earliest remote entry — not an in-person entry
// ahead of it in the one waitlist.
func TestRemoteWaitlistPromotesEarliestRemote(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "HybridWait")
	eventID, date := seedRemoteEvent(t, app, admin, gid, intPtr(1), intPtr(1), true)

	var g models.Group
	app.DB.Where("id = ?", gid).First(&g)
	_, cIn := mkUser(t, app, &g, "rw-in")
	uRe, cRe := mkUser(t, app, &g, "rw-re")
	uInWait, cInWait := mkUser(t, app, &g, "rw-inwait")
	uReWait, cReWait := mkUser(t, app, &g, "rw-rewait")
	uReWait2, cReWait2 := mkUser(t, app, &g, "rw-rewait2")

	// Fill both pools, then line up: in-person first, then two remote.
	for _, step := range []struct {
		c    *apiClient
		mode string
	}{{cIn, models.AttendanceInPerson}, {cRe, models.AttendanceRemote},
		{cInWait, models.AttendanceInPerson}, {cReWait, models.AttendanceRemote}, {cReWait2, models.AttendanceRemote}} {
		if code, body := registerModeAs(step.c, eventID, date, "going", step.mode); code != http.StatusOK {
			t.Fatalf("register %s: %d %s", step.mode, code, body)
		}
	}
	var occ models.EventOccurrence
	app.DB.Where("event_id = ?", eventID).First(&occ)
	if got := regStatus(t, app, uReWait.ID, occ.ID); got != models.RegWaitlisted {
		t.Fatalf("first remote overflow = %q, want waitlisted", got)
	}

	// The remote going cancels (switching to maybe keeps their mode,
	// passed explicitly here). The earliest remote entry moves up; the
	// in-person entry ahead of it and the later remote entry stay.
	if code, body := registerModeAs(cRe, eventID, date, "declined", models.AttendanceRemote); code != http.StatusOK {
		t.Fatalf("remote cancel: %d %s", code, body)
	}
	var promoted models.Registration
	app.DB.Where("user_id = ? AND occurrence_id = ?", uReWait.ID, occ.ID).First(&promoted)
	if promoted.Status != models.RegGoing || promoted.PromotedAt == nil || promoted.AttendanceMode != models.AttendanceRemote {
		t.Fatalf("promoted remote row: status=%q mode=%q promoted_at=%v", promoted.Status, promoted.AttendanceMode, promoted.PromotedAt)
	}
	if got := regStatus(t, app, uInWait.ID, occ.ID); got != models.RegWaitlisted {
		t.Fatalf("in-person waitlist = %q, want still waitlisted", got)
	}
	if got := regStatus(t, app, uReWait2.ID, occ.ID); got != models.RegWaitlisted {
		t.Fatalf("second remote waitlist = %q, want still waitlisted", got)
	}
	if got := regStatus(t, app, uRe.ID, occ.ID); got != models.RegDeclined {
		t.Fatalf("canceller = %q, want declined", got)
	}
}

// Remote is refused with 400 when the event does not offer it, and a
// bad mode is a 400 too. No registration row is made.
func TestRemoteRefusedWhenNotAvailable(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "NoRemote")
	eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(10), true)

	var g models.Group
	app.DB.Where("id = ?", gid).First(&g)
	u, c := mkUser(t, app, &g, "nr1")

	if code, body := registerModeAs(c, eventID, date, "going", models.AttendanceRemote); code != http.StatusBadRequest {
		t.Fatalf("remote on non-remote event: %d %s, want 400", code, body)
	}
	if code, body := registerModeAs(c, eventID, date, "going", "carrier_pigeon"); code != http.StatusBadRequest {
		t.Fatalf("bad mode: %d %s, want 400", code, body)
	}
	var occ models.EventOccurrence
	app.DB.Where("event_id = ?", eventID).First(&occ)
	var n int64
	app.DB.Model(&models.Registration{}).Where("user_id = ? AND occurrence_id = ?", u.ID, occ.ID).Count(&n)
	if n != 0 {
		t.Fatal("a row was made for a refused remote registration")
	}
	// In person still works on the same event.
	if code, body := registerModeAs(c, eventID, date, "going", models.AttendanceInPerson); code != http.StatusOK {
		t.Fatalf("in-person control: %d %s", code, body)
	}
}

// Switching a going registration to the other pool re-registers under
// the lock: it takes a seat there when one fits, and waitlists when
// the new pool is full.
func TestSwitchingModeReRegistersInNewPool(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "HybridSwitch")
	eventID, date := seedRemoteEvent(t, app, admin, gid, intPtr(2), intPtr(1), true)

	var g models.Group
	app.DB.Where("id = ?", gid).First(&g)
	uRe, cRe := mkUser(t, app, &g, "sw-re")
	uSw, cSw := mkUser(t, app, &g, "sw-sw")

	if code, body := registerModeAs(cRe, eventID, date, "going", models.AttendanceRemote); code != http.StatusOK {
		t.Fatalf("remote going: %d %s", code, body)
	}
	if code, body := registerModeAs(cSw, eventID, date, "going", models.AttendanceInPerson); code != http.StatusOK {
		t.Fatalf("switcher in-person going: %d %s", code, body)
	}
	var occ models.EventOccurrence
	app.DB.Where("event_id = ?", eventID).First(&occ)

	// The remote pool is full, so switching to remote waitlists.
	code, body := registerModeAs(cSw, eventID, date, "going", models.AttendanceRemote)
	if code != http.StatusOK {
		t.Fatalf("switch to full remote pool: %d %s", code, body)
	}
	if got := regStatus(t, app, uSw.ID, occ.ID); got != models.RegWaitlisted {
		t.Fatalf("after switch to full pool = %q, want waitlisted", got)
	}
	if got := regMode(t, app, uSw.ID, occ.ID); got != models.AttendanceRemote {
		t.Fatalf("waitlisted switch mode = %q, want remote", got)
	}
	if used, _ := seatsUsed(app.DB, occ.ID); used != 0 {
		t.Fatalf("in-person seats after switch = %d, want 0 (seat freed)", used)
	}

	// Switching back to in-person, which has room, seats them again.
	if code, body := registerModeAs(cSw, eventID, date, "going", models.AttendanceInPerson); code != http.StatusOK {
		t.Fatalf("switch back: %d %s", code, body)
	}
	if got := regStatus(t, app, uSw.ID, occ.ID); got != models.RegGoing {
		t.Fatalf("after switch back = %q, want going", got)
	}
	if got := regMode(t, app, uSw.ID, occ.ID); got != models.AttendanceInPerson {
		t.Fatalf("switch-back mode = %q, want in_person", got)
	}
	// The original remote registration was never touched.
	if got := regStatus(t, app, uRe.ID, occ.ID); got != models.RegGoing {
		t.Fatalf("remote going = %q, want going", got)
	}
}

// A waiter who switches pools starts a fresh wait in the new pool, so
// someone already waiting there stays ahead (Whittler, #105).
func TestWaiterSwitchingModeJoinsBackOfNewPool(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "HybridWaitSwitch")
	eventID, date := seedRemoteEvent(t, app, admin, gid, intPtr(1), intPtr(1), true)

	var g models.Group
	app.DB.Where("id = ?", gid).First(&g)
	_, cIn := mkUser(t, app, &g, "ws-in")
	_, cRe := mkUser(t, app, &g, "ws-re")
	uA, cA := mkUser(t, app, &g, "ws-a")
	uB, cB := mkUser(t, app, &g, "ws-b")

	for _, step := range []struct {
		c    *apiClient
		mode string
	}{{cIn, models.AttendanceInPerson}, {cRe, models.AttendanceRemote},
		{cA, models.AttendanceInPerson}, {cB, models.AttendanceRemote}} {
		if code, body := registerModeAs(step.c, eventID, date, "going", step.mode); code != http.StatusOK {
			t.Fatalf("register %s: %d %s", step.mode, code, body)
		}
	}
	var occ models.EventOccurrence
	app.DB.Where("event_id = ?", eventID).First(&occ)

	// A (in-person waiter, ahead of B in line) switches to remote.
	if code, body := registerModeAs(cA, eventID, date, "going", models.AttendanceRemote); code != http.StatusOK {
		t.Fatalf("A switch: %d %s", code, body)
	}
	if got := regStatus(t, app, uA.ID, occ.ID); got != models.RegWaitlisted {
		t.Fatalf("A after switch = %q, want waitlisted", got)
	}
	// A remote seat frees: B, who was waiting for remote first, moves up.
	if code, body := registerModeAs(cRe, eventID, date, "declined", models.AttendanceRemote); code != http.StatusOK {
		t.Fatalf("remote cancel: %d %s", code, body)
	}
	if got := regStatus(t, app, uB.ID, occ.ID); got != models.RegGoing {
		t.Fatalf("B = %q, want going (B stays ahead)", got)
	}
	if got := regStatus(t, app, uA.ID, occ.ID); got != models.RegWaitlisted {
		t.Fatalf("A = %q, want still waitlisted", got)
	}
}

func seedChildcareEvent(t *testing.T, app *App, admin *apiClient, gid string, capacity, childcareCapacity *int, waitlist bool) (uint, string) {
	t.Helper()
	id, date := seedCappedEvent(t, app, admin, gid, capacity, waitlist)
	apiDo(t, admin, "PUT", "/api/v1/events/"+itoa(id), map[string]any{
		"childcareAvailable": true, "childcareCapacity": childcareCapacity,
		"childcareAges": "Ages 2–5", "childcareNotes": "Check in at the nursery.",
	}, http.StatusOK)
	return id, date
}

func childcareAs(t *testing.T, c *apiClient, id uint, date, status string, count int, want int) registrationJSON {
	t.Helper()
	w := c.do("PUT", "/api/v1/events/"+itoa(id)+"/registrations", map[string]any{
		"date": date, "status": status, "childcareCount": count, "childcareAgesNote": "3 and 5",
	}, true)
	if w.Code != want {
		t.Fatalf("childcare registration: %d %s, want %d", w.Code, w.Body.String(), want)
	}
	var body struct {
		Registration registrationJSON `json:"registration"`
		Code         string           `json:"code"`
	}
	decodeJSON(t, w, &body)
	if want == http.StatusConflict && body.Code != "childcare_full" {
		t.Fatalf("conflict code = %q", body.Code)
	}
	return body.Registration
}

func TestChildcareCapacityAndStoredRequests(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "Childcare")
	id, date := seedChildcareEvent(t, app, admin, gid, nil, intPtr(2), true)
	u := newTestUser(t, app, "parent", "password1234", models.RoleMember)
	parent := loginAPI(t, app, "parent", "password1234")
	childcareAs(t, admin, id, date, "going", 1, http.StatusOK)
	r := childcareAs(t, parent, id, date, "going", 1, http.StatusOK)
	if r.ChildcareCount != 1 || r.ChildcareAgesNote != "3 and 5" {
		t.Fatalf("own response = %+v", r)
	}
	// The old value is excluded: an unchanged request still fits.
	childcareAs(t, parent, id, date, "going", 1, http.StatusOK)
	childcareAs(t, parent, id, date, "going", 2, http.StatusConflict)
	var stored models.Registration
	if err := app.DB.First(&stored, "user_id = ? AND occurrence_id = ?", u.ID, r.OccurrenceID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.ChildcareCount != 1 || stored.Status != models.RegGoing {
		t.Fatalf("failed request changed row: %+v", stored)
	}
	// Maybe and declined may keep requests, but use no places.
	for _, status := range []string{models.RegMaybe, models.RegDeclined} {
		childcareAs(t, parent, id, date, status, 20, http.StatusOK)
		if used, err := childcareUsedFor(app.DB, r.OccurrenceID, 0); err != nil || used != 1 {
			t.Fatalf("%s counted: %d %v", status, used, err)
		}
	}
	childcareAs(t, parent, id, date, "going", 20, http.StatusConflict)
	// Zero is always a way to come without childcare.
	childcareAs(t, parent, id, date, "going", 0, http.StatusOK)
	apiDo(t, admin, "PUT", "/api/v1/events/"+itoa(id), map[string]any{"childcareAvailable": false}, http.StatusOK)
	w := parent.do("PUT", "/api/v1/events/"+itoa(id)+"/registrations", map[string]any{"date": date, "status": "going", "childcareCount": 1}, true)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "this event doesn't offer childcare") {
		t.Fatalf("off request: %d %s", w.Code, w.Body.String())
	}
	// Turning the feature off preserves old counts but stops enforcing them.
	apiDo(t, admin, "PUT", "/api/v1/events/"+itoa(id)+"/registrations", map[string]any{"date": date, "status": "maybe"}, http.StatusOK)
	inst := eventOccurrence(t, admin, id)
	if inst.MyChildcareCount == nil || *inst.MyChildcareCount != 1 || inst.ChildcareUsed != nil {
		t.Fatalf("disabled childcare: %+v", inst)
	}
}

func TestChildcareRegistrationValidationAndManagerAdd(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "ChildcareAdd")
	id, date := seedChildcareEvent(t, app, admin, gid, nil, intPtr(1), false)
	parent := newTestUser(t, app, "addedparent", "password1234", models.RoleMember)
	path := "/api/v1/events/" + itoa(id) + "/registrations"
	for _, body := range []map[string]any{
		{"childcareCount": -1}, {"childcareCount": 21}, {"childcareCount": 1.5}, {"childcareAgesNote": strings.Repeat("x", 201)},
	} {
		body["date"], body["status"] = date, "going"
		apiDo(t, admin, "PUT", path, body, http.StatusBadRequest)
	}
	apiDo(t, admin, "POST", path+"/add", map[string]any{"date": date, "userId": parent.ID, "childcareCount": 1, "childcareAgesNote": "4"}, http.StatusOK)
	// Re-adding the same person excludes their old value.
	apiDo(t, admin, "POST", path+"/add", map[string]any{"date": date, "userId": parent.ID, "childcareCount": 1}, http.StatusOK)
	other := newTestUser(t, app, "otherparent", "password1234", models.RoleMember)
	raw := apiDo(t, admin, "POST", path+"/add", map[string]any{"date": date, "userId": other.ID, "childcareCount": 1, "overCapacity": true}, http.StatusConflict)
	if !strings.Contains(raw, "childcare_full") {
		t.Fatal(raw)
	}
	var n int64
	app.DB.Model(&models.Registration{}).Where("user_id = ?", other.ID).Count(&n)
	if n != 0 {
		t.Fatal("refused manager add created a row")
	}
	apiDo(t, admin, "PUT", "/api/v1/events/"+itoa(id), map[string]any{"childcareAvailable": false}, http.StatusOK)
	apiDo(t, admin, "POST", path+"/add", map[string]any{"date": date, "userId": other.ID, "childcareCount": 1}, http.StatusBadRequest)
}

func TestChildcarePromotionSkipsUntilRequestFits(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "ChildcarePromotion")
	id, date := seedChildcareEvent(t, app, admin, gid, intPtr(2), intPtr(2), true)
	path := "/api/v1/events/" + itoa(id)
	// A remote parent fills childcare without using any in-person seats.
	apiDo(t, admin, "PUT", path, map[string]any{"remoteAvailable": true}, http.StatusOK)
	apiDo(t, admin, "PUT", path+"/registrations", map[string]any{
		"date": date, "status": "going", "attendanceMode": "remote", "childcareCount": 2,
	}, http.StatusOK)
	newTestUser(t, app, "seated", "password1234", models.RoleMember)
	a := newTestUser(t, app, "waitera", "password1234", models.RoleMember)
	b := newTestUser(t, app, "waiterb", "password1234", models.RoleMember)
	seated := loginAPI(t, app, "seated", "password1234")
	ca, cb := loginAPI(t, app, "waitera", "password1234"), loginAPI(t, app, "waiterb", "password1234")
	apiDo(t, seated, "PUT", path+"/registrations", map[string]any{
		"date": date, "status": "going", "guestCount": 1,
	}, http.StatusOK)
	r := childcareAs(t, ca, id, date, "going", 2, http.StatusOK)
	childcareAs(t, cb, id, date, "going", 0, http.StatusOK)
	var before models.Registration
	if err := app.DB.First(&before, "user_id = ? AND occurrence_id = ?", a.ID, r.OccurrenceID).Error; err != nil {
		t.Fatal(err)
	}
	if before.Status != models.RegWaitlisted || before.WaitlistedAt == nil {
		t.Fatalf("expected A waiting: %+v", before)
	}
	// Free one seat. A cannot get childcare, so B takes the seat.
	childcareAs(t, seated, id, date, "going", 0, http.StatusOK)
	var after models.Registration
	if err := app.DB.First(&after, before.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.Status != models.RegWaitlisted || after.ChildcareCount != 2 ||
		after.WaitlistedAt == nil || !after.WaitlistedAt.Equal(*before.WaitlistedAt) || after.PromotedAt != nil {
		t.Fatalf("skipping changed A: %+v", after)
	}
	if got := regStatus(t, app, b.ID, r.OccurrenceID); got != models.RegGoing {
		t.Fatalf("B = %q, want going", got)
	}
	if findKind(feed(t, ca, ""), "registration.promoted") != nil {
		t.Fatal("skipped A received a promotion notice")
	}
	// Leave one seat free, then free childcare without changing attendance.
	childcareAs(t, seated, id, date, "declined", 0, http.StatusOK)
	childcareAs(t, admin, id, date, "going", 1, http.StatusOK)
	if got := regStatus(t, app, a.ID, r.OccurrenceID); got != models.RegWaitlisted {
		t.Fatalf("A promoted with only one childcare spot free: %q", got)
	}
	childcareAs(t, admin, id, date, "going", 0, http.StatusOK)
	if err := app.DB.First(&after, before.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.Status != models.RegGoing || after.ChildcareCount != 2 || after.ChildcareAgesNote != "3 and 5" ||
		after.PromotedAt == nil || after.WaitlistedAt == nil || !after.WaitlistedAt.Equal(*before.WaitlistedAt) {
		t.Fatalf("promotion lost A's request or history: %+v", after)
	}
	if findKind(feed(t, ca, ""), "registration.promoted") == nil {
		t.Fatal("A received no promotion notice")
	}
}

func TestChildcareBatchPromotionsShareRemainingPlaces(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "BatchChildcare")
	id, date := seedChildcareEvent(t, app, admin, gid, intPtr(1), intPtr(1), true)
	childcareAs(t, admin, id, date, "going", 0, http.StatusOK)
	people := []*models.User{}
	for _, name := range []string{"firstparent", "secondparent"} {
		u := newTestUser(t, app, name, "password1234", models.RoleMember)
		people = append(people, u)
		r := childcareAs(t, loginAPI(t, app, name, "password1234"), id, date, "going", 1, http.StatusOK)
		if r.Status != models.RegWaitlisted {
			t.Fatal("expected waitlisted")
		}
	}
	apiDo(t, admin, "PUT", "/api/v1/events/"+itoa(id), map[string]any{"capacity": 3}, http.StatusOK)
	var occ models.EventOccurrence
	if err := app.DB.First(&occ, "event_id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	for i, u := range people {
		var reg models.Registration
		if err := app.DB.First(&reg, "user_id = ? AND occurrence_id = ?", u.ID, occ.ID).Error; err != nil {
			t.Fatal(err)
		}
		want := models.RegGoing
		if i == 1 {
			want = models.RegWaitlisted
		}
		if reg.Status != want || reg.ChildcareCount != 1 {
			t.Fatalf("promotion %d = %+v", i, reg)
		}
	}
	if n, err := childcareUsedFor(app.DB, occ.ID, 0); err != nil || n != 1 {
		t.Fatalf("promotions overfilled childcare: %d %v", n, err)
	}
}

func TestChildcareFreedPromotesOnlyWithSeat(t *testing.T) {
	for _, action := range []string{"lower count", "manager lowers count", "decline", "maybe", "remove", "disable", "manage cancel", "raise limit", "remove limit", "turn off childcare"} {
		for _, seatFree := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/seatFree=%t", action, seatFree), func(t *testing.T) {
				app := testApp(t)
				admin, gid := openGroup(t, app, "FreeChildcare")
				id, date := seedChildcareEvent(t, app, admin, gid, intPtr(1), intPtr(2), true)
				path := "/api/v1/events/" + itoa(id)
				apiDo(t, admin, "PUT", path, map[string]any{"remoteAvailable": true}, http.StatusOK)
				parent := newTestUser(t, app, "parent", "password1234", models.RoleMember)
				waiter := newTestUser(t, app, "waiter", "password1234", models.RoleMember)
				pc, wc := loginAPI(t, app, "parent", "password1234"), loginAPI(t, app, "waiter", "password1234")
				apiDo(t, pc, "PUT", path+"/registrations", map[string]any{
					"date": date, "status": "going", "attendanceMode": "remote", "childcareCount": 2,
				}, http.StatusOK)
				childcareAs(t, admin, id, date, "going", 0, http.StatusOK)
				r := childcareAs(t, wc, id, date, "going", 2, http.StatusOK)
				var before models.Registration
				if err := app.DB.First(&before, "user_id = ? AND occurrence_id = ?", waiter.ID, r.OccurrenceID).Error; err != nil {
					t.Fatal(err)
				}
				if before.Status != models.RegWaitlisted || before.WaitlistedAt == nil {
					t.Fatalf("expected waiter: %+v", before)
				}
				if seatFree {
					childcareAs(t, admin, id, date, "declined", 0, http.StatusOK)
					if got := regStatus(t, app, waiter.ID, r.OccurrenceID); got != models.RegWaitlisted {
						t.Fatalf("promoted before childcare freed: %q", got)
					}
				}
				switch action {
				case "lower count":
					childcareAs(t, pc, id, date, "going", 0, http.StatusOK)
				case "manager lowers count":
					apiDo(t, admin, "POST", path+"/registrations/add", map[string]any{
						"date": date, "userId": parent.ID, "childcareCount": 0,
					}, http.StatusOK)
				case "decline", "maybe":
					status := models.RegDeclined
					if action == "maybe" {
						status = models.RegMaybe
					}
					childcareAs(t, pc, id, date, status, 2, http.StatusOK)
				case "remove":
					apiDo(t, admin, "POST", path+"/registrations/remove", map[string]any{"date": date, "registrationId": registrationIDFor(t, app, parent.ID, id, date)}, http.StatusOK)
				case "disable":
					apiDo(t, admin, "PUT", "/api/v1/admin/users/"+itoa(parent.ID), map[string]any{"status": "disabled"}, http.StatusOK)
				case "manage cancel":
					raw, hash, err := createInvitationToken()
					if err != nil {
						t.Fatal(err)
					}
					if err := app.DB.Model(&models.Registration{}).
						Where("user_id = ? AND occurrence_id = ?", parent.ID, r.OccurrenceID).
						Update("manage_token_hash", hash).Error; err != nil {
						t.Fatal(err)
					}
					apiDo(t, anonClient(t, app, "10.4.0.10"), "POST", "/api/v1/guest-registrations/manage/"+raw+"/cancel", nil, http.StatusOK)
				case "raise limit":
					apiDo(t, admin, "PUT", path, map[string]any{"childcareCapacity": 4}, http.StatusOK)
				case "remove limit":
					apiDo(t, admin, "PUT", path, map[string]any{"childcareCapacity": nil}, http.StatusOK)
				case "turn off childcare":
					apiDo(t, admin, "PUT", path, map[string]any{"childcareAvailable": false}, http.StatusOK)
				}
				var after models.Registration
				if err := app.DB.First(&after, before.ID).Error; err != nil {
					t.Fatal(err)
				}
				want := models.RegWaitlisted
				if seatFree {
					want = models.RegGoing
				}
				if after.Status != want || after.ChildcareCount != 2 || after.ChildcareAgesNote != before.ChildcareAgesNote ||
					after.WaitlistedAt == nil || !after.WaitlistedAt.Equal(*before.WaitlistedAt) || (after.PromotedAt != nil) != seatFree {
					t.Fatalf("after freeing childcare, want %s with preserved request and wait time: %+v", want, after)
				}
			})
		}
	}
}

func TestManualChildcarePromotionRequiresRoom(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "ManualChildcare")
	id, date := seedChildcareEvent(t, app, admin, gid, intPtr(1), intPtr(2), true)
	path := "/api/v1/events/" + itoa(id)
	apiDo(t, admin, "PUT", path, map[string]any{"remoteAvailable": true}, http.StatusOK)
	apiDo(t, admin, "PUT", path+"/registrations", map[string]any{
		"date": date, "status": "going", "attendanceMode": "remote", "childcareCount": 2,
	}, http.StatusOK)
	seated := newTestUser(t, app, "seated", "password1234", models.RoleMember)
	waiter := newTestUser(t, app, "waiter", "password1234", models.RoleMember)
	childcareAs(t, loginAPI(t, app, "seated", "password1234"), id, date, "going", 0, http.StatusOK)
	wc := loginAPI(t, app, "waiter", "password1234")
	r := childcareAs(t, wc, id, date, "going", 2, http.StatusOK)
	var before models.Registration
	if err := app.DB.First(&before, "user_id = ? AND occurrence_id = ?", waiter.ID, r.OccurrenceID).Error; err != nil {
		t.Fatal(err)
	}
	apiDo(t, admin, "POST", path+"/registrations/remove", map[string]any{"date": date, "registrationId": registrationIDFor(t, app, seated.ID, id, date)}, http.StatusOK)
	response := apiDo(t, admin, "POST", path+"/registrations/promote", map[string]any{"date": date, "registrationId": registrationIDFor(t, app, waiter.ID, id, date)}, http.StatusConflict)
	if !strings.Contains(response, "childcare_full") {
		t.Fatal(response)
	}
	var after models.Registration
	if err := app.DB.First(&after, before.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.Status != models.RegWaitlisted || after.ChildcareCount != 2 || after.PromotedAt != nil ||
		after.WaitlistedAt == nil || before.WaitlistedAt == nil || !after.WaitlistedAt.Equal(*before.WaitlistedAt) {
		t.Fatalf("refused promotion changed request: %+v", after)
	}
	if auditCount(t, app, audit.RegistrationPromoted) != 0 || findKind(feed(t, wc, ""), "registration.promoted") != nil {
		t.Fatal("refused promotion wrote an audit or notice")
	}
	// In the cutoff, childcare becoming free leaves promotion to the leader.
	if err := app.DB.Model(&models.EventOccurrence{}).Where("id = ?", r.OccurrenceID).
		Update("starts_at", time.Now().UTC().Add(time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	childcareAs(t, admin, id, date, "going", 0, http.StatusOK)
	if got := regStatus(t, app, waiter.ID, r.OccurrenceID); got != models.RegWaitlisted {
		t.Fatalf("auto-promoted in cutoff: %q", got)
	}
	apiDo(t, admin, "POST", path+"/registrations/promote", map[string]any{"date": date, "registrationId": registrationIDFor(t, app, waiter.ID, id, date)}, http.StatusOK)
	if err := app.DB.First(&after, before.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.Status != models.RegGoing || after.ChildcareCount != 2 || after.ChildcareAgesNote != before.ChildcareAgesNote || after.PromotedAt == nil {
		t.Fatalf("manual promotion lost request: %+v", after)
	}
}

func TestConcurrentChildcareRequestsRespectLimit(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "ChildcareRace")
	id, date := seedChildcareEvent(t, app, admin, gid, nil, intPtr(2), false)
	const n = 8
	clients := make([]*apiClient, n)
	for i := range clients {
		name := fmt.Sprintf("parent%d", i)
		newTestUser(t, app, name, "password1234", models.RoleMember)
		clients[i] = loginAPI(t, app, name, "password1234")
	}
	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := range clients {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = clients[i].do("PUT", "/api/v1/events/"+itoa(id)+"/registrations", map[string]any{"date": date, "status": "going", "childcareCount": 1}, true).Code
		}(i)
	}
	wg.Wait()
	accepted := 0
	for _, code := range codes {
		if code == http.StatusOK {
			accepted++
		} else if code != http.StatusConflict {
			t.Fatalf("unexpected race response: %d", code)
		}
	}
	if accepted != 2 {
		t.Fatalf("accepted %d requests, want 2", accepted)
	}
	var occ models.EventOccurrence
	if err := app.DB.First(&occ, "event_id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	if used, err := childcareUsedFor(app.DB, occ.ID, 0); err != nil || used != 2 {
		t.Fatalf("childcare used=%d %v", used, err)
	}
}

func registrationIDFor(t *testing.T, app *App, userID, eventID uint, date string) uint {
	t.Helper()
	var r models.Registration
	if err := app.DB.Joins("JOIN event_occurrences ON event_occurrences.id = registrations.occurrence_id").Where("registrations.user_id = ? AND event_occurrences.event_id = ? AND event_occurrences.original_date = ?", userID, eventID, date).First(&r).Error; err != nil {
		t.Fatal(err)
	}
	return r.ID
}

// householdFamily creates selectable adults and a child through Profile, then
// confirms the child's person link through the ordinary admin action.
func householdFamily(t *testing.T, app *App, admin *apiClient, name string) (*models.User, *apiClient, householdPersonJSON, householdPersonJSON) {
	t.Helper()
	user := newTestUser(t, app, name, "password1234", models.RoleMember)
	client := loginAPI(t, app, name, "password1234")
	spouse := addMyPerson(t, client, map[string]any{"firstName": "Mary", "kind": "adult", "birthDate": "1990-01-01"})
	child := addMyPerson(t, client, map[string]any{"firstName": "Kit", "kind": "child", "birthDate": "2020-05-01", "careNotes": "Private allergy note"})
	if child.GuardianLinkID == nil {
		t.Fatal("missing guardian link")
	}
	apiDo(t, admin, "POST", "/api/v1/admin/guardian-links/"+itoa(*child.GuardianLinkID)+"/confirm", map[string]any{"checkNote": "Known family"}, http.StatusOK)
	return user, client, spouse, child
}

func batchAnswer(t *testing.T, c *apiClient, eventID uint, date string, body map[string]any, code int) registrationBatchJSON {
	t.Helper()
	body["date"] = date
	w := c.do("PUT", fmt.Sprintf("/api/v1/events/%d/registrations", eventID), body, true)
	if w.Code != code {
		t.Fatalf("batch: %d want %d: %s", w.Code, code, w.Body.String())
	}
	var result registrationBatchJSON
	if code == http.StatusOK {
		decodeJSON(t, w, &result)
	}
	return result
}

func TestHouseholdBatchAnswersAndStableHolder(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "Family")
	eventID, date := seedChildcareEvent(t, app, admin, gid, intPtr(2), intPtr(1), true)
	apiDo(t, admin, "PUT", "/api/v1/events/"+itoa(eventID), map[string]any{"remoteAvailable": true, "remoteCapacity": 1}, http.StatusOK)
	holder, c, spouse, child := householdFamily(t, app, admin, "family")
	result := batchAnswer(t, c, eventID, date, map[string]any{"status": "going", "attendanceMode": "remote", "people": []map[string]any{{"personId": spouse.ID, "status": "going", "attendanceMode": "in_person"}, {"personId": child.ID, "status": "going", "attendanceMode": "in_person", "childcareRequested": true}}}, http.StatusOK)
	if result.Registration == nil || len(result.People) != 2 {
		t.Fatalf("batch shape: %+v", result)
	}
	ids := map[uint]bool{result.Registration.RegistrationID: true}
	for _, r := range result.People {
		if r.Status != models.RegGoing || ids[r.RegistrationID] || r.RegisteredBy != holder.ID {
			t.Fatalf("subject answer %+v", r)
		}
		ids[r.RegistrationID] = true
	}
	if used, err := seatsUsed(app.DB, result.Registration.OccurrenceID); err != nil || used != 2 {
		t.Fatalf("seats %d: %v", used, err)
	}
	if used, err := childcareUsedFor(app.DB, result.Registration.OccurrenceID, 0); err != nil || used != 1 {
		t.Fatalf("care %d: %v", used, err)
	}
	// People-only edits preserve unnamed holder state and unmentioned people.
	result = batchAnswer(t, c, eventID, date, map[string]any{"people": []map[string]any{{"personId": child.ID, "status": "maybe", "childcareRequested": false}}}, http.StatusOK)
	if result.Registration != nil || len(result.People) != 1 || result.People[0].Status != models.RegMaybe {
		t.Fatalf("people-only %+v", result)
	}
	second := newTestUser(t, app, "second", "password1234", models.RoleMember)
	household := getMyHousehold(t, c)
	if err := app.DB.Create(&models.HouseholdAccount{HouseholdID: household.ID, UserID: second.ID, Relation: models.HouseholdRelationAdult}).Error; err != nil {
		t.Fatal(err)
	}
	secondClient := loginAPI(t, app, "second", "password1234")
	result = batchAnswer(t, secondClient, eventID, date, map[string]any{"people": []map[string]any{{"personId": child.ID, "status": "going"}}}, http.StatusOK)
	if result.People[0].RegisteredBy != holder.ID {
		t.Fatal("edit transferred ownership")
	}
	registrationID := result.People[0].RegistrationID
	apiDo(t, admin, "POST", "/api/v1/events/"+itoa(eventID)+"/registrations/remove", map[string]any{"date": date, "registrationId": registrationID}, http.StatusOK)
	batchAnswer(t, secondClient, eventID, date, map[string]any{"people": []map[string]any{{"personId": child.ID, "status": "going", "attendanceMode": "remote"}}}, http.StatusConflict)
	apiDo(t, admin, "POST", "/api/v1/events/"+itoa(eventID)+"/registrations/add", map[string]any{"date": date, "personId": child.ID, "registeredBy": holder.ID}, http.StatusOK)
	var restored models.Registration
	if err := app.DB.First(&restored, registrationID).Error; err != nil || restored.Status != models.RegGoing {
		t.Fatalf("restore %+v %v", restored, err)
	}
}

func TestHouseholdBatchValidationRollsBack(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "BatchValidation")
	eventID, date := seedChildcareEvent(t, app, admin, gid, intPtr(1), intPtr(1), false)
	_, c, spouse, child := householdFamily(t, app, admin, "validation")
	for _, tt := range []struct {
		name string
		body map[string]any
		code int
	}{
		{"cross household", map[string]any{"status": "going", "people": []map[string]any{{"personId": 999999, "status": "going"}}}, 404},
		{"duplicate", map[string]any{"status": "going", "people": []map[string]any{{"personId": child.ID, "status": "going"}, {"personId": child.ID, "status": "going"}}}, 400},
		{"not enough attendance", map[string]any{"status": "going", "people": []map[string]any{{"personId": spouse.ID, "status": "going"}}}, 409},
		{"nested unnamed counts", map[string]any{"people": []map[string]any{{"personId": child.ID, "status": "going", "guestCount": 1}}}, 400},
		{"arbitrary holder", map[string]any{"registeredBy": 9, "people": []map[string]any{{"personId": child.ID, "status": "going"}}}, 400},
		{"over capacity forbidden", map[string]any{"status": "going", "overCapacity": true}, 400},
		{"holder fields without status", map[string]any{"guestCount": 0, "people": []map[string]any{{"personId": child.ID, "status": "going"}}}, 400},
		{"remote care", map[string]any{"people": []map[string]any{{"personId": child.ID, "status": "going", "attendanceMode": "remote", "childcareRequested": true}}}, 409},
	} {
		t.Run(tt.name, func(t *testing.T) {
			batchAnswer(t, c, eventID, date, tt.body, tt.code)
			var n int64
			app.DB.Model(&models.Registration{}).Count(&n)
			if n != 0 {
				t.Fatalf("failed batch wrote %d rows", n)
			}
		})
	}
}

func TestHouseholdReleaseHonorsEarlierWaiter(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "FamilyQueue")
	eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(1), true)
	_, c, spouse, _ := householdFamily(t, app, admin, "queueholder")
	batchAnswer(t, c, eventID, date, map[string]any{"status": "going"}, http.StatusOK)
	waiter := newTestUser(t, app, "early", "password1234", models.RoleMember)
	wc := loginAPI(t, app, "early", "password1234")
	code, raw := registerAs(wc, eventID, date, "going", nil)
	if code != 200 {
		t.Fatal(raw)
	}
	result := batchAnswer(t, c, eventID, date, map[string]any{"status": "declined", "people": []map[string]any{{"personId": spouse.ID, "status": "going"}}}, http.StatusOK)
	if result.People[0].Status != models.RegWaitlisted {
		t.Fatalf("new person jumped queue: %+v", result)
	}
	if got := regStatus(t, app, waiter.ID, result.Registration.OccurrenceID); got != models.RegGoing {
		t.Fatalf("earlier waiter %s", got)
	}
	same := batchAnswer(t, c, eventID, date, map[string]any{"people": []map[string]any{{"personId": spouse.ID, "status": "going"}}}, http.StatusOK)
	if same.People[0].RegistrationID != result.People[0].RegistrationID || same.People[0].WaitlistPosition == nil || *same.People[0].WaitlistPosition != 1 {
		t.Fatalf("idempotent wait %+v", same)
	}
}

func TestConcurrentHouseholdAndAccountSeats(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "MixedRace")
	eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(10), true)
	type request struct {
		client *apiClient
		person uint
	}
	requests := []request{}
	for i := 0; i < 20; i++ {
		name := fmt.Sprintf("mixed%d", i)
		newTestUser(t, app, name, "password1234", models.RoleMember)
		c := loginAPI(t, app, name, "password1234")
		id := uint(0)
		if i%2 == 0 {
			p := addMyPerson(t, c, map[string]any{"firstName": "Adult", "kind": "adult", "birthDate": "1990-01-01"})
			id = p.ID
		}
		requests = append(requests, request{c, id})
	}
	var wg sync.WaitGroup
	failures := make(chan string, len(requests))
	for _, req := range requests {
		wg.Add(1)
		go func(req request) {
			defer wg.Done()
			body := map[string]any{"date": date, "status": "going"}
			if req.person != 0 {
				body = map[string]any{"date": date, "people": []map[string]any{{"personId": req.person, "status": "going"}}}
			}
			w := req.client.do("PUT", "/api/v1/events/"+itoa(eventID)+"/registrations", body, true)
			if w.Code != 200 {
				failures <- w.Body.String()
			}
		}(req)
	}
	wg.Wait()
	close(failures)
	for failure := range failures {
		t.Error(failure)
	}
	var o models.EventOccurrence
	app.DB.Where("event_id = ?", eventID).First(&o)
	if got, err := seatsUsed(app.DB, o.ID); err != nil || got != 10 {
		t.Fatalf("seats %d: %v", got, err)
	}
	if countRegs(t, app, o.ID, models.RegWaitlisted) != 10 {
		t.Fatal("lost waitlisted subjects")
	}
}
