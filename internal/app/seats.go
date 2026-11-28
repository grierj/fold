package app

import (
	"errors"
	"net/http"

	"fold/internal/accounts"
	"fold/internal/events"
	"fold/internal/write"
)

// Making an event or a date more private removes nobody (R3). Before it
// saves, the confirmation lists the people signed up who would no longer
// see it, with Remove beside each; each Remove is its own confirmed write,
// swapped in place, so the unsaved change waits on the page.

type privateView struct {
	Event        events.Event
	Date         *events.Row // nil for the whole event
	Day          string
	From, To     string
	Action, Back string
	Hidden       []Hidden // the form's fields, carried to the save
	Rows         []unseenRow
}

type unseenRow struct {
	ID, Name, Dates string
	Remove          Dialog
}

func (a *App) showPrivate(w http.ResponseWriter, r *http.Request, f pageFacts, v privateView, list []events.Unseen) {
	for _, u := range list {
		id := "unseen-" + u.UID
		action, what := "/events/"+v.Event.UID+"/people/"+u.UID+"/remove", "their "+plural(u.Dates, "answer", "answers")+" to its dates still to come"
		if v.Date != nil {
			action, what = "/dates/"+v.Date.UID+"/people/"+u.UID+"/remove", "their answer to this date"
		}
		row := unseenRow{ID: id, Name: u.Name, Dates: plural(u.Dates, "date", "dates"),
			Remove: Dialog{ID: "remove-" + u.UID, OpenLabel: "Remove", OpenKind: "secondary", OpenName: "Remove " + u.Name,
				Title: "Take " + u.Name + " off " + v.Event.Title + "?", Text: "This takes off " + what + ". If that frees a seat, the next person waiting moves up and is told.",
				CancelLabel: "Keep them", ConfirmLabel: "Remove", ConfirmKind: "danger", Action: action, CSRF: a.csrfToken(r), Target: "#" + id}}
		if v.Date != nil {
			row.Dates = ""
		}
		v.Rows = append(v.Rows, row)
	}
	f.Data = v
	a.render(w, r, http.StatusOK, "event_private", "", f.page)
}

// removed is the line that takes a removed person's place in R3's list.
type removed struct{ ID, Text string }

// postRemoveFromDate takes someone off one date (DEC-84).
func (a *App) postRemoveFromDate(w http.ResponseWriter, r *http.Request) {
	f, ok := a.loadEvent(w, r, true, nil)
	if !ok {
		return
	}
	a.removeAnswer(w, r, f, &events.RemoveAnswer{DateUID: f.date.UID, PersonUID: r.PathValue("person")}, "/dates/"+f.date.UID)
}

// postRemoveFromEvent takes someone off every date of an event still to
// come, from R3's list.
func (a *App) postRemoveFromEvent(w http.ResponseWriter, r *http.Request) {
	f, ok := a.loadEvent(w, r, false, nil)
	if !ok {
		return
	}
	a.removeAnswer(w, r, f, &events.RemoveAnswer{EventUID: f.event.UID, PersonUID: r.PathValue("person")}, "/events/"+f.event.UID)
}

// removeAnswer runs a removal for those running the event; anyone else gets
// the not-found page. With htmx it swaps in place: R3's row, or the date
// page's answer block.
func (a *App) removeAnswer(w http.ResponseWriter, r *http.Request, f eventFacts, c *events.RemoveAnswer, back string) {
	if !f.canManage() {
		a.notFound(w, r)
		return
	}
	target := r.Header.Get("HX-Target")
	htmx := r.Header.Get("HX-Request") == "true"
	if !a.limits.eventChanges.Allow(f.session.Person.UID) {
		a.removeDone(w, r, f, htmx, target, back, http.StatusTooManyRequests, "That's a lot of changes in a short time, so this one could not be saved. Try again in a little while.", "many")
		return
	}
	err := a.exec.Run(r.Context(), write.Session(accounts.Hash(a.sessionToken(r))), c)
	switch {
	case errors.Is(err, events.ErrUnchanged):
		a.removeDone(w, r, f, htmx, target, back, http.StatusOK, "They were already off it.", "")
		return
	case errors.Is(err, events.ErrDateGone):
		a.removeDone(w, r, f, htmx, target, back, http.StatusConflict, "That date has begun, so its answers stay as they are.", "gone")
		return
	case errors.Is(err, write.ErrNotAllowed), errors.Is(err, write.ErrSignedOut):
		a.notFound(w, r)
		return
	case err != nil:
		a.serverError(w, r, err)
		return
	}
	text := c.Name + " is off this date."
	if c.DateUID == "" {
		text = c.Name + " is off " + plural(c.Removed, "date", "dates") + "."
	}
	a.removeDone(w, r, f, htmx, target, back, http.StatusOK, text, "removed")
}

func (a *App) removeDone(w http.ResponseWriter, r *http.Request, f eventFacts, htmx bool, target, back string, code int, text, done string) {
	if htmx {
		code = http.StatusOK // htmx swaps nothing on an error, which would leave the dialog open and silent
	}
	switch row := "unseen-" + r.PathValue("person"); {
	case htmx && target == row:
		f.Data = removed{ID: row, Text: text}
		a.render(w, r, code, "event_private", "removed", f.page)
	case htmx && target == "answer" && f.date.UID != "":
		f, af, ok := a.loadDate(w, r)
		if !ok {
			return
		}
		af.open = true
		a.showDate(w, r, f, af, code, text, "answer", nil)
	default:
		if done != "" {
			back += "?done=" + done
		}
		http.Redirect(w, r, back, http.StatusSeeOther)
	}
}
