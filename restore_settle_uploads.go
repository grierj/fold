package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"

	"fold/internal/dbopen"
	"fold/migrations"
	"fold/storage"

	"gorm.io/gorm/logger"
)

// `fold restore settle-uploads` (M5.2). A restore that brings uploads back can
// bring back a photo of someone erased after the backup was taken. The
// restored database re-deletes that person in private recovery, but their
// folder is still on disk. This command runs inside the closed restore, after
// the app is stopped and before it reopens, and empties every avatar folder
// that belongs to a soft-deleted account or to no account. It starts no
// workers, runs no migrations and boots no restore fence, so nothing public
// and nothing asynchronous runs while it works. It is safe to run again, and
// it needs no archive bytes.
//
// Active accounts' folders are never touched, replaced old photos included:
// those are the running app's avatar.cleanup job to judge, on its own rule.
// No job row is read or changed here.

const (
	// settleBudgetDefault bounds the clearing passes together. A bigger tree
	// than one budget finishes on the next invocation.
	settleBudgetDefault = 10 * time.Minute
	// settleRescanBudgetDefault bounds the final look, which runs after the
	// clearing budget has ended, so exhausting the budget still reports counts.
	settleRescanBudgetDefault = time.Minute
	// settleLookupBatch is how many owners one users lookup covers.
	settleLookupBatch = 500
)

// The budgets are variables so a test can exhaust one without waiting.
var (
	settleBudget       = settleBudgetDefault
	settleRescanBudget = settleRescanBudgetDefault
)

// Fixed text, shown without a cause: nothing here names an id, key or path.
var (
	errSettleScan     = errors.New("could not list the profile photo folders")
	errSettleLookup   = errors.New("could not look up the accounts that own profile photo folders")
	errSettleSchema   = errors.New("the database schema does not match this Fold release; run this command with the release that matches the restored database")
	errSettleUploads  = errors.New("the uploads folder is missing")
	errSettleNotClear = errors.New("some profile photos of erased or unknown accounts could not be removed, or the photo folder holds entries this command does not own; the site must stay closed")
	errSettleConnect  = errors.New("could not connect to the database")
)

// settleCounts is the whole output: counts, never an id, key, name or path.
// Unexpected counts entries directly under the avatar folder whose name is not
// an account id. The store's scan skips them, so this command cannot judge
// them; they are left untouched and block success, because a photo under one
// could belong to an erased person.
type settleCounts struct {
	OwnersScanned  int `json:"ownersScanned"`
	ErasedCleared  int `json:"erasedCleared"`
	UnownedCleared int `json:"unownedCleared"`
	Remaining      int `json:"remaining"`
	Unexpected     int `json:"unexpected"`
}

// settleOwner is one avatar owner folder the scan found. Whether it is safe
// to enter is the store's call, made again by every removal.
type settleOwner struct{ id uint64 }

// settleClass says what a folder's owner is. Only erased and unowned folders
// are this command's to clear.
type settleClass int

const (
	settleActive settleClass = iota
	settleErased
	settleUnowned
)

// settleUploads clears every erased and unowned avatar folder, then looks
// again. It returns the counts, and errSettleNotClear when anything remains.
// A scan or lookup failure returns that fixed error and counts as not settled.
func (a *App) settleUploads(ctx context.Context) (settleCounts, error) {
	var counts settleCounts
	work, cancel := context.WithTimeout(ctx, settleBudget)
	defer cancel()

	owners, _, err := a.settleScan(work)
	if err != nil {
		return counts, err
	}
	counts.OwnersScanned = len(owners)

	for start := 0; start < len(owners) && work.Err() == nil; start += settleLookupBatch {
		batch := owners[start:min(start+settleLookupBatch, len(owners))]
		classes, err := a.settleClassify(work, batch)
		if err != nil {
			// The final look decides; a lookup that failed here leaves the
			// folders in place and they are counted there.
			break
		}
		for i, o := range batch {
			if work.Err() != nil {
				break
			}
			switch classes[i] {
			case settleErased:
				if a.settleClearErased(work, o) {
					counts.ErasedCleared++
				}
			case settleUnowned:
				if a.settleClearUnowned(work, o) {
					counts.UnownedCleared++
				}
			}
		}
	}

	// The final look uses its own budget, so a clearing pass that ran out of
	// time still ends with an honest count of what is left.
	look, stop := context.WithTimeout(ctx, settleRescanBudget)
	defer stop()
	left, unexpected, err := a.settleRemaining(look)
	if err != nil {
		return counts, err
	}
	counts.Remaining = left
	counts.Unexpected = unexpected
	if left > 0 || unexpected > 0 {
		return counts, errSettleNotClear
	}
	return counts, nil
}

