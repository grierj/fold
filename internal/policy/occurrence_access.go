package policy

import (
	"time"
	"unicode/utf8"
)

const (
	RequestContact       = "contact"
	RequestGroupAdd      = "group_add"
	RequestOccurrenceAdd = "occurrence_add"

	OccurrenceAccessActive           = "active"
	OccurrenceAccessEnded            = "ended"
	OccurrenceOriginManagerAdd       = "manager_add"
	OccurrenceOriginDepartureSelf    = "departure_self"
	OccurrenceOriginDepartureRemoval = "departure_removal"
	OccurrenceDeclined               = "declined"
	OccurrenceBlocked                = "blocked"
	OccurrenceRemoved                = "removed"
	OccurrenceDepartureSuperseded    = "departure_superseded"
	OccurrenceAlreadyAdded           = "already_added"
	OccurrenceCancelled              = "occurrence_cancelled"
	OccurrenceEnded                  = "occurrence_ended"
	OccurrenceDeleted                = "occurrence_deleted"
	OccurrenceEventDeleted           = "event_deleted"
	OccurrenceGroupDeleted           = "group_deleted"
	OccurrenceConsentLost            = "consent_lost"
	OccurrenceContextChanged         = "context_changed"
)

// OrganizerScope is internal identity, NOT a safe recipient DTO. Group events
// use their group, church-wide events the event. Never expose a hidden group.
type OrganizerScope struct {
	Type string
	ID   uint
}

func (s OrganizerScope) valid() bool {
	return s.ID > 0 && (s.Type == ContactGroup || s.Type == ContactEvent)
}

// AddOptions holds resolved FINAL account-add options, not PATCH semantics.
// At creation, resolve missing input using the existing registration. Persist
// these values unchanged and validate again against CURRENT event capabilities
// and capacity/childcare under the occurrence lock at completion. OverCapacity
// and OverLimit are explicit manager choices, NEVER inferred from approvals.
// An account add has no household/proxy target; group adds use member role and
// preserve any existing role. ChildcareRequested belongs to household rows only.
type AddOptions struct {
	OverLimit         bool
	OverCapacity      bool
	AttendanceMode    string
	GuestCount        int
	ChildcareCount    int
	ChildcareAgesNote string
}

func ValidAddOptions(purpose string, o AddOptions) bool {
	switch purpose {
	case RequestGroupAdd:
		return !o.OverCapacity && o.AttendanceMode == "" && o.GuestCount == 0 && o.ChildcareCount == 0 && o.ChildcareAgesNote == ""
	case RequestOccurrenceAdd:
		return !o.OverLimit && (o.AttendanceMode == "in_person" || o.AttendanceMode == "remote") &&
			o.GuestCount >= 0 && o.GuestCount <= 5 && o.ChildcareCount >= 0 && o.ChildcareCount <= 20 && utf8.RuneCountInString(o.ChildcareAgesNote) <= 200
	}
	return false
}

// AddScope cannot be used as ContactScope. There are no contact fields; an add
// never authorizes disclosure. OccurrenceID is a stable row ID, not a date key:
// moving that occurrence keeps the invitation, regeneration never copies it.
type AddScope struct {
	Purpose       string
	RequesterID   uint
	SubjectUserID uint
	ContextType   string
	ContextID     uint
	OccurrenceID  uint // zero ONLY for group_add
	Options       AddOptions
}

func validAddScope(s AddScope) bool {
	return s.RequesterID > 0 && s.SubjectUserID > 0 && s.RequesterID != s.SubjectUserID && s.ContextID > 0 &&
		ValidAddOptions(s.Purpose, s.Options) &&
		((s.Purpose == RequestGroupAdd && s.ContextType == ContactGroup && s.OccurrenceID == 0) ||
			(s.Purpose == RequestOccurrenceAdd && s.ContextType == ContactEvent && s.OccurrenceID > 0))
}

// AddFacts are CURRENT locked facts, not submitted assertions. Manager is
// loaded with CanManageGroup/Event, including admins. OrdinaryEligible is the
// existing target-add policy, NOT contact visibility. LatestAccessEndedAt is
// the most recent retained end for this exact subject/date (nil if none).
// Direct re-add is forbidden; a NEW request created no earlier than that end
// may create fresh access without reopening the old record. ExistingAccessActive
// is for this exact subject/date too, never unrelated accepted invitations.
type AddFacts struct {
	Requester            ContactAccount
	Subject              ContactAccount
	ContextType          string
	ContextID            uint
	ContextActive        bool
	Manager              bool
	OrdinaryEligible     bool
	Occurrence           OccurrenceFacts
	Block                *ManagerAddBlock
	LatestAccessEndedAt  *time.Time
	ExistingAccessActive bool
	Now                  time.Time
}

