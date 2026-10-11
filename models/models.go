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
const (
	VisibilitySourceGroup  = "group"
	VisibilitySourceCustom = "custom"
)

// Registration statuses (renamed from RSVP, M1.1a).
const (
	RegGoing    = "going"
	RegMaybe    = "maybe"
	RegDeclined = "declined"
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
	// SessionVersion is bumped when the password, email, role or status
	// changes, so older cookies stop working (10.1).
	SessionVersion int `gorm:"not null;default:0"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
	DeletedAt      gorm.DeletedAt `gorm:"index"`
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
	// Visibility is public, private, or invite_only. With VisibilitySource
	// group, it tracks the group's visibility. Church-wide events are
	// public or private and always custom (a CHECK enforces that).
	Visibility       string  `gorm:"not null"`
	VisibilitySource string  `gorm:"not null;default:group"`
	StartsLocal      string  `gorm:"not null"` // YYYY-MM-DDTHH:MM, no offset
	DurationMinutes  int     `gorm:"not null"`
	TZID             string  `gorm:"column:tzid;not null"`
	RRule            string  `gorm:"column:rrule;not null"`
	RRuleUntil       *string `gorm:"column:rrule_until"` // YYYY-MM-DD, inclusive
	Location         string  `gorm:"not null"`           // empty = the group's location
	CreatedBy        uint    `gorm:"not null"`
	Creator          User    `gorm:"foreignKey:CreatedBy"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
	DeletedAt        gorm.DeletedAt `gorm:"index"`
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
	CreatedAt        time.Time
	UpdatedAt        time.Time
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
type Registration struct {
	ID           uint            `gorm:"primaryKey"`
	OccurrenceID uint            `gorm:"not null;uniqueIndex:idx_registration"`
	Occurrence   EventOccurrence `gorm:"foreignKey:OccurrenceID"`
	UserID       uint            `gorm:"not null;uniqueIndex:idx_registration"`
	User         User            `gorm:"foreignKey:UserID"`
	Status       string          `gorm:"not null"` // going | maybe | declined
	CreatedAt    time.Time
	UpdatedAt    time.Time
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

// All lists every model, in dependency order. The drift check and the test
// helpers use it; a new model must be added here and to a migration.
func All() []any {
	return []any{
		&User{}, &Group{}, &Event{}, &EventHost{}, &EventOccurrence{},
		&Membership{}, &Registration{}, &Setting{}, &AuditLog{}, &LoginThrottle{},
	}
}
