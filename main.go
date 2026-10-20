package main

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"fold/internal/events"
	"fold/internal/mail"
	"fold/models"
	"fold/storage"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/sessions"
	"gorm.io/gorm"
)

// App wires together config, database, and session store.
type App struct {
	DB      *gorm.DB
	Store   *sessions.CookieStore
	Config  Config
	Uploads storage.Store // blob store for avatars and group covers
	Mailer  mail.Sender   // transactional email (C6, M1.2)

	// setupDone is set once any user exists. The wizard cannot be re-entered
	// without a process restart after that, which is the normal case.
	setupMu   sync.RWMutex
	setupDone bool
}

const sessionName = "fold-session"

// serve runs the web server until SIGINT or SIGTERM.
func serve(stderr io.Writer) int {
	cfg, err := LoadConfig()
	if err != nil {
		fmt.Fprintln(stderr, "fold:", err)
		return 1
	}

	// Distroless images have no shell, so log the working directory at
	// startup: template/static paths are relative and this is the only
	// way to see what they resolve against.
	if wd, err := os.Getwd(); err == nil {
		log.Printf("Working directory: %s", wd)
	}

	if cfg.IsDev() {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	db, err := openDatabase(context.Background(), cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintln(stderr, "fold:", err)
		return 1
	}

	uploads, err := storage.NewLocalStore(cfg.UploadDir, "/uploads")
	if err != nil {
		log.Fatalf("Upload storage failed: %v", err)
	}

	store := sessions.NewCookieStore(cfg.SessionSecret)
	store.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   86400 * 7, // 7 days
		HttpOnly: true,
		Secure:   cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	}

	app := &App{
		DB: db, Store: store, Config: cfg, Uploads: uploads,
		Mailer: newMailer(cfg, db),
	}
	r := app.setupRouter()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	app.startOccurrenceTicker(ctx)
	app.startNotificationPruning(ctx)
	app.startAccountRequestExpiry(ctx)

	srv := &http.Server{Addr: ":" + cfg.Port, Handler: r}
	go func() {
		log.Printf("Starting server on :%s (env=%s)", cfg.Port, cfg.Env)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("Shutdown: %v", err)
	}
	return 0
}

// newMailer picks the transactional email sender (C6, 5.6, M1.2): the
// devoutbox in dev, SMTP from FOLD_SMTP_URL otherwise. Without a sender,
// sends fail loudly instead of silently dropping password resets.
func newMailer(cfg Config, db *gorm.DB) mail.Sender {
	if cfg.IsDev() {
		return mail.NewDevOutbox(db, cfg.EmailFrom)
	}
	if cfg.SMTPURL == "" {
		log.Println("WARNING: FOLD_SMTP_URL is not set; transactional email (password resets, invitations) will fail")
		return mail.NoSender{}
	}
	s, err := mail.NewSMTP(cfg.SMTPURL, cfg.EmailFrom)
	if err != nil {
		log.Fatalf("FOLD_SMTP_URL: %v", err)
	}
	return s
}

// startOccurrenceTicker extends stored occurrences out to 18 months, once
// at startup and then daily. A second app process skips the run while the
// advisory lock is held (5.5).
func (a *App) startOccurrenceTicker(ctx context.Context) {
	go func() {
		run := func() {
			if err := events.ExtendLocked(ctx, a.DB, time.Now().UTC()); err != nil {
				log.Printf("occurrences: %v", err)
			}
		}
		run()
		tick := time.NewTicker(24 * time.Hour)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				run()
			}
		}
	}()
}

