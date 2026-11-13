package main

// The deleted-accounts review list (M7.4 T07B-3a, owner decision 7): the
// contact-request reasons that were left behind by accounts deleted before
// reasons were reviewed, or without a decision, found without reading any text.
//
// Discovery, the probe and a deleted account's own page are ungated reads,
// shaped like the live reason page (reasonPageAttempt) and sharing one attempt
// (legacyReadAttempt): an early plain actor read that refuses an ineligible
// admin before anything is read, one scanner unit, the readBeforeSeal seam, and
// the actor seal (currentAdminIn) as the last statement. The seal decides the
// same rule again on the locked actor row, an empty unit included: that is a
// deliberate difference from the live page, whose empty page passes no
// predicate, because here the early check is the only thing that keeps an
// ineligible admin from learning even that no reasons exist.
//
// The account page is the only one that reads text. It is a POST because it
// writes a view audit row (the same one the live page writes, source
// legacy_review), once per page that shows items and never for an empty one. It
// reads the account only after the actor is decided, so an admin who may not
// review cannot learn which ids are deleted accounts.
//
// Answers: 401 for a stale session, plain 403 for a non-admin, 403
// reason_review_not_allowed for an admin who may not review (a teen, a known
// 17-year-old, an inactive account), 400 for a malformed cursor, 404 for an id
// that is not a deleted account, and 503 reason_review_busy for anything that
// could not be read, which is never "no reasons".
//
// The save (PUT /admin/erasure-reasons/legacy/:userId, apiAdminLegacyReasonSave)
// is the one write: the admin's explicit keep or remove for 1 to 100 of an
// account's reasons, decided under the same locks and with the same writer
// (eraseContactRequestReasonsIn) as an account deletion's review. See its
// comment for the order of everything it does.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"strconv"
	"time"

	"fold/internal/audit"
	"fold/internal/jobs"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// legacyProbeRequests is how many requests the probe asks for: one is enough to
// say found, and it stops the unit at the first qualifying request.
const legacyProbeRequests = 1

// legacyReviewRule is policy.CanReviewLegacyErasureReasons on one transaction's
// clock and church zone: the early read decides the actor with it and the seal
// decides the locked actor row with it again. Discovery and the probe have no
// single account, so the account is deleted by definition.
type legacyReviewRule struct {
	now  time.Time
	zone *time.Location
}

func legacyReviewRuleIn(tx *gorm.DB) (legacyReviewRule, error) {
	zone, err := churchZoneIn(tx)
	if err != nil {
		return legacyReviewRule{}, err
	}
	return legacyReviewRule{now: time.Now().UTC(), zone: zone}, nil
}

func (r legacyReviewRule) allows(u *models.User) bool {
	return policy.CanReviewLegacyErasureReasons(contactAccount(u, r.now, r.zone), true)
}

// errLegacyAccountGone: the id is not a deleted account (no such user, or one
// that still has an account), found inside the read's transaction. Like the
// live routes' target lookup it answers 404 "user not found".
var errLegacyAccountGone = errors.New("the legacy review account is not a deleted account")

// parseLegacyUserID reads the :userId of an account's route as a canonical
// positive decimal that fits a signed 64-bit column: no sign, no leading zero,
// no whitespace. ok is false for anything else, which has no account to look
// up and is answered 404 before the database is touched.
func parseLegacyUserID(s string) (uint, bool) {
	v, err := strconv.ParseUint(s, 10, 63)
	if err != nil || v == 0 || strconv.FormatUint(v, 10) != s {
		return 0, false
	}
	return uint(v), true
}

// legacyReadStep is one attempt of a legacy read: everything that can differ
// between attempts is read inside it.
type legacyReadStep struct {
	unit legacyUnit
	// page is an account's own page, built inside the attempt with its view audit;
	// zero for discovery and the probe.
	page reasonPageJSON
	// predFailed: the legacy rule refused, on the early read or at the seal, as
	// opposed to the plain admin test (a stale or non-admin actor).
	predFailed bool
}

