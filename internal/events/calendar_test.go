package events_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"fold/internal/events"
	"fold/internal/groups"
	"fold/internal/policy"
	"fold/internal/testenv"
)

// The calendar's SQL is policy.CanSeeDate and policy.InScope together, for
// every kind of viewer, Just mine or not, every answer, church-wide or a
// group's, public or opened, each edge of the window, and with cancelled
// dates or without; a row names its group only to someone who may see the
// group, and carries the description and video link only as policy allows.
func TestCalendarSQLMatchesPolicy(t *testing.T) {
	env := setup(t)
	ctx := context.Background()
	now := testenv.Start
	exec(t, env, `INSERT INTO people (id, uid, name, email, status, created_at, updated_at) VALUES (7, 'p7', 'Guest', 'p7@example.org', 'guest', 0, 0)`)
	times := []struct{ start, end time.Duration }{
		{7 * 24 * time.Hour, 7*24*time.Hour + time.Hour}, // inside the window
		{policy.Window, policy.Window + time.Hour},       // its far edge
		{policy.Window + time.Second, policy.Window + time.Hour},
		{60 * 24 * time.Hour, 60*24*time.Hour + time.Hour}, // well beyond
		{-time.Hour, time.Second},                          // underway
		{-48 * time.Hour, -47 * time.Hour},                 // ended
	}
	answersFor := []string{"", "going", "maybe", "waiting", "cant", "cancelled"}
	type made struct {
		id, gid, eventID int64
		uid, dvis        string
		g                policy.Group
		e                events.Event
		cancelled        bool
		start, end       time.Time
	}
	var all []made
	add := func(gid int64, g policy.Group, ev string) {
		res, err := env.Writer.Exec(`INSERT INTO events (uid, group_id, title, about, video_url, visibility, zone, starts_local, minutes, repeat, created_at, updated_at)
			VALUES (?, ?, 'x', 'about', 'https://v.example.org', ?, 'America/Boise', '2026-12-01T19:00', 60, 'once', 0, 0)`,
			fmt.Sprintf("e%d", len(all)), sql.NullInt64{Int64: gid, Valid: gid != 0}, ev)
		if err != nil {
			t.Fatal(err)
		}
		eid, _ := res.LastInsertId()
		if eid%2 == 0 { // person 2 hosts every other event; on a group's event that grants nothing
			exec(t, env, `INSERT INTO event_hosts (event_id, person_id, created_at) VALUES (?, 2, 0)`, eid)
		}
		n := 0
		for _, dv := range []string{"", policy.Public} {
			for ti, tm := range times {
				for _, cancelled := range []bool{false, true} {
					if cancelled && ti > 0 {
						continue
					}
					n++
					uid := fmt.Sprintf("d%d-%d", eid, n)
					status, by := "scheduled", sql.NullString{}
					if cancelled {
						status, by = "cancelled", sql.NullString{String: "person", Valid: true}
					}
					start, end := now.Add(tm.start), now.Add(tm.end)
					res, err := env.Writer.Exec(`INSERT INTO dates (uid, event_id, day, starts_at, ends_at, starts_local, visibility, status, cancelled_by)
						VALUES (?, ?, ?, ?, ?, '2026-12-01T19:00', ?, ?, ?)`, uid, eid, fmt.Sprintf("day-%d", n), start.Unix(), end.Unix(),
						sql.NullString{String: dv, Valid: dv != ""}, status, by)
					if err != nil {
						t.Fatal(err)
					}
					did, _ := res.LastInsertId()
					all = append(all, made{did, gid, eid, uid, dv, g, events.Event{ID: eid, GroupID: gid, Status: policy.EventActive, Visibility: ev}, cancelled, start, end})
				}
			}
		}
	}
	gid := int64(0)
	for _, g := range []policy.Group{{Status: policy.GroupActive, Visibility: policy.Public}, {Status: policy.GroupActive, Visibility: policy.InviteOnly},
		{Status: policy.GroupArchived, Visibility: policy.Church}} {
		gid++
		addGroup(t, env, int(gid), g.Status, g.Visibility)
		for _, ev := range []string{policy.Public, policy.Church, policy.InviteOnly} {
			add(gid, g, ev)
		}
	}
	for _, ev := range []string{policy.Public, policy.Church, policy.Public, policy.Church} {
		add(0, policy.Group{}, ev)
	}
	// Every viewer answers every date, a different answer on each.
	for i, m := range all {
		for person := 2; person <= 7; person++ {
			if a := answersFor[(i+person)%len(answersFor)]; a != "" {
				exec(t, env, `INSERT INTO answers (date_id, person_id, status, created_at, updated_at) VALUES (?, ?, ?, 0, 0)`, m.id, person, a)
			}
		}
	}
	for viewer := 0; viewer <= 7; viewer++ {
		s := events.Scope{Viewer: events.Viewer{ID: int64(viewer), Admin: viewer == 1}, Answerer: int64(viewer)}
		kind := policy.ViewerMember
		var p *policy.Person
		switch viewer {
		case 0:
			kind = policy.ViewerSignedOut
		case 7: // a guest sees as a signed-out visitor
			kind, s.Viewer = policy.ViewerGuest, events.Viewer{}
		default:
			p = &policy.Person{ID: int64(viewer), Admin: viewer == 1}
		}
		if s.Kind() != kind {
			t.Fatalf("viewer %d is kind %d", viewer, s.Kind())
		}
		for _, mode := range []struct{ mine, cancelled bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
			mine := mode.mine
			s.JustMine, s.Cancelled, s.Details = mine, mode.cancelled, mode.cancelled
			list, err := events.Calendar(ctx, env.Writer, s, now.Add(-30*24*time.Hour), now.Add(100*24*time.Hour), time.Unix(0, 0), now, 100000)
			if err != nil {
				t.Fatal(err)
			}
			listed := map[string]events.CalDate{}
			for _, c := range list {
				listed[c.UID] = c
			}
			for _, m := range all {
				var r policy.Rel
				var host bool
				if p != nil {
					if m.gid != 0 {
						r, _ = groups.RelOf(ctx, env.Writer, m.gid, int64(viewer))
					}
					host, _ = events.IsHost(ctx, env.Writer, m.eventID, int64(viewer))
				}
				var answer string
				env.Writer.QueryRow(`SELECT coalesce((SELECT status FROM answers WHERE date_id = ? AND person_id = ?), '')`, m.id, viewer).Scan(&answer)
				f := policy.CalendarFacts{Seen: (mode.cancelled || !m.cancelled) && policy.CanSeeDate(p, m.e.Policy(host), m.dvis, m.g, r), Viewer: kind,
					InGroup: r.Member, ChurchWide: m.gid == 0, Host: host && m.gid == 0, Answer: answer,
					Public: policy.Widest(m.e.Visibility, m.dvis) == policy.Public, StartsAt: m.start, EndsAt: m.end}
				want, _ := policy.InScope(mine, f, now)
				c, got := listed[m.uid]
				if got != want {
					t.Errorf("viewer %d, mine %v, %+v: listed %v, policy %v", viewer, mine, f, got, want)
					continue
				}
				if !got {
					continue
				}
				if c.Facts(kind) != f {
					t.Errorf("viewer %d, date %s: facts %+v, want %+v", viewer, m.uid, c.Facts(kind), f)
				}
				if named := c.GroupName != ""; named != (m.gid != 0 && policy.CanSeeGroup(p, m.g, r)) {
					t.Errorf("viewer %d, date %s of group %+v: named %v", viewer, m.uid, m.g, named)
				}
				if c.Cancelled != m.cancelled {
					t.Errorf("viewer %d, date %s: cancelled %v", viewer, m.uid, c.Cancelled)
				}
				if !mode.cancelled {
					continue
				}
				ev := m.e.Policy(host)
				if about := c.About != ""; about != policy.CanSeeEvent(p, ev, m.g, r) {
					t.Errorf("viewer %d, date %s, event %+v: description %v", viewer, m.uid, ev, about)
				}
				if video := c.Video != ""; video != (!m.cancelled && policy.CanSeeVideo(policy.CanManageEvent(p, ev, m.g, r), answer)) {
					t.Errorf("viewer %d, date %s, answer %q: video %v", viewer, m.uid, answer, video)
				}
			}
		}
	}
}

