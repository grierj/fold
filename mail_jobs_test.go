package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fold/internal/audit"
	"fold/internal/mail"
	"fold/internal/providers"
	"fold/internal/testdb"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type directMailSenderFunc func(context.Context, mail.Message) error

func (f directMailSenderFunc) Send(ctx context.Context, msg mail.Message) error { return f(ctx, msg) }
func (f directMailSenderFunc) SendEmail(ctx context.Context, msg mail.Message) (string, error) {
	return "", f(ctx, msg)
}
func (directMailSenderFunc) Capabilities() providers.EmailCaps {
	return providers.EmailCaps{Name: "direct-test"}
}

func resetMailJobs(t *testing.T, app *App) []models.Job {
	t.Helper()
	var rows []models.Job
	if err := app.DB.Where("kind = ?", kindPasswordResetMail).Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

func queueResetMail(t *testing.T, app *App, user *models.User) models.Job {
	t.Helper()
	c := newAPIClient(t, app)
	c.holdNotices = true
	c.fetchCSRF()
	apiDo(t, c, "POST", "/api/v1/auth/password-reset", map[string]string{"email": user.Email}, http.StatusOK)
	rows := resetMailJobs(t, app)
	if len(rows) == 0 {
		t.Fatal("reset did not queue a delivery duty")
	}
	return rows[len(rows)-1]
}

// runResetMail runs the next due direct-mail job through the real runner. The
// runner claims the earliest due job of any kind, and a test that holds notices
// leaves earlier notify.deliver jobs queued. This runner knows only the mail
// kinds, so it would use its turn deferring one of those; each such job is
// deferred the same way (no attempt used, no side effect) until a mail job is
// the one claimed.
func runResetMail(t *testing.T, app *App) {
	t.Helper()
	mine := map[string]bool{}
	for _, k := range app.mailKinds() {
		mine[k.Name] = true
	}
	runner := app.newRunner(app.mailKinds()...)
	for range 100 {
		var next []string
		if err := app.DB.Model(&models.Job{}).Where("status IN ? AND run_at <= now()", []string{models.JobQueued, models.JobFailed}).
			Order("run_at, id").Limit(1).Pluck("kind", &next).Error; err != nil {
			t.Fatal(err)
		}
		if len(next) == 0 {
			t.Fatal("no due job to run")
		}
		ran, err := runner.RunOnce(context.Background(), "test:direct-mail")
		if err != nil || !ran {
			t.Fatalf("run direct mail: ran=%v err=%v", ran, err)
		}
		if mine[next[0]] {
			return
		}
	}
	t.Fatal("no direct-mail job became due")
}

// Invoke the registered production handler without claiming a job, so races
// exercise the same preparation path as the runner with a session we control.
func deliverResetMail(ctx context.Context, app *App, userID uint) error {
	for _, k := range app.mailKinds() {
		if k.Name == kindPasswordResetMail {
			return k.Run(ctx, json.RawMessage(jsonString(userMailJob{UserID: userID})))
		}
	}
	return errors.New("password reset job is not registered")
}

func TestPasswordResetEnqueueFailureIsUniformAndAtomic(t *testing.T) {
	app := testApp(t)
	user := newTestUser(t, app, "alice", "password1234", models.RoleMember)
	c := newAPIClient(t, app)
	c.holdNotices = true
	c.fetchCSRF()
	stop := failJobEnqueue(t, app.DB, kindPasswordResetMail)
	answer := apiDo(t, c, "POST", "/api/v1/auth/password-reset", map[string]string{"email": user.Email}, http.StatusOK)
	if answer != jsonString(map[string]string{"message": resetRequestMessage}) {
		t.Fatalf("enqueue failure changed public answer: %s", answer)
	}
	var tokens int64
	if err := app.DB.Model(&models.AccountToken{}).Count(&tokens).Error; err != nil {
		t.Fatal(err)
	}
	if tokens != 0 || len(resetMailJobs(t, app)) != 0 || auditCount(t, app, audit.PasswordResetRequested) != 0 || mailCount(t, app, "") != 0 {
		t.Fatal("failed enqueue left an audit, job, token or email")
	}
	stop()
	queueResetMail(t, app, user)
	if auditCount(t, app, audit.PasswordResetRequested) != 1 {
		t.Fatal("successful retry did not commit the audit")
	}
}

func TestPasswordResetPreparationRechecksAccount(t *testing.T) {
	for _, change := range []string{"email", "disabled", "guest", "pending_consent", "deleted", "missing"} {
		t.Run(change, func(t *testing.T) {
			app := testApp(t)
			user := newTestUser(t, app, "alice", "password1234", models.RoleMember)
			job := queueResetMail(t, app, user)
			switch change {
			case "email":
				if err := app.DB.Model(user).Update("email", "current@example.com").Error; err != nil {
					t.Fatal(err)
				}
			case "deleted", "missing":
				db := app.DB
				if change == "missing" {
					db = db.Unscoped()
				}
				if err := db.Delete(user).Error; err != nil {
					t.Fatal(err)
				}
			default:
				if err := app.DB.Model(user).Update("status", change).Error; err != nil {
					t.Fatal(err)
				}
			}
			runResetMail(t, app)
			if got := reloadJob(t, app, job.ID); got.Status != models.JobDone {
				t.Fatalf("job=%+v", got)
			}
			var tokens int64
			if err := app.DB.Model(&models.AccountToken{}).Where("user_id = ?", user.ID).Count(&tokens).Error; err != nil {
				t.Fatal(err)
			}
			if change == "email" {
				if tokens != 1 || mailCount(t, app, "current@example.com") != 1 || mailCount(t, app, "alice@example.com") != 0 {
					t.Fatal("reset did not use the current locked account email")
				}
			} else if tokens != 0 || mailCount(t, app, "") != 0 {
				t.Fatal("ineligible account got a token or mail")
			}
		})
	}
}

func TestPasswordResetPreparationUsesSingleConnection(t *testing.T) {
	for _, setting := range []string{"missing", "empty", "configured"} {
		t.Run(setting, func(t *testing.T) {
			app := testApp(t)
			user := newTestUser(t, app, "alice", "password1234", models.RoleMember)
			wantSite := "Fold"
			if setting != "missing" {
				value := ""
				if setting == "configured" {
					value, wantSite = "Grace Church", "Grace Church"
				}
				if err := app.DB.Create(&models.Setting{Key: "site_name", Value: value}).Error; err != nil {
					t.Fatal(err)
				}
			}
			// Keep the one-connection limit local to this test's handle.
			var dbName string
			if err := app.DB.Raw("SELECT current_database()").Scan(&dbName).Error; err != nil {
				t.Fatal(err)
			}
			db, err := testdb.Open(dbName)
			if err != nil {
				t.Fatal(err)
			}
			sqlDB, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = sqlDB.Close() })
			sqlDB.SetMaxOpenConns(1)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			// Bound even a regression that reads outside the transaction and
			// waits for its only connection while preparation holds it.
			app.DB = db.WithContext(ctx)
			var sent []mail.Message
			app.Mailer = directMailSenderFunc(func(_ context.Context, msg mail.Message) error {
				sent = append(sent, msg)
				return nil
			})
			// The real handler must prepare and commit with only one pooled
			// connection, including the site-name read while it holds locks.
			if err := deliverResetMail(ctx, app, user.ID); err != nil {
				t.Fatalf("delivery with one connection: %v", err)
			}
			if len(sent) != 1 || sent[0].To != user.Email || !strings.Contains(sent[0].Text, "at "+wantSite+".") {
				t.Fatal("delivery did not preserve the recipient and site name")
			}
			match := tokenRe.FindStringSubmatch(sent[0].Text)
			if len(match) != 2 {
				t.Fatal("delivery has no reset link")
			}
			var token models.AccountToken
			if err := app.DB.Where("user_id = ? AND kind = ? AND token_hash = ? AND used_at IS NULL", user.ID, models.TokenPasswordReset, hashToken(match[1])).First(&token).Error; err != nil {
				t.Fatalf("delivery did not commit its reset link: %v", err)
			}
		})
	}
}

