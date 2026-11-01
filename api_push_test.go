package main

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fold/internal/audit"
	"fold/internal/notify"
	"fold/models"

	"github.com/gin-gonic/gin"
)

const iPhoneUA = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.4 Mobile/15E148 Safari/604.1"
const windowsChromeUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"

// browserSub is what a browser's PushSubscription.toJSON() posts: a fresh
// key pair and an endpoint on FCM.
type browserSub struct {
	Endpoint string            `json:"endpoint"`
	Keys     map[string]string `json:"keys"`
}

func newBrowserSub(t *testing.T, name string) browserSub {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	return browserSub{Endpoint: "https://fcm.googleapis.com/fcm/send/" + name + "-secret",
		Keys: map[string]string{"p256dh": base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), "auth": base64.RawURLEncoding.EncodeToString(auth)}}
}

// secretsOf are the strings no response may carry for a subscription.
func (s browserSub) secretsOf() []string {
	path := strings.TrimPrefix(s.Endpoint, "https://fcm.googleapis.com/fcm/send/")
	return []string{s.Endpoint, path, s.Keys["p256dh"], s.Keys["auth"]}
}

func subscribe(t *testing.T, c *apiClient, s browserSub) (pushSubscribeJSON, *httptest.ResponseRecorder) {
	t.Helper()
	w := c.do("PUT", "/api/v1/push/subscriptions", s, true)
	var out pushSubscribeJSON
	if w.Code == http.StatusOK {
		_ = json.Unmarshal(w.Body.Bytes(), &out)
	}
	return out, w
}

func pushConsents(t *testing.T, app *App, u *models.User, scope string) []models.Consent {
	t.Helper()
	var rows []models.Consent
	app.DB.Where("user_id = ? AND channel = ? AND scope = ?", u.ID, models.ConsentPush, scope).Order("at, id").Find(&rows)
	return rows
}

func userDevices(app *App, u *models.User) []models.PushSubscription {
	var out []models.PushSubscription
	app.DB.Where("user_id = ?", u.ID).Order("id").Find(&out)
	return out
}

func errorCodeOf(w *httptest.ResponseRecorder) string {
	var body struct{ Code string }
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return body.Code
}

func TestPushConfig(t *testing.T) {
	app, _ := pushTestApp(t)
	newTestUser(t, app, "pat", "password123456", models.RoleMember)
	if w := newAPIClient(t, app).do("GET", "/api/v1/push/config", nil, false); w.Code != http.StatusUnauthorized {
		t.Fatalf("signed out: %d", w.Code)
	}
	pat := loginAPI(t, app, "pat", "password123456")
	public, _, _ := readVAPID(app.DB)
	var cfg pushConfigJSON
	w := pat.do("GET", "/api/v1/push/config", nil, false)
	decodeJSON(t, w, &cfg)
	if w.Code != http.StatusOK || !cfg.Enabled || cfg.PublicKey != public || public == "" {
		t.Fatalf("config %d %+v", w.Code, cfg)
	}
	app.DB.Create(&models.Setting{Key: settingPushEnabled, Value: "false"})
	w = pat.do("GET", "/api/v1/push/config", nil, false)
	decodeJSON(t, w, &cfg)
	if cfg.Enabled {
		t.Fatal("the church turned push off, but config says on")
	}
}

