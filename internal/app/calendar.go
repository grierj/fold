package app

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"fold/internal/events"
	"fold/internal/groups"
	"fold/internal/policy"
)

// The calendar (DEC-67): Agenda, Week and Month, of everything the viewer
// may see or just their own, in their zone. The URL holds the view, the
// day, and the filters, so every state is a plain link.

const (
	agendaStep = 30  // days the agenda adds each time
	agendaMax  = 365 // up to a year
	// calendarRows bounds one page's read; a month is at most 42 days.
	calendarRows = 2000
)

// calAsk is what the URL asks for, checked before any read.
type calAsk struct {
	view  string // agenda, week or month
	day   time.Time
	days  int // the agenda's length
	mine  bool
	group string // a group's uid, or ""
}

// askCalendar reads the URL. Anything unknown or out of range falls back
// to the default rather than failing: today, the agenda, 30 days.
func askCalendar(q url.Values, today time.Time, signedIn bool) calAsk {
	c := calAsk{view: "agenda", day: today, days: agendaStep, mine: signedIn && q.Get("mine") == "1"}
	if v := q.Get("view"); v == "week" || v == "month" {
		c.view = v
	}
	if d, err := time.ParseInLocation(events.DayLayout, q.Get("date"), today.Location()); err == nil &&
		!d.Before(today.AddDate(-2, 0, 0)) && !d.After(today.AddDate(2, 0, 0)) {
		c.day = d
	}
	if n, err := strconv.Atoi(q.Get("days")); err == nil {
		c.days = min(max(n, agendaStep), agendaMax)
	}
	if g := q.Get("group"); g != "" {
		c.group = g
	}
	return c
}

// href is the calendar's address for this ask, changed by edit.
func (c calAsk) href(edit func(*calAsk)) string {
	if edit != nil {
		edit(&c)
	}
	v := url.Values{}
	if c.view != "agenda" {
		v.Set("view", c.view)
	}
	v.Set("date", c.day.Format(events.DayLayout))
	if c.view == "agenda" && c.days != agendaStep {
		v.Set("days", strconv.Itoa(c.days))
	}
	if c.mine {
		v.Set("mine", "1")
	}
	if c.group != "" {
		v.Set("group", c.group)
	}
	return "/calendar?" + v.Encode()
}

// span is the days the view shows: [first, first+n) in the viewer's zone.
func (c calAsk) span() (first time.Time, n int) {
	switch c.view {
	case "week":
		return dayOf(c.day, -int(c.day.Weekday())), 7
	case "month":
		start := time.Date(c.day.Year(), c.day.Month(), 1, 0, 0, 0, 0, c.day.Location())
		return dayOf(start, -int(start.Weekday())), 42
	}
	return c.day, c.days
}

// dayOf is midnight n days after day, in day's zone. Days are counted on
// the calendar, never as 24 hours, so a daylight-saving change cannot move
// a date onto its neighbour (docs/HAZARDS.md section 1).
func dayOf(day time.Time, n int) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day()+n, 0, 0, 0, 0, day.Location())
}

// calRow is one date as the calendar shows it: never anyone else's answer,
// a count, or a group the viewer may not see (DEC-64, DEC-67).
type calRow struct {
	UID, Title, When, Place, Group string
	Starts                         string // its start alone, for a month's chip
	ChurchWide                     bool
	Mark                           string // going, ghost or "": how its answer is drawn (DEC-71)
	Label                          string // its answer in words, in the viewer's own scope
	Dot                            string // the phone month's dot: going, ghost, cant or other
}

type calDay struct {
	Key, Label, Href string
	Today            bool
	Rows             []calRow
}

// calCell is one day of the month's table.
type calCell struct {
	calDay
	Number          int
	InMonth, Picked bool
	Shown           []calRow // up to three chips from sm up
	More            int
	Dots            []string // the phone's dots, at most three
	Summary         string   // the phone day's name for a screen reader
}

type calendarData struct {
	View, Heading, ZoneLine string
	Views                   Pick
	Scopes                  *Pick
	Filter                  calFilter
	Prev, Today, Next       LinkButton
	Days                    []calDay // the agenda's days with dates, or the week's seven
	Weeks                   [][]calCell
	Picked                  *calDay
	More                    string // the agenda's Show more
	Full                    bool   // the read stopped at calendarRows
	Empty                   *Empty
}

// calFilter is the one-group filter, a GET form that keeps the rest.
type calFilter struct {
	Hidden []Hidden
	Group  Select
}

var calLabels = map[string]string{policy.AnswerGoing: "Going", policy.AnswerMaybe: "Maybe", policy.AnswerWaiting: "On the waitlist", policy.AnswerCant: "Can't go"}

