package policy

// Groups and the people in them. Carried from Fold (internal/policy/policy.go
// at the pinned commit in docs/CARRYOVER.md) with its table tests. Changes
// from Fold, each from docs/DECISIONS.md:
//   - The middle visibility is "church", shown as "Church only" (R28).
//   - A group can be deleted by an admin; a deleted group is seen by nobody
//     (R29). Fold's leader requests for deletion are gone.
//   - Leaders find people by name and add them at once (P6); there is no
//     request or contact workflow (P5).
//   - Disabling replaces Fold's delete-or-demote guard; erasure is slice 4.

// Visibility values, shared by groups and events.
const (
	Public     = "public"      // anyone, signed out too
	Church     = "church"      // signed-in members of the church
	InviteOnly = "invite_only" // members (and admins) only; missing to everyone else
)

const (
	GroupPending  = "pending"
	GroupActive   = "active"
	GroupArchived = "archived"
	GroupDeleted  = "deleted"
)

func ValidVisibility(v string) bool { return v == Public || v == Church || v == InviteOnly }

// openness orders the visibilities, most closed first.
func openness(v string) int {
	switch v {
	case Public:
		return 2
	case Church:
		return 1
	}
	return 0
}

// MorePrivate reports whether moving from one visibility to another lets
// fewer people see the group. It removes nobody (R3); the page says so.
func MorePrivate(from, to string) bool { return openness(to) < openness(from) }

// Person is what policy needs to know about someone. A nil *Person is a
// signed-out visitor.
type Person struct {
	ID    int64
	Admin bool
	// Teen is a 13 to 17 account (DEC-8). None exist before slice 5.
	Teen bool
	// Inactive is an account that is not active (disabled or waiting). No
	// rule hands such an account a role.
	Inactive bool
}

// IsAdmin gates the admin pages: approvals, people, deleting groups.
func IsAdmin(p *Person) bool { return p != nil && p.Admin && !p.Inactive }

type Group struct {
	Status     string
	Visibility string
}

// Rel is a person's membership in one group. Read it with groups.RelOf,
// which sees removed rows too; never build it from active_memberships,
// which drops them.
type Rel struct {
	Member bool // an active membership, leaders included
	Leader bool // an active membership with the leader role
	// Removed: a leader or admin removed this person (DEC-35). They are not a
	// member and cannot rejoin on their own. Leaving sets neither (DEC-86).
	Removed bool
}

// CanSeeGroup: public groups are open to all, church-only groups to
// signed-in people, invite-only groups to their members and admins (DEC-5).
// Waiting and archived groups are limited to their leaders and admins, and a
// deleted group to nobody. Every page answers a false here exactly as it
// answers something missing.
func CanSeeGroup(p *Person, g Group, r Rel) bool {
	if g.Status == GroupDeleted {
		return false
	}
	if IsAdmin(p) {
		return true
	}
	if g.Status != GroupActive {
		return p != nil && r.Leader
	}
	return visibleTo(g.Visibility, p, r.Member)
}

// visibleTo is the one visibility rule for something whose status allows
// being seen. Anything but public or church fails closed to members only.
func visibleTo(visibility string, p *Person, member bool) bool {
	switch visibility {
	case Public:
		return true
	case Church:
		return p != nil
	default:
		return p != nil && member
	}
}

// CanSeeRoster: the member list is for people who share the group, and
// admins.
func CanSeeRoster(p *Person, g Group, r Rel) bool {
	return p != nil && CanSeeGroup(p, g, r) && (IsAdmin(p) || r.Member)
}

// CanJoinGroup: signed-in people join public and church-only groups at
// once; invite-only groups are joined only by being added (DEC-30). Admins
// may join any active group.
func CanJoinGroup(p *Person, g Group, r Rel) bool {
	if p == nil || g.Status != GroupActive || !CanSeeGroup(p, g, r) {
		return false
	}
	if r.Removed && !IsAdmin(p) {
		return false // removed people come back only when a leader adds them
	}
	return IsAdmin(p) || r.Member || g.Visibility != InviteOnly
}

