package main

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// Household locks of an account erasure (M7.4 R4-3b-1; inventory 9.6
// requirement 5, signed in 15.5: option (a)).
//
// The cycle this closes: the erasure holds an admin's user row FOR UPDATE and
// wants a household row, while a household writer holds that household and
// asks the same admin's row FOR KEY SHARE (notify.Send's recipient lock, the
// N-11 notices, promotion and removal notices written under the household).
// The erasure therefore never waits for a household row while it holds the
// user row: it takes every household with NOWAIT, and on SQLSTATE 55P03 the
// whole transaction rolls back (releasing the gate and the user lock) and the
// whole erasure runs again, a bounded number of times.
//
// Lock census (rechecked by TestErasureHouseholdLockCensus, which reads the
// production source):
//
//   - The deletion callback in apiAdminDeleteUser takes, in order: the contact
//     gate (contactMutationCtx); the hosted events FOR KEY SHARE and the
//     account FOR UPDATE (deletionHostPlan); the actor row, a plain read; the
//     households of the account's seat and of PERS, NOWAIT, ascending
//     (lockErasureHouseholdsIn); then, in the order the callback runs them,
//     the groups of handOffLeadership, the events of handOffHosting, the
//     request, notification and occurrence rows of eraseAccountRequestsIn,
//     promoteOnDisable and eraseDependentRecordsIn. None of those helpers
//     reaches lockHousehold or a row of households: promoteLocked and
//     eligibleRegistration read households and seats with plain SELECTs,
//     which take no row lock.
//   - Every household writer holds a households row while it writes notices or
//     takes another user's lock: apiAddHouseholdPerson and
//     apiUpdateHouseholdPerson (N-11 admin recipients, createGuardianLink),
//     apiDeleteHouseholdPerson through reconcileHouseholdPerson
//     (notifyRegistrationRemoved, promoteLocked), flagHouseholdChild13
//     (age_rollover.go, N-05), the admin and conversion paths, and
//     addHouseholdRegistration. Each is why the erasure's household lock is
//     NOWAIT: it can be the second party of the cycle in either order.
//
// Only 55P03 retries. A cycle through an occurrence lock (D4-class) is not
// closed by NOWAIT: the database breaks it with one 40P01 and the whole
// erasure rolls back and answers as any other failure; nothing retries it.
const (
	// householdAttempts is the most times one deletion request runs the whole
	// erasure transaction.
	householdAttempts = 3
)

var (
	// deleteRetryWindow is how long after the first attempt began a new
	// attempt may still start. Inside an attempt the bound is the request
	// context. A var so tests can shorten it; a test that sets it stays serial.
	deleteRetryWindow = 2 * time.Second
	// householdRetryBackoff is the wait before attempt 2 and attempt 3.
	householdRetryBackoff = [householdAttempts - 1]time.Duration{25 * time.Millisecond, 100 * time.Millisecond}
)

// errErasureHouseholdGone: a household the erasure selected has no row to lock.
// Foreign keys make that impossible; the erasure stops rather than guess.
var errErasureHouseholdGone = errors.New("an erasure household has no row to lock")

// isHouseholdBusy is true for SQLSTATE 55P03 (lock_not_available), the NOWAIT
// refusal. It reads through *contactRollbackError to the cause; a commit error
// does not unwrap, so a COMMIT result is never taken for it.
func isHouseholdBusy(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "55P03"
}

// lockErasureHouseholdsIn locks, FOR UPDATE NOWAIT in ascending id, the
// household of the erased account's seat and the households of PERS (read
// unscoped, soft-deleted people included). It runs after the user row lock and
// captureErasureSetsIn and before the erasure's first write, and no other
// erasure statement takes a households row. A 55P03 comes back as it is (the
// transaction is aborted; retryHouseholdBusy decides). It returns the ids it
// locked.
func lockErasureHouseholdsIn(tx *gorm.DB, s erasureSubject) ([]uint, error) {
	var ids []uint
	if len(s.People) > 0 {
		if err := tx.Raw(`SELECT household_id FROM household_accounts WHERE user_id = ?
			UNION
			SELECT household_id FROM household_people WHERE id IN ?
			ORDER BY household_id`, s.ID, s.People).Scan(&ids).Error; err != nil {
			return nil, err
		}
	} else if err := tx.Raw(`SELECT household_id FROM household_accounts WHERE user_id = ?
		ORDER BY household_id`, s.ID).Scan(&ids).Error; err != nil {
		return nil, err
	}
	for _, id := range ids {
		var got []uint
		if err := tx.Raw("SELECT id FROM households WHERE id = ? FOR UPDATE NOWAIT", id).Scan(&got).Error; err != nil {
			return nil, err
		}
		if len(got) != 1 {
			return nil, errErasureHouseholdGone
		}
	}
	return ids, nil
}

// householdRetry is the bounded retry of the whole erasure on a busy household
// (the plan's retryHouseholdBusy, in the shape guard R-6 allows: the guard
// requires the a.contactMutationCtx call to sit in apiAdminDeleteUser itself,
// never in a func literal, so the caller owns the loop and asks after each
// attempt).
//
//	retry := newHouseholdRetry(ctx)
//	for {
//		err = a.contactMutationCtx(ctx, change, fn)
//		var again bool
//		if again, err = retry.after(err); !again { break }
//	}
//
// At most householdAttempts attempts share ctx. Only a 55P03 whose rollback was
// acknowledged (rollbackConfirmed) runs again: that acknowledgment proves the
// server released the gate and the user locks, so the next BEGIN cannot wait
// on this attempt. Everything else comes back at once, and a COMMIT whose
// result is unknown (checked first) is never run twice. A 55P03 inside a
// *contactRollbackError also comes back at once, with no further BEGIN.
//
// A new attempt starts only within deleteRetryWindow of the first attempt's
// start and while ctx is live, after a backoff that ends early with ctx. When
// the window or the attempts run out the last 55P03 comes back; when ctx ends
// first the error carries the context's, so classifyContactErr answers timed
// out. The caller makes every attempt read its facts again.
type householdRetry struct {
	ctx     context.Context
	started time.Time
	n       int // attempts finished
}

func newHouseholdRetry(ctx context.Context) *householdRetry {
	return &householdRetry{ctx: ctx, started: time.Now()}
}

// after takes the error of the attempt that just finished. It returns true (and a
// nil error) when, after the backoff, the caller should run the next attempt,
// or false and the error to answer with.
func (r *householdRetry) after(err error) (again bool, final error) {
	if err == nil || classifyContactErr(err) == outcomeUnknown || !isHouseholdBusy(err) || !rollbackConfirmed(err) {
		return false, err
	}
	r.n++
	if r.n >= householdAttempts {
		return false, err
	}
	timer := time.NewTimer(householdRetryBackoff[r.n-1])
	select {
	case <-r.ctx.Done():
		timer.Stop()
		return false, errors.Join(r.ctx.Err(), err)
	case <-timer.C:
	}
	if time.Since(r.started) >= deleteRetryWindow {
		return false, err
	}
	if r.ctx.Err() != nil {
		return false, errors.Join(r.ctx.Err(), err)
	}
	return true, nil
}
