package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"

	"fold/internal/testdb"
	"fold/models"

	"github.com/gin-gonic/gin"
)

func TestServeWithoutDatabaseURLExplainsDevDatabase(t *testing.T) {
	t.Setenv("APP_ENV", "dev")
	t.Setenv("DATABASE_URL", "")
	var stderr bytes.Buffer
	// No arguments means serve.
	if code := run(nil, &bytes.Buffer{}, &stderr); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "deploy/docker-compose.dev.yml") {
		t.Fatalf("stderr = %q, want the start-the-dev-database hint", stderr.String())
	}
}

func TestVersionNeedsNoEnvironment(t *testing.T) {
	for _, k := range []string{"APP_ENV", "SESSION_SECRET", "DATABASE_URL"} {
		t.Setenv(k, "")
	}
	var out bytes.Buffer
	if code := run([]string{"version"}, &out, &out); code != 0 || !strings.HasPrefix(out.String(), "fold ") {
		t.Fatalf("version: code %d, output %q", code, out.String())
	}
	out.Reset()
	if code := run([]string{"version", "--json"}, &out, &out); code != 0 || !json.Valid(out.Bytes()) {
		t.Fatalf("version --json: code %d, output %q", code, out.String())
	}
}

func TestMigrateStatusNeedsOnlyDatabaseURL(t *testing.T) {
	testdb.Shared(t) // make sure DATABASE_URL works before touching env
	url, err := testdb.URL(testdb.Fresh(t, false))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("APP_ENV", "")
	t.Setenv("SESSION_SECRET", "")
	t.Setenv("DATABASE_URL", url)

	var out, errOut bytes.Buffer
	if code := run([]string{"migrate", "status"}, &out, &errOut); code != 0 {
		t.Fatalf("status: code %d, stderr %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "pending") || !strings.Contains(out.String(), "00001_baseline.sql") {
		t.Fatalf("status output = %q", out.String())
	}
	out.Reset()
	if code := run([]string{"migrate", "up"}, &out, &errOut); code != 0 {
		t.Fatalf("up: code %d, stderr %q", code, errOut.String())
	}
	out.Reset()
	if code := run([]string{"migrate", "status", "--json"}, &out, &errOut); code != 0 {
		t.Fatalf("status --json: code %d, stderr %q", code, errOut.String())
	}
	var rows []struct{ State string }
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil || len(rows) == 0 || rows[0].State != "applied" {
		t.Fatalf("status --json = %q (%v)", out.String(), err)
	}
}

func TestParseTrustedProxies(t *testing.T) {
	got, err := parseTrustedProxies(" 172.30.0.0/24, 10.0.0.1 ,")
	if err != nil || !slices.Equal(got, []string{"172.30.0.0/24", "10.0.0.1"}) {
		t.Fatalf("got %v, %v", got, err)
	}
	if _, err := parseTrustedProxies("caddy"); err == nil {
		t.Fatal("a hostname should be rejected")
	}
}

// TestClientIPTrustsOnlyTheProxySubnet fakes Caddy on the compose subnet
// and an outsider who forges X-Forwarded-For.
func TestClientIPTrustsOnlyTheProxySubnet(t *testing.T) {
	app := testApp(t)
	app.Config.TrustedProxies = []string{defaultTrustedProxies}
	newTestUser(t, app, "admin", "password12345", models.RoleAdmin) // get past /setup
	r := app.setupRouter()
	r.GET("/test/ip", func(c *gin.Context) { c.String(http.StatusOK, c.ClientIP()) })

	cases := []struct{ name, remote, xff, want string }{
		{"via proxy", "172.30.0.2:4431", "203.0.113.9", "203.0.113.9"},
		{"spoofed from outside", "198.51.100.7:5555", "203.0.113.9", "198.51.100.7"},
		{"direct, no header", "198.51.100.7:5555", "", "198.51.100.7"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest("GET", "/test/ip", nil)
		req.RemoteAddr = tc.remote
		if tc.xff != "" {
			req.Header.Set("X-Forwarded-For", tc.xff)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if got := w.Body.String(); got != tc.want {
			t.Errorf("%s: ClientIP = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestCSRFExemptRoutesAreExact sends every state-changing route a request
// with no CSRF token. Only the routes in csrfExemptRoutes may get past CSRF.
func TestCSRFExemptRoutesAreExact(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password12345", models.RoleAdmin) // get past /setup
	r := app.setupRouter()
	param := regexp.MustCompile(`[:*][A-Za-z]+`)

	var exempt []string
	for _, rt := range r.Routes() {
		if isSafeMethod(rt.Method) {
			continue
		}
		req := httptest.NewRequest(rt.Method, param.ReplaceAllString(rt.Path, "test"), nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if !strings.Contains(w.Body.String(), "CSRF token invalid") {
			exempt = append(exempt, rt.Method+" "+rt.Path)
		}
	}
	var want []string
	for _, e := range csrfExemptRoutes {
		if strings.Contains(e, " ") {
			want = append(want, e)
			continue
		}
		for _, m := range []string{"POST", "PUT", "PATCH", "DELETE", "CONNECT"} {
			want = append(want, m+" "+e)
		}
	}
	slices.Sort(exempt)
	slices.Sort(want)
	if !slices.Equal(exempt, want) {
		t.Fatalf("routes outside CSRF:\n got %v\nwant %v", exempt, want)
	}

	// The acceptance check in plain words.
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/hooks/test", nil))
	if w.Code == http.StatusForbidden {
		t.Fatal("POST /hooks/test was rejected by CSRF")
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/auth/logout", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("POST /api/v1/auth/logout without a token = %d, want 403", w.Code)
	}
}
