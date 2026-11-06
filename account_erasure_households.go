package main

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"

	"fold/internal/audit"
	"fold/internal/notify"
	"fold/models"
)

// Household locks of an account erasure (M7.4 R4-3b-1; inventory 9.6
// requirement 5, signed in 15.5: option (a)).
//
// The cycle this closes: the erasure holds an admin's user row FOR UPDATE and
// wants a household row, while a household writer holds that household and
// asks the same admin's row FOR KEY SHARE (notify.Send's recipient lock, the
// N-11 notices, promotion and removal notices written under the household).
// The erasure therefore never waits for a household row while it holds the
// user row: it takes every household with NOWAIT, and on SQLSTATE 55P03 the
// whole transaction rolls back (releasing the gate and the user lock) and the
// whole erasure runs again, a bounded number of times.
//
// Lock census (rechecked by TestErasureHouseholdLockCensus, which reads the
// production source):
//
//   - The deletion callback in apiAdminDeleteUser takes, in order: the contact
//     gate (contactMutationCtx); the hosted events FOR KEY SHARE and the
//     account FOR UPDATE (deletionHostPlan); the actor row, a plain read; the
//     households of the account's seat and of PERS, NOWAIT, ascending
//     (lockErasureHouseholdsIn); then, in the order the callback runs them,
//     the groups of handOffLeadership, the events of handOffHosting, the
//     request, notification and occurrence rows of eraseAccountRequestsIn,
//     promoteOnDisable and eraseDependentRecordsIn. None of those helpers
//     reaches lockHousehold or a row of households: promoteLocked and
//     eligibleRegistration read households and seats with plain SELECTs,
//     which take no row lock.
//   - Every household writer holds a households row while it writes notices or
//     takes another user's lock: apiAddHouseholdPerson and
//     apiUpdateHouseholdPerson (N-11 admin recipients, createGuardianLink),
//     apiDeleteHouseholdPerson through reconcileHouseholdPerson
//     (notifyRegistrationRemoved, promoteLocked), flagHouseholdChild13
//     (age_rollover.go, N-05), the admin and conversion paths, and
//     addHouseholdRegistration. Each is why the erasure's household lock is
//     NOWAIT: it can be the second party of the cycle in either order.
//
// The household step (R4-3b-2, eraseHouseholdRecordsIn) runs under those same
// locks and takes no household row of its own. Its other row locks, in order:
// the staged person's occurrences, ascending, inside reconcileHouseholdPerson
// (still under the household, like every registration writer that holds a
// household first); then the guardian_links rows it revokes, FOR UPDATE in
// ascending id, with the household and the user row already held (the order
// decideGuardianLink takes: household, teen, link); then promoteOnDisable's own
// ascending occurrence set. Two ascending runs are not one global order, so a
// writer that holds a later occurrence of the first run and then asks for the
// household can still close a D4-class cycle; the database breaks it with one
// 40P01, the erasure rolls back whole, and nothing retries it.
//
// Only 55P03 retries. A cycle through an occurrence lock (D4-class) is not
// closed by NOWAIT: the database breaks it with one 40P01 and the whole
// erasure rolls back and answers as any other failure; nothing retries it.
const (
	// householdAttempts is the most times one deletion request runs the whole
	// erasure transaction.
	householdAttempts = 3
)

var (
	// deleteRetryWindow is how long after the first attempt began a new
	// attempt may still start. Inside an attempt the bound is the request
	// context. A var so tests can shorten it; a test that sets it stays serial.
	deleteRetryWindow = 2 * time.Second
	// householdRetryBackoff is the wait before attempt 2 and attempt 3.
	householdRetryBackoff = [householdAttempts - 1]time.Duration{25 * time.Millisecond, 100 * time.Millisecond}
)

// errErasureHouseholdGone: a household the erasure selected has no row to lock.
// Foreign keys make that impossible; the erasure stops rather than guess.
var errErasureHouseholdGone = errors.New("an erasure household has no row to lock")