// AddRequest reuses the explicit decision vocabulary, not contact authority.
// All approvals may remain pending while capacity/childcare prevents completion.
// Only successful domain add + provenance + audit/notices may set granted.
type AddRequest struct {
	AddScope
	NeedsAdmin      bool
	SubjectDecision string
	AdminDecision   string
	Status          string
	CreatedAt       time.Time
	ExpiresAt       time.Time
}

func AddIneligibility(s AddScope, f AddFacts, needsAdmin bool) string {
	if !validAddScope(s) || f.Now.IsZero() || !f.Requester.Active || !f.Requester.adult() || !f.Subject.Active || f.Subject.Person == nil ||
		f.Requester.Person.ID != s.RequesterID || f.Subject.Person.ID != s.SubjectUserID {
		return ContactAccountDisabled
	}
	if needsAdmin && f.Subject.AgeYears != nil && *f.Subject.AgeYears >= 18 {
		return ContactTurned18
	}
	if (needsAdmin && !f.Subject.teen()) || (!needsAdmin && !f.Subject.adult()) {
		return ContactAccountDisabled
	}
	if !f.ContextActive || f.ContextType != s.ContextType || f.ContextID != s.ContextID {
		return ContactContextEnded
	}
	if !f.Manager {
		return ContactLeftRole
	}
	if s.Purpose == RequestOccurrenceAdd {
		if !f.Occurrence.live() || f.Occurrence.ID != s.OccurrenceID || f.Occurrence.EventID != s.ContextID {
			return ContactContextEnded
		}
		if f.ExistingAccessActive {
			return OccurrenceAlreadyAdded
		}
		if ManagerAddBlocked(f.Block, s.SubjectUserID, f.Occurrence.Organizer) {
			return OccurrenceBlocked
		}
	}
	return ""
}

func CanCreateAddRequest(s AddScope, f AddFacts, openCount int) bool {
	teen := f.Subject.Person != nil && f.Subject.Person.Teen
	return AddIneligibility(s, f, teen) == "" && (teen || !f.OrdinaryEligible || (s.Purpose == RequestOccurrenceAdd && f.LatestAccessEndedAt != nil)) && openCount >= 0 && openCount < ContactOpenLimit
}

func pendingAddRequest(r AddRequest, f AddFacts) bool {
	return r.Status == ContactPending && validContactDecisions(ContactRequest{NeedsAdmin: r.NeedsAdmin, SubjectDecision: r.SubjectDecision, AdminDecision: r.AdminDecision}) &&
		r.SubjectDecision != ContactDenied && r.AdminDecision != ContactDenied &&
		!r.CreatedAt.IsZero() && r.ExpiresAt.After(r.CreatedAt) && !f.Now.Before(r.CreatedAt) && f.Now.Before(r.ExpiresAt) &&
		AddIneligibility(r.AddScope, f, r.NeedsAdmin) == "" &&
		(r.Purpose != RequestOccurrenceAdd || f.LatestAccessEndedAt == nil || !r.CreatedAt.Before(*f.LatestAccessEndedAt))
}

// AddRequestStatusAfterDecision returns pending even for the final approval.
// Store the selected party decision; then CanFinalizeAddRequest permits the
// existing helper to attempt completion. A capacity failure retains approved
// decisions in pending state, retryable only until the original expiry.
func AddRequestStatusAfterDecision(actor ContactAccount, party, decision string, r AddRequest, f AddFacts) string {
	if !actor.Active || actor.Person == nil || actor.Person.ID == 0 || !pendingAddRequest(r, f) || (decision != ContactApproved && decision != ContactDenied) {
		return ""
	}
	status := requestDecision(actor, party, decision, r.SubjectUserID, r.NeedsAdmin, r.SubjectDecision, r.AdminDecision)
	if status == ContactGranted {
		return ContactPending
	}
	return status
}

