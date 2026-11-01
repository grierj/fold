package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"fold/internal/testdb"
	"fold/models"

	"gorm.io/gorm"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

func TestBackoff(t *testing.T) {
	mid := func() float64 { return 0.5 } // no jitter
	for _, tc := range []struct {
		attempt int
		want    time.Duration
	}{{1, 30 * time.Second}, {2, time.Minute}, {3, 2 * time.Minute}, {7, 32 * time.Minute}, {8, time.Hour}, {50, time.Hour}, {0, 30 * time.Second}} {
		if got := Backoff(tc.attempt, mid); got != tc.want {
			t.Errorf("attempt %d: %v, want %v", tc.attempt, got, tc.want)
		}
	}
	if lo, hi := Backoff(3, func() float64 { return 0 }), Backoff(3, func() float64 { return 0.999999 }); lo != 96*time.Second || hi < 143*time.Second || hi > 144*time.Second {
		t.Errorf("jitter is ±20%%: %v .. %v", lo, hi)
	}
}

func TestEnqueueDedupes(t *testing.T) {
	db := testdb.Shared(t)
	now := time.Now()
	if ok, err := Enqueue(db, "x", map[string]int{"n": 1}, now, "x:1", 0); !ok || err != nil {
		t.Fatalf("first: %v %v", ok, err)
	}
	if ok, err := Enqueue(db, "x", nil, now, "x:1", 0); ok || err != nil {
		t.Fatalf("a repeated key should add nothing: %v %v", ok, err)
	}
	for i := 0; i < 2; i++ {
		if ok, err := Enqueue(db, "x", nil, now, "", 0); !ok || err != nil {
			t.Fatalf("no key never conflicts: %v %v", ok, err)
		}
	}
	var jobs []models.Job
	db.Order("id").Find(&jobs)
	if len(jobs) != 3 || jobs[0].Payload != `{"n": 1}` || jobs[0].MaxAttempts != DefaultMaxAttempts || jobs[0].Status != models.JobQueued {
		t.Fatalf("jobs: %+v", jobs)
	}
}

func runner(db *gorm.DB, kinds ...Kind) *Runner {
	r := NewRunner(db, kinds...)
	r.Rnd = func() float64 { return 0.5 }
	return r
}

func TestJobRunsOnceAndIsDone(t *testing.T) {
	db := testdb.Shared(t)
	var got string
	r := runner(db, Kind{Name: "hello", Run: func(_ context.Context, p json.RawMessage) error { got = string(p); return nil }})
	Enqueue(db, "hello", map[string]string{"to": "you"}, time.Now(), "", 0)
	Enqueue(db, "hello", nil, time.Now().Add(time.Hour), "", 0) // not due
	if ran, err := r.RunOnce(context.Background(), "w"); !ran || err != nil {
		t.Fatalf("run: %v %v", ran, err)
	}
	if got != `{"to": "you"}` {
		t.Fatalf("payload %q", got)
	}
	if ran, _ := r.RunOnce(context.Background(), "w"); ran {
		t.Fatal("a job not yet due ran")
	}
	var j models.Job
	db.Order("id").First(&j)
	if j.Status != models.JobDone || j.Attempts != 1 || j.LockedAt != nil {
		t.Fatalf("job: %+v", j)
	}
}

// A handler learns when it is on its last attempt, so it can record the
// failure itself before giving up (M3.5).
func TestLastAttempt(t *testing.T) {
	db := testdb.Shared(t)
	var seen []bool
	r := runner(db, Kind{Name: "count", Run: func(ctx context.Context, _ json.RawMessage) error {
		seen = append(seen, LastAttempt(ctx))
		return errors.New("again")
	}})
	Enqueue(db, "count", nil, time.Now(), "", 3)
	for range 3 {
		db.Model(&models.Job{}).Where("status = ?", models.JobFailed).Update("run_at", time.Now().Add(-time.Second))
		if ran, err := r.RunOnce(context.Background(), "w"); !ran || err != nil {
			t.Fatalf("run: %v %v", ran, err)
		}
	}
	if len(seen) != 3 || seen[0] || seen[1] || !seen[2] {
		t.Fatalf("last attempt seen as %v", seen)
	}
	if LastAttempt(context.Background()) {
		t.Fatal("outside a job it is never the last attempt")
	}
}

