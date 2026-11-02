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

// One notice, emailed and pushed. Eight attempts on jobs.Backoff span
// about an hour, the same as the message batch, and at most about 76
// minutes with jitter. The send itself is bounded so a run stays inside
// the job lease.
const (
	notifyDeliverAttempts = 8
	deliverSendTimeout    = 60 * time.Second
	// emailKindStopped is the ledger reason when a kind no longer emails.
	emailKindStopped = "this kind of notice no longer sends email"
	// emailKindUnknown is the ledger reason when this version of Fold
	// doesn't know the notice's kind.
	emailKindUnknown = "this version of Fold doesn't know this kind of notice"
	// notifyUnknownWait and notifyUnknownGrace mirror jobs.unknownKindWait
	// and jobs.unknownKindGrace. A notice this build doesn't know waits an
	// hour without spending an attempt, and is suppressed after a week.
	notifyUnknownWait  = time.Hour
	notifyUnknownGrace = 7 * 24 * time.Hour
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
	if !ok {
		// Mirror the runner: past the grace the row is suppressed; until
		// then the job waits and this writes nothing.
		if time.Since(n.CreatedAt) > notifyUnknownGrace {
			return updateEmailDelivery(a.DB.WithContext(ctx), n.ID, models.DeliverySuppressed, emailKindUnknown, "")
		}
		return jobs.Later(time.Now().Add(notifyUnknownWait))
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
// rolls back. A transient send stores its reason, puts the row back to
// queued, and returns the error after the transaction commits. The last
// attempt marks the row deferred first, then returns the error so the job
// dies. A cancelled job context (the caller stopped, not the send's own
// timeout) stores the reason, leaves the status, and returns jobs.Later
// so a shutdown spends no attempt.
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
		if time.Since(n.CreatedAt) > notifyUnknownGrace {
			return nil, updateEmailDelivery(tx, id, models.DeliverySuppressed, emailKindUnknown, "")
		}
		return jobs.Later(time.Now().Add(notifyUnknownWait)), nil
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
	case sendErr != nil && ctx.Err() != nil:
		if err := updateEmailDelivery(tx, id, "", jobs.CleanError(sendErr), ""); err != nil {
			return nil, err
		}
		return jobs.Later(time.Now()), nil
	case jobs.LastAttempt(ctx):
		if err := updateEmailDelivery(tx, id, models.DeliveryDeferred, jobs.CleanError(sendErr), ""); err != nil {
			return nil, err
		}
		return sendErr, nil
	default:
		if err := updateEmailDelivery(tx, id, models.DeliveryQueued, jobs.CleanError(sendErr), ""); err != nil {
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
// status as it is. sent clears the reason and records the provider id,
// or NULL when the sender did not return one.
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
		if providerID == "" {
			updates["provider_message_id"] = nil
		} else {
			updates["provider_message_id"] = providerID
		}
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
