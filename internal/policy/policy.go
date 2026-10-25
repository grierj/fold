// Package policy holds every permission rule (PLAN.md section 6) as pure
// functions. Handlers load the facts (who is asking, the group, and their
// membership) and ask policy; they never inline role checks or read
// Group.LeaderID for permissions (C12).
package policy

import "time"

// Group visibility values (C3). The same enum will be used on events.
const (
	Public     = "public"      // anyone, including signed-out visitors
	Private    = "private"     // signed-in people
	InviteOnly = "invite_only" // members (and admins) only; 404 to everyone else
)

// Group statuses.
const (
	GroupPending  = "pending"
	GroupActive   = "active"
	GroupArchived = "archived"
)

// ValidVisibility reports whether v is one of the three values.
func ValidVisibility(v string) bool {
	return v == Public || v == Private || v == InviteOnly
}

// Person is what policy needs to know about someone. A nil *Person is a
// signed-out visitor.
type Person struct {
	ID    uint
	Admin bool
	// Teen is a 13-17 account (DEC-8).
	Teen bool
}

// Group is what policy needs to know about a group.
type Group struct {
	Status     string
	Visibility string
}

// Rel is a person's active membership in one group.
type Rel struct {
	Member bool // any active membership, leaders included
	Leader bool // active membership with role=leader
	// Removed: a leader or admin removed this person (DEC-35). They aren't a
	// member and can't rejoin on their own.
	Removed bool
}

// CanSeeGroup: public groups are open to all, private groups to signed-in
// people, invite-only groups to their members and admins (DEC-5). Pending
// and archived groups are limited to their leaders and admins. Handlers
// answer 404, never 403, when this is false.
func CanSeeGroup(p *Person, g Group, r Rel) bool {
	if p != nil && p.Admin {
		return true
	}
	if g.Status != GroupActive {
		return p != nil && r.Leader
	}
	switch g.Visibility {
	case Public:
		return true
	case Private:
		return p != nil
	default: // invite_only, and anything unexpected fails closed
		return p != nil && r.Member
	}
}

// CanSeeRoster: the member list is for people who share the group, and
// admins (10.2: members see each other only inside groups they share).
func CanSeeRoster(p *Person, g Group, r Rel) bool {
	return p != nil && CanSeeGroup(p, g, r) && (p.Admin || r.Member)
}

// CanJoinGroup: signed-in people join public and private groups at once;
// invite-only groups are joined only by invitation (DEC-30). Admins may
// join any group.
func CanJoinGroup(p *Person, g Group, r Rel) bool {
	if p == nil || g.Status != GroupActive || !CanSeeGroup(p, g, r) {
		return false
	}
	if r.Removed && !p.Admin {
		return false // removed people come back only when a leader adds them
	}
	return p.Admin || r.Member || g.Visibility != InviteOnly
}

// CanCreateGroup: admins, leaders and members may propose groups; teens
// may not.
func CanCreateGroup(p *Person) bool { return p != nil && !p.Teen }

// NewGroupStatus is the status a group starts in: active at once for an
// admin, pending approval for everyone else.
func NewGroupStatus(p *Person) string {
	if p != nil && p.Admin {
		return GroupActive
	}
	return GroupPending
}

// CanManageGroup covers editing the group, its roster, cover and meetings:
// admins, and the group's leaders.
func CanManageGroup(p *Person, r Rel) bool {
	return p != nil && (p.Admin || r.Leader)
}

// CanSetGroupStatus: leaders may archive their own group; only admins may
// approve or reactivate.
func CanSetGroupStatus(p *Person, r Rel, status string) bool {
	if p != nil && p.Admin {
		return true
	}
	return CanManageGroup(p, r) && status == GroupArchived
}

