package main

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
