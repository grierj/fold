package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"fold/internal/audit"
	"fold/internal/eventdates"
	"fold/internal/events"
	"fold/internal/notify"
	"fold/internal/policy"
	"fold/internal/testdb"
	"fold/models"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func TestGroupListReadBudgetsAndSummaryValues(t *testing.T) {
	for _, path := range []string{"/api/v1/groups", "/api/v1/me/groups", "/api/v1/admin/groups", "/api/v1/admin/pending", "/api/v1/admin/deletions"} {
		t.Run(path, func(t *testing.T) {
			app := testApp(t)
			leader := newTestUser(t, app, "leader", "pw", models.RoleAdmin)
			member := newTestUser(t, app, "member", "pw", models.RoleMember)
			disabled := newTestUser(t, app, "disabled", "pw", models.RoleMember)
			if err := app.DB.Model(disabled).Update("status", models.UserDisabled).Error; err != nil {
				t.Fatal(err)
			}
			client := loginAPI(t, app, "leader", "pw")
			start := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)
			var baseline int64
			for i := 1; i <= 30; i++ {
				status := models.GroupActive
				if path == "/api/v1/admin/pending" {
					status = models.GroupPending
				}
				g := newTestGroup(t, app, leader, fmt.Sprintf("Group %02d", i), status)
				if err := app.DB.Model(g).Updates(map[string]any{"visibility": models.VisibilityPublic, "deletion_requested": true}).Error; err != nil {
					t.Fatal(err)
				}
				addMember(t, app, member, g, models.MembershipMember)
				addMember(t, app, disabled, g, models.MembershipMember)
				ev := models.Event{GroupID: &g.ID, Title: "Next", Visibility: models.VisibilityPublic, StartsLocal: start.Format("2006-01-02T15:04"), TZID: "UTC", DurationMinutes: 60, CreatedBy: leader.ID}
				if err := app.DB.Create(&ev).Error; err != nil {
					t.Fatal(err)
				}
				o := models.EventOccurrence{EventID: ev.ID, OriginalDate: calDate(start.Format("2006-01-02")), StartsAt: start, EndsAt: start.Add(time.Hour), Status: models.OccurrenceScheduled}
				if err := app.DB.Create(&o).Error; err != nil {
					t.Fatal(err)
				}
				if i != 3 && i != 30 {
					continue
				}
				counter := countDBReads(t, app.DB)
				w := client.do("GET", path, nil, false)
				reads := counter.Reads()
				counter.Stop()
				if w.Code != http.StatusOK {
					t.Fatalf("list: %d %s", w.Code, w.Body.String())
				}
				var body struct {
					Groups []groupSummaryJSON `json:"groups"`
				}
				decodeJSON(t, w, &body)
				if len(body.Groups) != i {
					t.Fatalf("got %d groups, want %d", len(body.Groups), i)
				}
				for _, summary := range body.Groups {
					if summary.MemberCount != 2 || summary.LeaderName != leader.PublicName() || summary.NextEventAt == nil || !summary.NextEventAt.Equal(start) {
						t.Fatalf("incorrect summary: %+v", summary)
					}
				}
				if i == 3 {
					baseline = reads
				}
				if reads != baseline || reads > 12 {
					t.Fatalf("%d groups: %d reads, baseline=%d, cap=12", i, reads, baseline)
				}
			}
		})
	}
}

func TestMeetingInstancesAreChronological(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")

	w := admin.do("POST", "/api/v1/groups", map[string]any{"name": "Choir"}, true)
	var created struct {
		Group groupSummaryJSON `json:"group"`
	}
	decodeJSON(t, w, &created)
	gid := itoa(created.Group.ID)
	admin.do("POST", "/api/v1/admin/groups/"+gid+"/approve", map[string]any{}, true)

	now := time.Now().UTC().Truncate(time.Minute)
	later := now.Add(30 * 24 * time.Hour)
	sooner := now.Add(48 * time.Hour)
	for _, start := range []time.Time{later, sooner} {
		w = admin.do("POST", "/api/v1/groups/"+gid+"/events", map[string]any{
			"startsLocal": start.Format("2006-01-02T15:04"), "durationMinutes": 60, "tzid": "UTC",
		}, true)
		if w.Code != http.StatusCreated {
			t.Fatalf("create event: %d %s", w.Code, w.Body.String())
		}
	}

	from := now.Add(-time.Hour).Format("2006-01-02")
	to := now.Add(60 * 24 * time.Hour).Format("2006-01-02")
	w = admin.do("GET", "/api/v1/groups/"+gid+"/events?from="+from+"&to="+to, nil, false)
	var list struct {
		Events []occurrenceJSON `json:"events"`
	}
	decodeJSON(t, w, &list)
	if len(list.Events) != 2 {
		t.Fatalf("occurrences: got %d", len(list.Events))
	}
	if !list.Events[0].StartsAt.Before(list.Events[1].StartsAt) {
		t.Fatalf("not chronological: %s then %s", list.Events[0].StartsAt, list.Events[1].StartsAt)
	}

	w = admin.do("GET", "/api/v1/groups/"+gid, nil, false)
	var detail struct {
		Group groupSummaryJSON `json:"group"`
	}
	decodeJSON(t, w, &detail)
	if detail.Group.NextEventAt == nil || !detail.Group.NextEventAt.UTC().Equal(sooner) {
		t.Fatalf("nextEventAt: got %v want %s", detail.Group.NextEventAt, sooner)
	}
}

func TestMeetingValidation(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	w := admin.do("POST", "/api/v1/groups", map[string]any{"name": "Study"}, true)
	var created struct {
		Group groupSummaryJSON `json:"group"`
	}
	decodeJSON(t, w, &created)
	gid := itoa(created.Group.ID)
	admin.do("POST", "/api/v1/admin/groups/"+gid+"/approve", map[string]any{}, true)

	w = admin.do("POST", "/api/v1/groups/"+gid+"/events", map[string]any{
		"startsLocal": "2026-10-20T18:00", "durationMinutes": 60, "tzid": "UTC",
		"recurrenceRule": "NOT_A_RULE",
	}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad rule: got %d %s", w.Code, w.Body.String())
	}

	w = admin.do("GET", "/api/v1/groups/"+gid+"/events?from=2000-01-01&to=2026-01-01", nil, false)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("wide range: got %d %s", w.Code, w.Body.String())
	}
}

func TestRemoteSeatLimitValidation(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "Hybrid study")
	const zeroMessage = "Uncheck remote attendance instead of a 0 remote seat limit"
	for _, path := range []string{"/api/v1/groups/" + gid + "/events", "/api/v1/events"} {
		w := admin.do("POST", path, map[string]any{
			"title": "Hybrid", "startsLocal": "2026-12-01T19:00", "durationMinutes": 60,
			"tzid": "UTC", "remoteAvailable": true, "remoteCapacity": 0,
		}, true)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("create with zero at %s: %d %s", path, w.Code, w.Body.String())
		}
		var body struct{ Error, Code string }
		decodeJSON(t, w, &body)
		if body.Error != zeroMessage || body.Code != "remote_capacity_zero" {
			t.Fatalf("zero limit error: %+v", body)
		}
	}
	eventID, date := seedRemoteEvent(t, app, admin, gid, nil, intPtr(5), false)
	path := fmt.Sprintf("/api/v1/events/%d", eventID)
	for _, bad := range []any{0, -2, 1.5, "1e", 100001} {
		w := admin.do("PUT", path, map[string]any{"remoteCapacity": bad}, true)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("update remote limit %v: %d %s", bad, w.Code, w.Body.String())
		}
		if bad == 0 && !strings.Contains(w.Body.String(), zeroMessage) {
			t.Fatalf("zero limit message: %s", w.Body.String())
		}
	}
	var ev models.Event
	app.DB.First(&ev, eventID)
	if ev.RemoteCapacity == nil || *ev.RemoteCapacity != 5 {
		t.Fatalf("invalid update changed remote seats: %+v", ev.RemoteCapacity)
	}
	datePath := path + "/occurrences/" + date
	for _, bad := range []any{-1, 1.5, "1e", 100001} {
		apiDo(t, admin, "PUT", datePath, map[string]any{"remoteCapacityOverride": bad}, http.StatusBadRequest)
	}
	w := admin.do("PUT", datePath, map[string]any{"remoteCapacityOverride": 0}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("close remote seats on one date: %d %s", w.Code, w.Body.String())
	}
	var occ models.EventOccurrence
	app.DB.Where("event_id = ? AND original_date = ?", eventID, date).First(&occ)
	if occ.RemoteCapacityOverride == nil || *occ.RemoteCapacityOverride != 0 {
		t.Fatalf("zero per-date override was not stored: %+v", occ.RemoteCapacityOverride)
	}
	apiDo(t, admin, "PUT", datePath, map[string]any{"title": "Same remote limit"}, http.StatusOK)
	occ = models.EventOccurrence{}
	app.DB.Where("event_id = ? AND original_date = ?", eventID, date).First(&occ)
	if occ.RemoteCapacityOverride == nil || *occ.RemoteCapacityOverride != 0 {
		t.Fatal("omitted remoteCapacityOverride changed the stored limit")
	}
	apiDo(t, admin, "PUT", datePath, map[string]any{"remoteCapacityOverride": nil}, http.StatusOK)
	occ = models.EventOccurrence{}
	app.DB.Where("event_id = ? AND original_date = ?", eventID, date).First(&occ)
	if occ.RemoteCapacityOverride != nil {
		t.Fatal("explicit null did not clear remoteCapacityOverride")
	}
	apiDo(t, admin, "PUT", path, map[string]any{"remoteCapacity": 1}, http.StatusOK)
	apiDo(t, admin, "PUT", path, map[string]any{"title": "Same series remote limit"}, http.StatusOK)
	ev = models.Event{}
	app.DB.First(&ev, eventID)
	if ev.RemoteCapacity == nil || *ev.RemoteCapacity != 1 {
		t.Fatal("omitted remoteCapacity changed the stored limit")
	}
	apiDo(t, admin, "PUT", path, map[string]any{"remoteCapacity": nil}, http.StatusOK)
	ev = models.Event{}
	app.DB.First(&ev, eventID)
	if ev.RemoteCapacity != nil {
		t.Fatal("explicit null did not clear remoteCapacity")
	}
	if raw := unfoldFeed(feedBody(t, newAPIClient(t, app), "/cal/public.ics")); !strings.Contains(raw, "Remote attendance available") {
		t.Fatalf("cleared remote capacity did not advertise unlimited remote attendance: %s", raw)
	}
	apiDo(t, admin, "PUT", path, map[string]any{"remoteAvailable": false}, http.StatusOK)
}

