package main

// Reason review during an account deletion (M7.4 T07B-2b, owner decision 6).
//
// The review is stateless: nothing about it is stored between the pages an
// admin reads and the DELETE that decides them. Each page item carries a
// keyed reviewHash of the request row as that admin was served it. The DELETE
// carries one set-v1 envelope (account_erasure_reasons_envelope.go): the count,
// a SHA-256 over the reviewed set in ascending signed id order (each id with its
// raw review hash) and a two-bit choice per item. Under the exclusive gate the
// server streams the current set in the same order, recomputes every hash and
// the digest, and compares. A set that is missing, extra or changed in any
// hashed column is 409 reason_review_changed, so a choice can never land on a
// row the admin did not see. The old {items:[...]} envelope is never applied
// (409 reason_review_outdated when the account has reasons).
//
// Every statement that lists reasons orders by contact_requests.id, the bigint
// column: reasonProjection casts id to text under the same name, and a bare
// ORDER BY id would sort that text (TestReasonSQLOrderGuard).
//
// Request ids are bigint and never assumed positive: they are int64 from the
// JSON string (parseRequestID) through the scans, the remove batches and the
// signed audit rows (R4-C04).

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"fold/internal/audit"
	"fold/internal/jobs"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Bounds. They are vars so tests can shorten them; a test that sets one stays
// serial.
var (
	// reasonUnitRows is the most contact_requests rows one scanner unit reads.
	reasonUnitRows = 50000
	// reasonPageRequests is the most qualifying requests one page returns.
	reasonPageRequests = 100
	// reasonPageBudget is a page route's deadline, including its busy retries.
	reasonPageBudget = 10 * time.Second
	// reasonBlankBatch is how many decisions one write batch holds: one audit
	// INSERT and the matching blanking UPDATE (T07B-2 F4). A var so tests can
	// shorten it.
	reasonBlankBatch = 1000
	// reasonGuardEvery is how many streamed rows pass between checks of the
	// context and the commit reserve. A var so a test can check every row.
	reasonGuardEvery uint64 = 256
	// reasonRowHook and reasonBatchHook are test seams, nil in production: the
	// first runs after each streamed row is counted (seen is 1 for the first),
	// the second at the top of each write batch (0 for the first), before that
	// batch's context check. A test uses them to park or spend the deletion.
	reasonRowHook   func(seen uint64)
	reasonBatchHook func(batch int)
)

const (
	// reasonReviewVersion is the audit marker version of these decisions.
	reasonReviewVersion = 1
	// reasonSourceDeletion marks a decision made inside an account deletion.
	reasonSourceDeletion = "deletion"
	// reasonStatementTimeout bounds each statement of a page transaction.
	reasonStatementTimeout = 10 * time.Second
)

var (
	// errNeedsReasonReview: the account has reasons to review and the DELETE
	// carried no review.
	errNeedsReasonReview = apiErr(http.StatusConflict, "needs_reason_review", "the account has contact-request reasons to review")
	// errReasonReviewChanged: the reviewed set is not the current set.
	errReasonReviewChanged = apiErr(http.StatusConflict, "reason_review_changed", "the contact-request reasons changed since they were reviewed")
	// errReasonReviewForbidden: the signed-in admin may not review reasons.
	errReasonReviewForbidden = apiErr(http.StatusForbidden, "reason_review_not_allowed", "only an adult admin can review these reasons")
	// errReasonReviewOutdated: the body carried the pre-set-v1 items envelope and
	// the account has reasons; nothing in it is ever applied.
	errReasonReviewOutdated = apiErr(http.StatusConflict, "reason_review_outdated", "the page that made this review is out of date")
	// errReasonCounterpartBusy: blanking a reason made the request trigger
	// share-lock a soft-deleted party's users row NOWAIT (migration 00007) and
	// another transaction held it, SQLSTATE 55P03. It wraps the *pgconn.PgError,
	// so errors.As still finds the code and the household retry rules apply
	// unchanged (only a 55P03 whose ROLLBACK was acknowledged is retried); it
	// exists so the answer names this cause and not a busy household.
	errReasonCounterpartBusy = apiErr(http.StatusConflict, "delete_reason_busy", "someone was changing a request that involves this person, so nothing was deleted")
	// errReasonReviewPageForbidden is errReasonReviewForbidden for the review
	// page, whose answer also tells the panel a review is required.
	errReasonReviewPageForbidden = apiErrWith(errReasonReviewForbidden, gin.H{"reviewRequired": true})
	// errReasonReviewBusy: the page could not load the reasons (busy after
	// every try, a timeout or a database error), which is not "no reasons".
	errReasonReviewBusy = apiErr(http.StatusServiceUnavailable, "reason_review_busy", "could not load the reasons")
	// errReasonInvariant: a blanking changed a different number of rows than
	// the review named; the whole deletion rolls back.
	errReasonInvariant = errors.New("the reason review changed an unexpected number of rows")
)

// --- request ids ------------------------------------------------------------

