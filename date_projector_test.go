package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"fold/models"

	"gorm.io/gorm"
)

// The date projector goldens (E1 item 4): every surface that turns a date into
// output (calendar rows, event pages, group lists, the open-events list, the
// date page, its one-date .ics and the three calendar feeds) is captured for
// every kind of viewer over one fixture, and must stay byte for byte the same
// when the code that builds them changes. Run with UPDATE_GOLDEN=1 to rewrite
// testdata/date_projector/*.golden after an intended change.

type projectorFixture struct {
	app                                         *App
	admin, leader, member, invited, stranger    *models.User
	host, teen                                  *models.User
	open, hidden                                models.Group
	study, opened, privateEv, potluck, churchPr models.Event
	byName                                      map[string]*apiClient
	base                                        time.Time
}

func projectorEvent(t *testing.T, f *projectorFixture, g *models.Group, title, vis string, day int, dates int, mod func(*models.Event)) (models.Event, []models.EventOccurrence) {
	t.Helper()
	start := f.base.AddDate(0, 0, day).Add(19 * time.Hour)
	ev := models.Event{Title: title, Description: title + " description", Visibility: vis, StartsLocal: start.Format("2006-01-02T15:04"),
		DurationMinutes: 60, TZID: "UTC", CreatedBy: f.admin.ID, ReminderEmailMinutes: 1440, VideoProvider: models.VideoProviderNone, VideoReveal: "registered_only"}
	if g != nil {
		ev.GroupID = &g.ID
	}
	if mod != nil {
		mod(&ev)
	}
	if err := f.app.DB.Create(&ev).Error; err != nil {
		t.Fatal(err)
	}
	var occs []models.EventOccurrence
	for i := 0; i < dates; i++ {
		s := start.AddDate(0, 0, 7*i)
		occ := models.EventOccurrence{EventID: ev.ID, OriginalDate: calDate(s.Format("2006-01-02")), StartsAt: s, EndsAt: s.Add(time.Hour), Status: models.OccurrenceScheduled}
		if err := f.app.DB.Create(&occ).Error; err != nil {
			t.Fatal(err)
		}
		occs = append(occs, occ)
	}
	return ev, occs
}

