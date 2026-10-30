package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fold/internal/events"
	"fold/models"

	ics "github.com/arran4/golang-ical"
	"gorm.io/gorm"
)

// feedStart is an evening a fixed number of days out, always inside the
// feed window (30 days back to 12 months ahead).
func feedStart(days int) time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day(), 19, 0, 0, 0, time.UTC).AddDate(0, 0, days)
}

func feedDate(t time.Time) string { return t.Format("2006-01-02") }

func createFeedGroup(t *testing.T, c *apiClient, name, visibility, location string) uint {
	t.Helper()
	w := c.do("POST", "/api/v1/groups", map[string]any{
		"name": name, "visibility": visibility, "location": location,
	}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create group %s: %d %s", name, w.Code, w.Body.String())
	}
	var created struct {
		Group groupSummaryJSON `json:"group"`
	}
	decodeJSON(t, w, &created)
	return created.Group.ID
}

func createFeedEvent(t *testing.T, c *apiClient, gid uint, title string, start time.Time, rule string) eventJSON {
	t.Helper()
	body := map[string]any{
		"title": title, "startsLocal": start.Format("2006-01-02T15:04"),
		"durationMinutes": 60, "tzid": "UTC",
	}
	if rule != "" {
		body["recurrenceRule"] = rule
	}
	w := c.do("POST", "/api/v1/groups/"+itoa(gid)+"/events", body, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create event %s: %d %s", title, w.Code, w.Body.String())
	}
	var created struct {
		Event eventJSON `json:"event"`
	}
	decodeJSON(t, w, &created)
	return created.Event
}

func fetchFeed(t *testing.T, c *apiClient, path string) *httptest.ResponseRecorder {
	t.Helper()
	return fetchFeedIf(t, c, path, "")
}

func fetchFeedIf(t *testing.T, c *apiClient, path, ifNoneMatch string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	w := httptest.NewRecorder()
	c.router.ServeHTTP(w, req)
	return w
}

func feedBody(t *testing.T, c *apiClient, path string) string {
	t.Helper()
	w := fetchFeed(t, c, path)
	if w.Code != http.StatusOK {
		t.Fatalf("fetch %s: %d %s", path, w.Code, w.Body.String())
	}
	return w.Body.String()
}

func feedUIDs(body string) []string {
	var out []string
	for _, line := range strings.Split(unfoldFeed(body), "\r\n") {
		if strings.HasPrefix(line, "UID:") {
			out = append(out, line)
		}
	}
	return out
}

func feedEventWithUIDPrefix(body, prefix string) string {
	for _, part := range strings.Split(unfoldFeed(body), "BEGIN:VEVENT\r\n")[1:] {
		block := "BEGIN:VEVENT\r\n" + strings.SplitN(part, "END:VEVENT\r\n", 2)[0] + "END:VEVENT\r\n"
		if strings.Contains(block, "\r\nUID:"+prefix) {
			return block
		}
	}
	return ""
}

// createFeedLink makes the signed-in client's calendar link and returns
// the feed path.
func createFeedLink(t *testing.T, c *apiClient, scope string) string {
	t.Helper()
	w := c.do("POST", "/api/v1/me/calendar-feed", map[string]any{"scope": scope}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("create calendar feed: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		CalendarFeed calendarFeedJSON `json:"calendarFeed"`
	}
	decodeJSON(t, w, &body)
	if !body.CalendarFeed.HasToken || body.CalendarFeed.URL == "" {
		t.Fatalf("create response has no one-time url: %+v", body.CalendarFeed)
	}
	i := strings.Index(body.CalendarFeed.URL, "/cal/")
	if i < 0 {
		t.Fatalf("feed url %q has no /cal/ path", body.CalendarFeed.URL)
	}
	return body.CalendarFeed.URL[i:]
}

func TestPublicFeedVisibilityAndCaching(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	start := feedStart(7)

	open := createFeedGroup(t, admin, "Open Choir", models.VisibilityPublic, "Hall")
	createFeedEvent(t, admin, open, "Open Rehearsal", start, "")
	priv := createFeedGroup(t, admin, "Staff Circle", models.VisibilityPrivate, "Office")
	createFeedEvent(t, admin, priv, "Members Night", start, "")
	hidden := createFeedGroup(t, admin, "Hidden Circle", models.VisibilityInviteOnly, "Secret barn")
	createFeedEvent(t, admin, hidden, "Hidden Night", start, "")
	opened := createFeedEvent(t, admin, hidden, "Opened Night", start.AddDate(0, 0, 1), "")
	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d/visibility", opened.ID),
		map[string]any{"visibility": "public"}, http.StatusOK)

	w := admin.do("POST", "/api/v1/events", map[string]any{
		"title": "Church Picnic", "startsLocal": start.Format("2006-01-02T15:04"),
		"durationMinutes": 60, "tzid": "UTC",
	}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create church event: %d %s", w.Code, w.Body.String())
	}

	anon := newAPIClient(t, app)
	resp := fetchFeed(t, anon, "/cal/public.ics")
	if resp.Code != http.StatusOK {
		t.Fatalf("public feed: %d %s", resp.Code, resp.Body.String())
	}
	body := resp.Body.String()
	for _, want := range []string{"Open Rehearsal", "Church Picnic", "Opened Night"} {
		if !strings.Contains(body, want) {
			t.Fatalf("public feed is missing %q:\n%s", want, body)
		}
	}
	for _, banned := range []string{"Members Night", "Hidden Night", "Hidden Circle", "Secret barn", "Staff Circle", "ATTENDEE"} {
		if strings.Contains(body, banned) {
			t.Fatalf("public feed leaks %q:\n%s", banned, body)
		}
	}
	if got := resp.Header().Get("Cache-Control"); got != "private, max-age=900" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := resp.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/calendar") {
		t.Fatalf("Content-Type = %q", got)
	}
	etag := resp.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on the feed")
	}
	again := fetchFeedIf(t, anon, "/cal/public.ics", etag)
	if again.Code != http.StatusNotModified || again.Body.Len() != 0 {
		t.Fatalf("If-None-Match: %d with %d body bytes, want 304 and none", again.Code, again.Body.Len())
	}
}

func TestGroupFeedOnlyForPublicGroups(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	start := feedStart(7)

	open := createFeedGroup(t, admin, "Open Choir", models.VisibilityPublic, "Hall")
	createFeedEvent(t, admin, open, "Open Rehearsal", start, "")
	quiet := createFeedEvent(t, admin, open, "Quiet Rehearsal", start.AddDate(0, 0, 2), "")
	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d/visibility", quiet.ID),
		map[string]any{"visibility": "private"}, http.StatusOK)
	priv := createFeedGroup(t, admin, "Staff Circle", models.VisibilityPrivate, "Office")
	hidden := createFeedGroup(t, admin, "Hidden Circle", models.VisibilityInviteOnly, "Barn")

	anon := newAPIClient(t, app)
	body := feedBody(t, anon, fmt.Sprintf("/cal/groups/%d.ics", open))
	if !strings.Contains(body, "Open Rehearsal") || !strings.Contains(body, "Open Choir") {
		t.Fatalf("group feed should carry the public event and the group name:\n%s", body)
	}
	if strings.Contains(body, "Quiet Rehearsal") {
		t.Fatalf("group feed carries a private event:\n%s", body)
	}
	for _, gid := range []uint{priv, hidden} {
		if w := fetchFeed(t, anon, fmt.Sprintf("/cal/groups/%d.ics", gid)); w.Code != http.StatusNotFound {
			t.Fatalf("group %d feed: %d, want 404", gid, w.Code)
		}
	}
	if w := fetchFeed(t, anon, "/cal/groups/999999.ics"); w.Code != http.StatusNotFound {
		t.Fatalf("missing group feed: %d, want 404", w.Code)
	}
}

