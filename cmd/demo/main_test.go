package main

import (
	"context"
	"database/sql"
	"io"
	"strings"
	"testing"
	"time"

	"fold/internal/accounts"
	"fold/internal/audit"
	"fold/internal/db"
	"fold/internal/notices"
	"fold/internal/people"
	"fold/internal/server"
)

func TestSeed(t *testing.T) {
	for _, k := range []string{"SITE_URL", "LISTEN", "SECRET_KEY", "SECRET_KEY_PREVIOUS", "TRUSTED_PROXIES"} {
		t.Setenv(k, "") // the developer's shell must not change the result
	}
	ctx := context.Background()
	dir := t.TempDir()
	if err := seed(ctx, dir, io.Discard); err != nil {
		t.Fatal(err)
	}
	r, err := db.OpenReader(server.DBPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	count := func(q *sql.Tx, query string, args ...any) int {
		var n int
		if err := q.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	err = r.Read(ctx, func(q *sql.Tx) error {
		if done, err := accounts.IsSetupDone(ctx, q); err != nil || !done {
			t.Errorf("setup done = %v, %v", done, err)
		}
		rep, err := audit.Verify(ctx, q)
		if err != nil || !rep.Intact() {
			t.Errorf("audit log: %v %s", err, rep.Problem)
		}
		wantPeople := map[string]int{"active": 1 + len(invitedPeople) + len(requestedPeople) - 1, "pending": len(waitingPeople), "disabled": 1}
		for status, want := range wantPeople {
			if n := count(q, `SELECT count(*) FROM people WHERE status = ?`, status); n != want {
				t.Errorf("%d %s people, want %d", n, status, want)
			}
		}
		if n := count(q, `SELECT count(*) FROM people WHERE is_admin = 1 AND status = 'active'`); n != 2 {
			t.Errorf("%d admins, want 2", n)
		}
		wantGroups := map[string]int{}
		for _, g := range groupList {
			wantGroups[g.status]++
		}
		for status, want := range wantGroups {
			if n := count(q, `SELECT count(*) FROM groups WHERE status = ?`, status); n != want {
				t.Errorf("%d %s groups, want %d", n, status, want)
			}
		}
		if n := count(q, `SELECT count(*) FROM groups g WHERE g.status = 'active' AND
			(SELECT count(*) FROM active_memberships am WHERE am.group_id = g.id) NOT BETWEEN 3 AND 15`); n != 0 {
			t.Errorf("%d active groups with fewer than 3 or more than 15 members", n)
		}
		for _, name := range []string{demoLeader, demoMember, demoInvitee} {
			p, err := people.ByEmail(ctx, q, email(name))
			if err != nil {
				t.Fatal(err)
			}
			if n, err := notices.Unread(ctx, q, notices.Reader{ID: p.ID, Now: time.Now()}); err != nil || n == 0 {
				t.Errorf("%s has %d unread notices (%v), want some", name, n, err)
			}
		}
		if n := count(q, `SELECT count(*) FROM events`); n != len(eventList)+1 {
			t.Errorf("%d events, want %d", n, len(eventList)+1)
		}
		if n := count(q, `SELECT count(*) FROM event_hosts h JOIN events e ON e.id = h.event_id WHERE e.group_id IS NULL`); n != len(churchHosts) {
			t.Errorf("%d church-wide hosts", n)
		}
		for _, status := range []string{"going", "maybe", "cant"} {
			if n := count(q, `SELECT count(*) FROM answers WHERE status = ?`, status); n == 0 {
				t.Errorf("no %s answers", status)
			}
		}
		if n := count(q, `SELECT count(*) FROM answers a JOIN dates d ON d.id = a.date_id JOIN events e ON e.id = d.event_id
			WHERE e.seats = 3 AND a.status = 'waiting'`); n != 2 {
			t.Errorf("%d waiting on the full event, want 2", n)
		}
		if n := count(q, `SELECT count(*) FROM answers a JOIN dates d ON d.id = a.date_id JOIN events e ON e.id = d.event_id
			WHERE e.seats = 3 AND a.status = 'going'`); n != 3 {
			t.Errorf("%d going on the full event, want 3", n)
		}
		if n := count(q, `SELECT count(*) FROM events WHERE video_url <> ''`); n != 1 {
			t.Errorf("%d events with a video link", n)
		}
		if n := count(q, `SELECT count(*) FROM dates WHERE cancelled_by = 'person'`); n != 1 {
			t.Errorf("%d dates cancelled by hand", n)
		}
		if n := count(q, `SELECT count(*) FROM dates WHERE visibility = 'church' AND open_reason <> ''`); n != 1 {
			t.Errorf("%d dates opened wider", n)
		}
		if n := count(q, `SELECT count(*) FROM dates d JOIN events e ON e.id = d.event_id WHERE e.repeat = 'monthly' AND d.status = 'scheduled'`); n < 30 {
			t.Errorf("%d monthly dates, want 18 months of two monthly events", n)
		}
		if n := count(q, `SELECT count(*) FROM jobs WHERE state IN ('pending', 'failed')`); n != 1 {
			t.Errorf("%d jobs unfinished, want only the emailed invitation", n)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := seed(ctx, dir, io.Discard); err == nil || !strings.Contains(err.Error(), "delete the folder") {
		t.Fatalf("a second run into the same folder: %v", err)
	}
}
