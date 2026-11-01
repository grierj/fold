package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"fold/internal/content"
	"fold/internal/jobs"
	"fold/internal/notify"
	"fold/internal/push"
	"fold/models"

	"gorm.io/gorm"
)

// Announcement pushes (M3.13d2, PLAN 7.1 step 7, DEC-123). An announcement
// always goes by email; with "Push notification too" on (push_alert, the
// default) message.send also snapshots one message_recipients row with
// channel 'push' for each direct recipient who is active and has push on
// for messages, and queues one push.deliver job per device of theirs, in
// the same transaction as the snapshot. Push follows push consent, not
// the email row (DEC-122): someone whose email was skipped for an email
// reason (turned off email from leaders, a bounce, no address) still gets
// the push, and reads the message in Fold (/messages/:id/read, DEC-135).
// Guardian copies, guests, household people and accounts that aren't
// active get no push. The push is a short note: the subject and the first
// 120 characters of the plain text. The push rows never decide the
// message's status or counts: those follow its email rows
// (settleMessage), and "Try again" resends only email.

const (
	// pushWaitPrefix starts a waiting push row's reason; the rest is when
	// quiet hours end, on the recipient's clock: "outside quiet hours ·
	// sending at 8:00 AM" (research 2.6). The history counts by it.
	pushWaitPrefix = "outside quiet hours · sending at "
	// pushWhyStopped: the daily reaper found a push row still queued with
	// no job left to send it.
	pushWhyStopped = "didn't go out"
	// messagePushSnippet is how much of the plain text a push carries.
	messagePushSnippet = 120
	// messagePushOpen ends every announcement push (PLAN 7.3).
	messagePushOpen = "Open to read."
)

// pushReady says who among ids would get an optional push of category
// now: their latest (push, all) consent is opted in, their latest
// (push, category:<category>) consent isn't opted out, and they have at
// least one device (DEC-122). It reads in chunks, like recipientFacts.
func pushReady(db *gorm.DB, ids []uint, category string) (map[uint]bool, error) {
	out := map[uint]bool{}
	err := eachIDChunk(ids, func(chunk []uint) error {
		var on []uint
		if err := db.Raw(`SELECT c.user_id FROM (
				SELECT DISTINCT ON (user_id, scope) user_id, scope, status FROM consents
				WHERE user_id IN ? AND channel = ? AND scope IN ('all', ?) ORDER BY user_id, scope, at DESC, id DESC) c
			WHERE EXISTS (SELECT 1 FROM push_subscriptions d WHERE d.user_id = c.user_id)
			GROUP BY c.user_id
			HAVING bool_or(c.scope = 'all' AND c.status = ?) AND NOT bool_or(c.scope <> 'all' AND c.status = ?)`,
			chunk, models.ConsentPush, categoryScope(category), models.ConsentOptedIn, models.ConsentOptedOut).Scan(&on).Error; err != nil {
			return err
		}
		for _, id := range on {
			out[id] = true
		}
		return nil
	})
	return out, err
}

// pushCandidates are the people among facts who may get a push: active
// accounts. A guest never has a device, and an account that is disabled
// or waiting for consent gets neither the email nor the push. An email
// skip (turned off email from leaders, a bounce, no address) doesn't
// matter here: push follows its own consent (DEC-122).
func pushCandidates(facts []recipientFact) []uint {
	out := []uint{}
	for _, f := range facts {
		if f.User.Status == models.UserActive {
			out = append(out, f.User.ID)
		}
	}
	return out
}

// messagePushRows are message.send's push rows for m: one queued row per
// candidate among facts who has push on for m's category and a device,
// when m asks for it, is an announcement, and the church has push on. A
// reminder's push is M3.13d3's.
func (a *App) messagePushRows(db *gorm.DB, m *models.Message, facts []recipientFact) ([]models.MessageRecipient, error) {
	if !m.PushAlert || m.Category != models.MessageCategoryAnnouncement || !a.pushEnabled() {
		return nil, nil
	}
	ids := pushCandidates(facts)
	if len(ids) == 0 {
		return nil, nil
	}
	on, err := pushReady(db, ids, messageCategory(m))
	if err != nil {
		return nil, err
	}
	rows := []models.MessageRecipient{}
	for _, id := range ids {
		if on[id] {
			rows = append(rows, models.MessageRecipient{MessageID: m.ID, UserID: id, Channel: models.DeliveryPush, Status: models.RecipientQueued})
		}
	}
	return rows, nil
}

