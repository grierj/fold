package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"fold/internal/jobs"
	"fold/internal/testdb"
	"fold/models"
	"fold/storage"
)

// An id no users row holds, and one for a symlink folder.
const (
	settleStrayID   = 7351842
	settleSymlinkID = 7351843
)

func settleApp(t *testing.T) (*App, *avatarBarrierStore) {
	t.Helper()
	app := testApp(t)
	return app, installAvatarBarrier(app)
}

func settleFill(t *testing.T, app *App, owner uint, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		writeUploadFile(t, avatarRootDir(app), fmt.Sprintf("avatars/%d/old%04d.png", owner, i), "old")
	}
}

// settleJobs is every job row as text, so a test can show none changed.
func settleJobs(t *testing.T, app *App) string {
	t.Helper()
	var rows []struct {
		ID        uint
		Kind      string
		Payload   string
		Status    string
		Attempts  int
		LockedBy  string
		LastError string
		RunAt     time.Time
	}
	if err := app.DB.Raw(`SELECT id, kind, payload::text AS payload, status, attempts, locked_by, last_error, run_at FROM jobs ORDER BY id`).Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// settleTree maps every file under dir to its bytes.
func settleTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(dir, p)
			out[rel] = string(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func settleSameTree(t *testing.T, what string, want, got map[string]string) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("%s: %d files, want %d", what, len(got), len(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s: %s changed", what, k)
		}
	}
}

func TestSettleClearsErasedAndUnownedOnly(t *testing.T) {
	app, _ := settleApp(t)
	active, _ := avatarUser(t, app, "active", "replaced1.png", "replaced2.png")
	disabled, _ := avatarUser(t, app, "disabled", "replaced.png")
	if err := app.DB.Exec("UPDATE users SET status = ? WHERE id = ?", models.UserDisabled, disabled.ID).Error; err != nil {
		t.Fatal(err)
	}
	erased, _ := avatarUser(t, app, "erased", "old.png")
	tombstone(t, app, erased.ID)
	writeUploadFile(t, avatarRootDir(app), fmt.Sprintf("avatars/%d/stray.png", settleStrayID), "stray")
	// Work that belongs to the running app: a pending photo cleanup for the
	// erased account and an unrelated queued notice. Settling must not read or
	// move either.
	queueCleanup(t, app, erased.ID)
	if _, err := (jobs.Type{Name: "notify.deliver"}).Enqueue(app.DB, map[string]int{"notificationId": 1}, time.Now(), ""); err != nil {
		t.Fatal(err)
	}
	jobsBefore := settleJobs(t, app)
	activeBefore := settleTree(t, filepath.Join(avatarRootDir(app), "avatars", itoa(active.ID)))
	disabledBefore := settleTree(t, filepath.Join(avatarRootDir(app), "avatars", itoa(disabled.ID)))
	if len(activeBefore) != 3 || len(disabledBefore) != 2 {
		t.Fatalf("fixture: %d and %d files", len(activeBefore), len(disabledBefore))
	}

	counts, err := app.settleUploads(context.Background())
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if want := (settleCounts{OwnersScanned: 4, ErasedCleared: 1, UnownedCleared: 1}); counts != want {
		t.Fatalf("counts = %+v, want %+v", counts, want)
	}
	if got := avatarFiles(t, app, erased.ID); got != nil {
		t.Fatalf("erased folder still holds %v", got)
	}
	if got := avatarFiles(t, app, settleStrayID); got != nil {
		t.Fatalf("unowned folder still holds %v", got)
	}
	settleSameTree(t, "active", activeBefore, settleTree(t, filepath.Join(avatarRootDir(app), "avatars", itoa(active.ID))))
	settleSameTree(t, "disabled", disabledBefore, settleTree(t, filepath.Join(avatarRootDir(app), "avatars", itoa(disabled.ID))))
	if got := settleJobs(t, app); got != jobsBefore {
		t.Fatalf("settling changed the job rows:\nbefore %s\nafter  %s", jobsBefore, got)
	}

	// A second run has nothing to do and says so.
	counts, err = app.settleUploads(context.Background())
	if err != nil || counts != (settleCounts{OwnersScanned: 2}) {
		t.Fatalf("second run: %+v, %v", counts, err)
	}
}

// The running app's job still leaves a folder with no account alone: only the
// closed restore command clears one.
func TestSettleLeavesTheJobsNoAccountRuleAlone(t *testing.T) {
	app, _ := settleApp(t)
	writeUploadFile(t, avatarRootDir(app), fmt.Sprintf("avatars/%d/stray.png", settleStrayID), "stray")
	if err := app.runAvatarCleanup(context.Background(), settleStrayID); err != nil {
		t.Fatalf("cleanup job for no account: %v", err)
	}
	if got := avatarFiles(t, app, settleStrayID); len(got) != 1 {
		t.Fatalf("the job touched an unowned folder: %v", got)
	}
}

func TestSettleFinishesBeyondOneBatchInOneRun(t *testing.T) {
	app, store := settleApp(t)
	active, _ := avatarUser(t, app, "active", "replaced.png")
	erased, _ := avatarUser(t, app, "erased")
	tombstone(t, app, erased.ID)
	settleFill(t, app, erased.ID, 2*storage.CleanBatch+7)
	settleFill(t, app, settleStrayID, storage.CleanBatch+3)
	activeBefore := settleTree(t, filepath.Join(avatarRootDir(app), "avatars", itoa(active.ID)))

	counts, err := app.settleUploads(context.Background())
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if counts.ErasedCleared != 1 || counts.UnownedCleared != 1 || counts.Remaining != 0 {
		t.Fatalf("counts = %+v", counts)
	}
	store.mu.Lock()
	calls := store.cleanCalls
	store.mu.Unlock()
	if calls < 5 {
		t.Fatalf("%d store calls: the batches were not repeated (want 3 erased + 2 unowned)", calls)
	}
	if avatarFiles(t, app, erased.ID) != nil || avatarFiles(t, app, settleStrayID) != nil {
		t.Fatal("a big folder survived")
	}
	settleSameTree(t, "active", activeBefore, settleTree(t, filepath.Join(avatarRootDir(app), "avatars", itoa(active.ID))))
}

func TestSettleDeadlineRefusesAndTheNextRunConverges(t *testing.T) {
	app, store := settleApp(t)
	active, _ := avatarUser(t, app, "active", "replaced.png")
	erased, _ := avatarUser(t, app, "erased", "old.png")
	tombstone(t, app, erased.ID)
	settleFill(t, app, settleStrayID, 5)
	activeBefore := settleTree(t, filepath.Join(avatarRootDir(app), "avatars", itoa(active.ID)))

	// A store that always says "more remain" runs the budget out.
	store.mu.Lock()
	store.cleanErr = storage.ErrMoreRemain
	store.mu.Unlock()
	oldBudget := settleBudget
	settleBudget = 150 * time.Millisecond
	t.Cleanup(func() { settleBudget = oldBudget })

	counts, err := app.settleUploads(context.Background())
	if err != errSettleNotClear {
		t.Fatalf("out of time: err = %v, want the not-clear refusal", err)
	}
	if counts.Remaining != 2 || counts.ErasedCleared != 0 || counts.UnownedCleared != 0 {
		t.Fatalf("counts after the deadline = %+v", counts)
	}
	var out, errOut bytes.Buffer
	if code := app.settleUploadsCommand(context.Background(), true, &out, &errOut); code != 1 {
		t.Fatalf("command exit = %d, want 1 (the site must not reopen)", code)
	}

	store.mu.Lock()
	store.cleanErr = nil
	store.mu.Unlock()
	settleBudget = oldBudget
	counts, err = app.settleUploads(context.Background())
	if err != nil || counts.Remaining != 0 || counts.ErasedCleared != 1 || counts.UnownedCleared != 1 {
		t.Fatalf("next run: %+v, %v", counts, err)
	}
	settleSameTree(t, "active", activeBefore, settleTree(t, filepath.Join(avatarRootDir(app), "avatars", itoa(active.ID))))
}

func TestSettleRefusesUnsafeAndNestedFoldersWithCountsOnly(t *testing.T) {
	app, _ := settleApp(t)
	root := avatarRootDir(app)
	active, _ := avatarUser(t, app, "active", "replaced.png")
	erased := newTestUser(t, app, "sentinel-erased-name", "pw", models.RoleMember)
	tombstone(t, app, erased.ID)
	writeUploadFile(t, root, fmt.Sprintf("avatars/%d/sentinel-key-file.png", erased.ID), "x")
	writeUploadFile(t, root, fmt.Sprintf("avatars/%d/nested/inner.png", erased.ID), "inner")
	// An unowned folder that is a symlink out of the tree: it is refused, and
	// what it points at is never touched.
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "canary.png"), []byte("canary"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "avatars", itoa(settleSymlinkID))); err != nil {
		t.Fatal(err)
	}
	activeBefore := settleTree(t, filepath.Join(root, "avatars", itoa(active.ID)))

	var out, errOut bytes.Buffer
	if code := app.settleUploadsCommand(context.Background(), true, &out, &errOut); code != 1 {
		t.Fatalf("exit = %d, want 1; stdout %q stderr %q", code, out.String(), errOut.String())
	}
	var got settleCounts
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("stdout %q is not the counts: %v", out.String(), err)
	}
	if want := (settleCounts{OwnersScanned: 3, Remaining: 2}); got != want {
		t.Fatalf("counts = %+v, want %+v", got, want)
	}
	out2 := out.String() + errOut.String()
	for _, secret := range []string{"sentinel-erased-name", "sentinel-key-file", "inner.png", "canary", "nested", outside, root,
		fmt.Sprint(settleStrayID), fmt.Sprint(settleSymlinkID), "avatars/"} {
		if strings.Contains(out2, secret) {
			t.Fatalf("output leaks %q: %q", secret, out2)
		}
	}
	// Nothing recursed, nothing followed.
	if b, _ := os.ReadFile(filepath.Join(root, "avatars", itoa(erased.ID), "nested", "inner.png")); string(b) != "inner" {
		t.Fatal("the nested folder's content was removed")
	}
	if _, err := os.Stat(filepath.Join(outside, "canary.png")); err != nil {
		t.Fatalf("the symlink target was touched: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "avatars", itoa(erased.ID), "sentinel-key-file.png")); err == nil {
		t.Fatal("the erased folder's plain file stayed")
	}
	settleSameTree(t, "active", activeBefore, settleTree(t, filepath.Join(root, "avatars", itoa(active.ID))))

	// The plain-text form prints the same counts and nothing else.
	out.Reset()
	errOut.Reset()
	if code := app.settleUploadsCommand(context.Background(), false, &out, &errOut); code != 1 ||
		!strings.Contains(out.String(), "2 remaining") || strings.Contains(out.String()+errOut.String(), "sentinel") {
		t.Fatalf("plain output: code %d, %q, %q", code, out.String(), errOut.String())
	}
}