// legacyReadAttempt reads one legacy scanner unit as the signed-in admin and
// ends with the actor seal. party is 0 for discovery and the probe, or the
// deleted account whose own page this is. Order: the early plain read of the
// actor and the legacy rule (before the account or any unit is read, empty or
// not), the account's read (404), the unit, the account page's items and view
// audit, the readBeforeSeal seam, the seal. The seal is the last locking
// statement; on a busy actor row the caller reruns the whole attempt. Only an
// account page writes, and its one row is in the same transaction as the seal.
func (a *App) legacyReadAttempt(ctx context.Context, me *models.User, party uint, after *int64, pageRequests int, step *legacyReadStep) error {
	return a.reasonPageTx(ctx, func(tx *gorm.DB) error {
		*step = legacyReadStep{}
		actorRow, err := lockActorIn(tx, me, actorRead)
		if err != nil {
			return err
		}
		if !policy.IsAdmin(asPerson(actorRow)) {
			return errActorForbidden
		}
		rule, err := legacyReviewRuleIn(tx)
		if err != nil {
			return err
		}
		if !rule.allows(actorRow) {
			step.predFailed = true
			return errReasonReviewForbidden
		}
		var account *models.User
		if party != 0 {
			if account, err = deletedAccountIn(tx, party); err != nil {
				return err
			}
		}
		unit, err := scanLegacyUnit(tx, int64(party), after, pageRequests)
		if err != nil {
			return err
		}
		step.unit = unit
		if account != nil {
			if step.page, err = a.legacyPageIn(tx, actorRow, account, unit); err != nil {
				return err
			}
		}
		pred := func(u *models.User) bool {
			ok := rule.allows(u)
			step.predFailed = !ok
			return ok
		}
		if a.hooks.readBeforeSeal != nil {
			a.hooks.readBeforeSeal(tx)
		}
		// Last statement: nothing locks after it.
		return currentAdminIn(tx, me, pred)
	})
}

// deletedAccountIn reads one account unscoped and plain, with no lock: it is a
// read, and the save locks the row itself and decides again. A missing user and
// a live one are both errLegacyAccountGone.
func deletedAccountIn(tx *gorm.DB, id uint) (*models.User, error) {
	var u models.User
	if err := tx.Raw("SELECT id, deleted_at FROM users WHERE id = ?", id).Scan(&u).Error; err != nil {
		return nil, err
	}
	if u.ID == 0 || !u.DeletedAt.Valid {
		return nil, errLegacyAccountGone
	}
	return &u, nil
}

// legacyPageIn is a deleted account's page of one unit: the items the unit
// named, read for this admin and hashed for (admin, account), and one view
// audit row when it shows any. reviewRequired is set as the live page sets it:
// when the unit found requests, even if every reason was blanked since (that
// page is empty and its next still leads on). An empty unit is {items: [], next}
// with no audit.
func (a *App) legacyPageIn(tx *gorm.DB, actor, account *models.User, unit legacyUnit) (reasonPageJSON, error) {
	page := reasonPageJSON{Items: []reasonItemJSON{}}
	if unit.Next != nil {
		s := strconv.FormatInt(*unit.Next, 10)
		page.Next = &s
	}
	if len(unit.Items) == 0 {
		return page, nil
	}
	// One item a request: the scan was asked for this account's side only.
	ids := make([]int64, len(unit.Items))
	for i, it := range unit.Items {
		ids[i] = it.Request
	}
	items, err := a.reasonPageItems(tx, actor, account, ids)
	if err != nil {
		return page, err
	}
	ref := strconv.FormatUint(uint64(account.ID), 10)
	for i := range items {
		items[i].PartyRef = ref
	}
	page.ReviewRequired = true
	page.Items = items
	if len(items) > 0 {
		if err := audit.Write(tx, audit.Entry{Actor: &actor.ID, Action: audit.UserErasureReasonsViewed,
			TargetType: "user", TargetID: account.ID, Meta: map[string]any{"count": len(items), "source": reasonSourceLegacy}}); err != nil {
			return page, err
		}
	}
	return page, nil
}

