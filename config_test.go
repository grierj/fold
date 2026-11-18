package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fold/models"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
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

// TestDangerousConfigRefusedAtStartup: a trust-everyone proxy list, an http
// SITE_URL and COOKIE_SECURE=false make the server unsafe outside dev. Each
// refusal names the variable and the fix, and none echoes SESSION_SECRET.
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

// composeService is the part of a resolved Compose service these tests read.
type composeService struct {
	Image       string             `json:"image"`
	Build       *composeBuild      `json:"build"`
	Environment map[string]*string `json:"environment"`
	Ports       []json.RawMessage  `json:"ports"`
	Volumes     []composeMount     `json:"volumes"`
	User        string             `json:"user"`
	CapDrop     []string           `json:"cap_drop"`
	CapAdd      []string           `json:"cap_add"`
	Privileged  bool               `json:"privileged"`
	Healthcheck struct {
		Test []string `json:"test"`
	} `json:"healthcheck"`
	DependsOn map[string]struct {
		Condition string `json:"condition"`
	} `json:"depends_on"`
}

type composeBuild struct {
	Context string `json:"context"`
}

type composeMount struct {
	Type     string `json:"type"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only"`
}

type composeResolved struct {
	Services map[string]composeService  `json:"services"`
	Volumes  map[string]json.RawMessage `json:"volumes"`
}

func (s composeService) env(key string) string {
	if v := s.Environment[key]; v != nil {
		return *v
	}
	return ""
}

func (s composeService) mount(target string) (composeMount, bool) {
	for _, m := range s.Volumes {
		if m.Target == target {
			return m, true
		}
	}
	return composeMount{}, false
}

func (s composeService) mountsSource(source string) bool {
	for _, m := range s.Volumes {
		if m.Source == source {
			return true
		}
	}
	return false
}

// composeFixtureEnv is the .env a deployment would have, with every optional
// name left out. Tests add to it.
func composeFixtureEnv() map[string]string {
	return map[string]string{
		"DOMAIN":         "groups.example.org",
		"SESSION_SECRET": "fixture-session-secret-not-a-real-one",
		"DB_PASSWORD":    "fixture-db-password",
		"FOLD_VERSION":   "0.5.0",
	}
}

// resolveCompose renders the shipped Compose file (plus overrides) through
// `docker compose config` with only the fixture environment, so nothing from
// the developer's shell or a project .env can change the answer. It skips when
// Docker Compose is not installed; rendering needs no daemon.
func resolveCompose(t *testing.T, env map[string]string, overrides ...string) composeResolved {
	t.Helper()
	return resolveComposeFiles(t, env, append([]string{"docker-compose.yml"}, overrides...)...)
}

