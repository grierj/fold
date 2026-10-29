package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"fold/models"
)

type calendarPage struct {
	Occurrences []calendarOccurrenceJSON `json:"occurrences"`
	Truncated   bool                     `json:"truncated"`
}

func useCalendarTestClock(app *App, now time.Time) {
	app.calendarClock = func() time.Time { return now.UTC() }
}

func getCalendar(t *testing.T, c *apiClient, query string, want int) (calendarPage, string) {
	t.Helper()
	w := c.do("GET", "/api/v1/calendar?"+query, nil, false)
	if w.Code != want {
		t.Fatalf("get calendar %s: %d %s, want %d", query, w.Code, w.Body.String(), want)
	}
	var page calendarPage
	if want == http.StatusOK {
		decodeJSON(t, w, &page)
	}
	return page, w.Body.String()
}

func calendarTitles(page calendarPage) []string {
	out := []string{}
	for _, o := range page.Occurrences {
		out = append(out, o.Title+"@"+o.Date)
	}
	return out
}

func hasTitle(page calendarPage, title string) bool {
	for _, o := range page.Occurrences {
		if o.Title == title {
			return true
		}
	}
	return false
}

// M1.6 acceptance: a member never sees invite-only events of groups they
// are not in, signed-out visitors see public events only, and the slim
// calendar rows carry no attendee or declined data for anyone (DEC-64).
func TestCalendarVisibilityAndSlimRows(t *testing.T) {
	app := testApp(t)
	useCalendarTestClock(app, time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC))
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")

	open := createGroupAs(t, admin, "Open Choir", models.VisibilityPublic)
	openEv := scheduleGroupEvent(t, admin, open, "Rehearsal")
	secret := createGroupAs(t, admin, "Secret Circle", models.VisibilityInviteOnly)
	scheduleGroupEvent(t, admin, secret, "Secret Night")
	membersOnly := createGroupAs(t, admin, "Members Circle", models.VisibilityPrivate)
	scheduleGroupEvent(t, admin, membersOnly, "Members Night")

	var openGroup models.Group
	if err := app.DB.First(&openGroup, open).Error; err != nil {
		t.Fatal(err)
	}
	_, member := mkUser(t, app, &openGroup, "mem")
	_, decliner := mkUser(t, app, &openGroup, "nope")
	_, goer := mkUser(t, app, &openGroup, "yes")
	regPath := fmt.Sprintf("/api/v1/events/%d/registrations", openEv.ID)
	apiDo(t, decliner, "PUT", regPath, map[string]any{"status": "declined", "date": "2026-12-01"}, http.StatusOK)
	apiDo(t, goer, "PUT", regPath, map[string]any{"status": "going", "date": "2026-12-01"}, http.StatusOK)

	const window = "from=2026-11-30&to=2026-12-02&tz=UTC"
	guest := newAPIClient(t, app)

	page, raw := getCalendar(t, member, window, http.StatusOK)
	if !hasTitle(page, "Rehearsal") {
		t.Fatalf("member should see the public event: %v", calendarTitles(page))
	}
	if hasTitle(page, "Secret Night") || strings.Contains(raw, "Secret Circle") {
		t.Fatalf("member saw an invite-only group's event: %s", raw)
	}

	page, raw = getCalendar(t, guest, window, http.StatusOK)
	if !hasTitle(page, "Rehearsal") || hasTitle(page, "Secret Night") || hasTitle(page, "Members Night") {
		t.Fatalf("signed-out calendar should hold public events only: %v", calendarTitles(page))
	}
	if strings.Contains(raw, "Secret Circle") || strings.Contains(raw, "Members Circle") {
		t.Fatalf("signed-out calendar named a hidden group: %s", raw)
	}

	// The slim rows never carry attendance, whoever asks (DEC-64): no
	// attendee names, no counts, and no declined key or value, even for
	// the admin who runs the event.
	for _, who := range []*apiClient{member, admin, guest} {
		_, raw := getCalendar(t, who, window, http.StatusOK)
		for _, banned := range []string{`"attendees"`, `"registrations"`, "declined", `"seatsUsed"`, `"capacity"`, `"name":"nope"`, `"name":"yes"`} {
			if strings.Contains(raw, banned) {
				t.Fatalf("calendar response contains %s: %s", banned, raw)
			}
		}
	}

	// Each viewer gets only their own answer. Can't go remains visible in
	// Everything so they can find and change it.
	page, _ = getCalendar(t, goer, window, http.StatusOK)
	for _, o := range page.Occurrences {
		if o.Title == "Rehearsal" && (o.MyStatus == nil || *o.MyStatus != "going") {
			t.Fatalf("goer's row should say going: %+v", o)
		}
	}
	page, _ = getCalendar(t, decliner, window, http.StatusOK)
	for _, o := range page.Occurrences {
		if o.Title == "Rehearsal" && (o.MyStatus == nil || *o.MyStatus != models.RegDeclined || !o.OwnScope) {
			t.Fatalf("decliner's Everything row should say Can't go via their own status: %+v", o)
		}
	}
	page, _ = getCalendar(t, decliner, window+"&mine=1", http.StatusOK)
	if hasTitle(page, "Rehearsal") {
		t.Fatalf("Can't go should disappear from Just mine: %+v", page.Occurrences)
	}
}

