package main

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"fold/internal/audit"
	"fold/internal/notify"
	"fold/models"
)

type composeFixture struct {
	app                   *App
	admin, leader, host   *apiClient
	member                *apiClient
	leaderUser, hostUser  *models.User
	group, otherGroup     *models.Group
	groupEvent, otherEv   eventJSON
	churchEvent, otherCW  eventJSON
	groupOcc              models.EventOccurrence
	teen, opted, bouncing *models.User
}

// newComposeFixture: a leader's group (with a teen, someone who turned off
// the group's announcements and someone whose email bounces), another
// group, a group event of each, and two church-wide events, one hosted.
func newComposeFixture(t *testing.T) composeFixture {
	t.Helper()
	app := testApp(t)
	f := composeFixture{app: app}
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	f.leaderUser = newTestUser(t, app, "leader", "password1234", models.RoleMember)
	f.hostUser = newTestUser(t, app, "host", "password1234", models.RoleMember)
	newTestUser(t, app, "member", "password1234", models.RoleMember)
	f.admin, f.leader = loginAPI(t, app, "admin", "password1234"), loginAPI(t, app, "leader", "password1234")
	f.host, f.member = loginAPI(t, app, "host", "password1234"), loginAPI(t, app, "member", "password1234")
	g := activeGroup(t, app, f.leaderUser, "Book club", models.VisibilityPublic, "")
	f.group = &g
	other := activeGroup(t, app, f.hostUser, "Choir", models.VisibilityPublic, "")
	f.otherGroup = &other
	start := feedStart(3)
	f.groupEvent = createFeedEvent(t, f.leader, g.ID, "Book night", start, "")
	f.otherEv = createFeedEvent(t, f.host, other.ID, "Rehearsal", start, "")
	app.DB.Where("event_id = ?", f.groupEvent.ID).First(&f.groupOcc)
	church := func(title string) eventJSON {
		var out struct {
			Event eventJSON `json:"event"`
		}
		w := f.admin.do("POST", "/api/v1/events", map[string]any{"title": title, "startsLocal": start.Format("2006-01-02T15:04"), "durationMinutes": 60, "tzid": "UTC", "visibility": "public"}, true)
		if w.Code != http.StatusCreated {
			t.Fatalf("church event: %d %s", w.Code, w.Body.String())
		}
		decodeJSON(t, w, &out)
		return out.Event
	}
	f.churchEvent, f.otherCW = church("Picnic"), church("Concert")
	if err := app.DB.Create(&models.EventHost{EventID: f.churchEvent.ID, UserID: f.hostUser.ID, AddedBy: f.hostUser.ID}).Error; err != nil {
		t.Fatal(err)
	}
	f.teen = newTestUser(t, app, "teen", "password1234", models.RoleMember)
	app.DB.Model(f.teen).Update("account_type", models.AccountTeen)
	f.opted = newTestUser(t, app, "opted", "password1234", models.RoleMember)
	f.bouncing = newTestUser(t, app, "bouncing", "password1234", models.RoleMember)
	for _, u := range []*models.User{f.teen, f.opted, f.bouncing} {
		addMember(t, app, u, f.group, models.MembershipMember)
	}
	// Turning off messages from leaders is what stops announcements (DEC-115, DEC-119).
	if w := loginAPI(t, app, "opted", "password1234").do("PUT", "/api/v1/me/notification-settings", map[string]any{"category": "messages", "off": true}, true); w.Code != http.StatusOK {
		t.Fatalf("opt out: %d %s", w.Code, w.Body.String())
	}
	app.DB.Create(&models.Suppression{Channel: "email", Address: strings.ToLower(f.bouncing.Email), Reason: models.SuppressHardBounce})
	return f
}

type composeOptions = composeOptionsJSON

func (f composeFixture) options(t *testing.T, c *apiClient) composeOptions {
	t.Helper()
	var o composeOptions
	decodeJSON(t, c.do("GET", "/api/v1/messages/compose", nil, false), &o)
	return o
}

