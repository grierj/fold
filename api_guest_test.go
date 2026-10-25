package main

import (
	"errors"
	"fmt"
	"fold/internal/audit"
	"fold/internal/notify"
	"fold/internal/policy"
	"fold/models"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

// guestDate is the one date scheduleGroupEvent creates.
const guestDate = "2026-12-01"

func guestBody(name, email string, over18 bool) map[string]any {
	return map[string]any{"name": name, "email": email, "date": guestDate, "over18": over18}
}

func guestSubmit(t *testing.T, c *apiClient, eventID uint, body map[string]any, want int) string {
	t.Helper()
	return apiDo(t, c, "POST", fmt.Sprintf("/api/v1/events/%d/guest-registrations", eventID), body, want)
}

func guestUser(t *testing.T, app *App, email string) models.User {
	t.Helper()
	var u models.User
	if err := app.DB.Where("email = ?", email).First(&u).Error; err != nil {
		t.Fatalf("guest user %s: %v", email, err)
	}
	return u
}

func guestSignupCount(t *testing.T, app *App, email string) int64 {
	t.Helper()
	var n int64
	app.DB.Model(&models.GuestSignup{}).Where("email = ?", email).Count(&n)
	return n
}

func guestEmailCount(t *testing.T, app *App, email string) int64 {
	t.Helper()
	var n int64
	app.DB.Model(&models.MailOutbox{}).Where("to_address = ?", email).Count(&n)
	return n
}

func guestRegistrationRow(t *testing.T, app *App, userID uint) models.Registration {
	t.Helper()
	var r models.Registration
	if err := app.DB.Where("user_id = ?", userID).First(&r).Error; err != nil {
		t.Fatalf("registration for user %d: %v", userID, err)
	}
	return r
}

// guestConfirm submits the guest form and redeems the emailed link,
// returning the confirm response.
func guestConfirm(t *testing.T, app *App, c *apiClient, eventID uint, name, email string, createAccount bool) (guestRegistrationJSON, string) {
	t.Helper()
	body := guestBody(name, email, true)
	body["createAccount"] = createAccount
	guestSubmit(t, c, eventID, body, http.StatusOK)
	token := tokenInEmail(t, latestEmail(t, app, email))
	w := c.do("POST", "/api/v1/guest-registrations/confirm", map[string]string{"token": token}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("confirm guest: %d %s", w.Code, w.Body.String())
	}
	var res struct {
		Registration guestRegistrationJSON `json:"registration"`
		ManageToken  string                `json:"manageToken"`
	}
	decodeJSON(t, w, &res)
	if res.ManageToken == "" {
		t.Fatal("confirm returned no manage token")
	}
	return res.Registration, res.ManageToken
}

// M1.4 acceptance: a guest registers without an account, gets a
// confirmation email with a manage link, and can cancel from it.
// Nothing exists before the emailed link is redeemed.
func TestGuestRegistrationFlow(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	gid := createGroupAs(t, admin, "Garden Club", models.VisibilityPublic)
	ev := scheduleGroupEvent(t, admin, gid, "Picnic")
	anon := anonClient(t, app, "10.4.0.1")

	raw := guestSubmit(t, anon, ev.ID, guestBody("Gail Guest", "gail@example.com", true), http.StatusOK)
	if !strings.Contains(raw, signupThanks) {
		t.Fatalf("submit answer: %s", raw)
	}
	var users int64
	app.DB.Model(&models.User{}).Where("email = ?", "gail@example.com").Count(&users)
	if users != 0 || guestSignupCount(t, app, "gail@example.com") != 1 {
		t.Fatalf("before verify: %d users, %d signups", users, guestSignupCount(t, app, "gail@example.com"))
	}
	var regs int64
	app.DB.Model(&models.Registration{}).Count(&regs)
	if regs != 0 {
		t.Fatalf("registrations before verify: %d", regs)
	}

	token := tokenInEmail(t, latestEmail(t, app, "gail@example.com"))
	w := anon.do("POST", "/api/v1/guest-registrations/confirm", map[string]string{"token": token}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("confirm: %d %s", w.Code, w.Body.String())
	}
	var res struct {
		Registration guestRegistrationJSON `json:"registration"`
		ManageToken  string                `json:"manageToken"`
	}
	decodeJSON(t, w, &res)
	if res.Registration.Status != models.RegGoing || res.Registration.Title != "Picnic" {
		t.Fatalf("confirmed registration: %+v", res.Registration)
	}
	// The verification link is single use.
	w = anon.do("POST", "/api/v1/guest-registrations/confirm", map[string]string{"token": token}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("verify token reuse: %d", w.Code)
	}

	u := guestUser(t, app, "gail@example.com")
	if u.Status != models.UserGuest || u.PasswordHash != nil || u.CanSignIn() {
		t.Fatalf("guest user: %+v", u)
	}
	w = anon.do("POST", "/api/v1/auth/login",
		map[string]string{"email": "gail@example.com", "password": "whatever1234"}, true)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("guest login: %d", w.Code)
	}
	reg := guestRegistrationRow(t, app, u.ID)
	if reg.Source != models.RegSourceGuestForm || reg.ManageTokenHash == "" || reg.ManageTokenHash == res.ManageToken {
		t.Fatalf("registration row: %+v", reg)
	}
	if m := latestEmail(t, app, "gail@example.com"); !strings.Contains(m.Subject, "You're registered") ||
		!strings.Contains(m.Body, "/guest/manage/"+res.ManageToken) {
		t.Fatalf("confirmation email: %q %q", m.Subject, m.Body)
	}
	// The guest has no membership: registering never joins the group.
	if n := activeCount(app, &models.Group{ID: gid}); n != 1 {
		t.Fatalf("group members after guest registration: %d", n)
	}

	// The leader sees one going and the guest's name.
	page, _ := getEvent(t, admin, ev.ID, http.StatusOK)
	if len(page.Occurrences) == 0 || page.Occurrences[0].Registrations["going"] != 1 {
		t.Fatalf("occurrences: %+v", page.Occurrences)
	}

	// A forged manage token finds nothing.
	apiDo(t, anon, "GET", "/api/v1/guest-registrations/manage/not-a-real-token", nil, http.StatusNotFound)

	// The manage link shows the registration, then cancels it.
	w = anon.do("GET", "/api/v1/guest-registrations/manage/"+res.ManageToken, nil, false)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"going"`) {
		t.Fatalf("manage show: %d %s", w.Code, w.Body.String())
	}
	apiDo(t, anon, "POST", "/api/v1/guest-registrations/manage/"+res.ManageToken+"/cancel", nil, http.StatusOK)
	if r := guestRegistrationRow(t, app, u.ID); r.Status != models.RegCancelled {
		t.Fatalf("after cancel: %+v", r)
	}
	if auditCount(t, app, audit.RegistrationCancelled) != 1 {
		t.Fatal("guest cancellation not audited")
	}
	// Cancel is idempotent, and the leader's counts forget the guest.
	apiDo(t, anon, "POST", "/api/v1/guest-registrations/manage/"+res.ManageToken+"/cancel", nil, http.StatusOK)
	page, _ = getEvent(t, admin, ev.ID, http.StatusOK)
	if page.Occurrences[0].Registrations["going"] != 0 || len(page.Occurrences[0].Attendees) != 0 {
		t.Fatalf("after cancel: %+v", page.Occurrences[0])
	}
}

// M1.4 acceptance: without "I am 18 or older", nothing is stored and
// the guardian message shows. The honeypot stores nothing either.
func TestGuestAgeGateAndHoneypotStoreNothing(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	gid := createGroupAs(t, admin, "Garden Club", models.VisibilityPublic)
	ev := scheduleGroupEvent(t, admin, gid, "Picnic")
	anon := anonClient(t, app, "10.4.1.1")

	raw := guestSubmit(t, anon, ev.ID, guestBody("Young Person", "young@example.com", false), http.StatusBadRequest)
	if !strings.Contains(raw, "parent or guardian") {
		t.Fatalf("age gate answer: %s", raw)
	}
	body := guestBody("Bot", "bot@example.com", true)
	body["website"] = "https://spam.example"
	guestSubmit(t, anon, ev.ID, body, http.StatusOK)

	for _, email := range []string{"young@example.com", "bot@example.com"} {
		if guestSignupCount(t, app, email) != 0 || guestEmailCount(t, app, email) != 0 {
			t.Fatalf("%s: signups %d, emails %d", email,
				guestSignupCount(t, app, email), guestEmailCount(t, app, email))
		}
	}
}

// M1.4 acceptance: a guest form for an email that already has an
// account gets the usual response, and the email asks the person to
// sign in.
func TestGuestExistingEmailIsUniform(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	newTestUser(t, app, "taken", "password1234", models.RoleMember)
	gid := createGroupAs(t, admin, "Garden Club", models.VisibilityPublic)
	ev := scheduleGroupEvent(t, admin, gid, "Picnic")
	anon := anonClient(t, app, "10.4.2.1")

	fresh := guestSubmit(t, anon, ev.ID, guestBody("Fresh", "fresh@example.com", true), http.StatusOK)
	taken := guestSubmit(t, anon, ev.ID, guestBody("Taken", "TAKEN@example.com", true), http.StatusOK)
	if fresh != taken {
		t.Fatalf("answers differ:\n%s\n%s", fresh, taken)
	}
	if guestSignupCount(t, app, "taken@example.com") != 0 {
		t.Fatal("stored a signup for an existing account")
	}
	if m := latestEmail(t, app, "taken@example.com"); !strings.Contains(m.Subject, "already have") {
		t.Fatalf("hint subject: %q", m.Subject)
	}
}

// The account-exists hint is an email too: repeated guest forms for a
// real account's address, from many IPs, send at most 3 an hour.
func TestGuestExistingEmailHintIsRateLimited(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	newTestUser(t, app, "taken", "password1234", models.RoleMember)
	gid := createGroupAs(t, admin, "Garden Club", models.VisibilityPublic)
	ev := scheduleGroupEvent(t, admin, gid, "Picnic")
	for i := 1; i <= 6; i++ {
		c := anonClient(t, app, fmt.Sprintf("10.4.5.%d", i))
		guestSubmit(t, c, ev.ID, guestBody("Taken", "taken@example.com", true), http.StatusOK)
	}
	if n := guestEmailCount(t, app, "taken@example.com"); n != guestVerifyPerHour {
		t.Fatalf("hint emails: got %d, want %d", n, guestVerifyPerHour)
	}
}

// M1.4 acceptance: verification emails to one address stop after 3 in
// an hour or 10 in a day, whatever the IP.
func TestGuestVerificationRateLimitPerAddress(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	gid := createGroupAs(t, admin, "Garden Club", models.VisibilityPublic)
	ev := scheduleGroupEvent(t, admin, gid, "Picnic")

	for i := 1; i <= 3; i++ {
		c := anonClient(t, app, fmt.Sprintf("10.4.3.%d", i))
		guestSubmit(t, c, ev.ID, guestBody("Gail", "gail@example.com", true), http.StatusOK)
	}
	// The fourth attempt, from a fresh IP, answers the same way but
	// sends nothing.
	c := anonClient(t, app, "10.4.3.99")
	guestSubmit(t, c, ev.ID, guestBody("Gail", "gail@example.com", true), http.StatusOK)
	if guestSignupCount(t, app, "gail@example.com") != 3 || guestEmailCount(t, app, "gail@example.com") != 3 {
		t.Fatalf("after hour limit: signups %d, emails %d",
			guestSignupCount(t, app, "gail@example.com"), guestEmailCount(t, app, "gail@example.com"))
	}

	// Ten signups earlier today (outside the hour) close the day too.
	var occ models.EventOccurrence
	if err := app.DB.Where("event_id = ?", ev.ID).First(&occ).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		s := models.GuestSignup{
			Name: "Daily", Email: "daily@example.com", OccurrenceID: occ.ID,
			TokenHash: fmt.Sprintf("seeded-%d", i),
			ExpiresAt: time.Now().UTC().Add(time.Hour),
		}
		if err := app.DB.Create(&s).Error; err != nil {
			t.Fatal(err)
		}
		// default:now() stamps the row at create; move it out of the hour.
		if err := app.DB.Model(&s).Update("created_at", time.Now().UTC().Add(-2*time.Hour)).Error; err != nil {
			t.Fatal(err)
		}
	}
	c = anonClient(t, app, "10.4.3.100")
	guestSubmit(t, c, ev.ID, guestBody("Daily", "daily@example.com", true), http.StatusOK)
	if guestSignupCount(t, app, "daily@example.com") != 10 || guestEmailCount(t, app, "daily@example.com") != 0 {
		t.Fatalf("after day limit: signups %d, emails %d",
			guestSignupCount(t, app, "daily@example.com"), guestEmailCount(t, app, "daily@example.com"))
	}
}

// Guests register for public events only (section 6). An invite-only
// group's opened public event takes guests without ever naming the
// group, and tightening that event to private cancels the guest while
// signed-in registrants keep their places.
func TestGuestVisibilityAndTightening(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	mem := newTestUser(t, app, "mem", "password1234", models.RoleMember)
	memC := loginAPI(t, app, "mem", "password1234")
	gid := createGroupAs(t, admin, "Hidden Care", models.VisibilityInviteOnly)
	ev := scheduleGroupEvent(t, admin, gid, "Open House")
	anon := anonClient(t, app, "10.4.4.1")

	// While the event is invite-only, the guest door is a 404.
	guestSubmit(t, anon, ev.ID, guestBody("Gail", "gail@example.com", true), http.StatusNotFound)
	setVisibility(t, admin, ev.ID, models.VisibilityPublic, "", http.StatusOK)

	display, _ := guestConfirm(t, app, anon, ev.ID, "Gail Guest", "gail@example.com", false)
	if display.GroupName != "" || display.HostedBy == "" {
		t.Fatalf("confirm display leaks or loses the host: %+v", display)
	}
	var bodies []models.MailOutbox
	app.DB.Where("to_address = ?", "gail@example.com").Find(&bodies)
	if len(bodies) != 2 {
		t.Fatalf("guest emails: %d", len(bodies))
	}
	for _, m := range bodies {
		if strings.Contains(m.Subject+m.Body, "Hidden Care") || strings.Contains(m.Body, "Hall") {
			t.Fatalf("email named the hidden group: %q %q", m.Subject, m.Body)
		}
	}
	register(t, memC, ev.ID)
	u := guestUser(t, app, "gail@example.com")
	if regCount(app, &u, ev.ID) != 1 || regCount(app, mem, ev.ID) != 1 {
		t.Fatal("expected one guest and one member registration")
	}

	// A private event also refuses new guests, and tightening to
	// private removes the guest who can no longer see it.
	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d/visibility", ev.ID),
		map[string]any{"visibility": models.VisibilityPrivate, "confirm": true}, http.StatusOK)
	guestSubmit(t, anon, ev.ID, guestBody("Late", "late@example.com", true), http.StatusNotFound)
	if regCount(app, &u, ev.ID) != 0 || regCount(app, mem, ev.ID) != 1 {
		t.Fatalf("after tightening: guest %d, member %d", regCount(app, &u, ev.ID), regCount(app, mem, ev.ID))
	}
}

// Tightening an event must cancel waitlisted rows too: a waitlisted
// guest who loses access must not be promoted into the event later.
func TestTighteningCancelsWaitlistedGuest(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	gid := createGroupAs(t, admin, "Garden Club", models.VisibilityPublic)
	ev := scheduleGroupEvent(t, admin, gid, "Picnic")
	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d", ev.ID),
		map[string]any{"capacity": 1, "waitlistEnabled": true}, http.StatusOK)

	// A group member takes the one seat.
	mem := newTestUser(t, app, "mem", "password1234", models.RoleMember)
	addMemberEmail(t, admin, gid, mem.Email)
	memC := loginAPI(t, app, "mem", "password1234")
	register(t, memC, ev.ID)

	// A guest lands on the waitlist.
	anon := anonClient(t, app, "10.4.4.9")
	reg, _ := guestConfirm(t, app, anon, ev.ID, "Willa Guest", "willa@example.com", false)
	if reg.Status != models.RegWaitlisted {
		t.Fatalf("guest status = %q, want waitlisted", reg.Status)
	}
	u := guestUser(t, app, "willa@example.com")
	if regCount(app, &u, ev.ID) != 1 {
		t.Fatal("expected one waitlisted guest registration")
	}

	// Tightening to invite-only cancels the waitlisted guest row and
	// tells them; the member's seat is untouched.
	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d/visibility", ev.ID),
		map[string]any{"visibility": models.VisibilityInviteOnly, "confirm": true}, http.StatusOK)
	if regCount(app, &u, ev.ID) != 0 || regCount(app, mem, ev.ID) != 1 {
		t.Fatalf("after tightening: guest %d, member %d", regCount(app, &u, ev.ID), regCount(app, mem, ev.ID))
	}
	var n int64
	app.DB.Model(&models.Notification{}).Where("user_id = ? AND kind = ?", u.ID, notify.RegistrationCancelled).Count(&n)
	if n != 1 {
		t.Fatalf("guest cancellation notices: %d, want 1", n)
	}
}

// A promoted guest must never get the group's name, place, or link:
// the guest is a nil viewer in the notice builders, like guestDisplay.
func TestPromotedGuestEmailHidesPrivateGroup(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	gid := createGroupAs(t, admin, "Hidden Care", models.VisibilityPrivate)
	if err := app.DB.Model(&models.Group{}).Where("id = ?", gid).Update("location", "12 Elm St").Error; err != nil {
		t.Fatal(err)
	}
	ev := scheduleGroupEvent(t, admin, gid, "Open House")
	// Empty event location: with the bug, the group fallback place leaks.
	if err := app.DB.Model(&models.Event{}).Where("id = ?", ev.ID).Update("location", "").Error; err != nil {
		t.Fatal(err)
	}
	// Open the event so guests can register, then cap it at one seat.
	setVisibility(t, admin, ev.ID, models.VisibilityPublic, "", http.StatusOK)
	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d", ev.ID),
		map[string]any{"capacity": 1, "waitlistEnabled": true}, http.StatusOK)

	mem := newTestUser(t, app, "mem", "password1234", models.RoleMember)
	addMemberEmail(t, admin, gid, mem.Email)
	memC := loginAPI(t, app, "mem", "password1234")
	register(t, memC, ev.ID)

	anon := anonClient(t, app, "10.4.4.10")
	reg, _ := guestConfirm(t, app, anon, ev.ID, "Gail Guest", "gail@example.com", false)
	if reg.Status != models.RegWaitlisted {
		t.Fatalf("guest status = %q, want waitlisted", reg.Status)
	}

	// The member declines; the guest is promoted and emailed.
	var occ models.EventOccurrence
	app.DB.Where("event_id = ?", ev.ID).First(&occ)
	if code, _ := registerAs(memC, ev.ID, occ.OriginalDate, "declined", nil); code != http.StatusOK {
		t.Fatalf("member decline: %d", code)
	}
	u := guestUser(t, app, "gail@example.com")
	if got := regStatus(t, app, u.ID, occ.ID); got != models.RegGoing {
		t.Fatalf("guest status after promotion = %q, want going", got)
	}
	m := latestEmail(t, app, "gail@example.com")
	if strings.Contains(m.Subject, "Hidden Care") || strings.Contains(m.Body, "Hidden Care") ||
		strings.Contains(m.Body, "12 Elm St") || strings.Contains(m.Body, "/groups/") {
		t.Fatalf("promotion email leaked the group: %q %q", m.Subject, m.Body)
	}
	var n models.Notification
	if err := app.DB.Where("user_id = ? AND kind = ?", u.ID, notify.RegistrationPromoted).Order("id DESC").First(&n).Error; err != nil {
		t.Fatalf("promotion notice: %v", err)
	}
	if n.Link != fmt.Sprintf("/events/%d", ev.ID) {
		t.Fatalf("promotion link = %q, want /events/%d", n.Link, ev.ID)
	}
}

// A pruned guest can't re-enter through the public form; the leader add
// action is the way back in.
func TestGuestRemoveThenReconfirm(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	gid := createGroupAs(t, admin, "Garden Club", models.VisibilityPublic)
	ev := scheduleGroupEvent(t, admin, gid, "Picnic")

	anon := anonClient(t, app, "10.4.4.11")
	reg, _ := guestConfirm(t, app, anon, ev.ID, "Rita Guest", "rita@example.com", false)
	if reg.Status != models.RegGoing {
		t.Fatalf("guest status = %q, want going", reg.Status)
	}
	u := guestUser(t, app, "rita@example.com")

	// A leader prunes the guest.
	apiDo(t, admin, "POST", fmt.Sprintf("/api/v1/events/%d/registrations/remove", ev.ID),
		map[string]any{"registrationId": registrationIDFor(t, app, u.ID, ev.ID, guestDate), "date": guestDate, "note": "no room"}, http.StatusOK)
	if got := regStatus(t, app, u.ID, reg.OccurrenceID); got != models.RegRemoved {
		t.Fatalf("after remove: %q, want removed", got)
	}

	// Re-submitting the public form and confirming is refused.
	guestSubmit(t, anon, ev.ID, guestBody("Rita Guest", "rita@example.com", true), http.StatusOK)
	m := latestEmail(t, app, "rita@example.com")
	token := tokenInEmail(t, m)
	w := anon.do("POST", "/api/v1/guest-registrations/confirm", map[string]string{"token": token}, true)
	if w.Code != http.StatusConflict {
		t.Fatalf("reconfirm after removal: %d %s", w.Code, w.Body.String())
	}
	if got := regStatus(t, app, u.ID, reg.OccurrenceID); got != models.RegRemoved {
		t.Fatalf("after reconfirm: %q, want removed", got)
	}

	// The leader add action seats them again.
	apiDo(t, admin, "POST", fmt.Sprintf("/api/v1/events/%d/registrations/add", ev.ID),
		map[string]any{"userId": u.ID, "date": guestDate}, http.StatusOK)
	if got := regStatus(t, app, u.ID, reg.OccurrenceID); got != models.RegGoing {
		t.Fatalf("after leader add: %q, want going", got)
	}
}

// M1.4 acceptance: a guest who later gets an account keeps their
// registrations (same email, citext), through both the guest_upgrade
// door and a plain self-signup.
func TestGuestUpgradeKeepsRegistrations(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	gid := createGroupAs(t, admin, "Garden Club", models.VisibilityPublic)
	ev := scheduleGroupEvent(t, admin, gid, "Picnic")
	anon := anonClient(t, app, "10.4.5.1")

	guestConfirm(t, app, anon, ev.ID, "Gail Guest", "gail@example.com", true)
	before := guestUser(t, app, "gail@example.com")
	req := pendingRequest(t, app, "gail@example.com")
	if req.Source != models.RequestGuestUpgrade {
		t.Fatalf("request source: %+v", req)
	}
	w := admin.do("POST", "/api/v1/admin/account-requests/"+itoa(req.ID)+"/approve", map[string]any{}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("approve upgrade: %d %s", w.Code, w.Body.String())
	}
	after := guestUser(t, app, "GAIL@example.com")
	if after.ID != before.ID || after.Status != models.UserInvited {
		t.Fatalf("upgraded user: %+v (was %d)", after, before.ID)
	}
	if regCount(app, &after, ev.ID) != 1 {
		t.Fatal("upgrade lost the guest registration")
	}
	token := tokenInEmail(t, latestEmail(t, app, "gail@example.com"))
	fresh := anonClient(t, app, "10.4.5.2")
	w = fresh.do("POST", "/api/v1/account-requests/confirm",
		map[string]string{"token": token, "password": "longenoughpassword"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("set password: %d %s", w.Code, w.Body.String())
	}
	if w = fresh.do("GET", "/api/v1/auth/me", nil, false); w.Code != http.StatusOK {
		t.Fatalf("me after upgrade: %d", w.Code)
	}

	// A second guest who later self-signs up is stored as a request,
	// not answered as though the account already existed.
	guestConfirm(t, app, anon, ev.ID, "Hank Guest", "hank@example.com", false)
	w = anon.do("POST", "/api/v1/account-requests",
		signupBody("Hank Guest", "hank@example.com", "adult", ""), true)
	if w.Code != http.StatusOK {
		t.Fatalf("guest self-signup: %d %s", w.Code, w.Body.String())
	}
	if r := pendingRequest(t, app, "hank@example.com"); r.Source != models.RequestSelfSignup {
		t.Fatalf("self-signup request: %+v", r)
	}
}

// The invitation door upgrades a guest row the same way (C5), and a
// guest cannot be added to a group before that upgrade happens.
func TestGuestInvitationUpgradeAndNoMembership(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	gid := createGroupAs(t, admin, "Garden Club", models.VisibilityPublic)
	ev := scheduleGroupEvent(t, admin, gid, "Picnic")
	anon := anonClient(t, app, "10.4.6.1")

	guestConfirm(t, app, anon, ev.ID, "Gail Guest", "gail@example.com", false)
	before := guestUser(t, app, "gail@example.com")

	w := admin.do("POST", "/api/v1/groups/"+itoa(gid)+"/members",
		map[string]any{"email": "gail@example.com"}, true)
	if w.Code != http.StatusForbidden {
		t.Fatalf("adding a guest as a member: %d %s", w.Code, w.Body.String())
	}

	w = admin.do("POST", "/api/v1/invitations",
		map[string]any{"email": "gail@example.com", "name": "Gail Guest"}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("invite a guest: %d %s", w.Code, w.Body.String())
	}
	token := tokenInEmail(t, latestEmail(t, app, "gail@example.com"))
	fresh := anonClient(t, app, "10.4.6.2")
	w = fresh.do("POST", "/api/v1/invitations/accept",
		map[string]string{"ageBand": "adult", "token": token, "name": "Gail Guest", "password": "longenoughpassword"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("accept invitation: %d %s", w.Code, w.Body.String())
	}
	after := guestUser(t, app, "gail@example.com")
	if after.ID != before.ID || after.Status != models.UserActive || !after.CanSignIn() {
		t.Fatalf("invited guest: %+v (was %d)", after, before.ID)
	}
	if regCount(app, &after, ev.ID) != 1 {
		t.Fatal("invitation upgrade lost the guest registration")
	}
}

// A guest redeem on a full event waitlists when the waitlist is on, with
// the position in the response and the confirmation email.
func TestGuestRedeemOnFullEventWaitlists(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	gid := createGroupAs(t, admin, "Garden Club", models.VisibilityPublic)
	ev := scheduleGroupEvent(t, admin, gid, "Picnic")
	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d", ev.ID),
		map[string]any{"capacity": 1, "waitlistEnabled": true}, http.StatusOK)

	// A member takes the one seat.
	member := newTestUser(t, app, "member", "password1234", models.RoleMember)
	var g models.Group
	app.DB.First(&g, gid)
	addMember(t, app, member, &g, models.MembershipMember)
	mc := loginAPI(t, app, "member", "password1234")
	var occ models.EventOccurrence
	app.DB.Where("event_id = ?", ev.ID).First(&occ)
	if code, _ := registerAs(mc, ev.ID, occ.OriginalDate, "going", nil); code != http.StatusOK {
		t.Fatalf("member going: %d", code)
	}

	anon := anonClient(t, app, "10.4.0.1")
	guestSubmit(t, anon, ev.ID, guestBody("Gail Guest", "gail@example.com", true), http.StatusOK)
	token := tokenInEmail(t, latestEmail(t, app, "gail@example.com"))
	w := anon.do("POST", "/api/v1/guest-registrations/confirm", map[string]string{"token": token}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("confirm: %d %s", w.Code, w.Body.String())
	}
	var res struct {
		Registration guestRegistrationJSON `json:"registration"`
		ManageToken  string                `json:"manageToken"`
	}
	decodeJSON(t, w, &res)
	if res.Registration.Status != models.RegWaitlisted {
		t.Fatalf("guest status = %q, want waitlisted", res.Registration.Status)
	}
	if res.Registration.WaitlistPosition == nil || *res.Registration.WaitlistPosition != 1 {
		t.Fatalf("waitlist position = %v, want 1", res.Registration.WaitlistPosition)
	}
	// The manage page shows the position too.
	w = anon.do("GET", "/api/v1/guest-registrations/manage/"+res.ManageToken, nil, false)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"waitlistPosition":1`) {
		t.Fatalf("manage page: %d %s", w.Code, w.Body.String())
	}
	// The confirmation email names the waitlist and the position.
	m := latestEmail(t, app, "gail@example.com")
	if !strings.Contains(m.Subject, "waitlist") || !strings.Contains(m.Body, "number 1 in line") {
		t.Fatalf("waitlist confirmation email: %q %q", m.Subject, m.Body)
	}
}

