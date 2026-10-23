package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"fold/internal/audit"
	"fold/internal/notify"
	"fold/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func consentTeen(t *testing.T, app *App) *models.User {
	t.Helper()
	teen := newTestUser(t, app, "consent-teen", "password1234", models.RoleMember)
	if err := app.DB.Model(teen).Updates(map[string]any{"account_type": models.AccountTeen, "status": models.UserPendingConsent}).Error; err != nil {
		t.Fatal(err)
	}
	return teen
}

func TestTeenConsentInitialGrantKeepsExistingPlaces(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "Consent capacity")
	eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(1), true)
	teen := consentTeen(t, app)
	var occurrence models.EventOccurrence
	app.DB.Where("event_id = ?", eventID).First(&occurrence)
	groupID := uint(0)
	if _, err := fmt.Sscan(gid, &groupID); err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Model(&models.Group{}).Where("id = ?", groupID).Update("member_limit", 2).Error; err != nil {
		t.Fatal(err)
	}
	m := models.Membership{GroupID: groupID, UserID: teen.ID, Role: models.MembershipMember, Status: "active", JoinedAt: time.Now()}
	if err := app.DB.Create(&m).Error; err != nil {
		t.Fatal(err)
	}
	r := models.Registration{OccurrenceID: occurrence.ID, UserID: &teen.ID, Status: models.RegGoing, AttendanceMode: models.AttendanceInPerson}
	if err := app.DB.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	apiDo(t, admin, "POST", consentPath(teen), map[string]string{"scope": "account", "status": "granted", "evidenceText": "Paper form"}, 200)
	var count int64
	app.DB.Model(&models.Membership{}).Where("id = ?", m.ID).Count(&count)
	if count != 1 || regStatus(t, app, teen.ID, occurrence.ID) != models.RegGoing {
		t.Fatal("initial grant dropped an already-listed teen's place")
	}
	if rows, _ := auditRows(t, app, audit.MemberRestored); len(rows) != 0 {
		t.Fatal("initial grant restored already-listed membership")
	}
	if rows, _ := auditRows(t, app, audit.RegistrationRestored); len(rows) != 0 {
		t.Fatal("initial grant restored already-listed registration")
	}
	newTestUser(t, app, "consent-waiter", "pw", models.RoleMember)
	waiter := loginAPI(t, app, "consent-waiter", "pw")
	if code, body := registerAs(waiter, eventID, date, "going", nil); code != 200 {
		t.Fatalf("waiter: %d %s", code, body)
	}
	apiDo(t, admin, "POST", consentPath(teen), map[string]string{"scope": "account", "status": "revoked"}, 200)
	var waitingUser models.User
	app.DB.Where("email = ?", testEmail("consent-waiter")).First(&waitingUser)
	if regStatus(t, app, waitingUser.ID, occurrence.ID) != models.RegGoing {
		t.Fatal("revocation failed to free seat and promote waiter")
	}
	apiDo(t, admin, "POST", consentPath(teen), map[string]string{"scope": "account", "status": "granted", "evidenceText": "Renewed form"}, 200)
	app.DB.Model(&models.Registration{}).Where("id = ?", r.ID).Count(&count)
	if count != 0 {
		t.Fatal("renewed consent overfilled the event")
	}
	var noticeCount int64
	app.DB.Model(&models.Notification{}).Where("user_id = ? AND kind = ?", teen.ID, notify.RegistrationNotRestored).Count(&noticeCount)
	if noticeCount != 1 {
		t.Fatal("missing ask-a-leader notice for non-restored place")
	}
}

