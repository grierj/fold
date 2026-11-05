package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/textproto"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// recipientsOf is a message's email rows by address ("copy:" for a
// guardian copy); pushRowsOf has its push rows.
func recipientsOf(t *testing.T, app *App, id uint) map[string]models.MessageRecipient {
	t.Helper()
	var rows []models.MessageRecipient
	app.DB.Where("message_id = ? AND channel = ?", id, models.DeliveryEmail).Find(&rows)
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
	if !strings.Contains(got.HTML, "/events/"+itoa(f.groupEvent.ID)+"/dates/"+f.groupOcc.StartsAt.UTC().Format("2006-01-02")) || strings.Contains(got.Body, "{{") {
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
	if !strings.Contains(got.HTML, "/events/"+itoa(ev.ID)+"/dates/"+occs[1].StartsAt.UTC().Format("2006-01-02")) {
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

// The send snapshot and the mail that goes out treat a hashed row and a
// legacy NULL-hash row alike, in either order: skipped people are recorded
// with the preview's words and get nothing, a cleared row skips nobody.
func TestMessagePipelineSnapshotSkipsHashedAndLegacySuppressions(t *testing.T) {
	f := newComposeFixture(t)
	cases := suppressionCases()
	users := f.addSuppressionMembers(t, cases)
	for _, tc := range cases {
		suppressSpecs(t, f.app, strings.ToLower(users[tc.name].Email), tc.specs...)
	}
	id := f.scheduleNow(t, f.leader, "Book night", "<p>Hi</p>", map[string]any{"type": "group", "groupId": f.group.ID})
	runMessageJobs(t, f.app)
	rows := recipientsOf(t, f.app, id)
	for _, tc := range cases {
		u := users[tc.name]
		row, mails := rows[u.Email], mailCount(t, f.app, u.Email)
		if skip := tc.snapshotSkip(); skip != "" {
			if row.Status != models.RecipientSuppressed || row.Reason != skip || mails != 0 {
				t.Errorf("%s: row %+v, %d emails, want suppressed %q and none sent", tc.name, row, mails, skip)
			}
		} else if row.Status != models.RecipientSent || row.ProviderMessageID == "" || mails != 1 {
			t.Errorf("%s: row %+v, %d emails, want sent once", tc.name, row, mails)
		}
	}
}

// failSetSender refuses mail to its listed addresses with a permanent 550 and
// passes the rest to the real sender.
type failSetSender struct {
	next mail.Sender
	bad  map[string]bool
}

func (s failSetSender) Send(ctx context.Context, m mail.Message) error {
	_, err := s.SendEmail(ctx, m)
	return err
}

func (s failSetSender) SendEmail(ctx context.Context, m mail.Message) (string, error) {
	if s.bad[m.To] {
		return "", fmt.Errorf("mail: rcpt: %w", &textproto.Error{Code: 550, Msg: "no"})
	}
	return mail.Unwrap(s.next).(providers.EmailSender).SendEmail(ctx, m)
}

func (failSetSender) Capabilities() providers.EmailCaps {
	return providers.EmailCaps{Name: "fail-set"}
}

// A suppression that appears after the snapshot is read again at send time,
// including on Try again: hashed and legacy rows alike stop the mail, with
// the label of the older row, and cleared rows do not.
func TestMessageRetryReadsHashedAndLegacySuppressionsAtSendTime(t *testing.T) {
	f := newComposeFixture(t)
	cases := suppressionCases()
	users := f.addSuppressionMembers(t, cases)
	bad := map[string]bool{}
	for _, u := range users {
		bad[u.Email] = true
	}
	next := f.app.Mailer
	f.app.Mailer = failSetSender{next: next, bad: bad}
	id := f.scheduleNow(t, f.leader, "Book night", "<p>Hi</p>", map[string]any{"type": "group", "groupId": f.group.ID})
	runMessageJobs(t, f.app)
	f.app.Mailer = next
	rows := recipientsOf(t, f.app, id)
	for _, tc := range cases {
		if r := rows[users[tc.name].Email]; r.Status != models.RecipientFailed {
			t.Fatalf("%s: first send left %+v, want failed", tc.name, r)
		}
	}
	// The rows arrive after the snapshot; the retry goes straight to the send
	// batch, which reads them once for everyone.
	for _, tc := range cases {
		suppressSpecs(t, f.app, strings.ToLower(users[tc.name].Email), tc.specs...)
	}
	if w := f.leader.do("POST", "/api/v1/messages/"+itoa(id)+"/retry", nil, true); w.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", w.Code, w.Body.String())
	}
	runMessageJobs(t, f.app)
	rows = recipientsOf(t, f.app, id)
	for _, tc := range cases {
		u := users[tc.name]
		row, mails := rows[u.Email], mailCount(t, f.app, u.Email)
		if skip := tc.sendTimeSkip(); skip != "" {
			if row.Status != models.RecipientSuppressed || row.Reason != skip || mails != 0 {
				t.Errorf("%s: row %+v, %d emails, want suppressed %q and none sent", tc.name, row, mails, skip)
			}
		} else if row.Status != models.RecipientSent || row.ProviderMessageID == "" || mails != 1 {
			t.Errorf("%s: row %+v, %d emails, want sent once", tc.name, row, mails)
		}
	}
}

// Announcement send reads the open reasons once per batch: 1,201 distinct
// addresses take three reads and nothing is lost or duplicated. Optional
// mail follows the older row, which was unspecified before R4-S: complaint
// first gives the complaint label, hard bounce first the bounce one.
func TestBatchSuppressionsReadsInChunks(t *testing.T) {
	app := testApp(t)
	suppressHashed(t, app, "hashed@example.com", models.SuppressHardBounce)
	suppress(t, app, "legacy@example.com", models.SuppressComplaint)
	suppressHashed(t, app, "complaintfirst@example.com", models.SuppressComplaint)
	suppressHashed(t, app, "complaintfirst@example.com", models.SuppressHardBounce)
	suppress(t, app, "bouncefirst@example.com", models.SuppressHardBounce)
	suppress(t, app, "bouncefirst@example.com", models.SuppressComplaint)
	// Matches in every chunk (500, 500 and 201 distinct inputs), on both sides
	// of the 500/501 and 1000/1001 boundaries, hashed and legacy: a lookup
	// that discarded or repeated the results of any read fails the complete
	// map below.
	suppress(t, app, "first-legacy@example.com", models.SuppressManual)            // input 0, chunk 1
	suppressHashed(t, app, "last1-hashed@example.com", models.SuppressManual)      // input 499, chunk 1
	suppressHashed(t, app, "first2-hashed@example.com", models.SuppressHardBounce) // input 500, chunk 2
	suppress(t, app, "last2-legacy@example.com", models.SuppressComplaint)         // input 999, chunk 2
	suppress(t, app, "first3-legacy@example.com", models.SuppressHardBounce)       // input 1000, chunk 3

	placed := map[int]string{
		0:    "First-Legacy@example.com",
		499:  "last1-hashed@example.com ",
		500:  "first2-hashed@example.com",
		999:  " LAST2-legacy@example.com",
		1000: "first3-legacy@example.com",
		// The older four sit in the last chunk too.
		1197: " Hashed@Example.com",
		1198: "legacy@example.com",
		1199: "complaintfirst@example.com",
		1200: "BounceFirst@example.com ",
	}
	var rows []models.MessageRecipient
	for i := range 1201 {
		a, ok := placed[i]
		if !ok {
			a = fmt.Sprintf("u%d@example.com", i)
		}
		rows = append(rows, models.MessageRecipient{Address: a})
	}
	rows = append(rows, models.MessageRecipient{Address: "hashed@example.com"}) // a repeat is read once
	counter := countDBReads(t, app.DB)
	got, err := batchSuppressions(app.DB, rows)
	reads := counter.Reads()
	counter.Stop()
	if err != nil {
		t.Fatal(err)
	}
	if reads != 3 {
		t.Fatalf("1,201 distinct addresses made %d reads, want 3", reads)
	}
	wantReasons := map[string][]string{
		"first-legacy@example.com":   {models.SuppressManual},
		"last1-hashed@example.com":   {models.SuppressManual},
		"first2-hashed@example.com":  {models.SuppressHardBounce},
		"last2-legacy@example.com":   {models.SuppressComplaint},
		"first3-legacy@example.com":  {models.SuppressHardBounce},
		"hashed@example.com":         {models.SuppressHardBounce},
		"legacy@example.com":         {models.SuppressComplaint},
		"complaintfirst@example.com": {models.SuppressComplaint, models.SuppressHardBounce},
		"bouncefirst@example.com":    {models.SuppressHardBounce, models.SuppressComplaint},
	}
	if !reflect.DeepEqual(got, wantReasons) {
		t.Fatalf("lost or duplicated rows: got %v, want %v", got, wantReasons)
	}
	for addr, wantReason := range map[string]string{
		"complaintfirst@example.com": models.SuppressComplaint,
		"bouncefirst@example.com":    models.SuppressHardBounce,
		"hashed@example.com":         models.SuppressHardBounce,
	} {
		err := mail.Blocked(got[addr], providers.EmailMessage{Optional: true})
		var s *mail.ErrSuppressed
		if !mail.IsSuppressed(err) || !errors.As(err, &s) || s.Reason != wantReason {
			t.Errorf("%s: optional mail refused with %v, want reason %s", addr, err, wantReason)
		}
	}
	// Required mail: a complaint alone lets it through; with a hard bounce
	// beside it the bounce refuses it, in either order.
	for _, addr := range []string{"complaintfirst@example.com", "bouncefirst@example.com"} {
		err := mail.Blocked(got[addr], providers.EmailMessage{})
		var s *mail.ErrSuppressed
		if !errors.As(err, &s) || s.Reason != models.SuppressHardBounce {
			t.Errorf("%s: required mail refused with %v, want hard_bounce", addr, err)
		}
	}
	if err := mail.Blocked(got["legacy@example.com"], providers.EmailMessage{}); err != nil {
		t.Errorf("complaint alone blocked required mail: %v", err)
	}
}

// ---------------------------------------------------------------------------
// R4-2a-4: erasure and the message pipeline.
// ---------------------------------------------------------------------------

// erasureTestSendKey names one message by its recipient address and subject.
func erasureTestSendKey(to, subject string) string { return normalizeEmail(to) + "|" + subject }

type erasureTestBlockKey struct {
	reached     chan struct{}
	release     chan struct{}
	reachedOnce sync.Once
	releaseOnce sync.Once
}

// erasureTestBlockingSender is a mail.Sender for the erasure tests (F2). A
// message that matches one of its keys closes that key's reached channel and
// blocks until the key is released or the send context ends; any other
// message is recorded and returns at once, so it never holds a row lock.
type erasureTestBlockingSender struct {
	mu     sync.Mutex
	keys   map[string]*erasureTestBlockKey
	others []mail.Message
}

// newErasureTestBlockingSender blocks the given keys (erasureTestSendKey). Its
// cleanup releases every key before the test ends.
func newErasureTestBlockingSender(t *testing.T, keys ...string) *erasureTestBlockingSender {
	t.Helper()
	s := &erasureTestBlockingSender{keys: map[string]*erasureTestBlockKey{}}
	for _, k := range keys {
		s.keys[k] = &erasureTestBlockKey{reached: make(chan struct{}), release: make(chan struct{})}
	}
	t.Cleanup(s.ReleaseAll)
	return s
}

func (s *erasureTestBlockingSender) Send(ctx context.Context, m mail.Message) error {
	k := s.keys[erasureTestSendKey(m.To, m.Subject)]
	if k == nil {
		s.mu.Lock()
		s.others = append(s.others, m)
		s.mu.Unlock()
		return nil
	}
	k.reachedOnce.Do(func() { close(k.reached) })
	select {
	case <-k.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Reached closes when a message with the key is inside Send.
func (s *erasureTestBlockingSender) Reached(key string) <-chan struct{} { return s.keys[key].reached }

// Release lets the blocked send for key return nil.
func (s *erasureTestBlockingSender) Release(key string) {
	k := s.keys[key]
	k.releaseOnce.Do(func() { close(k.release) })
}

func (s *erasureTestBlockingSender) ReleaseAll() {
	for key := range s.keys {
		s.Release(key)
	}
}

// Others are the messages that matched no key.
func (s *erasureTestBlockingSender) Others() []mail.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]mail.Message(nil), s.others...)
}

func snapshotCtx() context.Context {
	return context.WithValue(context.Background(), testDBOperationKey{}, "snapshot-prepare")
}

// snapshotPoint is the snapshot's locking read of users: facts GORM sets
// before gorm:query (the table, the FOR clause and the operation), so a Before
// barrier matches it (F6).
func snapshotPoint(after bool) erasureTestPoint {
	return erasureTestPoint{Processor: "query", Step: "gorm:query", After: after, Table: "users", Locking: true, Operation: "snapshot-prepare"}
}

func snapshotRowsOf(t *testing.T, app *App, messageID, userID uint) []models.MessageRecipient {
	t.Helper()
	var rows []models.MessageRecipient
	if err := app.DB.Where("message_id = ? AND user_id = ?", messageID, userID).Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

// snapshotSendsAfterErasure pauses prepareMessageOnce before it locks its
// users, erases the person, and lets the snapshot go on.
func snapshotSendsAfterErasure(t *testing.T, f composeFixture, id, erased uint) {
	t.Helper()
	reached, release := erasureTestPause(t, f.app.DB, snapshotPoint(false))
	done := make(chan error, 1)
	go func() { done <- f.app.prepareMessageOnce(snapshotCtx(), id) }()
	awaitDBSignal(t, reached)
	if r := deleteAccount(f.admin, erased, nil); r.code != http.StatusOK {
		t.Fatalf("delete: %d %s", r.code, r.body)
	}
	release()
	if err := awaitDBResult(t, done); err != nil {
		t.Fatalf("prepare: %v", err)
	}
}

func snapshotPerson(t *testing.T, f composeFixture, name string) *models.User {
	t.Helper()
	u := newTestUser(t, f.app, name, "password1234", models.RoleMember)
	addMember(t, f.app, u, f.group, models.MembershipMember)
	return u
}

func snapshotAddressCount(t *testing.T, app *App, address string) int64 {
	t.Helper()
	var n int64
	if err := app.DB.Raw("SELECT count(*) FROM message_recipients WHERE lower(address) = lower(?)", address).Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

// The snapshot locks every person it names before it claims the message and
// reads them again under that lock, so an erasure on either side of the lock
// leaves no row holding the erased address.
func TestMessageSnapshotRevalidatesRecipientsUnderLock(t *testing.T) {
	t.Run("direct recipient erased before the lock", func(t *testing.T) {
		f := newComposeFixture(t)
		f.admin.holdNotices, f.leader.holdNotices = true, true
		useFakePush(t, f.app)
		u := snapshotPerson(t, f, "snap-u")
		pushConsentRow(t, f.app, u, "all", models.ConsentOptedIn)
		newPushDevice(t, f.app, u, "snap-phone")
		address := strings.ToLower(u.Email)
		id := f.scheduleNow(t, f.leader, "Snapshot A", "<p>Hi</p>", groupAudience(f))
		snapshotSendsAfterErasure(t, f, id, u.ID)

		rows := snapshotRowsOf(t, f.app, id, u.ID)
		if len(rows) != 1 || rows[0].Channel != models.DeliveryEmail || rows[0].Status != models.RecipientSuppressed ||
			rows[0].Reason != skipNotActive || rows[0].Address != "" {
			t.Errorf("the erased person's rows: %+v, want one suppressed email row with no address and no push row", rows)
		}
		var m models.Message
		f.app.DB.First(&m, id)
		var queued int64
		f.app.DB.Model(&models.MessageRecipient{}).Where("message_id = ? AND channel = ? AND status = ?", id, models.DeliveryEmail, models.RecipientQueued).Count(&queued)
		if queued == 0 || int64(m.RecipientCount) != queued {
			t.Errorf("recipient_count %d, queued email rows %d", m.RecipientCount, queued)
		}
		teen := snapshotRowsOf(t, f.app, id, f.teen.ID)
		if len(teen) == 0 || teen[0].Status != models.RecipientQueued || teen[0].Address != f.teen.Email {
			t.Errorf("another recipient changed: %+v", teen)
		}
		if n := snapshotAddressCount(t, f.app, address); n != 0 {
			t.Errorf("%d rows still hold the erased address", n)
		}
	})

	t.Run("guardian copy for an erased guardian", func(t *testing.T) {
		f := newComposeFixture(t)
		f.admin.holdNotices, f.leader.holdNotices = true, true
		f.app.DB.Create(&models.Setting{Key: settingGuardianCopies, Value: "true"})
		guardian := newTestUser(t, f.app, "snap-guardian", "password1234", models.RoleMember)
		f.app.DB.Create(&models.GuardianLink{GuardianUserID: guardian.ID, TeenUserID: &f.teen.ID, Status: models.GuardianConfirmed})
		address := strings.ToLower(guardian.Email)
		id := f.scheduleNow(t, f.leader, "Snapshot B", "<p>Hi</p>", groupAudience(f))
		snapshotSendsAfterErasure(t, f, id, guardian.ID)

		rows := snapshotRowsOf(t, f.app, id, guardian.ID)
		if len(rows) != 1 || rows[0].CopyOfUserID == nil || *rows[0].CopyOfUserID != f.teen.ID ||
			rows[0].Status != models.RecipientSuppressed || rows[0].Reason != skipNotActive || rows[0].Address != "" {
			t.Errorf("the guardian's copy row: %+v", rows)
		}
		direct := snapshotRowsOf(t, f.app, id, f.teen.ID)
		if len(direct) != 1 || direct[0].Status != models.RecipientQueued || direct[0].Address != f.teen.Email {
			t.Errorf("the teen's direct row changed: %+v", direct)
		}
		if n := snapshotAddressCount(t, f.app, address); n != 0 {
			t.Errorf("%d rows still hold the erased address", n)
		}
	})

	t.Run("erased teen", func(t *testing.T) {
		f := newComposeFixture(t)
		f.admin.holdNotices, f.leader.holdNotices = true, true
		f.app.DB.Create(&models.Setting{Key: settingGuardianCopies, Value: "true"})
		u := snapshotPerson(t, f, "snap-teen")
		f.app.DB.Model(u).Update("account_type", models.AccountTeen)
		parent := newTestUser(t, f.app, "snap-parent", "password1234", models.RoleMember)
		f.app.DB.Create(&models.GuardianLink{GuardianUserID: parent.ID, TeenUserID: &u.ID, Status: models.GuardianConfirmed})
		address := strings.ToLower(u.Email)
		id := f.scheduleNow(t, f.leader, "Snapshot C", "<p>Hi</p>", groupAudience(f))
		snapshotSendsAfterErasure(t, f, id, u.ID)

		rows := snapshotRowsOf(t, f.app, id, u.ID)
		if len(rows) != 1 || rows[0].Status != models.RecipientSuppressed || rows[0].Reason != skipNotActive || rows[0].Address != "" {
			t.Errorf("the erased teen's row: %+v", rows)
		}
		var copies int64
		f.app.DB.Model(&models.MessageRecipient{}).Where("copy_of_user_id = ?", u.ID).Count(&copies)
		if copies != 0 {
			t.Errorf("%d guardian copies about the erased teen", copies)
		}
		if n := snapshotAddressCount(t, f.app, address); n != 0 {
			t.Errorf("%d rows still hold the erased address", n)
		}
	})

	t.Run("snapshot first", func(t *testing.T) {
		f := newComposeFixture(t)
		f.admin.holdNotices, f.leader.holdNotices = true, true
		u := snapshotPerson(t, f, "snap-late")
		address := strings.ToLower(u.Email)
		id := f.scheduleNow(t, f.leader, "Snapshot D", "<p>Hi</p>", groupAudience(f))
		var prepPID atomic.Int64
		p := snapshotPoint(true)
		p.PID = &prepPID
		reached, release := erasureTestPause(t, f.app.DB, p)
		done := make(chan error, 1)
		go func() { done <- f.app.prepareMessageOnce(snapshotCtx(), id) }()
		awaitDBSignal(t, reached) // the snapshot holds U FOR KEY SHARE
		del := bgDo(f.admin, "DELETE", "/api/v1/admin/users/"+itoa(u.ID), nil)
		erasurePID := waitForRowLockWaiter(t, f.app)
		waitDBBlocked(t, f.app.DB, erasurePID, int(prepPID.Load()))
		release()
		if err := awaitDBResult(t, done); err != nil {
			t.Fatalf("prepare: %v", err)
		}
		if r := awaitResult(t, del); r.code != http.StatusOK {
			t.Fatalf("delete: %d %s", r.code, r.body)
		}
		rows := snapshotRowsOf(t, f.app, id, u.ID)
		if len(rows) != 1 || rows[0].Status != models.RecipientSuppressed || rows[0].Reason != skipNotActive || rows[0].Address != "" {
			t.Errorf("the snapshot's row for the erased person after the erasure: %+v", rows)
		}
		if n := snapshotAddressCount(t, f.app, address); n != 0 {
			t.Errorf("%d rows still hold the erased address", n)
		}
	})
}
