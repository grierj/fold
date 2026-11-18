package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"
)

// The backup service (deploy/backup) publishes two small files into a
// directory the app mounts read-only (FOLD_BACKUP_DIR, M5.2): LAST_OK, one UTC
// RFC 3339 line written after a whole run succeeded, and status.json with
// details. This is the only reader. It answers a state and a few typed
// fields; file content, paths and read errors never leave it, because the
// Admin page, the API and the CLI all print its answer.

const (
	// backupStaleAfter is how old LAST_OK may be before the System page warns:
	// a nightly run plus a day and a half of grace for a retry or a slow
	// weekend. Exactly this old is still ok.
	backupStaleAfter = 36 * time.Hour
	// backupFutureSlack is how far ahead of the server's clock a timestamp may
	// be (clock drift between containers) before the file is called unreadable.
	backupFutureSlack = time.Hour

	// The producer writes about 22 bytes and 250 bytes. A file far past that
	// is not ours, so it is never read whole.
	maxBackupLastOKBytes = 128
	maxBackupStatusBytes = 4096
	// maxBackupSizeBytes rejects sizes no archive reaches (a petabyte).
	maxBackupSizeBytes = int64(1) << 50
)

// The backup states (backupStatusJSON.State). Unwatched: no directory is
// configured. Missing: it is readable and no backup has finished yet.
// Unreadable: something is there that Fold cannot trust.
const (
	backupUnwatched  = "unwatched"
	backupMissing    = "missing"
	backupUnreadable = "unreadable"
	backupStale      = "stale"
	backupOK         = "ok"
)

// backupSteps are the steps status.json names (deploy/backup/README.md). A
// step this build does not know becomes null, so a newer service cannot
// make the page invent wording.
var backupSteps = []string{"config", "uploads", "dump", "expire", "encrypt", "upload"}

