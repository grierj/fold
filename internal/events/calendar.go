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
	// Cancelled lists dates cancelled by hand or by a series edit too, so a
	// calendar app removes them (the feed).
	Cancelled bool
	// Details reads each row's description and video link, kept only where
	// the viewer may have them (the feed).
	Details bool
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
	Cancelled        bool
	Zone             string
	Sequence         int
	UpdatedAt        time.Time
	// With Scope.Details: the event's description only to someone who may
	// see the event, not just an opened date (#167), and its video link as
	// the date page gives it (R41, R49).
	About, Video string
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
	admin, mine, cancelled := 0, 0, 0
	if s.Viewer.Admin {
		admin = 1
	}
	if s.JustMine {
		mine = 1
	}
	if s.Cancelled {
		cancelled = 1
	}
	rows, err := q.QueryContext(ctx, `SELECT d.uid, d.starts_at, d.ends_at, d.moved, d.status = 'cancelled', d.sequence, d.updated_at, e.title, e.place, e.zone, e.visibility,
		CASE WHEN ?12 = 1 THEN e.about ELSE '' END, CASE WHEN ?12 = 1 THEN e.video_url ELSE '' END, e.group_id IS NULL, `+publicSQL+`,
		coalesce(o.status, ''), e.group_id IS NOT NULL AND `+inGroupSQL+`,
		EXISTS (SELECT 1 FROM active_memberships am WHERE am.group_id = e.group_id AND am.person_id = ?1 AND am.role = 'leader'),
		`+hostSQL+`, coalesce(g.status, ''), coalesce(g.visibility, ''), coalesce(g.uid, ''), coalesce(g.name, '')
		FROM dates d JOIN events e ON e.id = d.event_id LEFT JOIN groups g ON g.id = e.group_id
		LEFT JOIN answers o ON o.date_id = d.id AND o.person_id = ?3
		WHERE `+visibleSQL+` AND (d.status = 'scheduled' OR ?11 = 1) AND d.starts_at >= ?7 AND d.starts_at < ?8 AND d.ends_at > ?9
		AND (?10 = 0 OR e.group_id = ?10) AND `+scopeSQL+`
		ORDER BY d.starts_at, d.id LIMIT `+strconv.Itoa(limit),
		s.Viewer.ID, admin, s.Answerer, mine, now.Unix(), now.Add(policy.Window).Unix(), from.Unix(), to.Unix(), since.Unix(), s.GroupID, cancelled, s.Details)
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
		var starts, ends, updated int64
		var visibility, about, video string
		if err := rows.Scan(&c.UID, &starts, &ends, &c.Moved, &c.Cancelled, &c.Sequence, &updated, &c.Title, &c.Place, &c.Zone, &visibility, &about, &video,
			&c.ChurchWide, &c.Public, &c.Answer, &c.InGroup, &c.Leads, &c.Host, &g.Status, &g.Visibility, &c.GroupUID, &c.GroupName); err != nil {
			return nil, err
		}
		ev := policy.Event{Status: policy.EventActive, Visibility: visibility, ChurchWide: c.ChurchWide, Host: c.Host}
		rel := policy.Rel{Member: c.InGroup, Leader: c.Leads}
		if policy.CanSeeEvent(p, ev, g, rel) {
			c.About = about
		}
		if !c.Cancelled && policy.CanSeeVideo(policy.CanManageEvent(p, ev, g, rel), c.Answer) {
			c.Video = video
		}
		// An event can be more open than its group (DEC-48, P7): the row
		// names the group only to someone who may see it.
		if c.ChurchWide || !policy.CanSeeGroup(p, g, rel) {
			c.GroupUID, c.GroupName = "", ""
		}
		c.StartsAt, c.EndsAt, c.UpdatedAt = time.Unix(starts, 0).UTC(), time.Unix(ends, 0).UTC(), time.Unix(updated, 0).UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// SoonGroup is a group someone might join, with its next date they may see.
type SoonGroup struct {
	UID, Name string
	Next      time.Time
	Zone      string // the next date's event's zone
}

// JoinableSoon lists up to limit active groups the viewer may see and join
// and is not in, soonest next date first, for Home when they are in no group
// (DEC-94). The next date is the soonest the viewer may see, so it reveals
// no hidden date's time (docs/HAZARDS.md section 2, #359). Joining follows
// policy.CanJoinGroup: not invite-only unless an admin, and not someone a
// leader removed; the test holds them together.
func JoinableSoon(ctx context.Context, q db.Querier, v Viewer, now time.Time, limit int) ([]SoonGroup, error) {
	admin := 0
	if v.Admin {
		admin = 1
	}
	// SQLite takes a bare column from the row min() picks, so e.zone is the
	// next date's.
	rows, err := q.QueryContext(ctx, `SELECT g.uid, g.name, min(d.starts_at), e.zone
		FROM dates d JOIN events e ON e.id = d.event_id JOIN groups g ON g.id = e.group_id
		WHERE `+visibleSQL+` AND d.status = 'scheduled' AND d.starts_at > ?3 AND ?1 > 0
		AND g.status = 'active' AND (g.visibility IN ('public', 'church') OR ?2 = 1)
		AND NOT EXISTS (SELECT 1 FROM memberships m WHERE m.group_id = g.id AND m.person_id = ?1 AND m.state IN ('active', 'removed'))
		GROUP BY g.id ORDER BY min(d.starts_at), g.id LIMIT `+strconv.Itoa(limit), v.ID, admin, now.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SoonGroup
	for rows.Next() {
		var s SoonGroup
		var next int64
		if err := rows.Scan(&s.UID, &s.Name, &next, &s.Zone); err != nil {
			return nil, err
		}
		s.Next = time.Unix(next, 0).UTC()
		out = append(out, s)
	}
	return out, rows.Err()
}