// CanChangeRole decides making someone a leader or a member again.
//   - Admins may change anyone; leaders only in their own groups.
//   - The target must already be an active member.
//   - Only adults lead: a teen can never be made a leader (DEC-18).
//   - A group can never lose its last leader.
//   - A leader can't demote an admin; only another admin can (DEC-35).
//
// There is no admin-approval gate for a leader promoting a co-leader
// (DEC-31, 5.11). The handler writes an audit row and admins see a notice.
// Leadership never unlocks teen contact details (see CanSeeContact).
func CanChangeRole(p *Person, r Rel, target *Person, targetRel Rel, makeLeader bool, leaders int) bool {
	if !CanManageGroup(p, r) || target == nil || !targetRel.Member {
		return false
	}
	if makeLeader {
		return !target.Teen
	}
	// A leader can't demote an admin who co-leads the group (DEC-35).
	if targetRel.Leader && target.Admin && !p.Admin {
		return false
	}
	return !targetRel.Leader || leaders > 1
}

// CanRemoveMember decides taking someone off a group's roster (DEC-35).
//   - Admins may remove anyone; leaders only in groups they lead.
//   - A leader can't remove an admin; only another admin can.
//   - Nobody removes the group's last leader (hand off first).
//   - Co-leaders can be removed like members; removing yourself is leaving
//     (CanLeaveGroup).
//
// The handler marks the membership removed (so the person can't rejoin on
// their own) and writes an audit row (DEC-34: log it, no approval layer).
func CanRemoveMember(p *Person, r Rel, target *Person, targetRel Rel, leaders int) bool {
	if !CanManageGroup(p, r) || target == nil || !targetRel.Member || p.ID == target.ID {
		return false
	}
	if target.Admin && !p.Admin {
		return false
	}
	return !targetRel.Leader || leaders > 1
}

// CanBeSuccessor: when an admin deletes the account of a group's only
// leader, the new leader is an adult active member of that group or any
// admin (DEC-36). Teens never lead (DEC-18).
func CanBeSuccessor(candidate *Person, candidateRel Rel) bool {
	return candidate != nil && !candidate.Teen && (candidate.Admin || candidateRel.Member)
}

// CanLeaveGroup: anyone may leave, except the last leader, who must hand
// the group to someone else first.
func CanLeaveGroup(r Rel, leaders int) bool {
	return r.Member && (!r.Leader || leaders > 1)
}

// CanRequestDeletion: a leader may ask an admin to delete the group when
// nobody else is in it.
func CanRequestDeletion(p *Person, r Rel, otherMembers int64) bool {
	return p != nil && r.Leader && otherMembers == 0
}

// CanCancelDeletion: the group's leaders and admins.
func CanCancelDeletion(p *Person, r Rel) bool { return CanManageGroup(p, r) }

// CanRegister: signed-in people register for events of active groups they
// can see. Guest registration comes in M1.4. Group events that are open
// wider than the group use CanRegisterEvent, which does not require seeing
// the group.
func CanRegister(p *Person, g Group, r Rel) bool {
	return p != nil && g.Status == GroupActive && CanSeeGroup(p, g, r)
}

// EventView is what policy needs to know about an event. Host is true only
// for a church-wide event this person hosts; a host row on a group event
// grants nothing (DEC-7).
//
// Visibility is the event's own value. A group event copies its group's
// visibility when it is created; afterwards only a leader or admin changes
// it, or a group becoming more private tightens it (DEC-48).
type EventView struct {
	ChurchWide bool
	Visibility string
	Host       bool
}

// openness ranks invite_only < private < public. Anything else is closed.
func openness(v string) int {
	switch v {
	case Public:
		return 2
	case Private:
		return 1
	default:
		return 0
	}
}

// MoreOpen reports whether the event's visibility lets more people in than
// the group's. A reason is optional when this is true (DEC-28).
func MoreOpen(eventVis, groupVis string) bool {
	return openness(eventVis) > openness(groupVis)
}

// Openness ranks a visibility: invite_only 0, private 1, public 2.
// Anything unexpected is 0 (closed).
func Openness(v string) int { return openness(v) }

