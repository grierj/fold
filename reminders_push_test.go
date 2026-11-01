package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"fold/internal/push"
	"fold/models"
)

// pushReminderFixture is the compose fixture with the fake push service
// (quiet hours off) and the teen going to the group date with push on and
// one device.
func pushReminderFixture(t *testing.T) (composeFixture, *fakePush, testDevice) {
	t.Helper()
	f := newComposeFixture(t)
	fp := useFakePush(t, f.app)
	going(t, f, f.teen)
	pushConsentRow(t, f.app, f.teen, "all", models.ConsentOptedIn)
	return f, fp, newPushDevice(t, f.app, f.teen, "phone")
}

func going(t *testing.T, f composeFixture, u *models.User) {
	t.Helper()
	if err := f.app.DB.Create(&models.Registration{OccurrenceID: f.groupOcc.ID, UserID: &u.ID, Status: models.RegGoing}).Error; err != nil {
		t.Fatal(err)
	}
}

func sweepAt(t *testing.T, app *App, now time.Time) {
	t.Helper()
	if err := app.sweepReminders(context.Background(), now); err != nil {
		t.Fatal(err)
	}
}

// deliverAt runs every waiting push job as if the clock read at.
func deliverAt(t *testing.T, app *App, at time.Time) {
	t.Helper()
	app.pushClock = func() time.Time { return at }
	makeDue(app)
	runMessageJobs(t, app)
}

// pushReminderOf is the date's push reminder message for its start, and
// its rows by address.
func pushReminderOf(t *testing.T, app *App, occ models.EventOccurrence) (models.Message, map[string]models.MessageRecipient) {
	t.Helper()
	var m models.Message
	if err := app.DB.Where("reminder_key = ?", pushReminderKey(occ.ID, occ.StartsAt)).First(&m).Error; err != nil {
		return m, nil
	}
	return m, pushRowsOf(t, app, m.ID)
}

