package write_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"fold/internal/testenv"
	"fold/internal/write"
)

func queue(t *testing.T, env *testenv.Env, j write.NewJob) {
	t.Helper()
	err := env.Exec.Run(context.Background(), write.System(), fn{"test.queue", func(tx *write.Tx) error { return tx.Enqueue(j) }})
	if err != nil {
		t.Fatal(err)
	}
}

func TestJobSecretIsSealedThenErased(t *testing.T) {
	env := testenv.New(t)
	queue(t, env, write.NewJob{Kind: "mail.test", Ref: 7, Secret: []byte("raw-token")})
	var stored []byte
	env.Writer.QueryRow(`SELECT secret FROM jobs`).Scan(&stored)
	if stored == nil || bytes.Contains(stored, []byte("raw-token")) {
		t.Fatalf("secret stored as %q", stored)
	}
	r := write.NewRunner(env.Exec, env.Log)
	var got []byte
	r.Handle("mail.test", func(_ context.Context, j write.Job) error { got = j.Secret; return nil })
	if n, err := r.RunDue(context.Background()); n != 1 || err != nil {
		t.Fatalf("RunDue = %d, %v", n, err)
	}
	if string(got) != "raw-token" {
		t.Fatalf("handler got %q", got)
	}
	if n := env.Count(t, `SELECT count(*) FROM jobs WHERE state = 'done' AND secret IS NULL`); n != 1 {
		t.Fatal("secret not erased when the job ended")
	}
}

func TestJobRetriesThenFails(t *testing.T) {
	env := testenv.New(t)
	queue(t, env, write.NewJob{Kind: "flaky"})
	r := write.NewRunner(env.Exec, env.Log)
	calls := 0
	r.Handle("flaky", func(context.Context, write.Job) error {
		calls++
		return errors.New("timeout to smtp for pat@example.org")
	})
	ctx := context.Background()
	for range 10 {
		r.RunDue(ctx)
		env.Clock.Advance(time.Hour)
	}
	if calls != 6 {
		t.Fatalf("%d attempts, want 6", calls)
	}
	var state, last string
	env.Writer.QueryRow(`SELECT state, last_error FROM jobs`).Scan(&state, &last)
	if state != "failed" || bytes.Contains([]byte(last), []byte("@")) {
		t.Fatalf("state %q, last_error %q", state, last)
	}
}

func TestPermanentErrorStopsAtOnce(t *testing.T) {
	env := testenv.New(t)
	queue(t, env, write.NewJob{Kind: "bad"})
	r := write.NewRunner(env.Exec, env.Log)
	calls := 0
	r.Handle("bad", func(context.Context, write.Job) error { calls++; return write.Permanent(errors.New("no")) })
	r.RunDue(context.Background())
	env.Clock.Advance(time.Hour)
	r.RunDue(context.Background())
	if calls != 1 {
		t.Fatalf("%d calls", calls)
	}
}

func TestRecurringSchedulesItsNextRun(t *testing.T) {
	env := testenv.New(t)
	r := write.NewRunner(env.Exec, env.Log)
	runs := 0
	r.Handle("nightly", func(context.Context, write.Job) error { runs++; return nil })
	r.Every(write.Recurring{Kind: "nightly", Next: func(now time.Time) time.Time {
		return now.Truncate(24 * time.Hour).Add(24*time.Hour + 3*time.Hour)
	}})
	ctx := context.Background()
	r.Schedule(ctx)
	r.Schedule(ctx) // twice is still one job
	if n := env.Count(t, `SELECT count(*) FROM jobs WHERE state = 'pending'`); n != 1 {
		t.Fatalf("%d pending", n)
	}
	for range 3 {
		env.Clock.Advance(24 * time.Hour)
		r.RunDue(ctx)
	}
	if runs != 3 {
		t.Fatalf("%d runs", runs)
	}
	if n := env.Count(t, `SELECT count(*) FROM jobs WHERE state = 'pending'`); n != 1 {
		t.Fatalf("%d pending after runs", n)
	}
}
