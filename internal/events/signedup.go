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

func (e Event) answers(g groups.Group) answers.Event {
	return answers.Event{ID: e.ID, GroupID: e.GroupID, Visibility: e.Visibility, ChurchWide: e.ChurchWide(), Group: g.Policy()}
}

// signedUp lists the people signed up to an event's dates that match
// where, soonest date first.
func signedUp(ctx context.Context, q db.Querier, e Event, g groups.Group, where string, args ...any) ([]answers.Signed, error) {
	return answers.SignedUp(ctx, q, e.answers(g), where, args...)
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

// audience is who a change to dates tells (answers.Audience).
func audience(list []answers.Signed, e Event, g groups.Group, eventVis string, except int64, kind string) []notices.New {
	return answers.Audience(list, e.answers(g), eventVis, except, kind)
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
	Guest     bool // marked beside their name (R46)
	Dates     int  // how many answers Remove would take: Answers on its dates still to come
}

// unseen lists who among list may see their date now and would not with
// the event at eventVis and each date at dateVis(date) instead.
func unseen(ctx context.Context, q db.Querier, list []answers.Signed, e Event, g groups.Group, eventVis string, dateVis func(answers.Signed) string, now time.Time, oneDate bool) ([]Unseen, error) {
	seen := map[int64]bool{}
	var out []Unseen
	ae := e.answers(g)
	for _, s := range list {
		if seen[s.PersonID] || !s.Sees(ae, e.Visibility, s.DateVis) || s.Sees(ae, eventVis, dateVis(s)) {
			continue
		}
		seen[s.PersonID] = true
		u := Unseen{UID: s.UID, Name: s.Name, Guest: s.Guest(), Dates: 1}
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

// removable are an event's dates still to come that are on: what removing
// someone from the event takes them off, and what its count says.
const removable = `d.event_id = ? AND d.status = 'scheduled' AND d.starts_at > ?`

// Answers counts a person's answers on an event's dates still to come that
// Remove would take back: the count its confirmation states.
func Answers(ctx context.Context, q db.Querier, eventID, personID int64, now time.Time) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM answers a JOIN dates d ON d.id = a.date_id
		WHERE a.person_id = ? AND a.status IN (`+inList(policy.MoveRemove.From())+`) AND `+removable, personID, eventID, now.Unix()).Scan(&n)
	return n, err
}

// RemoveAnswer takes a person off one date, or with no DateUID off every
// date of the event still to come that is on, for those running the event
// (DEC-84, R3). The next waiting person moves up only where that frees a
// seat, and the person is told (R44); they may answer again while they can
// see the date. It is also what R3's list offers beside each name.
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
	var guest bool
	if err := tx.QueryRow(`SELECT id, name, status = 'guest' FROM people WHERE uid = ?`, c.PersonUID).Scan(&personID, &c.Name, &guest); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUnchanged
		}
		return err
	}
	n, first, err := answers.Remove(tx, personID, ids)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrUnchanged
	}
	c.Removed = n
	tx.Subject(c.PersonUID)
	tx.Detail("removed", strconv.Itoa(n))
	if personID == tx.Actor.Person.ID {
		return nil // nobody is told about what they did themselves
	}
	kind := notices.AnswerRemoved
	if c.DateUID == "" {
		kind = notices.AnswerRemovedAll
	}
	// R44: told plainly, at the bell, with no reason; a guest by email too (R47).
	return notices.Add(tx, notices.New{PersonID: personID, Kind: kind, ActorID: tx.Actor.Person.ID, EventID: e.ID, DateID: first, Guest: guest})
}
