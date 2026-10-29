package main

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"fold/migrations"
	"fold/models"

	"gorm.io/gorm"
)

func pendingSecurityCapabilities(t *testing.T, app *App, user *models.User) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, kind := range []string{models.TokenPasswordReset, models.TokenEmailVerify, models.TokenEmailChangeCancel, models.TokenAccountSetup, models.TokenTeenConsent} {
		payload := map[string]any(nil)
		if kind == models.TokenEmailVerify {
			payload = map[string]any{"new_email": "pending-takeover@example.com"}
		}
		raw, err := createAccountToken(app.DB, kind, user.ID, payload, tokenTTL)
		if err != nil {
			t.Fatal(err)
		}
		out[kind] = raw
	}
	return out
}

func TestCredentialChangesRevokeEveryPendingAccountToken(t *testing.T) {
	for _, change := range []string{"password_change", "password_reset", "password_setup", "admin_reset", "admin_email", "email_verify", "disable", "delete"} {
		t.Run(change, func(t *testing.T) {
			app := testApp(t)
			user := newTestUser(t, app, "security-subject", "password1234", models.RoleMember)
			client := loginAPI(t, app, user.Email, "password1234")
			newTestUser(t, app, "security-admin", "password1234", models.RoleAdmin)
			admin := loginAPI(t, app, "security-admin", "password1234")
			anon := newAPIClient(t, app)
			anon.fetchCSRF()
			tokens := pendingSecurityCapabilities(t, app, user)
			target := "/api/v1/admin/users/" + itoa(user.ID)
			switch change {
			case "password_change":
				apiDo(t, client, "POST", "/api/v1/me/password", map[string]string{"current": "password1234", "password": "replacement1234"}, http.StatusOK)
			case "password_reset":
				apiDo(t, anon, "POST", "/api/v1/auth/password-reset/confirm", map[string]string{"token": tokens[models.TokenPasswordReset], "password": "replacement1234"}, http.StatusOK)
			case "password_setup":
				apiDo(t, anon, "POST", "/api/v1/account-requests/confirm", map[string]string{"token": tokens[models.TokenAccountSetup], "password": "replacement1234"}, http.StatusOK)
			case "admin_reset":
				apiDo(t, admin, "POST", target+"/password", map[string]string{"password": "replacement1234"}, http.StatusOK)
			case "admin_email":
				apiDo(t, admin, "PUT", target, map[string]string{"email": "admin-corrected@example.com"}, http.StatusOK)
			case "email_verify":
				apiDo(t, client, "POST", "/api/v1/me/email/verify", map[string]string{"token": tokens[models.TokenEmailVerify]}, http.StatusOK)
			case "disable":
				apiDo(t, admin, "PUT", target, map[string]string{"status": models.UserDisabled}, http.StatusOK)
			case "delete":
				apiDo(t, admin, "DELETE", target, nil, http.StatusOK)
			}
			assertNoPendingSecurityTokens(t, app, user.ID)
			var fresh models.User
			if err := app.DB.Unscoped().First(&fresh, user.ID).Error; err != nil {
				t.Fatal(err)
			}
			if fresh.SessionVersion != user.SessionVersion+1 {
				t.Fatalf("%s did not advance credential epoch: %d", change, fresh.SessionVersion)
			}
			if change == "password_change" || change == "password_reset" || change == "password_setup" || change == "admin_reset" {
				if !fresh.CheckPassword("replacement1234") {
					t.Fatal("replacement password was not saved")
				}
			}
			// This is the original takeover: an old verification capability must
			// never survive recovery or an admin's direct email correction.
			apiDo(t, anon, "POST", "/api/v1/me/email/verify", map[string]string{"token": tokens[models.TokenEmailVerify]}, http.StatusBadRequest)
			apiDo(t, anon, "POST", "/api/v1/me/email/cancel", map[string]string{"token": tokens[models.TokenEmailChangeCancel]}, http.StatusBadRequest)
			var after models.User
			if err := app.DB.Unscoped().First(&after, user.ID).Error; err != nil {
				t.Fatal(err)
			}
			if after.Email != fresh.Email || after.SessionVersion != fresh.SessionVersion {
				t.Fatal("a stale capability overwrote recovered identity or sessions")
			}
		})
	}
}

