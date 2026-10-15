package main

import (
	"fmt"
	"fold/internal/audit"
	"fold/models"
	"net/http"
	"strings"
	"testing"
	"time"
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
		map[string]string{"token": token, "name": "Gail Guest", "password": "longenoughpassword"}, true)
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
