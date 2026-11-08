package main

// The live review pages read their scanner unit in bounded batches (T07B-2,
// design t07b-scanner-read-amplification-design-v2.md, option 1).
//
// scanReasonItems (account_erasure_reasons.go) sends one statement with
// LIMIT reasonUnitRows. pgx reads and discards every row the server sends after
// Go has returned, so a page of 100 requests cost up to a unit of 50,000 rows.
// scanReasonItemsBatched sends the same two statement texts with LIMIT 256, then
// twice as many, and so on, clipped to the unit, and stops after the batch that
// holds the 100th qualifying request (internal/reasonscan). Per attempt that is at
// most reasonUnitRows rows and 8 statements; a request with the actor seal's
// three busy attempts is at most 150,000 rows and 24 statements.
//
// Each batch is its own statement, so under READ COMMITTED it has its own
// snapshot. A request committed or blanked while a page is read can therefore
// change where that page ends, and one committed below a cursor already returned
// is not shown by the walk. The deletion does not depend on the walk: it re-reads
// the account's reasons FOR UPDATE and refuses a reviewed set that differs from
// the locked one (planReasonReviewIn). Live pages only: legacy discovery and
// per-tombstone pages qualify a unit with a second, batched marker lookup and
// stay on scanReasonItems until T07B-3a defines an incremental rule.

import (
	"database/sql"

	"fold/internal/reasonscan"

	"gorm.io/gorm"
)

// The first batch and the growth are pinned (design section 4). Tests shorten
// them like reasonUnitRows; changing either in production changes the statement
// bounds above and needs the design and its bound tests updated.
var (
	reasonScanBatchFirst  = 256
	reasonScanBatchGrowth = 2
	// reasonScanHooks are test seams, zero in production: Before runs before a
	// batch statement is sent, After when it has finished.
	reasonScanHooks reasonscan.Hooks
)

type reasonScanMode int

const (
	// reasonScanSingle is the original one-statement unit.
	reasonScanSingle reasonScanMode = iota
	// reasonScanBatched is the bounded-batch unit of the live pages.
	reasonScanBatched
)

// scanReasonUnit reads one unit in the given mode. The live page route passes
// reasonScanBatched; nothing else is batched.
func scanReasonUnit(tx *gorm.DB, filter reasonFilter, after *int64, pageRequests int, mode reasonScanMode) (reasonUnit, error) {
	if mode == reasonScanBatched {
		return scanReasonItemsBatched(tx, filter, after, pageRequests)
	}
	return scanReasonItems(tx, filter, after, pageRequests)
}

// scanReasonItemsBatched is scanReasonItems with the unit read in batches. The
// filter runs per row exactly as there, so a request is returned whole or not
// at all whatever the batch boundaries.
func scanReasonItemsBatched(tx *gorm.DB, filter reasonFilter, after *int64, pageRequests int) (reasonUnit, error) {
	query := func(cursor *int64, limit int) (reasonscan.Rows, error) {
		var rows reasonRowsAdapter
		var err error
		if cursor == nil {
			rows.Rows, err = tx.Raw(reasonScanInitialSQL, limit).Rows()
		} else {
			rows.Rows, err = tx.Raw(reasonScanContinuationSQL, *cursor, limit).Rows()
		}
		if err != nil {
			return nil, err
		}
		return &rows, nil
	}
	qualifies := func(r reasonscan.Row) bool {
		return len(filter(reasonSource{ID: r.ID, RequesterID: r.RequesterID, SubjectUserID: r.SubjectUserID, HasReason: r.HasReason})) > 0
	}
	u, err := reasonscan.Scan(query, qualifies, after, reasonscan.Params{
		UnitRows: reasonUnitRows, PageRequests: pageRequests, FirstBatch: reasonScanBatchFirst, Growth: reasonScanBatchGrowth,
	}, reasonScanHooks)
	if err != nil {
		return reasonUnit{}, err
	}
	return reasonUnit{Requests: u.Requests, Next: u.Next}, nil
}

// reasonRowsAdapter reads the scanner's four columns from a database/sql result.
// Close returns the driver's error: with pgx that includes a stream that failed
// while the rest of the result was being read, which fails the page.
type reasonRowsAdapter struct{ *sql.Rows }

func (a *reasonRowsAdapter) Scan() (reasonscan.Row, error) {
	var r reasonscan.Row
	err := a.Rows.Scan(&r.ID, &r.RequesterID, &r.SubjectUserID, &r.HasReason)
	return r, err
}
