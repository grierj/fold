package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"net/http"
	"strings"
	"time"

	"fold/internal/audit"
	"fold/internal/content"
	"fold/internal/jobs"
	"fold/internal/mail"
	"fold/internal/policy"
	"fold/internal/providers"
	"fold/internal/userlock"
	"fold/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The send pipeline (M3.5, PLAN 7.1). Scheduling a message queues
// message.send for its time. That job checks again that the author may
// still write to the audience, snapshots who it reaches into
// message_recipients (the skipped ones with their reason), adds guardian
// copies (DEC-19), and queues message.deliver_batch jobs of 50. Each batch
// sends its queued rows one by one, each as its reader gets it, and the
// last batch to finish settles the message as sent or failed.

const (
	kindMessageSend  = "message.send"
	kindMessageBatch = "message.deliver_batch"
	// messageBatchSize is the recipients one message.deliver_batch job sends:
	// the work a single job holds a worker for.
	messageBatchSize = 50
)

type messageSendJob struct {
	MessageID uint `json:"messageId"`
}

type messageBatchJob struct {
	MessageID    uint   `json:"messageId"`
	RecipientIDs []uint `json:"recipientIds"`
}

func (a *App) messageKinds() []jobs.Kind {
	return []jobs.Kind{
		{Type: messageSendType, Label: "Getting an announcement ready to send",
			Run: func(ctx context.Context, payload json.RawMessage) error {
				var p messageSendJob
				if err := json.Unmarshal(payload, &p); err != nil {
					return err
				}
				return a.prepareMessage(ctx, p.MessageID)
			}},
		{Type: messageBatchType, Label: "Sending an announcement",
			Run: func(ctx context.Context, payload json.RawMessage) error {
				var p messageBatchJob
				if err := json.Unmarshal(payload, &p); err != nil {
					return err
				}
				return a.deliverMessageBatch(ctx, p)
			}},
	}
}

func enqueueMessageSend(tx *gorm.DB, id uint, when time.Time) error {
	_, err := messageSendType.Enqueue(tx, messageSendJob{MessageID: id}, when, messageSendKey(id, when))
	return err
}

// Failure texts a message's author reads on its page.
const (
	whyAuthorGone    = "It wasn't sent because its author's account isn't active anymore."
	whyAuthorRefused = "It wasn't sent because its author can no longer write to these people."
	whyAudienceGone  = "It wasn't sent because that group or event isn't there anymore."
	whyNotPrepared   = "We couldn't get this ready to send. Try again to send it."
	whyStopped       = "Sending stopped before these went out."
	// whyNoSender fails a row at once when no email service is set up:
	// retrying for an hour can't help until an admin sets one up.
	whyNoSender = "No email service is set up yet, so it couldn't go out. Once an admin sets one up, try again."
	// whyProvider stands in for a provider's own error text, which a
	// leader or host can't act on (DEC-52); admins see the provider's words.
	whyProvider = "The email service didn't take it. Try again in a little while. If it keeps happening, ask an admin to open this message to see what went wrong."
	// whyNotPreparedM35a is how M3.5a worded whyNotPrepared; rows stored
	// then still carry it.
	whyNotPreparedM35a = "We couldn't get this ready to send. Schedule it again to try once more."
)

// ownMessageErrors are the failure texts Fold itself writes, which anyone
// who may see the message reads as they are; any other stored error is a
// provider's words. TestOwnMessageErrorsListEveryWhy keeps every why*
// constant here, so a copy change can't drop one; an old wording stays
// listed for the rows that carry it.
var ownMessageErrors = map[string]bool{
	whyAuthorGone: true, whyAuthorRefused: true, whyAudienceGone: true, whyNotPrepared: true, whyStopped: true,
	whyNoSender: true, whyProvider: true, whyNotPreparedM35a: true,
}

// neverPrepared: err says the message failed before anyone was chosen
// because it couldn't be got ready, in today's words or M3.5a's.
func neverPrepared(err string) bool { return err == whyNotPrepared || err == whyNotPreparedM35a }

// prepareMessage is message.send: steps 1–5 and 7 of PLAN 7.1. When its
// last attempt fails, the message fails with a reason instead of staying
// scheduled.
func (a *App) prepareMessage(ctx context.Context, id uint) error {
	err := a.prepareMessageOnce(ctx, id)
	if err != nil && jobs.LastAttempt(ctx) {
		log.Printf("message %d: couldn't prepare it: %s", id, jobs.CleanError(err))
		var m models.Message
		if lookup := a.DB.Select("id, author_id").First(&m, id).Error; lookup == nil {
			return a.refuseMessage(a.DB, &m, nil, whyNotPrepared)
		} else if !errors.Is(lookup, gorm.ErrRecordNotFound) {
			log.Printf("message %d: couldn't load it to say why: %s", id, jobs.CleanError(lookup))
		}
	}
	return err
}

