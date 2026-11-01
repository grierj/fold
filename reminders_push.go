package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"fold/internal/content"
	"fold/internal/events"
	"fold/internal/jobs"
	"fold/internal/notify"
	"fold/internal/policy"
	"fold/internal/push"
	"fold/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The 2-hour push reminder (M3.13d3, PLAN 7.7, DEC-123). Each event has a
// switch (reminder_push, on by default); each date's push reminder is its
// own automatic reminder message, kind push, with only push rows: one per
// going registrant who has push on for event updates and a device. It is
// never an announcement's push-only case (DEC-123 refuses that): reminders
// are a channel of their own (7.7). No email, no waitlist push, no guests
// (they can't subscribe), and never a bell row (DEC-44).
//
// The hourly sweep (reminders.sweep) plans each person's send: due 2
// hours before the start; outside their quiet hours it moves to the last
// allowed minute before, up to 18 hours earlier (7:59 PM the night before
// a 9:00 AM date), else it is skipped and their row says "outside quiet
// hours" (7.7, written once for SMS and push). A planned row is queued with
// one push.deliver per device due at its time; the job checks the date,
// the switch, the registration and who may see the date again when it
// runs, so a cancelled or moved date, a switch turned off or someone no
// longer going sends nothing, and a backlog that runs it more than 15
// minutes into quiet hours skips it. The lock screen never shows the place
// (DEC-136).

const (
	// reminderPushKind is the push reminder's kind in its key and on the
	// date page.
	reminderPushKind = "push"
	// pushReminderLead is how long before the start it is due.
	pushReminderLead = 2 * time.Hour
	// pushReminderMaxMove is how much earlier quiet hours may move it.
	pushReminderMaxMove = 18 * time.Hour
)

// Why a push reminder row didn't go.
const (
	pushWhyQuietHours  = "outside quiet hours"
	pushWhyDateChanged = "date cancelled or moved"
	pushWhyReminderOff = "push reminder turned off"
)

// pushReminderKey is a date's push reminder key. It names the start, so a
// moved date gets a new reminder (and the old one's rows send nothing);
// 120 keeps the key in the email reminders' four-part shape.
func pushReminderKey(occID uint, start time.Time) string {
	return reminderPushKind + ":" + historyID(occID) + ":" + strconv.FormatInt(start.Unix(), 10) + ":" + strconv.Itoa(int(pushReminderLead/time.Minute))
}

// pushReminderStart is the start a push reminder key names.
func pushReminderStart(key *string) (int64, bool) {
	if key == nil {
		return 0, false
	}
	parts := strings.Split(*key, ":")
	if len(parts) != 4 || parts[0] != reminderPushKind {
		return 0, false
	}
	start, err := strconv.ParseInt(parts[2], 10, 64)
	return start, err == nil
}

// pushReminderPlan is when one person's push reminder goes: at At, or
// skipped (quiet hours leave no time within 18 hours before it is due).
type pushReminderPlan struct {
	UserID uint
	At     time.Time
	Skip   bool
}

// planPushReminder is the out-of-window rule for one person in zone loc,
// at the sweep time now: the due time if it is inside their window, else
// the last allowed minute before it (push.Window.LastAllowed, at most 18
// hours earlier). A time already past goes now if their window is open now
// and it passed no longer ago than the sweep's lookback; with the window
// shut it is skipped, never sent at night. ok is false when nothing is
// decided yet: the time is beyond the sweep's lookahead (a later sweep
// plans it), or it passed too long ago, as an email reminder missed by a
// long outage is. A skip is recorded only once the reminder is near due.
func planPushReminder(w push.Window, due time.Time, loc *time.Location, now time.Time) (at time.Time, skip, ok bool) {
	at, allowed := w.LastAllowed(due, "", loc, pushReminderMaxMove)
	switch {
	case !allowed && due.After(now.Add(reminderLookahead)):
		return time.Time{}, false, false
	case !allowed:
		return time.Time{}, true, true
	case at.After(now.Add(reminderLookahead)):
		return time.Time{}, false, false
	case !at.Before(now):
		return at, false, true
	}
	if inside, _ := w.Check(now, "", loc); !inside {
		return time.Time{}, true, true
	}
	if now.Sub(at) > reminderLookback {
		return time.Time{}, false, false
	}
	return now, false, true
}