func eventTitles(o composeOptions) string {
	var s []string
	for _, e := range o.Events {
		s = append(s, e.Title)
	}
	return strings.Join(s, ",")
}

// The picker offers leaders their own groups and events, hosts only the
// events they host, admins everything; members can't compose (M3.4).
func TestComposerOffersOnlyWhatTheAuthorMayMessage(t *testing.T) {
	f := newComposeFixture(t)
	if o := f.options(t, f.leader); !o.CanCompose || o.CanMessageEveryone || len(o.Groups) != 1 || o.Groups[0].Name != "Book club" || eventTitles(o) != "Book night" {
		t.Fatalf("leader: %+v", o)
	}
	if o := f.options(t, f.host); !o.CanCompose || len(o.Groups) != 1 || eventTitles(o) != "Picnic,Rehearsal" {
		t.Fatalf("host (also leads the choir): %+v", o)
	}
	if o := f.options(t, f.admin); !o.CanMessageEveryone || len(o.Groups) != 2 || eventTitles(o) != "Book night,Concert,Picnic,Rehearsal" {
		t.Fatalf("admin: %+v", o)
	}
	if o := f.options(t, f.member); o.CanCompose || len(o.Groups) != 0 {
		t.Fatalf("member: %+v", o)
	}
	if len(f.options(t, f.leader).Events[0].Dates) != 1 {
		t.Fatal("events list their upcoming dates")
	}
}

// ?event=<id> answers only that event, by the same rules, for the date
// page's "Email people going" (M3.4c).
func TestComposeOptionsForOneEvent(t *testing.T) {
	f := newComposeFixture(t)
	one := func(c *apiClient, id uint) composeOptions {
		t.Helper()
		var o composeOptions
		decodeJSON(t, c.do("GET", "/api/v1/messages/compose?event="+itoa(id), nil, false), &o)
		return o
	}
	if o := one(f.leader, f.groupEvent.ID); len(o.Groups) != 0 || eventTitles(o) != "Book night" || len(o.Events[0].Dates) != 1 {
		t.Fatalf("leader, own event: %+v", o)
	}
	for name, tc := range map[string]struct {
		c  *apiClient
		id uint
	}{"leader, another's event": {f.leader, f.otherEv.ID}, "host, unhosted church event": {f.host, f.otherCW.ID}, "member": {f.member, f.groupEvent.ID}, "no such event": {f.admin, 99999}} {
		if o := one(tc.c, tc.id); len(o.Events) != 0 || len(o.Groups) != 0 {
			t.Errorf("%s: %+v", name, o)
		}
	}
	if o := one(f.host, f.churchEvent.ID); eventTitles(o) != "Picnic" {
		t.Fatalf("host, hosted event: %+v", o)
	}
	if o := one(f.admin, f.otherEv.ID); eventTitles(o) != "Rehearsal" {
		t.Fatalf("admin: %+v", o)
	}
	if w := f.leader.do("GET", "/api/v1/messages/compose?event=nope", nil, false); w.Code != http.StatusBadRequest {
		t.Fatalf("bad id: %d", w.Code)
	}
}

// The SMS tab stays hidden until texting can be set up (DEC-117, M3.7).
func TestComposeOptionsSayTextingIsOff(t *testing.T) {
	f := newComposeFixture(t)
	for _, c := range []*apiClient{f.admin, f.leader, f.member} {
		var raw map[string]any
		decodeJSON(t, c.do("GET", "/api/v1/messages/compose", nil, false), &raw)
		if on, ok := raw["smsEnabled"].(bool); !ok || on {
			t.Fatalf("smsEnabled: %v", raw["smsEnabled"])
		}
	}
}

