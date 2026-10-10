package main

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ---------------------------------------------------------------------------
// Group endpoints
// ---------------------------------------------------------------------------

func (a *App) apiListGroups(c *gin.Context) {
	search := c.Query("search")

	var groups []models.Group
	query := a.directoryQuery(apiUser(c))
	if search != "" {
		like := likeContains(strings.ToLower(search))
		query = query.Where(
			"LOWER(name) LIKE ? ESCAPE '\\' OR LOWER(description) LIKE ? ESCAPE '\\'",
			like, like,
		)
	}
	// Repeated tag params are AND-ed. Blank values are ignored.
	for _, tag := range c.QueryArray("tag") {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		query = query.Where("tags LIKE ? ESCAPE '\\'", likeContains(tag))
	}
	query.Order("name").Find(&groups)

	out := make([]groupSummaryJSON, 0, len(groups))
	for i := range groups {
		out = append(out, a.groupSummary(&groups[i]))
	}
	c.JSON(http.StatusOK, gin.H{"groups": out})
}

func (a *App) apiCreateGroup(c *gin.Context) {
	var body struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Tags        []string `json:"tags"`
		ImageURL    string   `json:"imageUrl"`
		Location    string   `json:"location"`
		Visibility  string   `json:"visibility"`
		InviteOnly  bool     `json:"inviteOnly"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.Name) == "" {
		jsonError(c, http.StatusBadRequest, "name is required")
		return
	}
	name := strings.TrimSpace(body.Name)
	description := strings.TrimSpace(body.Description)
	location := strings.TrimSpace(body.Location)
	tags := joinTags(body.Tags)
	if msg := validateGroupText(name, description, location, tags); msg != "" {
		jsonError(c, http.StatusBadRequest, msg)
		return
	}
	visibility, inviteOnly, msg := normalizeVisibility(body.Visibility, body.InviteOnly)
	if msg != "" {
		jsonError(c, http.StatusBadRequest, msg)
		return
	}
	u := apiUser(c)
	g := models.Group{
		Name:        name,
		Description: description,
		Tags:        tags,
		ImageURL:    strings.TrimSpace(body.ImageURL),
		Location:    location,
		LeaderID:    u.ID,
		Status:      models.GroupPending,
		Visibility:  visibility,
		InviteOnly:  inviteOnly,
	}
	if err := a.DB.Create(&g).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not create group")
		return
	}
	// The creator becomes the group's leader-member.
	a.DB.Create(&models.Membership{
		UserID: u.ID, GroupID: g.ID,
		Role: models.MembershipLeader, Status: "active", JoinedAt: time.Now().UTC(),
	})
	c.JSON(http.StatusCreated, gin.H{"group": a.groupSummary(&g)})
}

func (a *App) apiGetGroup(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var g models.Group
	if err := a.DB.First(&g, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	me := apiUser(c)
	if !a.canSeeGroup(me, &g) {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}

	var members []models.Membership
	memberList := []memberJSON{}
	if me != nil {
		a.DB.Preload("User").Where("group_id = ? AND status = ?", g.ID, "active").
			Order("joined_at").Find(&members)
		for _, m := range members {
			memberList = append(memberList, a.toMemberJSON(&m))
		}
	}

	var meetings []models.Meeting
	a.DB.Where("group_id = ?", g.ID).Find(&meetings)
	now := time.Now().UTC()
	upcoming := a.buildInstances(meetings, now, now.AddDate(0, 0, 90), me)

	summary := a.groupSummary(&g)
	c.JSON(http.StatusOK, gin.H{
		"group":            summary,
		"members":          memberList,
		"upcomingMeetings": upcoming,
		"canManage":        me != nil && canManageGroup(a, me, g.ID),
		"isMember":         me != nil && a.isMember(me.ID, g.ID),
	})
}

func (a *App) isMember(userID, groupID uint) bool {
	var n int64
	a.DB.Model(&models.Membership{}).
		Where("user_id = ? AND group_id = ? AND status = ?", userID, groupID, "active").
		Count(&n)
	return n > 0
}

func (a *App) apiUpdateGroup(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var g models.Group
	if err := a.DB.First(&g, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	u := apiUser(c)
	if !canManageGroup(a, u, g.ID) {
		jsonError(c, http.StatusForbidden, "forbidden")
		return
	}
	var body struct {
		Name        *string  `json:"name"`
		Description *string  `json:"description"`
		Tags        []string `json:"tags"`
		ImageURL    *string  `json:"imageUrl"`
		Location    *string  `json:"location"`
		Status      *string  `json:"status"`
		CoverFocal  *struct {
			X float64 `json:"x"`
			Y float64 `json:"y"`
		} `json:"coverFocal"`
		Visibility *string `json:"visibility"`
		InviteOnly *bool   `json:"inviteOnly"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Name != nil {
		name := strings.TrimSpace(*body.Name)
		if name == "" {
			jsonError(c, http.StatusBadRequest, "name is required")
			return
		}
		if len(name) > maxGroupNameLen {
			jsonError(c, http.StatusBadRequest, "name is too long")
			return
		}
		g.Name = name
	}
	if body.Description != nil {
		description := strings.TrimSpace(*body.Description)
		if len(description) > maxDescriptionLen {
			jsonError(c, http.StatusBadRequest, "description is too long")
			return
		}
		g.Description = description
	}
	if body.Tags != nil {
		tags := joinTags(body.Tags)
		if len(tags) > maxTagsLen {
			jsonError(c, http.StatusBadRequest, "tags are too long")
			return
		}
		g.Tags = tags
	}
	if body.ImageURL != nil {
		g.ImageURL = strings.TrimSpace(*body.ImageURL)
	}
	if body.CoverFocal != nil {
		g.CoverFocalX = clampFocal(body.CoverFocal.X)
		g.CoverFocalY = clampFocal(body.CoverFocal.Y)
	}
	if body.Location != nil {
		location := strings.TrimSpace(*body.Location)
		if len(location) > maxLocationLen {
			jsonError(c, http.StatusBadRequest, "location is too long")
			return
		}
		g.Location = location
	}
	if body.Status != nil {
		if !validGroupStatus(*body.Status) {
			jsonError(c, http.StatusBadRequest, "status must be pending, active, or archived")
			return
		}
		// Leaders may archive their own group; only admins may (re)activate.
		if *body.Status == models.GroupArchived || u.IsAdmin() {
			g.Status = *body.Status
		} else {
			jsonError(c, http.StatusForbidden, "only admins can change status to "+*body.Status)
			return
		}
	}
	if body.Visibility != nil || body.InviteOnly != nil {
		visibility := g.Visibility
		inviteOnly := g.InviteOnly
		if body.Visibility != nil {
			visibility = *body.Visibility
		}
		if body.InviteOnly != nil {
			inviteOnly = *body.InviteOnly
		}
		visibility, inviteOnly, msg := normalizeVisibility(visibility, inviteOnly)
		if msg != "" {
			jsonError(c, http.StatusBadRequest, msg)
			return
		}
		g.Visibility = visibility
		g.InviteOnly = inviteOnly
	}
	if err := a.DB.Save(&g).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not update group")
		return
	}
	c.JSON(http.StatusOK, gin.H{"group": a.groupSummary(&g)})
}

