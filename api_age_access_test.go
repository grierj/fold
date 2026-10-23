package main

import (
	"net/http"
	"testing"
	"time"

	"fold/models"
)

func TestAdminCreationAgeGate(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "root", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "root", "password1234")
	for _, tc := range []struct {
		name, band, birth, role string
		status                  int
	}{
		{"missing", "", "", "member", 400},
		{"child", "under13", "2020-01-01", "member", 400},
		{"mismatch", "adult", "2020-01-01", "member", 400},
		{"teenadmin", "teen", time.Now().AddDate(-15, 0, 0).Format("2006-01-02"), "admin", 400},
		{"teen", "teen", time.Now().AddDate(-15, 0, 0).Format("2006-01-02"), "member", 201},
		{"adult", "adult", "", "member", 201},
	} {
		t.Run(tc.name, func(t *testing.T) {
			email := tc.name + "@example.com"
			w := admin.do("POST", "/api/v1/admin/users", map[string]string{"name": tc.name, "email": email, "password": "password1234", "role": tc.role, "ageBand": tc.band, "birthDate": tc.birth}, true)
			if w.Code != tc.status {
				t.Fatalf("create: %d %s", w.Code, w.Body.String())
			}
			var users []models.User
			app.DB.Where("email = ?", email).Find(&users)
			if tc.status != 201 {
				if len(users) != 0 {
					t.Fatal("rejected input stored a user")
				}
				return
			}
			if len(users) != 1 {
				t.Fatal("user missing")
			}
			if tc.band == "teen" && (users[0].Status != models.UserPendingConsent || users[0].BirthDate == nil) {
				t.Fatalf("teen activated: %+v", users[0])
			}
			if tc.band == "adult" && (users[0].Status != models.UserActive || users[0].BirthDate != nil) {
				t.Fatal("adult behavior changed")
			}
		})
	}
}