// "Just mine" is per occurrence: my groups' events, events I host, and
// the specific dates I answered going or maybe — not the whole series.
func TestCalendarMineNarrowsPerOccurrence(t *testing.T) {
	app := testApp(t)
	useCalendarTestClock(app, time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC))
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")

	mine := createGroupAs(t, admin, "My Choir", models.VisibilityPublic)
	scheduleGroupEvent(t, admin, mine, "My Rehearsal")
	other := createGroupAs(t, admin, "Other Choir", models.VisibilityPublic)
	w := admin.do("POST", "/api/v1/groups/"+itoa(other)+"/events", map[string]any{
		"title": "Other Series", "startsLocal": "2026-12-01T19:00", "durationMinutes": 60,
		"tzid": "UTC", "recurrenceRule": "FREQ=WEEKLY;BYDAY=TU;COUNT=3",
	}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create series: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Event eventJSON `json:"event"`
	}
	decodeJSON(t, w, &created)
	series := created.Event

	var myGroup models.Group
	if err := app.DB.First(&myGroup, mine).Error; err != nil {
		t.Fatal(err)
	}
	_, member := mkUser(t, app, &myGroup, "mem")

	// A church-wide event the member hosts.
	w = admin.do("POST", "/api/v1/events", map[string]any{
		"title": "Church Picnic", "startsLocal": "2026-12-05T17:00", "durationMinutes": 60,
		"tzid": "UTC", "hosts": []string{"mem@example.com"},
	}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create church event: %d %s", w.Code, w.Body.String())
	}
	regPath := fmt.Sprintf("/api/v1/events/%d/registrations", series.ID)
	apiDo(t, member, "PUT", regPath, map[string]any{"status": "going", "date": "2026-12-08"}, http.StatusOK)
	apiDo(t, member, "PUT", regPath, map[string]any{"status": "maybe", "date": "2026-12-15"}, http.StatusOK)

	const window = "from=2026-12-01&to=2026-12-31&tz=UTC"
	all, _ := getCalendar(t, member, window, http.StatusOK)
	if got := calendarTitles(all); len(got) != 4 {
		t.Fatalf("everything view: the out-of-window unanswered public date should be omitted, got %v", got)
	}

	only, _ := getCalendar(t, member, window+"&mine=1", http.StatusOK)
	want := []string{"My Rehearsal@2026-12-01", "Church Picnic@2026-12-05", "Other Series@2026-12-08", "Other Series@2026-12-15"}
	if got := calendarTitles(only); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("mine view: got %v, want %v", got, want)
	}
	for _, o := range only.Occurrences {
		if o.Title == "Church Picnic" && !o.ChurchWide {
			t.Fatalf("picnic should be church-wide: %+v", o)
		}
		if o.Title == "Other Series" && o.Date == "2026-12-08" && (o.MyStatus == nil || *o.MyStatus != "going") {
			t.Fatalf("Dec 8 should say going: %+v", o)
		}
		if o.Title == "Other Series" && o.Date == "2026-12-15" && (o.MyStatus == nil || *o.MyStatus != "maybe") {
			t.Fatalf("Dec 15 should say maybe: %+v", o)
		}
	}

	// mine is ignored when signed out: Everything still applies its window.
	guest := newAPIClient(t, app)
	guestAll, _ := getCalendar(t, guest, window, http.StatusOK)
	guestMine, _ := getCalendar(t, guest, window+"&mine=1", http.StatusOK)
	if got, want := strings.Join(calendarTitles(guestMine), ","), strings.Join(calendarTitles(guestAll), ","); got != want || len(guestMine.Occurrences) != 0 {
		t.Fatalf("signed-out mine=1 changed windowed Everything: all=%v mine=%v", calendarTitles(guestAll), calendarTitles(guestMine))
	}
}

