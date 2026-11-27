package events_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"fold/internal/events"
	"fold/internal/groups"
	"fold/internal/policy"
	"fold/internal/testenv"
	"fold/internal/write"
)

// People in every test: 1 an admin, 2 the leader of every group, 3 a member
// of every group, 4 in none, 5 removed from every group, 6 left every group.
func setup(t *testing.T) *testenv.Env {
	t.Helper()
	env := testenv.New(t)
	exec(t, env, `INSERT INTO settings (key, value, updated_at) VALUES ('church.zone', 'America/Boise', 0)`)
	for id := 1; id <= 6; id++ {
		exec(t, env, `INSERT INTO people (id, uid, name, email, status, is_admin, created_at, updated_at) VALUES (?, ?, ?, ?, 'active', ?, 0, 0)`,
			id, fmt.Sprintf("p%d", id), fmt.Sprintf("Person %d", id), fmt.Sprintf("p%d@example.org", id), id == 1)
		exec(t, env, `INSERT INTO sessions (token_hash, person_id, created_at, renewed_at, expires_at) VALUES (?, ?, 0, 0, ?)`,
			[]byte{byte(id)}, id, testenv.Start.Add(1000*24*time.Hour).Unix())
	}
	return env
}

func exec(t *testing.T, env *testenv.Env, q string, args ...any) {
	t.Helper()
	if _, err := env.Writer.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func addGroup(t *testing.T, env *testenv.Env, id int, status, vis string) string {
	t.Helper()
	uid := fmt.Sprintf("g%d", id)
	exec(t, env, `INSERT INTO groups (id, uid, name, visibility, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, 0, 0)`, id, uid, uid, vis, status)
	for person, row := range map[int][2]string{2: {"leader", "active"}, 3: {"member", "active"}, 5: {"member", "removed"}, 6: {"member", "left"}} {
		exec(t, env, `INSERT INTO memberships (group_id, person_id, role, state, created_at, updated_at) VALUES (?, ?, ?, ?, 0, 0)`, id, person, row[0], row[1])
	}
	return uid
}

func as(id int) write.ActorRef { return write.Session([]byte{byte(id)}) }

func run(t *testing.T, env *testenv.Env, by int, c write.Command) error {
	t.Helper()
	return env.Exec.Run(context.Background(), as(by), c)
}

func weekly(start string) events.Input {
	return events.Input{Title: "Study", Visibility: policy.Public, Pattern: events.Pattern{Start: start, Minutes: 90, Repeat: events.Weekly}}
}

// The SQL filter on every list of dates is policy.CanSeeEvent, for every
// viewer, group status, group and event visibility, and event status.
func TestVisibleSQLMatchesPolicy(t *testing.T) {
	env := setup(t)
	ctx := context.Background()
	type made struct {
		uid string
		g   policy.Group
		e   policy.Event
		gid int64
	}
	var all []made
	n := 0
	vis := []string{policy.Public, policy.Church, policy.InviteOnly}
	for _, status := range []string{policy.GroupPending, policy.GroupActive, policy.GroupArchived, policy.GroupDeleted} {
		for _, gv := range vis {
			n++
			addGroup(t, env, n, status, gv)
			for _, ev := range vis {
				for _, es := range []string{policy.EventActive, policy.EventDeleted} {
					uid := fmt.Sprintf("e-%d-%s-%s", n, ev, es)
					res, err := env.Writer.Exec(`INSERT INTO events (uid, group_id, title, visibility, zone, starts_local, minutes, repeat, status, created_at, updated_at)
						VALUES (?, ?, 'x', ?, 'America/Boise', '2026-12-01T19:00', 60, 'once', ?, 0, 0)`, uid, n, ev, es)
					if err != nil {
						t.Fatal(err)
					}
					id, _ := res.LastInsertId()
					exec(t, env, `INSERT INTO dates (uid, event_id, day, starts_at, ends_at, starts_local) VALUES (?, ?, '2026-12-01', ?, ?, '2026-12-01T19:00')`,
						"d"+uid, id, testenv.Start.Add(time.Hour).Unix(), testenv.Start.Add(2*time.Hour).Unix())
					all = append(all, made{uid, policy.Group{Status: status, Visibility: gv}, policy.Event{Status: es, Visibility: ev}, int64(n)})
				}
			}
		}
	}
	for viewer := 0; viewer <= 6; viewer++ {
		v := events.Viewer{ID: int64(viewer), Admin: viewer == 1}
		var p *policy.Person
		if viewer > 0 {
			p = &policy.Person{ID: int64(viewer), Admin: viewer == 1}
		}
		list, err := events.Upcoming(ctx, env.Writer, v, events.Filter{}, testenv.Start, 10000)
		if err != nil {
			t.Fatal(err)
		}
		listed := map[string]bool{}
		for _, l := range list {
			listed[l.Event] = true
		}
		for _, m := range all {
			r, _ := groups.RelOf(ctx, env.Writer, m.gid, int64(viewer))
			if want := policy.CanSeeEvent(p, m.e, m.g, r); listed[m.uid] != want {
				t.Errorf("viewer %d, group %+v, event %+v: listed %v, policy %v", viewer, m.g, m.e, listed[m.uid], want)
			}
		}
	}
}

// Hidden dates that sort first do not push a visible one past the limit.
func TestUpcomingFiltersBeforeLimiting(t *testing.T) {
	env := setup(t)
	addGroup(t, env, 1, policy.GroupActive, policy.Public)
	c := &events.Create{GroupUID: "g1", Input: weekly("2026-12-02T19:00")}
	c.Visibility = policy.InviteOnly
	if err := run(t, env, 2, c); err != nil {
		t.Fatal(err)
	}
	open := &events.Create{GroupUID: "g1", Input: weekly("2027-06-02T19:00")}
	if err := run(t, env, 2, open); err != nil {
		t.Fatal(err)
	}
	list, err := events.Upcoming(context.Background(), env.Writer, events.Viewer{ID: 4}, events.Filter{}, testenv.Start, 5)
	if err != nil || len(list) != 5 || list[0].Event != open.UID {
		t.Fatalf("an outsider's first five: %v %+v", err, list)
	}
	mine, _ := events.Upcoming(context.Background(), env.Writer, events.Viewer{ID: 3}, events.Filter{}, testenv.Start, 5)
	if len(mine) != 5 || mine[0].Event != c.UID {
		t.Fatalf("a member's first five: %+v", mine)
	}
}

func dateRows(t *testing.T, env *testenv.Env, uid string) map[string]string {
	t.Helper()
	rows, err := env.Writer.Query(`SELECT d.day, d.starts_local, d.status, coalesce(d.cancelled_by, '') FROM dates d JOIN events e ON e.id = d.event_id WHERE e.uid = ?`, uid)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var day, local, status, by string
		rows.Scan(&day, &local, &status, &by)
		out[day] = local[11:] + " " + status + " " + by
	}
	return out
}

