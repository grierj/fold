package events_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"fold/internal/accounts"
	"fold/internal/answers"
	"fold/internal/events"
	"fold/internal/groups"
	"fold/internal/notices"
	"fold/internal/policy"
	"fold/internal/testenv"
	"fold/internal/write"
)

// seated is a public group's one-off event on Tuesday, December 1 with a
// seat limit. Person 3 (a member) holds the only seat when seats is 1;
// persons 4 and 6, outsiders, wait in that order.
func seated(t *testing.T, seats int) (*testenv.Env, string, string) {
	t.Helper()
	env := setup(t)
	addGroup(t, env, 1, policy.GroupActive, policy.Public)
	in := weekly("2026-12-01T19:00")
	in.Pattern.Repeat, in.Seats = events.Once, seats
	c := &events.Create{GroupUID: "g1", Input: in}
	if err := run(t, env, 2, c); err != nil {
		t.Fatal(err)
	}
	date := dateUID(t, env, c.UID, "2026-12-01")
	for _, p := range []int{3, 4, 6} {
		env.Clock.Advance(time.Minute) // waiting_since orders the line
		if err := run(t, env, p, &events.Answer{DateUID: date, Status: "going", Scope: "date"}); err != nil {
			t.Fatal(err)
		}
	}
	return env, c.UID, date
}

func answerOf(t *testing.T, env *testenv.Env, person int, date string) string {
	t.Helper()
	var s string
	env.Writer.QueryRow(`SELECT a.status FROM answers a JOIN dates d ON d.id = a.date_id WHERE a.person_id = ? AND d.uid = ?`, person, date).Scan(&s)
	return s
}