// sweepPushReminders is the push part of reminders.sweep. One query reads,
// for every scheduled date of an event with the switch on whose push
// reminder is due between six hours back and 18 hours plus the lookahead
// ahead (the most quiet hours can move it earlier), each going registrant
// (a household person's reaches the account holder, as the email does)
// who is active, has a device, and has no row yet on that date's push
// reminder for its current start. So a date whose people are all planned
// costs nothing on later sweeps; what is left each hour is the people not
// yet due (planned later), the ones without push on for event updates
// (never given a row), and late sign-ups, who are planned the hour they
// appear. Push consent is read once for all of them, and the church's
// zone once.
func (a *App) sweepPushReminders(ctx context.Context, now time.Time) error {
	if !a.pushEnabled() {
		return nil
	}
	db := a.DB.WithContext(ctx)
	cands, err := pushReminderCandidates(db, now)
	if err != nil {
		return err
	}
	if len(cands) == 0 {
		return nil
	}
	ids := make([]uint, 0, len(cands))
	for _, c := range cands {
		ids = append(ids, c.UserID)
	}
	ready, err := pushReady(db, dedupeIDs(ids), notify.CatEvents)
	if err != nil {
		return err
	}
	church := churchZone(db)
	win := a.pushQuietHours()
	type date struct {
		occ   models.EventOccurrence
		plans []pushReminderPlan
	}
	var dates []*date
	for _, c := range cands {
		if !ready[c.UserID] {
			continue
		}
		loc, err := events.LoadZone(c.Timezone)
		if err != nil {
			loc = church
		}
		at, skip, ok := planPushReminder(win, c.StartsAt.Add(-pushReminderLead), loc, now)
		if !ok {
			continue
		}
		if len(dates) == 0 || dates[len(dates)-1].occ.ID != c.OccurrenceID {
			dates = append(dates, &date{occ: models.EventOccurrence{ID: c.OccurrenceID, StartsAt: c.StartsAt}})
		}
		d := dates[len(dates)-1]
		d.plans = append(d.plans, pushReminderPlan{UserID: c.UserID, At: at, Skip: skip})
	}
	for _, d := range dates {
		if err := writePushReminderPlans(db, &d.occ, d.plans); err != nil {
			return err
		}
	}
	return nil
}

// writePushReminderPlans writes one date's message (once), the planned
// people's rows (once each) and their jobs, in one transaction.
func writePushReminderPlans(db *gorm.DB, occ *models.EventOccurrence, plans []pushReminderPlan) error {
	return db.Transaction(func(tx *gorm.DB) error {
		id, err := writePushReminder(tx, occ)
		if err != nil {
			return err
		}
		rows := make([]models.MessageRecipient, 0, len(plans))
		at := map[uint]time.Time{}
		for _, p := range plans {
			row := models.MessageRecipient{MessageID: id, UserID: p.UserID, Channel: models.DeliveryPush, Status: models.RecipientQueued}
			if p.Skip {
				row.Status, row.Reason = models.RecipientSuppressed, pushWhyQuietHours
			} else {
				at[p.UserID] = p.At
			}
			rows = append(rows, row)
		}
		// A person already planned keeps their row (and its job). A date
		// cancelled and then restored at the same start keeps its key, so
		// a row its cancelled job suppressed ("date cancelled or moved")
		// stays: that person gets no push reminder for it. The date has no
		// restore count to put in the key, and it needs a cancel and a
		// restore inside the last 20 hours before the date.
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(&rows, 500).Error; err != nil {
			return err
		}
		return enqueuePushReminderJobs(tx, id, at)
	})
}

