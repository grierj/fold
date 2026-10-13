// Package audit writes AuditLog rows (5.3, 10.6). Every milestone that
// records a sensitive action uses Write, inside the same transaction as the
// change it records.
package audit

import (
	"encoding/json"

	"fold/models"

	"gorm.io/gorm"
)

// Actions. Add new ones here so they are easy to find and query.
const (
	// RoleChanged: someone was made a group leader, or stopped being one.
	// Target is the group; meta has userId, oldRole and newRole ("leader",
	// "member", or "none" when they left or were removed).
	RoleChanged = "membership.role_changed"

	// MemberRemoved: a leader or admin removed someone from a group (DEC-35).
	// Target is the group; meta has userId, oldRole ("leader" or "member"),
	// and the optional note, which is never shown to the removed person.
	MemberRemoved = "membership.removed"

	// UserDeleted: an admin deleted an account. Target is the user. Any
	// sole-leader handoff is recorded as RoleChanged rows with reason
	// "account_deleted" (DEC-36).
	UserDeleted = "user.deleted"

	// UserDisabled: an admin disabled an account (handoff rows use reason
	// "account_disabled").
	UserDisabled = "user.disabled"

	// UserEnabled: an admin enabled a disabled account again.
	UserEnabled = "user.enabled"

	// MemberRestored: a disabled account was enabled and one of its kept
	// memberships came back as a plain member (DEC-38). Meta: userId, status.
	MemberRestored = "membership.restored"

	// AddedOverLimit: a leader or admin added someone to a full group by
	// explicit choice (DEC-39). Meta: userId, count, limit, role.
	AddedOverLimit = "membership.added_over_limit"

	// MemberNotRestored: a disabled account was enabled while the group was
	// full, so that membership was dropped and the person was told to ask a
	// leader (DEC-47). Meta: userId, count, limit. Older logs may still hold
	// membership.return_approved / return_declined rows from DEC-39.
	MemberNotRestored = "membership.not_restored"

	// VisibilityOverride: a group event was made more open than its group,
	// or kept open when the group became more private (DEC-6, DEC-17,
	// DEC-28, DEC-48). Target is the event. Meta has oldVisibility,
	// newVisibility, oldLeaderOpened, leaderOpened, groupVisibility, and an
	// optional reason. Rows before DEC-48 have oldSource/newSource instead. The group's other leaders and the admins see a derived notice.
	VisibilityOverride = "event.visibility_override"

	// VisibilityChanged: any other visibility change (stricter, back to
	// the group's value, tightened because the group became more private,
	// or a church-wide public/private switch). Same meta shape, without a
	// notice.
	VisibilityChanged = "event.visibility_changed"

	// RegistrationCancelled: an event became more private (its group was
	// made more private, DEC-48) and this person could no longer see it, so
	// their registrations on upcoming dates were removed. Target is the
	// event. Meta: userId, dates, newVisibility, reason. The person sees a
	// derived notice.
	RegistrationCancelled = "registration.cancelled"
)

// Entry is one audit record. Actor is nil for system actions.
type Entry struct {
	Actor      *uint
	Action     string
	TargetType string
	TargetID   uint
	Meta       map[string]any
	IP         string
}

// Write stores e using db, which should be the caller's transaction.
func Write(db *gorm.DB, e Entry) error {
	meta := []byte("{}")
	if len(e.Meta) > 0 {
		var err error
		if meta, err = json.Marshal(e.Meta); err != nil {
			return err
		}
	}
	return db.Create(&models.AuditLog{
		ActorID:    e.Actor,
		Action:     e.Action,
		TargetType: e.TargetType,
		TargetID:   e.TargetID,
		MetaJSON:   string(meta),
		IP:         e.IP,
	}).Error
}
