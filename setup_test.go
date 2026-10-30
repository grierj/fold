package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"fold/models"

	"gorm.io/gorm"
)

func validSetupInput() setupInput {
	return setupInput{SiteName: "Grace Church", Timezone: "America/Denver", Name: "Root", Email: "root@example.com", Password: "password123", Confirm: "password123", Adult: true}
}

func TestSetupInputReportsEveryField(t *testing.T) {
	in := validSetupInput()
	in.Timezone, in.Password, in.Confirm = "Mars/Olympus", "tiny!", "different!"
	fields := in.fieldErrors()
	if len(fields) != 3 || fields["timezone"] == "" || fields["password"] == "" || fields["password_confirm"] == "" {
		t.Fatalf("want all three errors, got %v", fields)
	}
	data := in.pageData(fields, "")
	if data["FirstError"] != "timezone" {
		t.Fatalf("first error: %v", data["FirstError"])
	}
	summary := data["ErrorSummary"].([]setupFieldError)
	var ids []string
	for _, field := range summary {
		ids = append(ids, field.ID)
	}
	if !reflect.DeepEqual(ids, []string{"timezone", "password", "password_confirm"}) {
		t.Fatalf("summary order: %v", ids)
	}
	if data["AdultConfirmed"] != true || data["EnteredSiteName"] != in.SiteName || data["EnteredEmail"] != in.Email {
		t.Fatalf("non-secret answers lost: %v", data)
	}
	for _, value := range data {
		if text, ok := value.(string); ok && (strings.Contains(text, in.Password) || strings.Contains(text, in.Confirm)) {
			t.Fatal("password entered template data")
		}
	}
}

func TestSetupInputValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alter func(*setupInput)
		field string
	}{
		{"church name", func(in *setupInput) { in.SiteName = "" }, "site_name"},
		{"empty time zone", func(in *setupInput) { in.Timezone = "" }, "timezone"},
		{"name", func(in *setupInput) { in.Name = "" }, "name"},
		{"empty email", func(in *setupInput) { in.Email = "" }, "email"},
		{"invalid email", func(in *setupInput) { in.Email = "not-an-email" }, "email"},
		{"short password", func(in *setupInput) { in.Password, in.Confirm = "tiny!", "tiny!" }, "password"},
		{"empty confirmation", func(in *setupInput) { in.Confirm = "" }, "password_confirm"},
		{"mismatch", func(in *setupInput) { in.Confirm = "another1234" }, "password_confirm"},
		{"adult affirmation", func(in *setupInput) { in.Adult = false }, "age_band"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := validSetupInput()
			tc.alter(&in)
			if got := in.fieldErrors(); len(got) != 1 || got[tc.field] == "" {
				t.Fatalf("want only %s, got %v", tc.field, got)
			}
		})
	}
	if got := (setupInput{}).fieldErrors(); len(got) != 7 {
		t.Fatalf("empty form: %v", got)
	}
	in := validSetupInput()
	in.Password, in.Confirm = strings.Repeat(" ", minPasswordLength), strings.Repeat(" ", minPasswordLength)
	if got := in.fieldErrors(); len(got) != 0 {
		t.Fatalf("changed existing password whitespace semantics: %v", got)
	}
}

func postSetupForm(t *testing.T, app *App, values url.Values) *httptest.ResponseRecorder {
	t.Helper()
	router := app.setupRouter()
	get := httptest.NewRecorder()
	router.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/setup", nil))
	token := regexp.MustCompile(`name="csrf_token" value="([^"]+)"`).FindStringSubmatch(get.Body.String())
	if len(token) != 2 {
		t.Fatalf("no setup CSRF token: %d %s", get.Code, get.Body.String())
	}
	form := url.Values{}
	for key, value := range values {
		form[key] = append([]string(nil), value...)
	}
	form.Set("csrf_token", token[1])
	request := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, cookie := range get.Result().Cookies() {
		request.AddCookie(cookie)
	}
	out := httptest.NewRecorder()
	router.ServeHTTP(out, request)
	return out
}

