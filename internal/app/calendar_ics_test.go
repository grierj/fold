package app_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// DEC-93: a date downloads for a calendar app to anyone who may see it, as
// its page shows it: the description only to those who see the event, the
// video link only to people coming (R41), Going confirmed and Maybe
// tentative, nobody else's answer, and an unseen date answers as missing.
func TestAddToCalendar(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	group := s.start(admin, "Supper Club", "public")
	f := eventForm("Supper, then songs", "2026-12-01", "18:00", "once")
	f.Set("video", "https://meet.example.org/supper")
	uid := s.schedule(admin, group, f)
	date := firstDate(admin.get("/events/" + uid).body)
	ics := "/dates/" + date + "/calendar.ics"

	if r := admin.get("/dates/" + date); !strings.Contains(r.body, `href="`+ics+`"`) || !strings.Contains(r.body, `value="`+s.srv.URL+`/dates/`+date+`"`) {
		t.Fatal("the date page offers no Add to calendar or link to share")
	}
	stranger := s.browser()
	r := stranger.get(ics)
	if r.status != http.StatusOK || r.header.Get("Content-Type") != "text/calendar; charset=utf-8" ||
		r.header.Get("Content-Disposition") != `attachment; filename="event.ics"` {
		t.Fatalf("a public date's file: %d %v", r.status, r.header)
	}
	for _, want := range []string{"BEGIN:VCALENDAR", "TZID:America/Boise", "UID:" + date + "@127.0.0.1",
		"DTSTART;TZID=America/Boise:20261201T180000", "DTEND;TZID=America/Boise:20261201T193000",
		`SUMMARY:Supper\, then songs`, `DESCRIPTION:Bring a Bible.\nCoffee at 6:45.\n\n` + s.srv.URL + "/dates/" + date, `LOCATION:Room 4 & the hall`} {
		if !strings.Contains(unfold(r.body), want) {
			t.Errorf("the file lacks %q:\n%s", want, r.body)
		}
	}
	if strings.Contains(r.body, "meet.example.org") || strings.Contains(r.body, "STATUS:") {
		t.Fatal("a stranger's file has the video link or a status")
	}

	mia := s.member("Mia Member", "mia@example.org")
	answer(mia, date, "maybe", "date")
	if body := unfold(mia.get(ics).body); !strings.Contains(body, "STATUS:TENTATIVE") || !strings.Contains(body, "Join by video: https://meet.example.org/supper") {
		t.Fatalf("a maybe:\n%s", body)
	}
	answer(mia, date, "going", "date")
	if body := mia.get(ics).body; !strings.Contains(body, "STATUS:CONFIRMED") || strings.Contains(body, "Mia") {
		t.Fatalf("a going:\n%s", body)
	}
	if body := admin.get(ics).body; strings.Contains(body, "STATUS:") || !strings.Contains(unfold(body), "meet.example.org") {
		t.Fatal("the leader's file carries someone else's answer, or lacks the link they run")
	}

	// Made invite-only, it answers as missing to anyone outside it.
	if _, err := s.Writer.Exec(`UPDATE events SET visibility = 'invite_only'`); err != nil {
		t.Fatal(err)
	}
	if r := stranger.get(ics); r.status != http.StatusNotFound {
		t.Fatalf("an unseen date's file: %d", r.status)
	}
	if r := stranger.get("/dates/nope/calendar.ics"); r.status != http.StatusNotFound {
		t.Fatalf("a missing date's file: %d", r.status)
	}
	if r := admin.get(ics); r.status != http.StatusOK {
		t.Fatalf("the leader's own file: %d", r.status)
	}
}

