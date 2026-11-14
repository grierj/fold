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
// the locked one (planReasonReviewIn).
//
// The legacy filter (deleted accounts' undecided reasons, T07B-3a) cannot be
// decided from the four scan columns: it needs each party's deleted state, the
// purpose and the party's deletion marker. Its incremental rule is the
// batch-qualified scan (reasonscan.ScanParties, scanLegacyUnit): each batch is
// read whole, then two batched lookups run over that batch's reason-bearing
// rows (legacyFactsIn, legacyMarkersIn), never one per row, and the
// request-atomic stop is applied over the batch in id order. Per attempt that
// is the same at most 8 scan statements and reasonUnitRows rows plus at most 16
// lookups; a request with the actor seal's three busy attempts is at most 72
// statements. Skew: the batch statement and its two lookups each have their own
// READ COMMITTED snapshot, so a party deleted or decided, or a request deleted
// or blanked, between them may appear or be missed once in that page. The cursor
// walk and the save's exact-set recheck under locks keep that safe, and no page
// is authoritative.

import (
	"database/sql"
	"fmt"
	"strings"

	"fold/internal/policy"
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

// scanReasonItemsBatched reads one unit: at most reasonUnitRows rows by
// primary key, in batches, filtered in Go per row, stopping right after the
// pageRequests-th qualifying request, so a request is returned whole or not at
// all whatever the batch boundaries (R4-C01). next is that request's id; with
// fewer qualifying requests it is the last id read when the unit read its whole
// budget, else nil. The tests check it against the one-statement oracle
// scanReasonItems (account_erasure_reasons_scan_test.go).
func scanReasonItemsBatched(tx *gorm.DB, filter reasonFilter, after *int64, pageRequests int) (reasonUnit, error) {
	qualifies := func(r reasonscan.Row) bool {
		return filter(reasonSource{ID: r.ID, RequesterID: r.RequesterID, SubjectUserID: r.SubjectUserID, HasReason: r.HasReason})
	}
	u, err := reasonscan.Scan(reasonScanQuery(tx), qualifies, after, reasonScanParams(pageRequests), reasonScanHooks)
	if err != nil {
		return reasonUnit{}, err
	}
	return reasonUnit{Requests: u.Requests, Next: u.Next}, nil
}

// reasonScanQuery is the batch statement of both scans: the same two statement
// texts with the batch's LIMIT.
func reasonScanQuery(tx *gorm.DB) reasonscan.Query {
	return func(cursor *int64, limit int) (reasonscan.Rows, error) {
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
}

func reasonScanParams(pageRequests int) reasonscan.Params {
	return reasonscan.Params{UnitRows: reasonUnitRows, PageRequests: pageRequests, FirstBatch: reasonScanBatchFirst, Growth: reasonScanBatchGrowth}
}

// legacyUnit is what one legacy unit found: the (request, tombstone) pairs that
// are unreviewed, request-atomic (reasonscan.PartyUnit), and the cursor.
type legacyUnit = reasonscan.PartyUnit

// scanLegacyUnit reads one legacy unit: the requests with a reason one of whose
// parties is a deleted account with no decision marker for that (request,
// party). party 0 is discovery (each such party, requester first); a nonzero
// party is one tombstone's own page (that party only, so at most one item per
// request). It returns at most pageRequests requests, whole (R4-C01).
func scanLegacyUnit(tx *gorm.DB, party int64, after *int64, pageRequests int) (legacyUnit, error) {
	return reasonscan.ScanParties(reasonScanQuery(tx), legacyQualifier(tx, party), after, reasonScanParams(pageRequests), reasonScanHooks)
}

// legacyFacts are what the legacy filter reads about one request beyond the
// scan's columns. A party with no users row (none is hard deleted today) is
// not a tombstone: only a soft-deleted account is.
type legacyFacts struct {
	Purpose                          string
	RequesterDeleted, SubjectDeleted bool
}

// legacyFactsSQL reads the facts for a batch's reason-bearing requests by
// primary key. It is raw SQL on purpose: a soft-deleted users row must be seen.
const legacyFactsSQL = `SELECT cr.id, cr.purpose, (req.deleted_at IS NOT NULL) AS requester_deleted, (sub.deleted_at IS NOT NULL) AS subject_deleted
FROM contact_requests cr
LEFT JOIN users req ON req.id = cr.requester_id
LEFT JOIN users sub ON sub.id = cr.subject_user_id
WHERE cr.id = ANY(?::bigint[])`

func legacyFactsIn(tx *gorm.DB, ids []int64) (map[int64]legacyFacts, error) {
	rows, err := tx.Raw(legacyFactsSQL, pgBigintArray(ids)).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]legacyFacts, len(ids))
	for rows.Next() {
		var id int64
		var f legacyFacts
		if err := rows.Scan(&id, &f.Purpose, &f.RequesterDeleted, &f.SubjectDeleted); err != nil {
			return nil, err
		}
		out[id] = f
	}
	return out, rows.Err()
}

// legacyPair names one (request, party) the legacy filter decides.
type legacyPair struct{ Request, Party int64 }

// legacyCandidate is a legacyPair with the audit vocabulary of its request's
// purpose: the marker must be a decision of that purpose's own kind (its target
// type and its kept or removed action), never a global set (requestKindOf owns
// the mapping).
type legacyCandidate struct {
	legacyPair
	kind *requestKind
}

