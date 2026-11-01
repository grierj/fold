package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"fold/internal/jobs"
	"fold/internal/notify"
	"fold/internal/push"
	"fold/models"

	"gorm.io/gorm"
)

// Web push delivery (M3.13a, research 2.2, PLAN 5.12). One push.deliver job
// per device, so a retry only ever repeats that device and can never send
// twice to another one that already took it. The payload holds ids, never
// an endpoint. One aggregate notification_deliveries row per notification
// records the push channel, whatever the device count.

const (
	kindPushDeliver     = "push.deliver"
	pushDeliverAttempts = jobs.DefaultMaxAttempts
	// pushTTL is how long a push service keeps an undelivered message.
	// Slice b sets TTL and urgency per kind; the bell and email are the
	// record, so an expired push is harmless.
	pushTTL = 24 * time.Hour
	// pushDeviceFailedFor is how long a failing device is kept before the
	// daily cleanup removes it.
	pushDeviceFailedFor = 30 * 24 * time.Hour
)

// pushJob is push.deliver's payload. NotificationID is set for a
// notification; MessageRecipientID is reserved for announcements and
// reminders (M3.13d, key push:mr:<id>:d:<device>).
type pushJob struct {
	NotificationID     uint `json:"notificationId,omitempty"`
	MessageRecipientID uint `json:"messageRecipientId,omitempty"`
	DeviceID           uint `json:"deviceId"`
}

// enqueuePushDelivery queues one notification for one device, once. Slice
// b calls it after commit for AllowPush kinds; until then only tests do
// (its .deadcode-allow line goes with that caller).
func enqueuePushDelivery(tx *gorm.DB, notificationID, deviceID uint, at time.Time) error {
	key := "push:n:" + strconv.FormatUint(uint64(notificationID), 10) + ":d:" + strconv.FormatUint(uint64(deviceID), 10)
	_, err := jobs.Enqueue(tx, kindPushDeliver, pushJob{NotificationID: notificationID, DeviceID: deviceID}, at, key, pushDeliverAttempts)
	return err
}

func (a *App) pushKinds() []jobs.Kind {
	return []jobs.Kind{{Name: kindPushDeliver, Label: "Sending a notification to a device", MaxAttempts: pushDeliverAttempts,
		Run: func(ctx context.Context, payload json.RawMessage) error {
			var p pushJob
			if err := json.Unmarshal(payload, &p); err != nil {
				return err
			}
			return a.deliverPush(ctx, p)
		}}}
}

// renderPush is a notification's lock-screen text (DEC-124). Slice b gives
// each kind that may push (Kind.AllowPush) its own short PushRender, never
// the bell body cut short; until then no kind pushes, so it reports false
// for every row and push.deliver sends nothing.
func renderPush(*models.Notification) (push.Notification, bool) {
	return push.Notification{}, false
}

// Delivery reasons a push row can carry (notification_deliveries.reason).
const (
	pushWhyChurchOff  = "push turned off by the church"
	pushWhyInactive   = "recipient not active"
	pushWhyNotOn      = "push not turned on"
	pushWhyGone       = "device gone"
	pushWhyTooLarge   = "the notification was too large to push"
	pushWhyRefused    = "the push service refused it"
	pushWhyNotReached = "the push service couldn't be reached"
)

// deliverPush sends one notification to one device. It returns an error
// only when a retry may succeed (429, 5xx, a timeout).
func (a *App) deliverPush(ctx context.Context, p pushJob) error {
	if p.NotificationID == 0 || p.DeviceID == 0 {
		return nil // announcements arrive in M3.13d; nothing else is valid
	}
	var dev models.PushSubscription
	if err := a.DB.WithContext(ctx).First(&dev, p.DeviceID).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return nil // turned off, gone, or rotated away since it was queued
	} else if err != nil {
		return err
	}
	var n models.Notification
	if err := a.DB.WithContext(ctx).First(&n, p.NotificationID).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	if n.UserID != dev.UserID {
		return nil // the device changed hands since this was queued
	}
	kind, ok := notify.Lookup(n.Kind)
	if !ok {
		return nil
	}
	if a.settingValue(settingPushEnabled) == "false" {
		return a.recordPush(n, models.DeliverySuppressed, pushWhyChurchOff)
	}
	var u models.User
	if err := a.DB.WithContext(ctx).Select("id", "status").First(&u, n.UserID).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	if u.Status != models.UserActive {
		return a.recordPush(n, models.DeliverySuppressed, pushWhyInactive)
	}
	on, categoryOff, err := pushConsent(a.DB.WithContext(ctx), u.ID, kind.Category)
	if err != nil {
		return err
	}
	switch {
	case !on:
		return a.recordPush(n, models.DeliverySuppressed, pushWhyNotOn)
	case categoryOff && !kind.Required:
		return a.recordPush(n, models.DeliverySuppressed, "push turned off for "+notify.CategoryEmailName(kind.Category))
	}
	render := renderPush
	if a.pushRender != nil {
		render = a.pushRender
	}
	note, ok := render(&n)
	if !ok {
		return nil
	}
	body, err := a.pushBody(&n, note)
	if err != nil {
		log.Printf("push: notification %d (%s) is too large to push: a bug in its push text", n.ID, n.Kind)
		return a.recordPush(n, models.DeliveryFailed, pushWhyTooLarge)
	}
	sub := push.Subscription{Endpoint: dev.Endpoint, P256dh: dev.P256dh, Auth: dev.Auth}
	res := a.pushClient().Send(ctx, sub, body, push.Options{TTL: pushTTL, Urgency: push.UrgencyNormal, Topic: "n" + strconv.FormatUint(uint64(n.ID), 10)})
	return a.pushOutcome(ctx, n, dev, res)
}

