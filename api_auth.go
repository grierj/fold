package main

import (
	"net/http"

	"church-groups/models"

	"github.com/gin-gonic/gin"
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
	var user models.User
	if err := a.DB.Where("username = ?", body.Username).First(&user).Error; err != nil {
		jsonError(c, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if !user.CheckPassword(body.Password) {
		jsonError(c, http.StatusUnauthorized, "invalid credentials")
		return
	}
	session, err := a.Store.Get(c.Request, sessionName)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "session error")
		return
	}
	session.Values["userID"] = user.ID
	if err := session.Save(c.Request, c.Writer); err != nil {
		jsonError(c, http.StatusInternalServerError, "session error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"user": a.toUserJSON(&user)})
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
		if memberships[i].Group.Status == models.GroupActive {
			out = append(out, a.groupSummary(&memberships[i].Group))
		}
	}
	c.JSON(http.StatusOK, gin.H{"groups": out})
}