func TestAnonymousEverythingUsesThirtyDayWindow(t *testing.T) {
	app := testApp(t)
	now := time.Date(2030, 1, 15, 12, 0, 0, 0, time.UTC)
	useCalendarTestClock(app, now)
	leader := newTestUser(t, app, "calendar boundary leader", "password1234", models.RoleMember)
	viewer := newTestUser(t, app, "calendar boundary viewer", "password1234", models.RoleMember)
	client := loginAPI(t, app, viewer.Name, "password1234")
	discovery := activeGroup(t, app, leader, "Calendar discovery boundaries", models.VisibilityPublic, "")
	own := activeGroup(t, app, leader, "Calendar own boundary", models.VisibilityPublic, "")
	addMember(t, app, viewer, &own, models.MembershipMember)

	seedNamed := func(group *models.Group, title string, startsAt time.Time) (models.Event, models.EventOccurrence) {
		t.Helper()
		ev, occ := seedEvent(t, app, group, leader.ID, startsAt)
		if err := app.DB.Model(&ev).Update("title", title).Error; err != nil {
			t.Fatal(err)
		}
		ev.Title = title
		return ev, occ
	}
	seedChurch := func(title string, startsAt time.Time) {
		t.Helper()
		ev := models.Event{Title: title, Visibility: models.VisibilityPublic, StartsLocal: startsAt.UTC().Format("2006-01-02T15:04"), DurationMinutes: 60, TZID: "UTC", CreatedBy: leader.ID}
		if err := app.DB.Create(&ev).Error; err != nil {
			t.Fatal(err)
		}
		occ := models.EventOccurrence{EventID: ev.ID, OriginalDate: startsAt.UTC().Format("2006-01-02"), StartsAt: startsAt.UTC(), EndsAt: startsAt.UTC().Add(time.Hour), Status: models.OccurrenceScheduled}
		if err := app.DB.Create(&occ).Error; err != nil {
			t.Fatal(err)
		}
	}
	seedNamed(&discovery, "Past public", now.Add(-time.Microsecond))
	seedNamed(&discovery, "At public boundary", now.AddDate(0, 0, 30))
	seedNamed(&discovery, "Beyond public boundary", now.AddDate(0, 0, 30).Add(time.Microsecond))
	seedChurch("Past church-wide", now.Add(-time.Microsecond))
	seedChurch("At church-wide boundary", now.AddDate(0, 0, 30))
	seedChurch("Beyond church-wide boundary", now.AddDate(0, 0, 30).Add(time.Microsecond))
	seedNamed(&discovery, "Far public", now.AddDate(0, 0, 60))
	answered, answeredOcc := seedNamed(&discovery, "Far answered", now.AddDate(0, 0, 60).Add(time.Hour))
	seedNamed(&own, "Far own group", now.AddDate(0, 0, 60).Add(2*time.Hour))
	apiDo(t, client, "PUT", fmt.Sprintf("/api/v1/events/%d/registrations", answered.ID), map[string]any{"status": models.RegMaybe, "date": answeredOcc.OriginalDate}, http.StatusOK)

	query := "from=2030-01-14&to=2030-03-17&tz=UTC"
	anon := newAPIClient(t, app)
	for _, suffix := range []string{"", "&mine=1"} {
		page, raw := getCalendar(t, anon, query+suffix, http.StatusOK)
		if got := calendarTitles(page); len(got) != 2 || !hasTitle(page, "At public boundary") || !hasTitle(page, "At church-wide boundary") {
			t.Fatalf("anonymous Everything%s bypassed the fixed window: got=%v raw=%s", suffix, got, raw)
		}
		for _, title := range []string{"Past public", "Beyond public boundary", "Past church-wide", "Beyond church-wide boundary", "Far public", "Far answered", "Far own group"} {
			if hasTitle(page, title) {
				t.Fatalf("anonymous Everything%s kept %q: %s", suffix, title, raw)
			}
		}
	}

	all, raw := getCalendar(t, client, query, http.StatusOK)
	for _, title := range []string{"At public boundary", "At church-wide boundary", "Far answered", "Far own group"} {
		if !hasTitle(all, title) {
			t.Fatalf("signed-in Everything lost %q exception: %s", title, raw)
		}
	}
	for _, title := range []string{"Past public", "Beyond public boundary", "Past church-wide", "Beyond church-wide boundary", "Far public"} {
		if hasTitle(all, title) {
			t.Fatalf("signed-in Everything kept %q: %s", title, raw)
		}
	}
	mine, raw := getCalendar(t, client, query+"&mine=1", http.StatusOK)
	for _, title := range []string{"At church-wide boundary", "Far answered", "Far own group"} {
		if !hasTitle(mine, title) {
			t.Fatalf("signed-in Just mine lost %q exception: %s", title, raw)
		}
	}
	if hasTitle(mine, "At public boundary") {
		t.Fatalf("unanswered outsider public date entered Just mine: %s", raw)
	}
}

