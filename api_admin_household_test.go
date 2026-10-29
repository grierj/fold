package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"fold/internal/audit"
	"fold/models"
	"gorm.io/gorm"
)

func TestAdminHouseholdDelegation(t *testing.T) {
	app := testApp(t)
	admin := newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	holder := newTestUser(t, app, "Family Holder", "pw", models.RoleMember)
	client := loginAPI(t, app, "admin", "pw")
	path := "/api/v1/admin/users/" + itoa(holder.ID) + "/household"
	for range 2 {
		body := apiDo(t, client, "GET", path, nil, http.StatusOK)
		if !strings.Contains(body, `"household":null`) {
			t.Fatal(body)
		}
	}
	if contactCount(t, app, &models.Household{}, "1=1") != 0 || contactCount(t, app, &models.HouseholdAccount{}, "1=1") != 0 || contactCount(t, app, &models.AuditLog{}, "action = ?", audit.HouseholdCreated) != 0 {
		t.Fatal("GET created household data")
	}
	apiDo(t, client, "POST", path+"/people", map[string]any{"firstName": "Adult", "kind": "adult"}, http.StatusConflict)
	apiDo(t, client, "POST", path, map[string]any{"name": "The Family", "address": "1 Oak St"}, http.StatusCreated)
	apiDo(t, client, "POST", path, map[string]any{"name": "Duplicate"}, http.StatusConflict)
	var household models.Household
	if err := app.DB.First(&household).Error; err != nil {
		t.Fatal(err)
	}
	if household.Name != "The Family" || household.Address != "1 Oak St" || household.CreatedBy != admin.ID {
		t.Fatalf("household: %+v", household)
	}
	var account models.HouseholdAccount
	if err := app.DB.First(&account).Error; err != nil {
		t.Fatal(err)
	}
	if account.UserID != holder.ID || account.Relation != models.HouseholdRelationAdult || contactCount(t, app, &models.HouseholdAccount{}, "user_id = ?", admin.ID) != 0 {
		t.Fatalf("acting admin became a holder: %+v", account)
	}
	var created models.AuditLog
	if err := app.DB.First(&created, "action = ?", audit.HouseholdCreated).Error; err != nil {
		t.Fatal(err)
	}
	var meta map[string]uint
	if err := json.Unmarshal([]byte(created.MetaJSON), &meta); err != nil {
		t.Fatal(err)
	}
	if created.ActorID == nil || *created.ActorID != admin.ID || created.TargetID != household.ID || meta["holderUserId"] != holder.ID {
		t.Fatalf("creation attribution: %+v %v", created, meta)
	}
	// An adult needs no link. Minors never default the guardian to either
	// the holder or the admin, even when the admin created the household.
	apiDo(t, client, "POST", path+"/people", map[string]any{"firstName": "Adult", "kind": "adult", "birthDate": "1990-01-01"}, http.StatusCreated)
	apiDo(t, client, "POST", path+"/people", map[string]any{"firstName": "Child", "kind": "child"}, http.StatusConflict)
	if contactCount(t, app, &models.HouseholdPerson{}, "household_id = ?", household.ID) != 1 {
		t.Fatal("missing guardian left a person")
	}
	apiDo(t, client, "POST", path+"/people", map[string]any{"firstName": "Child", "kind": "child", "guardianUserId": holder.ID}, http.StatusCreated)
	other := newTestUser(t, app, "Second Adult", "pw", models.RoleMember)
	if err := app.DB.Create(&models.HouseholdAccount{HouseholdID: household.ID, UserID: other.ID, Relation: models.HouseholdRelationAdult}).Error; err != nil {
		t.Fatal(err)
	}
	apiDo(t, client, "POST", path+"/people", map[string]any{"firstName": "Teen", "kind": "teen", "guardianUserId": other.ID}, http.StatusCreated)
	for name, guardianID := range map[string]uint{"Child": holder.ID, "Teen": other.ID} {
		var person models.HouseholdPerson
		if err := app.DB.First(&person, "first_name = ?", name).Error; err != nil {
			t.Fatal(err)
		}
		var link models.GuardianLink
		if err := app.DB.First(&link, "person_id = ?", person.ID).Error; err != nil {
			t.Fatal(err)
		}
		if person.CreatedBy != admin.ID || link.GuardianUserID != guardianID || link.Status != models.GuardianPending || link.ConfirmedBy != nil {
			t.Fatalf("person/link attribution: %+v %+v", person, link)
		}
		if contactCount(t, app, &models.Notification{}, "kind = ? AND subject_id = ? AND waiting = true", "household.link_pending", link.ID) == 0 {
			t.Fatal("pending guardian link did not create an admin task")
		}
	}
	body := apiDo(t, client, "GET", path, nil, http.StatusOK)
	if !strings.Contains(body, `"name":"The Family"`) || !strings.Contains(body, `"firstName":"Child"`) || !strings.Contains(body, `"linkStatus":"pending"`) {
		t.Fatal(body)
	}
	// The holder has the same household through self-service; the admin's
	// own household remains absent. No directory, edit, or delete is added.
	holderClient := loginAPI(t, app, "Family Holder", "pw")
	if got := getMyHousehold(t, holderClient); got.ID != household.ID {
		t.Fatalf("holder household: %+v", got)
	}
	apiDo(t, client, "GET", "/api/v1/me/household", nil, http.StatusOK)
	if contactCount(t, app, &models.Household{}, "1=1") != 1 || contactCount(t, app, &models.GuardianLink{}, "guardian_user_id = ?", admin.ID) != 0 {
		t.Fatal("delegation attached acting admin")
	}
}

