package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"fold/internal/notify"
	"fold/models"
)

func TestUnsubscribeTokens(t *testing.T) {
	app := testApp(t)
	tok := app.unsubscribeToken(42, "category:groups")
	id, scope, err := app.parseUnsubscribeToken(tok)
	if err != nil || id != 42 || scope != "category:groups" {
		t.Fatalf("round trip: %d %q %v", id, scope, err)
	}
	other := &App{Config: Config{SessionSecret: []byte("a different secret entirely, long enough")}}
	flipped := []byte(tok)
	flipped[len(flipped)/2] ^= 1
	for name, bad := range map[string]string{
		"tampered":             string(flipped),
		"another secret":       other.unsubscribeToken(42, "category:groups"),
		"not base64":           "%%%",
		"too short":            "AQ",
		"a scope we don't use": app.unsubscribeToken(42, "category:safety"),
		"no user":              app.unsubscribeToken(0, "category:groups"),
	} {
		if _, _, err := app.parseUnsubscribeToken(bad); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// approveGroupFor gives the leader an optional email (their group was
// approved, category groups) and returns the leader's latest outbox row.
func approveGroupFor(t *testing.T, app *App, leader, admin *apiClient, name string) models.MailOutbox {
	t.Helper()
	var body struct {
		Group groupSummaryJSON `json:"group"`
	}
	decodeJSON(t, leader.do("POST", "/api/v1/groups", map[string]any{"name": name}, true), &body)
	if w := admin.do("POST", "/api/v1/admin/groups/"+itoa(body.Group.ID)+"/approve", map[string]any{}, true); w.Code != http.StatusOK {
		t.Fatalf("approve: %d", w.Code)
	}
	var row models.MailOutbox
	app.DB.Where("to_address = ?", testEmail("lee")).Order("id DESC").First(&row)
	return row
}

func postOneClick(app *App, path, ua string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString("List-Unsubscribe=One-Click"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", ua)
	w := httptest.NewRecorder()
	app.setupRouter().ServeHTTP(w, req)
	return w
}

// Optional email carries a one-click link and a footer; the one-click POST
// works with no cookie or CSRF token, turns off that kind of email at once
// with an appended consent row, and leaves required mail alone (M3.3).
func TestOneClickUnsubscribe(t *testing.T) {
	app := testApp(t)
	app.DB.Create(&models.Setting{Key: settingChurchAddress, Value: "Grace Church\r\n 12 Main St \n\nBoise, ID 83702"})
	admin := newTestUser(t, app, "root", "password1234", models.RoleAdmin)
	lee := newTestUser(t, app, "lee", "password1234", models.RoleMember)
	adminClient, leeClient := loginAPI(t, app, "root", "password1234"), loginAPI(t, app, "lee", "password1234")

	mail := approveGroupFor(t, app, leeClient, adminClient, "Book club")
	base := app.emailBaseURL()
	if !strings.HasPrefix(mail.ListUnsubscribe, base+"/u/unsubscribe/") {
		t.Fatalf("no one-click link: %q", mail.ListUnsubscribe)
	}
	token := strings.TrimPrefix(mail.ListUnsubscribe, base+"/u/unsubscribe/")
	if !strings.Contains(mail.Body, "You're getting group updates from") || !strings.Contains(mail.Body, base+"/email-settings/"+token) ||
		!strings.Contains(mail.Body, "\nGrace Church\n12 Main St\nBoise, ID 83702") {
		t.Fatalf("footer: %q", mail.Body)
	}
	// Required mail (the admin's new-group task) has neither.
	var task models.MailOutbox
	app.DB.Where("to_address = ?", admin.Email).Order("id DESC").First(&task)
	if task.ListUnsubscribe != "" || strings.Contains(task.Body, "/email-settings/") {
		t.Fatalf("required mail offered unsubscribe: %+v", task)
	}

	path := "/u/unsubscribe/" + token
	if w := postOneClick(app, "/u/unsubscribe/"+token+"x", "Mail"); w.Code != http.StatusNotFound {
		t.Fatalf("tampered: %d", w.Code)
	}
	if contactCount(t, app, &models.Consent{}, "user_id = ?", lee.ID) != 0 {
		t.Fatal("a tampered link changed something")
	}
	for i := 0; i < 2; i++ {
		if w := postOneClick(app, path, "Gmail one-click"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "unsubscribed") {
			t.Fatalf("one-click: %d %s", w.Code, w.Body.String())
		}
	}
	var rows []models.Consent
	app.DB.Where("user_id = ?", lee.ID).Find(&rows)
	if len(rows) != 1 || rows[0].Scope != "category:groups" || rows[0].Status != models.ConsentOptedOut ||
		rows[0].Source != models.ConsentSourceUnsubscribe || rows[0].UserAgent != "Gmail one-click" || rows[0].IP == "" || rows[0].At.IsZero() {
		t.Fatalf("consent rows (one, even after a repeat): %+v", rows)
	}

	// The next group update is suppressed; other kinds and required mail
	// still go.
	approveGroupFor(t, app, leeClient, adminClient, "Second club")
	var n models.Notification
	app.DB.Where("user_id = ? AND kind = ?", lee.ID, notify.GroupApproved).Order("id DESC").First(&n)
	if contactCount(t, app, &models.NotificationDelivery{}, "notification_id = ? AND status = ? AND reason = ?", n.ID, models.DeliverySuppressed, "optional email turned off") != 1 {
		t.Fatal("the unsubscribed kind still sends")
	}
	var task2 models.Notification
	app.DB.Where("user_id = ? AND kind = ?", admin.ID, notify.GroupPending).Order("id DESC").First(&task2)
	if contactCount(t, app, &models.NotificationDelivery{}, "notification_id = ? AND status = ?", task2.ID, models.DeliverySent) != 1 {
		t.Fatal("required mail stopped")
	}
	if w := postOneClick(app, "/u/unsubscribe/"+app.unsubscribeToken(lee.ID, "category:events"), "x"); w.Code != http.StatusOK ||
		contactCount(t, app, &models.Consent{}, "user_id = ?", lee.ID) != 2 {
		t.Fatal("a second kind is its own row")
	}
	// A deleted account's links stop working.
	app.DB.Delete(&models.User{}, lee.ID)
	if w := postOneClick(app, path, "x"); w.Code != http.StatusNotFound {
		t.Fatalf("deleted user: %d", w.Code)
	}
}

// Outside dev, the one-click header goes out only over https (RFC 8058);
// the footer link is there either way.
func TestOneClickNeedsHTTPS(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "root", "password1234", models.RoleAdmin)
	newTestUser(t, app, "lee", "password1234", models.RoleMember)
	adminClient, leeClient := loginAPI(t, app, "root", "password1234"), loginAPI(t, app, "lee", "password1234")
	app.Config.Env = "prod"
	app.Config.SiteURL = "http://groups.example.org"
	if mail := approveGroupFor(t, app, leeClient, adminClient, "Plain http"); mail.ListUnsubscribe != "" || !strings.Contains(mail.Body, "http://groups.example.org/email-settings/") {
		t.Fatalf("http: %+v", mail)
	}
	app.Config.SiteURL = "https://groups.example.org"
	if mail := approveGroupFor(t, app, leeClient, adminClient, "Over https"); !strings.HasPrefix(mail.ListUnsubscribe, "https://groups.example.org/u/unsubscribe/") {
		t.Fatalf("https: %+v", mail)
	}
}