func (a *App) prepareMessageOnce(ctx context.Context, id uint) error {
	db := a.DB.WithContext(ctx)
	var m models.Message
	if err := db.First(&m, id).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	now := time.Now()
	// Cancelled, already taken, or moved later (its own job comes then).
	if m.Status != models.MessageScheduled || m.ScheduledAt == nil || m.ScheduledAt.After(now.Add(time.Second)) {
		return nil
	}
	read := &m
	var au audienceJSON
	_ = json.Unmarshal([]byte(m.Audience), &au)
	category := messageCategory(&m)
	var ids []uint
	var hidden map[uint]bool
	if m.Category == models.MessageCategoryReminder {
		// A reminder has no author: its date's registrants, each checked
		// again for whether they may still see the date (M3.6, M1.13).
		if err := au.normalize(); err != nil || m.OccurrenceID == nil {
			return a.refuseMessage(db, &m, read, whyAudienceGone)
		}
		var err error
		if ids, err = a.audienceUserIDs(db, au, now); err != nil {
			return err
		}
		can, err := a.reminderReaders(db, *m.OccurrenceID, ids, now)
		if errors.Is(err, errAudienceGone) {
			return a.refuseMessage(db, &m, read, whyAudienceGone)
		}
		if err != nil {
			return err
		}
		hidden = map[uint]bool{}
		for _, id := range ids {
			hidden[id] = !can[id]
		}
	} else {
		// 1. The author's rights are checked again: roles change after scheduling.
		var author models.User
		if m.AuthorID == nil || db.First(&author, *m.AuthorID).Error != nil || author.Status != models.UserActive {
			return a.refuseMessage(db, &m, read, whyAuthorGone)
		}
		var err error
		ids, err = a.resolveAudience(&author, &au, now)
		switch {
		case errors.Is(err, errAudienceRefused):
			return a.refuseMessage(db, &m, read, whyAuthorRefused)
		case errors.Is(err, errAudienceGone), errors.Is(err, errAudienceInvalid):
			return a.refuseMessage(db, &m, read, whyAudienceGone)
		case err != nil:
			return err
		}
	}
	// 2–4. Who it reaches, each once, and who is skipped and why.
	facts, err := recipientFacts(db, ids, category)
	if err != nil {
		return err
	}
	for i := range facts {
		if facts[i].Skip == "" && hidden[facts[i].User.ID] {
			facts[i].Skip = skipCantSeeDate
		}
	}
	rows := make([]models.MessageRecipient, 0, len(facts))
	direct := map[uint]bool{}
	var teens []uint
	for _, f := range facts {
		row := models.MessageRecipient{MessageID: m.ID, UserID: f.User.ID, Channel: models.DeliveryEmail, Status: models.RecipientQueued}
		direct[f.User.ID] = true
		if f.Skip != "" {
			row.Status, row.Reason = models.RecipientSuppressed, f.Skip
		} else {
			row.Address = strings.TrimSpace(f.User.Email)
			if f.User.AccountType == models.AccountTeen {
				teens = append(teens, f.User.ID)
			}
		}
		rows = append(rows, row)
	}
	// 5. Guardian copies (DEC-19): by email only (DEC-29), each guardian
	// filtered like anyone else.
	if len(teens) > 0 && boolSettingIn(a.DB, settingGuardianCopies, false) {
		copies, err := guardianCopyRows(db, m.ID, teens, direct, category)
		if err != nil {
			return err
		}
		rows = append(rows, copies...)
	}
	// 7, push (DEC-123, DEC-122): a row for each active direct recipient
	// with push on, whatever their email row says; never a guardian copy.
	pushRows, err := a.messagePushRows(db, &m, facts)
	if err != nil {
		return err
	}
	rows = append(rows, pushRows...)
	return db.Transaction(func(tx *gorm.DB) error {
		// Everyone the snapshot names is locked FOR KEY SHARE, in ascending
		// id, before the claim: the rows below take that lock on their
		// users through their foreign keys anyway, and an account erasure
		// takes the user FOR UPDATE first. A person erased after the facts
		// were read is therefore seen as gone here, and no row is inserted
		// that still carries their address (L1: users, then the message).
		live, err := lockSnapshotUsers(tx, rows)
		if err != nil {
			return err
		}
		rows = revalidateSnapshotRows(rows, live)
		// The claim holds only if nobody changed the message since it was
		// read; if somebody did, it is checked again (stillDue).
		res := tx.Model(&models.Message{}).Where("id = ? AND status = ? AND scheduled_at = ? AND updated_at = ?", m.ID, models.MessageScheduled, m.ScheduledAt, m.UpdatedAt).
			Updates(map[string]any{"status": models.MessageSending, "error": "", "updated_at": time.Now()})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return stillDue(tx, m.ID)
		}
		if len(rows) > 0 {
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(&rows, rowBatch).Error; err != nil {
				return err
			}
		}
		// 7. Batches of 50 queued email rows, and one push job per device
		// of each push row, committed with the snapshot.
		var queued []uint
		if err := tx.Model(&models.MessageRecipient{}).Where("message_id = ? AND status = ? AND channel = ?", m.ID, models.RecipientQueued, models.DeliveryEmail).
			Order("id").Pluck("id", &queued).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.Message{}).Where("id = ?", m.ID).Update("recipient_count", len(queued)).Error; err != nil {
			return err
		}
		// Pushes first: they follow push consent, so they go even when no
		// email does (DEC-122).
		if err := enqueueMessagePushes(tx, m.ID); err != nil {
			return err
		}
		if len(queued) == 0 {
			return a.settleMessage(tx, m.ID)
		}
		return enqueueBatches(tx, m.ID, queued, "")
	})
}

