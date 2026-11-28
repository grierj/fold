package events

import (
	"context"
	"database/sql"
	"errors"
	"strconv"

	"fold/internal/answers"
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
	// Seats is the most who may say Going to each date, or 0 for no limit
	// (DEC-95). Past it, Going waits.
	Seats int
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
	if !policy.CanSetEventVisibility(c.Visibility, churchWide) || !policy.ValidSeats(c.Seats) {
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
		open_reason, video_url, seats, created_by, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.UID, nullable(g.ID), c.Title, c.About, c.Place, c.Visibility, zone, p.Start, p.Minutes, p.Repeat, nullable(p.MonthWeek), nullable(p.Until),
		reason, c.VideoURL, nullable(c.Seats), tx.Actor.Person.ID, tx.Now.Unix(), tx.Now.Unix())
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
	if c.Seats > 0 {
		tx.Detail("seats", strconv.Itoa(c.Seats))
	}
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
// and Edit.Told say how many dates and how many people.
var ErrDropsDates = errors.New("events: the edit drops dates")

// ErrMorePrivate is an edit that makes an event or a date more private,
// which the person has not yet been asked about (R3). Nothing is saved;
// Unseen lists who signed up would no longer see it.
var ErrMorePrivate = errors.New("events: the edit makes it more private")

// Edit saves an event and edits its series onto its future dates (DEC-91,
// DEC-161). The form carries the version it was opened with; if the event
// changed since, nothing is saved (P16). An edit that would cancel dates is
// refused until the person has seen how many dates and people, and one
// that makes it more private until they have seen who would lose sight of
// it (docs/HAZARDS.md section 7, R3). Each question has its own answer.
// The people signed up to cancelled dates are told, and answers never
// follow a date to another day (R40); those signed up to dates back on are
// told too (DEC-91). Raising or clearing the seat limit moves the waitlist
// up; lowering it bumps nobody. The zone never changes: edit forms use the
// event's own (DEC-57).
type Edit struct {
	UID string
	Input
	Version   int64
	Confirmed bool     // the person saw that dates would be cancelled
	Private   bool     // the person saw who would no longer see it (R3)
	Drops     int      // set when the edit would cancel dates
	Told      int      // with Drops: how many people that tells
	Unseen    []Unseen // set with ErrMorePrivate
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
	if !policy.ValidSeats(c.Seats) {
		return write.ErrNotAllowed
	}
	plan := PlanSeries(have, next, tx.Now)
	var dropped []Signed
	if len(plan.Cancel) > 0 {
		where, args := dateIDs(plan.Cancel)
		if dropped, err = signedUp(tx.Context(), tx, e, g, where, args...); err != nil {
			return err
		}
	}
	dropKind := notices.DatesDropped
	if len(plan.Add) > 0 {
		dropKind = notices.DatesMoved
	}
	told := audience(dropped, e, g, c.Visibility, tx.Actor.Person.ID, dropKind)
	if len(plan.Cancel) > 0 && !c.Confirmed {
		c.Drops, c.Told = len(plan.Cancel), len(told)
		return ErrDropsDates
	}
	if policy.MorePrivate(e.Visibility, c.Visibility) && !c.Private {
		list, err := signedUp(tx.Context(), tx, e, g, upcoming, tx.Now.Unix())
		if err != nil {
			return err
		}
		if c.Unseen, err = unseen(tx.Context(), tx, list, e, g, c.Visibility, func(s Signed) string { return s.DateVis }, tx.Now, false); err != nil {
			return err
		}
		return ErrMorePrivate
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
		month_week = ?, until = ?, open_reason = ?, video_url = ?, seats = ?, updated_at = ?, version = version + 1 WHERE id = ?`,
		c.Title, c.About, c.Place, c.Visibility, p.Start, p.Minutes, p.Repeat, nullable(p.MonthWeek), nullable(p.Until), reason, c.VideoURL,
		nullable(c.Seats), tx.Now.Unix(), e.ID); err != nil {
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
	if c.Seats != e.Seats {
		tx.Detail("seats", strconv.Itoa(c.Seats))
	}
	ns := told
	var settle []int64
	if len(plan.Restore) > 0 {
		var back []int64
		for _, ch := range plan.Restore {
			back = append(back, ch.ID)
		}
		where, args := dateIDs(back)
		list, err := signedUp(tx.Context(), tx, e, g, where, args...)
		if err != nil {
			return err
		}
		ns = append(ns, audience(list, e, g, c.Visibility, tx.Actor.Person.ID, notices.DatesRestored)...)
		settle = back
	}
	if c.Seats != e.Seats {
		if settle, err = scanDates(tx, `SELECT d.id FROM dates d WHERE d.event_id = ? AND `+upcoming, e.ID, tx.Now.Unix()); err != nil {
			return err
		}
	}
	if err := tell(tx, ns); err != nil {
		return err
	}
	if err := answers.Settle(tx, settle); err != nil {
		return err
	}
	if opened {
		return tellOpened(tx, g.ID, e.ID, 0, reason)
	}
	return nil
}

func scanDates(tx *write.Tx, query string, args ...any) ([]int64, error) {
	rows, err := tx.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// retime puts a date on new times and schedules it.
func retime(tx *write.Tx, id int64, d Date) error {
	_, err := tx.Exec(`UPDATE dates SET starts_at = ?, ends_at = ?, starts_local = ?, moved = ?, status = 'scheduled', cancelled_by = NULL WHERE id = ?`,
		d.StartsAt.Unix(), d.EndsAt.Unix(), d.StartsLocal, d.Moved, id)
	return err
}

// Delete deletes an event. Its row and dates stay, marked by the event, and
// nothing shows them again. Everyone signed up to a date still to come who
// may see it is told once (DEC-175), as Telling counts them.
type Delete struct{ UID string }

func (Delete) Action() string { return "event.delete" }

func (c Delete) Apply(tx *write.Tx) error {
	e, g, err := target(tx, c.UID)
	if err != nil {
		return err
	}
	list, err := signedUp(tx.Context(), tx, e, g, upcoming, tx.Now.Unix())
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE events SET status = 'deleted', updated_at = ?, version = version + 1 WHERE id = ?`, tx.Now.Unix(), e.ID); err != nil {
		return err
	}
	tx.Subject(e.UID)
	return tell(tx, audience(list, e, g, e.Visibility, tx.Actor.Person.ID, notices.EventDeleted))
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
// brings it back; only RestoreDate does (DEC-91). Its answers stay for
// that, and the people signed up are told, as Telling counts them.
type CancelDate struct{ UID string }

func (CancelDate) Action() string { return "date.cancel" }

func (c CancelDate) Apply(tx *write.Tx) error {
	d, e, g, err := dateTarget(tx, c.UID)
	if err != nil {
		return err
	}
	if !policy.CanCancelDate(true, d.State(tx.Now)) {
		return ErrDateGone
	}
	if _, err = tx.Exec(`UPDATE dates SET status = 'cancelled', cancelled_by = 'person' WHERE id = ?`, d.ID); err != nil {
		return err
	}
	list, err := signedUp(tx.Context(), tx, e, g, `d.id = ?`, d.ID)
	if err != nil {
		return err
	}
	return tell(tx, audience(list, e, g, e.Visibility, tx.Actor.Person.ID, notices.DateCancelled))
}

// RestoreDate brings back a date cancelled by hand, on the event's times as
// they are now, while its repeat still makes that day (DEC-91). Its answers
// come back as they were, a Going over a lowered limit included; the people
// signed up are told, and a free seat moves the waitlist up.
type RestoreDate struct{ UID string }

func (RestoreDate) Action() string { return "date.restore" }

func (c RestoreDate) Apply(tx *write.Tx) error {
	d, e, g, err := dateTarget(tx, c.UID)
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
	if err := retime(tx, d.ID, now); err != nil {
		return err
	}
	list, err := signedUp(tx.Context(), tx, e, g, `d.id = ?`, d.ID)
	if err != nil {
		return err
	}
	if err := tell(tx, audience(list, e, g, e.Visibility, tx.Actor.Person.ID, notices.DatesRestored)); err != nil {
		return err
	}
	return answers.Settle(tx, []int64{d.ID})
}

// OpenDate opens one date wider than its series, or puts it back to the
// series' with "" (P7). It is never narrower than its event. Opening it
// wider than its group is told, with the reason (DEC-28). Closing it again
// removes nobody, and asks first with who would lose sight of it (R3).
type OpenDate struct {
	UID        string
	Visibility string
	Reason     string
	Private    bool     // the person saw who would no longer see it (R3)
	Unseen     []Unseen // set with ErrMorePrivate
}

func (*OpenDate) Action() string { return "date.visibility" }

func (c *OpenDate) Apply(tx *write.Tx) error {
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
	if policy.MorePrivate(was, now) && !c.Private {
		list, err := signedUp(tx.Context(), tx, e, g, `d.id = ?`, d.ID)
		if err != nil {
			return err
		}
		if c.Unseen, err = unseen(tx.Context(), tx, list, e, g, e.Visibility, func(Signed) string { return c.Visibility }, tx.Now, true); err != nil {
			return err
		}
		return ErrMorePrivate
	}
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