// enqueueMessagePushes queues one push.deliver per device of each queued
// push row of the message, in batched INSERTs. Its key
// (push:mr:<row>:d:<device>) makes it once per device, however often it
// runs; the payload holds ids only. A queued row whose devices are all
// gone by now is "device gone" at once, rather than waiting for the
// reaper.
func enqueueMessagePushes(tx *gorm.DB, messageID uint) error {
	var pairs []struct {
		RecipientID uint
		DeviceID    uint
	}
	if err := tx.Raw(`SELECT r.id AS recipient_id, d.id AS device_id FROM message_recipients r
		JOIN push_subscriptions d ON d.user_id = r.user_id
		WHERE r.message_id = ? AND r.channel = ? AND r.status = ? ORDER BY r.id, d.id`,
		messageID, models.DeliveryPush, models.RecipientQueued).Scan(&pairs).Error; err != nil {
		return err
	}
	now := time.Now()
	specs := make([]jobs.Spec, 0, len(pairs))
	for _, p := range pairs {
		specs = append(specs, jobs.Spec{Kind: kindPushDeliver, Payload: pushJob{MessageRecipientID: p.RecipientID, DeviceID: p.DeviceID}, RunAt: now,
			UniqueKey: "push:mr:" + historyID(p.RecipientID) + ":d:" + historyID(p.DeviceID), MaxAttempts: pushDeliverAttempts})
	}
	if _, err := jobs.EnqueueMany(tx, specs); err != nil {
		return err
	}
	return tx.Exec(`UPDATE message_recipients r SET status = ?, reason = ?, updated_at = now()
		WHERE r.message_id = ? AND r.channel = ? AND r.status = ?
		AND NOT EXISTS (SELECT 1 FROM push_subscriptions d WHERE d.user_id = r.user_id)`,
		models.RecipientSuppressed, pushWhyGone, messageID, models.DeliveryPush, models.RecipientQueued).Error
}

// messagePushRecorder writes an announcement push's outcome on its row,
// as recordPushIn does for a notification: a sent row is final, and any
// other first outcome stays unless a later device takes it, which makes
// the row sent. A row is never moved back to queued.
func messagePushRecorder(id uint) pushRecorder {
	return func(db *gorm.DB, status, reason string, now time.Time) error {
		updates := map[string]any{"status": status, "reason": reason, "updated_at": time.Now()}
		q := db.Model(&models.MessageRecipient{}).Where("id = ? AND channel = ?", id, models.DeliveryPush)
		if status == models.RecipientSent {
			updates["sent_at"] = now.UTC()
			q = q.Where("status <> ?", models.RecipientSent)
		} else {
			q = q.Where("status = ?", models.RecipientQueued)
		}
		return q.Updates(updates).Error
	}
}

