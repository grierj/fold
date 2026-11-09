package main

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"fold/internal/audit"
	"fold/internal/mail"
	"fold/internal/notify"
	"fold/models"
)

// heldAddress is available only while erasing a freshly locked account. A
// legacy tombstone has lost its address; its placeholder is never a selector.
type heldAddress struct {
	value string
	ok    bool
}

// erasureSubject is what an account erasure needs to remember about the
// person after their row is scrubbed: the id and the address the account held
// at erasure, plus the REQ, PERS and INV sets (inventory 3.1) that
// captureErasureSetsIn reads once, before the first scrub.
//
// It lives only in memory inside the erasure transaction. It is never logged,
// audited, queued, serialized, put in an error or returned.
type erasureSubject struct {
	ID      uint
	Address heldAddress
	// Requests is REQ: account_requests of the person, by the retained
	// approved_user_id link or, with no link, by the held address.
	Requests []uint
	// People is PERS: the household people who are this individual.
	People []uint
	// Invitations is INV: the invitations of the person, by retained id links
	// or, for one nobody accepted, by the held address.
	Invitations []uint
}

// captureErasureSubject reads the subject from the row the erasure has just
// locked FOR UPDATE and reloaded into u (deletionHostPlan), so the address is
// the one the account holds now and never a past one.
func captureErasureSubject(u *models.User) erasureSubject {
	return erasureSubject{ID: u.ID, Address: heldAddress{value: mail.NormalizeAddress(u.Email), ok: true}}
}

// captureErasureSetsIn fills REQ, PERS and INV (inventory 3.1) with plain reads,
// no locks, inside the erasure transaction and before the first scrub. The
// selectors read only columns the erasure keeps (ids, status, source,
// audit meta userId), and an exact id link beats an address match: a row that
// names another account is never selected by address. Soft-deleted household
// people count (raw SQL, no soft-delete scope). Empty sets skip their IN
// queries. The sets stay in memory and are never logged or returned.
func captureErasureSetsIn(tx *gorm.DB, s *erasureSubject) error {
	var req []uint
	reqSQL := "SELECT id FROM account_requests WHERE approved_user_id = ?"
	args := []any{s.ID}
	if s.Address.ok {
		reqSQL += " OR (approved_user_id IS NULL AND email = ?)"
		args = append(args, s.Address.value)
	}
	if err := tx.Raw(reqSQL+" ORDER BY id", args...).Scan(&req).Error; err != nil {
		return err
	}
	var pers []uint
	if err := tx.Raw(persSQL, s.ID, s.ID).Scan(&pers).Error; err != nil {
		return err
	}
	inv := map[uint]bool{}
	add := func(ids []uint) {
		for _, id := range ids {
			inv[id] = true
		}
	}
	var ids []uint
	// (a) the acceptance audit row names the user, so it survives an address change.
	if err := tx.Raw(`SELECT target_id FROM audit_logs
		WHERE action = 'invitation.accepted' AND target_type = 'invitation'
		AND meta_json->>'userId' = ?`, strconv.FormatUint(uint64(s.ID), 10)).Scan(&ids).Error; err != nil {
		return err
	}
	add(ids)
	// (b) the invitation of a conversion request approved for this user.
	if len(req) > 0 {
		ids = nil
		if err := tx.Raw(`SELECT invitation_id FROM account_requests
			WHERE id IN ? AND approved_user_id = ? AND invitation_id IS NOT NULL`,
			req, s.ID).Scan(&ids).Error; err != nil {
			return err
		}
		add(ids)
	}
	// (c) the invitation that led to one of this individual's household people.
	if len(pers) > 0 {
		ids = nil
		if err := tx.Raw(`SELECT id FROM invitations WHERE household_person_id IN ?`, pers).Scan(&ids).Error; err != nil {
			return err
		}
		add(ids)
	}
	// (d) an invitation to the held address that nobody accepted.
	if s.Address.ok {
		ids = nil
		if err := tx.Raw(`SELECT id FROM invitations WHERE email = ? AND accepted_at IS NULL`, s.Address.value).Scan(&ids).Error; err != nil {
			return err
		}
		add(ids)
	}

	s.Requests, s.People, s.Invitations = req, pers, nil
	sort.Slice(s.People, func(i, j int) bool { return s.People[i] < s.People[j] })
	for id := range inv {
		s.Invitations = append(s.Invitations, id)
	}
	sort.Slice(s.Invitations, func(i, j int) bool { return s.Invitations[i] < s.Invitations[j] })
	return nil
}