// A guest redeem on a full event with the waitlist off creates no user
// and no registration, and the link dies.
func TestGuestRedeemOnFullEventWithoutWaitlistCreatesNothing(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	gid := createGroupAs(t, admin, "Garden Club", models.VisibilityPublic)
	ev := scheduleGroupEvent(t, admin, gid, "Picnic")
	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d", ev.ID),
		map[string]any{"capacity": 1, "waitlistEnabled": false}, http.StatusOK)

	member := newTestUser(t, app, "member", "password1234", models.RoleMember)
	var g models.Group
	app.DB.First(&g, gid)
	addMember(t, app, member, &g, models.MembershipMember)
	mc := loginAPI(t, app, "member", "password1234")
	var occ models.EventOccurrence
	app.DB.Where("event_id = ?", ev.ID).First(&occ)
	if code, _ := registerAs(mc, ev.ID, occ.OriginalDate, "going", nil); code != http.StatusOK {
		t.Fatalf("member going: %d", code)
	}

	anon := anonClient(t, app, "10.4.0.2")
	guestSubmit(t, anon, ev.ID, guestBody("Gus Guest", "gus@example.com", true), http.StatusOK)
	token := tokenInEmail(t, latestEmail(t, app, "gus@example.com"))
	w := anon.do("POST", "/api/v1/guest-registrations/confirm", map[string]string{"token": token}, true)
	if w.Code != http.StatusConflict {
		t.Fatalf("confirm on full no-waitlist event: %d %s, want 409", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"full":true`) {
		t.Fatalf("409 body: %s", w.Body.String())
	}
	var users int64
	app.DB.Model(&models.User{}).Where("email = ?", "gus@example.com").Count(&users)
	var regs int64
	app.DB.Model(&models.Registration{}).Where("occurrence_id = ?", occ.ID).Count(&regs)
	if users != 0 || regs != 1 {
		t.Fatalf("after 409: %d users, %d registrations (want 0 and 1)", users, regs)
	}
	// The link is dead: a second redeem fails.
	w = anon.do("POST", "/api/v1/guest-registrations/confirm", map[string]string{"token": token}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("reuse of dead link: %d, want 400", w.Code)
	}
}

// A promoted guest gets the promotion email at the guest address in the
// devoutbox, with a working manage link.
func TestPromotedGuestGetsEmail(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	gid := createGroupAs(t, admin, "Garden Club", models.VisibilityPublic)
	ev := scheduleGroupEvent(t, admin, gid, "Picnic")
	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d", ev.ID),
		map[string]any{"capacity": 1, "waitlistEnabled": true}, http.StatusOK)

	member := newTestUser(t, app, "member", "password1234", models.RoleMember)
	var g models.Group
	app.DB.First(&g, gid)
	addMember(t, app, member, &g, models.MembershipMember)
	mc := loginAPI(t, app, "member", "password1234")
	var occ models.EventOccurrence
	app.DB.Where("event_id = ?", ev.ID).First(&occ)
	if code, _ := registerAs(mc, ev.ID, occ.OriginalDate, "going", nil); code != http.StatusOK {
		t.Fatalf("member going: %d", code)
	}
	anon := anonClient(t, app, "10.4.0.3")
	guestSubmit(t, anon, ev.ID, guestBody("Gail Guest", "gail@example.com", true), http.StatusOK)
	token := tokenInEmail(t, latestEmail(t, app, "gail@example.com"))
	w := anon.do("POST", "/api/v1/guest-registrations/confirm", map[string]string{"token": token}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("guest confirm: %d %s", w.Code, w.Body.String())
	}

	// The member cancels; the guest is promoted and emailed.
	if code, _ := registerAs(mc, ev.ID, occ.OriginalDate, "declined", nil); code != http.StatusOK {
		t.Fatalf("member cancel: %d", code)
	}
	m := latestEmail(t, app, "gail@example.com")
	if !strings.Contains(m.Subject, "You're in") {
		t.Fatalf("promotion email subject: %q", m.Subject)
	}
	manageToken := manageTokenInEmail(t, m)
	// Fetching the manage page changes nothing; the token still works.
	w = anon.do("GET", "/api/v1/guest-registrations/manage/"+manageToken, nil, false)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"going"`) {
		t.Fatalf("manage page after promotion: %d %s", w.Code, w.Body.String())
	}
	// Confirming the cancel sets cancelled and the old token then fails.
	apiDo(t, anon, "POST", "/api/v1/guest-registrations/manage/"+manageToken+"/cancel", nil, http.StatusOK)
	gu := guestUser(t, app, "gail@example.com")
	if r := guestRegistrationRow(t, app, gu.ID); r.Status != models.RegCancelled {
		t.Fatalf("after manage cancel: %q", r.Status)
	}
}

