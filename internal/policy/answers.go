package policy

import "slices"

// Answers to dates. Carried from Fold (internal/policy/registration_lifecycle.go,
// CanRegisterEvent and CanSeeAttendeeNames at the pinned commit in
// docs/CARRYOVER.md) with its rules. Changes from Fold:
//   - "declined" is called cant, the button's own words.
//   - Being removed from a group takes answers back as cancelled, like
//     leaving; there is no separate removed state, so a removed person who
//     may still see a date may answer it again.
//   - Guests, household people, managers adding or removing an answer, and
//     the waitlist arrive in later milestones; their moves are not here yet.

// Answer states.
const (
	AnswerNone      = ""
	AnswerGoing     = "going"
	AnswerMaybe     = "maybe"
	AnswerCant      = "cant"
	AnswerCancelled = "cancelled" // taken back by the system, never chosen (DEC-54)
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
)

// given are a person's own answers: what leaving takes back.
var given = []string{AnswerGoing, AnswerMaybe, AnswerCant}

var answerMoves = map[AnswerMove]struct{ from, to []string }{
	MoveAnswer:     {from: []string{AnswerNone, AnswerGoing, AnswerMaybe, AnswerCant, AnswerCancelled}, to: []string{AnswerGoing, AnswerMaybe, AnswerCant}},
	MoveLeaveGroup: {from: given, to: []string{AnswerCancelled}},
}

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