// A weekly 7:00 pm America/Boise event stays at 7:00 pm local on both sides
// of the 2026-11-01 and 2027-03-14 changes, and the API returns both clocks.
func TestBoiseWeeklyKeepsLocalTime(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "Boise")

	w := admin.do("PUT", "/api/v1/admin/timezone", map[string]any{"timezone": "Not/AZone"}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad zone: %d %s", w.Code, w.Body.String())
	}
	w = admin.do("PUT", "/api/v1/admin/timezone", map[string]any{"timezone": "America/Boise"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("set zone: %d %s", w.Code, w.Body.String())
	}
	anon := newAPIClient(t, app)
	w = anon.do("GET", "/api/v1/site", nil, false)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"timezone":"America/Boise"`) {
		t.Fatalf("site: %d %s", w.Code, w.Body.String())
	}

	w = admin.do("POST", "/api/v1/groups/"+gid+"/events", map[string]any{
		"title": "Evening", "startsLocal": "2026-10-28T19:00", "durationMinutes": 60,
		"tzid": "America/Boise", "recurrenceRule": "FREQ=WEEKLY",
	}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Event eventJSON `json:"event"`
	}
	decodeJSON(t, w, &created)
	if created.Event.StartsLocal != "2026-10-28T19:00" || created.Event.StartsAt.UTC().Hour() != 1 {
		t.Fatalf("create clocks: local %s utc %s", created.Event.StartsLocal, created.Event.StartsAt.UTC())
	}

	// Fall back is Nov 1 (MDT → MST). Spring forward is Mar 14 (MST → MDT).
	// 19:00 MDT is 01:00 UTC the next day; 19:00 MST is 02:00 UTC.
	checks := []struct {
		from, to string
		hours    map[string]int
	}{
		{"2026-10-28", "2026-11-04", map[string]int{"2026-10-28": 1, "2026-11-04": 2}},
		{"2027-03-10", "2027-03-17", map[string]int{"2027-03-10": 2, "2027-03-17": 1}},
	}
	for _, c := range checks {
		list := listOccurrences(t, admin, gid, c.from, c.to)
		for date, hour := range c.hours {
			o := occurrenceOn(t, list, date)
			if o.StartsLocal != date+"T19:00" || o.EndsLocal != date+"T20:00" {
				t.Fatalf("%s local: %s–%s", date, o.StartsLocal, o.EndsLocal)
			}
			if o.StartsAt.UTC().Hour() != hour || o.TZID != "America/Boise" {
				t.Fatalf("%s utc hour %d tz %s, want %d", date, o.StartsAt.UTC().Hour(), o.TZID, hour)
			}
		}
	}
}

// A series edit moves future dates and keeps their registrations, cancels
// dates the new rule drops (and tells going/waitlisted, not maybe), and
// leaves per-date overrides alone. A date already underway stays put.
func TestSeriesEditKeepsRegistrationsAndOverrides(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "Prayer")
	samUser := newTestUser(t, app, "sam", "password1234", models.RoleMember)
	patUser := newTestUser(t, app, "pat", "password1234", models.RoleMember)
	sam := loginAPI(t, app, "sam", "password1234")
	pat := loginAPI(t, app, "pat", "password1234")
	for _, c := range []*apiClient{sam, pat} {
		if w := c.do("POST", "/api/v1/groups/"+gid+"/join", map[string]any{}, true); w.Code != http.StatusOK {
			t.Fatalf("join: %d %s", w.Code, w.Body.String())
		}
	}

	start := atUTC(time.Now().AddDate(0, 0, 14), 19, 0)
	d0 := start.Format("2006-01-02")
	d1 := start.AddDate(0, 0, 7).Format("2006-01-02")
	d2 := start.AddDate(0, 0, 14).Format("2006-01-02")
	d3 := start.AddDate(0, 0, 21).Format("2006-01-02")
	d4 := start.AddDate(0, 0, 28).Format("2006-01-02")
	w := admin.do("POST", "/api/v1/groups/"+gid+"/events", map[string]any{
		"title": "Prayer", "startsLocal": start.Format("2006-01-02T15:04"),
		"durationMinutes": 60, "tzid": "UTC", "recurrenceRule": weeklyRule(start),
	}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Event eventJSON `json:"event"`
	}
	decodeJSON(t, w, &created)
	eid := itoa(created.Event.ID)
	windowEnd := start.AddDate(0, 0, 35).Format("2006-01-02")

	register := func(c *apiClient, date, status string) {
		t.Helper()
		w := c.do("PUT", "/api/v1/events/"+eid+"/registrations", map[string]any{"date": date, "status": status}, true)
		if w.Code != http.StatusOK {
			t.Fatalf("register %s: %d %s", date, w.Code, w.Body.String())
		}
	}
	register(sam, d1, "going")
	register(pat, d3, "maybe")
	register(sam, d3, "going")

	// One date moves to 9:00 pm. Another only changes its room.
	w = admin.do("PUT", "/api/v1/events/"+eid+"/occurrences/"+d2, map[string]any{
		"startsLocal": d2 + "T21:00",
	}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("override time: %d %s", w.Code, w.Body.String())
	}
	w = admin.do("PUT", "/api/v1/events/"+eid+"/occurrences/"+d4, map[string]any{
		"location": "Chapel",
	}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("override location: %d %s", w.Code, w.Body.String())
	}
	moved := occurrenceOn(t, listOccurrences(t, admin, gid, d0, windowEnd), d1)
	var reg models.Registration
	if err := app.DB.Where("user_id = ? AND occurrence_id = ?", samUser.ID, moved.ID).First(&reg).Error; err != nil {
		t.Fatal(err)
	}

	// The series moves to 8:00 pm. Future plain dates follow; overrides do not.
	w = admin.do("PUT", "/api/v1/events/"+eid, map[string]any{
		"startsLocal": start.Format("2006-01-02") + "T20:00",
	}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("series edit: %d %s", w.Code, w.Body.String())
	}
	after := listOccurrences(t, admin, gid, d0, windowEnd)
	if got := occurrenceOn(t, after, d1); got.ID != moved.ID || got.StartsLocal != d1+"T20:00" {
		t.Fatalf("moved date: %+v", got)
	}
	if got := occurrenceOn(t, after, d2); got.StartsLocal != d2+"T21:00" {
		t.Fatalf("time override moved: %s", got.StartsLocal)
	}
	if got := occurrenceOn(t, after, d4); got.StartsLocal != d4+"T19:00" || got.Location != "Chapel" {
		t.Fatalf("location override: %s %q", got.StartsLocal, got.Location)
	}
	var still models.Registration
	if err := app.DB.First(&still, reg.ID).Error; err != nil {
		t.Fatal(err)
	}
	if still.OccurrenceID != moved.ID || still.Status != models.RegGoing {
		t.Fatalf("registration changed: %+v", still)
	}

	// Ending the series on d1 drops d3. d2 and d4 are overrides, so they stay.
	w = admin.do("PUT", "/api/v1/events/"+eid, map[string]any{"rruleUntil": d1}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("until: %d %s", w.Code, w.Body.String())
	}
	left := listOccurrences(t, admin, gid, d0, windowEnd)
	for _, date := range []string{d0, d1, d2, d4} {
		if occurrenceOn(t, left, date).Date != date {
			t.Fatalf("lost %s", date)
		}
	}
	for _, o := range left {
		if o.Date == d3 {
			t.Fatalf("dropped date still listed: %+v", o)
		}
	}
	var cancelled models.EventOccurrence
	if err := app.DB.Where("event_id = ? AND original_date = ?", created.Event.ID, d3).First(&cancelled).Error; err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != models.OccurrenceCancelled {
		t.Fatalf("status %s", cancelled.Status)
	}
	var kept int64
	app.DB.Model(&models.Registration{}).Where("occurrence_id = ? AND user_id = ?", cancelled.ID, patUser.ID).Count(&kept)
	if kept != 1 {
		t.Fatalf("cancelled registration rows: %d", kept)
	}

	assertNotice := func(c *apiClient, want bool) {
		t.Helper()
		ns := feed(t, c, "")
		found := false
		for _, n := range ns {
			if n.Kind == "event.cancelled" && n.Title == "Cancelled: Prayer" {
				found = true
			}
		}
		if found != want {
			t.Fatalf("notice found=%v want=%v in %+v", found, want, ns)
		}
	}
	// M1.5: cancellation tells going and waitlisted, not maybe. Pat was
	// maybe on the dropped date; Sam was going on it and on a kept date.
	assertNotice(pat, false)
	assertNotice(sam, true)
}

func TestPastOccurrenceStaysOnSeriesEdit(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "History")
	start := atUTC(time.Now().AddDate(0, 0, -35), 19, 0)
	w := admin.do("POST", "/api/v1/groups/"+gid+"/events", map[string]any{
		"startsLocal": start.Format("2006-01-02T15:04"), "durationMinutes": 60,
		"tzid": "UTC", "recurrenceRule": weeklyRule(start),
	}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Event eventJSON `json:"event"`
	}
	decodeJSON(t, w, &created)
	w = admin.do("PUT", "/api/v1/events/"+itoa(created.Event.ID), map[string]any{
		"startsLocal": start.Format("2006-01-02") + "T20:00",
	}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", w.Code, w.Body.String())
	}
	list := listOccurrences(t, admin, gid, start.Format("2006-01-02"), time.Now().AddDate(0, 0, 21).Format("2006-01-02"))
	var sawPast, sawFuture bool
	now := time.Now()
	for _, o := range list {
		want := "T19:00"
		if o.StartsAt.After(now) {
			want = "T20:00"
			sawFuture = true
		} else {
			sawPast = true
		}
		if !strings.HasSuffix(o.StartsLocal, want) {
			t.Fatalf("%s starts %s (at %s), want %s", o.Date, o.StartsLocal, o.StartsAt, want)
		}
	}
	if !sawPast || !sawFuture {
		t.Fatalf("past %v future %v (%d rows)", sawPast, sawFuture, len(list))
	}
}

// The daily extender only inserts dates that are missing. A second run, and
// a run that cannot take the advisory lock, leaves cancelled rows cancelled.
func TestExtendInsertsOnlyMissingDates(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "Horizon")
	start := atUTC(time.Now().AddDate(0, 0, 7), 19, 0)
	w := admin.do("POST", "/api/v1/groups/"+gid+"/events", map[string]any{
		"startsLocal": start.Format("2006-01-02T15:04"), "durationMinutes": 60,
		"tzid": "UTC", "recurrenceRule": weeklyRule(start),
	}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Event eventJSON `json:"event"`
	}
	decodeJSON(t, w, &created)

	var n int64
	app.DB.Model(&models.EventOccurrence{}).Where("event_id = ?", created.Event.ID).Count(&n)
	if n < 70 || n > 90 {
		t.Fatalf("18-month weekly count %d", n)
	}
	if err := eventdates.Extend(app.DB, time.Now().UTC(), nil); err != nil {
		t.Fatal(err)
	}
	var again int64
	app.DB.Model(&models.EventOccurrence{}).Where("event_id = ?", created.Event.ID).Count(&again)
	if again != n {
		t.Fatalf("extend rewrote the series: %d then %d", n, again)
	}

	var cancel models.EventOccurrence
	if err := app.DB.Where("event_id = ? AND original_date = ?", created.Event.ID, start.Format("2006-01-02")).First(&cancel).Error; err != nil {
		t.Fatal(err)
	}
	app.DB.Model(&cancel).Update("status", models.OccurrenceCancelled)
	if err := eventdates.Extend(app.DB, time.Now().UTC(), nil); err != nil {
		t.Fatal(err)
	}
	var kept models.EventOccurrence
	if err := app.DB.First(&kept, cancel.ID).Error; err != nil {
		t.Fatal(err)
	}
	if kept.Status != models.OccurrenceCancelled {
		t.Fatalf("extend un-cancelled: %s", kept.Status)
	}

	var drop models.EventOccurrence
	if err := app.DB.Where("event_id = ? AND original_date <> ?", created.Event.ID, models.FormatCalendarDate(cancel.OriginalDate)).First(&drop).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Delete(&drop).Error; err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	sqlDB, err := app.DB.DB()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var got bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", eventdates.LockID).Scan(&got); err != nil || !got {
		t.Fatalf("lock: got %v err %v", got, err)
	}
	if err := eventdates.ExtendLocked(ctx, app.DB, time.Now().UTC(), nil); err != nil {
		t.Fatal(err)
	}
	var missing int64
	app.DB.Model(&models.EventOccurrence{}).Where("event_id = ? AND original_date = ?", created.Event.ID, models.FormatCalendarDate(drop.OriginalDate)).Count(&missing)
	if missing != 0 {
		t.Fatal("extend ran while the advisory lock was held")
	}
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", eventdates.LockID); err != nil {
		t.Fatal(err)
	}
	if err := eventdates.ExtendLocked(ctx, app.DB, time.Now().UTC(), nil); err != nil {
		t.Fatal(err)
	}
	app.DB.Model(&models.EventOccurrence{}).Where("event_id = ? AND original_date = ?", created.Event.ID, models.FormatCalendarDate(drop.OriginalDate)).Count(&missing)
	if missing != 1 {
		t.Fatalf("missing date not restored: %d", missing)
	}
}

// DEC-48: an event copies the group's visibility at creation and keeps its
// own value. Opening the group never changes events; making it more
// private tightens inherited events.
func TestEventKeepsItsOwnVisibility(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	gid := createGroupAs(t, admin, "Choir", models.VisibilityPrivate)
	rehearsal := scheduleGroupEvent(t, admin, gid, "Rehearsal")
	if rehearsal.Visibility != models.VisibilityPrivate || rehearsal.LeaderOpened {
		t.Fatalf("copied at creation: %+v", rehearsal)
	}
	// Setting the group's own value is not an override.
	setVisibility(t, admin, rehearsal.ID, models.VisibilityPrivate, "", http.StatusOK)
	var overrides int64
	app.DB.Model(&models.AuditLog{}).Where("action = ? AND target_id = ?", audit.VisibilityOverride, rehearsal.ID).Count(&overrides)
	if overrides != 0 {
		t.Fatal("matching the group is not an override")
	}

	// More open group: events don't move.
	apiDo(t, admin, "PUT", "/api/v1/groups/"+itoa(gid), map[string]any{"visibility": models.VisibilityPublic}, http.StatusOK)
	page, _ := getEvent(t, admin, rehearsal.ID, http.StatusOK)
	if page.Event.Visibility != models.VisibilityPrivate {
		t.Fatalf("opening the group changed the event: %+v", page.Event)
	}
	guest := newAPIClient(t, app)
	getEvent(t, guest, rehearsal.ID, http.StatusNotFound)

	// A stricter value is audited without a notice.
	concert := scheduleGroupEvent(t, admin, gid, "Concert")
	setVisibility(t, admin, concert.ID, models.VisibilityPrivate, "", http.StatusOK)
	var changed int64
	app.DB.Model(&models.AuditLog{}).Where("action = ? AND target_id = ?", audit.VisibilityChanged, concert.ID).Count(&changed)
	if changed != 1 {
		t.Fatal("stricter change was not audited")
	}
	if hasVisibilityNotice(noticeIDs(t, admin), "Concert", "private") {
		t.Fatal("stricter change notified")
	}

	// More private group: inherited events tighten, no decision needed.
	apiDo(t, admin, "PUT", "/api/v1/groups/"+itoa(gid), map[string]any{"visibility": models.VisibilityInviteOnly}, http.StatusOK)
	for _, id := range []uint{rehearsal.ID, concert.ID} {
		page, _ = getEvent(t, admin, id, http.StatusOK)
		if page.Event.Visibility != models.VisibilityInviteOnly || page.Event.LeaderOpened {
			t.Fatalf("not tightened: %+v", page.Event)
		}
	}
}

func TestChurchWideHosts(t *testing.T) {
	app := testApp(t)
	adminUser := newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	hostUser := newTestUser(t, app, "host", "password1234", models.RoleMember)
	host := loginAPI(t, app, "host", "password1234")
	guest := newAPIClient(t, app)

	apiDo(t, host, "POST", "/api/v1/events", map[string]any{
		"title": "Nope", "startsLocal": "2026-12-01T19:00", "durationMinutes": 60,
	}, http.StatusForbidden)
	apiDo(t, admin, "POST", "/api/v1/events", map[string]any{
		"title": "Nope", "startsLocal": "2026-12-01T19:00", "durationMinutes": 60,
		"visibility": "invite_only",
	}, http.StatusBadRequest)

	w := admin.do("POST", "/api/v1/events", map[string]any{
		"title": "Potluck", "startsLocal": "2026-12-01T19:00", "durationMinutes": 90,
		"tzid": "UTC", "hosts": []string{hostUser.Email},
	}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Event eventJSON `json:"event"`
	}
	decodeJSON(t, w, &created)
	ev := created.Event
	if !ev.ChurchWide || ev.GroupID != nil || ev.HostedBy != "Fold" || ev.Visibility != models.VisibilityPublic {
		t.Fatalf("church event: %+v", ev)
	}
	if len(ev.Hosts) != 2 {
		t.Fatalf("hosts: %+v", ev.Hosts)
	}
	got := map[uint]bool{}
	for _, h := range ev.Hosts {
		got[h.UserID] = true
		if strings.Contains(h.Email, " ") {
			t.Fatalf("host email: %+v", h)
		}
	}
	if !got[adminUser.ID] || !got[hostUser.ID] {
		t.Fatalf("creator was not kept as a host: %+v", ev.Hosts)
	}

	// The host list is an admin view. A guest sees the event and no names or emails.
	guestPage, guestRaw := getEvent(t, guest, ev.ID, http.StatusOK)
	if guestPage.Event.HostedBy != "Fold" || len(guestPage.Event.Hosts) != 0 || strings.Contains(guestRaw, "admin@example.com") {
		t.Fatalf("guest church event: %s", guestRaw)
	}
	if guestPage.CanManage || guestPage.CanSetVisibility {
		t.Fatalf("guest permissions: %+v", guestPage)
	}

	apiDo(t, admin, "DELETE", fmt.Sprintf("/api/v1/events/%d/hosts/%d", ev.ID, adminUser.ID), nil, http.StatusOK)
	apiDo(t, admin, "DELETE", fmt.Sprintf("/api/v1/events/%d/hosts/%d", ev.ID, hostUser.ID), nil, http.StatusBadRequest)

	page, _ := getEvent(t, host, ev.ID, http.StatusOK)
	if !page.CanManage || !page.CanSetVisibility {
		t.Fatalf("host permissions: %+v", page)
	}
	apiDo(t, host, "PUT", fmt.Sprintf("/api/v1/events/%d", ev.ID), map[string]any{"title": "Christmas Potluck"}, http.StatusOK)
	apiDo(t, host, "PUT", fmt.Sprintf("/api/v1/events/%d/visibility", ev.ID), map[string]any{
		"visibility": "private", "source": "custom",
	}, http.StatusOK)
	getEvent(t, guest, ev.ID, http.StatusNotFound)
	apiDo(t, host, "PUT", fmt.Sprintf("/api/v1/events/%d/visibility", ev.ID), map[string]any{
		"visibility": "invite_only", "source": "custom",
	}, http.StatusBadRequest)
	apiDo(t, host, "POST", fmt.Sprintf("/api/v1/events/%d/hosts", ev.ID), map[string]any{"email": adminUser.Email}, http.StatusForbidden)

	// A host of this event cannot manage another event or a group they can't see.
	pub := createGroupAs(t, admin, "Public Choir", models.VisibilityPublic)
	other := scheduleGroupEvent(t, admin, pub, "Rehearsal")
	apiDo(t, host, "PUT", fmt.Sprintf("/api/v1/events/%d", other.ID), map[string]any{"title": "Taken"}, http.StatusForbidden)
	hidden := createGroupAs(t, admin, "Hidden Care", models.VisibilityInviteOnly)
	secret := scheduleGroupEvent(t, admin, hidden, "Secret")
	apiDo(t, host, "GET", fmt.Sprintf("/api/v1/events/%d", secret.ID), nil, http.StatusNotFound)
	apiDo(t, host, "PUT", fmt.Sprintf("/api/v1/events/%d", secret.ID), map[string]any{"title": "Taken"}, http.StatusNotFound)

	// Switch back so a signed-in non-host can see registrants' names.
	apiDo(t, host, "PUT", fmt.Sprintf("/api/v1/events/%d/visibility", ev.ID), map[string]any{
		"visibility": "public", "source": "custom",
	}, http.StatusOK)
	apiDo(t, host, "PUT", fmt.Sprintf("/api/v1/events/%d/registrations", ev.ID), map[string]any{
		"status": "going", "date": "2026-12-01",
	}, http.StatusOK)
	reader := newTestUser(t, app, "reader", "password1234", models.RoleMember)
	_ = reader
	readerC := loginAPI(t, app, "reader", "password1234")
	seen, raw := getEvent(t, readerC, ev.ID, http.StatusOK)
	if !seen.Occurrences[0].AttendeesVisible || !strings.Contains(raw, `"name":"host"`) {
		t.Fatalf("signed-in names: %s", raw)
	}
	if seen.CanManage {
		t.Fatal("a non-host can manage the event")
	}
}

func TestEventDescription(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")

	apiDo(t, admin, "POST", "/api/v1/events", map[string]any{
		"title": "Too long", "startsLocal": "2026-12-02T19:00", "durationMinutes": 60,
		"description": strings.Repeat("a", descriptionField.max+1),
	}, http.StatusBadRequest)

	w := admin.do("POST", "/api/v1/events", map[string]any{
		"title": "Potluck", "startsLocal": "2026-12-02T19:00", "durationMinutes": 90,
		"description": "  Bring a salad.  ",
	}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Event eventJSON `json:"event"`
	}
	decodeJSON(t, w, &created)
	if created.Event.Description != "Bring a salad." {
		t.Fatalf("description: %q", created.Event.Description)
	}
	page, _ := getEvent(t, admin, created.Event.ID, http.StatusOK)
	if page.Event.Description != "Bring a salad." || page.Occurrences[0].Description != "Bring a salad." {
		t.Fatalf("page: %+v occ %+v", page.Event.Description, page.Occurrences[0].Description)
	}

	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d", created.Event.ID), map[string]any{
		"description": "Dessert too.",
	}, http.StatusOK)
	page, _ = getEvent(t, admin, created.Event.ID, http.StatusOK)
	if page.Event.Description != "Dessert too." || page.Event.Title != "Potluck" {
		t.Fatalf("update: %+v", page.Event)
	}

	gid := createGroupAs(t, admin, "Choir", "public")
	raw := apiDo(t, admin, "POST", fmt.Sprintf("/api/v1/groups/%d/events", gid), map[string]any{
		"title": "Rehearsal", "startsLocal": "2026-12-03T19:00", "durationMinutes": 60,
		"description": "Music stands are in the closet.",
	}, http.StatusCreated)
	if !strings.Contains(raw, "Music stands are in the closet.") {
		t.Fatalf("group event: %s", raw)
	}
}

// Regression (Grier, Oct 14): a second series in a group must copy only the
// group's visibility, never a sibling series' override.
func TestNewSeriesCopiesOnlyGroupVisibility(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	w := admin.do("POST", "/api/v1/groups", map[string]any{"name": "Choir", "visibility": "private"}, true)
	var created struct {
		Group groupSummaryJSON `json:"group"`
	}
	decodeJSON(t, w, &created)
	gid := itoa(created.Group.ID)
	admin.do("POST", "/api/v1/admin/groups/"+gid+"/approve", map[string]any{}, true)

	start := time.Now().AddDate(0, 0, 3).Format("2006-01-02") + "T19:00"
	mk := func(title string) uint {
		w := admin.do("POST", "/api/v1/groups/"+gid+"/events", map[string]any{
			"title": title, "startsLocal": start, "durationMinutes": 60, "tzid": "America/Boise",
			"recurrenceRule": "FREQ=WEEKLY;BYDAY=MO",
		}, true)
		if w.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", title, w.Code, w.Body.String())
		}
		var r struct {
			Event struct {
				ID uint `json:"id"`
			} `json:"event"`
		}
		decodeJSON(t, w, &r)
		return r.Event.ID
	}
	first := mk("First")
	if w := admin.do("PUT", "/api/v1/events/"+itoa(first)+"/visibility", map[string]any{"visibility": "public", "reason": "open house"}, true); w.Code != http.StatusOK {
		t.Fatalf("open first: %d %s", w.Code, w.Body.String())
	}
	second := mk("Second")

	var ev models.Event
	app.DB.First(&ev, second)
	if ev.Visibility != "private" || ev.LeaderOpened {
		t.Fatalf("second series: visibility=%s leaderOpened=%v", ev.Visibility, ev.LeaderOpened)
	}
	w = admin.do("GET", "/api/v1/groups/"+gid, nil, false)
	var detail struct {
		UpcomingEvents []occurrenceJSON `json:"upcomingEvents"`
	}
	decodeJSON(t, w, &detail)
	seen := false
	for _, o := range detail.UpcomingEvents {
		if o.EventID == second {
			seen = true
			if o.Visibility != "private" || o.LeaderOpened {
				t.Fatalf("second series date %s: visibility=%s leaderOpened=%v", o.Date, o.Visibility, o.LeaderOpened)
			}
		}
	}
	if !seen {
		t.Fatal("second series not listed")
	}
}

// DEC-51: visibility is chosen at creation. Wider than the group goes
// through the override path (audit row, notice, leader_opened).
func TestCreateEventWithVisibility(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	newTestUser(t, app, "admin2", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	w := admin.do("POST", "/api/v1/groups", map[string]any{"name": "Choir", "visibility": "private"}, true)
	var created struct {
		Group groupSummaryJSON `json:"group"`
	}
	decodeJSON(t, w, &created)
	gid := itoa(created.Group.ID)
	start := time.Now().AddDate(0, 0, 3).Format("2006-01-02") + "T19:00"
	mk := func(body map[string]any) models.Event {
		body["startsLocal"], body["durationMinutes"], body["tzid"] = start, 60, "America/Boise"
		w := admin.do("POST", "/api/v1/groups/"+gid+"/events", body, true)
		if w.Code != http.StatusCreated {
			t.Fatalf("create: %d %s", w.Code, w.Body.String())
		}
		var r struct {
			Event struct {
				ID uint `json:"id"`
			} `json:"event"`
		}
		decodeJSON(t, w, &r)
		var ev models.Event
		app.DB.First(&ev, r.Event.ID)
		return ev
	}
	plain := mk(map[string]any{"title": "Plain"})
	if plain.Visibility != "private" || plain.LeaderOpened {
		t.Fatalf("default: %s %v", plain.Visibility, plain.LeaderOpened)
	}
	opened := mk(map[string]any{"title": "Open house", "visibility": "public", "visibilityReason": "guests welcome"})
	if opened.Visibility != "public" || !opened.LeaderOpened {
		t.Fatalf("opened: %s %v", opened.Visibility, opened.LeaderOpened)
	}
	var n int64
	app.DB.Model(&models.AuditLog{}).Where("action = ? AND target_id = ?", "event.visibility_override", opened.ID).Count(&n)
	if n != 1 {
		t.Fatalf("override audit rows: %d", n)
	}
	app.DB.Model(&models.Notification{}).Where("kind = ?", "event.visibility_override").Count(&n)
	if n == 0 {
		t.Fatal("no override notice")
	}
	tight := mk(map[string]any{"title": "Members", "visibility": "invite_only"})
	if tight.Visibility != "invite_only" || tight.LeaderOpened {
		t.Fatalf("tightened: %s %v", tight.Visibility, tight.LeaderOpened)
	}
	if w := admin.do("POST", "/api/v1/groups/"+gid+"/events", map[string]any{"startsLocal": start, "durationMinutes": 60, "visibility": "secret"}, true); w.Code != http.StatusBadRequest {
		t.Fatalf("bad visibility: %d", w.Code)
	}
}

// DEC-64: who said "can't go" (count and names) is for the group's leaders
// and admins only. A member and a signed-out guest get no declined data:
// no "declined" count key and no declined attendee.
func TestDeclinedOnlyForLeadersAndAdmins(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	gid := createGroupAs(t, admin, "Open Choir", models.VisibilityPublic)
	var g models.Group
	if err := app.DB.First(&g, gid).Error; err != nil {
		t.Fatal(err)
	}
	ev := scheduleGroupEvent(t, admin, gid, "Rehearsal")
	_, leader := mkUser(t, app, &g, "lead")
	leadUser := models.User{}
	app.DB.Where("name = ?", "lead").First(&leadUser)
	app.DB.Model(&models.Membership{}).Where("user_id = ? AND group_id = ?", leadUser.ID, gid).Update("role", models.MembershipLeader)
	_, member := mkUser(t, app, &g, "mem")
	_, decliner := mkUser(t, app, &g, "nope")
	_, goer := mkUser(t, app, &g, "yes")
	guest := newAPIClient(t, app)
	path := fmt.Sprintf("/api/v1/events/%d/registrations", ev.ID)
	apiDo(t, decliner, "PUT", path, map[string]any{"status": "declined", "date": "2026-12-01"}, http.StatusOK)
	apiDo(t, goer, "PUT", path, map[string]any{"status": "going", "date": "2026-12-01"}, http.StatusOK)

	for _, tc := range []struct {
		who  string
		c    *apiClient
		sees bool
	}{{"leader", leader, true}, {"admin", admin, true}, {"member", member, false}, {"guest", guest, false}} {
		page, raw := getEvent(t, tc.c, ev.ID, http.StatusOK)
		o := page.Occurrences[0]
		n, hasKey := o.Registrations["declined"]
		named := strings.Contains(raw, `"name":"nope"`)
		if tc.sees && (!hasKey || n != 1 || !named) {
			t.Fatalf("%s should see the declined count and name: %s", tc.who, raw)
		}
		if !tc.sees && (hasKey || named || strings.Contains(raw, `"declined"`)) {
			t.Fatalf("%s got declined data: %s", tc.who, raw)
		}
		if o.Registrations["going"] != 1 {
			t.Fatalf("%s going count: %+v", tc.who, o.Registrations)
		}
	}
}

func TestChildcareEventValidationAndNullCapacity(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "ChildcareFields")
	id, _ := seedChildcareEvent(t, app, admin, gid, nil, intPtr(5), false)
	path := "/api/v1/events/" + itoa(id)
	for _, fields := range []map[string]any{
		{"childcareCapacity": 0}, {"childcareCapacity": -1}, {"childcareCapacity": 1.5}, {"childcareCapacity": "3"}, {"childcareCapacity": 100001},
		{"childcareNotes": strings.Repeat("x", 501)}, {"childcareAges": strings.Repeat("x", 101)},
	} {
		apiDo(t, admin, "PUT", path, fields, http.StatusBadRequest)
		fields["startsLocal"], fields["durationMinutes"], fields["tzid"] = videoEventStart().Format("2006-01-02T15:04"), 60, "UTC"
		apiDo(t, admin, "POST", "/api/v1/groups/"+gid+"/events", fields, http.StatusBadRequest)
		fields["title"] = "Church childcare"
		apiDo(t, admin, "POST", "/api/v1/events", fields, http.StatusBadRequest)
	}
	ev := eventRow(t, app, id)
	if ev.ChildcareCapacity == nil || *ev.ChildcareCapacity != 5 {
		t.Fatal("bad update changed capacity")
	}
	// An unrelated edit preserves the limit. Explicit null clears it.
	apiDo(t, admin, "PUT", path, map[string]any{"title": "With childcare"}, http.StatusOK)
	if ev = eventRow(t, app, id); ev.ChildcareCapacity == nil || *ev.ChildcareCapacity != 5 {
		t.Fatal("omitted limit cleared it")
	}
	apiDo(t, admin, "PUT", path, map[string]any{"childcareCapacity": nil}, http.StatusOK)
	if ev = eventRow(t, app, id); ev.ChildcareCapacity != nil {
		t.Fatal("null did not clear limit")
	}
	w := admin.do("POST", "/api/v1/events", map[string]any{"title": "Childcare church event", "startsLocal": videoEventStart().Format("2006-01-02T15:04"), "durationMinutes": 60, "tzid": "UTC", "childcareAvailable": true, "childcareCapacity": 3, "childcareAges": "2–5", "childcareNotes": "Nursery"}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("church create: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Event eventJSON `json:"event"`
	}
	decodeJSON(t, w, &created)
	if !created.Event.ChildcareAvailable || created.Event.ChildcareCapacity == nil || *created.Event.ChildcareCapacity != 3 || created.Event.ChildcareNotes != "Nursery" {
		t.Fatalf("childcare fields: %+v", created.Event)
	}
}

func TestChildcareVisibilityAndOwnRequest(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "ChildcarePrivacy")
	id, date := seedChildcareEvent(t, app, admin, gid, nil, intPtr(5), false)
	var group models.Group
	if err := app.DB.First(&group, "id = ?", gid).Error; err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"parent", "member", "leader", "outsider"} {
		u := newTestUser(t, app, name, "password1234", models.RoleMember)
		if name != "outsider" {
			role := models.MembershipMember
			if name == "leader" {
				role = models.MembershipLeader
			}
			addMember(t, app, u, &group, role)
		}
	}
	parent := loginAPI(t, app, "parent", "password1234")
	childcareAs(t, parent, id, date, "going", 2, http.StatusOK)
	for _, name := range []string{"parent", "member", "leader", "outsider", "admin", "signed-out"} {
		who := admin
		if name == "signed-out" {
			who = newAPIClient(t, app)
		} else if name != "admin" {
			who = loginAPI(t, app, name, "password1234")
		}
		page, raw := getEvent(t, who, id, http.StatusOK)
		if len(page.Occurrences) == 0 {
			t.Fatal("no event occurrences")
		}
		inst := page.Occurrences[0]
		manager := name == "leader" || name == "admin"
		if !manager {
			for _, key := range []string{`"childcareUsed"`, `"childcareSpotsLeft"`, `"childcareCount"`, `"childcareAgesNote"`} {
				if strings.Contains(raw, key) {
					t.Fatalf("%s got private field %s: %s", name, key, raw)
				}
			}
		}
		if manager && len(inst.Attendees) == 0 {
			t.Fatal("manager got no attendees")
		}
		if !inst.ChildcareAvailable || inst.ChildcareAges == "" || inst.ChildcareNotes == "" || inst.ChildcareCapacity == nil {
			t.Fatalf("%s missing public childcare: %+v", name, inst)
		}
		if inst.ChildcareFull || !strings.Contains(raw, `"childcareFull":false`) {
			t.Fatalf("%s missing public not-full flag: %s", name, raw)
		}
		if (inst.ChildcareUsed != nil) != manager || (inst.ChildcareSpotsLeft != nil) != manager {
			t.Fatalf("%s totals visibility: %+v", name, inst)
		}
		for _, a := range inst.Attendees {
			if (a.ChildcareCount != nil) != manager || (a.ChildcareAgesNote != nil) != manager {
				t.Fatalf("%s attendee request visibility: %+v", name, a)
			}
		}
		if name == "parent" {
			if inst.MyChildcareCount == nil || *inst.MyChildcareCount != 2 || inst.MyChildcareAgesNote == nil || *inst.MyChildcareAgesNote != "3 and 5" {
				t.Fatalf("own request missing: %+v", inst)
			}
		} else if inst.MyChildcareCount != nil || inst.MyChildcareAgesNote != nil {
			t.Fatalf("%s got another person's own request", name)
		}
	}
	// A church-event host has the same childcare access as group leaders.
	w := admin.do("POST", "/api/v1/events", map[string]any{"title": "Church night", "startsLocal": videoEventStart().Format("2006-01-02T15:04"), "durationMinutes": 60, "tzid": "UTC", "hosts": []string{testEmail("outsider")}, "childcareAvailable": true}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("church event: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Event eventJSON `json:"event"`
	}
	decodeJSON(t, w, &created)
	host := eventOccurrence(t, loginAPI(t, app, "outsider", "password1234"), created.Event.ID)
	if host.ChildcareUsed == nil || *host.ChildcareUsed != 0 {
		t.Fatalf("host childcare access: %+v", host)
	}
}

func TestChildcareFullForEveryViewer(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "ChildcareFull")
	id, date := seedChildcareEvent(t, app, admin, gid, nil, intPtr(2), false)
	parent := newTestUser(t, app, "parent", "password1234", models.RoleMember)
	childcareAs(t, loginAPI(t, app, "parent", "password1234"), id, date, "going", 2, http.StatusOK)
	var group models.Group
	if err := app.DB.First(&group, "id = ?", gid).Error; err != nil {
		t.Fatal(err)
	}
	viewers := map[string]*apiClient{"signed-out": newAPIClient(t, app), "admin": admin}
	for _, role := range []string{models.MembershipMember, models.MembershipLeader} {
		u := newTestUser(t, app, role, "password1234", models.RoleMember)
		addMember(t, app, u, &group, role)
		viewers[role] = loginAPI(t, app, role, "password1234")
	}
	for _, tt := range []struct {
		name       string
		available  bool
		capacity   *int
		status     string
		userStatus string
		full       bool
	}{
		{"at capacity", true, intPtr(2), models.RegGoing, models.UserActive, true},
		{"over capacity", true, intPtr(1), models.RegGoing, models.UserActive, true},
		{"space left", true, intPtr(3), models.RegGoing, models.UserActive, false},
		{"unlimited", true, nil, models.RegGoing, models.UserActive, false},
		{"not offered", false, intPtr(2), models.RegGoing, models.UserActive, false},
		{"maybe does not count", true, intPtr(2), models.RegMaybe, models.UserActive, false},
		{"waitlisted does not count", true, intPtr(2), models.RegWaitlisted, models.UserActive, false},
		{"disabled does not count", true, intPtr(2), models.RegGoing, models.UserDisabled, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := app.DB.Model(&models.Event{}).Where("id = ?", id).Updates(map[string]any{
				"childcare_available": tt.available, "childcare_capacity": tt.capacity,
			}).Error; err != nil {
				t.Fatal(err)
			}
			if err := app.DB.Model(&models.Registration{}).Where("user_id = ?", parent.ID).Update("status", tt.status).Error; err != nil {
				t.Fatal(err)
			}
			if err := app.DB.Model(&models.User{}).Where("id = ?", parent.ID).Update("status", tt.userStatus).Error; err != nil {
				t.Fatal(err)
			}
			for name, viewer := range viewers {
				page, raw := getEvent(t, viewer, id, http.StatusOK)
				if len(page.Occurrences) != 1 || page.Occurrences[0].ChildcareFull != tt.full ||
					!strings.Contains(raw, fmt.Sprintf(`"childcareFull":%t`, tt.full)) {
					t.Fatalf("%s full=%t: %s", name, tt.full, raw)
				}
				if name == "signed-out" || name == models.MembershipMember {
					for _, key := range []string{`"childcareUsed"`, `"childcareSpotsLeft"`, `"childcareCount"`, `"childcareAgesNote"`} {
						if strings.Contains(raw, key) {
							t.Fatalf("%s got private field %s: %s", name, key, raw)
						}
					}
				}
			}
		})
	}
}

func TestChildcareTotalsQueriedOnlyWhenNeeded(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "ChildcareQueries")
	id, _ := seedChildcareEvent(t, app, admin, gid, nil, nil, false)
	for _, tt := range []struct {
		name      string
		available bool
		capacity  *int
		manager   bool
		queries   int
	}{
		{"off for public", false, intPtr(2), false, 0},
		{"off for manager", false, intPtr(2), true, 0},
		{"unlimited for public", true, nil, false, 0},
		{"unlimited for manager", true, nil, true, 1},
		{"limited for public", true, intPtr(2), false, 1},
		{"limited for manager", true, intPtr(2), true, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := app.DB.Model(&models.Event{}).Where("id = ?", id).Updates(map[string]any{
				"childcare_available": tt.available, "childcare_capacity": tt.capacity,
			}).Error; err != nil {
				t.Fatal(err)
			}
			queries := 0
			const name = "test:count_childcare_totals"
			if err := app.DB.Callback().Row().Before("gorm:row").Register(name, func(tx *gorm.DB) {
				if strings.Contains(strings.Join(tx.Statement.Selects, " "), "SUM(registrations.childcare_count") {
					queries++
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = app.DB.Callback().Row().Remove(name) })
			viewer := newAPIClient(t, app)
			if tt.manager {
				viewer = admin
			}
			getEvent(t, viewer, id, http.StatusOK)
			if queries != tt.queries {
				t.Fatalf("childcare total queries=%d, want %d", queries, tt.queries)
			}
		})
	}
}

func TestOccurrenceCountErrorsKeepDates(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "CountFallbacks")
	id, date := seedChildcareEvent(t, app, admin, gid, nil, intPtr(2), false)
	apiDo(t, admin, "PUT", "/api/v1/events/"+itoa(id), map[string]any{"remoteAvailable": true, "remoteCapacity": 3}, http.StatusOK)
	apiDo(t, admin, "PUT", "/api/v1/events/"+itoa(id)+"/registrations", map[string]any{
		"date": date, "status": "going", "attendanceMode": "remote", "childcareCount": 2,
	}, http.StatusOK)
	for _, pool := range []string{"childcare", "seats"} {
		t.Run(pool, func(t *testing.T) {
			failed := 0
			const name = "test:fail_occurrence_totals"
			if err := app.DB.Callback().Row().Before("gorm:row").Register(name, func(tx *gorm.DB) {
				selection := strings.Join(tx.Statement.Selects, " ")
				if (pool == "childcare" && strings.Contains(selection, "SUM(registrations.childcare_count")) ||
					(pool == "seats" && strings.Contains(selection, "SUM(1 + registrations.guest_count)")) {
					failed++
					_ = tx.AddError(errors.New("count unavailable"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = app.DB.Callback().Row().Remove(name) })
			for _, viewer := range []*apiClient{admin, newAPIClient(t, app)} {
				page, raw := getEvent(t, viewer, id, http.StatusOK)
				if len(page.Occurrences) != 1 {
					t.Fatalf("count error lost the date: %s", raw)
				}
				inst := page.Occurrences[0]
				if inst.Date != date || inst.MyRegistration != nil && *inst.MyRegistration != models.RegGoing {
					t.Fatalf("count error lost registration details: %+v", inst)
				}
				if pool == "childcare" && (inst.ChildcareFull || inst.ChildcareUsed != nil && *inst.ChildcareUsed != 0) {
					t.Fatalf("childcare did not fall back to zero: %+v", inst)
				}
				if pool == "seats" && (inst.SeatsUsed != 0 || inst.RemoteSeatsUsed != 0 || !inst.ChildcareFull) {
					t.Fatalf("seat counts did not fall back independently: %+v", inst)
				}
			}
			if failed == 0 {
				t.Fatal("count failure was not exercised")
			}
		})
	}
}

func TestHouseholdAttendeesArePrivateToHouseholdAndManagers(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "HouseholdPrivacy")
	eventID, date := seedChildcareEvent(t, app, admin, gid, nil, intPtr(1), true)
	_, c, spouse, child := householdFamily(t, app, admin, "privacyholder")
	apiDo(t, c, "PUT", "/api/v1/me/household", map[string]any{"address": "Private household address"}, http.StatusOK)
	batchAnswer(t, c, eventID, date, map[string]any{"people": []map[string]any{{"personId": child.ID, "status": "going", "childcareRequested": true}, {"personId": spouse.ID, "status": "going"}}}, http.StatusOK)
	newTestUser(t, app, "unrelated", "password1234", models.RoleMember)
	ordinary := loginAPI(t, app, "unrelated", "password1234")
	for _, viewer := range []*apiClient{ordinary, newAPIClient(t, app)} {
		page, raw := getEvent(t, viewer, eventID, http.StatusOK)
		if page.Occurrences[0].Registrations[models.RegGoing] != 2 {
			t.Fatalf("counts %s", raw)
		}
		for _, secret := range []string{`"householdPerson"`, "Kit", "Mary", "Private allergy note", "Private household address", "2020-05-01"} {
			if strings.Contains(raw, secret) {
				t.Fatalf("private data %q: %s", secret, raw)
			}
		}
	}
	// Admin gets names and integer ages, with no date/address; a manager's
	// care notes are separately bounded to this going occurrence.
	page, raw := getEvent(t, admin, eventID, http.StatusOK)
	if len(page.Occurrences[0].Attendees) != 2 {
		t.Fatalf("mixed subjects %s", raw)
	}
	for _, secret := range []string{"Private household address", "2020-05-01", `"userId":0`} {
		if strings.Contains(raw, secret) {
			t.Fatalf("bad projection %s", raw)
		}
	}
	var group models.Group
	app.DB.Where("id = ?", gid).First(&group)
	leader := newTestUser(t, app, "eventleader", "password1234", models.RoleMember)
	addMember(t, app, leader, &group, models.MembershipLeader)
	lc := loginAPI(t, app, "eventleader", "password1234")
	_, raw = getEvent(t, lc, eventID, http.StatusOK)
	if strings.Contains(raw, "Private allergy note") {
		t.Fatal("care before window")
	}
	now := time.Now().UTC()
	app.DB.Model(&models.EventOccurrence{}).Where("event_id = ?", eventID).Updates(map[string]any{"starts_at": now.Add(time.Hour), "ends_at": now.Add(2 * time.Hour)})
	_, raw = getEvent(t, lc, eventID, http.StatusOK)
	if !strings.Contains(raw, "Private allergy note") || !strings.Contains(raw, "careNotesVisibleUntil") {
		t.Fatalf("missing windowed care %s", raw)
	}
}

func TestOpenEventsSiteNameReadBudget(t *testing.T) {
	app := testApp(t)
	creator := newTestUser(t, app, "creator", "pw", models.RoleAdmin)
	if err := app.DB.Create(&models.Setting{Key: "site_name", Value: "Our Church"}).Error; err != nil {
		t.Fatal(err)
	}
	client := newAPIClient(t, app)
	client.do("GET", "/api/v1/events", nil, false) // Warm the setup-complete cache.
	settings := countSiteNameReads(t, app.DB)
	start := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Minute)
	var baseline int64
	for i := 1; i <= 30; i++ {
		ev := models.Event{Title: "Church-wide", Visibility: models.VisibilityPublic, StartsLocal: start.Format("2006-01-02T15:04"), TZID: "UTC", DurationMinutes: 60, CreatedBy: creator.ID}
		if err := app.DB.Create(&ev).Error; err != nil {
			t.Fatal(err)
		}
		o := models.EventOccurrence{EventID: ev.ID, OriginalDate: calDate(start.Format("2006-01-02")), StartsAt: start, EndsAt: start.Add(time.Hour), Status: models.OccurrenceScheduled}
		if err := app.DB.Create(&o).Error; err != nil {
			t.Fatal(err)
		}
		if i != 3 && i != 30 {
			continue
		}
		*settings = 0
		counter := countDBReads(t, app.DB)
		w := client.do("GET", "/api/v1/events?from="+start.Format("2006-01-02")+"&to="+start.Format("2006-01-02"), nil, false)
		reads := counter.Reads()
		counter.Stop()
		if w.Code != http.StatusOK {
			t.Fatalf("events: %d %s", w.Code, w.Body.String())
		}
		var body struct {
			Events []occurrenceJSON `json:"events"`
		}
		decodeJSON(t, w, &body)
		if len(body.Events) != i {
			t.Fatalf("got %d events, want %d", len(body.Events), i)
		}
		for _, row := range body.Events {
			if row.HostedBy != "Our Church" || row.GroupID != nil || row.GroupName != "" {
				t.Fatalf("church-wide host: %+v", row)
			}
		}
		if i == 3 {
			baseline = reads
		}
		if reads != baseline || *settings != 1 {
			t.Fatalf("%d dates: %d reads (baseline=%d), site_name=%d", i, reads, baseline, *settings)
		}
		t.Logf("%d dates: %d reads, site_name=%d", i, reads, *settings)
	}
}

func TestGroupEventsContinuationBeyond100Dates(t *testing.T) {
	app := testApp(t)
	leader := newTestUser(t, app, "leader", "pw", models.RoleMember)
	g := newTestGroup(t, app, leader, "Daily group", models.GroupActive)
	client := newAPIClient(t, app)
	start := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Minute)
	ev, first := seedEvent(t, app, g, leader.ID, start)
	visible := []uint{}
	for i := 0; i < 160; i++ {
		o := first
		if i > 0 {
			date := start.AddDate(0, 0, i)
			o = models.EventOccurrence{EventID: ev.ID, OriginalDate: calDate(date.Format("2006-01-02")), StartsAt: date, EndsAt: date.Add(time.Hour), Status: models.OccurrenceScheduled}
			if err := app.DB.Create(&o).Error; err != nil {
				t.Fatal(err)
			}
		}
		if i%7 == 0 || i == 159 {
			if err := app.DB.Model(&o).Update("visibility_override", models.VisibilityInviteOnly).Error; err != nil {
				t.Fatal(err)
			}
		} else {
			visible = append(visible, o.ID)
		}
	}
	path := "/api/v1/groups/" + itoa(g.ID) + "/events"
	window := "?from=" + start.Format("2006-01-02") + "&to=" + start.AddDate(0, 0, 159).Format("2006-01-02")
	read := func(query string) groupEventsJSON {
		t.Helper()
		w := client.do("GET", path+window+query, nil, false)
		if w.Code != http.StatusOK {
			t.Fatalf("events: %d %s", w.Code, w.Body.String())
		}
		var page groupEventsJSON
		decodeJSON(t, w, &page)
		return page
	}
	firstPage := read("")
	if len(firstPage.Events) != 25 || !firstPage.HasMore || !firstPage.Truncated || firstPage.NextCursor == "" {
		t.Fatalf("first page: %d dates, hasMore=%v truncated=%v cursor=%q", len(firstPage.Events), firstPage.HasMore, firstPage.Truncated, firstPage.NextCursor)
	}
	second := read("&limit=100&cursor=" + firstPage.NextCursor)
	if len(second.Events) != 100 || !second.HasMore || !second.Truncated || second.NextCursor == "" {
		t.Fatalf("second page: %d dates, hasMore=%v truncated=%v cursor=%q", len(second.Events), second.HasMore, second.Truncated, second.NextCursor)
	}
	last := read("&cursor=" + second.NextCursor)
	if len(last.Events) != len(visible)-125 || last.HasMore || last.Truncated || last.NextCursor != "" {
		t.Fatalf("last page: %d dates, hasMore=%v truncated=%v cursor=%q", len(last.Events), last.HasMore, last.Truncated, last.NextCursor)
	}
	all := append(append(firstPage.Events, second.Events...), last.Events...)
	for i, row := range all {
		if row.ID != visible[i] {
			t.Fatalf("date %d: got %d want %d", i, row.ID, visible[i])
		}
	}
	// The non-paged shared listing must not silently drop the same series tail.
	full := app.occurrencesForEvents([]uint{ev.ID}, start.Format("2006-01-02"), start.AddDate(0, 0, 159).Format("2006-01-02"), nil, nil)
	if len(full) != len(visible) {
		t.Fatalf("shared list: got %d dates, want %d", len(full), len(visible))
	}
	// Current visibility, not the old cursor, controls the remaining dates.
	if err := app.DB.Model(&models.EventOccurrence{}).Where("id IN ?", visible[125:]).Update("visibility_override", models.VisibilityInviteOnly).Error; err != nil {
		t.Fatal(err)
	}
	hidden := read("&cursor=" + second.NextCursor)
	if len(hidden.Events) != 0 || hidden.HasMore || hidden.Truncated || hidden.NextCursor != "" {
		t.Fatal("hidden tail leaked into continuation metadata")
	}
	one := read("&limit=1")
	if len(one.Events) != 1 || one.Events[0].ID != visible[0] || !one.HasMore {
		t.Fatal("limit=1 did not apply after visibility")
	}
	other := newTestGroup(t, app, leader, "Other group", models.GroupActive)
	w := client.do("GET", "/api/v1/groups/"+itoa(other.ID)+"/events"+window+"&cursor="+firstPage.NextCursor, nil, false)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("cross-group cursor: %d %s", w.Code, w.Body.String())
	}
	for _, query := range []string{"&limit=0", "&limit=-1", "&limit=101", "&limit=abc", "&cursor=invalid", "&cursor=e30"} {
		w := client.do("GET", path+window+query, nil, false)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"code":"group_upcoming_page"`) {
			t.Fatalf("%s: %d %s", query, w.Code, w.Body.String())
		}
	}
}

