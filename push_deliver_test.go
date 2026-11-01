package main

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"fold/internal/jobs"
	"fold/internal/notify"
	"fold/internal/push"
	"fold/models"

	"gorm.io/gorm"
)

// fakePush is a push service behind the App's push client: it answers
// each device (by endpoint path) with the status the test set, and records
// every request it got.
type fakePush struct {
	mu      sync.Mutex
	status  map[string]int // by path; default 201
	err     map[string]bool
	sent    map[string]int
	headers []http.Header
}

func (f *fakePush) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		_, _ = io.Copy(io.Discard, r.Body)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent[r.URL.Path]++
	f.headers = append(f.headers, r.Header.Clone())
	if f.err[r.URL.Path] {
		// A transport error that, like many, repeats the URL.
		return nil, fmt.Errorf("connection reset talking to %s", r.URL)
	}
	code := http.StatusCreated
	if c, ok := f.status[r.URL.Path]; ok {
		code = c
	}
	return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
}

func (f *fakePush) set(path string, code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status[path] = code
}

func (f *fakePush) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sent[path]
}

func (f *fakePush) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.sent {
		n += c
	}
	return n
}

// pushTestApp is a test App with VAPID keys and the fake push service.
// Quiet hours are off, so a test's result never depends on the hour it
// runs; the quiet-hours tests (push_quiet_test.go) turn them on.
func pushTestApp(t *testing.T) (*App, *fakePush) {
	t.Helper()
	app := testApp(t)
	if _, err := ensureVAPIDKeys(app.DB, app.Config.SessionSecret); err != nil {
		t.Fatal(err)
	}
	f := &fakePush{status: map[string]int{}, err: map[string]bool{}, sent: map[string]int{}}
	app.Push = &push.Client{HTTP: &http.Client{Transport: f}, Subject: "mailto:admin@example.com"}
	open := push.AlwaysOpen
	app.pushWindow = &open
	return app, f
}

// testDevice is a device with real browser keys; its endpoint path is
// /fcm/send/<name>-secret.
type testDevice struct {
	models.PushSubscription
	path string
}

func newPushDevice(t *testing.T, app *App, u *models.User, name string) testDevice {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	path := "/fcm/send/" + name + "-secret"
	d := models.PushSubscription{UserID: u.ID, Endpoint: "https://fcm.googleapis.com" + path,
		P256dh: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), Auth: base64.RawURLEncoding.EncodeToString(auth), Label: name}
	if err := app.DB.Create(&d).Error; err != nil {
		t.Fatal(err)
	}
	return testDevice{d, path}
}

func pushConsentRow(t *testing.T, app *App, u *models.User, scope, status string) {
	t.Helper()
	if err := app.DB.Create(&models.Consent{UserID: u.ID, Channel: models.ConsentPush, Scope: scope, Status: status, Source: models.ConsentSourceWebForm}).Error; err != nil {
		t.Fatal(err)
	}
}

// pushMember is an active member who turned push on.
func pushMember(t *testing.T, app *App, name string) *models.User {
	t.Helper()
	u := newTestUser(t, app, name, "password123456", models.RoleMember)
	pushConsentRow(t, app, u, "all", models.ConsentOptedIn)
	return u
}

func sendNotice(t *testing.T, app *App, kind string, u *models.User) uint {
	t.Helper()
	ids, err := notify.Send(app.DB, notify.Msg{Kind: kind, To: []uint{u.ID}, Link: "/groups/1", Vars: notify.Vars{Group: "Youth"}})
	if err != nil || len(ids) != 1 {
		t.Fatalf("notify: %v %v", ids, err)
	}
	return ids[0]
}

func pushDelivery(t *testing.T, app *App, notificationID uint) (status, reason string) {
	t.Helper()
	var d models.NotificationDelivery
	if err := app.DB.Where("notification_id = ? AND channel = ?", notificationID, models.DeliveryPush).First(&d).Error; err != nil {
		return "", ""
	}
	return d.Status, d.Reason
}