// isHouseholdBusy is true for SQLSTATE 55P03 (lock_not_available), the NOWAIT
// refusal. It reads through *contactRollbackError to the cause; a commit error
// does not unwrap, so a COMMIT result is never taken for it.
func isHouseholdBusy(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "55P03"
}

// lockErasureHouseholdsIn locks, FOR UPDATE NOWAIT in ascending id, the
// household of the erased account's seat and the households of PERS (read
// unscoped, soft-deleted people included). It runs after the user row lock and
// captureErasureSetsIn and before the erasure's first write, and no other
// erasure statement takes a households row. A 55P03 comes back as it is (the
// transaction is aborted; retryHouseholdBusy decides). It returns the ids it
// locked.
func lockErasureHouseholdsIn(tx *gorm.DB, s erasureSubject) ([]uint, error) {
	var ids []uint
	if len(s.People) > 0 {
		if err := tx.Raw(`SELECT household_id FROM household_accounts WHERE user_id = ?
			UNION
			SELECT household_id FROM household_people WHERE id IN ?
			ORDER BY household_id`, s.ID, s.People).Scan(&ids).Error; err != nil {
			return nil, err
		}
	} else if err := tx.Raw(`SELECT household_id FROM household_accounts WHERE user_id = ?
		ORDER BY household_id`, s.ID).Scan(&ids).Error; err != nil {
		return nil, err
	}
	for _, id := range ids {
		var got []uint
		if err := tx.Raw("SELECT id FROM households WHERE id = ? FOR UPDATE NOWAIT", id).Scan(&got).Error; err != nil {
			return nil, err
		}
		if len(got) != 1 {
			return nil, errErasureHouseholdGone
		}
	}
	return ids, nil
}

// householdRetry is the bounded retry of the whole erasure on a busy household
// (the plan's retryHouseholdBusy, in the shape guard R-6 allows: the guard
// requires the a.contactMutationCtx call to sit in apiAdminDeleteUser itself,
// never in a func literal, so the caller owns the loop and asks after each
// attempt).
//
//	retry := newHouseholdRetry(ctx)
//	for {
//		err = a.contactMutationCtx(ctx, change, fn)
//		var again bool
//		if again, err = retry.after(err); !again { break }
//	}
//
// At most householdAttempts attempts share ctx. Only a 55P03 whose rollback was
// acknowledged (rollbackConfirmed) runs again: that acknowledgment proves the
// server released the gate and the user locks, so the next BEGIN cannot wait
// on this attempt. Everything else comes back at once, and a COMMIT whose
// result is unknown (checked first) is never run twice. A 55P03 inside a
// *contactRollbackError also comes back at once, with no further BEGIN.
//
// A new attempt starts only within deleteRetryWindow of the first attempt's
// start and while ctx is live, after a backoff that ends early with ctx. When
// the window or the attempts run out the last 55P03 comes back; when ctx ends
// first the error carries the context's, so classifyContactErr answers timed
// out. The caller makes every attempt read its facts again.
type householdRetry struct {
	ctx     context.Context
	started time.Time
	n       int // attempts finished
}

func newHouseholdRetry(ctx context.Context) *householdRetry {
	return &householdRetry{ctx: ctx, started: time.Now()}
}

// after takes the error of the attempt that just finished. It returns true (and a
// nil error) when, after the backoff, the caller should run the next attempt,
// or false and the error to answer with.
func (r *householdRetry) after(err error) (again bool, final error) {
	if err == nil || classifyContactErr(err) == outcomeUnknown || !isHouseholdBusy(err) || !rollbackConfirmed(err) {
		return false, err
	}
	r.n++
	if r.n >= householdAttempts {
		return false, err
	}
	timer := time.NewTimer(householdRetryBackoff[r.n-1])
	select {
	case <-r.ctx.Done():
		timer.Stop()
		return false, errors.Join(r.ctx.Err(), err)
	case <-timer.C:
	}
	if time.Since(r.started) >= deleteRetryWindow {
		return false, err
	}
	if r.ctx.Err() != nil {
		return false, errors.Join(r.ctx.Err(), err)
	}
	return true, nil
}

