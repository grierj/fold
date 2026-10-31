package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/textproto"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"fold/internal/audit"
	"fold/internal/jobs"
	"fold/internal/mail"
	"fold/internal/providers"
	"fold/models"
)

// runMessageJobs runs due message jobs until none are left; retries are
// made due at once so a test can walk a job through its attempts.
func runMessageJobs(t *testing.T, app *App) {
	t.Helper()
	r := jobs.NewRunner(app.DB, app.backgroundKinds()...)
	for range 100 {
		app.DB.Model(&models.Job{}).Where("status = ?", models.JobFailed).Update("run_at", time.Now().Add(-time.Second))
		ran, err := r.RunOnce(context.Background(), "test")
		if err != nil {
			t.Fatal(err)
		}
		if !ran {
			return
		}
	}
	t.Fatal("message jobs never finished")
}

// scheduleNow drafts and schedules a message for now, and returns its id.
func (f composeFixture) scheduleNow(t *testing.T, c *apiClient, subject, body string, aud map[string]any) uint {
	t.Helper()
	var out struct {
		Message messageJSON `json:"message"`
	}
	w := c.do("POST", "/api/v1/messages", map[string]any{"subject": subject, "bodyHtml": body, "audience": aud}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("draft: %d %s", w.Code, w.Body.String())
	}
	decodeJSON(t, w, &out)
	if w := c.do("POST", "/api/v1/messages/"+itoa(out.Message.ID)+"/schedule", map[string]any{}, true); w.Code != http.StatusOK {
		t.Fatalf("schedule: %d %s", w.Code, w.Body.String())
	}
	return out.Message.ID
}

func recipientsOf(t *testing.T, app *App, id uint) map[string]models.MessageRecipient {
	t.Helper()
	var rows []models.MessageRecipient
	app.DB.Where("message_id = ?", id).Find(&rows)
	out := map[string]models.MessageRecipient{}
	for _, r := range rows {
		var u models.User
		app.DB.First(&u, r.UserID)
		key := u.Email
		if r.CopyOfUserID != nil {
			key = "copy:" + key
		}
		out[key] = r
	}
	return out
}

// A scheduled message goes out as each reader gets it, from the author
// via the church, with replies to the church office; the skipped are
// recorded with their reasons (M3.5 acceptance).
func TestMessagePipelineSendsAndRecordsWhoWasSkipped(t *testing.T) {
	f := newComposeFixture(t)
	f.app.DB.Create(&models.Setting{Key: settingReplyTo, Value: "office@example.org"})
	id := f.scheduleNow(t, f.leader, "Book night", "<p>Hi {{first_name}}, see you there.</p>", map[string]any{"type": "group", "groupId": f.group.ID})
	runMessageJobs(t, f.app)
	var m models.Message
	f.app.DB.First(&m, id)
	if m.Status != models.MessageSent || m.SentAt == nil || m.RecipientCount != 2 {
		t.Fatalf("message: %+v", m)
	}
	rows := recipientsOf(t, f.app, id)
	if rows[f.opted.Email].Status != models.RecipientSuppressed || rows[f.opted.Email].Reason != skipOptedOut ||
		rows[f.bouncing.Email].Status != models.RecipientSuppressed || rows[f.bouncing.Email].Reason != skipNotWorking {
		t.Fatalf("skipped: %+v", rows)
	}
	got, err := devOutbox(t, f.app).LatestTo(f.teen.Email)
	if err != nil || rows[f.teen.Email].Status != models.RecipientSent || rows[f.teen.Email].ProviderMessageID == "" {
		t.Fatalf("teen: %+v %v", rows[f.teen.Email], err)
	}
	if !strings.Contains(got.Body, "Hi "+firstName(f.teen.PublicName())+",") || !strings.Contains(got.HTML, "Hi "+firstName(f.teen.PublicName())+",") ||
		!strings.Contains(got.From, f.leaderUser.PublicName()+" via ") || !strings.Contains(got.From, "noreply@example.com") ||
		got.ReplyTo != "office@example.org" || got.ListUnsubscribe == "" {
		t.Fatalf("email: %+v", got)
	}
	if _, err := devOutbox(t, f.app).LatestTo(f.opted.Email); err == nil {
		t.Fatal("an opted-out member got it")
	}
	if auditCount(t, f.app, audit.MessageSent) != 1 {
		t.Fatal("a send is audited")
	}
	// Running again sends nothing twice.
	var before int64
	f.app.DB.Model(&models.MailOutbox{}).Count(&before)
	if _, err := jobs.Enqueue(f.app.DB, kindMessageSend, messageSendJob{MessageID: id}, time.Now(), "", 0); err != nil {
		t.Fatal(err)
	}
	runMessageJobs(t, f.app)
	var after int64
	f.app.DB.Model(&models.MailOutbox{}).Count(&after)
	if after != before {
		t.Fatalf("sent again: %d then %d", before, after)
	}
}