// settleScan lists the avatar owner folders and counts the entries under the
// avatar folder that are not owner folders. It collects them all before any
// removal, so nothing is removed from a folder the scan is still reading.
func (a *App) settleScan(ctx context.Context) (owners []settleOwner, unexpected int, err error) {
	stats, err := a.Uploads.ScanOwners(ctx, storage.KindAvatars, 1, func(oe storage.OwnerEntries) error {
		owners = append(owners, settleOwner{id: oe.Owner})
		return nil
	})
	if err != nil {
		return nil, 0, errSettleScan
	}
	return owners, stats.SkippedNames, nil
}

// settleClassify looks up each owner's users row, soft-deleted rows included.
// An id past int64 cannot be an account and cannot be a bind value, so it is
// unowned without asking.
func (a *App) settleClassify(ctx context.Context, batch []settleOwner) ([]settleClass, error) {
	ids := make([]uint64, 0, len(batch))
	for _, o := range batch {
		if o.id <= math.MaxInt64 {
			ids = append(ids, o.id)
		}
	}
	deleted := map[uint64]bool{}
	if len(ids) > 0 {
		var rows []struct {
			ID      uint64
			Deleted bool
		}
		if err := a.DB.WithContext(ctx).Raw(`SELECT id, deleted_at IS NOT NULL AS deleted FROM users WHERE id IN ?`, ids).Scan(&rows).Error; err != nil {
			return nil, errSettleLookup
		}
		for _, r := range rows {
			deleted[r.ID] = r.Deleted
		}
	}
	classes := make([]settleClass, len(batch))
	for i, o := range batch {
		switch d, ok := deleted[o.id]; {
		case !ok:
			classes[i] = settleUnowned
		case d:
			classes[i] = settleErased
		default:
			classes[i] = settleActive
		}
	}
	return classes, nil
}

// settleClearErased runs the shared cleanup rule for a soft-deleted account
// until its folder is gone. It reports whether the folder ended cleared; any
// other outcome (unsafe, nested, out of time) is left for the final look.
func (a *App) settleClearErased(ctx context.Context, o settleOwner) bool {
	for ctx.Err() == nil {
		cctx, cancel := context.WithTimeout(ctx, avatarCleanupTxTimeout)
		found, err := a.cleanAvatarAccount(cctx, uint(o.id))
		cancel()
		switch {
		case errors.Is(err, storage.ErrMoreRemain):
			continue
		case err != nil:
			return false
		}
		// A row that vanished between the lookup and the lock cannot happen in
		// a closed restore (ids are never hard-deleted); the final look would
		// still catch the folder.
		return found
	}
	return false
}

// settleClearUnowned empties a folder with no users row. Only this closed
// command does so; the running app's job leaves such a folder alone.
func (a *App) settleClearUnowned(ctx context.Context, o settleOwner) bool {
	owner, err := storage.AvatarOwner(uint(o.id))
	if err != nil {
		return false
	}
	for ctx.Err() == nil {
		err := a.Uploads.CleanOwner(ctx, owner, "")
		switch {
		case errors.Is(err, storage.ErrMoreRemain):
			continue
		case err != nil:
			return false
		}
		return true
	}
	return false
}

// settleRemaining scans again and counts the folders of erased or unowned
// accounts that are still present, whatever they hold (a cleared folder is
// gone, so a present one is an unsafe, nested or unfinished one), and the
// entries that are not owner folders at all.
func (a *App) settleRemaining(ctx context.Context) (left, unexpected int, err error) {
	owners, unexpected, err := a.settleScan(ctx)
	if err != nil {
		return 0, 0, err
	}
	for start := 0; start < len(owners); start += settleLookupBatch {
		batch := owners[start:min(start+settleLookupBatch, len(owners))]
		classes, err := a.settleClassify(ctx, batch)
		if err != nil {
			return 0, 0, err
		}
		for _, c := range classes {
			if c != settleActive {
				left++
			}
		}
	}
	return left, unexpected, nil
}