// CanSeeEvent: admins see every event. A group that isn't active is limited
// to its leaders, even when the event is more open. Otherwise the event's
// own visibility applies (DEC-48). Seeing the event does not reveal the group.
func CanSeeEvent(p *Person, ev EventView, g Group, r Rel) bool {
	if p != nil && p.Admin {
		return true
	}
	if ev.ChurchWide {
		switch ev.Visibility {
		case Public:
			return true
		case Private:
			return p != nil
		default:
			return false
		}
	}
	if g.Status != GroupActive {
		return p != nil && r.Leader
	}
	switch ev.Visibility {
	case Public:
		return true
	case Private:
		return p != nil
	default:
		return p != nil && r.Member
	}
}

// CanManageEvent: admins, a leader of the event's group, or a host of that
// church-wide event. A host row does not grant this on a group event.
func CanManageEvent(p *Person, ev EventView, r Rel) bool {
	if p == nil {
		return false
	}
	if p.Admin {
		return true
	}
	if ev.ChurchWide {
		return ev.Host
	}
	return r.Leader
}

// CanSetEventVisibility: a leader or admin may set a group event to any of
// the three values; wider than the group marks it leader-opened (DEC-48).
// A host or admin may switch a church-wide event between public and private.
func CanSetEventVisibility(p *Person, ev EventView, r Rel, visibility string) bool {
	if p == nil {
		return false
	}
	if ev.ChurchWide {
		return (p.Admin || ev.Host) && (visibility == Public || visibility == Private)
	}
	return (p.Admin || r.Leader) && ValidVisibility(visibility)
}

// CanAssignHost: only an admin assigns a host, and never a teen (DEC-18).
// The handler also requires the account to be active.
func CanAssignHost(p, target *Person) bool {
	return p != nil && p.Admin && target != nil && !target.Teen
}

// CanSeeAttendeeNames: guests never see names (section 6). Leaders, admins,
// and a church-wide event's hosts do. When the group is invite-only and the
// event isn't, people outside the group see a count only; members of the
// group see names (DEC-48). Anyone else who can see the event
// sees names. Teen and Child badges are limited to roster managers.
func CanSeeAttendeeNames(p *Person, ev EventView, g Group, r Rel) bool {
	if p == nil || !CanSeeEvent(p, ev, g, r) {
		return false
	}
	if p.Admin || r.Leader || (ev.ChurchWide && ev.Host) {
		return true
	}
	if !ev.ChurchWide && g.Visibility == InviteOnly && ev.Visibility != InviteOnly && !r.Member {
		return false
	}
	return true
}

// CanMessageEvent: the same people who can manage the event may message its
// registrants (section 6). The composer arrives in M3; this is the rule.
func CanMessageEvent(p *Person, ev EventView, r Rel) bool {
	if p == nil {
		return false
	}
	if p.Admin {
		return true
	}
	if ev.ChurchWide {
		return ev.Host
	}
	return r.Leader
}

// CanSeeChildcare protects childcare totals and other people's requests.
// The caller has already checked event visibility.
func CanSeeChildcare(p *Person, ev EventView, r Rel) bool {
	return CanManageEvent(p, ev, r)
}

// CanRegisterEvent: a signed-in person who can see the event may register.
// That includes an outsider of an opened invite-only event. It does not add
// them to the group. Signed-out visitors use CanRegisterGuest instead.
func CanRegisterEvent(p *Person, ev EventView, g Group, r Rel) bool {
	return p != nil && CanSeeEvent(p, ev, g, r)
}

// CanRegisterGuest: a signed-out visitor may register for a public event
// with a name, a verified email, and "I am 18 or older" (section 6, M1.4).
// This is the visibility half of that rule. The handler enforces the age
// confirmation and the emailed verification link before it creates
// anything. It does not add the guest to the group.
func CanRegisterGuest(ev EventView, g Group) bool {
	return CanSeeEvent(nil, ev, g, Rel{})
}

// Video reveal settings (5.8, M1.8).
const (
	VideoRevealAlways         = "always"
	VideoRevealRegisteredOnly = "registered_only"
)