// Someone registered for two chosen events gets one copy, and each
// reader's {{manage_link}} is their own date.
func TestMessagePipelineDedupesAcrossEvents(t *testing.T) {
	f := newComposeFixture(t)
	second := createFeedEvent(t, f.leader, f.group.ID, "Second night", feedStart(5), "")
	var occ2 models.EventOccurrence
	f.app.DB.Where("event_id = ?", second.ID).First(&occ2)
	for _, occ := range []uint{f.groupOcc.ID, occ2.ID} {
		f.app.DB.Create(&models.Registration{OccurrenceID: occ, UserID: &f.teen.ID, Status: models.RegGoing})
	}
	id := f.scheduleNow(t, f.leader, "Both nights", "<p>Hi</p>", map[string]any{"type": "events", "eventIds": []uint{f.groupEvent.ID, second.ID}})
	runMessageJobs(t, f.app)
	var n, rows int64
	f.app.DB.Model(&models.MailOutbox{}).Where("to_address = ?", f.teen.Email).Count(&n)
	f.app.DB.Model(&models.MessageRecipient{}).Where("message_id = ? AND user_id = ?", id, f.teen.ID).Count(&rows)
	if n != 1 || rows != 1 {
		t.Fatalf("copies to someone in both events: %d emails, %d rows", n, rows)
	}
	// One date's people: manage_link is that date's page.
	id = f.scheduleNow(t, f.leader, "Tonight", `<p><a href="{{manage_link}}">Change your RSVP</a> {{event_time}}</p>`,
		map[string]any{"type": "occurrence", "occurrenceId": f.groupOcc.ID})
	runMessageJobs(t, f.app)
	got, _ := devOutbox(t, f.app).LatestTo(f.teen.Email)
	if !strings.Contains(got.HTML, occurrenceDatePath(f.groupEvent.ID, f.groupOcc.OriginalDate)) || strings.Contains(got.Body, "{{") {
		t.Fatalf("message %d: %s", id, got.Body)
	}
}

// A leader who steps down between scheduling and sending is refused at
// send time; disabled accounts never get it (DEC-38).
func TestMessagePipelineChecksAgainAtSendTime(t *testing.T) {
	f := newComposeFixture(t)
	aud := map[string]any{"type": "group", "groupId": f.group.ID}
	f.app.DB.Model(&models.User{}).Where("id = ?", f.teen.ID).Update("status", models.UserDisabled)
	id := f.scheduleNow(t, f.leader, "One", "<p>Hi</p>", aud)
	runMessageJobs(t, f.app)
	if rows := recipientsOf(t, f.app, id); rows[f.teen.Email].Status != models.RecipientSuppressed || rows[f.teen.Email].Reason != skipNotActive {
		t.Fatalf("disabled: %+v", rows[f.teen.Email])
	}
	id = f.scheduleNow(t, f.leader, "Two", "<p>Hi</p>", aud)
	f.app.DB.Model(&models.Membership{}).Where("group_id = ? AND user_id = ?", f.group.ID, f.leaderUser.ID).Update("role", models.MembershipMember)
	var before int64
	f.app.DB.Model(&models.MailOutbox{}).Count(&before)
	runMessageJobs(t, f.app)
	var m models.Message
	f.app.DB.First(&m, id)
	var after int64
	f.app.DB.Model(&models.MailOutbox{}).Count(&after)
	if m.Status != models.MessageFailed || m.Error != whyAuthorRefused || after != before || auditCount(t, f.app, audit.MessageRefused) != 1 {
		t.Fatalf("demoted: %+v, sent %d", m, after-before)
	}
}

