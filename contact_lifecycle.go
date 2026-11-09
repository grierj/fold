package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"time"

	"fold/internal/accounts"
	"fold/internal/audit"
	"fold/internal/notify"
	"fold/internal/policy"
	"fold/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Contact operations use one deliberately coarse transaction advisory gate.
// Every contact-affecting domain transaction takes it FIRST, before its existing
// user/group/occurrence/notification locks. Contact code then locks requests by
// ID, then grants by ID. Contact-only operations never take domain row locks;
// group ask completion then keeps the existing group-first domain lock prefix.
// The gate stays held
// through audit, task resolution and commit (including reveal). This preserves
// both group-first membership and user-first deletion/consent lock prefixes.
const contactLockID int64 = 0x464f4c44434f4e

func lockContacts(tx *gorm.DB) error {
	return tx.Exec("SELECT pg_advisory_xact_lock(?)", contactLockID).Error
}

// lockContactsShared is the gate for pure reads: readers run together and wait
// only for writers, who take the exclusive lock above on the same id. Use it
// only where nothing but append-only rows (such as a refusal audit) is written.
func lockContactsShared(tx *gorm.DB) error {
	return tx.Exec("SELECT pg_advisory_xact_lock_shared(?)", contactLockID).Error
}

type contactChange struct {
	UserID  uint
	EventID uint
	GroupID uint
	// Seal, when set, runs after reconcileContacts and immediately before the
	// commit-reserve check (T07B-1b, R5-C01). It is a pointer so contactChange
	// stays comparable; reconcileContacts reads only the three ids above. Only
	// fencedContactMutation sets it (from its callback's result); a nil Seal
	// sends exactly the statements a call sent before.
	Seal *contactSeal
}

// contactSeal is the actor seal a fenced transaction runs last: the signed-in
// admin is decided FOR SHARE NOWAIT on their own row (currentAdminIn), so the
// lock adds no wait-for edge and the transaction then runs only plain reads,
// in-memory work and COMMIT.
type contactSeal struct{ run func(*gorm.DB) error }

// Server-side bounds for a contact transaction that has a deadline (R4-CTX).
// They are vars so tests can set and restore them; a test that sets one stays
// serial.
//
//   - contactCommitReserve is the time that must be left to send COMMIT: with
//     less, the change is rolled back instead (errCommitReserve).
//   - contactServerCap is the statement_timeout of the transaction, the longest
//     any one statement (the gate wait and COMMIT included) may run on the
//     server.
//   - adminDeleteBudget is the deadline of an admin's account deletion: the 60
//     second httpWriteTimeout minus 20 seconds, as syncMailTimeout.
var (
	contactCommitReserve = 2 * time.Second
	contactServerCap     = 20 * time.Second
	adminDeleteBudget    = 40 * time.Second
)

var (
	// errCommitReserve: too little time was left to commit, so the change was
	// rolled back (or never begun).
	errCommitReserve = errors.New("not enough time left to commit the contact change")
	// errContactRollback marks a *contactRollbackError.
	errContactRollback = errors.New("the contact transaction's rollback was not acknowledged")
	// errContactCommit marks a *contactCommitError.
	errContactCommit = errors.New("the contact transaction's commit result is unknown")
)

// contactRollbackError: the transaction failed before COMMIT and its ROLLBACK
// was not acknowledged on the live connection, so the server may still hold
// the gate and the row locks (they end by Terminate, socket close or the idle
// timeout). Unwrap gives Cause only, so errors.Is and errors.As see the
// original failure; Rollback is kept for logs.
type contactRollbackError struct {
	Cause    error
	Rollback error
}

func (e *contactRollbackError) Error() string {
	return fmt.Sprintf("%v (rollback not acknowledged: %v)", e.Cause, e.Rollback)
}
func (e *contactRollbackError) Unwrap() error        { return e.Cause }
func (e *contactRollbackError) Is(target error) bool { return target == errContactRollback }

// contactCommitError: COMMIT was sent (or may have been) and no answer came
// back, so the change may or may not be stored. Cause is for logs only;
// nothing unwraps it, so no caller classifies the failure by what it wraps.
type contactCommitError struct{ Cause error }

