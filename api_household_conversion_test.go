package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"fold/internal/audit"
	"fold/internal/mail"
	"fold/internal/notify"
	"fold/models"
)

func conversionPath(id uint) string { return "/api/v1/me/household/people/" + itoa(id) + "/convert" }
func inviteHouseholdPerson(t *testing.T, app *App, c *apiClient, id uint, email string) string {
	t.Helper()
	apiDo(t, c, "POST", conversionPath(id), map[string]any{"email": email}, http.StatusCreated)
	return tokenInEmail(t, latestEmail(t, app, email))
}
func acceptConversion(t *testing.T, app *App, token string, status int) *apiClient {
	t.Helper()
	anon := newAPIClient(t, app)
	anon.fetchCSRF()
	apiDo(t, anon, "POST", "/api/v1/invitations/accept", map[string]any{"token": token, "password": "password1234"}, status)
	return anon
}

func TestHouseholdConversionTransfersEveryRow(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "Conversion")
	holder, c, person, _ := householdFamily(t, app, admin, "convertparent")
	eventID, _ := seedCappedEvent(t, app, admin, gid, intPtr(30), true)
	var original []models.Registration
	now := time.Now().UTC().Truncate(time.Second)
	for i, status := range []string{models.RegGoing, models.RegMaybe, models.RegWaitlisted, models.RegCancelled, models.RegDeclined, models.RegRemoved} {
		for j, days := range []int{-100, 100} {
			start := now.AddDate(0, 0, days+i)
			occurrence := models.EventOccurrence{EventID: eventID, OriginalDate: start.Format("2006-01-02"), StartsAt: start, EndsAt: start.Add(time.Hour), Status: models.OccurrenceScheduled}
			if j == 0 {
				occurrence.Status = models.OccurrenceCancelled
			}
			if err := app.DB.Create(&occurrence).Error; err != nil {
				t.Fatal(err)
			}
			row := models.Registration{OccurrenceID: occurrence.ID, HouseholdPersonID: &person.ID, RegisteredBy: &holder.ID, Status: status, AttendanceMode: models.AttendanceInPerson, Source: "admin", ChildcareRequested: j == 0, CreatedAt: now.Add(-time.Hour), UpdatedAt: now, WaitlistedAt: &now, RemovedBy: &holder.ID, RemovalNote: "Kept history"}
			if err := app.DB.Create(&row).Error; err != nil {
				t.Fatal(err)
			}
			original = append(original, row)
		}
	}
	if err := app.DB.Where("household_person_id = ?", person.ID).Order("id").Find(&original).Error; err != nil {
		t.Fatal(err)
	}
	token := inviteHouseholdPerson(t, app, c, person.ID, "independent@example.com")
	var before []models.Registration
	app.DB.Where("household_person_id = ?", person.ID).Order("id").Find(&before)
	if !reflect.DeepEqual(original, before) {
		t.Fatal("issuing changed registration history")
	}
	response := apiDo(t, newAPIClient(t, app), "GET", "/api/v1/invitations/"+token, nil, http.StatusOK)
	if !strings.Contains(response, `"conversion":true`) || strings.Contains(response, "householdId") || strings.Contains(response, "birthDate") {
		t.Fatal(response)
	}
	// Adults keep their household identity until approval.
	waiting := acceptConversion(t, app, token, http.StatusOK)
	apiDo(t, waiting, "GET", "/api/v1/auth/me", nil, http.StatusUnauthorized)
	if loginAPIExpectFail(t, app, "independent@example.com", "password1234") {
		t.Fatal("adult signed in before approval")
	}
	assertConversionUnmoved(t, app, person, "independent@example.com", original)
	var request models.AccountRequest
	if err := app.DB.Where("email = ? AND source = ? AND status = ?", "independent@example.com", models.RequestHouseholdConversion, models.RequestPending).First(&request).Error; err != nil {
		t.Fatal(err)
	}
	if request.PasswordHash == nil || request.HouseholdPersonID == nil || request.InvitationID == nil {
		t.Fatal("missing held conversion details")
	}
	apiDo(t, c, "POST", conversionPath(person.ID), map[string]any{"email": "other@example.com"}, http.StatusConflict)
	apiDo(t, c, "POST", conversionPath(person.ID)+"/resend", nil, http.StatusConflict)
	if err := conversionEmailAvailable(app.DB, request.Email, 0); !errors.Is(err, errConversionEmail) {
		t.Fatal("pending request did not reserve email", err)
	}
	apiDo(t, admin, "POST", "/api/v1/invitations", map[string]any{"email": request.Email, "name": "Reserved"}, http.StatusConflict)
	rawPending := apiDo(t, c, "GET", "/api/v1/me/household", nil, http.StatusOK)
	if !strings.Contains(rawPending, "awaiting_approval") {
		t.Fatal(rawPending)
	}
	apiDo(t, admin, "POST", "/api/v1/admin/account-requests/"+itoa(request.ID)+"/approve", map[string]any{}, http.StatusOK)
	var user models.User
	if err := app.DB.Where("email = ?", request.Email).First(&user).Error; err != nil {
		t.Fatal(err)
	}
	if user.Status != models.UserActive || user.AccountType != models.AccountAdult || user.BirthDate == nil || user.Name != person.Name {
		t.Fatalf("approved account: %+v", user)
	}
	request = models.AccountRequest{ID: request.ID}
	app.DB.First(&request, request.ID)
	if request.PasswordHash != nil {
		t.Fatal("approval retained held hash")
	}
	signedIn := loginAPI(t, app, "independent@example.com", "password1234")
	apiDo(t, signedIn, "GET", "/api/v1/auth/me", nil, http.StatusOK)
	if !strings.Contains(latestEmail(t, app, user.Email).Body, "password you chose") {
		t.Fatal("approval email should point to sign-in, not a set-password link")
	}
	for _, old := range original {
		var fresh models.Registration
		if err := app.DB.First(&fresh, old.ID).Error; err != nil {
			t.Fatal(err)
		}
		want := old
		want.UserID = &user.ID
		want.HouseholdPersonID = nil
		want.RegisteredBy = nil
		want.ChildcareRequested = false
		if !reflect.DeepEqual(want, fresh) {
			t.Fatalf("changed registration fields:\nwant %+v\ngot %+v", want, fresh)
		}
	}
	var membershipCount int64
	app.DB.Model(&models.Membership{}).Where("user_id = ?", user.ID).Count(&membershipCount)
	if membershipCount != 0 {
		t.Fatal("conversion inherited memberships")
	}
	var historical models.HouseholdPerson
	app.DB.First(&historical, person.ID)
	if historical.ConvertedUserID == nil || *historical.ConvertedUserID != user.ID {
		t.Fatal("historical person lost")
	}
	var account models.HouseholdAccount
	app.DB.Where("user_id = ?", user.ID).First(&account)
	if account.HouseholdID != person.HouseholdID || account.Relation != "adult" {
		t.Fatal(account)
	}
	raw := apiDo(t, c, "GET", "/api/v1/me/household", nil, http.StatusOK)
	if strings.Contains(raw, `"convertedUserId"`) || !strings.Contains(raw, `"name":"Mary"`) {
		t.Fatal(raw)
	}
	apiDo(t, c, "PUT", "/api/v1/me/household/people/"+itoa(person.ID), map[string]any{"firstName": "Changed"}, http.StatusConflict)
	apiDo(t, c, "DELETE", "/api/v1/me/household/people/"+itoa(person.ID), nil, http.StatusConflict)
	acceptConversion(t, app, token, http.StatusBadRequest)
	if auditCount(t, app, audit.HouseholdPersonConverted) != 1 || auditCount(t, app, audit.RegistrationChanged) < int64(len(original)) {
		t.Fatal("transfer audit missing")
	}
}