// A message cancelled before its time sends nothing.
func TestMessagePipelineHonorsCancel(t *testing.T) {
	f := newComposeFixture(t)
	id := f.scheduleNow(t, f.leader, "Soon", "<p>Hi</p>", map[string]any{"type": "group", "groupId": f.group.ID})
	if w := f.leader.do("POST", "/api/v1/messages/"+itoa(id)+"/cancel", nil, true); w.Code != http.StatusOK {
		t.Fatalf("cancel: %d", w.Code)
	}
	runMessageJobs(t, f.app)
	var n int64
	f.app.DB.Model(&models.MessageRecipient{}).Where("message_id = ?", id).Count(&n)
	if n != 0 {
		t.Fatalf("a cancelled message made %d rows", n)
	}
}

// Guardian copies (DEC-19, DEC-29): off by default; on, each confirmed
// guardian gets one marked email copy of a message to their teen.
func TestMessagePipelineGuardianCopies(t *testing.T) {
	f := newComposeFixture(t)
	parent := newTestUser(t, f.app, "parent", "password1234", models.RoleMember)
	pending := newTestUser(t, f.app, "pending", "password1234", models.RoleMember)
	f.app.DB.Create(&models.GuardianLink{GuardianUserID: parent.ID, TeenUserID: &f.teen.ID, Status: models.GuardianConfirmed})
	f.app.DB.Create(&models.GuardianLink{GuardianUserID: pending.ID, TeenUserID: &f.teen.ID, Status: models.GuardianPending})
	aud := map[string]any{"type": "group", "groupId": f.group.ID}
	f.scheduleNow(t, f.leader, "Off", "<p>Hi</p>", aud)
	runMessageJobs(t, f.app)
	if _, err := devOutbox(t, f.app).LatestTo(parent.Email); err == nil {
		t.Fatal("a copy went out with guardian copies off")
	}
	f.app.DB.Create(&models.Setting{Key: settingGuardianCopies, Value: "true"})
	id := f.scheduleNow(t, f.leader, "On", "<p>Hi {{first_name}}</p>", aud)
	runMessageJobs(t, f.app)
	rows := recipientsOf(t, f.app, id)
	if rows["copy:"+parent.Email].Status != models.RecipientSent || len(rows) != 5 {
		t.Fatalf("copies: %+v", rows)
	}
	got, err := devOutbox(t, f.app).LatestTo(parent.Email)
	if err != nil || got.Subject != "Copy: On" || !strings.Contains(got.Body, "Copy: sent to "+f.teen.PublicName()) ||
		!strings.Contains(got.Body, "Hi "+firstName(f.teen.PublicName())) {
		t.Fatalf("copy: %+v %v", got, err)
	}
	if _, err := devOutbox(t, f.app).LatestTo(pending.Email); err == nil {
		t.Fatal("an unconfirmed guardian got a copy")
	}
}

// failingSender stands in for a provider that is down, or that refuses.
type failingSender struct{ permanent bool }

func (s failingSender) Send(ctx context.Context, m mail.Message) error {
	_, err := s.SendEmail(ctx, m)
	return err
}

