package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"fold/internal/content"
	"fold/internal/policy"
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

// The read page fills a message's event fields only while, and only as far
// as, the reader may still see the event or date (readerEventValues). The
// tests seed the announcement and its recipient row directly (the send path
// is not what they pin), then change what the reader may see and rename the
// event, so a leaked title is told apart from the title they were sent.

const (
	readVisSubject = "{{event_title}} news for {{first_name}}"
	readVisBody    = `<p>{{event_title}} at {{event_time}}. <a href="{{event_link}}">See the event</a> <a href="{{manage_link}}">your sign-up</a></p>`
)

// seedAnnouncement is a sent announcement by author with a recipient row for
// each of to, and returns its id.
func seedAnnouncement(t *testing.T, app *App, author *models.User, au audienceJSON, to ...*models.User) uint {
	t.Helper()
	raw, err := json.Marshal(au)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	m := models.Message{AuthorID: &author.ID, Status: models.MessageSent, Subject: readVisSubject,
		BodyHTML: content.SanitizeHTML(readVisBody), Audience: string(raw), SentAt: &now}
	if err := app.DB.Create(&m).Error; err != nil {
		t.Fatal(err)
	}
	for _, u := range to {
		row := models.MessageRecipient{MessageID: m.ID, UserID: u.ID, Channel: models.DeliveryEmail, Address: u.Email, Status: models.RecipientSent}
		if err := app.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	return m.ID
}

// readAs reads a message's page as c, expecting 200.
func readAs(t *testing.T, c *apiClient, id uint) messageReadJSON {
	t.Helper()
	w := c.do("GET", "/api/v1/messages/"+itoa(id)+"/read", nil, false)
	if w.Code != http.StatusOK {
		t.Fatalf("read %d: %d %s", id, w.Code, w.Body.String())
	}
	var out struct {
		Message messageReadJSON `json:"message"`
	}
	decodeJSON(t, w, &out)
	return out.Message
}

// readWant is what one reader's page holds. An empty field is "nothing":
// no title, no time, and a link that reads as its text.
type readWant struct{ title, when, event, manage string }

// checkRead asserts each field on its own: the title (subject and body), the
// time, and each link's href, with the link text always kept.
func checkRead(t *testing.T, label string, got messageReadJSON, reader *models.User, w readWant) {
	t.Helper()
	first := firstName(reader.PublicName())
	if want := strings.TrimSpace(w.title + " news for " + first); got.Subject != want {
		t.Errorf("%s: subject %q, want %q", label, got.Subject, want)
	}
	if prefix := "<p>" + w.title + " at " + w.when + ". "; !strings.HasPrefix(got.BodyHTML, prefix) {
		t.Errorf("%s: body %q, want it to start %q", label, got.BodyHTML, prefix)
	}
	for text, url := range map[string]string{"See the event": w.event, "your sign-up": w.manage} {
		if !strings.Contains(got.BodyHTML, text) {
			t.Errorf("%s: the %q text is gone: %s", label, text, got.BodyHTML)
		}
		linked := regexp.MustCompile(`<a href="` + regexp.QuoteMeta(url) + `"[^>]*>` + regexp.QuoteMeta(text) + `</a>`)
		anyLink := regexp.MustCompile(`<a [^>]*>` + regexp.QuoteMeta(text) + `</a>`)
		switch {
		case url != "" && !linked.MatchString(got.BodyHTML):
			t.Errorf("%s: %q should link to %s: %s", label, text, url, got.BodyHTML)
		case url == "" && anyLink.MatchString(got.BodyHTML):
			t.Errorf("%s: %q should read as text: %s", label, text, got.BodyHTML)
		}
	}
}

type readVisFixture struct {
	app             *App
	admin, leader   *apiClient
	adminU, leaderU *models.User
	reader          *models.User
	readerC         *apiClient
	group           models.Group
	ev              eventJSON
	occs            []models.EventOccurrence
}

// newReadVisFixture: a leader's group of the given visibility with a weekly
// event of three dates, and a reader who is not a member (yet).
func newReadVisFixture(t *testing.T, groupVisibility string) readVisFixture {
	t.Helper()
	app := testApp(t)
	f := readVisFixture{app: app}
	f.adminU = newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	f.leaderU = newTestUser(t, app, "leader", "password1234", models.RoleMember)
	f.reader = newTestUser(t, app, "reader", "password1234", models.RoleMember)
	f.group = activeGroup(t, app, f.leaderU, "Vis group", groupVisibility, "")
	f.admin, f.leader = loginAPI(t, app, "admin", "password1234"), loginAPI(t, app, "leader", "password1234")
	f.readerC = loginAPI(t, app, "reader", "password1234")
	f.ev = createFeedEvent(t, f.leader, f.group.ID, "Vis night", feedStart(3), "FREQ=WEEKLY;COUNT=3")
	if err := app.DB.Where("event_id = ?", f.ev.ID).Order("starts_at").Find(&f.occs).Error; err != nil || len(f.occs) != 3 {
		t.Fatal(err, len(f.occs))
	}
	return f
}

func (f readVisFixture) eventURL() string {
	return f.app.emailLink("/events/" + itoa(f.ev.ID))
}

func (f readVisFixture) dateURL(o models.EventOccurrence) string {
	return f.app.emailLink(occurrenceDatePath(f.ev.ID, models.FormatCalendarDate(o.OriginalDate)))
}

func (f readVisFixture) when(o models.EventOccurrence) string {
	return formatWhen(o.StartsAt, eventZone("UTC"))
}

func (f readVisFixture) occAudience(o models.EventOccurrence) audienceJSON {
	return audienceJSON{Type: policy.AudienceOccurrence, OccurrenceID: o.ID}
}

func (f readVisFixture) seriesAudience() audienceJSON {
	return audienceJSON{Type: policy.AudienceEventSeries, EventID: f.ev.ID}
}

// register holds the reader's seat on a date without the registration API, so
// no visibility rule cancels it first.
func (f readVisFixture) register(t *testing.T, u *models.User, o models.EventOccurrence) {
	t.Helper()
	if err := f.app.DB.Create(&models.Registration{OccurrenceID: o.ID, UserID: &u.ID, Status: models.RegGoing}).Error; err != nil {
		t.Fatal(err)
	}
}

func (f readVisFixture) rename(t *testing.T, title string) {
	t.Helper()
	if err := f.app.DB.Model(&models.Event{}).Where("id = ?", f.ev.ID).Update("title", title).Error; err != nil {
		t.Fatal(err)
	}
}

func (f readVisFixture) setDate(t *testing.T, o models.EventOccurrence, column string, value any) {
	t.Helper()
	if err := f.app.DB.Model(&models.EventOccurrence{}).Where("id = ?", o.ID).Update(column, value).Error; err != nil {
		t.Fatal(err)
	}
}

func (f readVisFixture) setMembership(t *testing.T, u *models.User, status string) {
	t.Helper()
	if err := f.app.DB.Model(&models.Membership{}).Where("group_id = ? AND user_id = ?", f.group.ID, u.ID).Update("status", status).Error; err != nil {
		t.Fatal(err)
	}
}

// Readers who may see the series get what a send gives them, a live rename
// included: the recipient, the author and an admin. A group announcement
// gets the first name only.
func TestReadPageSeriesViewerSeesLiveValues(t *testing.T) {
	f := newReadVisFixture(t, models.VisibilityPublic)
	f.register(t, f.reader, f.occs[0])
	occ := seedAnnouncement(t, f.app, f.leaderU, f.occAudience(f.occs[0]), f.reader)
	series := seedAnnouncement(t, f.app, f.leaderU, f.seriesAudience(), f.reader)
	group := seedAnnouncement(t, f.app, f.leaderU, audienceJSON{Type: policy.AudienceGroup, GroupID: f.group.ID}, f.reader)
	live := readWant{title: "Vis night", when: f.when(f.occs[0]), event: f.eventURL(), manage: f.dateURL(f.occs[0])}
	for _, id := range []uint{occ, series} {
		checkRead(t, "before the rename", readAs(t, f.readerC, id), f.reader, live)
	}
	f.rename(t, "Renamed night")
	live.title = "Renamed night"
	for _, id := range []uint{occ, series} {
		checkRead(t, "after the rename", readAs(t, f.readerC, id), f.reader, live)
	}
	// The author and an admin hold no seat: no time, and the event's page
	// stands in for their date.
	for name, c := range map[string]*apiClient{"author": f.leader, "admin": f.admin} {
		u := f.leaderU
		if name == "admin" {
			u = f.adminU
		}
		checkRead(t, name, readAs(t, c, series), u, readWant{title: "Renamed night", event: f.eventURL(), manage: f.eventURL()})
	}
	checkRead(t, "group audience", readAs(t, f.readerC, group), f.reader, readWant{})
}

// Leaving an invite-only group ends the reader's sight of its event, and the
// page stops filling event fields; joining again restores them.
func TestReadPageLeavingTheGroupBlanksEventFields(t *testing.T) {
	f := newReadVisFixture(t, models.VisibilityInviteOnly)
	addMember(t, f.app, f.reader, &f.group, models.MembershipMember)
	f.register(t, f.reader, f.occs[0])
	ids := []uint{
		seedAnnouncement(t, f.app, f.leaderU, f.occAudience(f.occs[0]), f.reader),
		seedAnnouncement(t, f.app, f.leaderU, f.seriesAudience(), f.reader),
	}
	live := readWant{title: "Vis night", when: f.when(f.occs[0]), event: f.eventURL(), manage: f.dateURL(f.occs[0])}
	for _, id := range ids {
		checkRead(t, "a member", readAs(t, f.readerC, id), f.reader, live)
	}
	// A direct status write, as the roster's removal makes it with no date kept.
	f.setMembership(t, f.reader, models.MembershipRemoved)
	getEvent(t, f.readerC, f.ev.ID, http.StatusNotFound)
	f.rename(t, "Renamed night")
	for _, id := range ids {
		checkRead(t, "removed", readAs(t, f.readerC, id), f.reader, readWant{})
	}
	f.setMembership(t, f.reader, models.MembershipActive)
	live.title = "Renamed night"
	for _, id := range ids {
		checkRead(t, "added again", readAs(t, f.readerC, id), f.reader, live)
	}
}

func TestReadPageAfterSendAndLostAccess(t *testing.T) {
	f := newReadVisFixture(t, models.VisibilityInviteOnly)
	addMember(t, f.app, f.reader, &f.group, models.MembershipMember)
	f.register(t, f.reader, f.occs[0])
	compose := composeFixture{app: f.app}
	id := compose.scheduleNow(t, f.leader, readVisSubject, readVisBody, map[string]any{"type": "occurrence", "occurrenceId": f.occs[0].ID})
	runMessageJobs(t, f.app)
	var row models.MessageRecipient
	if err := f.app.DB.Where("message_id = ? AND user_id = ? AND channel = ?", id, f.reader.ID, models.DeliveryEmail).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Status != models.RecipientSent {
		t.Fatalf("recipient was not sent the announcement: %+v", row)
	}
	f.setMembership(t, f.reader, models.MembershipRemoved)
	f.rename(t, "Renamed night")
	checkRead(t, "after delivery and removal", readAs(t, f.readerC, id), f.reader, readWant{})
}

// A private event is open to any signed-in person, so leaving its group is not
// losing sight of it: the gate is the event's visibility, not membership.
func TestReadPagePrivateEventStaysSeenAfterLeaving(t *testing.T) {
	f := newReadVisFixture(t, models.VisibilityPrivate)
	addMember(t, f.app, f.reader, &f.group, models.MembershipMember)
	f.register(t, f.reader, f.occs[0])
	id := seedAnnouncement(t, f.app, f.leaderU, f.occAudience(f.occs[0]), f.reader)
	f.setMembership(t, f.reader, models.MembershipRemoved)
	f.rename(t, "Renamed night")
	checkRead(t, "removed from a private group", readAs(t, f.readerC, id), f.reader,
		readWant{title: "Renamed night", when: f.when(f.occs[0]), event: f.eventURL(), manage: f.dateURL(f.occs[0])})
}

// Making the event alone invite-only (the group stays public) cancels a
// non-member's seat; the announcements sent before read blank.
func TestReadPageEventTightenedToInviteOnlyBlanks(t *testing.T) {
	f := newReadVisFixture(t, models.VisibilityPublic)
	f.register(t, f.reader, f.occs[0])
	ids := []uint{
		seedAnnouncement(t, f.app, f.leaderU, f.occAudience(f.occs[0]), f.reader),
		seedAnnouncement(t, f.app, f.leaderU, f.seriesAudience(), f.reader),
	}
	apiDo(t, f.admin, "PUT", fmt.Sprintf("/api/v1/events/%d/visibility", f.ev.ID),
		map[string]any{"visibility": models.VisibilityInviteOnly, "confirm": true}, http.StatusOK)
	f.rename(t, "Renamed night")
	for _, id := range ids {
		checkRead(t, "tightened", readAs(t, f.readerC, id), f.reader, readWant{})
	}
}

// A date opened by its own override on a hidden series is seen on its own:
// its title, its time and its page, never the series link.
func TestReadPageOpenedDateOfHiddenSeries(t *testing.T) {
	f := newReadVisFixture(t, models.VisibilityInviteOnly)
	f.setDate(t, f.occs[0], "visibility_override", models.VisibilityPublic)
	f.register(t, f.reader, f.occs[0])
	occ := seedAnnouncement(t, f.app, f.leaderU, f.occAudience(f.occs[0]), f.reader)
	series := seedAnnouncement(t, f.app, f.leaderU, f.seriesAudience(), f.reader)
	f.rename(t, "Renamed night")
	checkRead(t, "the opened date", readAs(t, f.readerC, occ), f.reader,
		readWant{title: "Renamed night", when: f.when(f.occs[0]), manage: f.dateURL(f.occs[0])})
	// Its title is the series' too, while the date has none of its own.
	checkRead(t, "the series, by an untitled date", readAs(t, f.readerC, series), f.reader, readWant{title: "Renamed night"})

	// A date's own title, even an empty one, never lends the series title.
	f.setDate(t, f.occs[0], "title_override", "")
	checkRead(t, "an empty date title", readAs(t, f.readerC, occ), f.reader, readWant{when: f.when(f.occs[0]), manage: f.dateURL(f.occs[0])})
	checkRead(t, "the series, by an empty date title", readAs(t, f.readerC, series), f.reader, readWant{})
	f.setDate(t, f.occs[0], "title_override", "Special night")
	checkRead(t, "a titled date", readAs(t, f.readerC, occ), f.reader, readWant{title: "Special night", when: f.when(f.occs[0]), manage: f.dateURL(f.occs[0])})
	checkRead(t, "the series, by a titled date", readAs(t, f.readerC, series), f.reader, readWant{})
}

// Closing one date of a public series shuts the reader out of that date only.
func TestReadPageOverrideTighteningBlanksOneDate(t *testing.T) {
	f := newReadVisFixture(t, models.VisibilityPublic)
	f.setDate(t, f.occs[0], "visibility_override", models.VisibilityInviteOnly)
	f.register(t, f.reader, f.occs[0])
	occ := seedAnnouncement(t, f.app, f.leaderU, f.occAudience(f.occs[0]), f.reader)
	series := seedAnnouncement(t, f.app, f.leaderU, f.seriesAudience(), f.reader)
	checkRead(t, "the closed date", readAs(t, f.readerC, occ), f.reader, readWant{})
	// The series is still seen; the closed date supplies no time, and the
	// event's page stands in for the date's.
	checkRead(t, "the series", readAs(t, f.readerC, series), f.reader, readWant{title: "Vis night", event: f.eventURL(), manage: f.eventURL()})
}

// An invitation to a closed date of a public series lets its time and page
// through on a series announcement (the scoped fallback).
func TestReadPageSeriesViewerWithAnInvitedDate(t *testing.T) {
	f := newReadVisFixture(t, models.VisibilityPublic)
	f.setDate(t, f.occs[0], "visibility_override", models.VisibilityInviteOnly)
	seedReadInvitation(t, f.app, f.adminU, f.reader, f.ev, f.occs[0])
	series := seedAnnouncement(t, f.app, f.leaderU, f.seriesAudience(), f.reader)
	occ := seedAnnouncement(t, f.app, f.leaderU, f.occAudience(f.occs[0]), f.reader)
	checkRead(t, "the series", readAs(t, f.readerC, series), f.reader,
		readWant{title: "Vis night", when: f.when(f.occs[0]), event: f.eventURL(), manage: f.dateURL(f.occs[0])})
	// Seen on its own, the invited date has no series link.
	checkRead(t, "the invited date", readAs(t, f.readerC, occ), f.reader,
		readWant{title: "Vis night", when: f.when(f.occs[0]), manage: f.dateURL(f.occs[0])})
}

// An exact-date invitation to a hidden series gives the date alone, and ends
// with the invitation or the date.
func TestReadPageExactDateInvitation(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	app, date := f.app, f.dates[1]
	url := func(o models.EventOccurrence) string {
		return app.emailLink(occurrenceDatePath(f.event.ID, models.FormatCalendarDate(o.OriginalDate)))
	}
	when := formatWhen(date.StartsAt, eventZone("UTC"))
	occ := seedAnnouncement(t, app, f.issuer, audienceJSON{Type: policy.AudienceOccurrence, OccurrenceID: date.ID}, f.recipient)
	series := seedAnnouncement(t, app, f.issuer, audienceJSON{Type: policy.AudienceEventSeries, EventID: f.event.ID}, f.recipient)

	checkRead(t, "the invited date", readAs(t, f.client, occ), f.recipient,
		readWant{title: "Invited date title", when: when, manage: url(date)})
	// The date has a title of its own, so the series title stays blank.
	checkRead(t, "the series, by a titled date", readAs(t, f.client, series), f.recipient, readWant{})

	// Without its own title the date shows the event's, and lends it to the
	// series announcement: no time, no link, even when the invitee's next
	// series registration is on another date they cannot see.
	if err := app.DB.Model(&models.EventOccurrence{}).Where("id = ?", date.ID).Update("title_override", nil).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Create(&models.Registration{OccurrenceID: f.dates[0].ID, UserID: &f.recipient.ID, Status: models.RegGoing}).Error; err != nil {
		t.Fatal(err)
	}
	checkRead(t, "the untitled date", readAs(t, f.client, occ), f.recipient,
		readWant{title: "Hidden series title", when: when, manage: url(date)})
	checkRead(t, "the series, by an untitled date", readAs(t, f.client, series), f.recipient, readWant{title: "Hidden series title"})

	// A cancelled date is blank to someone who cannot see the series.
	if err := app.DB.Model(&models.EventOccurrence{}).Where("id = ?", date.ID).Update("status", models.OccurrenceCancelled).Error; err != nil {
		t.Fatal(err)
	}
	checkRead(t, "the cancelled date", readAs(t, f.client, occ), f.recipient, readWant{})
	if err := app.DB.Model(&models.EventOccurrence{}).Where("id = ?", date.ID).Update("status", models.OccurrenceScheduled).Error; err != nil {
		t.Fatal(err)
	}
	endSeededAccess(t, app.DB, f.access.RegistrationID, policy.OccurrenceDeclined, &f.recipient.ID, time.Now().UTC())
	checkRead(t, "the ended invitation", readAs(t, f.client, occ), f.recipient, readWant{})
	checkRead(t, "the series, after the invitation", readAs(t, f.client, series), f.recipient, readWant{})
}

// A date kept on leaving a group is seen on its own until its access ends.
func TestReadPageKeptDepartureDate(t *testing.T) {
	f := newDepartureReadFixture(t, models.VisibilityInviteOnly, models.RegGoing, policy.OccurrenceOriginDepartureSelf)
	date := f.dates[1]
	occ := seedAnnouncement(t, f.app, f.issuer, audienceJSON{Type: policy.AudienceOccurrence, OccurrenceID: date.ID}, f.recipient)
	checkRead(t, "the kept date", readAs(t, f.client, occ), f.recipient, readWant{
		title:  "Retained date title",
		when:   formatWhen(date.StartsAt, eventZone("UTC")),
		manage: f.app.emailLink(occurrenceDatePath(f.event.ID, models.FormatCalendarDate(date.OriginalDate)))})
	endSeededAccess(t, f.app.DB, f.access.RegistrationID, policy.OccurrenceDeclined, &f.recipient.ID, time.Now().UTC())
	checkRead(t, "the ended date", readAs(t, f.client, occ), f.recipient, readWant{})
}

// A cancelled date shows its series title and link to someone who sees the
// series, never the date's own title or time.
func TestReadPageCancelledDate(t *testing.T) {
	f := newReadVisFixture(t, models.VisibilityPublic)
	f.setDate(t, f.occs[0], "title_override", "Special night")
	f.register(t, f.reader, f.occs[0])
	occ := seedAnnouncement(t, f.app, f.leaderU, f.occAudience(f.occs[0]), f.reader)
	f.setDate(t, f.occs[0], "status", models.OccurrenceCancelled)
	checkRead(t, "a series viewer", readAs(t, f.readerC, occ), f.reader, readWant{title: "Vis night", event: f.eventURL(), manage: f.eventURL()})
	apiDo(t, f.admin, "PUT", fmt.Sprintf("/api/v1/events/%d/visibility", f.ev.ID),
		map[string]any{"visibility": models.VisibilityInviteOnly, "confirm": true}, http.StatusOK)
	checkRead(t, "someone who cannot see the series", readAs(t, f.readerC, occ), f.reader, readWant{})
}

// Nobody is exempt: an author who lost the event reads blank, an admin and a
// current manager see it, and a reminder is read only by its managers.
func TestReadPageAuthorAdminAndReminderManager(t *testing.T) {
	f := newReadVisFixture(t, models.VisibilityInviteOnly)
	f.register(t, f.reader, f.occs[0])
	authored := seedAnnouncement(t, f.app, f.leaderU, f.occAudience(f.occs[0]), f.reader)
	checkRead(t, "the author, a leader", readAs(t, f.leader, authored), f.leaderU,
		readWant{title: "Vis night", event: f.eventURL(), manage: f.eventURL()})

	reminder := seedAnnouncement(t, f.app, f.adminU, f.occAudience(f.occs[0]))
	if err := f.app.DB.Model(&models.Message{}).Where("id = ?", reminder).
		Updates(map[string]any{"category": models.MessageCategoryReminder, "occurrence_id": f.occs[0].ID}).Error; err != nil {
		t.Fatal(err)
	}
	checkRead(t, "a managing leader's reminder", readAs(t, f.leader, reminder), f.leaderU,
		readWant{title: "Vis night", event: f.eventURL(), manage: f.eventURL()})
	if w := f.readerC.do("GET", "/api/v1/messages/"+itoa(reminder)+"/read", nil, false); w.Code != http.StatusNotFound {
		t.Fatalf("a reminder for a non-manager: %d", w.Code)
	}

	// The leader steps down with no date kept: still the author, and blind.
	if err := f.app.DB.Model(&models.Membership{}).Where("group_id = ? AND user_id = ?", f.group.ID, f.leaderU.ID).
		Update("status", models.MembershipRemoved).Error; err != nil {
		t.Fatal(err)
	}
	f.rename(t, "Renamed night")
	checkRead(t, "the removed author", readAs(t, f.leader, authored), f.leaderU, readWant{})
	// An admin sees every event, even one in an archived group.
	if err := f.app.DB.Model(&models.Group{}).Where("id = ?", f.group.ID).Update("status", models.GroupArchived).Error; err != nil {
		t.Fatal(err)
	}
	checkRead(t, "an admin", readAs(t, f.admin, authored), f.adminU, readWant{title: "Renamed night", event: f.eventURL(), manage: f.eventURL()})
}

// Church-wide events: a public one reads as ever; a date closed by an
// invite-only override reads blank for its registrant and its host, who see
// no more than anyone, but not for an admin or someone invited to it.
func TestReadPageChurchWideEvent(t *testing.T) {
	f := newComposeFixture(t)
	var occ models.EventOccurrence
	if err := f.app.DB.Where("event_id = ?", f.churchEvent.ID).First(&occ).Error; err != nil {
		t.Fatal(err)
	}
	memberU, adminU := &models.User{}, &models.User{}
	if err := f.app.DB.Where("name = ?", "member").First(memberU).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.app.DB.Where("name = ?", "admin").First(adminU).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.app.DB.Create(&models.Registration{OccurrenceID: occ.ID, UserID: &memberU.ID, Status: models.RegGoing}).Error; err != nil {
		t.Fatal(err)
	}
	when := formatWhen(occ.StartsAt, eventZone("UTC"))
	eventURL := f.app.emailLink("/events/" + itoa(f.churchEvent.ID))
	dateURL := f.app.emailLink(occurrenceDatePath(f.churchEvent.ID, models.FormatCalendarDate(occ.OriginalDate)))
	id := seedAnnouncement(t, f.app, adminU, audienceJSON{Type: policy.AudienceOccurrence, OccurrenceID: occ.ID}, memberU)
	checkRead(t, "a public church-wide date", readAs(t, f.member, id), memberU, readWant{title: "Picnic", when: when, event: eventURL, manage: dateURL})

	if err := f.app.DB.Model(&models.EventOccurrence{}).Where("id = ?", occ.ID).Update("visibility_override", models.VisibilityInviteOnly).Error; err != nil {
		t.Fatal(err)
	}
	checkRead(t, "the registrant of a closed date", readAs(t, f.member, id), memberU, readWant{})
	// A host who manages a reminder for the date gets 200 and no event fields.
	reminder := seedAnnouncement(t, f.app, adminU, audienceJSON{Type: policy.AudienceOccurrence, OccurrenceID: occ.ID})
	if err := f.app.DB.Model(&models.Message{}).Where("id = ?", reminder).
		Updates(map[string]any{"category": models.MessageCategoryReminder, "occurrence_id": occ.ID}).Error; err != nil {
		t.Fatal(err)
	}
	checkRead(t, "the host", readAs(t, f.host, reminder), f.hostUser, readWant{})
	// An admin keeps them (no seat, so the event's page stands in).
	checkRead(t, "an admin", readAs(t, f.admin, id), adminU, readWant{title: "Picnic", event: eventURL, manage: eventURL})
	// Someone invited to the date sees the date alone.
	invitee := newTestUser(t, f.app, "invitee", "password1234", models.RoleMember)
	seedReadInvitation(t, f.app, adminU, invitee, f.churchEvent, occ)
	inviteeID := seedAnnouncement(t, f.app, adminU, audienceJSON{Type: policy.AudienceOccurrence, OccurrenceID: occ.ID}, invitee)
	checkRead(t, "an invitee", readAs(t, loginAPI(t, f.app, "invitee", "password1234"), inviteeID), invitee,
		readWant{title: "Picnic", when: when, manage: dateURL})
}

// A soft-deleted event (or its group) and a date that is gone read blank, an
// admin included, and never answer 500.
func TestReadPageGoneEventsReadBlank(t *testing.T) {
	f := newReadVisFixture(t, models.VisibilityPublic)
	f.register(t, f.reader, f.occs[0])
	occ := seedAnnouncement(t, f.app, f.leaderU, f.occAudience(f.occs[0]), f.reader)
	series := seedAnnouncement(t, f.app, f.leaderU, f.seriesAudience(), f.reader)
	missing := seedAnnouncement(t, f.app, f.leaderU, audienceJSON{Type: policy.AudienceOccurrence, OccurrenceID: f.occs[2].ID + 1000}, f.reader)
	checkRead(t, "a missing date", readAs(t, f.readerC, missing), f.reader, readWant{})

	if err := f.app.DB.Exec("UPDATE events SET deleted_at = now() WHERE id = ?", f.ev.ID).Error; err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint{occ, series} {
		checkRead(t, "a deleted event", readAs(t, f.readerC, id), f.reader, readWant{})
		checkRead(t, "a deleted event, as admin", readAs(t, f.admin, id), f.adminU, readWant{})
	}
	if err := f.app.DB.Exec("UPDATE events SET deleted_at = NULL WHERE id = ?", f.ev.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.app.DB.Exec("UPDATE groups SET deleted_at = now() WHERE id = ?", f.group.ID).Error; err != nil {
		t.Fatal(err)
	}
	checkRead(t, "a deleted group, as admin", readAs(t, f.admin, series), f.adminU, readWant{})
}

// A failed read inside the check is returned, never read as "not visible".
func TestReaderEventValuesReturnsReadErrors(t *testing.T) {
	f := newReadVisFixture(t, models.VisibilityPublic)
	f.register(t, f.reader, f.occs[0])
	original := f.app.DB
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.app.DB = original.WithContext(ctx)
	t.Cleanup(func() { f.app.DB = original })
	for name, au := range map[string]audienceJSON{"occurrence": f.occAudience(f.occs[0]), "series": f.seriesAudience()} {
		if vals, err := f.app.readerEventValues(f.reader, au, time.Now().UTC()); err == nil {
			t.Errorf("%s: a failed read answered %v, no error", name, vals)
		}
	}
}

// The hidden-series fallback proves a visible untitled date exists without
// materializing every grant at once: the probe pages the event's untitled
// scheduled dates and validates each page with the date page's own grant
// check. Ended earlier grants never hide a later live one, and with no live
// grant the series title stays blank. The page shows the title alone in both
// cases, never a time or an /events/ href.
func TestReadPageHiddenSeriesManyGrantsInvalidBeforeValid(t *testing.T) {
	app := testApp(t)
	issuer := newTestUser(t, app, "issuer", "password1234", models.RoleAdmin)
	reader := newTestUser(t, app, "reader", "password1234", models.RoleMember)
	admin := loginAPI(t, app, issuer.Name, "password1234")
	readerC := loginAPI(t, app, reader.Name, "password1234")
	group := createFeedGroup(t, admin, "Hidden group many", models.VisibilityInviteOnly, "")
	ev := createFeedEvent(t, admin, group, "Hidden series many", feedStart(7), "FREQ=DAILY;COUNT=55")
	var dates []models.EventOccurrence
	if err := app.DB.Where("event_id = ?", ev.ID).Order("id").Find(&dates).Error; err != nil || len(dates) != 55 {
		t.Fatal(err, len(dates))
	}
	rows := make([]accessRow, 0, len(dates))
	for _, d := range dates {
		rows = append(rows, seedReadInvitation(t, app, issuer, reader, ev, d))
	}
	// Earlier grants remain live but their cancelled registrations fail policy.
	// This forces validation across a full page before the final valid grant.
	for _, r := range rows[:len(rows)-1] {
		if err := app.DB.Model(&models.Registration{}).Where("id = ?", r.RegistrationID).Update("status", models.RegCancelled).Error; err != nil {
			t.Fatal(err)
		}
	}
	series := seedAnnouncement(t, app, issuer, audienceJSON{Type: policy.AudienceEventSeries, EventID: ev.ID}, reader)
	checkRead(t, "invalid grants before the valid one", readAs(t, readerC, series), reader, readWant{title: "Hidden series many"})
	// With no live grant left, the series title goes too: nothing leaks.
	endSeededAccess(t, app.DB, rows[len(rows)-1].RegistrationID, policy.OccurrenceDeclined, &reader.ID, time.Now().UTC())
	checkRead(t, "no valid grant", readAs(t, readerC, series), reader, readWant{})
}