func TestTeenConsentAuthorityAndAtomicRollback(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	adult := newTestUser(t, app, "member", "pw", models.RoleMember)
	admin := loginAPI(t, app, "admin", "pw")
	member := loginAPI(t, app, "member", "pw")
	teen := consentTeen(t, app)
	apiDo(t, member, "POST", consentPath(teen), map[string]string{"scope": "account", "status": "granted", "evidenceText": "Paper"}, 403)
	apiDo(t, member, "GET", consentPath(teen), nil, 403)
	apiDo(t, admin, "POST", consentPath(adult), map[string]string{"scope": "account", "status": "granted", "evidenceText": "Paper"}, 409)
	// Force notification storage to fail after the ledger/status/audit writes.
	if err := app.DB.Exec(`ALTER TABLE notifications ADD CONSTRAINT reject_consent_notice CHECK (kind <> 'teen_consent.changed')`).Error; err != nil {
		t.Fatal(err)
	}
	apiDo(t, admin, "POST", consentPath(teen), map[string]string{"scope": "account", "status": "granted", "evidenceText": "Paper"}, 500)
	if err := app.DB.Exec(`ALTER TABLE notifications DROP CONSTRAINT reject_consent_notice`).Error; err != nil {
		t.Fatal(err)
	}
	app.DB.First(teen, teen.ID)
	if teen.Status != models.UserPendingConsent {
		t.Fatal("failed notification committed account activation")
	}
	var count int64
	app.DB.Model(&models.TeenConsent{}).Count(&count)
	if count != 0 {
		t.Fatal("failed notification committed consent history")
	}
	if rows, _ := auditRows(t, app, audit.TeenConsentGranted); len(rows) != 0 {
		t.Fatal("failed consent committed audit")
	}
}

func TestTeenConsentAdminGuardianNoticeAndAdultHistory(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	other := newTestUser(t, app, "other-admin", "pw", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "pw")
	teen := consentTeen(t, app)
	if err := app.DB.Create(&models.GuardianLink{TeenUserID: &teen.ID, GuardianUserID: other.ID, Status: models.GuardianConfirmed}).Error; err != nil {
		t.Fatal(err)
	}
	apiDo(t, admin, "POST", consentPath(teen), map[string]string{"scope": "account", "status": "granted", "evidenceText": "Paper"}, 200)
	var count int64
	app.DB.Model(&models.Notification{}).Where("user_id = ? AND kind = ?", other.ID, notify.TeenConsentChanged).Count(&count)
	if count != 1 {
		t.Fatalf("admin guardian notices = %d, want one", count)
	}
	if err := app.DB.Model(teen).Update("account_type", models.AccountAdult).Error; err != nil {
		t.Fatal(err)
	}
	apiDo(t, admin, "GET", consentPath(teen), nil, 200)
	apiDo(t, admin, "POST", consentPath(teen), map[string]string{"scope": "account", "status": "revoked"}, 409)
	var users struct {
		Users []userJSON `json:"users"`
	}
	decodeJSON(t, admin.do("GET", "/api/v1/admin/users", nil, false), &users)
	for _, user := range users.Users {
		if user.ID == teen.ID && !user.HasTeenConsentHistory {
			t.Fatal("adult history has no entry in admin list")
		}
	}
}

func waitForBlockedConsent(t *testing.T, app *App) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var count int64
		if err := app.DB.Raw("SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock' AND state = 'active' AND query LIKE '%users%'").Scan(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("consent request never waited on the lifecycle lock")
}

func TestTeenEnableReadsConsentAfterLifecycleLock(t *testing.T) {
	app := testApp(t)
	adminUser := newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "pw")
	teen := consentTeen(t, app)
	apiDo(t, admin, "POST", consentPath(teen), map[string]string{"scope": "account", "status": "granted", "evidenceText": "Paper"}, 200)
	apiDo(t, admin, "PUT", "/api/v1/admin/users/"+itoa2(teen.ID), map[string]string{"status": "disabled"}, 200)
	tx := app.DB.Begin()
	defer tx.Rollback()
	if err := tx.Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).First(&models.User{}, teen.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Create(&models.TeenConsent{TeenUserID: teen.ID, Scope: "account", Status: "revoked", GrantedBy: adminUser.ID, GranterRole: "admin"}).Error; err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() {
		done <- admin.do("PUT", "/api/v1/admin/users/"+itoa2(teen.ID), map[string]string{"status": "active"}, true).Code
	}()
	waitForBlockedConsent(t, app)
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 200 {
			t.Fatalf("enable: %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("enable deadlocked")
	}
	app.DB.First(teen, teen.ID)
	if teen.Status != models.UserPendingConsent {
		t.Fatalf("stale consent activated teen: %s", teen.Status)
	}
}

func TestTeenConsentRechecksAdminUnderLock(t *testing.T) {
	app := testApp(t)
	adminUser := newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "pw")
	teen := consentTeen(t, app)
	tx := app.DB.Begin()
	defer tx.Rollback()
	if err := tx.Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).First(&models.User{}, adminUser.ID).Error; err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() {
		done <- admin.do("POST", consentPath(teen), map[string]string{"scope": "account", "status": "granted", "evidenceText": "Paper"}, true).Code
	}()
	waitForBlockedConsent(t, app)
	if err := tx.Model(&models.User{}).Where("id = ?", adminUser.ID).Updates(map[string]any{"role": models.RoleMember, "session_version": gorm.Expr("session_version + 1")}).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 403 {
			t.Fatalf("stale admin consent: %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("grant deadlocked")
	}
	var count int64
	app.DB.Model(&models.TeenConsent{}).Count(&count)
	if count != 0 {
		t.Fatal("former admin granted consent")
	}
}

