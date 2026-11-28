package events

import (
	"context"
	"strconv"
	"time"

	"fold/internal/db"
	"fold/internal/policy"
)

// The calendar's read: the dates a viewer may see, in their scope, with the
// facts each row needs and nothing about anyone else's answer (DEC-64,
// docs/HAZARDS.md section 2).

// Scope is who is looking and what they asked for.
type Scope struct {
	// Viewer is who sees, for visibleSQL: the zero Viewer for a guest, who
	// sees as a signed-out visitor does (R15).
	Viewer Viewer
	// Answerer is whose answers mark the dates: the member, the guest, or 0.
	Answerer int64
	JustMine bool
	GroupID  int64 // 0 for every group
}

// Kind is the viewer in policy.InScope's terms.
func (s Scope) Kind() int {
	switch {
	case s.Viewer.ID > 0:
		return policy.ViewerMember
	case s.Answerer > 0:
		return policy.ViewerGuest
	}
	return policy.ViewerSignedOut
}

// CalDate is one row of the calendar: the date, its event's title and
// place, its group's name only when the viewer may see the group, and the
// viewer's own answer.
type CalDate struct {
	UID              string
	StartsAt, EndsAt time.Time
	Moved            bool
	Title, Place     string
	ChurchWide       bool
	Public           bool // its widest visibility is public
	Answer           string
	InGroup, Leads   bool // the viewer's membership of its group
	Host             bool
	GroupUID         string // "" for a church-wide event or a group the viewer may not see
	GroupName        string
}

// Facts are the row in policy.InScope's terms.
func (c CalDate) Facts(kind int) policy.CalendarFacts {
	return policy.CalendarFacts{Seen: true, Viewer: kind, InGroup: c.InGroup, ChurchWide: c.ChurchWide, Host: c.Host,
		Answer: c.Answer, Public: c.Public, StartsAt: c.StartsAt, EndsAt: c.EndsAt}
}

// inGroupSQL, hostSQL and publicSQL are the facts policy.InScope reads, for
// the viewer ?1. A guest's ?1 is 0, so no membership or host row is theirs.
const (
	inGroupSQL = `EXISTS (SELECT 1 FROM active_memberships am WHERE am.group_id = e.group_id AND am.person_id = ?1)`
	hostSQL    = `(e.group_id IS NULL AND EXISTS (SELECT 1 FROM event_hosts h JOIN people hp ON hp.id = h.person_id
		WHERE h.event_id = e.id AND h.person_id = ?1 AND hp.status = 'active'))`
	publicSQL = `(e.visibility = 'public' OR coalesce(d.visibility, '') = 'public')`
)

// scopeSQL is policy.InScope as SQL, so the calendar is scoped before it is
// limited. ?4 is 1 for Just mine, ?5 now and ?6 the window's far edge, both
// in Unix seconds; o is the answerer's own answer. A test checks it
// against the policy function.
const scopeSQL = `CASE
	WHEN ?4 = 1 AND coalesce(o.status, '') = 'cant' THEN 0
	WHEN coalesce(o.status, '') IN ('going', 'maybe', 'waiting', 'cant') OR ` + inGroupSQL + ` OR ` + hostSQL + ` THEN 1
	WHEN ?4 = 1 AND NOT (?1 > 0 AND e.group_id IS NULL) THEN 0
	WHEN e.group_id IS NULL OR (?4 = 0 AND ` + publicSQL + `) THEN d.ends_at > ?5 AND d.starts_at <= ?6
	ELSE 1 END`

// Calendar lists the scheduled dates the viewer may see, in their scope,
// that start in [from, to) and end after since, soonest first, at most
// limit. Visibility and scope are filtered in the query, before the limit.
func Calendar(ctx context.Context, q db.Querier, s Scope, from, to, since, now time.Time, limit int) ([]CalDate, error) {
	admin, mine := 0, 0
	if s.Viewer.Admin {
		admin = 1
	}
	if s.JustMine {
		mine = 1
	}
	rows, err := q.QueryContext(ctx, `SELECT d.uid, d.starts_at, d.ends_at, d.moved, e.title, e.place, e.group_id IS NULL, `+publicSQL+`,
		coalesce(o.status, ''), e.group_id IS NOT NULL AND `+inGroupSQL+`,
		EXISTS (SELECT 1 FROM active_memberships am WHERE am.group_id = e.group_id AND am.person_id = ?1 AND am.role = 'leader'),
		`+hostSQL+`, coalesce(g.status, ''), coalesce(g.visibility, ''), coalesce(g.uid, ''), coalesce(g.name, '')
		FROM dates d JOIN events e ON e.id = d.event_id LEFT JOIN groups g ON g.id = e.group_id
		LEFT JOIN answers o ON o.date_id = d.id AND o.person_id = ?3
		WHERE `+visibleSQL+` AND d.status = 'scheduled' AND d.starts_at >= ?7 AND d.starts_at < ?8 AND d.ends_at > ?9
		AND (?10 = 0 OR e.group_id = ?10) AND `+scopeSQL+`
		ORDER BY d.starts_at, d.id LIMIT `+strconv.Itoa(limit),
		s.Viewer.ID, admin, s.Answerer, mine, now.Unix(), now.Add(policy.Window).Unix(), from.Unix(), to.Unix(), since.Unix(), s.GroupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var p *policy.Person
	if s.Viewer.ID > 0 {
		p = &policy.Person{ID: s.Viewer.ID, Admin: s.Viewer.Admin}
	}
	var out []CalDate
	for rows.Next() {
		var c CalDate
		var g policy.Group
		var starts, ends int64
		if err := rows.Scan(&c.UID, &starts, &ends, &c.Moved, &c.Title, &c.Place, &c.ChurchWide, &c.Public, &c.Answer, &c.InGroup, &c.Leads,
			&c.Host, &g.Status, &g.Visibility, &c.GroupUID, &c.GroupName); err != nil {
			return nil, err
		}
		// An event can be more open than its group (DEC-48, P7): the row
		// names the group only to someone who may see it.
		if c.ChurchWide || !policy.CanSeeGroup(p, g, policy.Rel{Member: c.InGroup, Leader: c.Leads}) {
			c.GroupUID, c.GroupName = "", ""
		}
		c.StartsAt, c.EndsAt = time.Unix(starts, 0).UTC(), time.Unix(ends, 0).UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}
