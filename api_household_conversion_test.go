package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
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
	signedIn := acceptConversion(t, app, token, http.StatusOK)
	apiDo(t, signedIn, "GET", "/api/v1/auth/me", nil, http.StatusOK)
	var user models.User
	if err := app.DB.Where("email = ?", "independent@example.com").First(&user).Error; err != nil {
		t.Fatal(err)
	}
	if !user.CanSignIn() || user.AccountType != models.AccountAdult || user.BirthDate == nil || user.Name != person.Name {
		t.Fatalf("new account: %+v", user)
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
	if auditCount(t, app, audit.HouseholdPersonConverted) != 1 || auditCount(t, app, audit.RegistrationChanged) < len(original) {
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
	apiDo(t, newAPIClient(t, app), "POST", conversionPath(person.ID), map[string]any{"email": "new@example.com"}, http.StatusUnauthorized)
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

func TestTeenHouseholdConversionWaitsForConsent(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "TeenConversion")
	holder, c, _, _ := householdFamily(t, app, admin, "teenparent")
	birth := time.Now().UTC().AddDate(-15, 0, 0).Format("2006-01-02")
	teen := addMyPerson(t, c, map[string]any{"firstName": "Teen", "kind": "teen", "birthDate": birth})
	apiDo(t, admin, "POST", "/api/v1/admin/guardian-links/"+itoa(*teen.GuardianLinkID)+"/confirm", map[string]any{"checkNote": "Known family"}, http.StatusOK)
	eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(1), true)
	batchAnswer(t, c, eventID, date, map[string]any{"people": []map[string]any{{"personId": teen.ID, "status": "going"}}}, http.StatusOK)
	token := inviteHouseholdPerson(t, app, c, teen.ID, "teenown@example.com")
	anon := newAPIClient(t, app)
	anon.fetchCSRF()
	response := apiDo(t, anon, "POST", "/api/v1/invitations/accept", map[string]any{"token": token, "password": "password1234"}, http.StatusOK)
	if strings.Contains(response, `"user"`) || !strings.Contains(response, `"pendingConsent":true`) {
		t.Fatal(response)
	}
	apiDo(t, anon, "GET", "/api/v1/auth/me", nil, http.StatusUnauthorized)
	var user models.User
	app.DB.Where("email = ?", "teenown@example.com").First(&user)
	if user.Status != models.UserPendingConsent || user.CanSignIn() {
		t.Fatal("teen activated")
	}
	var link models.GuardianLink
	if err := app.DB.Where("teen_user_id = ? AND guardian_user_id = ?", user.ID, holder.ID).First(&link).Error; err != nil {
		t.Fatal(err)
	}
	if link.Status != models.GuardianPending || link.PersonID != nil || link.ConfirmedBy != nil || link.CheckNote != "" {
		t.Fatal("guardian decision copied")
	}
	var old models.GuardianLink
	app.DB.First(&old, *teen.GuardianLinkID)
	if old.Status != models.GuardianRevoked {
		t.Fatal("old link live")
	}
	inst := eventOccurrence(t, admin, eventID)
	if inst.SpotsLeft == nil || *inst.SpotsLeft != 0 || !strings.Contains(apiDo(t, admin, "GET", "/api/v1/events/"+itoa(eventID), nil, http.StatusOK), `"name":"Teen"`) {
		t.Fatal("pending teen lost seat or leader visibility")
	}
	apiDo(t, admin, "POST", "/api/v1/admin/guardian-links/"+itoa(link.ID)+"/confirm", map[string]any{"checkNote": "Fresh check"}, http.StatusOK)
	app.DB.First(&user, user.ID)
	if user.Status != models.UserPendingConsent {
		t.Fatal("link confirmation activated teen")
	}
	var pending int64
	app.DB.Model(&models.Notification{}).Where("kind = ? AND subject_id = ? AND resolved_at IS NULL", notify.HouseholdLinkPending, link.ID).Count(&pending)
	if pending != 0 {
		t.Fatal("guardian task still open")
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
	token := inviteHouseholdPerson(t, app, c, person.ID, "limit@example.com")
	for range leaderInviteDailyLimit - 1 {
		apiDo(t, c, "POST", conversionPath(person.ID)+"/resend", nil, http.StatusOK)
	}
	token = tokenInEmail(t, latestEmail(t, app, "limit@example.com"))
	var before models.Invitation
	app.DB.Where("household_person_id = ?", person.ID).First(&before)
	apiDo(t, c, "POST", conversionPath(person.ID)+"/resend", nil, http.StatusOK)
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
	if auditCount(t, app, audit.HouseholdPersonConverted) != 1 {
		t.Fatal("conversion occurred twice")
	}
}
