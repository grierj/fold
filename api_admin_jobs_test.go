package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"fold/internal/audit"
	"fold/internal/jobs"
	"fold/internal/notify"
	"fold/models"
)

func TestDeadNoticeJobDecisions(t *testing.T) {
	for _, status := range []string{models.DeliveryQueued, models.DeliveryDeferred} {
		for _, decision := range []struct {
			name string
			kind string
		}{
			{"dismiss", notify.DeliverKind},
			{"retry", notify.DeliverKind},
			{"dismiss", "contact.sweep"},
		} {
			t.Run(status+"/"+decision.name+"/"+decision.kind, func(t *testing.T) {
				app := testApp(t)
				u := newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
				admin := loginAPI(t, app, "admin", "password1234")
				admin.holdNotices = true
				id, job := seedNotifyDeliver(t, app, u, 1)
				now := time.Now().UTC()
				if err := app.DB.Model(&models.NotificationDelivery{}).Where("notification_id = ?", id).
					Updates(map[string]any{"status": status, "reason": "waiting", "created_at": now.Add(-time.Hour), "updated_at": now.Add(-time.Hour)}).Error; err != nil {
					t.Fatal(err)
				}
				if err := app.DB.Model(&job).Updates(map[string]any{"status": models.JobDead, "kind": decision.kind}).Error; err != nil {
					t.Fatal(err)
				}
				before := emailDeliveryOf(t, app, id)
				path := "/api/v1/admin/jobs/" + itoa(job.ID) + "/" + decision.name
				if w := admin.do("POST", path, nil, true); w.Code != http.StatusOK {
					t.Fatalf("%s: %d %s", decision.name, w.Code, w.Body.String())
				}
				action, jobStatus := audit.JobDismissed, models.JobDismissed
				if decision.name == "retry" {
					action, jobStatus = audit.JobRetried, models.JobQueued
				}
				if got := reloadJob(t, app, job.ID); got.Status != jobStatus {
					t.Fatalf("job status = %s, want %s", got.Status, jobStatus)
				}
				if auditCount(t, app, action) != 1 {
					t.Fatal("decision should be audited")
				}
				after := emailDeliveryOf(t, app, id)
				if decision.name != "dismiss" || decision.kind != notify.DeliverKind {
					if after.Status != before.Status || after.Reason != before.Reason || !after.UpdatedAt.Equal(before.UpdatedAt) {
						t.Fatalf("decision changed the email ledger: %+v -> %+v", before, after)
					}
					return
				}
				if after.Status != models.DeliverySuppressed || after.Reason != emailDismissed || !after.UpdatedAt.After(before.UpdatedAt) {
					t.Fatalf("dismissed email: %+v", after)
				}
				if n, err := jobs.PruneDone(app.DB, now.Add(time.Hour)); err != nil || n != 1 {
					t.Fatalf("prune: %d %v", n, err)
				}
				var count int64
				if err := app.DB.Model(&models.Job{}).Where("id = ?", job.ID).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("pruned job remains: %d %v", count, err)
				}
				if err := app.reconcileNotifications(context.Background(), now); err != nil {
					t.Fatal(err)
				}
				if err := app.DB.Model(&models.Job{}).Where("kind = ?", notify.DeliverKind).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("requeued dismissed notice: %d %v", count, err)
				}
				drainNotices(t, app)
				if mailCount(t, app, "") != 0 || len(pushJobsFor(t, app, id)) != 0 {
					t.Fatal("dismissed notice sent mail or queued push")
				}
			})
		}
	}
}

func deadJob(t *testing.T, app *App, kind, key, lastError string) models.Job {
	t.Helper()
	if _, err := enqueueJob(app.DB, kind, map[string]string{"email": "secret@example.com"}, time.Now(), key, 1); err != nil {
		t.Fatal(err)
	}
	var j models.Job
	app.DB.Where("unique_key = ?", key).First(&j)
	app.DB.Model(&j).Updates(map[string]any{"status": models.JobDead, "attempts": 5, "last_error": lastError})
	app.DB.First(&j, j.ID)
	return j
}

