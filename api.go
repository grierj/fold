package main

import (
	"net/http"
	"strings"
	"time"

	"fold/models"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/sessions"
)

// minPasswordLength is the minimum for local-account passwords, enforced
// at setup, admin creation, password change, and admin password reset.
// The setup template states the requirement next to the field — keep them in sync.
const minPasswordLength = 10

// Text caps keep a single request from storing an unbounded row.
// Profile name/display name stay at 100 (see api_profile.go).
const (
	maxGroupNameLen   = 200
	maxLocationLen    = 300
	maxDescriptionLen = 5000
	maxTagsLen        = 500
	maxRecurrenceLen  = 300
)

// ---------------------------------------------------------------------------
// JSON shapes
// ---------------------------------------------------------------------------

type userJSON struct {
	ID           uint    `json:"id"`
	Username     string  `json:"username"`
	Name         string  `json:"name"`
	DisplayName  string  `json:"displayName"`
	Email        string  `json:"email"`
	Phone        string  `json:"phone"`
	AvatarURL    string  `json:"avatarUrl"`
	AvatarFocalX float64 `json:"avatarFocalX"`
	AvatarFocalY float64 `json:"avatarFocalY"`
	Role         string  `json:"role"`
}

func (a *App) toUserJSON(u *models.User) userJSON {
	avatarURL := ""
	if u.AvatarKey != "" {
		avatarURL = a.Uploads.URL(u.AvatarKey)
	}
	return userJSON{
		ID: u.ID, Username: u.Username, Name: u.Name,
		DisplayName: u.DisplayName, Email: u.Email, Phone: u.Phone,
		AvatarURL:    avatarURL,
		AvatarFocalX: u.AvatarFocalX, AvatarFocalY: u.AvatarFocalY,
		Role: u.Role,
	}
}

type groupSummaryJSON struct {
	ID                uint       `json:"id"`
	Name              string     `json:"name"`
	Description       string     `json:"description"`
	Tags              []string   `json:"tags"`
	ImageURL          string     `json:"imageUrl"`
	CoverFocalX       float64    `json:"coverFocalX"`
	CoverFocalY       float64    `json:"coverFocalY"`
	Location          string     `json:"location"`
	LeaderName        string     `json:"leaderName"`
	MemberCount       int64      `json:"memberCount"`
	NextMeetingAt     *time.Time `json:"nextMeetingAt"`
	Status            string     `json:"status"`
	LeaderID          uint       `json:"leaderId"`
	Visibility        string     `json:"visibility"`
	InviteOnly        bool       `json:"inviteOnly"`
	DeletionRequested bool       `json:"deletionRequested"`
}

type memberJSON struct {
	ID           uint    `json:"id"`
	Name         string  `json:"name"`
	AvatarURL    string  `json:"avatarUrl"`
	AvatarFocalX float64 `json:"avatarFocalX"`
	AvatarFocalY float64 `json:"avatarFocalY"`
	Role         string  `json:"role"`
	JoinedAt     string  `json:"joinedAt"`
}

// toMemberJSON renders a membership's user for the roster.
func (a *App) toMemberJSON(m *models.Membership) memberJSON {
	avatarURL := ""
	if m.User.AvatarKey != "" {
		avatarURL = a.Uploads.URL(m.User.AvatarKey)
	}
	return memberJSON{
		ID: m.User.ID, Name: m.User.PublicName(), AvatarURL: avatarURL,
		AvatarFocalX: m.User.AvatarFocalX, AvatarFocalY: m.User.AvatarFocalY,
		Role: m.Role, JoinedAt: m.JoinedAt.Format(time.RFC3339),
	}
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
	ID             uint      `json:"id"`
	GroupID        uint      `json:"groupId"`
	StartsAt       time.Time `json:"startsAt"`
	EndsAt         time.Time `json:"endsAt"`
	Location       string    `json:"location"`
	RecurrenceRule string    `json:"recurrenceRule"`
	CancelledDates []string  `json:"cancelledDates"`
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func jsonError(c *gin.Context, code int, msg string) {
	c.JSON(code, gin.H{"error": msg})
}

// sessionVersion reads the password generation stored at login.
// A cookie issued before this field existed has no value and counts as 0.
func sessionVersion(s *sessions.Session) int {
	switch v := s.Values["sessionVersion"].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case uint:
		return int(v)
	default:
		return 0
	}
}

// userFromSession loads the logged-in user when the cookie is still current.
func (a *App) userFromSession(c *gin.Context) (*models.User, bool) {
	session, err := a.Store.Get(c.Request, sessionName)
	if err != nil {
		return nil, false
	}
	uid, ok := session.Values["userID"].(uint)
	if !ok {
		return nil, false
	}
	var user models.User
	if err := a.DB.First(&user, uid).Error; err != nil {
		return nil, false
	}
	if sessionVersion(session) != user.SessionVersion {
		return nil, false
	}
	return &user, true
}

// apiAuth requires a logged-in user; 401 JSON otherwise.
func (a *App) apiAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		user, ok := a.userFromSession(c)
		if !ok {
			jsonError(c, http.StatusUnauthorized, "unauthorized")
			c.Abort()
			return
		}
		c.Set("apiUser", user)
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
		if user, ok := a.userFromSession(c); ok {
			c.Set("apiUser", user)
		}
		c.Next()
	}
}

