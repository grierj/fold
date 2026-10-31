package main

import (
	"fold/models"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestComposePassesTransactionalMailEnvironment(t *testing.T) {
	data, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	_, app, ok := strings.Cut(string(data), "\n  app:\n")
	if !ok {
		t.Fatal("no app service")
	}
	app, _, _ = strings.Cut(app, "\n  caddy:\n")
	if !strings.Contains(app, "\n      FOLD_SMTP_ALLOW_INSECURE: ${FOLD_SMTP_ALLOW_INSECURE:-false}\n") {
		t.Error("app service must pass through the explicit SMTP plaintext opt-out")
	}
	for _, key := range []string{"FOLD_SMTP_URL", "FOLD_EMAIL_FROM", "FOLD_EMAIL_PROVIDER", "FOLD_POSTMARK_SERVER_TOKEN",
		"FOLD_POSTMARK_TRANSACTIONAL_STREAM", "FOLD_POSTMARK_BROADCAST_STREAM", "FOLD_POSTMARK_WEBHOOK_SECRET"} {
		if !strings.Contains(app, "\n      "+key+": ${"+key+":-}\n") {
			t.Errorf("app service must pass through %s", key)
		}
	}
}

func TestSMTPInsecureRequiresExplicitOptOut(t *testing.T) {
	t.Setenv("APP_ENV", "prod")
	t.Setenv("SESSION_SECRET", "test-session-secret-at-least-32-bytes")
	t.Setenv("DATABASE_URL", "postgres://unused")
	for _, value := range []string{"", "false", "true", "invalid"} {
		t.Setenv("FOLD_SMTP_ALLOW_INSECURE", value)
		cfg, err := LoadConfig()
		if err != nil || cfg.SMTPAllowInsecure != (value == "true") {
			t.Fatalf("%q: config=%+v err=%v", value, cfg, err)
		}
	}
}

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

func TestJobWorkerSettings(t *testing.T) {
	t.Setenv("APP_ENV", "prod")
	t.Setenv("SESSION_SECRET", "test-session-secret-at-least-32-bytes")
	t.Setenv("DATABASE_URL", "postgres://unused")
	cfg, err := LoadConfig()
	if err != nil || cfg.Workers != 2 || cfg.JobPoll != time.Second {
		t.Fatalf("defaults: %+v %v", cfg, err)
	}
	t.Setenv("FOLD_WORKERS", "0")
	t.Setenv("FOLD_JOB_POLL", "2s")
	if cfg, err := LoadConfig(); err != nil || cfg.Workers != 0 || cfg.JobPoll != 2*time.Second {
		t.Fatalf("set: %+v %v", cfg, err)
	}
	for env, bad := range map[string]string{"FOLD_WORKERS": "-1", "FOLD_JOB_POLL": "forever"} {
		t.Setenv(env, bad)
		if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), env) {
			t.Fatalf("%s=%s: %v", env, bad, err)
		}
		t.Setenv(env, "")
	}
}