func TestPersonalFeedScopes(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	start := feedStart(7)

	choir := createFeedGroup(t, admin, "Sam Choir", models.VisibilityPublic, "Hall")
	createFeedEvent(t, admin, choir, "Choir Series", start, weeklyRule(start)+";COUNT=4")
	var choirGroup models.Group
	if err := app.DB.First(&choirGroup, choir).Error; err != nil {
		t.Fatal(err)
	}
	_, sam := mkUser(t, app, &choirGroup, "sam")

	midStart := start.AddDate(0, 0, 1)
	w := admin.do("POST", "/api/v1/events", map[string]any{
		"title": "Midweek Series", "startsLocal": midStart.Format("2006-01-02T15:04"),
		"durationMinutes": 60, "tzid": "UTC",
		"recurrenceRule": weeklyRule(midStart) + ";COUNT=3",
	}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create midweek series: %d %s", w.Code, w.Body.String())
	}
	var midCreated struct {
		Event eventJSON `json:"event"`
	}
	decodeJSON(t, w, &midCreated)
	mid := midCreated.Event
	goingDate := feedDate(midStart.AddDate(0, 0, 7))
	apiDo(t, sam, "PUT", fmt.Sprintf("/api/v1/events/%d/registrations", mid.ID),
		map[string]any{"status": "going", "date": goingDate}, http.StatusOK)

	staff := createFeedGroup(t, admin, "Staff Circle", models.VisibilityPrivate, "Office")
	createFeedEvent(t, admin, staff, "Staff Night", start, "")

	anon := newAPIClient(t, app)
	path := createFeedLink(t, sam, "mine")
	body := feedBody(t, anon, path)
	if !strings.Contains(body, "Choir Series") {
		t.Fatalf("mine feed is missing the member's own series:\n%s", body)
	}
	if strings.Contains(body, "RRULE:") {
		t.Fatalf("personal feed must expand dates so an RRULE cannot reintroduce exclusions:\n%s", body)
	}
	if got := strings.Count(body, "SUMMARY:Choir Series"); got != 4 {
		t.Fatalf("mine feed has %d choir dates, want 4 expanded dates:\n%s", got, body)
	}
	if got := strings.Count(body, "STATUS:TENTATIVE"); got < 4 {
		t.Fatalf("unanswered own-group dates should be tentative:\n%s", body)
	}
	goingUID := fmt.Sprintf("UID:event-%d-%s@", mid.ID, strings.ReplaceAll(goingDate, "-", ""))
	if !strings.Contains(body, goingUID) || !strings.Contains(body, "STATUS:CONFIRMED") {
		t.Fatalf("mine feed is missing the one going date as a single:\n%s", body)
	}
	// Church-wide dates are standing own scope. The unanswered dates inside
	// 30 days are present as tentative rather than being conflated with Going.
	for _, other := range []string{feedDate(midStart), feedDate(midStart.AddDate(0, 0, 14))} {
		uid := fmt.Sprintf("UID:event-%d-%s@", mid.ID, strings.ReplaceAll(other, "-", ""))
		if !strings.Contains(body, uid) {
			t.Fatalf("mine feed is missing unanswered church-wide date %s:\n%s", other, body)
		}
	}
	if strings.Contains(body, "Staff Night") {
		t.Fatalf("mine feed carries an event sam has no tie to:\n%s", body)
	}

	apiDo(t, sam, "PUT", "/api/v1/me/calendar-feed", map[string]any{"scope": "all"}, http.StatusOK)
	body = feedBody(t, anon, path)
	if !strings.Contains(body, "Staff Night") {
		t.Fatalf("all feed is missing a private event sam can see:\n%s", body)
	}
	if strings.Contains(body, "RRULE:") {
		t.Fatalf("all personal feed should also be recurrence-safe expanded dates:\n%s", body)
	}
	if got := strings.Count(body, goingUID); got != 1 {
		t.Fatalf("all feed should contain the answered date exactly once, got %d:\n%s", got, body)
	}
}

func TestPersonalFeedScopeAndThirtyDayWindow(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	newTestUser(t, app, "feedwindow", "password1234", models.RoleMember)
	viewer := loginAPI(t, app, "feedwindow", "password1234")
	near, far := feedStart(7), feedStart(31)
	gid := createFeedGroup(t, admin, "Feed discovery", models.VisibilityPublic, "")
	createFeedEvent(t, admin, gid, "Near public feed", near, "")
	createFeedEvent(t, admin, gid, "Far public feed", far, "")
	answered := createFeedEvent(t, admin, gid, "Far answered feed", far.Add(time.Hour), "")
	apiDo(t, viewer, "PUT", fmt.Sprintf("/api/v1/events/%d/registrations", answered.ID), map[string]any{"status": "maybe", "date": feedDate(far)}, http.StatusOK)
	church := func(title string, start time.Time) {
		w := admin.do("POST", "/api/v1/events", map[string]any{"title": title, "startsLocal": start.Format("2006-01-02T15:04"), "durationMinutes": 60, "tzid": "UTC"}, true)
		if w.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", title, w.Code, w.Body.String())
		}
	}
	church("Near church feed", near.Add(2*time.Hour))
	church("Far church feed", far.Add(2*time.Hour))

	anon := newAPIClient(t, app)
	path := createFeedLink(t, viewer, models.CalendarFeedMine)
	mine := feedBody(t, anon, path)
	for _, title := range []string{"Far answered feed", "Near church feed"} {
		if !strings.Contains(mine, title) {
			t.Fatalf("mine missing %q:\n%s", title, mine)
		}
	}
	for _, title := range []string{"Near public feed", "Far public feed", "Far church feed"} {
		if strings.Contains(mine, title) {
			t.Fatalf("mine kept %q:\n%s", title, mine)
		}
	}
	apiDo(t, viewer, "PUT", "/api/v1/me/calendar-feed", map[string]any{"scope": models.CalendarFeedAll}, http.StatusOK)
	all := feedBody(t, anon, path)
	for _, title := range []string{"Near public feed", "Far answered feed", "Near church feed"} {
		if !strings.Contains(all, title) {
			t.Fatalf("all missing %q:\n%s", title, all)
		}
	}
	for _, title := range []string{"Far public feed", "Far church feed"} {
		if strings.Contains(all, title) {
			t.Fatalf("all kept %q:\n%s", title, all)
		}
	}
}