func TestHouseholdConversionReservationsAndLifecycle(t *testing.T) {
	app := testApp(t)
	admin, _ := openGroup(t, app, "Invites")
	holder, c, person, child := householdFamily(t, app, admin, "inviteparent")
	outsider := newTestUser(t, app, "outsider", "password1234", models.RoleMember)
	outside := loginAPI(t, app, "outsider", "password1234")
	apiDo(t, outside, "POST", conversionPath(person.ID), map[string]any{"email": "new@example.com"}, http.StatusNotFound)
	apiDo(t, anonClient(t, app, "192.0.2.77"), "POST", conversionPath(person.ID), map[string]any{"email": "new@example.com"}, http.StatusUnauthorized)
	apiDo(t, c, "POST", conversionPath(child.ID), map[string]any{"email": "child@example.com"}, http.StatusConflict)
	apiDo(t, c, "POST", conversionPath(person.ID), map[string]any{"email": holder.Email}, http.StatusConflict)
	for i, status := range []string{models.UserActive, models.UserInvited, models.UserDisabled, models.UserPendingConsent, models.UserGuest} {
		email := fmt.Sprintf("occupied%d@example.com", i)
		u := models.User{Email: email, Status: status, Role: models.RoleMember}
		if err := app.DB.Create(&u).Error; err != nil {
			t.Fatal(err)
		}
		apiDo(t, c, "POST", conversionPath(person.ID), map[string]any{"email": strings.ToUpper(email)}, http.StatusConflict)
	}
	if err := app.DB.Delete(outsider).Error; err != nil {
		t.Fatal(err)
	}
	apiDo(t, c, "POST", conversionPath(person.ID), map[string]any{"email": outsider.Email}, http.StatusConflict)
	token := inviteHouseholdPerson(t, app, c, person.ID, "separate@example.com")
	apiDo(t, c, "POST", conversionPath(person.ID), map[string]any{"email": "SEPARATE@example.com"}, http.StatusCreated)
	var sends int64
	app.DB.Model(&models.MailOutbox{}).Where("to_address = ?", "separate@example.com").Count(&sends)
	if sends != 1 {
		t.Fatalf("idempotent issue sent %d", sends)
	}
	apiDo(t, c, "POST", conversionPath(person.ID), map[string]any{"email": "different@example.com"}, http.StatusConflict)
	apiDo(t, admin, "POST", "/api/v1/invitations", map[string]any{"email": "separate@example.com"}, http.StatusConflict)
	apiDo(t, c, "POST", conversionPath(person.ID)+"/resend", nil, http.StatusOK)
	next := tokenInEmail(t, latestEmail(t, app, "separate@example.com"))
	if next == token {
		t.Fatal("resend retained raw token")
	}
	acceptConversion(t, app, token, http.StatusBadRequest)
	for range 2 {
		apiDo(t, c, "DELETE", conversionPath(person.ID), nil, http.StatusOK)
	}
	acceptConversion(t, app, next, http.StatusBadRequest)
	// Admins may initiate outside their own household; ordinary invitations reserve emails too.
	apiDo(t, admin, "POST", "/api/v1/invitations", map[string]any{"email": "ordinary@example.com"}, http.StatusCreated)
	apiDo(t, admin, "POST", conversionPath(person.ID), map[string]any{"email": "ordinary@example.com"}, http.StatusConflict)
	token = inviteHouseholdPerson(t, app, admin, person.ID, "adminstarted@example.com")
	if token == "" {
		t.Fatal("no cross-household admin invitation")
	}
	// A material edit invalidates; notes and photo consent do not.
	apiDo(t, c, "PUT", "/api/v1/me/household/people/"+itoa(person.ID), map[string]any{"careNotes": "Private", "photoConsent": true}, http.StatusOK)
	apiDo(t, newAPIClient(t, app), "GET", "/api/v1/invitations/"+token, nil, http.StatusOK)
	apiDo(t, c, "PUT", "/api/v1/me/household/people/"+itoa(person.ID), map[string]any{"firstName": "Maria"}, http.StatusOK)
	acceptConversion(t, app, token, http.StatusBadRequest)
	token = inviteHouseholdPerson(t, app, c, person.ID, "delete@example.com")
	apiDo(t, c, "DELETE", "/api/v1/me/household/people/"+itoa(person.ID), nil, http.StatusOK)
	acceptConversion(t, app, token, http.StatusBadRequest)
}