func (e *contactCommitError) Error() string {
	return fmt.Sprintf("could not confirm the commit: %v", e.Cause)
}
func (e *contactCommitError) Is(target error) bool { return target == errContactCommit }

// rollbackConfirmed is true only for an error before COMMIT whose ROLLBACK, if
// one was needed, came back on the live connection: the server has released
// the gate and the locks. It says nothing about the outcome.
func rollbackConfirmed(err error) bool {
	return err != nil && !errors.Is(err, errContactCommit) && !errors.Is(err, errContactRollback)
}

type contactOutcome int

const (
	// outcomeFailed: the change was not stored for a reason other than time.
	outcomeFailed contactOutcome = iota
	// outcomeTimedOut: the change was rolled back or never begun because time ran out.
	outcomeTimedOut
	// outcomeUnknown: COMMIT was sent and its result never came back.
	outcomeUnknown
)

// classifyContactErr is the one place that decides how a contactMutationCtx
// error ends. A commit error is unknown whatever it wraps; the rest read
// through *contactRollbackError to the cause. Every consumer classifies
// through it and never tests for a context error itself.
func classifyContactErr(err error) contactOutcome {
	if errors.Is(err, errContactCommit) {
		return outcomeUnknown
	}
	if errors.Is(err, errCommitReserve) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return outcomeTimedOut
	}
	if pgCode(err) == pgQueryCanceled {
		return outcomeTimedOut
	}
	return outcomeFailed
}

// discardConn closes a connection instead of returning it to the pool, so a
// session setting made for one call never reaches another.
func discardConn(conn *sql.Conn) {
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
}

func ceilMillis(d time.Duration) int64 {
	return int64((d + time.Millisecond - 1) / time.Millisecond)
}

// contactMutation is contactMutationCtx without a deadline. It runs on a.DB as
// it is, keeping any context the handle carries.
func (a *App) contactMutation(change contactChange, fn func(*gorm.DB) error) error {
	return a.contactMutationOn(context.Background(), a.DB, change, fn)
}

// contactMutationCtx retains the caller's transaction boundary, and ends access
// using its own writes before commit. A failed audit rolls back the domain
// mutation too. Notices queue their own delivery in the same transaction.
//
// When ctx has a deadline D the server is bounded too, because a client
// deadline is not server cancellation: on expiry the driver closes the
// connection asynchronously (a cancel request, then Terminate, then the
// socket), so this can return while the server still runs the statement and
// holds the gate. The call takes one dedicated connection. Just before BEGIN,
// r = time.Until(D); with r at most contactCommitReserve the call returns
// errCommitReserve and sends nothing. Otherwise, before BEGIN, the connection
// gets a session-level idle_in_transaction_session_timeout = r (whole
// milliseconds, rounded up), and the first statement after BEGIN sets
// statement_timeout = contactServerCap for the transaction (and the same idle
// value, locally). The idle timeout is set on the session, not only for the
// transaction, because a transaction-local setting is reverted by the abort a
// statement timeout causes, which would leave an aborted backend connected
// for as long as its socket stays open. Every statement is sent before D and
// the one in flight ends by its start + contactServerCap, and the backend then
// ends at Terminate, socket close or the idle timeout, so the gate and locks
// are released, and the backend is gone, by D + contactServerCap + r however
// the cancel and the socket fare. Since that session setting must never reach
// another caller, the connection is always discarded when the call ends
// (database/sql closes it), never returned to the pool. Nothing here assumes
// that returning means the server let go. Without a deadline nothing is set
// and the statements are the same as before.
//
// Errors: before COMMIT (the gate, the callback, either reconcile, the reserve
// check) the transaction is rolled back and the error returned as it came.
// If the ROLLBACK is not acknowledged the error is a *contactRollbackError
// (rollbackConfirmed is false). A COMMIT error of any kind, including a context
// that ends between the reserve check and COMMIT, is a *contactCommitError.
// A panic in fn rolls back and panics again.
func (a *App) contactMutationCtx(ctx context.Context, change contactChange, fn func(*gorm.DB) error) error {
	// db.Statement is the clone WithContext makes, so the bounded path's
	// ConnPool binding reaches only this call.
	return a.contactMutationOn(ctx, a.DB.WithContext(ctx), change, fn)
}

