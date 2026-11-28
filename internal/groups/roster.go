package groups

import (
	"strconv"

	"context"
	"database/sql"
	"errors"
	"fold/internal/answers"

	"fold/internal/db"
	"fold/internal/notices"
	"fold/internal/people"
	"fold/internal/policy"
	"fold/internal/write"
)

// The roster: adding a person found by name (P6), co-leaders, removal.

// ErrUnchanged is a roster action that was already done (a second click):
// nothing changes, and the page says nothing happened rather than repeat
// its success.
var ErrUnchanged = errors.New("groups: already so")

// member loads a person by uid and their relation to the group, inside the
// write.
func member(tx *write.Tx, groupID int64, personUID string) (people.Person, policy.Rel, error) {
	var id int64
	err := tx.QueryRow(`SELECT id FROM people WHERE uid = ?`, personUID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return people.Person{}, policy.Rel{}, write.ErrNotAllowed
	}
	if err != nil {
		return people.Person{}, policy.Rel{}, err
	}
	p, err := people.ByID(tx.Context(), tx, id)
	if err != nil {
		return people.Person{}, policy.Rel{}, err
	}
	rel, err := RelOf(tx.Context(), tx, groupID, id)
	return p, rel, err
}

// Add is an admin putting a person straight into a group, without an
// invitation (R32). The person is told and can leave. Anyone else invites
// (accounts.InviteByName). A removed person may be added back (DEC-35).
type Add struct{ GroupUID, PersonUID string }

func (Add) Action() string { return "group.add" }

func (c Add) Apply(tx *write.Tx) error {
	g, _, err := target(tx, c.GroupUID)
	if err != nil {
		return err
	}
	if !policy.CanAddDirectly(actor(tx), g.Policy()) {
		return write.ErrNotAllowed
	}
	p, prel, err := member(tx, g.ID, c.PersonUID)
	if err != nil {
		return err
	}
	if p.Status != people.StatusActive {
		return write.ErrNotAllowed
	}
	if prel.Member {
		return ErrUnchanged
	}
	if err := addMembership(tx, g.ID, p.ID); err != nil {
		return err
	}
	// Any invitation waiting for them to this group is answered by this.
	if _, err := tx.Exec(`UPDATE invitations SET state = 'accepted', decided_at = ? WHERE group_id = ? AND person_id = ? AND state = 'open'`,
		tx.Now.Unix(), g.ID, p.ID); err != nil {
		return err
	}
	tx.Subject(g.UID)
	tx.Subject(p.UID)
	return notices.Add(tx, notices.New{PersonID: p.ID, Kind: notices.MemberAdded, ActorID: tx.Actor.Person.ID, GroupID: g.ID})
}

// AddMembership makes a person an active member, whatever row they had.
func AddMembership(tx *write.Tx, groupID, personID int64) error {
	return addMembership(tx, groupID, personID)
}

func addMembership(tx *write.Tx, groupID, personID int64) error {
	_, err := tx.Exec(`INSERT INTO memberships (group_id, person_id, role, state, created_at, updated_at) VALUES (?, ?, 'member', 'active', ?, ?)
		ON CONFLICT (group_id, person_id) DO UPDATE SET role = 'member', state = 'active', updated_at = excluded.updated_at`,
		groupID, personID, tx.Now.Unix(), tx.Now.Unix())
	return err
}

// SetRole makes a member a leader, or a leader a member again. A teen never
// leads, a group keeps at least one leader, and only an admin steps down
// an admin (DEC-18, DEC-35).
type SetRole struct {
	GroupUID, PersonUID string
	Leader              bool
}

func (c SetRole) Action() string {
	if c.Leader {
		return "group.make_leader"
	}
	return "group.make_member"
}

