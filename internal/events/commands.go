package events

import (
	"context"
	"errors"
	"strconv"

	"fold/internal/groups"
	"fold/internal/people"
	"fold/internal/policy"
	"fold/internal/settings"
	"fold/internal/write"
)

// Input is what the event form saves. The form checks lengths and shapes;
// the commands check what policy and the clock decide.
type Input struct {
	Title, About, Place, Visibility string
	Pattern                         Pattern
}

func actor(tx *write.Tx) *policy.Person {
	if tx.Actor.Person == nil {
		return nil
	}
	return tx.Actor.Person.Policy()
}

// groupOf loads an event's group and the actor's relation to it, inside the
// write, so policy is asked with facts no older than the change (#164).
func groupOf(tx *write.Tx, groupID int64) (groups.Group, policy.Rel, error) {
	g, err := groups.ByID(tx.Context(), tx, groupID)
	if errors.Is(err, groups.ErrNotFound) {
		return groups.Group{}, policy.Rel{}, write.ErrNotAllowed
	}
	if err != nil {
		return groups.Group{}, policy.Rel{}, err
	}
	var rel policy.Rel
	if tx.Actor.Person != nil {
		if rel, err = groups.RelOf(tx.Context(), tx, g.ID, tx.Actor.Person.ID); err != nil {
			return groups.Group{}, policy.Rel{}, err
		}
	}
	return g, rel, nil
}