func (a *App) getCalendar(w http.ResponseWriter, r *http.Request) {
	now := a.clock.Now()
	var f pageFacts
	var data calendarData
	seen := true
	err := a.reader.Read(r.Context(), func(q *sql.Tx) error {
		var err error
		if f, err = a.load(r.Context(), r, q); err != nil {
			return err
		}
		loc := viewerZone(f)
		today := dayOf(now.In(loc), 0)
		who := f.someone()
		ask := askCalendar(r.URL.Query(), today, who != nil)
		s := events.Scope{Viewer: listViewer(f), JustMine: ask.mine}
		if who != nil {
			s.Answerer = who.Person.ID
		}
		p := viewerFacts(f.session)
		if ask.group != "" {
			g, err := groups.ByUID(r.Context(), q, ask.group)
			if errors.Is(err, groups.ErrNotFound) {
				seen = false
				return nil
			}
			if err != nil {
				return err
			}
			var rel policy.Rel
			if f.session != nil {
				if rel, err = groups.RelOf(r.Context(), q, g.ID, f.session.Person.ID); err != nil {
					return err
				}
			}
			if !policy.CanSeeGroup(p, g.Policy(), rel) {
				seen = false
				return nil
			}
			s.GroupID, ask.group = g.ID, g.UID
		}
		options, err := calendarGroups(r, q, f)
		if err != nil {
			return err
		}
		first, n := ask.span()
		list, err := events.Calendar(r.Context(), q, s, first, dayOf(first, n), time.Unix(0, 0), now, calendarRows)
		if err != nil {
			return err
		}
		data = calendarView(ask, s.Kind(), list, options, today, now)
		data.Full = len(list) == calendarRows
		data.ZoneLine = "Times are shown in " + events.ZoneCity(loc.String()) + " time."
		return nil
	})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if !seen {
		a.notFound(w, r)
		return
	}
	f.Data = data
	a.render(w, r, http.StatusOK, "calendar", "", f.page)
}

// calendarGroups are the groups the filter offers: those the viewer may
// see, and the waiting or archived groups they lead.
func calendarGroups(r *http.Request, q *sql.Tx, f pageFacts) ([]groups.Listed, error) {
	list, err := groups.Directory(r.Context(), q, groups.Viewer(listViewer(f)), "", 500)
	if err != nil || f.session == nil {
		return list, err
	}
	mine, err := groups.Mine(r.Context(), q, f.session.Person.ID)
	for _, g := range mine {
		if g.Status != policy.GroupActive {
			list = append(list, g)
		}
	}
	return list, err
}

