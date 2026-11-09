package main

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"fold/internal/httpx"
	"fold/internal/images"
	"fold/internal/mail"
	"fold/internal/providers"
	"fold/internal/push"
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

	// seriesClock is a per-App clock seam for lock-boundary tests. Production
	// leaves it nil and reads the current UTC time.
	seriesClock func() time.Time

	// calendarClock fixes DEC-77 window boundaries in handler tests. Production
	// leaves it nil and reads the current UTC time.
	calendarClock func() time.Time

	// setupDone is set once any user exists. The wizard cannot be re-entered
	// without a process restart after that, which is the normal case.
	setupMu   sync.RWMutex
	setupDone bool

	// Push sends web push (M3.13). nil builds the default from Config on
	// first use (pushClient); tests point its HTTP transport at a fake.
	Push     *push.Client
	pushOnce sync.Once
	// vapid caches the decrypted VAPID key (push_keys.go).
	vapid vapidCache
	// pushRender overrides renderPush in tests (a rendering past the
	// payload cap); nil uses the stored push text.
	pushRender func(*models.Notification) (push.Notification, bool)
	// pushClock and pushWindow fix the time and the quiet hours in push
	// tests (DEC-126). Production leaves them unset: the current time and
	// push.DefaultWindow.
	pushClock  func() time.Time
	pushWindow *push.Window

	// fence is the restore barrier (restore_fence.go) and attempts the admin
	// operation ledger (admin_operation_attempts.go). Both are ready at their
	// zero value; serve makes the boot floor and the per-process key before it
	// serves, and exits non-zero when it cannot.
	fence    fenceState
	attempts attemptsState
	// requestRestart asks serve to shut down and exit non-zero once a restore
	// is detected. serve sets it; tests set a recorder; nil does nothing.
	requestRestart func()

	// Test seams, all nil in production (T07B-1b). A nil seam sends no
	// statement and takes no time.
	//
	// restoreAckPause runs inside acknowledgeRestoreTicket before it raises the
	// floor. attemptClock replaces the monotonic time since the process
	// started. settleBeforeTx runs before settle's first transaction;
	// settleBeforeFence and settleAfterFence run inside its seal, around the
	// ledger transition, given the open transaction. deleteBeforeSeal runs in
	// the deletion's seal just before currentAdminIn, given the open
	// transaction; deleteBeforeRetry runs between a busy attempt's rollback and
	// the next attempt, with no transaction open. conversionAfterUserLocks runs
	// in acceptHouseholdConversion right after its users statement.
	// readBeforeSeal runs in a reason-review page transaction between its view
	// audit and the actor seal, given the open transaction (T07B-2b).
	// sealAttempts counts seal entries of the deletion and settle.
	restoreAckPause          func()
	attemptClock             func() time.Duration
	settleBeforeTx           func()
	settleBeforeFence        func(*gorm.DB)
	settleAfterFence         func(*gorm.DB)
	deleteBeforeSeal         func(*gorm.DB)
	deleteBeforeRetry        func()
	readBeforeSeal           func(*gorm.DB)
	conversionAfterUserLocks func(*gorm.DB)
	sealAttempts             *atomic.Int64
}

const sessionName = "fold-session"

// newSessionStore applies the same lifetime to browser cookies and the signed
// codec. Options.MaxAge alone does not change the codec's 30-day default.
func newSessionStore(secret []byte, secure bool) *sessions.CookieStore {
	store := sessions.NewCookieStore(secret)
	store.Options = &sessions.Options{
		Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	}
	store.MaxAge(86400 * 7)
	return store
}

