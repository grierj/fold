package policy

import "testing"

func TestAnswerMoves(t *testing.T) {
	cases := []struct {
		m        AnswerMove
		from, to string
		want     bool
	}{
		{MoveAnswer, AnswerNone, AnswerGoing, true},
		{MoveAnswer, AnswerCant, AnswerMaybe, true},
		{MoveAnswer, AnswerCancelled, AnswerGoing, true}, // rejoined, answering again
		{MoveAnswer, AnswerGoing, AnswerCancelled, false},
		{MoveAnswer, AnswerGoing, AnswerNone, false},
		{MoveAnswer, AnswerGoing, "waiting", false},
		{MoveLeaveGroup, AnswerGoing, AnswerCancelled, true},
		{MoveLeaveGroup, AnswerMaybe, AnswerCancelled, true},
		{MoveLeaveGroup, AnswerCant, AnswerCancelled, false}, // history stays
		{MoveLeaveGroup, AnswerNone, AnswerCancelled, false},
		{"unknown", AnswerNone, AnswerGoing, false},
	}
	for _, c := range cases {
		if got := c.m.Allows(c.from, c.to); got != c.want {
			t.Errorf("%s %q to %q: got %v", c.m, c.from, c.to, got)
		}
	}
	if !IsAnswer(AnswerCant) || IsAnswer(AnswerCancelled) || IsAnswer("waiting") || IsAnswer("") {
		t.Error("only going, maybe and can't go are chosen")
	}
}

func TestCanAnswer(t *testing.T) {
	cases := []struct {
		name             string
		p                *Person
		e                Event
		dvis             string
		g                Group
		r                Rel
		cancelled, ended bool
		want             bool
	}{
		{"a member of the group", adult, event(Church), "", active(Church), member, false, false, true},
		{"a member, invite-only event", adult, event(InviteOnly), "", active(InviteOnly), member, false, false, true},
		{"every member, church-wide (DEC-72)", adult, churchEvent(Church, false), "", Group{}, none, false, false, true},
		{"anyone signed in, public date (DEC-73)", adult, event(Public), "", active(Public), none, false, false, true},
		{"a non-member on a church-only date they can see", adult, event(Church), "", active(Church), none, false, false, true},
		{"a non-member admin", admin, event(InviteOnly), "", active(InviteOnly), none, false, false, true},
		{"an outsider on a date opened to the church", adult, event(InviteOnly), Church, active(InviteOnly), none, false, false, true},
		{"an outsider on an invite-only date", adult, event(InviteOnly), "", active(InviteOnly), none, false, false, false},
		{"someone removed from the group, public date (R42)", adult, event(Public), "", active(Public), Rel{Removed: true}, false, false, false},
		{"someone removed, date opened to the church (R42)", adult, event(InviteOnly), Church, active(InviteOnly), Rel{Removed: true}, false, false, false},
		{"signed out, even public (guests: milestone 3)", guest, event(Public), "", active(Public), none, false, false, false},
		{"a disabled account", &Person{ID: 9, Inactive: true}, event(Public), "", active(Public), member, false, false, false},
		{"a cancelled date", adult, event(Public), "", active(Public), member, true, false, false},
		{"an ended date", adult, event(Public), "", active(Public), member, false, true, false},
		{"an archived group's date", adult, event(Public), "", archived, member, false, false, false},
		{"a deleted event", adult, Event{Status: EventDeleted, Visibility: Public}, "", active(Public), member, false, false, false},
	}
	for _, c := range cases {
		if got := CanAnswer(c.p, c.e, c.dvis, c.g, c.r, c.cancelled, c.ended); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}

func TestWhoSeesWhoIsComing(t *testing.T) {
	cases := []struct {
		name         string
		p            *Person
		e            Event
		dvis         string
		g            Group
		r            Rel
		names, cants bool
	}{
		{"a member sees names, not can't go", adult, event(Church), "", active(Church), member, true, false},
		{"a leader sees both (DEC-64)", adult, event(Church), "", active(Church), leader, true, true},
		{"an admin sees both", admin, event(Church), "", active(Church), none, true, true},
		{"a host sees both", adult, churchEvent(Church, true), "", Group{}, none, true, true},
		{"a member sees a church-wide event's names", adult, churchEvent(Church, false), "", Group{}, none, true, false},
		{"anyone signed in sees a public group's names", adult, event(Public), "", active(Public), none, true, false},
		{"an outsider of an invite-only group sees counts only (DEC-48)", adult, event(Public), "", active(InviteOnly), none, false, false},
		{"and on its date opened wider", adult, event(InviteOnly), Public, active(InviteOnly), none, false, false},
		{"a member of that group sees names", adult, event(Public), "", active(InviteOnly), member, true, false},
		{"signed out sees counts only", guest, event(Public), "", active(Public), none, false, false},
		{"nobody who can't see the date", adult, event(InviteOnly), "", active(InviteOnly), none, false, false},
	}
	for _, c := range cases {
		if got := CanSeeAttendeeNames(c.p, c.e, c.dvis, c.g, c.r); got != c.names {
			t.Errorf("names, %s: got %v", c.name, got)
		}
		if got := CanSeeCantGo(c.p, c.e, c.g, c.r); got != c.cants {
			t.Errorf("can't go, %s: got %v", c.name, got)
		}
	}
}

func TestVideoAndSeries(t *testing.T) {
	if !CanSeeVideo(true, AnswerNone) || !CanSeeVideo(false, AnswerGoing) || !CanSeeVideo(false, AnswerMaybe) ||
		CanSeeVideo(false, AnswerCant) || CanSeeVideo(false, AnswerCancelled) || CanSeeVideo(false, AnswerNone) {
		t.Error("video: those running it, and Going or Maybe (R41)")
	}
	if !CanAnswerSeries(adult, event(Church), active(Church), member, true) || CanAnswerSeries(adult, event(Church), active(Church), member, false) ||
		CanAnswerSeries(adult, event(InviteOnly), active(InviteOnly), none, true) {
		t.Error("series answers: a repeating event the person may see as a whole")
	}
	if !ValidScope(ScopeFollowing) || ValidScope("") || ValidScope("forever") {
		t.Error("scopes")
	}
}
