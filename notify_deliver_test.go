package main

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"fold/internal/jobs"
	"fold/internal/mail"
	"fold/internal/notify"
	"fold/internal/providers"
	"fold/models"
)

// notifyFailSender is a mailer whose error and call count a test sets.
// onSend runs inside SendEmail, still holding the lock.
type notifyFailSender struct {
	mu     sync.Mutex
	err    error
	calls  int
	onSend func()
}

func (s *notifyFailSender) Send(ctx context.Context, m mail.Message) error {
	_, err := s.SendEmail(ctx, m)
	return err
}

func (s *notifyFailSender) SendEmail(context.Context, mail.Message) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.onSend != nil {
		s.onSend()
	}
	return "", s.err
}

func (s *notifyFailSender) Capabilities() providers.EmailCaps {
	return providers.EmailCaps{Name: "notify-fail"}
}

func (s *notifyFailSender) n() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// notifyPlainSender sends mail and reports no provider id.
type notifyPlainSender struct{}

func (notifyPlainSender) Send(context.Context, mail.Message) error { return nil }

// seedNotifyDeliver writes a notice, a queued email row, and a job whose
// key is the literal notify.deliver:<id>.
func seedNotifyDeliver(t *testing.T, app *App, u *models.User, attempts int) (uint, models.Job) {
	t.Helper()
	id := sendNotice(t, app, notify.GroupApproved, u)
	if err := app.DB.Create(&models.NotificationDelivery{
		NotificationID: id, Channel: models.DeliveryEmail, UserID: u.ID, Status: models.DeliveryQueued,
	}).Error; err != nil {
		t.Fatal(err)
	}
	key := "notify.deliver:" + strconv.FormatUint(uint64(id), 10)
	if _, err := jobs.Enqueue(app.DB, notify.DeliverKind, notifyDeliverJob{NotificationID: id}, time.Now(), key, attempts); err != nil {
		t.Fatal(err)
	}
	var job models.Job
	if err := app.DB.Where("unique_key = ?", key).First(&job).Error; err != nil {
		t.Fatal(err)
	}
	return id, job
}

func runNotifyOnce(t *testing.T, app *App) {
	t.Helper()
	ran, err := jobs.NewRunner(app.DB, app.notifyKinds()...).RunOnce(context.Background(), "test")
	if err != nil || !ran {
		t.Fatalf("run: ran %v err %v", ran, err)
	}
}

func reloadJob(t *testing.T, app *App, id uint) models.Job {
	t.Helper()
	var job models.Job
	if err := app.DB.First(&job, id).Error; err != nil {
		t.Fatal(err)
	}
	return job
}

func emailDeliveryOf(t *testing.T, app *App, notificationID uint) models.NotificationDelivery {
	t.Helper()
	var d models.NotificationDelivery
	if err := app.DB.Where("notification_id = ? AND channel = ?", notificationID, models.DeliveryEmail).First(&d).Error; err != nil {
		t.Fatal(err)
	}
	return d
}