// runLegacyRead is the busy-retry loop and the error answers every legacy read
// shares. It answers the request itself on failure and reports whether the
// caller may answer with the step.
func (a *App) runLegacyRead(c *gin.Context, party uint, after *int64, pageRequests int) (legacyReadStep, bool) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), reasonPageBudget)
	defer cancel()
	retry := newBusyRetry(ctx)
	var step legacyReadStep
	var err error
	for {
		err = a.legacyReadAttempt(ctx, apiUser(c), party, after, pageRequests, &step)
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
		return step, true
	case errors.Is(err, errSecuritySession):
		fail(c, errSecuritySession)
	case errors.Is(err, errLegacyAccountGone):
		jsonError(c, http.StatusNotFound, "user not found")
	case errors.Is(err, errReasonReviewForbidden) || (errors.Is(err, errActorForbidden) && step.predFailed):
		fail(c, errReasonReviewForbidden)
	case errors.Is(err, errActorForbidden):
		fail(c, err)
	default:
		// Busy after every try, a timeout or a database error: not "none".
		log.Printf("legacy reason review read failed (busy: %v): %s", errors.Is(err, errActorBusy), jobs.CleanError(err))
		fail(c, errReasonReviewBusy)
	}
	return legacyReadStep{}, false
}

// legacyCursor reads the optional after cursor as a canonical signed decimal.
// ok is false after answering 400.
func legacyCursor(c *gin.Context) (after *int64, ok bool) {
	raw, present := c.GetQuery("after")
	if !present {
		return nil, true
	}
	v, valid := parseRequestID(raw)
	if !valid {
		jsonError(c, http.StatusBadRequest, "invalid cursor")
		return nil, false
	}
	return &v, true
}

// apiAdminLegacyReasons lists one unit of the undecided reasons of deleted
// accounts (GET /api/v1/admin/erasure-reasons/legacy?after=), grouped by
// account. No text, no audit row: a request id and an account reference only.
func (a *App) apiAdminLegacyReasons(c *gin.Context) {
	after, ok := legacyCursor(c)
	if !ok {
		return
	}
	step, ok := a.runLegacyRead(c, 0, after, reasonPageRequests)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, legacyDiscoveryPage(step.unit))
}

// legacyDiscoveryPage groups a unit's pairs by account in first-seen order.
func legacyDiscoveryPage(unit legacyUnit) legacyReasonDiscoveryJSON {
	out := legacyReasonDiscoveryJSON{Accounts: []legacyDiscoveryAccountJSON{}}
	at := map[int64]int{}
	for _, it := range unit.Items {
		i, seen := at[it.Party]
		if !seen {
			i = len(out.Accounts)
			at[it.Party] = i
			out.Accounts = append(out.Accounts, legacyDiscoveryAccountJSON{
				UserID: strconv.FormatInt(it.Party, 10), Name: deletedAccountName, Items: []legacyDiscoveryItemJSON{},
			})
		}
		out.Accounts[i].Items = append(out.Accounts[i].Items, legacyDiscoveryItemJSON{RequestID: strconv.FormatInt(it.Request, 10)})
	}
	if unit.Next != nil {
		s := strconv.FormatInt(*unit.Next, 10)
		out.Next = &s
	}
	return out
}

// apiAdminLegacyReasonsProbe asks whether the first unit holds any undecided
// reason (GET /api/v1/admin/erasure-reasons/legacy/probe). It decides who may
// ask exactly as the list does and reads from the start of the table.
func (a *App) apiAdminLegacyReasonsProbe(c *gin.Context) {
	step, ok := a.runLegacyRead(c, 0, nil, legacyProbeRequests)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, legacyReasonProbeJSON{Found: len(step.unit.Items) > 0})
}

