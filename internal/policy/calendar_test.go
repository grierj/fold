package policy

import (
	"testing"
	"time"
)

// Carried from Fold's TestDecideCalendar, without kept and household dates,
// and with guests and the window's lower edge as calendar.go says.
func TestInScope(t *testing.T) {
	now := time.Date(2026, 11, 25, 17, 0, 0, 0, time.UTC)
	at := func(d time.Duration) (time.Time, time.Time) { return now.Add(d), now.Add(d + time.Hour) }
	in7s, in7e := at(7 * 24 * time.Hour)
	// An outsider's public date of someone else's group, a week out.
	base := CalendarFacts{Seen: true, Viewer: ViewerMember, Public: true, StartsAt: in7s, EndsAt: in7e}
	with := func(change func(*CalendarFacts)) CalendarFacts {
		f := base
		change(&f)
		return f
	}
	far := func(f *CalendarFacts) { f.StartsAt, f.EndsAt = at(90 * 24 * time.Hour) }
	type want struct{ include, own bool }
	cases := []struct {
		name     string
		justMine bool
		f        CalendarFacts
		want     want
	}{
		{"seeing is the gate", false, CalendarFacts{Viewer: ViewerMember, InGroup: true, StartsAt: in7s, EndsAt: in7e}, want{}},
		{"seeing is the gate for an answer", false, with(func(f *CalendarFacts) { f.Seen = false; f.Answer = AnswerGoing }), want{}},

		// DEC-73: a public date is not an invitation until answered.
		{"an unanswered public date is not mine", true, base, want{}},
		{"an unanswered public date shows in Everything within the window", false, base, want{true, false}},
		{"an unanswered public date beyond the window is left out", false, with(far), want{}},
		{"the window's far edge is inclusive", false, with(func(f *CalendarFacts) { f.StartsAt, f.EndsAt = now.Add(Window), now.Add(Window+time.Hour) }), want{true, false}},
		{"one second past the far edge is out", false, with(func(f *CalendarFacts) { f.StartsAt, f.EndsAt = now.Add(Window+time.Second), now.Add(Window+time.Hour) }), want{}},
		{"an underway public date stays", false, with(func(f *CalendarFacts) { f.StartsAt, f.EndsAt = now.Add(-time.Hour), now.Add(time.Second) }), want{true, false}},
		{"an ended public date is out", false, with(func(f *CalendarFacts) { f.StartsAt, f.EndsAt = now.Add(-time.Hour), now }), want{}},
		{"going to a public date makes it mine", true, with(func(f *CalendarFacts) { f.Answer = AnswerGoing }), want{true, true}},
		{"maybe makes it mine", true, with(func(f *CalendarFacts) { f.Answer = AnswerMaybe }), want{true, true}},
		{"waiting makes it mine", true, with(func(f *CalendarFacts) { f.Answer = AnswerWaiting }), want{true, true}},
		{"an answer outlives the window", true, with(func(f *CalendarFacts) { far(f); f.Answer = AnswerMaybe }), want{true, true}},
		{"an answered past date stays", false, with(func(f *CalendarFacts) { f.StartsAt, f.EndsAt = at(-48 * time.Hour); f.Answer = AnswerGoing }), want{true, true}},
		{"a cancelled answer is no answer", true, with(func(f *CalendarFacts) { f.Answer = AnswerCancelled }), want{}},
		{"a cancelled answer keeps the window", false, with(func(f *CalendarFacts) { far(f); f.Answer = AnswerCancelled }), want{}},

		// DEC-71: Can't go leaves Just mine and stays, labelled, in Everything.
		{"can't go leaves Just mine", true, with(func(f *CalendarFacts) { f.Answer = AnswerCant }), want{false, true}},
		{"can't go stays in Everything", false, with(func(f *CalendarFacts) { far(f); f.Answer = AnswerCant }), want{true, true}},
		{"can't go leaves Just mine for my own group", true, with(func(f *CalendarFacts) { f.InGroup = true; f.Answer = AnswerCant }), want{false, true}},

		// My groups and the events I host are not limited.
		{"my group's date is mine however far", true, with(func(f *CalendarFacts) { far(f); f.InGroup = true }), want{true, true}},
		{"my group's past date stays", true, with(func(f *CalendarFacts) { f.StartsAt, f.EndsAt = at(-48 * time.Hour); f.InGroup = true }), want{true, true}},
		{"my group's private date is mine", true, with(func(f *CalendarFacts) { far(f); f.Public = false; f.InGroup = true }), want{true, true}},
		{"a hosted date is mine however far", true, with(func(f *CalendarFacts) { far(f); f.ChurchWide = true; f.Host = true }), want{true, true}},

		// DEC-72, DEC-77: church-wide dates invite every member, for 30 days.
		{"church-wide inside the window is mine", true, with(func(f *CalendarFacts) { f.ChurchWide = true; f.Public = false }), want{true, true}},
		{"church-wide beyond the window is left out of Just mine", true, with(func(f *CalendarFacts) { far(f); f.ChurchWide = true; f.Public = false }), want{false, true}},
		{"church-wide beyond the window is left out of Everything", false, with(func(f *CalendarFacts) { far(f); f.ChurchWide = true; f.Public = false }), want{false, true}},
		{"church-wide answered outlives the window", true, with(func(f *CalendarFacts) { far(f); f.ChurchWide = true; f.Answer = AnswerGoing }), want{true, true}},

		// Dates of other groups that are not public are not discovery rows.
		{"another group's church-only date shows in Everything", false, with(func(f *CalendarFacts) { far(f); f.Public = false }), want{true, false}},
		{"another group's church-only date is not mine", true, with(func(f *CalendarFacts) { f.Public = false }), want{}},

		// P7: a date opened wider is seen, not an invitation.
		{"an opened public date is a discovery row", false, with(func(f *CalendarFacts) { far(f) }), want{}},
		{"an opened date answered is mine", true, with(func(f *CalendarFacts) { far(f); f.Answer = AnswerGoing }), want{true, true}},

		// Guests: their own answers, and public dates as a visitor sees them.
		{"a guest's answered date is theirs", true, with(func(f *CalendarFacts) { far(f); f.Viewer = ViewerGuest; f.Answer = AnswerGoing }), want{true, true}},
		{"a guest's unanswered public date is in the window", false, with(func(f *CalendarFacts) { f.Viewer = ViewerGuest }), want{true, false}},
		{"a guest's unanswered public date is not theirs", true, with(func(f *CalendarFacts) { f.Viewer = ViewerGuest }), want{}},
		{"church-wide does not invite a guest", true, with(func(f *CalendarFacts) { f.Viewer = ViewerGuest; f.ChurchWide = true }), want{}},
		{"a guest's church-wide date is in the window", false, with(func(f *CalendarFacts) { f.Viewer = ViewerGuest; f.ChurchWide = true }), want{true, false}},
		{"a guest's church-wide date beyond it is out", false, with(func(f *CalendarFacts) { far(f); f.Viewer = ViewerGuest; f.ChurchWide = true }), want{}},
		{"a stray membership invites no guest", false, with(func(f *CalendarFacts) { far(f); f.Viewer = ViewerGuest; f.InGroup = true }), want{}},

		// Signed out: public dates in the window, and nothing is theirs (DEC-99).
		{"signed out sees a public date in the window", false, with(func(f *CalendarFacts) { f.Viewer = ViewerSignedOut }), want{true, false}},
		{"signed out, church-wide is not theirs", false, with(func(f *CalendarFacts) { f.Viewer = ViewerSignedOut; f.ChurchWide = true }), want{true, false}},
		{"signed out, beyond the window is out", false, with(func(f *CalendarFacts) { far(f); f.Viewer = ViewerSignedOut }), want{}},
	}
	for _, c := range cases {
		include, own := InScope(c.justMine, c.f, now)
		if (want{include, own}) != c.want {
			t.Errorf("%s: include %v own %v, want %+v", c.name, include, own, c.want)
		}
	}
}
