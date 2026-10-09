package main

import (
	"net/http"
	"strings"
	"time"

	"fold/models"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
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
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "username and password are required")
		return
	}
	a.loginDelay(body.Username)
	var user models.User
	if err := a.DB.Where("username = ?", body.Username).First(&user).Error; err != nil {
		a.noteLoginFailure(body.Username)
		jsonError(c, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if !user.CheckPassword(body.Password) {
		a.noteLoginFailure(body.Username)
		jsonError(c, http.StatusUnauthorized, "invalid credentials")
		return
	}
	a.clearLoginFailure(body.Username)
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
	c.JSON(http.StatusOK, gin.H{"user": a.toUserJSON(&user)})
}

// loginKey normalizes the throttle map key. The counter resets when the
// process restarts and is not shared by a second replica.
func loginKey(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

func (a *App) loginDelay(username string) {
	key := loginKey(username)
	if key == "" {
		return
	}
	a.loginMu.Lock()
	n := 0
	if a.loginFails != nil {
		n = a.loginFails[key]
	}
	a.loginMu.Unlock()
	if n <= 0 {
		return
	}
	shift := n - 1
	if shift > 4 {
		shift = 4
	}
	d := 200 * time.Millisecond << shift
	if d > 3*time.Second {
		d = 3 * time.Second
	}
	time.Sleep(d)
}

func (a *App) noteLoginFailure(username string) {
	key := loginKey(username)
	if key == "" {
		return
	}
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	if a.loginFails == nil {
		a.loginFails = map[string]int{}
	}
	if a.loginFails[key] < 8 {
		a.loginFails[key]++
	}
}

func (a *App) clearLoginFailure(username string) {
	key := loginKey(username)
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	if a.loginFails != nil {
		delete(a.loginFails, key)
	}
}

// setPassword stores a new bcrypt hash and invalidates existing sessions.
func (a *App) setPassword(u *models.User, plaintext string) error {
	hashed, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return a.DB.Model(u).Updates(map[string]interface{}{
		"password":        string(hashed),
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

func (a *App) apiLogout(c *gin.Context) {
	session, _ := a.Store.Get(c.Request, sessionName)
	session.Values["userID"] = nil
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
