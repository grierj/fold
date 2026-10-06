package main

import (
	"log"
	"os"
	"strconv"
)

// Config holds all runtime configuration, sourced from the environment.
type Config struct {
	// Env is "dev" or "prod". Defaults to "prod" — the safe choice.
	Env string
	// Port the HTTP server listens on. Default "8080".
	Port string
	// SessionSecret signs session cookies. Required in prod; a loud
	// insecure default is used for dev only.
	SessionSecret []byte
	// DatabaseURL, when set, selects Postgres (e.g.
	// "postgres://user:pass@host:5432/dbname?sslmode=disable").
	// When empty, SQLite is used — intended for development.
	DatabaseURL string
	// SQLitePath is the SQLite file location for dev mode.
	SQLitePath string
	// CookieSecure sets the Secure flag on cookies. Default true in prod.
	CookieSecure bool
	// SiteURL is the public base URL (used for CSRF origin checks behind
	// proxies, e.g. "https://groups.example.org").
	SiteURL string
}

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

func LoadConfig() Config {
	env := getEnv("APP_ENV", "prod")
	isDev := env == "dev"

	secret := os.Getenv("SESSION_SECRET")
	if secret == "" {
		if isDev {
			secret = "dev-only-insecure-secret-do-not-use-in-prod"
			log.Println("WARNING: SESSION_SECRET not set; using insecure dev default")
		} else {
			log.Fatal("SESSION_SECRET must be set when APP_ENV != dev")
		}
	}

	return Config{
		Env:           env,
		Port:          getEnv("PORT", "8080"),
		SessionSecret: []byte(secret),
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		SQLitePath:    getEnv("SQLITE_PATH", "./data/church-groups.db"),
		CookieSecure:  getEnvBool("COOKIE_SECURE", !isDev),
		SiteURL:       os.Getenv("SITE_URL"),
	}
}

func (c Config) IsDev() bool { return c.Env == "dev" }
