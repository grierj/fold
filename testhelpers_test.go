package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"fold/internal/audit"
	"fold/internal/mail"
	"fold/internal/notify"
	"fold/internal/testdb"
	"fold/models"
	"fold/storage"
	"image"
	"image/png"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func loginAs(t *testing.T, app *App, name, password string) *apiClient {
	t.Helper()
	return loginAPI(t, app, name, password)
}

// loginAPIExpectFail reports whether login succeeded (it shouldn't).
func loginAPIExpectFail(t *testing.T, app *App, name, password string) bool {
	t.Helper()
	c := newAPIClient(t, app)
	c.fetchCSRF()
	w := c.do("POST", "/api/v1/auth/login",
		map[string]string{"email": testEmail(name), "password": password}, true)
	return w.Code == http.StatusOK
}

func itoa2(i uint) string {
	return uintToStringTest(i)
}

func uintToStringTest(u uint) string {
	if u == 0 {
		return "0"
	}
	var b [32]byte
	i := len(b)
	for u > 0 {
		i--
		b[i] = byte('0' + u%10)
		u /= 10
	}
	return string(b[i:])
}

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
	store := newSessionStore([]byte("test-secret"), false)
	uploads, err := storage.NewLocalStore(t.TempDir(), "/uploads")
	if err != nil {
		t.Fatalf("upload store: %v", err)
	}
	return &App{
		DB:      db,
		Store:   store,
		Config:  Config{Env: "dev", Port: "8080", SessionSecret: []byte("test-session-secret-at-least-32-bytes")},
		Uploads: uploads,
		Mailer:  mail.NewDevOutbox(db, "Fold <noreply@example.com>"),
		// Inline, so a test can read the outbox right after a request.
		background: func(f func()) { f() },
	}
}

// queryCounter counts GORM reads (Query and Row callbacks: Find, First,
// Pluck, Scan, ...) on one database handle. Writes are not counted.
type queryCounter struct {
	db      *gorm.DB
	name    string
	reads   atomic.Int64
	stopped atomic.Bool
}

var queryCounterSeq atomic.Uint64

// countDBReads registers a query counter on db and removes it when the
// test ends. Call Stop before asserting on rows the counted code wrote,
// so the assertion queries are not counted.
func countDBReads(t *testing.T, db *gorm.DB) *queryCounter {
	t.Helper()
	qc := &queryCounter{db: db, name: fmt.Sprintf("test:count_db_reads:%d", queryCounterSeq.Add(1))}
	count := func(*gorm.DB) { qc.reads.Add(1) }
	if err := db.Callback().Query().Before("gorm:query").Register(qc.name, count); err != nil {
		t.Fatalf("register query counter: %v", err)
	}
	if err := db.Callback().Row().Before("gorm:row").Register(qc.name, count); err != nil {
		qc.Stop()
		t.Fatalf("register row counter: %v", err)
	}
	t.Cleanup(qc.Stop)
	return qc
}

// Reads is how many read callbacks ran since the counter was registered.
func (qc *queryCounter) Reads() int64 { return qc.reads.Load() }

