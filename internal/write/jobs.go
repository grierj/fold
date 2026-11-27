package write

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	"fold/internal/keys"
)

// Background work is rows in jobs, run by one goroutine (AGENTS.md). Mail and
// other network calls happen here, after the command that queued them has
// committed. A handler must be safe to run twice: a crash after the work and
// before the bookkeeping runs it again.

// NewJob is a job to queue from inside a command.
type NewJob struct {
	Kind string
	Ref  int64 // the id of the row the job is about
	// Secret, when set, is sealed while the job waits and erased when it ends.
	Secret []byte
	RunAt  time.Time // zero means now
	// UniqueKey, when set, makes a second job with the same key a no-op.
	UniqueKey string
}

// Enqueue queues j in the command's transaction.
func (t *Tx) Enqueue(j NewJob) error {
	t.queued = true
	return enqueue(t.ctx, t.tx, t.keys, t.Now, j)
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func enqueue(ctx context.Context, x execer, k *keys.Keys, now time.Time, j NewJob) error {
	runAt := j.RunAt
	if runAt.IsZero() {
		runAt = now
	}
	var secret []byte
	if j.Secret != nil {
		secret = k.Seal(keys.Jobs, j.Secret)
	}
	var unique any
	if j.UniqueKey != "" {
		unique = j.UniqueKey
	}
	_, err := x.ExecContext(ctx, `INSERT INTO jobs (kind, ref, secret, unique_key, run_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (unique_key) DO NOTHING`,
		j.Kind, j.Ref, secret, unique, runAt.Unix(), now.Unix())
	if err != nil {
		return fmt.Errorf("write: enqueue %s: %w", j.Kind, err)
	}
	return nil
}

// CancelJobs cancels pending jobs of a kind about one row (a newer link
// replaces an older one, so its email is no longer wanted).
func (t *Tx) CancelJobs(kind string, ref int64) error {
	_, err := t.Exec(`UPDATE jobs SET state = 'cancelled', secret = NULL, finished_at = ?
		WHERE kind = ? AND ref = ? AND state = 'pending'`, t.Now.Unix(), kind, ref)
	return err
}

// Job is what a handler receives.
type Job struct {
	ID       int64
	Kind     string
	Ref      int64
	Secret   []byte // opened; nil if none
	Attempts int
}

// Handler does one job. Returning Permanent(err) stops retries.
type Handler func(ctx context.Context, j Job) error

type permanent struct{ error }

func (p permanent) Unwrap() error { return p.error }

// Permanent marks an error that retrying cannot fix.
func Permanent(err error) error { return permanent{err} }

// Recurring is a job that schedules its next run when it ends.
type Recurring struct {
	Kind string
	Next func(now time.Time) time.Time
}

// Runner is the one goroutine that does background work.
type Runner struct {
	e         *Executor
	handlers  map[string]Handler
	recurring []Recurring
	log       *slog.Logger
	poll      time.Duration
}

const maxAttempts = 6

func NewRunner(e *Executor, log *slog.Logger) *Runner {
	return &Runner{e: e, handlers: map[string]Handler{}, log: log, poll: 15 * time.Second}
}

func (r *Runner) Handle(kind string, h Handler) { r.handlers[kind] = h }

func (r *Runner) Every(rec Recurring) { r.recurring = append(r.recurring, rec) }

// Schedule makes sure every recurring job has its next run queued.
func (r *Runner) Schedule(ctx context.Context) error {
	now := r.e.clock.Now()
	for _, rec := range r.recurring {
		at := rec.Next(now)
		key := fmt.Sprintf("%s@%d", rec.Kind, at.Unix())
		if err := enqueue(ctx, r.e.w, r.e.keys, now, NewJob{Kind: rec.Kind, RunAt: at, UniqueKey: key}); err != nil {
			return err
		}
	}
	return nil
}

// Loop runs due jobs until ctx ends.
func (r *Runner) Loop(ctx context.Context) {
	if err := r.Schedule(ctx); err != nil {
		r.log.Error("jobs: schedule", "err", err)
	}
	t := time.NewTicker(r.poll)
	defer t.Stop()
	for {
		if _, err := r.RunDue(ctx); err != nil && ctx.Err() == nil {
			r.log.Error("jobs: run", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-r.e.wake:
		}
	}
}

// RunDue runs every job that is due now, one at a time, and returns how many
// it ran. Tests call it directly instead of waiting on the loop.
func (r *Runner) RunDue(ctx context.Context) (int, error) {
	n := 0
	for ctx.Err() == nil {
		j, ok, err := r.next(ctx)
		if err != nil || !ok {
			return n, err
		}
		r.run(ctx, j)
		n++
	}
	return n, ctx.Err()
}

func (r *Runner) next(ctx context.Context) (Job, bool, error) {
	var j Job
	var sealed []byte
	err := r.e.w.QueryRowContext(ctx, `SELECT id, kind, ref, secret, attempts FROM jobs
		WHERE state = 'pending' AND run_at <= ? ORDER BY run_at, id LIMIT 1`, r.e.clock.Now().Unix()).
		Scan(&j.ID, &j.Kind, &j.Ref, &sealed, &j.Attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, err
	}
	if sealed != nil {
		if j.Secret, _, err = r.e.keys.Open(keys.Jobs, sealed); err != nil {
			// A secret from before a key change cannot be used. Nothing
			// retrying can do; the person asks again.
			r.finish(ctx, j, permanent{err})
			return r.next(ctx)
		}
	}
	return j, true, nil
}

func (r *Runner) run(ctx context.Context, j Job) {
	h, ok := r.handlers[j.Kind]
	var err error
	if !ok {
		err = permanent{fmt.Errorf("no handler for job kind %q", j.Kind)}
	} else {
		err = h(ctx, j)
	}
	r.finish(ctx, j, err)
}

// finish records the outcome. A failure is retried with a growing wait,
// up to maxAttempts.
func (r *Runner) finish(ctx context.Context, j Job, jobErr error) {
	now := r.e.clock.Now()
	attempts := j.Attempts + 1
	var err error
	switch {
	case jobErr == nil:
		_, err = r.e.w.ExecContext(ctx, `UPDATE jobs SET state = 'done', secret = NULL, attempts = ?, last_error = '', finished_at = ? WHERE id = ?`,
			attempts, now.Unix(), j.ID)
		if err == nil {
			r.scheduleNext(ctx, j.Kind)
		}
	case errors.As(jobErr, new(permanent)) || attempts >= maxAttempts:
		r.log.Warn("jobs: failed", "kind", j.Kind, "id", j.ID, "attempts", attempts, "err", scrub(jobErr.Error()))
		_, err = r.e.w.ExecContext(ctx, `UPDATE jobs SET state = 'failed', secret = NULL, attempts = ?, last_error = ?, finished_at = ? WHERE id = ?`,
			attempts, scrub(jobErr.Error()), now.Unix(), j.ID)
		if err == nil {
			r.scheduleNext(ctx, j.Kind)
		}
	default:
		wait := time.Duration(attempts*attempts) * 30 * time.Second
		r.log.Info("jobs: will retry", "kind", j.Kind, "id", j.ID, "attempts", attempts, "in", wait, "err", scrub(jobErr.Error()))
		_, err = r.e.w.ExecContext(ctx, `UPDATE jobs SET attempts = ?, last_error = ?, run_at = ? WHERE id = ?`,
			attempts, scrub(jobErr.Error()), now.Add(wait).Unix(), j.ID)
	}
	if err != nil {
		r.log.Error("jobs: record outcome", "id", j.ID, "err", err)
	}
}

func (r *Runner) scheduleNext(ctx context.Context, kind string) {
	for _, rec := range r.recurring {
		if rec.Kind != kind {
			continue
		}
		now := r.e.clock.Now()
		at := rec.Next(now)
		key := fmt.Sprintf("%s@%d", rec.Kind, at.Unix())
		if err := enqueue(ctx, r.e.w, r.e.keys, now, NewJob{Kind: kind, RunAt: at, UniqueKey: key}); err != nil {
			r.log.Error("jobs: schedule next", "kind", kind, "err", err)
		}
	}
}

var addressPattern = regexp.MustCompile(`[^\s<>"'@]+@[^\s<>"']+`)

// scrub removes email addresses and caps the length, so a provider's error
// message does not put personal data in the jobs table or the log.
func scrub(s string) string {
	s = addressPattern.ReplaceAllString(s, "(address)")
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}
