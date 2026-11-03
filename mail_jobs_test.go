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