// A repeat rule is refused when it repeats more than once a day, or makes
// more dates than the cap, and the answer carries its own code.
func TestRepeatRuleLimitsAreRefused(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "Rules")
	start := atUTC(time.Now().AddDate(0, 0, 7), 19, 0)
	create := func(rule string) *httptest.ResponseRecorder {
		return admin.do("POST", "/api/v1/groups/"+gid+"/events", map[string]any{
			"title": "Repeats", "startsLocal": start.Format("2006-01-02T15:04"), "durationMinutes": 60,
			"tzid": "UTC", "recurrenceRule": rule,
		}, true)
	}
	for _, rule := range []string{"FREQ=SECONDLY", "FREQ=MINUTELY", "FREQ=HOURLY;INTERVAL=2"} {
		w := create(rule)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"code":"recurrence_too_frequent"`) {
			t.Fatalf("%s: %d %s", rule, w.Code, w.Body.String())
		}
	}
	// Daily with every hour of the day is still only "daily", so the cap
	// catches it.
	hours := make([]string, 24)
	for i := range hours {
		hours[i] = fmt.Sprint(i)
	}
	w := create("FREQ=DAILY;BYHOUR=" + strings.Join(hours, ","))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"code":"recurrence_too_many"`) {
		t.Fatalf("byhour: %d %s", w.Code, w.Body.String())
	}
	var n int64
	app.DB.Model(&models.Event{}).Count(&n)
	if n != 0 {
		t.Fatalf("a refused rule left %d events", n)
	}
	for _, rule := range []string{weeklyRule(start), "FREQ=DAILY", "FREQ=MONTHLY;BYMONTHDAY=1"} {
		if w := create(rule); w.Code != http.StatusCreated {
			t.Fatalf("%s: %d %s", rule, w.Code, w.Body.String())
		}
	}
	var created struct {
		Event eventJSON `json:"event"`
	}
	decodeJSON(t, create(weeklyRule(start)), &created)
	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d", created.Event.ID), map[string]any{"recurrenceRule": "FREQ=SECONDLY"}, http.StatusBadRequest)
}