// writePushReminder writes the date's push reminder message for its
// start, or finds the one already written, and returns its id. Its channel
// is push. It holds only push rows, which never settle it: it is sent from
// the start, and its sent_at is the first push that went
// (pushReminderRecorder).
func writePushReminder(tx *gorm.DB, occ *models.EventOccurrence) (uint, error) {
	key := pushReminderKey(occ.ID, occ.StartsAt)
	due := occ.StartsAt.Add(-pushReminderLead)
	aud, _ := json.Marshal(audienceJSON{Type: policy.AudienceOccurrence, OccurrenceID: occ.ID, Statuses: []string{models.RegGoing}})
	// What the history page shows of it: the lock screen's words, with
	// the time on each person's own clock and never the place (DEC-136).
	clean := content.SanitizeHTML("<p>Starts at {{event_time}}, on each person's own clock.</p>")
	m := models.Message{Channel: models.DeliveryPush, Status: models.MessageSent, Category: models.MessageCategoryReminder,
		Subject: "Reminder: {{event_title}}", BodyHTML: clean, BodyText: content.PlainText(clean), Audience: string(aud),
		ScheduledAt: &due, OccurrenceID: &occ.ID, ReminderKey: &key}
	res := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "reminder_key"}}, TargetWhere: clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "reminder_key IS NOT NULL"}}}, DoNothing: true}).Create(&m)
	if res.Error != nil {
		return 0, res.Error
	}
	if res.RowsAffected == 0 {
		if err := tx.Select("id").Where("reminder_key = ?", key).First(&m).Error; err != nil {
			return 0, err
		}
	}
	return m.ID, nil
}

// enqueuePushReminderJobs queues one push.deliver per device of each
// queued push row of the message whose person is planned in at, due at
// their time. The key (push:mr:<row>:d:<device>) is the announcements'
// one, so a row's device is sent once however often the sweep runs.
func enqueuePushReminderJobs(tx *gorm.DB, messageID uint, at map[uint]time.Time) error {
	if len(at) == 0 {
		return nil
	}
	people := make([]uint, 0, len(at))
	for id := range at {
		people = append(people, id)
	}
	var pairs []struct {
		RecipientID uint
		UserID      uint
		DeviceID    uint
	}
	if err := tx.Raw(`SELECT r.id AS recipient_id, r.user_id, d.id AS device_id FROM message_recipients r
		JOIN push_subscriptions d ON d.user_id = r.user_id
		WHERE r.message_id = ? AND r.channel = ? AND r.status = ? AND r.user_id IN ? ORDER BY r.id, d.id`,
		messageID, models.DeliveryPush, models.RecipientQueued, people).Scan(&pairs).Error; err != nil {
		return err
	}
	specs := make([]jobs.Spec, 0, len(pairs))
	for _, p := range pairs {
		specs = append(specs, jobs.Spec{Kind: kindPushDeliver, Payload: pushJob{MessageRecipientID: p.RecipientID, DeviceID: p.DeviceID, PlannedAt: at[p.UserID].Unix()}, RunAt: at[p.UserID],
			UniqueKey: "push:mr:" + historyID(p.RecipientID) + ":d:" + historyID(p.DeviceID), MaxAttempts: pushDeliverAttempts})
	}
	if _, err := jobs.EnqueueMany(tx, specs); err != nil {
		return err
	}
	// A queued row whose devices are all gone by now, as for an
	// announcement, says so at once rather than waiting for the reaper.
	return tx.Exec(`UPDATE message_recipients r SET status = ?, reason = ?, updated_at = now()
		WHERE r.message_id = ? AND r.channel = ? AND r.status = ? AND r.user_id IN ?
		AND NOT EXISTS (SELECT 1 FROM push_subscriptions d WHERE d.user_id = r.user_id)`,
		models.RecipientSuppressed, pushWhyGone, messageID, models.DeliveryPush, models.RecipientQueued, people).Error
}

