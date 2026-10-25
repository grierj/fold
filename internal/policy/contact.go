package policy

import "time"

const (
	ContactEmail = "email"
	ContactPhone = "phone"
	ContactBoth  = "both"

	ContactGroup = "group"
	ContactEvent = "event"

	ContactPending   = "pending"
	ContactGranted   = "granted"
	ContactDenied    = "denied"
	ContactWithdrawn = "withdrawn"
	ContactExpired   = "expired"
	ContactEnded     = "ended"

	ContactApproved  = "approved"
	ContactNotNeeded = "not_needed"
	ContactSubject   = "subject"
	ContactAdmin     = "admin"

	ContactRevokedBySubject  = "revoked_by_subject"
	ContactRevokedByAdmin    = "revoked_by_admin"
	ContactRevokedByGuardian = "revoked_by_guardian"
	ContactLeftRole          = "left_role"
	ContactLeftContext       = "left_context"
	ContactContextEnded      = "context_ended"
	ContactTurned18          = "turned_18"
	ContactAccountDisabled   = "account_disabled"
	ContactEventEnded        = "event_ended"

	ContactOpenLimit       = 10
	ContactReasonLimit     = 1000
	ContactRequestLifetime = 14 * 24 * time.Hour
	ContactEventGrace      = 30 * 24 * time.Hour
)

// ContactAccount contains current, loaded facts. Active excludes deleted,
// disabled, invited, guest and pending-consent accounts. AgeYears is calculated
// on the church-local date; teens with no known age fail closed (DEC-83).
// Adult accounts need no birth date, but a known underage adult fails closed.
type ContactAccount struct {
	Person          *Person
	Active          bool
	AgeYears        *int
	HideFromLeaders bool
}

// CanSearchPeople allows only active adult managers; a historical host row on
// a group event is not a host fact. Search never grants add/visibility rights.
func CanSearchPeople(actor ContactAccount, leadsGroup, hostsEvent bool) bool {
	return actor.Active && actor.adult() && (actor.Person.Admin || leadsGroup || hostsEvent)
}

// CanInitiatePeopleAdd is independent of scope permission, which the group or
// event policy must also grant. It keeps teen managers from using legacy adds.
func CanInitiatePeopleAdd(actor ContactAccount) bool { return actor.Active && actor.adult() }

// TeenAddNeedsApproval applies to every manager-initiated account add, including
// admins and email/raw-ID siblings. Self/proxy/household/invitation flows differ.
func TeenAddNeedsApproval(subject *Person) bool { return subject != nil && subject.Teen }

func (a ContactAccount) adult() bool {
	return a.Person != nil && !a.Person.Teen && (a.AgeYears == nil || *a.AgeYears >= 18)
}

func (a ContactAccount) teen() bool {
	return a.Person != nil && a.Person.Teen && a.AgeYears != nil && *a.AgeYears >= 13 && *a.AgeYears < 18
}

// ContactContext facts are for this requester and subject in exactly Type/ID.
// EventManager is loaded using CanManageEvent; mere visibility is insufficient.
// EventRegistrant means an actual current registration, not an invitation or
// membership. LastOccurrenceEnd is the final occurrence's end, not the next date.
type ContactContext struct {
	Type              string
	ID                uint
	Active            bool
	GroupLeader       bool
	GroupMember       bool
	EventManager      bool
	EventRegistrant   bool
	LastOccurrenceEnd *time.Time
}

type ContactAccessFacts struct {
	Requester ContactAccount
	Subject   ContactAccount
	Context   ContactContext
	Now       time.Time
}

// ContactScope is intentionally contact-only. AddScope is a distinct type:
// adapters MUST dispatch the stored purpose before constructing this scope.
// Contact grants never add membership, registration or occurrence visibility.
type ContactScope struct {
	RequesterID   uint
	SubjectUserID uint
	ContextType   string
	ContextID     uint
	Fields        string
}

func ValidContactFields(fields string) bool {
	return fields == ContactEmail || fields == ContactPhone || fields == ContactBoth
}

func validContactScope(s ContactScope) bool {
	return s.RequesterID > 0 && s.SubjectUserID > 0 && s.RequesterID != s.SubjectUserID &&
		s.ContextID > 0 && (s.ContextType == ContactGroup || s.ContextType == ContactEvent) && ValidContactFields(s.Fields)
}

func contactScopeMatches(s ContactScope, f ContactAccessFacts) bool {
	return validContactScope(s) && f.Requester.Person != nil && f.Subject.Person != nil &&
		s.RequesterID == f.Requester.Person.ID && s.SubjectUserID == f.Subject.Person.ID &&
		s.ContextType == f.Context.Type && s.ContextID == f.Context.ID
}

