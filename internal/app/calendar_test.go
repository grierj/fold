package app_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// The fake clock starts on Wed 25 Nov 2026, 10:00 am in Boise.

var dayHeading = regexp.MustCompile(`<h3 class="font-bold">([^<]+)`)

// dayOn is the day heading a date's row sits under on a calendar page.
func dayOn(body, date string) string {
	i := strings.Index(body, `href="/dates/`+date+`"`)
	if i < 0 {
		return ""
	}
	m := dayHeading.FindAllStringSubmatch(body[:i], -1)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[len(m)-1][1])
}

func setZone(s *site, email, zone string) {
	s.t.Helper()
	if _, err := s.Writer.Exec(`UPDATE people SET zone = ? WHERE email = ?`, zone, email); err != nil {
		s.t.Fatal(err)
	}
}

// HAZARDS 1: days are the viewer's. An 11:30 pm Boise date is on the next
// day seen from New York, and a date just after midnight stays on its own
// day across the night the clocks change.
func TestCalendarDaysAreTheViewers(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	group := s.start(admin, "Night Owls", "public")
	late := firstDate(admin.get("/events/" + s.schedule(admin, group, eventForm("Late prayer", "2026-12-01", "23:30", "once"))).body)
	mia := s.member("Mia Member", "mia@example.org")
	setZone(s, "mia@example.org", "America/New_York")
	for _, path := range []string{"/calendar?date=2026-12-01", "/calendar?date=2026-12-01&view=week"} {
		if got := dayOn(mia.get(path).body, late); got != "Wed, Dec 2" {
			t.Errorf("%s from New York: on %q", path, got)
		}
	}
	if r := mia.get("/calendar?view=month&date=2026-12-02"); !strings.Contains(between(r.body, `id="picked"`, "</section>"), late) {
		t.Error("New York's month does not list the date on Wednesday")
	}
	if r := mia.get("/calendar?date=2026-12-01"); !strings.Contains(r.body, "1:30 to 3:00 am") || !strings.Contains(r.body, "New York time") {
		t.Fatal("New York's agenda is not in New York time")
	}
	if r := admin.get("/calendar?date=2026-12-01&view=week"); dayOn(r.body, late) != "Tue, Dec 1" || !strings.Contains(r.body, "11:30 pm to Wed, Dec 2, 1:00 am") {
		t.Fatalf("Boise's week: %q", dayOn(r.body, late))
	}

	// Sunday 14 March 2027 is 23 hours long in Boise.
	sunday := firstDate(admin.get("/events/" + s.schedule(admin, group, eventForm("Sunday night", "2027-03-14", "23:30", "once"))).body)
	monday := firstDate(admin.get("/events/" + s.schedule(admin, group, eventForm("Monday dawn", "2027-03-15", "00:30", "once"))).body)
	r := admin.get("/calendar?view=week&date=2027-03-14")
	if dayOn(r.body, sunday) != "Sun, Mar 14, 2027" || dayOn(r.body, monday) != "Mon, Mar 15, 2027" {
		t.Fatalf("across the change: Sunday's on %q, Monday's on %q", dayOn(r.body, sunday), dayOn(r.body, monday))
	}
	if r := admin.get("/calendar?view=month&date=2027-03-15"); !strings.Contains(r.body, `<span class="sr-only">Monday, March 15, 2027, 1 date</span>`) {
		t.Fatal("the month's Monday after the change lost its date")
	}
}

// DEC-6, DEC-28, HAZARDS 2: an invite-only group's date is in nobody
// else's calendar, view or Home, and its group answers the filter as
// missing; one date of it opened wider shows without naming the group.
func TestInviteOnlyDatesStayInTheGroup(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	mel := s.member("Mel Leader", "mel@example.org")
	group := s.start(mel, "Elders Prayer", "invite_only")
	admin.post("/admin/groups", "/admin/groups/"+group+"/approve", nil)
	form := eventForm("Prayer night", "2026-12-02", "19:00", "weekly")
	form.Set("visibility", "invite_only")
	uid := s.schedule(mel, group, form)
	date := firstDate(mel.get("/events/" + uid).body)
	mia := s.member("Mia Member", "mia@example.org")
	stranger := s.browser()
	pages := []string{"/calendar", "/calendar?mine=1", "/calendar?view=week&date=2026-12-02", "/calendar?view=month&date=2026-12-02", "/"}
	for _, b := range []*browser{mia, stranger} {
		for _, p := range pages {
			if r := b.get(p); r.status != http.StatusOK || strings.Contains(r.body, "/dates/") || strings.Contains(r.body, "Elders") || strings.Contains(r.body, "Prayer night") {
				t.Errorf("%s: %d, shows the invite-only group's date", p, r.status)
			}
		}
		if r := b.get("/calendar?group=" + group); r.status != http.StatusNotFound {
			t.Errorf("filtering by an unseen group: %d", r.status)
		}
	}
	if r := mia.get("/calendar?group=nope"); r.status != http.StatusNotFound {
		t.Fatalf("filtering by a missing group: %d", r.status)
	}
	if r := mel.get("/calendar?group=" + group); dayOn(r.body, date) != "Wed, Dec 2" || !strings.Contains(r.body, "Elders Prayer") || !strings.Contains(r.body, "Going") && !strings.Contains(r.body, "Not answered") {
		t.Fatal("the leader's own calendar lacks the group's date")
	}

	open := "/dates/" + date + "/open"
	mel.post(open, open, url.Values{"visibility": {"church"}, "reason": {"Everyone is welcome."}})
	r := mia.get("/calendar")
	if dayOn(r.body, date) != "Wed, Dec 2" || strings.Contains(r.body, "Elders") {
		t.Fatalf("an opened date: on %q, names the group %v", dayOn(r.body, date), strings.Contains(r.body, "Elders"))
	}
	if r := mia.get("/calendar?mine=1"); strings.Contains(r.body, date) {
		t.Fatal("an opened date is in Just mine unanswered (DEC-73)")
	}
}