func resolveComposeFiles(t *testing.T, env map[string]string, files ...string) composeResolved {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not installed")
	}
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		t.Skip("docker compose is not available")
	}
	envFile := filepath.Join(t.TempDir(), "env")
	var lines bytes.Buffer
	for k, v := range env {
		lines.WriteString(k + "=" + v + "\n")
	}
	if err := os.WriteFile(envFile, lines.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"compose"}
	for _, f := range files {
		abs, err := filepath.Abs(f)
		if err != nil {
			t.Fatal(err)
		}
		args = append(args, "-f", abs)
	}
	args = append(args, "--env-file", envFile, "config", "--format", "json")
	cmd := exec.Command("docker", args...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	for _, k := range []string{"XDG_RUNTIME_DIR", "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG"} {
		if v, ok := os.LookupEnv(k); ok {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("docker compose config: %v\n%s", err, stderr.String())
	}
	var cfg composeResolved
	if err := json.Unmarshal(out.Bytes(), &cfg); err != nil {
		t.Fatalf("parsing resolved Compose: %v", err)
	}
	return cfg
}

func TestComposeBackupImageSelection(t *testing.T) {
	env := composeFixtureEnv()
	if got := resolveCompose(t, env).Services["backup"].Image; got != "ghcr.io/grierj/fold-backup:0.5.0" {
		t.Errorf("without FOLD_BACKUP_VERSION the backup image follows the app: %q", got)
	}
	env["FOLD_BACKUP_VERSION"] = "0.6.1"
	cfg := resolveCompose(t, env)
	if got := cfg.Services["backup"].Image; got != "ghcr.io/grierj/fold-backup:0.6.1" {
		t.Errorf("FOLD_BACKUP_VERSION must pin the backup image on its own: %q", got)
	}
	if got := cfg.Services["app"].Image; got != "ghcr.io/grierj/fold:0.5.0" {
		t.Errorf("FOLD_BACKUP_VERSION must not move the app: %q", got)
	}
	for _, svc := range []string{"app", "backup"} {
		if cfg.Services[svc].Build != nil {
			t.Errorf("the shipped file must not build %s", svc)
		}
	}
}

func TestComposeBuildOverrideBuildsBackupLocally(t *testing.T) {
	env := composeFixtureEnv()
	env["FOLD_VERSION"] = "dev"
	cfg := resolveCompose(t, env, "docker-compose.build.yml")
	backup := cfg.Services["backup"]
	if backup.Build == nil || !strings.HasSuffix(filepath.ToSlash(backup.Build.Context), "/deploy/backup") {
		t.Fatalf("the build override must build the backup image from deploy/backup: %+v", backup.Build)
	}
	if backup.Image != "ghcr.io/grierj/fold-backup:dev" {
		t.Errorf("the local backup image is named for FOLD_VERSION: %q", backup.Image)
	}
	if cfg.Services["app"].Build == nil {
		t.Error("the build override must still build the app")
	}
	if cfg.Services["db"].Build != nil || cfg.Services["caddy"].Build != nil {
		t.Error("only app and backup are built from source")
	}
}

func TestComposePassesBackupEnvironment(t *testing.T) {
	want := map[string]string{
		"FOLD_BACKUP_TZ":            "America/Denver",
		"FOLD_BACKUP_KEEP_DAYS":     "30",
		"FOLD_BACKUP_S3_BUCKET":     "church-fold",
		"FOLD_BACKUP_S3_ENDPOINT":   "https://s3.us-east-005.backblazeb2.com",
		"FOLD_BACKUP_S3_REGION":     "us-east-005",
		"AWS_ACCESS_KEY_ID":         "fixture-key-id",
		"AWS_SECRET_ACCESS_KEY":     "fixture-secret-key",
		"FOLD_BACKUP_AGE_RECIPIENT": "age1fixturerecipient",
	}
	env := composeFixtureEnv()
	for k, v := range want {
		env[k] = v
	}
	backup := resolveCompose(t, env).Services["backup"]
	for k, v := range want {
		if got := backup.env(k); got != v {
			t.Errorf("backup %s = %q, want %q", k, got, v)
		}
	}
	if got := backup.env("PGPASSWORD"); got != "fixture-db-password" {
		t.Errorf("backup must use the database password from .env: %q", got)
	}

	// Left out of .env, every optional setting reaches the service empty, so
	// backup.sh applies its own defaults (UTC, 14 days, no offsite).
	bare := resolveCompose(t, composeFixtureEnv()).Services["backup"]
	for k := range want {
		if v, ok := bare.Environment[k]; !ok || (v != nil && *v != "") {
			t.Errorf("unset %s must reach backup empty, got %v (present %v)", k, v, ok)
		}
	}

	// The private age identity never reaches any service, even if an operator
	// pastes it into .env.
	env["FOLD_BACKUP_AGE_IDENTITY"] = "AGE-SECRET-KEY-1FIXTURE"
	for name, svc := range resolveCompose(t, env).Services {
		for k, v := range svc.Environment {
			if strings.Contains(strings.ToUpper(k), "IDENTITY") || (v != nil && strings.Contains(*v, "AGE-SECRET-KEY")) {
				t.Errorf("service %s carries a private age key through %s", name, k)
			}
		}
	}
}

func TestComposeVolumeBoundaries(t *testing.T) {
	cfg := resolveCompose(t, composeFixtureEnv())
	app, backup := cfg.Services["app"], cfg.Services["backup"]

	if m, ok := backup.mount("/data"); !ok || m.Source != "uploads" || !m.ReadOnly {
		t.Errorf("backup must mount uploads read-only at /data: %+v", m)
	}
	if m, ok := backup.mount("/backups"); !ok || m.Source != "backups" || m.ReadOnly {
		t.Errorf("backup must write the backups volume at /backups: %+v", m)
	}
	if m, ok := backup.mount("/backup-status"); !ok || m.Source != "backup-status" || m.ReadOnly {
		t.Errorf("backup must write the backup-status volume: %+v", m)
	}
	if m, ok := app.mount("/data"); !ok || m.Source != "uploads" || m.ReadOnly {
		t.Errorf("the app keeps writing uploads at /data: %+v", m)
	}
	if m, ok := app.mount("/backup-status"); !ok || m.Source != "backup-status" || !m.ReadOnly {
		t.Errorf("the app may only read the backup status: %+v", m)
	}
	if got := app.env("FOLD_BACKUP_DIR"); got != "/backup-status" {
		t.Errorf("app FOLD_BACKUP_DIR = %q", got)
	}
	if app.mountsSource("backups") {
		t.Error("the app must never mount the archives")
	}
	for _, name := range []string{"db", "caddy"} {
		for _, v := range []string{"backups", "backup-status"} {
			if cfg.Services[name].mountsSource(v) {
				t.Errorf("%s must not mount %s", name, v)
			}
		}
	}
	for _, v := range []string{"backups", "backup-status", "backup-coord", "uploads"} {
		if _, ok := cfg.Volumes[v]; !ok {
			t.Errorf("volume %s is not declared", v)
		}
	}
}

func TestComposePublishesNoNewPorts(t *testing.T) {
	cfg := resolveCompose(t, composeFixtureEnv())
	for name, svc := range cfg.Services {
		if name == "caddy" {
			if len(svc.Ports) != 2 {
				t.Errorf("caddy publishes exactly 80 and 443, got %d ports", len(svc.Ports))
			}
			continue
		}
		if len(svc.Ports) != 0 {
			t.Errorf("service %s must publish no ports", name)
		}
	}
}

func TestComposeBackupCoordination(t *testing.T) {
	unset := resolveCompose(t, composeFixtureEnv()).Services["backup"]
	if got := unset.env("FOLD_RECOVERY_DIR"); got != "" {
		t.Errorf("with FOLD_BACKUP_COORD unset FOLD_RECOVERY_DIR must be empty, got %q", got)
	}
	m, ok := unset.mount("/coord")
	if !ok || m.Type != "volume" || m.Source != "backup-coord" || !m.ReadOnly {
		t.Errorf("unset, /coord is the empty read-only backup-coord volume: %+v", m)
	}

	env := composeFixtureEnv()
	env["FOLD_BACKUP_COORD"] = "/opt/fold"
	set := resolveCompose(t, env).Services["backup"]
	if got := set.env("FOLD_RECOVERY_DIR"); got != "/coord" {
		t.Errorf("with FOLD_BACKUP_COORD set FOLD_RECOVERY_DIR must be /coord, got %q", got)
	}
	m, ok = set.mount("/coord")
	if !ok || m.Type != "bind" || m.Source != "/opt/fold" || !m.ReadOnly {
		t.Errorf("set, /coord is a read-only bind of the deploy directory: %+v", m)
	}
	if set.mountsSource("backup-coord") {
		t.Error("set, the placeholder volume is not mounted")
	}
}

func TestComposeBackupServiceRuns(t *testing.T) {
	backup := resolveCompose(t, composeFixtureEnv()).Services["backup"]
	// The host keeps its own copies of older archives in a root-only folder of
	// the backups volume; the service must be an explicit unprivileged identity
	// with every capability dropped (provision.sh refuses a resolved override).
	if backup.User != "65532:65532" {
		t.Errorf("the backup service runs as 65532:65532, got %q", backup.User)
	}
	if len(backup.CapDrop) != 1 || backup.CapDrop[0] != "ALL" || len(backup.CapAdd) != 0 || backup.Privileged {
		t.Errorf("the backup service drops every capability and adds none: drop %v add %v privileged %v", backup.CapDrop, backup.CapAdd, backup.Privileged)
	}
	if got := strings.Join(backup.Healthcheck.Test, " "); !strings.Contains(got, "/usr/local/bin/backup.sh health") {
		t.Errorf("backup healthcheck must run backup.sh health: %q", got)
	}
	if backup.DependsOn["db"].Condition != "service_healthy" {
		t.Errorf("backup must wait for a healthy database: %+v", backup.DependsOn)
	}
}

// The recovery service extends app, so it inherits the read-only status mount
// and must not gain the archives.
func TestComposeRecoveryServiceKeepsBackupBoundaries(t *testing.T) {
	// restore.sh copies both files into the deployment directory, because the
	// override extends docker-compose.yml beside it.
	dir := t.TempDir()
	for _, f := range []string{"docker-compose.yml", "deploy/digitalocean/compose.recovery.yml"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, filepath.Base(f)), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	env := composeFixtureEnv()
	env["FOLD_RECOVERY_IMAGE"] = "fold-recovery-fixture"
	env["FOLD_RECOVERY_NONCE"] = strings.Repeat("a", 32)
	env["COMPOSE_PROFILES"] = "recovery"
	cfg := resolveComposeFiles(t, env, filepath.Join(dir, "docker-compose.yml"), filepath.Join(dir, "compose.recovery.yml"))
	rec, ok := cfg.Services["recovery"]
	if !ok {
		t.Fatal("the recovery service did not render")
	}
	if rec.mountsSource("backups") {
		t.Error("recovery must not mount the archives")
	}
	if m, ok := rec.mount("/backup-status"); ok && !m.ReadOnly {
		t.Error("recovery may only read the backup status")
	}
}

func TestComposeBackupSourceContracts(t *testing.T) {
	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	compose := read("docker-compose.yml")
	_, backup, ok := strings.Cut(compose, "\n  backup:\n")
	if !ok {
		t.Fatal("no backup service")
	}
	backup, _, _ = strings.Cut(backup, "\n# ")
	_, caddyAndAfter, _ := strings.Cut(compose, "\n  caddy:\n")
	if !strings.Contains(caddyAndAfter, "\n  backup:\n") {
		t.Error("the backup service comes after caddy")
	}
	for _, want := range []string{
		"image: ghcr.io/grierj/fold-backup:${FOLD_BACKUP_VERSION:-${FOLD_VERSION}}",
		"restart: unless-stopped",
		`user: "65532:65532"`,
		"cap_drop:\n      - ALL",
		"FOLD_RECOVERY_DIR: ${FOLD_BACKUP_COORD:+/coord}",
		"${FOLD_BACKUP_COORD:-backup-coord}:/coord:ro",
	} {
		if !strings.Contains(backup, want) {
			t.Errorf("backup service lacks %q", want)
		}
	}
	if strings.Contains(backup, "\n    ports:") || strings.Contains(backup, "\n    build:") {
		t.Error("the shipped backup service publishes nothing and builds nothing")
	}
	if !strings.Contains(read("docker-compose.build.yml"), "\n  backup:\n    build:\n      context: deploy/backup\n") {
		t.Error("docker-compose.build.yml must build the backup image from deploy/backup")
	}

	// The archives volume root is root:65532 mode 1770, so the nonroot service
	// writes its own files and cannot touch what the host keeps there.
	backupImage := read("deploy/backup/Dockerfile")
	for _, want := range []string{"chown 0:65532 /backups", "chmod 1770 /backups"} {
		if !strings.Contains(backupImage, want) {
			t.Errorf("the backup image lacks %q", want)
		}
	}
	if strings.Contains(backupImage, "chmod 700 /backups") || strings.Contains(backupImage, "chown 65532:65532 /backups") {
		t.Error("/backups must not be a service-owned folder")
	}

	// A fresh named volume copies its first mount's image folder, so both
	// images pre-create what they share with the same owner.
	dockerfile := read("Dockerfile")
	for _, want := range []string{"mkdir -p /data/uploads /backup-status", "COPY --from=build --chown=65532:65532 /backup-status /backup-status"} {
		if !strings.Contains(dockerfile, want) {
			t.Errorf("the app Dockerfile lacks %q", want)
		}
	}
	if strings.Contains(dockerfile, "VOLUME /backup-status") {
		t.Error("/backup-status is a named volume, never an anonymous one")
	}

	envExample := read(".env.example")
	for _, key := range []string{"FOLD_BACKUP_TZ", "FOLD_BACKUP_KEEP_DAYS", "FOLD_BACKUP_VERSION", "FOLD_BACKUP_COORD",
		"FOLD_BACKUP_S3_BUCKET", "FOLD_BACKUP_S3_ENDPOINT", "FOLD_BACKUP_S3_REGION", "AWS_ACCESS_KEY_ID",
		"AWS_SECRET_ACCESS_KEY", "FOLD_BACKUP_AGE_RECIPIENT"} {
		if !strings.Contains(envExample, "\n# "+key+"=") {
			t.Errorf(".env.example must document %s (commented out)", key)
		}
	}
	for _, want := range []string{"all six or\n# none", "Keep the matching private key", "r2.cloudflarestorage.com", "backblazeb2.com"} {
		if !strings.Contains(envExample, want) {
			t.Errorf(".env.example lacks %q", want)
		}
	}
}

// freshVolumeName returns a volume name no other invocation shares: concurrent
// checkouts and subtests use one Docker daemon, so a fixed name would let one
// run remove or reuse another's volume mid-proof.
func freshVolumeName(t *testing.T, role string) string {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("random volume name: %v", err)
	}
	return "fold-test-" + role + "-" + hex.EncodeToString(b)
}

