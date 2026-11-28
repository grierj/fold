package app_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func answerOf(s *site, email string) string {
	var st string
	s.Writer.QueryRow(`SELECT a.status FROM answers a JOIN people p ON p.id = a.person_id WHERE p.email = ?`, email).Scan(&st)
	return st
}

func (s *site) uidOf(email string) string {
	s.t.Helper()
	var uid string
	if err := s.Writer.QueryRow(`SELECT uid FROM people WHERE email = ?`, email).Scan(&uid); err != nil {
		s.t.Fatal(err)
	}
	return uid
}

func htmxPost(b *browser, page, path, target string, form url.Values) resp {
	b.s.t.Helper()
	tok := csrfPattern.FindStringSubmatch(b.get(page).body)[1]
	if form == nil {
		form = url.Values{}
	}
	form.Set("csrf", tok)
	return b.postRaw(path, form, http.Header{"Hx-Request": {"true"}, "Hx-Target": {target}})
}

// DEC-95, DEC-84: a full date says so; Going waits with a place in line;
// those running it remove someone beside their name, in place, and the
// next person moves up and is told by bell and email (R36).
func TestAFullDateAndItsWaitlist(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	group := s.start(admin, "Supper", "public")
	page := "/groups/" + group + "/events/new"
	bad := eventForm("Supper", "2026-12-01", "18:00", "once")
	bad.Set("seats", "0")
	if r := admin.post(page, page, bad); r.status != http.StatusUnprocessableEntity || !strings.Contains(r.body, "Give a number of seats") {
		t.Fatalf("no seats: %d", r.status)
	}
	f := eventForm("Supper", "2026-12-01", "18:00", "once")
	f.Set("seats", "1")
	uid := s.schedule(admin, group, f)
	date := firstDate(admin.get("/events/" + uid).body)
	mia := s.member("Mia Member", "mia@example.org")
	answer(mia, date, "going", "date")
	ned := s.member("Ned Next", "ned@example.org")
	if r := ned.get("/dates/" + date); !strings.Contains(r.body, ">Join the waitlist<") || !strings.Contains(r.body, "The one seat is taken.") {
		t.Fatalf("a full date doesn't say so: %s", between(r.body, `id="answer"`, "</section>"))
	}
	answer(ned, date, "going", "date")
	r := ned.get("/dates/" + date)
	for _, want := range []string{"You&#39;re on the waitlist, number 1.", "1 going · 1 on the waitlist", `aria-pressed="true">On the waitlist`, "You&#39;re on the waitlist</p>"} {
		if !strings.Contains(r.body+ned.get("/events/"+uid).body, want) {
			t.Errorf("the waiting person doesn't see %q", want)
		}
	}
	if strings.Contains(r.body, "/remove") {
		t.Error("a member is offered Remove")
	}
	// The line's order is for those running it; others see how many wait.
	cal := s.member("Cal Coming", "cal@example.org")
	if r := cal.get("/dates/" + date); !strings.Contains(r.body, "1 on the waitlist") || !strings.Contains(r.body, "Mia Member") ||
		strings.Contains(r.body, "Ned Next") || strings.Contains(r.body, "On the waitlist, in order") {
		t.Errorf("someone allowed names sees the waitlist's order: %s", between(r.body, `id="answer"`, "</section>"))
	}
	remove := "/dates/" + date + "/people/" + s.uidOf("mia@example.org") + "/remove"
	if r := ned.post("/dates/"+date, remove, nil); r.status != http.StatusNotFound {
		t.Fatalf("a member removed someone: %d", r.status)
	}
	if r := admin.get("/dates/" + date); !strings.Contains(r.body, `action="`+remove+`"`) || !strings.Contains(r.body, "On the waitlist, in order") {
		t.Fatal("those running it see no Remove beside the name, or no waitlist")
	}
	h := htmxPost(admin, "/dates/"+date, remove, "answer", nil)
	if h.status != http.StatusOK || strings.Contains(h.body, "<html") || !strings.Contains(h.body, "Mia Member is off this date.") ||
		!strings.Contains(h.body, "1 going") || strings.Contains(h.body, "on the waitlist") {
		t.Fatalf("remove in place: %d %s", h.status, h.body)
	}
	if r := ned.get("/notices"); !strings.Contains(r.body, "A seat opened, so you&#39;re going to “Supper” on Tue, Dec 1.") {
		t.Fatalf("the person moved up isn't told: %s", between(r.body, "<main", "</main>"))
	}
	s.work()
	if m, ok := s.outbox.LatestTo("ned@example.org"); !ok || !strings.Contains(m.Text, "A seat opened") {
		t.Fatal("the person moved up isn't emailed (R36)")
	}
	if n := s.Count(t, `SELECT count(*) FROM audit_log WHERE action = 'answer.remove'`); n != 1 {
		t.Fatalf("%d audit rows for one removal", n)
	}
	// htmx swaps nothing on an error status, so a refusal answers 200 and says why.
	again := htmxPost(admin, "/dates/"+date, remove, "answer", nil)
	if again.status != http.StatusOK || !strings.Contains(again.body, "They were already off it.") {
		t.Fatalf("removing twice: %d", again.status)
	}
	s.Clock.Set(time.Date(2026, 12, 2, 3, 0, 0, 0, time.UTC))
	begun := htmxPost(admin, "/dates/"+date, "/dates/"+date+"/people/"+s.uidOf("ned@example.org")+"/remove", "answer", nil)
	if begun.status != http.StatusOK || !strings.Contains(begun.body, "That date has begun") || answerOf(s, "ned@example.org") != "going" {
		t.Fatalf("removing from a date that has begun: %d %s", begun.status, begun.body)
	}
	// Clearing the limit means no limit (HAZARDS 6).
	edit := "/events/" + uid + "/edit"
	cleared := editForm(eventForm("Supper", "2026-12-01", "18:00", "once"), "1")
	cleared.Set("seats", "")
	if r := admin.post(edit, edit, cleared); r.status != http.StatusSeeOther {
		t.Fatalf("clearing seats: %d", r.status)
	}
	if n := s.Count(t, `SELECT count(*) FROM events WHERE seats IS NULL`); n != 1 {
		t.Fatal("a cleared seat limit kept its old value")
	}
}