// pushReminderRecorder records a push reminder row as an announcement's
// is, and stamps the message's sent_at with the first push that went.
func pushReminderRecorder(row models.MessageRecipient) pushRecorder {
	rec := messagePushRecorder(row.ID)
	return func(db *gorm.DB, status, reason string, now time.Time) error {
		if err := rec(db, status, reason, now); err != nil {
			return err
		}
		if status != models.RecipientSent {
			return nil
		}
		return db.Model(&models.Message{}).Where("id = ? AND sent_at IS NULL", row.MessageID).Update("sent_at", now.UTC()).Error
	}
}

// pushReminderGrace is how far into quiet hours a push reminder may still
// go past the time the sweep planned for it: a 7:59 PM push whose job runs
// a minute late still goes; a backlog that runs it at 8:30 PM doesn't.
const pushReminderGrace = 15 * time.Minute

// deliverReminderPush sends one push reminder row to one device, once
// deliverMessagePush has checked the church's switch, the account, push
// consent for event updates and the device. It checks again that the date
// is still scheduled at the start the key names and not started, that the
// event's switch is still on, that the person is still going, and that
// they may still see the date (M1.13), as an email reminder does at send.
// The sweep planned it inside the person's window; if the window has shut
// by the time it runs, more than 15 minutes past that plan (plannedAt,
// Unix seconds), it is skipped as "outside quiet hours". The hourly limit
// never stops a reminder, though a sent one counts toward it (DEC-126).
func (a *App) deliverReminderPush(ctx context.Context, row *models.MessageRecipient, u *models.User, dev models.PushSubscription, m *models.Message, plannedAt int64) error {
	db := a.DB.WithContext(ctx)
	rec := pushReminderRecorder(*row)
	record := func(status, reason string) error { return rec(db, status, reason, a.pushNow()) }
	now := a.pushNow()
	start, ok := pushReminderStart(m.ReminderKey)
	if !ok || m.OccurrenceID == nil {
		return record(models.RecipientSuppressed, pushWhyStale)
	}
	var occ models.EventOccurrence
	if err := db.First(&occ, *m.OccurrenceID).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return record(models.RecipientSuppressed, pushWhyDateChanged)
	} else if err != nil {
		return err
	}
	if occ.Status != models.OccurrenceScheduled || occ.StartsAt.Unix() != start {
		return record(models.RecipientSuppressed, pushWhyDateChanged)
	}
	if !occ.StartsAt.After(now) {
		return record(models.RecipientSuppressed, pushWhyStale)
	}
	var ev models.Event
	if err := db.First(&ev, occ.EventID).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return record(models.RecipientSuppressed, pushWhyDateChanged)
	} else if err != nil {
		return err
	}
	if !ev.ReminderPush {
		return record(models.RecipientSuppressed, pushWhyReminderOff)
	}
	var going int64
	if err := db.Model(&models.Registration{}).Where("occurrence_id = ? AND status = ? AND COALESCE(user_id, registered_by) = ?", occ.ID, models.RegGoing, u.ID).
		Count(&going).Error; err != nil {
		return err
	}
	if going == 0 {
		return record(models.RecipientSuppressed, skipNoLongerGoing)
	}
	can, err := a.reminderReaders(db, occ.ID, []uint{u.ID}, now)
	if err != nil {
		return err
	}
	if !can[u.ID] {
		return record(models.RecipientSuppressed, skipCantSeeDate)
	}
	loc := zoneOrChurch(db, u.Timezone)
	if inside, _ := a.pushQuietHours().Check(now, "", loc); !inside && now.After(time.Unix(plannedAt, 0).Add(pushReminderGrace)) {
		return record(models.RecipientSuppressed, pushWhyQuietHours)
	}
	title := ev.Title
	if occ.TitleOverride != nil {
		title = *occ.TitleOverride
	}
	head, text := reminderPushText(title, occ.StartsAt, loc, now)
	what := fmt.Sprintf("reminder %d recipient %d", m.ID, row.ID)
	body, err := push.MarshalPayload(push.NewPayload(push.Notification{
		Title: head, Body: text, Navigate: a.emailLink(occurrenceDatePath(ev.ID, occ.OriginalDate)), Tag: "r" + historyID(occ.ID),
	}))
	if err != nil {
		return record(models.RecipientFailed, pushWhyTooLarge)
	}
	// Kept by the push service until the date starts, never longer than a
	// day: a reminder for a date already begun is no use.
	ttl := min(max(occ.StartsAt.Sub(now), time.Minute), pushTTL)
	sub := push.Subscription{Endpoint: dev.Endpoint, P256dh: dev.P256dh, Auth: dev.Auth}
	opts := push.Options{TTL: ttl, Urgency: push.UrgencyNormal, Topic: "mr" + strconv.FormatUint(uint64(row.ID), 10)}
	return a.pushOutcome(ctx, what, dev, a.pushClient().Send(ctx, sub, body, opts), "", rec)
}