// CanCreateGroup: admins and members may propose groups; teens may not.
func CanCreateGroup(p *Person) bool { return p != nil && !p.Inactive && !p.Teen }

// NewGroupStatus is the status a group starts in: active at once for an
// admin, waiting for approval for everyone else (DEC-96).
func NewGroupStatus(p *Person) string {
	if IsAdmin(p) {
		return GroupActive
	}
	return GroupPending
}

// CanManageGroup covers editing the group and its roster: admins, and the
// group's leaders. A deleted group is managed by nobody.
func CanManageGroup(p *Person, g Group, r Rel) bool {
	return p != nil && g.Status != GroupDeleted && (IsAdmin(p) || r.Leader)
}

// GroupStatusDecision is what setting a group's status from one value to
// another amounts to: a named move, no move at all, or a refusal.
type GroupStatusDecision int

const (
	GroupStatusForbidden GroupStatusDecision = iota // this person may not make the move
	GroupStatusKept                                 // the status it already has: not a move
	GroupStatusApprove                              // pending to active
	GroupStatusReject                               // pending to archived
	GroupStatusArchive                              // active to archived
	GroupStatusUnarchive                            // archived to active
	GroupStatusNoReturn                             // back to pending, which nothing asks an admin to decide again
)

// DecideGroupStatus judges setting a group's status from one value to
// another. Leaders may archive their own group; admins may also approve,
// reject and reactivate. Nobody moves a group back to pending (DEC-160).
// Deleting is its own command (CanDeleteGroup), never a status edit.
func DecideGroupStatus(p *Person, r Rel, from, to string) GroupStatusDecision {
	if !CanManageGroup(p, Group{Status: from}, r) || to == GroupDeleted {
		return GroupStatusForbidden
	}
	if from == to {
		return GroupStatusKept
	}
	// Only an admin decides a waiting group (DEC-96), and only an admin
	// reactivates; a leader may archive their own active group.
	if !IsAdmin(p) && (to != GroupArchived || from == GroupPending) {
		return GroupStatusForbidden
	}
	switch {
	case from == GroupPending && to == GroupActive:
		return GroupStatusApprove
	case from == GroupPending && to == GroupArchived:
		return GroupStatusReject
	case from == GroupActive && to == GroupArchived:
		return GroupStatusArchive
	case from == GroupArchived && to == GroupActive:
		return GroupStatusUnarchive
	case to == GroupPending:
		return GroupStatusNoReturn
	}
	return GroupStatusForbidden
}

// GroupReasons are what an admin may give for a group decision (R31).
// "other" carries a note.
var GroupReasons = map[string]string{
	"similar": "A similar group already exists.",
	"detail":  "It needs a little more detail.",
	"office":  "Please talk with the church office first.",
	"other":   "",
}

// CanDeleteGroup: only an admin deletes a group, and its members are told
// (R29, DEC-177).
func CanDeleteGroup(p *Person, g Group) bool { return IsAdmin(p) && g.Status != GroupDeleted }

// CanChangeRole decides making someone a leader or a member again.
//   - Admins may change anyone; leaders only in their own groups.
//   - The target must already be an active member.
//   - Only adults lead: a teen can never be made a leader (DEC-18).
//   - A group can never lose its last leader.
//   - A leader can't demote an admin; only another admin can (DEC-35).
func CanChangeRole(p *Person, g Group, r Rel, target *Person, targetRel Rel, makeLeader bool, leaders int) bool {
	if !CanManageGroup(p, g, r) || target == nil || target.Inactive || !targetRel.Member {
		return false
	}
	if makeLeader {
		return !target.Teen
	}
	if targetRel.Leader && target.Admin && !IsAdmin(p) {
		return false
	}
	return !targetRel.Leader || leaders > 1
}

