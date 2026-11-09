package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"

	"fold/migrations"

	"gorm.io/gorm"
)

// The restore barrier (M7.4 T07B-1b, T07B-D4-CONTRACT (a) and (b)).
//
// A database restored while Fold keeps running would hand back receipts and
// ledger facts the running process has already acted on. The barrier makes the
// process notice. One settings row, restore_fence, is a counter. Every fenced
// transaction (the admin account deletion and the settle route) adds one to it
// as its first statement (the ticket). The process keeps the largest value of
// a fenced COMMIT it saw acknowledged in memory (the floor, never written
// anywhere). A ticket that reads a value at or below the floor means the
// database has lost an acknowledged commit: it was restored (or tampered with).
//
// The pieces:
//
//   - bootRestoreFence runs once at start, after migrations and before the
//     router: its acknowledged COMMIT is the first floor.
//   - fencedContactMutation is the only way to run a fenced transaction. It
//     takes the ticket as the first statement of the callback, runs the
//     caller's work, hands the caller's actor seal to contactMutationCtx, and
//     right after the call acknowledges or abandons the ticket:
//   - takeRestoreTicket takes the fence turn (one holder at a time, selected
//     against the transaction's context), then increments the counter.
//   - acknowledgeRestoreTicket raises the floor when the call returned nil,
//     then releases the turn. abandonRestoreTicket (deferred) releases a
//     still-held turn and raises nothing.
//
// Lock order is exclusive contact gate -> turn -> settings row. The turn keeps
// each COMMIT-to-acknowledgment interval ahead of later ticket reads, so a
// restore that lands in that interval is not mistaken for a normal commit, and
// nothing is detected falsely: rollbacks leave the counter unchanged and an
// unknown COMMIT only raises later tickets. What is not detected (stated): a
// restore of a dump taken after the last acknowledged fenced commit, and one
// that loses only an unknown commit.
//
// Only bootRestoreFence and takeRestoreTicket write the row
// (TestRestoreFenceHasTwoWriters parses the source for writers).

// recoveryPolicyLevel is the recovery level this release reports (R4-RP):
// 0 = no restore barrier, 1 = the restore barrier (T07B-1b), 2 = reason review
// (T07B-2b). deploy/recovery-policy holds the same integer and
// TestRecoveryPolicyFileMatchesConstant keeps them equal. R4-RP's tooling
// refuses a scripted rollback, forward upgrade or deploy to a lower level, so a
// release at level 2 cannot be rolled back to one without the review.
// Never publish a release at level 1 or above before R4-6c-R's current-release
// restore tooling and operator procedure land and are verified.
const recoveryPolicyLevel = 2

// restoreFenceKey is the settings row that holds the counter.
const restoreFenceKey = "restore_fence"

// errRestoreDetected: the counter is at or below the floor, or its row is gone,
// so the database was restored under the running process. The transaction
// rolled back; nothing it was going to do happened.
var errRestoreDetected = apiErr(http.StatusServiceUnavailable, "restore_detected", "the database was restored while Fold was running")

// errRestoreFenceKey is returned to a settings writer that would write the
// barrier's own row with a key it did not name in a literal.
var errRestoreFenceKey = errors.New("the restore barrier's settings key cannot be written through a general settings writer")

// refuseRestoreFenceKey is the one check every settings writer with a key it
// does not spell as a constant makes (the census in restore_fence_test.go).
func refuseRestoreFenceKey(key string) error {
	if key == restoreFenceKey {
		return errRestoreFenceKey
	}
	return nil
}

// fenceState is the process's side of the barrier. The zero value is ready.
type fenceState struct {
	floor    atomic.Int64
	detected atomic.Bool
	waiters  atomic.Int64

	turnOnce sync.Once
	turn     chan struct{} // one slot: a send takes the turn, a receive releases it

	restartOnce sync.Once
}

func (f *fenceState) turnChan() chan struct{} {
	f.turnOnce.Do(func() { f.turn = make(chan struct{}, 1) })
	return f.turn
}

// raise moves the floor up to v, never down.
func (f *fenceState) raise(v int64) {
	for {
		cur := f.floor.Load()
		if v <= cur || f.floor.CompareAndSwap(cur, v) {
			return
		}
	}
}

// restoreDetected reports whether this process has seen a restore.
func (a *App) restoreDetected() bool { return a.fence.detected.Load() }

// noteRestoreDetected marks the restore and asks the process to restart once.
func (a *App) noteRestoreDetected() {
	a.fence.detected.Store(true)
	a.fence.restartOnce.Do(func() {
		if a.requestRestart != nil {
			a.requestRestart()
		}
	})
}