const erasedDomain = "deleted.invalid"

// tombstoneEmail is the address an erased account's users row keeps,
// deleted-<id>@deleted.invalid: byte-identical to what every release wrote,
// which the tombstone sweep's exact match (R4-6) depends on.
func tombstoneEmail(id uint) string {
	return fmt.Sprintf("deleted-%d@%s", id, erasedDomain)
}

// tombstoneEmailSQL is tombstoneEmail as a SQL expression over the id column
// idCol, a trusted constant.
func tombstoneEmailSQL(idCol string) string {
	return "'deleted-' || " + idCol + " || '@" + erasedDomain + "'"
}

// erasedRowAddressSQL is the SQL expression 'deleted-<table>-' || id ||
// '@deleted.invalid' (O-10), the placeholder a scrub writes in an address
// column. table is a trusted constant, never request data.
func erasedRowAddressSQL(table string) string {
	return "'deleted-" + table + "-' || id || '@" + erasedDomain + "'"
}

// erasedTokenMarkerSQL is the SQL expression 'erased-<table>-' || id for a
// unique token_hash column, so a marker never equals a sha256 hex digest.
// table is a trusted constant.
func erasedTokenMarkerSQL(table string) string {
	return "'erased-" + table + "-' || id"
}

func isErasedAddress(addr string) bool {
	return strings.HasSuffix(mail.NormalizeAddress(addr), "@"+erasedDomain)
}

// erasureRecipientWait bounds the erasure's one message_recipients UPDATE
// (budget three of L5). A var so tests can shorten it.
var erasureRecipientWait = 20 * time.Second

// withStatementTimeout runs fn with a transaction-local statement_timeout of
// d: it reads the current setting, sets d with set_config(..., true), runs fn,
// and on success puts the prior value back.
//
// If fn returns an error it is returned unchanged and no further statement is
// issued. A failed statement (for example SQLSTATE 57014) has aborted the
// transaction, a restoring set_config would fail with 25P02, and the caller
// must return the error to its Transaction so GORM rolls back at once, which
// also undoes the local setting. If fn succeeds and the restore fails, the
// restore error is returned.
//
// Three separate budgets exist and none bounds another. Provider network
// limits are per request (Postmark 15 s, SMTP dial 10 s and exchange 30 s) and
// the batch deadline is only a context handed to Send. The database bound here
// covers one statement, so successive waits on rows that workers hold can add
// up past d. The HTTP write timeout bounds the response and no transaction; the
// admin deletion's own deadline and the contact transaction's server bounds
// (contactMutationCtx) do. Inside that transaction the call never raises the
// timeout in force: it sets the smaller of d and the current value.
func withStatementTimeout(tx *gorm.DB, d time.Duration, fn func() error) error {
	// The text is put back as it was; the milliseconds decide the cap. A
	// timeout already in force (the contact transaction's contactServerCap)
	// is never raised: the call sets the smaller of the two, and 0 means none.
	var cur struct {
		Text string `gorm:"column:text"`
		MS   int64  `gorm:"column:ms"`
	}
	if err := tx.Raw("SELECT current_setting('statement_timeout') AS text, (SELECT setting::bigint FROM pg_settings WHERE name = 'statement_timeout') AS ms").Scan(&cur).Error; err != nil {
		return err
	}
	prior := cur.Text
	ms := d.Milliseconds()
	if ms < 1 {
		ms = 1
	}
	if cur.MS > 0 && cur.MS < ms {
		ms = cur.MS
	}
	if err := tx.Exec("SELECT set_config('statement_timeout', ?, true)", fmt.Sprintf("%dms", ms)).Error; err != nil {
		return err
	}
	if err := fn(); err != nil {
		return err
	}
	return tx.Exec("SELECT set_config('statement_timeout', ?, true)", prior).Error
}

