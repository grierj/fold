package main

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"fold/internal/policy"
	"fold/models"
)

func seedSeriesEvent(t *testing.T, app *App, g *models.Group, createdBy uint, capacity *int, waitlist bool, count int) (models.Event, []models.EventOccurrence) {
	t.Helper()
	start := time.Now().UTC().AddDate(0, 0, 14).Truncate(24 * time.Hour).Add(18 * time.Hour)
	gid := g.ID
	ev := models.Event{GroupID: &gid, Title: "Series", Visibility: g.Visibility, StartsLocal: start.Format("2006-01-02T15:04"),
		DurationMinutes: 60, TZID: "UTC", RRule: "FREQ=WEEKLY", Location: "", CreatedBy: createdBy,
		Capacity: capacity, WaitlistEnabled: waitlist, GuestsAllowed: 2}
	if err := app.DB.Create(&ev).Error; err != nil {
		t.Fatal(err)
	}
	occs := make([]models.EventOccurrence, count)
	for i := range occs {
		at := start.AddDate(0, 0, i*7)
		occs[i] = models.EventOccurrence{EventID: ev.ID, OriginalDate: at.Format("2006-01-02"), StartsAt: at, EndsAt: at.Add(time.Hour), Status: models.OccurrenceScheduled}
		if err := app.DB.Create(&occs[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	return ev, occs
}

func postSeriesAnswer(c *apiClient, eventID uint, body map[string]any) *seriesRegistrationResponse {
	w := c.do("POST", fmt.Sprintf("/api/v1/events/%d/series-registrations", eventID), body, true)
	if w.Code != http.StatusOK {
		return nil
	}
	var out seriesRegistrationResponse
	decodeJSON(c.t, w, &out)
	return &out
}

func TestSeriesRegistrationThisAndFollowingPreservesEarlierAndRemoved(t *testing.T) {
	app := testApp(t)
	leader := newTestUser(t, app, "series-leader", "password1234", models.RoleMember)
	group := activeGroup(t, app, leader, "Series group", models.VisibilityPublic, "")
	actor := newTestUser(t, app, "series-actor", "password1234", models.RoleMember)
	addMember(t, app, actor, &group, models.MembershipMember)
	ev, occs := seedSeriesEvent(t, app, &group, leader.ID, nil, false, 4)
	uid := actor.ID
	earlier := models.Registration{OccurrenceID: occs[0].ID, UserID: &uid, Status: models.RegMaybe, AttendanceMode: models.AttendanceInPerson, Source: models.RegSourceWeb}
	removedBy := leader.ID
	removed := models.Registration{OccurrenceID: occs[2].ID, UserID: &uid, Status: models.RegRemoved, AttendanceMode: models.AttendanceInPerson, Source: models.RegSourceWeb, RemovedBy: &removedBy}
	if err := app.DB.Create(&earlier).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Create(&removed).Error; err != nil {
		t.Fatal(err)
	}

	client := loginAPI(t, app, "series-actor", "password1234")
	out := postSeriesAnswer(client, ev.ID, map[string]any{"date": occs[1].OriginalDate, "scope": seriesScopeThisAndFollowing, "status": models.RegGoing})
	if out == nil {
		t.Fatal("series answer failed")
	}
	if len(out.Results) != 2 || len(out.Refusals) != 1 || out.Refusals[0].Code != "registration_removed" {
		t.Fatalf("results=%+v refusals=%+v", out.Results, out.Refusals)
	}
	if got := regStatus(t, app, actor.ID, occs[0].ID); got != models.RegMaybe {
		t.Fatalf("earlier status=%q", got)
	}
	if got := regStatus(t, app, actor.ID, occs[2].ID); got != models.RegRemoved {
		t.Fatalf("removed status=%q", got)
	}
	for _, i := range []int{1, 3} {
		if got := regStatus(t, app, actor.ID, occs[i].ID); got != models.RegGoing {
			t.Fatalf("date %d status=%q", i, got)
		}
	}
}

func TestSeriesRegistrationMixedCapacityAndLaterExtension(t *testing.T) {
	app := testApp(t)
	leader := newTestUser(t, app, "capacity-leader", "password1234", models.RoleMember)
	group := activeGroup(t, app, leader, "Capacity group", models.VisibilityPublic, "")
	actor := newTestUser(t, app, "capacity-actor", "password1234", models.RoleMember)
	filler := newTestUser(t, app, "capacity-filler", "password1234", models.RoleMember)
	addMember(t, app, actor, &group, models.MembershipMember)
	addMember(t, app, filler, &group, models.MembershipMember)
	ev, occs := seedSeriesEvent(t, app, &group, leader.ID, intPtr(1), true, 3)
	fillerID := filler.ID
	if err := app.DB.Create(&models.Registration{OccurrenceID: occs[1].ID, UserID: &fillerID, Status: models.RegGoing, AttendanceMode: models.AttendanceInPerson, Source: models.RegSourceWeb}).Error; err != nil {
		t.Fatal(err)
	}
	client := loginAPI(t, app, "capacity-actor", "password1234")
	out := postSeriesAnswer(client, ev.ID, map[string]any{"scope": seriesScopeAllFuture, "status": models.RegGoing})
	if out == nil || len(out.Results) != 3 {
		t.Fatalf("out=%+v", out)
	}
	if got := regStatus(t, app, actor.ID, occs[1].ID); got != models.RegWaitlisted {
		t.Fatalf("full date=%q", got)
	}
	if out.Results[1].After.WaitlistPosition == nil || *out.Results[1].After.WaitlistPosition != 1 {
		t.Fatalf("waitlist result=%+v", out.Results[1])
	}

	laterAt := occs[2].StartsAt.AddDate(0, 0, 7)
	later := models.EventOccurrence{EventID: ev.ID, OriginalDate: laterAt.Format("2006-01-02"), StartsAt: laterAt, EndsAt: laterAt.Add(time.Hour), Status: models.OccurrenceScheduled}
	if err := app.DB.Create(&later).Error; err != nil {
		t.Fatal(err)
	}
	var n int64
	if err := app.DB.Model(&models.Registration{}).Where("occurrence_id = ? AND user_id = ?", later.ID, actor.ID).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("later extension got %d registrations", n)
	}
}

func TestSeriesRegistrationRequiresSeriesAuthorityAndIgnoresManagerBlock(t *testing.T) {
	app := testApp(t)
	leader := newTestUser(t, app, "hidden-leader", "password1234", models.RoleMember)
	group := activeGroup(t, app, leader, "Hidden group", models.VisibilityInviteOnly, "")
	actor := newTestUser(t, app, "hidden-actor", "password1234", models.RoleMember)
	ev, occs := seedSeriesEvent(t, app, &group, leader.ID, nil, false, 2)
	uid := actor.ID
	reg := models.Registration{OccurrenceID: occs[0].ID, UserID: &uid, Status: models.RegGoing, AttendanceMode: models.AttendanceInPerson, Source: models.RegSourceAdmin}
	if err := app.DB.Create(&reg).Error; err != nil {
		t.Fatal(err)
	}
	access := models.OccurrenceAccess{SubjectUserID: actor.ID, OccurrenceID: occs[0].ID, EventID: ev.ID, OrganizerType: policy.ContactGroup,
		OrganizerID: group.ID, IssuerID: leader.ID, RegistrationID: reg.ID, Status: policy.OccurrenceAccessActive, GrantedAt: time.Now().UTC()}
	if err := app.DB.Create(&access).Error; err != nil {
		t.Fatal(err)
	}
	client := loginAPI(t, app, "hidden-actor", "password1234")
	w := client.do("POST", fmt.Sprintf("/api/v1/events/%d/series-registrations", ev.ID), map[string]any{"scope": seriesScopeAllFuture, "status": models.RegMaybe}, true)
	if w.Code != http.StatusNotFound {
		t.Fatalf("exact-date series answer=%d %s", w.Code, w.Body.String())
	}
	var n int64
	app.DB.Model(&models.Registration{}).Where("user_id = ?", actor.ID).Count(&n)
	if n != 1 {
		t.Fatalf("hidden series changed rows: %d", n)
	}

	addMember(t, app, actor, &group, models.MembershipMember)
	if err := app.DB.Create(&models.ManagerAddBlock{SubjectUserID: actor.ID, OrganizerType: policy.ContactGroup, OrganizerID: group.ID}).Error; err != nil {
		t.Fatal(err)
	}
	out := postSeriesAnswer(client, ev.ID, map[string]any{"scope": seriesScopeAllFuture, "status": models.RegMaybe})
	if out == nil || len(out.Results) != 2 {
		t.Fatalf("manager block affected own answer: %+v", out)
	}
}

func TestSeriesRegistrationUsesEffectiveDateVisibility(t *testing.T) {
	app := testApp(t)
	leader := newTestUser(t, app, "visibility-leader", "password1234", models.RoleMember)
	group := activeGroup(t, app, leader, "Visible group", models.VisibilityPublic, "")
	actor := newTestUser(t, app, "visibility-actor", "password1234", models.RoleMember)
	ev, occs := seedSeriesEvent(t, app, &group, leader.ID, nil, false, 3)
	hidden := models.VisibilityInviteOnly
	if err := app.DB.Model(&models.EventOccurrence{}).Where("id = ?", occs[1].ID).Update("visibility_override", hidden).Error; err != nil {
		t.Fatal(err)
	}
	client := loginAPI(t, app, "visibility-actor", "password1234")
	out := postSeriesAnswer(client, ev.ID, map[string]any{"scope": seriesScopeAllFuture, "status": models.RegMaybe})
	if out == nil || len(out.Results) != 2 || len(out.Refusals) != 0 {
		t.Fatalf("effective visibility result=%+v", out)
	}
	var n int64
	if err := app.DB.Model(&models.Registration{}).Where("occurrence_id = ? AND user_id = ?", occs[1].ID, actor.ID).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("hidden date received %d registration rows", n)
	}
}

func TestSeriesRegistrationDisabledActorAndConcurrentNormalMutation(t *testing.T) {
	app := testApp(t)
	leader := newTestUser(t, app, "concurrent-leader", "password1234", models.RoleMember)
	group := activeGroup(t, app, leader, "Concurrent group", models.VisibilityPublic, "")
	actor := newTestUser(t, app, "concurrent-actor", "password1234", models.RoleMember)
	addMember(t, app, actor, &group, models.MembershipMember)
	ev, occs := seedSeriesEvent(t, app, &group, leader.ID, intPtr(2), true, 3)
	bulk := loginAPI(t, app, "concurrent-actor", "password1234")
	normal := loginAPI(t, app, "concurrent-actor", "password1234")
	var wg sync.WaitGroup
	codes := make([]int, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		codes[0] = bulk.do("POST", fmt.Sprintf("/api/v1/events/%d/series-registrations", ev.ID), map[string]any{"scope": seriesScopeAllFuture, "status": models.RegGoing}, true).Code
	}()
	go func() {
		defer wg.Done()
		codes[1], _ = registerAs(normal, ev.ID, occs[1].OriginalDate, models.RegMaybe, nil)
	}()
	wg.Wait()
	if codes[0] != http.StatusOK || codes[1] != http.StatusOK {
		t.Fatalf("codes=%v", codes)
	}
	var n int64
	if err := app.DB.Model(&models.Registration{}).Where("occurrence_id = ? AND user_id = ?", occs[1].ID, actor.ID).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("concurrent row count=%d", n)
	}

	disabledClient := loginAPI(t, app, "concurrent-actor", "password1234")
	if err := app.DB.Model(&models.User{}).Where("id = ?", actor.ID).Update("status", models.UserDisabled).Error; err != nil {
		t.Fatal(err)
	}
	w := disabledClient.do("POST", fmt.Sprintf("/api/v1/events/%d/series-registrations", ev.ID), map[string]any{"scope": seriesScopeAllFuture, "status": models.RegDeclined}, true)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("disabled actor=%d %s", w.Code, w.Body.String())
	}
}