// canSeeGroup reports whether u may open this group.
// Active public groups are open to everyone. Active private groups require
// a signed-in user. Invite-only groups require a member, the leader, or an
// admin. Pending and archived groups stay limited to the leader and admins.
func (a *App) canSeeGroup(u *models.User, g *models.Group) bool {
	if g.Status != models.GroupActive {
		return u != nil && (u.IsAdmin() || g.LeaderID == u.ID)
	}
	if g.InviteOnly {
		if u == nil {
			return false
		}
		return u.IsAdmin() || g.LeaderID == u.ID || a.isMember(u.ID, g.ID)
	}
	if g.Visibility == models.VisibilityPrivate {
		return u != nil
	}
	return true
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

	// Profile.
	v1.PUT("/me", a.apiAuth(), a.apiUpdateMe)
	v1.POST("/me/password", a.apiAuth(), a.apiChangePassword)
	v1.POST("/me/avatar", a.apiAuth(), a.apiUploadAvatar)
	v1.POST("/me/avatar/url", a.apiAuth(), a.apiFetchAvatar)
	v1.DELETE("/me/avatar", a.apiAuth(), a.apiDeleteAvatar)

	// Groups.
	v1.GET("/groups", a.optionalUser(), a.apiListGroups)
	v1.GET("/me/groups", a.apiAuth(), a.apiMyGroups)
	v1.POST("/groups", a.apiAuth(), a.apiCreateGroup)
	v1.GET("/groups/:id", a.optionalUser(), a.apiGetGroup)
	v1.PUT("/groups/:id", a.apiAuth(), a.apiUpdateGroup)
	v1.POST("/groups/:id/join", a.apiAuth(), a.apiJoinGroup)
	v1.POST("/groups/:id/leave", a.apiAuth(), a.apiLeaveGroup)
	v1.POST("/groups/:id/leader", a.apiAuth(), a.apiTransferLeadership)
	v1.POST("/groups/:id/members", a.apiAuth(), a.apiAddGroupMember)
	v1.POST("/groups/:id/deletion-request", a.apiAuth(), a.apiRequestDeletion)
	v1.DELETE("/groups/:id/deletion-request", a.apiAuth(), a.apiCancelDeletion)
	v1.GET("/groups/:id/members", a.apiAuth(), a.apiGroupMembers)
	v1.GET("/groups/:id/meetings", a.optionalUser(), a.apiListMeetings)
	v1.POST("/groups/:id/meetings", a.apiAuth(), a.apiCreateMeeting)
	v1.POST("/groups/:id/cover", a.apiAuth(), a.apiUploadGroupCover)
	v1.DELETE("/groups/:id/cover", a.apiAuth(), a.apiDeleteGroupCover)

	// Meetings.
	v1.PUT("/meetings/:id", a.apiAuth(), a.apiUpdateMeeting)
	v1.DELETE("/meetings/:id", a.apiAuth(), a.apiDeleteMeeting)
	v1.POST("/meetings/:id/cancel", a.apiAuth(), a.apiCancelInstance)
	v1.PUT("/meetings/:id/rsvp", a.apiAuth(), a.apiRSVP)

	// Admin.
	v1.GET("/admin/pending", a.apiAuth(), a.apiAdmin(), a.apiPendingGroups)
	v1.GET("/admin/deletions", a.apiAuth(), a.apiAdmin(), a.apiDeletionRequests)
	v1.POST("/admin/groups/:id/approve", a.apiAuth(), a.apiAdmin(), a.apiApproveGroup)
	v1.POST("/admin/groups/:id/reject", a.apiAuth(), a.apiAdmin(), a.apiRejectGroup)
	v1.POST("/admin/groups/:id/reject-deletion", a.apiAuth(), a.apiAdmin(), a.apiRejectDeletion)
	v1.DELETE("/admin/groups/:id", a.apiAuth(), a.apiAdmin(), a.apiAdminDeleteGroup)
	v1.GET("/admin/groups", a.apiAuth(), a.apiAdmin(), a.apiAdminListGroups)
	v1.POST("/admin/groups/:id/members", a.apiAuth(), a.apiAdmin(), a.apiAdminAddMember)
	v1.DELETE("/admin/groups/:id/members/:userId", a.apiAuth(), a.apiAdmin(), a.apiAdminRemoveMember)
	v1.GET("/admin/users", a.apiAuth(), a.apiAdmin(), a.apiAdminListUsers)
	v1.POST("/admin/users", a.apiAuth(), a.apiAdmin(), a.apiAdminCreateUser)
	v1.PUT("/admin/users/:id", a.apiAuth(), a.apiAdmin(), a.apiAdminUpdateUser)
	v1.DELETE("/admin/users/:id", a.apiAuth(), a.apiAdmin(), a.apiAdminDeleteUser)
	v1.POST("/admin/users/:id/password", a.apiAuth(), a.apiAdmin(), a.apiAdminResetPassword)

	// Notifications.
	v1.GET("/notifications", a.apiAuth(), a.apiNotifications)
}
