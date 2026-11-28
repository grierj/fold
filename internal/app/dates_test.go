package app_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// P7, DEC-91: those who run an event cancel one date and bring it back; the
// date page says why it is off; members never see the cancelled date in a
// list, and can't cancel one.
func TestCancelAndRestoreOneDate(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	group := s.start(admin, "Study", "public")
	uid := s.schedule(admin, group, eventForm("Study", "2026-12-01", "19:00", "weekly"))
	date := firstDate(admin.get("/events/" + uid).body)
	mia := s.member("Mia Member", "mia@example.org")
	mia.post("/groups/"+group, "/groups/"+group+"/join", nil)
	if r := mia.get("/dates/" + date); strings.Contains(r.body, "Cancel this date") {
		t.Fatal("a member is offered to cancel")
	}
	if r := mia.post("/dates/"+date, "/dates/"+date+"/cancel", nil); r.status != http.StatusNotFound {
		t.Fatalf("a member cancelled: %d", r.status)
	}
	if r := admin.get("/dates/" + date); !strings.Contains(r.body, "Cancel this date") || !strings.Contains(r.body, "Only this date is cancelled") {
		t.Fatal("no way to cancel one date")
	}
	r := admin.post("/dates/"+date, "/dates/"+date+"/cancel", nil)
	if r.header.Get("Location") != "/dates/"+date+"?done=cancelled" {
		t.Fatalf("cancel: %d %s", r.status, r.header.Get("Location"))
	}
	if r := mia.get("/dates/" + date); !strings.Contains(r.body, "The people running the event cancelled this date.") {
		t.Fatal("the date page doesn't say it was cancelled by hand")
	}
	if r := mia.get("/events/" + uid); strings.Contains(r.body, "Tue, Dec 1, 7:00") {
		t.Fatal("a member's list shows the cancelled date")
	}
	if r := admin.get("/events/" + uid); !strings.Contains(r.body, `Tue, Dec 1, 7:00 to 8:30 pm</a> <span class="tag">Cancelled</span>`) {
		t.Fatal("the leader has no way back to the cancelled date")
	}
	if r := admin.post("/dates/"+date, "/dates/"+date+"/cancel", nil); !strings.HasSuffix(r.header.Get("Location"), "done=gone") {
		t.Fatalf("a second cancel: %s", r.header.Get("Location"))
	}
	r = admin.post("/dates/"+date, "/dates/"+date+"/restore", nil)
	if !strings.HasSuffix(r.header.Get("Location"), "done=restored") {
		t.Fatalf("restore: %s", r.header.Get("Location"))
	}
	if r := mia.get("/events/" + uid); !strings.Contains(r.body, "Tue, Dec 1, 7:00") {
		t.Fatal("the restored date is not back")
	}
	if n := s.Count(t, `SELECT count(*) FROM audit_log WHERE action IN ('date.cancel', 'date.restore')`); n != 2 {
		t.Fatalf("%d audit rows", n)
	}
}

// P7, DEC-28: one date of an invite-only group's event opened to the public
// is seen by strangers; the event is not, and nothing names the group.
func TestOpenOneDateWider(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	mel := s.member("Mel Leader", "mel@example.org")
	group := s.start(mel, "Elders Prayer", "invite_only")
	admin.post("/admin/groups", "/admin/groups/"+group+"/approve", nil)
	form := eventForm("Prayer night", "2026-12-02", "19:00", "weekly")
	form.Set("visibility", "invite_only")
	uid := s.schedule(mel, group, form)
	date := firstDate(mel.get("/events/" + uid).body)
	open := "/dates/" + date + "/open"
	if r := mel.get(open); !strings.Contains(r.body, "The same as the event") || strings.Contains(r.body, `value="invite_only"`) {
		t.Fatal("the form offers narrower than the event")
	}
	if r := mel.post(open, open, url.Values{"visibility": {"invite_only"}}); r.status != http.StatusNotFound {
		t.Fatalf("a narrower date: %d", r.status)
	}
	if r := mel.post(open, open, url.Values{"visibility": {"public"}, "reason": {"Our open night."}}); !strings.HasSuffix(r.header.Get("Location"), "done=opened") {
		t.Fatalf("open: %d %s", r.status, r.header.Get("Location"))
	}
	stranger := s.browser()
	r := stranger.get("/dates/" + date)
	if r.status != http.StatusOK || !strings.Contains(r.body, "Prayer night") {
		t.Fatalf("a stranger can't see the opened date: %d", r.status)
	}
	for _, leak := range []string{"Elders Prayer", group, uid, "Mel Leader", "About this event"} {
		if strings.Contains(r.body, leak) {
			t.Errorf("the opened date shows %q", leak)
		}
	}
	if r := stranger.get("/events/" + uid); r.status != http.StatusNotFound {
		t.Fatal("the event shows to a stranger")
	}
	if r := admin.get("/notices"); !strings.Contains(r.body, "Mel Leader opened a date of “Prayer night” to everyone.") || !strings.Contains(r.body, "Our open night.") {
		t.Fatalf("the admin's notice: %s", between(r.body, "<main", "</main>"))
	}
	if r := mel.get("/dates/" + date); !strings.Contains(r.body, "open wider than the rest of the event") {
		t.Fatal("the leader is not told the date is opened")
	}
}

