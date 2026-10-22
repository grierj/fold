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

	// The goer's own row says they are going; the decliner's does not
	// say anything.
	page, _ = getCalendar(t, goer, window, http.StatusOK)
	for _, o := range page.Occurrences {
		if o.Title == "Rehearsal" && (o.MyStatus == nil || *o.MyStatus != "going") {
			t.Fatalf("goer's row should say going: %+v", o)
		}
	}
	page, _ = getCalendar(t, decliner, window, http.StatusOK)
	for _, o := range page.Occurrences {
		if o.Title == "Rehearsal" && o.MyStatus != nil {
			t.Fatalf("decliner's row should have no status: %+v", o)
		}
	}
}

// "Just mine" is per occurrence: my groups' events, events I host, and
// the specific dates I answered going or maybe — not the whole series.
func TestCalendarMineNarrowsPerOccurrence(t *testing.T) {
	app := testApp(t)
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
	if got := calendarTitles(all); len(got) != 5 {
		t.Fatalf("everything view: want 5 rows, got %v", got)
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

	// mine is ignored when signed out: the public list is unchanged.
	guest := newAPIClient(t, app)
	guestPage, _ := getCalendar(t, guest, window+"&mine=1", http.StatusOK)
	if len(guestPage.Occurrences) != 5 {
		t.Fatalf("signed-out mine=1 should be ignored: %v", calendarTitles(guestPage))
	}
}

func TestCalendarWindowGroupFilterAndZones(t *testing.T) {
	app := testApp(t)
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
