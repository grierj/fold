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
	RegistrationChanged    = "registration.changed"
	RegistrationRestored   = "registration.restored"
	HouseholdUpdated       = "household.updated"
	HouseholdPersonUpdated = "household.person_updated"
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
	// optional reason. Rows before DEC-48 have oldSource/newSource instead. The group's other leaders and the admins get a stored notification (M1.1c).
	VisibilityOverride = "event.visibility_override"

	// VisibilityChanged: any other visibility change (stricter, back to
	// the group's value, tightened because the group became more private,
	// or a church-wide public/private switch). Same meta shape, without a
	// notification.
	VisibilityChanged = "event.visibility_changed"

	// RegistrationCancelled: an event became more private (its group was
	// made more private, DEC-48) and this person could no longer see it, so
	// their registrations on upcoming dates were removed. Target is the
	// event. Meta: userId, dates, newVisibility, reason. The person gets a
	// stored notification.
	RegistrationCancelled = "registration.cancelled"

	// RegistrationRemoved: a leader or host pruned someone's registration
	// (DEC-20, DEC-30, M1.5). Target is the event. Meta: userId,
	// occurrenceId, note.
	RegistrationRemoved = "registration.removed"

	// RegistrationAdded: a leader, host, or admin added someone to an
	// occurrence directly (M1.5). Target is the event. Meta: userId,
	// occurrenceId.
	RegistrationAdded = "registration.added"

	// RegistrationPromoted: a leader or host promoted one waitlisted row
	// by hand (M1.5). Target is the event. Meta: userId, occurrenceId.
	// Automatic transitions also record registration.changed.
	RegistrationPromoted = "registration.promoted"

	// RegistrationAddedOverCapacity: a leader, host, or admin added
	// someone over the event capacity by explicit choice (M1.5, DEC-47).
	// Target is the event. Meta: userId, occurrenceId, seats, capacity.
	RegistrationAddedOverCapacity = "registration.added_over_capacity"

	// RegistrationNotRestored: a disabled account was enabled while the
	// occurrence was full, so that registration was dropped and the
	// person was told to ask a leader or host (DEC-38, DEC-47, M1.5).
	// Meta: userId, occurrenceId, seats, capacity.
	RegistrationNotRestored = "registration.not_restored"

	// GroupApproved / GroupRejected: an admin decided a pending group
	// (M1.1c). Target is the group. The admins' notification tasks are
	// resolved in the same transaction.
	GroupApproved = "group.approved"
	GroupRejected = "group.rejected"

	// InvitationCreated: an admin or leader invited someone (M1.2, DEC-4).
	// Target is the invitation; meta has email and the optional groupId and
	// overLimit.
	InvitationCreated        = "invitation.created"
	InvitationResent         = "invitation.resent"
	InvitationRevoked        = "invitation.revoked"
	HouseholdPersonConverted = "household.person_converted"

	// InvitationAccepted: the person accepted and got an active account.
	// Target is the invitation; meta has userId and the optional groupId.
	InvitationAccepted = "invitation.accepted"

	// InvitationRateLimited: a leader hit the daily invitation limit (10.1).
	// Target is the leader's user row; meta has email and the limit.
	InvitationRateLimited = "invitation.rate_limited"

	// PasswordResetRequested / PasswordResetCompleted: a password reset was
	// requested and completed (M1.2). Target is the user. The request is
	// only audited when the address has an account (the response is uniform
	// either way, 10.1).
	PasswordResetRequested = "password.reset_requested"
	PasswordResetCompleted = "password.reset_completed"

	// EmailChangeRequested / EmailChanged: a verified email change (10.1,
	// M1.2). Target is the user; meta has oldEmail and newEmail.
	EmailChangeRequested = "email.change_requested"
	EmailChanged         = "email.changed"

	// AccountRequested: a self-signup or member invite-a-friend created
	// an account request (DEC-4, M1.3). Target is the request; meta has
	// source, accountType, and hasWarnings (never the email or phone).
	AccountRequested = "account.requested"

	// AccountApproved / AccountRejected: an admin decided an account
	// request (DEC-4). Target is the request; meta has userId (created
	// or already existing) on approval and the optional decisionNote is
	// kept on the request row, not here.
	AccountApproved = "account.approved"
	AccountRejected = "account.rejected"

	// AccountExpired: a pending request passed its 30-day expiry
	// (DEC-4). Target is the request; the actor is nil (system).
	AccountExpired = "account.expired"

	// AccountSetupCompleted: an approved person set their first
	// password from the emailed link. Target is the user.
	AccountSetupCompleted = "account.setup_completed"

	// SelfSignupChanged: an admin turned self-signup between request
	// and off (DEC-4). Target type is setting; meta has mode.
	SelfSignupChanged = "setting.self_signup_changed"

	// CalendarTokenRegenerated: a person created their calendar-feed
	// link or made a new one, which revokes the old link (M1.7, 8.2).
	// Target is the user; meta has scope.
	CalendarTokenRegenerated = "calendar_token.regenerated"

	// CalendarTokenRevoked: a calendar-feed link was turned off, by its
	// owner or by an admin (the actor says which). Target is the user.
	CalendarTokenRevoked = "calendar_token.revoked"

	// CalendarTokenScopeChanged: the feed switched between "mine" and
	// "all" (8.2 Options A and B). Target is the user; meta has
	// oldScope and newScope.
	CalendarTokenScopeChanged = "calendar_token.scope_changed"

	// HouseholdCreated: an account got a household on first use (M1.9,
	// 5.3). Target is the household.
	HouseholdCreated = "household.created"

	// HouseholdPersonAdded: an adult account in the household added a
	// household person (M1.9). Target is the household; meta has
	// personId, kind, and guardianLinkId when a child or teen started a
	// pending link (DEC-32).
	HouseholdPersonAdded = "household.person_added"

	// HouseholdPersonRemoved: a household person was removed (M1.9).
	// Target is the household; meta has personId and kind. Any live
	// guardian link is revoked alongside (GuardianLinkRevoked).
	HouseholdPersonRemoved = "household.person_removed"

	// GuardianLinkConfirmed / GuardianLinkRejected: an admin decided a
	// pending guardian link (DEC-32). Target is the link; meta has
	// guardianUserId, personId (or teenUserId), checkNote, and selfConfirm
	// when a sole admin confirmed their own link (DEC-79). The admins'
	// notification tasks are resolved in the same transaction.
	GuardianLinkConfirmed = "guardian_link.confirmed"
	GuardianLinkRejected  = "guardian_link.rejected"

	// GuardianLinkRevoked: a live guardian link ended because the
	// household person was removed or stopped being a child or teen
	// (M1.9). Target is the link; meta has guardianUserId and personId.
	GuardianLinkRevoked = "guardian_link.revoked"
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