// DEC-64, DEC-71: the calendar carries the viewer's own answer and nothing
// about anyone else's; Can't go leaves Just mine and stays labelled in
// Everything; unanswered church-wide dates stay within 30 days.
func TestCalendarAnswersAreYourOwn(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	group := s.start(admin, "Study", "church")
	form := eventForm("Study", "2026-12-01", "19:00", "weekly")
	form.Set("visibility", "church")
	uid := s.schedule(admin, group, form)
	body := admin.get("/events/" + uid).body
	first := firstDate(body)
	second := firstDate(body[strings.Index(body, first)+len(first):])
	mia := s.member("Mia Member", "mia@example.org")
	sam := s.member("Sam Member", "sam@example.org")
	for _, b := range []*browser{mia, sam} {
		b.post("/groups/"+group, "/groups/"+group+"/join", nil)
	}
	answer(mia, first, "going", "date")
	answer(mia, second, "cant", "date")

	r := mia.get("/calendar")
	if !strings.Contains(r.body, `class="cal-row cal-going" href="/dates/`+first+`"`) || !strings.Contains(r.body, "Can&#39;t go") {
		t.Fatal("Mia's Everything lacks her Going or her Can't go")
	}
	if r := mia.get("/calendar?mine=1"); !strings.Contains(r.body, first) || strings.Contains(r.body, second) {
		t.Fatal("Just mine keeps a Can't go, or lost a Going")
	}
	r = sam.get("/calendar")
	for _, leak := range []string{"Mia", "Can&#39;t go", "1 going", "Going</span>"} {
		if strings.Contains(r.body, leak) {
			t.Errorf("Sam's calendar shows %q", leak)
		}
	}
	if !strings.Contains(r.body, `class="cal-row cal-ghost" href="/dates/`+first+`"`) || !strings.Contains(r.body, "Not answered") {
		t.Fatal("Sam's own group's date is not marked unanswered")
	}

	// A church-wide date 40 days out is not in Just mine until answered.
	wide := url.Values{"title": {"Harvest supper"}, "about": {""}, "date": {"2027-01-04"}, "time": {"18:00"}, "hours": {"2"}, "minutes": {"0"},
		"repeat": {"once"}, "place": {"Hall"}, "visibility": {"church"}}
	r = admin.post("/events/new", "/events/new", wide)
	wuid := strings.TrimPrefix(strings.SplitN(r.header.Get("Location"), "?", 2)[0], "/events/")
	wdate := firstDate(admin.get("/events/" + wuid).body)
	if r := sam.get("/calendar?mine=1&days=60"); strings.Contains(r.body, wdate) {
		t.Fatal("an unanswered church-wide date past 30 days is in Just mine")
	}
	answer(sam, wdate, "maybe", "date")
	if r := sam.get("/calendar?mine=1&days=60"); !strings.Contains(r.body, wdate) || !strings.Contains(r.body, "Maybe") {
		t.Fatal("an answered church-wide date is not in Just mine")
	}
}

