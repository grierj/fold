package main

import (
	"testing"

	"fold/internal/eventdates"
	"fold/migrations"
)

func TestAdvisoryLockIDsAreUnique(t *testing.T) {
	ids := map[string]int64{
		"setupLockID":             setupLockID,
		"contactLockID":           contactLockID,
		"invitationEmailLockSeed": invitationEmailLockSeed,
		"inviteSendLockClass":     int64(inviteSendLockClass),
		"migrations.LockID":       migrations.LockID,
		"eventdates.LockID":       eventdates.LockID,
	}
	seen := map[int64]string{}
	for name, id := range ids {
		if other, dup := seen[id]; dup {
			t.Fatalf("%s and %s share advisory lock id %d", name, other, id)
		}
		seen[id] = name
	}
}
