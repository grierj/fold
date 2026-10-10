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
	return !targetRel.Leader || leaders > 1
}

// CanRemoveMember: leaders and admins prune their roster; nobody removes
// the last leader.
func CanRemoveMember(p *Person, r Rel, targetRel Rel, leaders int) bool {
	return CanManageGroup(p, r) && (!targetRel.Leader || leaders > 1)
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

// CanRSVP: signed-in people respond to meetings of active groups they can
// see. Guest registration comes in M1.4.
func CanRSVP(p *Person, g Group, r Rel) bool {
	return p != nil && g.Status == GroupActive && CanSeeGroup(p, g, r)
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
