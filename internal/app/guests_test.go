package app_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"fold/internal/accounts"
	"fold/internal/notices"
	"fold/internal/write"
)

var (
	confirmPattern = regexp.MustCompile(`/l/([A-Za-z0-9_-]{43})`)
	managePattern  = regexp.MustCompile(`/g/([A-Za-z0-9_-]{43})`)
)

func guestForm(name, email string) url.Values {
	return url.Values{"name": {name}, "email": {email}, "adult": {"yes"}}
}

// publicDate is a public group's one-off supper on Tuesday, December 1,
// with an optional seat limit, and the admin's browser.
func publicDate(t *testing.T, seats string) (*site, *browser, string, string) {
	s := newSite(t)
	admin := s.finishSetup()
	group := s.start(admin, "Supper Club", "public")
	f := eventForm("Supper", "2026-12-01", "18:00", "once")
	if seats != "" {
		f.Set("seats", seats)
	}
	uid := s.schedule(admin, group, f)
	return s, admin, uid, firstDate(admin.get("/events/" + uid).body)
}

func (b *browser) rsvp(date, name, email string) resp {
	b.s.t.Helper()
	return b.post("/dates/"+date+"/guest", "/dates/"+date+"/guest", guestForm(name, email))
}

// confirmLink is the token in the newest confirm email to addr.
func (s *site) confirmLink(addr string) string {
	s.t.Helper()
	m, ok := s.outbox.LatestTo(addr)
	if !ok {
		s.t.Fatalf("no email to %s", addr)
	}
	l := confirmPattern.FindStringSubmatch(m.Text)
	if l == nil {
		s.t.Fatalf("no confirm link in %q", m.Text)
	}
	return l[1]
}

var csrfValue = regexp.MustCompile(`value="[A-Za-z0-9_-]{43}"`)

func stripTokens(body string) string { return csrfValue.ReplaceAllString(body, "") }

// HAZARDS 3, R15: the guest form answers alike for a new address, a member,
// a guest, a waiting request and a turned-off account, and when a limit is
// reached; every address gets the same row, job and email shape.
func TestGuestFormGivesOneAnswer(t *testing.T) {
	s, admin, _, date := publicDate(t, "")
	s.member("Mia Member", "mia@example.org")
	s.Writer.Exec(`INSERT INTO people (uid, name, email, status, created_at, updated_at) VALUES
		('g1', 'Gus Guest', 'gus@example.org', 'guest', 0, 0), ('w1', 'Wes Waiting', 'wes@example.org', 'pending', 0, 0),
		('d1', 'Dee Off', 'dee@example.org', 'disabled', 0, 0)`)
	people := s.Count(t, `SELECT count(*) FROM people`)
	addrs := []string{"new@example.org", "mia@example.org", "gus@example.org", "wes@example.org", "dee@example.org"}
	var first string
	for _, a := range addrs {
		r := s.browser().rsvp(date, "Zorro Typed", a)
		body := strings.ReplaceAll(stripTokens(between(r.body, "<main", "</main>")), a, "ADDR")
		if r.status != http.StatusOK || !strings.Contains(body, "Check your email") {
			t.Fatalf("%s: %d", a, r.status)
		}
		if first == "" {
			first = body
		} else if body != first {
			t.Errorf("%s answered differently", a)
		}
	}
	s.work()
	if n := s.Count(t, `SELECT count(*) FROM links WHERE purpose = 'guest'`); n != len(addrs) {
		t.Fatalf("%d confirm links for %d addresses", n, len(addrs))
	}
	if s.Count(t, `SELECT count(*) FROM people`) != people || s.Count(t, `SELECT count(*) FROM answers`) != 0 {
		t.Fatal("a person or an answer exists before anyone confirmed (R35)")
	}
	var mail string
	for _, a := range addrs {
		m, ok := s.outbox.LatestTo(a)
		if !ok {
			t.Fatalf("no email to %s", a)
		}
		text := confirmPattern.ReplaceAllString(m.Text, "LINK")
		if strings.Contains(text, "Zorro") {
			t.Fatal("the confirm email carries what the stranger typed")
		}
		if mail == "" {
			mail = m.Subject + text
		} else if m.Subject+text != mail {
			t.Errorf("%s got a different email", a)
		}
	}
	// A limit answers with the same page.
	b := s.browser()
	for i := 0; i < 25; i++ {
		r := b.rsvp(date, "Someone", "flood@example.org")
		if r.status != http.StatusOK || !strings.Contains(r.body, "Check your email") {
			t.Fatalf("request %d: %d", i, r.status)
		}
	}
	s.work()
	if n := s.Count(t, `SELECT count(*) FROM links WHERE purpose = 'guest'`); n != len(addrs)+1 {
		t.Fatalf("%d links; the same address and date replace each other", n)
	}
	_ = admin
}