// lockSnapshotUsers locks every user a snapshot names (each row's recipient
// and, for a guardian copy, the teen it is about) as notify.Send locks its
// recipients (userlock.KeyShareLive) and returns the ones that still exist
// and are not deleted.
func lockSnapshotUsers(tx *gorm.DB, rows []models.MessageRecipient) (map[uint]bool, error) {
	var ids []uint
	for _, r := range rows {
		ids = append(ids, r.UserID)
		if r.CopyOfUserID != nil {
			ids = append(ids, *r.CopyOfUserID)
		}
	}
	return userlock.KeyShareLive(tx, ids)
}

// revalidateSnapshotRows applies what lockSnapshotUsers found: an email row
// (direct or copy) whose recipient is gone is suppressed as not active with no
// address, a copy about a gone teen is dropped, and a push row for a gone
// recipient is dropped. The other rows pass through unchanged.
func revalidateSnapshotRows(rows []models.MessageRecipient, live map[uint]bool) []models.MessageRecipient {
	out := make([]models.MessageRecipient, 0, len(rows))
	for _, r := range rows {
		if r.Channel != models.DeliveryEmail {
			if live[r.UserID] {
				out = append(out, r)
			}
			continue
		}
		if r.CopyOfUserID != nil && !live[*r.CopyOfUserID] {
			continue
		}
		if !live[r.UserID] {
			r.Status, r.Reason, r.Address = models.RecipientSuppressed, skipNotActive, ""
		}
		out = append(out, r)
	}
	return out
}

// enqueueBatches queues message.deliver_batch jobs of 50 for ids. gen is
// "" for the first send and a fresh value for each "Try again"
// (messageBatchKeyPrefix).
func enqueueBatches(tx *gorm.DB, messageID uint, ids []uint, gen string) error {
	prefix := messageBatchKeyPrefix(messageID, gen)
	return eachChunk(ids, messageBatchSize, func(batch []uint) error {
		_, err := messageBatchType.Enqueue(tx, messageBatchJob{MessageID: messageID, RecipientIDs: batch}, time.Now(),
			prefix+idString(batch[0])+":"+idString(batch[len(batch)-1]))
		return err
	})
}

