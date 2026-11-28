package notices

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"fold/internal/db"
	"fold/internal/policy"
)

// dateSeen is what the notices to people signed up need about one date,
// worded for its reader.
type dateSeen struct {
	eventUID, title, dateUID string
	day                      time.Time
	seesEvent                bool
}

// seeDate loads a date and its event and asks policy whether the reader may
// see the date now, by the same rule as the date page. A deleted event is
// asked about as if it were not deleted, so the people it was taken from
// still read why; nothing else about it is shown. This loads the reader's
// relation as groups.RelOf does (notices cannot import groups); a test holds
// the notices to the pages.
func seeDate(ctx context.Context, q db.Querier, r Reader, eventID, dateID int64, deleted bool) (dateSeen, bool, error) {
	var d dateSeen
	var status, vis, dvis, day, groupStatus, groupVis, role, state string
	var groupID sql.NullInt64
	var host bool
	err := q.QueryRowContext(ctx, `SELECT e.uid, e.title, e.status, e.visibility, e.group_id, d.uid, d.day, coalesce(d.visibility, ''),
		coalesce(g.status, ''), coalesce(g.visibility, ''), coalesce(m.role, ''), coalesce(m.state, ''),
		EXISTS (SELECT 1 FROM event_hosts h WHERE h.event_id = e.id AND h.person_id = ?1)
		FROM dates d JOIN events e ON e.id = d.event_id LEFT JOIN groups g ON g.id = e.group_id
		LEFT JOIN memberships m ON m.group_id = e.group_id AND m.person_id = ?1
		WHERE d.id = ?2 AND e.id = ?3`, r.ID, dateID, eventID).
		Scan(&d.eventUID, &d.title, &status, &vis, &groupID, &d.dateUID, &day, &dvis, &groupStatus, &groupVis, &role, &state, &host)
	if errors.Is(err, sql.ErrNoRows) {
		return d, false, nil
	}
	if err != nil {
		return d, false, err
	}
	if deleted != (status == policy.EventDeleted) {
		return d, false, nil
	}
	d.day, _ = time.Parse("2006-01-02", day)
	p := &policy.Person{ID: r.ID, Admin: r.Admin}
	ev := policy.Event{Status: policy.EventActive, Visibility: vis, ChurchWide: !groupID.Valid, Host: host}
	active := state == "active" && groupStatus != policy.GroupDeleted
	g, rel := policy.Group{Status: groupStatus, Visibility: groupVis}, policy.Rel{Member: active, Leader: active && role == "leader", Removed: state == "removed"}
	d.seesEvent = policy.CanSeeEvent(p, ev, g, rel)
	return d, policy.CanSeeDate(p, ev, dvis, g, rel), nil
}

// wordSignedUp words the notices to the people signed up to a date. Each
// shows only while its reader may still see the date.
func wordSignedUp(ctx context.Context, q db.Querier, r Reader, x row, n Notice) (Notice, bool, error) {
	if !x.event.Valid || !x.date.Valid {
		return n, false, nil
	}
	d, ok, err := seeDate(ctx, q, r, x.event.Int64, x.date.Int64, x.kind == EventDeleted)
	if err != nil || !ok {
		return n, false, err
	}
	quoted := "“" + d.title + "”"
	day := d.day.Format("Monday, January 2")
	n.Href = "/dates/" + d.dateUID
	if d.seesEvent && (x.kind == DatesDropped || x.kind == DatesMoved) {
		n.Href = "/events/" + d.eventUID
	}
	switch x.kind {
	case Promoted:
		n.Text, n.Subject = "A seat opened, so you're going to "+quoted+" on "+day+". If you can't make it now, change your answer so the next person can come.", "You have a seat"
	case NoRoom:
		n.Text, n.Subject = "Your account is on again. "+quoted+" on "+day+" is full now, so your Going there wasn't kept, nor on any later date that filled. Ask a leader if you'd still like to come.", "Some dates are full"
	case DateCancelled:
		n.Text, n.Subject = quoted+" on "+day+" is cancelled.", "A date is cancelled"
	case EventDeleted:
		n.Text, n.Subject, n.Href = quoted+" was deleted, so its dates are off, starting "+day+".", "An event is cancelled", "/"
	case DatesDropped:
		n.Text, n.Subject = quoted+" changed when it meets, so dates you answered are off, starting "+day+".", "Dates are cancelled"
	case DatesMoved:
		n.Text, n.Subject = quoted+" moved to new times, so dates you answered are off, starting "+day+". Answers don't move with it, so answer the new dates if you can come.", "An event moved"
	case DatesRestored:
		n.Text, n.Subject = quoted+" is back on, starting "+day+", with your answer as it was.", "A date is back on"
	}
	return n, true, nil
}
