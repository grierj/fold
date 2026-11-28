package app

import (
	"errors"
	"net/http"
	"time"

	"fold/internal/accounts"
	"fold/internal/events"
	"fold/internal/policy"
	"fold/internal/write"
)

// One date's page, and what the people running its event do to that date
// alone: cancel it, bring it back, or open it wider than its series (P7).

var dateStatuses = map[string]string{
	"cancelled": "This date is cancelled.",
	"restored":  "This date is back on.",
	"opened":    "Saved.",
	"gone":      "That could not be done. The page shows how things stand now.",
	"many":      "That's a lot of changes in a short time, so this one could not be saved. Try again in a little while.",
}

// seriesMakes reports whether the event's repeat still makes the date's day,
// so a date cancelled by hand can come back.
func seriesMakes(e events.Event, d events.Row, now time.Time) bool {
	loc, err := events.LoadZone(e.Zone)
	if err != nil {
		return false
	}
	ds, err := events.Ahead(e.Pattern(), loc, now)
	if err != nil {
		return false
	}
	for _, x := range ds {
		if x.Day == d.Day {
			return x.StartsAt.After(now)
		}
	}
	return false
}

func (a *App) getDate(w http.ResponseWriter, r *http.Request) {
	f, ok := a.loadEvent(w, r, true, nil)
	if !ok {
		return
	}
	now := a.clock.Now()
	loc := viewerZone(f.pageFacts)
	start, end := f.date.StartsAt.In(loc), f.date.EndsAt.In(loc)
	when := clockSpan(start, end)
	if start.YearDay() != end.YearDay() || start.Year() != end.Year() {
		when = clockWords(start) + " to " + dayWords(end, now) + ", " + clockWords(end)
	}
	moved := ""
	if f.date.Moved {
		if planned, err := time.Parse(events.WallLayout, f.date.Day+f.event.StartsLocal[10:]); err == nil {
			moved = "The clocks change that night and " + clockWords(planned) + " doesn't happen, so this date starts at " +
				clockWords(f.date.StartsAt.In(mustZone(f.event.Zone))) + " " + events.ZoneCity(f.event.Zone) + " time."
		}
	}
	manages := f.canManage()
	state := f.date.State(now)
	groupName, groupHref := groupLink(f)
	opened := ""
	if f.date.Visibility != "" && policy.MoreOpen(f.date.Visibility, f.event.Visibility) && manages {
		opened = "This date is open wider than the rest of the event: " + visibilityLabels[f.date.Visibility] + ", where the event is " + visibilityLabels[f.event.Visibility] + "."
	}
	f.Data = struct {
		Event                events.Event
		Day, When, ZoneLine  string
		Moved, Opened        string
		Visibility           string
		Cancelled, BySeries  bool
		Past, SeesEvent      bool
		GroupName, GroupHref string
		Maps                 string
		Status               string
		CanCancel, CanOpen   bool
		CanRestore           bool
		UID                  string
		Cancel, Restore      Dialog
	}{
		Event: f.event, Day: longDay(start), When: when, ZoneLine: zoneLine(f.event.Zone, loc), Moved: moved, Opened: opened,
		Visibility: visibilityLabels[policy.Widest(f.event.Visibility, f.date.Visibility)],
		Cancelled:  f.date.Cancelled, BySeries: f.date.BySeries, Past: !f.date.EndsAt.After(now), SeesEvent: f.seesEvent(),
		GroupName: groupName, GroupHref: groupHref, Maps: mapsLink(f.event.Place),
		Status:     dateStatuses[r.URL.Query().Get("done")],
		CanCancel:  policy.CanCancelDate(manages, state),
		CanOpen:    policy.CanOpenDate(manages, state),
		CanRestore: policy.CanRestoreDate(manages, state, manages && f.date.Cancelled && !f.date.BySeries && seriesMakes(f.event, f.date, now)),
		UID:        f.date.UID,
		Cancel: Dialog{ID: "cancel-date", OpenLabel: "Cancel this date", OpenKind: "danger-outline",
			Title: "Cancel " + dayWords(start, now) + "?", Text: "Only this date is cancelled; the rest of the event stays as it is. You can bring it back before it starts, while the event still meets that day.",
			CancelLabel: "Keep it", ConfirmLabel: "Cancel this date", ConfirmKind: "danger",
			Action: "/dates/" + f.date.UID + "/cancel", CSRF: a.csrfToken(r)},
		Restore: Dialog{ID: "restore-date", OpenLabel: "Bring this date back", OpenKind: "secondary",
			Title: "Bring back " + dayWords(start, now) + "?", Text: "It goes back on at the event's usual time.",
			CancelLabel: "Not now", ConfirmLabel: "Bring it back", ConfirmKind: "primary",
			Action: "/dates/" + f.date.UID + "/restore", CSRF: a.csrfToken(r)},
	}
	a.render(w, r, http.StatusOK, "date", "", f.page)
}

