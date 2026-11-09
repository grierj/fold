package main

import (
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"fold/internal/push"
)

// Config holds the runtime configuration for `fold serve`, sourced from the
// environment. Other subcommands read only the variables they need (C11).
type Config struct {
	// Env is "dev" or "prod". Defaults to "prod" — the safe choice.
	Env string
	Port string
	// SessionSecret signs session cookies. Required in prod; a loud
	// insecure default is used for dev only.
	SessionSecret []byte
	// SetupToken is an optional 32-byte hex credential shared by app processes.
	SetupToken string
	// DatabaseURL is the Postgres DSN, required everywhere (DEC-1, DEC-24),
	// e.g. "postgres://user:pass@host:5432/dbname?sslmode=disable".
	DatabaseURL string
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
	// PushHosts are extra web push service hosts (FOLD_PUSH_HOSTS,
	// optional, comma-separated), added to push.DefaultHosts (M3.13).
	PushHosts []string
	// EmailProvider names the mail adapter (FOLD_EMAIL_PROVIDER); empty lets
	// mail.Choose pick by what is configured. The Postmark fields are the
	// server token and the two message streams.
	EmailProvider               string
	PostmarkServerToken         string
	PostmarkTransactionalStream string
	PostmarkBroadcastStream     string
}

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

// defaultPort and defaultEnv are the one place these defaults are written.
const (
	defaultPort = "8080"
	defaultEnv  = "prod"
)

// envName reads APP_ENV; envPort reads PORT; envPushHosts reads FOLD_PUSH_HOSTS.
// LoadConfig and the subcommands that load only part of the configuration (C11)
// call these, so each variable is read in one place.
func envName() string { return getEnv("APP_ENV", defaultEnv) }

func envPort() string { return getEnv("PORT", defaultPort) }

func envPushHosts() ([]string, error) { return push.ParseHosts(os.Getenv("FOLD_PUSH_HOSTS")) }

func databaseURL() (string, error) {
	if v := os.Getenv("DATABASE_URL"); v != "" {
		return v, nil
	}
	return "", errNoDatabaseURL
}

func parseTrustedProxies(s string) ([]string, error) {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		_, network, err := net.ParseCIDR(p)
		if err != nil && net.ParseIP(p) == nil {
			return nil, fmt.Errorf("FOLD_TRUSTED_PROXIES: %q is not an IP or CIDR", p)
		}
		// A /0 makes Gin believe X-Forwarded-For from the whole internet, so
		// every per-IP throttle can be dodged by sending a header.
		if err == nil {
			if ones, _ := network.Mask.Size(); ones == 0 {
				return nil, fmt.Errorf("FOLD_TRUSTED_PROXIES: %q trusts every sender, which lets anyone forge their IP address. "+
					"Fix: list only your reverse proxy's address or subnet (the shipped Compose default is %s), "+
					"in .env next to docker-compose.yml or in the environment of `fold serve`", p, defaultTrustedProxies)
			}
		}
		out = append(out, p)
	}
	return out, nil
}

// isHTTPSURL reports whether s is an https URL with a host. SITE_URL
// (outside dev) and the web push contact claim (vapidSubject) both need it.
func isHTTPSURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != ""
}

const siteURLMessage = "SITE_URL must be an https URL with a host: password-reset and invitation emails link to it, " +
	"and an http:// link sends them in cleartext. Fix: set SITE_URL=https://<your domain> " +
	"(docker-compose.yml builds it from DOMAIN in .env), or remove SITE_URL. Plain-HTTP testing belongs in APP_ENV=dev"

const cookieSecureMessage = "COOKIE_SECURE=false is not allowed when APP_ENV is not dev: session cookies would travel " +
	"without the Secure flag and the HSTS header would be dropped. Fix: remove COOKIE_SECURE or set it to true; " +
	"plain-HTTP testing belongs in APP_ENV=dev"

