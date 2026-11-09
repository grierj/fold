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

// One notice, emailed and pushed (notify.DeliverJob). The send itself is
// bounded so a run stays inside the job lease.
const (
	deliverSendTimeout   = 60 * time.Second
	kindNotifyReconcile  = "notify.reconcile"
	notifyReconcileBatch = 500
	// emailKindStopped is the ledger reason when a kind no longer emails.
	emailKindStopped = "this kind of notice no longer sends email"
	// emailKindUnknown is the ledger reason when this version of Fold
	// doesn't know the notice's kind.
	emailKindUnknown = "this version of Fold doesn't know this kind of notice"
	emailDismissed   = "an admin set this notice aside"
	// notifyUnknownWait and notifyUnknownGrace mirror jobs.unknownKindWait
	// and jobs.unknownKindGrace. A notice this build doesn't know waits an
	// hour without spending an attempt, and is suppressed after a week.
	notifyUnknownWait  = time.Hour
	notifyUnknownGrace = 7 * 24 * time.Hour
)

// notifyKinds registers the delivery jobs queued by notify.Send.
func (a *App) notifyKinds() []jobs.Kind {
	return []jobs.Kind{{
		Type: notify.DeliverJob, Label: "Sending a notice by email and to devices",
		// Dismissing a dead delivery settles the notice's email row.
		OnDismiss: func(tx *gorm.DB, payload json.RawMessage) error {
			var p notify.DeliverPayload
			if json.Unmarshal(payload, &p) != nil || p.NotificationID == 0 {
				return nil
			}
			return updateEmailDelivery(tx, p.NotificationID, models.DeliverySuppressed, emailDismissed, "")
		},
		Run: func(ctx context.Context, payload json.RawMessage) error {
			var p notify.DeliverPayload
			if err := json.Unmarshal(payload, &p); err != nil {
				return err
			}
			return a.deliverOneNotification(ctx, p.NotificationID)
		},
	}}
}

// reconcileNotifications repairs lost delivery jobs and backfills pre-outbox
// notices from the last 24 hours. Older notices stay bell-only. Both passes
// advance by the last candidate, even when every write collides with an
// existing key or a kind has no delivery channels.
func (a *App) reconcileNotifications(ctx context.Context, now time.Time) error {
	db := a.DB.WithContext(ctx)
	var afterAt time.Time
	var afterID uint
	// A key in any status means the job path owns the row; a dead key goes
	// to the admin alert, and dismissing it settles the ledger.
	for {
		var rows []models.NotificationDelivery
		if err := db.Raw(`SELECT nd.notification_id, nd.user_id, nd.created_at, nd.id
			FROM notification_deliveries nd
			WHERE nd.channel = ? AND nd.status = ? AND nd.created_at < ?
			  AND (nd.created_at, nd.id) > (?, ?)
			  AND NOT EXISTS (SELECT 1 FROM jobs j
				WHERE j.unique_key = ? || nd.notification_id)
			ORDER BY nd.created_at, nd.id LIMIT ?`,
			models.DeliveryEmail, models.DeliveryQueued, now.Add(-10*time.Minute), afterAt, afterID,
			notify.DeliverKeyPrefix, notifyReconcileBatch).
			Scan(&rows).Error; err != nil {
			return err
		}
		if len(rows) > 0 {
			if err := db.Transaction(func(tx *gorm.DB) error { return requeueLostDeliveries(tx, rows, now) }); err != nil {
				return err
			}
		}
		if len(rows) < notifyReconcileBatch {
			break
		}
		last := rows[len(rows)-1]
		afterAt, afterID = last.CreatedAt, last.ID
	}
	afterAt, afterID = time.Time{}, 0
	// No notifications created_at index by choice: at church scale, <=180 days kept makes the hourly scan small; R11-0's EXPLAIN pack, statement 5 (reconcile scans), decides whether one is needed.
	for {
		var rows []models.Notification
		if err := db.Raw(`SELECT n.id, n.user_id, n.kind, n.subject_id, n.created_at,
			n.waiting, n.resolved_at, n.expires_at, n.archived_at
			FROM notifications n
			WHERE n.created_at > ? AND (n.created_at, n.id) > (?, ?)
			  AND NOT EXISTS (SELECT 1 FROM notification_deliveries nd
				WHERE nd.notification_id = n.id AND nd.channel = ?)
			  AND NOT EXISTS (SELECT 1 FROM jobs j WHERE j.unique_key = ? || n.id)
			ORDER BY n.created_at, n.id LIMIT ?`,
			now.Add(-24*time.Hour), afterAt, afterID, models.DeliveryEmail, notify.DeliverKeyPrefix, notifyReconcileBatch).
			Scan(&rows).Error; err != nil {
			return err
		}
		if err := db.Transaction(func(tx *gorm.DB) error { return queueReconciledNotifications(tx, rows, now) }); err != nil {
			return err
		}
		if len(rows) < notifyReconcileBatch {
			return nil
		}
		last := rows[len(rows)-1]
		afterAt, afterID = last.CreatedAt, last.ID
	}
}

