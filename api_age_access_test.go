package main

import (
	"strings"
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

func TestPendingTeenBrowsesPubliclyWithoutAnAuthenticatedSession(t *testing.T) {
	app := testApp(t)
	admin := newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	teen := newTestUser(t, app, "teen", "password1234", models.RoleMember)
	legacy := loginAPI(t, app, "teen", "password1234")
	app.DB.Model(teen).Updates(map[string]any{"account_type": models.AccountTeen, "status": models.UserPendingConsent})
	public := activeGroup(t, app, admin, "Public study", models.VisibilityPublic, "")
	private := activeGroup(t, app, admin, "Private study", models.VisibilityPrivate, "")
	event, _ := seedEvent(t, app, &public, admin.ID, time.Now().Add(24*time.Hour))
	client := newAPIClient(t, app)
	client.fetchCSRF()
	if w := client.do("POST", "/api/v1/auth/login", map[string]string{"email": teen.Email, "password": "wrong"}, true); w.Code != 401 {
		t.Fatalf("wrong password disclosed consent: %d %s", w.Code, w.Body.String())
	}
	if w := client.do("POST", "/api/v1/auth/login", map[string]string{"email": teen.Email, "password": "password1234"}, true); w.Code != 403 || !strings.Contains(w.Body.String(), `"code":"consent_required"`) {
		t.Fatalf("missing consent explanation: %d %s", w.Code, w.Body.String())
	}
	for name, browser := range map[string]*apiClient{"new login": client, "old cookie": legacy} {
		t.Run(name, func(t *testing.T) {
			for _, path := range []string{"/api/v1/site", "/api/v1/groups", "/api/v1/groups/" + itoa(public.ID), "/api/v1/events", "/api/v1/events/" + itoa(event.ID), "/api/v1/calendar"} {
				w := browser.do("GET", path, nil, false)
				if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), private.Name) {
					t.Errorf("public GET %s: %d %s", path, w.Code, w.Body.String())
				}
			}
			for _, path := range []string{"/api/v1/auth/me", "/api/v1/me/groups", "/api/v1/notifications", "/api/v1/admin/users", "/api/v1/me/household"} {
				if w := browser.do("GET", path, nil, false); w.Code != 401 {
					t.Errorf("authenticated GET %s: %d %s", path, w.Code, w.Body.String())
				}
			}
			for _, path := range []string{"/api/v1/me", "/api/v1/events/" + itoa(event.ID) + "/registrations"} {
				if w := browser.do("PUT", path, map[string]string{}, true); w.Code != 401 {
					t.Errorf("authenticated mutation %s: %d %s", path, w.Code, w.Body.String())
				}
			}
		})
	}
	// Consent enables a fresh login, never a legacy pending cookie.
	adminClient := loginAPI(t, app, "admin", "password1234")
	apiDo(t, adminClient, "POST", consentPath(teen), map[string]string{"scope": "account", "status": "granted", "evidenceText": "Parent signed consent"}, 200)
	apiDo(t, legacy, "GET", "/api/v1/auth/me", nil, 401)
	if code, _ := tryLogin(t, app, "", teen.Email, "password1234"); code != 200 {
		t.Fatalf("consented teen could not sign in: %d", code)
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
	if w := anon.do("GET", "/api/v1/auth/me", nil, false); w.Code != 401 {
		t.Fatalf("pending invitation created a login: %d", w.Code)
	}
	if w := anon.do("GET", "/api/v1/groups", nil, false); w.Code != 200 {
		t.Fatalf("invitation blocked public browsing: %d", w.Code)
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
