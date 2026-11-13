// Package reasonscan reads one scanner unit of the contact-request reason
// review (T07B-2) in bounded batches.
//
// A unit is at most UnitRows source rows by primary key. The single-statement
// scan asks the server for all of them (LIMIT UnitRows) and, once the page is
// full, pgx still reads and discards the rest of the result, so a page of 100
// requests cost a unit's worth of rows. Scan asks for FirstBatch rows, then
// Growth times as many, and so on, clipped to the unit, and stops after the
// batch that holds the PageRequests-th qualifying request. The statement texts,
// the id order, the cursor, the request-atomic stop and the 50,000-row bound
// are the single statement's; see design t07b-scanner-read-amplification-design-v2.md.
//
// The package knows nothing about SQL or GORM: the caller supplies Query, which
// runs the initial statement (after == nil) or the continuation (after = the last
// id read) with the given LIMIT. Each batch is its own statement, so under READ
// COMMITTED it has its own snapshot; the design's section 3 lists what that
// changes.
package reasonscan

import (
	"errors"
	"fmt"
)

// Row is the part of a request the scanner decides from.
type Row struct {
	ID, RequesterID, SubjectUserID int64
	HasReason                      bool
}

// Rows is the result of one batch statement.
type Rows interface {
	// Next advances to the next row; false at the end or on an error.
	Next() bool
	// Scan returns the current row.
	Scan() (Row, error)
	// Err is the error that ended iteration, if any.
	Err() error
	// Close releases the result. With pgx it reads and discards every row the
	// server still sends, so its error (a failed stream) must not be ignored.
	Close() error
}

// Query runs one batch: the initial statement when after is nil, else the
// continuation with id > *after, in id order, at most limit rows.
type Query func(after *int64, limit int) (Rows, error)

// Params are the bounds of a scan. UnitRows and PageRequests are the approved
// 50,000 and 100 in production (the caller owns them); FirstBatch and Growth
// are the pinned 256 and 2.
type Params struct {
	UnitRows     int
	PageRequests int
	FirstBatch   int
	Growth       int
}

// Unit is what one scan found: the qualifying requests in id order and the
// cursor to continue from (nil when the table is exhausted).
type Unit struct {
	Requests []int64
	Next     *int64
}

// BatchEvent describes one finished batch, for tests and diagnostics. It says
// what the loop did, not what the server sent: rows the server sent beyond
// Consumed are drained by Close and are only known from the statement's
// command tag, which a failed stream never delivers.
type BatchEvent struct {
	Index    int    // 0-based within the scan
	After    *int64 // nil for the initial statement
	Limit    int
	Consumed int   // rows taken with Next
	Stopped  bool  // the PageRequests-th qualifying request was in this batch
	Short    bool  // fewer rows than Limit: the table ended
	Err      error // query, scan, iteration or close error
}

// Hooks are test seams; the zero value does nothing.
type Hooks struct {
	// Before runs before batch index is sent.
	Before func(index int)
	// After runs when a batch has finished (success or failure).
	After func(BatchEvent)
}

// Scan reads one unit. See the package comment.
func Scan(q Query, qualifies func(Row) bool, after *int64, p Params, h Hooks) (Unit, error) {
	if p.UnitRows < 1 || p.PageRequests < 1 || p.FirstBatch < 1 || p.Growth < 1 {
		return Unit{}, fmt.Errorf("reasonscan: invalid bounds %+v", p)
	}
	var unit Unit
	budget, batch, cur := p.UnitRows, p.FirstBatch, after
	var last int64
	for index := 0; budget > 0; index++ {
		n := batch
		if n > budget {
			n = budget
		}
		if h.Before != nil {
			h.Before(index)
		}
		ev := BatchEvent{Index: index, After: cur, Limit: n}
		rows, err := q(cur, n)
		if err != nil {
			ev.Err = err
			if h.After != nil {
				h.After(ev)
			}
			return Unit{}, err
		}
		var scanErr error
		for rows.Next() {
			row, err := rows.Scan()
			if err != nil {
				scanErr = err
				break
			}
			ev.Consumed++
			last = row.ID
			if !qualifies(row) {
				continue
			}
			unit.Requests = append(unit.Requests, row.ID)
			if len(unit.Requests) == p.PageRequests {
				next := row.ID
				unit.Next = &next
				ev.Stopped = true
				break
			}
		}
		iterErr := scanErr
		if iterErr == nil && !ev.Stopped {
			iterErr = rows.Err()
		}
		// Close drains what the server still sends. Its error is a failed
		// stream and fails the page; it is never dropped.
		closeErr := rows.Close()
		ev.Err = errors.Join(iterErr, closeErr, rows.Err())
		ev.Short = !ev.Stopped && ev.Err == nil && ev.Consumed < n
		if h.After != nil {
			h.After(ev)
		}
		if ev.Err != nil {
			return Unit{}, ev.Err
		}
		if ev.Stopped || ev.Short {
			return unit, nil
		}
		budget -= n
		lc := last
		cur = &lc
		batch *= p.Growth
	}
	// The whole unit budget was read without a full page: the cursor is the
	// last id read, as in the single statement.
	lc := last
	unit.Next = &lc
	return unit, nil
}

