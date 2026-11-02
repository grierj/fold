package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"fold/internal/jobs"
	"fold/internal/mail"
	"fold/internal/notify"
	"fold/internal/providers"
	"fold/models"

	"gorm.io/gorm"
)

// One notice, emailed and pushed. Eight attempts ride out about two hours
// of a provider outage (the message batch's eight attempts cover about an
// hour; the same backoff, capped, runs longer here). The send itself is
// bounded so a run stays inside the job lease.
const (
	notifyDeliverAttempts = 8
	deliverSendTimeout    = 60 * time.Second
	// emailKindStopped is the ledger reason when a kind no longer emails.
	emailKindStopped = "this kind of notice no longer sends email"
)

// notifyDeliverJob is notify.deliver's payload: the notice id and nothing
// else. The key in the jobs table is notify.deliver:<id>.
type notifyDeliverJob struct {
	NotificationID uint `json:"notificationId"`
}

// notifyKinds registers notify.deliver. Nothing enqueues it yet; the
// post-commit path still delivers.
func (a *App) notifyKinds() []jobs.Kind {
	return []jobs.Kind{{
		Name: notify.DeliverKind, MaxAttempts: notifyDeliverAttempts,
		Label: "Sending a notice by email and to devices",
		Run: func(ctx context.Context, payload json.RawMessage) error {
			var p notifyDeliverJob
			if err := json.Unmarshal(payload, &p); err != nil {
				return err
			}
			return a.deliverOneNotification(ctx, p.NotificationID)
		},
	}}
}

// deliverOneNotification sends one stored notice. Push is queued first.
// The email row is then claimed with FOR UPDATE SKIP LOCKED, and that
// transaction stays open across the provider send, as deliverMessageBatch
// does. Delivery is at least once at the provider boundary: a crash after
// the provider accepts the mail and before the claim transaction commits
// sends that one email again on retry.
func (a *App) deliverOneNotification(ctx context.Context, id uint) error {
	var n models.Notification
	err := a.DB.WithContext(ctx).Where("id = ?", id).First(&n).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	k, ok := notify.Lookup(n.Kind)
	if !ok || (!k.AllowEmail && !k.AllowPush) {
		return nil
	}
	if !k.AllowEmail {
		if err := updateEmailDelivery(a.DB.WithContext(ctx), n.ID, models.DeliverySuppressed, emailKindStopped, ""); err != nil {
			return err
		}
	}
	if err := a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return queuePushFor(tx, &n)
	}); err != nil {
		return err
	}
	if !k.AllowEmail {
		return nil
	}
	return a.claimAndSendNotification(ctx, n.ID)
}

// claimAndSendNotification claims the queued or deferred email row and
// sends it. The database context outlives a cancelled caller so a send
// that already reached the provider still records its outcome. A row
// another run holds comes back as jobs.Later, which does not spend an
// attempt, including on the last one: jobs.finish applies Later before
// the dead-attempt branch when the handler itself returned.
func (a *App) claimAndSendNotification(ctx context.Context, id uint) error {
	db := a.DB.WithContext(context.WithoutCancel(ctx))
	var held bool
	var outcome error
	err := db.Transaction(func(tx *gorm.DB) error {
		var deliveryID uint
		if err := tx.Raw(`SELECT id FROM notification_deliveries
			WHERE notification_id = ? AND channel = ? AND status IN (?, ?)
			FOR UPDATE SKIP LOCKED`,
			id, models.DeliveryEmail, models.DeliveryQueued, models.DeliveryDeferred).Scan(&deliveryID).Error; err != nil {
			return err
		}
		if deliveryID == 0 {
			return nil
		}
		held = true
		var txErr error
		outcome, txErr = a.sendHeldNotification(tx, ctx, id)
		return txErr
	})
	if err != nil {
		return err
	}
	if !held {
		return a.emailDeliveryWait(db, id)
	}
	return outcome
}

