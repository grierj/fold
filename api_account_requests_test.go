package main

import (
	"fold/internal/audit"
	"fold/models"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAccountRequestsDeferAllMailForExistingAndNewAddresses(t *testing.T) {
	app := testApp(t)
	admin := newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	member := newTestUser(t, app, "member", "pw", models.RoleMember)
	var jobs []func()
	app.background = func(f func()) { jobs = append(jobs, f) }
	anon := anonClient(t, app, "10.1.0.1")
	for _, email := range []string{member.Email, "new@example.com"} {
		w := anon.do("POST", "/api/v1/account-requests", signupBody("Person", email, "adult", ""), true)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), signupThanks) {
			t.Fatalf("%s: %d %s", email, w.Code, w.Body.String())
		}
		if n := mailsTo(app, email); n != 0 {
			t.Fatalf("response waited on mail for %s: %d", email, n)
		}
	}
	if len(jobs) != 2 || mailsTo(app, admin.Email) != 0 {
		t.Fatalf("want two deferred batches and no admin mail, jobs=%d", len(jobs))
	}
	for _, job := range jobs {
		job()
	}
	for _, email := range []string{member.Email, "new@example.com", admin.Email} {
		if n := mailsTo(app, email); n != 1 {
			t.Fatalf("background mail for %s: %d", email, n)
		}
	}
}

// M1.3 acceptance: self-signup creates a pending request; nobody can
// sign in until an admin approves; approval emails a set-password link
// and confirming it signs the new adult in.
func TestSelfSignupApprovalFlow(t *testing.T) {
	app := testApp(t)
	admin := newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	anon := anonClient(t, app, "10.1.0.1")

	w := anon.do("POST", "/api/v1/account-requests",
		signupBody("New Person", "new@example.com", "adult", ""), true)
	if w.Code != http.StatusOK {
		t.Fatalf("signup: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Message string `json:"message"`
	}
	decodeJSON(t, w, &body)
	if body.Message != signupThanks {
		t.Fatalf("message: %q", body.Message)
	}
	req := pendingRequest(t, app, "new@example.com")
	if req.Source != models.RequestSelfSignup || req.AccountType != models.AccountAdult {
		t.Fatalf("request: %+v", req)
	}
	// No account yet, so no sign-in is possible.
	w = anon.do("POST", "/api/v1/auth/login",
		map[string]string{"email": "new@example.com", "password": "whatever123"}, true)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("login before approval: %d", w.Code)
	}
	// The requester and the admins were both told (bell + ack email).
	if m := latestEmail(t, app, "new@example.com"); !strings.Contains(m.Subject, "got your") {
		t.Fatalf("ack subject: %q", m.Subject)
	}
	adminClient := loginAPI(t, app, "admin", "pw")
	w = adminClient.do("GET", "/api/v1/admin/account-requests", nil, false)
	if w.Code != http.StatusOK {
		t.Fatalf("queue: %d %s", w.Code, w.Body.String())
	}
	var queue struct {
		Requests []accountRequestJSON `json:"requests"`
	}
	decodeJSON(t, w, &queue)
	if len(queue.Requests) != 1 || queue.Requests[0].HasWarnings {
		t.Fatalf("queue: %+v", queue.Requests)
	}
	// Approve from the queue: an invited account appears, still unusable.
	w = adminClient.do("POST", "/api/v1/admin/account-requests/"+itoa(req.ID)+"/approve",
		map[string]any{}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", w.Code, w.Body.String())
	}
	var u models.User
	if err := app.DB.Where("email = ?", "new@example.com").First(&u).Error; err != nil {
		t.Fatalf("approved user: %v", err)
	}
	if u.Status != models.UserInvited || u.CanSignIn() {
		t.Fatalf("approved user status: %q", u.Status)
	}
	if n := adminNotification(t, app, admin.ID, req.ID); n.ResolvedAt == nil {
		t.Fatal("bell item not resolved by approval")
	}
	if auditCount(t, app, audit.AccountApproved) != 1 {
		t.Fatal("approval not audited")
	}
	// The set-password link finishes the job and signs the person in.
	token := tokenInEmail(t, latestEmail(t, app, "new@example.com"))
	fresh := anonClient(t, app, "10.1.0.2")
	w = fresh.do("POST", "/api/v1/account-requests/confirm",
		map[string]string{"token": token, "password": "longenoughpassword"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("confirm: %d %s", w.Code, w.Body.String())
	}
	w = fresh.do("GET", "/api/v1/auth/me", nil, false)
	if w.Code != http.StatusOK {
		t.Fatalf("me after confirm: %d", w.Code)
	}
	// The token is single use.
	w = fresh.do("POST", "/api/v1/account-requests/confirm",
		map[string]string{"token": token, "password": "longenoughpassword"}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("token reuse: %d", w.Code)
	}
}

// M1.3 acceptance: an email that already has an account gets the same
// on-screen answer, no stored request, and a sign-in hint by email.
func TestSelfSignupExistingEmailIsUniform(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "taken", "pw", models.RoleMember)
	anon := anonClient(t, app, "10.2.0.1")

	w := anon.do("POST", "/api/v1/account-requests",
		signupBody("Fresh Face", "fresh@example.com", "adult", ""), true)
	var freshBody struct {
		Message string `json:"message"`
	}
	decodeJSON(t, w, &freshBody)

	w = anon.do("POST", "/api/v1/account-requests",
		signupBody("Taken Name", "TAKEN@example.com", "adult", ""), true)
	if w.Code != http.StatusOK {
		t.Fatalf("existing email: %d %s", w.Code, w.Body.String())
	}
	var takenBody struct {
		Message string `json:"message"`
	}
	decodeJSON(t, w, &takenBody)
	if takenBody.Message != freshBody.Message {
		t.Fatalf("answers differ: %q vs %q", freshBody.Message, takenBody.Message)
	}
	if n := requestCount(t, app, "taken@example.com"); n != 0 {
		t.Fatalf("stored %d requests for an existing account", n)
	}
	if m := latestEmail(t, app, "taken@example.com"); !strings.Contains(m.Subject, "already have") {
		t.Fatalf("hint subject: %q", m.Subject)
	}
}

