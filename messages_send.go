package main

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"log"
	"slices"
	"strconv"
	"strings"
	"time"

	"fold/internal/audit"
	"fold/internal/content"
	"fold/internal/jobs"
	"fold/internal/mail"
	"fold/internal/policy"
	"fold/internal/providers"
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
	messageBatchSize = 50
	// Attempts: a batch rides out a provider outage of about an hour (30 seconds doubling, the
	// jobs backoff) before its rows are marked failed.
	messageSendAttempts  = 5
	messageBatchAttempts = 8
)

// Church settings the pipeline reads (5.3).
const (
	// settingGuardianCopies: "true" sends each confirmed guardian a marked
	// email copy of messages to their teen (DEC-19, DEC-29). Default off.
	settingGuardianCopies = "guardian_copies"
	// settingReplyTo is where replies to an announcement go until the reply
	// relay (M3.11): the church office address (DEC-130). Empty: none.
	settingReplyTo = "reply_to_address"
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
		{Name: kindMessageSend, Label: "Getting an announcement ready to send", MaxAttempts: messageSendAttempts,
			Run: func(ctx context.Context, payload json.RawMessage) error {
				var p messageSendJob
				if err := json.Unmarshal(payload, &p); err != nil {
					return err
				}
				return a.prepareMessage(ctx, p.MessageID)
			}},
		{Name: kindMessageBatch, Label: "Sending an announcement", MaxAttempts: messageBatchAttempts,
			Run: func(ctx context.Context, payload json.RawMessage) error {
				var p messageBatchJob
				if err := json.Unmarshal(payload, &p); err != nil {
					return err
				}
				return a.deliverMessageBatch(ctx, p)
			}},
	}
}

// backgroundKinds are the job kinds queued by events, not by the day.
func (a *App) backgroundKinds() []jobs.Kind {
	return slices.Concat(a.eventKinds(), a.messageKinds(), a.reminderKinds(), a.pushKinds())
}

// enqueueMessageSend queues message.send for a message scheduled at when.
// The key carries the time, so rescheduling queues a new job and the old
// one finds the message not yet due and does nothing.
func enqueueMessageSend(tx *gorm.DB, id uint, when time.Time) error {
	_, err := jobs.Enqueue(tx, kindMessageSend, messageSendJob{MessageID: id}, when,
		"message.send:"+historyID(id)+":"+strconv.FormatInt(when.Unix(), 10), messageSendAttempts)
	return err
}