// The session says whether to offer Messages in the nav (M3.4c): leaders,
// hosts and admins, not plain members.
func TestSessionSaysWhoCanCompose(t *testing.T) {
	f := newComposeFixture(t)
	for name, tc := range map[string]struct {
		c    *apiClient
		want bool
	}{"admin": {f.admin, true}, "leader": {f.leader, true}, "host": {f.host, true}, "member": {f.member, false}} {
		var me struct {
			CanCompose bool `json:"canCompose"`
		}
		decodeJSON(t, tc.c.do("GET", "/api/v1/auth/me", nil, false), &me)
		if me.CanCompose != tc.want {
			t.Errorf("%s: canCompose %v, want %v", name, me.CanCompose, tc.want)
		}
	}
	// Signing in answers it too, so the nav is right before the next read.
	c := newAPIClient(t, f.app)
	c.fetchCSRF()
	var login struct {
		CanCompose bool `json:"canCompose"`
	}
	decodeJSON(t, c.do("POST", "/api/v1/auth/login", map[string]string{"email": testEmail("leader"), "password": "password1234"}, true), &login)
	if !login.CanCompose {
		t.Fatal("login: a leader can compose")
	}
}

// The API refuses targets the author may not message (M3.4 acceptance).
func TestComposerRefusesOtherTargets(t *testing.T) {
	f := newComposeFixture(t)
	draft := func(c *apiClient, audience map[string]any) int {
		return c.do("POST", "/api/v1/messages", map[string]any{"subject": "Hi", "bodyHtml": "<p>Hi</p>", "audience": audience}, true).Code
	}
	for name, tc := range map[string]struct {
		c    *apiClient
		aud  map[string]any
		want int
	}{
		"leader, own group":            {f.leader, map[string]any{"type": "group", "groupId": f.group.ID}, http.StatusCreated},
		"leader, another group":        {f.leader, map[string]any{"type": "group", "groupId": f.otherGroup.ID}, http.StatusForbidden},
		"leader, own event's date":     {f.leader, map[string]any{"type": "occurrence", "occurrenceId": f.groupOcc.ID}, http.StatusCreated},
		"leader, another's event":      {f.leader, map[string]any{"type": "event_series", "eventId": f.otherEv.ID}, http.StatusForbidden},
		"leader, everyone":             {f.leader, map[string]any{"type": "all_members"}, http.StatusForbidden},
		"leader, chosen people":        {f.leader, map[string]any{"type": "users", "userIds": []uint{f.teen.ID}}, http.StatusForbidden},
		"host, hosted event":           {f.host, map[string]any{"type": "events", "eventIds": []uint{f.churchEvent.ID}}, http.StatusCreated},
		"host, unhosted church event":  {f.host, map[string]any{"type": "events", "eventIds": []uint{f.churchEvent.ID, f.otherCW.ID}}, http.StatusForbidden},
		"member, a group they're in":   {f.member, map[string]any{"type": "group", "groupId": f.group.ID}, http.StatusForbidden},
		"admin, everyone":              {f.admin, map[string]any{"type": "all_members"}, http.StatusCreated},
		"no audience":                  {f.admin, map[string]any{}, http.StatusBadRequest},
		"a group that isn't there":     {f.admin, map[string]any{"type": "group", "groupId": 99999}, http.StatusNotFound},
		"leader, no such group":        {f.leader, map[string]any{"type": "group", "groupId": 99999}, http.StatusForbidden},
		"an event list that's too big": {f.admin, map[string]any{"type": "events", "eventIds": make([]uint, 51)}, http.StatusBadRequest},
	} {
		if got := draft(tc.c, tc.aud); got != tc.want {
			t.Errorf("%s: %d, want %d", name, got, tc.want)
		}
	}
	if w := f.leader.do("POST", "/api/v1/messages", map[string]any{"channel": "sms", "subject": "Hi", "audience": map[string]any{"type": "group", "groupId": f.group.ID}}, true); w.Code != http.StatusBadRequest ||
		!strings.Contains(w.Body.String(), "message_channel") {
		t.Fatalf("texting isn't set up: %d %s", w.Code, w.Body.String())
	}
}