// M1.3 acceptance: choosing under 13 stores nothing.
func TestUnder13StoresNothing(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	anon := anonClient(t, app, "10.3.0.1")
	w := anon.do("POST", "/api/v1/account-requests",
		signupBody("Kid", "kid@example.com", "under13", ""), true)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "household") {
		t.Fatalf("under 13: %d %s", w.Code, w.Body.String())
	}
	if n := requestCount(t, app, "kid@example.com"); n != 0 {
		t.Fatalf("stored %d rows for an under-13", n)
	}
	var notifications int64
	app.DB.Model(&models.Notification{}).Count(&notifications)
	if notifications != 0 {
		t.Fatalf("under-13 notified admins: %d", notifications)
	}
	// A teen band with a mismatched birth date is a plain 400 too.
	w = anon.do("POST", "/api/v1/account-requests",
		signupBody("Lying Teen", "teen@example.com", "teen", "2000-01-01"), true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("mismatched teen date: %d", w.Code)
	}
}

// M1.3 acceptance: teens are routed to pending_consent (finished in
// M1.10) and can never be approved inline from the bell (DEC-44).
func TestTeenSignupPendingConsent(t *testing.T) {
	app := testApp(t)
	admin := newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	anon := anonClient(t, app, "10.4.0.1")
	w := anon.do("POST", "/api/v1/account-requests",
		signupBody("Teen Tess", "tess@example.com", "teen", teenBirthDate()), true)
	if w.Code != http.StatusOK {
		t.Fatalf("teen signup: %d %s", w.Code, w.Body.String())
	}
	req := pendingRequest(t, app, "tess@example.com")
	if req.AccountType != models.AccountTeen || req.BirthDate == nil {
		t.Fatalf("teen request: %+v", req)
	}
	note := adminNotification(t, app, admin.ID, req.ID)
	if note.Actions != "[]" {
		t.Fatalf("teen bell item carries actions: %s", note.Actions)
	}
	adminClient := loginAPI(t, app, "admin", "pw")
	w = adminClient.do("POST", "/api/v1/admin/account-requests/"+itoa(req.ID)+"/approve",
		map[string]any{"fromNotification": note.ID}, true)
	if w.Code != http.StatusForbidden {
		t.Fatalf("inline teen approval: %d %s", w.Code, w.Body.String())
	}
	// Queue approval creates the pending_consent account.
	w = adminClient.do("POST", "/api/v1/admin/account-requests/"+itoa(req.ID)+"/approve",
		map[string]any{}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("queue teen approval: %d %s", w.Code, w.Body.String())
	}
	var u models.User
	if err := app.DB.Where("email = ?", "tess@example.com").First(&u).Error; err != nil {
		t.Fatalf("teen user: %v", err)
	}
	if u.Status != models.UserPendingConsent || u.AccountType != models.AccountTeen {
		t.Fatalf("teen user: status %q type %q", u.Status, u.AccountType)
	}
	// Setting a password does not sign the teen in.
	token := tokenInEmail(t, latestEmail(t, app, "tess@example.com"))
	fresh := anonClient(t, app, "10.4.0.2")
	w = fresh.do("POST", "/api/v1/account-requests/confirm",
		map[string]string{"token": token, "password": "longenoughpassword"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("teen confirm: %d %s", w.Code, w.Body.String())
	}
	var conf struct {
		PendingConsent bool `json:"pendingConsent"`
	}
	decodeJSON(t, w, &conf)
	if !conf.PendingConsent {
		t.Fatal("teen confirm did not report pendingConsent")
	}
	w = fresh.do("GET", "/api/v1/auth/me", nil, false)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("teen password setup created a login: %d", w.Code)
	}
	if w := fresh.do("GET", "/api/v1/groups", nil, false); w.Code != http.StatusOK {
		t.Fatalf("pending teen could not browse publicly: %d", w.Code)
	}
}

