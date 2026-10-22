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
	if got := strings.Count(body, "RRULE:"); got != 1 {
		t.Fatalf("mine feed has %d RRULE entries, want only the choir's:\n%s", got, body)
	}
	singleUID := fmt.Sprintf("event-%d-%s@", mid.ID, strings.ReplaceAll(goingDate, "-", ""))
	if !strings.Contains(body, singleUID) || !strings.Contains(body, "STATUS:CONFIRMED") {
		t.Fatalf("mine feed is missing the one going date as a single:\n%s", body)
	}
	for _, other := range []string{feedDate(midStart), feedDate(midStart.AddDate(0, 0, 14))} {
		uid := fmt.Sprintf("event-%d-%s@", mid.ID, strings.ReplaceAll(other, "-", ""))
		if strings.Contains(body, uid) {
			t.Fatalf("mine feed carries a midweek date sam is not going to (%s):\n%s", other, body)
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
	if got := strings.Count(body, "RRULE:"); got != 2 {
		t.Fatalf("all feed has %d RRULE entries, want both series whole:\n%s", got, body)
	}
	if strings.Contains(body, singleUID) {
		t.Fatalf("all feed duplicates the series with a single:\n%s", body)
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
	apiDo(t, sam, "POST", "/api/v1/groups/"+itoa(inner)+"/leave", map[string]any{}, http.StatusOK)
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
