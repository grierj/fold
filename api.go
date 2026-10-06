package main

import (
	"net/http"
	"strings"
	"time"

	"church-groups/models"

	"github.com/gin-gonic/gin"
)

// ---------------------------------------------------------------------------
// JSON shapes
// ---------------------------------------------------------------------------

type userJSON struct {
	ID       uint   `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Phone    string `json:"phone"`
	Role     string `json:"role"`
}

func toUserJSON(u *models.User) userJSON {
	return userJSON{ID: u.ID, Username: u.Username, Name: u.Name, Email: u.Email, Phone: u.Phone, Role: u.Role}
}

type groupSummaryJSON struct {
	ID            uint       `json:"id"`
	Name          string     `json:"name"`
	Description   string     `json:"description"`
	Tags          []string   `json:"tags"`
	ImageURL      string     `json:"imageUrl"`
	Location      string     `json:"location"`
	LeaderName    string     `json:"leaderName"`
	MemberCount   int64      `json:"memberCount"`
	NextMeetingAt *time.Time `json:"nextMeetingAt"`
	Status        string     `json:"status"`
}

type memberJSON struct {
	ID       uint   `json:"id"`
	Name     string `json:"name"`
	Role     string `json:"role"`
	JoinedAt string `json:"joinedAt"`
}

type meetingInstanceJSON struct {
	MeetingID uint             `json:"meetingId"`
	Date      string           `json:"date"`
	StartsAt  time.Time        `json:"startsAt"`
	EndsAt    time.Time        `json:"endsAt"`
	Location  string           `json:"location"`
	RSVPs     map[string]int64 `json:"rsvps"`
	MyRSVP    *string          `json:"myRsvp"`
}

type meetingJSON struct {
	ID             uint     `json:"id"`
	GroupID        uint     `json:"groupId"`
	StartsAt       time.Time `json:"startsAt"`
	EndsAt         time.Time `json:"endsAt"`
	Location       string   `json:"location"`
	RecurrenceRule string   `json:"recurrenceRule"`
	CancelledDates []string `json:"cancelledDates"`
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func jsonError(c *gin.Context, code int, msg string) {
	c.JSON(code, gin.H{"error": msg})
}

// apiAuth requires a logged-in user; 401 JSON otherwise.
func (a *App) apiAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		session, err := a.Store.Get(c.Request, sessionName)
		if err != nil {
			jsonError(c, http.StatusUnauthorized, "unauthorized")
			c.Abort()
			return
		}
		uid, ok := session.Values["userID"].(uint)
		if !ok {
			jsonError(c, http.StatusUnauthorized, "unauthorized")
			c.Abort()
			return
		}
		var user models.User
		if err := a.DB.First(&user, uid).Error; err != nil {
			jsonError(c, http.StatusUnauthorized, "unauthorized")
			c.Abort()
			return
		}
		c.Set("apiUser", &user)
		c.Next()
	}
}

// apiAdmin requires an admin user.
func (a *App) apiAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		u, ok := c.Get("apiUser")
		if !ok || !u.(*models.User).IsAdmin() {
			jsonError(c, http.StatusForbidden, "admin required")
			c.Abort()
			return
		}
		c.Next()
	}
}

func apiUser(c *gin.Context) *models.User {
	if u, ok := c.Get("apiUser"); ok {
		return u.(*models.User)
	}
	return nil
}

// optionalUser loads the user when logged in but never rejects.
func (a *App) optionalUser() gin.HandlerFunc {
	return func(c *gin.Context) {
		session, err := a.Store.Get(c.Request, sessionName)
		if err == nil {
			if uid, ok := session.Values["userID"].(uint); ok {
				var user models.User
				if err := a.DB.First(&user, uid).Error; err == nil {
					c.Set("apiUser", &user)
				}
			}
		}
		c.Next()
	}
}

// canManageGroup reports whether u is the group's leader or an admin.
func canManageGroup(a *App, u *models.User, groupID uint) bool {
	if u.IsAdmin() {
		return true
	}
	var m models.Membership
	err := a.DB.Where("user_id = ? AND group_id = ? AND role = ? AND status = ?",
		u.ID, groupID, models.MembershipLeader, "active").First(&m).Error
	return err == nil
}

func splitTags(s string) []string {
	out := []string{}
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func joinTags(tags []string) string {
	var clean []string
	for _, t := range tags {
		if t = strings.TrimSpace(t); t != "" {
			clean = append(clean, t)
		}
	}
	return strings.Join(clean, ",")
}

// ---------------------------------------------------------------------------
// Route registration
// ---------------------------------------------------------------------------

func (a *App) registerAPIRoutes(r *gin.Engine) {
	v1 := r.Group("/api/v1")

	// Auth (csrf token endpoint is public; login/logout need the token).
	v1.GET("/auth/csrf", a.apiGetCSRF)
	v1.POST("/auth/login", a.apiLogin)
	v1.POST("/auth/logout", a.apiLogout)
	v1.GET("/auth/me", a.apiAuth(), a.apiMe)

	// Groups.
	v1.GET("/groups", a.optionalUser(), a.apiListGroups)
	v1.GET("/me/groups", a.apiAuth(), a.apiMyGroups)
	v1.POST("/groups", a.apiAuth(), a.apiCreateGroup)
	v1.GET("/groups/:id", a.optionalUser(), a.apiGetGroup)
	v1.PUT("/groups/:id", a.apiAuth(), a.apiUpdateGroup)
	v1.POST("/groups/:id/join", a.apiAuth(), a.apiJoinGroup)
	v1.POST("/groups/:id/leave", a.apiAuth(), a.apiLeaveGroup)
	v1.GET("/groups/:id/members", a.apiAuth(), a.apiGroupMembers)
	v1.GET("/groups/:id/meetings", a.optionalUser(), a.apiListMeetings)
	v1.POST("/groups/:id/meetings", a.apiAuth(), a.apiCreateMeeting)

	// Meetings.
	v1.PUT("/meetings/:id", a.apiAuth(), a.apiUpdateMeeting)
	v1.DELETE("/meetings/:id", a.apiAuth(), a.apiDeleteMeeting)
	v1.POST("/meetings/:id/cancel", a.apiAuth(), a.apiCancelInstance)
	v1.PUT("/meetings/:id/rsvp", a.apiAuth(), a.apiRSVP)

	// Admin.
	v1.GET("/admin/pending", a.apiAuth(), a.apiAdmin(), a.apiPendingGroups)
	v1.POST("/admin/groups/:id/approve", a.apiAuth(), a.apiAdmin(), a.apiApproveGroup)
	v1.POST("/admin/groups/:id/reject", a.apiAuth(), a.apiAdmin(), a.apiRejectGroup)
}
