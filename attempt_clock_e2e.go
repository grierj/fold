//go:build e2eclock

package main

import (
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"fold/models"

	"github.com/gin-gonic/gin"
)

// A test-only clock for the browser tests of attempt ids (T07B-1c). An id's
// age is the server's own monotonic time, which a browser cannot move, so this
// file, compiled only with -tags e2eclock, adds an offset to the App clock and an
// admin-only route that changes it, and one admin-only route that seeds a
// deleted account for the legacy reason review. It does nothing unless
// FOLD_E2E_CLOCK=1 is set (web/e2e/server.sh sets it); it never exists in a
// build without the tag.
func init() {
	e2eRoutes = func(a *App, v1 *gin.RouterGroup) {
		if os.Getenv("FOLD_E2E_CLOCK") != "1" {
			return
		}
		var offset atomic.Int64
		a.clock = func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }
		// POST {"offsetSeconds": N} sets how far ahead the attempt clock runs.
		v1.POST("/e2e/attempt-clock", a.apiAuth(), a.apiAdmin(), func(c *gin.Context) {
			var body struct {
				OffsetSeconds int64 `json:"offsetSeconds"`
			}
			if err := c.ShouldBindJSON(&body); err != nil || body.OffsetSeconds < 0 || body.OffsetSeconds > 24*3600 {
				jsonError(c, http.StatusBadRequest, "invalid request")
				return
			}
			offset.Store(body.OffsetSeconds * int64(time.Second))
			c.JSON(http.StatusOK, gin.H{"offsetSeconds": body.OffsetSeconds})
		})
		// POST {"userId": N} soft-deletes one live account without deciding any of
		// its contact-request reasons, which stands in for a deletion made before
		// reasons were reviewed. No live path leaves that state (a deletion records a
		// decision for every reason it reaches), so the legacy review page has
		// nothing to open without it. The caller and any admin are refused so a
		// shard cannot lock itself out (a deleted admin is covered by the Go
		// tests); 200 means exactly one live account became a tombstone, and a
		// missing or already deleted id is a 404.
		v1.POST("/e2e/legacy-tombstone", a.apiAuth(), a.apiAdmin(), func(c *gin.Context) {
			var body struct {
				UserID uint `json:"userId"`
			}
			if err := c.ShouldBindJSON(&body); err != nil || body.UserID == 0 {
				jsonError(c, http.StatusBadRequest, "invalid request")
				return
			}
			if me := apiUser(c); me == nil || me.ID == body.UserID {
				jsonError(c, http.StatusBadRequest, "refused: that is the signed-in account")
				return
			}
			res := a.DB.Exec("UPDATE users SET deleted_at = now() WHERE id = ? AND deleted_at IS NULL AND role <> ?", body.UserID, models.RoleAdmin)
			if res.Error != nil {
				jsonError(c, http.StatusInternalServerError, "could not tombstone")
				return
			}
			if res.RowsAffected == 1 {
				c.JSON(http.StatusOK, gin.H{"userId": body.UserID})
				return
			}
			// Nothing changed: an admin is refused, anything else is not a live account.
			var admins int64
			if err := a.DB.Raw("SELECT count(*) FROM users WHERE id = ? AND deleted_at IS NULL AND role = ?", body.UserID, models.RoleAdmin).Scan(&admins).Error; err != nil {
				jsonError(c, http.StatusInternalServerError, "could not tombstone")
				return
			}
			if admins > 0 {
				jsonError(c, http.StatusBadRequest, "refused: that account is an admin")
				return
			}
			jsonError(c, http.StatusNotFound, "no live account with that id")
		})
	}
}
