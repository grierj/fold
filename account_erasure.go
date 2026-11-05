package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"fold/internal/audit"
	"fold/internal/mail"
	"fold/internal/notify"
	"fold/models"
)

// erasureSubject is what an account erasure needs to remember about the
// person after their row is scrubbed: the id and the address the account held
// at erasure, plus the REQ, PERS and INV sets (inventory 3.1) that
// captureErasureSetsIn reads once, before the first scrub.
//
// It lives only in memory inside the erasure transaction. It is never logged,
// audited, queued, serialized, put in an error or returned.
type erasureSubject struct {
	ID      uint
	Address string
	// Requests is REQ: account_requests of the person, by the retained
	// approved_user_id link or, with no link, by the held address.
	Requests []uint
	// People is PERS: the household people who are this individual. Read-only
	// in R4-2a; R4-3 uses it.
	People []uint
	// Invitations is INV: the invitations of the person, by retained id links
	// or, for one nobody accepted, by the held address.
	Invitations []uint
}

// captureErasureSubject reads the subject from the row the erasure has just
// locked FOR UPDATE and reloaded into u (deletionHostPlan), so the address is
// the one the account holds now and never a past one.
func captureErasureSubject(u *models.User) erasureSubject {
	return erasureSubject{ID: u.ID, Address: mail.NormalizeAddress(u.Email)}
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
	if err := tx.Raw(`SELECT id FROM account_requests
		WHERE approved_user_id = ? OR (approved_user_id IS NULL AND email = ?)
		ORDER BY id`, s.ID, s.Address).Scan(&req).Error; err != nil {
		return err
	}
	var pers []uint
	if err := tx.Raw(`SELECT id FROM household_people WHERE converted_user_id = ?
		UNION
		SELECT household_person_id FROM account_requests
		WHERE source = 'household_conversion' AND status = 'approved'
		AND approved_user_id = ? AND household_person_id IS NOT NULL`,
		s.ID, s.ID).Scan(&pers).Error; err != nil {
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
	ids = nil
	if err := tx.Raw(`SELECT id FROM invitations WHERE email = ? AND accepted_at IS NULL`, s.Address).Scan(&ids).Error; err != nil {
		return err
	}
	add(ids)

	s.Requests, s.People, s.Invitations = req, pers, nil
	sort.Slice(s.People, func(i, j int) bool { return s.People[i] < s.People[j] })
	for id := range inv {
		s.Invitations = append(s.Invitations, id)
	}
	sort.Slice(s.Invitations, func(i, j int) bool { return s.Invitations[i] < s.Invitations[j] })
	return nil
}

// erasedDomain is the reserved domain of every erased row's placeholder
// address. tombstoneUserIn keeps its own byte-identical
// deleted-<id>@deleted.invalid form, which R4-6's legacy match depends on.
const erasedDomain = "deleted.invalid"

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

// isErasedAddress reports whether addr is a placeholder address of the erased
// domain.
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
// up past d. The HTTP write timeout bounds the response and no transaction: an
// erasure can commit after the browser lost its answer.
func withStatementTimeout(tx *gorm.DB, d time.Duration, fn func() error) error {
	var prior string
	if err := tx.Raw("SELECT current_setting('statement_timeout')").Scan(&prior).Error; err != nil {
		return err
	}
	ms := d.Milliseconds()
	if ms < 1 {
		ms = 1
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
func eraseAccountRequestsIn(tx *gorm.DB, s erasureSubject, actorID uint) error {
	if len(s.Requests) > 0 {
		var withdrawn []uint
		if err := tx.Raw(`UPDATE account_requests SET status = 'withdrawn', password_hash = NULL, updated_at = now()
			WHERE id IN ? AND status = 'pending' RETURNING id`, s.Requests).Scan(&withdrawn).Error; err != nil {
			return err
		}
		sort.Slice(withdrawn, func(i, j int) bool { return withdrawn[i] < withdrawn[j] })
		for _, id := range withdrawn {
			if _, err := notify.Resolve(tx, notify.AccountRequested, id, &actorID, "withdrawn"); err != nil {
				return err
			}
		}
		if err := tx.Exec(`UPDATE account_requests SET name = '', email = `+erasedRowAddressSQL("account_requests")+`,
			phone = '', birth_date = NULL, message = '', guardian_email = '', password_hash = NULL,
			decision_note = '', ip = CASE WHEN source = 'member_invite' THEN ip ELSE '' END, updated_at = now()
			WHERE id IN ?`, s.Requests).Error; err != nil {
			return err
		}
	}
	if err := tx.Exec(`UPDATE account_requests SET ip = ''
		WHERE source = 'member_invite' AND requested_by = ? AND ip <> ''`, s.ID).Error; err != nil {
		return err
	}
	return tx.Exec(`UPDATE account_requests SET guardian_email = '' WHERE guardian_email = ?`, s.Address).Error
}

// eraseDependentRecordsIn scrubs the address-keyed rows around the erased
// person, after promoteOnDisable and before the throttle delete, which stays
// the callback's last statement. In order: (5) the invitations in INV keep
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
// actorID and ip are the deleting admin and the request address, used by the
// message cancellation audit rows.
func eraseDependentRecordsIn(tx *gorm.DB, s erasureSubject, actorID uint, ip string) error {
	if len(s.Invitations) > 0 {
		if err := tx.Exec(`UPDATE invitations SET email = `+erasedRowAddressSQL("invitations")+`,
			phone = '', name = '', token_hash = `+erasedTokenMarkerSQL("invitations")+`,
			revoked_at = CASE WHEN accepted_at IS NULL AND revoked_at IS NULL THEN now() ELSE revoked_at END
			WHERE id IN ?`, s.Invitations).Error; err != nil {
			return err
		}
	}
	if err := tx.Exec(`DELETE FROM guest_signups WHERE email = ? AND confirmed_at IS NULL`, s.Address).Error; err != nil {
		return err
	}
	if err := tx.Exec(`UPDATE guest_signups SET name = '', email = `+erasedRowAddressSQL("guest_signups")+`,
		ip = '', timezone = '', token_hash = `+erasedTokenMarkerSQL("guest_signups")+`, updated_at = now()
		WHERE email = ? AND confirmed_at IS NOT NULL`, s.Address).Error; err != nil {
		return err
	}
	if err := tx.Exec(`DELETE FROM mail_outboxes WHERE lower(btrim(to_address)) = ?`, s.Address).Error; err != nil {
		return err
	}
	return eraseMessagesIn(tx, s, actorID, ip)
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
// back, and the admin sees the usual "could not delete user" and can retry.
func eraseMessagesIn(tx *gorm.DB, s erasureSubject, actorID uint, ip string) error {
	var cancelled []uint
	if err := tx.Raw(`UPDATE messages SET status = ?, updated_at = now()
		WHERE author_id = ? AND status = ? RETURNING id`,
		models.MessageCancelled, s.ID, models.MessageScheduled).Scan(&cancelled).Error; err != nil {
		return err
	}
	sort.Slice(cancelled, func(i, j int) bool { return cancelled[i] < cancelled[j] })
	for _, id := range cancelled {
		if err := audit.Write(tx, audit.Entry{Actor: &actorID, Action: audit.MessageCancelled,
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

// tombstoneUserIn scrubs the personal columns of the erased account in the
// one UPDATE that also bumps session_version, so every open session ends.
// The row keeps its id, role (O-11), account_type, created_at and
// logout_version; the caller soft-deletes it afterwards. The address becomes
// a placeholder that frees the real one for a new account.
func tombstoneUserIn(tx *gorm.DB, u *models.User) error {
	return tx.Model(u).Updates(map[string]any{
		"email":                     fmt.Sprintf("deleted-%d@deleted.invalid", u.ID),
		"name":                      "",
		"display_name":              "",
		"phone":                     "",
		"timezone":                  "",
		"password_hash":             gorm.Expr("NULL"),
		"birth_date":                gorm.Expr("NULL"),
		"avatar_focal_x":            50,
		"avatar_focal_y":            50,
		"hide_contact_from_leaders": false,
		"avatar_key":                "",
		"status":                    models.UserDisabled,
		"administratively_disabled": true,
		"session_version":           gorm.Expr("session_version + 1"),
	}).Error
}
