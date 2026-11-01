package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"fold/internal/jobs"
	"fold/internal/notify"
	"fold/models"
)

// pushRowsOf is a message's push rows by the recipient's address.
func pushRowsOf(t *testing.T, app *App, id uint) map[string]models.MessageRecipient {
	t.Helper()
	var rows []models.MessageRecipient
	app.DB.Where("message_id = ? AND channel = ?", id, models.DeliveryPush).Find(&rows)
	out := map[string]models.MessageRecipient{}
	for _, r := range rows {
		var u models.User
		app.DB.First(&u, r.UserID)
		if r.CopyOfUserID != nil {
			t.Fatalf("a guardian copy has a push row: %+v", r)
		}
		out[u.Email] = r
	}
	return out
}

func outboxCount(app *App) int64 {
	var n int64
	app.DB.Model(&models.MailOutbox{}).Count(&n)
	return n
}

func groupAudience(f composeFixture) map[string]any {
	return map[string]any{"type": "group", "groupId": f.group.ID}
}

// One email and one push for each recipient with a device, even across a
// restart (M3.13 acceptance): the teen's two devices each get the push
// once and it is recorded once; the leader, push on but no device, gets
// the email only and no row; the message's own count and status follow
// the email. Running every job again sends nothing twice.
func TestAnnouncementSendsOneEmailAndOnePush(t *testing.T) {
	f := newComposeFixture(t)
	fp := useFakePush(t, f.app)
	pushConsentRow(t, f.app, f.teen, "all", models.ConsentOptedIn)
	pushConsentRow(t, f.app, f.leaderUser, "all", models.ConsentOptedIn)
	phone, laptop := newPushDevice(t, f.app, f.teen, "phone"), newPushDevice(t, f.app, f.teen, "laptop")
	id := f.scheduleNow(t, f.leader, "Snow day", "<p>Hi {{first_name}}, no meeting tonight.</p>", groupAudience(f))
	runMessageJobs(t, f.app)

	var m models.Message
	f.app.DB.First(&m, id)
	if m.Status != models.MessageSent || m.RecipientCount != 2 || !m.PushAlert {
		t.Fatalf("message: %+v", m)
	}
	emails := recipientsOf(t, f.app, id)
	if emails[f.teen.Email].Status != models.RecipientSent || emails[f.leaderUser.Email].Status != models.RecipientSent {
		t.Fatalf("emails: %+v", emails)
	}
	pushes := pushRowsOf(t, f.app, id)
	if len(pushes) != 1 || pushes[f.teen.Email].Status != models.RecipientSent || pushes[f.teen.Email].SentAt == nil || pushes[f.teen.Email].Reason != "" {
		t.Fatalf("push rows: %+v", pushes)
	}
	if fp.count(phone.path) != 1 || fp.count(laptop.path) != 1 || fp.total() != 2 {
		t.Fatalf("pushes: %v", fp.sent)
	}
	// One job per device, keyed by row and device, with ids only.
	var js []models.Job
	f.app.DB.Where("kind = ?", kindPushDeliver).Order("id").Find(&js)
	if len(js) != 2 {
		t.Fatalf("%d push jobs", len(js))
	}
	want := map[string]bool{}
	for _, d := range []testDevice{phone, laptop} {
		want[fmt.Sprintf("push:mr:%d:d:%d", pushes[f.teen.Email].ID, d.ID)] = true
	}
	for _, j := range js {
		if j.UniqueKey == nil || !want[*j.UniqueKey] || strings.Contains(j.Payload, "fcm") || strings.Contains(j.Payload, "secret") {
			t.Fatalf("job %v %s", j.UniqueKey, j.Payload)
		}
		delete(want, *j.UniqueKey)
	}
	// A restart: message.send again, and the push jobs queued again.
	before := outboxCount(f.app)
	if _, err := jobs.Enqueue(f.app.DB, kindMessageSend, messageSendJob{MessageID: id}, time.Now(), "", 0); err != nil {
		t.Fatal(err)
	}
	if err := enqueueMessagePushes(f.app.DB, id); err != nil {
		t.Fatal(err)
	}
	for _, d := range []testDevice{phone, laptop} {
		if added, err := jobs.Enqueue(f.app.DB, kindPushDeliver, pushJob{MessageRecipientID: pushes[f.teen.Email].ID, DeviceID: d.ID}, time.Now(),
			fmt.Sprintf("push:mr:%d:d:%d", pushes[f.teen.Email].ID, d.ID), 0); err != nil || added {
			t.Fatalf("queued twice: %v %v", added, err)
		}
	}
	runMessageJobs(t, f.app)
	if outboxCount(f.app) != before || fp.total() != 2 || len(pushRowsOf(t, f.app, id)) != 1 {
		t.Fatalf("sent again: emails %d→%d, pushes %v", before, outboxCount(f.app), fp.sent)
	}
}