func insertDates(tx *write.Tx, eventID int64, ds []Date) error {
	for _, d := range ds {
		if _, err := tx.Exec(`INSERT INTO dates (uid, event_id, day, starts_at, ends_at, starts_local, moved) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			people.NewUID(), eventID, d.Day, d.StartsAt.Unix(), d.EndsAt.Unix(), d.StartsLocal, d.Moved); err != nil {
			return err
		}
	}
	return nil
}

func nullable[T comparable](v T) any {
	var zero T
	if v == zero {
		return nil
	}
	return v
}

// Create schedules an event for a group, in the church's zone (DEC-50),
// with its dates stored to the horizon. It starts with its group's
// visibility or something more private (DEC-48).
type Create struct {
	GroupUID string
	Input
	UID string // set on success
}

func (*Create) Action() string { return "event.create" }

func (c *Create) Apply(tx *write.Tx) error {
	g, err := groups.ByUID(tx.Context(), tx, c.GroupUID)
	if errors.Is(err, groups.ErrNotFound) {
		return write.ErrNotAllowed
	}
	if err != nil {
		return err
	}
	_, rel, err := groupOf(tx, g.ID)
	if err != nil {
		return err
	}
	if !policy.CanScheduleEvent(actor(tx), g.Policy(), rel) || !policy.CanSetEventVisibility(g.Visibility, c.Visibility, g.Visibility) {
		return write.ErrNotAllowed
	}
	s, err := settings.Load(tx.Context(), tx)
	if err != nil {
		return err
	}
	zone := s[settings.ChurchZone]
	loc, err := LoadZone(zone)
	if err != nil {
		return err
	}
	p := c.Pattern
	if err := CheckStart(p, loc, tx.Now); err != nil {
		return err
	}
	ds, err := Ahead(p, loc, tx.Now)
	if err != nil {
		return err
	}
	c.UID = people.NewUID()
	res, err := tx.Exec(`INSERT INTO events (uid, group_id, title, about, place, visibility, zone, starts_local, minutes, repeat, month_week, until,
		created_by, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.UID, g.ID, c.Title, c.About, c.Place, c.Visibility, zone, p.Start, p.Minutes, p.Repeat, nullable(p.MonthWeek), nullable(p.Until),
		tx.Actor.Person.ID, tx.Now.Unix(), tx.Now.Unix())
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	if err := insertDates(tx, id, ds); err != nil {
		return err
	}
	tx.Subject(c.UID)
	tx.Subject(g.UID)
	tx.Detail("repeat", p.Repeat)
	tx.Detail("visibility", c.Visibility)
	return nil
}

// target loads an event that is not deleted, its group and the actor's
// relation to it, and refuses anyone policy does not let run it.
func target(tx *write.Tx, uid string) (Event, groups.Group, error) {
	e, err := ByUID(tx.Context(), tx, uid)
	if errors.Is(err, ErrNotFound) {
		return Event{}, groups.Group{}, write.ErrNotAllowed
	}
	if err != nil {
		return Event{}, groups.Group{}, err
	}
	g, rel, err := groupOf(tx, e.GroupID)
	if err != nil {
		return Event{}, groups.Group{}, err
	}
	if !policy.CanManageEvent(actor(tx), e.Policy(), g.Policy(), rel) {
		return Event{}, groups.Group{}, write.ErrNotAllowed
	}
	return e, g, nil
}

// ErrDropsDates is a series edit that would cancel scheduled dates, which
// the person has not yet been asked about. Nothing is saved; Edit.Drops
// says how many.
var ErrDropsDates = errors.New("events: the edit drops dates")

// Edit saves an event and edits its series onto its future dates (DEC-91,
// DEC-161). The form carries the version it was opened with; if the event
// changed since, nothing is saved (P16). An edit that would cancel dates is
// refused until the person has seen how many (docs/HAZARDS.md section 7).
// The zone never changes: edit forms use the event's own (DEC-57).
type Edit struct {
	UID string
	Input
	Version   int64
	Confirmed bool // the person saw that dates would be cancelled
	Drops     int  // set when the edit would cancel dates
}

func (*Edit) Action() string { return "event.edit" }

func (c *Edit) Apply(tx *write.Tx) error {
	e, g, err := target(tx, c.UID)
	if err != nil {
		return err
	}
	if e.Version != c.Version {
		return write.ErrConflict
	}
	if !policy.CanSetEventVisibility(e.Visibility, c.Visibility, g.Visibility) {
		return write.ErrNotAllowed
	}
	loc, err := LoadZone(e.Zone)
	if err != nil {
		return err
	}
	p := c.Pattern
	// Only a new first day is bounded, so an event that began over a year
	// ago can still change its time.
	newDay, err := p.StartDay()
	if err != nil {
		return err
	}
	if oldDay, _ := e.Pattern().StartDay(); !newDay.Equal(oldDay) {
		if err := CheckStart(p, loc, tx.Now); err != nil {
			return err
		}
	}
	next, err := Ahead(p, loc, tx.Now)
	if err != nil {
		return err
	}
	have, err := stored(tx.Context(), tx, e.ID)
	if err != nil {
		return err
	}
	plan := PlanSeries(have, next, tx.Now)
	if len(plan.Cancel) > 0 && !c.Confirmed {
		c.Drops = len(plan.Cancel)
		return ErrDropsDates
	}
	if _, err := tx.Exec(`UPDATE events SET title = ?, about = ?, place = ?, visibility = ?, starts_local = ?, minutes = ?, repeat = ?,
		month_week = ?, until = ?, updated_at = ?, version = version + 1 WHERE id = ?`,
		c.Title, c.About, c.Place, c.Visibility, p.Start, p.Minutes, p.Repeat, nullable(p.MonthWeek), nullable(p.Until), tx.Now.Unix(), e.ID); err != nil {
		return err
	}
	for _, ch := range append(plan.Move, plan.Restore...) {
		if _, err := tx.Exec(`UPDATE dates SET starts_at = ?, ends_at = ?, starts_local = ?, moved = ?, status = 'scheduled', cancelled_by = NULL WHERE id = ?`,
			ch.Date.StartsAt.Unix(), ch.Date.EndsAt.Unix(), ch.Date.StartsLocal, ch.Date.Moved, ch.ID); err != nil {
			return err
		}
	}
	for _, id := range plan.Cancel {
		if _, err := tx.Exec(`UPDATE dates SET status = 'cancelled', cancelled_by = 'series' WHERE id = ?`, id); err != nil {
			return err
		}
	}
	if err := insertDates(tx, e.ID, plan.Add); err != nil {
		return err
	}
	tx.Subject(e.UID)
	for k, n := range map[string]int{"moved": len(plan.Move), "cancelled": len(plan.Cancel), "restored": len(plan.Restore), "added": len(plan.Add)} {
		if n > 0 {
			tx.Detail(k, strconv.Itoa(n))
		}
	}
	if c.Visibility != e.Visibility {
		tx.Detail("visibility", c.Visibility)
		tx.Detail("was", e.Visibility)
	}
	return nil
}

// Delete deletes an event. Its row and dates stay, marked by the event, and
// nothing shows them again. Telling the people signed up arrives with
// answers (milestone 2, DEC-175).
type Delete struct{ UID string }

func (Delete) Action() string { return "event.delete" }

func (c Delete) Apply(tx *write.Tx) error {
	e, _, err := target(tx, c.UID)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE events SET status = 'deleted', updated_at = ?, version = version + 1 WHERE id = ?`, tx.Now.Unix(), e.ID); err != nil {
		return err
	}
	tx.Subject(e.UID)
	return nil
}

// JobExtend keeps every repeating event's dates stored 18 months ahead.
const JobExtend = "events.extend"

// extend adds the days the horizon has reached. It only adds, so running it
// twice is the same as once; it is upkeep, not something a person did (R19).
type extend struct{}

func (extend) Action() string { return "event.extend" }
func (extend) Upkeep()        {}

func (extend) Apply(tx *write.Tx) error {
	rows, err := tx.Query(`SELECT ` + eventColumns + ` FROM events e JOIN groups g ON g.id = e.group_id
		WHERE e.status = 'active' AND g.status <> 'deleted' AND e.repeat <> 'once'`)
	if err != nil {
		return err
	}
	var list []Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			rows.Close()
			return err
		}
		list = append(list, e)
	}
	rows.Close()
	added := int64(0)
	for _, e := range list {
		loc, err := LoadZone(e.Zone)
		if err != nil {
			continue // a zone this build no longer knows keeps the dates it has
		}
		ds, err := Ahead(e.Pattern(), loc, tx.Now)
		if err != nil {
			continue
		}
		for _, d := range ds {
			if !d.StartsAt.After(tx.Now) {
				continue
			}
			res, err := tx.Exec(`INSERT INTO dates (uid, event_id, day, starts_at, ends_at, starts_local, moved) VALUES (?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (event_id, day) DO NOTHING`, people.NewUID(), e.ID, d.Day, d.StartsAt.Unix(), d.EndsAt.Unix(), d.StartsLocal, d.Moved)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			added += n
		}
	}
	if added == 0 {
		return write.ErrNothing
	}
	return nil
}

// Extend is the JobExtend handler.
func Extend(e *write.Executor) write.Handler {
	return func(ctx context.Context, _ write.Job) error { return e.Run(ctx, write.System(), extend{}) }
}
