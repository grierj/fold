package app_test

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func answer(b *browser, date, status, scope string) resp {
	page := "/dates/" + date
	return b.post(page, page+"/answer", url.Values{"answer": {status}, "scope": {scope}})
}

// DEC-63, DEC-92: a member answers a date; the line that stays says what
// happened; counts come first and names follow; their answer shows on the
// group's and the event's lists.
func TestAnswerADate(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	group := s.start(admin, "Study", "church")
	uid := s.schedule(admin, group, func() url.Values {
		f := eventForm("Study", "2026-12-01", "19:00", "weekly")
		f.Set("visibility", "church")
		return f
	}())
	date := firstDate(admin.get("/events/" + uid).body)
	mia := s.member("Mia Member", "mia@example.org")
	mia.post("/groups/"+group, "/groups/"+group+"/join", nil)
	r := answer(mia, date, "going", "date")
	if r.header.Get("Location") != "/dates/"+date+"?done=going" {
		t.Fatalf("answer: %d %s", r.status, r.header.Get("Location"))
	}
	page := mia.get(r.header.Get("Location"))
	for _, want := range []string{"You&#39;re going. See you there!", "1 going", "Mia Member", `aria-pressed="true">Going`, "New dates added later won't be answered for you."} {
		if !strings.Contains(page.body, want) {
			t.Errorf("the date page lacks %q: %s", want, between(page.body, `id="answer"`, "</section>"))
		}
	}
	if r := mia.get("/groups/" + group); !strings.Contains(r.body, "You&#39;re going") {
		t.Error("the group page doesn't show the person's answer")
	}
	if r := mia.get("/events/" + uid); !strings.Contains(r.body, "You&#39;re going") {
		t.Error("the event page doesn't show the person's answer")
	}
	// With htmx the answer block alone comes back, updated.
	tok := csrfPattern.FindStringSubmatch(page.body)[1]
	h := mia.postRaw("/dates/"+date+"/answer", url.Values{"answer": {"maybe"}, "scope": {"date"}, "csrf": {tok}}, http.Header{"Hx-Request": {"true"}})
	if h.status != http.StatusOK || strings.Contains(h.body, "<html") || !strings.Contains(h.body, `id="answer"`) ||
		!strings.Contains(h.body, "You said maybe. Change it anytime.") || !strings.Contains(h.body, "1 maybe") {
		t.Fatalf("htmx answer: %d %s", h.status, h.body)
	}
	// This date and the ones after it, in one write.
	r = answer(mia, date, "going", "following")
	if r.header.Get("Location") != "/dates/"+date+"?done=going" {
		t.Fatalf("series: %s", r.header.Get("Location"))
	}
	n := s.Count(t, `SELECT count(*) FROM answers WHERE status = 'going'`)
	if n < 70 || !strings.Contains(mia.get(r.header.Get("Location")).body, "You&#39;re going. See you there!") {
		t.Fatalf("%d dates answered", n)
	}
	// With htmx the line counts the dates the answer itself wrote.
	tok = csrfPattern.FindStringSubmatch(page.body)[1]
	h = mia.postRaw("/dates/"+date+"/answer", url.Values{"answer": {"maybe"}, "scope": {"following"}, "csrf": {tok}}, http.Header{"Hx-Request": {"true"}})
	if !strings.Contains(h.body, "You said maybe to "+strconv.Itoa(n)+" dates.") {
		t.Fatalf("the htmx line doesn't count the dates written: %s", between(h.body, "status-line", "</p>"))
	}
	// Can't go for every date asks first, with the count.
	r = answer(mia, date, "cant", "all")
	if !strings.Contains(r.body, "Say you can't go to ") || s.Count(t, `SELECT count(*) FROM answers WHERE status = 'cant'`) != 0 {
		t.Fatalf("can't go for every date didn't ask: %d", r.status)
	}
	confirm := url.Values{"answer": {"cant"}, "scope": {"all"}, "confirmed": {"yes"}}
	mia.post("/dates/"+date, "/dates/"+date+"/answer", confirm)
	if s.Count(t, `SELECT count(*) FROM answers WHERE status = 'cant'`) < 70 {
		t.Fatal("the confirmed can't go was not saved")
	}
	if r := answer(mia, date, "cancelled", "date"); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("a cancelled answer from a form: %d", r.status)
	}
	if r := s.browser().post("/signin", "/dates/"+date+"/answer", url.Values{"answer": {"going"}}); r.status != http.StatusNotFound {
		t.Fatalf("a stranger on a church-only date: %d", r.status)
	}
}

