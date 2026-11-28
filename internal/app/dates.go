package app

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"fold/internal/accounts"
	"fold/internal/answers"
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

// answerFacts are what the date page shows about answers, read in the
// page's one read. Can't go is read only for those who may see it (DEC-64).
type answerFacts struct {
	mine               string
	counts             answers.Counts
	going, maybe, cant []string
}

// loadDate loads a date page's facts and its answers.
func (a *App) loadDate(w http.ResponseWriter, r *http.Request) (eventFacts, answerFacts, bool) {
	var af answerFacts
	f, ok := a.loadEvent(w, r, true, func(q *sql.Tx, f *eventFacts) error {
		ctx, p, ev, g := r.Context(), f.viewer(), f.ev(), f.group.Policy()
		var err error
		if f.session != nil {
			if af.mine, err = answers.Mine(ctx, q, f.date.ID, f.session.Person.ID); err != nil {
				return err
			}
		}
		cants := policy.CanSeeCantGo(p, ev, g, f.rel)
		if af.counts, err = answers.CountsFor(ctx, q, f.date.ID, cants); err != nil {
			return err
		}
		if !policy.CanSeeAttendeeNames(p, ev, f.date.Visibility, g, f.rel) {
			return nil
		}
		if af.going, err = answers.Names(ctx, q, f.date.ID, policy.AnswerGoing); err != nil {
			return err
		}
		if af.maybe, err = answers.Names(ctx, q, f.date.ID, policy.AnswerMaybe); err != nil {
			return err
		}
		if cants {
			af.cant, err = answers.Names(ctx, q, f.date.ID, policy.AnswerCant)
		}
		return err
	})
	return f, af, ok
}

// answerBlock is the part of the date page that answering swaps: the
// control, the status line that stays (DEC-92), who is coming, and the video
// link. Can't go is in it only for those running the event (DEC-64).
type answerBlock struct {
	UID                string
	CanAnswer, SignIn  bool
	Heading            string
	Segment            Segment
	Series             bool
	Scopes             []Choice
	Status             string
	Counts             string
	Names, ShowCant    bool
	Going, Maybe, Cant []string
	Video              string
	Confirm            *answerConfirm
	CSRF               string
}

// answerConfirm asks before a Can't go for several dates (Fold's 5.4).
type answerConfirm struct {
	Count string
	Scope string
}

var answerLabels = map[string]string{policy.AnswerGoing: "Going", policy.AnswerMaybe: "Maybe", policy.AnswerCant: "Can't go"}

// answerStatus is the line that stays after an answer (DEC-92).
func answerStatus(answer string, dates int) string {
	switch answer {
	case policy.AnswerGoing:
		if dates > 1 {
			return "You're going to " + strconv.Itoa(dates) + " dates. See you there!"
		}
		return "You're going. See you there!"
	case policy.AnswerMaybe:
		if dates > 1 {
			return "You said maybe to " + strconv.Itoa(dates) + " dates. Change it anytime."
		}
		return "You said maybe. Change it anytime."
	case policy.AnswerCant:
		return "Got it. Only the people running the event see this."
	}
	return ""
}

func (a *App) answerView(f eventFacts, af answerFacts, r *http.Request, status string, confirm *answerConfirm) answerBlock {
	now := a.clock.Now()
	p, ev, g := f.viewer(), f.ev(), f.group.Policy()
	ended := !f.date.EndsAt.After(now)
	b := answerBlock{UID: f.date.UID, Status: status, CSRF: a.csrfToken(r), Confirm: confirm,
		CanAnswer: policy.CanAnswer(p, ev, f.date.Visibility, g, f.rel, f.date.Cancelled, ended),
		Names:     policy.CanSeeAttendeeNames(p, ev, f.date.Visibility, g, f.rel),
		ShowCant:  policy.CanSeeCantGo(p, ev, g, f.rel), Going: af.going, Maybe: af.maybe, Cant: af.cant}
	b.SignIn = f.session == nil && !f.date.Cancelled && !ended
	b.Heading = "Who's coming"
	if b.CanAnswer {
		b.Heading = "Are you going?"
		b.Segment = Segment{Label: "Your answer", Name: "answer"}
		for _, v := range []string{policy.AnswerGoing, policy.AnswerMaybe, policy.AnswerCant} {
			b.Segment.Options = append(b.Segment.Options, SegmentOption{Label: answerLabels[v], Value: v, Pressed: af.mine == v})
		}
		if policy.CanAnswerSeries(p, ev, g, f.rel, f.event.Repeat != events.Once) {
			b.Series = true
			for _, sc := range []struct{ v, label string }{{policy.ScopeDate, "Just this date"}, {policy.ScopeFollowing, "This date and the ones after it"}, {policy.ScopeAll, "All dates still to come"}} {
				b.Scopes = append(b.Scopes, Choice{ID: "scope-" + sc.v, Name: "scope", Type: "radio", Value: sc.v, Label: sc.label, Checked: sc.v == policy.ScopeDate})
			}
		}
	}
	var parts []string
	if n := af.counts.Going; n > 0 {
		parts = append(parts, strconv.Itoa(n)+" going")
	}
	if n := af.counts.Maybe; n > 0 {
		parts = append(parts, strconv.Itoa(n)+" maybe")
	}
	if n := af.counts.Cant; n > 0 && b.ShowCant {
		parts = append(parts, strconv.Itoa(n)+" can't go")
	}
	b.Counts = strings.Join(parts, " · ")
	if b.Counts == "" {
		b.Counts = "No one has answered yet."
	}
	if f.event.VideoURL != "" && policy.CanSeeVideo(f.canManage(), af.mine) {
		b.Video = f.event.VideoURL
	}
	return b
}