// Entries under avatars/ that are not account ids are skipped by the store's
// scan, so the command cannot judge what is inside. A photo under one could be
// an erased person's: the run must refuse, count them, and leave them alone.
func TestSettleRefusesUnexpectedAvatarEntriesWithCountsOnly(t *testing.T) {
	app, _ := settleApp(t)
	root := avatarRootDir(app)
	erased := newTestUser(t, app, "sentinel-erased-name", "pw", models.RoleMember)
	tombstone(t, app, erased.ID)
	writeUploadFile(t, root, fmt.Sprintf("avatars/%d/a.png", erased.ID), "x")
	writeUploadFile(t, root, fmt.Sprintf("avatars/%d/stray.png", settleStrayID), "stray")
	writeUploadFile(t, root, "avatars/01/sentinel-photo.png", "leading zero")
	writeUploadFile(t, root, "avatars/sentinel-unknown/sentinel-photo.png", "not an id")
	writeUploadFile(t, root, "avatars/sentinel-note.txt", "loose file")
	unexpectedBefore := map[string]string{}
	for rel, body := range settleTree(t, filepath.Join(root, "avatars")) {
		if strings.HasPrefix(rel, "01") || strings.HasPrefix(rel, "sentinel-") {
			unexpectedBefore[rel] = body
		}
	}
	if len(unexpectedBefore) != 3 {
		t.Fatalf("fixture: %d unexpected files", len(unexpectedBefore))
	}

	var out, errOut bytes.Buffer
	if code := app.settleUploadsCommand(context.Background(), true, &out, &errOut); code != 1 {
		t.Fatalf("exit = %d, want 1; stdout %q stderr %q", code, out.String(), errOut.String())
	}
	var got settleCounts
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("stdout %q is not the counts: %v", out.String(), err)
	}
	want := settleCounts{OwnersScanned: 2, ErasedCleared: 1, UnownedCleared: 1, Unexpected: 3}
	if got != want {
		t.Fatalf("counts = %+v, want %+v", got, want)
	}
	for _, secret := range []string{"sentinel", "photo.png", "note.txt", root, "avatars/", fmt.Sprint(settleStrayID)} {
		if strings.Contains(out.String()+errOut.String(), secret) {
			t.Fatalf("output leaks %q: %q %q", secret, out.String(), errOut.String())
		}
	}
	if errOut.Len() == 0 {
		t.Fatal("a refusal printed no message")
	}
	afterTree := settleTree(t, filepath.Join(root, "avatars"))
	for rel, body := range unexpectedBefore {
		if afterTree[rel] != body {
			t.Fatalf("an unexpected entry was changed or removed: %s", rel)
		}
	}
	if got := avatarFiles(t, app, erased.ID); got != nil {
		t.Fatalf("the erased folder still holds %v", got)
	}

	// Nothing else is wrong now, yet the run still refuses until the operator
	// reconciles the entries; the plain form says so too.
	out.Reset()
	errOut.Reset()
	if code := app.settleUploadsCommand(context.Background(), false, &out, &errOut); code != 1 ||
		!strings.Contains(out.String(), "3 unexpected") || !strings.Contains(out.String(), "0 remaining") ||
		strings.Contains(out.String()+errOut.String(), "sentinel") {
		t.Fatalf("second run: code %d, %q, %q", code, out.String(), errOut.String())
	}
	for _, rel := range []string{"01", "sentinel-unknown", "sentinel-note.txt"} {
		if err := os.RemoveAll(filepath.Join(root, "avatars", rel)); err != nil {
			t.Fatal(err)
		}
	}
	out.Reset()
	errOut.Reset()
	if code := app.settleUploadsCommand(context.Background(), false, &out, &errOut); code != 0 || errOut.Len() != 0 ||
		!strings.Contains(out.String(), "0 unexpected") {
		t.Fatalf("after reconciling: code %d, %q, %q", code, out.String(), errOut.String())
	}
}