// CanSeeVideoLink decides who sees an event's video link (5.8, M1.8): with
// "always", everyone who can see the event; with "registered_only" (the
// default, and anything unexpected, failing closed), only people
// registered going for that date, plus the event's leaders or hosts and
// admins (isManager). Waitlisted, maybe and declined do not see it. The
// caller checks CanSeeEvent first and only calls this when the event has
// a link at all.
func CanSeeVideoLink(reveal string, isGoing, isManager bool) bool {
	if reveal == VideoRevealAlways {
		return true
	}
	return isGoing || isManager
}

// CanSeeVideoLinkInFeed is the personal ICS feed rule (DEC-70, Grier,
// Oct 20, 2026). It widens CanSeeVideoLink for the feed owner only:
// "always" shows the link; under "registered_only" the owner sees it when
// going or maybe on that date, when they haven't answered and are a member
// of the event's group or a host (inGroup), or when they manage the
// event. Waitlisted, declined, cancelled and removed never see it, and an
// outsider who hasn't answered doesn't either. status is "" for no answer.
func CanSeeVideoLinkInFeed(reveal, status string, inGroup, isManager bool) bool {
	if CanSeeVideoLink(reveal, status == "going", isManager) {
		return true
	}
	switch status {
	case "maybe":
		return true
	case "":
		return inGroup
	}
	return false
}

// IsAdmin gates the admin area: approvals, users and roles, settings, and
// (later) the audit log.
func IsAdmin(p *Person) bool { return p != nil && p.Admin }

// Contact is the person whose email or phone someone wants to see.
type Contact struct {
	Teen bool
	// HideFromLeaders is the adult's "Hide my contact info from group
	// leaders" setting (DEC-31). Always treated as on for teens.
	HideFromLeaders bool
	// These facts must describe the same group or event being presented,
	// never an unrelated leadership, hosting, membership or registration.
	GroupLeader     bool
	GroupMember     bool
	EventManager    bool
	EventRegistrant bool
	// ConfirmedGuardian is a current confirmed link to this teen, not
	// household membership or a pending/revoked guardian claim.
	ConfirmedGuardian bool
}

// CanSeeContact preserves self/admin/confirmed-guardian rights. Otherwise,
// only the presented group's leaders or event's managers see unhidden adult
// contact. No grant-based disclosure is available here: grants require the
// separately audited reveal lifecycle, not bulk list presentation (DEC-31).
func CanSeeContact(p *Person, self bool, c Contact) bool {
	switch {
	case p == nil:
		return false
	case p.Admin, self:
		return true
	case c.Teen && c.ConfirmedGuardian && !p.Teen:
		return true
	case p.Teen, c.Teen, c.HideFromLeaders:
		return false
	default:
		return (c.GroupLeader && c.GroupMember) || (c.EventManager && c.EventRegistrant)
	}
}

// CanEditOwnContactPrivacy: adults choose their own preference; teens are
// always hidden and cannot change that by submitting a profile preference.
func CanEditOwnContactPrivacy(p *Person) bool { return p != nil && !p.Teen }

// CanDemoteOrDelete guards account roles: only admins manage accounts,
// nobody demotes or deletes themselves, and the last admin is never demoted
// or deleted.
func CanDemoteOrDelete(p, target *Person, admins int) bool {
	return IsAdmin(p) && target != nil && p.ID != target.ID && (!target.Admin || admins > 1)
}

// GroupHasRoom reports whether one more active member fits under a group's
// member limit (DEC-39); nil means unlimited. When it is false, self-join
// is refused, a re-enabled account is left out (DEC-47), and a leader or
// admin may still add someone by explicit choice. Groups have no waitlist.
func GroupHasRoom(limit *int, count int) bool {
	return limit == nil || count < *limit
}

// CanInviteFriend reports whether p may use the member "invite a
// friend" door (5.10, DEC-4, M1.3): a signed-in adult member. It always
// creates an account request an admin decides, never an account, so
// leaders and admins who use it get no shortcut either. Teens may not
// (section 6); guests use self-signup instead.
func CanInviteFriend(p *Person) bool { return p != nil && !p.Teen }