func (s failingSender) SendEmail(context.Context, mail.Message) (string, error) {
	return "", &providers.SendError{Provider: "fake", Code: 503, Message: "service unavailable", Permanent: s.permanent}
}

func (failingSender) Capabilities() providers.EmailCaps { return providers.EmailCaps{Name: "fake"} }

// An outage is retried and then shown as a failed state with the error
// (M3.5 acceptance); a refusal fails the person at once.
func TestMessagePipelineShowsAProviderOutage(t *testing.T) {
	f := newComposeFixture(t)
	f.app.Mailer = failingSender{}
	id := f.scheduleNow(t, f.leader, "Down", "<p>Hi</p>", map[string]any{"type": "group", "groupId": f.group.ID})
	runMessageJobs(t, f.app)
	var m models.Message
	f.app.DB.First(&m, id)
	if m.Status != models.MessageFailed || !strings.Contains(m.Error, "service unavailable") {
		t.Fatalf("outage: %+v", m)
	}
	var attempts int
	f.app.DB.Model(&models.Job{}).Where("kind = ?", kindMessageBatch).Pluck("attempts", &attempts)
	if attempts != messageBatchAttempts {
		t.Fatalf("batch attempts: %d", attempts)
	}
	if rows := recipientsOf(t, f.app, id); rows[f.teen.Email].Status != models.RecipientFailed || rows[f.teen.Email].Error == "" {
		t.Fatalf("rows: %+v", rows)
	}
	f.app.Mailer = failingSender{permanent: true}
	id = f.scheduleNow(t, f.leader, "Refused", "<p>Hi</p>", map[string]any{"type": "group", "groupId": f.group.ID})
	runMessageJobs(t, f.app)
	f.app.DB.First(&m, id)
	if m.Status != models.MessageFailed {
		t.Fatalf("refused: %+v", m)
	}
}

