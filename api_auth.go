package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"fold/internal/audit"
	foldmail "fold/internal/mail"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
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
	email := normalizeEmail(body.Email)
	keys := []string{"email:" + email, "ip:" + rateLimitIP(c.ClientIP())}
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
	a.DB.Exec(`UPDATE login_throttles SET failures = GREATEST(failures - 1, 0) WHERE key = ?`, keys[1])
	// Success clears the email's counter. The IP counter stays, so one
	// working account can't be used to reset it.
	a.DB.Where("key = ?", keys[0]).Delete(&models.LoginThrottle{})
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
	c.JSON(http.StatusOK, gin.H{"user": a.toUserJSON(user)})
}

// normalizeEmail trims and lower-cases an address for lookups and throttle
// keys. The column is citext, so lookups match any case anyway.
func normalizeEmail(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// validEmail checks an address before it is stored as a login.
func validEmail(s string) bool {
	if len(s) > 254 || !strings.Contains(s, "@") {
		return false
	}
	addr, err := mail.ParseAddress(s)
	return err == nil && addr.Address == s
}

// Login throttling (10.1): ten failures for one email lock that email for
// 15 minutes from any IP; fifty failures from one IP lock that IP.
const (
	emailFailureLimit = 10
	ipFailureLimit    = 50
	loginWindow       = 15 * time.Minute
)

func failureLimit(key string) int {
	if strings.HasPrefix(key, "ip:") {
		return ipFailureLimit
	}
	return emailFailureLimit
}

// reserveLoginAttempt counts one attempt for each key before the password
// is checked, and reports whether every key is still allowed. Each key's
// row is incremented with one upsert, so parallel requests serialize on it
// and at most the limit pass. A locked key, or one already past its limit,
// is refused; a key that goes past its limit is locked for 15 minutes.
func (a *App) reserveLoginAttempt(ctx context.Context, keys []string) (bool, error) {
	window := fmt.Sprintf("%d seconds", int(loginWindow.Seconds()))
	allowed := true
	err := a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, key := range keys {
			var row struct {
				Failures int
				Locked   bool
			}
			err := tx.Raw(`
INSERT INTO login_throttles (key, failures, window_start) VALUES (?, 1, now())
ON CONFLICT (key) DO UPDATE SET
  failures = CASE WHEN login_throttles.window_start < now() - ?::interval
                  THEN 1 ELSE login_throttles.failures + 1 END,
  window_start = CASE WHEN login_throttles.window_start < now() - ?::interval
                      THEN now() ELSE login_throttles.window_start END
RETURNING failures, COALESCE(locked_until > now(), false) AS locked`,
				key, window, window).Scan(&row).Error
			if err != nil {
				return err
			}
			switch {
			case row.Locked:
				allowed = false
			case row.Failures > failureLimit(key):
				allowed = false
				if err := tx.Exec(`UPDATE login_throttles
SET locked_until = now() + ?::interval, failures = 0, window_start = now()
WHERE key = ?`, window, key).Error; err != nil {
					return err
				}
			}
		}
		// Old rows carry no information; keep the table small.
		return tx.Exec(`DELETE FROM login_throttles
WHERE window_start < now() - interval '1 day' AND (locked_until IS NULL OR locked_until < now())`).Error
	})
	return allowed, err
}

// lockLoginKeysAtLimit runs after a failed attempt (already counted by
// reserveLoginAttempt): a key that has reached its limit within the window
// is locked for 15 minutes and starts over.
func (a *App) lockLoginKeysAtLimit(ctx context.Context, keys []string) error {
	window := fmt.Sprintf("%d seconds", int(loginWindow.Seconds()))
	for _, key := range keys {
		err := a.DB.WithContext(ctx).Exec(`UPDATE login_throttles
SET locked_until = now() + ?::interval, failures = 0, window_start = now()
WHERE key = ? AND failures >= ?`, window, key, failureLimit(key)).Error
		if err != nil {
			return err
		}
	}
	return nil
}