// settleSchemaCurrent reports whether the database is at the newest migration
// this binary carries. It only reads: a database with no goose table (not a
// Fold database) is a mismatch and is left as it is.
func settleSchemaCurrent(ctx context.Context, db *sql.DB) (bool, error) {
	p, err := migrations.NewProvider(db)
	if err != nil {
		return false, err
	}
	sources := p.ListSources()
	if len(sources) == 0 {
		return false, nil
	}
	newest := sources[len(sources)-1].Version
	// Two statements, not one CASE: Postgres resolves every table name in a
	// statement before it runs any branch, so a lone SELECT naming a missing
	// goose_db_version fails instead of answering NULL.
	var present bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('goose_db_version') IS NOT NULL`).Scan(&present); err != nil {
		return false, err
	}
	if !present {
		return false, nil
	}
	var have sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT max(version_id) FROM goose_db_version`).Scan(&have); err != nil {
		return false, err
	}
	return have.Valid && have.Int64 == newest, nil
}

// runRestore is `fold restore ...`. Only settle-uploads exists. It reads
// DATABASE_URL and UPLOAD_DIR and nothing else: no session secret, no
// migrations. Exit 0 when no erased or unowned photo remains, 1 when one does
// or the work could not finish, 2 for bad usage or a schema that does not
// match this binary (nothing is touched in that case).
func runRestore(args []string, asJSON bool, stdout, stderr io.Writer) int {
	if len(args) != 1 || args[0] != "settle-uploads" {
		fmt.Fprintf(stderr, "fold: unknown restore command %q (want settle-uploads)\n", strings.Join(args, " "))
		return 2
	}
	url, err := databaseURL()
	if err != nil {
		fmt.Fprintln(stderr, "fold:", err)
		return 1
	}
	// The driver's error can carry the connection string or the server's
	// user, database and host, so none of it is printed or wrapped.
	db, err := dbopen.Open(url, logger.Discard)
	if err != nil {
		fmt.Fprintln(stderr, "fold:", errSettleConnect)
		return 1
	}
	sqlDB, err := db.DB()
	if err != nil {
		fmt.Fprintln(stderr, "fold: could not open the database")
		return 1
	}
	defer sqlDB.Close()

	ctx := context.Background()
	current, err := settleSchemaCurrent(ctx, sqlDB)
	if err != nil {
		fmt.Fprintln(stderr, "fold: could not read the database schema version")
		return 1
	}
	if !current {
		fmt.Fprintln(stderr, "fold:", errSettleSchema)
		return 2
	}
	// The uploads folder must already be there: a missing volume is not "no
	// photos", and this command never makes one.
	dir := getEnv("UPLOAD_DIR", "./data/uploads")
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		fmt.Fprintln(stderr, "fold:", errSettleUploads)
		return 1
	}
	uploads, err := storage.NewLocalStore(dir, uploadsURLPrefix)
	if err != nil {
		fmt.Fprintln(stderr, "fold:", errSettleUploads)
		return 1
	}
	app := &App{DB: db, Uploads: uploads}
	return app.settleUploadsCommand(ctx, asJSON, stdout, stderr)
}

// settleUploadsCommand runs settleUploads and prints its counts. The counts
// print on every outcome that reached the final look; a failure before it
// prints only the fixed message.
func (a *App) settleUploadsCommand(ctx context.Context, asJSON bool, stdout, stderr io.Writer) int {
	counts, err := a.settleUploads(ctx)
	if err != nil && !errors.Is(err, errSettleNotClear) {
		fmt.Fprintln(stderr, "fold:", err)
		return 1
	}
	if asJSON {
		_ = json.NewEncoder(stdout).Encode(counts)
	} else {
		fmt.Fprintf(stdout, "profile photo folders: %d scanned, %d erased cleared, %d unowned cleared, %d remaining, %d unexpected\n",
			counts.OwnersScanned, counts.ErasedCleared, counts.UnownedCleared, counts.Remaining, counts.Unexpected)
	}
	if err != nil {
		fmt.Fprintln(stderr, "fold:", err)
		return 1
	}
	return 0
}
