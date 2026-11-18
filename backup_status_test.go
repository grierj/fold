package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

var backupTestNow = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

func stampOf(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// Sentinels that must never reach an answer: a path fragment, a bucket name and
// a raw error line a hostile or newer status file could carry.
const backupSentinel = "SENTINEL-bucket-s3.example.invalid-/var/secret/path"

// backupDirWith writes the given files (name -> content) into a new directory.
func backupDirWith(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func statusFile(lastOK, lastFailure string) string {
	return `{"format":1,"lastOk":` + lastOK + `,"lastFailure":` + lastFailure + `}` + "\n"
}

func lastOKDetails(at time.Time, offsite string) string {
	return `{"finishedAt":"` + stampOf(at) + `","dumpBytes":1234,"uploadsBytes":5678,"uploadsFiles":9,"offsite":"` + offsite + `"}`
}

func failureDetails(at time.Time, step string) string {
	return `{"at":"` + stampOf(at) + `","step":"` + step + `"}`
}

// assertNoLeak fails if any text of the answer carries a sentinel, a path or a
// file's content.
func assertNoLeak(t *testing.T, dir string, st backupStatusJSON) {
	t.Helper()
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"SENTINEL", "s3.example", "/var/", dir, "LAST_OK", "status.json"} {
		if strings.Contains(string(b), bad) {
			t.Fatalf("answer %s leaks %q", b, bad)
		}
	}
}

func TestReadBackupStatusStates(t *testing.T) {
	ok := backupTestNow.Add(-3 * time.Hour)
	for name, tc := range map[string]struct {
		files map[string]string
		want  string
	}{
		"fresh":                    {map[string]string{"LAST_OK": stampOf(ok) + "\n"}, backupOK},
		"no trailing newline":      {map[string]string{"LAST_OK": stampOf(ok)}, backupOK},
		"exactly 36h is ok":        {map[string]string{"LAST_OK": stampOf(backupTestNow.Add(-backupStaleAfter)) + "\n"}, backupOK},
		"one second past 36h":      {map[string]string{"LAST_OK": stampOf(backupTestNow.Add(-backupStaleAfter-time.Second)) + "\n"}, backupStale},
		"a week old":               {map[string]string{"LAST_OK": stampOf(backupTestNow.Add(-7*24*time.Hour)) + "\n"}, backupStale},
		"empty directory":          {map[string]string{}, backupMissing},
		"only status.json":         {map[string]string{"status.json": statusFile("null", "null")}, backupMissing},
		"empty LAST_OK":            {map[string]string{"LAST_OK": ""}, backupUnreadable},
		"garbage":                  {map[string]string{"LAST_OK": backupSentinel + "\n"}, backupUnreadable},
		"offset instead of Z":      {map[string]string{"LAST_OK": "2026-11-15T09:00:00+00:00\n"}, backupUnreadable},
		"fractional seconds":       {map[string]string{"LAST_OK": "2026-11-15T09:00:00.5Z\n"}, backupUnreadable},
		"two lines":                {map[string]string{"LAST_OK": stampOf(ok) + "\n" + stampOf(ok) + "\n"}, backupUnreadable},
		"padded with a space":      {map[string]string{"LAST_OK": " " + stampOf(ok) + "\n"}, backupUnreadable},
		"carriage return":          {map[string]string{"LAST_OK": stampOf(ok) + "\r\n"}, backupUnreadable},
		"impossible date":          {map[string]string{"LAST_OK": "2026-13-45T25:61:61Z\n"}, backupUnreadable},
		"oversize":                 {map[string]string{"LAST_OK": stampOf(ok) + "\n" + strings.Repeat("x", 4096)}, backupUnreadable},
		"an hour ahead is allowed": {map[string]string{"LAST_OK": stampOf(backupTestNow.Add(time.Hour)) + "\n"}, backupOK},
		"past an hour ahead":       {map[string]string{"LAST_OK": stampOf(backupTestNow.Add(time.Hour+time.Second)) + "\n"}, backupUnreadable},
	} {
		t.Run(name, func(t *testing.T) {
			dir := backupDirWith(t, tc.files)
			got := readBackupStatus(dir, backupTestNow)
			if got.State != tc.want {
				t.Fatalf("state = %q, want %q (%+v)", got.State, tc.want, got)
			}
			assertNoLeak(t, dir, got)
			if got.State == backupUnreadable && (got.LastOkAt != nil || got.DumpBytes != nil || got.LastFailedAt != nil) {
				t.Fatalf("unreadable carries fields: %+v", got)
			}
		})
	}
}

func TestReadBackupStatusUnwatchedAndBadDirectory(t *testing.T) {
	if got := readBackupStatus("", backupTestNow); got != (backupStatusJSON{State: backupUnwatched}) {
		t.Fatalf("unwatched = %+v", got)
	}
	dir := t.TempDir()
	missing := filepath.Join(dir, "gone")
	if got := readBackupStatus(missing, backupTestNow); got.State != backupUnreadable {
		t.Fatalf("missing directory = %+v", got)
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readBackupStatus(file, backupTestNow); got.State != backupUnreadable {
		t.Fatalf("a file as the directory = %+v", got)
	}
}

func TestReadBackupStatusRefusesSpecialFiles(t *testing.T) {
	ok := stampOf(backupTestNow.Add(-time.Hour)) + "\n"
	t.Run("directory named LAST_OK", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "LAST_OK"), 0o700); err != nil {
			t.Fatal(err)
		}
		if got := readBackupStatus(dir, backupTestNow); got.State != backupUnreadable {
			t.Fatalf("state = %q", got.State)
		}
	})
	t.Run("symlink named LAST_OK", func(t *testing.T) {
		dir := backupDirWith(t, map[string]string{"real": ok})
		if err := os.Symlink(filepath.Join(dir, "real"), filepath.Join(dir, "LAST_OK")); err != nil {
			t.Skip("no symlinks here")
		}
		if got := readBackupStatus(dir, backupTestNow); got.State != backupUnreadable {
			t.Fatalf("state = %q", got.State)
		}
	})
	t.Run("symlinked status.json is not read", func(t *testing.T) {
		dir := backupDirWith(t, map[string]string{
			"LAST_OK": ok,
			"real":    statusFile(lastOKDetails(backupTestNow.Add(-time.Hour), "uploaded"), "null"),
		})
		if err := os.Symlink(filepath.Join(dir, "real"), filepath.Join(dir, "status.json")); err != nil {
			t.Skip("no symlinks here")
		}
		got := readBackupStatus(dir, backupTestNow)
		if got.State != backupOK || got.DumpBytes != nil {
			t.Fatalf("got %+v", got)
		}
	})
}

