package main

import (
	"fold/internal/audit"
	"fold/models"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestFocalPoints(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "grier", "password1234", "member")
	leader := newTestUser(t, app, "leader", "password1234", "member")
	c := loginAs(t, app, "grier", "password1234")

	w := c.do("PUT", "/api/v1/me", map[string]interface{}{
		"avatarFocal": map[string]float64{"x": 30, "y": 70},
	}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT /me focal: %d %s", w.Code, w.Body.String())
	}
	var me struct {
		User userJSON `json:"user"`
	}
	decodeJSON(t, w, &me)
	if me.User.AvatarFocalX != 30 || me.User.AvatarFocalY != 70 {
		t.Fatalf("focal not saved: %+v", me.User)
	}

	// Clamping.
	w = c.do("PUT", "/api/v1/me", map[string]interface{}{
		"avatarFocal": map[string]float64{"x": 150, "y": -20},
	}, true)
	decodeJSON(t, w, &me)
	if me.User.AvatarFocalX != 100 || me.User.AvatarFocalY != 0 {
		t.Fatalf("focal not clamped: %+v", me.User)
	}

	// Group cover focal (leader). Management checks the leader membership,
	// which the API creates alongside LeaderID.
	g := models.Group{Name: "G", LeaderID: leader.ID, Status: models.GroupActive}
	app.DB.Create(&g)
	app.DB.Create(&models.Membership{
		UserID: leader.ID, GroupID: g.ID,
		Role: models.MembershipLeader, Status: "active", JoinedAt: time.Now().UTC(),
	})
	lc := loginAs(t, app, "leader", "password1234")
	w = lc.do("PUT", "/api/v1/groups/"+itoa2(g.ID), map[string]interface{}{
		"coverFocal": map[string]float64{"x": 25, "y": 25},
	}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT group focal: %d %s", w.Code, w.Body.String())
	}
	var gr struct {
		Group groupSummaryJSON `json:"group"`
	}
	decodeJSON(t, w, &gr)
	if gr.Group.CoverFocalX != 25 || gr.Group.CoverFocalY != 25 {
		t.Fatalf("cover focal not saved: %+v", gr.Group)
	}
}

