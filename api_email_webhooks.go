package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"fold/internal/jobs"
	"fold/internal/mail"
	"fold/internal/providers"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Email delivery reports (M3.2b, PLAN 5.6, 7.1). The provider posts each
// delivery, bounce and spam complaint to /hooks/postmark/email. The request
// is checked, its event queued as a webhook.process job (deduplicated by
// the report's own id), and answered at once; the job updates the delivery
// record and suppresses an address that can't take mail (DEC-112).

// webhookUser is the user name in the webhook URL Postmark is given:
// https://postmark:<FOLD_POSTMARK_WEBHOOK_SECRET>@<site>/hooks/postmark/email
const webhookUser = "postmark"

// maxWebhookBody bounds one report; Postmark's are a few kilobytes, more
// with a bounce dump attached.
const maxWebhookBody = 1 << 20

const kindEmailEvent = "webhook.process"

// postmarkEmailHook receives one Postmark webhook. Postmark doesn't sign
// webhooks; it sends the HTTP Basic credentials from the webhook URL, so a
// wrong or missing one is a 401 that changes nothing. With no secret set the
// endpoint doesn't exist (404).
func (a *App) postmarkEmailHook(c *gin.Context) {
	secret := a.Config.PostmarkWebhookSecret
	if secret == "" {
		c.Status(http.StatusNotFound)
		return
	}
	user, pass, ok := c.Request.BasicAuth()
	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(webhookUser)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(pass), []byte(secret)) == 1
	if !ok || !userOK || !passOK {
		c.Header("WWW-Authenticate", `Basic realm="fold"`)
		c.Status(http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxWebhookBody+1))
	if err != nil || len(body) > maxWebhookBody {
		c.Status(http.StatusRequestEntityTooLarge)
		return
	}
	evs, err := mail.ParsePostmarkWebhook(c.Request.Header, body)
	if err != nil {
		// A malformed report won't get better if Postmark sends it again.
		log.Printf("email webhook: %v", err)
		c.Status(http.StatusBadRequest)
		return
	}
	for _, e := range evs {
		if _, err := jobs.Enqueue(a.DB, kindEmailEvent, e, time.Now(), e.Key, 8); err != nil {
			// Postmark retries a failed delivery, so nothing is lost.
			log.Printf("email webhook: queue: %v", err)
			c.Status(http.StatusInternalServerError)
			return
		}
	}
	c.Status(http.StatusOK)
}

// eventKinds are the jobs queued by outside events rather than the clock.
func (a *App) eventKinds() []jobs.Kind {
	return []jobs.Kind{{Name: kindEmailEvent, Label: "Recording an email delivery report", MaxAttempts: 8,
		Run: func(ctx context.Context, payload json.RawMessage) error {
			var e providers.Event
			if err := json.Unmarshal(payload, &e); err != nil {
				return err
			}
			return a.applyEmailEvent(ctx, e)
		}}}
}

// errDeliveryNotYet: a report came in before the send that it is about
// finished recording its delivery row; the job retries shortly.
var errDeliveryNotYet = errors.New("the delivery this report is about isn't recorded yet")

// reportGrace is how long a report for a message with no delivery row is
// retried before it is taken to be about mail Fold didn't send.
const reportGrace = 2 * time.Minute

// applyEmailEvent records one report. It is safe to run twice: each update
// only moves a delivery forward, and a suppression is added once.
func (a *App) applyEmailEvent(ctx context.Context, e providers.Event) error {
	return a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// The report is about a notification's email or an announcement's
		// (M3.5); each table moves its row forward the same way.
		deliveries := tx.Model(&models.NotificationDelivery{}).Where("channel = ? AND provider_message_id = ?", models.DeliveryEmail, e.MessageID)
		recipients := tx.Model(&models.MessageRecipient{}).Where("channel = ? AND provider_message_id = ?", models.DeliveryEmail, e.MessageID)
		var res, mres *gorm.DB
		switch e.Kind {
		case providers.EventDelivered:
			res = deliveries.Where("status = ?", models.DeliverySent).Update("status", models.DeliveryDelivered)
			mres = recipients.Where("status = ?", models.RecipientSent).Updates(map[string]any{"status": models.RecipientDelivered, "updated_at": time.Now()})
		case providers.EventBounced, providers.EventComplained:
			reason := "soft bounce"
			status := models.RecipientBounced
			switch {
			case e.Kind == providers.EventComplained:
				reason, status = "marked as spam", models.RecipientComplained
			case e.Permanent:
				reason = "hard bounce"
			}
			res = deliveries.Where("status IN ?", []string{models.DeliverySent, models.DeliveryDelivered}).
				Updates(map[string]any{"status": models.DeliveryBounced, "reason": reason})
			mres = recipients.Where("status IN ?", []string{models.RecipientSent, models.RecipientDelivered}).
				Updates(map[string]any{"status": status, "reason": reason, "updated_at": time.Now()})
		default:
			return nil
		}
		if res.Error != nil {
			return res.Error
		}
		if mres.Error != nil {
			return mres.Error
		}
		if res.RowsAffected+mres.RowsAffected == 0 && recent(e.At) {
			var n, mn int64
			if err := tx.Model(&models.NotificationDelivery{}).Where("channel = ? AND provider_message_id = ?", models.DeliveryEmail, e.MessageID).
				Count(&n).Error; err != nil {
				return err
			}
			if err := tx.Model(&models.MessageRecipient{}).Where("channel = ? AND provider_message_id = ?", models.DeliveryEmail, e.MessageID).
				Count(&mn).Error; err != nil {
				return err
			}
			if n+mn == 0 {
				return errDeliveryNotYet
			}
		}
		// A soft bounce or a delivery is only recorded (DEC-112).
		if !e.Permanent || e.Kind == providers.EventDelivered {
			return nil
		}
		// The report is authenticated and says the address can't take mail,
		// so it is suppressed even when the message wasn't a notification
		// (account mail keeps no delivery row).
		why := models.SuppressHardBounce
		if e.Kind == providers.EventComplained {
			why = models.SuppressComplaint
		}
		return tx.Exec(`INSERT INTO suppressions (channel, address, reason, provider, detail) VALUES ('email', ?, ?, ?, ?)
			ON CONFLICT (channel, address, reason) WHERE cleared_at IS NULL DO NOTHING`,
			e.Address, why, e.Provider, e.Detail).Error
	})
}

// recent: at is within reportGrace of now (either way, for clock skew).
func recent(at time.Time) bool {
	if at.IsZero() {
		return false
	}
	d := time.Since(at)
	return d > -reportGrace && d < reportGrace
}