// A guest's emails use the guest form browser's zone (DEC-58), and the
// guest's account keeps it for later notices. A form with no zone falls
// back to the church's (UTC here). The event is 7 PM UTC on Dec 1.
func TestGuestEmailsUseTheFormZone(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	gid := createGroupAs(t, admin, "Zone Club", models.VisibilityPublic)
	ev := scheduleGroupEvent(t, admin, gid, "Supper")
	anon := anonClient(t, app, "10.4.0.9")

	body := guestBody("Bo Guest", "bo@example.com", true)
	body["timezone"] = "America/Boise"
	guestSubmit(t, anon, ev.ID, body, http.StatusOK)
	verify := latestEmail(t, app, "bo@example.com")
	if !strings.Contains(verify.Body, "Tue, Dec 1 · 12:00 PM MST") {
		t.Fatalf("verification email: %q", verify.Body)
	}
	w := anon.do("POST", "/api/v1/guest-registrations/confirm",
		map[string]string{"token": tokenInEmail(t, verify)}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("confirm: %d %s", w.Code, w.Body.String())
	}
	if m := latestEmail(t, app, "bo@example.com"); !strings.Contains(m.Body, "Tue, Dec 1 · 12:00 PM MST") {
		t.Fatalf("confirmation email: %q", m.Body)
	}
	if u := guestUser(t, app, "bo@example.com"); u.Timezone != "America/Boise" {
		t.Fatalf("guest zone: %q", u.Timezone)
	}

	body = guestBody("Nell Guest", "nell@example.com", true)
	body["timezone"] = "Local"
	guestSubmit(t, anon, ev.ID, body, http.StatusOK)
	if m := latestEmail(t, app, "nell@example.com"); !strings.Contains(m.Body, "Tue, Dec 1 · 7:00 PM UTC") {
		t.Fatalf("no-zone verification email: %q", m.Body)
	}
}