// contactMutationOn runs contactMutationCtx on db. The deadline and the
// cancellation checks use ctx; the transaction's statements run on db as it
// stands, so a handle that already carries its own context (a pinned
// connection, or a test's per-participant context) keeps it. contactMutation
// passes a.DB itself with a background ctx: replacing the handle's context
// there would drop its values and its cancellation (R4-3b-2 test repair).
func (a *App) contactMutationOn(ctx context.Context, db *gorm.DB, change contactChange, fn func(*gorm.DB) error) error {
	deadline, bounded := ctx.Deadline()
	// ErrTxDone can mean database/sql's cancellation rollback is still
	// releasing the connection. Do not use Raw alongside that release.
	autoRollback := false
	var remaining time.Duration
	if bounded {
		remaining = time.Until(deadline)
		if remaining <= contactCommitReserve {
			return errCommitReserve
		}
	}
	if bounded {
		sqlDB, err := a.DB.DB()
		if err != nil {
			return err
		}
		conn, err := sqlDB.Conn(ctx)
		if err != nil {
			return err
		}
		// Runs last, after the transaction is over. When ctx has ended,
		// database/sql may still be releasing the transaction from its own
		// goroutine, and closing waits for that, so it must not hold the caller.
		defer func() {
			if autoRollback {
				// Close is safe alongside database/sql's release; Raw can race
				// its clearing of conn. pgx closes the underlying connection
				// when its cancellation rollback fails, so Close cannot return
				// a live session with our timeout setting to the pool.
				go conn.Close()
				return
			}
			if ctx.Err() != nil {
				go discardConn(conn)
				return
			}
			discardConn(conn)
		}()
		remaining = time.Until(deadline)
		if remaining <= contactCommitReserve {
			return errCommitReserve
		}
		if _, err := conn.ExecContext(ctx, "SELECT set_config('idle_in_transaction_session_timeout', $1, false)",
			fmt.Sprintf("%dms", ceilMillis(remaining))); err != nil {
			return err
		}
		// contactMutationCtx passes a WithContext clone, so this binds only it.
		db.Statement.ConnPool = conn
	}
	tx := db.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	finished := false
	defer func() {
		if finished {
			return
		}
		if p := recover(); p != nil {
			autoRollback = errors.Is(tx.Rollback().Error, sql.ErrTxDone)
			panic(p)
		}
	}()
	rollback := func(cause error) error {
		finished = true
		if rerr := tx.Rollback().Error; rerr != nil {
			autoRollback = errors.Is(rerr, sql.ErrTxDone)
			return &contactRollbackError{Cause: cause, Rollback: rerr}
		}
		return cause
	}
	if bounded {
		if err := tx.Exec("SELECT set_config('statement_timeout', ?, true), set_config('idle_in_transaction_session_timeout', ?, true)",
			fmt.Sprintf("%dms", ceilMillis(contactServerCap)), fmt.Sprintf("%dms", ceilMillis(remaining))).Error; err != nil {
			return rollback(err)
		}
	}
	if err := lockContacts(tx); err != nil {
		return rollback(err)
	}
	// Event edits can move a retained departure date after its old end.
	// Persist expiry under the shared gate before the event/occurrence rows
	// change, so a move cannot resurrect authority that already elapsed.
	if change.EventID != 0 {
		if err := a.reconcileOccurrenceAccesses(tx, time.Now().UTC(), change); err != nil {
			return rollback(err)
		}
	}
	if err := fn(tx); err != nil {
		return rollback(err)
	}
	if err := a.reconcileContacts(tx, time.Now().UTC(), change); err != nil {
		return rollback(err)
	}
	// The actor seal is the last locking statement (R5-C01): after it the
	// transaction runs only plain reads, in-memory work and COMMIT.
	if change.Seal != nil {
		if err := change.Seal.run(tx); err != nil {
			return rollback(err)
		}
	}
	if ctx.Err() != nil || (bounded && time.Until(deadline) < contactCommitReserve) {
		return rollback(errCommitReserve)
	}
	finished = true
	if err := tx.Commit().Error; err != nil {
		// Commit checks its context before claiming the transaction. These
		// errors may therefore precede the automatic rollback's completion.
		autoRollback = errors.Is(err, sql.ErrTxDone) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
		return &contactCommitError{Cause: err}
	}
	return nil
}

