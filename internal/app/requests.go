package app

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"time"

	"fold/internal/accounts"
	"fold/internal/httpx"
	"fold/internal/mail"
	"fold/internal/notices"
	"fold/internal/people"
	"fold/internal/policy"
	"fold/internal/settings"
	"fold/internal/write"
)

// The account request form, the bell, and the admin's list of requests.

func (a *App) requestForm(name, email, age string, errs map[string]string) any {
	nameF := Field{ID: "name", Name: "name", Label: "Your name", Hint: "As people at church know you.", Value: name,
		Autocomplete: "name", MaxLength: maxName, Required: true, Error: errs["name"]}
	emailF := Field{ID: "email", Name: "email", Label: "Your email", Hint: "You'll sign in with it, and we'll write to you there.",
		Type: "email", Value: email, Autocomplete: "email", MaxLength: maxEmail, Required: true, Error: errs["email"]}
	ages := []Choice{
		{ID: "age-adult", Name: "age", Type: "radio", Value: "adult", Label: "I am 18 or older", Checked: age == "adult", Error: errs["age"]},
		{ID: "age-young", Name: "age", Type: "radio", Value: "young", Label: "I am under 18", Checked: age == "young"},
	}
	if errs == nil {
		nameF.Autofocus = true
	} else {
		focusFirstError(&nameF, &emailF)
	}
	sum := errorSummary(nameF, emailF)
	if errs["age"] != "" {
		sum = append(sum, FieldError{ID: "age-adult", Message: errs["age"]})
	}
	return struct {
		Name, Email Field
		Ages        []Choice
		Errors      []FieldError
	}{nameF, emailF, ages, sum}
}

func (a *App) getRequest(w http.ResponseWriter, r *http.Request) {
	a.renderPlain(w, r, http.StatusOK, "request", a.requestForm("", "", "", nil))
}

// postRequest answers the same for every address (docs/HAZARDS.md section
// 3). It spends the same per-address allowance as the sign-in form, so the
// two together cannot flood an inbox, and the account work happens in the
// queue.
func (a *App) postRequest(w http.ResponseWriter, r *http.Request) {
	name, nameOK := text(r, "name", maxName)
	raw, emailOK := text(r, "email", maxEmail)
	email := people.NormalizeEmail(raw)
	age := r.PostFormValue("age")
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
	switch age {
	case "young":
		// Accounts for under-18s open with slice 5; nothing is stored.
		a.renderPlain(w, r, http.StatusOK, "request_young", nil)
		return
	case "adult":
	default:
		errs["age"] = "Choose whether you are 18 or older."
	}
	if len(errs) > 0 {
		a.renderPlain(w, r, http.StatusUnprocessableEntity, "request", a.requestForm(name, raw, age, errs))
		return
	}
	a.markDevice(w, email)
	if a.allowStrangerMail(r, email) {
		a.queue.Add(accounts.RequestAccount{Name: name, Email: email})
	}
	a.renderPlain(w, r, http.StatusOK, "request_sent", struct{ Email string }{email})
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

func (a *App) noticeReader(f pageFacts) notices.Reader {
	return notices.Reader{ID: f.session.Person.ID, Admin: policy.IsAdmin(f.session.Person.Policy())}
}

func (a *App) getNotices(w http.ResponseWriter, r *http.Request) {
	var list []notices.Notice
	f, ok := a.signedIn(w, r, func(q *sql.Tx, f *pageFacts) error {
		var err error
		list, err = notices.List(r.Context(), q, a.noticeReader(*f), 100)
		return err
	})
	if !ok {
		return
	}
	zone := churchZone(f)
	items := make([]NoticeItem, 0, len(list))
	for _, n := range list {
		items = append(items, NoticeItem{ID: n.ID, Text: n.Text, When: n.At.In(zone).Format("Mon 2 Jan, 3:04 pm"), Read: n.Read, CSRF: a.csrfToken(r)})
	}
	f.Data = struct{ Notices []NoticeItem }{items}
	a.render(w, r, http.StatusOK, "notices", "", f.page)
}

// postNoticeOpen marks a notice read and goes where it points.
func (a *App) postNoticeOpen(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var n notices.Notice
	var gone bool
	_, ok := a.signedIn(w, r, func(q *sql.Tx, f *pageFacts) error {
		var err error
		n, err = notices.One(r.Context(), q, a.noticeReader(*f), id)
		if errors.Is(err, notices.ErrGone) {
			gone, err = true, nil
		}
		return err
	})
	if !ok {
		return
	}
	if gone {
		http.Redirect(w, r, "/notices", http.StatusSeeOther)
		return
	}
	if err := a.exec.Run(r.Context(), write.Session(accounts.Hash(a.sessionToken(r))), notices.Open{ID: id}); err != nil && !errors.Is(err, write.ErrSignedOut) {
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, n.Href, http.StatusSeeOther)
}

func (a *App) postNoticesRead(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.signedIn(w, r, nil); !ok {
		return
	}
	if err := a.exec.Run(r.Context(), write.Session(accounts.Hash(a.sessionToken(r))), notices.ReadAll{}); err != nil && !errors.Is(err, write.ErrSignedOut) {
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/notices", http.StatusSeeOther)
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

func (a *App) getAdminRequests(w http.ResponseWriter, r *http.Request) {
	var list []accounts.Request
	f, ok := a.adminPage(w, r, func(q *sql.Tx, f *pageFacts) error {
		var err error
		list, err = accounts.Waiting(r.Context(), q)
		return err
	})
	if !ok {
		return
	}
	status := map[string]string{
		"approved": "Approved. They've been sent a sign-in link.",
		"declined": "Declined. Nothing about them was kept.",
		"gone":     "That request was already decided, so nothing changed.",
	}[r.URL.Query().Get("done")]
	type row struct {
		accounts.Request
		Decline Dialog
	}
	var rows []row
	for _, req := range list {
		rows = append(rows, row{Request: req, Decline: Dialog{ID: "decline-" + req.UID, OpenLabel: "Decline", OpenKind: "danger-outline",
			Title: "Decline " + req.Name + "'s request?", Text: "They won't be told why, and nothing about them is kept. They can ask again later.",
			CancelLabel: "Keep it", ConfirmLabel: "Decline", ConfirmKind: "danger",
			Action: "/admin/requests/" + req.UID + "/decline", CSRF: a.csrfToken(r)}})
	}
	f.Data = struct {
		Requests []row
		Status   string
	}{rows, status}
	a.render(w, r, http.StatusOK, "admin_requests", "", f.page)
}

func (a *App) postDecideRequest(approve bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := a.adminPage(w, r, nil); !ok {
			return
		}
		err := a.exec.Run(r.Context(), write.Session(accounts.Hash(a.sessionToken(r))),
			accounts.DecideAccount{PersonUID: r.PathValue("uid"), Approve: approve})
		done := "declined"
		if approve {
			done = "approved"
		}
		switch {
		case errors.Is(err, write.ErrNotAllowed):
			done = "gone"
		case errors.Is(err, write.ErrSignedOut):
			http.Redirect(w, r, "/signin", http.StatusSeeOther)
			return
		case err != nil:
			a.serverError(w, r, err)
			return
		}
		http.Redirect(w, r, "/admin/requests?done="+done, http.StatusSeeOther)
	}
}

// churchZone is the church's time zone, for times shown before a person has
// a zone of their own (DEC-58 arrives in slice 2).
func churchZone(f pageFacts) *time.Location {
	if loc, err := time.LoadLocation(f.setup[settings.ChurchZone]); err == nil && f.setup[settings.ChurchZone] != "" {
		return loc
	}
	return time.UTC
}