// The preview counts who gets it and who is skipped and why, with a teen
// count and Teen badges (DEC-20), names only (DEC-31).
func TestAudiencePreview(t *testing.T) {
	f := newComposeFixture(t)
	w := f.leader.do("POST", "/api/v1/messages/audience-preview", map[string]any{"audience": map[string]any{"type": "group", "groupId": f.group.ID}}, true)
	var out struct {
		Summary audienceSummaryJSON `json:"summary"`
	}
	decodeJSON(t, w, &out)
	s := out.Summary
	if s.People != 4 || s.Eligible != 2 || s.Teens != 1 || s.Skipped[skipOptedOut] != 1 || s.Skipped[skipNotWorking] != 1 {
		t.Fatalf("summary: %+v", s)
	}
	teens := 0
	for _, r := range s.Recipients {
		if r.Teen {
			teens++
		}
	}
	if teens != 1 || len(s.Recipients) != 4 {
		t.Fatalf("recipients: %+v", s.Recipients)
	}
	// A leader sees that someone won't get it, not why; an admin sees why.
	for _, r := range s.Recipients {
		if r.Skipped != "" && r.Skipped != skipHidden {
			t.Fatalf("a leader sees a person's reason: %+v", r)
		}
	}
	decodeJSON(t, f.admin.do("POST", "/api/v1/messages/audience-preview", map[string]any{"audience": map[string]any{"type": "group", "groupId": f.group.ID}}, true), &out)
	reasons := map[string]bool{}
	for _, r := range out.Summary.Recipients {
		reasons[r.Skipped] = true
	}
	if !reasons[skipOptedOut] || !reasons[skipNotWorking] {
		t.Fatalf("admin reasons: %+v", out.Summary.Recipients)
	}
	if strings.Contains(w.Body.String(), "@") {
		t.Fatal("the preview must never show an address")
	}
	// Leaders only by role.
	w = f.leader.do("POST", "/api/v1/messages/audience-preview", map[string]any{"audience": map[string]any{"type": "group", "groupId": f.group.ID, "roles": []string{"leader"}}}, true)
	decodeJSON(t, w, &out)
	if out.Summary.People != 1 {
		t.Fatalf("leaders only: %+v", out.Summary)
	}
}

// A household person's registration reaches the account holder who
// registered them (DEC-8), once.
func TestOccurrenceAudienceReachesTheAccountHolder(t *testing.T) {
	f := newComposeFixture(t)
	parent := newTestUser(t, f.app, "parent", "password1234", models.RoleMember)
	hh := models.Household{Name: "Parents", CreatedBy: parent.ID}
	f.app.DB.Create(&hh)
	kid := models.HouseholdPerson{HouseholdID: hh.ID, FirstName: "Kid", Kind: "child", CreatedBy: parent.ID}
	f.app.DB.Create(&kid)
	f.app.DB.Create(&models.Registration{OccurrenceID: f.groupOcc.ID, HouseholdPersonID: &kid.ID, RegisteredBy: &parent.ID, Status: models.RegGoing})
	f.app.DB.Create(&models.Registration{OccurrenceID: f.groupOcc.ID, UserID: &parent.ID, Status: models.RegGoing})
	f.app.DB.Create(&models.Registration{OccurrenceID: f.groupOcc.ID, UserID: &f.teen.ID, Status: models.RegDeclined})
	// A guest registrant is named, but gets only transactional mail and
	// reminders until they ask for updates (PLAN 7.4).
	guest := newTestUser(t, f.app, "zguest", "password1234", models.RoleMember)
	f.app.DB.Model(guest).Update("status", models.UserGuest)
	f.app.DB.Create(&models.Registration{OccurrenceID: f.groupOcc.ID, UserID: &guest.ID, Status: models.RegGoing})
	var out struct {
		Summary audienceSummaryJSON `json:"summary"`
	}
	decodeJSON(t, f.admin.do("POST", "/api/v1/messages/audience-preview", map[string]any{"audience": map[string]any{"type": "occurrence", "occurrenceId": f.groupOcc.ID}}, true), &out)
	if out.Summary.People != 2 || out.Summary.Eligible != 1 || out.Summary.Skipped[skipGuest] != 1 || out.Summary.Recipients[0].Name != parent.PublicName() {
		t.Fatalf("household and declined registrants: %+v", out.Summary)
	}
}

