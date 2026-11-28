package answers

import (
	"context"
	"time"

	"fold/internal/db"
	"fold/internal/notices"
	"fold/internal/policy"
)

// Event is what asking who may see an event's dates needs: the event, and
// its group as it stands (the zero group for a church-wide event).
type Event struct {
	ID, GroupID int64
	Visibility  string
	ChurchWide  bool
	Group       policy.Group
}

// Signed is one person with an answer on one date, with what policy needs
// to know whether they may see it.
type Signed struct {
	PersonID int64
	UID      string
	Name     string
	Answer   string
	DateID   int64
	DateVis  string
	StartsAt int64
	person   policy.Person
	guest    bool // asked about as a signed-out visitor (R15)
	rel      policy.Rel
	host     bool
}

// Sees asks policy whether the person may see their date with the event at
// eventVis and the date at dateVis, as if the event were not deleted: a
// delete tells the people who could see it.
func (s Signed) Sees(e Event, eventVis, dateVis string) bool {
	ev := policy.Event{Status: policy.EventActive, Visibility: eventVis, ChurchWide: e.ChurchWide, Host: s.host}
	p := &s.person
	if s.guest {
		p = nil
	}
	return policy.CanSeeDate(p, ev, dateVis, e.Group, s.rel)
}

// Guest: the person is a guest (R46).
func (s Signed) Guest() bool { return s.guest }

// Member: the person is an active member of the event's group.
func (s Signed) Member() bool { return s.rel.Member }

// SignedUp lists the active accounts signed up (Going, Maybe or waiting) to
// an event's dates that match where (over dates d), soonest date first.
func SignedUp(ctx context.Context, q db.Querier, e Event, where string, args ...any) ([]Signed, error) {
	return signed(ctx, q, e, policy.SignedUp, where, "d.starts_at, d.id, p.name COLLATE NOCASE", args...)
}

func signed(ctx context.Context, q db.Querier, e Event, states []string, where, order string, args ...any) ([]Signed, error) {
	rows, err := q.QueryContext(ctx, `SELECT a.person_id, p.uid, p.name, p.is_admin, p.status = 'guest', a.status, d.id, coalesce(d.visibility, ''), d.starts_at,
		coalesce(m.role, ''), coalesce(m.state, ''),
		EXISTS (SELECT 1 FROM event_hosts h WHERE h.event_id = d.event_id AND h.person_id = a.person_id)
		FROM answers a JOIN people p ON p.id = a.person_id JOIN dates d ON d.id = a.date_id
		LEFT JOIN memberships m ON m.group_id = ? AND m.person_id = a.person_id
		WHERE d.event_id = ? AND p.status IN ('active', 'guest') AND a.status IN (`+quoted(states)+`) AND `+where+`
		ORDER BY `+order, append([]any{e.GroupID, e.ID}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Signed
	for rows.Next() {
		var s Signed
		var role, state string
		if err := rows.Scan(&s.PersonID, &s.UID, &s.Name, &s.person.Admin, &s.guest, &s.Answer, &s.DateID, &s.DateVis, &s.StartsAt, &role, &state, &s.host); err != nil {
			return nil, err
		}
		s.person.ID = s.PersonID
		active := state == "active" && e.Group.Status != policy.GroupDeleted
		s.rel = policy.Rel{Member: active, Leader: active && role == "leader", Removed: state == "removed"}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Audience is who a change to dates tells: everyone in list who may see
// their date with the event at eventVis, but the person making the change.
// Each person is told once, about their first date, and by email when any
// of theirs was Going or Maybe (R36). Every confirmation that says how many
// people will be told counts with this.
func Audience(list []Signed, e Event, eventVis string, except int64, kind string) []notices.New {
	at := map[int64]int{}
	var out []notices.New
	for _, s := range list {
		if s.PersonID == except || !s.Sees(e, eventVis, s.DateVis) {
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

// eventOf loads what Audience and Settle need about a date's event.
func eventOf(ctx context.Context, q db.Querier, eventID int64) (Event, error) {
	var e Event
	err := q.QueryRowContext(ctx, `SELECT e.id, coalesce(e.group_id, 0), e.visibility, e.group_id IS NULL, coalesce(g.status, ''), coalesce(g.visibility, '')
		FROM events e LEFT JOIN groups g ON g.id = e.group_id WHERE e.id = ?`, eventID).
		Scan(&e.ID, &e.GroupID, &e.Visibility, &e.ChurchWide, &e.Group.Status, &e.Group.Visibility)
	return e, err
}

// GroupAudience is who deleting a group tells besides its members: everyone
// else signed up to a date still to come of its events, who may see it
// (DEC-177). Each is told once, about their first date, as the event going
// away (R36). It is read before the group is marked deleted, and is what
// the delete confirmation counts.
func GroupAudience(ctx context.Context, q db.Querier, groupID int64, now time.Time, except int64) ([]notices.New, error) {
	rows, err := q.QueryContext(ctx, `SELECT id FROM events WHERE group_id = ? AND status = 'active' ORDER BY id`, groupID)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	type first struct {
		at     int
		starts int64
	}
	seen := map[int64]first{}
	var out []notices.New
	for _, id := range ids {
		e, err := eventOf(ctx, q, id)
		if err != nil {
			return nil, err
		}
		list, err := SignedUp(ctx, q, e, `d.status = 'scheduled' AND d.starts_at > ?`, now.Unix())
		if err != nil {
			return nil, err
		}
		for _, s := range list {
			if s.Member() || s.PersonID == except || !s.Sees(e, e.Visibility, s.DateVis) {
				continue
			}
			f, ok := seen[s.PersonID]
			n := notices.New{PersonID: s.PersonID, Kind: notices.EventDeleted, EventID: e.ID, DateID: s.DateID, Answer: s.Answer}
			switch {
			case !ok:
				seen[s.PersonID] = first{len(out), s.StartsAt}
				out = append(out, n)
				continue
			case s.StartsAt < f.starts:
				if policy.Urgent(out[f.at].Answer) && !policy.Urgent(n.Answer) {
					n.Answer = out[f.at].Answer
				}
				out[f.at] = n
				seen[s.PersonID] = first{f.at, s.StartsAt}
			case policy.Urgent(s.Answer):
				out[f.at].Answer = s.Answer
			}
		}
	}
	return out, nil
}
