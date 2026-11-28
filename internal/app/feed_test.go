package app_test

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"fold/internal/backup"
)

var feedPattern = regexp.MustCompile(`/feed/([A-Za-z0-9_-]{43})/calendar\.ics`)

// turnOnFeed makes b's feed, or a new link for it, and returns its path.
func turnOnFeed(t *testing.T, b *browser) string {
	t.Helper()
	r := b.post("/profile", "/profile/feed/new", nil)
	m := feedPattern.FindString(r.body)
	if r.status != http.StatusOK || m == "" {
		t.Fatalf("turning on the feed: %d", r.status)
	}
	return m
}

// gone checks that a feed answers exactly as one never made.
func gone(t *testing.T, s *site, path, why string) {
	t.Helper()
	never := s.browser().get("/feed/" + strings.Repeat("A", 43) + "/calendar.ics")
	r := s.browser().get(path)
	if r.status != http.StatusNotFound || r.body != never.body || r.status != never.status ||
		r.header.Get("Content-Type") != never.header.Get("Content-Type") || r.header.Get("Cache-Control") != never.header.Get("Cache-Control") {
		t.Fatalf("%s: %d %q %v, where never made is %d %q %v", why, r.status, r.body, r.header, never.status, never.body, never.header)
	}
}

// AGENTS.md "Links are secrets": a member's feed is made, replaced and
// turned off by audited commands; each old link answers as one never made,
// as does one whose account is turned off (and stays so when it is on
// again), or one from before a restore. The link is shown once, and is
// never stored or written to the audit log.
func TestFeedLifecycle(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	mia := s.member("Mia Member", "mia@example.org")
	if r := mia.get("/profile"); !strings.Contains(r.body, "Turn on the calendar feed") || feedPattern.MatchString(r.body) {
		t.Fatal("the profile does not offer the feed")
	}
	first := turnOnFeed(t, mia)
	r := s.browser().get(first)
	if r.status != http.StatusOK || r.header.Get("Content-Type") != "text/calendar; charset=utf-8" || !strings.HasPrefix(r.header.Get("Cache-Control"), "private") ||
		r.header.Get("Set-Cookie") != "" || !strings.Contains(r.body, "X-WR-CALNAME:Cloverdale Church of God") {
		t.Fatalf("the feed: %d %v", r.status, r.header)
	}
	if r := mia.get("/profile"); feedPattern.MatchString(r.body) || !strings.Contains(r.body, "Your calendar feed is on") || !strings.Contains(r.body, "Make a new link") {
		t.Fatal("the profile shows the link again, or not that the feed is on")
	}
	second := turnOnFeed(t, mia)
	gone(t, s, first, "a replaced link")
	if r := s.browser().get(second); r.status != http.StatusOK {
		t.Fatalf("the new link: %d", r.status)
	}
	if r := mia.post("/profile", "/profile/feed/off", nil); r.header.Get("Location") != "/profile?done=feed-off#feed" {
		t.Fatalf("turning off: %d %s", r.status, r.header.Get("Location"))
	}
	gone(t, s, second, "a turned-off link")
	if n := s.Count(t, `SELECT count(*) FROM audit_log WHERE action = 'feed.new'`); n != 2 {
		t.Fatalf("%d feed.new rows", n)
	}
	if n := s.Count(t, `SELECT count(*) FROM audit_log WHERE action = 'feed.new' AND details LIKE '%replaced%'`); n != 1 {
		t.Fatal("replacing is not told apart in the audit log")
	}
	if n := s.Count(t, `SELECT count(*) FROM audit_log WHERE action = 'feed.off'`); n != 1 {
		t.Fatal("turning off is not audited")
	}

	third := turnOnFeed(t, mia)
	miaUID := personUID(s, "mia@example.org")
	admin.post("/admin/people/"+miaUID, "/admin/people/"+miaUID+"/disable", nil)
	gone(t, s, third, "a turned-off account's link")
	admin.post("/admin/people/"+miaUID, "/admin/people/"+miaUID+"/enable", nil)
	gone(t, s, third, "a link from before the account was turned off")

	// Nothing holds a link but the person it was shown to.
	for _, path := range []string{first, second, third} {
		token := feedPattern.FindStringSubmatch(path)[1]
		for _, f := range []string{s.Path, s.Path + "-wal"} {
			b, _ := os.ReadFile(f)
			if bytes.Contains(b, []byte(token)) {
				t.Fatalf("%s holds a feed token", f)
			}
		}
	}

	// A restore ends every feed, as it ends sessions and links.
	pat := turnOnFeed(t, admin)
	m, err := backup.Take(context.Background(), s.Exec, s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backup.Restore(context.Background(), s.Exec, s.Dir, m.Name); err != nil {
		t.Fatal(err)
	}
	gone(t, s, pat, "a link from before a restore")
}

// R15: a guest has no feed. Malformed links answer as unknown ones, and the
// feed sets no cookie and renews no session, even read by a signed-in
// browser.
func TestFeedRefusals(t *testing.T) {
	s, _, _, date := publicDate(t, "")
	g := s.guestGoing(date, "Gina Guest", "gina@example.org")
	if r := g.postRaw("/profile/feed/new", url.Values{"csrf": {csrfPattern.FindStringSubmatch(g.get("/").body)[1]}}, nil); r.status != http.StatusSeeOther || r.header.Get("Location") != "/signin" {
		t.Fatalf("a guest turning on a feed: %d %s", r.status, r.header.Get("Location"))
	}
	if n := s.Count(t, `SELECT count(*) FROM people WHERE feed_hash IS NOT NULL`); n != 0 {
		t.Fatal("a guest has a feed")
	}
	for _, bad := range []string{"/feed/short/calendar.ics", "/feed/" + strings.Repeat("!", 43) + "/calendar.ics"} {
		gone(t, s, bad, bad)
	}
	mia := s.member("Mia Member", "mia@example.org")
	path := turnOnFeed(t, mia)
	s.Clock.Advance(2 * time.Hour) // a session renews on the next page after an hour
	for _, p := range []string{path, "/feed/" + strings.Repeat("B", 43) + "/calendar.ics"} {
		if r := mia.get(p); r.header.Get("Set-Cookie") != "" {
			t.Fatalf("%s set a cookie: %v", p, r.header["Set-Cookie"])
		}
	}
	// One link is read at most so often; a calendar app checks hourly.
	var r resp
	for range 60 {
		r = s.browser().get(path)
	}
	if r = s.browser().get(path); r.status != http.StatusTooManyRequests || r.header.Get("Retry-After") == "" {
		t.Fatalf("a link read over and over: %d", r.status)
	}
}

// DEC-71, DEC-64: the feed is the person's Just mine as the date pages
// would show it to them: Going confirmed, Maybe tentative, Can't go left
// out, a cancelled date marked so, the video link only to people coming,
// nobody else's answer, each date as its own download gives it; and an
// invite-only group's dates leave it when the person is removed.
func TestFeedContent(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	group := s.start(admin, "Elders Prayer", "invite_only")
	form := eventForm("Prayer night", "2026-12-02", "19:00", "weekly")
	form.Set("visibility", "invite_only")
	form.Set("video", "https://meet.example.org/prayer")
	uid := s.schedule(admin, group, form)
	body := admin.get("/events/" + uid + "?all=1").body
	var dates []string
	for _, m := range regexp.MustCompile(`href="/dates/([a-z0-9]+)"`).FindAllStringSubmatch(body, 5) {
		dates = append(dates, m[1])
	}
	mia := s.member("Mia Member", "mia@example.org")
	miaUID := personUID(s, "mia@example.org")
	admin.post("/groups/"+group+"/members", "/groups/"+group+"/members/add", url.Values{"person": {miaUID}})
	sam := s.member("Sam Member", "sam@example.org")
	samUID := personUID(s, "sam@example.org")
	admin.post("/groups/"+group+"/members", "/groups/"+group+"/members/add", url.Values{"person": {samUID}})
	answer(mia, dates[0], "going", "date")
	answer(mia, dates[1], "maybe", "date")
	answer(mia, dates[2], "cant", "date")
	answer(sam, dates[3], "going", "date")
	admin.post("/dates/"+dates[3], "/dates/"+dates[3]+"/cancel", nil)

	path := turnOnFeed(t, mia)
	feed := unfold(s.browser().get(path).body)
	event := func(date string) string {
		i := strings.Index(feed, "UID:"+date+"@")
		if i < 0 {
			return ""
		}
		block := feed[i:]
		return block[:strings.Index(block, "END:VEVENT")]
	}
	if e := event(dates[0]); !strings.Contains(e, "STATUS:CONFIRMED") || !strings.Contains(e, "Join by video") {
		t.Errorf("a Going date:\n%s", e)
	}
	if e := event(dates[1]); !strings.Contains(e, "STATUS:TENTATIVE") || !strings.Contains(e, "Join by video") {
		t.Errorf("a Maybe date:\n%s", e)
	}
	if e := event(dates[2]); e != "" {
		t.Errorf("a Can't go date is in the feed:\n%s", e)
	}
	if e := event(dates[3]); !strings.Contains(e, "STATUS:CANCELLED") || strings.Contains(e, "Join by video") {
		t.Errorf("a cancelled date:\n%s", e)
	}
	if e := event(dates[4]); e == "" || strings.Contains(e, "STATUS:") || strings.Contains(e, "Join by video") {
		t.Errorf("an unanswered date of her group:\n%s", e)
	}
	for _, leak := range []string{"Sam", "Mia", "going", "Elders"} {
		if strings.Contains(feed, leak) {
			t.Errorf("the feed carries %q", leak)
		}
	}
	stamp := regexp.MustCompile(`DTSTAMP:\S+`)
	for _, d := range []string{dates[0], dates[1], dates[3]} {
		one := unfold(mia.get("/dates/" + d + "/calendar.ics").body)
		i := strings.Index(one, "BEGIN:VEVENT")
		if want := stamp.ReplaceAllString(one[i+len("BEGIN:VEVENT\r\n"):strings.Index(one, "END:VEVENT")], ""); stamp.ReplaceAllString(event(d), "") != want {
			t.Errorf("the feed's %s differs from its download:\n%s\nwant:\n%s", d, event(d), want)
		}
	}

	admin.post("/groups/"+group+"/members", "/groups/"+group+"/members/"+miaUID+"/remove", nil)
	if feed := s.browser().get(path).body; strings.Contains(feed, "BEGIN:VEVENT") || !strings.Contains(feed, "BEGIN:VCALENDAR") {
		t.Fatalf("a removed member's feed still has the group's dates:\n%s", feed)
	}
}
