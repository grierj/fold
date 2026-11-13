package main

import "fold/models"

// Named shapes for JSON answers that were ad-hoc gin.H maps. Each is exactly
// the keys its handler always sent; web/src/types.ts names the same fields
// and TestWebTypesMatchGoJSON (tsContracts) keeps the two together.

// sessionJSON answers a sign-in and GET /auth/me for a signed-in person.
type sessionJSON struct {
	User       userJSON `json:"user"`
	CanCompose bool     `json:"canCompose"`
}

// siteJSON is GET /site.
type siteJSON struct {
	Name         string `json:"name"`
	Timezone     string `json:"timezone"`
	SelfSignup   string `json:"selfSignup"`
	RoomsEnabled bool   `json:"roomsEnabled"`
	// PushEnabled: the directions page (/notifications/setup) is public and
	// hides itself when the church turned notifications off (DEC-124).
	PushEnabled bool `json:"pushEnabled"`
}

// accountRequestConfirmJSON answers POST /account-requests/confirm.
type accountRequestConfirmJSON struct {
	User           userJSON `json:"user"`
	PendingConsent bool     `json:"pendingConsent"`
}

// accountRequestDecisionJSON answers an admin's approve or reject.
type accountRequestDecisionJSON struct {
	Request accountRequestJSON `json:"request"`
	UserID  uint               `json:"userId"`
}

// calendarResponseJSON is GET /calendar.
type calendarResponseJSON struct {
	Occurrences []calendarOccurrenceJSON `json:"occurrences"`
	Truncated   bool                     `json:"truncated"`
}

// guestConfirmationJSON answers POST /guest-registrations/confirm.
type guestConfirmationJSON struct {
	Registration guestRegistrationJSON `json:"registration"`
	ManageToken  string                `json:"manageToken"`
}

// profileUpdateJSON answers PUT /me. VerificationSent is set when an email
// change started.
type profileUpdateJSON struct {
	User             userJSON `json:"user"`
	VerificationSent bool     `json:"verificationSent,omitempty"`
}

// emailSettingsLinkJSON is GET /email-settings/:token.
type emailSettingsLinkJSON struct {
	EmailSettings emailSettingsJSON `json:"emailSettings"`
	Email         string            `json:"email"`
	FromCategory  string            `json:"fromCategory"`
	CanTurnOn     bool              `json:"canTurnOn"`
}

// teenConsentsJSON is GET /admin/users/:id/teen-consents.
type teenConsentsJSON struct {
	Consents                 []teenConsentJSON `json:"consents"`
	AdministrativelyDisabled bool              `json:"administrativelyDisabled"`
}

// guardianLinksResponseJSON is GET /admin/guardian-links.
type guardianLinksResponseJSON struct {
	GuardianLinks []guardianLinkJSON       `json:"guardianLinks"`
	Nominations   []guardianNominationJSON `json:"nominations"`
}

// adminJobsJSON is GET /admin/jobs.
type adminJobsJSON struct {
	Jobs       []adminJobJSON           `json:"jobs"`
	More       bool                     `json:"more"`
	Deliveries adminDeliverySummaryJSON `json:"deliveries"`
}

// adminRoomsJSON is GET /admin/rooms.
type adminRoomsJSON struct {
	Rooms        []roomJSON `json:"rooms"`
	NeedApproval bool       `json:"needApproval"`
}

// matchLocationJSON answers POST /admin/rooms/locations/match.
type matchLocationJSON struct {
	Booked    int             `json:"booked"`
	Busy      []matchBusyJSON `json:"busy"`
	BusyTotal int             `json:"busyTotal"`
	Series    int             `json:"series"`
}

// roomRequestConfirmationJSON answers POST /admin/room-requests/:id/confirm.
type roomRequestConfirmationJSON struct {
	Confirmed  int      `json:"confirmed"`
	Overlapped int      `json:"overlapped"`
	Lost       []string `json:"lost"`
}

// dateRoomsJSON is GET /events/:id/dates/:date/rooms.
type dateRoomsJSON struct {
	Rooms       []roomPickerJSON    `json:"rooms"`
	Booking     *roomBookingJSON    `json:"booking"`
	Suggestions roomSuggestionsJSON `json:"suggestions"`
}