// M1.3 acceptance: a member's invite-a-friend creates a pending
// request, and the friend hears nothing until an admin approves.
func TestMemberInviteFriendWaitsForApproval(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	newTestUser(t, app, "sam", "pw", models.RoleMember)
	sam := loginAPI(t, app, "sam", "pw")
	w := sam.do("POST", "/api/v1/account-requests/invite-friend",
		signupBody("Friend Fran", "fran@example.com", "adult", ""), true)
	if w.Code != http.StatusOK {
		t.Fatalf("invite friend: %d %s", w.Code, w.Body.String())
	}
	req := pendingRequest(t, app, "fran@example.com")
	if req.Source != models.RequestMemberInvite || req.RequestedBy == nil {
		t.Fatalf("friend request: %+v", req)
	}
	var mailCount int64
	app.DB.Model(&models.MailOutbox{}).Where("to_address = ?", "fran@example.com").Count(&mailCount)
	if mailCount != 0 {
		t.Fatalf("friend emailed before approval: %d", mailCount)
	}
	adminClient := loginAPI(t, app, "admin", "pw")
	w = adminClient.do("POST", "/api/v1/admin/account-requests/"+itoa(req.ID)+"/approve",
		map[string]any{}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("approve friend: %d %s", w.Code, w.Body.String())
	}
	if m := latestEmail(t, app, "fran@example.com"); !strings.Contains(m.Subject, "approved") {
		t.Fatalf("friend approval subject: %q", m.Subject)
	}
	// Teens and signed-out visitors may not use this door.
	teen := models.User{Email: "teenmember@example.com", Name: "Teen Member", Role: models.RoleMember,
		Status: models.UserActive, AccountType: models.AccountTeen}
	if err := teen.SetPassword("pw"); err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Create(&teen).Error; err != nil {
		t.Fatal(err)
	}
	teenClient := loginAPI(t, app, "teenmember", "pw")
	w = teenClient.do("POST", "/api/v1/account-requests/invite-friend",
		signupBody("Other", "other@example.com", "adult", ""), true)
	if w.Code != http.StatusForbidden {
		t.Fatalf("teen invite friend: %d", w.Code)
	}
	anon := anonClient(t, app, "10.5.0.1")
	w = anon.do("POST", "/api/v1/account-requests/invite-friend",
		signupBody("Anon", "anon2@example.com", "adult", ""), true)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("signed-out invite friend: %d", w.Code)
	}
}

