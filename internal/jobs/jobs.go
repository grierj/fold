// Package jobs runs background work from the jobs table (M3.1, PLAN 5.5).
// The worker lives in the same binary as the server. Workers claim due jobs
// with FOR UPDATE SKIP LOCKED, so any number of app processes can share the
// table and never run one job twice. A failed attempt retries with
// exponential backoff and jitter; after its kind's MaxAttempts the job is
// dead and waits for an admin on the System page.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"math/rand/v2"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"fold/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Kind is one sort of job: its handler and how many attempts it gets.
// A handler does its own transactions; returning an error retries it. It must
// be safe to run twice (a retry, or a lease taken back from a worker that
// was slow rather than gone) and should finish well within the lease.
type Kind struct {
	Name        string
	MaxAttempts int
	// Label says what the job does, in plain words, on the System page.
	Label string
	// Daily: the job is queued again every church day, so a dead one is
	// retried by tomorrow's run anyway.
	Daily bool
	Run   func(ctx context.Context, payload json.RawMessage) error
}

// Defaults (PLAN 5.5: FOLD_JOB_POLL=1s).
const (
	DefaultPoll        = time.Second
	DefaultLease       = 15 * time.Minute
	DefaultMaxAttempts = 5
	// maxErrorLen keeps a runaway error message out of the table.
	maxErrorLen = 2000
)

// Enqueue adds a job to run at runAt. A uniqueKey that some job already
// has (in any status) adds nothing and reports false, so scheduling the
// same work twice is harmless; "" means no key.
func Enqueue(db *gorm.DB, kind string, payload any, runAt time.Time, uniqueKey string, maxAttempts int) (bool, error) {
	body := []byte("{}")
	if payload != nil {
		var err error
		if body, err = json.Marshal(payload); err != nil {
			return false, err
		}
		if string(body) == "null" { // a typed nil
			body = []byte("{}")
		}
	}
	if maxAttempts <= 0 {
		maxAttempts = DefaultMaxAttempts
	}
	job := models.Job{Kind: kind, Payload: string(body), RunAt: runAt.UTC(), Status: models.JobQueued, MaxAttempts: maxAttempts}
	if uniqueKey != "" {
		job.UniqueKey = &uniqueKey
	}
	res := db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "unique_key"}}, DoNothing: true}).Create(&job)
	return res.RowsAffected == 1, res.Error
}

// Backoff is how long a job waits after its attempt-th failure (1-based):
// 30 seconds doubling each time up to an hour, then up to 20% jitter either
// way so retries from one outage spread out (so at most 72 minutes). rnd
// returns [0, 1).
func Backoff(attempt int, rnd func() float64) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	base := 30 * time.Second * time.Duration(math.Pow(2, float64(min(attempt-1, 20))))
	if base > time.Hour || base <= 0 {
		base = time.Hour
	}
	jitter := 1 + (rnd()*2-1)*0.2
	return time.Duration(float64(base) * jitter)
}

// Runner claims and runs jobs.
type Runner struct {
	DB    *gorm.DB
	Kinds map[string]Kind
	// Poll is how often an idle worker looks for due work.
	Poll time.Duration
	// Lease is how long a claimed job may run before another worker may
	// take it back (its process stopped mid-job).
	Lease time.Duration
	// ID names this process in locked_by.
	ID  string
	Rnd func() float64
}

// NewRunner registers kinds and fills the defaults.
func NewRunner(db *gorm.DB, kinds ...Kind) *Runner {
	host, _ := os.Hostname()
	r := &Runner{DB: db, Kinds: map[string]Kind{}, Poll: DefaultPoll, Lease: DefaultLease,
		ID: fmt.Sprintf("%s:%d", host, os.Getpid()), Rnd: rand.Float64}
	for _, k := range kinds {
		r.Kinds[k.Name] = k
	}
	return r
}

// Start runs n workers until ctx ends; n <= 0 starts none (FOLD_WORKERS=0,
// for debugging). It returns a wait function for a clean shutdown.
func (r *Runner) Start(ctx context.Context, n int) func() {
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			id := fmt.Sprintf("%s/%d", r.ID, worker)
			for {
				ran, err := r.RunOnce(ctx, id)
				if err != nil && ctx.Err() == nil {
					log.Printf("jobs: %v", err)
				}
				if ran {
					continue // more may be due right away
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(r.Poll):
				}
			}
		}(i)
	}
	return wg.Wait
}

type claimed struct {
	ID          uint
	Kind        string
	Payload     string
	Attempts    int
	MaxAttempts int
	CreatedAt   time.Time
}

// unknownKindWait: a job of a kind this build doesn't know (an older
// build running after an upgrade was rolled back) waits for a build that
// knows it, without using an attempt, and is dead only after a week.
const (
	unknownKindWait  = time.Hour
	unknownKindGrace = 7 * 24 * time.Hour
)