// Creating, deleting and changing the hosts of an event leave audit rows.
func TestEventLifecycleIsAudited(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "Audited")
	newTestUser(t, app, "host2", "password1234", models.RoleMember)
	start := atUTC(time.Now().AddDate(0, 0, 7), 19, 0)
	count := func(action string, id uint) int64 {
		var n int64
		app.DB.Model(&models.AuditLog{}).Where("action = ? AND target_id = ?", action, id).Count(&n)
		return n
	}
	var grp struct {
		Event eventJSON `json:"event"`
	}
	decodeJSON(t, admin.do("POST", "/api/v1/groups/"+gid+"/events", map[string]any{
		"title": "Group night", "startsLocal": start.Format("2006-01-02T15:04"), "durationMinutes": 60, "tzid": "UTC",
	}, true), &grp)
	if count(audit.EventCreated, grp.Event.ID) != 1 {
		t.Fatal("group event creation not audited")
	}
	apiDo(t, admin, "DELETE", fmt.Sprintf("/api/v1/events/%d", grp.Event.ID), nil, http.StatusOK)
	if count(audit.EventDeleted, grp.Event.ID) != 1 {
		t.Fatal("event deletion not audited")
	}

	var church struct {
		Event eventJSON `json:"event"`
	}
	decodeJSON(t, admin.do("POST", "/api/v1/events", map[string]any{
		"title": "Church picnic", "startsLocal": start.Format("2006-01-02T15:04"), "durationMinutes": 60, "tzid": "UTC",
	}, true), &church)
	if church.Event.ID == 0 || count(audit.EventCreated, church.Event.ID) != 1 {
		t.Fatalf("church event creation not audited: %+v", church.Event)
	}
	hosts := fmt.Sprintf("/api/v1/events/%d/hosts", church.Event.ID)
	apiDo(t, admin, "POST", hosts, map[string]any{"email": "host2@example.com"}, http.StatusOK)
	apiDo(t, admin, "POST", hosts, map[string]any{"email": "host2@example.com"}, http.StatusOK)
	if got := count(audit.EventHostAdded, church.Event.ID); got != 1 {
		t.Fatalf("host added rows %d, want 1 (a repeat adds nothing)", got)
	}
	var host models.User
	app.DB.Where("email = ?", "host2@example.com").First(&host)
	apiDo(t, admin, "DELETE", fmt.Sprintf("%s/%d", hosts, host.ID), nil, http.StatusOK)
	if count(audit.EventHostRemoved, church.Event.ID) != 1 {
		t.Fatal("host removal not audited")
	}
}

// A bad event must not stop the extender from reaching later events.
func TestExtendContinuesPastABadEvent(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "Extender")
	start := atUTC(time.Now().AddDate(0, 0, 7), 19, 0)
	var first models.User
	app.DB.Order("id").First(&first)
	// Created first, so it sorts before the good event.
	bad := models.Event{Title: "Bad", StartsLocal: start.Format("2006-01-02T15:04"), DurationMinutes: 60,
		TZID: "UTC", RRule: "FREQ=SECONDLY", Visibility: models.VisibilityPublic, CreatedBy: first.ID}
	if err := app.DB.Create(&bad).Error; err != nil {
		t.Fatal(err)
	}
	var good struct {
		Event eventJSON `json:"event"`
	}
	decodeJSON(t, admin.do("POST", "/api/v1/groups/"+gid+"/events", map[string]any{
		"title": "Good", "startsLocal": start.Format("2006-01-02T15:04"), "durationMinutes": 60,
		"tzid": "UTC", "recurrenceRule": weeklyRule(start),
	}, true), &good)
	app.DB.Where("event_id = ?", good.Event.ID).Delete(&models.EventOccurrence{})
	err := eventdates.Extend(app.DB, time.Now().UTC(), nil)
	if err == nil || !errors.Is(err, events.ErrTooFrequent) {
		t.Fatalf("want the bad event's error, got %v", err)
	}
	var n int64
	app.DB.Model(&models.EventOccurrence{}).Where("event_id = ?", good.Event.ID).Count(&n)
	if n < 70 {
		t.Fatalf("later event was not extended: %d dates", n)
	}
}

// A date opened from a hidden series lists like the exact-date page: no
// description or childcare details from the series, and a registered-only
// video link stays hidden from someone who is not going.
func TestHiddenSeriesListHidesDescriptionChildcareAndVideo(t *testing.T) {
	app, admin, _, ev, dates := dateVisibilityFixture(t, models.VisibilityInviteOnly)
	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d", ev.ID), map[string]any{
		"description":        "SECRET-DESCRIPTION",
		"childcareAvailable": true, "childcareNotes": "SECRET-CHILDCARE-NOTE", "childcareAges": "SECRET-AGES",
		"childcareCapacity": 7,
		"videoProvider":     "link", "videoUrl": "https://example.com/SECRET-VIDEO", "videoReveal": "registered_only",
	}, http.StatusOK)
	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d/occurrences/%s", ev.ID, models.FormatCalendarDate(dates[0].OriginalDate)),
		map[string]any{"visibility": "public", "reason": "Open"}, http.StatusOK)

	listed, err := app.occurrencesForEventsErr([]uint{ev.ID}, models.FormatCalendarDate(dates[0].OriginalDate), models.FormatCalendarDate(dates[3].OriginalDate), nil, nil)
	if err != nil || len(listed) != 1 {
		t.Fatalf("list: %v %+v", err, listed)
	}
	raw, err := json.Marshal(listed)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"SECRET-DESCRIPTION", "SECRET-CHILDCARE-NOTE", "SECRET-AGES", "SECRET-VIDEO"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("hidden series leaked %s: %s", secret, raw)
		}
	}
	if listed[0].ChildcareCapacity != nil {
		t.Fatalf("childcare capacity leaked: %v", *listed[0].ChildcareCapacity)
	}
}