// M1.3 acceptance: public forms are rate limited (10 per IP per hour)
// and carry a honeypot; flagged rows lose inline approval (DEC-44).
func TestPublicFormRateLimitAndHoneypot(t *testing.T) {
	app := testApp(t)
	admin := newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	adminClient := loginAPI(t, app, "admin", "pw")

	// Honeypot: uniform success, stored flagged, Review-only.
	bot := anonClient(t, app, "10.6.0.1")
	body := signupBody("Bot", "bot@example.com", "adult", "")
	body["website"] = "http://spam.example"
	w := bot.do("POST", "/api/v1/account-requests", body, true)
	if w.Code != http.StatusOK {
		t.Fatalf("honeypot submit: %d %s", w.Code, w.Body.String())
	}
	botReq := pendingRequest(t, app, "bot@example.com")
	if !parseRequestFlags(botReq.Flags).Honeypot {
		t.Fatalf("honeypot not flagged: %s", botReq.Flags)
	}
	note := adminNotification(t, app, admin.ID, botReq.ID)
	w = adminClient.do("POST", "/api/v1/admin/account-requests/"+itoa(botReq.ID)+"/approve",
		map[string]any{"fromNotification": note.ID}, true)
	if w.Code != http.StatusForbidden {
		t.Fatalf("inline honeypot approval: %d", w.Code)
	}

	// Rate limit: ten from one IP pass; the eleventh gets the usual
	// answer but stores nothing (DEC-52).
	anon := anonClient(t, app, "10.6.0.2")
	for i := 1; i <= accountRequestIPLimit; i++ {
		w = anon.do("POST", "/api/v1/account-requests",
			signupBody("Person", "person"+itoa(uint(i))+"@example.com", "adult", ""), true)
		if w.Code != http.StatusOK {
			t.Fatalf("submit %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	w = anon.do("POST", "/api/v1/account-requests",
		signupBody("Person", "person11@example.com", "adult", ""), true)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), signupThanks) {
		t.Fatalf("11th submit: %d %s, want the usual thanks", w.Code, w.Body.String())
	}
	if n := requestCount(t, app, "person11@example.com"); n != 0 {
		t.Fatalf("rate-limited submit stored %d rows", n)
	}
	// The fifth submit from that IP was already flagged for velocity.
	fifth := pendingRequest(t, app, "person5@example.com")
	if !parseRequestFlags(fifth.Flags).RateLimited {
		t.Fatalf("fifth submit not velocity-flagged: %s", fifth.Flags)
	}
}

// M1.3 acceptance: a clean adult request approves inline from the
// bell; a second admin losing the race gets the handled answer.
func TestInlineApproveCleanAdultAndRace(t *testing.T) {
	app := testApp(t)
	pat := newTestUser(t, app, "pat", "pw", models.RoleAdmin)
	kim := newTestUser(t, app, "kim", "pw", models.RoleAdmin)
	anon := anonClient(t, app, "10.7.0.1")
	w := anon.do("POST", "/api/v1/account-requests",
		signupBody("Clean Adult", "clean@example.com", "adult", ""), true)
	if w.Code != http.StatusOK {
		t.Fatalf("signup: %d %s", w.Code, w.Body.String())
	}
	req := pendingRequest(t, app, "clean@example.com")
	patNote := adminNotification(t, app, pat.ID, req.ID)
	if !strings.Contains(patNote.Actions, "approve") {
		t.Fatalf("clean adult bell item has no Approve: %s", patNote.Actions)
	}
	patClient := loginAPI(t, app, "pat", "pw")
	w = patClient.do("POST", "/api/v1/admin/account-requests/"+itoa(req.ID)+"/approve",
		map[string]any{"fromNotification": patNote.ID}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("inline approve: %d %s", w.Code, w.Body.String())
	}
	// Kim's copy resolves too; her inline attempt reports who won.
	kimNote := adminNotification(t, app, kim.ID, req.ID)
	kimClient := loginAPI(t, app, "kim", "pw")
	w = kimClient.do("POST", "/api/v1/admin/account-requests/"+itoa(req.ID)+"/approve",
		map[string]any{"fromNotification": kimNote.ID}, true)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "resolvedBy") {
		t.Fatalf("lost race: %d %s", w.Code, w.Body.String())
	}
	// Rejection is never inline, even with a notification id.
	apiDo(t, anon, "POST", "/api/v1/account-requests",
		signupBody("Second", "second@example.com", "adult", ""), http.StatusOK)
	second := pendingRequest(t, app, "second@example.com")
	secondNote := adminNotification(t, app, pat.ID, second.ID)
	w = patClient.do("POST", "/api/v1/admin/account-requests/"+itoa(second.ID)+"/reject",
		map[string]any{"fromNotification": secondNote.ID}, true)
	if w.Code != http.StatusForbidden {
		t.Fatalf("inline reject: %d %s", w.Code, w.Body.String())
	}
}

