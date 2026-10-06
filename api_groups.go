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
// Group endpoints
// ---------------------------------------------------------------------------

func (a *App) apiListGroups(c *gin.Context) {
	search := c.Query("search")
	tag := c.Query("tag")

	var groups []models.Group
	query := a.DB.Where("status = ?", models.GroupActive)
	if search != "" {
		like := "%" + strings.ToLower(search) + "%"
		query = query.Where("LOWER(name) LIKE ? OR LOWER(description) LIKE ?", like, like)
	}
	if tag != "" {
		query = query.Where("tags LIKE ?", "%"+tag+"%")
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
	}
	if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.Name) == "" {
		jsonError(c, http.StatusBadRequest, "name is required")
		return
	}
	u := apiUser(c)
	g := models.Group{
		Name:        strings.TrimSpace(body.Name),
		Description: strings.TrimSpace(body.Description),
		Tags:        joinTags(body.Tags),
		ImageURL:    strings.TrimSpace(body.ImageURL),
		Location:    strings.TrimSpace(body.Location),
		LeaderID:    u.ID,
		Status:      models.GroupPending,
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
	if g.Status != models.GroupActive {
		// Pending/archived groups are visible to admins and the leader only.
		if me == nil || (!me.IsAdmin() && g.LeaderID != me.ID) {
			jsonError(c, http.StatusNotFound, "group not found")
			return
		}
	}

	var members []models.Membership
	memberList := []memberJSON{}
	if me != nil {
		a.DB.Preload("User").Where("group_id = ? AND status = ?", g.ID, "active").
			Order("joined_at").Find(&members)
		for _, m := range members {
			name := m.User.Name
			if name == "" {
				name = m.User.Username
			}
			memberList = append(memberList, memberJSON{
				ID: m.User.ID, Name: name, Role: m.Role,
				JoinedAt: m.JoinedAt.Format(time.RFC3339),
			})
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
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Name != nil {
		g.Name = strings.TrimSpace(*body.Name)
	}
	if body.Description != nil {
		g.Description = strings.TrimSpace(*body.Description)
	}
	if body.Tags != nil {
		g.Tags = joinTags(body.Tags)
	}
	if body.ImageURL != nil {
		g.ImageURL = strings.TrimSpace(*body.ImageURL)
	}
	if body.Location != nil {
		g.Location = strings.TrimSpace(*body.Location)
	}
	if body.Status != nil {
		// Leaders may archive their own group; only admins may (re)activate.
		if *body.Status == models.GroupArchived || u.IsAdmin() {
			g.Status = *body.Status
		} else {
			jsonError(c, http.StatusForbidden, "only admins can change status to " + *body.Status)
			return
		}
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
	if err := a.DB.First(&g, id).Error; err != nil || g.Status != models.GroupActive {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	u := apiUser(c)
	m := models.Membership{UserID: u.ID, GroupID: g.ID}
	if err := a.DB.Where("user_id = ? AND group_id = ?", u.ID, g.ID).
		Attrs(models.Membership{Role: models.MembershipMember, Status: "active", JoinedAt: time.Now().UTC()}).
		FirstOrCreate(&m).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not join group")
		return
	}
	c.JSON(http.StatusOK, gin.H{"membership": gin.H{
		"id": m.ID, "userId": m.UserID, "groupId": m.GroupID,
		"role": m.Role, "status": m.Status,
	}})
}

func (a *App) apiLeaveGroup(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	u := apiUser(c)
	a.DB.Where("user_id = ? AND group_id = ?", u.ID, id).Delete(&models.Membership{})
	c.JSON(http.StatusOK, gin.H{})
}

func (a *App) apiGroupMembers(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var members []models.Membership
	a.DB.Preload("User").Where("group_id = ? AND status = ?", id, "active").
		Order("joined_at").Find(&members)
	out := make([]memberJSON, 0, len(members))
	for _, m := range members {
		name := m.User.Name
		if name == "" {
			name = m.User.Username
		}
		out = append(out, memberJSON{
			ID: m.User.ID, Name: name, Role: m.Role,
			JoinedAt: m.JoinedAt.Format(time.RFC3339),
		})
	}
	c.JSON(http.StatusOK, gin.H{"members": out})
}