// deliverMessagePush sends one announcement push row to one device. It
// checks, in order: the church's switch, the account, the person's push
// consent for the message's category, the device, that the message is
// still going out, quiet hours (announcements are optional, so they
// wait), and the hourly limit, which counts announcement and notification
// pushes together (DEC-126). It returns an error only when a retry may
// succeed.
func (a *App) deliverMessagePush(ctx context.Context, rowID, deviceID uint) error {
	db := a.DB.WithContext(ctx)
	var row models.MessageRecipient
	if err := db.First(&row, rowID).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	if row.Channel != models.DeliveryPush || row.CopyOfUserID != nil {
		return nil
	}
	rec := messagePushRecorder(row.ID)
	record := func(status, reason string) error { return rec(db, status, reason, a.pushNow()) }
	if !a.pushEnabled() {
		return record(models.RecipientSuppressed, pushWhyChurchOff)
	}
	var u models.User
	if err := db.Select("id", "name", "display_name", "status", "timezone").First(&u, row.UserID).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	if u.Status != models.UserActive {
		return record(models.RecipientSuppressed, pushWhyInactive)
	}
	var m models.Message
	if err := db.Select("id", "status", "category", "subject", "body_text", "audience").First(&m, row.MessageID).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	category := messageCategory(&m)
	on, categoryOff, err := pushConsent(db, u.ID, category)
	if err != nil {
		return err
	}
	switch {
	case !on:
		return record(models.RecipientSuppressed, pushWhyNotOn)
	case categoryOff:
		return record(models.RecipientSuppressed, "push turned off for "+notify.CategoryEmailName(category))
	}
	var dev models.PushSubscription
	if err := db.First(&dev, deviceID).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if dev.ID == 0 || dev.UserID != u.ID {
		// Turned off, gone, or changed hands since it was queued. Another
		// device's job decides when the person still has one.
		var left int64
		if err := db.Model(&models.PushSubscription{}).Where("user_id = ?", u.ID).Count(&left).Error; err != nil {
			return err
		}
		if left == 0 {
			return record(models.RecipientSuppressed, pushWhyGone)
		}
		return nil
	}
	if m.Status != models.MessageSending && m.Status != models.MessageSent {
		return record(models.RecipientSuppressed, pushWhyStale)
	}
	now := a.pushNow()
	loc := zoneOrChurch(db, u.Timezone)
	if inside, opens := a.pushQuietHours().Check(now, "", loc); !inside {
		if soonest := now.Add(time.Second); opens.Before(soonest) {
			opens = soonest
		}
		// The row stays queued and says when it goes, on the
		// recipient's clock, for the history.
		if err := db.Model(&models.MessageRecipient{}).Where("id = ? AND status = ?", row.ID, models.RecipientQueued).
			Updates(map[string]any{"reason": pushWaitPrefix + opens.In(loc).Format("3:04 PM"), "updated_at": time.Now()}).Error; err != nil {
			return err
		}
		return jobs.Later(opens)
	}
	over, err := a.overHourlyLimit(ctx, pushItem{UserID: u.ID, RecipientID: row.ID}, now)
	if err != nil {
		return err
	}
	if over {
		return record(models.RecipientSuppressed, pushWhyHourly)
	}
	vals, err := a.pushValues(db, &m, &u)
	if err != nil {
		return err
	}
	title, text := messagePushText(&m, vals)
	what := fmt.Sprintf("message %d recipient %d", m.ID, row.ID)
	body, err := push.MarshalPayload(push.NewPayload(push.Notification{
		Title: title, Body: text, Navigate: a.emailLink(messageReadPath(m.ID)), Tag: "m" + historyID(m.ID),
	}))
	if err != nil {
		log.Printf("push: %s is too large to push: a bug in its push text", what)
		return record(models.RecipientFailed, pushWhyTooLarge)
	}
	sub := push.Subscription{Endpoint: dev.Endpoint, P256dh: dev.P256dh, Auth: dev.Auth}
	opts := push.Options{TTL: pushTTL, Urgency: push.UrgencyNormal, Topic: "mr" + strconv.FormatUint(uint64(row.ID), 10)}
	return a.pushOutcome(ctx, what, dev, a.pushClient().Send(ctx, sub, body, opts), "", rec)
}

// pushValues are u's merge values for m's push. The lock screen never
// carries a link, so only the first name and the event's title and time
// matter; the registration reads behind those run only when the subject
// or text uses one.
func (a *App) pushValues(db *gorm.DB, m *models.Message, u *models.User) (map[string]string, error) {
	needsEvent := false
	for _, f := range content.FieldsIn(m.Subject + "\n" + m.BodyText) {
		if f == content.FieldEventTitle || f == content.FieldEventTime {
			needsEvent = true
		}
	}
	if !needsEvent {
		return map[string]string{content.FieldFirstName: firstName(u.PublicName())}, nil
	}
	var au audienceJSON
	_ = json.Unmarshal([]byte(m.Audience), &au)
	values, err := a.recipientValues(db, m, au, map[uint]*models.User{u.ID: u})
	return values[u.ID], err
}

// messageReadPath is where a recipient reads an announcement in Fold.
func messageReadPath(id uint) string { return "/messages/" + historyID(id) + "/read" }

