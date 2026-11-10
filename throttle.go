package main

// The throttle module: every rate limit in Fold is counted here. Three
// mechanisms, each implemented once:
//
//   - login_throttles rows (countThrottleHit): per-key counters in Postgres,
//     with lockouts for sign-in. Keys come only from the builders in
//     throttle_keys.go. Sign-in and password proofs reserve, lock and refund
//     attempts; every other door counts hits through throttleHit, throttleAllow
//     and targetAllow.
//   - the audit-counted send limit (inviteSendAllowed): invitation mail is
//     counted from audit_logs under an advisory lock, because the audit rows
//     are the durable record of what was sent.
//   - the row-counted guest verification limit (guestVerifyAllowedIn): guest
//     verification mail is counted from guest_signups under a per-address
//     advisory lock, in the same transaction as the signup insert, because the
//     stored signups are the durable record of what was mailed.

import (
	"context"
	"fmt"
	"net/netip"
	"time"

	"fold/internal/audit"
	"fold/internal/mail"
	"fold/models"

	"gorm.io/gorm"
)

// publicSignupSiteLimit bounds each public signup door independently, across
// clients and targets. Rejections keep the form's ordinary success response.
const publicSignupSiteLimit = 100

// rateLimitIP groups IPv6 clients by /64 so rotating interface addresses cannot
// evade a per-IP budget. IPv4 and mapped IPv4 retain their individual budget.
// This key is only for throttles; audit records keep the original client IP.
func rateLimitIP(raw string) string {
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		return raw
	}
	addr = addr.Unmap().WithZone("")
	if addr.Is6() {
		return netip.PrefixFrom(addr, 64).Masked().String()
	}
	return addr.String()
}

// Login throttling (10.1): ten failures for one email lock that email for
// 15 minutes from any IP; fifty failures from one IP lock that IP.
const (
	emailFailureLimit = 10
	ipFailureLimit    = 50
	loginWindow       = 15 * time.Minute
)

func failureLimit(key throttleKey) int {
	if key.isIP() {
		return ipFailureLimit
	}
	return emailFailureLimit
}