// DEC-64, HAZARDS 2: only those running the event learn anything about who
// can't go; everyone else gets no count and no name, not a hidden one.
func TestCantGoOnlyForThoseRunningIt(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	group := s.start(admin, "Study", "public")
	uid := s.schedule(admin, group, eventForm("Study", "2026-12-01", "19:00", "once"))
	date := firstDate(admin.get("/events/" + uid).body)
	cara := s.member("Cara Cantgo", "cara@example.org")
	answer(cara, date, "cant", "date")
	mia := s.member("Mia Member", "mia@example.org")
	answer(mia, date, "going", "date")
	for _, b := range []*browser{mia, s.browser()} {
		r := b.get("/dates/" + date)
		if strings.Contains(r.body, "Cara") || strings.Contains(r.body, "can&#39;t go") || strings.Contains(r.body, `font-semibold">Can't go`) {
			t.Error("can't go reached someone not running the event")
		}
	}
	tok := csrfPattern.FindStringSubmatch(mia.get("/dates/" + date).body)[1]
	h := mia.postRaw("/dates/"+date+"/answer", url.Values{"answer": {"going"}, "scope": {"date"}, "csrf": {tok}}, http.Header{"Hx-Request": {"true"}})
	if h.status != http.StatusOK || strings.Contains(h.body, "Cara") || strings.Contains(h.body, "can&#39;t go") || strings.Contains(h.body, `font-semibold">Can't go`) {
		t.Fatal("the htmx fragment carries can't go to a member")
	}
	if r := s.browser().get("/dates/" + date); strings.Contains(r.body, "Mia Member") || !strings.Contains(r.body, "1 going") || !strings.Contains(r.body, "Sign in to answer") {
		t.Error("a visitor sees names, or no count, or no way to answer")
	}
	r := admin.get("/dates/" + date)
	if !strings.Contains(r.body, "1 going · 1 can&#39;t go") || !strings.Contains(r.body, "Cara Cantgo") {
		t.Fatal("the leader doesn't see who can't go")
	}
}

// R41: the video link is for Going, Maybe and those running the event.
func TestVideoLinkOnlyForThoseComing(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	group := s.start(admin, "Study", "public")
	page := "/groups/" + group + "/events/new"
	for _, bad := range []string{"http://meet.example.org/x", "javascript:alert(1)", "https://pat:pw@meet.example.org/x", "meet.example.org/x", "https:///x", "https://meet.example.org/a b"} {
		f := eventForm("Study", "2026-12-01", "19:00", "weekly")
		f.Set("video", bad)
		if r := admin.post(page, page, f); r.status != http.StatusUnprocessableEntity || !strings.Contains(r.body, "starting with https://") {
			t.Errorf("video %q: %d %s", bad, r.status, between(r.body, "field-error", "</p>"))
		}
	}
	f := eventForm("Study", "2026-12-01", "19:00", "weekly")
	f.Set("video", "https://meet.example.org/study?room=4")
	uid := s.schedule(admin, group, f)
	date := firstDate(admin.get("/events/" + uid).body)
	link := "https://meet.example.org/study?room=4"
	if r := admin.get("/dates/" + date); !strings.Contains(r.body, link) {
		t.Fatal("the leader doesn't see the link")
	}
	mia := s.member("Mia Member", "mia@example.org")
	for _, b := range []*browser{mia, s.browser()} {
		if r := b.get("/dates/" + date); strings.Contains(r.body, "meet.example.org") {
			t.Fatal("someone who hasn't answered sees the link")
		}
		if r := b.get("/events/" + uid); strings.Contains(r.body, "meet.example.org") {
			t.Fatal("the event page shows the link to someone not coming")
		}
	}
	answer(mia, date, "maybe", "date")
	if r := mia.get("/dates/" + date); !strings.Contains(r.body, link) {
		t.Fatal("a Maybe doesn't see the link")
	}
	if r := mia.get("/events/" + uid); !strings.Contains(r.body, link) {
		t.Fatal("the event page hides the link from someone coming")
	}
	other := firstDate(strings.Replace(admin.get("/events/"+uid).body, `href="/dates/`+date, "", 1))
	if r := mia.get("/dates/" + other); strings.Contains(r.body, "meet.example.org") {
		t.Fatal("an answer on one date shows the link on another")
	}
	answer(mia, date, "cant", "date")
	if r := mia.get("/dates/" + date); strings.Contains(r.body, "meet.example.org") {
		t.Fatal("a Can't go still sees the link")
	}
	if s.Count(t, `SELECT count(*) FROM audit_log WHERE details LIKE '%meet.example%' OR text LIKE '%meet.example%'`) != 0 {
		t.Fatal("the link is in the audit log")
	}
	// HAZARDS 6: emptied, it is cleared.
	edit := "/events/" + uid + "/edit"
	cleared := editForm(eventForm("Study", "2026-12-01", "19:00", "weekly"), "1")
	admin.post(edit, edit, cleared)
	if s.Count(t, `SELECT count(*) FROM events WHERE video_url = ''`) != 1 {
		t.Fatal("an emptied video link was kept")
	}
}

