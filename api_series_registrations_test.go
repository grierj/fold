package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"fold/internal/notify"
	"fold/internal/policy"
	"fold/internal/testdb"
	"fold/models"

	"gorm.io/gorm"
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
		occs[i] = models.EventOccurrence{EventID: ev.ID, OriginalDate: calDate(at.Format("2006-01-02")), StartsAt: at, EndsAt: at.Add(time.Hour), Status: models.OccurrenceScheduled}
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

type seriesTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *seriesTestClock) Time() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *seriesTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func useSeriesTestClock(app *App, now time.Time) *seriesTestClock {
	clock := &seriesTestClock{now: now.UTC()}
	app.clock = clock.Time
	return clock
}

func holdSeriesOccurrence(t *testing.T, app *App, occurrenceID uint) *gorm.DB {
	t.Helper()
	tx := app.DB.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	if _, err := lockOccurrence(tx, occurrenceID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tx.Rollback() })
	return tx
}

// waitSeriesOccurrenceWaiter returns once some other session is blocked on a
// lock that gate (from holdSeriesOccurrence) holds.
func waitSeriesOccurrenceWaiter(t *testing.T, app *App, gate *gorm.DB) {
	t.Helper()
	var pid int
	if err := gate.Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
		t.Fatal(err)
	}
	testdb.WaitUntil(t, "a series occurrence lock contender", 5*time.Second, func() bool {
		var blocked bool
		if err := app.DB.Raw("SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname = current_database() AND ? = ANY(pg_blocking_pids(pid)))", pid).Scan(&blocked).Error; err != nil {
			t.Fatal(err)
		}
		return blocked
	})
}

