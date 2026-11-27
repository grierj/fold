package accounts

import (
	"errors"

	"fold/internal/groups"
	"fold/internal/notices"
	"fold/internal/people"
	"fold/internal/policy"
	"fold/internal/write"
)

// Invite is a leader or an admin inviting someone by email (DEC-4). The
// person skips review: a new address gets an account at once, and an
// address waiting for review is approved. Both get an email with a sign-in
// link. Naming a group puts them in it. The inviter's right is checked
// here, when it is used (#155). The page gives one acknowledgement however
// the address stands, so inviting never tells a leader who has an account.
type Invite struct {
	GroupUID string // "" for no group
	Name     string
	Email    string
}

func (Invite) Action() string { return "account.invite" }

func (c Invite) Apply(tx *write.Tx) error {
	actor := tx.Actor.Person.Policy()
	var g groups.Group
	var target *policy.Group
	var leadsTarget bool
	if c.GroupUID != "" {
		var err error
		g, err = groups.ByUID(tx.Context(), tx, c.GroupUID)
		if errors.Is(err, groups.ErrNotFound) {
			return write.ErrNotAllowed
		}
		if err != nil {
			return err
		}
		rel, err := groups.RelOf(tx.Context(), tx, g.ID, actor.ID)
		if err != nil {
			return err
		}
		if !policy.CanSeeGroup(actor, g.Policy(), rel) {
			return write.ErrNotAllowed
		}
		pg := g.Policy()
		target, leadsTarget = &pg, rel.Leader
	}
	leadsAnywhere, err := groups.LeadsActive(tx.Context(), tx, actor.ID)
	if err != nil {
		return err
	}
	if !policy.CanInvite(actor, target, leadsTarget, leadsAnywhere) {
		return write.ErrNotAllowed
	}
	p, err := people.ByEmail(tx.Context(), tx, c.Email)
	outcome := ""
	switch {
	case errors.Is(err, people.ErrNotFound):
		uid := people.NewUID()
		res, err := tx.Exec(`INSERT INTO people (uid, name, email, status, created_at, updated_at) VALUES (?, ?, ?, 'active', ?, ?)`,
			uid, c.Name, c.Email, tx.Now.Unix(), tx.Now.Unix())
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		p = people.Person{ID: id, UID: uid, Status: people.StatusActive}
		outcome = "new"
	case err != nil:
		return err
	case p.Status == people.StatusPending:
		if _, err := tx.Exec(`UPDATE people SET status = 'active', updated_at = ?, version = version + 1 WHERE id = ?`, tx.Now.Unix(), p.ID); err != nil {
			return err
		}
		outcome = "approved"
	case p.Status == people.StatusActive:
		outcome = "existing"
	default:
		return write.ErrNothing // disabled, or the first admin mid-setup
	}
	tx.Subject(p.UID)
	tx.Detail("outcome", outcome)
	if target != nil {
		tx.Subject(g.UID)
		rel, err := groups.RelOf(tx.Context(), tx, g.ID, p.ID)
		if err != nil {
			return err
		}
		if !rel.Member {
			if err := groups.AddMembership(tx, g.ID, p.ID); err != nil {
				return err
			}
		}
	}
	n := notices.New{PersonID: p.ID, Kind: notices.Invited, ActorID: actor.ID}
	if target != nil {
		n.GroupID = g.ID
	}
	if outcome == "existing" {
		if target == nil {
			return write.ErrNothing // they already have an account; nothing to do
		}
		return notices.Add(tx, n)
	}
	if err := notices.Add(tx, n); err != nil {
		return err
	}
	_, err = issueLinkAs(tx, p.ID, "signin", JobInvitedMail)
	return err
}