// HAZARDS 3: every field is checked before anything else, and a bad one
// answers alike whoever the address belongs to.
func TestGuestFormChecksFirst(t *testing.T) {
	s, _, _, date := publicDate(t, "")
	s.member("Mia Member", "mia@example.org")
	for _, f := range []url.Values{
		guestForm("Mi\x00a", "mia@example.org"), guestForm("Mi\x00a", "nobody@example.org"),
		guestForm("Mia\r\nBcc: all@example.org", "mia@example.org"), guestForm(strings.Repeat("a", 101), "mia@example.org"),
		guestForm("Mia", "mia@example.org\r\nBcc: x@example.org"), {"name": {"Mia"}, "email": {"mia@example.org"}},
	} {
		r := s.browser().post("/dates/"+date+"/guest", "/dates/"+date+"/guest", f)
		if r.status != http.StatusUnprocessableEntity || !strings.Contains(r.body, "Check these, then try again") {
			t.Errorf("%q: %d", f, r.status)
		}
	}
	s.work()
	if n := s.Count(t, `SELECT count(*) FROM links WHERE purpose = 'guest'`); n != 0 {
		t.Fatalf("%d links from bad forms", n)
	}
}

// R35, R26: the confirm link is one click in the browser that asked and a
// button anywhere else; it works once, within 24 hours, and its row is
// gone after use or expiry. Using it makes the guest, answers Going, signs
// them in, and emails a manage link.
func TestConfirmLink(t *testing.T) {
	s, _, _, date := publicDate(t, "")
	asker := s.browser()
	asker.rsvp(date, "Gina Guest", "gina@example.org")
	s.work()
	tok := s.confirmLink("gina@example.org")
	other := s.browser()
	if r := other.get("/l/" + tok); r.status != http.StatusOK || !strings.Contains(r.body, "Confirm my RSVP") {
		t.Fatalf("another browser: %d", r.status)
	}
	if s.Count(t, `SELECT count(*) FROM people WHERE email = 'gina@example.org'`) != 0 {
		t.Fatal("opening the link elsewhere made a person")
	}
	r := asker.get("/l/" + tok)
	if loc := r.header.Get("Location"); loc != "/dates/"+date+"?done=guest" {
		t.Fatalf("one click in the asking browser: %d %s", r.status, loc)
	}
	if s.Count(t, `SELECT count(*) FROM people p JOIN answers a ON a.person_id = p.id WHERE p.email = 'gina@example.org' AND p.status = 'guest' AND a.status = 'going'`) != 1 {
		t.Fatal("no guest going")
	}
	if s.Count(t, `SELECT count(*) FROM links WHERE purpose = 'guest'`) != 0 {
		t.Fatal("the confirm link's row survived its use")
	}
	page := asker.get("/dates/" + date + "?done=guest")
	if !strings.Contains(page.body, "We&#39;ve emailed you a link") || !strings.Contains(page.body, "Cancel my RSVP") || !strings.Contains(page.body, "Gina Guest, guest") {
		t.Fatalf("the date page for the guest: %s", between(page.body, "<header", "</section>"))
	}
	if r := other.post("/signin", "/l/"+tok, nil); !strings.Contains(r.body, "This link no longer works") {
		t.Fatal("the link worked twice")
	}
	s.work()
	m, _ := s.outbox.LatestTo("gina@example.org")
	if !strings.Contains(m.Text, "You're going to") || managePattern.FindString(m.Text) == "" {
		t.Fatalf("the going email: %q", m.Text)
	}
	// A link left a day expires, and the cleanup deletes it with its sealed address.
	s.browser().rsvp(date, "Late Larry", "larry@example.org")
	s.work()
	late := s.confirmLink("larry@example.org")
	s.Clock.Advance(25 * time.Hour)
	if r := s.browser().post("/signin", "/l/"+late, nil); !strings.Contains(r.body, "This link no longer works") {
		t.Fatal("an expired link worked")
	}
	if err := accounts.CleanupGuests(s.Exec)(context.Background(), write.Job{}); err != nil {
		t.Fatal(err)
	}
	if s.Count(t, `SELECT count(*) FROM links WHERE purpose = 'guest'`) != 0 {
		t.Fatal("an expired confirm link was kept")
	}
}