func mailCount(t *testing.T, app *App, to string) int64 {
	t.Helper()
	q := app.DB.Model(&models.MailOutbox{})
	if to != "" {
		q = q.Where("to_address = ?", to)
	}
	var n int64
	if err := q.Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func pushJobsFor(t *testing.T, app *App, notificationID uint) []models.Job {
	t.Helper()
	prefix := "push:n:" + strconv.FormatUint(uint64(notificationID), 10) + ":d:"
	var rows []models.Job
	if err := app.DB.Where("kind = ? AND unique_key LIKE ?", kindPushDeliver, prefix+"%").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

func assertOnePushEach(t *testing.T, app *App, notificationID uint, devices ...uint) {
	t.Helper()
	rows := pushJobsFor(t, app, notificationID)
	if len(rows) != len(devices) {
		t.Fatalf("push jobs %d, want %d", len(rows), len(devices))
	}
	got := map[string]bool{}
	prefix := "push:n:" + strconv.FormatUint(uint64(notificationID), 10) + ":d:"
	for _, row := range rows {
		if row.UniqueKey == nil || !strings.HasPrefix(*row.UniqueKey, prefix) {
			t.Fatalf("key %v", row.UniqueKey)
		}
		got[*row.UniqueKey] = true
	}
	for _, id := range devices {
		key := prefix + strconv.FormatUint(uint64(id), 10)
		if !got[key] {
			t.Fatalf("missing %s in %v", key, got)
		}
	}
}

func assertSentOnce(t *testing.T, app *App, id uint, to, subject string, devices ...uint) {
	t.Helper()
	if n := mailCount(t, app, to); n != 1 {
		t.Fatalf("outbox mails to %s: %d", to, n)
	}
	got := latestEmail(t, app, to)
	if got.Subject != subject {
		t.Fatalf("subject %q", got.Subject)
	}
	d := emailDeliveryOf(t, app, id)
	if d.Status != models.DeliverySent || d.Reason != "" || d.SentAt == nil || d.ProviderMessageID == nil || !strings.HasPrefix(*d.ProviderMessageID, "devoutbox-") {
		t.Fatalf("delivery %+v", d)
	}
	assertOnePushEach(t, app, id, devices...)
}

func TestNotifyDeliverSendsOnce(t *testing.T) {
	const subject = "Youth was approved"
	t.Run("twice", func(t *testing.T) {
		app := testApp(t)
		u := newTestUser(t, app, "pat", "password123456", models.RoleMember)
		phone := newPushDevice(t, app, u, "phone")
		laptop := newPushDevice(t, app, u, "laptop")
		id, _ := seedNotifyDeliver(t, app, u, notifyDeliverAttempts)
		for range 2 {
			if err := app.deliverOneNotification(context.Background(), id); err != nil {
				t.Fatal(err)
			}
		}
		assertSentOnce(t, app, id, u.Email, subject, phone.ID, laptop.ID)
	})
	t.Run("parallel", func(t *testing.T) {
		app := testApp(t)
		u := newTestUser(t, app, "pat", "password123456", models.RoleMember)
		phone := newPushDevice(t, app, u, "phone")
		laptop := newPushDevice(t, app, u, "laptop")
		id, _ := seedNotifyDeliver(t, app, u, notifyDeliverAttempts)
		errCh := make(chan error, 2)
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				err := app.deliverOneNotification(context.Background(), id)
				if err != nil && !strings.Contains(err.Error(), "not due until") {
					errCh <- err
					return
				}
				errCh <- nil
			}()
		}
		wg.Wait()
		close(errCh)
		for err := range errCh {
			if err != nil {
				t.Fatal(err)
			}
		}
		assertSentOnce(t, app, id, u.Email, subject, phone.ID, laptop.ID)
	})
}

func TestNotifyDeliverWaitsOnLockedRow(t *testing.T) {
	app := testApp(t)
	u := newTestUser(t, app, "pat", "password123456", models.RoleMember)
	id, job := seedNotifyDeliver(t, app, u, 1)
	const title = "Updated notice title"

	tx := app.DB.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	committed := false
	t.Cleanup(func() {
		if !committed {
			_ = tx.Rollback()
		}
	})
	var held uint
	if err := tx.Raw("SELECT id FROM notification_deliveries WHERE notification_id = ? AND channel = ? FOR UPDATE",
		id, models.DeliveryEmail).Scan(&held).Error; err != nil {
		t.Fatal(err)
	}
	if held == 0 {
		t.Fatal("no email delivery to lock")
	}
	if err := tx.Model(&models.Notification{}).Where("id = ?", id).Update("email_title", title).Error; err != nil {
		t.Fatal(err)
	}

	runNotifyOnce(t, app)

	var seen models.Notification
	if err := app.DB.Select("email_title").First(&seen, id).Error; err != nil {
		t.Fatal(err)
	}
	if seen.EmailTitle == title {
		t.Fatal("title change was visible before commit")
	}
	job = reloadJob(t, app, job.ID)
	if job.Status != models.JobQueued || job.Attempts != 0 || job.LastError != "" || !job.RunAt.After(time.Now()) {
		t.Fatalf("waiting job: %+v", job)
	}
	if n := mailCount(t, app, ""); n != 0 {
		t.Fatalf("mail while locked: %d", n)
	}

	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	committed = true
	if err := app.DB.Exec("UPDATE jobs SET run_at = now() WHERE id = ?", job.ID).Error; err != nil {
		t.Fatal(err)
	}
	runNotifyOnce(t, app)

	got := latestEmail(t, app, u.Email)
	if got.Subject != title {
		t.Fatalf("subject %q", got.Subject)
	}
	job = reloadJob(t, app, job.ID)
	if job.Status != models.JobDone || job.LastError != "" {
		t.Fatalf("job after send: %+v", job)
	}
	d := emailDeliveryOf(t, app, id)
	if d.Status != models.DeliverySent || d.ProviderMessageID == nil || !strings.HasPrefix(*d.ProviderMessageID, "devoutbox-") {
		t.Fatalf("delivery %+v", d)
	}
}

