package main

import (
	"fold/models"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestShortSessionSecretRefusedOutsideDev(t *testing.T) {
	t.Setenv("APP_ENV", "prod")
	t.Setenv("SESSION_SECRET", "changeme")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "at least 32") {
		t.Fatalf("short secret: %v", err)
	}
}

func TestShortSecretMessageIsActionableAndHidesTheValue(t *testing.T) {
	t.Setenv("APP_ENV", "prod")
	t.Setenv("SESSION_SECRET", "hunter2-secret")
	_, err := LoadConfig()
	msg := err.Error()
	for _, want := range []string{"14 characters", "at least 32", "openssl rand -base64 48", ".env", "docker compose up -d"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, "hunter2") || strings.Contains(msg, "\n") {
		t.Errorf("message leaks the value or spans lines: %s", msg)
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
