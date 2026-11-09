package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"strconv"
	"strings"
	"time"

	"fold/internal/jobs"
	"fold/models"
	"fold/storage"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Durable avatar cleanup (M7.4 R4-5). Nothing on a request path deletes an
// avatar file. An upload, a removal and an erasure change avatar_key under
// the user's row lock and queue one `avatar.cleanup` job in the same
// transaction. The job takes the same lock, reads the key as it is then, and
// removes everything else in the account's folder. A daily sweep queues the
// same job for folders a crash left behind. Both are safe to run twice.
const (
	kindAvatarCleanup = "avatar.cleanup"
	kindAvatarSweep   = "avatar.sweep"

	// avatarLockTimeout bounds the wait for a user row, separately from the
	// transaction's deadline.
	avatarLockTimeout = "5s"
	// avatarCleanupTxTimeout is the longest a cleanup run holds the row.
	avatarCleanupTxTimeout = 15 * time.Second
	// avatarTxTimeout is the longest an upload or removal holds the row after
	// its body is read; avatarHandlerBudget caps it from when the handler
	// started, so a slow body cannot push it past the 60s write timeout.
	avatarTxTimeout     = 20 * time.Second
	avatarHandlerBudget = 50 * time.Second
	avatarPutTimeout    = 10 * time.Second
	// avatarFallbackTimeout bounds the lock-free enqueue made after a failed
	// or ambiguous upload commit.
	avatarFallbackTimeout = 5 * time.Second

	avatarSweepLimit   = 500
	avatarSweepBatch   = 500
	avatarSweepNames   = 32
	avatarSweepTimeout = 5 * time.Minute
)

// Fixed-text errors. Each is what a job's last_error and the runner's log
// carry, so none depends on jobs.CleanError and none wraps a cause.
var (
	errAvatarCleanupPayload = errors.New("invalid avatar cleanup task")
	errAvatarKeyForeign     = errors.New("the stored profile photo is not in this account's folder")
	errAvatarFolderUnsafe   = errors.New("avatar folder is not a plain folder")
	errAvatarFolderNested   = errors.New("avatar folder holds an unexpected folder")
	errAvatarCleanupPartial = errors.New("some old profile photos could not be removed")
	errAvatarLockTimeout    = errors.New("could not lock the account for photo cleanup")
	errAvatarOutOfTime      = errors.New("photo cleanup ran out of time")
	errAvatarCleanupFailed  = errors.New("photo cleanup could not finish")

	errSweepZone         = errors.New("could not check for leftover profile photos (time zone)")
	errSweepUnsafe       = errors.New("the profile photo folder is not a plain folder")
	errSweepScan         = errors.New("could not check for leftover profile photos (scan)")
	errSweepLookup       = errors.New("could not check for leftover profile photos (lookup)")
	errSweepQueue        = errors.New("could not check for leftover profile photos (queue)")
	errSweepOutOfTime    = errors.New("could not check for leftover profile photos (out of time)")
	errAvatarSweepFailed = errors.New("could not check for leftover profile photos")
	// errSweepFull stops the scan once the run has its jobs. It is success.
	errSweepFull = errors.New("avatar sweep is full")

	// Upload and removal outcomes the handlers map to HTTP answers.
	errTeenAvatar       = errors.New("teen accounts cannot have a public profile picture")
	errAvatarStore      = errors.New("could not store avatar")
	errAvatarOutOfDate  = errors.New("avatar request ran out of time")
	errAvatarUnexpected = errors.New("avatar update changed no account")
)

// avatarCleanupJob is the whole payload: no key, no path, no name.
type avatarCleanupJob struct {
	UserID uint `json:"userId"`
}

func (a *App) avatarKinds() []jobs.Kind {
	return []jobs.Kind{{Name: kindAvatarCleanup, Label: "Removing old profile photos", MaxAttempts: jobs.DefaultMaxAttempts,
		Run: func(ctx context.Context, payload json.RawMessage) error {
			dec := json.NewDecoder(bytes.NewReader(payload))
			dec.DisallowUnknownFields()
			var p avatarCleanupJob
			if err := dec.Decode(&p); err != nil || p.UserID == 0 {
				return errAvatarCleanupPayload
			}
			if _, err := dec.Token(); err != io.EOF {
				return errAvatarCleanupPayload
			}
			return a.runAvatarCleanup(ctx, p.UserID)
		}}}
}