// shortSecretMessage is the one startup error for a short secret: what is
// wrong (its length, never its value), how to fix it, and where to set it.
func shortSecretMessage(n int) string {
	return fmt.Sprintf("SESSION_SECRET is too short: it is %d characters and must be at least %d. "+
		"Fix: generate a new one with `openssl rand -base64 48` and set SESSION_SECRET to it, "+
		"in .env next to docker-compose.yml for Docker Compose (then run `docker compose up -d`), "+
		"or in the environment of `fold serve`. Changing it signs everyone out once.", n, minSessionSecretLen)
}

const minSessionSecretLen = 32

// sessionSecret reads SESSION_SECRET: required outside dev, at least
// minSessionSecretLen there; dev falls back to a loud insecure default.
// `fold push keys rotate` reads it the same way as `fold serve`.
func sessionSecret(isDev bool) (string, error) {
	secret := os.Getenv("SESSION_SECRET")
	if secret == "" {
		if !isDev {
			return "", errors.New("SESSION_SECRET must be set when APP_ENV != dev")
		}
		log.Println("WARNING: SESSION_SECRET not set; using insecure dev default")
		return "dev-only-insecure-secret-do-not-use-in-prod", nil
	}
	if !isDev && len(secret) < minSessionSecretLen {
		// A short secret can be guessed offline from one cookie, and then
		// anyone can sign an admin session.
		return "", fmt.Errorf("%s", shortSecretMessage(len(secret)))
	}
	return secret, nil
}

// LoadConfig reads everything `fold serve` needs.
func LoadConfig() (Config, error) {
	env := envName()
	isDev := env == "dev"

	secret, err := sessionSecret(isDev)
	if err != nil {
		return Config{}, err
	}
	setupToken := os.Getenv("FOLD_SETUP_TOKEN")
	if setupToken != "" {
		decoded, err := hex.DecodeString(setupToken)
		if err != nil || len(decoded) != 32 {
			return Config{}, errors.New("FOLD_SETUP_TOKEN must be 64 hexadecimal characters; generate one with `openssl rand -hex 32`")
		}
	}

	dbURL, err := databaseURL()
	if err != nil {
		return Config{}, err
	}

	proxies, err := parseTrustedProxies(getEnv("FOLD_TRUSTED_PROXIES", defaultTrustedProxies))
	if err != nil {
		return Config{}, err
	}

	siteURL := os.Getenv("SITE_URL")
	if !isDev && siteURL != "" && !isHTTPSURL(siteURL) {
		return Config{}, errors.New(siteURLMessage)
	}
	cookieSecure := getEnvBool("COOKIE_SECURE", !isDev)
	if !isDev && !cookieSecure {
		return Config{}, errors.New(cookieSecureMessage)
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
	pushHosts, err := envPushHosts()
	if err != nil {
		return Config{}, err
	}

	return Config{
		Env:                         env,
		Port:                        envPort(),
		SessionSecret:               []byte(secret),
		SetupToken:                  setupToken,
		DatabaseURL:                 dbURL,
		CookieSecure:                cookieSecure,
		SiteURL:                     siteURL,
		UploadDir:                   getEnv("UPLOAD_DIR", "./data/uploads"),
		TrustedProxies:              proxies,
		SMTPURL:                     os.Getenv("FOLD_SMTP_URL"),
		SMTPAllowInsecure:           getEnvBool("FOLD_SMTP_ALLOW_INSECURE", false),
		EmailFrom:                   getEnv("FOLD_EMAIL_FROM", "Fold <noreply@localhost>"),
		PostmarkWebhookSecret:       os.Getenv("FOLD_POSTMARK_WEBHOOK_SECRET"),
		Workers:                     workers,
		JobPoll:                     poll,
		PushHosts:                   pushHosts,
		EmailProvider:               os.Getenv("FOLD_EMAIL_PROVIDER"),
		PostmarkServerToken:         os.Getenv("FOLD_POSTMARK_SERVER_TOKEN"),
		PostmarkTransactionalStream: os.Getenv("FOLD_POSTMARK_TRANSACTIONAL_STREAM"),
		PostmarkBroadcastStream:     os.Getenv("FOLD_POSTMARK_BROADCAST_STREAM"),
	}, nil
}

func (c Config) IsDev() bool { return c.Env == "dev" }