func count(m map[string]string, suffix string) int {
	n := 0
	for _, v := range m {
		if len(v) >= len(suffix) && v[len(v)-len(suffix):] == suffix {
			n++
		}
	}
	return n
}

// DEC-91: the series edit moves future dates, cancels the ones it drops
// after asking, brings them back when a later edit makes them again, and
// never touches a date that has started. Testenv's clock is Wed 25 Nov 2026.
func TestSeriesEdit(t *testing.T) {
	env := setup(t)
	addGroup(t, env, 1, policy.GroupActive, policy.Public)
	c := &events.Create{GroupUID: "g1", Input: weekly("2026-11-17T19:00")}
	if err := run(t, env, 2, c); err != nil {
		t.Fatal(err)
	}
	before := dateRows(t, env, c.UID)
	if before["2026-11-17"] != "19:00 scheduled " || before["2026-11-24"] != "19:00 scheduled " || len(before) < 75 {
		t.Fatalf("created: %d dates, %v", len(before), before["2026-11-17"])
	}
	// A later clock: future dates move, the two past ones do not.
	later := weekly("2026-11-17T20:00")
	if err := run(t, env, 2, &events.Edit{UID: c.UID, Input: later, Version: 1}); err != nil {
		t.Fatal(err)
	}
	after := dateRows(t, env, c.UID)
	if after["2026-11-24"] != "19:00 scheduled " || after["2026-12-01"] != "20:00 scheduled " {
		t.Fatalf("moved: %s %s", after["2026-11-24"], after["2026-12-01"])
	}
	// Wednesdays instead drops every future Tuesday: refused until confirmed.
	wed := &events.Edit{UID: c.UID, Input: weekly("2026-11-18T20:00"), Version: 2}
	if err := run(t, env, 2, wed); !errors.Is(err, events.ErrDropsDates) || wed.Drops < 70 {
		t.Fatalf("drops without asking: %v, %d", err, wed.Drops)
	}
	if v := env.Count(t, `SELECT version FROM events WHERE uid = ?`, c.UID); v != 2 {
		t.Fatalf("an unconfirmed edit saved: version %d", v)
	}
	wed.Confirmed = true
	if err := run(t, env, 2, wed); err != nil {
		t.Fatal(err)
	}
	after = dateRows(t, env, c.UID)
	if after["2026-12-01"] != "20:00 cancelled series" || after["2026-12-02"] != "20:00 scheduled " || after["2026-11-24"] != "19:00 scheduled " {
		t.Fatalf("Wednesdays: %s / %s / %s", after["2026-12-01"], after["2026-12-02"], after["2026-11-24"])
	}
	if after["2026-11-18"] != "" {
		t.Fatal("a past day was added")
	}
	// Back to Tuesdays at 7: the Tuesdays return, the Wednesdays go.
	back := &events.Edit{UID: c.UID, Input: weekly("2026-11-17T19:00"), Version: 3, Confirmed: true}
	if err := run(t, env, 2, back); err != nil {
		t.Fatal(err)
	}
	after = dateRows(t, env, c.UID)
	if after["2026-12-01"] != "19:00 scheduled " || after["2026-12-02"] != "20:00 cancelled series" {
		t.Fatalf("restored: %s / %s", after["2026-12-01"], after["2026-12-02"])
	}
	var details string
	env.Writer.QueryRow(`SELECT details FROM audit_log WHERE action = 'event.edit' ORDER BY seq DESC LIMIT 1`).Scan(&details)
	if details == "" || details == "{}" {
		t.Fatal("the edit's audit row has no counts")
	}
}