func TestHouseholdManageTokenCancelsOnlyItsSubject(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "HouseholdTokens")
	eventID, date := seedCappedEvent(t, app, admin, gid, nil, true)
	_, c, spouse, child := householdFamily(t, app, admin, "tokenholder")
	result := batchAnswer(t, c, eventID, date, map[string]any{"people": []map[string]any{{"personId": spouse.ID, "status": "going"}, {"personId": child.ID, "status": "going"}}}, http.StatusOK)
	raw, hash, err := createInvitationToken()
	if err != nil {
		t.Fatal(err)
	}
	app.DB.Model(&models.Registration{}).Where("id = ?", result.People[1].RegistrationID).Update("manage_token_hash", hash)
	anon := anonClient(t, app, "10.9.0.1")
	body := apiDo(t, anon, "GET", "/api/v1/guest-registrations/manage/"+raw, nil, http.StatusOK)
	for _, secret := range []string{"Kit", "householdPerson", "Private allergy note", "personId"} {
		if strings.Contains(body, secret) {
			t.Fatalf("public identity leak %s", body)
		}
	}
	apiDo(t, anon, "POST", "/api/v1/guest-registrations/manage/"+raw+"/cancel", map[string]any{}, http.StatusOK)
	var rows []models.Registration
	app.DB.Order("id").Find(&rows)
	if len(rows) != 2 || rows[0].Status != models.RegGoing || rows[1].Status != models.RegCancelled || rows[1].ManageTokenHash != "" {
		t.Fatalf("token changed other answer %+v", rows)
	}
	apiDo(t, anon, "GET", "/api/v1/guest-registrations/manage/"+raw, nil, http.StatusNotFound)
}