// guardianCopyRows are the copy rows for the confirmed guardians of teens:
// queued for those who can get email, suppressed with the reason for the
// rest. A guardian of two teens gets one copy for each; a guardian the
// message already reaches directly gets none.
func guardianCopyRows(db *gorm.DB, messageID uint, teens []uint, direct map[uint]bool, category string) ([]models.MessageRecipient, error) {
	var links []models.GuardianLink
	if err := db.Where("teen_user_id IN ? AND status = ?", teens, models.GuardianConfirmed).Order("id").Find(&links).Error; err != nil {
		return nil, err
	}
	ids := []uint{}
	for _, l := range links {
		if !direct[l.GuardianUserID] {
			ids = append(ids, l.GuardianUserID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	facts, err := recipientFacts(db, dedupeIDs(ids), category)
	if err != nil {
		return nil, err
	}
	byID := map[uint]recipientFact{}
	for _, f := range facts {
		byID[f.User.ID] = f
	}
	out := make([]models.MessageRecipient, 0, len(links))
	for _, l := range links {
		f, ok := byID[l.GuardianUserID]
		if !ok || direct[l.GuardianUserID] {
			continue
		}
		teen := *l.TeenUserID
		row := models.MessageRecipient{MessageID: messageID, UserID: l.GuardianUserID, CopyOfUserID: &teen, Channel: models.DeliveryEmail, Status: models.RecipientQueued}
		if f.Skip != "" {
			row.Status, row.Reason = models.RecipientSuppressed, f.Skip
		} else {
			row.Address = strings.TrimSpace(f.User.Email)
		}
		out = append(out, row)
	}
	return out, nil
}

// refuseMessage fails a scheduled message before it sends, saying why, and
// audits it. read, when set, is the message as the job read it: if it has
// changed since, the refusal is left to the next job.
func (a *App) refuseMessage(db *gorm.DB, m *models.Message, read *models.Message, why string) error {
	return db.Transaction(func(tx *gorm.DB) error {
		q := tx.Model(&models.Message{}).Where("id = ? AND status = ?", m.ID, models.MessageScheduled)
		if read != nil {
			q = q.Where("updated_at = ?", read.UpdatedAt)
		}
		res := q.Updates(map[string]any{"status": models.MessageFailed, "error": why, "updated_at": time.Now()})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			if read != nil {
				return stillDue(tx, m.ID)
			}
			return nil
		}
		return audit.Write(tx, audit.Entry{Actor: m.AuthorID, Action: audit.MessageRefused, TargetType: "message", TargetID: m.ID,
			Meta: map[string]any{"why": why}})
	})
}

// settleMessage marks a sending message sent, or failed when nobody got it
// and some sends failed, once no email row is still queued; the last batch
// to finish wins. It is audited message.sent with the counts. Push rows
// are companions (M3.13d2): one waiting for quiet hours never holds a
// message in sending, and none counts here.
func (a *App) settleMessage(tx *gorm.DB, id uint) error {
	type count struct {
		Status string
		N      int
	}
	var counts []count
	if err := tx.Model(&models.MessageRecipient{}).Select("status, count(*) AS n").Where("message_id = ? AND channel = ?", id, models.DeliveryEmail).Group("status").Scan(&counts).Error; err != nil {
		return err
	}
	by := map[string]int{}
	for _, c := range counts {
		by[c.Status] = c.N
	}
	if by[models.RecipientQueued] > 0 {
		return nil
	}
	reached := by[models.RecipientSent] + by[models.RecipientDelivered] + by[models.RecipientBounced] + by[models.RecipientComplained]
	status, why := models.MessageSent, ""
	if by[models.RecipientFailed] > 0 {
		var first models.MessageRecipient
		if err := tx.Select("error").Where("message_id = ? AND status = ? AND channel = ?", id, models.RecipientFailed, models.DeliveryEmail).Order("id").First(&first).Error; err != nil {
			return err
		}
		why = first.Error
		if reached == 0 {
			status = models.MessageFailed
		}
	}
	now := time.Now()
	res := tx.Model(&models.Message{}).Where("id = ? AND status = ?", id, models.MessageSending).
		Updates(map[string]any{"status": status, "sent_at": now, "error": why, "updated_at": now})
	if res.Error != nil || res.RowsAffected == 0 {
		return res.Error
	}
	var m models.Message
	if err := tx.Select("id, author_id").First(&m, id).Error; err != nil {
		return err
	}
	return audit.Write(tx, audit.Entry{Actor: m.AuthorID, Action: audit.MessageSent, TargetType: "message", TargetID: id,
		Meta: map[string]any{"status": status, "sent": reached, "skipped": by[models.RecipientSuppressed], "failed": by[models.RecipientFailed]}})
}

// batchDeadline keeps a batch well inside the jobs lease (15 minutes), so
// no second worker takes it over while it is still sending.
const batchDeadline = 10 * time.Minute

// errStopBatch: a send error that may pass stopped the batch; the job
// retries it.
var errStopBatch = errors.New("stop the batch and retry it")

// deliverMessageBatch is message.deliver_batch. Each queued row is sent
// under a row lock (SKIP LOCKED), so two workers never send the same row,
// and recorded in the same transaction. A crash between the provider
// accepting an email and the commit can still send that one again on the
// retry: delivery is at least once. A send error that may pass stops the
// batch to retry it; on the last attempt each such row fails with its
// error instead, so every row is tried and the message settles. Any other
// error on the last attempt fails the rows still queued.
func (a *App) deliverMessageBatch(ctx context.Context, p messageBatchJob) error {
	ctx, cancel := context.WithTimeout(ctx, batchDeadline)
	defer cancel()
	err := a.deliverMessageBatchOnce(ctx, p)
	if err == nil || !jobs.LastAttempt(ctx) {
		return err
	}
	// Records outlive the batch's deadline.
	db := a.DB.WithContext(context.WithoutCancel(ctx))
	log.Printf("message %d: giving up on a batch after its last attempt: %s", p.MessageID, jobs.CleanError(err))
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.MessageRecipient{}).Where("id IN ? AND status = ? AND channel = ?", p.RecipientIDs, models.RecipientQueued, models.DeliveryEmail).
			Updates(map[string]any{"status": models.RecipientFailed, "error": jobs.CleanError(err), "updated_at": time.Now()}).Error; err != nil {
			return err
		}
		return a.settleMessage(tx, p.MessageID)
	})
}