// CanDecideAccountRequest: only admins approve or reject account
// requests (5.10, DEC-4).
func CanDecideAccountRequest(p *Person) bool { return IsAdmin(p) }

// InlineApproveAccountOK reports whether an account request may be
// approved inline from an admin's bell (DEC-44): an adult request with
// no warnings (duplicate, honeypot, or rate-limit flags). Every teen
// request and every flagged request goes to Review, and the approval
// endpoint refuses inline approval of it whatever the client sends.
func InlineApproveAccountOK(accountType string, hasWarnings bool) bool {
	return accountType == "adult" && !hasWarnings
}

// CanInvite reports whether p may create an invitation (5.10, DEC-4, M1.2).
// Admins may invite anyone, into any group or none. Leaders may invite into
// a group they lead, or with no group at all. leadsTarget is p's leadership
// in the named group; leadsAnywhere covers the no-group case. Hosts,
// members, teens and guests may not invite (a member's "invite a friend"
// creates an account request instead, M1.3).
func CanInvite(p *Person, groupNamed, leadsTarget, leadsAnywhere bool) bool {
	if p == nil || p.Teen {
		return false
	}
	if p.Admin {
		return true
	}
	if groupNamed {
		return leadsTarget
	}
	return leadsAnywhere
}

// ---------------------------------------------------------------------------
// Households (section 6, "Accounts, ages, households"; DEC-8, DEC-32)
// ---------------------------------------------------------------------------

// Household person kinds (5.3) and guardian link statuses (DEC-32).
const (
	KindChild = "child"
	KindTeen  = "teen"
	KindAdult = "adult"

	LinkPending   = "pending"
	LinkConfirmed = "confirmed"
	LinkRejected  = "rejected"
	LinkRevoked   = "revoked"
)

// HouseholdRel is a viewer's relation to one household, and (for Guardian
// and EventManager) to one household person in it. Handlers load these
// facts from HouseholdAccount and GuardianLink rows.
type HouseholdRel struct {
	// Account: the viewer has a HouseholdAccount row in this household.
	Account bool
	// Adult: that row's relation is guardian or adult (not teen).
	Adult bool
	// Guardian: the viewer has a confirmed GuardianLink to this person.
	// A pending link is not guardianship yet (DEC-32).
	Guardian bool
	// EventManager: the viewer leads or hosts an event this person is
	// registered for (registrations arrive in M1.9 slice 2).
	EventManager bool
}

// HouseholdPerson is what policy needs to know about a household person.
// LinkStatus is the person's effective guardian link status: a confirmed
// link wins, then a pending one, then the latest decided one. "" means
// no link, which is the adult case: adults need no approval (DEC-32).
type HouseholdPerson struct {
	AgeYears   *int
	Deleted    bool
	Converted  bool
	Kind       string // child | teen | adult
	LinkStatus string // pending | confirmed | rejected | revoked | ""
}

// NeedsGuardianLink reports whether adding a person of this kind waits
// for an admin to confirm a guardian link (DEC-32): children and teens
// do, adults do not. An unknown kind fails closed.
func NeedsGuardianLink(kind string) bool {
	return kind != KindAdult
}

// CanCreateHousehold: a signed-in adult gets a household on first use
// (section 6: own household only). Teens and guests may not.
func CanCreateHousehold(p *Person) bool { return p != nil && !p.Teen }

// CanManageHousehold covers naming the household and adding, editing and
// removing its people: admins over any household, and any adult account
// in the household over their own. Teens (including a teen household
// account) and guests manage none. Children and teens added here still
// wait for an admin to confirm the link (NeedsGuardianLink, DEC-32);
// managing a household never confirms a link.
func CanManageHousehold(p *Person, rel HouseholdRel) bool {
	if p == nil {
		return false
	}
	if p.Admin {
		return true
	}
	return !p.Teen && rel.Adult
}