// A real waitlist promotion gives an active, hidden member the same bearer
// manage link as a guest. Contact authority depends on their last registration.
type promotedContactCancellationFixture struct {
	*contactAPIFixture
	eventID uint
	date    string
	occ     models.EventOccurrence
	grant   models.ContactGrant
	pending contactRequestJSON
	token   string
	anon    *apiClient
}

func newPromotedContactCancellationFixture(t *testing.T) *promotedContactCancellationFixture {
	t.Helper()
	f := &promotedContactCancellationFixture{contactAPIFixture: newContactAPIFixture(t, false)}
	f.eventID, f.date = seedCappedEvent(t, f.app, f.lc, itoa(f.group.ID), intPtr(1), true)
	for _, client := range []*apiClient{f.lc, f.sc} {
		if code, _ := registerAs(client, f.eventID, f.date, models.RegGoing, nil); code != http.StatusOK {
			t.Fatal("could not fill seat and waitlist")
		}
	}
	if code, _ := registerAs(f.lc, f.eventID, f.date, models.RegDeclined, nil); code != http.StatusOK {
		t.Fatal("could not release seat for promotion")
	}
	if err := f.app.DB.Where("event_id = ?", f.eventID).First(&f.occ).Error; err != nil {
		t.Fatal(err)
	}
	if regStatus(t, f.app, f.subject.ID, f.occ.ID) != models.RegGoing {
		t.Fatal("subject was not promoted")
	}
	f.token = manageTokenInEmail(t, latestEmail(t, f.app, f.subject.Email))
	f.anon = anonClient(t, f.app, "10.4.0.19")
	create := func(fields string) contactRequestJSON {
		w := f.lc.do("POST", "/api/v1/contact-requests", map[string]any{"subjectUserId": f.subject.ID, "contextType": "event", "contextId": f.eventID, "fields": fields}, true)
		if w.Code != http.StatusCreated {
			t.Fatalf("event contact request: %d", w.Code)
		}
		var body struct {
			Request contactRequestJSON `json:"request"`
		}
		decodeJSON(t, w, &body)
		return body.Request
	}
	f.grant = f.contactAPIFixture.grant(t, create("email"))
	f.pending = create("phone")
	// The cancellation must also retain ordinary capacity promotion and mail.
	if code, _ := registerAs(f.oc, f.eventID, f.date, models.RegGoing, nil); code != http.StatusOK {
		t.Fatal("could not queue next waiter")
	}
	return f
}