// bootRestoreFence is the restart boundary: one transaction under the
// migrations advisory lock creates the counter if absent and advances it, and
// only after an acknowledged COMMIT does the value become the floor. Any
// failure (an unacknowledged COMMIT and an unparsable value included) is
// returned and serve exits non-zero. A new process has no floor to protect, so
// the repair for an unparsable row is to delete it with the site closed.
func (a *App) bootRestoreFence() error {
	var v int64
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", migrations.LockID).Error; err != nil {
			return err
		}
		if err := tx.Exec("INSERT INTO settings (key, value) VALUES ('restore_fence', '0') ON CONFLICT (key) DO NOTHING").Error; err != nil {
			return err
		}
		res := tx.Raw("UPDATE settings SET value = (value::bigint + 1)::text WHERE key = 'restore_fence' RETURNING value::bigint").Scan(&v)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errors.New("the settings row is missing")
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("restore barrier (settings row %q): %w", restoreFenceKey, err)
	}
	a.fence.raise(v)
	return nil
}

// restoreTicket is one fenced attempt's place in the barrier. Its zero value
// holds nothing and every method on it is a no-op.
type restoreTicket struct {
	a     *App
	value int64
	held  bool
}

// takeRestoreTicket takes the fence turn and the counter inside tx, the
// transaction a.contactMutationCtx passed to its callback. A context that ends
// while waiting for the turn comes back as its error, with no ticket and the
// counter untouched. A counter at or below the floor (or a missing row) is
// errRestoreDetected, and the process is marked and asked to restart.
func (a *App) takeRestoreTicket(tx *gorm.DB) (restoreTicket, error) {
	if a.restoreDetected() {
		return restoreTicket{}, errRestoreDetected
	}
	ctx := tx.Statement.Context
	if ctx == nil {
		ctx = context.Background()
	}
	turn := a.fence.turnChan()
	if err := ctx.Err(); err != nil {
		return restoreTicket{}, err
	}
	select {
	case turn <- struct{}{}:
	default:
		a.fence.waiters.Add(1)
		select {
		case turn <- struct{}{}:
			a.fence.waiters.Add(-1)
		case <-ctx.Done():
			a.fence.waiters.Add(-1)
			return restoreTicket{}, ctx.Err()
		}
	}
	tk := restoreTicket{a: a, held: true}
	var v int64
	res := tx.Raw("UPDATE settings SET value = (value::bigint + 1)::text WHERE key = 'restore_fence' RETURNING value::bigint").Scan(&v)
	if res.Error != nil {
		tk.release()
		return restoreTicket{}, res.Error
	}
	if res.RowsAffected != 1 || v <= a.fence.floor.Load() {
		tk.release()
		a.noteRestoreDetected()
		return restoreTicket{}, errRestoreDetected
	}
	tk.value = v
	return tk, nil
}

func (tk *restoreTicket) release() {
	if tk == nil || !tk.held {
		return
	}
	tk.held = false
	<-tk.a.fence.turnChan()
}

// acknowledgeRestoreTicket is the statement right after the a.contactMutationCtx
// call in fencedContactMutation. Only a nil result with a taken ticket raises the floor to the ticket's
// value (the COMMIT was acknowledged); then the turn is released. Every error,
// an unknown COMMIT included, raises nothing. Idempotent.
func (a *App) acknowledgeRestoreTicket(tk *restoreTicket, err error) {
	if tk == nil || !tk.held {
		return
	}
	if err == nil {
		if a.hooks.restoreAckPause != nil {
			a.hooks.restoreAckPause()
		}
		a.fence.raise(tk.value)
	}
	tk.release()
}

// abandonRestoreTicket is deferred by fencedContactMutation before the call. It releases a turn still
// held (a panic, or a path that never acknowledged) and raises nothing.
// Idempotent.
func (a *App) abandonRestoreTicket(tk *restoreTicket) {
	if tk == nil || !tk.held {
		return
	}
	tk.release()
}

// errFenceUnsealed: a fenced callback returned no actor seal. Every fenced
// transaction decides its actor last (R5-C01), so this is a programming error
// and the transaction rolls back.
var errFenceUnsealed = errors.New("a fenced transaction returned no actor seal")

// fencedContactMutation is the only way to run a fenced transaction (the
// admin account deletion and the settle route). It is a.contactMutationCtx
// with the restore ticket around it:
//
//   - the ticket is the callback's first statement, on the callback's own
//     transaction, before fn runs;
//   - fn returns the actor seal; contactMutationOn runs it after
//     reconcileContacts as the last locking statement before COMMIT;
//   - right after the call the ticket is acknowledged with the call's own
//     result, so the floor is raised only on an acknowledged COMMIT, and only
//     then is the turn released;
//   - on every other path (a panic included) the deferred abandon releases a
//     still-held turn and raises nothing.
//
// A caller that retries calls it again: each call takes a fresh ticket.
func (a *App) fencedContactMutation(ctx context.Context, change contactChange, fn func(tx *gorm.DB) (*contactSeal, error)) error {
	var tk restoreTicket
	defer a.abandonRestoreTicket(&tk)
	var seal *contactSeal
	change.Seal = &contactSeal{run: func(tx *gorm.DB) error {
		if seal == nil {
			return errFenceUnsealed
		}
		return seal.run(tx)
	}}
	err := a.contactMutationCtx(ctx, change, func(tx *gorm.DB) error {
		var err error
		if tk, err = a.takeRestoreTicket(tx); err != nil {
			return err
		}
		seal, err = fn(tx)
		return err
	})
	a.acknowledgeRestoreTicket(&tk, err)
	return err
}
