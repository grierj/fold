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

type User struct {
	ID          uint   `gorm:"primaryKey"`
	Username    string `gorm:"unique;not null"`
	Password    string `gorm:"not null"` // bcrypt hash
	Name        string // full name
	DisplayName string // shown in the UI; falls back to Name, then Username
	Email       string
	Phone       string // first-class: SMS is a required channel
	AvatarKey   string // blob-store key for the avatar image ("" = none)
	// AvatarFocalX/Y is the focal point as a 0-100 percentage, used for
	// CSS object-position so faces stay centered in cropped avatars.
	AvatarFocalX float64 `gorm:"default:50"`
	AvatarFocalY float64 `gorm:"default:50"`
	Role         string  `gorm:"default:member"` // admin | member
	// SessionVersion is bumped when the password changes so older cookies
	// stop working. Missing cookies count as 0, which matches existing rows.
	SessionVersion int `gorm:"not null;default:0"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
	DeletedAt      gorm.DeletedAt `gorm:"index"`
}

// PublicName returns the name shown in the UI.
func (u *User) PublicName() string {
	if u.DisplayName != "" {
		return u.DisplayName
	}
	if u.Name != "" {
		return u.Name
	}
	return u.Username
}

// HashPassword hashes the password before saving
func (u *User) BeforeCreate(tx *gorm.DB) (err error) {
	hashed, err := bcrypt.GenerateFromPassword([]byte(u.Password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	u.Password = string(hashed)
	return nil
}

// CheckPassword compares a password with the stored hash
func (u *User) CheckPassword(password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(u.Password), []byte(password)) == nil
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

// All lists every model, in dependency order. The drift check and the test
// helpers use it; a new model must be added here and to a migration.
func All() []any {
	return []any{
		&User{}, &Group{}, &Meeting{}, &MeetingException{},
		&Membership{}, &RSVP{}, &Setting{}, &AuditLog{},
	}
}