func (a *App) deliverMessageBatchOnce(ctx context.Context, p messageBatchJob) error {
	db := a.DB.WithContext(context.WithoutCancel(ctx))
	var m models.Message
	if err := db.First(&m, p.MessageID).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	if m.Status != models.MessageSending {
		return nil
	}
	var rows []models.MessageRecipient
	if err := db.Where("id IN ? AND message_id = ? AND status = ? AND channel = ?", p.RecipientIDs, m.ID, models.RecipientQueued, models.DeliveryEmail).Order("id").Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) == 0 {
		return db.Transaction(func(tx *gorm.DB) error { return a.settleMessage(tx, m.ID) })
	}
	readers, err := a.batchReaders(db, rows)
	if err != nil {
		return err
	}
	var au audienceJSON
	_ = json.Unmarshal([]byte(m.Audience), &au)
	values, err := a.recipientValues(db, &m, au, readers)
	if err != nil {
		return err
	}
	blocked, err := batchSuppressions(db, rows)
	if err != nil {
		return err
	}
	footer, err := footerFactsIn(db)
	if err != nil {
		return err
	}
	from, err := messageFromName(db, &m, footer.site)
	if err != nil {
		return err
	}
	replyTo, err := replyToAddressIn(db)
	if err != nil {
		return err
	}
	sender := mail.Unwrap(a.mailSender())
	optOut := categoryScope(messageCategory(&m))
	last := jobs.LastAttempt(ctx)
	for _, row := range rows {
		reader := readers[row.UserID]
		err := db.Transaction(func(tx *gorm.DB) error {
			var held uint
			if err := tx.Raw("SELECT id FROM message_recipients WHERE id = ? AND status = ? FOR UPDATE SKIP LOCKED", row.ID, models.RecipientQueued).Scan(&held).Error; err != nil {
				return err
			}
			if held == 0 {
				return nil // another worker has it, or it is done
			}
			// Who the reader is now: someone disabled or opted out since
			// the snapshot doesn't get it.
			if reader == nil || !(reader.Status == models.UserActive || reader.Status == models.UserGuest && guestMayGet(messageCategory(&m))) {
				return markRecipient(tx, row.ID, models.RecipientSuppressed, skipNotActive, "", "")
			}
			// D11: check the actual destination after claiming each row, including
			// guardian copies and rows re-queued by apiRetryMessage or recheckReminderRows.
			// A choice committed before this statement starts is honored; a later change
			// may not stop this one in-flight send. Every later row and retry rechecks.
			off, err := readEmailOff(tx, []emailDest{{UserID: row.UserID, Address: row.Address}})
			if err != nil {
				return err
			}
			if off.Off(row.UserID, row.Address, optOut) {
				return markRecipient(tx, row.ID, models.RecipientSuppressed, skipOptedOutOf(messageCategory(&m)), "", "")
			}
			msg, err := a.messageEmail(&m, row, reader, readers, values, footer)
			if err != nil {
				return err
			}
			msg.FromName, msg.ReplyTo = from, replyTo
			res := sendTracked(ctx, sender, msg, blocked[mail.NormalizeAddress(row.Address)])
			switch {
			case res.outcome == sendSent:
				return markRecipient(tx, row.ID, models.RecipientSent, "", res.providerID, "")
			case res.outcome == sendSuppressed:
				// The same words the snapshot uses for these people.
				why := skipNotWorking
				if s := (*mail.ErrSuppressed)(nil); errors.As(res.err, &s) && s.Reason == models.SuppressComplaint {
					why = skipComplained
				}
				return markRecipient(tx, row.ID, models.RecipientSuppressed, why, "", "")
			case res.outcome == sendNotConfigured:
				return markRecipient(tx, row.ID, models.RecipientFailed, "", "", whyNoSender)
			case res.outcome == sendPermanent, last:
				return markRecipient(tx, row.ID, models.RecipientFailed, "", "", jobs.CleanError(res.err))
			default:
				return errors.Join(errStopBatch, res.err)
			}
		})
		if err != nil {
			return err
		}
	}
	return db.Transaction(func(tx *gorm.DB) error { return a.settleMessage(tx, m.ID) })
}

