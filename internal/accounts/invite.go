package accounts

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"fold/internal/db"
	"fold/internal/groups"
	"fold/internal/notices"
	"fold/internal/people"
	"fold/internal/policy"
	"fold/internal/write"
)

// Invitations (DEC-4, R32). Adding someone to a group, or inviting them by
// email, is an invitation they accept: nothing about the invited person
// changes until they do. That keeps an adult in charge of what they join,
// keeps their email off a roster they didn't choose (P5), keeps an inviter
// from learning whether an address has an account (docs/HAZARDS.md
// section 3), and lets the invited person, not the inviter, say they are an
// adult. The inviter's right is checked again when the invitation is used
// (#155).

const (
	// NameInviteLife is how long an invitation by name waits for an answer.
	NameInviteLife = 30 * 24 * time.Hour
	// JobInvitationMail emails an invitation. Its ref is the invitation and
	// its secret the link's token, erased when the job ends.
	JobInvitationMail = "mail.invitation"
)

var (
	// ErrInvited is an invitation by name to someone already invited.
	ErrInvited = errors.New("accounts: already invited")
	// ErrUnder18 is an invitation answered by someone under 18. Nothing is
	// created and the invitation ends.
	ErrUnder18 = errors.New("accounts: under 18")
)

// InviteByName invites an active person a leader found by name into a
// group (R32). They answer from their notice.
type InviteByName struct{ GroupUID, PersonUID string }

func (InviteByName) Action() string { return "group.invite" }