// docs/HAZARDS.md section 6: clearing the last day really clears it, and the
// series grows back to the horizon.
func TestClearingUntilRegrows(t *testing.T) {
	env := setup(t)
	addGroup(t, env, 1, policy.GroupActive, policy.Public)
	in := weekly("2026-12-01T19:00")
	in.Pattern.Until = "2026-12-15"
	c := &events.Create{GroupUID: "g1", Input: in}
	if err := run(t, env, 2, c); err != nil {
		t.Fatal(err)
	}
	if n := len(dateRows(t, env, c.UID)); n != 3 {
		t.Fatalf("%d dates until the 15th", n)
	}
	if err := run(t, env, 2, &events.Edit{UID: c.UID, Input: weekly("2026-12-01T19:00"), Version: 1}); err != nil {
		t.Fatal(err)
	}
	if n := env.Count(t, `SELECT count(*) FROM events WHERE uid = ? AND until IS NULL`, c.UID); n != 1 {
		t.Fatal("until was not cleared")
	}
	if rows := dateRows(t, env, c.UID); len(rows) < 75 || count(rows, "scheduled ") != len(rows) {
		t.Fatalf("%d dates after clearing until", len(rows))
	}
}

// P16: a stale form saves nothing.
func TestEditConflictSavesNothing(t *testing.T) {
	env := setup(t)
	addGroup(t, env, 1, policy.GroupActive, policy.Public)
	c := &events.Create{GroupUID: "g1", Input: weekly("2026-12-01T19:00")}
	run(t, env, 2, c)
	run(t, env, 2, &events.Edit{UID: c.UID, Input: weekly("2026-12-01T20:00"), Version: 1})
	if err := run(t, env, 1, &events.Edit{UID: c.UID, Input: weekly("2026-12-01T21:00"), Version: 1}); !errors.Is(err, write.ErrConflict) {
		t.Fatalf("stale edit: %v", err)
	}
	if rows := dateRows(t, env, c.UID); rows["2026-12-01"] != "20:00 scheduled " {
		t.Fatalf("a stale edit moved dates: %s", rows["2026-12-01"])
	}
}

