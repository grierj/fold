package main

import (
	"context"
	"errors"
	"log"
	"math"
	"net/http"
	"strings"
	"time"

	"fold/internal/audit"
	"fold/internal/policy"
	"fold/internal/push"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Web push API (M3.13b, research 2.2, 2.5, 2.8). A device is a browser
// installation that turned on notifications; its endpoint is a capability,
// so no response, log line, audit row or error carries an endpoint or key.
// The browser already has its own; it names a device by sending it.

const (
	// maxPushDevices: the 11th device replaces the one least recently used.
	maxPushDevices = 10
	// pushSavesPerHour and pushTestsPerHour are DEC-129's limits. Past
	// them the answer is 200 with a line the card shows, never a 429.
	pushSavesPerHour = 30
	pushTestsPerHour = 10
)

// Lines the card shows past a limit or when a test didn't arrive (DEC-129).
const (
	pushSaveLimitMessage = "Try again in a few minutes."
	pushTestLimitMessage = "You just sent a few tests. Try again in a few minutes."
	pushTestGoneMessage  = "This device stopped taking notifications. Turn them on again."
	pushTestFailMessage  = "The notification service didn't take the test. Try again in a few minutes."
)

// Errors a person can act on (DEC-61; codes in api_errors.go).
const (
	errPushServiceUnknown = "that browser's notification service isn't one we can send to"
	errPushGuest          = "notifications on a device need a Fold account"
	errPushConsent        = "notifications wait until your account's consent is complete"
	errPushChurchOff      = "your church has turned off notifications on devices"
	errPushDeviceUnknown  = "this device doesn't have notifications on"
	errPushKindLocked     = "that kind of notification can't be turned off"
)

// pushConfigJSON is what the card needs before it asks the browser.
type pushConfigJSON struct {
	// Enabled is the church's switch (push_enabled, default on).
	Enabled bool `json:"enabled"`
	// PublicKey is the VAPID public key, the applicationServerKey.
	PublicKey string `json:"publicKey"`
}

// pushDeviceJSON is one device with push on, by its server-made label.
type pushDeviceJSON struct {
	ID         uint      `json:"id"`
	Label      string    `json:"label"`
	CreatedAt  time.Time `json:"createdAt"`
	LastUsedAt time.Time `json:"lastUsedAt"`
}

// pushSubscribeJSON answers PUT /push/subscriptions. Saved false (past the
// hourly limit) comes with Message and saves nothing.
type pushSubscribeJSON struct {
	Saved   bool            `json:"saved"`
	Device  *pushDeviceJSON `json:"device,omitempty"`
	Message string          `json:"message,omitempty"`
}

// pushTestJSON answers POST /push/test. Sent false comes with Message.
type pushTestJSON struct {
	Sent    bool   `json:"sent"`
	Message string `json:"message,omitempty"`
}

// pushSettingsJSON is the push section of Me → Notifications (DEC-122).
type pushSettingsJSON struct {
	// On: the person's latest (push, all) consent opts in.
	On bool `json:"on"`
	// Devices with push on, newest first.
	Devices []pushDeviceJSON `json:"devices"`
	// Kinds are the optional categories' Push switches; required ones
	// (safety, tasks waiting on you) always push and aren't listed.
	Kinds []emailKindJSON `json:"kinds"`
}

func deviceJSON(d models.PushSubscription) pushDeviceJSON {
	return pushDeviceJSON{ID: d.ID, Label: d.Label, CreatedAt: d.CreatedAt, LastUsedAt: d.LastUsedAt}
}

func (a *App) pushEnabled() bool { return a.settingValue(settingPushEnabled) != "false" }

// pushHosts is the allow-list the client sends to.
func (a *App) pushHosts() *push.AllowList {
	if h := a.pushClient().Hosts; h != nil {
		return h
	}
	return push.NewAllowList(a.Config.PushHosts...)
}

// refusePush answers 403 when me may not turn on or test push; true means
// it answered.
func (a *App) refusePush(c *gin.Context, me *models.User) bool {
	switch policy.PushRefusal(me.Status, a.pushEnabled()) {
	case policy.PushOK:
		return false
	case policy.PushGuest:
		jsonError(c, http.StatusForbidden, errPushGuest)
	case policy.PushPendingConsent:
		jsonError(c, http.StatusForbidden, errPushConsent)
	case policy.PushChurchOff:
		jsonError(c, http.StatusForbidden, errPushChurchOff)
	default:
		jsonError(c, http.StatusUnauthorized, "unauthorized")
	}
	return true
}

func (a *App) apiPushConfig(c *gin.Context) {
	public, _, err := readVAPID(a.DB)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load the notification settings")
		return
	}
	c.JSON(http.StatusOK, pushConfigJSON{Enabled: a.pushEnabled(), PublicKey: public})
}