func TestPasswordResetSiteNameReadRespectsCancellation(t *testing.T) {
	app := testApp(t)
	user := newTestUser(t, app, "alice", "password1234", models.RoleMember)
	old, err := createAccountToken(app.DB, models.TokenPasswordReset, user.ID, nil, tokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	readSite := false
	const name = "test:cancel_direct_mail_site_read"
	if err := app.DB.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "settings" {
			readSite = true
			// Cancel after token rotation, immediately before the settings
			// query. The query must use the job context and abort preparation.
			cancel()
			if !errors.Is(tx.Statement.Context.Err(), context.Canceled) {
				t.Error("site-name read lost the job context")
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.DB.Callback().Query().Remove(name) })
	calls := 0
	app.Mailer = directMailSenderFunc(func(context.Context, mail.Message) error {
		calls++
		return nil
	})
	if err := deliverResetMail(ctx, app, user.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled preparation must retry: %v", err)
	}
	if !readSite || calls != 0 {
		t.Fatalf("site-name read=%v sends=%d", readSite, calls)
	}
	var tokens []models.AccountToken
	if err := app.DB.Where("user_id = ? AND kind = ?", user.ID, models.TokenPasswordReset).Find(&tokens).Error; err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 || tokens[0].TokenHash != hashToken(old) || tokens[0].UsedAt != nil {
		t.Fatal("canceled preparation did not restore the previous reset link")
	}
}

func TestPasswordResetPreparationFailureRollsBackAndRetries(t *testing.T) {
	for _, failure := range []string{"user read", "token insert", "site name"} {
		t.Run(failure, func(t *testing.T) {
			app := testApp(t)
			user := newTestUser(t, app, "alice", "password1234", models.RoleMember)
			old, err := createAccountToken(app.DB, models.TokenPasswordReset, user.ID, nil, tokenTTL)
			if err != nil {
				t.Fatal(err)
			}
			job := queueResetMail(t, app, user)
			const name = "test:direct_mail_preparation_failure"
			// A real SQL error aborts the transaction, including the deletion of
			// the earlier token when the replacement insert fails.
			fail := func(tx *gorm.DB) {
				if (failure == "user read" && tx.Statement.Table == "users") || (failure == "token insert" && tx.Statement.Table == "account_tokens") || (failure == "site name" && tx.Statement.Table == "settings") {
					tx.AddError(tx.Session(&gorm.Session{NewDB: true}).Exec("SELECT 1 / 0").Error)
				}
			}
			var remove func()
			if failure != "token insert" {
				if err := app.DB.Callback().Query().Before("gorm:query").Register(name, fail); err != nil {
					t.Fatal(err)
				}
				remove = func() { _ = app.DB.Callback().Query().Remove(name) }
			} else {
				if err := app.DB.Callback().Create().Before("gorm:create").Register(name, fail); err != nil {
					t.Fatal(err)
				}
				remove = func() { _ = app.DB.Callback().Create().Remove(name) }
			}
			t.Cleanup(remove)
			runResetMail(t, app)
			remove()
			got := reloadJob(t, app, job.ID)
			if got.Status != models.JobFailed || got.Attempts != 1 || got.LastError == "" || mailCount(t, app, "") != 0 {
				t.Fatalf("preparation did not retry without sending: %+v", got)
			}
			var token models.AccountToken
			if err := app.DB.Where("user_id = ? AND token_hash = ? AND used_at IS NULL", user.ID, hashToken(old)).First(&token).Error; err != nil {
				t.Fatalf("failed preparation revoked the existing token: %v", err)
			}
			if err := app.DB.Model(&models.Job{}).Where("id = ?", job.ID).Update("run_at", time.Now().Add(-time.Second)).Error; err != nil {
				t.Fatal(err)
			}
			runResetMail(t, app)
			if got := reloadJob(t, app, job.ID); got.Status != models.JobDone || got.Attempts != 2 || mailCount(t, app, user.Email) != 1 {
				t.Fatalf("retry did not recover: %+v", got)
			}
		})
	}
}

func TestPasswordResetRetryAndMultipleRequestsKeepOnlyLatestLink(t *testing.T) {
	app := testApp(t)
	user := newTestUser(t, app, "alice", "password1234", models.RoleMember)
	job := queueResetMail(t, app, user)
	var sent []string
	app.Mailer = directMailSenderFunc(func(_ context.Context, msg mail.Message) error {
		match := tokenRe.FindStringSubmatch(msg.Text)
		if len(match) != 2 {
			return errors.New("missing reset link")
		}
		sent = append(sent, match[1])
		if len(sent) == 1 {
			return errors.New("451 try later")
		}
		return nil
	})
	runResetMail(t, app)
	if got := reloadJob(t, app, job.ID); got.Status != models.JobFailed || !got.RunAt.After(time.Now()) {
		t.Fatalf("transient send did not back off: %+v", got)
	}
	if err := app.DB.Model(&models.Job{}).Where("id = ?", job.ID).Update("run_at", time.Now().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	runResetMail(t, app)
	if got := reloadJob(t, app, job.ID); got.Status != models.JobDone || got.Attempts != 2 {
		t.Fatalf("retry=%+v", got)
	}
	second := queueResetMail(t, app, user)
	if second.ID == job.ID || second.UniqueKey != nil || len(resetMailJobs(t, app)) != 2 {
		t.Fatal("accepted requests must remain distinct delivery duties")
	}
	runResetMail(t, app)
	if len(sent) != 3 || sent[0] == sent[1] || sent[1] == sent[2] || sent[0] == sent[2] {
		t.Fatalf("attempts did not mint fresh links: %d", len(sent))
	}
	var tokens []models.AccountToken
	if err := app.DB.Where("user_id = ? AND kind = ? AND used_at IS NULL", user.ID, models.TokenPasswordReset).Find(&tokens).Error; err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 || tokens[0].TokenHash != hashToken(sent[2]) || time.Until(tokens[0].ExpiresAt) < tokenTTL-time.Minute {
		t.Fatal("only the newest link should be live for 24 hours from preparation")
	}
	c := newAPIClient(t, app)
	c.fetchCSRF()
	for _, raw := range sent[:2] {
		apiDo(t, c, "POST", "/api/v1/auth/password-reset/confirm", map[string]string{"token": raw, "password": "replacement1234"}, http.StatusBadRequest)
	}
	apiDo(t, c, "POST", "/api/v1/auth/password-reset/confirm", map[string]string{"token": sent[2], "password": "replacement1234"}, http.StatusOK)
	apiDo(t, c, "POST", "/api/v1/auth/password-reset/confirm", map[string]string{"token": sent[2], "password": "replacement5678"}, http.StatusBadRequest)
}

func TestDirectMailOutcomes(t *testing.T) {
	for _, outcome := range []string{"sent", "nil", "unconfigured", "permanent", "hard bounce", "complaint", "transient"} {
		t.Run(outcome, func(t *testing.T) {
			app := testApp(t)
			user := newTestUser(t, app, "alice", "password1234", models.RoleMember)
			job := queueResetMail(t, app, user)
			calls := 0
			sender := directMailSenderFunc(func(_ context.Context, msg mail.Message) error {
				calls++
				if msg.Optional || msg.Stream != "" {
					t.Error("direct account mail must remain required")
				}
				switch outcome {
				case "permanent":
					return &providers.SendError{Permanent: true, Message: "550 refused"}
				case "transient":
					return errors.New("451 busy")
				}
				return nil
			})
			app.Mailer = sender
			switch outcome {
			case "nil":
				app.Mailer = nil
			case "unconfigured":
				app.Mailer = mail.NoSender{}
			case "hard bounce", "complaint":
				reason := models.SuppressHardBounce
				if outcome == "complaint" {
					reason = models.SuppressComplaint
				}
				if err := app.DB.Create(&models.Suppression{Address: user.Email, Channel: "email", Reason: reason}).Error; err != nil {
					t.Fatal(err)
				}
				app.Mailer = mail.WithSuppressions(app.DB, sender)
			}
			runResetMail(t, app)
			want, wantCalls := models.JobDone, 1
			if outcome == "transient" {
				want = models.JobFailed
			}
			if outcome == "nil" || outcome == "unconfigured" || outcome == "hard bounce" {
				wantCalls = 0
			}
			if got := reloadJob(t, app, job.ID); got.Status != want || calls != wantCalls {
				t.Fatalf("outcome=%+v sends=%d, want status=%s sends=%d", got, calls, want, wantCalls)
			}
		})
	}
}

func TestPasswordResetSendDeadlineCancellationAndCommittedPreparation(t *testing.T) {
	for _, earlierCancel := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%v", earlierCancel), func(t *testing.T) {
			app := testApp(t)
			user := newTestUser(t, app, "alice", "password1234", models.RoleMember)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if earlierCancel {
				ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
			}
			calls := 0
			app.Mailer = directMailSenderFunc(func(sendCtx context.Context, msg mail.Message) error {
				calls++
				deadline, ok := sendCtx.Deadline()
				if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > deliverSendTimeout {
					return errors.New("send lacks the shared bounded deadline")
				}
				if earlierCancel {
					parentDeadline, _ := ctx.Deadline()
					if deadline.After(parentDeadline) {
						return errors.New("send extended the job deadline")
					}
				}
				// A different session can take both preparation locks and read
				// the committed hash while provider I/O is in progress.
				if err := app.DB.Transaction(func(tx *gorm.DB) error {
					var got bool
					if err := tx.Raw("SELECT pg_try_advisory_xact_lock(?)", contactLockID).Scan(&got).Error; err != nil {
						return err
					}
					if !got {
						return errors.New("provider I/O holds the contact gate")
					}
					if err := tx.Exec("SELECT id FROM users WHERE id = ? FOR NO KEY UPDATE NOWAIT", user.ID).Error; err != nil {
						return err
					}
					return tx.Where("token_hash = ? AND used_at IS NULL", hashToken(tokenRe.FindStringSubmatch(msg.Text)[1])).First(&models.AccountToken{}).Error
				}); err != nil {
					return err
				}
				if earlierCancel {
					cancel()
					<-sendCtx.Done()
					return sendCtx.Err()
				}
				return nil
			})
			err := deliverResetMail(ctx, app, user.ID)
			if earlierCancel && !errors.Is(err, context.Canceled) {
				t.Fatalf("job cancellation must retry: %v", err)
			}
			if !earlierCancel && err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("sends=%d", calls)
			}
		})
	}
}

func TestDirectMailDeadJobPrivacyAndDecisions(t *testing.T) {
	for _, decision := range []string{"retry", "dismiss"} {
		t.Run(decision, func(t *testing.T) {
			app := testApp(t)
			adminUser := newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
			user := newTestUser(t, app, "alice", "password1234", models.RoleMember)
			admin := loginAPI(t, app, "admin", "password1234")
			admin.holdNotices = true
			job := queueResetMail(t, app, user)
			var logs bytes.Buffer
			prev := log.Writer()
			log.SetOutput(&logs)
			t.Cleanup(func() { log.SetOutput(prev) })
			var raw string
			var capabilities []string
			app.Mailer = directMailSenderFunc(func(_ context.Context, msg mail.Message) error {
				raw = tokenRe.FindStringSubmatch(msg.Text)[1]
				capabilities = append(capabilities, raw)
				return fmt.Errorf("451 rejected %s at https://mailer.example/private-path?token=%s", msg.To, raw)
			})
			// Spend all eight attempts through the real runner, moving only the
			// due time so backoff doesn't make this regression test take an hour.
			for range directMailAttempts {
				runResetMail(t, app)
				if err := app.DB.Model(&models.Job{}).Where("id = ?", job.ID).Update("run_at", time.Now().Add(-time.Second)).Error; err != nil {
					t.Fatal(err)
				}
			}
			got := reloadJob(t, app, job.ID)
			if got.Status != models.JobDead || got.Attempts != directMailAttempts {
				t.Fatalf("exhaustion=%+v", got)
			}
			alerts := deadAlerts(t, app, job.ID)
			if len(alerts) != 1 || alerts[0].UserID != adminUser.ID || !alerts[0].Waiting || !strings.Contains(alerts[0].Title, "Sending a password reset link") {
				t.Fatalf("dead reset must alert the admin with its label: %+v", alerts)
			}
			system := apiDo(t, admin, "GET", "/api/v1/admin/jobs", nil, http.StatusOK)
			for _, output := range []string{logs.String(), got.LastError, got.Payload, system, alerts[0].Title + alerts[0].Body} {
				for _, secret := range append([]string{user.Email, "private-path", "?token="}, capabilities...) {
					if strings.Contains(output, secret) {
						t.Fatalf("direct mail diagnostics leaked %q", secret)
					}
				}
			}
			apiDo(t, admin, "POST", "/api/v1/admin/jobs/"+itoa(job.ID)+"/"+decision, nil, http.StatusOK)
			alerts = deadAlerts(t, app, job.ID)
			resolution, action, status := "retried", audit.JobRetried, models.JobQueued
			if decision == "dismiss" {
				resolution, action, status = "dismissed", audit.JobDismissed, models.JobDismissed
			}
			if alerts[0].ResolvedAt == nil || alerts[0].Resolution != resolution || auditCount(t, app, action) != 1 || reloadJob(t, app, job.ID).Status != status {
				t.Fatal("admin decision did not resolve and audit the dead-mail alert")
			}
			if decision == "retry" {
				app.Mailer = mail.WithSuppressions(app.DB, mail.NewDevOutbox(app.DB, "Fold <noreply@example.com>"))
				runResetMail(t, app)
				if got := reloadJob(t, app, job.ID); got.Status != models.JobDone || got.Attempts != 1 || mailCount(t, app, user.Email) != 1 {
					t.Fatalf("admin retry did not deliver: %+v", got)
				}
				if fresh := tokenInEmail(t, latestEmail(t, app, user.Email)); fresh == raw {
					t.Fatal("admin retry reused the failed capability")
				}
			}
		})
	}
}

func mailJobsOfKind(t *testing.T, app *App, kind string) []models.Job {
	t.Helper()
	var rows []models.Job
	if err := app.DB.Where("kind = ?", kind).Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

// payloadEquals compares a stored jsonb payload with want, ignoring the key
// order and spacing Postgres normalizes.
func payloadEquals(job models.Job, want any) bool {
	var got, exp any
	if json.Unmarshal([]byte(job.Payload), &got) != nil || json.Unmarshal([]byte(jsonString(want)), &exp) != nil {
		return false
	}
	return reflect.DeepEqual(got, exp)
}

// deliverMailJob runs the registered production handler for one payload
// without claiming a job, as deliverResetMail does.
func deliverMailJob(ctx context.Context, app *App, kind string, payload any) error {
	for _, k := range app.mailKinds() {
		if k.Name == kind {
			return k.Run(ctx, json.RawMessage(jsonString(payload)))
		}
	}
	return fmt.Errorf("%s is not registered", kind)
}

// decidedMailJob finds the job one decision queued, by its exact payload.
func decidedMailJob(t *testing.T, app *App, requestID uint, mailCase string) models.Job {
	t.Helper()
	for _, job := range mailJobsOfKind(t, app, kindAccountRequestMail) {
		if payloadEquals(job, requestMailJob{RequestID: requestID, Case: mailCase}) {
			return job
		}
	}
	t.Fatalf("no %s mail job for request %d", mailCase, requestID)
	return models.Job{}
}

func accountTokens(t *testing.T, app *App, userID uint, kind string) []models.AccountToken {
	t.Helper()
	var rows []models.AccountToken
	if err := app.DB.Where("user_id = ? AND kind = ?", userID, kind).Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

// approveHeldSignup submits a signup, retires its received mail, then
// approves it as admin and leaves the approval mail queued, so a test can
// change the world before delivery and see only that mail reach the person.
// The admin client holds notices.
func approveHeldSignup(t *testing.T, app *App, admin *apiClient, name, email, band, birth string, approval map[string]any) (models.AccountRequest, models.User) {
	t.Helper()
	admin.holdNotices = true
	anon := anonClient(t, app, "10.7.0.1")
	anon.holdNotices = true
	apiDo(t, anon, "POST", "/api/v1/account-requests", signupBody(name, email, band, birth), http.StatusOK)
	if err := app.DB.Where("kind = ?", kindAccountRequestMail).Delete(&models.Job{}).Error; err != nil {
		t.Fatal(err)
	}
	req := pendingRequest(t, app, email)
	if approval == nil {
		approval = map[string]any{}
	}
	apiDo(t, admin, "POST", "/api/v1/admin/account-requests/"+itoa(req.ID)+"/approve", approval, http.StatusOK)
	var user models.User
	if err := app.DB.Where("email = ?", email).First(&user).Error; err != nil {
		t.Fatal(err)
	}
	return req, user
}

func TestApprovedSetupMailMintsItsLinkAtDelivery(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "pw")
	req, user := approveHeldSignup(t, app, admin, "New Person", "new@example.com", "adult", "", nil)
	job := decidedMailJob(t, app, req.ID, requestMailApproved)
	if len(accountTokens(t, app, user.ID, models.TokenAccountSetup)) != 0 || mailCount(t, app, user.Email) != 0 {
		t.Fatal("approval minted a link or mailed before delivery")
	}
	// Ids and the fixed case only: nothing a person typed or a capability.
	for _, secret := range []string{user.Email, user.Name, "New Person"} {
		if strings.Contains(job.Payload, secret) {
			t.Fatalf("payload %s carries %q", job.Payload, secret)
		}
	}
	runResetMail(t, app)
	if got := reloadJob(t, app, job.ID); got.Status != models.JobDone {
		t.Fatalf("job=%+v", got)
	}
	token := tokenInEmail(t, latestEmail(t, app, user.Email))
	anon := anonClient(t, app, "10.7.0.2")
	apiDo(t, anon, "POST", "/api/v1/account-requests/confirm", map[string]string{"token": token, "password": "longenoughpassword"}, http.StatusOK)
}

func TestApprovedSetupMailConsentSentenceFollowsAccountState(t *testing.T) {
	const sentence = "also needs a parent or guardian's consent"
	for _, tt := range []struct {
		name         string
		approval     map[string]any
		consentFirst bool
		want         bool
	}{
		{name: "waiting for consent", want: true},
		{name: "consented at approval", approval: map[string]any{"accountConsentEvidenceText": "Paper form signed today"}},
		{name: "consented before delivery", consentFirst: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			app := testApp(t)
			newTestUser(t, app, "admin", "pw", models.RoleAdmin)
			admin := loginAPI(t, app, "admin", "pw")
			_, user := approveHeldSignup(t, app, admin, "Teen Casey", "casey@example.com", "teen", teenBirthDate(), tt.approval)
			if tt.consentFirst {
				if err := app.DB.Model(&user).Update("status", models.UserActive).Error; err != nil {
					t.Fatal(err)
				}
			}
			message := approvalEmailTo(t, app, user.Email)
			if strings.Contains(message.Body, sentence) != tt.want {
				t.Fatalf("consent sentence present=%v, want %v:\n%s", !tt.want, tt.want, message.Body)
			}
			anon := anonClient(t, app, "10.7.0.2")
			apiDo(t, anon, "POST", "/api/v1/account-requests/confirm", map[string]string{"token": tokenInEmail(t, message), "password": "longenoughpassword"}, http.StatusOK)
		})
	}
}