func TestApproveTeenAndGrantConsentAtomically(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "pw")
	anon := anonClient(t, app, "10.4.9.1")
	apiDo(t, anon, "POST", "/api/v1/account-requests", signupBody("Teen Casey", "casey@example.com", "teen", teenBirthDate()), 200)
	req := pendingRequest(t, app, "casey@example.com")
	path := "/api/v1/admin/account-requests/" + itoa2(req.ID) + "/approve"
	apiDo(t, admin, "POST", path, map[string]string{"accountConsentEvidenceText": " "}, 400)
	app.DB.First(&req, req.ID)
	if req.Status != models.RequestPending {
		t.Fatal("invalid consent committed approval")
	}
	var count int64
	app.DB.Model(&models.User{}).Where("email = ?", req.Email).Count(&count)
	if count != 0 {
		t.Fatal("invalid consent created account")
	}
	apiDo(t, admin, "POST", path, map[string]string{"accountConsentEvidenceText": "Paper form signed today"}, 200)
	var teen models.User
	app.DB.Where("email = ?", req.Email).First(&teen)
	if teen.Status != models.UserActive || teen.PasswordHash != nil {
		t.Fatalf("approved+consented account: %+v", teen)
	}
	if loginAPIExpectFail(t, app, "casey", "password1234") {
		t.Fatal("passwordless account signed in")
	}
	if granted, err := latestTeenConsentIn(app.DB, teen.ID, "account"); err != nil || !granted {
		t.Fatalf("missing consent: %t %v", granted, err)
	}
	message := latestEmail(t, app, req.Email)
	if strings.Contains(message.Body, "also needs a parent") {
		t.Fatal("consented approval email still asks for consent")
	}
	token := tokenInEmail(t, message)
	apiDo(t, anon, "POST", "/api/v1/account-requests/confirm", map[string]string{"token": token, "password": "password1234"}, 200)
	app.DB.First(&teen, teen.ID)
	if teen.Status != models.UserActive || teen.PasswordHash == nil {
		t.Fatal("setup overwrote consent state")
	}
	apiDo(t, anon, "GET", "/api/v1/auth/me", nil, 200)
}

func TestApproveTeenConsentNeverChangesExistingAccount(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "pw")
	anon := anonClient(t, app, "10.4.9.2")
	apiDo(t, anon, "POST", "/api/v1/account-requests", signupBody("Teen Another", "another@example.com", "teen", teenBirthDate()), 200)
	req := pendingRequest(t, app, "another@example.com")
	existing := newTestUser(t, app, "another", "pw", models.RoleMember)
	path := "/api/v1/admin/account-requests/" + itoa2(req.ID) + "/approve"
	apiDo(t, admin, "POST", path, map[string]string{"accountConsentEvidenceText": "Paper"}, 409)
	app.DB.First(&req, req.ID)
	app.DB.First(existing, existing.ID)
	if req.Status != models.RequestPending || existing.AccountType != models.AccountAdult {
		t.Fatal("existing account or request changed on conflicting consent approval")
	}
	var count int64
	app.DB.Model(&models.TeenConsent{}).Count(&count)
	if count != 0 {
		t.Fatal("grant appended to unrelated existing account")
	}
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
