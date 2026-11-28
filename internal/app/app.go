// Package app is the web server: the routes, the request plumbing and the
// handlers. Handlers load facts in one read, ask policy, and render; every
// change goes through write.Executor.
package app

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"fold/internal/accounts"
	"fold/internal/clock"
	"fold/internal/db"
	"fold/internal/httpx"
	"fold/internal/keys"
	"fold/internal/mail"
	"fold/internal/notices"
	"fold/internal/people"
	"fold/internal/ratelimit"
	"fold/internal/settings"
	"fold/internal/write"
)

// Config is what the server needs from its environment.
type Config struct {
	SiteURL *url.URL // https://church.example.org, or http://localhost:8080 in dev
	Dev     bool
	Proxies httpx.Proxies
}

// App is the server's state. It caches nothing read from the database: a
// restore can change anything, and every page reads what it shows.
type App struct {
	cfg    Config
	reader *db.Reader
	exec   *write.Executor
	queue  *accounts.Queue
	keys   *keys.Keys
	clock  clock.Clock
	log    *slog.Logger
	outbox *mail.Outbox
	tmpl   *templates
	limits limits
	setup  setupCode
	secure bool // cookies are Secure and __Host- prefixed
	origin string
}

type limits struct {
	signinPair    *ratelimit.Limiter // one address from one client
	signinAddress *ratelimit.Limiter // one address from anywhere: the inbox flood cap
	signinClient  *ratelimit.Limiter // anything from one client
	setupCode     *ratelimit.Limiter // setup code guesses from one client
	setupMail     *ratelimit.Limiter // test messages sent during setup
	requests      *ratelimit.Limiter // account requests from the whole site
	newGroups     *ratelimit.Limiter // groups one person starts
	groupActions  *ratelimit.Limiter // other changes one person makes to groups
	newEvents     *ratelimit.Limiter // events one person schedules
	eventChanges  *ratelimit.Limiter // edits one person makes to events
	invites       *ratelimit.Limiter // invitations sent by one person
	invitesSite   *ratelimit.Limiter // invitations sent from the whole site
}

type Deps struct {
	Reader *db.Reader
	Exec   *write.Executor
	Queue  *accounts.Queue
	Keys   *keys.Keys
	Clock  clock.Clock
	Log    *slog.Logger
	Outbox *mail.Outbox
}

func New(cfg Config, d Deps) (*App, error) {
	t, err := loadTemplates()
	if err != nil {
		return nil, err
	}
	c := d.Clock
	a := &App{cfg: cfg, reader: d.Reader, exec: d.Exec, queue: d.Queue, keys: d.Keys, clock: c, log: d.Log,
		outbox: d.Outbox, tmpl: t, secure: cfg.SiteURL.Scheme == "https",
		origin: cfg.SiteURL.Scheme + "://" + cfg.SiteURL.Host,
		limits: limits{
			signinPair:    ratelimit.New(c, ratelimit.Rule{N: 3, Per: time.Hour}, ratelimit.Rule{N: 10, Per: 24 * time.Hour}),
			signinAddress: ratelimit.New(c, ratelimit.Rule{N: 6, Per: time.Hour}, ratelimit.Rule{N: 20, Per: 24 * time.Hour}),
			signinClient:  ratelimit.New(c, ratelimit.Rule{N: 20, Per: time.Hour}, ratelimit.Rule{N: 100, Per: 24 * time.Hour}),
			setupCode:     ratelimit.New(c, ratelimit.Rule{N: 10, Per: time.Hour}),
			setupMail:     ratelimit.New(c, ratelimit.Rule{N: 10, Per: time.Hour}, ratelimit.Rule{N: 30, Per: 24 * time.Hour}),
			requests:      ratelimit.New(c, ratelimit.Rule{N: 20, Per: time.Hour}, ratelimit.Rule{N: 60, Per: 24 * time.Hour}),
			newGroups:     ratelimit.New(c, ratelimit.Rule{N: 5, Per: 24 * time.Hour}),
			groupActions:  ratelimit.New(c, ratelimit.Rule{N: 120, Per: time.Hour}),
			newEvents:     ratelimit.New(c, ratelimit.Rule{N: 20, Per: 24 * time.Hour}),
			eventChanges:  ratelimit.New(c, ratelimit.Rule{N: 120, Per: time.Hour}),
			invites:       ratelimit.New(c, ratelimit.Rule{N: 20, Per: 24 * time.Hour}),
			invitesSite:   ratelimit.New(c, ratelimit.Rule{N: 100, Per: 24 * time.Hour}),
		}}
	return a, nil
}

// maxBody caps every request body. Slice 0 has only short forms.
const maxBody = 64 << 10

// Handler is the whole server, outermost first: access log, security
// headers, body cap, request checks, routes.
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	a.routes(mux)
	var h http.Handler = mux
	h = a.guard(h)
	h = httpx.LimitBody(maxBody, h)
	h = httpx.SecurityHeaders(a.secure, h)
	return httpx.AccessLog(a.log, a.cfg.Proxies.ClientIP, h)
}

