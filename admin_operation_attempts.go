package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"fold/internal/jobs"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Admin operation attempts (M7.4 T07B-1b, T07B-D4-CONTRACT "attempt lifecycle").
//
// A browser that loses the reply to an account deletion cannot tell whether the
// account is gone. The panel mints an attempt id before each press and sends
// it with the DELETE; if the reply is lost it asks to settle the same id.
//
// The id is a capability bound to one process: 16 random bytes, the minting
// time (milliseconds since this process started), and a truncated HMAC over
// both, the kind, the actor and the subject under a 32-byte key made at
// start-up and kept only in memory. A restart or another process cannot
// recognise it, so no outcome is promised after a restart. The process keeps a
// ledger of ids (a mutex-guarded map, forward-only states) and the database
// keeps a receipt row inserted in the deletion's own transaction, so a
// committed deletion always has one for 24 hours.
//
// Windows: an id first submitted more than 10 minutes after it was minted is
// refused (and fenced, so it can never run); it can be settled for 60 minutes;
// ledger entries are pruned after 61.
//
// Guarantee, narrowed on purpose: an id applies at most once, only in the
// process that minted it, only if first submitted within 10 minutes. The id is
// never put in a URL path, a log line, a job payload or an error body.

const (
	attemptKindAccountDelete = "account_delete"

	attemptSubmitWindow = 10 * time.Minute
	attemptSettleWindow = 60 * time.Minute
	attemptPruneAfter   = 61 * time.Minute
	attemptLedgerCap    = 100000
	attemptReceiptKeep  = 24 * time.Hour

	// attemptSettleBudget is the whole of one settle request; the busy retry
	// inside it is the shared R5-C01 policy (busy_retry.go).
	attemptSettleBudget = 10 * time.Second

	attemptVersionTag = "fold/admin-attempt/v1"
)

// attemptSweepBatch, attemptSweepBatchBudget and attemptSweepBudget bound the
// receipt sweep; vars so tests can shorten them (a test that sets one stays
// serial).
var (
	attemptSweepBatch       = 5000
	attemptSweepBatchBudget = 10 * time.Second
	attemptSweepBudget      = 2 * time.Minute
)

const kindAttemptSweep = "admin_attempts.sweep"

var (
	// errActorBusy: the signed-in admin's own row is held by another
	// transaction (SQLSTATE 55P03 under the seal's NOWAIT). It wraps the
	// *pgconn.PgError, so errors.As still finds the code.
	errActorBusy = apiErr(http.StatusConflict, "admin_account_busy", "your own account was being updated, so nothing was changed")
	// errActorForbidden: the locked actor row is not an admin (or not allowed
	// by the caller's predicate): 403.
	errActorForbidden = apiErr(http.StatusForbidden, "", "forbidden")
	// The refusals a submission with an attempt id can get (answerSubmit).
	// attempt_expired means only that this submission ran nothing.
	errAttemptClosed  = apiErr(http.StatusConflict, "attempt_closed", "this attempt was already used or settled")
	errAttemptBusy    = apiErr(http.StatusServiceUnavailable, "attempt_busy", "too many attempts are being tracked")
	errAttemptExpired = apiErr(http.StatusConflict, "attempt_expired", "this attempt is too old to run")
	// errSettleRunning: the attempt being settled is still running; settle
	// rolls back and waits outside every lock.
	errSettleRunning = errors.New("the attempt is still running")
	// errLedgerFull: the ledger is at its cap and cannot record a new entry.
	errLedgerFull = errors.New("the attempt ledger is full")
	// errAttemptReceiptInvariant: the receipt insert hit an existing nonce,
	// which the ledger's claim-once rule makes impossible.
	errAttemptReceiptInvariant = errors.New("the attempt receipt already exists")
)

type attemptState uint8

const (
	attemptAbsent attemptState = iota
	attemptRunning
	attemptApplied
	attemptRolledBack
	attemptUnknown
	attemptFenced
)

type ledgerEntry struct {
	state attemptState
	mint  time.Duration // elapsed since the process started, when minted
	done  chan struct{} // closed when a running entry ends; nil for a fenced one
}