// serve runs the web server until SIGINT or SIGTERM.
func serve(stderr io.Writer) int {
	cfg, err := LoadConfig()
	if err != nil {
		fmt.Fprintln(stderr, "fold:", err)
		return 1
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
		fmt.Fprintln(stderr, "fold: upload storage:", err)
		return 1
	}
	mailer, err := chooseMailer(cfg, db)
	if err != nil {
		fmt.Fprintln(stderr, "fold: email:", err)
		return 1
	}

	store := newSessionStore(cfg.SessionSecret, cfg.CookieSecure)

	app := &App{
		DB: db, Store: store, Config: cfg, Uploads: uploads,
		Mailer: mailer,
	}
	// The signal context exists this early so a detected database restore can
	// end the process (T07B-1b): requestRestart cancels it, and serve returns
	// non-zero after the graceful shutdown below.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	var restarting atomic.Bool
	app.requestRestart = func() {
		restarting.Store(true)
		stop()
	}
	if err := app.initAttempts(); err != nil {
		fmt.Fprintln(stderr, "fold: make the attempt key:", err)
		return 1
	}
	// The restore barrier's restart boundary: after migrations, before anything
	// serves, in one transaction under the migrations lock. Any failure,
	// including an unacknowledged COMMIT, exits non-zero.
	if err := app.bootRestoreFence(); err != nil {
		fmt.Fprintln(stderr, "fold:", err)
		return 1
	}
	ready, err := app.setupComplete()
	if err != nil {
		fmt.Fprintln(stderr, "fold: check setup availability:", err)
		return 1
	}
	if !ready && app.Config.SetupToken == "" {
		app.Config.SetupToken, err = newSetupToken()
		if err != nil {
			fmt.Fprintln(stderr, "fold: generate setup code:", err)
			return 1
		}
		log.Printf("First-run setup code: %s (enter at /setup; keep this code private)", app.Config.SetupToken)
	}
	// Web push keys (M3.13a): made on first boot, and again if
	// SESSION_SECRET changed. A failure here costs only push, so the
	// server still starts and the next boot tries again.
	if err := ensureVAPIDKeys(db, cfg.SessionSecret); err != nil {
		log.Printf("push: couldn't set up the web push keys: %v", err)
	}
	if vapidSubject(cfg) == "" {
		log.Println("WARNING: web push has no contact address for the push services; set FOLD_EMAIL_FROM to an address at your church's domain, or SITE_URL. Some push services refuse notifications without one")
	}
	r := app.setupRouter()

	// Background work runs from the jobs table (M3.1, PLAN 5.5).
	waitJobs := app.startJobs(ctx)
	defer waitJobs()

	srv := newHTTPServer(cfg, r)
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
	if restarting.Load() {
		// A restored database was detected: leave so the supervisor starts a
		// new process, which runs the boot step again.
		log.Printf("Stopping: the database was restored while Fold was running")
		return 1
	}
	return 0
}

// HTTP server limits (M7.2g). The largest honest request is a 5 MB photo from
// a phone on church Wi-Fi, so 60 s to read it is generous. No route streams or
// long-polls; background work runs through internal/jobs. Handlers that wait on
// mail or a push service carry their own shorter deadlines (SMTP 30 s,
// Postmark 15 s, push test 15 s), so none needs a longer server timeout.
const (
	httpReadHeaderTimeout = 10 * time.Second
	httpReadTimeout       = 60 * time.Second
	httpWriteTimeout      = 60 * time.Second
	httpIdleTimeout       = 120 * time.Second
	httpMaxHeaderBytes    = 1 << 20

	// Leave 40 seconds of the write budget for transaction work and the
	// response carrying the browser's guest manage link or verification state.
	// Delivery jobs retain their separate 60-second deliverSendTimeout.
	syncMailTimeout = 20 * time.Second
)

// newHTTPServer builds the production server with its timeouts, so a slow or
// stalled client cannot hold a connection and its goroutine forever.
func newHTTPServer(cfg Config, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           handler,
		ReadHeaderTimeout: httpReadHeaderTimeout,
		ReadTimeout:       httpReadTimeout,
		WriteTimeout:      httpWriteTimeout,
		IdleTimeout:       httpIdleTimeout,
		MaxHeaderBytes:    httpMaxHeaderBytes,
	}
}

