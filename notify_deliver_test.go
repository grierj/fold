package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
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

	"github.com/jackc/pgx/v5/pgconn"
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
		id, _ := seedNotifyDeliver(t, app, u, notify.DeliverJob.MaxAttempts)
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
		id, _ := seedNotifyDeliver(t, app, u, notify.DeliverJob.MaxAttempts)
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
		{name: "transient", attempts: notify.DeliverJob.MaxAttempts, sendErr: dirty, status: models.DeliveryQueued, job: models.JobFailed, later: true, calls: 1, clean: true},
		{name: "permanent", attempts: notify.DeliverJob.MaxAttempts, sendErr: permanent, status: models.DeliveryFailed, reason: jobs.CleanError(permanent), job: models.JobDone, calls: 1},
		{name: "not configured", attempts: notify.DeliverJob.MaxAttempts, sendErr: noSenderErr, status: models.DeliveryFailed, reason: jobs.CleanError(noSenderErr), job: models.JobDone, calls: 1},
		{name: "suppressed address", attempts: notify.DeliverJob.MaxAttempts, sendErr: dirty, prepare: suppressDeliverAddress, status: models.DeliverySuppressed, reason: "email to this address bounces", job: models.JobDone, calls: 0},
		{name: "opt-out", attempts: notify.DeliverJob.MaxAttempts, sendErr: dirty, prepare: optOutDeliverEmail, status: models.DeliverySuppressed, reason: "optional email turned off", job: models.JobDone, calls: 0},
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