// setPassword stores a new bcrypt hash, activates an invited account, and
// ends existing sessions.
func (a *App) setPassword(u *models.User, plaintext string) error {
	if err := u.SetPassword(plaintext); err != nil {
		return err
	}
	return a.DB.Model(u).Updates(map[string]interface{}{
		"password_hash":   *u.PasswordHash,
		"status":          gorm.Expr("CASE WHEN status = ? THEN ? ELSE status END", models.UserInvited, models.UserActive),
		"session_version": gorm.Expr("session_version + 1"),
	}).Error
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
	if len(body.Password) < minPasswordLength {
		jsonError(c, http.StatusBadRequest,
			"password must be at least 10 characters")
		return
	}
	if !u.CheckPassword(body.Current) {
		jsonError(c, http.StatusBadRequest, "current password is incorrect")
		return
	}
	if err := a.setPassword(u, body.Password); err != nil {
		jsonError(c, http.StatusInternalServerError, "could not change password")
		return
	}
	var fresh models.User
	if err := a.DB.First(&fresh, u.ID).Error; err != nil {
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

// apiLogout ends the session: the cookie is expired (MaxAge -1), so the
// browser drops it along with its CSRF token. The SPA then reloads.
func (a *App) apiLogout(c *gin.Context) {
	session, _ := a.Store.Get(c.Request, sessionName)
	session.Values = map[any]any{}
	opts := *a.Store.Options
	opts.MaxAge = -1
	session.Options = &opts
	_ = session.Save(c.Request, c.Writer)
	c.JSON(http.StatusOK, gin.H{})
}

func (a *App) apiMe(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"user": a.toUserJSON(apiUser(c))})
}

// apiMyGroups lists the groups the current user is an active member of.
func (a *App) apiMyGroups(c *gin.Context) {
	u := apiUser(c)
	var memberships []models.Membership
	a.DB.Preload("Group").Where("user_id = ? AND status = ?", u.ID, "active").
		Order("joined_at").Find(&memberships)
	groups := make([]models.Group, 0, len(memberships))
	for i := range memberships {
		// Include pending and archived groups this person still belongs to.
		// The directory (GET /groups) stays limited to active groups.
		if memberships[i].Group.ID == 0 {
			continue
		}
		groups = append(groups, memberships[i].Group)
	}
	c.JSON(http.StatusOK, gin.H{"groups": a.groupSummaries(groups)})
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
func (a *App) throttleAllow(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	var failures int
	windowArg := fmt.Sprintf("%d seconds", int(window.Seconds()))
	err := a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Raw(`
INSERT INTO login_throttles (key, failures, window_start) VALUES (?, 1, now())
ON CONFLICT (key) DO UPDATE SET
  failures = CASE WHEN login_throttles.window_start < now() - ?::interval
                  THEN 1 ELSE login_throttles.failures + 1 END,
  window_start = CASE WHEN login_throttles.window_start < now() - ?::interval
                      THEN now() ELSE login_throttles.window_start END
RETURNING failures`, key, windowArg, windowArg).Scan(&failures).Error; err != nil {
			return err
		}
		return tx.Exec(`DELETE FROM login_throttles
WHERE window_start < now() - interval '1 day' AND (locked_until IS NULL OR locked_until < now())`).Error
	})
	return failures <= limit, err
}

// Per-target email limits (PLAN 10.6 defaults, as for guest verification
// emails): a door that mails an address someone else typed sends it at most
// 3 per hour and 10 per day, whoever asks and from wherever (#91, #92).
const (
	targetHourLimit = 3
	targetDayLimit  = 10
)

// targetAllow counts one send to key (a prefix plus an address or user id)
// in both the hourly and the daily window and reports whether both stay
// under their limits.
func (a *App) targetAllow(ctx context.Context, key string) (bool, error) {
	hourOK, err := a.throttleAllow(ctx, key+":h", targetHourLimit, time.Hour)
	if err != nil {
		return false, err
	}
	dayOK, err := a.throttleAllow(ctx, key+":d", targetDayLimit, 24*time.Hour)
	return hourOK && dayOK, err
}

