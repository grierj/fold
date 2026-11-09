package main

import (
	"fold/internal/audit"
	"fold/internal/notify"
	"fold/internal/policy"
	"fold/models"
	"gorm.io/gorm"
)

// requestKind is everything the code needs to know about a stored request
// because of its purpose. A contact request and the two add requests (group,
// date) share one table, one lifecycle and one set of handlers; what differs
// is named here and nowhere else: the audit target and actions, the notice
// subject, link, dedupe prefix and kinds, the review task kinds, and the
// reason-review actions.
type requestKind struct {
	// target is the audit target type and the notice subject type.
	target string
	// route is the page a notice links to ("/<route>/<id>"); dedupe starts
	// every notice dedupe key.
	route, dedupe string
	// The notice kinds: the two review tasks and the "updated" notice.
	subjectReview, adminReview, updated string
	// action maps the lifecycle action the code names (always the contact
	// spelling) to this kind's own audit action.
	action func(contactAction string) string
	// purposes are the stored purposes of this kind.
	purposes []string
	// canView is the rule for who may see a request in a list.
	canView func(actor policy.ContactAccount, r models.ContactRequest) bool
	// reasonKept and reasonRemoved are the reason-review decision actions.
	reasonKept, reasonRemoved string
}

// noticeRole is which of a request's notices a call means, whatever the kind.
type noticeRole int

const (
	roleSubjectReview noticeRole = iota
	roleAdminReview
	roleUpdated
)

// reviewRole is the review task for the deciding party.
func reviewRole(party string) noticeRole {
	if party == policy.ContactAdmin {
		return roleAdminReview
	}
	return roleSubjectReview
}

// notice is this kind's notify kind for role.
func (k *requestKind) notice(role noticeRole) string {
	switch role {
	case roleSubjectReview:
		return k.subjectReview
	case roleAdminReview:
		return k.adminReview
	default:
		return k.updated
	}
}

var contactKind = &requestKind{
	purposes: []string{policy.RequestContact},
	canView: func(actor policy.ContactAccount, r models.ContactRequest) bool {
		return policy.CanViewContactRequest(actor, contactRequestPolicy(r))
	},
	target: "contact_request", route: "contact-requests", dedupe: "contact",
	subjectReview: notify.ContactSubjectReview, adminReview: notify.ContactAdminReview, updated: notify.ContactUpdated,
	action:     func(a string) string { return a },
	reasonKept: audit.ContactReasonKept, reasonRemoved: audit.ContactReasonRemoved,
}

var addKind = &requestKind{
	purposes: addRequestPurposes,
	canView: func(actor policy.ContactAccount, r models.ContactRequest) bool {
		return policy.CanViewAddRequest(actor, addRequestPolicy(r))
	},
	target: "add_request", route: "add-requests", dedupe: "add",
	subjectReview: notify.AddSubjectReview, adminReview: notify.AddAdminReview, updated: notify.AddUpdated,
	action:     addAuditAction,
	reasonKept: audit.AddReasonKept, reasonRemoved: audit.AddReasonRemoved,
}

// requestKindOf is the kind of a stored purpose, or nil for one that is not a
// request purpose.
func requestKindOf(purpose string) *requestKind {
	switch purpose {
	case policy.RequestContact:
		return contactKind
	case policy.RequestGroupAdd, policy.RequestOccurrenceAdd:
		return addKind
	}
	return nil
}

// addRequestPurposes are the purposes the add kind covers.
var addRequestPurposes = []string{policy.RequestGroupAdd, policy.RequestOccurrenceAdd}

// addAuditAction maps a lifecycle action, spelled as the contact action, to
// the add action. Anything without an add spelling is a refusal.
func addAuditAction(action string) string {
	switch action {
	case audit.ContactRequested:
		return audit.AddRequested
	case audit.ContactDecision:
		return audit.AddDecision
	case audit.ContactWithdrawn:
		return audit.AddWithdrawn
	case audit.ContactExpired:
		return audit.AddExpired
	case audit.ContactGranted:
		return audit.AddCompleted
	case audit.ContactEnded:
		return audit.AddEnded
	default:
		return audit.AddRefused
	}
}

// refuse audits and records a refused operation on r in this kind's own words.
func (k *requestKind) refuse(tx *gorm.DB, op *contactOperation, r models.ContactRequest, operation, ip string) error {
	if k == addKind {
		return refuseAdd(tx, op, r, operation, ip)
	}
	return refuseContact(tx, op, r, operation, ip)
}