// CanRemoveMember decides taking someone off a group's roster (DEC-35).
//   - Admins may remove anyone; leaders only in groups they lead.
//   - A leader can't remove an admin; only another admin can.
//   - Nobody removes the group's last leader (hand it on first).
//   - Removing yourself is leaving (CanLeaveGroup).
func CanRemoveMember(p *Person, g Group, r Rel, target *Person, targetRel Rel, leaders int) bool {
	if !CanManageGroup(p, g, r) || target == nil || !targetRel.Member || p.ID == target.ID {
		return false
	}
	if target.Admin && !IsAdmin(p) {
		return false
	}
	return !targetRel.Leader || leaders > 1
}

// CanBeSuccessor: when an admin disables the only leader of a group, the
// new leader is an adult active member of that group or any admin (DEC-36).
func CanBeSuccessor(candidate *Person, candidateRel Rel) bool {
	return candidate != nil && !candidate.Inactive && !candidate.Teen && (candidate.Admin || candidateRel.Member)
}

// CanLeaveGroup: anyone may leave, except the last leader, who hands the
// group to someone else first.
func CanLeaveGroup(r Rel, leaders int) bool {
	return r.Member && (!r.Leader || leaders > 1)
}

// CanAddToGroup: a manager adds a person they found by name (P6). Leading a
// group that is waiting or archived grants no power to add anyone; admins
// may (DEC-88).
func CanAddToGroup(p *Person, g Group, r Rel) bool {
	return CanManageGroup(p, g, r) && (g.Status == GroupActive || IsAdmin(p))
}

// LeadsApprovedGroup reports whether any of the groups p actively leads is
// active. Powers that reach past one group, such as inviting a person or
// searching people by name, count only an approved group.
func LeadsApprovedGroup(statuses []string) bool {
	for _, s := range statuses {
		if s == GroupActive {
			return true
		}
	}
	return false
}

// CanInvite reports whether p may invite someone by email, skipping review
// (DEC-4). Admins may invite anyone, into any active group or none. Leaders
// may invite into an active group they lead, or with no group if they lead
// any active group. target is nil for an invitation into no group; an
// invitation names only an active group, since nothing else takes members.
func CanInvite(p *Person, target *Group, leadsTarget, leadsAnywhere bool) bool {
	if p == nil || p.Inactive || p.Teen {
		return false
	}
	if target != nil {
		return target.Status == GroupActive && (IsAdmin(p) || leadsTarget)
	}
	return IsAdmin(p) || leadsAnywhere
}

// CanSearchPeople: leaders of an active group and admins find people by
// name to add them (DEC-78, R28). Results show a name, never contact
// details.
func CanSearchPeople(p *Person, leadsActiveGroup bool) bool {
	return p != nil && !p.Inactive && !p.Teen && (IsAdmin(p) || leadsActiveGroup)
}

// CanSeeEmail: admins see everyone's address (DEC-5). A leader of a group
// the person belongs to sees an adult's address unless that adult hid it
// (P5). Nobody else does, and a teen's address is never shown to a leader.
func CanSeeEmail(viewer *Person, leadsTheirGroup bool, target *Person, hidden bool) bool {
	if viewer == nil || target == nil {
		return false
	}
	if IsAdmin(viewer) || viewer.ID == target.ID {
		return true
	}
	return leadsTheirGroup && !target.Teen && !hidden
}

// CanDecideAccount: an admin approves or declines an account request.
func CanDecideAccount(p *Person) bool { return IsAdmin(p) }

// CanDisable guards disabling an account: only admins, never themselves,
// never the last active admin, and never while a group the person alone
// leads has no successor chosen (DEC-36). leaderless counts those groups.
func CanDisable(p, target *Person, activeAdmins, leaderless int) bool {
	return IsAdmin(p) && target != nil && p.ID != target.ID && (!target.Admin || activeAdmins > 1) && leaderless == 0
}
