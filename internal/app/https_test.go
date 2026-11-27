package app_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"fold/internal/accounts"
	"fold/internal/app"
	"fold/internal/httpx"
	"fold/internal/mail"
	"fold/internal/testenv"
)

// The production shape: an https site behind Caddy. Cookies are __Host- and
// Secure, and a POST is judged by its Origin against SITE_URL.
func TestHTTPSCookiesAndOrigin(t *testing.T) {
	env := testenv.New(t)
	u, _ := url.Parse("https://church.example.org")
	a, err := app.New(app.Config{SiteURL: u, Proxies: httpx.Proxies{}},
		app.Deps{Reader: env.Reader, Exec: env.Exec, Queue: accounts.NewQueue(env.Exec, env.Log), Keys: env.Keys, Clock: env.Clock, Log: env.Log, Outbox: &mail.Outbox{}})
	if err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "https://church.example.org/signin", nil))
	cookie := rec.Header().Get("Set-Cookie")
	if !strings.HasPrefix(cookie, "__Host-csrf=") || !strings.Contains(cookie, "Secure") || !strings.Contains(cookie, "HttpOnly") {
		t.Fatalf("cookie %q", cookie)
	}
	if rec.Header().Get("Strict-Transport-Security") == "" {
		t.Fatal("no HSTS over https")
	}
	tok := regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindStringSubmatch(rec.Body.String())[1]
	post := func(origin string) int {
		form := url.Values{"email": {"pat@example.org"}, "csrf": {tok}}
		req := httptest.NewRequest("POST", "https://church.example.org/signin", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", origin)
		req.Header.Set("Cookie", strings.Split(cookie, ";")[0])
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := post("https://church.example.org"); c != http.StatusOK {
		t.Fatalf("own origin: %d", c)
	}
	if c := post("http://church.example.org"); c != http.StatusForbidden {
		t.Fatalf("plain-http origin: %d", c)
	}
}
