package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"fold/internal/jobs"
	"fold/models"
	"fold/storage"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// ---------------------------------------------------------------------------
// Helpers shared with api_profile_test.go and api_admin_test.go
// ---------------------------------------------------------------------------

// avatarBarrierStore wraps *storage.LocalStore so a test can count Put calls,
// hold one open, or hold or fail CleanOwner. Open is promoted, so GET
// /uploads keeps serving while it is installed.
type avatarBarrierStore struct {
	*storage.LocalStore

	mu           sync.Mutex
	puts         int
	putEntered   chan struct{} // signalled (never blocking) when Put starts
	putGate      chan struct{} // when non-nil, Put waits for it to close
	cleanCalls   int
	cleanEntered chan struct{}
	cleanGate    chan struct{} // when non-nil, CleanOwner waits for it to close
	cleanIgnores bool          // CleanOwner keeps waiting even after ctx ends
	cleanErr     error         // when non-nil, CleanOwner returns it
}

func pingChan(ch chan struct{}) {
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (s *avatarBarrierStore) Put(ctx context.Context, key string, src io.Reader, size int64, ct string) error {
	s.mu.Lock()
	s.puts++
	gate := s.putGate
	s.mu.Unlock()
	pingChan(s.putEntered)
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.LocalStore.Put(ctx, key, src, size, ct)
}

func (s *avatarBarrierStore) CleanOwner(ctx context.Context, owner storage.Owner, keep string) error {
	s.mu.Lock()
	s.cleanCalls++
	gate, ignores, crafted := s.cleanGate, s.cleanIgnores, s.cleanErr
	s.mu.Unlock()
	pingChan(s.cleanEntered)
	if crafted != nil {
		return crafted
	}
	if gate != nil {
		if ignores {
			<-gate
		} else {
			select {
			case <-gate:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return s.LocalStore.CleanOwner(ctx, owner, keep)
}

// forgetSignals drops entry signals left by earlier calls, so the next
// awaitSignal waits for a new one.
func (s *avatarBarrierStore) forgetSignals() {
	for _, ch := range []chan struct{}{s.putEntered, s.cleanEntered} {
		for drained := false; !drained; {
			select {
			case <-ch:
			default:
				drained = true
			}
		}
	}
}

func (s *avatarBarrierStore) putCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.puts
}

// installAvatarBarrier swaps the app's store for a barrier wrapper.
func installAvatarBarrier(app *App) *avatarBarrierStore {
	s := &avatarBarrierStore{LocalStore: app.Uploads.(*storage.LocalStore),
		putEntered: make(chan struct{}, 8), cleanEntered: make(chan struct{}, 8)}
	app.Uploads = s
	return s
}

func awaitSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("the store call was never reached")
	}
}

func avatarRootDir(app *App) string {
	switch s := app.Uploads.(type) {
	case *storage.LocalStore:
		return s.Dir
	case *avatarBarrierStore:
		return s.Dir
	}
	panic("unexpected upload store")
}

// avatarFiles lists the names in avatars/<uid>/, nil when the folder is gone.
func avatarFiles(t *testing.T, app *App, uid uint) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(avatarRootDir(app), "avatars", itoa(uid)))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func avatarKeyOf(t *testing.T, app *App, uid uint) string {
	t.Helper()
	var key string
	if err := app.DB.Raw("SELECT COALESCE(avatar_key, '') FROM users WHERE id = ?", uid).Scan(&key).Error; err != nil {
		t.Fatal(err)
	}
	return key
}

func avatarJobsFor(t *testing.T, app *App, uid uint) []models.Job {
	t.Helper()
	var js []models.Job
	if err := app.DB.Where("kind = ? AND payload->>'userId' = ?", kindAvatarCleanup, itoa(uid)).Order("id").Find(&js).Error; err != nil {
		t.Fatal(err)
	}
	return js
}

func avatarTestKinds(app *App) []jobs.Kind {
	return append(app.avatarKinds(), app.dailyKinds()...)
}

// runAvatarJobs runs every due job of r until none is due.
func runAvatarJobs(t *testing.T, r *jobs.Runner) {
	t.Helper()
	for i := 0; i < 200; i++ {
		ran, err := r.RunOnce(context.Background(), "test:avatar")
		if err != nil {
			t.Fatal(err)
		}
		if !ran {
			return
		}
	}
	t.Fatal("avatar jobs never drained")
}

// drainAvatarJobs runs every due avatar.cleanup and avatar.sweep job.
func drainAvatarJobs(t *testing.T, app *App) {
	t.Helper()
	runAvatarJobs(t, jobs.NewRunner(app.DB, avatarTestKinds(app)...))
}

// drainAvatarJobsAlerting is drainAvatarJobs through the app's own runner,
// so a job that dies tells the admins (OnDead).
func drainAvatarJobsAlerting(t *testing.T, app *App) {
	t.Helper()
	runAvatarJobs(t, app.newRunner(avatarTestKinds(app)...))
}

// limitAvatarJobAttempts gives every queued avatar job one attempt, so its
// first failure is also its last.
func limitAvatarJobAttempts(t *testing.T, app *App) {
	t.Helper()
	if err := app.DB.Model(&models.Job{}).Where("kind = ?", kindAvatarCleanup).Update("max_attempts", 1).Error; err != nil {
		t.Fatal(err)
	}
}

// queueCleanup queues avatar.cleanup the way a request does.
func queueCleanup(t *testing.T, app *App, uid uint) {
	t.Helper()
	if err := app.DB.Transaction(func(tx *gorm.DB) error { return queueAvatarCleanup(tx, uid) }); err != nil {
		t.Fatal(err)
	}
}

// assertAvatarPayloads checks that every avatar.cleanup row holds exactly
// {"userId": N} with N > 0: no key, no path, no name.
func assertAvatarPayloads(t *testing.T, app *App) {
	t.Helper()
	var js []models.Job
	if err := app.DB.Where("kind = ?", kindAvatarCleanup).Find(&js).Error; err != nil {
		t.Fatal(err)
	}
	for _, j := range js {
		var p map[string]json.RawMessage
		if err := json.Unmarshal([]byte(j.Payload), &p); err != nil || len(p) != 1 {
			t.Fatalf("job %d payload %q", j.ID, j.Payload)
		}
		id, err := strconv.ParseUint(string(p["userId"]), 10, 64)
		if err != nil || id == 0 {
			t.Fatalf("job %d payload %q", j.ID, j.Payload)
		}
	}
}

// assertNoAvatarLeak checks that text names neither the upload directory,
// any avatars/ key, nor the given secrets (a key, a random file name).
func assertNoAvatarLeak(t *testing.T, app *App, what, text string, secrets ...string) {
	t.Helper()
	for _, leak := range append([]string{avatarRootDir(app), "avatars/"}, secrets...) {
		if leak != "" && strings.Contains(text, leak) {
			t.Errorf("%s leaks %q: %s", what, leak, text)
		}
	}
}

