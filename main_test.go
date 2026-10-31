package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/gob"
	"fmt"
	"fold/internal/providers"
	"fold/models"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// --- tests ---

func TestSessionCookieExpiresOnServerAfterSevenDays(t *testing.T) {
	secret := []byte("test-session-secret-at-least-32-bytes")
	store := newSessionStore(secret, true)
	if store.Options.MaxAge != 7*86400 || !store.Options.Secure || !store.Options.HttpOnly || store.Options.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie options: %+v", store.Options)
	}
	// Sign historical cookies using securecookie's documented wire format.
	// A valid six-day cookie proves failure isn't a bad signature or payload.
	for _, days := range []int{6, 8} {
		var payload bytes.Buffer
		if err := gob.NewEncoder(&payload).Encode(map[interface{}]interface{}{"user_id": uint(1)}); err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf("%d|%s", time.Now().Add(-time.Duration(days)*24*time.Hour).Unix(), base64.URLEncoding.EncodeToString(payload.Bytes()))
		mac := hmac.New(sha256.New, secret)
		mac.Write([]byte(sessionName + "|" + body))
		value := base64.URLEncoding.EncodeToString(append([]byte(body+"|"), mac.Sum(nil)...))
		req := httptest.NewRequest("GET", "/", nil)
		req.AddCookie(&http.Cookie{Name: sessionName, Value: value})
		session, err := store.Get(req, sessionName)
		if days == 6 && (err != nil || session.IsNew) {
			t.Fatalf("six-day cookie rejected: %v", err)
		}
		if days == 8 && (err == nil || !strings.Contains(err.Error(), "expired timestamp")) {
			t.Fatalf("eight-day cookie should expire, got %v", err)
		}
	}
}

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
		"csrf_token": {m[1]}, "age_band": {"adult"},
		"site_name": {"Fold Test"}, "timezone": {"America/Denver"},
		"name": {"Root"}, "email": {"Root@Example.com"},
		"password": {"pw12345678"}, "password_confirm": {"pw12345678"},
	}
	for _, band := range []string{"", "teen", "under13"} {
		form.Set("age_band", band)
		rejected := httptest.NewRecorder()
		request := httptest.NewRequest("POST", "/setup", strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for _, cookie := range getW.Result().Cookies() {
			request.AddCookie(cookie)
		}
		r.ServeHTTP(rejected, request)
		if rejected.Code != http.StatusBadRequest {
			t.Fatalf("setup age %q: %d", band, rejected.Code)
		}
		var stored int64
		app.DB.Model(&models.User{}).Count(&stored)
		if stored != 0 {
			t.Fatal("setup stored a non-adult account")
		}
	}
	form.Set("age_band", "adult")
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
	var tz models.Setting
	app.DB.Where("key = ?", "timezone").First(&tz)
	if tz.Value != "America/Denver" {
		t.Fatalf("timezone not stored: %q", tz.Value)
	}
}

func TestHealthz(t *testing.T) {
	app := testApp(t)
	w := httptest.NewRecorder()
	app.setupRouter().ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("healthz: got %d", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("healthz body: %q", w.Body.String())
	}
}

func TestOversizedBodyRefused(t *testing.T) {
	app := testApp(t)
	c := newAPIClient(t, app)
	big := map[string]any{"email": strings.Repeat("a", maxRequestBytes), "password": "x"}
	if w := c.do("POST", "/api/v1/auth/login", big, true); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized login: %d", w.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	for _, secure := range []bool{false, true} {
		app := testApp(t)
		app.Config.CookieSecure = secure
		r := app.setupRouter()
		for _, path := range []string{"/api/v1/auth/csrf", "/", "/groups/1", "/setup"} {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
			h := w.Header()
			csp := h.Get("Content-Security-Policy")
			if !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "frame-ancestors 'self'") ||
				!strings.Contains(csp, "object-src 'none'") || strings.Contains(csp, "unsafe") {
				t.Fatalf("%s CSP: %q", path, csp)
			}
			for k, v := range map[string]string{
				"X-Content-Type-Options": "nosniff",
				"Referrer-Policy":        "same-origin",
				"X-Frame-Options":        "SAMEORIGIN",
				"Permissions-Policy":     "camera=(), microphone=(), geolocation=()",
			} {
				if h.Get(k) != v {
					t.Fatalf("%s %s: %q", path, k, h.Get(k))
				}
			}
			if hsts := h.Get("Strict-Transport-Security"); (hsts != "") != secure {
				t.Fatalf("%s HSTS with secure=%v: %q", path, secure, hsts)
			}
		}
	}
}

