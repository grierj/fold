package policy

import "testing"

func TestGuestCanAnswer(t *testing.T) {
	cases := []struct {
		name             string
		e                Event
		dvis             string
		g                Group
		cancelled, ended bool
		want             bool
	}{
		{"a public date", event(Public), "", active(Public), false, false, true},
		{"a date opened to everyone", event(InviteOnly), Public, active(InviteOnly), false, false, true},
		{"a church-only date", event(Church), "", active(Church), false, false, false},
		{"a date opened to the church only", event(InviteOnly), Church, active(InviteOnly), false, false, false},
		{"cancelled", event(Public), "", active(Public), true, false, false},
		{"ended", event(Public), "", active(Public), false, true, false},
		{"an archived group's date", event(Public), "", archived, false, false, false},
		{"a public church-wide date", churchEvent(Public, false), "", Group{}, false, false, true},
		{"a deleted event", Event{Status: EventDeleted, Visibility: Public}, "", active(Public), false, false, false},
	}
	for _, c := range cases {
		if got := GuestCanAnswer(c.e, c.dvis, c.g, c.cancelled, c.ended); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}

func TestConfirmGuest(t *testing.T) {
	cases := []struct {
		open      bool
		status    string
		memberMay bool
		want      string
	}{
		{true, "", false, ConfirmNew},
		{true, "guest", false, ConfirmAsThem},
		{true, "active", true, ConfirmAsThem},
		{true, "active", false, ConfirmRefused}, // removed from the group (R42)
		{true, "pending", false, ConfirmRefused},
		{true, "disabled", false, ConfirmRefused},
		{true, "setup", false, ConfirmRefused},
		{false, "", false, ConfirmDateGone},
		{false, "active", true, ConfirmDateGone},
	}
	for _, c := range cases {
		if got := ConfirmGuest(c.open, c.status, c.memberMay); got != c.want {
			t.Errorf("%+v: got %q", c, got)
		}
	}
}

func TestManageLinkWorks(t *testing.T) {
	ok := ManageLink{Found: true, Visible: true, SignsIn: true, Answer: AnswerGoing}
	if !ManageLinkWorks(ok) {
		t.Fatal("a good link refused")
	}
	waiting := ok
	waiting.Answer = AnswerWaiting
	if !ManageLinkWorks(waiting) {
		t.Fatal("a place in line can't be managed")
	}
	for name, change := range map[string]func(*ManageLink){
		"unknown":          func(l *ManageLink) { l.Found = false },
		"revoked":          func(l *ManageLink) { l.Revoked = true },
		"ended":            func(l *ManageLink) { l.Ended = true },
		"made private":     func(l *ManageLink) { l.Visible = false },
		"account off":      func(l *ManageLink) { l.SignsIn = false },
		"answer cancelled": func(l *ManageLink) { l.Answer = AnswerCancelled },
		"now can't go":     func(l *ManageLink) { l.Answer = AnswerCant },
	} {
		l := ok
		change(&l)
		if ManageLinkWorks(l) {
			t.Errorf("%s: works", name)
		}
	}
	if !GuestCanCancel(AnswerGoing, false) || !GuestCanCancel(AnswerWaiting, false) || GuestCanCancel(AnswerMaybe, false) || GuestCanCancel(AnswerGoing, true) {
		t.Error("guest cancel")
	}
}