// A handler that returns Later waits until then without using an attempt
// or recording an error, even on what would be its last attempt, and
// runs from the top when it is due.
func TestLaterWaitsWithoutUsingAnAttempt(t *testing.T) {
	db := testdb.Shared(t)
	calls := 0
	at := time.Now().Add(9 * time.Hour).Truncate(time.Second)
	r := runner(db, Kind{Name: "wait", Run: func(context.Context, json.RawMessage) error {
		calls++
		if calls <= 3 {
			return fmt.Errorf("quiet hours: %w", Later(at))
		}
		return nil
	}})
	Enqueue(db, "wait", nil, time.Now(), "wait:1", 1)
	for i := 1; i <= 3; i++ {
		if ran, err := r.RunOnce(context.Background(), "w"); !ran || err != nil {
			t.Fatalf("run %d: %v %v", i, ran, err)
		}
		var j models.Job
		db.First(&j)
		if j.Status != models.JobQueued || j.Attempts != 0 || j.LastError != "" || !j.RunAt.Equal(at) || j.LockedAt != nil {
			t.Fatalf("run %d: %+v", i, j)
		}
		if ran, _ := r.RunOnce(context.Background(), "w"); ran {
			t.Fatal("a waiting job ran early")
		}
		db.Model(&j).Update("run_at", time.Now().Add(-time.Second))
	}
	if ran, err := r.RunOnce(context.Background(), "w"); !ran || err != nil {
		t.Fatalf("last run: %v %v", ran, err)
	}
	var j models.Job
	db.First(&j)
	if j.Status != models.JobDone || j.Attempts != 1 || calls != 4 {
		t.Fatalf("after it was due: %+v, %d calls", j, calls)
	}
}

// A failing job retries with backoff and is dead after max_attempts.
func TestFailingJobRetriesThenDies(t *testing.T) {
	db := testdb.Shared(t)
	calls := 0
	r := runner(db, Kind{Name: "flaky", Run: func(context.Context, json.RawMessage) error { calls++; return errors.New("provider down") }})
	Enqueue(db, "flaky", nil, time.Now(), "", 3)
	for attempt := 1; attempt <= 3; attempt++ {
		if ran, err := r.RunOnce(context.Background(), "w"); !ran || err != nil {
			t.Fatalf("attempt %d: %v %v", attempt, ran, err)
		}
		var j models.Job
		db.First(&j)
		if attempt < 3 {
			wait := time.Until(j.RunAt)
			want := Backoff(attempt, r.Rnd)
			if j.Status != models.JobFailed || j.LastError != "provider down" || wait < want-5*time.Second || wait > want {
				t.Fatalf("attempt %d: %+v (waits %v, want %v)", attempt, j, wait, want)
			}
			if ran, _ := r.RunOnce(context.Background(), "w"); ran {
				t.Fatal("a job in backoff ran early")
			}
			db.Model(&j).Update("run_at", time.Now().Add(-time.Second))
		} else if j.Status != models.JobDead || j.Attempts != 3 {
			t.Fatalf("after the last attempt: %+v", j)
		}
	}
	if ran, _ := r.RunOnce(context.Background(), "w"); ran || calls != 3 {
		t.Fatalf("a dead job ran again: %d calls", calls)
	}
}