func (a *App) getDate(w http.ResponseWriter, r *http.Request) {
	f, af, ok := a.loadDate(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	status := dateStatuses[q.Get("done")]
	// The line after an answer says only what the viewer's stored answer
	// bears out; a crafted address shows nothing.
	if done := q.Get("done"); policy.IsAnswer(done) {
		status = ""
		if af.mine == done {
			n, _ := strconv.Atoi(q.Get("n"))
			status = answerStatus(done, min(max(n, 1), events.MaxDates))
		}
	}
	a.showDate(w, r, f, af, http.StatusOK, status, "", nil)
}

// showDate renders the date page, or with block "answer" just the answer
// part, for htmx.
func (a *App) showDate(w http.ResponseWriter, r *http.Request, f eventFacts, af answerFacts, code int, status, block string, confirm *answerConfirm) {
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
	pageStatus, answerStatus := status, ""
	if block == "answer" || strings.HasPrefix(status, "You") || strings.HasPrefix(status, "Got it") {
		pageStatus, answerStatus = "", status
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
		Answer               answerBlock
	}{
		Event: f.event, Day: longDay(start), When: when, ZoneLine: zoneLine(f.event.Zone, loc), Moved: moved, Opened: opened,
		Visibility: visibilityLabels[policy.Widest(f.event.Visibility, f.date.Visibility)],
		Cancelled:  f.date.Cancelled, BySeries: f.date.BySeries, Past: !f.date.EndsAt.After(now), SeesEvent: f.seesEvent(),
		GroupName: groupName, GroupHref: groupHref, Maps: mapsLink(f.event.Place),
		Status:     pageStatus,
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
		Answer: a.answerView(f, af, r, answerStatus, confirm),
	}
	a.render(w, r, code, "date", block, f.page)
}

// postAnswer saves a person's answer. A signed-out visitor who may see the
// date is sent to sign in; anyone who may not see it gets the not-found
// page, as for something missing.
func (a *App) postAnswer(w http.ResponseWriter, r *http.Request) {
	f, ok := a.loadEvent(w, r, true, nil)
	if !ok {
		return
	}
	if f.session == nil {
		http.Redirect(w, r, "/signin", http.StatusSeeOther)
		return
	}
	htmx := r.Header.Get("HX-Request") == "true"
	block := ""
	if htmx {
		block = "answer"
	}
	again := func(code int, status string, confirm *answerConfirm) {
		f, af, ok := a.loadDate(w, r)
		if !ok {
			return
		}
		a.showDate(w, r, f, af, code, status, block, confirm)
	}
	if !a.limits.answers.Allow(f.session.Person.UID) {
		again(http.StatusTooManyRequests, "That's a lot of answers in a short time, so this one could not be saved. Try again in a little while.", nil)
		return
	}
	scope := r.PostFormValue("scope")
	if scope == "" {
		scope = policy.ScopeDate
	}
	c := &events.Answer{DateUID: f.date.UID, Status: r.PostFormValue("answer"), Scope: scope, Confirmed: r.PostFormValue("confirmed") == "yes"}
	err := a.exec.Run(r.Context(), write.Session(accounts.Hash(a.sessionToken(r))), c)
	switch {
	case errors.Is(err, events.ErrConfirmCantGo):
		again(http.StatusOK, "", &answerConfirm{Count: strconv.Itoa(c.Dates) + " dates", Scope: scope})
		return
	case errors.Is(err, events.ErrDateGone):
		again(http.StatusConflict, "This date can't be answered now. The page shows how things stand.", nil)
		return
	case errors.Is(err, write.ErrSignedOut):
		http.Redirect(w, r, "/signin", http.StatusSeeOther)
		return
	case errors.Is(err, write.ErrNotAllowed):
		if !policy.IsAnswer(c.Status) || !policy.ValidScope(scope) {
			again(http.StatusUnprocessableEntity, "Choose Going, Maybe or Can't go.", nil)
			return
		}
		a.notFound(w, r)
		return
	case err != nil:
		a.serverError(w, r, err)
		return
	}
	if !htmx {
		http.Redirect(w, r, "/dates/"+f.date.UID+"?done="+c.Status+"&n="+strconv.Itoa(c.Dates), http.StatusSeeOther)
		return
	}
	again(http.StatusOK, answerStatus(c.Status, c.Dates), nil)
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
