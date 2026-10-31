package main

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"strconv"
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
	// process was restarting still goes.
	reminderLookahead = 2 * time.Hour
	reminderLookback  = 15 * time.Minute
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
// reminder falls between a little before now and two hours ahead, with
// someone going or waitlisted. The job key names the date, its start and
// the reminder time, so the hourly overlap and restarts queue nothing
// twice, and a date that moves gets a new job (the old one finds the start
// changed and does nothing).
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
		  AND o.starts_at > ?
		  AND o.starts_at - make_interval(mins => e.reminder_email_minutes::int) BETWEEN ? AND ?
		  AND EXISTS (SELECT 1 FROM registrations r WHERE r.occurrence_id = o.id AND r.status IN ?)`,
		models.OccurrenceScheduled, now, now.Add(-reminderLookback), now.Add(reminderLookahead),
		[]string{models.RegGoing, models.RegWaitlisted}).Scan(&rows).Error; err != nil {
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
	status := models.RegGoing
	if kind == reminderWaitlist {
		status = models.RegWaitlisted
	}
	aud, _ := json.Marshal(audienceJSON{Type: policy.AudienceOccurrence, OccurrenceID: occ.ID, Statuses: []string{status}})
	subject, body := reminderText(kind, occurrencePlace(ev, occ))
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
func reminderText(kind, place string) (string, string) {
	where := ""
	if place != "" {
		where = "<p>Where: " + html.EscapeString(place) + "</p>"
	}
	button := `<p><a class="fold-button" href="{{manage_link}}">See the details</a></p>`
	if kind == reminderWaitlist {
		return "Still on the waitlist: {{event_title}}, {{event_time}}",
			"<p>Hi {{first_name}},</p><p>{{event_title}} on {{event_time}} is still full, so you're still on the waitlist. We'll email you right away if a spot opens.</p>" + where + button
	}
	return "Reminder: {{event_title}}, {{event_time}}",
		"<p>Hi {{first_name}},</p><p>This is a reminder that you're going to <strong>{{event_title}}</strong> on {{event_time}}.</p>" + where + button +
			"<p>Can't make it? Change your RSVP on the same page, so someone else can have your spot.</p>"
}

// skipCantSeeDate: a registrant who can no longer see the date (it was
// made more private, or an exact-date invitation ended) gets no reminder.
const skipCantSeeDate = "can't see this date anymore"

// reminderReaders filters a reminder's readers by who may still see the
// date (M1.13: the date's effective visibility, or a live exact-date
// invitation), checked at send time. It returns the ids who may.
func (a *App) reminderReaders(db *gorm.DB, occID uint, ids []uint, now time.Time) (map[uint]bool, error) {
	var occ models.EventOccurrence
	if err := db.First(&occ, occID).Error; err != nil {
		return nil, err
	}
	var users []models.User
	if err := db.Where("id IN ?", ids).Find(&users).Error; err != nil {
		return nil, err
	}
	out := map[uint]bool{}
	for i := range users {
		u := &users[i]
		vs, err := a.dateCandidates(u, a.DB.Where("id = ?", occ.EventID))
		if err != nil {
			return nil, err
		}
		if ev := vs.ByID[occ.EventID]; ev != nil && vs.canSeeDate(u, ev, &occ) {
			out[u.ID] = true
			continue
		}
		scoped, err := a.scopedOccurrenceReads(u, a.DB.Model(&models.EventOccurrence{}).Where("id = ?", occ.ID), now)
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
