package events

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"fold/internal/db"
	"fold/internal/groups"
	"fold/internal/policy"
)

// Limits on what people type about an event, checked by the form.
const (
	MaxTitle = 100
	MaxAbout = 2000
	MaxPlace = 200
)

// Event is one event's row.
type Event struct {
	ID          int64
	UID         string
	GroupID     int64
	Title       string
	About       string
	Place       string
	Visibility  string
	Zone        string
	StartsLocal string
	Minutes     int
	Repeat      string
	MonthWeek   int
	Until       string
	Status      string
	Version     int64
	OpenReason  string // why it was opened wider than its group
}

// ChurchWide is an event with no group (DEC-7).
func (e Event) ChurchWide() bool { return e.GroupID == 0 }

// Policy is the event in policy's terms; host is whether the person asking
// hosts it.
func (e Event) Policy(host bool) policy.Event {
	return policy.Event{Status: e.Status, Visibility: e.Visibility, ChurchWide: e.ChurchWide(), Host: host}
}

// Pattern is when the event happens.
func (e Event) Pattern() Pattern {
	return Pattern{Start: e.StartsLocal, Minutes: e.Minutes, Repeat: e.Repeat, MonthWeek: e.MonthWeek, Until: e.Until}
}

// ErrNotFound is an event or date that does not exist.
var ErrNotFound = errors.New("events: not found")

const eventColumns = `e.id, e.uid, coalesce(e.group_id, 0), e.title, e.about, e.place, e.visibility, e.zone, e.starts_local,
	e.minutes, e.repeat, coalesce(e.month_week, 0), coalesce(e.until, ''), e.status, e.version, e.open_reason`

func scanEvent(row interface{ Scan(...any) error }) (Event, error) {
	var e Event
	err := row.Scan(&e.ID, &e.UID, &e.GroupID, &e.Title, &e.About, &e.Place, &e.Visibility, &e.Zone, &e.StartsLocal,
		&e.Minutes, &e.Repeat, &e.MonthWeek, &e.Until, &e.Status, &e.Version, &e.OpenReason)
	if errors.Is(err, sql.ErrNoRows) {
		return Event{}, ErrNotFound
	}
	return e, err
}

// ByUID loads an event by its random id. A deleted event is loaded too, and
// policy hides it.
func ByUID(ctx context.Context, q db.Querier, uid string) (Event, error) {
	return scanEvent(q.QueryRowContext(ctx, `SELECT `+eventColumns+` FROM events e WHERE e.uid = ?`, uid))
}

// Row is one stored date.
type Row struct {
	ID          int64
	UID         string
	EventID     int64
	Day         string
	StartsAt    time.Time
	EndsAt      time.Time
	StartsLocal string
	Moved       bool
	Cancelled   bool
	BySeries    bool
	Visibility  string // its own when opened wider than its series (P7), else ""
	OpenReason  string
}

// State is the date in policy's terms.
func (d Row) State(now time.Time) policy.DateState {
	return policy.DateState{Started: !d.StartsAt.After(now), Cancelled: d.Cancelled, BySeries: d.BySeries}
}

const dateColumns = `d.id, d.uid, d.event_id, d.day, d.starts_at, d.ends_at, d.starts_local, d.moved, d.status = 'cancelled',
	coalesce(d.cancelled_by, '') = 'series', coalesce(d.visibility, ''), d.open_reason`

func scanDate(row interface{ Scan(...any) error }) (Row, error) {
	var d Row
	var starts, ends int64
	err := row.Scan(&d.ID, &d.UID, &d.EventID, &d.Day, &starts, &ends, &d.StartsLocal, &d.Moved, &d.Cancelled, &d.BySeries, &d.Visibility, &d.OpenReason)
	d.StartsAt, d.EndsAt = time.Unix(starts, 0).UTC(), time.Unix(ends, 0).UTC()
	return d, err
}

// DateByUID loads one date and its event. Callers ask policy about the
// event before showing either.
func DateByUID(ctx context.Context, q db.Querier, uid string) (Row, Event, error) {
	d, err := scanDate(q.QueryRowContext(ctx, `SELECT `+dateColumns+` FROM dates d WHERE d.uid = ?`, uid))
	if errors.Is(err, sql.ErrNoRows) {
		return Row{}, Event{}, ErrNotFound
	}
	if err != nil {
		return Row{}, Event{}, err
	}
	e, err := scanEvent(q.QueryRowContext(ctx, `SELECT `+eventColumns+` FROM events e WHERE e.id = ?`, d.EventID))
	return d, e, err
}

// stored lists every date of an event, for the series edit.
func stored(ctx context.Context, q db.Querier, eventID int64) ([]Stored, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+dateColumns+` FROM dates d WHERE d.event_id = ? ORDER BY d.day`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Stored
	for rows.Next() {
		d, err := scanDate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, Stored{ID: d.ID, Day: d.Day, StartsAt: d.StartsAt, EndsAt: d.EndsAt, StartsLocal: d.StartsLocal,
			Moved: d.Moved, Cancelled: d.Cancelled, BySeries: d.BySeries})
	}
	return out, rows.Err()
}