// parseRequestID reads a canonical signed decimal request id. It refuses a
// plus sign, a leading zero, "-0", whitespace, an empty string and anything
// outside int64 (R4-C04); no positivity is assumed.
func parseRequestID(s string) (int64, bool) {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || strconv.FormatInt(v, 10) != s {
		return 0, false
	}
	return v, true
}

// int64Array formats ids as a Postgres array literal for a ?::bigint[]
// parameter: GORM would expand a slice into a list of values.
func int64Array(ids []int64) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, id := range ids {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatInt(id, 10))
	}
	b.WriteByte('}')
	return b.String()
}

// --- the review hash --------------------------------------------------------

type reasonColumn struct{ name, kind string }

// The kinds of hashed column, named as information_schema.columns names their
// data_type so TestReviewHashCoversEveryColumn can compare them.
const (
	reasonBigint = "bigint"
	reasonText   = "text"
	reasonBool   = "boolean"
	reasonTime   = "timestamp with time zone"
)

// reasonColumns are the contact_requests columns the review hash covers, in
// table order. add_childcare_ages_note is the only one left out: the same
// deletion blanks it (R4-T07A), so hashing it would make that blank change the
// hash of a request the admin decides. A new column that is neither listed nor
// excluded fails TestReviewHashCoversEveryColumn.
var reasonColumns = []reasonColumn{
	{"id", reasonBigint},
	{"requester_id", reasonBigint},
	{"subject_user_id", reasonBigint},
	{"context_type", reasonText},
	{"context_id", reasonBigint},
	{"fields", reasonText},
	{"reason", reasonText},
	{"needs_admin", reasonBool},
	{"subject_decision", reasonText},
	{"subject_decided_by", reasonBigint},
	{"subject_decided_at", reasonTime},
	{"admin_decision", reasonText},
	{"admin_decided_by", reasonBigint},
	{"admin_decided_at", reasonTime},
	{"status", reasonText},
	{"created_at", reasonTime},
	{"expires_at", reasonTime},
	{"completed_at", reasonTime},
	{"withdrawn_by_id", reasonBigint},
	{"end_reason", reasonText},
	{"purpose", reasonText},
	{"occurrence_id", reasonBigint},
	{"add_over_limit", reasonBool},
	{"add_over_capacity", reasonBool},
	{"add_attendance_mode", reasonText},
	{"add_guest_count", reasonBigint},
	{"add_childcare_count", reasonBigint},
	{"completed_target_id", reasonBigint},
}

// reasonHashExcluded are the contact_requests columns the hash leaves out, and
// why (see reasonColumns).
var reasonHashExcluded = []string{"add_childcare_ages_note"}

// reasonColIndex maps a hashed column to its place in reasonColumns.
var reasonColIndex = func() map[string]int {
	m := make(map[string]int, len(reasonColumns))
	for i, c := range reasonColumns {
		m[c.name] = i
	}
	return m
}()

// reasonProjection is the SQL text projection of the hashed columns
// (reviewHashColumns): bigint as text, text as it is, boolean as 't' or 'f',
// timestamptz as its exact microsecond epoch (or 'infinity' and '-infinity'),
// and NULL stays NULL in every kind.
func reasonProjection() string {
	parts := make([]string, len(reasonColumns))
	for i, c := range reasonColumns {
		col := `"` + c.name + `"`
		switch c.kind {
		case reasonBigint:
			parts[i] = col + "::text"
		case reasonText:
			parts[i] = col
		case reasonBool:
			parts[i] = "CASE " + col + " WHEN true THEN 't' WHEN false THEN 'f' END"
		case reasonTime:
			parts[i] = "CASE WHEN " + col + " = 'infinity' THEN 'infinity' WHEN " + col + " = '-infinity' THEN '-infinity' " +
				"ELSE trunc(extract(epoch FROM " + col + ") * 1000000)::text END"
		default:
			panic("reasonProjection: unknown column kind " + c.kind)
		}
	}
	return strings.Join(parts, ", ")
}

// reasonSelect is the column list every review statement reads: the hashed
// projection, then created_at as a time.
var reasonSelect = reasonProjection() + ", created_at"

// reasonHashPrefix is the 21-byte start of every hashed message.
const reasonHashPrefix = "fold-reason-review-v1"

// reasonHashKey derives the HMAC key from SESSION_SECRET the way the VAPID
// cipher does. A changed secret makes every open panel's hashes stale (409,
// reload). The insecure development secret gives no secrecy and none is
// claimed.
func reasonHashKey(secret []byte) ([]byte, error) {
	return deriveKey(secret, "erasure-reason-review:v1")
}

// reviewHashRaw is HMAC-SHA256 over the prefix, then the viewing admin's id, the
// party's id and the hashed columns, each framed (NULL is 0x00; otherwise 0x01,
// a 4-byte big-endian length and the bytes). The viewer and the party are in
// the message so a hash proves this admin was served this item for this party.
// It is the 32 raw bytes; the page shows them as hex. Never stored, not a
// capability and not authority; the set digest takes the raw bytes as they are,
// so the streamed consume never round-trips through hex.
func reviewHashRaw(key []byte, viewerID, partyID int64, fields []sql.NullString) [sha256.Size]byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(reasonHashPrefix))
	frame := func(s sql.NullString) {
		if !s.Valid {
			mac.Write([]byte{0})
			return
		}
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(s.String)))
		mac.Write([]byte{1})
		mac.Write(n[:])
		mac.Write([]byte(s.String))
	}
	frame(sql.NullString{String: strconv.FormatInt(viewerID, 10), Valid: true})
	frame(sql.NullString{String: strconv.FormatInt(partyID, 10), Valid: true})
	for _, f := range fields {
		frame(f)
	}
	var out [sha256.Size]byte
	mac.Sum(out[:0])
	return out
}