func TestPushSubscribeSavesBindsAndConsents(t *testing.T) {
	app, _ := pushTestApp(t)
	patUser := newTestUser(t, app, "pat", "password123456", models.RoleMember)
	leeUser := newTestUser(t, app, "lee", "password123456", models.RoleMember)
	pat := loginAPI(t, app, "pat", "password123456")
	pat.ua = iPhoneUA
	var bodies []string
	sub := newBrowserSub(t, "phone")

	// Off the allow-list: refused, nothing saved.
	bad := sub
	for _, endpoint := range []string{"https://push.evil.example/x", "http://fcm.googleapis.com/fcm/send/x", "https://evilfcm.googleapis.com/x", "https://127.0.0.1/x"} {
		bad.Endpoint = endpoint
		_, w := subscribe(t, pat, bad)
		if w.Code != http.StatusBadRequest || errorCodeOf(w) != "push_service_unknown" {
			t.Errorf("%s: %d %s", endpoint, w.Code, w.Body.String())
		}
		bodies = append(bodies, w.Body.String())
	}
	// Keys that aren't keys.
	broken := newBrowserSub(t, "broken")
	broken.Keys["auth"] = "short"
	if _, w := subscribe(t, pat, broken); w.Code != http.StatusBadRequest {
		t.Errorf("bad keys: %d", w.Code)
	}
	if n := len(userDevices(app, patUser)); n != 0 {
		t.Fatalf("%d devices saved from refused requests", n)
	}

	// The first device turns push on, labelled from the User-Agent.
	got, w := subscribe(t, pat, sub)
	bodies = append(bodies, w.Body.String())
	if w.Code != http.StatusOK || !got.Saved || got.Device == nil || got.Device.Label != "iPhone · Home Screen app" {
		t.Fatalf("subscribe %d %s", w.Code, w.Body.String())
	}
	on := pushConsents(t, app, patUser, "all")
	if len(on) != 1 || on[0].Status != models.ConsentOptedIn || on[0].Source != models.ConsentSourceWebForm ||
		on[0].EvidenceText != "Turned on notifications for iPhone (Home Screen app)" {
		t.Fatalf("consent %+v", on)
	}
	// Saving the same subscription again keeps the device and the record.
	again, w := subscribe(t, pat, sub)
	bodies = append(bodies, w.Body.String())
	if again.Device == nil || again.Device.ID != got.Device.ID || len(pushConsents(t, app, patUser, "all")) != 1 {
		t.Fatalf("resubscribe %s", w.Body.String())
	}

	// The same endpoint from another account is rebound to it.
	time.Sleep(10 * time.Millisecond)
	lee := loginAPI(t, app, "lee", "password123456")
	lee.ua = windowsChromeUA
	rebound, w := subscribe(t, lee, sub)
	bodies = append(bodies, w.Body.String())
	if !rebound.Saved || rebound.Device.ID != got.Device.ID || rebound.Device.Label != "Chrome on Windows" || !rebound.Device.CreatedAt.After(got.Device.CreatedAt) {
		t.Fatalf("rebind %s", w.Body.String())
	}
	if len(userDevices(app, patUser)) != 0 || len(userDevices(app, leeUser)) != 1 {
		t.Fatal("the device wasn't rebound")
	}
	if c := pushConsents(t, app, leeUser, "all"); len(c) != 1 || c[0].EvidenceText != "Turned on notifications for Chrome on Windows" {
		t.Fatalf("lee's consent %+v", c)
	}
	rows, _ := auditRows(t, app, audit.PushDeviceOn)
	var first, last map[string]any
	if len(rows) == 3 {
		_ = json.Unmarshal([]byte(rows[0].MetaJSON), &first)
		_ = json.Unmarshal([]byte(rows[2].MetaJSON), &last)
	}
	if len(rows) != 3 || last["rebound"] != true || first["rebound"] != false || last["label"] != "Chrome on Windows" {
		t.Fatalf("audit %v %v", first, last)
	}

	// Turned off everywhere, then on again: a new opt-in.
	if w := pat.do("POST", "/api/v1/push/turn-off", nil, true); w.Code != http.StatusOK {
		t.Fatalf("turn off: %d", w.Code)
	}
	if _, w := subscribe(t, pat, newBrowserSub(t, "phone2")); w.Code != http.StatusOK {
		t.Fatalf("subscribe after turn-off: %d", w.Code)
	}
	if c := pushConsents(t, app, patUser, "all"); len(c) != 3 || c[1].Status != models.ConsentOptedOut || c[2].Status != models.ConsentOptedIn {
		t.Fatalf("consents after turning on again %+v", c)
	}

	// No response, audit row or consent row carries an endpoint or key.
	settings := pat.do("GET", "/api/v1/me/notification-settings", nil, false)
	bodies = append(bodies, settings.Body.String())
	var stored []string
	var as []models.AuditLog
	app.DB.Find(&as)
	for _, a := range as {
		stored = append(stored, a.MetaJSON)
	}
	var cs []models.Consent
	app.DB.Find(&cs)
	for _, c := range cs {
		stored = append(stored, c.EvidenceText)
	}
	haystack := strings.Join(bodies, "\n") + strings.Join(stored, "\n")
	for _, s := range sub.secretsOf() {
		if strings.Contains(haystack, s) {
			t.Errorf("a response or record carries %q", s)
		}
	}
}

func TestPushDeviceLabels(t *testing.T) {
	for ua, want := range map[string]string{
		iPhoneUA: "iPhone · Home Screen app",
		"Mozilla/5.0 (iPad; CPU OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1": "iPad · Home Screen app",
		windowsChromeUA: "Chrome on Windows",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36 Edg/130.0.0.0":              "Edge on Windows",
		"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Mobile Safari/537.36":                      "Chrome on Android",
		"Mozilla/5.0 (Linux; Android 14; SM-S918B) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/26.0 Chrome/122.0.0.0 Mobile Safari/537.36": "Samsung Internet on Android",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.4 Safari/605.1.15":                      "Safari on Mac",
		"Mozilla/5.0 (X11; Linux x86_64; rv:131.0) Gecko/20100101 Firefox/131.0":                                                                     "Firefox on Linux",
		"Mozilla/5.0 (X11; CrOS x86_64 14541.0.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36":                             "Chrome on ChromeOS",
		"":                                    "A web browser",
		"<script>alert(1)</script> Firefox/1": "Firefox",
	} {
		if got, _ := pushDeviceLabel(ua); got != want {
			t.Errorf("%q: %q, want %q", ua, got, want)
		}
	}
}

