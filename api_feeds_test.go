package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fold/models"
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
		"video_reveal": models.VideoRevealAlways,
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
		for _, forbidden := range []string{
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
	if !strings.Contains(public, fmt.Sprintf("UID:event-%d@", series.ID)) || !strings.Contains(public, "RRULE:") || strings.Contains(public, uidPrefix) {
		t.Fatalf("public series feed contract changed: %s", public)
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
	body := feedBody(t, anon, "/cal/public.ics")
	if got := strings.Count(body, "RRULE:"); got != 1 {
		t.Fatalf("feed has %d RRULE entries, want one series:\n%s", got, body)
	}
	if !strings.Contains(body, "EXDATE") || !strings.Contains(body, strings.ReplaceAll(cancelled, "-", "")+"T190000") {
		t.Fatalf("feed is missing the EXDATE for the cancelled date:\n%s", body)
	}
	if !strings.Contains(body, "RECURRENCE-ID") || !strings.Contains(body, "Special Rehearsal") {
		t.Fatalf("feed is missing the moved date's RECURRENCE-ID entry:\n%s", body)
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
// gets that date with the link in a "mine" feed (and the whole series
// entry carries it in an "all" feed, accepted for now).
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
	if !has(oneDate, models.CalendarFeedAll) {
		t.Fatal("the series entry should carry the link for someone going to one date")
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