func mailJobs(t *testing.T, env *testenv.Env, kind string) map[int64]int {
	t.Helper()
	rows, err := env.Writer.Query(`SELECT n.person_id, count(*) FROM jobs j JOIN notices n ON n.id = j.ref WHERE j.kind = ? AND n.kind = ? GROUP BY n.person_id`,
		notices.JobMail, kind)
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

// moved checks the line after a change: who holds what, and that exactly
// the people moved up were told, by bell and email (R36).
func moved(t *testing.T, env *testenv.Env, date string, want map[int]string, told ...int64) {
	t.Helper()
	for p, s := range want {
		if got := answerOf(t, env, p, date); got != s {
			t.Errorf("person %d is %q, want %q", p, got, s)
		}
	}
	bell, mail := noticesOf(t, env, notices.Promoted), mailJobs(t, env, notices.Promoted)
	if len(bell) != len(told) || len(mail) != len(told) {
		t.Errorf("told %v by bell and %v by mail, want %v", bell, mail, told)
	}
	for _, p := range told {
		if bell[p] != 1 || mail[p] != 1 {
			t.Errorf("person %d told %d times, mailed %d", p, bell[p], mail[p])
		}
	}
}

func TestGoingOnAFullDateWaits(t *testing.T) {
	env, _, date := seated(t, 1)
	moved(t, env, date, map[int]string{3: "going", 4: "waiting", 6: "waiting"})
	for p, want := range map[int64]int{3: 0, 4: 1, 6: 2} {
		if n, _ := placeOf(t, env, p, date); n != want {
			t.Errorf("person %d is number %d, want %d", p, n, want)
		}
	}
	// Going again keeps a place in line; Can't go leaves the line.
	run(t, env, 4, &events.Answer{DateUID: date, Status: "going", Scope: "date"})
	if n, _ := placeOf(t, env, 4, date); n != 1 {
		t.Fatalf("answering Going again lost the place: %d", n)
	}
	run(t, env, 4, &events.Answer{DateUID: date, Status: "cant", Scope: "date"})
	if n, _ := placeOf(t, env, 6, date); n != 1 {
		t.Fatalf("leaving the waitlist didn't move the line: %d", n)
	}
	moved(t, env, date, map[int]string{3: "going", 4: "cant", 6: "waiting"})
}

func placeOf(t *testing.T, env *testenv.Env, person int64, date string) (int, error) {
	t.Helper()
	var id int64
	env.Writer.QueryRow(`SELECT id FROM dates WHERE uid = ?`, date).Scan(&id)
	return answers.Place(context.Background(), env.Writer, id, person)
}

// Each change that frees a seat moves the earliest waiting person up and
// tells them, in the same write; each change that frees none moves nobody
// (docs/HAZARDS.md section 7, DEC-84).
func TestEverySeatFreedMovesTheLineUp(t *testing.T) {
	frees := []struct {
		name   string
		change func(t *testing.T, env *testenv.Env, event, date string) error
	}{
		{"Going to Maybe", func(t *testing.T, env *testenv.Env, _, date string) error {
			return run(t, env, 3, &events.Answer{DateUID: date, Status: "maybe", Scope: "date"})
		}},
		{"Going to Can't go", func(t *testing.T, env *testenv.Env, _, date string) error {
			return run(t, env, 3, &events.Answer{DateUID: date, Status: "cant", Scope: "date"})
		}},
		{"leaving the group", func(t *testing.T, env *testenv.Env, _, _ string) error {
			return run(t, env, 3, groups.Leave{UID: "g1"})
		}},
		{"removed from the group", func(t *testing.T, env *testenv.Env, _, _ string) error {
			return run(t, env, 2, groups.Remove{GroupUID: "g1", PersonUID: "p3"})
		}},
		{"removed from the date", func(t *testing.T, env *testenv.Env, _, date string) error {
			return run(t, env, 2, &events.RemoveAnswer{DateUID: date, PersonUID: "p3"})
		}},
		{"removed from the event", func(t *testing.T, env *testenv.Env, event, _ string) error {
			return run(t, env, 2, &events.RemoveAnswer{EventUID: event, PersonUID: "p3"})
		}},
		{"an account disabled", func(t *testing.T, env *testenv.Env, _, _ string) error {
			return run(t, env, 1, accounts.Disable{PersonUID: "p3"})
		}},
	}
	for _, c := range frees {
		t.Run(c.name, func(t *testing.T) {
			env, event, date := seated(t, 1)
			if err := c.change(t, env, event, date); err != nil {
				t.Fatal(err)
			}
			moved(t, env, date, map[int]string{4: "going", 6: "waiting"}, 4)
			if n := env.Count(t, `SELECT count(*) FROM audit_log WHERE details LIKE '%"promoted":"1"%' AND subjects LIKE '%p4%'`); n != 1 {
				t.Errorf("%d audit rows record the move up; it belongs to the change's own row", n)
			}
		})
	}
	room := []struct {
		name string
		to   int
		want map[int]string
		told []int64
	}{
		{"a raised limit", 2, map[int]string{3: "going", 4: "going", 6: "waiting"}, []int64{4}},
		{"a cleared limit", 0, map[int]string{3: "going", 4: "going", 6: "going"}, []int64{4, 6}},
		{"a lowered limit bumps nobody", 1, map[int]string{3: "going", 4: "waiting", 6: "waiting"}, nil},
	}
	for _, c := range room {
		t.Run(c.name, func(t *testing.T) {
			env, event, date := seated(t, 1)
			if c.name == "a lowered limit bumps nobody" {
				exec(t, env, `UPDATE events SET seats = 3`)
				exec(t, env, `UPDATE answers SET status = 'going' WHERE person_id = 4`)
			}
			in := weekly("2026-12-01T19:00")
			in.Pattern.Repeat, in.Seats = events.Once, c.to
			if err := run(t, env, 2, &events.Edit{UID: event, Input: in, Version: 1}); err != nil {
				t.Fatal(err)
			}
			if c.name == "a lowered limit bumps nobody" {
				c.want[4] = "going"
			}
			moved(t, env, date, c.want, c.told...)
		})
	}
	stays := []struct {
		name   string
		change func(t *testing.T, env *testenv.Env, event, date string) error
	}{
		{"Going again", func(t *testing.T, env *testenv.Env, _, date string) error {
			return run(t, env, 3, &events.Answer{DateUID: date, Status: "going", Scope: "date"})
		}},
		{"a waiting person leaves the line", func(t *testing.T, env *testenv.Env, _, date string) error {
			return run(t, env, 4, &events.Answer{DateUID: date, Status: "maybe", Scope: "date"})
		}},
		{"a waiting person removed", func(t *testing.T, env *testenv.Env, _, date string) error {
			return run(t, env, 2, &events.RemoveAnswer{DateUID: date, PersonUID: "p4"})
		}},
		{"a waiting account disabled", func(t *testing.T, env *testenv.Env, _, _ string) error {
			return run(t, env, 1, accounts.Disable{PersonUID: "p4"})
		}},
		{"a title edit", func(t *testing.T, env *testenv.Env, event, _ string) error {
			in := weekly("2026-12-01T19:00")
			in.Pattern.Repeat, in.Seats, in.Title = events.Once, 1, "Supper"
			return run(t, env, 2, &events.Edit{UID: event, Input: in, Version: 1})
		}},
	}
	for _, c := range stays {
		t.Run(c.name, func(t *testing.T) {
			env, event, date := seated(t, 1)
			if err := c.change(t, env, event, date); err != nil {
				t.Fatal(err)
			}
			if got := answerOf(t, env, 3, date); got != "going" {
				t.Errorf("the seat holder is %q", got)
			}
			moved(t, env, date, map[int]string{6: "waiting"})
		})
	}
}

// Fold's 5.4: within two hours of the start, a freed seat moves nobody up;
// it goes to whoever says Going next.
func TestNoMovingUpInsideTheCutoff(t *testing.T) {
	env, _, date := seated(t, 1)
	env.Clock.Set(time.Date(2026, 12, 2, 1, 0, 0, 0, time.UTC)) // 6 pm in Boise, an hour before
	run(t, env, 3, &events.Answer{DateUID: date, Status: "cant", Scope: "date"})
	moved(t, env, date, map[int]string{4: "waiting", 6: "waiting"})
	run(t, env, 6, &events.Answer{DateUID: date, Status: "going", Scope: "date"})
	moved(t, env, date, map[int]string{4: "waiting", 6: "going"})
}

// DEC-91: a date brought back moves the line up if it has room now, and
// tells the people signed up that it is back on (bell only, R36).
func TestRestoringADateMovesTheLineUp(t *testing.T) {
	env, event, date := seated(t, 1)
	run(t, env, 2, events.CancelDate{UID: date})
	exec(t, env, `UPDATE events SET seats = 2`) // raised while it was off: no date to settle
	moved(t, env, date, map[int]string{4: "waiting"})
	if err := run(t, env, 2, events.RestoreDate{UID: date}); err != nil {
		t.Fatal(err)
	}
	moved(t, env, date, map[int]string{3: "going", 4: "going", 6: "waiting"}, 4)
	if got := noticesOf(t, env, notices.DatesRestored); len(got) != 3 || got[3] != 1 || got[4] != 1 || got[6] != 1 {
		t.Fatalf("restored told %v", got)
	}
	if len(mailJobs(t, env, notices.DatesRestored)) != 0 {
		t.Fatal("a date back on was emailed")
	}
	_ = event
}

// DEC-47: an account turned back on gets its answers back where there is
// room; a Going on a date now full is dropped, and they're told once.
func TestTurningAnAccountBackOn(t *testing.T) {
	t.Run("no room", func(t *testing.T) {
		env, _, date := seated(t, 1)
		run(t, env, 1, accounts.Disable{PersonUID: "p3"})
		en := &accounts.Enable{PersonUID: "p3"}
		if err := run(t, env, 1, en); err != nil || en.Dropped != 1 {
			t.Fatalf("enable: %v, dropped %d", err, en.Dropped)
		}
		moved(t, env, date, map[int]string{3: "cancelled", 4: "going", 6: "waiting"}, 4)
		if got := noticesOf(t, env, notices.NoRoom); len(got) != 1 || got[3] != 1 {
			t.Fatalf("told %v to ask a leader", got)
		}
	})
	t.Run("room", func(t *testing.T) {
		env, _, date := seated(t, 3)
		run(t, env, 1, accounts.Disable{PersonUID: "p3"})
		run(t, env, 1, accounts.Disable{PersonUID: "p4"})
		run(t, env, 1, &accounts.Enable{PersonUID: "p3"})
		run(t, env, 1, &accounts.Enable{PersonUID: "p4"})
		moved(t, env, date, map[int]string{3: "going", 4: "going", 6: "going"})
		if len(noticesOf(t, env, notices.NoRoom)) != 0 {
			t.Fatal("told to ask a leader with room to spare")
		}
	})
	t.Run("a place in line comes back", func(t *testing.T) {
		env, _, date := seated(t, 1)
		run(t, env, 1, accounts.Disable{PersonUID: "p4"})
		if n, _ := placeOf(t, env, 6, date); n != 1 {
			t.Fatalf("a disabled account kept a place: %d", n)
		}
		run(t, env, 1, &accounts.Enable{PersonUID: "p4"})
		if n, _ := placeOf(t, env, 4, date); n != 1 {
			t.Fatalf("the place didn't come back: %d", n)
		}
	})
}

// A Going for many dates of a full series waits on each full one; a seat
// freed on many dates tells each person moved up once.
func TestASeriesOnTheWaitlist(t *testing.T) {
	env := setup(t)
	addGroup(t, env, 1, policy.GroupActive, policy.Public)
	in := weekly("2026-12-01T19:00")
	in.Seats = 1
	c := &events.Create{GroupUID: "g1", Input: in}
	run(t, env, 2, c)
	first := dateUID(t, env, c.UID, "2026-12-01")
	run(t, env, 3, &events.Answer{DateUID: first, Status: "going", Scope: "all"})
	a := &events.Answer{DateUID: first, Status: "going", Scope: "all"}
	run(t, env, 4, a)
	if a.Waiting != a.Dates || a.Dates < 70 {
		t.Fatalf("%d of %d dates waiting", a.Waiting, a.Dates)
	}
	run(t, env, 3, &events.Answer{DateUID: first, Status: "cant", Scope: "all", Confirmed: true})
	if got := noticesOf(t, env, notices.Promoted); len(got) != 1 || got[4] != 1 {
		t.Fatalf("moved up on every date, told %v", got)
	}
	if n := env.Count(t, `SELECT count(*) FROM answers WHERE person_id = 4 AND status = 'going'`); n != a.Dates {
		t.Fatalf("moved up on %d of %d dates", n, a.Dates)
	}
}

// DEC-175, R36, HAZARDS 7: deleting an event, cancelling a date, and a
// series edit that drops or moves dates each tell exactly the people their
// confirmation counted, once each, by bell, and by email those who said
// Going or Maybe. The person making the change is not told.
func TestTellingThePeopleSignedUp(t *testing.T) {
	setupTold := func(t *testing.T) (*testenv.Env, string, string) {
		env, event, date := seated(t, 1)
		run(t, env, 2, &events.Answer{DateUID: date, Status: "maybe", Scope: "date"}) // the leader
		exec(t, env, `INSERT INTO people (id, uid, name, email, status, created_at, updated_at) VALUES (7, 'p7', 'Pat', 'p7@example.org', 'active', 0, 0),
			(8, 'p8', 'Cal', 'p8@example.org', 'active', 0, 0)`)
		exec(t, env, `INSERT INTO sessions (token_hash, person_id, created_at, renewed_at, expires_at) VALUES (X'07', 7, 0, 0, ?), (X'08', 8, 0, 0, ?)`,
			testenv.Start.Add(1000*24*time.Hour).Unix(), testenv.Start.Add(1000*24*time.Hour).Unix())
		run(t, env, 7, &events.Answer{DateUID: date, Status: "maybe", Scope: "date"})
		run(t, env, 8, &events.Answer{DateUID: date, Status: "cant", Scope: "date"}) // can't go has nothing to lose
		return env, event, date
	}
	check := func(t *testing.T, env *testenv.Env, kind string, counted int) {
		t.Helper()
		bell, mail := noticesOf(t, env, kind), mailJobs(t, env, kind)
		if len(bell) != counted {
			t.Errorf("told %d people, the confirmation counted %d", len(bell), counted)
		}
		for p, want := range map[int64]int{3: 1, 4: 1, 6: 1, 7: 1} {
			if bell[p] != want {
				t.Errorf("person %d told %d times", p, bell[p])
			}
		}
		if bell[2] != 0 || bell[8] != 0 {
			t.Error("the person making the change, or someone who can't go, was told")
		}
		if len(mail) != 2 || mail[3] != 1 || mail[7] != 1 {
			t.Errorf("mailed %v; only Going and Maybe are (R36)", mail)
		}
	}
	t.Run("delete", func(t *testing.T) {
		env, event, _ := setupTold(t)
		e, _ := events.ByUID(context.Background(), env.Writer, event)
		g, _ := groups.ByID(context.Background(), env.Writer, e.GroupID)
		n, err := events.Telling(context.Background(), env.Writer, e, g, 0, env.Clock.Now(), 2)
		if err != nil || n != 4 {
			t.Fatalf("the delete counts %d: %v", n, err)
		}
		run(t, env, 2, events.Delete{UID: event})
		check(t, env, notices.EventDeleted, n)
	})
	t.Run("cancel a date", func(t *testing.T) {
		env, event, date := setupTold(t)
		e, _ := events.ByUID(context.Background(), env.Writer, event)
		g, _ := groups.ByID(context.Background(), env.Writer, e.GroupID)
		d, _, _ := events.DateByUID(context.Background(), env.Writer, date)
		n, _ := events.Telling(context.Background(), env.Writer, e, g, d.ID, env.Clock.Now(), 2)
		run(t, env, 2, events.CancelDate{UID: date})
		check(t, env, notices.DateCancelled, n)
	})
	t.Run("a series edit moves it to another day (R40)", func(t *testing.T) {
		env, event, date := setupTold(t)
		in := weekly("2026-12-02T19:00")
		in.Pattern.Repeat, in.Seats = events.Once, 1
		edit := &events.Edit{UID: event, Input: in, Version: 1}
		if err := run(t, env, 2, edit); !errors.Is(err, events.ErrDropsDates) || edit.Drops != 1 {
			t.Fatalf("dropping a date didn't ask: %v", err)
		}
		edit.Confirmed = true
		if err := run(t, env, 2, edit); err != nil {
			t.Fatal(err)
		}
		check(t, env, notices.DatesMoved, edit.Told)
		if answerOf(t, env, 3, date) != "going" || env.Count(t, `SELECT count(*) FROM answers a JOIN dates d ON d.id = a.date_id WHERE d.day = '2026-12-02'`) != 0 {
			t.Fatal("answers followed the date to another day")
		}
	})
	t.Run("a series edit drops dates", func(t *testing.T) {
		env := setup(t)
		addGroup(t, env, 1, policy.GroupActive, policy.Public)
		c := &events.Create{GroupUID: "g1", Input: weekly("2026-12-01T19:00")}
		run(t, env, 2, c)
		run(t, env, 3, &events.Answer{DateUID: dateUID(t, env, c.UID, "2026-12-15"), Status: "going", Scope: "following"})
		in := weekly("2026-12-01T19:00")
		in.Pattern.Until = "2026-12-08"
		edit := &events.Edit{UID: c.UID, Input: in, Version: 1, Confirmed: true}
		run(t, env, 2, edit)
		if got := noticesOf(t, env, notices.DatesDropped); len(got) != 1 || got[3] != 1 {
			t.Fatalf("dropping dates told %v", got)
		}
	})
}

// A notice about a date shows only while its reader may see the date: not
// to someone removed from the group, an outsider once an opened date is
// closed, or a host no longer hosting a church-wide event.
func TestAHiddenDateTellsNothing(t *testing.T) {
	read := func(t *testing.T, env *testenv.Env, person int64) []notices.Notice {
		t.Helper()
		list, err := notices.List(context.Background(), env.Writer, notices.Reader{ID: person, Now: env.Clock.Now()}, 50)
		if err != nil {
			t.Fatal(err)
		}
		return list
	}
	t.Run("removed from the group", func(t *testing.T) {
		env, _, date := seated(t, 1)
		run(t, env, 2, events.CancelDate{UID: date})
		if list := read(t, env, 3); len(list) != 1 || list[0].Text != "“Study” on Tuesday, December 1 is cancelled." {
			t.Fatalf("the member reads %+v", list)
		}
		exec(t, env, `UPDATE events SET visibility = 'invite_only'`)
		exec(t, env, `UPDATE memberships SET state = 'removed' WHERE person_id = 3`)
		if list := read(t, env, 3); len(list) != 0 {
			t.Fatalf("someone removed reads %+v", list)
		}
	})
	t.Run("an opened date closed again", func(t *testing.T) {
		env := setup(t)
		addGroup(t, env, 1, policy.GroupActive, policy.InviteOnly)
		in := weekly("2026-12-01T19:00")
		in.Visibility = policy.InviteOnly
		c := &events.Create{GroupUID: "g1", Input: in}
		run(t, env, 2, c)
		d := dateUID(t, env, c.UID, "2026-12-08")
		run(t, env, 2, &events.OpenDate{UID: d, Visibility: policy.Church})
		run(t, env, 4, &events.Answer{DateUID: d, Status: "going", Scope: "date"})
		run(t, env, 2, events.CancelDate{UID: d})
		if list := read(t, env, 4); len(list) != 1 {
			t.Fatalf("the outsider reads %d notices", len(list))
		}
		exec(t, env, `UPDATE dates SET visibility = NULL WHERE uid = ?`, d)
		if list := read(t, env, 4); len(list) != 0 {
			t.Fatalf("an outsider reads a closed date's notice: %+v", list)
		}
	})
	t.Run("an outsider once the event is invite-only", func(t *testing.T) {
		env, _, date := seated(t, 1)
		run(t, env, 3, &events.Answer{DateUID: date, Status: "cant", Scope: "date"})
		if list := read(t, env, 4); len(list) != 1 || list[0].Kind != notices.Promoted || list[0].Href != "/dates/"+date {
			t.Fatalf("the person moved up reads %+v", list)
		}
		exec(t, env, `UPDATE events SET visibility = 'invite_only'`)
		if list := read(t, env, 4); len(list) != 0 {
			t.Fatalf("an outsider reads an invite-only event's notice: %+v", list)
		}
		if list := read(t, env, 3); len(list) != 0 {
			t.Fatal("a notice reached the person who freed the seat")
		}
	})
}

// R3: making an event, or one date, more private removes nobody; it asks
// first, listing who signed up would no longer see it.
func TestMorePrivateListsWhoLosesSight(t *testing.T) {
	env, event, date := seated(t, 0)
	in := weekly("2026-12-01T19:00")
	in.Pattern.Repeat, in.Visibility = events.Once, policy.InviteOnly
	edit := &events.Edit{UID: event, Input: in, Version: 1}
	if err := run(t, env, 2, edit); !errors.Is(err, events.ErrMorePrivate) {
		t.Fatalf("narrowing didn't ask: %v", err)
	}
	if len(edit.Unseen) != 2 || edit.Unseen[0].UID != "p4" || edit.Unseen[1].UID != "p6" || edit.Unseen[0].Dates != 1 {
		t.Fatalf("unseen: %+v", edit.Unseen)
	}
	edit.Private = true
	if err := run(t, env, 2, edit); err != nil {
		t.Fatal(err)
	}
	if answerOf(t, env, 4, date) != "going" {
		t.Fatal("making it more private removed someone")
	}
	if err := run(t, env, 2, &events.RemoveAnswer{EventUID: event, PersonUID: "p4"}); err != nil || answerOf(t, env, 4, date) != "cancelled" {
		t.Fatalf("remove from R3's list: %v", err)
	}
	if err := run(t, env, 3, &events.RemoveAnswer{DateUID: date, PersonUID: "p6"}); !errors.Is(err, write.ErrNotAllowed) {
		t.Fatalf("a member removed someone: %v", err)
	}
	// Closing an opened date again asks the same way.
	run(t, env, 2, &events.OpenDate{UID: date, Visibility: policy.Public})
	run(t, env, 4, &events.Answer{DateUID: date, Status: "maybe", Scope: "date"})
	closeIt := &events.OpenDate{UID: date}
	if err := run(t, env, 2, closeIt); !errors.Is(err, events.ErrMorePrivate) || len(closeIt.Unseen) != 2 {
		t.Fatalf("closing a date: %v %+v", err, closeIt.Unseen)
	}
}