// A panic is a failure, never a crashed worker. A kind this build doesn't
// know waits (an upgrade rolled back), and is dead only after a week.
func TestPanicsAndUnknownKinds(t *testing.T) {
	db := testdb.Shared(t)
	r := runner(db, Kind{Name: "boom", MaxAttempts: 2, Run: func(context.Context, json.RawMessage) error { panic("oops") }})
	Enqueue(db, "boom", nil, time.Now(), "", 2)
	Enqueue(db, "nobody", nil, time.Now(), "", 5)
	r.RunOnce(context.Background(), "w")
	r.RunOnce(context.Background(), "w")
	var boom, nobody models.Job
	db.Where("kind = ?", "boom").First(&boom)
	db.Where("kind = ?", "nobody").First(&nobody)
	if boom.Status != models.JobFailed || boom.LastError != "panic: oops" {
		t.Fatalf("boom: %+v", boom)
	}
	if nobody.Status != models.JobQueued || nobody.Attempts != 0 || time.Until(nobody.RunAt) < 50*time.Minute {
		t.Fatalf("an unknown kind waits for a build that knows it, without using an attempt: %+v", nobody)
	}
	// After a week it is dead.
	db.Model(&nobody).Updates(map[string]any{"created_at": time.Now().AddDate(0, 0, -8), "run_at": time.Now()})
	r.RunOnce(context.Background(), "w")
	db.First(&nobody, nobody.ID)
	if nobody.Status != models.JobDead {
		t.Fatalf("an unknown kind after a week: %+v", nobody)
	}
}

// A job whose worker stopped mid-run is taken back after its lease; one
// that had used its last attempt is dead instead of running again. A slow
// worker whose lease ran out can't overwrite the new holder's outcome.
func TestExpiredLeases(t *testing.T) {
	db := testdb.Shared(t)
	runs := 0
	r := runner(db, Kind{Name: "work", Run: func(context.Context, json.RawMessage) error { runs++; return nil }})
	r.Lease = time.Minute
	Enqueue(db, "work", nil, time.Now(), "a", 3)
	Enqueue(db, "work", nil, time.Now(), "b", 1)
	stale := time.Now().Add(-2 * time.Minute)
	db.Model(&models.Job{}).Where("unique_key = ?", "a").Updates(map[string]any{"status": models.JobRunning, "attempts": 1, "locked_at": stale, "locked_by": "gone"})
	db.Model(&models.Job{}).Where("unique_key = ?", "b").Updates(map[string]any{"status": models.JobRunning, "attempts": 1, "locked_at": stale, "locked_by": "gone"})
	r.RunOnce(context.Background(), "w")
	r.RunOnce(context.Background(), "w")
	var a, b models.Job
	db.Where("unique_key = ?", "a").First(&a)
	db.Where("unique_key = ?", "b").First(&b)
	if a.Status != models.JobDone || a.Attempts != 2 || runs != 1 {
		t.Fatalf("taken back: %+v (%d runs)", a, runs)
	}
	if b.Status != models.JobDead || b.LastError != "the worker stopped while running it" {
		t.Fatalf("out of attempts: %+v", b)
	}
	// The old holder finishing late changes nothing.
	if err := r.finish(claimed{ID: a.ID, Attempts: 1, MaxAttempts: 3}, "gone", errors.New("late"), false); err != nil {
		t.Fatal(err)
	}
	db.First(&a, a.ID)
	if a.Status != models.JobDone {
		t.Fatalf("a late finish overwrote the outcome: %+v", a)
	}
}