// attemptLedger is the process's memory of attempt ids. Its mutex is held only
// inside its own methods, which make no database call, channel operation,
// select, sleep or context wait (TestLedgerMutexHoldsNoWait parses this file):
// a method that ends an entry hands the done channel back for the caller to
// close.
type attemptLedger struct {
	mu        sync.Mutex
	m         map[[16]byte]*ledgerEntry
	lastPrune time.Duration
}

// attemptState is the App's side of the lifecycle. The zero value is ready;
// attemptsInit makes the key and the start time on first use, or at start-up
// from serve, where a failure exits non-zero.
type attemptsState struct {
	once   sync.Once
	err    error
	key    [32]byte
	start  time.Time
	ledger attemptLedger
}

func (s *attemptsState) init() error {
	s.once.Do(func() {
		if _, err := rand.Read(s.key[:]); err != nil {
			s.err = err
			return
		}
		s.start = time.Now()
	})
	return s.err
}

// initAttempts makes the per-process key. serve calls it before serving.
func (a *App) initAttempts() error { return a.attempts.init() }

// attemptElapsed is the monotonic time since the process started, read from
// the App clock so tests (and the e2e build) can move it.
func (a *App) attemptElapsed() time.Duration {
	if err := a.attempts.init(); err != nil {
		panic(err)
	}
	return a.now().Sub(a.attempts.start)
}

// --- the id ----------------------------------------------------------------

func (a *App) attemptMAC(kind string, actor, subject uint, mintMs uint64, nonce []byte) []byte {
	if err := a.attempts.init(); err != nil {
		panic(err)
	}
	m := hmac.New(sha256.New, a.attempts.key[:])
	m.Write([]byte(attemptVersionTag))
	m.Write([]byte{0})
	m.Write([]byte(kind))
	m.Write([]byte{0})
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(actor))
	m.Write(n[:])
	binary.BigEndian.PutUint64(n[:], uint64(subject))
	m.Write(n[:])
	binary.BigEndian.PutUint64(n[:], mintMs)
	m.Write(n[:])
	m.Write(nonce)
	return m.Sum(nil)[:16]
}

// mintAttempt returns a new 80-hex-character id for kind, the admin and the
// subject. It touches no database.
func (a *App) mintAttempt(kind string, actor, subject uint) (string, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	mint := a.attemptElapsed().Milliseconds()
	if mint < 0 {
		mint = 0
	}
	buf := make([]byte, 0, 40)
	buf = append(buf, nonce...)
	buf = binary.BigEndian.AppendUint64(buf, uint64(mint))
	buf = append(buf, a.attemptMAC(kind, actor, subject, uint64(mint), nonce)...)
	return hex.EncodeToString(buf), nil
}

// openAttempt verifies id for the tuple. ok is false for a malformed id, a
// wrong kind, actor or subject, and an id another process minted.
func (a *App) openAttempt(id, kind string, actor, subject uint) (nonce [16]byte, mint time.Duration, ok bool) {
	if len(id) != 80 {
		return nonce, 0, false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return nonce, 0, false
		}
	}
	raw, err := hex.DecodeString(id)
	if err != nil || len(raw) != 40 {
		return nonce, 0, false
	}
	mintMs := binary.BigEndian.Uint64(raw[16:24])
	want := a.attemptMAC(kind, actor, subject, mintMs, raw[:16])
	if !hmac.Equal(want, raw[24:40]) {
		return nonce, 0, false
	}
	copy(nonce[:], raw[:16])
	// A forged-in-range value cannot overflow Duration: the mac vouches for it.
	return nonce, time.Duration(mintMs) * time.Millisecond, true
}

// --- the ledger ------------------------------------------------------------

type submitResult int

const (
	submitOK submitResult = iota
	submitExpired
	submitClosed
	submitBusy
)

// prune drops entries past their last use. Callers hold l.mu.
func (l *attemptLedger) pruneLocked(now time.Duration) {
	if now-l.lastPrune < time.Minute {
		return
	}
	l.lastPrune = now
	for k, e := range l.m {
		if now-e.mint > attemptPruneAfter {
			delete(l.m, k)
		}
	}
}