func TestPendingTeenSessionRestrictedAcrossAllAPIs(t *testing.T) {
	app := testApp(t)
	teen := newTestUser(t, app, "teen", "password1234", models.RoleMember)
	app.DB.Model(teen).Updates(map[string]any{"account_type": models.AccountTeen, "status": models.UserPendingConsent})
	client := loginAPI(t, app, "teen", "password1234")
	for _, path := range []string{"/api/v1/auth/me", "/api/v1/auth/csrf"} {
		w := client.do("GET", path, nil, false)
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("session route %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/api/v1/site", "/api/v1/groups", "/api/v1/groups/1", "/api/v1/events", "/api/v1/events/1", "/api/v1/calendar", "/api/v1/me/groups", "/api/v1/notifications", "/api/v1/admin/users", "/api/v1/me/household"} {
		w := client.do("GET", path, nil, false)
		if w.Code != http.StatusForbidden || w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("restricted GET %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/api/v1/me", "/api/v1/groups", "/api/v1/events/1/registrations", "/api/v1/account-requests"} {
		method := "POST"
		if path == "/api/v1/me" || path == "/api/v1/events/1/registrations" {
			method = "PUT"
		}
		w := client.do(method, path, map[string]string{}, true)
		if w.Code != http.StatusForbidden {
			t.Errorf("restricted mutation %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	w := client.do("POST", "/api/v1/auth/logout", nil, true)
	if w.Code != 200 {
		t.Fatalf("logout: %d %s", w.Code, w.Body.String())
	}
	if w := client.do("GET", "/api/v1/groups", nil, false); w.Code != 200 {
		t.Fatalf("signed-out public access: %d", w.Code)
	}
}

func TestInvitationAgeGatePreservesTokenAndGuest(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "root", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "root", "password1234")
	guest := models.User{Email: "newteen@example.com", Name: "Guest", Status: models.UserGuest, Role: models.RoleMember, AccountType: models.AccountAdult}
	if err := app.DB.Create(&guest).Error; err != nil {
		t.Fatal(err)
	}
	w := admin.do("POST", "/api/v1/invitations", map[string]string{"name": "New teen", "email": guest.Email}, true)
	if w.Code != 201 {
		t.Fatalf("invite: %d %s", w.Code, w.Body.String())
	}
	token := tokenInEmail(t, latestEmail(t, app, guest.Email))
	anon := newAPIClient(t, app)
	anon.fetchCSRF()
	for _, band := range []string{"", "under13"} {
		w = anon.do("POST", "/api/v1/invitations/accept", map[string]string{"token": token, "name": "New teen", "password": "password1234", "ageBand": band}, true)
		if w.Code != 400 {
			t.Fatalf("gate %q: %d %s", band, w.Code, w.Body.String())
		}
		var unchanged models.User
		app.DB.First(&unchanged, guest.ID)
		if unchanged.Status != models.UserGuest || unchanged.PasswordHash != nil {
			t.Fatal("rejected upgrade changed guest")
		}
		var inv models.Invitation
		app.DB.Where("email = ?", guest.Email).First(&inv)
		if inv.AcceptedAt != nil {
			t.Fatal("age refusal consumed invitation")
		}
	}
	w = anon.do("POST", "/api/v1/invitations/accept", map[string]string{"token": token, "name": "New teen", "password": "password1234", "ageBand": "teen", "birthDate": time.Now().AddDate(-15, 0, 0).Format("2006-01-02")}, true)
	if w.Code != 200 {
		t.Fatalf("teen accept: %d %s", w.Code, w.Body.String())
	}
	var teen models.User
	app.DB.First(&teen, guest.ID)
	if teen.Status != models.UserPendingConsent || teen.AccountType != models.AccountTeen || teen.PasswordHash == nil {
		t.Fatalf("upgrade: %+v", teen)
	}
	if w := anon.do("GET", "/api/v1/auth/me", nil, false); w.Code != 200 {
		t.Fatalf("missing restricted session: %d", w.Code)
	}
	if w := anon.do("GET", "/api/v1/groups", nil, false); w.Code != 403 {
		t.Fatalf("invitation widened access: %d", w.Code)
	}
}

func TestTeenGroupRosterPrivacyAndAvatarBoundary(t *testing.T) {
	app := testApp(t)
	admin := newTestUser(t, app, "root", "password1234", models.RoleAdmin)
	teen := newTestUser(t, app, "teen", "password1234", models.RoleMember)
	member := newTestUser(t, app, "member", "password1234", models.RoleMember)
	app.DB.Model(teen).Updates(map[string]any{"account_type": models.AccountTeen, "avatar_key": "private-teen-avatar"})
	group := models.Group{Name: "Youth", Status: models.GroupActive, Visibility: models.VisibilityPublic, LeaderID: admin.ID}
	if err := app.DB.Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	for _, u := range []*models.User{admin, teen, member} {
		app.DB.Create(&models.Membership{UserID: u.ID, GroupID: group.ID, Status: "active", Role: models.MembershipMember, JoinedAt: time.Now()})
	}
	for _, tc := range []struct {
		name         string
		minorVisible bool
	}{{"root", true}, {"member", false}} {
		client := loginAPI(t, app, tc.name, "password1234")
		w := client.do("GET", "/api/v1/groups/"+itoa(group.ID)+"/members", nil, false)
		if w.Code != 200 {
			t.Fatalf("roster: %d %s", w.Code, w.Body.String())
		}
		var payload struct {
			Members []memberJSON `json:"members"`
		}
		decodeJSON(t, w, &payload)
		found := false
		for _, row := range payload.Members {
			if row.ID == teen.ID {
				found = true
				if row.AccountType != models.AccountTeen || row.AvatarURL != "" {
					t.Fatalf("minor roster: %+v", row)
				}
			}
		}
		if found != tc.minorVisible {
			t.Fatalf("%s visibility %v", tc.name, found)
		}
	}
	client := loginAPI(t, app, "teen", "password1234")
	w := client.do("POST", "/api/v1/me/avatar", nil, true)
	if w.Code != 403 {
		t.Fatalf("teen avatar upload: %d %s", w.Code, w.Body.String())
	}
}
