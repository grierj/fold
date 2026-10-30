package main

import (
	"errors"
	"net/http"

	"fold/internal/audit"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// apiCancelEmailChange is an explicit CSRF-protected POST, never a GET/email
// scanner action. The old-address capability cancels pending changes and ends
// every login; it cannot roll an already-verified address back.
func (a *App) apiCancelEmailChange(c *gin.Context) {
	var body struct {
		Token string `json:"token"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Token == "" {
		jsonError(c, http.StatusBadRequest, "that link is invalid or has expired")
		return
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		user, _, err := lockAndConsumeAccountToken(tx, models.TokenEmailChangeCancel, body.Token)
		if err != nil {
			return err
		}
		if err := tx.Model(user).Update("session_version", gorm.Expr("session_version + 1")).Error; err != nil {
			return err
		}
		if err := revokeAccountTokens(tx, user.ID); err != nil {
			return err
		}
		return audit.Write(tx, audit.Entry{Actor: &user.ID, Action: audit.EmailChangeCancelled,
			TargetType: "user", TargetID: user.ID, IP: c.ClientIP()})
	})
	if errors.Is(err, errTokenInvalid) {
		jsonError(c, http.StatusBadRequest, "that link is invalid or has expired")
		return
	}
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not cancel email change")
		return
	}
	// Never refresh the subject's cookie, nor clear an unrelated account's
	// cookie. Epoch invalidation ends all subject sessions, including this
	// browser if it belongs to them; the SPA refreshes its auth state.
	c.JSON(http.StatusOK, gin.H{"cancelled": true})
}