// The exact-date page shows no cancelled registration as the viewer's answer.
func TestDatePageIgnoresCancelledRegistration(t *testing.T) {
	app, admin, _, ev, dates := dateVisibilityFixture(t, models.VisibilityPublic)
	_ = admin
	member := newTestUser(t, app, "cancelled-member", "password1234", models.RoleMember)
	if err := app.DB.Create(&models.Registration{
		UserID: &member.ID, OccurrenceID: dates[0].ID, Status: models.RegCancelled,
	}).Error; err != nil {
		t.Fatal(err)
	}
	c := loginAPI(t, app, "cancelled-member", "password1234")
	raw := apiDo(t, c, "GET", fmt.Sprintf("/api/v1/events/%d/dates/%s", ev.ID, models.FormatCalendarDate(dates[0].OriginalDate)), nil, http.StatusOK)
	if strings.Contains(raw, `"myRegistration":"cancelled"`) {
		t.Fatalf("cancelled registration came back: %s", raw)
	}
}

// occurrencesForEvents is occurrencesForEventsErr for tests that only look
// at the rows.
func (a *App) occurrencesForEvents(eventIDs []uint, from, to string, startAfter *time.Time, me *models.User) []occurrenceJSON {
	out, _ := a.occurrencesForEventsErr(eventIDs, from, to, startAfter, me)
	return out
}

// A series edit that drops dates cancels them and says why. A later edit
// whose rule includes them again brings them back on and tells the people who
// were told they were cancelled; their registrations were never removed, so
// none is recreated. A date a person cancelled stays cancelled.
func TestSeriesEditRestoresSeriesCancelledDates(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "Restore")
	newTestUser(t, app, "sam", "password1234", models.RoleMember)
	newTestUser(t, app, "pat", "password1234", models.RoleMember)
	sam := loginAPI(t, app, "sam", "password1234")
	pat := loginAPI(t, app, "pat", "password1234")
	for _, c := range []*apiClient{sam, pat} {
		if w := c.do("POST", "/api/v1/groups/"+gid+"/join", map[string]any{}, true); w.Code != http.StatusOK {
			t.Fatalf("join: %d %s", w.Code, w.Body.String())
		}
	}
	start := atUTC(time.Now().AddDate(0, 0, 14), 19, 0)
	d1 := start.AddDate(0, 0, 7).Format("2006-01-02")
	d2 := start.AddDate(0, 0, 14).Format("2006-01-02")
	d3 := start.AddDate(0, 0, 21).Format("2006-01-02")
	d4 := start.AddDate(0, 0, 28).Format("2006-01-02")
	w := admin.do("POST", "/api/v1/groups/"+gid+"/events", map[string]any{
		"title": "Prayer", "startsLocal": start.Format("2006-01-02T15:04"),
		"durationMinutes": 60, "tzid": "UTC", "recurrenceRule": weeklyRule(start),
	}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Event eventJSON `json:"event"`
	}
	decodeJSON(t, w, &created)
	eid := itoa(created.Event.ID)
	reg := func(c *apiClient, date, status string) {
		t.Helper()
		if w := c.do("PUT", "/api/v1/events/"+eid+"/registrations", map[string]any{"date": date, "status": status}, true); w.Code != http.StatusOK {
			t.Fatalf("register %s: %d %s", date, w.Code, w.Body.String())
		}
	}
	reg(sam, d2, "going")
	reg(sam, d3, "going")
	reg(sam, d4, "going")
	reg(pat, d3, "maybe")

	occ := func(date string) models.EventOccurrence {
		t.Helper()
		var o models.EventOccurrence
		if err := app.DB.Where("event_id = ? AND original_date = ?", created.Event.ID, date).First(&o).Error; err != nil {
			t.Fatal(err)
		}
		return o
	}
	count := func(c *apiClient, title string) int {
		n := 0
		for _, item := range feed(t, c, "") {
			if item.Title == title {
				n++
			}
		}
		return n
	}
	registrations := func() int64 {
		var n int64
		app.DB.Model(&models.Registration{}).Joins("JOIN event_occurrences ON event_occurrences.id = registrations.occurrence_id").
			Where("event_occurrences.event_id = ? AND registrations.status <> ?", created.Event.ID, models.RegCancelled).Count(&n)
		return n
	}
	before := registrations()
	// The dates in notice data and audit rows are the dynamic sinks the
	// compiler cannot check: they must read exactly YYYY-MM-DD.
	noticeDates := func(kind string) []string {
		t.Helper()
		var rows []models.Notification
		if err := app.DB.Where("kind = ?", kind).Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, n := range rows {
			var data struct {
				Date string `json:"date"`
			}
			if err := json.Unmarshal([]byte(n.Data), &data); err != nil {
				t.Fatal(err)
			}
			out = append(out, data.Date)
		}
		sort.Strings(out)
		return out
	}
	storedUntil := func() string {
		t.Helper()
		var ev models.Event
		if err := app.DB.First(&ev, created.Event.ID).Error; err != nil {
			t.Fatal(err)
		}
		if ev.RRuleUntil == nil {
			return ""
		}
		return models.FormatCalendarDate(*ev.RRuleUntil)
	}
	if storedUntil() != "" {
		t.Fatalf("a series with no end has until %q", storedUntil())
	}

	// A person cancels d4 by hand. Sam is told once.
	if w := admin.do("POST", "/api/v1/events/"+eid+"/cancel", map[string]any{"date": d4}, true); w.Code != http.StatusOK {
		t.Fatalf("cancel d4: %d %s", w.Code, w.Body.String())
	}
	if got := occ(d4); got.Status != models.OccurrenceCancelled || got.CancelSource != models.CancelSourceManual {
		t.Fatalf("d4 after manual cancel: %+v", got)
	}

	// Ending the series on d1 drops d2 and d3; d4 stays a manual cancellation.
	if w := admin.do("PUT", "/api/v1/events/"+eid, map[string]any{"rruleUntil": d1}, true); w.Code != http.StatusOK {
		t.Fatalf("until: %d %s", w.Code, w.Body.String())
	}
	for _, d := range []string{d2, d3} {
		if got := occ(d); got.Status != models.OccurrenceCancelled || got.CancelSource != models.CancelSourceSeries {
			t.Fatalf("%s after until: %+v", d, got)
		}
	}
	if got := occ(d4); got.CancelSource != models.CancelSourceManual {
		t.Fatalf("d4 source changed: %q", got.CancelSource)
	}
	if got := count(sam, "Cancelled: Prayer"); got != 3 {
		t.Fatalf("sam cancelled notices = %d, want 3 (d2, d3, d4)", got)
	}
	if got := storedUntil(); got != d1 {
		t.Fatalf("stored until = %q, want %q", got, d1)
	}
	if got, want := strings.Join(noticeDates(notify.OccurrenceCancelled), ","), strings.Join([]string{d2, d3, d4}, ","); got != want {
		t.Fatalf("cancelled notice dates = %q, want %q", got, want)
	}
	if got := count(pat, "Cancelled: Prayer"); got != 0 {
		t.Fatalf("pat (maybe) cancelled notices = %d", got)
	}
	if registrations() != before {
		t.Fatal("cancelling changed registrations")
	}

	// Clearing the end date brings d2 and d3 back, not d4.
	if w := admin.do("PUT", "/api/v1/events/"+eid, map[string]any{"rruleUntil": ""}, true); w.Code != http.StatusOK {
		t.Fatalf("clear until: %d %s", w.Code, w.Body.String())
	}
	for _, d := range []string{d2, d3} {
		if got := occ(d); got.Status != models.OccurrenceScheduled || got.CancelSource != "" || got.StartsAt.UTC().Hour() != 19 {
			t.Fatalf("%s after clearing until: %+v", d, got)
		}
	}
	if got := occ(d4); got.Status != models.OccurrenceCancelled || got.CancelSource != models.CancelSourceManual {
		t.Fatalf("manually cancelled d4 came back: %+v", got)
	}
	if got := count(sam, "Back on: Prayer"); got != 2 {
		t.Fatalf("sam back-on notices = %d, want 2 (d2, d3)", got)
	}
	if got := count(pat, "Back on: Prayer"); got != 0 {
		t.Fatalf("pat (maybe) back-on notices = %d", got)
	}
	if registrations() != before {
		t.Fatalf("restoring changed registrations: %d then %d", before, registrations())
	}
	if storedUntil() != "" {
		t.Fatalf("clearing the end date left until %q", storedUntil())
	}
	if got, want := strings.Join(noticeDates(notify.OccurrenceRestored), ","), strings.Join([]string{d2, d3}, ","); got != want {
		t.Fatalf("restored notice dates = %q, want %q", got, want)
	}
	var audits int64
	app.DB.Model(&models.AuditLog{}).Where("action = ? AND target_type = 'event' AND target_id = ?", audit.EventDatesRestored, created.Event.ID).Count(&audits)
	if audits != 1 {
		t.Fatalf("restore audit rows = %d", audits)
	}
	var restoredAudit models.AuditLog
	if err := app.DB.Where("action = ? AND target_type = 'event' AND target_id = ?", audit.EventDatesRestored, created.Event.ID).First(&restoredAudit).Error; err != nil {
		t.Fatal(err)
	}
	var restoredMeta struct {
		Dates []string `json:"dates"`
	}
	if err := json.Unmarshal([]byte(restoredAudit.MetaJSON), &restoredMeta); err != nil {
		t.Fatal(err)
	}
	// restoreSeriesDates records every occurrence the edit brought back, not
	// only the ones someone registered for. The series has no end date again,
	// so that is each generated date after d1 except the manually cancelled d4,
	// in date order. The people told (noticeDates above) are only d2 and d3.
	var generated []models.EventOccurrence
	if err := app.DB.Where("event_id = ?", created.Event.ID).Order("original_date").Find(&generated).Error; err != nil {
		t.Fatal(err)
	}
	wantRestored := []string{}
	for _, o := range generated {
		if day := models.FormatCalendarDate(o.OriginalDate); day > d1 && o.Status == models.OccurrenceScheduled && o.CancelSource == "" {
			wantRestored = append(wantRestored, day)
		}
	}
	if len(wantRestored) <= 2 || wantRestored[0] != d2 || wantRestored[1] != d3 {
		t.Fatalf("restored series dates %v, want d2 %s and d3 %s first, then the rest of the series", wantRestored, d2, d3)
	}
	for _, day := range wantRestored {
		if day == d4 {
			t.Fatalf("manually cancelled %s counted as restored in %v", d4, wantRestored)
		}
	}
	for _, day := range restoredMeta.Dates {
		if parsed, err := time.Parse("2006-01-02", day); err != nil || parsed.Format("2006-01-02") != day {
			t.Fatalf("restore audit date %q is not YYYY-MM-DD", day)
		}
	}
	if strings.Join(restoredMeta.Dates, ",") != strings.Join(wantRestored, ",") {
		t.Fatalf("restore audit dates = %v, want %v", restoredMeta.Dates, wantRestored)
	}

	// Cancelling again after a restore tells people again.
	if w := admin.do("PUT", "/api/v1/events/"+eid, map[string]any{"rruleUntil": d1}, true); w.Code != http.StatusOK {
		t.Fatalf("until again: %d %s", w.Code, w.Body.String())
	}
	if got := count(sam, "Cancelled: Prayer"); got != 5 {
		t.Fatalf("sam cancelled notices after second cut = %d, want 5", got)
	}
}

// events.original_date and events.rrule_until are date columns, so a date
// that cannot exist would fail inside Postgres. Every place that takes one
// answers as it did when the columns were text: a missing date, or a 400 where
// the field is checked first.
func TestImpossibleDatesKeepTheirAnswers(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "Impossible dates")
	eventID, _ := seedCappedEvent(t, app, admin, gid, intPtr(5), false)
	snapshot := func() string {
		t.Helper()
		var state struct {
			Events []models.Event
			Dates  []models.EventOccurrence
		}
		if err := app.DB.Order("id").Find(&state.Events).Error; err != nil {
			t.Fatal(err)
		}
		if err := app.DB.Order("id").Find(&state.Dates).Error; err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	before := snapshot()
	apiDo(t, admin, "POST", "/api/v1/groups/"+gid+"/events", map[string]any{
		"title": "Never", "startsLocal": "0000-01-01T09:00", "durationMinutes": 60, "tzid": "UTC",
	}, http.StatusBadRequest)
	if snapshot() != before {
		t.Fatal("rejected year-zero creation changed events or occurrences")
	}
	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d", eventID), map[string]any{
		"title": "Never updated", "startsLocal": "0000-01-01T09:00", "recurrenceRule": "", "rruleUntil": "",
	}, http.StatusBadRequest)
	if snapshot() != before {
		t.Fatal("rejected year-zero update changed events or occurrences")
	}
	absent, _ := registerAs(admin, eventID, "2099-12-31", "going", nil)
	for _, bad := range []string{"2026-02-30", "2027-02-29", "2026-13-01", "2026-04-31", "0000-01-01"} {
		if code, body := registerAs(admin, eventID, bad, "going", nil); code != absent || code >= http.StatusInternalServerError {
			t.Fatalf("register on %s: %d %s, want %d like any other date the event lacks", bad, code, body, absent)
		}
		apiDo(t, admin, "GET", fmt.Sprintf("/api/v1/events/%d/dates/%s", eventID, bad), nil, http.StatusNotFound)
		apiDo(t, admin, "GET", fmt.Sprintf("/api/v1/events/%d/dates/%s/calendar.ics", eventID, bad), nil, http.StatusNotFound)
		apiDo(t, admin, "POST", fmt.Sprintf("/api/v1/events/%d/cancel", eventID), map[string]any{"date": bad}, http.StatusBadRequest)
		apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d/occurrences/%s", eventID, bad), map[string]any{"location": "Hall"}, http.StatusBadRequest)
		// parseDateWindow checks from and to on the group list and the calendar.
		// The church-wide /api/v1/events list takes no window (it uses
		// upcomingWindow), so it ignores the parameters and answers 200 as it did
		// before the columns became dates.
		apiDo(t, admin, "GET", "/api/v1/groups/"+gid+"/events?from="+bad+"&to=2030-01-01", nil, http.StatusBadRequest)
		apiDo(t, admin, "GET", "/api/v1/groups/"+gid+"/events?from=2030-01-01&to="+bad, nil, http.StatusBadRequest)
		apiDo(t, admin, "GET", "/api/v1/calendar?from="+bad+"&to=2030-01-01", nil, http.StatusBadRequest)
		apiDo(t, admin, "GET", "/api/v1/events?from="+bad+"&to=2030-01-01", nil, http.StatusOK)
		apiDo(t, admin, "POST", "/api/v1/groups/"+gid+"/events", map[string]any{
			"title": "Never", "startsLocal": "2030-01-07T19:00", "durationMinutes": 60, "tzid": "UTC",
			"recurrenceRule": "FREQ=WEEKLY", "rruleUntil": bad,
		}, http.StatusBadRequest)
	}
}

// A leap day is a real end date, kept as written; a one-digit hour that
// time.Parse accepts is stored in the full YYYY-MM-DDTHH:MM the column's
// check wants.
func TestEventStoresLeapUntilAndPaddedHour(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "Leap")
	start := atUTC(time.Now().AddDate(0, 0, 14), 9, 0)
	w := admin.do("POST", "/api/v1/groups/"+gid+"/events", map[string]any{
		"title": "Leap day", "startsLocal": start.Format("2006-01-02") + "T9:00",
		"durationMinutes": 60, "tzid": "UTC", "recurrenceRule": weeklyRule(start), "rruleUntil": "2032-02-29",
	}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Event eventJSON `json:"event"`
	}
	decodeJSON(t, w, &created)
	if created.Event.RRuleUntil != "2032-02-29" || created.Event.StartsLocal != start.Format("2006-01-02")+"T09:00" {
		t.Fatalf("created event: until %q, startsLocal %q", created.Event.RRuleUntil, created.Event.StartsLocal)
	}
	var stored models.Event
	if err := app.DB.First(&stored, created.Event.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.RRuleUntil == nil || models.FormatCalendarDate(*stored.RRuleUntil) != "2032-02-29" || stored.StartsLocal != start.Format("2006-01-02")+"T09:00" {
		t.Fatalf("stored event: %+v", stored)
	}
	// Clearing the end date stores NULL, not an empty or zero date.
	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d", created.Event.ID), map[string]any{"rruleUntil": ""}, http.StatusOK)
	var cleared models.Event
	if err := app.DB.First(&cleared, created.Event.ID).Error; err != nil {
		t.Fatal(err)
	}
	if cleared.RRuleUntil != nil {
		t.Fatalf("a cleared end date is stored as %v, want NULL", cleared.RRuleUntil)
	}
}

