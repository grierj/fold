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
	"fold/internal/answers"
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
	group groups.Group // the zero group for a church-wide event
	rel   policy.Rel
	host  bool       // the viewer hosts this church-wide event
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

// ev is the event in policy's terms, for this viewer.
func (f eventFacts) ev() policy.Event { return f.event.Policy(f.host) }

func (f eventFacts) canManage() bool {
	return policy.CanManageEvent(f.viewer(), f.ev(), f.group.Policy(), f.rel)
}

// seesEvent: someone who sees only a date opened wider than its series does
// not see the event (P7).
func (f eventFacts) seesEvent() bool {
	return policy.CanSeeEvent(f.viewer(), f.ev(), f.group.Policy(), f.rel)
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
		if !f.event.ChurchWide() {
			f.group, err = groups.ByID(r.Context(), q, f.event.GroupID)
			if errors.Is(err, groups.ErrNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
		}
		if f.session != nil {
			if f.event.ChurchWide() {
				f.host, err = events.IsHost(r.Context(), q, f.event.ID, f.session.Person.ID)
			} else {
				f.rel, err = groups.RelOf(r.Context(), q, f.group.ID, f.session.Person.ID)
			}
			if err != nil {
				return err
			}
		}
		if byDate {
			visible = policy.CanSeeDate(f.viewer(), f.ev(), f.date.Visibility, f.group.Policy(), f.rel)
		} else {
			visible = f.seesEvent()
		}
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
	Mine             string // the viewer's own answer, in words
	Moved, Cancelled bool
}

// mineWords is a person's own answer as a list shows it.
var mineWords = map[string]string{policy.AnswerGoing: "You're going", policy.AnswerMaybe: "You said maybe", policy.AnswerCant: "You can't go",
	policy.AnswerWaiting: "You're on the waitlist"}

func dateItems(list []events.Listed, loc *time.Location, now time.Time, titled bool) []dateItem {
	out := make([]dateItem, 0, len(list))
	for _, d := range list {
		it := dateItem{UID: d.UID, When: span(d.StartsAt, d.EndsAt, loc, now), Moved: d.Moved, Cancelled: d.Cancelled, Mine: mineWords[d.Mine]}
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
	if !f.event.ChurchWide() && policy.CanSeeGroup(f.viewer(), f.group.Policy(), f.rel) {
		return f.group.Name, "/groups/" + f.group.UID
	}
	return "", ""
}

var eventStatuses = map[string]string{
	"created": "Your event is scheduled.",
	"saved":   "Saved.",
	"removed": "Removed from its dates.",
	"many":    "That's a lot of changes in a short time, so this one could not be saved. Try again in a little while.",
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
	var hosts []events.Person
	var coming bool
	var telling int
	now := a.clock.Now()
	f, ok := a.loadEvent(w, r, false, func(q *sql.Tx, f *eventFacts) error {
		var err error
		if f.canManage() {
			if telling, err = events.Telling(r.Context(), q, f.event, f.group, 0, now, f.session.Person.ID); err != nil {
				return err
			}
		}
		if f.session != nil && f.event.VideoURL != "" {
			if coming, err = answers.GoingOrMaybe(r.Context(), q, f.event.ID, f.session.Person.ID, now); err != nil {
				return err
			}
		}
		filter := events.Filter{EventID: f.event.ID, ByHand: f.canManage()}
		if list, err = events.Upcoming(r.Context(), q, f.listViewer(), filter, now, limit); err != nil {
			return err
		}
		if f.event.ChurchWide() {
			hosts, err = events.Hosts(r.Context(), q, f.event.ID)
		}
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
	var hostNames []string
	for _, h := range hosts {
		hostNames = append(hostNames, h.Name)
	}
	f.Data = struct {
		Event                events.Event
		Pattern, ZoneLine    string
		Visibility           string
		GroupName, GroupHref string
		ChurchWide           bool
		Hosts                []string
		Maps                 string
		Dates                []dateItem
		More, All            bool
		CanManage, Frozen    bool
		CanHosts             bool
		Video                string
		Status               string
		Delete               Dialog
	}{
		Video: map[bool]string{true: f.event.VideoURL}[f.event.VideoURL != "" && policy.CanSeeVideo(f.canManage(), map[bool]string{true: policy.AnswerGoing}[coming])],
		Event: f.event, Pattern: pattern(f.event, now), ZoneLine: zoneLine(f.event.Zone, loc),
		Visibility: visibilityLabels[f.event.Visibility], GroupName: groupName, GroupHref: groupHref,
		ChurchWide: f.event.ChurchWide(), Hosts: hostNames,
		Maps: mapsLink(f.event.Place), Dates: dateItems(list, loc, now, false), More: more, All: all,
		CanManage: f.canManage(),
		Frozen:    !f.event.ChurchWide() && f.group.Status != policy.GroupActive,
		CanHosts:  policy.CanFindHosts(f.viewer(), f.ev()),
		Status:    eventStatuses[r.URL.Query().Get("done")],
		Delete: Dialog{ID: "delete-event", OpenLabel: "Delete the event", OpenKind: "danger-outline",
			Title: "Delete " + f.event.Title + "?", Text: "All its dates will be taken off. " + willTell(telling) + " This cannot be undone.",
			CancelLabel: "Keep it", ConfirmLabel: "Delete", ConfirmKind: "danger",
			Action: "/events/" + f.event.UID + "/delete", CSRF: a.csrfToken(r)},
	}
	a.render(w, r, http.StatusOK, "event", "", f.page)
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
	Reason                                 string // why it opens wider than its group
	Video                                  string
	Seats                                  string // "" is no limit
}

func readEventForm(r *http.Request) eventForm {
	get := func(k string) string { return strings.TrimSpace(r.PostFormValue(k)) }
	return eventForm{Title: r.PostFormValue("title"), About: r.PostFormValue("about"), Place: r.PostFormValue("place"),
		Day: get("date"), Time: get("time"), Hours: get("hours"), Minutes: get("minutes"), Repeat: get("repeat"),
		MonthWeek: get("month_week"), Until: get("until"), Visibility: get("visibility"), Reason: r.PostFormValue("reason"), Video: get("video"),
		Seats: get("seats")}
}

func formOf(e events.Event) eventForm {
	hours, mins := e.Minutes/60, e.Minutes%60
	week := ""
	if e.Repeat == events.Monthly {
		week = strconv.Itoa(e.MonthWeek)
	}
	seats := ""
	if e.Seats > 0 {
		seats = strconv.Itoa(e.Seats)
	}
	return eventForm{Title: e.Title, About: e.About, Place: e.Place, Day: e.StartsLocal[:10], Time: e.StartsLocal[11:],
		Hours: strconv.Itoa(hours), Minutes: strconv.Itoa(mins), Repeat: e.Repeat, MonthWeek: week, Until: e.Until, Visibility: e.Visibility, Video: e.VideoURL,
		Seats: seats}
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
	if !validVideo(v.Video) {
		errs["video"] = "Paste the call's whole link, starting with https://."
	}
	seats := 0
	if v.Seats != "" {
		n, err := strconv.Atoi(v.Seats)
		if err != nil || n < 1 || !policy.ValidSeats(n) {
			errs["seats"] = "Give a number of seats from 1 to 10,000, or leave it empty for no limit."
		}
		seats = n
	}
	if !multiline(v.Reason, events.MaxReason) {
		errs["reason"] = "Keep it under 500 characters."
	}
	v.Reason = trimLines(v.Reason)
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
	return events.Input{Title: v.Title, About: v.About, Place: v.Place, Visibility: v.Visibility, Pattern: p, Reason: v.Reason, VideoURL: v.Video,
		Seats: seats}, errs
}

// MaxVideo caps a video link's length.
const MaxVideo = 500

// validVideo: an optional link out to a video call (DEC-16): https only,
// with a host and no user name or password in it, one line, length-capped.
// The server never fetches it.
func validVideo(v string) bool {
	if v == "" {
		return true
	}
	if !oneLine(v, MaxVideo) || strings.ContainsAny(v, " \t") {
		return false
	}
	u, err := url.Parse(v)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.Opaque == ""
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
	Reason, Video, Seats                                  Field
	GroupVisibility                                       string // "" for a church-wide event
	ShowReason                                            bool
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

// eventFormView builds the form. allowed is the visibilities it may offer;
// groupVis is what a choice must be wider than to open the event and be
// told (openedFrom), or "" for a church-wide event.
func eventFormView(v eventForm, errs map[string]string, zone string, allowed []string, groupVis string) eventFormData {
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
		Reason: reasonField(v.Reason, errs["reason"]),
		Video: Field{ID: "video", Name: "video", Label: "Video call link (optional)", Type: "url", Inputmode: "url",
			Hint:  "For people joining online. Only people who say Going or Maybe, and the people running the event, see it.",
			Value: v.Video, MaxLength: MaxVideo, Error: errs["video"]},
		Seats: Field{ID: "seats", Name: "seats", Label: "Seats (optional)", Type: "number", Inputmode: "numeric",
			Hint:  "The most who can say Going to each date. When it's full, people can join a waitlist and move up when a seat opens. Leave it empty for no limit.",
			Value: v.Seats, MaxLength: 5, Error: errs["seats"]},
		ZoneHint:        "Times are in " + events.ZoneCity(zone) + " time (" + zone + ").",
		GroupVisibility: groupVis,
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
	d.Visibility = visibilityChoices("vis-", allowed, v.Visibility, groupVis)
	for _, vis := range allowed {
		d.ShowReason = d.ShowReason || (groupVis != "" && policy.MoreOpen(vis, groupVis))
	}
	if errs == nil {
		d.Title.Autofocus = true
	} else {
		focusFirstError(&d.Title, &d.About, &d.Day, &d.Time, &d.Hours, &d.Until, &d.Place, &d.Video, &d.Seats, &d.Reason)
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
	d.Errors = append(d.Errors, errorSummary(d.Until, d.Place, d.Video, d.Seats, d.Reason)...)
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

// visibilityChoices are the radios for who can see an event or date. One
// wider than its group says who will be told (DEC-28).
func visibilityChoices(prefix string, allowed []string, checked, groupVis string) []Choice {
	var out []Choice
	for _, vis := range allowed {
		hint := eventVisibilityHints[vis]
		if groupVis != "" && policy.MoreOpen(vis, groupVis) {
			hint += " That's wider than the group, so its other leaders and the admins are told."
		}
		out = append(out, Choice{ID: prefix + vis, Name: "visibility", Type: "radio", Value: vis, Label: visibilityLabels[vis], Hint: hint, Checked: checked == vis})
	}
	return out
}

func reasonField(value, err string) Field {
	return Field{ID: "reason", Name: "reason", Label: "Why open it wider than the group? (optional)",
		Hint: "Shown to the group's other leaders and the admins, and kept in the church's records.", Value: value, MaxLength: events.MaxReason, Error: err}
}

// openedFrom is what a change must be wider than to open the event and be
// told (DEC-28): its group's visibility, or its own when that is already
// wider. "" for a church-wide event, which has no group to be wider than.
func openedFrom(f eventFacts, current string) string {
	if f.event.ChurchWide() {
		return ""
	}
	return policy.Widest(f.group.Visibility, current)
}

// visibilities is what an event may be set to, most open first.
func visibilities(churchWide bool) []string {
	var out []string
	for _, v := range []string{policy.Public, policy.Church, policy.InviteOnly} {
		if policy.CanSetEventVisibility(v, churchWide) {
			out = append(out, v)
		}
	}
	return out
}

// eventHome is where a new event comes from: a group, or the church.
type eventHome struct {
	groupUID, groupName, groupVis string // empty for a church-wide event
	action, back                  string
}

func (a *App) showNewEvent(w http.ResponseWriter, r *http.Request, f pageFacts, h eventHome, status int, d eventFormData) {
	d.Action, d.Submit, d.Back = h.action, "Schedule it", h.back
	d.Heading = "Schedule an event"
	name := h.groupName
	if h.groupUID == "" {
		d.Heading, name = "Schedule a church-wide event", ""
	}
	f.Data = struct {
		eventFormData
		GroupName string
	}{d, name}
	a.render(w, r, status, "event_form", "", f.page)
}

func (a *App) newEventForm(w http.ResponseWriter, r *http.Request, f pageFacts, h eventHome) {
	vis := h.groupVis
	if vis == "" {
		vis = policy.Church
	}
	zone := churchZone(f).String()
	d := eventFormView(eventForm{Hours: "1", Minutes: "0", Repeat: events.Once, Visibility: vis}, nil, zone, visibilities(h.groupUID == ""), h.groupVis)
	a.showNewEvent(w, r, f, h, http.StatusOK, d)
}

// createEvent saves the new-event form for a group or the church.
func (a *App) createEvent(w http.ResponseWriter, r *http.Request, f pageFacts, h eventHome) {
	loc := churchZone(f)
	allowed := visibilities(h.groupUID == "")
	v := readEventForm(r)
	now := a.clock.Now()
	in, errs := v.check(loc, now, true)
	view := func(errs map[string]string) eventFormData {
		return eventFormView(v, errs, loc.String(), allowed, h.groupVis)
	}
	if len(errs) > 0 {
		a.showNewEvent(w, r, f, h, http.StatusUnprocessableEntity, view(errs))
		return
	}
	if warn := a.dstCheck(in, loc, now, v, r); warn != "" {
		d := view(nil)
		d.Warning, d.Timing = warn, v.timing()
		a.showNewEvent(w, r, f, h, http.StatusOK, d)
		return
	}
	if !a.limits.newEvents.Allow(f.session.Person.UID) {
		d := view(nil)
		d.TooMany, d.Timing = true, v.timing()
		a.showNewEvent(w, r, f, h, http.StatusTooManyRequests, d)
		return
	}
	c := &events.Create{GroupUID: h.groupUID, Input: in}
	err := a.exec.Run(r.Context(), write.Session(accounts.Hash(a.sessionToken(r))), c)
	switch {
	case errors.Is(err, write.ErrNotAllowed), errors.Is(err, write.ErrSignedOut):
		a.notFound(w, r)
		return
	case errors.Is(err, events.ErrStartRange):
		a.showNewEvent(w, r, f, h, http.StatusUnprocessableEntity, view(map[string]string{"date": "Choose a first date within the last year or the next two years."}))
		return
	case err != nil:
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/events/"+c.UID+"?done=created", http.StatusSeeOther)
}

func groupHome(f groupFacts) eventHome {
	return eventHome{groupUID: f.group.UID, groupName: f.group.Name, groupVis: f.group.Visibility,
		action: "/groups/" + f.group.UID + "/events/new", back: "/groups/" + f.group.UID}
}

var churchHome = eventHome{action: "/events/new", back: "/admin/events"}

func (a *App) getNewEvent(w http.ResponseWriter, r *http.Request) {
	f, ok := a.loadGroup(w, r, nil)
	if !ok {
		return
	}
	if !policy.CanScheduleEvent(f.viewer(), f.group.Policy(), f.rel) {
		a.notFound(w, r)
		return
	}
	a.newEventForm(w, r, f.pageFacts, groupHome(f))
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
	a.createEvent(w, r, f.pageFacts, groupHome(f))
}

// The church-wide event form is for admins only (DEC-7); anyone else gets
// the not-found page.
func (a *App) getNewChurchEvent(w http.ResponseWriter, r *http.Request) {
	if f, ok := a.adminPage(w, r, nil); ok {
		a.newEventForm(w, r, f, churchHome)
	}
}

func (a *App) postNewChurchEvent(w http.ResponseWriter, r *http.Request) {
	if f, ok := a.adminPage(w, r, nil); ok {
		a.createEvent(w, r, f, churchHome)
	}
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
	d := eventFormView(v, nil, f.event.Zone, visibilities(f.event.ChurchWide()), openedFrom(f, f.event.Visibility))
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
	allowed := visibilities(f.event.ChurchWide())
	gv := openedFrom(f, f.event.Visibility)
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
		show(http.StatusUnprocessableEntity, eventFormView(v, errs, f.event.Zone, allowed, gv))
		return
	}
	if warn := a.dstCheck(in, loc, now, v, r); warn != "" {
		d := eventFormView(v, nil, f.event.Zone, allowed, gv)
		d.Warning, d.Timing = warn, v.timing()
		show(http.StatusOK, d)
		return
	}
	if !a.limits.eventChanges.Allow(f.session.Person.UID) {
		d := eventFormView(v, nil, f.event.Zone, allowed, gv)
		d.TooMany = true
		show(http.StatusTooManyRequests, d)
		return
	}
	c := &events.Edit{UID: f.event.UID, Input: in, Version: version, Confirmed: r.PostFormValue("confirmed") == "yes", Private: r.PostFormValue("private") == "yes"}
	err := a.exec.Run(r.Context(), write.Session(accounts.Hash(a.sessionToken(r))), c)
	switch {
	case errors.Is(err, write.ErrConflict):
		// Someone else saved first: show what the event says now (P16).
		cur := formOf(f.event)
		d := eventFormView(cur, nil, f.event.Zone, allowed, gv)
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
			Told    string
			Version int64
			Timing  string
			Private string
		}{f.event, v, c.Drops, count, willTell(c.Told), version, v.timing(), r.PostFormValue("private")}
		a.render(w, r, http.StatusOK, "event_drop", "", f.page)
		return
	case errors.Is(err, events.ErrMorePrivate):
		a.showPrivate(w, r, f.pageFacts, privateView{Event: f.event, From: visibilityLabels[f.event.Visibility], To: visibilityLabels[v.Visibility],
			Action: "/events/" + f.event.UID + "/edit", Back: "/events/" + f.event.UID + "/edit", Hidden: v.hidden(version, r.PostFormValue("confirmed"))}, c.Unseen)
		return
	case errors.Is(err, events.ErrStartRange):
		show(http.StatusUnprocessableEntity, eventFormView(v, map[string]string{"date": "Choose a first date within the last year or the next two years."}, f.event.Zone, allowed, gv))
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

// hidden carries the form through a confirmation, with the answer to the
// other question it may have asked.
func (v eventForm) hidden(version int64, confirmed string) []Hidden {
	return []Hidden{{"version", strconv.FormatInt(version, 10)}, {"timing", v.timing()}, {"title", v.Title}, {"about", v.About},
		{"place", v.Place}, {"date", v.Day}, {"time", v.Time}, {"hours", v.Hours}, {"minutes", v.Minutes}, {"repeat", v.Repeat},
		{"month_week", v.MonthWeek}, {"until", v.Until}, {"visibility", v.Visibility}, {"reason", v.Reason}, {"video", v.Video},
		{"seats", v.Seats}, {"confirmed", confirmed}}
}

func (a *App) postDeleteEvent(w http.ResponseWriter, r *http.Request) {
	f, ok := a.loadEvent(w, r, false, nil)
	if !ok {
		return
	}
	if f.canManage() && !a.limits.eventChanges.Allow(f.session.Person.UID) {
		http.Redirect(w, r, "/events/"+f.event.UID+"?done=many", http.StatusSeeOther)
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
	if f.event.ChurchWide() {
		http.Redirect(w, r, "/?done=event-deleted", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/groups/"+f.group.UID+"?done=event-deleted", http.StatusSeeOther)
}
