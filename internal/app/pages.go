package app

import (
	"database/sql"
	"net/http"
	"time"

	"fold/internal/httpx"
	"fold/internal/policy"
	"fold/internal/settings"
)

// Helpers every page shares: who may load it, the church's clock, and the
// public forms' mail allowance.

// signedIn loads a page for a signed-in person, or sends them to sign in.
func (a *App) signedIn(w http.ResponseWriter, r *http.Request, extra func(q *sql.Tx, f *pageFacts) error) (pageFacts, bool) {
	var f pageFacts
	err := a.reader.Read(r.Context(), func(q *sql.Tx) error {
		var err error
		if f, err = a.load(r.Context(), r, q); err != nil || f.session == nil || extra == nil {
			return err
		}
		return extra(q, &f)
	})
	if err != nil {
		a.serverError(w, r, err)
		return f, false
	}
	if f.session == nil {
		http.Redirect(w, r, "/signin", http.StatusSeeOther)
		return f, false
	}
	return f, true
}

// adminPage loads a page only an admin may see; anyone else gets the
// not-found page.
func (a *App) adminPage(w http.ResponseWriter, r *http.Request, extra func(q *sql.Tx, f *pageFacts) error) (pageFacts, bool) {
	var f pageFacts
	var admin bool
	err := a.reader.Read(r.Context(), func(q *sql.Tx) error {
		var err error
		if f, err = a.load(r.Context(), r, q); err != nil {
			return err
		}
		admin = f.session != nil && policy.IsAdmin(f.session.Person.Policy())
		if !admin || extra == nil {
			return nil
		}
		return extra(q, &f)
	})
	if err != nil {
		a.serverError(w, r, err)
		return f, false
	}
	if !admin {
		a.notFound(w, r)
		return f, false
	}
	return f, true
}

// churchZone is the church's time zone, for times shown before a person has
// a zone of their own (DEC-58 arrives in slice 2).
func churchZone(f pageFacts) *time.Location {
	if loc, err := time.LoadLocation(f.setup[settings.ChurchZone]); err == nil && f.setup[settings.ChurchZone] != "" {
		return loc
	}
	return time.UTC
}

// allowStrangerMail spends one unit of the allowance for mailing an address
// from a public form. Limits are spent in order and stop at the first
// refusal: a client, then an address from that client, then the address
// from anywhere, so one stranger cannot use up someone else's allowance.
func (a *App) allowStrangerMail(r *http.Request, email string) bool {
	client := httpx.ClientKey(a.cfg.Proxies.ClientIP(r))
	switch {
	case !a.limits.signinClient.Allow(client):
		a.log.Warn("public form: client limit reached; answered as usual")
	case !a.limits.signinPair.Allow(client + " " + email):
		a.log.Warn("public form: address limit for this client reached; answered as usual")
	case !a.limits.signinAddress.Allow(email):
		a.log.Warn("public form: address limit reached; answered as usual")
	default:
		return true
	}
	return false
}
