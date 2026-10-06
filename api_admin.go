package main

import (
	"net/http"
	"strconv"

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
