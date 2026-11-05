package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fold/internal/notify"
	"fold/models"

	"gorm.io/gorm"
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

// consentTestPauseBeforeLock pauses the one request that is about to take
// userID's setConsent lock: a Row callback that fires once, when the SQL is
// the deleted_at-reading `FOR NO KEY UPDATE` select on users and its first
// bound value is userID. Release precedes callback removal on cleanup, so a
// failed assertion cannot strand the request.
func consentTestPauseBeforeLock(t *testing.T, db *gorm.DB, userID uint) (<-chan struct{}, func()) {
	t.Helper()
	reached, resume := make(chan struct{}), make(chan struct{})
	var fired atomic.Bool
	name := fmt.Sprintf("test:consent_pause_before_lock:%d", queryCounterSeq.Add(1))
	if err := db.Callback().Row().Before("gorm:row").Register(name, func(tx *gorm.DB) {
		sql := tx.Statement.SQL.String()
		if !strings.Contains(sql, "deleted_at FROM users WHERE id") || !strings.Contains(sql, "FOR NO KEY UPDATE") ||
			len(tx.Statement.Vars) == 0 || fmt.Sprint(tx.Statement.Vars[0]) != fmt.Sprint(userID) || fired.Swap(true) {
			return
		}
		close(reached)
		select {
		case <-resume:
		case <-tx.Statement.Context.Done():
			tx.AddError(tx.Statement.Context.Err())
		}
	}); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release := func() { once.Do(func() { close(resume) }) }
	t.Cleanup(func() { release(); _ = db.Callback().Row().Remove(name) })
	return reached, release
}

// consentTestAsync runs a request in a goroutine and returns what waits for
// its answer.
func consentTestAsync(t *testing.T, request func() *httptest.ResponseRecorder) func() *httptest.ResponseRecorder {
	t.Helper()
	answer := make(chan *httptest.ResponseRecorder, 1)
	go func() { answer <- request() }()
	return func() *httptest.ResponseRecorder {
		t.Helper()
		select {
		case w := <-answer:
			return w
		case <-time.After(10 * time.Second):
			t.Fatal("the request never finished")
			return nil
		}
	}
}

// consentTestSoftDeleteWhile holds userID FOR UPDATE with an uncommitted
// deleted_at, starts the request, waits until a backend is blocked on a lock
// running a statement matching like, then commits the soft delete and
// returns the request's answer.
func consentTestSoftDeleteWhile(t *testing.T, app *App, userID uint, like string, request func() *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	locked, commit := make(chan struct{}), make(chan struct{})
	_, done := startDBSession(t, app.DB, "consent-owner", func(db *gorm.DB) error {
		return db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec("SELECT 1 FROM users WHERE id = ? FOR UPDATE", userID).Error; err != nil {
				return err
			}
			if err := tx.Exec("UPDATE users SET deleted_at = now() WHERE id = ?", userID).Error; err != nil {
				return err
			}
			close(locked)
			select {
			case <-commit:
				return nil
			case <-tx.Statement.Context.Done():
				return tx.Statement.Context.Err()
			}
		})
	})
	var once sync.Once
	release := func() { once.Do(func() { close(commit) }) }
	t.Cleanup(release)
	select {
	case <-locked:
	case err := <-done:
		t.Fatalf("soft-delete session ended early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("soft-delete session never took the lock")
	}
	wait := consentTestAsync(t, request)
	waitForLockedUsersQuery(t, app, like)
	release()
	if err := awaitDBResult(t, done); err != nil {
		t.Fatalf("soft-delete session: %v", err)
	}
	return wait()
}

// consentTestSeed stores one prior consent row with evidence, ip and user
// agent filled in, so a refused write has something to leave untouched.
func consentTestSeed(t *testing.T, app *App, u *models.User, channel, scope, status string) {
	t.Helper()
	if err := app.DB.Create(&models.Consent{UserID: u.ID, Channel: channel, Scope: scope, Status: status,
		Source: models.ConsentSourceWebForm, EvidenceText: "Seeded evidence", IP: "203.0.113.9", UserAgent: "Seed agent"}).Error; err != nil {
		t.Fatal(err)
	}
}