func (a *App) settingValue(key string) string {
	var s models.Setting
	if a.DB.Select("value").Where("key = ?", key).First(&s).Error != nil {
		return ""
	}
	return strings.TrimSpace(s.Value)
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
		if a.DB.Select("id, author_id").First(&m, id).Error == nil {
			return a.refuseMessage(a.DB, &m, nil, whyNotPrepared)
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
	if len(teens) > 0 && a.settingValue(settingGuardianCopies) == "true" {
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
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(&rows, 500).Error; err != nil {
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

// enqueueBatches queues message.deliver_batch jobs of 50 for ids. gen is
// "" for the first send and a fresh value for each "Try again", so a
// retry's keys never collide with the jobs already done.
func enqueueBatches(tx *gorm.DB, messageID uint, ids []uint, gen string) error {
	prefix := "message.batch:" + historyID(messageID) + ":"
	if gen != "" {
		prefix += gen + ":"
	}
	for len(ids) > 0 {
		n := min(len(ids), messageBatchSize)
		batch := ids[:n]
		ids = ids[n:]
		if _, err := jobs.Enqueue(tx, kindMessageBatch, messageBatchJob{MessageID: messageID, RecipientIDs: batch}, time.Now(),
			prefix+historyID(batch[0])+":"+historyID(batch[len(batch)-1]), messageBatchAttempts); err != nil {
			return err
		}
	}
	return nil
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
	optedOut, err := emailOptOuts(db, keysOf(readers))
	if err != nil {
		return err
	}
	footer := footerFacts{site: a.siteName(), address: a.churchAddress()}
	from := a.messageFromName(db, &m, footer.site)
	replyTo := a.replyToAddress()
	sender := mail.Unwrap(a.Mailer)
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
			switch {
			case reader == nil || !(reader.Status == models.UserActive || reader.Status == models.UserGuest && m.Category == models.MessageCategoryReminder):
				return markRecipient(tx, row.ID, models.RecipientSuppressed, skipNotActive, "", "")
			case optedOut[row.UserID][optOut]:
				return markRecipient(tx, row.ID, models.RecipientSuppressed, skipOptedOutOf(messageCategory(&m)), "", "")
			}
			msg, err := a.messageEmail(&m, row, reader, readers, values, footer)
			if err != nil {
				return err
			}
			msg.FromName, msg.ReplyTo = from, replyTo
			var providerID string
			err = mail.Blocked(blocked[normalizeEmail(row.Address)], msg)
			if err == nil {
				if tracked, ok := sender.(providers.EmailSender); ok {
					providerID, err = tracked.SendEmail(ctx, msg)
				} else {
					err = sender.Send(ctx, msg)
				}
			}
			switch {
			case err == nil:
				return markRecipient(tx, row.ID, models.RecipientSent, "", providerID, "")
			case mail.IsSuppressed(err):
				// The same words the snapshot uses for these people.
				why := skipNotWorking
				if s := (*mail.ErrSuppressed)(nil); errors.As(err, &s) && s.Reason == models.SuppressComplaint {
					why = skipComplained
				}
				return markRecipient(tx, row.ID, models.RecipientSuppressed, why, "", "")
			case mail.NotConfigured(err):
				return markRecipient(tx, row.ID, models.RecipientFailed, "", "", whyNoSender)
			case mail.IsPermanent(err), last:
				return markRecipient(tx, row.ID, models.RecipientFailed, "", "", jobs.CleanError(err))
			default:
				return errors.Join(errStopBatch, err)
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

// batchReaders loads everyone a batch is for, and the teens its copies
// are about, in one read.
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
		addrs = append(addrs, normalizeEmail(r.Address))
	}
	var sups []models.Suppression
	if err := db.Where("channel = 'email' AND cleared_at IS NULL AND address IN ?", addrs).Find(&sups).Error; err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, s := range sups {
		out[s.Address] = append(out[s.Address], s.Reason)
	}
	return out, nil
}

// messageFromName is "Pat Lee via First Church" (PLAN 7.8): the author's
// name in the From header over the church's own address, kept short.
func (a *App) messageFromName(db *gorm.DB, m *models.Message, site string) string {
	name := site
	var author models.User
	if m.AuthorID != nil && db.Select("id, name, display_name").First(&author, *m.AuthorID).Error == nil {
		name = author.PublicName() + " via " + site
	}
	return clipRunes(name, 120)
}

// replyToAddress is the church office address replies go to (DEC-130),
// or "" when none is set or it isn't one plain address.
func (a *App) replyToAddress() string {
	addr := a.settingValue(settingReplyTo)
	if addr == "" || !validEmail(addr) {
		return ""
	}
	return addr
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
	r, err := a.renderMessageWith(&msg, reader, vals, footer)
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
	if err := db.Select("id, title, tzid").First(&ev, ids[0]).Error; err != nil {
		return out, nil
	}
	loc, err := time.LoadLocation(ev.TZID)
	if err != nil {
		loc = time.UTC
	}
	statuses := au.Statuses
	if len(statuses) == 0 {
		statuses = defaultRegStatuses
	}
	type next struct {
		UID          uint
		OriginalDate string
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
		if db.Select("id, title_override").First(&occ, au.OccurrenceID).Error == nil && occ.TitleOverride != nil && *occ.TitleOverride != "" {
			title = *occ.TitleOverride
		}
	}
	link := a.emailLink("/events/" + historyID(ev.ID))
	for id := range out {
		out[id][content.FieldEventTitle], out[id][content.FieldEventLink], out[id][content.FieldManageLink] = title, link, link
	}
	for _, d := range dates {
		if v := out[d.UID]; v != nil {
			v[content.FieldEventTime] = formatWhen(d.StartsAt, loc)
			v[content.FieldManageLink] = a.emailLink(occurrenceDatePath(ev.ID, d.OriginalDate))
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
			AND (j.payload->>'messageId')::bigint = messages.id)`
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

const errMessageDailyLimit = "you've sent 5 messages to these people today"

// dailyLimitReached reports whether scheduling message id for when would
// pass the limit for any group or church-wide event its audience reaches.
func (a *App) dailyLimitReached(me *models.User, id uint, au audienceJSON, when time.Time) (bool, error) {
	if policy.IsAdmin(asPerson(me)) {
		return false, nil
	}
	keys, err := a.audienceKeys(au)
	if err != nil || len(keys) == 0 {
		return false, err
	}
	loc, err := time.LoadLocation(a.churchTimezone())
	if err != nil {
		loc = time.UTC
	}
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
		return []string{"g" + historyID(au.GroupID)}, nil
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
		k := "e" + historyID(ev.ID)
		if ev.GroupID != nil {
			k = "g" + historyID(*ev.GroupID)
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