func contactScope(r models.ContactRequest) policy.ContactScope {
	if r.Purpose != policy.RequestContact {
		return policy.ContactScope{}
	}
	return policy.ContactScope{RequesterID: r.RequesterID, SubjectUserID: r.SubjectUserID, ContextType: r.ContextType, ContextID: r.ContextID, Fields: r.Fields}
}
func contactRequestPolicy(r models.ContactRequest) policy.ContactRequest {
	if r.Purpose != policy.RequestContact {
		return policy.ContactRequest{}
	}
	return policy.ContactRequest{ContactScope: contactScope(r), NeedsAdmin: r.NeedsAdmin, SubjectDecision: r.SubjectDecision, AdminDecision: r.AdminDecision, Status: r.Status, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt}
}
func contactGrantPolicy(g models.ContactGrant, r models.ContactRequest) policy.ContactGrant {
	if r.Purpose != policy.RequestContact {
		return policy.ContactGrant{}
	}
	return policy.ContactGrant{ContactScope: contactScope(r), RequestID: g.RequestID, NeedsAdmin: r.NeedsAdmin, GrantedAt: g.GrantedAt, EndedAt: g.EndedAt}
}
func contactAccount(u *models.User, now time.Time, zone *time.Location) policy.ContactAccount {
	if u == nil {
		return policy.ContactAccount{}
	}
	out := policy.ContactAccount{Person: asPerson(u), Active: !u.DeletedAt.Valid && u.CanSignIn(), HideFromLeaders: u.HideContactFromLeaders}
	if u.BirthDate != nil {
		age := accounts.AgeOn(*u.BirthDate, now.In(zone))
		out.AgeYears = &age
	}
	return out
}
func contactUser(tx *gorm.DB, id uint) (*models.User, error) {
	var u models.User
	err := tx.First(&u, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &u, err
}

// loadContactFacts must run under the contact gate, after lock acquisition's
// clock. It is the page loader with one scope, so a list and a single request
// are decided from the same facts. Every query error fails closed rather than
// becoming an eligibility fact.
func (a *App) loadContactFacts(tx *gorm.DB, s policy.ContactScope, now time.Time) (policy.ContactAccessFacts, *models.User, error) {
	f := policy.ContactAccessFacts{Now: now, Context: policy.ContactContext{Type: s.ContextType, ID: s.ContextID}}
	zone, err := churchZoneIn(tx)
	if err != nil {
		return f, nil, err
	}
	r := models.ContactRequest{Purpose: policy.RequestContact, RequesterID: s.RequesterID, SubjectUserID: s.SubjectUserID, ContextType: s.ContextType, ContextID: s.ContextID}
	page, err := loadContactPageReads(tx, s.RequesterID, []models.ContactRequest{r}, false)
	if err != nil {
		return f, nil, err
	}
	return page.facts(now, zone, r), page.users[s.SubjectUserID], nil
}

func contactAudit(tx *gorm.DB, actor *uint, action string, r models.ContactRequest, reason, ip string) error {
	if r.ContextType != policy.ContactGroup && r.ContextType != policy.ContactEvent {
		r.ContextType = ""
	}
	k := requestKindOf(r.Purpose)
	// Empty purpose is reserved for malformed/not-found contact refusals.
	if k == nil && r.Purpose == "" {
		k = contactKind
	}
	if k == nil {
		return errors.New("unknown request audit purpose")
	}
	if k == addKind {
		meta := map[string]any{"purpose": r.Purpose, "requesterId": r.RequesterID, "subjectUserId": r.SubjectUserID, "contextType": r.ContextType, "contextId": r.ContextID, "needsAdmin": r.NeedsAdmin, "outcome": r.Status, "reason": reason}
		if r.Purpose == policy.RequestOccurrenceAdd {
			meta["occurrenceId"] = r.OccurrenceID
		}
		return audit.Write(tx, audit.Entry{Actor: actor, Action: k.action(action), TargetType: k.target, TargetID: r.ID, IP: ip, Meta: meta})
	}
	// A refused malformed input must not smuggle arbitrary text into audit.
	if !policy.ValidContactFields(r.Fields) {
		r.Fields = ""
	}
	return audit.Write(tx, audit.Entry{Actor: actor, Action: k.action(action), TargetType: k.target, TargetID: r.ID, IP: ip, Meta: map[string]any{"requesterId": r.RequesterID, "subjectUserId": r.SubjectUserID, "contextType": r.ContextType, "contextId": r.ContextID, "fields": r.Fields, "needsAdmin": r.NeedsAdmin, "outcome": r.Status, "reason": reason}})
}
func contactGuardians(tx *gorm.DB, teen uint) ([]uint, error) {
	var ids []uint
	err := tx.Model(&models.GuardianLink{}).Joins("JOIN users ON users.id = guardian_links.guardian_user_id AND users.deleted_at IS NULL AND users.status = ? AND users.account_type = ? AND users.password_hash IS NOT NULL", models.UserActive, models.AccountAdult).Where("teen_user_id = ? AND guardian_links.status = ?", teen, models.GuardianConfirmed).Order("guardian_user_id").Pluck("guardian_user_id", &ids).Error
	return ids, err
}
func contactConfirmedGuardian(tx *gorm.DB, actor, teen uint) (bool, error) {
	links, err := confirmedGuardianLinks(tx, actor, []uint{teen})
	return links[teen], err
}

func contactNotice(tx *gorm.DB, r models.ContactRequest, role noticeRole, recipients []uint, key string) error {
	k := requestKindOf(r.Purpose)
	if k == nil {
		return errors.New("unknown request notice purpose")
	}
	// Deleted/disabled parties retain history but receive no new feed/email.
	var active []uint
	if len(recipients) == 0 {
		return nil
	}
	if err := tx.Model(&models.User{}).Where("id IN ? AND status = ? AND password_hash IS NOT NULL", recipients, models.UserActive).Order("id").Pluck("id", &active).Error; err != nil {
		return err
	}
	_, err := notify.Send(tx, notify.Msg{Kind: k.notice(role), To: active, SubjectType: k.target, SubjectID: r.ID, Link: fmt.Sprintf("/%s/%d", k.route, r.ID), DedupeKey: fmt.Sprintf("%s:%d:%s", k.dedupe, r.ID, key), Vars: notify.Vars{Teen: r.NeedsAdmin}})
	return err
}
func resolveContactTasks(tx *gorm.DB, r models.ContactRequest, actor *uint, outcome string) error {
	k := requestKindOf(r.Purpose)
	if k == nil {
		return errors.New("unknown request task purpose")
	}
	for _, role := range []noticeRole{roleSubjectReview, roleAdminReview} {
		if _, err := notify.Resolve(tx, k.notice(role), r.ID, actor, outcome); err != nil {
			return err
		}
	}
	return nil
}
func finishContactRequest(tx *gorm.DB, r *models.ContactRequest, status, reason string, now time.Time) error {
	if requestKindOf(r.Purpose) == nil {
		return errors.New("unknown request lifecycle purpose")
	}
	r.Status, r.EndReason, r.CompletedAt = status, reason, &now
	if err := tx.Save(r).Error; err != nil {
		return err
	}
	action := audit.ContactEnded
	if status == policy.ContactExpired {
		action = audit.ContactExpired
	}
	if err := contactAudit(tx, nil, action, *r, reason, ""); err != nil {
		return err
	}
	if err := resolveContactTasks(tx, *r, nil, status); err != nil {
		return err
	}
	return contactNotice(tx, *r, roleUpdated, []uint{r.RequesterID, r.SubjectUserID}, status)
}
func endContactGrant(tx *gorm.DB, g *models.ContactGrant, r models.ContactRequest, reason string, actor *uint, now time.Time, ip string) error {
	if r.Purpose != policy.RequestContact {
		return errors.New("not a contact grant")
	}
	if g.EndedAt != nil {
		return nil
	}
	g.EndedAt, g.EndReason, g.EndedBy = &now, reason, actor
	if err := tx.Save(g).Error; err != nil {
		return err
	}
	action := audit.ContactEnded
	if actor != nil {
		action = audit.ContactRevoked
	}
	if err := contactAudit(tx, actor, action, r, reason, ip); err != nil {
		return err
	}
	recipients := []uint{r.RequesterID, r.SubjectUserID}
	if r.NeedsAdmin {
		ids, err := contactGuardians(tx, r.SubjectUserID)
		if err != nil {
			return err
		}
		recipients = append(recipients, ids...)
	}
	return contactNotice(tx, r, roleUpdated, recipients, "grant-ended")
}

// reconcileContacts is idempotent: only pending requests and unended grants
// are candidates. History stays granted after its grant ends. Scoped mutation
// hooks include group events so leadership loss cannot be undone by rejoining.
func (a *App) reconcileContacts(tx *gorm.DB, now time.Time, change contactChange) error {
	q := tx.Where("purpose IN ?", []string{policy.RequestContact, policy.RequestGroupAdd, policy.RequestOccurrenceAdd}).Where("status = ? OR id IN (SELECT request_id FROM contact_grants WHERE ended_at IS NULL)", policy.ContactPending)
	switch {
	case change.UserID != 0:
		q = q.Where("requester_id = ? OR subject_user_id = ?", change.UserID, change.UserID)
	case change.EventID != 0:
		q = q.Where("context_type = ? AND context_id = ?", policy.ContactEvent, change.EventID)
	case change.GroupID != 0:
		q = q.Where("(context_type = 'group' AND context_id = ?) OR (context_type = 'event' AND context_id IN (SELECT id FROM events WHERE group_id = ?))", change.GroupID, change.GroupID)
	}
	var rs []models.ContactRequest
	if err := q.Clauses(clause.Locking{Strength: "UPDATE"}).Order("id").Find(&rs).Error; err != nil {
		return err
	}
	for i := range rs {
		r := &rs[i]
		if r.Purpose == policy.RequestOccurrenceAdd {
			if err := a.reconcileOccurrenceAsk(tx, r, now); err != nil {
				return err
			}
			continue
		}
		if r.Purpose == policy.RequestGroupAdd {
			if err := a.reconcileGroupAsk(tx, r, now); err != nil {
				return err
			}
			continue
		}
		if r.Purpose != policy.RequestContact {
			continue
		}
		f, _, err := a.loadContactFacts(tx, contactScope(*r), now)
		if err != nil {
			return err
		}
		reason := policy.ContactIneligibility(f, r.NeedsAdmin)
		if r.Status == policy.ContactPending {
			status := ""
			if policy.ContactRequestExpired(contactRequestPolicy(*r), now) {
				status = policy.ContactExpired
				reason = ""
			} else if reason != "" {
				status = policy.ContactEnded
			}
			if status != "" {
				if err := finishContactRequest(tx, r, status, reason, now); err != nil {
					return err
				}
			}
		} else if reason != "" {
			var gs []models.ContactGrant
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("request_id = ? AND ended_at IS NULL", r.ID).Order("id").Find(&gs).Error; err != nil {
				return err
			}
			for j := range gs {
				if err := endContactGrant(tx, &gs[j], *r, reason, nil, now, ""); err != nil {
					return err
				}
			}
		}
	}
	return a.reconcileOccurrenceAccesses(tx, now, change)
}

func (a *App) sweepContacts(ctx context.Context, now time.Time) error {
	return a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var locked bool
		if err := tx.Raw("SELECT pg_try_advisory_xact_lock(?)", contactLockID).Scan(&locked).Error; err != nil {
			return err
		}
		if !locked {
			return nil
		}
		return a.reconcileContacts(tx, now, contactChange{})
	})
}

