package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	netmail "net/mail"
	"strings"
	"time"

	"fold/internal/audit"
	"fold/internal/jobs"
	"fold/internal/mail"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ---------------------------------------------------------------------------
// Auth endpoints
// ---------------------------------------------------------------------------

func (a *App) apiGetCSRF(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"csrfToken": c.GetString("csrfToken")})
}

func (a *App) apiLogin(c *gin.Context) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.Email) == "" || body.Password == "" {
		jsonError(c, http.StatusBadRequest, "email and password are required")
		return
	}
	email := mail.NormalizeAddress(body.Email)
	keys := []throttleKey{loginEmailKey(email), loginIPKey(c.ClientIP())}
	ctx := c.Request.Context()
	// The attempt is counted before the password check, atomically per key,
	// so a burst of parallel requests can't all pass the check before any
	// failure is recorded (#90).
	allowed, err := a.reserveLoginAttempt(ctx, keys)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not sign in")
		return
	}
	if !allowed {
		jsonError(c, http.StatusTooManyRequests, "too many sign-in attempts; try again in 15 minutes")
		return
	}
	// Every failure gets the same answer, and costs one bcrypt comparison,
	// whether or not the email has an account (10.1).
	var user *models.User
	var found models.User
	// Find, not First: a miss is normal here and shouldn't log the address.
	if res := a.DB.Where("email = ?", email).Limit(1).Find(&found); res.Error != nil {
		jsonError(c, http.StatusInternalServerError, "could not sign in")
		return
	} else if res.RowsAffected == 1 {
		user = &found
	}
	waiting := user != nil && user.Status == models.UserPendingConsent && user.AccountType == models.AccountTeen
	if !user.CheckPassword(body.Password) || (!user.CanSignIn() && !waiting) {
		if err := a.lockLoginKeysAtLimit(ctx, keys); err != nil {
			jsonError(c, http.StatusInternalServerError, "could not sign in")
			return
		}
		jsonError(c, http.StatusUnauthorized, "invalid email or password")
		return
	}
	// A success isn't a failure: give the IP back its attempt.
	a.DB.Exec(`UPDATE login_throttles SET failures = GREATEST(failures - 1, 0) WHERE key = ?`, string(keys[1]))
	// Success clears the email's counter. The IP counter stays, so one
	// working account can't be used to reset it.
	a.DB.Where("key = ?", string(keys[0])).Delete(&models.LoginThrottle{})
	if err := a.signInSession(c, user); err != nil {
		jsonError(c, http.StatusInternalServerError, "session error")
		return
	}
	if waiting {
		// Only a verified password earns this explanation; no account status
		// is disclosed for an unknown address or a wrong password.
		jsonError(c, http.StatusForbidden, "your account is waiting for consent")
		return
	}
	c.JSON(http.StatusOK, sessionJSON{User: a.toUserJSON(user), CanCompose: a.canComposeOrLog(user)})
}

// validEmail checks an address before it is stored as a login.
func validEmail(s string) bool {
	if len(s) > 254 || !strings.Contains(s, "@") {
		return false
	}
	addr, err := netmail.ParseAddress(s)
	return err == nil && addr.Address == s
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
// cleanup.login_throttles job runs it; hits no longer pay for the cleanup.
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

// reauthenticate shares one per-user budget across authenticated password
// proofs and the IP budget with sign-in. Reserve before bcrypt so parallel
// attempts cannot bypass the limits. Only a correct proof refunds its attempt;
// it must not clear failures from other requests.
func (a *App) reauthenticate(c *gin.Context, u *models.User, password string) bool {
	keys := []throttleKey{loginUserKey(u.ID), loginIPKey(c.ClientIP())}
	ctx := c.Request.Context()
	allowed, err := a.reserveLoginAttempt(ctx, keys)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not verify password")
		return false
	}
	if !allowed {
		jsonError(c, http.StatusTooManyRequests, "too many sign-in attempts; try again in 15 minutes")
		return false
	}
	if !u.CheckPassword(password) {
		if err := a.lockLoginKeysAtLimit(ctx, keys); err != nil {
			jsonError(c, http.StatusInternalServerError, "could not verify password")
			return false
		}
		jsonError(c, http.StatusBadRequest, "current password is incorrect")
		return false
	}
	if err := a.DB.WithContext(ctx).Exec(`UPDATE login_throttles
SET failures = GREATEST(failures - 1, 0) WHERE key IN ?`, throttleKeyStrings(keys)).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not verify password")
		return false
	}
	return true
}