func TestCalendarWindowGroupFilterAndZones(t *testing.T) {
	app := testApp(t)
	useCalendarTestClock(app, time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC))
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	open := createGroupAs(t, admin, "Open Choir", models.VisibilityPublic)
	scheduleGroupEvent(t, admin, open, "Rehearsal") // 2026-12-01T19:00 UTC
	secret := createGroupAs(t, admin, "Secret Circle", models.VisibilityInviteOnly)

	guest := newAPIClient(t, app)

	// A window over 366 days is a 400, a bad zone is a 400, and a group
	// the viewer can't see is a 404.
	getCalendar(t, guest, "from=2026-01-01&to=2027-01-03&tz=UTC", http.StatusBadRequest)
	getCalendar(t, guest, "from=2026-12-01&to=2026-12-02&tz=Mars/Olympus", http.StatusBadRequest)
	getCalendar(t, guest, fmt.Sprintf("from=2026-12-01&to=2026-12-02&tz=UTC&group=%d", secret), http.StatusNotFound)

	// The group filter narrows to that group's events.
	page, _ := getCalendar(t, guest, fmt.Sprintf("from=2026-12-01&to=2026-12-02&tz=UTC&group=%d", open), http.StatusOK)
	if len(page.Occurrences) != 1 || page.Occurrences[0].GroupName != "Open Choir" {
		t.Fatalf("group filter: %+v", page.Occurrences)
	}

	// The window follows the viewer's zone: 19:00 UTC on Dec 1 is Dec 2
	// in Kiritimati (UTC+14), so it lands in a Dec 2 window there and
	// not in a Dec 2 UTC window.
	page, _ = getCalendar(t, guest, "from=2026-12-02&to=2026-12-02&tz=Pacific/Kiritimati", http.StatusOK)
	if !hasTitle(page, "Rehearsal") {
		t.Fatalf("Kiritimati Dec 2 should include the event: %v", calendarTitles(page))
	}
	page, _ = getCalendar(t, guest, "from=2026-12-02&to=2026-12-02&tz=UTC", http.StatusOK)
	if hasTitle(page, "Rehearsal") {
		t.Fatalf("UTC Dec 2 should not include the event: %v", calendarTitles(page))
	}
}

func TestCalendarTruncatesAt500(t *testing.T) {
	app := testApp(t)
	useCalendarTestClock(app, time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC))
	leader := newTestUser(t, app, "lead", "password1234", models.RoleMember)
	grp := activeGroup(t, app, leader, "Busy Choir", models.VisibilityPublic, "")
	start := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < maxCalendarOccurrences+1; i++ {
		seedEvent(t, app, &grp, leader.ID, start.Add(time.Duration(i)*time.Minute))
	}
	guest := newAPIClient(t, app)
	page, _ := getCalendar(t, guest, "from=2026-12-01&to=2026-12-01&tz=UTC", http.StatusOK)
	if len(page.Occurrences) != maxCalendarOccurrences || !page.Truncated {
		t.Fatalf("want %d rows and truncated, got %d rows truncated=%v",
			maxCalendarOccurrences, len(page.Occurrences), page.Truncated)
	}
}

