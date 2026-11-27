package app

import (
	"database/sql"
	"errors"
	"io/fs"
	"net/http"
	"strings"

	"fold/internal/accounts"
	"fold/internal/httpx"
	"fold/internal/mail"
	"fold/internal/people"
	"fold/internal/policy"
	"fold/internal/write"
	"fold/web"
)

// home is the signed-in home page, or the church's front page. Before setup
// it sends the visitor to setup.
func (a *App) home(w http.ResponseWriter, r *http.Request) {
	var f pageFacts
	err := a.reader.Read(r.Context(), func(q *sql.Tx) error {
		var err error
		f, err = a.load(r.Context(), r, q)
		return err
	})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if !f.setup.Done() {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	status := ""
	if r.URL.Query().Get("welcome") == "1" && f.Viewer != nil {
		status = "You're signed in."
	}
	f.Data = struct{ Status string }{status}
	a.render(w, r, http.StatusOK, "home", "", f.page)
}

func (a *App) signinForm(email, problem, status string) any {
	f := Field{ID: "email", Name: "email", Label: "Email", Type: "email", Value: email, Error: problem,
		Autocomplete: "email", MaxLength: maxEmail, Required: true, Autofocus: true}
	return struct {
		Email  Field
		Errors []FieldError
		Status string
	}{f, errorSummary(f), status}
}

func (a *App) getSignin(w http.ResponseWriter, r *http.Request) {
	status := ""
	if r.URL.Query().Get("out") == "1" {
		status = "You're signed out."
	}
	a.renderPlain(w, r, http.StatusOK, "signin", a.signinForm("", "", status))
}

// postSignin gives one answer for every address (docs/HAZARDS.md section 3).
// The address is checked for shape before anything else, the limits are
// spent in memory, and the lookup happens later in the queue, so neither the
// answer nor its timing depends on whether the address has an account.
func (a *App) postSignin(w http.ResponseWriter, r *http.Request) {
	raw, ok := text(r, "email", maxEmail)
	email := people.NormalizeEmail(raw)
	if !ok || !mail.ValidAddress(email) {
		a.renderPlain(w, r, http.StatusUnprocessableEntity, "signin",
			a.signinForm(raw, "Enter an email address like name@example.org.", ""))
		return
	}
	// Limits are spent in order and stop at the first refusal. Counting an
	// address per client first means one stranger cannot use up the
	// address's own allowance and lock its owner out for the day.
	a.markDevice(w, email)
	client := httpx.ClientKey(a.cfg.Proxies.ClientIP(r))
	switch {
	case !a.limits.signinClient.Allow(client):
		a.log.Warn("signin: client limit reached; answered as usual")
	case !a.limits.signinPair.Allow(client + " " + email):
		a.log.Warn("signin: address limit for this client reached; answered as usual")
	case !a.limits.signinAddress.Allow(email):
		a.log.Warn("signin: address limit reached; answered as usual")
	default:
		a.queue.Add(email)
	}
	a.renderPlain(w, r, http.StatusOK, "signin_sent", struct{ Email string }{email})
}

// The page behind an emailed link. Opened in the browser that asked for the
// link, it acts at once (R26). Anywhere else opening it changes nothing and
// only its button's POST acts, because mail scanners open links
// (docs/HAZARDS.md section 9).
type linkPage struct {
	Usable                      bool
	Title, Text, Button, Action string
	Next                        *LinkButton // what to do when the link no longer works
}

func (a *App) getLink(w http.ResponseWriter, r *http.Request) { a.showLink(w, r, true) }

// showLink draws the link's page; mayAct lets a same-device open act.
func (a *App) showLink(w http.ResponseWriter, r *http.Request, mayAct bool) {
	token := r.PathValue("token")
	var f pageFacts
	var info accounts.LinkInfo
	err := a.reader.Read(r.Context(), func(q *sql.Tx) error {
		var err error
		if f, err = a.load(r.Context(), r, q); err != nil {
			return err
		}
		info, err = accounts.LookUpLink(r.Context(), q, token, a.clock.Now())
		return err
	})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if mayAct && r.Method == http.MethodGet && policy.LinkActsWhenOpened(info.Usable, a.sameDevice(r, info.Email)) {
		a.useLink(w, r, token, info.Purpose)
		return
	}
	purpose, usable := info.Purpose, info.Usable
	lp := linkPage{Usable: usable, Action: "/l/" + token,
		Next: &LinkButton{Kind: "primary", Label: "Get a new sign-in link", Href: "/signin"}}
	switch purpose {
	case "setup":
		lp.Title, lp.Text, lp.Button = "Finish setting up", "Your mail settings work. Press the button to finish setup and sign in as the first administrator.", "Finish setup"
		// Before setup is done there is no account to sign in to: the way on
		// is another test message.
		if !f.setup.Done() {
			lp.Next = &LinkButton{Kind: "primary", Label: "Back to setup", Href: "/setup"}
		}
	default:
		church := f.Church
		lp.Title, lp.Text, lp.Button = "Sign in to "+church, "Press the button to sign in on this device.", "Sign in"
	}
	f.Data = lp
	a.render(w, r, http.StatusOK, "link", "", f.page)
}

func (a *App) postLink(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	var info accounts.LinkInfo
	err := a.reader.Read(r.Context(), func(q *sql.Tx) error {
		var err error
		info, err = accounts.LookUpLink(r.Context(), q, token, a.clock.Now())
		return err
	})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.useLink(w, r, token, info.Purpose)
}

// useLink runs the link's command: the button's POST, or opening the link in
// the browser that asked for it.
func (a *App) useLink(w http.ResponseWriter, r *http.Request, token, purpose string) {
	var err error
	var session string
	switch purpose {
	case "setup":
		c := &accounts.FinishSetup{Token: token}
		err = a.exec.Run(r.Context(), write.Anonymous(), c)
		session = c.SessionToken
		if err == nil {
			// Setup is done; the code is no longer needed.
			a.setup.mu.Lock()
			a.setup.code = ""
			a.setup.mu.Unlock()
			a.setCookie(w, "setup", "", -1)
		}
	default:
		c := &accounts.UseSignInLink{Token: token}
		err = a.exec.Run(r.Context(), write.Anonymous(), c)
		session = c.SessionToken
	}
	if errors.Is(err, write.ErrNotAllowed) {
		// Used, replaced or expired since the page was drawn: show the page,
		// which now says so.
		a.showLink(w, r, false)
		return
	}
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.setCookie(w, "device", "", -1)
	a.setCookie(w, "session", session, accounts.SessionLife)
	http.Redirect(w, r, "/?welcome=1", http.StatusSeeOther)
}

func (a *App) postSignout(w http.ResponseWriter, r *http.Request) {
	if t := a.sessionToken(r); t != "" {
		err := a.exec.Run(r.Context(), write.Session(accounts.Hash(t)), accounts.SignOut{})
		if err != nil && !errors.Is(err, write.ErrSignedOut) {
			a.serverError(w, r, err)
			return
		}
	}
	a.setCookie(w, "session", "", -1)
	http.Redirect(w, r, "/signin?out=1", http.StatusSeeOther)
}

// static serves the embedded files. A URL carrying the file's hash may be
// cached for a year; the hash changes when the file does.
func (a *App) static() http.Handler {
	sub, _ := fs.Sub(web.Static, "static")
	files := http.FileServerFS(sub)
	return http.StripPrefix("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") || r.URL.Path == "" {
			a.notFound(w, r)
			return
		}
		if h, ok := a.tmpl.assets[r.URL.Path]; ok && r.URL.Query().Get("v") == h {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=300")
		}
		files.ServeHTTP(w, r)
	}))
}

func (a *App) healthz(w http.ResponseWriter, r *http.Request) {
	err := a.reader.Read(r.Context(), func(q *sql.Tx) error {
		var one int
		return q.QueryRowContext(r.Context(), "SELECT 1").Scan(&one)
	})
	if err != nil {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte("ok\n"))
}

// devOutbox shows the mail kept in development. The route exists only in
// development mode, which refuses to run on a public address.
func (a *App) devOutbox(w http.ResponseWriter, r *http.Request) {
	a.renderPlain(w, r, http.StatusOK, "outbox", struct{ Sent []mail.Sent }{a.outbox.All()})
}

// viewerFacts turns the loaded session into policy's terms.
func viewerFacts(s *people.Session) policy.Viewer {
	if s == nil {
		return policy.Viewer{}
	}
	return policy.Viewer{SignedIn: true, Active: s.Person.Status == people.StatusActive, Admin: s.Person.IsAdmin}
}