// The push is the subject and the first 120 characters of the plain text,
// as the reader's email says them, and opens the message in Fold. No link
// of any kind reaches the lock screen.
func TestMessagePushText(t *testing.T) {
	m := &models.Message{ID: 7, Subject: "Hi {{first_name}}: snow day",
		BodyText: "No meeting tonight, {{first_name}}. Details (https://example.org/x?token=abc) and your date ({{manage_link}}) " +
			"or www.example.com/page. " + strings.Repeat("word ", 40)}
	title, body := messagePushText(m, map[string]string{"first_name": "Sam", "manage_link": "https://fold.example/events/1/dates/2026-11-05"})
	if title != "Hi Sam: snow day" {
		t.Fatalf("title %q", title)
	}
	snippet, open, ok := strings.Cut(body, "\n")
	if !ok || open != messagePushOpen {
		t.Fatalf("body %q", body)
	}
	if !strings.HasPrefix(snippet, "No meeting tonight, Sam. Details and your date or ") || !strings.HasSuffix(snippet, "…") ||
		len([]rune(snippet)) > messagePushSnippet+1 {
		t.Fatalf("snippet %q (%d)", snippet, len([]rune(snippet)))
	}
	for _, bad := range []string{"http", "www.", "token", "{{", "fold.example"} {
		if strings.Contains(body, bad) || strings.Contains(title, bad) {
			t.Fatalf("%q leaked: %q / %q", bad, title, body)
		}
	}
	if _, body := messagePushText(&models.Message{Subject: "Short"}, nil); body != messagePushOpen {
		t.Fatalf("an empty body: %q", body)
	}
}

// No contact detail reaches a lock screen (DEC-124): email addresses (with
// or without mailto:) and phone numbers are taken out of the subject and
// the text; dates, times and ordinary numbers stay.
func TestMessagePushTextLeavesOutContactDetails(t *testing.T) {
	m := &models.Message{Subject: "Questions? pat@example.org or 555-123-4567",
		BodyText: "Write to Pat.Lee+church@mail.example.co.uk, mailto:office@example.org or call (208) 555-0199, +44 20 7946 0958 or 555 0100. " +
			"We meet 2026-11-05 at 6:45 in Room 104 with 12 people."}
	title, body := messagePushText(m, nil)
	for _, bad := range []string{"@", "mailto", "555", "0199", "7946", "0100"} {
		if strings.Contains(title, bad) || strings.Contains(body, bad) {
			t.Fatalf("%q reached the lock screen: %q / %q", bad, title, body)
		}
	}
	for _, keep := range []string{"Write to", "2026-11-05", "6:45", "Room 104", "12 people"} {
		if !strings.Contains(body, keep) {
			t.Fatalf("%q was taken out: %q", keep, body)
		}
	}
	if title != "Questions? or" {
		t.Fatalf("title %q", title)
	}
}

