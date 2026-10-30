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

// personSearchJSON is a deliberately closed name-picker projection. No contact
// data, internal account IDs, teen images or hidden group identifiers belong here.
type personSearchJSON struct {
	SelectionID  string   `json:"selectionId"`
	DisplayName  string   `json:"displayName"`
	Teen         bool     `json:"teen"`
	AvatarURL    string   `json:"avatarUrl,omitempty"`
	AvatarFocalX *float64 `json:"avatarFocalX,omitempty"`
	AvatarFocalY *float64 `json:"avatarFocalY,omitempty"`
	SharedGroups []string `json:"sharedGroups"`
	OtherGroups  []string `json:"otherGroups"`
}

type peopleSearchJSON struct {
	State  string             `json:"state"` // short_query | throttled | no_results | results
	People []personSearchJSON `json:"people"`
}

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
	// Admin list only. Kept after age rollover so consent history is reachable.
	HasTeenConsentHistory bool `json:"hasTeenConsentHistory,omitempty"`
	// Effective preference: always true for a teen, regardless of stored value.
	HideContactFromLeaders bool `json:"hideContactFromLeaders"`
	// Timezone is the person's own zone for emails (DEC-58); "" means
	// the church's, until the browser fills it in.
	Timezone string `json:"timezone"`
}

// toUserJSON is unrestricted: only self/session and admin account endpoints
// may use it. Rosters and attendees use context-filtered contact presentation.
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
		Timezone:               u.Timezone,
		HideContactFromLeaders: u.AccountType == models.AccountTeen || u.HideContactFromLeaders,
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
	Status            string     `json:"status"`
	LeaderID          uint       `json:"leaderId"`
	Visibility        string     `json:"visibility"` // public | private | invite_only
	DeletionRequested bool       `json:"deletionRequested"`
	// MemberLimit caps active members (DEC-39); null means unlimited.
	MemberLimit *int `json:"memberLimit"`
	// MyRole is the viewer's own active role, "leader" or "member", on the
	// directory and My groups lists only (DEC-94); absent otherwise.
	MyRole string `json:"myRole,omitempty"`
}

type memberJSON struct {
	Email        *string `json:"email,omitempty"`
	Phone        *string `json:"phone,omitempty"`
	AccountType  string  `json:"accountType,omitempty"`
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
	if m.User.AvatarKey != "" && m.User.AccountType != models.AccountTeen {
		avatarURL = a.Uploads.URL(m.User.AvatarKey)
	}
	return memberJSON{
		ID: m.User.ID, Name: m.User.PublicName(), AvatarURL: avatarURL,
		AvatarFocalX: m.User.AvatarFocalX, AvatarFocalY: m.User.AvatarFocalY,
		Role: m.Role, JoinedAt: m.JoinedAt.Format(time.RFC3339),
		Admin: asPerson(&m.User).Admin, AccountType: m.User.AccountType,
	}
}

