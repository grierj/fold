package app

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"fold/internal/accounts"
	"fold/internal/events"
	"fold/internal/groups"
	"fold/internal/policy"
	"fold/internal/write"
)

// Group pages. Every /groups/{uid} route loads through groupFacts, which
// asks policy and answers exactly as if the group did not exist when the
// viewer may not see it (AGENTS.md, "Every read and every write asks
// policy").

var visibilityLabels = map[string]string{
	policy.Public:     "Public",
	policy.Church:     "Church only",
	policy.InviteOnly: "Invite only",
}

var visibilityHints = map[string]string{
	policy.Public:     "Anyone can find it, signed in or not.",
	policy.Church:     "Anyone signed in at the church can find it and join.",
	policy.InviteOnly: "Only its members and admins can see it. People join when a leader adds them.",
}

type groupFacts struct {
	pageFacts
	group groups.Group
	rel   policy.Rel
}

func (f groupFacts) viewer() *policy.Person { return viewerFacts(f.session) }

func (f groupFacts) listViewer() events.Viewer { return listViewer(f.pageFacts) }

// groupDates is how many dates a group page shows coming up.
const groupDates = 5

// loadGroup reads the group named in the path, the viewer, and their
// relation, in one read; ok is false (and the not-found page written) when
// the viewer may not see it.
func (a *App) loadGroup(w http.ResponseWriter, r *http.Request, extra func(q *sql.Tx, f *groupFacts) error) (groupFacts, bool) {
	var f groupFacts
	visible := false
	err := a.reader.Read(r.Context(), func(q *sql.Tx) error {
		var err error
		if f.pageFacts, err = a.load(r.Context(), r, q); err != nil {
			return err
		}
		f.group, err = groups.ByUID(r.Context(), q, r.PathValue("uid"))
		if errors.Is(err, groups.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if f.session != nil {
			if f.rel, err = groups.RelOf(r.Context(), q, f.group.ID, f.session.Person.ID); err != nil {
				return err
			}
		}
		visible = policy.CanSeeGroup(f.viewer(), f.group.Policy(), f.rel)
		if !visible || extra == nil {
			return nil
		}
		return extra(q, &f)
	})
	if err != nil {
		a.serverError(w, r, err)
		return f, false
	}
	if !visible {
		a.notFound(w, r)
		return f, false
	}
	return f, true
}

// runGroup runs a group command as the signed-in viewer and answers: back
// to the group with a status line, the not-found page when the group is
// hidden, or a gentle line when the action is no longer possible.
func (a *App) runGroup(w http.ResponseWriter, r *http.Request, c write.Command, done string) {
	f, ok := a.loadGroup(w, r, nil)
	if !ok {
		return
	}
	if f.session == nil {
		http.Redirect(w, r, "/signin", http.StatusSeeOther)
		return
	}
	if !a.limits.groupActions.Allow(f.session.Person.UID) {
		http.Redirect(w, r, "/groups/"+f.group.UID+"?done=many", http.StatusSeeOther)
		return
	}
	err := a.exec.Run(r.Context(), write.Session(accounts.Hash(a.sessionToken(r))), c)
	switch {
	case errors.Is(err, write.ErrNotAllowed):
		done = "refused"
	case errors.Is(err, groups.ErrAlreadyMember):
		done = "already"
	case errors.Is(err, write.ErrSignedOut):
		http.Redirect(w, r, "/signin", http.StatusSeeOther)
		return
	case err != nil:
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/groups/"+f.group.UID+"?done="+done, http.StatusSeeOther)
}

var groupStatuses = map[string]string{
	"joined":        "You joined the group.",
	"already":       "You're already in this group.",
	"many":          "That's a lot of changes in a short time, so this one wasn't made. Try again in a little while.",
	"left":          "You left the group.",
	"saved":         "Saved.",
	"archived":      "The group is archived. It no longer shows in the list.",
	"unarchived":    "The group is active again.",
	"refused":       "That could not be done. The page shows how things stand now.",
	"created":       "Your group is started.",
	"event-deleted": "The event is deleted.",
	"waiting":       "Your group is waiting for an administrator to approve it. You'll get a notice when they do.",
}

func (a *App) getGroups(w http.ResponseWriter, r *http.Request) {
	search := r.URL.Query().Get("q")
	if len(search) > 100 {
		search = search[:100]
	}
	var list []groups.Listed
	var f pageFacts
	err := a.reader.Read(r.Context(), func(q *sql.Tx) error {
		var err error
		if f, err = a.load(r.Context(), r, q); err != nil {
			return err
		}
		v := groups.Viewer{}
		if f.session != nil {
			v = groups.Viewer{ID: f.session.Person.ID, Admin: policy.IsAdmin(f.session.Person.Policy())}
		}
		list, err = groups.Directory(r.Context(), q, v, search, 200)
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
	f.Data = struct {
		Groups []groups.Listed
		Search Field
		Query  string
		Labels map[string]string
	}{list, Field{ID: "q", Name: "q", Label: "Search groups", Type: "search", Value: search, MaxLength: 100}, search, visibilityLabels}
	a.render(w, r, http.StatusOK, "groups", "", f.page)
}

func (a *App) groupForm(name, about, vis string, errs map[string]string) groupFormData {
	if vis == "" {
		vis = policy.Church
	}
	d := groupFormData{
		Name:  Field{ID: "name", Name: "name", Label: "Name", Value: name, MaxLength: groups.MaxName, Required: true, Error: errs["name"]},
		About: Field{ID: "about", Name: "about", Label: "About the group", Hint: "What you do, when and where you meet, and who it's for.", Value: about, MaxLength: groups.MaxAbout, Error: errs["about"]},
	}
	for _, v := range []string{policy.Public, policy.Church, policy.InviteOnly} {
		d.Visibility = append(d.Visibility, Choice{ID: "vis-" + v, Name: "visibility", Type: "radio", Value: v,
			Label: visibilityLabels[v], Hint: visibilityHints[v], Checked: vis == v})
	}
	if errs == nil {
		d.Name.Autofocus = true
	} else {
		focusFirstError(&d.Name, &d.About)
	}
	d.Errors = errorSummary(d.Name, d.About)
	if e := errs["visibility"]; e != "" {
		d.Visibility[0].Error = e
		d.Errors = append(d.Errors, FieldError{ID: d.Visibility[0].ID, Message: e})
	}
	return d
}

type groupFormData struct {
	Name, About Field
	Visibility  []Choice
	Errors      []FieldError
	Version     int64
	Action      string
	Submit      string
	Conflict    bool
	TooMany     bool
}

// readGroupForm reads and checks the group form.
func readGroupForm(r *http.Request) (name, about, vis string, errs map[string]string) {
	errs = map[string]string{}
	name, okName := text(r, "name", groups.MaxName)
	about = r.PostFormValue("about")
	vis = r.PostFormValue("visibility")
	switch {
	case !okName:
		errs["name"] = "That name is too long or has characters we can't use."
	case name == "":
		errs["name"] = "Give the group a name."
	}
	if !multiline(about, groups.MaxAbout) {
		errs["about"] = "Keep it under 2,000 characters."
	}
	if !policy.ValidVisibility(vis) {
		errs["visibility"] = "Choose who can see it."
		vis = policy.Church
	}
	if len(errs) == 0 {
		errs = nil
	}
	return name, trimLines(about), vis, errs
}

func (a *App) getNewGroup(w http.ResponseWriter, r *http.Request) {
	f, ok := a.signedIn(w, r, nil)
	if !ok {
		return
	}
	d := a.groupForm("", "", "", nil)
	d.Action, d.Submit = "/groups/new", "Start the group"
	f.Data = d
	a.render(w, r, http.StatusOK, "group_form", "", f.page)
}

func (a *App) postNewGroup(w http.ResponseWriter, r *http.Request) {
	f, ok := a.signedIn(w, r, nil)
	if !ok {
		return
	}
	name, about, vis, errs := readGroupForm(r)
	if errs != nil {
		d := a.groupForm(name, about, vis, errs)
		d.Action, d.Submit = "/groups/new", "Start the group"
		f.Data = d
		a.render(w, r, http.StatusUnprocessableEntity, "group_form", "", f.page)
		return
	}
	if !a.limits.newGroups.Allow(f.session.Person.UID) {
		d := a.groupForm(name, about, vis, nil)
		d.Action, d.Submit, d.TooMany = "/groups/new", "Start the group", true
		f.Data = d
		a.render(w, r, http.StatusTooManyRequests, "group_form", "", f.page)
		return
	}
	c := &groups.Create{Name: name, About: about, Visibility: vis}
	err := a.exec.Run(r.Context(), write.Session(accounts.Hash(a.sessionToken(r))), c)
	if errors.Is(err, groups.ErrTooManyWaiting) {
		d := a.groupForm(name, about, vis, nil)
		d.Action, d.Submit, d.TooMany = "/groups/new", "Start the group", true
		f.Data = d
		a.render(w, r, http.StatusTooManyRequests, "group_form", "", f.page)
		return
	}
	if errors.Is(err, write.ErrSignedOut) {
		http.Redirect(w, r, "/signin", http.StatusSeeOther)
		return
	}
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	done := "created"
	if !policy.IsAdmin(f.session.Person.Policy()) {
		done = "waiting"
	}
	http.Redirect(w, r, "/groups/"+c.UID+"?done="+done, http.StatusSeeOther)
}

func (a *App) getGroup(w http.ResponseWriter, r *http.Request) {
	var leaders []string
	var leaderCount, members int
	var coming []events.Listed
	now := a.clock.Now()
	f, ok := a.loadGroup(w, r, func(q *sql.Tx, f *groupFacts) error {
		var err error
		if leaders, err = groups.Leaders(r.Context(), q, f.group.ID); err != nil {
			return err
		}
		if coming, err = events.Upcoming(r.Context(), q, f.listViewer(), events.Filter{GroupID: f.group.ID}, now, groupDates); err != nil {
			return err
		}
		if err = q.QueryRowContext(r.Context(), `SELECT count(*) FROM active_memberships WHERE group_id = ?`, f.group.ID).Scan(&members); err != nil {
			return err
		}
		leaderCount, err = groups.LeaderCount(r.Context(), q, f.group.ID)
		return err
	})
	if !ok {
		return
	}
	p, g := f.viewer(), f.group.Policy()
	d := struct {
		Group                        groups.Group
		Visibility                   string
		Leaders                      []string
		Status                       string
		CanJoin, CanLeave, CanManage bool
		CanArchive, CanUnarchive     bool
		CanDelete, Waiting, Archived bool
		SignedIn, LastLeader         bool
		CanSeeMembers                bool
		Members                      int
		Delete                       Dialog
		Reason                       string
		Coming                       []dateItem
		CanSchedule                  bool
	}{
		Coming:      dateItems(coming, viewerZone(f.pageFacts), now, true),
		CanSchedule: policy.CanScheduleEvent(p, g, f.rel),
		Group:       f.group, Visibility: visibilityLabels[f.group.Visibility], Leaders: leaders,
		Status:        groupStatuses[r.URL.Query().Get("done")],
		CanJoin:       policy.CanJoinGroup(p, g, f.rel) && !f.rel.Member,
		CanLeave:      policy.CanLeaveGroup(f.rel, leaderCount),
		LastLeader:    f.rel.Leader && leaderCount == 1,
		CanManage:     policy.CanManageGroup(p, g, f.rel),
		CanArchive:    policy.DecideGroupStatus(p, f.rel, f.group.Status, policy.GroupArchived) == policy.GroupStatusArchive,
		CanUnarchive:  policy.DecideGroupStatus(p, f.rel, f.group.Status, policy.GroupActive) == policy.GroupStatusUnarchive,
		CanDelete:     policy.CanDeleteGroup(p, g),
		Waiting:       f.group.Status == policy.GroupPending,
		Archived:      f.group.Status == policy.GroupArchived,
		SignedIn:      f.session != nil,
		CanSeeMembers: policy.CanSeeRoster(p, g, f.rel),
		Members:       members,
		Reason:        policy.GroupReasons[f.group.DecisionReason],
		Delete: Dialog{ID: "delete-group", OpenLabel: "Delete the group", OpenKind: "danger-outline",
			Title: "Delete " + f.group.Name + "?", Text: "Its members will be told. This cannot be undone.",
			CancelLabel: "Keep it", ConfirmLabel: "Delete", ConfirmKind: "danger",
			Action: "/groups/" + f.group.UID + "/delete", CSRF: a.csrfToken(r)},
	}
	f.Data = d
	a.render(w, r, http.StatusOK, "group", "", f.page)
}

func (a *App) getEditGroup(w http.ResponseWriter, r *http.Request) {
	f, ok := a.loadGroup(w, r, nil)
	if !ok {
		return
	}
	if !policy.CanManageGroup(f.viewer(), f.group.Policy(), f.rel) {
		a.notFound(w, r)
		return
	}
	d := a.groupForm(f.group.Name, f.group.About, f.group.Visibility, nil)
	d.Action, d.Submit, d.Version = "/groups/"+f.group.UID+"/edit", "Save", f.group.Version
	f.Data = d
	a.render(w, r, http.StatusOK, "group_form", "", f.page)
}

// postEditGroup saves the form. Making the group more private first asks
// for a confirmation that says nobody is removed (R3).
func (a *App) postEditGroup(w http.ResponseWriter, r *http.Request) {
	f, ok := a.loadGroup(w, r, nil)
	if !ok {
		return
	}
	if !policy.CanManageGroup(f.viewer(), f.group.Policy(), f.rel) {
		a.notFound(w, r)
		return
	}
	name, about, vis, errs := readGroupForm(r)
	version, _ := strconv.ParseInt(r.PostFormValue("version"), 10, 64)
	show := func(status int, d groupFormData) {
		d.Action, d.Submit = "/groups/"+f.group.UID+"/edit", "Save"
		f.Data = d
		a.render(w, r, status, "group_form", "", f.page)
	}
	if errs != nil {
		d := a.groupForm(name, about, vis, errs)
		d.Version = version
		show(http.StatusUnprocessableEntity, d)
		return
	}
	if policy.MorePrivate(f.group.Visibility, vis) && r.PostFormValue("confirmed") != "yes" {
		f.Data = struct {
			Group                   groups.Group
			Name, About, Visibility string
			Version                 int64
			From, To, ToHint        string
		}{f.group, name, about, vis, version, visibilityLabels[f.group.Visibility], visibilityLabels[vis], visibilityHints[vis]}
		a.render(w, r, http.StatusOK, "group_private", "", f.page)
		return
	}
	err := a.exec.Run(r.Context(), write.Session(accounts.Hash(a.sessionToken(r))),
		groups.Edit{UID: f.group.UID, Name: name, About: about, Visibility: vis, Version: version})
	switch {
	case errors.Is(err, write.ErrConflict):
		// Someone else saved first: show what the group says now (P16).
		d := a.groupForm(f.group.Name, f.group.About, f.group.Visibility, nil)
		d.Version, d.Conflict = f.group.Version, true
		show(http.StatusConflict, d)
		return
	case errors.Is(err, write.ErrNotAllowed):
		a.notFound(w, r)
		return
	case errors.Is(err, write.ErrSignedOut):
		http.Redirect(w, r, "/signin", http.StatusSeeOther)
		return
	case err != nil:
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/groups/"+f.group.UID+"?done=saved", http.StatusSeeOther)
}

func (a *App) postJoin(w http.ResponseWriter, r *http.Request) {
	a.runGroup(w, r, groups.Join{UID: r.PathValue("uid")}, "joined")
}

func (a *App) postLeave(w http.ResponseWriter, r *http.Request) {
	a.runGroup(w, r, groups.Leave{UID: r.PathValue("uid")}, "left")
}

func (a *App) postArchive(w http.ResponseWriter, r *http.Request) {
	a.runGroup(w, r, groups.SetStatus{UID: r.PathValue("uid"), To: policy.GroupArchived}, "archived")
}

func (a *App) postUnarchive(w http.ResponseWriter, r *http.Request) {
	a.runGroup(w, r, groups.SetStatus{UID: r.PathValue("uid"), To: policy.GroupActive}, "unarchived")
}

func (a *App) postDeleteGroup(w http.ResponseWriter, r *http.Request) {
	f, ok := a.loadGroup(w, r, nil)
	if !ok {
		return
	}
	err := a.exec.Run(r.Context(), write.Session(accounts.Hash(a.sessionToken(r))), groups.Delete{UID: f.group.UID})
	switch {
	case errors.Is(err, write.ErrNotAllowed), errors.Is(err, write.ErrSignedOut):
		a.notFound(w, r)
		return
	case err != nil:
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/groups?deleted=1", http.StatusSeeOther)
}

// The admin's list of groups waiting for approval.

func (a *App) getAdminGroups(w http.ResponseWriter, r *http.Request) {
	var list []groups.Listed
	f, ok := a.adminPage(w, r, func(q *sql.Tx, f *pageFacts) error {
		var err error
		list, err = groups.Waiting(r.Context(), q)
		return err
	})
	if !ok {
		return
	}
	type row struct {
		groups.Listed
		Reasons     []Choice
		Note        Field
		ApproveNote Field
	}
	var rows []row
	for _, g := range list {
		rr := row{Listed: g, Note: Field{ID: "note-" + g.UID, Name: "note", Label: "A note to the leaders",
			Hint: "Needed for \"Another reason\", optional otherwise. Shown to the group's leaders and kept in the church's records.", MaxLength: groups.MaxNote},
			ApproveNote: Field{ID: "approve-note-" + g.UID, Name: "note", Label: "A welcome note to the leaders (optional)",
				Hint: "Shown to the group's leaders and kept in the church's records.", MaxLength: groups.MaxNote}}
		for _, k := range []string{"similar", "detail", "office", "other"} {
			label := policy.GroupReasons[k]
			if k == "other" {
				label = "Another reason (write it in the note)"
			}
			rr.Reasons = append(rr.Reasons, Choice{ID: "reason-" + k + "-" + g.UID, Name: "reason", Type: "radio", Value: k, Label: label})
		}
		rows = append(rows, rr)
	}
	f.Data = struct {
		Groups []row
		Status string
	}{rows, map[string]string{
		"approved": "Approved. Its leaders have been told.",
		"rejected": "Turned down. Its leaders have been told why.",
		"gone":     "That group was already decided, so nothing changed.",
		"reason":   "Choose a reason, or write a note for \"Another reason\".",
		"note":     "Keep the note under 500 characters.",
	}[r.URL.Query().Get("done")]}
	a.render(w, r, http.StatusOK, "admin_groups", "", f.page)
}

func (a *App) postDecideGroup(approve bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := a.adminPage(w, r, nil); !ok {
			return
		}
		reason := r.PostFormValue("reason")
		raw := r.PostFormValue("note")
		note := trimLines(raw)
		if !multiline(raw, groups.MaxNote) {
			http.Redirect(w, r, "/admin/groups?done=note", http.StatusSeeOther)
			return
		}
		if _, known := policy.GroupReasons[reason]; !approve && (!known || (reason == "other" && note == "")) {
			http.Redirect(w, r, "/admin/groups?done=reason", http.StatusSeeOther)
			return
		}
		if approve {
			reason = ""
		}
		err := a.exec.Run(r.Context(), write.Session(accounts.Hash(a.sessionToken(r))),
			groups.Decide{UID: r.PathValue("uid"), Approve: approve, Reason: reason, Note: note})
		done := map[bool]string{true: "approved", false: "rejected"}[approve]
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
		http.Redirect(w, r, "/admin/groups?done="+done, http.StatusSeeOther)
	}
}