func validReviewHash(h string) bool {
	if len(h) != 2*sha256.Size {
		return false
	}
	for i := 0; i < len(h); i++ {
		c := h[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// --- reading request rows ---------------------------------------------------

// reasonRow is one contact_requests row as the review reads it. Every id is
// int64 and the row is filled by a raw scan, never through
// models.ContactRequest (whose ids are uint).
type reasonRow struct {
	ID, RequesterID, SubjectUserID int64
	ContextType                    string
	ContextID                      int64
	Purpose, Status, Reason        string
	CreatedAt                      time.Time
	// Hash is the review hash for the viewer and party it was read for, as hex.
	// Raw is the same MAC before encoding; Hash is empty when the caller asked
	// for raw hashes only.
	Hash string
	Raw  [sha256.Size]byte
}

// The places of the columns fill reads, found once.
var (
	reasonIdxID, reasonIdxRequester, reasonIdxSubject, reasonIdxContext      = reasonColIndex["id"], reasonColIndex["requester_id"], reasonColIndex["subject_user_id"], reasonColIndex["context_id"]
	reasonIdxContextType, reasonIdxPurpose, reasonIdxStatus, reasonIdxReason = reasonColIndex["context_type"], reasonColIndex["purpose"], reasonColIndex["status"], reasonColIndex["reason"]
)

// fill reads the ids and labels back out of a reasonSelect row. A non-nil
// intern shares the strings it returns (purpose and status repeat a handful of
// values) and skips the text and the context type, which a caller that keeps
// only the facts of an audit row does not read.
func (r *reasonRow) fill(fields []sql.NullString, created time.Time, intern map[string]string) error {
	for _, p := range [...]struct {
		idx  int
		name string
		dst  *int64
	}{{reasonIdxID, "id", &r.ID}, {reasonIdxRequester, "requester_id", &r.RequesterID}, {reasonIdxSubject, "subject_user_id", &r.SubjectUserID}, {reasonIdxContext, "context_id", &r.ContextID}} {
		v, err := strconv.ParseInt(fields[p.idx].String, 10, 64)
		if err != nil {
			return fmt.Errorf("contact_requests.%s is not an integer", p.name)
		}
		*p.dst = v
	}
	if intern == nil {
		r.ContextType, r.Purpose, r.Status, r.Reason = fields[reasonIdxContextType].String, fields[reasonIdxPurpose].String, fields[reasonIdxStatus].String, fields[reasonIdxReason].String
	} else {
		r.Purpose, r.Status = internString(intern, fields[reasonIdxPurpose].String), internString(intern, fields[reasonIdxStatus].String)
	}
	r.CreatedAt = created
	return nil
}

// internString returns one shared copy of s.
func internString(m map[string]string, s string) string {
	if v, ok := m[s]; ok {
		return v
	}
	m[s] = s
	return s
}

// scanReasonRows reads every row of rows with reasonSelect's shape and hashes
// each for (viewer, party). It calls keep with the row; the caller decides
// whether to retain it. rows is closed.
func scanReasonRows(rows *sql.Rows, key []byte, viewer, party int64, keep func(reasonRow) error) error {
	return scanReasonRowsWith(rows, key, viewer, party, true, nil, keep)
}

// scanReasonRowsWith is scanReasonRows with two knobs for the streamed consume:
// wantHex false leaves Hash empty (Raw is always set), and a non-nil intern
// makes the row carry only the facts of an audit row (see reasonRow.fill). The
// scan buffers are made once per call and reused for every row (F4).
func scanReasonRowsWith(rows *sql.Rows, key []byte, viewer, party int64, wantHex bool, intern map[string]string, keep func(reasonRow) error) error {
	defer rows.Close()
	fields := make([]sql.NullString, len(reasonColumns))
	dest := make([]any, 0, len(fields)+1)
	for i := range fields {
		dest = append(dest, &fields[i])
	}
	var created time.Time
	dest = append(dest, &created)
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return err
		}
		var r reasonRow
		if err := r.fill(fields, created, intern); err != nil {
			return err
		}
		r.Raw = reviewHashRaw(key, viewer, party, fields)
		if wantHex {
			r.Hash = hex.EncodeToString(r.Raw[:])
		}
		if err := keep(r); err != nil {
			return err
		}
	}
	return rows.Err()
}

// --- the scanner ------------------------------------------------------------

// reasonSource is the part of a request the scanner decides from.
type reasonSource struct {
	ID, RequesterID, SubjectUserID int64
	HasReason                      bool
}

// reasonFilter returns the parties of a source row that qualify as items, in
// the order they are emitted. An empty answer means the row is not an item.
type reasonFilter func(reasonSource) []int64

// liveReasonFilter qualifies the rows where the live target is a party and
// the reason is not blank. contact_requests_check keeps requester_id and
// subject_user_id apart, so there is at most one party.
func liveReasonFilter(target int64) reasonFilter {
	return func(s reasonSource) []int64 {
		if !s.HasReason || (s.RequesterID != target && s.SubjectUserID != target) {
			return nil
		}
		if policy.ErasureReasonItemState(policy.ErasureReasonFacts{}) != policy.ReasonItemLive {
			return nil
		}
		return []int64{target}
	}
}

// reasonUnit is what one scanner unit found: the requests that qualify, in id
// order, and the cursor to continue from (nil when the table is exhausted).
type reasonUnit struct {
	Requests []int64
	Next     *int64
}

// The scanner's two statements, chosen in Go. Neither has a NULL-or-condition:
// a generic (prepared) plan cannot turn "$1 IS NULL OR id > $1" into an index
// condition, so a continuation would rescan from the start. The first has no
// id predicate at all; the continuation has one the primary key serves (F5,
// SC-R3).
const (
	reasonScanInitialSQL      = "SELECT id, requester_id, subject_user_id, (reason <> '') AS has_reason FROM contact_requests ORDER BY id LIMIT ?"
	reasonScanContinuationSQL = "SELECT id, requester_id, subject_user_id, (reason <> '') AS has_reason FROM contact_requests WHERE id > ?::bigint ORDER BY id LIMIT ?"
)

// scanReasonItems reads one unit: at most reasonUnitRows rows by primary key,
// filtered in Go, stopping right after the pageRequests-th qualifying
// request, so a request's parties are returned together or not at all
// (R4-C01). next is that request's id; with fewer qualifying requests it is
// the last id read when the unit read its whole budget, else nil. No statement
// scans the table: the first page has no lower bound but still reads at most
// reasonUnitRows rows. pageRequests is an argument only so a test can shorten
// it; production passes reasonPageRequests.
func scanReasonItems(tx *gorm.DB, filter reasonFilter, after *int64, pageRequests int) (reasonUnit, error) {
	var unit reasonUnit
	var rows *sql.Rows
	var err error
	if after == nil {
		rows, err = tx.Raw(reasonScanInitialSQL, reasonUnitRows).Rows()
	} else {
		rows, err = tx.Raw(reasonScanContinuationSQL, *after, reasonUnitRows).Rows()
	}
	if err != nil {
		return unit, err
	}
	defer rows.Close()
	read := 0
	var last int64
	for rows.Next() {
		var s reasonSource
		if err := rows.Scan(&s.ID, &s.RequesterID, &s.SubjectUserID, &s.HasReason); err != nil {
			return unit, err
		}
		read++
		last = s.ID
		if len(filter(s)) == 0 {
			continue
		}
		unit.Requests = append(unit.Requests, s.ID)
		if len(unit.Requests) == pageRequests {
			next := s.ID
			unit.Next = &next
			return unit, rows.Err()
		}
	}
	if err := rows.Err(); err != nil {
		return unit, err
	}
	if read >= reasonUnitRows {
		unit.Next = &last
	}
	return unit, nil
}

// --- the page route ---------------------------------------------------------

type reasonPageBody struct {
	After *string `json:"after"`
}

// reasonItemJSON is one reason in a page. requestId and counterpartRef are
// decimal strings so a signed 64-bit id survives JavaScript.
type reasonItemJSON struct {
	RequestID          string    `json:"requestId"`
	ReviewHash         string    `json:"reviewHash"`
	Role               string    `json:"role"`
	Purpose            string    `json:"purpose"`
	Status             string    `json:"status"`
	CreatedAt          time.Time `json:"createdAt"`
	CounterpartName    string    `json:"counterpartName"`
	CounterpartDeleted bool      `json:"counterpartDeleted"`
	CounterpartRef     string    `json:"counterpartRef,omitempty"`
	ReviewerIsParty    bool      `json:"reviewerIsParty"`
	ContextLabel       string    `json:"contextLabel,omitempty"`
	Reason             string    `json:"reason"`
}

// reasonPageJSON is a page's answer. reviewRequired is present only on a
// nonempty page: one empty unit proves nothing, and the client concludes there
// are no reasons only when a traversal from null ends with zero items.
type reasonPageJSON struct {
	ReviewRequired bool             `json:"reviewRequired,omitempty"`
	Items          []reasonItemJSON `json:"items"`
	Next           *string          `json:"next"`
}

const deletedAccountName = "Deleted account"

// reasonPageStep is one attempt of a page: everything that can differ between
// attempts is read inside it.
type reasonPageStep struct {
	page reasonPageJSON
	// predFailed: the seal's predicate (not the plain admin test) refused.
	predFailed bool
}

// reasonPageTx runs fn in one transaction on ctx whose statements the server
// also bounds (R4-CTX): a client deadline is not server cancellation.
func (a *App) reasonPageTx(ctx context.Context, fn func(tx *gorm.DB) error) error {
	return a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT set_config('statement_timeout', ?, true)", fmt.Sprintf("%dms", ceilMillis(reasonStatementTimeout))).Error; err != nil {
			return err
		}
		return fn(tx)
	})
}