// DEC-7: an admin makes a church-wide event and names hosts, who run it;
// nobody else may.
func TestChurchWideEventAndHosts(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	mia := s.member("Mia Member", "mia@example.org")
	if r := mia.get("/events/new"); r.status != http.StatusNotFound {
		t.Fatal("a member reached the church-wide form")
	}
	if r := admin.get("/admin/events"); !strings.Contains(r.body, "No church-wide events yet") || !strings.Contains(r.body, `href="/events/new"`) {
		t.Fatal("no way in from the admin pages")
	}
	form := eventForm("Christmas Eve service", "2026-12-24", "18:00", "once")
	form.Set("visibility", "invite_only")
	if r := admin.post("/events/new", "/events/new", form); r.status != http.StatusNotFound {
		t.Fatalf("an invite-only church-wide event: %d", r.status)
	}
	form.Set("visibility", "church")
	r := admin.post("/events/new", "/events/new", form)
	loc := r.header.Get("Location")
	if !strings.HasPrefix(loc, "/events/") {
		t.Fatalf("create: %d %s", r.status, between(r.body, "<main", "</main>"))
	}
	uid := strings.TrimPrefix(strings.SplitN(loc, "?", 2)[0], "/events/")
	if r := mia.get("/events/" + uid); r.status != http.StatusOK || !strings.Contains(r.body, "Church-wide") || strings.Contains(r.body, "Edit the event") {
		t.Fatalf("a member's view: %d", r.status)
	}
	if r := s.browser().get("/events/" + uid); r.status != http.StatusNotFound {
		t.Fatal("a church-only event shows to a stranger")
	}
	if r := mia.get("/events/" + uid + "/hosts"); r.status != http.StatusNotFound {
		t.Fatal("a member reached the hosts page")
	}
	hosts := "/events/" + uid + "/hosts"
	r = admin.get(hosts + "?find=Mia")
	if !strings.Contains(r.body, "Mia Member") || strings.Contains(r.body, "mia@example.org") {
		t.Fatal("the search doesn't find a name, or shows an address")
	}
	admin.post(hosts, hosts+"/add", url.Values{"person": {personUID(s, "mia@example.org")}})
	if r := mia.get("/notices"); !strings.Contains(r.body, "Pat Admin made you a host of “Christmas Eve service”.") {
		t.Fatal("the host was not told")
	}
	if r := s.browser().get("/admin/events"); r.status != http.StatusNotFound {
		t.Fatal("the admin list shows to a stranger")
	}
	if r := mia.get("/events/" + uid); !strings.Contains(r.body, "Hosted by Mia Member") || !strings.Contains(r.body, "Edit the event") {
		t.Fatal("the host can't run it")
	}
	edit := "/events/" + uid + "/edit"
	changed := editForm(eventForm("Christmas Eve candlelight", "2026-12-24", "18:00", "once"), "1")
	changed.Set("visibility", "public")
	if r := mia.post(edit, edit, changed); r.status != http.StatusSeeOther {
		t.Fatalf("the host's edit: %d", r.status)
	}
	if r := mia.get(hosts); !strings.Contains(r.body, "Remove Mia Member as a host?") {
		t.Fatal("the host can't see the hosts")
	}
	s.Writer.Exec(`UPDATE people SET status = 'disabled' WHERE email = 'mia@example.org'`)
	if r := admin.get("/events/" + uid); strings.Contains(r.body, "Hosted by") {
		t.Fatal("a disabled host is still named")
	}
	s.Writer.Exec(`UPDATE people SET status = 'active' WHERE email = 'mia@example.org'`)
	admin.post(hosts, hosts+"/"+personUID(s, "mia@example.org")+"/remove", nil)
	if r := mia.get(edit); r.status != http.StatusNotFound {
		t.Fatal("a removed host can still edit")
	}
	r = admin.post("/events/"+uid, "/events/"+uid+"/delete", nil)
	if r.header.Get("Location") != "/?done=event-deleted" {
		t.Fatalf("after deleting a church-wide event: %s", r.header.Get("Location"))
	}
	if n := s.Count(t, `SELECT count(*) FROM audit_log WHERE action IN ('event.host_add', 'event.host_remove')`); n != 2 {
		t.Fatalf("%d host audit rows", n)
	}
}

