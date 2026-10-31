package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"fold/models"
)

// reminderFor sends the reminder for occ as the sweep's job would at its
// time (the clock is moved to it), then runs the batches.
func reminderFor(t *testing.T, app *App, occ models.EventOccurrence, minutes int) {
	t.Helper()
	at := occ.StartsAt.Add(-time.Duration(minutes) * time.Minute)
	if err := app.sendReminder(context.Background(), reminderJob{OccurrenceID: occ.ID, StartsAt: occ.StartsAt.Unix(), Minutes: minutes}, at); err != nil {
		t.Fatal(err)
	}
	runMessageJobs(t, app)
}

func reminderMails(t *testing.T, app *App, to string) []models.MailOutbox {
	t.Helper()
	var rows []models.MailOutbox
	app.DB.Where("to_address = ?", to).Order("id").Find(&rows)
	return rows
}

// The sweep queues one reminder per date at its exact time, only for an
// event with reminders on and someone going, however often it runs.
func TestReminderSweepQueuesOncePerDate(t *testing.T) {
	f := newComposeFixture(t)
	f.app.DB.Create(&models.Registration{OccurrenceID: f.groupOcc.ID, UserID: &f.teen.ID, Status: models.RegGoing})
	due := f.groupOcc.StartsAt.Add(-24 * time.Hour)
	for _, now := range []time.Time{due.Add(-90 * time.Minute), due.Add(-30 * time.Minute), due.Add(5 * time.Minute)} {
		if err := f.app.sweepReminders(context.Background(), now); err != nil {
			t.Fatal(err)
		}
	}
	var queued []models.Job
	f.app.DB.Where("kind = ? AND (payload->>'occurrenceId')::bigint = ?", kindReminderSend, f.groupOcc.ID).Find(&queued)
	if len(queued) != 1 || !queued[0].RunAt.Equal(due.UTC()) && queued[0].RunAt.Sub(due).Abs() > time.Second {
		t.Fatalf("queued: %+v", queued)
	}
	// Off means none.
	f.app.DB.Exec("DELETE FROM jobs")
	f.app.DB.Model(&models.Event{}).Where("id = ?", f.groupEvent.ID).Update("reminder_email_minutes", 0)
	if err := f.app.sweepReminders(context.Background(), due.Add(-30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var n int64
	f.app.DB.Model(&models.Job{}).Where("kind = ? AND (payload->>'occurrenceId')::bigint = ?", kindReminderSend, f.groupOcc.ID).Count(&n)
	if n != 0 {
		t.Fatal("an event with reminders off was queued")
	}
}

// Each going registrant gets one reminder, from the church, with their own
// name and date page, following Event updates; waitlisted people hear
// they're still waiting; nobody gets a bell row (M3.6 acceptance).
func TestReminderSendsOnceToGoingAndWaitlist(t *testing.T) {
	f := newComposeFixture(t)
	f.app.DB.Model(&models.Event{}).Where("id = ?", f.groupEvent.ID).Update("waitlist_enabled", true)
	waiting := newTestUser(t, f.app, "waiting", "password1234", models.RoleMember)
	declined := newTestUser(t, f.app, "declined", "password1234", models.RoleMember)
	for u, s := range map[*models.User]string{f.teen: models.RegGoing, f.opted: models.RegGoing, waiting: models.RegWaitlisted, declined: models.RegDeclined} {
		f.app.DB.Create(&models.Registration{OccurrenceID: f.groupOcc.ID, UserID: &u.ID, Status: s})
	}
	var notesBefore int64
	f.app.DB.Model(&models.Notification{}).Count(&notesBefore)
	reminderFor(t, f.app, f.groupOcc, 1440)
	reminderFor(t, f.app, f.groupOcc, 1440) // a second run sends nothing more
	got := reminderMails(t, f.app, f.teen.Email)
	if len(got) != 1 || !strings.HasPrefix(got[0].Subject, "Reminder: Book night on ") ||
		!strings.Contains(got[0].Body, "Hi "+firstName(f.teen.PublicName())) ||
		!strings.Contains(got[0].HTMLBody, occurrenceDatePath(f.groupEvent.ID, f.groupOcc.OriginalDate)) ||
		!strings.Contains(got[0].Body, "You're getting event updates") || strings.Contains(got[0].FromAddress, " via ") {
		t.Fatalf("going: %+v", got)
	}
	// Opting out of messages from leaders doesn't stop reminders; the
	// opted fixture turned off messages, not event updates.
	if len(reminderMails(t, f.app, f.opted.Email)) != 1 {
		t.Fatal("a reminder follows Event updates, not Messages from leaders")
	}
	if w := reminderMails(t, f.app, waiting.Email); len(w) != 1 || !strings.HasPrefix(w[0].Subject, "Still on the waitlist: Book night") {
		t.Fatalf("waitlist: %+v", w)
	}
	if len(reminderMails(t, f.app, declined.Email)) != 0 {
		t.Fatal("someone who declined got a reminder")
	}
	var notesAfter, reminders int64
	f.app.DB.Model(&models.Notification{}).Count(&notesAfter)
	f.app.DB.Model(&models.Message{}).Where("category = ? AND occurrence_id = ?", models.MessageCategoryReminder, f.groupOcc.ID).Count(&reminders)
	if notesAfter != notesBefore || reminders != 2 {
		t.Fatalf("bell rows %d -> %d, reminder messages %d", notesBefore, notesAfter, reminders)
	}
}

// Turning off Event updates stops reminders.
func TestReminderFollowsEventUpdates(t *testing.T) {
	f := newComposeFixture(t)
	f.app.DB.Create(&models.Registration{OccurrenceID: f.groupOcc.ID, UserID: &f.teen.ID, Status: models.RegGoing})
	if w := loginAPI(t, f.app, "teen", "password1234").do("PUT", "/api/v1/me/notification-settings", map[string]any{"category": "events", "off": true}, true); w.Code != http.StatusOK {
		t.Fatalf("opt out: %d", w.Code)
	}
	reminderFor(t, f.app, f.groupOcc, 1440)
	if len(reminderMails(t, f.app, f.teen.Email)) != 0 {
		t.Fatal("someone who turned off event updates got a reminder")
	}
	var r models.MessageRecipient
	f.app.DB.Where("user_id = ?", f.teen.ID).First(&r)
	if r.Status != models.RecipientSuppressed || r.Reason != skipEventsOff {
		t.Fatalf("row: %+v", r)
	}
}

// A cancelled date, a moved date, or a changed reminder time sends
// nothing from the old job (M3.6 acceptance).
func TestReminderSkipsStaleJobs(t *testing.T) {
	f := newComposeFixture(t)
	f.app.DB.Create(&models.Registration{OccurrenceID: f.groupOcc.ID, UserID: &f.teen.ID, Status: models.RegGoing})
	job := reminderJob{OccurrenceID: f.groupOcc.ID, StartsAt: f.groupOcc.StartsAt.Unix(), Minutes: 1440}
	at := f.groupOcc.StartsAt.Add(-24 * time.Hour)
	for name, change := range map[string]func(){
		"reminder time changed": func() {
			f.app.DB.Model(&models.Event{}).Where("id = ?", f.groupEvent.ID).Update("reminder_email_minutes", 120)
		},
		"date moved": func() {
			f.app.DB.Model(&models.EventOccurrence{}).Where("id = ?", f.groupOcc.ID).Update("starts_at", f.groupOcc.StartsAt.Add(time.Hour))
		},
		"date cancelled": func() {
			f.app.DB.Model(&models.EventOccurrence{}).Where("id = ?", f.groupOcc.ID).Update("status", models.OccurrenceCancelled)
		},
	} {
		f.app.DB.Model(&models.Event{}).Where("id = ?", f.groupEvent.ID).Update("reminder_email_minutes", 1440)
		f.app.DB.Model(&models.EventOccurrence{}).Where("id = ?", f.groupOcc.ID).Updates(map[string]any{"starts_at": f.groupOcc.StartsAt, "status": models.OccurrenceScheduled})
		change()
		if err := f.app.sendReminder(context.Background(), job, at); err != nil {
			t.Fatal(err)
		}
		runMessageJobs(t, f.app)
		if n := len(reminderMails(t, f.app, f.teen.Email)); n != 0 {
			t.Fatalf("%s: %d reminders", name, n)
		}
	}
}

// A registrant who can no longer see the date gets no reminder.
func TestReminderRechecksWhoCanSeeTheDate(t *testing.T) {
	f := newComposeFixture(t)
	outsider := newTestUser(t, f.app, "outsider", "password1234", models.RoleMember)
	f.app.DB.Create(&models.Registration{OccurrenceID: f.groupOcc.ID, UserID: &outsider.ID, Status: models.RegGoing})
	f.app.DB.Create(&models.Registration{OccurrenceID: f.groupOcc.ID, UserID: &f.teen.ID, Status: models.RegGoing})
	// Made invite-only behind the registrations' back: the member still sees
	// it, the outsider doesn't.
	f.app.DB.Model(&models.Event{}).Where("id = ?", f.groupEvent.ID).Update("visibility", models.VisibilityInviteOnly)
	f.app.DB.Model(&models.Group{}).Where("id = ?", f.group.ID).Update("visibility", models.VisibilityInviteOnly)
	reminderFor(t, f.app, f.groupOcc, 1440)
	if len(reminderMails(t, f.app, outsider.Email)) != 0 || len(reminderMails(t, f.app, f.teen.Email)) != 1 {
		t.Fatalf("outsider %d, member %d", len(reminderMails(t, f.app, outsider.Email)), len(reminderMails(t, f.app, f.teen.Email)))
	}
	var r models.MessageRecipient
	f.app.DB.Where("user_id = ?", outsider.ID).First(&r)
	if r.Reason != skipCantSeeDate {
		t.Fatalf("outsider row: %+v", r)
	}
}

// Leaders choose the reminder time from the list (DEC-133).
func TestEventReminderTime(t *testing.T) {
	f := newComposeFixture(t)
	path := "/api/v1/events/" + itoa(f.groupEvent.ID)
	if w := f.leader.do("PUT", path, map[string]any{"reminderEmailMinutes": 30}, true); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "event_reminder_time") {
		t.Fatalf("30 minutes: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Event eventJSON `json:"event"`
	}
	w := f.leader.do("PUT", path, map[string]any{"reminderEmailMinutes": 2880}, true)
	decodeJSON(t, w, &out)
	if w.Code != http.StatusOK || out.Event.ReminderEmailMinutes != 2880 {
		t.Fatalf("2 days: %d %s", w.Code, w.Body.String())
	}
	if f.groupEvent.ReminderEmailMinutes != 1440 {
		t.Fatalf("default: %d", f.groupEvent.ReminderEmailMinutes)
	}
}

// "Off" chosen at creation stays off; no choice means 1 day.
func TestNewEventReminderDefaults(t *testing.T) {
	f := newComposeFixture(t)
	create := func(body map[string]any) eventJSON {
		body["title"], body["startsLocal"], body["durationMinutes"], body["tzid"] = "New", feedStart(4).Format("2006-01-02T15:04"), 60, "UTC"
		var out struct {
			Event eventJSON `json:"event"`
		}
		w := f.leader.do("POST", "/api/v1/groups/"+itoa(f.group.ID)+"/events", body, true)
		if w.Code != http.StatusCreated {
			t.Fatalf("create: %d %s", w.Code, w.Body.String())
		}
		decodeJSON(t, w, &out)
		return out.Event
	}
	if ev := create(map[string]any{"reminderEmailMinutes": 0}); ev.ReminderEmailMinutes != 0 {
		t.Fatalf("off at creation: %d", ev.ReminderEmailMinutes)
	}
	var stored models.Event
	f.app.DB.Where("title = ?", "New").Order("id").First(&stored)
	if stored.ReminderEmailMinutes != 0 {
		t.Fatalf("stored: %d", stored.ReminderEmailMinutes)
	}
	if ev := create(map[string]any{}); ev.ReminderEmailMinutes != 1440 {
		t.Fatalf("default: %d", ev.ReminderEmailMinutes)
	}
}

// Nobody going means no reminder message; a guest who registered gets
// one (PLAN 7.4); moving a date a little later after its reminder went
// sends no second one.
func TestReminderGuestsEmptyDatesAndMoves(t *testing.T) {
	f := newComposeFixture(t)
	reminderFor(t, f.app, f.groupOcc, 1440)
	var n int64
	f.app.DB.Model(&models.Message{}).Where("category = ?", models.MessageCategoryReminder).Count(&n)
	if n != 0 {
		t.Fatalf("a date with nobody going made %d reminders", n)
	}
	guest := newTestUser(t, f.app, "guest", "password1234", models.RoleMember)
	f.app.DB.Model(guest).Update("status", models.UserGuest)
	f.app.DB.Create(&models.Registration{OccurrenceID: f.groupOcc.ID, UserID: &guest.ID, Status: models.RegGoing})
	reminderFor(t, f.app, f.groupOcc, 1440)
	if len(reminderMails(t, f.app, guest.Email)) != 1 {
		t.Fatal("a guest registrant gets the reminder for their event")
	}
	moved := f.groupOcc
	moved.StartsAt = moved.StartsAt.Add(time.Hour)
	f.app.DB.Model(&models.EventOccurrence{}).Where("id = ?", moved.ID).Update("starts_at", moved.StartsAt)
	reminderFor(t, f.app, moved, 1440)
	if got := len(reminderMails(t, f.app, guest.Email)); got != 1 {
		t.Fatalf("moving the date an hour later re-reminded: %d", got)
	}
}

// Inside two hours the waitlist note promises no email about a spot.
func TestReminderWaitlistCopyNearTheStart(t *testing.T) {
	if _, body := reminderText(reminderWaitlist, "", 120); strings.Contains(body, "we'll let you know") {
		t.Fatalf("2-hour note: %s", body)
	}
	if _, body := reminderText(reminderWaitlist, "Hall {{first_name}}", 1440); !strings.Contains(body, "we'll let you know") || strings.Contains(body, "{{first_name}}</p>") && strings.Contains(body, "Hall {{") {
		t.Fatalf("1-day note: %s", body)
	}
}

// The daily reaper leaves reminders to their own job.
func TestReapLeavesRemindersAlone(t *testing.T) {
	f := newComposeFixture(t)
	old := time.Now().Add(-2 * time.Hour)
	key := "going:1:1:1440"
	r := models.Message{Status: models.MessageScheduled, Category: models.MessageCategoryReminder, Subject: "s", Audience: "{}", ScheduledAt: &old, ReminderKey: &key}
	f.app.DB.Create(&r)
	if err := f.app.reapMessages(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	f.app.DB.First(&r, r.ID)
	if r.Status != models.MessageScheduled {
		t.Fatalf("reaped a reminder: %+v", r)
	}
}

// A guest sees what a signed-out visitor sees: once their date isn't
// public, no reminder (C5: a guest is never loaded as an actor).
func TestReminderGuestOnlyForPublicDates(t *testing.T) {
	f := newComposeFixture(t)
	guest := newTestUser(t, f.app, "guest", "password1234", models.RoleMember)
	f.app.DB.Model(guest).Update("status", models.UserGuest)
	f.app.DB.Create(&models.Registration{OccurrenceID: f.groupOcc.ID, UserID: &guest.ID, Status: models.RegGoing})
	f.app.DB.Model(&models.Event{}).Where("id = ?", f.groupEvent.ID).Update("visibility", models.VisibilityPrivate)
	reminderFor(t, f.app, f.groupOcc, 1440)
	if len(reminderMails(t, f.app, guest.Email)) != 0 {
		t.Fatal("a guest got a reminder for a private date")
	}
	var r models.MessageRecipient
	f.app.DB.Where("user_id = ?", guest.ID).First(&r)
	if r.Reason != skipCantSeeDate {
		t.Fatalf("guest row: %+v", r)
	}
}

// A changed reminder time sends the new reminder; the leader asked for it.
func TestReminderShortenedTimeSendsAgain(t *testing.T) {
	f := newComposeFixture(t)
	f.app.DB.Create(&models.Registration{OccurrenceID: f.groupOcc.ID, UserID: &f.teen.ID, Status: models.RegGoing})
	reminderFor(t, f.app, f.groupOcc, 1440)
	f.app.DB.Model(&models.Event{}).Where("id = ?", f.groupEvent.ID).Update("reminder_email_minutes", 120)
	reminderFor(t, f.app, f.groupOcc, 120)
	if got := len(reminderMails(t, f.app, f.teen.Email)); got != 2 {
		t.Fatalf("reminders after shortening: %d", got)
	}
}