func (c SetRole) Apply(tx *write.Tx) error {
	g, rel, err := target(tx, c.GroupUID)
	if err != nil {
		return err
	}
	p, prel, err := member(tx, g.ID, c.PersonUID)
	if err != nil {
		return err
	}
	leaders, err := LeaderCount(tx.Context(), tx, g.ID)
	if err != nil {
		return err
	}
	if !policy.CanChangeRole(actor(tx), g.Policy(), rel, p.Policy(), prel, c.Leader, leaders) {
		return write.ErrNotAllowed
	}
	if prel.Leader == c.Leader {
		return ErrUnchanged
	}
	role := map[bool]string{true: "leader", false: "member"}[c.Leader]
	if _, err := tx.Exec(`UPDATE memberships SET role = ?, updated_at = ? WHERE group_id = ? AND person_id = ?`, role, tx.Now.Unix(), g.ID, p.ID); err != nil {
		return err
	}
	tx.Subject(g.UID)
	tx.Subject(p.UID)
	if c.Leader && p.ID != tx.Actor.Person.ID {
		return notices.Add(tx, notices.New{PersonID: p.ID, Kind: notices.MadeLeader, ActorID: tx.Actor.Person.ID, GroupID: g.ID})
	}
	return nil
}

// Remove takes someone off the roster. They cannot rejoin on their own
// (DEC-35); a leader may add them back.
type Remove struct{ GroupUID, PersonUID string }

func (Remove) Action() string { return "group.remove" }

func (c Remove) Apply(tx *write.Tx) error {
	g, rel, err := target(tx, c.GroupUID)
	if err != nil {
		return err
	}
	p, prel, err := member(tx, g.ID, c.PersonUID)
	if err != nil {
		return err
	}
	leaders, err := LeaderCount(tx.Context(), tx, g.ID)
	if err != nil {
		return err
	}
	if !policy.CanRemoveMember(actor(tx), g.Policy(), rel, p.Policy(), prel, leaders) {
		return write.ErrNotAllowed
	}
	if _, err := tx.Exec(`UPDATE memberships SET state = 'removed', role = 'member', updated_at = ? WHERE group_id = ? AND person_id = ?`,
		tx.Now.Unix(), g.ID, p.ID); err != nil {
		return err
	}
	n, err := answers.Withdraw(tx, g.ID, p.ID)
	if err != nil {
		return err
	}
	tx.Subject(g.UID)
	tx.Subject(p.UID)
	if n > 0 {
		tx.Detail("answers", strconv.Itoa(n))
	}
	return nil
}

// RosterRow is one member as the roster shows them.
type RosterRow struct {
	ID            int64
	UID           string
	Name          string
	Email         string
	ContactHidden bool
	Leader        bool
	Admin         bool
}

// Roster lists a group's active members, leaders first, then by name.
func Roster(ctx context.Context, q db.Querier, groupID int64) ([]RosterRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT p.id, p.uid, p.name, p.email, p.contact_hidden, am.role = 'leader', p.is_admin
		FROM active_memberships am JOIN people p ON p.id = am.person_id WHERE am.group_id = ?
		ORDER BY am.role = 'leader' DESC, p.name COLLATE NOCASE`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RosterRow
	for rows.Next() {
		var r RosterRow
		if err := rows.Scan(&r.ID, &r.UID, &r.Name, &r.Email, &r.ContactHidden, &r.Leader, &r.Admin); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Found is one person a name search found: a name, never contact details
// (DEC-78).
type Found struct{ UID, Name string }

// FindPeople searches active people by name who are not active members of
// the group. The caller has asked policy.CanSearchPeople.
func FindPeople(ctx context.Context, q db.Querier, groupID int64, name string, limit int) ([]Found, error) {
	rows, err := q.QueryContext(ctx, `SELECT p.uid, p.name FROM people p
		WHERE p.status = 'active' AND p.name LIKE ? ESCAPE '\'
		AND NOT EXISTS (SELECT 1 FROM active_memberships am WHERE am.group_id = ? AND am.person_id = p.id)
		ORDER BY p.name COLLATE NOCASE LIMIT ?`, "%"+escapeLike(name)+"%", groupID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Found
	for rows.Next() {
		var f Found
		if err := rows.Scan(&f.UID, &f.Name); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// LeadsActive reports whether a person leads any active group: what lets a
// leader search people and invite with no group named.
func LeadsActive(ctx context.Context, q db.Querier, personID int64) (bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT g.status FROM active_memberships am JOIN groups g ON g.id = am.group_id
		WHERE am.person_id = ? AND am.role = 'leader'`, personID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	var statuses []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		statuses = append(statuses, s)
	}
	return policy.LeadsApprovedGroup(statuses), rows.Err()
}