// runHealthcheck is the distroless-friendly probe used by Docker. The image
// has no shell or curl, so the binary asks the local server itself.
func runHealthcheck(port string) int {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func (a *App) setupRouter() *gin.Engine {
	r := gin.New()
	// The SPA is served by a NoRoute fallback; Gin's default trailing-slash
	// redirect would 301-loop client-side routes (e.g. "/" -> "/"), so it
	// must stay off.
	r.RedirectTrailingSlash = false
	r.Use(gin.Logger(), gin.Recovery(), securityHeaders(a.Config.CookieSecure))
	// Cap every request body before anything parses it (the CSRF check
	// reads form fields, and Gin spools big multipart bodies to disk).
	r.Use(limitBody(maxRequestBytes))
	// Believe X-Forwarded-For only from the reverse proxy (C17). LoadConfig
	// validated the list; an empty one trusts nobody.
	if err := r.SetTrustedProxies(a.Config.TrustedProxies); err != nil {
		log.Printf("WARNING: trusted proxies: %v; trusting none", err)
		_ = r.SetTrustedProxies(nil)
	}

	css := builtStylesheet(distFS)
	r.SetFuncMap(template.FuncMap{
		"split":      strings.Split,
		"stylesheet": func() string { return css },
	})
	r.LoadHTMLGlob("templates/*.html")

	// User-uploaded assets (avatars, group covers). Served with
	// X-Content-Type-Options: nosniff; uploads are validated as images
	// on the way in.
	r.GET("/uploads/*filepath", a.serveUpload)

	// Registered before the setup redirect so a database ping still
	// answers while the wizard is waiting for the first admin.
	r.GET("/healthz", a.healthz)

	// Routes outside the CSRF and setup middleware (C17). They authenticate
	// with provider signatures (hooks) or signed tokens (RFC 8058 one-click
	// unsubscribe) instead. Their handlers arrive with messaging (M3);
	// until then they answer 404. csrfExemptRoutes must match this list.
	r.Any("/hooks/*path", notFound)
	r.POST("/u/unsubscribe/:token", notFound)

	// First-run setup wizard takes precedence over everything else.
	r.Use(a.setupRequired())
	r.Use(a.csrfMiddleware())

	// Setup wizard (only reachable when no users exist; server-rendered).
	r.GET("/setup", a.showSetup)
	r.POST("/setup", a.handleSetup)

	// Dev-only mail outbox (M1.2). The handler 404s unless APP_ENV=dev.
	r.GET("/dev/outbox", a.devOutbox)

	// JSON API.
	a.registerAPIRoutes(r)

	// ICS calendar feeds (M1.7). GETs ride the setup and CSRF
	// middleware like everything else; safe methods need no token.
	a.registerCalendarFeeds(r)

	// Embedded SPA for everything else.
	r.NoRoute(a.serveSPA())

	return r
}

//go:embed web/dist
var distFS embed.FS

// stylesheetRe finds the CSS bundle Vite links from index.html.
var stylesheetRe = regexp.MustCompile(`<link rel="stylesheet"[^>]*href="(/assets/[^"]+\.css)"`)

// builtStylesheet returns the path of the SPA's CSS bundle, which the
// server-rendered pages (setup, dev outbox) share so they use the same
// tokens and font. It is empty when web/dist is not built; those pages
// then render unstyled.
func builtStylesheet(fsys fs.FS) string {
	b, err := fs.ReadFile(fsys, "web/dist/index.html")
	if err != nil {
		return ""
	}
	m := stylesheetRe.FindSubmatch(b)
	if m == nil {
		return ""
	}
	return string(m[1])
}

// serveSPA serves the embedded frontend bundle, falling back to index.html
// for client-side routes. API paths get a JSON 404 instead.
func (a *App) serveSPA() gin.HandlerFunc {
	sub, err := fs.Sub(distFS, "web/dist")
	built := err == nil
	if built {
		if _, err := fs.Stat(sub, "index.html"); err != nil {
			built = false
		}
	}
	if !built {
		log.Println("WARNING: web/dist is empty — run `npm run build` in web/ to build the frontend")
	}
	return func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.Header("Cache-Control", "no-store")
			jsonError(c, http.StatusNotFound, "not found")
			return
		}
		if !built {
			c.String(http.StatusServiceUnavailable,
				"Frontend not built. Run `npm run build` in web/ and restart.")
			return
		}
		path := strings.TrimPrefix(c.Request.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(sub, path); err != nil {
			// Client-side route — let the SPA router handle it.
			path = "index.html"
		}
		// ServeContent, not http.FileServer: FileServer 301-redirects
		// index.html serves to "./", which breaks the SPA fallback.
		data, err := fs.ReadFile(sub, path)
		if err != nil {
			jsonError(c, http.StatusNotFound, "not found")
			return
		}
		info, err := fs.Stat(sub, path)
		if err != nil || info.IsDir() {
			jsonError(c, http.StatusNotFound, "not found")
			return
		}
		if path == "index.html" {
			// no-store: always fetch the shell fresh (a rebuilt image is what
			// runs), and keep the page out of the back/forward cache, so going
			// back after logout never shows the previous session's screen.
			c.Header("Cache-Control", "no-store")
		} else if strings.HasPrefix(path, "assets/") {
			c.Header("Cache-Control", "public, max-age=31536000, immutable")
		}
		http.ServeContent(c.Writer, c.Request, path, info.ModTime(), bytes.NewReader(data))
	}
}

// contentSecurityPolicy covers the SPA and /setup (DEC-37, 10.1). Scripts
// and styles come only from this origin; React sets focal points through
// the CSSOM, which CSP allows without 'unsafe-inline'. Images may be data:
// and blob: (upload previews) or any https URL (group covers given by URL);
// tighten img-src when uploads are the only source (M4.7).
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; " +
	"img-src 'self' data: blob: https:; connect-src 'self'; font-src 'self'; object-src 'none'; " +
	"base-uri 'self'; form-action 'self'; frame-ancestors 'self'"

// securityHeaders sets the headers on every response, so they hold behind
// any proxy. HSTS goes out only when cookies are Secure, which means the
// site is served over HTTPS (never in dev).
func securityHeaders(hsts bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if hsts {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		c.Next()
	}
}

func (a *App) healthz(c *gin.Context) {
	sqlDB, err := a.DB.DB()
	if err != nil {
		c.Status(http.StatusServiceUnavailable)
		return
	}
	if err := sqlDB.PingContext(c.Request.Context()); err != nil {
		c.Status(http.StatusServiceUnavailable)
		return
	}
	c.Status(http.StatusOK)
}