// reminderPushText is a push reminder's lock screen (DEC-124, DEC-136):
// "Reminder: Book night", then "Starts at 7:00 PM" on the reader's clock
// ("Starts tomorrow at 9:00 AM" when it goes the evening before). It never
// carries the place: a lock screen is public and a small group often meets
// in a home. The tap opens the date page, which shows it. It takes no
// person, so no name or badge can reach it; the title goes through
// lockScreenSafe.
func reminderPushText(title string, start time.Time, loc *time.Location, now time.Time) (string, string) {
	oneLine := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	head := clipRunes(oneLine(lockScreenSafe("Reminder: "+oneLine(title))), maxMessageSubject)
	local, today := start.In(loc), now.In(loc)
	clock := local.Format("3:04 PM")
	when := "Starts " + local.Format("Mon, Jan 2") + " at " + clock
	switch local.Format(time.DateOnly) {
	case today.Format(time.DateOnly):
		when = "Starts at " + clock
	case today.AddDate(0, 0, 1).Format(time.DateOnly):
		when = "Starts tomorrow at " + clock
	}
	return head, when
}

// pushReminderCandidate is one going person on one date the sweep may
// still have to plan.
type pushReminderCandidate struct {
	OccurrenceID uint
	StartsAt     time.Time
	UserID       uint
	Timezone     string
}

// pushReminderCandidates is sweepPushReminders' one read (see there).
func pushReminderCandidates(db *gorm.DB, now time.Time) ([]pushReminderCandidate, error) {
	var cands []pushReminderCandidate
	err := db.Raw(`SELECT DISTINCT o.id AS occurrence_id, o.starts_at, u.id AS user_id, u.timezone
		FROM event_occurrences o
		JOIN events e ON e.id = o.event_id AND e.deleted_at IS NULL
		JOIN registrations r ON r.occurrence_id = o.id AND r.status = ?
		JOIN users u ON u.id = COALESCE(r.user_id, r.registered_by) AND u.status = ?
		WHERE o.status = ? AND e.reminder_push AND o.starts_at > ? AND o.starts_at BETWEEN ? AND ?
		  AND EXISTS (SELECT 1 FROM push_subscriptions d WHERE d.user_id = u.id)
		  AND NOT EXISTS (SELECT 1 FROM messages m JOIN message_recipients mr ON mr.message_id = m.id AND mr.user_id = u.id AND mr.channel = ?
			WHERE m.reminder_key = ? || o.id || ':' || extract(epoch FROM o.starts_at)::bigint || ?)
		ORDER BY o.starts_at, o.id, u.id`,
		models.RegGoing, models.UserActive, models.OccurrenceScheduled, now,
		now.Add(pushReminderLead-reminderLookback), now.Add(pushReminderLead+pushReminderMaxMove+reminderLookahead),
		models.DeliveryPush, reminderPushKind+":", ":"+strconv.Itoa(int(pushReminderLead/time.Minute))).Scan(&cands).Error
	return cands, err
}
