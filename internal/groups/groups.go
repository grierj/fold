// Package groups reads groups and memberships for pages and commands.
package groups

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"fold/internal/db"
	"fold/internal/policy"
)

// RelOf is a person's relation to a group, read from the membership row
// itself so a removal is seen (DEC-35). A membership counts as active only
// while its person is active and its group is not deleted, the same three
// conditions as the active_memberships view.
func RelOf(ctx context.Context, q db.Querier, groupID, personID int64) (policy.Rel, error) {
	var role, state, personStatus, groupStatus string
	err := q.QueryRowContext(ctx, `SELECT m.role, m.state, p.status, g.status FROM memberships m
		JOIN people p ON p.id = m.person_id JOIN groups g ON g.id = m.group_id
		WHERE m.group_id = ? AND m.person_id = ?`, groupID, personID).Scan(&role, &state, &personStatus, &groupStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return policy.Rel{}, nil
	}
	if err != nil {
		return policy.Rel{}, err
	}
	active := state == "active" && personStatus == "active" && groupStatus != policy.GroupDeleted
	return policy.Rel{Member: active, Leader: active && role == "leader", Removed: state == "removed"}, nil
}

// Group is one group's row.
type Group struct {
	ID             int64
	UID            string
	Name           string
	About          string
	Visibility     string
	Status         string
	Version        int64
	DecisionReason string
	DecisionNote   string
}

// Policy is the group in policy's terms.
func (g Group) Policy() policy.Group { return policy.Group{Status: g.Status, Visibility: g.Visibility} }

// ErrNotFound is a group that does not exist.
var ErrNotFound = errors.New("groups: not found")

const columns = `g.id, g.uid, g.name, g.about, g.visibility, g.status, g.version, g.decision_reason, g.decision_note`

func scan(row interface{ Scan(...any) error }) (Group, error) {
	var g Group
	err := row.Scan(&g.ID, &g.UID, &g.Name, &g.About, &g.Visibility, &g.Status, &g.Version, &g.DecisionReason, &g.DecisionNote)
	return g, err
}

// ByUID loads a group by its random id. Callers ask policy before showing
// it; a deleted group is loaded too, and policy hides it.
func ByUID(ctx context.Context, q db.Querier, uid string) (Group, error) {
	g, err := scan(q.QueryRowContext(ctx, `SELECT `+columns+` FROM groups g WHERE g.uid = ?`, uid))
	if errors.Is(err, sql.ErrNoRows) {
		return Group{}, ErrNotFound
	}
	return g, err
}

// ByID loads a group by its row id.
func ByID(ctx context.Context, q db.Querier, id int64) (Group, error) {
	g, err := scan(q.QueryRowContext(ctx, `SELECT `+columns+` FROM groups g WHERE g.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Group{}, ErrNotFound
	}
	return g, err
}

// LeaderCount is how many active leaders a group has.
func LeaderCount(ctx context.Context, q db.Querier, groupID int64) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM active_memberships WHERE group_id = ? AND role = 'leader'`, groupID).Scan(&n)
	return n, err
}

// Listed is a group as a list shows it.
type Listed struct {
	Group
	Members int
	Mine    bool // the viewer is a member
	Leads   bool // the viewer leads it
}

// Viewer is who is looking at a list, in the terms the SQL filter needs.
type Viewer struct {
	ID    int64 // 0 when signed out
	Admin bool
}

// visibleSQL is policy.CanSeeGroup for an active group, as SQL, so a list
// is filtered before it is limited (docs/HAZARDS.md section 2). ?1 is the
// viewer's id (0 signed out) and ?2 is 1 for an admin.
const visibleSQL = `g.status = 'active' AND (
	g.visibility = 'public'
	OR (g.visibility = 'church' AND ?1 > 0)
	OR ?2 = 1
	OR EXISTS (SELECT 1 FROM active_memberships am WHERE am.group_id = g.id AND am.person_id = ?1))`

// EscapeLike makes a search term literal inside LIKE ... ESCAPE '\'.
func EscapeLike(s string) string { return escapeLike(s) }

func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// Directory lists the active groups the viewer may see, matching a search
// in name or about, by name. It is checked against policy in the tests.
func Directory(ctx context.Context, q db.Querier, v Viewer, search string, limit int) ([]Listed, error) {
	admin := 0
	if v.Admin {
		admin = 1
	}
	query := `SELECT ` + columns + `,
		(SELECT count(*) FROM active_memberships am WHERE am.group_id = g.id),
		EXISTS (SELECT 1 FROM active_memberships am WHERE am.group_id = g.id AND am.person_id = ?1),
		EXISTS (SELECT 1 FROM active_memberships am WHERE am.group_id = g.id AND am.person_id = ?1 AND am.role = 'leader')
		FROM groups g WHERE ` + visibleSQL
	args := []any{v.ID, admin}
	if search = strings.TrimSpace(search); search != "" {
		query += ` AND (g.name LIKE ?3 ESCAPE '\' OR g.about LIKE ?3 ESCAPE '\')`
		args = append(args, "%"+escapeLike(search)+"%")
	}
	query += ` ORDER BY g.name COLLATE NOCASE, g.id LIMIT ` + strconv.Itoa(limit)
	return listed(ctx, q, query, args...)
}

func listed(ctx context.Context, q db.Querier, query string, args ...any) ([]Listed, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Listed
	for rows.Next() {
		var l Listed
		if err := rows.Scan(&l.ID, &l.UID, &l.Name, &l.About, &l.Visibility, &l.Status, &l.Version, &l.DecisionReason, &l.DecisionNote,
			&l.Members, &l.Mine, &l.Leads); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Mine lists the groups a person is in, active ones and any waiting or
// archived ones they lead, by name.
func Mine(ctx context.Context, q db.Querier, personID int64) ([]Listed, error) {
	return listed(ctx, q, `SELECT `+columns+`,
		(SELECT count(*) FROM active_memberships x WHERE x.group_id = g.id), 1, am.role = 'leader'
		FROM active_memberships am JOIN groups g ON g.id = am.group_id
		WHERE am.person_id = ?1 AND (g.status = 'active' OR am.role = 'leader')
		ORDER BY g.name COLLATE NOCASE, g.id`, personID)
}

// Waiting lists the groups waiting for an admin, oldest first, with who
// asked.
func Waiting(ctx context.Context, q db.Querier) ([]Listed, error) {
	return listed(ctx, q, `SELECT `+columns+`, 0, 0, 0 FROM groups g WHERE g.status = 'pending' ORDER BY g.created_at, g.id`)
}

// Leaders are the names of a group's active leaders, by name.
func Leaders(ctx context.Context, q db.Querier, groupID int64) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT p.name FROM active_memberships am JOIN people p ON p.id = am.person_id
		WHERE am.group_id = ? AND am.role = 'leader' ORDER BY p.name COLLATE NOCASE`, groupID)
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
