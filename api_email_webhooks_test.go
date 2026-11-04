package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"fold/internal/jobs"
	"fold/internal/mail"
	"fold/internal/providers"
	"fold/models"
)

func postWebhook(t *testing.T, app *App, user, pass, trace string, body []byte) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/hooks/postmark/email", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if user != "" || pass != "" {
		req.SetBasicAuth(user, pass)
	}
	if trace != "" {
		req.Header.Set("X-PM-Webhook-Trace-Id", trace)
	}
	w := httptest.NewRecorder()
	app.setupRouter().ServeHTTP(w, req)
	return w.Code
}

// postmarkSample is a sample payload from internal/mail/testdata with its
// message id and address replaced.
func postmarkSample(t *testing.T, file, messageID, address string) []byte {
	t.Helper()
	b, err := os.ReadFile("internal/mail/testdata/" + file)
	if err != nil {
		t.Fatal(err)
	}
	s := strings.ReplaceAll(string(b), "883953f4-6105-42a2-a16a-77a8eac79483", messageID)
	for _, a := range []string{"John@example.com", "john@example.com"} {
		s = strings.ReplaceAll(s, a, address)
	}
	return []byte(s)
}

func runEmailEvents(t *testing.T, app *App) {
	t.Helper()
	r := jobs.NewRunner(app.DB, app.eventKinds()...)
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

// The webhook needs the credentials from its URL: wrong, missing or none
// configured answers without changing anything.
func TestPostmarkWebhookAuthentication(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin) // past /setup
	body := postmarkSample(t, "postmark_bounce.json", "m-1", "lee@example.com")
	if code := postWebhook(t, app, webhookUser, "anything", "", body); code != http.StatusNotFound {
		t.Fatalf("no secret configured: %d", code)
	}
	app.Config.PostmarkWebhookSecret = "s3cret-webhook"
	for _, c := range [][2]string{{"", ""}, {webhookUser, "wrong"}, {"someone", "s3cret-webhook"}} {
		if code := postWebhook(t, app, c[0], c[1], "", body); code != http.StatusUnauthorized {
			t.Fatalf("%v: %d", c, code)
		}
	}
	var n int64
	app.DB.Model(&models.Job{}).Where("kind = ?", kindEmailEvent).Count(&n)
	if n != 0 {
		t.Fatalf("a refused webhook queued %d jobs", n)
	}
	if code := postWebhook(t, app, webhookUser, "s3cret-webhook", "", []byte("{not json")); code != http.StatusBadRequest {
		t.Fatalf("malformed: %d", code)
	}
	if code := postWebhook(t, app, webhookUser, "s3cret-webhook", "", postmarkSample(t, "postmark_open.json", "m-1", "lee@example.com")); code != http.StatusOK {
		t.Fatalf("an unused record type is fine: %d", code)
	}
	app.DB.Model(&models.Job{}).Count(&n)
	if n != 0 {
		t.Fatalf("an ignored record queued %d jobs", n)
	}
}