func TestReadBackupStatusDetails(t *testing.T) {
	at := backupTestNow.Add(-5 * time.Hour)
	line := stampOf(at) + "\n"

	t.Run("matching format 1 details", func(t *testing.T) {
		dir := backupDirWith(t, map[string]string{"LAST_OK": line, "status.json": statusFile(lastOKDetails(at, "uploaded"), "null")})
		got := readBackupStatus(dir, backupTestNow)
		if got.State != backupOK || got.LastOkAt == nil || !got.LastOkAt.Equal(at) {
			t.Fatalf("got %+v", got)
		}
		if got.DumpBytes == nil || *got.DumpBytes != 1234 || got.UploadsBytes == nil || *got.UploadsBytes != 5678 ||
			got.Offsite == nil || *got.Offsite != "uploaded" || got.LastFailedAt != nil || got.LastFailedStep != nil {
			t.Fatalf("details = %+v", got)
		}
		assertNoLeak(t, dir, got)
	})

	t.Run("zero sizes are real sizes", func(t *testing.T) {
		details := `{"finishedAt":"` + stampOf(at) + `","dumpBytes":0,"uploadsBytes":0,"uploadsFiles":0,"offsite":"off"}`
		dir := backupDirWith(t, map[string]string{"LAST_OK": line, "status.json": statusFile(details, "null")})
		got := readBackupStatus(dir, backupTestNow)
		if got.DumpBytes == nil || *got.DumpBytes != 0 || got.Offsite == nil || *got.Offsite != "off" {
			t.Fatalf("got %+v", got)
		}
	})

	for name, body := range map[string]string{
		"format 2":             `{"format":2,"lastOk":` + lastOKDetails(at, "uploaded") + `,"lastFailure":null}`,
		"format missing":       `{"lastOk":` + lastOKDetails(at, "uploaded") + `,"lastFailure":null}`,
		"format a string":      `{"format":"1","lastOk":` + lastOKDetails(at, "uploaded") + `,"lastFailure":null}`,
		"not json":             backupSentinel,
		"truncated":            `{"format":1,"lastOk":{"finishedAt":`,
		"finishedAt differs":   statusFile(lastOKDetails(at.Add(-time.Hour), "uploaded"), "null"),
		"finishedAt is newer":  statusFile(lastOKDetails(at.Add(time.Hour), "uploaded"), "null"),
		"negative size":        statusFile(`{"finishedAt":"`+stampOf(at)+`","dumpBytes":-1,"uploadsBytes":5,"uploadsFiles":1,"offsite":"off"}`, "null"),
		"fractional size":      statusFile(`{"finishedAt":"`+stampOf(at)+`","dumpBytes":1.5,"uploadsBytes":5,"uploadsFiles":1,"offsite":"off"}`, "null"),
		"size as a string":     statusFile(`{"finishedAt":"`+stampOf(at)+`","dumpBytes":"5","uploadsBytes":5,"uploadsFiles":1,"offsite":"off"}`, "null"),
		"size beyond reason":   statusFile(`{"finishedAt":"`+stampOf(at)+`","dumpBytes":9007199254740991,"uploadsBytes":5,"uploadsFiles":1,"offsite":"off"}`, "null"),
		"missing size":         statusFile(`{"finishedAt":"`+stampOf(at)+`","uploadsBytes":5,"uploadsFiles":1,"offsite":"off"}`, "null"),
		"offsite not in enum":  statusFile(`{"finishedAt":"`+stampOf(at)+`","dumpBytes":5,"uploadsBytes":5,"uploadsFiles":1,"offsite":"`+backupSentinel+`"}`, "null"),
		"lastOk null":          statusFile("null", "null"),
		"lastOk wrong type":    statusFile(`"`+backupSentinel+`"`, "null"),
		"negative file count":  statusFile(`{"finishedAt":"`+stampOf(at)+`","dumpBytes":5,"uploadsBytes":5,"uploadsFiles":-3,"offsite":"off"}`, "null"),
		"oversize status.json": statusFile(lastOKDetails(at, "off"), "null") + strings.Repeat(" ", 8192),
	} {
		t.Run("bad details keep the timestamp: "+name, func(t *testing.T) {
			dir := backupDirWith(t, map[string]string{"LAST_OK": line, "status.json": body})
			got := readBackupStatus(dir, backupTestNow)
			if got.State != backupOK || got.LastOkAt == nil || !got.LastOkAt.Equal(at) {
				t.Fatalf("timestamp lost: %+v", got)
			}
			if got.DumpBytes != nil || got.UploadsBytes != nil || got.Offsite != nil {
				t.Fatalf("details used: %+v", got)
			}
			assertNoLeak(t, dir, got)
		})
	}

	t.Run("a bad lastOk does not cost the failure, nor the other way round", func(t *testing.T) {
		later := at.Add(time.Hour)
		dir := backupDirWith(t, map[string]string{"LAST_OK": line,
			"status.json": statusFile(`{"finishedAt":"nope"}`, failureDetails(later, "upload"))})
		got := readBackupStatus(dir, backupTestNow)
		if got.DumpBytes != nil || got.LastFailedAt == nil || got.LastFailedStep == nil || *got.LastFailedStep != "upload" {
			t.Fatalf("got %+v", got)
		}
		dir = backupDirWith(t, map[string]string{"LAST_OK": line,
			"status.json": statusFile(lastOKDetails(at, "off"), `{"at":"nope","step":"dump"}`)})
		got = readBackupStatus(dir, backupTestNow)
		if got.DumpBytes == nil || got.LastFailedAt != nil {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("failure ordering", func(t *testing.T) {
		for name, tc := range map[string]struct {
			failAt time.Time
			want   bool
		}{
			"older than LAST_OK":     {at.Add(-time.Hour), false},
			"same second as LAST_OK": {at, false},
			"newer than LAST_OK":     {at.Add(time.Second), true},
			"an hour ahead of now":   {backupTestNow.Add(time.Hour), true},
			"past an hour ahead":     {backupTestNow.Add(time.Hour + time.Second), false},
		} {
			dir := backupDirWith(t, map[string]string{"LAST_OK": line,
				"status.json": statusFile(lastOKDetails(at, "uploaded"), failureDetails(tc.failAt, "dump"))})
			got := readBackupStatus(dir, backupTestNow)
			if (got.LastFailedAt != nil) != tc.want {
				t.Errorf("%s: failure shown = %v, want %v (%+v)", name, got.LastFailedAt != nil, tc.want, got)
			}
			if got.State != backupOK || got.LastOkAt == nil || !got.LastOkAt.Equal(at) || got.DumpBytes == nil {
				t.Errorf("%s: a failure changed the last success: %+v", name, got)
			}
		}
	})

	t.Run("a failure on a stale backup", func(t *testing.T) {
		old := backupTestNow.Add(-50 * time.Hour)
		dir := backupDirWith(t, map[string]string{"LAST_OK": stampOf(old) + "\n",
			"status.json": statusFile(lastOKDetails(old, "off"), failureDetails(backupTestNow.Add(-time.Hour), "config"))})
		got := readBackupStatus(dir, backupTestNow)
		if got.State != backupStale || got.LastFailedStep == nil || *got.LastFailedStep != "config" {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("no LAST_OK yet: the failure is shown", func(t *testing.T) {
		failAt := backupTestNow.Add(-2 * time.Hour)
		dir := backupDirWith(t, map[string]string{"status.json": statusFile("null", failureDetails(failAt, "encrypt"))})
		got := readBackupStatus(dir, backupTestNow)
		if got.State != backupMissing || got.LastFailedAt == nil || !got.LastFailedAt.Equal(failAt) || got.LastFailedStep == nil || *got.LastFailedStep != "encrypt" {
			t.Fatalf("got %+v", got)
		}
		if got.LastOkAt != nil || got.DumpBytes != nil {
			t.Fatalf("invented a success: %+v", got)
		}
	})

	t.Run("every known step, and unknown ones become null", func(t *testing.T) {
		for _, step := range backupSteps {
			dir := backupDirWith(t, map[string]string{"status.json": statusFile("null", failureDetails(backupTestNow.Add(-time.Hour), step))})
			if got := readBackupStatus(dir, backupTestNow); got.LastFailedStep == nil || *got.LastFailedStep != step {
				t.Errorf("step %s: %+v", step, got)
			}
		}
		for _, step := range []string{"restore", "", "DUMP", backupSentinel} {
			dir := backupDirWith(t, map[string]string{"status.json": statusFile("null", failureDetails(backupTestNow.Add(-time.Hour), step))})
			got := readBackupStatus(dir, backupTestNow)
			if got.LastFailedAt == nil || got.LastFailedStep != nil {
				t.Errorf("step %q: %+v", step, got)
			}
			assertNoLeak(t, dir, got)
		}
		dir := backupDirWith(t, map[string]string{"status.json": statusFile("null", `{"at":"`+stampOf(backupTestNow.Add(-time.Hour))+`","step":7}`)})
		if got := readBackupStatus(dir, backupTestNow); got.LastFailedAt == nil || got.LastFailedStep != nil {
			t.Errorf("numeric step: %+v", got)
		}
	})

	t.Run("an unreadable LAST_OK shows no failure", func(t *testing.T) {
		dir := backupDirWith(t, map[string]string{"LAST_OK": "junk\n",
			"status.json": statusFile("null", failureDetails(backupTestNow.Add(-time.Hour), "dump"))})
		got := readBackupStatus(dir, backupTestNow)
		if got != (backupStatusJSON{State: backupUnreadable}) {
			t.Fatalf("got %+v", got)
		}
	})
}

// The answer is the contract: JSON null, not zero or empty, for what is not known.
func TestBackupStatusJSONUsesNullForUnknowns(t *testing.T) {
	b, err := json.Marshal(readBackupStatus("", backupTestNow))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"state":"unwatched","lastOkAt":null,"dumpBytes":null,"uploadsBytes":null,"offsite":null,"lastFailedAt":null,"lastFailedStep":null}`
	if string(b) != want {
		t.Fatalf("got %s\nwant %s", b, want)
	}
}

// A FIFO swapped in between the Lstat and the open must be refused at once:
// a blocking open would wait for a writer and hang the API request or CLI.
func TestOpenVerifiedBackupFileRefusesSwappedFIFO(t *testing.T) {
	dir := backupDirWith(t, map[string]string{"LAST_OK": stampOf(backupTestNow) + "\n"})
	p := filepath.Join(dir, "LAST_OK")
	before, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Skip("no FIFOs here")
	}

	done := make(chan bool, 1)
	go func() {
		f, ok := openVerifiedBackupFile(p, before)
		if f != nil {
			f.Close()
		}
		done <- ok
	}()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("a FIFO was accepted as the checked regular file")
		}
	case <-time.After(5 * time.Second):
		// Release the blocked open so the goroutine ends.
		if w, err := os.OpenFile(p, os.O_RDWR, 0); err == nil {
			w.Close()
		}
		t.Fatal("opening a swapped-in FIFO blocked")
	}
}