// M1.3 acceptance: rejection emails the person; requests expire after
// 30 days and leave the pending queue and the bell.
func TestRejectEmailsAndRequestsExpire(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	adminClient := loginAPI(t, app, "admin", "pw")
	anon := anonClient(t, app, "10.8.0.1")

	apiDo(t, anon, "POST", "/api/v1/account-requests",
		signupBody("Rejected Rob", "rob@example.com", "adult", ""), http.StatusOK)
	req := pendingRequest(t, app, "rob@example.com")
	w := adminClient.do("POST", "/api/v1/admin/account-requests/"+itoa(req.ID)+"/reject",
		map[string]any{"note": "We could not verify your connection to the church."}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("reject: %d %s", w.Code, w.Body.String())
	}
	if m := latestEmail(t, app, "rob@example.com"); !strings.Contains(m.Body, "could not verify") {
		t.Fatalf("rejection email lacks the note: %q", m.Body)
	}
	if auditCount(t, app, audit.AccountRejected) != 1 {
		t.Fatal("rejection not audited")
	}

	apiDo(t, anon, "POST", "/api/v1/account-requests",
		signupBody("Old Olga", "olga@example.com", "adult", ""), http.StatusOK)
	old := pendingRequest(t, app, "olga@example.com")
	past := time.Now().UTC().Add(-time.Hour)
	if err := app.DB.Model(&models.AccountRequest{}).Where("id = ?", old.ID).
		Update("expires_at", past).Error; err != nil {
		t.Fatal(err)
	}
	w = adminClient.do("GET", "/api/v1/admin/account-requests", nil, false)
	var pending struct {
		Requests []accountRequestJSON `json:"requests"`
	}
	decodeJSON(t, w, &pending)
	for _, r := range pending.Requests {
		if r.ID == old.ID {
			t.Fatal("expired request still pending in the queue")
		}
	}
	w = adminClient.do("GET", "/api/v1/admin/account-requests?status=expired", nil, false)
	decodeJSON(t, w, &pending)
	if len(pending.Requests) != 1 || pending.Requests[0].ID != old.ID {
		t.Fatalf("expired queue: %+v", pending.Requests)
	}
	var stored models.AccountRequest
	app.DB.First(&stored, old.ID)
	if stored.Status != models.RequestExpired || auditCount(t, app, audit.AccountExpired) != 1 {
		t.Fatalf("expired row: %q", stored.Status)
	}
}