// pushBody is the Declarative Web Push JSON for n: the rendered text, a
// tap that opens the notification's own link on SITE_URL, and a tag so a
// second copy replaces the first on the device. It is refused past the
// 3,000-byte cap.
func (a *App) pushBody(n *models.Notification, note push.Notification) ([]byte, error) {
	note.Navigate = a.emailLink(n.Link)
	if note.Tag == "" {
		note.Tag = "n" + strconv.FormatUint(uint64(n.ID), 10)
	}
	return push.MarshalPayload(push.NewPayload(note))
}

// pushOutcome records what the push service said about one device.
func (a *App) pushOutcome(ctx context.Context, n models.Notification, dev models.PushSubscription, res push.Result) error {
	switch res.Status {
	case push.Accepted:
		// It went: nothing below may turn this into a retry, which would
		// send it again.
		if err := a.DB.Exec(`UPDATE push_subscriptions SET last_used_at = now(), failed_at = NULL
			WHERE id = ? AND (last_used_at < now() - interval '1 day' OR failed_at IS NOT NULL)`, dev.ID).Error; err != nil {
			log.Printf("push: device %d: marking it used: %v", dev.ID, err)
		}
		if err := a.recordPush(n, models.DeliverySent, ""); err != nil {
			log.Printf("push: notification %d: recording the delivery: %v", n.ID, err)
		}
		return nil
	case push.Gone:
		log.Printf("push: device gone for user %d (device %d, %d)", dev.UserID, dev.ID, res.Code)
		return a.DB.Transaction(func(tx *gorm.DB) error {
			if err := tx.Delete(&models.PushSubscription{}, dev.ID).Error; err != nil {
				return err
			}
			var left int64
			if err := tx.Model(&models.PushSubscription{}).Where("user_id = ?", dev.UserID).Count(&left).Error; err != nil {
				return err
			}
			if left > 0 {
				return nil // another device may still take it
			}
			return recordPushIn(tx, n, models.DeliverySuppressed, pushWhyGone)
		})
	case push.TooLarge:
		log.Printf("push: notification %d (%s) was too large for the push service (%d): a bug in its push text", n.ID, n.Kind, res.Code)
		return a.recordPush(n, models.DeliveryFailed, pushWhyTooLarge)
	case push.RateLimited, push.Temporary:
		if !jobs.LastAttempt(ctx) {
			return fmt.Errorf("push: device %d: %s (retrying): %w", dev.ID, res.Status, res.Err)
		}
		a.markPushFailed(dev.ID)
		if err := a.recordPush(n, models.DeliveryFailed, pushWhyNotReached); err != nil {
			return err
		}
		return fmt.Errorf("push: device %d: %s: %w", dev.ID, res.Status, res.Err)
	default:
		log.Printf("push: notification %d to device %d: %s (%d)", n.ID, dev.ID, res.Status, res.Code)
		a.markPushFailed(dev.ID)
		return a.recordPush(n, models.DeliveryFailed, pushWhyRefused)
	}
}

// markPushFailed stamps a device's first failure; the daily cleanup
// removes it 30 days later unless a send succeeds first.
func (a *App) markPushFailed(id uint) {
	if err := a.DB.Exec("UPDATE push_subscriptions SET failed_at = now() WHERE id = ? AND failed_at IS NULL", id).Error; err != nil {
		log.Printf("push: device %d: marking it failed: %v", id, err)
	}
}

// pushConsent reads the person's latest push consent (DEC-122): on is the
// (push, all) row opting in; categoryOff is category's row opting out.
func pushConsent(db *gorm.DB, userID uint, category string) (on, categoryOff bool, err error) {
	var rows []models.Consent
	if err := db.Raw(`SELECT DISTINCT ON (scope) scope, status FROM consents
		WHERE user_id = ? AND channel = ? AND scope IN ('all', ?) ORDER BY scope, at DESC, id DESC`,
		userID, models.ConsentPush, categoryScope(category)).Scan(&rows).Error; err != nil {
		return false, false, err
	}
	for _, r := range rows {
		switch r.Scope {
		case "all":
			on = r.Status == models.ConsentOptedIn
		default:
			categoryOff = r.Status == models.ConsentOptedOut
		}
	}
	return on, categoryOff, nil
}

func (a *App) recordPush(n models.Notification, status, reason string) error {
	return recordPushIn(a.DB, n, status, reason)
}

// recordPushIn writes the notification's one push delivery row. A sent
// row is final; any other first outcome stays unless a later device takes
// it, which upgrades the row to sent.
func recordPushIn(db *gorm.DB, n models.Notification, status, reason string) error {
	var sentAt *time.Time
	if status == models.DeliverySent {
		now := time.Now().UTC()
		sentAt = &now
	}
	return db.Exec(`INSERT INTO notification_deliveries (notification_id, user_id, channel, status, reason, sent_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (notification_id, channel) DO UPDATE SET status = excluded.status, reason = excluded.reason, sent_at = excluded.sent_at
		WHERE excluded.status = 'sent' AND notification_deliveries.status <> 'sent'`,
		n.ID, n.UserID, models.DeliveryPush, status, reason, sentAt).Error
}

// prunePushDevices removes devices that have been failing for 30 days or
// whose subscription has expired (part of the daily notifications.cleanup).
func prunePushDevices(db *gorm.DB, now time.Time) (int64, error) {
	res := db.Exec("DELETE FROM push_subscriptions WHERE failed_at < ? OR expires_at <= ?", now.Add(-pushDeviceFailedFor), now)
	return res.RowsAffected, res.Error
}
