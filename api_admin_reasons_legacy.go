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

import (
	"context"
	"errors"
	"log"
	"net/http"
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