// N-18 (inventory 9.2.2, a plain entry): a leader creating an event wider than
// its group tells the other leaders and the admins, from a transaction that
// takes no lock before createEvent. An admin erased meanwhile is left out in
// either order, and the other admin is still told.
func TestVisibilityOverrideAtCreationSerializesWithAdminErasure(t *testing.T) {
	runErasureBarriers(t, 7402007, func(t *testing.T) erasureScene {
		app := testApp(t)
		kept := newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
		victim := newTestUser(t, app, "admin2", "password1234", models.RoleAdmin)
		lead := newTestUser(t, app, "lead", "password1234", models.RoleMember)
		g := newTestGroup(t, app, lead, "Youth", models.GroupActive)
		if err := app.DB.Model(g).Update("visibility", models.VisibilityPrivate).Error; err != nil {
			t.Fatal(err)
		}
		leader := loginAPI(t, app, "lead", "password1234")
		leader.holdNotices = true
		start := feedStart(5)
		return erasureScene{app: app, victim: victim,
			run: func() error {
				return doExpect(leader, "POST", "/api/v1/groups/"+itoa(g.ID)+"/events", map[string]any{
					"title": "Lock-in", "startsLocal": start.Format("2006-01-02T15:04"), "durationMinutes": 60, "tzid": "UTC",
					"visibility": models.VisibilityPublic, "visibilityReason": "invite the neighbors",
				}, http.StatusCreated)
			},
			told: func(t *testing.T) {
				if n := contactCount(t, app, &models.Notification{}, "user_id = ? AND kind = ?", kept.ID, notify.VisibilityOverride); n != 1 {
					t.Fatalf("the other admin has %d visibility notices, want 1", n)
				}
			},
		}
	})
}

// registrantRace is the fixture for the gated event routes: a public group with
// a single-date event thirty days out and three people signed up through the
// real registration route: a victim (the person the erasure removes), a kept
// registrant who is not a group member, and a group member. Two admin clients
// (the one who edits and the one who deletes) leave notice jobs queued so they
// may run on an operation's goroutine.
type registrantRace struct {
	app                  *App
	admin, eraser        *apiClient
	eventID              uint
	date                 string
	victim, kept, member *models.User
}

func newRegistrantRace(t *testing.T) registrantRace {
	t.Helper()
	app := testApp(t)
	admin, gid := openGroup(t, app, "Registrant race")
	newTestUser(t, app, "eraser", "password1234", models.RoleAdmin)
	victim := newTestUser(t, app, "victim", "password1234", models.RoleMember)
	kept := newTestUser(t, app, "kept", "password1234", models.RoleMember)
	member := newTestUser(t, app, "member", "password1234", models.RoleMember)
	var g models.Group
	if err := app.DB.Where("id = ?", gid).First(&g).Error; err != nil {
		t.Fatal(err)
	}
	addMember(t, app, member, &g, models.MembershipMember)
	eventID, date := seedCappedEvent(t, app, admin, gid, nil, false)
	for _, u := range []*models.User{victim, kept, member} {
		if code, body := registerAs(loginAPI(t, app, u.Name, "password1234"), eventID, date, "going", nil); code != http.StatusOK {
			t.Fatalf("%s going: %d %s", u.Name, code, body)
		}
	}
	admin.holdNotices = true
	return registrantRace{app: app, admin: admin, eraser: loginHeld(t, app, "eraser", "password1234"),
		eventID: eventID, date: date, victim: victim, kept: kept, member: member}
}

// N-28 (inventory 9.2.2, a gated entry): a change to where or when a date is
// held tells everyone going or waitlisted on it. The real operation is
// PUT /events/:id/occurrences/:date {location} -> apiUpdateOccurrence ->
// contactMutation (gate first, with the event id, so occurrence access is
// reconciled first) -> the event row and occurrence row FOR UPDATE -> the date
// is saved -> notifyEventChanged: the registrants of that date are read (listed
// users only), then notify.WithRecipients locks them all once and each Send
// reuses the scope. The edit answers 200 in both orders.
//
// Sender first: the edit stops after it wrote the victim's notice; the real
// delete waits on the gate and removes it. Deletion first: the edit waits on the
// gate, then reads its registrants after the erasure, so the victim is not in
// the list. The kept registrant and the member are told in both. Not covered:
// the series edit (apiUpdateEvent, which reaches notifyEventChanged for the
// time and place variants at api_events.go:1063 and :1095), the "time" and
// "time and place" variants, and invited holders (sendOccurrenceNotice, which
// has its own plain-entry test under N-32).
func TestEventChangedNoticeSerializesWithRealAdminDelete(t *testing.T) {
	runGatedErasureBarriers(t, 7402121, 7402122, func(t *testing.T) gatedErasureScene {
		f := newRegistrantRace(t)
		return gatedErasureScene{app: f.app, victim: f.victim, sends: []string{notify.EventChanged},
			run: func() error {
				return doExpect(f.admin, "PUT", "/api/v1/events/"+itoa(f.eventID)+"/occurrences/"+f.date, map[string]any{"location": "The new hall"}, http.StatusOK)
			},
			erase: eraseVia(f.eraser, f.victim),
			told: func(t *testing.T) {
				var o models.EventOccurrence
				if err := f.app.DB.Where("event_id = ?", f.eventID).First(&o).Error; err != nil || o.LocationOverride == nil || *o.LocationOverride != "The new hall" {
					t.Fatalf("the date was not moved to the new place: %+v %v", o, err)
				}
				wantNotices(t, f.app, notify.EventChanged, map[*models.User]int64{f.kept: 1, f.member: 1})
			},
		}
	})
}

// N-16 (inventory 9.2.2, a gated entry): cancelling a date tells everyone going
// or waitlisted on it. The real operation is POST /events/:id/cancel {date} ->
// apiCancelOccurrence -> contactMutation (gate first, with the event id) -> one
// UPDATE marks the date cancelled (its row lock; no earlier row lock) -> notifyOccurrences: the
// registrants are read (listed users only), one WithRecipients lock covers all
// of them, and each Send reuses the scope; the room is freed after. 200 in
// both orders.
//
// Sender first: the cancellation stops after it wrote the victim's notice; the
// delete waits on the gate. Deletion first: the cancellation waits, then does
// not see the victim among the registrants. The kept registrant and the member
// are told in both. Not covered: the series-edit cancel in apiUpdateEvent
// (api_events.go:1016), the restore variant (notifyOccurrences with OccurrenceRestored, same
// fan-out with another kind), and a date with invited holders.
func TestOccurrenceCancelledNoticeSerializesWithRealAdminDelete(t *testing.T) {
	runGatedErasureBarriers(t, 7402123, 7402124, func(t *testing.T) gatedErasureScene {
		f := newRegistrantRace(t)
		return gatedErasureScene{app: f.app, victim: f.victim, sends: []string{notify.OccurrenceCancelled},
			run: func() error {
				return doExpect(f.admin, "POST", "/api/v1/events/"+itoa(f.eventID)+"/cancel", map[string]any{"date": f.date}, http.StatusOK)
			},
			erase: eraseVia(f.eraser, f.victim),
			told: func(t *testing.T) {
				var o models.EventOccurrence
				if err := f.app.DB.Where("event_id = ?", f.eventID).First(&o).Error; err != nil || o.Status != models.OccurrenceCancelled {
					t.Fatalf("the date was not cancelled: %+v %v", o, err)
				}
				wantNotices(t, f.app, notify.OccurrenceCancelled, map[*models.User]int64{f.kept: 1, f.member: 1})
			},
		}
	})
}

// A viewer who can see only one date of a hidden series must get none of the
// series: the seven "omit" keys are absent from the JSON, the blanked fields are
// empty. The tag names are pinned here so a typo in a `series:` tag cannot
// silently stop hiding a field.
func TestHiddenSeriesDateHidesEverySeriesField(t *testing.T) {
	omit := []string{"recurrenceRule", "rruleUntil", "seriesStartsLocal", "seriesTitle", "seriesLocation", "seriesDurationMinutes", "seriesVisibility"}
	blank := []string{"description", "childcareNotes", "childcareCapacity", "childcareAges"}

	// The tags name exactly these fields.
	gotOmit, gotBlank := []string{}, []string{}
	typ := reflect.TypeOf(occurrenceJSON{})
	for i := 0; i < typ.NumField(); i++ {
		name := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		switch typ.Field(i).Tag.Get("series") {
		case "omit":
			gotOmit = append(gotOmit, name)
		case "blank":
			gotBlank = append(gotBlank, name)
		case "":
		default:
			t.Fatalf("field %s has an unknown series tag %q", typ.Field(i).Name, typ.Field(i).Tag.Get("series"))
		}
	}
	sort.Strings(omit)
	sort.Strings(gotOmit)
	sort.Strings(blank)
	sort.Strings(gotBlank)
	if !reflect.DeepEqual(omit, gotOmit) || !reflect.DeepEqual(blank, gotBlank) {
		t.Fatalf("series tags: omit %v blank %v, want omit %v blank %v", gotOmit, gotBlank, omit, blank)
	}

	cap := 3
	inst := occurrenceJSON{
		ID: 1, EventID: 2, Date: "2030-01-01", Title: "The date", Location: "Hall",
		Description: "series description", RecurrenceRule: "FREQ=WEEKLY", RRuleUntil: "2031-01-01",
		SeriesStartsLocal: "2030-01-01T19:00", SeriesTitle: "The series", SeriesLocation: "Series hall",
		SeriesDurationMinutes: 60, SeriesVisibility: "invite_only",
		ChildcareNotes: "notes", ChildcareCapacity: &cap, ChildcareAges: "2-5",
	}
	inst.hideSeriesDetails("/events/2/dates/2030-01-01")
	body, err := json.Marshal(inst)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range omit {
		if _, present := fields[key]; present {
			t.Errorf("hidden-series date still carries %q: %s", key, body)
		}
	}
	for _, key := range blank {
		switch v := fields[key].(type) {
		case nil:
		case string:
			if v != "" {
				t.Errorf("hidden-series date carries %q = %q", key, v)
			}
		default:
			t.Errorf("hidden-series date carries %q = %v", key, v)
		}
	}
	if fields["dateUrl"] != "/events/2/dates/2030-01-01" || fields["title"] != "The date" {
		t.Errorf("the date's own fields must stay: %s", body)
	}
	// A viewer who can see the series still gets every key.
	full, _ := json.Marshal(occurrenceJSON{SeriesTitle: "The series"})
	for _, key := range omit {
		if !strings.Contains(string(full), `"`+key+`"`) && key != "rruleUntil" && key != "seriesVisibility" {
			t.Errorf("authorized JSON lost %q: %s", key, full)
		}
	}
}

// DEC-147: leaders and admins alike create no event for a group that isn't
// active, and a refusal writes nothing. Church-wide events have no group.
func TestCreateEventNeedsActiveGroup(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	leader := newTestUser(t, app, "leader", "password1234", models.RoleMember)
	admin := loginAPI(t, app, "admin", "password1234")
	lc := loginAPI(t, app, "leader", "password1234")
	start := atUTC(time.Now().AddDate(0, 0, 7), 19, 0)
	body := map[string]any{"title": "Night", "startsLocal": start.Format("2006-01-02T15:04"), "durationMinutes": 60, "tzid": "UTC"}

	for _, status := range []string{models.GroupPending, models.GroupArchived} {
		g := newTestGroup(t, app, leader, "Not active "+status, status)
		path := "/api/v1/groups/" + itoa(g.ID) + "/events"
		auditBefore := contactCount(t, app, &models.AuditLog{}, "1 = 1")
		datesBefore := contactCount(t, app, &models.EventOccurrence{}, "1 = 1")
		for name, c := range map[string]*apiClient{"leader": lc, "admin": admin} {
			w := c.do("POST", path, body, true)
			if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"group_events_not_approved"`) {
				t.Fatalf("%s creating in a %s group: %d %s", name, status, w.Code, w.Body.String())
			}
		}
		withRoom := map[string]any{"title": "Room night", "startsLocal": start.Format("2006-01-02T15:04"), "durationMinutes": 60, "tzid": "UTC", "roomId": 999999}
		beforeBookings := contactCount(t, app, &models.RoomBooking{}, "1 = 1")
		if w := admin.do("POST", path, withRoom, true); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"group_events_not_approved"`) {
			t.Fatalf("creating with a room in a %s group: %d %s", status, w.Code, w.Body.String())
		}
		if n := contactCount(t, app, &models.RoomBooking{}, "1 = 1"); n != beforeBookings {
			t.Fatalf("refused event added room bookings: %d -> %d", beforeBookings, n)
		}
		if n := contactCount(t, app, &models.Event{}, "group_id = ?", g.ID); n != 0 {
			t.Fatalf("%d events written for a %s group", n, status)
		}
		if n := contactCount(t, app, &models.AuditLog{}, "action = ? AND meta_json->>'groupId' = ?", audit.EventCreated, itoa(g.ID)); n != 0 {
			t.Fatalf("%d event.created audit rows for a %s group", n, status)
		}
		// Every refusal above together: no audit row of any kind (the
		// visibility override's included) and no date.
		if n := contactCount(t, app, &models.AuditLog{}, "1 = 1"); n != auditBefore {
			t.Fatalf("refused creates wrote audit rows for a %s group: %d -> %d", status, auditBefore, n)
		}
		if n := contactCount(t, app, &models.EventOccurrence{}, "1 = 1"); n != datesBefore {
			t.Fatalf("refused creates wrote dates for a %s group: %d -> %d", status, datesBefore, n)
		}
		if status == models.GroupPending {
			if w := admin.do("POST", "/api/v1/admin/groups/"+itoa(g.ID)+"/approve", map[string]any{}, true); w.Code != http.StatusOK {
				t.Fatalf("approve group: %d %s", w.Code, w.Body.String())
			}
			if w := lc.do("POST", path, body, true); w.Code != http.StatusCreated {
				t.Fatalf("creating once the group is approved: %d %s", w.Code, w.Body.String())
			}
		}
	}

	w := admin.do("POST", "/api/v1/events", body, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("church-wide event: %d %s", w.Code, w.Body.String())
	}
}

// The group card and the group page decide a date by the same rule: for every
// kind of viewer and every mix of group visibility, event visibility and date
// override, nextEventAt is the first date of the same viewer's upcoming list.
func TestGroupNextEventAtMatchesUpcomingEvents(t *testing.T) {
	app := testApp(t)
	leader := newTestUser(t, app, "leader", "password1234", models.RoleMember)
	member := newTestUser(t, app, "member", "password1234", models.RoleMember)
	outsider := newTestUser(t, app, "outsider", "password1234", models.RoleMember)
	admin := newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	guest := newTestUser(t, app, "guest", "password1234", models.RoleMember)
	if err := app.DB.Model(guest).Update("status", models.UserGuest).Error; err != nil {
		t.Fatal(err)
	}
	guest.Status = models.UserGuest

	vis := []string{models.VisibilityPublic, models.VisibilityPrivate, models.VisibilityInviteOnly}
	overrides := []*string{nil}
	for i := range vis {
		overrides = append(overrides, &vis[i])
	}
	first := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Minute)
	for _, status := range []string{models.GroupActive, models.GroupPending, models.GroupArchived} {
		for _, gv := range vis {
			for _, ev := range vis {
				for _, ov := range overrides {
					g := newTestGroup(t, app, leader, fmt.Sprintf("G %s %s %s", status, gv, ev), status)
					if err := app.DB.Model(g).Update("visibility", gv).Error; err != nil {
						t.Fatal(err)
					}
					addMember(t, app, member, g, models.MembershipMember)
					e, _ := nextEventSeed(t, app, g, leader.ID, first, ev, ov)
					// A plain second date, so a hidden first date falls back to it.
					second := first.Add(48 * time.Hour)
					if err := app.DB.Create(&models.EventOccurrence{EventID: e.ID, OriginalDate: calDate(second.Format("2006-01-02")),
						StartsAt: second, EndsAt: second.Add(time.Hour), Status: models.OccurrenceScheduled}).Error; err != nil {
						t.Fatal(err)
					}
				}
			}
		}
	}
	var groups []models.Group
	if err := app.DB.Find(&groups).Error; err != nil {
		t.Fatal(err)
	}
	if len(groups) != 3*len(vis)*len(vis)*len(overrides) {
		t.Fatalf("seeded %d groups", len(groups))
	}

	viewers := []struct {
		kind string
		u    *models.User
	}{{"anonymous", nil}, {"guest", guest}, {"signed-in", outsider}, {"member", member}, {"leader", leader}, {"admin", admin}}
	now := time.Now().UTC()
	from, to := upcomingWindow(now)
	sawNext, sawNone := 0, 0
	for _, v := range viewers {
		got, err := app.groupSummaries(v.u, groups)
		if err != nil {
			t.Fatal(err)
		}
		for i, g := range groups {
			var eventIDs []uint
			if err := app.DB.Model(&models.Event{}).Where("group_id = ?", g.ID).Pluck("id", &eventIDs).Error; err != nil {
				t.Fatal(err)
			}
			upcoming, _, err := app.occurrencesForEventsAfterErr(eventIDs, from, to, &now, v.u, maxGroupUpcomingOccurrences, nil)
			if err != nil {
				t.Fatal(err)
			}
			var want *time.Time
			if len(upcoming) > 0 {
				want = &upcoming[0].StartsAt
				sawNext++
			} else {
				sawNone++
			}
			requireNextAt(t, fmt.Sprintf("%s, %s", v.kind, g.Name), got[i].NextEventAt, want)
		}
	}
	if sawNext == 0 || sawNone == 0 {
		t.Fatalf("the property check needs both visible and hidden cases: visible=%d hidden=%d", sawNext, sawNone)
	}
}

