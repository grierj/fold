package main

import (
	"net/http"

	"fold/internal/policy"

	"github.com/gin-gonic/gin"
)

// The System page's backup panel (M5.2). The answer is one of the five states
// in backup_status.go, so a missing, stale or unreadable status file is a 200
// with that state, never an error a person can't act on (DEC-52).

func (a *App) backupViewer() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !policy.CanSeeBackupStatus(asPerson(apiUser(c))) {
			jsonError(c, http.StatusForbidden, "admin required")
			c.Abort()
			return
		}
		c.Next()
	}
}

func (a *App) apiAdminBackup(c *gin.Context) {
	c.JSON(http.StatusOK, readBackupStatus(a.Config.BackupDir, a.now()))
}