// apiAdminLegacyReasonPage serves one audited page of a deleted account's
// undecided reasons (POST /api/v1/admin/erasure-reasons/legacy/:userId/page).
// At most 100 items, one for each request, because only this account's side of
// a request is asked for. The items are the live page's, read for the
// signed-in admin and hashed for (admin, account); the save that decides them
// recomputes every hash. A timeout or failure answers 503 reason_review_busy
// and is never "no reasons": the client retries the same cursor.
func (a *App) apiAdminLegacyReasonPage(c *gin.Context) {
	account, ok := parseLegacyUserID(c.Param("userId"))
	if !ok {
		jsonError(c, http.StatusNotFound, "user not found")
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
	step, ok := a.runLegacyRead(c, account, after, reasonPageRequests)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, step.page)
}

// legacySaveBudget is the save's own deadline, made from context.Background()
// so a disconnecting browser cannot abort a commit. It equals the account
// deletion's (adminDeleteBudget, 40 seconds: the 60 second write timeout minus
// 20) and holds the busy window (busyRetryWindow, 2 s) and up to 100 locked
// decisions. A var so tests can shorten it (a test that sets it stays serial).
var legacySaveBudget = adminDeleteBudget

// legacySaveMaxDecisions is the most decisions one save carries, the size of a
// page.
const legacySaveMaxDecisions = 100

const (
	legacyChoiceKeep   = "keep"
	legacyChoiceRemove = "remove"
)

var (
	// errLegacySaveTimedOut: the save ran out of time (or too little was left to
	// commit) and was rolled back or never begun.
	errLegacySaveTimedOut = apiErr(http.StatusServiceUnavailable, "legacy_save_timed_out", "saving took too long, so nothing was saved")
	// errLegacySaveOutcomeUnknown: COMMIT may have reached the server, so the
	// answer is never "nothing was saved"; the client settles the attempt.
	errLegacySaveOutcomeUnknown = apiErr(http.StatusInternalServerError, "legacy_save_outcome_unknown", "could not confirm whether the choices were saved")
	// errLegacySaveBusy: blanking a reason needed the other deleted person's users
	// row (migration 00007) and another transaction held it on every attempt.
	// COMMIT was never sent. It is never delete_reason_busy, which says an account
	// deletion was refused.
	errLegacySaveBusy = apiErr(http.StatusConflict, "legacy_save_busy", "someone was changing a request that involves this account, so nothing was saved")
)

// legacySaveBody is the PUT's body. attemptId is required: the save has no
// old-client mode.
type legacySaveBody struct {
	AttemptID string                   `json:"attemptId"`
	Decisions []legacySaveDecisionBody `json:"decisions"`
}

type legacySaveDecisionBody struct {
	RequestID  string `json:"requestId"`
	ReviewHash string `json:"reviewHash"`
	Choice     string `json:"choice"`
}

// readLegacySaveBody reads the body once and decodes it the way gin's binder
// does, so the strict key scan sees the same object. ok is false for a body
// that does not decode, one with no attempt id, and one that names attemptId or
// decisions twice or in another case: none leaves a single id to claim or a
// single list to apply, so each is refused before the claim, recording nothing.
func readLegacySaveBody(c *gin.Context) (body legacySaveBody, ok bool) {
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return body, false
	}
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&body); err != nil {
		return legacySaveBody{}, false
	}
	keys := scanTopKeys(raw)
	if keys.attemptAmbiguous || keys.decisionsAmbiguous || body.AttemptID == "" {
		return legacySaveBody{}, false
	}
	return body, true
}

// legacyDecision is one parsed decision: the request, the review hash the admin
// was served as raw bytes, and the explicit choice.
type legacyDecision struct {
	request int64
	hash    [sha256.Size]byte
	remove  bool
}