// DEC-68: a deleted group's events are hidden from everyone by default;
// an admin's "show deleted events" switch (deleted=1) lists them, marked
// groupDeleted, and the switch does nothing for anyone else.
func TestCalendarShowDeletedIsAdminOnly(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	newTestUser(t, app, "mem", "password1234", models.RoleMember)
	mem := loginAPI(t, app, "mem", "password1234")
	gid := createGroupAs(t, admin, "Old Choir", models.VisibilityPublic)
	scheduleGroupEvent(t, admin, gid, "Last Rehearsal") // 2026-12-01T19:00 UTC
	if err := app.DB.Delete(&models.Group{}, gid).Error; err != nil {
		t.Fatal(err)
	}
	q := "from=2026-12-01&to=2026-12-02&tz=UTC"
	if page, _ := getCalendar(t, admin, q, http.StatusOK); hasTitle(page, "Last Rehearsal") {
		t.Fatalf("admin sees a deleted group's event by default: %v", calendarTitles(page))
	}
	page, _ := getCalendar(t, admin, q+"&deleted=1", http.StatusOK)
	if len(page.Occurrences) != 1 || !page.Occurrences[0].GroupDeleted {
		t.Fatalf("admin with deleted=1: %+v", page.Occurrences)
	}
	for _, c := range []*apiClient{mem, newAPIClient(t, app)} {
		if page, body := getCalendar(t, c, q+"&deleted=1", http.StatusOK); hasTitle(page, "Last Rehearsal") || strings.Contains(body, "groupDeleted") {
			t.Fatalf("non-admin got a deleted group's event: %s", body)
		}
	}
}

// remote=1 keeps only events that offer remote attendance (M1.8
// slice 2), for signed-in and signed-out viewers alike, and never
// widens visibility: a hidden group's remote event stays hidden.
func TestCalendarRemoteFilter(t *testing.T) {
	app := testApp(t)
	useCalendarTestClock(app, time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC))
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")

	open := createGroupAs(t, admin, "Open Choir", models.VisibilityPublic)
	scheduleGroupEvent(t, admin, open, "In Person Only")
	w := admin.do("POST", "/api/v1/groups/"+itoa(open)+"/events", map[string]any{
		"title": "Hybrid Night", "startsLocal": "2026-12-01T19:00", "durationMinutes": 60,
		"tzid": "UTC", "remoteAvailable": true,
	}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create remote event: %d %s", w.Code, w.Body.String())
	}
	var hybrid struct {
		Event eventJSON `json:"event"`
	}
	decodeJSON(t, w, &hybrid)
	closed := scheduleGroupEvent(t, admin, open, "Remote Closed This Date")
	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d", closed.ID), map[string]any{"remoteAvailable": true}, http.StatusOK)
	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d/occurrences/%s", closed.ID, closed.StartsLocal[:10]), map[string]any{"remoteCapacityOverride": 0}, http.StatusOK)
	secret := createGroupAs(t, admin, "Secret Remote", models.VisibilityInviteOnly)
	w = admin.do("POST", "/api/v1/groups/"+itoa(secret)+"/events", map[string]any{
		"title": "Secret Hybrid", "startsLocal": "2026-12-01T19:00", "durationMinutes": 60,
		"tzid": "UTC", "remoteAvailable": true,
	}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create secret remote event: %d %s", w.Code, w.Body.String())
	}

	const window = "from=2026-11-30&to=2026-12-02&tz=UTC"
	for _, who := range []*apiClient{admin, newAPIClient(t, app)} {
		page, raw := getCalendar(t, who, window+"&remote=1", http.StatusOK)
		if hasTitle(page, "In Person Only") {
			t.Fatalf("remote=1 kept an in-person-only event: %v", calendarTitles(page))
		}
		if !hasTitle(page, "Hybrid Night") {
			t.Fatalf("remote=1 dropped the remote event: %v", calendarTitles(page))
		}
		if hasTitle(page, "Remote Closed This Date") {
			t.Fatalf("remote filter kept a date with zero remote seats: %s", raw)
		}
		for _, row := range page.Occurrences {
			if !row.RemoteAvailable {
				t.Fatalf("remote filter row missing availability: %+v", row)
			}
		}
		if who != admin && (hasTitle(page, "Secret Hybrid") || strings.Contains(raw, "Secret Remote")) {
			t.Fatalf("signed-out remote=1 saw an invite-only event: %s", raw)
		}
	}
	// Without the filter both public events are there.
	page, _ := getCalendar(t, newAPIClient(t, app), window, http.StatusOK)
	if !hasTitle(page, "In Person Only") || !hasTitle(page, "Hybrid Night") {
		t.Fatalf("unfiltered calendar: %v", calendarTitles(page))
	}
	for _, row := range page.Occurrences {
		if row.RemoteAvailable != (row.EventID == hybrid.Event.ID) {
			t.Fatalf("unfiltered calendar remote availability: %+v", row)
		}
	}
}