func TestSettleCleanAccountSharesTheJobRule(t *testing.T) {
	app, store := settleApp(t)
	u, cur := avatarUser(t, app, "keeps", "old.png")
	found, err := app.cleanAvatarAccount(context.Background(), u.ID)
	if err != nil || !found {
		t.Fatalf("active account: found %v, err %v", found, err)
	}
	if got := avatarFiles(t, app, u.ID); len(got) != 1 || got[0] != filepath.Base(cur) {
		t.Fatalf("the active account's folder = %v, want only its current photo", got)
	}
	tombstone(t, app, u.ID)
	if found, err = app.cleanAvatarAccount(context.Background(), u.ID); err != nil || !found {
		t.Fatalf("tombstone: found %v, err %v", found, err)
	}
	if got := avatarFiles(t, app, u.ID); got != nil {
		t.Fatalf("a tombstone kept %v", got)
	}
	if found, err = app.cleanAvatarAccount(context.Background(), settleStrayID); err != nil || found {
		t.Fatalf("no account: found %v, err %v", found, err)
	}
	// The job's adapter still maps the raw outcome to its fixed text.
	store.mu.Lock()
	store.cleanErr = storage.ErrMoreRemain
	store.mu.Unlock()
	live, _ := avatarUser(t, app, "later")
	if err := app.runAvatarCleanup(context.Background(), live.ID); !isLater(err) {
		t.Fatalf("job with more remaining: %v, want a Later", err)
	}
}

