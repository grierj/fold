package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"fold/internal/audit"
	"fold/models"
)

func guardianEmailSetup(t *testing.T) (*App, *models.User, *models.User, *models.GuardianLink, *apiClient, string) {
	t.Helper()
	app := testApp(t)
	app.Config.SessionSecret = []byte("test-guardian-email-signing-key-32bytes")
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	guardian := newTestUser(t, app, "guardian", "pw", models.RoleMember)
	teen := guardianTestTeen(t, app, "teen")
	client := loginAPI(t, app, "guardian", "pw")
	apiDo(t, client, "POST", "/api/v1/me/guardian-claims", map[string]string{"email": teen.Email}, 200)
	var link models.GuardianLink
	if err := app.DB.Where("teen_user_id = ?", teen.ID).First(&link).Error; err != nil {
		t.Fatal(err)
	}
	// Pending links have no power, and reveal no teen in the guardian list.
	apiDo(t, client, "POST", guardianConsentPath(link.ID), map[string]string{"scope": "account", "status": "granted"}, 403)
	if body := apiDo(t, client, "GET", "/api/v1/me/guardian-teens", nil, 200); body != `{"teens":[]}` {
		t.Fatalf("pending claim disclosed teen: %s", body)
	}
	admin := loginAPI(t, app, "admin", "pw")
	apiDo(t, admin, "POST", "/api/v1/admin/guardian-links/"+itoa(link.ID)+"/confirm", map[string]string{"checkNote": "known family"}, 200)
	return app, guardian, teen, &link, client, guardianEmailRaw(t, app, guardian.Email)
}

func guardianConsentPath(linkID uint) string {
	return "/api/v1/me/guardian-links/" + itoa(linkID) + "/consents"
}

func guardianEmailRaw(t *testing.T, app *App, email string) string {
	t.Helper()
	mail := latestEmail(t, app, email)
	match := regexp.MustCompile(`https?://[^\s]+/guardian-consent\?token=[^\s]+`).FindString(mail.Body)
	parsed, err := url.Parse(match)
	if err != nil || parsed.Query().Get("token") == "" {
		t.Fatalf("missing consent URL: %s", mail.Body)
	}
	return parsed.Query().Get("token")
}

func TestGuardianEmailPreviewAndExplicitSingleUseGrant(t *testing.T) {
	app, guardian, teen, link, client, raw := guardianEmailSetup(t)
	anon := newAPIClient(t, app)
	for range 2 {
		body := apiDo(t, anon, "GET", "/api/v1/guardian-consent?token="+url.QueryEscape(raw), nil, 200)
		if !strings.Contains(body, teen.PublicName()) || strings.Contains(body, guardian.Email) {
			t.Fatalf("preview: %s", body)
		}
	}
	var count int64
	app.DB.Model(&models.TeenConsent{}).Count(&count)
	if count != 0 {
		t.Fatal("GET preview gave consent")
	}
	var token models.AccountToken
	app.DB.Where("kind = ?", models.TokenTeenConsent).First(&token)
	if token.UsedAt != nil || token.TokenHash == raw || strings.Contains(token.Payload, raw) {
		t.Fatal("token was consumed or stored raw")
	}
	// The explicit POST still needs CSRF even though the email is authority.
	w := anon.do("POST", "/api/v1/guardian-consent", map[string]string{"token": raw}, false)
	if w.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF: %d", w.Code)
	}
	anon.fetchCSRF()
	apiDo(t, anon, "POST", "/api/v1/guardian-consent", map[string]string{"token": raw}, 200)
	apiDo(t, anon, "POST", "/api/v1/guardian-consent", map[string]string{"token": raw}, 400)
	app.DB.First(teen, teen.ID)
	if teen.Status != models.UserActive {
		t.Fatalf("email grant status: %s", teen.Status)
	}
	var consent models.TeenConsent
	app.DB.First(&consent)
	if consent.Scope != models.TeenConsentAccount || consent.Status != models.ConsentGranted || consent.GrantedBy != guardian.ID || consent.GranterRole != models.ConsentGuardian {
		t.Fatalf("consent: %+v", consent)
	}
	for _, scope := range []string{"sms", "photos"} {
		granted, err := latestTeenConsentIn(app.DB, teen.ID, scope)
		if err != nil || granted {
			t.Fatalf("email gave %s consent", scope)
		}
	}
	rows, _ := auditRows(t, app, audit.TeenConsentGranted)
	for _, row := range rows {
		if strings.Contains(row.MetaJSON, raw) {
			t.Fatal("token in audit")
		}
	}
	// Every scope can be managed by a confirmed guardian, and revocation ends
	// the teen's authenticated session.
	teenClient := loginAPI(t, app, "teen", "password1234")
	for _, scope := range []string{"sms", "photos"} {
		apiDo(t, client, "POST", guardianConsentPath(link.ID), map[string]string{"scope": scope, "status": "granted"}, 200)
	}
	apiDo(t, client, "POST", guardianConsentPath(link.ID), map[string]string{"scope": "account", "status": "revoked"}, 200)
	apiDo(t, teenClient, "GET", "/api/v1/auth/me", nil, 401)
	apiDo(t, client, "POST", guardianConsentPath(link.ID), map[string]string{"scope": "account", "status": "granted"}, 200)
}

