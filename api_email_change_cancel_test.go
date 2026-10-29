package main

import (
	"errors"
	"net/http"
	"sync"
	"testing"

	"fold/internal/audit"
	"fold/models"

	"gorm.io/gorm"
)

func requestSecurityEmailChange(t *testing.T, app *App, client *apiClient, user *models.User, email string) (string, string) {
	t.Helper()
	apiDo(t, client, "PUT", "/api/v1/me", map[string]string{"email": email, "currentPassword": "password1234"}, http.StatusOK)
	verify := tokenInEmail(t, latestEmail(t, app, email))
	cancel := tokenInEmail(t, latestEmail(t, app, user.Email))
	if verify == cancel {
		t.Fatal("verification and cancellation must have independent capabilities")
	}
	return verify, cancel
}

func assertNoPendingSecurityTokens(t *testing.T, app *App, id uint) {
	t.Helper()
	var count int64
	if err := app.DB.Model(&models.AccountToken{}).Where("user_id = ? AND used_at IS NULL", id).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("outstanding account capabilities: %d", count)
	}
}

func TestEmailChangeCancellationEndsAllSubjectSessions(t *testing.T) {
	app := testApp(t)
	user := newTestUser(t, app, "cancel-subject", "password1234", models.RoleMember)
	first := loginAPI(t, app, user.Email, "password1234")
	second := loginAPI(t, app, user.Email, "password1234")
	foreign := newTestUser(t, app, "cancel-foreign", "password1234", models.RoleMember)
	other := loginAPI(t, app, foreign.Email, "password1234")
	verify, cancel := requestSecurityEmailChange(t, app, first, user, "cancel-new@example.com")
	for _, kind := range []string{models.TokenPasswordReset, models.TokenAccountSetup, models.TokenTeenConsent} {
		if _, err := createAccountToken(app.DB, kind, user.ID, nil, tokenTTL); err != nil {
			t.Fatal(err)
		}
	}
	// Merely opening a link cannot mutate anything, and the POST has no CSRF
	// exemption. Even a valid capability must survive a rejected submission.
	anon := newAPIClient(t, app)
	anon.do("GET", "/cancel-email-change?token="+cancel, nil, false)
	if w := anon.do("POST", "/api/v1/me/email/cancel", map[string]string{"token": cancel}, false); w.Code != http.StatusForbidden {
		t.Fatalf("cancel without CSRF: %d %s", w.Code, w.Body.String())
	}
	anon.fetchCSRF()
	apiDo(t, first, "GET", "/api/v1/auth/me", nil, http.StatusOK)
	apiDo(t, second, "GET", "/api/v1/auth/me", nil, http.StatusOK)
	// A different signed-in account can follow the emailed capability without
	// being signed out itself. Neither subject browser gets its epoch renewed.
	apiDo(t, other, "POST", "/api/v1/me/email/cancel", map[string]string{"token": cancel}, http.StatusOK)
	apiDo(t, first, "GET", "/api/v1/auth/me", nil, http.StatusUnauthorized)
	apiDo(t, second, "GET", "/api/v1/auth/me", nil, http.StatusUnauthorized)
	apiDo(t, other, "GET", "/api/v1/auth/me", nil, http.StatusOK)
	var fresh models.User
	if err := app.DB.First(&fresh, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if fresh.Email != user.Email || fresh.SessionVersion != user.SessionVersion+1 {
		t.Fatalf("cancellation changed identity or missed epoch: %+v", fresh)
	}
	assertNoPendingSecurityTokens(t, app, user.ID)
	if auditCount(t, app, audit.EmailChangeCancelled) != 1 {
		t.Fatal("cancellation was not audited")
	}
	apiDo(t, anon, "POST", "/api/v1/me/email/cancel", map[string]string{"token": cancel}, http.StatusBadRequest)
	apiDo(t, anon, "POST", "/api/v1/me/email/verify", map[string]string{"token": verify}, http.StatusBadRequest)
	var again models.User
	if err := app.DB.First(&again, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if again.SessionVersion != fresh.SessionVersion || auditCount(t, app, audit.EmailChangeCancelled) != 1 {
		t.Fatal("replay changed sessions or audit")
	}
}

func TestEmailChangeCancellationCannotUndoVerifiedEmail(t *testing.T) {
	app := testApp(t)
	user := newTestUser(t, app, "verified-cancel", "password1234", models.RoleMember)
	client := loginAPI(t, app, user.Email, "password1234")
	verify, cancel := requestSecurityEmailChange(t, app, client, user, "verified-final@example.com")
	apiDo(t, client, "POST", "/api/v1/me/email/verify", map[string]string{"token": verify}, http.StatusOK)
	apiDo(t, client, "POST", "/api/v1/me/email/cancel", map[string]string{"token": cancel}, http.StatusBadRequest)
	apiDo(t, client, "GET", "/api/v1/auth/me", nil, http.StatusOK)
	var fresh models.User
	if err := app.DB.First(&fresh, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if fresh.Email != "verified-final@example.com" || fresh.SessionVersion != user.SessionVersion+1 || auditCount(t, app, audit.EmailChangeCancelled) != 0 {
		t.Fatalf("cancel undid a completed change: %+v", fresh)
	}
	assertNoPendingSecurityTokens(t, app, user.ID)
}

func TestEmailChangeNewRequestReplacesBothLinks(t *testing.T) {
	app := testApp(t)
	user := newTestUser(t, app, "latest-cancel", "password1234", models.RoleMember)
	client := loginAPI(t, app, user.Email, "password1234")
	oldVerify, oldCancel := requestSecurityEmailChange(t, app, client, user, "latest-first@example.com")
	_, cancel := requestSecurityEmailChange(t, app, client, user, "latest-second@example.com")
	anon := newAPIClient(t, app)
	anon.fetchCSRF()
	apiDo(t, anon, "POST", "/api/v1/me/email/cancel", map[string]string{"token": oldCancel}, http.StatusBadRequest)
	apiDo(t, anon, "POST", "/api/v1/me/email/verify", map[string]string{"token": oldVerify}, http.StatusBadRequest)
	apiDo(t, client, "GET", "/api/v1/auth/me", nil, http.StatusOK)
	apiDo(t, anon, "POST", "/api/v1/me/email/cancel", map[string]string{"token": cancel}, http.StatusOK)
	assertNoPendingSecurityTokens(t, app, user.ID)
}

func TestEmailChangeCancellationAuditFailureRollsBack(t *testing.T) {
	app := testApp(t)
	user := newTestUser(t, app, "cancel-rollback", "password1234", models.RoleMember)
	client := loginAPI(t, app, user.Email, "password1234")
	_, cancel := requestSecurityEmailChange(t, app, client, user, "rollback-new@example.com")
	const callback = "security:cancel-audit-failure"
	if err := app.DB.Callback().Create().Before("gorm:create").Register(callback, func(db *gorm.DB) {
		if db.Statement.Table == "audit_logs" {
			db.AddError(errors.New("injected audit failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.DB.Callback().Create().Remove(callback) })
	apiDo(t, client, "POST", "/api/v1/me/email/cancel", map[string]string{"token": cancel}, http.StatusInternalServerError)
	apiDo(t, client, "GET", "/api/v1/auth/me", nil, http.StatusOK)
	var fresh models.User
	if err := app.DB.First(&fresh, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	var token models.AccountToken
	if err := app.DB.Where("token_hash = ?", hashToken(cancel)).First(&token).Error; err != nil {
		t.Fatal(err)
	}
	if fresh.SessionVersion != user.SessionVersion || fresh.Email != user.Email || token.UsedAt != nil {
		t.Fatal("failed audit left a partial cancellation")
	}
	var pending int64
	if err := app.DB.Model(&models.AccountToken{}).Where("user_id = ? AND used_at IS NULL", user.ID).Count(&pending).Error; err != nil {
		t.Fatal(err)
	}
	if pending != 2 {
		t.Fatalf("failed audit revoked pending pair: %d", pending)
	}
}

func TestEmailChangeVerifyAndCancelHaveOnlyOneWinner(t *testing.T) {
	app := testApp(t)
	user := newTestUser(t, app, "cancel-race", "password1234", models.RoleMember)
	client := loginAPI(t, app, user.Email, "password1234")
	verify, cancel := requestSecurityEmailChange(t, app, client, user, "race-final@example.com")
	requests := []struct {
		client      *apiClient
		path, token string
	}{
		{newAPIClient(t, app), "/api/v1/me/email/verify", verify},
		{newAPIClient(t, app), "/api/v1/me/email/cancel", cancel},
	}
	for _, request := range requests {
		request.client.fetchCSRF()
	}
	start, codes := make(chan struct{}), make(chan int, 2)
	var wg sync.WaitGroup
	for _, request := range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			codes <- request.client.do("POST", request.path, map[string]string{"token": request.token}, true).Code
		}()
	}
	close(start)
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for code := range codes {
		counts[code]++
	}
	if counts[http.StatusOK] != 1 || counts[http.StatusBadRequest] != 1 || len(counts) != 2 {
		t.Fatalf("verify/cancel race: %v", counts)
	}
	var fresh models.User
	if err := app.DB.First(&fresh, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if fresh.SessionVersion != user.SessionVersion+1 {
		t.Fatalf("race epoch: %d", fresh.SessionVersion)
	}
	cancelled := auditCount(t, app, audit.EmailChangeCancelled)
	changed := auditCount(t, app, audit.EmailChanged)
	if cancelled+changed != 1 || (cancelled == 1 && fresh.Email != user.Email) || (changed == 1 && fresh.Email != "race-final@example.com") {
		t.Fatal("race outcome and audit disagree", cancelled, changed, fresh.Email)
	}
	assertNoPendingSecurityTokens(t, app, user.ID)
}