// pushSubscriptionBody is a browser's PushSubscription.toJSON().
type pushSubscriptionBody struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
	// ExpirationTime is milliseconds since the epoch, usually null.
	ExpirationTime *float64 `json:"expirationTime"`
}

var errPushLimit = errors.New("push: hourly limit")

// apiPushSubscribe saves this browser's subscription for the signed-in
// person (upsert by endpoint). An endpoint another account holds is rebound:
// whoever holds the device holds the subscription, and the old account
// hears nothing (there is nothing for it to do, DEC-52). The first device,
// or the first after "Turn off notifications everywhere", is the opt-in
// (DEC-122).
func (a *App) apiPushSubscribe(c *gin.Context) {
	me := apiUser(c)
	if a.refusePush(c, me) {
		return
	}
	var body pushSubscriptionBody
	if err := c.ShouldBindJSON(&body); err != nil || body.Endpoint == "" || body.Keys.P256dh == "" || body.Keys.Auth == "" {
		jsonError(c, http.StatusBadRequest, "invalid request")
		return
	}
	if a.pushHosts().Allowed(body.Endpoint) != nil {
		jsonError(c, http.StatusBadRequest, errPushServiceUnknown)
		return
	}
	if push.ValidKeys(body.Keys.P256dh, body.Keys.Auth) != nil || len(body.Keys.P256dh) > 200 || len(body.Keys.Auth) > 100 {
		jsonError(c, http.StatusBadRequest, "invalid request")
		return
	}
	var expires *time.Time
	if e := body.ExpirationTime; e != nil && *e > 0 && *e < math.MaxInt64/2 {
		t := time.UnixMilli(int64(*e)).UTC()
		expires = &t
	}
	label, evidence := pushDeviceLabel(c.Request.UserAgent())
	var dev models.PushSubscription
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT 1 FROM users WHERE id = ? FOR UPDATE", me.ID).Error; err != nil {
			return err
		}
		var saves int64
		if err := tx.Model(&models.AuditLog{}).Where("actor_id = ? AND action = ? AND at > ?", me.ID, audit.PushDeviceOn, time.Now().Add(-time.Hour)).
			Count(&saves).Error; err != nil {
			return err
		}
		if saves >= pushSavesPerHour {
			return errPushLimit
		}
		var prior []models.PushSubscription
		if err := tx.Select("id", "user_id").Where("endpoint = ?", body.Endpoint).Find(&prior).Error; err != nil {
			return err
		}
		rebound := len(prior) == 1 && prior[0].UserID != me.ID
		if err := tx.Raw(`INSERT INTO push_subscriptions (user_id, endpoint, p256dh, auth, expires_at, label)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (endpoint) DO UPDATE SET
				created_at = CASE WHEN push_subscriptions.user_id = excluded.user_id THEN push_subscriptions.created_at ELSE now() END,
				user_id = excluded.user_id, p256dh = excluded.p256dh, auth = excluded.auth, expires_at = excluded.expires_at,
				label = excluded.label, last_used_at = now(), failed_at = NULL
			RETURNING id, user_id, label, created_at, last_used_at`,
			me.ID, body.Endpoint, body.Keys.P256dh, body.Keys.Auth, expires, label).Scan(&dev).Error; err != nil {
			return err
		}
		// At most ten: the 11th replaces the one least recently used.
		if err := tx.Exec(`DELETE FROM push_subscriptions WHERE id IN (
			SELECT id FROM push_subscriptions WHERE user_id = ? AND id <> ?
			ORDER BY last_used_at DESC, id DESC OFFSET ?)`, me.ID, dev.ID, maxPushDevices-1).Error; err != nil {
			return err
		}
		if _, err := setConsent(tx, models.Consent{UserID: me.ID, Channel: models.ConsentPush, Scope: "all", Status: models.ConsentOptedIn,
			Source: models.ConsentSourceWebForm, EvidenceText: "Turned on notifications for " + evidence,
			IP: c.ClientIP(), UserAgent: c.Request.UserAgent()}, models.ConsentOptedOut); err != nil {
			return err
		}
		return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.PushDeviceOn, TargetType: "user", TargetID: me.ID,
			Meta: map[string]any{"deviceId": dev.ID, "label": label, "rebound": rebound}, IP: c.ClientIP()})
	})
	if errors.Is(err, errPushLimit) {
		c.JSON(http.StatusOK, pushSubscribeJSON{Saved: false, Message: pushSaveLimitMessage})
		return
	}
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not turn on notifications")
		return
	}
	d := deviceJSON(dev)
	c.JSON(http.StatusOK, pushSubscribeJSON{Saved: true, Device: &d})
}