func markRecipient(tx *gorm.DB, id uint, status, reason, providerID, errText string) error {
	updates := map[string]any{"status": status, "reason": reason, "provider_message_id": providerID, "error": errText, "updated_at": time.Now()}
	if status == models.RecipientSent {
		updates["sent_at"] = time.Now()
	}
	return tx.Model(&models.MessageRecipient{}).Where("id = ? AND status = ?", id, models.RecipientQueued).Updates(updates).Error
}

func (a *App) batchReaders(db *gorm.DB, rows []models.MessageRecipient) (map[uint]*models.User, error) {
	ids := []uint{}
	for _, r := range rows {
		ids = append(ids, r.UserID)
		if r.CopyOfUserID != nil {
			ids = append(ids, *r.CopyOfUserID)
		}
	}
	var users []models.User
	if err := db.Where("id IN ?", dedupeIDs(ids)).Find(&users).Error; err != nil {
		return nil, err
	}
	out := make(map[uint]*models.User, len(users))
	for i := range users {
		out[users[i].ID] = &users[i]
	}
	return out, nil
}

// batchSuppressions are the open suppression reasons on the batch's
// addresses, read once just before sending: an address may have bounced
// since the snapshot.
func batchSuppressions(db *gorm.DB, rows []models.MessageRecipient) (map[string][]string, error) {
	addrs := make([]string, 0, len(rows))
	for _, r := range rows {
		addrs = append(addrs, mail.NormalizeAddress(r.Address))
	}
	return mail.OpenSuppressionReasons(db, addrs)
}

// messageFromName is "Pat Lee via First Church" (PLAN 7.8): the author's
// name in the From header over the church's own address, kept short.
func messageFromName(db *gorm.DB, m *models.Message, site string) (string, error) {
	name := site
	if m.AuthorID != nil {
		var author models.User
		err := db.Select("id, name, display_name").First(&author, *m.AuthorID).Error
		switch {
		case err == nil:
			name = author.PublicName() + " via " + site
		case !errors.Is(err, gorm.ErrRecordNotFound):
			return "", err
		}
	}
	return clipRunes(name, 120), nil
}

// replyToAddressIn is the church office address replies go to (DEC-130),
// or "" when none is set or it isn't one plain address.
func replyToAddressIn(db *gorm.DB) (string, error) {
	addr, err := settingValueIn(db, settingReplyTo)
	if err != nil || addr == "" || !validEmail(addr) {
		return "", err
	}
	return addr, nil
}

// messageEmail renders m for one row's reader. A guardian copy is the
// teen's message with the teen's values, marked as a copy, with the
// guardian's own footer and unsubscribe link (DEC-19).
func (a *App) messageEmail(m *models.Message, row models.MessageRecipient, reader *models.User, readers map[uint]*models.User, values map[uint]map[string]string, footer footerFacts) (mail.Message, error) {
	vals := values[reader.ID]
	msg := *m
	if row.CopyOfUserID != nil {
		name := "your teen"
		if teen := readers[*row.CopyOfUserID]; teen != nil {
			// A name can't become a merge field.
			name = strings.ReplaceAll(teen.PublicName(), "{{", "{ {")
		}
		vals = values[*row.CopyOfUserID]
		note := "Copy: sent to " + name + " because your church sends guardian copies."
		msg.Subject = "Copy: " + m.Subject
		msg.BodyHTML = "<p><em>" + html.EscapeString(note) + "</em></p>" + m.BodyHTML
		msg.BodyText = note + "\n\n" + m.BodyText
	}
	r, err := a.renderMessageWith(&msg, reader, row.Address, vals, footer)
	if err != nil {
		return mail.Message{}, err
	}
	return mail.Message{To: row.Address, Subject: r.Subject, Text: r.Text, HTML: r.HTML,
		Stream: providers.StreamBroadcast, Optional: true, ListUnsubscribe: r.Unsubscribe}, nil
}