// DEC-175: the delete and cancel confirmations say how many people will be
// told, and they are.
func TestDeleteAndCancelSayHowMany(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	group := s.start(admin, "Supper", "public")
	uid := s.schedule(admin, group, eventForm("Supper", "2026-12-01", "18:00", "weekly"))
	date := firstDate(admin.get("/events/" + uid).body)
	mia := s.member("Mia Member", "mia@example.org")
	answer(mia, date, "going", "date")
	answer(admin, date, "going", "date")
	if r := admin.get("/dates/" + date); !strings.Contains(r.body, "1 other person signed up will be told.") {
		t.Fatal("the cancel confirmation has no count")
	}
	if r := admin.get("/events/" + uid); !strings.Contains(r.body, "1 other person signed up will be told.") {
		t.Fatal("the delete confirmation has no count")
	}
	if r := admin.get("/groups/" + group); !strings.Contains(r.body, "So will 1 other person signed up to its dates.") {
		t.Fatal("the group delete confirmation has no count (DEC-177)")
	}
	admin.post("/events/"+uid, "/events/"+uid+"/delete", nil)
	if r := mia.get("/notices"); !strings.Contains(r.body, "“Supper” was deleted, so its dates are off, starting Tue, Dec 1.") {
		t.Fatalf("mia isn't told: %s", between(r.body, "<main", "</main>"))
	}
	if r := admin.get("/notices"); strings.Contains(r.body, "was deleted") {
		t.Fatal("the person who deleted it was told")
	}
	s.work()
	if m, ok := s.outbox.LatestTo("mia@example.org"); !ok || !strings.Contains(m.Text, "was deleted") {
		t.Fatal("a Going isn't emailed when its event is deleted (R36)")
	}
}

