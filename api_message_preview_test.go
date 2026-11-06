package main

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"fold/internal/audit"
	"fold/internal/mail"
	"fold/models"
)

// Merge fields must be ones the composer offers, and event fields need a
// message to one event's people (M3.4b).
func TestMessageMergeFieldsAreChecked(t *testing.T) {
	f := newComposeFixture(t)
	group := map[string]any{"type": "group", "groupId": f.group.ID}
	date := map[string]any{"type": "occurrence", "occurrenceId": f.groupOcc.ID}
	for name, tc := range map[string]struct {
		subject, body string
		aud           map[string]any
		code          string
	}{
		"first name to a group":     {"Hi {{first_name}}", "<p>Hello</p>", group, ""},
		"an unknown field":          {"Hi", "<p>{{favorite_color}}</p>", group, "message_merge_unknown"},
		"an event field to a group": {"Hi", "<p>{{event_title}}</p>", group, "message_merge_event"},
		"event fields to one date":  {"{{event_title}}", `<p><a href="{{manage_link}}">Change your RSVP</a> {{event_time}}</p>`, date, ""},
	} {
		w := f.leader.do("POST", "/api/v1/messages", map[string]any{"subject": tc.subject, "bodyHtml": tc.body, "audience": tc.aud}, true)
		if tc.code == "" && w.Code != http.StatusCreated || tc.code != "" && (w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), tc.code)) {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
}

func (f composeFixture) draft(t *testing.T, c *apiClient, subject, body string, aud map[string]any) string {
	t.Helper()
	var out struct {
		Message messageJSON `json:"message"`
	}
	w := c.do("POST", "/api/v1/messages", map[string]any{"subject": subject, "bodyHtml": body, "audience": aud}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("draft: %d %s", w.Code, w.Body.String())
	}
	decodeJSON(t, w, &out)
	return "/api/v1/messages/" + itoa(out.Message.ID)
}

// The preview is the email the author would get: merge fields filled with
// their own values, the layout, and the footer with their settings link;
// the HTML is served under a policy that lets nothing in it run.
func TestMessagePreview(t *testing.T) {
	f := newComposeFixture(t)
	path := f.draft(t, f.leader, "{{event_title}} news", `<p>Hi {{first_name}}, see you {{event_time}}.</p><p><a class="fold-button" href="{{event_link}}">Open it</a></p>`,
		map[string]any{"type": "occurrence", "occurrenceId": f.groupOcc.ID})
	var out struct {
		Preview renderedMessage `json:"preview"`
	}
	decodeJSON(t, f.leader.do("GET", path+"/preview", nil, false), &out)
	p := out.Preview
	if p.Subject != "Book night news" || !strings.Contains(p.Text, "Hi "+firstName(f.leaderUser.PublicName())+", see you ") ||
		!strings.Contains(p.Text, "/events/"+itoa(f.groupEvent.ID)) || !strings.Contains(p.Text, "/email-settings/") || strings.Contains(p.Text, "{{") {
		t.Fatalf("preview: %+v", p)
	}
	w := f.leader.do("GET", path+"/preview.html", nil, false)
	html := w.Body.String()
	csp := w.Header().Get("Content-Security-Policy")
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") || !strings.Contains(csp, "sandbox") || strings.Contains(csp, "script-src") {
		t.Fatalf("preview.html: %d %v", w.Code, w.Header())
	}
	if !strings.Contains(html, "Book night news</title>") || !strings.Contains(html, `/events/`+itoa(f.groupEvent.ID)+`" rel=`) || !strings.Contains(html, `style="display:inline-block;`) ||
		!strings.Contains(html, "visit your email settings") || strings.Contains(html, "{{") {
		t.Fatalf("preview html:\n%s", html)
	}
	if w := f.host.do("GET", path+"/preview", nil, false); w.Code != http.StatusNotFound {
		t.Fatalf("someone else's preview: %d", w.Code)
	}
	// Stepping down ends the preview too: it would show the event's details.
	f.app.DB.Model(&models.Membership{}).Where("group_id = ? AND user_id = ?", f.group.ID, f.leaderUser.ID).Update("role", models.MembershipMember)
	for _, p := range []string{"/preview", "/preview.html"} {
		if w := f.leader.do("GET", path+p, nil, false); w.Code != http.StatusForbidden {
			t.Fatalf("former leader %s: %d", p, w.Code)
		}
	}
}

