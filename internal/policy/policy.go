// Package policy holds every permission rule (PLAN.md section 6) as pure
// functions. Handlers load the facts (who is asking, the group, and their
// membership) and ask policy; they never inline role checks or read
// Group.LeaderID for permissions (C12).
package policy

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
	// Teen is a 13-17 account (DEC-8). Teen accounts arrive in M1.10; until
	// then nobody is a teen, but the rules below already treat them right.
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

// Where an event's visibility comes from.
const (
	SourceGroup  = "group"
	SourceCustom = "custom"
)

// EventView is what policy needs to know about an event. Host is true only
// for a church-wide event this person hosts; a host row on a group event
// grants nothing (DEC-7).
type EventView struct {
	ChurchWide bool
	Visibility string
	Source     string // group | custom
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

// effectiveVisibility is the visibility that decides who can see the event.
// Source group follows the group even when the stored value is stale.
func effectiveVisibility(ev EventView, g Group) string {
	if !ev.ChurchWide && ev.Source != SourceCustom {
		return g.Visibility
	}
	return ev.Visibility
}

// CanSeeEvent: admins see every event. A group that isn't active is limited
// to its leaders, even when the event is more open. Otherwise the event's
// effective visibility applies. Seeing the event does not reveal the group.
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
	switch effectiveVisibility(ev, g) {
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
// the three values. Source group must match the group's visibility. A host
// or admin may switch a church-wide event between public and private, and
// its source stays custom.
func CanSetEventVisibility(p *Person, ev EventView, g Group, r Rel, visibility, source string) bool {
	if p == nil {
		return false
	}
	if ev.ChurchWide {
		if !(p.Admin || ev.Host) {
			return false
		}
		return source == SourceCustom && (visibility == Public || visibility == Private)
	}
	if !(p.Admin || r.Leader) {
		return false
	}
	if source == SourceGroup {
		return visibility == g.Visibility && ValidVisibility(visibility)
	}
	return source == SourceCustom && ValidVisibility(visibility)
}

// CanAssignHost: only an admin assigns a host, and never a teen (DEC-18).
// The handler also requires the account to be active.
func CanAssignHost(p, target *Person) bool {
	return p != nil && p.Admin && target != nil && !target.Teen
}

// CanSeeAttendeeNames: guests never see names (section 6). Leaders, admins,
// and a church-wide event's hosts do. On an invite-only group's event that
// was opened to private or public, people outside the group see a count
// only; members of the group see names. Anyone else who can see the event
// sees names. Teen and Child badges arrive in M1.10.
func CanSeeAttendeeNames(p *Person, ev EventView, g Group, r Rel) bool {
	if p == nil || !CanSeeEvent(p, ev, g, r) {
		return false
	}
	if p.Admin || r.Leader || (ev.ChurchWide && ev.Host) {
		return true
	}
	if !ev.ChurchWide && g.Visibility == InviteOnly && ev.Source == SourceCustom &&
		ev.Visibility != InviteOnly && !r.Member {
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

// CanRegisterEvent: a signed-in person who can see the event may register.
// That includes an outsider of an opened invite-only event. It does not add
// them to the group. Guest registration comes in M1.4.
func CanRegisterEvent(p *Person, ev EventView, g Group, r Rel) bool {
	return p != nil && CanSeeEvent(p, ev, g, r)
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
	// Shares is true when the viewer leads a group the contact belongs to.
	Shares bool
	// Grant is true when an active ContactGrant covers the viewer (M1.11).
	Grant bool
}

// CanSeeContact: admins always; the person themselves; leaders of a shared
// group for adults who don't hide their details. A teen's details are never
// shown to a leader, co-leader or host, whoever promoted them, unless both
// an admin and the teen approved a grant (DEC-31, 5.11). Guardians come in
// M1.10. Nothing exposes contact details to leaders yet; this rule is
// written now so later milestones only call it.
func CanSeeContact(p *Person, self bool, c Contact) bool {
	switch {
	case p == nil:
		return false
	case p.Admin, self:
		return true
	case !c.Shares:
		return false
	case c.Teen, c.HideFromLeaders:
		return c.Grant
	default:
		return true
	}
}

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
