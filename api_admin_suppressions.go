package main

import (
	"net/http"
	"strconv"
	"time"

	"fold/internal/audit"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Admins see who Fold stopped mailing and why, and clear it (M3.2c,
// DEC-113): someone fixed a typo in their address, or asks for mail again.

// openSuppressions maps each suppressed email address to its strongest
// open reason (a hard bounce or a manual one before a complaint) and when
// the earliest one began. One read for the whole user list.
func openSuppressions(db *gorm.DB) (map[string]*emailSuppressionJSON, error) {
	var rows []models.Suppression
	if err := db.Where("channel = ? AND cleared_at IS NULL", "email").Order("created_at").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := map[string]*emailSuppressionJSON{}
	for _, r := range rows {
		cur := out[r.Address]
		switch {
		case cur == nil:
			out[r.Address] = &emailSuppressionJSON{Reason: r.Reason, Since: r.CreatedAt}
		case cur.Reason == models.SuppressComplaint && r.Reason != models.SuppressComplaint:
			cur.Reason = r.Reason
		}
	}
	return out, nil
}

// apiAdminClearSuppression lets Fold mail a person's address again,
// clearing every open suppression on it, audited. Clearing nothing is fine.
func (a *App) apiAdminClearSuppression(c *gin.Context) {
	me := apiUser(c)
	if !policy.CanClearSuppression(asPerson(me)) {
		jsonError(c, http.StatusForbidden, "admin required")
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 63)
	var u models.User
	if err != nil || a.DB.First(&u, id).Error != nil {
		jsonError(c, http.StatusNotFound, "user not found")
		return
	}
	address := normalizeEmail(u.Email)
	var cleared int64
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		var reasons []string
		if err := tx.Model(&models.Suppression{}).Where("channel = ? AND address = ? AND cleared_at IS NULL", "email", address).
			Pluck("reason", &reasons).Error; err != nil {
			return err
		}
		res := tx.Model(&models.Suppression{}).Where("channel = ? AND address = ? AND cleared_at IS NULL", "email", address).
			Updates(map[string]any{"cleared_at": time.Now(), "cleared_by": me.ID})
		if res.Error != nil || res.RowsAffected == 0 {
			return res.Error
		}
		cleared = res.RowsAffected
		return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.EmailSuppressionCleared, TargetType: "user", TargetID: u.ID,
			Meta: map[string]any{"reasons": reasons}, IP: c.ClientIP()})
	})
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not clear the email block")
		return
	}
	c.JSON(http.StatusOK, gin.H{"cleared": cleared})
}
