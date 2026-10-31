package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"fold/internal/jobs"
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

func outboxTo(t *testing.T, app *App, to string) []models.MailOutbox {
	t.Helper()
	var rows []models.MailOutbox
	app.DB.Where("to_address = ?", to).Find(&rows)
	return rows
}
