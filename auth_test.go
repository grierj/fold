package main

import (
	"crypto/sha256"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"church-groups/models"

	"github.com/glebarez/sqlite"
	"github.com/gorilla/sessions"
	"gorm.io/gorm"
)

func testConfig() Config {
	secret := []byte("test-secret-32-bytes-long-xxxxxxxx")
	sum := sha256.Sum256(secret)
	return Config{
		Env:           "test",
		Port:          "0",
		SessionSecret: secret,
		CSRFKey:       sum[:],
		SQLitePath:    "file::memory:?cache=shared",
		CookieSecure:  false,
	}
}

// newTestApp builds the full router against an in-memory SQLite DB.
func newTestApp(t *testing.T) *App {
	t.Helper()
	cfg := testConfig()
	db, err := gorm.Open(sqlite.Open(cfg.SQLitePath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&models.User{}, &models.Group{}, &models.Setting{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store := sessions.NewCookieStore(cfg.SessionSecret)
	store.Options = &sessions.Options{Path: "/", HttpOnly: true, Secure: false, SameSite: http.SameSiteLaxMode}
	app := &App{DB: db, Store: store, Config: cfg}
	t.Cleanup(func() { sqlDB.Close() })
	return app
}

var csrfTokenRe = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)

// testClient is an httptest-backed client that keeps cookies and does not
// follow redirects (so tests can assert on 3xx responses).
type testClient struct {
	t      *testing.T
	server *httptest.Server
	http   *http.Client
}

func newTestClient(t *testing.T, app *App) *testClient {
	t.Helper()
	srv := httptest.NewServer(app.setupRouter())
	jar, _ := cookiejar.New(nil)
	c := &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	t.Cleanup(srv.Close)
	return &testClient{t: t, server: srv, http: c}
}

func (c *testClient) get(path string) *http.Response {
	resp, err := c.http.Get(c.server.URL + path)
	if err != nil {
		c.t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

// getCSRF fetches a form page and returns its CSRF token (cookies are kept
// in the jar automatically).
func (c *testClient) getCSRF(path string) string {
	resp := c.get(path)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	m := csrfTokenRe.FindSubmatch(body)
	if m == nil {
		c.t.Fatalf("no CSRF token found on %s", path)
	}
	return string(m[1])
}

func (c *testClient) postForm(path string, form url.Values) *http.Response {
	resp, err := c.http.PostForm(c.server.URL+path, form)
	if err != nil {
		c.t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

func createUser(t *testing.T, app *App, username, password string, admin bool) {
	t.Helper()
	u := models.User{Username: username, Password: password, IsAdmin: admin}
	if err := app.DB.Create(&u).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
}

func login(t *testing.T, c *testClient, username, password string) *http.Response {
	t.Helper()
	token := c.getCSRF("/login")
	return c.postForm("/login", url.Values{
		"username":           {username},
		"password":           {password},
		"csrf_token": {token},
	})
}

func TestSetupWizard(t *testing.T) {
	app := newTestApp(t)
	c := newTestClient(t, app)

	// Fresh DB: everything redirects to /setup.
	resp := c.get("/")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("GET / on fresh DB: got %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/setup" {
		t.Fatalf("redirect location = %q, want /setup", loc)
	}
	resp.Body.Close()

	// Complete the wizard.
	token := c.getCSRF("/setup")
	resp = c.postForm("/setup", url.Values{
		"site_name":        {"Grace Church"},
		"username":         {"pastor"},
		"password":         {"supersecret123"},
		"password_confirm": {"supersecret123"},
		"csrf_token": {token},
	})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /setup: got %d, want 303", resp.StatusCode)
	}
	resp.Body.Close()

	// Wizard is now closed.
	resp = c.get("/setup")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("GET /setup after setup: got %d, want 303", resp.StatusCode)
	}
	resp.Body.Close()

	// Admin can log in with the new credentials.
	resp = login(t, c, "pastor", "supersecret123")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login after setup: got %d, want 303", resp.StatusCode)
	}
	resp.Body.Close()

	// Site name is stored and used.
	var s models.Setting
	if err := app.DB.Where("key = ?", "site_name").First(&s).Error; err != nil {
		t.Fatalf("site_name setting: %v", err)
	}
	if s.Value != "Grace Church" {
		t.Fatalf("site_name = %q, want %q", s.Value, "Grace Church")
	}
}

func TestSetupValidation(t *testing.T) {
	app := newTestApp(t)
	c := newTestClient(t, app)

	token := c.getCSRF("/setup")
	resp := c.postForm("/setup", url.Values{
		"site_name":          {"Grace Church"},
		"username":           {"pastor"},
		"password":           {"short"},
		"password_confirm":   {"short"},
		"csrf_token": {token},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("short password: got %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestLoginSuccessAndFailure(t *testing.T) {
	app := newTestApp(t)
	c := newTestClient(t, app)
	createUser(t, app, "alice", "correct-horse-123", false)

	resp := login(t, c, "alice", "correct-horse-123")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("valid login: got %d, want 303", resp.StatusCode)
	}
	resp.Body.Close()

	// Authenticated requests now reach protected routes.
	resp = c.get("/create")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /create after login: got %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	// Wrong password is rejected.
	c2 := newTestClient(t, app)
	resp = login(t, c2, "alice", "wrong-password")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("invalid login: got %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestAuthMiddlewareRedirectsAnonymous(t *testing.T) {
	app := newTestApp(t)
	c := newTestClient(t, app)
	createUser(t, app, "alice", "correct-horse-123", false)

	for _, path := range []string{"/create", "/group/1", "/admin/"} {
		resp := c.get(path)
		if resp.StatusCode != http.StatusSeeOther {
			t.Errorf("GET %s anonymous: got %d, want 303", path, resp.StatusCode)
		}
		if loc := resp.Header.Get("Location"); loc != "/login" {
			t.Errorf("GET %s: redirect = %q, want /login", path, loc)
		}
		resp.Body.Close()
	}
}

func TestAdminMiddlewareForbidsNonAdmin(t *testing.T) {
	app := newTestApp(t)
	c := newTestClient(t, app)
	createUser(t, app, "alice", "correct-horse-123", false)

	resp := login(t, c, "alice", "correct-horse-123")
	resp.Body.Close()

	resp = c.get("/admin/")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin GET /admin/: got %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestCSRFRejectsMissingToken(t *testing.T) {
	app := newTestApp(t)
	c := newTestClient(t, app)
	createUser(t, app, "alice", "correct-horse-123", false)

	// POST without the CSRF token must be rejected even with valid credentials.
	resp := c.postForm("/login", url.Values{
		"username": {"alice"},
		"password": {"correct-horse-123"},
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("POST without CSRF token: got %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestPasswordIsHashed(t *testing.T) {
	app := newTestApp(t)
	createUser(t, app, "alice", "correct-horse-123", false)

	var u models.User
	if err := app.DB.Where("username = ?", "alice").First(&u).Error; err != nil {
		t.Fatalf("find user: %v", err)
	}
	if u.Password == "correct-horse-123" {
		t.Fatal("password stored in plaintext")
	}
	if !strings.HasPrefix(u.Password, "$2a$") {
		t.Fatalf("password does not look bcrypt-hashed: %.10s...", u.Password)
	}
}