func TestTeenHouseholdConversionRefused(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "TeenConversion")
	_, c, _, _ := householdFamily(t, app, admin, "teenparent")
	birth := time.Now().UTC().AddDate(-15, 0, 0).Format("2006-01-02")
	teen := addMyPerson(t, c, map[string]any{"firstName": "Teen", "kind": "teen", "birthDate": birth})
	apiDo(t, admin, "POST", "/api/v1/admin/guardian-links/"+itoa(*teen.GuardianLinkID)+"/confirm", map[string]any{"checkNote": "Known family"}, http.StatusOK)
	eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(1), true)
	batchAnswer(t, c, eventID, date, map[string]any{"people": []map[string]any{{"personId": teen.ID, "status": "going"}}}, http.StatusOK)
	checkRefusal := func(body string) {
		t.Helper()
		var response struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		}
		if err := json.Unmarshal([]byte(body), &response); err != nil {
			t.Fatal(err)
		}
		if response.Code != "household_conversion_teen" || response.Error != "teens can get their own account once parent consent is available" {
			t.Fatal(body)
		}
	}
	checkRefusal(apiDo(t, c, "POST", conversionPath(teen.ID), map[string]any{"email": "teenown@example.com"}, http.StatusConflict))
	checkRefusal(apiDo(t, admin, "POST", conversionPath(teen.ID), map[string]any{"email": "teenown@example.com"}, http.StatusConflict))
	var invitationCount int64
	if err := app.DB.Model(&models.Invitation{}).Where("household_person_id = ?", teen.ID).Count(&invitationCount).Error; err != nil {
		t.Fatal(err)
	}
	if invitationCount != 0 {
		t.Fatal("refused creation saved an invitation")
	}

	// An invitation issued before teen conversion was paused must stay untouched.
	raw, hash, err := createInvitationToken()
	if err != nil {
		t.Fatal(err)
	}
	inv := models.Invitation{HouseholdPersonID: &teen.ID, Email: "teenown@example.com", Name: "Teen", InvitedBy: teen.CreatedBy, Role: models.MembershipMember, AccountType: models.AccountTeen, TokenHash: hash, ExpiresAt: time.Now().UTC().Add(tokenTTL)}
	if err := app.DB.Create(&inv).Error; err != nil {
		t.Fatal(err)
	}
	var beforePerson models.HouseholdPerson
	var beforeRegs []models.Registration
	var beforeLinks []models.GuardianLink
	if err := app.DB.First(&beforePerson, teen.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Where("household_person_id = ?", teen.ID).Order("id").Find(&beforeRegs).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Where("person_id = ?", teen.ID).Order("id").Find(&beforeLinks).Error; err != nil {
		t.Fatal(err)
	}
	var beforeInv models.Invitation
	if err := app.DB.First(&beforeInv, inv.ID).Error; err != nil {
		t.Fatal(err)
	}
	counts := func() []int64 {
		t.Helper()
		var out []int64
		for _, table := range []any{&models.User{}, &models.HouseholdAccount{}, &models.GuardianLink{}, &models.AccountRequest{}, &models.AuditLog{}, &models.Notification{}} {
			var n int64
			if err := app.DB.Model(table).Count(&n).Error; err != nil {
				t.Fatal(err)
			}
			out = append(out, n)
		}
		return out
	}
	beforeCounts := counts()
	checkRefusal(apiDo(t, c, "POST", conversionPath(teen.ID)+"/resend", nil, http.StatusConflict))
	anon := newAPIClient(t, app)
	anon.fetchCSRF()
	checkRefusal(apiDo(t, anon, "POST", "/api/v1/invitations/accept", map[string]any{"token": raw, "password": "password1234"}, http.StatusConflict))
	apiDo(t, anon, "GET", "/api/v1/auth/me", nil, http.StatusUnauthorized)
	var afterPerson models.HouseholdPerson
	var afterRegs []models.Registration
	var afterLinks []models.GuardianLink
	var afterInv models.Invitation
	if err := app.DB.First(&afterPerson, teen.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Where("household_person_id = ?", teen.ID).Order("id").Find(&afterRegs).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Where("person_id = ?", teen.ID).Order("id").Find(&afterLinks).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.First(&afterInv, inv.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforePerson, afterPerson) || !reflect.DeepEqual(beforeRegs, afterRegs) || !reflect.DeepEqual(beforeLinks, afterLinks) || !reflect.DeepEqual(beforeInv, afterInv) || !reflect.DeepEqual(beforeCounts, counts()) {
		t.Fatal("teen refusal changed household, registrations, invitation, account state, audits or notices")
	}
	if len(afterRegs) != 1 || afterRegs[0].HouseholdPersonID == nil || *afterRegs[0].HouseholdPersonID != teen.ID {
		t.Fatal("teen sign-up moved")
	}
	var household struct {
		Household householdJSON `json:"household"`
	}
	if err := json.Unmarshal([]byte(apiDo(t, c, "GET", "/api/v1/me/household", nil, http.StatusOK)), &household); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, person := range household.Household.People {
		if person.ID == teen.ID {
			found = true
			if person.CanConvert || person.ConvertBlocked != "teen" {
				t.Fatalf("teen eligibility: %+v", person)
			}
		}
	}
	if !found {
		t.Fatal("teen disappeared from household")
	}
}

type conversionFailMailer struct{}

func (conversionFailMailer) Send(context.Context, mail.Message) error {
	return errors.New("test delivery failure")
}

func TestHouseholdConversionFailedMailExpiryAndDisabledHolder(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "ConversionFailure")
	holder, c, person, _ := householdFamily(t, app, admin, "failureparent")
	sender := app.Mailer
	app.Mailer = conversionFailMailer{}
	apiDo(t, c, "POST", conversionPath(person.ID), map[string]any{"email": "retry@example.com"}, http.StatusCreated)
	var inv models.Invitation
	if err := app.DB.Where("household_person_id = ?", person.ID).First(&inv).Error; err != nil {
		t.Fatal(err)
	}
	if inv.AcceptedAt != nil {
		t.Fatal("failed mail consumed invitation")
	}
	app.Mailer = sender
	apiDo(t, c, "POST", conversionPath(person.ID)+"/resend", nil, http.StatusOK)
	token := tokenInEmail(t, latestEmail(t, app, "retry@example.com"))
	app.DB.Model(&inv).Update("expires_at", time.Now().UTC().Add(-time.Hour))
	acceptConversion(t, app, token, http.StatusBadRequest)
	token = inviteHouseholdPerson(t, app, c, person.ID, "retry@example.com")
	eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(1), true)
	batchAnswer(t, c, eventID, date, map[string]any{"people": []map[string]any{{"personId": person.ID, "status": "going"}}}, http.StatusOK)
	apiDo(t, admin, "PUT", "/api/v1/admin/users/"+itoa(holder.ID), map[string]any{"status": "disabled"}, http.StatusOK)
	acceptConversion(t, app, token, http.StatusConflict)
	var n int64
	app.DB.Model(&models.User{}).Where("email = ?", "retry@example.com").Count(&n)
	if n != 0 {
		t.Fatal("disabled holder's registration moved")
	}
	apiDo(t, admin, "DELETE", conversionPath(person.ID), nil, http.StatusOK)
}

