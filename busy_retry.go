package main

import (
	"context"
	"time"
)

// The R5-C01 busy retry. Three transactions refuse rather than wait on a
// row another transaction holds (NOWAIT, 55P03): the admin account deletion
// (a busy household, the actor seal, a reviewed request's deleted party), the
// settle route's actor seal and the reason page's actor seal. Each runs its
// whole transaction again, a bounded number of times, under one policy:
//
//   - at most busyRetryTries attempts in all;
//   - a new attempt starts only within busyRetryWindow of the first one's
//     start, checked before and again after the backoff;
//   - busyRetryBackoff is the wait before attempt 2 and attempt 3, ended early
//     by the caller's context.
//
// Each caller decides which failure is busy and retryable, and every caller's
// rule retries only an error whose rollback was acknowledged
// (rollbackConfirmed): never an unknown COMMIT and never an unacknowledged
// ROLLBACK, after which the server may still hold the locks.
const busyRetryTries = 3

// Vars so tests can shorten them; a test that sets one stays serial.
var (
	busyRetryWindow  = 2 * time.Second
	busyRetryBackoff = [busyRetryTries - 1]time.Duration{25 * time.Millisecond, 100 * time.Millisecond}
)

// busyRetry counts one request's busy attempts. Start it before the first
// attempt; the window runs from then.
type busyRetry struct {
	ctx     context.Context
	started time.Time
	n       int // busy attempts finished
}

func newBusyRetry(ctx context.Context) *busyRetry {
	return &busyRetry{ctx: ctx, started: time.Now()}
}

// again is called after a busy, retryable attempt. It waits the backoff and
// reports whether the caller should run another attempt. It reports false
// when the tries or the window are spent, and false with the context's error
// when the context ended first.
func (r *busyRetry) again() (bool, error) {
	r.n++
	if r.n >= busyRetryTries || time.Since(r.started) >= busyRetryWindow {
		return false, nil
	}
	timer := time.NewTimer(busyRetryBackoff[r.n-1])
	select {
	case <-r.ctx.Done():
		timer.Stop()
		return false, r.ctx.Err()
	case <-timer.C:
	}
	// Checked again after the backoff: a transaction never starts once the
	// window has closed, however the wait was spent.
	if time.Since(r.started) >= busyRetryWindow {
		return false, nil
	}
	if err := r.ctx.Err(); err != nil {
		return false, err
	}
	return true, nil
}