// seriesRoomsJSON is GET /events/:id/rooms.
type seriesRoomsJSON struct {
	Rooms       []seriesRoomJSON    `json:"rooms"`
	Dates       int                 `json:"dates"`
	DefaultRoom *occurrenceRoomJSON `json:"defaultRoom"`
	NeedsRoom   []needsRoomJSON     `json:"needsRoom"`
	Pending     int                 `json:"pending"`
	Suggestions roomSuggestionsJSON `json:"suggestions"`
}

// roomsCheckJSON answers POST /rooms/check.
type roomsCheckJSON struct {
	Rooms       []seriesRoomJSON    `json:"rooms"`
	Dates       int                 `json:"dates"`
	Suggestions roomSuggestionsJSON `json:"suggestions"`
}

// badgeJSON is GET /notifications/badge.
type badgeJSON struct {
	Unread  int64 `json:"unread"`
	Waiting int64 `json:"waiting"`
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
	// EmailSuppression: Fold stopped mailing this person's address (admin
	// list only, M3.2c, DEC-113).
	EmailSuppression *emailSuppressionJSON `json:"emailSuppression,omitempty"`
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

// One-key (and a few two-key) answers the SPA wraps in an envelope. Each is
// exactly the keys its handlers always sent; web/src/types.ts names the same
// fields (the *Response interfaces) and tsContracts keeps the two together.

// noticeResponseJSON is a plain acknowledgement line (a request received, a mail sent).
type noticeResponseJSON struct {
	Message string `json:"message"`
}

// requestResponseJSON and requestsResponseJSON answer one contact or add
// request and a page of them; T is that kind's presented request.
type requestResponseJSON[T any] struct {
	Request T `json:"request"`
}
type requestsResponseJSON[T any] struct {
	Requests []T `json:"requests"`
}

type messageResponseJSON struct {
	Message messageJSON `json:"message"`
}

// messageScheduledResponseJSON answers scheduling: the message and who it will reach.
type messageScheduledResponseJSON struct {
	Message messageJSON         `json:"message"`
	Summary audienceSummaryJSON `json:"summary"`
}

type messageReadResponseJSON struct {
	Message messageReadJSON `json:"message"`
}

type messagePreviewResponseJSON struct {
	Preview renderedMessage `json:"preview"`
}

type messageTestResponseJSON struct {
	OK bool `json:"ok"`
}

type audienceSummaryResponseJSON struct {
	Summary audienceSummaryJSON `json:"summary"`
}

type groupListResponseJSON struct {
	Groups []groupSummaryJSON `json:"groups"`
}

type groupResponseJSON struct {
	Group groupSummaryJSON `json:"group"`
}

type membersResponseJSON struct {
	Members []memberJSON `json:"members"`
}

type soleLedGroupsResponseJSON struct {
	Groups []soleLedGroupJSON `json:"groups"`
}

type departurePreviewResponseJSON struct {
	Departure departurePreviewJSON `json:"departure"`
}

type departureConfirmationResponseJSON struct {
	Departure departureConfirmationJSON `json:"departure"`
}

type coverUploadResponseJSON struct {
	ImageURL string `json:"imageUrl"`
}

type avatarUploadResponseJSON struct {
	AvatarURL string `json:"avatarUrl"`
}

type userResponseJSON struct {
	User userJSON `json:"user"`
}

type usersResponseJSON struct {
	Users []userJSON `json:"users"`
}

type eventSeriesResponseJSON struct {
	Event eventJSON `json:"event"`
}

type openEventsResponseJSON struct {
	Events []occurrenceJSON `json:"events"`
}

type occurrenceResponseJSON struct {
	Occurrence occurrenceJSON `json:"occurrence"`
}

type eventDateResponseJSON struct {
	Occurrence dateOccurrenceJSON `json:"occurrence"`
}

type eventHostsResponseJSON struct {
	Hosts []hostJSON `json:"hosts"`
}

type jitsiLinkResponseJSON struct {
	URL string `json:"url"`
}

type timezoneResponseJSON struct {
	Timezone string `json:"timezone"`
}

type calendarFeedResponseJSON struct {
	CalendarFeed calendarFeedJSON `json:"calendarFeed"`
}

// calendarFeedRevokedResponseJSON: an admin turned a person's link off; HadLink
// says whether there was one to turn off.
type calendarFeedRevokedResponseJSON struct {
	CalendarFeed calendarFeedJSON `json:"calendarFeed"`
	HadLink      bool             `json:"hadLink"`
}

type settingOnResponseJSON struct {
	On bool `json:"on"`
}

type settingAddressResponseJSON struct {
	Address string `json:"address"`
}

type selfSignupResponseJSON struct {
	SelfSignup string `json:"selfSignup"`
}

type adminJobStatusResponseJSON struct {
	Status string `json:"status"`
}

type notificationResponseJSON struct {
	Notification notificationJSON `json:"notification"`
}

type notificationSettingsResponseJSON struct {
	Settings notificationSettingsJSON `json:"settings"`
}

type emailSettingsChangeResponseJSON struct {
	EmailSettings emailSettingsJSON `json:"emailSettings"`
}

type pushSettingsResponseJSON struct {
	Push pushSettingsJSON `json:"push"`
}

type emailSuppressionClearedResponseJSON struct {
	Cleared int64 `json:"cleared"`
}

type emailChangeCancelledResponseJSON struct {
	Cancelled bool `json:"cancelled"`
}

type roomResponseJSON struct {
	Room roomJSON `json:"room"`
}

type roomsEnabledResponseJSON struct {
	RoomsEnabled bool `json:"roomsEnabled"`
}

type roomApprovalResponseJSON struct {
	NeedApproval bool `json:"needApproval"`
}

type roomLocationsResponseJSON struct {
	Locations []typedLocationJSON `json:"locations"`
}

type roomFeedResponseJSON struct {
	Feed roomFeedJSON `json:"feed"`
}

type roomRequestsResponseJSON struct {
	Requests []roomRequestJSON `json:"requests"`
}

type roomBookingResponseJSON struct {
	Booking *roomBookingJSON `json:"booking"`
}

type guardianLinkResponseJSON struct {
	GuardianLink guardianLinkJSON `json:"guardianLink"`
}

type guardianTeensResponseJSON struct {
	Teens []guardianTeenJSON `json:"teens"`
}

type guestRegistrationResponseJSON struct {
	Registration guestRegistrationJSON `json:"registration"`
}

type dateRegistrationResponseJSON struct {
	Registration registrationJSON `json:"registration"`
}

type householdResponseJSON struct {
	Household householdJSON `json:"household"`
}

type myHouseholdResponseJSON struct {
	Household *householdJSON `json:"household"`
}

type householdPersonResponseJSON struct {
	Person householdPersonJSON `json:"person"`
}

type householdConversionResponseJSON struct {
	Conversion conversionJSON `json:"conversion"`
}

type invitationResponseJSON struct {
	Invitation invitationJSON `json:"invitation"`
}

type contactGrantsResponseJSON struct {
	Grants []contactGrantJSON `json:"grants"`
}

type accountRequestsResponseJSON struct {
	Requests []accountRequestJSON `json:"requests"`
}

// legacyDiscoveryItemJSON is one undecided reason in the legacy review list:
// only the request, as a decimal string (a request id is signed 64-bit and must
// survive JavaScript). The text, the hash and the other person arrive only with
// an audited per-account page.
type legacyDiscoveryItemJSON struct {
	RequestID string `json:"requestId"`
}

// legacyDiscoveryAccountJSON groups a page's items by deleted account. userId
// is the account's users id as a decimal string and is the only reference a
// person is shown beside "Deleted account"; the client merges accounts across
// pages by it.
type legacyDiscoveryAccountJSON struct {
	UserID string                    `json:"userId"`
	Name   string                    `json:"name"`
	Items  []legacyDiscoveryItemJSON `json:"items"`
}

// legacyReasonDiscoveryJSON answers GET /admin/erasure-reasons/legacy: one
// request-atomic unit, at most 100 requests and 200 items, both parties of a
// request together. Accounts are in first-seen order and may continue on the
// next page; no total is claimed. next is the cursor, null when the table ended.
type legacyReasonDiscoveryJSON struct {
	Accounts []legacyDiscoveryAccountJSON `json:"accounts"`
	Next     *string                      `json:"next"`
}

// legacyReasonProbeJSON answers GET /admin/erasure-reasons/legacy/probe. found
// false proves only that the first unit had none.
type legacyReasonProbeJSON struct {
	Found bool `json:"found"`
}

// legacyReasonSaveJSON answers PUT /admin/erasure-reasons/legacy/:userId: how
// many of the sent decisions kept and how many removed their reason. Counts
// only, no ids and no text.
type legacyReasonSaveJSON struct {
	Kept    int `json:"kept"`
	Removed int `json:"removed"`
}