// Viewer is who is looking at a list, in the terms the SQL filter needs.
type Viewer struct {
	ID    int64 // 0 when signed out
	Admin bool
}

// visibleSQL is policy.CanSeeDate as SQL, so a list of dates is filtered
// before it is limited (docs/HAZARDS.md section 2). ?1 is the viewer's id
// (0 signed out) and ?2 is 1 for an admin. It needs dates d, events e and a
// left join of groups g. A test checks it against the policy function.
const visibleSQL = `e.status = 'active' AND (
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

// Listed is a date as a list shows it.
type Listed struct {
	UID       string
	StartsAt  time.Time
	EndsAt    time.Time
	Moved     bool
	Cancelled bool // only in a list asked to include dates cancelled by hand
	Event     string // the event's uid
	Title     string
	Zone      string
}

// Filter narrows a list of dates.
type Filter struct {
	GroupID int64
	EventID int64
	// ByHand includes future dates cancelled by hand, for the people who run
	// the event: the only way back to one to restore it.
	ByHand bool
}

// Upcoming lists the scheduled dates the viewer may see that have not ended
// by now, soonest first. Visibility is filtered in the query, before the
// limit.
func Upcoming(ctx context.Context, q db.Querier, v Viewer, f Filter, now time.Time, limit int) ([]Listed, error) {
	admin := 0
	if v.Admin {
		admin = 1
	}
	rows, err := q.QueryContext(ctx, `SELECT d.uid, d.starts_at, d.ends_at, d.moved, d.status = 'cancelled', e.uid, e.title, e.zone
		FROM dates d JOIN events e ON e.id = d.event_id LEFT JOIN groups g ON g.id = e.group_id
		WHERE `+visibleSQL+` AND d.ends_at > ?3
		AND (d.status = 'scheduled' OR (?6 = 1 AND d.cancelled_by = 'person' AND d.starts_at > ?3))
		AND (?4 = 0 OR e.group_id = ?4) AND (?5 = 0 OR e.id = ?5)
		ORDER BY d.starts_at, d.id LIMIT `+strconv.Itoa(limit), v.ID, admin, now.Unix(), f.GroupID, f.EventID, f.ByHand)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Listed
	for rows.Next() {
		var l Listed
		var starts, ends int64
		if err := rows.Scan(&l.UID, &starts, &ends, &l.Moved, &l.Cancelled, &l.Event, &l.Title, &l.Zone); err != nil {
			return nil, err
		}
		l.StartsAt, l.EndsAt = time.Unix(starts, 0).UTC(), time.Unix(ends, 0).UTC()
		out = append(out, l)
	}
	return out, rows.Err()
}

// IsHost reports whether a person hosts an event. A host's account must be
// active; a disabled host runs nothing.
func IsHost(ctx context.Context, q db.Querier, eventID, personID int64) (bool, error) {
	var yes bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM event_hosts h JOIN people p ON p.id = h.person_id
		WHERE h.event_id = ? AND h.person_id = ? AND p.status = 'active')`, eventID, personID).Scan(&yes)
	return yes, err
}

// Person is someone named on an event page: a name, never contact details.
type Person struct{ UID, Name string }

func hostRows(ctx context.Context, q db.Querier, query string, args ...any) ([]Person, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Person
	for rows.Next() {
		var p Person
		if err := rows.Scan(&p.UID, &p.Name); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Hosts are an event's hosts whose accounts are active, by name.
func Hosts(ctx context.Context, q db.Querier, eventID int64) ([]Person, error) {
	return hostRows(ctx, q, `SELECT p.uid, p.name FROM event_hosts h JOIN people p ON p.id = h.person_id
		WHERE h.event_id = ? AND p.status = 'active' ORDER BY p.name COLLATE NOCASE`, eventID)
}

// FindHosts searches active people by name who do not host the event yet.
// The caller has asked policy.CanFindHosts.
func FindHosts(ctx context.Context, q db.Querier, eventID int64, name string, limit int) ([]Person, error) {
	return hostRows(ctx, q, `SELECT p.uid, p.name FROM people p
		WHERE p.status = 'active' AND p.name LIKE ? ESCAPE '\'
		AND NOT EXISTS (SELECT 1 FROM event_hosts h WHERE h.event_id = ? AND h.person_id = p.id)
		ORDER BY p.name COLLATE NOCASE LIMIT ?`, "%"+groups.EscapeLike(name)+"%", eventID, limit)
}

// ChurchWide lists the church-wide events that are not deleted, by title.
func ChurchWide(ctx context.Context, q db.Querier) ([]Event, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+eventColumns+` FROM events e WHERE e.group_id IS NULL AND e.status = 'active'
		ORDER BY e.title COLLATE NOCASE, e.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