// addRequestPolicy is deliberately separate from contactScope: an ask never
// supplies contact authority, even after completion.
func addRequestPolicy(r models.ContactRequest) policy.AddRequest {
	occ := uint(0)
	if r.OccurrenceID != nil {
		occ = *r.OccurrenceID
	}
	return policy.AddRequest{AddScope: policy.AddScope{Purpose: r.Purpose, RequesterID: r.RequesterID, SubjectUserID: r.SubjectUserID, ContextType: r.ContextType, ContextID: r.ContextID, OccurrenceID: occ, Options: policy.AddOptions{OverLimit: r.AddOverLimit, OverCapacity: r.AddOverCapacity, AttendanceMode: r.AddAttendanceMode, GuestCount: r.AddGuestCount, ChildcareCount: r.AddChildcareCount, ChildcareAgesNote: r.AddChildcareAgesNote}}, NeedsAdmin: r.NeedsAdmin, SubjectDecision: r.SubjectDecision, AdminDecision: r.AdminDecision, Status: r.Status, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt}
}

// groupAddFacts are the facts of one group ask from rows already loaded: the
// single-request loader and the list page both build them here. g is nil when
// the group is gone.
func groupAddFacts(r models.ContactRequest, now time.Time, zone *time.Location, requester, subject *models.User, g *models.Group, requesterRel policy.Rel) policy.AddFacts {
	f := policy.AddFacts{Now: now, ContextType: r.ContextType, ContextID: r.ContextID}
	f.Requester, f.Subject = contactAccount(requester, now, zone), contactAccount(subject, now, zone)
	if g == nil {
		return f
	}
	f.ContextActive = g.Status == models.GroupActive
	f.Manager = policy.CanManageGroup(asPerson(requester), requesterRel)
	// Ordinary manager group additions allow active adults; teens alone need
	// this request contract. Hidden contact does not restrict membership.
	f.OrdinaryEligible = subject != nil && !policy.TeenAddNeedsApproval(asPerson(subject))
	return f
}