// recipientValues are each reader's merge values (PLAN 7.3), by user id:
// the first name, and for one event's people the event, with the date and
// page of the reader's own registration in the audience's statuses (the
// next one, for a series).
func (a *App) recipientValues(db *gorm.DB, m *models.Message, au audienceJSON, readers map[uint]*models.User) (map[uint]map[string]string, error) {
	out := map[uint]map[string]string{}
	for id, u := range readers {
		out[id] = map[string]string{content.FieldFirstName: firstName(u.PublicName())}
	}
	if au.Type != policy.AudienceOccurrence && au.Type != policy.AudienceEventSeries {
		return out, nil
	}
	ids, err := a.audienceEventIDs(db, au)
	if err != nil || len(ids) != 1 {
		return out, err
	}
	var ev models.Event
	if err := db.Select("id, title, tzid").First(&ev, ids[0]).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return out, nil // the event is gone: the fields stay unset
	} else if err != nil {
		return nil, err
	}
	statuses := au.Statuses
	if len(statuses) == 0 {
		statuses = defaultRegStatuses
	}
	type next struct {
		UID          uint
		OriginalDate time.Time
		StartsAt     time.Time
	}
	var dates []next
	q := db.Table("registrations r").Select("DISTINCT ON (COALESCE(r.user_id, r.registered_by)) COALESCE(r.user_id, r.registered_by) AS uid, o.original_date, o.starts_at").
		Joins("JOIN event_occurrences o ON o.id = r.occurrence_id").
		Where("o.event_id = ? AND r.status IN ? AND COALESCE(r.user_id, r.registered_by) IN ?", ev.ID, statuses, keysOf(readers))
	if au.Type == policy.AudienceOccurrence {
		q = q.Where("o.id = ?", au.OccurrenceID)
	} else {
		q = q.Where("o.status = ? AND o.starts_at >= ?", models.OccurrenceScheduled, time.Now())
	}
	if err := q.Order("COALESCE(r.user_id, r.registered_by), o.starts_at").Scan(&dates).Error; err != nil {
		return nil, err
	}
	title := ev.Title
	if au.Type == policy.AudienceOccurrence {
		// One date: its own title, when it has one.
		var occ models.EventOccurrence
		if err := db.Select("id, title_override").First(&occ, au.OccurrenceID).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		} else if err == nil && occ.TitleOverride != nil && *occ.TitleOverride != "" {
			title = *occ.TitleOverride
		}
	}
	for id := range out {
		a.setEventFields(out[id], ev.ID, title)
	}
	for _, d := range dates {
		if v := out[d.UID]; v != nil {
			v[content.FieldEventTime] = eventTimeText(d.StartsAt, ev.TZID)
			v[content.FieldManageLink] = a.emailLink(occurrenceDatePath(ev.ID, models.FormatCalendarDate(d.OriginalDate)))
		}
	}
	return out, nil
}

func keysOf(m map[uint]*models.User) []uint {
	out := make([]uint, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// reapMessages is the daily backstop for a send that stopped in a way no
// job will pick up (the process died on a job's last attempt): a message
// still scheduled long after its time with no send job left fails with a
// reason, and a sending one with no batch job left fails its queued rows
// and settles, so "Try again" can resend them.
func (a *App) reapMessages(ctx context.Context, now time.Time) error {
	db := a.DB.WithContext(ctx)
	live := func(kind string) string {
		return `NOT EXISTS (SELECT 1 FROM jobs j WHERE j.kind = '` + kind + `' AND j.status IN ('queued', 'running', 'failed')
			AND (j.payload->>'` + messageJobIDField + `')::bigint = messages.id)`
	}
	var stuck []models.Message
	if err := db.Select("id, author_id").Where("status = ? AND category <> ? AND scheduled_at < ? AND "+live(kindMessageSend), models.MessageScheduled, models.MessageCategoryReminder, now.Add(-time.Hour)).
		Find(&stuck).Error; err != nil {
		return err
	}
	for i := range stuck {
		if err := a.refuseMessage(db, &stuck[i], nil, whyNotPrepared); err != nil {
			return err
		}
	}
	var sending []uint
	if err := db.Model(&models.Message{}).Where("status = ? AND updated_at < ? AND "+live(kindMessageBatch), models.MessageSending, now.Add(-time.Hour)).
		Pluck("id", &sending).Error; err != nil {
		return err
	}
	for _, id := range sending {
		if err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&models.MessageRecipient{}).Where("message_id = ? AND status = ? AND channel = ?", id, models.RecipientQueued, models.DeliveryEmail).
				Updates(map[string]any{"status": models.RecipientFailed, "error": whyStopped, "updated_at": now}).Error; err != nil {
				return err
			}
			return a.settleMessage(tx, id)
		}); err != nil {
			return err
		}
	}
	if n := len(stuck) + len(sending); n > 0 {
		log.Printf("messages: settled %d that had stopped", n)
	}
	if n, err := reapMessagePushes(db, now); err != nil {
		return err
	} else if n > 0 {
		log.Printf("messages: stopped %d announcement pushes that had no job left", n)
	}
	return nil
}

