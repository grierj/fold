package policy

import (
	"testing"
	"time"
)

var now = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

func TestStartSetup(t *testing.T) {
	for _, c := range []struct {
		done, code, want bool
	}{
		{false, true, true},
		{false, false, false}, // a stranger without the code
		{true, true, false},   // setup already done
		{true, false, false},
	} {
		if got := StartSetup(Setup{Done: c.done}, c.code); got != c.want {
			t.Errorf("StartSetup(done=%v, code=%v) = %v", c.done, c.code, got)
		}
	}
}

func TestLinkUsable(t *testing.T) {
	ok := Link{Found: true, Purpose: "signin", ExpiresAt: now.Add(time.Hour)}
	cases := []struct {
		name string
		l    Link
		want bool
	}{
		{"fresh", ok, true},
		{"missing", Link{}, false},
		{"used", with(ok, func(l *Link) { l.Used = true }), false},
		{"replaced by a newer one", with(ok, func(l *Link) { l.Cancelled = true }), false},
		{"expired", with(ok, func(l *Link) { l.ExpiresAt = now }), false},
		{"other purpose", with(ok, func(l *Link) { l.Purpose = "setup" }), false},
	}
	for _, c := range cases {
		if got := LinkUsable(c.l, "signin", now); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
}

func with(l Link, f func(*Link)) Link { f(&l); return l }

func TestFinishSetup(t *testing.T) {
	l := Link{Found: true, Purpose: "setup", ExpiresAt: now.Add(time.Hour)}
	if !FinishSetup(Setup{}, l, now) {
		t.Error("a fresh setup link is refused")
	}
	if FinishSetup(Setup{Done: true}, l, now) {
		t.Error("a second admin from a setup link")
	}
	if FinishSetup(Setup{}, with(l, func(l *Link) { l.Purpose = "signin" }), now) {
		t.Error("a sign-in link finished setup")
	}
}

func TestSignIn(t *testing.T) {
	l := Link{Found: true, Purpose: "signin", ExpiresAt: now.Add(time.Hour)}
	for _, c := range []struct {
		a    Account
		want bool
	}{
		{Account{Exists: true, Active: true}, true},
		{Account{Exists: true}, false}, // disabled or waiting for setup
		{Account{}, false},
	} {
		if got := SignIn(c.a, l, now); got != c.want {
			t.Errorf("SignIn(%+v) = %v", c.a, got)
		}
		if got := SendSignInLink(c.a); got != c.want {
			t.Errorf("SendSignInLink(%+v) = %v", c.a, got)
		}
	}
}

func TestViewGallery(t *testing.T) {
	for _, c := range []struct {
		v    Viewer
		want bool
	}{
		{Viewer{SignedIn: true, Active: true, Admin: true}, true},
		{Viewer{SignedIn: true, Active: true}, false},
		{Viewer{}, false},
	} {
		if got := ViewGallery(c.v); got != c.want {
			t.Errorf("ViewGallery(%+v) = %v", c.v, got)
		}
	}
}

func TestLinkActsWhenOpened(t *testing.T) {
	for _, c := range []struct{ usable, same, want bool }{
		{true, true, true},
		{true, false, false}, // a scanner, or another device: the button
		{false, true, false}, // used or expired
		{false, false, false},
	} {
		if got := LinkActsWhenOpened(c.usable, c.same); got != c.want {
			t.Errorf("LinkActsWhenOpened(%v, %v) = %v", c.usable, c.same, got)
		}
	}
}