func TestSystemPageListsDeadJobsForAdmins(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	newTestUser(t, app, "member", "password1234", models.RoleMember)
	admin, member := loginAPI(t, app, "admin", "password1234"), loginAPI(t, app, "member", "password1234")
	extend := deadJob(t, app, "occurrences.extend", "occurrences.extend:2026-10-30", "connection refused")
	deadJob(t, app, "mystery.kind", "mystery:1", "no handler")
	enqueueJob(app.DB, "accounts.age_rollover", nil, time.Now(), "alive", 0) // queued, not listed

	if w := member.do("GET", "/api/v1/admin/jobs", nil, false); w.Code != http.StatusForbidden {
		t.Fatalf("member: %d", w.Code)
	}
	if w := member.do("POST", "/api/v1/admin/jobs/"+itoa(extend.ID)+"/retry", nil, true); w.Code != http.StatusForbidden {
		t.Fatalf("member retry: %d", w.Code)
	}

	w := admin.do("GET", "/api/v1/admin/jobs", nil, false)
	body := w.Body.String()
	var out struct {
		Jobs []adminJobJSON `json:"jobs"`
		More bool           `json:"more"`
	}
	decodeJSON(t, w, &out)
	if len(out.Jobs) != 2 || out.More {
		t.Fatalf("dead jobs: %s", body)
	}
	byKind := map[string]adminJobJSON{}
	for _, j := range out.Jobs {
		byKind[j.Kind] = j
	}
	if j := byKind["occurrences.extend"]; j.Label != "Adding upcoming event dates" || !j.RunsDaily || j.Attempts != 5 || j.LastError != "connection refused" {
		t.Fatalf("extend: %+v", j)
	}
	if j := byKind["mystery.kind"]; j.Label != "mystery.kind" || j.RunsDaily {
		t.Fatalf("an unknown kind is named by its kind: %+v", j)
	}
	// The payload can name people and never reaches the page.
	if strings.Contains(body, "secret@example.com") || strings.Contains(body, "payload") {
		t.Fatalf("payload leaked: %s", body)
	}
	var raw struct {
		Jobs []map[string]any `json:"jobs"`
	}
	json.Unmarshal([]byte(body), &raw)
	keys := []string{}
	for k := range raw.Jobs[0] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != "attempts,failedAt,id,kind,label,lastError,runsDaily" {
		t.Fatalf("a job carries %v", keys)
	}
}

