package accounts

import (
	"errors"

	"fold/internal/people"
	"fold/internal/policy"
	"fold/internal/write"
)

// RequestSignIn sends a sign-in link to an active account. It runs from the
// sign-in queue, never from the request, so the form answers in the same
// time whether or not the address has an account (docs/HAZARDS.md section 3).
// An address with no active account changes nothing and writes no row.
type RequestSignIn struct {
	Email string
}

func (RequestSignIn) Action() string { return "signin.request" }

func (c RequestSignIn) Apply(tx *write.Tx) error {
	p, err := people.ByEmail(tx.Context(), tx, c.Email)
	if errors.Is(err, people.ErrNotFound) {
		return write.ErrNothing
	}
	if err != nil {
		return err
	}
	if !policy.SendSignInLink(policy.Account{Exists: true, Active: p.Status == people.StatusActive}) {
		return write.ErrNothing
	}
	tx.Subject(p.UID)
	_, err = issueLink(tx, p.ID, "signin")
	return err
}

// UseSignInLink is the sign-in page's button.
type UseSignInLink struct {
	Token        string
	SessionToken string // set on success
}

func (*UseSignInLink) Action() string { return "signin.link" }

func (c *UseSignInLink) Apply(tx *write.Tx) error {
	if !ValidTokenShape(c.Token) {
		return write.ErrNotAllowed
	}
	l, err := loadLink(tx.Context(), tx, c.Token)
	if err != nil {
		return err
	}
	if !l.facts.Found {
		return write.ErrNotAllowed
	}
	p, err := people.ByID(tx.Context(), tx, l.PersonID)
	if err != nil {
		return err
	}
	if !policy.SignIn(policy.Account{Exists: true, Active: p.Status == people.StatusActive}, l.facts, tx.Now) {
		return write.ErrNotAllowed
	}
	if err := useLink(tx, l.ID); err != nil {
		return err
	}
	tx.Subject(p.UID)
	c.SessionToken, err = startSession(tx, p.ID)
	return err
}

// RenewSession extends a session by a week from now (R8), at most hourly.
type RenewSession struct{}

func (RenewSession) Action() string { return "session.renew" }

func (RenewSession) Apply(tx *write.Tx) error {
	res, err := tx.Exec(`UPDATE sessions SET renewed_at = ?, expires_at = ? WHERE id = ? AND renewed_at <= ?`,
		tx.Now.Unix(), tx.Now.Add(SessionLife).Unix(), tx.Actor.SessionID, tx.Now.Add(-RenewEvery).Unix())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return write.ErrNothing
	}
	tx.Subject(tx.Actor.Person.UID)
	return nil
}

// SignOut ends every session of the account, so a copied cookie dies too
// (Fold M7.2).
type SignOut struct{}

func (SignOut) Action() string { return "session.end_all" }

func (SignOut) Apply(tx *write.Tx) error {
	if _, err := tx.Exec(`UPDATE sessions SET ended_at = ? WHERE person_id = ? AND ended_at IS NULL`,
		tx.Now.Unix(), tx.Actor.Person.ID); err != nil {
		return err
	}
	tx.Subject(tx.Actor.Person.UID)
	return nil
}
