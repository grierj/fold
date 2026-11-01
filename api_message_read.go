package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"fold/internal/content"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
)

// Reading an announcement in Fold (M3.13d2): the page an announcement's
// push opens. A recipient may read what they were sent (policy
// CanReadMessage), and nothing else about it: no audience, counts,
// history or other recipients. Its author and admins may read it too.

// messageReadJSON is GET /messages/:id/read: the message as its reader's
// email said it, merge fields filled with their own values.
type messageReadJSON struct {
	ID      uint   `json:"id"`
	Subject string `json:"subject"`
	// From is "Pat Lee via First Church", or the church's name when the
	// author's account is gone (PLAN 7.8).
	From string `json:"from"`
	// SentAt is when it went out (when it started, while it is sending).
	SentAt   *time.Time `json:"sentAt"`
	BodyHTML string     `json:"bodyHtml"`
}

// isMessageRecipient: the person has a direct row (not a guardian copy)
// in any channel for an announcement that is sending or sent.
func (a *App) isMessageRecipient(m *models.Message, userID uint) (bool, error) {
	if m.Status != models.MessageSending && m.Status != models.MessageSent {
		return false, nil
	}
	var n int64
	err := a.DB.Model(&models.MessageRecipient{}).
		Where("message_id = ? AND user_id = ? AND copy_of_user_id IS NULL", m.ID, userID).Limit(1).Count(&n).Error
	return n > 0, err
}

func (a *App) apiReadMessage(c *gin.Context) {
	me := apiUser(c)
	id, err := strconv.ParseUint(c.Param("id"), 10, 63)
	var m models.Message
	if err != nil || a.DB.First(&m, id).Error != nil {
		jsonError(c, http.StatusNotFound, "message not found")
		return
	}
	p := asPerson(me)
	isAuthor := m.AuthorID != nil && *m.AuthorID == me.ID
	reminder := m.Category == models.MessageCategoryReminder
	manages, recipient := false, false
	if reminder && !p.Admin && m.OccurrenceID != nil {
		if manages, err = a.managesOccurrenceEvent(me, *m.OccurrenceID); err != nil {
			jsonError(c, http.StatusInternalServerError, "could not load the message")
			return
		}
	}
	if !reminder && !p.Admin && !isAuthor {
		if recipient, err = a.isMessageRecipient(&m, me.ID); err != nil {
			jsonError(c, http.StatusInternalServerError, "could not load the message")
			return
		}
	}
	// Anyone else hears what a message that isn't there answers.
	if !policy.CanReadMessage(p, isAuthor, reminder, manages, recipient) {
		jsonError(c, http.StatusNotFound, "message not found")
		return
	}
	var au audienceJSON
	_ = json.Unmarshal([]byte(m.Audience), &au)
	values, err := a.recipientValues(a.DB, &m, au, map[uint]*models.User{me.ID: me})
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load the message")
		return
	}
	vals := values[me.ID]
	sentAt := m.SentAt
	if sentAt == nil {
		sentAt = m.ScheduledAt
	}
	// The stored body is sanitized; it is sanitized again once the
	// reader's values are in, so the page gets only the composer's
	// formatting (no script, style, image or frame).
	c.JSON(http.StatusOK, gin.H{"message": messageReadJSON{
		ID:       m.ID,
		Subject:  strings.TrimSpace(content.Merge(m.Subject, vals, false)),
		From:     a.messageFromName(a.DB, &m, a.siteName()),
		SentAt:   sentAt,
		BodyHTML: content.SanitizeHTML(content.Merge(m.BodyHTML, vals, true)),
	}})
}
