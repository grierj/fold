package main

import (
	"net/http"
	"strings"

	"fold/internal/audit"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// The admin settings the send pipeline reads (M3.5b): guardian copies of
// messages to teens (DEC-19, off by default) and where replies to an
// announcement go (DEC-130). Each change is audited in its transaction.

var errReplyToInvalid = apiErr(http.StatusBadRequest, "reply_to_invalid", "enter one email address, or leave it empty")

func (a *App) saveSetting(c *gin.Context, key, value, action string, meta map[string]any) error {
	me := apiUser(c)
	return a.DB.Transaction(func(tx *gorm.DB) error {
		if err := setSettingIn(tx, key, value); err != nil {
			return err
		}
		return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: action, TargetType: "setting", Meta: meta, IP: c.ClientIP()})
	})
}

func (a *App) apiAdminGetGuardianCopies(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"on": boolSettingIn(a.DB, settingGuardianCopies, false)})
}

func (a *App) apiAdminSetGuardianCopies(c *gin.Context) {
	var body struct {
		On *bool `json:"on"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.On == nil {
		jsonError(c, http.StatusBadRequest, "invalid request")
		return
	}
	value := "false"
	if *body.On {
		value = "true"
	}
	if err := a.saveSetting(c, settingGuardianCopies, value, audit.GuardianCopiesChanged, map[string]any{"on": *body.On}); err != nil {
		jsonError(c, http.StatusInternalServerError, "could not save the setting")
		return
	}
	c.JSON(http.StatusOK, gin.H{"on": *body.On})
}

func (a *App) apiAdminGetReplyTo(c *gin.Context) {
	address, err := settingValueIn(a.DB, settingReplyTo)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load the address")
		return
	}
	c.JSON(http.StatusOK, gin.H{"address": address})
}

// apiAdminSetReplyTo sets where replies to announcements go: one plain
// address, usually the church office's, or empty for none (DEC-130).
func (a *App) apiAdminSetReplyTo(c *gin.Context) {
	var body struct {
		Address *string `json:"address"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Address == nil {
		jsonError(c, http.StatusBadRequest, "invalid request")
		return
	}
	addr := strings.TrimSpace(*body.Address)
	if addr != "" && !validEmail(addr) {
		fail(c, errReplyToInvalid)
		return
	}
	if err := a.saveSetting(c, settingReplyTo, addr, audit.ReplyToChanged, map[string]any{"set": addr != ""}); err != nil {
		jsonError(c, http.StatusInternalServerError, "could not save the setting")
		return
	}
	c.JSON(http.StatusOK, gin.H{"address": addr})
}
