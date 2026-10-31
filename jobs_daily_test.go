package main

import (
	"context"
	"testing"
	"time"

	"fold/internal/jobs"
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
