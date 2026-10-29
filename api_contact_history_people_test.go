package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"fold/models"
)

const contactHistoryPeoplePath = "/api/v1/admin/contact-history/people"

func getContactHistoryPeople(t *testing.T, client *apiClient, query string) contactHistoryPeopleJSON {
	t.Helper()
	w := client.do("GET", contactHistoryPeoplePath+query, nil, false)
	if w.Code != http.StatusOK {
		t.Fatalf("people %d: %s", w.Code, w.Body.String())
	}
	var out contactHistoryPeopleJSON
	decodeJSON(t, w, &out)
	if out.People == nil {
		t.Fatal("people must be a non-nil array")
	}
	return out
}

func contactHistoryPeopleHasID(people []contactHistoryPersonJSON, id uint) bool {
	want := historyID(id)
	for _, person := range people {
		if person.ID == want {
			return true
		}
	}
	return false
}

func TestContactHistoryPeopleAuthorization(t *testing.T) {
	f := newContactAPIFixture(t, false)
	if w := newAPIClient(t, f.app).do("GET", contactHistoryPeoplePath, nil, false); w.Code != http.StatusUnauthorized {
		t.Fatalf("signed out: %d %s", w.Code, w.Body.String())
	}
	if w := f.lc.do("GET", contactHistoryPeoplePath, nil, false); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "admin required") {
		t.Fatalf("member: %d %s", w.Code, w.Body.String())
	}
	if f.admin.Status != models.UserActive || f.admin.AccountType != models.AccountAdult {
		t.Fatal("fixture admin must be an active adult")
	}
	if people := getContactHistoryPeople(t, f.ac, "").People; len(people) == 0 {
		t.Fatal("admin did not receive people")
	}
}

func TestContactHistoryPeopleSearchIsLiteralCaseInsensitiveAndExcludesDeleted(t *testing.T) {
	f := newContactAPIFixture(t, false)
	mixedName := newTestUser(t, f.app, "MiXeD Picker Name", "password1234", models.RoleMember)
	mixedEmail := newTestUser(t, f.app, "email-picker", "password1234", models.RoleMember)
	mixedEmail.Email = "Case.Search.Picker@example.com"
	if err := f.app.DB.Save(mixedEmail).Error; err != nil {
		t.Fatal(err)
	}
	displayName := newTestUser(t, f.app, "display-picker", "password1234", models.RoleMember)
	displayName.DisplayName = "DiSpLaY Picker Alias"
	if err := f.app.DB.Save(displayName).Error; err != nil {
		t.Fatal(err)
	}
	literal := newTestUser(t, f.app, "Literal % Picker", "password1234", models.RoleMember)
	deleted := newTestUser(t, f.app, "Deleted Picker Person", "password1234", models.RoleMember)
	if err := f.app.DB.Delete(deleted).Error; err != nil {
		t.Fatal(err)
	}
	invited := newTestUser(t, f.app, "Invited Picker Person", "password1234", models.RoleMember)
	if err := f.app.DB.Model(invited).Update("status", models.UserInvited).Error; err != nil {
		t.Fatal(err)
	}

	for query, want := range map[string]*models.User{
		"mixed picker name":              mixedName,
		"CASE.SEARCH.PICKER@EXAMPLE.COM": mixedEmail,
		"display picker alias":           displayName,
		"%":                              literal,
		"invited picker":                 invited,
	} {
		people := getContactHistoryPeople(t, f.ac, "?q="+url.QueryEscape(query)).People
		if !contactHistoryPeopleHasID(people, want.ID) {
			t.Fatalf("query %q missing %d: %+v", query, want.ID, people)
		}
		if query == "%" && len(people) != 1 {
			t.Fatalf("literal wildcard matched other people: %+v", people)
		}
	}
	if people := getContactHistoryPeople(t, f.ac, "?q=deleted+picker").People; contactHistoryPeopleHasID(people, deleted.ID) {
		t.Fatalf("deleted person returned: %+v", people)
	}
	if people := getContactHistoryPeople(t, f.ac, "?q="+url.QueryEscape(strings.Repeat("界", 255))).People; len(people) != 0 {
		t.Fatalf("overlong query returned people: %+v", people)
	}
}

func TestContactHistoryPeopleCapsResultsAndKeepsExactClosedDTO(t *testing.T) {
	f := newContactAPIFixture(t, false)
	for i := 0; i < 55; i++ {
		u := models.User{Email: fmt.Sprintf("cap-picker-%02d@example.com", i), Name: fmt.Sprintf("Cap Picker %02d", i), Role: models.RoleMember, Status: models.UserActive, AccountType: models.AccountAdult}
		if err := f.app.DB.Create(&u).Error; err != nil {
			t.Fatal(err)
		}
	}
	if people := getContactHistoryPeople(t, f.ac, "?q=cap+picker").People; len(people) != contactHistoryPeopleLimit {
		t.Fatalf("cap: got %d people", len(people))
	}
	if people := getContactHistoryPeople(t, f.ac, "").People; len(people) != contactHistoryPeopleLimit {
		t.Fatalf("empty-query cap: got %d people", len(people))
	}

	const exactID = uint(9007199254740993)
	exact := models.User{ID: exactID, Email: "exact-picker@example.com", Phone: "private-phone", Role: models.RoleMember, Status: models.UserActive, AccountType: models.AccountAdult}
	if err := f.app.DB.Create(&exact).Error; err != nil {
		t.Fatal(err)
	}
	want := contactHistoryPersonJSON{ID: "9007199254740993", Name: exact.PublicName(), Email: exact.Email}
	var exactBody string
	for _, query := range []string{
		"?q=exact-picker%40example.com",
		"?id=9007199254740993&q=not-found",
		"?id=9007199254740993&q=%00",
		"?email=EXACT-PICKER%40EXAMPLE.COM&q=not-found",
	} {
		w := f.ac.do("GET", contactHistoryPeoplePath+query, nil, false)
		if w.Code != http.StatusOK {
			t.Fatalf("exact lookup %s: %d %s", query, w.Code, w.Body.String())
		}
		var out contactHistoryPeopleJSON
		decodeJSON(t, w, &out)
		if len(out.People) != 1 || out.People[0] != want {
			t.Fatalf("exact lookup %s: %+v", query, out.People)
		}
		exactBody = w.Body.String()
	}
	for _, forbidden := range []string{`"phone"`, `"birthDate"`, `"status"`, "private-phone"} {
		if strings.Contains(exactBody, forbidden) {
			t.Fatalf("response disclosed %q: %s", forbidden, exactBody)
		}
	}

	deleted := newTestUser(t, f.app, "deleted-exact-picker", "password1234", models.RoleMember)
	if err := f.app.DB.Delete(deleted).Error; err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		"?id=",
		"?id=0",
		"?id=-1",
		"?id=abc&q=exact-picker",
		"?id=9223372036854775808",
		"?id=9007199254740992",
		"?id=" + historyID(deleted.ID),
		"?email=unknown%40example.com&q=exact-picker",
		"?email=" + url.QueryEscape(strings.Repeat("x", 255)),
		"?q=%00",
		"?email=%00",
	} {
		if people := getContactHistoryPeople(t, f.ac, query).People; len(people) != 0 {
			t.Fatalf("lookup %s returned people: %+v", query, people)
		}
	}
}