func TestAdminJobsDeliverySummary(t *testing.T) {
	app := testApp(t)
	u := newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	newTestUser(t, app, "member", "password1234", models.RoleMember)
	admin := loginAPI(t, app, "admin", "password1234")
	member := loginAPI(t, app, "member", "password1234")
	admin.holdNotices, member.holdNotices = true, true
	readSummary := func() adminDeliverySummaryJSON {
		t.Helper()
		w := admin.do("GET", "/api/v1/admin/jobs", nil, false)
		var out struct {
			Deliveries adminDeliverySummaryJSON `json:"deliveries"`
		}
		decodeJSON(t, w, &out)
		var raw struct {
			Deliveries map[string]any `json:"deliveries"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
			t.Fatal(err)
		}
		keys := []string{}
		for k := range raw.Deliveries {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if strings.Join(keys, ",") != "deferred,failed,oldestQueuedAt,queued" {
			t.Fatalf("delivery summary carries %v", keys)
		}
		return out.Deliveries
	}
	if got := readSummary(); got != (adminDeliverySummaryJSON{}) {
		t.Fatalf("empty summary: %+v", got)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	oldest := now.Add(-10 * 24 * time.Hour)
	for _, channel := range []string{models.DeliveryEmail, models.DeliveryPush} {
		for _, row := range []struct {
			status  string
			created time.Time
			updated time.Time
		}{
			{models.DeliveryQueued, oldest, oldest},
			{models.DeliveryQueued, now.Add(-time.Hour), now},
			{models.DeliveryDeferred, oldest.Add(-time.Hour), oldest},
			{models.DeliveryFailed, oldest, now},
			{models.DeliveryFailed, now.Add(-6 * 24 * time.Hour), now.Add(-6 * 24 * time.Hour)},
			{models.DeliveryFailed, oldest, now.Add(-8 * 24 * time.Hour)},
			{models.DeliverySent, oldest, now},
			{models.DeliverySuppressed, oldest, now},
		} {
			id, _ := seedNotifyDeliver(t, app, u, 1)
			created := row.created
			if channel == models.DeliveryPush {
				created = created.Add(-24 * time.Hour)
			}
			if err := app.DB.Model(&models.NotificationDelivery{}).Where("notification_id = ?", id).
				Updates(map[string]any{"channel": channel, "status": row.status,
					"created_at": created, "updated_at": row.updated, "reason": "private@example.com delivery error"}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	got := readSummary()
	if got.Queued != 2 || got.Deferred != 1 || got.Failed != 2 || got.OldestQueuedAt == nil || !got.OldestQueuedAt.Equal(oldest) {
		t.Fatalf("delivery summary: %+v, want queued 2, deferred 1, failed 2, oldest %v", got, oldest)
	}
	if w := member.do("GET", "/api/v1/admin/jobs", nil, false); w.Code != http.StatusForbidden {
		t.Fatalf("member: %d %s", w.Code, w.Body.String())
	}
	if err := app.DB.Model(&models.NotificationDelivery{}).Where("channel = ? AND status = ?", models.DeliveryEmail, models.DeliveryQueued).
		Update("status", models.DeliverySent).Error; err != nil {
		t.Fatal(err)
	}
	if got := readSummary(); got.Queued != 0 || got.OldestQueuedAt != nil {
		t.Fatalf("no queued emails: %+v", got)
	}
}

func TestRetryAndDismissDeadJobs(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	retry := deadJob(t, app, "occurrences.extend", "a", "boom")
	dismiss := deadJob(t, app, "contact.sweep", "b", "boom")

	if w := admin.do("POST", "/api/v1/admin/jobs/"+itoa(retry.ID)+"/retry", nil, true); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"queued"`) {
		t.Fatalf("retry: %d %s", w.Code, w.Body.String())
	}
	var j models.Job
	app.DB.First(&j, retry.ID)
	if j.Status != models.JobQueued || j.Attempts != 0 || time.Until(j.RunAt) > time.Second {
		t.Fatalf("retried job: %+v", j)
	}
	if w := admin.do("POST", "/api/v1/admin/jobs/"+itoa(dismiss.ID)+"/dismiss", nil, true); w.Code != http.StatusOK {
		t.Fatalf("dismiss: %d %s", w.Code, w.Body.String())
	}
	var d models.Job
	app.DB.First(&d, dismiss.ID)
	if d.Status != models.JobDismissed {
		t.Fatalf("dismissed job: %+v", d)
	}
	if auditCount(t, app, audit.JobRetried) != 1 || auditCount(t, app, audit.JobDismissed) != 1 {
		t.Fatal("retry and dismiss should be audited")
	}
	// Handling it again: 409 with its code; a missing job: 404.
	for _, path := range []string{"/retry", "/dismiss"} {
		w := admin.do("POST", "/api/v1/admin/jobs/"+itoa(retry.ID)+path, nil, true)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"job_handled"`) {
			t.Fatalf("again %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	if w := admin.do("POST", "/api/v1/admin/jobs/99999/retry", nil, true); w.Code != http.StatusNotFound {
		t.Fatalf("missing: %d", w.Code)
	}
	if auditCount(t, app, audit.JobRetried) != 1 {
		t.Fatal("a refused retry writes no audit row")
	}
	// The dismissed job is cleaned up with the finished ones.
	app.DB.Model(&models.Job{}).Where("id = ?", dismiss.ID).Update("updated_at", time.Now().AddDate(0, 0, -31))
	if n, _ := jobs.PruneDone(app.DB, time.Now().Add(-doneJobsKept)); n != 1 {
		t.Fatalf("pruned %d", n)
	}
}

// Every daily task has a plain label for the System page.
func TestDailyJobsHaveLabels(t *testing.T) {
	app := &App{}
	for _, k := range app.dailyKinds() {
		if k.Label == "" || !k.Daily || strings.Contains(k.Label, ".") {
			t.Errorf("%s: label %q daily %v", k.Name, k.Label, k.Daily)
		}
	}
}

// Only a dead job can be retried or dismissed: running, queued and failed
// ones answer 409; the list stops at 100 and says there are more.
func TestSystemPageEdges(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	for _, status := range []string{models.JobRunning, models.JobQueued, models.JobFailed} {
		j := deadJob(t, app, "contact.sweep", "edge:"+status, "boom")
		app.DB.Model(&j).Update("status", status)
		if w := admin.do("POST", "/api/v1/admin/jobs/"+itoa(j.ID)+"/retry", nil, true); w.Code != http.StatusConflict {
			t.Fatalf("retry a %s job: %d", status, w.Code)
		}
	}
	app.DB.Exec(`INSERT INTO jobs (kind, status, attempts, max_attempts, last_error)
		SELECT 'contact.sweep', 'dead', 5, 5, 'boom' FROM generate_series(1, 101)`)
	var out struct {
		Jobs []adminJobJSON `json:"jobs"`
		More bool           `json:"more"`
	}
	decodeJSON(t, admin.do("GET", "/api/v1/admin/jobs", nil, false), &out)
	if len(out.Jobs) != maxDeadJobsListed || !out.More {
		t.Fatalf("listed %d, more %v", len(out.Jobs), out.More)
	}
}

const (
	deadAlertKind    = "test.dead_alert"
	deadAlertSecret  = "payload-secret-7731"
	deadAlertErrText = "backend refused sam@example.com with key eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig"
)

// deadAlertKinds is one generic job that always fails; the payload and the
// error text carry things an alert must never repeat.
func deadAlertKinds() []jobs.Kind {
	return []jobs.Kind{{Type: jobs.Type{Name: deadAlertKind, MaxAttempts: 1},
		Run: func(context.Context, json.RawMessage) error { return errors.New(deadAlertErrText) }}}
}

// killDeadAlertJob queues the failing job and runs it once through the
// app's runner, so it dies and OnDead fires.
func killDeadAlertJob(t *testing.T, app *App) uint {
	t.Helper()
	if _, err := enqueueJob(app.DB, deadAlertKind, map[string]string{"note": deadAlertSecret}, time.Now(), "", 1); err != nil {
		t.Fatal(err)
	}
	var job models.Job
	if err := app.DB.Where("kind = ?", deadAlertKind).Order("id DESC").First(&job).Error; err != nil {
		t.Fatal(err)
	}
	runDeadAlertJob(t, app)
	if got := reloadJob(t, app, job.ID); got.Status != models.JobDead {
		t.Fatalf("job %+v", got)
	}
	return job.ID
}

func runDeadAlertJob(t *testing.T, app *App) {
	t.Helper()
	ran, err := app.newRunner(deadAlertKinds()...).RunOnce(context.Background(), "test:dead")
	if err != nil || !ran {
		t.Fatalf("run: ran %v err %v", ran, err)
	}
}

func deadAlerts(t *testing.T, app *App, jobID uint) []models.Notification {
	t.Helper()
	var rows []models.Notification
	if err := app.DB.Where("kind = ? AND subject_id = ?", notify.SystemJobDead, jobID).Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

// wantDeadAlerts checks that every admin has exactly n copies for the job,
// open or resolved as asked, and that no copy repeats the payload or error.
// resolution: "open" means every copy is unresolved, a word means every copy is
// resolved (the caller checks which), "" means don't check.
func wantDeadAlerts(t *testing.T, app *App, jobID uint, admins []uint, n int, resolution string) {
	t.Helper()
	rows := deadAlerts(t, app, jobID)
	if len(rows) != n*len(admins) {
		t.Fatalf("alerts for job %d: %d, want %d", jobID, len(rows), n*len(admins))
	}
	per := map[uint]int{}
	for _, r := range rows {
		per[r.UserID]++
		text := r.Title + "|" + r.Body + "|" + r.EmailTitle + "|" + r.EmailBody + "|" + r.PushTitle + "|" + r.PushBody
		for _, bad := range []string{deadAlertSecret, "sam@example.com", "eyJ", "refused"} {
			if strings.Contains(text, bad) {
				t.Fatalf("alert repeats %q: %q", bad, text)
			}
		}
		if !r.Waiting || r.Link != "/admin?tab=system" || !strings.HasPrefix(r.Title, "A background task stopped: "+deadAlertKind) ||
			!strings.Contains(r.Body, "after 1 tries") {
			t.Fatalf("alert %+v", r)
		}
	}
	for _, id := range admins {
		if per[id] != n {
			t.Fatalf("admin %d has %d alerts, want %d", id, per[id], n)
		}
	}
	for _, r := range rows {
		if resolution != "" && resolution != "open" && r.ResolvedAt == nil {
			t.Fatalf("alert %d is still open", r.ID)
		}
	}
}

func TestDeadJobAlertFiresOnceAndResolves(t *testing.T) {
	for _, decision := range []string{"retry", "dismiss"} {
		t.Run(decision, func(t *testing.T) {
			app := testApp(t)
			a1 := newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
			a2 := newTestUser(t, app, "admin2", "password1234", models.RoleAdmin)
			newTestUser(t, app, "pat", "password123456", models.RoleMember)
			admins := []uint{a1.ID, a2.ID}
			admin := loginAPI(t, app, "admin", "password1234")
			admin.holdNotices = true

			id := killDeadAlertJob(t, app)
			wantDeadAlerts(t, app, id, admins, 1, "open")
			for _, r := range deadAlerts(t, app, id) {
				if r.ResolvedAt != nil || r.SubjectType != "job" {
					t.Fatalf("alert %+v", r)
				}
			}
			var n int64
			if err := app.DB.Model(&models.Job{}).Where("kind = ?", notify.DeliverKind).Count(&n).Error; err != nil || n != 0 {
				t.Fatalf("an alert queued delivery: %d %v", n, err)
			}
			var deliveries int64
			app.DB.Model(&models.NotificationDelivery{}).Count(&deliveries)
			if deliveries != 0 {
				t.Fatalf("alert wrote %d delivery rows", deliveries)
			}

			if w := admin.do("POST", "/api/v1/admin/jobs/"+strconv.Itoa(int(id))+"/"+decision, nil, true); w.Code != http.StatusOK {
				t.Fatalf("%s: %d %s", decision, w.Code, w.Body.String())
			}
			want, action := "retried", audit.JobRetried
			if decision == "dismiss" {
				want, action = "dismissed", audit.JobDismissed
			}
			wantDeadAlerts(t, app, id, admins, 1, want)
			for _, r := range deadAlerts(t, app, id) {
				if r.Resolution != want || r.ResolvedByID == nil || *r.ResolvedByID != a1.ID {
					t.Fatalf("resolution %+v", r)
				}
			}
			if auditCount(t, app, action) != 1 {
				t.Fatal("decision should be audited")
			}

			if decision == "retry" {
				// It dies again: a new death earns a new alert.
				runDeadAlertJob(t, app)
				wantDeadAlerts(t, app, id, admins, 2, "")
				var open int64
				app.DB.Model(&models.Notification{}).Where("kind = ? AND subject_id = ? AND resolved_at IS NULL", notify.SystemJobDead, id).Count(&open)
				if open != int64(len(admins)) {
					t.Fatalf("open alerts %d", open)
				}
			}
		})
	}
}

// alertDeadJob is called after the dead UPDATE committed, so a decision can
// get in first; it must follow the committed status, never its own memory.
func TestDeadJobAlertRaces(t *testing.T) {
	setup := func(t *testing.T) (*App, uint, []uint) {
		app := testApp(t)
		a1 := newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
		a2 := newTestUser(t, app, "admin2", "password1234", models.RoleAdmin)
		app.DB.Model(&models.Job{}).Where("true").Delete(&models.Job{})
		if _, err := enqueueJob(app.DB, deadAlertKind, nil, time.Now(), "", 1); err != nil {
			t.Fatal(err)
		}
		var job models.Job
		if err := app.DB.Where("kind = ?", deadAlertKind).First(&job).Error; err != nil {
			t.Fatal(err)
		}
		if err := app.DB.Model(&job).Updates(map[string]any{"status": models.JobDead, "attempts": 1}).Error; err != nil {
			t.Fatal(err)
		}
		return app, job.ID, []uint{a1.ID, a2.ID}
	}

	for _, decision := range []string{"retry", "dismiss"} {
		t.Run("after "+decision+" writes nothing", func(t *testing.T) {
			app, id, _ := setup(t)
			admin := loginAPI(t, app, "admin", "password1234")
			admin.holdNotices = true
			if w := admin.do("POST", "/api/v1/admin/jobs/"+strconv.Itoa(int(id))+"/"+decision, nil, true); w.Code != http.StatusOK {
				t.Fatalf("%s: %d %s", decision, w.Code, w.Body.String())
			}
			app.alertDeadJob(id, deadAlertKind, 1)
			if rows := deadAlerts(t, app, id); len(rows) != 0 {
				t.Fatalf("alerts for a handled job: %+v", rows)
			}
		})
	}

	t.Run("twice on one death leaves one alert per admin", func(t *testing.T) {
		app, id, admins := setup(t)
		app.alertDeadJob(id, deadAlertKind, 1)
		app.alertDeadJob(id, deadAlertKind, 1)
		if rows := deadAlerts(t, app, id); len(rows) != len(admins) {
			t.Fatalf("alerts %d, want %d", len(rows), len(admins))
		}
	})

	// A decision that holds the job row decides what the callback does; the
	// callback waits for it rather than reading the stale dead status.
	for _, tc := range []struct {
		name   string
		status string
		alerts int
	}{{"waits then sees the dismissal", models.JobDismissed, 0}, {"waits then alerts when still dead", models.JobDead, 2}} {
		t.Run(tc.name, func(t *testing.T) {
			app, id, _ := setup(t)
			tx := app.DB.Begin()
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			defer tx.Rollback()
			if err := tx.Exec("SELECT 1 FROM jobs WHERE id = ? FOR UPDATE", id).Error; err != nil {
				t.Fatal(err)
			}
			if err := tx.Model(&models.Job{}).Where("id = ?", id).Update("status", tc.status).Error; err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() { app.alertDeadJob(id, deadAlertKind, 1); close(done) }()
			// It is blocked on the job row: another session shows as waiting
			// on a lock, and a session with a short lock_timeout cannot take it.
			deadline := time.Now().Add(5 * time.Second)
			for {
				var waiting int64
				app.DB.Raw(`SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND query LIKE '%FROM jobs WHERE id%FOR UPDATE%'`).Scan(&waiting)
				if waiting == 1 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("alertDeadJob never waited on the job row")
				}
				time.Sleep(10 * time.Millisecond)
			}
			select {
			case <-done:
				t.Fatal("alertDeadJob finished while the job row was locked")
			default:
			}
			probe := app.DB.Begin()
			if err := probe.Exec("SET LOCAL lock_timeout = '50ms'").Error; err != nil {
				t.Fatal(err)
			}
			if err := probe.Exec("SELECT 1 FROM jobs WHERE id = ? FOR UPDATE", id).Error; err == nil {
				t.Fatal("the job row was not locked")
			}
			probe.Rollback()
			if err := tx.Commit().Error; err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("alertDeadJob did not finish after the lock was released")
			}
			if rows := deadAlerts(t, app, id); len(rows) != tc.alerts {
				t.Fatalf("alerts %d, want %d", len(rows), tc.alerts)
			}
		})
	}
}

func TestEveryRegisteredJobKindHasSystemLabel(t *testing.T) {
	a := &App{}
	registered := append(append(a.dailyKinds(), a.hourlyKinds()...), a.backgroundKinds()...)
	kinds := a.jobKinds()
	seen := map[string]bool{}
	for _, k := range registered {
		if seen[k.Name] {
			t.Errorf("duplicate registered kind %q", k.Name)
		}
		seen[k.Name] = true
		if got, ok := kinds[k.Name]; !ok || strings.TrimSpace(got.Label) == "" || got.Run == nil {
			t.Errorf("kind %q missing from System registry or without label/handler", k.Name)
		}
	}
	for _, name := range []string{kindTombstoneSweep, kindTombstonePage, kindTombstone} {
		if !seen[name] {
			t.Errorf("erasure kind %q is not registered", name)
		}
	}
}

func TestTombstoneDeadJobShowsItsLabel(t *testing.T) {
	a := testApp(t)
	newTestUser(t, a, "tombstone-admin", erasurePW, models.RoleAdmin)
	admin := loginAPI(t, a, "tombstone-admin", erasurePW)
	admin.holdNotices = true
	deadJob(t, a, kindTombstone, "tombstone-dead", "could not finish")
	var out struct {
		Jobs []adminJobJSON `json:"jobs"`
	}
	decodeJSON(t, admin.do("GET", "/api/v1/admin/jobs", nil, false), &out)
	if len(out.Jobs) != 1 || out.Jobs[0].Kind != kindTombstone || out.Jobs[0].Label != "Finishing the erasure of a deleted account" {
		t.Fatalf("dead erasure job: %+v", out.Jobs)
	}
}
