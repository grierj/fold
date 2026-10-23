package main

import (
	"encoding/json"
	"strings"
	"testing"

	"fold/internal/audit"
	"fold/internal/notify"
	"fold/models"
)

func consentTeen(t *testing.T, app *App) *models.User {
	t.Helper()
	teen := newTestUser(t, app, "consent-teen", "password1234", models.RoleMember)
	if err := app.DB.Model(teen).Updates(map[string]any{"account_type": models.AccountTeen, "status": models.UserPendingConsent}).Error; err != nil {
		t.Fatal(err)
	}
	return teen
}

func consentPath(teen *models.User) string {
	return "/api/v1/admin/users/" + itoa2(teen.ID) + "/teen-consents"
}

func TestAdminTeenConsentScopesAndHistory(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	newTestUser(t, app, "other-admin", "pw", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "pw")
	teen := consentTeen(t, app)
	path := consentPath(teen)
	apiDo(t, admin, "POST", path, map[string]string{"scope": "account", "status": "granted"}, 400)
	apiDo(t, admin, "POST", path, map[string]string{"scope": "other", "status": "granted", "evidenceText": "paper"}, 400)
	apiDo(t, admin, "POST", path, map[string]string{"scope": "sms", "status": "granted", "evidenceText": strings.Repeat("a", 1001)}, 400)
	for _, scope := range []string{"sms", "photos", "account"} {
		apiDo(t, admin, "POST", path, map[string]string{"scope": scope, "status": "granted", "evidenceText": " Signed paper form on file "}, 200)
		app.DB.First(teen, teen.ID)
		want := models.UserPendingConsent
		if scope == "account" {
			want = models.UserActive
		}
		if teen.Status != want {
			t.Fatalf("after %s grant: %s", scope, teen.Status)
		}
	}
	teenSession := loginAPI(t, app, "consent-teen", "password1234")
	apiDo(t, admin, "POST", path, map[string]string{"scope": "account", "status": "revoked"}, 200)
	apiDo(t, teenSession, "GET", "/api/v1/auth/me", nil, 401)
	app.DB.First(teen, teen.ID)
	if teen.Status != models.UserDisabled || teen.AdministrativelyDisabled {
		t.Fatalf("consent revocation: %+v", teen)
	}
	for _, scope := range []string{"sms", "photos"} {
		granted, err := latestTeenConsentIn(app.DB, teen.ID, scope)
		if err != nil || !granted {
			t.Fatalf("scope %s lost: %t %v", scope, granted, err)
		}
	}
	var response struct {
		Consents []teenConsentJSON `json:"consents"`
	}
	if err := json.Unmarshal([]byte(apiDo(t, admin, "GET", path, nil, 200)), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Consents) != 4 || response.Consents[0].Status != "revoked" || response.Consents[3].EvidenceText != "Signed paper form on file" {
		t.Fatalf("history: %+v", response.Consents)
	}
	rows, _ := auditRows(t, app, audit.TeenConsentGranted)
	if len(rows) != 3 {
		t.Fatalf("grant audit count %d", len(rows))
	}
	var notices []models.Notification
	app.DB.Where("kind = ?", notify.TeenConsentChanged).Find(&notices)
	if len(notices) < 4 {
		t.Fatalf("consent notices: %d", len(notices))
	}
	for _, notice := range notices {
		if strings.Contains(notice.EmailBody, teen.PublicName()) || strings.Contains(notice.Body, "paper") || strings.Contains(notice.EmailBody, "paper") {
			t.Fatalf("sensitive evidence in notification: %+v", notice)
		}
	}
}

func TestTeenConsentPreservesAdministrativeSuspension(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "pw")
	teen := consentTeen(t, app)
	path := "/api/v1/admin/users/" + itoa2(teen.ID)
	change := func(status string) {
		apiDo(t, admin, "POST", consentPath(teen), map[string]string{"scope": "account", "status": status, "evidenceText": "Paper form"}, 200)
	}
	change("granted")
	apiDo(t, admin, "PUT", path, map[string]string{"status": "disabled"}, 200)
	change("revoked")
	change("granted")
	app.DB.First(teen, teen.ID)
	if teen.Status != models.UserDisabled || !teen.AdministrativelyDisabled {
		t.Fatalf("grant lifted suspension: %+v", teen)
	}
	apiDo(t, admin, "PUT", path, map[string]string{"status": "active"}, 200)
	app.DB.First(teen, teen.ID)
	if teen.Status != models.UserActive || teen.AdministrativelyDisabled {
		t.Fatalf("enable with valid consent: %+v", teen)
	}
	change("revoked")
	// An admin can independently suspend an already consent-disabled teen.
	apiDo(t, admin, "PUT", path, map[string]string{"status": "disabled"}, 200)
	change("granted")
	app.DB.First(teen, teen.ID)
	if !teen.AdministrativelyDisabled || teen.Status != models.UserDisabled {
		t.Fatal("suspension on consent-disabled teen was lost")
	}
	change("revoked")
	apiDo(t, admin, "PUT", path, map[string]string{"status": "active"}, 200)
	app.DB.First(teen, teen.ID)
	if teen.Status != models.UserPendingConsent || teen.AdministrativelyDisabled {
		t.Fatalf("enable without valid consent: %+v", teen)
	}
}