func (f *promotedContactCancellationFixture) cancel() *httptest.ResponseRecorder {
	return f.anon.do("POST", "/api/v1/guest-registrations/manage/"+f.token+"/cancel", nil, true)
}

func (f *promotedContactCancellationFixture) assertContactsEnded(t *testing.T) {
	t.Helper()
	var pending models.ContactRequest
	var grant models.ContactGrant
	if err := f.app.DB.First(&pending, f.pending.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.app.DB.First(&grant, f.grant.ID).Error; err != nil {
		t.Fatal(err)
	}
	if pending.Status != policy.ContactEnded || pending.EndReason != policy.ContactLeftContext || pending.CompletedAt == nil || grant.EndedAt == nil || grant.EndReason != policy.ContactLeftContext {
		t.Fatal("cancellation did not immediately end pending request and grant")
	}
	if contactCount(t, f.app, &models.Notification{}, "subject_type = ? AND subject_id = ? AND waiting AND resolved_at IS NULL", "contact_request", pending.ID) != 0 {
		t.Fatal("cancellation left a pending review task")
	}
	if contactCount(t, f.app, &models.ContactRequest{}, "id = ? AND status = ?", grant.RequestID, policy.ContactGranted) != 1 {
		t.Fatal("cancellation lost successful request history")
	}
}

func TestGuestManageCancellationEndsMemberContacts(t *testing.T) {
	f := newPromotedContactCancellationFixture(t)
	reg := guestRegistrationRow(t, f.app, f.subject.ID)
	for i := 0; i < 2; i++ {
		if w := f.cancel(); w.Code != http.StatusOK {
			t.Fatalf("cancel/replay: %d", w.Code)
		}
		f.assertContactsEnded(t)
	}
	if regStatus(t, f.app, f.subject.ID, f.occ.ID) != models.RegCancelled || regStatus(t, f.app, f.outsider.ID, f.occ.ID) != models.RegGoing {
		t.Fatal("cancellation failed to free and promote exactly one seat")
	}
	if mail := latestEmail(t, f.app, f.outsider.Email); !strings.Contains(mail.Subject, "You're in") {
		t.Fatal("next waiter did not receive post-commit promotion mail")
	}
	if contactCount(t, f.app, &models.AuditLog{}, "action = ?", audit.ContactEnded) != 2 || contactCount(t, f.app, &models.AuditLog{}, "action = ?", audit.RegistrationCancelled) != 1 {
		t.Fatal("cancellation replay duplicated terminal audits")
	}
	if code, _ := registerAs(f.sc, f.eventID, f.date, models.RegGoing, nil); code != http.StatusOK {
		t.Fatal("could not RSVP again")
	}
	if restored := guestRegistrationRow(t, f.app, f.subject.ID); restored.ID != reg.ID || restored.Status == models.RegCancelled {
		t.Fatal("RSVP did not reuse the cancelled registration")
	}
	f.assertContactsEnded(t)
	if w := f.reveal(f.lc, f.grant, "email"); w.Code != http.StatusForbidden {
		t.Fatalf("re-RSVP resurrected old grant: %d", w.Code)
	}
}

func TestGuestManageCancellationWinsContactReveal(t *testing.T) {
	f := newPromotedContactCancellationFixture(t)
	gate := holdContactGate(t, f.app)
	cancel := queueContact(f.cancel)
	waitContactWaiters(t, f.app, 1)
	reveal := queueContact(func() *httptest.ResponseRecorder { return f.reveal(f.lc, f.grant, "email") })
	waitContactWaiters(t, f.app, 2)
	if regStatus(t, f.app, f.subject.ID, f.occ.ID) != models.RegGoing {
		t.Fatal("cancellation mutated registration before acquiring contact gate")
	}
	gate.Rollback()
	if w := awaitContactResponse(t, cancel); w.Code != http.StatusOK {
		t.Fatalf("cancel: %d", w.Code)
	}
	if w := awaitContactResponse(t, reveal); w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), f.subject.Email) {
		t.Fatalf("reveal after cancellation: %d", w.Code)
	}
	f.assertContactsEnded(t)
	if contactCount(t, f.app, &models.AuditLog{}, "action = ?", audit.ContactReveal) != 0 {
		t.Fatal("cancelled contact authority revealed")
	}
}

