package main

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"fold/internal/content"
	"fold/models"
)

// A recipient reads the announcement they got, as their email said it,
// at /messages/:id/read (the page its push opens), and nothing else about
// it: its history and its own page stay its author's. Someone it wasn't
// for, a guardian with only a copy, and anyone before it goes out hear
// that it isn't there (404, as the message's other routes answer).
func TestRecipientReadsTheAnnouncement(t *testing.T) {
	f := newComposeFixture(t)
	parent := newTestUser(t, f.app, "parent", "password1234", models.RoleMember)
	f.app.DB.Create(&models.GuardianLink{GuardianUserID: parent.ID, TeenUserID: &f.teen.ID, Status: models.GuardianConfirmed})
	f.app.DB.Create(&models.Setting{Key: settingGuardianCopies, Value: "true"})
	teen, guardian := loginAPI(t, f.app, "teen", "password1234"), loginAPI(t, f.app, "parent", "password1234")
	opted := loginAPI(t, f.app, "opted", "password1234")

	// A draft has no recipients yet: not even the people it will reach.
	var draft struct {
		Message messageJSON `json:"message"`
	}
	decodeJSON(t, f.leader.do("POST", "/api/v1/messages", map[string]any{"subject": "Draft", "bodyHtml": "<p>Hi</p>", "audience": groupAudience(f)}, true), &draft)
	if w := teen.do("GET", "/api/v1/messages/"+itoa(draft.Message.ID)+"/read", nil, false); w.Code != http.StatusNotFound {
		t.Fatalf("a draft: %d", w.Code)
	}

	id := f.scheduleNow(t, f.leader, "Snow day for {{first_name}}", `<p>Hi {{first_name}}, <strong>no meeting</strong>.</p>`, groupAudience(f))
	runMessageJobs(t, f.app)
	w := teen.do("GET", "/api/v1/messages/"+itoa(id)+"/read", nil, false)
	if w.Code != http.StatusOK {
		t.Fatalf("recipient: %d %s", w.Code, w.Body.String())
	}
	var raw struct {
		Message map[string]json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for k := range raw.Message {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"bodyHtml", "from", "id", "sentAt", "subject"}) {
		t.Fatalf("a recipient gets more than the message: %v", keys)
	}
	var out struct {
		Message messageReadJSON `json:"message"`
	}
	decodeJSON(t, teen.do("GET", "/api/v1/messages/"+itoa(id)+"/read", nil, false), &out)
	first := firstName(f.teen.PublicName())
	if out.Message.Subject != "Snow day for "+first || out.Message.BodyHTML != "<p>Hi "+first+", <strong>no meeting</strong>.</p>" ||
		out.Message.From != f.leaderUser.PublicName()+" via "+f.app.siteName() || out.Message.SentAt == nil {
		t.Fatalf("read: %+v", out.Message)
	}
	// Skipped people were on the list too (they have a row): they may read it.
	if w := opted.do("GET", "/api/v1/messages/"+itoa(id)+"/read", nil, false); w.Code != http.StatusOK {
		t.Fatalf("a skipped recipient: %d", w.Code)
	}
	// Not for them, or only a copy: not there. Neither is the history.
	for name, c := range map[string]*apiClient{"member": f.member, "guardian": guardian, "host": f.host} {
		if w := c.do("GET", "/api/v1/messages/"+itoa(id)+"/read", nil, false); w.Code != http.StatusNotFound {
			t.Fatalf("%s: %d", name, w.Code)
		}
	}
	for _, path := range []string{"", "/recipients"} {
		if w := teen.do("GET", "/api/v1/messages/"+itoa(id)+path, nil, false); w.Code != http.StatusNotFound {
			t.Fatalf("a recipient opened %q: %d", path, w.Code)
		}
	}
	// The author and an admin read it too.
	for name, c := range map[string]*apiClient{"author": f.leader, "admin": f.admin} {
		if w := c.do("GET", "/api/v1/messages/"+itoa(id)+"/read", nil, false); w.Code != http.StatusOK {
			t.Fatalf("%s: %d", name, w.Code)
		}
	}
}

// A link to {{event_link}} or {{manage_link}} with no value for the reader
// (its event is gone) reads as its text, never an empty href.
func TestReadPageUnlinksMissingLinkFields(t *testing.T) {
	stored := content.SanitizeHTML(`<p>Hi. <a href="{{event_link}}">See the event</a> or <a href="{{ manage_link }}">your sign-up</a>, <a href="https://example.org">this</a>.</p>`)
	got := content.SanitizeHTML(content.Merge(unlinkMissingFields(stored, map[string]string{}), map[string]string{}, true))
	if strings.Contains(got, `href=""`) || !strings.Contains(got, "See the event") || !strings.Contains(got, "your sign-up") || !strings.Contains(got, `href="https://example.org"`) {
		t.Fatalf("no values: %s", got)
	}
	vals := map[string]string{content.FieldEventLink: "https://fold.example/events/3", content.FieldManageLink: "https://fold.example/events/3/dates/2026-11-05"}
	got = content.SanitizeHTML(content.Merge(unlinkMissingFields(stored, vals), vals, true))
	if !strings.Contains(got, `href="https://fold.example/events/3"`) || !strings.Contains(got, `href="https://fold.example/events/3/dates/2026-11-05"`) {
		t.Fatalf("with values: %s", got)
	}
}