// CanConvertHouseholdPerson requires current adult authority and birth-date
// evidence. Teens also need a confirmed household guardian link.
func CanConvertHouseholdPerson(p *Person, rel HouseholdRel, person HouseholdPerson, active bool) bool {
	if !active || p == nil || p.Teen || !CanManageHousehold(p, rel) || person.Deleted || person.Converted {
		return false
	}
	return MeetsAdultAccountRules(person) || MeetsTeenAccountRules(person)
}

// MeetsAdultAccountRules requires an adult with a birth date proving age 18+.
func MeetsAdultAccountRules(person HouseholdPerson) bool {
	return person.Kind == KindAdult && person.AgeYears != nil && *person.AgeYears >= 18
}

// MeetsTeenAccountRules requires a teen aged 13-17 and a confirmed household link.
func MeetsTeenAccountRules(person HouseholdPerson) bool {
	return person.Kind == KindTeen && person.AgeYears != nil && *person.AgeYears >= 13 && *person.AgeYears < 18 && person.LinkStatus == LinkConfirmed
}

// CanCancelHouseholdConversion remains available after eligibility changes.
func CanCancelHouseholdConversion(p *Person, rel HouseholdRel, active bool) bool {
	return active && p != nil && !p.Teen && CanManageHousehold(p, rel)
}

// CanSeePendingTeenAttendee keeps a pending teen's existing manager and
// household visibility while the account waits for consent (judgment 44).
func CanSeePendingTeenAttendee(p *Person, rel HouseholdRel, manager bool) bool {
	return p != nil && !p.Teen && (p.Admin || rel.Adult || manager)
}

// householdPersonConfirmed reports whether the person counts as
// confirmed for people outside the household: an adult (no link needed)
// or a child or teen with a confirmed guardian link. Until then leaders
// and hosts see nothing about the person (5.3, DEC-32).
func householdPersonConfirmed(person HouseholdPerson) bool {
	if person.Deleted || person.Converted {
		return false
	}
	if person.Kind == KindAdult {
		return person.AgeYears != nil && *person.AgeYears >= 18
	}
	return person.LinkStatus == LinkConfirmed
}

// CanSeeHouseholdPerson decides who sees a household person's name and
// age (section 6): admins; a confirmed guardian; an adult account in the
// same household (the account holders, including the adult who added a
// child and is still waiting for an admin, who sees the waiting state in
// their own household list); and the leaders or hosts of an event the
// person is registered for, once any needed link is confirmed. Teens
// and guests see household people nowhere.
func CanSeeHouseholdPerson(p *Person, person HouseholdPerson, rel HouseholdRel) bool {
	if p == nil {
		return false
	}
	if p.Admin {
		return true
	}
	if p.Teen {
		return false
	}
	if rel.Guardian || rel.Adult {
		return true
	}
	return rel.EventManager && householdPersonConfirmed(person)
}

// EventWindow is one occurrence's start and end, for CanSeeCareNotes.
type EventWindow struct {
	StartsAt time.Time
	EndsAt   time.Time
}

// inCareWindow reports whether now is in the care-notes window: from
// 24 hours before the occurrence starts to 24 hours after it ends
// (section 6). The bounds are inclusive.
func (w EventWindow) inCareWindow(now time.Time) bool {
	end := w.EndsAt
	if end.Before(w.StartsAt) {
		end = w.StartsAt
	}
	return !now.Before(w.StartsAt.Add(-24*time.Hour)) && !now.After(end.Add(24*time.Hour))
}

// CanSeeCareNotes decides who sees a household child's care notes
// (section 6): admins and confirmed guardians always, and the leaders
// or hosts (rel.EventManager) of an event the child is registered for
// only during the event window and only once the guardian link is
// confirmed. An adult account in the household who is not a confirmed
// guardian of this child does not see the notes through this rule; the
// household's own list goes through CanManageHousehold instead. Care
// notes belong to children, so the event-manager path is child-only.
func CanSeeCareNotes(p *Person, person HouseholdPerson, rel HouseholdRel, window *EventWindow, now time.Time) bool {
	if p == nil {
		return false
	}
	if p.Admin {
		return true
	}
	if p.Teen {
		return false
	}
	if rel.Guardian {
		return true
	}
	return person.Kind == KindChild && person.LinkStatus == LinkConfirmed &&
		rel.EventManager && window != nil && window.inCareWindow(now)
}

