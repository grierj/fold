package main

import (
	"errors"
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
		fail(c, errLockedOut)
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
		fail(c, errBadCredentials)
		return
	}
	// A success isn't a failure: give the IP back its attempt, and clear the
	// email's counter. The IP counter otherwise stays, so one working account
	// can't be used to reset it.
	if err := a.refundLoginAttempts(ctx, []throttleKey{loginIPKey(c.ClientIP())}); err != nil {
		jsonError(c, http.StatusInternalServerError, "could not sign in")
		return
	}
	if err := a.clearLoginKey(ctx, loginEmailKey(email)); err != nil {
		jsonError(c, http.StatusInternalServerError, "could not sign in")
		return
	}
	if err := a.signInSession(c, user); err != nil {
		jsonError(c, http.StatusInternalServerError, "session error")
		return
	}
	if waiting {
		// Only a verified password earns this explanation; no account status
		// is disclosed for an unknown address or a wrong password.
		fail(c, errConsentRequired)
		return
	}
	c.JSON(http.StatusOK, sessionJSON{User: a.toUserJSON(user), CanCompose: a.canComposeOrLog(user)})
}

func validEmail(s string) bool {
	if len(s) > 254 || !strings.Contains(s, "@") {
		return false
	}
	addr, err := netmail.ParseAddress(s)
	return err == nil && addr.Address == s
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
		fail(c, errLockedOut)
		return false
	}
	if !u.CheckPassword(password) {
		if err := a.lockLoginKeysAtLimit(ctx, keys); err != nil {
			jsonError(c, http.StatusInternalServerError, "could not verify password")
			return false
		}
		fail(c, errWrongPassword)
		return false
	}
	if err := a.refundLoginAttempts(ctx, keys); err != nil {
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
	if answerStaleSession(c, err) {
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
	setSessionLogin(session, &fresh)
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

func (a *App) apiMyGroups(c *gin.Context) {
	u := apiUser(c)
	var memberships []models.Membership
	if err := a.DB.Preload("Group").Where("user_id = ? AND status = ?", u.ID, models.MembershipActive).
		Order("joined_at").Find(&memberships).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not list groups")
		return
	}
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
	list, err := a.viewerGroupList(groups, roles)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not list groups")
		return
	}
	c.JSON(http.StatusOK, gin.H{"groups": list})
}


// resetRequestMessage is the uniform answer: the same for every address,
// known or not, so the endpoint can't be used to enumerate accounts.
const resetRequestMessage = "If that email has an account, we've sent a link"

// Password-reset requests also use the shared hourly/daily target mail caps.
const (
	resetIPLimit = 10
	resetWindow  = time.Hour
)

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
		fail(c, errEmailRequired)
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
		if _, err := passwordResetMailType.Enqueue(tx, userMailJob{UserID: user.ID}, time.Now(), ""); err != nil {
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

// redeemPasswordLink is the one redemption of an emailed set-password link
// (reset or first setup): it reads the token and the new password, pays for
// bcrypt before the gate, then claims the token, replaces the password and
// records auditAction in one transaction, and signs the person in. Unknown,
// expired or used tokens all get the same answer. It answers the request
// itself when it fails (ok is false); otherwise the caller writes the success
// body. pendingConsent reports a teen still waiting for consent when they set
// the password.
func (a *App) redeemPasswordLink(c *gin.Context, kind, auditAction, failMsg string) (fresh models.User, pendingConsent, ok bool) {
	var body struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Token == "" || body.Password == "" {
		jsonError(c, http.StatusBadRequest, "token and password are required")
		return fresh, false, false
	}
	if !checkNewPassword(c, body.Password) {
		return fresh, false, false
	}
	passwordHash, err := prepareTokenPassword(a.DB.WithContext(c.Request.Context()), kind, body.Token, body.Password)
	if err == nil {
		err = a.DB.Transaction(func(tx *gorm.DB) error {
			u, _, err := lockAndConsumeAccountToken(tx, kind, body.Token)
			if err != nil {
				return err
			}
			pendingConsent = u.Status == models.UserPendingConsent
			if err := replacePasswordIn(tx, u, passwordHash); err != nil {
				return err
			}
			if err := audit.Write(tx, audit.Entry{Actor: &u.ID, Action: auditAction,
				TargetType: "user", TargetID: u.ID, IP: c.ClientIP()}); err != nil {
				return err
			}
			fresh = *u
			return nil
		})
	}
	if errors.Is(err, errTokenInvalid) {
		fail(c, errLinkExpired)
		return fresh, false, false
	}
	if err != nil {
		jsonError(c, http.StatusInternalServerError, failMsg)
		return fresh, false, false
	}
	if err := a.signInSession(c, &fresh); err != nil {
		jsonError(c, http.StatusInternalServerError, "session error")
		return fresh, false, false
	}
	return fresh, pendingConsent, true
}

func (a *App) apiConfirmPasswordReset(c *gin.Context) {
	fresh, _, ok := a.redeemPasswordLink(c, models.TokenPasswordReset, audit.PasswordResetCompleted, "could not reset password")
	if !ok {
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
		setSessionLogin(session, u)
	} else {
		clearSessionLogin(session)
	}
	return session.Save(c.Request, c.Writer)
}