func TestCalendarChildcareFilter(t *testing.T) {
	app := testApp(t)
	useCalendarTestClock(app, time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC))
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	open := createGroupAs(t, admin, "Open Nursery", models.VisibilityPublic)
	plain := scheduleGroupEvent(t, admin, open, "No Childcare")
	offered := scheduleGroupEvent(t, admin, open, "Family Night")
	apiDo(t, admin, "PUT", "/api/v1/events/"+itoa(offered.ID), map[string]any{"childcareAvailable": true}, http.StatusOK)
	secret := createGroupAs(t, admin, "Secret Nursery", models.VisibilityInviteOnly)
	hidden := scheduleGroupEvent(t, admin, secret, "Hidden Family Night")
	apiDo(t, admin, "PUT", "/api/v1/events/"+itoa(hidden.ID), map[string]any{"childcareAvailable": true}, http.StatusOK)
	const window = "from=2026-11-30&to=2026-12-02&tz=UTC"
	for _, who := range []*apiClient{admin, newAPIClient(t, app)} {
		page, raw := getCalendar(t, who, window+"&childcare=1", http.StatusOK)
		if hasTitle(page, plain.Title) || !hasTitle(page, offered.Title) {
			t.Fatalf("childcare filter: %s", raw)
		}
		for _, row := range page.Occurrences {
			if !row.ChildcareAvailable {
				t.Fatalf("missing childcare flag: %+v", row)
			}
		}
		if who != admin && (hasTitle(page, hidden.Title) || strings.Contains(raw, "Secret Nursery")) {
			t.Fatalf("filter widened visibility: %s", raw)
		}
	}
	page, _ := getCalendar(t, newAPIClient(t, app), window, http.StatusOK)
	if !hasTitle(page, plain.Title) || !hasTitle(page, offered.Title) {
		t.Fatalf("unfiltered: %v", calendarTitles(page))
	}
	for _, row := range page.Occurrences {
		if row.ChildcareAvailable != (row.EventID == offered.ID) {
			t.Fatalf("wrong childcare flag: %+v", row)
		}
	}
}

func TestHouseholdOnlyCalendarDoesNotOverwriteOwnStatus(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "HouseholdCalendar")
	eventID, date := seedCappedEvent(t, app, admin, gid, nil, true)
	_, c, spouse, _ := householdFamily(t, app, admin, "calendarholder")
	batchAnswer(t, c, eventID, date, map[string]any{"people": []map[string]any{{"personId": spouse.ID, "status": "going"}}}, http.StatusOK)
	page, raw := getCalendar(t, c, "from="+date+"&to="+date+"&tz=UTC&mine=1", http.StatusOK)
	if len(page.Occurrences) != 1 || page.Occurrences[0].EventID != eventID || page.Occurrences[0].MyStatus != nil || !page.Occurrences[0].OwnScope {
		t.Fatalf("household-only calendar %s", raw)
	}
	for _, secret := range []string{"Mary", "householdPerson", "careNotes", "birthDate"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("calendar leak %s", raw)
		}
	}
}