// apiChangePassword lets the current user replace their password.
// This browser stays logged in; other sessions for the account do not.
func (a *App) apiChangePassword(c *gin.Context) {
	u := apiUser(c)
	var body struct {
		Current  string `json:"current"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Current == "" || body.Password == "" {
		jsonError(c, http.StatusBadRequest, "current and new passwords are required")
		return
	}
	if !checkNewPassword(c, body.Password) {
		return
	}
	if !a.reauthenticate(c, u, body.Current) {
		return
	}
	var prepared models.User
	if err := prepared.SetPassword(body.Password); err != nil {
		jsonError(c, http.StatusInternalServerError, "could not change password")
		return
	}
	var fresh models.User
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		locked, err := lockCurrentCredentials(tx, u)
		if err != nil {
			return err
		}
		if err := replacePasswordIn(tx, locked, *prepared.PasswordHash); err != nil {
			return err
		}
		if err := audit.Write(tx, audit.Entry{Actor: &u.ID, Action: audit.PasswordChanged,
			TargetType: "user", TargetID: u.ID, IP: c.ClientIP()}); err != nil {
			return err
		}
		fresh = *locked
		return nil
	})
	if errors.Is(err, errSecuritySession) {
		jsonError(c, http.StatusUnauthorized, "unauthorized")
		return
	}
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not change password")
		return
	}
	session, err := a.Store.Get(c.Request, sessionName)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "session error")
		return
	}
	session.Values["userID"] = fresh.ID
	session.Values["sessionVersion"] = fresh.SessionVersion
	if err := session.Save(c.Request, c.Writer); err != nil {
		jsonError(c, http.StatusInternalServerError, "session error")
		return
	}
	c.JSON(http.StatusOK, gin.H{})
}

// apiLogout ends the session everywhere: it bumps the account's
// logout_version, so this cookie and any copy of it, and every other
// device's session for the account, stop working (one epoch per account;
// M7.2 slice b). It leaves session_version, the credential epoch, alone, so
// emailed links and push devices survive an ordinary sign-out. The cookie
// is also expired (MaxAge -1), so the browser drops it along with its CSRF
// token. The SPA then reloads. A body of {pushEndpoint} deletes this
// browser's push device in the same request (M3.13b): a separate call after
// logout would have no CSRF token.
func (a *App) apiLogout(c *gin.Context) {
	var body struct {
		PushEndpoint string `json:"pushEndpoint"`
	}
	if c.Request.ContentLength != 0 {
		_ = c.ShouldBindJSON(&body) // the body is optional
	}
	a.endPushDeviceAtLogout(body.PushEndpoint)
	bumpErr := a.endSessionsAtLogout(c)
	session, _ := a.Store.Get(c.Request, sessionName)
	session.Values = map[any]any{}
	opts := *a.Store.Options
	opts.MaxAge = -1
	session.Options = &opts
	_ = session.Save(c.Request, c.Writer)
	if bumpErr != nil {
		log.Printf("logout: ending sessions: %v", bumpErr)
		jsonError(c, http.StatusInternalServerError, "could not sign out everywhere")
		return
	}
	c.JSON(http.StatusOK, gin.H{})
}

// endSessionsAtLogout bumps logout_version for the signed-in account, so a
// copied cookie can't be replayed after logout. A cookie that is already
// stale changes nothing.
func (a *App) endSessionsAtLogout(c *gin.Context) error {
	user, ok := a.authenticatedSessionUser(c)
	if !ok {
		return nil
	}
	return a.DB.Model(&models.User{}).Where("id = ? AND logout_version = ?", user.ID, user.LogoutVersion).
		Update("logout_version", gorm.Expr("logout_version + 1")).Error
}

func (a *App) apiMe(c *gin.Context) {
	me := apiUser(c)
	c.JSON(http.StatusOK, sessionJSON{User: a.toUserJSON(me), CanCompose: a.canComposeOrLog(me)})
}

// canComposeOrLog says whether the nav offers Messages (M3.4c). A failed
// read only hides the link; the composer's own routes check again.
func (a *App) canComposeOrLog(me *models.User) bool {
	// Admins always can; skip the two reads on every page load.
	if policy.IsAdmin(asPerson(me)) {
		return true
	}
	ok, err := a.canCompose(me)
	if err != nil {
		log.Printf("can compose for user %d: %v", me.ID, err)
	}
	return ok
}

// apiMyGroups lists the groups the current user is an active member of.
func (a *App) apiMyGroups(c *gin.Context) {
	u := apiUser(c)
	var memberships []models.Membership
	a.DB.Preload("Group").Where("user_id = ? AND status = ?", u.ID, models.MembershipActive).
		Order("joined_at").Find(&memberships)
	groups := make([]models.Group, 0, len(memberships))
	roles := make(map[uint]string, len(memberships))
	for i := range memberships {
		// Include pending and archived groups this person still belongs to.
		// The directory (GET /groups) stays limited to active groups.
		if memberships[i].Group.ID == 0 {
			continue
		}
		groups = append(groups, memberships[i].Group)
		roles[memberships[i].GroupID] = memberships[i].Role
	}
	c.JSON(http.StatusOK, gin.H{"groups": a.viewerGroupList(groups, roles)})
}

// ---------------------------------------------------------------------------
// Password reset (M1.2, 10.1)
// ---------------------------------------------------------------------------

// resetRequestMessage is the uniform answer: the same for every address,
// known or not, so the endpoint can't be used to enumerate accounts.
const resetRequestMessage = "If that email has an account, we've sent a link"

// Password-reset requests also use the shared hourly/daily target mail caps.
const (
	resetIPLimit = 10
	resetWindow  = time.Hour
)

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

// apiRequestPasswordReset starts a password reset. It always answers 200
// with the uniform message. A known address queues a single-use link valid
// for 24 hours from delivery preparation. Inside the IP/address budget, both
// known and unknown addresses pay a dummy bcrypt comparison; exhausted budgets return before
// account lookup or bcrypt, independent of account existence (10.1).
func (a *App) apiRequestPasswordReset(c *gin.Context) {
	var body struct {
		Email string `json:"email"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.Email) == "" {
		jsonError(c, http.StatusBadRequest, "email is required")
		return
	}
	email := mail.NormalizeAddress(body.Email)
	ctx := c.Request.Context()
	ipOK, err := a.throttleAllow(ctx, pwresetIPKey(c.ClientIP()), resetIPLimit, resetWindow)
	addrOK := true
	if err == nil {
		addrOK, err = a.targetAllow(ctx, pwresetAddrKey(email))
	}
	if err != nil || !ipOK || !addrOK {
		log.Printf("password reset rate limited: ip=%s err=%v", c.ClientIP(), err)
		c.JSON(http.StatusOK, gin.H{"message": resetRequestMessage})
		return
	}

	var user models.User
	// Only real accounts get a reset: guests (M1.4) have no password to
	// reset and disabled accounts can't sign in. Both get the same
	// answer as an unknown address, so guest status isn't revealed (DEC-53).
	found := a.DB.Where("email = ? AND status = ?", email, models.UserActive).
		Limit(1).Find(&user).RowsAffected == 1
	// Both paths pay the same password-check cost, and neither waits on
	// the email: the job sends it later, so response time doesn't
	// reveal whether the account exists (#93).
	(&models.User{}).CheckPassword("not a real password")
	if !found {
		c.JSON(http.StatusOK, gin.H{"message": resetRequestMessage})
		return
	}
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		if err := lockContacts(tx); err != nil {
			return err
		}
		// Recheck the address and account while credential changes are excluded.
		var fresh models.User
		if err := tx.Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).First(&fresh, user.ID).Error; err != nil {
			return err
		}
		if fresh.Status != models.UserActive || mail.NormalizeAddress(fresh.Email) != email {
			return errTokenInvalid
		}
		if _, err := jobs.Enqueue(tx, kindPasswordResetMail, userMailJob{UserID: user.ID}, time.Now(), "", directMailAttempts); err != nil {
			return err
		}
		return audit.Write(tx, audit.Entry{Actor: &user.ID, Action: audit.PasswordResetRequested,
			TargetType: "user", TargetID: user.ID, IP: c.ClientIP()})
	})
	if err != nil {
		// Logged, not leaked: an error status here would tell the caller
		// the account exists.
		log.Printf("password reset for user %d: %s", user.ID, jobs.CleanError(err))
		c.JSON(http.StatusOK, gin.H{"message": resetRequestMessage})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": resetRequestMessage})
}