// Stop removes the counter's callbacks. It is safe to call twice.
func (qc *queryCounter) Stop() {
	if qc.stopped.Swap(true) {
		return
	}
	_ = qc.db.Callback().Query().Remove(qc.name)
	_ = qc.db.Callback().Row().Remove(qc.name)
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
		req.RemoteAddr = net.JoinHostPort(c.ip, "1234")
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

func activeGroup(t *testing.T, app *App, leader *models.User, name, visibility, tags string) models.Group {
	t.Helper()
	g := models.Group{
		Name: name, LeaderID: leader.ID, Status: models.GroupActive,
		Visibility: visibility, Tags: tags,
	}
	if err := app.DB.Create(&g).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Create(&models.Membership{
		UserID: leader.ID, GroupID: g.ID,
		Role: models.MembershipLeader, Status: "active", JoinedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	return g
}

func groupNames(t *testing.T, w *httptest.ResponseRecorder) []string {
	t.Helper()
	var body struct {
		Groups []groupSummaryJSON `json:"groups"`
	}
	decodeJSON(t, w, &body)
	names := make([]string, 0, len(body.Groups))
	for _, g := range body.Groups {
		names = append(names, g.Name)
	}
	return names
}

func containsName(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

func addMember(t *testing.T, app *App, u *models.User, g *models.Group, role string) {
	t.Helper()
	if err := app.DB.Create(&models.Membership{UserID: u.ID, GroupID: g.ID, Role: role, Status: "active"}).Error; err != nil {
		t.Fatal(err)
	}
}

func roleChangeRows(t *testing.T, app *App) []models.AuditLog {
	t.Helper()
	var rows []models.AuditLog
	if err := app.DB.Where("action = ?", audit.RoleChanged).Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

func notificationTitles(t *testing.T, c *apiClient) []string {
	t.Helper()
	var out []string
	for _, n := range feed(t, c, "") {
		kind := n.Kind
		if kind == "group.leadership_changed" {
			kind = "leadership"
		}
		out = append(out, kind+": "+n.Title)
	}
	return out
}

// M0.3a: log in by email (C16, DEC-23), throttling in Postgres, no account
// enumeration, and sessions ended on password, email, role or status changes.

func tryLogin(t *testing.T, app *App, ip, email, password string) (int, string) {
	t.Helper()
	c := newAPIClient(t, app)
	c.ip = ip
	c.fetchCSRF()
	w := c.do("POST", "/api/v1/auth/login", map[string]string{"email": email, "password": password}, true)
	return w.Code, w.Body.String()
}

// DEC-39: an optional group member limit.

func setLimit(t *testing.T, c *apiClient, g *models.Group, limit any) {
	t.Helper()
	if w := c.do("PUT", "/api/v1/groups/"+itoa(g.ID), map[string]any{"memberLimit": limit}, true); w.Code != http.StatusOK {
		t.Fatalf("set limit %v: %d %s", limit, w.Code, w.Body.String())
	}
}

func activeCount(app *App, g *models.Group) int64 { return memberCount(app.DB, g.ID) }

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// M0.3b: leaders remove members (DEC-35), sole-leader handoff (DEC-36),
// security headers and dependency scanning (DEC-37).

func departureConfirmBody(t *testing.T, c *apiClient, previewPath string) map[string]any {
	t.Helper()
	w := c.do("GET", previewPath, nil, false)
	if w.Code != http.StatusOK {
		t.Fatalf("departure preview %s: %d %s", previewPath, w.Code, w.Body.String())
	}
	var response struct {
		Departure departurePreviewJSON `json:"departure"`
	}
	decodeJSON(t, w, &response)
	keep := make([]uint, 0, response.Departure.DefaultKeepCount)
	for _, date := range response.Departure.Dates {
		if date.DefaultKeep {
			keep = append(keep, date.OccurrenceID)
		}
	}
	return map[string]any{"snapshot": response.Departure.Snapshot, "keepOccurrenceIds": keep}
}

func confirmDeparture(t *testing.T, c *apiClient, method, mutationPath, previewPath string, note string) *httptest.ResponseRecorder {
	t.Helper()
	body := departureConfirmBody(t, c, previewPath)
	if note != "" {
		body["note"] = note
	}
	return c.do(method, mutationPath, body, true)
}

type auditMeta struct {
	UserID          uint   `json:"userId"`
	OldRole         string `json:"oldRole"`
	NewRole         string `json:"newRole"`
	Note            string `json:"note"`
	Reason          string `json:"reason"`
	DepartingUserID uint   `json:"departingUserId"`
	Status          string `json:"status"`
	Limit           int    `json:"limit"`
	SelfConfirm     bool   `json:"selfConfirm"`
}

func auditRows(t *testing.T, app *App, action string) ([]models.AuditLog, []auditMeta) {
	t.Helper()
	var rows []models.AuditLog
	if err := app.DB.Where("action = ?", action).Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	metas := make([]auditMeta, len(rows))
	for i, r := range rows {
		if err := json.Unmarshal([]byte(r.MetaJSON), &metas[i]); err != nil {
			t.Fatal(err)
		}
	}
	return rows, metas
}

func membershipStatus(app *App, u *models.User, g *models.Group) string {
	var m models.Membership
	if app.DB.Where("user_id = ? AND group_id = ?", u.ID, g.ID).Limit(1).Find(&m).RowsAffected == 0 {
		return "none"
	}
	return m.Status
}

// seedEvent inserts a one-time UTC event and its occurrence. Direct inserts
// have to set CreatedBy (the foreign key) and a scheduled occurrence, or
// registrations and the notification feed see nothing.
func seedEvent(t *testing.T, app *App, g *models.Group, createdBy uint, start time.Time) (models.Event, models.EventOccurrence) {
	t.Helper()
	start = start.UTC()
	gid := g.ID
	vis := g.Visibility
	if vis == "" {
		vis = models.VisibilityPublic
	}
	ev := models.Event{
		GroupID: &gid, Visibility: vis,
		StartsLocal:     start.Format("2006-01-02T15:04"),
		DurationMinutes: 60, TZID: "UTC", Location: "", CreatedBy: createdBy,
	}
	if err := app.DB.Create(&ev).Error; err != nil {
		t.Fatalf("seed event: %v", err)
	}
	occ := models.EventOccurrence{
		EventID: ev.ID, OriginalDate: start.Format("2006-01-02"),
		StartsAt: start, EndsAt: start.Add(time.Hour),
		Status: models.OccurrenceScheduled,
	}
	if err := app.DB.Create(&occ).Error; err != nil {
		t.Fatalf("seed occurrence: %v", err)
	}
	return ev, occ
}

func openGroup(t *testing.T, app *App, name string) (*apiClient, string) {
	t.Helper()
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	w := admin.do("POST", "/api/v1/groups", map[string]any{"name": name, "location": "Hall"}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create group: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Group groupSummaryJSON `json:"group"`
	}
	decodeJSON(t, w, &created)
	return admin, itoa(created.Group.ID)
}

func listOccurrences(t *testing.T, c *apiClient, gid, from, to string) []occurrenceJSON {
	t.Helper()
	w := c.do("GET", "/api/v1/groups/"+gid+"/events?from="+from+"&to="+to, nil, false)
	if w.Code != http.StatusOK {
		t.Fatalf("list events: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Events []occurrenceJSON `json:"events"`
	}
	decodeJSON(t, w, &body)
	return body.Events
}

func occurrenceOn(t *testing.T, list []occurrenceJSON, date string) occurrenceJSON {
	t.Helper()
	for _, o := range list {
		if o.Date == date {
			return o
		}
	}
	t.Fatalf("no occurrence on %s (%d in the list)", date, len(list))
	return occurrenceJSON{}
}

func weeklyRule(start time.Time) string {
	days := []string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}
	return "FREQ=WEEKLY;BYDAY=" + days[start.UTC().Weekday()]
}

func atUTC(t time.Time, hour, min int) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), hour, min, 0, 0, time.UTC)
}

func apiDo(t *testing.T, c *apiClient, method, path string, body any, want int) string {
	t.Helper()
	w := c.do(method, path, body, method != "GET")
	if w.Code != want {
		t.Fatalf("%s %s: %d %s, want %d", method, path, w.Code, w.Body.String(), want)
	}
	return w.Body.String()
}

func createGroupAs(t *testing.T, c *apiClient, name, visibility string) uint {
	t.Helper()
	w := c.do("POST", "/api/v1/groups", map[string]any{
		"name": name, "visibility": visibility, "location": "Hall",
	}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create group: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Group groupSummaryJSON `json:"group"`
	}
	decodeJSON(t, w, &created)
	return created.Group.ID
}

func addMemberEmail(t *testing.T, c *apiClient, gid uint, email string) uint {
	t.Helper()
	w := c.do("POST", "/api/v1/groups/"+itoa(gid)+"/members", map[string]any{"email": email}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("add member: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Membership struct {
			UserID uint `json:"userId"`
		} `json:"membership"`
	}
	decodeJSON(t, w, &body)
	return body.Membership.UserID
}

func scheduleGroupEvent(t *testing.T, c *apiClient, gid uint, title string) eventJSON {
	t.Helper()
	w := c.do("POST", "/api/v1/groups/"+itoa(gid)+"/events", map[string]any{
		"title": title, "startsLocal": "2026-12-01T19:00", "durationMinutes": 60, "tzid": "UTC",
	}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create event: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Event eventJSON `json:"event"`
	}
	decodeJSON(t, w, &created)
	return created.Event
}

func setVisibility(t *testing.T, c *apiClient, eventID uint, visibility, reason string, want int) string {
	t.Helper()
	return apiDo(t, c, "PUT", fmt.Sprintf("/api/v1/events/%d/visibility", eventID), map[string]any{
		"visibility": visibility, "reason": reason,
	}, want)
}

type eventPage struct {
	Event            eventJSON        `json:"event"`
	Occurrences      []occurrenceJSON `json:"occurrences"`
	CanManage        bool             `json:"canManage"`
	CanSetVisibility bool             `json:"canSetVisibility"`
}

func getEvent(t *testing.T, c *apiClient, id uint, want int) (eventPage, string) {
	t.Helper()
	w := c.do("GET", fmt.Sprintf("/api/v1/events/%d", id), nil, false)
	if w.Code != want {
		t.Fatalf("get event: %d %s, want %d", w.Code, w.Body.String(), want)
	}
	var page eventPage
	if want == http.StatusOK {
		decodeJSON(t, w, &page)
	}
	return page, w.Body.String()
}

func noticeIDs(t *testing.T, c *apiClient) []notificationJSON {
	t.Helper()
	return feed(t, c, "")
}

func hasVisibilityNotice(ns []notificationJSON, titlePart, reason string) bool {
	for _, n := range ns {
		if n.Kind == "event.visibility_override" && strings.Contains(n.Title, titlePart) && strings.Contains(n.Body, reason) {
			return true
		}
	}
	return false
}

// DEC-48 tightening, the leader prompt, the leader-opened marker, and
// cancel-and-notify.

func register(t *testing.T, c *apiClient, eventID uint) {
	t.Helper()
	apiDo(t, c, "PUT", fmt.Sprintf("/api/v1/events/%d/registrations", eventID), map[string]any{
		"status": "going", "date": "2026-12-01",
	}, http.StatusOK)
}

func regCount(app *App, u *models.User, eventID uint) int64 {
	var n int64
	app.DB.Table("registrations").
		Joins("JOIN event_occurrences ON event_occurrences.id = registrations.occurrence_id").
		Where("registrations.user_id = ? AND event_occurrences.event_id = ?", u.ID, eventID).Count(&n)
	return n
}

func eventRow(t *testing.T, app *App, id uint) models.Event {
	t.Helper()
	var ev models.Event
	if err := app.DB.First(&ev, id).Error; err != nil {
		t.Fatal(err)
	}
	return ev
}

// feed returns the user's feed for filter: open tasks first, then items.
func feed(t *testing.T, c *apiClient, filter string) []notificationJSON {
	t.Helper()
	w := c.do("GET", "/api/v1/notifications?filter="+filter, nil, false)
	if w.Code != http.StatusOK {
		t.Fatalf("notifications: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Waiting       []notificationJSON `json:"waiting"`
		Notifications []notificationJSON `json:"notifications"`
	}
	decodeJSON(t, w, &body)
	return append(body.Waiting, body.Notifications...)
}

func badge(t *testing.T, c *apiClient, etag string) (code int, unread, waiting int64, tag string) {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/notifications/badge", nil)
	for _, ck := range c.jar {
		req.AddCookie(ck)
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	w := httptest.NewRecorder()
	c.router.ServeHTTP(w, req)
	var body struct{ Unread, Waiting int64 }
	if w.Code == http.StatusOK {
		decodeJSON(t, w, &body)
	}
	return w.Code, body.Unread, body.Waiting, w.Header().Get("ETag")
}

func findKind(ns []notificationJSON, kind string) *notificationJSON {
	for i := range ns {
		if ns[i].Kind == kind {
			return &ns[i]
		}
	}
	return nil
}

func devOutbox(t *testing.T, app *App) *mail.DevOutbox {
	t.Helper()
	ob, ok := app.Mailer.(*mail.DevOutbox)
	if !ok {
		t.Fatal("test app has no devoutbox mailer")
	}
	return ob
}

func latestEmail(t *testing.T, app *App, to string) mail.OutboxMessage {
	t.Helper()
	m, err := devOutbox(t, app).LatestTo(to)
	if err != nil {
		t.Fatalf("no email to %s: %v", to, err)
	}
	return m
}

var tokenRe = regexp.MustCompile(`token=([A-Za-z0-9_-]+)`)
var manageTokenRe = regexp.MustCompile(`/guest/manage/([A-Za-z0-9_-]+)`)

func tokenInEmail(t *testing.T, m mail.OutboxMessage) string {
	t.Helper()
	mt := tokenRe.FindStringSubmatch(m.Body)
	if len(mt) != 2 {
		t.Fatalf("no token in email to %s: %q", m.To, m.Body)
	}
	return mt[1]
}

// manageTokenInEmail pulls the manage token from a /guest/manage/<token>
// link in an email body.
func manageTokenInEmail(t *testing.T, m mail.OutboxMessage) string {
	t.Helper()
	mt := manageTokenRe.FindStringSubmatch(m.Body)
	if len(mt) != 2 {
		t.Fatalf("no manage token in email to %s: %q", m.To, m.Body)
	}
	return mt[1]
}

func auditCount(t *testing.T, app *App, action string) int64 {
	t.Helper()
	var n int64
	if err := app.DB.Model(&models.AuditLog{}).Where("action = ?", action).Count(&n).Error; err != nil {
		t.Fatalf("audit count: %v", err)
	}
	return n
}

func anonClient(t *testing.T, app *App, ip string) *apiClient {
	t.Helper()
	c := newAPIClient(t, app)
	c.ip = ip
	c.fetchCSRF()
	return c
}

func signupBody(name, email, band, birth string) map[string]any {
	body := map[string]any{"name": name, "email": email, "ageBand": band}
	if birth != "" {
		body["birthDate"] = birth
	}
	return body
}

func pendingRequest(t *testing.T, app *App, email string) models.AccountRequest {
	t.Helper()
	var req models.AccountRequest
	if err := app.DB.Where("email = ? AND status = ?", email, models.RequestPending).First(&req).Error; err != nil {
		t.Fatalf("pending request for %s: %v", email, err)
	}
	return req
}

func requestCount(t *testing.T, app *App, email string) int64 {
	t.Helper()
	var n int64
	app.DB.Model(&models.AccountRequest{}).Where("email = ?", email).Count(&n)
	return n
}

func adminNotification(t *testing.T, app *App, adminID, requestID uint) models.Notification {
	t.Helper()
	var n models.Notification
	if err := app.DB.Where("user_id = ? AND kind = ? AND subject_id = ?", adminID, notify.AccountRequested, requestID).
		First(&n).Error; err != nil {
		t.Fatalf("admin notification for request %d: %v", requestID, err)
	}
	return n
}

func teenBirthDate() string {
	return time.Now().UTC().AddDate(-15, 0, 0).Format("2006-01-02")
}

// tinyPNG returns a valid 1x1 PNG for upload tests.
func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func (c *apiClient) doMultipart(method, path, field, filename string, data []byte) *httptest.ResponseRecorder {
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	fw, err := w.CreateFormFile(field, filename)
	if err != nil {
		c.t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write(data); err != nil {
		c.t.Fatalf("write form file: %v", err)
	}
	if err := w.Close(); err != nil {
		c.t.Fatalf("close writer: %v", err)
	}
	req := httptest.NewRequest(method, path, &b)
	req.Header.Set("Content-Type", w.FormDataContentType())
	for _, ck := range c.jar {
		req.AddCookie(ck)
	}
	req.Header.Set("X-CSRF-Token", c.token)
	rec := httptest.NewRecorder()
	c.router.ServeHTTP(rec, req)
	for _, ck := range rec.Result().Cookies() {
		c.jar[ck.Name] = ck
	}
	return rec
}

func countSiteNameReads(t *testing.T, db *gorm.DB) *int {
	t.Helper()
	n := new(int)
	name := fmt.Sprintf("test:site_name_reads:%d", queryCounterSeq.Add(1))
	if err := db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "settings" {
			for _, v := range tx.Statement.Vars {
				if v == "site_name" {
					*n++
					break
				}
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(name) })
	return n
}
