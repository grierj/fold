package app_test

import (
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// The fake clock starts on Wed 25 Nov 2026 in Boise, the church's zone.

func eventForm(title, day, hm, repeat string) url.Values {
	return url.Values{"title": {title}, "about": {"Bring a Bible.\nCoffee at 6:45."}, "date": {day}, "time": {hm},
		"hours": {"1"}, "minutes": {"30"}, "repeat": {repeat}, "place": {"Room 4 & the hall"}, "visibility": {"public"}}
}

// schedule makes an event for group as b and returns its uid.
func (s *site) schedule(b *browser, group string, form url.Values) string {
	s.t.Helper()
	page := "/groups/" + group + "/events/new"
	r := b.post(page, page, form)
	loc := r.header.Get("Location")
	if !strings.HasPrefix(loc, "/events/") {
		s.t.Fatalf("schedule: %d %s %s", r.status, loc, between(r.body, "<main", "</main>"))
	}
	return strings.TrimPrefix(strings.SplitN(loc, "?", 2)[0], "/events/")
}

var dateLink = `href="/dates/`

// firstDate is the uid of the first date linked on a page.
func firstDate(body string) string {
	i := strings.Index(body, dateLink)
	if i < 0 {
		return ""
	}
	rest := body[i+len(dateLink):]
	return rest[:strings.IndexByte(rest, '"')]
}

func TestScheduleAWeeklyEvent(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	group := s.start(admin, "Tuesday Study", "public")
	if r := admin.get("/groups/" + group); !strings.Contains(r.body, "Nothing is scheduled yet") || !strings.Contains(r.body, "/events/new") {
		t.Fatal("the group page offers no way to schedule")
	}
	form := eventForm("Bible Study", "2026-12-01", "19:00", "weekly")
	uid := s.schedule(admin, group, form)
	r := admin.get("/events/" + uid + "?done=created")
	for _, want := range []string{"Every Tuesday, 7:00 to 8:30 pm", "Your event is scheduled.", "Tue 1 Dec, 7:00 to 8:30 pm",
		"See all dates", "Room 4 &amp; the hall", "https://www.google.com/maps/search/?api=1&amp;query=Room&#43;4&#43;%26&#43;the&#43;hall", "Tuesday Study"} {
		if !strings.Contains(r.body, want) {
			t.Errorf("the event page lacks %q", want)
		}
	}
	if strings.Contains(r.body, "This event keeps") {
		t.Error("a zone line when the zones agree")
	}
	g := s.browser().get("/groups/" + group)
	if !strings.Contains(g.body, "Bible Study") || !strings.Contains(g.body, "Tue 1 Dec, 7:00 to 8:30 pm") {
		t.Fatal("the group page doesn't show what's coming up")
	}
	d := s.browser().get("/dates/" + firstDate(g.body))
	if d.status != 200 || !strings.Contains(d.body, "Tuesday 1 December 2026") || !strings.Contains(d.body, "7:00 to 8:30 pm") || !strings.Contains(d.body, "/events/"+uid) {
		t.Fatalf("date page: %d", d.status)
	}
	if n := s.Count(t, `SELECT count(*) FROM dates`); n < 75 || n > 80 {
		t.Fatalf("%d dates stored", n)
	}
	var about string
	s.Writer.QueryRow(`SELECT about FROM events`).Scan(&about)
	if about != "Bring a Bible.\nCoffee at 6:45." {
		t.Fatalf("about: %q", about)
	}
}

// Check A: an invite-only group's event answers like a missing page to
// someone outside it, signed in or not, on every route, GET and POST.
func TestInviteOnlyEventIsMissingToOutsiders(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	group := s.start(admin, "Elders Prayer", "invite_only")
	form := eventForm("Elders Prayer Night", "2026-12-02", "19:00", "weekly")
	form.Set("visibility", "invite_only")
	uid := s.schedule(admin, group, form)
	date := firstDate(admin.get("/events/" + uid).body)
	if date == "" {
		t.Fatal("no dates")
	}
	outsider := s.member("Oscar Other", "oscar@example.org")
	stranger := s.browser()
	const fake = "nosuchthing0000000000000000"
	for _, b := range []*browser{outsider, stranger} {
		for _, p := range [][2]string{{"/events/" + uid, "/events/" + fake}, {"/events/" + uid + "/edit", "/events/" + fake + "/edit"},
			{"/events/" + uid + "?all=1", "/events/" + fake + "?all=1"}, {"/dates/" + date, "/dates/" + fake},
			{"/groups/" + group + "/events/new", "/groups/" + fake + "/events/new"}} {
			a, z := b.get(p[0]), b.get(p[1])
			if a.status != http.StatusNotFound || a.status != z.status || noisePattern.ReplaceAllString(a.body, "") != noisePattern.ReplaceAllString(z.body, "") {
				t.Errorf("GET %s answers differently from a missing one: %d", p[0], a.status)
			}
		}
		for _, p := range [][2]string{{"/events/" + uid + "/edit", "/events/" + fake + "/edit"}, {"/events/" + uid + "/delete", "/events/" + fake + "/delete"},
			{"/groups/" + group + "/events/new", "/groups/" + fake + "/events/new"}} {
			edit := eventForm("Taken", "2026-12-02", "20:00", "weekly")
			edit.Set("version", "1")
			a := b.post("/signin", p[0], edit)
			z := b.post("/signin", p[1], eventForm("Taken", "2026-12-02", "20:00", "weekly"))
			if a.status != http.StatusNotFound || a.status != z.status || noisePattern.ReplaceAllString(a.body, "") != noisePattern.ReplaceAllString(z.body, "") {
				t.Errorf("POST %s: %d, missing %d", p[0], a.status, z.status)
			}
		}
		if r := b.get("/groups/" + group); strings.Contains(r.body, "Prayer Night") {
			t.Error("the group page leaks the event")
		}
	}
	if s.Count(t, `SELECT count(*) FROM events WHERE title = 'Taken' OR status = 'deleted'`) != 0 || s.Count(t, `SELECT count(*) FROM events`) != 1 {
		t.Fatal("an outsider changed the event")
	}
}

// An invite-only event of a public group is its members' only.
func TestInviteOnlyEventOfAPublicGroup(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	group := s.start(admin, "Open Choir", "public")
	form := eventForm("Section rehearsal", "2026-12-02", "19:00", "weekly")
	form.Set("visibility", "invite_only")
	uid := s.schedule(admin, group, form)
	mia := s.member("Mia Member", "mia@example.org")
	if r := mia.get("/events/" + uid); r.status != http.StatusNotFound {
		t.Fatal("a non-member sees it")
	}
	if r := mia.get("/groups/" + group); strings.Contains(r.body, "Section rehearsal") {
		t.Fatal("the group page lists it to a non-member")
	}
	mia.post("/groups/"+group, "/groups/"+group+"/join", nil)
	if r := mia.get("/events/" + uid); r.status != http.StatusOK {
		t.Fatal("a member can't see it")
	}
}

// DEC-48: a public event stays public after its group closes, and then the
// event's pages never show the group's name, link or id.
func TestEventWiderThanItsGroupHidesTheGroup(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	group := s.start(admin, "Secret Society", "public")
	uid := s.schedule(admin, group, eventForm("Open House", "2026-12-02", "19:00", "once"))
	date := firstDate(admin.get("/events/" + uid).body)
	edit := groupForm("Secret Society", "", "invite_only")
	edit.Set("version", "1")
	edit.Set("confirmed", "yes")
	admin.post("/groups/"+group+"/edit", "/groups/"+group+"/edit", edit)
	stranger := s.browser()
	for _, p := range []string{"/events/" + uid, "/dates/" + date} {
		r := stranger.get(p)
		if r.status != http.StatusOK {
			t.Fatalf("%s: %d; the event stays public (DEC-48)", p, r.status)
		}
		if strings.Contains(r.body, "Secret Society") || strings.Contains(r.body, group) {
			t.Errorf("%s shows the invite-only group", p)
		}
	}
}

// An archived group's events are hidden from its members and frozen for
// its leaders.
func TestArchivedGroupHidesItsEvents(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	mel := s.member("Mel Leader", "mel@example.org")
	group := s.start(mel, "Tuesday Study", "church")
	admin.post("/admin/groups", "/admin/groups/"+group+"/approve", nil)
	form := eventForm("Study", "2026-12-01", "19:00", "weekly")
	form.Set("visibility", "church")
	uid := s.schedule(mel, group, form)
	date := firstDate(mel.get("/events/" + uid).body)
	mia := s.member("Mia Member", "mia@example.org")
	mia.post("/groups/"+group, "/groups/"+group+"/join", nil)
	if r := mia.get("/dates/" + date); r.status != http.StatusOK {
		t.Fatal("a member can't see the date")
	}
	mel.post("/groups/"+group, "/groups/"+group+"/archive", nil)
	for _, p := range []string{"/events/" + uid, "/dates/" + date} {
		if r := mia.get(p); r.status != http.StatusNotFound {
			t.Errorf("%s shows to a member of an archived group", p)
		}
	}
	r := mel.get("/events/" + uid)
	if r.status != http.StatusOK || !strings.Contains(r.body, "This group isn&#39;t active") || strings.Contains(r.body, "Edit the event") {
		t.Fatalf("the leader's view of an archived group's event: %d", r.status)
	}
	if r := mel.get("/events/" + uid + "/edit"); r.status != http.StatusNotFound {
		t.Fatal("an archived group's event can be edited")
	}
}

func editForm(form url.Values, version string) url.Values {
	form.Set("version", version)
	return form
}

// DEC-91 and HAZARDS 7: an edit that drops dates says how many and asks
// before saving; confirming saves, carrying every field.
func TestDroppedDatesAskFirst(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	group := s.start(admin, "Study", "public")
	uid := s.schedule(admin, group, eventForm("Study", "2026-12-01", "19:00", "weekly"))
	edit := "/events/" + uid + "/edit"
	tuesdays := s.Count(t, `SELECT count(*) FROM dates`) // all still to come
	tuesday := firstDate(admin.get("/events/" + uid).body)
	r := admin.post(edit, edit, editForm(eventForm("Study", "2026-12-02", "19:00", "weekly"), "1"))
	if r.status != http.StatusOK || !strings.Contains(r.body, "Cancel "+strconv.Itoa(tuesdays)+" dates?") || !strings.Contains(r.body, `name="confirmed" value="yes"`) {
		t.Fatalf("no question before dropping dates: %d %s", r.status, between(r.body, "<h1", "</h1>"))
	}
	if s.Count(t, `SELECT version FROM events`) != 1 || s.Count(t, `SELECT count(*) FROM dates WHERE status = 'cancelled'`) != 0 {
		t.Fatal("asking saved something")
	}
	confirm := url.Values{}
	for _, name := range []string{"version", "timing", "title", "about", "place", "date", "time", "hours", "minutes", "repeat", "month_week", "until", "visibility", "confirmed"} {
		confirm.Set(name, html.UnescapeString(between(r.body, `name="`+name+`" value="`, `"`)))
	}
	if r := admin.post(edit, edit, confirm); r.status != http.StatusSeeOther {
		t.Fatalf("confirmed: %d %s", r.status, between(r.body, "<main", "</main>"))
	}
	if n := s.Count(t, `SELECT count(*) FROM dates WHERE status = 'cancelled' AND cancelled_by = 'series'`); n != tuesdays {
		t.Fatalf("%d dates cancelled", n)
	}
	if r := admin.get("/events/" + uid); !strings.Contains(r.body, "Every Wednesday") || !strings.Contains(r.body, "Wed 2 Dec") || strings.Contains(r.body, "Tue 1 Dec") {
		t.Fatal("the event doesn't show its new day")
	}
	if r := s.browser().get("/dates/" + tuesday); r.status != http.StatusOK || !strings.Contains(r.body, "This date is cancelled") {
		t.Fatalf("a dropped date's page: %d", r.status)
	}
	var about, place string
	s.Writer.QueryRow(`SELECT about, place FROM events`).Scan(&about, &place)
	if about != "Bring a Bible.\nCoffee at 6:45." || place != "Room 4 & the hall" {
		t.Fatalf("after the confirmation: %q %q", about, place)
	}
}

// The confirmation's hidden fields keep a description's line breaks.
func TestDropConfirmationKeepsLineBreaks(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	group := s.start(admin, "Study", "public")
	uid := s.schedule(admin, group, eventForm("Study", "2026-12-01", "19:00", "weekly"))
	edit := "/events/" + uid + "/edit"
	r := admin.post(edit, edit, editForm(eventForm("Study", "2026-12-02", "19:00", "weekly"), "1"))
	if !strings.Contains(r.body, `name="about" value="Bring a Bible.`+"\n"+`Coffee at 6:45."`) {
		t.Fatal("the line break is lost in the confirmation")
	}
}

// P16: a stale form saves nothing and shows what the event says now.
func TestEventEditConflict(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	group := s.start(admin, "Study", "public")
	uid := s.schedule(admin, group, eventForm("Study", "2026-12-01", "19:00", "weekly"))
	edit := "/events/" + uid + "/edit"
	stale := admin.get(edit)
	tok := csrfPattern.FindStringSubmatch(stale.body)[1]
	admin.post(edit, edit, editForm(eventForm("Bible Study", "2026-12-01", "19:00", "weekly"), "1"))
	form := editForm(eventForm("Book Club", "2026-12-01", "20:00", "weekly"), "1")
	form.Set("csrf", tok)
	r := admin.postRaw(edit, form, nil)
	if r.status != http.StatusConflict || !strings.Contains(r.body, "could not be saved") || !strings.Contains(r.body, `value="Bible Study"`) {
		t.Fatalf("conflict: %d", r.status)
	}
	if s.Count(t, `SELECT count(*) FROM events WHERE title = 'Book Club'`) != 0 || s.Count(t, `SELECT count(*) FROM dates WHERE starts_local LIKE '%T20:00'`) != 0 {
		t.Fatal("a stale form saved")
	}
}

// HAZARDS 6: an emptied optional field is cleared.
func TestClearingOptionalEventFields(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	group := s.start(admin, "Study", "public")
	form := eventForm("Study", "2026-12-01", "19:00", "weekly")
	form.Set("until", "2026-12-15")
	uid := s.schedule(admin, group, form)
	if n := s.Count(t, `SELECT count(*) FROM dates`); n != 3 {
		t.Fatalf("%d dates until the 15th", n)
	}
	edit := "/events/" + uid + "/edit"
	cleared := editForm(eventForm("Study", "2026-12-01", "19:00", "weekly"), "1")
	cleared.Set("place", "")
	cleared.Set("about", "")
	if r := admin.post(edit, edit, cleared); r.status != http.StatusSeeOther {
		t.Fatalf("clear: %d %s", r.status, between(r.body, "<main", "</main>"))
	}
	if s.Count(t, `SELECT count(*) FROM events WHERE until IS NULL AND place = '' AND about = ''`) != 1 {
		t.Fatal("an emptied field was kept")
	}
	if n := s.Count(t, `SELECT count(*) FROM dates WHERE status = 'scheduled'`); n < 75 {
		t.Fatalf("clearing the last date left %d dates", n)
	}
	if r := admin.get("/events/" + uid); strings.Contains(r.body, "Open in maps") {
		t.Fatal("an empty place still has a maps link")
	}
}

// Visibility is checked by the command: a leader cannot open an event wider
// than its group in 1a, whatever the form offered.
func TestEventCannotBeWiderThanItsGroup(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	group := s.start(admin, "Men's Breakfast", "church")
	if r := admin.get("/groups/" + group + "/events/new"); strings.Contains(r.body, `value="public"`) {
		t.Fatal("the form offers wider than the group")
	}
	page := "/groups/" + group + "/events/new"
	if r := admin.post(page, page, eventForm("Breakfast", "2026-12-05", "08:00", "weekly")); r.status != http.StatusNotFound {
		t.Fatalf("a wider event: %d", r.status)
	}
	if s.Count(t, `SELECT count(*) FROM events`) != 0 {
		t.Fatal("it was saved")
	}
}

// R34: the form warns once when a date falls in a skipped or doubled hour;
// the date page says when its time moved.
func TestDaylightSavingWarning(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	group := s.start(admin, "Night Owls", "public")
	page := "/groups/" + group + "/events/new"
	form := eventForm("Vigil", "2027-03-07", "02:30", "weekly")
	form.Set("until", "2027-03-21")
	r := admin.post(page, page, form)
	if r.status != http.StatusOK || !strings.Contains(r.body, "The clocks change on a night this meets") || !strings.Contains(r.body, "Sun 14 Mar 2027 the clocks go forward, so that date starts at 3:30 am") {
		t.Fatalf("no warning: %d %s", r.status, between(r.body, "<main", "</main>"))
	}
	form.Set("timing", between(r.body, `name="timing" value="`, `"`))
	uid := s.schedule(admin, group, form)
	var date string
	s.Writer.QueryRow(`SELECT uid FROM dates WHERE day = '2027-03-14' AND moved = 1 AND starts_local = '2027-03-14T03:30'`).Scan(&date)
	if date == "" {
		t.Fatal("the date did not move to 3:30")
	}
	if r := admin.get("/dates/" + date); !strings.Contains(r.body, "2:30 am doesn&#39;t happen, so this date starts at 3:30 am") {
		t.Fatalf("the date page doesn't say it moved: %s", between(r.body, "<main", "</main>"))
	}
	// A changed time is warned about again: 1:30 am on the fall night.
	edit := "/events/" + uid + "/edit"
	fall := editForm(eventForm("Vigil", "2027-11-07", "01:30", "weekly"), "1")
	fall.Set("until", "2027-11-07")
	if r := admin.post(edit, edit, fall); !strings.Contains(r.body, "the clocks go back and 1:30 am happens twice") {
		t.Fatalf("no warning for the fall night: %s", between(r.body, "<main", "</main>"))
	}
}

// DEC-57: times show in the viewer's zone, with one quiet line naming the
// event's clock when it differs.
func TestTimesInTheViewersZone(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	group := s.start(admin, "Study", "public")
	uid := s.schedule(admin, group, eventForm("Study", "2026-12-01", "19:00", "weekly"))
	s.Writer.Exec(`UPDATE people SET zone = 'America/Chicago'`)
	r := admin.get("/events/" + uid)
	if !strings.Contains(r.body, "This event keeps Boise time. Times here are shown in Chicago time.") || !strings.Contains(r.body, "Tue 1 Dec, 8:00 to 9:30 pm") {
		t.Fatalf("viewer zone: %s", between(r.body, "<main", "</main>"))
	}
	if !strings.Contains(r.body, "Every Tuesday, 7:00 to 8:30 pm") {
		t.Fatal("the pattern is not on the event's own clock")
	}
	if r := admin.get("/events/" + uid + "/edit"); !strings.Contains(r.body, "Times are in Boise time (America/Boise).") || !strings.Contains(r.body, `value="19:00"`) {
		t.Fatal("the edit form does not use and name the event's zone")
	}
	if r := s.browser().get("/events/" + uid); strings.Contains(r.body, "This event keeps") {
		t.Fatal("a visitor, in the church's zone, got a zone line")
	}
}

func TestDeleteEvent(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	group := s.start(admin, "Study", "public")
	uid := s.schedule(admin, group, eventForm("Study", "2026-12-01", "19:00", "weekly"))
	date := firstDate(admin.get("/events/" + uid).body)
	mia := s.member("Mia Member", "mia@example.org")
	mia.post("/groups/"+group, "/groups/"+group+"/join", nil)
	if r := mia.post("/events/"+uid, "/events/"+uid+"/delete", nil); r.status != http.StatusNotFound {
		t.Fatal("a member deleted an event")
	}
	r := admin.post("/events/"+uid, "/events/"+uid+"/delete", nil)
	if loc := r.header.Get("Location"); loc != "/groups/"+group+"?done=event-deleted" {
		t.Fatalf("after delete: %s", loc)
	}
	for _, p := range []string{"/events/" + uid, "/dates/" + date} {
		if r := admin.get(p); r.status != http.StatusNotFound {
			t.Errorf("%s still shows", p)
		}
	}
	if r := admin.get("/groups/" + group + "?done=event-deleted"); strings.Contains(r.body, "Tue 1 Dec") || !strings.Contains(r.body, "The event is deleted.") {
		t.Fatal("the group page still lists its dates")
	}
	if s.Count(t, `SELECT count(*) FROM audit_log WHERE action = 'event.delete'`) != 1 {
		t.Fatal("no audit row")
	}
}

func TestEventFormChecksEveryField(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	group := s.start(admin, "Study", "public")
	page := "/groups/" + group + "/events/new"
	cases := []struct {
		field, value, want string
	}{
		{"title", "", "Give the event a title."},
		{"title", strings.Repeat("x", 101), "That title is too long"},
		{"place", strings.Repeat("x", 201), "Keep it under 200 characters"},
		{"date", "2026-02-30", "Choose the date of the first one."},
		{"date", "2029-01-01", "within the last year or the next two years"},
		{"time", "7pm", "Choose the time it starts"},
		{"hours", "200", "Choose how long it lasts"},
		{"repeat", "FREQ=HOURLY", "Choose how often it happens."},
		{"until", "2026-11-01", "The last date can&#39;t be before the first."},
	}
	for _, c := range cases {
		form := eventForm("Study", "2026-12-01", "19:00", "weekly")
		form.Set(c.field, c.value)
		r := admin.post(page, page, form)
		if r.status != http.StatusUnprocessableEntity || !strings.Contains(r.body, c.want) {
			t.Errorf("%s=%q: %d, want %q", c.field, c.value, r.status, c.want)
		}
	}
	// A monthly repeat on a 5th Tuesday offers only the last.
	form := eventForm("Study", "2026-12-29", "19:00", "monthly")
	form.Set("month_week", "4")
	if r := admin.post(page, page, form); !strings.Contains(r.body, "Not every month has a 5th Tuesday, so choose Last.") {
		t.Errorf("5th Tuesday: %s", between(r.body, "<main", "</main>"))
	}
	form.Set("month_week", "-1")
	s.schedule(admin, group, form)
	if s.Count(t, `SELECT count(*) FROM events WHERE month_week = -1`) != 1 {
		t.Fatal("the last Tuesday was not saved")
	}
}
