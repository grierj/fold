package main

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"fold/internal/mail"
	"fold/models"
)

type tokenSettings struct {
	EmailSettings emailSettingsJSON `json:"emailSettings"`
	Email         string            `json:"email"`
	CanTurnOn     bool              `json:"canTurnOn"`
	FromCategory  string            `json:"fromCategory"`
}

func kindOff(s emailSettingsJSON, cat string) (bool, bool) {
	for _, k := range s.Kinds {
		if k.Category == cat {
			return k.Off, true
		}
	}
	return false, false
}

// The signed-out page reads and changes one person's switches by the link
// alone (it still needs the CSRF token); a bad link is a 404 that changes
// nothing; members don't see admin notices; every change appends a row.
func TestEmailSettingsByLink(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "root", "password1234", models.RoleAdmin)
	lee := newTestUser(t, app, "lee", "password1234", models.RoleMember)
	anon := newAPIClient(t, app)
	anon.fetchCSRF() // signed out, like the page: a CSRF token but no session
	path := "/api/v1/email-settings/" + app.unsubscribeToken(lee.ID, "category:groups", lee.Email)

	var got tokenSettings
	decodeJSON(t, anon.do("GET", path, nil, false), &got)
	if got.Email != "l•••@"+strings.SplitN(lee.Email, "@", 2)[1] || got.FromCategory != "groups" || got.EmailSettings.OptionalEmailOff {
		t.Fatalf("read: %+v", got)
	}
	if _, admin := kindOff(got.EmailSettings, "admin"); admin || len(got.EmailSettings.Kinds) != 3 {
		t.Fatalf("a member's kinds: %+v", got.EmailSettings.Kinds)
	}
	if w := anon.do("GET", path+"x", nil, false); w.Code != http.StatusNotFound {
		t.Fatalf("bad link: %d", w.Code)
	}
	if w := anon.do("PUT", path, map[string]any{"category": "events", "off": true}, false); w.Code != http.StatusForbidden {
		t.Fatalf("without the CSRF token: %d", w.Code)
	}
	if w := anon.do("PUT", path, map[string]any{"category": "admin", "off": true}, true); w.Code != http.StatusBadRequest ||
		!strings.Contains(w.Body.String(), "email_kind_unknown") {
		t.Fatalf("a kind the member doesn't get: %d %s", w.Code, w.Body.String())
	}
	decodeJSON(t, anon.do("PUT", path, map[string]any{"category": "events", "off": true}, true), &got)
	if off, _ := kindOff(got.EmailSettings, "events"); !off || got.EmailSettings.OptionalEmailOff {
		t.Fatalf("events off: %+v", got.EmailSettings)
	}
	decodeJSON(t, anon.do("PUT", path, map[string]any{"off": true}, true), &got)
	if !got.EmailSettings.OptionalEmailOff {
		t.Fatalf("all off: %+v", got.EmailSettings)
	}
	var rows []models.Consent
	app.DB.Where("user_id = ?", lee.ID).Order("id").Find(&rows)
	// events, then every other optional kind (events already off; admin
	// notices too, so it survives becoming an admin): four rows.
	if len(rows) != 4 || rows[0].Source != models.ConsentSourceUnsubscribe {
		t.Fatalf("consent rows: %+v", rows)
	}

	// Signed in, Me shows the same state and takes the same changes.
	leeClient := loginAPI(t, app, "lee", "password1234")
	var me struct {
		Settings notificationSettingsJSON `json:"settings"`
	}
	decodeJSON(t, leeClient.do("PUT", "/api/v1/me/notification-settings", map[string]any{"category": "groups", "off": false}, true), &me)
	if me.Settings.OptionalEmailOff || len(me.Settings.Kinds) != 3 {
		t.Fatalf("me: %+v", me.Settings)
	}
	var last models.Consent
	app.DB.Where("user_id = ?", lee.ID).Order("id DESC").First(&last)
	if last.Source != models.ConsentSourceWebForm || last.Scope != "category:groups" || last.Status != models.ConsentOptedIn {
		t.Fatalf("me row: %+v", last)
	}
	// An admin also gets admin notices.
	root := loginAPI(t, app, "root", "password1234")
	decodeJSON(t, root.do("GET", "/api/v1/me/notification-settings", nil, false), &me)
	if _, ok := kindOff(emailSettingsJSON{Kinds: me.Settings.Kinds}, "admin"); !ok {
		t.Fatalf("admin kinds: %+v", me.Settings.Kinds)
	}
}

