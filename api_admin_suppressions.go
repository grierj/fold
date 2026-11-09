package main

import (
	"net/http"
	"strconv"
	"time"

	"fold/internal/audit"
	"fold/internal/mail"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Admins see who Fold stopped mailing and why, and clear it (M3.2c,
// DEC-113): someone fixed a typo in their address, or asks for mail again.

// openSuppressions maps each suppressed email address to its strongest
// open reason (a hard bounce or a manual one before a complaint) and when
// the earliest one began. One read for the whole user list. The key is the
// address hash: the row's own, or the hash of its stored address while a
// legacy row's hash is still NULL (R4-S3). Look an account up with
// mail.AddressHash(email).
func openSuppressions(db *gorm.DB) (map[string]*emailSuppressionJSON, error) {
	var rows []models.Suppression
	if err := db.Where("channel = ? AND cleared_at IS NULL", "email").Order("created_at").Find(&rows).Error; err != nil {
		return nil, err
	}
	reasons := map[string][]string{}
	out := map[string]*emailSuppressionJSON{}
	for _, r := range rows {
		key := mail.StoredAddressHash(r.Address)
		if r.AddressHash != nil {
			key = *r.AddressHash
		}
		reasons[key] = append(reasons[key], r.Reason)
		if out[key] == nil {
			out[key] = &emailSuppressionJSON{Since: r.CreatedAt}
		}
	}
	for key, cur := range out {
		cur.Reason = mail.StrongestReason(reasons[key])
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
	address := mail.NormalizeAddress(u.Email)
	var cleared int64
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		// One statement clears them and says which, so the audit row names
		// exactly what was cleared.
		var reasons []string
		if err := tx.Raw(`UPDATE suppressions SET cleared_at = ?, cleared_by = ?
			WHERE channel = 'email' AND cleared_at IS NULL
			AND (address_hash = ? OR (address_hash IS NULL AND address = ?)) RETURNING reason`,
			time.Now(), me.ID, mail.AddressHash(u.Email), address).
			Scan(&reasons).Error; err != nil {
			return err
		}
		if len(reasons) == 0 {
			return nil
		}
		cleared = int64(len(reasons))
		return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.EmailSuppressionCleared, TargetType: "user", TargetID: u.ID,
			Meta: map[string]any{"reasons": reasons}, IP: c.ClientIP()})
	})
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not clear the email block")
		return
	}
	c.JSON(http.StatusOK, gin.H{"cleared": cleared})
}