// pushEndpointBody names this browser's device by its endpoint.
type pushEndpointBody struct {
	Endpoint string `json:"endpoint"`
}

// apiPushUnsubscribe is "Turn off notifications on this device": it
// deletes the device if it is the person's own. It answers the same either
// way, so it says nothing about anyone else's device.
func (a *App) apiPushUnsubscribe(c *gin.Context) {
	me := apiUser(c)
	var body pushEndpointBody
	if err := c.ShouldBindJSON(&body); err != nil || body.Endpoint == "" {
		jsonError(c, http.StatusBadRequest, "invalid request")
		return
	}
	if err := a.DB.Where("endpoint = ? AND user_id = ?", body.Endpoint, me.ID).Delete(&models.PushSubscription{}).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not turn off notifications")
		return
	}
	c.JSON(http.StatusOK, gin.H{})
}

// apiPushTurnOff is "Turn off notifications everywhere": an opt-out that
// overrides every per-category switch (DEC-122), and every device deleted.
func (a *App) apiPushTurnOff(c *gin.Context) {
	me := apiUser(c)
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if _, err := setConsent(tx, models.Consent{UserID: me.ID, Channel: models.ConsentPush, Scope: "all", Status: models.ConsentOptedOut,
			Source: models.ConsentSourceWebForm, EvidenceText: "Turned off notifications everywhere",
			IP: c.ClientIP(), UserAgent: c.Request.UserAgent()}, models.ConsentOptedOut); err != nil {
			return err
		}
		return tx.Where("user_id = ?", me.ID).Delete(&models.PushSubscription{}).Error
	})
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not turn off notifications")
		return
	}
	s, err := a.pushSettingsFor(a.DB, me)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load notification settings")
		return
	}
	c.JSON(http.StatusOK, gin.H{"push": s})
}

