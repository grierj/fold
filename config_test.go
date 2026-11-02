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

// C10 (M5.1): the shipped Compose file pulls a pinned release and never builds;
// the build lives in the developer override.
func TestShippedComposePullsAPinnedImage(t *testing.T) {
	data, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	_, app, ok := strings.Cut(string(data), "\n  app:\n")
	if !ok {
		t.Fatal("no app service")
	}
	app, _, _ = strings.Cut(app, "\n  caddy:\n")
	if !strings.Contains(app, "\n    image: ghcr.io/grierj/fold:${FOLD_VERSION") {
		t.Error("app service must pull ghcr.io/grierj/fold:${FOLD_VERSION...}")
	}
	if !strings.Contains(app, "${FOLD_VERSION:?") {
		t.Error("FOLD_VERSION must be required, so Compose never guesses a release")
	}
	if strings.Contains(app, "\n    build:") {
		t.Error("the shipped compose file must not build; put build: . in docker-compose.build.yml")
	}
	for _, line := range strings.Split(app, "\n") {
		if strings.Contains(line, "image:") && strings.Contains(line, "latest") {
			t.Errorf("app image must not float on latest: %q", line)
		}
	}

	build, err := os.ReadFile("docker-compose.build.yml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(build), "\n    build: .\n") {
		t.Error("docker-compose.build.yml must carry the app's build: .")
	}

	env, err := os.ReadFile(".env.example")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(env), "\nFOLD_VERSION=\n") {
		t.Error(".env.example must list FOLD_VERSION= for operators to fill in")
	}
	if strings.Contains(string(env), "FOLD_VERSION=latest") {
		t.Error(".env.example must not suggest a floating tag")
	}
}