func (c InviteByName) Apply(tx *write.Tx) error {
	actor := tx.Actor.Person.Policy()
	g, rel, err := groupFor(tx, c.GroupUID, actor.ID)
	if err != nil {
		return err
	}
	if !policy.CanAddToGroup(actor, g.Policy(), rel) {
		return write.ErrNotAllowed
	}
	p, err := byUID(tx, c.PersonUID)
	if err != nil {
		return err
	}
	if p.Status != people.StatusActive {
		return write.ErrNotAllowed
	}
	prel, err := groups.RelOf(tx.Context(), tx, g.ID, p.ID)
	if err != nil {
		return err
	}
	if prel.Member {
		return groups.ErrUnchanged
	}
	var open int
	if err := tx.QueryRow(`SELECT count(*) FROM invitations WHERE group_id = ? AND person_id = ? AND state = 'open' AND expires_at > ?`,
		g.ID, p.ID, tx.Now.Unix()).Scan(&open); err != nil {
		return err
	}
	if open > 0 {
		return ErrInvited
	}
	if _, err := tx.Exec(`INSERT INTO invitations (uid, inviter_id, group_id, person_id, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
		people.NewUID(), actor.ID, g.ID, p.ID, tx.Now.Unix(), tx.Now.Add(NameInviteLife).Unix()); err != nil {
		return err
	}
	tx.Subject(g.UID)
	tx.Subject(p.UID)
	return notices.Add(tx, notices.New{PersonID: p.ID, Kind: notices.Invited, ActorID: actor.ID, GroupID: g.ID})
}

// InviteByEmail invites an address, into a group or none. The same thing
// happens whatever the address: an emailed link the person must open and
// accept.
type InviteByEmail struct {
	GroupUID string // "" for no group
	Name     string
	Email    string
}

func (InviteByEmail) Action() string { return "account.invite" }

func (c InviteByEmail) Apply(tx *write.Tx) error {
	actor := tx.Actor.Person.Policy()
	var groupID any
	var target *policy.Group
	var leadsTarget bool
	if c.GroupUID != "" {
		g, rel, err := groupFor(tx, c.GroupUID, actor.ID)
		if err != nil {
			return err
		}
		pg := g.Policy()
		target, leadsTarget, groupID = &pg, rel.Leader, g.ID
		tx.Subject(g.UID)
	}
	leadsAnywhere, err := groups.LeadsActive(tx.Context(), tx, actor.ID)
	if err != nil {
		return err
	}
	if !policy.CanInvite(actor, target, leadsTarget, leadsAnywhere) {
		return write.ErrNotAllowed
	}
	// A newer invitation to the same address and group replaces the older.
	rows, err := tx.Query(`SELECT id FROM invitations WHERE email = ? AND group_id IS ? AND state = 'open'`, c.Email, groupID)
	if err != nil {
		return err
	}
	var old []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		old = append(old, id)
	}
	rows.Close()
	for _, id := range old {
		if _, err := tx.Exec(`UPDATE invitations SET state = 'cancelled', decided_at = ? WHERE id = ?`, tx.Now.Unix(), id); err != nil {
			return err
		}
		if err := tx.CancelJobs(JobInvitationMail, id); err != nil {
			return err
		}
	}
	token := NewToken()
	res, err := tx.Exec(`INSERT INTO invitations (uid, inviter_id, group_id, name, email, token_hash, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		people.NewUID(), actor.ID, groupID, c.Name, c.Email, Hash(token), tx.Now.Unix(), tx.Now.Add(LinkLife).Unix())
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	return tx.Enqueue(write.NewJob{Kind: JobInvitationMail, Ref: id, Secret: []byte(token)})
}

func groupFor(tx *write.Tx, uid string, actorID int64) (groups.Group, policy.Rel, error) {
	g, err := groups.ByUID(tx.Context(), tx, uid)
	if errors.Is(err, groups.ErrNotFound) {
		return groups.Group{}, policy.Rel{}, write.ErrNotAllowed
	}
	if err != nil {
		return groups.Group{}, policy.Rel{}, err
	}
	rel, err := groups.RelOf(tx.Context(), tx, g.ID, actorID)
	return g, rel, err
}

// invitation is one row, as the commands need it.
type invitation struct {
	ID, InviterID     int64
	GroupID, PersonID sql.NullInt64
	Name, Email       sql.NullString
	State             string
	ExpiresAt         time.Time
}

func loadInvitation(ctx context.Context, q db.Querier, where string, arg any) (invitation, error) {
	var i invitation
	var expires int64
	err := q.QueryRowContext(ctx, `SELECT id, inviter_id, group_id, person_id, name, email, state, expires_at FROM invitations WHERE `+where, arg).
		Scan(&i.ID, &i.InviterID, &i.GroupID, &i.PersonID, &i.Name, &i.Email, &i.State, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return invitation{}, write.ErrNotAllowed
	}
	i.ExpiresAt = time.Unix(expires, 0)
	return i, err
}

func (i invitation) open(now time.Time) bool { return i.State == "open" && now.Before(i.ExpiresAt) }

// inviterStillMay checks, when the invitation is used, that the inviter may
// still invite into its group (#155): they are active and, for a group, it
// is active and they still lead it or are an admin.
func inviterStillMay(tx *write.Tx, i invitation) (*groups.Group, error) {
	inviter, err := people.ByID(tx.Context(), tx, i.InviterID)
	if err != nil {
		return nil, write.ErrNotAllowed
	}
	p := inviter.Policy()
	if !i.GroupID.Valid {
		leads, err := groups.LeadsActive(tx.Context(), tx, p.ID)
		if err != nil {
			return nil, err
		}
		if !policy.CanInvite(p, nil, false, leads) {
			return nil, write.ErrNotAllowed
		}
		return nil, nil
	}
	g, err := groups.ByID(tx.Context(), tx, i.GroupID.Int64)
	if err != nil {
		return nil, write.ErrNotAllowed
	}
	rel, err := groups.RelOf(tx.Context(), tx, g.ID, p.ID)
	if err != nil {
		return nil, err
	}
	if !policy.CanAddToGroup(p, g.Policy(), rel) || g.Status != policy.GroupActive {
		return nil, write.ErrNotAllowed
	}
	return &g, nil
}

func decide(tx *write.Tx, id int64, state string) error {
	_, err := tx.Exec(`UPDATE invitations SET state = ?, decided_at = ? WHERE id = ?`, state, tx.Now.Unix(), id)
	return err
}

// AnswerInvitation is the invited person joining, or saying no thanks, from
// an invitation by name.
type AnswerInvitation struct {
	UID    string
	Accept bool
	Group  string // set on success: the group's uid
}

func (c *AnswerInvitation) Action() string {
	if c.Accept {
		return "invitation.accept"
	}
	return "invitation.decline"
}

func (c *AnswerInvitation) Apply(tx *write.Tx) error {
	i, err := loadInvitation(tx.Context(), tx, `uid = ?`, c.UID)
	if err != nil {
		return err
	}
	me := tx.Actor.Person
	if !i.PersonID.Valid || i.PersonID.Int64 != me.ID || !i.open(tx.Now) {
		return write.ErrNotAllowed
	}
	if !c.Accept {
		return decide(tx, i.ID, "declined")
	}
	g, err := inviterStillMay(tx, i)
	if err != nil {
		return err
	}
	if err := groups.AddMembership(tx, g.ID, me.ID); err != nil {
		return err
	}
	c.Group = g.UID
	tx.Subject(g.UID)
	tx.Subject(me.UID)
	return decide(tx, i.ID, "accepted")
}

// RedeemInvitation is the page behind an emailed invitation: the person
// says they are an adult and accepts. A new address becomes an account, a
// waiting request is approved, an existing account joins; in every case
// the person is signed in, since the link proves the inbox is theirs.
type RedeemInvitation struct {
	Token        string
	Name         string
	Adult        bool
	SessionToken string // set on success
	Group        string // the group's uid, if any
}

func (*RedeemInvitation) Action() string { return "invitation.redeem" }

func (c *RedeemInvitation) Apply(tx *write.Tx) error {
	if !ValidTokenShape(c.Token) {
		return write.ErrNotAllowed
	}
	i, err := loadInvitation(tx.Context(), tx, `token_hash = ?`, Hash(c.Token))
	if err != nil {
		return err
	}
	if !i.Email.Valid || !i.open(tx.Now) {
		return write.ErrNotAllowed
	}
	if !c.Adult {
		// Accounts for under-18s open with slice 5. Nothing is created and
		// the invitation ends; the decline is the change this row records.
		if err := decide(tx, i.ID, "declined"); err != nil {
			return err
		}
		tx.Detail("outcome", "under18")
		return nil
	}
	g, err := inviterStillMay(tx, i)
	if err != nil {
		return err
	}
	p, err := people.ByEmail(tx.Context(), tx, i.Email.String)
	switch {
	case errors.Is(err, people.ErrNotFound):
		name := c.Name
		if name == "" {
			name = i.Name.String
		}
		uid := people.NewUID()
		res, err := tx.Exec(`INSERT INTO people (uid, name, email, status, created_at, updated_at) VALUES (?, ?, ?, 'active', ?, ?)`,
			uid, name, i.Email.String, tx.Now.Unix(), tx.Now.Unix())
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		p = people.Person{ID: id, UID: uid, Status: people.StatusActive}
	case err != nil:
		return err
	case p.Status == people.StatusPending:
		if _, err := tx.Exec(`UPDATE people SET status = 'active', updated_at = ?, version = version + 1 WHERE id = ?`, tx.Now.Unix(), p.ID); err != nil {
			return err
		}
	case p.Status != people.StatusActive:
		return write.ErrNotAllowed // turned off, or the first admin mid-setup
	}
	if g != nil {
		if err := groups.AddMembership(tx, g.ID, p.ID); err != nil {
			return err
		}
		c.Group = g.UID
		tx.Subject(g.UID)
	}
	tx.Subject(p.UID)
	if err := decide(tx, i.ID, "accepted"); err != nil {
		return err
	}
	c.SessionToken, err = startSession(tx, p.ID)
	return err
}

// Invitation is an open invitation as its page shows it.
type Invitation struct {
	UID, Inviter, GroupUID, GroupName, GroupAbout, Name, Church string
	Open                                                        bool
	ByEmail                                                     bool
}

// ByUID reads an invitation by name for its invited person; ok is false
// when it is not theirs.
func InvitationForPerson(ctx context.Context, q db.Querier, uid string, personID int64, now time.Time) (Invitation, bool, error) {
	i, err := loadInvitation(ctx, q, `uid = ?`, uid)
	if errors.Is(err, write.ErrNotAllowed) || (err == nil && (!i.PersonID.Valid || i.PersonID.Int64 != personID)) {
		return Invitation{}, false, nil
	}
	if err != nil {
		return Invitation{}, false, err
	}
	v, err := describe(ctx, q, i, now)
	v.UID = uid
	return v, true, err
}

// InvitationForToken reads an emailed invitation by its link's token.
func InvitationForToken(ctx context.Context, q db.Querier, token string, now time.Time) (Invitation, bool, error) {
	if !ValidTokenShape(token) {
		return Invitation{}, false, nil
	}
	i, err := loadInvitation(ctx, q, `token_hash = ?`, Hash(token))
	if errors.Is(err, write.ErrNotAllowed) || (err == nil && !i.Email.Valid) {
		return Invitation{}, false, nil
	}
	if err != nil {
		return Invitation{}, false, err
	}
	v, err := describe(ctx, q, i, now)
	v.ByEmail, v.Name = true, i.Name.String
	return v, true, err
}

func describe(ctx context.Context, q db.Querier, i invitation, now time.Time) (Invitation, error) {
	v := Invitation{Open: i.open(now)}
	q.QueryRowContext(ctx, `SELECT name FROM people WHERE id = ?`, i.InviterID).Scan(&v.Inviter)
	if i.GroupID.Valid {
		g, err := groups.ByID(ctx, q, i.GroupID.Int64)
		if err != nil {
			return v, err
		}
		v.GroupUID, v.GroupName, v.GroupAbout = g.UID, g.Name, g.About
		if g.Status != policy.GroupActive {
			v.Open = false
		}
	}
	return v, nil
}

// OpenByName lists the names of people with an open invitation by name to
// a group, for its roster.
func OpenByName(ctx context.Context, q db.Querier, groupID int64, now time.Time) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT p.name FROM invitations i JOIN people p ON p.id = i.person_id
		WHERE i.group_id = ? AND i.state = 'open' AND i.expires_at > ? ORDER BY p.name COLLATE NOCASE`, groupID, now.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		out = append(out, n)
	}
	return out, rows.Err()
}

func byUID(tx *write.Tx, uid string) (people.Person, error) {
	var id int64
	err := tx.QueryRow(`SELECT id FROM people WHERE uid = ?`, uid).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return people.Person{}, write.ErrNotAllowed
	}
	if err != nil {
		return people.Person{}, err
	}
	return people.ByID(tx.Context(), tx, id)
}
