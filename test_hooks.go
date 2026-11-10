package main

import (
	"sync/atomic"
	"time"

	"fold/internal/push"
	"fold/models"

	"gorm.io/gorm"
)

// testHooks are the App's test seams, all nil in production (T07B-1b): a nil
// seam sends no statement and takes no time. They are one struct so production
// code names them the same way (a.hooks.x) and nothing else on App is a test
// control. The time seam is App.clock.
type testHooks struct {
	// restoreAckPause runs inside acknowledgeRestoreTicket before it raises the
	// floor. settleBeforeTx runs before settle's first transaction;
	// settleBeforeFence and settleAfterFence run inside its seal, around the
	// ledger transition, given the open transaction. deleteBeforeSeal runs in
	// the deletion's seal just before currentAdminIn, given the open
	// transaction; deleteBeforeRetry runs between a busy attempt's rollback and
	// the next attempt, with no transaction open. conversionAfterUserLocks runs
	// in acceptHouseholdConversion right after its users statement.
	// readBeforeSeal runs in a reason-review page transaction between its view
	// audit and the actor seal, given the open transaction (T07B-2b).
	// sealAttempts counts seal entries of the deletion and settle.
	restoreAckPause          func()
	settleBeforeTx           func()
	settleBeforeFence        func(*gorm.DB)
	settleAfterFence         func(*gorm.DB)
	deleteBeforeSeal         func(*gorm.DB)
	deleteBeforeRetry        func()
	readBeforeSeal           func(*gorm.DB)
	conversionAfterUserLocks func(*gorm.DB)
	sealAttempts             *atomic.Int64
	// messageLimitAfterCount runs inside the schedule, scheduled-edit and
	// Try again transactions, after a daily-limit count that did not refuse
	// and before the message write or claim, given the open transaction
	// (#260).
	messageLimitAfterCount func(*gorm.DB)

	// pushRender overrides renderPush in tests (a rendering past the payload
	// cap); nil uses the stored push text. pushWindow fixes the quiet hours
	// (DEC-126); nil is push.DefaultWindow.
	pushRender func(*models.Notification) (push.Notification, bool)
	pushWindow *push.Window
}

// now is the current time. Production reads the wall clock; tests and the e2e
// build inject App.clock, so every time-dependent decision (the calendar
// window, series undo, push quiet hours, an attempt id's age) moves together.
func (a *App) now() time.Time {
	if a.clock != nil {
		return a.clock()
	}
	return time.Now()
}
