package main

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"strconv"
	"strings"
	"time"

	"fold/internal/content"
	"fold/internal/jobs"
	"fold/internal/notify"
	"fold/internal/policy"
	"fold/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Email reminders (M3.6, PLAN 7.7, DEC-133). Each event says how long
// before each date its reminder goes. An hourly sweep queues one
// reminder.send job per date at its exact time; that job writes the
// reminder as a message with no author (category reminder, one per date,
// time and kind by reminder_key) and hands it to the M3.5 pipeline, which
// sends it to the date's going registrants, and a "still on the waitlist"
// note to its waitlist. Reminders follow the Event updates switch
// (category:events) and never write a bell row (DEC-44).

const (
	kindReminderSweep = "reminders.sweep"
	kindReminderSend  = "reminder.send"
	// The sweep looks this far ahead, more than an hour so a late wake-up
	// misses nothing, and this far back, so a reminder due while the
	// process was down for a few hours, or for an event created after its
	// reminder time, still goes as soon as it can (before the date starts).
	reminderLookahead = 2 * time.Hour
	reminderLookback  = 6 * time.Hour
	// longestReminder bounds the sweep's scan to dates it could reach.
	longestReminder = 10080 * time.Minute
)

// Reminder times a leader can choose (DEC-133), in minutes before the
// date; 0 is off.
var reminderChoices = []int{0, 120, 1440, 2880, 10080}

const errReminderTime = "reminderEmailMinutes must be 0 (off), 120, 1440, 2880 or 10080"

func validReminderMinutes(m int) bool {
	for _, c := range reminderChoices {
		if c == m {
			return true
		}
	}
	return false
}

// Who a reminder is for.
const (
	reminderGoing    = "going"
	reminderWaitlist = "waitlist"
)

type reminderJob struct {
	OccurrenceID uint  `json:"occurrenceId"`
	StartsAt     int64 `json:"startsAt"`
	Minutes      int   `json:"minutes"`
}

func (a *App) reminderKinds() []jobs.Kind {
	return []jobs.Kind{{Name: kindReminderSend, Label: "Sending an event reminder", MaxAttempts: jobs.DefaultMaxAttempts,
		Run: func(ctx context.Context, payload json.RawMessage) error {
			var p reminderJob
			if err := json.Unmarshal(payload, &p); err != nil {
				return err
			}
			return a.sendReminder(ctx, p, time.Now())
		}}}
}

// hourlyKinds are queued once an hour (jobs_daily.go).
func (a *App) hourlyKinds() []jobs.Kind {
	return []jobs.Kind{{Name: kindReminderSweep, Label: "Lining up event reminders", MaxAttempts: jobs.DefaultMaxAttempts,
		Run: func(ctx context.Context, _ json.RawMessage) error { return a.sweepReminders(ctx, time.Now()) }}}
}

// sweepReminders queues reminder.send for each scheduled date whose
// reminder falls between six hours back and two hours ahead. Whether
// anyone is going is checked when the job runs, so someone who signs up
// after this sweep and before the reminder time still gets it. The job key
// names the date, its start and the reminder time, so the hourly overlap
// and restarts queue nothing twice, and a date that moves gets a new job
// (the old one finds the start changed and does nothing).
func (a *App) sweepReminders(ctx context.Context, now time.Time) error {
	type due struct {
		ID       uint
		StartsAt time.Time
		Minutes  int
	}
	var rows []due
	if err := a.DB.WithContext(ctx).Raw(`SELECT o.id, o.starts_at, e.reminder_email_minutes AS minutes
		FROM event_occurrences o JOIN events e ON e.id = o.event_id AND e.deleted_at IS NULL
		WHERE o.status = ? AND e.reminder_email_minutes > 0
		  AND o.starts_at > ? AND o.starts_at <= ?
		  AND o.starts_at - make_interval(mins => e.reminder_email_minutes::int) BETWEEN ? AND ?`,
		models.OccurrenceScheduled, now, now.Add(longestReminder+reminderLookahead), now.Add(-reminderLookback), now.Add(reminderLookahead)).
		Scan(&rows).Error; err != nil {
		return err
	}
	for _, r := range rows {
		at := r.StartsAt.Add(-time.Duration(r.Minutes) * time.Minute)
		if at.Before(now) {
			at = now
		}
		key := kindReminderSend + ":" + historyID(r.ID) + ":" + strconv.FormatInt(r.StartsAt.Unix(), 10) + ":" + strconv.Itoa(r.Minutes)
		if _, err := jobs.Enqueue(a.DB.WithContext(ctx), kindReminderSend, reminderJob{OccurrenceID: r.ID, StartsAt: r.StartsAt.Unix(), Minutes: r.Minutes},
			at, key, jobs.DefaultMaxAttempts); err != nil {
			return err
		}
	}
	return nil
}

