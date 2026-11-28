package events

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"fold/internal/answers"
	"fold/internal/db"
	"fold/internal/groups"
	"fold/internal/notices"
	"fold/internal/policy"
	"fold/internal/write"
)

// Signed is one person signed up to one date (Going, Maybe or waiting),
// with what policy needs to know whether they may see it.
type Signed struct {
	PersonID int64
	UID      string
	Name     string
	Answer   string
	DateID   int64
	DateVis  string
	person   policy.Person
	rel      policy.Rel
	host     bool
}

// sees asks policy whether the person may see their date with the event at
// the given visibility, as if the event were not deleted (a delete tells
// the people who could see it).
func (s Signed) sees(e Event, g groups.Group, eventVis string) bool {
	ev := e.Policy(s.host)
	ev.Status, ev.Visibility = policy.EventActive, eventVis
	return policy.CanSeeDate(&s.person, ev, s.DateVis, g.Policy(), s.rel)
}

// signedUp lists the active accounts signed up to an event's dates that
// match where, soonest date first.
func signedUp(ctx context.Context, q db.Querier, e Event, g groups.Group, where string, args ...any) ([]Signed, error) {
	rows, err := q.QueryContext(ctx, `SELECT a.person_id, p.uid, p.name, p.is_admin, a.status, d.id, coalesce(d.visibility, ''),
		coalesce(m.role, ''), coalesce(m.state, ''),
		EXISTS (SELECT 1 FROM event_hosts h WHERE h.event_id = d.event_id AND h.person_id = a.person_id)
		FROM answers a JOIN people p ON p.id = a.person_id JOIN dates d ON d.id = a.date_id
		LEFT JOIN memberships m ON m.group_id = ? AND m.person_id = a.person_id
		WHERE d.event_id = ? AND p.status = 'active' AND a.status IN (`+inList(policy.SignedUp)+`) AND `+where+`
		ORDER BY d.starts_at, d.id, p.name COLLATE NOCASE`, append([]any{e.GroupID, e.ID}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Signed
	for rows.Next() {
		var s Signed
		var role, state string
		if err := rows.Scan(&s.PersonID, &s.UID, &s.Name, &s.person.Admin, &s.Answer, &s.DateID, &s.DateVis, &role, &state, &s.host); err != nil {
			return nil, err
		}
		s.person.ID = s.PersonID
		active := state == "active" && g.Status != policy.GroupDeleted
		s.rel = policy.Rel{Member: active, Leader: active && role == "leader", Removed: state == "removed"}
		out = append(out, s)
	}
	return out, rows.Err()
}

func inList(states []string) string {
	q := make([]string, len(states))
	for i, s := range states {
		q[i] = "'" + s + "'"
	}
	return strings.Join(q, ", ")
}

// upcoming are the dates still to come that are on.
const upcoming = `d.status = 'scheduled' AND d.starts_at > ?`

// audience is who a change to dates tells: everyone signed up who may see
// their date with the event at eventVis, but the person making the change.
// Each person is told once, about their first date, and by email when any
// of theirs was Going or Maybe (R36). Every confirmation that says how many
// people will be told counts with this.
func audience(list []Signed, e Event, g groups.Group, eventVis string, except int64, kind string) []notices.New {
	at := map[int64]int{}
	var out []notices.New
	for _, s := range list {
		if s.PersonID == except || !s.sees(e, g, eventVis) {
			continue
		}
		i, seen := at[s.PersonID]
		if !seen {
			at[s.PersonID] = len(out)
			out = append(out, notices.New{PersonID: s.PersonID, Kind: kind, EventID: e.ID, DateID: s.DateID, Answer: s.Answer})
			continue
		}
		if policy.Urgent(s.Answer) {
			out[i].Answer = s.Answer
		}
	}
	return out
}

// tell adds the audience's notices, and records how many were told.
func tell(tx *write.Tx, ns []notices.New) error {
	if len(ns) == 0 {
		return nil
	}
	tx.Detail("told", strconv.Itoa(len(ns)))
	return notices.AddMany(tx, ns)
}

func dateIDs(list []int64) (string, []any) {
	args := make([]any, len(list))
	for i, id := range list {
		args[i] = id
	}
	return `d.id IN (` + strings.TrimSuffix(strings.Repeat("?, ", len(list)), ", ") + `)`, args
}

// Telling counts the people a delete (dateID 0) or a cancel of one date
// would tell, for its confirmation (DEC-175). The number is shown, not
// bound to the press.
func Telling(ctx context.Context, q db.Querier, e Event, g groups.Group, dateID int64, now time.Time, except int64) (int, error) {
	where, args := upcoming, []any{now.Unix()}
	if dateID != 0 {
		where, args = `d.id = ?`, []any{dateID}
	}
	list, err := signedUp(ctx, q, e, g, where, args...)
	return len(audience(list, e, g, e.Visibility, except, "")), err
}

// Unseen is one person signed up who would no longer see what they signed
// up to, were it made more private (R3). Nobody is removed; those running
// the event may remove them.
type Unseen struct {
	UID, Name string
	Dates     int // how many answers Remove would take: Answers on its dates still to come
}

// unseen lists who among list may see their date now and would not with
// the event at eventVis and each date at dateVis(date) instead.
func unseen(ctx context.Context, q db.Querier, list []Signed, e Event, g groups.Group, eventVis string, dateVis func(Signed) string, now time.Time, oneDate bool) ([]Unseen, error) {
	seen := map[int64]bool{}
	var out []Unseen
	for _, s := range list {
		after := s
		after.DateVis = dateVis(s)
		if seen[s.PersonID] || !s.sees(e, g, e.Visibility) || after.sees(e, g, eventVis) {
			continue
		}
		seen[s.PersonID] = true
		u := Unseen{UID: s.UID, Name: s.Name, Dates: 1}
		if !oneDate {
			n, err := Answers(ctx, q, e.ID, s.PersonID, now)
			if err != nil {
				return nil, err
			}
			u.Dates = n
		}
		out = append(out, u)
	}
	return out, nil
}

// removable are an event's dates still to come, cancelled for now or not:
// what removing someone from the event takes them off.
const removable = `d.event_id = ? AND d.starts_at > ?`

// Answers counts a person's answers on an event's dates still to come that
// Remove would take back: the count its confirmation states.
func Answers(ctx context.Context, q db.Querier, eventID, personID int64, now time.Time) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM answers a JOIN dates d ON d.id = a.date_id
		WHERE a.person_id = ? AND a.status IN (`+inList(policy.MoveRemove.From())+`) AND `+removable, personID, eventID, now.Unix()).Scan(&n)
	return n, err
}

// RemoveAnswer takes a person off one date, or with no DateUID off every
// date of the event still to come, for those running the event (DEC-84,
// R3). The next waiting person moves up only where that frees a seat. It
// is also what R3's list offers beside each name.
type RemoveAnswer struct {
	DateUID, EventUID string
	PersonUID         string
	Removed           int    // set on success
	Name              string // theirs, set on success
}

func (*RemoveAnswer) Action() string { return "answer.remove" }

func (c *RemoveAnswer) Apply(tx *write.Tx) error {
	var e Event
	var ids []int64
	if c.DateUID != "" {
		d, de, _, err := dateTarget(tx, c.DateUID)
		if err != nil {
			return err
		}
		if !policy.CanRemoveAnswer(true, d.State(tx.Now).Started) {
			return ErrDateGone
		}
		e, ids = de, []int64{d.ID}
	} else {
		var err error
		if e, _, err = target(tx, c.EventUID); err != nil {
			return err
		}
		rows, err := tx.Query(`SELECT d.id FROM dates d WHERE `+removable, e.ID, tx.Now.Unix())
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			rows.Scan(&id)
			ids = append(ids, id)
		}
		rows.Close()
		tx.Subject(e.UID)
	}
	var personID int64
	if err := tx.QueryRow(`SELECT id, name FROM people WHERE uid = ?`, c.PersonUID).Scan(&personID, &c.Name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUnchanged
		}
		return err
	}
	n, err := answers.Remove(tx, personID, ids)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrUnchanged
	}
	c.Removed = n
	tx.Subject(c.PersonUID)
	tx.Detail("removed", strconv.Itoa(n))
	return nil
}
