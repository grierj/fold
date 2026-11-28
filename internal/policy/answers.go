package policy

import (
	"slices"
	"time"
)

// Answers to dates. Carried from Fold (internal/policy/registration_lifecycle.go,
// CanRegisterEvent and CanSeeAttendeeNames at the pinned commit in
// docs/CARRYOVER.md) with its rules. Changes from Fold:
//   - "declined" is called cant, the button's own words.
//   - Being removed from a group takes answers back as cancelled, like
//     leaving; there is no separate removed state, so a removed person who
//     may still see a date may answer it again.
//   - A leader removing someone's answer takes it back as cancelled, like
//     leaving; there is no removed state, so they may answer again while
//     they may see the date.
//   - Parties are one person (R37), so a waiting party always fits a free
//     seat and nobody is skipped.
//   - Inside the two-hour cutoff nobody moves up; Fold's notice to the
//     leaders and their one-click promote are not built.
//   - Guests, household people and a manager adding someone arrive in later
//     milestones; their moves are not here yet.

// Answer states.
const (
	AnswerNone      = ""
	AnswerGoing     = "going"
	AnswerMaybe     = "maybe"
	AnswerCant      = "cant"
	AnswerCancelled = "cancelled" // taken back by the system, never chosen (DEC-54)
	// AnswerWaiting is Going on a full date: never chosen, only given by
	// the write that finds no seat.
	AnswerWaiting = "waiting"
)

// IsAnswer reports whether a person may choose this answer for themselves.
func IsAnswer(s string) bool { return s == AnswerGoing || s == AnswerMaybe || s == AnswerCant }

// AnswerMove is one way an answer changes, named by what causes it. The
// table below is the whole machine; package answers writes every move.
type AnswerMove string

const (
	// A person's own answer, for one date or across a series.
	MoveAnswer AnswerMove = "answer"
	// A person leaves a group, or a leader or admin removes them: their
	// answers on its dates still to come are taken back, Can't go included,
	// so the people running it no longer see them listed (DEC-74).
	MoveLeaveGroup AnswerMove = "leave the group"
	// A seat frees and the earliest waiting person takes it (DEC-84).
	MovePromote AnswerMove = "move up from the waitlist"
	// Those running the event take someone off a date, or off all of its
	// dates still to come (DEC-84, R3).
	MoveRemove AnswerMove = "remove from a date"
	// An account turned on again finds a date it said Going to full: that
	// answer is dropped and the person told to ask a leader (DEC-47).
	MoveNoRoom AnswerMove = "no room on turning back on"
	// A guest takes back their own Going, or their place on the waitlist,
	// from the date page or their manage link (DEC-55).
	MoveGuestCancel AnswerMove = "guest cancels"
)

// given are a person's answers that stand: what leaving takes back.
var given = []string{AnswerGoing, AnswerMaybe, AnswerCant, AnswerWaiting}

var answerMoves = map[AnswerMove]struct{ from, to []string }{
	MoveAnswer: {from: []string{AnswerNone, AnswerGoing, AnswerMaybe, AnswerCant, AnswerCancelled, AnswerWaiting},
		to: []string{AnswerGoing, AnswerMaybe, AnswerCant, AnswerWaiting}},
	MoveLeaveGroup:  {from: given, to: []string{AnswerCancelled}},
	MovePromote:     {from: []string{AnswerWaiting}, to: []string{AnswerGoing}},
	MoveRemove:      {from: given, to: []string{AnswerCancelled}},
	MoveNoRoom:      {from: []string{AnswerGoing}, to: []string{AnswerCancelled}},
	MoveGuestCancel: {from: []string{AnswerGoing, AnswerWaiting}, to: []string{AnswerCancelled}},
}

// SignedUp are the answers whose people are told when a date goes away or
// comes back: Going, Maybe and waiting. Can't go has nothing to lose.
var SignedUp = []string{AnswerGoing, AnswerMaybe, AnswerWaiting}

// Urgent: until email choices exist, a date going away is emailed only to
// people who said Going or Maybe to it (R36). The bell tells everyone.
func Urgent(answer string) bool { return answer == AnswerGoing || answer == AnswerMaybe }