func TestGuardianEmailRejectsStaleAuthority(t *testing.T) {
	cases := []struct {
		name   string
		change func(*testing.T, *App, *models.User, *models.User, *models.GuardianLink, *apiClient)
	}{
		{"guardian email changed", func(t *testing.T, app *App, g, _ *models.User, _ *models.GuardianLink, _ *apiClient) {
			app.DB.Model(g).Update("email", "changed@example.com")
		}},
		{"guardian identity version changed", func(t *testing.T, app *App, g, _ *models.User, _ *models.GuardianLink, _ *apiClient) {
			app.DB.Model(g).Update("session_version", g.SessionVersion+1)
		}},
		{"guardian disabled", func(t *testing.T, app *App, g, _ *models.User, _ *models.GuardianLink, _ *apiClient) {
			app.DB.Model(g).Update("status", models.UserDisabled)
		}},
		{"guardian deleted", func(t *testing.T, app *App, g, _ *models.User, _ *models.GuardianLink, _ *apiClient) {
			app.DB.Delete(g)
		}},
		{"guardian is teen", func(t *testing.T, app *App, g, _ *models.User, _ *models.GuardianLink, _ *apiClient) {
			app.DB.Model(g).Update("account_type", models.AccountTeen)
		}},
		{"link revoked", func(t *testing.T, app *App, _, _ *models.User, l *models.GuardianLink, _ *apiClient) {
			app.DB.Model(l).Update("status", models.GuardianRevoked)
		}},
		{"link pending", func(t *testing.T, app *App, _, _ *models.User, l *models.GuardianLink, _ *apiClient) {
			app.DB.Model(l).Update("status", models.GuardianPending)
		}},
		{"teen became adult", func(t *testing.T, app *App, _, teen *models.User, _ *models.GuardianLink, _ *apiClient) {
			app.DB.Model(teen).Update("account_type", models.AccountAdult)
		}},
		{"teen deleted", func(t *testing.T, app *App, _, teen *models.User, _ *models.GuardianLink, _ *apiClient) {
			app.DB.Delete(teen)
		}},
		{"consent changed after email", func(t *testing.T, app *App, _, _ *models.User, l *models.GuardianLink, c *apiClient) {
			apiDo(t, c, "POST", guardianConsentPath(l.ID), map[string]string{"scope": "account", "status": "revoked"}, 200)
		}},
		{"expired token", func(t *testing.T, app *App, _, _ *models.User, _ *models.GuardianLink, _ *apiClient) {
			app.DB.Model(&models.AccountToken{}).Where("kind = ?", models.TokenTeenConsent).Update("expires_at", time.Now().Add(-time.Minute))
		}},
		{"tampered signed payload", func(t *testing.T, app *App, _, _ *models.User, _ *models.GuardianLink, _ *apiClient) {
			app.DB.Exec("UPDATE account_tokens SET payload = jsonb_set(payload, '{consentId}', '999') WHERE kind = ?", models.TokenTeenConsent)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, guardian, teen, link, client, raw := guardianEmailSetup(t)
			tc.change(t, app, guardian, teen, link, client)
			anon := newAPIClient(t, app)
			anon.fetchCSRF()
			apiDo(t, anon, "GET", "/api/v1/guardian-consent?token="+url.QueryEscape(raw), nil, 400)
			apiDo(t, anon, "POST", "/api/v1/guardian-consent", map[string]string{"token": raw}, 400)
			if auditCount(t, app, audit.TeenConsentGranted) != 0 {
				t.Fatal("stale email granted consent")
			}
		})
	}
}

