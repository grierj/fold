package policy

import "testing"

// Carried from Fold's TestCanSeeEvent and TestCanManageEvent, changed where
// events.go says. Church-wide cases return with milestone 1b.

func event(vis string) Event { return Event{Status: EventActive, Visibility: vis} }

func TestCanSeeEvent(t *testing.T) {
	cases := []struct {
		name string
		p    *Person
		e    Event
		g    Group
		r    Rel
		want bool
	}{
		{"guest sees a public event", guest, event(Public), active(Public), none, true},
		{"guest can't see a church-only event", guest, event(Church), active(Public), none, false},
		{"guest can't see an invite-only event", guest, event(InviteOnly), active(Public), none, false},
		{"member sees a church-only event", adult, event(Church), active(Church), none, true},
		{"teen sees a church-only event", teen, event(Church), active(Church), none, true},
		{"outsider can't see an invite-only event", adult, event(InviteOnly), active(InviteOnly), none, false},
		{"outsider can't see an invite-only event of a public group", adult, event(InviteOnly), active(Public), none, false},
		{"member sees their invite-only event", adult, event(InviteOnly), active(InviteOnly), member, true},
		{"leader sees their invite-only event", adult, event(InviteOnly), active(InviteOnly), leader, true},
		{"admin sees an invite-only event (DEC-5)", admin, event(InviteOnly), active(InviteOnly), none, true},
		{"a disabled admin is no admin", &Person{ID: 9, Admin: true, Inactive: true}, event(InviteOnly), active(InviteOnly), none, false},
		{"a public event stays public after its group closes (DEC-48)", guest, event(Public), active(InviteOnly), none, true},
		{"a church-only event stays so after its group opens (DEC-48)", guest, event(Church), active(Public), none, false},
		{"a waiting group hides its event from a member", adult, event(Public), pending, member, false},
		{"a waiting group hides its event from a guest", guest, event(Public), pending, none, false},
		{"leader of a waiting group sees its event", adult, event(Public), pending, leader, true},
		{"an archived group hides its event from a member", adult, event(Public), archived, member, false},
		{"leader of an archived group sees its event", adult, event(Public), archived, leader, true},
		{"admin sees an archived group's event", admin, event(Public), archived, none, true},
		{"nobody sees a deleted event, admins included", admin, Event{Status: EventDeleted, Visibility: Public}, active(Public), leader, false},
		{"nobody sees an event of a deleted group (R29)", admin, event(Public), deleted, leader, false},
		{"an unknown event status fails closed", admin, Event{Status: "", Visibility: Public}, active(Public), none, false},
		{"an unknown visibility fails closed", adult, event("secret"), active(Public), none, false},
		{"a group with no status (none in 1a) fails closed", adult, event(Public), Group{}, member, false},
	}
	for _, c := range cases {
		if got := CanSeeEvent(c.p, c.e, c.g, c.r); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCanScheduleAndManageEvent(t *testing.T) {
	ev := event(Public)
	cases := []struct {
		name string
		p    *Person
		e    Event
		g    Group
		r    Rel
		want bool
	}{
		{"leader of an active group", adult, ev, active(Public), leader, true},
		{"admin who doesn't lead it", admin, ev, active(InviteOnly), none, true},
		{"member", adult, ev, active(Public), member, false},
		{"outsider", adult, ev, active(Public), none, false},
		{"guest", guest, ev, active(Public), none, false},
		{"signed-out stale leader", nil, ev, active(Public), leader, false},
		{"removed leader", adult, ev, active(Public), Rel{Removed: true}, false},
		{"a disabled admin", &Person{ID: 9, Admin: true, Inactive: true}, ev, active(Public), none, false},
		{"leader of a waiting group (DEC-88)", adult, ev, pending, leader, false},
		{"admin on a waiting group (DEC-147)", admin, ev, pending, none, false},
		{"leader of an archived group", adult, ev, archived, leader, false},
		{"admin on an archived group", admin, ev, archived, none, false},
		{"a deleted group", admin, ev, deleted, leader, false},
	}
	for _, c := range cases {
		if got := CanScheduleEvent(c.p, c.g, c.r); got != c.want {
			t.Errorf("schedule, %s: got %v, want %v", c.name, got, c.want)
		}
		if got := CanManageEvent(c.p, c.e, c.g, c.r); got != c.want {
			t.Errorf("manage, %s: got %v, want %v", c.name, got, c.want)
		}
	}
	if CanManageEvent(admin, Event{Status: EventDeleted, Visibility: Public}, active(Public), leader) {
		t.Error("a deleted event was managed")
	}
}

// Everyone who runs an event sees it, so its editor never answers as missing.
func TestManagersSeeTheirEvents(t *testing.T) {
	for _, p := range []*Person{admin, adult, teen, guest, {ID: 9, Admin: true, Inactive: true}} {
		for _, r := range []Rel{none, member, leader, {Removed: true}} {
			for _, status := range []string{GroupPending, GroupActive, GroupArchived, GroupDeleted} {
				for _, gv := range []string{Public, Church, InviteOnly} {
					for _, ev := range []string{Public, Church, InviteOnly} {
						g, e := Group{Status: status, Visibility: gv}, event(ev)
						if CanManageEvent(p, e, g, r) && !CanSeeEvent(p, e, g, r) {
							t.Errorf("%+v %+v %+v %+v manages but can't see", p, r, g, e)
						}
					}
				}
			}
		}
	}
}

func TestCanSetEventVisibility(t *testing.T) {
	cases := []struct {
		name, from, to, group string
		want                  bool
	}{
		{"the group's own", Public, Public, Public, true},
		{"more private than the group", Public, InviteOnly, Public, true},
		{"back up to the group's", InviteOnly, Church, Church, true},
		{"wider than the group waits for 1b", Church, Public, Church, false},
		{"wider than an invite-only group", InviteOnly, Church, InviteOnly, false},
		{"keeping a visibility wider than the group now is no change", Public, Public, InviteOnly, true},
		{"narrowing an event wider than its group is allowed", Public, Church, InviteOnly, true},
		{"but never wider than it is and its group", Church, Public, InviteOnly, false},
		{"from wider than the group to the group's", Public, InviteOnly, InviteOnly, true},
		{"an unknown value", Public, "secret", Public, false},
	}
	for _, c := range cases {
		if got := CanSetEventVisibility(c.from, c.to, c.group); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
