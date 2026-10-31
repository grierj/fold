package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"fold/internal/audit"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// A sent message's history and "Try again" (M3.5b, PLAN 7.1, DEC-131).
// The history is the message_recipients snapshot in counts and names, the
// same privacy rule as the composer's preview: counts by status and reason
// for everyone, each person's status in words for the author, each
// person's own reason only for an admin, and never an address (DEC-31).

// recipientCountsJSON counts the people a message was for, by status;
// guardian copies are counted apart (DEC-132). Skipped is the suppressed
// rows (by reason in skippedBy); Teens counts the teens it was for, not
// skipped (DEC-20). GuardianCopies counts the copies, and
// GuardianCopiesMissed the ones that didn't go out (skipped, bounced or
// failed).
type recipientCountsJSON struct {
	Sent                 int `json:"sent"`
	Delivered            int `json:"delivered"`
	Bounced              int `json:"bounced"`
	Complained           int `json:"complained"`
	Failed               int `json:"failed"`
	Skipped              int `json:"skipped"`
	Queued               int `json:"queued"`
	Teens                int `json:"teens"`
	GuardianCopies       int `json:"guardianCopies"`
	GuardianCopiesMissed int `json:"guardianCopiesMissed"`
	// Every row and every failed row, copies included: what "Try again"
	// works from.
	rows, failedRows int
}

// Each person's status as the history lists it. A leader or host can't tell
// a bounce, a complaint, a failure or a skip apart: all are "missed".
const (
	historySent      = "sent"
	historyDelivered = "delivered"
	historyMissed    = "missed"
	historyWaiting   = "waiting"
)

