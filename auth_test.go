package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"fold/internal/testdb"
	"fold/models"
	"fold/storage"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/sessions"
)

// TestMain gives this package one migrated Postgres database and drops it
// afterwards (internal/testdb).
func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

// testApp builds an App for handler tests on the package's Postgres
// database, emptied before each test. Tests in this package do not call
// t.Parallel, so they share that database safely.
func testApp(t *testing.T) *App {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := testdb.Shared(t)
	store := sessions.NewCookieStore([]byte("test-secret"))
	store.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   86400 * 7,
		HttpOnly: true,
		Secure:   false,
		SameSite: http.SameSiteLaxMode,
	}
	uploads, err := storage.NewLocalStore(t.TempDir(), "/uploads")
	if err != nil {
		t.Fatalf("upload store: %v", err)
	}
	return &App{
		DB:      db,
		Store:   store,
		Config:  Config{Env: "dev", Port: "8080"},
		Uploads: uploads,
	}
}

// testEmail turns a short test name ("alice") into an address.
func testEmail(name string) string {
	if strings.Contains(name, "@") {
		return name
	}
	return name + "@example.com"
}

// newTestUser creates an active account named name, signing in as
// testEmail(name).
func newTestUser(t *testing.T, app *App, name, password, role string) *models.User {
	t.Helper()
	u := models.User{Email: testEmail(name), Role: role, Name: name, Status: models.UserActive}
	if err := u.SetPassword(password); err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Create(&u).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return &u
}

// apiClient wraps a router with cookie-jar + CSRF handling for API tests.
type apiClient struct {
	t      *testing.T
	router http.Handler
	jar    map[string]*http.Cookie
	token  string
	ip     string // client address for requests; empty uses httptest's default
}

func newAPIClient(t *testing.T, app *App) *apiClient {
	t.Helper()
	return &apiClient{t: t, router: app.setupRouter(), jar: map[string]*http.Cookie{}}
}

func (c *apiClient) fetchCSRF() {
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/auth/csrf", nil)
	for _, ck := range c.jar {
		req.AddCookie(ck)
	}
	c.router.ServeHTTP(w, req)
	for _, ck := range w.Result().Cookies() {
		c.jar[ck.Name] = ck
	}
	var body struct {
		CSRFToken string `json:"csrfToken"`
	}
	if err := json.NewDecoder(w.Result().Body).Decode(&body); err != nil {
		c.t.Fatalf("decode csrf: %v", err)
	}
	if body.CSRFToken == "" {
		c.t.Fatal("empty csrf token")
	}
	c.token = body.CSRFToken
}

func (c *apiClient) do(method, path string, jsonBody any, csrf bool) *httptest.ResponseRecorder {
	var rdr *bytes.Reader
	if jsonBody != nil {
		b, err := json.Marshal(jsonBody)
		if err != nil {
			c.t.Fatalf("marshal: %v", err)
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	if c.ip != "" {
		req.RemoteAddr = c.ip + ":1234"
	}
	if jsonBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, ck := range c.jar {
		req.AddCookie(ck)
	}
	if csrf {
		req.Header.Set("X-CSRF-Token", c.token)
	}
	w := httptest.NewRecorder()
	c.router.ServeHTTP(w, req)
	for _, ck := range w.Result().Cookies() {
		c.jar[ck.Name] = ck
	}
	return w
}

func decodeJSON(t *testing.T, w *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.NewDecoder(w.Result().Body).Decode(v); err != nil {
		t.Fatalf("decode json: %v", err)
	}
}

func loginAPI(t *testing.T, app *App, name, password string) *apiClient {
	t.Helper()
	c := newAPIClient(t, app)
	c.fetchCSRF()
	w := c.do("POST", "/api/v1/auth/login",
		map[string]string{"email": testEmail(name), "password": password}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", w.Code, w.Body.String())
	}
	return c
}

// --- tests ---

func TestSetupWizardCreatesAdmin(t *testing.T) {
	app := testApp(t)
	r := app.setupRouter()

	// Fetch the setup form to get a CSRF token (form posts need it too).
	getW := httptest.NewRecorder()
	r.ServeHTTP(getW, httptest.NewRequest("GET", "/setup", nil))
	formRe := regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)
	m := formRe.FindStringSubmatch(getW.Body.String())
	if len(m) != 2 {
		t.Fatalf("no csrf token in setup form")
	}

	form := url.Values{
		"csrf_token": {m[1]},
		"site_name":  {"Fold Test"}, "name": {"Root"}, "email": {"Root@Example.com"},
		"password": {"pw12345678"}, "password_confirm": {"pw12345678"},
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, ck := range getW.Result().Cookies() {
		req.AddCookie(ck)
	}
	r.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("setup: got %d", w.Code)
	}
	var u models.User
	if err := app.DB.Where("email = ?", "root@example.com").First(&u).Error; err != nil {
		t.Fatalf("admin not created: %v", err)
	}
	if u.Role != models.RoleAdmin {
		t.Fatalf("expected admin role, got %q", u.Role)
	}
	var s models.Setting
	app.DB.Where("key = ?", "site_name").First(&s)
	if s.Value != "Fold Test" {
		t.Fatalf("site name not stored: %q", s.Value)
	}
}

func TestAPILoginFlow(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "alice", "secret123", models.RoleMember)

	c := newAPIClient(t, app)
	c.fetchCSRF()

	// wrong password → 401
	w := c.do("POST", "/api/v1/auth/login",
		map[string]string{"email": "alice@example.com", "password": "nope"}, true)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: got %d", w.Code)
	}

	// correct → 200 with user payload
	w = c.do("POST", "/api/v1/auth/login",
		map[string]string{"email": "Alice@Example.COM", "password": "secret123"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("login: got %d %s", w.Code, w.Body.String())
	}
	var loginBody struct {
		User userJSON `json:"user"`
	}
	decodeJSON(t, w, &loginBody)
	if loginBody.User.Email != "alice@example.com" {
		t.Fatalf("unexpected user: %+v", loginBody.User)
	}

	// me works with the session cookie
	w = c.do("GET", "/api/v1/auth/me", nil, false)
	if w.Code != http.StatusOK {
		t.Fatalf("me: got %d", w.Code)
	}

	// anonymous me → 401 JSON (not a redirect)
	anon := newAPIClient(t, app)
	w = anon.do("GET", "/api/v1/auth/me", nil, false)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anon me: got %d", w.Code)
	}
}

