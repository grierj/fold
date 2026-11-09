package main

import (
	"net/http"

	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// authenticatedSessionUser loads the user the session cookie names, once per
// request, when the cookie is still current (sessionCurrent).
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
	uid, ok := session.Values[sessionUserKey].(uint)
	if !ok {
		return nil, false
	}
	var user models.User
	if err := a.DB.First(&user, uid).Error; err != nil {
		return nil, false
	}
	if !sessionCurrent(&user, &models.User{ID: uid, SessionVersion: sessionVersion(session), LogoutVersion: logoutVersion(session)}) {
		return nil, false
	}
	c.Set("sessionUser", &user)
	return &user, true
}

// userFromSession is authenticatedSessionUser for an active account.
func (a *App) userFromSession(c *gin.Context) (*models.User, bool) {
	user, ok := a.authenticatedSessionUser(c)
	return user, ok && user.Status == models.UserActive
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

// membershipRel turns a membership row (any status) into policy facts.
func membershipRel(m models.Membership) policy.Rel {
	active := m.ID != 0 && m.Status == models.MembershipActive
	return policy.Rel{
		Member:  active,
		Leader:  active && m.Role == models.MembershipLeader,
		Removed: m.ID != 0 && m.Status == models.MembershipRemoved,
	}
}

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
			groupID, models.MembershipLeader, models.MembershipActive).
		Count(&n)
	return int(n)
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
	// The composer (M3.4): announcements from leaders, hosts and admins.
	v1.GET("/messages/compose", a.apiAuth(), a.apiComposeOptions)
	v1.POST("/messages/audience-preview", a.apiAuth(), a.apiAudiencePreview)
	v1.GET("/messages", a.apiAuth(), a.apiListMessages)
	v1.POST("/messages", a.apiAuth(), a.apiCreateMessage)
	v1.GET("/messages/:id", a.apiAuth(), a.apiGetMessage)
	v1.PUT("/messages/:id", a.apiAuth(), a.apiUpdateMessage)
	v1.POST("/messages/:id/schedule", a.apiAuth(), a.apiScheduleMessage)
	v1.POST("/messages/:id/cancel", a.apiAuth(), a.apiCancelMessage)
	v1.GET("/messages/:id/read", a.apiAuth(), a.apiReadMessage)
	v1.GET("/messages/:id/preview", a.apiAuth(), a.apiPreviewMessage)
	v1.GET("/messages/:id/preview.html", a.apiAuth(), a.apiPreviewMessageHTML)
	v1.POST("/messages/:id/test", a.apiAuth(), a.apiTestMessage)
	// History and Try again (M3.5b, DEC-131).
	v1.GET("/messages/:id/recipients", a.apiAuth(), a.apiMessageRecipients)
	v1.POST("/messages/:id/retry", a.apiAuth(), a.apiRetryMessage)
	// Web push (M3.13b): this browser's device, by its endpoint, which no
	// response ever returns.
	v1.GET("/push/config", a.apiAuth(), a.apiPushConfig)
	v1.PUT("/push/subscriptions", a.apiAuth(), a.apiPushSubscribe)
	v1.DELETE("/push/subscriptions", a.apiAuth(), a.apiPushUnsubscribe)
	v1.POST("/push/turn-off", a.apiAuth(), a.apiPushTurnOff)
	v1.POST("/push/test", a.apiAuth(), a.apiPushTest)
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
	// The church's mailing address for email footers (M3.3b, DEC-116).
	v1.GET("/admin/settings/church-address", a.apiAuth(), a.apiAdmin(), a.apiAdminGetChurchAddress)
	v1.PUT("/admin/settings/church-address", a.apiAuth(), a.apiAdmin(), a.apiAdminSetChurchAddress)
	// What the send pipeline reads (M3.5b): guardian copies (DEC-19) and
	// where replies go (DEC-130).
	v1.GET("/admin/settings/guardian-copies", a.apiAuth(), a.apiAdmin(), a.apiAdminGetGuardianCopies)
	v1.PUT("/admin/settings/guardian-copies", a.apiAuth(), a.apiAdmin(), a.apiAdminSetGuardianCopies)
	v1.GET("/admin/settings/push-enabled", a.apiAuth(), a.apiAdmin(), a.apiAdminGetPushEnabled)
	v1.PUT("/admin/settings/push-enabled", a.apiAuth(), a.apiAdmin(), a.apiAdminSetPushEnabled)
	v1.GET("/admin/settings/reply-to", a.apiAuth(), a.apiAdmin(), a.apiAdminGetReplyTo)
	v1.PUT("/admin/settings/reply-to", a.apiAuth(), a.apiAdmin(), a.apiAdminSetReplyTo)
	// Email settings from an email's link, without signing in (M3.3b).
	v1.GET("/email-settings/:token", a.apiTokenEmailSettings)
	v1.PUT("/email-settings/:token", a.apiTokenUpdateEmailSettings)

	// Groups.
	v1.GET("/groups", a.optionalUser(), a.apiListGroups)
	v1.GET("/me/groups", a.apiAuth(), a.apiMyGroups)
	v1.POST("/groups", a.apiAuth(), a.apiCreateGroup)
	v1.GET("/groups/:id", a.optionalUser(), a.apiGetGroup)
	v1.PUT("/groups/:id", a.apiAuth(), a.apiUpdateGroup)
	v1.POST("/groups/:id/join", a.apiAuth(), a.apiJoinGroup)
	v1.GET("/groups/:id/departure-preview", a.apiAuth(), a.apiLeaveGroupDeparturePreview)
	v1.POST("/groups/:id/leave", a.apiAuth(), a.apiLeaveGroupConfirmed)
	v1.GET("/groups/:id/members/:userId/departure-preview", a.apiAuth(), a.apiRemoveGroupMemberDeparturePreview)
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
	v1.PUT("/admin/settings/rooms", a.apiAuth(), a.roomManager(), a.apiSetRoomsEnabled)
	v1.GET("/admin/rooms", a.roomsOn(), a.apiAuth(), a.roomManager(), a.apiAdminListRooms)
	v1.POST("/admin/rooms", a.roomsOn(), a.apiAuth(), a.roomManager(), a.apiAdminCreateRoom)
	v1.PUT("/admin/rooms/:id", a.roomsOn(), a.apiAuth(), a.roomManager(), a.apiAdminUpdateRoom)
	// Room bookings (M2.1b): whoever runs the event books its dates.
	v1.GET("/events/:id/dates/:date/rooms", a.roomsOn(), a.apiAuth(), a.apiDateRooms)
	v1.PUT("/events/:id/dates/:date/room", a.roomsOn(), a.apiAuth(), a.apiSetDateRoom)
	v1.GET("/events/:id/rooms", a.roomsOn(), a.apiAuth(), a.apiSeriesRooms)
	v1.PUT("/events/:id/room", a.roomsOn(), a.apiAuth(), a.apiSetSeriesRoom)
	v1.POST("/rooms/check", a.roomsOn(), a.apiAuth(), a.apiCheckRooms)
	// Approval (M2.1d, DEC-102).
	v1.PUT("/admin/settings/room-approval", a.roomsOn(), a.apiAuth(), a.roomManager(), a.apiSetRoomApproval)
	v1.GET("/admin/room-requests", a.roomsOn(), a.apiAuth(), a.roomManager(), a.apiAdminRoomRequests)
	v1.POST("/admin/room-requests/:id/confirm", a.roomsOn(), a.apiAuth(), a.roomManager(), a.apiConfirmRoomRequest)
	v1.POST("/admin/room-requests/:id/reject", a.roomsOn(), a.apiAuth(), a.roomManager(), a.apiRejectRoomRequest)
	// Matching old locations (M2.1e, 9.1).
	v1.GET("/admin/rooms/locations", a.roomsOn(), a.apiAuth(), a.roomManager(), a.apiAdminTypedLocations)
	v1.POST("/admin/rooms/locations/match", a.roomsOn(), a.apiAuth(), a.roomManager(), a.apiAdminMatchLocation)
	// The System page (M3.1b): dead background jobs, for admins.
	v1.GET("/admin/jobs", a.apiAuth(), a.jobManager(), a.apiAdminDeadJobs)
	v1.POST("/admin/jobs/:id/retry", a.apiAuth(), a.jobManager(), a.apiAdminRetryJob)
	v1.POST("/admin/jobs/:id/dismiss", a.apiAuth(), a.jobManager(), a.apiAdminDismissJob)
	// Room feeds (M2.2b): an admin makes, replaces or turns off a room's link.
	v1.GET("/admin/rooms/:id/feed", a.roomsOn(), a.apiAuth(), a.roomManager(), a.apiAdminGetRoomFeed)
	v1.POST("/admin/rooms/:id/feed", a.roomsOn(), a.apiAuth(), a.roomManager(), a.apiAdminCreateRoomFeed)
	v1.DELETE("/admin/rooms/:id/feed", a.roomsOn(), a.apiAuth(), a.roomManager(), a.apiAdminRevokeRoomFeed)
	v1.GET("/events", a.optionalUser(), a.apiListOpenEvents)
	// The calendar (M1.6): every occurrence the viewer may see over a
	// date range, as slim rows with no attendee data.
	v1.GET("/calendar", a.optionalUser(), a.apiCalendar)
	// The by-room view (M2.2a): signed-in adults, 404 while rooms are off.
	v1.GET("/calendar/rooms", a.roomsOn(), a.apiAuth(), a.apiRoomCalendar)
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
	// Email suppressions (M3.2c): let Fold mail a bounced address again.
	v1.POST("/admin/users/:id/email-suppression/clear", a.apiAuth(), a.apiAdmin(), a.apiAdminClearSuppression)
	v1.PUT("/admin/users/:id", a.apiAuth(), a.apiAdmin(), a.apiAdminUpdateUser)
	v1.GET("/admin/users/:id/sole-leader-groups", a.apiAuth(), a.apiAdmin(), a.apiAdminSoleLedGroups)
	v1.GET("/admin/users/:id/sole-host-events", a.apiAuth(), a.apiAdmin(), a.apiAdminSoleHostEvents)
	v1.DELETE("/admin/users/:id", a.apiAuth(), a.apiAdmin(), a.apiAdminDeleteUser)
	// One audited page of the contact-request reasons deleting this account would
	// leave behind, for the admin to review (T07B-2b). A POST: it writes a view
	// audit row.
	v1.POST("/admin/users/:id/erasure-reasons/page", a.apiAuth(), a.apiAdmin(), a.apiAdminErasureReasonsPage)
	// Attempt ids for the deletion and settling a lost reply (T07B-1b). Both
	// are CSRF-protected POSTs; the id travels in the body, never the path.
	v1.POST("/admin/operation-attempts", a.apiAuth(), a.apiAdmin(), a.apiAdminMintOperationAttempt)
	v1.POST("/admin/operation-attempts/settle", a.apiAuth(), a.apiAdmin(), a.apiAdminSettleOperationAttempt)
	v1.POST("/admin/users/:id/password", a.apiAuth(), a.apiAdmin(), a.apiAdminResetPassword)

	// Notifications (5.12).
	a.registerNotificationRoutes(v1)

	// Browser-test routes: nil outside -tags e2eclock (e2e_hooks.go).
	if e2eRoutes != nil {
		e2eRoutes(a, v1)
	}
}