// R3: making an event more private asks first, lists who signed up would
// lose sight of it with Remove beside each, swapped in place, and saves
// every field through the confirmation; the drop confirmation carries the
// seat limit and says how many people it tells.
func TestMorePrivateAsksWithTheList(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	group := s.start(admin, "Supper", "invite_only")
	f := eventForm("Supper", "2026-12-01", "18:00", "weekly")
	f.Set("seats", "4")
	uid := s.schedule(admin, group, f)
	date := firstDate(admin.get("/events/" + uid).body)
	ned := s.member("Ned Outsider", "ned@example.org")
	answer(ned, date, "going", "date")
	edit := "/events/" + uid + "/edit"
	narrowed := editForm(eventForm("Supper", "2026-12-01", "18:00", "weekly"), "1")
	narrowed.Set("visibility", "invite_only")
	narrowed.Set("seats", "4")
	r := admin.post(edit, edit, narrowed)
	ned404 := "/events/" + uid + "/people/" + s.uidOf("ned@example.org") + "/remove"
	if r.status != http.StatusOK || !strings.Contains(r.body, "Nobody is removed.") || !strings.Contains(r.body, "Ned Outsider") ||
		!strings.Contains(r.body, `action="`+ned404+`"`) || !strings.Contains(r.body, `hx-target="#unseen-`) {
		t.Fatalf("no R3 list: %d %s", r.status, between(r.body, "<main", "</main>"))
	}
	if s.Count(t, `SELECT count(*) FROM events WHERE visibility = 'invite_only'`) != 0 {
		t.Fatal("saved before asking")
	}
	h := htmxPost(admin, "/events/"+uid, ned404, "unseen-"+s.uidOf("ned@example.org"), nil)
	if h.status != http.StatusOK || !strings.Contains(h.body, "Ned Outsider is off 1 date.") || !strings.Contains(h.body, "autofocus") {
		t.Fatalf("remove in R3's list: %d %s", h.status, h.body)
	}
	narrowed.Set("private", "yes")
	if r := admin.post(edit, edit, narrowed); r.status != http.StatusSeeOther {
		t.Fatalf("confirming: %d", r.status)
	}
	var about string
	s.Writer.QueryRow(`SELECT about FROM events WHERE uid = ?`, uid).Scan(&about)
	if about != "Bring a Bible.\nCoffee at 6:45." || s.Count(t, `SELECT count(*) FROM events WHERE visibility = 'invite_only' AND seats = 4`) != 1 {
		t.Fatalf("fields lost through the confirmation: %q", about)
	}
	// Dropping dates asks with the people count and keeps the seat limit.
	mia := s.member("Mia Member", "mia@example.org")
	s.Writer.Exec(`INSERT INTO memberships (group_id, person_id, role, state, created_at, updated_at)
		SELECT g.id, p.id, 'member', 'active', 0, 0 FROM groups g, people p WHERE p.email = 'mia@example.org'`)
	answer(mia, firstDate(admin.get("/events/"+uid).body), "going", "all")
	moved := editForm(eventForm("Supper", "2026-12-02", "18:00", "weekly"), "2")
	moved.Set("visibility", "invite_only")
	moved.Set("seats", "4")
	r = admin.post(edit, edit, moved)
	if !strings.Contains(r.body, "1 other person signed up will be told.") || !strings.Contains(r.body, `name="seats" value="4"`) {
		t.Fatalf("the drop confirmation: %s", between(r.body, "<main", "</main>"))
	}
	moved.Set("confirmed", "yes")
	admin.post(edit, edit, moved)
	if s.Count(t, `SELECT count(*) FROM events WHERE seats = 4`) != 1 {
		t.Fatal("confirming the drop cleared the seat limit")
	}
	if r := mia.get("/notices"); !strings.Contains(r.body, "moved to new times") {
		t.Fatalf("mia isn't told the dates moved: %s", between(r.body, "<main", "</main>"))
	}
}

// One edit that drops dates and makes the event more private asks each
// question in turn, each page carrying the other's answer, and saves every
// field once both are answered.
func TestDropAndNarrowAskEachInTurn(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	group := s.start(admin, "Supper", "invite_only")
	f := eventForm("Supper", "2026-12-01", "18:00", "weekly")
	f.Set("seats", "4")
	uid := s.schedule(admin, group, f)
	ned := s.member("Ned Outsider", "ned@example.org")
	answer(ned, firstDate(admin.get("/events/"+uid).body), "going", "all")
	edit := "/events/" + uid + "/edit"
	both := editForm(eventForm("Supper", "2026-12-02", "18:00", "weekly"), "1")
	both.Set("visibility", "invite_only")
	both.Set("seats", "4")
	r := admin.post(edit, edit, both)
	if !strings.Contains(r.body, "Save and cancel") || !strings.Contains(r.body, `name="private" value=""`) {
		t.Fatalf("the first question isn't the drop: %s", between(r.body, "<main", "</main>"))
	}
	both.Set("confirmed", "yes")
	r = admin.post(edit, edit, both)
	if !strings.Contains(r.body, "Nobody is removed.") || !strings.Contains(r.body, `name="confirmed" value="yes"`) || !strings.Contains(r.body, "Ned Outsider") {
		t.Fatalf("the second question doesn't carry the first's answer: %s", between(r.body, "<main", "</main>"))
	}
	both.Set("private", "yes")
	if r := admin.post(edit, edit, both); r.status != http.StatusSeeOther {
		t.Fatalf("both answered: %d", r.status)
	}
	if s.Count(t, `SELECT count(*) FROM events WHERE visibility = 'invite_only' AND seats = 4 AND starts_local = '2026-12-02T18:00'`) != 1 {
		t.Fatal("the edit didn't save whole")
	}
}
