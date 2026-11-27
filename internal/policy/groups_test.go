package policy

import "testing"

// Carried from Fold's policy_test.go with its group rules; changes are noted
// where a case differs (see groups.go).

var (
	admin  = &Person{ID: 1, Admin: true}
	adult  = &Person{ID: 2}
	adult2 = &Person{ID: 3}
	teen   = &Person{ID: 4, Teen: true}
	guest  *Person

	none   = Rel{}
	member = Rel{Member: true}
	leader = Rel{Member: true, Leader: true}

	pending  = Group{Status: GroupPending, Visibility: Public}
	archived = Group{Status: GroupArchived, Visibility: Public}
	deleted  = Group{Status: GroupDeleted, Visibility: Public}
)

func active(vis string) Group { return Group{Status: GroupActive, Visibility: vis} }

func TestCanSeeGroup(t *testing.T) {
	cases := []struct {
		name string
		p    *Person
		g    Group
		r    Rel
		want bool
	}{
		{"guest sees public", guest, active(Public), none, true},
		{"guest can't see church only", guest, active(Church), none, false},
		{"guest can't see invite-only", guest, active(InviteOnly), none, false},
		{"member sees public", adult, active(Public), none, true},
		{"member sees church only", adult, active(Church), none, true},
		{"teen sees church only", teen, active(Church), none, true},
		{"outsider can't see invite-only", adult, active(InviteOnly), none, false},
		{"member of invite-only sees it", adult, active(InviteOnly), member, true},
		{"teen member of invite-only sees it", teen, active(InviteOnly), member, true},
		{"leader sees own invite-only", adult, active(InviteOnly), leader, true},
		{"admin sees invite-only (DEC-5)", admin, active(InviteOnly), none, true},
		{"a disabled admin is no admin", &Person{ID: 9, Admin: true, Inactive: true}, active(InviteOnly), none, false},
		{"admin sees pending", admin, pending, none, true},
		{"leader sees own pending", adult, pending, leader, true},
		{"member can't see pending", adult, pending, member, false},
		{"guest can't see pending", guest, pending, none, false},
		{"leader sees own archived", adult, archived, leader, true},
		{"member can't see archived", adult, archived, member, false},
		{"nobody sees deleted, admins included (R29)", admin, deleted, leader, false},
		{"unknown visibility fails closed", adult, active("secret"), none, false},
	}
	for _, c := range cases {
		if got := CanSeeGroup(c.p, c.g, c.r); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCanJoinGroup(t *testing.T) {
	cases := []struct {
		name string
		p    *Person
		g    Group
		r    Rel
		want bool
	}{
		{"member joins public", adult, active(Public), none, true},
		{"member joins church only", adult, active(Church), none, true},
		{"teen joins like a member", teen, active(Church), none, true},
		{"member can't join invite-only", adult, active(InviteOnly), none, false},
		{"existing member re-joins invite-only", adult, active(InviteOnly), member, true},
		{"admin joins invite-only", admin, active(InviteOnly), none, true},
		{"guest can't join", guest, active(Public), none, false},
		{"nobody joins a pending group", adult, pending, none, false},
		{"nobody joins an archived group", admin, archived, none, false},
		{"nobody joins a deleted group", admin, deleted, none, false},
		{"removed people don't rejoin on their own (DEC-35)", adult, active(Public), Rel{Removed: true}, false},
		{"admins may rejoin after removal", admin, active(Public), Rel{Removed: true}, true},
	}
	for _, c := range cases {
		if got := CanJoinGroup(c.p, c.g, c.r); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCreateAndApproveGroups(t *testing.T) {
	if !CanCreateGroup(admin) || !CanCreateGroup(adult) || CanCreateGroup(teen) || CanCreateGroup(guest) {
		t.Error("create: admins and adults yes; teens and guests no")
	}
	if NewGroupStatus(admin) != GroupActive || NewGroupStatus(adult) != GroupPending {
		t.Error("admin groups start active, others pending")
	}
	if !IsAdmin(admin) || IsAdmin(adult) || IsAdmin(guest) {
		t.Error("only admins approve groups and manage people")
	}
}

func TestCanManageGroup(t *testing.T) {
	g := active(Public)
	cases := []struct {
		name string
		p    *Person
		g    Group
		r    Rel
		want bool
	}{
		{"admin manages any group", admin, g, none, true},
		{"leader manages own group", adult, g, leader, true},
		{"co-leader manages too", adult2, g, leader, true},
		{"member can't manage", adult, g, member, false},
		{"outsider can't manage", adult, g, none, false},
		{"guest can't manage", guest, g, none, false},
		{"signed-out stale leader can't manage", nil, g, leader, false},
		{"removed leader can't manage", adult, g, Rel{Removed: true}, false},
		{"nobody manages a deleted group", admin, deleted, leader, false},
	}
	for _, c := range cases {
		if got := CanManageGroup(c.p, c.g, c.r); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestDecideGroupStatus(t *testing.T) {
	cases := []struct {
		name     string
		p        *Person
		r        Rel
		from, to string
		want     GroupStatusDecision
	}{
		{"admin approves", admin, none, GroupPending, GroupActive, GroupStatusApprove},
		{"admin rejects", admin, none, GroupPending, GroupArchived, GroupStatusReject},
		{"leader archives own", adult, leader, GroupActive, GroupArchived, GroupStatusArchive},
		{"leader can't approve own", adult, leader, GroupPending, GroupActive, GroupStatusForbidden},
		{"leader can't unarchive", adult, leader, GroupArchived, GroupActive, GroupStatusForbidden},
		{"admin unarchives", admin, none, GroupArchived, GroupActive, GroupStatusUnarchive},
		{"nobody goes back to pending (DEC-160)", admin, none, GroupActive, GroupPending, GroupStatusNoReturn},
		{"a status edit never deletes", admin, none, GroupActive, GroupDeleted, GroupStatusForbidden},
		{"same status is no move", adult, leader, GroupActive, GroupActive, GroupStatusKept},
		{"member can't", adult, member, GroupActive, GroupArchived, GroupStatusForbidden},
	}
	for _, c := range cases {
		if got := DecideGroupStatus(c.p, c.r, c.from, c.to); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	if !CanDeleteGroup(admin, active(Public)) || CanDeleteGroup(adult, active(Public)) || CanDeleteGroup(admin, deleted) {
		t.Error("only admins delete, and only once (R29)")
	}
}

func TestCanChangeRole(t *testing.T) {
	g := active(Public)
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
		{"can't promote a disabled account", adult, leader, &Person{ID: 8, Inactive: true}, member, true, 1, false},
		{"leader demotes a co-leader", adult, leader, adult2, leader, false, 2, true},
		{"can't demote the last leader", admin, none, adult2, leader, false, 1, false},
		{"leader can step down when another leads", adult, leader, adult, leader, false, 2, true},
		{"demoting a plain member is a no-op that's allowed", adult, leader, adult2, member, false, 1, true},
		{"leader can't demote an admin co-leader (DEC-35)", adult, leader, admin, leader, false, 2, false},
		{"admin demotes another admin co-leader", admin, none, &Person{ID: 9, Admin: true}, leader, false, 2, true},
		{"leader may promote an admin member", adult, leader, admin, member, true, 1, true},
	}
	for _, c := range cases {
		if got := CanChangeRole(c.p, g, c.r, c.target, c.targetRel, c.makeLeader, c.leaders); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCanRemoveMember(t *testing.T) {
	g := active(Public)
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
		if got := CanRemoveMember(c.p, g, c.r, c.target, c.targetRel, c.leaders); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCanBeSuccessor(t *testing.T) {
	if !CanBeSuccessor(adult, member) || !CanBeSuccessor(adult2, leader) || !CanBeSuccessor(admin, none) {
		t.Error("adult members and admins can take over")
	}
	if CanBeSuccessor(adult, none) || CanBeSuccessor(teen, member) || CanBeSuccessor(guest, none) {
		t.Error("outsiders, teens and nobody can't")
	}
	if CanBeSuccessor(&Person{ID: 9, Inactive: true}, member) || CanBeSuccessor(&Person{ID: 9, Admin: true, Inactive: true}, none) {
		t.Error("an account that is not active can't take over")
	}
}

func TestLeaving(t *testing.T) {
	if !CanLeaveGroup(member, 1) || !CanLeaveGroup(leader, 2) || CanLeaveGroup(leader, 1) || CanLeaveGroup(none, 1) {
		t.Error("anyone leaves except the last leader")
	}
}

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

func TestCanAddToGroup(t *testing.T) {
	for _, c := range []struct {
		p    *Person
		g    Group
		r    Rel
		want bool
	}{
		{adult, active(Public), leader, true},
		{adult, pending, leader, false},  // leading a waiting group grants nothing (DEC-88)
		{adult, archived, leader, false}, // nor an archived one
		{admin, pending, none, true},
		{adult, active(Public), member, false},
		{admin, deleted, none, false},
	} {
		if got := CanAddToGroup(c.p, c.g, c.r); got != c.want {
			t.Errorf("CanAddToGroup(%+v, %+v, %+v) = %v", c.p, c.g, c.r, got)
		}
	}
}

func TestCanInvite(t *testing.T) {
	act, arch := active(Public), archived
	cases := []struct {
		name          string
		p             *Person
		target        *Group
		leadsTarget   bool
		leadsAnywhere bool
		want          bool
	}{
		{"admin invites with no group", admin, nil, false, false, true},
		{"admin invites into any active group", admin, &act, false, false, true},
		{"not even an admin invites into an archived group", admin, &arch, false, false, false},
		{"leader invites into own group", adult, &act, true, true, true},
		{"leader can't invite into own archived group (DEC-88)", adult, &arch, true, true, false},
		{"leader invites with no group", adult, nil, false, true, true},
		{"leader can't invite into a group they don't lead", adult, &act, false, true, false},
		{"member can't invite into a group", adult, &act, false, false, false},
		{"member can't invite with no group", adult, nil, false, false, false},
		{"teen can't invite", teen, nil, false, true, false},
		{"guest can't invite", guest, nil, false, false, false},
		{"disabled leader can't invite", &Person{ID: 7, Inactive: true}, &act, true, true, false},
	}
	for _, c := range cases {
		if got := CanInvite(c.p, c.target, c.leadsTarget, c.leadsAnywhere); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	if !LeadsApprovedGroup([]string{GroupPending, GroupActive}) || LeadsApprovedGroup([]string{GroupPending, GroupArchived}) {
		t.Error("only an active group counts")
	}
}

func TestPeopleAndContact(t *testing.T) {
	if !CanSearchPeople(admin, false) || !CanSearchPeople(adult, true) || CanSearchPeople(adult, false) || CanSearchPeople(guest, true) {
		t.Error("search: admins and leaders of an active group (R28)")
	}
	for _, c := range []struct {
		name   string
		viewer *Person
		leads  bool
		target *Person
		hidden bool
		want   bool
	}{
		{"admin sees any address (DEC-5)", admin, false, adult, true, true},
		{"leader sees a member's address (P5)", adult, true, adult2, false, true},
		{"leader respects a hidden address (P5)", adult, true, adult2, true, false},
		{"leader never sees a teen's address", adult, true, teen, false, false},
		{"a member who doesn't lead sees none", adult, false, adult2, false, false},
		{"you see your own", adult2, false, adult2, true, true},
		{"signed out sees none", guest, true, adult, false, false},
	} {
		if got := CanSeeEmail(c.viewer, c.leads, c.target, c.hidden); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
	if !CanDecideAccount(admin) || CanDecideAccount(adult) {
		t.Error("admins decide account requests")
	}
	if !CanDisable(admin, adult, 1) || CanDisable(admin, admin, 2) || CanDisable(admin, &Person{ID: 9, Admin: true}, 1) || CanDisable(adult, adult2, 2) {
		t.Error("disable: admins only, never yourself, never the last admin")
	}
	if !MorePrivate(Public, Church) || !MorePrivate(Church, InviteOnly) || MorePrivate(InviteOnly, Public) || MorePrivate(Church, Church) {
		t.Error("MorePrivate")
	}
}
