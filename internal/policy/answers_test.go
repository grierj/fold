package policy

import (
	"slices"
	"testing"
	"time"
)

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
		{MoveAnswer, AnswerGoing, AnswerWaiting, true}, // the write found no seat
		{MoveAnswer, AnswerWaiting, AnswerCant, true},  // leaving the waitlist is an answer
		{MoveLeaveGroup, AnswerWaiting, AnswerCancelled, true},
		{MovePromote, AnswerWaiting, AnswerGoing, true},
		{MovePromote, AnswerMaybe, AnswerGoing, false},
		{MovePromote, AnswerNone, AnswerGoing, false},
		{MoveRemove, AnswerGoing, AnswerCancelled, true},
		{MoveRemove, AnswerWaiting, AnswerCancelled, true},
		{MoveRemove, AnswerCant, AnswerCancelled, true},
		{MoveRemove, AnswerCancelled, AnswerCancelled, false},
		{MoveRemove, AnswerNone, AnswerCancelled, false},
		{MoveNoRoom, AnswerGoing, AnswerCancelled, true},
		{MoveNoRoom, AnswerMaybe, AnswerCancelled, false},
		{MoveNoRoom, AnswerWaiting, AnswerCancelled, false},
		{MoveLeaveGroup, AnswerGoing, AnswerCancelled, true},
		{MoveLeaveGroup, AnswerMaybe, AnswerCancelled, true},
		{MoveLeaveGroup, AnswerCant, AnswerCancelled, true},
		{MoveLeaveGroup, AnswerNone, AnswerCancelled, false},
		{MoveLeaveGroup, AnswerCancelled, AnswerCancelled, false},
		{"unknown", AnswerNone, AnswerGoing, false},
	}
	for _, c := range cases {
		if got := c.m.Allows(c.from, c.to); got != c.want {
			t.Errorf("%s %q to %q: got %v", c.m, c.from, c.to, got)
		}
	}
	if !IsAnswer(AnswerCant) || IsAnswer(AnswerCancelled) || IsAnswer(AnswerWaiting) || IsAnswer("") {
		t.Error("only going, maybe and can't go are chosen")
	}
}

// DEC-84, HAZARDS 7: who moves up, given the limit, how many are going and
// the queue in order.
func TestPromote(t *testing.T) {
	q := []int64{7, 3, 9}
	cases := []struct {
		name         string
		seats, going int
		queue        []int64
		want         []int64
	}{
		{"one seat freed", 5, 4, q, []int64{7}},
		{"two seats freed", 5, 3, q, []int64{7, 3}},
		{"more seats than waiting", 10, 2, q, q},
		{"still full", 5, 5, q, nil},
		{"a limit lowered below the going count frees nothing", 3, 5, q, nil},
		{"no limit takes everyone waiting", 0, 40, q, q},
		{"nobody waiting", 5, 1, nil, nil},
	}
	for _, c := range cases {
		got := Promote(c.seats, c.going, c.queue)
		if !slices.Equal(got, c.want) && !(len(got) == 0 && len(c.want) == 0) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	if got := Promote(5, 4, q); &got[0] == &q[0] {
		t.Error("Promote hands back the caller's slice")
	}
}

func TestHasSeat(t *testing.T) {
	for _, c := range []struct {
		seats, others int
		want          bool
	}{{0, 99, true}, {5, 4, true}, {5, 5, false}, {3, 5, false}, {1, 0, true}} {
		if got := HasSeat(c.seats, c.others); got != c.want {
			t.Errorf("HasSeat(%d, %d) = %v", c.seats, c.others, got)
		}
	}
}

// Fold's 5.4: no moving up within two hours of the start, or on a date that
// is cancelled, has begun, or whose event is deleted.
func TestCanPromote(t *testing.T) {
	now := time.Date(2026, 12, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name              string
		active, cancelled bool
		starts            time.Time
		want              bool
	}{
		{"a day ahead", true, false, now.Add(24 * time.Hour), true},
		{"just outside the cutoff", true, false, now.Add(Cutoff + time.Minute), true},
		{"at the cutoff", true, false, now.Add(Cutoff), false},
		{"inside the cutoff", true, false, now.Add(time.Hour), false},
		{"begun", true, false, now.Add(-time.Minute), false},
		{"cancelled", true, true, now.Add(24 * time.Hour), false},
		{"its event deleted", false, false, now.Add(24 * time.Hour), false},
	}
	for _, c := range cases {
		if got := CanPromote(c.active, c.cancelled, c.starts, now); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}

func TestUrgentAndRemove(t *testing.T) {
	if !Urgent(AnswerGoing) || !Urgent(AnswerMaybe) || Urgent(AnswerWaiting) || Urgent(AnswerCant) {
		t.Error("R36: only Going and Maybe are emailed when a date goes away")
	}
	if !CanRemoveAnswer(true, false) || CanRemoveAnswer(false, false) || CanRemoveAnswer(true, true) {
		t.Error("only those running the event remove, and never from a date that has begun")
	}
	if !ValidSeats(0) || !ValidSeats(MaxSeats) || ValidSeats(-1) || ValidSeats(MaxSeats+1) {
		t.Error("seat limits")
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