// parseLegacyDecisions checks the list's shape: 1 to 100 entries, each a
// canonical signed request id (parseRequestID), a review hash and exactly keep
// or remove, with no request twice. Nothing is defaulted: a missing or unknown
// choice refuses the whole list. ok is false for any other shape.
func parseLegacyDecisions(in []legacySaveDecisionBody) (out []legacyDecision, ok bool) {
	if len(in) < 1 || len(in) > legacySaveMaxDecisions {
		return nil, false
	}
	seen := make(map[int64]bool, len(in))
	out = make([]legacyDecision, 0, len(in))
	for _, d := range in {
		id, valid := parseRequestID(d.RequestID)
		if !valid || seen[id] || !validReviewHash(d.ReviewHash) {
			return nil, false
		}
		seen[id] = true
		dec := legacyDecision{request: id}
		if _, err := hex.Decode(dec.hash[:], []byte(d.ReviewHash)); err != nil {
			return nil, false
		}
		switch d.Choice {
		case legacyChoiceKeep:
		case legacyChoiceRemove:
			dec.remove = true
		default:
			return nil, false
		}
		out = append(out, dec)
	}
	slices.SortFunc(out, func(a, b legacyDecision) int {
		switch {
		case a.request < b.request:
			return -1
		case a.request > b.request:
			return 1
		}
		return 0
	})
	return out, true
}

// lockDeletedAccountIn locks one account's users row FOR UPDATE, read raw and
// unscoped so a soft-deleted row is seen. A missing user and a live one are
// both errLegacyAccountGone. It is the first lock of the save after the ticket,
// the receipt and the plain actor read, and nothing locks a users row after it
// but the trigger's NOWAIT share lock and the seal's.
func lockDeletedAccountIn(tx *gorm.DB, id uint) error {
	var u models.User
	if err := tx.Raw("SELECT id, deleted_at FROM users WHERE id = ? FOR UPDATE", id).Scan(&u).Error; err != nil {
		return err
	}
	if u.ID == 0 || !u.DeletedAt.Valid {
		return errLegacyAccountGone
	}
	return nil
}

// legacySavePlanIn is everything the save decides before it writes, inside its
// fenced transaction and after its receipt: the actor (a plain read of the
// session's row and the legacy rule, before the account or any request is
// read), the account locked FOR UPDATE, then the named requests locked FOR
// UPDATE in ascending id and compared with what the admin was served. The set
// is exact: every named request must still be one of this account's, still
// have a reason, still have no decision marker for (request, account) by the one
// marker selector (legacyMarkersIn) and hash, for this admin and this account,
// to what was sent. Any miss is errReasonReviewChanged and nothing is written.
// The rule it returns is the seal's.
func (a *App) legacySavePlanIn(tx *gorm.DB, me *models.User, account uint, key []byte, decisions []legacyDecision, ip string) (reasonPlan, legacyReviewRule, error) {
	plan := reasonPlan{IP: ip, Source: reasonSourceLegacy}
	actor, err := lockActorIn(tx, me, actorRead)
	if err != nil {
		return plan, legacyReviewRule{}, err
	}
	if !policy.IsAdmin(asPerson(actor)) {
		return plan, legacyReviewRule{}, errActorForbidden
	}
	rule, err := legacyReviewRuleIn(tx)
	if err != nil {
		return plan, rule, err
	}
	if !rule.allows(actor) {
		return plan, rule, errReasonReviewForbidden
	}
	plan.ActorID = actor.ID
	if err := lockDeletedAccountIn(tx, account); err != nil {
		return plan, rule, err
	}
	ids := make([]int64, len(decisions))
	choice := make(map[int64]legacyDecision, len(decisions))
	for i, d := range decisions {
		ids[i] = d.request
		choice[d.request] = d
	}
	rows, err := tx.Raw("SELECT "+reasonSelect+" FROM contact_requests WHERE id = ANY(?::bigint[]) AND (requester_id = ? OR subject_user_id = ?) ORDER BY contact_requests.id FOR UPDATE",
		pgBigintArray(ids), account, account).Rows()
	if err != nil {
		return plan, rule, err
	}
	var read []reasonRow
	if err := scanReasonRowsWith(rows, key, int64(actor.ID), int64(account), false, nil, func(r reasonRow) error {
		read = append(read, r)
		return nil
	}); err != nil {
		return plan, rule, err
	}
	if len(read) != len(decisions) {
		return plan, rule, errReasonReviewChanged
	}
	cands := make([]legacyCandidate, len(read))
	for i, r := range read {
		d, named := choice[r.ID]
		if !named || r.Reason == "" || !hmac.Equal(r.Raw[:], d.hash[:]) {
			return plan, rule, errReasonReviewChanged
		}
		kind := requestKindOf(r.Purpose)
		if kind == nil {
			return plan, rule, fmt.Errorf("legacy review: unknown request purpose %q", r.Purpose)
		}
		cands[i] = legacyCandidate{legacyPair{r.ID, int64(account)}, kind}
	}
	markers, err := legacyMarkersIn(tx, cands)
	if err != nil {
		return plan, rule, err
	}
	plan.Decisions = make([]reasonDecision, len(read))
	for i, r := range read {
		state := policy.ErasureReasonItemState(policy.ErasureReasonFacts{PartyDeleted: true, Marker: markers[cands[i].legacyPair], ReasonBlank: r.Reason == ""})
		if state != policy.ReasonItemUnreviewedTombstone {
			return plan, rule, errReasonReviewChanged
		}
		role, reviewerIsParty := reasonParties(r, int64(actor.ID), int64(account))
		plan.Decisions[i] = reasonDecision{RequestID: r.ID, Remove: choice[r.ID].remove, Role: role,
			Purpose: r.Purpose, Status: r.Status, ReviewerIsParty: reviewerIsParty}
	}
	return plan, rule, nil
}

