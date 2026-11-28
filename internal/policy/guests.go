package policy

// Guests (R15, R35, DEC-55, DEC-56). A guest is a signed-out visitor to
// every other rule: people.Person.Policy gives nil for one. These are the
// few things a guest may do besides.

// GuestCanAnswer: a guest says Going, and only Going (DEC-56), to a date a
// signed-out visitor may see, while it is on and has not ended.
func GuestCanAnswer(e Event, dateVisibility string, g Group, cancelled, ended bool) bool {
	return !cancelled && !ended && CanSeeDate(nil, e, dateVisibility, g, Rel{})
}

// GuestCanCancel: a guest takes back their own Going or place in line on a
// date that has not ended.
func GuestCanCancel(answer string, ended bool) bool {
	return !ended && MoveGuestCancel.Allows(answer, AnswerCancelled)
}

// Confirm outcomes: what opening a guest's confirm link does.
const (
	ConfirmDateGone = "date gone" // the date is no longer open to guests: nothing is answered
	ConfirmNew      = "new"       // no account: a guest is made, and answers
	ConfirmAsThem   = "as them"   // an account that may answer: it answers
	ConfirmRefused  = "refused"   // an account that may not: nothing is answered (R42)
)

// ConfirmGuest decides a confirm link (R35, R15). The date must still be
// open to guests. An address with no account becomes a guest; a guest
// answers as themself; an active member answers as themself where they may
// (removed from the group, they may not, R42); any other account (waiting,
// turned off, mid-setup) answers nothing.
func ConfirmGuest(dateOpen bool, status string, memberMay bool) string {
	switch {
	case !dateOpen:
		return ConfirmDateGone
	case status == "":
		return ConfirmNew
	case status == "guest", status == "active" && memberMay:
		return ConfirmAsThem
	}
	return ConfirmRefused
}

// ManageLink is what a manage link's page knows, read when it is used.
type ManageLink struct {
	Found   bool
	Revoked bool   // cancelled when its person cancelled, or by a restore
	Ended   bool   // the date, as it is now, has ended
	Visible bool   // a signed-out visitor may see the date now (HAZARDS 2)
	SignsIn bool   // the account is active or a guest
	Answer  string // the person's answer on the date now
}

// ManageLinkWorks: a manage link shows its date and a Cancel while all of
// these hold; otherwise it answers exactly as an unknown link (DEC-55).
func ManageLinkWorks(l ManageLink) bool {
	return l.Found && !l.Revoked && !l.Ended && l.Visible && l.SignsIn && (l.Answer == AnswerGoing || l.Answer == AnswerWaiting)
}
