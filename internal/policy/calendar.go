package policy

import "time"

// The calendar's scope. Carried from Fold (internal/policy/calendar.go,
// DecideCalendar at the pinned commit in docs/CARRYOVER.md) with its rules
// (DEC-67, DEC-71, DEC-72, DEC-73, DEC-77). Changes from Fold:
//   - Dates kept after leaving (DEC-157) are left behind, as are household
//     and one-person dates, which arrive in later slices.
//   - A guest is their own kind of viewer: their answers are theirs, but no
//     church-wide date invites them (DEC-72 invites members).
//   - The window keeps a date until it ends, not until it starts, so one
//     that is underway stays on today.
//   - A cancelled answer, the system taking one back, counts as no answer.

// Window is how far ahead an unanswered church-wide or public date shows
// (DEC-77).
const Window = 30 * 24 * time.Hour

// Viewer kinds, for the calendar.
const (
	ViewerSignedOut = iota
	ViewerGuest
	ViewerMember // a signed-in account that is not a guest
)

// CalendarFacts are what the scope needs to know about one date for one
// viewer.
type CalendarFacts struct {
	Seen       bool // CanSeeDate (a guest is asked as a signed-out visitor)
	Viewer     int  // ViewerSignedOut, ViewerGuest or ViewerMember
	InGroup    bool // an active member of the date's group, leaders included
	ChurchWide bool
	Host       bool   // hosts this church-wide event
	Answer     string // the viewer's own answer
	Public     bool   // the date's visibility, the wider of its own and its event's, is public
	StartsAt   time.Time
	EndsAt     time.Time
}

// Answered: Going, Maybe, waiting or Can't go. A cancelled answer is none.
func Answered(answer string) bool {
	return answer == AnswerGoing || answer == AnswerMaybe || answer == AnswerWaiting || answer == AnswerCant
}

// InScope decides one date. include is whether the calendar lists it; own
// is whether it is in the viewer's own scope, where its answer is marked
// (DEC-71). Just mine is my groups, church-wide dates, events I host and
// dates I answered, but never a Can't go. Unanswered church-wide dates, and
// unanswered public dates of groups I am not in, show only within Window
// of now; the public ones only in Everything (DEC-73, DEC-77). Seeing is
// the gate: the scope never shows a date the viewer may not see.
func InScope(justMine bool, f CalendarFacts, now time.Time) (include, own bool) {
	if !f.Seen {
		return false, false
	}
	answered := Answered(f.Answer)
	standing := f.Viewer == ViewerMember && (f.InGroup || f.Host)
	own = answered || standing || (f.Viewer == ViewerMember && f.ChurchWide)
	switch {
	case justMine && (f.Answer == AnswerCant || !own):
		return false, own
	case answered || standing:
		return true, own
	case f.ChurchWide || (!justMine && f.Public):
		return f.EndsAt.After(now) && !f.StartsAt.After(now.Add(Window)), own
	}
	return true, own
}