// reasonPageAttempt reads one scanner unit as the signed-in admin, writes the
// view audit and ends with the actor seal. Order: the early plain read of the
// actor, the unit, the view audit, the readBeforeSeal seam, the seal. The seal
// is the last locking statement; on a busy actor row the caller reruns the
// whole attempt.
func (a *App) reasonPageAttempt(ctx context.Context, me, target *models.User, after *int64, step *reasonPageStep) error {
	return a.reasonPageTx(ctx, func(tx *gorm.DB) error {
		*step = reasonPageStep{}
		actorRow, err := lockActorIn(tx, me, actorRead)
		if err != nil {
			return err
		}
		if !policy.IsAdmin(asPerson(actorRow)) {
			return errActorForbidden
		}
		actor := *actorRow
		admins, err := adminCountIn(tx)
		if err != nil {
			return err
		}
		if !policy.CanDemoteOrDelete(asPerson(&actor), asPerson(target), admins) {
			return adminRuleRefusal(adminDelete, target.ID == actor.ID)
		}
		unit, err := scanReasonUnit(tx, liveReasonFilter(int64(target.ID)), after, reasonPageRequests, reasonScanBatched)
		if err != nil {
			return err
		}
		step.page = reasonPageJSON{Items: []reasonItemJSON{}}
		if unit.Next != nil {
			s := strconv.FormatInt(*unit.Next, 10)
			step.page.Next = &s
		}
		var pred func(*models.User) bool
		if len(unit.Requests) > 0 {
			rule, err := reasonReviewRuleIn(tx, target, admins)
			if err != nil {
				return err
			}
			if !rule.allows(&actor) {
				step.predFailed = true
				return errReasonReviewForbidden
			}
			pred = func(u *models.User) bool {
				ok := rule.allows(u)
				step.predFailed = !ok
				return ok
			}
			items, err := a.reasonPageItems(tx, &actor, target, unit.Requests)
			if err != nil {
				return err
			}
			step.page.ReviewRequired = true
			step.page.Items = items
			if len(items) > 0 {
				if err := audit.Write(tx, audit.Entry{Actor: &actor.ID, Action: audit.UserErasureReasonsViewed,
					TargetType: "user", TargetID: target.ID, Meta: map[string]any{"count": len(items), "source": reasonSourceDeletion}}); err != nil {
					return err
				}
			}
		}
		if a.hooks.readBeforeSeal != nil {
			a.hooks.readBeforeSeal(tx)
		}
		// Last statement: nothing locks after it.
		return currentAdminIn(tx, me, pred)
	})
}