func TestAPICSRFRequired(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "bob", "secret123", models.RoleMember)
	c := loginAPI(t, app, "bob", "secret123")

	// POST without the CSRF header → 403
	w := c.do("POST", "/api/v1/groups", map[string]any{"name": "X"}, false)
	if w.Code != http.StatusForbidden {
		t.Fatalf("missing csrf: got %d", w.Code)
	}
	// with token → not a CSRF rejection
	w = c.do("POST", "/api/v1/groups", map[string]any{"name": "X"}, true)
	if w.Code == http.StatusForbidden {
		t.Fatalf("valid csrf rejected: %d", w.Code)
	}
}

func TestGroupApprovalFlow(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	newTestUser(t, app, "leader", "pw", models.RoleMember)

	leader := loginAPI(t, app, "leader", "pw")

	// Create → pending, creator becomes leader-member.
	w := leader.do("POST", "/api/v1/groups", map[string]any{
		"name": "Hiking", "description": "trails", "tags": []string{"outdoors"},
	}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create group: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Group groupSummaryJSON `json:"group"`
	}
	decodeJSON(t, w, &created)
	if created.Group.Status != models.GroupPending {
		t.Fatalf("expected pending, got %q", created.Group.Status)
	}

	// Pending group is hidden from the public list.
	w = leader.do("GET", "/api/v1/groups", nil, false)
	var list struct {
		Groups []groupSummaryJSON `json:"groups"`
	}
	decodeJSON(t, w, &list)
	if len(list.Groups) != 0 {
		t.Fatalf("pending group visible publicly: %d", len(list.Groups))
	}

	// Non-admin cannot approve.
	w = leader.do("POST", "/api/v1/admin/groups/1/approve", map[string]any{}, true)
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-admin approve: got %d", w.Code)
	}

	// Admin approves → group goes live.
	admin := loginAPI(t, app, "admin", "pw")
	w = admin.do("GET", "/api/v1/admin/pending", nil, false)
	var pending struct {
		Groups []groupSummaryJSON `json:"groups"`
	}
	decodeJSON(t, w, &pending)
	if len(pending.Groups) != 1 {
		t.Fatalf("pending queue: %d", len(pending.Groups))
	}
	w = admin.do("POST", "/api/v1/admin/groups/1/approve",
		map[string]any{"tags": []string{"outdoors", "fitness"}}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", w.Code, w.Body.String())
	}

	// Now visible publicly.
	w = leader.do("GET", "/api/v1/groups", nil, false)
	decodeJSON(t, w, &list)
	if len(list.Groups) != 1 || list.Groups[0].Status != models.GroupActive {
		t.Fatalf("approved group not listed: %+v", list.Groups)
	}
	if len(list.Groups[0].Tags) != 2 {
		t.Fatalf("tags not saved: %v", list.Groups[0].Tags)
	}
}