// apiAdminLegacyReasonSave decides 1 to 100 of a deleted account's undecided
// reasons, each an explicit keep or remove
// (PUT /api/v1/admin/erasure-reasons/legacy/:userId). A removed reason is blanked
// and every decision writes the signed audit row that makes the pair leave the
// list, in one transaction; there is no default, no bulk and no cutoff.
//
// Order: the :userId (404 recording nothing for anything but a canonical
// positive id), the body (400 recording nothing, attemptId required), the
// submission check (the id is then claimed, and every refusal below ends it
// rolled back), restore detection, the list's shape (400), then the fenced
// transaction under its own legacySaveBudget: the restore ticket, the receipt,
// the plan (legacySavePlanIn), the one reason writer and, after
// reconcileContacts, the actor seal as the last locking statement. A busy actor
// row or a busy other deleted person runs the whole save again, only after the
// ROLLBACK was acknowledged (rollbackConfirmed), under the shared busy-retry
// bounds. The id's final state is recorded before any response is sent.
// Classification is classifyContactErr's, commit phase first.
func (a *App) apiAdminLegacyReasonSave(c *gin.Context) {
	me := apiUser(c)
	account, ok := parseLegacyUserID(c.Param("userId"))
	if !ok {
		jsonError(c, http.StatusNotFound, "user not found")
		return
	}
	body, ok := readLegacySaveBody(c)
	if !ok {
		jsonError(c, http.StatusBadRequest, "invalid body")
		return
	}
	claim, r := a.submitAttempt(body.AttemptID, attemptKindLegacyReasonSave, me.ID, account)
	if claim == nil {
		answerSubmit(c, r)
		return
	}
	defer claim.guard()
	if a.restoreDetected() {
		claim.end(attemptRolledBack)
		answerRestoreDetected(c)
		return
	}
	decisions, ok := parseLegacyDecisions(body.Decisions)
	if !ok {
		claim.end(attemptRolledBack)
		jsonError(c, http.StatusBadRequest, "invalid decisions")
		return
	}
	key, err := reasonHashKey(a.Config.SessionSecret)
	if err != nil {
		claim.end(attemptRolledBack)
		log.Printf("legacy reason save could not derive its key: %s", jobs.CleanError(err))
		jsonError(c, http.StatusInternalServerError, "could not save")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), legacySaveBudget)
	defer cancel()
	retry := newBusyRetry(ctx)
	// Reset at the start of every attempt: a rolled-back attempt's values are
	// never used. predFailed: the seal's rule (not the plain admin test) refused.
	var plan reasonPlan
	var predFailed bool
	for {
		plan, predFailed = reasonPlan{}, false
		err = a.fencedContactMutation(ctx, contactChange{UserID: account}, func(tx *gorm.DB) (*contactSeal, error) {
			if err := insertAttemptReceipt(tx, claim.nonce); err != nil {
				return nil, err
			}
			p, rule, err := a.legacySavePlanIn(tx, me, account, key, decisions, c.ClientIP())
			if err != nil {
				return nil, err
			}
			plan = p
			if err := eraseContactRequestReasonsIn(tx, erasureSubject{ID: account}, plan); err != nil {
				return nil, err
			}
			return a.legacySaveSeal(me, func(u *models.User) bool {
				ok := rule.allows(u)
				predFailed = !ok
				return ok
			}), nil
		})
		if !(errors.Is(err, errActorBusy) || errors.Is(err, errReasonCounterpartBusy)) || !rollbackConfirmed(err) {
			break
		}
		again, cerr := retry.again()
		if cerr != nil {
			err = errors.Join(cerr, err)
		}
		if !again {
			break
		}
		if a.hooks.legacySaveBeforeRetry != nil {
			a.hooks.legacySaveBeforeRetry()
		}
	}
	// The id's final state is recorded before any response is sent: applied only
	// from an acknowledged COMMIT, unknown from a commit that may have happened,
	// rolled back otherwise (every refusal after the claim).
	claim.end(outcomeAttemptState(err))
	switch {
	case err == nil:
		kept, removed := plan.counts()
		c.JSON(http.StatusOK, legacyReasonSaveJSON{Kept: kept, Removed: removed})
	case classifyContactErr(err) == outcomeUnknown:
		// COMMIT may have reached the server: never say nothing was saved.
		log.Printf("could not confirm legacy reason save: %s", jobs.CleanError(err))
		fail(c, errLegacySaveOutcomeUnknown)
	case errors.Is(err, errSecuritySession):
		fail(c, errSecuritySession)
	case errors.Is(err, errReasonReviewChanged):
		fail(c, errReasonReviewChanged)
	case errors.Is(err, errReasonReviewForbidden) || (errors.Is(err, errActorForbidden) && predFailed):
		fail(c, errReasonReviewForbidden)
	case errors.Is(err, errActorForbidden):
		jsonError(c, http.StatusForbidden, "forbidden")
	case errors.Is(err, errLegacyAccountGone):
		jsonError(c, http.StatusNotFound, "user not found")
	case errors.Is(err, errRestoreDetected):
		log.Printf("legacy reason save refused: the database was restored while Fold was running")
		answerRestoreDetected(c)
	case classifyContactErr(err) == outcomeTimedOut:
		log.Printf("legacy reason save timed out (rollback confirmed: %v): %s", rollbackConfirmed(err), jobs.CleanError(err))
		fail(c, errLegacySaveTimedOut)
	case errors.Is(err, errActorBusy):
		// The admin's own row was held at the seal on every attempt (or the
		// rollback after one was not acknowledged): COMMIT was never sent.
		log.Printf("legacy reason save found the admin's own account busy (rollback confirmed: %v): %s", rollbackConfirmed(err), jobs.CleanError(err))
		fail(c, errActorBusy)
	case errors.Is(err, errReasonCounterpartBusy):
		log.Printf("legacy reason save found the other deleted person busy (rollback confirmed: %v): %s", rollbackConfirmed(err), jobs.CleanError(err))
		fail(c, errLegacySaveBusy)
	default:
		log.Printf("could not save legacy reasons (rollback confirmed: %v): %s", rollbackConfirmed(err), jobs.CleanError(err))
		jsonError(c, http.StatusInternalServerError, "could not save")
	}
}
