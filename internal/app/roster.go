package app

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"fold/internal/accounts"
	"fold/internal/groups"
	"fold/internal/mail"
	"fold/internal/people"
	"fold/internal/policy"
	"fold/internal/write"
)

// The roster: who is in a group, adding people found by name, co-leaders,
// removal, and inviting by email.

type rosterRow struct {
	groups.RosterRow
	ShowEmail                        bool
	CanPromote, CanDemote, CanRemove bool
	Remove                           Dialog
	CSRF                             string
	GroupUID                         string
}

func (a *App) getRoster(w http.ResponseWriter, r *http.Request) {
	find := strings.TrimSpace(r.URL.Query().Get("find"))
	if len(find) > 100 {
		find = find[:100]
	}
	var roster []groups.RosterRow
	var found []groups.Found
	var leaders int
	var leadsActive bool
	var waiting []string
	f, ok := a.loadGroup(w, r, func(q *sql.Tx, f *groupFacts) error {
		if !policy.CanSeeRoster(f.viewer(), f.group.Policy(), f.rel) {
			return nil
		}
		var err error
		if roster, err = groups.Roster(r.Context(), q, f.group.ID); err != nil {
			return err
		}
		if leaders, err = groups.LeaderCount(r.Context(), q, f.group.ID); err != nil {
			return err
		}
		if leadsActive, err = groups.LeadsActive(r.Context(), q, f.session.Person.ID); err != nil {
			return err
		}
		if waiting, err = accounts.OpenByName(r.Context(), q, f.group.ID, a.clock.Now()); err != nil {
			return err
		}
		if len([]rune(find)) >= 2 && policy.CanAddToGroup(f.viewer(), f.group.Policy(), f.rel) && policy.CanSearchPeople(f.viewer(), leadsActive) {
			found, err = groups.FindPeople(r.Context(), q, f.group.ID, find, 20)
		}
		return err
	})
	if !ok {
		return
	}
	viewer, g := f.viewer(), f.group.Policy()
	if !policy.CanSeeRoster(viewer, g, f.rel) {
		a.notFound(w, r)
		return
	}
	rows := make([]rosterRow, 0, len(roster))
	for _, m := range roster {
		target := &policy.Person{ID: m.ID, Admin: m.Admin}
		trel := policy.Rel{Member: true, Leader: m.Leader}
		row := rosterRow{RosterRow: m, GroupUID: f.group.UID, CSRF: a.csrfToken(r),
			ShowEmail:  policy.CanSeeEmail(viewer, f.rel.Leader, target, m.ContactHidden) && m.ID != viewer.ID,
			CanPromote: !m.Leader && policy.CanChangeRole(viewer, g, f.rel, target, trel, true, leaders),
			CanDemote:  m.Leader && m.ID != viewer.ID && policy.CanChangeRole(viewer, g, f.rel, target, trel, false, leaders),
			CanRemove:  policy.CanRemoveMember(viewer, g, f.rel, target, trel, leaders),
		}
		row.Remove = Dialog{ID: "remove-" + m.UID, OpenLabel: "Remove", OpenKind: "ghost",
			Title: "Remove " + m.Name + " from " + f.group.Name + "?", Text: "They won't be able to join again on their own, and they're taken off the group's dates still to come. A leader can add them back.",
			CancelLabel: "Keep them", ConfirmLabel: "Remove", ConfirmKind: "danger",
			Action: "/groups/" + f.group.UID + "/members/" + m.UID + "/remove", CSRF: a.csrfToken(r)}
		rows = append(rows, row)
	}
	canAdd := policy.CanAddToGroup(viewer, g, f.rel) && policy.CanSearchPeople(viewer, leadsActive)
	canInvite := policy.CanInvite(viewer, &g, f.rel.Leader, leadsActive)
	f.Data = struct {
		Group             groups.Group
		Rows              []rosterRow
		CanAdd, CanInvite bool
		Find              Field
		Query             string
		Found             []groups.Found
		Invite            inviteForm
		Status            string
		Waiting           []string
		CanAddDirectly    bool
	}{f.group, rows, canAdd, canInvite,
		Field{ID: "find", Name: "find", Label: "Add someone by name", Hint: "Type at least two letters of their name.", Type: "search", Value: find, MaxLength: 100},
		map[bool]string{true: find, false: ""}[len([]rune(find)) >= 2], found, newInviteForm("/groups/"+f.group.UID+"/invite", "", "", false, nil),
		rosterStatuses[r.URL.Query().Get("done")], waiting, policy.CanAddDirectly(viewer, g)}
	a.render(w, r, http.StatusOK, "roster", "", f.page)
}