func TestHouseholdConversionMailLimitCountsResends(t *testing.T) {
	app := testApp(t)
	admin, _ := openGroup(t, app, "ConversionLimits")
	_, c, person, _ := householdFamily(t, app, admin, "limitparent")
	inviteHouseholdPerson(t, app, c, person.ID, "limit@example.com")
	for range leaderInviteDailyLimit - 1 {
		apiDo(t, c, "POST", conversionPath(person.ID)+"/resend", nil, http.StatusOK)
	}
	token := tokenInEmail(t, latestEmail(t, app, "limit@example.com"))
	var before models.Invitation
	app.DB.Where("household_person_id = ?", person.ID).First(&before)
	apiDo(t, c, "POST", conversionPath(person.ID)+"/resend", nil, http.StatusOK)
	apiDo(t, admin, "POST", conversionPath(person.ID)+"/resend", nil, http.StatusOK)
	var after models.Invitation
	app.DB.First(&after, before.ID)
	var count int64
	app.DB.Model(&models.MailOutbox{}).Where("to_address = ?", "limit@example.com").Count(&count)
	if count != leaderInviteDailyLimit || after.TokenHash != before.TokenHash || after.TokenHash != hashToken(token) {
		t.Fatal("suppressed send changed token or exceeded limit")
	}
}

func TestHouseholdConversionConcurrentDoubleAccept(t *testing.T) {
	app := testApp(t)
	admin, _ := openGroup(t, app, "ConversionRace")
	_, c, person, _ := householdFamily(t, app, admin, "raceparent")
	token := inviteHouseholdPerson(t, app, c, person.ID, "race@example.com")
	clients := []*apiClient{newAPIClient(t, app), newAPIClient(t, app)}
	for _, client := range clients {
		client.fetchCSRF()
	}
	codes := make([]int, 2)
	var wg sync.WaitGroup
	for i, client := range clients {
		wg.Add(1)
		go func(i int, c *apiClient) {
			defer wg.Done()
			codes[i] = c.do("POST", "/api/v1/invitations/accept", map[string]any{"token": token, "password": "password1234"}, true).Code
		}(i, client)
	}
	wg.Wait()
	if !((codes[0] == 200 && codes[1] == 400) || (codes[1] == 200 && codes[0] == 400)) {
		t.Fatalf("double accept: %v", codes)
	}
	if auditCount(t, app, audit.AccountRequested) != 1 || auditCount(t, app, audit.HouseholdPersonConverted) != 0 {
		t.Fatal("acceptance did not create exactly one pending request")
	}
}

