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
	"database/sql"
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
	"fold/internal/settings"
	"fold/internal/write"
)

// Keep is how many backups are kept; older ones are deleted.
const Keep = 14

// Meta is the checkpoint file written beside each backup.
type Meta struct {
	Name       string           `json:"name"`
	TakenAt    time.Time        `json:"taken_at"`
	Schema     int              `json:"schema"`
	Note       string           `json:"note,omitempty"`
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
	m, err := take(ctx, e, dataDir, "")
	if err != nil {
		return Meta{}, err
	}
	return m, prune(Dir(dataDir))
}

func take(ctx context.Context, e *write.Executor, dataDir, note string) (Meta, error) {
	m, err := snapshot(ctx, dataDir, e.Clock().Now(), db.Latest(), note, e.VacuumInto)
	if err != nil {
		return Meta{}, err
	}
	return m, e.Run(ctx, write.System(), taken{m})
}

// snapshot copies the database and writes the checkpoint file beside it.
func snapshot(ctx context.Context, dataDir string, now time.Time, schema int, note string,
	vacuum func(context.Context, string) (audit.Checkpoint, error)) (Meta, error) {
	dir := Dir(dataDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Meta{}, err
	}
	now = now.UTC()
	// Two backups in one second (the copy kept before a restore, say) get
	// distinct names; a backup is never overwritten.
	name := now.Format("20060102-150405")
	for i := 2; exists(dir, name); i++ {
		name = fmt.Sprintf("%s-%d", now.Format("20060102-150405"), i)
	}
	dbPath, metaPath := paths(dir, name)
	tmp := dbPath + ".partial"
	os.Remove(tmp)
	cp, err := vacuum(ctx, tmp)
	if err != nil {
		return Meta{}, err
	}
	m := Meta{Name: name, TakenAt: now, Schema: schema, Note: note, Checkpoint: cp}
	b, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(metaPath, append(b, '\n'), 0o600); err != nil {
		return Meta{}, err
	}
	return m, os.Rename(tmp, dbPath)
}

// BeforeMigrate keeps a copy of a database that is about to be upgraded, as
// an ordinary backup, so going back is the old release plus fold restore
// (docs/DECISIONS.md R11). It runs at start, before anything else writes.
func BeforeMigrate(ctx context.Context, w *sql.DB, dataDir string, now time.Time, from int) (Meta, error) {
	return snapshot(ctx, dataDir, now, from, fmt.Sprintf("before upgrading from schema %d", from),
		func(ctx context.Context, path string) (audit.Checkpoint, error) {
			cp, err := audit.CheckpointOf(ctx, w)
			if err != nil {
				return cp, err
			}
			_, err = w.ExecContext(ctx, "VACUUM INTO ?", path)
			return cp, err
		})
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
	removeDB(staging)
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
	// Bring an older backup up to this release's schema first, so the chain
	// check reads it the way this release writes. Migrations never touch an
	// audit row's stored fields, so the chain and checkpoint still prove it.
	if err := db.Migrate(ctx, w); err != nil {
		return "", err
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
	// Mail settings sealed under a key this server no longer has would leave
	// the church unable to send a sign-in link once everyone is signed out.
	// Refuse before anything goes live; reseal any that need the old key.
	if _, err := settings.Reseal(tx); err != nil {
		return fmt.Errorf("backup: %w. Set SECRET_KEY_PREVIOUS to the key in use when %s was taken, then restore again (docs/OPERATIONS.md)", err, c.m.Name)
	}
	tx.Detail("from", c.m.Name)
	tx.Detail("from_seq", strconv.FormatInt(c.m.Checkpoint.Seq, 10))
	return nil
}

// Restore replaces the live database with a backup, through the running
// server's writer. A copy of the live database is kept first, as a backup
// like any other.
func Restore(ctx context.Context, e *write.Executor, dataDir, name string) (Meta, error) {
	meta, err := ReadMeta(dataDir, name)
	if err != nil {
		return Meta{}, fmt.Errorf("backup: no backup named %s: %w", name, err)
	}
	// The copy comes before the staged "restored" row, so the daily check
	// knows the restore explains it. Nothing is pruned until the end, so the
	// backup being restored cannot be deleted under it.
	if _, err := take(ctx, e, dataDir, "before restoring "+name); err != nil {
		return Meta{}, fmt.Errorf("backup: could not keep a copy of the live database first: %w", err)
	}
	staging, err := Prepare(ctx, dataDir, name, e.Clock(), e.Keys())
	if err != nil {
		return Meta{}, err
	}
	defer removeDB(staging)
	if err := e.ReplaceWith(ctx, staging); err != nil {
		return Meta{}, err
	}
	return meta, prune(Dir(dataDir))
}

// RestoreOffline restores with the server stopped, for when it will not
// start. It never opens the live database, which may be damaged or from a
// newer release: the live files are moved aside to a folder in the data
// directory, and the prepared backup takes their place. The caller holds the
// server's lock.
func RestoreOffline(ctx context.Context, dataDir, livePath, name string, c clock.Clock, k *keys.Keys) (Meta, error) {
	meta, err := ReadMeta(dataDir, name)
	if err != nil {
		return Meta{}, fmt.Errorf("backup: no backup named %s: %w", name, err)
	}
	staging, err := Prepare(ctx, dataDir, name, c, k)
	if err != nil {
		return Meta{}, err
	}
	defer removeDB(staging)
	aside := filepath.Join(dataDir, "before-restore-"+c.Now().UTC().Format("20060102-150405"))
	if err := os.MkdirAll(aside, 0o700); err != nil {
		return Meta{}, err
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		err := os.Rename(livePath+suffix, filepath.Join(aside, filepath.Base(livePath)+suffix))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Meta{}, err
		}
	}
	return meta, os.Rename(staging, livePath)
}

func removeDB(p string) {
	for _, s := range []string{"", "-wal", "-shm"} {
		os.Remove(p + s)
	}
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