func calendarView(ask calAsk, kind int, list []events.CalDate, options []groups.Listed, today, now time.Time) calendarData {
	loc := today.Location()
	byDay := map[string][]calRow{}
	for _, c := range list {
		_, own := policy.InScope(ask.mine, c.Facts(kind), now)
		key := c.StartsAt.In(loc).Format(events.DayLayout)
		byDay[key] = append(byDay[key], calendarRow(c, own, loc, now))
	}
	first, n := ask.span()
	day := func(t time.Time) calDay {
		key := t.Format(events.DayLayout)
		return calDay{Key: key, Label: dayWords(t, now), Today: key == today.Format(events.DayLayout), Rows: byDay[key],
			Href: ask.href(func(c *calAsk) { c.view, c.day = "month", t })}
	}
	d := calendarData{View: ask.view}
	d.Views = Pick{Label: "Calendar view"}
	for _, v := range []struct{ view, label string }{{"agenda", "Agenda"}, {"week", "Week"}, {"month", "Month"}} {
		d.Views.Options = append(d.Views.Options, PickOption{Label: v.label, Current: ask.view == v.view,
			Href: ask.href(func(c *calAsk) { c.view = v.view })})
	}
	if kind != policy.ViewerSignedOut {
		d.Scopes = &Pick{Label: "Whose dates", Options: []PickOption{
			{Label: "Everything", Current: !ask.mine, Href: ask.href(func(c *calAsk) { c.mine = false })},
			{Label: "Just mine", Current: ask.mine, Href: ask.href(func(c *calAsk) { c.mine = true })}}}
	}
	d.Filter = calFilter{Hidden: []Hidden{{"date", ask.day.Format(events.DayLayout)}},
		Group: Select{ID: "group", Name: "group", Label: "Group", Options: []Option{{Value: "", Label: "Every group", Selected: ask.group == ""}}}}
	if ask.view != "agenda" {
		d.Filter.Hidden = append(d.Filter.Hidden, Hidden{"view", ask.view})
	}
	if ask.mine {
		d.Filter.Hidden = append(d.Filter.Hidden, Hidden{"mine", "1"})
	}
	if ask.view == "agenda" && ask.days != agendaStep {
		d.Filter.Hidden = append(d.Filter.Hidden, Hidden{"days", strconv.Itoa(ask.days)})
	}
	for _, g := range options {
		d.Filter.Group.Options = append(d.Filter.Group.Options, Option{Value: g.UID, Label: g.Name, Selected: g.UID == ask.group})
	}
	move := func(label, name string, to time.Time) LinkButton {
		return LinkButton{Kind: "secondary", Label: label, Name: name, Href: ask.href(func(c *calAsk) { c.day = to })}
	}
	d.Today = move("Today", "", today)
	switch ask.view {
	case "agenda":
		d.Heading = "From " + dayWords(ask.day, now)
		d.Prev, d.Next = move("Earlier", "", dayOf(ask.day, -agendaStep)), move("Later", "", dayOf(ask.day, ask.days))
		for i := range n {
			if dd := day(dayOf(first, i)); len(dd.Rows) > 0 {
				d.Days = append(d.Days, dd)
			}
		}
		if ask.days < agendaMax && len(list) < calendarRows {
			d.More = ask.href(func(c *calAsk) { c.days = min(c.days+agendaStep, agendaMax) })
		}
		if len(d.Days) == 0 {
			d.Empty = calendarEmpty(ask, kind)
		}
	case "week":
		d.Heading = "Week of " + dayWords(first, now)
		d.Prev, d.Next = move("Previous", "Previous week", dayOf(ask.day, -7)), move("Next", "Next week", dayOf(ask.day, 7))
		empty := true
		for i := range n {
			d.Days = append(d.Days, day(dayOf(first, i)))
			empty = empty && len(d.Days[i].Rows) == 0
		}
		if empty {
			d.Empty = &Empty{Title: "Nothing this week", Text: "Dates show here as they are scheduled.", Action: &d.Next}
		}
	case "month":
		d.Heading = monthWords(ask.day)
		start := time.Date(ask.day.Year(), ask.day.Month(), 1, 0, 0, 0, 0, loc)
		d.Prev, d.Next = move("Previous", "Previous month", start.AddDate(0, -1, 0)), move("Next", "Next month", start.AddDate(0, 1, 0))
		empty := true
		for w := range 6 {
			var week []calCell
			for i := range 7 {
				t := dayOf(first, w*7+i)
				c := calCell{calDay: day(t), Number: t.Day(), InMonth: t.Month() == ask.day.Month(), Picked: t.Equal(ask.day)}
				c.Shown = c.Rows[:min(3, len(c.Rows))]
				c.More = len(c.Rows) - len(c.Shown)
				for _, row := range c.Shown {
					c.Dots = append(c.Dots, row.Dot)
				}
				c.Summary = longDay(t) + ", " + plural(len(c.Rows), "date", "dates")
				if c.Today {
					c.Summary += ", today"
				}
				empty = empty && len(c.Rows) == 0
				week = append(week, c)
				if c.Picked {
					picked := c.calDay
					d.Picked = &picked
				}
			}
			d.Weeks = append(d.Weeks, week)
		}
		if empty {
			d.Empty = &Empty{Title: "Nothing this month", Text: "Dates show here as they are scheduled.", Action: &d.Next}
		}
	}
	return d
}

func calendarRow(c events.CalDate, own bool, loc *time.Location, now time.Time) calRow {
	start, end := c.StartsAt.In(loc), c.EndsAt.In(loc)
	row := calRow{UID: c.UID, Title: c.Title, When: timeSpan(start, end, now), Starts: clockWords(start), Place: c.Place, Group: c.GroupName, ChurchWide: c.ChurchWide, Dot: "other"}
	if !own {
		return row
	}
	row.Label, row.Mark, row.Dot = calLabels[c.Answer], "ghost", "ghost"
	switch {
	case c.Answer == policy.AnswerGoing:
		row.Mark, row.Dot = "going", "going"
	case c.Answer == policy.AnswerCant:
		row.Mark, row.Dot = "", "cant"
	case row.Label == "":
		row.Label = "Not answered"
	}
	return row
}

// calendarEmpty says what would appear and offers the next step (DEC-66).
func calendarEmpty(ask calAsk, kind int) *Empty {
	switch {
	case ask.mine:
		return &Empty{Image: "illustrations/all-caught-up.svg", Title: "Nothing of yours in these days",
			Text:   "Dates of your groups, church-wide dates and dates you answer show here.",
			Action: &LinkButton{Kind: "primary", Label: "See everything", Href: ask.href(func(c *calAsk) { c.mine = false })}}
	case kind == policy.ViewerSignedOut:
		return &Empty{Image: "illustrations/all-caught-up.svg", Title: "No public events in these days",
			Text: "Public events of the church and its groups show here.", Action: &LinkButton{Kind: "primary", Label: "See the groups", Href: "/groups"}}
	}
	return &Empty{Image: "illustrations/all-caught-up.svg", Title: "Nothing on the calendar in these days",
		Text:   "Dates of your groups, church-wide dates and public events coming soon show here.",
		Action: &LinkButton{Kind: "primary", Label: "Find a group", Href: "/groups"}}
}