// queueAvatarCleanup queues cleanup for a user. Callers hold the user's row
// lock until commit. A job still queued has not run its handler, and the
// handler must take that lock, so it will see the committed key: nothing is
// added. A running, failed, done, dead or dismissed job never coalesces, so
// new work always lands after it. There is no unique key (it covers every
// status), so a sweep key never blocks it.
func queueAvatarCleanup(tx *gorm.DB, userID uint) error {
	if userID == 0 {
		return errAvatarCleanupPayload
	}
	var pending bool
	if err := tx.Raw(`SELECT EXISTS (SELECT 1 FROM jobs WHERE kind = ? AND status = ? AND payload = ?::jsonb)`,
		kindAvatarCleanup, models.JobQueued, fmt.Sprintf(`{"userId":%d}`, userID)).Scan(&pending).Error; err != nil {
		return err
	}
	if pending {
		return nil
	}
	_, err := jobs.Enqueue(tx, kindAvatarCleanup, avatarCleanupJob{UserID: userID}, time.Now(), "", jobs.DefaultMaxAttempts)
	return err
}

// avatarRow is the locked read of a user row. It is raw SQL so a tombstone
// shows up too.
type avatarRow struct {
	ID             uint
	AvatarKey      string
	Deleted        bool
	Status         string
	PasswordHash   *string
	SessionVersion int
	LogoutVersion  int
	AccountType    string
}

// lockAvatarRow takes the user row FOR NO KEY UPDATE, waiting at most
// avatarLockTimeout. The caller's transaction must carry a deadline context,
// so database/sql rolls it back (and frees the row) when time is up even if
// Go is still inside a blocked filesystem call.
func lockAvatarRow(tx *gorm.DB, id uint) (avatarRow, error) {
	if err := tx.Exec("SET LOCAL lock_timeout = '" + avatarLockTimeout + "'").Error; err != nil {
		return avatarRow{}, err
	}
	var row avatarRow
	err := tx.Raw(`SELECT id, COALESCE(avatar_key,'') AS avatar_key, deleted_at IS NOT NULL AS deleted, status,
		password_hash, session_version, logout_version, account_type
		FROM users WHERE id = ? FOR NO KEY UPDATE`, id).Scan(&row).Error
	return row, err
}

// checkAvatarSession compares the locked row with the request's snapshot.
func checkAvatarSession(row avatarRow, snap *models.User) error {
	current := models.User{ID: row.ID, Status: row.Status, PasswordHash: row.PasswordHash,
		SessionVersion: row.SessionVersion, LogoutVersion: row.LogoutVersion}
	if row.Deleted || !sessionCurrent(&current, snap) {
		return errSecuritySession
	}
	return nil
}

// runAvatarCleanup removes every file in the account's folder except its
// current avatar. The row lock is held for the whole run, so a concurrent
// upload either commits first (and its key is kept) or waits.
func (a *App) runAvatarCleanup(ctx context.Context, userID uint) error {
	owner, err := storage.AvatarOwner(userID)
	if err != nil {
		return errAvatarCleanupPayload
	}
	cctx, cancel := context.WithTimeout(ctx, avatarCleanupTxTimeout)
	defer cancel()
	err = a.DB.WithContext(cctx).Transaction(func(tx *gorm.DB) error {
		row, err := lockAvatarRow(tx, userID)
		if err != nil {
			return err
		}
		if row.ID == 0 {
			// No account: no upload can target it, and a restore may have left
			// the database behind the uploads. Touch nothing.
			return nil
		}
		keep := ""
		// Only deleted_at decides: a disabled or invited person's photo is
		// still their current photo. A tombstone keeps nothing.
		if !row.Deleted && row.AvatarKey != "" {
			kind, o, _, perr := storage.ParseKey(row.AvatarKey)
			if perr != nil || kind != storage.KindAvatars || o != uint64(row.ID) {
				return errAvatarKeyForeign
			}
			keep = row.AvatarKey
		}
		return a.Uploads.CleanOwner(cctx, owner, keep)
	})
	return mapAvatarCleanupError(cctx, err)
}

