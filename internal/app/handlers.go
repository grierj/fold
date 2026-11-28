package app

import (
	"database/sql"
	"errors"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"fold/internal/accounts"
	"fold/internal/events"
	"fold/internal/groups"
	"fold/internal/mail"
	"fold/internal/people"
	"fold/internal/policy"
	"fold/internal/write"
	"fold/web"
)

// home greets a signed-in person and leads with what is coming up for them
// (DEC-94): their next dates from the calendar's Just mine, and their
// groups, or a few groups meeting soon when they are in none. A visitor
// sees the church's public events and groups; a guest, their RSVPs. Before
// setup it sends the visitor to setup.
func (a *App) home(w http.ResponseWriter, r *http.Request) {
	now := a.clock.Now()
	var f pageFacts
	var list []groups.Listed
	var rsvps []events.Listed
	var coming []events.CalDate
	var soon []events.SoonGroup
	err := a.reader.Read(r.Context(), func(q *sql.Tx) error {
		var err error
		if f, err = a.load(r.Context(), r, q); err != nil {
			return err
		}
		if f.guest != nil {
			// Read as a signed-out viewer: a date since made private drops
			// out of a guest's list (R15).
			if rsvps, err = events.Upcoming(r.Context(), q, events.Viewer{}, events.Filter{Answerer: f.guest.Person.ID}, now, 50); err != nil {
				return err
			}
		}
		if f.guest == nil {
			s := events.Scope{Viewer: listViewer(f), JustMine: f.session != nil}
			if f.session != nil {
				s.Answerer = f.session.Person.ID
			}
			// A date lasts at most a week, so one that began a week ago may
			// still be on.
			if coming, err = events.Calendar(r.Context(), q, s, now.Add(-events.MaxMinutes*time.Minute), now.AddDate(1, 0, 0), now, now, 4); err != nil {
				return err
			}
		}
		if f.session == nil {
			list, err = groups.Directory(r.Context(), q, groups.Viewer{}, groups.Find{}, now, 12)
			return err
		}
		if list, err = groups.Mine(r.Context(), q, groups.Viewer(listViewer(f)), now); err != nil || len(list) > 0 {
			return err
		}
		soon, err = events.JoinableSoon(r.Context(), q, listViewer(f), now, 3)
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
	if r.URL.Query().Get("done") == "event-deleted" && f.Viewer != nil {
		status = "The event is deleted."
	}
	loc := viewerZone(f)
	kind := policy.ViewerSignedOut
	if f.session != nil {
		kind = policy.ViewerMember
	}
	rows := make([]calRow, 0, len(coming))
	for _, c := range coming {
		_, own := policy.InScope(f.session != nil, c.Facts(kind), now)
		row := calendarRow(c, own, loc, now)
		row.When = span(c.StartsAt, c.EndsAt, loc, now)
		rows = append(rows, row)
	}
	var meeting []soonGroup
	for _, g := range soon {
		meeting = append(meeting, soonGroup{UID: g.UID, Name: g.Name, Next: meetingWords(g.Next, loc, now)})
	}
	f.Data = struct {
		Status   string
		Greeting string
		Groups   []groupItem
		Guest    bool
		RSVPs    []dateItem
		Coming   []calRow
		Soon     []soonGroup
	}{status, greeting(now.In(loc)), groupItems(list, loc, now), f.guest != nil, dateItems(rsvps, churchZone(f), now, true), rows, meeting}
	a.render(w, r, http.StatusOK, "home", "", f.page)
}

// soonGroup is a group meeting soon that a person in no group may join.
type soonGroup struct{ UID, Name, Next string }

// greeting is "Good morning" before noon, "Good afternoon" before 5 pm,
// and "Good evening" after, on the viewer's clock.
func greeting(t time.Time) string {
	switch {
	case t.Hour() < 12:
		return "Good morning"
	case t.Hour() < 17:
		return "Good afternoon"
	}
	return "Good evening"
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
	a.markDevice(w, email)
	if a.allowStrangerMail(r, email) {
		a.queue.Add(accounts.RequestSignIn{Email: email})
	}
	a.renderPlain(w, r, http.StatusOK, "signin_sent", struct{ Email string }{email})
}

// The page behind an emailed link. Opened in the browser that asked for the
// link, it acts at once (R26). Anywhere else opening it changes nothing and
// only its button's POST acts, because mail scanners open links
// (docs/HAZARDS.md section 9).
type linkPage struct {
	Usable  bool
	Message bool // a page saying what happened, in place of the link's

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
		info, err = accounts.LookUpLink(r.Context(), q, a.keys, token, a.clock.Now())
		return err
	})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if mayAct && r.Method == http.MethodGet && personOpened(r) && policy.LinkActsWhenOpened(info.Usable, a.sameDevice(r, info.Email)) {
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
	case "guest":
		lp.Title, lp.Text, lp.Button = "Confirm your RSVP", "Press the button to say you're going. Until you do, nothing is saved.", "Confirm my RSVP"
		lp.Next = &LinkButton{Kind: "primary", Label: "See what's coming up", Href: "/groups"}
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
		info, err = accounts.LookUpLink(r.Context(), q, a.keys, token, a.clock.Now())
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
	case "guest":
		a.useGuestLink(w, r, token)
		return
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

// viewerFacts turns the loaded session into policy's terms; nil is signed
// out.
func viewerFacts(s *people.Session) *policy.Person {
	if s == nil {
		return nil
	}
	return s.Person.Policy()
}