// "Send a test to me" sends the rendered email to the viewer, marked as a
// test, ten an hour.
func TestMessageTestSend(t *testing.T) {
	f := newComposeFixture(t)
	path := f.draft(t, f.leader, "Next week", "<p>Hi {{first_name}}</p>", map[string]any{"type": "group", "groupId": f.group.ID})
	if w := f.leader.do("POST", path+"/test", nil, true); w.Code != http.StatusOK {
		t.Fatalf("test: %d %s", w.Code, w.Body.String())
	}
	got, err := devOutbox(t, f.app).LatestTo(f.leaderUser.Email)
	if err != nil {
		t.Fatal(err)
	}
	if got.Subject != "[Test] Next week" || !strings.Contains(got.Body, "Hi "+firstName(f.leaderUser.PublicName())) || !strings.Contains(got.HTML, "Hi "+firstName(f.leaderUser.PublicName())) {
		t.Fatalf("sent: %+v", got)
	}
	if auditCount(t, f.app, audit.MessageTestSent) != 1 {
		t.Fatal("a test send is audited")
	}
	for range testSendsPerHour - 1 {
		f.app.DB.Create(&models.AuditLog{ActorID: &f.leaderUser.ID, Action: audit.MessageTestSent, TargetType: "message"})
	}
	if w := f.leader.do("POST", path+"/test", nil, true); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "message_test_limit") {
		t.Fatalf("eleventh test: %d %s", w.Code, w.Body.String())
	}
	if w := f.host.do("POST", path+"/test", nil, true); w.Code != http.StatusNotFound {
		t.Fatalf("someone else's message: %d", w.Code)
	}
	// A suppressed address hears so instead of a silent drop.
	f.app.DB.Create(&models.Suppression{Channel: "email", Address: strings.ToLower(f.hostUser.Email), Reason: models.SuppressHardBounce})
	hp := f.draft(t, f.host, "Picnic", "<p>Bring a dish</p>", map[string]any{"type": "event_series", "eventId": f.churchEvent.ID})
	if w := f.host.do("POST", hp+"/test", nil, true); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "message_test_email") {
		t.Fatalf("suppressed: %d %s", w.Code, w.Body.String())
	}
}

// The test send is optional broadcast mail to the viewer, so any open row
// refuses it, hashed or legacy, in either order; a cleared row does not. The
// refused viewer hears so (message_test_email), nothing reaches the outbox,
// and the try still counts toward the hourly limit.
func TestMessageTestSendHashedAndLegacySuppressions(t *testing.T) {
	for _, tc := range suppressionCases() {
		t.Run(tc.name, func(t *testing.T) {
			f := newComposeFixture(t)
			suppressSpecs(t, f.app, strings.ToLower(f.leaderUser.Email), tc.specs...)
			path := f.draft(t, f.leader, "Next week", "<p>Hi</p>", map[string]any{"type": "group", "groupId": f.group.ID})
			before := mailCount(t, f.app, f.leaderUser.Email)
			w := f.leader.do("POST", path+"/test", nil, true)
			after := mailCount(t, f.app, f.leaderUser.Email)
			if tc.optional != "" {
				if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "message_test_email") || after != before {
					t.Fatalf("refused test send: %d %s, outbox %d to %d", w.Code, w.Body.String(), before, after)
				}
			} else if w.Code != http.StatusOK || after != before+1 {
				t.Fatalf("test send: %d %s, outbox %d to %d", w.Code, w.Body.String(), before, after)
			}
			if auditCount(t, f.app, audit.MessageTestSent) != 1 {
				t.Fatal("a test send is audited whether or not it is delivered")
			}
		})
	}
}