// R35: a confirm on a date since made private, cancelled or ended answers
// nothing and keeps nothing.
func TestConfirmRechecksTheDate(t *testing.T) {
	for _, c := range []struct {
		name   string
		change func(s *site, admin *browser, event, date string)
	}{
		{"made church only", func(s *site, _ *browser, event, _ string) {
			s.Writer.Exec(`UPDATE events SET visibility = 'church' WHERE uid = ?`, event)
		}},
		{"cancelled", func(s *site, admin *browser, _, date string) {
			admin.post("/dates/"+date, "/dates/"+date+"/cancel", nil)
		}},
		{"ended", func(s *site, _ *browser, _, _ string) { s.Clock.Set(time.Date(2026, 12, 2, 6, 0, 0, 0, time.UTC)) }}, // the asking was the day before
	} {
		t.Run(c.name, func(t *testing.T) {
			s, admin, event, date := publicDate(t, "")
			s.Clock.Set(time.Date(2026, 12, 1, 12, 0, 0, 0, time.UTC))
			b := s.browser()
			b.rsvp(date, "Gina Guest", "gina@example.org")
			s.work()
			tok := s.confirmLink("gina@example.org")
			c.change(s, admin, event, date)
			r := b.get("/l/" + tok)
			if !strings.Contains(r.body, "isn&#39;t taking RSVPs now") {
				t.Fatalf("%d %s", r.status, between(r.body, "<main", "</main>"))
			}
			if s.Count(t, `SELECT count(*) FROM people WHERE email = 'gina@example.org'`)+s.Count(t, `SELECT count(*) FROM links WHERE purpose = 'guest'`) != 0 {
				t.Fatal("something was kept")
			}
		})
	}
}

// R15, R42: an address that has an account answers as it, where it may; a
// member removed from the group, or a waiting or turned-off account, is
// told gently that it could not be saved.
func TestConfirmForAnExistingAccount(t *testing.T) {
	s, admin, _, date := publicDate(t, "")
	mia := s.member("Mia Member", "mia@example.org")
	_ = mia
	b := s.browser()
	b.rsvp(date, "Not Mia", "mia@example.org")
	s.work()
	b.get("/l/" + s.confirmLink("mia@example.org"))
	if s.Count(t, `SELECT count(*) FROM answers a JOIN people p ON p.id = a.person_id WHERE p.email = 'mia@example.org' AND a.status = 'going' AND p.name = 'Mia Member' AND p.status = 'active'`) != 1 {
		t.Fatal("a member's address didn't answer as the member, or the typed name replaced theirs")
	}
	rob := s.member("Rob Removed", "rob@example.org")
	group := strings.TrimPrefix(between(admin.get("/dates/"+date).body, `href="/groups/`, `"`), "")
	rob.post("/groups/"+group, "/groups/"+group+"/join", nil)
	admin.post("/groups/"+group+"/members", "/groups/"+group+"/members/"+s.uidOf("rob@example.org")+"/remove", nil)
	s.Writer.Exec(`INSERT INTO people (uid, name, email, status, created_at, updated_at) VALUES ('d1', 'Dee Off', 'dee@example.org', 'disabled', 0, 0)`)
	for _, a := range []string{"rob@example.org", "dee@example.org"} {
		b := s.browser()
		b.rsvp(date, "Someone", a)
		s.work()
		r := b.get("/l/" + s.confirmLink(a))
		if !strings.Contains(r.body, "Your RSVP could not be saved") || r.header.Get("Set-Cookie") != "" && strings.Contains(r.header.Get("Set-Cookie"), "session=") && !strings.Contains(r.header.Get("Set-Cookie"), "Max-Age=0") {
			t.Fatalf("%s: %s", a, between(r.body, "<main", "</main>"))
		}
		if s.Count(t, `SELECT count(*) FROM answers a JOIN people p ON p.id = a.person_id WHERE p.email = ?`, a) != 0 {
			t.Fatalf("%s answered", a)
		}
	}
}