// An outsider on a date of an invite-only group opened to everyone answers
// that date only, sees counts but no names, and is offered no series answer.
func TestAnsweringAnOpenedDate(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	group := s.start(admin, "Elders Prayer", "invite_only")
	f := eventForm("Prayer night", "2026-12-02", "19:00", "weekly")
	f.Set("visibility", "invite_only")
	uid := s.schedule(admin, group, f)
	date := firstDate(admin.get("/events/" + uid).body)
	admin.post("/dates/"+date+"/open", "/dates/"+date+"/open", url.Values{"visibility": {"public"}})
	answer(admin, date, "going", "date")
	oscar := s.member("Oscar Other", "oscar@example.org")
	r := oscar.get("/dates/" + date)
	if !strings.Contains(r.body, "1 going") || strings.Contains(r.body, "Pat Admin") || strings.Contains(r.body, "Answer for") {
		t.Fatal("the outsider sees names, or the series question")
	}
	answer(oscar, date, "going", "all")
	if n := s.Count(t, `SELECT count(*) FROM answers a JOIN people p ON p.id = a.person_id WHERE p.email = 'oscar@example.org'`); n != 0 {
		t.Fatalf("a forged series answer wrote %d answers", n)
	}
	answer(oscar, date, "going", "date")
	if n := s.Count(t, `SELECT count(*) FROM answers a JOIN people p ON p.id = a.person_id WHERE p.email = 'oscar@example.org'`); n != 1 {
		t.Fatalf("%d answers for the outsider", n)
	}
}

// DEC-74, DEC-38: leaving says how many dates it takes the person off, and
// does; a disabled account's answers stop counting and come back.
func TestLeavingAndDisabledAnswers(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	group := s.start(admin, "Study", "public")
	uid := s.schedule(admin, group, eventForm("Study", "2026-12-01", "19:00", "weekly"))
	date := firstDate(admin.get("/events/" + uid).body)
	mia := s.member("Mia Member", "mia@example.org")
	mia.post("/groups/"+group, "/groups/"+group+"/join", nil)
	answer(mia, date, "going", "date")
	if r := mia.get("/groups/" + group); !strings.Contains(r.body, "You&#39;ve answered 1 date of this group") {
		t.Fatal("leaving doesn't say what it takes the person off")
	}
	s.Writer.Exec(`UPDATE people SET status = 'disabled' WHERE email = 'mia@example.org'`)
	if r := admin.get("/dates/" + date); strings.Contains(r.body, "Mia Member") || !strings.Contains(r.body, "No one has answered yet.") {
		t.Fatal("a disabled account's answer still counts")
	}
	s.Writer.Exec(`UPDATE people SET status = 'active' WHERE email = 'mia@example.org'`)
	if r := admin.get("/dates/" + date); !strings.Contains(r.body, "Mia Member") || !strings.Contains(r.body, "1 going") {
		t.Fatal("turned on again, the answer is not back")
	}
	mia.post("/groups/"+group, "/groups/"+group+"/leave", nil)
	if r := admin.get("/dates/" + date); strings.Contains(r.body, "Mia Member") || strings.Contains(r.body, "1 going") {
		t.Fatal("leaving left the answer")
	}
	var details string
	s.Writer.QueryRow(`SELECT details FROM audit_log WHERE action = 'group.leave'`).Scan(&details)
	if !strings.Contains(details, `"answers":"1"`) {
		t.Fatalf("the leave's audit row: %s", details)
	}
}