func TestTestSendHonorsTheEffectiveChoice(t *testing.T) {
	for _, source := range []string{"address", "account", "other kind"} {
		t.Run(source, func(t *testing.T) {
			f := newComposeFixture(t)
			path := f.draft(t, f.leader, "Choice", "<p>Hi</p>", groupAudience(f))
			switch source {
			case "address":
				seedEmailAddressOff(t, f.app, f.leaderUser.Email, "messages")
			case "other kind":
				seedEmailAddressOff(t, f.app, f.leaderUser.Email, "groups")
			case "account":
				if w := f.leader.do("PUT", "/api/v1/me/notification-settings", map[string]any{"category": "messages", "off": true}, true); w.Code != http.StatusOK {
					t.Fatal(w.Body.String())
				}
			}
			records := dumpRows(t, f.app.DB, "email_address_optouts", "scope", "true")
			consents := dumpRows(t, f.app.DB, "consents", "id", "true")
			before := mailCount(t, f.app, f.leaderUser.Email)
			attempts := 10
			if source == "other kind" {
				attempts = 1
			}
			for range attempts {
				w := f.leader.do("POST", path+"/test", nil, true)
				if source == "other kind" {
					if w.Code != http.StatusOK {
						t.Fatalf("unrelated kind: %d %s", w.Code, w.Body.String())
					}
				} else if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "message_test_opted_out") {
					t.Fatalf("refusal: %d %s", w.Code, w.Body.String())
				}
			}
			if records != dumpRows(t, f.app.DB, "email_address_optouts", "scope", "true") || consents != dumpRows(t, f.app.DB, "consents", "id", "true") {
				t.Fatal("test send changed choice evidence")
			}
			if source != "other kind" {
				if mailCount(t, f.app, f.leaderUser.Email) != before || auditCount(t, f.app, audit.MessageTestSent) != 0 {
					t.Fatal("refused test sent or consumed a try")
				}
				if source == "address" {
					if err := f.app.DB.Where("address_hash = ?", mail.AddressHash(f.leaderUser.Email)).Delete(&models.EmailAddressOptOut{}).Error; err != nil {
						t.Fatal(err)
					}
				} else {
					if w := f.leader.do("PUT", "/api/v1/me/notification-settings", map[string]any{"category": "messages", "off": false}, true); w.Code != http.StatusOK {
						t.Fatal(w.Body.String())
					}
				}
				for range testSendsPerHour {
					if w := f.leader.do("POST", path+"/test", nil, true); w.Code != http.StatusOK {
						t.Fatalf("refusal used hourly budget: %d %s", w.Code, w.Body.String())
					}
				}
				if auditCount(t, f.app, audit.MessageTestSent) != testSendsPerHour {
					t.Fatal("successful tries not audited")
				}
			}
		})
	}
}

func TestTestSendRejectsAddressChangeAfterAuthentication(t *testing.T) {
	f := newComposeFixture(t)
	f.admin.holdNotices, f.leader.holdNotices = true, true
	path := f.draft(t, f.leader, "Stale session", "<p>Hi</p>", groupAudience(f))
	reached, release := erasureTestPause(t, f.app.DB, erasureTestPoint{Processor: "row", Step: "gorm:row", SQLContains: "SELECT id, email, status, session_version, logout_version, deleted_at FROM users", VarAt: map[int]any{0: f.leaderUser.ID}})
	result := bgDo(f.leader, "POST", path+"/test", nil)
	awaitDBSignal(t, reached)
	// Authentication has completed; the real admin change commits before the lock.
	if w := f.admin.do("PUT", "/api/v1/admin/users/"+itoa(f.leaderUser.ID), map[string]any{"email": "new-author@example.com"}, true); w.Code != http.StatusOK {
		t.Fatalf("email change: %d %s", w.Code, w.Body.String())
	}
	release()
	got := awaitResult(t, result)
	if got.code != http.StatusUnauthorized || mailCount(t, f.app, f.leaderUser.Email) != 0 || auditCount(t, f.app, audit.MessageTestSent) != 0 {
		t.Fatalf("stale test: %+v", got)
	}
}