// A guest session is a signed-out visitor everywhere but their own answers
// and notices (R15).
func TestAGuestIsSignedOutElsewhere(t *testing.T) {
	s, admin, _, date := publicDate(t, "")
	church := s.start(admin, "Elders", "church")
	churchEvent := s.schedule(admin, church, func() url.Values {
		f := eventForm("Elders meeting", "2026-12-02", "19:00", "once")
		f.Set("visibility", "church")
		return f
	}())
	churchDate := firstDate(admin.get("/events/" + churchEvent).body)
	g := s.browser()
	g.rsvp(date, "Gina Guest", "gina@example.org")
	s.work()
	g.get("/l/" + s.confirmLink("gina@example.org"))
	for path, want := range map[string]int{
		"/dates/" + churchDate:           http.StatusNotFound,
		"/groups/" + church:              http.StatusNotFound,
		"/groups/" + church + "/members": http.StatusNotFound,
		"/admin/requests":                http.StatusNotFound,
		"/admin/people":                  http.StatusNotFound,
		"/groups/new":                    http.StatusSeeOther,
		"/profile":                       http.StatusSeeOther,
	} {
		if r := g.get(path); r.status != want {
			t.Errorf("%s: %d, want %d", path, r.status, want)
		}
	}
	if r := g.get("/groups"); strings.Contains(r.body, "Elders") {
		t.Error("a guest finds a church-only group")
	}
	if r := g.get("/notices"); r.status != http.StatusOK {
		t.Errorf("a guest's notices: %d", r.status)
	}
	if r := g.get("/"); !strings.Contains(r.body, "Your RSVPs") || !strings.Contains(r.body, "Supper") {
		t.Error("a guest's home lacks their RSVPs")
	}
	// A date since made church only drops out of their list and hides.
	s.Writer.Exec(`UPDATE events SET visibility = 'church' WHERE title = 'Supper'`)
	if r := g.get("/"); strings.Contains(r.body, "Supper") {
		t.Error("a guest's list shows a church-only date")
	}
	if r := g.get("/dates/" + date); r.status != http.StatusNotFound {
		t.Errorf("a guest sees a church-only date: %d", r.status)
	}
	s.Writer.Exec(`UPDATE events SET visibility = 'public' WHERE title = 'Supper'`)
	// Joining, starting a group and editing a profile are refused.
	public := between(admin.get("/dates/"+date).body, `href="/groups/`, `"`)
	g.post("/groups/"+public, "/groups/"+public+"/join", nil)
	if s.Count(t, `SELECT count(*) FROM memberships m JOIN people p ON p.id = m.person_id WHERE p.email = 'gina@example.org'`) != 0 {
		t.Fatal("a guest joined a group")
	}
	var token string
	for _, c := range g.c.Jar.Cookies(mustURL(s.srv.URL)) {
		if strings.HasSuffix(c.Name, "session") {
			token = c.Value
		}
	}
	err := s.Exec.Run(context.Background(), write.Session(accounts.Hash(token)), accounts.EditName{Name: "Hacked", Version: 1})
	if !errors.Is(err, write.ErrNotAllowed) {
		t.Fatalf("a guest edited a profile: %v", err)
	}
}

func mustURL(s string) *url.URL {
	u, _ := url.Parse(s)
	return u
}