// Per-author limit (PLAN 7.5): a leader or host may send at most this many
// emails to one group's people (or one church-wide event's) a day, counted
// in the church's day. Admins have no limit; their sends are audited.
const dailyMessagesPerGroup = 5

var errMessageDailyLimit = apiErr(http.StatusBadRequest, "message_daily_limit", fmt.Sprintf("you've sent %d messages to these people today", dailyMessagesPerGroup))

func (a *App) dailyLimitReached(me *models.User, id uint, au audienceJSON, when time.Time) (bool, error) {
	if policy.IsAdmin(asPerson(me)) {
		return false, nil
	}
	keys, err := a.audienceKeys(au)
	if err != nil || len(keys) == 0 {
		return false, err
	}
	loc := churchZoneOrUTC(a.DB)
	local := when.In(loc)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	var others []models.Message
	if err := a.DB.Select("id, audience").Where("author_id = ? AND id <> ? AND status IN ? AND scheduled_at >= ? AND scheduled_at < ?",
		me.ID, id, []string{models.MessageScheduled, models.MessageSending, models.MessageSent}, start, start.AddDate(0, 0, 1)).
		Find(&others).Error; err != nil {
		return false, err
	}
	counts := map[string]int{}
	for _, o := range others {
		var oau audienceJSON
		_ = json.Unmarshal([]byte(o.Audience), &oau)
		oks, err := a.audienceKeys(oau)
		if err != nil {
			return false, err
		}
		for _, k := range oks {
			counts[k]++
		}
	}
	for _, k := range keys {
		if counts[k] >= dailyMessagesPerGroup {
			return true, nil
		}
	}
	return false, nil
}

// audienceKeys names whose people an audience reaches, for the limit: a
// group ("g<id>", including its events) or a church-wide event ("e<id>").
func (a *App) audienceKeys(au audienceJSON) ([]string, error) {
	if au.Type == policy.AudienceGroup {
		return []string{"g" + idString(au.GroupID)}, nil
	}
	ids, err := a.audienceEventIDs(a.DB, au)
	if err != nil || len(ids) == 0 {
		return nil, nil
	}
	var evs []models.Event
	if err := a.DB.Unscoped().Select("id, group_id").Where("id IN ?", ids).Find(&evs).Error; err != nil {
		return nil, err
	}
	out := []string{}
	seen := map[string]bool{}
	for _, ev := range evs {
		k := "e" + idString(ev.ID)
		if ev.GroupID != nil {
			k = "g" + idString(*ev.GroupID)
		}
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out, nil
}

// errChangedWhilePreparing: the message was edited while message.send
// worked on it and is still due, so the job runs again on the new version.
var errChangedWhilePreparing = errors.New("the message changed while it was being prepared")

// stillDue answers a missed claim: nil when the message is no longer
// scheduled or was moved later (another job, or none, has it), else
// errChangedWhilePreparing so the job retries. An edit queues no job of its
// own, so without this a mid-claim edit would wait for the daily reaper.
func stillDue(tx *gorm.DB, id uint) error {
	var m models.Message
	if err := tx.Select("id, status, scheduled_at").First(&m, id).Error; err != nil {
		return err
	}
	if m.Status == models.MessageScheduled && m.ScheduledAt != nil && !m.ScheduledAt.After(time.Now().Add(time.Second)) {
		return errChangedWhilePreparing
	}
	return nil
}
