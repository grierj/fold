// Package policy holds every permission as a pure function of facts the
// caller loaded. Handlers and commands load facts and ask; they never decide
// (AGENTS.md, "Every permission is a pure function").
package policy

import "time"

// Viewer is who is looking or acting, as loaded in the current transaction.
type Viewer struct {
	SignedIn bool
	Active   bool // the account is active (not waiting for setup, not disabled)
	Admin    bool
}

// Setup is what the database says about first-run setup.
type Setup struct {
	Done bool // a first admin exists
}

// StartSetup: whoever holds the setup code may save setup answers and send
// the test message, until setup is done.
func StartSetup(s Setup, codeVerified bool) bool {
	return !s.Done && codeVerified
}

// Link is what a link row says, read inside the write that would use it.
type Link struct {
	Found     bool
	Purpose   string
	Used      bool
	Cancelled bool
	ExpiresAt time.Time
}

// LinkUsable: a link works once, before it expires, unless a newer one
// replaced it.
func LinkUsable(l Link, purpose string, now time.Time) bool {
	return l.Found && l.Purpose == purpose && !l.Used && !l.Cancelled && now.Before(l.ExpiresAt)
}

// FinishSetup: the test message's link finishes setup only while setup is not
// done. Two people pressing it at once make one admin: the second finds setup
// done, inside the one writer.
func FinishSetup(s Setup, l Link, now time.Time) bool {
	return !s.Done && LinkUsable(l, "setup", now)
}

// LinkActsWhenOpened: an emailed link acts as soon as it is opened only in the
// browser that asked for it (R26). Anywhere else, a mail scanner included,
// it shows a page with a button, so a scanner cannot use it up.
func LinkActsWhenOpened(usable, sameDevice bool) bool { return usable && sameDevice }

// Account is the facts about the person a sign-in is for.
type Account struct {
	Exists bool
	Active bool
}

// SendSignInLink: only an active account gets a sign-in link. The form's
// answer is the same either way; this decides only whether mail goes.
func SendSignInLink(a Account) bool { return a.Exists && a.Active }

// SignIn: a sign-in link signs in an active account.
func SignIn(a Account, l Link, now time.Time) bool {
	return a.Exists && a.Active && LinkUsable(l, "signin", now)
}

// ViewGallery: the component gallery is for admins.
func ViewGallery(v Viewer) bool { return v.SignedIn && v.Active && v.Admin }
