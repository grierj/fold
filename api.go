package main

import (
	"net/http"
	"strings"
	"time"

	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/sessions"
	"gorm.io/gorm"
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
	Name         string  `json:"name"`
	DisplayName  string  `json:"displayName"`
	Email        string  `json:"email"`
	Phone        string  `json:"phone"`
	AvatarURL    string  `json:"avatarUrl"`
	AvatarFocalX float64 `json:"avatarFocalX"`
	AvatarFocalY float64 `json:"avatarFocalY"`
	Role         string  `json:"role"`
	Status       string  `json:"status"`
	HasPassword  bool    `json:"hasPassword"`
	AccountType  string  `json:"accountType"`
	BirthDate    string  `json:"birthDate,omitempty"`
}

func (a *App) toUserJSON(u *models.User) userJSON {
	avatarURL := ""
	if u.AvatarKey != "" {
		avatarURL = a.Uploads.URL(u.AvatarKey)
	}
	accountType := u.AccountType
	if accountType == "" {
		accountType = models.AccountAdult
	}
	return userJSON{
		ID: u.ID, Name: u.Name,
		DisplayName: u.DisplayName, Email: u.Email, Phone: u.Phone,
		AvatarURL:    avatarURL,
		AvatarFocalX: u.AvatarFocalX, AvatarFocalY: u.AvatarFocalY,
		Role: u.Role, Status: u.Status, HasPassword: u.PasswordHash != nil,
		AccountType: accountType, BirthDate: formatBirthDate(u.BirthDate),
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
	NextEventAt       *time.Time `json:"nextEventAt"`
	NextEventLocal    string     `json:"nextEventLocal"`
	NextEventTzid     string     `json:"nextEventTzid"`
	Status            string     `json:"status"`
	LeaderID          uint       `json:"leaderId"`
	Visibility        string     `json:"visibility"` // public | private | invite_only
	DeletionRequested bool       `json:"deletionRequested"`
	// MemberLimit caps active members (DEC-39); null means unlimited.
	MemberLimit *int `json:"memberLimit"`
}

type memberJSON struct {
	ID           uint    `json:"id"`
	Name         string  `json:"name"`
	AvatarURL    string  `json:"avatarUrl"`
	AvatarFocalX float64 `json:"avatarFocalX"`
	AvatarFocalY float64 `json:"avatarFocalY"`
	Role         string  `json:"role"`
	JoinedAt     string  `json:"joinedAt"`
	// Admin marks church admins so leaders see the label and no Remove button
	// (DEC-38); the server refuses such removals anyway (DEC-35).
	Admin bool `json:"admin"`
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
		Admin: asPerson(&m.User).Admin,
	}
}

// occurrenceJSON is one date of an event. startsAt/endsAt are UTC;
// startsLocal/endsLocal are the wall clock in tzid (C2). date is the series
// date and stays put when a per-date override moves the clock.
type occurrenceJSON struct {
	ID                    uint             `json:"id"`
	EventID               uint             `json:"eventId"`
	Date                  string           `json:"date"`
	StartsAt              time.Time        `json:"startsAt"`
	EndsAt                time.Time        `json:"endsAt"`
	StartsLocal           string           `json:"startsLocal"`
	EndsLocal             string           `json:"endsLocal"`
	TZID                  string           `json:"tzid"`
	DurationMinutes       int              `json:"durationMinutes"`
	Location              string           `json:"location"`
	Title                 string           `json:"title"`
	Description           string           `json:"description"`
	RecurrenceRule        string           `json:"recurrenceRule"`
	RRuleUntil            string           `json:"rruleUntil,omitempty"`
	SeriesStartsLocal     string           `json:"seriesStartsLocal"`
	SeriesTitle           string           `json:"seriesTitle"`
	SeriesLocation        string           `json:"seriesLocation"`
	SeriesDurationMinutes int              `json:"seriesDurationMinutes"`
	Overridden            bool             `json:"overridden"`
	Registrations         map[string]int64 `json:"registrations"`
	MyRegistration        *string          `json:"myRegistration"`
	Visibility            string           `json:"visibility"`
	LeaderOpened          bool             `json:"leaderOpened"`
	ChurchWide            bool             `json:"churchWide"`
	GroupID               *uint            `json:"groupId,omitempty"`
	GroupName             string           `json:"groupName,omitempty"`
	HostedBy              string           `json:"hostedBy,omitempty"`
	// AttendeesVisible is false when the caller gets a count only. An empty
	// allowed list is omitted; the bool is what says names were allowed.
	AttendeesVisible bool           `json:"attendeesVisible"`
	Attendees        []attendeeJSON `json:"attendees,omitempty"`
}