func TestApprovedSetupMailSkipsAccountsThatCannotUseIt(t *testing.T) {
	for _, change := range []string{"disabled", "deleted", "guest", "setup finished"} {
		t.Run(change, func(t *testing.T) {
			app := testApp(t)
			newTestUser(t, app, "admin", "pw", models.RoleAdmin)
			admin := loginAPI(t, app, "admin", "pw")
			req, user := approveHeldSignup(t, app, admin, "New Person", "new@example.com", "adult", "", nil)
			job := decidedMailJob(t, app, req.ID, requestMailApproved)
			switch change {
			case "deleted":
				if err := app.DB.Delete(&user).Error; err != nil {
					t.Fatal(err)
				}
			case "setup finished":
				hash := "x"
				if err := app.DB.Model(&user).Updates(map[string]any{"password_hash": hash, "status": models.UserActive}).Error; err != nil {
					t.Fatal(err)
				}
			default:
				if err := app.DB.Model(&user).Update("status", change).Error; err != nil {
					t.Fatal(err)
				}
			}
			runResetMail(t, app)
			if got := reloadJob(t, app, job.ID); got.Status != models.JobDone {
				t.Fatalf("job=%+v", got)
			}
			if mailCount(t, app, user.Email) != 0 || len(accountTokens(t, app, user.ID, models.TokenAccountSetup)) != 0 {
				t.Fatal("ineligible account got a setup link")
			}
		})
	}
}

func TestApprovedSetupMailRetryReplacesItsLink(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "pw")
	req, user := approveHeldSignup(t, app, admin, "New Person", "new@example.com", "adult", "", nil)
	job := decidedMailJob(t, app, req.ID, requestMailApproved)
	var sent []string
	app.Mailer = directMailSenderFunc(func(_ context.Context, msg mail.Message) error {
		sent = append(sent, tokenRe.FindStringSubmatch(msg.Text)[1])
		if len(sent) == 1 {
			return errors.New("451 try later")
		}
		return nil
	})
	runResetMail(t, app)
	if got := reloadJob(t, app, job.ID); got.Status != models.JobFailed {
		t.Fatalf("transient send did not back off: %+v", got)
	}
	if err := app.DB.Model(&models.Job{}).Where("id = ?", job.ID).Update("run_at", time.Now().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	runResetMail(t, app)
	if got := reloadJob(t, app, job.ID); got.Status != models.JobDone || len(sent) != 2 || sent[0] == sent[1] {
		t.Fatalf("retry=%+v sent=%d", got, len(sent))
	}
	if live := accountTokens(t, app, user.ID, models.TokenAccountSetup); len(live) != 1 || live[0].TokenHash != hashToken(sent[1]) {
		t.Fatal("only the latest setup link should be live")
	}
	anon := anonClient(t, app, "10.7.0.2")
	apiDo(t, anon, "POST", "/api/v1/account-requests/confirm", map[string]string{"token": sent[0], "password": "longenoughpassword"}, http.StatusBadRequest)
	apiDo(t, anon, "POST", "/api/v1/account-requests/confirm", map[string]string{"token": sent[1], "password": "longenoughpassword"}, http.StatusOK)
}

func TestApprovedSetupMailAndRedemptionLockOrders(t *testing.T) {
	for _, first := range []string{"delivery", "redemption"} {
		t.Run(first+" first", func(t *testing.T) {
			app := testApp(t)
			newTestUser(t, app, "admin", "pw", models.RoleAdmin)
			admin := loginAPI(t, app, "admin", "pw")
			req, user := approveHeldSignup(t, app, admin, "New Person", "new@example.com", "adult", "", nil)
			// The race runs the handler directly, so retire the queued job that
			// would otherwise deliver again while the test reads the outbox.
			if err := app.DB.Where("kind = ?", kindAccountRequestMail).Delete(&models.Job{}).Error; err != nil {
				t.Fatal(err)
			}
			old, err := createAccountToken(app.DB, models.TokenAccountSetup, user.ID, nil, tokenTTL)
			if err != nil {
				t.Fatal(err)
			}
			expected := http.StatusBadRequest
			if first == "redemption" {
				expected = http.StatusOK
			}
			work := func(operation string) func(*gorm.DB) error {
				return func(db *gorm.DB) error {
					a := &App{DB: db, Store: app.Store, Mailer: app.Mailer, Config: app.Config}
					if operation == "delivery" {
						return deliverMailJob(db.Statement.Context, a, kindAccountRequestMail, requestMailJob{RequestID: req.ID, Case: requestMailApproved})
					}
					w := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(w)
					c.Request = httptest.NewRequest("POST", "/api/v1/account-requests/confirm",
						strings.NewReader(jsonString(map[string]string{"token": old, "password": "replacement1234"}))).WithContext(db.Statement.Context)
					c.Request.Header.Set("Content-Type", "application/json")
					a.apiConfirmAccountSetup(c)
					if w.Code != expected {
						return fmt.Errorf("redemption: got %d %s, want %d", w.Code, w.Body.String(), expected)
					}
					return nil
				}
			}
			second := "redemption"
			if first == "redemption" {
				second = "delivery"
			}
			reached, release := pauseDBRead(t, app.DB, first, "users")
			winner, winnerDone := startDBSession(t, app.DB, first, work(first))
			awaitDBSignal(t, reached) // winner holds the contact gate and the user lock
			loser, loserDone := startDBSession(t, app.DB, second, work(second))
			waitDBBlocked(t, app.DB, loser, winner)
			release()
			if err := awaitDBResult(t, winnerDone); err != nil {
				t.Fatal(err)
			}
			if err := awaitDBResult(t, loserDone); err != nil {
				t.Fatal(err)
			}
			var fresh models.User
			if err := app.DB.First(&fresh, user.ID).Error; err != nil {
				t.Fatal(err)
			}
			rows := accountTokens(t, app, user.ID, models.TokenAccountSetup)
			if first == "redemption" {
				// Completed setup made the waiting delivery a no-op: no mail, no
				// new link, the consumed token stays consumed.
				if fresh.PasswordHash == nil || !fresh.CheckPassword("replacement1234") || mailCount(t, app, user.Email) != 0 ||
					len(rows) != 1 || rows[0].TokenHash != hashToken(old) || rows[0].UsedAt == nil {
					t.Fatalf("redemption first: user=%+v tokens=%+v", fresh, rows)
				}
				return
			}
			// Rotation first revoked the old link without setting a password.
			if fresh.PasswordHash != nil || len(rows) != 1 || rows[0].TokenHash == hashToken(old) {
				t.Fatalf("delivery first: user=%+v tokens=%+v", fresh, rows)
			}
			newRaw := tokenInEmail(t, latestEmail(t, app, user.Email))
			anon := anonClient(t, app, "10.7.0.2")
			apiDo(t, anon, "POST", "/api/v1/account-requests/confirm", map[string]string{"token": old, "password": "replacement1234"}, http.StatusBadRequest)
			apiDo(t, anon, "POST", "/api/v1/account-requests/confirm", map[string]string{"token": newRaw, "password": "replacement1234"}, http.StatusOK)
		})
	}
}

// Both doors queue an existing-account hint by user id; the decision path
// queues the same kind of mail under request_mail case exists. All three
// address the account's current email, still reach disabled accounts, and
// send nothing once the account is gone or has become a guest.
func TestExistingAccountMailRechecksTheAccount(t *testing.T) {
	for _, door := range []string{"signup", "guest", "decision"} {
		for _, change := range []string{"none", "disabled", "email", "deleted", "guest"} {
			t.Run(door+"/"+change, func(t *testing.T) {
				app := testApp(t)
				newTestUser(t, app, "admin", "pw", models.RoleAdmin)
				admin := loginAPI(t, app, "admin", "pw")
				admin.holdNotices = true
				member := newTestUser(t, app, "member", "pw", models.RoleMember)
				var job models.Job
				switch door {
				case "signup":
					anon := anonClient(t, app, "10.7.1.1")
					anon.holdNotices = true
					apiDo(t, anon, "POST", "/api/v1/account-requests", signupBody("Someone", member.Email, "adult", ""), http.StatusOK)
					hints := mailJobsOfKind(t, app, kindAccountExistsMail)
					if len(hints) != 1 || !payloadEquals(hints[0], userMailJob{UserID: member.ID}) {
						t.Fatalf("hint jobs: %+v", hints)
					}
					job = hints[0]
				case "guest":
					gid := createGroupAs(t, admin, "Hint door", models.VisibilityPublic)
					ev := scheduleGroupEvent(t, admin, gid, "Picnic")
					anon := anonClient(t, app, "10.7.1.2")
					anon.holdNotices = true
					guestSubmit(t, anon, ev.ID, guestBody("Someone", member.Email, true), http.StatusOK)
					hints := mailJobsOfKind(t, app, kindAccountExistsMail)
					if len(hints) != 1 || !payloadEquals(hints[0], userMailJob{UserID: member.ID}) {
						t.Fatalf("hint jobs: %+v", hints)
					}
					job = hints[0]
				default:
					// The request was made before the account existed; the
					// account appears, then an admin approves.
					anon := anonClient(t, app, "10.7.1.3")
					anon.holdNotices = true
					apiDo(t, anon, "POST", "/api/v1/account-requests", signupBody("Late", "late@example.com", "adult", ""), http.StatusOK)
					if err := app.DB.Where("kind = ?", kindAccountRequestMail).Delete(&models.Job{}).Error; err != nil {
						t.Fatal(err)
					}
					req := pendingRequest(t, app, "late@example.com")
					member = newTestUser(t, app, "late", "pw", models.RoleMember)
					apiDo(t, admin, "POST", "/api/v1/admin/account-requests/"+itoa(req.ID)+"/approve", map[string]any{}, http.StatusOK)
					job = decidedMailJob(t, app, req.ID, requestMailExists)
				}
				if mailCount(t, app, "") != 0 && door != "decision" {
					t.Fatal("mail was sent before delivery")
				}
				original, to := member.Email, member.Email
				switch change {
				case "disabled", "guest":
					if err := app.DB.Model(member).Update("status", change).Error; err != nil {
						t.Fatal(err)
					}
				case "email":
					to = "moved@example.com"
					if err := app.DB.Model(member).Update("email", to).Error; err != nil {
						t.Fatal(err)
					}
				case "deleted":
					if err := app.DB.Delete(member).Error; err != nil {
						t.Fatal(err)
					}
				}
				before := mailCount(t, app, "")
				runResetMail(t, app)
				if got := reloadJob(t, app, job.ID); got.Status != models.JobDone {
					t.Fatalf("job=%+v", got)
				}
				want := int64(1)
				if change == "deleted" || change == "guest" {
					want = 0
				}
				if mailCount(t, app, to) != want || mailCount(t, app, "") != before+want {
					t.Fatalf("mail to %s = %d, want %d (total %d, was %d)", to, mailCount(t, app, to), want, mailCount(t, app, ""), before)
				}
				if change == "email" && mailCount(t, app, original) != 0 {
					t.Fatal("hint went to the old address")
				}
			})
		}
	}
}