// Anything the URL says out of range falls back, and the views move by
// their own steps.
func TestCalendarURL(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	for path, want := range map[string]string{
		"/calendar":                                   "From Wed, Nov 25",
		"/calendar?view=bogus&date=2026-13-45":        "From Wed, Nov 25",
		"/calendar?date=2031-01-01":                   "From Wed, Nov 25",
		"/calendar?view=week&date=2026-12-03":         "Week of Sun, Nov 29",
		"/calendar?view=month&date=2026-12-31":        "December 2026",
		"/calendar?days=100000":                       "From Wed, Nov 25",
		"/calendar?mine=1&group=&view=week&days=-5":   "Week of Sun, Nov 22",
		"/calendar?view=month&date=2027-02-01&mine=1": "February 2027",
	} {
		if r := admin.get(path); r.status != http.StatusOK || !strings.Contains(r.body, want) {
			t.Errorf("%s: %d, want %q", path, r.status, want)
		}
	}
	r := admin.get("/calendar?view=month&date=2026-12-31")
	for _, link := range []string{`href="/calendar?date=2026-11-01&amp;view=month"`, `href="/calendar?date=2027-01-01&amp;view=month"`, `aria-label="Next month"`} {
		if !strings.Contains(r.body, link) {
			t.Errorf("the month lacks %s", link)
		}
	}
	if r := admin.get("/calendar?days=360"); !strings.Contains(r.body, "days=365") {
		t.Fatal("Show more does not stop at a year")
	}
	if r := admin.get("/calendar?days=365"); strings.Contains(r.body, "Show 30 more days") {
		t.Fatal("Show more past a year")
	}
	if r := s.browser().get("/calendar?mine=1"); strings.Contains(r.body, "Just mine") || r.status != http.StatusOK {
		t.Fatal("signed out is offered Just mine")
	}
	if r := admin.get("/calendar?days=90"); !strings.Contains(r.body, `name="days" value="90"`) {
		t.Fatal("the group filter drops the agenda's length")
	}
	if r := admin.get("/calendar?view=month&date=2027-03-01"); !strings.Contains(r.body, "Nothing this month") {
		t.Fatal("an empty month says nothing")
	}
}

// DEC-94: Home greets a member by the time of day and lists their next four
// dates with their answers; with no groups, a few groups meeting soon they
// may join. Signed out, the church's name and its public events.
func TestHomeComingUp(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	group := s.start(admin, "Supper Club", "public")
	uid := s.schedule(admin, group, eventForm("Supper", "2026-12-01", "18:00", "weekly"))
	date := firstDate(admin.get("/events/" + uid).body)
	mia := s.member("Mia Member", "mia@example.org")
	r := mia.get("/")
	if !strings.Contains(r.body, "Good morning, Mia Member") || !strings.Contains(r.body, "Groups meeting soon") ||
		!strings.Contains(r.body, "Meets next Tue, Dec 1, 6:00 pm") || strings.Contains(r.body, "/dates/") {
		t.Fatal("a member in no group: no greeting, or no groups meeting soon, or someone else's dates")
	}
	answer(mia, date, "going", "date")
	r = mia.get("/")
	if !strings.Contains(r.body, `class="cal-row cal-going" href="/dates/`+date+`"`) || !strings.Contains(r.body, "Tue, Dec 1, 6:00 to 7:30 pm") || strings.Count(r.body, `<a class="cal-row`) != 1 {
		t.Fatalf("Coming up lacks her answered date, or lists unanswered public ones: %s", between(r.body, "Coming up", "</section>"))
	}
	mia.post("/groups/"+group, "/groups/"+group+"/join", nil)
	r = mia.get("/")
	if n := strings.Count(r.body, `<a class="cal-row`); n != 4 || strings.Contains(r.body, "Groups meeting soon") || !strings.Contains(r.body, "See calendar") {
		t.Fatalf("Coming up for a member: %d rows", n)
	}
	s.Clock.Advance(9 * 60 * 60 * 1e9) // 7 pm in Boise
	if r := mia.get("/"); !strings.Contains(r.body, "Good evening, Mia Member") {
		t.Fatal("no evening greeting")
	}
	r = s.browser().get("/")
	if !strings.Contains(r.body, "Welcome to Cloverdale Church of God") || !strings.Contains(r.body, `href="/calendar">Browse public events`) ||
		!strings.Contains(r.body, "Request an account") || !strings.Contains(r.body, "/dates/") {
		t.Fatal("signed-out Home lacks the church, its public events, or the way in")
	}
}

// R15: a guest's calendar is the public one, with their own RSVP marked and
// in Just mine; no church-wide date is theirs, and a date made private
// leaves it.
func TestGuestCalendar(t *testing.T) {
	s, admin, event, date := publicDate(t, "")
	g := s.guestGoing(date, "Gina Guest", "gina@example.org")
	group := between(admin.get("/events/"+event).body, `href="/groups/`, `"`)
	later := firstDate(admin.get("/events/" + s.schedule(admin, group, eventForm("Picnic", "2026-12-05", "12:00", "once"))).body)
	r := g.get("/calendar")
	if !strings.Contains(r.body, `class="cal-row cal-going" href="/dates/`+date+`"`) || !strings.Contains(r.body, "Just mine") || strings.Contains(r.body, "Not answered") {
		t.Fatal("a guest's calendar does not mark their RSVP, or marks a date they never answered")
	}
	if !strings.Contains(r.body, `class="cal-row" href="/dates/`+later+`"`) {
		t.Fatal("a guest's calendar lacks a public date coming soon")
	}
	if r := g.get("/calendar?mine=1"); !strings.Contains(r.body, date) || strings.Contains(r.body, later) {
		t.Fatal("a guest's Just mine is not their RSVPs")
	}
	if _, err := s.Writer.Exec(`UPDATE events SET visibility = 'church'`); err != nil {
		t.Fatal(err)
	}
	if r := g.get("/calendar?mine=1"); strings.Contains(r.body, date) {
		t.Fatal("a date made private stays in a guest's calendar")
	}
}