// ContactIneligibility returns the automatic terminal reason, or "" if the
// current relationship is eligible. needsAdmin is the request-time teen fact:
// actual adulthood ends that request/grant even if account_type is stale.
// Callers persist an end under transaction locks; a later regained role must
// never revive a previously ended grant.
func ContactIneligibility(f ContactAccessFacts, needsAdmin bool) string {
	if f.Now.IsZero() || !f.Requester.Active || !f.Subject.Active || f.Requester.Person == nil || f.Subject.Person == nil {
		return ContactAccountDisabled
	}
	if needsAdmin && f.Subject.AgeYears != nil && *f.Subject.AgeYears >= 18 {
		return ContactTurned18
	}
	if !f.Requester.adult() || (needsAdmin && !f.Subject.teen()) || (!needsAdmin && !f.Subject.adult()) {
		return ContactAccountDisabled
	}
	c := f.Context
	if !c.Active || c.ID == 0 {
		return ContactContextEnded
	}
	switch c.Type {
	case ContactGroup:
		if !c.GroupLeader {
			return ContactLeftRole
		}
		if !c.GroupMember {
			return ContactLeftContext
		}
	case ContactEvent:
		if !c.EventManager {
			return ContactLeftRole
		}
		if !c.EventRegistrant {
			return ContactLeftContext
		}
		if c.LastOccurrenceEnd == nil || !f.Now.Before(c.LastOccurrenceEnd.Add(ContactEventGrace)) {
			return ContactEventEnded
		}
	default:
		return ContactContextEnded
	}
	return ""
}

// CanCreateContactRequest does not grant access. Serialize openCount under the
// requester's account lock and persist Now+ContactRequestLifetime in the API.
// Neither admin nor guardian status bypasses the management relationship.
func CanCreateContactRequest(s ContactScope, f ContactAccessFacts, openCount int) bool {
	needsAdmin := f.Subject.Person != nil && f.Subject.Person.Teen
	return contactScopeMatches(s, f) && ContactIneligibility(f, needsAdmin) == "" &&
		(needsAdmin || f.Subject.HideFromLeaders) && openCount >= 0 && openCount < ContactOpenLimit
}

type ContactRequest struct {
	ContactScope
	NeedsAdmin      bool
	SubjectDecision string
	AdminDecision   string
	Status          string
	CreatedAt       time.Time
	ExpiresAt       time.Time
}

func validContactDecisions(r ContactRequest) bool {
	valid := func(d string) bool { return d == ContactPending || d == ContactApproved || d == ContactDenied }
	return valid(r.SubjectDecision) && ((r.NeedsAdmin && valid(r.AdminDecision)) || (!r.NeedsAdmin && r.AdminDecision == ContactNotNeeded))
}

// ContactRequestExpired uses a half-open interval: the exact expiry instant is
// already too late for a decision, grant or withdrawal.
func ContactRequestExpired(r ContactRequest, now time.Time) bool {
	return r.Status == ContactPending && !now.IsZero() && !r.ExpiresAt.IsZero() && !now.Before(r.ExpiresAt)
}

func pendingContactRequest(r ContactRequest, f ContactAccessFacts) bool {
	return r.Status == ContactPending && validContactDecisions(r) && r.SubjectDecision != ContactDenied && r.AdminDecision != ContactDenied &&
		!(r.SubjectDecision == ContactApproved && (!r.NeedsAdmin || r.AdminDecision == ContactApproved)) &&
		!r.CreatedAt.IsZero() && r.ExpiresAt.After(r.CreatedAt) && !f.Now.Before(r.CreatedAt) && f.Now.Before(r.ExpiresAt) &&
		contactScopeMatches(r.ContactScope, f) && ContactIneligibility(f, r.NeedsAdmin) == ""
}

// ContactRequestStatusAfterDecision returns "" for a refused transition. The
// caller updates only the selected party's actor/time/decision facts, then the
// returned status, and creates a grant only for ContactGranted, all under locks.
// An admin requester still needs the teen's independent subject approval.
func ContactRequestStatusAfterDecision(actor ContactAccount, party, decision string, r ContactRequest, f ContactAccessFacts) string {
	if !actor.Active || actor.Person == nil || actor.Person.ID == 0 || !pendingContactRequest(r, f) || (decision != ContactApproved && decision != ContactDenied) {
		return ""
	}
	return requestDecision(actor, party, decision, r.SubjectUserID, r.NeedsAdmin, r.SubjectDecision, r.AdminDecision)
}