// Batches returns the LIMIT of each statement a scan that reads the whole unit
// sends: FirstBatch, then Growth times as many, the last clipped so the sum is
// UnitRows. A scan that stops earlier sends a prefix of this list.
func Batches(p Params) []int {
	if p.UnitRows < 1 || p.FirstBatch < 1 || p.Growth < 1 {
		return nil
	}
	var out []int
	budget, batch := p.UnitRows, p.FirstBatch
	for budget > 0 {
		n := batch
		if n > budget {
			n = budget
		}
		out = append(out, n)
		budget -= n
		batch *= p.Growth
	}
	return out
}

// Parties says which parties of one request qualify. A request's parties are
// emitted together or not at all (R4-C01).
type Parties struct{ Requester, Subject bool }

// Any reports whether the request qualifies at all.
func (p Parties) Any() bool { return p.Requester || p.Subject }

// BatchQualifier decides one fully read batch: it returns one Parties per row,
// in row order, and may run lookups for the batch's rows. It decides each
// request from its own parties and is never asked about a partial batch.
type BatchQualifier func(rows []Row) ([]Parties, error)

// Item is one qualifying (request, party) pair.
type Item struct{ Request, Party int64 }

// PartyUnit is what ScanParties found: the qualifying pairs in request id
// order, the requester before the subject within a request, how many requests
// they come from (at most PageRequests) and the cursor to continue from.
type PartyUnit struct {
	Items    []Item
	Requests int
	Next     *int64
}

// ScanParties reads one unit like Scan, for a rule that needs a lookup over a
// whole batch before it can say which parties of a request qualify (the legacy
// filter, T07B-3a: each deleted party's deletion marker). Each batch is read
// whole (Close drains whatever is not consumed in any case), handed to qb, and
// then walked in id order counting requests, never items: a request's parties
// are emitted together and the walk stops right after the PageRequests-th
// qualifying request, so a request is never split by a page or a batch boundary
// and a page holds at most PageRequests requests (twice as many items when both
// parties qualify). The statement sequence, the cursor rule and the UnitRows
// bound are Scan's: the cursor is that request's id, else the last id read when
// the unit budget is spent, else nil when the table ended. The only difference
// is that rows after the stopping request in the same batch were already read
// and qualified, but their results are dropped: the next page starts after
// the stopping request's id, so it reads those rows again and reconsiders
// them.
//
// BatchEvent.Consumed is every row of the batch and Stopped is decided after qb
// ran, so After fires once the batch's lookups are done.
func ScanParties(q Query, qb BatchQualifier, after *int64, p Params, h Hooks) (PartyUnit, error) {
	if p.UnitRows < 1 || p.PageRequests < 1 || p.FirstBatch < 1 || p.Growth < 1 {
		return PartyUnit{}, fmt.Errorf("reasonscan: invalid bounds %+v", p)
	}
	var unit PartyUnit
	budget, batch, cur := p.UnitRows, p.FirstBatch, after
	var last int64
	for index := 0; budget > 0; index++ {
		n := batch
		if n > budget {
			n = budget
		}
		if h.Before != nil {
			h.Before(index)
		}
		ev := BatchEvent{Index: index, After: cur, Limit: n}
		rows, err := q(cur, n)
		if err != nil {
			ev.Err = err
			if h.After != nil {
				h.After(ev)
			}
			return PartyUnit{}, err
		}
		read := make([]Row, 0, min(n, 1024))
		var scanErr error
		for rows.Next() {
			row, err := rows.Scan()
			if err != nil {
				scanErr = err
				break
			}
			ev.Consumed++
			read = append(read, row)
		}
		iterErr := scanErr
		if iterErr == nil {
			iterErr = rows.Err()
		}
		closeErr := rows.Close()
		ev.Err = errors.Join(iterErr, closeErr, rows.Err())
		ev.Short = ev.Err == nil && ev.Consumed < n
		var parties []Parties
		if ev.Err == nil && len(read) > 0 {
			parties, ev.Err = qb(read)
			if ev.Err == nil && len(parties) != len(read) {
				ev.Err = fmt.Errorf("reasonscan: qualifier answered %d rows for %d", len(parties), len(read))
			}
		}
		if ev.Err == nil {
			for i, row := range read {
				last = row.ID
				if !parties[i].Any() {
					continue
				}
				if parties[i].Requester {
					unit.Items = append(unit.Items, Item{Request: row.ID, Party: row.RequesterID})
				}
				if parties[i].Subject {
					unit.Items = append(unit.Items, Item{Request: row.ID, Party: row.SubjectUserID})
				}
				unit.Requests++
				if unit.Requests == p.PageRequests {
					next := row.ID
					unit.Next = &next
					ev.Stopped = true
					break
				}
			}
			ev.Short = ev.Short && !ev.Stopped
		}
		if h.After != nil {
			h.After(ev)
		}
		if ev.Err != nil {
			return PartyUnit{}, ev.Err
		}
		if ev.Stopped || ev.Short {
			return unit, nil
		}
		budget -= n
		lc := last
		cur = &lc
		batch *= p.Growth
	}
	lc := last
	unit.Next = &lc
	return unit, nil
}
