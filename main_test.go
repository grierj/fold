package main

import (
	"fold/models"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

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
		"site_name":  {"Fold Test"}, "timezone": {"America/Denver"},
		"name": {"Root"}, "email": {"Root@Example.com"},
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