// Drafts are sanitized; scheduling needs a time ahead and someone to get
// it; scheduled messages can change or be cancelled until they send.
func TestMessageDraftScheduleCancel(t *testing.T) {
	f := newComposeFixture(t)
	aud := map[string]any{"type": "group", "groupId": f.group.ID}
	var out struct {
		Message messageJSON `json:"message"`
	}
	w := f.leader.do("POST", "/api/v1/messages", map[string]any{"subject": "Next week", "bodyHtml": `<p onclick="x()">See <a href="javascript:alert(1)">you</a></p><script>bad()</script>`, "audience": aud}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	decodeJSON(t, w, &out)
	m := out.Message
	if m.Status != models.MessageDraft || m.BodyHTML != "<p>See you</p>" || !m.Editable || m.AuthorName == "" {
		t.Fatalf("draft: %+v", m)
	}
	var row models.Message
	f.app.DB.First(&row, m.ID)
	if row.BodyText != "See you" {
		t.Fatalf("plain text: %q", row.BodyText)
	}
	path := "/api/v1/messages/" + itoa(m.ID)
	if w := f.host.do("GET", path, nil, false); w.Code != http.StatusNotFound {
		t.Fatalf("someone else's message: %d", w.Code)
	}
	if w := f.admin.do("GET", path, nil, false); w.Code != http.StatusOK {
		t.Fatalf("admin: %d", w.Code)
	}
	for _, when := range []time.Time{time.Now().Add(-time.Hour), time.Now().AddDate(2, 0, 0)} {
		if w := f.leader.do("POST", path+"/schedule", map[string]any{"scheduledAt": when}, true); w.Code != http.StatusBadRequest {
			t.Fatalf("schedule at %v: %d", when, w.Code)
		}
	}
	when := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	w = f.leader.do("POST", path+"/schedule", map[string]any{"scheduledAt": when}, true)
	decodeJSON(t, w, &out)
	if w.Code != http.StatusOK || out.Message.Status != models.MessageScheduled || !out.Message.ScheduledAt.Equal(when) {
		t.Fatalf("schedule: %d %s", w.Code, w.Body.String())
	}
	f.app.DB.First(&row, m.ID)
	if row.RecipientCount != 2 || auditCount(t, f.app, audit.MessageScheduled) != 1 {
		t.Fatalf("recipients %d, audited %d", row.RecipientCount, auditCount(t, f.app, audit.MessageScheduled))
	}
	// Still editable while scheduled, but only into something that could
	// still go out; the count follows the audience, and the edit is audited.
	if w := f.leader.do("PUT", path, map[string]any{"subject": "", "bodyHtml": "", "audience": aud}, true); w.Code != http.StatusBadRequest {
		t.Fatalf("emptying a scheduled message: %d %s", w.Code, w.Body.String())
	}
	if w := f.leader.do("PUT", path, map[string]any{"subject": "Next week, again", "bodyHtml": "<p>Changed</p>", "audience": map[string]any{"type": "group", "groupId": f.group.ID, "roles": []string{"leader"}}}, true); w.Code != http.StatusOK {
		t.Fatalf("edit scheduled: %d %s", w.Code, w.Body.String())
	}
	f.app.DB.First(&row, m.ID)
	if row.Status != models.MessageScheduled || row.Subject != "Next week, again" || row.RecipientCount != 1 || auditCount(t, f.app, audit.MessageEdited) != 1 {
		t.Fatalf("scheduled edit: %+v, audited %d", row, auditCount(t, f.app, audit.MessageEdited))
	}
	// What is stored is bounded, not just what was sent: escaping grows it.
	if w := f.leader.do("PUT", path, map[string]any{"subject": "x", "bodyHtml": "<p>" + strings.Repeat("&", 60000) + "</p>", "audience": aud}, true); w.Code != http.StatusBadRequest ||
		!strings.Contains(w.Body.String(), "message_body") {
		t.Fatalf("too long once escaped: %d %s", w.Code, w.Body.String())
	}
	// Once it is sending, nothing changes.
	f.app.DB.Model(&models.Message{}).Where("id = ?", m.ID).Update("status", models.MessageSending)
	for _, c := range []struct{ method, path string }{{"PUT", path}, {"POST", path + "/schedule"}, {"POST", path + "/cancel"}} {
		if w := f.leader.do(c.method, c.path, map[string]any{"subject": "x", "bodyHtml": "<p>x</p>", "audience": aud}, true); w.Code != http.StatusConflict {
			t.Fatalf("%s %s while sending: %d", c.method, c.path, w.Code)
		}
	}
	f.app.DB.Model(&models.Message{}).Where("id = ?", m.ID).Update("status", models.MessageScheduled)
	if w := f.leader.do("POST", path+"/cancel", nil, true); w.Code != http.StatusOK || auditCount(t, f.app, audit.MessageCancelled) != 1 {
		t.Fatalf("cancel: %d", w.Code)
	}
	// A message nobody can get can't be scheduled.
	solo := activeGroup(t, f.app, f.leaderUser, "Just me", models.VisibilityPublic, "")
	f.app.DB.Model(&models.User{}).Where("id = ?", f.leaderUser.ID).Update("email", "")
	decodeJSON(t, f.leader.do("POST", "/api/v1/messages", map[string]any{"subject": "Hi", "bodyHtml": "<p>Hi</p>", "audience": map[string]any{"type": "group", "groupId": solo.ID}}, true), &out)
	if w := f.leader.do("POST", "/api/v1/messages/"+itoa(out.Message.ID)+"/schedule", map[string]any{}, true); w.Code != http.StatusBadRequest ||
		!strings.Contains(w.Body.String(), "message_nobody") {
		t.Fatalf("nobody: %d %s", w.Code, w.Body.String())
	}
	var list struct {
		Messages []messageJSON `json:"messages"`
	}
	decodeJSON(t, f.leader.do("GET", "/api/v1/messages", nil, false), &list)
	if len(list.Messages) != 2 {
		t.Fatalf("my messages: %d", len(list.Messages))
	}
}

// "Push notification too" (M3.13d2, DEC-123) defaults to on when a
// request leaves it out, on create and on update; false is kept; and the
// composer hears whether the church allows push.
func TestMessagePushAlertDefaultsOn(t *testing.T) {
	f := newComposeFixture(t)
	aud := map[string]any{"type": "group", "groupId": f.group.ID}
	var out struct {
		Message messageJSON `json:"message"`
	}
	decodeJSON(t, f.leader.do("POST", "/api/v1/messages", map[string]any{"subject": "Hi", "bodyHtml": "<p>Hi</p>", "audience": aud}, true), &out)
	if !out.Message.PushAlert {
		t.Fatal("pushAlert left out isn't on")
	}
	id := itoa(out.Message.ID)
	decodeJSON(t, f.leader.do("PUT", "/api/v1/messages/"+id, map[string]any{"subject": "Hi", "bodyHtml": "<p>Hi</p>", "audience": aud, "pushAlert": false}, true), &out)
	var m models.Message
	if f.app.DB.First(&m, out.Message.ID); out.Message.PushAlert || m.PushAlert {
		t.Fatalf("pushAlert false: %v %v", out.Message.PushAlert, m.PushAlert)
	}
	decodeJSON(t, f.leader.do("PUT", "/api/v1/messages/"+id, map[string]any{"subject": "Hi", "bodyHtml": "<p>Hi</p>", "audience": aud}, true), &out)
	if !out.Message.PushAlert {
		t.Fatal("an update that leaves pushAlert out doesn't turn it on")
	}
	var opts composeOptionsJSON
	decodeJSON(t, f.leader.do("GET", "/api/v1/messages/compose", nil, false), &opts)
	if !opts.PushEnabled {
		t.Fatal("pushEnabled off by default")
	}
	f.app.DB.Create(&models.Setting{Key: settingPushEnabled, Value: "false"})
	decodeJSON(t, f.leader.do("GET", "/api/v1/messages/compose", nil, false), &opts)
	if opts.PushEnabled {
		t.Fatal("pushEnabled with the church's switch off")
	}
}

// An announcement is never push-only (DEC-123): push is a switch beside
// the email, so asking for channel "push" is refused with its own code,
// on create and on update.
func TestMessagePushOnlyIsRefused(t *testing.T) {
	f := newComposeFixture(t)
	aud := map[string]any{"type": "group", "groupId": f.group.ID}
	for _, ch := range []string{"push", "Push"} {
		w := f.leader.do("POST", "/api/v1/messages", map[string]any{"channel": ch, "subject": "Hi", "bodyHtml": "<p>Hi</p>", "audience": aud, "pushAlert": true}, true)
		if w.Code != http.StatusBadRequest || errorCodeOf(w) != "message_push_only" {
			t.Fatalf("create %q: %d %s", ch, w.Code, w.Body.String())
		}
	}
	var out struct {
		Message messageJSON `json:"message"`
	}
	decodeJSON(t, f.leader.do("POST", "/api/v1/messages", map[string]any{"subject": "Hi", "bodyHtml": "<p>Hi</p>", "audience": aud}, true), &out)
	w := f.leader.do("PUT", "/api/v1/messages/"+itoa(out.Message.ID), map[string]any{"channel": "push", "subject": "Hi", "bodyHtml": "<p>Hi</p>", "audience": aud}, true)
	if w.Code != http.StatusBadRequest || errorCodeOf(w) != "message_push_only" {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	var m models.Message
	if f.app.DB.First(&m, out.Message.ID); m.Channel != "email" {
		t.Fatalf("channel %q", m.Channel)
	}
}

// The preview counts who also gets a push: push on for messages from
// leaders and a device. Someone with push on but no device isn't a skip;
// they still get the email. Off for messages from leaders, or the church
// switch off, counts nobody.
func TestAudiencePreviewCountsPush(t *testing.T) {
	f := newComposeFixture(t)
	pushConsentRow(t, f.app, f.teen, "all", models.ConsentOptedIn)
	newPushDevice(t, f.app, f.teen, "teen-phone")
	pushConsentRow(t, f.app, f.leaderUser, "all", models.ConsentOptedIn) // no device
	// Opted out of email from leaders: skipped, so never counted for push.
	pushConsentRow(t, f.app, f.opted, "all", models.ConsentOptedIn)
	newPushDevice(t, f.app, f.opted, "opted-phone")
	preview := func() audienceSummaryJSON {
		var out struct {
			Summary audienceSummaryJSON `json:"summary"`
		}
		decodeJSON(t, f.leader.do("POST", "/api/v1/messages/audience-preview", map[string]any{"audience": map[string]any{"type": "group", "groupId": f.group.ID}}, true), &out)
		return out.Summary
	}
	s := preview()
	if s.Push != 1 || s.Eligible != 2 || len(s.Skipped) != 2 {
		t.Fatalf("summary: %+v", s)
	}
	for _, r := range s.Recipients {
		if want := r.Name == f.teen.PublicName(); r.Push != want {
			t.Fatalf("%s: push %v", r.Name, r.Push)
		}
	}
	pushConsentRow(t, f.app, f.teen, "category:"+notify.CatMessages, models.ConsentOptedOut)
	if s := preview(); s.Push != 0 || s.Eligible != 2 {
		t.Fatalf("push off for messages from leaders: %+v", s)
	}
	pushConsentRow(t, f.app, f.teen, "category:"+notify.CatMessages, models.ConsentOptedIn)
	f.app.DB.Create(&models.Setting{Key: settingPushEnabled, Value: "false"})
	if s := preview(); s.Push != 0 {
		t.Fatalf("church off: %+v", s)
	}
}
