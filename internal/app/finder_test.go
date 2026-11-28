package app_test

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// found reports whether a group is listed on a /groups page.
func found(body, name string) bool { return strings.Contains(body, ">"+name+"</a>") }

// PLAN slice 2 milestone 4b, HAZARDS 2: the finder filters by day and time
// of day from the dates the viewer may see. A group whose only Tuesday
// meeting is invite-only is not under Tuesday for an outsider, and its card
// gives the hidden date's time to nobody who may not see it.
func TestFinderUsesOnlyVisibleDates(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	group := s.start(admin, "Supper Club", "public")
	private := eventForm("Leaders' supper", "2026-12-01", "19:00", "once") // a Tuesday
	private.Set("visibility", "invite_only")
	s.schedule(admin, group, private)
	s.schedule(admin, group, eventForm("Supper", "2026-12-03", "18:00", "once")) // a Thursday
	mia := s.member("Mia Member", "mia@example.org")
	for _, b := range []*browser{mia, s.browser()} {
		if r := b.get("/groups?day=tue"); r.status != http.StatusOK || found(r.body, "Supper Club") || !strings.Contains(r.body, "No groups meet then") {
			t.Error("an outsider finds the group by its invite-only Tuesday")
		}
		r := b.get("/groups?day=thu&time=evening")
		if !found(r.body, "Supper Club") || !strings.Contains(r.body, "Next: Thu, Dec 3, 6:00 pm") || strings.Contains(r.body, "Dec 1") {
			t.Error("the group's card is not by its visible Thursday")
		}
	}
	if r := admin.get("/groups?day=tue"); !found(r.body, "Supper Club") || !strings.Contains(r.body, "Next: Tue, Dec 1, 7:00 pm") {
		t.Fatal("the leader does not find their own group by its Tuesday")
	}
	if r := mia.get("/groups?day=funday&time=midnight"); !found(r.body, "Supper Club") {
		t.Fatal("an unknown filter is not ignored")
	}
}

// The day and time are the viewer's: an 11:30 pm Boise meeting is a
// Wednesday morning in New York. Noon is afternoon and 5 pm is evening, and
// days stay whole across the night the clocks change.
func TestFinderDaysAndTimesAreTheViewers(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	for _, g := range []struct{ name, day, at string }{
		{"Night Owls", "2026-12-01", "23:30"}, {"Lunch Bunch", "2026-12-02", "12:00"}, {"Supper Club", "2026-12-02", "17:00"},
		{"Early Risers", "2027-03-15", "00:30"},
	} {
		group := s.start(admin, g.name, "public")
		s.schedule(admin, group, eventForm(g.name, g.day, g.at, "once"))
	}
	mia := s.member("Mia Member", "mia@example.org")
	setZone(s, "mia@example.org", "America/New_York")
	cases := []struct {
		b       *browser
		query   string
		in, out []string
	}{
		{admin, "day=tue&time=evening", []string{"Night Owls"}, []string{"Lunch Bunch"}},
		{mia, "day=wed&time=morning", []string{"Night Owls"}, []string{"Lunch Bunch", "Supper Club"}},
		{mia, "day=tue", nil, []string{"Night Owls"}},
		{admin, "time=afternoon", []string{"Lunch Bunch"}, []string{"Supper Club", "Night Owls"}},
		{admin, "time=evening", []string{"Supper Club", "Night Owls"}, []string{"Lunch Bunch"}},
		{admin, "time=morning", nil, []string{"Lunch Bunch"}},
	}
	for _, c := range cases {
		r := c.b.get("/groups?" + c.query)
		for _, name := range c.in {
			if !found(r.body, name) {
				t.Errorf("%s: %s missing", c.query, name)
			}
		}
		for _, name := range c.out {
			if found(r.body, name) {
				t.Errorf("%s: %s listed", c.query, name)
			}
		}
	}
	// Eight weeks before Monday 15 March 2027, the Sunday before it is 23
	// hours long.
	s.Clock.Set(time.Date(2027, 3, 8, 17, 0, 0, 0, time.UTC))
	if r := admin.get("/groups?day=mon&time=morning"); !found(r.body, "Early Risers") {
		t.Error("a Monday 12:30 am meeting after the change is not on Monday")
	}
	if r := admin.get("/groups?day=sun"); found(r.body, "Early Risers") {
		t.Error("a Monday 12:30 am meeting after the change is on Sunday")
	}
}

// DEC-94: group lists sort by next meeting, groups with none last.
func TestGroupsSortByNextMeeting(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	for _, g := range []struct{ name, day string }{{"Alpha", "2026-12-10"}, {"Bravo", "2026-12-01"}, {"Charlie", ""}} {
		group := s.start(admin, g.name, "public")
		if g.day != "" {
			s.schedule(admin, group, eventForm(g.name, g.day, "19:00", "once"))
		}
	}
	for _, page := range []string{"/groups", "/"} {
		body := admin.get(page).body
		b, a, c := strings.Index(body, ">Bravo</a>"), strings.Index(body, ">Alpha</a>"), strings.Index(body, ">Charlie</a>")
		if b < 0 || !(b < a && a < c) {
			t.Errorf("%s is not by next meeting: Bravo %d, Alpha %d, Charlie %d", page, b, a, c)
		}
	}
}
