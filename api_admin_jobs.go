package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"fold/internal/audit"
	"fold/internal/jobs"
	"fold/internal/notify"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// The System page (M3.1b, DEC-111): background jobs that ran out of
// attempts, for an admin to retry or set aside. It never shows a job's
// payload, which can name people.

type adminJobJSON struct {
	ID   uint   `json:"id"`
	Kind string `json:"kind"`
	// Label says what the job does in plain words.
	Label string `json:"label"`
	// RunsDaily: tomorrow's run will try again by itself.
	RunsDaily bool      `json:"runsDaily"`
	Attempts  int       `json:"attempts"`
	LastError string    `json:"lastError"`
	FailedAt  time.Time `json:"failedAt"`
}

// adminDeliverySummaryJSON contains email delivery totals without recipient details.
type adminDeliverySummaryJSON struct {
	Queued         int64      `json:"queued"`
	Deferred       int64      `json:"deferred"`
	Failed         int64      `json:"failed"`
	OldestQueuedAt *time.Time `json:"oldestQueuedAt"`
}

const maxDeadJobsListed = 100

// jobKinds is every job kind the server runs, by name, for its label and
// what dismissing one settles.
func (a *App) jobKinds() map[string]jobs.Kind {
	out := map[string]jobs.Kind{}
	for _, k := range a.allJobKinds() {
		out[k.Name] = k
	}
	return out
}

func (a *App) jobManager() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !policy.CanManageJobs(asPerson(apiUser(c))) {
			jsonError(c, http.StatusForbidden, "admin required")
			c.Abort()
			return
		}
		c.Next()
	}
}

func (a *App) apiAdminDeadJobs(c *gin.Context) {
	var rows []models.Job
	if err := a.DB.Select("id, kind, attempts, last_error, updated_at").Where("status = ?", models.JobDead).
		Order("updated_at DESC, id DESC").Limit(maxDeadJobsListed + 1).Find(&rows).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load background jobs")
		return
	}
	var deliveries adminDeliverySummaryJSON
	if err := a.DB.Raw(`SELECT
		count(*) FILTER (WHERE status = 'queued') AS queued,
		count(*) FILTER (WHERE status = 'deferred') AS deferred,
		count(*) FILTER (WHERE status = 'failed' AND updated_at > now() - interval '7 days') AS failed,
		min(created_at) FILTER (WHERE status = 'queued') AS oldest_queued_at
		FROM notification_deliveries WHERE channel = 'email'`).Scan(&deliveries).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load background jobs")
		return
	}
	more := len(rows) > maxDeadJobsListed
	if more {
		rows = rows[:maxDeadJobsListed]
	}
	kinds := a.jobKinds()
	out := make([]adminJobJSON, 0, len(rows))
	for _, j := range rows {
		k := kinds[j.Kind]
		label := k.Label
		if label == "" {
			label = j.Kind
		}
		out = append(out, adminJobJSON{ID: j.ID, Kind: j.Kind, Label: label, RunsDaily: k.Daily,
			Attempts: j.Attempts, LastError: j.LastError, FailedAt: j.UpdatedAt})
	}
	c.JSON(http.StatusOK, adminJobsJSON{Jobs: out, More: more, Deliveries: deliveries})
}

// errJobHandled: the job is no longer dead (another admin, or a second
// tab, already retried or dismissed it).
var errJobHandled = apiErr(http.StatusConflict, "job_handled", "that task was already handled; refresh the list")

// decideDeadJob moves one dead job to status, audited, or answers why not.
func (a *App) decideDeadJob(c *gin.Context, action string, decide func(tx *gorm.DB, id uint) (bool, error)) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 63)
	if err != nil {
		jsonError(c, http.StatusNotFound, "job not found")
		return
	}
	newStatus := models.JobQueued
	if action == audit.JobDismissed {
		newStatus = models.JobDismissed
	}
	me := apiUser(c)
	var job models.Job
	found, handled := true, false
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Select("id, kind, payload").First(&job, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				found = false
				return nil
			}
			return err
		}
		// Only a dead job changes; two admins can't both act on one.
		done, err := decide(tx, job.ID)
		if err != nil || !done {
			return err
		}
		handled = true
		if k := a.jobKinds()[job.Kind]; action == audit.JobDismissed && k.OnDismiss != nil {
			if err := k.OnDismiss(tx, json.RawMessage(job.Payload)); err != nil {
				return err
			}
		}
		// The alert for this death is done: close every admin's copy. The status
		// UPDATE above holds the job row, so alertDeadJob (which locks it and
		// rechecks) either ran first and is resolved here, or runs after and
		// sees a job that is no longer dead.
		resolution := "retried"
		if action == audit.JobDismissed {
			resolution = "dismissed"
		}
		if _, err := notify.Resolve(tx, notify.SystemJobDead, job.ID, &me.ID, resolution); err != nil {
			return err
		}
		return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: action, TargetType: "job", TargetID: job.ID,
			Meta: map[string]any{"kind": job.Kind}, IP: c.ClientIP()})
	})
	switch {
	case err != nil:
		jsonError(c, http.StatusInternalServerError, "could not update the job")
	case !found:
		jsonError(c, http.StatusNotFound, "job not found")
	case !handled:
		fail(c, errJobHandled)
	default:
		c.JSON(http.StatusOK, gin.H{"status": newStatus})
	}
}

func (a *App) apiAdminRetryJob(c *gin.Context) {
	a.decideDeadJob(c, audit.JobRetried, jobs.Retry)
}

func (a *App) apiAdminDismissJob(c *gin.Context) {
	a.decideDeadJob(c, audit.JobDismissed, jobs.Dismiss)
}
