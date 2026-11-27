package accounts

import (
	"context"
	"database/sql"
	"errors"

	"fold/internal/db"
	"fold/internal/groups"
	"fold/internal/people"
	"fold/internal/policy"
	"fold/internal/write"
)

// Disable turns an account off. Its memberships stay, hidden and not
// counted, until it is turned on again (DEC-38). Every session ends and
// every link stops working, so nothing of it comes back on re-enabling.
// A group the person alone leads needs a successor first (DEC-36), named in
// Successors by group uid.
type Disable struct {
	PersonUID  string
	Successors map[string]string // group uid -> successor's person uid
}

func (Disable) Action() string { return "account.disable" }

func (c Disable) Apply(tx *write.Tx) error {
	target, err := byUID(tx, c.PersonUID)
	if err != nil {
		return err
	}
	if target.Status != people.StatusActive {
		return write.ErrNotAllowed
	}
	var admins int
	if err := tx.QueryRow(`SELECT count(*) FROM people WHERE is_admin = 1 AND status = 'active'`).Scan(&admins); err != nil {
		return err
	}
	sole, err := SoleLed(tx.Context(), tx, target.ID)
	if err != nil {
		return err
	}
	type handover struct {
		group     groups.Group
		successor people.Person
	}
	var handovers []handover
	leaderless := 0
	for _, g := range sole {
		uid, named := c.Successors[g.UID]
		if !named {
			leaderless++
			continue
		}
		s, err := byUID(tx, uid)
		if err != nil {
			return err
		}
		rel, err := groups.RelOf(tx.Context(), tx, g.ID, s.ID)
		if err != nil {
			return err
		}
		if s.ID == target.ID || !policy.CanBeSuccessor(s.Policy(), rel) {
			return write.ErrNotAllowed
		}
		handovers = append(handovers, handover{g, s})
	}
	if !policy.CanDisable(tx.Actor.Person.Policy(), target.Policy(), admins, leaderless) {
		return write.ErrNotAllowed
	}
	for _, h := range handovers {
		if _, err := tx.Exec(`INSERT INTO memberships (group_id, person_id, role, state, created_at, updated_at) VALUES (?, ?, 'leader', 'active', ?, ?)
			ON CONFLICT (group_id, person_id) DO UPDATE SET role = 'leader', state = 'active', updated_at = excluded.updated_at`,
			h.group.ID, h.successor.ID, tx.Now.Unix(), tx.Now.Unix()); err != nil {
			return err
		}
		tx.Subject(h.group.UID)
		tx.Subject(h.successor.UID)
	}
	now := tx.Now.Unix()
	for _, q := range []string{
		`UPDATE people SET status = 'disabled', updated_at = ?1, version = version + 1 WHERE id = ?2`,
		`UPDATE sessions SET ended_at = ?1 WHERE person_id = ?2 AND ended_at IS NULL`,
		`UPDATE links SET cancelled_at = ?1 WHERE person_id = ?2 AND used_at IS NULL AND cancelled_at IS NULL`,
	} {
		if _, err := tx.Exec(q, now, target.ID); err != nil {
			return err
		}
	}
	tx.Subject(target.UID)
	return nil
}

// Enable turns an account on again. Its memberships come back with it,
// since no group has a member limit yet (DEC-47 applies once one does).
type Enable struct{ PersonUID string }

func (Enable) Action() string { return "account.enable" }

func (c Enable) Apply(tx *write.Tx) error {
	if !policy.IsAdmin(tx.Actor.Person.Policy()) {
		return write.ErrNotAllowed
	}
	target, err := byUID(tx, c.PersonUID)
	if err != nil {
		return err
	}
	if target.Status != people.StatusDisabled {
		return write.ErrNotAllowed
	}
	if _, err := tx.Exec(`UPDATE people SET status = 'active', updated_at = ?, version = version + 1 WHERE id = ?`, tx.Now.Unix(), target.ID); err != nil {
		return err
	}
	tx.Subject(target.UID)
	return nil
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

// SoleLed lists the groups (not deleted) a person is the only active leader
// of: the ones that need a successor before the person is disabled.
func SoleLed(ctx context.Context, q db.Querier, personID int64) ([]groups.Group, error) {
	rows, err := q.QueryContext(ctx, `SELECT g.uid FROM active_memberships am JOIN groups g ON g.id = am.group_id
		WHERE am.person_id = ? AND am.role = 'leader'
		AND (SELECT count(*) FROM active_memberships x WHERE x.group_id = g.id AND x.role = 'leader') = 1
		ORDER BY g.name COLLATE NOCASE`, personID)
	if err != nil {
		return nil, err
	}
	var uids []string
	for rows.Next() {
		var u string
		rows.Scan(&u)
		uids = append(uids, u)
	}
	rows.Close()
	var out []groups.Group
	for _, u := range uids {
		g, err := groups.ByUID(ctx, q, u)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, nil
}

// EditName saves a person's own name, checking the version the form was
// opened with (P16).
type EditName struct {
	Name    string
	Version int64
}

func (EditName) Action() string { return "profile.name" }

func (c EditName) Apply(tx *write.Tx) error {
	p := tx.Actor.Person
	if p.Version != c.Version {
		return write.ErrConflict
	}
	if _, err := tx.Exec(`UPDATE people SET name = ?, updated_at = ?, version = version + 1 WHERE id = ?`, c.Name, tx.Now.Unix(), p.ID); err != nil {
		return err
	}
	tx.Subject(p.UID)
	return nil
}

// SetContactHidden is the person's one switch for whether leaders of their
// groups see their email (P5). It saves when changed (DEC-98); the last
// change wins.
type SetContactHidden struct{ Hidden bool }

func (SetContactHidden) Action() string { return "profile.contact" }

func (c SetContactHidden) Apply(tx *write.Tx) error {
	p := tx.Actor.Person
	if p.ContactHidden == c.Hidden {
		return write.ErrNothing
	}
	if _, err := tx.Exec(`UPDATE people SET contact_hidden = ?, updated_at = ? WHERE id = ?`, c.Hidden, tx.Now.Unix(), p.ID); err != nil {
		return err
	}
	tx.Subject(p.UID)
	tx.Detail("hidden", map[bool]string{true: "1", false: "0"}[c.Hidden])
	return nil
}
