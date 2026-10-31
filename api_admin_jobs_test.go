package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"fold/internal/audit"
	"fold/internal/jobs"
	"fold/models"
)

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