// mapAvatarCleanupError turns every outcome into fixed text, or a Later.
func mapAvatarCleanupError(cctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, storage.ErrMoreRemain):
		return jobs.Later(time.Now())
	case errors.Is(err, errAvatarKeyForeign):
		return errAvatarKeyForeign
	case errors.Is(err, storage.ErrUnsafeOwner):
		return errAvatarFolderUnsafe
	case errors.Is(err, storage.ErrUnexpectedEntry):
		return errAvatarFolderNested
	case errors.Is(err, storage.ErrCleanupIncomplete):
		return errAvatarCleanupPartial
	case pgCode(err) == pgLockNotAvailable:
		return errAvatarLockTimeout
	case cctx.Err() != nil, errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return errAvatarOutOfTime
	}
	return errAvatarCleanupFailed
}

// requestStartKey holds the time stampRequestStart saw the request.
const requestStartKey = "requestStart"

// stampRequestStart records when the request reached the router, before any
// middleware reads its body.
func stampRequestStart() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(requestStartKey, time.Now())
		c.Next()
	}
}

// requestStart is when the request reached the router. The CSRF middleware
// parses a multipart form, which reads a whole upload body before the handler
// runs, so a handler's own clock would start after a slow body.
func requestStart(c *gin.Context) time.Time {
	if v, ok := c.Get(requestStartKey); ok {
		if t, ok := v.(time.Time); ok {
			return t
		}
	}
	return time.Now()
}

// avatarTxDeadline is when an upload's or removal's transaction must end:
// avatarTxTimeout after the body was read, but never later than
// avatarHandlerBudget after the request reached the router (the server's 60s
// write timeout counts from the request headers). start is requestStart, not
// the handler's own clock: CSRF has already read the body by then.
func avatarTxDeadline(start, bodyRead time.Time) time.Time {
	deadline := bodyRead.Add(avatarTxTimeout)
	if budget := start.Add(avatarHandlerBudget); budget.Before(deadline) {
		return budget
	}
	return deadline
}

// storeAvatar makes r the user's avatar. Under the row lock it checks the
// session snapshot and account, puts the new file, points avatar_key at it
// and queues cleanup, all in one transaction that ends by deadline. It never
// deletes a file itself: the old one, and a new one a failed or ambiguous
// commit may have kept, are the cleanup job's to judge.
func (a *App) storeAvatar(ctx context.Context, u *models.User, r io.Reader, size int64, mime string, deadline time.Time) (string, error) {
	ext, ok := allowedImageTypes[mime]
	if !ok {
		return "", errAvatarStore
	}
	rid, err := randomHex(16)
	if err != nil {
		return "", err
	}
	if !time.Now().Before(deadline) {
		return "", errAvatarOutOfDate
	}
	key := storage.NewKey(storage.KindAvatars, strconv.FormatUint(uint64(u.ID), 10), rid, strings.TrimPrefix(ext, "."))
	tctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	putDone := false
	err = a.DB.WithContext(tctx).Transaction(func(tx *gorm.DB) error {
		row, err := lockAvatarRow(tx, u.ID)
		if err != nil {
			return err
		}
		if err := checkAvatarSession(row, u); err != nil {
			return err
		}
		if row.AccountType == models.AccountTeen {
			return errTeenAvatar
		}
		pctx, pcancel := context.WithTimeout(tctx, avatarPutTimeout)
		defer pcancel()
		if err := a.Uploads.Put(pctx, key, r, size, mime); err != nil {
			return errAvatarStore
		}
		putDone = true
		if err := setAvatarKey(tx, u.ID, key); err != nil {
			return err
		}
		return queueAvatarCleanup(tx, u.ID)
	})
	if err != nil {
		if putDone {
			a.queueAvatarCleanupFallback(u.ID)
		}
		return "", err
	}
	u.AvatarKey = key
	return a.Uploads.URL(key), nil
}

