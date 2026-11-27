package backup

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"fold/internal/audit"
	"fold/internal/brand"
	"fold/internal/db"
	"fold/internal/mail"
	"fold/internal/settings"
	"fold/internal/write"
)

// Job kinds.
const (
	JobNightly = "backup.nightly"
	JobCheck   = "audit.check"   // daily: is the chain whole, do recent checkpoints hold
	JobMonthly = "audit.monthly" // the checkpoint email to every admin
)

// Jobs runs the backup and audit work in the background.
type Jobs struct {
	Exec    *write.Executor
	Reader  *db.Reader
	DataDir string
	Outbox  *mail.Outbox
	Dev     bool
	Log     *slog.Logger
}

// Register adds the handlers and their schedules to the runner.
func (j *Jobs) Register(r *write.Runner) {
	r.Handle(JobNightly, j.nightly)
	r.Handle(JobCheck, j.check)
	r.Handle(JobMonthly, j.monthly)
	r.Every(write.Recurring{Kind: JobNightly, Next: j.at(3, false)})
	r.Every(write.Recurring{Kind: JobCheck, Next: j.at(4, false)})
	r.Every(write.Recurring{Kind: JobMonthly, Next: j.at(9, true)})
}

// at is the next given hour on the church's clock: tomorrow's, or the first
// of next month's.
func (j *Jobs) at(hour int, monthly bool) func(time.Time) time.Time {
	return func(now time.Time) time.Time {
		loc := j.zone()
		t := now.In(loc)
		next := time.Date(t.Year(), t.Month(), t.Day(), hour, 0, 0, 0, loc)
		if monthly {
			next = time.Date(t.Year(), t.Month(), 1, hour, 0, 0, 0, loc)
			for !next.After(now) {
				next = next.AddDate(0, 1, 0)
			}
			return next
		}
		for !next.After(now) {
			next = next.AddDate(0, 0, 1)
		}
		return next
	}
}

func (j *Jobs) zone() *time.Location {
	var name string
	j.Reader.Read(context.Background(), func(q *sql.Tx) error {
		s, err := settings.Load(context.Background(), q)
		name = s[settings.ChurchZone]
		return err
	})
	if name == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(name)
	if err != nil || name == "Local" {
		return time.UTC
	}
	return loc
}

func (j *Jobs) nightly(ctx context.Context, _ write.Job) error {
	m, err := Take(ctx, j.Exec, j.DataDir)
	if err != nil {
		return err
	}
	j.Log.Info("backup taken", "name", m.Name, "checkpoint", m.Checkpoint.String())
	return nil
}

// check verifies the chain, and that the checkpoint of every backup taken
// since the last restore still holds. A restore rewinds the log honestly and
// says so in its own row, which explains checkpoints from before it.
func (j *Jobs) check(ctx context.Context, _ write.Job) error {
	rep, err := j.Exec.Verify(ctx)
	if err != nil {
		return err
	}
	problem := ""
	if !rep.Intact() {
		problem = rep.Problem
	} else {
		lastRestore, err := j.lastRestore(ctx)
		if err != nil {
			return err
		}
		metas, err := List(j.DataDir)
		if err != nil {
			return err
		}
		for _, m := range metas {
			if m.TakenAt.Unix() < lastRestore {
				continue
			}
			var ok bool
			err := j.Reader.Read(ctx, func(q *sql.Tx) error {
				var err error
				ok, err = audit.Holds(ctx, q, m.Checkpoint)
				return err
			})
			if err != nil {
				return err
			}
			if !ok {
				problem = fmt.Sprintf("the log no longer matches the checkpoint in backup %s (row %d)", m.Name, m.Checkpoint.Seq)
				break
			}
		}
	}
	if problem == "" {
		return nil
	}
	j.Log.Error("audit log check failed", "problem", problem)
	return j.tellAdmins(ctx, "The audit log check failed",
		[]string{
			"The daily check of the site's sealed audit log found a problem: " + problem + ".",
			"This can mean someone changed the database directly on the server. Nothing on the site was stopped.",
			"Keep this email. Ask whoever runs your server to look into it, and keep the backups and older checkpoint emails: they show what the log said before.",
		})
}

func (j *Jobs) lastRestore(ctx context.Context) (int64, error) {
	var at sql.NullInt64
	err := j.Reader.Read(ctx, func(q *sql.Tx) error {
		// The one read of the log outside verification: when the last
		// restore happened. It is a job, not a page.
		return q.QueryRowContext(ctx, `SELECT max(at) FROM audit_log WHERE action = 'backup.restored'`).Scan(&at)
	})
	return at.Int64, err
}

// monthly emails the current checkpoint to every admin. An older email that
// no longer matches the log is what shows a rewrite.
func (j *Jobs) monthly(ctx context.Context, _ write.Job) error {
	cp, err := j.Exec.Checkpoint(ctx)
	if err != nil {
		return err
	}
	return j.tellAdmins(ctx, "This month's audit checkpoint",
		[]string{
			"Your site keeps a sealed log of every change. Once a month it sends this checkpoint so that a later change to the log would show.",
			fmt.Sprintf("Checkpoint: row %d, %s", cp.Seq, cp.Hash),
			"You do not need to do anything. Keep this email; if the log is ever opened for a serious reason, it is one of the ways to prove the log was not changed.",
		})
}

func (j *Jobs) tellAdmins(ctx context.Context, subject string, paras []string) error {
	var admins []string
	var s settings.Settings
	err := j.Reader.Read(ctx, func(q *sql.Tx) error {
		var err error
		if s, err = settings.Load(ctx, q); err != nil {
			return err
		}
		rows, err := q.QueryContext(ctx, `SELECT email FROM people WHERE is_admin = 1 AND status = 'active' ORDER BY id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e string
			rows.Scan(&e)
			admins = append(admins, e)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	if len(admins) == 0 {
		return nil
	}
	sender, err := s.Sender(j.Exec.Keys(), j.Outbox, j.Dev)
	if err != nil {
		return err
	}
	church := s[settings.ChurchName]
	e := mail.Email{Church: church, Subject: subject, Before: paras,
		Footer: fmt.Sprintf("Sent by %s to the administrators of %s.", brand.Name, church)}
	for _, to := range admins {
		m, err := e.Message(to)
		if err != nil {
			return err
		}
		if _, err := sender.Send(ctx, m); err != nil {
			return err
		}
	}
	return nil
}