// eraseAccountRequestsIn handles every account request of the erased person
// (REQ, captured by captureErasureSetsIn) and runs after the audit row and
// immediately before the target's notifications are deleted. Every
// account_requests statement of the erasure sits before that DELETE, so this
// path and expirePendingRequests, which updates the request and then resolves
// its notifications, take request rows and then notification rows in the same
// order (L2).
//
// In order: (1) a pending REQ row becomes withdrawn, its password hash is
// dropped and its waiting admin task is closed for every admin as resolved by
// the deleting admin ("withdrawn"); (2) the REQ rows lose name, address,
// phone, birth date, message, guardian address, password hash and decision
// note, and keep ip only for member_invite (the inviter's address, M39);
// every id link, status, decision field, expiry and flag stays; (3) a
// member_invite row the erased person requested loses its ip (M39, the
// inviter's own address); (4) a guardian_email equal to the erased address is
// cleared on any row, which is another person's request (M13).
//
// No audit action and no withdrawal mail are added: user.deleted is the
// record (a documented Consistency exception). It returns the first error
// unwrapped, issues no further statement after it, and logs nothing.
// A nil actor resolves waiting tasks as a system action. Already scrubbed
// requests are not rewritten, including their updated_at.
func eraseAccountRequestsIn(tx *gorm.DB, s erasureSubject, actor *uint) error {
	if len(s.Requests) > 0 {
		var withdrawn []uint
		if err := tx.Raw(`UPDATE account_requests SET status = 'withdrawn', password_hash = NULL, updated_at = now()
			WHERE id IN ? AND status = 'pending' RETURNING id`, s.Requests).Scan(&withdrawn).Error; err != nil {
			return err
		}
		sort.Slice(withdrawn, func(i, j int) bool { return withdrawn[i] < withdrawn[j] })
		for _, id := range withdrawn {
			if _, err := notify.Resolve(tx, notify.AccountRequested, id, actor, "withdrawn"); err != nil {
				return err
			}
		}
		if err := tx.Exec(`UPDATE account_requests SET name = '', email = `+erasedRowAddressSQL("account_requests")+`,
			phone = '', birth_date = NULL, message = '', guardian_email = '', password_hash = NULL,
			decision_note = '', ip = CASE WHEN source = 'member_invite' THEN ip ELSE '' END, updated_at = now()
			WHERE id IN ? AND (name <> '' OR phone <> '' OR message <> '' OR guardian_email <> ''
			OR decision_note <> '' OR birth_date IS NOT NULL OR password_hash IS NOT NULL
			OR email <> `+erasedRowAddressSQL("account_requests")+`
			OR (source <> 'member_invite' AND ip <> ''))`, s.Requests).Error; err != nil {
			return err
		}
	}
	if err := tx.Exec(`UPDATE account_requests SET ip = ''
		WHERE source = 'member_invite' AND requested_by = ? AND ip <> ''`, s.ID).Error; err != nil {
		return err
	}
	if s.Address.ok {
		return tx.Exec(`UPDATE account_requests SET guardian_email = '' WHERE guardian_email = ? AND guardian_email <> ''`, s.Address.value).Error
	}
	return nil
}