func TestHouseholdConversionRollbackAfterTransfer(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "RollbackConversion")
	holder, c, person, _ := householdFamily(t, app, admin, "rollbackparent")
	eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(3), true)
	batchAnswer(t, c, eventID, date, map[string]any{"people": []map[string]any{{"personId": person.ID, "status": "going"}}}, http.StatusOK)
	token := inviteHouseholdPerson(t, app, c, person.ID, "rollback@example.com")
	// Fail after the transfer statement, while marking the historical identity.
	if err := app.DB.Exec(`CREATE FUNCTION reject_conversion_test() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test transfer failure'; END $$`).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Exec(fmt.Sprintf(`CREATE TRIGGER reject_conversion_test BEFORE UPDATE ON household_people FOR EACH ROW WHEN (OLD.id = %d AND NEW.converted_user_id IS NOT NULL) EXECUTE FUNCTION reject_conversion_test()`, person.ID)).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		app.DB.Exec(`DROP TRIGGER IF EXISTS reject_conversion_test ON household_people`)
		app.DB.Exec(`DROP FUNCTION IF EXISTS reject_conversion_test()`)
	})
	var before models.Registration
	app.DB.Where("household_person_id = ?", person.ID).First(&before)
	acceptConversion(t, app, token, http.StatusOK)
	var request models.AccountRequest
	app.DB.Where("email = ?", "rollback@example.com").First(&request)
	apiDo(t, admin, "POST", "/api/v1/admin/account-requests/"+itoa(request.ID)+"/approve", map[string]any{}, http.StatusInternalServerError)
	var after models.Registration
	app.DB.First(&after, before.ID)
	if !reflect.DeepEqual(before, after) || after.RegisteredBy == nil || *after.RegisteredBy != holder.ID {
		t.Fatal("failed transaction moved registration")
	}
	for _, table := range []any{&models.User{}, &models.HouseholdAccount{}} {
		var n int64
		q := app.DB.Model(table)
		if _, ok := table.(*models.User); ok {
			q = q.Where("email = ?", "rollback@example.com")
		} else {
			q = q.Where("household_id = ?", person.HouseholdID)
		}
		if err := q.Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		if (reflect.TypeOf(table) == reflect.TypeOf(&models.User{}) && n != 0) || (reflect.TypeOf(table) == reflect.TypeOf(&models.HouseholdAccount{}) && n != 1) {
			t.Fatal("failed conversion left account state")
		}
	}
	var inv models.Invitation
	app.DB.Where("token_hash = ?", hashToken(token)).First(&inv)
	if inv.AcceptedAt == nil || auditCount(t, app, audit.HouseholdPersonConverted) != 0 {
		t.Fatal("failed transfer lost acceptance or left audit")
	}
	var notices int64
	app.DB.Model(&models.Notification{}).Where("kind = ?", notify.HouseholdConverted).Count(&notices)
	if notices != 0 {
		t.Fatal("failed transfer left notifications")
	}
	app.DB.Exec(`DROP TRIGGER reject_conversion_test ON household_people`)
	approveConversionForEmail(t, app, admin, "rollback@example.com")
}

func TestHouseholdConversionRechecksAgeAndReservesEmailAtAccept(t *testing.T) {
	app := testApp(t)
	admin, _ := openGroup(t, app, "FreshConversion")
	_, c, person, _ := householdFamily(t, app, admin, "freshparent")
	token := inviteHouseholdPerson(t, app, c, person.ID, "takenlater@example.com")
	guest := models.User{Email: "takenlater@example.com", Name: "Guest", Status: models.UserGuest, Role: models.RoleMember}
	if err := app.DB.Create(&guest).Error; err != nil {
		t.Fatal(err)
	}
	acceptConversion(t, app, token, http.StatusConflict)
	app.DB.Unscoped().Delete(&guest)
	// Simulate a birthday crossing the teen/adult boundary without a profile edit.
	if err := app.DB.Model(&models.HouseholdPerson{}).Where("id = ?", person.ID).Updates(map[string]any{"kind": models.HouseholdTeen, "birth_date": time.Now().UTC().AddDate(-18, 0, -1)}).Error; err != nil {
		t.Fatal(err)
	}
	acceptConversion(t, app, token, http.StatusConflict)
	var inv models.Invitation
	app.DB.Where("token_hash = ?", hashToken(token)).First(&inv)
	if inv.AcceptedAt != nil {
		t.Fatal("age refusal consumed invitation")
	}
}

func TestHouseholdConversionsSharingOccurrencesDoNotDeadlock(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "SharedConversion")
	_, ca, pa, _ := householdFamily(t, app, admin, "sharedparenta")
	_, cb, pb, _ := householdFamily(t, app, admin, "sharedparentb")
	eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(2), true)
	for _, pair := range []struct {
		c *apiClient
		p uint
	}{{ca, pa.ID}, {cb, pb.ID}} {
		batchAnswer(t, pair.c, eventID, date, map[string]any{"people": []map[string]any{{"personId": pair.p, "status": "going"}}}, http.StatusOK)
	}
	tokens := []string{inviteHouseholdPerson(t, app, ca, pa.ID, "shareda@example.com"), inviteHouseholdPerson(t, app, cb, pb.ID, "sharedb@example.com")}
	var requests []models.AccountRequest
	for _, token := range tokens {
		acceptConversion(t, app, token, http.StatusOK)
	}
	app.DB.Where("source = ? AND status = ?", models.RequestHouseholdConversion, models.RequestPending).Order("id").Find(&requests)
	if len(requests) != 2 {
		t.Fatal("missing conversion requests")
	}
	clients := []*apiClient{loginAPI(t, app, "admin", "password1234"), loginAPI(t, app, "admin", "password1234")}
	for _, c := range clients {
		c.fetchCSRF()
	}
	var wg sync.WaitGroup
	codes := make([]int, 2)
	start := make(chan struct{})
	for i, c := range clients {
		wg.Add(1)
		go func(i int, c *apiClient) {
			defer wg.Done()
			<-start
			codes[i] = c.do("POST", "/api/v1/admin/account-requests/"+itoa(requests[i].ID)+"/approve", map[string]any{}, true).Code
		}(i, c)
	}
	close(start)
	wg.Wait()
	if codes[0] != 200 || codes[1] != 200 {
		t.Fatalf("shared occurrence conversions: %v", codes)
	}
	inst := eventOccurrence(t, admin, eventID)
	if inst.SpotsLeft == nil || *inst.SpotsLeft != 0 {
		t.Fatal("conversion changed capacity")
	}
}