// guard runs before every route: pages are never cached, and every POST must
// come from this site and carry the CSRF token.
func (a *App) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isStatic(r) {
			// Nothing a page shows is left in a cache after signing out.
			w.Header().Set("Cache-Control", "no-store")
		}
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			a.ensureCSRFCookie(w, r)
			if !isStatic(r) {
				a.renew(w, r)
			}
		case http.MethodPost:
			if !a.sameOrigin(r) || !a.checkCSRF(r) {
				a.staleForm(w, r)
				return
			}
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isStatic(r *http.Request) bool {
	return len(r.URL.Path) > 8 && r.URL.Path[:8] == "/static/"
}

// sameOrigin compares the browser's Origin header with the site's own URL,
// never with the Host header a request supplies. Browsers that send no
// Origin are judged by Sec-Fetch-Site; the CSRF token is the main defence.
func (a *App) sameOrigin(r *http.Request) bool {
	if o := r.Header.Get("Origin"); o != "" {
		return o == a.origin
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
		return true
	}
	return false
}

// staleForm answers a POST that failed the CSRF or origin check. It is
// almost always a form left open across a sign-in or a restart, so it says
// nothing was sent and offers the page again.
func (a *App) staleForm(w http.ResponseWriter, r *http.Request) {
	a.log.Info("refused a post without a valid token", "path", httpx.RedactPath(r.URL.Path))
	back := "/"
	if ref, err := url.Parse(r.Referer()); err == nil && ref.Scheme+"://"+ref.Host == a.origin && ref.Path != "" {
		back = ref.Path
	}
	a.render(w, r, http.StatusForbidden, "problem", "", page{Data: problem{
		Title: "That page was out of date", Text: "So nothing was sent. The page has a fresh copy now; please try again.",
		Next: "Go back to the page", NextHref: back}})
}

// Cookie names. __Host- cookies must be Secure, so plain-HTTP development
// uses unprefixed names.
func (a *App) cookieName(base string) string {
	if a.secure {
		return "__Host-" + base
	}
	return base
}

func (a *App) setCookie(w http.ResponseWriter, name, value string, maxAge time.Duration) {
	c := &http.Cookie{Name: a.cookieName(name), Value: value, Path: "/", HttpOnly: true, Secure: a.secure,
		SameSite: http.SameSiteLaxMode}
	if maxAge > 0 {
		c.MaxAge = int(maxAge.Seconds())
	} else if maxAge < 0 {
		c.MaxAge = -1
	}
	http.SetCookie(w, c)
}

func (a *App) cookie(r *http.Request, name string) string {
	c, err := r.Cookie(a.cookieName(name))
	if err != nil {
		return ""
	}
	return c.Value
}

// sessionToken is the session cookie's token, if it has the right shape.
func (a *App) sessionToken(r *http.Request) string {
	t := a.cookie(r, "session")
	if !accounts.ValidTokenShape(t) {
		return ""
	}
	return t
}

// pageFacts is what every page's read loads first.
type pageFacts struct {
	page
	session *people.Session
	setup   settings.Settings
}

// load reads the church's settings and the viewer inside the page's one
// read transaction.
func (a *App) load(ctx context.Context, r *http.Request, q *sql.Tx) (pageFacts, error) {
	s, err := settings.Load(ctx, q)
	if err != nil {
		return pageFacts{}, err
	}
	f := pageFacts{page: page{Church: s[settings.ChurchName]}, setup: s}
	if t := a.sessionToken(r); t != "" {
		sess, err := people.BySession(ctx, q, accounts.Hash(t), a.clock.Now())
		if err == nil {
			f.session = &sess
			f.Viewer = &viewerInfo{Name: sess.Person.Name, IsAdmin: sess.Person.IsAdmin}
			if f.Unread, err = notices.Unread(ctx, q, a.noticeReader(sess.Person)); err != nil {
				return pageFacts{}, err
			}
		} else if !errors.Is(err, people.ErrNotFound) {
			return pageFacts{}, err
		}
	}
	return f, nil
}

// renew extends a live session when any page is visited (R8). It runs here,
// for every page, so a new page cannot forget it. It is the one write a GET
// makes, and it is the ruled behaviour of a session; it writes no audit row
// (R19).
func (a *App) renew(w http.ResponseWriter, r *http.Request) {
	token := a.sessionToken(r)
	if token == "" {
		return
	}
	var due bool
	err := a.reader.Read(r.Context(), func(q *sql.Tx) error {
		s, err := people.BySession(r.Context(), q, accounts.Hash(token), a.clock.Now())
		if errors.Is(err, people.ErrNotFound) {
			return nil
		}
		due = err == nil && a.clock.Now().Sub(s.RenewedAt) >= accounts.RenewEvery
		return err
	})
	if err != nil || !due {
		return
	}
	if err := a.exec.Run(r.Context(), write.Session(accounts.Hash(token)), accounts.RenewSession{}); err != nil {
		a.log.Warn("session renew", "err", err)
		return
	}
	a.setCookie(w, "session", token, accounts.SessionLife)
}

// renderPlain renders a page that needs only the church's name and viewer.
func (a *App) renderPlain(w http.ResponseWriter, r *http.Request, status int, name string, data any) {
	var f pageFacts
	err := a.reader.Read(r.Context(), func(q *sql.Tx) error {
		var err error
		f, err = a.load(r.Context(), r, q)
		return err
	})
	if err != nil {
		a.log.Error("read", "err", err)
	}
	f.Data = data
	a.render(w, r, status, name, "", f.page)
}

func constantEqual(a, b string) bool {
	ha, hb := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}