// apiRequestPasswordReset starts a password reset. It always answers 200
// with the uniform message. A known address gets a single-use link valid
// for 24 hours; an unknown one costs a dummy bcrypt comparison so the
// timing doesn't reveal whether the account exists (10.1).
func (a *App) apiRequestPasswordReset(c *gin.Context) {
	var body struct {
		Email string `json:"email"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.Email) == "" {
		jsonError(c, http.StatusBadRequest, "email is required")
		return
	}
	email := normalizeEmail(body.Email)
	ctx := c.Request.Context()
	ipOK, err := a.throttleAllow(ctx, "pwreset:ip:"+rateLimitIP(c.ClientIP()), resetIPLimit, resetWindow)
	addrOK := true
	if err == nil {
		addrOK, err = a.targetAllow(ctx, "pwreset:addr:"+email)
	}
	throttled := err != nil || !ipOK || !addrOK

	var user models.User
	// Only real accounts get a reset: guests (M1.4) have no password to
	// reset and disabled accounts can't sign in. Both get the same
	// answer as an unknown address, so guest status isn't revealed (DEC-53).
	found := a.DB.Where("email = ? AND status = ?", email, models.UserActive).
		Limit(1).Find(&user).RowsAffected == 1
	// Both paths pay the same password-check cost, and neither waits on
	// the email: the send runs in the background, so response time doesn't
	// reveal whether the account exists (#93).
	(&models.User{}).CheckPassword("not a real password")
	if !found || throttled {
		c.JSON(http.StatusOK, gin.H{"message": resetRequestMessage})
		return
	}
	var raw string
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		raw, err = createAccountToken(tx, models.TokenPasswordReset, user.ID, nil, tokenTTL)
		if err != nil {
			return err
		}
		return audit.Write(tx, audit.Entry{Actor: &user.ID, Action: audit.PasswordResetRequested,
			TargetType: "user", TargetID: user.ID, IP: c.ClientIP()})
	})
	if err != nil {
		// Logged, not leaked: an error status here would tell the caller
		// the account exists.
		log.Printf("password reset for user %d: %v", user.ID, err)
		c.JSON(http.StatusOK, gin.H{"message": resetRequestMessage})
		return
	}
	a.runInBackground(func() {
		ctx, cancel := context.WithTimeout(context.Background(), resetMailTimeout)
		defer cancel()
		if err := a.sendPasswordResetEmail(ctx, email, raw); err != nil {
			// The token is stored; the send failure is logged, not leaked.
			log.Printf("password reset email to %s: %v", email, err)
		}
	})
	c.JSON(http.StatusOK, gin.H{"message": resetRequestMessage})
}

// resetMailTimeout bounds a background reset send, so a hung SMTP server
// can't pile up goroutines.
const resetMailTimeout = 30 * time.Second

// runInBackground runs f without making the response wait for it. A panic
// in f is logged and recovered, so it can't take the server down.
func (a *App) runInBackground(f func()) {
	safe := func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("background task panicked: %v", r)
			}
		}()
		f()
	}
	if a.background != nil {
		a.background(safe)
		return
	}
	go safe()
}

// sendPasswordResetEmail sends the single-use reset link (C6: plain text).
func (a *App) sendPasswordResetEmail(ctx context.Context, to, raw string) error {
	link := a.emailBaseURL() + "/reset-password?token=" + raw
	return a.Mailer.Send(ctx, foldmail.Message{
		To:      to,
		Subject: "Reset your Fold password",
		Text: fmt.Sprintf(`Someone asked to reset the password for this email address at %s.

Reset it here (the link works once and expires in 24 hours):
%s

If that wasn't you, you can ignore this email — your password won't change.`,
			a.siteName(), link),
	})
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
	if len(body.Password) < minPasswordLength {
		jsonError(c, http.StatusBadRequest, "password must be at least 10 characters")
		return
	}
	var userID uint
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		tok, err := consumeAccountToken(tx, models.TokenPasswordReset, body.Token)
		if err != nil {
			return err
		}
		var u models.User
		if err := tx.First(&u, tok.UserID).Error; err != nil || u.Status == models.UserDisabled {
			return errTokenInvalid
		}
		if err := u.SetPassword(body.Password); err != nil {
			return err
		}
		userID = u.ID
		if err := tx.Model(&u).Updates(map[string]any{
			"password_hash":   *u.PasswordHash,
			"status":          gorm.Expr("CASE WHEN status = ? THEN ? ELSE status END", models.UserInvited, models.UserActive),
			"session_version": gorm.Expr("session_version + 1"),
		}).Error; err != nil {
			return err
		}
		return audit.Write(tx, audit.Entry{Actor: &u.ID, Action: audit.PasswordResetCompleted,
			TargetType: "user", TargetID: u.ID, IP: c.ClientIP()})
	})
	if errors.Is(err, errTokenInvalid) {
		jsonError(c, http.StatusBadRequest, "that link is invalid or has expired")
		return
	}
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not reset password")
		return
	}
	var fresh models.User
	if err := a.DB.First(&fresh, userID).Error; err != nil {
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
// account's login), while keeping CSRF so public browsing remains available.
func (a *App) signInSession(c *gin.Context, u *models.User) error {
	session, err := a.Store.Get(c.Request, sessionName)
	if err != nil {
		return err
	}
	if u.CanSignIn() {
		session.Values["userID"] = u.ID
		session.Values["sessionVersion"] = u.SessionVersion
	} else {
		delete(session.Values, "userID")
		delete(session.Values, "sessionVersion")
	}
	return session.Save(c.Request, c.Writer)
}