// apiConfirmPasswordReset redeems a reset token and sets the new password.
// Unknown, expired, or already-used tokens all get the same answer, and a
// successful reset signs the person in.
func (a *App) apiConfirmPasswordReset(c *gin.Context) {
	var body struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Token == "" || body.Password == "" {
		jsonError(c, http.StatusBadRequest, "token and password are required")
		return
	}
	if !checkNewPassword(c, body.Password) {
		return
	}
	var fresh models.User
	passwordHash, err := prepareTokenPassword(a.DB.WithContext(c.Request.Context()), models.TokenPasswordReset, body.Token, body.Password)
	if err == nil {
		err = a.DB.Transaction(func(tx *gorm.DB) error {
			u, _, err := lockAndConsumeAccountToken(tx, models.TokenPasswordReset, body.Token)
			if err != nil {
				return err
			}
			if err := replacePasswordIn(tx, u, passwordHash); err != nil {
				return err
			}
			if err := audit.Write(tx, audit.Entry{Actor: &u.ID, Action: audit.PasswordResetCompleted,
				TargetType: "user", TargetID: u.ID, IP: c.ClientIP()}); err != nil {
				return err
			}
			fresh = *u
			return nil
		})
	}
	if errors.Is(err, errTokenInvalid) {
		jsonError(c, http.StatusBadRequest, "that link is invalid or has expired")
		return
	}
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not reset password")
		return
	}
	if err := a.signInSession(c, &fresh); err != nil {
		jsonError(c, http.StatusInternalServerError, "session error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"user": a.toUserJSON(&fresh)})
}

// signInSession stores only an eligible login. Password setup/reset can also
// reach here for pending teens: clear authentication (including a previous
// account's login). Both branches drop the CSRF token; the next request mints
// a fresh one, so public browsing keeps working.
func (a *App) signInSession(c *gin.Context, u *models.User) error {
	session, err := a.Store.Get(c.Request, sessionName)
	if err != nil {
		return err
	}
	// A token fixed or leaked before sign-in must not outlive it: drop it, and
	// the next request mints a fresh one (csrfToken; M7.2 slice b).
	delete(session.Values, csrfSessionKey)
	if u.CanSignIn() {
		session.Values["userID"] = u.ID
		session.Values["sessionVersion"] = u.SessionVersion
		session.Values["logoutVersion"] = u.LogoutVersion
	} else {
		delete(session.Values, "userID")
		delete(session.Values, "sessionVersion")
		delete(session.Values, "logoutVersion")
	}
	return session.Save(c.Request, c.Writer)
}
