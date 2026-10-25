package main

import (
	"encoding/json"
	"fmt"
	"fold/internal/audit"
	"fold/internal/events"
	"fold/models"
	"strings"
	"testing"
	"time"
)

func dateVisibilityFixture(t *testing.T, visibility string) (*App, *apiClient, *apiClient, eventJSON, []models.EventOccurrence) {
	t.Helper()
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	gid := createGroupAs(t, admin, "Hidden fellowship", visibility)
	app.DB.Model(&models.Group{}).Where("id = ?", gid).Update("location", "SECRET GROUP PLACE")
	w := admin.do("POST", fmt.Sprintf("/api/v1/groups/%d/events", gid), map[string]any{"title": "Open evening", "startsLocal": time.Now().UTC().AddDate(0, 0, 2).Format("2006-01-02T15:04"), "durationMinutes": 60, "tzid": "UTC", "recurrenceRule": "FREQ=WEEKLY;COUNT=4"}, true)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Event eventJSON `json:"event"`
	}
	decodeJSON(t, w, &created)
	var dates []models.EventOccurrence
	app.DB.Where("event_id = ?", created.Event.ID).Order("original_date").Find(&dates)
	if len(dates) != 4 {
		t.Fatalf("dates: %d", len(dates))
	}
	return app, admin, anonClient(t, app, "10.13.0.1"), created.Event, dates
}

func TestDateVisibilityParsing(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		set   bool
		value string
		bad   bool
	}{{"", false, "", false}, {"null", true, "", false}, {`"series"`, true, "", false}, {`"public"`, true, "public", false}, {`"private"`, true, "private", false}, {`"invite_only"`, true, "invite_only", false}, {`"bad"`, true, "", true}, {`true`, true, "", true}} {
		v, set, err := parseDateVisibility(json.RawMessage(tc.raw))
		got := ""
		if v != nil {
			got = *v
		}
		if set != tc.set || got != tc.value || (err != nil) != tc.bad {
			t.Fatalf("%s: %v %v %v", tc.raw, v, set, err)
		}
	}
}

func TestOpenedDateDiscoveryAndFeedPrivacy(t *testing.T) {
	app, admin, anon, ev, dates := dateVisibilityFixture(t, models.VisibilityInviteOnly)
	path := fmt.Sprintf("/api/v1/events/%d/occurrences/%s", ev.ID, dates[0].OriginalDate)
	apiDo(t, admin, "PUT", path, map[string]any{"visibility": "public", "reason": "Welcome our neighbours"}, 200)
	exact := fmt.Sprintf("/api/v1/events/%d/dates/%s", ev.ID, dates[0].OriginalDate)
	raw := apiDo(t, anon, "GET", exact, nil, 200)
	for _, forbidden := range []string{"groupId", "groupName", "SECRET GROUP PLACE", "Hidden fellowship", "recurrenceRule", "attendees", "hosts", "seriesStartsLocal"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("exact leaked %s: %s", forbidden, raw)
		}
	}
	apiDo(t, anon, "GET", fmt.Sprintf("/api/v1/events/%d", ev.ID), nil, 404)
	apiDo(t, anon, "GET", fmt.Sprintf("/api/v1/events/%d/dates/%s", ev.ID, dates[1].OriginalDate), nil, 404)
	listed := app.occurrencesForEvents([]uint{ev.ID}, dates[0].OriginalDate, dates[3].OriginalDate, nil, nil)
	if len(listed) != 1 || listed[0].GroupID != nil || listed[0].RecurrenceRule != "" || listed[0].Location != "" || listed[0].AttendeesVisible {
		t.Fatalf("list: %+v", listed)
	}
	feed := events.RenderFeed(app.buildFeed(nil, feedOptions{name: "Church"}, time.Now().UTC()))
	if strings.Contains(string(feed), "RRULE") || strings.Count(string(feed), "BEGIN:VEVENT") != 1 || !strings.Contains(string(feed), "/dates/"+dates[0].OriginalDate) || strings.Contains(string(feed), "SECRET") {
		t.Fatalf("feed: %s", feed)
	}
	var n int64
	app.DB.Model(&models.AuditLog{}).Where("action = ? AND target_id = ?", audit.VisibilityOverride, ev.ID).Count(&n)
	if n != 1 {
		t.Fatalf("audit rows %d", n)
	}
	apiDo(t, admin, "PUT", path, map[string]any{"visibility": "public"}, 200)
	app.DB.Model(&models.AuditLog{}).Where("action = ? AND target_id = ?", audit.VisibilityOverride, ev.ID).Count(&n)
	if n != 1 {
		t.Fatalf("no-op audited twice: %d", n)
	}
	apiDo(t, admin, "PUT", path, map[string]any{"visibility": nil}, 200)
	apiDo(t, anon, "GET", exact, nil, 404)
}