func TestPersonalFeedOpenedHiddenSeriesDateStaysStandaloneAndPrivate(t *testing.T) {
	app, admin, _, ev, dates := dateVisibilityFixture(t, models.VisibilityInviteOnly)
	viewer := newTestUser(t, app, "opened date viewer", "password1234", models.RoleMember)
	client := loginAPI(t, app, viewer.Name, "password1234")
	const hiddenDescription = "HIDDEN PARENT SERIES DESCRIPTION"
	const hiddenVideo = "https://meet.example.org/hidden-parent-video"
	if err := app.DB.Model(&models.Event{}).Where("id = ?", ev.ID).Updates(map[string]any{
		"title": "Hidden parent series", "description": hiddenDescription,
		"video_provider": models.VideoProviderLink, "video_url": hiddenVideo,
		"video_reveal": models.VideoRevealAlways, "remote_available": true,
		"childcare_available": true, "childcare_ages": "Hidden nursery ages",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Model(&dates[0]).Updates(map[string]any{
		"title_override": "Opened community date", "location_override": "Opened date place",
	}).Error; err != nil {
		t.Fatal(err)
	}
	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d/occurrences/%s", ev.ID, dates[0].OriginalDate), map[string]any{"visibility": "public"}, http.StatusOK)

	path := createFeedLink(t, client, models.CalendarFeedAll)
	dateUID := fmt.Sprintf("UID:event-%d-%s@", ev.ID, strings.ReplaceAll(dates[0].OriginalDate, "-", ""))
	assertDateOnly := func(scope, raw string) {
		t.Helper()
		if strings.Count(raw, "BEGIN:VEVENT") != 1 || !strings.Contains(raw, dateUID) || !strings.Contains(raw, "SUMMARY:Opened community date") || !strings.Contains(raw, "LOCATION:Opened date place") || !strings.Contains(raw, occurrenceDatePath(ev.ID, dates[0].OriginalDate)) {
			t.Fatalf("scope=%s did not export the one opened date as a standalone event: %s", scope, raw)
		}
		if !strings.Contains(raw, "Remote attendance available") {
			t.Fatalf("scope=%s suppressed authorized date participation information: %s", scope, raw)
		}
		for _, forbidden := range []string{
			"Childcare available", "Hidden nursery ages",
			fmt.Sprintf("UID:event-%d@", ev.ID), "RRULE:", "EXDATE", "RECURRENCE-ID",
			"Hidden fellowship", "SECRET GROUP PLACE", "Hidden parent series", hiddenDescription, hiddenVideo,
			strings.ReplaceAll(dates[1].OriginalDate, "-", ""), strings.ReplaceAll(dates[2].OriginalDate, "-", ""),
		} {
			if strings.Contains(raw, forbidden) {
				t.Fatalf("scope=%s leaked %q: %s", scope, forbidden, raw)
			}
		}
	}

	allBefore := unfoldFeed(feedBody(t, client, path))
	assertDateOnly("all before RSVP", allBefore)
	apiDo(t, client, "PUT", "/api/v1/me/calendar-feed", map[string]any{"scope": models.CalendarFeedMine}, http.StatusOK)
	mineBefore := unfoldFeed(feedBody(t, client, path))
	if strings.Contains(mineBefore, "BEGIN:VEVENT") {
		t.Fatalf("mine included an unanswered outsider date: %s", mineBefore)
	}

	apiDo(t, client, "PUT", fmt.Sprintf("/api/v1/events/%d/registrations", ev.ID), map[string]any{"status": models.RegGoing, "date": dates[0].OriginalDate}, http.StatusOK)
	mineAfter := unfoldFeed(feedBody(t, client, path))
	assertDateOnly("mine after RSVP", mineAfter)
	apiDo(t, client, "PUT", "/api/v1/me/calendar-feed", map[string]any{"scope": models.CalendarFeedAll}, http.StatusOK)
	allAfter := unfoldFeed(feedBody(t, client, path))
	assertDateOnly("all after RSVP", allAfter)
	if got, want := strings.Join(feedUIDs(allAfter), "\n"), strings.Join(feedUIDs(allBefore), "\n"); got != want {
		t.Fatalf("opened date identity changed after RSVP: before=%q after=%q", want, got)
	}

	var group models.Group
	if err := app.DB.First(&group, *ev.GroupID).Error; err != nil {
		t.Fatal(err)
	}
	addMember(t, app, viewer, &group, models.MembershipMember)
	ordinary := unfoldFeed(feedBody(t, client, path))
	if strings.Count(ordinary, dateUID) != 1 || strings.Count(ordinary, "BEGIN:VEVENT") != len(dates) || strings.Contains(ordinary, "RECURRENCE-ID") {
		t.Fatalf("date-only to series authority created a duplicate identity: %s", ordinary)
	}
	seen := map[string]bool{}
	for _, uid := range feedUIDs(ordinary) {
		if seen[uid] {
			t.Fatalf("ordinary series feed repeated %s: %s", uid, ordinary)
		}
		seen[uid] = true
	}
}

func TestPersonalFeedDateUIDSurvivesSeriesTimeEdit(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	start := feedStart(14)
	gid := createFeedGroup(t, admin, "Identity group", models.VisibilityPublic, "Hall")
	series := createFeedEvent(t, admin, gid, "Identity series", start, weeklyRule(start)+";COUNT=3")
	var group models.Group
	if err := app.DB.First(&group, gid).Error; err != nil {
		t.Fatal(err)
	}
	member, client := mkUser(t, app, &group, "identity member")
	var occs []models.EventOccurrence
	if err := app.DB.Where("event_id = ?", series.ID).Order("original_date").Find(&occs).Error; err != nil || len(occs) != 3 {
		t.Fatalf("occurrences: %v count=%d", err, len(occs))
	}
	target := occs[1]
	apiDo(t, client, "PUT", fmt.Sprintf("/api/v1/events/%d/registrations", series.ID), map[string]any{"status": models.RegGoing, "date": target.OriginalDate}, http.StatusOK)
	path := createFeedLink(t, client, models.CalendarFeedMine)
	before := unfoldFeed(feedBody(t, client, path))
	uidPrefix := fmt.Sprintf("event-%d-%s@", series.ID, strings.ReplaceAll(target.OriginalDate, "-", ""))
	beforeBlock := feedEventWithUIDPrefix(before, uidPrefix)
	if beforeBlock == "" || !strings.Contains(beforeBlock, "DTSTART;TZID=UTC:"+strings.ReplaceAll(target.OriginalDate, "-", "")+"T190000") || strings.Contains(before, "RECURRENCE-ID") {
		t.Fatalf("before edit identity/clock: %s", before)
	}
	beforeUIDs := strings.Join(feedUIDs(before), "\n")

	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d", series.ID), map[string]any{"startsLocal": feedDate(start) + "T20:00"}, http.StatusOK)
	after := unfoldFeed(feedBody(t, client, path))
	afterBlock := feedEventWithUIDPrefix(after, uidPrefix)
	if afterBlock == "" || !strings.Contains(afterBlock, "DTSTART;TZID=UTC:"+strings.ReplaceAll(target.OriginalDate, "-", "")+"T200000") || strings.Contains(after, "RECURRENCE-ID") {
		t.Fatalf("after edit identity/clock: %s", after)
	}
	if afterUIDs := strings.Join(feedUIDs(after), "\n"); afterUIDs != beforeUIDs {
		t.Fatalf("series time edit changed personal identities: before=%q after=%q", beforeUIDs, afterUIDs)
	}
	var moved models.EventOccurrence
	if err := app.DB.First(&moved, target.ID).Error; err != nil {
		t.Fatal(err)
	}
	if moved.ID != target.ID || moved.OriginalDate != target.OriginalDate || !moved.StartsAt.Equal(target.StartsAt.Add(time.Hour)) {
		t.Fatalf("series edit did not preserve occurrence identity: before=%+v after=%+v", target, moved)
	}
	var reg models.Registration
	if err := app.DB.Where("user_id = ? AND occurrence_id = ?", member.ID, target.ID).First(&reg).Error; err != nil || reg.Status != models.RegGoing {
		t.Fatalf("registration did not stay on the preserved occurrence: %+v err=%v", reg, err)
	}
	public := unfoldFeed(feedBody(t, newAPIClient(t, app), "/cal/public.ics"))
	if publicBlock := feedEventWithUIDPrefix(public, uidPrefix); publicBlock == "" || !strings.Contains(publicBlock, "DTSTART;TZID=UTC:"+strings.ReplaceAll(target.OriginalDate, "-", "")+"T200000") || strings.Contains(public, "RRULE:") {
		t.Fatalf("public date did not keep the same identity and edited clock: %s", public)
	}
}

func TestPersonalFeedMixedRSVPStatesAreRecurrenceSafe(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	start := feedStart(7)
	gid := createFeedGroup(t, admin, "State Choir", models.VisibilityPublic, "Hall")
	series := createFeedEvent(t, admin, gid, "State Series", start, weeklyRule(start)+";COUNT=5")
	var group models.Group
	if err := app.DB.First(&group, gid).Error; err != nil {
		t.Fatal(err)
	}
	user, client := mkUser(t, app, &group, "feedstates")
	var occs []models.EventOccurrence
	if err := app.DB.Where("event_id = ?", series.ID).Order("starts_at").Find(&occs).Error; err != nil || len(occs) < 5 {
		t.Fatalf("occurrences: %v %d", err, len(occs))
	}
	for i, status := range []string{models.RegGoing, models.RegMaybe, models.RegWaitlisted, models.RegDeclined} {
		uid := user.ID
		if err := app.DB.Create(&models.Registration{OccurrenceID: occs[i].ID, UserID: &uid, Status: status, AttendanceMode: models.AttendanceInPerson, Source: "web"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	anon := newAPIClient(t, app)
	path := createFeedLink(t, client, models.CalendarFeedMine)
	first := fetchFeed(t, anon, path)
	if first.Code != http.StatusOK {
		t.Fatalf("first feed: %d %s", first.Code, first.Body.String())
	}
	body := first.Body.String()
	if strings.Contains(body, "RRULE:") {
		t.Fatalf("personal RRULE could restore an omitted date:\n%s", body)
	}
	if got := strings.Count(body, "SUMMARY:State Series"); got != 4 { // Going, Maybe, Waitlisted, Not answered; declined omitted.
		t.Fatalf("got %d included dates, want 4:\n%s", got, body)
	}
	if got := strings.Count(body, "STATUS:CONFIRMED"); got != 1 {
		t.Fatalf("confirmed=%d:\n%s", got, body)
	}
	if got := strings.Count(body, "STATUS:TENTATIVE"); got != 3 {
		t.Fatalf("tentative=%d:\n%s", got, body)
	}
	declinedUID := fmt.Sprintf("UID:event-%d-%s@", series.ID, strings.ReplaceAll(occs[3].OriginalDate, "-", ""))
	if strings.Contains(body, declinedUID) {
		t.Fatalf("Can't go date remained in feed:\n%s", body)
	}
	unansweredUID := fmt.Sprintf("UID:event-%d-%s@", series.ID, strings.ReplaceAll(occs[4].OriginalDate, "-", ""))
	if !strings.Contains(body, unansweredUID) {
		t.Fatalf("own-group Not answered date missing:\n%s", body)
	}

	// Existing subscriptions update in place: changing one answer changes the
	// ETag and STATUS while retaining the immutable date-specific UID.
	etag := first.Header().Get("ETag")
	apiDo(t, client, "PUT", fmt.Sprintf("/api/v1/events/%d/registrations", series.ID), map[string]any{"status": "maybe", "date": occs[0].OriginalDate}, http.StatusOK)
	updated := fetchFeedIf(t, anon, path, etag)
	if updated.Code != http.StatusOK || updated.Header().Get("ETag") == etag {
		t.Fatalf("updated feed cache response: %d old=%q new=%q", updated.Code, etag, updated.Header().Get("ETag"))
	}
	updatedBody := updated.Body.String()
	firstUID := fmt.Sprintf("UID:event-%d-%s@", series.ID, strings.ReplaceAll(occs[0].OriginalDate, "-", ""))
	if !strings.Contains(updatedBody, firstUID) || strings.Contains(updatedBody, "RECURRENCE-ID") || strings.Count(updatedBody, "STATUS:CONFIRMED") != 0 || strings.Count(updatedBody, "STATUS:TENTATIVE") != 4 {
		t.Fatalf("updated subscription lost identity or status:\n%s", updatedBody)
	}
}

func TestPersonalFeedDropsGroupAfterLeaving(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	inner := createFeedGroup(t, admin, "Inner Circle", models.VisibilityInviteOnly, "Barn")
	createFeedEvent(t, admin, inner, "Inner Night", feedStart(7), "")
	var g models.Group
	if err := app.DB.First(&g, inner).Error; err != nil {
		t.Fatal(err)
	}
	_, sam := mkUser(t, app, &g, "sam")

	anon := newAPIClient(t, app)
	path := createFeedLink(t, sam, "mine")
	if body := feedBody(t, anon, path); !strings.Contains(body, "Inner Night") {
		t.Fatalf("member feed is missing the invite-only event:\n%s", body)
	}
	if w := confirmDeparture(t, sam, "POST", "/api/v1/groups/"+itoa(inner)+"/leave", "/api/v1/groups/"+itoa(inner)+"/departure-preview", ""); w.Code != http.StatusOK {
		t.Fatalf("leave: %d %s", w.Code, w.Body.String())
	}
	if body := feedBody(t, anon, path); strings.Contains(body, "Inner Night") {
		t.Fatalf("feed still carries the group after leaving:\n%s", body)
	}
}

func TestCalendarTokenLifecycle(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	samUser := newTestUser(t, app, "sam", "password1234", models.RoleMember)
	sam := loginAPI(t, app, "sam", "password1234")
	newTestUser(t, app, "lee", "password1234", models.RoleMember)
	leeClient := loginAPI(t, app, "lee", "password1234")
	anon := newAPIClient(t, app)

	if w := fetchFeed(t, anon, "/cal/u/no-such-token.ics"); w.Code != http.StatusNotFound {
		t.Fatalf("unknown token: %d, want 404", w.Code)
	}
	if w := sam.do("PUT", "/api/v1/me/calendar-feed", map[string]any{"scope": "all"}, true); w.Code != http.StatusNotFound {
		t.Fatalf("PUT with no token: %d %s, want 404", w.Code, w.Body.String())
	}
	w := sam.do("POST", "/api/v1/me/calendar-feed", map[string]any{"scope": "everything"}, true)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "invalid_feed_scope") {
		t.Fatalf("bad scope: %d %s, want 400 invalid_feed_scope", w.Code, w.Body.String())
	}

	path := createFeedLink(t, sam, "mine")
	raw := strings.TrimSuffix(strings.TrimPrefix(path, "/cal/u/"), ".ics")
	if got := feedBody(t, anon, path); !strings.Contains(got, "BEGIN:VCALENDAR") {
		t.Fatalf("fresh link does not serve a calendar:\n%s", got)
	}
	w = sam.do("GET", "/api/v1/me/calendar-feed", nil, false)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), raw) {
		t.Fatalf("GET leaks the raw token: %d %s", w.Code, w.Body.String())
	}
	var got struct {
		CalendarFeed calendarFeedJSON `json:"calendarFeed"`
	}
	decodeJSON(t, w, &got)
	if !got.CalendarFeed.HasToken || got.CalendarFeed.Scope != "mine" || got.CalendarFeed.URL != "" {
		t.Fatalf("GET after create: %+v", got.CalendarFeed)
	}

	newPath := createFeedLink(t, sam, "mine")
	if newPath == path {
		t.Fatal("regenerating returned the same link")
	}
	if w := fetchFeed(t, anon, path); w.Code != http.StatusNotFound {
		t.Fatalf("old link after regenerate: %d, want 404", w.Code)
	}
	if w := fetchFeed(t, anon, newPath); w.Code != http.StatusOK {
		t.Fatalf("new link after regenerate: %d", w.Code)
	}

	// A disabled owner's link dies with the same 404 as a bad token.
	if err := app.DB.Model(&models.User{}).Where("id = ?", samUser.ID).
		Update("status", models.UserDisabled).Error; err != nil {
		t.Fatal(err)
	}
	if w := fetchFeed(t, anon, newPath); w.Code != http.StatusNotFound {
		t.Fatalf("disabled owner's link: %d, want 404", w.Code)
	}
	if err := app.DB.Model(&models.User{}).Where("id = ?", samUser.ID).
		Update("status", models.UserActive).Error; err != nil {
		t.Fatal(err)
	}

	// Admins revoke anyone's link; doing it twice is fine; members can't.
	if body := apiDo(t, admin, "DELETE", fmt.Sprintf("/api/v1/admin/users/%d/calendar-feed", samUser.ID), nil, http.StatusOK); !strings.Contains(body, `"hadLink":true`) {
		t.Fatalf("first admin revoke should report hadLink true: %s", body)
	}
	if w := fetchFeed(t, anon, newPath); w.Code != http.StatusNotFound {
		t.Fatalf("link after admin revoke: %d, want 404", w.Code)
	}
	if body := apiDo(t, admin, "DELETE", fmt.Sprintf("/api/v1/admin/users/%d/calendar-feed", samUser.ID), nil, http.StatusOK); !strings.Contains(body, `"hadLink":false`) {
		t.Fatalf("second admin revoke should report hadLink false: %s", body)
	}
	apiDo(t, admin, "DELETE", "/api/v1/admin/users/nope/calendar-feed", nil, http.StatusNotFound)
	if w := leeClient.do("DELETE", fmt.Sprintf("/api/v1/admin/users/%d/calendar-feed", samUser.ID), nil, true); w.Code != http.StatusForbidden {
		t.Fatalf("member at the admin revoke: %d, want 403", w.Code)
	}

	third := createFeedLink(t, sam, "all")
	apiDo(t, sam, "DELETE", "/api/v1/me/calendar-feed", nil, http.StatusOK)
	if w := fetchFeed(t, anon, third); w.Code != http.StatusNotFound {
		t.Fatalf("link after own revoke: %d, want 404", w.Code)
	}
	w = sam.do("GET", "/api/v1/me/calendar-feed", nil, false)
	decodeJSON(t, w, &got)
	if got.CalendarFeed.HasToken {
		t.Fatalf("GET after revoke still claims a token: %+v", got.CalendarFeed)
	}
}

func TestFeedRecurringAcceptance(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	start := feedStart(7)
	open := createFeedGroup(t, admin, "Open Choir", models.VisibilityPublic, "Hall")
	series := createFeedEvent(t, admin, open, "Weekly Practice", start, weeklyRule(start)+";COUNT=5")

	cancelled := feedDate(start.AddDate(0, 0, 7))
	apiDo(t, admin, "POST", fmt.Sprintf("/api/v1/events/%d/cancel", series.ID),
		map[string]any{"date": cancelled}, http.StatusOK)
	moved := feedDate(start.AddDate(0, 0, 14))
	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d/occurrences/%s", series.ID, moved),
		map[string]any{"title": "Special Rehearsal", "startsLocal": moved + "T21:00"}, http.StatusOK)

	anon := newAPIClient(t, app)
	for _, path := range []string{"/cal/public.ics", fmt.Sprintf("/cal/groups/%d.ics", open)} {
		body := unfoldFeed(feedBody(t, anon, path))
		parsed, err := ics.ParseCalendar(strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if len(parsed.Events()) != 4 || strings.Contains(body, strings.ReplaceAll(cancelled, "-", "")+"T190000") {
			t.Fatalf("%s did not omit exactly the cancelled date: %s", path, body)
		}
		for _, event := range parsed.Events() {
			for _, p := range []ics.ComponentProperty{ics.ComponentPropertyRrule, ics.ComponentPropertyExdate, ics.ComponentPropertyRecurrenceId} {
				if event.GetProperty(p) != nil {
					t.Fatalf("%s retained recurrence property %s", path, p)
				}
			}
		}
		uid := fmt.Sprintf("event-%d-%s@", series.ID, strings.ReplaceAll(moved, "-", ""))
		block := feedEventWithUIDPrefix(body, uid)
		if !strings.Contains(block, "SUMMARY:Special Rehearsal") || !strings.Contains(block, "DTSTART;TZID=UTC:"+strings.ReplaceAll(moved, "-", "")+"T210000") {
			t.Fatalf("%s missing moved date with stable identity: %s", path, body)
		}
	}
	// The next subscription snapshot removes cancelled first, last and all
	// remaining dates rather than reconstructing any from a series master.
	for i := 0; i < 5; i++ {
		if i == 1 {
			continue
		}
		apiDo(t, admin, "POST", fmt.Sprintf("/api/v1/events/%d/cancel", series.ID), map[string]any{"date": feedDate(start.AddDate(0, 0, 7*i))}, http.StatusOK)
	}
	for _, path := range []string{"/cal/public.ics", fmt.Sprintf("/cal/groups/%d.ics", open)} {
		if body := feedBody(t, anon, path); strings.Contains(body, "BEGIN:VEVENT") {
			t.Fatalf("%s reconstructed cancelled dates: %s", path, body)
		}
	}
}

// unfoldFeed joins ICS folded lines so a long URL can be searched whole.
func unfoldFeed(body string) string { return strings.ReplaceAll(body, "\r\n ", "") }

// Video links in feeds (5.8, M1.8): the public feeds carry an "always"
// link only. A personal feed carries a registered_only link for a date
// its owner is going to — not for a waitlisted date, and not at all for
// someone merely looking.
func TestFeedVideoLinkReveal(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	start := feedStart(7)

	const alwaysLink = "https://meet.example.org/always-room"
	const quietLink = "https://meet.example.org/quiet-room"
	gid := createFeedGroup(t, admin, "Video Choir", models.VisibilityPublic, "Hall")
	open := createFeedEvent(t, admin, gid, "Open Video Night", start, "")
	quiet := createFeedEvent(t, admin, gid, "Quiet Video Night", start.AddDate(0, 0, 1), "")
	setVideo := func(id uint, reveal, link string, extra map[string]any) {
		updates := map[string]any{
			"video_provider": models.VideoProviderLink, "video_url": link, "video_reveal": reveal,
		}
		for k, v := range extra {
			updates[k] = v
		}
		if err := app.DB.Model(&models.Event{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			t.Fatal(err)
		}
	}
	setVideo(open.ID, models.VideoRevealAlways, alwaysLink, nil)
	setVideo(quiet.ID, models.VideoRevealRegisteredOnly, quietLink,
		map[string]any{"capacity": 1, "waitlist_enabled": true})

	anon := newAPIClient(t, app)
	for _, path := range []string{"/cal/public.ics", fmt.Sprintf("/cal/groups/%d.ics", gid)} {
		body := unfoldFeed(feedBody(t, anon, path))
		if !strings.Contains(body, "Join by video: "+alwaysLink) {
			t.Fatalf("%s is missing the always link:\n%s", path, body)
		}
		if strings.Contains(body, quietLink) {
			t.Fatalf("%s leaks the registered-only link:\n%s", path, body)
		}
	}

	mk := func(name string) *apiClient {
		newTestUser(t, app, name, "password1234", models.RoleMember)
		return loginAPI(t, app, name, "password1234")
	}
	quietDate := feedDate(start.AddDate(0, 0, 1))
	going := mk("fgoing")
	if code, body := registerAs(going, quiet.ID, quietDate, "going", nil); code != http.StatusOK {
		t.Fatalf("going register: %d %s", code, body)
	}
	waiting := mk("fwaiting")
	if code, body := registerAs(waiting, quiet.ID, quietDate, "going", nil); code != http.StatusOK {
		t.Fatalf("waitlist register: %d %s", code, body)
	}
	looking := mk("flooking")

	// Going: the standalone date in a "mine" feed carries the link.
	body := unfoldFeed(feedBody(t, going, createFeedLink(t, going, models.CalendarFeedMine)))
	if !strings.Contains(body, "Join by video: "+quietLink) {
		t.Fatalf("going person's feed is missing the link:\n%s", body)
	}
	// Waitlisted: the date is there (TENTATIVE) but the link is not.
	body = unfoldFeed(feedBody(t, waiting, createFeedLink(t, waiting, models.CalendarFeedMine)))
	if !strings.Contains(body, "Quiet Video Night") || !strings.Contains(body, "TENTATIVE") {
		t.Fatalf("waitlisted feed should carry the date as tentative:\n%s", body)
	}
	if strings.Contains(body, quietLink) {
		t.Fatalf("waitlisted feed leaks the link:\n%s", body)
	}
	// Looking with "all I can see": the series is there, the quiet link
	// is not, the always link is.
	body = unfoldFeed(feedBody(t, looking, createFeedLink(t, looking, models.CalendarFeedAll)))
	if strings.Contains(body, quietLink) {
		t.Fatalf("a bystander's feed leaks the link:\n%s", body)
	}
	if !strings.Contains(body, "Join by video: "+alwaysLink) {
		t.Fatalf("an all-scope feed is missing the always link:\n%s", body)
	}
	// The leader's own feed carries it (they run the event).
	body = unfoldFeed(feedBody(t, admin, createFeedLink(t, admin, models.CalendarFeedAll)))
	if !strings.Contains(body, "Join by video: "+quietLink) {
		t.Fatalf("leader's feed is missing the link:\n%s", body)
	}
}

// A remote event's feed description says remote attendance is
// available (8.2, M1.8 slice 2); an in-person-only event never does.
func TestFeedMentionsRemoteAttendance(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	start := feedStart(7)
	gid := createFeedGroup(t, admin, "Feed Remote Choir", models.VisibilityPublic, "Hall")
	hybrid := createFeedEvent(t, admin, gid, "Hybrid Feed Night", start, "")
	createFeedEvent(t, admin, gid, "Hall Only Night", start.AddDate(0, 0, 1), "")
	if err := app.DB.Model(&models.Event{}).Where("id = ?", hybrid.ID).
		Update("remote_available", true).Error; err != nil {
		t.Fatal(err)
	}
	body := unfoldFeed(feedBody(t, newAPIClient(t, app), "/cal/public.ics"))
	if got := strings.Count(body, "Remote attendance available"); got != 1 {
		t.Fatalf("remote mentions = %d, want 1 (only the hybrid event):\n%s", got, body)
	}
}

// DEC-70: in a personal feed a registered_only link also reaches the
// owner when they said maybe, or when they are in the event's group and
// haven't answered. Declined, waitlisted, and outsiders who haven't
// answered still get nothing. A member going to one date of a series
// gets that date with the link in either scope; unanswered sibling dates
// in "all" must not inherit that date's video eligibility.
func TestFeedVideoLinkMaybeNoResponseAndSeriesDate(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	start := feedStart(7)
	const link = "https://meet.example.org/series-room"
	gid := createFeedGroup(t, admin, "Series Video", models.VisibilityPublic, "Hall")
	ev := createFeedEvent(t, admin, gid, "Weekly Video Study", start, "FREQ=WEEKLY;COUNT=3")
	if err := app.DB.Model(&models.Event{}).Where("id = ?", ev.ID).Updates(map[string]any{
		"video_provider": models.VideoProviderLink, "video_url": link,
		"video_reveal": models.VideoRevealRegisteredOnly,
	}).Error; err != nil {
		t.Fatal(err)
	}
	var g models.Group
	app.DB.First(&g, gid)
	dates := []string{feedDate(start), feedDate(start.AddDate(0, 0, 7)), feedDate(start.AddDate(0, 0, 14))}
	mk := func(name string, member bool) *apiClient {
		u := newTestUser(t, app, name, "password1234", models.RoleMember)
		if member {
			addMember(t, app, u, &g, models.MembershipMember)
		}
		return loginAPI(t, app, name, "password1234")
	}
	has := func(c *apiClient, scope string) bool {
		return strings.Contains(unfoldFeed(feedBody(t, c, createFeedLink(t, c, scope))), link)
	}

	silentMember := mk("vsilent", true)
	if !has(silentMember, models.CalendarFeedAll) {
		t.Fatal("a member who hasn't answered should see the link")
	}
	maybeOutsider := mk("vmaybe", false)
	if code, body := registerAs(maybeOutsider, ev.ID, dates[0], "maybe", nil); code != http.StatusOK {
		t.Fatalf("maybe: %d %s", code, body)
	}
	if !has(maybeOutsider, models.CalendarFeedAll) {
		t.Fatal("a maybe should see the link")
	}
	silentOutsider := mk("voutsider", false)
	if has(silentOutsider, models.CalendarFeedAll) {
		t.Fatal("an outsider who hasn't answered must not see the link")
	}
	decliner := mk("vdecline", true)
	for _, d := range dates {
		if code, body := registerAs(decliner, ev.ID, d, "declined", nil); code != http.StatusOK {
			t.Fatalf("decline %s: %d %s", d, code, body)
		}
	}
	if has(decliner, models.CalendarFeedAll) {
		t.Fatal("someone who declined every date must not see the link")
	}

	// Going to one date: an outsider's "mine" feed has just that date,
	// with the link.
	oneDate := mk("vonedate", false)
	if code, body := registerAs(oneDate, ev.ID, dates[1], "going", nil); code != http.StatusOK {
		t.Fatalf("going: %d %s", code, body)
	}
	body := unfoldFeed(feedBody(t, oneDate, createFeedLink(t, oneDate, models.CalendarFeedMine)))
	if strings.Count(body, "BEGIN:VEVENT") != 1 || !strings.Contains(body, "Join by video: "+link) {
		t.Fatalf("one-date feed should carry one date with the link:\n%s", body)
	}
	all := unfoldFeed(feedBody(t, oneDate, createFeedLink(t, oneDate, models.CalendarFeedAll)))
	if strings.Count(all, "BEGIN:VEVENT") != 3 || strings.Count(all, "Join by video: "+link) != 1 {
		t.Fatalf("one qualifying date must not widen sibling video eligibility: %s", all)
	}
}

func TestFeedMentionsChildcareAndAges(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	start := feedStart(7)
	gid := createFeedGroup(t, admin, "Family Feed", models.VisibilityPublic, "Hall")
	offered := createFeedEvent(t, admin, gid, "Family Night", start, "")
	createFeedEvent(t, admin, gid, "Adults Night", start.AddDate(0, 0, 1), "")
	apiDo(t, admin, "PUT", "/api/v1/events/"+itoa(offered.ID), map[string]any{"childcareAvailable": true, "childcareAges": "Ages 2–5"}, http.StatusOK)
	for _, path := range []string{"/cal/public.ics", "/cal/groups/" + itoa(gid) + ".ics"} {
		body := unfoldFeed(feedBody(t, newAPIClient(t, app), path))
		if strings.Count(body, "Childcare available (Ages 2–5)") != 1 {
			t.Fatalf("%s childcare description: %s", path, body)
		}
	}
}

func TestHouseholdOnlyPersonalFeedKeepsDetailsPrivate(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "HouseholdFeed")
	ev, date := createVideoEvent(t, admin, gid, map[string]any{"remoteAvailable": true, "videoProvider": "link", "videoUrl": videoLink, "videoReveal": "registered_only"})
	_, c, spouse, child := householdFamily(t, app, admin, "feedholder")
	batchAnswer(t, c, ev.ID, date, map[string]any{"people": []map[string]any{{"personId": spouse.ID, "status": "going", "attendanceMode": "remote"}, {"personId": child.ID, "status": "going"}}}, http.StatusOK)
	path := createFeedLink(t, c, "mine")
	raw := fetchFeed(t, newAPIClient(t, app), path).Body.String()
	if !strings.Contains(raw, ev.Title) || !strings.Contains(raw, videoLink) {
		t.Fatalf("missing personal date/link %s", raw)
	}
	uid := fmt.Sprintf("event-%d-%s@", ev.ID, strings.ReplaceAll(date, "-", ""))
	if block := feedEventWithUIDPrefix(raw, uid); !strings.Contains(block, "STATUS:TENTATIVE\r\n") || strings.Contains(block, "STATUS:CONFIRMED") {
		t.Fatalf("household scope became the viewer's RSVP: %s", block)
	}
	for _, secret := range []string{"Mary", "Kit", "Private allergy note", "household", "2020-05-01"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("feed leak %s", raw)
		}
	}
}

func TestPersonalFeedScopedInvitationAlwaysStandalone(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	seedReadInvitation(t, f.app, f.issuer, f.other, f.event, f.dates[2])
	for _, scope := range []string{models.CalendarFeedMine, models.CalendarFeedAll} {
		path := createFeedLink(t, f.client, scope)
		raw := unfoldFeed(feedBody(t, f.client, path))
		if strings.Count(raw, "BEGIN:VEVENT") != 1 || !strings.Contains(raw, "SUMMARY:Invited date title") || !strings.Contains(raw, "LOCATION:Invited date place") || !strings.Contains(raw, occurrenceDatePath(f.event.ID, f.dates[1].OriginalDate)) || !strings.Contains(raw, "private-video") {
			t.Fatalf("wrong scoped feed (%s): %s", scope, raw)
		}
		for _, forbidden := range []string{"RRULE:", "EXDATE", "RECURRENCE-ID", "Hidden series", "Hidden group", f.issuer.Email, strings.ReplaceAll(f.dates[0].OriginalDate, "-", ""), strings.ReplaceAll(f.dates[2].OriginalDate, "-", "")} {
			if strings.Contains(raw, forbidden) {
				t.Fatalf("scope=%s feed leaked %q: %s", scope, forbidden, raw)
			}
		}
	}
	if err := f.app.DB.Model(&f.access).Updates(map[string]any{"status": "ended", "ended_at": time.Now().UTC(), "end_reason": "removed", "ended_by": f.issuer.ID}).Error; err != nil {
		t.Fatal(err)
	}
	gone := feedBody(t, f.client, createFeedLink(t, f.client, models.CalendarFeedAll))
	if strings.Contains(gone, "BEGIN:VEVENT") {
		t.Fatal("revoked invitation still exported", gone)
	}
	raw := feedBody(t, f.stranger, createFeedLink(t, f.stranger, models.CalendarFeedAll))
	if strings.Count(raw, "BEGIN:VEVENT") != 1 || !strings.Contains(raw, occurrenceDatePath(f.event.ID, f.dates[2].OriginalDate)) || strings.Contains(raw, "Invited date title") {
		t.Fatal("cross-recipient feed", raw)
	}
	public := feedBody(t, newAPIClient(t, f.app), "/cal/public.ics")
	if strings.Contains(public, "BEGIN:VEVENT") {
		t.Fatal("public invitation leak", public)
	}
	if w := fetchFeed(t, f.client, fmt.Sprintf("/cal/groups/%d.ics", f.group)); w.Code != 404 {
		t.Fatal("hidden group feed", w.Code, w.Body.String())
	}
}

func TestPublicAndGroupFeedsOpenedDateHidesSeriesDetails(t *testing.T) {
	for _, visibility := range []string{models.VisibilityPrivate, models.VisibilityInviteOnly} {
		t.Run(visibility, func(t *testing.T) {
			app, admin, anon, ev, dates := dateVisibilityFixture(t, models.VisibilityPublic)
			const description = "Hidden series description with enough words to cross an ICS folding boundary and stay secret"
			const video = "https://meet.example.org/hidden-series-video-with-a-long-path-that-must-never-reach-a-date-feed"
			const ages = "Hidden nursery ages"
			if err := app.DB.Model(&models.Event{}).Where("id = ?", ev.ID).Updates(map[string]any{
				"visibility": visibility, "description": description,
				"video_provider": models.VideoProviderLink, "video_url": video, "video_reveal": models.VideoRevealAlways,
				"childcare_available": true, "childcare_ages": ages, "remote_available": true,
			}).Error; err != nil {
				t.Fatal(err)
			}
			apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d/occurrences/%s", ev.ID, dates[0].OriginalDate), map[string]any{
				"visibility": "public", "title": "Opened community date", "location": "Opened date place",
			}, http.StatusOK)
			for _, path := range []string{"/cal/public.ics", fmt.Sprintf("/cal/groups/%d.ics", *ev.GroupID)} {
				raw := unfoldFeed(feedBody(t, anon, path))
				for _, want := range []string{"BEGIN:VEVENT", "SUMMARY:Opened community date", "LOCATION:Opened date place", "Remote attendance available", occurrenceDatePath(ev.ID, dates[0].OriginalDate), dates[0].StartsAt.UTC().Format("20060102T150405")} {
					if !strings.Contains(raw, want) {
						t.Fatalf("%s missing %q: %s", path, want, raw)
					}
				}
				if strings.Count(raw, "BEGIN:VEVENT") != 1 {
					t.Fatalf("%s exported sibling dates: %s", path, raw)
				}
				for _, secret := range []string{description, video, ages, "Childcare available", "RRULE:", "RECURRENCE-ID:"} {
					if strings.Contains(raw, secret) {
						t.Fatalf("%s leaked %q: %s", path, secret, raw)
					}
				}
			}
			// Ordinary series access still includes its description, always-video
			// and childcare details; projection is not blanket suppression.
			if err := app.DB.Model(&models.Event{}).Where("id = ?", ev.ID).Update("visibility", models.VisibilityPublic).Error; err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"/cal/public.ics", fmt.Sprintf("/cal/groups/%d.ics", *ev.GroupID)} {
				raw := unfoldFeed(feedBody(t, anon, path))
				for _, want := range []string{description, video, ages, "Childcare available"} {
					if !strings.Contains(raw, want) {
						t.Fatalf("%s suppressed authorized %q: %s", path, want, raw)
					}
				}
			}
		})
	}
}