// reasonPageItems reads the named requests as items for the target, hashed for
// this admin. A request whose reason was blanked since the unit read it is not
// an item any more.
func (a *App) reasonPageItems(tx *gorm.DB, actor, target *models.User, ids []int64) ([]reasonItemJSON, error) {
	key, err := reasonHashKey(a.Config.SessionSecret)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Raw("SELECT "+reasonSelect+" FROM contact_requests WHERE id = ANY(?::bigint[]) AND reason <> '' AND (requester_id = ? OR subject_user_id = ?) ORDER BY contact_requests.id",
		int64Array(ids), target.ID, target.ID).Rows()
	if err != nil {
		return nil, err
	}
	var read []reasonRow
	if err := scanReasonRows(rows, key, int64(actor.ID), int64(target.ID), func(r reasonRow) error {
		read = append(read, r)
		return nil
	}); err != nil {
		return nil, err
	}
	// Labels are live and not hashed: the counterpart's name and the group or
	// event the request was made in.
	counterparts := map[uint]reasonCounterpartFacts{}
	var other []uint
	for _, r := range read {
		other = append(other, uint(reasonCounterpart(r, int64(target.ID))))
	}
	if len(other) > 0 {
		var users []models.User
		if err := tx.Unscoped().Select("id", "name", "display_name", "deleted_at").Where("id IN ?", other).Find(&users).Error; err != nil {
			return nil, err
		}
		for i := range users {
			u := &users[i]
			counterparts[u.ID] = reasonCounterpartFacts{u.PublicName(), u.DeletedAt.Valid}
		}
	}
	labels, err := a.reasonContextLabels(tx, actor, read)
	if err != nil {
		return nil, err
	}
	out := make([]reasonItemJSON, 0, len(read))
	for _, r := range read {
		role := "subject"
		if r.RequesterID == int64(target.ID) {
			role = "requester"
		}
		other := uint(reasonCounterpart(r, int64(target.ID)))
		item := reasonItemJSON{
			RequestID: strconv.FormatInt(r.ID, 10), ReviewHash: r.Hash, Role: role, Purpose: r.Purpose, Status: r.Status,
			CreatedAt: r.CreatedAt, ReviewerIsParty: int64(actor.ID) == r.RequesterID || int64(actor.ID) == r.SubjectUserID,
			ContextLabel: labels[r.ID], Reason: r.Reason,
		}
		cp, found := counterparts[other]
		switch {
		case !found || cp.deleted:
			// A tombstone, or a row we cannot read, is never named.
			item.CounterpartName, item.CounterpartDeleted = deletedAccountName, true
			item.CounterpartRef = strconv.FormatUint(uint64(other), 10)
		default:
			item.CounterpartName = cp.name
		}
		out = append(out, item)
	}
	return out, nil
}