// claim is the submission rule after the mac: any entry is closed, whatever
// its age; an absent id older than the submit window is expired (and fenced
// while it can still be settled); else it starts running. At the cap an
// expired id records nothing and a fresh one is busy.
func (l *attemptLedger) claim(n [16]byte, mint, now time.Duration) submitResult {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.m == nil {
		l.m = map[[16]byte]*ledgerEntry{}
	}
	l.pruneLocked(now)
	if _, ok := l.m[n]; ok {
		return submitClosed
	}
	if now-mint > attemptSubmitWindow {
		if now-mint <= attemptSettleWindow && len(l.m) < attemptLedgerCap {
			l.m[n] = &ledgerEntry{state: attemptFenced, mint: mint}
		}
		return submitExpired
	}
	if len(l.m) >= attemptLedgerCap {
		return submitBusy
	}
	l.m[n] = &ledgerEntry{state: attemptRunning, mint: mint, done: make(chan struct{})}
	return submitOK
}

// look reads an entry without changing it.
func (l *attemptLedger) look(n [16]byte) (attemptState, chan struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.m[n]
	if !ok {
		return attemptAbsent, nil
	}
	return e.state, e.done
}

// end moves a running entry to its final state, once. It returns the done
// channel for the caller to close (never closed here, under the mutex).
func (l *attemptLedger) end(n [16]byte, s attemptState) (chan struct{}, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.m[n]
	if !ok || e.state != attemptRunning {
		return nil, false
	}
	e.state = s
	return e.done, true
}

// settleTransition is settle's ledger step, taken inside its transaction: an
// absent id becomes fenced (the one irreversible step), a running one is
// errSettleRunning (settle waits outside every lock), an ended or fenced one is
// read only. At the cap an absent id cannot be recorded (errLedgerFull).
func (l *attemptLedger) settleTransition(n [16]byte, mint, now time.Duration) (attemptState, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.m == nil {
		l.m = map[[16]byte]*ledgerEntry{}
	}
	l.pruneLocked(now)
	e, ok := l.m[n]
	if !ok {
		if len(l.m) >= attemptLedgerCap {
			return attemptAbsent, errLedgerFull
		}
		l.m[n] = &ledgerEntry{state: attemptFenced, mint: mint}
		return attemptFenced, nil
	}
	if e.state == attemptRunning {
		return attemptRunning, errSettleRunning
	}
	return e.state, nil
}

// attemptClaim is a handler's hold on a running entry. A nil claim (a request
// without an attempt id) ignores every call.
type attemptClaim struct {
	a     *App
	nonce [16]byte
}

// end records the final state once and wakes settlers waiting on it.
func (c *attemptClaim) end(s attemptState) {
	if c == nil {
		return
	}
	if done, ok := c.a.attempts.ledger.end(c.nonce, s); ok && done != nil {
		close(done)
	}
}

// guard is deferred right after the claim: an exit that never recorded an
// outcome (a panic) is unknown, so a later settle never says "not applied".
func (c *attemptClaim) guard() { c.end(attemptUnknown) }

// submitAttempt is the submission check. It runs as the first step after the
// request body is decoded and before any transaction. On submitOK the claim is
// running; otherwise it is nil and the handler answers by result.
func (a *App) submitAttempt(id, kind string, actor, subject uint) (*attemptClaim, submitResult) {
	nonce, mint, ok := a.openAttempt(id, kind, actor, subject)
	if !ok {
		return nil, submitExpired
	}
	switch r := a.attempts.ledger.claim(nonce, mint, a.attemptElapsed()); r {
	case submitOK:
		return &attemptClaim{a: a, nonce: nonce}, submitOK
	default:
		return nil, r
	}
}

// answerSubmit sends the refusal for a submission result other than submitOK.
func answerSubmit(c *gin.Context, r submitResult) {
	switch r {
	case submitClosed:
		fail(c, errAttemptClosed)
	case submitBusy:
		fail(c, errAttemptBusy)
	default:
		fail(c, errAttemptExpired)
	}
}

// outcomeAttemptState maps how a fenced call ended to the ledger state: only
// an acknowledged COMMIT is applied; an unknown one stays unknown; everything
// else ended before COMMIT was sent.
func outcomeAttemptState(err error) attemptState {
	switch {
	case err == nil:
		return attemptApplied
	case classifyContactErr(err) == outcomeUnknown:
		return attemptUnknown
	default:
		return attemptRolledBack
	}
}