func newProjectorFixture(t *testing.T) *projectorFixture {
	t.Helper()
	app := testApp(t)
	now := time.Now().UTC()
	f := &projectorFixture{app: app, base: time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC), byName: map[string]*apiClient{}}
	f.admin = newTestUser(t, app, "Ada Admin", "password1234", models.RoleAdmin)
	f.leader = newTestUser(t, app, "Lee Leader", "password1234", models.RoleMember)
	f.member = newTestUser(t, app, "Mia Member", "password1234", models.RoleMember)
	f.invited = newTestUser(t, app, "Ivy Invited", "password1234", models.RoleMember)
	f.stranger = newTestUser(t, app, "Sam Stranger", "password1234", models.RoleMember)
	f.host = newTestUser(t, app, "Hal Host", "password1234", models.RoleMember)
	f.teen = newTestUser(t, app, "Tess Teen", "password1234", models.RoleMember)
	if err := app.DB.Model(f.teen).Update("account_type", models.AccountTeen).Error; err != nil {
		t.Fatal(err)
	}
	f.open = activeGroup(t, app, f.leader, "Open Group", models.VisibilityPublic, "")
	f.hidden = activeGroup(t, app, f.leader, "Hidden Group", models.VisibilityInviteOnly, "")
	if err := app.DB.Model(&f.open).Update("location", "Open Hall").Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Model(&f.hidden).Update("location", "Secret Room").Error; err != nil {
		t.Fatal(err)
	}
	f.open.Location, f.hidden.Location = "Open Hall", "Secret Room"
	addMember(t, app, f.member, &f.open, models.MembershipMember)
	addMember(t, app, f.member, &f.hidden, models.MembershipMember)
	addMember(t, app, f.teen, &f.open, models.MembershipMember)

	capacity := 2
	var occs, opened, priv, potluck, churchPriv []models.EventOccurrence
	f.study, occs = projectorEvent(t, f, &f.open, "Open Study", models.VisibilityPublic, 7, 4, func(e *models.Event) {
		e.Capacity, e.WaitlistEnabled, e.GuestsAllowed = &capacity, true, 2
		e.ChildcareAvailable, e.ChildcareAges = true, "0-5"
		e.VideoProvider, e.VideoURL = models.VideoProviderLink, "https://example.com/study-call"
		e.RRule = "FREQ=WEEKLY;COUNT=4"
	})
	// d2 cancelled by a person; d3 invite-only override; d4 renamed and moved.
	gormMust(t, app.DB.Model(&occs[1]).Updates(map[string]any{"status": models.OccurrenceCancelled, "cancel_source": models.CancelSourceManual}))
	gormMust(t, app.DB.Model(&occs[2]).Update("visibility_override", models.VisibilityInviteOnly))
	gormMust(t, app.DB.Model(&occs[3]).Updates(map[string]any{"title_override": "Renamed Study", "location_override": "Back Room", "duration_override": 90}))
	f.opened, opened = projectorEvent(t, f, &f.hidden, "Opened Event", models.VisibilityPublic, 8, 2, func(e *models.Event) {
		e.LeaderOpened = true
		e.VideoProvider, e.VideoURL, e.VideoReveal = models.VideoProviderLink, "https://example.com/opened-call", "always"
	})
	f.privateEv, priv = projectorEvent(t, f, &f.open, "Private Event", models.VisibilityPrivate, 9, 2, func(e *models.Event) {
		e.Location = "Private Porch"
		e.VideoProvider, e.VideoURL = models.VideoProviderLink, "https://example.com/private-call"
		e.RemoteAvailable = true
	})
	// The second date of the private event is public: a series-hidden date.
	gormMust(t, app.DB.Model(&priv[1]).Update("visibility_override", models.VisibilityPublic))
	f.potluck, potluck = projectorEvent(t, f, nil, "Church Potluck", models.VisibilityPublic, 10, 1, func(e *models.Event) {
		e.Location = "Fellowship Hall"
		e.RemoteAvailable = true
		e.VideoProvider, e.VideoURL = models.VideoProviderLink, "https://example.com/potluck-call"
	})
	f.churchPr, churchPriv = projectorEvent(t, f, nil, "Staff Retreat", models.VisibilityPrivate, 11, 1, nil)
	for _, ev := range []models.Event{f.potluck, f.churchPr} {
		gormMust(t, app.DB.Create(&models.EventHost{EventID: ev.ID, UserID: f.host.ID, AddedBy: f.admin.ID}))
	}
	_ = opened
	_ = churchPriv

	reg := func(u *models.User, occ models.EventOccurrence, status string, guests int) {
		r := models.Registration{UserID: &u.ID, OccurrenceID: occ.ID, Status: status, GuestCount: guests, Source: models.RegSourceWeb, AttendanceMode: models.AttendanceInPerson}
		gormMust(t, app.DB.Create(&r))
	}
	reg(f.member, occs[0], models.RegGoing, 1)
	reg(f.invited, occs[0], models.RegMaybe, 0)
	reg(f.teen, occs[0], models.RegWaitlisted, 0)
	reg(f.stranger, occs[0], models.RegDeclined, 0)
	reg(f.stranger, priv[1], models.RegGoing, 0)
	reg(f.host, potluck[0], models.RegGoing, 0)
	reg(f.member, potluck[0], models.RegMaybe, 0)
	seedReadInvitation(t, app, f.admin, f.invited, eventJSON{ID: f.study.ID}, occs[2])

	// Freeze the change times (they feed DTSTAMP): the event a day before today at
	// its id in hours, each date at 90 minutes times its id, so either may be later.
	gormMust(t, app.DB.Exec("UPDATE events SET updated_at = ?::timestamptz + id * interval '1 hour'", f.base.Add(-24*time.Hour)))
	gormMust(t, app.DB.Exec("UPDATE event_occurrences SET updated_at = ?::timestamptz + id * interval '90 minutes'", f.base.Add(-24*time.Hour)))

	for _, u := range []*models.User{f.admin, f.leader, f.member, f.invited, f.stranger, f.host, f.teen} {
		f.byName[u.Name] = loginAPI(t, app, u.Name, "password1234")
	}
	f.byName["signed out"] = anonClient(t, app, "")
	return f
}

func gormMust(t *testing.T, db *gorm.DB) {
	t.Helper()
	if db.Error != nil {
		t.Fatal(db.Error)
	}
}

var projectorViewers = []string{"signed out", "Ada Admin", "Lee Leader", "Mia Member", "Ivy Invited", "Sam Stranger", "Hal Host", "Tess Teen"}

var (
	projectorDateRe  = regexp.MustCompile(`\d{4}-\d{2}-\d{2}|\d{8}`)
	projectorStampRe = regexp.MustCompile(`(?m)^(LAST-MODIFIED|CREATED)[^\r\n]*\r?\n`)
)

// normalize replaces the fixture's moving dates with offsets from today, so
// the goldens do not rot.
func (f *projectorFixture) normalize(s string) string {
	s = projectorStampRe.ReplaceAllString(s, "")
	return projectorDateRe.ReplaceAllStringFunc(s, func(m string) string {
		layout := "2006-01-02"
		if len(m) == 8 {
			layout = "20060102"
		}
		d, err := time.Parse(layout, m)
		if err != nil {
			return m
		}
		days := int(d.Sub(f.base).Hours() / 24)
		// A date far from today is a constant of the output (the VTIMEZONE
		// onset 19700101), not a moving fixture date; rewriting it would
		// change the golden every midnight.
		if days < -3650 || days > 3650 {
			return m
		}
		return fmt.Sprintf("D%+d", days)
	})
}