// Seeing a hidden date costs no query per group: the statements groupSummaries
// runs are the same for one group and for many, each with several events and
// per-date overrides.
func TestGroupSummariesDateReadsDoNotGrowWithGroups(t *testing.T) {
	app := testApp(t)
	leader := newTestUser(t, app, "leader", "password1234", models.RoleMember)
	viewer := newTestUser(t, app, "viewer", "password1234", models.RoleMember)
	start := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Minute)
	seedGroup := func(name string) {
		t.Helper()
		g := newTestGroup(t, app, leader, name, models.GroupActive)
		for i, vis := range []string{models.VisibilityPublic, models.VisibilityPrivate, models.VisibilityInviteOnly} {
			override := models.VisibilityPrivate
			nextEventSeed(t, app, g, leader.ID, start.Add(time.Duration(i)*time.Hour), vis, nil)
			nextEventSeed(t, app, g, leader.ID, start.Add(time.Duration(i+3)*time.Hour), vis, &override)
		}
	}
	n := collectionReadCounter(t, app.DB)
	reads := func() int {
		t.Helper()
		var groups []models.Group
		if err := app.DB.Order("id").Find(&groups).Error; err != nil {
			t.Fatal(err)
		}
		*n = 0
		if _, err := app.groupSummaries(viewer, groups); err != nil {
			t.Fatal(err)
		}
		return *n
	}
	seedGroup("Group 1")
	one := reads()
	for i := 2; i <= 6; i++ {
		seedGroup(fmt.Sprintf("Group %d", i))
	}
	if six := reads(); six != one {
		t.Fatalf("groupSummaries ran %d statements for 6 groups and %d for 1", six, one)
	}
}

// A writer commits after the snapshot's first read or its last policy-fact
// read. The result must belong to the state before that commit, never combine
// stale permission with a start time that only exists after permission is lost.
func TestNextVisibleDatesUsesOneSnapshot(t *testing.T) {
	for _, boundary := range []string{"events", "memberships"} {
		for _, change := range []string{"event visibility", "group status", "membership", "deleted event", "deleted group"} {
			t.Run(boundary+"/"+change, func(t *testing.T) {
				app := testApp(t)
				leader := newTestUser(t, app, "leader", "password1234", models.RoleMember)
				viewer := newTestUser(t, app, "viewer", "password1234", models.RoleMember)
				g := newTestGroup(t, app, leader, "Choir", models.GroupActive)
				visibility := models.VisibilityPublic
				if change == "membership" {
					addMember(t, app, viewer, g, models.MembershipMember)
					visibility = models.VisibilityInviteOnly
				}
				before := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Minute)
				after := before.Add(time.Hour)
				ev, occ := nextEventSeed(t, app, g, leader.ID, before, visibility, nil)
				fired := false
				callback := "test:next_dates_snapshot"
				if err := app.DB.Callback().Query().After("gorm:query").Register(callback, func(read *gorm.DB) {
					if fired || read.Error != nil || read.Statement.Table != boundary {
						return
					}
					fired = true
					var mode struct {
						Isolation string
						ReadOnly  string
					}
					if err := read.Session(&gorm.Session{NewDB: true}).Raw(`SELECT
						current_setting('transaction_isolation') AS isolation,
						current_setting('transaction_read_only') AS read_only`).Scan(&mode).Error; err != nil {
						read.AddError(err)
						return
					}
					if mode.Isolation != "repeatable read" || mode.ReadOnly != "on" {
						read.AddError(fmt.Errorf("snapshot mode = %+v", mode))
						return
					}
					read.AddError(app.DB.Transaction(func(write *gorm.DB) error {
						switch change {
						case "event visibility":
							if err := write.Model(&ev).Update("visibility", models.VisibilityInviteOnly).Error; err != nil {
								return err
							}
						case "group status":
							if err := write.Model(g).Update("status", models.GroupArchived).Error; err != nil {
								return err
							}
						case "membership":
							if err := write.Model(&models.Membership{}).Where("group_id = ? AND user_id = ?", g.ID, viewer.ID).
								Update("status", models.MembershipRemoved).Error; err != nil {
								return err
							}
						case "deleted event":
							if err := write.Delete(&ev).Error; err != nil {
								return err
							}
						case "deleted group":
							if err := write.Delete(g).Error; err != nil {
								return err
							}
						}
						return write.Model(&occ).Updates(map[string]any{"starts_at": after, "ends_at": after.Add(time.Hour)}).Error
					}))
				}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = app.DB.Callback().Query().Remove(callback) })

				got, err := nextVisibleDates(app.DB, viewer, []uint{g.ID})
				if err != nil {
					t.Fatal(err)
				}
				if !fired {
					t.Fatal("writer did not commit between snapshot reads")
				}
				requireNextAt(t, "in-flight snapshot", got[g.ID], &before)
				fresh, err := nextVisibleDates(app.DB, viewer, []uint{g.ID})
				if err != nil {
					t.Fatal(err)
				}
				requireNextAt(t, "fresh snapshot after tightening", fresh[g.ID], nil)
			})
		}
	}
}

func TestNextVisibleDatesPreservesCallerTransaction(t *testing.T) {
	for _, isolation := range []sql.IsolationLevel{sql.LevelRepeatableRead, sql.LevelSerializable, sql.LevelReadCommitted} {
		t.Run(isolation.String(), func(t *testing.T) {
			app := testApp(t)
			leader := newTestUser(t, app, "leader", "password1234", models.RoleMember)
			viewer := newTestUser(t, app, "viewer", "password1234", models.RoleMember)
			g := newTestGroup(t, app, leader, "Choir", models.GroupActive)
			originalDescription := g.Description
			start := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Minute)
			ev, _ := nextEventSeed(t, app, g, leader.ID, start, models.VisibilityInviteOnly, nil)
			tx := app.DB.Begin(&sql.TxOptions{Isolation: isolation})
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			defer tx.Rollback()
			// Only this transaction can see the opened event and the viewer's
			// new membership. Escaping to the pool for either read is wrong.
			if err := tx.Model(&ev).Update("visibility", models.VisibilityPublic).Error; err != nil {
				t.Fatal(err)
			}
			got, err := nextVisibleDates(tx, nil, []uint{g.ID})
			if isolation == sql.LevelReadCommitted {
				if !errors.Is(err, errNextDatesSnapshot) || got != nil {
					t.Fatalf("read-committed caller: result=%v err=%v", got, err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				requireNextAt(t, "caller's uncommitted event", got[g.ID], &start)
				if err := tx.Model(&ev).Update("visibility", models.VisibilityInviteOnly).Error; err != nil {
					t.Fatal(err)
				}
				if err := tx.Create(&models.Membership{GroupID: g.ID, UserID: viewer.ID,
					Status: models.MembershipActive, Role: models.MembershipMember}).Error; err != nil {
					t.Fatal(err)
				}
				got, err = nextVisibleDates(tx, viewer, []uint{g.ID})
				if err != nil {
					t.Fatal(err)
				}
				requireNextAt(t, "caller's uncommitted membership", got[g.ID], &start)
			}
			// The helper must neither commit the caller nor turn it read-only,
			// even when it refuses the caller's isolation level.
			if err := tx.Model(g).Update("description", "Still writable").Error; err != nil {
				t.Fatal(err)
			}
			if err := tx.Rollback().Error; err != nil {
				t.Fatal(err)
			}
			var stored models.Event
			if err := app.DB.First(&stored, ev.ID).Error; err != nil {
				t.Fatal(err)
			}
			if stored.Visibility != models.VisibilityInviteOnly {
				t.Fatal("helper committed its caller's event edit")
			}
			var storedGroup models.Group
			if err := app.DB.First(&storedGroup, g.ID).Error; err != nil {
				t.Fatal(err)
			}
			if storedGroup.Description != originalDescription {
				t.Fatal("caller transaction was not rolled back")
			}
		})
	}
}

func createEventBodyForTest() map[string]any {
	return map[string]any{"title": "Held event", "startsLocal": time.Now().UTC().AddDate(0, 0, 7).Format("2006-01-02") + "T19:00", "durationMinutes": 60, "tzid": "UTC"}
}

func createEventWriteCounts(t *testing.T, app *App) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	for name, model := range map[string]any{
		"events": &models.Event{}, "dates": &models.EventOccurrence{}, "audit": &models.AuditLog{},
		"notices": &models.Notification{}, "bookings": &models.RoomBooking{}, "jobs": &models.Job{},
	} {
		out[name] = contactCount(t, app, model, "1 = 1")
	}
	return out
}

func TestCreateEventRechecksAfterHeldBody(t *testing.T) {
	for _, change := range []string{"removed leader", "demoted admin", "disabled account", "archived group", "deleted group", "hidden group"} {
		t.Run(change, func(t *testing.T) {
			app := testApp(t)
			leader := newTestUser(t, app, "leader", "password1234", models.RoleMember)
			other := newTestUser(t, app, "other", "password1234", models.RoleAdmin)
			g := newTestGroup(t, app, leader, "Choir", models.GroupActive)
			addMember(t, app, other, g, models.MembershipLeader)
			actor := leader
			if change == "demoted admin" {
				actor = newTestUser(t, app, "non-leading admin", "password1234", models.RoleAdmin)
			}
			client := loginHeld(t, app, actor.Email, "password1234")
			admin := loginHeld(t, app, other.Email, "password1234")
			body := createEventBodyForTest()
			// A revoked writer must fail before any attempt to book this room.
			body["roomId"] = 999999
			held := client.doHeld("POST", "/api/v1/groups/"+itoa(g.ID)+"/events", body)
			want, code := http.StatusForbidden, ""
			switch change {
			case "removed leader", "hidden group":
				if err := app.DB.Model(&models.Membership{}).Where("user_id = ? AND group_id = ?", actor.ID, g.ID).
					Update("status", models.MembershipRemoved).Error; err != nil {
					t.Fatal(err)
				}
				if change == "hidden group" {
					apiDo(t, admin, "PUT", "/api/v1/groups/"+itoa(g.ID), map[string]any{"visibility": models.VisibilityInviteOnly, "confirm": true}, http.StatusOK)
					want = http.StatusNotFound
				}
			case "demoted admin":
				apiDo(t, admin, "PUT", "/api/v1/admin/users/"+itoa(actor.ID), map[string]any{"role": models.RoleMember}, http.StatusOK)
				want = http.StatusUnauthorized
			case "disabled account":
				apiDo(t, admin, "PUT", "/api/v1/admin/users/"+itoa(actor.ID), map[string]any{"status": models.UserDisabled}, http.StatusOK)
				want = http.StatusUnauthorized
			case "archived group":
				apiDo(t, admin, "PUT", "/api/v1/groups/"+itoa(g.ID), map[string]any{"status": models.GroupArchived}, http.StatusOK)
				want, code = http.StatusConflict, "group_events_not_approved"
			case "deleted group":
				if err := app.DB.Delete(g).Error; err != nil {
					t.Fatal(err)
				}
				want = http.StatusNotFound
			}
			before := createEventWriteCounts(t, app)
			held.release()
			w := held.wait(t)
			if w.Code != want || (code != "" && !strings.Contains(w.Body.String(), `"code":"`+code+`"`)) {
				t.Fatalf("create after %s: %d %s, want %d %s", change, w.Code, w.Body.String(), want, code)
			}
			if want == http.StatusForbidden && w.Body.String() != `{"code":"not_allowed","error":"forbidden"}` {
				t.Fatalf("changed preflight refusal body: %s", w.Body.String())
			}
			if want == http.StatusNotFound && w.Body.String() != `{"code":"not_found","error":"group not found"}` {
				t.Fatalf("changed preflight hidden body: %s", w.Body.String())
			}
			if after := createEventWriteCounts(t, app); !reflect.DeepEqual(before, after) {
				t.Fatalf("refused create wrote rows: before=%v after=%v", before, after)
			}
		})
	}
}

func requireCreatedEvent(t *testing.T, app *App, w *httptest.ResponseRecorder) models.Event {
	t.Helper()
	if w.Code != http.StatusCreated {
		t.Fatalf("create event: %d %s", w.Code, w.Body.String())
	}
	var body eventSeriesResponseJSON
	decodeJSON(t, w, &body)
	var ev models.Event
	if err := app.DB.First(&ev, body.Event.ID).Error; err != nil {
		t.Fatal(err)
	}
	if n := contactCount(t, app, &models.AuditLog{}, "target_type = 'event' AND target_id = ? AND action = ?", ev.ID, audit.EventCreated); n != 1 {
		t.Fatalf("event.created audits = %d, want exactly one", n)
	}
	if n := contactCount(t, app, &models.EventOccurrence{}, "event_id = ?", ev.ID); n != 1 {
		t.Fatalf("created dates = %d, want one", n)
	}
	var created models.AuditLog
	if err := app.DB.Where("target_type = 'event' AND target_id = ? AND action = ?", ev.ID, audit.EventCreated).First(&created).Error; err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(created.MetaJSON), &meta); err != nil {
		t.Fatal(err)
	}
	if meta["visibility"] != ev.Visibility {
		t.Fatalf("created-event audit visibility = %v, saved %s", meta["visibility"], ev.Visibility)
	}
	return ev
}

func requireCreateVisibilityAudit(t *testing.T, app *App, ev models.Event, groupVisibility, action string) {
	t.Helper()
	var rows []models.AuditLog
	if err := app.DB.Where("target_type = 'event' AND target_id = ? AND action IN ?", ev.ID,
		[]string{audit.VisibilityChanged, audit.VisibilityOverride}).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if action == "" {
		if len(rows) != 0 {
			t.Fatalf("inherited/equal visibility wrote audit: %+v", rows)
		}
		return
	}
	if len(rows) != 1 || rows[0].Action != action {
		t.Fatalf("visibility audits = %+v, want one %s", rows, action)
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(rows[0].MetaJSON), &meta); err != nil {
		t.Fatal(err)
	}
	if meta["oldVisibility"] != groupVisibility || meta["groupVisibility"] != groupVisibility || meta["newVisibility"] != ev.Visibility ||
		meta["atCreation"] != true || meta["oldLeaderOpened"] != false || meta["leaderOpened"] != ev.LeaderOpened {
		t.Fatalf("creation visibility audit used stale facts: %v", meta)
	}
}

func TestCreateEventUsesGroupVisibilityAfterHeldBody(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprintf("explicit=%v", explicit), func(t *testing.T) {
			app := testApp(t)
			leader := newTestUser(t, app, "leader", "password1234", models.RoleMember)
			other := newTestUser(t, app, "other leader", "password1234", models.RoleMember)
			admin := newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
			g := newTestGroup(t, app, leader, "Choir", models.GroupActive)
			addMember(t, app, other, g, models.MembershipLeader)
			client := loginHeld(t, app, leader.Email, "password1234")
			coLeader := loginHeld(t, app, other.Email, "password1234")
			body := createEventBodyForTest()
			if explicit {
				body["visibility"], body["visibilityReason"] = models.VisibilityPublic, "Guests welcome"
			}
			held := client.doHeld("POST", "/api/v1/groups/"+itoa(g.ID)+"/events", body)
			apiDo(t, coLeader, "PUT", "/api/v1/groups/"+itoa(g.ID), map[string]any{"visibility": models.VisibilityPrivate, "confirm": true}, http.StatusOK)
			held.release()
			ev := requireCreatedEvent(t, app, held.wait(t))
			want, action := models.VisibilityPrivate, ""
			if explicit {
				want, action = models.VisibilityPublic, audit.VisibilityOverride
			}
			if ev.Visibility != want || ev.LeaderOpened != explicit {
				t.Fatalf("saved visibility=%s leaderOpened=%v, want %s/%v", ev.Visibility, ev.LeaderOpened, want, explicit)
			}
			requireCreateVisibilityAudit(t, app, ev, models.VisibilityPrivate, action)
			for _, u := range []*models.User{leader, other, admin} {
				wantNotices := int64(0)
				if explicit && u.ID != leader.ID {
					wantNotices = 1
				}
				if n := contactCount(t, app, &models.Notification{}, "user_id = ? AND kind = ? AND subject_id = ?", u.ID, notify.VisibilityOverride, ev.ID); n != wantNotices {
					t.Fatalf("override notices for %s = %d, want %d", u.Name, n, wantNotices)
				}
			}
			if !explicit {
				anon := newAPIClient(t, app)
				var occ models.EventOccurrence
				if err := app.DB.Where("event_id = ?", ev.ID).First(&occ).Error; err != nil {
					t.Fatal(err)
				}
				for _, surface := range []struct {
					path   string
					status int
				}{
					{"/api/v1/groups/" + itoa(g.ID), http.StatusNotFound},
					{"/api/v1/groups", http.StatusOK},
					{"/cal/public.ics", http.StatusOK},
				} {
					w := anon.do("GET", surface.path, nil, false)
					if w.Code != surface.status || strings.Contains(w.Body.String(), ev.Title) || strings.Contains(w.Body.String(), occ.StartsAt.UTC().Format(time.RFC3339)) {
						t.Fatalf("anonymous surface %s disclosed private create: %d %s", surface.path, w.Code, w.Body.String())
					}
					if surface.status == http.StatusNotFound && w.Body.String() != `{"code":"not_found","error":"group not found"}` {
						t.Fatalf("private group detail changed non-disclosure body: %s", w.Body.String())
					}
					if surface.path == "/api/v1/groups" {
						var listed groupListResponseJSON
						decodeJSON(t, w, &listed)
						if len(listed.Groups) != 0 {
							t.Fatalf("private group or its nextEventAt appeared in anonymous directory: %s", w.Body.String())
						}
					}
				}
			}
		})
	}
}