// chooseMailer picks the email adapter (5.6, M3.2): see mail.Choose. Without
// one, sends fail loudly instead of silently dropping password resets. A
// provider that can't be built is an error, and serve stops. Every setting
// comes from Config, which has already checked it.
func chooseMailer(cfg Config, db *gorm.DB) (mail.Sender, error) {
	settings := func(k string) string {
		switch k {
		case "FOLD_EMAIL_PROVIDER":
			return cfg.EmailProvider
		case "FOLD_SMTP_URL":
			return cfg.SMTPURL
		case "FOLD_SMTP_ALLOW_INSECURE":
			return strconv.FormatBool(cfg.SMTPAllowInsecure)
		case "FOLD_POSTMARK_SERVER_TOKEN":
			return cfg.PostmarkServerToken
		case "FOLD_POSTMARK_TRANSACTIONAL_STREAM":
			return cfg.PostmarkTransactionalStream
		case "FOLD_POSTMARK_BROADCAST_STREAM":
			return cfg.PostmarkBroadcastStream
		}
		return ""
	}
	name := mail.Choose(settings, cfg.IsDev())
	if name == "" {
		log.Println("WARNING: no email provider is set (FOLD_POSTMARK_SERVER_TOKEN or FOLD_SMTP_URL); transactional email (password resets, invitations) will fail")
		return mail.NoSender{}, nil
	}
	if strings.HasSuffix(mail.Address(cfg.EmailFrom), "@localhost") && !cfg.IsDev() {
		log.Println("WARNING: FOLD_EMAIL_FROM is a localhost address; providers will refuse to send it. Set it to an address at your church's domain")
	}
	sender, err := providers.NewEmail(name, providers.Settings{Get: settings, DB: db, From: cfg.EmailFrom})
	if err != nil {
		return nil, err
	}
	log.Printf("email: sending through %s", sender.Capabilities().Name)
	adapter, ok := sender.(mail.Adapter)
	if !ok {
		return nil, fmt.Errorf("the %s adapter can't send mail", name)
	}
	// Nothing reaches a suppressed address, whichever adapter runs (DEC-112).
	return mail.WithSuppressions(db, adapter), nil
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
	// Stamp the request before anything reads its body: the CSRF check parses a
	// multipart form, so a slow upload is already read when a handler starts.
	r.Use(stampRequestStart(), httpx.AccessLog(), gin.Recovery(), httpx.SecurityHeaders(a.Config.CookieSecure))
	// Cap every request body before anything parses it (the CSRF check
	// reads form fields, and Gin spools big multipart bodies to disk).
	r.Use(httpx.LimitBody(maxRequestBytes, jsonError))
	// Believe X-Forwarded-For only from the reverse proxy (C17). LoadConfig
	// validated the list; an empty one trusts nobody.
	if err := r.SetTrustedProxies(a.Config.TrustedProxies); err != nil {
		log.Printf("WARNING: trusted proxies: %v; trusting none", err)
		_ = r.SetTrustedProxies(nil)
	}

	tmpl, err := pageTemplates()
	if err != nil {
		// The files are embedded and parsed by TestPageTemplatesParse, so this
		// is a build defect, not a runtime condition.
		panic(fmt.Errorf("parse embedded templates: %w", err))
	}
	r.SetHTMLTemplate(tmpl)

	// User-uploaded assets (avatars, group covers). Served with
	// X-Content-Type-Options: nosniff; uploads are validated as images
	// on the way in.
	r.GET("/uploads/*filepath", a.serveUpload)

	// Registered before the setup redirect so a database ping still
	// answers while the wizard is waiting for the first admin.
	r.GET("/healthz", httpx.Healthz(func(ctx context.Context) error {
		sqlDB, err := a.DB.DB()
		if err != nil {
			return err
		}
		return sqlDB.PingContext(ctx)
	}))
	r.GET("/help/members", serveMemberHelp)

	// Routes outside the CSRF and setup middleware (C17). They authenticate
	// with provider credentials (hooks) or signed tokens (RFC 8058 one-click
	// unsubscribe) instead. TestCSRFExemptRoutesAreExact (csrf_test.go)
	// keeps its csrfExemptRoutes list equal to these routes.
	// Postmark's delivery reports (M3.2b): HTTP Basic credentials from the
	// webhook URL, 404 while FOLD_POSTMARK_WEBHOOK_SECRET is unset.
	r.POST("/hooks/postmark/email", a.postmarkEmailHook)
	// RFC 8058 one-click unsubscribe (M3.3): the signed token is the only
	// authority, so mail clients can POST with no cookie or CSRF token.
	r.POST("/u/unsubscribe/:token", a.oneClickUnsubscribe)

	// First-run setup wizard takes precedence over everything else.
	r.Use(a.setupRequired())
	r.Use(a.csrfMiddleware())

	// Setup wizard (only reachable when no users exist; server-rendered).
	r.GET("/setup", a.showSetup)
	r.POST("/setup", a.handleSetup)
	r.POST("/setup/unlock", a.unlockSetup)

	// Dev-only mail outbox (M1.2). The handler 404s unless APP_ENV=dev.
	r.GET("/dev/outbox", a.devOutbox)
	r.GET("/dev/outbox/:id/html", a.devOutboxHTML)

	// JSON API.
	a.registerAPIRoutes(r)

	// ICS calendar feeds (M1.7). GETs ride the setup and CSRF
	// middleware like everything else; safe methods need no token.
	a.registerCalendarFeeds(r)

	// The Home Screen app's manifest carries the church's name (M3.13c1).
	r.GET("/manifest.webmanifest", httpx.Manifest(a.siteName, defaultSiteName))

	// Embedded SPA for everything else, including the service worker.
	r.NoRoute(httpx.ServeSPA(distSub(), a.emailBaseURL(), jsonError))

	return r
}