func TestCoverFocalUpdateKeepsUpload(t *testing.T) {
	app := testApp(t)
	leader := newTestUser(t, app, "leader", "password1234", models.RoleMember)
	g := models.Group{Name: "Band", LeaderID: leader.ID, Status: models.GroupActive}
	if err := app.DB.Create(&g).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := app.DB.Create(&models.Membership{
		UserID: leader.ID, GroupID: g.ID,
		Role: models.MembershipLeader, Status: "active", JoinedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	c := loginAPI(t, app, "leader", "password1234")
	gid := itoa(g.ID)
	w := c.doMultipart("POST", "/api/v1/groups/"+gid+"/cover", "cover", "cover.png", tinyPNG(t))
	if w.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	var uploaded struct {
		ImageURL string `json:"imageUrl"`
	}
	decodeJSON(t, w, &uploaded)

	w = c.do("PUT", "/api/v1/groups/"+gid, map[string]any{
		"coverFocal": map[string]float64{"x": 10, "y": 20},
	}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("focal: %d %s", w.Code, w.Body.String())
	}
	var got models.Group
	if err := app.DB.First(&got, g.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.ImageURL != uploaded.ImageURL || !strings.HasPrefix(got.ImageURL, "/uploads/") {
		t.Fatalf("cover URL changed: %q", got.ImageURL)
	}
	if got.CoverFocalX != 10 || got.CoverFocalY != 20 {
		t.Fatalf("focal: %v %v", got.CoverFocalX, got.CoverFocalY)
	}
}

// M1.2 acceptance: changing the sign-in email sends a verification link to
// the new address and a notice to the old one; the change applies only after
// verification and ends every other session.
func TestVerifiedEmailChange(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "alice", "password1234", models.RoleMember)
	ac := loginAPI(t, app, "alice", "password1234")
	other := loginAPI(t, app, "alice", "password1234") // a second session

	w := ac.do("PUT", "/api/v1/me", map[string]string{"email": "alice.new@example.com"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("request change: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		VerificationSent bool `json:"verificationSent"`
	}
	decodeJSON(t, w, &body)
	if !body.VerificationSent {
		t.Fatalf("no verificationSent: %s", w.Body.String())
	}
	// The address hasn't changed yet.
	var u models.User
	if err := app.DB.Where("email = ?", "alice@example.com").First(&u).Error; err != nil {
		t.Fatalf("email changed before verification: %v", err)
	}

	// Verification link to the new address, notice to the old one.
	verifyMail := latestEmail(t, app, "alice.new@example.com")
	if !strings.Contains(verifyMail.Subject, "Verify") {
		t.Fatalf("verify subject: %q", verifyMail.Subject)
	}
	noticeMail := latestEmail(t, app, "alice@example.com")
	if !strings.Contains(noticeMail.Subject, "changing") {
		t.Fatalf("notice subject: %q", noticeMail.Subject)
	}
	token := tokenInEmail(t, verifyMail)

	// Verify: the change applies.
	w = ac.do("POST", "/api/v1/me/email/verify", map[string]string{"token": token}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("verify: %d %s", w.Code, w.Body.String())
	}
	if err := app.DB.Where("email = ?", "alice.new@example.com").First(&u).Error; err != nil {
		t.Fatalf("email not changed: %v", err)
	}
	if auditCount(t, app, audit.EmailChangeRequested) != 1 ||
		auditCount(t, app, audit.EmailChanged) != 1 {
		t.Fatalf("email change not audited")
	}

	// Every other session ended...
	w = other.do("GET", "/api/v1/auth/me", nil, false)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("other session alive: %d", w.Code)
	}
	// ...but this one still works.
	w = ac.do("GET", "/api/v1/auth/me", nil, false)
	if w.Code != http.StatusOK {
		t.Fatalf("own session ended: %d", w.Code)
	}
	// Single use.
	w = ac.do("POST", "/api/v1/me/email/verify", map[string]string{"token": token}, true)
	if w.Code == http.StatusOK {
		t.Fatalf("verification token reused")
	}

	// Login works with the new address.
	loginAPI(t, app, "alice.new", "password1234")
}

func TestUpdateMe(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "grier", "password1234", "member")
	c := loginAPI(t, app, "grier", "password1234")

	w := c.do("PUT", "/api/v1/me", map[string]string{
		"name":        "Grier Johnson",
		"displayName": "Grier",
		"email":       "Grier@Example.com", // same address: allowed
		"phone":       "555-0100",
	}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT /me: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		User userJSON `json:"user"`
	}
	decodeJSON(t, w, &resp)
	if resp.User.DisplayName != "Grier" || resp.User.Name != "Grier Johnson" ||
		resp.User.Email != "grier@example.com" || resp.User.Phone != "555-0100" {
		t.Fatalf("unexpected user json: %+v", resp.User)
	}
	if resp.User.AvatarURL != "" {
		t.Fatalf("expected empty avatarUrl, got %q", resp.User.AvatarURL)
	}

	// Partial update: only displayName.
	w = c.do("PUT", "/api/v1/me", map[string]string{"displayName": "GJ"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT /me partial: %d %s", w.Code, w.Body.String())
	}
	decodeJSON(t, w, &resp)
	if resp.User.DisplayName != "GJ" {
		t.Fatalf("displayName not updated: %+v", resp.User)
	}
	if resp.User.Name != "Grier Johnson" {
		t.Fatalf("name should be unchanged: %+v", resp.User)
	}
}

func TestUpdateMeValidation(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "grier", "password1234", "member")
	c := loginAPI(t, app, "grier", "password1234")

	w := c.do("PUT", "/api/v1/me", map[string]string{"email": "not-an-email"}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad email: got %d, want 400", w.Code)
	}
	w = c.do("PUT", "/api/v1/me", map[string]string{"name": strings.Repeat("x", 101)}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("long name: got %d, want 400", w.Code)
	}
	// The email is the login: it can't be cleared, and changing it starts
	// a verification flow (M1.2) instead of applying directly.
	w = c.do("PUT", "/api/v1/me", map[string]string{"email": ""}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("clear email: got %d, want 400", w.Code)
	}
	w = c.do("PUT", "/api/v1/me", map[string]string{"email": "other@example.com"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("change email: got %d, want 200", w.Code)
	}
	var changed struct {
		VerificationSent bool `json:"verificationSent"`
		User             struct {
			Email string `json:"email"`
		} `json:"user"`
	}
	decodeJSON(t, w, &changed)
	if !changed.VerificationSent {
		t.Fatalf("no verification started: %s", w.Body.String())
	}
	if changed.User.Email != "grier@example.com" {
		t.Fatalf("email changed before verification: %q", changed.User.Email)
	}
}

func TestAvatarUploadAndDelete(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "grier", "password1234", "member")
	c := loginAPI(t, app, "grier", "password1234")

	w := c.doMultipart("POST", "/api/v1/me/avatar", "avatar", "me.png", tinyPNG(t))
	if w.Code != http.StatusOK {
		t.Fatalf("avatar upload: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		AvatarURL string `json:"avatarUrl"`
	}
	decodeJSON(t, w, &resp)
	if !strings.HasPrefix(resp.AvatarURL, "/uploads/avatars/") {
		t.Fatalf("unexpected avatarUrl %q", resp.AvatarURL)
	}

	// The file is publicly served.
	getW := httptest.NewRecorder()
	app.setupRouter().ServeHTTP(getW, httptest.NewRequest("GET", resp.AvatarURL, nil))
	if getW.Code != http.StatusOK {
		t.Fatalf("GET avatar: %d", getW.Code)
	}
	if ct := getW.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("avatar content-type %q", ct)
	}
	if getW.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("missing nosniff header")
	}

	// /auth/me reflects the avatar.
	w = c.do("GET", "/api/v1/auth/me", nil, false)
	var me struct {
		User userJSON `json:"user"`
	}
	decodeJSON(t, w, &me)
	if me.User.AvatarURL != resp.AvatarURL {
		t.Fatalf("me avatarUrl %q != %q", me.User.AvatarURL, resp.AvatarURL)
	}

	// Delete it.
	w = c.do("DELETE", "/api/v1/me/avatar", nil, true)
	if w.Code != http.StatusOK {
		t.Fatalf("avatar delete: %d %s", w.Code, w.Body.String())
	}
	var u models.User
	app.DB.Where("email = ?", testEmail("grier")).First(&u)
	if u.AvatarKey != "" {
		t.Fatalf("avatar key not cleared")
	}
	// The file is gone.
	getW = httptest.NewRecorder()
	app.setupRouter().ServeHTTP(getW, httptest.NewRequest("GET", resp.AvatarURL, nil))
	if getW.Code != http.StatusNotFound {
		t.Fatalf("GET deleted avatar: %d, want 404", getW.Code)
	}
}

func TestAvatarUploadRejectsNonImage(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "grier", "password1234", "member")
	c := loginAPI(t, app, "grier", "password1234")

	w := c.doMultipart("POST", "/api/v1/me/avatar", "avatar", "evil.html",
		[]byte("<script>alert(1)</script>"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("non-image upload: got %d, want 400", w.Code)
	}
}

func TestGroupCoverUpload(t *testing.T) {
	app := testApp(t)
	leader := newTestUser(t, app, "grier", "password1234", "member")
	other := newTestUser(t, app, "other", "password1234", "member")
	_ = other
	g := newTestGroup(t, app, leader, "Test Group", models.GroupActive)

	leaderClient := loginAPI(t, app, "grier", "password1234")
	coverPath := "/api/v1/groups/" + strconv.Itoa(int(g.ID)) + "/cover"
	w := leaderClient.doMultipart("POST", coverPath, "cover", "cover.png", tinyPNG(t))
	if w.Code != http.StatusOK {
		t.Fatalf("cover upload: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		ImageURL string `json:"imageUrl"`
	}
	decodeJSON(t, w, &resp)
	if !strings.HasPrefix(resp.ImageURL, "/uploads/covers/") {
		t.Fatalf("unexpected imageUrl %q", resp.ImageURL)
	}

	// Non-leader, non-admin cannot upload.
	otherClient := loginAPI(t, app, "other", "password1234")
	w = otherClient.doMultipart("POST", coverPath, "cover", "cover.png", tinyPNG(t))
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-leader cover upload: got %d, want 403", w.Code)
	}

	// Leader can delete it.
	w = leaderClient.do("DELETE", coverPath, nil, true)
	if w.Code != http.StatusOK {
		t.Fatalf("cover delete: %d %s", w.Code, w.Body.String())
	}
	var got models.Group
	app.DB.First(&got, g.ID)
	if got.ImageURL != "" {
		t.Fatalf("cover not cleared")
	}
}

func TestServeUploadRejectsTraversal(t *testing.T) {
	app := testApp(t)
	r := app.setupRouter()
	for _, p := range []string{"/uploads/../main.go", "/uploads/%2e%2e/main.go"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("GET %s: got %d, want 404", p, w.Code)
		}
	}
}