func TestOpenedDateFeedsUseEffectiveRemoteAvailability(t *testing.T) {
	app, admin, anon, ev, dates := dateVisibilityFixture(t, models.VisibilityPublic)
	viewer := newTestUser(t, app, "remote date viewer", "password1234", models.RoleMember)
	client := loginAPI(t, app, viewer.Name, "password1234")
	personalPath := createFeedLink(t, client, models.CalendarFeedAll)
	if err := app.DB.Model(&models.Event{}).Where("id = ?", ev.ID).Update("visibility", models.VisibilityInviteOnly).Error; err != nil {
		t.Fatal(err)
	}
	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d/occurrences/%s", ev.ID, dates[0].OriginalDate), map[string]any{"visibility": "public"}, http.StatusOK)
	for _, tc := range []struct {
		name     string
		enabled  bool
		capacity *int
		override *int
		want     bool
	}{
		{"unlimited", true, nil, nil, true},
		{"positive capacity", true, intPtr(5), nil, true},
		{"zero capacity", true, intPtr(0), nil, false},
		{"positive override opens zero capacity", true, intPtr(0), intPtr(2), true},
		{"zero override closes unlimited capacity", true, nil, intPtr(0), false},
		{"positive override", true, intPtr(5), intPtr(2), true},
		{"cleared override returns to unlimited", true, nil, nil, true},
		{"disabled with positive override", false, intPtr(5), intPtr(2), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := app.DB.Model(&models.Event{}).Where("id = ?", ev.ID).Updates(map[string]any{"remote_available": tc.enabled, "remote_capacity": tc.capacity}).Error; err != nil {
				t.Fatal(err)
			}
			apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d/occurrences/%s", ev.ID, dates[0].OriginalDate), map[string]any{"remoteCapacityOverride": tc.override}, http.StatusOK)
			var body struct {
				Occurrence dateOccurrenceJSON `json:"occurrence"`
			}
			dateResponse := anon.do("GET", dateReadPath(ev.ID, dates[0].OriginalDate), nil, false)
			if dateResponse.Code != http.StatusOK {
				t.Fatalf("date API: %d %s", dateResponse.Code, dateResponse.Body.String())
			}
			decodeJSON(t, dateResponse, &body)
			if body.Occurrence.RemoteAvailable != tc.want {
				t.Fatalf("date API remoteAvailable = %t, want %t", body.Occurrence.RemoteAvailable, tc.want)
			}
			for _, path := range []string{"/cal/public.ics", fmt.Sprintf("/cal/groups/%d.ics", *ev.GroupID), personalPath} {
				raw := unfoldFeed(feedBody(t, anon, path))
				if strings.Count(raw, "BEGIN:VEVENT") != 1 || strings.Contains(raw, "RRULE:") {
					t.Fatalf("%s exported more than the opened date: %s", path, raw)
				}
				if got := strings.Contains(raw, "Remote attendance available"); got != tc.want {
					t.Errorf("%s remote marker = %t, want %t: %s", path, got, tc.want, raw)
				}
			}
		})
	}
}

