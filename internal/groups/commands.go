package groups

import (
	"errors"

	"fold/internal/notices"
	"fold/internal/people"
	"fold/internal/policy"
	"fold/internal/write"
)

// Limits on what people type about a group.
const (
	MaxName  = 100
	MaxAbout = 2000
	MaxNote  = 500
)

// target loads a group and the actor's relation to it, inside the write,
// so policy is asked with facts no older than the change (#164).
func target(tx *write.Tx, uid string) (Group, policy.Rel, error) {
	g, err := ByUID(tx.Context(), tx, uid)
	if errors.Is(err, ErrNotFound) {
		return Group{}, policy.Rel{}, write.ErrNotAllowed
	}
	if err != nil {
		return Group{}, policy.Rel{}, err
	}
	var rel policy.Rel
	if tx.Actor.Person != nil {
		if rel, err = RelOf(tx.Context(), tx, g.ID, tx.Actor.Person.ID); err != nil {
			return Group{}, policy.Rel{}, err
		}
	}
	return g, rel, nil
}

func actor(tx *write.Tx) *policy.Person {
	if tx.Actor.Person == nil {
		return nil
	}
	return tx.Actor.Person.Policy()
}

// Create starts a group. An admin's is active at once; anyone else's waits
// for an admin, who gets a notice (DEC-96). The person who starts it leads
// it.
type Create struct {
	Name, About, Visibility string
	UID                     string // set on success
}

func (*Create) Action() string { return "group.create" }

// MaxWaiting is how many groups one person may have waiting for approval
// at once.
const MaxWaiting = 3

// ErrTooManyWaiting is a person who already has MaxWaiting groups waiting.
var ErrTooManyWaiting = errors.New("groups: too many waiting")

func (c *Create) Apply(tx *write.Tx) error {
	p := actor(tx)
	if !policy.CanCreateGroup(p) || !policy.ValidVisibility(c.Visibility) {
		return write.ErrNotAllowed
	}
	if !policy.IsAdmin(p) {
		var waiting int
		if err := tx.QueryRow(`SELECT count(*) FROM groups g JOIN memberships m ON m.group_id = g.id
			WHERE g.status = 'pending' AND m.person_id = ? AND m.role = 'leader'`, p.ID).Scan(&waiting); err != nil {
			return err
		}
		if waiting >= MaxWaiting {
			return ErrTooManyWaiting
		}
	}
	c.UID = people.NewUID()
	status := policy.NewGroupStatus(p)
	res, err := tx.Exec(`INSERT INTO groups (uid, name, about, visibility, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		c.UID, c.Name, c.About, c.Visibility, status, tx.Now.Unix(), tx.Now.Unix())
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	if _, err := tx.Exec(`INSERT INTO memberships (group_id, person_id, role, state, created_at, updated_at) VALUES (?, ?, 'leader', 'active', ?, ?)`,
		id, p.ID, tx.Now.Unix(), tx.Now.Unix()); err != nil {
		return err
	}
	tx.Subject(c.UID)
	tx.Detail("status", status)
	tx.Detail("visibility", c.Visibility)
	if status == policy.GroupPending {
		return notices.ToAdmins(tx, notices.New{Kind: notices.GroupRequested, ActorID: p.ID, GroupID: id})
	}
	return nil
}

// Decide is an admin approving or turning down a waiting group, with a
// reason and an optional note (R31). The group's leaders hear the answer;
// the note is kept in the audit log (R30).
type Decide struct {
	UID     string
	Approve bool
	Reason  string // a key of Reasons; required to turn a group down
	Note    string
}

func (c Decide) Action() string {
	if c.Approve {
		return "group.approve"
	}
	return "group.reject"
}

func (c Decide) Apply(tx *write.Tx) error {
	g, rel, err := target(tx, c.UID)
	if err != nil {
		return err
	}
	to, want := policy.GroupArchived, policy.GroupStatusReject
	if c.Approve {
		to, want = policy.GroupActive, policy.GroupStatusApprove
	}
	if policy.DecideGroupStatus(actor(tx), rel, g.Status, to) != want {
		return write.ErrNotAllowed
	}
	if _, ok := policy.GroupReasons[c.Reason]; c.Reason != "" && !ok {
		return write.ErrNotAllowed
	}
	if _, err := tx.Exec(`UPDATE groups SET status = ?, decision_reason = ?, decision_note = ?, updated_at = ?, version = version + 1 WHERE id = ?`,
		to, c.Reason, c.Note, tx.Now.Unix(), g.ID); err != nil {
		return err
	}
	tx.Subject(g.UID)
	if c.Reason != "" {
		tx.Detail("reason", c.Reason)
	}
	tx.Text(c.Note)
	kind := notices.GroupRejected
	if c.Approve {
		kind = notices.GroupApproved
	}
	return toLeaders(tx, g.ID, notices.New{Kind: kind, ActorID: tx.Actor.Person.ID, GroupID: g.ID})
}

func toLeaders(tx *write.Tx, groupID int64, n notices.New) error {
	return toMembers(tx, groupID, n, true)
}

func toMembers(tx *write.Tx, groupID int64, n notices.New, leadersOnly bool) error {
	q := `SELECT m.person_id, p.uid FROM memberships m JOIN people p ON p.id = m.person_id
		WHERE m.group_id = ? AND m.state = 'active' AND p.status = 'active'`
	if leadersOnly {
		q += ` AND m.role = 'leader'`
	}
	rows, err := tx.Query(q, groupID)
	if err != nil {
		return err
	}
	type recipient struct {
		id  int64
		uid string
	}
	var to []recipient
	for rows.Next() {
		var r recipient
		rows.Scan(&r.id, &r.uid)
		to = append(to, r)
	}
	rows.Close()
	for _, r := range to {
		if tx.Actor.Person != nil && r.id == tx.Actor.Person.ID {
			continue // nobody is told about what they did themselves
		}
		// The audit row names everyone told, so it shows who received any
		// words it keeps (R30).
		tx.Subject(r.uid)
		n.PersonID = r.id
		if err := notices.Add(tx, n); err != nil {
			return err
		}
	}
	return nil
}

// SetStatus archives or reactivates a group: a leader may archive their
// own active group; an admin may also bring an archived one back
// (DEC-160). Approving and turning down are Decide.
type SetStatus struct {
	UID string
	To  string
}

func (c SetStatus) Action() string {
	if c.To == policy.GroupArchived {
		return "group.archive"
	}
	return "group.unarchive"
}

func (c SetStatus) Apply(tx *write.Tx) error {
	g, rel, err := target(tx, c.UID)
	if err != nil {
		return err
	}
	switch policy.DecideGroupStatus(actor(tx), rel, g.Status, c.To) {
	case policy.GroupStatusArchive, policy.GroupStatusUnarchive:
	default:
		return write.ErrNotAllowed
	}
	if _, err := tx.Exec(`UPDATE groups SET status = ?, updated_at = ?, version = version + 1 WHERE id = ?`, c.To, tx.Now.Unix(), g.ID); err != nil {
		return err
	}
	tx.Subject(g.UID)
	return nil
}

// Edit saves a group's name, about and visibility. It never changes status
// or roles (#94, #165). The form carries the version it was opened with; if
// the group changed since, nothing is saved (P16). Making a group more
// private removes nobody (R3).
type Edit struct {
	UID                     string
	Name, About, Visibility string
	Version                 int64
}

func (Edit) Action() string { return "group.edit" }

func (c Edit) Apply(tx *write.Tx) error {
	g, rel, err := target(tx, c.UID)
	if err != nil {
		return err
	}
	if !policy.CanManageGroup(actor(tx), policy.Group{Status: g.Status, Visibility: g.Visibility}, rel) || !policy.ValidVisibility(c.Visibility) {
		return write.ErrNotAllowed
	}
	if g.Version != c.Version {
		return write.ErrConflict
	}
	if _, err := tx.Exec(`UPDATE groups SET name = ?, about = ?, visibility = ?, updated_at = ?, version = version + 1 WHERE id = ?`,
		c.Name, c.About, c.Visibility, tx.Now.Unix(), g.ID); err != nil {
		return err
	}
	tx.Subject(g.UID)
	if c.Visibility != g.Visibility {
		tx.Detail("visibility", c.Visibility)
		tx.Detail("was", g.Visibility)
	}
	return nil
}

// Delete is an admin deleting a group. Its members are told (DEC-177); the
// row stays, marked deleted, so that notice can name it, and nothing shows
// it again (R29).
type Delete struct{ UID string }

func (Delete) Action() string { return "group.delete" }

func (c Delete) Apply(tx *write.Tx) error {
	g, _, err := target(tx, c.UID)
	if err != nil {
		return err
	}
	if !policy.CanDeleteGroup(actor(tx), policy.Group{Status: g.Status}) {
		return write.ErrNotAllowed
	}
	if err := toMembers(tx, g.ID, notices.New{Kind: notices.GroupDeleted, ActorID: tx.Actor.Person.ID, GroupID: g.ID}, false); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE groups SET status = 'deleted', updated_at = ?, version = version + 1 WHERE id = ?`, tx.Now.Unix(), g.ID); err != nil {
		return err
	}
	tx.Subject(g.UID)
	return nil
}