func auditTotal(t *testing.T, app *App) int64 {
	t.Helper()
	var n int64
	if err := app.DB.Model(&models.AuditLog{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func readAvatarFile(t *testing.T, app *App, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(avatarRootDir(app), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// bulkTombstones inserts soft-deleted users with ids from..to and a folder
// holding one file for each.
func bulkTombstones(t *testing.T, app *App, from, to int) {
	t.Helper()
	if err := app.DB.Exec(`INSERT INTO users (id, email, deleted_at)
		SELECT g, 'bulk-' || g || '@example.invalid', now() FROM generate_series(?::int, ?::int) AS g`, from, to).Error; err != nil {
		t.Fatal(err)
	}
	for id := from; id <= to; id++ {
		writeUploadFile(t, avatarRootDir(app), fmt.Sprintf("avatars/%d/a.png", id), "x")
	}
}

func isLater(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "not due until ")
}

// waitForLockedUsersQuery waits until a backend is blocked on a lock while
// running a statement that matches the LIKE pattern, and returns its pid.
func waitForLockedUsersQuery(t *testing.T, app *App, like string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var pid int
		if err := app.DB.Raw(`SELECT coalesce(min(pid), 0) FROM pg_stat_activity WHERE datname = current_database()
			AND wait_event_type = 'Lock' AND state = 'active' AND query LIKE ?`, like).Scan(&pid).Error; err != nil {
			t.Fatal(err)
		}
		if pid != 0 {
			return pid
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no backend ever waited on %s", like)
	return 0
}

// waitForRowLockWaiter waits until an erasure (users ... FOR UPDATE) is
// blocked on a lock and returns its pid.
func waitForRowLockWaiter(t *testing.T, app *App) int {
	t.Helper()
	return waitForLockedUsersQuery(t, app, "%users%FOR UPDATE%")
}

// advisoryLocksHeldBy counts the advisory locks a backend holds.
func advisoryLocksHeldBy(t *testing.T, app *App, pid int) int {
	t.Helper()
	var n int
	if err := app.DB.Raw("SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND granted AND pid = ?", pid).Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

// avatarUser makes a user whose folder holds the current file and the given
// extra files, and whose avatar_key names the current one.
func avatarUser(t *testing.T, app *App, name string, extra ...string) (*models.User, string) {
	t.Helper()
	u := newTestUser(t, app, name, "pw", models.RoleMember)
	cur := fmt.Sprintf("avatars/%d/current.png", u.ID)
	writeUploadFile(t, avatarRootDir(app), cur, "current bytes")
	for _, e := range extra {
		writeUploadFile(t, avatarRootDir(app), fmt.Sprintf("avatars/%d/%s", u.ID, e), "old bytes")
	}
	if err := app.DB.Exec("UPDATE users SET avatar_key = ? WHERE id = ?", cur, u.ID).Error; err != nil {
		t.Fatal(err)
	}
	return u, cur
}

func tombstone(t *testing.T, app *App, uid uint) {
	t.Helper()
	if err := app.DB.Exec("UPDATE users SET deleted_at = now() WHERE id = ?", uid).Error; err != nil {
		t.Fatal(err)
	}
}

// installJobTrigger makes inserts into jobs of kind avatar.cleanup fail with
// a message that holds a path, a key and an id. It is dropped at cleanup.
func installRejectAvatarJobs(t *testing.T, app *App) {
	t.Helper()
	for _, q := range []string{
		`CREATE FUNCTION fold_test_reject_avatar_jobs() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN
				IF NEW.kind = 'avatar.cleanup' THEN
					RAISE EXCEPTION 'cannot write /srv/uploads/avatars/7/secret-key.png for user 7';
				END IF;
				RETURN NEW;
			END $$`,
		`CREATE TRIGGER fold_test_reject_avatar_jobs BEFORE INSERT ON jobs FOR EACH ROW EXECUTE FUNCTION fold_test_reject_avatar_jobs()`,
	} {
		if err := app.DB.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		app.DB.Exec("DROP TRIGGER IF EXISTS fold_test_reject_avatar_jobs ON jobs")
		app.DB.Exec("DROP FUNCTION IF EXISTS fold_test_reject_avatar_jobs()")
	})
}

// captureLog collects the standard logger's output with no timestamp, so a
// test can search the text for names and numbers.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	flags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(os.Stderr); log.SetFlags(flags) })
	return &buf
}

// ---------------------------------------------------------------------------
// The job and its queueing
// ---------------------------------------------------------------------------

func TestAvatarCleanupJobRejectsBadPayloads(t *testing.T) {
	app := testApp(t)
	run := app.avatarKinds()[0].Run
	for _, p := range []string{``, `{}`, `null`, `[]`, `{"userId":0}`, `{"userId":"abc"}`, `{"userId":-1}`,
		`{"userId":5,"key":"avatars/5/x.png"}`, `{"userId":5} {"userId":6}`} {
		if err := run(context.Background(), json.RawMessage(p)); err != errAvatarCleanupPayload {
			t.Errorf("payload %q: got %v, want the fixed payload error", p, err)
		}
	}
}

func TestQueueAvatarCleanupCoalescesOnlyQueuedWork(t *testing.T) {
	app := testApp(t)
	u := newTestUser(t, app, "pat", "pw", models.RoleMember)
	queue := func() {
		t.Helper()
		if err := app.DB.Transaction(func(tx *gorm.DB) error { return queueAvatarCleanup(tx, u.ID) }); err != nil {
			t.Fatal(err)
		}
	}
	queue()
	queue()
	js := avatarJobsFor(t, app, u.ID)
	if len(js) != 1 || js[0].UniqueKey != nil || js[0].Status != models.JobQueued {
		t.Fatalf("two removals with no runner: %+v", js)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(js[0].Payload), &payload); err != nil || len(payload) != 1 || payload["userId"] != float64(u.ID) {
		t.Fatalf("payload %s", js[0].Payload)
	}
	for _, st := range []string{models.JobRunning, models.JobFailed, models.JobDone, models.JobDead, models.JobDismissed} {
		before := avatarJobsFor(t, app, u.ID)
		last := before[len(before)-1]
		if err := app.DB.Model(&models.Job{}).Where("id = ?", last.ID).Update("status", st).Error; err != nil {
			t.Fatal(err)
		}
		queue()
		queue()
		after := avatarJobsFor(t, app, u.ID)
		if len(after) != len(before)+1 || after[len(after)-1].Status != models.JobQueued || after[len(after)-1].UniqueKey != nil {
			t.Fatalf("after a %s row: %d rows, want %d with a fresh queued one", st, len(after), len(before)+1)
		}
	}
	// A dead row holding today's sweep key never blocks normal work.
	key := fmt.Sprintf("avatar.cleanup:%d:2026-11-04", u.ID)
	other := newTestUser(t, app, "sam", "pw", models.RoleMember)
	if err := app.DB.Create(&models.Job{Kind: kindAvatarCleanup, Payload: fmt.Sprintf(`{"userId":%d}`, other.ID),
		Status: models.JobDead, MaxAttempts: 5, UniqueKey: &key}).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Transaction(func(tx *gorm.DB) error { return queueAvatarCleanup(tx, other.ID) }); err != nil {
		t.Fatal(err)
	}
	if js := avatarJobsFor(t, app, other.ID); len(js) != 2 || js[1].Status != models.JobQueued {
		t.Fatalf("after a dead sweep-keyed row: %+v", js)
	}
}

// ---------------------------------------------------------------------------
// The keep rule
// ---------------------------------------------------------------------------

func TestAvatarCleanupMissingAccountTouchesNothing(t *testing.T) {
	app := testApp(t)
	writeUploadFile(t, avatarRootDir(app), "avatars/4242/a.png", "stranger")
	if err := app.runAvatarCleanup(context.Background(), 4242); err != nil {
		t.Fatal(err)
	}
	if got := avatarFiles(t, app, 4242); len(got) != 1 || readAvatarFile(t, app, "avatars/4242/a.png") != "stranger" {
		t.Fatalf("a folder with no account was touched: %v", got)
	}
}

func TestAvatarCleanupKeepsCurrentPhotoByDeletedAtAlone(t *testing.T) {
	for _, status := range []string{models.UserActive, models.UserDisabled, models.UserInvited, models.UserGuest, models.UserPendingConsent} {
		t.Run(status, func(t *testing.T) {
			app := testApp(t)
			u, cur := avatarUser(t, app, "pat", "old.png", ".tmp-remnant")
			if err := app.DB.Exec("UPDATE users SET status = ? WHERE id = ?", status, u.ID).Error; err != nil {
				t.Fatal(err)
			}
			if status == models.UserInvited || status == models.UserGuest {
				if err := app.DB.Exec("UPDATE users SET password_hash = NULL WHERE id = ?", u.ID).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := app.runAvatarCleanup(context.Background(), u.ID); err != nil {
				t.Fatal(err)
			}
			if got := avatarFiles(t, app, u.ID); len(got) != 1 || got[0] != "current.png" {
				t.Fatalf("files %v", got)
			}
			b, err := os.ReadFile(filepath.Join(avatarRootDir(app), filepath.FromSlash(cur)))
			if err != nil || string(b) != "current bytes" {
				t.Fatalf("current file: %q %v", b, err)
			}
			if avatarKeyOf(t, app, u.ID) != cur {
				t.Fatal("the key changed")
			}
			if w := getUpload(t, app, "/uploads/"+cur); w.Code != http.StatusOK {
				t.Fatalf("GET current: %d", w.Code)
			}
		})
	}
}

func TestAvatarCleanupTombstoneKeepsNothing(t *testing.T) {
	app := testApp(t)
	u, _ := avatarUser(t, app, "pat", "old.png")
	tombstone(t, app, u.ID) // a legacy tombstone still holds avatar_key
	if err := app.runAvatarCleanup(context.Background(), u.ID); err != nil {
		t.Fatal(err)
	}
	if got := avatarFiles(t, app, u.ID); got != nil {
		t.Fatalf("a tombstone's folder remains: %v", got)
	}
	if avatarKeyOf(t, app, u.ID) == "" {
		t.Fatal("cleanup scrubbed the row; that is R4-1 and R4-6")
	}
}

func TestAvatarCleanupMalformedKeyFailsAndRemovesNothing(t *testing.T) {
	for name, key := range map[string]string{
		"other owner": "avatars/%d/x.png",
		"unparseable": "not a key",
		"covers":      "covers/%d/x.png",
	} {
		t.Run(name, func(t *testing.T) {
			app := testApp(t)
			admin := newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
			adminC := loginAPI(t, app, "admin", "password1234")
			u, _ := avatarUser(t, app, "pat", "old.png")
			if strings.Contains(key, "%d") {
				id := u.ID
				if name == "other owner" {
					id++
				}
				key = fmt.Sprintf(key, id)
			}
			writeUploadFile(t, avatarRootDir(app), fmt.Sprintf("avatars/%d/x.png", u.ID+1), "someone else")
			writeUploadFile(t, avatarRootDir(app), fmt.Sprintf("covers/%d/x.png", u.ID), "a cover")
			if err := app.DB.Exec("UPDATE users SET avatar_key = ? WHERE id = ?", key, u.ID).Error; err != nil {
				t.Fatal(err)
			}
			if err := app.runAvatarCleanup(context.Background(), u.ID); err != errAvatarKeyForeign {
				t.Fatalf("got %v, want the fixed malformed-key error", err)
			}
			if got := avatarFiles(t, app, u.ID); len(got) != 2 {
				t.Fatalf("files were removed: %v", got)
			}
			if got := avatarFiles(t, app, u.ID+1); len(got) != 1 || readAvatarFile(t, app, fmt.Sprintf("avatars/%d/x.png", u.ID+1)) != "someone else" {
				t.Fatalf("another namespace was touched: %v", got)
			}
			if readAvatarFile(t, app, fmt.Sprintf("covers/%d/x.png", u.ID)) != "a cover" {
				t.Fatal("the covers namespace was touched")
			}
			// Through the runner, after its attempts, the job is dead, says why,
			// tells the admins, and is listed on Admin, System.
			queueCleanup(t, app, u.ID)
			limitAvatarJobAttempts(t, app)
			drainAvatarJobsAlerting(t, app)
			js := avatarJobsFor(t, app, u.ID)
			if len(js) != 1 || js[0].Status != models.JobDead || js[0].LastError != errAvatarKeyForeign.Error() {
				t.Fatalf("job %+v", js)
			}
			if got := avatarFiles(t, app, u.ID); len(got) != 2 {
				t.Fatalf("files were removed after the runner: %v", got)
			}
			var listed struct {
				Jobs []adminJobJSON `json:"jobs"`
			}
			decodeJSON(t, adminC.do("GET", "/api/v1/admin/jobs", nil, false), &listed)
			if len(listed.Jobs) != 1 || listed.Jobs[0].ID != js[0].ID || listed.Jobs[0].Label != "Removing old profile photos" ||
				listed.Jobs[0].LastError != errAvatarKeyForeign.Error() {
				t.Fatalf("Admin, System lists %+v", listed.Jobs)
			}
			if n := len(deadAlerts(t, app, js[0].ID)); n != 1 {
				t.Fatalf("%d dead-job notices for admin %d, want 1", n, admin.ID)
			}
		})
	}
}

func TestAvatarCleanupBatchesThroughLater(t *testing.T) {
	t.Run("tombstone with 600 files", func(t *testing.T) {
		app := testApp(t)
		u := newTestUser(t, app, "pat", "pw", models.RoleMember)
		for i := 0; i < 600; i++ {
			writeUploadFile(t, avatarRootDir(app), fmt.Sprintf("avatars/%d/f%03d.png", u.ID, i), "x")
		}
		tombstone(t, app, u.ID)
		for i, wantLater := range []bool{true, true, false} {
			err := app.runAvatarCleanup(context.Background(), u.ID)
			if wantLater != isLater(err) || (!wantLater && err != nil) {
				t.Fatalf("run %d: %v", i+1, err)
			}
		}
		if got := avatarFiles(t, app, u.ID); got != nil {
			t.Fatalf("folder remains: %d files", len(got))
		}
	})
	t.Run("job attempts are not used by Later", func(t *testing.T) {
		app := testApp(t)
		u := newTestUser(t, app, "pat", "pw", models.RoleMember)
		for i := 0; i < 600; i++ {
			writeUploadFile(t, avatarRootDir(app), fmt.Sprintf("avatars/%d/f%03d.png", u.ID, i), "x")
		}
		tombstone(t, app, u.ID)
		if err := app.DB.Transaction(func(tx *gorm.DB) error { return queueAvatarCleanup(tx, u.ID) }); err != nil {
			t.Fatal(err)
		}
		drainAvatarJobs(t, app)
		js := avatarJobsFor(t, app, u.ID)
		if len(js) != 1 || js[0].Status != models.JobDone || js[0].Attempts != 1 {
			t.Fatalf("job %+v", js)
		}
		if avatarFiles(t, app, u.ID) != nil {
			t.Fatal("folder remains")
		}
	})
	t.Run("live owner with 600 orphans", func(t *testing.T) {
		app := testApp(t)
		u, _ := avatarUser(t, app, "pat")
		for i := 0; i < 600; i++ {
			writeUploadFile(t, avatarRootDir(app), fmt.Sprintf("avatars/%d/f%03d.png", u.ID, i), "x")
		}
		for i := 0; i < 6; i++ {
			if err := app.runAvatarCleanup(context.Background(), u.ID); err == nil {
				break
			} else if !isLater(err) {
				t.Fatal(err)
			}
		}
		if got := avatarFiles(t, app, u.ID); len(got) != 1 || got[0] != "current.png" {
			t.Fatalf("files %d", len(got))
		}
	})
}

// ---------------------------------------------------------------------------
// Error mapping
// ---------------------------------------------------------------------------

func TestMapAvatarCleanupError(t *testing.T) {
	live := context.Background()
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	pathErr := &os.PathError{Op: "unlink", Path: "/srv/uploads/avatars/7/deadbeefdeadbeef.png", Err: os.ErrPermission}
	tokenErr := errors.New("7 abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMN")
	for name, tc := range map[string]struct {
		ctx  context.Context
		err  error
		want error
	}{
		"nil":                {live, nil, nil},
		"unsafe":             {live, storage.ErrUnsafeOwner, errAvatarFolderUnsafe},
		"unexpected":         {live, storage.ErrUnexpectedEntry, errAvatarFolderNested},
		"incomplete":         {live, storage.ErrCleanupIncomplete, errAvatarCleanupPartial},
		"foreign key":        {live, errAvatarKeyForeign, errAvatarKeyForeign},
		"lock timeout":       {live, &pgconn.PgError{Code: "55P03", Message: "canceling statement due to lock timeout"}, errAvatarLockTimeout},
		"deadline":           {live, context.DeadlineExceeded, errAvatarOutOfTime},
		"canceled":           {live, context.Canceled, errAvatarOutOfTime},
		"ended context":      {ended, errors.New("commit failed"), errAvatarOutOfTime},
		"path error":         {live, pathErr, errAvatarCleanupFailed},
		"token error":        {live, tokenErr, errAvatarCleanupFailed},
		"wrapped storage":    {live, fmt.Errorf("%w at /srv/uploads", storage.ErrCleanupIncomplete), errAvatarCleanupPartial},
		"other pg error":     {live, &pgconn.PgError{Code: "40001", Message: "serialization failure for user 7"}, errAvatarCleanupFailed},
		"unclassified plain": {live, errors.New("connection reset"), errAvatarCleanupFailed},
	} {
		got := mapAvatarCleanupError(tc.ctx, tc.err)
		if got != tc.want {
			t.Errorf("%s: got %v, want %v", name, got, tc.want)
		}
		if got != nil && (strings.Contains(got.Error(), "/srv") || strings.Contains(got.Error(), "7 ")) {
			t.Errorf("%s: error text leaks: %q", name, got)
		}
	}
	if err := mapAvatarCleanupError(live, storage.ErrMoreRemain); !isLater(err) {
		t.Errorf("more remain: %v, want a Later", err)
	}
}

// A cleanup job's last_error and the runner's log hold the fixed text and
// nothing from the storage error, whatever the store returns.
func TestAvatarCleanupJobErrorsAreFixedText(t *testing.T) {
	for name, crafted := range map[string]error{
		"path error": &os.PathError{Op: "unlink", Path: "/tmp/uploads/avatars/1/abc123.png", Err: os.ErrPermission},
		"id and tok": errors.New("1 abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMN"),
	} {
		t.Run(name, func(t *testing.T) {
			app := testApp(t)
			store := installAvatarBarrier(app)
			store.cleanErr = crafted
			u := newTestUser(t, app, "pat", "pw", models.RoleMember)
			buf := captureLog(t)
			if err := app.DB.Transaction(func(tx *gorm.DB) error { return queueAvatarCleanup(tx, u.ID) }); err != nil {
				t.Fatal(err)
			}
			drainAvatarJobs(t, app)
			js := avatarJobsFor(t, app, u.ID)
			if len(js) != 1 || js[0].Status != models.JobFailed || js[0].LastError != errAvatarCleanupFailed.Error() {
				t.Fatalf("job %+v", js)
			}
			assertNoAvatarLeak(t, app, "runner log", buf.String(), "abc123", "abcdefghijklmnop", crafted.Error())
			assertNoAvatarLeak(t, app, "last_error", js[0].LastError, "abc123", "abcdefghijklmnop", crafted.Error())
			assertAvatarPayloads(t, app)
			if !strings.Contains(buf.String(), errAvatarCleanupFailed.Error()) {
				t.Errorf("runner log lacks the fixed text: %s", buf.String())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Locks and bounds
// ---------------------------------------------------------------------------

func TestAvatarCleanupLockTimeoutIsOrdinaryFailure(t *testing.T) {
	app := testApp(t)
	u, _ := avatarUser(t, app, "pat", "old.png")
	holder, _ := beginOwnedTx(t, app.DB)
	if err := holder.Exec("SELECT id FROM users WHERE id = ? FOR UPDATE", u.ID).Error; err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err := app.runAvatarCleanup(context.Background(), u.ID)
	if err != errAvatarLockTimeout {
		t.Fatalf("got %v, want the lock timeout text", err)
	}
	if d := time.Since(start); d > 8*time.Second {
		t.Fatalf("took %s", d)
	}
	if got := avatarFiles(t, app, u.ID); len(got) != 2 {
		t.Fatalf("files touched: %v", got)
	}
}

// The same lock timeout through the runner: one attempt is used, the job
// fails with the fixed text, and no file is touched.
func TestAvatarCleanupLockTimeoutThroughTheRunnerUsesOneAttempt(t *testing.T) {
	app := testApp(t)
	u, _ := avatarUser(t, app, "pat", "old.png")
	queueCleanup(t, app, u.ID)
	buf := captureLog(t)
	holder, _ := beginOwnedTx(t, app.DB)
	if err := holder.Exec("SELECT id FROM users WHERE id = ? FOR UPDATE", u.ID).Error; err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	ran, err := jobs.NewRunner(app.DB, app.avatarKinds()...).RunOnce(context.Background(), "test:locked")
	if err != nil || !ran {
		t.Fatalf("run: ran %v err %v", ran, err)
	}
	if d := time.Since(start); d > 8*time.Second {
		t.Fatalf("took %s", d)
	}
	js := avatarJobsFor(t, app, u.ID)
	if len(js) != 1 || js[0].Status != models.JobFailed || js[0].Attempts != 1 || js[0].LastError != "could not lock the account for photo cleanup" {
		t.Fatalf("job %+v", js)
	}
	if got := avatarFiles(t, app, u.ID); len(got) != 2 {
		t.Fatalf("files touched: %v", got)
	}
	if !strings.Contains(buf.String(), "could not lock the account for photo cleanup") {
		t.Errorf("runner log lacks the fixed text: %s", buf.String())
	}
	assertNoAvatarLeak(t, app, "runner log", buf.String())
}

// The row lock is released at the cleanup's deadline even when the storage
// call never returns, because database/sql rolls the transaction back: an
// erasure started behind it answers while the call is still blocked. The
// stuck run then records that it ran out of time, and the erasure's own job
// removes the folder.
func TestStuckCleanupIsReleasedAtItsDeadline(t *testing.T) {
	app := testApp(t)
	store := installAvatarBarrier(app)
	store.cleanGate, store.cleanIgnores = make(chan struct{}), true
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	target, _ := avatarUser(t, app, "target", "old.png")
	adminC := loginAPI(t, app, "admin", "pw")
	queueCleanup(t, app, target.ID)

	done := make(chan error, 1)
	go func() {
		_, err := app.newRunner(app.avatarKinds()...).RunOnce(context.Background(), "test:stuck")
		done <- err
	}()
	awaitSignal(t, store.cleanEntered)

	started := time.Now()
	code := deleteUserAsync(adminC, target.ID)
	select {
	case c := <-code:
		if c != http.StatusOK {
			t.Fatalf("erasure answered %d", c)
		}
	case <-time.After(avatarCleanupTxTimeout + 10*time.Second):
		t.Fatal("erasure never got the row")
	}
	elapsed := time.Since(started)
	if elapsed > avatarCleanupTxTimeout+5*time.Second {
		t.Fatalf("erasure answered after %s, want about %s", elapsed, avatarCleanupTxTimeout)
	}
	if elapsed < avatarCleanupTxTimeout-3*time.Second {
		t.Fatalf("erasure answered after %s: it did not wait for the cleanup's row", elapsed)
	}
	select {
	case err := <-done:
		t.Fatalf("the blocked call returned (%v) before its gate opened", err)
	default:
	}
	close(store.cleanGate)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runner: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the stuck run never ended")
	}
	js := avatarJobsFor(t, app, target.ID)
	if len(js) != 2 || js[0].Status != models.JobFailed || js[0].LastError != "photo cleanup ran out of time" || js[1].Status != models.JobQueued {
		t.Fatalf("jobs %+v", js)
	}
	drainAvatarJobs(t, app)
	if got := avatarFiles(t, app, target.ID); got != nil {
		t.Fatalf("folder remains: %v", got)
	}
	if reloadJob(t, app, js[1].ID).Status != models.JobDone {
		t.Fatal("the erasure's job did not finish")
	}
}

func TestAvatarTxDeadline(t *testing.T) {
	start := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		bodyRead time.Duration
		want     time.Duration
	}{
		"quick body":           {time.Second, time.Second + avatarTxTimeout},
		"body read took 30s":   {30 * time.Second, 50 * time.Second},
		"slow body":            {40 * time.Second, avatarHandlerBudget},
		"body read took 55s":   {55 * time.Second, avatarHandlerBudget},
		"removal has no body":  {0, avatarTxTimeout},
		"exactly at the limit": {30 * time.Second, avatarHandlerBudget},
	} {
		if got := avatarTxDeadline(start, start.Add(tc.bodyRead)); !got.Equal(start.Add(tc.want)) {
			t.Errorf("%s: deadline %s after start, want %s", name, got.Sub(start), tc.want)
		}
	}
	// A body that took over 50s leaves a deadline already past.
	if d := avatarTxDeadline(start, start.Add(55*time.Second)); !d.Before(start.Add(55 * time.Second)) {
		t.Errorf("a 55s body read left a deadline %s after start, which is not past", d.Sub(start))
	}
}

// ---------------------------------------------------------------------------
// The recovery sweep
// ---------------------------------------------------------------------------

var sweepDay = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

func sweepKeys(t *testing.T, app *App) map[string]bool {
	t.Helper()
	var keys []string
	if err := app.DB.Model(&models.Job{}).Where("kind = ? AND unique_key IS NOT NULL", kindAvatarCleanup).Pluck("unique_key", &keys).Error; err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, k := range keys {
		out[k] = true
	}
	return out
}

func TestSweepQueuesOnlyOwnersThatNeedWork(t *testing.T) {
	app := testApp(t)
	// A tombstone with files: queued.
	tomb, _ := avatarUser(t, app, "tomb", "old.png")
	tombstone(t, app, tomb.ID)
	// A live owner with an orphan: queued. With only its current file: not.
	orphan, _ := avatarUser(t, app, "orphan", ".tmp-x")
	clean, _ := avatarUser(t, app, "clean")
	// A disabled person is still live: clean means not queued.
	disabled, _ := avatarUser(t, app, "disabled")
	app.DB.Exec("UPDATE users SET status = 'disabled' WHERE id = ?", disabled.ID)
	// A live owner with no key and a folder: queued.
	noKey := newTestUser(t, app, "nokey", "pw", models.RoleMember)
	writeUploadFile(t, avatarRootDir(app), fmt.Sprintf("avatars/%d/a.png", noKey.ID), "x")
	// An owner already waiting (dead job): skipped.
	waiting, _ := avatarUser(t, app, "waiting", "old.png")
	tombstone(t, app, waiting.ID)
	if err := app.DB.Create(&models.Job{Kind: kindAvatarCleanup, Payload: fmt.Sprintf(`{"userId":%d}`, waiting.ID),
		Status: models.JobDead, MaxAttempts: 5}).Error; err != nil {
		t.Fatal(err)
	}
	// Folders with no account: skipped, files intact.
	writeUploadFile(t, avatarRootDir(app), "avatars/99999/a.png", "stranger")
	// Payloads that are not ours must not break the lookup.
	for _, p := range []string{`{"userId":"abc"}`, `{}`} {
		if err := app.DB.Exec("INSERT INTO jobs (kind, payload, status) VALUES (?, ?::jsonb, 'dead')", kindAvatarCleanup, p).Error; err != nil {
			t.Fatal(err)
		}
	}

	if err := app.sweepAvatarsLimit(context.Background(), sweepDay, avatarSweepLimit); err != nil {
		t.Fatal(err)
	}
	keys := sweepKeys(t, app)
	for _, u := range []*models.User{tomb, orphan, noKey} {
		if !keys[fmt.Sprintf("avatar.cleanup:%d:2026-11-04", u.ID)] {
			t.Errorf("user %d not queued", u.ID)
		}
	}
	for _, u := range []*models.User{clean, disabled, waiting} {
		if keys[fmt.Sprintf("avatar.cleanup:%d:2026-11-04", u.ID)] {
			t.Errorf("user %d queued but needs no work", u.ID)
		}
	}
	if len(avatarFiles(t, app, 99999)) != 1 {
		t.Error("a folder with no account was touched")
	}
	// The same date queues nothing new; tomorrow's date queues the same
	// owners again unless their job is waiting.
	if err := app.sweepAvatarsLimit(context.Background(), sweepDay, avatarSweepLimit); err != nil {
		t.Fatal(err)
	}
	if err := app.sweepAvatarsLimit(context.Background(), sweepDay.AddDate(0, 0, 1), avatarSweepLimit); err != nil {
		t.Fatal(err)
	}
	var queued int64
	app.DB.Model(&models.Job{}).Where("kind = ? AND unique_key IS NOT NULL", kindAvatarCleanup).Count(&queued)
	if queued != 3 {
		t.Fatalf("%d sweep-keyed jobs, want 3 (queued owners wait for their job)", queued)
	}
	// Running them keeps current photos and empties the rest.
	drainAvatarJobs(t, app)
	if got := avatarFiles(t, app, orphan.ID); len(got) != 1 || got[0] != "current.png" {
		t.Errorf("orphan owner files %v", got)
	}
	if avatarFiles(t, app, tomb.ID) != nil || avatarFiles(t, app, noKey.ID) != nil {
		t.Error("a tombstone's or keyless owner's folder remains")
	}
}

func TestSweepLimitTruncatesAndNextDatesFinish(t *testing.T) {
	app := testApp(t)
	var ids []uint
	for i := 0; i < 5; i++ {
		u := newTestUser(t, app, fmt.Sprintf("tomb%d", i), "pw", models.RoleMember)
		writeUploadFile(t, avatarRootDir(app), fmt.Sprintf("avatars/%d/a.png", u.ID), "x")
		tombstone(t, app, u.ID)
		ids = append(ids, u.ID)
	}
	for i, want := range []int{2, 2, 1, 0} {
		before := len(sweepKeys(t, app))
		if err := app.sweepAvatarsLimit(context.Background(), sweepDay.AddDate(0, 0, i), 2); err != nil {
			t.Fatal(err)
		}
		if got := len(sweepKeys(t, app)) - before; got != want {
			t.Fatalf("run %d queued %d, want %d", i+1, got, want)
		}
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if n := len(avatarJobsFor(t, app, id)); n != 1 {
			t.Fatalf("owner %d has %d jobs", id, n)
		}
		seen[itoa(id)] = true
	}
	if len(seen) != 5 {
		t.Fatalf("owners queued: %v", seen)
	}
}

func TestSweepBatchBoundaryQueuesExactlyTheLimit(t *testing.T) {
	app := testApp(t)
	bulkTombstones(t, app, 5000, 5502)
	if err := app.sweepAvatarsLimit(context.Background(), sweepDay, 500); err != nil {
		t.Fatal(err)
	}
	if n := len(sweepKeys(t, app)); n != 500 {
		t.Fatalf("queued %d, want 500", n)
	}
	if err := app.sweepAvatarsLimit(context.Background(), sweepDay.AddDate(0, 0, 1), 500); err != nil {
		t.Fatal(err)
	}
	if n := len(sweepKeys(t, app)); n != 503 {
		t.Fatalf("after the next date %d queued, want 503", n)
	}
	assertAvatarPayloads(t, app)
}

// A partial batch is looked up by the final flush: three owners, far under
// the batch size, are all queued.
func TestSweepFinalFlushQueuesAPartialBatch(t *testing.T) {
	app := testApp(t)
	for i := 0; i < 3; i++ {
		u := newTestUser(t, app, fmt.Sprintf("tomb%d", i), "pw", models.RoleMember)
		writeUploadFile(t, avatarRootDir(app), fmt.Sprintf("avatars/%d/a.png", u.ID), "x")
		tombstone(t, app, u.ID)
	}
	if err := app.sweepAvatarsLimit(context.Background(), sweepDay, 500); err != nil {
		t.Fatal(err)
	}
	if n := len(sweepKeys(t, app)); n != 3 {
		t.Fatalf("queued %d, want 3", n)
	}
	assertAvatarPayloads(t, app)
}

// One job per owner per church date comes from the unique key alone: with the
// first jobs finished (so nothing is waiting) the same date adds nothing,
// tomorrow adds one each, and a request's own job never conflicts with a
// sweep key.
func TestSweepKeyedJobsAreOnePerOwnerPerChurchDate(t *testing.T) {
	app := testApp(t)
	var ids []uint
	for i := 0; i < 2; i++ {
		u := newTestUser(t, app, fmt.Sprintf("tomb%d", i), "pw", models.RoleMember)
		writeUploadFile(t, avatarRootDir(app), fmt.Sprintf("avatars/%d/a.png", u.ID), "x")
		tombstone(t, app, u.ID)
		ids = append(ids, u.ID)
	}
	sweep := func(day time.Time) {
		t.Helper()
		if err := app.sweepAvatarsLimit(context.Background(), day, avatarSweepLimit); err != nil {
			t.Fatal(err)
		}
	}
	finish := func() {
		t.Helper()
		if err := app.DB.Model(&models.Job{}).Where("kind = ?", kindAvatarCleanup).Update("status", models.JobDone).Error; err != nil {
			t.Fatal(err)
		}
	}
	total := func() int64 {
		var n int64
		app.DB.Model(&models.Job{}).Where("kind = ?", kindAvatarCleanup).Count(&n)
		return n
	}
	sweep(sweepDay)
	if total() != 2 {
		t.Fatalf("first sweep queued %d, want 2", total())
	}
	finish()
	sweep(sweepDay)
	if total() != 2 {
		t.Fatalf("the same date queued again: %d jobs", total())
	}
	sweep(sweepDay.AddDate(0, 0, 1))
	if total() != 4 {
		t.Fatalf("tomorrow queued %d jobs in all, want 4", total())
	}
	finish()
	queueCleanup(t, app, ids[0])
	js := avatarJobsFor(t, app, ids[0])
	if len(js) != 3 || js[2].Status != models.JobQueued || js[2].UniqueKey != nil {
		t.Fatalf("a request's job next to sweep keys: %+v", js)
	}
	assertAvatarPayloads(t, app)
}

// The sweep judges a person by deleted_at alone: every status with a current
// photo is queued only when an orphan exists, never when the folder holds
// only the current key.
func TestSweepTreatsEveryStatusAsLive(t *testing.T) {
	for _, status := range []string{models.UserActive, models.UserDisabled, models.UserInvited, models.UserGuest, models.UserPendingConsent} {
		t.Run(status, func(t *testing.T) {
			app := testApp(t)
			orphan, _ := avatarUser(t, app, "orphan", ".tmp-x")
			clean, _ := avatarUser(t, app, "clean")
			for _, u := range []*models.User{orphan, clean} {
				if err := app.DB.Exec("UPDATE users SET status = ? WHERE id = ?", status, u.ID).Error; err != nil {
					t.Fatal(err)
				}
				if status == models.UserInvited || status == models.UserGuest {
					if err := app.DB.Exec("UPDATE users SET password_hash = NULL WHERE id = ?", u.ID).Error; err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := app.sweepAvatarsLimit(context.Background(), sweepDay, avatarSweepLimit); err != nil {
				t.Fatal(err)
			}
			keys := sweepKeys(t, app)
			if !keys[fmt.Sprintf("avatar.cleanup:%d:2026-11-04", orphan.ID)] {
				t.Error("the owner with an orphan was not queued")
			}
			if keys[fmt.Sprintf("avatar.cleanup:%d:2026-11-04", clean.ID)] {
				t.Error("an owner holding only its current photo was queued")
			}
			drainAvatarJobs(t, app)
			if got := avatarFiles(t, app, orphan.ID); len(got) != 1 || got[0] != "current.png" {
				t.Errorf("orphan owner files %v", got)
			}
		})
	}
}

func TestSweepFailuresAreFixedText(t *testing.T) {
	t.Run("queue failure through the runner", func(t *testing.T) {
		app := testApp(t)
		u := newTestUser(t, app, "tomb", "pw", models.RoleMember)
		writeUploadFile(t, avatarRootDir(app), fmt.Sprintf("avatars/%d/a.png", u.ID), "x")
		tombstone(t, app, u.ID)
		installRejectAvatarJobs(t, app)
		buf := captureLog(t)
		if _, err := enqueueJob(app.DB, kindAvatarSweep, nil, time.Now(), kindAvatarSweep+":2026-11-04", 1); err != nil {
			t.Fatal(err)
		}
		runAvatarJobs(t, jobs.NewRunner(app.DB, app.dailyKinds()...))
		var sweep models.Job
		if err := app.DB.Where("kind = ?", kindAvatarSweep).First(&sweep).Error; err != nil {
			t.Fatal(err)
		}
		if sweep.Status != models.JobDead || sweep.LastError != "could not check for leftover profile photos (queue)" {
			t.Fatalf("sweep job %+v", sweep)
		}
		if err := app.sweepAvatarsLimit(context.Background(), sweepDay, avatarSweepLimit); err != errSweepQueue {
			t.Fatalf("direct sweep: %v", err)
		}
		for _, leak := range []string{"/srv/uploads", "secret-key", "user 7", "avatars/7", avatarRootDir(app)} {
			if strings.Contains(sweep.LastError, leak) || strings.Contains(buf.String(), leak) {
				t.Errorf("leaks %q: %s / %s", leak, sweep.LastError, buf.String())
			}
		}
		if !strings.Contains(buf.String(), "could not check for leftover profile photos (queue)") {
			t.Errorf("runner log lacks the fixed text: %s", buf.String())
		}
	})
	t.Run("lookup ends with the context", func(t *testing.T) {
		app := testApp(t)
		for i := 0; i < 3; i++ {
			u := newTestUser(t, app, fmt.Sprintf("tomb%d", i), "pw", models.RoleMember)
			writeUploadFile(t, avatarRootDir(app), fmt.Sprintf("avatars/%d/a.png", u.ID), "x")
			tombstone(t, app, u.ID)
		}
		holder, _ := beginOwnedTx(t, app.DB)
		if err := holder.Exec("LOCK TABLE users IN ACCESS EXCLUSIVE MODE").Error; err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		start := time.Now()
		err := app.sweepAvatarsLimit(ctx, sweepDay, avatarSweepLimit)
		if err == nil || err.Error() != "could not check for leftover profile photos (out of time)" {
			t.Fatalf("got %v", err)
		}
		if d := time.Since(start); d > 4*time.Second {
			t.Fatalf("took %s", d)
		}
		if strings.ContainsAny(err.Error(), "0123456789") {
			t.Errorf("error text holds an id: %q", err)
		}
	})
	t.Run("lookup fails inside the scan", func(t *testing.T) {
		app := testApp(t)
		bulkTombstones(t, app, 5000, 5502)
		holder, _ := beginOwnedTx(t, app.DB)
		if err := holder.Exec("LOCK TABLE users IN ACCESS EXCLUSIVE MODE").Error; err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		start := time.Now()
		err := app.sweepAvatarsLimit(ctx, sweepDay, avatarSweepLimit)
		if err == nil || err.Error() != "could not check for leftover profile photos (out of time)" {
			t.Fatalf("got %v, want out of time and not (scan)", err)
		}
		if d := time.Since(start); d > 4*time.Second {
			t.Fatalf("took %s", d)
		}
	})
	t.Run("empty scan runs no statement on locked tables", func(t *testing.T) {
		app := testApp(t)
		if err := os.MkdirAll(filepath.Join(avatarRootDir(app), "avatars"), 0o755); err != nil {
			t.Fatal(err)
		}
		holder, _ := beginOwnedTx(t, app.DB)
		if err := holder.Exec("LOCK TABLE users, jobs IN ACCESS EXCLUSIVE MODE").Error; err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		start := time.Now()
		if err := app.sweepAvatarsLimit(ctx, sweepDay, avatarSweepLimit); err != nil {
			t.Fatalf("got %v", err)
		}
		if time.Since(start) > time.Second {
			t.Fatal("an empty sweep waited on a locked table")
		}
		run := &avatarSweepRun{db: app.DB.WithContext(ctx), sctx: ctx, day: "2026-11-04", limit: 5}
		start = time.Now()
		if err := run.processBatch(); err != nil {
			t.Fatalf("empty processBatch: %v", err)
		}
		if time.Since(start) > time.Second {
			t.Fatal("an empty processBatch ran a statement")
		}
	})
	t.Run("symlinked avatars folder", func(t *testing.T) {
		app := testApp(t)
		target := t.TempDir()
		if err := os.Symlink(target, filepath.Join(avatarRootDir(app), "avatars")); err != nil {
			t.Fatal(err)
		}
		err := app.sweepAvatarsLimit(context.Background(), sweepDay, avatarSweepLimit)
		if err == nil || err.Error() != "the profile photo folder is not a plain folder" {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("avatars folder that cannot be opened", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root opens any folder")
		}
		app := testApp(t)
		dir := filepath.Join(avatarRootDir(app), "avatars")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(dir, 0o755) })
		err := app.sweepAvatarsLimit(context.Background(), sweepDay, avatarSweepLimit)
		if err == nil || err.Error() != "could not check for leftover profile photos (scan)" {
			t.Fatalf("got %v", err)
		}
		if strings.Contains(err.Error(), "/") {
			t.Errorf("scan text has a path: %q", err)
		}
	})
}

func TestSweepErrorMapping(t *testing.T) {
	pathErr := &os.PathError{Op: "open", Path: "/srv/uploads/avatars/7", Err: os.ErrPermission}
	ended := context.DeadlineExceeded
	for name, tc := range map[string]struct {
		err, ctxErr, want error
	}{
		"nil":                  {nil, nil, nil},
		"full":                 {errSweepFull, nil, nil},
		"lookup":               {errSweepLookup, nil, errSweepLookup},
		"lookup, ctx ended":    {errSweepLookup, ended, errSweepLookup},
		"out of time":          {errSweepOutOfTime, nil, errSweepOutOfTime},
		"out of time, ctx too": {errSweepOutOfTime, ended, errSweepOutOfTime},
		"deadline":             {context.DeadlineExceeded, nil, errSweepOutOfTime},
		"canceled":             {context.Canceled, nil, errSweepOutOfTime},
		"any error, ctx ended": {errors.New("x"), ended, errSweepOutOfTime},
		"unsafe":               {storage.ErrUnsafeOwner, nil, errSweepUnsafe},
		"scan failed":          {storage.ErrScanFailed, nil, errSweepScan},
		"path error":           {pathErr, nil, errSweepScan},
	} {
		if got := sweepScanError(tc.err, tc.ctxErr); got != tc.want {
			t.Errorf("sweepScanError %s: got %v, want %v", name, got, tc.want)
		}
	}
	for _, s := range []error{nil, errSweepZone, errSweepUnsafe, errSweepScan, errSweepLookup, errSweepQueue, errSweepOutOfTime, errAvatarSweepFailed} {
		if got := fixedSweepError(s); got != s {
			t.Errorf("fixedSweepError(%v) = %v", s, got)
		}
	}
	got := fixedSweepError(fmt.Errorf("user 7 at %w", pathErr))
	if got != errAvatarSweepFailed || got.Error() != "could not check for leftover profile photos" {
		t.Errorf("unclassified: %v", got)
	}
	if strings.Contains(errSweepScan.Error(), "/") {
		t.Error("scan text has a path")
	}
}

func TestSweepLogsOnlyCounts(t *testing.T) {
	app := testApp(t)
	root := avatarRootDir(app)
	for _, rel := range []string{"avatars/01/a.png", "avatars/abc/a.png", "avatars/0/a.png", "avatars/readme.txt"} {
		writeUploadFile(t, root, rel, "x")
	}
	// Queued: a tombstone with a file.
	u := newTestUser(t, app, "tomb", "pw", models.RoleMember)
	writeUploadFile(t, root, fmt.Sprintf("avatars/%d/a.png", u.ID), "x")
	tombstone(t, app, u.ID)
	// Waiting: a tombstone whose earlier job is dead.
	w := newTestUser(t, app, "waiting", "pw", models.RoleMember)
	writeUploadFile(t, root, fmt.Sprintf("avatars/%d/a.png", w.ID), "x")
	tombstone(t, app, w.ID)
	if err := app.DB.Create(&models.Job{Kind: kindAvatarCleanup, Payload: fmt.Sprintf(`{"userId":%d}`, w.ID),
		Status: models.JobDead, MaxAttempts: 5}).Error; err != nil {
		t.Fatal(err)
	}
	// No account: a folder of files, and a symlinked folder.
	writeUploadFile(t, root, "avatars/31337/a.png", "stranger")
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "avatars", "777")); err != nil {
		t.Fatal(err)
	}
	buf := captureLog(t)
	if err := app.sweepAvatarsLimit(context.Background(), sweepDay, avatarSweepLimit); err != nil {
		t.Fatal(err)
	}
	line := buf.String()
	want := "avatars: sweep scanned 4 owners, skipped 4 names, 1 with no account, 1 unsafe with no account, 1 already waiting, queued 1, cap reached false\n"
	if line != want {
		t.Errorf("log %q, want %q", line, want)
	}
	for _, leak := range []string{"readme", "abc", "31337", "777", "01", "/", root, "avatars/"} {
		if strings.Contains(line, leak) {
			t.Errorf("log leaks %q: %s", leak, line)
		}
	}
	// The symlinked folder with no account queues nothing and is left alone.
	if n := len(avatarJobsFor(t, app, 777)); n != 0 {
		t.Errorf("%d jobs for an owner with no account", n)
	}
}

// A poisoned owner (a nested folder) dies once and is not queued again until
// an admin dismisses it, so it never starves the others.
func TestSweepFairnessWithPoisonedOwner(t *testing.T) {
	app := testApp(t)
	var ids []uint
	for i := 0; i < 3; i++ {
		u := newTestUser(t, app, fmt.Sprintf("tomb%d", i), "pw", models.RoleMember)
		writeUploadFile(t, avatarRootDir(app), fmt.Sprintf("avatars/%d/a.png", u.ID), "x")
		tombstone(t, app, u.ID)
		ids = append(ids, u.ID)
	}
	poisoned := ids[1]
	if err := os.MkdirAll(filepath.Join(avatarRootDir(app), "avatars", itoa(poisoned), "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	app.DB.Model(&models.Job{}).Where("true").Update("max_attempts", 1)
	for i := 0; i < 4; i++ {
		if err := app.sweepAvatarsLimit(context.Background(), sweepDay.AddDate(0, 0, i), 2); err != nil {
			t.Fatal(err)
		}
		app.DB.Model(&models.Job{}).Where("kind = ?", kindAvatarCleanup).Update("max_attempts", 1)
		drainAvatarJobs(t, app)
	}
	js := avatarJobsFor(t, app, poisoned)
	if len(js) != 1 || js[0].Status != models.JobDead || js[0].LastError != errAvatarFolderNested.Error() {
		t.Fatalf("poisoned owner's jobs: %+v", js)
	}
	for _, id := range ids {
		if id != poisoned && avatarFiles(t, app, id) != nil {
			t.Fatalf("owner %d was starved", id)
		}
	}
	// While it is dead, later dates skip it and queue nothing for it.
	buf := captureLog(t)
	if err := app.sweepAvatarsLimit(context.Background(), sweepDay.AddDate(0, 0, 5), 2); err != nil {
		t.Fatal(err)
	}
	if line := buf.String(); !strings.Contains(line, "1 already waiting") || !strings.Contains(line, "queued 0") {
		t.Errorf("log %q: want the poisoned owner counted as waiting and nothing queued", line)
	}
	if n := len(avatarJobsFor(t, app, poisoned)); n != 1 {
		t.Fatalf("a dead owner was queued again: %d jobs", n)
	}
	// Dismissing it lets the next date queue it exactly once more.
	app.DB.Model(&models.Job{}).Where("id = ?", js[0].ID).Update("status", models.JobDismissed)
	if err := app.sweepAvatarsLimit(context.Background(), sweepDay.AddDate(0, 0, 10), 2); err != nil {
		t.Fatal(err)
	}
	if n := len(avatarJobsFor(t, app, poisoned)); n != 2 {
		t.Fatalf("poisoned owner has %d jobs after dismissal, want 2", n)
	}
}

// Cleaning user N through upload, removal, erasure and the sweep never
// touches the folder of user N followed by a zero (users 1 and 10), nor any
// covers file.
func TestAvatarWorkNeverTouchesNeighborsOrCovers(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	adminC := loginAPI(t, app, "admin", "pw")
	u := newTestUser(t, app, "grier", "pw", models.RoleMember)
	c := loginAPI(t, app, "grier", "pw")
	nid := u.ID * 10
	nkey := fmt.Sprintf("avatars/%d/current.png", nid)
	if err := app.DB.Exec("INSERT INTO users (id, email, name, avatar_key) VALUES (?, 'neighbor@example.com', 'neighbor', ?)", nid, nkey).Error; err != nil {
		t.Fatal(err)
	}
	writeUploadFile(t, avatarRootDir(app), nkey, "neighbor bytes")
	writeUploadFile(t, avatarRootDir(app), fmt.Sprintf("covers/%d/c.png", u.ID), "cover bytes")
	writeUploadFile(t, avatarRootDir(app), fmt.Sprintf("covers/%d/c.png", nid), "other cover")
	intact := func(step string) {
		t.Helper()
		for rel, want := range map[string]string{nkey: "neighbor bytes",
			fmt.Sprintf("covers/%d/c.png", u.ID): "cover bytes", fmt.Sprintf("covers/%d/c.png", nid): "other cover"} {
			if got := readAvatarFile(t, app, rel); got != want {
				t.Fatalf("after %s: %s holds %q", step, rel, got)
			}
		}
		if got := avatarFiles(t, app, nid); len(got) != 1 || got[0] != "current.png" {
			t.Fatalf("after %s: neighbor files %v", step, got)
		}
	}
	for i := 0; i < 2; i++ {
		if w := c.doMultipart("POST", "/api/v1/me/avatar", "avatar", "me.png", tinyPNG(t)); w.Code != http.StatusOK {
			t.Fatalf("upload: %d", w.Code)
		}
	}
	drainAvatarJobs(t, app)
	intact("upload")
	if w := c.do("DELETE", "/api/v1/me/avatar", nil, true); w.Code != http.StatusOK {
		t.Fatalf("removal: %d", w.Code)
	}
	drainAvatarJobs(t, app)
	intact("removal")
	writeUploadFile(t, avatarRootDir(app), fmt.Sprintf("avatars/%d/.tmp-left", u.ID), "x")
	if err := app.sweepAvatarsLimit(context.Background(), sweepDay, avatarSweepLimit); err != nil {
		t.Fatal(err)
	}
	if n := len(avatarJobsFor(t, app, nid)); n != 0 {
		t.Fatalf("the sweep queued the neighbor: %d jobs", n)
	}
	drainAvatarJobs(t, app)
	intact("sweep")
	if got := avatarFiles(t, app, u.ID); got != nil {
		t.Fatalf("the sweep left %v", got)
	}
	writeUploadFile(t, avatarRootDir(app), fmt.Sprintf("avatars/%d/stray.png", u.ID), "x")
	if w := adminC.do("DELETE", "/api/v1/admin/users/"+itoa(u.ID), nil, true); w.Code != http.StatusOK {
		t.Fatalf("erasure: %d", w.Code)
	}
	drainAvatarJobs(t, app)
	intact("erasure")
	if got := avatarFiles(t, app, u.ID); got != nil {
		t.Fatalf("erasure left %v", got)
	}
}

// A symlinked owner folder of a tombstone is queued by the sweep. Its job
// dies with fixed text, the target's files stay, Admin, System lists it and
// the admins get one notice. A symlinked owner folder with no account only
// raises a logged count.
func TestSweepQueuesAnUnsafeOwnerAndItsJobDiesVisibly(t *testing.T) {
	app := testApp(t)
	admin := newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	adminC := loginAPI(t, app, "admin", "password1234")
	u := newTestUser(t, app, "tomb", "pw", models.RoleMember)
	tombstone(t, app, u.ID)
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "a.png"), []byte("target bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	avatars := filepath.Join(avatarRootDir(app), "avatars")
	if err := os.MkdirAll(avatars, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(avatars, itoa(u.ID))); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(avatars, "888")); err != nil {
		t.Fatal(err)
	}
	buf := captureLog(t)
	if err := app.sweepAvatarsLimit(context.Background(), sweepDay, avatarSweepLimit); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "1 unsafe with no account") || !strings.Contains(buf.String(), "queued 1") {
		t.Errorf("sweep log %q", buf.String())
	}
	if n := len(avatarJobsFor(t, app, 888)); n != 0 {
		t.Fatalf("%d jobs for an owner with no account", n)
	}
	limitAvatarJobAttempts(t, app)
	drainAvatarJobsAlerting(t, app)
	js := avatarJobsFor(t, app, u.ID)
	if len(js) != 1 || js[0].Status != models.JobDead || js[0].LastError != "avatar folder is not a plain folder" {
		t.Fatalf("job %+v", js)
	}
	if b, err := os.ReadFile(filepath.Join(target, "a.png")); err != nil || string(b) != "target bytes" {
		t.Fatalf("the symlink's target was touched: %q %v", b, err)
	}
	var listed struct {
		Jobs []adminJobJSON `json:"jobs"`
	}
	decodeJSON(t, adminC.do("GET", "/api/v1/admin/jobs", nil, false), &listed)
	if len(listed.Jobs) != 1 || listed.Jobs[0].ID != js[0].ID || listed.Jobs[0].Label != "Removing old profile photos" ||
		listed.Jobs[0].LastError != "avatar folder is not a plain folder" {
		t.Fatalf("Admin, System lists %+v", listed.Jobs)
	}
	notices := deadAlerts(t, app, js[0].ID)
	if len(notices) != 1 || notices[0].UserID != admin.ID || !strings.Contains(notices[0].Title, "Removing old profile photos") {
		t.Fatalf("dead-job notices %+v", notices)
	}
	assertNoAvatarLeak(t, app, "notice", notices[0].Title+"|"+notices[0].Body, target)
}

// While a request or a job holds the user row, none of them holds or needs
// the contact gate, and none writes a notice.
func TestAvatarWorkTakesOnlyTheUserRow(t *testing.T) {
	gateIsFree := func(t *testing.T, app *App) {
		t.Helper()
		if err := app.DB.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec("SET LOCAL lock_timeout = '1s'").Error; err != nil {
				return err
			}
			return lockContacts(tx)
		}); err != nil {
			t.Fatalf("the contact gate was held: %v", err)
		}
		var held int
		if err := app.DB.Raw(`SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND granted
			AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`).Scan(&held).Error; err != nil || held != 0 {
			t.Fatalf("%d advisory locks held (%v)", held, err)
		}
		var notices int64
		app.DB.Model(&models.Notification{}).Count(&notices)
		if notices != 0 {
			t.Fatalf("%d notices written", notices)
		}
	}
	t.Run("upload", func(t *testing.T) {
		app := testApp(t)
		store := installAvatarBarrier(app)
		store.putGate = make(chan struct{})
		newTestUser(t, app, "grier", "pw", models.RoleMember)
		c := loginAPI(t, app, "grier", "pw")
		data := tinyPNG(t)
		resp := make(chan int, 1)
		go func() { resp <- c.doMultipart("POST", "/api/v1/me/avatar", "avatar", "me.png", data).Code }()
		awaitSignal(t, store.putEntered)
		gateIsFree(t, app)
		close(store.putGate)
		if code := <-resp; code != http.StatusOK {
			t.Fatalf("upload: %d", code)
		}
	})
	t.Run("removal", func(t *testing.T) {
		app := testApp(t)
		u, _ := avatarUser(t, app, "grier", "old.png")
		c := loginAPI(t, app, "grier", "pw")
		holder, pid := beginOwnedTx(t, app.DB)
		if err := holder.Exec("SELECT id FROM users WHERE id = ? FOR UPDATE", u.ID).Error; err != nil {
			t.Fatal(err)
		}
		resp := make(chan int, 1)
		go func() { resp <- c.do("DELETE", "/api/v1/me/avatar", nil, true).Code }()
		waitPID := waiterOn(t, app.DB, pid)
		if n := advisoryLocksHeldBy(t, app, waitPID); n != 0 {
			t.Fatalf("a waiting removal holds %d advisory locks", n)
		}
		holder.Rollback()
		if code := <-resp; code != http.StatusOK {
			t.Fatalf("removal: %d", code)
		}
	})
	t.Run("cleanup", func(t *testing.T) {
		app := testApp(t)
		store := installAvatarBarrier(app)
		store.cleanGate = make(chan struct{})
		u, _ := avatarUser(t, app, "grier", "old.png")
		done := make(chan error, 1)
		go func() { done <- app.runAvatarCleanup(context.Background(), u.ID) }()
		awaitSignal(t, store.cleanEntered)
		gateIsFree(t, app)
		close(store.cleanGate)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		gateIsFree(t, app)
	})
}

// A job left running by a process that died resumes, and a job queued
// before a restart runs under a new App.
func TestAvatarCleanupResumesAfterAProcessFailure(t *testing.T) {
	t.Run("lease ran out after some files were removed", func(t *testing.T) {
		app := testApp(t)
		u := newTestUser(t, app, "pat", "pw", models.RoleMember)
		for _, n := range []string{"a", "b", "c", "d", "e"} {
			writeUploadFile(t, avatarRootDir(app), fmt.Sprintf("avatars/%d/%s.png", u.ID, n), "x")
		}
		tombstone(t, app, u.ID)
		// The dead worker had removed two files.
		for _, n := range []string{"a", "b"} {
			if err := os.Remove(filepath.Join(avatarRootDir(app), "avatars", itoa(u.ID), n+".png")); err != nil {
				t.Fatal(err)
			}
		}
		locked := time.Now().Add(-time.Hour)
		job := models.Job{Kind: kindAvatarCleanup, Payload: fmt.Sprintf(`{"userId":%d}`, u.ID), Status: models.JobRunning,
			Attempts: 1, MaxAttempts: 5, LockedAt: &locked, LockedBy: "crashed:1"}
		if err := app.DB.Create(&job).Error; err != nil {
			t.Fatal(err)
		}
		r := jobs.NewRunner(app.DB, app.avatarKinds()...)
		r.Lease = time.Minute
		ran, err := r.RunOnce(context.Background(), "test:fresh")
		if err != nil || !ran {
			t.Fatalf("run: ran %v err %v", ran, err)
		}
		if got := reloadJob(t, app, job.ID); got.Status != models.JobDone || got.Attempts != 2 {
			t.Fatalf("job %+v", got)
		}
		if got := avatarFiles(t, app, u.ID); got != nil {
			t.Fatalf("folder remains: %v", got)
		}
	})
	t.Run("queued before a restart", func(t *testing.T) {
		app := testApp(t)
		u, _ := avatarUser(t, app, "pat", "old.png")
		queueCleanup(t, app, u.ID)
		uploads, err := storage.NewLocalStore(avatarRootDir(app), "/uploads")
		if err != nil {
			t.Fatal(err)
		}
		restarted := &App{DB: app.DB, Store: app.Store, Config: app.Config, Uploads: uploads, Mailer: app.Mailer}
		runAvatarJobs(t, jobs.NewRunner(restarted.DB, restarted.avatarKinds()...))
		if got := avatarFiles(t, app, u.ID); len(got) != 1 || got[0] != "current.png" {
			t.Fatalf("files %v", got)
		}
		if js := avatarJobsFor(t, app, u.ID); len(js) != 1 || js[0].Status != models.JobDone {
			t.Fatalf("jobs %+v", js)
		}
	})
}