// historyRecipientJSON is one row: a name and a Teen badge, the status,
// and for an admin why they missed it. CopyOf is the teen's name on a
// guardian copy (DEC-19).
type historyRecipientJSON struct {
	Name   string `json:"name"`
	Teen   bool   `json:"teen"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	CopyOf string `json:"copyOf,omitempty"`
}

// messageRecipientsJSON is GET /messages/:id/recipients.
type messageRecipientsJSON struct {
	Counts    recipientCountsJSON `json:"counts"`
	SkippedBy map[string]int      `json:"skippedBy"`
	// Recipients are the first 200 by name.
	Recipients []historyRecipientJSON `json:"recipients"`
	More       bool                   `json:"more"`
	// CanRetry shows "Try again": some sends failed and the author may
	// still write to these people.
	CanRetry bool `json:"canRetry"`
}

const maxHistoryRecipients = 200

const (
	errMessageNothingToRetry = "there's nothing to send again; everyone it could reach got it"
	errMessageRetryRefused   = "its author can no longer write to these people, so it can't be sent again"
)

// recipientCounts counts m's rows by status, and the skipped by reason,
// in one query.
func (a *App) recipientCounts(id uint) (recipientCountsJSON, map[string]int, error) {
	var groups []struct {
		Status string
		Reason string
		Teen   bool
		Copy   bool
		N      int
	}
	err := a.DB.Raw(`SELECT r.status, CASE WHEN r.status = ? AND r.copy_of_user_id IS NULL THEN r.reason ELSE '' END AS reason,
		(u.account_type = ? AND r.copy_of_user_id IS NULL) AS teen, r.copy_of_user_id IS NOT NULL AS copy, count(*) AS n
		FROM message_recipients r JOIN users u ON u.id = r.user_id
		WHERE r.message_id = ? GROUP BY 1, 2, 3, 4`, models.RecipientSuppressed, models.AccountTeen, id).Scan(&groups).Error
	var c recipientCountsJSON
	by := map[string]int{}
	for _, g := range groups {
		c.rows += g.N
		if g.Status == models.RecipientFailed {
			c.failedRows += g.N
		}
		if g.Copy {
			c.GuardianCopies += g.N
			switch g.Status {
			case models.RecipientSuppressed, models.RecipientBounced, models.RecipientFailed:
				c.GuardianCopiesMissed += g.N
			}
			continue
		}
		switch g.Status {
		case models.RecipientSent:
			c.Sent += g.N
		case models.RecipientDelivered:
			c.Delivered += g.N
		case models.RecipientBounced:
			c.Bounced += g.N
		case models.RecipientComplained:
			c.Complained += g.N
		case models.RecipientFailed:
			c.Failed += g.N
		case models.RecipientSuppressed:
			c.Skipped += g.N
			by[g.Reason] += g.N
		default: // queued, and deferred texts
			c.Queued += g.N
		}
		if g.Teen && g.Status != models.RecipientSuppressed {
			c.Teens += g.N
		}
	}
	return c, by, err
}

// historyStatus is a row's status in the history's terms, and for an
// admin why they missed it, in words.
func historyStatus(status, reason, errText string, admin bool) (string, string) {
	why := ""
	out := historyMissed
	switch status {
	case models.RecipientSent:
		return historySent, ""
	case models.RecipientDelivered:
		return historyDelivered, ""
	case models.RecipientQueued, "deferred":
		return historyWaiting, ""
	case models.RecipientSuppressed:
		why = reason
	case models.RecipientBounced:
		why = "bounced"
		switch reason {
		case "hard bounce":
			why = "bounced: the address doesn't take email"
		case "soft bounce":
			why = "bounced for now, for example a full mailbox"
		}
	case models.RecipientComplained:
		why = skipComplained
	case models.RecipientFailed:
		why = "couldn't send"
		if errText != "" {
			why += ": " + errText
		}
	}
	if !admin {
		why = ""
	}
	return out, why
}

// historyRecipients lists the first rows by name in one query, and says
// whether there are more. Only an admin's list has guardian copies, each
// with its teen; a leader or host never sees a guardian's name (DEC-132).
func (a *App) historyRecipients(id uint, admin bool) ([]historyRecipientJSON, bool, error) {
	var rows []struct {
		Status          string
		Reason          string
		Error           string
		Copy            bool
		Name            string
		DisplayName     string
		AccountType     string
		TeenName        string
		TeenDisplayName string
	}
	err := a.DB.Raw(`SELECT r.status, r.reason, r.error, r.copy_of_user_id IS NOT NULL AS copy,
		u.name, u.display_name, u.account_type, COALESCE(t.name, '') AS teen_name, COALESCE(t.display_name, '') AS teen_display_name
		FROM message_recipients r JOIN users u ON u.id = r.user_id LEFT JOIN users t ON t.id = r.copy_of_user_id
		WHERE r.message_id = ? AND (? OR r.copy_of_user_id IS NULL)
		ORDER BY lower(COALESCE(NULLIF(u.display_name, ''), NULLIF(u.name, ''), 'Member')), r.id
		LIMIT ?`, id, admin, maxHistoryRecipients+1).Scan(&rows).Error
	more := len(rows) > maxHistoryRecipients
	if more {
		rows = rows[:maxHistoryRecipients]
	}
	out := make([]historyRecipientJSON, 0, len(rows))
	for _, r := range rows {
		u := models.User{Name: r.Name, DisplayName: r.DisplayName}
		h := historyRecipientJSON{Name: u.PublicName(), Teen: r.AccountType == models.AccountTeen && !r.Copy}
		h.Status, h.Reason = historyStatus(r.Status, r.Reason, r.Error, admin)
		if r.Copy {
			teen := models.User{Name: r.TeenName, DisplayName: r.TeenDisplayName}
			h.CopyOf = teen.PublicName()
		}
		out = append(out, h)
	}
	return out, more, err
}

// retryable: m has people to try again (failed rows on a settled send), or
// failed before anyone was chosen because it couldn't be got ready.
func retryable(m *models.Message, c recipientCountsJSON) bool {
	switch {
	case m.Status != models.MessageSent && m.Status != models.MessageFailed:
		return false
	case c.failedRows > 0:
		return true
	}
	return m.Status == models.MessageFailed && c.rows == 0 && m.Error == whyNotPrepared
}

// authorMayResend checks again, as message.send does, that m's author is
// active and may still write to its audience: a "Try again" goes out in
// the author's name, whoever presses it.
func (a *App) authorMayResend(m *models.Message) (bool, error) {
	if m.AuthorID == nil {
		return false, nil
	}
	var author models.User
	if err := a.DB.First(&author, *m.AuthorID).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if author.Status != models.UserActive {
		return false, nil
	}
	var au audienceJSON
	_ = json.Unmarshal([]byte(m.Audience), &au)
	_, err := a.resolveAudience(&author, &au, time.Now())
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, errAudienceRefused), errors.Is(err, errAudienceGone), errors.Is(err, errAudienceInvalid):
		return false, nil
	}
	return false, err
}

// apiMessageRecipients is a message's history, for its author or an admin.
func (a *App) apiMessageRecipients(c *gin.Context) {
	m, ok := a.loadMessage(c)
	if !ok {
		return
	}
	admin := policy.IsAdmin(asPerson(apiUser(c)))
	counts, by, err := a.recipientCounts(m.ID)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load who got the message")
		return
	}
	list, more, err := a.historyRecipients(m.ID, admin)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load who got the message")
		return
	}
	out := messageRecipientsJSON{Counts: counts, SkippedBy: by, Recipients: list, More: more}
	if retryable(m, counts) {
		if out.CanRetry, err = a.authorMayResend(m); err != nil {
			jsonError(c, http.StatusInternalServerError, "could not load who got the message")
			return
		}
	}
	c.JSON(http.StatusOK, out)
}

// apiRetryMessage is "Try again" (DEC-131): the people a send failed for
// are queued again and sent in fresh batches. Only failed rows go back, so
// nobody it reached gets it twice. A message that failed before anyone
// was chosen is prepared again from the start.
func (a *App) apiRetryMessage(c *gin.Context) {
	m, ok := a.loadMessage(c)
	if !ok {
		return
	}
	counts, _, err := a.recipientCounts(m.ID)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not send the message again")
		return
	}
	if !retryable(m, counts) {
		jsonError(c, http.StatusConflict, errMessageNothingToRetry)
		return
	}
	if may, err := a.authorMayResend(m); err != nil {
		jsonError(c, http.StatusInternalServerError, "could not send the message again")
		return
	} else if !may {
		jsonError(c, http.StatusForbidden, errMessageRetryRefused)
		return
	}
	me := apiUser(c)
	now := time.Now()
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		// The claim holds only if nobody else retried it meanwhile.
		claim := func(fields map[string]any) error {
			res := tx.Model(&models.Message{}).Where("id = ? AND status = ? AND updated_at = ?", m.ID, m.Status, m.UpdatedAt).Updates(fields)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				return errLocked
			}
			return nil
		}
		var ids []uint
		if counts.failedRows == 0 {
			if err := claim(map[string]any{"status": models.MessageScheduled, "scheduled_at": now, "error": "", "updated_at": now}); err != nil {
				return err
			}
			if err := enqueueMessageSend(tx, m.ID, now); err != nil {
				return err
			}
		} else {
			if err := claim(map[string]any{"status": models.MessageSending, "error": "", "updated_at": now}); err != nil {
				return err
			}
			if err := tx.Model(&models.MessageRecipient{}).Where("message_id = ? AND status = ?", m.ID, models.RecipientFailed).
				Order("id").Pluck("id", &ids).Error; err != nil {
				return err
			}
			if err := tx.Model(&models.MessageRecipient{}).Where("id IN ? AND status = ?", ids, models.RecipientFailed).
				Updates(map[string]any{"status": models.RecipientQueued, "error": "", "updated_at": now}).Error; err != nil {
				return err
			}
			if err := enqueueBatches(tx, m.ID, ids, "retry"+strconv.FormatInt(now.UnixNano(), 36)); err != nil {
				return err
			}
		}
		return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.MessageRetried, TargetType: "message", TargetID: m.ID,
			Meta: map[string]any{"people": len(ids), "byOther": m.AuthorID == nil || *m.AuthorID != me.ID}, IP: c.ClientIP()})
	})
	if errors.Is(err, errLocked) {
		jsonError(c, http.StatusConflict, errMessageNothingToRetry)
		return
	}
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not send the message again")
		return
	}
	a.respondMessage(c, http.StatusOK, m, nil)
}