// insertAttemptReceipt is the receipt, the first write after the ticket. It
// commits only with the operation.
func insertAttemptReceipt(tx *gorm.DB, nonce [16]byte) error {
	// The nonce travels as hex and is decoded by the server: GORM would expand a
	// []byte argument into a list of values.
	if err := tx.Exec("INSERT INTO admin_operation_attempts (attempt_id) VALUES (decode(?, 'hex'))", hex.EncodeToString(nonce[:])).Error; err != nil {
		if pgCode(err) == pgUniqueViolation {
			return fmt.Errorf("%w: %w", errAttemptReceiptInvariant, err)
		}
		return err
	}
	return nil
}

// --- the actor seal ---------------------------------------------------------

// currentAdminIn decides, on the actor's own row, that the session that sent
// the request is still current and may act. It is one statement, taken FOR
// SHARE NOWAIT so it never waits on a row lock, and it is the last locking
// statement of its transaction (R5-C01): from here to COMMIT the transaction
// runs only plain reads, in-memory work and COMMIT, so the lock adds no edge to
// any wait-for cycle. It reads nothing from the early-read row.
//
// 401 (errSecuritySession) when no row comes back, the row is soft deleted, the
// session is not current (sessionCurrent, the predicate the middleware and every
// other recheck use: sign-in ability and both versions); 403 (errActorForbidden)
// unless the row is an admin and pred (when given) holds; errActorBusy on
// SQLSTATE 55P03. The hash is read only so sessionCurrent can test that it
// exists; it is never compared, logged, returned or copied.
func currentAdminIn(tx *gorm.DB, me *models.User, pred func(*models.User) bool) error {
	var actor models.User
	err := tx.Raw("SELECT id, status, deleted_at, role, account_type, birth_date, password_hash, session_version, logout_version FROM users WHERE id = ? FOR SHARE NOWAIT", me.ID).Scan(&actor).Error
	if err != nil {
		if pgCode(err) == pgLockNotAvailable {
			return fmt.Errorf("%w: %w", errActorBusy, err)
		}
		return err
	}
	if !sessionCurrent(&actor, me) {
		return errSecuritySession
	}
	if !policy.IsAdmin(asPerson(&actor)) || (pred != nil && !pred(&actor)) {
		return errActorForbidden
	}
	return nil
}

// deletionSeal is the seal of the admin account deletion: the actor is decided
// on the locked row as the last locking statement before COMMIT. pred is the
// attempt's reason plan's predicate: set when the account has contact-request
// reasons (the locked actor must be allowed to review them), nil otherwise.
func (a *App) deletionSeal(me *models.User, pred func(*models.User) bool) *contactSeal {
	return &contactSeal{run: func(tx *gorm.DB) error {
		if a.hooks.sealAttempts != nil {
			a.hooks.sealAttempts.Add(1)
		}
		if a.hooks.deleteBeforeSeal != nil {
			a.hooks.deleteBeforeSeal(tx)
		}
		return currentAdminIn(tx, me, pred)
	}}
}

// adminDeletionTarget is adminTargetUser for the deletion route, with one
// difference: a target that is already gone answers attempt_closed, not 404,
// when the request carries an id the ledger already holds, and
// attempt_expired when the id does not open for this admin and account (another
// process, a restart, another tuple). A replay of an applied deletion therefore
// reads as "this attempt was used" and the panel settles it, while a missing
// target with an unclaimed id of its own tuple still answers 404 and records
// nothing (nothing ran and the id was never claimed). attemptID is the one id
// readDeleteUserBody found; a body that did not parse or named it
// ambiguously gives none, so it answers 404 too.
func (a *App) adminDeletionTarget(c *gin.Context, me *models.User, attemptID string) (*models.User, bool) {
	u, id := a.loadTargetUser(c)
	if u == nil {
		if attemptID != "" {
			nonce, _, ok := a.openAttempt(attemptID, attemptKindAccountDelete, me.ID, uint(max(id, 0)))
			if !ok {
				// Not this process's id for this admin and account: the
				// submission rule's first step, before any lookup.
				answerSubmit(c, submitExpired)
				return nil, false
			}
			if st, _ := a.attempts.ledger.look(nonce); st != attemptAbsent {
				answerSubmit(c, submitClosed)
				return nil, false
			}
		}
		jsonError(c, http.StatusNotFound, "user not found")
		return nil, false
	}
	return u, true
}