// The setup and outbox pages link the SPA's CSS bundle from index.html.
func TestBuiltStylesheetReadsTheViteLink(t *testing.T) {
	fsys := fstest.MapFS{"web/dist/index.html": {Data: []byte(`<head><script type="module" crossorigin src="/assets/index-a1.js"></script>
<link rel="stylesheet" crossorigin href="/assets/index-B5rK.css"></head>`)}}
	if got := builtStylesheet(fsys); got != "/assets/index-B5rK.css" {
		t.Errorf("got %q", got)
	}
	if got := builtStylesheet(fstest.MapFS{}); got != "" {
		t.Errorf("unbuilt: got %q", got)
	}
}

// The setup page names its language and links the built CSS (no
// /static/style.css any more), and a fresh install can load that CSS.
func TestSetupPageUsesBuiltStylesheet(t *testing.T) {
	app := testApp(t)
	r := app.setupRouter()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/setup", nil))
	body := w.Body.String()
	if !strings.Contains(body, `<html lang="en">`) {
		t.Error("setup page has no lang")
	}
	if strings.Contains(body, "/static/") {
		t.Error("setup page still links /static/")
	}
	css := builtStylesheet(distFS)
	if css == "" {
		t.Skip("web/dist not built")
	}
	if !strings.Contains(body, `href="`+css+`"`) {
		t.Errorf("setup page does not link %s", css)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", css, nil))
	if w.Code != 200 {
		t.Errorf("GET %s before setup: %d", css, w.Code)
	}
}

func TestMemberHelpIsEmbedded(t *testing.T) {
	router := gin.New()
	router.GET("/help/members", serveMemberHelp)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/help/members", nil))
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("help response %d %v", w.Code, w.Header())
	}
	source, err := os.ReadFile("docs/MEMBERS.md")
	if err != nil || !bytes.Equal(source, w.Body.Bytes()) {
		t.Fatalf("help differs from canonical document: %v", err)
	}
	for _, heading := range []string{"## Share an email", "## Create a separate account", "## Teen accounts and consent", "## Birthdays"} {
		if !strings.Contains(w.Body.String(), heading) {
			t.Fatalf("missing member guidance section %q", heading)
		}
	}
}

// The email provider is a configuration choice (M3.2): the registry builds
// whichever adapter the environment names, and a broken one stops the
// server instead of dropping mail.
func TestChooseMailer(t *testing.T) {
	env := func(kv ...string) func(string) string {
		m := map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return func(k string) string { return m[k] }
	}
	prod := Config{Env: "prod", EmailFrom: "Fold <noreply@example.com>"}
	for _, tc := range []struct {
		get  func(string) string
		want string
	}{
		{env(), "none"},
		{env("FOLD_POSTMARK_SERVER_TOKEN", "tok"), "postmark"},
		{env("FOLD_EMAIL_PROVIDER", "smtp", "FOLD_POSTMARK_SERVER_TOKEN", "tok"), "smtp"},
		{env(), "smtp"}, // with cfg.SMTPURL below
	} {
		cfg := prod
		if tc.want == "smtp" {
			cfg.SMTPURL = "smtp://localhost:25" // Config, not the environment, holds it
		}
		s, err := chooseMailer(cfg, nil, tc.get)
		if err != nil {
			t.Fatalf("%s: %v", tc.want, err)
		}
		if got := s.(providers.EmailSender).Capabilities().Name; got != tc.want {
			t.Errorf("chose %s, want %s", got, tc.want)
		}
	}
	for _, get := range []func(string) string{env("FOLD_EMAIL_PROVIDER", "postmark"), env("FOLD_EMAIL_PROVIDER", "pigeon"), env("FOLD_EMAIL_PROVIDER", "smtp")} {
		if _, err := chooseMailer(prod, nil, get); err == nil {
			t.Error("a provider without its settings should be refused")
		}
	}
}