func TestSeriesVisibilityPreservesAndResetsDates(t *testing.T) {
	app, admin, anon, ev, dates := dateVisibilityFixture(t, models.VisibilityPrivate)
	path := fmt.Sprintf("/api/v1/events/%d/occurrences/%s", ev.ID, dates[0].OriginalDate)
	apiDo(t, admin, "PUT", path, map[string]any{"visibility": "public"}, 200)
	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d/visibility", ev.ID), map[string]any{"visibility": "invite_only"}, 200)
	exact := fmt.Sprintf("/api/v1/events/%d/dates/%s", ev.ID, dates[0].OriginalDate)
	apiDo(t, anon, "GET", exact, nil, 200)
	var impact visibilityImpactJSON
	decodeJSON(t, admin.do("GET", fmt.Sprintf("/api/v1/events/%d/visibility-impact?visibility=invite_only", ev.ID), nil, false), &impact)
	if impact.FutureOverrides != 1 {
		t.Fatalf("override count: %+v", impact)
	}
	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d/visibility", ev.ID), map[string]any{"visibility": "invite_only", "resetOverrides": true}, 200)
	apiDo(t, anon, "GET", exact, nil, 404)
	var date models.EventOccurrence
	app.DB.First(&date, dates[0].ID)
	if date.VisibilityOverride != nil {
		t.Fatalf("reset: %+v", date)
	}
}

func TestGroupTighteningRequiresDateDecision(t *testing.T) {
	app, admin, anon, ev, dates := dateVisibilityFixture(t, models.VisibilityPrivate)
	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d/occurrences/%s", ev.ID, dates[0].OriginalDate), map[string]any{"visibility": "public"}, 200)
	var impact visibilityImpactJSON
	groupPath := fmt.Sprintf("/api/v1/groups/%d", *ev.GroupID)
	decodeJSON(t, admin.do("GET", groupPath+"/visibility-impact?visibility=invite_only", nil, false), &impact)
	if len(impact.LeaderOpened) != 1 || impact.LeaderOpened[0].DecisionKey != dateDecisionKey(dates[0].ID) {
		t.Fatalf("date review: %+v", impact)
	}
	apiDo(t, admin, "PUT", groupPath, map[string]any{"visibility": "invite_only"}, 409)
	var group models.Group
	app.DB.First(&group, *ev.GroupID)
	if group.Visibility != models.VisibilityPrivate {
		t.Fatal("missing decision did not roll back")
	}
	apiDo(t, admin, "PUT", groupPath, map[string]any{"visibility": "invite_only", "openedEvents": map[string]string{dateDecisionKey(dates[0].ID): "tighten"}}, 200)
	apiDo(t, anon, "GET", fmt.Sprintf("/api/v1/events/%d/dates/%s", ev.ID, dates[0].OriginalDate), nil, 404)
}

