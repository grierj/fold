package app

import (
	"database/sql"
	"errors"
	"net/http"

	"fold/internal/accounts"
	"fold/internal/mail"
	"fold/internal/people"
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
	if done, ok := a.setupState(w, r); !ok {
		return
	} else if !done {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
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
	// Requests have their own allowance for the whole site too, since each
	// new address reaches every admin; past it the form answers as usual.
	if a.allowStrangerMail(r, email) {
		if a.limits.requests.Allow("site") {
			a.queue.Add(accounts.RequestAccount{Name: name, Email: email})
		} else {
			a.log.Warn("account requests: site limit reached; answered as usual")
		}
	}
	a.renderPlain(w, r, http.StatusOK, "request_sent", struct{ Email string }{email})
}

func (a *App) getAdminRequests(w http.ResponseWriter, r *http.Request) {
	var list []accounts.Request
	var total int
	f, ok := a.adminPage(w, r, func(q *sql.Tx, f *pageFacts) error {
		var err error
		list, total, err = accounts.Waiting(r.Context(), q, 100)
		return err
	})
	if !ok {
		return
	}
	status := map[string]string{
		"approved": "Approved. They've been sent a sign-in link.",
		"declined": "Declined. Nothing about them was kept.",
		"gone":     "That request was already decided, so nothing changed.",
		"invited":  "Invitation sent. If that address is new, they'll get an email to sign in.",
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
		More     int
		Invite   inviteForm
	}{rows, status, total - len(rows), newInviteForm("/admin/invite", "", "", false, nil)}
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