// removedHouseholdName is the neutral name of a sole-subject household (T16,
// O-9). Nobody sees it: no account or person remains on the household.
const removedHouseholdName = "Removed household"

// errErasureIntegrity: the erased account's household identity links
// contradict each other (inventory T15, O-18). The erasure stops before its
// first write and the whole transaction rolls back; it never widens the
// selector to guess. The text names no id or person.
var errErasureIntegrity = errors.New("the account's household identity links disagree, so nothing was erased")

// errErasureHouseholdUnlocked: the household step reached a household that
// lockErasureHouseholdsIn did not lock. Both read the same seat and person
// rows under the held user lock, so this cannot happen; the erasure stops
// rather than write a household it does not hold.
var errErasureHouseholdUnlocked = errors.New("an erasure household was not locked")

// householdErasureCounts is what the household step did, for the erasure
// report (account_erasure_report.go). Counts only.
type householdErasureCounts struct {
	HouseholdsKept       int
	HouseholdsScrubbed   int
	PeopleAnonymized     int
	GuardianLinksRevoked int
}

// validatePersIn checks PERS (inventory 3.1, T15, O-18) before anything is
// written or locked: it reads both arms again, unscoped (soft-deleted people
// count), and aborts with errErasureIntegrity when the identity is not one
// clean individual. Completed = household_people.converted_user_id = U; staged
// = household_person_id of the approved household_conversion request with
// approved_user_id = U. It aborts when
//
//   - the distinct union holds more than one person (completed twice,
//     completed and a different staged person, or staged twice: the partial
//     unique indexes at baseline 2588-2596 normally stop the last, which is why
//     the test drops one);
//   - a staged person has no row, or already points at another account;
//   - any person of the set is named by an approved household_conversion
//     request whose approved_user_id is another account (the contradiction is
//     read from the person's side, so it is caught whether or not U has a
//     request of its own); or
//   - the invitation the staged request names (invitations.household_person_id,
//     when set) belongs to a person of a different household.
//
// Plain reads, no lock: the user row is held FOR UPDATE, and completing a
// conversion locks it, so no completion can change these links before commit.
// It returns the error unwrapped otherwise and logs nothing.
func validatePersIn(tx *gorm.DB, s erasureSubject) error {
	var completed []uint
	if err := tx.Raw(`SELECT id FROM household_people WHERE converted_user_id = ? ORDER BY id`, s.ID).Scan(&completed).Error; err != nil {
		return err
	}
	var staged []struct {
		PersonID     uint  `gorm:"column:person_id"`
		InvitationID *uint `gorm:"column:invitation_id"`
	}
	if err := tx.Raw(`SELECT household_person_id AS person_id, invitation_id FROM account_requests
		WHERE source = 'household_conversion' AND status = 'approved'
		AND approved_user_id = ? AND household_person_id IS NOT NULL ORDER BY id`, s.ID).Scan(&staged).Error; err != nil {
		return err
	}
	distinct := map[uint]bool{}
	for _, id := range completed {
		distinct[id] = true
	}
	for _, st := range staged {
		distinct[st.PersonID] = true
	}
	if len(distinct) > 1 {
		return errErasureIntegrity
	}
	for id := range distinct {
		var claimed int64
		if err := tx.Raw(`SELECT count(*) FROM account_requests
			WHERE source = 'household_conversion' AND status = 'approved'
			AND household_person_id = ? AND approved_user_id IS NOT NULL AND approved_user_id <> ?`, id, s.ID).Scan(&claimed).Error; err != nil {
			return err
		}
		if claimed > 0 {
			return errErasureIntegrity
		}
	}
	for _, st := range staged {
		var people []struct {
			HouseholdID uint  `gorm:"column:household_id"`
			Converted   *uint `gorm:"column:converted_user_id"`
		}
		if err := tx.Raw(`SELECT household_id, converted_user_id FROM household_people WHERE id = ?`, st.PersonID).Scan(&people).Error; err != nil {
			return err
		}
		if len(people) != 1 || (people[0].Converted != nil && *people[0].Converted != s.ID) {
			return errErasureIntegrity
		}
		if st.InvitationID == nil {
			continue
		}
		var invitations []struct {
			PersonID *uint `gorm:"column:household_person_id"`
		}
		if err := tx.Raw(`SELECT household_person_id FROM invitations WHERE id = ?`, *st.InvitationID).Scan(&invitations).Error; err != nil {
			return err
		}
		if len(invitations) != 1 {
			return errErasureIntegrity
		}
		if invitations[0].PersonID == nil {
			continue
		}
		var households []uint
		if err := tx.Raw(`SELECT household_id FROM household_people WHERE id = ?`, *invitations[0].PersonID).Scan(&households).Error; err != nil {
			return err
		}
		if len(households) != 1 || households[0] != people[0].HouseholdID {
			return errErasureIntegrity
		}
	}
	return nil
}