func TestRecurringFeedsUseDateRemoteCapacityAndRefresh(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "Remote feed dates")
	start := feedStart(7)
	var group models.Group
	if err := app.DB.First(&group, gid).Error; err != nil {
		t.Fatal(err)
	}
	ev := createFeedEvent(t, admin, group.ID, "Hybrid dates", start, weeklyRule(start)+";COUNT=3")
	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d", ev.ID), map[string]any{
		"remoteAvailable": true, "remoteCapacity": 1, "videoProvider": "link", "videoUrl": "https://meet.example.org/date-feed", "videoReveal": "always",
	}, http.StatusOK)
	_, member := mkUser(t, app, &group, "remote feed member")
	personal := createFeedLink(t, member, models.CalendarFeedMine)
	var dates []models.EventOccurrence
	if err := app.DB.Where("event_id = ?", ev.ID).Order("original_date").Find(&dates).Error; err != nil || len(dates) != 3 {
		t.Fatalf("dates: %v count=%d", err, len(dates))
	}
	datePath := fmt.Sprintf("/api/v1/events/%d/occurrences/%s", ev.ID, dates[0].OriginalDate)
	apiDo(t, admin, "PUT", datePath, map[string]any{"remoteCapacityOverride": 0, "location": ""}, http.StatusOK)
	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d/occurrences/%s", ev.ID, dates[1].OriginalDate), map[string]any{"remoteCapacityOverride": 2}, http.StatusOK)
	// Fill the inherited positive pool: offered means configured, not free seats.
	if code, body := registerModeAs(member, ev.ID, dates[2].OriginalDate, models.RegGoing, models.AttendanceRemote); code != http.StatusOK {
		t.Fatalf("remote RSVP: %d %s", code, body)
	}
	anon := newAPIClient(t, app)
	paths := []string{"/cal/public.ics", fmt.Sprintf("/cal/groups/%d.ics", group.ID), personal}
	assertDates := func(wantZero bool) {
		t.Helper()
		for _, path := range paths {
			body := unfoldFeed(feedBody(t, anon, path))
			cal, err := ics.ParseCalendar(strings.NewReader(body))
			if err != nil || len(cal.Events()) != 3 {
				t.Fatalf("%s dates: %v %s", path, err, body)
			}
			for i, date := range dates {
				uid := fmt.Sprintf("event-%d-%s@", ev.ID, strings.ReplaceAll(date.OriginalDate, "-", ""))
				block := feedEventWithUIDPrefix(body, uid)
				if block == "" || strings.Contains(block, "RRULE:") {
					t.Fatalf("%s missing standalone date %s", path, uid)
				}
				if got, want := strings.Contains(block, "Remote attendance available"), i != 0 || !wantZero; got != want {
					t.Fatalf("%s date %d remote=%t want=%t: %s", path, i, got, want, block)
				}
				if !strings.Contains(block, "Join by video:") {
					t.Fatalf("%s zero capacity incorrectly hid authorized video: %s", path, block)
				}
				if i == 0 && strings.Contains(block, "LOCATION:") {
					t.Fatalf("%s restored an explicitly empty location: %s", path, block)
				}
			}
		}
	}
	assertDates(true)
	apiDo(t, member, "PUT", "/api/v1/me/calendar-feed", map[string]any{"scope": models.CalendarFeedAll}, http.StatusOK)
	assertDates(true)
	before := fetchFeed(t, anon, paths[0])
	if w := fetchFeedIf(t, anon, paths[0], before.Header().Get("ETag")); w.Code != http.StatusNotModified {
		t.Fatalf("unchanged feed: %d", w.Code)
	}
	apiDo(t, admin, "PUT", datePath, map[string]any{"remoteCapacityOverride": nil}, http.StatusOK)
	if w := fetchFeedIf(t, anon, paths[0], before.Header().Get("ETag")); w.Code != http.StatusOK || w.Header().Get("ETag") == before.Header().Get("ETag") {
		t.Fatalf("capacity reset did not refresh feed: %d", w.Code)
	}
	assertDates(false)
	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d", ev.ID), map[string]any{"remoteCapacity": nil}, http.StatusOK)
	assertDates(false)
}