// The case decides which stored facts must still hold at delivery.
func TestAccountRequestMailCasesRenderFromStoredFacts(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "pw")
	admin.holdNotices = true
	anon := anonClient(t, app, "10.7.2.1")
	anon.holdNotices = true
	apiDo(t, anon, "POST", "/api/v1/account-requests", signupBody("Declined", "declined@example.com", "adult", ""), http.StatusOK)
	apiDo(t, anon, "POST", "/api/v1/account-requests", signupBody("Quick", "quick@example.com", "adult", ""), http.StatusOK)
	declined, quick := pendingRequest(t, app, "declined@example.com"), pendingRequest(t, app, "quick@example.com")
	// Decided before the received mail is delivered: it is no longer true.
	apiDo(t, admin, "POST", "/api/v1/admin/account-requests/"+itoa(quick.ID)+"/reject", map[string]any{"note": "Not this time"}, http.StatusOK)
	apiDo(t, admin, "POST", "/api/v1/admin/account-requests/"+itoa(declined.ID)+"/reject", map[string]any{"note": "Please call the office"}, http.StatusOK)
	drainNotices(t, app)
	if got := mailCount(t, app, quick.Email); got != 1 {
		t.Fatalf("a request rejected before its received mail went out got %d mails, want only the rejection", got)
	}
	message := latestEmail(t, app, quick.Email)
	if !strings.Contains(message.Subject, "About your") || !strings.Contains(message.Body, "Note from the church office: Not this time") {
		t.Fatalf("rejection mail: %q %q", message.Subject, message.Body)
	}
	// A case that no longer matches the request finishes without mail, and an
	// unknown case is an error the runner retries and then reports.
	if err := deliverMailJob(context.Background(), app, kindAccountRequestMail, requestMailJob{RequestID: declined.ID, Case: requestMailApproved}); err != nil {
		t.Fatal(err)
	}
	if err := deliverMailJob(context.Background(), app, kindAccountRequestMail, requestMailJob{RequestID: 999999, Case: requestMailReceived}); err != nil {
		t.Fatalf("missing request must finish quietly: %v", err)
	}
	if err := deliverMailJob(context.Background(), app, kindAccountRequestMail, requestMailJob{RequestID: declined.ID, Case: "bogus"}); err == nil {
		t.Fatal("unknown case was accepted")
	}
	if got := mailCount(t, app, declined.Email); got != 1 {
		t.Fatalf("mismatched cases sent mail: %d", got)
	}
}

// heldInvitationMail creates through the real API but leaves its delivery duty
// queued, so each test can change authority before the worker reads it.
type heldInvitationMail struct {
	inv     models.Invitation
	job     models.Job
	inviter *models.User
	group   *models.Group
	person  householdPersonJSON
	client  *apiClient
	admin   *apiClient
}

func queueHeldInvitationMail(t *testing.T, app *App, conversion, bound bool) heldInvitationMail {
	t.Helper()
	adminUser := newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	admin.holdNotices = true
	f := heldInvitationMail{admin: admin}
	if conversion {
		f.inviter, f.client, f.person, _ = householdFamily(t, app, admin, "parent")
		f.client.holdNotices = true
		f.group = newTestGroup(t, app, adminUser, "Conversion dates", models.GroupActive)
		apiDo(t, f.client, "POST", conversionPath(f.person.ID), map[string]any{"email": "invited@example.com"}, http.StatusCreated)
	} else {
		f.inviter = newTestUser(t, app, "leader", "password1234", models.RoleMember)
		f.group = newTestGroup(t, app, f.inviter, "Invitation group", models.GroupActive)
		f.client = loginAPI(t, app, "leader", "password1234")
		f.client.holdNotices = true
		body := map[string]any{"email": "invited@example.com", "name": "Invited Person"}
		if bound {
			body["groupId"] = f.group.ID
		}
		apiDo(t, f.client, "POST", "/api/v1/invitations", body, http.StatusCreated)
	}
	if err := app.DB.Where("email = ?", "invited@example.com").First(&f.inv).Error; err != nil {
		t.Fatal(err)
	}
	rows := mailJobsOfKind(t, app, kindInvitationMail)
	if len(rows) != 1 || !payloadEquals(rows[0], invitationMailJob{InvitationID: f.inv.ID}) || rows[0].MaxAttempts != directMailAttempts || rows[0].UniqueKey != nil || mailCount(t, app, f.inv.Email) != 0 {
		t.Fatalf("creation must queue only an invitation ID, without sending: %+v", rows)
	}
	f.job = rows[0]
	return f
}

func invitationIn(t *testing.T, app *App, id uint) models.Invitation {
	t.Helper()
	var inv models.Invitation
	if err := app.DB.First(&inv, id).Error; err != nil {
		t.Fatal(err)
	}
	return inv
}

func TestInvitationMailRetryPreservesDeadline(t *testing.T) {
	for _, conversion := range []bool{false, true} {
		t.Run(fmt.Sprintf("conversion=%v", conversion), func(t *testing.T) {
			app := testApp(t)
			f := queueHeldInvitationMail(t, app, conversion, true)
			var sent []mail.Message
			app.Mailer = directMailSenderFunc(func(_ context.Context, msg mail.Message) error {
				sent = append(sent, msg)
				// Preparation must commit and release the invitation lock before I/O.
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := app.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
					return tx.Exec("SELECT id FROM invitations WHERE id = ? FOR UPDATE NOWAIT", f.inv.ID).Error
				}); err != nil {
					return fmt.Errorf("invitation still locked during send: %w", err)
				}
				if len(sent) == 1 {
					return errors.New("451 try again")
				}
				return nil
			})
			runResetMail(t, app)
			failed := reloadJob(t, app, f.job.ID)
			if failed.Status != models.JobFailed || failed.Attempts != 1 || len(sent) != 1 {
				t.Fatalf("transient send did not remain retryable: %+v", failed)
			}
			first := tokenRe.FindStringSubmatch(sent[0].Text)[1]
			inv := invitationIn(t, app, f.inv.ID)
			if inv.TokenHash != hashToken(first) || !inv.ExpiresAt.Equal(f.inv.ExpiresAt) {
				t.Fatal("first preparation did not commit only the new hash")
			}
			if err := app.DB.Model(&models.Job{}).Where("id = ?", f.job.ID).Update("run_at", time.Now().Add(-time.Second)).Error; err != nil {
				t.Fatal(err)
			}
			runResetMail(t, app)
			if got := reloadJob(t, app, f.job.ID); got.Status != models.JobDone || got.Attempts != 2 || len(sent) != 2 {
				t.Fatalf("retry did not finish: %+v", got)
			}
			last := tokenRe.FindStringSubmatch(sent[1].Text)[1]
			inv = invitationIn(t, app, f.inv.ID)
			if last == first || inv.TokenHash != hashToken(last) || !inv.ExpiresAt.Equal(f.inv.ExpiresAt) {
				t.Fatal("retry reused a capability or extended the deadline")
			}
			for _, msg := range sent {
				if !strings.Contains(msg.Text, "expires within 24 hours") || msg.To != f.inv.Email || msg.Optional {
					t.Fatalf("invitation copy/recipient changed: %+v", msg)
				}
			}
			anon := anonClient(t, app, "10.7.3.1")
			body := map[string]string{"ageBand": "adult", "token": first, "name": "Invited Person", "password": "password1234"}
			apiDo(t, anon, "POST", "/api/v1/invitations/accept", body, http.StatusBadRequest)
			body["token"] = last
			apiDo(t, anon, "POST", "/api/v1/invitations/accept", body, http.StatusOK)
			apiDo(t, anon, "POST", "/api/v1/invitations/accept", body, http.StatusBadRequest)
		})
	}
}

func TestInvitationMailSkipsClosedInvitations(t *testing.T) {
	for _, conversion := range []bool{false, true} {
		for _, state := range []string{"accepted", "revoked", "expired", "missing", "disabled inviter", "deleted inviter"} {
			t.Run(fmt.Sprintf("conversion=%v/%s", conversion, state), func(t *testing.T) {
				app := testApp(t)
				f := queueHeldInvitationMail(t, app, conversion, true)
				var err error
				switch state {
				case "accepted":
					err = app.DB.Model(&f.inv).Update("accepted_at", time.Now()).Error
				case "revoked":
					err = app.DB.Model(&f.inv).Update("revoked_at", time.Now()).Error
				case "expired":
					err = app.DB.Model(&f.inv).Update("expires_at", time.Now().Add(-time.Hour)).Error
				case "missing":
					err = app.DB.Delete(&f.inv).Error
				case "disabled inviter":
					err = app.DB.Model(f.inviter).Update("status", models.UserDisabled).Error
				case "deleted inviter":
					err = app.DB.Delete(f.inviter).Error
				}
				if err != nil {
					t.Fatal(err)
				}
				runResetMail(t, app)
				if got := reloadJob(t, app, f.job.ID); got.Status != models.JobDone || mailCount(t, app, f.inv.Email) != 0 {
					t.Fatalf("closed invitation was not a no-op: %+v", got)
				}
				if state != "missing" && invitationIn(t, app, f.inv.ID).TokenHash != f.inv.TokenHash {
					t.Fatal("closed invitation rotated its hash")
				}
			})
		}
	}
}

func TestInvitationMailRechecksOrdinaryAuthority(t *testing.T) {
	for _, change := range []string{"admin demoted", "leadership lost", "membership missing", "only led group archived", "bound group archived", "bound group pending", "bound group deleted", "full group"} {
		t.Run(change, func(t *testing.T) {
			app := testApp(t)
			f := queueHeldInvitationMail(t, app, false, change != "only led group archived")
			var err error
			switch change {
			case "admin demoted":
				// Issue as an admin with no leadership, then demote them.
				var admin models.User
				if err := app.DB.Where("role = ?", models.RoleAdmin).First(&admin).Error; err != nil {
					t.Fatal(err)
				}
				if err := app.DB.Model(&f.inv).Update("invited_by", admin.ID).Error; err != nil {
					t.Fatal(err)
				}
				err = app.DB.Model(&admin).Update("role", models.RoleMember).Error
			case "leadership lost":
				err = app.DB.Model(&models.Membership{}).Where("user_id = ? AND group_id = ?", f.inviter.ID, f.group.ID).Update("role", models.MembershipMember).Error
			case "membership missing":
				err = app.DB.Where("user_id = ? AND group_id = ?", f.inviter.ID, f.group.ID).Delete(&models.Membership{}).Error
			case "only led group archived", "bound group archived":
				err = app.DB.Model(f.group).Update("status", models.GroupArchived).Error
			case "bound group pending":
				err = app.DB.Model(f.group).Update("status", models.GroupPending).Error
			case "bound group deleted":
				err = app.DB.Delete(f.group).Error
			case "full group":
				err = app.DB.Model(f.group).Update("member_limit", 1).Error
			}
			if err != nil {
				t.Fatal(err)
			}
			runResetMail(t, app)
			wantMail := int64(0)
			if change == "full group" {
				wantMail = 1
			}
			if got := reloadJob(t, app, f.job.ID); got.Status != models.JobDone || mailCount(t, app, f.inv.Email) != wantMail {
				t.Fatalf("authority change %s: %+v", change, got)
			}
			if rotated := invitationIn(t, app, f.inv.ID).TokenHash != f.inv.TokenHash; rotated != (wantMail == 1) {
				t.Fatal("authority loss rotated a capability or capacity prevented delivery")
			}
		})
	}
}