// Covers are upload-only: an imageUrl in a create or update body is ignored.
func TestGroupCoverLinkIgnored(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	c := loginAPI(t, app, "admin", "password1234")
	w := c.do("POST", "/api/v1/groups", map[string]any{"name": "Choir", "imageUrl": "https://example.com/a.jpg"}, true)
	var created struct {
		Group groupSummaryJSON `json:"group"`
	}
	decodeJSON(t, w, &created)
	if created.Group.ImageURL != "" {
		t.Fatalf("create stored link: %q", created.Group.ImageURL)
	}
	c.do("PUT", "/api/v1/groups/"+itoa(created.Group.ID), map[string]any{"imageUrl": "javascript:alert(1)"}, true)
	var g models.Group
	app.DB.First(&g, created.Group.ID)
	if g.ImageURL != "" {
		t.Fatalf("update stored link: %q", g.ImageURL)
	}
	if w := c.do("POST", "/api/v1/me/avatar/url", map[string]string{"url": "https://example.com/a.png"}, true); w.Code != http.StatusNotFound {
		t.Fatalf("avatar url endpoint still exists: %d", w.Code)
	}
}

// The profile time zone (DEC-58) starts empty (the church's), takes any
// IANA name, and refuses names time.LoadLocation would quietly accept.
func TestUpdateMeTimezone(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "grier", "password1234", "member")
	c := loginAPI(t, app, "grier", "password1234")
	var me struct {
		User struct {
			Timezone string `json:"timezone"`
		} `json:"user"`
	}
	decodeJSON(t, c.do("GET", "/api/v1/auth/me", nil, false), &me)
	if me.User.Timezone != "" {
		t.Fatalf("new user zone: %q, want empty", me.User.Timezone)
	}
	for _, bad := range []string{"", "Local", "Mars/Olympus", strings.Repeat("A", 65)} {
		if w := c.do("PUT", "/api/v1/me", map[string]string{"timezone": bad}, true); w.Code != http.StatusBadRequest {
			t.Fatalf("zone %q: got %d, want 400", bad, w.Code)
		}
	}
	w := c.do("PUT", "/api/v1/me", map[string]string{"timezone": " America/Boise "}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("set zone: %d %s", w.Code, w.Body.String())
	}
	decodeJSON(t, w, &me)
	if me.User.Timezone != "America/Boise" {
		t.Fatalf("saved zone: %q", me.User.Timezone)
	}
}