// Scope and visibility are filtered before the limit: a guest's own
// answered date far ahead comes after hidden dates, and one since made
// private is gone.
func TestCalendarLimitsAfterScope(t *testing.T) {
	env := setup(t)
	ctx := context.Background()
	exec(t, env, `INSERT INTO people (id, uid, name, email, status, created_at, updated_at) VALUES (7, 'p7', 'Guest', 'p7@example.org', 'guest', 0, 0)`)
	addGroup(t, env, 1, policy.GroupActive, policy.Public)
	hidden := &events.Create{GroupUID: "g1", Input: weekly("2026-12-02T19:00")}
	hidden.Visibility = policy.InviteOnly
	open := &events.Create{GroupUID: "g1", Input: weekly("2027-03-03T19:00")}
	gone := &events.Create{GroupUID: "g1", Input: weekly("2027-03-04T19:00")}
	for _, c := range []*events.Create{hidden, open, gone} {
		if err := run(t, env, 2, c); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []*events.Create{open, gone} {
		exec(t, env, `INSERT INTO answers (date_id, person_id, status, created_at, updated_at)
			SELECT d.id, 7, 'going', 0, 0 FROM dates d JOIN events e ON e.id = d.event_id WHERE e.uid = ? ORDER BY d.starts_at LIMIT 1`, c.UID)
	}
	exec(t, env, `UPDATE events SET visibility = 'church' WHERE uid = ?`, gone.UID)
	guest := events.Scope{Answerer: 7, JustMine: true}
	list, err := events.Calendar(ctx, env.Writer, guest, testenv.Start, testenv.Start.AddDate(1, 0, 0), testenv.Start, testenv.Start, 1)
	if err != nil || len(list) != 1 || list[0].Answer != "going" || list[0].UID != dateUID(t, env, open.UID, "2027-03-03") {
		t.Fatalf("a guest's first date: %v %+v", err, list)
	}
}

// Home's groups meeting soon are the groups policy.CanJoinGroup lets the
// viewer join and they are not in, each by its next date they may see: a
// hidden earlier date neither lists a group nor gives its time.
func TestJoinableSoonMatchesPolicy(t *testing.T) {
	env := setup(t)
	ctx := context.Background()
	type made struct {
		uid string
		gid int64
		g   policy.Group
	}
	var all []made
	n := 0
	for _, status := range []string{policy.GroupPending, policy.GroupActive, policy.GroupArchived} {
		for _, vis := range []string{policy.Public, policy.Church, policy.InviteOnly} {
			n++
			uid := addGroup(t, env, n, status, vis)
			all = append(all, made{uid, int64(n), policy.Group{Status: status, Visibility: vis}})
			// A hidden date first, then one its group's visibility shows.
			for i, ev := range []string{policy.InviteOnly, vis} {
				res, err := env.Writer.Exec(`INSERT INTO events (uid, group_id, title, visibility, zone, starts_local, minutes, repeat, created_at, updated_at)
					VALUES (?, ?, 'x', ?, 'America/Boise', '2026-12-01T19:00', 60, 'once', 0, 0)`, fmt.Sprintf("e%d-%d", n, i), n, ev)
				if err != nil {
					t.Fatal(err)
				}
				eid, _ := res.LastInsertId()
				start := testenv.Start.Add(time.Duration(n*10+i) * time.Hour)
				exec(t, env, `INSERT INTO dates (uid, event_id, day, starts_at, ends_at, starts_local) VALUES (?, ?, '2026-12-01', ?, ?, '2026-12-01T19:00')`,
					fmt.Sprintf("d%d-%d", n, i), eid, start.Unix(), start.Add(time.Hour).Unix())
			}
		}
	}
	for viewer := 1; viewer <= 6; viewer++ {
		p := &policy.Person{ID: int64(viewer), Admin: viewer == 1}
		list, err := events.JoinableSoon(ctx, env.Writer, events.Viewer{ID: int64(viewer), Admin: viewer == 1}, testenv.Start, 100)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]time.Time{}
		for _, s := range list {
			got[s.UID] = s.Next
		}
		for i, m := range all {
			r, _ := groups.RelOf(ctx, env.Writer, m.gid, int64(viewer))
			want := policy.CanJoinGroup(p, m.g, r) && !r.Member
			next, listed := got[m.uid]
			if listed != want {
				t.Errorf("viewer %d, group %+v %+v: listed %v, policy %v", viewer, m.g, r, listed, want)
			}
			seesHidden := viewer == 1
			if first := testenv.Start.Add(time.Duration((i+1)*10) * time.Hour); listed && !seesHidden && !next.Equal(first.Add(time.Hour)) {
				t.Errorf("viewer %d, group %+v: next %v, the hidden date's time", viewer, m.g, next)
			}
		}
	}
	if list, _ := events.JoinableSoon(ctx, env.Writer, events.Viewer{}, testenv.Start, 100); len(list) != 0 {
		t.Fatalf("signed out: %+v", list)
	}
}
