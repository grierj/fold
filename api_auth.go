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
	keys := []string{"email:" + email, "ip:" + c.ClientIP()}
	ctx := c.Request.Context()
	locked, err := a.loginLocked(ctx, keys)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not sign in")
		return
	}
	if locked {
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
	if !user.CheckPassword(body.Password) || !user.CanSignIn() {
		if err := a.noteLoginFailure(ctx, keys); err != nil {
			jsonError(c, http.StatusInternalServerError, "could not sign in")
			return
		}
		jsonError(c, http.StatusUnauthorized, "invalid email or password")
		return
	}
	// Success clears the email's counter. The IP counter stays, so one
	// working account can't be used to reset it.
	a.DB.Where("key = ?", keys[0]).Delete(&models.LoginThrottle{})
	session, err := a.Store.Get(c.Request, sessionName)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "session error")
		return
	}
	session.Values["userID"] = user.ID
	session.Values["sessionVersion"] = user.SessionVersion
	if err := session.Save(c.Request, c.Writer); err != nil {
		jsonError(c, http.StatusInternalServerError, "session error")
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

func (a *App) loginLocked(ctx context.Context, keys []string) (bool, error) {
	var n int64
	err := a.DB.WithContext(ctx).Model(&models.LoginThrottle{}).
		Where("key IN ? AND locked_until > now()", keys).Count(&n).Error
	return n > 0, err
}

// noteLoginFailure counts one failure for each key. A key that reaches its
// limit within the window is locked for 15 minutes and starts over.
func (a *App) noteLoginFailure(ctx context.Context, keys []string) error {
	window := fmt.Sprintf("%d seconds", int(loginWindow.Seconds()))
	return a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, key := range keys {
			err := tx.Exec(`
INSERT INTO login_throttles (key, failures, window_start) VALUES (?, 1, now())
ON CONFLICT (key) DO UPDATE SET
  failures = CASE WHEN login_throttles.window_start < now() - ?::interval
                  THEN 1 ELSE login_throttles.failures + 1 END,
  window_start = CASE WHEN login_throttles.window_start < now() - ?::interval
                      THEN now() ELSE login_throttles.window_start END`,
				key, window, window).Error
			if err != nil {
				return err
			}
			err = tx.Exec(`UPDATE login_throttles
SET locked_until = now() + ?::interval, failures = 0, window_start = now()
WHERE key = ? AND failures >= ?`, window, key, failureLimit(key)).Error
			if err != nil {
				return err
			}
		}
		// Old rows carry no information; keep the table small.
		return tx.Exec(`DELETE FROM login_throttles
WHERE window_start < now() - interval '1 day' AND (locked_until IS NULL OR locked_until < now())`).Error
	})
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
	out := make([]groupSummaryJSON, 0, len(memberships))
	for i := range memberships {
		// Include pending and archived groups this person still belongs to.
		// The directory (GET /groups) stays limited to active groups.
		if memberships[i].Group.ID == 0 {
			continue
		}
		out = append(out, a.groupSummary(&memberships[i].Group))
	}
	c.JSON(http.StatusOK, gin.H{"groups": out})
}

// ---------------------------------------------------------------------------
// Password reset (M1.2, 10.1)
// ---------------------------------------------------------------------------

// resetRequestMessage is the uniform answer: the same for every address,
// known or not, so the endpoint can't be used to enumerate accounts.
const resetRequestMessage = "If that email has an account, we've sent a link"

// resetThrottle limits: per client IP and per target address, per hour.
const (
	resetIPLimit      = 10
	resetAddressLimit = 5
	resetWindow       = time.Hour
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
	ipOK, err := a.throttleAllow(ctx, "pwreset:ip:"+c.ClientIP(), resetIPLimit, resetWindow)
	addrOK := true
	if err == nil {
		addrOK, err = a.throttleAllow(ctx, "pwreset:addr:"+email, resetAddressLimit, resetWindow)
	}
	throttled := err != nil || !ipOK || !addrOK

	var user models.User
	found := a.DB.Where("email = ?", email).Limit(1).Find(&user).RowsAffected == 1
	if !found || throttled {
		// Same cost as a real request's password check, and the same
		// answer: nothing here reveals whether the account exists.
		(&models.User{}).CheckPassword("not a real password")
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
		jsonError(c, http.StatusInternalServerError, "could not start password reset")
		return
	}
	if err := a.sendPasswordResetEmail(email, raw); err != nil {
		// The token is stored; the send failure is logged, not leaked.
		log.Printf("password reset email to %s: %v", email, err)
	}
	c.JSON(http.StatusOK, gin.H{"message": resetRequestMessage})
}

// sendPasswordResetEmail sends the single-use reset link (C6: plain text).
func (a *App) sendPasswordResetEmail(to, raw string) error {
	link := a.emailBaseURL() + "/reset-password?token=" + raw
	return a.Mailer.Send(context.Background(), foldmail.Message{
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

// signInSession stores the login in the session cookie.
func (a *App) signInSession(c *gin.Context, u *models.User) error {
	session, err := a.Store.Get(c.Request, sessionName)
	if err != nil {
		return err
	}
	session.Values["userID"] = u.ID
	session.Values["sessionVersion"] = u.SessionVersion
	return session.Save(c.Request, c.Writer)
}