// #92: the email-change form can't be used to spam an address. Past the
// limit the answer is the same (DEC-52) but nothing is sent.
func TestEmailChangeRateLimited(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "alice", "password1234", models.RoleMember)
	ac := loginAPI(t, app, "alice", "password1234")
	for i := 0; i < targetHourLimit; i++ {
		apiDo(t, ac, "PUT", "/api/v1/me", map[string]string{"email": "victim@example.com"}, http.StatusOK)
	}
	apiDo(t, ac, "PUT", "/api/v1/me", map[string]string{"email": "victim@example.com"}, http.StatusOK)
	var n int64
	app.DB.Model(&models.MailOutbox{}).Where("to_address = ?", "victim@example.com").Count(&n)
	if n != targetHourLimit {
		t.Fatalf("victim got %d emails, want %d", n, targetHourLimit)
	}
	// The per-user limit applies across targets too.
	apiDo(t, ac, "PUT", "/api/v1/me", map[string]string{"email": "other@example.com"}, http.StatusOK)
	// Another member can't add to the same victim either.
	newTestUser(t, app, "bob", "password1234", models.RoleMember)
	bc := loginAPI(t, app, "bob", "password1234")
	apiDo(t, bc, "PUT", "/api/v1/me", map[string]string{"email": "victim@example.com"}, http.StatusOK)
	for addr, want := range map[string]int64{"victim@example.com": targetHourLimit, "other@example.com": 0} {
		app.DB.Model(&models.MailOutbox{}).Where("to_address = ?", addr).Count(&n)
		if n != want {
			t.Fatalf("%s got %d emails, want %d", addr, n, want)
		}
	}
}

// ---------------------------------------------------------------------------
// My household (M1.9 slice 1)
// ---------------------------------------------------------------------------