// reasonCounterpartFacts are the live facts the page shows about the other party.
type reasonCounterpartFacts struct {
	name    string
	deleted bool
}

// reasonCounterpart is the other party of a request the target is in.
func reasonCounterpart(r reasonRow, target int64) int64 {
	if r.RequesterID == target {
		return r.SubjectUserID
	}
	return r.RequesterID
}

// reasonContextLabels names the group or event each request was made in, only
// when requestContexts.shown says the actor may see it: the same rule the
// contact and add request lists use.
func (a *App) reasonContextLabels(tx *gorm.DB, actor *models.User, rows []reasonRow) (map[int64]string, error) {
	refs := make([]contextRef, 0, len(rows))
	for _, r := range rows {
		if r.ContextID > 0 {
			refs = append(refs, contextRef{r.ContextType, uint(r.ContextID)})
		}
	}
	contexts, err := loadRequestContexts(tx, refs, []uint{actor.ID})
	if err != nil {
		return nil, err
	}
	out := map[int64]string{}
	for _, r := range rows {
		if r.ContextID <= 0 {
			continue
		}
		if label, ok := contexts.shown(a, actor, contextRef{r.ContextType, uint(r.ContextID)}); ok {
			out[r.ID] = label
		}
	}
	return out, nil
}

// apiAdminErasureReasonsPage serves one audited page of the reasons an account
// would leave behind (POST /api/v1/admin/users/:id/erasure-reasons/page). It
// writes a view audit row, so it is a POST and carries the CSRF token. A
// timeout or failure answers 503 reason_review_busy and is never "no reasons":
// the client retries the same cursor.
func (a *App) apiAdminErasureReasonsPage(c *gin.Context) {
	me := apiUser(c)
	target, ok := a.adminTargetUser(c)
	if !ok {
		return
	}
	var body reasonPageBody
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid body")
		return
	}
	var after *int64
	if body.After != nil {
		v, ok := parseRequestID(*body.After)
		if !ok {
			jsonError(c, http.StatusBadRequest, "invalid cursor")
			return
		}
		after = &v
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), reasonPageBudget)
	defer cancel()
	retry := newBusyRetry(ctx)
	var step reasonPageStep
	var err error
	for {
		err = a.reasonPageAttempt(ctx, me, target, after, &step)
		// A busy actor seal runs the whole attempt again (R5-C01).
		if !errors.Is(err, errActorBusy) || !rollbackConfirmed(err) {
			break
		}
		if again, _ := retry.again(); !again {
			break
		}
	}
	switch {
	case err == nil:
		c.JSON(http.StatusOK, step.page)
	case errors.Is(err, errSecuritySession):
		fail(c, errSecuritySession)
	case errors.Is(err, errReasonReviewForbidden) || (errors.Is(err, errActorForbidden) && step.predFailed):
		fail(c, errReasonReviewPageForbidden)
	case errors.Is(err, errActorForbidden):
		fail(c, err)
	default:
		if isAdminRuleRefusal(err) {
			fail(c, err)
			return
		}
		// Busy after every try, a timeout, or a database error: not "empty".
		log.Printf("reason review page failed (busy: %v): %s", errors.Is(err, errActorBusy), jobs.CleanError(err))
		fail(c, errReasonReviewBusy)
	}
}

// --- the deletion's consume step --------------------------------------------

// reasonDecision is one decided item. It holds no text and no hash.
type reasonDecision struct {
	RequestID       int64
	Remove          bool
	Role            string // the erased account's part: requester or subject
	Purpose, Status string
	ReviewerIsParty bool
}

// reasonPlan is what the deletion decided about the account's reasons.
type reasonPlan struct {
	Decisions []reasonDecision
	ActorID   uint
	IP        string
	// mayReview is what the deletion's seal decides again on the locked actor
	// row (reasonReviewRule.allows); nil when the account has no reasons.
	mayReview func(*models.User) bool
}

func (p reasonPlan) counts() (kept, removed int) {
	for _, d := range p.Decisions {
		if d.Remove {
			removed++
		} else {
			kept++
		}
	}
	return kept, removed
}

// errReasonStreamStop ends the streamed read early inside scanReasonRowsWith.
// It never leaves planReasonReviewIn.
var errReasonStreamStop = errors.New("reason stream stopped")

// reasonSetPrefix starts every set digest message (26 bytes, then 0x00).
const reasonSetPrefix = "fold:erasure-reason-set:v1"

// reasonSetHasher is the incremental set digest (F2): SHA-256 over the prefix,
// a zero byte, the claimed count as a big-endian uint64, then for every item in
// ascending signed id order the id as a big-endian two's complement int64 and
// the raw 32-byte review hash. The preimage is 35 + 40n bytes. The unkeyed
// outer hash adds order and completeness; the review hash already binds the
// viewer, the party and every hashed column.
type reasonSetHasher struct{ h hash.Hash }