// backupStampRe is the one timestamp shape the service writes.
var backupStampRe = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$`)

func parseBackupStamp(s string) (time.Time, bool) {
	if !backupStampRe.MatchString(s) {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	return t, err == nil
}

// backupFile is the outcome of reading one status file.
type backupFile int

const (
	backupFileRead backupFile = iota
	backupFileAbsent
	backupFileBad
)

// readBackupFile reads one regular file of the status directory, at most
// limit bytes. A symlink, a special file, an oversize file or any read error
// is bad; only a file that is not there is absent.
func readBackupFile(dir, name string, limit int64) ([]byte, backupFile) {
	p := filepath.Join(dir, name)
	before, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, backupFileAbsent
	}
	if err != nil || !before.Mode().IsRegular() || before.Size() > limit {
		return nil, backupFileBad
	}
	f, ok := openVerifiedBackupFile(p, before)
	if !ok {
		return nil, backupFileBad
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, backupFileBad
	}
	return b, backupFileRead
}

// openVerifiedBackupFile opens the file readBackupFile just saw and requires
// the opened descriptor to be a regular file with the same identity.
// O_NONBLOCK keeps a FIFO swapped in after the Lstat from blocking the
// caller, as in storage's openVerifiedEntry.
func openVerifiedBackupFile(p string, want fs.FileInfo) (*os.File, bool) {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, false
	}
	got, err := f.Stat()
	if err != nil || !got.Mode().IsRegular() || !os.SameFile(want, got) {
		f.Close()
		return nil, false
	}
	return f, true
}

// backupLastOK is status.json's lastOk once every field checked out.
type backupLastOK struct {
	finishedAt   string
	dumpBytes    int64
	uploadsBytes int64
	offsite      string
}

// backupFailure is status.json's lastFailure; step is "" for one this build
// does not know.
type backupFailure struct {
	at   time.Time
	step string
}

// backupDetails is whatever of status.json is trustworthy. Each half stands
// alone, so a bad lastFailure never costs the sizes and the other way round.
type backupDetails struct {
	lastOK  *backupLastOK
	failure *backupFailure
}

func nonNegativeSize(n *int64) bool { return n != nil && *n >= 0 && *n <= maxBackupSizeBytes }

// readBackupDetails reads status.json: format 1 only (a newer format may mean
// different things by the same keys), and any problem yields nothing for that
// half. It never fails the status; LAST_OK alone decides the state.
func readBackupDetails(dir string) backupDetails {
	b, res := readBackupFile(dir, "status.json", maxBackupStatusBytes)
	if res != backupFileRead {
		return backupDetails{}
	}
	var top struct {
		Format      json.RawMessage `json:"format"`
		LastOK      json.RawMessage `json:"lastOk"`
		LastFailure json.RawMessage `json:"lastFailure"`
	}
	if json.Unmarshal(b, &top) != nil || !bytes.Equal(bytes.TrimSpace(top.Format), []byte("1")) {
		return backupDetails{}
	}
	var out backupDetails

	var ok struct {
		FinishedAt   string `json:"finishedAt"`
		DumpBytes    *int64 `json:"dumpBytes"`
		UploadsBytes *int64 `json:"uploadsBytes"`
		UploadsFiles *int64 `json:"uploadsFiles"`
		Offsite      string `json:"offsite"`
	}
	if json.Unmarshal(top.LastOK, &ok) == nil && nonNegativeSize(ok.DumpBytes) && nonNegativeSize(ok.UploadsBytes) &&
		(ok.UploadsFiles == nil || nonNegativeSize(ok.UploadsFiles)) && (ok.Offsite == "off" || ok.Offsite == "uploaded") {
		out.lastOK = &backupLastOK{finishedAt: ok.FinishedAt, dumpBytes: *ok.DumpBytes, uploadsBytes: *ok.UploadsBytes, offsite: ok.Offsite}
	}

	var fail struct {
		At   string          `json:"at"`
		Step json.RawMessage `json:"step"`
	}
	if json.Unmarshal(top.LastFailure, &fail) == nil {
		if at, ok := parseBackupStamp(fail.At); ok {
			var step string
			if json.Unmarshal(fail.Step, &step) != nil || !slices.Contains(backupSteps, step) {
				step = ""
			}
			out.failure = &backupFailure{at: at, step: step}
		}
	}
	return out
}

// readBackupStatus is the whole answer for the directory at now. dir "" means
// nobody watches. A directory that cannot be read, a LAST_OK that is not one
// UTC timestamp line or lies more than an hour ahead, and anything oversize or
// not a plain file are unreadable, with no other field set: Fold does not
// know what to trust.
func readBackupStatus(dir string, now time.Time) backupStatusJSON {
	if dir == "" {
		return backupStatusJSON{State: backupUnwatched}
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return backupStatusJSON{State: backupUnreadable}
	}
	raw, res := readBackupFile(dir, "LAST_OK", maxBackupLastOKBytes)
	if res == backupFileBad {
		return backupStatusJSON{State: backupUnreadable}
	}
	details := readBackupDetails(dir)

	out := backupStatusJSON{State: backupMissing}
	var lastOK *time.Time
	if res == backupFileRead {
		line := strings.TrimSuffix(string(raw), "\n")
		t, ok := parseBackupStamp(line)
		if !ok || t.After(now.Add(backupFutureSlack)) {
			return backupStatusJSON{State: backupUnreadable}
		}
		lastOK = &t
		out.LastOkAt = lastOK
		out.State = backupOK
		if now.Sub(t) > backupStaleAfter {
			out.State = backupStale
		}
		// The details belong to this LAST_OK only when they name the same run.
		if d := details.lastOK; d != nil && d.finishedAt == line {
			out.DumpBytes, out.UploadsBytes, out.Offsite = &d.dumpBytes, &d.uploadsBytes, &d.offsite
		}
	}
	// A failure matters when no run has finished, or a later try failed; an
	// older one is history the last success already answered.
	if f := details.failure; f != nil && !f.at.After(now.Add(backupFutureSlack)) && (lastOK == nil || f.at.After(*lastOK)) {
		at := f.at
		out.LastFailedAt = &at
		if f.step != "" {
			step := f.step
			out.LastFailedStep = &step
		}
	}
	return out
}