// Self-signup off refuses the public door (member invite still works),
// and only admins flip the setting (DEC-4).
func TestSelfSignupOff(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	newTestUser(t, app, "sam", "pw", models.RoleMember)
	adminClient := loginAPI(t, app, "admin", "pw")
	w := adminClient.do("PUT", "/api/v1/admin/self-signup", map[string]string{"mode": "off"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("turn off: %d %s", w.Code, w.Body.String())
	}
	anon := anonClient(t, app, "10.9.0.1")
	w = anon.do("GET", "/api/v1/site", nil, false)
	if !strings.Contains(w.Body.String(), `"selfSignup":"off"`) {
		t.Fatalf("site does not report off: %s", w.Body.String())
	}
	w = anon.do("POST", "/api/v1/account-requests",
		signupBody("Nope", "nope@example.com", "adult", ""), true)
	if w.Code != http.StatusForbidden || requestCount(t, app, "nope@example.com") != 0 {
		t.Fatalf("signup while off: %d, rows %d", w.Code, requestCount(t, app, "nope@example.com"))
	}
	sam := loginAPI(t, app, "sam", "pw")
	w = sam.do("POST", "/api/v1/account-requests/invite-friend",
		signupBody("Still Works", "works@example.com", "adult", ""), true)
	if w.Code != http.StatusOK {
		t.Fatalf("friend invite while off: %d %s", w.Code, w.Body.String())
	}
	w = sam.do("PUT", "/api/v1/admin/self-signup", map[string]string{"mode": "request"}, true)
	if w.Code != http.StatusForbidden {
		t.Fatalf("member flipping the setting: %d", w.Code)
	}
	w = adminClient.do("PUT", "/api/v1/admin/self-signup", map[string]string{"mode": "open"}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("open mode accepted: %d", w.Code)
	}
}

// A duplicate pending request is stored but flagged, so the bell
// offers Review only (DEC-44).
func TestDuplicatePendingFlagged(t *testing.T) {
	app := testApp(t)
	admin := newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	anon := anonClient(t, app, "10.10.0.1")
	for i := 0; i < 2; i++ {
		w := anon.do("POST", "/api/v1/account-requests",
			signupBody("Dupe", "dupe@example.com", "adult", ""), true)
		if w.Code != http.StatusOK {
			t.Fatalf("submit %d: %d", i, w.Code)
		}
	}
	var rows []models.AccountRequest
	app.DB.Where("email = ?", "dupe@example.com").Order("id").Find(&rows)
	if len(rows) != 2 || parseRequestFlags(rows[0].Flags).Duplicate || !parseRequestFlags(rows[1].Flags).Duplicate {
		t.Fatalf("duplicate flags: %+v", rows)
	}
	note := adminNotification(t, app, admin.ID, rows[1].ID)
	if note.Actions != "[]" {
		t.Fatalf("duplicate bell item carries actions: %s", note.Actions)
	}
	adminClient := loginAPI(t, app, "admin", "pw")
	w := adminClient.do("POST", "/api/v1/admin/account-requests/"+itoa(rows[1].ID)+"/approve",
		map[string]any{"fromNotification": note.ID}, true)
	if w.Code != http.StatusForbidden {
		t.Fatalf("inline duplicate approval: %d", w.Code)
	}
}

func mailsTo(app *App, addr string) int64 {
	var n int64
	app.DB.Model(&models.MailOutbox{}).Where("to_address = ?", addr).Count(&n)
	return n
}

// #91: rotating IPs can't flood one address. Past the per-address limit
// the answer is the same, but nothing is stored or sent.
func TestAccountRequestPerAddressLimit(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	for i := 0; i < targetHourLimit+3; i++ {
		anon := anonClient(t, app, "10.20.0."+itoa2(uint(i+1)))
		w := anon.do("POST", "/api/v1/account-requests",
			signupBody("Victim", "victim@example.com", "adult", ""), true)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), signupThanks) {
			t.Fatalf("submit %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	var stored int64
	app.DB.Model(&models.AccountRequest{}).Where("email = ?", "victim@example.com").Count(&stored)
	if stored != targetHourLimit {
		t.Fatalf("stored %d requests, want %d", stored, targetHourLimit)
	}
	// One request-received email; the duplicates (stored, flagged) send nothing.
	if n := mailsTo(app, "victim@example.com"); n != 1 {
		t.Fatalf("victim got %d emails, want 1", n)
	}
	if n := mailsTo(app, testEmail("admin")); n > 1 {
		t.Fatalf("admin got %d emails, want at most 1", n)
	}
}

// #91: an address that already has an account gets the account-exists
// hint at most up to the per-address limit.
func TestAccountExistsHintPerAddressLimit(t *testing.T) {
	app := testApp(t)
	alice := newTestUser(t, app, "alice", "pw", models.RoleMember)
	for i := 0; i < targetHourLimit+3; i++ {
		anon := anonClient(t, app, "10.21.0."+itoa2(uint(i+1)))
		if w := anon.do("POST", "/api/v1/account-requests",
			signupBody("Alice", alice.Email, "adult", ""), true); w.Code != http.StatusOK {
			t.Fatalf("submit %d: %d", i, w.Code)
		}
	}
	if n := mailsTo(app, alice.Email); n != targetHourLimit {
		t.Fatalf("alice got %d hints, want %d", n, targetHourLimit)
	}
}

func TestAccountRequestListReadBudgetAndInviterNames(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	inviter := newTestUser(t, app, "inviter", "pw", models.RoleMember)
	if err := app.DB.Model(inviter).Update("display_name", "Pat").Error; err != nil {
		t.Fatal(err)
	}
	// Historical requests still name a soft-deleted inviter.
	if err := app.DB.Delete(inviter).Error; err != nil {
		t.Fatal(err)
	}
	client := loginAPI(t, app, "admin", "pw")
	var baseline int64
	for i := 1; i <= 30; i++ {
		r := models.AccountRequest{Name: "Invitee", Email: "invitee@example.com", Source: models.RequestMemberInvite,
			RequestedBy: &inviter.ID, Status: models.RequestPending, AccountType: models.AccountAdult, Flags: "{}", ExpiresAt: time.Now().Add(time.Hour)}
		if err := app.DB.Create(&r).Error; err != nil {
			t.Fatal(err)
		}
		if i != 3 && i != 30 {
			continue
		}
		counter := countDBReads(t, app.DB)
		w := client.do("GET", "/api/v1/admin/account-requests?status=pending", nil, false)
		reads := counter.Reads()
		counter.Stop()
		if w.Code != http.StatusOK {
			t.Fatalf("list: %d %s", w.Code, w.Body.String())
		}
		var body struct {
			Requests []accountRequestJSON `json:"requests"`
		}
		decodeJSON(t, w, &body)
		if len(body.Requests) != i {
			t.Fatalf("got %d requests, want %d", len(body.Requests), i)
		}
		for _, r := range body.Requests {
			if r.RequestedByName != "Pat" {
				t.Fatalf("historical inviter: %+v", r)
			}
		}
		if i == 3 {
			baseline = reads
		}
		if reads != baseline || reads > 5 {
			t.Fatalf("%d requests: %d reads, baseline=%d, cap=5", i, reads, baseline)
		}
		t.Logf("%d requests: %d reads", i, reads)
	}
	missing := uint(999999)
	names := publicNamesIn(app.DB, []uint{missing})
	if got := toAccountRequestJSON(&models.AccountRequest{RequestedBy: &missing}, names); got.RequestedByName != "Someone" {
		t.Fatalf("missing inviter: %+v", got)
	}
	if got := toAccountRequestJSON(&models.AccountRequest{}, names); got.RequestedByName != "" {
		t.Fatalf("self request: %+v", got)
	}
}

// A rate-limited invite-a-friend gets the door's own message, never the
// self-signup text, so the limit stays invisible (DEC-52).
func TestRateLimitedInviteFriendAnswersWithItsOwnMessage(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	newTestUser(t, app, "sam", "pw", models.RoleMember)
	sam := loginAPI(t, app, "sam", "pw")
	sam.ip = "10.6.7.7"
	if err := app.DB.Create(&models.LoginThrottle{Key: "acctreq:ip:" + rateLimitIP(sam.ip),
		Failures: accountRequestIPLimit, WindowStart: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	w := sam.do("POST", "/api/v1/account-requests/invite-friend",
		signupBody("Friend Fran", "fran@example.com", "adult", ""), true)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), signupThanks) ||
		!strings.Contains(w.Body.String(), "your friend will hear from us") {
		t.Fatalf("limited invite: %d %s", w.Code, w.Body.String())
	}
	if n := requestCount(t, app, "fran@example.com"); n != 0 {
		t.Fatalf("limited invite stored %d rows", n)
	}
}
