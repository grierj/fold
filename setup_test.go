package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func TestSetupRequiresOperatorCredential(t *testing.T) {
	app := testApp(t)
	router := app.setupRouter()
	get := httptest.NewRecorder()
	router.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/setup", nil))
	if strings.Contains(get.Body.String(), `name="site_name"`) {
		t.Fatal("anonymous visitor can open the setup wizard")
	}
	csrf := regexp.MustCompile(`name="csrf_token" value="([^"]+)"`).FindStringSubmatch(get.Body.String())
	if len(csrf) != 2 {
		t.Fatal("missing CSRF token")
	}
	values := url.Values{
		"site_name": {"Taken"}, "timezone": {"America/Denver"},
		"name": {"Attacker"}, "email": {"attacker@example.com"}, "age_band": {"adult"},
		"password": {"password123"}, "password_confirm": {"password123"},
		"csrf_token": {csrf[1]},
	}
	req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, cookie := range get.Result().Cookies() {
		req.AddCookie(cookie)
	}
	post := httptest.NewRecorder()
	router.ServeHTTP(post, req)
	if post.Code != http.StatusForbidden {
		t.Fatalf("anonymous POST: %d", post.Code)
	}
	var users int64
	if err := app.DB.Model(&models.User{}).Count(&users).Error; err != nil || users != 0 {
		t.Fatalf("anonymous setup created %d users (POST %d): %v", users, post.Code, err)
	}
}

// setupRequest gives existing wizard tests a signed authorized session. The
// unlock flow itself is exercised separately through HTTP.
func setupRequest(t *testing.T, app *App, method, path string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	session, err := app.Store.Get(req, sessionName)
	if err != nil {
		t.Fatal(err)
	}
	session.Values[setupSessionKey] = setupTokenHash(app.Config.SetupToken)
	w := httptest.NewRecorder()
	if err := session.Save(req, w); err != nil {
		t.Fatal(err)
	}
	for _, cookie := range w.Result().Cookies() {
		req.AddCookie(cookie)
	}
	return req
}