func TestCalendarScopedInvitationIsOneSlimGoingDate(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	seedReadInvitation(t, f.app, f.issuer, f.other, f.event, f.dates[2])
	window := "from=" + f.dates[0].OriginalDate + "&to=" + f.dates[2].OriginalDate + "&tz=UTC"
	for _, mine := range []string{"", "&mine=1"} {
		page, raw := getCalendar(t, f.client, window+mine, 200)
		if len(page.Occurrences) != 1 {
			t.Fatalf("calendar broadened: %s", raw)
		}
		row := page.Occurrences[0]
		if row.Date != f.dates[1].OriginalDate || row.DateURL != occurrenceDatePath(f.event.ID, row.Date) || row.MyStatus == nil || *row.MyStatus != "going" || row.Title != "Invited date title" || row.Location != "Invited date place" {
			t.Fatalf("wrong invited row: %+v", row)
		}
		for _, forbidden := range []string{"Hidden group", "Hidden series", `"groupId"`, `"groupName"`, `"hostedBy"`, `"attendees"`, `"registrations"`, `"capacity"`, `"seatsUsed"`, `"recurrenceRule"`, f.issuer.Email} {
			if strings.Contains(raw, forbidden) {
				t.Fatalf("calendar leaked %q: %s", forbidden, raw)
			}
		}
	}
	other, _ := getCalendar(t, f.stranger, window+"&mine=1", 200)
	if len(other.Occurrences) != 1 || other.Occurrences[0].Date != f.dates[2].OriginalDate {
		t.Fatalf("distinct recipient scope: %+v", other)
	}
	getCalendar(t, f.client, window+"&group="+itoa(f.group), 404)
	getCalendar(t, f.client, window+"&remote=1", 200)
	// A visible group filter cannot be widened by an unrelated invitation.
	public := createFeedGroup(t, f.admin, "Other visible group", models.VisibilityPublic, "")
	page, _ := getCalendar(t, f.client, window+"&group="+itoa(public), 200)
	if len(page.Occurrences) != 0 {
		t.Fatal("invitation escaped group filter", page)
	}
	// There is no implicit hidden-group location fallback.
	if err := f.app.DB.Model(&f.dates[1]).Update("location_override", nil).Error; err != nil {
		t.Fatal(err)
	}
	page, _ = getCalendar(t, f.client, window, 200)
	if len(page.Occurrences) != 1 || page.Occurrences[0].Location != "" {
		t.Fatal("hidden fallback", page)
	}
}

func TestCalendarOwnScopeStatusAndThirtyDayWindow(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	now := time.Now().UTC()
	near := time.Date(now.Year(), now.Month(), now.Day(), 19, 0, 0, 0, time.UTC).AddDate(0, 0, 7)
	far := near.AddDate(0, 0, 24) // 31 days from today.

	mineID := createFeedGroup(t, admin, "Scope members", models.VisibilityPublic, "")
	var mineGroup models.Group
	if err := app.DB.First(&mineGroup, mineID).Error; err != nil {
		t.Fatal(err)
	}
	_, member := mkUser(t, app, &mineGroup, "scopeviewer")
	createFeedEvent(t, admin, mineID, "Own far date", far, "")

	otherID := createFeedGroup(t, admin, "Scope discovery", models.VisibilityPublic, "")
	createFeedEvent(t, admin, otherID, "Near public unanswered", near, "")
	createFeedEvent(t, admin, otherID, "Far public unanswered", far, "")
	maybe := createFeedEvent(t, admin, otherID, "Far public maybe", far.Add(time.Hour), "")
	declined := createFeedEvent(t, admin, otherID, "Near public declined", near.Add(time.Hour), "")
	apiDo(t, member, "PUT", fmt.Sprintf("/api/v1/events/%d/registrations", maybe.ID), map[string]any{"status": "maybe", "date": feedDate(far)}, http.StatusOK)
	apiDo(t, member, "PUT", fmt.Sprintf("/api/v1/events/%d/registrations", declined.ID), map[string]any{"status": "declined", "date": feedDate(near)}, http.StatusOK)

	church := func(title string, start time.Time) {
		w := admin.do("POST", "/api/v1/events", map[string]any{"title": title, "startsLocal": start.Format("2006-01-02T15:04"), "durationMinutes": 60, "tzid": "UTC"}, true)
		if w.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", title, w.Code, w.Body.String())
		}
	}
	church("Near church-wide", near.Add(2*time.Hour))
	church("Far church-wide", far.Add(2*time.Hour))

	query := "from=" + now.Format("2006-01-02") + "&to=" + far.AddDate(0, 0, 1).Format("2006-01-02") + "&tz=UTC"
	all, raw := getCalendar(t, member, query, http.StatusOK)
	for _, title := range []string{"Own far date", "Near public unanswered", "Far public maybe", "Near public declined", "Near church-wide"} {
		if !hasTitle(all, title) {
			t.Fatalf("Everything missing %q: %s", title, raw)
		}
	}
	for _, title := range []string{"Far public unanswered", "Far church-wide"} {
		if hasTitle(all, title) {
			t.Fatalf("Everything kept out-of-window %q: %s", title, raw)
		}
	}
	mine, raw := getCalendar(t, member, query+"&mine=1", http.StatusOK)
	for _, title := range []string{"Own far date", "Far public maybe", "Near church-wide"} {
		if !hasTitle(mine, title) {
			t.Fatalf("Just mine missing %q: %s", title, raw)
		}
	}
	for _, title := range []string{"Near public unanswered", "Far public unanswered", "Near public declined", "Far church-wide"} {
		if hasTitle(mine, title) {
			t.Fatalf("Just mine kept %q: %s", title, raw)
		}
	}
	for _, row := range all.Occurrences {
		if row.Title == "Near public unanswered" && row.OwnScope {
			t.Fatalf("outsider discovery row marked own: %+v", row)
		}
		if row.Title == "Own far date" && (!row.OwnScope || row.MyStatus != nil) {
			t.Fatalf("own unanswered row: %+v", row)
		}
		if row.Title == "Far public maybe" && (!row.OwnScope || row.MyStatus == nil || *row.MyStatus != models.RegMaybe) {
			t.Fatalf("maybe row: %+v", row)
		}
	}
}