func TestGuestManageCancellationContactAuditFailureRollsBack(t *testing.T) {
	for _, failPending := range []bool{false, true} {
		t.Run(fmt.Sprintf("pending=%t", failPending), func(t *testing.T) {
			f := newPromotedContactCancellationFixture(t)
			target := f.grant.RequestID
			if failPending {
				target = f.pending.ID
			}
			name := "guest:test_contact_ending_audit_failure"
			if err := f.app.DB.Callback().Create().Before("gorm:create").Register(name, func(db *gorm.DB) {
				if row, ok := db.Statement.Dest.(*models.AuditLog); ok && row.Action == audit.ContactEnded && row.TargetID == target {
					db.AddError(errors.New("injected contact ending audit failure"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			defer f.app.DB.Callback().Create().Remove(name)
			mails := contactCount(t, f.app, &models.MailOutbox{}, "1 = 1")
			if w := f.cancel(); w.Code != http.StatusNotFound {
				t.Fatalf("failed cancellation: %d", w.Code)
			}
			if regStatus(t, f.app, f.subject.ID, f.occ.ID) != models.RegGoing || regStatus(t, f.app, f.outsider.ID, f.occ.ID) != models.RegWaitlisted {
				t.Fatal("failed contact audit committed cancellation or capacity promotion")
			}
			if contactCount(t, f.app, &models.ContactRequest{}, "id = ? AND status = ?", f.pending.ID, policy.ContactPending) != 1 || contactCount(t, f.app, &models.ContactGrant{}, "id = ? AND ended_at IS NULL", f.grant.ID) != 1 {
				t.Fatal("failed contact audit committed request/grant ending")
			}
			if contactCount(t, f.app, &models.Notification{}, "subject_id = ? AND kind = ? AND resolved_at IS NULL", f.pending.ID, notify.ContactSubjectReview) != 1 {
				t.Fatal("failed contact audit resolved pending review")
			}
			if contactCount(t, f.app, &models.AuditLog{}, "action IN ?", []string{audit.ContactEnded, audit.RegistrationCancelled}) != 0 || contactCount(t, f.app, &models.MailOutbox{}, "1 = 1") != mails {
				t.Fatal("failed cancellation left terminal audits or delivered mail")
			}
		})
	}
}

func TestGuestManageCancellationRechecksTokenAndScopeUnderGate(t *testing.T) {
	for _, change := range []string{"token-rotation", "event-mismatch"} {
		t.Run(change, func(t *testing.T) {
			f := newPromotedContactCancellationFixture(t)
			gate := holdContactGate(t, f.app)
			cancel := queueContact(f.cancel)
			waitContactWaiters(t, f.app, 1) // preflight finished; authoritative lookup still waiting
			if change == "token-rotation" {
				if err := gate.Model(&models.Registration{}).Where("user_id = ? AND occurrence_id = ?", f.subject.ID, f.occ.ID).Update("manage_token_hash", hashToken("rotated-test-token")).Error; err != nil {
					t.Fatal(err)
				}
			} else {
				other, _ := seedEvent(t, f.app, f.group, f.leader.ID, f.occ.StartsAt.Add(24*time.Hour))
				if err := gate.Model(&f.occ).Update("event_id", other.ID).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := gate.Commit().Error; err != nil {
				t.Fatal(err)
			}
			if w := awaitContactResponse(t, cancel); w.Code != http.StatusNotFound {
				t.Fatalf("stale preflight authorized cancellation: %d", w.Code)
			}
			if regStatus(t, f.app, f.subject.ID, f.occ.ID) != models.RegGoing {
				t.Fatal("stale preflight mutated a registration")
			}
		})
	}
}
