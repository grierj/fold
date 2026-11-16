package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fold/models"
)

func TestAdminBackupStatusAccess(t *testing.T) {
	app := testApp(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "LAST_OK"), []byte(stampOf(time.Now().Add(-time.Hour))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	app.Config.BackupDir = dir
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	newTestUser(t, app, "member", "password1234", models.RoleMember)
	leader := newTestUser(t, app, "leader", "password1234", models.RoleMember)
	newTestGroup(t, app, leader, "Leaders' group", models.GroupActive)

	anon := newAPIClient(t, app)
	w := anon.do("GET", "/api/v1/admin/backup", nil, false)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d %s", w.Code, w.Body.String())
	}
	for _, name := range []string{"member", "leader"} {
		w := loginAPI(t, app, name, "password1234").do("GET", "/api/v1/admin/backup", nil, false)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "state") {
			t.Fatalf("%s got status: %s", name, w.Body.String())
		}
	}

	w = loginAPI(t, app, "admin", "password1234").do("GET", "/api/v1/admin/backup", nil, false)
	if w.Code != http.StatusOK {
		t.Fatalf("admin: %d %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	var st backupStatusJSON
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil || st.State != backupOK || st.LastOkAt == nil {
		t.Fatalf("admin body: %v %s", err, w.Body.String())
	}
	if strings.Contains(w.Body.String(), dir) {
		t.Fatalf("answer names the directory: %s", w.Body.String())
	}
}

func TestAdminBackupStatusEveryFileCondition(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	now := time.Now().UTC().Truncate(time.Second)
	app.clock = func() time.Time { return now }
	good := stampOf(now.Add(-time.Hour))

	for name, tc := range map[string]struct {
		dir   func(t *testing.T) string
		state string
	}{
		"unwatched": {func(*testing.T) string { return "" }, backupUnwatched},
		"missing":   {func(t *testing.T) string { return t.TempDir() }, backupMissing},
		"ok":        {func(t *testing.T) string { return backupDirWith(t, map[string]string{"LAST_OK": good + "\n"}) }, backupOK},
		"stale": {func(t *testing.T) string {
			return backupDirWith(t, map[string]string{"LAST_OK": stampOf(now.Add(-40*time.Hour)) + "\n"})
		}, backupStale},
		"unreadable":        {func(t *testing.T) string { return backupDirWith(t, map[string]string{"LAST_OK": backupSentinel}) }, backupUnreadable},
		"no such directory": {func(t *testing.T) string { return filepath.Join(t.TempDir(), "gone") }, backupUnreadable},
	} {
		t.Run(name, func(t *testing.T) {
			dir := tc.dir(t)
			app.Config.BackupDir = dir
			w := admin.do("GET", "/api/v1/admin/backup", nil, false)
			if w.Code != http.StatusOK {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			var raw map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
				t.Fatal(err)
			}
			if raw["state"] != tc.state || len(raw) != 7 {
				t.Fatalf("body %s", w.Body.String())
			}
			for _, bad := range []string{"SENTINEL", "s3.example"} {
				if strings.Contains(w.Body.String(), bad) {
					t.Fatalf("body leaks %q: %s", bad, w.Body.String())
				}
			}
			if dir != "" && strings.Contains(w.Body.String(), dir) {
				t.Fatalf("body names the directory: %s", w.Body.String())
			}
		})
	}
}

// The route needs a session even before the capability is asked about. An
// installation with no user yet redirects every request to /setup, so one
// member exists and the 401 is the session check's own answer.
func TestAdminBackupRouteIsNotPublic(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "member", "password1234", models.RoleMember)
	app.Config.BackupDir = t.TempDir()
	rec := httptest.NewRecorder()
	app.setupRouter().ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/admin/backup", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d", rec.Code)
	}
}