var (
	// A link's address after its text in the plain-text part, as
	// content.PlainText writes it: " (https://…)" or " ({{event_link}})".
	pushLinkAddressRe = regexp.MustCompile(`\s*\((?:https?://|\{\{)[^()\s]*\)`)
	pushBareURLRe     = regexp.MustCompile(`(?i)\b(?:https?://|www\.)\S+`)
	// Contact details (DEC-124): an email address, with or without
	// mailto:, and a phone number.
	pushEmailRe = regexp.MustCompile(`(?i)(?:\bmailto:\S*)|(?:[a-z0-9._%+-]+@[a-z0-9-]+(?:\.[a-z0-9-]+)+)`)
	// pushPhoneRe matches phone-shaped runs only: "+44 20 7946 0958" (a
	// leading + with groups of 1–4 digits), "(208) 555-0123",
	// "1-208-555-0123" and "555-0123" (3-3-4 or 3-4 groups joined by one
	// space, dot or dash), and ten unbroken digits. Room lists, hymn
	// numbers and years ("101 102 103", "12 45 301", "2026 2027") have no
	// such shape and stay; lockScreenSafe also checks for 7 to 15 digits.
	pushPhoneRe = regexp.MustCompile(`\+\d{1,3}(?:[ .-]\(?\d{1,4}\)?){1,5}` +
		`|\(\d{3}\)[ .-]?\d{3}[ .-]\d{4}\b` +
		`|\b(?:1[ .-])?\d{3}[ .-]\d{3}[ .-]\d{4}\b` +
		`|\b\d{3}[ .-]\d{4}\b` +
		`|\b1?\d{10}\b`)
	// pushDateTimeRe is an ISO date (with its time, if any) or a clock
	// time: never a phone number, so the phone pass skips it.
	pushDateTimeRe = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}(?:[T ]\d{1,2}:\d{2}(?::\d{2})?)?\b|\b\d{1,2}:\d{2}(?::\d{2})?\b`)
)

// lockScreenSafe takes out of s what a lock screen must never show
// (DEC-124): link addresses, URLs, email addresses and phone-shaped
// numbers, leaving dates, times and other numbers. It is best effort: a
// number spelled out in words, or written in an odd shape, isn't caught.
func lockScreenSafe(s string) string {
	s = pushLinkAddressRe.ReplaceAllString(s, "")
	s = pushBareURLRe.ReplaceAllString(s, "")
	s = pushEmailRe.ReplaceAllString(s, "")
	// The phone pass runs only between the dates and times.
	var b strings.Builder
	at := 0
	for _, span := range pushDateTimeRe.FindAllStringIndex(s, -1) {
		b.WriteString(stripPhones(s[at:span[0]]))
		b.WriteString(s[span[0]:span[1]])
		at = span[1]
	}
	b.WriteString(stripPhones(s[at:]))
	return b.String()
}

// stripPhones removes the phone-shaped runs in s that hold 7 to 15 digits.
func stripPhones(s string) string {
	return pushPhoneRe.ReplaceAllStringFunc(s, func(run string) string {
		digits := 0
		for _, r := range run {
			if r >= '0' && r <= '9' {
				digits++
			}
		}
		if digits < 7 || digits > 15 {
			return run
		}
		return ""
	})
}

// messagePushText is an announcement's lock-screen text for one reader
// (DEC-124, research 2.4): the subject as their email says it, and the
// first 120 characters of the plain text then "Open to read.". It is never
// the HTML, and carries no link or contact detail of any kind: link
// addresses, URLs, the link fields, email addresses and phone numbers are
// left out, so no signed or manage link can reach a lock screen. vals are
// the reader's merge values; any field without one (a link field, a stray
// field) becomes nothing.
func messagePushText(m *models.Message, vals map[string]string) (title, body string) {
	text := map[string]string{}
	for k, v := range vals {
		if k != content.FieldEventLink && k != content.FieldManageLink {
			text[k] = v
		}
	}
	oneLine := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	title = clipRunes(oneLine(lockScreenSafe(oneLine(content.Merge(m.Subject, text, false)))), maxMessageSubject)
	if title == "" {
		title = "New message"
	}
	// Link addresses go before the merge, while a link field still reads as
	// one; lines are joined before the contact pass, so a number split
	// across lines is caught.
	s := oneLine(lockScreenSafe(oneLine(content.Merge(pushLinkAddressRe.ReplaceAllString(m.BodyText, ""), text, false))))
	if r := []rune(s); len(r) > messagePushSnippet {
		cut := string(r[:messagePushSnippet])
		if i := strings.LastIndex(cut, " "); i > messagePushSnippet/2 {
			cut = cut[:i]
		}
		s = strings.TrimRight(cut, " ,;:.-–—") + "…"
	}
	if s == "" {
		return title, messagePushOpen
	}
	return title, s + "\n" + messagePushOpen
}

// reapMessagePushes is the daily backstop for announcement pushes: a
// push row still queued an hour on with no push.deliver job left for it
// (its devices were added after the snapshot, or its job stopped) is
// suppressed, so a history never waits forever.
func reapMessagePushes(db *gorm.DB, now time.Time) (int64, error) {
	res := db.Exec(`UPDATE message_recipients r SET status = ?, reason = ?, updated_at = ?
		WHERE r.channel = 'push' AND r.status = 'queued' AND r.updated_at < ?
		AND NOT EXISTS (SELECT 1 FROM jobs j WHERE j.kind = ? AND j.status IN ('queued', 'running', 'failed')
			AND (j.payload->>'messageRecipientId')::bigint = r.id)`,
		models.RecipientSuppressed, pushWhyStopped, now, now.Add(-time.Hour), kindPushDeliver)
	return res.RowsAffected, res.Error
}