// eraseHouseholdRecordsIn is the household step of an account erasure (T12,
// T14-T16, T28, T36, inventory 9.6; R4-3b-2). It runs once, right after
// eraseContactRequestTextIn (so the account is already soft-deleted) and before
// promoteOnDisable, and touches only the households lockErasureHouseholdsIn
// already locked. In order:
//
//  1. the erased account's seat (T14), after noting which households it held;
//  2. PERS anonymized, unscoped: names, birth date, care notes and photo
//     consent cleared and the row soft-deleted, converted_user_id never
//     written (owner decision 2);
//  3. a staged person (converted_user_id NULL): future going, maybe and
//     waitlisted registrations cancelled through reconcileHouseholdPerson
//     (audit, notice to the living holder, promotion) after step 2, so no
//     stored notice carries the name; then the capability and note fields of
//     every PERS registration cleared, and never user_id or registered_by;
//  4. pending and confirmed guardian links of PERS, or with the account as
//     teen or guardian, revoked with one audit row each, the pending task
//     resolved; check_note and finished rows untouched (O-4);
//  5. unused teen_consent tokens that name the account as the teen;
//  6. each locked household scrubbed when householdErasedOwnerIn says so.
func (a *App) eraseHouseholdRecordsIn(tx *gorm.DB, s erasureSubject, households []uint, actorID uint, ip string) (householdErasureCounts, error) {
	var counts householdErasureCounts
	locked := make(map[uint]bool, len(households))
	for _, h := range households {
		locked[h] = true
	}
	// (1) The seat. Which households it held is read first: an empty household
	// is the account's own only through it.
	var seats []uint
	if err := tx.Raw(`SELECT household_id FROM household_accounts WHERE user_id = ? ORDER BY household_id`, s.ID).Scan(&seats).Error; err != nil {
		return counts, err
	}
	seatHouseholds := make(map[uint]bool, len(seats))
	for _, h := range seats {
		if !locked[h] {
			return counts, errErasureHouseholdUnlocked
		}
		seatHouseholds[h] = true
	}
	if err := tx.Exec(`DELETE FROM household_accounts WHERE user_id = ?`, s.ID).Error; err != nil {
		return counts, err
	}
	if len(s.People) > 0 {
		var homes []uint
		if err := tx.Raw(`SELECT DISTINCT household_id FROM household_people WHERE id IN ?`, s.People).Scan(&homes).Error; err != nil {
			return counts, err
		}
		for _, h := range homes {
			if !locked[h] {
				return counts, errErasureHouseholdUnlocked
			}
		}
		// (2) PERS, unscoped. converted_user_id stays exactly as stored.
		res := tx.Exec(`UPDATE household_people SET first_name = '', last_name = '', birth_date = NULL,
			care_notes = '', photo_consent = false, deleted_at = COALESCE(deleted_at, now())
			WHERE id IN ?`, s.People)
		if res.Error != nil {
			return counts, res.Error
		}
		counts.PeopleAnonymized = int(res.RowsAffected)
		// (3) A staged person: nobody converted them, so their registrations are
		// still theirs. Cancel the future ones the way removing a household
		// person does, then clear what could be used to manage or identify them.
		var staged []uint
		if err := tx.Raw(`SELECT id FROM household_people WHERE id IN ? AND converted_user_id IS NULL ORDER BY id`, s.People).Scan(&staged).Error; err != nil {
			return counts, err
		}
		for _, id := range staged {
			var person models.HouseholdPerson
			if err := tx.Unscoped().First(&person, id).Error; err != nil {
				return counts, err
			}
			if _, err := a.reconcileHouseholdPerson(tx, &person, true, actorID, ip); err != nil {
				return counts, err
			}
		}
		if err := tx.Exec(`UPDATE registrations SET manage_token_hash = '', childcare_ages_note = '', removal_note = ''
			WHERE household_person_id IN ? AND (manage_token_hash <> '' OR childcare_ages_note <> '' OR removal_note <> '')`, s.People).Error; err != nil {
			return counts, err
		}
	}
	// (4) Guardian links, locked in ascending id with the household and the
	// user row already held.
	var links []models.GuardianLink
	if err := tx.Raw(`SELECT id, guardian_user_id, teen_user_id, person_id, status FROM guardian_links
		WHERE status IN ('pending', 'confirmed') AND (teen_user_id = ? OR guardian_user_id = ? OR person_id IN ?)
		ORDER BY id FOR UPDATE`, s.ID, s.ID, s.People).Scan(&links).Error; err != nil {
		return counts, err
	}
	for _, link := range links {
		res := tx.Exec(`UPDATE guardian_links SET status = ?, decided_at = now(), updated_at = now()
			WHERE id = ? AND status IN ('pending', 'confirmed')`, models.GuardianRevoked, link.ID)
		if res.Error != nil {
			return counts, res.Error
		}
		if res.RowsAffected == 0 {
			continue
		}
		if link.Status == models.GuardianPending {
			if _, err := notify.Resolve(tx, notify.HouseholdLinkPending, link.ID, &actorID, "withdrawn"); err != nil {
				return counts, err
			}
		}
		meta := map[string]any{"reason": "account_deleted", "guardianUserId": link.GuardianUserID}
		if link.PersonID != nil {
			meta["personId"] = *link.PersonID
		}
		if link.TeenUserID != nil {
			meta["teenUserId"] = *link.TeenUserID
		}
		if err := audit.Write(tx, audit.Entry{Actor: &actorID, Action: audit.GuardianLinkRevoked,
			TargetType: "guardian_link", TargetID: link.ID, Meta: meta, IP: ip}); err != nil {
			return counts, err
		}
		counts.GuardianLinksRevoked++
	}
	// (5) A guardian's unused consent link for this teen dies with the teen.
	// guardianEmailAuthority writes the teen as the number "teenId"; a living
	// guardian's used tokens stay.
	if err := tx.Exec(`DELETE FROM account_tokens
		WHERE kind = ? AND used_at IS NULL AND payload->>'teenId' = ?`,
		models.TokenTeenConsent, strconv.FormatUint(uint64(s.ID), 10)).Error; err != nil {
		return counts, err
	}
	// (6) Each locked household, ascending.
	for _, h := range households {
		_, ok, err := householdErasedOwnerIn(tx, h, s, seatHouseholds)
		if err != nil {
			return counts, err
		}
		if !ok {
			counts.HouseholdsKept++
			continue
		}
		// Idempotent: a household already scrubbed is counted, not rewritten.
		if err := tx.Exec(`UPDATE households SET name = ?, address = '', updated_at = now()
			WHERE id = ? AND (name <> ? OR address <> '')`, removedHouseholdName, h, removedHouseholdName).Error; err != nil {
			return counts, err
		}
		counts.HouseholdsScrubbed++
	}
	return counts, nil
}