// requestDecision is the shared two-party decision rule, not a completion
// engine. Contact callers create a grant on granted; add callers treat that
// result only as readiness and must complete the actual locked add first.
func requestDecision(actor ContactAccount, party, decision string, subjectID uint, needsAdmin bool, subject, admin string) string {
	switch party {
	case ContactSubject:
		if actor.Person.ID != subjectID || subject != ContactPending {
			return ""
		}
		subject = decision
	case ContactAdmin:
		if !needsAdmin || !actor.adult() || !actor.Person.Admin || admin != ContactPending {
			return ""
		}
		admin = decision
	default:
		return ""
	}
	if subject == ContactDenied || admin == ContactDenied {
		return ContactDenied
	}
	if subject == ContactApproved && (admin == ContactApproved || admin == ContactNotNeeded) {
		return ContactGranted
	}
	return ContactPending
}

// Withdrawal does not require retaining management rights; it never revives a
// terminal request. Automatic invalidation may have already ended it.
func CanWithdrawContactRequest(actor ContactAccount, r ContactRequest, now time.Time) bool {
	return actor.Active && actor.Person != nil && validContactScope(r.ContactScope) && actor.Person.ID == r.RequesterID && r.Status == ContactPending &&
		!now.IsZero() && !r.CreatedAt.IsZero() && !now.Before(r.CreatedAt) && r.ExpiresAt.After(now)
}

type ContactGrant struct {
	ContactScope
	RequestID  uint
	NeedsAdmin bool // loaded from the successful request, never from the client
	GrantedAt  time.Time
	EndedAt    *time.Time
}

// ContactGrantCurrent is only a lifecycle check, not disclosure authorization.
// Reveal still needs the exact requester, context and selected field check.
func ContactGrantCurrent(g ContactGrant, f ContactAccessFacts) bool {
	return g.RequestID > 0 && g.EndedAt == nil && !g.GrantedAt.IsZero() && !f.Now.Before(g.GrantedAt) &&
		contactScopeMatches(g.ContactScope, f) && ContactIneligibility(f, g.NeedsAdmin) == ""
}

// ContactGrantRevocationReason grants no create/approval right to guardians.
// Revocation may still close an unended row whose relationship has just ended.
func ContactGrantRevocationReason(actor ContactAccount, confirmedGuardian bool, g ContactGrant) string {
	if !actor.Active || actor.Person == nil || actor.Person.ID == 0 || g.EndedAt != nil || !validContactScope(g.ContactScope) || g.RequestID == 0 {
		return ""
	}
	if actor.Person.ID == g.SubjectUserID {
		return ContactRevokedBySubject
	}
	if actor.adult() && actor.Person.Admin {
		return ContactRevokedByAdmin
	}
	if actor.adult() && g.NeedsAdmin && confirmedGuardian {
		return ContactRevokedByGuardian
	}
	return ""
}

// CanRevealContactGrant is for the explicit, transactionally audited endpoint
// only. Never feed it into CanSeeContact or any roster/list presentation.
// A both-fields grant can reveal either field, never the reverse.
func CanRevealContactGrant(actor ContactAccount, requested ContactScope, g ContactGrant, f ContactAccessFacts) bool {
	if !actor.Active || actor.Person == nil || actor.Person.ID != g.RequesterID || !ContactGrantCurrent(g, f) || !validContactScope(requested) {
		return false
	}
	return requested.RequesterID == g.RequesterID && requested.SubjectUserID == g.SubjectUserID &&
		requested.ContextType == g.ContextType && requested.ContextID == g.ContextID &&
		(requested.Fields == g.Fields || g.Fields == ContactBoth)
}

// CanViewContactRequest authorizes outcome review only, never contact fields.
func CanViewContactRequest(actor ContactAccount, r ContactRequest) bool {
	return actor.Active && actor.Person != nil && (actor.Person.ID == r.RequesterID || actor.Person.ID == r.SubjectUserID || (actor.adult() && actor.Person.Admin))
}

// CanViewContactGrant gives confirmed guardians active teen grants only. An
// actual adult with a stale teen account must not retain guardian-based access.
func CanViewContactGrant(actor ContactAccount, confirmedGuardian, subjectTeen bool, g ContactGrant) bool {
	return CanViewContactRequest(actor, ContactRequest{ContactScope: g.ContactScope}) ||
		(actor.Active && actor.adult() && confirmedGuardian && subjectTeen && g.NeedsAdmin && g.EndedAt == nil)
}