// eraseDependentRecordsIn scrubs the address-keyed rows around the erased
// person, after promoteOnDisable and before the throttle delete, which stays
// eraseAccountIn's last statement. In order: (5) the invitations in INV keep
// their inviter, group, household person, acceptance time and revocation
// but lose address, phone, name and token (an open one is revoked, so its
// link dies); (6) a guest signup at the erased address that was never
// confirmed is deleted, a confirmed one is anonymized (placeholder address,
// marker token, name, ip and timezone cleared) and keeps its occurrence and
// times; (7) the development outbox copies sent to the address are deleted.
//
// Statements 5 and 6 run after the user row lock, which guest redemption also
// takes first (holder, then occurrence, then signup), so the signup rows are
// reached in the order redemption holds them. It returns the first error
// unwrapped, issues no further statement after it, and logs nothing.
// actor and ip are the deleting admin (nil for the sweep) and the request
// address, used by the message cancellation audit rows.
// An unavailable held address skips every address-only statement (O-17).
func eraseDependentRecordsIn(tx *gorm.DB, s erasureSubject, actor *uint, ip string) error {
	if len(s.Invitations) > 0 {
		if err := tx.Exec(`UPDATE invitations SET email = `+erasedRowAddressSQL("invitations")+`,
			phone = '', name = '', token_hash = `+erasedTokenMarkerSQL("invitations")+`,
			revoked_at = CASE WHEN accepted_at IS NULL AND revoked_at IS NULL THEN now() ELSE revoked_at END
			WHERE id IN ? AND (email <> `+erasedRowAddressSQL("invitations")+`
			OR phone <> '' OR name <> '' OR token_hash <> `+erasedTokenMarkerSQL("invitations")+`
			OR (accepted_at IS NULL AND revoked_at IS NULL))`, s.Invitations).Error; err != nil {
			return err
		}
	}
	if s.Address.ok {
		if err := tx.Exec(`DELETE FROM guest_signups WHERE email = ? AND confirmed_at IS NULL`, s.Address.value).Error; err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE guest_signups SET name = '', email = `+erasedRowAddressSQL("guest_signups")+`,
			ip = '', timezone = '', token_hash = `+erasedTokenMarkerSQL("guest_signups")+`, updated_at = now()
			WHERE email = ? AND confirmed_at IS NOT NULL`, s.Address.value).Error; err != nil {
			return err
		}
		if err := tx.Exec(`DELETE FROM mail_outboxes WHERE lower(btrim(to_address)) = ?`, s.Address.value).Error; err != nil {
			return err
		}
	}
	if err := eraseMessagesIn(tx, s, actor, ip); err != nil {
		return err
	}
	return eraseActivityRowsIn(tx, s)
}

// eraseActivityRowsIn is statements 10 to 13 of eraseDependentRecordsIn:
// (10) the erased actor's series-undo receipts are deleted; (11) the manager
// add blocks that name the erased person as their subject are deleted (the
// provenance trigger already refuses a grant to a non-active subject);
// (12) their registrations lose the manage token hash, the childcare ages
// note and the removal note, and no access, identity, status or count column
// is written, so the access-provenance trigger takes its unchanged branch;
// (13) their notification delivery rows are deleted, which catches anything
// the notifications cascade missed. contact_requests is deliberately not
// touched here: the childcare ages note of the person's occurrence-add rows is
// blanked by eraseContactRequestTextIn (reason is eraseContactRequestReasonsIn's), and
// contact_grants have no new ending path, since reconcileContacts already ends
// them. It returns the first error unwrapped and issues no later statement.
func eraseActivityRowsIn(tx *gorm.DB, s erasureSubject) error {
	if err := tx.Exec(`DELETE FROM series_undo_receipts WHERE actor_id = ?`, s.ID).Error; err != nil {
		return err
	}
	if err := tx.Exec(`DELETE FROM manager_add_blocks WHERE subject_user_id = ?`, s.ID).Error; err != nil {
		return err
	}
	if err := tx.Exec(`UPDATE registrations SET manage_token_hash = '', childcare_ages_note = '', removal_note = ''
		WHERE user_id = ? AND (manage_token_hash <> '' OR childcare_ages_note <> '' OR removal_note <> '')`, s.ID).Error; err != nil {
		return err
	}
	return tx.Exec(`DELETE FROM notification_deliveries WHERE user_id = ?`, s.ID).Error
}

// eraseMessagesIn is statements 8 and 9 of eraseDependentRecordsIn. (8) The
// erased author's scheduled messages are cancelled, each with the one
// message.cancelled row an author's own cancel writes (actor the deleting
// admin, the caller's address); drafts, failed and sent messages and other
// people's messages stay (O-8). (9) The erased person's recipient rows lose
// their address and error text, and a queued row becomes suppressed as not
// active, so a later send job sends nothing to them. A guardian's copy row
// that is about the erased person has that person only in copy_of_user_id and
// stays (M23).
//
// The recipient UPDATE runs under a transaction-local statement_timeout of
// erasureRecipientWait for the whole statement (three separate budgets, none
// bounding another): a message worker holds one queued row FOR UPDATE SKIP
// LOCKED across its provider call, so the UPDATE can wait on it. Provider
// limits (Postmark 15 s a request, SMTP 10 s to dial and 30 s for the
// exchange) bound nothing here, because the worker's database work uses
// context.WithoutCancel and a Sender may ignore its ctx; batchDeadline is only
// a context handed to Send. The 20 s bound covers the whole statement, so
// successive waits on several rows can use it up whatever the provider does.
// The 60 s HTTP write budget bounds no transaction. When the statement times
// out the error comes back unchanged, the Transaction rolls the whole erasure
// back, and the admin sees a generic server error and can retry. The handler logs
// "could not delete user".
func eraseMessagesIn(tx *gorm.DB, s erasureSubject, actor *uint, ip string) error {
	var cancelled []uint
	if err := tx.Raw(`UPDATE messages SET status = ?, updated_at = now()
		WHERE author_id = ? AND status = ? RETURNING id`,
		models.MessageCancelled, s.ID, models.MessageScheduled).Scan(&cancelled).Error; err != nil {
		return err
	}
	sort.Slice(cancelled, func(i, j int) bool { return cancelled[i] < cancelled[j] })
	for _, id := range cancelled {
		if err := audit.Write(tx, audit.Entry{Actor: actor, Action: audit.MessageCancelled,
			TargetType: "message", TargetID: id, IP: ip}); err != nil {
			return err
		}
	}
	return withStatementTimeout(tx, erasureRecipientWait, func() error {
		return tx.Exec(`UPDATE message_recipients SET address = '', error = '',
			status = CASE WHEN status = 'queued' THEN 'suppressed' ELSE status END,
			reason = CASE WHEN status = 'queued' THEN ? ELSE reason END, updated_at = now()
			WHERE user_id = ? AND (address <> '' OR error <> '' OR status = 'queued')`,
			skipNotActive, s.ID).Error
	})
}

// eraseAccountCredentialsIn deletes everything that lets someone act as the
// erased account later: every account token (used or not, of every kind), the
// calendar link and the push devices. It runs after the contact gate and the
// account lock, in the slot revokeAccountTokens held, and deleting devices
// before the session bump means the push trigger finds none and queues no
// notice. It returns the first error unwrapped and logs nothing.
func eraseAccountCredentialsIn(tx *gorm.DB, s erasureSubject) error {
	if err := tx.Exec("DELETE FROM account_tokens WHERE user_id = ?", s.ID).Error; err != nil {
		return err
	}
	if err := tx.Exec("DELETE FROM calendar_tokens WHERE user_id = ?", s.ID).Error; err != nil {
		return err
	}
	return tx.Exec("DELETE FROM push_subscriptions WHERE user_id = ?", s.ID).Error
}

// eraseOwnedThrottleKeysIn deletes the sign-in and mail-budget rows the
// account owns (ownedThrottleKeys): one statement, a bound list, exact
// equality and never LIKE. It is separate from eraseAccountCredentialsIn only
// because it runs last, so the erasure holds throttle rows for the shortest
// time before commit.
//
// This is detected limit D2: the IN-list delete, reserveLoginAttempt,
// reauthenticate's refund and pruneLoginThrottles lock rows in scan or call
// order, not one key order, so Postgres can find a cycle and abort one side.
// The erasure is not deadlock-free. An aborted erasure rolls back whole and
// answers its usual 500. It returns the error unwrapped and logs nothing.
func eraseOwnedThrottleKeysIn(tx *gorm.DB, s erasureSubject) error {
	return tx.Exec("DELETE FROM login_throttles WHERE key IN ?", ownedThrottleKeys(s.ID, s.Address)).Error
}

// eraseConsentEvidenceIn blanks the ip, user_agent and evidence_text of the
// erased person's consent rows (T05, M24). Everything else on the row stays:
// the channel, scope, choice and time are the opt-out record the optional-mail
// and push readers still honor, and no hash or address column is added (O-1).
// suppressions and teen_consents are not touched (O-4).
//
// It must run after the soft delete in the same transaction: the
// consents_append_only trigger (migration 00003) allows this scrub only while
// the owner's users row has deleted_at set, and the transaction sees its own
// soft delete. Before it, the statement raises P0001.
//
// Locks: the erasure already holds the user row FOR UPDATE, and every consent
// writer (setConsent) takes that row FOR NO KEY UPDATE before it inserts, so
// writers and this scrub serialize on the user row and the row locks taken
// here on consents cannot form a cycle. Nothing else updates consents and
// readers take no lock. The filter skips rows that are already blank, which
// keeps a retry idempotent. It returns the error unwrapped and logs nothing.
//
// Export the in-force email opt-outs only when the locked account's address
// is still held. Migration 00003's scrub preserves statuses and choice times,
// so the export can run before or after it. The user lock prevents a consent
// append in between. Both statements roll back with the erasure transaction.
// Tombstones have no held address and issue no export statement.
func eraseConsentEvidenceIn(tx *gorm.DB, s erasureSubject) error {
	if err := tx.Exec("UPDATE consents SET ip = '', user_agent = '', evidence_text = '' "+
		"WHERE user_id = ? AND (ip <> '' OR user_agent <> '' OR evidence_text <> '')", s.ID).Error; err != nil {
		return err
	}
	if s.Address.ok && mail.NormalizeAddress(s.Address.value) != "" {
		return exportEmailOptOutsIn(tx, s.ID, mail.AddressHash(s.Address.value))
	}
	return nil
}

// eraseContactRequestTextIn blanks add_childcare_ages_note on the erased
// person's occurrence_add rows, whatever their status, and nothing else
// (R4-T07A). The person is the subject of those rows; rows where they only
// asked stay as written, and reason is not touched here
// (eraseContactRequestReasonsIn does it).
//
// It must run after the soft delete in the same transaction: the
// add_request_identity trigger (migration 00004) licenses this one blank only
// while the subject's users row has deleted_at set, and the transaction sees
// its own soft delete. Before it, the UPDATE raises 23514.
//
// Locks, in order: the contact gate and the user row FOR UPDATE (taken by the
// caller), then every selected request row FOR UPDATE in ascending id in one
// SELECT, then the writes, in batches of at most lookupChunk ids in that same
// ascending order, so one statement never carries more bind parameters than
// PostgreSQL's 65535 and no row lock is taken after a write. Nothing is
// promised about the order rows are visited inside one UPDATE. Every batch is
// part of the caller's transaction, so a failure in any of them rolls back
// all of them. It returns errors unwrapped and logs nothing.
func eraseContactRequestTextIn(tx *gorm.DB, s erasureSubject) error {
	var ids []uint
	if err := tx.Raw("SELECT id FROM contact_requests WHERE purpose = 'occurrence_add' AND subject_user_id = ? AND add_childcare_ages_note <> '' ORDER BY id FOR UPDATE", s.ID).Scan(&ids).Error; err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	for start := 0; start < len(ids); start += lookupChunk {
		chunk := ids[start:min(start+lookupChunk, len(ids))]
		res := tx.Exec("UPDATE contact_requests SET add_childcare_ages_note = '' WHERE id IN ? AND purpose = 'occurrence_add' AND subject_user_id = ? AND add_childcare_ages_note <> ''", chunk, s.ID)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != int64(len(chunk)) {
			return fmt.Errorf("ages-note scrub changed %d of %d rows", res.RowsAffected, len(chunk))
		}
	}
	return nil
}

// erasedUserColumn is one personal column of users that an erasure clears and
// the value it leaves, for GORM and as SQL.
type erasedUserColumn struct {
	name  string
	value any
	sql   string
}

// erasedUserColumns are the personal columns both erasures clear on the users
// row (tombstoneUserIn on the admin path, scrubTombstoneRowIn in the sweep).
// The row keeps its id, role (O-11), account_type, created_at and
// logout_version.
var erasedUserColumns = []erasedUserColumn{
	{"name", "", "''"},
	{"display_name", "", "''"},
	{"phone", "", "''"},
	{"timezone", "", "''"},
	{"password_hash", gorm.Expr("NULL"), "NULL"},
	{"birth_date", gorm.Expr("NULL"), "NULL"},
	{"avatar_focal_x", 50, "50"},
	{"avatar_focal_y", 50, "50"},
	{"hide_contact_from_leaders", false, "false"},
	{"avatar_key", "", "''"},
	{"status", models.UserDisabled, "'" + models.UserDisabled + "'"},
	{"administratively_disabled", true, "true"},
}

// tombstoneUserIn scrubs the personal columns of the erased account in the
// one UPDATE that also bumps session_version, so every open session ends;
// the caller soft-deletes it afterwards. The address becomes a placeholder
// that frees the real one for a new account.
func tombstoneUserIn(tx *gorm.DB, u *models.User) error {
	updates := map[string]any{"email": tombstoneEmail(u.ID), "session_version": sessionVersionBump()}
	for _, c := range erasedUserColumns {
		updates[c.name] = c.value
	}
	return tx.Model(u).Updates(updates).Error
}

// erasureOptions is everything that differs between the two account erasures:
// the admin deletion (apiAdminDeleteUser) and the system sweep of a legacy
// tombstone (eraseTombstoneOnce). Both run eraseAccountIn's steps in its one
// order; nothing else about them differs.
type erasureOptions struct {
	// Actor is the deleting admin. Nil for the sweep: its task resolutions,
	// audit rows and cancellations are system actions.
	Actor *uint
	// IP is the admin request's address; empty for the sweep.
	IP string
	// Live is the admin path's part. Nil for the sweep, whose subject is a
	// tombstone that is already soft-deleted and that lockTombstoneIn locked.
	Live *liveErasure
}

// liveErasure is what only the admin deletion brings: the account row that
// deletionHostPlan locked FOR UPDATE and this erasure soft-deletes, the
// successors of what the account leads and hosts alone, and the admin's
// decisions about the account's contact-request reasons.
type liveErasure struct {
	User *models.User
	// C is the request: handOffLeadership reads the acting admin and the
	// address from it.
	C            *gin.Context
	Successors   map[string]uint
	HostPicks    map[uint]uint
	LockedHosted []uint
	Reasons      reasonPlan
}

type erasureCounts struct {
	Households householdErasureCounts
	Audit      auditErasureCounts
}

// errErasureOptions: options that are neither the admin path (Live and Actor)
// nor the sweep (neither). A programming error; nothing is written.
var errErasureOptions = errors.New("erasure options must carry both an actor and a live account, or neither")

// eraseAccountIn is the one account erasure. It runs inside the caller's
// contact transaction, after the caller has taken the subject's users row
// FOR UPDATE (the admin path through deletionHostPlan, the sweep through
// lockTombstoneIn), and returns the first error unwrapped with no statement
// after it. The steps and why they run in this order:
//
//  1. captureErasureSetsIn reads REQ, PERS and INV once, with plain reads,
//     before anything is scrubbed (the selectors read columns later steps
//     blank).
//  2. validatePersIn aborts before any lock or write when PERS is not one
//     clean individual (T15, O-18).
//  3. lockErasureHouseholdsIn takes every household NOWAIT, before the first
//     write and while no household writer can be waiting on a row this
//     transaction holds (a 55P03 rolls the whole erasure back).
//  4. eraseAccountCredentialsIn deletes tokens, the calendar link and push
//     devices before the users row changes, so the session bump's push trigger
//     finds no device and queues no notice.
//  5. Live only: what the account leads and hosts alone is handed off, its
//     memberships end and its host rows go (handOffLeadership, handOffHosting),
//     in the lock order deletionHostPlan began (events, then the account).
//  6. The users row: tombstoneUserIn (live; it bumps the session version) or
//     scrubTombstoneRowIn (sweep; no version change, a clean row unwritten).
//  7. queueAvatarCleanup, under the row lock already held (R4-5).
//  8. Live only: the user.deleted audit row.
//  9. eraseAccountRequestsIn and then the notifications DELETE: every
//     account_requests statement runs before the notification rows, the order
//     expirePendingRequests also takes (L2). The DELETE's exact form is what
//     the erasure tests' barrier (erasureTestBeforeNotificationsDelete)
//     matches.
//  10. Live only: the soft delete. A tombstone already is soft-deleted.
//  11. eraseConsentEvidenceIn, eraseContactRequestTextIn and
//     eraseContactRequestReasonsIn, in that order: each table's trigger lets
//     its scrub through only once the owner's deleted_at is set, which this
//     transaction now sees. The reasons step has work only on the live path.
//  12. eraseHouseholdRecordsIn, after the soft delete so the sole-subject test
//     sees the account as erased, and only on the households step 3 locked.
//  13. promoteOnDisable: the account no longer counts, so its seats free up.
//     Only the live path tells people inside the cutoff.
//  14. eraseDependentRecordsIn: invitations, guest signups and the outbox,
//     after the user and occurrence locks, in guest redemption's order.
//  15. Sweep only: the tombstone's stale host rows (the live path removed its
//     rows in step 5).
//  16. eraseAuditDetailsIn.
//  17. eraseOwnedThrottleKeysIn last, so the sign-in rows are held for the
//     shortest time before commit (detected limit D2).
func (a *App) eraseAccountIn(tx *gorm.DB, s erasureSubject, opts erasureOptions) (erasureCounts, error) {
	var out erasureCounts
	if (opts.Live == nil) != (opts.Actor == nil) {
		return out, errErasureOptions
	}
	if err := captureErasureSetsIn(tx, &s); err != nil {
		return out, err
	}
	if err := validatePersIn(tx, s); err != nil {
		return out, err
	}
	homes, err := lockErasureHouseholdsIn(tx, s)
	if err != nil {
		return out, err
	}
	if err := eraseAccountCredentialsIn(tx, s); err != nil {
		return out, err
	}
	if live := opts.Live; live != nil {
		if err := handOffLeadership(live.C, tx, s.ID, live.Successors, "account_deleted"); err != nil {
			return out, err
		}
		if err := handOffHosting(tx, *opts.Actor, s.ID, live.HostPicks, live.LockedHosted, opts.IP); err != nil {
			return out, err
		}
		if err := tombstoneUserIn(tx, live.User); err != nil {
			return out, err
		}
	} else if err := scrubTombstoneRowIn(tx, s.ID); err != nil {
		return out, err
	}
	if err := queueAvatarCleanup(tx, s.ID); err != nil {
		return out, err
	}
	if opts.Live != nil {
		if err := audit.Write(tx, audit.Entry{Actor: opts.Actor, Action: audit.UserDeleted,
			TargetType: "user", TargetID: s.ID, IP: opts.IP}); err != nil {
			return out, err
		}
	}
	if err := eraseAccountRequestsIn(tx, s, opts.Actor); err != nil {
		return out, err
	}
	if err := tx.Where("user_id = ?", s.ID).Delete(&models.Notification{}).Error; err != nil {
		return out, err
	}
	var reasons reasonPlan
	if opts.Live != nil {
		if err := tx.Delete(opts.Live.User).Error; err != nil {
			return out, err
		}
		reasons = opts.Live.Reasons
	}
	if err := eraseConsentEvidenceIn(tx, s); err != nil {
		return out, err
	}
	if err := eraseContactRequestTextIn(tx, s); err != nil {
		return out, err
	}
	if err := eraseContactRequestReasonsIn(tx, s, reasons); err != nil {
		return out, err
	}
	if out.Households, err = a.eraseHouseholdRecordsIn(tx, s, homes, opts.Actor, opts.IP); err != nil {
		return out, err
	}
	if err := a.promoteOnDisable(tx, s.ID, opts.Live != nil); err != nil {
		return out, err
	}
	if err := eraseDependentRecordsIn(tx, s, opts.Actor, opts.IP); err != nil {
		return out, err
	}
	if opts.Live == nil {
		if err := removeTombstoneHostRowsIn(tx, s.ID); err != nil {
			return out, err
		}
	}
	if out.Audit, err = eraseAuditDetailsIn(tx, s); err != nil {
		return out, err
	}
	return out, eraseOwnedThrottleKeysIn(tx, s)
}