// apiPushTest sends one test notification to this browser's device, now,
// through the same client and allow-list as push.deliver. Ten an hour per
// person (DEC-129); past that, and when the device didn't take it, the
// answer is 200 with a line the card shows.
func (a *App) apiPushTest(c *gin.Context) {
	me := apiUser(c)
	if a.refusePush(c, me) {
		return
	}
	var body pushEndpointBody
	if err := c.ShouldBindJSON(&body); err != nil || body.Endpoint == "" {
		jsonError(c, http.StatusBadRequest, "invalid request")
		return
	}
	var dev models.PushSubscription
	if err := a.DB.Where("endpoint = ? AND user_id = ?", body.Endpoint, me.ID).First(&dev).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		jsonError(c, http.StatusNotFound, errPushDeviceUnknown)
		return
	} else if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not send the test")
		return
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT 1 FROM users WHERE id = ? FOR UPDATE", me.ID).Error; err != nil {
			return err
		}
		var tests int64
		if err := tx.Model(&models.AuditLog{}).Where("actor_id = ? AND action = ? AND at > ?", me.ID, audit.PushTestSent, time.Now().Add(-time.Hour)).
			Count(&tests).Error; err != nil {
			return err
		}
		if tests >= pushTestsPerHour {
			return errPushLimit
		}
		return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.PushTestSent, TargetType: "user", TargetID: me.ID,
			Meta: map[string]any{"deviceId": dev.ID}, IP: c.ClientIP()})
	})
	if errors.Is(err, errPushLimit) {
		c.JSON(http.StatusOK, pushTestJSON{Sent: false, Message: pushTestLimitMessage})
		return
	}
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not send the test")
		return
	}
	payload, err := push.MarshalPayload(push.NewPayload(push.Notification{
		Title: "Notifications are on", Body: "This is a test from " + a.siteName() + ".",
		Navigate: a.emailLink("/profile/notifications"), Tag: "test",
	}))
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not send the test")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	sub := push.Subscription{Endpoint: dev.Endpoint, P256dh: dev.P256dh, Auth: dev.Auth}
	// A test is for now: if the device is off for ten minutes, drop it.
	res := a.pushClient().Send(ctx, sub, payload, push.Options{TTL: 10 * time.Minute, Urgency: push.UrgencyHigh, Topic: "test"})
	switch res.Status {
	case push.Accepted:
		if err := a.DB.Model(&models.PushSubscription{}).Where("id = ?", dev.ID).
			UpdateColumns(map[string]any{"last_used_at": time.Now().UTC(), "failed_at": nil}).Error; err != nil {
			log.Printf("push: device %d: marking it used: %v", dev.ID, err)
		}
		c.JSON(http.StatusOK, pushTestJSON{Sent: true})
	case push.Gone:
		log.Printf("push: test: device gone for user %d (device %d, %d)", me.ID, dev.ID, res.Code)
		if err := a.DB.Delete(&models.PushSubscription{}, dev.ID).Error; err != nil {
			log.Printf("push: device %d: deleting it: %v", dev.ID, err)
		}
		c.JSON(http.StatusOK, pushTestJSON{Sent: false, Message: pushTestGoneMessage})
	default:
		log.Printf("push: test to device %d for user %d: %s (%d)", dev.ID, me.ID, res.Status, res.Code)
		c.JSON(http.StatusOK, pushTestJSON{Sent: false, Message: pushTestFailMessage})
	}
}

// pushSettingsFor is the push section of the settings payload.
func (a *App) pushSettingsFor(db *gorm.DB, u *models.User) (pushSettingsJSON, error) {
	out := pushSettingsJSON{Devices: []pushDeviceJSON{}, Kinds: []emailKindJSON{}}
	var rows []models.Consent
	if err := db.Raw(`SELECT DISTINCT ON (scope) scope, status FROM consents
		WHERE user_id = ? AND channel = ? ORDER BY scope, at DESC, id DESC`, u.ID, models.ConsentPush).Scan(&rows).Error; err != nil {
		return out, err
	}
	off := map[string]bool{}
	for _, r := range rows {
		if r.Scope == "all" {
			out.On = r.Status == models.ConsentOptedIn
		} else if r.Status == models.ConsentOptedOut {
			off[r.Scope] = true
		}
	}
	for _, cat := range emailKindsFor(u) {
		out.Kinds = append(out.Kinds, emailKindJSON{Category: cat, Off: off[categoryScope(cat)]})
	}
	var devices []models.PushSubscription
	if err := db.Select("id", "label", "created_at", "last_used_at").Where("user_id = ?", u.ID).
		Order("created_at DESC, id DESC").Find(&devices).Error; err != nil {
		return out, err
	}
	for _, d := range devices {
		out.Devices = append(out.Devices, deviceJSON(d))
	}
	return out, nil
}

