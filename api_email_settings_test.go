package main

import (
	"net/http"
	"strings"
	"testing"

	"fold/models"
)

type tokenSettings struct {
	EmailSettings emailSettingsJSON `json:"emailSettings"`
	Email         string            `json:"email"`
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
	path := "/api/v1/email-settings/" + app.unsubscribeToken(lee.ID, "category:groups")

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
	// events, then groups and messages (events already off): three rows.
	if len(rows) != 3 || rows[0].Source != models.ConsentSourceUnsubscribe {
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
	if app.churchAddress() != "Grace Church\n12 Main St\nBoise, ID" || auditCount(t, app, "setting.church_address_changed") != 1 {
		t.Fatal("not saved or not audited")
	}
	if w := root.do("PUT", "/api/v1/admin/settings/church-address", map[string]any{"address": strings.Repeat("x\n", 7)}, true); w.Code != http.StatusBadRequest ||
		!strings.Contains(w.Body.String(), "church_address_long") {
		t.Fatalf("too long: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(root.do("GET", "/api/v1/admin/settings/church-address", nil, false).Body.String(), "12 Main St") {
		t.Fatal("read back")
	}
}