// ---------------------------------------------------------------------------
// Membership endpoints
// ---------------------------------------------------------------------------

func (a *App) apiJoinGroup(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var g models.Group
	u := apiUser(c)
	if err := a.DB.First(&g, id).Error; err != nil || g.Status != models.GroupActive || !a.canSeeGroup(u, &g) {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	if g.InviteOnly && !a.isMember(u.ID, g.ID) {
		jsonError(c, http.StatusForbidden, "this group is invite only")
		return
	}
	m := models.Membership{UserID: u.ID, GroupID: g.ID}
	if err := a.DB.Where("user_id = ? AND group_id = ?", u.ID, g.ID).
		Attrs(models.Membership{Role: models.MembershipMember, Status: "active", JoinedAt: time.Now().UTC()}).
		FirstOrCreate(&m).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not join group")
		return
	}
	// A new member means the leader is no longer alone, so a pending
	// deletion request no longer matches the rule that created it.
	if g.DeletionRequested && u.ID != g.LeaderID {
		a.DB.Model(&models.Group{}).Where("id = ?", g.ID).Update("deletion_requested", false)
	}
	c.JSON(http.StatusOK, gin.H{"membership": gin.H{
		"id": m.ID, "userId": m.UserID, "groupId": m.GroupID,
		"role": m.Role, "status": m.Status,
	}})
}