// Six workers (app processes) contend for 200 jobs: every job runs exactly
// once (FOR UPDATE SKIP LOCKED).
func TestTwoWorkersNeverRunAJobTwice(t *testing.T) {
	db := testdb.Shared(t)
	var mu sync.Mutex
	seen := map[int]int{}
	kind := Kind{Name: "count", Run: func(_ context.Context, p json.RawMessage) error {
		time.Sleep(2 * time.Millisecond) // long enough for workers to contend
		var body struct{ N int }
		_ = json.Unmarshal(p, &body)
		mu.Lock()
		seen[body.N]++
		mu.Unlock()
		return nil
	}}
	for i := 0; i < 200; i++ {
		if _, err := Enqueue(db, "count", map[string]int{"n": i}, time.Now(), fmt.Sprintf("count:%d", i), 0); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for _, name := range []string{"process-1", "process-2", "process-3", "process-4", "process-5", "process-6"} {
		r := runner(db, kind)
		r.ID = name
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				ran, err := r.RunOnce(context.Background(), r.ID)
				if err != nil {
					t.Error(err)
					return
				}
				if !ran {
					return
				}
			}
		}()
	}
	wg.Wait()
	if len(seen) != 200 {
		t.Fatalf("ran %d distinct jobs, want 200", len(seen))
	}
	for n, c := range seen {
		if c != 1 {
			t.Fatalf("job %d ran %d times", n, c)
		}
	}
	var by []struct {
		LockedBy string
		N        int
	}
	db.Raw("SELECT locked_by, count(*) AS n FROM jobs WHERE status = 'done' GROUP BY locked_by").Scan(&by)
	if len(by) != 6 {
		t.Fatalf("want every process to have claimed jobs: %+v", by)
	}
}

// A newly queued job starts within 2 seconds of being queued.
func TestNewJobStartsWithinTwoSeconds(t *testing.T) {
	db := testdb.Shared(t)
	started := make(chan time.Time, 1)
	r := runner(db, Kind{Name: "ping", Run: func(context.Context, json.RawMessage) error { started <- time.Now(); return nil }})
	r.Poll = DefaultPoll // the real setting: the bound is what a deployment gets
	ctx, cancel := context.WithCancel(context.Background())
	wait := r.Start(ctx, 1)
	defer func() { cancel(); wait() }()
	time.Sleep(100 * time.Millisecond) // the worker is idle, mid-poll
	queued := time.Now()
	Enqueue(db, "ping", nil, queued, "", 0)
	select {
	case at := <-started:
		if d := at.Sub(queued); d > 2*time.Second {
			t.Fatalf("started after %v", d)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the job never started")
	}
}

func TestPruneDone(t *testing.T) {
	db := testdb.Shared(t)
	for _, s := range []string{models.JobDone, models.JobDismissed, models.JobDead, models.JobQueued} {
		Enqueue(db, s, nil, time.Now(), "", 0)
		db.Model(&models.Job{}).Where("kind = ?", s).Updates(map[string]any{"status": s, "updated_at": time.Now().AddDate(0, 0, -40)})
	}
	n, err := PruneDone(db, time.Now().AddDate(0, 0, -30))
	var left int64
	db.Model(&models.Job{}).Count(&left)
	if err != nil || n != 2 || left != 2 {
		t.Fatalf("pruned %d (%v), %d left", n, err, left)
	}
}

func TestCleanError(t *testing.T) {
	for in, want := range map[string]string{
		"550 <lee@example.org>: rejected\nmore detail": "550 <[email]>: rejected",
		"bad\x00byte":        "badbyte",
		"connection refused": "connection refused",
		// A push endpoint's path is the device's secret (M3.13).
		`Post "https://fcm.googleapis.com/fcm/send/dQw4w9WgXcQ:APA91bH": EOF`:         `Post "https://fcm.googleapis.com/[path]": EOF`,
		"to https://user:pw@push.example.org:443/x?token=abc failed":                  "to https://push.example.org:443/[path] failed",
		"key BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4b":           "key [token]",
		"vapid t=eyJ0eXAiOiJKV1QiLCJhbGciOiJFUzI1NiJ9.eyJhdWQiOiJodHRwcyJ9.c2ln, k=x": "vapid t=[token], k=x",
	} {
		if got := CleanError(errors.New(in)); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestTypedNilPayloadIsAnEmptyObject(t *testing.T) {
	db := testdb.Shared(t)
	var none map[string]string
	Enqueue(db, "x", none, time.Now(), "", 0)
	var j models.Job
	db.First(&j)
	if j.Payload != "{}" {
		t.Fatalf("payload %q", j.Payload)
	}
}