// At most ten devices: the 11th replaces the one least recently used.
func TestPushDeviceCap(t *testing.T) {
	app, _ := pushTestApp(t)
	u := pushMember(t, app, "pat")
	var oldest uint
	for i := range maxPushDevices {
		d := newPushDevice(t, app, u, "device"+itoa(uint(i)))
		// Device 3 was used longest ago.
		at := time.Now().Add(-time.Duration(i+1) * time.Minute)
		if i == 3 {
			at = time.Now().Add(-48 * time.Hour)
			oldest = d.ID
		}
		app.DB.Model(&models.PushSubscription{}).Where("id = ?", d.ID).Update("last_used_at", at)
	}
	pat := loginAPI(t, app, "pat", "password123456")
	got, w := subscribe(t, pat, newBrowserSub(t, "eleventh"))
	if !got.Saved {
		t.Fatalf("subscribe %s", w.Body.String())
	}
	devices := userDevices(app, u)
	if len(devices) != maxPushDevices || deviceExists(app, oldest) || !deviceExists(app, got.Device.ID) {
		t.Fatalf("%d devices; oldest kept %v", len(devices), deviceExists(app, oldest))
	}
}

// Past 30 saves an hour, nothing is saved and the answer is 200 with a line
// to show (DEC-129), never a 429.
func TestPushSubscribeLimit(t *testing.T) {
	app, _ := pushTestApp(t)
	u := newTestUser(t, app, "pat", "password123456", models.RoleMember)
	for range pushSavesPerHour {
		app.DB.Create(&models.AuditLog{ActorID: &u.ID, Action: audit.PushDeviceOn, TargetType: "user", TargetID: u.ID})
	}
	pat := loginAPI(t, app, "pat", "password123456")
	got, w := subscribe(t, pat, newBrowserSub(t, "phone"))
	if w.Code != http.StatusOK || got.Saved || got.Device != nil || got.Message != "Try again in a few minutes." {
		t.Fatalf("past the limit: %d %s", w.Code, w.Body.String())
	}
	if len(userDevices(app, u)) != 0 || len(pushConsents(t, app, u, "all")) != 0 {
		t.Fatal("the limit saved something")
	}
	// An hour on, it saves again.
	app.DB.Model(&models.AuditLog{}).Where("action = ?", audit.PushDeviceOn).Update("at", time.Now().Add(-61*time.Minute))
	if got, _ := subscribe(t, pat, newBrowserSub(t, "phone")); !got.Saved {
		t.Fatal("still limited after an hour")
	}
}