// P7, #167: one date of an invite-only group's event opened to the public
// downloads without the event's description, and names no group.
func TestAddToCalendarOpenedDate(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	group := s.start(admin, "Elders Prayer", "invite_only")
	f := eventForm("Prayer night", "2026-12-02", "19:00", "weekly")
	f.Set("visibility", "invite_only")
	uid := s.schedule(admin, group, f)
	date := firstDate(admin.get("/events/" + uid).body)
	open := "/dates/" + date + "/open"
	admin.post(open, open, url.Values{"visibility": {"public"}, "reason": {"Our open night."}})
	r := s.browser().get("/dates/" + date + "/calendar.ics")
	if r.status != http.StatusOK || strings.Contains(r.body, "Bring a Bible") || strings.Contains(r.body, "Elders") || strings.Contains(r.body, "RRULE") {
		t.Fatalf("an opened date's file: %d\n%s", r.status, r.body)
	}
	if _, err := s.Writer.Exec(`UPDATE events SET video_url = 'https://meet.example.org/prayer'`); err != nil {
		t.Fatal(err)
	}
	if r := admin.get("/dates/" + date + "/calendar.ics"); !strings.Contains(unfold(r.body), "meet.example.org") {
		t.Fatal("the leader's file lacks the video link")
	}
	cancel := "/dates/" + date + "/cancel"
	admin.post("/dates/"+date, cancel, nil)
	if r := s.browser().get("/dates/" + date + "/calendar.ics"); !strings.Contains(r.body, "STATUS:CANCELLED") {
		t.Fatalf("a cancelled date's file:\n%s", r.body)
	}
	if r := admin.get("/dates/" + date + "/calendar.ics"); strings.Contains(unfold(r.body), "meet.example.org") {
		t.Fatal("a cancelled date's file gives the video link")
	}
	if r := admin.get("/dates/" + date); strings.Contains(r.body, "/calendar.ics") || !strings.Contains(r.body, `id="share-link"`) {
		t.Fatal("a cancelled date offers Add to calendar, or no longer offers its link")
	}
}

// unfold joins a calendar file's folded lines (RFC 5545 section 3.1).
func unfold(s string) string { return strings.ReplaceAll(s, "\r\n ", "") }

// RFC 5545: every change a calendar app shows about a date raises its
// SEQUENCE and LAST-MODIFIED, in the download and the feed alike, so apps
// replace their copy; a change it does not show raises nothing.
func TestCalendarFileSequence(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	group := s.start(admin, "Study", "public")
	uid := s.schedule(admin, group, eventForm("Study", "2026-12-01", "19:00", "weekly"))
	date := firstDate(admin.get("/events/" + uid).body)
	feed := turnOnFeed(t, admin)
	edit := "/events/" + uid + "/edit"
	version := 1
	steps := []struct {
		name string
		do   func()
		seq  string
	}{
		{"scheduled", func() {}, "SEQUENCE:0"},
		{"a new title", func() {
			admin.post(edit, edit, editForm(eventForm("Bible study", "2026-12-01", "19:00", "weekly"), strconv.Itoa(version)))
		}, "SEQUENCE:1"},
		{"a seat limit, which no calendar shows", func() {
			f := eventForm("Bible study", "2026-12-01", "19:00", "weekly")
			f.Set("seats", "12")
			admin.post(edit, edit, editForm(f, strconv.Itoa(version)))
		}, "SEQUENCE:1"},
		{"a later start", func() {
			f := eventForm("Bible study", "2026-12-01", "19:30", "weekly")
			f.Set("seats", "12")
			admin.post(edit, edit, editForm(f, strconv.Itoa(version)))
		}, "SEQUENCE:2"},
		{"cancelled", func() { admin.post("/dates/"+date, "/dates/"+date+"/cancel", nil) }, "SEQUENCE:3"},
		{"brought back", func() { admin.post("/dates/"+date, "/dates/"+date+"/restore", nil) }, "SEQUENCE:4"},
		{"who can see it, saved", func() {
			admin.post("/dates/"+date+"/open", "/dates/"+date+"/open", url.Values{"visibility": {""}})
		}, "SEQUENCE:5"},
	}
	for _, st := range steps {
		s.Clock.Advance(time.Hour)
		st.do()
		version = s.Count(t, `SELECT version FROM events`)
		one := unfold(admin.get("/dates/" + date + "/calendar.ics").body)
		if !strings.Contains(one, st.seq+"\r\n") || !strings.Contains(one, "LAST-MODIFIED:") {
			t.Fatalf("%s: the download has %q", st.name, regexp.MustCompile(`SEQUENCE:\d+`).FindString(one))
		}
		all := unfold(s.browser().get(feed).body)
		i := strings.Index(all, "UID:"+date+"@")
		if i < 0 || !strings.HasPrefix(all[i:][strings.Index(all[i:], "SEQUENCE:"):], st.seq+"\r\n") {
			t.Fatalf("%s: the feed's sequence differs from %s", st.name, st.seq)
		}
	}
	// Seven steps an hour apart from 5 pm UTC on 25 November.
	if one := admin.get("/dates/" + date + "/calendar.ics").body; !strings.Contains(one, "LAST-MODIFIED:20261126T000000Z") {
		t.Fatalf("LAST-MODIFIED is not the last change's time:\n%s", one)
	}
}