func TestHouseholdConversionCompetesWithHouseholdMutations(t *testing.T) {
	for _, operation := range []string{"batch", "second adult batch", "disable", "edit", "delete", "resend", "cancel"} {
		t.Run(operation, func(t *testing.T) {
			app := testApp(t)
			admin, gid := openGroup(t, app, "MutationConversion")
			holder, c, person, _ := householdFamily(t, app, admin, "mutationparent")
			batchClient := c
			if operation == "second adult batch" {
				second := newTestUser(t, app, "mutationsecond", "password1234", models.RoleMember)
				if err := app.DB.Create(&models.HouseholdAccount{HouseholdID: person.HouseholdID, UserID: second.ID, Relation: models.HouseholdRelationAdult}).Error; err != nil {
					t.Fatal(err)
				}
				batchClient = loginAPI(t, app, "mutationsecond", "password1234")
			}
			eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(1), true)
			batchAnswer(t, c, eventID, date, map[string]any{"people": []map[string]any{{"personId": person.ID, "status": "going"}}}, http.StatusOK)
			token := inviteHouseholdPerson(t, app, c, person.ID, "mutation@example.com")
			anon := newAPIClient(t, app)
			anon.fetchCSRF()
			var codes [2]int
			var wg sync.WaitGroup
			start := make(chan struct{})
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				codes[0] = anon.do("POST", "/api/v1/invitations/accept", map[string]any{"token": token, "password": "password1234"}, true).Code
			}()
			go func() {
				defer wg.Done()
				<-start
				switch operation {
				case "batch", "second adult batch":
					codes[1] = batchClient.do("PUT", "/api/v1/events/"+itoa(eventID)+"/registrations", map[string]any{"date": date, "people": []map[string]any{{"personId": person.ID, "status": "maybe"}}}, true).Code
				case "disable":
					codes[1] = admin.do("PUT", "/api/v1/admin/users/"+itoa(holder.ID), map[string]any{"status": "disabled"}, true).Code
				case "edit":
					codes[1] = c.do("PUT", "/api/v1/me/household/people/"+itoa(person.ID), map[string]any{"firstName": "Changed"}, true).Code
				case "delete":
					codes[1] = c.do("DELETE", "/api/v1/me/household/people/"+itoa(person.ID), nil, true).Code
				case "resend":
					codes[1] = c.do("POST", conversionPath(person.ID)+"/resend", nil, true).Code
				case "cancel":
					codes[1] = c.do("DELETE", conversionPath(person.ID), nil, true).Code
				}
			}()
			close(start)
			wg.Wait()
			for _, code := range codes {
				if code != 200 && code != 400 && code != 404 && code != 409 {
					t.Fatalf("%s race responses: %v", operation, codes)
				}
			}
			var old models.Registration
			app.DB.Where("occurrence_id IN (SELECT id FROM event_occurrences WHERE event_id = ?)", eventID).First(&old)
			if old.HouseholdPersonID != nil && old.UserID != nil {
				t.Fatal("partial identity transfer")
			}
			var count int64
			app.DB.Model(&models.User{}).Where("email = ?", "mutation@example.com").Count(&count)
			if count != 0 {
				t.Fatalf("accept response and account disagree: %v count %d", codes, count)
			}
		})
	}
}

func TestHouseholdConversionLargeHistoryIsAtomic(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "HistoryConversion")
	holder, c, person, _ := householdFamily(t, app, admin, "historyparent")
	eventID, _ := seedCappedEvent(t, app, admin, gid, nil, true)
	start := time.Now().UTC().AddDate(-5, 0, 0).Truncate(24 * time.Hour)
	const total = 300
	for i := range total {
		at := start.AddDate(0, 0, i)
		o := models.EventOccurrence{EventID: eventID, OriginalDate: at.Format("2006-01-02"), StartsAt: at, EndsAt: at.Add(time.Hour), Status: models.OccurrenceCancelled}
		if err := app.DB.Create(&o).Error; err != nil {
			t.Fatal(err)
		}
		r := models.Registration{OccurrenceID: o.ID, HouseholdPersonID: &person.ID, RegisteredBy: &holder.ID, Status: models.RegCancelled, AttendanceMode: models.AttendanceInPerson, ChildcareRequested: true}
		if err := app.DB.Create(&r).Error; err != nil {
			t.Fatal(err)
		}
	}
	token := inviteHouseholdPerson(t, app, c, person.ID, "history@example.com")
	began := time.Now()
	acceptConversion(t, app, token, http.StatusOK)
	approveConversionForEmail(t, app, admin, "history@example.com")
	t.Logf("atomic transfer of %d historical occurrences took %s", total, time.Since(began))
	var remaining, transferred int64
	app.DB.Model(&models.Registration{}).Where("household_person_id = ?", person.ID).Count(&remaining)
	app.DB.Model(&models.Registration{}).Joins("JOIN users ON users.id = registrations.user_id").Where("users.email = ? AND registrations.registered_by IS NULL AND NOT registrations.childcare_requested", "history@example.com").Count(&transferred)
	if remaining != 0 || transferred != total {
		t.Fatalf("partial history transfer: %d remaining, %d transferred", remaining, transferred)
	}
}

func TestConversionInviterReenabledNeedsFreshLink(t *testing.T) {
	app := testApp(t)
	admin, _ := openGroup(t, app, "ReenabledConversion")
	holder, c, person, _ := householdFamily(t, app, admin, "reenabledparent")
	token := inviteHouseholdPerson(t, app, c, person.ID, "reenabled@example.com")
	apiDo(t, admin, "PUT", "/api/v1/admin/users/"+itoa(holder.ID), map[string]any{"status": "disabled"}, http.StatusOK)
	apiDo(t, admin, "PUT", "/api/v1/admin/users/"+itoa(holder.ID), map[string]any{"status": "active"}, http.StatusOK)
	acceptConversion(t, app, token, http.StatusConflict)
	c = loginAPI(t, app, "reenabledparent", "password1234")
	apiDo(t, c, "POST", conversionPath(person.ID)+"/resend", nil, http.StatusOK)
	fresh := tokenInEmail(t, latestEmail(t, app, "reenabled@example.com"))
	acceptConversion(t, app, fresh, http.StatusOK)
}