// legacyMarkersSQL is the one marker selector (R3-C08), one statement over a
// batch's candidates. A marker for (request, party) is an audit row targeting
// the request with the candidate's own target type and one of its own two
// actions, whose metadata names the party as erasedUserId and carries
// reviewVersion 1, both as JSON numbers. Only the canonical integer 1 counts:
// a stored 1.0, "1", null or a missing key is not a marker, so a writer that
// changed the encoding would make decided pairs reappear in the list loudly
// instead of unreviewing them silently. Malformed metadata is never cast.
//
// The marker is probed once per candidate: a LATERAL subquery with LIMIT 1 is
// not flattened into a join over one scan of audit_logs. The jsonb predicates
// have no statistics, so the semi-join this replaced was costed as if almost no
// row matched, and a batch of about a thousand candidates could be planned as
// one scan of audit_logs read again for every candidate. The access of each
// probe is still the planner's choice (a table of a few pages is read
// sequentially, an unanalyzed one through a bitmap); on the analyzed table of
// the test it picks idx_audit_logs_target (target_type, target_id) in every
// plan-cache mode (TestLegacyMarkerLookupUsesTargetIndexInEveryPlanMode).
const legacyMarkersSQL = `SELECT c.req, c.party
FROM unnest(?::bigint[], ?::bigint[], ?::text[], ?::text[], ?::text[]) AS c(req, party, target_type, kept, removed)
CROSS JOIN LATERAL (
	SELECT 1 FROM audit_logs a
	WHERE a.target_type = c.target_type AND a.target_id = c.req AND a.action IN (c.kept, c.removed)
	AND jsonb_typeof(a.meta_json->'reviewVersion') = 'number' AND a.meta_json->>'reviewVersion' = '1'
	AND jsonb_typeof(a.meta_json->'erasedUserId') = 'number' AND a.meta_json->>'erasedUserId' = c.party::text
	LIMIT 1) m`

// legacyMarkersIn returns the candidates that already have a decision marker.
// It is the only production form of the selector: discovery, per-account pages
// and the save's recheck all go through it.
func legacyMarkersIn(tx *gorm.DB, cands []legacyCandidate) (map[legacyPair]bool, error) {
	out := make(map[legacyPair]bool, len(cands))
	if len(cands) == 0 {
		return out, nil
	}
	reqs, parties := make([]int64, len(cands)), make([]int64, len(cands))
	targets, kept, removed := make([]string, len(cands)), make([]string, len(cands)), make([]string, len(cands))
	for i, c := range cands {
		reqs[i], parties[i] = c.Request, c.Party
		targets[i], kept[i], removed[i] = c.kind.target, c.kind.reasonKept, c.kind.reasonRemoved
	}
	rows, err := tx.Raw(legacyMarkersSQL, pgBigintArray(reqs), pgBigintArray(parties), pgTextArray(targets), pgTextArray(kept), pgTextArray(removed)).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p legacyPair
		if err := rows.Scan(&p.Request, &p.Party); err != nil {
			return nil, err
		}
		out[p] = true
	}
	return out, rows.Err()
}

// pgTextEscaper escapes an element of a quoted Postgres array literal: only the
// backslash and the double quote are special inside the quotes.
var pgTextEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`)

// pgTextArray formats strings as a Postgres array literal for a ?::text[]
// parameter (see pgBigintArray): every element is quoted and escaped.
func pgTextArray(vals []string) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, v := range vals {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		b.WriteString(pgTextEscaper.Replace(v))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

// legacyQualifier decides each row of a batch: which of its parties are a
// deleted account whose reason nobody decided. It reads, for the batch's
// reason-bearing rows only, the purposes and the parties' deleted states in one
// statement and the markers of the deleted parties in one more, then classifies
// each party with policy.ErasureReasonItemState and keeps the unreviewed
// tombstones. A request with no kind (an unknown purpose) is an error, never
// "qualifies". A row that disappeared between the scan and the facts lookup is
// skipped (the skew rule in the file comment).
func legacyQualifier(tx *gorm.DB, party int64) reasonscan.BatchQualifier {
	return func(rows []reasonscan.Row) ([]reasonscan.Parties, error) {
		out := make([]reasonscan.Parties, len(rows))
		var ids []int64
		for _, r := range rows {
			if r.HasReason && (party == 0 || r.RequesterID == party || r.SubjectUserID == party) {
				ids = append(ids, r.ID)
			}
		}
		if len(ids) == 0 {
			return out, nil
		}
		facts, err := legacyFactsIn(tx, ids)
		if err != nil {
			return nil, err
		}
		type legacyOrdered struct {
			idx           int
			requesterSide bool
			pair          legacyPair
		}
		var cands []legacyCandidate
		var order []legacyOrdered
		for i, r := range rows {
			if !r.HasReason || (party != 0 && r.RequesterID != party && r.SubjectUserID != party) {
				continue
			}
			f, ok := facts[r.ID]
			if !ok {
				continue
			}
			kind := requestKindOf(f.Purpose)
			if kind == nil {
				return nil, fmt.Errorf("legacy review: unknown request purpose %q", f.Purpose)
			}
			for _, side := range [...]struct {
				requester bool
				id        int64
				deleted   bool
			}{{true, r.RequesterID, f.RequesterDeleted}, {false, r.SubjectUserID, f.SubjectDeleted}} {
				if !side.deleted || (party != 0 && side.id != party) {
					continue
				}
				pair := legacyPair{r.ID, side.id}
				cands = append(cands, legacyCandidate{pair, kind})
				order = append(order, legacyOrdered{i, side.requester, pair})
			}
		}
		markers, err := legacyMarkersIn(tx, cands)
		if err != nil {
			return nil, err
		}
		for _, o := range order {
			state := policy.ErasureReasonItemState(policy.ErasureReasonFacts{PartyDeleted: true, Marker: markers[o.pair]})
			if state != policy.ReasonItemUnreviewedTombstone {
				continue
			}
			if o.requesterSide {
				out[o.idx].Requester = true
			} else {
				out[o.idx].Subject = true
			}
		}
		return out, nil
	}
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