// occurrenceJSON is one date of an event. startsAt/endsAt are UTC;
// startsLocal/endsLocal are the wall clock in tzid (C2). date is the series
// date and stays put when a per-date override moves the clock.
type occurrenceJSON struct {
	hideSeries            bool             `json:"-"`
	ID                    uint             `json:"id"`
	EventID               uint             `json:"eventId"`
	Date                  string           `json:"date"`
	DateURL               string           `json:"dateUrl,omitempty"`
	VisibilityOverride    *string          `json:"visibilityOverride,omitempty"`
	SeriesVisibility      string           `json:"seriesVisibility,omitempty"`
	GroupVisibility       string           `json:"groupVisibility,omitempty"`
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
	AttendeesVisible         bool                          `json:"attendeesVisible"`
	Attendees                []attendeeJSON                `json:"attendees,omitempty"`
	MyHouseholdRegistrations []myHouseholdRegistrationJSON `json:"myHouseholdRegistrations"`
	RemovedRegistrations     []attendeeJSON                `json:"removedRegistrations,omitempty"`
	// Capacity, waitlist, and guests (M1.5). Capacity is the effective
	// cap for this date (per-date override wins); null means unlimited.
	// SpotsLeft is null when unlimited and never below 0. SeatsUsed
	// counts seats (1 + guest_count per going party of a listed
	// account); the Registrations map counts rows.
	Capacity        *int `json:"capacity"`
	SeatsUsed       int  `json:"seatsUsed"`
	SpotsLeft       *int `json:"spotsLeft"`
	WaitlistEnabled bool `json:"waitlistEnabled"`
	GuestsAllowed   int  `json:"guestsAllowed"`
	// Remote attendance (M1.8 slice 2): a second seat pool. The remote
	// fields mirror the in-person ones; remoteCapacity is the effective
	// cap for this date (per-date override wins), null is unlimited.
	RemoteAvailable     bool    `json:"remoteAvailable"`
	ChildcareAvailable  bool    `json:"childcareAvailable"`
	ChildcareFull       bool    `json:"childcareFull"`
	ChildcareNotes      string  `json:"childcareNotes"`
	ChildcareCapacity   *int    `json:"childcareCapacity"`
	ChildcareAges       string  `json:"childcareAges"`
	RemoteCapacity      *int    `json:"remoteCapacity"`
	RemoteSeatsUsed     int     `json:"remoteSeatsUsed"`
	ChildcareUsed       *int    `json:"childcareUsed,omitempty"`
	ChildcareSpotsLeft  *int    `json:"childcareSpotsLeft,omitempty"`
	MyChildcareCount    *int    `json:"myChildcareCount,omitempty"`
	MyChildcareAgesNote *string `json:"myChildcareAgesNote,omitempty"`
	RemoteSpotsLeft     *int    `json:"remoteSpotsLeft"`
	// MyGuestCount and MyWaitlistPosition describe the signed-in
	// viewer's own registration; both are absent without one. Position
	// is set only when the viewer is waitlisted.
	MyGuestCount       *int `json:"myGuestCount,omitempty"`
	MyWaitlistPosition *int `json:"myWaitlistPosition,omitempty"`
	// MyAttendanceMode is the viewer's own pool (in_person or remote);
	// absent without a registration.
	MyAttendanceMode *string `json:"myAttendanceMode,omitempty"`
	// Video link (5.8, M1.8). VideoURL is present only when this viewer
	// may see it for this date (policy.CanSeeVideoLink); VideoHidden says
	// a link exists but is being withheld, so the UI can say "Register
	// to get the link".
	VideoProvider string `json:"videoProvider"`
	VideoURL      string `json:"videoUrl,omitempty"`
	VideoReveal   string `json:"videoReveal"`
	VideoHidden   bool   `json:"videoHidden,omitempty"`
}

type attendeeJSON struct {
	AddedByLeader      *bool                  `json:"addedByLeader,omitempty"`
	Email              *string                `json:"email,omitempty"`
	Phone              *string                `json:"phone,omitempty"`
	AccountType        string                 `json:"accountType,omitempty"`
	RegistrationID     uint                   `json:"registrationId"`
	HouseholdPerson    *householdAttendeeJSON `json:"householdPerson,omitempty"`
	ChildcareRequested *bool                  `json:"childcareRequested,omitempty"`
	UserID             *uint                  `json:"userId,omitempty"`
	Name               string                 `json:"name"`
	Status             string                 `json:"status"`
	// GuestCount is the extra unnamed guests this party brings (M1.5).
	GuestCount int `json:"guestCount"`
	// AttendanceMode is in_person or remote (M1.8 slice 2).
	AttendanceMode    string  `json:"attendanceMode"`
	ChildcareCount    *int    `json:"childcareCount,omitempty"`
	ChildcareAgesNote *string `json:"childcareAgesNote,omitempty"`
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
	// Capacity, waitlist, and guests (M1.5). Capacity null means
	// unlimited.
	Capacity        *int `json:"capacity"`
	WaitlistEnabled bool `json:"waitlistEnabled"`
	GuestsAllowed   int  `json:"guestsAllowed"`
	// Remote attendance (M1.8 slice 2). RemoteCapacity null means
	// unlimited remote seats.
	RemoteAvailable    bool   `json:"remoteAvailable"`
	ChildcareAvailable bool   `json:"childcareAvailable"`
	ChildcareNotes     string `json:"childcareNotes"`
	ChildcareCapacity  *int   `json:"childcareCapacity"`
	ChildcareAges      string `json:"childcareAges"`
	RemoteCapacity     *int   `json:"remoteCapacity"`
	// Video link (5.8, M1.8). At series level the link shows under
	// "always" and to the event's managers; a person going to one date
	// gets it on that date's occurrence row instead.
	VideoProvider string `json:"videoProvider"`
	VideoURL      string `json:"videoUrl,omitempty"`
	VideoReveal   string `json:"videoReveal"`
	VideoHidden   bool   `json:"videoHidden,omitempty"`
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

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
func (a *App) authenticatedSessionUser(c *gin.Context) (*models.User, bool) {
	if value, cached := c.Get("sessionUser"); cached {
		user, ok := value.(*models.User)
		return user, ok
	}
	c.Set("sessionUser", nil)
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
	if sessionVersion(session) != user.SessionVersion || !user.CanSignIn() {
		return nil, false
	}
	c.Set("sessionUser", &user)
	return &user, true
}

func (a *App) userFromSession(c *gin.Context) (*models.User, bool) {
	user, ok := a.authenticatedSessionUser(c)
	return user, ok && user.Status == models.UserActive
}

func (a *App) sessionAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		user, ok := a.authenticatedSessionUser(c)
		if !ok {
			jsonError(c, http.StatusUnauthorized, "unauthorized")
			c.Abort()
			return
		}
		c.Set("apiUser", user)
		c.Next()
	}
}

