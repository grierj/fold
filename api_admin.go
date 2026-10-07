package main

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"church-groups/models"

	"github.com/gin-gonic/gin"
)

// ---------------------------------------------------------------------------
// Admin endpoints
// ---------------------------------------------------------------------------

func (a *App) apiPendingGroups(c *gin.Context) {
	var groups []models.Group
	a.DB.Where("status = ?", models.GroupPending).Order("created_at").Find(&groups)
	out := make([]groupSummaryJSON, 0, len(groups))
	for i := range groups {
		out = append(out, a.groupSummary(&groups[i]))
	}
	c.JSON(http.StatusOK, gin.H{"groups": out})
}

func (a *App) apiApproveGroup(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var g models.Group
	if err := a.DB.First(&g, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	var body struct {
		Tags []string `json:"tags"`
	}
	_ = c.ShouldBindJSON(&body)
	if body.Tags != nil {
		g.Tags = joinTags(body.Tags)
	}
	g.Status = models.GroupActive
	if err := a.DB.Save(&g).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not approve group")
		return
	}
	c.JSON(http.StatusOK, gin.H{"group": a.groupSummary(&g)})
}

func (a *App) apiRejectGroup(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var g models.Group
	if err := a.DB.First(&g, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	g.Status = models.GroupArchived
	if err := a.DB.Save(&g).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not reject group")
		return
	}
	c.JSON(http.StatusOK, gin.H{})
}

// ---------------------------------------------------------------------------
// Admin: user management
// ---------------------------------------------------------------------------

func (a *App) apiAdminListUsers(c *gin.Context) {
	var users []models.User
	a.DB.Order("username").Find(&users)
	out := make([]userJSON, 0, len(users))
	for i := range users {
		out = append(out, a.toUserJSON(&users[i]))
	}
	c.JSON(http.StatusOK, gin.H{"users": out})
}

func (a *App) apiAdminCreateUser(c *gin.Context) {
	var body struct {
		Username    string `json:"username"`
		Password    string `json:"password"`
		Role        string `json:"role"`
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
		Email       string `json:"email"`
		Phone       string `json:"phone"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	username := strings.TrimSpace(body.Username)
	if username == "" {
		jsonError(c, http.StatusBadRequest, "username is required")
		return
	}
	if len(body.Password) < minPasswordLength {
		jsonError(c, http.StatusBadRequest,
			"password must be at least "+strconv.Itoa(minPasswordLength)+" characters")
		return
	}
	role := body.Role
	if role == "" {
		role = models.RoleMember
	}
	if role != models.RoleAdmin && role != models.RoleMember {
		jsonError(c, http.StatusBadRequest, "role must be admin or member")
		return
	}
	u := models.User{
		Username: username, Password: body.Password, Role: role,
		Name: strings.TrimSpace(body.Name), DisplayName: strings.TrimSpace(body.DisplayName),
		Email: strings.TrimSpace(body.Email), Phone: strings.TrimSpace(body.Phone),
	}
	if err := a.DB.Create(&u).Error; err != nil {
		jsonError(c, http.StatusConflict, "username is taken")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"user": a.toUserJSON(&u)})
}

func (a *App) adminTargetUser(c *gin.Context) (*models.User, bool) {
	id, _ := strconv.Atoi(c.Param("id"))
	var u models.User
	if err := a.DB.First(&u, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "user not found")
		return nil, false
	}
	return &u, true
}

// lastAdmin reports whether u is the only remaining admin.
func (a *App) lastAdmin(u *models.User) bool {
	if !u.IsAdmin() {
		return false
	}
	var n int64
	a.DB.Model(&models.User{}).Where("role = ?", models.RoleAdmin).Count(&n)
	return n <= 1
}

func (a *App) apiAdminUpdateUser(c *gin.Context) {
	u, ok := a.adminTargetUser(c)
	if !ok {
		return
	}
	me := apiUser(c)
	var body struct {
		Name        *string `json:"name"`
		DisplayName *string `json:"displayName"`
		Email       *string `json:"email"`
		Phone       *string `json:"phone"`
		Role        *string `json:"role"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	updates := map[string]interface{}{}
	if body.Name != nil {
		if len(*body.Name) > 100 {
			jsonError(c, http.StatusBadRequest, "name must be at most 100 characters")
			return
		}
		updates["name"] = strings.TrimSpace(*body.Name)
	}
	if body.DisplayName != nil {
		if len(*body.DisplayName) > 100 {
			jsonError(c, http.StatusBadRequest, "display name must be at most 100 characters")
			return
		}
		updates["display_name"] = strings.TrimSpace(*body.DisplayName)
	}
	if body.Email != nil {
		updates["email"] = strings.TrimSpace(*body.Email)
	}
	if body.Phone != nil {
		if len(*body.Phone) > 30 {
			jsonError(c, http.StatusBadRequest, "phone must be at most 30 characters")
			return
		}
		updates["phone"] = strings.TrimSpace(*body.Phone)
	}
	if body.Role != nil {
		role := strings.TrimSpace(*body.Role)
		if role != models.RoleAdmin && role != models.RoleMember {
			jsonError(c, http.StatusBadRequest, "role must be admin or member")
			return
		}
		if u.IsAdmin() && role != models.RoleAdmin {
			if u.ID == me.ID {
				jsonError(c, http.StatusBadRequest, "you cannot demote yourself")
				return
			}
			if a.lastAdmin(u) {
				jsonError(c, http.StatusBadRequest, "cannot demote the last admin")
				return
			}
		}
		updates["role"] = role
	}
	if len(updates) > 0 {
		if err := a.DB.Model(u).Updates(updates).Error; err != nil {
			jsonError(c, http.StatusInternalServerError, "could not update user")
			return
		}
	}
	var updated models.User
	a.DB.First(&updated, u.ID)
	c.JSON(http.StatusOK, gin.H{"user": a.toUserJSON(&updated)})
}

func (a *App) apiAdminDeleteUser(c *gin.Context) {
	u, ok := a.adminTargetUser(c)
	if !ok {
		return
	}
	me := apiUser(c)
	if u.ID == me.ID {
		jsonError(c, http.StatusBadRequest, "you cannot delete your own account")
		return
	}
	if a.lastAdmin(u) {
		jsonError(c, http.StatusBadRequest, "cannot delete the last admin")
		return
	}
	// Soft delete (DeletedAt). Their memberships stay for history; the
	// roster only lists active memberships whose user still exists.
	if err := a.DB.Delete(u).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not delete user")
		return
	}
	c.JSON(http.StatusOK, gin.H{})
}