func newReasonSetHasher(count uint64) *reasonSetHasher {
	h := sha256.New()
	var head [len(reasonSetPrefix) + 1 + 8]byte
	copy(head[:], reasonSetPrefix)
	binary.BigEndian.PutUint64(head[len(reasonSetPrefix)+1:], count)
	h.Write(head[:])
	return &reasonSetHasher{h: h}
}

func (d *reasonSetHasher) add(id int64, raw *[sha256.Size]byte) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(id))
	d.h.Write(b[:])
	d.h.Write(raw[:])
}

func (d *reasonSetHasher) sum() [sha256.Size]byte {
	var out [sha256.Size]byte
	d.h.Sum(out[:0])
	return out
}

// reasonWriteGuard stops a long consume or write loop when its context has
// ended or too little time is left to send COMMIT (the same reserve
// contactMutationOn keeps), so the deletion rolls back with a known state and
// answers delete_timed_out through classifyContactErr (F4).
func reasonWriteGuard(tx *gorm.DB) error {
	ctx := tx.Statement.Context
	if ctx == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) <= contactCommitReserve {
		return errCommitReserve
	}
	return nil
}

// reasonProbeSQL is the probe statement: one row at most and no lock.
const reasonProbeSQL = "SELECT id FROM contact_requests WHERE reason <> '' AND (requester_id = ? OR subject_user_id = ?) LIMIT 1"

// reasonSetExistsIn is the "is there any reason to review" probe: one row at
// most, no lock. LIMIT 1 bounds what comes back, not what the server reads to
// find it (an absent or late match may scan the table); the deletion budget
// bounds that.
func reasonSetExistsIn(tx *gorm.DB, target uint) (bool, error) {
	rows, err := tx.Raw(reasonProbeSQL, target, target).Rows()
	if err != nil {
		return false, err
	}
	defer rows.Close()
	found := rows.Next()
	return found, rows.Err()
}

// planReasonReviewIn is the consume step, run inside the deletion's gated
// transaction after keepsAnAdminIn.
//
//   - A body with no review, or the old items envelope: one LIMIT 1 probe. No
//     reason: the envelope is ignored. A reason: needs_reason_review for none,
//     reason_review_outdated for the old envelope, whose entries are never
//     applied.
//   - A set-v1 review: one streamed, id-ordered statement locks and reads the
//     account's current reasons, hashing each for (actor, target) and feeding
//     the set digest. No reasons: the review is ignored whatever it says.
//     Otherwise the actor must be allowed to review (reason_review_not_allowed)
//     and the count and digest must equal the claim (reason_review_changed).
//     Go keeps a compact record per row, never more than the claim (the server
//     has read and locked the whole set by then, which the exclusive gate needs).
//
// The plan's mayReview is what the deletion's seal re-decides on the locked
// actor row. actor is the deletion's re-read actor row; target is the locked
// account.
func (a *App) planReasonReviewIn(tx *gorm.DB, actor, target *models.User, env reasonEnvelope, ip string) (reasonPlan, error) {
	plan := reasonPlan{ActorID: actor.ID, IP: ip}
	if env.kind != reasonEnvelopeNew {
		has, err := reasonSetExistsIn(tx, target.ID)
		if err != nil {
			return plan, err
		}
		switch {
		case !has:
			return plan, nil
		case env.kind == reasonEnvelopeLegacy:
			return plan, errReasonReviewOutdated
		default:
			return plan, errNeedsReasonReview
		}
	}
	key, err := reasonHashKey(a.Config.SessionSecret)
	if err != nil {
		return plan, err
	}
	rows, err := tx.Raw("SELECT "+reasonSelect+" FROM contact_requests WHERE reason <> '' AND (requester_id = ? OR subject_user_id = ?) ORDER BY contact_requests.id FOR UPDATE",
		target.ID, target.ID).Rows()
	if err != nil {
		return plan, err
	}
	hasher := newReasonSetHasher(env.count)
	// Never sized from the claim: it grows from real rows.
	decisions := make([]reasonDecision, 0, 64)
	intern := map[string]string{}
	var seen uint64
	var over bool
	err = scanReasonRowsWith(rows, key, int64(actor.ID), int64(target.ID), false, intern, func(r reasonRow) error {
		seen++
		if reasonRowHook != nil {
			reasonRowHook(seen)
		}
		if reasonGuardEvery > 0 && seen%reasonGuardEvery == 0 {
			if err := reasonWriteGuard(tx); err != nil {
				return err
			}
		}
		if seen > env.count {
			over = true
			return errReasonStreamStop
		}
		hasher.add(r.ID, &r.Raw)
		role := "subject"
		if r.RequesterID == int64(target.ID) {
			role = "requester"
		}
		// The text is not kept: only the id and the facts of the audit row.
		decisions = append(decisions, reasonDecision{RequestID: r.ID, Role: role, Purpose: r.Purpose, Status: r.Status,
			ReviewerIsParty: int64(actor.ID) == r.RequesterID || int64(actor.ID) == r.SubjectUserID})
		return nil
	})
	if err != nil && !errors.Is(err, errReasonStreamStop) {
		return plan, err
	}
	if seen == 0 {
		return plan, nil
	}
	if err := reasonWriteGuard(tx); err != nil {
		return plan, err
	}
	admins, err := adminCountIn(tx)
	if err != nil {
		return plan, err
	}
	rule, err := reasonReviewRuleIn(tx, target, admins)
	if err != nil {
		return plan, err
	}
	if !rule.allows(actor) {
		return plan, errReasonReviewForbidden
	}
	if over || seen != env.count {
		return plan, errReasonReviewChanged
	}
	sum := hasher.sum()
	if !hmac.Equal(sum[:], env.digest[:]) {
		return plan, errReasonReviewChanged
	}
	for i := range decisions {
		decisions[i].Remove = env.choiceAt(i) == reasonCodeRemove
	}
	plan.Decisions = decisions
	plan.mayReview = rule.allows
	return plan, nil
}