// Delivery and bounce reports move an announcement's rows forward.
func TestMessagePipelineRecordsReports(t *testing.T) {
	f := newComposeFixture(t)
	id := f.scheduleNow(t, f.leader, "Report", "<p>Hi</p>", map[string]any{"type": "group", "groupId": f.group.ID})
	runMessageJobs(t, f.app)
	row := recipientsOf(t, f.app, id)[f.teen.Email]
	if err := f.app.applyEmailEvent(context.Background(), providers.Event{Kind: providers.EventDelivered, MessageID: row.ProviderMessageID, At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if got := recipientsOf(t, f.app, id)[f.teen.Email]; got.Status != models.RecipientDelivered {
		t.Fatalf("delivered: %+v", got)
	}
	if err := f.app.applyEmailEvent(context.Background(), providers.Event{Kind: providers.EventBounced, Permanent: true, MessageID: row.ProviderMessageID,
		Address: f.teen.Email, At: time.Now()}); err != nil && !errors.Is(err, errDeliveryNotYet) {
		t.Fatal(err)
	}
	if got := recipientsOf(t, f.app, id)[f.teen.Email]; got.Status != models.RecipientBounced || got.Reason != "hard bounce" {
		t.Fatalf("bounced: %+v", got)
	}
}

// A leader may send 5 messages a day to one group's people; admins have
// no limit (PLAN 7.5).
func TestMessageDailyLimit(t *testing.T) {
	f := newComposeFixture(t)
	aud := map[string]any{"type": "group", "groupId": f.group.ID}
	for range dailyMessagesPerGroup {
		f.scheduleNow(t, f.leader, "Again", "<p>Hi</p>", aud)
	}
	var out struct {
		Message messageJSON `json:"message"`
	}
	decodeJSON(t, f.leader.do("POST", "/api/v1/messages", map[string]any{"subject": "Sixth", "bodyHtml": "<p>Hi</p>", "audience": aud}, true), &out)
	if w := f.leader.do("POST", "/api/v1/messages/"+itoa(out.Message.ID)+"/schedule", map[string]any{}, true); w.Code != http.StatusBadRequest ||
		!strings.Contains(w.Body.String(), "message_daily_limit") {
		t.Fatalf("sixth: %d %s", w.Code, w.Body.String())
	}
	// Another day is fine, and an admin isn't limited.
	if w := f.leader.do("POST", "/api/v1/messages/"+itoa(out.Message.ID)+"/schedule", map[string]any{"scheduledAt": time.Now().Add(49 * time.Hour)}, true); w.Code != http.StatusOK {
		t.Fatalf("another day: %d %s", w.Code, w.Body.String())
	}
	for range dailyMessagesPerGroup + 1 {
		f.scheduleNow(t, f.admin, "Admin", "<p>Hi</p>", aud)
	}
}

// pickySender passes everything to the outbox except mail to one address,
// which it refuses the way an SMTP server does: 550 is permanent, 451 may
// pass.
type pickySender struct {
	next mail.Sender
	bad  string
	code int
}

func (s pickySender) Send(ctx context.Context, m mail.Message) error {
	_, err := s.SendEmail(ctx, m)
	return err
}

func (s pickySender) SendEmail(ctx context.Context, m mail.Message) (string, error) {
	if m.To == s.bad {
		return "", fmt.Errorf("mail: rcpt: %w", &textproto.Error{Code: s.code, Msg: "no"})
	}
	return mail.Unwrap(s.next).(providers.EmailSender).SendEmail(ctx, m)
}

func (pickySender) Capabilities() providers.EmailCaps { return providers.EmailCaps{Name: "picky"} }

// One bad address fails only its own row; the rest of its batch still
// goes. A 5xx fails at once; a 4xx is retried, then fails alone.
func TestMessagePipelineOneBadAddress(t *testing.T) {
	for _, code := range []int{550, 451} {
		f := newComposeFixture(t)
		f.app.Mailer = pickySender{next: f.app.Mailer, bad: f.leaderUser.Email, code: code}
		id := f.scheduleNow(t, f.leader, "Mixed", "<p>Hi</p>", map[string]any{"type": "group", "groupId": f.group.ID})
		runMessageJobs(t, f.app)
		rows := recipientsOf(t, f.app, id)
		var m models.Message
		f.app.DB.First(&m, id)
		if rows[f.leaderUser.Email].Status != models.RecipientFailed || rows[f.teen.Email].Status != models.RecipientSent ||
			m.Status != models.MessageSent || m.Error == "" {
			t.Fatalf("%d: message %+v, rows %+v", code, m, rows)
		}
	}
}

// A guardian the message already reaches directly gets no second copy.
func TestMessagePipelineNoCopyForADirectGuardian(t *testing.T) {
	f := newComposeFixture(t)
	f.app.DB.Create(&models.GuardianLink{GuardianUserID: f.leaderUser.ID, TeenUserID: &f.teen.ID, Status: models.GuardianConfirmed})
	f.app.DB.Create(&models.Setting{Key: settingGuardianCopies, Value: "true"})
	id := f.scheduleNow(t, f.leader, "Direct", "<p>Hi</p>", map[string]any{"type": "group", "groupId": f.group.ID})
	runMessageJobs(t, f.app)
	if _, ok := recipientsOf(t, f.app, id)["copy:"+f.leaderUser.Email]; ok {
		t.Fatal("a guardian who got it directly also got a copy")
	}
}

// A series reader's date is their own next registration in the
// audience's statuses, not a date they declined.
func TestMessagePipelineSeriesDateFollowsTheReader(t *testing.T) {
	f := newComposeFixture(t)
	ev := createFeedEvent(t, f.leader, f.group.ID, "Weekly", feedStart(2), "FREQ=WEEKLY;COUNT=3")
	var occs []models.EventOccurrence
	f.app.DB.Where("event_id = ?", ev.ID).Order("starts_at").Find(&occs)
	if len(occs) < 2 {
		t.Fatalf("occurrences: %d", len(occs))
	}
	f.app.DB.Create(&models.Registration{OccurrenceID: occs[0].ID, UserID: &f.teen.ID, Status: models.RegDeclined})
	f.app.DB.Create(&models.Registration{OccurrenceID: occs[1].ID, UserID: &f.teen.ID, Status: models.RegGoing})
	f.scheduleNow(t, f.leader, "Weekly", `<p><a href="{{manage_link}}">Your date</a></p>`, map[string]any{"type": "event_series", "eventId": ev.ID})
	runMessageJobs(t, f.app)
	got, _ := devOutbox(t, f.app).LatestTo(f.teen.Email)
	if !strings.Contains(got.HTML, occurrenceDatePath(ev.ID, occs[1].OriginalDate)) {
		t.Fatalf("manage link: %s", got.HTML)
	}
}

// The daily backstop settles a send that stopped with no job left.
func TestReapMessages(t *testing.T) {
	f := newComposeFixture(t)
	old := time.Now().Add(-2 * time.Hour)
	stuck := models.Message{AuthorID: &f.leaderUser.ID, Status: models.MessageScheduled, Subject: "s", Audience: "{}", ScheduledAt: &old}
	sending := models.Message{AuthorID: &f.leaderUser.ID, Status: models.MessageSending, Subject: "s", Audience: "{}", ScheduledAt: &old}
	f.app.DB.Create(&stuck)
	f.app.DB.Create(&sending)
	f.app.DB.Model(&models.Message{}).Where("id IN ?", []uint{stuck.ID, sending.ID}).Update("updated_at", old)
	f.app.DB.Create(&models.MessageRecipient{MessageID: sending.ID, UserID: f.teen.ID, Status: models.RecipientQueued, Address: f.teen.Email})
	if err := f.app.reapMessages(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	f.app.DB.First(&stuck, stuck.ID)
	f.app.DB.First(&sending, sending.ID)
	if stuck.Status != models.MessageFailed || stuck.Error != whyNotPrepared || sending.Status != models.MessageFailed || sending.Error != whyStopped {
		t.Fatalf("stuck %+v, sending %+v", stuck, sending)
	}
}

// The daily limit in the error copy is the limit the server enforces.
func TestDailyLimitCopyMatches(t *testing.T) {
	b, err := os.ReadFile("web/src/lib/errors.ts")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "already sent "+strconv.Itoa(dailyMessagesPerGroup)+" messages") || !strings.Contains(errMessageDailyLimit, strconv.Itoa(dailyMessagesPerGroup)) {
		t.Fatal("the message_daily_limit copy doesn't match dailyMessagesPerGroup")
	}
}

// A claim that misses because of an edit retries while the message is
// still due, and lets go once it is moved later or no longer scheduled.
func TestStillDue(t *testing.T) {
	f := newComposeFixture(t)
	past, later := time.Now().Add(-time.Minute), time.Now().Add(time.Hour)
	due := models.Message{AuthorID: &f.leaderUser.ID, Status: models.MessageScheduled, Subject: "s", Audience: "{}", ScheduledAt: &past}
	moved := models.Message{AuthorID: &f.leaderUser.ID, Status: models.MessageScheduled, Subject: "s", Audience: "{}", ScheduledAt: &later}
	sent := models.Message{AuthorID: &f.leaderUser.ID, Status: models.MessageSent, Subject: "s", Audience: "{}", ScheduledAt: &past}
	for _, m := range []*models.Message{&due, &moved, &sent} {
		f.app.DB.Create(m)
	}
	if err := stillDue(f.app.DB, due.ID); !errors.Is(err, errChangedWhilePreparing) {
		t.Fatalf("due: %v", err)
	}
	if stillDue(f.app.DB, moved.ID) != nil || stillDue(f.app.DB, sent.ID) != nil {
		t.Fatal("a moved or sent message isn't retried")
	}
}