// The line after an answer says only what the viewer's own answer bears out.
func TestAnswerStatusComesFromTheAnswer(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	group := s.start(admin, "Study", "public")
	uid := s.schedule(admin, group, eventForm("Study", "2026-12-01", "19:00", "weekly"))
	date := firstDate(admin.get("/events/" + uid).body)
	for _, b := range []*browser{s.browser(), admin} {
		// "restored" on a date that is on matches what is stored, as the
		// line is defined: only a cancelled date refuses it (below).
		for _, crafted := range []string{"?done=going", "?done=going&n=50", "?done=cancelled", "?done=opened"} {
			r := b.get("/dates/" + date + crafted)
			for _, line := range []string{"You&#39;re going", "This date is cancelled.", "This date is back on.", "Saved.", "50 dates"} {
				if strings.Contains(r.body, line) {
					t.Errorf("%s put %q in the status line", crafted, line)
				}
			}
		}
	}
	// Each line shows when what is stored bears it out.
	admin.post("/dates/"+date, "/dates/"+date+"/cancel", nil)
	if r := admin.get("/dates/" + date + "?done=cancelled"); !strings.Contains(r.body, "This date is cancelled.") {
		t.Error("the cancelled line is missing")
	}
	if r := admin.get("/dates/" + date + "?done=restored"); strings.Contains(r.body, "This date is back on.") {
		t.Error("a cancelled date says it is back on")
	}
	admin.post("/dates/"+date, "/dates/"+date+"/restore", nil)
	if r := admin.get("/dates/" + date + "?done=restored"); !strings.Contains(r.body, "This date is back on.") {
		t.Error("the restored line is missing")
	}
}

// DEC-91: a cancelled date hides its answers, and bringing it back brings
// them back.
func TestCancelAndRestoreKeepAnswers(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	group := s.start(admin, "Study", "public")
	uid := s.schedule(admin, group, eventForm("Study", "2026-12-01", "19:00", "weekly"))
	date := firstDate(admin.get("/events/" + uid).body)
	mia := s.member("Mia Member", "mia@example.org")
	answer(mia, date, "going", "date")
	admin.post("/dates/"+date, "/dates/"+date+"/cancel", nil)
	if r := mia.get("/dates/" + date); strings.Contains(r.body, `id="answer"`) || strings.Contains(r.body, "1 going") {
		t.Fatal("a cancelled date still offers answers or counts")
	}
	admin.post("/dates/"+date, "/dates/"+date+"/restore", nil)
	if r := mia.get("/dates/" + date); !strings.Contains(r.body, "1 going") || !strings.Contains(r.body, `aria-pressed="true">Going`) {
		t.Fatal("bringing the date back did not bring back the answer")
	}
}

// R41 on an opened date: an outsider sees the link only once they answer.
func TestVideoOnAnOpenedDate(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	group := s.start(admin, "Elders Prayer", "invite_only")
	f := eventForm("Prayer night", "2026-12-02", "19:00", "weekly")
	f.Set("visibility", "invite_only")
	f.Set("video", "https://meet.example.org/prayer")
	uid := s.schedule(admin, group, f)
	date := firstDate(admin.get("/events/" + uid).body)
	admin.post("/dates/"+date+"/open", "/dates/"+date+"/open", url.Values{"visibility": {"church"}})
	oscar := s.member("Oscar Other", "oscar@example.org")
	if r := oscar.get("/dates/" + date); strings.Contains(r.body, "meet.example.org") {
		t.Fatal("the outsider sees the link before answering")
	}
	answer(oscar, date, "going", "date")
	if r := oscar.get("/dates/" + date); !strings.Contains(r.body, "https://meet.example.org/prayer") {
		t.Fatal("the outsider doesn't see the link after answering Going")
	}
	if r := oscar.get("/events/" + uid); r.status != http.StatusNotFound {
		t.Fatal("the event page shows to the outsider")
	}
}

