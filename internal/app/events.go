package app

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"fold/internal/accounts"
	"fold/internal/events"
	"fold/internal/groups"
	"fold/internal/policy"
	"fold/internal/write"
)

// Event and date pages. Every /events/{uid} and /dates/{uid} route loads
// through loadEvent, which asks policy and answers exactly as if the event
// did not exist when the viewer may not see it, on GET and POST alike.

type eventFacts struct {
	pageFacts
	event events.Event
	group groups.Group
	rel   policy.Rel
	date  events.Row // set on a date page
}

func (f eventFacts) viewer() *policy.Person { return viewerFacts(f.session) }

func (f eventFacts) listViewer() events.Viewer { return listViewer(f.pageFacts) }

// listViewer is the viewer in the terms the date lists' SQL filter needs.
func listViewer(f pageFacts) events.Viewer {
	if f.session == nil {
		return events.Viewer{}
	}
	return events.Viewer{ID: f.session.Person.ID, Admin: policy.IsAdmin(f.session.Person.Policy())}
}

func (f eventFacts) canManage() bool {
	return policy.CanManageEvent(f.viewer(), f.event.Policy(), f.group.Policy(), f.rel)
}

// loadEvent reads the event (or, with byDate, the date) named in the path,
// its group, the viewer and their relation, in one read; ok is false (and
// the not-found page written) when the viewer may not see it.
func (a *App) loadEvent(w http.ResponseWriter, r *http.Request, byDate bool, extra func(q *sql.Tx, f *eventFacts) error) (eventFacts, bool) {
	var f eventFacts
	visible := false
	err := a.reader.Read(r.Context(), func(q *sql.Tx) error {
		var err error
		if f.pageFacts, err = a.load(r.Context(), r, q); err != nil {
			return err
		}
		if byDate {
			f.date, f.event, err = events.DateByUID(r.Context(), q, r.PathValue("uid"))
		} else {
			f.event, err = events.ByUID(r.Context(), q, r.PathValue("uid"))
		}
		if errors.Is(err, events.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		f.group, err = groups.ByID(r.Context(), q, f.event.GroupID)
		if errors.Is(err, groups.ErrNotFound) {
			return nil // a church-wide event: milestone 1b
		}
		if err != nil {
			return err
		}
		if f.session != nil {
			if f.rel, err = groups.RelOf(r.Context(), q, f.group.ID, f.session.Person.ID); err != nil {
				return err
			}
		}
		visible = policy.CanSeeEvent(f.viewer(), f.event.Policy(), f.group.Policy(), f.rel)
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

// dateItem is one date in a list.
type dateItem struct {
	UID, Title, When string
	Moved            bool
}

func dateItems(list []events.Listed, loc *time.Location, now time.Time, titled bool) []dateItem {
	out := make([]dateItem, 0, len(list))
	for _, d := range list {
		it := dateItem{UID: d.UID, When: span(d.StartsAt, d.EndsAt, loc, now), Moved: d.Moved}
		if titled {
			it.Title = d.Title
		}
		out = append(out, it)
	}
	return out
}

// mapsLink is a plain search link built from the place's text. The server
// fetches nothing (R37).
func mapsLink(place string) string {
	if place == "" {
		return ""
	}
	return "https://www.google.com/maps/search/?api=1&query=" + url.QueryEscape(place)
}

// groupLink is the group's name and link when the viewer may see the group;
// an event can be more open than its group is now (DEC-48), and then the
// group stays hidden (docs/HAZARDS.md section 2).
func groupLink(f eventFacts) (name, href string) {
	if policy.CanSeeGroup(f.viewer(), f.group.Policy(), f.rel) {
		return f.group.Name, "/groups/" + f.group.UID
	}
	return "", ""
}

var eventStatuses = map[string]string{
	"created": "Your event is scheduled.",
	"saved":   "Saved.",
}

// The event page shows a few dates, or all of them with ?all=1.
const (
	eventDates    = 6
	allEventDates = 500
)

func (a *App) getEvent(w http.ResponseWriter, r *http.Request) {
	all := r.URL.Query().Get("all") == "1"
	limit := eventDates + 1
	if all {
		limit = allEventDates
	}
	var list []events.Listed
	now := a.clock.Now()
	f, ok := a.loadEvent(w, r, false, func(q *sql.Tx, f *eventFacts) error {
		var err error
		list, err = events.Upcoming(r.Context(), q, f.listViewer(), events.Filter{EventID: f.event.ID}, now, limit)
		return err
	})
	if !ok {
		return
	}
	more := !all && len(list) > eventDates
	if more {
		list = list[:eventDates]
	}
	loc := viewerZone(f.pageFacts)
	groupName, groupHref := groupLink(f)
	f.Data = struct {
		Event                events.Event
		Pattern, ZoneLine    string
		Visibility           string
		GroupName, GroupHref string
		Maps                 string
		Dates                []dateItem
		More, All            bool
		CanManage, Frozen    bool
		Status               string
		Delete               Dialog
	}{
		Event: f.event, Pattern: pattern(f.event, now), ZoneLine: zoneLine(f.event.Zone, loc),
		Visibility: visibilityLabels[f.event.Visibility], GroupName: groupName, GroupHref: groupHref,
		Maps: mapsLink(f.event.Place), Dates: dateItems(list, loc, now, false), More: more, All: all,
		CanManage: f.canManage(),
		Frozen:    f.group.Status != policy.GroupActive,
		Status:    eventStatuses[r.URL.Query().Get("done")],
		Delete: Dialog{ID: "delete-event", OpenLabel: "Delete the event", OpenKind: "danger-outline",
			Title: "Delete " + f.event.Title + "?", Text: "All its dates will be taken off. This cannot be undone.",
			CancelLabel: "Keep it", ConfirmLabel: "Delete", ConfirmKind: "danger",
			Action: "/events/" + f.event.UID + "/delete", CSRF: a.csrfToken(r)},
	}
	a.render(w, r, http.StatusOK, "event", "", f.page)
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
	groupName, groupHref := groupLink(f)
	f.Data = struct {
		Event                events.Event
		Day, When, ZoneLine  string
		Moved                string
		Cancelled, Past      bool
		GroupName, GroupHref string
		Maps                 string
	}{
		Event: f.event, Day: longDay(start), When: when, ZoneLine: zoneLine(f.event.Zone, loc), Moved: moved,
		Cancelled: f.date.Cancelled, Past: !f.date.EndsAt.After(now),
		GroupName: groupName, GroupHref: groupHref, Maps: mapsLink(f.event.Place),
	}
	a.render(w, r, http.StatusOK, "date", "", f.page)
}

// mustZone is an event's stored zone, which was checked when it was saved.
func mustZone(name string) *time.Location {
	loc, err := events.LoadZone(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

// The event form: title, description, then When, then place, then more
// options (DEC-95). Its times are in the event's zone, named on the form
// (DEC-57); a new event takes the church's (DEC-50).

type eventForm struct {
	Title, Hours, Minutes                  string
	About, Place, Day, Time, Until, Repeat string
	MonthWeek                              string
	Visibility                             string
}

func readEventForm(r *http.Request) eventForm {
	get := func(k string) string { return strings.TrimSpace(r.PostFormValue(k)) }
	return eventForm{Title: r.PostFormValue("title"), About: r.PostFormValue("about"), Place: r.PostFormValue("place"),
		Day: get("date"), Time: get("time"), Hours: get("hours"), Minutes: get("minutes"), Repeat: get("repeat"),
		MonthWeek: get("month_week"), Until: get("until"), Visibility: get("visibility")}
}

func formOf(e events.Event) eventForm {
	hours, mins := e.Minutes/60, e.Minutes%60
	week := ""
	if e.Repeat == events.Monthly {
		week = strconv.Itoa(e.MonthWeek)
	}
	return eventForm{Title: e.Title, About: e.About, Place: e.Place, Day: e.StartsLocal[:10], Time: e.StartsLocal[11:],
		Hours: strconv.Itoa(hours), Minutes: strconv.Itoa(mins), Repeat: e.Repeat, MonthWeek: week, Until: e.Until, Visibility: e.Visibility}
}

// check validates the form. It returns the input to save and an error per
// field; every limit is checked here, before anything expands the series
// (docs/HAZARDS.md section 4).
func (v *eventForm) check(loc *time.Location, now time.Time, startChanged bool) (events.Input, map[string]string) {
	errs := map[string]string{}
	title, ok := textValue(v.Title, events.MaxTitle)
	switch {
	case !ok:
		errs["title"] = "That title is too long or has characters we can't use."
	case title == "":
		errs["title"] = "Give the event a title."
	}
	v.Title = title
	if !multiline(v.About, events.MaxAbout) {
		errs["about"] = "Keep it under 2,000 characters."
	}
	v.About = trimLines(v.About)
	place, ok := textValue(v.Place, events.MaxPlace)
	if !ok {
		errs["place"] = "Keep it under 200 characters, on one line."
	}
	v.Place = place
	day, dayErr := time.Parse(events.DayLayout, v.Day)
	if dayErr != nil || day.Format(events.DayLayout) != v.Day {
		errs["date"] = "Choose the date of the first one."
	}
	if len(v.Time) == 8 {
		v.Time = v.Time[:5] // a browser may send seconds
	}
	if t, err := time.Parse("15:04", v.Time); err != nil || t.Format("15:04") != v.Time {
		errs["time"] = "Choose the time it starts, like 7:00 pm."
	}
	hours, herr := strconv.Atoi(orZero(v.Hours))
	mins, merr := strconv.Atoi(orZero(v.Minutes))
	total := hours*60 + mins
	if herr != nil || merr != nil || hours < 0 || mins < 0 || mins > 59 || total < 1 || total > events.MaxMinutes {
		errs["hours"] = "Choose how long it lasts: at least a minute, and no more than 7 days."
	}
	p := events.Pattern{Start: v.Day + "T" + v.Time, Minutes: total, Repeat: v.Repeat, Until: v.Until}
	switch v.Repeat {
	case events.Once, events.Weekly, events.Biweekly:
		v.MonthWeek = ""
	case events.Monthly:
		if v.MonthWeek == "" && dayErr == nil {
			v.MonthWeek = strconv.Itoa(events.WeeksOf(day)[0])
		}
		w, err := strconv.Atoi(v.MonthWeek)
		if err != nil || (w != events.Last && (w < 1 || w > 4)) {
			errs["month_week"] = "Choose which week of the month."
		}
		p.MonthWeek = w
	default:
		errs["repeat"] = "Choose how often it happens."
		v.Repeat = events.Once
	}
	if v.Repeat == events.Once {
		v.Until, p.Until = "", ""
	}
	if v.Until != "" {
		if u, err := time.Parse(events.DayLayout, v.Until); err != nil || u.Format(events.DayLayout) != v.Until {
			errs["until"] = "Choose a last date, or leave it empty."
		} else if dayErr == nil && u.Before(day) {
			errs["until"] = "The last date can't be before the first."
		}
	}
	if !policy.ValidVisibility(v.Visibility) {
		errs["visibility"] = "Choose who can see it."
	}
	if len(errs) == 0 {
		if startChanged {
			if err := events.CheckStart(p, loc, now); err != nil {
				errs["date"] = "Choose a first date within the last year or the next two years."
			}
		}
		if err := p.Check(); errors.Is(err, events.ErrWeekMismatch) {
			errs["month_week"] = weekMismatch(day)
		} else if err != nil {
			errs["date"] = "Check the date and time."
		}
	}
	return events.Input{Title: v.Title, About: v.About, Place: v.Place, Visibility: v.Visibility, Pattern: p}, errs
}

func orZero(s string) string {
	if s == "" {
		return "0"
	}
	return s
}

// textValue is text() for a value already read: one line, at most max
// characters.
func textValue(v string, max int) (string, bool) {
	v = strings.TrimSpace(v)
	return v, oneLine(v, max)
}

// weekMismatch names the weeks a first date can repeat on.
func weekMismatch(day time.Time) string {
	weeks := events.WeeksOf(day)
	var names []string
	for _, w := range weeks {
		names = append(names, events.Ordinal(w))
	}
	which := day.Format("Mon, Jan 2") + " is the " + names[0] + " " + day.Weekday().String() + " of its month"
	if len(names) == 2 {
		which += ", and the last"
	}
	if slices.Equal(weeks, []int{events.Last}) {
		return which + ". Not every month has a 5th " + day.Weekday().String() + ", so choose Last."
	}
	return which + ", so choose " + strings.Join(names, " or ") + "."
}

// dstWarning words the nights R34 changes a date's time, or "" when none.
func dstWarning(ds []events.Date, loc *time.Location, now time.Time) string {
	var parts []string
	for _, d := range ds {
		local := d.StartsAt.In(loc)
		switch {
		case d.Moved:
			parts = append(parts, "On "+dayWords(local, now)+", the clocks go forward, so that date starts at "+clockWords(local)+".")
		case d.Doubled:
			parts = append(parts, "On "+dayWords(local, now)+", the clocks go back and "+clockWords(local)+" happens twice, so that date starts at the first one.")
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " ") + " Save again to keep this time, or choose another."
}

// timing is what the daylight-saving warning was given for, so it is given
// once for each choice of times.
func (v eventForm) timing() string {
	return strings.Join([]string{v.Day, v.Time, v.Repeat, v.MonthWeek, v.Until}, "|")
}

type eventFormData struct {
	Title, About, Place, Day, Time, Hours, Minutes, Until Field
	Repeat, MonthWeek, Visibility                         []Choice
	ZoneHint                                              string
	Warning                                               string
	Errors                                                []FieldError
	Action, Submit, Heading                               string
	Version                                               int64
	Timing                                                string // the timing already warned about
	Conflict, TooMany                                     bool
	Back                                                  string
}

var repeatLabels = []struct{ value, label string }{
	{events.Once, "Just once"}, {events.Weekly, "Every week"}, {events.Biweekly, "Every other week"}, {events.Monthly, "Every month"},
}

// eventFormView builds the form. allowed is the visibilities it may offer.
func eventFormView(v eventForm, errs map[string]string, zone string, allowed []string) eventFormData {
	d := eventFormData{
		Title: Field{ID: "title", Name: "title", Label: "Title", Value: v.Title, MaxLength: events.MaxTitle, Required: true, Error: errs["title"]},
		About: Field{ID: "about", Name: "about", Label: "Description", Hint: "What happens, who it's for, and anything to bring.",
			Value: v.About, MaxLength: events.MaxAbout, Error: errs["about"]},
		Day:     Field{ID: "date", Name: "date", Label: "Date", Hint: "For a repeating event, the first date.", Type: "date", Value: v.Day, Required: true, Error: errs["date"]},
		Time:    Field{ID: "time", Name: "time", Label: "Starts at", Type: "time", Value: v.Time, Required: true, Error: errs["time"]},
		Hours:   Field{ID: "hours", Name: "hours", Label: "Hours", Type: "number", Inputmode: "numeric", Value: v.Hours, MaxLength: 3, Error: errs["hours"]},
		Minutes: Field{ID: "minutes", Name: "minutes", Label: "Minutes", Type: "number", Inputmode: "numeric", Value: v.Minutes, MaxLength: 2},
		Until: Field{ID: "until", Name: "until", Label: "Last date (optional)", Hint: "Leave it empty to keep going.", Type: "date",
			Value: v.Until, Error: errs["until"]},
		Place: Field{ID: "place", Name: "place", Label: "Where", Hint: "An address or a room. People get a link to open it in maps.",
			Value: v.Place, MaxLength: events.MaxPlace, Error: errs["place"]},
		ZoneHint: "Times are in " + events.ZoneCity(zone) + " time (" + zone + ").",
	}
	for _, o := range repeatLabels {
		d.Repeat = append(d.Repeat, Choice{ID: "repeat-" + o.value, Name: "repeat", Type: "radio", Value: o.value, Label: o.label, Checked: v.Repeat == o.value})
	}
	for _, w := range []int{1, 2, 3, 4, events.Last} {
		s := strconv.Itoa(w)
		label := events.Ordinal(w)
		if w == events.Last {
			label = "Last"
		}
		d.MonthWeek = append(d.MonthWeek, Choice{ID: "week-" + s, Name: "month_week", Type: "radio", Value: s, Label: label, Checked: v.MonthWeek == s})
	}
	for _, vis := range allowed {
		d.Visibility = append(d.Visibility, Choice{ID: "vis-" + vis, Name: "visibility", Type: "radio", Value: vis,
			Label: visibilityLabels[vis], Hint: eventVisibilityHints[vis], Checked: v.Visibility == vis})
	}
	if errs == nil {
		d.Title.Autofocus = true
	} else {
		focusFirstError(&d.Title, &d.About, &d.Day, &d.Time, &d.Hours, &d.Until, &d.Place)
	}
	d.Errors = errorSummary(d.Title, d.About, d.Day, d.Time, d.Hours)
	for _, k := range []struct {
		key     string
		choices []Choice
	}{{"repeat", d.Repeat}, {"month_week", d.MonthWeek}} {
		if e := errs[k.key]; e != "" {
			k.choices[0].Error = e
			d.Errors = append(d.Errors, FieldError{ID: k.choices[0].ID, Message: e})
		}
	}
	d.Errors = append(d.Errors, errorSummary(d.Until, d.Place)...)
	if e := errs["visibility"]; e != "" && len(d.Visibility) > 0 {
		d.Visibility[0].Error = e
		d.Errors = append(d.Errors, FieldError{ID: d.Visibility[0].ID, Message: e})
	}
	return d
}

var eventVisibilityHints = map[string]string{
	policy.Public:     "Anyone can see it, signed in or not.",
	policy.Church:     "Anyone signed in at the church can see it.",
	policy.InviteOnly: "Only the group's members and admins can see it.",
}

// visibilities is what an event may be set to, most open first.
func visibilities(from, groupVis string) []string {
	var out []string
	for _, v := range []string{policy.Public, policy.Church, policy.InviteOnly} {
		if policy.CanSetEventVisibility(from, v, groupVis) {
			out = append(out, v)
		}
	}
	return out
}

func (a *App) getNewEvent(w http.ResponseWriter, r *http.Request) {
	f, ok := a.loadGroup(w, r, nil)
	if !ok {
		return
	}
	if !policy.CanScheduleEvent(f.viewer(), f.group.Policy(), f.rel) {
		a.notFound(w, r)
		return
	}
	zone := churchZone(f.pageFacts).String()
	d := eventFormView(eventForm{Hours: "1", Minutes: "0", Repeat: events.Once, Visibility: f.group.Visibility}, nil, zone,
		visibilities(f.group.Visibility, f.group.Visibility))
	a.showNewEvent(w, r, f, http.StatusOK, d)
}

func (a *App) showNewEvent(w http.ResponseWriter, r *http.Request, f groupFacts, status int, d eventFormData) {
	d.Action, d.Submit, d.Heading, d.Back = "/groups/"+f.group.UID+"/events/new", "Schedule it", "Schedule an event", "/groups/"+f.group.UID
	f.Data = struct {
		eventFormData
		GroupName string
	}{d, f.group.Name}
	a.render(w, r, status, "event_form", "", f.page)
}

func (a *App) postNewEvent(w http.ResponseWriter, r *http.Request) {
	f, ok := a.loadGroup(w, r, nil)
	if !ok {
		return
	}
	if !policy.CanScheduleEvent(f.viewer(), f.group.Policy(), f.rel) {
		a.notFound(w, r)
		return
	}
	loc := churchZone(f.pageFacts)
	allowed := visibilities(f.group.Visibility, f.group.Visibility)
	v := readEventForm(r)
	now := a.clock.Now()
	in, errs := v.check(loc, now, true)
	if len(errs) > 0 {
		a.showNewEvent(w, r, f, http.StatusUnprocessableEntity, eventFormView(v, errs, loc.String(), allowed))
		return
	}
	if warn := a.dstCheck(in, loc, now, v, r); warn != "" {
		d := eventFormView(v, nil, loc.String(), allowed)
		d.Warning, d.Timing = warn, v.timing()
		a.showNewEvent(w, r, f, http.StatusOK, d)
		return
	}
	if !a.limits.newEvents.Allow(f.session.Person.UID) {
		d := eventFormView(v, nil, loc.String(), allowed)
		d.TooMany, d.Timing = true, v.timing()
		a.showNewEvent(w, r, f, http.StatusTooManyRequests, d)
		return
	}
	c := &events.Create{GroupUID: f.group.UID, Input: in}
	err := a.exec.Run(r.Context(), write.Session(accounts.Hash(a.sessionToken(r))), c)
	switch {
	case errors.Is(err, write.ErrNotAllowed), errors.Is(err, write.ErrSignedOut):
		a.notFound(w, r)
		return
	case errors.Is(err, events.ErrStartRange):
		errs = map[string]string{"date": "Choose a first date within the last year or the next two years."}
		a.showNewEvent(w, r, f, http.StatusUnprocessableEntity, eventFormView(v, errs, loc.String(), allowed))
		return
	case err != nil:
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/events/"+c.UID+"?done=created", http.StatusSeeOther)
}

// dstCheck is R34's warning: given once for each choice of times, when a
// date those times make falls in a skipped or doubled hour.
func (a *App) dstCheck(in events.Input, loc *time.Location, now time.Time, v eventForm, r *http.Request) string {
	if r.PostFormValue("timing") == v.timing() {
		return ""
	}
	ds, err := events.Ahead(in.Pattern, loc, now)
	if err != nil {
		return ""
	}
	return dstWarning(ds, loc, now)
}

func (a *App) getEditEvent(w http.ResponseWriter, r *http.Request) {
	f, ok := a.loadEvent(w, r, false, nil)
	if !ok {
		return
	}
	if !f.canManage() {
		a.notFound(w, r)
		return
	}
	v := formOf(f.event)
	d := eventFormView(v, nil, f.event.Zone, visibilities(f.event.Visibility, f.group.Visibility))
	d.Version, d.Timing = f.event.Version, v.timing()
	a.showEditEvent(w, r, f, http.StatusOK, d)
}

func (a *App) showEditEvent(w http.ResponseWriter, r *http.Request, f eventFacts, status int, d eventFormData) {
	d.Action, d.Submit, d.Heading, d.Back = "/events/"+f.event.UID+"/edit", "Save", "Edit the event", "/events/"+f.event.UID
	f.Data = struct {
		eventFormData
		GroupName string
	}{d, ""}
	a.render(w, r, status, "event_form", "", f.page)
}

// postEditEvent saves the series edit. It may stop twice before saving: to
// warn once about a daylight-saving night (R34), and to say how many dates
// the new times would cancel (DEC-91). Each step carries every field.
func (a *App) postEditEvent(w http.ResponseWriter, r *http.Request) {
	f, ok := a.loadEvent(w, r, false, nil)
	if !ok {
		return
	}
	if !f.canManage() {
		a.notFound(w, r)
		return
	}
	loc := mustZone(f.event.Zone)
	allowed := visibilities(f.event.Visibility, f.group.Visibility)
	v := readEventForm(r)
	version, _ := strconv.ParseInt(r.PostFormValue("version"), 10, 64)
	now := a.clock.Now()
	in, errs := v.check(loc, now, v.Day != f.event.StartsLocal[:10])
	show := func(status int, d eventFormData) {
		if d.Version == 0 {
			d.Version = version
		}
		if d.Timing == "" {
			d.Timing = r.PostFormValue("timing")
		}
		a.showEditEvent(w, r, f, status, d)
	}
	if len(errs) > 0 {
		show(http.StatusUnprocessableEntity, eventFormView(v, errs, f.event.Zone, allowed))
		return
	}
	if warn := a.dstCheck(in, loc, now, v, r); warn != "" {
		d := eventFormView(v, nil, f.event.Zone, allowed)
		d.Warning, d.Timing = warn, v.timing()
		show(http.StatusOK, d)
		return
	}
	if !a.limits.eventChanges.Allow(f.session.Person.UID) {
		d := eventFormView(v, nil, f.event.Zone, allowed)
		d.TooMany = true
		show(http.StatusTooManyRequests, d)
		return
	}
	c := &events.Edit{UID: f.event.UID, Input: in, Version: version, Confirmed: r.PostFormValue("confirmed") == "yes"}
	err := a.exec.Run(r.Context(), write.Session(accounts.Hash(a.sessionToken(r))), c)
	switch {
	case errors.Is(err, write.ErrConflict):
		// Someone else saved first: show what the event says now (P16).
		cur := formOf(f.event)
		d := eventFormView(cur, nil, f.event.Zone, visibilities(f.event.Visibility, f.group.Visibility))
		d.Version, d.Timing, d.Conflict = f.event.Version, cur.timing(), true
		a.showEditEvent(w, r, f, http.StatusConflict, d)
		return
	case errors.Is(err, events.ErrDropsDates):
		count := strconv.Itoa(c.Drops) + " dates"
		if c.Drops == 1 {
			count = "1 date"
		}
		f.Data = struct {
			Event   events.Event
			Form    eventForm
			Drops   int
			Count   string
			Version int64
			Timing  string
		}{f.event, v, c.Drops, count, version, v.timing()}
		a.render(w, r, http.StatusOK, "event_drop", "", f.page)
		return
	case errors.Is(err, events.ErrStartRange):
		show(http.StatusUnprocessableEntity, eventFormView(v, map[string]string{"date": "Choose a first date within the last year or the next two years."}, f.event.Zone, allowed))
		return
	case errors.Is(err, write.ErrNotAllowed), errors.Is(err, write.ErrSignedOut):
		a.notFound(w, r)
		return
	case err != nil:
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/events/"+f.event.UID+"?done=saved", http.StatusSeeOther)
}

func (a *App) postDeleteEvent(w http.ResponseWriter, r *http.Request) {
	f, ok := a.loadEvent(w, r, false, nil)
	if !ok {
		return
	}
	err := a.exec.Run(r.Context(), write.Session(accounts.Hash(a.sessionToken(r))), events.Delete{UID: f.event.UID})
	switch {
	case errors.Is(err, write.ErrNotAllowed), errors.Is(err, write.ErrSignedOut):
		a.notFound(w, r)
		return
	case err != nil:
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/groups/"+f.group.UID+"?done=event-deleted", http.StatusSeeOther)
}