type attendeeJSON struct {
	UserID uint   `json:"userId"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type hostJSON struct {
	UserID uint   `json:"userId"`
	Name   string `json:"name"`
	Email  string `json:"email"`
}

type eventJSON struct {
	ID              uint       `json:"id"`
	GroupID         *uint      `json:"groupId,omitempty"`
	GroupName       string     `json:"groupName,omitempty"`
	GroupVisibility string     `json:"groupVisibility,omitempty"`
	HostedBy        string     `json:"hostedBy,omitempty"`
	Title           string     `json:"title"`
	Description     string     `json:"description"`
	StartsLocal     string     `json:"startsLocal"`
	DurationMinutes int        `json:"durationMinutes"`
	TZID            string     `json:"tzid"`
	StartsAt        time.Time  `json:"startsAt"`
	EndsAt          time.Time  `json:"endsAt"`
	Location        string     `json:"location"`
	RecurrenceRule  string     `json:"recurrenceRule"`
	RRuleUntil      string     `json:"rruleUntil,omitempty"`
	Visibility      string     `json:"visibility"`
	LeaderOpened    bool       `json:"leaderOpened"`
	ChurchWide      bool       `json:"churchWide"`
	Hosts           []hostJSON `json:"hosts,omitempty"`
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
	if sessionVersion(session) != user.SessionVersion || user.Status != models.UserActive {
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
		if !ok || !policy.IsAdmin(asPerson(u.(*models.User))) {
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

// ---------------------------------------------------------------------------
// Policy glue: load the facts, then ask internal/policy (C12)
// ---------------------------------------------------------------------------

// asPerson describes the signed-in user to policy; nil means signed out.
func asPerson(u *models.User) *policy.Person {
	if u == nil {
		return nil
	}
	// A disabled admin has no admin powers (and can't sign in anyway).
	return &policy.Person{
		ID: u.ID, Admin: u.Role == models.RoleAdmin && u.Status == models.UserActive,
		Teen: u.AccountType == models.AccountTeen,
	}
}

func asGroup(g *models.Group) policy.Group {
	return policy.Group{Status: g.Status, Visibility: g.Visibility}
}

// relOf loads u's active membership in a group.
func (a *App) relOf(u *models.User, groupID uint) policy.Rel {
	if u == nil {
		return policy.Rel{}
	}
	return relIn(a.DB, u.ID, groupID)
}

// membershipRel turns a membership row (any status) into policy facts.
func membershipRel(m models.Membership) policy.Rel {
	active := m.ID != 0 && m.Status == "active"
	return policy.Rel{
		Member:  active,
		Leader:  active && m.Role == models.MembershipLeader,
		Removed: m.ID != 0 && m.Status == memberRemoved,
	}
}

// memberRemoved is the Membership.Status of someone a leader or admin
// removed (DEC-35).
const memberRemoved = "removed"

// listedUsers joins memberships or registrations (the table name) to accounts
// that are listed: not deleted and not disabled. A disabled account keeps its
// memberships and registrations, but they are hidden and never counted (DEC-38).
func listedUsers(table string) string {
	return "JOIN users ON users.id = " + table + ".user_id AND users.deleted_at IS NULL AND users.status <> 'disabled'"
}

// leaderCount counts a group's active leaders whose accounts still exist
// and aren't disabled.
func leaderCount(db *gorm.DB, groupID uint) int {
	var n int64
	db.Model(&models.Membership{}).
		Joins(listedUsers("memberships")).
		Where("memberships.group_id = ? AND memberships.role = ? AND memberships.status = ?",
			groupID, models.MembershipLeader, "active").
		Count(&n)
	return int(n)
}

func (a *App) canSeeGroup(u *models.User, g *models.Group) bool {
	return policy.CanSeeGroup(asPerson(u), asGroup(g), a.relOf(u, g.ID))
}

// loadGroupFor loads group id for u: 404 when it doesn't exist or u can't
// see it (invite-only groups never answer 403 to outsiders).
func (a *App) loadGroupFor(c *gin.Context, id uint) (*models.Group, policy.Rel, bool) {
	u := apiUser(c)
	var g models.Group
	if err := a.DB.First(&g, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "group not found")
		return nil, policy.Rel{}, false
	}
	rel := a.relOf(u, g.ID)
	if !policy.CanSeeGroup(asPerson(u), asGroup(&g), rel) {
		jsonError(c, http.StatusNotFound, "group not found")
		return nil, policy.Rel{}, false
	}
	return &g, rel, true
}

// loadManagedGroupID is loadGroupFor plus CanManageGroup (403 when the
// group is visible but u doesn't lead it).
func (a *App) loadManagedGroupID(c *gin.Context, id uint) (*models.Group, policy.Rel, bool) {
	g, rel, ok := a.loadGroupFor(c, id)
	if !ok {
		return nil, rel, false
	}
	if !policy.CanManageGroup(asPerson(apiUser(c)), rel) {
		jsonError(c, http.StatusForbidden, "forbidden")
		return nil, rel, false
	}
	return g, rel, true
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
	// API responses carry per-user data: never store them in the HTTP cache
	// or let back/forward replay them after logout.
	v1.Use(func(c *gin.Context) { c.Header("Cache-Control", "no-store") })

	// Auth (csrf token endpoint is public; login/logout need the token).
	v1.GET("/auth/csrf", a.apiGetCSRF)
	v1.POST("/auth/login", a.apiLogin)
	v1.POST("/auth/logout", a.apiLogout)
	v1.GET("/auth/me", a.apiAuth(), a.apiMe)
	// Password reset (M1.2): uniform response, single-use expiring tokens.
	v1.POST("/auth/password-reset", a.apiRequestPasswordReset)
	v1.POST("/auth/password-reset/confirm", a.apiConfirmPasswordReset)

	// Profile.
	v1.PUT("/me", a.apiAuth(), a.apiUpdateMe)
	v1.POST("/me/password", a.apiAuth(), a.apiChangePassword)
	v1.POST("/me/avatar", a.apiAuth(), a.apiUploadAvatar)
	v1.DELETE("/me/avatar", a.apiAuth(), a.apiDeleteAvatar)
	// Verified email change (M1.2): the token is the credential.
	v1.POST("/me/email/verify", a.apiVerifyEmailChange)

	// Invitations (M1.2): admins and leaders invite; the person accepts
	// with a token and sets a password. No approval step.
	v1.POST("/invitations", a.apiAuth(), a.apiCreateInvitation)
	v1.GET("/invitations/:token", a.apiGetInvitation)
	v1.POST("/invitations/accept", a.apiAcceptInvitation)

	// Account requests (M1.3): self-signup (public) and member
	// invite-a-friend create a pending request an admin decides.
	v1.POST("/account-requests", a.apiCreateAccountRequest)
	v1.POST("/account-requests/invite-friend", a.apiAuth(), a.apiInviteFriend)
	v1.POST("/account-requests/confirm", a.apiConfirmAccountSetup)
	v1.GET("/admin/account-requests", a.apiAuth(), a.apiAdmin(), a.apiListAccountRequests)
	v1.POST("/admin/account-requests/:id/approve", a.apiAuth(), a.apiAdmin(), a.apiApproveAccountRequest)
	v1.POST("/admin/account-requests/:id/reject", a.apiAuth(), a.apiAdmin(), a.apiRejectAccountRequest)
	v1.PUT("/admin/self-signup", a.apiAuth(), a.apiAdmin(), a.apiSetSelfSignup)

	// Groups.
	v1.GET("/groups", a.optionalUser(), a.apiListGroups)
	v1.GET("/me/groups", a.apiAuth(), a.apiMyGroups)
	v1.POST("/groups", a.apiAuth(), a.apiCreateGroup)
	v1.GET("/groups/:id", a.optionalUser(), a.apiGetGroup)
	v1.PUT("/groups/:id", a.apiAuth(), a.apiUpdateGroup)
	v1.POST("/groups/:id/join", a.apiAuth(), a.apiJoinGroup)
	v1.POST("/groups/:id/leave", a.apiAuth(), a.apiLeaveGroup)
	v1.PUT("/groups/:id/members/:userId/role", a.apiAuth(), a.apiSetMemberRole)
	v1.POST("/groups/:id/members", a.apiAuth(), a.apiAddGroupMember)
	v1.DELETE("/groups/:id/members/:userId", a.apiAuth(), a.apiRemoveGroupMember)
	v1.POST("/groups/:id/deletion-request", a.apiAuth(), a.apiRequestDeletion)
	v1.DELETE("/groups/:id/deletion-request", a.apiAuth(), a.apiCancelDeletion)
	v1.GET("/groups/:id/members", a.apiAuth(), a.apiGroupMembers)
	v1.GET("/groups/:id/events", a.optionalUser(), a.apiListEvents)
	v1.POST("/groups/:id/events", a.apiAuth(), a.apiCreateEvent)
	v1.POST("/groups/:id/cover", a.apiAuth(), a.apiUploadGroupCover)
	v1.DELETE("/groups/:id/cover", a.apiAuth(), a.apiDeleteGroupCover)

	// Events. A series edit (PUT) moves future dates; a per-date edit leaves
	// that date alone the next time the series changes (M1.1a). Church-wide
	// events have no group (M1.1b). GET /events is the home list of events
	// the viewer can see without seeing their group.
	v1.GET("/site", a.apiSite)
	v1.PUT("/admin/timezone", a.apiAuth(), a.apiAdmin(), a.apiSetTimezone)
	v1.GET("/events", a.optionalUser(), a.apiListOpenEvents)
	v1.POST("/events", a.apiAuth(), a.apiAdmin(), a.apiCreateChurchEvent)
	v1.GET("/events/:id", a.optionalUser(), a.apiGetEvent)
	v1.PUT("/events/:id", a.apiAuth(), a.apiUpdateEvent)
	v1.DELETE("/events/:id", a.apiAuth(), a.apiDeleteEvent)
	v1.PUT("/events/:id/visibility", a.apiAuth(), a.apiSetEventVisibility)
	v1.GET("/groups/:id/visibility-impact", a.apiAuth(), a.apiGroupVisibilityImpact)
	v1.GET("/events/:id/visibility-impact", a.apiAuth(), a.apiEventVisibilityImpact)
	v1.POST("/events/:id/hosts", a.apiAuth(), a.apiAdmin(), a.apiAddEventHost)
	v1.DELETE("/events/:id/hosts/:userId", a.apiAuth(), a.apiAdmin(), a.apiRemoveEventHost)
	v1.POST("/events/:id/cancel", a.apiAuth(), a.apiCancelOccurrence)
	v1.PUT("/events/:id/occurrences/:date", a.apiAuth(), a.apiUpdateOccurrence)
	v1.PUT("/events/:id/registrations", a.apiAuth(), a.apiRegister)

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
	v1.GET("/admin/users/:id/sole-leader-groups", a.apiAuth(), a.apiAdmin(), a.apiAdminSoleLedGroups)
	v1.DELETE("/admin/users/:id", a.apiAuth(), a.apiAdmin(), a.apiAdminDeleteUser)
	v1.POST("/admin/users/:id/password", a.apiAuth(), a.apiAdmin(), a.apiAdminResetPassword)

	// Notifications (5.12).
	a.registerNotificationRoutes(v1)
}