// CanConfirmGuardianLink: only an admin confirms a guardian or household
// link to a teen or child, with a note on how it was checked (DEC-32).
// There is no auto-confirm path, and a leader role never grants this.
func CanConfirmGuardianLink(p *Person) bool { return IsAdmin(p) }

func CanRequestGuardianLink(p *Person) bool { return p != nil && !p.Teen }

// CanChangeTeenConsent never gives a leader consent powers. A confirmed
// guardian may act regardless of any group role. Only adults grant consent.
func CanChangeTeenConsent(p *Person, confirmedGuardian bool) bool {
	return p != nil && !p.Teen && (p.Admin || confirmedGuardian)
}

// HouseholdRegistrationProblem validates saved age and person-link facts.
func HouseholdRegistrationProblem(person HouseholdPerson) string {
	if person.Deleted || person.Converted {
		return "household_registration_holder_unavailable"
	}
	if person.AgeYears == nil {
		return "household_birth_date_required"
	}
	if *person.AgeYears < 0 || (person.Kind == KindAdult && *person.AgeYears < 18) {
		return "household_age_mismatch"
	}
	if person.Kind == KindAdult {
		return ""
	}
	if person.Kind != KindChild && person.Kind != KindTeen {
		return "household_age_mismatch"
	}
	// PLAN 2.x bands: child under 13, teen 13 to 17 (Warden, PR 110 N1).
	// Nobody 18 or older is a child or teen, and a teen can't be under 13.
	// A child who turned 13 stays registrable until the guardian updates
	// the kind; the accounts.age_rollover job flags them (judgment call 39).
	if *person.AgeYears >= 18 || (person.Kind == KindTeen && *person.AgeYears < 13) {
		return "household_age_mismatch"
	}
	if person.LinkStatus == LinkPending {
		return "household_link_pending"
	}
	if person.LinkStatus != LinkConfirmed {
		return "household_link_unconfirmed"
	}
	return ""
}

// CanRegisterHouseholdPerson grants self-service only through an adult
// household account, with both actor and responsible holder event access.
func CanRegisterHouseholdPerson(actor, holder *Person, actorRel, rel HouseholdRel, person HouseholdPerson, holderActive, actorAccess, holderAccess bool) string {
	if actor == nil || actor.Teen || holder == nil || holder.Teen || !holderActive || !rel.Adult || !rel.Account {
		return "household_registration_holder_unavailable"
	}
	if !actorRel.Account || !actorRel.Adult || !actorAccess || !holderAccess {
		return "household_registration_holder_unavailable"
	}
	return HouseholdRegistrationProblem(person)
}

// CanManageHouseholdRegistration never lets management bypass the holder's
// authority, event visibility, age or confirmation.
func CanManageHouseholdRegistration(actor, holder *Person, managesEvent, previouslyRegistered bool, rel HouseholdRel, person HouseholdPerson, holderActive, holderAccess bool) string {
	if actor == nil || actor.Teen || (!actor.Admin && (!managesEvent || !previouslyRegistered)) {
		return "household_registration_holder_unavailable"
	}
	return CanRegisterHouseholdPerson(holder, holder, rel, rel, person, holderActive, holderAccess, holderAccess)
}

// CanSeeMinorRosterIdentity limits minor roster names and badges to managers.
func CanSeeMinorRosterIdentity(p *Person, manager bool) bool {
	return p != nil && (p.Admin || manager)
}

// CanUploadAvatar: teens have no public avatar (DEC-8).
func CanUploadAvatar(p *Person) bool { return p != nil && !p.Teen }

// EffectiveVisibility resolves only the date's ordinary visibility. It never
// grants series, group, roster or explicit-invitation authority.
func EffectiveVisibility(series string, override *string) string {
	if override != nil {
		return *override
	}
	return series
}