func TestConversionCompetesWithOrdinaryInvitation(t *testing.T) {
	app := testApp(t)
	admin, _ := openGroup(t, app, "EmailCompetition")
	_, c, person, _ := householdFamily(t, app, admin, "emailparent")
	var codes [2]int
	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		codes[0] = c.do("POST", conversionPath(person.ID), map[string]any{"email": "competition@example.com"}, true).Code
	}()
	go func() {
		defer wg.Done()
		<-start
		codes[1] = admin.do("POST", "/api/v1/invitations", map[string]any{"email": "competition@example.com"}, true).Code
	}()
	close(start)
	wg.Wait()
	if !((codes[0] == 201 && codes[1] == 409) || (codes[1] == 201 && codes[0] == 409)) {
		t.Fatalf("invitation email reservation race: %v", codes)
	}
	var n int64
	app.DB.Model(&models.Invitation{}).Where("email = ? AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now()", "competition@example.com").Count(&n)
	if n != 1 {
		t.Fatalf("email reserved %d times", n)
	}
}

// A rate-limited first invitation saves nothing, so a limited sender can't
// reserve a newcomer's email (Warden, PR 111 F2).
func TestHouseholdConversionSuppressedFirstSendSavesNothing(t *testing.T) {
	app := testApp(t)
	admin, _ := openGroup(t, app, "ConversionSuppressed")
	_, c, person, _ := householdFamily(t, app, admin, "suppressparent")
	normal := apiDo(t, c, "POST", conversionPath(person.ID), map[string]any{"email": "fill@example.com"}, http.StatusCreated)
	for range leaderInviteDailyLimit - 1 {
		apiDo(t, c, "POST", conversionPath(person.ID)+"/resend", nil, http.StatusOK)
	}
	apiDo(t, c, "DELETE", conversionPath(person.ID), nil, http.StatusOK)
	raw := apiDo(t, c, "POST", conversionPath(person.ID), map[string]any{"email": "newcomer@example.com"}, http.StatusCreated)
	if !strings.Contains(raw, "newcomer@example.com") {
		t.Fatal("suppressed send should still look normal:", raw)
	}
	var normalJSON, suppressedJSON map[string]map[string]any
	if err := json.Unmarshal([]byte(normal), &normalJSON); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), &suppressedJSON); err != nil {
		t.Fatal(err)
	}
	keys := func(m map[string]any) []string {
		var out []string
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	if !reflect.DeepEqual(keys(normalJSON["conversion"]), keys(suppressedJSON["conversion"])) {
		t.Fatal("suppressed response has different keys")
	}
	var count int64
	app.DB.Model(&models.Invitation{}).Where("email = ?", "newcomer@example.com").Count(&count)
	if count != 0 {
		t.Fatal("suppressed first send saved an invitation row")
	}
	app.DB.Model(&models.MailOutbox{}).Where("to_address = ?", "newcomer@example.com").Count(&count)
	if count != 0 {
		t.Fatal("suppressed send mailed")
	}
}

func approveConversionForEmail(t *testing.T, app *App, admin *apiClient, email string) {
	t.Helper()
	var req models.AccountRequest
	if err := app.DB.Where("email = ? AND status = ?", email, models.RequestPending).First(&req).Error; err != nil {
		t.Fatal(err)
	}
	apiDo(t, admin, "POST", "/api/v1/admin/account-requests/"+itoa(req.ID)+"/approve", map[string]any{}, http.StatusOK)
}

func assertConversionUnmoved(t *testing.T, app *App, person householdPersonJSON, email string, before []models.Registration) {
	t.Helper()
	var after []models.Registration
	app.DB.Where("household_person_id = ?", person.ID).Order("id").Find(&after)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("pending conversion changed sign-ups")
	}
	var n int64
	app.DB.Unscoped().Model(&models.User{}).Where("email = ?", email).Count(&n)
	if n != 0 {
		t.Fatal("pending conversion created a user")
	}
	var fresh models.HouseholdPerson
	app.DB.First(&fresh, person.ID)
	if fresh.ConvertedUserID != nil {
		t.Fatal("pending conversion marked person converted")
	}
	app.DB.Model(&models.HouseholdAccount{}).Where("household_id = ?", person.HouseholdID).Count(&n)
	if n != 1 {
		t.Fatal("pending conversion added a household account")
	}
}

func TestHouseholdConversionClosedRequestKeepsHousehold(t *testing.T) {
	for _, decision := range []string{"reject", "expire"} {
		t.Run(decision, func(t *testing.T) {
			app := testApp(t)
			admin, gid := openGroup(t, app, "ClosedConversion")
			_, c, person, _ := householdFamily(t, app, admin, "closedparent")
			eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(1), true)
			batchAnswer(t, c, eventID, date, map[string]any{"people": []map[string]any{{"personId": person.ID, "status": "going"}}}, http.StatusOK)
			var before []models.Registration
			app.DB.Where("household_person_id = ?", person.ID).Order("id").Find(&before)
			token := inviteHouseholdPerson(t, app, c, person.ID, "closed@example.com")
			acceptConversion(t, app, token, http.StatusOK)
			var req models.AccountRequest
			app.DB.Where("email = ?", "closed@example.com").First(&req)
			if decision == "reject" {
				apiDo(t, admin, "POST", "/api/v1/admin/account-requests/"+itoa(req.ID)+"/reject", map[string]any{}, http.StatusOK)
			} else {
				app.DB.Model(&req).Update("expires_at", time.Now().Add(-time.Hour))
				if _, err := app.expirePendingRequests(); err != nil {
					t.Fatal(err)
				}
			}
			assertConversionUnmoved(t, app, person, req.Email, before)
			req = models.AccountRequest{ID: req.ID}
			app.DB.First(&req, req.ID)
			if req.Status == models.RequestPending || req.PasswordHash != nil {
				t.Fatal("request did not close and clear credentials")
			}
			inviteHouseholdPerson(t, app, c, person.ID, req.Email)
		})
	}
}

