package main

// The deleted-accounts review list (M7.4 T07B-3a, owner decision 7): the
// contact-request reasons that were left behind by accounts deleted before
// reasons were reviewed, or without a decision, found without reading any text.
//
// Discovery and the probe are ungated reads, shaped like the live reason page
// (reasonPageAttempt): an early plain actor read that refuses an ineligible
// admin before anything is read, one scanner unit, the readBeforeSeal seam, and
// the actor seal (currentAdminIn) as the last statement. The seal decides the
// same rule again on the locked actor row, an empty unit included: that is a
// deliberate difference from the live page, whose empty page passes no
// predicate, because here the early check is the only thing that keeps an
// ineligible admin from learning even that no reasons exist.
//
// Answers: 401 for a stale session, plain 403 for a non-admin, 403
// reason_review_not_allowed for an admin who may not review (a teen, a known
// 17-year-old, an inactive account), 400 for a malformed cursor, and 503
// reason_review_busy for anything that could not be read, which is never "no
// reasons".

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

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

// legacyReadStep is one attempt of a legacy read: everything that can differ
// between attempts is read inside it.
type legacyReadStep struct {
	unit legacyUnit
	// predFailed: the legacy rule refused, on the early read or at the seal, as
	// opposed to the plain admin test (a stale or non-admin actor).
	predFailed bool
}

// legacyReadAttempt reads one legacy scanner unit as the signed-in admin and
// ends with the actor seal. Order: the early plain read of the actor and the
// legacy rule (before any unit is read, empty or not), the unit, the
// readBeforeSeal seam, the seal. The seal is the last locking statement; on a
// busy actor row the caller reruns the whole attempt. It writes nothing.
func (a *App) legacyReadAttempt(ctx context.Context, me *models.User, after *int64, pageRequests int, step *legacyReadStep) error {
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
		unit, err := scanLegacyUnit(tx, 0, after, pageRequests)
		if err != nil {
			return err
		}
		step.unit = unit
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

// runLegacyRead is the busy-retry loop and the error answers every legacy read
// shares. It answers the request itself on failure and reports whether the
// caller may answer with the unit.
func (a *App) runLegacyRead(c *gin.Context, after *int64, pageRequests int) (legacyUnit, bool) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), reasonPageBudget)
	defer cancel()
	retry := newBusyRetry(ctx)
	var step legacyReadStep
	var err error
	for {
		err = a.legacyReadAttempt(ctx, apiUser(c), after, pageRequests, &step)
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
		return step.unit, true
	case errors.Is(err, errSecuritySession):
		fail(c, errSecuritySession)
	case errors.Is(err, errReasonReviewForbidden) || (errors.Is(err, errActorForbidden) && step.predFailed):
		fail(c, errReasonReviewForbidden)
	case errors.Is(err, errActorForbidden):
		fail(c, err)
	default:
		// Busy after every try, a timeout or a database error: not "none".
		log.Printf("legacy reason review read failed (busy: %v): %s", errors.Is(err, errActorBusy), jobs.CleanError(err))
		fail(c, errReasonReviewBusy)
	}
	return legacyUnit{}, false
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
	unit, ok := a.runLegacyRead(c, after, reasonPageRequests)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, legacyDiscoveryPage(unit))
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
	unit, ok := a.runLegacyRead(c, nil, legacyProbeRequests)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, legacyReasonProbeJSON{Found: len(unit.Items) > 0})
}