// sendReminder is reminder.send. It does nothing when the date was
// cancelled or moved, or the event's reminder time changed or was turned
// off, since the job was queued (PLAN M3.6: a cancelled date cancels its
// reminders). Otherwise it writes the going reminder and, with a waitlist,
// the waitlist note, each once (reminder_key), and prepares them.
func (a *App) sendReminder(ctx context.Context, p reminderJob, now time.Time) error {
	db := a.DB.WithContext(ctx)
	var occ models.EventOccurrence
	if err := db.First(&occ, p.OccurrenceID).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	var ev models.Event
	if err := db.First(&ev, occ.EventID).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	if occ.Status != models.OccurrenceScheduled || occ.StartsAt.Unix() != p.StartsAt || ev.ReminderEmailMinutes != p.Minutes ||
		!occ.StartsAt.After(now) {
		return nil
	}
	kinds := []string{reminderGoing}
	if ev.WaitlistEnabled {
		kinds = append(kinds, reminderWaitlist)
	}
	for _, kind := range kinds {
		// Nobody with that status: no message at all.
		var n int64
		if err := db.Model(&models.Registration{}).Where("occurrence_id = ? AND status = ?", occ.ID, reminderStatus(kind)).Count(&n).Error; err != nil {
			return err
		}
		if n == 0 {
			continue
		}
		// A date moved a little later, or a reminder time shortened, after
		// its reminder went: the one already sent covers it.
		covered, err := reminderAlreadyCovers(db, occ.ID, kind, p)
		if err != nil {
			return err
		}
		if covered {
			continue
		}
		id, err := a.writeReminder(db, &ev, &occ, p, kind, now)
		if err != nil {
			return err
		}
		if id == 0 {
			continue
		}
		if err := a.prepareMessage(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// writeReminder writes one reminder message, or finds the one already
// written, and returns its id if it still needs preparing (0 if not).
func (a *App) writeReminder(db *gorm.DB, ev *models.Event, occ *models.EventOccurrence, p reminderJob, kind string, now time.Time) (uint, error) {
	key := kind + ":" + historyID(occ.ID) + ":" + strconv.FormatInt(p.StartsAt, 10) + ":" + strconv.Itoa(p.Minutes)
	aud, _ := json.Marshal(audienceJSON{Type: policy.AudienceOccurrence, OccurrenceID: occ.ID, Statuses: []string{reminderStatus(kind)}})
	subject, body := reminderText(kind, occurrencePlace(ev, occ), p.Minutes)
	clean := content.SanitizeHTML(body)
	// Due as soon as this job runs (now may be a test's clock).
	at := time.Now()
	m := models.Message{Channel: models.DeliveryEmail, Status: models.MessageScheduled, Category: models.MessageCategoryReminder,
		Subject: subject, BodyHTML: clean, BodyText: content.PlainText(clean), Audience: string(aud), ScheduledAt: &at,
		OccurrenceID: &occ.ID, ReminderKey: &key}
	res := db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "reminder_key"}}, TargetWhere: clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "reminder_key IS NOT NULL"}}}, DoNothing: true}).Create(&m)
	if res.Error != nil {
		return 0, res.Error
	}
	if res.RowsAffected == 0 {
		// Written by an earlier run: prepare it only if that run stopped
		// before sending started.
		if err := db.Select("id, status").Where("reminder_key = ?", key).First(&m).Error; err != nil {
			return 0, err
		}
		if m.Status != models.MessageScheduled {
			return 0, nil
		}
	}
	return m.ID, nil
}

// occurrencePlace is the date's place: its own, else the event's.
func occurrencePlace(ev *models.Event, occ *models.EventOccurrence) string {
	if occ.LocationOverride != nil {
		return *occ.LocationOverride
	}
	return ev.Location
}

// reminderText is a reminder's subject and body. The event, time and links
// are merge fields, filled for each reader at send time; the place is the
// date's own, which every registrant who can still see the date may see.
func reminderText(kind, place string, minutes int) (string, string) {
	where := ""
	if place != "" {
		// A place can't become a merge field.
		where = "<p>Where: " + html.EscapeString(strings.ReplaceAll(place, "{{", "{ {")) + "</p>"
	}
	button := `<p><a class="fold-button" href="{{manage_link}}">See the details</a></p>`
	if kind == reminderWaitlist {
		// Inside the last two hours the waitlist no longer moves on its own
		// (DEC-80), so the note doesn't promise a spot by email.
		promise := "If a spot opens, we'll let you know."
		if minutes <= 120 {
			promise = "The organizers can still add you if a spot opens."
		}
		return "Still on the waitlist: {{event_title}} on {{event_time}}",
			"<p>Hi {{first_name}},</p><p>{{event_title}} is on {{event_time}} and it's still full, so you're on the waitlist. " + promise + "</p>" + where + button
	}
	return "Reminder: {{event_title}} on {{event_time}}",
		"<p>Hi {{first_name}},</p><p>This is a reminder that you're going to <strong>{{event_title}}</strong> on {{event_time}}.</p>" + where + button +
			"<p>Can't make it? Change your RSVP, so someone else can have your spot.</p>"
}

