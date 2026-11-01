package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
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
// failed); GuardianCopiesFailed are the failed ones, which Try again sends.
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
	GuardianCopiesFailed int `json:"guardianCopiesFailed"`
	// Push counts the companion pushes (M3.13d2), apart from the email:
	// they never change the counts above.
	Push pushCountsJSON `json:"push"`
	// Every email row and every failed one, copies included: what "Try
	// again" works from. A push is never sent again.
	rows, failedRows int
}

// pushCountsJSON counts an announcement's push rows (one per person
// however many devices): sent, still waiting, and the rest by reason
// (skipped or failed): "38 pushed · 2 waiting until 8:00 AM · 1 device
// gone". Waiting is by when quiet hours end on each person's clock ("" for
// pushes about to go).
type pushCountsJSON struct {
	Sent    int            `json:"sent"`
	Waiting []pushWaitJSON `json:"waiting"`
	Missed  map[string]int `json:"missed"`
}

type pushWaitJSON struct {
	// Until is "8:00 AM" on the recipient's clock, or "" for pushes about
	// to go out.
	Until string `json:"until"`
	N     int    `json:"n"`
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
	// Channel is email, or push for a companion push (M3.13d2), listed
	// after the same person's email.
	Channel string `json:"channel"`
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
	errMessageRetrySending   = "it's already sending again"
)