// CanFinalizeAddRequest is authorization, NOT a capacity override or success.
// Run under the shared contact gate, then domain locks, before the actual add.
func CanFinalizeAddRequest(r AddRequest, f AddFacts) bool {
	return pendingAddRequest(r, f) && r.SubjectDecision == ContactApproved &&
		(r.AdminDecision == ContactApproved || r.AdminDecision == ContactNotNeeded)
}

func AddRequestExpired(r AddRequest, now time.Time) bool {
	return r.Status == ContactPending && !now.IsZero() && !r.ExpiresAt.IsZero() && !now.Before(r.ExpiresAt)
}

func CanWithdrawAddRequest(actor ContactAccount, r AddRequest, now time.Time) bool {
	return validAddScope(r.AddScope) && actor.Active && actor.Person != nil && actor.Person.ID == r.RequesterID &&
		r.Status == ContactPending && !now.IsZero() && !r.CreatedAt.IsZero() && !now.Before(r.CreatedAt) && r.ExpiresAt.After(now)
}

// Review grants only a purpose-specific safe preview (title/requested date),
// never ordinary event/group presentation, contact or actual read authority.
func CanViewAddRequest(actor ContactAccount, r AddRequest) bool {
	return validAddScope(r.AddScope) && actor.Active && actor.Person != nil && actor.Person.ID > 0 &&
		(actor.Person.ID == r.RequesterID || actor.Person.ID == r.SubjectUserID || (actor.adult() && actor.Person.Admin))
}

// OccurrenceFacts describe LIVE domain rows, loaded even for explicit invites.
// ContextActive excludes deleted/archived groups and deleted events. Scheduled
// excludes cancelled/deleted occurrences. Organizer matches the CURRENT event.
type OccurrenceFacts struct {
	ID            uint
	EventID       uint
	Organizer     OrganizerScope
	ContextActive bool
	Scheduled     bool
	EndsAt        time.Time
}

func (f OccurrenceFacts) live() bool {
	return f.ID > 0 && f.EventID > 0 && f.Organizer.valid() && f.ContextActive && f.Scheduled &&
		(f.Organizer.Type != ContactEvent || f.Organizer.ID == f.EventID)
}

type OccurrenceAccess struct {
	SubjectUserID         uint
	OccurrenceID          uint
	EventID               uint
	Organizer             OrganizerScope
	Origin                string
	IssuerID              uint
	RequestID             uint // zero for a direct adult add or any departure origin
	RegistrationID        uint
	DepartureMembershipID uint
	DepartureActorID      uint
	Status                string
	GrantedAt             time.Time
	EndedAt               *time.Time
}

// ValidOccurrenceAccessOrigin validates immutable provenance shape only. The
// database validates the locked live membership and actor authority when a
// departure row is inserted. Reads must not require that membership to remain
// active: leaving is the reason this date-only access exists.
func ValidOccurrenceAccessOrigin(a OccurrenceAccess) bool {
	switch a.Origin {
	case OccurrenceOriginManagerAdd:
		return a.IssuerID > 0 && a.IssuerID != a.SubjectUserID && a.DepartureMembershipID == 0 && a.DepartureActorID == 0
	case OccurrenceOriginDepartureSelf:
		return a.Organizer.Type == ContactGroup && a.IssuerID == 0 && a.RequestID == 0 &&
			a.DepartureMembershipID > 0 && a.DepartureActorID == a.SubjectUserID
	case OccurrenceOriginDepartureRemoval:
		return a.Organizer.Type == ContactGroup && a.IssuerID == 0 && a.RequestID == 0 &&
			a.DepartureMembershipID > 0 && a.DepartureActorID > 0 && a.DepartureActorID != a.SubjectUserID
	default:
		return false
	}
}

// CanRetainOccurrenceAfterDeparture is the pure membership authorization T4
// must re-check under its departure locks. The caller chooses the matching
// origin and then persists the locked membership ID and actor ID; the database
// independently verifies those facts before accepting access provenance.
func CanRetainOccurrenceAfterDeparture(origin string, actor, subject *Person, actorRel, subjectRel Rel, leaders int) bool {
	if actor == nil || subject == nil || actor.ID == 0 || subject.ID == 0 {
		return false
	}
	switch origin {
	case OccurrenceOriginDepartureSelf:
		return actor.ID == subject.ID && CanLeaveGroup(subjectRel, leaders)
	case OccurrenceOriginDepartureRemoval:
		return actor.ID != subject.ID && CanRemoveMember(actor, actorRel, subject, subjectRel, leaders)
	default:
		return false
	}
}