func TestJoinLeaveAndRSVP(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	newTestUser(t, app, "sam", "pw", models.RoleMember)

	admin := loginAPI(t, app, "admin", "pw")
	w := admin.do("POST", "/api/v1/groups",
		map[string]any{"name": "Runners", "location": "Park"}, true)
	var created struct {
		Group groupSummaryJSON `json:"group"`
	}
	decodeJSON(t, w, &created)
	gid := itoa(created.Group.ID)
	admin.do("POST", "/api/v1/admin/groups/"+gid+"/approve", map[string]any{}, true)

	sam := loginAPI(t, app, "sam", "pw")

	// Join (idempotent).
	for i := 0; i < 2; i++ {
		w = sam.do("POST", "/api/v1/groups/"+gid+"/join", map[string]any{}, true)
		if w.Code != http.StatusOK {
			t.Fatalf("join: %d %s", w.Code, w.Body.String())
		}
	}
	var n int64
	app.DB.Model(&models.Membership{}).Where("group_id = ?", created.Group.ID).Count(&n)
	if n != 2 { // admin-leader + sam
		t.Fatalf("expected 2 memberships, got %d", n)
	}

	// Leader creates a weekly meeting.
	start := time.Date(2026, 10, 14, 18, 30, 0, 0, time.UTC) // a Wednesday
	w = admin.do("POST", "/api/v1/groups/"+gid+"/meetings", map[string]any{
		"startsAt":       start.Format(time.RFC3339),
		"endsAt":         start.Add(90 * time.Minute).Format(time.RFC3339),
		"recurrenceRule": "FREQ=WEEKLY;BYDAY=WE",
	}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create meeting: %d %s", w.Code, w.Body.String())
	}
	var mcreated struct {
		Meeting meetingJSON `json:"meeting"`
	}
	decodeJSON(t, w, &mcreated)
	mid := itoa(mcreated.Meeting.ID)

	// Expanded list shows 4 weekly instances.
	w = sam.do("GET", "/api/v1/groups/"+gid+"/meetings?from=2026-10-06&to=2026-11-06", nil, false)
	var mlist struct {
		Meetings []meetingInstanceJSON `json:"meetings"`
	}
	decodeJSON(t, w, &mlist)
	if len(mlist.Meetings) != 4 {
		t.Fatalf("expected 4 instances, got %d", len(mlist.Meetings))
	}
	first := mlist.Meetings[0]

	// Sam RSVPs going.
	w = sam.do("PUT", "/api/v1/meetings/"+mid+"/rsvp", map[string]any{
		"status": "going", "date": first.Date,
	}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("rsvp: %d %s", w.Code, w.Body.String())
	}

	// Counts reflect it.
	w = sam.do("GET", "/api/v1/groups/"+gid+"/meetings?from=2026-10-06&to=2026-11-06", nil, false)
	decodeJSON(t, w, &mlist)
	if mlist.Meetings[0].RSVPs["going"] != 1 {
		t.Fatalf("rsvp count: %+v", mlist.Meetings[0].RSVPs)
	}
	if mlist.Meetings[0].MyRSVP == nil || *mlist.Meetings[0].MyRSVP != "going" {
		t.Fatalf("myRsvp missing: %+v", mlist.Meetings[0].MyRSVP)
	}

	// Cancel one instance → it disappears.
	w = admin.do("POST", "/api/v1/meetings/"+mid+"/cancel",
		map[string]any{"date": first.Date}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	w = sam.do("GET", "/api/v1/groups/"+gid+"/meetings?from=2026-10-06&to=2026-11-06", nil, false)
	decodeJSON(t, w, &mlist)
	if len(mlist.Meetings) != 3 {
		t.Fatalf("expected 3 after cancel, got %d", len(mlist.Meetings))
	}

	// Non-leader cannot create meetings.
	w = sam.do("POST", "/api/v1/groups/"+gid+"/meetings", map[string]any{
		"startsAt": start.Format(time.RFC3339),
		"endsAt":   start.Add(time.Hour).Format(time.RFC3339),
	}, true)
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-leader create meeting: %d", w.Code)
	}

	// Leave.
	w = sam.do("POST", "/api/v1/groups/"+gid+"/leave", map[string]any{}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("leave: %d", w.Code)
	}
	app.DB.Model(&models.Membership{}).Where("group_id = ?", created.Group.ID).Count(&n)
	if n != 1 {
		t.Fatalf("expected 1 membership after leave, got %d", n)
	}
}

func TestAnonymousGroupCreateRejected(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	c := newAPIClient(t, app)
	c.fetchCSRF() // real clients always hold a token; auth is checked after CSRF
	w := c.do("POST", "/api/v1/groups", map[string]any{"name": "X"}, true)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anon create: got %d", w.Code)
	}
}

func itoa(u uint) string {
	if u < 10 {
		return string(rune('0' + u))
	}
	var b [8]byte
	i := len(b)
	for u > 0 {
		i--
		b[i] = byte('0' + u%10)
		u /= 10
	}
	return string(b[i:])
}

// newTestGroup creates a group led (through its membership, C12) by leader.
func newTestGroup(t *testing.T, app *App, leader *models.User, name, status string) *models.Group {
	t.Helper()
	g := models.Group{Name: name, LeaderID: leader.ID, Status: status, Visibility: models.VisibilityPublic}
	if err := app.DB.Create(&g).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := app.DB.Create(&models.Membership{UserID: leader.ID, GroupID: g.ID,
		Role: models.MembershipLeader, Status: "active", JoinedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatalf("create leader membership: %v", err)
	}
	return &g
}