func TestCredentialRevocationFailureRollsBackChange(t *testing.T) {
	for _, change := range []string{"password_change", "password_reset", "admin_email"} {
		t.Run(change, func(t *testing.T) {
			app := testApp(t)
			user := newTestUser(t, app, "revoke-failure", "password1234", models.RoleMember)
			client := loginAPI(t, app, user.Email, "password1234")
			newTestUser(t, app, "revoke-admin", "password1234", models.RoleAdmin)
			admin := loginAPI(t, app, "revoke-admin", "password1234")
			var anon *apiClient
			if change == "password_reset" {
				anon = newAPIClient(t, app)
				anon.fetchCSRF()
			}
			tokens := pendingSecurityCapabilities(t, app, user)
			const callback = "security:revoke-failure"
			if err := app.DB.Callback().Delete().Before("gorm:delete").Register(callback, func(db *gorm.DB) {
				if db.Statement.Table == "account_tokens" {
					db.AddError(errors.New("injected token revocation failure"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { app.DB.Callback().Delete().Remove(callback) })
			switch change {
			case "password_change":
				apiDo(t, client, "POST", "/api/v1/me/password", map[string]string{"current": "password1234", "password": "replacement1234"}, http.StatusInternalServerError)
			case "password_reset":
				apiDo(t, anon, "POST", "/api/v1/auth/password-reset/confirm", map[string]string{"token": tokens[models.TokenPasswordReset], "password": "replacement1234"}, http.StatusInternalServerError)
			case "admin_email":
				apiDo(t, admin, "PUT", "/api/v1/admin/users/"+itoa(user.ID), map[string]string{"email": "should-not-save@example.com"}, http.StatusInternalServerError)
			}
			var fresh models.User
			if err := app.DB.First(&fresh, user.ID).Error; err != nil {
				t.Fatal(err)
			}
			if fresh.Email != user.Email || fresh.SessionVersion != user.SessionVersion || !fresh.CheckPassword("password1234") {
				t.Fatal("failed revocation left a partial credential change")
			}
			var pending int64
			if err := app.DB.Model(&models.AccountToken{}).Where("user_id = ? AND used_at IS NULL", user.ID).Count(&pending).Error; err != nil {
				t.Fatal(err)
			}
			if pending != int64(len(tokens)) {
				t.Fatalf("rollback lost pending capabilities: %d", pending)
			}
		})
	}
}

func TestCurrentCredentialProofRejectsStaleEpochOrHash(t *testing.T) {
	for _, changed := range []string{"epoch", "hash", "disabled"} {
		t.Run(changed, func(t *testing.T) {
			app := testApp(t)
			expected := newTestUser(t, app, "stale-proof", "password1234", models.RoleMember)
			if !expected.CheckPassword("password1234") {
				t.Fatal("fixture password proof failed")
			}
			updates := map[string]any{}
			switch changed {
			case "epoch":
				updates["session_version"] = expected.SessionVersion + 1
			case "hash":
				var prepared models.User
				if err := prepared.SetPassword("replacement1234"); err != nil {
					t.Fatal(err)
				}
				updates["password_hash"] = *prepared.PasswordHash
			case "disabled":
				updates["status"] = models.UserDisabled
			}
			// Do not mutate the expected snapshot: it represents the request's
			// authenticated user and already-completed password comparison.
			if err := app.DB.Model(&models.User{}).Where("id = ?", expected.ID).Updates(updates).Error; err != nil {
				t.Fatal(err)
			}
			err := app.DB.Transaction(func(tx *gorm.DB) error {
				_, err := lockCurrentCredentials(tx, expected)
				return err
			})
			if !errors.Is(err, errSecuritySession) {
				t.Fatalf("stale %s proof accepted: %v", changed, err)
			}
		})
	}
}

func TestCredentialTokenMigrationRevokesUnsafePendingLinks(t *testing.T) {
	app := testApp(t)
	sqlDB, err := app.DB.DB()
	if err != nil {
		t.Fatal(err)
	}
	provider, err := migrations.NewProvider(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := provider.Up(context.Background()); err != nil {
			t.Errorf("restore latest migrations: %v", err)
		}
	})
	if _, err := provider.DownTo(context.Background(), 39); err != nil {
		t.Fatal(err)
	}

	user := newTestUser(t, app, "token-migration", "password1234", models.RoleMember)
	usedAt := time.Now().UTC()
	expiresAt := usedAt.Add(time.Hour)
	create := func(kind, raw, payload string, used *time.Time) string {
		t.Helper()
		hash := hashToken(raw)
		if err := app.DB.Create(&models.AccountToken{Kind: kind, UserID: user.ID, TokenHash: hash,
			Payload: payload, ExpiresAt: expiresAt, UsedAt: used}).Error; err != nil {
			t.Fatal(err)
		}
		return hash
	}
	exists := func(hash string) bool {
		t.Helper()
		var count int64
		if err := app.DB.Model(&models.AccountToken{}).Where("token_hash = ?", hash).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		return count == 1
	}

	pendingVerify := create(models.TokenEmailVerify, "migration-pending-verify", `{"new_email":"unsafe@example.com"}`, nil)
	usedVerify := create(models.TokenEmailVerify, "migration-used-verify", `{"new_email":"history@example.com"}`, &usedAt)
	pendingReset := create(models.TokenPasswordReset, "migration-pending-reset", `{}`, nil)
	if _, err := provider.UpTo(context.Background(), 40); err != nil {
		t.Fatal(err)
	}
	if exists(pendingVerify) || !exists(usedVerify) || !exists(pendingReset) {
		t.Fatal("migration 40 did not revoke only the unsafe legacy verification link")
	}

	pendingCancel := create(models.TokenEmailChangeCancel, "migration-pending-cancel", `{}`, nil)
	usedCancel := create(models.TokenEmailChangeCancel, "migration-used-cancel", `{}`, &usedAt)
	rollbackVerify := create(models.TokenEmailVerify, "migration-rollback-verify", `{"new_email":"rollback@example.com"}`, nil)
	if _, err := provider.DownTo(context.Background(), 39); err != nil {
		t.Fatal(err)
	}
	if exists(pendingCancel) || exists(usedCancel) || exists(rollbackVerify) || !exists(usedVerify) || !exists(pendingReset) {
		t.Fatal("migration 40 rollback did not preserve only safe token history")
	}
	if version, err := provider.GetDBVersion(context.Background()); err != nil || version != 39 {
		t.Fatalf("version after migration rollback=%d err=%v, want 39", version, err)
	}
}