// recipientCounts counts m's rows by status, and the skipped by reason,
// in one query.
func (a *App) recipientCounts(id uint) (recipientCountsJSON, map[string]int, error) {
	var groups []struct {
		Push   bool
		Status string
		Reason string
		Teen   bool
		Copy   bool
		N      int
	}
	// A push row's reason is kept whatever its status (why it missed, or
	// when it waits until); an email row's only when it was skipped.
	err := a.DB.Raw(`SELECT r.channel = ? AS push, r.status,
		CASE WHEN r.channel = ? OR (r.status = ? AND r.copy_of_user_id IS NULL) THEN r.reason ELSE '' END AS reason,
		(u.account_type = ? AND r.copy_of_user_id IS NULL) AS teen, r.copy_of_user_id IS NOT NULL AS copy, count(*) AS n
		FROM message_recipients r JOIN users u ON u.id = r.user_id
		WHERE r.message_id = ? GROUP BY 1, 2, 3, 4, 5`, models.DeliveryPush, models.DeliveryPush, models.RecipientSuppressed, models.AccountTeen, id).Scan(&groups).Error
	c := recipientCountsJSON{Push: pushCountsJSON{Waiting: []pushWaitJSON{}, Missed: map[string]int{}}}
	by := map[string]int{}
	waiting := map[string]int{}
	for _, g := range groups {
		if g.Push {
			switch g.Status {
			case models.RecipientSent:
				c.Push.Sent += g.N
			case models.RecipientQueued:
				waiting[strings.TrimPrefix(g.Reason, pushWaitPrefix)] += g.N
			default:
				c.Push.Missed[g.Reason] += g.N
			}
			continue
		}
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
			if g.Status == models.RecipientFailed {
				c.GuardianCopiesFailed += g.N
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
	for until, n := range waiting {
		c.Push.Waiting = append(c.Push.Waiting, pushWaitJSON{Until: until, N: n})
	}
	sort.Slice(c.Push.Waiting, func(i, j int) bool { return c.Push.Waiting[i].Until < c.Push.Waiting[j].Until })
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

// pushHistoryStatus is a push row's status in the history's terms: sent,
// waiting (with when, for an admin), or missed with its reason (admin).
func pushHistoryStatus(status, reason string, admin bool) (string, string) {
	out := historyMissed
	switch status {
	case models.RecipientSent:
		return historySent, ""
	case models.RecipientQueued:
		out = historyWaiting
	}
	if !admin {
		reason = ""
	}
	return out, reason
}

// historyRecipients lists the first rows by name in one query, and says
// whether there are more. Only an admin's list has guardian copies, each
// with its teen; a leader or host never sees a guardian's name (DEC-132).
func (a *App) historyRecipients(id uint, admin bool) ([]historyRecipientJSON, bool, error) {
	var rows []struct {
		Channel         string
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
	// By name, each person's email before their push (rows are
	// snapshotted email first, so r.id orders them).
	err := a.DB.Raw(`SELECT r.channel, r.status, r.reason, r.error, r.copy_of_user_id IS NOT NULL AS copy,
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
		h := historyRecipientJSON{Name: u.PublicName(), Teen: r.AccountType == models.AccountTeen && !r.Copy, Channel: r.Channel}
		if r.Channel == models.DeliveryPush {
			h.Status, h.Reason = pushHistoryStatus(r.Status, r.Reason, admin)
		} else {
			h.Status, h.Reason = historyStatus(r.Status, r.Reason, r.Error, admin)
		}
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
	return m.Status == models.MessageFailed && c.rows == 0 && neverPrepared(m.Error)
}

// goingOut: m is sending, so a Try again has already been pressed (or its
// first send is still going). A scheduled message, due or not, has nothing
// to retry: it was never sent, or a retry of one that never got ready is
// about to prepare it.
func goingOut(m *models.Message) bool { return m.Status == models.MessageSending }

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

// reminderMayResend: an automatic reminder has no author, so a "Try again"
// needs only what reminder.send needs: its date still scheduled and not
// yet started, and its event's reminders still on (who pressed it was
// checked by loadMessage: an admin or a manager of the event). Each person
// is checked again by recheckReminderRows.
func (a *App) reminderMayResend(m *models.Message) (bool, error) {
	if m.OccurrenceID == nil {
		return false, nil
	}
	// An audience that no longer reads as one (a gone audience, as for an
	// announcement) can't say who is still going: nothing goes again.
	var au audienceJSON
	if json.Unmarshal([]byte(m.Audience), &au) != nil || au.normalize() != nil {
		return false, nil
	}
	var occ models.EventOccurrence
	if err := a.DB.First(&occ, *m.OccurrenceID).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	var ev models.Event
	if err := a.DB.Select("id, reminder_email_minutes").First(&ev, occ.EventID).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return ev.ReminderEmailMinutes > 0 && occ.Status == models.OccurrenceScheduled && occ.StartsAt.After(time.Now()), nil
}

// Why a reminder's failed row isn't sent again: its person's answer
// changed since (they cancelled, declined, or moved off or onto the
// waitlist).
const (
	skipNoLongerGoing    = "no longer going"
	skipNoLongerWaitlist = "no longer on the waitlist"
)

// recheckReminderRows checks a reminder's failed rows again before a Try
// again, as message.send does: someone no longer going (or no longer on
// the waitlist, for the waitlist note), or who can no longer see the date
// (M1.13), is skipped with that reason, not sent to. A guardian copy
// follows its teen. It answers the row ids still to send.
func (a *App) recheckReminderRows(tx *gorm.DB, m *models.Message, ids []uint, now time.Time) ([]uint, error) {
	if len(ids) == 0 || m.OccurrenceID == nil {
		return ids, nil
	}
	var rows []models.MessageRecipient
	if err := tx.Select("id, user_id, copy_of_user_id").Where("id IN ?", ids).Order("id").Find(&rows).Error; err != nil {
		return nil, err
	}
	subject := func(r models.MessageRecipient) uint {
		if r.CopyOfUserID != nil {
			return *r.CopyOfUserID
		}
		return r.UserID
	}
	// Who the reminder's audience reaches now (the date's registrants with
	// its status, a household person's through its account holder).
	var au audienceJSON
	_ = json.Unmarshal([]byte(m.Audience), &au)
	current := map[uint]bool{}
	if err := au.normalize(); err != nil {
		// reminderMayResend refuses this first; should it slip through,
		// nobody is sent to, under the neutral reason.
		if err := tx.Model(&models.MessageRecipient{}).Where("id IN ? AND status = ?", ids, models.RecipientFailed).
			Updates(map[string]any{"status": models.RecipientSuppressed, "reason": skipCantSeeDate, "error": "", "updated_at": now}).Error; err != nil {
			return nil, err
		}
		return []uint{}, nil
	}
	still, err := a.audienceUserIDs(tx, au, now)
	if err != nil {
		return nil, err
	}
	for _, id := range still {
		current[id] = true
	}
	people := make([]uint, 0, len(rows))
	for _, r := range rows {
		people = append(people, subject(r))
	}
	can, err := a.reminderReaders(tx, *m.OccurrenceID, dedupeIDs(people), now)
	if err != nil {
		return nil, err
	}
	answer := skipNoLongerGoing
	if reminderKind(m) == reminderWaitlist {
		answer = skipNoLongerWaitlist
	}
	keep := []uint{}
	drop := map[string][]uint{}
	for _, r := range rows {
		switch {
		case !current[subject(r)]:
			drop[answer] = append(drop[answer], r.ID)
		case !can[subject(r)]:
			drop[skipCantSeeDate] = append(drop[skipCantSeeDate], r.ID)
		default:
			keep = append(keep, r.ID)
		}
	}
	for why, list := range drop {
		if err := tx.Model(&models.MessageRecipient{}).Where("id IN ? AND status = ?", list, models.RecipientFailed).
			Updates(map[string]any{"status": models.RecipientSuppressed, "reason": why, "error": "", "updated_at": now}).Error; err != nil {
			return nil, err
		}
	}
	return keep, nil
}

// mayResend is whether m can go out again, and if not, why: an
// announcement's author must still be able to write to its people; a
// reminder's date must still be ahead.
func (a *App) mayResend(m *models.Message) (bool, string, error) {
	if m.Category == models.MessageCategoryReminder {
		ok, err := a.reminderMayResend(m)
		return ok, errReminderRetryRefused, err
	}
	ok, err := a.authorMayResend(m)
	return ok, errMessageRetryRefused, err
}

// apiMessageRecipients is a message's history, for whoever may read it
// (loadMessage): its author, an admin, or a reminder's event managers.
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
		if out.CanRetry, _, err = a.mayResend(m); err != nil {
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
	if goingOut(m) {
		jsonError(c, http.StatusConflict, errMessageRetrySending)
		return
	}
	if !retryable(m, counts) {
		jsonError(c, http.StatusConflict, errMessageNothingToRetry)
		return
	}
	if may, why, err := a.mayResend(m); err != nil {
		jsonError(c, http.StatusInternalServerError, "could not send the message again")
		return
	} else if !may {
		jsonError(c, http.StatusForbidden, why)
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
			// Email only: a push is never sent again (M3.13d2).
			if err := tx.Model(&models.MessageRecipient{}).Where("message_id = ? AND status = ? AND channel = ?", m.ID, models.RecipientFailed, models.DeliveryEmail).
				Order("id").Pluck("id", &ids).Error; err != nil {
				return err
			}
			if m.Category == models.MessageCategoryReminder {
				var err error
				if ids, err = a.recheckReminderRows(tx, m, ids, now); err != nil {
					return err
				}
			}
			if err := tx.Model(&models.MessageRecipient{}).Where("id IN ? AND status = ?", ids, models.RecipientFailed).
				Updates(map[string]any{"status": models.RecipientQueued, "error": "", "updated_at": now}).Error; err != nil {
				return err
			}
			if len(ids) == 0 {
				// Nobody left to send to: it settles as it now stands.
				if err := a.settleMessage(tx, m.ID); err != nil {
					return err
				}
			} else if err := enqueueBatches(tx, m.ID, ids, "retry"+strconv.FormatInt(now.UnixNano(), 36)); err != nil {
				return err
			}
		}
		return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.MessageRetried, TargetType: "message", TargetID: m.ID,
			Meta: map[string]any{"people": len(ids), "byOther": m.AuthorID == nil || *m.AuthorID != me.ID}, IP: c.ClientIP()})
	})
	if errors.Is(err, errLocked) {
		// Someone else pressed it first.
		if a.DB.First(m, m.ID).Error == nil && goingOut(m) {
			jsonError(c, http.StatusConflict, errMessageRetrySending)
		} else {
			jsonError(c, http.StatusConflict, errMessageNothingToRetry)
		}
		return
	}
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not send the message again")
		return
	}
	a.respondMessage(c, http.StatusOK, m, nil)
}