func TestDateTighteningCancelsOnlyThatDateAndRechecksGuestRedemption(t *testing.T) {
	app, admin, anon, ev, dates := dateVisibilityFixture(t, models.VisibilityPrivate)
	for i := 0; i < 2; i++ {
		apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d/occurrences/%s", ev.ID, dates[i].OriginalDate), map[string]any{"visibility": "public"}, 200)
	}
	email := "date-guest@example.com"
	for i := 0; i < 2; i++ {
		guestSubmit(t, anon, ev.ID, map[string]any{"name": "Guest", "email": email, "date": dates[i].OriginalDate, "over18": true}, 200)
		token := tokenInEmail(t, latestEmail(t, app, email))
		apiDo(t, anon, "POST", "/api/v1/guest-registrations/confirm", map[string]any{"token": token}, 200)
	}
	pending := "pending-date@example.com"
	guestSubmit(t, anon, ev.ID, map[string]any{"name": "Waiting Guest", "email": pending, "date": dates[0].OriginalDate, "over18": true}, 200)
	token := tokenInEmail(t, latestEmail(t, app, pending))
	path := fmt.Sprintf("/api/v1/events/%d/occurrences/%s", ev.ID, dates[0].OriginalDate)
	apiDo(t, admin, "PUT", path, map[string]any{"visibility": nil}, 409)
	var n int64
	app.DB.Model(&models.Registration{}).Count(&n)
	if n != 2 {
		t.Fatalf("unconfirmed cancellations: %d", n)
	}
	apiDo(t, admin, "PUT", path, map[string]any{"visibility": nil, "confirm": true}, 200)
	app.DB.Model(&models.Registration{}).Where("occurrence_id = ?", dates[1].ID).Count(&n)
	if n != 1 {
		t.Fatal("sibling registration cancelled")
	}
	app.DB.Model(&models.Registration{}).Where("occurrence_id = ?", dates[0].ID).Count(&n)
	if n != 0 {
		t.Fatal("closed-date guest retained")
	}
	w := anon.do("POST", "/api/v1/guest-registrations/confirm", map[string]any{"token": token}, true)
	if w.Code == 200 {
		t.Fatalf("guest redeemed closed date: %s", w.Body.String())
	}
	var notices []models.Notification
	app.DB.Where("kind = ?", "registration.cancelled").Find(&notices)
	if len(notices) != 1 || !strings.Contains(notices[0].Title, dates[0].OriginalDate) || strings.Contains(notices[0].Body, "Hidden fellowship") {
		t.Fatalf("date cancellation notices: %+v", notices)
	}
}

func TestVisibilityOverrideProtectsDateFromSeriesClockEdit(t *testing.T) {
	app, admin, _, ev, dates := dateVisibilityFixture(t, models.VisibilityPrivate)
	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d/occurrences/%s", ev.ID, dates[0].OriginalDate), map[string]any{"visibility": "public"}, 200)
	changed := dates[0].StartsAt.UTC().Add(time.Hour).Format("2006-01-02T15:04")
	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d", ev.ID), map[string]any{"startsLocal": changed, "tzid": "UTC"}, 200)
	var first, second models.EventOccurrence
	app.DB.First(&first, dates[0].ID)
	app.DB.First(&second, dates[1].ID)
	if !first.StartsAt.Equal(dates[0].StartsAt) || !second.StartsAt.Equal(dates[1].StartsAt.Add(time.Hour)) {
		t.Fatalf("customized date moved or inherited date stuck: %v %v", first.StartsAt, second.StartsAt)
	}
}

func TestConcurrentRegistrationVersusDateTightening(t *testing.T) {
	app, admin, _, ev, dates := dateVisibilityFixture(t, models.VisibilityInviteOnly)
	path := fmt.Sprintf("/api/v1/events/%d/occurrences/%s", ev.ID, dates[0].OriginalDate)
	apiDo(t, admin, "PUT", path, map[string]any{"visibility": "public"}, 200)
	clients := make([]*apiClient, 8)
	for i := range clients {
		name := fmt.Sprintf("racer%d", i)
		newTestUser(t, app, name, "password1234", models.RoleMember)
		clients[i] = loginAPI(t, app, name, "password1234")
	}
	start := make(chan struct{})
	results := make(chan int, len(clients))
	for _, c := range clients {
		go func(c *apiClient) {
			<-start
			w := c.do("PUT", fmt.Sprintf("/api/v1/events/%d/registrations", ev.ID), map[string]any{"date": dates[0].OriginalDate, "status": "going"}, true)
			results <- w.Code
		}(c)
	}
	close(start)
	apiDo(t, admin, "PUT", path, map[string]any{"visibility": "series", "confirm": true}, 200)
	for range clients {
		code := <-results
		if code != 200 && code != 404 {
			t.Errorf("race response: %d", code)
		}
	}
	var n int64
	app.DB.Model(&models.Registration{}).Where("occurrence_id = ?", dates[0].ID).Count(&n)
	if n != 0 {
		t.Fatalf("%d inaccessible registrations survived tightening", n)
	}
}