// removeAvatar clears the user's avatar key and always queues cleanup, so a
// repeated removal creates fresh work after a done, dead or dismissed job.
// It does not require CanUploadAvatar: a teen holding a legacy key can still
// remove it.
func (a *App) removeAvatar(ctx context.Context, u *models.User, deadline time.Time) error {
	if !time.Now().Before(deadline) {
		return errAvatarOutOfDate
	}
	tctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	err := a.DB.WithContext(tctx).Transaction(func(tx *gorm.DB) error {
		row, err := lockAvatarRow(tx, u.ID)
		if err != nil {
			return err
		}
		if err := checkAvatarSession(row, u); err != nil {
			return err
		}
		if row.AvatarKey != "" {
			if err := setAvatarKey(tx, u.ID, ""); err != nil {
				return err
			}
		}
		return queueAvatarCleanup(tx, u.ID)
	})
	if err != nil {
		return err
	}
	u.AvatarKey = ""
	return nil
}

// setAvatarKey writes the key on a live row. GORM adds deleted_at IS NULL.
func setAvatarKey(tx *gorm.DB, id uint, key string) error {
	res := tx.Model(&models.User{}).Where("id = ?", id).Update("avatar_key", key)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return errAvatarUnexpected
	}
	return nil
}

// queueAvatarCleanupFallback queues cleanup after an upload whose commit
// failed or is unknown. It takes no lock: the original transaction has ended.
// It logs no id or key.
func (a *App) queueAvatarCleanupFallback(id uint) {
	fctx, cancel := context.WithTimeout(context.Background(), avatarFallbackTimeout)
	defer cancel()
	if err := queueAvatarCleanup(a.DB.WithContext(fctx), id); err != nil {
		log.Println("avatar: could not queue cleanup")
	}
}

// ---------------------------------------------------------------------------
// Recovery sweep
// ---------------------------------------------------------------------------

// sweepAvatars is the daily avatar.sweep job.
func (a *App) sweepAvatars(ctx context.Context, now time.Time) error {
	return a.sweepAvatarsLimit(ctx, now, avatarSweepLimit)
}

// sweepAvatarsLimit queues avatar.cleanup, keyed for the church day, for up
// to limit owner folders that need work. One context, derived once, bounds
// the scan and every database call.
func (a *App) sweepAvatarsLimit(ctx context.Context, now time.Time, limit int) error {
	return fixedSweepError(a.sweepAvatarsRun(ctx, now, limit))
}

func (a *App) sweepAvatarsRun(ctx context.Context, now time.Time, limit int) error {
	sctx, cancel := context.WithTimeout(ctx, avatarSweepTimeout)
	defer cancel()
	db := a.DB.WithContext(sctx)
	zone, err := churchZoneIn(db)
	if err != nil {
		if sctx.Err() != nil {
			return errSweepOutOfTime
		}
		return errSweepZone
	}
	run := &avatarSweepRun{db: db, sctx: sctx, day: now.In(zone).Format("2006-01-02"), limit: limit}
	stats, err := a.Uploads.ScanOwners(sctx, storage.KindAvatars, avatarSweepNames, run.add)
	if err := sweepScanError(err, sctx.Err()); err != nil {
		return err
	}
	// The final flush: a partial batch, or none.
	if err := run.processBatch(); err != nil && err != errSweepFull {
		return err
	}
	var queued int64
	if len(run.specs) > 0 {
		var err error
		if queued, err = jobs.EnqueueMany(db, run.specs); err != nil {
			if sctx.Err() != nil {
				return errSweepOutOfTime
			}
			return errSweepQueue
		}
	}
	// Counts only: never an id, a key, a name or a path.
	log.Printf("avatars: sweep scanned %d owners, skipped %d names, %d with no account, %d unsafe with no account, %d already waiting, queued %d, cap reached %t",
		stats.Owners, stats.SkippedNames, run.noAccount, run.unsafeNoAccount, run.waiting, queued, run.capped)
	return nil
}

// sweepScanError maps ScanOwners' result. The sweep's own lookup and
// out-of-time errors come back through fn unchanged and are never remapped.
func sweepScanError(err, ctxErr error) error {
	switch {
	case err == nil || err == errSweepFull:
		return nil
	case err == errSweepLookup || err == errSweepOutOfTime:
		return err
	case ctxErr != nil, errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return errSweepOutOfTime
	case errors.Is(err, storage.ErrUnsafeOwner):
		return errSweepUnsafe
	}
	return errSweepScan
}