func TestWhoMaySchedule(t *testing.T) {
	env := setup(t)
	addGroup(t, env, 1, policy.GroupActive, policy.Church)
	addGroup(t, env, 2, policy.GroupPending, policy.Public)
	addGroup(t, env, 3, policy.GroupArchived, policy.Public)
	for _, c := range []struct {
		name  string
		by    int
		group string
		vis   string
		want  error
	}{
		{"leader", 2, "g1", policy.Church, nil},
		{"admin", 1, "g1", policy.InviteOnly, nil},
		{"member", 3, "g1", policy.Church, write.ErrNotAllowed},
		{"outsider", 4, "g1", policy.Church, write.ErrNotAllowed},
		{"removed", 5, "g1", policy.Church, write.ErrNotAllowed},
		{"wider than the group", 2, "g1", policy.Public, write.ErrNotAllowed},
		{"a waiting group", 2, "g2", policy.Public, write.ErrNotAllowed},
		{"an archived group", 1, "g3", policy.Public, write.ErrNotAllowed},
		{"no such group", 1, "nope", policy.Public, write.ErrNotAllowed},
	} {
		in := weekly("2026-12-01T19:00")
		in.Visibility = c.vis
		if err := run(t, env, c.by, &events.Create{GroupUID: c.group, Input: in}); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
	far := weekly("2029-12-01T19:00")
	far.Visibility = policy.Church
	if err := run(t, env, 2, &events.Create{GroupUID: "g1", Input: far}); !errors.Is(err, events.ErrStartRange) {
		t.Errorf("a start three years ahead: %v", err)
	}
}

// The nightly job adds the days the horizon reaches, writes no audit row,
// and running it twice adds nothing more.
func TestExtend(t *testing.T) {
	env := setup(t)
	addGroup(t, env, 1, policy.GroupActive, policy.Public)
	c := &events.Create{GroupUID: "g1", Input: weekly("2026-12-01T19:00")}
	run(t, env, 2, c)
	once := &events.Create{GroupUID: "g1", Input: events.Input{Title: "Party", Visibility: policy.Public,
		Pattern: events.Pattern{Start: "2026-12-20T18:00", Minutes: 120, Repeat: events.Once}}}
	run(t, env, 2, once)
	before := len(dateRows(t, env, c.UID))
	audit := env.Count(t, `SELECT count(*) FROM audit_log`)
	env.Clock.Advance(30 * 24 * time.Hour)
	job := events.Extend(env.Exec)
	if err := job(context.Background(), write.Job{}); err != nil {
		t.Fatal(err)
	}
	if after := len(dateRows(t, env, c.UID)); after < before+3 || after > before+5 {
		t.Fatalf("%d dates before, %d after a month", before, after)
	}
	n := env.Count(t, `SELECT count(*) FROM dates`)
	job(context.Background(), write.Job{})
	if env.Count(t, `SELECT count(*) FROM dates`) != n || env.Count(t, `SELECT count(*) FROM audit_log`) != audit {
		t.Fatal("a second run changed something, or the job was audited")
	}
	if len(dateRows(t, env, once.UID)) != 1 {
		t.Fatal("a one-time event grew")
	}
}

// An event that began over a year ago can still change its time; only a new
// first day is held to the bounds.
func TestOldEventChangesItsTime(t *testing.T) {
	env := setup(t)
	addGroup(t, env, 1, policy.GroupActive, policy.Public)
	c := &events.Create{GroupUID: "g1", Input: weekly("2026-12-01T19:00")}
	if err := run(t, env, 2, c); err != nil {
		t.Fatal(err)
	}
	env.Clock.Advance(396 * 24 * time.Hour) // 13 months on
	if err := run(t, env, 2, &events.Edit{UID: c.UID, Input: weekly("2026-12-01T19:30"), Version: 1}); err != nil {
		t.Fatalf("changing only the time: %v", err)
	}
	if rows := dateRows(t, env, c.UID); rows["2026-12-01"] != "19:00 scheduled " || rows["2028-01-04"] != "19:30 scheduled " {
		t.Fatalf("past %s, future %s", rows["2026-12-01"], rows["2028-01-04"])
	}
	if err := run(t, env, 2, &events.Edit{UID: c.UID, Input: weekly("2026-12-08T19:30"), Version: 2}); !errors.Is(err, events.ErrStartRange) {
		t.Fatalf("a new first day over a year back: %v", err)
	}
}