// runDate runs a date command for someone who may see the date, and comes
// back to it. Anyone who may not run its event gets the not-found page, as
// for something missing.
func (a *App) runDate(w http.ResponseWriter, r *http.Request, c write.Command, done string) {
	f, ok := a.loadEvent(w, r, true, nil)
	if !ok {
		return
	}
	if !f.canManage() {
		a.notFound(w, r)
		return
	}
	back := "/dates/" + f.date.UID + "?done="
	if !a.limits.eventChanges.Allow(f.session.Person.UID) {
		http.Redirect(w, r, back+"many", http.StatusSeeOther)
		return
	}
	err := a.exec.Run(r.Context(), write.Session(accounts.Hash(a.sessionToken(r))), c)
	switch {
	case errors.Is(err, events.ErrDateGone):
		done = "gone"
	case errors.Is(err, write.ErrNotAllowed), errors.Is(err, write.ErrSignedOut):
		a.notFound(w, r)
		return
	case err != nil:
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, back+done, http.StatusSeeOther)
}

func (a *App) postCancelDate(w http.ResponseWriter, r *http.Request) {
	a.runDate(w, r, events.CancelDate{UID: r.PathValue("uid")}, "cancelled")
}

func (a *App) postRestoreDate(w http.ResponseWriter, r *http.Request) {
	a.runDate(w, r, events.RestoreDate{UID: r.PathValue("uid")}, "restored")
}

// The form that opens one date wider than its series (P7): the series'
// visibility, or anything wider, with a reason when that is wider than the
// group (DEC-28).

type openDateData struct {
	Event      events.Event
	Day, UID   string
	Visibility []Choice
	Reason     Field
	ShowReason bool
	GroupVis   string
	Errors     []FieldError
}

func (a *App) openDateView(f eventFacts, checked, reason string, errs map[string]string) openDateData {
	same := Choice{ID: "vis-series", Name: "visibility", Type: "radio", Value: "", Label: "The same as the event",
		Hint: "Who can see the rest of the event: " + visibilityLabels[f.event.Visibility] + ".", Checked: checked == ""}
	var wider []string
	for _, v := range visibilities(f.event.ChurchWide()) {
		if policy.CanSetDateVisibility(f.event.Visibility, v, f.event.ChurchWide()) {
			wider = append(wider, v)
		}
	}
	gv := openedFrom(f, policy.Widest(f.event.Visibility, f.date.Visibility))
	d := openDateData{Event: f.event, Day: longDay(f.date.StartsAt.In(viewerZone(f.pageFacts))), UID: f.date.UID,
		Visibility: append([]Choice{same}, visibilityChoices("vis-", wider, checked, gv)...),
		Reason:     reasonField(reason, errs["reason"]), GroupVis: gv}
	for _, v := range wider {
		d.ShowReason = d.ShowReason || (gv != "" && policy.MoreOpen(v, gv))
	}
	d.Errors = errorSummary(d.Reason)
	if e := errs["visibility"]; e != "" {
		d.Visibility[0].Error = e
		d.Errors = append(d.Errors, FieldError{ID: d.Visibility[0].ID, Message: e})
	}
	return d
}

func (a *App) getOpenDate(w http.ResponseWriter, r *http.Request) {
	f, ok := a.loadEvent(w, r, true, nil)
	if !ok {
		return
	}
	if !policy.CanOpenDate(f.canManage(), f.date.State(a.clock.Now())) {
		a.notFound(w, r)
		return
	}
	f.Data = a.openDateView(f, f.date.Visibility, "", nil)
	a.render(w, r, http.StatusOK, "date_open", "", f.page)
}

func (a *App) postOpenDate(w http.ResponseWriter, r *http.Request) {
	f, ok := a.loadEvent(w, r, true, nil)
	if !ok {
		return
	}
	if !f.canManage() {
		a.notFound(w, r)
		return
	}
	vis := r.PostFormValue("visibility")
	raw := r.PostFormValue("reason")
	errs := map[string]string{}
	if !multiline(raw, events.MaxReason) {
		errs["reason"] = "Keep it under 500 characters."
	}
	if vis != "" && !policy.ValidVisibility(vis) {
		errs["visibility"] = "Choose who can see this date."
		vis = f.date.Visibility
	}
	if len(errs) > 0 {
		f.Data = a.openDateView(f, vis, raw, errs)
		a.render(w, r, http.StatusUnprocessableEntity, "date_open", "", f.page)
		return
	}
	a.runDate(w, r, events.OpenDate{UID: f.date.UID, Visibility: vis, Reason: trimLines(raw)}, "opened")
}