// applyPushSettings writes one Push switch ({channel: "push", category,
// off}); false means an error was sent. Only the optional categories have
// a switch: required kinds always push to a device with push on (DEC-122).
func (a *App) applyPushSettings(c *gin.Context, u *models.User, category string, off *bool) bool {
	if off == nil {
		jsonError(c, http.StatusBadRequest, "invalid request")
		return false
	}
	found := false
	for _, k := range emailKindsFor(u) {
		found = found || k == category
	}
	if !found {
		jsonError(c, http.StatusBadRequest, errPushKindLocked)
		return false
	}
	status := models.ConsentOptedIn
	if *off {
		status = models.ConsentOptedOut
	}
	// No row means on: with a device, every optional category pushes.
	if _, err := setConsent(a.DB, models.Consent{UserID: u.ID, Channel: models.ConsentPush, Scope: categoryScope(category), Status: status,
		Source: models.ConsentSourceWebForm, IP: c.ClientIP(), UserAgent: c.Request.UserAgent()}, models.ConsentOptedIn); err != nil {
		jsonError(c, http.StatusInternalServerError, "could not save notification settings")
		return false
	}
	return true
}

// endPushDeviceAtLogout deletes the device whose endpoint the logout
// request carried (research 2.8), in the same request, so the next person
// on a shared tablet gets nothing meant for this one. The endpoint is the
// device's own capability, so it is deleted whichever account holds it:
// the browser presenting it is that device, even when its session has
// already expired.
func (a *App) endPushDeviceAtLogout(endpoint string) {
	if endpoint == "" {
		return
	}
	if err := a.DB.Where("endpoint = ?", endpoint).Delete(&models.PushSubscription{}).Error; err != nil {
		log.Printf("push: logout: deleting this browser's device: %v", err)
	}
}

// pushDeviceLabel names a device from its User-Agent, never from anything
// the client wrote: the card's label ("iPhone · Home Screen app", "Chrome
// on Windows") and the consent evidence ("iPhone (Home Screen app)").
// iOS pushes only to a Home Screen web app, so an iPhone or iPad
// subscription is one.
func pushDeviceLabel(ua string) (label, evidence string) {
	for _, d := range []string{"iPhone", "iPad"} {
		if strings.Contains(ua, d) {
			return d + " · Home Screen app", d + " (Home Screen app)"
		}
	}
	browser := ""
	for _, b := range []struct{ token, name string }{
		{"SamsungBrowser/", "Samsung Internet"},
		{"EdgA/", "Edge"}, {"Edg/", "Edge"},
		{"OPR/", "Opera"},
		{"Firefox/", "Firefox"},
		{"Chrome/", "Chrome"}, {"Chromium/", "Chromium"},
		{"Safari/", "Safari"},
	} {
		if strings.Contains(ua, b.token) {
			browser = b.name
			break
		}
	}
	system := ""
	for _, o := range []struct{ token, name string }{
		{"Android", "Android"},
		{"Windows", "Windows"},
		{"CrOS", "ChromeOS"},
		{"Macintosh", "Mac"},
		{"Linux", "Linux"},
	} {
		if strings.Contains(ua, o.token) {
			system = o.name
			break
		}
	}
	switch {
	case browser != "" && system != "":
		label = browser + " on " + system
	case browser != "":
		label = browser
	case system != "":
		label = "A browser on " + system
	default:
		label = "A web browser"
	}
	return label, label
}