// Cutoff is how close to its start a date stops moving people up (Fold's
// 5.4): a seat freed later goes to whoever says Going next.
const Cutoff = 2 * time.Hour

// CanPromote: a freed seat moves the waitlist up only on a date that is on,
// in an event not deleted, more than Cutoff before it starts.
func CanPromote(eventActive, cancelled bool, startsAt, now time.Time) bool {
	return eventActive && !cancelled && now.Before(startsAt.Add(-Cutoff))
}

// HasSeat: a Going fits when the date has no limit (seats 0) or fewer than
// seats others are going. Someone already going keeps their seat whatever
// the limit became; the caller does not ask for them.
func HasSeat(seats, others int) bool { return seats == 0 || others < seats }

// Promote is who moves up: the earliest of queue, one per free seat. A
// limit lowered below how many are going frees nothing; no limit takes
// everyone waiting.
func Promote(seats, going int, queue []int64) []int64 {
	if seats == 0 {
		return slices.Clone(queue)
	}
	free := max(seats-going, 0)
	return slices.Clone(queue[:min(free, len(queue))])
}

// ValidSeats: a seat limit is a whole number of people, or 0 for none.
const MaxSeats = 10000

func ValidSeats(n int) bool { return n >= 0 && n <= MaxSeats }

// CanRemoveAnswer: those running the event take someone off a date that
// has not begun. A date that has begun is history.
func CanRemoveAnswer(manages, started bool) bool { return manages && !started }

// From is the states a move starts from.
func (m AnswerMove) From() []string { return slices.Clone(answerMoves[m].from) }

// Allows: the move may take an answer from one state (AnswerNone: none yet)
// to another.
func (m AnswerMove) Allows(from, to string) bool {
	r, ok := answerMoves[m]
	return ok && slices.Contains(r.from, from) && slices.Contains(r.to, to)
}

// CanAnswer: a signed-in, active person answers a date they may see, while
// it is on and has not ended. That covers a group's members, every member
// for a church-wide event (DEC-72), anyone signed in for a public date
// (DEC-73), and anyone a date was opened to (P7). It adds nobody to a group.
// Someone removed from the group answers none of its dates, public ones
// included (R42).
func CanAnswer(p *Person, e Event, dateVisibility string, g Group, r Rel, cancelled, ended bool) bool {
	return p != nil && !p.Inactive && !r.Removed && !cancelled && !ended && CanSeeDate(p, e, dateVisibility, g, r)
}

// CanSeeAttendeeNames: those running the event see names. Otherwise anyone
// signed in who may see the date does, except that people outside an
// invite-only group see only counts for its date opened wider (DEC-48,
// DEC-63). Signed-out visitors see counts only.
func CanSeeAttendeeNames(p *Person, e Event, dateVisibility string, g Group, r Rel) bool {
	if p == nil || !CanSeeDate(p, e, dateVisibility, g, r) {
		return false
	}
	if CanManageEvent(p, e, g, r) || IsAdmin(p) || r.Leader {
		return true
	}
	return e.ChurchWide || g.Visibility != InviteOnly || r.Member
}

// CanSeeCantGo: only those running the event see who can't go, or how many
// (DEC-64). Everyone else gets nothing about it at all.
func CanSeeCantGo(p *Person, e Event, g Group, r Rel) bool { return CanManageEvent(p, e, g, r) }

// CanSeeVideo: an event's video link is for those running it and for people
// who said Going or Maybe to the date (R41). Seeing the date is not enough.
func CanSeeVideo(manages bool, answer string) bool {
	return manages || answer == AnswerGoing || answer == AnswerMaybe
}

// SeriesScopes: a series date may be answered just for itself, for it and
// the dates after, or for all dates still to come (DEC-71). Dates added
// later are not answered (DEC-92).
const (
	ScopeDate      = "date"
	ScopeFollowing = "following"
	ScopeAll       = "all"
)

func ValidScope(s string) bool { return s == ScopeDate || s == ScopeFollowing || s == ScopeAll }

// CanAnswerSeries: offering a series answer reveals the series, so only
// someone who may see the event, not just an opened date, is offered it.
func CanAnswerSeries(p *Person, e Event, g Group, r Rel, repeats bool) bool {
	return repeats && CanSeeEvent(p, e, g, r)
}
