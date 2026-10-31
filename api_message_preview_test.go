package main

import (
	"net/http"
	"strings"
	"testing"

	"fold/internal/audit"
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
		!strings.Contains(html, "Change which emails you get") || strings.Contains(html, "{{") {
		t.Fatalf("preview html:\n%s", html)
	}
	if w := f.host.do("GET", path+"/preview", nil, false); w.Code != http.StatusNotFound {
		t.Fatalf("someone else's preview: %d", w.Code)
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