func TestAdminHouseholdAccessAndEligibility(t *testing.T) {
	app := testApp(t)
	admin := newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	holder := newTestUser(t, app, "holder", "pw", models.RoleMember)
	client := loginAPI(t, app, "admin", "pw")
	member := loginAPI(t, app, "holder", "pw")
	path := "/api/v1/admin/users/" + itoa(holder.ID) + "/household"
	anon := newAPIClient(t, app)
	anon.fetchCSRF()
	for _, route := range []struct{ method, suffix string }{{"GET", ""}, {"POST", ""}, {"POST", "/people"}} {
		apiDo(t, member, route.method, path+route.suffix, map[string]any{}, http.StatusForbidden)
		apiDo(t, anon, route.method, path+route.suffix, map[string]any{}, http.StatusUnauthorized)
	}
	if w := client.do("POST", path, map[string]any{}, false); w.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF: %d %s", w.Code, w.Body.String())
	}
	apiDo(t, client, "GET", "/api/v1/admin/users/999999/household", nil, http.StatusNotFound)
	apiDo(t, client, "POST", "/api/v1/admin/users/999999/household", map[string]any{}, http.StatusNotFound)
	apiDo(t, client, "POST", "/api/v1/admin/users/"+itoa(admin.ID)+"/household", map[string]any{}, http.StatusConflict)
	for _, updates := range []map[string]any{
		{"status": models.UserDisabled},
		{"status": models.UserInvited},
		{"status": models.UserGuest, "password_hash": nil},
		{"status": models.UserActive, "account_type": models.AccountTeen},
	} {
		if err := app.DB.Model(holder).Updates(updates).Error; err != nil {
			t.Fatal(err)
		}
		apiDo(t, client, "GET", path, nil, http.StatusConflict)
		apiDo(t, client, "POST", path, map[string]any{}, http.StatusConflict)
		apiDo(t, client, "POST", path+"/people", map[string]any{"firstName": "Adult", "kind": "adult"}, http.StatusConflict)
	}
	if contactCount(t, app, &models.Household{}, "1=1") != 0 {
		t.Fatal("ineligible holder created household")
	}
}

