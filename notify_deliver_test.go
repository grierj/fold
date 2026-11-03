package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
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

	"gorm.io/gorm"
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
	key := notify.DeliverKey(id)
	if err := app.DB.Model(&models.Job{}).Where("unique_key = ?", key).Update("max_attempts", attempts).Error; err != nil {
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
	// The whole chain: every attempt fails transiently, the deliver job
	// dies with the email left deferred, each admin gets one alert, and an
	// admin's retry with a healed mailer sends it and closes the alerts.
	t.Run("dead job alerts the admins and a retry heals it", func(t *testing.T) {
		app := testApp(t)
		a1 := newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
		a2 := newTestUser(t, app, "admin2", "password1234", models.RoleAdmin)
		u := newTestUser(t, app, "pat", "password123456", models.RoleMember)
		admin := loginAPI(t, app, "admin", "password1234")
		admin.holdNotices = true
		sender := &notifyFailSender{err: dirty}
		app.Mailer = sender
		id, seeded := seedNotifyDeliver(t, app, u, 1)
		run := func() {
			ran, err := app.newRunner(app.notifyKinds()...).RunOnce(context.Background(), "test")
			if err != nil || !ran {
				t.Fatalf("run: ran %v err %v", ran, err)
			}
		}
		run()
		if d := emailDeliveryOf(t, app, id); d.Status != models.DeliveryDeferred {
			t.Fatalf("delivery %+v", d)
		}
		if job := reloadJob(t, app, seeded.ID); job.Status != models.JobDead {
			t.Fatalf("job %+v", job)
		}
		alerts := deadAlerts(t, app, seeded.ID)
		if len(alerts) != 2 || alerts[0].UserID != a1.ID || alerts[1].UserID != a2.ID {
			t.Fatalf("alerts %+v", alerts)
		}
		for _, n := range alerts {
			if n.ResolvedAt != nil || !strings.HasPrefix(n.Title, "A background task stopped: ") ||
				strings.Contains(n.Body+n.Title, "@") || strings.Contains(n.Body+n.Title, "eyJ") {
				t.Fatalf("alert %+v", n)
			}
		}
		sender.mu.Lock()
		sender.err = nil // the provider recovers
		sender.mu.Unlock()
		if w := admin.do("POST", "/api/v1/admin/jobs/"+strconv.Itoa(int(seeded.ID))+"/retry", nil, true); w.Code != http.StatusOK {
			t.Fatalf("retry: %d %s", w.Code, w.Body.String())
		}
		run()
		if d := emailDeliveryOf(t, app, id); d.Status != models.DeliverySent {
			t.Fatalf("delivery after retry %+v", d)
		}
		if job := reloadJob(t, app, seeded.ID); job.Status != models.JobDone {
			t.Fatalf("job after retry %+v", job)
		}
		if sender.n() != 2 {
			t.Fatalf("sends %d", sender.n())
		}
		for _, n := range deadAlerts(t, app, seeded.ID) {
			if n.ResolvedAt == nil || n.Resolution != "retried" {
				t.Fatalf("alert not resolved: %+v", n)
			}
		}
	})
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

func TestNotifyDeliverRunThenMergeKeepsSent(t *testing.T) {
	app := testApp(t)
	u := newTestUser(t, app, "recipient", "password1234", models.RoleMember)
	id, job := seedNotifyDeliver(t, app, u, 8)
	runNotifyOnce(t, app)
	before := emailDeliveryOf(t, app, id)
	if before.Status != models.DeliverySent || mailCount(t, app, u.Email) != 1 {
		t.Fatalf("first send: %+v", before)
	}
	// Production merge locks the notification, then the delivery row, before
	// replacing its text. A send that finished first must stay terminal.
	if err := app.DB.Transaction(func(tx *gorm.DB) error {
		var n models.Notification
		if err := tx.Raw("SELECT * FROM notifications WHERE id = ? FOR UPDATE", id).Scan(&n).Error; err != nil {
			return err
		}
		var held uint
		if err := tx.Raw("SELECT id FROM notification_deliveries WHERE notification_id = ? AND channel = 'email' FOR UPDATE", id).Scan(&held).Error; err != nil {
			return err
		}
		return tx.Model(&n).Update("email_title", "Merged after sending").Error
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.deliverOneNotification(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	after := emailDeliveryOf(t, app, id)
	if after.Status != models.DeliverySent || after.ID != before.ID || after.SentAt == nil || !after.SentAt.Equal(*before.SentAt) || mailCount(t, app, u.Email) != 1 {
		t.Fatalf("after merge: %+v", after)
	}
	var count int64
	if err := app.DB.Model(&models.Job{}).Where("kind = ?", notify.DeliverKind).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 || reloadJob(t, app, job.ID).Status != models.JobDone {
		t.Fatalf("jobs after merge: %d", count)
	}
}

func TestDrainNoticesLeavesOtherKindsAndHonorsLater(t *testing.T) {
	app := testApp(t)
	u := newTestUser(t, app, "recipient", "password1234", models.RoleMember)
	id, job := seedNotifyDeliver(t, app, u, 8)
	for _, kind := range []string{kindPushDeliver, kindMessageSend} {
		if _, err := jobs.Enqueue(app.DB, kind, nil, time.Now(), kind+":unrelated", 8); err != nil {
			t.Fatal(err)
		}
	}
	tx := app.DB.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	var held uint
	if err := tx.Raw("SELECT id FROM notification_deliveries WHERE notification_id = ? AND channel = 'email' FOR UPDATE", id).Scan(&held).Error; err != nil {
		t.Fatal(err)
	}
	drainNotices(t, app)
	got := reloadJob(t, app, job.ID)
	if got.Status != models.JobQueued || got.Attempts != 0 || got.LastError != "" || !got.RunAt.After(time.Now()) {
		t.Fatalf("Later consumed an attempt or failed: %+v", got)
	}
	var unrelated []models.Job
	if err := app.DB.Where("kind <> ?", notify.DeliverKind).Find(&unrelated).Error; err != nil {
		t.Fatal(err)
	}
	if len(unrelated) != 2 {
		t.Fatalf("unrelated jobs: %+v", unrelated)
	}
	for _, j := range unrelated {
		if j.Status != models.JobQueued || j.Attempts != 0 || j.LockedAt != nil || j.LastError != "" || j.RunAt.After(time.Now()) {
			t.Fatalf("claimed or postponed another kind: %+v", j)
		}
	}
	if n := mailCount(t, app, ""); n != 0 {
		t.Fatalf("sent while held: %d", n)
	}
}

// preOutboxNotice stores only the bell row, as the pre-outbox writer did.
func preOutboxNotice(t *testing.T, app *App, u *models.User, kind string, at time.Time, subjectID *uint) models.Notification {
	t.Helper()
	n := models.Notification{UserID: u.ID, Kind: kind, Category: notify.CatAdmin, Link: "/",
		Title: "Old notice", CreatedAt: at, SubjectID: subjectID}
	if k, ok := notify.Lookup(kind); ok {
		v := notify.Vars{Group: "Youth"}
		n.Category = k.Category
		n.Title, n.Body = k.Render(v)
		n.EmailTitle, n.EmailBody = k.RenderForEmail(v)
		n.PushTitle, n.PushBody, _ = k.RenderForPush(v)
	}
	if kind == notify.AccountRequested {
		n.SubjectType = "account_request"
	}
	if err := app.DB.Create(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func TestNotifyReconcileRequeuesOrphans(t *testing.T) {
	app := testApp(t)
	u := newTestUser(t, app, "recipient", "password1234", models.RoleAdmin)
	phone := newPushDevice(t, app, u, "phone")
	laptop := newPushDevice(t, app, u, "laptop")
	now := time.Now().UTC()
	recent := now.Add(-time.Hour)
	old := now.Add(-25 * time.Hour)
	orphan := preOutboxNotice(t, app, u, notify.GroupApproved, old, nil)
	dead := preOutboxNotice(t, app, u, notify.GroupApproved, old, nil)
	for _, row := range []models.NotificationDelivery{
		{NotificationID: orphan.ID, UserID: u.ID, Channel: models.DeliveryEmail, Status: models.DeliveryQueued, CreatedAt: now.Add(-11 * time.Minute)},
		{NotificationID: dead.ID, UserID: u.ID, Channel: models.DeliveryEmail, Status: models.DeliveryDeferred, CreatedAt: old},
	} {
		if err := app.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	deadKey := notify.DeliverKey(dead.ID)
	if err := app.DB.Create(&models.Job{Kind: notify.DeliverKind, UniqueKey: &deadKey, Status: models.JobDead}).Error; err != nil {
		t.Fatal(err)
	}
	backfill := preOutboxNotice(t, app, u, notify.GroupApproved, recent, nil)
	stale := preOutboxNotice(t, app, u, notify.GroupApproved, old, nil)
	pushed := preOutboxNotice(t, app, u, notify.GroupApproved, recent, nil)
	if err := app.DB.Create(&models.NotificationDelivery{NotificationID: pushed.ID, UserID: u.ID,
		Channel: models.DeliveryPush, Status: models.DeliverySent}).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Transaction(func(tx *gorm.DB) error { return queuePushFor(tx, &pushed) }); err != nil {
		t.Fatal(err)
	}
	priorPush := pushJobsFor(t, app, pushed.ID)
	for _, job := range priorPush {
		if err := app.DB.Model(&job).Update("status", models.JobDone).Error; err != nil {
			t.Fatal(err)
		}
	}
	bellIDs, err := notify.Send(app.DB, notify.Msg{Kind: notify.GroupApproved, To: []uint{u.ID}, Link: "/", BellOnly: true})
	if err != nil || len(bellIDs) != 1 {
		t.Fatalf("bell-only notice: %v %v", bellIDs, err)
	}
	bellKind := preOutboxNotice(t, app, u, notify.PushDevicesOff, recent, nil)
	unknown := preOutboxNotice(t, app, u, "future.notice", recent, nil)
	noEmail := []uint{stale.ID, bellKind.ID, unknown.ID}
	// Even a terminal delivery key without an email row prevents backfill.
	for _, status := range []string{models.JobDone, models.JobDead, models.JobDismissed} {
		n := preOutboxNotice(t, app, u, notify.GroupApproved, recent, nil)
		key := notify.DeliverKey(n.ID)
		if err := app.DB.Create(&models.Job{Kind: notify.DeliverKind, UniqueKey: &key, Status: status}).Error; err != nil {
			t.Fatal(err)
		}
		noEmail = append(noEmail, n.ID)
	}
	queued := []uint{orphan.ID, backfill.ID, pushed.ID}
	suppressed := []uint{bellIDs[0]}
	for _, flags := range []models.RequestFlags{{Duplicate: true}, {Honeypot: true}, {}, {RateLimited: true}} {
		body, err := json.Marshal(flags)
		if err != nil {
			t.Fatal(err)
		}
		r := models.AccountRequest{Name: "New person", Email: "new@example.com", Source: "self_signup",
			Flags: string(body), ExpiresAt: now.Add(24 * time.Hour)}
		if err := app.DB.Create(&r).Error; err != nil {
			t.Fatal(err)
		}
		n := preOutboxNotice(t, app, u, notify.AccountRequested, recent, &r.ID)
		if flags.Duplicate || flags.Honeypot {
			suppressed = append(suppressed, n.ID)
		} else {
			queued = append(queued, n.ID)
		}
	}
	counts := func() [3]int64 {
		t.Helper()
		var out [3]int64
		for i, model := range []any{&models.Job{}, &models.NotificationDelivery{}, &models.MailOutbox{}} {
			if err := app.DB.Model(model).Count(&out[i]).Error; err != nil {
				t.Fatal(err)
			}
		}
		return out
	}
	before := counts()
	if err := app.reconcileNotifications(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	after := counts()
	if after[0]-before[0] != int64(len(queued)) {
		t.Fatalf("added jobs = %d, want %d", after[0]-before[0], len(queued))
	}
	for _, id := range queued {
		if d := emailDeliveryOf(t, app, id); d.Status != models.DeliveryQueued {
			t.Fatalf("expected queued email: %+v", d)
		}
		var job models.Job
		if err := app.DB.Where("unique_key = ?", notify.DeliverKey(id)).First(&job).Error; err != nil {
			t.Fatal(err)
		}
		if job.Status != models.JobQueued || job.MaxAttempts != notifyDeliverAttempts {
			t.Fatalf("reconciled job: %+v", job)
		}
	}
	for _, id := range suppressed {
		d := emailDeliveryOf(t, app, id)
		if d.Status != models.DeliverySuppressed || d.Reason != "bell only" {
			t.Fatalf("expected bell-only marker: %+v", d)
		}
		var n int64
		if err := app.DB.Model(&models.Job{}).Where("unique_key = ?", notify.DeliverKey(id)).Count(&n).Error; err != nil || n != 0 {
			t.Fatalf("bell-only job: %d %v", n, err)
		}
	}
	var n int64
	if err := app.DB.Model(&models.NotificationDelivery{}).Where("notification_id IN ? AND channel = ?", noEmail, models.DeliveryEmail).
		Count(&n).Error; err != nil || n != 0 {
		t.Fatalf("backfilled an excluded notice: %d %v", n, err)
	}
	if d := emailDeliveryOf(t, app, dead.ID); d.Status != models.DeliveryDeferred {
		t.Fatalf("dead job's delivery changed: %+v", d)
	}
	if err := app.reconcileNotifications(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if got := counts(); got != after {
		t.Fatalf("second reconcile wrote rows: %v -> %v", after, got)
	}
	drainNotices(t, app)
	for _, id := range queued {
		if d := emailDeliveryOf(t, app, id); d.Status != models.DeliverySent {
			t.Fatalf("recovered notice not sent: %+v", d)
		}
	}
	if got := mailCount(t, app, u.Email); got != int64(len(queued)) {
		t.Fatalf("emails = %d, want %d", got, len(queued))
	}
	assertOnePushEach(t, app, pushed.ID, phone.ID, laptop.ID)
	for _, job := range priorPush {
		if got := reloadJob(t, app, job.ID); got.Status != models.JobDone || got.Attempts != 0 {
			t.Fatalf("requeued a completed push: %+v", got)
		}
	}
	after = counts()
	if err := app.reconcileNotifications(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	drainNotices(t, app)
	if got := counts(); got != after {
		t.Fatalf("reconcile after delivery wrote rows: %v -> %v", after, got)
	}
}

func TestNotifyReconcileExcludesTerminalKeys(t *testing.T) {
	app := testApp(t)
	u := newTestUser(t, app, "recipient", "password1234", models.RoleMember)
	now := time.Now().UTC()
	// Identical created_at values force the id tie-breaker to advance.
	// All 501 keys exist in terminal states, so pass (a) must exclude
	// them without reviving done, dead or dismissed work.
	if err := app.DB.Exec(`WITH notices AS (
		INSERT INTO notifications (user_id, kind, category, title, link, created_at)
		SELECT ?, ?, ?, 'Old notice', '/', ? FROM generate_series(1, 501)
		RETURNING id, user_id, created_at
	), deliveries AS (
		INSERT INTO notification_deliveries (notification_id, user_id, channel, status, created_at)
		SELECT id, user_id, 'email', 'queued', created_at FROM notices
	)
	INSERT INTO jobs (kind, unique_key, status)
	SELECT ?, ? || id, CASE id % 3 WHEN 0 THEN 'done' WHEN 1 THEN 'dead' ELSE 'dismissed' END FROM notices`,
		u.ID, notify.GroupApproved, notify.CatGroups, now.Add(-25*time.Hour), notify.DeliverKind, notify.DeliverKind+":").Error; err != nil {
		t.Fatal(err)
	}
	orphan := preOutboxNotice(t, app, u, notify.GroupApproved, now.Add(-24*time.Hour), nil)
	if err := app.DB.Create(&models.NotificationDelivery{NotificationID: orphan.ID, UserID: u.ID, Channel: models.DeliveryEmail,
		Status: models.DeliveryQueued, CreatedAt: now.Add(-24 * time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	qc := countDBReads(t, app.DB)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := app.reconcileNotifications(ctx, now)
	qc.Stop()
	if err != nil {
		t.Fatal(err)
	}
	// One orphan page and one empty backfill page: terminal keys aren't
	// selected as candidates, even while their email rows remain queued.
	if reads := qc.Reads(); reads != 2 {
		t.Fatalf("reconcile page reads = %d, want 2", reads)
	}
	var rows []models.Job
	if err := app.DB.Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 502 {
		t.Fatalf("jobs = %d, want 501 collisions and one recovered job", len(rows))
	}
	for _, job := range rows {
		if job.UniqueKey != nil && *job.UniqueKey == notify.DeliverKey(orphan.ID) {
			if job.Status != models.JobQueued {
				t.Fatalf("orphan job: %+v", job)
			}
			continue
		}
		if job.Status != models.JobDone && job.Status != models.JobDead && job.Status != models.JobDismissed {
			t.Fatalf("revived a collided key: %+v", job)
		}
	}
}

func TestNotifyReconcileAccountRequestsFailClosed(t *testing.T) {
	app := testApp(t)
	u := newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	now := time.Now().UTC()
	for _, tc := range []struct {
		name       string
		flags      string
		wantQueued bool
	}{
		{name: "nil subject"},
		{name: "missing request"},
		{name: "invalid flags", flags: `{"duplicate":"invalid"}`},
		{name: "clean flags", flags: `{}`, wantQueued: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var subjectID *uint
			if tc.name == "missing request" {
				missing := ^uint(0) >> 1
				subjectID = &missing
			} else if tc.flags != "" {
				r := models.AccountRequest{Name: "New person", Email: "new@example.com", Source: "self_signup",
					Flags: tc.flags, ExpiresAt: now.Add(24 * time.Hour)}
				if err := app.DB.Create(&r).Error; err != nil {
					t.Fatal(err)
				}
				subjectID = &r.ID
			}
			n := preOutboxNotice(t, app, u, notify.AccountRequested, now.Add(-time.Hour), subjectID)
			if err := app.reconcileNotifications(context.Background(), now); err != nil {
				t.Fatal(err)
			}
			d := emailDeliveryOf(t, app, n.ID)
			wantStatus, wantReason := models.DeliverySuppressed, "bell only"
			var wantJobs int64
			if tc.wantQueued {
				wantStatus, wantReason, wantJobs = models.DeliveryQueued, "", 1
			}
			if d.Status != wantStatus || d.Reason != wantReason {
				t.Fatalf("email ledger: %+v, want %s %q", d, wantStatus, wantReason)
			}
			var count int64
			if err := app.DB.Model(&models.Job{}).Where("unique_key = ?", notify.DeliverKey(n.ID)).Count(&count).Error; err != nil || count != wantJobs {
				t.Fatalf("delivery jobs = %d, want %d: %v", count, wantJobs, err)
			}
		})
	}
}

func TestNotifyDeliverSurvivesLostJob(t *testing.T) {
	app := testApp(t)
	u := newTestUser(t, app, "recipient", "password1234", models.RoleMember)
	phone := newPushDevice(t, app, u, "phone")
	laptop := newPushDevice(t, app, u, "laptop")
	var id uint
	if err := app.DB.Transaction(func(tx *gorm.DB) error {
		ids, err := notify.Send(tx, notify.Msg{Kind: notify.GroupApproved, To: []uint{u.ID}, Link: "/", Vars: notify.Vars{Group: "Youth"}})
		if err != nil {
			return err
		}
		if len(ids) != 1 {
			t.Fatalf("created notice ids: %v", ids)
		}
		id = ids[0]
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Where("unique_key = ?", notify.DeliverKey(id)).Delete(&models.Job{}).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := app.DB.Model(&models.NotificationDelivery{}).Where("notification_id = ? AND channel = ?", id, models.DeliveryEmail).
		Update("created_at", now.Add(-11*time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	if got := mailCount(t, app, ""); got != 0 || len(pushJobsFor(t, app, id)) != 0 {
		t.Fatal("delivery ran before reconcile")
	}
	if err := app.reconcileNotifications(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	drainNotices(t, app)
	assertSentOnce(t, app, id, u.Email, "Youth was approved", phone.ID, laptop.ID)
}

func TestNotifyReconcileBackfillPaginatesPastSkippedKinds(t *testing.T) {
	app := testApp(t)
	u := newTestUser(t, app, "recipient", "password1234", models.RoleMember)
	now := time.Now().UTC()
	at := now.Add(-time.Hour)
	// These rows never gain email intent or a delivery key, so advancing
	// only after a successful write would keep selecting the first page.
	if err := app.DB.Exec(`INSERT INTO notifications (user_id, kind, category, title, link, created_at)
		SELECT ?, CASE i % 2 WHEN 0 THEN ? ELSE 'future.notice' END, ?, 'Bell notice', '/', ?
		FROM generate_series(1, 501) AS s(i)`, u.ID, notify.PushDevicesOff, notify.CatAdmin, at).Error; err != nil {
		t.Fatal(err)
	}
	backfill := preOutboxNotice(t, app, u, notify.GroupApproved, at, nil)
	for range 2 {
		qc := countDBReads(t, app.DB)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := app.reconcileNotifications(ctx, now)
		cancel()
		qc.Stop()
		if err != nil {
			t.Fatal(err)
		}
		if reads := qc.Reads(); reads != 3 {
			t.Fatalf("page reads = %d, want one orphan and two backfill pages", reads)
		}
	}
	var deliveries []models.NotificationDelivery
	if err := app.DB.Find(&deliveries).Error; err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 1 || deliveries[0].NotificationID != backfill.ID || deliveries[0].Status != models.DeliveryQueued {
		t.Fatalf("backfilled deliveries: %+v", deliveries)
	}
	var jobs []models.Job
	if err := app.DB.Find(&jobs).Error; err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].UniqueKey == nil || *jobs[0].UniqueKey != notify.DeliverKey(backfill.ID) {
		t.Fatalf("backfilled jobs: %+v", jobs)
	}
}

func TestNotifyReconcileLeavesYoungOrLiveDeliveries(t *testing.T) {
	app := testApp(t)
	u := newTestUser(t, app, "recipient", "password1234", models.RoleMember)
	now := time.Now().UTC()
	for _, tc := range []struct {
		age    time.Duration
		status string
	}{
		{age: 9 * time.Minute},
		{age: 10 * time.Minute},
		{age: time.Hour, status: models.JobQueued},
		{age: time.Hour, status: models.JobRunning},
		{age: time.Hour, status: models.JobFailed},
	} {
		n := preOutboxNotice(t, app, u, notify.GroupApproved, now.Add(-25*time.Hour), nil)
		if err := app.DB.Create(&models.NotificationDelivery{NotificationID: n.ID, UserID: u.ID, Channel: models.DeliveryEmail,
			Status: models.DeliveryQueued, CreatedAt: now.Add(-tc.age)}).Error; err != nil {
			t.Fatal(err)
		}
		if tc.status != "" {
			key := notify.DeliverKey(n.ID)
			if err := app.DB.Create(&models.Job{Kind: notify.DeliverKind, UniqueKey: &key, Status: tc.status}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := app.reconcileNotifications(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	var rows []models.Job
	if err := app.DB.Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].Status != models.JobQueued || rows[1].Status != models.JobRunning || rows[2].Status != models.JobFailed {
		t.Fatalf("changed young or live jobs: %+v", rows)
	}
}

func TestNotifyReconcileHourlyRetriesErrors(t *testing.T) {
	app := testApp(t)
	var kind jobs.Kind
	for _, k := range app.hourlyKinds() {
		if k.Name == kindNotifyReconcile {
			kind = k
		}
	}
	if kind.Run == nil || kind.Label != "Checking for notices that still need sending" || kind.MaxAttempts != jobs.DefaultMaxAttempts {
		t.Fatalf("hourly kind: %+v", kind)
	}
	// This local hour belongs to the following UTC day.
	at := time.Date(2026, 11, 1, 23, 10, 0, 0, time.FixedZone("test", -7*60*60))
	for range 2 {
		if err := queueHourly(app.DB, []jobs.Kind{kind}, at); err != nil {
			t.Fatal(err)
		}
	}
	var queued []models.Job
	if err := app.DB.Where("kind = ?", kindNotifyReconcile).Find(&queued).Error; err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 || queued[0].UniqueKey == nil || *queued[0].UniqueKey != "notify.reconcile:2026-11-02T06" {
		t.Fatalf("hourly keys: %+v", queued)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := kind.Run(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("handler lost its error: %v", err)
	}
	// A failed page read reaches the runner and becomes a retry, rather
	// than quietly marking this hour done.
	const name = "test:fail_reconcile_read"
	readErr := errors.New("notification deliveries unavailable")
	if err := app.DB.Callback().Row().Before("gorm:row").Register(name, func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), "SELECT nd.notification_id") {
			_ = tx.AddError(readErr)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.DB.Callback().Row().Remove(name) })
	if err := app.DB.Model(&models.Job{}).Where("id = ?", queued[0].ID).Update("run_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	ran, err := jobs.NewRunner(app.DB, kind).RunOnce(context.Background(), "test:reconcile")
	if err != nil || !ran {
		t.Fatalf("run: %v %v", ran, err)
	}
	job := reloadJob(t, app, queued[0].ID)
	if job.Status != models.JobFailed || job.LastError != readErr.Error() || !job.RunAt.After(time.Now()) {
		t.Fatalf("hourly read failure did not retry: %+v", job)
	}
}

func TestNotifyReconcileBackfillSkipsClosedNotices(t *testing.T) {
	app := testApp(t)
	u := newTestUser(t, app, "recipient", "password1234", models.RoleAdmin)
	now := time.Now().UTC()
	at := now.Add(-time.Hour)
	set := func(n models.Notification, col string, v any) models.Notification {
		t.Helper()
		if err := app.DB.Model(&models.Notification{}).Where("id = ?", n.ID).Update(col, v).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}
	resolved := preOutboxNotice(t, app, u, notify.GroupPending, at, nil)
	set(resolved, "waiting", true)
	set(resolved, "resolved_at", at.Add(time.Minute))
	expired := set(preOutboxNotice(t, app, u, notify.GroupApproved, at, nil), "expires_at", now.Add(-time.Minute))
	archived := set(preOutboxNotice(t, app, u, notify.GroupApproved, at, nil), "archived_at", at.Add(time.Minute))
	read := set(preOutboxNotice(t, app, u, notify.GroupApproved, at, nil), "read_at", at.Add(time.Minute))
	open := preOutboxNotice(t, app, u, notify.GroupPending, at, nil)
	set(open, "waiting", true)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.reconcileNotifications(ctx, now); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		id     uint
		closed bool
	}{"resolved task": {resolved.ID, true}, "expired": {expired.ID, true}, "archived": {archived.ID, true},
		"read ordinary": {read.ID, false}, "open task": {open.ID, false}} {
		var d models.NotificationDelivery
		if err := app.DB.Where("notification_id = ? AND channel = ?", tc.id, models.DeliveryEmail).First(&d).Error; err != nil {
			t.Fatalf("%s: no email row: %v", name, err)
		}
		var njobs int64
		if err := app.DB.Model(&models.Job{}).Where("unique_key = ?", notify.DeliverKey(tc.id)).Count(&njobs).Error; err != nil {
			t.Fatal(err)
		}
		if tc.closed && (d.Status != models.DeliverySuppressed || d.Reason != "bell only" || njobs != 0) {
			t.Errorf("%s: want suppressed bell only with no job, got %q %q and %d jobs", name, d.Status, d.Reason, njobs)
		}
		if !tc.closed && (d.Status != models.DeliveryQueued || njobs != 1) {
			t.Errorf("%s: want queued with one job, got %q and %d jobs", name, d.Status, njobs)
		}
	}
}