func TestSetupTokenConfiguration(t *testing.T) {
	t.Setenv("APP_ENV", "prod")
	t.Setenv("SESSION_SECRET", "test-session-secret-at-least-32-bytes")
	t.Setenv("DATABASE_URL", "postgres://unused")
	for _, value := range []string{"short", strings.Repeat("z", 64), strings.Repeat("a", 63)} {
		t.Setenv("FOLD_SETUP_TOKEN", value)
		if _, err := LoadConfig(); err == nil || strings.Contains(err.Error(), value) {
			t.Fatalf("invalid setup token should fail without exposing it: %v", err)
		}
	}
	want := strings.Repeat("a", 64)
	t.Setenv("FOLD_SETUP_TOKEN", want)
	if cfg, err := LoadConfig(); err != nil || cfg.SetupToken != want {
		t.Fatalf("valid setup token rejected: %v", err)
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

func TestWebhookSecretMustBeLong(t *testing.T) {
	t.Setenv("APP_ENV", "prod")
	t.Setenv("SESSION_SECRET", "test-session-secret-at-least-32-bytes")
	t.Setenv("DATABASE_URL", "postgres://unused")
	t.Setenv("FOLD_POSTMARK_WEBHOOK_SECRET", "secret123")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "at least 24") {
		t.Fatalf("short secret: %v", err)
	}
	t.Setenv("FOLD_POSTMARK_WEBHOOK_SECRET", strings.Repeat("a1", 12))
	if cfg, err := LoadConfig(); err != nil || cfg.PostmarkWebhookSecret == "" {
		t.Fatalf("24 characters: %v", err)
	}
}

// TestDangerousConfigRefusedAtStartup: three values that LoadConfig used to
// accept make the server unsafe (SEC1 slice e). Each refusal names the
// variable and the fix, and none echoes SESSION_SECRET.
func TestDangerousConfigRefusedAtStartup(t *testing.T) {
	const secret = "test-session-secret-at-least-32-bytes"
	cases := []struct {
		name    string
		env     string
		vars    map[string]string
		wantVar string // "" means the config must load
		wantFix string
	}{
		{"shipped combination", "prod", map[string]string{
			"COOKIE_SECURE": "true", "SITE_URL": "https://groups.example.org",
			"FOLD_TRUSTED_PROXIES": "172.30.0.0/24"}, "", ""},
		{"nothing set", "prod", nil, "", ""},
		{"bare IPs stay fine", "prod", map[string]string{"FOLD_TRUSTED_PROXIES": "10.0.0.1,::1"}, "", ""},
		{"ipv4 /0 prod", "prod", map[string]string{"FOLD_TRUSTED_PROXIES": "0.0.0.0/0"}, "FOLD_TRUSTED_PROXIES", "172.30.0.0/24"},
		{"ipv4 /0 dev", "dev", map[string]string{"FOLD_TRUSTED_PROXIES": "0.0.0.0/0"}, "FOLD_TRUSTED_PROXIES", "172.30.0.0/24"},
		{"ipv6 /0 prod", "prod", map[string]string{"FOLD_TRUSTED_PROXIES": "::/0"}, "FOLD_TRUSTED_PROXIES", "172.30.0.0/24"},
		{"ipv6 /0 dev", "dev", map[string]string{"FOLD_TRUSTED_PROXIES": "::/0"}, "FOLD_TRUSTED_PROXIES", "172.30.0.0/24"},
		{"/0 hidden in a list", "prod", map[string]string{"FOLD_TRUSTED_PROXIES": "172.30.0.0/24, 0.0.0.0/0"}, "FOLD_TRUSTED_PROXIES", "172.30.0.0/24"},
		{"a /8 is still a choice", "prod", map[string]string{"FOLD_TRUSTED_PROXIES": "10.0.0.0/8"}, "", ""},
		{"http SITE_URL prod", "prod", map[string]string{"SITE_URL": "http://groups.example.org"}, "SITE_URL", "https://"},
		{"hostless SITE_URL prod", "prod", map[string]string{"SITE_URL": "https://"}, "SITE_URL", "https://"},
		{"schemeless SITE_URL prod", "prod", map[string]string{"SITE_URL": "groups.example.org"}, "SITE_URL", "https://"},
		{"http SITE_URL dev", "dev", map[string]string{"SITE_URL": "http://localhost:8080"}, "", ""},
		{"hostless SITE_URL dev", "dev", map[string]string{"SITE_URL": "https://"}, "", ""},
		{"COOKIE_SECURE=false prod", "prod", map[string]string{"COOKIE_SECURE": "false"}, "COOKIE_SECURE", "APP_ENV=dev"},
		{"COOKIE_SECURE=false dev", "dev", map[string]string{"COOKIE_SECURE": "false"}, "", ""},
		{"COOKIE_SECURE=true prod", "prod", map[string]string{"COOKIE_SECURE": "true"}, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("APP_ENV", tc.env)
			t.Setenv("SESSION_SECRET", secret)
			t.Setenv("DATABASE_URL", "postgres://unused")
			for _, k := range []string{"COOKIE_SECURE", "SITE_URL", "FOLD_TRUSTED_PROXIES"} {
				t.Setenv(k, tc.vars[k])
			}
			cfg, err := LoadConfig()
			if tc.wantVar == "" {
				if err != nil {
					t.Fatalf("want it to load, got %v", err)
				}
				if tc.vars["SITE_URL"] != "" && cfg.SiteURL != tc.vars["SITE_URL"] {
					t.Fatalf("SiteURL = %q", cfg.SiteURL)
				}
				return
			}
			if err == nil {
				t.Fatalf("want a refusal naming %s, got config %+v", tc.wantVar, cfg)
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.wantVar) || !strings.Contains(msg, tc.wantFix) {
				t.Errorf("message should name %s and the fix %q: %s", tc.wantVar, tc.wantFix, msg)
			}
			if strings.Contains(msg, secret) || strings.Contains(msg, "\n") {
				t.Errorf("message leaks the secret or spans lines: %s", msg)
			}
		})
	}
}
