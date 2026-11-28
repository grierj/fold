package app

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"fold/internal/accounts"
	"fold/internal/events"
	"fold/internal/policy"
	"fold/internal/write"
)

// Church-wide events (DEC-7): the admins' list of them, and the page where
// those who run one name and remove its hosts.

func (a *App) getAdminEvents(w http.ResponseWriter, r *http.Request) {
	var list []events.Event
	f, ok := a.adminPage(w, r, func(q *sql.Tx, f *pageFacts) error {
		var err error
		list, err = events.ChurchWide(r.Context(), q)
		return err
	})
	if !ok {
		return
	}
	type row struct{ UID, Title, Pattern string }
	now := a.clock.Now()
	var rows []row
	for _, e := range list {
		rows = append(rows, row{e.UID, e.Title, pattern(e, now)})
	}
	f.Data = struct{ Events []row }{rows}
	a.render(w, r, http.StatusOK, "admin_events", "", f.page)
}

var hostStatuses = map[string]string{
	"added":   "They're now a host, and they've been told.",
	"removed": "Removed.",
	"same":    "That was already done, so nothing changed.",
	"refused": "That could not be done. The list shows how things stand now.",
	"busy":    "That's a lot of changes in a short time, so this one could not be saved. Try again in a little while.",
}

func (a *App) getHosts(w http.ResponseWriter, r *http.Request) {
	find := strings.TrimSpace(r.URL.Query().Get("find"))
	if len(find) > 100 {
		find = find[:100]
	}
	var hosts, found []events.Person
	searched, busy := false, false
	f, ok := a.loadEvent(w, r, false, func(q *sql.Tx, f *eventFacts) error {
		if !policy.CanFindHosts(f.viewer(), f.ev()) {
			return nil
		}
		var err error
		if hosts, err = events.Hosts(r.Context(), q, f.event.ID); err != nil {
			return err
		}
		if len([]rune(find)) >= 2 {
			// Searching names is limited per person, before the query runs.
			if busy = !a.limits.hostSearch.Allow(f.session.Person.UID); !busy {
				searched = true
				found, err = events.FindHosts(r.Context(), q, f.event.ID, find, 20)
			}
		}
		return err
	})
	if !ok {
		return
	}
	if !policy.CanFindHosts(f.viewer(), f.ev()) {
		a.notFound(w, r)
		return
	}
	type hostRow struct {
		events.Person
		Remove Dialog
	}
	var rows []hostRow
	for _, h := range hosts {
		rows = append(rows, hostRow{h, Dialog{ID: "remove-" + h.UID, OpenLabel: "Remove", OpenKind: "ghost",
			Title: "Remove " + h.Name + " as a host?", Text: "They will no longer be able to change this event. Admins always can.",
			CancelLabel: "Keep them", ConfirmLabel: "Remove", ConfirmKind: "danger",
			Action: "/events/" + f.event.UID + "/hosts/" + h.UID + "/remove", CSRF: a.csrfToken(r)}})
	}
	status := hostStatuses[r.URL.Query().Get("done")]
	if busy {
		status = "That's a lot of searches in a short time. Try again in a little while."
	}
	f.Data = struct {
		Event    events.Event
		Hosts    []hostRow
		Find     Field
		Searched bool
		Found    []events.Person
		Status   string
	}{f.event, rows,
		Field{ID: "find", Name: "find", Label: "Find someone by name", Hint: "Type at least two letters of their name.", Type: "search", Value: find, MaxLength: 100},
		searched, found, status}
	a.render(w, r, http.StatusOK, "hosts", "", f.page)
}

func (a *App) runHosts(w http.ResponseWriter, r *http.Request, c func(eventUID string) write.Command, done string) {
	f, ok := a.loadEvent(w, r, false, nil)
	if !ok {
		return
	}
	if !policy.CanFindHosts(f.viewer(), f.ev()) {
		a.notFound(w, r)
		return
	}
	back := "/events/" + f.event.UID + "/hosts?done="
	if !a.limits.eventChanges.Allow(f.session.Person.UID) {
		http.Redirect(w, r, back+"busy", http.StatusSeeOther)
		return
	}
	err := a.exec.Run(r.Context(), write.Session(accounts.Hash(a.sessionToken(r))), c(f.event.UID))
	switch {
	case errors.Is(err, write.ErrNotAllowed):
		done = "refused"
	case errors.Is(err, write.ErrSignedOut):
		a.notFound(w, r)
		return
	case err != nil:
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, back+done, http.StatusSeeOther)
}

func (a *App) postAddHost(w http.ResponseWriter, r *http.Request) {
	person := r.PostFormValue("person")
	a.runHosts(w, r, func(uid string) write.Command { return events.AddHost{EventUID: uid, PersonUID: person} }, "added")
}

func (a *App) postRemoveHost(w http.ResponseWriter, r *http.Request) {
	person := r.PathValue("person")
	a.runHosts(w, r, func(uid string) write.Command { return events.RemoveHost{EventUID: uid, PersonUID: person} }, "removed")
}