// runPushJobs queues one push.deliver per device and runs every due job.
func runPushJobs(t *testing.T, app *App, notificationID uint, devices ...testDevice) {
	t.Helper()
	for _, d := range devices {
		if err := enqueuePushDelivery(app.DB, notificationID, d.ID, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	drainJobs(t, app)
}

func drainJobs(t *testing.T, app *App) {
	t.Helper()
	r := jobs.NewRunner(app.DB, app.pushKinds()...)
	for {
		ran, err := r.RunOnce(context.Background(), "test")
		if err != nil {
			t.Fatal(err)
		}
		if !ran {
			return
		}
	}
}

func deviceExists(app *App, id uint) bool {
	var n int64
	app.DB.Model(&models.PushSubscription{}).Where("id = ?", id).Count(&n)
	return n == 1
}

func TestPushDeliverRecordsOneSentRowForSeveralDevices(t *testing.T) {
	app, f := pushTestApp(t)
	u := pushMember(t, app, "pat")
	phone, laptop := newPushDevice(t, app, u, "phone"), newPushDevice(t, app, u, "laptop")
	n := sendNotice(t, app, notify.GroupApproved, u)
	runPushJobs(t, app, n, phone, laptop)
	if f.count(phone.path) != 1 || f.count(laptop.path) != 1 {
		t.Fatalf("sends: %v", f.sent)
	}
	if status, _ := pushDelivery(t, app, n); status != models.DeliverySent {
		t.Fatalf("delivery %q", status)
	}
	var rows int64
	app.DB.Model(&models.NotificationDelivery{}).Where("notification_id = ?", n).Count(&rows)
	if rows != 1 {
		t.Fatalf("%d delivery rows", rows)
	}
	h := f.headers[0]
	if h.Get("Content-Encoding") != "aes128gcm" || h.Get("TTL") != "86400" || h.Get("Topic") != fmt.Sprintf("n%d", n) || !strings.HasPrefix(h.Get("Authorization"), "vapid t=") {
		t.Fatalf("headers %v", h)
	}
	// Queuing the same device again adds nothing: one job per device.
	runPushJobs(t, app, n, phone)
	if f.count(phone.path) != 1 {
		t.Fatalf("phone got it %d times", f.count(phone.path))
	}
}

func TestPushDeliverGoneDeletesTheDevice(t *testing.T) {
	for _, code := range []int{http.StatusNotFound, http.StatusGone} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			app, f := pushTestApp(t)
			u := pushMember(t, app, "pat")
			phone := newPushDevice(t, app, u, "phone")
			f.set(phone.path, code)
			n := sendNotice(t, app, notify.GroupApproved, u)
			runPushJobs(t, app, n, phone)
			if deviceExists(app, phone.ID) {
				t.Fatal("the device is still there")
			}
			if status, reason := pushDelivery(t, app, n); status != models.DeliverySuppressed || reason != "device gone" {
				t.Fatalf("delivery %q %q", status, reason)
			}
		})
	}
	// With another device that takes it, the row is sent, whatever order.
	app, f := pushTestApp(t)
	u := pushMember(t, app, "pat")
	old, phone := newPushDevice(t, app, u, "old"), newPushDevice(t, app, u, "phone")
	f.set(old.path, http.StatusGone)
	n := sendNotice(t, app, notify.GroupApproved, u)
	runPushJobs(t, app, n, old, phone)
	if status, _ := pushDelivery(t, app, n); status != models.DeliverySent || deviceExists(app, old.ID) || !deviceExists(app, phone.ID) {
		t.Fatalf("delivery %q", status)
	}
}

func TestPushDeliverRetriesWithoutSendingTwice(t *testing.T) {
	for _, code := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			app, f := pushTestApp(t)
			u := pushMember(t, app, "pat")
			phone, laptop := newPushDevice(t, app, u, "phone"), newPushDevice(t, app, u, "laptop")
			f.set(laptop.path, code)
			n := sendNotice(t, app, notify.GroupApproved, u)
			runPushJobs(t, app, n, phone, laptop)
			var failed models.Job
			if err := app.DB.Where("kind = ? AND status = ?", kindPushDeliver, models.JobFailed).First(&failed).Error; err != nil {
				t.Fatalf("no retrying job: %v", err)
			}
			if failed.UniqueKey == nil || *failed.UniqueKey != fmt.Sprintf("push:n:%d:d:%d", n, laptop.ID) || failed.Attempts != 1 {
				t.Fatalf("job %+v", failed)
			}
			// The service recovers; the retry repeats only the laptop.
			f.set(laptop.path, http.StatusCreated)
			app.DB.Model(&models.Job{}).Where("id = ?", failed.ID).Update("run_at", time.Now().Add(-time.Second))
			drainJobs(t, app)
			if f.count(phone.path) != 1 || f.count(laptop.path) != 2 {
				t.Fatalf("sends %v", f.sent)
			}
			if status, _ := pushDelivery(t, app, n); status != models.DeliverySent {
				t.Fatalf("delivery %q", status)
			}
		})
	}
}

func TestPushDeliverLastAttemptRecordsFailure(t *testing.T) {
	app, f := pushTestApp(t)
	u := pushMember(t, app, "pat")
	phone := newPushDevice(t, app, u, "phone")
	f.set(phone.path, http.StatusInternalServerError)
	n := sendNotice(t, app, notify.GroupApproved, u)
	runPushJobs(t, app, n, phone)
	for range pushDeliverAttempts {
		app.DB.Model(&models.Job{}).Where("kind = ?", kindPushDeliver).Update("run_at", time.Now().Add(-time.Second))
		drainJobs(t, app)
	}
	var j models.Job
	app.DB.Where("kind = ?", kindPushDeliver).First(&j)
	if j.Status != models.JobDead || f.count(phone.path) != pushDeliverAttempts {
		t.Fatalf("job %s after %d sends", j.Status, f.count(phone.path))
	}
	var dev models.PushSubscription
	app.DB.First(&dev, phone.ID)
	if status, reason := pushDelivery(t, app, n); status != models.DeliveryFailed || reason != pushWhyNotReached || dev.FailedAt == nil {
		t.Fatalf("delivery %q %q, failed at %v", status, reason, dev.FailedAt)
	}
}

func TestPushDeliverFailures(t *testing.T) {
	for code, want := range map[int]string{http.StatusForbidden: pushWhyRefused, http.StatusRequestEntityTooLarge: pushWhyTooLarge} {
		app, f := pushTestApp(t)
		u := pushMember(t, app, "pat")
		phone := newPushDevice(t, app, u, "phone")
		f.set(phone.path, code)
		n := sendNotice(t, app, notify.GroupApproved, u)
		runPushJobs(t, app, n, phone)
		if status, reason := pushDelivery(t, app, n); status != models.DeliveryFailed || reason != want {
			t.Errorf("%d: delivery %q %q", code, status, reason)
		}
		if f.count(phone.path) != 1 || !deviceExists(app, phone.ID) {
			t.Errorf("%d: %d sends", code, f.count(phone.path))
		}
	}
	// A rendering past the cap is refused before any send.
	app, f := pushTestApp(t)
	app.pushRender = func(*models.Notification) (push.Notification, bool) {
		return push.Notification{Title: strings.Repeat("a", push.MaxPlaintext)}, true
	}
	u := pushMember(t, app, "pat")
	phone := newPushDevice(t, app, u, "phone")
	n := sendNotice(t, app, notify.GroupApproved, u)
	runPushJobs(t, app, n, phone)
	if status, reason := pushDelivery(t, app, n); status != models.DeliveryFailed || reason != pushWhyTooLarge || f.total() != 0 {
		t.Fatalf("delivery %q %q after %d sends", status, reason, f.total())
	}
}