// DEC-55: the manage link shows the date and a Cancel; Cancel takes the
// answer back and revokes the link; every link that does not work answers
// alike, and it dies when the date ends. It never signs anyone in.
func TestManageLink(t *testing.T) {
	s, _, _, date := publicDate(t, "")
	b := s.browser()
	b.rsvp(date, "Gina Guest", "gina@example.org")
	s.work()
	b.get("/l/" + s.confirmLink("gina@example.org"))
	s.work()
	m, _ := s.outbox.LatestTo("gina@example.org")
	tok := managePattern.FindStringSubmatch(m.Text)[1]
	stranger := s.browser()
	r := stranger.get("/g/" + tok)
	if r.status != http.StatusOK || !strings.Contains(r.body, "Cancel my RSVP") || !strings.Contains(r.body, "Supper") || strings.Contains(r.body, "maps") {
		t.Fatalf("manage page: %d", r.status)
	}
	if strings.Contains(r.header.Get("Set-Cookie"), "session=") || !strings.Contains(r.header.Get("Cache-Control"), "no-store") || r.header.Get("Referrer-Policy") != "same-origin" {
		t.Fatal("the manage page signs someone in, or may be cached or leak its address")
	}
	dead := func(path string) resp { return s.browser().get(path) }
	unknown := dead("/g/" + strings.Repeat("A", 43))
	s.browser().rsvp(date, "Zed", "zed@example.org")
	s.work()
	for name, got := range map[string]resp{"short": dead("/g/abc"), "a confirm token": dead("/g/" + s.confirmLink("zed@example.org"))} {
		if got.status != unknown.status || stripTokens(got.body) != stripTokens(unknown.body) {
			t.Errorf("%s answers differently from an unknown link", name)
		}
	}
	if r := stranger.post("/g/"+tok, "/g/"+tok, nil); !strings.Contains(r.body, "Your RSVP is cancelled") {
		t.Fatalf("cancel: %s", between(r.body, "<main", "</main>"))
	}
	if s.Count(t, `SELECT count(*) FROM answers WHERE status = 'cancelled'`) != 1 {
		t.Fatal("the answer wasn't taken back")
	}
	if got := dead("/g/" + tok); got.status != unknown.status || stripTokens(got.body) != stripTokens(unknown.body) {
		t.Error("a revoked link answers differently from an unknown one")
	}
	// Answer again, and the new link dies when the date ends.
	b.post("/dates/"+date, "/dates/"+date+"/answer", url.Values{"answer": {"going"}, "scope": {"date"}})
	var tok2 string
	if err := s.Exec.Run(context.Background(), write.System(), manageFor{email: "gina@example.org", out: &tok2}); err != nil {
		t.Fatal(err)
	}
	if r := dead("/g/" + tok2); r.status != http.StatusOK {
		t.Fatalf("a new manage link: %d", r.status)
	}
	s.Clock.Set(time.Date(2026, 12, 2, 6, 0, 0, 0, time.UTC))
	if got := dead("/g/" + tok2); got.status != unknown.status || stripTokens(got.body) != stripTokens(unknown.body) {
		t.Error("a link for an ended date answers differently from an unknown one")
	}
}

type manageFor struct {
	email string
	out   *string
}

func (manageFor) Action() string { return "test.manage" }
func (c manageFor) Apply(tx *write.Tx) error {
	var person, date int64
	if err := tx.QueryRow(`SELECT a.person_id, a.date_id FROM answers a JOIN people p ON p.id = a.person_id WHERE p.email = ?`, c.email).Scan(&person, &date); err != nil {
		return err
	}
	_, tok, err := notices.ManageLink(tx, person, date)
	*c.out = tok
	return err
}