// Push follows push consent, not the email row (DEC-122): someone who
// turned off email from leaders, or whose email bounces, still gets the
// push when push is on, and no email. Someone with push off for messages
// from leaders gets the email only; a guardian's copy gets no push;
// push_alert off sends none; and with the church's switch off no push row
// is recorded at all.
func TestAnnouncementPushSkips(t *testing.T) {
	f := newComposeFixture(t)
	fp := useFakePush(t, f.app)
	for _, u := range []*models.User{f.teen, f.opted, f.bouncing} {
		pushConsentRow(t, f.app, u, "all", models.ConsentOptedIn)
		newPushDevice(t, f.app, u, u.Name+"-phone")
	}
	// The leader turned push off for messages from leaders.
	pushConsentRow(t, f.app, f.leaderUser, "all", models.ConsentOptedIn)
	pushConsentRow(t, f.app, f.leaderUser, "category:"+notify.CatMessages, models.ConsentOptedOut)
	newPushDevice(t, f.app, f.leaderUser, "leader-phone")
	// The teen's guardian has push on, but copies are email only.
	parent := pushMember(t, f.app, "parent")
	newPushDevice(t, f.app, parent, "parent-phone")
	f.app.DB.Create(&models.GuardianLink{GuardianUserID: parent.ID, TeenUserID: &f.teen.ID, Status: models.GuardianConfirmed})
	f.app.DB.Create(&models.Setting{Key: settingGuardianCopies, Value: "true"})

	id := f.scheduleNow(t, f.leader, "One", "<p>Hi</p>", groupAudience(f))
	runMessageJobs(t, f.app)
	pushes := pushRowsOf(t, f.app, id)
	if len(pushes) != 3 || pushes[f.teen.Email].Status != models.RecipientSent || pushes[f.opted.Email].Status != models.RecipientSent ||
		pushes[f.bouncing.Email].Status != models.RecipientSent {
		t.Fatalf("push rows: %+v", pushes)
	}
	emails := recipientsOf(t, f.app, id)
	if emails[f.leaderUser.Email].Status != models.RecipientSent || emails["copy:"+parent.Email].Status != models.RecipientSent ||
		emails[f.opted.Email].Status != models.RecipientSuppressed || emails[f.bouncing.Email].Status != models.RecipientSuppressed {
		t.Fatalf("emails: %+v", emails)
	}
	if _, err := devOutbox(t, f.app).LatestTo(f.opted.Email); err == nil {
		t.Fatal("someone who turned off email from leaders got the email")
	}
	if fp.total() != 3 {
		t.Fatalf("pushes %v", fp.sent)
	}
	// The history shows it on the person's own row: missed the email,
	// pushed. Each person once, guardian copies apart.
	var h messageRecipientsJSON
	decodeJSON(t, f.admin.do("GET", "/api/v1/messages/"+itoa(id)+"/recipients", nil, false), &h)
	for _, r := range h.Recipients {
		if r.Name == f.opted.PublicName() && (r.Status != historyMissed || r.Push == nil || r.Push.Status != historySent) {
			t.Fatalf("opted: %+v", r)
		}
		if r.CopyOf != "" && r.Push != nil {
			t.Fatalf("a copy shows a push: %+v", r)
		}
	}
	if len(h.Recipients) != 5 || h.Counts.Push.Sent != 3 {
		t.Fatalf("history: %+v", h)
	}

	// push_alert off: email only.
	var out struct {
		Message messageJSON `json:"message"`
	}
	w := f.leader.do("POST", "/api/v1/messages", map[string]any{"subject": "Two", "bodyHtml": "<p>Hi</p>", "audience": groupAudience(f), "pushAlert": false}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("draft: %d %s", w.Code, w.Body.String())
	}
	decodeJSON(t, w, &out)
	if out.Message.PushAlert {
		t.Fatal("pushAlert false was saved as true")
	}
	if w := f.leader.do("POST", "/api/v1/messages/"+itoa(out.Message.ID)+"/schedule", map[string]any{}, true); w.Code != http.StatusOK {
		t.Fatalf("schedule: %d", w.Code)
	}
	runMessageJobs(t, f.app)
	if rows := pushRowsOf(t, f.app, out.Message.ID); len(rows) != 0 || recipientsOf(t, f.app, out.Message.ID)[f.teen.Email].Status != models.RecipientSent {
		t.Fatalf("push_alert off: %+v", rows)
	}

	// The church's switch off: no push row recorded.
	f.app.DB.Create(&models.Setting{Key: settingPushEnabled, Value: "false"})
	off := f.scheduleNow(t, f.leader, "Three", "<p>Hi</p>", groupAudience(f))
	runMessageJobs(t, f.app)
	if rows := pushRowsOf(t, f.app, off); len(rows) != 0 || fp.total() != 3 {
		t.Fatalf("church off: %+v, pushes %v", rows, fp.sent)
	}
	var m models.Message
	if f.app.DB.First(&m, off); m.Status != models.MessageSent {
		t.Fatalf("church off: %+v", m)
	}
}