// reminderStatus is the registration status a reminder kind is for.
func reminderStatus(kind string) string {
	if kind == reminderWaitlist {
		return models.RegWaitlisted
	}
	return models.RegGoing
}

// skipCantSeeDate: a registrant who can no longer see the date (it was
// made more private, or an exact-date invitation ended) gets no reminder.
const skipCantSeeDate = "can't see this date anymore"

// reminderReaders filters a reminder's readers by who may still see the
// date (M1.13: the date's effective visibility, or a live exact-date
// invitation), checked at send time. It returns the ids who may.
func (a *App) reminderReaders(db *gorm.DB, occID uint, ids []uint, now time.Time) (map[uint]bool, error) {
	var occ models.EventOccurrence
	if err := db.First(&occ, occID).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errAudienceGone
	} else if err != nil {
		return nil, err
	}
	var users []models.User
	if err := db.Where("id IN ?", ids).Find(&users).Error; err != nil {
		return nil, err
	}
	out := map[uint]bool{}
	for i := range users {
		u := &users[i]
		// A guest is never loaded as an actor (AGENTS.md, C5): they see what
		// a signed-out visitor sees, so only a public date, and hold no
		// exact-date invitations.
		viewer := u
		if u.Status == models.UserGuest {
			viewer = nil
		}
		vs, err := a.dateCandidates(viewer, db.Where("id = ?", occ.EventID))
		if err != nil {
			return nil, err
		}
		if ev := vs.ByID[occ.EventID]; ev != nil && vs.canSeeDate(viewer, ev, &occ) {
			out[u.ID] = true
			continue
		}
		if viewer == nil {
			continue
		}
		scoped, err := a.scopedOccurrenceReads(u, db.Model(&models.EventOccurrence{}).Where("id = ?", occ.ID), now)
		if err != nil {
			return nil, err
		}
		if scoped[occ.ID] != nil {
			out[u.ID] = true
		}
	}
	return out, nil
}

// messageCategory is the email settings kind a message follows: event
// updates for reminders (DEC-133), messages from leaders otherwise
// (DEC-119).
func messageCategory(m *models.Message) string {
	if m.Category == models.MessageCategoryReminder {
		return notify.CatEvents
	}
	return notify.CatMessages
}

// defaultReminderMinutes is a new event's reminder time: 1 day (DEC-133).
const defaultReminderMinutes = 1440

// createEvent inserts ev. GORM leaves a zero value out of an INSERT, so the
// column's default (1 day) would replace a leader's "Off"; an Off is
// written explicitly after.
func createEvent(tx *gorm.DB, ev *models.Event) error {
	// Create reads the column's default back into ev, so the choice is
	// kept from before it.
	off := ev.ReminderEmailMinutes == 0
	if err := tx.Create(ev).Error; err != nil {
		return err
	}
	if off {
		ev.ReminderEmailMinutes = 0
		return tx.Model(ev).Update("reminder_email_minutes", 0).Error
	}
	return nil
}

// reminderAlreadyCovers reports whether a reminder of this kind already
// went for the date at a time still inside the new reminder's lead: its
// own reminder time (from its key: kind:occ:start:minutes) is less than
// p.Minutes before the new one. A date moved an hour later sends no second
// email; a date moved a week later does, and so does a changed reminder
// time (a 1-day reminder shortened to 2 hours sends the 2-hour one too:
// the leader asked for it).
func reminderAlreadyCovers(db *gorm.DB, occID uint, kind string, p reminderJob) (bool, error) {
	var keys []string
	if err := db.Model(&models.Message{}).Where("occurrence_id = ? AND category = ? AND reminder_key LIKE ? AND status IN ?",
		occID, models.MessageCategoryReminder, kind+":%", []string{models.MessageSending, models.MessageSent}).
		Pluck("reminder_key", &keys).Error; err != nil {
		return false, err
	}
	at := p.StartsAt - int64(p.Minutes)*60
	for _, k := range keys {
		parts := strings.Split(k, ":")
		if len(parts) != 4 {
			continue
		}
		start, err1 := strconv.ParseInt(parts[2], 10, 64)
		minutes, err2 := strconv.Atoi(parts[3])
		if err1 != nil || err2 != nil || (start == p.StartsAt && minutes == p.Minutes) {
			continue
		}
		if was := start - int64(minutes)*60; at-was < int64(p.Minutes)*60 {
			return true, nil
		}
	}
	return false, nil
}
