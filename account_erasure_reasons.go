package main

// Reason review during an account deletion (M7.4 T07B-2b, owner decision 6).
//
// The review is stateless: nothing about it is stored between the pages an
// admin reads and the DELETE that decides them. Each page item carries a
// keyed reviewHash of the request row as that admin was served it; the DELETE
// carries every {requestId, reviewHash, choice}, and under the exclusive gate
// the server recomputes the current set and every hash. A set that is missing,
// extra or changed in any hashed column is 409 reason_review_changed, so a
// choice can never land on a row the admin did not see.
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
	"github.com/jackc/pgx/v5/pgconn"
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
	// reasonBusyWindow: a busy actor seal may start a new transaction only this
	// long after the first one began (at most reasonBusyTries of them).
	reasonBusyWindow = 2 * time.Second
)

const (
	reasonBusyTries = 3
	// reasonRemoveBatch is how many request ids one blanking UPDATE names.
	reasonRemoveBatch = 1000
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
	errNeedsReasonReview = errors.New("the account has contact-request reasons to review")
	// errReasonReviewChanged: the reviewed set is not the current set.
	errReasonReviewChanged = errors.New("the contact-request reasons changed since they were reviewed")
	// errReasonReviewForbidden: the signed-in admin may not review reasons.
	errReasonReviewForbidden = errors.New("the signed-in admin may not review contact-request reasons")
	// errReasonCounterpartBusy: blanking a reason made the request trigger
	// share-lock a soft-deleted party's users row NOWAIT (migration 00007) and
	// another transaction held it, SQLSTATE 55P03. It wraps the *pgconn.PgError,
	// so errors.As still finds the code and the household retry rules apply
	// unchanged (only a 55P03 whose ROLLBACK was acknowledged is retried); it
	// exists so the answer names this cause and not a busy household.
	errReasonCounterpartBusy = errors.New("another transaction holds a deleted party of a reviewed request")
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
func reasonHashKey(secret []byte) []byte {
	k := sha256.Sum256(append([]byte("fold:erasure-reason-review:v1:"), secret...))
	return k[:]
}

// reviewHashOf is HMAC-SHA256 over the prefix, then the viewing admin's id, the
// party's id and the hashed columns, each framed (NULL is 0x00; otherwise 0x01,
// a 4-byte big-endian length and the bytes). The viewer and the party are in
// the message so a hash proves this admin was served this item for this party.
// It is hex, never stored, not a capability and not authority.
func reviewHashOf(key []byte, viewerID, partyID int64, fields []sql.NullString) string {
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
	return hex.EncodeToString(mac.Sum(nil))
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
	// Hash is the review hash for the viewer and party it was read for.
	Hash string
}

// fill reads the ids and labels back out of a reasonSelect row.
func (r *reasonRow) fill(fields []sql.NullString, created time.Time) error {
	get := func(name string) string { return fields[reasonColIndex[name]].String }
	for name, dst := range map[string]*int64{"id": &r.ID, "requester_id": &r.RequesterID, "subject_user_id": &r.SubjectUserID, "context_id": &r.ContextID} {
		v, err := strconv.ParseInt(get(name), 10, 64)
		if err != nil {
			return fmt.Errorf("contact_requests.%s is not an integer", name)
		}
		*dst = v
	}
	r.ContextType, r.Purpose, r.Status, r.Reason = get("context_type"), get("purpose"), get("status"), get("reason")
	r.CreatedAt = created
	return nil
}

// scanReasonRows reads every row of rows with reasonSelect's shape and hashes
// each for (viewer, party). It calls keep with the row; the caller decides
// whether to retain it. rows is closed.
func scanReasonRows(rows *sql.Rows, key []byte, viewer, party int64, keep func(reasonRow) error) error {
	defer rows.Close()
	for rows.Next() {
		fields := make([]sql.NullString, len(reasonColumns))
		dest := make([]any, 0, len(fields)+1)
		for i := range fields {
			dest = append(dest, &fields[i])
		}
		var created time.Time
		dest = append(dest, &created)
		if err := rows.Scan(dest...); err != nil {
			return err
		}
		var r reasonRow
		if err := r.fill(fields, created); err != nil {
			return err
		}
		r.Hash = reviewHashOf(key, viewer, party, fields)
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

// scanReasonItems reads one unit: at most reasonUnitRows rows by primary key,
// filtered in Go, stopping right after the reasonPageRequests-th qualifying
// request, so a request's parties are returned together or not at all
// (R4-C01). next is that request's id; with fewer qualifying requests it is
// the last id read when the unit read its whole budget, else nil. No statement
// scans the table: the first page has no lower bound but still reads at most
// reasonUnitRows rows.
func scanReasonItems(tx *gorm.DB, filter reasonFilter, after *int64) (reasonUnit, error) {
	var unit reasonUnit
	rows, err := tx.Raw("SELECT id, requester_id, subject_user_id, (reason <> '') AS has_reason FROM contact_requests WHERE (?::bigint IS NULL OR id > ?::bigint) ORDER BY id LIMIT ?",
		after, after, reasonUnitRows).Rows()
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
		if len(unit.Requests) == reasonPageRequests {
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
		var actor models.User
		if err := tx.First(&actor, me.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errSecuritySession
			}
			return err
		}
		if !actor.CanSignIn() || actor.SessionVersion != me.SessionVersion || actor.LogoutVersion != me.LogoutVersion {
			return errSecuritySession
		}
		if !policy.IsAdmin(asPerson(&actor)) {
			return errActorForbidden
		}
		admins, err := adminCountIn(tx)
		if err != nil {
			return err
		}
		if !policy.CanDemoteOrDelete(asPerson(&actor), asPerson(target), admins) {
			msg := "cannot delete the last admin"
			if target.ID == actor.ID {
				msg = "you cannot delete your own account"
			}
			return errAdminRule{msg: msg}
		}
		unit, err := scanReasonItems(tx, liveReasonFilter(int64(target.ID)), after)
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
			now := time.Now().UTC()
			zone, err := contactZone(tx)
			if err != nil {
				return err
			}
			targetPerson := *asPerson(target)
			if !policy.CanReviewErasureReasons(contactAccount(&actor, now, zone), &targetPerson, admins) {
				step.predFailed = true
				return errReasonReviewForbidden
			}
			pred = func(u *models.User) bool {
				ok := policy.CanReviewErasureReasons(contactAccount(u, now, zone), &targetPerson, admins)
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
		if a.readBeforeSeal != nil {
			a.readBeforeSeal(tx)
		}
		// Last statement: nothing locks after it.
		return currentAdminIn(tx, me, pred)
	})
}

// reasonPageItems reads the named requests as items for the target, hashed for
// this admin. A request whose reason was blanked since the unit read it is not
// an item any more.
func (a *App) reasonPageItems(tx *gorm.DB, actor, target *models.User, ids []int64) ([]reasonItemJSON, error) {
	rows, err := tx.Raw("SELECT "+reasonSelect+" FROM contact_requests WHERE id = ANY(?::bigint[]) AND reason <> '' AND (requester_id = ? OR subject_user_id = ?) ORDER BY id",
		int64Array(ids), target.ID, target.ID).Rows()
	if err != nil {
		return nil, err
	}
	key := reasonHashKey(a.Config.SessionSecret)
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
	labels, err := reasonContextLabels(tx, actor, read)
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
// when canSeeGroupIn allows the actor to see the group it belongs to.
func reasonContextLabels(tx *gorm.DB, actor *models.User, rows []reasonRow) (map[int64]string, error) {
	groupIDs, eventIDs := map[uint]bool{}, map[uint]bool{}
	for _, r := range rows {
		switch {
		case r.ContextType == policy.ContactGroup && r.ContextID > 0:
			groupIDs[uint(r.ContextID)] = true
		case r.ContextType == policy.ContactEvent && r.ContextID > 0:
			eventIDs[uint(r.ContextID)] = true
		}
	}
	groups := map[uint]models.Group{}
	events := map[uint]models.Event{}
	if len(eventIDs) > 0 {
		var evs []models.Event
		if err := tx.Where("id IN ?", reasonIDList(eventIDs)).Find(&evs).Error; err != nil {
			return nil, err
		}
		for _, e := range evs {
			events[e.ID] = e
			if e.GroupID != nil {
				groupIDs[*e.GroupID] = true
			}
		}
	}
	if len(groupIDs) > 0 {
		var gs []models.Group
		if err := tx.Where("id IN ?", reasonIDList(groupIDs)).Find(&gs).Error; err != nil {
			return nil, err
		}
		for _, g := range gs {
			groups[g.ID] = g
		}
	}
	shown := func(id uint) *models.Group {
		g, ok := groups[id]
		if !ok || !canSeeGroupIn(tx, actor, &g) {
			return nil
		}
		return &g
	}
	out := map[int64]string{}
	for _, r := range rows {
		switch r.ContextType {
		case policy.ContactGroup:
			if g := shown(uint(r.ContextID)); g != nil {
				out[r.ID] = g.Name
			}
		case policy.ContactEvent:
			e, ok := events[uint(r.ContextID)]
			if !ok {
				continue
			}
			if e.GroupID != nil && shown(*e.GroupID) == nil {
				continue
			}
			out[r.ID] = e.Title
		}
	}
	return out, nil
}

func reasonIDList(m map[uint]bool) []uint {
	out := make([]uint, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
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
	start := time.Now()
	var step reasonPageStep
	var err error
	for attempt := 0; attempt < reasonBusyTries; attempt++ {
		err = a.reasonPageAttempt(ctx, me, target, after, &step)
		if !errors.Is(err, errActorBusy) {
			break
		}
		if attempt == reasonBusyTries-1 || time.Since(start) >= reasonBusyWindow {
			break
		}
		wait := 25 * time.Millisecond
		if attempt > 0 {
			wait = 100 * time.Millisecond
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
		}
		if ctx.Err() != nil || time.Since(start) >= reasonBusyWindow {
			break
		}
	}
	switch {
	case err == nil:
		c.JSON(http.StatusOK, step.page)
	case errors.Is(err, errSecuritySession):
		jsonError(c, http.StatusUnauthorized, "unauthorized")
	case errors.Is(err, errReasonReviewForbidden) || (errors.Is(err, errActorForbidden) && step.predFailed):
		c.JSON(http.StatusForbidden, gin.H{"error": "only an adult admin can review these reasons", "code": "reason_review_not_allowed", "reviewRequired": true})
	case errors.Is(err, errActorForbidden):
		jsonError(c, http.StatusForbidden, "forbidden")
	default:
		var rule errAdminRule
		if errors.As(err, &rule) {
			jsonError(c, http.StatusBadRequest, rule.msg)
			return
		}
		// Busy after every try, a timeout, or a database error: not "empty".
		log.Printf("reason review page failed (busy: %v): %s", errors.Is(err, errActorBusy), jobs.CleanError(err))
		jsonErrorCode(c, http.StatusServiceUnavailable, "reason_review_busy", "could not load the reasons")
	}
}

// --- the deletion's consume step --------------------------------------------

// reasonReviewBody is the DELETE body's review: every reason the admin was
// shown, with the hash it carried and the choice made.
type reasonReviewBody struct {
	Items []reasonChoiceBody `json:"items"`
}

type reasonChoiceBody struct {
	RequestID  string `json:"requestId"`
	ReviewHash string `json:"reviewHash"`
	Choice     string `json:"choice"`
}

type reasonChoice struct {
	hash   string
	remove bool
}

// parseReasonReview checks the body's shape: canonical ids, 64-character
// lowercase hashes, a choice of keep or remove, and no id twice. ok is false
// for anything else (a 400). A nil review parses to a nil map.
func parseReasonReview(rr *reasonReviewBody) (map[int64]reasonChoice, bool) {
	if rr == nil {
		return nil, true
	}
	out := make(map[int64]reasonChoice, len(rr.Items))
	for _, it := range rr.Items {
		id, ok := parseRequestID(it.RequestID)
		if !ok || !validReviewHash(it.ReviewHash) || (it.Choice != "keep" && it.Choice != "remove") {
			return nil, false
		}
		if _, dup := out[id]; dup {
			return nil, false
		}
		out[id] = reasonChoice{hash: it.ReviewHash, remove: it.Choice == "remove"}
	}
	return out, true
}

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

// planReasonReviewIn is the consume step, run inside the deletion's gated
// transaction after keepsAnAdminIn. One id-ordered statement locks and reads
// the account's current reasons and hashes each row for (actor, target). No
// reasons: no review, the body is ignored and nothing is decided. Otherwise
// the review must be there (needs_reason_review), the actor must be allowed
// to review (reason_review_not_allowed) and the body must name exactly the
// current set with equal hashes (reason_review_changed). The returned
// predicate is what the deletion's seal re-decides on the locked actor row.
// actor is the deletion's re-read actor row; target is the locked account.
func (a *App) planReasonReviewIn(tx *gorm.DB, actor, target *models.User, review map[int64]reasonChoice, supplied bool, ip string) (reasonPlan, func(*models.User) bool, error) {
	plan := reasonPlan{ActorID: actor.ID, IP: ip}
	rows, err := tx.Raw("SELECT "+reasonSelect+" FROM contact_requests WHERE reason <> '' AND (requester_id = ? OR subject_user_id = ?) ORDER BY id FOR UPDATE",
		target.ID, target.ID).Rows()
	if err != nil {
		return plan, nil, err
	}
	key := reasonHashKey(a.Config.SessionSecret)
	type current struct {
		reasonRow
		decision reasonDecision
	}
	var set []current
	if err := scanReasonRows(rows, key, int64(actor.ID), int64(target.ID), func(r reasonRow) error {
		role := "subject"
		if r.RequesterID == int64(target.ID) {
			role = "requester"
		}
		d := reasonDecision{RequestID: r.ID, Role: role, Purpose: r.Purpose, Status: r.Status,
			ReviewerIsParty: int64(actor.ID) == r.RequesterID || int64(actor.ID) == r.SubjectUserID}
		// The text is not kept: only the id, the hash and the facts of the audit.
		set = append(set, current{reasonRow: reasonRow{ID: r.ID, Hash: r.Hash}, decision: d})
		return nil
	}); err != nil {
		return plan, nil, err
	}
	if len(set) == 0 {
		return plan, nil, nil
	}
	if !supplied {
		return plan, nil, errNeedsReasonReview
	}
	admins, err := adminCountIn(tx)
	if err != nil {
		return plan, nil, err
	}
	zone, err := contactZone(tx)
	if err != nil {
		return plan, nil, err
	}
	now := time.Now().UTC()
	targetPerson := *asPerson(target)
	if !policy.CanReviewErasureReasons(contactAccount(actor, now, zone), &targetPerson, admins) {
		return plan, nil, errReasonReviewForbidden
	}
	pred := func(u *models.User) bool {
		return policy.CanReviewErasureReasons(contactAccount(u, now, zone), &targetPerson, admins)
	}
	if len(set) != len(review) {
		return plan, nil, errReasonReviewChanged
	}
	for _, cur := range set {
		choice, ok := review[cur.ID]
		if !ok || !hmac.Equal([]byte(choice.hash), []byte(cur.Hash)) {
			return plan, nil, errReasonReviewChanged
		}
		d := cur.decision
		d.Remove = choice.remove
		plan.Decisions = append(plan.Decisions, d)
	}
	return plan, pred, nil
}

// eraseContactRequestReasonsIn writes the decisions and blanks the removed
// reasons. It is the only writer of contact_requests.reason (the guard in
// guards_test.go allows its one UPDATE literal here and its one call in
// apiAdminDeleteUser's callback, directly after eraseContactRequestTextIn):
// each decision is one signed audit row, written first, and the blanking runs
// in batches whose summed RowsAffected must equal the remove count, else the
// whole deletion rolls back (500). A kept reason changes no column.
func eraseContactRequestReasonsIn(tx *gorm.DB, s erasureSubject, plan reasonPlan) error {
	if len(plan.Decisions) == 0 {
		return nil
	}
	actor := plan.ActorID
	entries := make([]audit.SignedEntry, 0, len(plan.Decisions))
	var remove []int64
	for _, d := range plan.Decisions {
		var action, target string
		switch d.Purpose {
		case policy.RequestContact:
			action, target = audit.ContactReasonKept, "contact_request"
			if d.Remove {
				action = audit.ContactReasonRemoved
			}
		case policy.RequestGroupAdd, policy.RequestOccurrenceAdd:
			action, target = audit.AddReasonKept, "add_request"
			if d.Remove {
				action = audit.AddReasonRemoved
			}
		default:
			return fmt.Errorf("reason review: unknown request purpose %q", d.Purpose)
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
	var total int64
	for start := 0; start < len(remove); start += reasonRemoveBatch {
		batch := remove[start:min(start+reasonRemoveBatch, len(remove))]
		res := tx.Exec("UPDATE contact_requests SET reason = '' WHERE id = ANY(?::bigint[]) AND reason <> '' AND (requester_id = ? OR subject_user_id = ?)",
			int64Array(batch), s.ID, s.ID)
		if res.Error != nil {
			var pgErr *pgconn.PgError
			if errors.As(res.Error, &pgErr) && pgErr.Code == "55P03" {
				return fmt.Errorf("%w: %w", errReasonCounterpartBusy, res.Error)
			}
			return res.Error
		}
		total += res.RowsAffected
	}
	if total != int64(len(remove)) {
		return fmt.Errorf("%w: blanked %d of %d", errReasonInvariant, total, len(remove))
	}
	return nil
}