// fixedSweepError lets only the sweep's own fixed errors out.
func fixedSweepError(err error) error {
	switch err {
	case nil, errSweepZone, errSweepUnsafe, errSweepScan, errSweepLookup, errSweepQueue, errSweepOutOfTime, errAvatarSweepFailed:
		return err
	}
	return errAvatarSweepFailed
}

// avatarSweepRun is one sweep's state: a buffer of scanned owners looked up
// in batches, and the jobs they earned.
type avatarSweepRun struct {
	db    *gorm.DB
	sctx  context.Context
	day   string
	limit int

	buf   []storage.OwnerEntries
	specs []jobs.Spec

	noAccount, unsafeNoAccount, waiting int
	capped                              bool
}

// add is ScanOwners' callback.
func (s *avatarSweepRun) add(oe storage.OwnerEntries) error {
	s.buf = append(s.buf, oe)
	if len(s.buf) >= avatarSweepBatch {
		return s.processBatch()
	}
	return nil
}

// processBatch decides the buffered owners with two unlocked reads and
// clears the buffer. An empty buffer runs nothing. It returns nil,
// errSweepFull once the run holds its limit, or a fixed lookup error.
func (s *avatarSweepRun) processBatch() error {
	if len(s.buf) == 0 {
		return nil
	}
	batch := s.buf
	s.buf = nil

	// An owner name past int64 cannot be an account and cannot be a bind
	// value: count it as having none instead of failing the whole batch.
	ids := make([]uint64, 0, len(batch))
	names := make([]string, 0, len(batch))
	for _, oe := range batch {
		if oe.Owner <= math.MaxInt64 {
			ids = append(ids, oe.Owner)
			names = append(names, strconv.FormatUint(oe.Owner, 10))
		}
	}
	lookup := func() error {
		if s.sctx.Err() != nil {
			return errSweepOutOfTime
		}
		return errSweepLookup
	}
	accounts := map[uint64]avatarRow{}
	waiting := map[string]bool{}
	if len(ids) > 0 {
		var rows []avatarRow
		if err := s.db.Raw(`SELECT id, COALESCE(avatar_key,'') AS avatar_key, deleted_at IS NOT NULL AS deleted
			FROM users WHERE id IN ?`, ids).Scan(&rows).Error; err != nil {
			return lookup()
		}
		for _, r := range rows {
			accounts[uint64(r.ID)] = r
		}
		var pending []struct{ UserID string }
		if err := s.db.Raw(`SELECT DISTINCT payload->>'userId' AS user_id FROM jobs
			WHERE kind = ? AND status IN ('queued','running','failed','dead') AND payload->>'userId' IN ?`,
			kindAvatarCleanup, names).Scan(&pending).Error; err != nil {
			return lookup()
		}
		for _, p := range pending {
			waiting[p.UserID] = true
		}
	}

	for _, oe := range batch {
		row, ok := accounts[oe.Owner]
		switch {
		case !ok && oe.Unsafe:
			s.unsafeNoAccount++
			continue
		case !ok:
			s.noAccount++
			continue
		case waiting[strconv.FormatUint(oe.Owner, 10)]:
			s.waiting++
			continue
		}
		if !avatarOwnerNeedsWork(oe, row) {
			continue
		}
		s.specs = append(s.specs, jobs.Spec{Kind: kindAvatarCleanup, Payload: avatarCleanupJob{UserID: row.ID},
			RunAt: time.Now(), UniqueKey: kindAvatarCleanup + ":" + strconv.FormatUint(oe.Owner, 10) + ":" + s.day,
			MaxAttempts: jobs.DefaultMaxAttempts})
	}
	if len(s.specs) >= s.limit {
		s.specs = s.specs[:s.limit]
		s.capped = true
		return errSweepFull
	}
	return nil
}

// avatarOwnerNeedsWork mirrors the cleanup keep rule, which depends on
// deleted_at alone: a tombstone keeps nothing, any other row keeps its key.
func avatarOwnerNeedsWork(oe storage.OwnerEntries, row avatarRow) bool {
	if oe.Unsafe || oe.Truncated || row.Deleted || row.AvatarKey == "" {
		return true
	}
	for _, k := range oe.Keys {
		if k != row.AvatarKey {
			return true
		}
	}
	return false
}