func TestFeedDateWindowIgnoresSeriesAnchor(t *testing.T) {
	app := testApp(t)
	admin := newTestUser(t, app, "feed window admin", "pw", models.RoleAdmin)
	group := models.Group{Name: "Window group", Status: models.GroupActive, Visibility: models.VisibilityPublic, LeaderID: admin.ID}
	if err := app.DB.Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	from, until := feedWindow(now)
	ev, first := seedEvent(t, app, &group, admin.ID, from.Add(-time.Second))
	if err := app.DB.Model(&ev).Updates(map[string]any{"starts_local": "1980-01-01T19:00", "rrule": "FREQ=WEEKLY;BYDAY=FR"}).Error; err != nil {
		t.Fatal(err)
	}
	for i, start := range []time.Time{from, until.Add(-time.Second), until, now, now.Add(time.Hour), now.Add(2 * time.Hour)} {
		occ := models.EventOccurrence{EventID: ev.ID, OriginalDate: fmt.Sprintf("1980-01-%02d", i+2), StartsAt: start, EndsAt: start.Add(90 * time.Minute), DurationOverride: intPtr(90), Status: models.OccurrenceScheduled}
		if i == 3 {
			occ.Status = models.OccurrenceCancelled
		}
		if i == 4 {
			visibility := models.VisibilityInviteOnly
			occ.VisibilityOverride = &visibility
		}
		if err := app.DB.Create(&occ).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, opts := range []feedOptions{{}, {onlyGroupID: &group.ID}} {
		feed := mustBuildFeed(t, app, nil, opts, now)
		if len(feed.Singles) != 3 {
			t.Fatalf("window exported %d dates, want from, until-1s and moved-in date", len(feed.Singles))
		}
		for _, date := range feed.Singles {
			if date.OriginalDate == first.OriginalDate || date.StartsAt.Before(from) || !date.StartsAt.Before(until) || date.EndsAt.Sub(date.StartsAt) != 90*time.Minute {
				t.Fatalf("wrong effective date: %+v", date)
			}
		}
		raw := string(events.RenderFeed(feed))
		if strings.Contains(raw, "RRULE:") || strings.Contains(raw, "DTSTART;TZID=UTC:1980") || strings.Count(raw, "BEGIN:STANDARD") != 1 {
			t.Fatalf("series anchor leaked into snapshot: %s", raw)
		}
	}
}

func TestFeedDateProjectionReadBudget(t *testing.T) {
	app := testApp(t)
	admin := newTestUser(t, app, "feed budget admin", "pw", models.RoleAdmin)
	group := models.Group{Name: "Feed budget", Status: models.GroupActive, Visibility: models.VisibilityPublic, LeaderID: admin.ID}
	if err := app.DB.Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	var baseline int64
	for i := 1; i <= 100; i++ {
		seedEvent(t, app, &group, admin.ID, now.Add(time.Duration(i)*time.Hour))
		if i != 1 && i != 100 {
			continue
		}
		counter := countDBReads(t, app.DB)
		feed := mustBuildFeed(t, app, nil, feedOptions{}, now)
		reads := counter.Reads()
		counter.Stop()
		if len(feed.Singles) != i {
			t.Fatalf("%d events exported %d dates", i, len(feed.Singles))
		}
		if i == 1 {
			baseline = reads
		}
		if reads != baseline || reads > 4 {
			t.Fatalf("%d events: %d reads, baseline=%d cap=4", i, reads, baseline)
		}
		t.Logf("%d events: %d reads", i, reads)
	}
}

func TestFeedReadFailuresNeverPublishSnapshots(t *testing.T) {
	app := testApp(t)
	if err := app.DB.Create(&models.Setting{Key: "site_name", Value: "Feed Church"}).Error; err != nil {
		t.Fatal(err)
	}
	admin, gid := openGroup(t, app, "Feed read failures")
	createVideoEvent(t, admin, gid, map[string]any{"remoteAvailable": true, "videoProvider": "link", "videoUrl": videoLink, "videoReveal": "registered_only"})
	var group models.Group
	if err := app.DB.First(&group, gid).Error; err != nil {
		t.Fatal(err)
	}
	_, member := mkUser(t, app, &group, "feed reader")
	personal := createFeedLink(t, member, models.CalendarFeedMine)
	anon := newAPIClient(t, app)
	for _, path := range []string{"/cal/public.ics", "/cal/groups/" + gid + ".ics", personal} {
		t.Run(path, func(t *testing.T) {
			before := fetchFeed(t, anon, path)
			if before.Code != http.StatusOK || !strings.Contains(before.Body.String(), "BEGIN:VEVENT") || before.Header().Get("ETag") == "" {
				t.Fatalf("missing baseline snapshot: %d %s", before.Code, before.Body.String())
			}
			type failedRead struct {
				table string
				read  int
			}
			failures := []failedRead{{"events", 1}, {"groups", 1}, {"event_occurrences", 1}}
			if path == "/cal/public.ics" || path == personal {
				if !strings.Contains(before.Body.String(), "X-WR-CALNAME:Feed Church\r\n") {
					t.Fatalf("missing configured calendar name: %s", before.Body.String())
				}
				failures = append(failures, failedRead{"settings", 1})
			}
			if path == personal {
				failures = append(failures, failedRead{"calendar_tokens", 1}, failedRead{"users", 1},
					failedRead{"memberships", 1}, failedRead{"event_hosts", 1}, failedRead{"occurrence_accesses", 1},
					failedRead{"registrations", 1}, failedRead{"registrations", 2}, failedRead{"registrations", 3})
			} else if strings.HasPrefix(path, "/cal/groups/") {
				failures = append(failures, failedRead{"groups", 2})
			}
			for _, failure := range failures {
				t.Run(fmt.Sprintf("%s/read%d", failure.table, failure.read), func(t *testing.T) {
					reads := 0
					const callback = "test:fail_feed_read"
					if err := app.DB.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
						if tx.Statement.Table == failure.table {
							reads++
							if reads == failure.read {
								_ = tx.AddError(errors.New("calendar database unavailable"))
							}
						}
					}); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = app.DB.Callback().Query().Remove(callback) })
					if path == personal && failure.table == "settings" {
						if w := fetchFeed(t, anon, "/cal/u/no-such-token.ics"); w.Code != http.StatusNotFound || reads != 0 {
							t.Fatalf("invalid token read settings: status=%d reads=%d", w.Code, reads)
						}
					}
					for _, etag := range []string{"", before.Header().Get("ETag"), "*"} {
						reads = 0
						w := fetchFeedIf(t, anon, path, etag)
						if reads < failure.read {
							t.Fatalf("failure was not injected: %d reads", reads)
						}
						if w.Code != http.StatusInternalServerError || w.Header().Get("ETag") != "" || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), "BEGIN:VCALENDAR") {
							t.Errorf("failed read published snapshot with If-None-Match=%q: status=%d headers=%v body=%s", etag, w.Code, w.Header(), w.Body.String())
						}
					}
					_ = app.DB.Callback().Query().Remove(callback)
					if recovered := fetchFeedIf(t, anon, path, before.Header().Get("ETag")); recovered.Code != http.StatusNotModified {
						t.Fatalf("same snapshot did not recover after failed read: %d %s", recovered.Code, recovered.Body.String())
					}
				})
			}
		})
	}
}

