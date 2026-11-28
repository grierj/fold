package events_test

import (
	"context"
	"errors"
	"strings"
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
		if list := read(t, env, 3); len(list) != 1 || list[0].Text != "“Study” on Tue, Dec 1 is cancelled." {
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

// DEC-91: a series edit that brings back dates it dropped tells the people
// signed up (bell only) and moves the line up where there is room now.
func TestASeriesEditBringsDatesBack(t *testing.T) {
	for _, c := range []struct {
		name  string
		seats int
		want  map[int]string
		told  []int64
	}{
		{"with room", 2, map[int]string{3: "going", 4: "going"}, []int64{4}},
		{"without room", 1, map[int]string{3: "going", 4: "waiting"}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			env := setup(t)
			addGroup(t, env, 1, policy.GroupActive, policy.Public)
			in := weekly("2026-12-01T19:00")
			in.Seats = 1
			ev := &events.Create{GroupUID: "g1", Input: in}
			run(t, env, 2, ev)
			dec8 := dateUID(t, env, ev.UID, "2026-12-08")
			run(t, env, 3, &events.Answer{DateUID: dec8, Status: "going", Scope: "date"})
			run(t, env, 4, &events.Answer{DateUID: dec8, Status: "going", Scope: "date"})
			short := in
			short.Pattern.Until = "2026-12-01"
			if err := run(t, env, 2, &events.Edit{UID: ev.UID, Input: short, Version: 1, Confirmed: true}); err != nil {
				t.Fatal(err)
			}
			exec(t, env, `UPDATE events SET seats = ?`, c.seats) // raised while the date was off
			back := in
			back.Seats = c.seats
			if err := run(t, env, 2, &events.Edit{UID: ev.UID, Input: back, Version: 2}); err != nil {
				t.Fatal(err)
			}
			moved(t, env, dec8, c.want, c.told...)
			if got := noticesOf(t, env, notices.DatesRestored); len(got) != 2 || got[3] != 1 || got[4] != 1 {
				t.Fatalf("back on told %v", got)
			}
			if len(mailJobs(t, env, notices.DatesRestored)) != 0 {
				t.Fatal("dates back on were emailed")
			}
		})
	}
}

func readAs(t *testing.T, env *testenv.Env, person int64) []notices.Notice {
	t.Helper()
	list, err := notices.List(context.Background(), env.Writer, notices.Reader{ID: person, Now: env.Clock.Now()}, 50)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// R44: someone taken off a date, or an event, is told plainly at the bell;
// once they can no longer see it, still told, without naming it.
func TestTakenOffTheListIsTold(t *testing.T) {
	env, event, date := seated(t, 1)
	run(t, env, 2, &events.RemoveAnswer{DateUID: date, PersonUID: "p3"})
	run(t, env, 2, &events.RemoveAnswer{EventUID: event, PersonUID: "p6"})
	if list := readAs(t, env, 3); len(list) != 1 || list[0].Text != "Person 2 took you off the list for “Study” on Tue, Dec 1." || list[0].Href != "/dates/"+date {
		t.Fatalf("taken off a date: %+v", list)
	}
	if list := readAs(t, env, 6); len(list) != 1 || list[0].Text != "Person 2 took you off the list for “Study”." || list[0].Href != "/events/"+event {
		t.Fatalf("taken off the event: %+v", list)
	}
	if len(mailJobs(t, env, notices.AnswerRemoved))+len(mailJobs(t, env, notices.AnswerRemovedAll)) != 0 {
		t.Fatal("R44 is bell only")
	}
	exec(t, env, `UPDATE events SET visibility = 'invite_only'`)
	if list := readAs(t, env, 6); len(list) != 1 || strings.Contains(list[0].Text, "Study") || list[0].Href != "/" {
		t.Fatalf("someone who can't see it now reads %+v", list)
	}
	if err := run(t, env, 6, &events.Answer{DateUID: date, Status: "going", Scope: "date"}); !errors.Is(err, write.ErrNotAllowed) {
		t.Fatalf("answered an invite-only date from outside: %v", err)
	}
	exec(t, env, `UPDATE events SET visibility = 'public'`)
	if err := run(t, env, 6, &events.Answer{DateUID: date, Status: "going", Scope: "date"}); err != nil {
		t.Fatalf("may answer again while they can see it: %v", err)
	}
	run(t, env, 2, &events.RemoveAnswer{DateUID: date, PersonUID: "p2"})
	if len(readAs(t, env, 2)) != 0 {
		t.Fatal("someone taking themselves off was told")
	}
}

// Someone R3 kept on the waitlist who can no longer see the date keeps
// their place, and the freed seat goes to someone who can come.
func TestTheSeatGoesToSomeoneWhoCanSeeIt(t *testing.T) {
	env, event, date := seated(t, 1)
	env.Clock.Advance(time.Minute)
	run(t, env, 2, &events.Answer{DateUID: date, Status: "going", Scope: "date"}) // the leader, third in line
	in := weekly("2026-12-01T19:00")
	in.Pattern.Repeat, in.Seats, in.Visibility = events.Once, 1, policy.InviteOnly
	if err := run(t, env, 2, &events.Edit{UID: event, Input: in, Version: 1, Private: true}); err != nil {
		t.Fatal(err)
	}
	run(t, env, 3, &events.Answer{DateUID: date, Status: "cant", Scope: "date"})
	moved(t, env, date, map[int]string{2: "going", 4: "waiting", 6: "waiting"}, 2)
}

// Each person gets at most UrgentPerDay urgent event emails a day, of every
// kind together; the bell tells them every time.
func TestUrgentEmailIsCappedPerPerson(t *testing.T) {
	env := setup(t)
	addGroup(t, env, 1, policy.GroupActive, policy.Public)
	c := &events.Create{GroupUID: "g1", Input: weekly("2026-12-01T19:00")}
	run(t, env, 2, c)
	run(t, env, 3, &events.Answer{DateUID: dateUID(t, env, c.UID, "2026-12-01"), Status: "going", Scope: "all"})
	for _, day := range []string{"2026-12-01", "2026-12-08", "2026-12-15", "2026-12-22", "2026-12-29", "2027-01-05", "2027-01-12"} {
		if err := run(t, env, 2, events.CancelDate{UID: dateUID(t, env, c.UID, day)}); err != nil {
			t.Fatal(err)
		}
	}
	if bell, mail := noticesOf(t, env, notices.DateCancelled), mailJobs(t, env, notices.DateCancelled); bell[3] != 7 || mail[3] != notices.UrgentPerDay {
		t.Fatalf("told %d times, mailed %d", bell[3], mail[3])
	}
	run(t, env, 2, events.Delete{UID: c.UID})
	if bell, mail := noticesOf(t, env, notices.EventDeleted), mailJobs(t, env, notices.EventDeleted); bell[3] != 1 || mail[3] != 0 {
		t.Fatalf("the cap isn't shared across kinds: told %d, mailed %d", bell[3], mail[3])
	}
	env.Clock.Advance(25 * time.Hour)
	c2 := &events.Create{GroupUID: "g1", Input: weekly("2026-12-03T19:00")}
	run(t, env, 2, c2)
	d := dateUID(t, env, c2.UID, "2026-12-10")
	run(t, env, 3, &events.Answer{DateUID: d, Status: "going", Scope: "date"})
	run(t, env, 2, events.CancelDate{UID: d})
	if mail := mailJobs(t, env, notices.DateCancelled); mail[3] != notices.UrgentPerDay+1 {
		t.Fatalf("the next day's email didn't go: %d", mail[3])
	}
}

// R3's count, and what its Remove takes, are the dates still to come that
// are on; a cancelled one keeps its answer for when it comes back.
func TestRemoveFromAnEventCountsDatesThatAreOn(t *testing.T) {
	env := setup(t)
	addGroup(t, env, 1, policy.GroupActive, policy.InviteOnly)
	c := &events.Create{GroupUID: "g1", Input: weekly("2026-12-01T19:00")}
	run(t, env, 2, c)
	run(t, env, 4, &events.Answer{DateUID: dateUID(t, env, c.UID, "2026-12-01"), Status: "going", Scope: "all"})
	dec8 := dateUID(t, env, c.UID, "2026-12-08")
	run(t, env, 2, events.CancelDate{UID: dec8})
	on := env.Count(t, `SELECT count(*) FROM answers a JOIN dates d ON d.id = a.date_id WHERE a.person_id = 4 AND d.status = 'scheduled'`)
	in := weekly("2026-12-01T19:00")
	in.Visibility = policy.InviteOnly
	edit := &events.Edit{UID: c.UID, Input: in, Version: 1}
	if err := run(t, env, 2, edit); !errors.Is(err, events.ErrMorePrivate) || len(edit.Unseen) != 1 || edit.Unseen[0].Dates != on {
		t.Fatalf("R3 counts %+v, want %d dates that are on", edit.Unseen, on)
	}
	rm := &events.RemoveAnswer{EventUID: c.UID, PersonUID: "p4"}
	if err := run(t, env, 2, rm); err != nil || rm.Removed != on {
		t.Fatalf("removed %d, the confirmation said %d: %v", rm.Removed, on, err)
	}
	if answerOf(t, env, 4, dec8) != "going" {
		t.Fatal("a cancelled date's answer was taken")
	}
}

// DEC-177: deleting a group tells its members, and everyone else signed up
// to its dates still to come, once each, as its confirmation counts.
func TestDeletingAGroupTellsThePeopleSignedUp(t *testing.T) {
	env := setup(t)
	addGroup(t, env, 1, policy.GroupActive, policy.Public)
	a := &events.Create{GroupUID: "g1", Input: weekly("2026-12-01T19:00")}
	b := &events.Create{GroupUID: "g1", Input: weekly("2026-12-03T19:00")}
	run(t, env, 2, a)
	run(t, env, 2, b)
	run(t, env, 3, &events.Answer{DateUID: dateUID(t, env, a.UID, "2026-12-08"), Status: "going", Scope: "date"})
	run(t, env, 4, &events.Answer{DateUID: dateUID(t, env, b.UID, "2026-12-03"), Status: "maybe", Scope: "date"})
	run(t, env, 4, &events.Answer{DateUID: dateUID(t, env, a.UID, "2026-12-08"), Status: "going", Scope: "date"})
	run(t, env, 6, &events.Answer{DateUID: dateUID(t, env, a.UID, "2026-12-15"), Status: "cant", Scope: "date"})
	counted, err := answers.GroupAudience(context.Background(), env.Writer, 1, env.Clock.Now(), 1)
	if err != nil || len(counted) != 1 {
		t.Fatalf("the confirmation counts %d: %v", len(counted), err)
	}
	if err := run(t, env, 1, groups.Delete{UID: "g1"}); err != nil {
		t.Fatal(err)
	}
	if got := noticesOf(t, env, notices.EventDeleted); len(got) != 1 || got[4] != 1 {
		t.Fatalf("signed up told %v; members get their own notice", got)
	}
	if got := mailJobs(t, env, notices.EventDeleted); got[4] != 1 {
		t.Fatal("a Going or Maybe isn't emailed (R36)")
	}
	if list := readAs(t, env, 4); len(list) != 1 || list[0].Text != "“Study” was deleted, so its dates are off, starting Thu, Dec 3." {
		t.Fatalf("reads %+v", list)
	}
}

// A same-day time change tells the people signed up to the dates it moves,
// at the bell; their answers stay.
func TestANewTimeIsTold(t *testing.T) {
	env := setup(t)
	addGroup(t, env, 1, policy.GroupActive, policy.Public)
	c := &events.Create{GroupUID: "g1", Input: weekly("2026-12-01T19:00")}
	run(t, env, 2, c)
	dec8 := dateUID(t, env, c.UID, "2026-12-08")
	run(t, env, 3, &events.Answer{DateUID: dec8, Status: "going", Scope: "following"})
	run(t, env, 4, &events.Answer{DateUID: dec8, Status: "cant", Scope: "date"})
	if err := run(t, env, 2, &events.Edit{UID: c.UID, Input: weekly("2026-12-01T20:00"), Version: 1}); err != nil {
		t.Fatal(err)
	}
	if got := noticesOf(t, env, notices.DatesRetimed); len(got) != 1 || got[3] != 1 {
		t.Fatalf("told %v", got)
	}
	if len(mailJobs(t, env, notices.DatesRetimed)) != 0 || answerOf(t, env, 3, dec8) != "going" {
		t.Fatal("a new time was emailed, or the answer didn't stay")
	}
	if list := readAs(t, env, 3); len(list) != 1 || list[0].Text != "The time of “Study” changed, starting Tue, Dec 8. Your answer stays as it was." {
		t.Fatalf("reads %+v", list)
	}
}