// apiAuth requires a logged-in user; 401 JSON otherwise.
func (a *App) apiAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		user, ok := a.userFromSession(c)
		if !ok {
			if err := a.auditContactRejection(c, "authentication"); err != nil {
				jsonError(c, http.StatusInternalServerError, "could not complete contact operation")
				c.Abort()
				return
			}
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

// listedUsers is the account-only membership join: not deleted or disabled.
// Registration queries use listedRegistrations for their two subject types.
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
	v1.GET("/auth/me", a.sessionAuth(), a.apiMe)
	// Password reset (M1.2): uniform response, single-use expiring tokens.
	v1.POST("/auth/password-reset", a.apiRequestPasswordReset)
	v1.POST("/auth/password-reset/confirm", a.apiConfirmPasswordReset)

	v1.GET("/people/search", a.apiAuth(), a.apiSearchPeople)

	// Direct contact: authenticated review, explicit audited disclosure only.
	v1.POST("/add-requests", a.apiAuth(), a.apiCreateAddRequest)
	v1.GET("/add-requests", a.apiAuth(), a.apiListAddRequests)
	v1.GET("/add-requests/:id", a.apiAuth(), a.apiGetAddRequest)
	v1.POST("/add-requests/:id/decision", a.apiAuth(), a.apiDecideAddRequest)
	v1.POST("/add-requests/:id/withdraw", a.apiAuth(), a.apiWithdrawAddRequest)
	v1.POST("/add-requests/:id/complete", a.apiAuth(), a.apiCompleteAddRequest)
	v1.POST("/contact-requests", a.apiAuth(), a.apiCreateContactRequest)
	v1.GET("/contact-requests", a.apiAuth(), a.apiListContactRequests)
	v1.GET("/contact-requests/:id", a.apiAuth(), a.apiGetContactRequest)
	v1.POST("/contact-requests/:id/decision", a.apiAuth(), a.apiDecideContactRequest)
	v1.POST("/contact-requests/:id/withdraw", a.apiAuth(), a.apiWithdrawContactRequest)
	v1.GET("/admin/contact-history/people", a.apiAuth(), a.apiContactHistoryPeople)
	v1.GET("/admin/users/:id/contact-history", a.apiAuth(), a.apiContactHistory)
	v1.GET("/admin/users/:id/contact-history.csv", a.apiAuth(), a.apiExportContactHistory)
	v1.GET("/contact-grants", a.apiAuth(), a.apiListContactGrants)
	v1.POST("/contact-grants/:id/revoke", a.apiAuth(), a.apiRevokeContactGrant)
	v1.POST("/contact-grants/:id/reveal", a.apiAuth(), a.apiRevealContactGrant)

	// Profile.
	v1.PUT("/me", a.apiAuth(), a.apiUpdateMe)
	v1.POST("/me/password", a.apiAuth(), a.apiChangePassword)
	v1.POST("/me/avatar", a.apiAuth(), a.apiUploadAvatar)
	v1.DELETE("/me/avatar", a.apiAuth(), a.apiDeleteAvatar)
	// Verified email change (M1.2): the token is the credential.
	v1.POST("/me/email/verify", a.apiVerifyEmailChange)
	v1.POST("/me/email/cancel", a.apiCancelEmailChange)
	// Calendar feed link (M1.7): the raw token comes back only from POST.
	v1.GET("/me/notification-settings", a.apiAuth(), a.apiGetNotificationSettings)
	v1.PUT("/me/notification-settings", a.apiAuth(), a.apiUpdateNotificationSettings)
	v1.GET("/me/calendar-feed", a.apiAuth(), a.apiGetMyCalendarFeed)
	v1.POST("/me/calendar-feed", a.apiAuth(), a.apiCreateMyCalendarFeed)
	v1.PUT("/me/calendar-feed", a.apiAuth(), a.apiUpdateMyCalendarFeed)
	v1.DELETE("/me/calendar-feed", a.apiAuth(), a.apiDeleteMyCalendarFeed)
	v1.DELETE("/admin/users/:id/calendar-feed", a.apiAuth(), a.apiAdmin(), a.apiAdminRevokeCalendarFeed)
	// My household (M1.9 slice 1): GET creates the household on first
	// use; a child or teen person starts a pending guardian link an
	// admin confirms below (DEC-32).
	v1.GET("/me/household", a.apiAuth(), a.apiGetMyHousehold)
	v1.PUT("/me/household", a.apiAuth(), a.apiUpdateMyHousehold)
	v1.POST("/me/household/people", a.apiAuth(), a.apiAddHouseholdPerson)
	v1.PUT("/me/household/people/:id", a.apiAuth(), a.apiUpdateHouseholdPerson)
	v1.DELETE("/me/household/people/:id", a.apiAuth(), a.apiDeleteHouseholdPerson)
	v1.POST("/me/household/people/:id/convert", a.apiAuth(), a.apiCreateHouseholdConversion)
	v1.POST("/me/household/people/:id/convert/resend", a.apiAuth(), a.apiResendHouseholdConversion)
	v1.DELETE("/me/household/people/:id/convert", a.apiAuth(), a.apiCancelHouseholdConversion)
	v1.GET("/admin/guardian-links", a.apiAuth(), a.apiAdmin(), a.apiListGuardianLinks)
	v1.POST("/me/guardian-claims", a.apiAuth(), a.apiClaimTeenGuardian)
	v1.GET("/me/guardian-teens", a.apiAuth(), a.apiMyGuardianTeens)
	v1.POST("/me/guardian-links/:id/consents", a.apiAuth(), a.apiGuardianChangeConsent)
	v1.POST("/me/guardian-links/:id/consent-email", a.apiAuth(), a.apiResendGuardianConsentEmail)
	v1.GET("/guardian-consent", a.apiPreviewGuardianEmail)
	v1.POST("/guardian-consent", a.apiUseGuardianEmail)
	v1.GET("/admin/users/:id/household", a.apiAuth(), a.apiAdmin(), a.apiAdminGetHousehold)
	v1.POST("/admin/users/:id/household", a.apiAuth(), a.apiAdmin(), a.apiAdminCreateHousehold)
	v1.POST("/admin/users/:id/household/people", a.apiAuth(), a.apiAdmin(), a.apiAdminAddHouseholdPerson)
	v1.GET("/admin/users/:id/teen-consents", a.apiAuth(), a.apiAdmin(), a.apiListTeenConsents)
	v1.POST("/admin/users/:id/teen-consents", a.apiAuth(), a.apiAdmin(), a.apiChangeTeenConsent)
	v1.POST("/admin/guardian-links/:id/confirm", a.apiAuth(), a.apiAdmin(), a.apiConfirmGuardianLink)
	v1.POST("/admin/guardian-links/:id/reject", a.apiAuth(), a.apiAdmin(), a.apiRejectGuardianLink)

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
	v1.GET("/groups/:id/departure-preview", a.sessionAuth(), a.apiLeaveGroupDeparturePreview)
	v1.POST("/groups/:id/leave", a.apiAuth(), a.apiLeaveGroupConfirmed)
	v1.GET("/groups/:id/members/:userId/departure-preview", a.sessionAuth(), a.apiRemoveGroupMemberDeparturePreview)
	v1.POST("/groups/:id/members/:userId/departure", a.apiAuth(), a.apiRemoveGroupMemberConfirmed)
	v1.PUT("/groups/:id/members/:userId/role", a.apiAuth(), a.apiSetMemberRole)
	v1.POST("/groups/:id/members", a.apiAuth(), a.apiAddGroupMember)
	v1.DELETE("/groups/:id/members/:userId", a.apiAuth(), a.apiRemoveGroupMemberConfirmed)
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
	// Rooms (M2.1a): every room route is 404 while rooms are off (9.1).
	v1.PUT("/admin/settings/rooms", a.apiAuth(), a.apiAdmin(), a.apiSetRoomsEnabled)
	v1.GET("/admin/rooms", a.roomsOn(), a.apiAuth(), a.roomManager(), a.apiAdminListRooms)
	v1.POST("/admin/rooms", a.roomsOn(), a.apiAuth(), a.roomManager(), a.apiAdminCreateRoom)
	v1.PUT("/admin/rooms/:id", a.roomsOn(), a.apiAuth(), a.roomManager(), a.apiAdminUpdateRoom)
	v1.GET("/events", a.optionalUser(), a.apiListOpenEvents)
	// The calendar (M1.6): every occurrence the viewer may see over a
	// date range, as slim rows with no attendee data.
	v1.GET("/calendar", a.optionalUser(), a.apiCalendar)
	v1.POST("/events", a.apiAuth(), a.apiAdmin(), a.apiCreateChurchEvent)
	// Video (M1.8): mint a Jitsi room URL (leaders, hosts, admins).
	v1.POST("/events/video/jitsi", a.apiAuth(), a.apiGenerateJitsiLink)
	v1.GET("/events/:id", a.optionalUser(), a.apiGetEvent)
	v1.GET("/events/:id/dates/:date", a.optionalUser(), a.apiGetOccurrenceDate)
	v1.GET("/events/:id/dates/:date/calendar.ics", a.optionalUser(), a.apiOccurrenceCalendar)
	v1.GET("/events/:id/occurrences/:date/visibility-impact", a.apiAuth(), a.apiDateVisibilityImpact)
	v1.POST("/events/:id/dates/:date/decline", a.apiAuth(), a.apiDeclineOccurrence)
	v1.POST("/events/:id/dates/:date/block", a.apiAuth(), a.apiBlockOccurrence)
	v1.PUT("/events/:id/dates/:date/registration", a.apiAuth(), a.apiRegisterOccurrence)
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
	// Actor-only bulk responses freeze the currently generated future dates;
	// occurrences extended later remain unanswered (M1.14).
	v1.POST("/events/:id/series-registrations", a.apiAuth(), a.apiRegisterSeries)
	v1.POST("/events/:id/series-registrations/undo", a.apiAuth(), a.apiUndoSeriesRegistration)
	// Pruning and hand promotion (M1.5, DEC-20, DEC-30): leaders and hosts
	// remove anyone, promote one waitlisted row by hand, or add someone
	// directly (over capacity only by explicit choice).
	v1.POST("/events/:id/registrations/remove", a.apiAuth(), a.apiRemoveRegistration)
	v1.POST("/events/:id/registrations/promote", a.apiAuth(), a.apiPromoteRegistration)
	v1.POST("/events/:id/registrations/add", a.apiAuth(), a.apiAddRegistration)
	// Guest registration (M1.4): the form and the emailed verification
	// link are public; the manage token in the confirmation email is
	// the credential for cancelling. All POSTs ride the CSRF flow like
	// the other public forms.
	v1.POST("/events/:id/guest-registrations", a.apiCreateGuestRegistration)
	v1.POST("/guest-registrations/confirm", a.apiConfirmGuestRegistration)
	v1.GET("/guest-registrations/manage/:token", a.apiGetGuestRegistration)
	v1.POST("/guest-registrations/manage/:token/cancel", a.apiCancelGuestRegistration)

	// Admin.
	v1.GET("/admin/pending", a.apiAuth(), a.apiAdmin(), a.apiPendingGroups)
	v1.GET("/admin/deletions", a.apiAuth(), a.apiAdmin(), a.apiDeletionRequests)
	v1.POST("/admin/groups/:id/approve", a.apiAuth(), a.apiAdmin(), a.apiApproveGroup)
	v1.POST("/admin/groups/:id/reject", a.apiAuth(), a.apiAdmin(), a.apiRejectGroup)
	v1.POST("/admin/groups/:id/reject-deletion", a.apiAuth(), a.apiAdmin(), a.apiRejectDeletion)
	v1.DELETE("/admin/groups/:id", a.apiAuth(), a.apiAdmin(), a.apiAdminDeleteGroup)
	v1.GET("/admin/groups", a.apiAuth(), a.apiAdmin(), a.apiAdminListGroups)
	v1.POST("/admin/groups/:id/members", a.apiAuth(), a.apiAdmin(), a.apiAdminAddMember)
	v1.DELETE("/admin/groups/:id/members/:userId", a.apiAuth(), a.apiAdmin(), a.apiRemoveGroupMemberConfirmed)
	v1.GET("/admin/users", a.apiAuth(), a.apiAdmin(), a.apiAdminListUsers)
	v1.POST("/admin/users", a.apiAuth(), a.apiAdmin(), a.apiAdminCreateUser)
	v1.PUT("/admin/users/:id", a.apiAuth(), a.apiAdmin(), a.apiAdminUpdateUser)
	v1.GET("/admin/users/:id/sole-leader-groups", a.apiAuth(), a.apiAdmin(), a.apiAdminSoleLedGroups)
	v1.DELETE("/admin/users/:id", a.apiAuth(), a.apiAdmin(), a.apiAdminDeleteUser)
	v1.POST("/admin/users/:id/password", a.apiAuth(), a.apiAdmin(), a.apiAdminResetPassword)

	// Notifications (5.12).
	a.registerNotificationRoutes(v1)
}