func TestAdminHouseholdGuardianEligibility(t *testing.T) {
	app := testApp(t)
	admin := newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	holder := newTestUser(t, app, "holder", "pw", models.RoleMember)
	outsider := newTestUser(t, app, "outsider", "pw", models.RoleMember)
	client := loginAPI(t, app, "admin", "pw")
	path := "/api/v1/admin/users/" + itoa(holder.ID) + "/household"
	apiDo(t, client, "POST", path, map[string]any{}, http.StatusCreated)
	for _, guardianID := range []uint{0, admin.ID, outsider.ID, 999999} {
		apiDo(t, client, "POST", path+"/people", map[string]any{"firstName": "Child", "kind": "child", "guardianUserId": guardianID}, http.StatusConflict)
	}
	var account models.HouseholdAccount
	if err := app.DB.First(&account, "user_id = ?", holder.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Create(&models.HouseholdAccount{HouseholdID: account.HouseholdID, UserID: outsider.ID, Relation: models.HouseholdRelationAdult}).Error; err != nil {
		t.Fatal(err)
	}
	for _, updates := range []map[string]any{{"status": models.UserDisabled}, {"status": models.UserGuest}, {"status": models.UserActive, "account_type": models.AccountTeen}} {
		if err := app.DB.Model(outsider).Updates(updates).Error; err != nil {
			t.Fatal(err)
		}
		apiDo(t, client, "POST", path+"/people", map[string]any{"firstName": "Child", "kind": "child", "guardianUserId": outsider.ID}, http.StatusConflict)
	}
	if contactCount(t, app, &models.HouseholdPerson{}, "1=1") != 0 || contactCount(t, app, &models.GuardianLink{}, "1=1") != 0 {
		t.Fatal("ineligible guardian left person or link")
	}
}

func TestAdminHouseholdAtomicWrites(t *testing.T) {
	for _, scenario := range []struct {
		name, table  string
		addingPerson bool
	}{
		{"account link", "household_accounts", false},
		{"creation audit", "audit_logs", false},
		{"guardian link", "guardian_links", true},
		{"notification", "notifications", true},
		{"person audit", "audit_logs", true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			app := testApp(t)
			newTestUser(t, app, "admin", "pw", models.RoleAdmin)
			holder := newTestUser(t, app, "holder", "pw", models.RoleMember)
			client := loginAPI(t, app, "admin", "pw")
			path := "/api/v1/admin/users/" + itoa(holder.ID) + "/household"
			addingPerson := scenario.addingPerson
			if addingPerson {
				apiDo(t, client, "POST", path, map[string]any{}, http.StatusCreated)
			}
			const callback = "admin-household:fail-create"
			if err := app.DB.Callback().Create().Before("gorm:create").Register(callback, func(db *gorm.DB) {
				if db.Statement.Table == scenario.table {
					db.AddError(errors.New("injected household write failure"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { app.DB.Callback().Create().Remove(callback) })
			if addingPerson {
				apiDo(t, client, "POST", path+"/people", map[string]any{"firstName": "Child", "kind": "child", "guardianUserId": holder.ID}, http.StatusInternalServerError)
			} else {
				apiDo(t, client, "POST", path, map[string]any{}, http.StatusInternalServerError)
			}
			wantHouseholds := int64(0)
			if addingPerson {
				wantHouseholds = 1
			}
			if contactCount(t, app, &models.Household{}, "1=1") != wantHouseholds || contactCount(t, app, &models.HouseholdAccount{}, "1=1") != wantHouseholds || contactCount(t, app, &models.HouseholdPerson{}, "1=1") != 0 || contactCount(t, app, &models.GuardianLink{}, "1=1") != 0 || contactCount(t, app, &models.AuditLog{}, "action = ?", audit.HouseholdPersonAdded) != 0 || contactCount(t, app, &models.AuditLog{}, "action = ?", audit.HouseholdCreated) != wantHouseholds || contactCount(t, app, &models.Notification{}, "kind = ?", "household.link_pending") != 0 {
				t.Fatal("failed write left partial household data")
			}
		})
	}
}

func TestAdminHouseholdRechecksActorAfterContactGate(t *testing.T) {
	app := testApp(t)
	admin := newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	holder := newTestUser(t, app, "holder", "pw", models.RoleMember)
	client := loginAPI(t, app, "admin", "pw")
	gate := holdContactGate(t, app)
	response := queueContact(func() *httptest.ResponseRecorder {
		return client.do("POST", "/api/v1/admin/users/"+itoa(holder.ID)+"/household", map[string]any{}, true)
	})
	waitContactWaiters(t, app, 1)
	if err := gate.Model(admin).Update("role", models.RoleMember).Error; err != nil {
		t.Fatal(err)
	}
	if err := gate.Commit().Error; err != nil {
		t.Fatal(err)
	}
	if w := awaitContactResponse(t, response); w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked actor: %d %s", w.Code, w.Body.String())
	}
	if contactCount(t, app, &models.Household{}, "1=1") != 0 {
		t.Fatal("revoked actor created household")
	}
}

func TestAdminHouseholdInputValidation(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	holder := newTestUser(t, app, "holder", "pw", models.RoleMember)
	client := loginAPI(t, app, "admin", "pw")
	path := "/api/v1/admin/users/" + itoa(holder.ID) + "/household"
	for _, body := range []map[string]any{{"name": " "}, {"name": strings.Repeat("x", 101)}, {"address": strings.Repeat("x", 501)}} {
		apiDo(t, client, "POST", path, body, http.StatusBadRequest)
	}
	if contactCount(t, app, &models.Household{}, "1=1") != 0 {
		t.Fatal("invalid create left household")
	}
	apiDo(t, client, "POST", path, map[string]any{}, http.StatusCreated)
	for _, body := range []map[string]any{
		{"kind": "adult"}, {"firstName": strings.Repeat("x", 101), "kind": "adult"},
		{"firstName": "Adult", "kind": "alien"}, {"firstName": "Adult", "kind": "adult", "birthDate": "not a date"},
		{"firstName": "Adult", "kind": "adult", "careNotes": strings.Repeat("x", 5001)},
	} {
		apiDo(t, client, "POST", path+"/people", body, http.StatusBadRequest)
	}
	apiDo(t, client, "POST", path+"/people", map[string]any{"firstName": "Minor", "kind": "adult", "birthDate": "2020-01-01"}, http.StatusConflict)
	if contactCount(t, app, &models.HouseholdPerson{}, "1=1") != 0 {
		t.Fatal("invalid person left data")
	}
}

func TestAdminHouseholdConcurrentCreationConflicts(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	holder := newTestUser(t, app, "holder", "pw", models.RoleMember)
	first := loginAPI(t, app, "admin", "pw")
	second := loginAPI(t, app, "admin", "pw")
	path := "/api/v1/admin/users/" + itoa(holder.ID) + "/household"
	gate := holdContactGate(t, app)
	one := queueContact(func() *httptest.ResponseRecorder { return first.do("POST", path, map[string]any{}, true) })
	two := queueContact(func() *httptest.ResponseRecorder { return second.do("POST", path, map[string]any{}, true) })
	waitContactWaiters(t, app, 2)
	if err := gate.Commit().Error; err != nil {
		t.Fatal(err)
	}
	codes := map[int]int{}
	codes[awaitContactResponse(t, one).Code]++
	codes[awaitContactResponse(t, two).Code]++
	if codes[http.StatusCreated] != 1 || codes[http.StatusConflict] != 1 {
		t.Fatalf("concurrent creation: %v", codes)
	}
	if contactCount(t, app, &models.Household{}, "1=1") != 1 || contactCount(t, app, &models.HouseholdAccount{}, "user_id = ?", holder.ID) != 1 || contactCount(t, app, &models.AuditLog{}, "action = ?", audit.HouseholdCreated) != 1 {
		t.Fatal("concurrent creation duplicated household data")
	}
}