// --- mint and settle routes --------------------------------------------------

type attemptMintJSON struct {
	AttemptID string `json:"attemptId"`
}

type attemptSettleJSON struct {
	Result         string `json:"result"`
	SubjectDeleted *bool  `json:"subjectDeleted,omitempty"`
}

const (
	settleApplied            = "applied"
	settleNotApplied         = "not_applied"
	settleHistoryUnavailable = "history_unavailable"
	settleChecking           = "checking"
)

// apiAdminMintOperationAttempt mints an attempt id. It reads no database: an
// id grants nothing by itself, since the deletion still decides everything.
func (a *App) apiAdminMintOperationAttempt(c *gin.Context) {
	var body struct {
		Kind          string `json:"kind"`
		SubjectUserID uint   `json:"subjectUserId"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Kind != attemptKindAccountDelete || body.SubjectUserID == 0 {
		jsonError(c, http.StatusBadRequest, "invalid request")
		return
	}
	if a.restoreDetected() {
		answerRestoreDetected(c)
		return
	}
	id, err := a.mintAttempt(body.Kind, apiUser(c).ID, body.SubjectUserID)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not make an attempt id")
		return
	}
	c.JSON(http.StatusOK, attemptMintJSON{AttemptID: id})
}

// answerRestoreDetected is the 503 every fenced route and the mint send once
// the process has seen a restore.
func answerRestoreDetected(c *gin.Context) {
	fail(c, errRestoreDetected)
}

// settleFacts is what settle's seal read inside the committed transaction.
type settleFacts struct {
	state          attemptState
	receipt        bool
	subjectDeleted bool
}

// apiAdminSettleOperationAttempt answers what became of an attempt id. Order:
// pure mac and age checks (history_unavailable, nothing touched); a read-only
// ledger look (a running attempt is waited for outside every lock and
// transaction); then one fenced transaction (fencedContactMutation) whose
// callback adds nothing after the ticket, then the wrapper's reconcile of the subject, then the seal: the actor decided
// FOR SHARE NOWAIT, the ledger transition (absent -> fenced), and plain reads
// of the receipt and the subject. The answer is sent only after the call
// returns nil, an acknowledged COMMIT. The response carries no actor,
// other-attempt or attribution field.
func (a *App) apiAdminSettleOperationAttempt(c *gin.Context) {
	me := apiUser(c)
	var body struct {
		AttemptID     string `json:"attemptId"`
		Kind          string `json:"kind"`
		SubjectUserID uint   `json:"subjectUserId"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Kind != attemptKindAccountDelete || body.SubjectUserID == 0 {
		jsonError(c, http.StatusBadRequest, "invalid request")
		return
	}
	if a.restoreDetected() {
		answerRestoreDetected(c)
		return
	}
	nonce, mint, ok := a.openAttempt(body.AttemptID, body.Kind, me.ID, body.SubjectUserID)
	if !ok || a.attemptElapsed()-mint > attemptSettleWindow {
		c.JSON(http.StatusOK, attemptSettleJSON{Result: settleHistoryUnavailable})
		return
	}
	if a.hooks.settleBeforeTx != nil {
		a.hooks.settleBeforeTx()
	}
	ctx, cancel := context.WithTimeout(context.Background(), attemptSettleBudget)
	defer cancel()
	retry := newBusyRetry(ctx)
	for {
		// s2: a running attempt is waited for with no transaction, gate or lock.
		if st, done := a.attempts.ledger.look(nonce); st == attemptRunning && done != nil {
			select {
			case <-done:
			case <-ctx.Done():
				c.JSON(http.StatusOK, attemptSettleJSON{Result: settleChecking})
				return
			}
		}
		if ctx.Err() != nil {
			c.JSON(http.StatusOK, attemptSettleJSON{Result: settleChecking})
			return
		}
		var facts settleFacts
		subject, nonceHex := body.SubjectUserID, hex.EncodeToString(nonce[:])
		seal := &contactSeal{run: func(tx *gorm.DB) error {
			if a.hooks.sealAttempts != nil {
				a.hooks.sealAttempts.Add(1)
			}
			if err := currentAdminIn(tx, me, nil); err != nil {
				return err
			}
			if a.hooks.settleBeforeFence != nil {
				a.hooks.settleBeforeFence(tx)
			}
			st, err := a.attempts.ledger.settleTransition(nonce, mint, a.attemptElapsed())
			if err != nil {
				return err
			}
			facts.state = st
			if a.hooks.settleAfterFence != nil {
				a.hooks.settleAfterFence(tx)
			}
			if err := tx.Raw("SELECT EXISTS (SELECT 1 FROM admin_operation_attempts WHERE attempt_id = decode(?, 'hex'))", nonceHex).Scan(&facts.receipt).Error; err != nil {
				return err
			}
			return tx.Raw("SELECT EXISTS (SELECT 1 FROM users WHERE id = ? AND deleted_at IS NOT NULL)", subject).Scan(&facts.subjectDeleted).Error
		}}
		// Nothing runs after the ticket but the seal: settle's work is the seal.
		err := a.fencedContactMutation(ctx, contactChange{UserID: body.SubjectUserID}, func(*gorm.DB) (*contactSeal, error) {
			return seal, nil
		})
		switch {
		case err == nil:
			c.JSON(http.StatusOK, settleAnswer(facts))
			return
		case errors.Is(err, errRestoreDetected):
			answerRestoreDetected(c)
			return
		case errors.Is(err, errSecuritySession):
			fail(c, errSecuritySession)
			return
		case errors.Is(err, errActorForbidden):
			jsonError(c, http.StatusForbidden, "forbidden")
			return
		case errors.Is(err, errSettleRunning) && rollbackConfirmed(err):
			// Back to the wait: the attempt is running.
			continue
		case errors.Is(err, errActorBusy) && rollbackConfirmed(err):
			if again, _ := retry.again(); !again {
				c.JSON(http.StatusOK, attemptSettleJSON{Result: settleChecking})
				return
			}
			continue
		default:
			// The ledger is full, the transaction failed or timed out, or its
			// COMMIT is unknown (even when it fenced): the answer is checking.
			c.JSON(http.StatusOK, attemptSettleJSON{Result: settleChecking})
			return
		}
	}
}

// settleAnswer is the disclosure after an acknowledged settle COMMIT.
func settleAnswer(f settleFacts) attemptSettleJSON {
	switch f.state {
	case attemptApplied:
		return attemptSettleJSON{Result: settleApplied}
	case attemptUnknown:
		if f.receipt {
			return attemptSettleJSON{Result: settleApplied}
		}
		return attemptSettleJSON{Result: settleHistoryUnavailable}
	case attemptFenced, attemptRolledBack:
		// subjectDeleted is present, and true, only when the account is already
		// deleted: "not applied, and it is gone anyway". An account that is still
		// there answers plain not_applied (design R7-C01), so the panel can offer
		// another press.
		ans := attemptSettleJSON{Result: settleNotApplied}
		if f.subjectDeleted {
			yes := true
			ans.SubjectDeleted = &yes
		}
		return ans
	default:
		return attemptSettleJSON{Result: settleChecking}
	}
}

// --- the receipt sweep ------------------------------------------------------

// sweepAttemptReceipts deletes receipts older than 24 hours, attemptSweepBatch
// at a time, each batch its own transaction under its own 10-second context.
// After the 2-minute budget the job is queued again, so a backlog is worked
// through across runs and two runs are safe.
func (a *App) sweepAttemptReceipts(ctx context.Context, now time.Time) error {
	deadline := time.Now().Add(attemptSweepBudget)
	cutoff := now.Add(-attemptReceiptKeep)
	for {
		bctx, cancel := context.WithTimeout(ctx, attemptSweepBatchBudget)
		res := a.DB.WithContext(bctx).Exec(`DELETE FROM admin_operation_attempts WHERE attempt_id IN (
			SELECT attempt_id FROM admin_operation_attempts WHERE created_at < ? ORDER BY created_at LIMIT ?)`, cutoff, attemptSweepBatch)
		cancel()
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected < int64(attemptSweepBatch) {
			return nil
		}
		if !time.Now().Before(deadline) {
			return jobs.Later(time.Now().Add(time.Minute))
		}
	}
}