func TestNotifyDeliverFailureModes(t *testing.T) {
	dirty := errors.New("451 mailbox busy for user@example.com token eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxIn0.signaturepart")
	_, noSenderErr := (mail.NoSender{}).SendEmail(context.Background(), mail.Message{})
	if noSenderErr == nil || !mail.NotConfigured(noSenderErr) {
		t.Fatal(noSenderErr)
	}
	permanent := &providers.SendError{Provider: "fake", Code: 550, Message: "no such user", Permanent: true}

	cases := []struct {
		name     string
		attempts int
		sendErr  error
		prepare  func(*testing.T, *App, *models.User)
		status   string
		reason   string
		job      string
		later    bool
		calls    int
		clean    bool
	}{
		{name: "transient", attempts: notifyDeliverAttempts, sendErr: dirty, status: models.DeliveryQueued, job: models.JobFailed, later: true, calls: 1, clean: true},
		{name: "permanent", attempts: notifyDeliverAttempts, sendErr: permanent, status: models.DeliveryFailed, reason: jobs.CleanError(permanent), job: models.JobDone, calls: 1},
		{name: "not configured", attempts: notifyDeliverAttempts, sendErr: noSenderErr, status: models.DeliveryFailed, reason: jobs.CleanError(noSenderErr), job: models.JobDone, calls: 1},
		{name: "suppressed address", attempts: notifyDeliverAttempts, sendErr: dirty, prepare: suppressDeliverAddress, status: models.DeliverySuppressed, reason: "email to this address bounces", job: models.JobDone, calls: 0},
		{name: "opt-out", attempts: notifyDeliverAttempts, sendErr: dirty, prepare: optOutDeliverEmail, status: models.DeliverySuppressed, reason: "optional email turned off", job: models.JobDone, calls: 0},
		{name: "last attempt", attempts: 1, sendErr: dirty, status: models.DeliveryDeferred, job: models.JobDead, calls: 1, clean: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := testApp(t)
			u := newTestUser(t, app, "pat", "password123456", models.RoleMember)
			sender := &notifyFailSender{err: tc.sendErr}
			app.Mailer = sender
			id, seeded := seedNotifyDeliver(t, app, u, tc.attempts)
			if tc.prepare != nil {
				tc.prepare(t, app, u)
			}
			runNotifyOnce(t, app)
			if sender.n() != tc.calls {
				t.Fatalf("sends %d, want %d", sender.n(), tc.calls)
			}
			if n := mailCount(t, app, ""); n != 0 {
				t.Fatalf("outbox %d", n)
			}
			d := emailDeliveryOf(t, app, id)
			if d.Status != tc.status || d.Reason == "" || (tc.reason != "" && d.Reason != tc.reason) {
				t.Fatalf("delivery status %q reason %q", d.Status, d.Reason)
			}
			job := reloadJob(t, app, seeded.ID)
			if job.Status != tc.job {
				t.Fatalf("job %+v", job)
			}
			if tc.later && !job.RunAt.After(time.Now()) {
				t.Fatalf("run_at %s", job.RunAt)
			}
			if tc.job == models.JobDone && job.LastError != "" {
				t.Fatalf("last error %q", job.LastError)
			}
			if tc.job != models.JobDone && job.LastError == "" {
				t.Fatal("missing last error")
			}
			if strings.Contains(d.Reason, "@") || strings.Contains(d.Reason, "eyJ") || strings.Contains(job.LastError, "@") || strings.Contains(job.LastError, "eyJ") {
				t.Fatalf("leaked contact or token: reason %q last %q", d.Reason, job.LastError)
			}
			if tc.clean && (!strings.Contains(d.Reason, "[email]") || !strings.Contains(d.Reason, "[token]")) {
				t.Fatalf("reason not cleaned: %q", d.Reason)
			}
		})
	}
}

func suppressDeliverAddress(t *testing.T, app *App, u *models.User) {
	t.Helper()
	if err := app.DB.Create(&models.Suppression{
		Channel: "email", Address: strings.ToLower(strings.TrimSpace(u.Email)), Reason: models.SuppressHardBounce,
	}).Error; err != nil {
		t.Fatal(err)
	}
}

