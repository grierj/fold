// Package groups reads groups and memberships for pages and commands.
package groups

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

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
	Mine    bool      // the viewer is a member
	Leads   bool      // the viewer leads it
	NextAt   time.Time // its next date the viewer may see; zero for none
	NextZone string    // that date's event's zone
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

// nextSQL is a group's next date the viewer may see: from dates passing
// DateVisibleSQL only, so neither the "Next" line nor the order of a list
// gives away a hidden date (docs/HAZARDS.md section 2, #359). ?3 is now.
const nextSQL = `(SELECT min(d.starts_at) FROM dates d JOIN events e ON e.id = d.event_id
	WHERE e.group_id = g.id AND d.status = 'scheduled' AND d.starts_at > ?3 AND ` + DateVisibleSQL + `)`

// nextZoneSQL is the zone of the event nextSQL's date belongs to.
const nextZoneSQL = `(SELECT e.zone FROM dates d JOIN events e ON e.id = d.event_id
	WHERE e.group_id = g.id AND d.status = 'scheduled' AND d.starts_at > ?3 AND ` + DateVisibleSQL + `
	ORDER BY d.starts_at, d.id LIMIT 1)`

// byNext sorts a list by next meeting, groups with none last (DEC-94).
const byNext = ` ORDER BY next IS NULL, next, g.name COLLATE NOCASE, g.id`

// Find narrows the directory.
type Find struct {
	Search string
	// Times keeps groups with a date the viewer may see in one of these
	// spans, a JSON array of [from, to) pairs in Unix seconds; "" keeps all.
	// The caller works them out in the viewer's zone, since SQLite has none.
	Times string
}

// Directory lists the active groups the viewer may see, matching a search
// in name or about and meeting in the times asked for, by next meeting.
// Both are filtered in the query, before the limit; the tests hold them to
// policy.
func Directory(ctx context.Context, q db.Querier, v Viewer, f Find, now time.Time, limit int) ([]Listed, error) {
	admin := 0
	if v.Admin {
		admin = 1
	}
	search := ""
	if s := strings.TrimSpace(f.Search); s != "" {
		search = "%" + escapeLike(s) + "%"
	}
	return listed(ctx, q, `SELECT `+columns+`,
		(SELECT count(*) FROM active_memberships am WHERE am.group_id = g.id),
		EXISTS (SELECT 1 FROM active_memberships am WHERE am.group_id = g.id AND am.person_id = ?1),
		EXISTS (SELECT 1 FROM active_memberships am WHERE am.group_id = g.id AND am.person_id = ?1 AND am.role = 'leader'),
		`+nextSQL+` AS next, coalesce(`+nextZoneSQL+`, '')
		FROM groups g WHERE `+visibleSQL+`
		AND (?4 = '' OR g.name LIKE ?4 ESCAPE '\' OR g.about LIKE ?4 ESCAPE '\')
		AND (?5 = '' OR EXISTS (SELECT 1 FROM dates d JOIN events e ON e.id = d.event_id, json_each(?5) w
			WHERE e.group_id = g.id AND d.status = 'scheduled' AND d.starts_at > ?3
			AND d.starts_at >= json_extract(w.value, '$[0]') AND d.starts_at < json_extract(w.value, '$[1]') AND `+DateVisibleSQL+`))`+
		byNext+` LIMIT `+strconv.Itoa(limit), v.ID, admin, now.Unix(), search, f.Times)
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
		var next sql.NullInt64
		if err := rows.Scan(&l.ID, &l.UID, &l.Name, &l.About, &l.Visibility, &l.Status, &l.Version, &l.DecisionReason, &l.DecisionNote,
			&l.Members, &l.Mine, &l.Leads, &next, &l.NextZone); err != nil {
			return nil, err
		}
		if next.Valid {
			l.NextAt = time.Unix(next.Int64, 0).UTC()
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Mine lists the groups a person is in, active ones and any waiting or
// archived ones they lead, by their next meeting as that person sees it.
func Mine(ctx context.Context, q db.Querier, v Viewer, now time.Time) ([]Listed, error) {
	admin := 0
	if v.Admin {
		admin = 1
	}
	return listed(ctx, q, `SELECT `+columns+`,
		(SELECT count(*) FROM active_memberships x WHERE x.group_id = g.id), 1, am.role = 'leader', `+nextSQL+` AS next, coalesce(`+nextZoneSQL+`, '')
		FROM active_memberships am JOIN groups g ON g.id = am.group_id
		WHERE am.person_id = ?1 AND (g.status = 'active' OR am.role = 'leader')`+byNext, v.ID, admin, now.Unix())
}

// DateVisibleSQL is policy.CanSeeDate as SQL, so a list of dates is filtered
// before it is limited (docs/HAZARDS.md section 2). ?1 is the viewer's id
// (0 signed out) and ?2 is 1 for an admin. It needs dates d, events e and a
// left join of groups g. events.TestVisibleSQLMatchesPolicy holds it to
// the policy function.
const DateVisibleSQL = `e.status = 'active' AND (
	(?2 = 1 AND (e.group_id IS NULL OR g.status <> 'deleted'))
	OR (e.group_id IS NULL AND (` + seenByVisibility + `
		OR EXISTS (SELECT 1 FROM event_hosts h JOIN people hp ON hp.id = h.person_id
			WHERE h.event_id = e.id AND h.person_id = ?1 AND hp.status = 'active')))
	OR (e.group_id IS NOT NULL AND g.status = 'active' AND (` + seenByVisibility + `
		OR EXISTS (SELECT 1 FROM active_memberships am WHERE am.group_id = g.id AND am.person_id = ?1)))
	OR (e.group_id IS NOT NULL AND g.status IN ('pending', 'archived') AND EXISTS (SELECT 1 FROM active_memberships am
		WHERE am.group_id = g.id AND am.person_id = ?1 AND am.role = 'leader')))`

// seenByVisibility is the date's visibility, the wider of its own and its
// event's (P7), letting in everyone or everyone signed in.
const seenByVisibility = `e.visibility = 'public' OR d.visibility = 'public'
		OR ((e.visibility = 'church' OR d.visibility = 'church') AND ?1 > 0)`

// Waiting lists the groups waiting for an admin, oldest first, with who
// asked.
func Waiting(ctx context.Context, q db.Querier) ([]Listed, error) {
	return listed(ctx, q, `SELECT `+columns+`, 0, 0, 0, NULL, '' FROM groups g WHERE g.status = 'pending' ORDER BY g.created_at, g.id`)
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