func TestInvitationMailRechecksConversionAuthority(t *testing.T) {
	for _, change := range []string{"household authority lost", "person removed", "person converted", "blocked holder", "inviter disabled then reenabled", "person renamed"} {
		t.Run(change, func(t *testing.T) {
			app := testApp(t)
			f := queueHeldInvitationMail(t, app, true, false)
			var err error
			switch change {
			case "household authority lost":
				err = app.DB.Where("user_id = ?", f.inviter.ID).Delete(&models.HouseholdAccount{}).Error
			case "person removed":
				err = app.DB.Delete(&models.HouseholdPerson{}, f.person.ID).Error
			case "person converted":
				converted := newTestUser(t, app, "converted", "password1234", models.RoleMember)
				err = app.DB.Model(&models.HouseholdPerson{}).Where("id = ?", f.person.ID).Update("converted_user_id", converted.ID).Error
			case "blocked holder":
				id, date := seedCappedEvent(t, app, f.admin, itoa(f.group.ID), intPtr(5), true)
				batchAnswer(t, f.client, id, date, map[string]any{"people": []map[string]any{{"personId": f.person.ID, "status": "going"}}}, http.StatusOK)
				// Keep the inviter active and authorized while another responsible
				// holder becomes unavailable, isolating errConversionHolder.
				holder := newTestUser(t, app, "holder", "password1234", models.RoleMember)
				var acct models.HouseholdAccount
				if err := app.DB.Where("user_id = ?", f.inviter.ID).First(&acct).Error; err != nil {
					t.Fatal(err)
				}
				if err := app.DB.Create(&models.HouseholdAccount{HouseholdID: acct.HouseholdID, UserID: holder.ID, Relation: models.HouseholdRelationAdult}).Error; err != nil {
					t.Fatal(err)
				}
				if err := app.DB.Model(&models.Registration{}).Where("household_person_id = ?", f.person.ID).Update("registered_by", holder.ID).Error; err != nil {
					t.Fatal(err)
				}
				err = app.DB.Model(holder).Update("status", models.UserDisabled).Error
			case "inviter disabled then reenabled":
				apiDo(t, f.admin, "PUT", "/api/v1/admin/users/"+itoa(f.inviter.ID), map[string]any{"status": "disabled"}, http.StatusOK)
				apiDo(t, f.admin, "PUT", "/api/v1/admin/users/"+itoa(f.inviter.ID), map[string]any{"status": "active"}, http.StatusOK)
			case "person renamed":
				err = app.DB.Model(&models.HouseholdPerson{}).Where("id = ?", f.person.ID).Update("first_name", "Current Name").Error
			}
			if err != nil {
				t.Fatal(err)
			}
			runResetMail(t, app)
			wantMail := int64(0)
			if change == "person renamed" {
				wantMail = 1
			}
			if got := reloadJob(t, app, f.job.ID); got.Status != models.JobDone || mailCount(t, app, f.inv.Email) != wantMail {
				t.Fatalf("conversion eligibility %s: %+v", change, got)
			}
			if rotated := invitationIn(t, app, f.inv.ID).TokenHash != f.inv.TokenHash; rotated != (wantMail == 1) {
				t.Fatal("ineligible conversion rotated its capability")
			}
			if wantMail == 1 && !strings.Contains(latestEmail(t, app, f.inv.Email).Body, "Current Name") {
				t.Fatal("conversion mailed stale person facts")
			}
		})
	}
}

func TestInvitationMailAuthorityReadFailuresRetry(t *testing.T) {
	for _, failure := range []struct {
		name       string
		conversion bool
		bound      bool
		table      string
		scan       bool
	}{
		{name: "bound membership database error", bound: true, table: "memberships"},
		{name: "bound membership scan error", bound: true, table: "memberships", scan: true},
		{name: "groupless leadership", table: "memberships"},
		{name: "group", bound: true, table: "groups"},
		{name: "conversion person", conversion: true, table: "household_people"},
		{name: "conversion household authority", conversion: true, table: "household_accounts"},
		{name: "conversion guardians", conversion: true, table: "guardian_links"},
		{name: "conversion holders", conversion: true, table: "registrations"},
		{name: "conversion timezone", conversion: true, table: "settings"},
		{name: "conversion disable audit", conversion: true, table: "audit_logs"},
		{name: "site name after rotation", bound: true, table: "settings"},
	} {
		t.Run(failure.name, func(t *testing.T) {
			app := testApp(t)
			f := queueHeldInvitationMail(t, app, failure.conversion, failure.bound)
			name := fmt.Sprintf("test:invitation_authority_failure:%d", queryCounterSeq.Add(1))
			var scanFailedWithoutAbort bool
			if err := app.DB.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
				if tx.Statement.Table != failure.table {
					return
				}
				if failure.scan {
					// The statement is valid PostgreSQL, but its text id cannot
					// scan into Membership.ID. COMMIT would succeed if ignored.
					tx.Statement.Selects = []string{"'not-a-number' AS id"}
					return
				}
				tx.AddError(tx.Session(&gorm.Session{NewDB: true}).Exec("SELECT 1 / 0").Error)
			}); err != nil {
				t.Fatal(err)
			}
			after := name + ":after"
			if err := app.DB.Callback().Query().After("gorm:query").Register(after, func(tx *gorm.DB) {
				if failure.scan && tx.Statement.Table == failure.table && tx.Error != nil {
					_, err := tx.Statement.ConnPool.ExecContext(tx.Statement.Context, "SELECT 1")
					scanFailedWithoutAbort = err == nil
				}
			}); err != nil {
				t.Fatal(err)
			}
			remove := func() {
				_ = app.DB.Callback().Query().Remove(name)
				_ = app.DB.Callback().Query().Remove(after)
			}
			t.Cleanup(remove)
			if err := deliverMailJob(context.Background(), app, kindInvitationMail, invitationMailJob{InvitationID: f.inv.ID}); err == nil {
				t.Fatal("authority read error was swallowed by the handler")
			}
			runResetMail(t, app)
			remove()
			got := reloadJob(t, app, f.job.ID)
			if got.Status != models.JobFailed || got.Attempts != 1 || got.LastError == "" ||
				invitationIn(t, app, f.inv.ID).TokenHash != f.inv.TokenHash || mailCount(t, app, f.inv.Email) != 0 {
				t.Fatalf("read failure did not preserve a retryable unchanged invitation: %+v", got)
			}
			if failure.scan && !scanFailedWithoutAbort {
				t.Fatal("scan failure must leave PostgreSQL usable; retry cannot depend on COMMIT detecting an abort")
			}
			if err := app.DB.Model(&models.Job{}).Where("id = ?", f.job.ID).Update("run_at", time.Now().Add(-time.Second)).Error; err != nil {
				t.Fatal(err)
			}
			runResetMail(t, app)
			if got := reloadJob(t, app, f.job.ID); got.Status != models.JobDone || got.Attempts != 2 || mailCount(t, app, f.inv.Email) != 1 {
				t.Fatalf("healed authority read did not deliver: %+v", got)
			}
		})
	}
}

func TestInvitationMailEnqueueFailureRollsBackCreationAndResend(t *testing.T) {
	for _, path := range []string{"ordinary", "conversion create", "conversion resend", "conversion replace expired"} {
		t.Run(path, func(t *testing.T) {
			app := testApp(t)
			newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
			admin := loginAPI(t, app, "admin", "password1234")
			admin.holdNotices = true
			client, target := admin, "/api/v1/invitations"
			var before models.Invitation
			if path != "ordinary" {
				_, c, person, _ := householdFamily(t, app, admin, "parent")
				client, target = c, conversionPath(person.ID)
				client.holdNotices = true
				if path != "conversion create" {
					apiDo(t, client, "POST", target, map[string]any{"email": "invited@example.com"}, http.StatusCreated)
					if err := app.DB.Where("household_person_id = ?", person.ID).First(&before).Error; err != nil {
						t.Fatal(err)
					}
					if path == "conversion resend" {
						target += "/resend"
					} else {
						if err := app.DB.Model(&before).Update("expires_at", time.Now().Add(-time.Hour)).Error; err != nil {
							t.Fatal(err)
						}
					}
					before = invitationIn(t, app, before.ID)
				}
			}
			audits := auditCount(t, app, audit.InvitationCreated) + auditCount(t, app, audit.InvitationResent) + auditCount(t, app, audit.InvitationRevoked)
			jobsBefore := len(mailJobsOfKind(t, app, kindInvitationMail))
			stop := failJobEnqueue(t, app.DB, kindInvitationMail)
			apiDo(t, client, "POST", target, map[string]any{"email": "invited@example.com", "name": "New Person"}, http.StatusInternalServerError)
			stop()
			if len(mailJobsOfKind(t, app, kindInvitationMail)) != jobsBefore || audits != auditCount(t, app, audit.InvitationCreated)+auditCount(t, app, audit.InvitationResent)+auditCount(t, app, audit.InvitationRevoked) {
				t.Fatal("failed enqueue committed an invitation audit or job")
			}
			var rows []models.Invitation
			if err := app.DB.Where("email = ?", "invited@example.com").Find(&rows).Error; err != nil {
				t.Fatal(err)
			}
			if before.ID == 0 {
				if len(rows) != 0 {
					t.Fatal("failed enqueue left a new invitation")
				}
			} else if len(rows) != 1 || !reflect.DeepEqual(rows[0], before) {
				t.Fatal("failed enqueue changed or revoked the existing invitation")
			}
			if mailCount(t, app, "invited@example.com") != 0 {
				t.Fatal("failed enqueue sent mail")
			}
		})
	}
}

// pauseInvitationLock stops the selected participant after it has taken the
// invitation lock. Ordinary acceptance takes it with a locking select just
// before its claim UPDATE and keeps it, so pausing after that UPDATE still
// holds the lock; conversion and preparation take it with SELECT FOR UPDATE
// after their discovery reads.
func pauseInvitationLock(t *testing.T, db *gorm.DB, operation string, read bool) (<-chan struct{}, func()) {
	t.Helper()
	reached, resume := make(chan struct{}), make(chan struct{})
	var paused atomic.Bool
	name := fmt.Sprintf("test:pause_invitation_lock:%d", queryCounterSeq.Add(1))
	pause := func(tx *gorm.DB) {
		if tx.Statement.Table != "invitations" || tx.Statement.Context.Value(testDBOperationKey{}) != operation || tx.Error != nil {
			return
		}
		if read {
			if _, locked := tx.Statement.Clauses["FOR"]; !locked {
				return
			}
		}
		if paused.Swap(true) {
			return
		}
		close(reached)
		select {
		case <-resume:
		case <-tx.Statement.Context.Done():
			tx.AddError(tx.Statement.Context.Err())
		}
	}
	var remove func()
	if read {
		if err := db.Callback().Query().After("gorm:query").Register(name, pause); err != nil {
			t.Fatal(err)
		}
		remove = func() { _ = db.Callback().Query().Remove(name) }
	} else {
		if err := db.Callback().Update().After("gorm:update").Register(name, pause); err != nil {
			t.Fatal(err)
		}
		remove = func() { _ = db.Callback().Update().Remove(name) }
	}
	var once sync.Once
	release := func() { once.Do(func() { close(resume) }) }
	t.Cleanup(func() { release(); remove() })
	return reached, release
}

func TestInvitationMailAndAcceptanceLockOrders(t *testing.T) {
	for _, conversion := range []bool{false, true} {
		for _, first := range []string{"delivery", "acceptance"} {
			t.Run(fmt.Sprintf("conversion=%v/%s first", conversion, first), func(t *testing.T) {
				app := testApp(t)
				f := queueHeldInvitationMail(t, app, conversion, true)
				// Run the handler directly on a known session, retiring its job so
				// later outbox reads cannot trigger an extra delivery.
				if err := app.DB.Delete(&models.Job{}, f.job.ID).Error; err != nil {
					t.Fatal(err)
				}
				old := "previous-invitation-capability"
				if err := app.DB.Model(&f.inv).Update("token_hash", hashToken(old)).Error; err != nil {
					t.Fatal(err)
				}
				expected := http.StatusBadRequest
				if first == "acceptance" {
					expected = http.StatusOK
				}
				work := func(operation string) func(*gorm.DB) error {
					return func(db *gorm.DB) error {
						a := &App{DB: db, Store: app.Store, Mailer: app.Mailer, Config: app.Config}
						if operation == "delivery" {
							return deliverMailJob(db.Statement.Context, a, kindInvitationMail, invitationMailJob{InvitationID: f.inv.ID})
						}
						w := httptest.NewRecorder()
						c, _ := gin.CreateTestContext(w)
						c.Request = httptest.NewRequest("POST", "/api/v1/invitations/accept", strings.NewReader(jsonString(map[string]string{
							"token": old, "name": "Invited Person", "password": "password1234", "ageBand": "adult",
						}))).WithContext(db.Statement.Context)
						c.Request.Header.Set("Content-Type", "application/json")
						a.apiAcceptInvitation(c)
						if w.Code != expected {
							return fmt.Errorf("acceptance: got %d %s, want %d", w.Code, w.Body.String(), expected)
						}
						return nil
					}
				}
				second := "acceptance"
				if first == "acceptance" {
					second = "delivery"
				}
				reached, release := pauseInvitationLock(t, app.DB, first, first == "delivery" || conversion)
				winner, winnerDone := startDBSession(t, app.DB, first, work(first))
				awaitDBSignal(t, reached)
				loser, loserDone := startDBSession(t, app.DB, second, work(second))
				waitDBBlocked(t, app.DB, loser, winner)
				release()
				if err := awaitDBResult(t, winnerDone); err != nil {
					t.Fatal(err)
				}
				if err := awaitDBResult(t, loserDone); err != nil {
					t.Fatal(err)
				}
				inv := invitationIn(t, app, f.inv.ID)
				var accounts, memberships, requests int64
				if err := app.DB.Model(&models.User{}).Where("email = ?", f.inv.Email).Count(&accounts).Error; err != nil {
					t.Fatal(err)
				}
				if err := app.DB.Model(&models.Membership{}).Joins("JOIN users ON users.id = memberships.user_id").Where("users.email = ?", f.inv.Email).Count(&memberships).Error; err != nil {
					t.Fatal(err)
				}
				if err := app.DB.Model(&models.AccountRequest{}).Where("email = ?", f.inv.Email).Count(&requests).Error; err != nil {
					t.Fatal(err)
				}
				if first == "acceptance" {
					wantAccounts, wantMemberships, wantRequests := int64(1), int64(1), int64(0)
					if conversion {
						wantAccounts, wantMemberships, wantRequests = 0, 0, 1
					}
					if inv.AcceptedAt == nil || inv.TokenHash != hashToken(old) || mailCount(t, app, f.inv.Email) != 0 ||
						accounts != wantAccounts || memberships != wantMemberships || requests != wantRequests || auditCount(t, app, audit.InvitationAccepted) != 1 {
						t.Fatal("acceptance first did not make waiting delivery a no-op")
					}
					return
				}
				if inv.AcceptedAt != nil || inv.TokenHash == hashToken(old) || !inv.ExpiresAt.Equal(f.inv.ExpiresAt) ||
					accounts != 0 || memberships != 0 || requests != 0 || auditCount(t, app, audit.InvitationAccepted) != 0 || auditCount(t, app, audit.AccountRequested) != 0 {
					t.Fatal("rotation first allowed a stale claim or leaked its account, membership, request or audit")
				}
				fresh := tokenInEmail(t, latestEmail(t, app, f.inv.Email))
				anon := anonClient(t, app, "10.7.3.2")
				apiDo(t, anon, "POST", "/api/v1/invitations/accept", map[string]string{
					"token": fresh, "name": "Invited Person", "password": "password1234", "ageBand": "adult",
				}, http.StatusOK)
			})
		}
	}
}

