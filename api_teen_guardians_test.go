package main

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"fold/internal/audit"
	"fold/models"
)

func guardianTestTeen(t *testing.T, app *App, name string) *models.User {
	t.Helper()
	u := newTestUser(t, app, name, "password1234", models.RoleMember)
	birth := time.Now().UTC().AddDate(-15, 0, 0)
	if err := app.DB.Model(u).Updates(map[string]any{"account_type": models.AccountTeen, "birth_date": birth, "status": models.UserPendingConsent}).Error; err != nil {
		t.Fatal(err)
	}
	app.DB.First(u, u.ID)
	return u
}

func TestTeenGuardianNominationWaitsForAccountApproval(t *testing.T) {
	for _, known := range []bool{true, false} {
		t.Run(map[bool]string{true: "active adult", false: "unknown adult"}[known], func(t *testing.T) {
			app := testApp(t)
			newTestUser(t, app, "admin", "pw", models.RoleAdmin)
			if known {
				newTestUser(t, app, "guardian", "pw", models.RoleMember)
			}
			anon := newAPIClient(t, app)
			anon.fetchCSRF()
			body := signupBody("Teen", "teen@example.com", "teen", time.Now().UTC().AddDate(-15, 0, 0).Format("2006-01-02"))
			body["guardianEmail"] = "Guardian@EXAMPLE.com"
			apiDo(t, anon, "POST", "/api/v1/account-requests", body, http.StatusOK)
			req := pendingRequest(t, app, "teen@example.com")
			if req.GuardianEmail != "guardian@example.com" {
				t.Fatalf("nomination: %q", req.GuardianEmail)
			}
			var links int64
			app.DB.Model(&models.GuardianLink{}).Count(&links)
			if links != 0 {
				t.Fatal("public nomination created link")
			}
			var teenCount int64
			app.DB.Model(&models.User{}).Where("email = ?", req.Email).Count(&teenCount)
			if teenCount != 0 {
				t.Fatal("public nomination created account")
			}
			admin := loginAPI(t, app, "admin", "pw")
			apiDo(t, admin, "POST", "/api/v1/admin/account-requests/"+itoa(req.ID)+"/approve", map[string]any{}, http.StatusOK)
			var link models.GuardianLink
			app.DB.First(&link)
			if known {
				if link.TeenUserID == nil || link.Status != models.GuardianPending || link.ConfirmedBy != nil {
					t.Fatalf("link: %+v", link)
				}
				if auditCount(t, app, audit.GuardianLinkRequested) != 1 {
					t.Fatal("missing nomination audit")
				}
			} else {
				if link.ID != 0 {
					t.Fatal("unknown nomination created link")
				}
				var guardianCount int64
				app.DB.Model(&models.User{}).Where("email = ?", req.GuardianEmail).Count(&guardianCount)
				if guardianCount != 0 {
					t.Fatal("unknown nomination created placeholder User")
				}
				nominations, err := unresolvedGuardianNominations(app.DB)
				if err != nil || len(nominations) != 1 || nominations[0].GuardianEmail != req.GuardianEmail {
					t.Fatalf("retained nomination: %+v %v", nominations, err)
				}
				app.DB.Model(&models.User{}).Where("id = ?", nominations[0].TeenUserID).Update("email", "teen-renamed@example.com")
				nominations, err = unresolvedGuardianNominations(app.DB)
				if err != nil || len(nominations) != 1 {
					t.Fatalf("email change lost nomination: %+v %v", nominations, err)
				}
			}
		})
	}
}

func TestGuardianClaimIsUniformPendingAndLimited(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	guardian := newTestUser(t, app, "guardian", "pw", models.RoleMember)
	teen := guardianTestTeen(t, app, "teen")
	client := loginAPI(t, app, "guardian", "pw")
	for _, email := range []string{teen.Email, "unknown@example.com", guardian.Email, teen.Email} {
		w := apiDo(t, client, "POST", "/api/v1/me/guardian-claims", map[string]string{"email": email}, http.StatusOK)
		if !strings.Contains(w, guardianClaimThanks) {
			t.Fatalf("claim leaks result: %s", w)
		}
	}
	var links []models.GuardianLink
	app.DB.Find(&links)
	if len(links) != 1 || links[0].Status != models.GuardianPending || links[0].GuardianUserID != guardian.ID {
		t.Fatalf("claims: %+v", links)
	}
	admin := loginAPI(t, app, "admin", "pw")
	apiDo(t, admin, "POST", "/api/v1/admin/guardian-links/"+itoa(links[0].ID)+"/confirm", map[string]string{"checkNote": "known family"}, http.StatusOK)
	app.DB.First(&links[0], links[0].ID)
	if links[0].Status != models.GuardianConfirmed {
		t.Fatal("admin could not confirm teen account link")
	}
	// The address cap is uniform too, and cannot create a second link.
	for range 5 {
		apiDo(t, client, "POST", "/api/v1/me/guardian-claims", map[string]string{"email": teen.Email}, http.StatusOK)
	}
	var count int64
	app.DB.Model(&models.GuardianLink{}).Count(&count)
	if count != 1 {
		t.Fatalf("duplicate links: %d", count)
	}
}