// RunOnce claims the next due job, if any, and runs it as worker. It
// reports whether it found one. An error is about the table, not the job:
// a job's own failure is recorded on its row.
func (r *Runner) RunOnce(ctx context.Context, worker string) (bool, error) {
	var job claimed
	// One statement: pick the earliest due job nobody holds, or one whose
	// lease ran out, and take it. SKIP LOCKED lets other workers move on.
	err := r.DB.WithContext(ctx).Raw(`UPDATE jobs SET status = ?, locked_at = now(), locked_by = ?, attempts = attempts + 1, updated_at = now()
		WHERE id = (SELECT id FROM jobs
			WHERE (status IN (?, ?) AND run_at <= now()) OR (status = ? AND locked_at < now() - make_interval(secs => ?))
			ORDER BY run_at, id FOR UPDATE SKIP LOCKED LIMIT 1)
		RETURNING id, kind, payload, attempts, max_attempts, created_at`,
		models.JobRunning, worker, models.JobQueued, models.JobFailed, models.JobRunning, r.Lease.Seconds()).Scan(&job).Error
	if err != nil || job.ID == 0 {
		return false, err
	}
	kind, known := r.Kinds[job.Kind]
	switch {
	case job.Attempts > job.MaxAttempts:
		// Its last attempt never finished: the process stopped mid-job.
		return true, r.finish(job, worker, errors.New("the worker stopped while running it"), true)
	case !known && time.Since(job.CreatedAt) > unknownKindGrace:
		return true, r.finish(job, worker, fmt.Errorf("no handler for job kind %q", job.Kind), true)
	case !known:
		return true, r.DB.Model(&models.Job{}).Where("id = ? AND status = ? AND locked_by = ?", job.ID, models.JobRunning, worker).
			Updates(map[string]any{"status": models.JobQueued, "attempts": gorm.Expr("attempts - 1"), "locked_at": nil,
				"last_error": "this version of Fold doesn't run this kind of task", "run_at": time.Now().Add(unknownKindWait), "updated_at": time.Now()}).Error
	}
	return true, r.finish(job, worker, safeRun(context.WithValue(ctx, attemptKey{}, job.Attempts >= job.MaxAttempts), kind, json.RawMessage(job.Payload)), false)
}

// attemptKey carries whether a run is its job's last attempt.
type attemptKey struct{}

// LastAttempt reports whether the running job will not be retried if it
// fails: a handler can record the failure itself (a message's failed
// state) before returning the error.
func LastAttempt(ctx context.Context) bool {
	last, _ := ctx.Value(attemptKey{}).(bool)
	return last
}

// safeRun turns a handler's panic into an error, so one bad job never
// takes the worker down.
func safeRun(ctx context.Context, k Kind, payload json.RawMessage) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v", p)
		}
	}()
	return k.Run(ctx, payload)
}

// finish records the outcome, but only while this worker still holds the
// job: if its lease ran out and another worker took it, that one decides.
func (r *Runner) finish(job claimed, worker string, runErr error, final bool) error {
	var updates map[string]any
	msg := ""
	switch {
	case runErr == nil:
		updates = map[string]any{"status": models.JobDone, "locked_at": nil, "last_error": "", "updated_at": time.Now()}
	case final || job.Attempts >= job.MaxAttempts:
		msg = CleanError(runErr)
		log.Printf("jobs: %s #%d is dead after %d attempts: %s", job.Kind, job.ID, job.Attempts, msg)
		updates = map[string]any{"status": models.JobDead, "locked_at": nil, "last_error": msg, "updated_at": time.Now()}
	default:
		msg = CleanError(runErr)
		wait := Backoff(job.Attempts, r.Rnd)
		log.Printf("jobs: %s #%d failed (attempt %d of %d), retrying in %s: %s", job.Kind, job.ID, job.Attempts, job.MaxAttempts, wait.Round(time.Second), msg)
		updates = map[string]any{"status": models.JobFailed, "locked_at": nil, "last_error": msg,
			"run_at": time.Now().Add(wait), "updated_at": time.Now()}
	}
	res := r.DB.Model(&models.Job{}).Where("id = ? AND status = ? AND locked_by = ?", job.ID, models.JobRunning, worker).Updates(updates)
	if res.Error == nil && res.RowsAffected == 0 {
		// Its lease ran out while it ran and another worker took it; that
		// worker's outcome stands.
		log.Printf("jobs: %s #%d ran past its lease; another worker took it over", job.Kind, job.ID)
	}
	return res.Error
}

var emailLike = regexp.MustCompile(`[^\s<>"'(),;:]+@[^\s<>"'(),;:]+`)

// CleanError is the error text a job keeps and the System page shows: its
// first line, without email addresses (an SMTP refusal echoes the
// recipient) or NUL bytes, as valid UTF-8, at most 2000 bytes.
func CleanError(err error) string {
	msg, _, _ := strings.Cut(err.Error(), "\n")
	msg = emailLike.ReplaceAllString(msg, "[email]")
	msg = strings.ReplaceAll(msg, "\x00", "")
	if len(msg) > maxErrorLen {
		msg = msg[:maxErrorLen]
	}
	return strings.ToValidUTF8(msg, "?")
}

// PruneDone deletes finished and dismissed jobs last touched before
// cutoff. Dead jobs stay until an admin retries or dismisses them.
func PruneDone(db *gorm.DB, cutoff time.Time) (int64, error) {
	res := db.Where("status IN ? AND updated_at < ?", []string{models.JobDone, models.JobDismissed}, cutoff).Delete(&models.Job{})
	return res.RowsAffected, res.Error
}
