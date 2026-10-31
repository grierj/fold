package main

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds the runtime configuration for `fold serve`, sourced from the
// environment. Other subcommands read only the variables they need (C11).
type Config struct {
	// Env is "dev" or "prod". Defaults to "prod" — the safe choice.
	Env string
	// Port the HTTP server listens on. Default "8080".
	Port string
	// SessionSecret signs session cookies. Required in prod; a loud
	// insecure default is used for dev only.
	SessionSecret []byte
	// DatabaseURL is the Postgres DSN, required everywhere (DEC-1, DEC-24),
	// e.g. "postgres://user:pass@host:5432/dbname?sslmode=disable".
	DatabaseURL string
	// CookieSecure sets the Secure flag on cookies. Default true in prod.
	CookieSecure bool
	// SiteURL is the public base URL (used for CSRF origin checks behind
	// proxies, e.g. "https://groups.example.org").
	SiteURL string
	// UploadDir is where user-uploaded assets (avatars, group covers) are
	// stored by the local blob store. Default "./data/uploads".
	UploadDir string
	// TrustedProxies are the CIDRs or IPs whose X-Forwarded-For is believed
	// (C17). The default is the compose network's fixed subnet.
	TrustedProxies []string
	// SMTPURL configures the minimal SMTP sender (C6, M1.2), e.g.
	// "smtp://user:pass@smtp.example.org:587". Empty means devoutbox in
	// dev, and no sender (sends fail loudly) in prod.
	SMTPURL string
	// SMTPAllowInsecure permits plaintext for a trusted non-loopback relay.
	// Default false; TLS verification is never disabled.
	SMTPAllowInsecure bool
	// EmailFrom is the From address on transactional email.
	EmailFrom string
	// PostmarkWebhookSecret is the password in the URL Postmark posts its
	// webhooks to (FOLD_POSTMARK_WEBHOOK_SECRET, M3.2b). Empty turns the
	// endpoint off (404).
	PostmarkWebhookSecret string
	// Workers is how many background job workers this process runs
	// (FOLD_WORKERS, default 2; 0 turns them off, for debugging only).
	Workers int
	// JobPoll is how often an idle worker looks for due jobs (FOLD_JOB_POLL,
	// default 1s).
	JobPoll time.Duration
}

// minWebhookSecretLen is the shortest FOLD_POSTMARK_WEBHOOK_SECRET accepted.
const minWebhookSecretLen = 24

// defaultTrustedProxies is the fixed subnet of the compose network
// (docker-compose.yml). Only Caddy lives there in production.
const defaultTrustedProxies = "172.30.0.0/24"

// errNoDatabaseURL tells developers how to get a database; there is no
// fallback database.
var errNoDatabaseURL = errors.New("DATABASE_URL is not set. Start the dev database with " +
	"`docker compose -f deploy/docker-compose.dev.yml up -d db` and set " +
	"DATABASE_URL=postgres://fold:fold@localhost:5432/fold?sslmode=disable (`make dev` does both)")

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		log.Printf("WARNING: invalid boolean %s=%q, using default %v", key, v, def)
		return def
	}
	return b
}

// databaseURL returns DATABASE_URL or the start-the-dev-database error.
func databaseURL() (string, error) {
	if v := os.Getenv("DATABASE_URL"); v != "" {
		return v, nil
	}
	return "", errNoDatabaseURL
}

// parseTrustedProxies splits a comma-separated list of CIDRs or IPs and
// rejects anything Gin would not accept.
func parseTrustedProxies(s string) ([]string, error) {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(p); err != nil && net.ParseIP(p) == nil {
			return nil, fmt.Errorf("FOLD_TRUSTED_PROXIES: %q is not an IP or CIDR", p)
		}
		out = append(out, p)
	}
	return out, nil
}

// LoadConfig reads everything `fold serve` needs.
// shortSecretMessage is the one startup error for a short secret: what is
// wrong (its length, never its value), how to fix it, and where to set it.
func shortSecretMessage(n int) string {
	return fmt.Sprintf("SESSION_SECRET is too short: it is %d characters and must be at least %d. "+
		"Fix: generate a new one with `openssl rand -base64 48` and set SESSION_SECRET to it, "+
		"in .env next to docker-compose.yml for Docker Compose (then run `docker compose up -d`), "+
		"or in the environment of `fold serve`. Changing it signs everyone out once.", n, minSessionSecretLen)
}

// minSessionSecretLen is the shortest SESSION_SECRET accepted outside dev.
const minSessionSecretLen = 32

func LoadConfig() (Config, error) {
	env := getEnv("APP_ENV", "prod")
	isDev := env == "dev"

	secret := os.Getenv("SESSION_SECRET")
	if secret == "" {
		if !isDev {
			return Config{}, errors.New("SESSION_SECRET must be set when APP_ENV != dev")
		}
		secret = "dev-only-insecure-secret-do-not-use-in-prod"
		log.Println("WARNING: SESSION_SECRET not set; using insecure dev default")
	} else if !isDev && len(secret) < minSessionSecretLen {
		// A short secret can be guessed offline from one cookie, and then
		// anyone can sign an admin session.
		return Config{}, fmt.Errorf("%s", shortSecretMessage(len(secret)))
	}

	dbURL, err := databaseURL()
	if err != nil {
		return Config{}, err
	}

	proxies, err := parseTrustedProxies(getEnv("FOLD_TRUSTED_PROXIES", defaultTrustedProxies))
	if err != nil {
		return Config{}, err
	}

	// The webhook secret is the only guard on a public endpoint (M3.2b).
	if s := os.Getenv("FOLD_POSTMARK_WEBHOOK_SECRET"); s != "" && len(s) < minWebhookSecretLen {
		return Config{}, fmt.Errorf("FOLD_POSTMARK_WEBHOOK_SECRET must be at least %d characters; make one with `openssl rand -hex 24`", minWebhookSecretLen)
	}
	workers, err := strconv.Atoi(getEnv("FOLD_WORKERS", "2"))
	if err != nil || workers < 0 || workers > 64 {
		return Config{}, fmt.Errorf("FOLD_WORKERS must be a whole number from 0 to 64")
	}
	poll, err := time.ParseDuration(getEnv("FOLD_JOB_POLL", "1s"))
	if err != nil || poll < 100*time.Millisecond || poll > time.Minute {
		return Config{}, fmt.Errorf("FOLD_JOB_POLL must be a duration from 100ms to 1m, for example 1s")
	}

	return Config{
		Env:                   env,
		Port:                  getEnv("PORT", "8080"),
		SessionSecret:         []byte(secret),
		DatabaseURL:           dbURL,
		CookieSecure:          getEnvBool("COOKIE_SECURE", !isDev),
		SiteURL:               os.Getenv("SITE_URL"),
		UploadDir:             getEnv("UPLOAD_DIR", "./data/uploads"),
		TrustedProxies:        proxies,
		SMTPURL:               os.Getenv("FOLD_SMTP_URL"),
		SMTPAllowInsecure:     getEnvBool("FOLD_SMTP_ALLOW_INSECURE", false),
		EmailFrom:             getEnv("FOLD_EMAIL_FROM", "Fold <noreply@localhost>"),
		PostmarkWebhookSecret: os.Getenv("FOLD_POSTMARK_WEBHOOK_SECRET"),
		Workers:               workers,
		JobPoll:               poll,
	}, nil
}

func (c Config) IsDev() bool { return c.Env == "dev" }
