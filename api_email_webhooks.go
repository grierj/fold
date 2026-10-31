package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
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

// applyEmailEvent records one report. It is safe to run twice: each update
// only moves a delivery forward, and a suppression is added once.
func (a *App) applyEmailEvent(ctx context.Context, e providers.Event) error {
	return a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		deliveries := tx.Model(&models.NotificationDelivery{}).Where("channel = ? AND provider_message_id = ?", models.DeliveryEmail, e.MessageID)
		switch e.Kind {
		case providers.EventDelivered:
			return deliveries.Where("status = ?", models.DeliverySent).Update("status", models.DeliveryDelivered).Error
		case providers.EventBounced, providers.EventComplained:
			reason := "soft bounce"
			switch {
			case e.Kind == providers.EventComplained:
				reason = "marked as spam"
			case e.Permanent:
				reason = "hard bounce"
			}
			if err := deliveries.Where("status IN ?", []string{models.DeliverySent, models.DeliveryDelivered}).
				Updates(map[string]any{"status": models.DeliveryBounced, "reason": reason}).Error; err != nil {
				return err
			}
			// A soft bounce is only recorded (DEC-112).
			if !e.Permanent {
				return nil
			}
			why := models.SuppressHardBounce
			if e.Kind == providers.EventComplained {
				why = models.SuppressComplaint
			}
			return tx.Exec(`INSERT INTO suppressions (channel, address, reason, provider, detail) VALUES ('email', ?, ?, ?, ?)
				ON CONFLICT (channel, address, reason) WHERE cleared_at IS NULL DO NOTHING`,
				e.Address, why, e.Provider, e.Detail).Error
		}
		return nil
	})
}