// setupComplete reports whether the wizard has already created an account.
// After the first user exists the answer is cached for the life of the process.
func (a *App) setupComplete() (bool, error) {
	a.setupMu.RLock()
	if a.setupDone {
		a.setupMu.RUnlock()
		return true, nil
	}
	a.setupMu.RUnlock()

	var n int64
	if err := a.DB.Model(&models.User{}).Count(&n).Error; err != nil {
		return false, err
	}
	if n > 0 {
		a.setupMu.Lock()
		a.setupDone = true
		a.setupMu.Unlock()
		return true, nil
	}
	return false, nil
}

// html renders a template, injecting the CSRF field and site name.
func (a *App) html(c *gin.Context, code int, name string, data gin.H) {
	if data == nil {
		data = gin.H{}
	}
	data["csrfField"] = csrfField(c.GetString("csrfToken"))
	data["SiteName"] = a.siteName()
	c.HTML(code, name, data)
}

func (a *App) siteName() string {
	var s models.Setting
	if err := a.DB.Where("key = ?", "site_name").First(&s).Error; err != nil || s.Value == "" {
		return "Fold"
	}
	return s.Value
}

// setupRequired redirects everything to the setup wizard until an admin
// account exists. This replaces the old hardcoded seed users.
func (a *App) setupRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		p := c.Request.URL.Path
		if p == "/setup" || p == "/healthz" || strings.HasPrefix(p, "/assets/") {
			c.Next()
			return
		}
		ready, err := a.setupComplete()
		if err != nil {
			c.String(http.StatusInternalServerError, "Database error")
			c.Abort()
			return
		}
		if !ready {
			c.Redirect(http.StatusSeeOther, "/setup")
			c.Abort()
			return
		}
		c.Next()
	}
}

// --- Setup wizard ---

func (a *App) showSetup(c *gin.Context) {
	var n int64
	a.DB.Model(&models.User{}).Count(&n)
	if n > 0 {
		c.Redirect(http.StatusSeeOther, "/")
		return
	}
	a.html(c, http.StatusOK, "setup.html", nil)
}

func (a *App) handleSetup(c *gin.Context) {
	var n int64
	a.DB.Model(&models.User{}).Count(&n)
	if n > 0 {
		c.Redirect(http.StatusSeeOther, "/")
		return
	}

	siteName := strings.TrimSpace(c.PostForm("site_name"))
	timezone := strings.TrimSpace(c.PostForm("timezone"))
	name := strings.TrimSpace(c.PostForm("name"))
	email := normalizeEmail(c.PostForm("email"))
	password := c.PostForm("password")
	confirm := c.PostForm("password_confirm")

	fail := func(msg string) {
		// Keep the fields that passed validation so the user doesn't have
		// to retype them. Never echo back secrets (passwords) or payment
		// details — only non-sensitive fields are preserved.
		a.html(c, http.StatusBadRequest, "setup.html", gin.H{
			"Error":           msg,
			"EnteredSiteName": siteName,
			"EnteredTimezone": timezone,
			"EnteredName":     name,
			"EnteredEmail":    email,
		})
	}

	if siteName == "" || timezone == "" || name == "" || email == "" || password == "" {
		fail("All fields are required.")
		return
	}
	if _, err := events.LoadZone(timezone); err != nil {
		fail("Time zone must be an IANA name, for example America/Denver.")
		return
	}
	if !validEmail(email) {
		fail("Enter a valid email address.")
		return
	}
	if len(password) < minPasswordLength {
		fail("Password must be at least 10 characters.")
		return
	}
	if password != confirm {
		fail("Passwords do not match.")
		return
	}

	admin := models.User{Email: email, Name: name, Role: models.RoleAdmin, Status: models.UserActive}
	err := admin.SetPassword(password)
	if err == nil {
		err = a.DB.Create(&admin).Error
	}
	if err != nil {
		log.Printf("Setup failed creating admin: %v", err)
		fail("Could not create the admin account.")
		return
	}
	a.DB.Where(models.Setting{Key: "site_name"}).
		Assign(models.Setting{Value: siteName}).
		FirstOrCreate(&models.Setting{})
	a.DB.Where(models.Setting{Key: settingTimezone}).
		Assign(models.Setting{Value: timezone}).
		FirstOrCreate(&models.Setting{})

	log.Printf("Setup complete: site=%q timezone=%q", siteName, timezone)
	c.Redirect(http.StatusSeeOther, "/login")
}

// maxRequestBytes is the largest request body accepted: one image upload
// plus room for the multipart envelope.
const maxRequestBytes = maxUploadBytes + 1<<20

// limitBody makes reads past n fail, so oversized bodies are refused
// instead of buffered.
func limitBody(n int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.ContentLength > n {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, errorBody(http.StatusRequestEntityTooLarge, "request too large"))
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, n)
		c.Next()
	}
}