func TestCreateEventVisibilityDefaultsAndChoices(t *testing.T) {
	for _, groupVis := range []string{models.VisibilityPublic, models.VisibilityPrivate, models.VisibilityInviteOnly} {
		for _, choice := range []string{"", models.VisibilityPublic, models.VisibilityPrivate, models.VisibilityInviteOnly} {
			t.Run(groupVis+"/"+choice, func(t *testing.T) {
				app := testApp(t)
				leader := newTestUser(t, app, "leader", "password1234", models.RoleMember)
				admin := newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
				g := activeGroup(t, app, leader, "Choir", groupVis, "")
				client := loginHeld(t, app, leader.Email, "password1234")
				body := createEventBodyForTest()
				want := groupVis
				if choice != "" {
					body["visibility"] = choice
					want = choice
				}
				ev := requireCreatedEvent(t, app, client.do("POST", "/api/v1/groups/"+itoa(g.ID)+"/events", body, true))
				opened := policy.MoreOpen(want, groupVis)
				if ev.Visibility != want || ev.LeaderOpened != opened {
					t.Fatalf("visibility=%s leaderOpened=%v, want %s/%v", ev.Visibility, ev.LeaderOpened, want, opened)
				}
				action := ""
				if want != groupVis {
					action = audit.VisibilityChanged
					if opened {
						action = audit.VisibilityOverride
					}
				}
				requireCreateVisibilityAudit(t, app, ev, groupVis, action)
				wantNotices := int64(0)
				if opened {
					wantNotices = 1
				}
				if n := contactCount(t, app, &models.Notification{}, "kind = ? AND subject_id = ?", notify.VisibilityOverride, ev.ID); n != wantNotices {
					t.Fatalf("visibility notices = %d, want %d for admin %d", n, wantNotices, admin.ID)
				}
			})
		}
	}
}

func TestCreateEventWaitsForLockedGroup(t *testing.T) {
	app := testApp(t)
	leader := newTestUser(t, app, "leader", "password1234", models.RoleMember)
	g := newTestGroup(t, app, leader, "Choir", models.GroupActive)
	client := loginHeld(t, app, leader.Email, "password1234")
	tx := app.DB.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	var blocker int
	if err := tx.Raw("SELECT pg_backend_pid()").Scan(&blocker).Error; err != nil {
		t.Fatal(err)
	}
	var locked models.Group
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, g.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Model(&locked).Update("visibility", models.VisibilityPrivate).Error; err != nil {
		t.Fatal(err)
	}
	body := createEventBodyForTest()
	done := queueContact(func() *httptest.ResponseRecorder {
		return client.do("POST", "/api/v1/groups/"+itoa(g.ID)+"/events", body, true)
	})
	// This holder deliberately has no contact gate. A SHARE waiter proves the
	// writing transaction, rather than the preflight, reads the new group row.
	testdb.WaitUntil(t, "create waiting on the group SHARE lock", 5*time.Second, func() bool {
		var waiting bool
		err := app.DB.Raw(`SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'
			AND query LIKE '%FROM "groups"%' AND query LIKE '%FOR SHARE%'
			AND ? = ANY(pg_blocking_pids(pid)))`, blocker).Scan(&waiting).Error
		if err != nil {
			t.Fatal(err)
		}
		return waiting
	})
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	ev := requireCreatedEvent(t, app, awaitContactResponse(t, done))
	if ev.Visibility != models.VisibilityPrivate || ev.LeaderOpened {
		t.Fatalf("create used pre-lock group visibility: %+v", ev)
	}
	requireCreateVisibilityAudit(t, app, ev, models.VisibilityPrivate, "")
}

// Invitation acceptance and departure lock the group before the creating
// leader's user row. Both orders at the gate must serialize without a cycle.
func TestCreateEventSerializesWithGroupFirstWriters(t *testing.T) {
	for _, writer := range []string{"invitation", "departure"} {
		for iteration := 0; iteration < 4; iteration++ {
			t.Run(fmt.Sprintf("%s/%d", writer, iteration), func(t *testing.T) {
				app := testApp(t)
				logs := captureServerLog(t)
				states := erasureTestSQLStates(t, app)
				leader := newTestUser(t, app, "leader", "password1234", models.RoleMember)
				g := newTestGroup(t, app, leader, "Choir", models.GroupActive)
				creator := loginHeld(t, app, leader.Email, "password1234")
				otherClient := cloneContactClient(creator)
				method, path, otherBody := "POST", "", map[string]any{}
				if writer == "invitation" {
					const email = "new-invitee@example.com"
					apiDo(t, creator, "POST", "/api/v1/invitations", map[string]any{"email": email, "groupId": g.ID}, http.StatusCreated)
					token := tokenInEmail(t, latestEmail(t, app, email))
					otherClient = newAPIClient(t, app)
					otherClient.fetchCSRF()
					otherClient.holdNotices = true
					path = "/api/v1/invitations/accept"
					otherBody = map[string]any{"token": token, "name": "Invitee", "password": "password1234", "ageBand": "adult"}
				} else {
					subject := newTestUser(t, app, "departing member", "password1234", models.RoleMember)
					addMember(t, app, subject, g, models.MembershipMember)
					path = "/api/v1/groups/" + itoa(g.ID) + "/members/" + itoa(subject.ID) + "/departure"
					preview := getDeparturePreview(t, otherClient, path+"-preview", http.StatusOK)
					otherBody = map[string]any{"snapshot": preview.Snapshot, "keepOccurrenceIds": []uint{}}
				}
				body := createEventBodyForTest()
				create := func() *httptest.ResponseRecorder {
					return creator.do("POST", "/api/v1/groups/"+itoa(g.ID)+"/events", body, true)
				}
				write := func() *httptest.ResponseRecorder { return otherClient.do(method, path, otherBody, true) }
				gate := holdContactGate(t, app)
				first, second := create, write
				if iteration%2 != 0 {
					first, second = write, create
				}
				one := queueContact(first)
				waitContactWaiters(t, app, 1)
				two := queueContact(second)
				waitContactWaiters(t, app, 2)
				if err := gate.Commit().Error; err != nil {
					t.Fatal(err)
				}
				firstResponse, secondResponse := awaitContactResponse(t, one), awaitContactResponse(t, two)
				created, changed := firstResponse, secondResponse
				if iteration%2 != 0 {
					created, changed = secondResponse, firstResponse
				}
				requireCreatedEvent(t, app, created)
				if changed.Code != http.StatusOK {
					t.Fatalf("%s interleaving: %d %s", writer, changed.Code, changed.Body.String())
				}
				if states.Count("40P01") != 0 || strings.Contains(logs.String(), "40P01") || strings.Contains(logs.String(), "deadlock detected") {
					t.Fatalf("%s/create deadlocked: %s", writer, logs.String())
				}
			})
		}
	}
}

func TestCreateEventRefusesMembersAndTeens(t *testing.T) {
	app := testApp(t)
	leader := newTestUser(t, app, "leader", "password1234", models.RoleMember)
	g := newTestGroup(t, app, leader, "Choir", models.GroupActive)
	for _, accountType := range []string{models.AccountAdult, models.AccountTeen} {
		u := newTestUser(t, app, accountType, "password1234", models.RoleMember)
		if err := app.DB.Model(u).Update("account_type", accountType).Error; err != nil {
			t.Fatal(err)
		}
		addMember(t, app, u, g, models.MembershipMember)
		client := loginHeld(t, app, u.Email, "password1234")
		before := createEventWriteCounts(t, app)
		apiDo(t, client, "POST", "/api/v1/groups/"+itoa(g.ID)+"/events", createEventBodyForTest(), http.StatusForbidden)
		if after := createEventWriteCounts(t, app); !reflect.DeepEqual(before, after) {
			t.Fatalf("%s member create wrote rows: %v -> %v", accountType, before, after)
		}
	}
}

func TestUpdateOccurrenceUsesZoneAfterHeldBody(t *testing.T) {
	for _, tc := range []struct {
		zone      string
		clock     string
		utcOffset time.Duration
	}{
		{"Asia/Tokyo", "00:30", -8*time.Hour - 30*time.Minute},
		{"Pacific/Honolulu", "23:30", 33*time.Hour + 30*time.Minute},
	} {
		t.Run(tc.zone, func(t *testing.T) {
			f := newEventWriterRace(t, false)
			eventPath := "/api/v1/events/" + itoa(f.event.ID)
			wall := f.date + "T" + tc.clock
			held := f.actor.doHeld("PUT", eventPath+"/occurrences/"+f.date,
				map[string]any{"startsLocal": " " + wall + " ", "durationMinutes": 90})
			apiDo(t, f.admin, "PUT", eventPath, map[string]any{"tzid": tc.zone}, http.StatusOK)
			var before models.EventOccurrence
			if err := f.app.DB.Where("event_id = ? AND original_date = ?", f.event.ID, f.date).First(&before).Error; err != nil {
				t.Fatal(err)
			}
			held.release()
			w := held.wait(t)
			if w.Code != http.StatusOK {
				t.Fatalf("date edit after zone change: %d %s", w.Code, w.Body.String())
			}
			day, err := models.ParseCalendarDate(f.date)
			if err != nil {
				t.Fatal(err)
			}
			wantStart := day.Add(tc.utcOffset)
			wantEnd := wantStart.Add(90 * time.Minute)
			var response occurrenceResponseJSON
			decodeJSON(t, w, &response)
			got := response.Occurrence
			if got.ID != before.ID || got.Date != f.date || got.TZID != tc.zone || got.StartsLocal != wall ||
				!got.StartsAt.Equal(wantStart) || !got.EndsAt.Equal(wantEnd) || got.DurationMinutes != 90 {
				t.Fatalf("date response: %+v, want %s in %s, UTC %s to %s on original date %s", got, wall, tc.zone, wantStart, wantEnd, f.date)
			}
			var saved models.EventOccurrence
			if err := f.app.DB.First(&saved, before.ID).Error; err != nil {
				t.Fatal(err)
			}
			if !saved.StartsAt.Equal(wantStart) || !saved.EndsAt.Equal(wantEnd) ||
				saved.StartsAtOverride == nil || !saved.StartsAtOverride.Equal(wantStart) ||
				saved.DurationOverride == nil || *saved.DurationOverride != 90 ||
				models.FormatCalendarDate(saved.OriginalDate) != f.date || saved.Status != before.Status || saved.CancelSource != before.CancelSource {
				t.Fatalf("saved date: %+v, want UTC %s to %s with date and status preserved", saved, wantStart, wantEnd)
			}
		})
	}
}

func TestUpdateOccurrenceInvalidStartAfterHeldBodyWritesNothing(t *testing.T) {
	f := newEventWriterRace(t, false)
	eventPath := "/api/v1/events/" + itoa(f.event.ID)
	held := f.actor.doHeld("PUT", eventPath+"/occurrences/"+f.date, map[string]any{
		"startsLocal": "not a wall time", "durationMinutes": 90, "title": "Changed date",
		"location": "New hall", "visibility": models.VisibilityPrivate, "confirm": true,
	})
	apiDo(t, f.admin, "PUT", eventPath, map[string]any{"tzid": "Asia/Tokyo"}, http.StatusOK)
	before := eventWriterState(t, f.app, f.event.ID)
	held.release()
	w := held.wait(t)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid date start: %d %s", w.Code, w.Body.String())
	}
	var answer map[string]any
	decodeJSON(t, w, &answer)
	if len(answer) != 2 || answer["code"] != errInvalidStart.Code || answer["error"] != errInvalidStart.Message {
		t.Fatalf("invalid date start answer: %v", answer)
	}
	if after := eventWriterState(t, f.app, f.event.ID); before != after {
		t.Fatalf("invalid date start changed rows:\nbefore %s\nafter %s", before, after)
	}
}

func TestUpdateOccurrenceEventDeletedAfterHeldBody(t *testing.T) {
	f := newEventWriterRace(t, false)
	eventPath := "/api/v1/events/" + itoa(f.event.ID)
	held := f.actor.doHeld("PUT", eventPath+"/occurrences/"+f.date, map[string]any{
		"startsLocal": f.date + "T21:00", "location": "New hall", "visibility": models.VisibilityPrivate,
	})
	apiDo(t, f.admin, "DELETE", eventPath, nil, http.StatusOK)
	before := eventWriterState(t, f.app, f.event.ID)
	held.release()
	w := held.wait(t)
	if w.Code != http.StatusNotFound {
		t.Fatalf("date edit after event deletion: %d %s", w.Code, w.Body.String())
	}
	var answer map[string]any
	decodeJSON(t, w, &answer)
	if len(answer) != 2 || answer["code"] != "not_found" || answer["error"] != "event not found" {
		t.Fatalf("deleted event answer: %v", answer)
	}
	if after := eventWriterState(t, f.app, f.event.ID); before != after {
		t.Fatalf("date edit after event deletion changed rows:\nbefore %s\nafter %s", before, after)
	}
}

func TestEventSiblingWritersRecheckAfterHeldBody(t *testing.T) {
	for _, writer := range []string{"series", "cancel"} {
		for _, change := range []string{"demoted admin", "disabled account", "removed leader"} {
			t.Run(writer+"/"+change, func(t *testing.T) {
				f := newEventWriterRace(t, change == "demoted admin")
				method, path := "PUT", "/api/v1/events/"+itoa(f.event.ID)
				body := map[string]any{"title": "Changed series", "location": "New hall"}
				if writer == "cancel" {
					method, path, body = "POST", path+"/cancel", map[string]any{"date": f.date}
				}
				held := f.actor.doHeld(method, path, body)
				f.revoke(t, change)
				before := eventWriterState(t, f.app, f.event.ID)
				held.release()
				w := held.wait(t)
				want := http.StatusUnauthorized
				if change == "removed leader" {
					want = http.StatusForbidden
				}
				if w.Code != want {
					t.Fatalf("%s after %s: %d %s, want %d", writer, change, w.Code, w.Body.String(), want)
				}
				if after := eventWriterState(t, f.app, f.event.ID); before != after {
					t.Fatalf("refused %s changed rows:\nbefore %s\nafter %s", writer, before, after)
				}
			})
		}
	}
}

// DELETE has no body. Queue a real demotion first at the contact gate, then
// the authenticated delete, so its transaction must judge the revoked actor.
func TestDeleteEventRechecksAfterContactGate(t *testing.T) {
	f := newEventWriterRace(t, true)
	before := eventRow(t, f.app, f.event.ID)
	gate := holdContactGate(t, f.app)
	demotion := queueContact(func() *httptest.ResponseRecorder {
		return f.admin.do("PUT", "/api/v1/admin/users/"+itoa(f.user.ID), map[string]any{"role": models.RoleMember}, true)
	})
	waitContactWaiters(t, f.app, 1)
	deletion := queueContact(func() *httptest.ResponseRecorder {
		return f.actor.do("DELETE", "/api/v1/events/"+itoa(f.event.ID), nil, true)
	})
	waitContactWaiters(t, f.app, 2)
	if err := gate.Commit().Error; err != nil {
		t.Fatal(err)
	}
	if w := awaitContactResponse(t, demotion); w.Code != http.StatusOK {
		t.Fatalf("demotion: %d %s", w.Code, w.Body.String())
	}
	if w := awaitContactResponse(t, deletion); w.Code != http.StatusUnauthorized {
		t.Fatalf("delete by demoted admin: %d %s", w.Code, w.Body.String())
	}
	if after := eventRow(t, f.app, f.event.ID); !reflect.DeepEqual(before, after) {
		t.Fatalf("refused delete changed event: before=%+v after=%+v", before, after)
	}
	if n := contactCount(t, f.app, &models.EventOccurrence{}, "event_id = ?", f.event.ID); n != 1 {
		t.Fatalf("refused delete left %d dates, want 1", n)
	}
	if n := contactCount(t, f.app, &models.AuditLog{}, "target_type = 'event' AND target_id = ? AND action = ?", f.event.ID, audit.EventDeleted); n != 0 {
		t.Fatalf("refused delete wrote %d deletion audits", n)
	}
}

// A user row held by an ungated writer must not stop these gated writers
// before their event lock. Adding an actor row lock here would create a
// user/event cycle with registration paths that already hold the user.
func TestEventWritersKeepEventFirstLockOrder(t *testing.T) {
	for _, writer := range []string{"visibility", "series", "delete", "cancel", "date"} {
		t.Run(writer, func(t *testing.T) {
			f := newEventWriterRace(t, false)
			tx := f.app.DB.Begin()
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			defer tx.Rollback()
			var blocker int
			if err := tx.Raw("SELECT pg_backend_pid()").Scan(&blocker).Error; err != nil {
				t.Fatal(err)
			}
			var actor models.User
			if err := tx.Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).First(&actor, f.user.ID).Error; err != nil {
				t.Fatal(err)
			}
			var ev models.Event
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&ev, f.event.ID).Error; err != nil {
				t.Fatal(err)
			}
			method, path, body := "PUT", "/api/v1/events/"+itoa(f.event.ID), map[string]any{"title": "Changed title"}
			switch writer {
			case "visibility":
				path, body = path+"/visibility", map[string]any{"visibility": models.VisibilityPrivate}
			case "delete":
				method, body = "DELETE", nil
			case "cancel":
				method, path, body = "POST", path+"/cancel", map[string]any{"date": f.date}
			case "date":
				path, body = path+"/occurrences/"+f.date, map[string]any{"visibility": models.VisibilityPrivate}
			}
			done := queueContact(func() *httptest.ResponseRecorder { return f.actor.do(method, path, body, true) })
			testdb.WaitUntil(t, "event writer waiting on its first row lock", 5*time.Second, func() bool {
				var waiting bool
				err := f.app.DB.Raw(`SELECT EXISTS (SELECT 1 FROM pg_stat_activity
					WHERE datname = current_database() AND wait_event_type = 'Lock'
					AND query LIKE '%FROM "events"%' AND query LIKE '%FOR UPDATE%'
					AND ? = ANY(pg_blocking_pids(pid)))`, blocker).Scan(&waiting).Error
				if err != nil {
					t.Fatal(err)
				}
				return waiting
			})
			if err := tx.Commit().Error; err != nil {
				t.Fatal(err)
			}
			if w := awaitContactResponse(t, done); w.Code != http.StatusOK {
				t.Fatalf("%s after lock release: %d %s", writer, w.Code, w.Body.String())
			}
		})
	}
}