func getMyHousehold(t *testing.T, c *apiClient) householdJSON {
	t.Helper()
	w := c.do("GET", "/api/v1/me/household", nil, false)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /me/household: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Household householdJSON `json:"household"`
	}
	decodeJSON(t, w, &body)
	return body.Household
}

func addMyPerson(t *testing.T, c *apiClient, body map[string]any) householdPersonJSON {
	t.Helper()
	w := c.do("POST", "/api/v1/me/household/people", body, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /me/household/people %v: %d %s", body, w.Code, w.Body.String())
	}
	var out struct {
		Person householdPersonJSON `json:"person"`
	}
	decodeJSON(t, w, &out)
	return out.Person
}

func TestMyHouseholdCreatedOnFirstUse(t *testing.T) {
	app := testApp(t)
	u := newTestUser(t, app, "john", "password1234", models.RoleMember)
	app.DB.Model(u).Update("name", "John Smith")
	c := loginAPI(t, app, "john", "password1234")

	// GET is read-only (Warden, #106): no household yet means null, and
	// nothing is written.
	if w := c.do("GET", "/api/v1/me/household", nil, false); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"household":null`) {
		t.Fatalf("GET before first use: %d %s", w.Code, w.Body.String())
	}
	var none int64
	app.DB.Model(&models.HouseholdAccount{}).Where("user_id = ?", u.ID).Count(&none)
	if none != 0 {
		t.Fatalf("GET created a household account row")
	}
	// PUT creates it on first use.
	if w := c.do("PUT", "/api/v1/me/household", map[string]string{}, true); w.Code != http.StatusOK {
		t.Fatalf("PUT first use: %d %s", w.Code, w.Body.String())
	}

	h := getMyHousehold(t, c)
	if h.Name != "Smith household" || h.CreatedBy != u.ID || len(h.People) != 0 {
		t.Fatalf("first household: %+v", h)
	}
	if len(h.Accounts) != 1 || h.Accounts[0].UserID != u.ID || h.Accounts[0].Relation != models.HouseholdRelationGuardian {
		t.Fatalf("household accounts: %+v", h.Accounts)
	}
	// Created once: a later GET is the same household, audited once.
	if again := getMyHousehold(t, c); again.ID != h.ID {
		t.Fatalf("second GET made household %d, want %d", again.ID, h.ID)
	}
	if rows, _ := auditRows(t, app, audit.HouseholdCreated); len(rows) != 1 || rows[0].TargetType != "household" || rows[0].TargetID != h.ID {
		t.Fatalf("household.created audit: %+v", rows)
	}
	var count int64
	app.DB.Model(&models.HouseholdAccount{}).Where("user_id = ?", u.ID).Count(&count)
	if count != 1 {
		t.Fatalf("household accounts for one user: %d, want 1", count)
	}

	// Editable name and address; an empty name is refused.
	w := c.do("PUT", "/api/v1/me/household", map[string]string{"name": "The Smiths", "address": "1 Oak St"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT /me/household: %d %s", w.Code, w.Body.String())
	}
	if h = getMyHousehold(t, c); h.Name != "The Smiths" || h.Address != "1 Oak St" {
		t.Fatalf("edited household: %+v", h)
	}
	w = c.do("PUT", "/api/v1/me/household", map[string]string{"name": " "}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty household name: %d, want 400", w.Code)
	}

	// Signed out: the auth answer, and no household appears.
	anon := newAPIClient(t, app)
	anon.fetchCSRF()
	if w := anon.do("GET", "/api/v1/me/household", nil, false); w.Code != http.StatusUnauthorized {
		t.Fatalf("signed-out household: %d, want 401", w.Code)
	}
}

func TestTeenAccountGetsNoHousehold(t *testing.T) {
	app := testApp(t)
	teenUser := newTestUser(t, app, "teen", "password1234", models.RoleMember)
	app.DB.Model(teenUser).Update("account_type", models.AccountTeen)
	c := loginAPI(t, app, "teen", "password1234")
	if w := c.do("GET", "/api/v1/me/household", nil, false); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"household":null`) {
		t.Fatalf("teen GET household: %d %s, want null", w.Code, w.Body.String())
	}
	if w := c.do("PUT", "/api/v1/me/household", map[string]string{"name": "Mine"}, true); w.Code != http.StatusForbidden {
		t.Fatalf("teen PUT household: %d %s, want 403", w.Code, w.Body.String())
	}
	var count int64
	app.DB.Model(&models.HouseholdAccount{}).Where("user_id = ?", teenUser.ID).Count(&count)
	if count != 0 {
		t.Fatalf("a teen got a household account row: %d", count)
	}
}