func (a *App) apiAdminResetPassword(c *gin.Context) {
	u, ok := a.adminTargetUser(c)
	if !ok {
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Password == "" {
		jsonError(c, http.StatusBadRequest, "password is required")
		return
	}
	if len(body.Password) < minPasswordLength {
		jsonError(c, http.StatusBadRequest,
			"password must be at least "+strconv.Itoa(minPasswordLength)+" characters")
		return
	}
	if err := a.setPassword(u, body.Password); err != nil {
		jsonError(c, http.StatusInternalServerError, "could not reset password")
		return
	}
	c.JSON(http.StatusOK, gin.H{})
}

// ---------------------------------------------------------------------------
// Admin: group + membership management
// ---------------------------------------------------------------------------

func (a *App) apiAdminListGroups(c *gin.Context) {
	var groups []models.Group
	a.DB.Order("name").Find(&groups)
	out := make([]groupSummaryJSON, 0, len(groups))
	for i := range groups {
		out = append(out, a.groupSummary(&groups[i]))
	}
	c.JSON(http.StatusOK, gin.H{"groups": out})
}

func (a *App) apiAdminAddMember(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var g models.Group
	if err := a.DB.First(&g, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	var body struct {
		UserID uint   `json:"userId"`
		Role   string `json:"role"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.UserID == 0 {
		jsonError(c, http.StatusBadRequest, "userId is required")
		return
	}
	var u models.User
	if err := a.DB.First(&u, body.UserID).Error; err != nil {
		jsonError(c, http.StatusNotFound, "user not found")
		return
	}
	role := body.Role
	if role == "" {
		role = models.MembershipMember
	}
	if role != models.MembershipLeader && role != models.MembershipMember {
		jsonError(c, http.StatusBadRequest, "role must be leader or member")
		return
	}
	m := models.Membership{UserID: u.ID, GroupID: g.ID}
	if err := a.DB.Where("user_id = ? AND group_id = ?", u.ID, g.ID).
		Attrs(models.Membership{Role: role, Status: "active", JoinedAt: time.Now().UTC()}).
		FirstOrCreate(&m).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not add member")
		return
	}
	// Keep an explicit role change in sync (FirstOrCreate won't update).
	if m.Role != role || m.Status != "active" {
		a.DB.Model(&m).Updates(map[string]interface{}{"role": role, "status": "active"})
	}
	if role == models.MembershipLeader && g.LeaderID != u.ID {
		// Reassigning leadership: demote the previous leader's membership.
		a.DB.Model(&models.Membership{}).
			Where("group_id = ? AND user_id = ? AND role = ?", g.ID, g.LeaderID, models.MembershipLeader).
			Update("role", models.MembershipMember)
		g.LeaderID = u.ID
		g.DeletionRequested = false
		if err := a.DB.Save(&g).Error; err != nil {
			jsonError(c, http.StatusInternalServerError, "could not reassign leader")
			return
		}
	} else if g.DeletionRequested && u.ID != g.LeaderID {
		a.DB.Model(&models.Group{}).Where("id = ?", g.ID).Update("deletion_requested", false)
	}
	c.JSON(http.StatusOK, gin.H{"membership": gin.H{
		"id": m.ID, "userId": m.UserID, "groupId": m.GroupID, "role": role,
	}})
}

func (a *App) apiAdminRemoveMember(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	uid, _ := strconv.Atoi(c.Param("userId"))
	var g models.Group
	if err := a.DB.First(&g, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	if uint(uid) == g.LeaderID {
		jsonError(c, http.StatusBadRequest,
			"cannot remove the group leader — assign a new leader first")
		return
	}
	a.DB.Where("user_id = ? AND group_id = ?", uid, id).Delete(&models.Membership{})
	c.JSON(http.StatusOK, gin.H{})
}

func (a *App) apiDeletionRequests(c *gin.Context) {
	var groups []models.Group
	a.DB.Where("deletion_requested = ?", true).Order("updated_at").Find(&groups)
	out := make([]groupSummaryJSON, 0, len(groups))
	for i := range groups {
		out = append(out, a.groupSummary(&groups[i]))
	}
	c.JSON(http.StatusOK, gin.H{"groups": out})
}

// apiRejectDeletion keeps the group and clears the leader's request.
func (a *App) apiRejectDeletion(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var g models.Group
	if err := a.DB.First(&g, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	if err := a.DB.Model(&g).Update("deletion_requested", false).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not reject deletion")
		return
	}
	c.JSON(http.StatusOK, gin.H{})
}

// apiAdminDeleteGroup removes any group. Unlike a leader's request, this
// does not wait for a membership check.
func (a *App) apiAdminDeleteGroup(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var g models.Group
	if err := a.DB.First(&g, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	if err := a.deleteGroup(c.Request.Context(), &g); err != nil {
		jsonError(c, http.StatusInternalServerError, "could not delete group")
		return
	}
	c.JSON(http.StatusOK, gin.H{})
}
