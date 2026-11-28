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
	for _, p := range []*Person{admin, adult, teen, guest, {ID: 9, Admin: true, Inactive: true}, {ID: 10, Inactive: true}} {
		for _, r := range []Rel{none, member, leader, {Removed: true}} {
			for _, status := range []string{GroupPending, GroupActive, GroupArchived, GroupDeleted} {
				for _, gv := range []string{Public, Church, InviteOnly} {
					for _, ev := range []string{Public, Church, InviteOnly} {
						for _, wide := range []bool{false, true} {
							for _, host := range []bool{false, true} {
								g, e := Group{Status: status, Visibility: gv}, Event{Status: EventActive, Visibility: ev, ChurchWide: wide, Host: host}
								if CanManageEvent(p, e, g, r) && !CanSeeEvent(p, e, g, r) {
									t.Errorf("%+v %+v %+v %+v manages but can't see", p, r, g, e)
								}
							}
						}
					}
				}
			}
		}
	}
}

func TestCanSetEventVisibility(t *testing.T) {
	cases := []struct {
		name, to   string
		churchWide bool
		want       bool
	}{
		{"a group's event may be public (1b opens wider)", Public, false, true},
		{"church only", Church, false, true},
		{"invite only", InviteOnly, false, true},
		{"an unknown value", "secret", false, false},
		{"a church-wide event may be public", Public, true, true},
		{"or church only", Church, true, true},
		{"but never invite only: it has no members", InviteOnly, true, false},
	}
	for _, c := range cases {
		if got := CanSetEventVisibility(c.to, c.churchWide); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestOpensWider(t *testing.T) {
	cases := []struct {
		name, from, to, group string
		churchWide            bool
		want                  bool
	}{
		{"invite-only group's event made public", InviteOnly, Public, InviteOnly, false, true},
		{"made church only from an invite-only group", InviteOnly, Church, InviteOnly, false, true},
		{"opened to its group's own", InviteOnly, Church, Church, false, false},
		{"already wider, made wider still", Church, Public, InviteOnly, false, true},
		{"already wider, kept", Public, Public, InviteOnly, false, false},
		{"narrowed, still wider than the group", Public, Church, InviteOnly, false, false},
		{"a church-wide event has no group", Church, Public, "", true, false},
	}
	for _, c := range cases {
		if got := OpensWider(c.from, c.to, c.group, c.churchWide); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestDateVisibility(t *testing.T) {
	for _, c := range []struct {
		event, date, want string
	}{{InviteOnly, "", InviteOnly}, {InviteOnly, Public, Public}, {Church, InviteOnly, Church}, {Public, Church, Public}, {Church, Church, Church}} {
		if got := Widest(c.event, c.date); got != c.want {
			t.Errorf("Widest(%q, %q) = %q, want %q", c.event, c.date, got, c.want)
		}
	}
	cases := []struct {
		name, event, to string
		churchWide      bool
		want            bool
	}{
		{"opened wider than its series", InviteOnly, Public, false, true},
		{"opened to church only", InviteOnly, Church, false, true},
		{"never narrower (P7)", Public, Church, false, false},
		{"the same is no opening", Church, Church, false, false},
		{"back to the series'", Public, "", false, true},
		{"a church-wide date opened to public", Church, Public, true, true},
		{"an unknown value", InviteOnly, "secret", false, false},
	}
	for _, c := range cases {
		if got := CanSetDateVisibility(c.event, c.to, c.churchWide); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	// An opened date is seen by those it is opened to; the event is not.
	stranger, g := guest, active(InviteOnly)
	if !CanSeeDate(stranger, event(InviteOnly), Public, g, none) || CanSeeEvent(stranger, event(InviteOnly), g, none) {
		t.Error("an opened date")
	}
	if CanSeeDate(adult, event(InviteOnly), Public, archived, member) || CanSeeDate(guest, event(InviteOnly), Public, deleted, none) {
		t.Error("a waiting, archived or deleted group still hides an opened date")
	}
	if CanSeeDate(admin, Event{Status: EventDeleted, Visibility: Public}, Public, g, none) {
		t.Error("a deleted event's opened date")
	}
}

func churchEvent(vis string, host bool) Event {
	return Event{Status: EventActive, Visibility: vis, ChurchWide: true, Host: host}
}

func TestChurchWideEvents(t *testing.T) {
	cases := []struct {
		name         string
		p            *Person
		e            Event
		see, manages bool
	}{
		{"guest sees a public church-wide event", guest, churchEvent(Public, false), true, false},
		{"guest can't see a church-only one", guest, churchEvent(Church, false), false, false},
		{"member sees a church-only one", adult, churchEvent(Church, false), true, false},
		{"invite only fails closed to everyone but admins and hosts", adult, churchEvent(InviteOnly, false), false, false},
		{"its host runs it", adult, churchEvent(Church, true), true, true},
		{"a host sees even an invite-only one", adult, churchEvent(InviteOnly, true), true, true},
		{"a disabled host runs nothing", &Person{ID: 9, Inactive: true}, churchEvent(Public, true), true, false},
		{"a teen host flag grants nothing (DEC-18)", teen, churchEvent(Church, true), true, false},
		{"admin runs any church-wide event", admin, churchEvent(Church, false), true, true},
		{"a deleted one is seen and run by nobody", admin, Event{Status: EventDeleted, Visibility: Public, ChurchWide: true}, false, false},
		{"a stray host row on a group's event grants nothing", adult, Event{Status: EventActive, Visibility: Public, Host: true}, true, false},
	}
	for _, c := range cases {
		g := Group{}
		if !c.e.ChurchWide {
			g = active(Public)
		}
		if got := CanSeeEvent(c.p, c.e, g, none); got != c.see {
			t.Errorf("see, %s: got %v", c.name, got)
		}
		if got := CanManageEvent(c.p, c.e, g, none); got != c.manages {
			t.Errorf("manage, %s: got %v", c.name, got)
		}
	}
	if !CanCreateChurchEvent(admin) || CanCreateChurchEvent(adult) || CanCreateChurchEvent(guest) {
		t.Error("only admins make church-wide events")
	}
}

func TestDateByDate(t *testing.T) {
	future, started := DateState{}, DateState{Started: true}
	byHand, bySeries := DateState{Cancelled: true}, DateState{Cancelled: true, BySeries: true}
	if !CanCancelDate(true, future) || CanCancelDate(false, future) || CanCancelDate(true, started) || CanCancelDate(true, byHand) {
		t.Error("cancel: those who run it, one future date not yet cancelled")
	}
	if !CanRestoreDate(true, byHand, true) || CanRestoreDate(true, bySeries, true) || CanRestoreDate(true, byHand, false) ||
		CanRestoreDate(false, byHand, true) || CanRestoreDate(true, DateState{Cancelled: true, Started: true}, true) {
		t.Error("restore: a future date cancelled by hand, while the series makes that day")
	}
	if !CanOpenDate(true, future) || CanOpenDate(true, byHand) || CanOpenDate(true, started) || CanOpenDate(false, future) {
		t.Error("open: those who run it, a future date that is on")
	}
}

func TestHosts(t *testing.T) {
	wide, hosted := churchEvent(Church, false), churchEvent(Church, true)
	if !CanNameHost(admin, wide, adult) || !CanNameHost(adult, hosted, adult2) {
		t.Error("admins and hosts name hosts")
	}
	if CanNameHost(adult, wide, adult2) || CanNameHost(admin, wide, teen) || CanNameHost(admin, wide, &Person{ID: 9, Inactive: true}) || CanNameHost(admin, wide, nil) {
		t.Error("only active adults, named by those who run it")
	}
	if CanNameHost(admin, event(Public), adult) || CanFindHosts(admin, event(Public)) {
		t.Error("a group's event has no hosts")
	}
	if !CanFindHosts(adult, hosted) || CanFindHosts(adult, wide) || CanFindHosts(guest, wide) || !CanRemoveHost(adult, hosted) || CanRemoveHost(adult2, wide) {
		t.Error("finding and removing hosts")
	}
}