func TestInvitationMailPreparationUsesSingleConnection(t *testing.T) {
	for _, conversion := range []bool{false, true} {
		t.Run(fmt.Sprintf("conversion=%v", conversion), func(t *testing.T) {
			app := testApp(t)
			f := queueHeldInvitationMail(t, app, conversion, true)
			if err := app.DB.Create(&models.Setting{Key: "site_name", Value: "Grace Church"}).Error; err != nil {
				t.Fatal(err)
			}
			if err := app.DB.Create(&models.Setting{Key: settingTimezone, Value: "America/Boise"}).Error; err != nil {
				t.Fatal(err)
			}
			var dbName string
			if err := app.DB.Raw("SELECT current_database()").Scan(&dbName).Error; err != nil {
				t.Fatal(err)
			}
			db, err := testdb.Open(dbName)
			if err != nil {
				t.Fatal(err)
			}
			sqlDB, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = sqlDB.Close() })
			sqlDB.SetMaxOpenConns(1)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			app.DB = db.WithContext(ctx)
			var sent []mail.Message
			app.Mailer = directMailSenderFunc(func(_ context.Context, msg mail.Message) error {
				sent = append(sent, msg)
				return nil
			})
			if err := deliverMailJob(ctx, app, kindInvitationMail, invitationMailJob{InvitationID: f.inv.ID}); err != nil {
				t.Fatalf("one-connection invitation delivery: %v", err)
			}
			if len(sent) != 1 || sent[0].To != f.inv.Email || !strings.Contains(sent[0].Subject, "Grace Church") ||
				!strings.Contains(sent[0].Text, f.inviter.PublicName()) || !invitationIn(t, app, f.inv.ID).ExpiresAt.Equal(f.inv.ExpiresAt) {
				t.Fatal("preparation did not use the current facts and preserve the deadline")
			}
		})
	}
}

// heldEmailChange requests an address change through the real API and leaves
// the verification mail queued, so a test can change the world before
// delivery. The old-address notice is synchronous and is already sent.
type heldEmailChange struct {
	user      *models.User
	client    *apiClient
	newEmail  string
	job       models.Job
	verify    models.AccountToken
	cancel    models.AccountToken
	cancelRaw string
}

func rawMailTo(t *testing.T, app *App, to string) string {
	t.Helper()
	var m models.MailOutbox
	if err := app.DB.Where("to_address = ?", to).Order("id DESC").First(&m).Error; err != nil {
		t.Fatalf("no mail to %s: %v", to, err)
	}
	return tokenInEmail(t, mail.OutboxMessage{To: m.ToAddress, Body: m.Body})
}

func queueHeldEmailChange(t *testing.T, app *App) heldEmailChange {
	t.Helper()
	f := heldEmailChange{newEmail: "alice.new@example.com"}
	f.user = newTestUser(t, app, "alice", "password1234", models.RoleMember)
	f.client = loginAPI(t, app, "alice", "password1234")
	f.client.holdNotices = true
	answer := apiDo(t, f.client, "PUT", "/api/v1/me", map[string]string{"email": f.newEmail, "currentPassword": "password1234"}, http.StatusOK)
	if !strings.Contains(answer, `"verificationSent":true`) {
		t.Fatalf("request did not report the verification as sent: %s", answer)
	}
	verify := accountTokens(t, app, f.user.ID, models.TokenEmailVerify)
	cancel := accountTokens(t, app, f.user.ID, models.TokenEmailChangeCancel)
	rows := mailJobsOfKind(t, app, kindEmailVerifyMail)
	if len(verify) != 1 || len(cancel) != 1 || len(rows) != 1 {
		t.Fatalf("request must leave one verify token, one cancel token and one job: %d %d %d", len(verify), len(cancel), len(rows))
	}
	f.verify, f.cancel, f.job = verify[0], cancel[0], rows[0]
	if !payloadEquals(f.job, accountTokenMailJob{AccountTokenID: f.verify.ID}) || f.job.MaxAttempts != directMailAttempts || f.job.UniqueKey != nil {
		t.Fatalf("job must carry only the verification token's ID: %+v", f.job)
	}
	if strings.Contains(f.job.Payload, f.newEmail) || strings.Contains(f.job.Payload, f.verify.TokenHash) {
		t.Fatalf("job payload leaked the address or hash: %s", f.job.Payload)
	}
	if mailCount(t, app, f.newEmail) != 0 {
		t.Fatal("request sent the verification mail before the job ran")
	}
	// The old-address notice is attempted synchronously, with the cancel link.
	if mailCount(t, app, f.user.Email) != 1 {
		t.Fatal("request did not send the old-address notice synchronously")
	}
	f.cancelRaw = rawMailTo(t, app, f.user.Email)
	if hashToken(f.cancelRaw) != f.cancel.TokenHash {
		t.Fatal("notice does not carry the stored cancel capability")
	}
	if f.verify.ExpiresAt.After(f.cancel.ExpiresAt) {
		t.Fatalf("verification outlives cancellation: %v > %v", f.verify.ExpiresAt, f.cancel.ExpiresAt)
	}
	return f
}

func TestEmailVerifyMailRetryPreservesDeadlineAndCancellationWindow(t *testing.T) {
	app := testApp(t)
	f := queueHeldEmailChange(t, app)
	var sent []mail.Message
	app.Mailer = directMailSenderFunc(func(_ context.Context, msg mail.Message) error {
		sent = append(sent, msg)
		// Preparation must commit and release its locks before provider I/O.
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := app.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec("SELECT id FROM account_tokens WHERE id = ? FOR UPDATE NOWAIT", f.verify.ID).Error; err != nil {
				return err
			}
			return tx.Exec("SELECT id FROM users WHERE id = ? FOR UPDATE NOWAIT", f.user.ID).Error
		}); err != nil {
			return fmt.Errorf("rows still locked during send: %w", err)
		}
		if len(sent) == 1 {
			return errors.New("451 try again")
		}
		return nil
	})
	runResetMail(t, app)
	failed := reloadJob(t, app, f.job.ID)
	if failed.Status != models.JobFailed || failed.Attempts != 1 || len(sent) != 1 {
		t.Fatalf("transient send did not remain retryable: %+v", failed)
	}
	first := tokenRe.FindStringSubmatch(sent[0].Text)[1]
	rows := accountTokens(t, app, f.user.ID, models.TokenEmailVerify)
	if len(rows) != 1 || rows[0].ID != f.verify.ID || rows[0].TokenHash != hashToken(first) ||
		!rows[0].ExpiresAt.Equal(f.verify.ExpiresAt) || rows[0].Payload != f.verify.Payload {
		t.Fatalf("first preparation must change only the hash: %+v", rows)
	}
	if err := app.DB.Model(&models.Job{}).Where("id = ?", f.job.ID).Update("run_at", time.Now().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	runResetMail(t, app)
	if got := reloadJob(t, app, f.job.ID); got.Status != models.JobDone || got.Attempts != 2 || len(sent) != 2 {
		t.Fatalf("retry did not finish: %+v", got)
	}
	last := tokenRe.FindStringSubmatch(sent[1].Text)[1]
	rows = accountTokens(t, app, f.user.ID, models.TokenEmailVerify)
	cancel := accountTokens(t, app, f.user.ID, models.TokenEmailChangeCancel)
	if last == first || len(rows) != 1 || rows[0].TokenHash != hashToken(last) || !rows[0].ExpiresAt.Equal(f.verify.ExpiresAt) {
		t.Fatal("retry reused a capability or extended the deadline")
	}
	if len(cancel) != 1 || !cancel[0].ExpiresAt.Equal(f.cancel.ExpiresAt) || cancel[0].TokenHash != f.cancel.TokenHash || rows[0].ExpiresAt.After(cancel[0].ExpiresAt) {
		t.Fatalf("rotation touched the cancellation capability or outlived it: %+v", cancel)
	}
	for _, msg := range sent {
		if msg.To != f.newEmail || msg.Optional || !strings.Contains(msg.Subject, "Verify") || !strings.Contains(msg.Text, "expires within 24 hours") ||
			!strings.Contains(msg.Text, "/verify-email?token=") {
			t.Fatalf("verification copy or recipient changed: %+v", msg)
		}
	}
	anon := anonClient(t, app, "10.7.5.1")
	apiDo(t, anon, "POST", "/api/v1/me/email/verify", map[string]string{"token": first}, http.StatusBadRequest)
	apiDo(t, anon, "POST", "/api/v1/me/email/verify", map[string]string{"token": last}, http.StatusOK)
	var fresh models.User
	if err := app.DB.First(&fresh, f.user.ID).Error; err != nil || fresh.Email != f.newEmail {
		t.Fatalf("recovered link did not complete the change: %v %+v", err, fresh)
	}
	// Completion revokes the cancel capability; it can no longer be used.
	apiDo(t, anon, "POST", "/api/v1/me/email/cancel", map[string]string{"token": f.cancelRaw}, http.StatusBadRequest)
}

