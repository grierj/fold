package policy

import "testing"

// People used across the tables. A nil *Person is a signed-out guest.
var (
	admin  = &Person{ID: 1, Admin: true}
	adult  = &Person{ID: 2}
	adult2 = &Person{ID: 3}
	teen   = &Person{ID: 4, Teen: true}
	guest  *Person

	none     = Rel{}
	member   = Rel{Member: true}
	leader   = Rel{Member: true, Leader: true}
	active   = func(v string) Group { return Group{Status: GroupActive, Visibility: v} }
	pending  = Group{Status: GroupPending, Visibility: Public}
	archived = Group{Status: GroupArchived, Visibility: Public}
)

// Section 6, "See public / private / invite-only groups" and "See pending
// or archived groups".
func TestCanSeeGroup(t *testing.T) {
	cases := []struct {
		name string
		p    *Person
		g    Group
		r    Rel
		want bool
	}{
		{"guest sees public", guest, active(Public), none, true},
		{"guest can't see private", guest, active(Private), none, false},
		{"guest can't see invite-only", guest, active(InviteOnly), none, false},
		{"member sees public", adult, active(Public), none, true},
		{"member sees private", adult, active(Private), none, true},
		{"teen sees private", teen, active(Private), none, true},
		{"outsider can't see invite-only", adult, active(InviteOnly), none, false},
		{"member of invite-only sees it", adult, active(InviteOnly), member, true},
		{"teen member of invite-only sees it", teen, active(InviteOnly), member, true},
		{"leader sees own invite-only", adult, active(InviteOnly), leader, true},
		{"admin sees invite-only (DEC-5)", admin, active(InviteOnly), none, true},
		{"admin sees pending", admin, pending, none, true},
		{"leader sees own pending", adult, pending, leader, true},
		{"member can't see pending", adult, pending, member, false},
		{"guest can't see pending", guest, pending, none, false},
		{"leader sees own archived", adult, archived, leader, true},
		{"member can't see archived", adult, archived, member, false},
		{"unknown visibility fails closed", adult, active("secret"), none, false},
	}
	for _, c := range cases {
		if got := CanSeeGroup(c.p, c.g, c.r); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// Section 6, "Join a group" (DEC-30: join at once; invite-only by invitation).
func TestCanJoinGroup(t *testing.T) {
	cases := []struct {
		name string
		p    *Person
		g    Group
		r    Rel
		want bool
	}{
		{"member joins public", adult, active(Public), none, true},
		{"member joins private", adult, active(Private), none, true},
		{"teen joins like a member", teen, active(Private), none, true},
		{"member can't join invite-only", adult, active(InviteOnly), none, false},
		{"existing member re-joins invite-only", adult, active(InviteOnly), member, true},
		{"admin joins invite-only", admin, active(InviteOnly), none, true},
		{"guest can't join (account request comes in M1.3)", guest, active(Public), none, false},
		{"nobody joins a pending group", adult, pending, none, false},
		{"nobody joins an archived group", admin, archived, none, false},
	}
	for _, c := range cases {
		if got := CanJoinGroup(c.p, c.g, c.r); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// Section 6, "Create a group" and "Approve or reject groups".
func TestCreateAndApproveGroups(t *testing.T) {
	if !CanCreateGroup(admin) || !CanCreateGroup(adult) || CanCreateGroup(teen) || CanCreateGroup(guest) {
		t.Error("create: admins and adults yes; teens and guests no")
	}
	if NewGroupStatus(admin) != GroupActive || NewGroupStatus(adult) != GroupPending {
		t.Error("admin groups start active, others pending")
	}
	if !IsAdmin(admin) || IsAdmin(adult) || IsAdmin(guest) {
		t.Error("only admins approve groups and manage users")
	}
}

// Section 6, "Edit a group, manage its roster" and "Create or edit events
// in a group" (meetings today).
func TestCanManageGroup(t *testing.T) {
	cases := []struct {
		name string
		p    *Person
		r    Rel
		want bool
	}{
		{"admin manages any group", admin, none, true},
		{"leader manages own group", adult, leader, true},
		{"co-leader manages too (several leaders, C12)", adult2, leader, true},
		{"member can't manage", adult, member, false},
		{"outsider can't manage", adult, none, false},
		{"guest can't manage", guest, none, false},
	}
	for _, c := range cases {
		if got := CanManageGroup(c.p, c.r); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	if !CanSetGroupStatus(adult, leader, GroupArchived) || CanSetGroupStatus(adult, leader, GroupActive) {
		t.Error("leaders archive but don't activate")
	}
	if !CanSetGroupStatus(admin, none, GroupActive) {
		t.Error("admins activate")
	}
}

// Section 6, "Make someone a group leader", plus the guard rails: a teen is
// never a leader (DEC-18) and a group never loses its last leader.
func TestCanChangeRole(t *testing.T) {
	cases := []struct {
		name       string
		p          *Person
		r          Rel
		target     *Person
		targetRel  Rel
		makeLeader bool
		leaders    int
		want       bool
	}{
		{"leader promotes an adult member, no admin gate (DEC-31)", adult, leader, adult2, member, true, 1, true},
		{"admin promotes an adult member", admin, none, adult2, member, true, 1, true},
		{"leader can't promote a teen (DEC-18)", adult, leader, teen, member, true, 1, false},
		{"admin can't promote a teen (DEC-18)", admin, none, teen, member, true, 1, false},
		{"member can't promote", adult, member, adult2, member, true, 1, false},
		{"outsider can't promote", adult, none, adult2, member, true, 1, false},
		{"can't promote a non-member", adult, leader, adult2, none, true, 1, false},
		{"leader demotes a co-leader", adult, leader, adult2, leader, false, 2, true},
		{"can't demote the last leader", admin, none, adult2, leader, false, 1, false},
		{"leader can step down when another leads", adult, leader, adult, leader, false, 2, true},
		{"demoting a plain member is a no-op that's allowed", adult, leader, adult2, member, false, 1, true},
		{"leader can't demote an admin co-leader (DEC-35)", adult, leader, admin, leader, false, 2, false},
		{"admin demotes another admin co-leader", admin, none, &Person{ID: 9, Admin: true}, leader, false, 2, true},
		{"leader may promote an admin member", adult, leader, admin, member, true, 1, true},
	}
	for _, c := range cases {
		if got := CanChangeRole(c.p, c.r, c.target, c.targetRel, c.makeLeader, c.leaders); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// Section 6, "Remove a member from a group" (DEC-35).
func TestCanRemoveMember(t *testing.T) {
	cases := []struct {
		name      string
		p         *Person
		r         Rel
		target    *Person
		targetRel Rel
		leaders   int
		want      bool
	}{
		{"leader removes a member", adult, leader, adult2, member, 1, true},
		{"leader removes a teen member", adult, leader, teen, member, 1, true},
		{"leader removes a co-leader", adult, leader, adult2, leader, 2, true},
		{"leader can't remove an admin member", adult, leader, admin, member, 1, false},
		{"leader can't remove an admin co-leader", adult, leader, admin, leader, 2, false},
		{"admin removes anyone", admin, none, adult2, leader, 2, true},
		{"admin removes another admin", admin, none, &Person{ID: 9, Admin: true}, member, 1, true},
		{"nobody removes the last leader", admin, none, adult2, leader, 1, false},
		{"removing yourself is leaving, not removal", adult, leader, adult, leader, 2, false},
		{"members don't remove anyone", adult, member, adult2, member, 1, false},
		{"leaders of other groups don't", adult, none, adult2, member, 1, false},
		{"can't remove a non-member", adult, leader, adult2, none, 1, false},
		{"guests can't", guest, none, adult2, member, 1, false},
	}
	for _, c := range cases {
		if got := CanRemoveMember(c.p, c.r, c.target, c.targetRel, c.leaders); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// DEC-36: a sole leader's successor is an adult member of the group or any admin.
func TestCanBeSuccessor(t *testing.T) {
	if !CanBeSuccessor(adult, member) || !CanBeSuccessor(adult2, leader) || !CanBeSuccessor(admin, none) {
		t.Error("adult members and admins can take over")
	}
	if CanBeSuccessor(adult, none) || CanBeSuccessor(teen, member) || CanBeSuccessor(guest, none) {
		t.Error("outsiders, teens and nobody can't")
	}
}

func TestLeavingAndDeletion(t *testing.T) {
	removed := Rel{Removed: true}
	if CanJoinGroup(adult, active(Public), removed) || !CanJoinGroup(admin, active(Public), removed) {
		t.Error("removed people don't rejoin on their own (admins may)")
	}
	if !CanLeaveGroup(member, 1) || !CanLeaveGroup(leader, 2) || CanLeaveGroup(leader, 1) {
		t.Error("anyone leaves except the last leader")
	}
	if !CanRequestDeletion(adult, leader, 0) || CanRequestDeletion(adult, leader, 1) || CanRequestDeletion(adult, member, 0) {
		t.Error("a lone leader may ask for deletion")
	}
	if !CanCancelDeletion(admin, none) || !CanCancelDeletion(adult, leader) || CanCancelDeletion(adult, member) {
		t.Error("leaders and admins cancel deletion requests")
	}
}

// 10.2: members see each other only inside groups they share.
func TestCanSeeRoster(t *testing.T) {
	if !CanSeeRoster(adult, active(Public), member) || !CanSeeRoster(admin, active(InviteOnly), none) {
		t.Error("members and admins see the roster")
	}
	if CanSeeRoster(adult, active(Public), none) || CanSeeRoster(guest, active(Public), none) {
		t.Error("outsiders and guests don't see the roster")
	}
	if CanSeeRoster(adult, pending, member) || !CanSeeRoster(adult, pending, leader) {
		t.Error("a pending group's roster is for its leaders")
	}
}

// Section 6, "Register for a public / private / invite-only event" (RSVPs
// on meetings today; guests come in M1.4).
func TestCanRSVP(t *testing.T) {
	cases := []struct {
		name string
		p    *Person
		g    Group
		r    Rel
		want bool
	}{
		{"member, public", adult, active(Public), none, true},
		{"member, private", adult, active(Private), none, true},
		{"teen, private (DEC-20)", teen, active(Private), none, true},
		{"group member, invite-only", adult, active(InviteOnly), member, true},
		{"outsider, invite-only", adult, active(InviteOnly), none, false},
		{"admin, invite-only", admin, active(InviteOnly), none, true},
		{"guest, public (not until M1.4)", guest, active(Public), none, false},
		{"pending group", admin, pending, none, false},
	}
	for _, c := range cases {
		if got := CanRSVP(c.p, c.g, c.r); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// Section 6, "See a teen's contact details" and "See contact details of
// adults" (DEC-31). Leadership, including co-leadership granted by another
// leader, never reveals a teen's contact details.
func TestCanSeeContact(t *testing.T) {
	cases := []struct {
		name string
		p    *Person
		self bool
		c    Contact
		want bool
	}{
		{"admin sees a teen", admin, false, Contact{Teen: true}, true},
		{"teen sees own", teen, true, Contact{Teen: true}, true},
		{"leader never sees a teen", adult, false, Contact{Teen: true, Shares: true}, false},
		{"co-leader never sees a teen", adult2, false, Contact{Teen: true, Shares: true}, false},
		{"leader sees a teen only with a grant", adult, false, Contact{Teen: true, Shares: true, Grant: true}, true},
		{"leader sees an adult member", adult, false, Contact{Shares: true}, true},
		{"leader doesn't see a hidden adult", adult, false, Contact{Shares: true, HideFromLeaders: true}, false},
		{"leader sees a hidden adult with a grant", adult, false, Contact{Shares: true, HideFromLeaders: true, Grant: true}, true},
		{"no shared group, no details", adult, false, Contact{}, false},
		{"guest sees nothing", guest, false, Contact{Shares: true}, false},
	}
	for _, c := range cases {
		if got := CanSeeContact(c.p, c.self, c.c); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// Guard rails: the last admin can't be demoted or deleted; nobody demotes
// or deletes themselves.
func TestCanDemoteOrDelete(t *testing.T) {
	otherAdmin := &Person{ID: 9, Admin: true}
	if !CanDemoteOrDelete(admin, otherAdmin, 2) || !CanDemoteOrDelete(admin, adult, 1) {
		t.Error("admins demote other admins (not the last) and delete members")
	}
	if CanDemoteOrDelete(admin, admin, 2) || CanDemoteOrDelete(admin, otherAdmin, 1) || CanDemoteOrDelete(adult, adult2, 2) {
		t.Error("not yourself, not the last admin, not as a member")
	}
}
