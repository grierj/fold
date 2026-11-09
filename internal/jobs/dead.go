package jobs

import (
	"time"

	"fold/models"

	"gorm.io/gorm"
)

// The state changes an admin makes to a dead job live here with the rest of
// the job state machine, so no caller writes the table's status columns.

// LockDead locks job id and reports whether it is dead. A caller that alerts
// about a death takes it so that an admin's retry or dismiss either lands
// first (the job is no longer dead) or waits.
func LockDead(tx *gorm.DB, id uint) (bool, error) {
	var status string
	if err := tx.Raw("SELECT status FROM jobs WHERE id = ? FOR UPDATE", id).Scan(&status).Error; err != nil {
		return false, err
	}
	return status == models.JobDead, nil
}

// Retry queues a dead job again from its first attempt. It reports false
// when the job is not dead (someone already handled it), so two admins
// cannot both act on one.
func Retry(tx *gorm.DB, id uint) (bool, error) {
	return decideDead(tx, id, map[string]any{"status": models.JobQueued, "attempts": 0,
		"run_at": time.Now(), "locked_at": nil, "locked_by": "", "updated_at": time.Now()})
}

// Dismiss sets a dead job aside without running it; PruneDone removes it
// later. It reports false when the job is not dead.
func Dismiss(tx *gorm.DB, id uint) (bool, error) {
	return decideDead(tx, id, map[string]any{"status": models.JobDismissed, "updated_at": time.Now()})
}

func decideDead(tx *gorm.DB, id uint, updates map[string]any) (bool, error) {
	res := tx.Model(&models.Job{}).Where("id = ? AND status = ?", id, models.JobDead).Updates(updates)
	return res.RowsAffected == 1, res.Error
}