func TestPushDeliverChecksConsentStatusAndTheChurchSwitch(t *testing.T) {
	app, f := pushTestApp(t)
	// Groups turned off: an optional groups kind is skipped, a required
	// (safety) kind still goes.
	pat := pushMember(t, app, "pat")
	pushConsentRow(t, app, pat, "category:groups", models.ConsentOptedOut)
	phone := newPushDevice(t, app, pat, "phone")
	optional := sendNotice(t, app, notify.GroupApproved, pat)
	required := sendNotice(t, app, notify.AccountTurned18, pat)
	runPushJobs(t, app, optional, phone)
	runPushJobs(t, app, required, phone)
	if status, reason := pushDelivery(t, app, optional); status != models.DeliverySuppressed || reason != "push turned off for group updates" {
		t.Errorf("optional: %q %q", status, reason)
	}
	if status, _ := pushDelivery(t, app, required); status != models.DeliverySent || f.count(phone.path) != 1 {
		t.Errorf("required: %q after %d sends", status, f.count(phone.path))
	}
	// Turning the category back on (a later row) lets it through.
	pushConsentRow(t, app, pat, "category:groups", models.ConsentOptedIn)
	again := sendNotice(t, app, notify.GroupRejected, pat)
	runPushJobs(t, app, again, phone)
	if status, _ := pushDelivery(t, app, again); status != models.DeliverySent {
		t.Errorf("after opting back in: %q", status)
	}

	// "Turn off notifications everywhere" overrides everything, required too.
	lee := pushMember(t, app, "lee")
	pushConsentRow(t, app, lee, "all", models.ConsentOptedOut)
	leePhone := newPushDevice(t, app, lee, "lee-phone")
	n := sendNotice(t, app, notify.AccountTurned18, lee)
	runPushJobs(t, app, n, leePhone)
	if status, reason := pushDelivery(t, app, n); status != models.DeliverySuppressed || reason != pushWhyNotOn {
		t.Errorf("all off: %q %q", status, reason)
	}

	// A disabled account gets nothing. (Disabling deletes its devices; this
	// one is written after, as a restore could leave one.)
	sam := pushMember(t, app, "sam")
	app.DB.Model(sam).Update("status", models.UserDisabled)
	samPhone := newPushDevice(t, app, sam, "sam-phone")
	n = sendNotice(t, app, notify.AccountTurned18, sam)
	runPushJobs(t, app, n, samPhone)
	if status, reason := pushDelivery(t, app, n); status != models.DeliverySuppressed || reason != pushWhyInactive {
		t.Errorf("disabled: %q %q", status, reason)
	}

	// The church turned push off: nothing sends.
	app.DB.Create(&models.Setting{Key: settingPushEnabled, Value: "false"})
	n = sendNotice(t, app, notify.AccountTurned18, pat)
	runPushJobs(t, app, n, phone)
	if status, reason := pushDelivery(t, app, n); status != models.DeliverySuppressed || reason != pushWhyChurchOff {
		t.Errorf("church off: %q %q", status, reason)
	}
	if f.count(leePhone.path)+f.count(samPhone.path) != 0 || f.count(phone.path) != 2 {
		t.Errorf("sends %v", f.sent)
	}
}

func TestPushDeliverSkipsWhatIsNoLongerThere(t *testing.T) {
	app, f := pushTestApp(t)
	pat, lee := pushMember(t, app, "pat"), pushMember(t, app, "lee")
	phone := newPushDevice(t, app, pat, "phone")
	n := sendNotice(t, app, notify.GroupApproved, pat)
	// The device changed hands after the job was queued.
	enqueuePushDelivery(app.DB, n, phone.ID, time.Now())
	app.DB.Model(&models.PushSubscription{}).Where("id = ?", phone.ID).Update("user_id", lee.ID)
	drainJobs(t, app)
	// A device or notification that's gone.
	runPushJobs(t, app, n, testDevice{models.PushSubscription{ID: 99999}, ""})
	if err := app.deliverPush(context.Background(), pushJob{NotificationID: 99999, DeviceID: phone.ID}); err != nil {
		t.Fatal(err)
	}
	if f.total() != 0 {
		t.Fatalf("sends %v", f.sent)
	}
	var dead int64
	app.DB.Model(&models.Job{}).Where("status <> ?", models.JobDone).Count(&dead)
	if dead != 0 {
		t.Fatalf("%d jobs not done", dead)
	}
}

// A push that can't say what it's about is titled with the church's name
// (DEC-134 B); every other kind keeps its own title.
func TestSafePushesAreTitledWithTheChurch(t *testing.T) {
	app := testApp(t)
	app.DB.Create(&models.Setting{Key: "site_name", Value: "First Church"})
	safe := &models.Notification{Kind: notify.TeenConsentChanged, PushTitle: notify.SafePushTitle, PushBody: notify.SafePushBody}
	if note, ok := app.lockScreen(safe); !ok || note.Title != "First Church" || note.Body != notify.SafePushBody {
		t.Fatalf("safe push %+v", note)
	}
	// A household row of an event kind uses the safe line too.
	household := &models.Notification{Kind: notify.RegistrationPromoted, PushTitle: notify.SafePushTitle, PushBody: notify.SafePushBody}
	if note, _ := app.lockScreen(household); note.Title != "First Church" {
		t.Fatalf("household push %+v", note)
	}
	own := &models.Notification{Kind: notify.GroupApproved, PushTitle: "Youth was approved", PushBody: "It's live now."}
	if note, _ := app.lockScreen(own); note.Title != "Youth was approved" {
		t.Fatalf("specific push %+v", note)
	}
}

