package main

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"fold/internal/audit"
	"fold/internal/mail"
	"fold/models"
)

func (f composeFixture) history(t *testing.T, c *apiClient, id uint) (messageRecipientsJSON, string) {
	t.Helper()
	w := c.do("GET", "/api/v1/messages/"+itoa(id)+"/recipients", nil, false)
	if w.Code != http.StatusOK {
		t.Fatalf("recipients: %d %s", w.Code, w.Body.String())
	}
	var out messageRecipientsJSON
	decodeJSON(t, w, &out)
	return out, w.Body.String()
}

func historyRow(h messageRecipientsJSON, name string) historyRecipientJSON {
	for _, r := range h.Recipients {
		if r.Name == name && r.CopyOf == "" {
			return r
		}
	}
	return historyRecipientJSON{}
}

// sendWithOneFailure sends to the fixture's group with the leader's own
// address refused (550), so one row fails and the message is partly sent.
func (f composeFixture) sendWithOneFailure(t *testing.T) uint {
	t.Helper()
	next := f.app.Mailer
	f.app.Mailer = pickySender{next: next, bad: f.leaderUser.Email, code: 550}
	id := f.scheduleNow(t, f.leader, "Partly", "<p>Hi</p>", map[string]any{"type": "group", "groupId": f.group.ID})
	runMessageJobs(t, f.app)
	f.app.Mailer = next
	return id
}

// The history follows the preview's privacy rule: counts by reason for
// everyone, each person's status in words for the author, each person's
// reason only for an admin, never an address. Guardian copies are a count
// for a leader and names only for an admin (DEC-132); a provider's own
// error reaches only an admin (DEC-52).
func TestMessageHistoryPrivacy(t *testing.T) {
	f := newComposeFixture(t)
	parent := newTestUser(t, f.app, "parent", "password1234", models.RoleMember)
	f.app.DB.Model(parent).Update("display_name", "Gail Guardian")
	parent.DisplayName = "Gail Guardian"
	f.app.DB.Create(&models.GuardianLink{GuardianUserID: parent.ID, TeenUserID: &f.teen.ID, Status: models.GuardianConfirmed})
	f.app.DB.Create(&models.Setting{Key: settingGuardianCopies, Value: "true"})
	id := f.sendWithOneFailure(t)
	const raw = "550" // the refusing server's own words: "mail: rcpt: 550 no"

	lead, body := f.history(t, f.leader, id)
	if strings.Contains(body, "@") || strings.Contains(body, parent.PublicName()) || strings.Contains(body, raw) {
		t.Fatalf("an address, a guardian's name or a provider error in the leader's history: %s", body)
	}
	c := lead.Counts
	if c.Sent != 1 || c.Failed != 1 || c.Skipped != 2 || c.Queued != 0 || c.Teens != 1 || c.GuardianCopies != 1 || c.GuardianCopiesMissed != 0 ||
		lead.SkippedBy[skipOptedOut] != 1 || lead.SkippedBy[skipNotWorking] != 1 || len(lead.Recipients) != 4 || lead.More || !lead.CanRetry {
		t.Fatalf("leader counts: %+v", lead)
	}
	if r := historyRow(lead, f.teen.PublicName()); r.Status != historySent || !r.Teen || r.Reason != "" {
		t.Fatalf("teen: %+v", r)
	}
	for _, u := range []*models.User{f.opted, f.bouncing, f.leaderUser} {
		if r := historyRow(lead, u.PublicName()); r.Status != historyMissed || r.Reason != "" {
			t.Fatalf("a leader sees %s's reason: %+v", u.Name, r)
		}
	}
	for _, r := range lead.Recipients {
		if r.CopyOf != "" {
			t.Fatalf("a leader sees a guardian copy: %+v", r)
		}
	}

	adm, body := f.history(t, f.admin, id)
	if strings.Contains(body, "@") {
		t.Fatalf("an address in the admin's history: %s", body)
	}
	if historyRow(adm, f.opted.PublicName()).Reason != skipOptedOut || historyRow(adm, f.bouncing.PublicName()).Reason != skipNotWorking ||
		!strings.Contains(historyRow(adm, f.leaderUser.PublicName()).Reason, raw) || len(adm.Recipients) != 5 {
		t.Fatalf("admin reasons: %+v", adm.Recipients)
	}
	copied := false
	for _, r := range adm.Recipients {
		copied = copied || (r.Name == parent.PublicName() && r.CopyOf == f.teen.PublicName() && !r.Teen)
	}
	if !copied {
		t.Fatalf("an admin sees no copy mark: %+v", adm.Recipients)
	}
	if w := f.member.do("GET", "/api/v1/messages/"+itoa(id)+"/recipients", nil, false); w.Code != http.StatusNotFound {
		t.Fatalf("member: %d", w.Code)
	}
	// The list says "Partly sent" from one count per message, and a leader
	// reads the provider's error in plain words; an admin reads it as is.
	var list struct {
		Messages []messageJSON `json:"messages"`
	}
	w := f.leader.do("GET", "/api/v1/messages", nil, false)
	decodeJSON(t, w, &list)
	if len(list.Messages) != 1 || list.Messages[0].FailedCount != 1 || list.Messages[0].Status != models.MessageSent ||
		list.Messages[0].Error != whyProvider || strings.Contains(w.Body.String(), raw) {
		t.Fatalf("list: %+v", list.Messages)
	}
	if b := f.leader.do("GET", "/api/v1/messages/"+itoa(id), nil, false).Body.String(); strings.Contains(b, raw) || !strings.Contains(b, "The email service didn") {
		t.Fatalf("leader's message: %s", b)
	}
	if b := f.admin.do("GET", "/api/v1/messages/"+itoa(id), nil, false).Body.String(); !strings.Contains(b, raw) {
		t.Fatalf("admin's message: %s", b)
	}
}

