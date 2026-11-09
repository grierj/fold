package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"fold/internal/notify"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ---------------------------------------------------------------------------
// Notifications API (5.12): the user's own stored rows only. Anyone else's
// row answers 404.
// ---------------------------------------------------------------------------

type notificationJSON struct {
	ID         uint            `json:"id"`
	Kind       string          `json:"kind"`
	SubjectID  *uint           `json:"subjectId,omitempty"` // the group an inline action is about
	Category   string          `json:"category"`
	Title      string          `json:"title"`
	Body       string          `json:"body"`
	Link       string          `json:"link"`
	Actions    []notify.Action `json:"actions"`
	BatchCount int             `json:"batchCount"`
	CreatedAt  time.Time       `json:"createdAt"`
	UpdatedAt  time.Time       `json:"updatedAt"`
	Read       bool            `json:"read"`
	Archived   bool            `json:"archived"`
	Waiting    bool            `json:"waiting"` // an open task, counted until resolved
	Resolution *resolutionJSON `json:"resolution,omitempty"`
}

type resolutionJSON struct {
	Outcome string    `json:"outcome"` // approved, rejected, deleted, kept, withdrawn
	By      string    `json:"by"`
	ByMe    bool      `json:"byMe"`
	At      time.Time `json:"at"`
}

func toNotificationJSON(n *models.Notification, me uint, names publicNames) notificationJSON {
	out := notificationJSON{
		ID: n.ID, Kind: n.Kind, SubjectID: n.SubjectID, Category: n.Category, Title: n.Title, Body: n.Body, Link: n.Link,
		Actions: []notify.Action{}, BatchCount: n.BatchCount, CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt,
		Read: n.ReadAt != nil, Archived: n.ArchivedAt != nil || (n.ExpiresAt != nil && !n.ExpiresAt.After(time.Now())),
		Waiting: n.Waiting && n.ResolvedAt == nil,
	}
	if out.Waiting {
		_ = json.Unmarshal([]byte(n.Actions), &out.Actions)
	}
	if n.ResolvedAt != nil {
		out.Resolution = &resolutionJSON{Outcome: n.Resolution, By: names.actorName(n.ResolvedByID),
			ByMe: n.ResolvedByID != nil && *n.ResolvedByID == me, At: *n.ResolvedAt}
	}
	return out
}

// GET /notifications/badge -> {unread, waiting}; 304 when unchanged.
func (a *App) apiNotificationBadge(c *gin.Context) {
	unread, waiting, err := notify.Badge(a.DB, apiUser(c).ID)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load notifications")
		return
	}
	etag := fmt.Sprintf(`"%d-%d"`, unread, waiting)
	c.Header("ETag", etag)
	if c.GetHeader("If-None-Match") == etag {
		c.Status(http.StatusNotModified)
		return
	}
	c.JSON(http.StatusOK, badgeJSON{Unread: unread, Waiting: waiting})
}

// GET /notifications?filter=all|unread|waiting|archived&before=<cursor>&limit=20
func (a *App) apiNotifications(c *gin.Context) {
	me := apiUser(c).ID
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	page, ok, err := notify.Feed(a.DB, me, c.Query("filter"), c.Query("before"), limit)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load notifications")
		return
	}
	if !ok {
		jsonError(c, http.StatusBadRequest, "unknown filter or cursor")
		return
	}
	ids := []uint{}
	for _, rows := range [][]models.Notification{page.Waiting, page.Items} {
		for _, n := range rows {
			if n.ResolvedAt != nil && n.ResolvedByID != nil {
				ids = append(ids, *n.ResolvedByID)
			}
		}
	}
	names := publicNamesIn(a.DB, ids)
	conv := func(rows []models.Notification) []notificationJSON {
		out := make([]notificationJSON, 0, len(rows))
		for i := range rows {
			out = append(out, toNotificationJSON(&rows[i], me, names))
		}
		return out
	}
	c.JSON(http.StatusOK, gin.H{
		"waiting": conv(page.Waiting), "waitingTotal": page.WaitingTotal,
		"notifications": conv(page.Items), "next": page.Next,
	})
}

// notificationToggle serves POST|DELETE /notifications/:id/read and /archive.
func (a *App) notificationToggle(set func(db *gorm.DB, userID, id uint, on bool) (bool, error)) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
		me := apiUser(c).ID
		found, err := set(a.DB, me, uint(id), c.Request.Method == http.MethodPost)
		if err != nil {
			jsonError(c, http.StatusInternalServerError, "could not update notifications")
			return
		}
		if !found {
			jsonError(c, http.StatusNotFound, "notification not found")
			return
		}
		n, _ := notify.Get(a.DB, me, uint(id))
		ids := []uint{}
		if n.ResolvedAt != nil && n.ResolvedByID != nil {
			ids = append(ids, *n.ResolvedByID)
		}
		c.JSON(http.StatusOK, gin.H{"notification": toNotificationJSON(&n, me, publicNamesIn(a.DB, ids))})
	}
}

func (a *App) notificationBulk(do func(db *gorm.DB, userID uint) error) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := do(a.DB, apiUser(c).ID); err != nil {
			jsonError(c, http.StatusInternalServerError, "could not update notifications")
			return
		}
		c.JSON(http.StatusOK, gin.H{})
	}
}

func (a *App) registerNotificationRoutes(v1 *gin.RouterGroup) {
	n := v1.Group("/notifications", a.apiAuth())
	n.GET("", a.apiNotifications)
	n.GET("/badge", a.apiNotificationBadge)
	n.POST("/read-all", a.notificationBulk(notify.ReadAll))
	n.POST("/archive-read", a.notificationBulk(notify.ArchiveRead))
	n.POST("/:id/read", a.notificationToggle(notify.SetRead))
	n.DELETE("/:id/read", a.notificationToggle(notify.SetRead))
	n.POST("/:id/archive", a.notificationToggle(notify.SetArchived))
	n.DELETE("/:id/archive", a.notificationToggle(notify.SetArchived))
}

// fromNotification checks an inline action. A request that names one of
// the user's notifications must be for an allow-listed action on that
// kind, about this subject; anything else is refused (6.6, 6.13 #5).
// It reports false after answering.
func (a *App) fromNotification(c *gin.Context, id *uint, action string, subjectID uint) bool {
	if id == nil {
		return true
	}
	n, ok := notify.Get(a.DB, apiUser(c).ID, *id)
	if !ok {
		jsonError(c, http.StatusNotFound, "notification not found")
		return false
	}
	if !notify.Allows(n.Kind, action) || n.SubjectID == nil || *n.SubjectID != subjectID {
		fail(c, errOpenPageInstead)
		return false
	}
	return true
}

// alreadyHandled answers 409 with who resolved the task, so the row can say
// "Already approved by Pat" instead of an error (6.4).
func (a *App) alreadyHandled(c *gin.Context, kind string, subjectID uint) {
	by, outcome, _ := notify.LastResolution(a.DB, kind, subjectID)
	var ids []uint
	if by != nil {
		ids = []uint{*by}
	}
	c.JSON(http.StatusConflict, gin.H{
		"error": "This was already handled", "resolvedBy": publicNamesIn(a.DB, ids).actorName(by), "resolution": outcome,
	})
}