func TestCalendarScopedInvitationLoadingIsBatched(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	window := "from=" + f.dates[0].OriginalDate + "&to=" + f.dates[2].OriginalDate + "&tz=UTC"
	counter := countDBReads(t, f.app.DB)
	getCalendar(t, f.client, window, 200)
	counter.Stop()
	one := counter.Reads()
	seedReadInvitation(t, f.app, f.issuer, f.recipient, f.event, f.dates[0])
	seedReadInvitation(t, f.app, f.issuer, f.recipient, f.event, f.dates[2])
	counter = countDBReads(t, f.app.DB)
	page, _ := getCalendar(t, f.client, window, 200)
	counter.Stop()
	three := counter.Reads()
	if len(page.Occurrences) != 3 || three != one {
		t.Fatalf("invitation reads grew per date: one=%d three=%d dates=%d", one, three, len(page.Occurrences))
	}
	t.Logf("calendar reads with 1 or 3 scoped dates: %d", one)
}

func TestCalendarHiddenGroupSiteNameReadBudget(t *testing.T) {
	app := testApp(t)
	creator := newTestUser(t, app, "creator", "pw", models.RoleAdmin)
	g := newTestGroup(t, app, creator, "Hidden", models.GroupActive)
	if err := app.DB.Model(g).Update("visibility", models.VisibilityInviteOnly).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Create(&models.Setting{Key: "site_name", Value: "Our Church"}).Error; err != nil {
		t.Fatal(err)
	}
	client := newAPIClient(t, app)
	client.do("GET", "/api/v1/calendar", nil, false) // Warm the setup-complete cache.
	settings := countSiteNameReads(t, app.DB)
	start := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Minute)
	var baseline int64
	for i := 1; i <= 30; i++ {
		ev, _ := seedEvent(t, app, g, creator.ID, start)
		if err := app.DB.Model(&ev).Update("visibility", models.VisibilityPublic).Error; err != nil {
			t.Fatal(err)
		}
		if i != 3 && i != 30 {
			continue
		}
		*settings = 0
		counter := countDBReads(t, app.DB)
		w := client.do("GET", "/api/v1/calendar?tz=UTC&from="+start.Format("2006-01-02")+"&to="+start.Format("2006-01-02"), nil, false)
		reads := counter.Reads()
		counter.Stop()
		if w.Code != http.StatusOK {
			t.Fatalf("calendar: %d %s", w.Code, w.Body.String())
		}
		var body struct {
			Occurrences []calendarOccurrenceJSON `json:"occurrences"`
		}
		decodeJSON(t, w, &body)
		if len(body.Occurrences) != i {
			t.Fatalf("got %d dates, want %d", len(body.Occurrences), i)
		}
		for _, row := range body.Occurrences {
			if row.HostedBy != "Our Church" || row.GroupID != nil || row.GroupName != "" {
				t.Fatalf("hidden group host: %+v", row)
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