// sendHeldNotification runs inside the claim transaction. The mail is
// built only from the notification row re-read after the claim. A fact
// read that fails returns the error and writes nothing, so the transaction
// rolls back. A transient send stores its reason and returns the error
// after the transaction commits; the last attempt marks the row deferred
// first, then returns the error so the job dies.
func (a *App) sendHeldNotification(tx *gorm.DB, ctx context.Context, id uint) (outcome error, err error) {
	var n models.Notification
	if err := tx.Where("id = ?", id).First(&n).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	k, ok := notify.Lookup(n.Kind)
	if !ok {
		return nil, nil
	}
	if !k.AllowEmail {
		return nil, updateEmailDelivery(tx, id, models.DeliverySuppressed, emailKindStopped, "")
	}
	u, optedOut, suppressions, footer, err := readEmailRecipient(tx, n.UserID)
	if err != nil {
		return nil, err
	}
	if !k.Required && optedOut[categoryScope(k.Category)] {
		return nil, updateEmailDelivery(tx, id, models.DeliverySuppressed, "optional email turned off", "")
	}
	if u == nil || (u.Status != models.UserActive && u.Status != models.UserGuest) {
		return nil, updateEmailDelivery(tx, id, models.DeliverySuppressed, "recipient not active", "")
	}
	msg := a.notificationEmailMessage(&n, u, k, footer)
	sendCtx, cancel := context.WithTimeout(ctx, deliverSendTimeout)
	defer cancel()
	providerID, sendErr := a.dispatchNotificationEmail(sendCtx, msg, suppressions)
	switch {
	case sendErr == nil:
		return nil, updateEmailDelivery(tx, id, models.DeliverySent, "", providerID)
	case mail.IsSuppressed(sendErr):
		return nil, updateEmailDelivery(tx, id, models.DeliverySuppressed, strings.TrimPrefix(sendErr.Error(), "mail: "), "")
	case mail.IsPermanent(sendErr), mail.NotConfigured(sendErr):
		return nil, updateEmailDelivery(tx, id, models.DeliveryFailed, jobs.CleanError(sendErr), "")
	case jobs.LastAttempt(ctx):
		if err := updateEmailDelivery(tx, id, models.DeliveryDeferred, jobs.CleanError(sendErr), ""); err != nil {
			return nil, err
		}
		return sendErr, nil
	default:
		if err := updateEmailDelivery(tx, id, "", jobs.CleanError(sendErr), ""); err != nil {
			return nil, err
		}
		return sendErr, nil
	}
}

// emailDeliveryWait runs when the claim took no row. No row, or a terminal
// one, means the notice was already settled: return nil and send nothing.
// queued or deferred means another run holds the lock.
func (a *App) emailDeliveryWait(db *gorm.DB, notificationID uint) error {
	var status string
	err := db.Raw(`SELECT status FROM notification_deliveries WHERE notification_id = ? AND channel = ?`,
		notificationID, models.DeliveryEmail).Row().Scan(&status)
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	switch status {
	case models.DeliveryQueued, models.DeliveryDeferred:
		return jobs.Later(time.Now().Add(time.Minute))
	default:
		return nil
	}
}

// updateEmailDelivery writes the ledger while the email row is still
// queued or deferred, and stamps updated_at. An empty status leaves the
// status as it is. sent clears the reason and records the provider id.
func updateEmailDelivery(tx *gorm.DB, notificationID uint, status, reason, providerID string) error {
	updates := map[string]any{
		"reason":     reason,
		"updated_at": time.Now().UTC(),
	}
	if status != "" {
		updates["status"] = status
	}
	if status == models.DeliverySent {
		updates["sent_at"] = time.Now().UTC()
		updates["reason"] = ""
		updates["provider_message_id"] = providerID
	}
	return tx.Model(&models.NotificationDelivery{}).
		Where("notification_id = ? AND channel = ? AND status IN ?", notificationID, models.DeliveryEmail,
			[]string{models.DeliveryQueued, models.DeliveryDeferred}).
		Updates(updates).Error
}

// dispatchNotificationEmail applies the suppression list already read with
// the recipient, then sends. A nil mailer is the unconfigured sender: the
// caller records that as failed and does not retry.
func (a *App) dispatchNotificationEmail(ctx context.Context, msg mail.Message, suppressions []string) (string, error) {
	if err := mail.Blocked(suppressions, msg); err != nil {
		return "", err
	}
	if a.Mailer == nil {
		return mail.NoSender{}.SendEmail(ctx, msg)
	}
	sender := mail.Unwrap(a.Mailer)
	if tracked, ok := sender.(providers.EmailSender); ok {
		return tracked.SendEmail(ctx, msg)
	}
	return "", sender.Send(ctx, msg)
}
