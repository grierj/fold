package main

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"fold/internal/jobs"
	"fold/internal/notify"
	"fold/models"
)

// The daily tasks are queued once per church day, however many times or
// processes ask, and each one runs cleanly on an ordinary database.
func TestDailyTasksQueueOncePerChurchDay(t *testing.T) {
	app := testApp(t)
	if err := app.DB.Where("key = ?", settingTimezone).Assign(models.Setting{Key: settingTimezone, Value: "America/Denver"}).
		FirstOrCreate(&models.Setting{}).Error; err != nil {
		t.Fatal(err)
	}
	kinds := app.dailyKinds()
	denver, err := churchZoneErr(app.DB)
	if err != nil || denver.String() != "America/Denver" {
		t.Fatalf("zone %v %v", denver, err)
	}
	// 11:30 PM in Denver on Oct 30 is already Oct 31 in UTC.
	late := time.Date(2026, 10, 31, 5, 30, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if err := app.queueDaily(kinds, late, denver); err != nil {
			t.Fatal(err)
		}
	}
	var keys []string
	app.DB.Model(&models.Job{}).Order("unique_key").Pluck("unique_key", &keys)
	if len(keys) != len(kinds) || keys[0] != "accounts.age_rollover:2026-10-30" {
		t.Fatalf("queued %v", keys)
	}
	if err := app.queueDaily(kinds, late.Add(time.Hour), denver); err != nil { // after Denver midnight
		t.Fatal(err)
	}
	var n int64
	app.DB.Model(&models.Job{}).Count(&n)
	if int(n) != 2*len(kinds) {
		t.Fatalf("want a second day's tasks, have %d jobs", n)
	}

	// Due now, to run them all.
	app.DB.Model(&models.Job{}).Where("true").Update("run_at", time.Now().Add(-time.Minute))
	runner := jobs.NewRunner(app.DB, kinds...)
	for {
		ran, err := runner.RunOnce(context.Background(), "test")
		if err != nil {
			t.Fatal(err)
		}
		if !ran {
			break
		}
	}
	var failed []models.Job
	app.DB.Where("status <> ?", models.JobDone).Find(&failed)
	if len(failed) != 0 {
		t.Fatalf("daily tasks failed: %+v", failed)
	}
}

// The notices the nightly extender writes carry their own delivery intent, so
// a crash after the extend commits cannot lose their emails: every notice has
// its notify.deliver job before anything else runs, and a drain sends them.
func TestExtendedDatesNoticesSurviveCrash(t *testing.T) {
	f := newBookingFixture(t)
	series := createFeedEvent(t, f.leader, f.group, "Weekly", f.startOn.AddDate(0, 0, 1), "FREQ=WEEKLY;COUNT=4")
	if code, out := f.seriesRoom(t, f.leader, series, map[string]any{"roomId": f.hall.ID}); code != http.StatusOK || out.Booked != 4 {
		t.Fatalf("book the series: %d %+v", code, out)
	}
	// The last two dates go missing (as if not yet generated), and another
	// event takes the hall on the last one, so the extender must tell the leader.
	third, fourth := feedDate(f.startOn.AddDate(0, 0, 15)), feedDate(f.startOn.AddDate(0, 0, 22))
	if err := f.app.DB.Where("event_id = ? AND original_date IN ?", series.ID, []string{third, fourth}).Delete(&models.EventOccurrence{}).Error; err != nil {
		t.Fatal(err)
	}
	other := createFeedEvent(t, f.leader, f.group, "Other", f.startOn.AddDate(0, 0, 22), "")
	if w := f.leader.do("PUT", "/api/v1/events/"+itoa(other.ID)+"/dates/"+fourth+"/room", map[string]any{"roomId": f.hall.ID}, true); w.Code != http.StatusOK {
		t.Fatalf("book the other event: %d %s", w.Code, w.Body.String())
	}
	drainNotices(t, f.app) // the setup's own notices are not under test
	var lastBefore uint
	f.app.DB.Model(&models.Notification{}).Select("COALESCE(MAX(id), 0)").Scan(&lastBefore)

	var extend *jobs.Kind
	for _, k := range f.app.dailyKinds() {
		if k.Name == "occurrences.extend" {
			k := k
			extend = &k
		}
	}
	if extend == nil {
		t.Fatal("no occurrences.extend task")
	}
	// Run it, and nothing after: no post-commit delivery call exists.
	if err := extend.Run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}

	var notices []models.Notification
	if err := f.app.DB.Where("kind = ? AND id > ?", notify.RoomLost, lastBefore).Order("id").Find(&notices).Error; err != nil {
		t.Fatal(err)
	}
	if len(notices) == 0 {
		t.Fatal("the extender wrote no notice for the date that lost its room")
	}
	for _, n := range notices {
		var jobsFound int64
		f.app.DB.Model(&models.Job{}).Where("kind = ? AND unique_key = ? AND status = ?", notify.DeliverKind, notify.DeliverKey(n.ID), models.JobQueued).Count(&jobsFound)
		if jobsFound != 1 {
			t.Fatalf("notice %d (%s) has %d queued notify.deliver jobs; want 1", n.ID, n.Kind, jobsFound)
		}
	}
	if m := latestEmail(t, f.app, f.leaderUser.Email); m.Subject == "" {
		t.Fatal("the drain sent no email to the leader")
	}
	for _, n := range notices {
		if d := emailDeliveryOf(t, f.app, n.ID); d.Status == models.DeliveryQueued {
			t.Fatalf("notice %d is still queued after the drain", n.ID)
		}
	}
}

// N-02 (inventory 9.2.2, a plain entry): the worker's OnDead hook tells the
// active admins from its own transaction, which starts with the job row FOR
// UPDATE and takes no contact gate. The job really dies through the app's
// runner. An admin erased while the alert is written is left out in either
// order, and the other admin is still told.
func TestDeadJobAlertSerializesWithAdminErasure(t *testing.T) {
	runErasureBarriers(t, 7402001, func(t *testing.T) erasureScene {
		app := testApp(t)
		kept := newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
		victim := newTestUser(t, app, "admin2", "password1234", models.RoleAdmin)
		if _, err := jobs.Enqueue(app.DB, deadAlertKind, nil, time.Now(), "", 1); err != nil {
			t.Fatal(err)
		}
		return erasureScene{app: app, victim: victim,
			run: func() error {
				ran, err := app.newRunner(deadAlertKinds()...).RunOnce(context.Background(), "test:barrier")
				if err == nil && !ran {
					err = errors.New("the dead job never ran")
				}
				return err
			},
			told: func(t *testing.T) {
				if n := contactCount(t, app, &models.Notification{}, "user_id = ? AND kind = ?", kept.ID, notify.SystemJobDead); n != 1 {
					t.Fatalf("the other admin has %d dead-job alerts, want 1", n)
				}
			},
		}
	})
}
