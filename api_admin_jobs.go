package main

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"fold/internal/audit"
	"fold/internal/jobs"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// The System page (M3.1b, DEC-111): background jobs that ran out of
// attempts, for an admin to retry or set aside. It never shows a job's
// payload, which can name people.

// adminJobJSON is one dead job as the System page shows it.
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

// maxDeadJobsListed bounds the System page; it says when there are more.
const maxDeadJobsListed = 100

// jobKinds is every job kind the server runs, by name, for its label.
func (a *App) jobKinds() map[string]jobs.Kind {
	out := map[string]jobs.Kind{}
	for _, k := range a.dailyKinds() {
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
	c.JSON(http.StatusOK, gin.H{"jobs": out, "more": more})
}

// errJobHandled: the job is no longer dead (another admin, or a second
// tab, already retried or dismissed it).
const errJobHandled = "that task was already handled; refresh the list"

// decideDeadJob moves one dead job to status, audited, or answers why not.
func (a *App) decideDeadJob(c *gin.Context, action string, updates map[string]any) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 63)
	if err != nil {
		jsonError(c, http.StatusNotFound, "job not found")
		return
	}
	me := apiUser(c)
	var job models.Job
	found, handled := true, false
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Select("id, kind").First(&job, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				found = false
				return nil
			}
			return err
		}
		// Only a dead job changes; two admins can't both act on one.
		res := tx.Model(&models.Job{}).Where("id = ? AND status = ?", job.ID, models.JobDead).Updates(updates)
		if res.Error != nil || res.RowsAffected == 0 {
			return res.Error
		}
		handled = true
		return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: action, TargetType: "job", TargetID: job.ID,
			Meta: map[string]any{"kind": job.Kind}, IP: c.ClientIP()})
	})
	switch {
	case err != nil:
		jsonError(c, http.StatusInternalServerError, "could not update the job")
	case !found:
		jsonError(c, http.StatusNotFound, "job not found")
	case !handled:
		jsonError(c, http.StatusConflict, errJobHandled)
	default:
		c.JSON(http.StatusOK, gin.H{"status": updates["status"]})
	}
}

// apiAdminRetryJob queues a dead job again, from its first attempt.
func (a *App) apiAdminRetryJob(c *gin.Context) {
	a.decideDeadJob(c, audit.JobRetried, map[string]any{"status": models.JobQueued, "attempts": 0,
		"run_at": time.Now(), "locked_at": nil, "locked_by": "", "updated_at": time.Now()})
}

// apiAdminDismissJob sets a dead job aside without running it; finished
// jobs' cleanup removes it later.
func (a *App) apiAdminDismissJob(c *gin.Context) {
	a.decideDeadJob(c, audit.JobDismissed, map[string]any{"status": models.JobDismissed, "updated_at": time.Now()})
}
