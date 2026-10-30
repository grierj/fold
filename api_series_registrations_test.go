package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"fold/internal/mail"
	"fold/internal/notify"
	"fold/internal/policy"
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
	app.seriesClock = clock.Time
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

func waitSeriesOccurrenceWaiter(t *testing.T, app *App) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var count int64
		err := app.DB.Raw(`SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'
			AND query ILIKE '%event_occurrences%' AND query ILIKE '%FOR UPDATE%'`).Scan(&count).Error
		if err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for series occurrence lock contender")
}

type blockingSeriesMailer struct {
	next    mail.Sender
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (m *blockingSeriesMailer) Send(ctx context.Context, msg mail.Message) error {
	m.once.Do(func() { close(m.started) })
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.release:
		return m.next.Send(ctx, msg)
	}
}

func awaitSeriesSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(8 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
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
	issuerID := leader.ID
	access := models.OccurrenceAccess{SubjectUserID: actor.ID, OccurrenceID: occs[0].ID, EventID: ev.ID, OrganizerType: policy.ContactGroup,
		OrganizerID: group.ID, Origin: policy.OccurrenceOriginManagerAdd, IssuerID: &issuerID, RegistrationID: reg.ID, Status: policy.OccurrenceAccessActive, GrantedAt: time.Now().UTC()}
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
		waitSeriesOccurrenceWaiter(t, app)
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
	waitSeriesOccurrenceWaiter(t, app)
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

func TestSeriesUndoWindowStartsAtResponseReadiness(t *testing.T) {
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
	mailer := &blockingSeriesMailer{next: app.Mailer, started: make(chan struct{}), release: make(chan struct{})}
	app.Mailer = mailer

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
	awaitSeriesSignal(t, mailer.started, "promotion email delivery")
	clock.Advance(30 * time.Second)
	close(mailer.release)
	w := awaitContactResponse(t, response)
	if w.Code != http.StatusOK {
		t.Fatalf("series answer=%d %s", w.Code, w.Body.String())
	}
	var out seriesRegistrationResponse
	decodeJSON(t, w, &out)
	if out.UndoToken == "" || out.UndoExpiresAt == nil {
		t.Fatalf("missing response-ready receipt: %+v", out)
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
	if len(undone.CapacityLoss) != 1 || undone.CapacityLoss[0] != occs[0].OriginalDate {
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

func TestSeriesReceiptFinalizationDoesNotResurrectReplacedUndo(t *testing.T) {
	app := testApp(t)
	base := time.Now().UTC().Truncate(time.Second)
	useSeriesTestClock(app, base)
	leader := newTestUser(t, app, "undo-replace-leader", "password1234", models.RoleMember)
	group := activeGroup(t, app, leader, "Undo replace", models.VisibilityPublic, "")
	actor := newTestUser(t, app, "undo-replace-actor", "password1234", models.RoleMember)
	waiter := newTestUser(t, app, "undo-replace-waiter", "password1234", models.RoleMember)
	addMember(t, app, actor, &group, models.MembershipMember)
	addMember(t, app, waiter, &group, models.MembershipMember)
	ev, occs := seedSeriesEvent(t, app, &group, leader.ID, intPtr(1), true, 1)
	actorID, waiterID := actor.ID, waiter.ID
	queuedAt := base.Add(-time.Minute)
	if err := app.DB.Create(&models.Registration{OccurrenceID: occs[0].ID, UserID: &actorID, Status: models.RegGoing, AttendanceMode: models.AttendanceInPerson, Source: models.RegSourceWeb}).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Create(&models.Registration{OccurrenceID: occs[0].ID, UserID: &waiterID, Status: models.RegWaitlisted, AttendanceMode: models.AttendanceInPerson, Source: models.RegSourceWeb, WaitlistedAt: &queuedAt}).Error; err != nil {
		t.Fatal(err)
	}
	mailer := &blockingSeriesMailer{next: app.Mailer, started: make(chan struct{}), release: make(chan struct{})}
	app.Mailer = mailer
	firstClient := loginAPI(t, app, "undo-replace-actor", "password1234")
	firstResponse := queueContact(func() *httptest.ResponseRecorder {
		return firstClient.do("POST", fmt.Sprintf("/api/v1/events/%d/series-registrations", ev.ID), map[string]any{"scope": seriesScopeAllFuture, "status": models.RegMaybe}, true)
	})
	awaitSeriesSignal(t, mailer.started, "first action email delivery")

	second := postSeriesAnswer(loginAPI(t, app, "undo-replace-actor", "password1234"), ev.ID, map[string]any{"scope": seriesScopeAllFuture, "status": models.RegDeclined})
	if second == nil || second.UndoToken == "" {
		t.Fatalf("replacement receipt=%+v", second)
	}
	close(mailer.release)
	w := awaitContactResponse(t, firstResponse)
	if w.Code != http.StatusOK {
		t.Fatalf("first action=%d %s", w.Code, w.Body.String())
	}
	var first seriesRegistrationResponse
	decodeJSON(t, w, &first)
	if first.UndoToken != "" || first.UndoExpiresAt != nil {
		t.Fatalf("older response resurrected Undo: %+v", first)
	}
	var receipts []models.SeriesUndoReceipt
	if err := app.DB.Where("actor_id = ?", actor.ID).Find(&receipts).Error; err != nil {
		t.Fatal(err)
	}
	if len(receipts) != 1 || receipts[0].TokenHash != hashToken(second.UndoToken) {
		t.Fatalf("live receipts=%+v", receipts)
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
	access := models.OccurrenceAccess{SubjectUserID: actor.ID, OccurrenceID: occs[0].ID, EventID: ev.ID, OrganizerType: policy.ContactGroup,
		OrganizerID: group.ID, Origin: policy.OccurrenceOriginManagerAdd, IssuerID: &issuerID, RegistrationID: reg.ID, Status: policy.OccurrenceAccessActive, GrantedAt: time.Now().UTC()}
	if err := app.DB.Create(&access).Error; err != nil {
		t.Fatal(err)
	}
	client := loginAPI(t, app, "undo-access-actor", "password1234")
	out := postSeriesAnswer(client, ev.ID, map[string]any{"scope": seriesScopeAllFuture, "status": models.RegDeclined})
	if out == nil {
		t.Fatal("series decline failed")
	}
	if err := app.DB.First(&access, access.ID).Error; err != nil || access.Status == policy.OccurrenceAccessActive {
		t.Fatalf("access was not ended: %+v err=%v", access, err)
	}
	if w := undoSeriesAnswer(client, ev.ID, out.UndoToken, true); w.Code != http.StatusOK {
		t.Fatalf("undo=%d %s", w.Code, w.Body.String())
	}
	if got := regStatus(t, app, actor.ID, occs[0].ID); got != models.RegGoing {
		t.Fatalf("registration=%q", got)
	}
	if err := app.DB.First(&access, access.ID).Error; err != nil || access.Status == policy.OccurrenceAccessActive {
		t.Fatalf("ended access was revived: %+v err=%v", access, err)
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
