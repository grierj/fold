package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"testing"
	"time"

	"fold/internal/audit"
	"fold/internal/mail"
	"fold/internal/providers"
	"fold/internal/testdb"
	"fold/models"

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

func runResetMail(t *testing.T, app *App) {
	t.Helper()
	ran, err := app.newRunner(app.mailKinds()...).RunOnce(context.Background(), "test:direct-mail")
	if err != nil || !ran {
		t.Fatalf("run reset delivery: ran=%v err=%v", ran, err)
	}
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