func TestChurchAddressSetting(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "root", "password1234", models.RoleAdmin)
	newTestUser(t, app, "sam", "password1234", models.RoleMember)
	root, sam := loginAPI(t, app, "root", "password1234"), loginAPI(t, app, "sam", "password1234")
	if w := sam.do("PUT", "/api/v1/admin/settings/church-address", map[string]any{"address": "x"}, true); w.Code != http.StatusForbidden {
		t.Fatalf("member: %d", w.Code)
	}
	w := root.do("PUT", "/api/v1/admin/settings/church-address", map[string]any{"address": " Grace Church \r\n\r\n12 Main St\nBoise, ID "}, true)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"Grace Church\n12 Main St\nBoise, ID"`) {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	if settingText(t, app, settingChurchAddress) != "Grace Church\n12 Main St\nBoise, ID" || auditCount(t, app, "setting.church_address_changed") != 1 {
		t.Fatal("not saved or not audited")
	}
	for _, bad := range []string{"a\u0000b", "tab\there", "rtl\u202eoverride", "line\u2028sep"} {
		if w := root.do("PUT", "/api/v1/admin/settings/church-address", map[string]any{"address": bad}, true); w.Code != http.StatusBadRequest ||
			!strings.Contains(w.Body.String(), "church_address_characters") {
			t.Fatalf("%q: %d %s", bad, w.Code, w.Body.String())
		}
	}
	if w := root.do("PUT", "/api/v1/admin/settings/church-address", map[string]any{"address": strings.Repeat("x\n", 7)}, true); w.Code != http.StatusBadRequest ||
		!strings.Contains(w.Body.String(), "church_address_long") {
		t.Fatalf("too long: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(root.do("GET", "/api/v1/admin/settings/church-address", nil, false).Body.String(), "12 Main St") {
		t.Fatal("read back")
	}
	if w := root.do("PUT", "/api/v1/admin/settings/church-address", map[string]any{"address": ""}, true); w.Code != http.StatusOK || settingText(t, app, settingChurchAddress) != "" {
		t.Fatalf("clear: %d %q", w.Code, settingText(t, app, settingChurchAddress))
	}
}

func wantEmailSettingsStatus(t *testing.T, got *httptest.ResponseRecorder, status int) {
	t.Helper()
	if got.Code != status {
		t.Fatalf("email settings answered %d %s, want %d", got.Code, got.Body.String(), status)
	}
}

func retainedEmailChoices(t *testing.T, app *App) []models.EmailAddressOptOut {
	t.Helper()
	var rows []models.EmailAddressOptOut
	if err := app.DB.Order("address_hash, scope").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

func wantRetainedEmailChoice(t *testing.T, app *App, address, category string, want bool) {
	t.Helper()
	n := contactCount(t, app, &models.EmailAddressOptOut{}, "address_hash = ? AND scope = ?", mail.AddressHash(address), categoryScope(category))
	if (n == 1) != want || n > 1 {
		t.Fatalf("retained %s choice for %s: %d, want present=%v", category, address, n, want)
	}
}

func wantAffirmativeEmailConsent(t *testing.T, app *App, u *models.User, category, source, ip, ua string) {
	t.Helper()
	rows := consentTestRowsOf(t, app, u.ID)
	if len(rows) != 1 {
		t.Fatalf("want one affirmative row, got %+v", rows)
	}
	r := rows[0]
	if r.Channel != models.ConsentEmail || r.Scope != categoryScope(category) || r.Status != models.ConsentOptedIn ||
		r.Source != source || r.IP != ip || r.UserAgent != ua || r.At.IsZero() {
		t.Fatalf("affirmative choice lost evidence: %+v", r)
	}
}

func TestEmailTurnOnClearsOnlyTheHeldAddressAndScope(t *testing.T) {
	for _, signedIn := range []bool{true, false} {
		t.Run(fmt.Sprintf("signed in=%v", signedIn), func(t *testing.T) {
			app := testApp(t)
			u := newTestUser(t, app, "holder", "password1234", models.RoleMember)
			seedAllEmailAddressOff(t, app, u.Email)
			seedAllEmailAddressOff(t, app, "other-holder@example.com")
			c, path, source := newAPIClient(t, app), "/api/v1/email-settings/"+app.unsubscribeToken(u.ID, "category:events", u.Email), models.ConsentSourceUnsubscribe
			if signedIn {
				c = loginAPI(t, app, "holder", "password1234")
				path, source = "/api/v1/me/notification-settings", models.ConsentSourceWebForm
			}
			c.ip, c.ua, c.holdNotices = "203.0.113.21", "Current holder", true
			c.fetchCSRF()
			wantEmailSettingsStatus(t, c.do("PUT", path, map[string]any{"category": "events", "off": false}, true), http.StatusOK)
			wantAffirmativeEmailConsent(t, app, u, "events", source, c.ip, c.ua)
			wantRetainedEmailChoice(t, app, u.Email, "events", false)
			if len(retainedEmailChoices(t, app)) != 7 {
				t.Fatal("turn-on changed another scope or address")
			}
			before := consentTestRows(t, app, u.ID)
			wantEmailSettingsStatus(t, c.do("PUT", path, map[string]any{"category": "events", "off": false}, true), http.StatusOK)
			if !reflect.DeepEqual(before, consentTestRows(t, app, u.ID)) {
				t.Fatal("repeat turn-on appended another row")
			}
			// DEC-97 includes admin notices even for a member.
			wantEmailSettingsStatus(t, c.do("PUT", path, map[string]any{"off": false}, true), http.StatusOK)
			if contactCount(t, app, &models.EmailAddressOptOut{}, "address_hash = ?", mail.AddressHash(u.Email)) != 0 ||
				contactCount(t, app, &models.EmailAddressOptOut{}, "address_hash = ?", mail.AddressHash("other-holder@example.com")) != 4 ||
				contactCount(t, app, &models.Consent{}, "user_id = ? AND status = ?", u.ID, models.ConsentOptedIn) != 4 {
				t.Fatal("turn-on all did not clear and affirm all four scopes for only this holder")
			}
		})
	}
}

func TestUnboundEmailLinksRefuseTurnOnUniformly(t *testing.T) {
	for _, version := range []string{"v1", "other address", "tampered address MAC"} {
		t.Run(version, func(t *testing.T) {
			var refusal string
			for _, retained := range []bool{false, true} {
				t.Run(fmt.Sprintf("retained=%v", retained), func(t *testing.T) {
					app := testApp(t)
					u := newTestUser(t, app, "holder", "password1234", models.RoleMember)
					token := app.unsubscribeToken(u.ID, "category:events", "previous@example.com")
					if version == "v1" {
						token = legacyUnsubscribeToken(app, u.ID, "category:events")
					} else if version == "tampered address MAC" {
						raw, err := base64.RawURLEncoding.DecodeString(app.unsubscribeToken(u.ID, "category:events", u.Email))
						if err != nil {
							t.Fatal(err)
						}
						raw[len(raw)-1] ^= 1
						token = base64.RawURLEncoding.EncodeToString(raw)
					}
					if retained {
						seedAllEmailAddressOff(t, app, u.Email)
					}
					c := newAPIClient(t, app)
					c.fetchCSRF()
					path := "/api/v1/email-settings/" + token
					var settings tokenSettings
					decodeJSON(t, c.do("GET", path, nil, false), &settings)
					if settings.CanTurnOn {
						t.Fatal("unbound link offered turn-on")
					}
					var current tokenSettings
					decodeJSON(t, c.do("GET", "/api/v1/email-settings/"+app.unsubscribeToken(u.ID, "category:events", u.Email), nil, false), &current)
					if !current.CanTurnOn {
						t.Fatal("current link lost turn-on authority")
					}
					before, records := consentTestRows(t, app, u.ID), retainedEmailChoices(t, app)
					for _, body := range []map[string]any{{"category": "events", "off": false}, {"off": false}} {
						w := c.do("PUT", path, body, true)
						wantEmailSettingsStatus(t, w, http.StatusForbidden)
						if !strings.Contains(w.Body.String(), `"code":"email_link_turn_on"`) {
							t.Fatal(w.Body.String())
						}
						if refusal == "" {
							refusal = w.Body.String()
						} else if refusal != w.Body.String() {
							t.Fatal("refusal revealed retained-choice existence")
						}
						if !reflect.DeepEqual(before, consentTestRows(t, app, u.ID)) || !reflect.DeepEqual(records, retainedEmailChoices(t, app)) {
							t.Fatal("refusal wrote a choice")
						}
					}
					wantEmailSettingsStatus(t, c.do("PUT", path, map[string]any{"category": "groups", "off": true}, true), http.StatusOK)
					wantEmailSettingsStatus(t, postOneClick(app, "/u/unsubscribe/"+token, "Old mail client"), http.StatusOK)
					if contactCount(t, app, &models.Consent{}, "user_id = ? AND status = ?", u.ID, models.ConsentOptedOut) != 2 || !reflect.DeepEqual(records, retainedEmailChoices(t, app)) {
						t.Fatal("old link did not retain opt-out authority, or changed records")
					}
				})
			}
		})
	}
}

func TestErasedEmailLinksNeverResolveThroughTheAddress(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "root", "password1234", models.RoleAdmin)
	u := newTestUser(t, app, "holder", "password1234", models.RoleMember)
	tokens := []string{legacyUnsubscribeToken(app, u.ID, "category:events"), app.unsubscribeToken(u.ID, "category:events", u.Email)}
	admin := loginAPI(t, app, "root", "password1234")
	wantEmailSettingsStatus(t, admin.do("DELETE", "/api/v1/admin/users/"+itoa(u.ID), nil, true), http.StatusOK)
	replacement := newTestUser(t, app, u.Email, "password1234", models.RoleMember)
	seedAllEmailAddressOff(t, app, replacement.Email)
	before, records := consentTestRows(t, app, replacement.ID), retainedEmailChoices(t, app)
	anon := newAPIClient(t, app)
	anon.fetchCSRF()
	for _, token := range tokens {
		path := "/api/v1/email-settings/" + token
		wantEmailSettingsStatus(t, anon.do("GET", path, nil, false), http.StatusNotFound)
		for _, off := range []bool{false, true} {
			wantEmailSettingsStatus(t, anon.do("PUT", path, map[string]any{"off": off}, true), http.StatusNotFound)
		}
		wantEmailSettingsStatus(t, postOneClick(app, "/u/unsubscribe/"+token, "Old mail client"), http.StatusNotFound)
	}
	if !reflect.DeepEqual(before, consentTestRows(t, app, replacement.ID)) || !reflect.DeepEqual(records, retainedEmailChoices(t, app)) || len(consentTestRows(t, app, u.ID)) != 0 {
		t.Fatal("erased links changed the replacement holder's choices")
	}
}

// emailAddressChangeFixture uses each real address-change API. The session used
// for turn-on is a different browser from the one requesting verification, so
// verification cannot refresh its old session epoch.
func emailAddressChangeFixture(t *testing.T, mode string) (*App, *models.User, *apiClient, string, func() *httptest.ResponseRecorder) {
	t.Helper()
	app := testApp(t)
	newTestUser(t, app, "root", "password1234", models.RoleAdmin)
	u := newTestUser(t, app, "holder", "password1234", models.RoleMember)
	c := loginAPI(t, app, "holder", "password1234")
	c.ip, c.ua, c.holdNotices = "203.0.113.22", "Signed-in holder", true
	const nextAddress = "changed-holder@example.com"
	seedEmailAddressOff(t, app, u.Email, "events")
	seedEmailAddressOff(t, app, nextAddress, "events")
	if mode == "verified" {
		requester := loginAPI(t, app, "holder", "password1234")
		wantEmailSettingsStatus(t, requester.do("PUT", "/api/v1/me", map[string]string{
			"email": nextAddress, "currentPassword": "password1234",
		}, true), http.StatusOK)
		token := tokenInEmail(t, latestEmail(t, app, nextAddress))
		requester.holdNotices = true
		return app, u, c, nextAddress, func() *httptest.ResponseRecorder {
			return requester.do("POST", "/api/v1/me/email/verify", map[string]string{"token": token}, true)
		}
	}
	admin := loginAPI(t, app, "root", "password1234")
	admin.holdNotices = true
	return app, u, c, nextAddress, func() *httptest.ResponseRecorder {
		return admin.do("PUT", "/api/v1/admin/users/"+itoa(u.ID), map[string]string{"email": nextAddress}, true)
	}
}

func wantMovedEmailLink(t *testing.T, app *App, u *models.User, oldToken string) {
	t.Helper()
	c := newAPIClient(t, app)
	c.holdNotices = true
	c.fetchCSRF()
	path := "/api/v1/email-settings/" + oldToken
	var settings tokenSettings
	decodeJSON(t, c.do("GET", path, nil, false), &settings)
	if settings.CanTurnOn {
		t.Fatal("link mailed to the old address can turn on at the new one")
	}
	before, records := consentTestRows(t, app, u.ID), retainedEmailChoices(t, app)
	w := c.do("PUT", path, map[string]any{"category": "events", "off": false}, true)
	wantEmailSettingsStatus(t, w, http.StatusForbidden)
	if !strings.Contains(w.Body.String(), `"code":"email_link_turn_on"`) || !reflect.DeepEqual(before, consentTestRows(t, app, u.ID)) {
		t.Fatal("moved link turn-on changed account choices or returned the wrong refusal")
	}
	wantEmailSettingsStatus(t, c.do("PUT", path, map[string]any{"category": "events", "off": true}, true), http.StatusOK)
	if !reflect.DeepEqual(records, retainedEmailChoices(t, app)) {
		t.Fatal("moved link changed retained records")
	}
}

func TestEmailTurnOnAndAddressChangeLockOrder(t *testing.T) {
	for _, mode := range []string{"verified", "admin"} {
		t.Run(mode, func(t *testing.T) {
			t.Run("address change commits first", func(t *testing.T) {
				app, u, c, nextAddress, change := emailAddressChangeFixture(t, mode)
				oldToken := app.unsubscribeToken(u.ID, "category:events", u.Email)
				before, records := consentTestRows(t, app, u.ID), retainedEmailChoices(t, app)
				var changePID atomic.Int64
				reached, release := erasureTestPause(t, app.DB, erasureTestPoint{
					Processor: "update", After: true, Step: "gorm:update", Table: "users",
					Assign: map[string]any{"email": nextAddress}, PID: &changePID,
				})
				changed := consentTestAsync(t, change)
				awaitDBSignal(t, reached)
				// Authentication sees the committed pre-change row while the change
				// transaction is paused with its users UPDATE still uncommitted.
				turnedOn := consentTestAsync(t, func() *httptest.ResponseRecorder {
					return c.do("PUT", "/api/v1/me/notification-settings", map[string]any{"category": "events", "off": false}, true)
				})
				waiter := waitForLockedUsersQuery(t, app, "%users%FOR NO KEY UPDATE%")
				waitDBBlocked(t, app.DB, waiter, int(changePID.Load()))
				release()
				wantEmailSettingsStatus(t, changed(), http.StatusOK)
				refused := turnedOn()
				wantEmailSettingsStatus(t, refused, http.StatusUnauthorized)
				if !strings.Contains(refused.Body.String(), `"code":"signed_out"`) ||
					!reflect.DeepEqual(before, consentTestRows(t, app, u.ID)) || !reflect.DeepEqual(records, retainedEmailChoices(t, app)) {
					t.Fatal("pre-change session wrote choices or got the wrong refusal")
				}
				fresh := loginAPI(t, app, nextAddress, "password1234")
				fresh.ip, fresh.ua, fresh.holdNotices = c.ip, c.ua, true
				wantEmailSettingsStatus(t, fresh.do("PUT", "/api/v1/me/notification-settings", map[string]any{"category": "events", "off": false}, true), http.StatusOK)
				wantRetainedEmailChoice(t, app, u.Email, "events", true)
				wantRetainedEmailChoice(t, app, nextAddress, "events", false)
				wantAffirmativeEmailConsent(t, app, u, "events", models.ConsentSourceWebForm, fresh.ip, fresh.ua)
				wantMovedEmailLink(t, app, u, oldToken)
			})
			t.Run("turn-on commits first", func(t *testing.T) {
				app, u, c, nextAddress, change := emailAddressChangeFixture(t, mode)
				oldToken := app.unsubscribeToken(u.ID, "category:events", u.Email)
				var writerPID atomic.Int64
				reached, release := erasureTestPause(t, app.DB, erasureTestPoint{
					Processor: "raw", After: true, Step: "gorm:raw", SQLContains: "DELETE FROM email_address_optouts",
					VarAt: map[int]any{0: mail.AddressHash(u.Email), 1: "category:events"}, PID: &writerPID,
				})
				turnedOn := consentTestAsync(t, func() *httptest.ResponseRecorder {
					return c.do("PUT", "/api/v1/me/notification-settings", map[string]any{"category": "events", "off": false}, true)
				})
				awaitDBSignal(t, reached)
				changed := consentTestAsync(t, change)
				// Both real APIs take NO KEY UPDATE on the holder at this head.
				waiter := waitForLockedUsersQuery(t, app, "%users%FOR NO KEY UPDATE%")
				waitDBBlocked(t, app.DB, waiter, int(writerPID.Load()))
				release()
				wantEmailSettingsStatus(t, turnedOn(), http.StatusOK)
				wantEmailSettingsStatus(t, changed(), http.StatusOK)
				wantRetainedEmailChoice(t, app, u.Email, "events", false)
				wantRetainedEmailChoice(t, app, nextAddress, "events", true)
				wantAffirmativeEmailConsent(t, app, u, "events", models.ConsentSourceWebForm, c.ip, c.ua)
				wantEmailSettingsStatus(t, c.do("PUT", "/api/v1/me/notification-settings", map[string]any{"category": "events", "off": false}, true), http.StatusUnauthorized)
				fresh := loginAPI(t, app, nextAddress, "password1234")
				var settings struct {
					Settings notificationSettingsJSON `json:"settings"`
				}
				decodeJSON(t, fresh.do("GET", "/api/v1/me/notification-settings", nil, false), &settings)
				if off, found := kindOff(emailSettingsJSON{Kinds: settings.Settings.Kinds}, "events"); !found || !off {
					t.Fatal("new address's retained choice lost to an earlier account opt-in")
				}
				wantMovedEmailLink(t, app, u, oldToken)
			})
		})
	}
}