func TestFreshVolumeNamesAreUniquePerCall(t *testing.T) {
	seen := map[string]bool{}
	for _, role := range []string{"uploads", "status"} {
		for range 50 {
			n := freshVolumeName(t, role)
			if seen[n] {
				t.Fatalf("volume name %s was handed out twice", n)
			}
			seen[n] = true
			if !strings.HasPrefix(n, "fold-test-"+role+"-") || n == "fold-test-"+role+"-app-first" || n == "fold-test-"+role+"-backup-first" {
				t.Errorf("volume name %s is fixed or mislabelled", n)
			}
		}
	}
	// Subtests of one run get distinct names too.
	var a, b string
	t.Run("one", func(t *testing.T) { a = freshVolumeName(t, "uploads") })
	t.Run("two", func(t *testing.T) { b = freshVolumeName(t, "uploads") })
	if a == b {
		t.Errorf("two subtests share volume name %s", a)
	}
}

// Fresh named volumes copy the first container's image folder. Whichever of the
// app or the backup service initializes uploads and backup-status first, uid
// 65532 must be able to write both. Needs built images, so it runs only when
// the two are named (scripts/compose-smoke.sh builds them):
//
//	FOLD_TEST_APP_IMAGE=ghcr.io/grierj/fold:ci FOLD_TEST_BACKUP_IMAGE=ghcr.io/grierj/fold-backup:ci \
//	  go test -run TestFreshVolumesAreWritableByTheAppUser .
func TestFreshVolumesAreWritableByTheAppUser(t *testing.T) {
	appImage, backupImage := os.Getenv("FOLD_TEST_APP_IMAGE"), os.Getenv("FOLD_TEST_BACKUP_IMAGE")
	if appImage == "" || backupImage == "" {
		t.Skip("set FOLD_TEST_APP_IMAGE and FOLD_TEST_BACKUP_IMAGE to run the fresh-volume proof")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not installed")
	}
	docker := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	// The app mount set (uploads read-write, status read-only) and the backup
	// mount set (uploads read-only, status read-write), as Compose declares them.
	runApp := func(uploads, status string) {
		docker("run", "--rm", "-v", uploads+":/data", "-v", status+":/backup-status:ro", appImage, "version")
	}
	runBackup := func(uploads, status string) {
		docker("run", "--rm", "-v", uploads+":/data:ro", "-v", status+":/backup-status", "--entrypoint", "/bin/sh",
			backupImage, "-c", "test -d /data/uploads && touch /backup-status/probe && rm /backup-status/probe")
	}
	for _, order := range []string{"app first", "backup first"} {
		t.Run(order, func(t *testing.T) {
			uploads, status := freshVolumeName(t, "uploads"), freshVolumeName(t, "status")
			for _, v := range []string{uploads, status} {
				// A random name that already exists is someone else's volume
				// (or a collision); refuse it rather than reuse or delete it.
				if err := exec.Command("docker", "volume", "inspect", v).Run(); err == nil {
					t.Fatalf("volume %s already exists; refusing to touch it", v)
				}
				docker("volume", "create", v)
				// Registered only after our own create succeeded, so cleanup
				// removes nothing this subtest did not make.
				t.Cleanup(func() { _ = exec.Command("docker", "volume", "rm", "-f", v).Run() })
			}
			if order == "app first" {
				runApp(uploads, status)
				runBackup(uploads, status)
			} else {
				runBackup(uploads, status)
				runApp(uploads, status)
			}
			// Uid 65532 (the backup image's user, the app's too) must own and
			// write both folders.
			out := docker("run", "--rm", "-v", uploads+":/data", "-v", status+":/backup-status", "--entrypoint", "/bin/sh",
				backupImage, "-c", "id -u; stat -c '%u:%g' /data/uploads /backup-status; touch /data/uploads/probe /backup-status/probe && echo writable")
			if want := "65532\n65532:65532\n65532:65532\nwritable"; out != want {
				t.Errorf("fresh volume ownership or writability wrong:\n%s", out)
			}
			// The archives volume root is root:65532 mode 1770 whichever image first mounts it.
			backups := freshVolumeName(t, "backups")
			if err := exec.Command("docker", "volume", "inspect", backups).Run(); err == nil {
				t.Fatalf("volume %s already exists; refusing to touch it", backups)
			}
			docker("volume", "create", backups)
			t.Cleanup(func() { _ = exec.Command("docker", "volume", "rm", "-f", backups).Run() })
			got := docker("run", "--rm", "-v", backups+":/backups", "--entrypoint", "/bin/sh", backupImage, "-c",
				"stat -c '%u:%g:%a' /backups; touch /backups/probe && rm /backups/probe && echo writable")
			if want := "0:65532:1770\nwritable"; got != want {
				t.Errorf("the archives volume root is wrong:\n%s", got)
			}
		})
	}
}