// ErrAlreadyMember is a join by someone already in the group (a second
// click): nothing changes, and the page says so.
var ErrAlreadyMember = errors.New("groups: already a member")

// Join adds the actor to a group they may join (DEC-30). Someone who left
// may join again (DEC-86); someone a leader removed may not (DEC-35).
type Join struct{ UID string }

func (Join) Action() string { return "group.join" }

func (c Join) Apply(tx *write.Tx) error {
	g, rel, err := target(tx, c.UID)
	if err != nil {
		return err
	}
	if rel.Member {
		return ErrAlreadyMember
	}
	if !policy.CanJoinGroup(actor(tx), policy.Group{Status: g.Status, Visibility: g.Visibility}, rel) {
		return write.ErrNotAllowed
	}
	if _, err := tx.Exec(`INSERT INTO memberships (group_id, person_id, role, state, created_at, updated_at) VALUES (?, ?, 'member', 'active', ?, ?)
		ON CONFLICT (group_id, person_id) DO UPDATE SET role = 'member', state = 'active', updated_at = excluded.updated_at`,
		g.ID, tx.Actor.Person.ID, tx.Now.Unix(), tx.Now.Unix()); err != nil {
		return err
	}
	tx.Subject(g.UID)
	return nil
}

// Leave takes the actor out of a group. The last leader hands the group on
// first.
type Leave struct{ UID string }

func (Leave) Action() string { return "group.leave" }

func (c Leave) Apply(tx *write.Tx) error {
	g, rel, err := target(tx, c.UID)
	if err != nil {
		return err
	}
	leaders, err := LeaderCount(tx.Context(), tx, g.ID)
	if err != nil {
		return err
	}
	if !policy.CanLeaveGroup(rel, leaders) {
		return write.ErrNotAllowed
	}
	if _, err := tx.Exec(`UPDATE memberships SET state = 'left', role = 'member', updated_at = ? WHERE group_id = ? AND person_id = ?`,
		tx.Now.Unix(), g.ID, tx.Actor.Person.ID); err != nil {
		return err
	}
	tx.Subject(g.UID)
	tx.Detail("was", map[bool]string{true: "leader", false: "member"}[rel.Leader])
	return nil
}