// consentTestRows is every consent row of the person as JSON, in id order.
func consentTestRows(t *testing.T, app *App, userID uint) []string {
	t.Helper()
	var rows []string
	if err := app.DB.Raw("SELECT to_jsonb(c)::text FROM consents c WHERE user_id = ? ORDER BY id", userID).Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

// consentTestWriter is one of the five setConsent writers, with the prior
// row it is seeded over and what a gone owner answers.
type consentTestWriter struct {
	name      string
	signedIn  bool
	channel   string
	scope     string
	prior     string // status of the seeded row; the request writes the other one
	gone      int
	goneBody  string
	run       func(t *testing.T, app *App, u *models.User, c *apiClient) *httptest.ResponseRecorder
	newStatus string
}

func consentTestWriters() []consentTestWriter {
	const groups = "category:groups"
	return []consentTestWriter{
		{name: "one-click unsubscribe", channel: models.ConsentEmail, scope: groups, prior: models.ConsentOptedIn,
			newStatus: models.ConsentOptedOut, gone: http.StatusNotFound, goneBody: "This unsubscribe link isn't valid.",
			run: func(t *testing.T, app *App, u *models.User, _ *apiClient) *httptest.ResponseRecorder {
				return postOneClick(app, "/u/unsubscribe/"+app.unsubscribeToken(u.ID, groups), "One-click agent")
			}},
		{name: "token page switch", channel: models.ConsentEmail, scope: groups, prior: models.ConsentOptedIn,
			newStatus: models.ConsentOptedOut, gone: http.StatusNotFound, goneBody: "not found",
			run: func(t *testing.T, app *App, u *models.User, _ *apiClient) *httptest.ResponseRecorder {
				c := anonClient(t, app, "203.0.113.20")
				c.holdNotices = true
				return c.do("PUT", "/api/v1/email-settings/"+app.unsubscribeToken(u.ID, groups),
					map[string]any{"category": "groups", "off": true}, true)
			}},
		{name: "signed-in email switch", signedIn: true, channel: models.ConsentEmail, scope: groups, prior: models.ConsentOptedIn,
			newStatus: models.ConsentOptedOut, gone: http.StatusUnauthorized, goneBody: "unauthorized",
			run: func(t *testing.T, app *App, u *models.User, c *apiClient) *httptest.ResponseRecorder {
				return c.do("PUT", "/api/v1/me/notification-settings", map[string]any{"category": "groups", "off": true}, true)
			}},
		{name: "signed-in push switch", signedIn: true, channel: models.ConsentPush, scope: groups, prior: models.ConsentOptedIn,
			newStatus: models.ConsentOptedOut, gone: http.StatusUnauthorized, goneBody: "unauthorized",
			run: func(t *testing.T, app *App, u *models.User, c *apiClient) *httptest.ResponseRecorder {
				return c.do("PUT", "/api/v1/me/notification-settings",
					map[string]any{"channel": "push", "category": "groups", "off": true}, true)
			}},
		{name: "push turn off everywhere", signedIn: true, channel: models.ConsentPush, scope: "all", prior: models.ConsentOptedIn,
			newStatus: models.ConsentOptedOut, gone: http.StatusUnauthorized, goneBody: "unauthorized",
			run: func(t *testing.T, app *App, u *models.User, c *apiClient) *httptest.ResponseRecorder {
				return c.do("POST", "/api/v1/push/turn-off", nil, true)
			}},
	}
}

// consentTestFixture makes the person, signs them in when the writer needs a
// session, and seeds the writer's prior row.
func consentTestFixture(t *testing.T, w consentTestWriter) (*App, *models.User, *apiClient) {
	t.Helper()
	app, _ := pushTestApp(t)
	u := newTestUser(t, app, "lee", "password123456", models.RoleMember)
	var c *apiClient
	if w.signedIn {
		c = loginAPI(t, app, "lee", "password123456")
		c.holdNotices = true
	}
	consentTestSeed(t, app, u, w.channel, w.scope, w.prior)
	return app, u, c
}

// A consent write takes the person's row lock and then looks at the row it
// locked: an owner who was erased (or soft-deleted) while the write waited
// gets no new consent row and the seeded one is untouched (F-9, M42).
func TestConsentWritersRefuseAfterTheOwnerIsSoftDeleted(t *testing.T) {
	for _, w := range consentTestWriters() {
		t.Run(w.name, func(t *testing.T) {
			app, u, c := consentTestFixture(t, w)
			if w.name == "push turn off everywhere" {
				newPushDevice(t, app, u, "lee-phone")
			}
			before := consentTestRows(t, app, u.ID)
			if len(before) != 1 {
				t.Fatalf("seed: %d rows", len(before))
			}
			got := consentTestSoftDeleteWhile(t, app, u.ID, "%FROM users%FOR NO KEY UPDATE%", func() *httptest.ResponseRecorder {
				return w.run(t, app, u, c)
			})
			if got.Code != w.gone || !strings.Contains(got.Body.String(), w.goneBody) {
				t.Fatalf("answer %d %q, want %d containing %q", got.Code, got.Body.String(), w.gone, w.goneBody)
			}
			if after := consentTestRows(t, app, u.ID); !reflect.DeepEqual(before, after) {
				t.Fatalf("consent rows changed for a gone owner:\n before %v\n after  %v", before, after)
			}
			if w.name == "push turn off everywhere" && len(userDevices(app, u)) != 1 {
				t.Fatal("a gone owner's devices were deleted")
			}
		})
	}
}

// The same seeds without the soft delete write what they always did.
func TestConsentWritersAppendOneRowWhenTheOwnerIsLive(t *testing.T) {
	for _, w := range consentTestWriters() {
		t.Run(w.name, func(t *testing.T) {
			app, u, c := consentTestFixture(t, w)
			if got := w.run(t, app, u, c); got.Code != http.StatusOK {
				t.Fatalf("answer %d %q", got.Code, got.Body.String())
			}
			var rows []models.Consent
			app.DB.Where("user_id = ?", u.ID).Order("id").Find(&rows)
			if len(rows) != 2 {
				t.Fatalf("want the seeded row and one new one: %+v", rows)
			}
			if n := rows[1]; n.Channel != w.channel || n.Scope != w.scope || n.Status != w.newStatus {
				t.Fatalf("new row %+v", n)
			}
		})
	}
	t.Run("all optional email", func(t *testing.T) {
		app, _ := pushTestApp(t)
		u := newTestUser(t, app, "lee", "password123456", models.RoleMember)
		c := loginAPI(t, app, "lee", "password123456")
		c.holdNotices = true
		if got := c.do("PUT", "/api/v1/me/notification-settings", map[string]any{"optionalEmailOff": true}, true); got.Code != http.StatusOK {
			t.Fatalf("answer %d %q", got.Code, got.Body.String())
		}
		want := int64(len(notify.OptionalEmailCategories()))
		if n := contactCount(t, app, &models.Consent{}, "user_id = ? AND channel = ? AND status = ?", u.ID, models.ConsentEmail, models.ConsentOptedOut); n != want {
			t.Fatalf("%d opted-out rows, want %d", n, want)
		}
	})
}

// A signed-in switch whose session was ended after the request began is
// refused under the lock: logout bumps logout_version, which setConsent
// compares with the session's.
func TestConsentWritersRefuseAStaleSession(t *testing.T) {
	for _, w := range consentTestWriters() {
		if !w.signedIn {
			continue
		}
		t.Run(w.name, func(t *testing.T) {
			app, u, c := consentTestFixture(t, w)
			before := consentTestRows(t, app, u.ID)
			reached, release := consentTestPauseBeforeLock(t, app.DB, u.ID)
			wait := consentTestAsync(t, func() *httptest.ResponseRecorder { return w.run(t, app, u, c) })
			awaitDBSignal(t, reached)
			// The paused request holds no user lock, so a real logout is not blocked.
			if lw := c.do("POST", "/api/v1/auth/logout", nil, true); lw.Code != http.StatusOK {
				t.Fatalf("logout: %d %s", lw.Code, lw.Body.String())
			}
			release()
			if got := wait(); got.Code != http.StatusUnauthorized {
				t.Fatalf("stale session: %d %s, want 401", got.Code, got.Body.String())
			}
			if after := consentTestRows(t, app, u.ID); !reflect.DeepEqual(before, after) {
				t.Fatalf("consent rows changed for a stale session:\n before %v\n after  %v", before, after)
			}
		})
	}
}
