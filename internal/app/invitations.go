package app

import (
	"database/sql"
	"errors"
	"net/http"

	"fold/internal/accounts"
	"fold/internal/write"
)

// The invited person's side of an invitation (R32): by name, from their
// notice; by email, from the emailed link.

func (a *App) getInvitation(w http.ResponseWriter, r *http.Request) {
	var v accounts.Invitation
	var mine bool
	f, ok := a.signedIn(w, r, func(q *sql.Tx, f *pageFacts) error {
		var err error
		v, mine, err = accounts.InvitationForPerson(r.Context(), q, r.PathValue("uid"), f.session.Person.ID, a.clock.Now())
		return err
	})
	if !ok {
		return
	}
	if !mine {
		a.notFound(w, r)
		return
	}
	f.Data = v
	a.render(w, r, http.StatusOK, "invitation", "", f.page)
}

func (a *App) postAnswerInvitation(accept bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := a.signedIn(w, r, nil); !ok {
			return
		}
		c := &accounts.AnswerInvitation{UID: r.PathValue("uid"), Accept: accept}
		err := a.exec.Run(r.Context(), write.Session(accounts.Hash(a.sessionToken(r))), c)
		switch {
		case errors.Is(err, write.ErrNotAllowed):
			// Answered, expired, or the inviter may no longer invite: the
			// page says how it stands.
			http.Redirect(w, r, "/invitations/"+r.PathValue("uid"), http.StatusSeeOther)
			return
		case errors.Is(err, write.ErrSignedOut):
			http.Redirect(w, r, "/signin", http.StatusSeeOther)
			return
		case err != nil:
			a.serverError(w, r, err)
			return
		}
		if accept {
			http.Redirect(w, r, "/groups/"+c.Group+"?done=joined", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/notices", http.StatusSeeOther)
	}
}

type redeemPage struct {
	accounts.Invitation
	Name   Field
	Ages   []Choice
	Errors []FieldError
	Action string
}

func (a *App) redeemForm(v accounts.Invitation, token, name, age string, errs map[string]string) redeemPage {
	p := redeemPage{Invitation: v, Action: "/invite/" + token,
		Name: Field{ID: "name", Name: "name", Label: "Your name", Hint: "As people at church know you.", Value: name,
			MaxLength: maxName, Required: true, Autocomplete: "name", Error: errs["name"]},
		Ages: []Choice{
			{ID: "age-adult", Name: "age", Type: "radio", Value: "adult", Label: "I am 18 or older", Checked: age == "adult", Error: errs["age"]},
			{ID: "age-young", Name: "age", Type: "radio", Value: "young", Label: "I am under 18", Checked: age == "young"},
		}}
	p.Errors = errorSummary(p.Name)
	if errs["age"] != "" {
		p.Errors = append(p.Errors, FieldError{ID: "age-adult", Message: errs["age"]})
	}
	return p
}

// getRedeem shows an emailed invitation. Opening it changes nothing.
func (a *App) getRedeem(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	var v accounts.Invitation
	var found bool
	var f pageFacts
	err := a.reader.Read(r.Context(), func(q *sql.Tx) error {
		var err error
		if f, err = a.load(r.Context(), r, q); err != nil {
			return err
		}
		v, found, err = accounts.InvitationForToken(r.Context(), q, token, a.clock.Now())
		return err
	})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if !found {
		v = accounts.Invitation{}
	}
	f.Data = a.redeemForm(v, token, v.Name, "", nil)
	a.render(w, r, http.StatusOK, "redeem", "", f.page)
}

func (a *App) postRedeem(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	name, okName := text(r, "name", maxName)
	age := r.PostFormValue("age")
	errs := map[string]string{}
	if !okName || name == "" {
		errs["name"] = "Enter your name."
	}
	if age != "adult" && age != "young" {
		errs["age"] = "Choose whether you are 18 or older."
	}
	if age == "young" {
		delete(errs, "name") // nothing will be kept, so no name is needed
	}
	if len(errs) > 0 {
		var v accounts.Invitation
		var f pageFacts
		a.reader.Read(r.Context(), func(q *sql.Tx) error {
			var err error
			if f, err = a.load(r.Context(), r, q); err != nil {
				return err
			}
			v, _, err = accounts.InvitationForToken(r.Context(), q, token, a.clock.Now())
			return err
		})
		f.Data = a.redeemForm(v, token, name, age, errs)
		a.render(w, r, http.StatusUnprocessableEntity, "redeem", "", f.page)
		return
	}
	c := &accounts.RedeemInvitation{Token: token, Name: name, Adult: age == "adult"}
	err := a.exec.Run(r.Context(), write.Anonymous(), c)
	switch {
	case errors.Is(err, write.ErrNotAllowed):
		a.getRedeem(w, r) // used, replaced, expired, or the inviter may no longer invite
		return
	case err != nil:
		a.serverError(w, r, err)
		return
	}
	if !c.Adult {
		a.renderPlain(w, r, http.StatusOK, "request_young", nil)
		return
	}
	a.setCookie(w, "session", c.SessionToken, accounts.SessionLife)
	if c.Group != "" {
		http.Redirect(w, r, "/groups/"+c.Group+"?done=joined", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/?welcome=1", http.StatusSeeOther)
}