// Our own plain texts reach everyone as they are; anything else is a
// provider's words, which only an admin reads.
func TestMessageErrorWords(t *testing.T) {
	for _, own := range []string{"", whyAuthorGone, whyAuthorRefused, whyAudienceGone, whyNotPrepared, whyStopped, whyNoSender} {
		if messageErrorFor(own, false) != own {
			t.Errorf("%q changed for a leader", own)
		}
	}
	if messageErrorFor("postmark: 503 service unavailable", false) != whyProvider || messageErrorFor("postmark: 503", true) != "postmark: 503" {
		t.Error("provider text")
	}
}

// "Try again" (DEC-131) sends again to just the people it failed for, in
// new batches, and nobody it reached gets it twice; it is audited.
func TestMessageRetrySendsOnlyToThoseItFailedFor(t *testing.T) {
	f := newComposeFixture(t)
	id := f.sendWithOneFailure(t)
	outbox := func(email string) int64 {
		var n int64
		f.app.DB.Model(&models.MailOutbox{}).Where("lower(to_address) = lower(?)", email).Count(&n)
		return n
	}
	if outbox(f.teen.Email) != 1 || outbox(f.leaderUser.Email) != 0 {
		t.Fatalf("first send: teen %d, leader %d", outbox(f.teen.Email), outbox(f.leaderUser.Email))
	}
	w := f.leader.do("POST", "/api/v1/messages/"+itoa(id)+"/retry", nil, true)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"sending"`) {
		t.Fatalf("retry: %d %s", w.Code, w.Body.String())
	}
	if rows := recipientsOf(t, f.app, id); rows[f.leaderUser.Email].Status != models.RecipientQueued || rows[f.leaderUser.Email].Error != "" ||
		rows[f.teen.Email].Status != models.RecipientSent {
		t.Fatalf("rows after retry: %+v", rows)
	}
	runMessageJobs(t, f.app)
	var m models.Message
	f.app.DB.First(&m, id)
	if m.Status != models.MessageSent || m.Error != "" || outbox(f.leaderUser.Email) != 1 || outbox(f.teen.Email) != 1 {
		t.Fatalf("after retry: %+v, leader %d, teen %d", m, outbox(f.leaderUser.Email), outbox(f.teen.Email))
	}
	if auditCount(t, f.app, audit.MessageRetried) != 1 {
		t.Fatal("a retry is audited")
	}
	// Nothing is left to send.
	if w := f.leader.do("POST", "/api/v1/messages/"+itoa(id)+"/retry", nil, true); w.Code != http.StatusConflict ||
		!strings.Contains(w.Body.String(), "message_nothing_to_retry") {
		t.Fatalf("again: %d %s", w.Code, w.Body.String())
	}
	if h, _ := f.history(t, f.leader, id); h.CanRetry {
		t.Fatal("canRetry with nothing to retry")
	}
	if w := f.member.do("POST", "/api/v1/messages/"+itoa(id)+"/retry", nil, true); w.Code != http.StatusNotFound {
		t.Fatalf("member: %d", w.Code)
	}
}

// A retry checks again that the author may write to these people, even
// when an admin presses it, and changes nothing when not.
func TestMessageRetryRefusedWhenTheAuthorLostRights(t *testing.T) {
	f := newComposeFixture(t)
	id := f.sendWithOneFailure(t)
	f.app.DB.Model(&models.Membership{}).Where("user_id = ? AND group_id = ?", f.leaderUser.ID, f.group.ID).Update("role", models.MembershipMember)
	if h, _ := f.history(t, f.admin, id); h.CanRetry {
		t.Fatal("canRetry for an author who lost rights")
	}
	for _, c := range []*apiClient{f.leader, f.admin} {
		if w := c.do("POST", "/api/v1/messages/"+itoa(id)+"/retry", nil, true); w.Code != http.StatusForbidden ||
			!strings.Contains(w.Body.String(), "message_retry_refused") {
			t.Fatalf("refused: %d %s", w.Code, w.Body.String())
		}
	}
	if rows := recipientsOf(t, f.app, id); rows[f.leaderUser.Email].Status != models.RecipientFailed {
		t.Fatalf("a refused retry changed rows: %+v", rows)
	}
	if auditCount(t, f.app, audit.MessageRetried) != 0 {
		t.Fatal("a refused retry was audited")
	}
}

// A message that failed before anyone was chosen is prepared again; a
// draft or a refused message has nothing to retry.
func TestMessageRetryWhenItNeverGotReady(t *testing.T) {
	f := newComposeFixture(t)
	aud := `{"type":"group","groupId":` + itoa(f.group.ID) + `,"roles":["member","leader"]}`
	at := time.Now().Add(-2 * time.Hour)
	stuck := models.Message{AuthorID: &f.leaderUser.ID, Status: models.MessageFailed, Subject: "Again", BodyHTML: "<p>Hi</p>", BodyText: "Hi",
		Audience: aud, ScheduledAt: &at, Error: whyNotPrepared}
	refused := stuck
	refused.Error = whyAuthorRefused
	draft := stuck
	draft.Status, draft.Error = models.MessageDraft, ""
	for _, m := range []*models.Message{&stuck, &refused, &draft} {
		f.app.DB.Create(m)
	}
	for _, m := range []*models.Message{&refused, &draft} {
		if w := f.leader.do("POST", "/api/v1/messages/"+itoa(m.ID)+"/retry", nil, true); w.Code != http.StatusConflict {
			t.Fatalf("%s: %d %s", m.Status, w.Code, w.Body.String())
		}
	}
	if h, _ := f.history(t, f.leader, stuck.ID); !h.CanRetry {
		t.Fatal("no Try again for a message that never got ready")
	}
	if w := f.leader.do("POST", "/api/v1/messages/"+itoa(stuck.ID)+"/retry", nil, true); w.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", w.Code, w.Body.String())
	}
	runMessageJobs(t, f.app)
	f.app.DB.First(&stuck, stuck.ID)
	if stuck.Status != models.MessageSent || recipientsOf(t, f.app, stuck.ID)[f.teen.Email].Status != models.RecipientSent {
		t.Fatalf("after retry: %+v", stuck)
	}
}

// With no email service at all, a send fails at once in plain words
// instead of retrying for an hour.
func TestMessageFailsAtOnceWithNoEmailService(t *testing.T) {
	f := newComposeFixture(t)
	f.app.Mailer = mail.NoSender{}
	id := f.scheduleNow(t, f.leader, "Nowhere", "<p>Hi</p>", map[string]any{"type": "group", "groupId": f.group.ID})
	runMessageJobs(t, f.app)
	var m models.Message
	f.app.DB.First(&m, id)
	var attempts int
	f.app.DB.Model(&models.Job{}).Where("kind = ?", kindMessageBatch).Pluck("attempts", &attempts)
	if m.Status != models.MessageFailed || m.Error != whyNoSender || attempts != 1 {
		t.Fatalf("message %+v after %d attempts", m, attempts)
	}
}