// seriesUndoSnapshotDates reads the persisted undo receipt for an event and
// returns each item's date by occurrence ID. The snapshot is a jsonb column
// built from structs, so the date is decoded as a string: a timestamp or a
// number there fails the decode.
func seriesUndoSnapshotDates(t *testing.T, app *App, eventID uint) map[uint]string {
	t.Helper()
	var receipt models.SeriesUndoReceipt
	if err := app.DB.Where("event_id = ?", eventID).Order("id DESC").First(&receipt).Error; err != nil {
		t.Fatal(err)
	}
	var items []struct {
		OccurrenceID uint   `json:"occurrenceId"`
		Date         string `json:"date"`
	}
	if err := json.Unmarshal([]byte(receipt.Snapshot), &items); err != nil {
		t.Fatalf("snapshot %q: %v", receipt.Snapshot, err)
	}
	out := make(map[uint]string, len(items))
	for _, it := range items {
		out[it.OccurrenceID] = it.Date
	}
	return out
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
	out := postSeriesAnswer(client, ev.ID, map[string]any{"date": models.FormatCalendarDate(occs[1].OriginalDate), "scope": seriesScopeThisAndFollowing, "status": models.RegGoing})
	if out == nil {
		t.Fatal("series answer failed")
	}
	if len(out.Results) != 2 || len(out.Refusals) != 1 || out.Refusals[0].Code != "registration_removed" {
		t.Fatalf("results=%+v refusals=%+v", out.Results, out.Refusals)
	}
	// The dates in results and refusals are plain YYYY-MM-DD. The expected
	// strings come from the fixture's own start times (UTC), not from the
	// formatter under test.
	for i, want := range []int{1, 3} {
		if got, wantDate := out.Results[i].Date, occs[want].StartsAt.Format("2006-01-02"); got != wantDate || out.Results[i].OccurrenceID != occs[want].ID {
			t.Fatalf("result %d date=%q occurrence=%d, want %q occurrence %d", i, got, out.Results[i].OccurrenceID, wantDate, occs[want].ID)
		}
	}
	if got, want := out.Refusals[0].Date, occs[2].StartsAt.Format("2006-01-02"); got != want || out.Refusals[0].OccurrenceID != occs[2].ID {
		t.Fatalf("refusal date=%q occurrence=%d, want %q occurrence %d", got, out.Refusals[0].OccurrenceID, want, occs[2].ID)
	}
	snapshot := seriesUndoSnapshotDates(t, app, ev.ID)
	if len(snapshot) != 2 || snapshot[occs[1].ID] != occs[1].StartsAt.Format("2006-01-02") || snapshot[occs[3].ID] != occs[3].StartsAt.Format("2006-01-02") {
		t.Fatalf("persisted undo snapshot dates=%v", snapshot)
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

// The series path refuses childcare on an event without childcare, the same
// 400 the single-date path gives, and leaves no rows behind.
func TestSeriesRegistrationRefusesChildcareWhenUnavailable(t *testing.T) {
	app := testApp(t)
	leader := newTestUser(t, app, "series-care-leader", "password1234", models.RoleMember)
	group := activeGroup(t, app, leader, "Series care group", models.VisibilityPublic, "")
	actor := newTestUser(t, app, "series-care-actor", "password1234", models.RoleMember)
	addMember(t, app, actor, &group, models.MembershipMember)
	ev, occs := seedSeriesEvent(t, app, &group, leader.ID, nil, false, 2)
	client := loginAPI(t, app, "series-care-actor", "password1234")

	body := map[string]any{"scope": seriesScopeAllFuture, "status": models.RegGoing, "childcareCount": 1}
	w := client.do("POST", fmt.Sprintf("/api/v1/events/%d/series-registrations", ev.ID), body, true)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), errChildcareUnavailable.Error()) {
		t.Fatalf("childcare on a no-childcare series: %d %s", w.Code, w.Body.String())
	}
	var n int64
	app.DB.Model(&models.Registration{}).Where("occurrence_id IN ?", []uint{occs[0].ID, occs[1].ID}).Count(&n)
	if n != 0 {
		t.Fatalf("refused series answer left %d rows", n)
	}
	body["childcareCount"] = 0
	if out := postSeriesAnswer(client, ev.ID, body); out == nil || len(out.Results) != 2 {
		t.Fatalf("zero childcare should answer both dates: %+v", out)
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
	later := models.EventOccurrence{EventID: ev.ID, OriginalDate: calDate(laterAt.Format("2006-01-02")), StartsAt: laterAt, EndsAt: laterAt.Add(time.Hour), Status: models.OccurrenceScheduled}
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
	issuerID := leader.ID
	seedAccessGrant(t, app.DB, reg.ID, accessGrant{Origin: policy.OccurrenceOriginManagerAdd, IssuerID: &issuerID}, &issuerID, time.Now().UTC())
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

func TestSeriesRegistrationRefreshesClockAfterContactAndOccurrenceLocks(t *testing.T) {
	t.Run("date starts while waiting on contact gate", func(t *testing.T) {
		app := testApp(t)
		base := time.Now().UTC().Truncate(time.Second)
		clock := useSeriesTestClock(app, base)
		leader := newTestUser(t, app, "clock-start-leader", "password1234", models.RoleMember)
		group := activeGroup(t, app, leader, "Clock start", models.VisibilityPublic, "")
		actor := newTestUser(t, app, "clock-start-actor", "password1234", models.RoleMember)
		addMember(t, app, actor, &group, models.MembershipMember)
		ev, occs := seedSeriesEvent(t, app, &group, leader.ID, nil, false, 2)
		starts := []time.Time{base.Add(time.Minute), base.Add(24 * time.Hour)}
		for i := range occs {
			if err := app.DB.Model(&models.EventOccurrence{}).Where("id = ?", occs[i].ID).
				Updates(map[string]any{"starts_at": starts[i], "ends_at": starts[i].Add(time.Hour)}).Error; err != nil {
				t.Fatal(err)
			}
		}

		gate := holdContactGate(t, app)
		client := loginAPI(t, app, "clock-start-actor", "password1234")
		response := queueContact(func() *httptest.ResponseRecorder {
			return client.do("POST", fmt.Sprintf("/api/v1/events/%d/series-registrations", ev.ID), map[string]any{"scope": seriesScopeAllFuture, "status": models.RegMaybe}, true)
		})
		waitContactWaiters(t, app, 1)
		clock.Advance(2 * time.Minute)
		if err := gate.Commit().Error; err != nil {
			t.Fatal(err)
		}
		w := awaitContactResponse(t, response)
		if w.Code != http.StatusOK {
			t.Fatalf("series answer=%d %s", w.Code, w.Body.String())
		}
		var out seriesRegistrationResponse
		decodeJSON(t, w, &out)
		if len(out.Results) != 1 || out.Results[0].OccurrenceID != occs[1].ID {
			t.Fatalf("started date was not excluded: %+v", out.Results)
		}
		var count int64
		if err := app.DB.Model(&models.Registration{}).Where("occurrence_id = ? AND user_id = ?", occs[0].ID, actor.ID).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("started date registration count=%d err=%v", count, err)
		}
	})

	t.Run("promotion cutoff crosses while waiting on occurrence", func(t *testing.T) {
		app := testApp(t)
		base := time.Now().UTC().Truncate(time.Second)
		clock := useSeriesTestClock(app, base)
		leader := newTestUser(t, app, "clock-cutoff-leader", "password1234", models.RoleMember)
		group := activeGroup(t, app, leader, "Clock cutoff", models.VisibilityPublic, "")
		actor := newTestUser(t, app, "clock-cutoff-actor", "password1234", models.RoleMember)
		waiter := newTestUser(t, app, "clock-cutoff-waiter", "password1234", models.RoleMember)
		addMember(t, app, actor, &group, models.MembershipMember)
		addMember(t, app, waiter, &group, models.MembershipMember)
		ev, occs := seedSeriesEvent(t, app, &group, leader.ID, intPtr(1), true, 1)
		start := base.Add(2*time.Hour + time.Minute)
		if err := app.DB.Model(&models.EventOccurrence{}).Where("id = ?", occs[0].ID).
			Updates(map[string]any{"starts_at": start, "ends_at": start.Add(time.Hour)}).Error; err != nil {
			t.Fatal(err)
		}
		actorID, waiterID := actor.ID, waiter.ID
		queuedAt := base.Add(-time.Minute)
		if err := app.DB.Create(&models.Registration{OccurrenceID: occs[0].ID, UserID: &actorID, Status: models.RegGoing, AttendanceMode: models.AttendanceInPerson, Source: models.RegSourceWeb}).Error; err != nil {
			t.Fatal(err)
		}
		if err := app.DB.Create(&models.Registration{OccurrenceID: occs[0].ID, UserID: &waiterID, Status: models.RegWaitlisted, AttendanceMode: models.AttendanceInPerson, Source: models.RegSourceWeb, WaitlistedAt: &queuedAt}).Error; err != nil {
			t.Fatal(err)
		}

		gate := holdSeriesOccurrence(t, app, occs[0].ID)
		client := loginAPI(t, app, "clock-cutoff-actor", "password1234")
		response := queueContact(func() *httptest.ResponseRecorder {
			return client.do("POST", fmt.Sprintf("/api/v1/events/%d/series-registrations", ev.ID), map[string]any{"scope": seriesScopeAllFuture, "status": models.RegMaybe}, true)
		})
		waitSeriesOccurrenceWaiter(t, app, gate)
		clock.Advance(2 * time.Minute)
		if err := gate.Commit().Error; err != nil {
			t.Fatal(err)
		}
		w := awaitContactResponse(t, response)
		if w.Code != http.StatusOK {
			t.Fatalf("series answer=%d %s", w.Code, w.Body.String())
		}
		if got := regStatus(t, app, actor.ID, occs[0].ID); got != models.RegMaybe {
			t.Fatalf("actor status=%q", got)
		}
		if got := regStatus(t, app, waiter.ID, occs[0].ID); got != models.RegWaitlisted {
			t.Fatalf("waiter was promoted inside cutoff: %q", got)
		}
		var notice models.Notification
		if err := app.DB.Where("user_id = ? AND kind = ?", leader.ID, notify.WaitlistCutoff).First(&notice).Error; err != nil {
			t.Fatalf("cutoff notice: %v", err)
		}
		if !strings.Contains(notice.Body, "within 2 hours") || !strings.Contains(notice.Body, "nobody was promoted automatically") {
			t.Fatalf("cutoff notice body=%q", notice.Body)
		}
		if got, want := noticeDataDate(t, notice), occs[0].StartsAt.Format("2006-01-02"); got != want {
			t.Fatalf("cutoff notice date=%q, want %q", got, want)
		}
	})
}

func undoSeriesAnswer(c *apiClient, eventID uint, token string, csrf bool) *httptest.ResponseRecorder {
	return c.do("POST", fmt.Sprintf("/api/v1/events/%d/series-registrations/undo", eventID), map[string]any{"token": token}, csrf)
}

func TestSeriesUndoRestoresNoAnswerAndIsActorBoundSingleUse(t *testing.T) {
	app := testApp(t)
	leader := newTestUser(t, app, "undo-leader", "password1234", models.RoleMember)
	group := activeGroup(t, app, leader, "Undo group", models.VisibilityPublic, "")
	actor := newTestUser(t, app, "undo-actor", "password1234", models.RoleMember)
	other := newTestUser(t, app, "undo-other", "password1234", models.RoleMember)
	addMember(t, app, actor, &group, models.MembershipMember)
	addMember(t, app, other, &group, models.MembershipMember)
	ev, occs := seedSeriesEvent(t, app, &group, leader.ID, nil, false, 2)
	client := loginAPI(t, app, "undo-actor", "password1234")
	out := postSeriesAnswer(client, ev.ID, map[string]any{"scope": seriesScopeAllFuture, "status": models.RegMaybe})
	if out == nil || out.UndoToken == "" || out.UndoExpiresAt == nil {
		t.Fatalf("missing receipt: %+v", out)
	}
	for i := range occs {
		if got, want := out.Results[i].Date, occs[i].StartsAt.Format("2006-01-02"); got != want {
			t.Fatalf("result %d date=%q, want %q", i, got, want)
		}
	}
	snapshot := seriesUndoSnapshotDates(t, app, ev.ID)
	if len(snapshot) != 2 || snapshot[occs[0].ID] != occs[0].StartsAt.Format("2006-01-02") || snapshot[occs[1].ID] != occs[1].StartsAt.Format("2006-01-02") {
		t.Fatalf("persisted undo snapshot dates=%v", snapshot)
	}
	if w := undoSeriesAnswer(loginAPI(t, app, "undo-other", "password1234"), ev.ID, out.UndoToken, true); w.Code != http.StatusForbidden {
		t.Fatalf("other actor=%d %s", w.Code, w.Body.String())
	}
	if w := undoSeriesAnswer(client, ev.ID, out.UndoToken, false); w.Code != http.StatusForbidden {
		t.Fatalf("missing csrf=%d %s", w.Code, w.Body.String())
	}
	if w := undoSeriesAnswer(client, ev.ID, out.UndoToken, true); w.Code != http.StatusOK {
		t.Fatalf("undo=%d %s", w.Code, w.Body.String())
	}
	var n int64
	if err := app.DB.Model(&models.Registration{}).Where("user_id = ? AND occurrence_id IN ?", actor.ID, []uint{occs[0].ID, occs[1].ID}).Count(&n).Error; err != nil || n != 0 {
		t.Fatalf("no-answer restore count=%d err=%v", n, err)
	}
	if w := undoSeriesAnswer(client, ev.ID, out.UndoToken, true); w.Code != http.StatusConflict {
		t.Fatalf("replay=%d %s", w.Code, w.Body.String())
	}
}

func TestSeriesUndoRefreshesClockAfterOccurrenceLock(t *testing.T) {
	app := testApp(t)
	base := time.Now().UTC().Truncate(time.Second)
	clock := useSeriesTestClock(app, base)
	leader := newTestUser(t, app, "undo-clock-leader", "password1234", models.RoleMember)
	group := activeGroup(t, app, leader, "Undo clock", models.VisibilityPublic, "")
	actor := newTestUser(t, app, "undo-clock-actor", "password1234", models.RoleMember)
	addMember(t, app, actor, &group, models.MembershipMember)
	ev, occs := seedSeriesEvent(t, app, &group, leader.ID, nil, false, 1)
	start := base.Add(time.Second)
	if err := app.DB.Model(&models.EventOccurrence{}).Where("id = ?", occs[0].ID).
		Updates(map[string]any{"starts_at": start, "ends_at": start.Add(time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	client := loginAPI(t, app, "undo-clock-actor", "password1234")
	out := postSeriesAnswer(client, ev.ID, map[string]any{"scope": seriesScopeAllFuture, "status": models.RegMaybe})
	if out == nil || out.UndoToken == "" {
		t.Fatalf("series receipt=%+v", out)
	}

	gate := holdSeriesOccurrence(t, app, occs[0].ID)
	response := queueContact(func() *httptest.ResponseRecorder {
		return undoSeriesAnswer(client, ev.ID, out.UndoToken, true)
	})
	waitSeriesOccurrenceWaiter(t, app, gate)
	clock.Advance(2 * time.Second)
	if err := gate.Commit().Error; err != nil {
		t.Fatal(err)
	}
	w := awaitContactResponse(t, response)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "date") {
		t.Fatalf("undo after date start=%d %s", w.Code, w.Body.String())
	}
	if got := regStatus(t, app, actor.ID, occs[0].ID); got != models.RegMaybe {
		t.Fatalf("undo mutated a started date: %q", got)
	}
	var receipt models.SeriesUndoReceipt
	if err := app.DB.Where("token_hash = ?", hashToken(out.UndoToken)).First(&receipt).Error; err != nil {
		t.Fatal(err)
	}
	if receipt.UsedAt != nil {
		t.Fatalf("failed atomic Undo consumed receipt at %v", *receipt.UsedAt)
	}
}

func TestSeriesUndoWindowStartsAfterLockWait(t *testing.T) {
	app := testApp(t)
	base := time.Now().UTC().Truncate(time.Second)
	clock := useSeriesTestClock(app, base)
	leader := newTestUser(t, app, "undo-window-leader", "password1234", models.RoleMember)
	group := activeGroup(t, app, leader, "Undo window", models.VisibilityPublic, "")
	actor := newTestUser(t, app, "undo-window-actor", "password1234", models.RoleMember)
	waiter := newTestUser(t, app, "undo-window-waiter", "password1234", models.RoleMember)
	addMember(t, app, actor, &group, models.MembershipMember)
	addMember(t, app, waiter, &group, models.MembershipMember)
	ev, occs := seedSeriesEvent(t, app, &group, leader.ID, intPtr(1), true, 1)
	start := base.Add(4 * time.Hour)
	if err := app.DB.Model(&models.EventOccurrence{}).Where("id = ?", occs[0].ID).
		Updates(map[string]any{"starts_at": start, "ends_at": start.Add(time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	actorID, waiterID := actor.ID, waiter.ID
	queuedAt := base.Add(-time.Minute)
	if err := app.DB.Create(&models.Registration{OccurrenceID: occs[0].ID, UserID: &actorID, Status: models.RegGoing, AttendanceMode: models.AttendanceInPerson, Source: models.RegSourceWeb}).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Create(&models.Registration{OccurrenceID: occs[0].ID, UserID: &waiterID, Status: models.RegWaitlisted, AttendanceMode: models.AttendanceInPerson, Source: models.RegSourceWeb, WaitlistedAt: &queuedAt}).Error; err != nil {
		t.Fatal(err)
	}
	gate := holdContactGate(t, app)
	client := loginAPI(t, app, "undo-window-actor", "password1234")
	response := queueContact(func() *httptest.ResponseRecorder {
		return client.do("POST", fmt.Sprintf("/api/v1/events/%d/series-registrations", ev.ID), map[string]any{"scope": seriesScopeAllFuture, "status": models.RegMaybe}, true)
	})
	waitContactWaiters(t, app, 1)
	clock.Advance(20 * time.Second)
	if err := gate.Commit().Error; err != nil {
		t.Fatal(err)
	}
	w := awaitContactResponse(t, response)
	if w.Code != http.StatusOK {
		t.Fatalf("series answer=%d %s", w.Code, w.Body.String())
	}
	var out seriesRegistrationResponse
	decodeJSON(t, w, &out)
	if out.UndoToken == "" || out.UndoExpiresAt == nil {
		t.Fatalf("missing Undo receipt: %+v", out)
	}
	if got := out.UndoExpiresAt.Sub(clock.Time()); got != seriesUndoTTL {
		t.Fatalf("usable Undo window=%v want %v", got, seriesUndoTTL)
	}
	undo := undoSeriesAnswer(client, ev.ID, out.UndoToken, true)
	if undo.Code != http.StatusOK {
		t.Fatalf("Undo inside response window=%d %s", undo.Code, undo.Body.String())
	}
	var undone seriesRegistrationResponse
	decodeJSON(t, undo, &undone)
	if len(undone.CapacityLoss) != 1 || undone.CapacityLoss[0] != models.FormatCalendarDate(occs[0].OriginalDate) {
		t.Fatalf("current-capacity loss=%v", undone.CapacityLoss)
	}
	if got := regStatus(t, app, actor.ID, occs[0].ID); got != models.RegWaitlisted {
		t.Fatalf("Undo forced a lost seat: %q", got)
	}

	next := postSeriesAnswer(client, ev.ID, map[string]any{"scope": seriesScopeAllFuture, "status": models.RegMaybe})
	if next == nil || next.UndoToken == "" {
		t.Fatalf("second receipt=%+v", next)
	}
	clock.Advance(seriesUndoTTL)
	if expired := undoSeriesAnswer(client, ev.ID, next.UndoToken, true); expired.Code != http.StatusConflict || !strings.Contains(expired.Body.String(), "time") {
		t.Fatalf("Undo at expiry boundary=%d %s", expired.Code, expired.Body.String())
	}
}

func TestSeriesNewerActionReplacesOlderUndo(t *testing.T) {
	app := testApp(t)
	useSeriesTestClock(app, time.Now().UTC().Truncate(time.Second))
	leader := newTestUser(t, app, "undo-replace-leader", "password1234", models.RoleMember)
	group := activeGroup(t, app, leader, "Undo replace", models.VisibilityPublic, "")
	actor := newTestUser(t, app, "undo-replace-actor", "password1234", models.RoleMember)
	addMember(t, app, actor, &group, models.MembershipMember)
	ev, _ := seedSeriesEvent(t, app, &group, leader.ID, nil, false, 1)
	client := loginAPI(t, app, "undo-replace-actor", "password1234")
	first := postSeriesAnswer(client, ev.ID, map[string]any{"scope": seriesScopeAllFuture, "status": models.RegMaybe})
	second := postSeriesAnswer(client, ev.ID, map[string]any{"scope": seriesScopeAllFuture, "status": models.RegDeclined})
	if first == nil || first.UndoToken == "" || second == nil || second.UndoToken == "" {
		t.Fatalf("receipts first=%+v second=%+v", first, second)
	}
	var receipts []models.SeriesUndoReceipt
	if err := app.DB.Where("actor_id = ?", actor.ID).Find(&receipts).Error; err != nil {
		t.Fatal(err)
	}
	if len(receipts) != 1 || receipts[0].TokenHash != hashToken(second.UndoToken) {
		t.Fatalf("live receipts=%+v", receipts)
	}
	if old := undoSeriesAnswer(client, ev.ID, first.UndoToken, true); old.Code != http.StatusNotFound {
		t.Fatalf("Undo with a replaced receipt=%d %s", old.Code, old.Body.String())
	}
}

func TestSeriesUndoRejectsExpiryChangedStateAndRevokedAuthority(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, *App, *models.User, models.Group, *seriesRegistrationResponse)
	}{
		{"expiry boundary", func(t *testing.T, app *App, _ *models.User, _ models.Group, out *seriesRegistrationResponse) {
			if err := app.DB.Model(&models.SeriesUndoReceipt{}).Where("token_hash = ?", hashToken(out.UndoToken)).Update("expires_at", time.Now().UTC()).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"intervening edit", func(t *testing.T, app *App, actor *models.User, _ models.Group, _ *seriesRegistrationResponse) {
			if err := app.DB.Model(&models.Registration{}).Where("user_id = ?", actor.ID).Update("status", models.RegGoing).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"revoked authority", func(t *testing.T, app *App, actor *models.User, group models.Group, _ *seriesRegistrationResponse) {
			if err := app.DB.Model(&models.Membership{}).Where("user_id = ? AND group_id = ?", actor.ID, group.ID).Update("status", "removed").Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"ended date provenance", func(t *testing.T, app *App, actor *models.User, _ models.Group, _ *seriesRegistrationResponse) {
			var reg models.Registration
			if err := app.DB.Where("user_id = ?", actor.ID).First(&reg).Error; err != nil {
				t.Fatal(err)
			}
			if err := app.DB.Model(&models.EventOccurrence{}).Where("id = ?", reg.OccurrenceID).Update("status", models.OccurrenceCancelled).Error; err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := testApp(t)
			leader := newTestUser(t, app, "undo-check-leader", "password1234", models.RoleMember)
			group := activeGroup(t, app, leader, "Undo checks", models.VisibilityInviteOnly, "")
			actor := newTestUser(t, app, "undo-check-actor", "password1234", models.RoleMember)
			addMember(t, app, actor, &group, models.MembershipMember)
			ev, _ := seedSeriesEvent(t, app, &group, leader.ID, nil, false, 1)
			client := loginAPI(t, app, "undo-check-actor", "password1234")
			out := postSeriesAnswer(client, ev.ID, map[string]any{"scope": seriesScopeAllFuture, "status": models.RegMaybe})
			if out == nil {
				t.Fatal("series answer failed")
			}
			tc.mutate(t, app, actor, group, out)
			if w := undoSeriesAnswer(client, ev.ID, out.UndoToken, true); w.Code != http.StatusConflict {
				t.Fatalf("undo=%d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestSeriesUndoDoesNotReviveEndedOccurrenceAccess(t *testing.T) {
	app := testApp(t)
	leader := newTestUser(t, app, "undo-access-leader", "password1234", models.RoleMember)
	group := activeGroup(t, app, leader, "Undo access", models.VisibilityInviteOnly, "")
	actor := newTestUser(t, app, "undo-access-actor", "password1234", models.RoleMember)
	addMember(t, app, actor, &group, models.MembershipMember)
	ev, occs := seedSeriesEvent(t, app, &group, leader.ID, nil, false, 1)
	actorID := actor.ID
	reg := models.Registration{OccurrenceID: occs[0].ID, UserID: &actorID, Status: models.RegGoing, AttendanceMode: models.AttendanceInPerson, Source: models.RegSourceAdmin}
	if err := app.DB.Create(&reg).Error; err != nil {
		t.Fatal(err)
	}
	issuerID := leader.ID
	access := seedAccessGrant(t, app.DB, reg.ID, accessGrant{Origin: policy.OccurrenceOriginManagerAdd, IssuerID: &issuerID}, &issuerID, time.Now().UTC())
	client := loginAPI(t, app, "undo-access-actor", "password1234")
	out := postSeriesAnswer(client, ev.ID, map[string]any{"scope": seriesScopeAllFuture, "status": models.RegDeclined})
	if out == nil {
		t.Fatal("series decline failed")
	}
	if row := loadAccessRow(t, app.DB, access.ID); row.Status == policy.OccurrenceAccessActive {
		t.Fatalf("access was not ended: %+v", row)
	}
	if w := undoSeriesAnswer(client, ev.ID, out.UndoToken, true); w.Code != http.StatusOK {
		t.Fatalf("undo=%d %s", w.Code, w.Body.String())
	}
	if got := regStatus(t, app, actor.ID, occs[0].ID); got != models.RegGoing {
		t.Fatalf("registration=%q", got)
	}
	if row := loadAccessRow(t, app.DB, access.ID); row.Status == policy.OccurrenceAccessActive {
		t.Fatalf("ended access was revived: %+v", row)
	}
}

func TestSeriesUndoConcurrentAndLostSeat(t *testing.T) {
	app := testApp(t)
	leader := newTestUser(t, app, "undo-seat-leader", "password1234", models.RoleMember)
	group := activeGroup(t, app, leader, "Undo seats", models.VisibilityPublic, "")
	actor := newTestUser(t, app, "undo-seat-actor", "password1234", models.RoleMember)
	waiter := newTestUser(t, app, "undo-seat-waiter", "password1234", models.RoleMember)
	addMember(t, app, actor, &group, models.MembershipMember)
	addMember(t, app, waiter, &group, models.MembershipMember)
	ev, occs := seedSeriesEvent(t, app, &group, leader.ID, intPtr(1), true, 1)
	actorID, waiterID := actor.ID, waiter.ID
	if err := app.DB.Create(&models.Registration{OccurrenceID: occs[0].ID, UserID: &actorID, Status: models.RegGoing, AttendanceMode: models.AttendanceInPerson, Source: models.RegSourceWeb}).Error; err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-time.Minute)
	if err := app.DB.Create(&models.Registration{OccurrenceID: occs[0].ID, UserID: &waiterID, Status: models.RegWaitlisted, AttendanceMode: models.AttendanceInPerson, Source: models.RegSourceWeb, WaitlistedAt: &at}).Error; err != nil {
		t.Fatal(err)
	}
	client := loginAPI(t, app, "undo-seat-actor", "password1234")
	out := postSeriesAnswer(client, ev.ID, map[string]any{"scope": seriesScopeAllFuture, "status": models.RegMaybe})
	if out == nil {
		t.Fatal("series answer failed")
	}
	if got := regStatus(t, app, waiter.ID, occs[0].ID); got != models.RegGoing {
		t.Fatalf("waiter=%q", got)
	}

	clients := []*apiClient{client, loginAPI(t, app, "undo-seat-actor", "password1234")}
	codes := make([]int, 2)
	var bodies [2]string
	var wg sync.WaitGroup
	wg.Add(2)
	for i := range clients {
		go func(i int) {
			defer wg.Done()
			w := undoSeriesAnswer(clients[i], ev.ID, out.UndoToken, true)
			codes[i], bodies[i] = w.Code, w.Body.String()
		}(i)
	}
	wg.Wait()
	if !((codes[0] == http.StatusOK && codes[1] == http.StatusConflict) || (codes[1] == http.StatusOK && codes[0] == http.StatusConflict)) {
		t.Fatalf("codes=%v bodies=%v", codes, bodies)
	}
	if got := regStatus(t, app, actor.ID, occs[0].ID); got != models.RegWaitlisted {
		t.Fatalf("actor seat was forced back: %q", got)
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
		codes[1], _ = registerAs(normal, ev.ID, models.FormatCalendarDate(occs[1].OriginalDate), models.RegMaybe, nil)
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

// TestCanRegisterWithViewerKinds pins that single-date registration authority
// (canRegisterIn), series register and series Undo are one decision: for every
// kind of viewer and event visibility they give the same answer. Guests and
// signed-out visitors have no session, so for them the series routes refuse
// before authority is asked (a guest account fails lockActorIn, a visitor has
// no cookie) and only the shared decision is compared.
func TestCanRegisterWithViewerKinds(t *testing.T) {
	inviteOnly := models.VisibilityInviteOnly
	cases := []struct {
		name       string
		visibility string
		override   *string
	}{
		{"public", models.VisibilityPublic, nil},
		{"private", models.VisibilityPrivate, nil},
		{"invite only", models.VisibilityInviteOnly, nil},
		{"date override", models.VisibilityPublic, &inviteOnly},
	}
	viewers := []string{"signed out", "guest account", "member", "leader", "admin", "teen", "invited date only", "stranger"}
	// Known answers by case order (public, private, invite only, date override):
	// 1 allowed, 0 refused, -1 only compared. The teen is not pinned.
	known := map[string][4]int{
		"signed out":        {0, 0, 0, 0},
		"guest account":     {1, 0, 0, 0},
		"member":            {1, 1, 1, 1},
		"leader":            {1, 1, 1, 1},
		"admin":             {1, 1, 1, 1},
		"teen":              {-1, -1, -1, -1},
		"invited date only": {1, 1, 0, 0},
		"stranger":          {1, 1, 0, 0},
	}
	for ci, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := testApp(t)
			leader := newTestUser(t, app, "viewer-leader", "password1234", models.RoleMember)
			group := activeGroup(t, app, leader, "Viewer group", tc.visibility, "")
			ev, occs := seedSeriesEvent(t, app, &group, leader.ID, nil, false, 2)
			if tc.override != nil {
				if err := app.DB.Model(&models.EventOccurrence{}).Where("event_id = ?", ev.ID).Update("visibility_override", *tc.override).Error; err != nil {
					t.Fatal(err)
				}
				if err := app.DB.Where("event_id = ?", ev.ID).Order("id").Find(&occs).Error; err != nil {
					t.Fatal(err)
				}
			}
			// The series and Undo routes need authority for the series and for
			// every date, so the shared decision is asked for both.
			decide := func(u *models.User) bool {
				if !app.canRegisterIn(app.DB, u, &ev) {
					return false
				}
				for i := range occs {
					if !app.canRegisterIn(app.DB, u, dateEvent(&ev, &occs[i])) {
						return false
					}
				}
				return true
			}
			for _, kind := range viewers {
				name := "viewer-" + strings.ReplaceAll(kind, " ", "-")
				var u *models.User
				switch kind {
				case "signed out":
				case "guest account":
					u = newTestUser(t, app, name, "password1234", models.RoleMember)
					if err := app.DB.Model(u).Update("status", models.UserGuest).Error; err != nil {
						t.Fatal(err)
					}
					u.Status = models.UserGuest
				case "member":
					u = newTestUser(t, app, name, "password1234", models.RoleMember)
					addMember(t, app, u, &group, models.MembershipMember)
				case "leader":
					u = leader
				case "admin":
					u = newTestUser(t, app, name, "password1234", models.RoleAdmin)
				case "teen":
					u = newTestUser(t, app, name, "password1234", models.RoleMember)
					birth := time.Now().AddDate(-15, 0, 0)
					if err := app.DB.Model(u).Updates(map[string]any{"account_type": models.AccountTeen, "birth_date": birth}).Error; err != nil {
						t.Fatal(err)
					}
				case "invited date only":
					u = newTestUser(t, app, name, "password1234", models.RoleMember)
					uid := u.ID
					reg := models.Registration{OccurrenceID: occs[0].ID, UserID: &uid, Status: models.RegGoing, AttendanceMode: models.AttendanceInPerson, Source: models.RegSourceAdmin}
					if err := app.DB.Create(&reg).Error; err != nil {
						t.Fatal(err)
					}
					issuer := leader.ID
					seedAccessGrant(t, app.DB, reg.ID, accessGrant{Origin: policy.OccurrenceOriginManagerAdd, IssuerID: &issuer}, &issuer, time.Now().UTC())
				case "stranger":
					u = newTestUser(t, app, name, "password1234", models.RoleMember)
				}
				if u != nil {
					if err := app.DB.First(u, u.ID).Error; err != nil {
						t.Fatal(err)
					}
				}
				want := decide(u)
				if pinned := known[kind][ci]; pinned >= 0 && want != (pinned == 1) {
					t.Errorf("%s: shared decision=%v, want %v", kind, want, pinned == 1)
				}
				if got := canRegisterWith(u, viewOf(&ev, false), asGroup(&group), policy.Rel{}); u != nil && u.Status == models.UserGuest && got != (tc.visibility == models.VisibilityPublic) {
					t.Errorf("%s: guest rule=%v for %s", kind, got, tc.visibility)
				}

				switch kind {
				case "signed out":
					c := newAPIClient(t, app)
					c.fetchCSRF()
					if w := c.do("POST", fmt.Sprintf("/api/v1/events/%d/series-registrations", ev.ID), map[string]any{"scope": seriesScopeAllFuture, "status": models.RegMaybe}, true); w.Code != http.StatusUnauthorized {
						t.Errorf("%s: series register=%d", kind, w.Code)
					}
					continue
				case "guest account":
					if _, err := lockActorIn(app.DB, u, actorNoKeyUpdate); err == nil {
						t.Errorf("%s: a guest account was accepted as a series actor", kind)
					}
					continue
				}

				client := loginAPI(t, app, name, "password1234")
				// Undo: the actor holds an answer on every date and a receipt for it.
				for i := range occs {
					uid := u.ID
					var have int64
					if err := app.DB.Model(&models.Registration{}).Where("occurrence_id = ? AND user_id = ?", occs[i].ID, uid).Count(&have).Error; err != nil {
						t.Fatal(err)
					}
					if have == 0 {
						reg := models.Registration{OccurrenceID: occs[i].ID, UserID: &uid, Status: models.RegMaybe, AttendanceMode: models.AttendanceInPerson, Source: models.RegSourceWeb}
						if err := app.DB.Create(&reg).Error; err != nil {
							t.Fatal(err)
						}
					}
				}
				var items []seriesUndoItem
				for i := range occs {
					var reg models.Registration
					if err := app.DB.Where("occurrence_id = ? AND user_id = ?", occs[i].ID, u.ID).First(&reg).Error; err != nil {
						t.Fatal(err)
					}
					items = append(items, seriesUndoItem{OccurrenceID: occs[i].ID, Date: models.FormatCalendarDate(occs[i].OriginalDate), After: undoState(reg)})
				}
				snapshot, err := json.Marshal(items)
				if err != nil {
					t.Fatal(err)
				}
				raw, err := newRawToken()
				if err != nil {
					t.Fatal(err)
				}
				if err := app.DB.Create(&models.SeriesUndoReceipt{TokenHash: hashToken(raw), ActorID: u.ID, EventID: ev.ID,
					Snapshot: string(snapshot), ExpiresAt: time.Now().UTC().Add(time.Minute), CreatedAt: time.Now().UTC()}).Error; err != nil {
					t.Fatal(err)
				}
				if undone := undoSeriesAnswer(client, ev.ID, raw, true).Code == http.StatusOK; undone != want {
					t.Errorf("%s: series Undo allowed=%v, shared decision=%v", kind, undone, want)
				}

				w := client.do("POST", fmt.Sprintf("/api/v1/events/%d/series-registrations", ev.ID), map[string]any{"scope": seriesScopeAllFuture, "status": models.RegMaybe}, true)
				registered := false
				if w.Code == http.StatusOK {
					var out seriesRegistrationResponse
					decodeJSON(t, w, &out)
					registered = len(out.Results) == len(occs)
				}
				if registered != want {
					t.Errorf("%s: series register allowed=%v (%d), shared decision=%v", kind, registered, w.Code, want)
				}
			}
		})
	}
}