func (f *projectorFixture) record(sb *strings.Builder, viewer, path string) {
	c := f.byName[viewer]
	w := c.do("GET", path, nil, false)
	sb.WriteString(fmt.Sprintf("### %s GET %s -> %d\n", viewer, f.normalize(path), w.Code))
	sb.WriteString(f.normalize(prettyBody(w)))
	sb.WriteString("\n")
}

func prettyBody(w *httptest.ResponseRecorder) string {
	body := w.Body.Bytes()
	if strings.Contains(w.Header().Get("Content-Type"), "json") {
		var out bytes.Buffer
		if err := json.Indent(&out, body, "", "  "); err == nil {
			return out.String()
		}
	}
	return strings.ReplaceAll(string(body), "\r\n", "\n")
}

func (f *projectorFixture) dateStrings(t *testing.T, ev models.Event) []string {
	var occs []models.EventOccurrence
	gormMust(t, f.app.DB.Where("event_id = ?", ev.ID).Order("starts_at").Find(&occs))
	var out []string
	for _, o := range occs {
		out = append(out, models.FormatCalendarDate(o.OriginalDate))
	}
	return out
}

func checkProjectorGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "date_projector", name+".golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (UPDATE_GOLDEN=1 writes it)", err)
	}
	if string(want) != got {
		_ = os.WriteFile(path+".got", []byte(got), 0o644)
		t.Fatalf("%s differs from its golden; see %s.got", name, path)
	}
}

func TestDateProjectorGoldens(t *testing.T) {
	f := newProjectorFixture(t)
	from, to := f.base.Format("2006-01-02"), f.base.AddDate(0, 0, 60).Format("2006-01-02")
	events := []models.Event{f.study, f.opened, f.privateEv, f.potluck, f.churchPr}

	t.Run("calendar", func(t *testing.T) {
		var sb strings.Builder
		for _, v := range projectorViewers {
			base := "/api/v1/calendar?from=" + from + "&to=" + to
			f.record(&sb, v, base)
			f.record(&sb, v, base+"&remote=1")
			f.record(&sb, v, base+"&church=1")
			if v != "signed out" {
				f.record(&sb, v, base+"&mine=1")
			}
		}
		checkProjectorGolden(t, "calendar", sb.String())
	})
	t.Run("event_pages", func(t *testing.T) {
		var sb strings.Builder
		for _, v := range projectorViewers {
			for _, ev := range events {
				f.record(&sb, v, fmt.Sprintf("/api/v1/events/%d", ev.ID))
			}
			f.record(&sb, v, "/api/v1/events")
			for _, g := range []models.Group{f.open, f.hidden} {
				f.record(&sb, v, fmt.Sprintf("/api/v1/groups/%d/events?from=%s&to=%s", g.ID, from, to))
			}
		}
		checkProjectorGolden(t, "event_pages", sb.String())
	})
	t.Run("date_pages", func(t *testing.T) {
		var sb strings.Builder
		for _, v := range projectorViewers {
			for _, ev := range events {
				for _, d := range f.dateStrings(t, ev) {
					f.record(&sb, v, fmt.Sprintf("/api/v1/events/%d/dates/%s", ev.ID, d))
				}
			}
		}
		checkProjectorGolden(t, "date_pages", sb.String())
	})
	t.Run("date_ics", func(t *testing.T) {
		var sb strings.Builder
		for _, v := range projectorViewers {
			for _, ev := range events {
				for _, d := range f.dateStrings(t, ev) {
					f.record(&sb, v, fmt.Sprintf("/api/v1/events/%d/dates/%s/calendar.ics", ev.ID, d))
				}
			}
		}
		checkProjectorGolden(t, "date_ics", sb.String())
	})
	t.Run("feeds", func(t *testing.T) {
		var sb strings.Builder
		anon := f.byName["signed out"]
		for _, p := range []string{"/cal/public.ics", fmt.Sprintf("/cal/groups/%d.ics", f.open.ID), fmt.Sprintf("/cal/groups/%d.ics", f.hidden.ID)} {
			f.record(&sb, "signed out", p)
		}
		for _, v := range projectorViewers[1:] {
			var u models.User
			gormMust(t, f.app.DB.Where("name = ?", v).First(&u))
			for _, scope := range []string{models.CalendarFeedMine, models.CalendarFeedAll} {
				raw := "projector-" + scope + "-" + fmt.Sprint(u.ID)
				tok := models.CalendarToken{UserID: u.ID, TokenHash: hashToken(raw), Scope: scope}
				if err := f.app.DB.Where("user_id = ?", u.ID).Delete(&models.CalendarToken{}).Error; err != nil {
					t.Fatal(err)
				}
				if err := f.app.DB.Create(&tok).Error; err != nil {
					t.Fatal(err)
				}
				w := anon.do("GET", "/cal/u/"+raw+".ics", nil, false)
				sb.WriteString(fmt.Sprintf("### %s (%s) GET /cal/u/<token>.ics -> %d\n%s\n", v, scope, w.Code, f.normalize(prettyBody(w))))
			}
		}
		checkProjectorGolden(t, "feeds", sb.String())
	})
	_ = http.StatusOK
}
