package models

import (
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// Role constants for User.Role.
const (
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// Role constants for Membership.Role.
const (
	MembershipLeader = "leader"
	MembershipMember = "member"
)

// Group statuses.
const (
	GroupPending  = "pending"
	GroupActive   = "active"
	GroupArchived = "archived"
)

// Who can see an active group (C3). The same values are used on events.
// internal/policy decides what each means.
const (
	VisibilityPublic     = "public"      // anyone, including logged-out visitors
	VisibilityPrivate    = "private"     // signed-in users
	VisibilityInviteOnly = "invite_only" // members and admins only
)

// Where an event's visibility comes from (DEC-6). group follows the group,
// including later changes. custom keeps the event's own value.
const ()

// Registration statuses (renamed from RSVP, M1.1a). Cancelled arrives
// with the guest manage link (M1.4): the person withdrew a registration
// they had made, which is not the same answer as declining an event.
// Waitlisted and removed arrive with capacity and pruning (M1.5, 5.4).
const (
	RegGoing      = "going"
	RegMaybe      = "maybe"
	RegDeclined   = "declined"
	RegCancelled  = "cancelled"
	RegWaitlisted = "waitlisted"
	RegRemoved    = "removed"
)

// Where a registration came from (5.3). Guest form registrations are the
// ones a signed-out visitor made and verified by email (M1.4).
const (
	RegSourceWeb       = "web"
	RegSourceAdmin     = "admin"
	RegSourceGuestForm = "guest_form"
)

// Attendance modes (5.3, 5.4, M1.8 slice 2): a registration counts
// against the in-person capacity or, when the event offers it, the
// separate remote capacity.
const (
	AttendanceInPerson = "in_person"
	AttendanceRemote   = "remote"
)

// Video providers and reveal settings (5.8, M1.8). The link is a plain
// link out; there is no embedded video anywhere (DEC-16).
const (
	VideoProviderNone  = "none"
	VideoProviderLink  = "link"
	VideoProviderJitsi = "jitsi"

	VideoRevealAlways         = "always"
	VideoRevealRegisteredOnly = "registered_only"
)

// Occurrence statuses. A cancelled date is a row, not a separate exception.
const (
	OccurrenceScheduled = "scheduled"
	OccurrenceCancelled = "cancelled"
)

// Account statuses (5.3). Only active accounts with a password sign in.
// Guests and teens waiting for consent arrive in M1.4 and M1.10.
const (
	UserActive         = "active"
	UserInvited        = "invited" // no password yet
	UserGuest          = "guest"
	UserPendingConsent = "pending_consent"
	UserDisabled       = "disabled"
)

// Account types (DEC-8). Nobody under 13 gets a User row.
const (
	AccountAdult = "adult"
	AccountTeen  = "teen"
)

// Account request sources and statuses (5.3, DEC-4, M1.3).
const (
	RequestSelfSignup   = "self_signup"
	RequestMemberInvite = "member_invite"
	RequestGuestUpgrade = "guest_upgrade"

	RequestPending   = "pending"
	RequestApproved  = "approved"
	RequestRejected  = "rejected"
	RequestExpired   = "expired"
	RequestWithdrawn = "withdrawn"
)

type User struct {
	ID uint `gorm:"primaryKey"`
	// Email is the login (DEC-23, C16): citext, so any letter case matches.
	Email string `gorm:"type:citext;not null;unique"`
	// PasswordHash is the bcrypt hash, nil until the person has a password.
	PasswordHash *string
	Name         string // full name
	DisplayName  string // shown in the UI; falls back to Name
	Phone        string // first-class: SMS is a required channel
	AvatarKey    string // blob-store key for the avatar image ("" = none)
	// AvatarFocalX/Y is the focal point as a 0-100 percentage, used for
	// CSS object-position so faces stay centered in cropped avatars.
	AvatarFocalX float64 `gorm:"default:50"`
	AvatarFocalY float64 `gorm:"default:50"`
	Role         string  `gorm:"default:member"` // admin | member
	// Status is active, invited, guest, pending_consent or disabled (a CHECK
	// enforces it).
	Status string `gorm:"not null;default:active"`
	// AccountType is adult or teen (DEC-8). BirthDate is required for teens
	// (so Fold knows when they turn 18) and optional for adults, who only
	// confirm they are 18 or older (5.3).
	AccountType string     `gorm:"not null;default:adult"`
	BirthDate   *time.Time `gorm:"type:date"`
	// SessionVersion is bumped when the password, email, role or status
	// changes, so older cookies stop working (10.1).
	SessionVersion int `gorm:"not null;default:0"`
	// Timezone is the IANA zone for this person's emails and notices
	// (DEC-58). "" means the church's zone; see recipientZone.
	Timezone  string `gorm:"not null;default:''"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

// PublicName returns the name shown in the UI. It never falls back to the
// email, which is contact information.
func (u *User) PublicName() string {
	if u.DisplayName != "" {
		return u.DisplayName
	}
	if u.Name != "" {
		return u.Name
	}
	return "Member"
}

// SetPassword stores a bcrypt hash of plain (it does not save the row).
func (u *User) SetPassword(plain string) error {
	hashed, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	h := string(hashed)
	u.PasswordHash = &h
	return nil
}

// dummyHash is compared against when there is no real hash, so a missing
// account or password costs the same time as a wrong password.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("not a real password"), bcrypt.DefaultCost)

// CheckPassword reports whether password matches. It is always false when
// no password is set, and takes about the same time either way.
func (u *User) CheckPassword(password string) bool {
	if u == nil || u.PasswordHash == nil {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(*u.PasswordHash), []byte(password)) == nil
}

// CanSignIn: an active account with a password.
func (u *User) CanSignIn() bool {
	return u.Status == UserActive && u.PasswordHash != nil
}

// Group is an ongoing entity (a small group, ministry, or activity) —
// not a single event. Events are its instances.
type Group struct {
	ID          uint   `gorm:"primaryKey"`
	Name        string `gorm:"not null"`
	Description string `gorm:"type:text"`
	Tags        string `gorm:"type:text"` // comma-separated
	ImageURL    string
	Location    string
	// CoverFocalX/Y is the cover-image focal point as a 0-100 percentage,
	// used for CSS object-position.
	CoverFocalX float64 `gorm:"default:50"`
	CoverFocalY float64 `gorm:"default:50"`
	// LeaderID is the displayed primary contact only. Leadership comes from
	// Membership.Role (C12); never use this for permissions.
	LeaderID uint   `gorm:"not null"`
	Leader   User   `gorm:"foreignKey:LeaderID"`
	Status   string `gorm:"default:pending"` // pending | active | archived
	// Visibility is public, private, or invite_only (a CHECK enforces it).
	Visibility string `gorm:"not null;default:public"`
	// MemberLimit caps active memberships (DEC-39); nil means unlimited. A
	// CHECK keeps it positive. Groups have no waitlist.
	MemberLimit *int
	// DeletionRequested is set by the leader when they are the only member.
	// An admin approves it by deleting the group.
	DeletionRequested bool `gorm:"not null;default:false"`
	CreatedAt         time.Time
	UpdatedAt         time.Time
	DeletedAt         gorm.DeletedAt `gorm:"index"`
}

// Event is a series (or a one-time event). GroupID is nil for a church-wide
// event (DEC-7): an admin creates it and its hosts run it. The wall clock
// (StartsLocal + TZID) is the source of truth; occurrences store UTC (C2).
type Event struct {
	ID      uint   `gorm:"primaryKey"`
	GroupID *uint  `gorm:"index"`
	Group   Group  `gorm:"foreignKey:GroupID"`
	Title   string `gorm:"not null"`
	// Description is plain text, same 5000-character limit as a group.
	// Sanitized HTML is a later milestone.
	Description string `gorm:"not null"`
	// Visibility is public, private, or invite_only: the event's own value.
	// A group event copies the group's visibility when it is created
	// (DEC-48). Church-wide events are public or private (a CHECK enforces
	// that).
	Visibility string `gorm:"not null"`
	// LeaderOpened marks a group event a leader or admin deliberately set
	// wider than its group (DEC-17, DEC-28). When the group becomes more
	// private, these are not tightened automatically; the leader decides
	// (DEC-48). Always false on church-wide events.
	LeaderOpened bool `gorm:"not null;default:false"`
	// Capacity caps seats on each occurrence (5.4, M1.5); nil means
	// unlimited. One going registration uses 1 + guest_count seats.
	Capacity *int
	// WaitlistEnabled holds overflow sign-ups on a waitlist when a
	// capacity is set. Off means a full event refuses new going
	// registrations with 409.
	WaitlistEnabled bool `gorm:"not null;default:false"`
	// GuestsAllowed caps the extra unnamed guests one registration may
	// bring (0-5, enforced by a CHECK).
	GuestsAllowed int `gorm:"not null;default:0"`
	// Remote attendance (M1.8 slice 2): people may register remote only
	// when RemoteAvailable is on. Remote seats count against
	// RemoteCapacity (nil means unlimited; 0 means no remote seats),
	// never against the in-person Capacity.
	RemoteAvailable    bool `gorm:"not null;default:false"`
	RemoteCapacity     *int
	ChildcareAvailable bool   `gorm:"not null;default:false"`
	ChildcareNotes     string `gorm:"type:text;not null;default:''"`
	ChildcareCapacity  *int
	ChildcareAges      string `gorm:"type:text;not null;default:''"`
	// Video is a link out to a call (5.8, M1.8, DEC-16): none, a pasted
	// link, or a generated Jitsi room. VideoReveal decides who may see
	// the URL; see policy.CanSeeVideoLink.
	VideoProvider   string  `gorm:"not null;default:none"`            // none | link | jitsi
	VideoURL        string  `gorm:"type:text;not null;default:''"`    // empty unless a provider is set
	VideoReveal     string  `gorm:"not null;default:registered_only"` // always | registered_only
	StartsLocal     string  `gorm:"not null"`                         // YYYY-MM-DDTHH:MM, no offset
	DurationMinutes int     `gorm:"not null"`
	TZID            string  `gorm:"column:tzid;not null"`
	RRule           string  `gorm:"column:rrule;not null"`
	RRuleUntil      *string `gorm:"column:rrule_until"` // YYYY-MM-DD, inclusive
	Location        string  `gorm:"not null"`           // empty = the group's location
	CreatedBy       uint    `gorm:"not null"`
	Creator         User    `gorm:"foreignKey:CreatedBy"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
	DeletedAt       gorm.DeletedAt `gorm:"index"`
}

// EventHost is an adult assigned to a church-wide event (DEC-7). Hosts get
// leader rights over that event only. Group events use the group's leaders;
// a host row on a group event grants nothing in v1. AddedBy has no
// association: a second belongs-to User would add a foreign key GORM and
// the migration disagree on.
type EventHost struct {
	ID        uint  `gorm:"primaryKey"`
	EventID   uint  `gorm:"not null;uniqueIndex:idx_event_host"`
	Event     Event `gorm:"foreignKey:EventID"`
	UserID    uint  `gorm:"not null;uniqueIndex:idx_event_host"`
	User      User  `gorm:"foreignKey:UserID"`
	AddedBy   uint  `gorm:"not null"`
	CreatedAt time.Time
}

// EventOccurrence is one date of an event, generated 18 months ahead.
// OriginalDate is the series date in the event's time zone and does not
// change when a per-date override moves the clock time. Registrations hang
// off this row, so a series edit can move the time without losing them.
type EventOccurrence struct {
	ID               uint      `gorm:"primaryKey"`
	EventID          uint      `gorm:"not null;uniqueIndex:idx_event_original_date"`
	Event            Event     `gorm:"foreignKey:EventID"`
	OriginalDate     string    `gorm:"not null;uniqueIndex:idx_event_original_date"` // YYYY-MM-DD
	StartsAt         time.Time `gorm:"not null;index"`
	EndsAt           time.Time `gorm:"not null"`
	Status           string    `gorm:"not null;default:scheduled"` // scheduled | cancelled
	TitleOverride    *string
	LocationOverride *string
	StartsAtOverride *time.Time
	DurationOverride *int
	// CapacityOverride replaces the event's capacity for this date (M1.5);
	// nil means the date uses the event's capacity.
	CapacityOverride *int
	// RemoteCapacityOverride replaces the event's remote capacity for
	// this date (M1.8 slice 2); nil means the date uses the event's.
	RemoteCapacityOverride *int
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

// TimeFrozen reports a per-date clock override. A series edit does not
// move that clock.
func (o EventOccurrence) TimeFrozen() bool {
	return o.StartsAtOverride != nil || o.DurationOverride != nil
}

// Customized reports a per-date override or a cancellation. A series edit
// leaves the row alone: it does not move the clock and does not drop the date.
func (o EventOccurrence) Customized() bool {
	return o.TimeFrozen() || o.TitleOverride != nil || o.LocationOverride != nil || o.Status == OccurrenceCancelled
}

// Membership is a user's roster entry in a group.
// Role=leader is the only source of leadership; a group may have several.
type Membership struct {
	ID       uint   `gorm:"primaryKey"`
	UserID   uint   `gorm:"not null;uniqueIndex:idx_user_group"`
	User     User   `gorm:"foreignKey:UserID"`
	GroupID  uint   `gorm:"not null;uniqueIndex:idx_user_group"`
	Group    Group  `gorm:"foreignKey:GroupID"`
	Role     string `gorm:"default:member"` // leader | member
	Status   string `gorm:"default:active"`
	JoinedAt time.Time
}

// Registration is a user's response to one occurrence (renamed from RSVP).
// A guest's registration (M1.4) is an ordinary row against their guest
// User (C5), with Source guest_form and a hashed manage token: the raw
// token travels only in the confirmation email and lets the guest cancel
// without an account.
type Registration struct {
	ID                 uint            `gorm:"primaryKey"`
	OccurrenceID       uint            `gorm:"not null;uniqueIndex:idx_registration;uniqueIndex:idx_household_registration,where:household_person_id IS NOT NULL"`
	Occurrence         EventOccurrence `gorm:"foreignKey:OccurrenceID"`
	UserID             *uint           `gorm:"uniqueIndex:idx_registration"`
	HouseholdPersonID  *uint           `gorm:"uniqueIndex:idx_household_registration,where:household_person_id IS NOT NULL;index:idx_registrations_household_person_id,where:household_person_id IS NOT NULL"`
	RegisteredBy       *uint           `gorm:"index:idx_registrations_registered_by,where:registered_by IS NOT NULL"`
	ChildcareRequested bool            `gorm:"not null;default:false"`
	User               User            `gorm:"foreignKey:UserID"`
	Status             string          `gorm:"not null"` // going | maybe | declined | cancelled | waitlisted | removed
	// AttendanceMode is the pool this registration counts against (M1.8
	// slice 2); a waitlisted entry keeps its mode in line.
	AttendanceMode  string `gorm:"not null;default:in_person"` // in_person | remote
	Source          string `gorm:"not null;default:web"`       // web | admin | guest_form
	ManageTokenHash string `gorm:"not null;default:'';uniqueIndex:idx_registrations_manage_token,where:manage_token_hash <> ''"`
	// GuestCount is the extra unnamed guests on this registration (0 to
	// the event's guests_allowed). Seats used = 1 + guest_count.
	GuestCount        int    `gorm:"not null;default:0"`
	ChildcareCount    int    `gorm:"not null;default:0"`
	ChildcareAgesNote string `gorm:"type:text;not null;default:''"`
	// WaitlistedAt is when the current wait began; kept as history after
	// a promotion. PromotedAt is when the row last became going from the
	// waitlist.
	WaitlistedAt *time.Time
	PromotedAt   *time.Time
	// RemovedBy is the leader or host who pruned this registration
	// (DEC-20, DEC-30). No GORM association: a second belongs-to User
	// would add a foreign key GORM and the migration disagree on.
	RemovedBy   *uint
	RemovalNote string `gorm:"not null;default:''"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// GuestSignup is one unverified guest registration intent (M1.4): a
// signed-out visitor's name and email for one occurrence, waiting for the
// emailed link to be redeemed. Nothing else exists until then, so an
// unverified address never lands on an attendee list. The raw token goes
// to the person's email; only its SHA-256 is stored. ConfirmedAt is set
// when the link is redeemed, which is also what makes the token dead.
type GuestSignup struct {
	ID           uint            `gorm:"primaryKey"`
	Name         string          `gorm:"not null"`
	Email        string          `gorm:"type:citext;not null;index"`
	OccurrenceID uint            `gorm:"not null;index"`
	Occurrence   EventOccurrence `gorm:"foreignKey:OccurrenceID"`
	// CreateAccount records the guest ticking "create an account" on the
	// form; redeeming the link then also opens a guest_upgrade account
	// request for an admin to decide (5.10).
	CreateAccount bool      `gorm:"not null;default:false"`
	TokenHash     string    `gorm:"not null;uniqueIndex:idx_guest_signups_token_hash"`
	ExpiresAt     time.Time `gorm:"not null"`
	ConfirmedAt   *time.Time
	IP            string `gorm:"not null;default:''"`
	// Timezone is the guest form browser's zone, for the guest's emails.
	Timezone  string    `gorm:"not null;default:''"`
	CreatedAt time.Time `gorm:"not null;default:now()"`
	UpdatedAt time.Time `gorm:"not null;default:now()"`
}

// Setting is a simple key/value store for site configuration
// (e.g. the church name collected by the setup wizard).
type Setting struct {
	Key   string `gorm:"primaryKey"`
	Value string
}

// AuditLog is one append-only record of a sensitive action (5.3). Write it
// through internal/audit. ActorID is nil for system actions. Rows have no
// foreign keys, so they outlive the people and groups they name.
type AuditLog struct {
	ID         uint      `gorm:"primaryKey"`
	ActorID    *uint     `gorm:"index"`
	Action     string    `gorm:"not null;index"`
	TargetType string    `gorm:"not null;index:idx_audit_logs_target"`
	TargetID   uint      `gorm:"not null;index:idx_audit_logs_target"`
	MetaJSON   string    `gorm:"column:meta_json;type:jsonb;not null;default:'{}'"`
	IP         string    `gorm:"column:ip"`
	At         time.Time `gorm:"not null;default:now();index"`
}

// LoginThrottle counts failed sign-ins for one key ("email:<address>" or
// "ip:<address>") in the current 15-minute window (10.1). It lives in
// Postgres so it survives restarts and is shared by every replica.
type LoginThrottle struct {
	Key         string    `gorm:"primaryKey"`
	Failures    int       `gorm:"not null;default:0"`
	WindowStart time.Time `gorm:"not null"`
	LockedUntil *time.Time
}

// Calendar feed scopes (8.2): "mine" is Option A (my groups, events I
// host, and the dates I'm going or waitlisted for); "all" is Option B
// (everything I can see).
const (
	CalendarFeedMine = "mine"
	CalendarFeedAll  = "all"
)

// CalendarToken is one person's secret calendar-feed link (M1.7, 8.2):
// one row per user, only the SHA-256 of the raw token stored. The raw
// token is shown once when the row is created or regenerated.
type CalendarToken struct {
	ID        uint      `gorm:"primaryKey"`
	UserID    uint      `gorm:"not null;uniqueIndex:idx_calendar_tokens_user_id"`
	User      User      `gorm:"foreignKey:UserID"`
	TokenHash string    `gorm:"not null;uniqueIndex:idx_calendar_tokens_token_hash"`
	Scope     string    `gorm:"not null;default:mine"` // mine | all
	CreatedAt time.Time `gorm:"not null;default:now()"`
	UpdatedAt time.Time `gorm:"not null;default:now()"`
}

// Account token kinds (10.1): single-use hashed tokens, valid 24 hours.
const (
	TokenPasswordReset = "password_reset"
	TokenEmailVerify   = "email_verify"
	TokenAccountSetup  = "account_setup"
)

// AccountToken is a single-use hashed token for a password reset or an
// email-change verification (10.1, M1.2). The raw token goes to the
// person's email; only its SHA-256 is stored. payload carries the new
// email for an email_verify token.
type AccountToken struct {
	ID        uint      `gorm:"primaryKey"`
	Kind      string    `gorm:"not null"` // password_reset | email_verify
	UserID    uint      `gorm:"not null;index"`
	User      User      `gorm:"foreignKey:UserID"`
	TokenHash string    `gorm:"not null;uniqueIndex:idx_account_tokens_token_hash"`
	Payload   string    `gorm:"type:jsonb;not null;default:'{}'"`
	ExpiresAt time.Time `gorm:"not null"`
	UsedAt    *time.Time
	CreatedAt time.Time `gorm:"not null;default:now()"`
}

// Invitation is an admin's or leader's invitation (5.10, DEC-4, M1.2): no
// approval step. The person sets a password on acceptance and gets an
// active account. A leader's invitation names only a group they lead.
// OverLimit records the inviting leader's or admin's explicit choice to
// join over the group's member limit on acceptance (DEC-39, audited).
type Invitation struct {
	ID                uint      `gorm:"primaryKey"`
	Email             string    `gorm:"type:citext;not null"`
	Phone             string    `gorm:"not null;default:''"`
	Name              string    `gorm:"not null;default:''"`
	InvitedBy         uint      `gorm:"not null;index"`
	Role              string    `gorm:"not null;default:member"` // member | leader (group role on accept)
	AccountType       string    `gorm:"not null;default:adult"`  // adult | teen (teens arrive in M1.10)
	GroupID           *uint     `gorm:"index"`
	Group             Group     `gorm:"foreignKey:GroupID"`
	OverLimit         bool      `gorm:"not null;default:false"`
	TokenHash         string    `gorm:"not null;uniqueIndex:idx_invitations_token_hash"`
	ExpiresAt         time.Time `gorm:"not null"`
	AcceptedAt        *time.Time
	HouseholdPersonID *uint `gorm:"uniqueIndex:idx_invitations_open_conversion,where:household_person_id IS NOT NULL AND accepted_at IS NULL AND revoked_at IS NULL"`
	RevokedAt         *time.Time
	CreatedAt         time.Time `gorm:"not null;default:now()"`
}

// MailOutbox is one email the devoutbox sender stored (5.6, M1.2): the
// sender for dev and tests. /dev/outbox shows them; tests read links from
// them.
type MailOutbox struct {
	ID          uint      `gorm:"primaryKey"`
	ToAddress   string    `gorm:"not null"`
	FromAddress string    `gorm:"not null;default:''"`
	Subject     string    `gorm:"not null"`
	Body        string    `gorm:"not null;default:''"`
	CreatedAt   time.Time `gorm:"not null;default:now()"`
}

// Notification delivery channels and statuses (5.3, 5.12).
const (
	DeliveryEmail = "email"
	DeliverySMS   = "sms"

	DeliverySent       = "sent"
	DeliveryFailed     = "failed"
	DeliveryQueued     = "queued"
	DeliverySuppressed = "suppressed"
	DeliveryDeferred   = "deferred"
	DeliveryDelivered  = "delivered"
	DeliveryBounced    = "bounced"
)

// NotificationDelivery records one email (later SMS) send of a stored
// notification (5.12, M1.2). Unique (notification_id, channel).
type NotificationDelivery struct {
	ID                uint   `gorm:"primaryKey"`
	NotificationID    uint   `gorm:"not null;uniqueIndex:idx_notification_deliveries_channel"`
	Channel           string `gorm:"not null;uniqueIndex:idx_notification_deliveries_channel"` // email | sms
	UserID            uint   `gorm:"not null;index"`
	Status            string `gorm:"not null"` // sent | failed | ...
	Reason            string `gorm:"not null;default:''"`
	ScheduledFor      *time.Time
	ProviderMessageID *string
	SentAt            *time.Time
	CreatedAt         time.Time `gorm:"not null;default:now()"`
}

// RequestFlags are the abuse warnings stored with an account request at
// creation (DEC-44): they decide whether an admin may approve it inline
// from the bell. They never reach the requester.
type RequestFlags struct {
	Duplicate   bool `json:"duplicate"`
	Honeypot    bool `json:"honeypot"`
	RateLimited bool `json:"rateLimited"`
}

// HasWarnings reports whether any flag is set.
func (f RequestFlags) HasWarnings() bool { return f.Duplicate || f.Honeypot || f.RateLimited }

// AccountRequest is one request for an account (5.3, DEC-4, M1.3):
// self-signup, a member's invite-a-friend, or (M1.4) a guest upgrade.
// Only admins decide it. Approval creates the User and emails a
// set-password link; a teen's account starts pending_consent (DEC-8).
// GroupID is reserved for the guest-upgrade door (M1.4); M1.3 approvals
// create the account only and never join a group.
type AccountRequest struct {
	ID           uint       `gorm:"primaryKey"`
	Name         string     `gorm:"not null"`
	Email        string     `gorm:"type:citext;not null;index"`
	Phone        string     `gorm:"not null;default:''"`
	AccountType  string     `gorm:"not null;default:adult"` // adult | teen
	BirthDate    *time.Time `gorm:"type:date"`
	Message      string     `gorm:"type:text;not null;default:''"`
	Source       string     `gorm:"not null"` // self_signup | member_invite | guest_upgrade
	RequestedBy  *uint      `gorm:"index"`
	GroupID      *uint      `gorm:"index"`
	Status       string     `gorm:"not null;default:pending;index"` // pending | approved | rejected | expired | withdrawn
	DecidedBy    *uint
	DecidedAt    *time.Time
	DecisionNote string `gorm:"not null;default:''"`
	// Flags holds the RequestFlags JSON (duplicate, honeypot, rateLimited).
	Flags     string    `gorm:"type:jsonb;not null;default:'{}'"`
	IP        string    `gorm:"not null;default:''"`
	ExpiresAt time.Time `gorm:"not null;index"`
	CreatedAt time.Time `gorm:"not null;default:now()"`
	UpdatedAt time.Time `gorm:"not null;default:now()"`
}

// Household people and guardian links (5.3, 5.10, DEC-8, DEC-23,
// DEC-32, M1.9). A household person has no login, email, or phone: a
// child under 13, a teen without their own email, or an adult who shares
// the account holder's email.
const (
	HouseholdChild = "child"
	HouseholdTeen  = "teen"
	HouseholdAdult = "adult"

	HouseholdRelationGuardian = "guardian"
	HouseholdRelationAdult    = "adult"
	HouseholdRelationTeen     = "teen"

	GuardianPending   = "pending"
	GuardianConfirmed = "confirmed"
	GuardianRejected  = "rejected"
	GuardianRevoked   = "revoked"
)

// Household is one household (5.3). It is created on first use, named
// after the account's last name, and editable. CreatedBy has no GORM
// association, like AccountRequest's user references.
type Household struct {
	ID        uint      `gorm:"primaryKey"`
	Name      string    `gorm:"not null"`
	Address   string    `gorm:"not null;default:''"`
	CreatedBy uint      `gorm:"not null;index"`
	CreatedAt time.Time `gorm:"not null;default:now()"`
	UpdatedAt time.Time `gorm:"not null;default:now()"`
}

// HouseholdAccount is a person in a household who has their own account
// (5.3). UserID is unique: an account belongs to at most one household.
type HouseholdAccount struct {
	ID          uint      `gorm:"primaryKey"`
	HouseholdID uint      `gorm:"not null;index"`
	UserID      uint      `gorm:"not null;uniqueIndex:idx_household_accounts_user_id"`
	Relation    string    `gorm:"not null"` // guardian | adult | teen
	CreatedAt   time.Time `gorm:"not null;default:now()"`
	UpdatedAt   time.Time `gorm:"not null;default:now()"`
}

// HouseholdPerson is a person with no account (5.3, DEC-8, DEC-23).
// CareNotes are visible only as section 6 allows (policy.CanSeeCareNotes);
// PhotoConsent defaults to no (10.3). ConvertedUserID is set when the
// person is turned into their own account (M1.9 slice 3). Removal is a
// soft delete, so guardian links and later registrations keep the row.
type HouseholdPerson struct {
	ID              uint           `gorm:"primaryKey"`
	HouseholdID     uint           `gorm:"not null;index"`
	FirstName       string         `gorm:"not null"`
	LastName        string         `gorm:"not null;default:''"`
	Kind            string         `gorm:"not null"` // child | teen | adult
	BirthDate       *time.Time     `gorm:"type:date"`
	CareNotes       string         `gorm:"type:text;not null;default:''"`
	PhotoConsent    bool           `gorm:"not null;default:false"`
	CreatedBy       uint           `gorm:"not null"`
	ConvertedUserID *uint          `gorm:""`
	CreatedAt       time.Time      `gorm:"not null;default:now()"`
	UpdatedAt       time.Time      `gorm:"not null;default:now()"`
	DeletedAt       gorm.DeletedAt `gorm:"index"`
}

// FullName is the person's first and last names joined for display.
func (p HouseholdPerson) FullName() string {
	if p.LastName == "" {
		return p.FirstName
	}
	if p.FirstName == "" {
		return p.LastName
	}
	return p.FirstName + " " + p.LastName
}

// GuardianLink links an adult to a teen or child (5.3, DEC-32). Exactly
// one of TeenUserID (a teen account) or PersonID (a household person) is
// set, enforced by a CHECK. Every link starts pending; only an admin can
// confirm it, writing how they checked in CheckNote. There is no
// auto-confirm path.
type GuardianLink struct {
	ID             uint      `gorm:"primaryKey"`
	GuardianUserID uint      `gorm:"not null;index"`
	TeenUserID     *uint     `gorm:"index"`
	PersonID       *uint     `gorm:"index"`
	Status         string    `gorm:"not null;default:pending;index"` // pending | confirmed | rejected | revoked
	ConfirmedBy    *uint     `gorm:""`
	CheckNote      string    `gorm:"not null;default:''"`
	CreatedAt      time.Time `gorm:"not null;default:now()"`
	UpdatedAt      time.Time `gorm:"not null;default:now()"`
	DecidedAt      *time.Time
}

// All lists every model, in dependency order. The drift check and the test
// helpers use it; a new model must be added here and to a migration.
func All() []any {
	return []any{
		&User{}, &Group{}, &Event{}, &EventHost{}, &EventOccurrence{},
		&Membership{}, &Registration{}, &Setting{}, &AuditLog{}, &LoginThrottle{},
		&Notification{}, &AccountToken{}, &Invitation{}, &MailOutbox{}, &NotificationDelivery{},
		&AccountRequest{}, &GuestSignup{}, &CalendarToken{},
		&Household{}, &HouseholdAccount{}, &HouseholdPerson{}, &GuardianLink{},
	}
}

// Notification is one stored in-app notification for one recipient (5.12,
// DEC-42). Write it through internal/notify. Title, body and link are
// rendered when the row is written. Waiting rows are tasks: they stay in
// the badge until ResolvedAt is set.
type Notification struct {
	ID          uint   `gorm:"primaryKey"`
	UserID      uint   `gorm:"not null"`
	Kind        string `gorm:"not null"`
	Category    string `gorm:"not null"`
	Priority    int    `gorm:"not null;default:0"`
	Waiting     bool   `gorm:"not null;default:false"`
	ActorID     *uint
	SubjectType string `gorm:"not null;default:''"`
	SubjectID   *uint
	GroupKey    *string
	DedupeKey   *string
	Title       string `gorm:"not null"`
	Body        string `gorm:"not null;default:''"`
	// EmailTitle/EmailBody are the link-only rendering for email (5.12,
	// M1.2): RenderEmailSafe when the kind has one, Render otherwise. The
	// in-app title/body may name people; the email variants of sensitive
	// kinds name nobody.
	EmailTitle   string    `gorm:"not null;default:''"`
	EmailBody    string    `gorm:"not null;default:''"`
	Link         string    `gorm:"not null"`
	Actions      string    `gorm:"type:jsonb;not null;default:'[]'"`
	BatchCount   int       `gorm:"not null;default:1"`
	Data         string    `gorm:"type:jsonb;not null;default:'{}'"`
	CreatedAt    time.Time `gorm:"not null;default:now()"`
	UpdatedAt    time.Time `gorm:"not null;default:now()"`
	SeenAt       *time.Time
	ReadAt       *time.Time
	ArchivedAt   *time.Time
	ResolvedAt   *time.Time
	ResolvedByID *uint
	Resolution   string `gorm:"not null;default:''"`
	ExpiresAt    *time.Time
}