// reserveLoginAttempt counts one attempt for each key before the password
// is checked, and reports whether every key is still allowed. Each key's
// row is incremented with one upsert, so parallel requests serialize on it
// and at most the limit pass. A locked key, or one already past its limit,
// is refused; a key that goes past its limit is locked for 15 minutes.
func (a *App) reserveLoginAttempt(ctx context.Context, keys []throttleKey) (bool, error) {
	window := fmt.Sprintf("%d seconds", int(loginWindow.Seconds()))
	allowed := true
	err := a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, key := range keys {
			failures, locked, err := countThrottleHit(tx, key, loginWindow)
			if err != nil {
				return err
			}
			switch {
			case locked:
				allowed = false
			case failures > failureLimit(key):
				allowed = false
				if err := tx.Exec(`UPDATE login_throttles
SET locked_until = now() + ?::interval, failures = 0, window_start = now()
WHERE key = ?`, window, string(key)).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
	return allowed, err
}

// countThrottleHit is the one upsert behind every throttle: it counts one
// hit for key, starting over when the key's window has passed, and reports
// the running count and whether the key is locked. Callers that do not use
// lockouts ignore locked.
func countThrottleHit(tx *gorm.DB, key throttleKey, window time.Duration) (failures int, locked bool, err error) {
	windowArg := fmt.Sprintf("%d seconds", int(window.Seconds()))
	var row struct {
		Failures int
		Locked   bool
	}
	err = tx.Raw(`
INSERT INTO login_throttles (key, failures, window_start) VALUES (?, 1, now())
ON CONFLICT (key) DO UPDATE SET
  failures = CASE WHEN login_throttles.window_start < now() - ?::interval
                  THEN 1 ELSE login_throttles.failures + 1 END,
  window_start = CASE WHEN login_throttles.window_start < now() - ?::interval
                      THEN now() ELSE login_throttles.window_start END
RETURNING failures, COALESCE(locked_until > now(), false) AS locked`,
		string(key), windowArg, windowArg).Scan(&row).Error
	return row.Failures, row.Locked, err
}

// pruneLoginThrottles drops rows that carry no information. The daily
// cleanup.login_throttles job runs it.
func (a *App) pruneLoginThrottles(ctx context.Context) error {
	return a.DB.WithContext(ctx).Exec(`DELETE FROM login_throttles
WHERE window_start < now() - interval '1 day' AND (locked_until IS NULL OR locked_until < now())`).Error
}

// lockLoginKeysAtLimit runs after a failed attempt (already counted by
// reserveLoginAttempt): a key that has reached its limit within the window
// is locked for 15 minutes and starts over.
func (a *App) lockLoginKeysAtLimit(ctx context.Context, keys []throttleKey) error {
	window := fmt.Sprintf("%d seconds", int(loginWindow.Seconds()))
	for _, key := range keys {
		err := a.DB.WithContext(ctx).Exec(`UPDATE login_throttles
SET locked_until = now() + ?::interval, failures = 0, window_start = now()
WHERE key = ? AND failures >= ?`, window, string(key), failureLimit(key)).Error
		if err != nil {
			return err
		}
	}
	return nil
}

// throttleAllow counts one hit for key and reports whether it stays under
// limit within window. It shares the login_throttles table under its own
// key prefix; the login lockout never reads these keys.
func (a *App) throttleAllow(ctx context.Context, key throttleKey, limit int, window time.Duration) (bool, error) {
	failures, err := a.throttleHit(ctx, key, window)
	return failures <= limit, err
}

// Per-target email limits (PLAN 10.6 defaults, as for guest verification
// emails): a door that mails an address someone else typed sends it at most
// 3 per hour and 10 per day, whoever asks and from wherever (#91, #92).
const (
	targetHourLimit = 3
	targetDayLimit  = 10
)

// targetAllow counts one send to key (a builder's key for an address or user id)
// in both the hourly and the daily window and reports whether both stay
// under their limits.
func (a *App) targetAllow(ctx context.Context, key throttleKey) (bool, error) {
	hourOK, err := a.throttleAllow(ctx, key.targeted()[0], targetHourLimit, time.Hour)
	if err != nil {
		return false, err
	}
	dayOK, err := a.throttleAllow(ctx, key.targeted()[1], targetDayLimit, 24*time.Hour)
	return hourOK && dayOK, err
}

// throttleHit counts one hit against key and reports the running count
// inside the window. It lives in login_throttles, so it survives
// restarts and is shared by every replica. Public forms each use their
// own key prefix, so one form's traffic never throttles another's.
func (a *App) throttleHit(ctx context.Context, key throttleKey, window time.Duration) (int, error) {
	failures, _, err := countThrottleHit(a.DB.WithContext(ctx), key, window)
	return failures, err
}

// inviteSendAllowed is the invitation send limit: ordinary invitations and
// household-conversion mail (resends included) share it. Audits are
// durable counters; refused ordinary invitations spend only the actor budget.
// The actor advisory lock serializes different households and invite targets.
func inviteSendAllowed(tx *gorm.DB, actor uint, email string) (bool, error) {
	if err := tx.Exec("SELECT pg_advisory_xact_lock(?, ?)", inviteSendLockClass, actor).Error; err != nil {
		return false, err
	}
	var byActor, byEmail int64
	mailKinds := []string{audit.InvitationCreated, audit.InvitationResent}
	actorKinds := []string{audit.InvitationCreated, audit.InvitationResent, audit.InvitationRefused}
	if err := tx.Model(&models.AuditLog{}).Where("actor_id = ? AND action IN ? AND at > now() - interval '24 hours'", actor, actorKinds).Count(&byActor).Error; err != nil {
		return false, err
	}
	if byActor >= leaderInviteDailyLimit {
		return false, nil
	}
	// Join the invitation instead of putting destination email in conversion audits.
	if err := tx.Model(&models.AuditLog{}).Joins("JOIN invitations ON invitations.id = audit_logs.target_id AND audit_logs.target_type = 'invitation'").Where("invitations.email = ? AND audit_logs.action IN ? AND audit_logs.at > now() - interval '24 hours'", email, mailKinds).Count(&byEmail).Error; err != nil {
		return false, err
	}
	return byEmail < leaderInviteDailyLimit, nil
}

// lockGuestVerifyAddress takes the transaction-scoped lock that serializes one
// address's guest verification count. It takes a two-key lock, so it never
// collides with the one-key ids in advisory_locks.go.
//
// Why the key is exact: guest_signups.email is citext, and citext equality
// compares str_tolower of each side under the database default collation, byte
// for byte. citext defines no lower(), so lower(x::citext) is lower(text), the
// same str_tolower under the same default collation. hashtext of a
// deterministic-collation text (every database default is deterministic)
// hashes those bytes. So any two addresses the count's `email = ?` treats as
// equal take the same key, whatever Go's ToLower does. Never add a COLLATE to
// either side, and never hash the Go string in place of the SQL fold. Two
// unequal addresses can share a 32-bit key; that only serializes them,
// because the count still matches by email.
func lockGuestVerifyAddress(tx *gorm.DB, email string) error {
	return tx.Exec("SELECT pg_advisory_xact_lock(?, hashtext(lower(?::citext)))",
		guestVerifyLockClass, mail.NormalizeAddress(email)).Error
}

// guestVerifyAllowedIn is the guest verification limit: 3 in the last hour and
// 10 in the last day for one address (10.1). Each stored signup is one
// verification mail queued in the same transaction, so the rows are the
// counter. The lock is held until the caller's COMMIT or ROLLBACK. At the
// database's default READ COMMITTED, a waiter's count, its first data
// statement after the lock, takes its snapshot after the holder's commit and
// sees the holder's row; a rolled-back insert spends nothing.
//
// The window is read after the lock is taken, on the Go clock the caller uses
// to stamp CreatedAt, so a waiter judges it as of its serialized count.
//
// Lock order: the guest transaction takes exactly this one advisory lock. Its
// insert then takes FOR KEY SHARE on one event_occurrences row through
// fk_guest_signups_occurrence, the table's only foreign key. The jobs insert
// (ON CONFLICT (unique_key) with a NULL key) waits on no domain row. Only
// guest transactions take this class, and a waiter holds nothing else. Event
// deletion, account erasure, redemption and mail preparation can hold
// occurrence or signup locks that this transaction waits on, but none of them
// waits for this class or for anything this transaction holds: its
// uncommitted row is invisible to them, and its KEY SHARE is taken last before
// commit. So the lock adds no wait cycle. Adding a second advisory lock, or an
// occurrence lock, inside this transaction requires rechecking this. The
// pre-existing race between this insert's FK check and event deletion is
// unchanged.
func guestVerifyAllowedIn(tx *gorm.DB, email string) (bool, error) {
	email = mail.NormalizeAddress(email)
	if err := lockGuestVerifyAddress(tx, email); err != nil {
		return false, err
	}
	now := time.Now().UTC()
	var counts struct{ HourCount, DayCount int64 }
	if err := tx.Raw(`SELECT count(*) FILTER (WHERE created_at > ?) AS hour_count, count(*) AS day_count
FROM guest_signups WHERE email = ? AND created_at > ?`,
		now.Add(-time.Hour), email, now.Add(-24*time.Hour)).Scan(&counts).Error; err != nil {
		return false, err
	}
	return counts.HourCount < guestVerifyPerHour && counts.DayCount < guestVerifyPerDay, nil
}

// refundLoginAttempts gives each key back the attempt reserveLoginAttempt
// counted, after a correct proof. It never clears failures other requests
// recorded.
func (a *App) refundLoginAttempts(ctx context.Context, keys []throttleKey) error {
	return a.DB.WithContext(ctx).Exec(`UPDATE login_throttles
SET failures = GREATEST(failures - 1, 0) WHERE key IN ?`, throttleKeyStrings(keys)).Error
}

// clearLoginKey forgets one key's counter: a successful sign-in clears the
// email's, never the IP's, so one working account cannot reset the IP budget.
func (a *App) clearLoginKey(ctx context.Context, key throttleKey) error {
	return a.DB.WithContext(ctx).Where("key = ?", string(key)).Delete(&models.LoginThrottle{}).Error
}