// A hard bounce marks the notification's delivery bounced and stops mail to
// the address; a redelivered report is handled once; a delivery report
// moves a sent row to delivered.
func TestPostmarkReportsUpdateDeliveriesAndSuppress(t *testing.T) {
	app := testApp(t)
	app.Config.PostmarkWebhookSecret = "s3cret-webhook"
	newTestUser(t, app, "root", "password1234", models.RoleAdmin)
	newTestUser(t, app, "sam", "password1234", models.RoleMember)
	sam := loginAPI(t, app, "sam", "password1234")
	createGroupAs(t, sam, "Breakfast", models.VisibilityPublic) // emails root a task
	var d models.NotificationDelivery
	app.DB.Where("status = ?", models.DeliverySent).First(&d)
	if d.ProviderMessageID == nil {
		t.Fatal("no provider id recorded")
	}
	id := *d.ProviderMessageID

	delivered := postmarkSample(t, "postmark_delivery.json", id, "root@example.com")
	if code := postWebhook(t, app, webhookUser, "s3cret-webhook", "trace-d", delivered); code != http.StatusOK {
		t.Fatalf("delivery: %d", code)
	}
	runEmailEvents(t, app)
	app.DB.First(&d, d.ID)
	if d.Status != models.DeliveryDelivered {
		t.Fatalf("after delivery: %+v", d)
	}

	bounce := postmarkSample(t, "postmark_bounce.json", id, "Root@Example.com")
	for i := 0; i < 2; i++ { // Postmark redelivers with the same trace id
		if code := postWebhook(t, app, webhookUser, "s3cret-webhook", "trace-b", bounce); code != http.StatusOK {
			t.Fatalf("bounce: %d", code)
		}
	}
	var queued int64
	app.DB.Model(&models.Job{}).Where("kind = ?", kindEmailEvent).Count(&queued)
	if queued != 2 {
		t.Fatalf("want one job per report, have %d", queued)
	}
	runEmailEvents(t, app)
	app.DB.First(&d, d.ID)
	var sup []models.Suppression
	app.DB.Find(&sup)
	if d.Status != models.DeliveryBounced || d.Reason != "hard bounce" || len(sup) != 1 ||
		sup[0].Address != "root@example.com" || sup[0].Reason != models.SuppressHardBounce || sup[0].Detail != "HardBounce" {
		t.Fatalf("after bounce: %+v %+v", d, sup)
	}

	// The next email to that address is suppressed, not sent.
	before := len(outboxTo(t, app, "root@example.com"))
	createGroupAs(t, sam, "Lunch", models.VisibilityPublic)
	if after := len(outboxTo(t, app, "root@example.com")); after != before {
		t.Fatal("mail went to a hard-bounced address")
	}
	var sup2 models.NotificationDelivery
	app.DB.Order("id DESC").First(&sup2)
	if sup2.Status != models.DeliverySuppressed || sup2.Reason != "email to this address bounces" {
		t.Fatalf("suppressed delivery: %+v", sup2)
	}
}

// A soft bounce is only recorded; a spam complaint suppresses optional mail.
func TestPostmarkSoftBounceAndComplaint(t *testing.T) {
	app := testApp(t)
	app.Config.PostmarkWebhookSecret = "s3cret-webhook"
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	postWebhook(t, app, webhookUser, "s3cret-webhook", "", postmarkSample(t, "postmark_soft_bounce.json", "m-soft", "x@example.com"))
	postWebhook(t, app, webhookUser, "s3cret-webhook", "", postmarkSample(t, "postmark_spam_complaint.json", "m-spam", "pat@example.com"))
	runEmailEvents(t, app)
	var sup []models.Suppression
	app.DB.Find(&sup)
	if len(sup) != 1 || sup[0].Address != "pat@example.com" || sup[0].Reason != models.SuppressComplaint {
		t.Fatalf("suppressions: %+v", sup)
	}
	var failed int64
	app.DB.Model(&models.Job{}).Where("status <> ?", models.JobDone).Count(&failed)
	if failed != 0 {
		t.Fatal("reports for unknown messages should still be handled")
	}
}

// A report that beats its delivery row is retried for a couple of minutes,
// then taken to be about mail Fold has no row for.
func TestEmailReportBeforeItsDeliveryRow(t *testing.T) {
	app := testApp(t)
	e := providers.Event{Provider: "postmark", Kind: providers.EventBounced, Address: "x@example.com", MessageID: "not-yet",
		Permanent: true, At: time.Now()}
	if err := app.applyEmailEvent(context.Background(), e); !errors.Is(err, errDeliveryNotYet) {
		t.Fatalf("a fresh report with no row: %v", err)
	}
	var n int64
	app.DB.Model(&models.Suppression{}).Count(&n)
	if n != 0 {
		t.Fatal("a retried report must leave nothing behind")
	}
	e.At = time.Now().Add(-5 * time.Minute)
	if err := app.applyEmailEvent(context.Background(), e); err != nil {
		t.Fatalf("an old report: %v", err)
	}
	app.DB.Model(&models.Suppression{}).Count(&n)
	if n != 1 {
		t.Fatal("an old hard bounce still suppresses the address")
	}
}