func TestEmailVerifyMailRetryAfterExpiryDoesNotRevive(t *testing.T) {
	app := testApp(t)
	f := queueHeldEmailChange(t, app)
	var sent []string
	app.Mailer = directMailSenderFunc(func(_ context.Context, msg mail.Message) error {
		sent = append(sent, tokenRe.FindStringSubmatch(msg.Text)[1])
		return errors.New("451 try again")
	})
	runResetMail(t, app)
	if len(sent) != 1 {
		t.Fatalf("first attempt sent %d", len(sent))
	}
	hashAfterFirst := hashToken(sent[0])
	if err := app.DB.Model(&models.AccountToken{}).Where("id = ?", f.verify.ID).Update("expires_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Model(&models.Job{}).Where("id = ?", f.job.ID).Update("run_at", time.Now().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	runResetMail(t, app)
	rows := accountTokens(t, app, f.user.ID, models.TokenEmailVerify)
	if got := reloadJob(t, app, f.job.ID); got.Status != models.JobDone || len(sent) != 1 {
		t.Fatalf("expired retry must finish quietly without sending: %+v sent=%d", got, len(sent))
	}
	if len(rows) != 1 || rows[0].TokenHash != hashAfterFirst {
		t.Fatalf("expired retry changed the hash: %+v", rows)
	}
	anon := anonClient(t, app, "10.7.5.2")
	apiDo(t, anon, "POST", "/api/v1/me/email/verify", map[string]string{"token": sent[0]}, http.StatusBadRequest)
}

func TestEmailVerifyMailSkipsClosedRequests(t *testing.T) {
	for _, state := range []string{"cancelled", "completed", "superseded", "expired", "used", "disabled", "deleted", "no longer able to sign in"} {
		t.Run(state, func(t *testing.T) {
			app := testApp(t)
			f := queueHeldEmailChange(t, app)
			anon := anonClient(t, app, "10.7.5.3")
			var err error
			switch state {
			case "cancelled":
				apiDo(t, anon, "POST", "/api/v1/me/email/cancel", map[string]string{"token": f.cancelRaw}, http.StatusOK)
			case "completed":
				raw, changed, rotateErr := rotateAccountToken(app.DB, f.verify.ID, models.TokenEmailVerify, f.user.ID)
				if rotateErr != nil || !changed {
					t.Fatalf("rotate: %v %v", changed, rotateErr)
				}
				apiDo(t, anon, "POST", "/api/v1/me/email/verify", map[string]string{"token": raw}, http.StatusOK)
			case "superseded":
				apiDo(t, f.client, "PUT", "/api/v1/me", map[string]string{"email": "other.new@example.com", "currentPassword": "password1234"}, http.StatusOK)
			case "expired":
				err = app.DB.Model(&models.AccountToken{}).Where("id = ?", f.verify.ID).Update("expires_at", time.Now().Add(-time.Minute)).Error
			case "used":
				err = app.DB.Model(&models.AccountToken{}).Where("id = ?", f.verify.ID).Update("used_at", time.Now()).Error
			case "disabled":
				err = app.DB.Model(f.user).Update("status", models.UserDisabled).Error
			case "deleted":
				err = app.DB.Delete(f.user).Error
			case "no longer able to sign in":
				err = app.DB.Model(f.user).Update("password_hash", nil).Error
			}
			if err != nil {
				t.Fatal(err)
			}
			before := accountTokens(t, app, f.user.ID, models.TokenEmailVerify)
			sendsBefore := mailCount(t, app, f.newEmail)
			// Run only the original job, directly, so a superseding request's
			// own job does not mask a stale one.
			if err := deliverMailJob(context.Background(), app, kindEmailVerifyMail, accountTokenMailJob{AccountTokenID: f.verify.ID}); err != nil {
				t.Fatal(err)
			}
			after := accountTokens(t, app, f.user.ID, models.TokenEmailVerify)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("stale job changed a token: before=%+v after=%+v", before, after)
			}
			if mailCount(t, app, f.newEmail) != sendsBefore || mailCount(t, app, "other.new@example.com") != 0 {
				t.Fatal("stale job sent a verification link")
			}
			if state == "superseded" {
				// The replacement request's own job still works.
				if len(after) != 1 || after[0].ID == f.verify.ID {
					t.Fatalf("superseding request must hold the only live row: %+v", after)
				}
				if err := deliverMailJob(context.Background(), app, kindEmailVerifyMail, accountTokenMailJob{AccountTokenID: after[0].ID}); err != nil {
					t.Fatal(err)
				}
				if mailCount(t, app, "other.new@example.com") != 1 {
					t.Fatal("superseding request's job did not deliver")
				}
			}
		})
	}
}

func TestEmailVerifyMailEnqueueFailureRollsBackBothTokensAndNotice(t *testing.T) {
	app := testApp(t)
	f := queueHeldEmailChange(t, app)
	tokensBefore := append(accountTokens(t, app, f.user.ID, models.TokenEmailVerify), accountTokens(t, app, f.user.ID, models.TokenEmailChangeCancel)...)
	audits := auditCount(t, app, audit.EmailChangeRequested)
	mails := mailCount(t, app, "")
	stop := failJobEnqueue(t, app.DB, kindEmailVerifyMail)
	apiDo(t, f.client, "PUT", "/api/v1/me", map[string]string{"email": "second.new@example.com", "currentPassword": "password1234"}, http.StatusInternalServerError)
	stop()
	tokensAfter := append(accountTokens(t, app, f.user.ID, models.TokenEmailVerify), accountTokens(t, app, f.user.ID, models.TokenEmailChangeCancel)...)
	if !reflect.DeepEqual(tokensBefore, tokensAfter) {
		t.Fatalf("failed enqueue changed the earlier pending pair: %+v -> %+v", tokensBefore, tokensAfter)
	}
	if len(mailJobsOfKind(t, app, kindEmailVerifyMail)) != 1 || auditCount(t, app, audit.EmailChangeRequested) != audits {
		t.Fatal("failed enqueue committed a job or audit")
	}
	if mailCount(t, app, "") != mails || mailCount(t, app, "second.new@example.com") != 0 {
		t.Fatal("failed enqueue attempted the old-address notice or a verification mail")
	}
	// The same request succeeds once enqueue works, and replaces the pair.
	apiDo(t, f.client, "PUT", "/api/v1/me", map[string]string{"email": "second.new@example.com", "currentPassword": "password1234"}, http.StatusOK)
	if len(mailJobsOfKind(t, app, kindEmailVerifyMail)) != 2 || mailCount(t, app, f.user.Email) != 2 {
		t.Fatal("retry after enqueue recovery did not queue the job and send the notice")
	}
}

func TestEmailVerifyMailAndRedemptionLockOrders(t *testing.T) {
	for _, first := range []string{"delivery", "redemption"} {
		t.Run(first+" first", func(t *testing.T) {
			app := testApp(t)
			f := queueHeldEmailChange(t, app)
			// The race runs the handler directly, so retire the queued job that
			// would otherwise deliver again while the test reads the outbox.
			if err := app.DB.Where("kind = ?", kindEmailVerifyMail).Delete(&models.Job{}).Error; err != nil {
				t.Fatal(err)
			}
			old, changed, err := rotateAccountToken(app.DB, f.verify.ID, models.TokenEmailVerify, f.user.ID)
			if err != nil || !changed {
				t.Fatalf("rotate: %v %v", changed, err)
			}
			expected := http.StatusBadRequest
			if first == "redemption" {
				expected = http.StatusOK
			}
			work := func(operation string) func(*gorm.DB) error {
				return func(db *gorm.DB) error {
					a := &App{DB: db, Store: app.Store, Mailer: app.Mailer, Config: app.Config}
					if operation == "delivery" {
						return deliverMailJob(db.Statement.Context, a, kindEmailVerifyMail, accountTokenMailJob{AccountTokenID: f.verify.ID})
					}
					w := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(w)
					c.Request = httptest.NewRequest("POST", "/api/v1/me/email/verify",
						strings.NewReader(jsonString(map[string]string{"token": old}))).WithContext(db.Statement.Context)
					c.Request.Header.Set("Content-Type", "application/json")
					a.apiVerifyEmailChange(c)
					if w.Code != expected {
						return fmt.Errorf("redemption: got %d %s, want %d", w.Code, w.Body.String(), expected)
					}
					return nil
				}
			}
			second := "redemption"
			if first == "redemption" {
				second = "delivery"
			}
			reached, release := pauseDBRead(t, app.DB, first, "users")
			winner, winnerDone := startDBSession(t, app.DB, first, work(first))
			awaitDBSignal(t, reached) // winner holds the contact gate and the user lock
			loser, loserDone := startDBSession(t, app.DB, second, work(second))
			waitDBBlocked(t, app.DB, loser, winner)
			release()
			if err := awaitDBResult(t, winnerDone); err != nil {
				t.Fatal(err)
			}
			if err := awaitDBResult(t, loserDone); err != nil {
				t.Fatal(err)
			}
			var fresh models.User
			if err := app.DB.First(&fresh, f.user.ID).Error; err != nil {
				t.Fatal(err)
			}
			rows := accountTokens(t, app, f.user.ID, models.TokenEmailVerify)
			if first == "redemption" {
				// The change completed first: the waiting delivery sends nothing
				// and mints nothing. Redemption keeps its consumed row as history
				// (only unused tokens are revoked), unchanged except for UsedAt;
				// the unused cancel capability is gone.
				if fresh.Email != f.newEmail || mailCount(t, app, f.newEmail) != 0 ||
					len(accountTokens(t, app, f.user.ID, models.TokenEmailChangeCancel)) != 0 {
					t.Fatalf("redemption first: user=%+v tokens=%+v", fresh, rows)
				}
				if len(rows) != 1 || rows[0].ID != f.verify.ID || rows[0].UsedAt == nil || rows[0].TokenHash != hashToken(old) ||
					!rows[0].ExpiresAt.Equal(f.verify.ExpiresAt) || rows[0].Payload != f.verify.Payload {
					t.Fatalf("redemption first: the waiting delivery rotated or replaced the consumed row: %+v", rows)
				}
				return
			}
			// Rotation came first: the old link is dead and the address unchanged.
			if fresh.Email != f.user.Email || len(rows) != 1 || rows[0].TokenHash == hashToken(old) || rows[0].UsedAt != nil ||
				!rows[0].ExpiresAt.Equal(f.verify.ExpiresAt) {
				t.Fatalf("delivery first: user=%+v tokens=%+v", fresh, rows)
			}
			newRaw := rawMailTo(t, app, f.newEmail)
			anon := anonClient(t, app, "10.7.5.4")
			apiDo(t, anon, "POST", "/api/v1/me/email/verify", map[string]string{"token": old}, http.StatusBadRequest)
			apiDo(t, anon, "POST", "/api/v1/me/email/verify", map[string]string{"token": newRaw}, http.StatusOK)
			if err := app.DB.First(&fresh, f.user.ID).Error; err != nil || fresh.Email != f.newEmail {
				t.Fatalf("replacement link did not complete the change: %v %+v", err, fresh)
			}
		})
	}
}

// expiryRace holds a real lock while another transaction, begun before the
// deadline, waits for it. Once that waiter is observed blocked behind the
// holder, the holder moves the deadline to the present in its own transaction
// and commits. The deadline therefore falls after the waiter's transaction
// began (so a now() test still passes) and before it can proceed (so only a
// wall-clock test refuses). Both participants are real, reserved sessions.
type expiryRace struct {
	holder int
	locked chan struct{}
	waiter chan int
	done   <-chan error
}

func holdThenExpire(t *testing.T, app *App, lock func(*gorm.DB) error, expire func(*gorm.DB) (time.Time, error)) *expiryRace {
	t.Helper()
	r := &expiryRace{locked: make(chan struct{}), waiter: make(chan int, 1)}
	r.holder, r.done = startDBSession(t, app.DB, "holder", func(db *gorm.DB) error {
		return db.Transaction(func(tx *gorm.DB) error {
			if err := lock(tx); err != nil {
				return err
			}
			close(r.locked)
			var waiter int
			select {
			case waiter = <-r.waiter:
			case <-tx.Statement.Context.Done():
				return tx.Statement.Context.Err()
			}
			deadline, err := expire(tx)
			if err != nil {
				return err
			}
			var began bool
			if err := tx.Raw("SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE pid = ? AND xact_start < ?)", waiter, deadline).Scan(&began).Error; err != nil {
				return err
			}
			if !began {
				return errors.New("the waiter's transaction began after the deadline, so this race proves nothing")
			}
			// Let the wall clock pass the deadline before the waiter resumes.
			return tx.Exec("SELECT pg_sleep(0.02)").Error
		})
	})
	return r
}

// run starts the waiter, waits until it is blocked behind the holder, lets the
// holder expire and release, and returns the waiter's own result.
func (r *expiryRace) run(t *testing.T, app *App, operation string, work func(*gorm.DB) error) error {
	t.Helper()
	awaitDBSignal(t, r.locked)
	waiter, waiterDone := startDBSession(t, app.DB, operation, work)
	waitDBBlocked(t, app.DB, waiter, r.holder)
	r.waiter <- waiter
	if err := awaitDBResult(t, r.done); err != nil {
		t.Fatal(err)
	}
	return awaitDBResult(t, waiterDone)
}

func lockUserRow(id uint) func(*gorm.DB) error {
	return func(tx *gorm.DB) error {
		return tx.Exec("SELECT id FROM users WHERE id = ? FOR NO KEY UPDATE", id).Error
	}
}

func lockInvitationRow(id uint) func(*gorm.DB) error {
	return func(tx *gorm.DB) error {
		return tx.Exec("SELECT id FROM invitations WHERE id = ? FOR UPDATE", id).Error
	}
}

func expireRowNow(table string, id uint) func(*gorm.DB) (time.Time, error) {
	return func(tx *gorm.DB) (time.Time, error) {
		var at time.Time
		err := tx.Raw("UPDATE "+table+" SET expires_at = clock_timestamp() WHERE id = ? RETURNING expires_at", id).Scan(&at).Error
		return at, err
	}
}

