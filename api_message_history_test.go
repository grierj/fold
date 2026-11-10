package main

import (
	"context"
	"fmt"
	"go/ast"
	"go/token"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strconv"
	"strings"
	"testing"
	"time"

	"fold/internal/guardkit"

	"fold/internal/audit"
	"fold/internal/mail"
	"fold/internal/providers"
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
	// The space keeps timestamps from matching; the marker also catches
	// provider errors with the "mail: rcpt:" prefix stripped.
	const privateMarker = "550 "
	sender := pickySender{bad: f.leaderUser.Email, code: 550}
	_, providerErr := sender.SendEmail(context.Background(), mail.Message{To: f.leaderUser.Email})
	if providerErr == nil || !strings.Contains(providerErr.Error(), privateMarker) {
		t.Fatalf("expected a provider error containing %q: %v", privateMarker, providerErr)
	}
	raw := providerErr.Error()

	lead, body := f.history(t, f.leader, id)
	if strings.Contains(body, "@") || strings.Contains(body, parent.PublicName()) || strings.Contains(body, privateMarker) {
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
		list.Messages[0].Error != whyProvider || strings.Contains(w.Body.String(), privateMarker) {
		t.Fatalf("list: %+v", list.Messages)
	}
	if b := f.leader.do("GET", "/api/v1/messages/"+itoa(id), nil, false).Body.String(); strings.Contains(b, privateMarker) || !strings.Contains(b, "The email service didn") {
		t.Fatalf("leader's message: %s", b)
	}
	var detail struct {
		Message messageJSON `json:"message"`
	}
	decodeJSON(t, f.admin.do("GET", "/api/v1/messages/"+itoa(id), nil, false), &detail)
	if detail.Message.Error != raw {
		t.Fatalf("admin's message error: got %q, want %q", detail.Message.Error, raw)
	}
}

// Our own plain texts reach everyone as they are; anything else is a
// provider's words, which only an admin reads.
func TestMessageErrorWords(t *testing.T) {
	for _, own := range []string{"", whyAuthorGone, whyAuthorRefused, whyAudienceGone, whyNotPrepared, whyNotPreparedM35a, whyStopped, whyNoSender} {
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
	// A second press while it goes out says so.
	if w := f.admin.do("POST", "/api/v1/messages/"+itoa(id)+"/retry", nil, true); w.Code != http.StatusConflict ||
		!strings.Contains(w.Body.String(), "message_retry_sending") {
		t.Fatalf("second press: %d %s", w.Code, w.Body.String())
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

// A message that failed before anyone was chosen is prepared again, in
// today's words or M3.5a's; a draft, a refused message and a scheduled one
// that is due (never sent) have nothing to retry.
func TestMessageRetryWhenItNeverGotReady(t *testing.T) {
	f := newComposeFixture(t)
	aud := `{"type":"group","groupId":` + itoa(f.group.ID) + `,"roles":["member","leader"]}`
	at := time.Now().Add(-2 * time.Hour)
	stuck := models.Message{AuthorID: &f.leaderUser.ID, Status: models.MessageFailed, Subject: "Again", BodyHTML: "<p>Hi</p>", BodyText: "Hi",
		Audience: aud, ScheduledAt: &at, Error: whyNotPreparedM35a}
	fresh := stuck
	fresh.Error = whyNotPrepared
	refused := stuck
	refused.Error = whyAuthorRefused
	draft := stuck
	draft.Status, draft.Error = models.MessageDraft, ""
	due := draft
	due.Status = models.MessageScheduled
	for _, m := range []*models.Message{&stuck, &fresh, &refused, &draft, &due} {
		f.app.DB.Create(m)
	}
	for _, m := range []*models.Message{&refused, &draft, &due} {
		if w := f.leader.do("POST", "/api/v1/messages/"+itoa(m.ID)+"/retry", nil, true); w.Code != http.StatusConflict ||
			!strings.Contains(w.Body.String(), "message_nothing_to_retry") {
			t.Fatalf("%s: %d %s", m.Status, w.Code, w.Body.String())
		}
	}
	for _, m := range []*models.Message{&stuck, &fresh} {
		if h, _ := f.history(t, f.leader, m.ID); !h.CanRetry {
			t.Fatalf("no Try again for %q", m.Error)
		}
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

// Every failure text Fold writes (a why* string constant anywhere in the
// package) is listed in ownMessageErrors, so a copy change can't quietly
// turn one into "a provider's words" for leaders.
func TestOwnMessageErrorsListEveryWhy(t *testing.T) {
	consts := map[string]string{}
	parsed := guardkit.Production(t).Parse(0)
	for _, problem := range parsed.Problems {
		t.Error(problem)
	}
	// why* constants are a package main convention (the messaging failure texts).
	for _, pf := range parsed.Package("main") {
		ast.Inspect(pf.AST, func(n ast.Node) bool {
			spec, ok := n.(*ast.ValueSpec)
			if !ok {
				return true
			}
			for i, name := range spec.Names {
				if !strings.HasPrefix(name.Name, "why") || i >= len(spec.Values) {
					continue
				}
				if lit, ok := spec.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					v, _ := strconv.Unquote(lit.Value)
					consts[name.Name] = v
				}
			}
			return true
		})
	}
	// Eight today; fewer than six means the scan stopped finding them.
	if len(consts) < 6 {
		t.Fatalf("found only %d why* failure texts (%v); did they move or get renamed? Point this scan at them", len(consts), consts)
	}
	for name, v := range consts {
		if !ownMessageErrors[v] {
			t.Errorf("%s is not in ownMessageErrors", name)
		}
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

// neverPrepared inserts a failed leader message that never got its recipients,
// for the group, the way TestMessageRetryWhenItNeverGotReady does.
func (f composeFixture) neverPrepared(t *testing.T) models.Message {
	t.Helper()
	at := time.Now().Add(-2 * time.Hour)
	m := models.Message{AuthorID: &f.leaderUser.ID, Status: models.MessageFailed, Subject: "Again", BodyHTML: "<p>Hi</p>", BodyText: "Hi",
		Audience: `{"type":"group","groupId":` + itoa(f.group.ID) + `,"roles":["member","leader"]}`, ScheduledAt: &at, Error: whyNotPreparedM35a}
	if err := f.app.DB.Create(&m).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.app.DB.First(&m, m.ID).Error; err != nil {
		t.Fatal(err)
	}
	return m
}

// fillToday inserts n scheduled leader messages for the group, today, as rows
// the daily limit counts.
func (f composeFixture) fillToday(t *testing.T, n int) {
	t.Helper()
	now := time.Now()
	for range n {
		m := models.Message{AuthorID: &f.leaderUser.ID, Status: models.MessageScheduled, Subject: "Filler", ScheduledAt: &now,
			Audience: `{"type":"group","groupId":` + itoa(f.group.ID) + `}`}
		if err := f.app.DB.Create(&m).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func (f composeFixture) retry(c *apiClient, id uint) *httptest.ResponseRecorder {
	return c.do("POST", "/api/v1/messages/"+itoa(id)+"/retry", nil, true)
}

// A never-prepared message goes back into today's slots, so the limit judges
// its retry; an admin has no limit.
func TestMessageRetryDailyLimitNeverPrepared(t *testing.T) {
	f := newComposeFixture(t)
	m := f.neverPrepared(t)
	aud := map[string]any{"type": "group", "groupId": f.group.ID}
	for range dailyMessagesPerGroup {
		f.scheduleNow(t, f.leader, "Counted", "<p>Hi</p>", aud)
	}
	w := f.retry(f.leader, m.ID)
	if w.Code != http.StatusBadRequest || errorCodeOf(w) != "message_retry_daily_limit" || strings.Contains(w.Body.String(), "Schedule this one for tomorrow") {
		t.Fatalf("leader retry: %d %s", w.Code, w.Body.String())
	}
	var after models.Message
	f.app.DB.First(&after, m.ID)
	if after.Status != models.MessageFailed || after.Error != m.Error || after.ScheduledAt == nil || !after.ScheduledAt.Equal(*m.ScheduledAt) ||
		jobCountFor(t, f.app, kindMessageSend, m.ID) != 0 || auditCountFor(t, f.app, audit.MessageRetried, m.ID) != 0 {
		t.Fatalf("a refused retry changed the message: %+v", after)
	}
	if w := f.retry(f.admin, m.ID); w.Code != http.StatusOK {
		t.Fatalf("admin retry: %d %s", w.Code, w.Body.String())
	}
	f.app.DB.First(&after, m.ID)
	if after.Status != models.MessageScheduled || after.ScheduledAt == nil || time.Since(*after.ScheduledAt) > time.Minute ||
		jobCountFor(t, f.app, kindMessageSend, m.ID) != 1 {
		t.Fatalf("admin retry left: %+v", after)
	}
}

// A retry to the people a send failed for is refused only when the author's
// other messages already fill the day.
func TestMessageRetryDailyLimitFailedRows(t *testing.T) {
	f := newComposeFixture(t)
	id := f.sendWithOneFailure(t)
	aud := map[string]any{"type": "group", "groupId": f.group.ID}
	for range dailyMessagesPerGroup - 1 {
		f.scheduleNow(t, f.leader, "Counted", "<p>Hi</p>", aud)
	}
	if w := f.retry(f.leader, id); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"sending"`) {
		t.Fatalf("retry with room: %d %s", w.Code, w.Body.String())
	}
	if rows := recipientsOf(t, f.app, id); rows[f.leaderUser.Email].Status != models.RecipientQueued {
		t.Fatalf("rows: %+v", rows)
	}
	if auditCount(t, f.app, audit.MessageRetried) != 1 {
		t.Fatal("the retry wasn't audited")
	}

	g := newComposeFixture(t)
	id = g.sendWithOneFailure(t)
	g.fillToday(t, dailyMessagesPerGroup)
	batches := len(jobIDsOf(t, g.app, kindMessageBatch))
	w := g.retry(g.leader, id)
	if w.Code != http.StatusBadRequest || errorCodeOf(w) != "message_retry_daily_limit" {
		t.Fatalf("retry of a full day: %d %s", w.Code, w.Body.String())
	}
	var m models.Message
	g.app.DB.First(&m, id)
	if m.Status != models.MessageSent || recipientsOf(t, g.app, id)[g.leaderUser.Email].Status != models.RecipientFailed ||
		len(jobIDsOf(t, g.app, kindMessageBatch)) != batches || auditCount(t, g.app, audit.MessageRetried) != 0 {
		t.Fatalf("a refused retry changed things: %+v", m)
	}
}

// A retry is charged to the day it goes out on: a message first sent
// yesterday takes one of today's five slots when it is retried today.
func TestMessageRetryDailyLimitFailedRowsPriorDay(t *testing.T) {
	prior := func(f composeFixture, id uint) {
		t.Helper()
		if err := f.app.DB.Exec("UPDATE messages SET scheduled_at = scheduled_at - interval '24 hours' WHERE id = ?", id).Error; err != nil {
			t.Fatal(err)
		}
	}
	f := newComposeFixture(t)
	id := f.sendWithOneFailure(t)
	prior(f, id)
	var before models.Message
	f.app.DB.First(&before, id)
	aud := map[string]any{"type": "group", "groupId": f.group.ID}
	for range dailyMessagesPerGroup - 1 {
		f.scheduleNow(t, f.leader, "Counted", "<p>Hi</p>", aud)
	}
	if w := f.retry(f.leader, id); w.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", w.Code, w.Body.String())
	}
	var m models.Message
	f.app.DB.First(&m, id)
	if m.Status != models.MessageSending || recipientsOf(t, f.app, id)[f.leaderUser.Email].Status != models.RecipientQueued ||
		m.ScheduledAt == nil || time.Since(*m.ScheduledAt) > time.Minute || m.SentAt == nil || !m.SentAt.Equal(*before.SentAt) {
		t.Fatalf("after retry: %+v", m)
	}
	path := f.draft(t, f.leader, "One more", "<p>Hi</p>", aud)
	if w := f.leader.do("POST", path+"/schedule", map[string]any{}, true); w.Code != http.StatusBadRequest || errorCodeOf(w) != "message_daily_limit" {
		t.Fatalf("a further schedule: %d %s", w.Code, w.Body.String())
	}
	if got := f.countedOn(t, f.group.ID, time.Now()); got != int64(dailyMessagesPerGroup) {
		t.Fatalf("counted today = %d, including the retried one", got)
	}

	g := newComposeFixture(t)
	id = g.sendWithOneFailure(t)
	prior(g, id)
	g.app.DB.First(&before, id)
	g.fillToday(t, dailyMessagesPerGroup)
	batches := len(jobIDsOf(t, g.app, kindMessageBatch))
	if w := g.retry(g.leader, id); w.Code != http.StatusBadRequest || errorCodeOf(w) != "message_retry_daily_limit" {
		t.Fatalf("retry of a full day: %d %s", w.Code, w.Body.String())
	}
	g.app.DB.First(&m, id)
	if m.Status != models.MessageSent || !m.ScheduledAt.Equal(*before.ScheduledAt) || recipientsOf(t, g.app, id)[g.leaderUser.Email].Status != models.RecipientFailed ||
		len(jobIDsOf(t, g.app, kindMessageBatch)) != batches || auditCount(t, g.app, audit.MessageRetried) != 0 {
		t.Fatalf("a refused retry changed things: %+v", m)
	}
}

// A retry and a schedule by one author race for the last slot: one wins.
func TestMessageRetryAndScheduleSerialize(t *testing.T) {
	f := newComposeFixture(t)
	aud := map[string]any{"type": "group", "groupId": f.group.ID}
	for range dailyMessagesPerGroup - 1 {
		f.scheduleNow(t, f.leader, "Counted", "<p>Hi</p>", aud)
	}
	stuck := f.neverPrepared(t)
	path := f.draft(t, f.leader, "Draft", "<p>Hi</p>", aud)
	other := loginAPI(t, f.app, "leader", "password1234")
	retry := bgDo(f.leader, "POST", "/api/v1/messages/"+itoa(stuck.ID)+"/retry", nil)
	schedule := bgDo(other, "POST", path+"/schedule", map[string]any{})
	r, s := awaitResult(t, retry), awaitResult(t, schedule)
	retried, scheduled := r.code == http.StatusOK, s.code == http.StatusOK
	if retried == scheduled {
		t.Fatalf("retry %d %s, schedule %d %s", r.code, r.body, s.code, s.body)
	}
	if !retried && (r.code != http.StatusBadRequest || !strings.Contains(r.body, "message_retry_daily_limit")) ||
		!scheduled && (s.code != http.StatusBadRequest || !strings.Contains(s.body, "message_daily_limit")) {
		t.Fatalf("refusal: retry %d %s, schedule %d %s", r.code, r.body, s.code, s.body)
	}
	if got := f.countedOn(t, f.group.ID, time.Now()); got != int64(dailyMessagesPerGroup) {
		t.Fatalf("counted today = %d", got)
	}
	var lost models.Message
	if retried {
		f.app.DB.First(&lost, idOfPath(t, path))
		if lost.Status != models.MessageDraft || lost.ScheduledAt != nil {
			t.Fatalf("the losing schedule changed: %+v", lost)
		}
	} else {
		f.app.DB.First(&lost, stuck.ID)
		if lost.Status != models.MessageFailed || !lost.ScheduledAt.Equal(*stuck.ScheduledAt) || auditCountFor(t, f.app, audit.MessageRetried, stuck.ID) != 0 {
			t.Fatalf("the losing retry changed: %+v", lost)
		}
	}
}

// The 401 comes from lockActorIn inside the retry's transaction: the session
// was current when the middleware read it and ended while the press waited.
func TestMessageRetryStaleActorUnderLock(t *testing.T) {
	f := newComposeFixture(t)
	m := f.neverPrepared(t)
	hold := holdUserRow(t, f.app, f.leaderUser.ID)
	res := bgDo(loginAPI(t, f.app, "leader", "password1234"), "POST", "/api/v1/messages/"+itoa(m.ID)+"/retry", nil)
	waitForAuthorLock(t, f.app)
	if err := hold.Exec("UPDATE users SET session_version = session_version + 1 WHERE id = ?", f.leaderUser.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := hold.Commit().Error; err != nil {
		t.Fatal(err)
	}
	if r := awaitResult(t, res); r.code != http.StatusUnauthorized {
		t.Fatalf("retry: %d %s", r.code, r.body)
	}
	var after models.Message
	f.app.DB.First(&after, m.ID)
	if after.Status != models.MessageFailed || !after.UpdatedAt.Equal(m.UpdatedAt) || jobCountFor(t, f.app, kindMessageSend, m.ID) != 0 ||
		auditCount(t, f.app, audit.MessageRetried) != 0 {
		t.Fatalf("a stale session retried: %+v", after)
	}
}

// The press is judged on the snapshot its counts came from: a message that
// changed while it waited answers as if someone else had pressed first.
func TestMessageRetryRechecksItsSnapshot(t *testing.T) {
	f := newComposeFixture(t)
	m := f.neverPrepared(t)
	hold := holdUserRow(t, f.app, f.leaderUser.ID)
	res := bgDo(loginAPI(t, f.app, "leader", "password1234"), "POST", "/api/v1/messages/"+itoa(m.ID)+"/retry", nil)
	waitForAuthorLock(t, f.app)
	if err := f.app.DB.Exec("UPDATE messages SET status = ?, updated_at = updated_at + interval '1 second' WHERE id = ?", models.MessageSent, m.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.app.DB.Create(&models.MessageRecipient{MessageID: m.ID, UserID: f.teen.ID, Status: models.RecipientFailed, Address: f.teen.Email}).Error; err != nil {
		t.Fatal(err)
	}
	if err := hold.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	if r := awaitResult(t, res); r.code != http.StatusConflict || !strings.Contains(r.body, "message_nothing_to_retry") {
		t.Fatalf("retry: %d %s", r.code, r.body)
	}
	var after models.Message
	f.app.DB.First(&after, m.ID)
	if after.Status != models.MessageSent || !after.ScheduledAt.Equal(*m.ScheduledAt) || jobCountFor(t, f.app, kindMessageSend, m.ID) != 0 ||
		auditCount(t, f.app, audit.MessageRetried) != 0 {
		t.Fatalf("a changed message was retried: %+v", after)
	}
}

// pickySet refuses (550) every address in bad and passes the rest on.
type pickySet struct {
	next mail.Sender
	bad  map[string]bool
}

func (s pickySet) SendEmail(ctx context.Context, m mail.Message) (string, error) {
	if s.bad[m.To] {
		return "", fmt.Errorf("mail: rcpt: %w", &textproto.Error{Code: 550, Msg: "no"})
	}
	return mail.Unwrap(s.next).SendEmail(ctx, m)
}

func (pickySet) Capabilities() providers.EmailCaps { return providers.EmailCaps{Name: "picky-set"} }

// failFor sends aud as the leader with every address in bad refused, so
// those rows fail and the message is partly sent. The mailer is restored.
func (f composeFixture) failFor(t *testing.T, aud map[string]any, bad ...*models.User) uint {
	t.Helper()
	next := f.app.Mailer
	set := map[string]bool{}
	for _, u := range bad {
		set[u.Email] = true
	}
	f.app.Mailer = pickySet{next: next, bad: set}
	id := f.scheduleNow(t, f.leader, "Partly", "<p>Hi</p>", aud)
	runMessageJobs(t, f.app)
	f.app.Mailer = next
	return id
}

// twoFailures adds members X and Y to the group and sends to it with both
// addresses refused.
func (f composeFixture) twoFailures(t *testing.T) (id uint, x, y *models.User) {
	t.Helper()
	x = newTestUser(t, f.app, "xavier", "password1234", models.RoleMember)
	y = newTestUser(t, f.app, "yvonne", "password1234", models.RoleMember)
	addMember(t, f.app, x, f.group, models.MembershipMember)
	addMember(t, f.app, y, f.group, models.MembershipMember)
	id = f.failFor(t, map[string]any{"type": "group", "groupId": f.group.ID}, x, y)
	rows := recipientsOf(t, f.app, id)
	if rows[x.Email].Status != models.RecipientFailed || rows[y.Email].Status != models.RecipientFailed {
		t.Fatalf("setup: %+v", rows)
	}
	return id, x, y
}

func mailsToAddr(t *testing.T, app *App, email string) int64 {
	t.Helper()
	var n int64
	if err := app.DB.Model(&models.MailOutbox{}).Where("lower(to_address) = lower(?)", email).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

// batchJobsListing counts message.deliver_batch jobs that name a row id.
func batchJobsListing(t *testing.T, app *App, rowID uint) int {
	t.Helper()
	var n int
	if err := app.DB.Raw("SELECT count(*) FROM jobs WHERE kind = ? AND payload->'recipientIds' @> to_jsonb(?::bigint)", kindMessageBatch, rowID).Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func (f composeFixture) setMembership(t *testing.T, u *models.User, status string) {
	t.Helper()
	if err := f.app.DB.Model(&models.Membership{}).Where("user_id = ? AND group_id = ?", u.ID, f.group.ID).Update("status", status).Error; err != nil {
		t.Fatal(err)
	}
}

func retriedPeople(t *testing.T, app *App) []string {
	t.Helper()
	var out []string
	if err := app.DB.Raw("SELECT meta_json->>'people' FROM audit_logs WHERE action = ? ORDER BY id", audit.MessageRetried).Scan(&out).Error; err != nil {
		t.Fatal(err)
	}
	return out
}

// A member who was removed or left since the send is skipped on Try
// again, the one still on the list is sent exactly once, and nobody it
// already reached gets it twice (#261).
func TestMessageRetrySkipsWhoLeftTheList(t *testing.T) {
	for _, status := range []string{models.MembershipRemoved, models.MembershipLeft} {
		t.Run(status, func(t *testing.T) {
			f := newComposeFixture(t)
			id, x, y := f.twoFailures(t)
			rowX := recipientsOf(t, f.app, id)[x.Email]
			beforeX := batchJobsListing(t, f.app, rowX.ID)
			teenMails, leaderMails := mailsToAddr(t, f.app, f.teen.Email), mailsToAddr(t, f.app, f.leaderUser.Email)
			f.setMembership(t, x, status)
			if w := f.retry(f.leader, id); w.Code != http.StatusOK {
				t.Fatalf("retry: %d %s", w.Code, w.Body.String())
			}
			rows := recipientsOf(t, f.app, id)
			if r := rows[x.Email]; r.Status != models.RecipientSuppressed || r.Reason != skipNoLongerOnList || r.Error != "" {
				t.Fatalf("X: %+v", r)
			}
			if rows[y.Email].Status != models.RecipientQueued {
				t.Fatalf("Y: %+v", rows[y.Email])
			}
			if batchJobsListing(t, f.app, rowX.ID) != beforeX {
				t.Fatal("a batch job lists the dropped row")
			}
			runMessageJobs(t, f.app)
			var m models.Message
			f.app.DB.First(&m, id)
			if mailsToAddr(t, f.app, x.Email) != 0 || mailsToAddr(t, f.app, y.Email) != 1 ||
				mailsToAddr(t, f.app, f.teen.Email) != teenMails || mailsToAddr(t, f.app, f.leaderUser.Email) != leaderMails || m.Status != models.MessageSent {
				t.Fatalf("X %d, Y %d, teen %d/%d, leader %d/%d, %s", mailsToAddr(t, f.app, x.Email), mailsToAddr(t, f.app, y.Email),
					mailsToAddr(t, f.app, f.teen.Email), teenMails, mailsToAddr(t, f.app, f.leaderUser.Email), leaderMails, m.Status)
			}
			if got := retriedPeople(t, f.app); len(got) != 1 || got[0] != "1" {
				t.Fatalf("message.retried people: %v", got)
			}
		})
	}
}

// When every failed row is dropped the retry still answers 200, queues
// nothing, and the message settles sent with nothing left to retry.
func TestMessageRetryWithEveryFailedRowDropped(t *testing.T) {
	f := newComposeFixture(t)
	id, x, y := f.twoFailures(t)
	jobs := jobCountFor(t, f.app, kindMessageBatch, id)
	f.setMembership(t, x, models.MembershipRemoved)
	f.setMembership(t, y, models.MembershipLeft)
	if w := f.retry(f.leader, id); w.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", w.Code, w.Body.String())
	}
	if jobCountFor(t, f.app, kindMessageBatch, id) != jobs {
		t.Fatal("a retry with nobody left queued a batch")
	}
	var m models.Message
	f.app.DB.First(&m, id)
	rows := recipientsOf(t, f.app, id)
	if m.Status != models.MessageSent || rows[x.Email].Status != models.RecipientSuppressed || rows[y.Email].Status != models.RecipientSuppressed {
		t.Fatalf("message %+v, rows %+v", m, rows)
	}
	if got := retriedPeople(t, f.app); len(got) != 1 || got[0] != "0" {
		t.Fatalf("message.retried people: %v", got)
	}
	if h, _ := f.history(t, f.leader, id); h.CanRetry {
		t.Fatal("canRetry with nothing left")
	}
	if w := f.retry(f.leader, id); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "message_nothing_to_retry") {
		t.Fatalf("again: %d %s", w.Code, w.Body.String())
	}
}

// An event's announcement follows the registrations: a cancelled registrant
// is skipped, one still going is sent.
func TestMessageRetryEventAudience(t *testing.T) {
	for name, aud := range map[string]func(f composeFixture) map[string]any{
		"occurrence": func(f composeFixture) map[string]any {
			return map[string]any{"type": "occurrence", "occurrenceId": f.groupOcc.ID}
		},
		"series": func(f composeFixture) map[string]any {
			return map[string]any{"type": "event_series", "eventId": f.groupEvent.ID}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newComposeFixture(t)
			x := newTestUser(t, f.app, "xavier", "password1234", models.RoleMember)
			y := newTestUser(t, f.app, "yvonne", "password1234", models.RoleMember)
			for _, u := range []*models.User{x, y} {
				f.app.DB.Create(&models.Registration{OccurrenceID: f.groupOcc.ID, UserID: &u.ID, Status: models.RegGoing})
			}
			id := f.failFor(t, aud(f), x, y)
			f.app.DB.Model(&models.Registration{}).Where("occurrence_id = ? AND user_id = ?", f.groupOcc.ID, x.ID).Update("status", models.RegCancelled)
			if w := f.retry(f.leader, id); w.Code != http.StatusOK {
				t.Fatalf("retry: %d %s", w.Code, w.Body.String())
			}
			rows := recipientsOf(t, f.app, id)
			if r := rows[x.Email]; r.Status != models.RecipientSuppressed || r.Reason != skipNoLongerOnList {
				t.Fatalf("X: %+v", r)
			}
			if rows[y.Email].Status != models.RecipientQueued {
				t.Fatalf("Y: %+v", rows[y.Email])
			}
			runMessageJobs(t, f.app)
			if mailsToAddr(t, f.app, x.Email) != 0 || mailsToAddr(t, f.app, y.Email) != 1 {
				t.Fatalf("X %d, Y %d", mailsToAddr(t, f.app, x.Email), mailsToAddr(t, f.app, y.Email))
			}
		})
	}
}

// A guardian copy is sent again only while it would still be made: the
// teen is on the list, the link confirmed, copies on, and the teen would
// still get the message.
func TestMessageRetryGuardianCopies(t *testing.T) {
	cases := []struct {
		name   string
		change func(t *testing.T, f composeFixture)
		skip   string // "" means the copy is sent once
	}{
		{"nothing changed", func(*testing.T, composeFixture) {}, ""},
		{"teen removed", func(t *testing.T, f composeFixture) { f.setMembership(t, f.teen, models.MembershipRemoved) }, skipNoLongerOnList},
		{"link revoked", func(t *testing.T, f composeFixture) {
			f.app.DB.Model(&models.GuardianLink{}).Where("teen_user_id = ?", f.teen.ID).Update("status", "revoked")
		}, skipNoGuardianCopy},
		{"copies turned off", func(t *testing.T, f composeFixture) {
			f.app.DB.Model(&models.Setting{}).Where("key = ?", settingGuardianCopies).Update("value", "false")
		}, skipNoGuardianCopy},
		{"teen turned off messages from leaders", func(t *testing.T, f composeFixture) {
			if w := loginAPI(t, f.app, "teen", "password1234").do("PUT", "/api/v1/me/notification-settings", map[string]any{"category": "messages", "off": true}, true); w.Code != http.StatusOK {
				t.Fatalf("opt out: %d %s", w.Code, w.Body.String())
			}
		}, skipNoGuardianCopy},
		{"teen no longer a teen account", func(t *testing.T, f composeFixture) {
			f.app.DB.Model(&models.User{}).Where("id = ?", f.teen.ID).Update("account_type", models.AccountAdult)
		}, skipNoGuardianCopy},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newComposeFixture(t)
			parent := newTestUser(t, f.app, "parent", "password1234", models.RoleMember)
			f.app.DB.Create(&models.GuardianLink{GuardianUserID: parent.ID, TeenUserID: &f.teen.ID, Status: models.GuardianConfirmed})
			f.app.DB.Create(&models.Setting{Key: settingGuardianCopies, Value: "true"})
			id := f.failFor(t, map[string]any{"type": "group", "groupId": f.group.ID}, parent)
			key := "copy:" + parent.Email
			if r := recipientsOf(t, f.app, id)[key]; r.Status != models.RecipientFailed {
				t.Fatalf("setup copy: %+v", r)
			}
			tc.change(t, f)
			if w := f.retry(f.leader, id); w.Code != http.StatusOK {
				t.Fatalf("retry: %d %s", w.Code, w.Body.String())
			}
			r := recipientsOf(t, f.app, id)[key]
			runMessageJobs(t, f.app)
			if tc.skip == "" {
				if r.Status != models.RecipientQueued || mailsToAddr(t, f.app, parent.Email) != 1 {
					t.Fatalf("copy: %+v, mails %d", r, mailsToAddr(t, f.app, parent.Email))
				}
				return
			}
			if r.Status != models.RecipientSuppressed || r.Reason != tc.skip || r.Error != "" || mailsToAddr(t, f.app, parent.Email) != 0 {
				t.Fatalf("copy: %+v, mails %d", r, mailsToAddr(t, f.app, parent.Email))
			}
		})
	}
}

func TestMessageRetryDropsGuardianCopiesWhenGuardianJoinsAudience(t *testing.T) {
	f := newComposeFixture(t)
	parent := newTestUser(t, f.app, "parent", "password1234", models.RoleMember)
	teen := newTestUser(t, f.app, "second-teen", "password1234", models.RoleMember)
	if err := f.app.DB.Model(teen).Update("account_type", models.AccountTeen).Error; err != nil {
		t.Fatal(err)
	}
	addMember(t, f.app, teen, f.group, models.MembershipMember)
	for _, child := range []*models.User{f.teen, teen} {
		if err := f.app.DB.Create(&models.GuardianLink{GuardianUserID: parent.ID, TeenUserID: &child.ID, Status: models.GuardianConfirmed}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := f.app.DB.Create(&models.Setting{Key: settingGuardianCopies, Value: "true"}).Error; err != nil {
		t.Fatal(err)
	}
	id := f.failFor(t, map[string]any{"type": "group", "groupId": f.group.ID}, parent)
	var copies []models.MessageRecipient
	if err := f.app.DB.Where("message_id = ? AND user_id = ? AND copy_of_user_id IS NOT NULL", id, parent.ID).Find(&copies).Error; err != nil {
		t.Fatal(err)
	}
	if len(copies) != 2 {
		t.Fatalf("wanted two copies, got %+v", copies)
	}
	before := make(map[uint]int, len(copies))
	for _, row := range copies {
		if row.Status != models.RecipientFailed {
			t.Fatalf("copy did not fail: %+v", row)
		}
		before[row.ID] = batchJobsListing(t, f.app, row.ID)
	}
	addMember(t, f.app, parent, f.group, models.MembershipMember)
	if w := f.retry(f.leader, id); w.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", w.Code, w.Body.String())
	}
	if err := f.app.DB.Where("message_id = ? AND user_id = ? AND copy_of_user_id IS NOT NULL", id, parent.ID).Find(&copies).Error; err != nil {
		t.Fatal(err)
	}
	for _, row := range copies {
		if row.Status != models.RecipientSuppressed || row.Reason != skipNoGuardianCopy || row.Error != "" || batchJobsListing(t, f.app, row.ID) != before[row.ID] {
			t.Fatalf("copy after retry: %+v", row)
		}
	}
	runMessageJobs(t, f.app)
	if n := mailsToAddr(t, f.app, parent.Email); n != 0 {
		t.Fatalf("sent %d emails to guardian", n)
	}
}

// The audience is read inside the transaction, after the locks: a removal
// that commits while the press waits for the author's row is honored.
func TestMessageRetryHonorsARemovalDuringTheLock(t *testing.T) {
	f := newComposeFixture(t)
	id, x, y := f.twoFailures(t)
	hold := holdUserRow(t, f.app, f.leaderUser.ID)
	res := bgDo(loginAPI(t, f.app, "leader", "password1234"), "POST", "/api/v1/messages/"+itoa(id)+"/retry", nil)
	waitForAuthorLock(t, f.app)
	f.setMembership(t, x, models.MembershipRemoved)
	if err := hold.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	if r := awaitResult(t, res); r.code != http.StatusOK {
		t.Fatalf("retry: %d %s", r.code, r.body)
	}
	rows := recipientsOf(t, f.app, id)
	if rows[x.Email].Status != models.RecipientSuppressed || rows[x.Email].Reason != skipNoLongerOnList || rows[y.Email].Status != models.RecipientQueued {
		t.Fatalf("rows: %+v", rows)
	}
	runMessageJobs(t, f.app)
	if mailsToAddr(t, f.app, x.Email) != 0 || mailsToAddr(t, f.app, y.Email) != 1 {
		t.Fatalf("X %d, Y %d", mailsToAddr(t, f.app, x.Email), mailsToAddr(t, f.app, y.Email))
	}
}

// The author's rights are read inside the transaction too: demoted while
// the press waits, the retry is refused and nothing changes.
func TestMessageRetryRefusesADemotionDuringTheLock(t *testing.T) {
	f := newComposeFixture(t)
	id, x, y := f.twoFailures(t)
	var before models.Message
	f.app.DB.First(&before, id)
	jobs := jobCountFor(t, f.app, kindMessageBatch, id)
	hold := holdUserRow(t, f.app, f.leaderUser.ID)
	res := bgDo(loginAPI(t, f.app, "leader", "password1234"), "POST", "/api/v1/messages/"+itoa(id)+"/retry", nil)
	waitForAuthorLock(t, f.app)
	if err := f.app.DB.Model(&models.Membership{}).Where("user_id = ? AND group_id = ?", f.leaderUser.ID, f.group.ID).Update("role", models.MembershipMember).Error; err != nil {
		t.Fatal(err)
	}
	if err := hold.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	if r := awaitResult(t, res); r.code != http.StatusForbidden || !strings.Contains(r.body, "message_retry_refused") {
		t.Fatalf("retry: %d %s", r.code, r.body)
	}
	var after models.Message
	f.app.DB.First(&after, id)
	rows := recipientsOf(t, f.app, id)
	if rows[x.Email].Status != models.RecipientFailed || rows[y.Email].Status != models.RecipientFailed ||
		after.Status != before.Status || !after.UpdatedAt.Equal(before.UpdatedAt) ||
		jobCountFor(t, f.app, kindMessageBatch, id) != jobs || auditCount(t, f.app, audit.MessageRetried) != 0 {
		t.Fatalf("a refused retry changed things: %+v, rows %+v", after, rows)
	}
}

// An admin pressing Try again checks in the author's name, not their own.
func TestMessageRetryByAnAdminChecksTheAuthorsAudience(t *testing.T) {
	f := newComposeFixture(t)
	id, x, y := f.twoFailures(t)
	f.setMembership(t, x, models.MembershipRemoved)
	if w := f.retry(f.admin, id); w.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", w.Code, w.Body.String())
	}
	rows := recipientsOf(t, f.app, id)
	if rows[x.Email].Reason != skipNoLongerOnList || rows[y.Email].Status != models.RecipientQueued {
		t.Fatalf("rows: %+v", rows)
	}
}

// The skip reason is counted for a leader and named only for an admin; a
// guardian copy's reason and the guardian's name never reach a leader.
func TestMessageRetrySkipReasonPrivacy(t *testing.T) {
	f := newComposeFixture(t)
	parent := newTestUser(t, f.app, "parent", "password1234", models.RoleMember)
	f.app.DB.Model(parent).Update("display_name", "Gail Guardian")
	parent.DisplayName = "Gail Guardian"
	f.app.DB.Create(&models.GuardianLink{GuardianUserID: parent.ID, TeenUserID: &f.teen.ID, Status: models.GuardianConfirmed})
	f.app.DB.Create(&models.Setting{Key: settingGuardianCopies, Value: "true"})
	x := newTestUser(t, f.app, "xavier", "password1234", models.RoleMember)
	addMember(t, f.app, x, f.group, models.MembershipMember)
	id := f.failFor(t, map[string]any{"type": "group", "groupId": f.group.ID}, x, parent)
	f.setMembership(t, x, models.MembershipRemoved)
	f.app.DB.Model(&models.GuardianLink{}).Where("teen_user_id = ?", f.teen.ID).Update("status", "revoked")
	if w := f.retry(f.leader, id); w.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", w.Code, w.Body.String())
	}
	lead, body := f.history(t, f.leader, id)
	if strings.Contains(body, "@") || strings.Contains(body, parent.PublicName()) || strings.Contains(body, skipNoGuardianCopy) {
		t.Fatalf("private detail in the leader's history: %s", body)
	}
	if lead.SkippedBy[skipNoLongerOnList] != 1 {
		t.Fatalf("skippedBy: %+v", lead.SkippedBy)
	}
	if r := historyRow(lead, x.PublicName()); r.Status != historyMissed || r.Reason != "" {
		t.Fatalf("leader's row for X: %+v", r)
	}
	for _, r := range lead.Recipients {
		if r.CopyOf != "" {
			t.Fatalf("a leader sees a guardian copy: %+v", r)
		}
	}
	adm, body := f.history(t, f.admin, id)
	if strings.Contains(body, "@") || historyRow(adm, x.PublicName()).Reason != skipNoLongerOnList {
		t.Fatalf("admin's history: %s", body)
	}
	copied := false
	for _, r := range adm.Recipients {
		copied = copied || (r.Name == parent.PublicName() && r.CopyOf != "" && r.Reason == skipNoGuardianCopy)
	}
	if !copied {
		t.Fatalf("an admin sees no copy reason: %+v", adm.Recipients)
	}
}
