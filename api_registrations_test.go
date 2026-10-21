package main

import (
	"fmt"
	"fold/internal/audit"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"fold/models"
)

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
		map[string]any{"date": date, "userId": u.ID}, true)
	if w.Code != http.StatusForbidden {
		t.Fatalf("member remove: %d, want 403", w.Code)
	}
	// The leader removes with a note.
	w = admin.do("POST", fmt.Sprintf("/api/v1/events/%d/registrations/remove", eventID),
		map[string]any{"date": date, "userId": u.ID, "note": "no-show twice"}, true)
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
		map[string]any{"date": date, "userId": u1.ID}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("remove hp1: %d %s", w.Code, w.Body.String())
	}
	w = admin.do("POST", fmt.Sprintf("/api/v1/events/%d/registrations/promote", eventID),
		map[string]any{"date": date, "userId": u2.ID}, true)
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
		map[string]any{"date": date, "userId": u2.ID}, true)
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
		if a.UserID == u1.ID {
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
		modes[at.UserID] = at.AttendanceMode
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