func TestAddHouseholdPeopleAndLinks(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	newTestUser(t, app, "pat", "password1234", models.RoleMember)
	admin := loginAPI(t, app, "admin", "password1234")
	c := loginAPI(t, app, "pat", "password1234")

	// An adult needs no approval: no link, no admin task.
	spouse := addMyPerson(t, c, map[string]any{"firstName": "Mary", "lastName": "Smith", "kind": "adult"})
	if spouse.LinkStatus != "none" || spouse.GuardianLinkID != nil || spouse.Name != "Mary Smith" {
		t.Fatalf("adult person: %+v", spouse)
	}
	var links int64
	app.DB.Model(&models.GuardianLink{}).Count(&links)
	if links != 0 {
		t.Fatalf("adult person made %d guardian links", links)
	}
	if _, _, waiting, _ := badge(t, admin, ""); waiting != 0 {
		t.Fatalf("admin waiting after adult: %d", waiting)
	}

	// A child waits for an admin: a pending link, a waiting bell item,
	// and a household.person_added audit row.
	child := addMyPerson(t, c, map[string]any{
		"firstName": "Kid", "lastName": "Smith", "kind": "child",
		"birthDate": "2020-05-01", "careNotes": "Peanut allergy", "photoConsent": true,
	})
	if child.LinkStatus != models.GuardianPending || child.GuardianLinkID == nil || child.BirthDate != "2020-05-01" || !child.PhotoConsent || child.CareNotes != nil {
		t.Fatalf("child person: %+v", child)
	}
	var link models.GuardianLink
	if err := app.DB.First(&link, *child.GuardianLinkID).Error; err != nil {
		t.Fatal(err)
	}
	if link.Status != models.GuardianPending || link.PersonID == nil || *link.PersonID != child.ID || link.TeenUserID != nil {
		t.Fatalf("guardian link: %+v", link)
	}
	if _, _, waiting, _ := badge(t, admin, ""); waiting != 1 {
		t.Fatalf("admin waiting after child: %d, want 1", waiting)
	}
	if n := findKind(feed(t, admin, ""), "household.link_pending"); n == nil || !n.Waiting {
		t.Fatalf("admin feed has no waiting household link: %+v", feed(t, admin, ""))
	}
	if rows, _ := auditRows(t, app, audit.HouseholdPersonAdded); len(rows) != 2 {
		t.Fatalf("household.person_added audit rows: %d, want 2", len(rows))
	}

	// A teen waits the same way.
	teen := addMyPerson(t, c, map[string]any{"firstName": "Tess", "kind": "teen", "birthDate": "2012-03-04"})
	if teen.LinkStatus != models.GuardianPending {
		t.Fatalf("teen person: %+v", teen)
	}
	if h := getMyHousehold(t, c); len(h.People) != 3 {
		t.Fatalf("household people: %+v", h.People)
	}

	// Validation: bad kind, no first name, bad birth date.
	for name, body := range map[string]map[string]any{
		"bad kind":      {"firstName": "X", "kind": "alien"},
		"no first name": {"kind": "adult"},
		"bad date":      {"firstName": "X", "kind": "child", "birthDate": "2020-02-30"},
	} {
		if w := c.do("POST", "/api/v1/me/household/people", body, true); w.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s, want 400", name, w.Code, w.Body.String())
		}
	}
	w := c.do("POST", "/api/v1/me/household/people", map[string]any{"firstName": "X", "kind": "alien"}, true)
	if !strings.Contains(w.Body.String(), `"code":"invalid_kind"`) {
		t.Fatalf("bad kind code: %s", w.Body.String())
	}
}