func setupControl(t *testing.T, body, id string) string {
	t.Helper()
	for _, input := range regexp.MustCompile(`<input\b[^>]*>`).FindAllString(body, -1) {
		if strings.Contains(input, `id="`+id+`"`) {
			return input
		}
	}
	t.Fatalf("missing setup control %s", id)
	return ""
}

func TestSetupErrorsLinkToFieldsAndPreserveAnswers(t *testing.T) {
	app := testApp(t)
	out := postSetupForm(t, app, url.Values{
		"site_name": {" Grace <Church> "}, "timezone": {"Mars/Olympus"},
		"name": {" Root "}, "email": {"ROOT@EXAMPLE.COM"}, "age_band": {"adult"},
		"password": {"tiny!"}, "password_confirm": {"different!"},
	})
	if out.Code != http.StatusBadRequest {
		t.Fatalf("setup: %d %s", out.Code, out.Body.String())
	}
	body := out.Body.String()
	for _, id := range []string{"timezone", "password", "password_confirm"} {
		control := setupControl(t, body, id)
		if !strings.Contains(control, `aria-invalid="true"`) || !strings.Contains(control, id+"-error") ||
			!strings.Contains(body, `id="`+id+`-error"`) || !strings.Contains(body, `href="#`+id+`"`) {
			t.Errorf("missing inline error or summary link for %s: %s", id, control)
		}
	}
	if !strings.Contains(setupControl(t, body, "timezone"), "autofocus") || strings.Count(body, "autofocus") != 1 {
		t.Error("first error must receive the only autofocus")
	}
	for id, value := range map[string]string{"site_name": "Grace &lt;Church&gt;", "timezone": "Mars/Olympus", "name": "Root", "email": "root@example.com"} {
		if !strings.Contains(setupControl(t, body, id), `value="`+value+`"`) {
			t.Errorf("lost %s", id)
		}
	}
	if !strings.Contains(setupControl(t, body, "age_band"), "checked") {
		t.Error("adult affirmation lost")
	}
	for _, id := range []string{"password", "password_confirm"} {
		if strings.Contains(setupControl(t, body, id), "value=") {
			t.Errorf("password control %s has a value", id)
		}
	}
	if strings.Contains(body, "tiny!") || strings.Contains(body, "different!") || strings.Contains(body, "IANA") || strings.Contains(body, "Church / site") {
		t.Error("secret or obsolete copy in setup response")
	}
	if strings.Index(body, `id="tz-hint"`) > strings.Index(body, `id="timezone"`) || strings.Index(body, `id="password-hint"`) > strings.Index(body, `id="password"`) {
		t.Error("hint must precede its control")
	}
	var users int64
	if err := app.DB.Model(&models.User{}).Count(&users).Error; err != nil || users != 0 {
		t.Fatalf("invalid setup stored an account: count %d, error %v", users, err)
	}
}

func TestSetupEmptyFormReportsAllFields(t *testing.T) {
	app := testApp(t)
	out := postSetupForm(t, app, url.Values{"site_name": {"   "}, "name": {"   "}})
	if out.Code != http.StatusBadRequest {
		t.Fatalf("setup: %d %s", out.Code, out.Body.String())
	}
	for _, id := range []string{"site_name", "timezone", "name", "email", "password", "password_confirm", "age_band"} {
		if control := setupControl(t, out.Body.String(), id); !strings.Contains(control, `aria-invalid="true"`) {
			t.Errorf("%s not marked invalid: %s", id, control)
		}
	}
}