//go:embed web/dist
var distFS embed.FS

//go:embed templates/*.html
var templatesFS embed.FS

// pageTemplates parses the server-rendered pages (setup wizard, dev outbox)
// from the binary once; every router shares the result.
var pageTemplates = sync.OnceValues(func() (*template.Template, error) {
	css := httpx.Stylesheet(distSub())
	return template.New("").Funcs(template.FuncMap{
		"split":      strings.Split,
		"stylesheet": func() string { return css },
	}).ParseFS(templatesFS, "templates/*.html")
})

//go:embed docs/MEMBERS.md
var memberHelp []byte

func serveMemberHelp(c *gin.Context) {
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, "text/plain; charset=utf-8", memberHelp)
}

// distSub is the built SPA bundle, rooted at index.html; nil when the embedded
// directory has no bundle.
func distSub() fs.FS {
	sub, err := fs.Sub(distFS, "web/dist")
	if err != nil {
		return nil
	}
	return sub
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

// setupRequired redirects everything to the setup wizard until an admin
// account exists. This replaces the old hardcoded seed users.
func (a *App) setupRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		p := c.Request.URL.Path
		if p == "/setup" || p == "/setup/unlock" || p == "/healthz" || strings.HasPrefix(p, "/assets/") {
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
	ready, err := a.setupComplete()
	if err != nil {
		log.Printf("Setup availability check failed: %v", err)
		if a.setupAuthorized(c) {
			a.html(c, http.StatusServiceUnavailable, "setup.html", (setupInput{}).pageData(nil, "Setup isn't available right now. Try again in a moment."))
		} else {
			a.html(c, http.StatusServiceUnavailable, "setup-unlock.html", gin.H{"Error": "Setup isn't available right now. Try again in a moment."})
		}
		return
	}
	if ready {
		c.Redirect(http.StatusSeeOther, "/")
		return
	}
	if !a.setupAuthorized(c) {
		a.html(c, http.StatusOK, "setup-unlock.html", nil)
		return
	}
	a.html(c, http.StatusOK, "setup.html", (setupInput{}).pageData(nil, ""))
}

func (a *App) handleSetup(c *gin.Context) {
	if !a.setupAuthorized(c) {
		c.String(http.StatusForbidden, "Enter the setup code first.")
		return
	}
	in := readSetupInput(c)
	ready, err := a.setupComplete()
	if err != nil {
		log.Printf("Setup availability check failed: %v", err)
		a.html(c, http.StatusServiceUnavailable, "setup.html", in.pageData(nil, "Setup isn't available right now. Try again in a moment."))
		return
	}
	if ready {
		c.Redirect(http.StatusSeeOther, "/")
		return
	}
	if fields := in.fieldErrors(); len(fields) > 0 {
		a.html(c, http.StatusBadRequest, "setup.html", in.pageData(fields, ""))
		return
	}

	admin := models.User{AccountType: models.AccountAdult, Email: in.Email, Name: in.Name, Role: models.RoleAdmin, Status: models.UserActive}
	err = admin.SetPassword(in.Password)
	if err == nil {
		err = a.createFirstAdmin(c.Request.Context(), &admin, in)
	}
	if err == errSetupComplete {
		c.Redirect(http.StatusSeeOther, "/")
		return
	}
	if err != nil {
		log.Printf("Setup failed creating admin: %v", err)
		a.html(c, http.StatusBadRequest, "setup.html", in.pageData(nil, "We couldn't create your account. Try again."))
		return
	}
	a.setupMu.Lock()
	a.setupDone = true
	a.setupMu.Unlock()
	log.Printf("Setup complete: site=%q timezone=%q", in.SiteName, in.Timezone)
	c.Redirect(http.StatusSeeOther, "/login")
}

// maxRequestBytes is the largest request body accepted: one image upload
// plus room for the multipart envelope.
const maxRequestBytes = images.MaxUploadBytes + 1<<20
