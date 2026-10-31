package main

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"fold/internal/events"
	"fold/internal/jobs"
	"fold/internal/notify"
)

// Daily background work (M3.1, PLAN 5.5). Each task is a job queued once
// per church day under the key "<kind>:<church date>", at startup and again
// at church-local midnight (DEC-83 for the age rollover). Any number of app
// processes may queue it; the key keeps it to one run, and a restart later
// the same day doesn't run it again.

// doneJobsKept is how long finished jobs stay in the table.
const doneJobsKept = 30 * 24 * time.Hour

// dailyKinds are the daily tasks. Each returns its error so the worker
// retries it with backoff; a task that keeps failing is dead and shows on
// the admin System page (DEC-111). Every one is safe to run twice.
func (a *App) dailyKinds() []jobs.Kind {
	daily := func(name string, run func(ctx context.Context, now time.Time) error) jobs.Kind {
		return jobs.Kind{Name: name, MaxAttempts: jobs.DefaultMaxAttempts, Run: func(ctx context.Context, _ json.RawMessage) error {
			return run(ctx, time.Now().UTC())
		}}
	}
	return []jobs.Kind{
		// Stored occurrences reach 18 months ahead (DEC-89).
		daily("occurrences.extend", func(ctx context.Context, now time.Time) error {
			var emailed []uint
			err := events.ExtendLocked(ctx, a.DB, now, a.afterExtend(&emailed))
			a.deliverNotificationEmails(emailed)
			return err
		}),
		// Contact requests expire and grants end (5.11).
		daily("contact.sweep", a.sweepContacts),
		daily("cleanup.login_throttles", func(ctx context.Context, _ time.Time) error { return a.pruneLoginThrottles(ctx) }),
		// Notification retention (DEC-45).
		daily("notifications.cleanup", func(_ context.Context, now time.Time) error {
			n, err := notify.Prune(a.DB, now)
			if n > 0 {
				log.Printf("notifications: pruned %d", n)
			}
			return err
		}),
		daily("accounts.expire_requests", func(context.Context, time.Time) error {
			n, err := a.expirePendingRequests()
			if n > 0 {
				log.Printf("account requests: expired %d", n)
			}
			return err
		}),
		// Teens turn 18 and children 13 on the church's calendar (DEC-83).
		daily("accounts.age_rollover", func(_ context.Context, now time.Time) error {
			n, err := a.rolloverAges(now)
			if n > 0 {
				log.Printf("age rollover: updated %d people", n)
			}
			return err
		}),
		daily("jobs.cleanup", func(_ context.Context, now time.Time) error {
			_, err := jobs.PruneDone(a.DB, now.Add(-doneJobsKept))
			return err
		}),
	}
}

// startJobs starts the workers (FOLD_WORKERS, 0 for none) and the daily
// scheduler. The scheduler runs even with no workers here, so another
// process's workers still find the day's tasks. It returns a function that
// waits for running jobs to finish after ctx ends.
func (a *App) startJobs(ctx context.Context) func() {
	kinds := a.dailyKinds()
	runner := jobs.NewRunner(a.DB, kinds...)
	runner.Poll = a.Config.JobPoll
	if a.Config.Workers <= 0 {
		log.Println("WARNING: FOLD_WORKERS=0; background jobs will not run in this process")
	}
	wait := runner.Start(ctx, a.Config.Workers)
	// Dates are extended at every start, as before jobs: a new build may
	// change how far ahead they go. It is safe to run twice.
	if _, err := jobs.Enqueue(a.DB, "occurrences.extend", nil, time.Now(), "", jobs.DefaultMaxAttempts); err != nil {
		log.Printf("jobs: queue date extension: %v", err)
	}
	go func() {
		for {
			now := time.Now()
			// The church's zone decides the day. If it can't be read, nothing
			// is queued under a guessed day; try again shortly.
			next := 30 * time.Second
			if zone, err := churchZoneErr(a.DB); err != nil {
				log.Printf("jobs: read the church time zone: %v", err)
			} else if err := a.queueDaily(kinds, now, zone); err != nil {
				log.Printf("jobs: queue daily tasks: %v", err)
			} else {
				// At church midnight, or within the hour if the zone changes.
				next = min(time.Until(nextAgeRollover(now, zone)), time.Hour) + time.Second
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(next):
			}
		}
	}()
	return wait
}

// queueDaily queues each daily task for the church's date in zone. A kind
// that fails to queue doesn't stop the others; the first error is returned.
func (a *App) queueDaily(kinds []jobs.Kind, now time.Time, zone *time.Location) error {
	day := now.In(zone).Format("2006-01-02")
	var first error
	for _, k := range kinds {
		if _, err := jobs.Enqueue(a.DB, k.Name, nil, now, k.Name+":"+day, k.MaxAttempts); err != nil && first == nil {
			first = err
		}
	}
	return first
}