// Turned off by the church after the snapshot: the waiting push is
// recorded "push turned off by the church", never sent.
func TestAnnouncementPushChecksTheChurchSwitchAtSendTime(t *testing.T) {
	f := newComposeFixture(t)
	fp := useFakePush(t, f.app)
	pushConsentRow(t, f.app, f.teen, "all", models.ConsentOptedIn)
	newPushDevice(t, f.app, f.teen, "phone")
	id := f.scheduleNow(t, f.leader, "One", "<p>Hi</p>", groupAudience(f))
	// Prepare only, then turn push off before the device's job runs.
	if err := f.app.prepareMessageOnce(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	f.app.DB.Create(&models.Setting{Key: settingPushEnabled, Value: "false"})
	runMessageJobs(t, f.app)
	if r := pushRowsOf(t, f.app, id)[f.teen.Email]; r.Status != models.RecipientSuppressed || r.Reason != pushWhyChurchOff || fp.total() != 0 {
		t.Fatalf("row %+v, pushes %v", r, fp.sent)
	}
}

// At 11 PM the push waits for 8 AM on the recipient's clock: the message
// is sent (the email went), the push row stays queued with when it goes,
// and the history says so. In the morning it sends once. A cancelled
// message's waiting push doesn't send.
func TestAnnouncementPushWaitsForTheMorning(t *testing.T) {
	f := newComposeFixture(t)
	fp := useFakePush(t, f.app)
	clock := quietHours(f.app)
	pushConsentRow(t, f.app, f.teen, "all", models.ConsentOptedIn)
	f.app.DB.Model(f.teen).Update("timezone", "America/Denver")
	phone := newPushDevice(t, f.app, f.teen, "phone")
	id := f.scheduleNow(t, f.leader, "Late", "<p>Hi</p>", groupAudience(f))
	runMessageJobs(t, f.app)
	var m models.Message
	f.app.DB.First(&m, id)
	row := pushRowsOf(t, f.app, id)[f.teen.Email]
	if m.Status != models.MessageSent || row.Status != models.RecipientQueued || row.Reason != "outside quiet hours · sending at 8:00 AM" || fp.total() != 0 {
		t.Fatalf("message %s, row %+v, pushes %v", m.Status, row, fp.sent)
	}
	var h messageRecipientsJSON
	w := f.leader.do("GET", "/api/v1/messages/"+itoa(id)+"/recipients", nil, false)
	decodeJSON(t, w, &h)
	if h.Counts.Push.Sent != 0 || len(h.Counts.Push.Waiting) != 1 || h.Counts.Push.Waiting[0] != (pushWaitJSON{Until: "8:00 AM", N: 1}) ||
		h.Counts.Sent != 2 || h.Counts.Queued != 0 || h.CanRetry {
		t.Fatalf("history counts: %+v", h.Counts)
	}
	// One row a person: the teen's push is on their own row, with when it
	// goes (as the counts say it) and no reason for a leader.
	waiting := 0
	for _, r := range h.Recipients {
		if r.Push != nil {
			waiting++
			if r.Name != f.teen.PublicName() || *r.Push != (historyPushJSON{Status: historyWaiting, Until: "8:00 AM"}) || r.Status != historySent {
				t.Fatalf("push on %+v: %+v", r, *r.Push)
			}
		}
	}
	if waiting != 1 || len(h.Recipients) != 4 {
		t.Fatalf("history rows: %+v", h.Recipients)
	}
	clock.set(8, 0, 16)
	makeDue(f.app)
	drainJobs(t, f.app)
	if r := pushRowsOf(t, f.app, id)[f.teen.Email]; r.Status != models.RecipientSent || r.Reason != "" || fp.count(phone.path) != 1 {
		t.Fatalf("in the morning: %+v, pushes %v", r, fp.sent)
	}

	// Cancelled while it waits: nothing goes.
	clock.set(23, 0, 16)
	late := f.scheduleNow(t, f.leader, "Later", "<p>Hi</p>", groupAudience(f))
	runMessageJobs(t, f.app)
	f.app.DB.Model(&models.Message{}).Where("id = ?", late).Update("status", models.MessageCancelled)
	clock.set(8, 0, 17)
	makeDue(f.app)
	drainJobs(t, f.app)
	if r := pushRowsOf(t, f.app, late)[f.teen.Email]; r.Status != models.RecipientSuppressed || r.Reason != pushWhyStale || fp.count(phone.path) != 1 {
		t.Fatalf("cancelled: %+v, pushes %v", r, fp.sent)
	}
}

// The hourly limit counts announcement and notification pushes together:
// 19 notifications and one announcement fill the hour, so the next
// notification is held back, and so is the next announcement.
func TestPushHourlyLimitCountsAnnouncements(t *testing.T) {
	f := newComposeFixture(t)
	fp := useFakePush(t, f.app)
	clock := quietHours(f.app)
	clock.set(10, 0, 16)
	pushConsentRow(t, f.app, f.teen, "all", models.ConsentOptedIn)
	f.app.DB.Model(f.teen).Update("timezone", "America/Denver")
	phone := newPushDevice(t, f.app, f.teen, "phone")
	for i := 0; i < pushHourlyLimit-1; i++ {
		n := sendNotice(t, f.app, notify.GroupApproved, f.teen)
		if err := recordPushIn(f.app.DB, models.Notification{ID: n, UserID: f.teen.ID}, models.DeliverySent, "", clock.at); err != nil {
			t.Fatal(err)
		}
	}
	first := f.scheduleNow(t, f.leader, "Twentieth", "<p>Hi</p>", groupAudience(f))
	runMessageJobs(t, f.app)
	if r := pushRowsOf(t, f.app, first)[f.teen.Email]; r.Status != models.RecipientSent || fp.count(phone.path) != 1 {
		t.Fatalf("the 20th: %+v", r)
	}
	n := sendNotice(t, f.app, notify.GroupApproved, f.teen)
	runPushJobs(t, f.app, n, phone)
	if status, reason := pushDelivery(t, f.app, n); status != models.DeliverySuppressed || reason != pushWhyHourly {
		t.Fatalf("a notification past the limit: %q %q", status, reason)
	}
	second := f.scheduleNow(t, f.leader, "Twenty-first", "<p>Hi</p>", groupAudience(f))
	runMessageJobs(t, f.app)
	if r := pushRowsOf(t, f.app, second)[f.teen.Email]; r.Status != models.RecipientSuppressed || r.Reason != pushWhyHourly || fp.count(phone.path) != 1 {
		t.Fatalf("an announcement past the limit: %+v, pushes %v", r, fp.sent)
	}
}

// "Try again" resends failed emails only: someone already pushed is never
// pushed again, and push rows never offer it.
func TestRetryNeverPushesAgain(t *testing.T) {
	f := newComposeFixture(t)
	fp := useFakePush(t, f.app)
	pushConsentRow(t, f.app, f.teen, "all", models.ConsentOptedIn)
	newPushDevice(t, f.app, f.teen, "phone")
	id := f.scheduleNow(t, f.leader, "Once", "<p>Hi</p>", groupAudience(f))
	runMessageJobs(t, f.app)
	// The leader's email failed.
	f.app.DB.Model(&models.MessageRecipient{}).Where("message_id = ? AND user_id = ? AND channel = ?", id, f.leaderUser.ID, models.DeliveryEmail).
		Updates(map[string]any{"status": models.RecipientFailed, "error": whyProvider})
	var h messageRecipientsJSON
	decodeJSON(t, f.leader.do("GET", "/api/v1/messages/"+itoa(id)+"/recipients", nil, false), &h)
	if !h.CanRetry || h.Counts.Failed != 1 || h.Counts.Push.Sent != 1 {
		t.Fatalf("history: %+v", h.Counts)
	}
	if w := f.leader.do("POST", "/api/v1/messages/"+itoa(id)+"/retry", map[string]any{}, true); w.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", w.Code, w.Body.String())
	}
	runMessageJobs(t, f.app)
	if fp.total() != 1 || len(pushRowsOf(t, f.app, id)) != 1 {
		t.Fatalf("pushed again: %v", fp.sent)
	}
	// A failed push alone is nothing to retry.
	f.app.DB.Model(&models.MessageRecipient{}).Where("message_id = ? AND channel = ?", id, models.DeliveryPush).Update("status", models.RecipientFailed)
	decodeJSON(t, f.leader.do("GET", "/api/v1/messages/"+itoa(id)+"/recipients", nil, false), &h)
	if h.CanRetry || h.Counts.Failed != 0 || h.Counts.Push.Missed[""] != 1 {
		t.Fatalf("a failed push offers Try again: %+v", h)
	}
}

