package policy

import "testing"

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