var rosterStatuses = map[string]string{
	"added":   "Added. They've been told and can leave if it isn't for them.",
	"asked":   "Invited. They'll join once they accept.",
	"waiting": "They already have an invitation waiting.",
	"leader":  "They're now a leader.",
	"member":  "They're a member again.",
	"removed": "Removed.",
	"invited": "Invitation sent. They'll join once they accept.",
	"refused": "That could not be done. The list shows how things stand now.",
	"same":    "That was already done, so nothing changed.",
	"many":    "That's a lot of invitations for one day, so this one wasn't sent. Try again tomorrow.",
	"busy":    "That's a lot of changes in a short time, so this one could not be saved. Try again in a little while.",
}

// runRoster runs a roster command and comes back to the roster.
func (a *App) runRoster(w http.ResponseWriter, r *http.Request, c write.Command, done string) {
	f, ok := a.loadGroup(w, r, nil)
	if !ok {
		return
	}
	if f.session == nil {
		http.Redirect(w, r, "/signin", http.StatusSeeOther)
		return
	}
	if !a.limits.groupActions.Allow(f.session.Person.UID) {
		http.Redirect(w, r, "/groups/"+f.group.UID+"/members?done=busy", http.StatusSeeOther)
		return
	}
	err := a.exec.Run(r.Context(), write.Session(accounts.Hash(a.sessionToken(r))), c)
	switch {
	case errors.Is(err, write.ErrNotAllowed):
		done = "refused"
	case errors.Is(err, groups.ErrUnchanged):
		done = "same"
	case errors.Is(err, accounts.ErrInvited):
		done = "waiting"
	case errors.Is(err, write.ErrSignedOut):
		http.Redirect(w, r, "/signin", http.StatusSeeOther)
		return
	case err != nil:
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/groups/"+f.group.UID+"/members?done="+done, http.StatusSeeOther)
}

// postAddMember is an admin adding someone directly (R32).
func (a *App) postAddMember(w http.ResponseWriter, r *http.Request) {
	a.runRoster(w, r, groups.Add{GroupUID: r.PathValue("uid"), PersonUID: r.PostFormValue("person")}, "added")
}

// postInvitePerson invites someone found by name; they join once they
// accept (R32).
func (a *App) postInvitePerson(w http.ResponseWriter, r *http.Request) {
	a.runRoster(w, r, accounts.InviteByName{GroupUID: r.PathValue("uid"), PersonUID: r.PostFormValue("person")}, "asked")
}

func (a *App) postMakeLeader(w http.ResponseWriter, r *http.Request) {
	a.runRoster(w, r, groups.SetRole{GroupUID: r.PathValue("uid"), PersonUID: r.PathValue("person"), Leader: true}, "leader")
}

func (a *App) postMakeMember(w http.ResponseWriter, r *http.Request) {
	a.runRoster(w, r, groups.SetRole{GroupUID: r.PathValue("uid"), PersonUID: r.PathValue("person")}, "member")
}

func (a *App) postRemoveMember(w http.ResponseWriter, r *http.Request) {
	a.runRoster(w, r, groups.Remove{GroupUID: r.PathValue("uid"), PersonUID: r.PathValue("person")}, "removed")
}

// The invite form: a name, an address, and that they are an adult (slice 1
// admits adults only).

type inviteForm struct {
	Action      string
	Name, Email Field
	Adult       Choice
	Errors      []FieldError
}

func newInviteForm(action, name, email string, adult bool, errs map[string]string) inviteForm {
	f := inviteForm{Action: action,
		Name:  Field{ID: "invite-name", Name: "name", Label: "Their name", Value: name, MaxLength: maxName, Required: true, Error: errs["name"]},
		Email: Field{ID: "invite-email", Name: "email", Label: "Their email", Type: "email", Value: email, MaxLength: maxEmail, Required: true, Error: errs["email"]},
		Adult: Choice{ID: "invite-adult", Name: "adult", Type: "checkbox", Value: "yes", Label: "They are 18 or older", Hint: "Accounts for under-18s aren't open yet.", Checked: adult, Error: errs["adult"]},
	}
	f.Errors = errorSummary(f.Name, f.Email)
	if f.Adult.Error != "" {
		f.Errors = append(f.Errors, FieldError{ID: f.Adult.ID, Message: f.Adult.Error})
	}
	return f
}

func readInvite(r *http.Request) (name, email string, adult bool, errs map[string]string) {
	errs = map[string]string{}
	name, okName := text(r, "name", maxName)
	raw, okEmail := text(r, "email", maxEmail)
	email = people.NormalizeEmail(raw)
	adult = r.PostFormValue("adult") == "yes"
	if !okName || name == "" {
		errs["name"] = "Enter their name."
	}
	if !okEmail || !mail.ValidAddress(email) {
		errs["email"] = "Enter an email address like name@example.org."
	}
	if !adult {
		errs["adult"] = "Confirm they are 18 or older."
	}
	if len(errs) == 0 {
		errs = nil
	}
	return name, email, adult, errs
}

// postInvite sends an invitation into a group. A leader has a daily limit,
// the one public-facing limit that answers with its own message (DEC-52).
func (a *App) postInvite(w http.ResponseWriter, r *http.Request) {
	f, ok := a.loadGroup(w, r, nil)
	if !ok {
		return
	}
	if f.session == nil {
		http.Redirect(w, r, "/signin", http.StatusSeeOther)
		return
	}
	name, email, adult, errs := readInvite(r)
	if errs != nil {
		a.renderInviteErrors(w, r, f.pageFacts, newInviteForm("/groups/"+f.group.UID+"/invite", name, email, adult, errs), "/groups/"+f.group.UID+"/members")
		return
	}
	if !a.limits.invites.Allow(f.session.Person.UID) || !a.limits.invitesSite.Allow("site") {
		http.Redirect(w, r, "/groups/"+f.group.UID+"/members?done=many", http.StatusSeeOther)
		return
	}
	a.runRoster(w, r, accounts.InviteByEmail{GroupUID: f.group.UID, Name: name, Email: email}, "invited")
}

func (a *App) renderInviteErrors(w http.ResponseWriter, r *http.Request, f pageFacts, form inviteForm, back string) {
	f.Data = struct {
		Invite inviteForm
		Back   string
	}{form, back}
	a.render(w, r, http.StatusUnprocessableEntity, "invite", "", f.page)
}

// postAdminInvite is an admin inviting someone with no group.
func (a *App) postAdminInvite(w http.ResponseWriter, r *http.Request) {
	f, ok := a.adminPage(w, r, nil)
	if !ok {
		return
	}
	name, email, adult, errs := readInvite(r)
	if errs != nil {
		a.renderInviteErrors(w, r, f, newInviteForm("/admin/invite", name, email, adult, errs), "/admin/requests")
		return
	}
	// Admins see everyone (DEC-5), so this page may say how the address
	// stands; a leader's invitation page never does.
	done := "invited"
	a.reader.Read(r.Context(), func(q *sql.Tx) error {
		if p, err := people.ByEmail(r.Context(), q, email); err == nil {
			switch p.Status {
			case people.StatusActive:
				done = "exists"
			case people.StatusDisabled:
				done = "off"
			}
		}
		return nil
	})
	if done != "invited" {
		http.Redirect(w, r, "/admin/requests?done="+done, http.StatusSeeOther)
		return
	}
	if !a.limits.invites.Allow(f.session.Person.UID) || !a.limits.invitesSite.Allow("site") {
		http.Redirect(w, r, "/admin/requests?done=many", http.StatusSeeOther)
		return
	}
	err := a.exec.Run(r.Context(), write.Session(accounts.Hash(a.sessionToken(r))), accounts.InviteByEmail{Name: name, Email: email})
	switch {
	case errors.Is(err, write.ErrSignedOut):
		http.Redirect(w, r, "/signin", http.StatusSeeOther)
		return
	case err != nil && !errors.Is(err, write.ErrNotAllowed):
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/requests?done="+done, http.StatusSeeOther)
}