// householdErasedOwnerIn decides whether household h now describes only an
// erased individual (T16, O-9) and whose it is. It is evaluated after the
// subject's seat is deleted and PERS anonymized, with h locked, and reads
// everything unscoped (soft-deleted people and every user's seat count).
//
//   - S1: no household_accounts row of any user remains, else (0, false).
//   - The subject first: every person of h is in PERS(U) (an h with no people
//     only when it is in the subject's own seat set, seatHouseholds) gives U.
//   - Otherwise, only for h with at least one person: exactly one soft-deleted
//     X owns every person of h (both PERS arms), no person is owned by two
//     users, and X's whole PERS is at most one distinct person (else the
//     record is ambiguous) gives X.
//
// So with seats U and V and P in PERS(U), U erased first retains h (V's seat)
// and V erased second scrubs it with owner U, and the other order ends the
// same. Two erased subjects who each own a person of h leave it shared. No
// marker is read or written, so every erasure that locks h asks again. It
// returns database errors unchanged; ok is false for anything it cannot prove.
func householdErasedOwnerIn(tx *gorm.DB, h uint, s erasureSubject, seatHouseholds map[uint]bool) (owner uint, ok bool, err error) {
	var seat []uint
	if err := tx.Raw(`SELECT id FROM household_accounts WHERE household_id = ? LIMIT 1`, h).Scan(&seat).Error; err != nil {
		return 0, false, err
	}
	if len(seat) > 0 {
		return 0, false, nil
	}
	var people []uint
	if err := tx.Raw(`SELECT id FROM household_people WHERE household_id = ? ORDER BY id`, h).Scan(&people).Error; err != nil {
		return 0, false, err
	}
	if len(people) == 0 {
		if seatHouseholds[h] {
			return s.ID, true, nil
		}
		return 0, false, nil
	}
	mine := make(map[uint]bool, len(s.People))
	for _, id := range s.People {
		mine[id] = true
	}
	all := true
	for _, id := range people {
		if !mine[id] {
			all = false
			break
		}
	}
	if all {
		return s.ID, true, nil
	}
	var owned []struct {
		PersonID uint `gorm:"column:person_id"`
		UserID   uint `gorm:"column:user_id"`
	}
	if err := tx.Raw(`SELECT person_id, user_id FROM (
			SELECT id AS person_id, converted_user_id AS user_id FROM household_people
			WHERE id IN ? AND converted_user_id IS NOT NULL
			UNION
			SELECT household_person_id, approved_user_id FROM account_requests
			WHERE household_person_id IN ? AND source = 'household_conversion' AND status = 'approved'
			AND approved_user_id IS NOT NULL) o`, people, people).Scan(&owned).Error; err != nil {
		return 0, false, err
	}
	owners := make(map[uint]map[uint]bool, len(people))
	for _, row := range owned {
		if owners[row.PersonID] == nil {
			owners[row.PersonID] = map[uint]bool{}
		}
		owners[row.PersonID][row.UserID] = true
	}
	var candidates map[uint]bool
	for _, id := range people {
		got := owners[id]
		if len(got) != 1 {
			// Nobody owns the person (shared), or two users do (ambiguous).
			return 0, false, nil
		}
		if candidates == nil {
			candidates = got
			continue
		}
		next := map[uint]bool{}
		for u := range got {
			if candidates[u] {
				next[u] = true
			}
		}
		candidates = next
	}
	if len(candidates) != 1 {
		return 0, false, nil
	}
	var x uint
	for u := range candidates {
		x = u
	}
	var gone []uint
	if err := tx.Raw(`SELECT id FROM users WHERE id = ? AND deleted_at IS NOT NULL`, x).Scan(&gone).Error; err != nil {
		return 0, false, err
	}
	if len(gone) != 1 {
		return 0, false, nil
	}
	var whole []uint
	if err := tx.Raw(`SELECT id FROM household_people WHERE converted_user_id = ?
		UNION
		SELECT household_person_id FROM account_requests
		WHERE source = 'household_conversion' AND status = 'approved'
		AND approved_user_id = ? AND household_person_id IS NOT NULL`, x, x).Scan(&whole).Error; err != nil {
		return 0, false, err
	}
	sort.Slice(whole, func(i, j int) bool { return whole[i] < whole[j] })
	if len(whole) > 1 {
		return 0, false, nil
	}
	return x, true, nil
}