func optOutDeliverEmail(t *testing.T, app *App, u *models.User) {
	t.Helper()
	if err := app.DB.Create(&models.Consent{
		UserID: u.ID, Channel: models.ConsentEmail, Scope: categoryScope(notify.CatGroups),
		Status: models.ConsentOptedOut, Source: models.ConsentSourceWebForm,
	}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestNotifyDeliverPushFanOutSurvivesEmailFailure(t *testing.T) {
	app := testApp(t)
	u := newTestUser(t, app, "pat", "password123456", models.RoleMember)
	phone := newPushDevice(t, app, u, "phone")
	laptop := newPushDevice(t, app, u, "laptop")
	sender := &notifyFailSender{err: &providers.SendError{Provider: "fake", Code: 550, Message: "no such user", Permanent: true}}
	app.Mailer = sender
	id, _ := seedNotifyDeliver(t, app, u, notifyDeliverAttempts)
	runNotifyOnce(t, app)
	assertOnePushEach(t, app, id, phone.ID, laptop.ID)
	d := emailDeliveryOf(t, app, id)
	if d.Status != models.DeliveryFailed {
		t.Fatalf("delivery %+v", d)
	}
	if err := app.deliverOneNotification(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	assertOnePushEach(t, app, id, phone.ID, laptop.ID)
	if sender.n() != 1 {
		t.Fatalf("sends %d", sender.n())
	}
	var all int64
	if err := app.DB.Model(&models.Job{}).Where("kind = ?", kindPushDeliver).Count(&all).Error; err != nil {
		t.Fatal(err)
	}
	if all != 2 {
		t.Fatalf("push jobs %d", all)
	}
}

func TestNotifyDeliverLedgerCases(t *testing.T) {
	busy := errors.New("451 mailbox busy")
	_, noSenderErr := (mail.NoSender{}).SendEmail(context.Background(), mail.Message{})
	if noSenderErr == nil || !mail.NotConfigured(noSenderErr) {
		t.Fatal(noSenderErr)
	}

	cases := []struct {
		name         string
		attempts     int
		seedStatus   string
		kind         string
		createdAgo   time.Duration
		mailer       string
		sendErr      error
		cancel       bool
		backdate     bool
		presetID     string
		wantStatus   string
		wantReason   string
		wantJob      string
		wantAttempts int
		checkAttempt bool
		wantHour     bool
		wantNullID   bool
		wantID       bool
		wantUpdated  bool
		unchanged    bool
		wantNoMail   bool
	}{
		{
			name: "deferred row sends on rerun", attempts: notifyDeliverAttempts,
			seedStatus: models.DeliveryDeferred, mailer: "default",
			wantStatus: models.DeliverySent, wantJob: models.JobDone, wantID: true,
		},
		{
			name: "transient failure returns deferred to queued", attempts: notifyDeliverAttempts,
			seedStatus: models.DeliveryDeferred, mailer: "fail", sendErr: busy,
			wantStatus: models.DeliveryQueued, wantReason: jobs.CleanError(busy),
			wantJob: models.JobFailed, wantNoMail: true,
		},
		{
			name: "nil mailer", attempts: notifyDeliverAttempts, mailer: "nil",
			wantStatus: models.DeliveryFailed, wantReason: jobs.CleanError(noSenderErr),
			wantJob: models.JobDone, wantNoMail: true,
		},
		{
			name: "updated_at moves past created_at", attempts: notifyDeliverAttempts,
			mailer: "default", backdate: true,
			wantStatus: models.DeliverySent, wantJob: models.JobDone, wantUpdated: true, wantID: true,
		},
		{
			name: "non-tracking sender stores a null provider id", attempts: notifyDeliverAttempts,
			mailer: "plain", presetID: "old-id",
			wantStatus: models.DeliverySent, wantJob: models.JobDone, wantNullID: true, wantNoMail: true,
		},
		{
			name: "unknown kind waits", attempts: notifyDeliverAttempts,
			kind:       "kind.not.in.this.build",
			wantStatus: models.DeliveryQueued, wantJob: models.JobQueued,
			wantAttempts: 0, checkAttempt: true, wantHour: true, unchanged: true, wantNoMail: true,
		},
		{
			name: "unknown kind past a week is suppressed", attempts: notifyDeliverAttempts,
			kind: "kind.not.in.this.build", createdAgo: notifyUnknownGrace + time.Hour,
			wantStatus: models.DeliverySuppressed, wantReason: emailKindUnknown,
			wantJob: models.JobDone, wantNoMail: true,
		},
		{
			name: "cancelled context on the last attempt", attempts: 1,
			mailer: "fail", sendErr: busy, cancel: true,
			wantStatus: models.DeliveryQueued, wantReason: jobs.CleanError(busy),
			wantJob: models.JobQueued, wantAttempts: 0, checkAttempt: true, wantNoMail: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := testApp(t)
			u := newTestUser(t, app, "pat", "password123456", models.RoleMember)
			ctx := context.Background()
			var cancel context.CancelFunc
			var sender *notifyFailSender
			switch tc.mailer {
			case "nil":
				app.Mailer = nil
			case "plain":
				app.Mailer = notifyPlainSender{}
			case "fail":
				ctx, cancel = context.WithCancel(context.Background())
				sender = &notifyFailSender{err: tc.sendErr}
				if tc.cancel {
					sender.onSend = cancel
				}
				app.Mailer = sender
				t.Cleanup(cancel)
			}
			id, seeded := seedNotifyDeliver(t, app, u, tc.attempts)
			if tc.kind != "" {
				if err := app.DB.Model(&models.Notification{}).Where("id = ?", id).UpdateColumn("kind", tc.kind).Error; err != nil {
					t.Fatal(err)
				}
			}
			if tc.createdAgo > 0 {
				when := time.Now().Add(-tc.createdAgo)
				if err := app.DB.Model(&models.Notification{}).Where("id = ?", id).UpdateColumn("created_at", when).Error; err != nil {
					t.Fatal(err)
				}
			}
			if tc.seedStatus != "" {
				if err := app.DB.Model(&models.NotificationDelivery{}).Where("notification_id = ? AND channel = ?", id, models.DeliveryEmail).UpdateColumn("status", tc.seedStatus).Error; err != nil {
					t.Fatal(err)
				}
			}
			if tc.presetID != "" {
				if err := app.DB.Model(&models.NotificationDelivery{}).Where("notification_id = ? AND channel = ?", id, models.DeliveryEmail).UpdateColumn("provider_message_id", tc.presetID).Error; err != nil {
					t.Fatal(err)
				}
			}
			if tc.backdate {
				past := time.Now().Add(-time.Hour).UTC()
				if err := app.DB.Model(&models.NotificationDelivery{}).Where("notification_id = ? AND channel = ?", id, models.DeliveryEmail).UpdateColumns(map[string]any{
					"created_at": past, "updated_at": past,
				}).Error; err != nil {
					t.Fatal(err)
				}
			}
			before := emailDeliveryOf(t, app, id)
			ran, err := jobs.NewRunner(app.DB, app.notifyKinds()...).RunOnce(ctx, "test")
			if err != nil || !ran {
				t.Fatalf("run: ran %v err %v", ran, err)
			}
			if sender != nil && sender.n() != 1 {
				t.Fatalf("sends %d", sender.n())
			}
			if tc.wantNoMail {
				if n := mailCount(t, app, ""); n != 0 {
					t.Fatalf("outbox %d", n)
				}
			}
			d := emailDeliveryOf(t, app, id)
			if d.Status != tc.wantStatus || (tc.wantReason != "" && d.Reason != tc.wantReason) {
				t.Fatalf("delivery status %q reason %q", d.Status, d.Reason)
			}
			job := reloadJob(t, app, seeded.ID)
			if job.Status != tc.wantJob {
				t.Fatalf("job %+v", job)
			}
			if tc.checkAttempt && job.Attempts != tc.wantAttempts {
				t.Fatalf("attempts %d, want %d", job.Attempts, tc.wantAttempts)
			}
			if tc.wantHour {
				until := time.Until(job.RunAt)
				if until < notifyUnknownWait-time.Minute || until > notifyUnknownWait+time.Minute {
					t.Fatalf("run_at %s (%s from now)", job.RunAt, until)
				}
			}
			if tc.wantNullID && d.ProviderMessageID != nil {
				t.Fatalf("provider id %q", *d.ProviderMessageID)
			}
			if tc.wantID && (d.ProviderMessageID == nil || !strings.HasPrefix(*d.ProviderMessageID, "devoutbox-")) {
				t.Fatalf("provider id %v", d.ProviderMessageID)
			}
			if tc.wantUpdated && !d.UpdatedAt.After(d.CreatedAt) {
				t.Fatalf("updated_at %s created_at %s", d.UpdatedAt, d.CreatedAt)
			}
			if tc.unchanged && (!d.UpdatedAt.Equal(before.UpdatedAt) || d.Status != before.Status || d.Reason != before.Reason) {
				t.Fatalf("row changed: before %+v after %+v", before, d)
			}
			if tc.wantJob == models.JobDone && job.LastError != "" {
				t.Fatalf("last error %q", job.LastError)
			}
			if tc.wantJob == models.JobQueued && job.LastError != "" {
				t.Fatalf("last error %q", job.LastError)
			}
			if tc.wantJob == models.JobFailed && job.LastError == "" {
				t.Fatal("missing last error")
			}
		})
	}
}