func (a *App) apiLeaveGroup(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	u := apiUser(c)
	var g models.Group
	if err := a.DB.First(&g, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	if g.LeaderID == u.ID {
		jsonError(c, http.StatusBadRequest,
			"cannot leave as the group leader — assign a new leader first")
		return
	}
	a.DB.Where("user_id = ? AND group_id = ?", u.ID, id).Delete(&models.Membership{})
	c.JSON(http.StatusOK, gin.H{})
}

func (a *App) apiGroupMembers(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var g models.Group
	if err := a.DB.First(&g, id).Error; err != nil || !a.canSeeGroup(apiUser(c), &g) {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	var members []models.Membership
	a.DB.Preload("User").Where("group_id = ? AND status = ?", g.ID, "active").
		Order("joined_at").Find(&members)
	out := make([]memberJSON, 0, len(members))
	for _, m := range members {
		out = append(out, a.toMemberJSON(&m))
	}
	c.JSON(http.StatusOK, gin.H{"members": out})
}

func validGroupStatus(s string) bool {
	switch s {
	case models.GroupPending, models.GroupActive, models.GroupArchived:
		return true
	}
	return false
}

func validateGroupText(name, description, location, tags string) string {
	if len(name) > maxGroupNameLen {
		return "name is too long"
	}
	if len(description) > maxDescriptionLen {
		return "description is too long"
	}
	if len(location) > maxLocationLen {
		return "location is too long"
	}
	if len(tags) > maxTagsLen {
		return "tags are too long"
	}
	return ""
}

// likeContains builds a LIKE pattern that matches the text as a substring.
// % and _ in the input are escaped so they are not wildcards. Callers must
// pass ESCAPE '\' so the escape character is explicit.
func likeContains(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return "%" + s + "%"
}

// directoryQuery is the home list: active groups a visitor is allowed to
// browse. Logged-out visitors get public groups only. Signed-in users also
// get private groups. Invite-only groups stay hidden unless the visitor
// leads them, already belongs to them, or is an admin.
func (a *App) directoryQuery(u *models.User) *gorm.DB {
	q := a.DB.Model(&models.Group{}).Where("status = ?", models.GroupActive)
	if u != nil && u.IsAdmin() {
		return q
	}
	if u == nil {
		return q.Where("(visibility = ? OR visibility = '') AND invite_only = ?", models.VisibilityPublic, false)
	}
	var ids []uint
	a.DB.Model(&models.Membership{}).
		Where("user_id = ? AND status = ?", u.ID, "active").
		Pluck("group_id", &ids)
	clause := "((visibility = ? OR visibility = '' OR visibility = ?) AND invite_only = ?) OR leader_id = ?"
	args := []any{models.VisibilityPublic, models.VisibilityPrivate, false, u.ID}
	if len(ids) > 0 {
		clause += " OR id IN ?"
		args = append(args, ids)
	}
	return q.Where(clause, args...)
}

// normalizeVisibility accepts public or private. An empty value means
// public, which is the default for groups created before visibility existed.
// A public group cannot be invite-only.
func normalizeVisibility(visibility string, inviteOnly bool) (string, bool, string) {
	visibility = strings.TrimSpace(visibility)
	if visibility == "" {
		visibility = models.VisibilityPublic
	}
	if visibility != models.VisibilityPublic && visibility != models.VisibilityPrivate {
		return "", false, "visibility must be public or private"
	}
	if visibility == models.VisibilityPublic && inviteOnly {
		return "", false, "public groups cannot be invite only"
	}
	return visibility, inviteOnly, ""
}

func (a *App) loadManagedGroup(c *gin.Context) (*models.Group, *models.User, bool) {
	id, _ := strconv.Atoi(c.Param("id"))
	var g models.Group
	if err := a.DB.First(&g, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "group not found")
		return nil, nil, false
	}
	u := apiUser(c)
	if !canManageGroup(a, u, g.ID) {
		jsonError(c, http.StatusForbidden, "forbidden")
		return nil, nil, false
	}
	return &g, u, true
}

// apiTransferLeadership hands the group to another active member.
func (a *App) apiTransferLeadership(c *gin.Context) {
	g, _, ok := a.loadManagedGroup(c)
	if !ok {
		return
	}
	var body struct {
		UserID uint `json:"userId"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.UserID == 0 {
		jsonError(c, http.StatusBadRequest, "userId is required")
		return
	}
	if body.UserID == g.LeaderID {
		jsonError(c, http.StatusBadRequest, "that person is already the leader")
		return
	}
	var m models.Membership
	if err := a.DB.Where("user_id = ? AND group_id = ? AND status = ?", body.UserID, g.ID, "active").
		First(&m).Error; err != nil {
		jsonError(c, http.StatusBadRequest, "that person is not a member of this group")
		return
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.Membership{}).
			Where("group_id = ? AND role = ?", g.ID, models.MembershipLeader).
			Update("role", models.MembershipMember).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.Membership{}).
			Where("group_id = ? AND user_id = ?", g.ID, body.UserID).
			Updates(map[string]any{"role": models.MembershipLeader, "status": "active"}).Error; err != nil {
			return err
		}
		return tx.Model(&models.Group{}).Where("id = ?", g.ID).Updates(map[string]any{
			"leader_id":          body.UserID,
			"deletion_requested": false,
		}).Error
	})
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not transfer leadership")
		return
	}
	if err := a.DB.First(g, g.ID).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not transfer leadership")
		return
	}
	c.JSON(http.StatusOK, gin.H{"group": a.groupSummary(g)})
}

// apiAddGroupMember lets a leader add someone by username. Invite-only
// groups have no public join button, so this is how they grow.
func (a *App) apiAddGroupMember(c *gin.Context) {
	g, _, ok := a.loadManagedGroup(c)
	if !ok {
		return
	}
	var body struct {
		Username string `json:"username"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.Username) == "" {
		jsonError(c, http.StatusBadRequest, "username is required")
		return
	}
	var target models.User
	if err := a.DB.Where("username = ?", strings.TrimSpace(body.Username)).First(&target).Error; err != nil {
		jsonError(c, http.StatusNotFound, "user not found")
		return
	}
	m := models.Membership{UserID: target.ID, GroupID: g.ID}
	if err := a.DB.Where("user_id = ? AND group_id = ?", target.ID, g.ID).
		Attrs(models.Membership{Role: models.MembershipMember, Status: "active", JoinedAt: time.Now().UTC()}).
		FirstOrCreate(&m).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not add member")
		return
	}
	if m.Status != "active" {
		a.DB.Model(&m).Updates(map[string]any{"status": "active"})
		m.Status = "active"
	}
	if g.DeletionRequested && target.ID != g.LeaderID {
		a.DB.Model(&models.Group{}).Where("id = ?", g.ID).Update("deletion_requested", false)
	}
	c.JSON(http.StatusOK, gin.H{"membership": gin.H{
		"id": m.ID, "userId": m.UserID, "groupId": m.GroupID,
		"role": m.Role, "status": m.Status,
	}})
}