func TestTestSendInFlightWindow(t *testing.T) {
	t.Run("opt-out waits for the locked check", func(t *testing.T) {
		f := newComposeFixture(t)
		f.leader.holdNotices = true
		path := f.draft(t, f.leader, "Locked choice", "<p>Hi</p>", groupAudience(f))
		choiceClient := loginAPI(t, f.app, "leader", "password1234")
		choiceClient.holdNotices = true
		var senderPID atomic.Int64
		reached, release := erasureTestPause(t, f.app.DB, erasureTestPoint{Processor: "row", Step: "gorm:row", SQLContains: "SELECT c.user_id, '' AS address_hash", VarAt: map[int]any{0: f.leaderUser.ID}, PID: &senderPID})
		send := bgDo(f.leader, "POST", path+"/test", nil)
		awaitDBSignal(t, reached)
		choice := bgDo(choiceClient, "PUT", "/api/v1/me/notification-settings", map[string]any{"category": "messages", "off": true})
		waiter := waitForLockedUsersQuery(t, f.app, "%users%FOR NO KEY UPDATE%")
		waitDBBlocked(t, f.app.DB, waiter, int(senderPID.Load()))
		release()
		if r := awaitResult(t, send); r.code != http.StatusOK {
			t.Fatalf("test send: %+v", r)
		}
		if r := awaitResult(t, choice); r.code != http.StatusOK {
			t.Fatalf("opt-out: %+v", r)
		}
		if mailCount(t, f.app, f.leaderUser.Email) != 1 || auditCount(t, f.app, audit.MessageTestSent) != 1 {
			t.Fatal("locked send was not delivered once")
		}
		if w := f.leader.do("POST", path+"/test", nil, true); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "message_test_opted_out") {
			t.Fatalf("next request: %d %s", w.Code, w.Body.String())
		}
	})
	t.Run("a change after commit does not recall the provider call", func(t *testing.T) {
		f := newComposeFixture(t)
		f.leader.holdNotices = true
		path := f.draft(t, f.leader, "In flight", "<p>Hi</p>", groupAudience(f))
		choiceClient := loginAPI(t, f.app, "leader", "password1234")
		choiceClient.holdNotices = true
		sender := newBlockingChoiceSender(t, f.app.Mailer)
		f.app.Mailer = sender
		send := bgDo(f.leader, "POST", path+"/test", nil)
		awaitDBSignal(t, sender.entered)
		// A successful response before release proves the transaction's user lock
		// no longer blocks the opt-out. The address fixture commits separately too.
		choice := bgDo(choiceClient, "PUT", "/api/v1/me/notification-settings", map[string]any{"category": "messages", "off": true})
		if r := awaitResult(t, choice); r.code != http.StatusOK {
			t.Fatalf("post-commit opt-out: %+v", r)
		}
		seedEmailAddressOff(t, f.app, f.leaderUser.Email, "messages")
		sender.resume()
		if r := awaitResult(t, send); r.code != http.StatusOK {
			t.Fatalf("in-flight send: %+v", r)
		}
		if mailCount(t, f.app, f.leaderUser.Email) != 1 || auditCount(t, f.app, audit.MessageTestSent) != 1 {
			t.Fatal("in-flight send did not complete once")
		}
		if w := f.leader.do("POST", path+"/test", nil, true); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "message_test_opted_out") {
			t.Fatalf("next request: %d %s", w.Code, w.Body.String())
		}
		if len(sender.sent()) != 1 || auditCount(t, f.app, audit.MessageTestSent) != 1 {
			t.Fatal("next request sent or wrote another audit row")
		}
	})
}
