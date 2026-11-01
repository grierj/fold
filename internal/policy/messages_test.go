package policy

import "testing"

func TestCanMessageAudience(t *testing.T) {
	admin, member, teen := &Person{ID: 1, Admin: true}, &Person{ID: 2}, &Person{ID: 3, Teen: true}
	active, pending := Group{Status: GroupActive}, Group{Status: GroupPending}
	ownGroup := MessageTarget{Group: active, Rel: Rel{Member: true, Leader: true}}
	otherGroup := MessageTarget{Group: active, Rel: Rel{Member: true}}
	pendingGroup := MessageTarget{Group: pending, Rel: Rel{Member: true, Leader: true}}
	ownEvent := MessageTarget{IsEvent: true, Group: active, Rel: Rel{Leader: true}}
	otherEvent := MessageTarget{IsEvent: true, Group: active}
	hosted := MessageTarget{IsEvent: true, Event: EventView{ChurchWide: true, Host: true}}
	notHosted := MessageTarget{IsEvent: true, Event: EventView{ChurchWide: true}}
	for _, tc := range []struct {
		name    string
		p       *Person
		kind    string
		targets []MessageTarget
		want    bool
	}{
		{"signed out", nil, AudienceGroup, []MessageTarget{ownGroup}, false},
		{"leader, own group", member, AudienceGroup, []MessageTarget{ownGroup}, true},
		{"member, a group they're in", member, AudienceGroup, []MessageTarget{otherGroup}, false},
		{"leader of a pending group", member, AudienceGroup, []MessageTarget{pendingGroup}, false},
		{"leader, own group's event", member, AudienceOccurrence, []MessageTarget{ownEvent}, true},
		{"leader, someone else's event", member, AudienceEventSeries, []MessageTarget{otherEvent}, false},
		{"leader, own and another's events", member, AudienceEvents, []MessageTarget{ownEvent, otherEvent}, false},
		{"host, hosted church-wide event", member, AudienceEvents, []MessageTarget{hosted}, true},
		{"church-wide event not hosted", member, AudienceOccurrence, []MessageTarget{notHosted}, false},
		{"leader, everyone", member, AudienceAllMembers, nil, false},
		{"leader, chosen people", member, AudienceUsers, nil, false},
		{"admin, everyone", admin, AudienceAllMembers, nil, true},
		{"admin, chosen people", admin, AudienceUsers, nil, true},
		{"admin, any event", admin, AudienceEvents, []MessageTarget{otherEvent, notHosted}, true},
		{"no targets", admin, AudienceGroup, nil, false},
		{"a group target for an event audience", member, AudienceOccurrence, []MessageTarget{ownGroup}, false},
		{"unknown kind", admin, "everyone_ever", nil, false},
		{"teen", teen, AudienceGroup, []MessageTarget{ownGroup}, false},
	} {
		if got := CanMessageAudience(tc.p, tc.kind, tc.targets); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
	if !CanComposeMessages(member, true, false) || !CanComposeMessages(member, false, true) || CanComposeMessages(member, false, false) ||
		!CanComposeMessages(admin, false, false) || CanComposeMessages(teen, true, true) {
		t.Error("CanComposeMessages")
	}
}

func TestCanEditMessage(t *testing.T) {
	admin, member := &Person{ID: 1, Admin: true}, &Person{ID: 2}
	for _, tc := range []struct {
		name               string
		p                  *Person
		author, isReminder bool
		want               bool
	}{
		{"signed out", nil, false, false, false},
		{"author", member, true, false, true},
		{"someone else", member, false, false, false},
		{"admin, another's message", admin, false, false, true},
		{"admin, a reminder", admin, false, true, false},
		{"anyone, a reminder", member, false, true, false},
	} {
		if got := CanEditMessage(tc.p, tc.author, tc.isReminder); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestCanSeeMessageHistory(t *testing.T) {
	admin, member := &Person{ID: 1, Admin: true}, &Person{ID: 2}
	for _, tc := range []struct {
		name                        string
		p                           *Person
		author, isReminder, manages bool
		want                        bool
	}{
		{"signed out", nil, false, false, false, false},
		{"signed out, a reminder of an event they'd manage", nil, false, true, true, false},
		{"author", member, true, false, false, true},
		{"someone else's announcement", member, false, false, false, false},
		{"an announcement to an event they manage", member, false, false, true, false},
		{"admin, any announcement", admin, false, false, false, true},
		{"admin, any reminder", admin, false, true, false, true},
		{"manager, the event's reminder", member, false, true, true, true},
		{"not a manager, a reminder", member, false, true, false, false},
	} {
		if got := CanSeeMessageHistory(tc.p, tc.author, tc.isReminder, tc.manages); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A recipient reads an announcement they got (the page a push opens), but
// that never opens its history, and a reminder stays its managers'.
func TestCanReadMessage(t *testing.T) {
	admin, member := &Person{ID: 1, Admin: true}, &Person{ID: 2}
	for _, tc := range []struct {
		name                                   string
		p                                      *Person
		author, isReminder, manages, recipient bool
		want                                   bool
	}{
		{"signed out", nil, false, false, false, false, false},
		{"signed out, even a recipient", nil, false, false, false, true, false},
		{"recipient of an announcement", member, false, false, false, true, true},
		{"not a recipient", member, false, false, false, false, false},
		{"author", member, true, false, false, false, true},
		{"admin, any announcement", admin, false, false, false, false, true},
		{"recipient of a reminder", member, false, true, false, true, false},
		{"manager, the event's reminder", member, false, true, true, false, true},
	} {
		if got := CanReadMessage(tc.p, tc.author, tc.isReminder, tc.manages, tc.recipient); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