// Seats and the waitlist apply to guests, who can cancel only their own.
func TestGuestsWaitAndCancelTheirOwn(t *testing.T) {
	s, admin, _, date := publicDate(t, "1")
	answer(admin, date, "going", "date")
	g := s.browser()
	g.rsvp(date, "Gina Guest", "gina@example.org")
	s.work()
	g.get("/l/" + s.confirmLink("gina@example.org"))
	if s.Count(t, `SELECT count(*) FROM answers WHERE status = 'waiting'`) != 1 {
		t.Fatal("a guest on a full date isn't waiting")
	}
	s.work()
	if m, _ := s.outbox.LatestTo("gina@example.org"); !strings.Contains(m.Text, "you're on the waitlist") {
		t.Fatalf("the going email for a waiting guest: %q", m.Text)
	}
	if r := g.get("/dates/" + date); !strings.Contains(r.body, "You&#39;re on the waitlist, number 1.") {
		t.Fatal("the guest doesn't see their place")
	}
	// A seat frees: the guest moves up, and their email carries a manage link.
	answer(admin, date, "cant", "date")
	s.work()
	m, _ := s.outbox.LatestTo("gina@example.org")
	if !strings.Contains(m.Text, "A seat opened") || managePattern.FindString(m.Text) == "" {
		t.Fatalf("the moved-up email: %q", m.Text)
	}
	// Another guest can't cancel Gina's answer.
	h := s.browser()
	h.rsvp(date, "Hal Guest", "hal@example.org")
	s.work()
	h.get("/l/" + s.confirmLink("hal@example.org"))
	h.post("/dates/"+date, "/dates/"+date+"/withdraw", nil)
	h.post("/dates/"+date, "/dates/"+date+"/withdraw", nil)
	if s.Count(t, `SELECT count(*) FROM answers a JOIN people p ON p.id = a.person_id WHERE p.email = 'gina@example.org' AND a.status = 'going'`) != 1 {
		t.Fatal("someone else's cancel reached Gina's answer")
	}
	if r := g.post("/dates/"+date, "/dates/"+date+"/withdraw", nil); r.header.Get("Location") != "/dates/"+date+"?done=guest-cancelled" {
		t.Fatalf("the guest's own cancel: %s", r.header.Get("Location"))
	}
	// A member's form can't cancel; a member's answer can't be made a guest's.
	if r := admin.post("/dates/"+date, "/dates/"+date+"/withdraw", nil); r.status != http.StatusNotFound {
		t.Fatalf("a member used the guest cancel: %d", r.status)
	}
}

// A guest who asks for an account stays a guest with their answers until
// an admin decides; approving makes them a member, declining keeps them a
// guest.
func TestAGuestAsksForAnAccount(t *testing.T) {
	s, admin, _, date := publicDate(t, "")
	g := s.browser()
	g.rsvp(date, "Gina Guest", "gina@example.org")
	s.work()
	g.get("/l/" + s.confirmLink("gina@example.org"))
	s.browser().post("/request", "/request", url.Values{"name": {"Someone Else"}, "email": {"gina@example.org"}, "age": {"adult"}})
	s.work()
	if r := admin.get("/admin/requests"); !strings.Contains(r.body, "Gina Guest") {
		t.Fatal("the guest's ask isn't waiting for an admin")
	}
	uid := s.uidOf("gina@example.org")
	admin.post("/admin/requests", "/admin/requests/"+uid+"/approve", nil)
	if s.Count(t, `SELECT count(*) FROM people p JOIN answers a ON a.person_id = p.id WHERE p.uid = ? AND p.status = 'active' AND p.name = 'Gina Guest' AND a.status = 'going'`, uid) != 1 {
		t.Fatal("approving didn't make a member with their answers")
	}
	h := s.browser()
	h.rsvp(date, "Hal Guest", "hal@example.org")
	s.work()
	h.get("/l/" + s.confirmLink("hal@example.org"))
	s.browser().post("/request", "/request", url.Values{"name": {"Hal"}, "email": {"hal@example.org"}, "age": {"adult"}})
	s.work()
	admin.post("/admin/requests", "/admin/requests/"+s.uidOf("hal@example.org")+"/decline", nil)
	if s.Count(t, `SELECT count(*) FROM people p JOIN answers a ON a.person_id = p.id WHERE p.email = 'hal@example.org' AND p.status = 'guest' AND p.requested_at IS NULL`) != 1 {
		t.Fatal("declining a guest's ask didn't leave them a guest with their answer")
	}
}