// --- the command: schema check, environment, exit codes ----------------------

func settleCLIEnv(t *testing.T, name string) (uploadDir string) {
	t.Helper()
	url, err := testdb.URL(name)
	if err != nil {
		t.Fatal(err)
	}
	uploadDir = t.TempDir()
	t.Setenv("DATABASE_URL", url)
	t.Setenv("UPLOAD_DIR", uploadDir)
	t.Setenv("SESSION_SECRET", "")
	t.Setenv("APP_ENV", "")
	return uploadDir
}

func TestRestoreSettleUploadsCommand(t *testing.T) {
	name := testdb.Fresh(t, true)
	dir := settleCLIEnv(t, name)
	db, err := testdb.OpenSQL(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, q := range []string{
		`INSERT INTO users(id,email,status,role) VALUES(11,'live@example.com','active','member')`,
		`INSERT INTO users(id,email,status,role,deleted_at) VALUES(12,'gone@example.com','active','member',now())`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for rel, body := range map[string]string{
		"avatars/11/current.png": "live", "avatars/11/old.png": "old",
		"avatars/12/a.png": "a", "avatars/13/stray.png": "s",
	} {
		writeUploadFile(t, dir, rel, body)
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"restore", "settle-uploads", "--json"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d; stdout %q stderr %q", code, out.String(), errOut.String())
	}
	var got settleCounts
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got != (settleCounts{OwnersScanned: 3, ErasedCleared: 1, UnownedCleared: 1}) {
		t.Fatalf("stdout %q: %+v, %v", out.String(), got, err)
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q", errOut.String())
	}
	tree := settleTree(t, dir)
	keys := make([]string, 0, len(tree))
	for k := range tree {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != "avatars/11/current.png,avatars/11/old.png" {
		t.Fatalf("files left: %v", keys)
	}
	// The plain form on a settled tree.
	out.Reset()
	if code := run([]string{"restore", "settle-uploads"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "0 remaining") {
		t.Fatalf("plain: code %d, %q, %q", code, out.String(), errOut.String())
	}
}

func TestRestoreSettleUploadsSchemaMismatchTouchesNothing(t *testing.T) {
	secrets := []string{"stray-sentinel"}
	t.Run("no schema at all", func(t *testing.T) {
		name := testdb.Fresh(t, false)
		dir := settleCLIEnv(t, name)
		writeUploadFile(t, dir, "avatars/13/stray-sentinel.png", "s")
		var out, errOut bytes.Buffer
		if code := run([]string{"restore", "settle-uploads", "--json"}, &out, &errOut); code != 2 {
			t.Fatalf("exit %d, want 2; stderr %q", code, errOut.String())
		}
		settleRefusalIsClean(t, name, dir, out.String(), errOut.String(), secrets)
		db, err := testdb.OpenSQL(name)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var tables int
		if err := db.QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_schema='public'`).Scan(&tables); err != nil || tables != 0 {
			t.Fatalf("the refused run left %d tables (%v): it must not create the goose table", tables, err)
		}
	})
	t.Run("one migration behind", func(t *testing.T) {
		name := testdb.Fresh(t, true)
		dir := settleCLIEnv(t, name)
		db, err := testdb.OpenSQL(name)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err := db.Exec(`INSERT INTO users(id,email,status,role,deleted_at) VALUES(12,'gone@example.com','active','member',now())`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`DELETE FROM goose_db_version WHERE version_id = (SELECT max(version_id) FROM goose_db_version)`); err != nil {
			t.Fatal(err)
		}
		writeUploadFile(t, dir, "avatars/12/stray-sentinel.png", "erased")
		var out, errOut bytes.Buffer
		if code := run([]string{"restore", "settle-uploads"}, &out, &errOut); code != 2 {
			t.Fatalf("exit %d, want 2; stderr %q", code, errOut.String())
		}
		settleRefusalIsClean(t, name, dir, out.String(), errOut.String(), secrets)
	})
}

// settleRefusalIsClean checks a refused run printed no value, left the upload
// tree as it was and (when the database has a users table) changed no row.
func settleRefusalIsClean(t *testing.T, name, dir, stdout, stderr string, secrets []string) {
	t.Helper()
	if stdout != "" {
		t.Fatalf("stdout = %q, want nothing", stdout)
	}
	for _, s := range append(secrets, os.Getenv("DATABASE_URL"), dir, name, "postgres://") {
		if s != "" && strings.Contains(stderr, s) {
			t.Fatalf("stderr leaks %q: %q", s, stderr)
		}
	}
	if len(settleTree(t, dir)) != 1 {
		t.Fatalf("the refused run changed the uploads: %v", settleTree(t, dir))
	}
}

// A connection that cannot be made answers with fixed text. The driver's own
// error can hold the connection string, the user, the database or the host.
func TestRestoreSettleUploadsConnectionFailureIsValueFree(t *testing.T) {
	sentinels := []string{"sentinel-user", "sentinel-pass", "sentinel-host", "sentinel-db", "sentinel-option", "badport"}
	for name, url := range map[string]string{
		"malformed url":      "postgres://sentinel-user:sentinel-pass@sentinel-host:badport/sentinel-db",
		"malformed keywords": "host=sentinel-host user=sentinel-user password=sentinel-pass dbname=sentinel-db sentinel-option",
		"refused":            "postgres://sentinel-user:sentinel-pass@127.0.0.1:1/sentinel-db?sslmode=disable&connect_timeout=2",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("DATABASE_URL", url)
			t.Setenv("UPLOAD_DIR", dir)
			t.Setenv("SESSION_SECRET", "")
			t.Setenv("APP_ENV", "")
			writeUploadFile(t, dir, "avatars/13/sentinel-photo.png", "s")
			var out, errOut bytes.Buffer
			if code := run([]string{"restore", "settle-uploads", "--json"}, &out, &errOut); code != 1 {
				t.Fatalf("exit %d, want 1; stderr %q", code, errOut.String())
			}
			if out.Len() != 0 {
				t.Fatalf("stdout = %q, want nothing", out.String())
			}
			if got, want := strings.TrimSpace(errOut.String()), "fold: "+errSettleConnect.Error(); got != want {
				t.Fatalf("stderr = %q, want %q", got, want)
			}
			for _, s := range append(sentinels, url, dir, "postgres://") {
				if strings.Contains(errOut.String(), s) {
					t.Fatalf("stderr leaks %q: %q", s, errOut.String())
				}
			}
			if len(settleTree(t, dir)) != 1 {
				t.Fatalf("a refused run changed the uploads: %v", settleTree(t, dir))
			}
		})
	}
}

func TestRestoreSettleUploadsUsageAndMissingFolder(t *testing.T) {
	name := testdb.Fresh(t, true)
	dir := settleCLIEnv(t, name)
	for _, args := range [][]string{{"restore"}, {"restore", "nope"}, {"restore", "settle-uploads", "extra"}} {
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut); code != 2 || out.Len() != 0 {
			t.Fatalf("%v: exit %d, stdout %q", args, code, out.String())
		}
	}
	gone := filepath.Join(dir, "no-such-volume")
	t.Setenv("UPLOAD_DIR", gone)
	var out, errOut bytes.Buffer
	if code := run([]string{"restore", "settle-uploads"}, &out, &errOut); code != 1 || strings.Contains(errOut.String(), gone) {
		t.Fatalf("missing uploads folder: exit %d, stderr %q", code, errOut.String())
	}
	if _, err := os.Stat(gone); err == nil {
		t.Fatal("the command made the uploads folder")
	}
	t.Setenv("DATABASE_URL", "")
	if code := run([]string{"restore", "settle-uploads"}, &out, &errOut); code != 1 {
		t.Fatalf("no DATABASE_URL: exit %d", code)
	}
	var usage bytes.Buffer
	run([]string{"--help"}, &usage, &usage)
	if !strings.Contains(usage.String(), "restore settle-uploads") {
		t.Fatalf("usage lacks the command: %q", usage.String())
	}
}
