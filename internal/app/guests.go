package app

import (
	"database/sql"
	"errors"
	"net/http"

	"fold/internal/accounts"
	"fold/internal/events"
	"fold/internal/mail"
	"fold/internal/people"
	"fold/internal/policy"
	"fold/internal/write"
)

// A stranger on a public date RSVPs as a guest (R15, R35, DEC-55, DEC-56):
// a short form, one answer for every address, a confirm link by email, and
// a manage link to that one date once it is confirmed.

// guestOpen reports whether a date takes guests now: the viewer is signed
// out and a signed-out visitor may answer it.
func (a *App) guestOpen(f eventFacts) bool {
	return f.someone() == nil && policy.GuestCanAnswer(f.ev(), f.date.Visibility, f.group.Policy(), f.date.Cancelled, !f.date.EndsAt.After(a.clock.Now()))
}

type guestForm struct {
	Event      events.Event
	Day, When  string
	UID        string
	Name       Field
	Email      Field
	Adult      Choice
	Errors     []FieldError
	SignInHref string
}

func (a *App) guestFormView(f eventFacts, name, email string, adult bool, errs map[string]string) guestForm {
	loc := viewerZone(f.pageFacts)
	start, end := f.date.StartsAt.In(loc), f.date.EndsAt.In(loc)
	v := guestForm{Event: f.event, Day: longDay(start), When: clockSpan(start, end), UID: f.date.UID, SignInHref: "/signin",
		Name: Field{ID: "name", Name: "name", Label: "Your name", Hint: "So the people running it know who's coming.", Value: name,
			Autocomplete: "name", MaxLength: maxName, Required: true, Error: errs["name"]},
		Email: Field{ID: "email", Name: "email", Label: "Your email", Hint: "We'll send a link to confirm. Nothing is saved until you open it.",
			Type: "email", Value: email, Autocomplete: "email", MaxLength: maxEmail, Required: true, Error: errs["email"]},
		Adult: Choice{ID: "adult", Name: "adult", Type: "checkbox", Value: "yes", Label: "I am 18 or older", Checked: adult, Error: errs["adult"]}}
	if errs == nil {
		v.Name.Autofocus = true
	} else {
		focusFirstError(&v.Name, &v.Email)
	}
	v.Errors = errorSummary(v.Name, v.Email)
	if errs["adult"] != "" {
		v.Errors = append(v.Errors, FieldError{ID: "adult", Message: errs["adult"]})
	}
	return v
}

func (a *App) getGuest(w http.ResponseWriter, r *http.Request) {
	f, ok := a.loadEvent(w, r, true, nil)
	if !ok {
		return
	}
	if !a.guestOpen(f) {
		http.Redirect(w, r, "/dates/"+f.date.UID, http.StatusSeeOther)
		return
	}
	f.Data = a.guestFormView(f, "", "", false, nil)
	a.render(w, r, http.StatusOK, "guest_form", "", f.page)
}

// postGuest answers the same for every address (docs/HAZARDS.md section
// 3): each field is checked for length and characters before anything
// else, the limits are spent in memory, and the request waits in the queue,
// which looks up no person at all.
func (a *App) postGuest(w http.ResponseWriter, r *http.Request) {
	name, nameOK := text(r, "name", maxName)
	raw, emailOK := text(r, "email", maxEmail)
	email := people.NormalizeEmail(raw)
	adult := r.PostFormValue("adult") == "yes"
	errs := map[string]string{}
	switch {
	case !nameOK:
		errs["name"] = "That name is too long or has characters we can't use."
	case name == "":
		errs["name"] = "Enter your name."
	}
	if !emailOK || !mail.ValidAddress(email) {
		errs["email"] = "Enter an email address like name@example.org."
	}
	if !adult {
		errs["adult"] = "Guests need to be 18 or older. A parent or guardian can RSVP instead."
	}
	f, ok := a.loadEvent(w, r, true, nil)
	if !ok {
		return
	}
	if !a.guestOpen(f) {
		http.Redirect(w, r, "/dates/"+f.date.UID, http.StatusSeeOther)
		return
	}
	if len(errs) > 0 {
		if !nameOK {
			name = ""
		}
		if !emailOK {
			raw = ""
		}
		f.Data = a.guestFormView(f, name, raw, adult, errs)
		a.render(w, r, http.StatusUnprocessableEntity, "guest_form", "", f.page)
		return
	}
	a.markDevice(w, email)
	if a.allowStrangerMail(r, email) && a.limits.guests.Allow("site") {
		a.queue.Add(accounts.RequestGuest{Name: name, Email: email, DateUID: f.date.UID})
	}
	f.Data = struct {
		Email string
		Event events.Event
		UID   string
	}{email, f.event, f.date.UID}
	a.render(w, r, http.StatusOK, "guest_sent", "", f.page)
}