// The daily reaper stops a push row that no job will send (its device
// was added after the snapshot), so a history never waits forever.
func TestReaperStopsAPushWithNoJob(t *testing.T) {
	f := newComposeFixture(t)
	useFakePush(t, f.app)
	id := f.scheduleNow(t, f.leader, "Once", "<p>Hi</p>", groupAudience(f))
	runMessageJobs(t, f.app)
	row := models.MessageRecipient{MessageID: id, UserID: f.teen.ID, Channel: models.DeliveryPush, Status: models.RecipientQueued,
		UpdatedAt: time.Now().Add(-2 * time.Hour)}
	if err := f.app.DB.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	waiting := models.MessageRecipient{MessageID: id, UserID: f.leaderUser.ID, Channel: models.DeliveryPush, Status: models.RecipientQueued,
		UpdatedAt: time.Now().Add(-2 * time.Hour)}
	if err := f.app.DB.Create(&waiting).Error; err != nil {
		t.Fatal(err)
	}
	// This one's job is still waiting for the morning.
	if _, err := jobs.Enqueue(f.app.DB, kindPushDeliver, pushJob{MessageRecipientID: waiting.ID, DeviceID: 1}, time.Now().Add(8*time.Hour), "", 0); err != nil {
		t.Fatal(err)
	}
	if err := f.app.reapMessages(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	var got, kept models.MessageRecipient
	f.app.DB.First(&got, row.ID)
	f.app.DB.First(&kept, waiting.ID)
	if got.Status != models.RecipientSuppressed || got.Reason != pushWhyStopped || kept.Status != models.RecipientQueued {
		t.Fatalf("reaped %+v, kept %+v", got, kept)
	}
}

// A queued push row whose person has no device left when the jobs are
// queued is "device gone" at once, not left waiting for the reaper; one
// with devices gets a job per device.
func TestQueuingPushesSettlesARowWithNoDevice(t *testing.T) {
	f := newComposeFixture(t)
	useFakePush(t, f.app)
	id := f.scheduleNow(t, f.leader, "Once", "<p>Hi</p>", groupAudience(f))
	phone, laptop := newPushDevice(t, f.app, f.teen, "phone"), newPushDevice(t, f.app, f.teen, "laptop")
	withDevices := models.MessageRecipient{MessageID: id, UserID: f.teen.ID, Channel: models.DeliveryPush, Status: models.RecipientQueued}
	gone := models.MessageRecipient{MessageID: id, UserID: f.leaderUser.ID, Channel: models.DeliveryPush, Status: models.RecipientQueued}
	for _, r := range []*models.MessageRecipient{&withDevices, &gone} {
		if err := f.app.DB.Create(r).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := enqueueMessagePushes(f.app.DB, id); err != nil {
		t.Fatal(err)
	}
	var keys []string
	f.app.DB.Model(&models.Job{}).Where("kind = ?", kindPushDeliver).Order("unique_key").Pluck("unique_key", &keys)
	want := []string{fmt.Sprintf("push:mr:%d:d:%d", withDevices.ID, phone.ID), fmt.Sprintf("push:mr:%d:d:%d", withDevices.ID, laptop.ID)}
	if len(keys) != 2 || keys[0] != min(want[0], want[1]) || keys[1] != max(want[0], want[1]) {
		t.Fatalf("jobs %v, want %v", keys, want)
	}
	var got, left models.MessageRecipient
	f.app.DB.First(&got, gone.ID)
	f.app.DB.First(&left, withDevices.ID)
	if got.Status != models.RecipientSuppressed || got.Reason != pushWhyGone || left.Status != models.RecipientQueued {
		t.Fatalf("no device: %+v; with devices: %+v", got, left)
	}
}
