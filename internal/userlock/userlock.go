// Package userlock holds the one statement that protects a user from being
// erased while someone writes a row about them: FOR KEY SHARE on the user's
// row. A foreign key to users takes the same lock, so an erasure (which
// updates the row) waits for the writer, or finds nothing left to resurrect.
package userlock

import (
	"slices"

	"gorm.io/gorm"
)

// Batch is how many ids one query locks.
const Batch = 500

// KeyShareLive takes FOR KEY SHARE on the users among ids that are not
// deleted, in ascending id order, Batch to a query, and returns the ids it
// locked. Zero ids are ignored. Run on a plain handle, the locks end with the
// statement; run in a transaction, they last until it ends.
func KeyShareLive(tx *gorm.DB, ids []uint) (map[uint]bool, error) {
	sorted := make([]uint, 0, len(ids))
	for _, id := range ids {
		if id != 0 {
			sorted = append(sorted, id)
		}
	}
	slices.Sort(sorted)
	sorted = slices.Compact(sorted)
	held := make(map[uint]bool, len(sorted))
	for len(sorted) > 0 {
		n := min(len(sorted), Batch)
		var got []uint
		if err := tx.Raw(`SELECT id FROM users WHERE id IN ? AND deleted_at IS NULL ORDER BY id FOR KEY SHARE`,
			sorted[:n]).Scan(&got).Error; err != nil {
			return nil, err
		}
		for _, id := range got {
			held[id] = true
		}
		sorted = sorted[n:]
	}
	return held, nil
}
