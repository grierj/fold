package main

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
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
}

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
	}

	dbURL, err := databaseURL()
	if err != nil {
		return Config{}, err
	}

	proxies, err := parseTrustedProxies(getEnv("FOLD_TRUSTED_PROXIES", defaultTrustedProxies))
	if err != nil {
		return Config{}, err
	}

	return Config{
		Env:            env,
		Port:           getEnv("PORT", "8080"),
		SessionSecret:  []byte(secret),
		DatabaseURL:    dbURL,
		CookieSecure:   getEnvBool("COOKIE_SECURE", !isDev),
		SiteURL:        os.Getenv("SITE_URL"),
		UploadDir:      getEnv("UPLOAD_DIR", "./data/uploads"),
		TrustedProxies: proxies,
	}, nil
}

func (c Config) IsDev() bool { return c.Env == "dev" }