// notify.deliver reads the address's open rows with the recipient, hashed or
// legacy NULL-hash, in either order. A hard bounce or manual row stops every
// notice; a complaint stops an optional kind but not a required one (DEC-112);
// cleared rows stop nothing. A stopped notice records the reason in words
// without the address, and the sender is not called.
func TestNotifyDeliverHashedAndLegacySuppressions(t *testing.T) {
	for _, kind := range []struct {
		name, key string
		required  bool
	}{
		{"optional kind", notify.GroupApproved, false},
		{"required kind", notify.OccurrenceChanged, true},
	} {
		if k, ok := notify.Lookup(kind.key); !ok || k.Required != kind.required {
			t.Fatalf("%s: registry says %+v", kind.name, k)
		}
		for _, tc := range suppressionCases() {
			t.Run(kind.name+"/"+tc.name, func(t *testing.T) {
				app := testApp(t)
				u := newTestUser(t, app, "pat", "password123456", models.RoleMember)
				sender := &notifyFailSender{}
				app.Mailer = sender
				suppressSpecs(t, app, strings.ToLower(u.Email), tc.specs...)
				id := sendNotice(t, app, kind.key, u)
				var job models.Job
				if err := app.DB.Where("unique_key = ?", notify.DeliverKey(id)).First(&job).Error; err != nil {
					t.Fatal(err)
				}
				runNotifyOnce(t, app)
				stop := tc.optional
				if kind.required {
					stop = tc.required
				}
				d := emailDeliveryOf(t, app, id)
				wantReason := map[string]string{
					"":                        "",
					models.SuppressComplaint:  "this address marked our email as spam",
					models.SuppressHardBounce: "email to this address bounces",
					models.SuppressManual:     "email to this address bounces",
				}[stop]
				if stop != "" {
					if d.Status != models.DeliverySuppressed || d.Reason != wantReason || sender.n() != 0 {
						t.Fatalf("delivery %q %q, %d sends; want suppressed %q and none", d.Status, d.Reason, sender.n(), wantReason)
					}
				} else if d.Status != models.DeliverySent || sender.n() != 1 {
					t.Fatalf("delivery %q %q, %d sends; want sent once", d.Status, d.Reason, sender.n())
				}
				if got := reloadJob(t, app, job.ID); got.Status != models.JobDone {
					t.Fatalf("job %+v", got)
				}
			})
		}
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
	id, _ := seedNotifyDeliver(t, app, u, notify.DeliverJob.MaxAttempts)
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
			name: "deferred row sends on rerun", attempts: notify.DeliverJob.MaxAttempts,
			seedStatus: models.DeliveryDeferred, mailer: "default",
			wantStatus: models.DeliverySent, wantJob: models.JobDone, wantID: true,
		},
		{
			name: "transient failure returns deferred to queued", attempts: notify.DeliverJob.MaxAttempts,
			seedStatus: models.DeliveryDeferred, mailer: "fail", sendErr: busy,
			wantStatus: models.DeliveryQueued, wantReason: jobs.CleanError(busy),
			wantJob: models.JobFailed, wantNoMail: true,
		},
		{
			name: "nil mailer", attempts: notify.DeliverJob.MaxAttempts, mailer: "nil",
			wantStatus: models.DeliveryFailed, wantReason: jobs.CleanError(noSenderErr),
			wantJob: models.JobDone, wantNoMail: true,
		},
		{
			name: "updated_at moves past created_at", attempts: notify.DeliverJob.MaxAttempts,
			mailer: "default", backdate: true,
			wantStatus: models.DeliverySent, wantJob: models.JobDone, wantUpdated: true, wantID: true,
		},
		{
			name: "non-tracking sender stores a null provider id", attempts: notify.DeliverJob.MaxAttempts,
			mailer: "plain", presetID: "old-id",
			wantStatus: models.DeliverySent, wantJob: models.JobDone, wantNullID: true, wantNoMail: true,
		},
		{
			name: "unknown kind waits", attempts: notify.DeliverJob.MaxAttempts,
			kind:       "kind.not.in.this.build",
			wantStatus: models.DeliveryQueued, wantJob: models.JobQueued,
			wantAttempts: 0, checkAttempt: true, wantHour: true, unchanged: true, wantNoMail: true,
		},
		{
			name: "unknown kind past a week is suppressed", attempts: notify.DeliverJob.MaxAttempts,
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
		if _, err := enqueueJob(app.DB, kind, nil, time.Now(), kind+":unrelated", 8); err != nil {
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
		if job.Status != models.JobQueued || job.MaxAttempts != notify.DeliverJob.MaxAttempts {
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
	// The orphan page's one row is protected before it is queued (R4-N): two
	// bulk reads for the whole page (recipients, then notices), never one per
	// row. The empty backfill page protects nothing.
	if reads := qc.Reads(); reads != 2+reconcileProtectReads {
		t.Fatalf("reconcile page reads = %d, want 2 page reads and %d protection reads", reads, reconcileProtectReads)
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
	for run := range 2 {
		qc := countDBReads(t, app.DB)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := app.reconcileNotifications(ctx, now)
		cancel()
		qc.Stop()
		if err != nil {
			t.Fatal(err)
		}
		// The first run finds the one deliverable row on its second backfill
		// page and protects it (two bulk reads, R4-N); the second run finds it
		// already has a delivery row and protects nothing. The skipped kinds
		// never reach the protection step.
		want := int64(3)
		if run == 0 {
			want += reconcileProtectReads
		}
		if reads := qc.Reads(); reads != want {
			t.Fatalf("run %d: page reads = %d, want %d (one orphan and two backfill pages, plus protection)", run, reads, want)
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
	if kind.Run == nil || kind.Label != "Checking for notices that still need sending" || kind.Attempts() != jobs.DefaultMaxAttempts {
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

// reconcileProtectReads is what protecting one non-empty repair page costs: one
// read to lock the recipients and one to lock the notices (notify.Protect).
const reconcileProtectReads = 2

// ---------------------------------------------------------------------------
// Erasure barriers (M7.4, R4-N). Waits observe a backend blocked on a named
// other backend (pg_blocking_pids); pauses are test-owned advisory locks taken
// by test-created statement triggers. Nothing sleeps and no production hook
// exists.
// ---------------------------------------------------------------------------

// beginOwnedTx begins a transaction the test owns and reports its backend pid.
func beginOwnedTx(t *testing.T, db *gorm.DB) (*gorm.DB, int) {
	t.Helper()
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	var pid int
	if err := tx.Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
		t.Fatal(err)
	}
	return tx, pid
}

// eraseNoticesTx is what an erasure does to a person's notices, in the order
// apiAdminDeleteUser does it: the user row FOR UPDATE, their notifications
// deleted (their deliveries cascade), then the soft delete.
func eraseNoticesTx(tx *gorm.DB, uid uint) error {
	if err := tx.Exec("SELECT id FROM users WHERE id = ? FOR UPDATE", uid).Error; err != nil {
		return err
	}
	if err := tx.Exec("DELETE FROM notifications WHERE user_id = ?", uid).Error; err != nil {
		return err
	}
	return tx.Exec("UPDATE users SET deleted_at = now() WHERE id = ?", uid).Error
}

func eraseNoticesNow(t *testing.T, db *gorm.DB, uid uint) {
	t.Helper()
	if err := db.Transaction(func(tx *gorm.DB) error { return eraseNoticesTx(tx, uid) }); err != nil {
		t.Fatal(err)
	}
}

// waiterOn returns the pid of a backend blocked by blocker, once there is one.
func waiterOn(t *testing.T, db *gorm.DB, blocker int) int {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		var pid int
		if err := db.Raw(`SELECT coalesce(min(pid), 0) FROM pg_stat_activity
			WHERE datname = current_database() AND ? = ANY(pg_blocking_pids(pid))`, blocker).Scan(&pid).Error; err != nil {
			t.Fatal(err)
		}
		if pid != 0 {
			return pid
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatalf("no session ever waited on session %d", blocker)
		}
	}
}

// pauseOnStatement installs a statement-level trigger (when is "AFTER INSERT"
// or "BEFORE DELETE") that takes a transaction-level advisory lock, then drops
// it at cleanup. While a test holds that lock (holdAdvisory), the statement's
// transaction stops right there, still holding everything it took before.
// Call it after all the test's own writes to table and before holdAdvisory.
func pauseOnStatement(t *testing.T, db *gorm.DB, when, table string, key int64) {
	t.Helper()
	name := fmt.Sprintf("fold_test_pause_%d", key)
	if err := db.Exec(fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN PERFORM pg_advisory_xact_lock(%d); RETURN NULL; END $$`, name, key)).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(fmt.Sprintf(`CREATE TRIGGER %s %s ON %s FOR EACH STATEMENT EXECUTE FUNCTION %s()`, name, when, table, name)).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec("DROP TRIGGER IF EXISTS " + name + " ON " + table)
		db.Exec("DROP FUNCTION IF EXISTS " + name + "()")
	})
}

// holdAdvisory takes the lock a paused statement waits for. release lets it go.
func holdAdvisory(t *testing.T, db *gorm.DB, key int64) (release func(), pid int) {
	t.Helper()
	h, pid := beginOwnedTx(t, db)
	if err := h.Exec("SELECT pg_advisory_xact_lock(?)", key).Error; err != nil {
		t.Fatal(err)
	}
	return func() { h.Rollback() }, pid
}

func rowsFor(t *testing.T, db *gorm.DB, table string, uid uint) int64 {
	t.Helper()
	var n int64
	if err := db.Table(table).Where("user_id = ?", uid).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func totalRows(t *testing.T, db *gorm.DB, table string) int64 {
	t.Helper()
	var n int64
	if err := db.Table(table).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func jobExistsFor(t *testing.T, app *App, notificationID uint) bool {
	t.Helper()
	var n int64
	if err := app.DB.Model(&models.Job{}).Where("unique_key = ?", notify.DeliverKey(notificationID)).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n > 0
}

// A repair page is selected before its transaction. When a person is erased
// (or a notice is pruned) in between, the stale rows are dropped inside the
// transaction: no delivery row or job is recreated, nothing errors on a
// missing notice, and a recipient who was already deleted before the pass
// (a legacy tombstone whose notice survived) gets nothing either.
func TestNotifyReconcileRepairSkipsStaleRows(t *testing.T) {
	app := testApp(t)
	live := newTestUser(t, app, "live", "password1234", models.RoleMember)
	erased := newTestUser(t, app, "erased", "password1234", models.RoleMember)
	tomb := newTestUser(t, app, "tomb", "password1234", models.RoleMember)
	now := time.Now().UTC()
	t.Run("backfill page", func(t *testing.T) {
		at := now.Add(-time.Hour)
		liveN := preOutboxNotice(t, app, live, notify.GroupApproved, at, nil)
		erasedN := preOutboxNotice(t, app, erased, notify.GroupApproved, at, nil)
		tombN := preOutboxNotice(t, app, tomb, notify.GroupApproved, at, nil)
		page := []models.Notification{liveN, erasedN, tombN}
		eraseNoticesNow(t, app.DB, erased.ID)
		if err := app.DB.Delete(tomb).Error; err != nil {
			t.Fatal(err)
		}
		if err := app.DB.Transaction(func(tx *gorm.DB) error { return queueReconciledNotifications(tx, page, now) }); err != nil {
			t.Fatalf("a stale page must be skipped, not fail: %v", err)
		}
		var deliveries []models.NotificationDelivery
		if err := app.DB.Find(&deliveries).Error; err != nil {
			t.Fatal(err)
		}
		if len(deliveries) != 1 || deliveries[0].NotificationID != liveN.ID || deliveries[0].UserID != live.ID {
			t.Fatalf("deliveries: %+v, want only the living recipient's", deliveries)
		}
		if !jobExistsFor(t, app, liveN.ID) || jobExistsFor(t, app, erasedN.ID) || jobExistsFor(t, app, tombN.ID) {
			t.Fatal("jobs were not limited to the living recipient")
		}
	})
	t.Run("lost job page", func(t *testing.T) {
		live2 := newTestUser(t, app, "live2", "password1234", models.RoleMember)
		erased2 := newTestUser(t, app, "erased2", "password1234", models.RoleMember)
		old := now.Add(-25 * time.Hour)
		var page []models.NotificationDelivery
		var kept, dropped models.Notification
		for _, u := range []*models.User{live2, erased2} {
			n := preOutboxNotice(t, app, u, notify.GroupApproved, old, nil)
			d := models.NotificationDelivery{NotificationID: n.ID, UserID: u.ID, Channel: models.DeliveryEmail,
				Status: models.DeliveryQueued, CreatedAt: now.Add(-11 * time.Minute)}
			if err := app.DB.Create(&d).Error; err != nil {
				t.Fatal(err)
			}
			page = append(page, d)
			if u == live2 {
				kept = n
			} else {
				dropped = n
			}
		}
		eraseNoticesNow(t, app.DB, erased2.ID)
		if err := app.DB.Transaction(func(tx *gorm.DB) error { return requeueLostDeliveries(tx, page, now) }); err != nil {
			t.Fatalf("a stale page must be skipped, not fail: %v", err)
		}
		if !jobExistsFor(t, app, kept.ID) || jobExistsFor(t, app, dropped.ID) {
			t.Fatal("jobs were not limited to the living recipient")
		}
	})
}

// The real repair, end to end, against an erasure in either order and with no
// contact gate: if the erasure got there first, the repair waits, rechecks and
// queues nothing; if the repair got there first, the erasure waits for it and
// then removes what it wrote.
func TestNotifyReconcileSerializesWithErasure(t *testing.T) {
	now := time.Now().UTC()
	for _, pass := range []string{"backfill", "lost job"} {
		for _, erasureFirst := range []bool{true, false} {
			name := pass + "/repair first"
			if erasureFirst {
				name = pass + "/erasure first"
			}
			t.Run(name, func(t *testing.T) {
				app := testApp(t)
				u := newTestUser(t, app, "recipient", "password1234", models.RoleMember)
				var n models.Notification
				table := "notification_deliveries" // what the repair inserts first
				if pass == "backfill" {
					n = preOutboxNotice(t, app, u, notify.GroupApproved, now.Add(-time.Hour), nil)
				} else {
					table = "jobs"
					n = preOutboxNotice(t, app, u, notify.GroupApproved, now.Add(-25*time.Hour), nil)
					if err := app.DB.Create(&models.NotificationDelivery{NotificationID: n.ID, UserID: u.ID, Channel: models.DeliveryEmail,
						Status: models.DeliveryQueued, CreatedAt: now.Add(-11 * time.Minute)}).Error; err != nil {
						t.Fatal(err)
					}
				}
				repair := func(db *gorm.DB) error {
					return (&App{DB: db}).reconcileNotifications(db.Statement.Context, now)
				}
				if erasureFirst {
					eraser, ePID := beginOwnedTx(t, app.DB)
					if err := eraseNoticesTx(eraser, u.ID); err != nil {
						t.Fatal(err)
					}
					pid, done := startDBSession(t, app.DB, "reconcile", repair)
					waitDBBlocked(t, app.DB, pid, ePID)
					if err := eraser.Commit().Error; err != nil {
						t.Fatal(err)
					}
					if err := awaitDBResult(t, done); err != nil {
						t.Fatalf("a repair that lost the race must skip: %v", err)
					}
					if deliveries, queued := totalRows(t, app.DB, "notification_deliveries"), totalRows(t, app.DB, "jobs"); deliveries != 0 || queued != 0 {
						t.Fatalf("repair recreated work for an erased person: %d deliveries, %d jobs", deliveries, queued)
					}
				} else {
					const key = 7401001
					pauseOnStatement(t, app.DB, "AFTER INSERT", table, key)
					release, hPID := holdAdvisory(t, app.DB, key)
					pid, done := startDBSession(t, app.DB, "reconcile", repair)
					waitDBBlocked(t, app.DB, pid, hPID)
					eraser, ePID := beginOwnedTx(t, app.DB)
					erased := make(chan error, 1)
					go func() { erased <- eraseNoticesTx(eraser, u.ID) }()
					waitDBBlocked(t, app.DB, ePID, pid)
					release()
					if err := awaitDBResult(t, done); err != nil {
						t.Fatal(err)
					}
					if err := awaitDBResult(t, erased); err != nil {
						t.Fatal(err)
					}
					if err := eraser.Commit().Error; err != nil {
						t.Fatal(err)
					}
					if deliveries := totalRows(t, app.DB, "notification_deliveries"); deliveries != 0 {
						t.Fatalf("%d delivery rows outlived the erasure", deliveries)
					}
					// A job the repair committed before the erasure waits for no
					// notice: delivering it finds nothing and sends nothing.
					if err := app.deliverOneNotification(context.Background(), n.ID); err != nil {
						t.Fatal(err)
					}
					if mails := mailCount(t, app, ""); mails != 0 {
						t.Fatalf("%d emails for an erased person", mails)
					}
				}
				if rowsFor(t, app.DB, "notifications", u.ID) != 0 {
					t.Fatal("a notice survived the erasure")
				}
			})
		}
	}
}

// ---------------------------------------------------------------------------
// Real-operation erasure barriers (M7.4, R4-N; inventory 9.2.5). Each site's
// test names its real entry, builds its own rows, and calls one driver, which
// runs the operation against an erasure of one recipient in both orders:
//
//   - deletion first: a test-owned transaction takes the victim's row FOR
//     UPDATE and deletes their notices (the statements apiAdminDeleteUser runs),
//     the operation is started, and the driver waits until pg_blocking_pids
//     shows the operation's backend waiting on that transaction, in the
//     statement the scene names (by default the recipient lock). Then the
//     erasure commits and the operation must finish without a notice for the
//     tombstone.
//   - sender first: a row trigger the test created pauses the operation right
//     after it inserted the victim's notice, its transaction still open; the
//     erasure is started and must wait on the operation; then the pause is
//     released, and the erasure removes what the operation wrote.
//
// No sleeps and no production hook: every wait is an observed lock wait or an
// operation result with a timeout. The eraser here takes no contact gate, so a
// scene is the plain-entry case; runGatedErasureBarriers is the gated one.
// ---------------------------------------------------------------------------

// erasureScene is one real operation and the person erased while it runs.
type erasureScene struct {
	app    *App
	victim *models.User // the recipient the erasure removes
	// run is the real operation. It runs on its own goroutine: it returns its
	// error and never calls t.Fatal.
	run func() error
	// blockedIn is the statement run waits in when the erasure committed first;
	// "" is the recipient lock (users ... FOR KEY SHARE).
	blockedIn string
	// told is checked after both orders: who the operation still had to tell.
	told func(t *testing.T)
	// mailBase is the mail the victim already had before the operation ran
	// (setup mail), which is not mail sent after the erasure.
	mailBase int64
}

// recipientLockText is what the recipient lock statement contains.
var recipientLockText = []string{"FROM users WHERE id IN", "FOR KEY SHARE"}

func startOperation(fn func() error) <-chan error {
	done := make(chan error, 1)
	go func() { done <- fn() }()
	return done
}

// doExpect sends one request and returns an error unless it answers a wanted
// status, so it is safe on an operation's goroutine.
func doExpect(c *apiClient, method, path string, body any, want ...int) error {
	w := c.do(method, path, body, true)
	for _, code := range want {
		if w.Code == code {
			return nil
		}
	}
	return fmt.Errorf("%s %s answered %d: %s", method, path, w.Code, w.Body.String())
}

// statementOf is the statement a backend is running now.
func statementOf(t *testing.T, db *gorm.DB, pid int) string {
	t.Helper()
	var q string
	if err := db.Raw("SELECT query FROM pg_stat_activity WHERE pid = ?", pid).Scan(&q).Error; err != nil {
		t.Fatal(err)
	}
	return q
}

// pauseAfterNoticeFor makes any transaction that inserts a notice for uid wait
// on a transaction-level advisory lock right after the insert, still holding
// everything it took before. Like pauseOnStatement it drops itself at cleanup.
func pauseAfterNoticeFor(t *testing.T, db *gorm.DB, uid uint, key int64) {
	t.Helper()
	name := fmt.Sprintf("fold_test_pause_row_%d", key)
	if err := db.Exec(fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN PERFORM pg_advisory_xact_lock(%d); RETURN NULL; END $$`, name, key)).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(fmt.Sprintf(`CREATE TRIGGER %s AFTER INSERT ON notifications FOR EACH ROW
		WHEN (NEW.user_id = %d) EXECUTE FUNCTION %s()`, name, uid, name)).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec("DROP TRIGGER IF EXISTS " + name + " ON notifications")
		db.Exec("DROP FUNCTION IF EXISTS " + name + "()")
	})
}

// settleErasure checks what is true after the operation and the erasure both
// finished: the victim is deleted and holds no notice or delivery row, no email
// is sent to them once the queued deliveries run, and the scene's own checks.
func settleErasure(t *testing.T, sc erasureScene) {
	t.Helper()
	db := sc.app.DB
	if n := rowsFor(t, db, "notifications", sc.victim.ID); n != 0 {
		t.Fatalf("%d notices left for the erased person", n)
	}
	if n := rowsFor(t, db, "notification_deliveries", sc.victim.ID); n != 0 {
		t.Fatalf("%d delivery rows left for the erased person", n)
	}
	var gone int64
	if err := db.Unscoped().Model(&models.User{}).Where("id = ? AND deleted_at IS NOT NULL", sc.victim.ID).Count(&gone).Error; err != nil || gone != 1 {
		t.Fatalf("the erasure did not commit: %d %v", gone, err)
	}
	drainNotices(t, sc.app)
	if n := mailCount(t, sc.app, sc.victim.Email); n != sc.mailBase {
		t.Fatalf("%d emails to the erased person, want the %d sent before the operation", n, sc.mailBase)
	}
	if sc.told != nil {
		sc.told(t)
	}
}

// runErasureBarriers runs the scene built by setup in both orders. key is the
// advisory lock key the sender-first pause uses; give each call its own.
func runErasureBarriers(t *testing.T, key int64, setup func(t *testing.T) erasureScene) {
	t.Helper()
	t.Run("deletion first", func(t *testing.T) {
		sc := setup(t)
		db := sc.app.DB
		eraser, ePID := beginOwnedTx(t, db)
		if err := eraseNoticesTx(eraser, sc.victim.ID); err != nil {
			t.Fatal(err)
		}
		done := startOperation(sc.run)
		opPID := waiterOn(t, db, ePID)
		want := recipientLockText
		if sc.blockedIn != "" {
			want = []string{sc.blockedIn}
		}
		q := statementOf(t, db, opPID)
		for _, part := range want {
			if !strings.Contains(q, part) {
				t.Fatalf("the operation waits in %q, want a statement containing %q", q, part)
			}
		}
		if err := eraser.Commit().Error; err != nil {
			t.Fatal(err)
		}
		if err := awaitDBResult(t, done); err != nil {
			t.Fatalf("an operation that lost the race must finish: %v", err)
		}
		settleErasure(t, sc)
	})
	t.Run("sender first", func(t *testing.T) {
		sc := setup(t)
		db := sc.app.DB
		pauseAfterNoticeFor(t, db, sc.victim.ID, key)
		release, hPID := holdAdvisory(t, db, key)
		done := startOperation(sc.run)
		opPID := waiterOn(t, db, hPID) // it wrote the victim's notice and its transaction is open
		eraser, ePID := beginOwnedTx(t, db)
		erased := startOperation(func() error { return eraseNoticesTx(eraser, sc.victim.ID) })
		waitDBBlocked(t, db, ePID, opPID) // the erasure waits for the operation
		release()
		if err := awaitDBResult(t, done); err != nil {
			t.Fatal(err)
		}
		if err := awaitDBResult(t, erased); err != nil {
			t.Fatal(err)
		}
		if err := eraser.Commit().Error; err != nil {
			t.Fatal(err)
		}
		settleErasure(t, sc)
	})
}

// gatedErasureScene is erasureScene with the real admin delete as the erasure,
// for operations that take the contact gate today: both sides queue on it.
type gatedErasureScene struct {
	app    *App
	victim *models.User
	run    func() error // the real gated operation
	erase  func() error // the real DELETE /api/v1/admin/users/:id for the victim
	told   func(t *testing.T)
	// runDeletionFirst replaces run in the deletion-first order when the
	// operation's answer differs there (an erased caller is signed out, or the
	// person it acts on is gone).
	runDeletionFirst func() error
	// sends is the set of notice kinds the victim must have been sent when the
	// operation won (sender first), and the victim must have been sent none when
	// the erasure won. A non-nil value turns the check on; use an empty, non-nil
	// slice for "none", though the driver already requires none in the second
	// order. It is read from a recorder the driver installs (recordNoticesFor),
	// which keeps the kind of every notice inserted for the victim even after the
	// erasure deletes the rows, so it names the Send site that really ran.
	sends []string
	// gateSkip, when set, says why the operation in the deletion-first order
	// does not wait on the contact gate first (its first lock is the victim's
	// own row, taken before the gate). The driver then checks only that the
	// operation waits on the delete.
	gateSkip string
	// mailBase is set by the driver: the victim's mail before the operation,
	// so setup mail is not mistaken for mail sent after the erasure.
	mailBase int64
}

// requireContactGate fails unless backend pid holds (granted) or waits for
// (not granted) the contact gate itself: the advisory lock whose key is
// contactLockID, read from pg_locks. Any other lock, such as a row or the
// test's own pause, does not satisfy it.
func requireContactGate(t *testing.T, db *gorm.DB, pid int, granted bool, who string) {
	t.Helper()
	var n int64
	if err := db.Raw(`SELECT count(*) FROM pg_locks WHERE pid = ? AND locktype = 'advisory' AND granted = ?
		AND classid::bigint = ? AND objid::bigint = ? AND objsubid = 1`,
		pid, granted, contactLockID>>32, contactLockID&0xffffffff).Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		verb := "waits for"
		if granted {
			verb = "holds"
		}
		t.Fatalf("%s (backend %d) does not %s the contact gate (%d matching locks); its statement is %q",
			who, pid, verb, n, statementOf(t, db, pid))
	}
}

// noticeRecorder keeps the kind of every notice inserted for one person, in a
// table the test created and an AFTER INSERT trigger fills inside the inserting
// transaction. The erasure deletes the notice rows; this record survives, so it
// shows which sends actually reached that person.
type noticeRecorder struct {
	db    *gorm.DB
	table string
}

func recordNoticesFor(t *testing.T, db *gorm.DB, uid uint, key int64) *noticeRecorder {
	t.Helper()
	table := fmt.Sprintf("fold_test_seen_%d", key)
	fn := table + "_fn"
	for _, stmt := range []string{
		fmt.Sprintf("CREATE TABLE %s (user_id bigint NOT NULL, kind text NOT NULL)", table),
		fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN INSERT INTO %s (user_id, kind) VALUES (NEW.user_id, NEW.kind); RETURN NULL; END $$`, fn, table),
		fmt.Sprintf(`CREATE TRIGGER %s AFTER INSERT ON notifications FOR EACH ROW
			WHEN (NEW.user_id = %d) EXECUTE FUNCTION %s()`, fn, uid, fn),
	} {
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		db.Exec("DROP TRIGGER IF EXISTS " + fn + " ON notifications")
		db.Exec("DROP FUNCTION IF EXISTS " + fn + "()")
		db.Exec("DROP TABLE IF EXISTS " + table)
	})
	return &noticeRecorder{db: db, table: table}
}

// kinds is the distinct kinds recorded so far, sorted.
func (r *noticeRecorder) kinds(t *testing.T) []string {
	t.Helper()
	var out []string
	if err := r.db.Raw("SELECT DISTINCT kind FROM " + r.table + " ORDER BY kind").Scan(&out).Error; err != nil {
		t.Fatal(err)
	}
	return out
}

// eraseVia is the real DELETE /api/v1/admin/users/:id, as an operation.
func eraseVia(admin *apiClient, victim *models.User) func() error {
	return func() error {
		return doExpect(admin, "DELETE", "/api/v1/admin/users/"+itoa(victim.ID), nil, http.StatusOK)
	}
}

// loginHeld signs in a client whose requests leave notice jobs queued, so it
// may be used on an operation's goroutine (it never calls t.Fatal there).
func loginHeld(t *testing.T, app *App, name, password string) *apiClient {
	t.Helper()
	c := loginAPI(t, app, name, password)
	c.holdNotices = true
	return c
}

// countOf is the number of notices of one kind a person holds.
func countOf(t *testing.T, app *App, uid uint, kind string) int64 {
	t.Helper()
	return contactCount(t, app, &models.Notification{}, "user_id = ? AND kind = ?", uid, kind)
}

// wantNotices fails unless each person holds the given number of notices of the
// kind (the surviving recipients of an operation).
func wantNotices(t *testing.T, app *App, kind string, want map[*models.User]int64) {
	t.Helper()
	for u, n := range want {
		if got := countOf(t, app, u.ID, kind); got != n {
			t.Fatalf("%s has %d %s notices, want %d", u.Name, got, kind, n)
		}
	}
}

// runGatedErasureBarriers runs a gated operation against the real delete in both
// orders, and in each proves which side holds and which waits for the contact
// gate itself (pg_locks, not just "blocked").
//
// Sender first: the operation stops right after it wrote the victim's notice,
// holding the gate; the delete queues on the gate (it must wait for the gate
// key, not something else); released, the delete then removes the notice.
// Deletion first: the delete stops after it holds the gate and the user row,
// before it removes the notices; the operation queues on the gate and, released,
// reads its facts after the delete committed. keyA and keyB are distinct
// advisory keys for the two pauses; keyA+1000000 and keyB+1000000 name the
// recorder tables, so use keys below that.
//
// In both orders the victim must end up deleted with no notification or
// delivery row, no mail beyond what setup already sent, and (when the scene
// sets sends) the recorded kinds the scene names: exactly those when the
// operation won, none when the erasure won.
func runGatedErasureBarriers(t *testing.T, keyA, keyB int64, setup func(t *testing.T) gatedErasureScene) {
	t.Helper()
	prepare := func(t *testing.T) gatedErasureScene {
		t.Helper()
		sc := setup(t)
		// Mail that setup queued is sent now, so only mail sent after the
		// operation can count against the erased person.
		drainNotices(t, sc.app)
		sc.mailBase = mailCount(t, sc.app, sc.victim.Email)
		return sc
	}
	settle := func(t *testing.T, sc gatedErasureScene, rec *noticeRecorder, wantKinds []string) {
		t.Helper()
		settleErasure(t, erasureScene{app: sc.app, victim: sc.victim, told: sc.told, mailBase: sc.mailBase})
		if sc.sends == nil {
			return
		}
		want := append([]string{}, wantKinds...)
		sort.Strings(want)
		if got := rec.kinds(t); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("notice kinds sent to the erased person: %v, want %v", got, want)
		}
	}
	t.Run("sender first", func(t *testing.T) {
		sc := prepare(t)
		db := sc.app.DB
		rec := recordNoticesFor(t, db, sc.victim.ID, keyA+1000000)
		pauseAfterNoticeFor(t, db, sc.victim.ID, keyA)
		release, hPID := holdAdvisory(t, db, keyA)
		done := startOperation(sc.run)
		opPID := waiterOn(t, db, hPID)
		requireContactGate(t, db, opPID, true, "the operation, stopped after it wrote the victim's notice,")
		erased := startOperation(sc.erase)
		deleter := waiterOn(t, db, opPID) // the delete queues behind the operation
		requireContactGate(t, db, deleter, false, "the delete")
		release()
		if err := awaitDBResult(t, done); err != nil {
			t.Fatal(err)
		}
		if err := awaitDBResult(t, erased); err != nil {
			t.Fatal(err)
		}
		settle(t, sc, rec, sc.sends)
	})
	t.Run("deletion first", func(t *testing.T) {
		sc := prepare(t)
		db := sc.app.DB
		rec := recordNoticesFor(t, db, sc.victim.ID, keyB+1000000)
		pauseOnStatement(t, db, "BEFORE DELETE", "notifications", keyB)
		release, hPID := holdAdvisory(t, db, keyB)
		erased := startOperation(sc.erase)
		deleter := waiterOn(t, db, hPID) // the delete holds the gate and the user row
		requireContactGate(t, db, deleter, true, "the delete, stopped before it removes the notices,")
		run := sc.run
		if sc.runDeletionFirst != nil {
			run = sc.runDeletionFirst
		}
		done := startOperation(run)
		opPID := waiterOn(t, db, deleter) // the operation queues behind the delete
		if sc.gateSkip == "" {
			requireContactGate(t, db, opPID, false, "the operation")
		}
		release()
		if err := awaitDBResult(t, erased); err != nil {
			t.Fatal(err)
		}
		if err := awaitDBResult(t, done); err != nil {
			t.Fatalf("an operation that lost the race must finish as the scene expects: %v", err)
		}
		settle(t, sc, rec, nil)
	})
}

func TestNotificationDeliveryHonorsRetainedAddressChoices(t *testing.T) {
	// Optional categories with registered notices, plus both required classes.
	for _, key := range []string{notify.GroupApproved, notify.LeadershipChanged, notify.EventChanged, notify.OccurrenceChanged, notify.GroupPending} {
		t.Run(key, func(t *testing.T) {
			app := testApp(t)
			u := newTestUser(t, app, "notice-choice", "password1234", models.RoleAdmin)
			sender := &notifyFailSender{}
			app.Mailer = sender
			id := sendNotice(t, app, key, u)
			// The choice arrives after queueing, so delivery must read it again.
			seedAllEmailAddressOff(t, app, u.Email)
			k, ok := notify.Lookup(key)
			if !ok {
				t.Fatal("unregistered notice")
			}
			runNotifyOnce(t, app)
			d := emailDeliveryOf(t, app, id)
			if k.Required {
				if d.Status != models.DeliverySent || sender.n() != 1 {
					t.Fatalf("required notice stopped: %+v, calls=%d", d, sender.n())
				}
			} else if d.Status != models.DeliverySuppressed || d.Reason != "optional email turned off" || sender.n() != 0 {
				t.Fatalf("optional notice sent: %+v, calls=%d", d, sender.n())
			}
		})
	}
}

func TestNotificationChoiceReadFailureDoesNotBlockRequired(t *testing.T) {
	for _, table := range []string{"consents", "email_address_optouts"} {
		t.Run(table, func(t *testing.T) {
			app := testApp(t)
			u := newTestUser(t, app, "notice-choice-fault", "password1234", models.RoleAdmin)
			sender := &notifyFailSender{}
			app.Mailer = sender
			optional := sendNotice(t, app, notify.GroupApproved, u)
			required := []uint{
				sendNotice(t, app, notify.OccurrenceChanged, u), // safety
				sendNotice(t, app, notify.GroupPending, u),      // waiting
			}
			name := fmt.Sprintf("test:notification_choice_fault:%d", queryCounterSeq.Add(1))
			reads, fail := 0, true
			if err := app.DB.Callback().Row().Before("gorm:row").Register(name, func(tx *gorm.DB) {
				statement := tx.Statement.SQL.String()
				if !fail || !strings.Contains(statement, "FROM "+table) {
					return
				}
				reads++
				// Let PostgreSQL fail the choice query itself, including aborting
				// its claim transaction. No schema or other reads are changed.
				tx.Statement.SQL.Reset()
				tx.Statement.SQL.WriteString(strings.ReplaceAll(statement, "FROM "+table, "FROM missing_notification_choice_"+table))
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = app.DB.Callback().Row().Remove(name) })

			// Positive control: the fault reaches the optional choice read and
			// must leave its delivery retryable without calling the provider.
			err := app.deliverOneNotification(context.Background(), optional)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "42P01" || reads != 1 || sender.n() != 0 {
				t.Fatalf("optional choice fault: err=%v, reads=%d, calls=%d", err, reads, sender.n())
			}
			if d := emailDeliveryOf(t, app, optional); d.Status != models.DeliveryQueued || d.SentAt != nil {
				t.Fatalf("choice fault changed optional delivery: %+v", d)
			}
			for i, id := range required {
				if err := app.deliverOneNotification(context.Background(), id); err != nil {
					t.Fatalf("required notice %d depended on choices: %v", id, err)
				}
				if d := emailDeliveryOf(t, app, id); d.Status != models.DeliverySent || d.SentAt == nil || sender.n() != i+1 || reads != 1 {
					t.Fatalf("required delivery: %+v, reads=%d, calls=%d", d, reads, sender.n())
				}
			}
			// Clearing the fault lets the rolled-back optional claim send once.
			fail = false
			if err := app.deliverOneNotification(context.Background(), optional); err != nil {
				t.Fatal(err)
			}
			if d := emailDeliveryOf(t, app, optional); d.Status != models.DeliverySent || sender.n() != len(required)+1 {
				t.Fatalf("optional retry: %+v, calls=%d", d, sender.n())
			}
		})
	}
}