// The lock-screen text is the row's stored push text, and only for kinds
// that push; never the bell title or body.
func TestRenderPushUsesTheStoredPushText(t *testing.T) {
	for _, k := range notify.Kinds() {
		got, ok := renderPush(&models.Notification{Kind: k.Key, Title: "Bell title", Body: "Bell body", PushTitle: "Push title", PushBody: "Push body", Link: "/"})
		if ok != k.AllowPush {
			t.Errorf("%s: renders a push %v, AllowPush %v", k.Key, ok, k.AllowPush)
		}
		if ok && (got.Title != "Push title" || got.Body != "Push body") {
			t.Errorf("%s: pushed %+v", k.Key, got)
		}
	}
	if _, ok := renderPush(&models.Notification{Kind: notify.GroupApproved, Title: "Bell title"}); ok {
		t.Error("a row with no push text pushed")
	}
}

// After commit, every notify.Send caller's rows of a pushed kind queue one
// push.deliver per device (deliverNotificationEmails does it beside the
// email), with no delay; a kind that doesn't push, or a person with no
// device, queues nothing.
func TestNotificationsQueueOnePushPerDevice(t *testing.T) {
	app, f := pushTestApp(t)
	pat, lee := pushMember(t, app, "pat"), pushMember(t, app, "lee")
	phone, laptop := newPushDevice(t, app, pat, "phone"), newPushDevice(t, app, pat, "laptop")
	pushed := sendNotice(t, app, notify.GroupApproved, pat)
	quiet := sendNotice(t, app, notify.MembershipLeft, pat)
	nobody := sendNotice(t, app, notify.GroupApproved, lee) // lee has no device
	app.deliverNotificationEmails([]uint{pushed, quiet, nobody})

	var queued []models.Job
	app.DB.Where("kind = ?", kindPushDeliver).Order("id").Find(&queued)
	if len(queued) != 2 {
		t.Fatalf("%d jobs queued, want one per device", len(queued))
	}
	want := map[string]bool{fmt.Sprintf("push:n:%d:d:%d", pushed, phone.ID): true, fmt.Sprintf("push:n:%d:d:%d", pushed, laptop.ID): true}
	for _, j := range queued {
		if j.UniqueKey == nil || !want[*j.UniqueKey] || j.RunAt.After(time.Now()) {
			t.Errorf("job %v at %v", j.UniqueKey, j.RunAt)
		}
	}
	// Delivering the same rows again (a merge, a retry) queues nothing new.
	app.deliverNotificationEmails([]uint{pushed})
	var again int64
	app.DB.Model(&models.Job{}).Where("kind = ?", kindPushDeliver).Count(&again)
	if again != 2 {
		t.Fatalf("%d jobs after a second delivery", again)
	}
	drainJobs(t, app)
	if f.count(phone.path) != 1 || f.count(laptop.path) != 1 || f.total() != 2 {
		t.Fatalf("sends %v", f.sent)
	}
	if status, _ := pushDelivery(t, app, pushed); status != models.DeliverySent {
		t.Fatalf("delivery %q", status)
	}
	if status, _ := pushDelivery(t, app, quiet); status != "" {
		t.Fatalf("a kind that doesn't push has a push delivery %q", status)
	}
}

// A real cancellation, through the handler that writes it, reaches a
// registrant's phone: notify.Send callers need no change to push.
func TestCancellingADatePushesToRegistrants(t *testing.T) {
	app, f := pushTestApp(t)
	leader := newTestUser(t, app, "leader", "password123456", models.RoleMember)
	g := newTestGroup(t, app, leader, "Supper club", models.GroupActive)
	_, occ := seedEvent(t, app, g, leader.ID, time.Now().Add(5*time.Hour).Truncate(time.Minute))
	app.DB.Model(&models.Event{}).Where("id = ?", occ.EventID).Update("location", "14 Elm Street")
	pat := pushMember(t, app, "pat")
	phone := newPushDevice(t, app, pat, "phone")
	if err := app.DB.Create(&models.Registration{OccurrenceID: occ.ID, UserID: &pat.ID, Status: models.RegGoing}).Error; err != nil {
		t.Fatal(err)
	}
	var ids []uint
	err := app.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		ids, err = notifyOccurrencesCancelled(tx, leader.ID, []uint{occ.ID})
		return err
	})
	if err != nil || len(ids) != 1 {
		t.Fatalf("cancel notices %v %v", ids, err)
	}
	app.deliverNotificationEmails(ids)
	drainJobs(t, app)
	if f.count(phone.path) != 1 {
		t.Fatalf("sends %v", f.sent)
	}
	// Today's date: urgent and short-lived (research 2.4).
	h := f.headers[0]
	if h.Get("TTL") != "21600" || h.Get("Urgency") != "high" {
		t.Fatalf("headers %v", h)
	}
	var n models.Notification
	app.DB.First(&n, ids[0])
	// The bell has the place; the lock screen doesn't.
	if n.PushTitle != "Cancelled: Supper club" || n.PushBody == "" || strings.Contains(n.PushBody, "Elm") || !strings.Contains(n.Body, "Elm") {
		t.Fatalf("push text %q %q (bell %q)", n.PushTitle, n.PushBody, n.Body)
	}
}

