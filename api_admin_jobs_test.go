package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
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
	if _, err := jobs.Enqueue(app.DB, kind, map[string]string{"email": "secret@example.com"}, time.Now(), key, 1); err != nil {
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
	jobs.Enqueue(app.DB, "accounts.age_rollover", nil, time.Now(), "alive", 0) // queued, not listed

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