func TestGuardianEmailPerLinkReplacementAndConcurrentUse(t *testing.T) {
	app, guardian, _, first, _, rawFirst := guardianEmailSetup(t)
	secondTeen := guardianTestTeen(t, app, "second-teen")
	second := models.GuardianLink{GuardianUserID: guardian.ID, TeenUserID: &secondTeen.ID, Status: models.GuardianConfirmed}
	if err := app.DB.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.sendGuardianConsentEmail(second.ID); err != nil {
		t.Fatal(err)
	}
	rawSecond := guardianEmailRaw(t, app, guardian.Email)
	anon := newAPIClient(t, app)
	anon.fetchCSRF()
	apiDo(t, anon, "GET", "/api/v1/guardian-consent?token="+url.QueryEscape(rawFirst), nil, 200)
	apiDo(t, anon, "POST", "/api/v1/guardian-consent", map[string]string{"token": rawSecond}, 200)
	// A resend replaces this teen's old email without replacing the other teen.
	if err := app.sendGuardianConsentEmail(first.ID); err != nil {
		t.Fatal(err)
	}
	fresh := guardianEmailRaw(t, app, guardian.Email)
	apiDo(t, anon, "POST", "/api/v1/guardian-consent", map[string]string{"token": rawFirst}, 400)
	apiDo(t, anon, "POST", "/api/v1/guardian-consent", map[string]string{"token": fresh[:len(fresh)-1] + "!"}, 400)
	clients := []*apiClient{newAPIClient(t, app), newAPIClient(t, app)}
	for _, c := range clients {
		c.fetchCSRF()
	}
	statuses := make(chan int, 2)
	var wg sync.WaitGroup
	for _, c := range clients {
		wg.Go(func() {
			statuses <- c.do("POST", "/api/v1/guardian-consent", map[string]string{"token": fresh}, true).Code
		})
	}
	wg.Wait()
	close(statuses)
	var successes, rejected int
	for code := range statuses {
		switch code {
		case 200:
			successes++
		case 400:
			rejected++
		default:
			t.Fatalf("concurrent use: %d", code)
		}
	}
	if successes != 1 || rejected != 1 {
		t.Fatalf("use outcomes: %d/%d", successes, rejected)
	}
	var rows []models.TeenConsent
	app.DB.Find(&rows)
	if len(rows) != 2 {
		t.Fatalf("two teen consents expected, got %d", len(rows))
	}
}

func TestOnlyConfirmedGuardianCanManageTeenConsent(t *testing.T) {
	app, _, teen, link, guardian, _ := guardianEmailSetup(t)
	newTestUser(t, app, "leader", "pw", models.RoleMember)
	leader := loginAPI(t, app, "leader", "pw")
	apiDo(t, leader, "POST", guardianConsentPath(link.ID), map[string]string{"scope": "account", "status": "granted"}, 403)
	apiDo(t, leader, "POST", consentPath(teen), map[string]string{"scope": "account", "status": "granted"}, 403)
	apiDo(t, guardian, "POST", guardianConsentPath(link.ID), map[string]string{"scope": "other", "status": "granted"}, 400)
	app.DB.Model(link).Update("status", models.GuardianRevoked)
	apiDo(t, guardian, "POST", guardianConsentPath(link.ID), map[string]string{"scope": "account", "status": "granted"}, 403)
	var response struct {
		Teens []guardianTeenJSON `json:"teens"`
	}
	if err := json.Unmarshal([]byte(apiDo(t, guardian, "GET", "/api/v1/me/guardian-teens", nil, 200)), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Teens) != 0 {
		t.Fatal("revoked link still listed")
	}
}

func TestAdminGuardianUsesConfirmedGuardianCapacity(t *testing.T) {
	app, guardian, teen, link, client, _ := guardianEmailSetup(t)
	if err := app.DB.Model(guardian).Update("role", models.RoleAdmin).Error; err != nil {
		t.Fatal(err)
	}
	apiDo(t, client, "POST", guardianConsentPath(link.ID), map[string]string{"scope": "account", "status": "granted"}, 200)
	var row models.TeenConsent
	app.DB.Where("teen_user_id = ?", teen.ID).First(&row)
	if row.GranterRole != models.ConsentGuardian {
		t.Fatalf("guardian acting as %s", row.GranterRole)
	}
}