// TTL and urgency follow the kind (research 2.4): a change to a date in
// the next 24 hours is high for 6 hours, a later one normal for a day, and
// an admin task low for 3 days.
func TestPushOptionsFollowTheKind(t *testing.T) {
	app := testApp(t)
	leader := newTestUser(t, app, "leader", "password123456", models.RoleMember)
	g := newTestGroup(t, app, leader, "Supper club", models.GroupActive)
	_, soon := seedEvent(t, app, g, leader.ID, time.Now().Add(23*time.Hour))
	_, later := seedEvent(t, app, g, leader.ID, time.Now().Add(72*time.Hour))
	for _, c := range []struct {
		kind    string
		subject *uint
		ttl     time.Duration
		urgency push.Urgency
	}{
		{notify.OccurrenceCancelled, &soon.ID, 6 * time.Hour, push.UrgencyHigh},
		{notify.EventChanged, &soon.ID, 6 * time.Hour, push.UrgencyHigh},
		{notify.OccurrenceCancelled, &later.ID, 24 * time.Hour, push.UrgencyNormal},
		{notify.RegistrationPromoted, &soon.ID, 24 * time.Hour, push.UrgencyNormal},
		{notify.GroupApproved, nil, 24 * time.Hour, push.UrgencyNormal},
		{notify.GroupPending, nil, 72 * time.Hour, push.UrgencyLow},
		{notify.AccountRequested, nil, 72 * time.Hour, push.UrgencyLow},
	} {
		k, _ := notify.Lookup(c.kind)
		n := &models.Notification{ID: 42, Kind: c.kind, SubjectType: "occurrence", SubjectID: c.subject}
		soon, err := app.startsSoon(context.Background(), k, n, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		got := pushOptions(k, n, soon)
		if got.TTL != c.ttl || got.Urgency != c.urgency || got.Topic != "n42" {
			t.Errorf("%s: %+v", c.kind, got)
		}
	}
}

func TestPushBodyIsDeclarativeWebPush(t *testing.T) {
	app := testApp(t)
	app.Config.SiteURL = "https://church.example/"
	n := &models.Notification{ID: 7, Title: "Youth was approved", Link: "/groups/1"}
	b, err := app.pushBody(n, push.Notification{Title: n.Title, Body: "Tonight", Navigate: "https://elsewhere.example/"})
	want := `{"web_push":8030,"notification":{"title":"Youth was approved","body":"Tonight","navigate":"https://church.example/groups/1","tag":"n7"}}`
	if err != nil || string(b) != want {
		t.Fatalf("payload %s (%v)", b, err)
	}
	if _, err := app.pushBody(n, push.Notification{Title: strings.Repeat("a", push.MaxPlaintext)}); !errors.Is(err, push.ErrTooLarge) {
		t.Fatalf("over the cap: %v", err)
	}
}

// TestPushLogsKeepSecretsOut: sends that fail every way leave no endpoint,
// key, auth secret or token in the log, the jobs table or the delivery rows.
func TestPushLogsKeepSecretsOut(t *testing.T) {
	var logs bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(prev) })

	app, f := pushTestApp(t)
	u := pushMember(t, app, "pat")
	var devices []testDevice
	for i, code := range []int{500, 429, 403, 404, 413, 0} {
		d := newPushDevice(t, app, u, fmt.Sprintf("device%d", i))
		if code == 0 {
			f.err[d.path] = true
		} else {
			f.set(d.path, code)
		}
		devices = append(devices, d)
	}
	n := sendNotice(t, app, notify.GroupApproved, u)
	runPushJobs(t, app, n, devices...)
	// Through to the last attempt, so dead jobs keep their final error.
	for range pushDeliverAttempts {
		app.DB.Model(&models.Job{}).Where("status = ?", models.JobFailed).Update("run_at", time.Now().Add(-time.Second))
		drainJobs(t, app)
	}
	var stored []string
	var js []models.Job
	app.DB.Find(&js)
	for _, j := range js {
		stored = append(stored, j.LastError, j.Payload)
	}
	var ds []models.NotificationDelivery
	app.DB.Find(&ds)
	for _, d := range ds {
		stored = append(stored, d.Reason)
	}
	var as []models.AuditLog
	app.DB.Find(&as)
	for _, a := range as {
		stored = append(stored, a.MetaJSON)
	}
	haystack := logs.String() + "\n" + strings.Join(stored, "\n")
	if !strings.Contains(haystack, "push") {
		t.Fatalf("nothing was logged or stored; the test isn't looking at anything:\n%s", haystack)
	}
	var secrets []string
	for _, d := range devices {
		secrets = append(secrets, d.Endpoint, strings.TrimPrefix(d.path, "/fcm/send/"), d.P256dh, d.Auth)
	}
	for _, h := range f.headers {
		token, _, _ := strings.Cut(strings.TrimPrefix(h.Get("Authorization"), "vapid t="), ",")
		// The token, and its claims and signature on their own.
		secrets = append(append(secrets, token), strings.Split(token, ".")[1:]...)
	}
	for _, s := range secrets {
		if s != "" && strings.Contains(haystack, s) {
			t.Errorf("a secret reached the logs or the database: %q", s)
		}
	}
	if !strings.Contains(logs.String(), "device gone for user") {
		t.Errorf("expected the gone log line:\n%s", logs.String())
	}
	if t.Failed() {
		t.Logf("logs and stored text:\n%s", haystack)
	}
}

