// Package server starts everything: it reads the environment, opens the
// database, starts the job runner and the sign-in queue, and serves HTTP and
// the control socket.
package server

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"fold/internal/accounts"
	"fold/internal/app"
	"fold/internal/backup"
	"fold/internal/clock"
	"fold/internal/db"
	"fold/internal/httpx"
	"fold/internal/keys"
	"fold/internal/mail"
	"fold/internal/write"
)

// Env is the configuration, from environment variables.
type Env struct {
	SiteURL *url.URL
	DataDir string
	Listen  string
	Dev     bool
	Keys    *keys.Keys
	Proxies httpx.Proxies
}

// ReadEnv reads and checks the environment. Development mode refuses any
// setting that would put it on a public address, because its outbox shows
// sign-in links to whoever opens it.
func ReadEnv(get func(string) string) (Env, error) {
	e := Env{DataDir: get("DATA_DIR"), Listen: get("LISTEN"), Dev: get("DEV") == "1"}
	if e.DataDir == "" {
		e.DataDir = "/data"
	}
	site := get("SITE_URL")
	if site == "" && e.Dev {
		site = "http://localhost:8080"
	}
	u, err := url.Parse(site)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.Path != "" && u.Path != "/" {
		return Env{}, errors.New("SITE_URL must be the site's address, like https://church.example.org")
	}
	u.Path = ""
	u.Host = strings.ToLower(u.Host) // browsers send the Origin in lower case
	e.SiteURL = u
	if e.Listen == "" {
		e.Listen = ":8080"
		if e.Dev {
			e.Listen = "127.0.0.1:8080"
		}
	}
	if e.Dev {
		host, _, err := net.SplitHostPort(e.Listen)
		if err != nil || !loopbackHost(host) || !loopbackHost(u.Hostname()) || u.Scheme != "http" {
			return Env{}, errors.New("DEV=1 runs only on this computer: LISTEN and SITE_URL must be localhost or 127.0.0.1, over http")
		}
	} else if u.Scheme != "https" {
		return Env{}, errors.New("SITE_URL must start with https:// (Caddy provides the certificate)")
	}
	if e.Proxies, err = httpx.ParseProxies(get("TRUSTED_PROXIES")); err != nil {
		return Env{}, err
	}
	secret := get("SECRET_KEY")
	if secret == "" && e.Dev {
		secret = devSecret(e.DataDir)
	}
	if secret == "" {
		return Env{}, errors.New("SECRET_KEY is not set. Make one with: openssl rand -hex 32")
	}
	if e.Keys, err = keys.Parse(secret, get("SECRET_KEY_PREVIOUS")); err != nil {
		return Env{}, err
	}
	return e, nil
}

func loopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// devSecret is a per-checkout secret for development, kept in the data folder.
func devSecret(dir string) string {
	p := filepath.Join(dir, "dev-secret.key")
	if b, err := os.ReadFile(p); err == nil {
		return strings.TrimSpace(string(b))
	}
	var b [32]byte
	rand.Read(b[:])
	s := hex.EncodeToString(b[:])
	os.MkdirAll(dir, 0o700)
	os.WriteFile(p, []byte(s+"\n"), 0o600)
	return s
}

// DBPath is the one database file.
func DBPath(dataDir string) string { return filepath.Join(dataDir, "app.db") }

// Lock holds DATA_DIR/server.lock for the life of the process, so two
// servers never share a database and an offline restore knows the server is
// stopped.
func Lock(dataDir string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(dataDir, "server.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("another server is using this data folder")
	}
	return f, nil
}

// Opened is a database ready to use, with the lock held.
type Opened struct {
	Writer *sql.DB
	Reader *db.Reader
	Exec   *write.Executor
	lock   *os.File
}

func (o *Opened) Close() {
	o.Reader.Close()
	o.Writer.Close()
	o.lock.Close()
}

// Open takes the lock, opens and migrates the database.
func Open(ctx context.Context, dataDir string, k *keys.Keys, c clock.Clock) (*Opened, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	l, err := Lock(dataDir)
	if err != nil {
		return nil, err
	}
	w, err := db.OpenWriter(DBPath(dataDir))
	if err != nil {
		l.Close()
		return nil, err
	}
	have, err := db.Version(ctx, w)
	if err == nil && have > 0 && have < db.Latest() {
		_, err = backup.BeforeMigrate(ctx, w, dataDir, c.Now(), have)
	}
	if err == nil {
		err = db.Migrate(ctx, w)
	}
	if err != nil {
		w.Close()
		l.Close()
		return nil, err
	}
	r, err := db.OpenReader(DBPath(dataDir))
	if err != nil {
		w.Close()
		l.Close()
		return nil, err
	}
	return &Opened{Writer: w, Reader: r, Exec: write.New(w, c, k), lock: l}, nil
}

// Serve runs until ctx ends.
func Serve(ctx context.Context, env Env, log *slog.Logger) error {
	c := clock.Real{}
	o, err := Open(ctx, env.DataDir, env.Keys, c)
	if err != nil {
		return err
	}
	defer o.Close()
	if env.Keys.Rotating() {
		if err := o.Exec.Run(ctx, write.System(), Reseal{}); err != nil {
			return fmt.Errorf("resealing settings with the new secret: %w", err)
		}
		log.Info("settings resealed with the new SECRET_KEY; SECRET_KEY_PREVIOUS can be removed")
	}
	var outbox *mail.Outbox
	if env.Dev {
		outbox = &mail.Outbox{}
	}
	queue := accounts.NewSignInQueue(o.Exec, log)
	a, err := app.New(app.Config{SiteURL: env.SiteURL, Dev: env.Dev, Proxies: env.Proxies},
		app.Deps{Reader: o.Reader, Exec: o.Exec, Queue: queue, Keys: env.Keys, Clock: c, Log: log, Outbox: outbox})
	if err != nil {
		return err
	}
	runner := write.NewRunner(o.Exec, log)
	mailer := &accounts.Mailer{Reader: o.Reader, Keys: env.Keys, SiteURL: env.SiteURL.String(), Outbox: outbox, Dev: env.Dev, Clock: c}
	runner.Handle(accounts.JobLinkMail, mailer.SendLink)
	(&backup.Jobs{Exec: o.Exec, Reader: o.Reader, DataDir: env.DataDir, Outbox: outbox, Dev: env.Dev, Log: log}).Register(runner)

	ctrl := &control{env: env, exec: o.Exec, reader: o.Reader, app: a, log: log}
	if err := ctrl.announceSetup(ctx); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go runner.Loop(ctx)
	go queue.Run(ctx)
	ln, err := ctrl.listen()
	if err != nil {
		return err
	}
	go ctrl.serve(ln)
	defer ln.Close()

	srv := &http.Server{
		Addr:              env.Listen,
		Handler:           a.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	errs := make(chan error, 1)
	go func() { errs <- srv.ListenAndServe() }()
	log.Info("listening", "addr", env.Listen, "site", env.SiteURL.String(), "dev", env.Dev)
	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}
	shut, done := context.WithTimeout(context.Background(), 10*time.Second)
	defer done()
	return srv.Shutdown(shut)
}