// asUser runs one handler with u as the signed-in person, past the session
// check: a guest or a teen waiting for consent can't hold a session, so
// this is how their 403s are reached.
func asUser(t *testing.T, u *models.User, method string, h gin.HandlerFunc, body any) *httptest.ResponseRecorder {
	t.Helper()
	r := gin.New()
	r.Handle(method, "/x", func(c *gin.Context) { c.Set("apiUser", u); c.Next() }, h)
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(method, "/x", strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestPushRefusals(t *testing.T) {
	app, _ := pushTestApp(t)
	sub := newBrowserSub(t, "phone")
	for status, code := range map[string]string{models.UserGuest: "push_guest", models.UserPendingConsent: "push_consent_required"} {
		u := models.User{Email: status + "@example.com", Name: status, Role: models.RoleMember, Status: status, AccountType: models.AccountAdult}
		if status == models.UserPendingConsent {
			u.AccountType = models.AccountTeen
		}
		if err := app.DB.Create(&u).Error; err != nil {
			t.Fatal(err)
		}
		for name, h := range map[string]gin.HandlerFunc{"subscribe": app.apiPushSubscribe, "test": app.apiPushTest} {
			if w := asUser(t, &u, "POST", h, sub); w.Code != http.StatusForbidden || errorCodeOf(w) != code {
				t.Errorf("%s %s: %d %s", status, name, w.Code, w.Body.String())
			}
		}
		if len(userDevices(app, &u)) != 0 {
			t.Errorf("%s: a device was saved", status)
		}
	}
	// The church turned push off.
	u := pushMember(t, app, "pat")
	phone := newPushDevice(t, app, u, "phone")
	app.DB.Create(&models.Setting{Key: settingPushEnabled, Value: "false"})
	pat := loginAPI(t, app, "pat", "password123456")
	if _, w := subscribe(t, pat, sub); w.Code != http.StatusForbidden || errorCodeOf(w) != "push_church_off" {
		t.Errorf("church off subscribe: %d %s", w.Code, w.Body.String())
	}
	if w := pat.do("POST", "/api/v1/push/test", map[string]string{"endpoint": phone.Endpoint}, true); w.Code != http.StatusForbidden || errorCodeOf(w) != "push_church_off" {
		t.Errorf("church off test: %d %s", w.Code, w.Body.String())
	}
}

func TestPushTurnOffOneDeviceOrEverywhere(t *testing.T) {
	app, _ := pushTestApp(t)
	patUser, leeUser := pushMember(t, app, "pat"), pushMember(t, app, "lee")
	phone, laptop := newPushDevice(t, app, patUser, "phone"), newPushDevice(t, app, patUser, "laptop")
	leePhone := newPushDevice(t, app, leeUser, "lee-phone")
	pat := loginAPI(t, app, "pat", "password123456")

	// This device: only the person's own; someone else's answers the same
	// and stays.
	if w := pat.do("DELETE", "/api/v1/push/subscriptions", map[string]string{"endpoint": phone.Endpoint}, true); w.Code != http.StatusOK {
		t.Fatalf("delete: %d", w.Code)
	}
	if w := pat.do("DELETE", "/api/v1/push/subscriptions", map[string]string{"endpoint": leePhone.Endpoint}, true); w.Code != http.StatusOK {
		t.Fatalf("delete someone else's: %d", w.Code)
	}
	if deviceExists(app, phone.ID) || !deviceExists(app, laptop.ID) || !deviceExists(app, leePhone.ID) {
		t.Fatal("deleted the wrong devices")
	}
	if c := pushConsents(t, app, patUser, "all"); c[len(c)-1].Status != models.ConsentOptedIn {
		t.Fatal("turning off one device turned push off")
	}

	// Everywhere: an opt-out and every device gone.
	pushConsentRow(t, app, patUser, "category:groups", models.ConsentOptedOut)
	w := pat.do("POST", "/api/v1/push/turn-off", nil, true)
	var out struct{ Push pushSettingsJSON }
	decodeJSON(t, w, &out)
	if w.Code != http.StatusOK || out.Push.On || len(out.Push.Devices) != 0 {
		t.Fatalf("turn off: %d %+v", w.Code, out)
	}
	if len(userDevices(app, patUser)) != 0 || !deviceExists(app, leePhone.ID) {
		t.Fatal("turn off everywhere left a device, or took someone else's")
	}
	c := pushConsents(t, app, patUser, "all")
	if last := c[len(c)-1]; last.Status != models.ConsentOptedOut || last.EvidenceText != "Turned off notifications everywhere" {
		t.Fatalf("consent %+v", last)
	}
	// A required kind doesn't reach a device that slipped in (a restore):
	// "everywhere" overrides every switch.
	stray := newPushDevice(t, app, patUser, "stray")
	n := sendNotice(t, app, notify.AccountTurned18, patUser)
	runPushJobs(t, app, n, stray)
	if status, reason := pushDelivery(t, app, n); status != models.DeliverySuppressed || reason != pushWhyNotOn {
		t.Fatalf("after turning off: %q %q", status, reason)
	}
}

func TestPushTestNotification(t *testing.T) {
	app, f := pushTestApp(t)
	app.DB.Create(&models.Setting{Key: "site_name", Value: "First Church"})
	u := pushMember(t, app, "pat")
	phone := newPushDevice(t, app, u, "phone")
	app.DB.Model(&models.PushSubscription{}).Where("id = ?", phone.ID).Update("last_used_at", time.Now().Add(-72*time.Hour))
	pat := loginAPI(t, app, "pat", "password123456")
	var got pushTestJSON
	w := pat.do("POST", "/api/v1/push/test", map[string]string{"endpoint": phone.Endpoint}, true)
	decodeJSON(t, w, &got)
	if w.Code != http.StatusOK || !got.Sent || f.count(phone.path) != 1 {
		t.Fatalf("test %d %s, sends %v", w.Code, w.Body.String(), f.sent)
	}
	if h := f.headers[0]; h.Get("Urgency") != "high" || h.Get("TTL") != "600" || h.Get("Content-Encoding") != "aes128gcm" {
		t.Fatalf("headers %v", h)
	}
	var dev models.PushSubscription
	app.DB.First(&dev, phone.ID)
	if time.Since(dev.LastUsedAt) > time.Minute {
		t.Fatal("a test that went didn't mark the device used")
	}
	if w := pat.do("POST", "/api/v1/push/test", map[string]string{"endpoint": "https://fcm.googleapis.com/fcm/send/unknown"}, true); w.Code != http.StatusNotFound || errorCodeOf(w) != "push_device_unknown" {
		t.Fatalf("unknown device: %d %s", w.Code, w.Body.String())
	}
	// A device the push service says is gone is deleted, and the card hears so.
	f.set(phone.path, http.StatusGone)
	w = pat.do("POST", "/api/v1/push/test", map[string]string{"endpoint": phone.Endpoint}, true)
	decodeJSON(t, w, &got)
	if got.Sent || got.Message != pushTestGoneMessage || deviceExists(app, phone.ID) {
		t.Fatalf("gone: %s", w.Body.String())
	}
	// Ten an hour, then 200 with DEC-129's line, not "Sent" and not a 429.
	laptop := newPushDevice(t, app, u, "laptop")
	for range pushTestsPerHour - 2 {
		w = pat.do("POST", "/api/v1/push/test", map[string]string{"endpoint": laptop.Endpoint}, true)
		decodeJSON(t, w, &got)
		if !got.Sent {
			t.Fatalf("under the limit: %s", w.Body.String())
		}
	}
	before := f.total()
	w = pat.do("POST", "/api/v1/push/test", map[string]string{"endpoint": laptop.Endpoint}, true)
	got = pushTestJSON{}
	decodeJSON(t, w, &got)
	if w.Code != http.StatusOK || got.Sent || got.Message != "You just sent a few tests. Try again in a few minutes." || f.total() != before {
		t.Fatalf("past the limit: %d %s", w.Code, w.Body.String())
	}
}

func TestPushNotificationSettings(t *testing.T) {
	app, f := pushTestApp(t)
	u := pushMember(t, app, "pat")
	phone := newPushDevice(t, app, u, "phone")
	pat := loginAPI(t, app, "pat", "password123456")
	w := pat.do("GET", "/api/v1/me/notification-settings", nil, false)
	var got struct{ Settings notificationSettingsJSON }
	decodeJSON(t, w, &got)
	p := got.Settings.Push
	if w.Code != http.StatusOK || !p.On || len(p.Devices) != 1 || p.Devices[0].Label != "phone" || p.Devices[0].ID != phone.ID {
		t.Fatalf("settings %d %+v", w.Code, p)
	}
	cats := []string{}
	for _, k := range p.Kinds {
		cats = append(cats, k.Category)
		if k.Off {
			t.Errorf("%s starts off", k.Category)
		}
	}
	if strings.Join(cats, ",") != "groups,events,messages" {
		t.Fatalf("push kinds %v (admin notices only for admins; safety is never listed)", cats)
	}
	if strings.Contains(w.Body.String(), phone.path) || strings.Contains(w.Body.String(), phone.P256dh) {
		t.Fatal("settings carry the endpoint or a key")
	}

	// A Push switch writes a push consent and leaves email alone.
	w = pat.do("PUT", "/api/v1/me/notification-settings", map[string]any{"channel": "push", "category": "groups", "off": true}, true)
	decodeJSON(t, w, &got)
	if w.Code != http.StatusOK || !got.Settings.Push.Kinds[0].Off || got.Settings.Kinds[0].Off {
		t.Fatalf("push switch %d %+v", w.Code, got.Settings)
	}
	if c := pushConsents(t, app, u, "category:groups"); len(c) != 1 || c[0].Status != models.ConsentOptedOut {
		t.Fatalf("push consent %+v", c)
	}
	var emailRows int64
	app.DB.Model(&models.Consent{}).Where("user_id = ? AND channel = ?", u.ID, models.ConsentEmail).Count(&emailRows)
	if emailRows != 0 {
		t.Fatal("a push switch wrote an email consent")
	}
	// Required categories are locked; an unknown channel is refused.
	for _, body := range []map[string]any{
		{"channel": "push", "category": "safety", "off": true},
		{"channel": "push", "category": "admin", "off": true}, // not an admin
	} {
		if w := pat.do("PUT", "/api/v1/me/notification-settings", body, true); w.Code != http.StatusBadRequest || errorCodeOf(w) != "push_kind_locked" {
			t.Errorf("%v: %d %s", body, w.Code, w.Body.String())
		}
	}
	if w := pat.do("PUT", "/api/v1/me/notification-settings", map[string]any{"channel": "sms", "category": "groups", "off": true}, true); w.Code != http.StatusBadRequest {
		t.Errorf("sms: %d", w.Code)
	}
	// The email switch works as before.
	w = pat.do("PUT", "/api/v1/me/notification-settings", map[string]any{"category": "events", "off": true}, true)
	decodeJSON(t, w, &got)
	if w.Code != http.StatusOK || !got.Settings.Kinds[1].Off || got.Settings.Push.Kinds[1].Off {
		t.Fatalf("email switch %d %+v", w.Code, got.Settings)
	}
	if w := pat.do("PUT", "/api/v1/me/notification-settings", map[string]any{"optionalEmailOff": true}, true); w.Code != http.StatusOK {
		t.Fatalf("all optional email: %d", w.Code)
	}

	// The groups switch skips an optional groups kind; a required kind
	// still pushes (a real notify.Send, queued after commit).
	optional := sendNotice(t, app, notify.GroupApproved, u)
	required := sendNotice(t, app, notify.TeenConsentChanged, u)
	app.deliverNotificationEmails([]uint{optional, required})
	drainJobs(t, app)
	if status, reason := pushDelivery(t, app, optional); status != models.DeliverySuppressed || reason != "push turned off for group updates" {
		t.Errorf("optional: %q %q", status, reason)
	}
	if status, _ := pushDelivery(t, app, required); status != models.DeliverySent || f.count(phone.path) != 1 {
		t.Errorf("required: %q after %d sends", status, f.count(phone.path))
	}
}

// devicesOffNotices are the person's "notifications were turned off" rows.
func devicesOffNotices(app *App, u *models.User) []models.Notification {
	var out []models.Notification
	app.DB.Where("user_id = ? AND kind = ?", u.ID, notify.PushDevicesOff).Find(&out)
	return out
}

// A person's devices end with their sessions (research 2.8): every path
// that revokes sessions or takes the account out of active deletes every
// device, and the person gets one bell notice saying so (DEC-134 C), never
// an email or a push.
func TestPushDevicesEndWithSessions(t *testing.T) {
	for _, change := range []string{"password_change", "password_reset", "admin_reset", "email_change", "admin_email", "role_change", "disable"} {
		t.Run(change, func(t *testing.T) {
			app, f := pushTestApp(t)
			newTestUser(t, app, "admin", "password123456", models.RoleAdmin)
			admin := loginAPI(t, app, "admin", "password123456")
			u := pushMember(t, app, "cal")
			newPushDevice(t, app, u, "cal-phone")
			newPushDevice(t, app, u, "cal-laptop")
			client := loginAPI(t, app, "cal", "password123456")
			anon := newAPIClient(t, app)
			anon.fetchCSRF()
			tokens := pendingSecurityCapabilities(t, app, u)
			target := "/api/v1/admin/users/" + itoa(u.ID)
			switch change {
			case "password_change":
				apiDo(t, client, "POST", "/api/v1/me/password", map[string]string{"current": "password123456", "password": "a-new-password-1"}, http.StatusOK)
			case "password_reset":
				apiDo(t, anon, "POST", "/api/v1/auth/password-reset/confirm", map[string]string{"token": tokens[models.TokenPasswordReset], "password": "a-new-password-1"}, http.StatusOK)
			case "admin_reset":
				apiDo(t, admin, "POST", target+"/password", map[string]string{"password": "a-new-password-1"}, http.StatusOK)
			case "email_change":
				apiDo(t, client, "POST", "/api/v1/me/email/verify", map[string]string{"token": tokens[models.TokenEmailVerify]}, http.StatusOK)
			case "admin_email":
				apiDo(t, admin, "PUT", target, map[string]string{"email": "cal-new@example.com"}, http.StatusOK)
			case "role_change":
				apiDo(t, admin, "PUT", target, map[string]string{"role": models.RoleAdmin}, http.StatusOK)
			case "disable":
				apiDo(t, admin, "PUT", target, map[string]string{"status": models.UserDisabled}, http.StatusOK)
			}
			if n := len(userDevices(app, u)); n != 0 {
				t.Fatalf("%d devices left", n)
			}
			drainJobs(t, app)
			drainJobs(t, app) // a second run (a retry) writes no second notice
			notes := devicesOffNotices(app, u)
			if len(notes) != 1 {
				t.Fatalf("%d notices, want 1", len(notes))
			}
			n := notes[0]
			if n.Title != "Notifications on your devices were turned off" ||
				n.Body != "Your password, email or account changed, so we turned off notifications on your phones and computers. Turn them on again in Me → Notifications." ||
				n.Link != "/profile/notifications" || n.Waiting || n.PushTitle != "" {
				t.Fatalf("notice %+v", n)
			}
			if k, _ := notify.Lookup(n.Kind); k.Required || k.AllowEmail || k.AllowPush {
				t.Fatalf("the notice is bell only and optional: %+v", k)
			}
			var mailed int64
			app.DB.Model(&models.NotificationDelivery{}).Where("notification_id = ?", n.ID).Count(&mailed)
			if mailed != 0 || f.total() != 0 {
				t.Fatalf("the notice was delivered: %d deliveries, %d pushes", mailed, f.total())
			}
		})
	}
}

// Turning devices off yourself, or a revocation with no device to remove,
// writes no notice; nor does anything that leaves sessions alone.
func TestPushDevicesOffNoticeOnlyWhenRevoked(t *testing.T) {
	app, _ := pushTestApp(t)
	newTestUser(t, app, "admin", "password123456", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password123456")
	u := pushMember(t, app, "pat")
	phone, laptop, tablet := newPushDevice(t, app, u, "phone"), newPushDevice(t, app, u, "laptop"), newPushDevice(t, app, u, "tablet")
	pat := loginAPI(t, app, "pat", "password123456")
	apiDo(t, pat, "DELETE", "/api/v1/push/subscriptions", map[string]string{"endpoint": phone.Endpoint}, http.StatusOK)
	apiDo(t, pat, "POST", "/api/v1/auth/logout", map[string]string{"pushEndpoint": laptop.Endpoint}, http.StatusOK)
	pat = loginAPI(t, app, "pat", "password123456")
	apiDo(t, admin, "PUT", "/api/v1/admin/users/"+itoa(u.ID), map[string]string{"name": "Pat Lee"}, http.StatusOK)
	if !deviceExists(app, tablet.ID) {
		t.Fatal("a name change deleted a device")
	}
	apiDo(t, pat, "POST", "/api/v1/push/turn-off", nil, http.StatusOK)
	// No devices left: a password change has nothing to tell.
	apiDo(t, pat, "POST", "/api/v1/me/password", map[string]string{"current": "password123456", "password": "a-new-password-1"}, http.StatusOK)
	drainJobs(t, app)
	if n := len(devicesOffNotices(app, u)); n != 0 {
		t.Fatalf("%d notices for devices the person turned off", n)
	}
	// A deleted account gets none either.
	gone := pushMember(t, app, "gil")
	newPushDevice(t, app, gone, "gil-phone")
	apiDo(t, admin, "DELETE", "/api/v1/admin/users/"+itoa(gone.ID), nil, http.StatusOK)
	drainJobs(t, app)
	var left int64
	app.DB.Model(&models.Notification{}).Where("user_id = ?", gone.ID).Count(&left)
	if left != 0 || len(userDevices(app, gone)) != 0 {
		t.Fatalf("a deleted account has %d notices", left)
	}
}

// An admin's admin-notices Push switch skips the one optional admin kind
// that pushes (leadership changes, DEC-134 A); tasks still push.
func TestPushAdminSwitchSkipsLeadershipChanges(t *testing.T) {
	app, f := pushTestApp(t)
	newTestUser(t, app, "admin", "password123456", models.RoleAdmin)
	var adminUser models.User
	app.DB.Where("email = ?", testEmail("admin")).First(&adminUser)
	pushConsentRow(t, app, &adminUser, "all", models.ConsentOptedIn)
	phone := newPushDevice(t, app, &adminUser, "phone")
	admin := loginAPI(t, app, "admin", "password123456")
	w := admin.do("GET", "/api/v1/me/notification-settings", nil, false)
	var got struct{ Settings notificationSettingsJSON }
	decodeJSON(t, w, &got)
	cats := []string{}
	for _, k := range got.Settings.Push.Kinds {
		cats = append(cats, k.Category)
	}
	if strings.Join(cats, ",") != "admin,groups,events,messages" {
		t.Fatalf("an admin's push kinds %v", cats)
	}
	apiDo(t, admin, "PUT", "/api/v1/me/notification-settings", map[string]any{"channel": "push", "category": "admin", "off": true}, http.StatusOK)
	leader := sendNotice(t, app, notify.LeadershipChanged, &adminUser)
	task := sendNotice(t, app, notify.GroupPending, &adminUser)
	app.deliverNotificationEmails([]uint{leader, task})
	drainJobs(t, app)
	if status, reason := pushDelivery(t, app, leader); status != models.DeliverySuppressed || reason != "push turned off for admin notices" {
		t.Errorf("leadership change: %q %q", status, reason)
	}
	if status, _ := pushDelivery(t, app, task); status != models.DeliverySent || f.count(phone.path) != 1 {
		t.Errorf("task: %q", status)
	}
}

// A device someone else holds is rebound only when its keys match too (the
// same browser subscription); an endpoint alone takes nothing, and the
// answer looks like any save.
func TestPushSubscribeNeedsTheKeysToTakeADevice(t *testing.T) {
	app, _ := pushTestApp(t)
	patUser := newTestUser(t, app, "pat", "password123456", models.RoleMember)
	samUser := newTestUser(t, app, "sam", "password123456", models.RoleMember)
	pat := loginAPI(t, app, "pat", "password123456")
	sub := newBrowserSub(t, "phone")
	mine, _ := subscribe(t, pat, sub)
	var before models.PushSubscription
	app.DB.First(&before, mine.Device.ID)

	stolen := newBrowserSub(t, "other")
	stolen.Endpoint = sub.Endpoint
	sam := loginAPI(t, app, "sam", "password123456")
	got, w := subscribe(t, sam, stolen)
	if w.Code != http.StatusOK || !got.Saved || got.Device == nil || got.Device.ID != mine.Device.ID {
		t.Fatalf("the answer differs from a save: %d %s", w.Code, w.Body.String())
	}
	var after models.PushSubscription
	app.DB.First(&after, mine.Device.ID)
	if after.UserID != patUser.ID || after.P256dh != before.P256dh || after.Auth != before.Auth || !after.CreatedAt.Equal(before.CreatedAt) {
		t.Fatalf("the device changed hands without its keys: %+v", after)
	}
	if len(pushConsents(t, app, samUser, "all")) != 0 {
		t.Fatal("a refused takeover turned push on")
	}
	// The same subscription, keys and all, is the shared-browser case.
	if got, _ := subscribe(t, sam, sub); !got.Saved {
		t.Fatal("rebind refused")
	}
	app.DB.First(&after, mine.Device.ID)
	if after.UserID != samUser.ID {
		t.Fatal("the same subscription wasn't rebound")
	}
}

// Under its lock, subscribe rechecks the account against the session:
// revoked or disabled meanwhile, it answers 401 and saves nothing.
func TestPushSubscribeRechecksTheSession(t *testing.T) {
	app, _ := pushTestApp(t)
	u := newTestUser(t, app, "pat", "password123456", models.RoleMember)
	stale := *u
	app.DB.Model(u).Update("session_version", u.SessionVersion+1)
	if w := asUser(t, &stale, "PUT", app.apiPushSubscribe, newBrowserSub(t, "phone")); w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked meanwhile: %d %s", w.Code, w.Body.String())
	}
	var fresh models.User
	app.DB.First(&fresh, u.ID)
	app.DB.Model(u).Update("status", models.UserDisabled)
	if w := asUser(t, &fresh, "PUT", app.apiPushSubscribe, newBrowserSub(t, "phone")); w.Code != http.StatusUnauthorized {
		t.Fatalf("disabled meanwhile: %d %s", w.Code, w.Body.String())
	}
	if len(userDevices(app, u)) != 0 || len(pushConsents(t, app, u, "all")) != 0 {
		t.Fatal("a stale session saved something")
	}
}

// The church's switch (M3.13c2, DEC-124): on by default, admins only,
// audited; off tells the card and the public directions page through
// /push/config and /site, refuses turning on and tests, and stops sends.
func TestPushEnabledSetting(t *testing.T) {
	app, fake := pushTestApp(t)
	newTestUser(t, app, "root", "password1234", models.RoleAdmin)
	samUser := pushMember(t, app, "sam")
	phone := newPushDevice(t, app, samUser, "sam-phone")
	root, sam := loginAPI(t, app, "root", "password1234"), loginAPI(t, app, "sam", "password123456")
	for _, method := range []string{"GET", "PUT"} {
		if w := sam.do(method, "/api/v1/admin/settings/push-enabled", map[string]any{"on": false}, true); w.Code != http.StatusForbidden {
			t.Fatalf("member %s: %d", method, w.Code)
		}
	}
	anon := newAPIClient(t, app)
	site := func() string { return anon.do("GET", "/api/v1/site", nil, false).Body.String() }
	if w := root.do("GET", "/api/v1/admin/settings/push-enabled", nil, false); !strings.Contains(w.Body.String(), `"on":true`) {
		t.Fatalf("default: %s", w.Body.String())
	}
	if !strings.Contains(site(), `"pushEnabled":true`) {
		t.Fatalf("site default: %s", site())
	}
	if w := root.do("PUT", "/api/v1/admin/settings/push-enabled", map[string]any{}, true); w.Code != http.StatusBadRequest {
		t.Fatalf("no value: %d", w.Code)
	}
	if w := root.do("PUT", "/api/v1/admin/settings/push-enabled", map[string]any{"on": false}, true); w.Code != http.StatusOK {
		t.Fatalf("off: %d %s", w.Code, w.Body.String())
	}
	if app.settingValue(settingPushEnabled) != "false" || !strings.Contains(site(), `"pushEnabled":false`) {
		t.Fatalf("stored %q, site %s", app.settingValue(settingPushEnabled), site())
	}
	if w := sam.do("GET", "/api/v1/push/config", nil, false); !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatalf("config: %s", w.Body.String())
	}
	if _, w := subscribe(t, sam, newBrowserSub(t, "off")); w.Code != http.StatusForbidden || errorCodeOf(w) != "push_church_off" {
		t.Fatalf("subscribe while off: %d %s", w.Code, w.Body.String())
	}
	if w := sam.do("POST", "/api/v1/push/test", map[string]string{"endpoint": phone.Endpoint}, true); w.Code != http.StatusForbidden || errorCodeOf(w) != "push_church_off" {
		t.Fatalf("test while off: %d %s", w.Code, w.Body.String())
	}
	// A push already queued is stopped at send time.
	n := sendNotice(t, app, notify.AccountTurned18, samUser)
	runPushJobs(t, app, n, phone)
	if status, reason := pushDelivery(t, app, n); status != models.DeliverySuppressed || reason != pushWhyChurchOff {
		t.Fatalf("queued push while off: %q %q", status, reason)
	}
	if fake.total() != 0 {
		t.Fatalf("sent while off: %v", fake.sent)
	}
	if w := root.do("PUT", "/api/v1/admin/settings/push-enabled", map[string]any{"on": true}, true); w.Code != http.StatusOK || !app.pushEnabled() {
		t.Fatalf("on again: %d", w.Code)
	}
	if auditCount(t, app, audit.PushEnabledChanged) != 2 {
		t.Fatal("each change is audited")
	}
}