func TestFeedSiteNameFallback(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "feed name member", "password1234", models.RoleMember)
	member := loginAPI(t, app, "feed name member", "password1234")
	personal := createFeedLink(t, member, models.CalendarFeedMine)
	anon := newAPIClient(t, app)
	for _, setting := range []string{"missing", "empty"} {
		t.Run(setting, func(t *testing.T) {
			if setting == "empty" {
				if err := app.DB.Create(&models.Setting{Key: "site_name", Value: ""}).Error; err != nil {
					t.Fatal(err)
				}
			}
			for _, path := range []string{"/cal/public.ics", personal} {
				w := fetchFeed(t, anon, path)
				if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "X-WR-CALNAME:Fold\r\n") || w.Header().Get("ETag") == "" {
					t.Fatalf("%s fallback: status=%d headers=%v body=%s", path, w.Code, w.Header(), w.Body.String())
				}
				if cached := fetchFeedIf(t, anon, path, w.Header().Get("ETag")); cached.Code != http.StatusNotModified {
					t.Fatalf("%s fallback did not cache: %d", path, cached.Code)
				}
			}
		})
	}
}

func mustBuildFeed(t *testing.T, app *App, viewer *models.User, opts feedOptions, now time.Time) events.Feed {
	t.Helper()
	feed, err := app.buildFeed(viewer, opts, now)
	if err != nil {
		t.Fatalf("build feed: %v", err)
	}
	return feed
}

