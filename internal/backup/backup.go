// Package backup takes the nightly copy of the database and restores one.
//
// A backup is a VACUUM INTO copy in DATA_DIR/backups, with a checkpoint file
// beside it naming the audit log's last row and hash at that moment
// (docs/DECISIONS.md P17). Backups stay on the server's volume; copying them
// elsewhere is the church's choice (docs/OPERATIONS.md).
//
// A restore is staged: the backup is copied to a staging file, checked,
// migrated, and finished there (sessions ended, links expired, pending mail
// dropped, the "restored" audit row written). Only then is it copied over
// the live database in one step, so a crash leaves either the old database or
// the finished one (docs/HAZARDS.md section 5).
package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"fold/internal/audit"
	"fold/internal/clock"
	"fold/internal/db"
	"fold/internal/keys"
	"fold/internal/write"
)

// Keep is how many backups are kept; older ones are deleted.
const Keep = 14

// Meta is the checkpoint file written beside each backup.
type Meta struct {
	Name       string           `json:"name"`
	TakenAt    time.Time        `json:"taken_at"`
	Schema     int              `json:"schema"`
	Checkpoint audit.Checkpoint `json:"checkpoint"`
}

var namePattern = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}(-[0-9]{1,3})?$`)

// ValidName refuses anything that is not a backup's name, so a name from the
// command line can never reach outside the backups folder.
func ValidName(s string) bool { return namePattern.MatchString(s) }

func Dir(dataDir string) string { return filepath.Join(dataDir, "backups") }

func paths(dir, name string) (dbPath, metaPath string) {
	return filepath.Join(dir, name+".db"), filepath.Join(dir, name+".json")
}

// Take writes a backup and its checkpoint, records it in the audit log, and
// deletes backups beyond Keep.
func Take(ctx context.Context, e *write.Executor, dataDir string) (Meta, error) {
	dir := Dir(dataDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Meta{}, err
	}
	now := e.Clock().Now().UTC()
	// Two backups in one second (the copy kept before a restore, say) get
	// distinct names; a backup is never overwritten.
	name := now.Format("20060102-150405")
	for i := 2; exists(dir, name); i++ {
		name = fmt.Sprintf("%s-%d", now.Format("20060102-150405"), i)
	}
	dbPath, metaPath := paths(dir, name)
	tmp := dbPath + ".partial"
	os.Remove(tmp)
	cp, err := e.VacuumInto(ctx, tmp)
	if err != nil {
		return Meta{}, err
	}
	m := Meta{Name: name, TakenAt: now, Schema: db.Latest(), Checkpoint: cp}
	b, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(metaPath, append(b, '\n'), 0o600); err != nil {
		return Meta{}, err
	}
	if err := os.Rename(tmp, dbPath); err != nil {
		return Meta{}, err
	}
	if err := e.Run(ctx, write.System(), taken{m}); err != nil {
		return Meta{}, err
	}
	return m, prune(dir)
}

type taken struct{ m Meta }

func (taken) Action() string { return "backup.taken" }

func (c taken) Apply(tx *write.Tx) error {
	tx.Detail("name", c.m.Name)
	tx.Detail("seq", strconv.FormatInt(c.m.Checkpoint.Seq, 10))
	return nil
}

// List is every backup, newest first.
func List(dataDir string) ([]Meta, error) {
	entries, err := os.ReadDir(Dir(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Meta
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !ValidName(name) {
			continue
		}
		m, err := ReadMeta(dataDir, name)
		if err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(Dir(dataDir), name+".db")); err == nil {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out, nil
}

func ReadMeta(dataDir, name string) (Meta, error) {
	if !ValidName(name) {
		return Meta{}, fmt.Errorf("backup: %q is not a backup name", name)
	}
	_, metaPath := paths(Dir(dataDir), name)
	b, err := os.ReadFile(metaPath)
	if err != nil {
		return Meta{}, err
	}
	var m Meta
	return m, json.Unmarshal(b, &m)
}

func prune(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if n, ok := strings.CutSuffix(e.Name(), ".db"); ok && ValidName(n) {
			names = append(names, n)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	for _, n := range names[min(len(names), Keep):] {
		d, m := paths(dir, n)
		os.Remove(d)
		os.Remove(m)
	}
	return nil
}

// Prepare stages a backup for restoring and returns the staging file's path.
func Prepare(ctx context.Context, dataDir, name string, c clock.Clock, k *keys.Keys) (string, error) {
	meta, err := ReadMeta(dataDir, name)
	if err != nil {
		return "", fmt.Errorf("backup: no backup named %s: %w", name, err)
	}
	src, _ := paths(Dir(dataDir), name)
	staging := filepath.Join(dataDir, "restore-staging.db")
	for _, p := range []string{staging, staging + "-wal", staging + "-shm"} {
		os.Remove(p)
	}
	if err := copyFile(src, staging); err != nil {
		return "", err
	}
	w, err := db.OpenWriter(staging)
	if err != nil {
		return "", err
	}
	defer w.Close()
	e := write.New(w, c, k)
	if err := e.IntegrityCheck(ctx); err != nil {
		return "", err
	}
	if v, err := db.Version(ctx, w); err != nil {
		return "", err
	} else if v > db.Latest() {
		return "", db.ErrTooNew{Have: v, Know: db.Latest()}
	}
	rep, err := e.Verify(ctx)
	if err != nil {
		return "", err
	}
	if !rep.Intact() {
		return "", fmt.Errorf("backup: the audit log in %s is broken: %s", name, rep.Problem)
	}
	if ok, err := audit.Holds(ctx, w, meta.Checkpoint); err != nil || !ok {
		return "", fmt.Errorf("backup: %s does not match its checkpoint file", name)
	}
	if err := db.Migrate(ctx, w, "", c.Now()); err != nil {
		return "", err
	}
	if err := e.Run(ctx, write.System(), restored{meta}); err != nil {
		return "", err
	}
	if _, err := w.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return "", err
	}
	return staging, nil
}

// restored finishes a staged database: everything a restore must undo
// happens here, before it goes live.
type restored struct{ m Meta }

func (restored) Action() string { return "backup.restored" }

func (c restored) Apply(tx *write.Tx) error {
	now := tx.Now.Unix()
	// Sessions and links from the backup's day must not come back to life.
	if _, err := tx.Exec(`UPDATE sessions SET ended_at = ? WHERE ended_at IS NULL`, now); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE links SET cancelled_at = ? WHERE used_at IS NULL AND cancelled_at IS NULL`, now); err != nil {
		return err
	}
	// Mail waiting in the backup would carry expired links. Recurring work
	// keeps its schedule.
	if _, err := tx.Exec(`UPDATE jobs SET state = 'cancelled', secret = NULL, finished_at = ? WHERE state = 'pending' AND unique_key IS NULL`, now); err != nil {
		return err
	}
	tx.Detail("from", c.m.Name)
	tx.Detail("from_seq", strconv.FormatInt(c.m.Checkpoint.Seq, 10))
	return nil
}

// Restore replaces the live database with a prepared backup. A copy of the
// live database is kept first, as a backup like any other.
func Restore(ctx context.Context, e *write.Executor, dataDir, name string) (Meta, error) {
	staging, err := Prepare(ctx, dataDir, name, e.Clock(), e.Keys())
	if err != nil {
		return Meta{}, err
	}
	defer func() {
		for _, p := range []string{staging, staging + "-wal", staging + "-shm"} {
			os.Remove(p)
		}
	}()
	if _, err := Take(ctx, e, dataDir); err != nil {
		return Meta{}, fmt.Errorf("backup: could not keep a copy of the live database first: %w", err)
	}
	if err := e.ReplaceWith(ctx, staging); err != nil {
		return Meta{}, err
	}
	return ReadMeta(dataDir, name)
}

func exists(dir, name string) bool {
	d, m := paths(dir, name)
	_, errD := os.Stat(d)
	_, errM := os.Stat(m)
	return errD == nil || errM == nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
