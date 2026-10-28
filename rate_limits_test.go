package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"fold/models"
)

func TestRateLimitIP(t *testing.T) {
	for _, tt := range []struct{ ip, want string }{
		{"192.0.2.1", "192.0.2.1"},
		{"::ffff:192.0.2.1", "192.0.2.1"},
		{"2001:db8:1:2::1", "2001:db8:1:2::/64"},
		{"2001:0db8:0001:0002:ffff:ffff:ffff:ffff", "2001:db8:1:2::/64"},
		{"2001:db8:1:3::1", "2001:db8:1:3::/64"},
		{"fe80::1%eth0", "fe80::/64"},
		{"invalid", "invalid"},
	} {
		if got := rateLimitIP(tt.ip); got != tt.want {
			t.Errorf("%s: got %s want %s", tt.ip, got, tt.want)
		}
	}
}

func TestAccountRequestIPv6RotationSharesBudget(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	for i := 1; i <= accountRequestIPLimit+1; i++ {
		anon := anonClient(t, app, fmt.Sprintf("2001:db8:1:2::%x", i))
		email := fmt.Sprintf("ipv6-%d@example.com", i)
		w := anon.do("POST", "/api/v1/account-requests", signupBody("Person", email, "adult", ""), true)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), signupThanks) {
			t.Fatalf("submit %d: %d %s", i, w.Code, w.Body.String())
		}
		want := int64(1)
		if i > accountRequestIPLimit {
			want = 0
		}
		if n := requestCount(t, app, email); n != want {
			t.Fatalf("submit %d stored %d rows, want %d", i, n, want)
		}
	}
}

func TestPublicSignupSiteCaps(t *testing.T) {
	for _, door := range []string{"acctreq", "guestreg"} {
		t.Run(door, func(t *testing.T) {
			app := testApp(t)
			newTestUser(t, app, "admin", "pw", models.RoleAdmin)
			admin := loginAPI(t, app, "admin", "pw")
			gid := createGroupAs(t, admin, "Site caps", models.VisibilityPublic)
			event := scheduleGroupEvent(t, admin, gid, "Picnic")
			for range publicSignupSiteLimit - 1 {
				if _, err := app.throttleHit(context.Background(), door+":site", time.Hour); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 2; i++ {
				email := fmt.Sprintf("sitecap-%d@example.com", i)
				anon := anonClient(t, app, fmt.Sprintf("192.0.2.%d", i+1))
				path := "/api/v1/account-requests"
				body := signupBody("Person", email, "adult", "")
				if door == "guestreg" {
					path = fmt.Sprintf("/api/v1/events/%d/guest-registrations", event.ID)
					body = guestBody("Person", email, true)
				}
				w := anon.do("POST", path, body, true)
				if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), signupThanks) {
					t.Fatalf("submit %d: %d %s", i, w.Code, w.Body.String())
				}
				want := int64(1 - i)
				if mailsTo(app, email) != want {
					t.Fatalf("submit %d sent %d mails, want %d", i, mailsTo(app, email), want)
				}
				stored := requestCount(t, app, email)
				if door == "guestreg" {
					stored = guestSignupCount(t, app, email)
				}
				if stored != want {
					t.Fatalf("submit %d stored %d rows, want %d", i, stored, want)
				}
			}
		})
	}
}

func TestGuestHoneypotStillChargesAbuseBudgets(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "pw")
	gid := createGroupAs(t, admin, "Guest honeypot", models.VisibilityPublic)
	event := scheduleGroupEvent(t, admin, gid, "Picnic")
	anon := anonClient(t, app, "2001:db8::1")
	body := guestBody("Bot", "bot@example.com", true)
	body["website"] = "spam"
	guestSubmit(t, anon, event.ID, body, http.StatusOK)
	for _, key := range []string{"guestreg:ip:2001:db8::/64", "guestreg:site"} {
		var row models.LoginThrottle
		if err := app.DB.First(&row, "key = ?", key).Error; err != nil || row.Failures != 1 {
			t.Fatalf("honeypot must charge %s: %+v %v", key, row, err)
		}
	}
	if guestSignupCount(t, app, "bot@example.com") != 0 || mailsTo(app, "bot@example.com") != 0 {
		t.Fatal("guest honeypot stored or mailed")
	}
}

func TestHoneypotAccountRequestsNeverSendMail(t *testing.T) {
	app := testApp(t)
	admin := newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	existing := newTestUser(t, app, "existing", "pw", models.RoleMember)
	anon := anonClient(t, app, "192.0.2.1")
	for _, email := range []string{existing.Email, "new@example.com"} {
		body := signupBody("Bot", email, "adult", "")
		body["website"] = "spam"
		w := anon.do("POST", "/api/v1/account-requests", body, true)
		if w.Code != http.StatusOK {
			t.Fatalf("honeypot: %d %s", w.Code, w.Body.String())
		}
	}
	if requestCount(t, app, existing.Email) != 0 {
		t.Fatal("honeypot request stored for existing account")
	}
	req := pendingRequest(t, app, "new@example.com")
	if !parseRequestFlags(req.Flags).Honeypot || adminNotification(t, app, admin.ID, req.ID).ID == 0 {
		t.Fatal("honeypot request must remain flagged in the admin queue")
	}
	var n int64
	app.DB.Model(&models.MailOutbox{}).Count(&n)
	if n != 0 {
		t.Fatalf("honeypot sent %d emails", n)
	}
}