// apiRequestDeletion asks an admin to delete the group. Only the leader can
// ask, and only when nobody else is still a member.
func (a *App) apiRequestDeletion(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var g models.Group
	if err := a.DB.First(&g, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	u := apiUser(c)
	if g.LeaderID != u.ID {
		jsonError(c, http.StatusForbidden, "only the group leader can request deletion")
		return
	}
	var others int64
	a.DB.Model(&models.Membership{}).
		Where("group_id = ? AND status = ? AND user_id <> ?", g.ID, "active", u.ID).
		Count(&others)
	if others > 0 {
		jsonError(c, http.StatusBadRequest, "the group still has other members")
		return
	}
	if err := a.DB.Model(&g).Update("deletion_requested", true).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not request deletion")
		return
	}
	g.DeletionRequested = true
	c.JSON(http.StatusOK, gin.H{"group": a.groupSummary(&g)})
}

// apiCancelDeletion withdraws a leader's deletion request.
func (a *App) apiCancelDeletion(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var g models.Group
	if err := a.DB.First(&g, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	u := apiUser(c)
	if g.LeaderID != u.ID && !u.IsAdmin() {
		jsonError(c, http.StatusForbidden, "forbidden")
		return
	}
	if err := a.DB.Model(&g).Update("deletion_requested", false).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not cancel deletion")
		return
	}
	c.JSON(http.StatusOK, gin.H{})
}

// deleteGroup removes the group and the rows that belong to it. Meetings
// and the group itself are soft-deleted. RSVPs, exceptions, and memberships
// have no deleted-at column, so they are removed outright.
func (a *App) deleteGroup(ctx context.Context, g *models.Group) error {
	imageURL := g.ImageURL
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		var meetingIDs []uint
		if err := tx.Unscoped().Model(&models.Meeting{}).Where("group_id = ?", g.ID).Pluck("id", &meetingIDs).Error; err != nil {
			return err
		}
		if len(meetingIDs) > 0 {
			if err := tx.Where("meeting_id IN ?", meetingIDs).Delete(&models.RSVP{}).Error; err != nil {
				return err
			}
			if err := tx.Where("meeting_id IN ?", meetingIDs).Delete(&models.MeetingException{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("group_id = ?", g.ID).Delete(&models.Meeting{}).Error; err != nil {
			return err
		}
		if err := tx.Where("group_id = ?", g.ID).Delete(&models.Membership{}).Error; err != nil {
			return err
		}
		return tx.Delete(&models.Group{}, g.ID).Error
	})
	if err != nil {
		return err
	}
	if key, ok := uploadKeyFromURL(imageURL); ok {
		_ = a.Uploads.Delete(ctx, key)
	}
	return nil
}