func TestSetupUnavailableDoesNotCreateAnAdmin(t *testing.T) {
	app := testApp(t)
	const callback = "test_setup_count_failure"
	if err := app.DB.Callback().Query().Before("gorm:query").Register(callback, func(db *gorm.DB) {
		if _, ok := db.Statement.Model.(*models.User); ok {
			db.AddError(errors.New("forced setup count failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.DB.Callback().Query().Remove(callback) })
	out := postSetupForm(t, app, url.Values{
		"site_name": {"Grace Church"}, "timezone": {"America/Denver"},
		"name": {"Root"}, "email": {"root@example.com"}, "age_band": {"adult"},
		"password": {"password123"}, "password_confirm": {"password123"},
	})
	if out.Code != http.StatusServiceUnavailable {
		t.Fatalf("setup count failure: %d %s", out.Code, out.Body.String())
	}
	assertSetupGenericErrorRecovery(t, out.Body.String())
	get := httptest.NewRecorder()
	app.setupRouter().ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/setup", nil))
	if get.Code != http.StatusServiceUnavailable {
		t.Fatalf("setup GET failure: %d", get.Code)
	}
	assertSetupGenericErrorRecovery(t, get.Body.String())
	var users int64
	if err := app.DB.Raw("SELECT COUNT(*) FROM users").Scan(&users).Error; err != nil || users != 0 {
		t.Fatalf("created an admin without verifying setup eligibility: %d, %v", users, err)
	}
}

func TestSetupRespectsCachedCompletion(t *testing.T) {
	app := testApp(t)
	user := models.User{Email: "root@example.com", AccountType: models.AccountAdult, Name: "Root", Role: models.RoleAdmin, Status: models.UserActive}
	if err := app.DB.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if ready, err := app.setupComplete(); err != nil || !ready {
		t.Fatalf("completion: %v, %v", ready, err)
	}
	if err := app.DB.Delete(&user).Error; err != nil {
		t.Fatal(err)
	}
	out := httptest.NewRecorder()
	app.setupRouter().ServeHTTP(out, httptest.NewRequest(http.MethodGet, "/setup", nil))
	if out.Code != http.StatusSeeOther || out.Header().Get("Location") != "/" {
		t.Fatalf("setup reopened after cached completion: %d", out.Code)
	}
}

func assertSetupGenericErrorRecovery(t *testing.T, body string) {
	t.Helper()
	for _, want := range []string{
		`id="setup-error"`, `role="alert" tabindex="-1" autofocus`,
		`href="#setup-form"`, `id="setup-form" tabindex="-1" aria-describedby="setup-error"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("generic setup error missing %s", want)
		}
	}
	if strings.Count(body, "autofocus") != 1 {
		t.Error("generic setup error must receive the only autofocus")
	}
}

func TestSetupCreateFailureOffersFocusedRecovery(t *testing.T) {
	app := testApp(t)
	const callback = "test_setup_create_failure"
	if err := app.DB.Callback().Create().Before("gorm:create").Register(callback, func(db *gorm.DB) {
		if _, ok := db.Statement.Model.(*models.User); ok {
			db.AddError(errors.New("forced setup create failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.DB.Callback().Create().Remove(callback) })
	out := postSetupForm(t, app, url.Values{
		"site_name": {"Grace Church"}, "timezone": {"America/Denver"},
		"name": {"Root"}, "email": {"root@example.com"}, "age_band": {"adult"},
		"password": {"password123"}, "password_confirm": {"password123"},
	})
	if out.Code != http.StatusBadRequest {
		t.Fatalf("setup create failure: %d %s", out.Code, out.Body.String())
	}
	assertSetupGenericErrorRecovery(t, out.Body.String())
	if !strings.Contains(setupControl(t, out.Body.String(), "site_name"), `value="Grace Church"`) {
		t.Error("generic setup error lost valid answers")
	}
	for _, id := range []string{"password", "password_confirm"} {
		if strings.Contains(setupControl(t, out.Body.String(), id), "value=") {
			t.Errorf("password control %s has a value", id)
		}
	}
}

func TestSetupInputRejectsPasswordOver72Bytes(t *testing.T) {
	in := validSetupInput()
	in.Password, in.Confirm = strings.Repeat("a", 73), strings.Repeat("a", 73)
	if got := in.fieldErrors(); len(got) != 1 || got["password"] == "" {
		t.Fatalf("want only password, got %v", got)
	}
	in.Password, in.Confirm = strings.Repeat("a", 72), strings.Repeat("a", 72)
	if got := in.fieldErrors(); len(got) != 0 {
		t.Fatalf("72 bytes: %v", got)
	}
}