func TestPushDevicesArePruned(t *testing.T) {
	app := testApp(t)
	u := newTestUser(t, app, "pat", "password123456", models.RoleMember)
	now := time.Now()
	keep, recent := newPushDevice(t, app, u, "keep"), newPushDevice(t, app, u, "recent")
	failing, expired := newPushDevice(t, app, u, "failing"), newPushDevice(t, app, u, "expired")
	app.DB.Model(&models.PushSubscription{}).Where("id = ?", recent.ID).Update("failed_at", now.Add(-29*24*time.Hour))
	app.DB.Model(&models.PushSubscription{}).Where("id = ?", failing.ID).Update("failed_at", now.Add(-31*24*time.Hour))
	app.DB.Model(&models.PushSubscription{}).Where("id = ?", expired.ID).Update("expires_at", now.Add(-time.Minute))
	app.DB.Model(&models.PushSubscription{}).Where("id = ?", keep.ID).Update("expires_at", now.Add(time.Hour))
	n, err := prunePushDevices(app.DB, now)
	if err != nil || n != 2 || !deviceExists(app, keep.ID) || !deviceExists(app, recent.ID) || deviceExists(app, failing.ID) || deviceExists(app, expired.ID) {
		t.Fatalf("pruned %d (%v)", n, err)
	}
	// It runs as part of the daily notifications.cleanup.
	gone := newPushDevice(t, app, u, "gone")
	app.DB.Model(&models.PushSubscription{}).Where("id = ?", gone.ID).Update("failed_at", now.Add(-40*24*time.Hour))
	for _, k := range app.dailyKinds() {
		if k.Name == "notifications.cleanup" {
			if err := k.Run(context.Background(), nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	if deviceExists(app, gone.ID) {
		t.Fatal("the daily cleanup kept a device failing for 40 days")
	}
}

func TestPushDeliverIsABackgroundKind(t *testing.T) {
	app := testApp(t)
	if _, ok := app.jobKinds()[kindPushDeliver]; !ok {
		t.Fatal("push.deliver isn't registered with the runner")
	}
}

// quietClock is a push test's fake time (DEC-126): quiet hours on, the
// clock at 11 PM in Denver on a night in the real future, so a job that
// waits for 8 AM is never due by the database's clock until a test says
// so (makeDue).
type quietClock struct{ at time.Time }

var denverZone = func() *time.Location {
	loc, err := time.LoadLocation("America/Denver")
	if err != nil {
		panic(err)
	}
	return loc
}()

func quietHours(app *App) *quietClock {
	c := &quietClock{at: time.Date(2030, 1, 15, 23, 0, 0, 0, denverZone)}
	w := push.DefaultWindow
	app.pushWindow = &w
	app.pushClock = func() time.Time { return c.at }
	return c
}

func (c *quietClock) set(h, m int, day int) { c.at = time.Date(2030, 1, day, h, m, 0, 0, denverZone) }

// makeDue moves every waiting push job's run_at into the database's past.
func makeDue(app *App) {
	app.DB.Model(&models.Job{}).Where("kind = ? AND status = ?", kindPushDeliver, models.JobQueued).Update("run_at", time.Now().Add(-time.Second))
}

func denverMember(t *testing.T, app *App, name string) *models.User {
	t.Helper()
	u := pushMember(t, app, name)
	if err := app.DB.Model(u).Update("timezone", "America/Denver").Error; err != nil {
		t.Fatal(err)
	}
	return u
}

func pushJobFor(t *testing.T, app *App, notificationID uint, device testDevice) models.Job {
	t.Helper()
	var j models.Job
	if err := app.DB.Where("unique_key = ?", fmt.Sprintf("push:n:%d:d:%d", notificationID, device.ID)).First(&j).Error; err != nil {
		t.Fatal(err)
	}
	return j
}

// An optional push at 11 PM in the recipient's zone waits, without using
// an attempt, and sends at 8 AM; running it again in the night still
// waits, and it never sends twice. A person with no zone of their own
// waits for the church's 8 AM.
func TestOptionalPushWaitsForTheMorning(t *testing.T) {
	app, f := pushTestApp(t)
	clock := quietHours(app)
	app.DB.Create(&models.Setting{Key: settingTimezone, Value: "America/Denver"})
	pat := denverMember(t, app, "pat")
	lee := pushMember(t, app, "lee") // no zone of their own: the church's
	phone, leePhone := newPushDevice(t, app, pat, "phone"), newPushDevice(t, app, lee, "lee-phone")
	n := sendNotice(t, app, notify.GroupApproved, pat)
	m := sendNotice(t, app, notify.GroupApproved, lee)
	runPushJobs(t, app, n, phone)
	runPushJobs(t, app, m, leePhone)
	morning := time.Date(2030, 1, 16, 8, 0, 0, 0, denverZone)
	for _, c := range []struct {
		id  uint
		dev testDevice
	}{{n, phone}, {m, leePhone}} {
		j := pushJobFor(t, app, c.id, c.dev)
		if j.Status != models.JobQueued || !j.RunAt.Equal(morning) || j.Attempts != 0 || j.LastError != "" {
			t.Fatalf("waiting job %+v, want queued for %s", j, morning)
		}
		if status, _ := pushDelivery(t, app, c.id); status != "" {
			t.Fatalf("a waiting push has a delivery row %q", status)
		}
	}
	// Due again at 11:30 PM (a clock or a lease, say): it waits again.
	clock.set(23, 30, 15)
	makeDue(app)
	drainJobs(t, app)
	if f.total() != 0 {
		t.Fatalf("sent in the night: %v", f.sent)
	}
	if j := pushJobFor(t, app, n, phone); !j.RunAt.Equal(morning) || j.Status != models.JobQueued {
		t.Fatalf("after a second run in the night: %+v", j)
	}
	clock.set(8, 0, 16)
	makeDue(app)
	drainJobs(t, app)
	if f.count(phone.path) != 1 || f.count(leePhone.path) != 1 {
		t.Fatalf("sends at 8 AM %v", f.sent)
	}
	if status, reason := pushDelivery(t, app, n); status != models.DeliverySent || reason != "" {
		t.Fatalf("delivery %q %q", status, reason)
	}
	// Queuing and running it again sends nothing more.
	runPushJobs(t, app, n, phone)
	makeDue(app)
	drainJobs(t, app)
	if f.count(phone.path) != 1 {
		t.Fatalf("phone got it %d times", f.count(phone.path))
	}
}

// Quiet hours are the recipient's own: 11 PM in Denver is mid-afternoon
// in Tokyo.
func TestQuietHoursAreTheRecipients(t *testing.T) {
	app, f := pushTestApp(t)
	quietHours(app)
	app.DB.Create(&models.Setting{Key: settingTimezone, Value: "America/Denver"})
	kenji := pushMember(t, app, "kenji")
	app.DB.Model(kenji).Update("timezone", "Asia/Tokyo")
	phone := newPushDevice(t, app, kenji, "phone")
	n := sendNotice(t, app, notify.GroupApproved, kenji)
	runPushJobs(t, app, n, phone)
	if f.count(phone.path) != 1 {
		t.Fatalf("sends %v", f.sent)
	}
}

// A required kind, and a change to a date starting within 24 hours, go at
// 11 PM; a change to a date three days away waits for 8 AM.
func TestUrgentPushesSkipQuietHours(t *testing.T) {
	app, f := pushTestApp(t)
	clock := quietHours(app)
	pat := denverMember(t, app, "pat")
	phone := newPushDevice(t, app, pat, "phone")

	required := sendNotice(t, app, notify.AccountTurned18, pat)
	runPushJobs(t, app, required, phone)
	if status, reason := pushDelivery(t, app, required); status != models.DeliverySent || reason != pushSentRequired || f.count(phone.path) != 1 {
		t.Fatalf("required: %q %q after %d sends", status, reason, f.count(phone.path))
	}

	leader := newTestUser(t, app, "leader", "password123456", models.RoleMember)
	g := newTestGroup(t, app, leader, "Supper club", models.GroupActive)
	_, tonight := seedEvent(t, app, g, leader.ID, clock.at.Add(3*time.Hour))
	_, later := seedEvent(t, app, g, leader.ID, clock.at.Add(72*time.Hour))
	change := func(occ models.EventOccurrence) uint {
		ids, err := notify.Send(app.DB, notify.Msg{Kind: notify.EventChanged, To: []uint{pat.ID}, SubjectType: "occurrence", SubjectID: occ.ID,
			Link: "/events/1", Vars: notify.Vars{Event: "Supper club", Change: "time"}})
		if err != nil || len(ids) != 1 {
			t.Fatalf("notify: %v %v", ids, err)
		}
		return ids[0]
	}
	soon := change(tonight)
	runPushJobs(t, app, soon, phone)
	if status, reason := pushDelivery(t, app, soon); status != models.DeliverySent || reason != pushSentSoon || f.count(phone.path) != 2 {
		t.Fatalf("3 hours away: %q %q after %d sends", status, reason, f.count(phone.path))
	}
	if h := f.headers[len(f.headers)-1]; h.Get("Urgency") != "high" {
		t.Fatalf("3 hours away is urgent: %v", h)
	}
	waits := change(later)
	runPushJobs(t, app, waits, phone)
	if f.count(phone.path) != 2 {
		t.Fatalf("3 days away sent in the night: %v", f.sent)
	}
	if j := pushJobFor(t, app, waits, phone); j.Status != models.JobQueued || !j.RunAt.Equal(time.Date(2030, 1, 16, 8, 0, 0, 0, denverZone)) {
		t.Fatalf("3 days away: %+v", j)
	}
	// Cancelled and restored dates are event changes too; a "you're in"
	// isn't (it waits).
	for kind, exempt := range map[string]bool{notify.OccurrenceCancelled: true, notify.OccurrenceRestored: true, notify.RegistrationPromoted: false} {
		k, _ := notify.Lookup(kind)
		got, err := app.startsSoon(context.Background(), k, &models.Notification{Kind: kind, SubjectType: "occurrence", SubjectID: &tonight.ID}, clock.at)
		if err != nil || got != exempt {
			t.Errorf("%s 3 hours away: soon %v (%v), want %v", kind, got, err, exempt)
		}
	}
}

// A push that waited the night rechecks everything at 8 AM: push turned
// off, a device gone, an item read, archived or expired since sends
// nothing, and the delivery row says why (a device that's gone leaves the
// row alone, as before quiet hours).
func TestWaitingPushRechecksInTheMorning(t *testing.T) {
	app, f := pushTestApp(t)
	clock := quietHours(app)
	off, gone, reader := denverMember(t, app, "off"), denverMember(t, app, "gone"), denverMember(t, app, "reader")
	offPhone, gonePhone, readerPhone := newPushDevice(t, app, off, "off-phone"), newPushDevice(t, app, gone, "gone-phone"), newPushDevice(t, app, reader, "reader-phone")
	nOff := sendNotice(t, app, notify.GroupApproved, off)
	nGone := sendNotice(t, app, notify.GroupApproved, gone)
	nRead := sendNotice(t, app, notify.GroupApproved, reader)
	nArchived := sendNotice(t, app, notify.GroupApproved, reader)
	nExpired := sendNotice(t, app, notify.GroupApproved, reader)
	runPushJobs(t, app, nOff, offPhone)
	runPushJobs(t, app, nGone, gonePhone)
	for _, id := range []uint{nRead, nArchived, nExpired} {
		runPushJobs(t, app, id, readerPhone)
	}
	if f.total() != 0 {
		t.Fatalf("sent in the night: %v", f.sent)
	}
	// Overnight.
	pushConsentRow(t, app, off, "all", models.ConsentOptedOut)
	app.DB.Delete(&models.PushSubscription{}, gonePhone.ID)
	app.DB.Model(&models.Notification{}).Where("id = ?", nRead).Update("read_at", clock.at)
	app.DB.Model(&models.Notification{}).Where("id = ?", nArchived).Update("archived_at", clock.at)
	app.DB.Model(&models.Notification{}).Where("id = ?", nExpired).Update("expires_at", clock.at.Add(time.Hour))

	clock.set(8, 0, 16)
	makeDue(app)
	drainJobs(t, app)
	if f.total() != 0 {
		t.Fatalf("sends %v", f.sent)
	}
	for id, want := range map[uint]string{nOff: pushWhyNotOn, nRead: pushWhyRead, nArchived: pushWhyStale, nExpired: pushWhyStale} {
		if status, reason := pushDelivery(t, app, id); status != models.DeliverySuppressed || reason != want {
			t.Errorf("notification %d: %q %q, want suppressed %q", id, status, reason, want)
		}
	}
	if status, _ := pushDelivery(t, app, nGone); status != "" {
		t.Errorf("a device gone overnight: %q", status)
	}
	var notDone int64
	app.DB.Model(&models.Job{}).Where("kind = ? AND status <> ?", kindPushDeliver, models.JobDone).Count(&notDone)
	if notDone != 0 {
		t.Fatalf("%d push jobs not done", notDone)
	}
}

// At most 20 optional pushes an hour: one notification on two devices
// counts once, the 21st is suppressed "hourly limit" with its bell row
// untouched, required kinds and urgent event changes still go and aren't
// counted, and an hour later pushes flow again.
func TestPushHourlyLimit(t *testing.T) {
	app, f := pushTestApp(t)
	clock := quietHours(app)
	clock.set(10, 0, 16)
	pat := denverMember(t, app, "pat")
	phone, laptop := newPushDevice(t, app, pat, "phone"), newPushDevice(t, app, pat, "laptop")
	required := sendNotice(t, app, notify.AccountTurned18, pat)
	runPushJobs(t, app, required, phone, laptop)
	for i := 1; i <= pushHourlyLimit; i++ {
		clock.at = clock.at.Add(time.Minute)
		n := sendNotice(t, app, notify.GroupApproved, pat)
		runPushJobs(t, app, n, phone, laptop)
		if status, _ := pushDelivery(t, app, n); status != models.DeliverySent {
			t.Fatalf("push %d: %q", i, status)
		}
	}
	if f.count(phone.path) != pushHourlyLimit+1 || f.count(laptop.path) != pushHourlyLimit+1 {
		t.Fatalf("both devices get the first 20 and the required one: %v", f.sent)
	}
	over := sendNotice(t, app, notify.GroupApproved, pat)
	runPushJobs(t, app, over, phone, laptop)
	if status, reason := pushDelivery(t, app, over); status != models.DeliverySuppressed || reason != pushWhyHourly {
		t.Fatalf("21st: %q %q", status, reason)
	}
	if f.count(phone.path) != pushHourlyLimit+1 || f.count(laptop.path) != pushHourlyLimit+1 {
		t.Fatalf("the 21st was sent: %v", f.sent)
	}
	var bell models.Notification
	if err := app.DB.First(&bell, over).Error; err != nil || bell.ReadAt != nil || bell.ArchivedAt != nil {
		t.Fatalf("the bell row: %+v (%v)", bell, err)
	}
	// Not limited: a required kind, and a change to a date tonight.
	again := sendNotice(t, app, notify.AccountTurned18, pat)
	runPushJobs(t, app, again, phone)
	leader := newTestUser(t, app, "leader", "password123456", models.RoleMember)
	g := newTestGroup(t, app, leader, "Supper club", models.GroupActive)
	_, tonight := seedEvent(t, app, g, leader.ID, clock.at.Add(3*time.Hour))
	ids, err := notify.Send(app.DB, notify.Msg{Kind: notify.OccurrenceCancelled, To: []uint{pat.ID}, SubjectType: "occurrence", SubjectID: tonight.ID,
		Link: "/events/1", Vars: notify.Vars{Event: "Supper club"}})
	if err != nil || len(ids) != 1 {
		t.Fatalf("notify: %v %v", ids, err)
	}
	runPushJobs(t, app, ids[0], phone)
	for _, id := range []uint{again, ids[0]} {
		if status, _ := pushDelivery(t, app, id); status != models.DeliverySent {
			t.Fatalf("exempt push %d: %q", id, status)
		}
	}
	// An hour after the first optional push, one slot is free again.
	clock.at = clock.at.Add(41 * time.Minute)
	later := sendNotice(t, app, notify.GroupApproved, pat)
	runPushJobs(t, app, later, phone)
	if status, _ := pushDelivery(t, app, later); status != models.DeliverySent {
		t.Fatalf("an hour later: %q", status)
	}
}
