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

// Who can see an active group (C3). internal/policy decides what each means.
const (
	VisibilityPublic     = "public"      // anyone, including logged-out visitors
	VisibilityPrivate    = "private"     // signed-in users
	VisibilityInviteOnly = "invite_only" // members and admins only
)

// RSVP statuses.
const (
	RSVPGoing    = "going"
	RSVPMaybe    = "maybe"
	RSVPDeclined = "declined"
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
// not a single event. Meetings are its instances.
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
	// DeletionRequested is set by the leader when they are the only member.
	// An admin approves it by deleting the group.
	DeletionRequested bool `gorm:"not null;default:false"`
	CreatedAt         time.Time
	UpdatedAt         time.Time
	DeletedAt         gorm.DeletedAt `gorm:"index"`
}

// Meeting is a scheduled occurrence series belonging to a group.
// StartsAt is the first occurrence; RecurrenceRule (RFC 5545 RRULE,
// e.g. "FREQ=WEEKLY;BYDAY=WE") generates the rest. Empty rule = one-time.
type Meeting struct {
	ID             uint      `gorm:"primaryKey"`
	GroupID        uint      `gorm:"not null;index"`
	Group          Group     `gorm:"foreignKey:GroupID"`
	StartsAt       time.Time `gorm:"not null"`
	EndsAt         time.Time `gorm:"not null"`
	Location       string    // empty = use the group's location
	RecurrenceRule string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	DeletedAt      gorm.DeletedAt `gorm:"index"`
}

// MeetingException cancels (or in future, reschedules) a single instance
// of a recurring meeting, addressed by date (YYYY-MM-DD).
type MeetingException struct {
	ID        uint    `gorm:"primaryKey"`
	MeetingID uint    `gorm:"not null;uniqueIndex:idx_meeting_date"`
	Meeting   Meeting `gorm:"foreignKey:MeetingID"`
	Date      string  `gorm:"not null;uniqueIndex:idx_meeting_date"` // YYYY-MM-DD
	Cancelled bool    `gorm:"default:false"`
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

// RSVP records a user's response to one meeting instance,
// addressed by (meeting, date).
type RSVP struct {
	ID        uint    `gorm:"primaryKey"`
	UserID    uint    `gorm:"not null;uniqueIndex:idx_rsvp"`
	User      User    `gorm:"foreignKey:UserID"`
	MeetingID uint    `gorm:"not null;uniqueIndex:idx_rsvp"`
	Meeting   Meeting `gorm:"foreignKey:MeetingID"`
	Date      string  `gorm:"not null;uniqueIndex:idx_rsvp"` // YYYY-MM-DD
	Status    string  `gorm:"not null"`                      // going | maybe | declined
	CreatedAt time.Time
	UpdatedAt time.Time
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
		&User{}, &Group{}, &Meeting{}, &MeetingException{},
		&Membership{}, &RSVP{}, &Setting{}, &AuditLog{}, &LoginThrottle{},
	}
}
