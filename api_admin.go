package main

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"fold/internal/audit"
	"fold/internal/notify"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
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

// apiApproveGroup and apiRejectGroup decide a pending group. Both are
// conditional on the group still being pending, and resolve every admin's
// task in the same transaction (5.12). A request from the bell names its
// notification (fromNotification); only Approve is allow-listed (6.6).
func (a *App) apiApproveGroup(c *gin.Context) {
	var body struct {
		Tags             []string `json:"tags"`
		FromNotification *uint    `json:"fromNotification"`
	}
	_ = c.ShouldBindJSON(&body)
	a.decideGroup(c, notify.ActionApproveGroup, body.FromNotification, func(g *models.Group) {
		if body.Tags != nil {
			g.Tags = joinTags(body.Tags)
		}
		g.Status = models.GroupActive
	})
}

func (a *App) apiRejectGroup(c *gin.Context) {
	var body struct {
		FromNotification *uint `json:"fromNotification"`
	}
	_ = c.ShouldBindJSON(&body)
	a.decideGroup(c, "reject", body.FromNotification, func(g *models.Group) { g.Status = models.GroupArchived })
}

func (a *App) decideGroup(c *gin.Context, action string, fromNotification *uint, apply func(*models.Group)) {
	id, _ := strconv.Atoi(c.Param("id"))
	var g models.Group
	if err := a.DB.First(&g, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	if !a.fromNotification(c, fromNotification, action, g.ID) {
		return
	}
	me := apiUser(c)
	outcome, auditAction := "approved", audit.GroupApproved
	if action != notify.ActionApproveGroup {
		outcome, auditAction = "rejected", audit.GroupRejected
	}
	apply(&g)
	errHandled := errors.New("already handled")
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.Group{}).Where("id = ? AND status = ?", g.ID, models.GroupPending).
			Updates(map[string]any{"status": g.Status, "tags": g.Tags})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errHandled
		}
		if _, err := notify.Resolve(tx, notify.GroupPending, g.ID, &me.ID, outcome); err != nil {
			return err
		}
		return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: auditAction, TargetType: "group", TargetID: g.ID, IP: c.ClientIP()})
	})
	if errors.Is(err, errHandled) {
		a.alreadyHandled(c, notify.GroupPending, g.ID)
		return
	}
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not "+action+" group")
		return
	}
	if action == notify.ActionApproveGroup {
		c.JSON(http.StatusOK, gin.H{"group": a.groupSummary(&g)})
		return
	}
	c.JSON(http.StatusOK, gin.H{})
}

// ---------------------------------------------------------------------------
// Admin: user management
// ---------------------------------------------------------------------------

func (a *App) apiAdminListUsers(c *gin.Context) {
	var users []models.User
	a.DB.Order("lower(name), email").Find(&users)
	out := make([]userJSON, 0, len(users))
	for i := range users {
		out = append(out, a.toUserJSON(&users[i]))
	}
	c.JSON(http.StatusOK, gin.H{"users": out})
}

