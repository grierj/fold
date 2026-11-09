package testdb

import (
	"testing"
	"time"

	"gorm.io/gorm"
)

// WaitUntil polls cond every few milliseconds until it reports true and fails
// the test with what after within. Use it for an observable event (a row, a
// lock wait, a backend ending), never as a stand-in for a sleep. cond may call
// t.Fatal for an error it cannot recover from.
func WaitUntil(t testing.TB, what string, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.NewTimer(within)
	defer deadline.Stop()
	tick := time.NewTicker(2 * time.Millisecond)
	defer tick.Stop()
	for !cond() {
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatalf("timed out after %s waiting for %s", within, what)
		}
	}
}

// WaitBlocked waits until backend waiter is waiting on a lock that backend
// blocker holds. It names both sessions, so unrelated waiters never satisfy it,
// and it needs no sleep to let a goroutine start.
func WaitBlocked(t testing.TB, db *gorm.DB, waiter, blocker int) {
	t.Helper()
	WaitUntil(t, "a session to wait on another", 10*time.Second, func() bool {
		var blocked bool
		if err := db.Raw("SELECT ? = ANY(pg_blocking_pids(?))", blocker, waiter).Scan(&blocked).Error; err != nil {
			t.Fatal(err)
		}
		return blocked
	})
}
