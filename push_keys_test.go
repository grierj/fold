package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"fold/internal/audit"
	"fold/internal/notify"
	"fold/internal/push"
	"fold/models"
)

func vapidRows(t *testing.T, app *App) (public, enc string) {
	t.Helper()
	public, enc, err := readVAPID(app.DB)
	if err != nil {
		t.Fatal(err)
	}
	return public, enc
}

func pushDeviceCount(app *App) int64 {
	var n int64
	app.DB.Model(&models.PushSubscription{}).Count(&n)
	return n
}

func TestVAPIDKeysAreMadeOnceOnBoot(t *testing.T) {
	app := testApp(t)
	secret := app.Config.SessionSecret
	// Several processes booting at once make one pair.
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := ensureVAPIDKeys(app.DB, secret)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	public, enc := vapidRows(t, app)
	if len(public) != 87 || enc == "" || strings.Contains(enc, public) {
		t.Fatalf("public %q enc %q", public, enc)
	}
	// The private half decrypts with the secret and matches the public half.
	key, err := openVAPIDKey(secret, enc)
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := push.PublicKey(key); p != public {
		t.Fatal("the stored halves don't match")
	}
	// Another boot keeps the pair, devices and all.
	u := newTestUser(t, app, "pat", "password123456", models.RoleMember)
	newPushDevice(t, app, u, "phone")
	if notices, err := ensureVAPIDKeys(app.DB, secret); err != nil || len(notices) != 0 {
		t.Fatalf("second boot: %v %v", notices, err)
	}
	if p, e := vapidRows(t, app); p != public || e != enc || pushDeviceCount(app) != 1 {
		t.Fatal("a second boot changed the keys or removed devices")
	}
	var notes int64
	app.DB.Model(&models.Notification{}).Count(&notes)
	if notes != 0 || auditCount(t, app, audit.PushKeysReset) != 0 {
		t.Fatal("first boot wrote a notice or an audit row")
	}
}

func TestVAPIDKeySealIsBoundToTheSecret(t *testing.T) {
	key, err := push.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := sealVAPIDKey([]byte("secret one, long enough for prod use"), key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := openVAPIDKey([]byte("secret one, long enough for prod use"), enc)
	if err != nil || !got.Equal(key) {
		t.Fatalf("round trip: %v", err)
	}
	for name, in := range map[string]string{"other secret": enc, "not base64": "!!", "short": "AAAA", "tampered": enc[:len(enc)-2] + "AA"} {
		secret := "secret one, long enough for prod use"
		if name == "other secret" {
			secret = "secret two, long enough for prod use"
		}
		if _, err := openVAPIDKey([]byte(secret), in); !errors.Is(err, errVAPIDUnreadable) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestSessionSecretChangeResetsPushKeys(t *testing.T) {
	app := testApp(t)
	if _, err := ensureVAPIDKeys(app.DB, []byte("the old secret, long enough for prod")); err != nil {
		t.Fatal(err)
	}
	oldPublic, _ := vapidRows(t, app)
	admin := newTestUser(t, app, "admin", "password123456", models.RoleAdmin)
	pat := newTestUser(t, app, "pat", "password123456", models.RoleMember)
	newPushDevice(t, app, pat, "phone")
	newPushDevice(t, app, admin, "laptop")

	newSecret := []byte("the new secret, long enough for prod")
	notices, err := ensureVAPIDKeys(app.DB, newSecret)
	if err != nil {
		t.Fatal(err)
	}
	public, enc := vapidRows(t, app)
	if public == oldPublic {
		t.Fatal("the keys weren't made again")
	}
	if _, err := openVAPIDKey(newSecret, enc); err != nil {
		t.Fatalf("the new key doesn't open with the new secret: %v", err)
	}
	if pushDeviceCount(app) != 0 {
		t.Fatal("devices bound to the old key are still there")
	}
	var rows []models.Notification
	app.DB.Where("kind = ?", notify.PushKeysReset).Find(&rows)
	if len(rows) != 1 || rows[0].UserID != admin.ID || len(notices) != 1 || notices[0] != rows[0].ID ||
		rows[0].Title != "Notifications on devices were reset" || !strings.Contains(rows[0].Body, "Members need to turn them on again") {
		t.Fatalf("admin notice: %+v (%v)", rows, notices)
	}
	if auditCount(t, app, audit.PushKeysReset) != 1 {
		t.Fatal("no audit row")
	}
	// The notice goes by email after commit, like other admin notices.
	app.deliverNotificationEmails(notices)
	if m := latestEmail(t, app, admin.Email); !strings.Contains(m.Subject, "Notifications on devices were reset") {
		t.Fatalf("email %q", m.Subject)
	}
	// Booting again with the new secret changes nothing.
	if again, err := ensureVAPIDKeys(app.DB, newSecret); err != nil || len(again) != 0 {
		t.Fatalf("second boot: %v %v", again, err)
	}
	if p, _ := vapidRows(t, app); p != public {
		t.Fatal("the second boot made keys again")
	}
}

func TestRotateRemovesDevicesAndARunningServerUsesTheNewKey(t *testing.T) {
	app := testApp(t)
	if _, err := ensureVAPIDKeys(app.DB, app.Config.SessionSecret); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	before, err := app.vapidKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := app.vapidKey(ctx); again != before {
		t.Fatal("the key isn't cached while the row is the same")
	}
	u := newTestUser(t, app, "pat", "password123456", models.RoleMember)
	newPushDevice(t, app, u, "phone")
	newPushDevice(t, app, u, "laptop")

	// As `fold push keys rotate` would, from another process.
	removed, err := rotateVAPIDKeys(app.DB, app.Config.SessionSecret)
	if err != nil || removed != 2 || pushDeviceCount(app) != 0 {
		t.Fatalf("rotate: %d %v", removed, err)
	}
	after, err := app.vapidKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	public, _ := vapidRows(t, app)
	if after.Equal(before) {
		t.Fatal("the running server still uses the old key")
	}
	if p, _ := push.PublicKey(after); p != public {
		t.Fatal("the running server's key isn't the stored one")
	}
	if auditCount(t, app, audit.PushKeysRotated) != 1 {
		t.Fatal("no audit row")
	}
	// The push client signs with it.
	if k, err := app.pushClient().Key(ctx); err != nil || !k.Equal(after) {
		t.Fatalf("push client key: %v", err)
	}
}

func TestVAPIDSubject(t *testing.T) {
	for _, tc := range []struct {
		from, site, want string
	}{
		{"First Church <hello@church.example>", "https://groups.church.example", "mailto:hello@church.example"},
		{"Fold <noreply@localhost>", "https://groups.church.example/", "https://groups.church.example"},
		{"Fold <noreply@localhost>", "", "mailto:noreply@localhost"},
	} {
		if got := vapidSubject(Config{EmailFrom: tc.from, SiteURL: tc.site}); got != tc.want {
			t.Errorf("%q %q: %q, want %q", tc.from, tc.site, got, tc.want)
		}
	}
}