// apiAdminCreateUser adds an account. Without a password it starts
// invited and can't sign in until someone sets one (M1.2 emails the link).
func (a *App) apiAdminCreateUser(c *gin.Context) {
	var body struct {
		Email       string `json:"email"`
		Password    string `json:"password"`
		Role        string `json:"role"`
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
		Phone       string `json:"phone"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	email := normalizeEmail(body.Email)
	if !validEmail(email) {
		jsonError(c, http.StatusBadRequest, "a valid email is required")
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" || len(name) > 100 {
		jsonError(c, http.StatusBadRequest, "name is required (at most 100 characters)")
		return
	}
	if body.Password != "" && len(body.Password) < minPasswordLength {
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
		Email: email, Role: role, Status: models.UserInvited,
		Name: name, DisplayName: strings.TrimSpace(body.DisplayName), Phone: strings.TrimSpace(body.Phone),
	}
	if body.Password != "" {
		if err := u.SetPassword(body.Password); err != nil {
			jsonError(c, http.StatusInternalServerError, "could not create user")
			return
		}
		u.Status = models.UserActive
	}
	if err := a.DB.Create(&u).Error; err != nil {
		if emailTaken(err) {
			jsonError(c, http.StatusConflict, "an account with that email already exists")
			return
		}
		jsonError(c, http.StatusInternalServerError, "could not create user")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"user": a.toUserJSON(&u)})
}

// emailTaken reports a unique-email violation.
func emailTaken(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
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

// adminCount counts admin accounts.
func (a *App) adminCount() int {
	var n int64
	a.DB.Model(&models.User{}).Where("role = ? AND status = ?", models.RoleAdmin, models.UserActive).Count(&n)
	return int(n)
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
		Status      *string `json:"status"`
		// Successors: required when disabling someone who leads a group alone
		// (DEC-36), as for deletion.
		Successors map[string]uint `json:"successors"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	updates := map[string]interface{}{}
	bump := false
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
		email := normalizeEmail(*body.Email)
		if !validEmail(email) {
			jsonError(c, http.StatusBadRequest, "a valid email is required")
			return
		}
		if email != normalizeEmail(u.Email) {
			updates["email"] = email
			bump = true
		}
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
		if role != models.RoleAdmin && !policy.CanDemoteOrDelete(asPerson(me), asPerson(u), a.adminCount()) {
			msg := "cannot demote the last admin"
			if u.ID == me.ID {
				msg = "you cannot demote yourself"
			}
			jsonError(c, http.StatusBadRequest, msg)
			return
		}
		if role != u.Role {
			updates["role"] = role
			bump = true
		}
	}
	if body.Status != nil {
		status := strings.TrimSpace(*body.Status)
		if status != models.UserActive && status != models.UserDisabled {
			jsonError(c, http.StatusBadRequest, "status must be active or disabled")
			return
		}
		if status == models.UserDisabled && !policy.CanDemoteOrDelete(asPerson(me), asPerson(u), a.adminCount()) {
			msg := "cannot disable the last admin"
			if u.ID == me.ID {
				msg = "you cannot disable your own account"
			}
			jsonError(c, http.StatusBadRequest, msg)
			return
		}
		if status == models.UserActive && u.PasswordHash == nil {
			status = models.UserInvited // re-enabled, but still needs a password
		}
		if status != u.Status {
			updates["status"] = status
			bump = true
		}
	}
	// Changing the email, role or status ends the person's sessions (10.1).
	if bump {
		updates["session_version"] = gorm.Expr("session_version + 1")
	}
	if len(updates) > 0 {
		disabling := updates["status"] == models.UserDisabled
		var emailed []uint
		err := a.DB.Transaction(func(tx *gorm.DB) error {
			if disabling {
				ids, err := handOffLeadership(c, tx, u.ID, body.Successors, "account_disabled")
				emailed = append(emailed, ids...)
				if err != nil {
					return err
				}
				if err := audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.UserDisabled,
					TargetType: "user", TargetID: u.ID, IP: c.ClientIP()}); err != nil {
					return err
				}
			}
			if s, ok := updates["status"].(string); ok && u.Status == models.UserDisabled && s != models.UserDisabled {
				ids, err := restoreMemberships(c, tx, u.ID)
				emailed = append(emailed, ids...)
				if err != nil {
					return err
				}
				if err := audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.UserEnabled,
					TargetType: "user", TargetID: u.ID, IP: c.ClientIP()}); err != nil {
					return err
				}
			}
			return tx.Model(u).Updates(updates).Error
		})
		if needSuccessors(c, err) {
			return
		}
		if err != nil {
			if emailTaken(err) {
				jsonError(c, http.StatusConflict, "an account with that email already exists")
				return
			}
			jsonError(c, http.StatusInternalServerError, "could not update user")
			return
		}
		a.deliverNotificationEmails(emailed)
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
	if !policy.CanDemoteOrDelete(asPerson(me), asPerson(u), a.adminCount()) {
		msg := "cannot delete the last admin"
		if u.ID == me.ID {
			msg = "you cannot delete your own account"
		}
		jsonError(c, http.StatusBadRequest, msg)
		return
	}
	var body struct {
		// Successors maps group id to the new leader, for every group this
		// person leads alone (DEC-36; see GET /admin/users/:id/sole-leader-groups).
		Successors map[string]uint `json:"successors"`
	}
	_ = c.ShouldBindJSON(&body) // optional when they lead no group alone
	// Soft delete (DeletedAt), after handing off every group they lead alone
	// and ending their memberships. The email is replaced so the address is
	// free for a new account, and the session version is bumped so open
	// sessions end.
	var emailed []uint
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		ids, err := handOffLeadership(c, tx, u.ID, body.Successors, "account_deleted")
		emailed = append(emailed, ids...)
		if err != nil {
			return err
		}
		if err := tx.Model(u).Updates(map[string]any{
			"email":           fmt.Sprintf("deleted-%d@deleted.invalid", u.ID),
			"session_version": gorm.Expr("session_version + 1"),
		}).Error; err != nil {
			return err
		}
		if err := audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.UserDeleted,
			TargetType: "user", TargetID: u.ID, IP: c.ClientIP()}); err != nil {
			return err
		}
		// Notifications go with the person (5.3); the audit log stays.
		if err := tx.Where("user_id = ?", u.ID).Delete(&models.Notification{}).Error; err != nil {
			return err
		}
		return tx.Delete(u).Error
	})
	if needSuccessors(c, err) {
		return
	}
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not delete user")
		return
	}
	a.deliverNotificationEmails(emailed)
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
		// OverLimit: add even though the group is full (DEC-39); audited.
		OverLimit bool `json:"overLimit"`
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
	me := apiUser(c)
	err := a.changeRole(c, &g, u.ID, role, "", body.OverLimit, func(actor, t policy.Rel, leaders int) bool {
		// Admins may add someone straight in with either role.
		return policy.CanChangeRole(asPerson(me), actor, asPerson(&u),
			policy.Rel{Member: true, Leader: t.Leader}, role == models.MembershipLeader, leaders)
	})
	switch err {
	case nil:
	case errGroupFull:
		groupFull(c)
		return
	case errRoleRefused:
		jsonError(c, http.StatusBadRequest,
			"only adults can be leaders, and a group must keep at least one leader")
		return
	default:
		jsonError(c, http.StatusInternalServerError, "could not add member")
		return
	}
	var m models.Membership
	a.DB.Where("user_id = ? AND group_id = ?", u.ID, g.ID).First(&m)
	if g.DeletionRequested && memberCount(a.DB, g.ID) > 1 {
		a.clearDeletionRequest(g.ID, nil, "withdrawn")
	}
	c.JSON(http.StatusOK, gin.H{"membership": gin.H{
		"id": m.ID, "userId": m.UserID, "groupId": m.GroupID, "role": role,
	}})
}

func (a *App) apiAdminRemoveMember(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var g models.Group
	if err := a.DB.First(&g, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	a.removeMember(c, &g)
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
	if err := a.clearDeletionRequest(g.ID, &apiUser(c).ID, "kept"); err != nil {
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
	if err := a.deleteGroup(c.Request.Context(), &g, &apiUser(c).ID); err != nil {
		jsonError(c, http.StatusInternalServerError, "could not delete group")
		return
	}
	c.JSON(http.StatusOK, gin.H{})
}