func moveDate(t *testing.T, app *App, occ *models.EventOccurrence, start time.Time) {
	t.Helper()
	occ.StartsAt = start
	if err := app.DB.Model(&models.EventOccurrence{}).Where("id = ?", occ.ID).Updates(map[string]any{"starts_at": start, "ends_at": start.Add(time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
}

// A going registrant with push on gets one push 2 hours before, however
// often the sweep and the job run; it is a reminder message of its own
// with one push row and no email rows, and writes no bell row (DEC-44).
func TestPushReminderGoesOnceTwoHoursBefore(t *testing.T) {
	f, fp, phone := pushReminderFixture(t)
	var notesBefore int64
	f.app.DB.Model(&models.Notification{}).Count(&notesBefore)
	due := f.groupOcc.StartsAt.Add(-2 * time.Hour)
	sweepAt(t, f.app, due.Add(-5*time.Hour)) // too early: nothing yet
	if m, _ := pushReminderOf(t, f.app, f.groupOcc); m.ID != 0 {
		t.Fatal("planned beyond the lookahead")
	}
	for _, now := range []time.Time{due.Add(-90 * time.Minute), due.Add(-30 * time.Minute)} {
		sweepAt(t, f.app, now)
	}
	var js []models.Job
	f.app.DB.Where("kind = ?", kindPushDeliver).Find(&js)
	if len(js) != 1 || js[0].RunAt.Sub(due).Abs() > time.Second {
		t.Fatalf("jobs: %+v", js)
	}
	deliverAt(t, f.app, due)
	sweepAt(t, f.app, due.Add(5*time.Minute))
	deliverAt(t, f.app, due.Add(5*time.Minute))
	if fp.count(phone.path) != 1 || fp.total() != 1 {
		t.Fatalf("pushes: %v", fp.sent)
	}
	m, rows := pushReminderOf(t, f.app, f.groupOcc)
	if m.Category != models.MessageCategoryReminder || m.Status != models.MessageSent || m.SentAt == nil || m.OccurrenceID == nil || reminderKind(&m) != reminderPushKind {
		t.Fatalf("message: %+v", m)
	}
	if len(rows) != 1 || rows[f.teen.Email].Status != models.RecipientSent || rows[f.teen.Email].Reason != "" {
		t.Fatalf("rows: %+v", rows)
	}
	var email, notesAfter int64
	f.app.DB.Model(&models.MessageRecipient{}).Where("message_id = ? AND channel <> ?", m.ID, models.DeliveryPush).Count(&email)
	f.app.DB.Model(&models.Notification{}).Count(&notesAfter)
	if email != 0 || notesAfter != notesBefore {
		t.Fatalf("email rows %d, bell rows %d -> %d", email, notesBefore, notesAfter)
	}
}

// Only going registrants who are active and have push on for event
// updates, with a device, get a push reminder; nobody else gets a row:
// not someone without push, not someone who turned push off for event
// updates, not the waitlist, not a guest.
func TestPushReminderOnlyGoingWithPushOn(t *testing.T) {
	f, fp, phone := pushReminderFixture(t)
	f.app.DB.Model(&models.Event{}).Where("id = ?", f.groupEvent.ID).Update("waitlist_enabled", true)
	noPush := newTestUser(t, f.app, "nopush", "password1234", models.RoleMember)
	newPushDevice(t, f.app, noPush, "nopush")
	eventsOff := pushMember(t, f.app, "eventsoff")
	pushConsentRow(t, f.app, eventsOff, "category:events", models.ConsentOptedOut)
	newPushDevice(t, f.app, eventsOff, "eventsoff")
	waiting := pushMember(t, f.app, "waiting")
	newPushDevice(t, f.app, waiting, "waiting")
	f.app.DB.Create(&models.Registration{OccurrenceID: f.groupOcc.ID, UserID: &waiting.ID, Status: models.RegWaitlisted})
	guest := pushMember(t, f.app, "guest")
	newPushDevice(t, f.app, guest, "guest")
	f.app.DB.Model(guest).Update("status", models.UserGuest)
	for _, u := range []*models.User{noPush, eventsOff, guest} {
		going(t, f, u)
	}
	due := f.groupOcc.StartsAt.Add(-2 * time.Hour)
	sweepAt(t, f.app, due.Add(-30*time.Minute))
	deliverAt(t, f.app, due)
	_, rows := pushReminderOf(t, f.app, f.groupOcc)
	if len(rows) != 1 || rows[f.teen.Email].Status != models.RecipientSent {
		t.Fatalf("rows: %+v", rows)
	}
	if fp.total() != 1 || fp.count(phone.path) != 1 {
		t.Fatalf("pushes: %v", fp.sent)
	}
}

// For a 9:00 AM date the push is due at 7:00 AM, outside the 8 AM to 8 PM
// window, so it goes at 7:59 PM the night before (11 hours earlier, within
// 18). Someone who signs up at 11 PM, once that time has passed, is
// skipped rather than woken: their row says "outside quiet hours".
func TestPushReminderMovesBeforeQuietHours(t *testing.T) {
	f, fp, phone := pushReminderFixture(t)
	w := push.DefaultWindow
	f.app.pushWindow = &w
	f.app.DB.Model(f.teen).Update("timezone", "America/Denver")
	day := f.groupOcc.StartsAt.AddDate(0, 0, 1)
	start := time.Date(day.Year(), day.Month(), day.Day(), 9, 0, 0, 0, denverZone)
	moveDate(t, f.app, &f.groupOcc, start)
	evening := time.Date(day.Year(), day.Month(), day.Day()-1, 19, 59, 0, 0, denverZone)
	sweepAt(t, f.app, evening.Add(-40*time.Minute))
	var js []models.Job
	f.app.DB.Where("kind = ?", kindPushDeliver).Find(&js)
	if len(js) != 1 || !js[0].RunAt.Equal(evening.UTC()) {
		t.Fatalf("jobs: %+v (want %s)", js, evening.UTC())
	}
	deliverAt(t, f.app, evening)
	if fp.count(phone.path) != 1 {
		t.Fatalf("pushes: %v", fp.sent)
	}
	late := denverMember(t, f.app, "late")
	newPushDevice(t, f.app, late, "late")
	going(t, f, late)
	sweepAt(t, f.app, evening.Add(3*time.Hour))
	sweepAt(t, f.app, start.Add(-150*time.Minute)) // 6:30 AM, the window still shut
	deliverAt(t, f.app, start.Add(-2*time.Hour))
	_, rows := pushReminderOf(t, f.app, f.groupOcc)
	if r := rows[late.Email]; r.Status != models.RecipientSuppressed || r.Reason != pushWhyQuietHours {
		t.Fatalf("late: %+v", r)
	}
	if fp.total() != 1 {
		t.Fatalf("pushes: %v", fp.sent)
	}
}

// When the last allowed minute is more than 18 hours before the due time
// it is skipped. With the default window the move is never more than 12
// hours, so this test narrows the window to 8 AM to noon: an 8:30 AM
// date's 6:30 AM push would move to 11:59 AM the day before, 18.5 hours
// earlier. Nothing is decided early, and once it is near due the row says
// "outside quiet hours".
func TestPushReminderSkippedPastEighteenHours(t *testing.T) {
	f, fp, _ := pushReminderFixture(t)
	w := push.Window{StartHour: 8, EndHour: 12}
	f.app.pushWindow = &w
	f.app.DB.Model(f.teen).Update("timezone", "America/Denver")
	day := f.groupOcc.StartsAt.AddDate(0, 0, 1)
	start := time.Date(day.Year(), day.Month(), day.Day(), 8, 30, 0, 0, denverZone)
	moveDate(t, f.app, &f.groupOcc, start)
	due := start.Add(-2 * time.Hour)
	sweepAt(t, f.app, due.Add(-19*time.Hour))
	if _, rows := pushReminderOf(t, f.app, f.groupOcc); len(rows) != 0 {
		t.Fatalf("decided early: %+v", rows)
	}
	sweepAt(t, f.app, due.Add(-time.Hour))
	deliverAt(t, f.app, due)
	_, rows := pushReminderOf(t, f.app, f.groupOcc)
	if r := rows[f.teen.Email]; r.Status != models.RecipientSuppressed || r.Reason != pushWhyQuietHours {
		t.Fatalf("row: %+v", rows)
	}
	var js int64
	f.app.DB.Model(&models.Job{}).Where("kind = ?", kindPushDeliver).Count(&js)
	if fp.total() != 0 || js != 0 {
		t.Fatalf("pushes %v, jobs %d", fp.sent, js)
	}
}

// The rule itself, on one clock (UTC) with the default window.
func TestPlanPushReminder(t *testing.T) {
	w := push.DefaultWindow
	at := func(d, h, m int) time.Time { return time.Date(2030, 1, d, h, m, 0, 0, time.UTC) }
	for _, c := range []struct {
		name     string
		due, now time.Time
		want     time.Time
		skip, ok bool
	}{
		{"inside, ahead", at(10, 17, 0), at(10, 16, 0), at(10, 17, 0), false, true},
		{"inside, beyond the lookahead", at(10, 17, 0), at(10, 14, 0), time.Time{}, false, false},
		{"inside, just passed", at(10, 17, 0), at(10, 17, 30), at(10, 17, 30), false, true},
		{"inside, passed past the lookback", at(10, 10, 0), at(10, 17, 0), time.Time{}, false, false},
		{"early morning moves to the night before", at(10, 7, 0), at(9, 19, 0), at(9, 19, 59), false, true},
		{"night before, already past and shut", at(10, 7, 0), at(9, 23, 0), time.Time{}, true, true},
	} {
		got, skip, ok := planPushReminder(w, c.due, time.UTC, c.now)
		if !got.Equal(c.want) || skip != c.skip || ok != c.ok {
			t.Errorf("%s: got %s %v %v", c.name, got, skip, ok)
		}
	}
	narrow := push.Window{StartHour: 8, EndHour: 12}
	if _, skip, ok := planPushReminder(narrow, at(10, 6, 30), time.UTC, at(9, 20, 0)); skip || ok {
		t.Errorf("a skip is decided early: %v %v", skip, ok)
	}
	if _, skip, ok := planPushReminder(narrow, at(10, 6, 30), time.UTC, at(10, 5, 0)); !skip || !ok {
		t.Errorf("a skip near due: %v %v", skip, ok)
	}
}

// Turning the event's push reminder off, or cancelling the date, cancels
// a push already queued: it sends nothing and its row says why.
func TestPushReminderCancelledBySwitchOrDate(t *testing.T) {
	for name, c := range map[string]struct {
		change func(f composeFixture)
		why    string
	}{
		"switch off": {func(f composeFixture) {
			f.app.DB.Model(&models.Event{}).Where("id = ?", f.groupEvent.ID).Update("reminder_push", false)
		}, pushWhyReminderOff},
		"date cancelled": {func(f composeFixture) {
			f.app.DB.Model(&models.EventOccurrence{}).Where("id = ?", f.groupOcc.ID).Update("status", models.OccurrenceCancelled)
		}, pushWhyDateChanged},
		"no longer going": {func(f composeFixture) {
			f.app.DB.Model(&models.Registration{}).Where("occurrence_id = ?", f.groupOcc.ID).Update("status", models.RegDeclined)
		}, skipNoLongerGoing},
	} {
		t.Run(name, func(t *testing.T) {
			f, fp, _ := pushReminderFixture(t)
			due := f.groupOcc.StartsAt.Add(-2 * time.Hour)
			sweepAt(t, f.app, due.Add(-30*time.Minute))
			c.change(f)
			deliverAt(t, f.app, due)
			_, rows := pushReminderOf(t, f.app, f.groupOcc)
			if fp.total() != 0 || rows[f.teen.Email].Status != models.RecipientSuppressed || rows[f.teen.Email].Reason != c.why {
				t.Fatalf("pushes %v, rows %+v", fp.sent, rows)
			}
			// A sweep afterwards plans nothing new.
			sweepAt(t, f.app, due.Add(10*time.Minute))
			deliverAt(t, f.app, due.Add(10*time.Minute))
			if fp.total() != 0 {
				t.Fatalf("sent after: %v", fp.sent)
			}
		})
	}
}

// A moved date reminds again for its new time and never for the old one;
// the date page shows only the new one.
func TestPushReminderMovedDate(t *testing.T) {
	f, fp, phone := pushReminderFixture(t)
	old := f.groupOcc
	oldDue := old.StartsAt.Add(-2 * time.Hour)
	sweepAt(t, f.app, oldDue.Add(-30*time.Minute))
	moveDate(t, f.app, &f.groupOcc, old.StartsAt.Add(24*time.Hour))
	deliverAt(t, f.app, oldDue)
	if fp.total() != 0 {
		t.Fatalf("the old time pushed: %v", fp.sent)
	}
	_, oldRows := pushReminderOf(t, f.app, old)
	if oldRows[f.teen.Email].Reason != pushWhyDateChanged {
		t.Fatalf("old row: %+v", oldRows)
	}
	newDue := f.groupOcc.StartsAt.Add(-2 * time.Hour)
	sweepAt(t, f.app, newDue.Add(-30*time.Minute))
	deliverAt(t, f.app, newDue)
	if fp.count(phone.path) != 1 {
		t.Fatalf("pushes: %v", fp.sent)
	}
	var ev models.Event
	f.app.DB.First(&ev, f.groupEvent.ID)
	r, err := f.app.dateReminders(&ev, &f.groupOcc)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Messages) != 1 || r.Messages[0].Kind != reminderPushKind || r.Messages[0].Reached != 1 || r.PushDueAt != nil || r.Push == nil || !*r.Push {
		t.Fatalf("date page: %+v", r)
	}
}

// A registrant who can no longer see the date by the time it sends gets
// no push; one who can still does.
func TestPushReminderRechecksWhoCanSeeTheDate(t *testing.T) {
	f, fp, phone := pushReminderFixture(t)
	outsider := pushMember(t, f.app, "outsider")
	outsiderPhone := newPushDevice(t, f.app, outsider, "outsider")
	going(t, f, outsider)
	due := f.groupOcc.StartsAt.Add(-2 * time.Hour)
	sweepAt(t, f.app, due.Add(-30*time.Minute))
	f.app.DB.Model(&models.Event{}).Where("id = ?", f.groupEvent.ID).Update("visibility", models.VisibilityInviteOnly)
	f.app.DB.Model(&models.Group{}).Where("id = ?", f.group.ID).Update("visibility", models.VisibilityInviteOnly)
	deliverAt(t, f.app, due)
	_, rows := pushReminderOf(t, f.app, f.groupOcc)
	if fp.count(outsiderPhone.path) != 0 || fp.count(phone.path) != 1 || rows[outsider.Email].Reason != skipCantSeeDate {
		t.Fatalf("pushes %v, rows %+v", fp.sent, rows)
	}
}

// The hourly limit never stops a reminder, but a sent one counts toward it.
func TestPushReminderCountsButIsNotLimited(t *testing.T) {
	f, fp, phone := pushReminderFixture(t)
	due := f.groupOcc.StartsAt.Add(-2 * time.Hour)
	earlier := func(n int, at time.Time) {
		for range n {
			m := models.Message{Status: models.MessageSent, Subject: "Earlier"}
			f.app.DB.Create(&m)
			f.app.DB.Create(&models.MessageRecipient{MessageID: m.ID, UserID: f.teen.ID, Channel: models.DeliveryPush, Status: models.RecipientSent, SentAt: &at})
		}
	}
	earlier(pushHourlyLimit, due.Add(-10*time.Minute))
	sweepAt(t, f.app, due.Add(-30*time.Minute))
	deliverAt(t, f.app, due)
	if fp.count(phone.path) != 1 {
		t.Fatalf("a reminder past 20 pushes in the hour didn't go: %v", fp.sent)
	}
	// It counts: with the first 20 an hour gone and 19 more since, the
	// reminder makes 20, so the next optional push is over the limit.
	f.app.DB.Model(&models.MessageRecipient{}).Where("user_id = ? AND sent_at < ?", f.teen.ID, due).Update("sent_at", due.Add(-2*time.Hour))
	earlier(pushHourlyLimit-1, due.Add(time.Minute))
	over, err := f.app.overHourlyLimit(context.Background(), pushItem{UserID: f.teen.ID}, due.Add(2*time.Minute))
	if err != nil || !over {
		t.Fatalf("the reminder didn't count: %v %v", over, err)
	}
}

// The lock screen: "Reminder: <title>", when it starts on the reader's
// clock, and the place, with no link or phone number.
func TestReminderPushText(t *testing.T) {
	start := time.Date(2030, 1, 10, 19, 0, 0, 0, denverZone)
	title, body := reminderPushText("Book night", "Room 201", start, denverZone, start.Add(-2*time.Hour))
	if title != "Reminder: Book night" || body != "Starts at 7:00 PM · Room 201" {
		t.Fatalf("%q / %q", title, body)
	}
	morning := time.Date(2030, 1, 10, 9, 0, 0, 0, denverZone)
	if _, body := reminderPushText("Prayer", "", morning, denverZone, morning.Add(-13*time.Hour-time.Minute)); body != "Starts tomorrow at 9:00 AM" {
		t.Fatalf("the night before: %q", body)
	}
	if _, body := reminderPushText("Prayer", "", morning, time.UTC, morning.Add(-2*time.Hour)); body != "Starts at 4:00 PM" {
		t.Fatalf("another zone: %q", body)
	}
	_, body = reminderPushText("Call", "https://zoom.us/j/123?pwd=secret or call 208-555-0123", start, denverZone, start.Add(-2*time.Hour))
	for _, bad := range []string{"zoom", "secret", "555"} {
		if strings.Contains(body, bad) {
			t.Fatalf("%q leaked: %q", bad, body)
		}
	}
}

// The history of a push reminder lists each person by their push, and it
// is never sent again.
func TestPushReminderHistory(t *testing.T) {
	f, _, _ := pushReminderFixture(t)
	due := f.groupOcc.StartsAt.Add(-2 * time.Hour)
	sweepAt(t, f.app, due.Add(-30*time.Minute))
	deliverAt(t, f.app, due)
	m, _ := pushReminderOf(t, f.app, f.groupOcc)
	var out messageRecipientsJSON
	w := f.leader.do("GET", "/api/v1/messages/"+itoa(m.ID)+"/recipients", nil, false)
	decodeJSON(t, w, &out)
	if w.Code != http.StatusOK || out.Counts.Push.Sent != 1 || len(out.Recipients) != 1 || out.Recipients[0].Status != historySent ||
		out.Recipients[0].Name != f.teen.PublicName() || out.CanRetry || out.Counts.Sent != 0 {
		t.Fatalf("history: %d %s", w.Code, w.Body.String())
	}
	var msg struct {
		Message messageJSON `json:"message"`
	}
	decodeJSON(t, f.leader.do("GET", "/api/v1/messages/"+itoa(m.ID), nil, false), &msg)
	if msg.Message.Reminder == nil || msg.Message.Reminder.Kind != reminderPushKind || msg.Message.DisplaySubject != "Reminder: Book night" {
		t.Fatalf("message: %+v", msg.Message)
	}
}

// The date page: when the push reminder goes before it is written, and
// nothing about push when the church turned it off.
func TestDateRemindersPushLine(t *testing.T) {
	f, _, _ := pushReminderFixture(t)
	var ev models.Event
	f.app.DB.First(&ev, f.groupEvent.ID)
	r, err := f.app.dateReminders(&ev, &f.groupOcc)
	if err != nil {
		t.Fatal(err)
	}
	if r.Push == nil || !*r.Push || r.PushDueAt == nil || !r.PushDueAt.Equal(f.groupOcc.StartsAt.Add(-2*time.Hour)) {
		t.Fatalf("before: %+v", r)
	}
	f.app.DB.Model(&models.Setting{}).Where("key = ?", settingPushEnabled).Delete(&models.Setting{})
	f.app.DB.Create(&models.Setting{Key: settingPushEnabled, Value: "false"})
	if r, _ = f.app.dateReminders(&ev, &f.groupOcc); r.Push != nil || r.PushDueAt != nil {
		t.Fatalf("church off: %+v", r)
	}
	// And the sweep plans nothing.
	sweepAt(t, f.app, f.groupOcc.StartsAt.Add(-150*time.Minute))
	if m, _ := pushReminderOf(t, f.app, f.groupOcc); m.ID != 0 {
		t.Fatal("planned with the church's push off")
	}
}

// Off chosen at creation stays off; no choice means on; a series edit
// changes it.
func TestEventReminderPushSwitch(t *testing.T) {
	f := newComposeFixture(t)
	create := func(path, title string, body map[string]any) eventJSON {
		body["title"], body["startsLocal"], body["durationMinutes"], body["tzid"] = title, feedStart(4).Format("2006-01-02T15:04"), 60, "UTC"
		var out struct {
			Event eventJSON `json:"event"`
		}
		c := f.leader
		if path == "/api/v1/events" {
			c = f.admin
			body["visibility"] = "public"
		}
		w := c.do("POST", path, body, true)
		if w.Code != http.StatusCreated {
			t.Fatalf("create: %d %s", w.Code, w.Body.String())
		}
		decodeJSON(t, w, &out)
		return out.Event
	}
	group := "/api/v1/groups/" + itoa(f.group.ID) + "/events"
	stored := func(title string) bool {
		var ev models.Event
		f.app.DB.Where("title = ?", title).First(&ev)
		return ev.ReminderPush
	}
	if ev := create(group, "Off", map[string]any{"reminderPush": false}); ev.ReminderPush || stored("Off") {
		t.Fatal("off at creation didn't stick")
	}
	if ev := create("/api/v1/events", "Church off", map[string]any{"reminderPush": false, "reminderEmailMinutes": 0}); ev.ReminderPush || stored("Church off") || ev.ReminderEmailMinutes != 0 {
		t.Fatal("off at a church event's creation didn't stick")
	}
	ev := create(group, "Default", map[string]any{})
	if !ev.ReminderPush || !stored("Default") {
		t.Fatal("the default is on")
	}
	var out struct {
		Event eventJSON `json:"event"`
	}
	w := f.leader.do("PUT", "/api/v1/events/"+itoa(ev.ID), map[string]any{"reminderPush": false}, true)
	decodeJSON(t, w, &out)
	if w.Code != http.StatusOK || out.Event.ReminderPush || stored("Default") {
		t.Fatalf("series edit: %d %s", w.Code, w.Body.String())
	}
}