// A request whose transaction began before the deadline must not rotate, mail
// or redeem once it gets its lock after the deadline. The wait is on the
// contact gate (so the first lookup is late) or on the user row (so the lookup
// has already passed and only the final rotation or claim can refuse).
func TestEmailVerifyMailAndRedemptionRefuseADeadlinePassedDuringALockWait(t *testing.T) {
	for _, operation := range []string{"delivery", "redemption"} {
		for _, wait := range []string{"contact gate", "user lock"} {
			t.Run(operation+" waiting on the "+wait, func(t *testing.T) {
				app := testApp(t)
				f := queueHeldEmailChange(t, app)
				if err := app.DB.Where("kind = ?", kindEmailVerifyMail).Delete(&models.Job{}).Error; err != nil {
					t.Fatal(err)
				}
				old, changed, err := rotateAccountToken(app.DB, f.verify.ID, models.TokenEmailVerify, f.user.ID)
				if err != nil || !changed {
					t.Fatalf("rotate: %v %v", changed, err)
				}
				lock := lockContacts
				if wait == "user lock" {
					lock = lockUserRow(f.user.ID)
				}
				race := holdThenExpire(t, app, lock, expireRowNow("account_tokens", f.verify.ID))
				err = race.run(t, app, operation, func(db *gorm.DB) error {
					a := &App{DB: db, Store: app.Store, Mailer: app.Mailer, Config: app.Config}
					if operation == "delivery" {
						return deliverMailJob(db.Statement.Context, a, kindEmailVerifyMail, accountTokenMailJob{AccountTokenID: f.verify.ID})
					}
					w := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(w)
					c.Request = httptest.NewRequest("POST", "/api/v1/me/email/verify",
						strings.NewReader(jsonString(map[string]string{"token": old}))).WithContext(db.Statement.Context)
					c.Request.Header.Set("Content-Type", "application/json")
					a.apiVerifyEmailChange(c)
					if w.Code != http.StatusBadRequest {
						return fmt.Errorf("redemption: got %d %s, want 400", w.Code, w.Body.String())
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				var fresh models.User
				if err := app.DB.First(&fresh, f.user.ID).Error; err != nil {
					t.Fatal(err)
				}
				rows := accountTokens(t, app, f.user.ID, models.TokenEmailVerify)
				cancel := accountTokens(t, app, f.user.ID, models.TokenEmailChangeCancel)
				if fresh.Email != f.user.Email || mailCount(t, app, f.newEmail) != 0 {
					t.Fatalf("an expired link changed the address or was mailed: %+v", fresh)
				}
				if len(rows) != 1 || rows[0].TokenHash != hashToken(old) || rows[0].UsedAt != nil || rows[0].Payload != f.verify.Payload {
					t.Fatalf("an expired verification row was rotated or redeemed: %+v", rows)
				}
				if len(cancel) != 1 || cancel[0].TokenHash != f.cancel.TokenHash || cancel[0].UsedAt != nil {
					t.Fatalf("a refused request touched the cancellation capability: %+v", cancel)
				}
			})
		}
	}
}

func TestInvitationMailRefusesADeadlinePassedDuringALockWait(t *testing.T) {
	for _, conversion := range []bool{false, true} {
		t.Run(fmt.Sprintf("conversion=%v", conversion), func(t *testing.T) {
			app := testApp(t)
			f := queueHeldInvitationMail(t, app, conversion, true)
			if err := app.DB.Delete(&models.Job{}, f.job.ID).Error; err != nil {
				t.Fatal(err)
			}
			race := holdThenExpire(t, app, lockInvitationRow(f.inv.ID), expireRowNow("invitations", f.inv.ID))
			err := race.run(t, app, "delivery", func(db *gorm.DB) error {
				a := &App{DB: db, Store: app.Store, Mailer: app.Mailer, Config: app.Config}
				return deliverMailJob(db.Statement.Context, a, kindInvitationMail, invitationMailJob{InvitationID: f.inv.ID})
			})
			if err != nil {
				t.Fatal(err)
			}
			inv := invitationIn(t, app, f.inv.ID)
			if inv.TokenHash != f.inv.TokenHash || inv.AcceptedAt != nil || mailCount(t, app, f.inv.Email) != 0 {
				t.Fatalf("an expired invitation was rotated or mailed: %+v", inv)
			}
		})
	}
}

// The acceptance transaction reads the invitation, then waits for the inviter's
// row lock; only the final claim can notice that the deadline has passed.
func TestInvitationAcceptanceRefusesADeadlinePassedDuringALockWait(t *testing.T) {
	app := testApp(t)
	f := queueHeldInvitationMail(t, app, false, true)
	if err := app.DB.Delete(&models.Job{}, f.job.ID).Error; err != nil {
		t.Fatal(err)
	}
	const raw = "known-invitation-capability"
	if err := app.DB.Model(&f.inv).Update("token_hash", hashToken(raw)).Error; err != nil {
		t.Fatal(err)
	}
	race := holdThenExpire(t, app, lockUserRow(f.inv.InvitedBy), expireRowNow("invitations", f.inv.ID))
	err := race.run(t, app, "acceptance", func(db *gorm.DB) error {
		a := &App{DB: db, Store: app.Store, Mailer: app.Mailer, Config: app.Config}
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/api/v1/invitations/accept", strings.NewReader(jsonString(map[string]string{
			"token": raw, "name": "Invited Person", "password": "password1234", "ageBand": "adult",
		}))).WithContext(db.Statement.Context)
		c.Request.Header.Set("Content-Type", "application/json")
		a.apiAcceptInvitation(c)
		if w.Code != http.StatusBadRequest {
			return fmt.Errorf("acceptance: got %d %s, want 400", w.Code, w.Body.String())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	inv := invitationIn(t, app, f.inv.ID)
	var accounts, memberships int64
	if err := app.DB.Model(&models.User{}).Where("email = ?", f.inv.Email).Count(&accounts).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Model(&models.Membership{}).Joins("JOIN users ON users.id = memberships.user_id").Where("users.email = ?", f.inv.Email).Count(&memberships).Error; err != nil {
		t.Fatal(err)
	}
	if inv.AcceptedAt != nil || accounts != 0 || memberships != 0 || auditCount(t, app, audit.InvitationAccepted) != 0 {
		t.Fatalf("an expired invitation was accepted: %+v accounts=%d memberships=%d", inv, accounts, memberships)
	}
}

// passDeadlineUnchanged is the expire step for a holder that only locks its
// row, as a delivery worker does: nothing is written, so a waiter's qualifying
// read is never invalidated and only the clock can refuse it. The deadline was
// set ahead of time; the holder checks the waiter reached it first (a late
// waiter would be refused for the wrong reason and prove nothing) and then
// waits out the remaining time inside its transaction.
func passDeadlineUnchanged(table string, id uint) func(*gorm.DB) (time.Time, error) {
	return func(tx *gorm.DB) (time.Time, error) {
		var at time.Time
		if err := tx.Raw("SELECT expires_at FROM "+table+" WHERE id = ?", id).Scan(&at).Error; err != nil {
			return at, err
		}
		var early bool
		if err := tx.Raw("SELECT clock_timestamp() < ?", at).Scan(&early).Error; err != nil {
			return at, err
		}
		if !early {
			return at, errors.New("the waiter was blocked only after the deadline, so this race proves nothing")
		}
		return at, tx.Exec("SELECT pg_sleep(GREATEST(0, EXTRACT(EPOCH FROM (? ::timestamptz - clock_timestamp()))))", at).Error
	}
}

// A delivery worker locks the invitation row FOR UPDATE and leaves it unchanged.
// Acceptance that already qualified and waits on that lock resumes without its
// row being re-read, so the claim must take the lock in a statement of its own
// and test the deadline afterwards. The holder releases only after the deadline
// and writes nothing; the earlier locks are not what the waiter waits on.
func TestInvitationAcceptanceRefusesADeadlinePassedDuringAnUnchangedRowLockWait(t *testing.T) {
	for _, conversion := range []bool{false, true} {
		t.Run(fmt.Sprintf("conversion=%v", conversion), func(t *testing.T) {
			app := testApp(t)
			f := queueHeldInvitationMail(t, app, conversion, true)
			if err := app.DB.Delete(&models.Job{}, f.job.ID).Error; err != nil {
				t.Fatal(err)
			}
			const raw = "known-invitation-capability"
			if err := app.DB.Model(&f.inv).Updates(map[string]any{
				"token_hash": hashToken(raw),
				"expires_at": gorm.Expr("clock_timestamp() + interval '2 seconds'"),
			}).Error; err != nil {
				t.Fatal(err)
			}
			before := invitationIn(t, app, f.inv.ID)
			race := holdThenExpire(t, app, lockInvitationRow(f.inv.ID), passDeadlineUnchanged("invitations", f.inv.ID))
			err := race.run(t, app, "acceptance", func(db *gorm.DB) error {
				a := &App{DB: db, Store: app.Store, Mailer: app.Mailer, Config: app.Config}
				w := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(w)
				c.Request = httptest.NewRequest("POST", "/api/v1/invitations/accept", strings.NewReader(jsonString(map[string]string{
					"token": raw, "name": "Invited Person", "password": "password1234", "ageBand": "adult",
				}))).WithContext(db.Statement.Context)
				c.Request.Header.Set("Content-Type", "application/json")
				a.apiAcceptInvitation(c)
				if w.Code != http.StatusBadRequest {
					return fmt.Errorf("acceptance: got %d %s, want 400", w.Code, w.Body.String())
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			inv := invitationIn(t, app, f.inv.ID)
			var accounts, memberships int64
			if err := app.DB.Model(&models.User{}).Where("email = ?", f.inv.Email).Count(&accounts).Error; err != nil {
				t.Fatal(err)
			}
			if err := app.DB.Model(&models.Membership{}).Joins("JOIN users ON users.id = memberships.user_id").Where("users.email = ?", f.inv.Email).Count(&memberships).Error; err != nil {
				t.Fatal(err)
			}
			if inv.AcceptedAt != nil || inv.TokenHash != before.TokenHash || !inv.ExpiresAt.Equal(before.ExpiresAt) ||
				accounts != 0 || memberships != 0 || auditCount(t, app, audit.InvitationAccepted) != 0 {
				t.Fatalf("an expired invitation was accepted after waiting on an unchanged row lock: %+v accounts=%d memberships=%d", inv, accounts, memberships)
			}
		})
	}
}

// Password reset and account setup redeem through lockAndConsumeAccountToken.
func TestLockAndConsumeAccountTokenRefusesADeadlinePassedDuringALockWait(t *testing.T) {
	app := testApp(t)
	user := newTestUser(t, app, "alice", "password1234", models.RoleMember)
	raw, err := createAccountToken(app.DB, models.TokenPasswordReset, user.ID, nil, tokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	rows := accountTokens(t, app, user.ID, models.TokenPasswordReset)
	if len(rows) != 1 {
		t.Fatalf("want one reset token, got %d", len(rows))
	}
	race := holdThenExpire(t, app, lockUserRow(user.ID), expireRowNow("account_tokens", rows[0].ID))
	err = race.run(t, app, "redemption", func(db *gorm.DB) error {
		return db.Transaction(func(tx *gorm.DB) error {
			_, _, err := lockAndConsumeAccountToken(tx, models.TokenPasswordReset, raw)
			return err
		})
	})
	if !errors.Is(err, errTokenInvalid) {
		t.Fatalf("an expired token was redeemed after a lock wait: %v", err)
	}
	after := accountTokens(t, app, user.ID, models.TokenPasswordReset)
	if len(after) != 1 || after[0].UsedAt != nil {
		t.Fatalf("the refused redemption marked the token used: %+v", after)
	}
}

// The old-address notice stays synchronous and keeps the public answer, but its
// provider call is bounded and its failure log carries no address.
func TestEmailChangeNoticeIsBoundedAndKeepsAddressesOutOfLogs(t *testing.T) {
	app := testApp(t)
	user := newTestUser(t, app, "alice", "password1234", models.RoleMember)
	client := loginAPI(t, app, "alice", "password1234")
	client.holdNotices = true
	const newEmail = "alice.new@example.com"
	var calls int
	var deadline time.Time
	var hasDeadline bool
	app.Mailer = directMailSenderFunc(func(ctx context.Context, msg mail.Message) error {
		if msg.To != user.Email {
			return nil
		}
		calls++
		deadline, hasDeadline = ctx.Deadline()
		return fmt.Errorf("550 5.1.1 <%s> mailbox unavailable", msg.To)
	})
	var logs bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(prev) })
	answer := apiDo(t, client, "PUT", "/api/v1/me", map[string]string{"email": newEmail, "currentPassword": "password1234"}, http.StatusOK)
	if !strings.Contains(answer, `"verificationSent":true`) {
		t.Fatalf("a failed notice changed the public answer: %s", answer)
	}
	if calls != 1 {
		t.Fatalf("the notice must be sent once, synchronously: %d", calls)
	}
	if !hasDeadline || time.Until(deadline) <= 0 || time.Until(deadline) > deliverSendTimeout {
		t.Fatalf("the notice send is not bounded by the shared timeout: %v %v", hasDeadline, deadline)
	}
	out := logs.String()
	if !strings.Contains(out, fmt.Sprintf("email change notice for user %d: ", user.ID)) {
		t.Fatalf("the failed notice was not logged by user ID: %q", out)
	}
	if strings.Contains(out, user.Email) || strings.Contains(out, newEmail) {
		t.Fatalf("the notice failure log leaked an address: %q", out)
	}
	if len(mailJobsOfKind(t, app, kindEmailVerifyMail)) != 1 {
		t.Fatal("the durable verification job was not queued")
	}
}