func TestChurchWideHostDateVisibilityAndInvitationFallback(t *testing.T) {
	app, admin, _, _, _ := dateVisibilityFixture(t, models.VisibilityPrivate)
	host := newTestUser(t, app, "date-host", "password1234", models.RoleMember)
	hostC := loginAPI(t, app, "date-host", "password1234")
	date := time.Now().UTC().AddDate(0, 0, 3).Format("2006-01-02")
	var response struct {
		Event eventJSON `json:"event"`
	}
	w := admin.do("POST", "/api/v1/events", map[string]any{"title": "Church supper", "startsLocal": date + "T18:00", "tzid": "UTC", "durationMinutes": 60, "visibility": "public", "hosts": []string{host.Email}}, true)
	if w.Code != 201 {
		t.Fatalf("church event: %s", w.Body.String())
	}
	decodeJSON(t, w, &response)
	path := fmt.Sprintf("/api/v1/events/%d/occurrences/%s", response.Event.ID, date)
	apiDo(t, hostC, "PUT", path, map[string]any{"visibility": "invite_only"}, 400)
	apiDo(t, hostC, "PUT", path, map[string]any{"visibility": "private"}, 200)
	apiDo(t, hostC, "PUT", path, map[string]any{"visibility": "series"}, 200)
	// A valid explicit invitation remains exact-date authority after tightening.
	var issuer models.User
	app.DB.Where("role = ?", models.RoleAdmin).First(&issuer)
	recipient := newTestUser(t, app, "date-invited", "password1234", models.RoleMember)
	client := loginAPI(t, app, "date-invited", "password1234")
	gid := createGroupAs(t, admin, "Invitation context", models.VisibilityInviteOnly)
	event := scheduleGroupEvent(t, admin, gid, "Invitation supper")
	var occ models.EventOccurrence
	app.DB.Where("event_id = ?", event.ID).First(&occ)
	grant := seedReadInvitation(t, app, &issuer, recipient, event, occ)
	p := fmt.Sprintf("/api/v1/events/%d/occurrences/%s", event.ID, occ.OriginalDate)
	apiDo(t, admin, "PUT", p, map[string]any{"visibility": "public"}, 200)
	apiDo(t, admin, "PUT", p, map[string]any{"visibility": "invite_only"}, 200)
	apiDo(t, client, "GET", dateReadPath(event.ID, occ.OriginalDate), nil, 200)
	var n int64
	app.DB.Model(&models.Registration{}).Where("id = ?", grant.RegistrationID).Count(&n)
	if n != 1 {
		t.Fatal("explicit invitation registration cancelled")
	}
	apiDo(t, client, "GET", fmt.Sprintf("/api/v1/events/%d", event.ID), nil, 404)
}

func TestDateVisibilityFeedsUseStandaloneEntriesAndInvalidateETags(t *testing.T) {
	app, admin, anon, ev, dates := dateVisibilityFixture(t, models.VisibilityPublic)
	path := fmt.Sprintf("/api/v1/events/%d/occurrences/%s", ev.ID, dates[0].OriginalDate)
	before := fetchFeed(t, anon, "/cal/public.ics")
	apiDo(t, admin, "PUT", path, map[string]any{"visibility": "invite_only"}, 200)
	after := fetchFeedIf(t, anon, "/cal/public.ics", before.Header().Get("ETag"))
	if after.Code != 200 || after.Header().Get("ETag") == before.Header().Get("ETag") {
		t.Fatalf("unchanged ETag: %d", after.Code)
	}
	body := after.Body.String()
	if strings.Contains(body, "RRULE") || strings.Count(body, "BEGIN:VEVENT") != 3 || strings.Contains(body, "/dates/"+dates[0].OriginalDate) {
		t.Fatalf("partial recurrence feed: %s", body)
	}
	for _, opts := range []feedOptions{{name: "Public"}, {name: "Group", onlyGroupID: ev.GroupID}} {
		feed := events.RenderFeed(app.buildFeed(nil, opts, time.Now().UTC()))
		if strings.Contains(string(feed), "RRULE") || strings.Count(string(feed), "BEGIN:VEVENT") != 3 {
			t.Fatalf("feed leaks hidden date: %s", feed)
		}
	}
}