// reasonReviewRule is policy.CanReviewErasureReasons fixed for one target, on
// one transaction's clock, church zone and admin count: the page and the
// deletion decide the actor with it, and their seals decide the locked actor
// row with it again.
type reasonReviewRule struct {
	now    time.Time
	zone   *time.Location
	target policy.Person
	admins int
}

func reasonReviewRuleIn(tx *gorm.DB, target *models.User, admins int) (reasonReviewRule, error) {
	zone, err := churchZoneIn(tx)
	if err != nil {
		return reasonReviewRule{}, err
	}
	return reasonReviewRule{now: time.Now().UTC(), zone: zone, target: *asPerson(target), admins: admins}, nil
}

func (r reasonReviewRule) allows(u *models.User) bool {
	return policy.CanReviewErasureReasons(contactAccount(u, r.now, r.zone), &r.target, r.admins)
}

// eraseContactRequestReasonsIn writes the decisions and blanks the removed
// reasons. It is the only writer of contact_requests.reason (the guard in
// guards_contact_text_test.go allows its one UPDATE literal here and its one call in
// eraseAccountIn, directly after eraseContactRequestTextIn).
// Decisions go in batches of reasonBlankBatch inside the same transaction: for
// each batch the signed audit rows are written first, then that batch's removes
// are blanked in one UPDATE whose RowsAffected must equal the batch's remove
// count, else the whole deletion rolls back (500). A kept reason changes no
// column. Between batches the context and the commit reserve are checked, and
// the Go side keeps one batch of audit entries at a time.
//
// A 55P03 from the blanking UPDATE of any batch (a request trigger's NOWAIT
// share lock on a soft-deleted party's users row, migration 00007) is wrapped
// with errReasonCounterpartBusy so the answer is delete_reason_busy; the audit
// INSERT cannot raise it.
func eraseContactRequestReasonsIn(tx *gorm.DB, s erasureSubject, plan reasonPlan) error {
	if len(plan.Decisions) == 0 {
		return nil
	}
	actor := plan.ActorID
	size := max(reasonBlankBatch, 1)
	entries := make([]audit.SignedEntry, 0, min(size, len(plan.Decisions)))
	remove := make([]int64, 0, min(size, len(plan.Decisions)))
	for start := 0; start < len(plan.Decisions); start += size {
		if reasonBatchHook != nil {
			reasonBatchHook(start / size)
		}
		if err := reasonWriteGuard(tx); err != nil {
			return err
		}
		batch := plan.Decisions[start:min(start+size, len(plan.Decisions))]
		entries, remove = entries[:0], remove[:0]
		for _, d := range batch {
			k := requestKindOf(d.Purpose)
			if k == nil {
				return fmt.Errorf("reason review: unknown request purpose %q", d.Purpose)
			}
			action, target := k.reasonKept, k.target
			if d.Remove {
				action = k.reasonRemoved
			}
			entries = append(entries, audit.SignedEntry{
				Actor: &actor, Action: action, TargetType: target, TargetID: d.RequestID, IP: plan.IP,
				Meta: map[string]any{
					"erasedUserId": s.ID, "erasedRole": d.Role, "purpose": d.Purpose, "status": d.Status,
					"reviewerIsParty": d.ReviewerIsParty, "source": reasonSourceDeletion, "reviewVersion": reasonReviewVersion,
				},
			})
			if d.Remove {
				remove = append(remove, d.RequestID)
			}
		}
		if err := audit.WriteAll(tx, entries); err != nil {
			return err
		}
		if len(remove) == 0 {
			continue
		}
		res := tx.Exec("UPDATE contact_requests SET reason = '' WHERE id = ANY(?::bigint[]) AND reason <> '' AND (requester_id = ? OR subject_user_id = ?)",
			int64Array(remove), s.ID, s.ID)
		if res.Error != nil {
			if pgCode(res.Error) == pgLockNotAvailable {
				return fmt.Errorf("%w: %w", errReasonCounterpartBusy, res.Error)
			}
			return res.Error
		}
		if res.RowsAffected != int64(len(remove)) {
			return fmt.Errorf("%w: blanked %d of %d", errReasonInvariant, res.RowsAffected, len(remove))
		}
	}
	return nil
}