func TestPersonalFeedHouseholdScopeKeepsOwnRSVPPrecedence(t *testing.T) {
	for _, recurring := range []bool{false, true} {
		t.Run(fmt.Sprintf("recurring=%v", recurring), func(t *testing.T) {
			app := testApp(t)
			newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
			admin := loginAPI(t, app, "admin", "password1234")
			gid := createFeedGroup(t, admin, "Household RSVP", models.VisibilityPublic, "Hall")
			owner, client, spouse, _ := householdFamily(t, app, admin, "household feed owner")
			// Beyond discovery's 30-day window, only actual own/household
			// ties can put these non-member dates in either feed scope.
			start := feedStart(60)
			states := []string{"", models.RegMaybe, models.RegWaitlisted, models.RegGoing, models.RegDeclined}
			var dates []models.EventOccurrence
			if recurring {
				ev := createFeedEvent(t, admin, gid, "Household series", start, weeklyRule(start)+";COUNT=5")
				if err := app.DB.Where("event_id = ?", ev.ID).Order("starts_at").Find(&dates).Error; err != nil {
					t.Fatal(err)
				}
			} else {
				for i := range states {
					ev := createFeedEvent(t, admin, gid, fmt.Sprintf("Household date %d", i), start.AddDate(0, 0, i), "")
					var occ models.EventOccurrence
					if err := app.DB.Where("event_id = ?", ev.ID).First(&occ).Error; err != nil {
						t.Fatal(err)
					}
					dates = append(dates, occ)
				}
			}
			if len(dates) != len(states) {
				t.Fatalf("fixture has %d dates, want %d", len(dates), len(states))
			}
			for i, occ := range dates {
				batchAnswer(t, client, occ.EventID, occ.OriginalDate, map[string]any{"people": []map[string]any{{"personId": spouse.ID, "status": models.RegGoing}}}, http.StatusOK)
				if states[i] != "" {
					reg := models.Registration{UserID: &owner.ID, OccurrenceID: occ.ID, Status: states[i], AttendanceMode: models.AttendanceInPerson, Source: "web"}
					if err := app.DB.Create(&reg).Error; err != nil {
						t.Fatal(err)
					}
				}
			}
			for _, scope := range []string{models.CalendarFeedMine, models.CalendarFeedAll} {
				raw := unfoldFeed(feedBody(t, client, createFeedLink(t, client, scope)))
				if strings.Count(raw, "BEGIN:VEVENT") != 4 || strings.Contains(raw, "RRULE:") || strings.Contains(raw, "RECURRENCE-ID:") {
					t.Fatalf("scope=%s recreated an omitted date: %s", scope, raw)
				}
				for i, occ := range dates {
					uid := fmt.Sprintf("event-%d-%s@", occ.EventID, strings.ReplaceAll(occ.OriginalDate, "-", ""))
					block := feedEventWithUIDPrefix(raw, uid)
					if states[i] == models.RegDeclined {
						if block != "" {
							t.Fatalf("scope=%s household Going overrode own Can't go: %s", scope, block)
						}
						continue
					}
					want := "TENTATIVE"
					if states[i] == models.RegGoing {
						want = "CONFIRMED"
					}
					if !strings.Contains(block, "STATUS:"+want+"\r\n") {
						t.Fatalf("scope=%s own=%q: want %s in %s", scope, states[i], want, block)
					}
				}
				for _, secret := range []string{"Mary", "Kit", "Private allergy note", "householdPerson", "2020-05-01"} {
					if strings.Contains(raw, secret) {
						t.Fatalf("scope=%s leaked %q", scope, secret)
					}
				}
			}
			var householdGoing int64
			if err := app.DB.Model(&models.Registration{}).Where("household_person_id = ? AND status = ?", spouse.ID, models.RegGoing).Count(&householdGoing).Error; err != nil {
				t.Fatal(err)
			}
			if householdGoing != int64(len(states)) {
				t.Fatalf("own RSVP/feed changed household attendance: %d Going", householdGoing)
			}
		})
	}
}