// FOLD_BACKUP_DIR is the read-only status directory (M5.2): empty is fine
// (unwatched), a relative path is refused with a message that names the
// variable and not its value.
func TestBackupDirConfiguration(t *testing.T) {
	t.Setenv("APP_ENV", "prod")
	t.Setenv("SESSION_SECRET", "test-session-secret-at-least-32-bytes")
	t.Setenv("DATABASE_URL", "postgres://unused")
	t.Setenv("FOLD_BACKUP_DIR", "")
	if cfg, err := LoadConfig(); err != nil || cfg.BackupDir != "" {
		t.Fatalf("unset: %+v %v", cfg, err)
	}
	t.Setenv("FOLD_BACKUP_DIR", "/backup-status")
	if cfg, err := LoadConfig(); err != nil || cfg.BackupDir != "/backup-status" {
		t.Fatalf("absolute: %+v %v", cfg, err)
	}
	for _, v := range []string{"relative-SENTINEL", "./backup-SENTINEL", "../status", " /x"} {
		t.Setenv("FOLD_BACKUP_DIR", v)
		_, err := LoadConfig()
		if err == nil || !strings.Contains(err.Error(), "FOLD_BACKUP_DIR") || strings.Contains(err.Error(), v) {
			t.Fatalf("%q: want a value-free refusal, got %v", v, err)
		}
	}
}