// requeueLostDeliveries queues the job for delivery rows that have none. The
// page was read earlier, so the work is checked again in this transaction
// first (notify.Protect: recipients locked in ascending ID order, then the
// notices that still exist): a notice erased since, or one whose recipient is
// deleted, gets no job.
func requeueLostDeliveries(tx *gorm.DB, rows []models.NotificationDelivery, now time.Time) error {
	stubs := make([]models.Notification, 0, len(rows))
	for _, row := range rows {
		stubs = append(stubs, models.Notification{ID: row.NotificationID, UserID: row.UserID})
	}
	kept, err := notify.Protect(tx, stubs)
	if err != nil {
		return err
	}
	specs := make([]jobs.Spec, 0, len(kept))
	for _, row := range kept {
		specs = append(specs, notify.DeliverSpec(row.ID, now))
	}
	_, err = jobs.EnqueueMany(tx, specs)
	return err
}

// queueReconciledNotifications uses the persisted request flags to preserve
// pre-outbox duplicate and honeypot notices as bell-only. Account requests
// deliver only with a found request and parsed, clean flags; missing subjects,
// missing requests and invalid flags stay bell-only. Read requests together
// and queue each group in the same transaction.
//
// The rows were selected before this transaction. notify.QueueDelivery
// protects both groups with one call (recipients locked in ascending user ID,
// deleted users left out, then the notices reloaded), so a notice erased since
// the selection, or owned by a deleted user, gets no delivery row or job.
func queueReconciledNotifications(tx *gorm.DB, rows []models.Notification, now time.Time) error {
	var eligible []models.Notification
	var requestIDs []uint
	for _, row := range rows {
		k, ok := notify.Lookup(row.Kind)
		if !ok || (!k.AllowEmail && !k.AllowPush) {
			continue
		}
		eligible = append(eligible, row)
		if row.Kind == notify.AccountRequested && row.SubjectID != nil {
			requestIDs = append(requestIDs, *row.SubjectID)
		}
	}
	flags := map[uint]models.RequestFlags{}
	if len(requestIDs) > 0 {
		var requests []models.AccountRequest
		if err := tx.Select("id, flags").Where("id IN ?", requestIDs).Find(&requests).Error; err != nil {
			return err
		}
		for _, request := range requests {
			var f models.RequestFlags
			if json.Unmarshal([]byte(request.Flags), &f) == nil {
				flags[request.ID] = f
			}
		}
	}
	var deliver, bellOnly []models.Notification
	for _, row := range eligible {
		// A backfill is a late repair, not an ordinary send: a notice already resolved,
		// expired or archived before the sweep found it stays bell-only, as push would
		// drop it (pushStale). A notice the person read still mails; whether email skips
		// read notices is R6 open question 3, left at its default (no). Ordinary sends
		// and admin retries do not use this rule.
		if pushStale(&row, now) == pushWhyStale {
			bellOnly = append(bellOnly, row)
			continue
		}
		if row.Kind == notify.AccountRequested {
			var f models.RequestFlags
			var found bool
			if row.SubjectID != nil {
				f, found = flags[*row.SubjectID]
			}
			if !found || f.Duplicate || f.Honeypot {
				bellOnly = append(bellOnly, row)
				continue
			}
		}
		deliver = append(deliver, row)
	}
	return notify.QueueDelivery(tx, deliver, bellOnly)
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
	if k.AllowPush && n.PushTitle != "" {
		if err := a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return queuePushFor(tx, &n)
		}); err != nil {
			return err
		}
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
	u, off, suppressions, footer, err := readEmailRecipient(tx, n.UserID, !k.Required)
	if err != nil {
		return nil, err
	}
	// D11: read optional choices per notification after the claim, before rendering and dispatch.
	// A choice committed before this statement starts is honored; a later change
	// may not stop this one in-flight send. Every later delivery rechecks.
	address := ""
	if u != nil {
		address = u.Email
	}
	if !k.Required && off.Off(n.UserID, address, categoryScope(k.Category)) {
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