// DEC-74, HAZARDS 7: the leave confirmation counts only dates still on.
func TestLeaveCountsOnlyDatesStillOn(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	group := s.start(admin, "Study", "public")
	uid := s.schedule(admin, group, eventForm("Study", "2026-12-01", "19:00", "weekly"))
	date := firstDate(admin.get("/events/" + uid).body)
	mia := s.member("Mia Member", "mia@example.org")
	mia.post("/groups/"+group, "/groups/"+group+"/join", nil)
	answer(mia, date, "going", "all")
	edit := "/events/" + uid + "/edit"
	moved := editForm(eventForm("Study", "2026-12-02", "19:00", "weekly"), "1")
	moved.Set("confirmed", "yes")
	admin.post(edit, edit, moved)
	if r := mia.get("/groups/" + group); strings.Contains(r.body, "You&#39;ve answered") {
		t.Fatalf("the leave dialog counts dates that are no longer on: %s", between(r.body, "leave-group", "</dialog>"))
	}
}

// DEC-74, R40 wording: leaving and removal take every answer on dates to
// come off, Can't go included; the confirmation's count and the audit row
// count the same dates; a date restored later comes back without the
// person who left; joining again, they answer again.
func TestLeaveAndRemoveTakeEveryAnswer(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	group := s.start(admin, "Study", "public")
	uid := s.schedule(admin, group, eventForm("Study", "2026-12-01", "19:00", "weekly"))
	body := admin.get("/events/" + uid).body
	first := firstDate(body)
	second := firstDate(strings.Replace(body, `href="/dates/`+first, "", 1))
	mia := s.member("Mia Member", "mia@example.org")
	mia.post("/groups/"+group, "/groups/"+group+"/join", nil)
	answer(mia, first, "cant", "date")
	answer(mia, second, "going", "date")
	admin.post("/dates/"+second, "/dates/"+second+"/cancel", nil)
	if r := mia.get("/groups/" + group); !strings.Contains(r.body, "You&#39;ve answered 1 date of this group") {
		t.Fatalf("the leave count: %s", between(r.body, "leave-group", "</dialog>"))
	}
	mia.post("/groups/"+group, "/groups/"+group+"/leave", nil)
	var details string
	s.Writer.QueryRow(`SELECT details FROM audit_log WHERE action = 'group.leave'`).Scan(&details)
	if !strings.Contains(details, `"answers":"1"`) {
		t.Fatalf("the audit row counts other dates than the confirmation: %s", details)
	}
	if r := admin.get("/dates/" + first); strings.Contains(r.body, "Mia Member") || strings.Contains(r.body, "can&#39;t go") {
		t.Fatal("the leaver is still listed under Can't go")
	}
	admin.post("/dates/"+second, "/dates/"+second+"/restore", nil)
	if r := admin.get("/dates/" + second); strings.Contains(r.body, "Mia Member") || strings.Contains(r.body, "1 going") {
		t.Fatal("the restored date brought the leaver back")
	}
	// Joining again, she answers again (DEC-86).
	mia.post("/groups/"+group, "/groups/"+group+"/join", nil)
	if r := answer(mia, first, "going", "date"); r.header.Get("Location") != "/dates/"+first+"?done=going" {
		t.Fatalf("answering after rejoining: %d", r.status)
	}
	if r := admin.get("/dates/" + first); !strings.Contains(r.body, "Mia Member") || !strings.Contains(r.body, "1 going") {
		t.Fatal("the answer after rejoining doesn't count")
	}
	// A leader's removal takes Can't go too.
	answer(mia, second, "cant", "date")
	admin.post("/groups/"+group+"/members", "/groups/"+group+"/members/"+personUID(s, "mia@example.org")+"/remove", nil)
	if r := admin.get("/dates/" + second); strings.Contains(r.body, "Mia Member") || strings.Contains(r.body, "can&#39;t go") {
		t.Fatal("the removed member is still listed under Can't go")
	}
}
