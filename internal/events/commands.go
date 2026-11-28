package events

import (
	"context"
	"database/sql"
	"errors"
	"strconv"

	"fold/internal/groups"
	"fold/internal/notices"
	"fold/internal/people"
	"fold/internal/policy"
	"fold/internal/settings"
	"fold/internal/write"
)

// MaxReason is the most an opening's reason may say (like R31's note).
const MaxReason = 500

// Input is what the event form saves. The form checks lengths and shapes;
// the commands check what policy and the clock decide.
type Input struct {
	Title, About, Place, Visibility string
	Pattern                         Pattern
	// Reason is why it is opened wider than its group, if it is (DEC-28).
	Reason string
	// VideoURL is an https link out to a video call, or "" (DEC-16, R41).
	VideoURL string
}

func actor(tx *write.Tx) *policy.Person {
	if tx.Actor.Person == nil {
		return nil
	}
	return tx.Actor.Person.Policy()
}

// groupOf loads a group and the actor's relation to it, inside the write, so
// policy is asked with facts no older than the change (#164). A church-wide
// event has none: id 0 gives the zero group.
func groupOf(tx *write.Tx, groupID int64) (groups.Group, policy.Rel, error) {
	if groupID == 0 {
		return groups.Group{}, policy.Rel{}, nil
	}
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

// tellOpened tells a group's other leaders and every admin that an event, or
// one date of it, was opened wider than the group (DEC-6, DEC-28). The
// reason goes into the audit row with everyone it reached (R30).
func tellOpened(tx *write.Tx, groupID, eventID, dateID int64, reason string) error {
	rows, err := tx.Query(`SELECT p.id, p.uid FROM people p WHERE p.status = 'active' AND p.id <> ? AND (p.is_admin = 1
		OR EXISTS (SELECT 1 FROM active_memberships am WHERE am.group_id = ? AND am.person_id = p.id AND am.role = 'leader'))
		ORDER BY p.id`, tx.Actor.Person.ID, groupID)
	if err != nil {
		return err
	}
	type to struct {
		id  int64
		uid string
	}
	var list []to
	for rows.Next() {
		var t to
		rows.Scan(&t.id, &t.uid)
		list = append(list, t)
	}
	rows.Close()
	tx.Detail("opened", "1")
	tx.Text(reason)
	for _, t := range list {
		tx.Subject(t.uid)
		if err := notices.Add(tx, notices.New{PersonID: t.id, Kind: notices.EventOpened, ActorID: tx.Actor.Person.ID,
			GroupID: groupID, EventID: eventID, DateID: dateID}); err != nil {
			return err
		}
	}
	return nil
}

// Create schedules an event, in the church's zone (DEC-50), with its dates
// stored to the horizon. With a GroupUID it is that group's, made by its
// leaders or admins, and starts with the group's visibility unless they
// choose otherwise (DEC-48); opening it wider than the group is told. With
// none it is church-wide, made by an admin (DEC-7).
type Create struct {
	GroupUID string
	Input
	UID string // set on success
}

func (*Create) Action() string { return "event.create" }

func (c *Create) Apply(tx *write.Tx) error {
	var g groups.Group
	churchWide := c.GroupUID == ""
	if churchWide {
		if !policy.CanCreateChurchEvent(actor(tx)) {
			return write.ErrNotAllowed
		}
	} else {
		var err error
		g, err = groups.ByUID(tx.Context(), tx, c.GroupUID)
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
		if !policy.CanScheduleEvent(actor(tx), g.Policy(), rel) {
			return write.ErrNotAllowed
		}
	}
	if !policy.CanSetEventVisibility(c.Visibility, churchWide) {
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
	opened := policy.OpensWider(g.Visibility, c.Visibility, g.Visibility, churchWide)
	reason := ""
	if opened {
		reason = c.Reason
	}
	c.UID = people.NewUID()
	res, err := tx.Exec(`INSERT INTO events (uid, group_id, title, about, place, visibility, zone, starts_local, minutes, repeat, month_week, until,
		open_reason, video_url, created_by, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.UID, nullable(g.ID), c.Title, c.About, c.Place, c.Visibility, zone, p.Start, p.Minutes, p.Repeat, nullable(p.MonthWeek), nullable(p.Until),
		reason, c.VideoURL, tx.Actor.Person.ID, tx.Now.Unix(), tx.Now.Unix())
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	if err := insertDates(tx, id, ds); err != nil {
		return err
	}
	tx.Subject(c.UID)
	if !churchWide {
		tx.Subject(g.UID)
	}
	tx.Detail("repeat", p.Repeat)
	tx.Detail("visibility", c.Visibility)
	if opened {
		return tellOpened(tx, g.ID, id, 0, reason)
	}
	return nil
}

// target loads an event that is not deleted and its group, and refuses
// anyone policy does not let run it. A host's row and account are read here,
// inside the write, so a disabled or removed host is refused at once.
func target(tx *write.Tx, uid string) (Event, groups.Group, error) {
	e, err := ByUID(tx.Context(), tx, uid)
	if errors.Is(err, ErrNotFound) {
		return Event{}, groups.Group{}, write.ErrNotAllowed
	}
	if err != nil {
		return Event{}, groups.Group{}, err
	}
	return check(tx, e)
}

func check(tx *write.Tx, e Event) (Event, groups.Group, error) {
	g, rel, err := groupOf(tx, e.GroupID)
	if err != nil {
		return Event{}, groups.Group{}, err
	}
	host := false
	if e.ChurchWide() && tx.Actor.Person != nil {
		if host, err = IsHost(tx.Context(), tx, e.ID, tx.Actor.Person.ID); err != nil {
			return Event{}, groups.Group{}, err
		}
	}
	if !policy.CanManageEvent(actor(tx), e.Policy(host), g.Policy(), rel) {
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
	if !policy.CanSetEventVisibility(c.Visibility, e.ChurchWide()) {
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
	opened := policy.OpensWider(e.Visibility, c.Visibility, g.Visibility, e.ChurchWide())
	reason := e.OpenReason // kept while it stays wider; a title edit tells nobody
	switch {
	case opened:
		reason = c.Reason
	case !policy.MoreOpen(c.Visibility, g.Visibility) || e.ChurchWide():
		reason = ""
	}
	if _, err := tx.Exec(`UPDATE events SET title = ?, about = ?, place = ?, visibility = ?, starts_local = ?, minutes = ?, repeat = ?,
		month_week = ?, until = ?, open_reason = ?, video_url = ?, updated_at = ?, version = version + 1 WHERE id = ?`,
		c.Title, c.About, c.Place, c.Visibility, p.Start, p.Minutes, p.Repeat, nullable(p.MonthWeek), nullable(p.Until), reason, c.VideoURL,
		tx.Now.Unix(), e.ID); err != nil {
		return err
	}
	for _, ch := range append(plan.Move, plan.Restore...) {
		if err := retime(tx, ch.ID, ch.Date); err != nil {
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
	if opened {
		return tellOpened(tx, g.ID, e.ID, 0, reason)
	}
	return nil
}

// retime puts a date on new times and schedules it.
func retime(tx *write.Tx, id int64, d Date) error {
	_, err := tx.Exec(`UPDATE dates SET starts_at = ?, ends_at = ?, starts_local = ?, moved = ?, status = 'scheduled', cancelled_by = NULL WHERE id = ?`,
		d.StartsAt.Unix(), d.EndsAt.Unix(), d.StartsLocal, d.Moved, id)
	return err
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

// dateTarget loads one date and its event, and refuses anyone who may not
// run the event.
func dateTarget(tx *write.Tx, uid string) (Row, Event, groups.Group, error) {
	d, e, err := DateByUID(tx.Context(), tx, uid)
	if errors.Is(err, ErrNotFound) {
		return Row{}, Event{}, groups.Group{}, write.ErrNotAllowed
	}
	if err != nil {
		return Row{}, Event{}, groups.Group{}, err
	}
	e, g, err := check(tx, e)
	if err != nil {
		return Row{}, Event{}, groups.Group{}, err
	}
	tx.Subject(d.UID)
	tx.Subject(e.UID)
	return d, e, g, nil
}

// ErrDateGone is a date-by-date change that no longer applies: the date has
// begun, or was cancelled or restored since the page was drawn.
var ErrDateGone = errors.New("events: that date can't be changed now")

// ErrUnchanged: the host was already named, or already gone. Nothing is
// written, and the page says so.
var ErrUnchanged = errors.New("events: already so")

// CancelDate cancels one date by hand (P7). A series edit never moves it or
// brings it back; only RestoreDate does (DEC-91).
type CancelDate struct{ UID string }

func (CancelDate) Action() string { return "date.cancel" }

func (c CancelDate) Apply(tx *write.Tx) error {
	d, _, _, err := dateTarget(tx, c.UID)
	if err != nil {
		return err
	}
	if !policy.CanCancelDate(true, d.State(tx.Now)) {
		return ErrDateGone
	}
	_, err = tx.Exec(`UPDATE dates SET status = 'cancelled', cancelled_by = 'person' WHERE id = ?`, d.ID)
	return err
}

// RestoreDate brings back a date cancelled by hand, on the event's times as
// they are now, while its repeat still makes that day (DEC-91).
type RestoreDate struct{ UID string }

func (RestoreDate) Action() string { return "date.restore" }

func (c RestoreDate) Apply(tx *write.Tx) error {
	d, e, _, err := dateTarget(tx, c.UID)
	if err != nil {
		return err
	}
	var now Date
	made := false
	if loc, err := LoadZone(e.Zone); err == nil {
		ds, err := Ahead(e.Pattern(), loc, tx.Now)
		if err != nil {
			return err
		}
		for _, x := range ds {
			if x.Day == d.Day {
				now, made = x, true
			}
		}
	}
	if !policy.CanRestoreDate(true, d.State(tx.Now), made && now.StartsAt.After(tx.Now)) {
		return ErrDateGone
	}
	return retime(tx, d.ID, now)
}

// OpenDate opens one date wider than its series, or puts it back to the
// series' with "" (P7). It is never narrower than its event. Opening it
// wider than its group is told, with the reason (DEC-28).
type OpenDate struct {
	UID        string
	Visibility string
	Reason     string
}

func (OpenDate) Action() string { return "date.visibility" }

func (c OpenDate) Apply(tx *write.Tx) error {
	d, e, g, err := dateTarget(tx, c.UID)
	if err != nil {
		return err
	}
	if !policy.CanSetDateVisibility(e.Visibility, c.Visibility, e.ChurchWide()) {
		return write.ErrNotAllowed
	}
	if !policy.CanOpenDate(true, d.State(tx.Now)) {
		return ErrDateGone
	}
	was, now := policy.Widest(e.Visibility, d.Visibility), policy.Widest(e.Visibility, c.Visibility)
	opened := policy.OpensWider(was, now, g.Visibility, e.ChurchWide())
	reason := d.OpenReason
	switch {
	case opened:
		reason = c.Reason
	case c.Visibility == "" || !policy.MoreOpen(now, g.Visibility) || e.ChurchWide():
		reason = ""
	}
	if _, err := tx.Exec(`UPDATE dates SET visibility = ?, open_reason = ? WHERE id = ?`, nullable(c.Visibility), reason, d.ID); err != nil {
		return err
	}
	tx.Detail("visibility", now)
	tx.Detail("was", was)
	if opened {
		return tellOpened(tx, g.ID, e.ID, d.ID, reason)
	}
	return nil
}

// AddHost names a host of a church-wide event, who gets a leader's rights
// over it alone and is told (DEC-7).
type AddHost struct{ EventUID, PersonUID string }

func (AddHost) Action() string { return "event.host_add" }

func (c AddHost) Apply(tx *write.Tx) error {
	e, _, err := target(tx, c.EventUID)
	if err != nil {
		return err
	}
	var id int64
	var status string
	err = tx.QueryRow(`SELECT id, status FROM people WHERE uid = ?`, c.PersonUID).Scan(&id, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return write.ErrNotAllowed
	}
	if err != nil {
		return err
	}
	host, err := IsHost(tx.Context(), tx, e.ID, tx.Actor.Person.ID)
	if err != nil {
		return err
	}
	if !policy.CanNameHost(actor(tx), e.Policy(host), &policy.Person{ID: id, Inactive: status != string(people.StatusActive)}) {
		return write.ErrNotAllowed
	}
	res, err := tx.Exec(`INSERT INTO event_hosts (event_id, person_id, created_at) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`, e.ID, id, tx.Now.Unix())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrUnchanged
	}
	tx.Subject(e.UID)
	tx.Subject(c.PersonUID)
	if id == tx.Actor.Person.ID {
		return nil
	}
	return notices.Add(tx, notices.New{PersonID: id, Kind: notices.HostNamed, ActorID: tx.Actor.Person.ID, EventID: e.ID})
}

// RemoveHost takes a host off a church-wide event. The last may go; admins
// still run it.
type RemoveHost struct{ EventUID, PersonUID string }

func (RemoveHost) Action() string { return "event.host_remove" }

func (c RemoveHost) Apply(tx *write.Tx) error {
	e, _, err := target(tx, c.EventUID)
	if err != nil {
		return err
	}
	host, err := IsHost(tx.Context(), tx, e.ID, tx.Actor.Person.ID)
	if err != nil {
		return err
	}
	if !policy.CanRemoveHost(actor(tx), e.Policy(host)) {
		return write.ErrNotAllowed
	}
	res, err := tx.Exec(`DELETE FROM event_hosts WHERE event_id = ? AND person_id = (SELECT id FROM people WHERE uid = ?)`, e.ID, c.PersonUID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrUnchanged
	}
	tx.Subject(e.UID)
	tx.Subject(c.PersonUID)
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
	rows, err := tx.Query(`SELECT ` + eventColumns + ` FROM events e LEFT JOIN groups g ON g.id = e.group_id
		WHERE e.status = 'active' AND (e.group_id IS NULL OR g.status <> 'deleted') AND e.repeat <> 'once'`)
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