// useGuestLink opens a confirm link: the button's POST, or opening it in the
// browser that asked (R26).
func (a *App) useGuestLink(w http.ResponseWriter, r *http.Request, token string) {
	c := &accounts.UseGuestLink{Token: token}
	err := a.exec.Run(r.Context(), write.Anonymous(), c)
	if errors.Is(err, write.ErrNotAllowed) {
		a.showLink(w, r, false)
		return
	}
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.setCookie(w, "device", "", -1)
	switch c.Outcome {
	case policy.ConfirmNew, policy.ConfirmAsThem:
		a.setCookie(w, "session", c.SessionToken, accounts.SessionLife)
		http.Redirect(w, r, "/dates/"+c.DateUID+"?done=guest", http.StatusSeeOther)
		return
	}
	var f pageFacts
	if err := a.reader.Read(r.Context(), func(q *sql.Tx) error {
		var err error
		f, err = a.load(r.Context(), r, q)
		return err
	}); err != nil {
		a.serverError(w, r, err)
		return
	}
	// Only the owner of the address reads this: only they opened the email.
	lp := linkPage{Message: true, Title: "Your RSVP could not be saved", Text: "Please get in touch with the church to RSVP to this date.",
		Next: &LinkButton{Kind: "primary", Label: "See what's coming up", Href: "/groups"}}
	if c.Outcome == policy.ConfirmDateGone {
		lp.Title, lp.Text = "This date isn't taking RSVPs now", "It changed after you asked, so your RSVP could not be saved. Nothing about you was kept."
	}
	f.Data = lp
	a.render(w, r, http.StatusOK, "link", "", f.page)
}

// The manage link's page (DEC-55): the date and a Cancel. It never acts on
// a GET, signs nobody in, and every link that does not work answers alike.

func (a *App) showManage(w http.ResponseWriter, r *http.Request, status string) {
	token := r.PathValue("token")
	var f pageFacts
	var m accounts.Manage
	var d events.Row
	var e events.Event
	err := a.reader.Read(r.Context(), func(q *sql.Tx) error {
		var err error
		if f, err = a.load(r.Context(), r, q); err != nil {
			return err
		}
		if m, err = accounts.LookUpManage(r.Context(), q, token, a.clock.Now()); err != nil || !m.Works {
			return err
		}
		d, e, err = events.DateByUID(r.Context(), q, m.DateUID)
		return err
	})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if !m.Works {
		f.Data = struct{ Works bool }{false}
		a.render(w, r, http.StatusNotFound, "manage", "", f.page)
		return
	}
	loc := churchZone(f)
	start, end := d.StartsAt.In(loc), d.EndsAt.In(loc)
	answer := "You're going."
	if m.Answer == policy.AnswerWaiting {
		answer = "You're on the waitlist. If a seat opens, you'll move up and we'll email you."
	}
	f.Data = struct {
		Works               bool
		Event               events.Event
		Day, When, ZoneLine string
		Answer, Status      string
		Cancelled           bool
		Action              string
	}{true, e, longDay(start), clockSpan(start, end), "Times are in " + events.ZoneCity(loc.String()) + " time.", answer, status, d.Cancelled, "/g/" + token}
	a.render(w, r, http.StatusOK, "manage", "", f.page)
}

func (a *App) getManage(w http.ResponseWriter, r *http.Request) { a.showManage(w, r, "") }

func (a *App) postManage(w http.ResponseWriter, r *http.Request) {
	c := &accounts.CancelByManageLink{Token: r.PathValue("token")}
	err := a.exec.Run(r.Context(), write.Anonymous(), c)
	switch {
	case errors.Is(err, write.ErrNotAllowed):
		a.showManage(w, r, "")
		return
	case err != nil:
		a.serverError(w, r, err)
		return
	}
	var f pageFacts
	if err := a.reader.Read(r.Context(), func(q *sql.Tx) error {
		var err error
		f, err = a.load(r.Context(), r, q)
		return err
	}); err != nil {
		a.serverError(w, r, err)
		return
	}
	f.Data = linkPage{Message: true, Title: "Your RSVP is cancelled", Text: "Thanks for letting us know. This link won't work any more; you can RSVP again from the date's page if your plans change.",
		Next: &LinkButton{Kind: "primary", Label: "Back to the date", Href: "/dates/" + c.DateUID}}
	a.render(w, r, http.StatusOK, "link", "", f.page)
}

// postGuestCancel is a signed-in guest's Cancel on the date page.
func (a *App) postGuestCancel(w http.ResponseWriter, r *http.Request) {
	f, ok := a.loadEvent(w, r, true, nil)
	if !ok {
		return
	}
	if f.guest == nil {
		a.notFound(w, r)
		return
	}
	if !a.limits.answers.Allow(f.guest.Person.UID) {
		http.Redirect(w, r, "/dates/"+f.date.UID+"?done=many", http.StatusSeeOther)
		return
	}
	err := a.exec.Run(r.Context(), write.Session(accounts.Hash(a.sessionToken(r))), accounts.GuestCancel{DateUID: f.date.UID})
	switch {
	case errors.Is(err, write.ErrNotAllowed):
		http.Redirect(w, r, "/dates/"+f.date.UID+"?done=gone", http.StatusSeeOther)
		return
	case errors.Is(err, write.ErrSignedOut):
		http.Redirect(w, r, "/signin", http.StatusSeeOther)
		return
	case err != nil:
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/dates/"+f.date.UID+"?done=guest-cancelled", http.StatusSeeOther)
}
