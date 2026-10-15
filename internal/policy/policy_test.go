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

// Section 6, "Register for a public / private / invite-only event"
// (registrations; guests come in M1.4).
func TestCanRegister(t *testing.T) {
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
		if got := CanRegister(c.p, c.g, c.r); got != c.want {
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

// DEC-39: an optional group member limit.
func TestGroupHasRoom(t *testing.T) {
	two := 2
	if !GroupHasRoom(nil, 1000) || !GroupHasRoom(&two, 1) || GroupHasRoom(&two, 2) || GroupHasRoom(&two, 3) {
		t.Error("nil is unlimited; full at or over the limit")
	}
}

// Section 6, accounts doors (DEC-4, DEC-44, M1.3): a member's
// invite-a-friend always waits for an admin; only admins decide; inline
// approval from the bell is for adult requests with no warnings only.
func TestAccountRequestRules(t *testing.T) {
	if !CanInviteFriend(adult) || !CanInviteFriend(admin) {
		t.Error("signed-in adults may invite a friend (still admin-approved)")
	}
	if CanInviteFriend(teen) || CanInviteFriend(guest) {
		t.Error("teens and guests may not invite a friend")
	}
	if !CanDecideAccountRequest(admin) || CanDecideAccountRequest(adult) || CanDecideAccountRequest(teen) {
		t.Error("only admins decide account requests")
	}
	if !InlineApproveAccountOK("adult", false) {
		t.Error("a clean adult request may be approved inline")
	}
	for _, tc := range []struct {
		typ  string
		warn bool
	}{
		{"adult", true}, {"teen", false}, {"teen", true},
	} {
		if InlineApproveAccountOK(tc.typ, tc.warn) {
			t.Errorf("inline approve allowed for %+v", tc)
		}
	}
}

// Section 6, "Invite a new person (no approval needed)" (DEC-4, M1.2):
// admins may invite anyone; leaders only into a group they lead, or with no
// group; hosts, members, teens and guests may not invite.
func TestCanInvite(t *testing.T) {
	cases := []struct {
		name          string
		p             *Person
		groupNamed    bool
		leadsTarget   bool
		leadsAnywhere bool
		want          bool
	}{
		{"admin invites with no group", admin, false, false, false, true},
		{"admin invites into any group", admin, true, false, false, true},
		{"leader invites into own group", adult, true, true, true, true},
		{"leader invites with no group", adult, false, false, true, true},
		{"leader can't invite into a group they don't lead", adult, true, false, true, false},
		{"member can't invite into a group", adult, true, false, false, false},
		{"member can't invite with no group", adult, false, false, false, false},
		{"host (not a leader) can't invite", adult, false, false, false, false},
		{"teen can't invite", teen, false, false, true, false},
		{"guest can't invite", guest, false, false, false, false},
	}
	for _, c := range cases {
		if got := CanInvite(c.p, c.groupNamed, c.leadsTarget, c.leadsAnywhere); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func churchEvent(vis string, host bool) EventView {
	return EventView{ChurchWide: true, Visibility: vis, Host: host}
}

func groupEvent(vis string) EventView {
	return EventView{Visibility: vis}
}

func TestMoreOpen(t *testing.T) {
	if !MoreOpen(Public, InviteOnly) || !MoreOpen(Public, Private) || !MoreOpen(Private, InviteOnly) {
		t.Fatal("more open pairs")
	}
	if MoreOpen(InviteOnly, Public) || MoreOpen(Public, Public) || MoreOpen(Private, Private) || MoreOpen("nope", InviteOnly) {
		t.Fatal("not more open")
	}
}

func TestCanSeeEvent(t *testing.T) {
	opened := groupEvent(Public)
	tightened := groupEvent(InviteOnly) // the event's own value decides (DEC-48)
	hidden := active(InviteOnly)
	cases := []struct {
		name string
		p    *Person
		ev   EventView
		g    Group
		r    Rel
		want bool
	}{
		{"guest sees public church-wide", guest, churchEvent(Public, false), Group{}, none, true},
		{"guest can't see private church-wide", guest, churchEvent(Private, false), Group{}, none, false},
		{"member sees private church-wide", adult, churchEvent(Private, false), Group{}, none, true},
		{"invite-only church-wide fails closed", adult, churchEvent(InviteOnly, false), Group{}, none, false},
		{"admin sees invite-only church-wide", admin, churchEvent(InviteOnly, false), Group{}, none, true},
		{"guest sees a public group event", guest, groupEvent(Public), active(Public), none, true},
		{"guest can't see a private group event", guest, groupEvent(Private), active(Public), none, false},
		{"member sees a private group event", adult, groupEvent(Private), active(InviteOnly), none, true},
		{"outsider can't see an invite-only event", adult, groupEvent(InviteOnly), active(Public), none, false},
		{"member sees their invite-only event", adult, groupEvent(InviteOnly), active(InviteOnly), member, true},
		{"outsider can't see a tightened invite-only event", adult, tightened, hidden, none, false},
		{"member sees a tightened invite-only event", adult, tightened, hidden, member, true},
		{"an inherited private event stays private after the group opens up", guest, groupEvent(Private), active(Public), none, false},
		{"opened invite-only group event is public to a guest", guest, opened, hidden, none, true},
		{"opened invite-only group event is private to members only when private", guest, groupEvent(Private), hidden, none, false},
		{"signed-in outsider sees an opened private event", adult, groupEvent(Private), hidden, none, true},
		{"pending group hides a public event from a member", adult, opened, pending, member, false},
		{"leader of a pending group still sees its event", adult, opened, pending, leader, true},
		{"archived group hides an opened event from a guest", guest, opened, archived, none, false},
		{"admin sees a pending group's event", admin, opened, pending, none, true},
	}
	for _, c := range cases {
		if got := CanSeeEvent(c.p, c.ev, c.g, c.r); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCanManageAndMessageEvent(t *testing.T) {
	group := groupEvent(Public)
	wide := churchEvent(Public, false)
	hosted := churchEvent(Private, true)
	cases := []struct {
		name    string
		p       *Person
		ev      EventView
		r       Rel
		manage  bool
		message bool
	}{
		{"guest", guest, wide, none, false, false},
		{"member of a group event", adult, group, member, false, false},
		{"leader of a group event", adult, group, leader, true, true},
		{"stray host flag on a group event", adult, EventView{Visibility: Public, Host: true}, member, false, false},
		{"non-host of a church-wide event", adult, wide, none, false, false},
		{"host of a church-wide event", adult, hosted, none, true, true},
		{"host can't manage a different church-wide event", adult, wide, none, false, false},
		{"admin manages a group event they don't lead", admin, group, none, true, true},
		{"host flag is a fact; assigning a teen is refused separately", teen, hosted, none, true, true},
	}
	for _, c := range cases {
		if got := CanManageEvent(c.p, c.ev, c.r); got != c.manage {
			t.Errorf("manage %s: got %v, want %v", c.name, got, c.manage)
		}
		if got := CanMessageEvent(c.p, c.ev, c.r); got != c.message {
			t.Errorf("message %s: got %v, want %v", c.name, got, c.message)
		}
	}
}

func TestCanSetEventVisibility(t *testing.T) {
	group := groupEvent(InviteOnly)
	wide := churchEvent(Public, false)
	hosted := churchEvent(Public, true)
	cases := []struct {
		name, vis string
		p         *Person
		ev        EventView
		r         Rel
		want      bool
	}{
		{"leader opens an invite-only group event", Public, adult, group, leader, true},
		{"leader sets it private", Private, adult, group, leader, true},
		{"leader sets it back to invite-only", InviteOnly, adult, group, leader, true},
		{"unknown value", "secret", adult, group, leader, false},
		{"member can't", Public, adult, group, member, false},
		{"admin can", Private, admin, group, none, true},
		{"host switches church-wide to private", Private, adult, hosted, none, true},
		{"host switches church-wide to public", Public, adult, hosted, none, true},
		{"host can't make church-wide invite-only", InviteOnly, adult, hosted, none, false},
		{"non-host can't switch a church-wide event", Private, adult, wide, none, false},
		{"admin can switch a church-wide event", Private, admin, wide, none, true},
	}
	for _, c := range cases {
		if got := CanSetEventVisibility(c.p, c.ev, c.r, c.vis); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCanAssignHost(t *testing.T) {
	if !CanAssignHost(admin, adult) {
		t.Error("admin assigns an adult")
	}
	if CanAssignHost(admin, teen) || CanAssignHost(adult, adult) || CanAssignHost(admin, nil) || CanAssignHost(nil, adult) {
		t.Error("only an admin assigns an adult")
	}
}

func TestCanSeeAttendeeNames(t *testing.T) {
	hidden := active(InviteOnly)
	opened := groupEvent(Public)
	openedPrivate := groupEvent(Private)
	ordinary := groupEvent(Public)
	strict := groupEvent(InviteOnly)
	wide := churchEvent(Public, false)
	hosted := churchEvent(Public, true)
	cases := []struct {
		name string
		p    *Person
		ev   EventView
		g    Group
		r    Rel
		want bool
	}{
		{"guest of a public event", guest, ordinary, active(Public), none, false},
		{"guest of an opened event", guest, opened, hidden, none, false},
		{"signed-in outsider of an ordinary public event", adult, ordinary, active(Public), none, true},
		{"signed-in outsider of a private group event", adult, groupEvent(Private), active(Private), none, true},
		{"outsider of an opened public event sees a count", adult, opened, hidden, none, false},
		{"outsider of an opened private event sees a count", adult2, openedPrivate, hidden, none, false},
		{"member of the opened group sees names", adult, opened, hidden, member, true},
		{"leader sees names", adult, opened, hidden, leader, true},
		{"admin sees names", admin, opened, hidden, none, true},
		{"teen member sees names", teen, opened, hidden, member, true},
		{"teen outsider of an opened event sees a count", teen, opened, hidden, none, false},
		{"stray host on an opened group event sees a count", adult, EventView{Visibility: Public, Host: true}, hidden, none, false},
		{"church-wide host sees names", adult, hosted, Group{}, none, true},
		{"signed-in person sees church-wide names", adult2, wide, Group{}, none, true},
		{"guest of a church-wide event", guest, wide, Group{}, none, false},
		{"someone who can't see the event", adult, groupEvent(InviteOnly), hidden, none, false},
		{"member of an invite-only event in an invite-only group", adult, strict, hidden, member, true},
		{"outsider of an invite-only event in a public group can't see it", adult, strict, active(Public), none, false},
	}
	for _, c := range cases {
		if got := CanSeeAttendeeNames(c.p, c.ev, c.g, c.r); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCanRegisterEvent(t *testing.T) {
	hidden := active(InviteOnly)
	if !CanRegisterEvent(adult, groupEvent(Public), hidden, none) {
		t.Error("outsider registers for an opened event without joining")
	}
	if CanRegisterEvent(guest, churchEvent(Public, false), Group{}, none) {
		t.Error("guest registration waits for M1.4")
	}
	if CanRegisterEvent(adult, groupEvent(InviteOnly), hidden, none) {
		t.Error("outsider can't register for an invite-only event")
	}
	if !CanRegisterEvent(adult, groupEvent(InviteOnly), hidden, member) {
		t.Error("member registers for their invite-only event")
	}
	if !CanRegisterEvent(adult, churchEvent(Private, false), Group{}, none) {
		t.Error("signed-in person registers for a private church-wide event")
	}
}