func TestHouseholdConversionApprovalRefusesUnavailable(t *testing.T) {
	for _, change := range []string{"deleted", "converted", "email taken", "holder disabled"} {
		t.Run(change, func(t *testing.T) {
			app := testApp(t)
			admin, gid := openGroup(t, app, "UnavailableConversion")
			holder, c, person, _ := householdFamily(t, app, admin, "unavailableparent")
			eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(1), true)
			batchAnswer(t, c, eventID, date, map[string]any{"people": []map[string]any{{"personId": person.ID, "status": "going"}}}, http.StatusOK)
			token := inviteHouseholdPerson(t, app, c, person.ID, "unavailable@example.com")
			acceptConversion(t, app, token, http.StatusOK)
			var req models.AccountRequest
			app.DB.Where("email = ?", "unavailable@example.com").First(&req)
			switch change {
			case "deleted":
				apiDo(t, c, "DELETE", "/api/v1/me/household/people/"+itoa(person.ID), nil, http.StatusOK)
			case "converted":
				if err := app.DB.Model(&models.HouseholdPerson{}).Where("id = ?", person.ID).Update("converted_user_id", holder.ID).Error; err != nil {
					t.Fatal(err)
				}
			case "email taken":
				u := models.User{Email: req.Email, Name: "Other", Role: models.RoleMember, Status: models.UserActive, AccountType: models.AccountAdult}
				if err := app.DB.Create(&u).Error; err != nil {
					t.Fatal(err)
				}
			case "holder disabled":
				app.DB.Model(&holder).Update("status", models.UserDisabled)
			}
			apiDo(t, admin, "POST", "/api/v1/admin/account-requests/"+itoa(req.ID)+"/approve", map[string]any{}, http.StatusConflict)
			app.DB.First(&req, req.ID)
			if req.Status != models.RequestPending || req.PasswordHash == nil {
				t.Fatal("refused approval closed the request")
			}
			if auditCount(t, app, audit.HouseholdPersonConverted) != 0 {
				t.Fatal("refused approval converted person")
			}
		})
	}
}

func TestHouseholdConversionApprovalRechecksAcceptedFacts(t *testing.T) {
	for _, change := range []string{"name", "birth date", "under 18", "inviter disabled", "inviter deleted"} {
		t.Run(change, func(t *testing.T) {
			app := testApp(t)
			admin, gid := openGroup(t, app, "ApprovalFacts")
			holder, c, person, _ := householdFamily(t, app, admin, "approvalparent")
			eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(1), true)
			batchAnswer(t, c, eventID, date, map[string]any{"people": []map[string]any{{"personId": person.ID, "status": "going"}}}, http.StatusOK)
			var before []models.Registration
			if err := app.DB.Where("household_person_id = ?", person.ID).Order("id").Find(&before).Error; err != nil {
				t.Fatal(err)
			}
			token := inviteHouseholdPerson(t, app, c, person.ID, "approvalfacts@example.com")
			acceptConversion(t, app, token, http.StatusOK)
			var req models.AccountRequest
			if err := app.DB.Where("email = ?", "approvalfacts@example.com").First(&req).Error; err != nil {
				t.Fatal(err)
			}
			wantCode := "household_conversion_details"
			var err error
			switch change {
			case "name":
				err = app.DB.Model(&models.HouseholdPerson{}).Where("id = ?", person.ID).Update("first_name", "Changed").Error
			case "birth date":
				err = app.DB.Model(&models.HouseholdPerson{}).Where("id = ?", person.ID).Update("birth_date", "1980-01-01").Error
			case "under 18":
				// Keep the snapshot matching to exercise the approval-date age check independently.
				birth := app.householdClock(time.Now().UTC()).AddDate(-18, 0, 1).Format("2006-01-02")
				if err := app.DB.Model(&req).Update("birth_date", birth).Error; err != nil {
					t.Fatal(err)
				}
				err = app.DB.Model(&models.HouseholdPerson{}).Where("id = ?", person.ID).Update("birth_date", birth).Error
				wantCode = "household_conversion_adult"
			case "inviter disabled":
				err = app.DB.Model(&holder).Update("status", models.UserDisabled).Error
				wantCode = "household_conversion_inviter"
			case "inviter deleted":
				err = app.DB.Delete(&holder).Error
				wantCode = "household_conversion_inviter"
			}
			if err != nil {
				t.Fatal(err)
			}
			raw := apiDo(t, admin, "POST", "/api/v1/admin/account-requests/"+itoa(req.ID)+"/approve", map[string]any{}, http.StatusConflict)
			var response struct{ Code string }
			if err := json.Unmarshal([]byte(raw), &response); err != nil {
				t.Fatal(err)
			}
			if response.Code != wantCode {
				t.Fatalf("code = %q, want %q: %s", response.Code, wantCode, raw)
			}
			if err := app.DB.First(&req, req.ID).Error; err != nil {
				t.Fatal(err)
			}
			if req.Status != models.RequestPending || req.PasswordHash == nil || req.DecidedAt != nil {
				t.Fatal("refused approval decided request or cleared credentials")
			}
			assertConversionUnmoved(t, app, person, req.Email, before)
			if auditCount(t, app, audit.HouseholdPersonConverted) != 0 || auditCount(t, app, audit.AccountApproved) != 0 {
				t.Fatal("refused approval left a decision or conversion audit")
			}
			var notices int64
			if err := app.DB.Model(&models.Notification{}).Where("kind = ?", notify.HouseholdConverted).Count(&notices).Error; err != nil {
				t.Fatal(err)
			}
			if notices != 0 {
				t.Fatal("refused approval left conversion notices")
			}
		})
	}
}

func TestConversionRetryErrorIsConflict(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	conversionError(c, fmt.Errorf("approval: %w", errConversionRetry))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var response struct{ Error, Code string }
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Code != "household_conversion_retry" || !strings.Contains(response.Error, "try again") {
		t.Fatalf("unexpected retry response: %+v", response)
	}
}