func (a *App) loadGroupAddFacts(tx *gorm.DB, r models.ContactRequest, now time.Time) (policy.AddFacts, error) {
	f := policy.AddFacts{Now: now, ContextType: r.ContextType, ContextID: r.ContextID}
	if r.Purpose != policy.RequestGroupAdd || r.ContextType != policy.ContactGroup {
		return f, nil
	}
	zone, err := churchZoneIn(tx)
	if err != nil {
		return f, err
	}
	requester, err := contactUser(tx, r.RequesterID)
	if err != nil {
		return f, err
	}
	subject, err := contactUser(tx, r.SubjectUserID)
	if err != nil {
		return f, err
	}
	var g models.Group
	err = tx.First(&g, r.ContextID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return groupAddFacts(r, now, zone, requester, subject, nil, policy.Rel{}), nil
	}
	if err != nil {
		return f, err
	}
	var m models.Membership
	if err = tx.Where("group_id = ? AND user_id = ?", g.ID, r.RequesterID).Limit(1).Find(&m).Error; err != nil {
		return f, err
	}
	return groupAddFacts(r, now, zone, requester, subject, &g, membershipRel(m)), nil
}

func (a *App) reconcileGroupAsk(tx *gorm.DB, r *models.ContactRequest, now time.Time) error {
	if r.Purpose != policy.RequestGroupAdd || r.Status != policy.ContactPending {
		return nil
	}
	f, err := a.loadGroupAddFacts(tx, *r, now)
	if err != nil {
		return err
	}
	if policy.AddRequestExpired(addRequestPolicy(*r), now) {
		return finishContactRequest(tx, r, policy.ContactExpired, "", now)
	}
	if reason := policy.AddIneligibility(addRequestPolicy(*r).AddScope, f, r.NeedsAdmin); reason != "" {
		return finishContactRequest(tx, r, policy.ContactEnded, reason, now)
	}
	return nil
}

// Only the group request finalizer constructs this fact. The membership core
// reloads its persisted purpose, exact scope and dual approvals under the gate.
// No HTTP boolean, user ID or email can supply teen-add provenance.
type verifiedGroupAdd struct {
	requestID uint
	now       time.Time
}

func (a *App) verifiedGroupAddIn(tx *gorm.DB, proof *verifiedGroupAdd, requester, subject, group uint) bool {
	var r models.ContactRequest
	if tx.Where("id = ? AND purpose = ? AND requester_id = ? AND subject_user_id = ? AND context_id = ?", proof.requestID, policy.RequestGroupAdd, requester, subject, group).First(&r).Error != nil {
		return false
	}
	f, err := a.loadGroupAddFacts(tx, r, proof.now)
	return err == nil && policy.CanFinalizeAddRequest(addRequestPolicy(r), f)
}