// A hard bounce and a complaint each store the address with its hash; a
// redelivery adds nothing; a legacy NULL-hash open row gets only its hash
// filled; a cleared row is left alone and a new open row sits beside it; a
// soft bounce or a delivery stores nothing (R4-S3).
func TestEmailReportsStoreTheAddressHash(t *testing.T) {
	app := testApp(t)
	ctx := context.Background()
	old := time.Now().Add(-5 * time.Minute) // past reportGrace: no delivery row is waited for
	report := func(kind providers.EventKind, permanent bool, address string) error {
		return app.applyEmailEvent(ctx, providers.Event{Provider: "postmark", Kind: kind, Address: address, MessageID: "m-hash",
			Permanent: permanent, Detail: "from the report", At: old})
	}
	count := func() int64 {
		var n int64
		app.DB.Model(&models.Suppression{}).Count(&n)
		return n
	}

	if err := report(providers.EventBounced, false, "soft@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := report(providers.EventDelivered, true, "delivered@example.com"); err != nil {
		t.Fatal(err)
	}
	if count() != 0 {
		t.Fatal("a soft bounce or a delivery stored a suppression")
	}

	for i := 0; i < 2; i++ { // the second is a redelivery
		if err := report(providers.EventBounced, true, "bounce@example.com"); err != nil {
			t.Fatal(err)
		}
		if err := report(providers.EventComplained, true, "complaint@example.com"); err != nil {
			t.Fatal(err)
		}
	}
	var rows []models.Suppression
	app.DB.Order("id").Find(&rows)
	if len(rows) != 2 {
		t.Fatalf("rows after redelivery: %+v", rows)
	}
	for _, r := range rows {
		if r.AddressHash == nil || *r.AddressHash != mail.StoredAddressHash(r.Address) {
			t.Errorf("%s: hash %v", r.Address, r.AddressHash)
		}
	}

	// A legacy open row (no hash) is filled and changed nowhere else.
	created := time.Now().Add(-72 * time.Hour).UTC().Truncate(time.Second)
	if err := app.DB.Create(&models.Suppression{Channel: "email", Address: "legacy@example.com", Reason: models.SuppressHardBounce,
		Provider: "manual-import", Detail: "kept", CreatedAt: created}).Error; err != nil {
		t.Fatal(err)
	}
	if err := report(providers.EventBounced, true, "Legacy@Example.com"); err != nil {
		t.Fatal(err)
	}
	var legacy models.Suppression
	app.DB.Where("address = ?", "legacy@example.com").First(&legacy)
	if legacy.AddressHash == nil || *legacy.AddressHash != mail.StoredAddressHash("legacy@example.com") {
		t.Fatalf("legacy hash not filled: %+v", legacy)
	}
	if legacy.Provider != "manual-import" || legacy.Detail != "kept" || !legacy.CreatedAt.Equal(created) || legacy.ClearedAt != nil || legacy.Reason != models.SuppressHardBounce {
		t.Fatalf("a hash fill changed another column: %+v", legacy)
	}
	if count() != 3 {
		t.Fatalf("rows = %d, want 3", count())
	}

	// A cleared row stays as it is and a report opens a new row beside it.
	cleared := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	if err := app.DB.Create(&models.Suppression{Channel: "email", Address: "again@example.com", Reason: models.SuppressHardBounce,
		Provider: "postmark", CreatedAt: created, ClearedAt: &cleared}).Error; err != nil {
		t.Fatal(err)
	}
	if err := report(providers.EventBounced, true, "again@example.com"); err != nil {
		t.Fatal(err)
	}
	var again []models.Suppression
	app.DB.Where("address = ?", "again@example.com").Order("id").Find(&again)
	if len(again) != 2 || again[0].AddressHash != nil || again[0].ClearedAt == nil || !again[0].ClearedAt.Equal(cleared) ||
		again[1].ClearedAt != nil || again[1].AddressHash == nil || *again[1].AddressHash != mail.StoredAddressHash("again@example.com") {
		t.Fatalf("cleared row handling: %+v", again)
	}
}
