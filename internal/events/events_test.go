package events_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
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

// The SQL filter on every list of dates is policy.CanSeeDate, for every
// viewer, group status, group, event and date visibility, event status,
// church-wide or not, host or not, and a stray host row on a group's event.
func TestVisibleSQLMatchesPolicy(t *testing.T) {
	env := setup(t)
	ctx := context.Background()
	type made struct {
		date, event string
		eventID     int64
		g           policy.Group
		e           events.Event
		dvis        string
		gid         int64
	}
	var all []made
	dvisAll := []string{"", policy.Public, policy.Church, policy.InviteOnly}
	add := func(gid int64, g policy.Group, ev, es string) {
		uid := fmt.Sprintf("e%d", len(all))
		res, err := env.Writer.Exec(`INSERT INTO events (uid, group_id, title, visibility, zone, starts_local, minutes, repeat, status, created_at, updated_at)
			VALUES (?, ?, 'x', ?, 'America/Boise', '2026-12-01T19:00', 60, 'once', ?, 0, 0)`, uid, sql.NullInt64{Int64: gid, Valid: gid != 0}, ev, es)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		// Person 2 hosts every other event; on a group's event that grants nothing.
		if id%2 == 0 {
			exec(t, env, `INSERT INTO event_hosts (event_id, person_id, created_at) VALUES (?, 2, 0)`, id)
		}
		for i, dv := range dvisAll {
			duid := fmt.Sprintf("%s-%d", uid, i)
			exec(t, env, `INSERT INTO dates (uid, event_id, day, starts_at, ends_at, starts_local, visibility) VALUES (?, ?, ?, ?, ?, '2026-12-01T19:00', ?)`,
				duid, id, fmt.Sprintf("2026-12-0%d", i+1), testenv.Start.Add(time.Hour).Unix(), testenv.Start.Add(2*time.Hour).Unix(), sql.NullString{String: dv, Valid: dv != ""})
			e := events.Event{ID: id, GroupID: gid, Status: es, Visibility: ev}
			all = append(all, made{duid, uid, id, g, e, dv, gid})
		}
	}
	n := 0
	vis := []string{policy.Public, policy.Church, policy.InviteOnly}
	for _, status := range []string{policy.GroupPending, policy.GroupActive, policy.GroupArchived, policy.GroupDeleted} {
		for _, gv := range vis {
			n++
			addGroup(t, env, n, status, gv)
			for _, ev := range vis {
				for _, es := range []string{policy.EventActive, policy.EventDeleted} {
					add(int64(n), policy.Group{Status: status, Visibility: gv}, ev, es)
				}
			}
		}
	}
	for _, ev := range vis {
		for _, es := range []string{policy.EventActive, policy.EventDeleted} {
			for range 2 { // one hosted by person 2, one not
				add(0, policy.Group{}, ev, es)
			}
		}
	}
	exec(t, env, `INSERT INTO people (id, uid, name, email, status, created_at, updated_at) VALUES (7, 'p7', 'Off', 'p7@example.org', 'disabled', 0, 0)`)
	exec(t, env, `INSERT INTO event_hosts (event_id, person_id, created_at) SELECT id, 7, 0 FROM events WHERE group_id IS NULL`)
	for viewer := 0; viewer <= 7; viewer++ {
		v := events.Viewer{ID: int64(viewer), Admin: viewer == 1}
		var p *policy.Person
		if viewer > 0 {
			p = &policy.Person{ID: int64(viewer), Admin: viewer == 1, Inactive: viewer == 7}
		}
		list, err := events.Upcoming(ctx, env.Writer, v, events.Filter{}, testenv.Start, 100000)
		if err != nil {
			t.Fatal(err)
		}
		listed := map[string]bool{}
		for _, l := range list {
			listed[l.UID] = true
		}
		for _, m := range all {
			var r policy.Rel
			if m.gid != 0 {
				r, _ = groups.RelOf(ctx, env.Writer, m.gid, int64(viewer))
			}
			host, _ := events.IsHost(ctx, env.Writer, m.eventID, int64(viewer))
			if want := policy.CanSeeDate(p, m.e.Policy(host), m.dvis, m.g, r); listed[m.date] != want {
				t.Errorf("viewer %d, group %+v, event %+v, date %q, host %v: listed %v, policy %v", viewer, m.g, m.e.Policy(host), m.dvis, host, listed[m.date], want)
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
		{"wider than the group, which is told", 2, "g1", policy.Public, nil},
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

func dateUID(t *testing.T, env *testenv.Env, event, day string) string {
	t.Helper()
	var uid string
	if err := env.Writer.QueryRow(`SELECT d.uid FROM dates d JOIN events e ON e.id = d.event_id WHERE e.uid = ? AND d.day = ?`, event, day).Scan(&uid); err != nil {
		t.Fatalf("no date on %s: %v", day, err)
	}
	return uid
}

// P7 and DEC-91: one date cancelled by hand stays cancelled through a series
// edit, keeps its clock, and comes back only by hand, on the event's
// current times, while the repeat still makes its day.
func TestCancelAndRestoreOneDate(t *testing.T) {
	env := setup(t)
	addGroup(t, env, 1, policy.GroupActive, policy.Public)
	c := &events.Create{GroupUID: "g1", Input: weekly("2026-12-01T19:00")}
	run(t, env, 2, c)
	d := dateUID(t, env, c.UID, "2026-12-08")
	if err := run(t, env, 3, events.CancelDate{UID: d}); !errors.Is(err, write.ErrNotAllowed) {
		t.Fatalf("a member cancelled a date: %v", err)
	}
	if err := run(t, env, 2, events.CancelDate{UID: d}); err != nil {
		t.Fatal(err)
	}
	if err := run(t, env, 2, events.CancelDate{UID: d}); !errors.Is(err, events.ErrDateGone) {
		t.Fatalf("cancelled twice: %v", err)
	}
	if err := run(t, env, 2, &events.Edit{UID: c.UID, Input: weekly("2026-12-01T20:00"), Version: 1}); err != nil {
		t.Fatal(err)
	}
	rows := dateRows(t, env, c.UID)
	if rows["2026-12-08"] != "19:00 cancelled person" || rows["2026-12-15"] != "20:00 scheduled " {
		t.Fatalf("after a series edit: %s / %s", rows["2026-12-08"], rows["2026-12-15"])
	}
	if err := run(t, env, 2, events.RestoreDate{UID: d}); err != nil {
		t.Fatal(err)
	}
	if rows := dateRows(t, env, c.UID); rows["2026-12-08"] != "20:00 scheduled " {
		t.Fatalf("restored on the old clock: %s", rows["2026-12-08"])
	}
	// A series-cancelled date is not restored by hand, and a hand-cancelled
	// day the repeat no longer makes cannot come back.
	run(t, env, 2, events.CancelDate{UID: d})
	if err := run(t, env, 2, &events.Edit{UID: c.UID, Input: weekly("2026-12-02T20:00"), Version: 2, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	if err := run(t, env, 2, events.RestoreDate{UID: d}); !errors.Is(err, events.ErrDateGone) {
		t.Fatalf("restored a day the repeat no longer makes: %v", err)
	}
	if err := run(t, env, 2, events.RestoreDate{UID: dateUID(t, env, c.UID, "2026-12-15")}); !errors.Is(err, events.ErrDateGone) {
		t.Fatalf("restored a series-cancelled date by hand: %v", err)
	}
	// A date that has begun is history.
	env.Clock.Set(time.Date(2026, 12, 10, 4, 0, 0, 0, time.UTC))
	if err := run(t, env, 2, events.CancelDate{UID: dateUID(t, env, c.UID, "2026-12-09")}); !errors.Is(err, events.ErrDateGone) {
		t.Fatalf("cancelled a started date: %v", err)
	}
	if n := env.Count(t, `SELECT count(*) FROM audit_log WHERE action IN ('date.cancel', 'date.restore')`); n != 3 {
		t.Fatalf("%d audit rows", n)
	}
}

func noticesOf(t *testing.T, env *testenv.Env, kind string) map[int64]int {
	t.Helper()
	rows, err := env.Writer.Query(`SELECT person_id, count(*) FROM notices WHERE kind = ? GROUP BY person_id`, kind)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		rows.Scan(&id, &n)
		out[id] = n
	}
	return out
}

// DEC-6, DEC-28: opening an event or one date wider than its group tells
// the group's other leaders and the admins, once each, with the reason kept
// in the audit row; a later edit that opens nothing tells nobody.
func TestOpeningWiderIsTold(t *testing.T) {
	env := setup(t)
	addGroup(t, env, 1, policy.GroupActive, policy.InviteOnly)
	exec(t, env, `UPDATE memberships SET role = 'leader' WHERE person_id = 3`) // a co-leader
	in := weekly("2026-12-01T19:00")
	in.Visibility = policy.InviteOnly
	c := &events.Create{GroupUID: "g1", Input: in}
	run(t, env, 2, c)
	if len(noticesOf(t, env, "event.opened")) != 0 {
		t.Fatal("an event at its group's visibility was told")
	}
	open := weekly("2026-12-01T19:00")
	open.Visibility, open.Reason = policy.Church, "For the whole church this time."
	if err := run(t, env, 2, &events.Edit{UID: c.UID, Input: open, Version: 1}); err != nil {
		t.Fatal(err)
	}
	if got := noticesOf(t, env, "event.opened"); len(got) != 2 || got[1] != 1 || got[3] != 1 {
		t.Fatalf("told: %v; want the admin and the co-leader once each", got)
	}
	var text, subjects string
	env.Writer.QueryRow(`SELECT text, subjects FROM audit_log WHERE action = 'event.edit' ORDER BY seq DESC LIMIT 1`).Scan(&text, &subjects)
	if text != open.Reason || !strings.Contains(subjects, `"p1"`) || !strings.Contains(subjects, `"p3"`) {
		t.Fatalf("audit: %q %s", text, subjects)
	}
	retitled := open
	retitled.Title, retitled.Reason = "Study night", ""
	run(t, env, 2, &events.Edit{UID: c.UID, Input: retitled, Version: 2})
	if got := noticesOf(t, env, "event.opened"); got[1] != 1 {
		t.Fatalf("a title edit told again: %v", got)
	}
	if n := env.Count(t, `SELECT count(*) FROM events WHERE open_reason = ?`, open.Reason); n != 1 {
		t.Fatal("a title edit lost the reason")
	}
	// One date opened to the public: told again, with its own reason; never narrower.
	d := dateUID(t, env, c.UID, "2026-12-08")
	if err := run(t, env, 2, &events.OpenDate{UID: d, Visibility: policy.InviteOnly}); !errors.Is(err, write.ErrNotAllowed) {
		t.Fatalf("a date narrower than its event: %v", err)
	}
	if err := run(t, env, 2, &events.OpenDate{UID: d, Visibility: policy.Public, Reason: "Open house"}); err != nil {
		t.Fatal(err)
	}
	if got := noticesOf(t, env, "event.opened"); got[1] != 2 || got[3] != 2 {
		t.Fatalf("opening a date: %v", got)
	}
	list, _ := events.Upcoming(context.Background(), env.Writer, events.Viewer{}, events.Filter{}, testenv.Start, 100)
	if len(list) != 1 || list[0].UID != d {
		t.Fatalf("a stranger sees %d dates; want only the opened one", len(list))
	}
	// Back to the series': nothing is told, and the reason goes.
	run(t, env, 2, &events.OpenDate{UID: d, Private: true})
	if n := env.Count(t, `SELECT count(*) FROM dates WHERE uid = ? AND visibility IS NULL AND open_reason = ''`, d); n != 1 {
		t.Fatal("not put back to the series'")
	}
	if got := noticesOf(t, env, "event.opened"); got[1] != 2 {
		t.Fatalf("narrowing told: %v", got)
	}
}

// DEC-7: only admins make church-wide events, public or church only; hosts
// run them, a disabled or removed host does not, and the series grows nightly.
func TestChurchWideEventsAndHosts(t *testing.T) {
	env := setup(t)
	in := weekly("2026-12-06T10:00")
	in.Visibility = policy.InviteOnly
	if err := run(t, env, 1, &events.Create{Input: in}); !errors.Is(err, write.ErrNotAllowed) {
		t.Fatalf("an invite-only church-wide event: %v", err)
	}
	in.Visibility = policy.Church
	if err := run(t, env, 2, &events.Create{Input: in}); !errors.Is(err, write.ErrNotAllowed) {
		t.Fatalf("a member made a church-wide event: %v", err)
	}
	c := &events.Create{Input: in}
	if err := run(t, env, 1, c); err != nil {
		t.Fatal(err)
	}
	retimed := weekly("2026-12-06T11:00")
	retimed.Visibility = policy.Church
	if err := run(t, env, 2, &events.Edit{UID: c.UID, Input: retimed, Version: 1}); !errors.Is(err, write.ErrNotAllowed) {
		t.Fatalf("a non-host edited: %v", err)
	}
	if err := run(t, env, 1, events.AddHost{EventUID: c.UID, PersonUID: "p2"}); err != nil {
		t.Fatal(err)
	}
	if got := noticesOf(t, env, "event.host"); got[2] != 1 {
		t.Fatalf("the host was not told: %v", got)
	}
	if err := run(t, env, 2, &events.Edit{UID: c.UID, Input: retimed, Version: 1}); err != nil {
		t.Fatalf("a host could not edit: %v", err)
	}
	if err := run(t, env, 2, events.AddHost{EventUID: c.UID, PersonUID: "p3"}); err != nil {
		t.Fatalf("a host could not name another: %v", err)
	}
	exec(t, env, `UPDATE people SET status = 'disabled' WHERE id = 6`)
	if err := run(t, env, 2, events.AddHost{EventUID: c.UID, PersonUID: "p6"}); !errors.Is(err, write.ErrNotAllowed) {
		t.Fatalf("a disabled person made a host: %v", err)
	}
	// Removing yourself, then the last host, is fine; admins still run it.
	run(t, env, 2, events.RemoveHost{EventUID: c.UID, PersonUID: "p2"})
	if err := run(t, env, 2, events.CancelDate{UID: dateUID(t, env, c.UID, "2026-12-13")}); !errors.Is(err, write.ErrNotAllowed) {
		t.Fatalf("a removed host acted: %v", err)
	}
	exec(t, env, `UPDATE people SET status = 'disabled' WHERE id = 3`)
	if err := run(t, env, 3, events.CancelDate{UID: dateUID(t, env, c.UID, "2026-12-13")}); !errors.Is(err, write.ErrSignedOut) {
		t.Fatalf("a disabled host acted: %v", err)
	}
	if err := run(t, env, 1, events.RemoveHost{EventUID: c.UID, PersonUID: "p3"}); err != nil {
		t.Fatal(err)
	}
	if err := run(t, env, 1, events.CancelDate{UID: dateUID(t, env, c.UID, "2026-12-13")}); err != nil {
		t.Fatalf("an admin with no hosts left: %v", err)
	}
	before := env.Count(t, `SELECT count(*) FROM dates`)
	env.Clock.Advance(31 * 24 * time.Hour)
	events.Extend(env.Exec)(context.Background(), write.Job{})
	if env.Count(t, `SELECT count(*) FROM dates`) <= before {
		t.Fatal("a church-wide series did not grow")
	}
}

func answerRows(t *testing.T, env *testenv.Env, person int) map[string]string {
	t.Helper()
	rows, err := env.Writer.Query(`SELECT d.day, a.status FROM answers a JOIN dates d ON d.id = a.date_id WHERE a.person_id = ?`, person)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var day, s string
		rows.Scan(&day, &s)
		out[day] = s
	}
	return out
}

// DEC-71, DEC-92: one date, this and following, or every date to come, each
// in one write; only dates the person may answer; a series answer is only
// for someone who may see the series.
func TestAnswers(t *testing.T) {
	env := setup(t)
	addGroup(t, env, 1, policy.GroupActive, policy.InviteOnly)
	in := weekly("2026-12-01T19:00")
	in.Visibility = policy.InviteOnly
	c := &events.Create{GroupUID: "g1", Input: in}
	run(t, env, 2, c)
	dec15 := dateUID(t, env, c.UID, "2026-12-15")
	if err := run(t, env, 3, &events.Answer{DateUID: dec15, Status: "going", Scope: "date"}); err != nil {
		t.Fatal(err)
	}
	if got := answerRows(t, env, 3); len(got) != 1 || got["2026-12-15"] != "going" {
		t.Fatalf("one date: %v", got)
	}
	following := &events.Answer{DateUID: dec15, Status: "maybe", Scope: "following"}
	if err := run(t, env, 3, following); err != nil {
		t.Fatal(err)
	}
	got := answerRows(t, env, 3)
	if got["2026-12-08"] != "" || got["2026-12-15"] != "maybe" || got["2026-12-22"] != "maybe" || following.Dates < 70 {
		t.Fatalf("this and following: %d dates, %v", following.Dates, got["2026-12-08"])
	}
	all := &events.Answer{DateUID: dec15, Status: "cant", Scope: "all"}
	if err := run(t, env, 3, all); !errors.Is(err, events.ErrConfirmCantGo) || all.Dates < 70 {
		t.Fatalf("can't go for many dates without asking: %v", err)
	}
	all.Confirmed = true
	if err := run(t, env, 3, all); err != nil {
		t.Fatal(err)
	}
	if got := answerRows(t, env, 3); got["2026-12-01"] != "cant" || got["2026-12-22"] != "cant" {
		t.Fatalf("all: %v", got["2026-12-01"])
	}
	if n := env.Count(t, `SELECT count(*) FROM audit_log WHERE action = 'answer.set'`); n != 3 {
		t.Fatalf("%d audit rows; one per answer", n)
	}
	for _, bad := range []events.Answer{{DateUID: dec15, Status: "cancelled", Scope: "date"}, {DateUID: dec15, Status: "waiting", Scope: "date"},
		{DateUID: dec15, Status: "going", Scope: "forever"}} {
		if err := run(t, env, 3, &bad); !errors.Is(err, write.ErrNotAllowed) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	if err := run(t, env, 4, &events.Answer{DateUID: dec15, Status: "going", Scope: "date"}); !errors.Is(err, write.ErrNotAllowed) {
		t.Fatalf("an outsider answered an invite-only date: %v", err)
	}
	// An outsider on a date opened to the church answers that date, and
	// may not answer the series it would reveal.
	run(t, env, 2, &events.OpenDate{UID: dec15, Visibility: policy.Church})
	if err := run(t, env, 4, &events.Answer{DateUID: dec15, Status: "going", Scope: "all"}); !errors.Is(err, write.ErrNotAllowed) {
		t.Fatalf("an outsider's series answer: %v", err)
	}
	if err := run(t, env, 4, &events.Answer{DateUID: dec15, Status: "going", Scope: "date"}); err != nil {
		t.Fatal(err)
	}
	// A cancelled date takes no answers.
	dec22 := dateUID(t, env, c.UID, "2026-12-22")
	run(t, env, 2, events.CancelDate{UID: dec22})
	if err := run(t, env, 3, &events.Answer{DateUID: dec22, Status: "going", Scope: "date"}); !errors.Is(err, events.ErrDateGone) {
		t.Fatalf("answered a cancelled date: %v", err)
	}
}

// DEC-74, HAZARDS 7: leaving takes back every answer on dates to come,
// Can't go included, never past ones; joining again, the person answers
// again.
func TestLeavingTakesBackAnswers(t *testing.T) {
	env := setup(t)
	addGroup(t, env, 1, policy.GroupActive, policy.Public)
	c := &events.Create{GroupUID: "g1", Input: weekly("2026-11-17T19:00")}
	run(t, env, 2, c)
	exec(t, env, `INSERT INTO answers (date_id, person_id, status, created_at, updated_at)
		SELECT d.id, 3, 'going', 0, 0 FROM dates d WHERE d.day IN ('2026-11-17', '2026-12-01')`)
	run(t, env, 3, &events.Answer{DateUID: dateUID(t, env, c.UID, "2026-12-08"), Status: "cant", Scope: "date"})
	if err := run(t, env, 3, groups.Leave{UID: "g1"}); err != nil {
		t.Fatal(err)
	}
	got := answerRows(t, env, 3)
	if got["2026-11-17"] != "going" || got["2026-12-01"] != "cancelled" || got["2026-12-08"] != "cancelled" {
		t.Fatalf("after leaving: %v", got)
	}
	if err := run(t, env, 3, groups.Join{UID: "g1"}); err != nil {
		t.Fatal(err)
	}
	if err := run(t, env, 3, &events.Answer{DateUID: dateUID(t, env, c.UID, "2026-12-01"), Status: "going", Scope: "date"}); err != nil {
		t.Fatal(err)
	}
	if got := answerRows(t, env, 3); got["2026-12-01"] != "going" {
		t.Fatalf("answering again: %v", got)
	}
	run(t, env, 2, groups.Remove{GroupUID: "g1", PersonUID: "p3"})
	if got := answerRows(t, env, 3); got["2026-12-01"] != "cancelled" {
		t.Fatalf("after removal: %v", got)
	}
}
