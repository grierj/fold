package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"fold/internal/events"
	"fold/models"
)

// seedEvent inserts a one-time UTC event and its occurrence. Direct inserts
// have to set CreatedBy (the foreign key) and a scheduled occurrence, or
// registrations and the notification feed see nothing.
func seedEvent(t *testing.T, app *App, g *models.Group, createdBy uint, start time.Time) (models.Event, models.EventOccurrence) {
	t.Helper()
	start = start.UTC()
	gid := g.ID
	vis := g.Visibility
	if vis == "" {
		vis = models.VisibilityPublic
	}
	ev := models.Event{
		GroupID: &gid, Visibility: vis,
		StartsLocal:     start.Format("2006-01-02T15:04"),
		DurationMinutes: 60, TZID: "UTC", Location: "", CreatedBy: createdBy,
	}
	if err := app.DB.Create(&ev).Error; err != nil {
		t.Fatalf("seed event: %v", err)
	}
	occ := models.EventOccurrence{
		EventID: ev.ID, OriginalDate: start.Format("2006-01-02"),
		StartsAt: start, EndsAt: start.Add(time.Hour),
		Status: models.OccurrenceScheduled,
	}
	if err := app.DB.Create(&occ).Error; err != nil {
		t.Fatalf("seed occurrence: %v", err)
	}
	return ev, occ
}

func openGroup(t *testing.T, app *App, name string) (*apiClient, string) {
	t.Helper()
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	w := admin.do("POST", "/api/v1/groups", map[string]any{"name": name, "location": "Hall"}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create group: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Group groupSummaryJSON `json:"group"`
	}
	decodeJSON(t, w, &created)
	return admin, itoa(created.Group.ID)
}

func listOccurrences(t *testing.T, c *apiClient, gid, from, to string) []occurrenceJSON {
	t.Helper()
	w := c.do("GET", "/api/v1/groups/"+gid+"/events?from="+from+"&to="+to, nil, false)
	if w.Code != http.StatusOK {
		t.Fatalf("list events: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Events []occurrenceJSON `json:"events"`
	}
	decodeJSON(t, w, &body)
	return body.Events
}

func occurrenceOn(t *testing.T, list []occurrenceJSON, date string) occurrenceJSON {
	t.Helper()
	for _, o := range list {
		if o.Date == date {
			return o
		}
	}
	t.Fatalf("no occurrence on %s (%d in the list)", date, len(list))
	return occurrenceJSON{}
}

func weeklyRule(start time.Time) string {
	days := []string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}
	return "FREQ=WEEKLY;BYDAY=" + days[start.UTC().Weekday()]
}

func atUTC(t time.Time, hour, min int) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), hour, min, 0, 0, time.UTC)
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
// dates the new rule drops (and tells going/maybe), and leaves per-date
// overrides alone. A date already underway stays put.
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
	assertNotice(pat, true)
	assertNotice(sam, false)
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
	if err := events.Extend(app.DB, time.Now().UTC()); err != nil {
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
	if err := events.Extend(app.DB, time.Now().UTC()); err != nil {
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
	if err := app.DB.Where("event_id = ? AND original_date <> ?", created.Event.ID, cancel.OriginalDate).First(&drop).Error; err != nil {
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
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", events.LockID).Scan(&got); err != nil || !got {
		t.Fatalf("lock: got %v err %v", got, err)
	}
	if err := events.ExtendLocked(ctx, app.DB, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var missing int64
	app.DB.Model(&models.EventOccurrence{}).Where("event_id = ? AND original_date = ?", created.Event.ID, drop.OriginalDate).Count(&missing)
	if missing != 0 {
		t.Fatal("extend ran while the advisory lock was held")
	}
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", events.LockID); err != nil {
		t.Fatal(err)
	}
	if err := events.ExtendLocked(ctx, app.DB, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	app.DB.Model(&models.EventOccurrence{}).Where("event_id = ? AND original_date = ?", created.Event.ID, drop.OriginalDate).Count(&missing)
	if missing != 1 {
		t.Fatalf("missing date not restored: %d", missing)
	}
}