// OccurrenceAccessCurrent intentionally ignores issuer status/management,
// departure membership status and organizer blocks: they stop NEW manager adds
// or authorize the departure transaction, not already accepted attendance. It
// requires the recipient's active authenticated account (current consent), not
// an acting guardian/proxy. Lifecycle writes must persist an irreversible end
// on disable/consent loss/deletion/cancellation BEFORE committing that mutation;
// live checks close the cleanup window, but cannot detect a historical disable
// followed by reenable unless the end was recorded.
func OccurrenceAccessCurrent(actor ContactAccount, a OccurrenceAccess, f OccurrenceFacts, now time.Time) bool {
	if !actor.Active || actor.Person == nil || actor.Person.ID == 0 || actor.Person.ID != a.SubjectUserID ||
		!ValidOccurrenceAccessOrigin(a) || a.RegistrationID == 0 ||
		a.Status != OccurrenceAccessActive || a.EndedAt != nil || a.GrantedAt.IsZero() || now.IsZero() || now.Before(a.GrantedAt) ||
		!f.live() || a.OccurrenceID != f.ID || a.EventID != f.EventID || a.Organizer != f.Organizer {
		return false
	}
	// DEC-74 retained-departure authority ends exactly with the occurrence.
	// A manager invitation selected at departure is first superseded by a new
	// departure-origin row; unrelated manager invitations keep their established
	// lifecycle.
	return a.Origin == OccurrenceOriginManagerAdd || (!f.EndsAt.IsZero() && now.Before(f.EndsAt))
}

// CanSeeOccurrence never changes CanSeeEvent or series/group/roster/contact/
// management/proxy rights. ordinaryAccess MUST come from CanSeeEvent for these
// same facts. Lists union exact allowed occurrence IDs, NEVER parent event IDs.
func CanSeeOccurrence(actor ContactAccount, ordinaryAccess bool, a OccurrenceAccess, f OccurrenceFacts, now time.Time) bool {
	return ordinaryAccess || OccurrenceAccessCurrent(actor, a, f, now)
}

func CanSeeOccurrenceVideo(actor ContactAccount, ordinaryAccess, ordinaryManager bool, a OccurrenceAccess, f OccurrenceFacts, now time.Time, reveal, registrationStatus string) bool {
	return CanSeeOccurrence(actor, ordinaryAccess, a, f, now) &&
		CanSeeVideoLink(reveal, registrationStatus == "going", ordinaryAccess && ordinaryManager)
}

// Invitations do not bypass the existing attendee boundary. Override-only
// recipients receive their own RSVP, never a roster/contact/attendee directory.
func CanSeeOccurrenceAttendees(ordinaryAccess, ordinaryAttendeeNames bool) bool {
	return ordinaryAccess && ordinaryAttendeeNames
}

type ManagerAddBlock struct {
	SubjectUserID uint
	Organizer     OrganizerScope
}

func ManagerAddBlocked(b *ManagerAddBlock, subjectID uint, organizer OrganizerScope) bool {
	return b != nil && subjectID > 0 && organizer.valid() && b.SubjectUserID == subjectID && b.Organizer == organizer
}

// Removal/Can't go and block operate on ONE retained subject/date invitation.
// Managers are current scoped managers, not necessarily the original issuer.
// The API releases/promotes seats under its existing occurrence lock. Blocking
// separately upserts organizer scope and closes pending asks; other accepted
// dates and unrelated membership remain untouched. No guardian/proxy shortcut.
func CanEndOccurrenceAccess(actor ContactAccount, currentManager bool, a OccurrenceAccess) bool {
	return actor.Active && actor.Person != nil && actor.Person.ID > 0 && a.Status == OccurrenceAccessActive && a.EndedAt == nil &&
		a.SubjectUserID > 0 && a.OccurrenceID > 0 && a.Organizer.valid() &&
		(actor.Person.ID == a.SubjectUserID || (actor.adult() && currentManager))
}

func CanBlockManagerAdds(actor ContactAccount, a OccurrenceAccess) bool {
	return actor.Person != nil && actor.Person.ID == a.SubjectUserID && CanEndOccurrenceAccess(actor, false, a)
}