func TestHouseholdPersonEditAndRemove(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	newTestUser(t, app, "pat", "password1234", models.RoleMember)
	admin := loginAPI(t, app, "admin", "password1234")
	c := loginAPI(t, app, "pat", "password1234")

	child := addMyPerson(t, c, map[string]any{"firstName": "Kid", "kind": "child", "birthDate": "2021-01-02"})
	path := "/api/v1/me/household/people/" + itoa2(child.ID)

	w := c.do("PUT", path, map[string]any{"firstName": "Kidd", "careNotes": "Asthma inhaler"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("edit person: %d %s", w.Code, w.Body.String())
	}
	var edited struct {
		Person householdPersonJSON `json:"person"`
	}
	decodeJSON(t, w, &edited)
	if edited.Person.FirstName != "Kidd" || edited.Person.CareNotes != nil || edited.Person.LinkStatus != models.GuardianPending {
		t.Fatalf("edited person: %+v", edited.Person)
	}

	// Becoming an adult revokes the pending link and closes the task.
	if _, _, waiting, _ := badge(t, admin, ""); waiting != 1 {
		t.Fatalf("admin waiting before kind change: %d", waiting)
	}
	w = c.do("PUT", path, map[string]any{"kind": "adult", "birthDate": "1990-01-02"}, true)
	decodeJSON(t, w, &edited)
	if edited.Person.Kind != "adult" || edited.Person.LinkStatus != "none" {
		t.Fatalf("adult after kind change: %+v", edited.Person)
	}
	var link models.GuardianLink
	app.DB.Where("person_id = ?", child.ID).First(&link)
	if link.Status != models.GuardianRevoked {
		t.Fatalf("link after kind change: %+v", link)
	}
	if _, _, waiting, _ := badge(t, admin, ""); waiting != 0 {
		t.Fatalf("admin waiting after revoke: %d", waiting)
	}
	if rows, _ := auditRows(t, app, audit.GuardianLinkRevoked); len(rows) != 1 {
		t.Fatalf("guardian_link.revoked audit rows: %d", len(rows))
	}

	// Removing is a soft delete of the household person's place in the list.
	w = c.do("DELETE", path, nil, true)
	if w.Code != http.StatusOK {
		t.Fatalf("remove person: %d %s", w.Code, w.Body.String())
	}
	if h := getMyHousehold(t, c); len(h.People) != 0 {
		t.Fatalf("people after removal: %+v", h.People)
	}
	var row models.HouseholdPerson
	if err := app.DB.Unscoped().First(&row, child.ID).Error; err != nil || !row.DeletedAt.Valid {
		t.Fatalf("removed person row should survive soft-deleted: %+v %v", row, err)
	}
	if rows, _ := auditRows(t, app, audit.HouseholdPersonRemoved); len(rows) != 1 {
		t.Fatalf("household.person_removed audit rows: %d", len(rows))
	}
	// Gone stays gone.
	if w := c.do("DELETE", path, nil, true); w.Code != http.StatusNotFound {
		t.Fatalf("second remove: %d, want 404", w.Code)
	}

	// Removing a still-pending child withdraws the admin task.
	pendingChild := addMyPerson(t, c, map[string]any{"firstName": "Pip", "kind": "child"})
	if _, _, waiting, _ := badge(t, admin, ""); waiting != 1 {
		t.Fatalf("admin waiting before removing Pip: %d", waiting)
	}
	apiDo(t, c, "DELETE", "/api/v1/me/household/people/"+itoa2(pendingChild.ID), nil, http.StatusOK)
	if _, _, waiting, _ := badge(t, admin, ""); waiting != 0 {
		t.Fatalf("admin waiting after removing pending child: %d", waiting)
	}
}

func TestHouseholdIsolation(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "ana", "password1234", models.RoleMember)
	newTestUser(t, app, "ben", "password1234", models.RoleMember)
	ana := loginAPI(t, app, "ana", "password1234")
	ben := loginAPI(t, app, "ben", "password1234")

	child := addMyPerson(t, ana, map[string]any{"firstName": "AnaKid", "kind": "child"})
	// Ben's household is his own: no people, a different id.
	hb := getMyHousehold(t, ben)
	if hb.ID == getMyHousehold(t, ana).ID || len(hb.People) != 0 {
		t.Fatalf("Ben's household: %+v", hb)
	}
	// Ben can't edit or remove Ana's person, and the answer is 404.
	path := "/api/v1/me/household/people/" + itoa2(child.ID)
	if w := ben.do("PUT", path, map[string]any{"firstName": "Hacked"}, true); w.Code != http.StatusNotFound {
		t.Fatalf("Ben edits Ana's person: %d, want 404", w.Code)
	}
	if w := ben.do("DELETE", path, nil, true); w.Code != http.StatusNotFound {
		t.Fatalf("Ben removes Ana's person: %d, want 404", w.Code)
	}
	if h := getMyHousehold(t, ana); len(h.People) != 1 || h.People[0].FirstName != "AnaKid" {
		t.Fatalf("Ana's household after Ben's attempts: %+v", h.People)
	}
}