func TestSetupUnlock(t *testing.T) {
	app := testApp(t)
	router := app.setupRouter()
	get := httptest.NewRecorder()
	router.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/setup", nil))
	csrf := regexp.MustCompile(`name="csrf_token" value="([^"]+)"`).FindStringSubmatch(get.Body.String())[1]
	post := func(code string, withCSRF bool) *httptest.ResponseRecorder {
		values := url.Values{"setup_token": {code}}
		if withCSRF {
			values.Set("csrf_token", csrf)
		}
		req := httptest.NewRequest(http.MethodPost, "/setup/unlock", strings.NewReader(values.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for _, cookie := range get.Result().Cookies() {
			req.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	if w := post(app.Config.SetupToken, false); w.Code != http.StatusForbidden {
		t.Fatalf("unlock without CSRF: %d", w.Code)
	}
	if w := post("wrong-code", true); w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "wrong-code") {
		t.Fatalf("wrong code: %d", w.Code)
	}
	w := post(app.Config.SetupToken, true)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/setup" {
		t.Fatalf("unlock: %d %s", w.Code, w.Body.String())
	}
	req := httptest.NewRequest(http.MethodGet, "/setup", nil)
	for _, cookie := range w.Result().Cookies() {
		req.AddCookie(cookie)
	}
	opened := httptest.NewRecorder()
	router.ServeHTTP(opened, req)
	if !strings.Contains(opened.Body.String(), `name="site_name"`) || strings.Contains(opened.Body.String(), app.Config.SetupToken) {
		t.Fatal("authorized setup form missing or exposed the code")
	}
	app.Config.SetupToken = strings.Repeat("b", 64)
	rotated := httptest.NewRecorder()
	router.ServeHTTP(rotated, req)
	if strings.Contains(rotated.Body.String(), `name="site_name"`) {
		t.Fatal("old setup session remained valid after code changed")
	}
}

func TestConcurrentSetupCreatesOneAdmin(t *testing.T) {
	app := testApp(t)
	// Separate App instances model processes with separate completion caches.
	other := testAppForSetup(app)
	routers := []*gin.Engine{app.setupRouter(), other.setupRouter()}
	requests := make([]*http.Request, 2)
	for i, instance := range []*App{app, other} {
		get := httptest.NewRecorder()
		routers[i].ServeHTTP(get, setupRequest(t, instance, http.MethodGet, "/setup"))
		csrf := regexp.MustCompile(`name="csrf_token" value="([^"]+)"`).FindStringSubmatch(get.Body.String())[1]
		values := url.Values{
			"csrf_token": {csrf}, "site_name": {fmt.Sprintf("Church %d", i)}, "timezone": {"America/Denver"},
			"name": {"Root"}, "email": {fmt.Sprintf("root%d@example.com", i)}, "age_band": {"adult"},
			"password": {"password123"}, "password_confirm": {"password123"},
		}
		requests[i] = httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(values.Encode()))
		requests[i].Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for _, cookie := range get.Result().Cookies() {
			requests[i].AddCookie(cookie)
		}
	}
	// Make both requests pass the initial, unlocked count before either can
	// create an account. The count inside the transaction must decide the race.
	var count atomic.Int32
	arrived, release := make(chan struct{}, 2), make(chan struct{})
	const callback = "test_setup_race"
	if err := app.DB.Callback().Query().After("gorm:query").Register(callback, func(db *gorm.DB) {
		if _, ok := db.Statement.Model.(*models.User); ok && count.Add(1) <= 2 {
			arrived <- struct{}{}
			<-release
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.DB.Callback().Query().Remove(callback) })
	responses := make([]*httptest.ResponseRecorder, 2)
	var wg sync.WaitGroup
	for i := range requests {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			responses[i] = httptest.NewRecorder()
			routers[i].ServeHTTP(responses[i], requests[i])
		}(i)
	}
	<-arrived
	<-arrived
	close(release)
	wg.Wait()
	var users []models.User
	if err := app.DB.Find(&users).Error; err != nil || len(users) != 1 || users[0].Role != models.RoleAdmin {
		t.Fatalf("want one admin, got %+v: %v", users, err)
	}
	winner := 0
	if users[0].Email == "root1@example.com" {
		winner = 1
	}
	if responses[winner].Header().Get("Location") != "/login" || responses[1-winner].Header().Get("Location") != "/" {
		t.Fatalf("unexpected redirects: %q, %q", responses[0].Header().Get("Location"), responses[1].Header().Get("Location"))
	}
	var site models.Setting
	if err := app.DB.Where("key = ?", "site_name").First(&site).Error; err != nil || site.Value != fmt.Sprintf("Church %d", winner) {
		t.Fatalf("settings do not belong to winning admin: %+v, %v", site, err)
	}
}

func testAppForSetup(app *App) *App {
	return &App{DB: app.DB, Store: app.Store, Config: app.Config, Uploads: app.Uploads, Mailer: app.Mailer}
}

func TestSetupRollsBackWhenSettingFails(t *testing.T) {
	app := testApp(t)
	var before []models.Setting
	if err := app.DB.Where("key IN ?", []string{"site_name", settingTimezone}).Order("key").Find(&before).Error; err != nil {
		t.Fatal(err)
	}
	var writes atomic.Int32
	fail := func(db *gorm.DB) {
		if _, ok := db.Statement.Model.(*models.Setting); ok && writes.Add(1) == 2 {
			db.AddError(errors.New("forced settings failure"))
		}
	}
	const callback = "test_setup_setting_failure"
	if err := app.DB.Callback().Create().Before("gorm:create").Register(callback, fail); err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Callback().Update().Before("gorm:update").Register(callback, fail); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.DB.Callback().Create().Remove(callback); app.DB.Callback().Update().Remove(callback) })
	w := postSetupForm(t, app, url.Values{
		"site_name": {"Grace Church"}, "timezone": {"America/Denver"}, "name": {"Root"},
		"email": {"root@example.com"}, "age_band": {"adult"}, "password": {"password123"}, "password_confirm": {"password123"},
	})
	var users int64
	if err := app.DB.Model(&models.User{}).Count(&users).Error; err != nil || users != 0 || w.Code != http.StatusBadRequest {
		t.Fatalf("failed setup left %d users, response %d, error %v", users, w.Code, err)
	}
	var after []models.Setting
	if err := app.DB.Where("key IN ?", []string{"site_name", settingTimezone}).Order("key").Find(&after).Error; err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("settings changed after rollback: %v -> %v, error %v", before, after, err)
	}
}

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
		{"nine emoji are not ten characters", func(in *setupInput) { in.Password, in.Confirm = strings.Repeat("😀", 9), strings.Repeat("😀", 9) }, "password"},
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
	router.ServeHTTP(get, setupRequest(t, app, http.MethodGet, "/setup"))
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
	app.setupRouter().ServeHTTP(get, setupRequest(t, app, http.MethodGet, "/setup"))
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
