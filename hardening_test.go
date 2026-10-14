package main

import (
	"net"
	"net/http"
	"strings"
	"testing"

	"fold/models"
)

// Hardening review (Oct 2026): every list of event dates hides events the
// viewer can't see, and making an event itself more private cancels the
// registrations of people it now shuts out.

func TestNarrowEventHiddenFromGroupLists(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	gid := createGroupAs(t, admin, "Choir", models.VisibilityPublic)
	open := scheduleGroupEvent(t, admin, gid, "Open rehearsal")
	secret := scheduleGroupEvent(t, admin, gid, "Members-only planning")
	setVisibility(t, admin, secret.ID, models.VisibilityInviteOnly, "", http.StatusOK)
	priv := scheduleGroupEvent(t, admin, gid, "Signed-in social")
	setVisibility(t, admin, priv.ID, models.VisibilityPrivate, "", http.StatusOK)

	newTestUser(t, app, "out", "password1234", models.RoleMember)
	outC := loginAPI(t, app, "out", "password1234")
	anon := newAPIClient(t, app)

	titles := func(c *apiClient) string {
		var b strings.Builder
		for _, o := range listOccurrences(t, c, itoa(gid), "2026-11-01", "2026-12-31") {
			b.WriteString(o.Title + "|")
		}
		w := c.do("GET", "/api/v1/groups/"+itoa(gid), nil, false)
		b.WriteString(w.Body.String())
		return b.String()
	}
	a, o, ad := titles(anon), titles(outC), titles(admin)
	for _, s := range []string{a, o, ad} {
		if !strings.Contains(s, open.Title) {
			t.Fatalf("public event missing: %s", s)
		}
	}
	if strings.Contains(a, secret.Title) || strings.Contains(a, priv.Title) {
		t.Fatalf("anonymous visitor saw a narrower event: %s", a)
	}
	if strings.Contains(o, secret.Title) || !strings.Contains(o, priv.Title) {
		t.Fatalf("signed-in outsider: %s", o)
	}
	if !strings.Contains(ad, secret.Title) {
		t.Fatal("admin lost the invite-only event")
	}
}

func TestEventMorePrivateCancelsShutOutRegistrants(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	gid := createGroupAs(t, admin, "Garden Club", models.VisibilityPublic)
	mem := newTestUser(t, app, "mem", "password1234", models.RoleMember)
	addMemberEmail(t, admin, gid, mem.Email)
	memC := loginAPI(t, app, "mem", "password1234")
	out := newTestUser(t, app, "out", "password1234", models.RoleMember)
	outC := loginAPI(t, app, "out", "password1234")
	ev := scheduleGroupEvent(t, admin, gid, "Picnic")
	register(t, memC, ev.ID)
	register(t, outC, ev.ID)

	setVisibility(t, admin, ev.ID, models.VisibilityInviteOnly, "", http.StatusOK)
	if regCount(app, out, ev.ID) != 0 || regCount(app, mem, ev.ID) != 1 {
		t.Fatalf("registrations: out %d, mem %d", regCount(app, out, ev.ID), regCount(app, mem, ev.ID))
	}
	found := false
	for _, n := range noticeIDs(t, outC) {
		if strings.HasPrefix(n.ID, "registration-cancelled:") {
			found = true
		}
	}
	if !found {
		t.Fatal("outsider was not told their registration was cancelled")
	}
}

func TestOversizedBodyRefused(t *testing.T) {
	app := testApp(t)
	c := newAPIClient(t, app)
	big := map[string]any{"email": strings.Repeat("a", maxRequestBytes), "password": "x"}
	if w := c.do("POST", "/api/v1/auth/login", big, true); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized login: %d", w.Code)
	}
}

func TestAvatarFetchRefusesInternalRanges(t *testing.T) {
	app := testApp(t)
	for _, ip := range []string{"100.64.0.1", "100.127.255.254", "0.1.2.3", "10.0.0.1", "169.254.169.254"} {
		if app.ipAllowed(net.ParseIP(ip)) {
			t.Errorf("%s allowed", ip)
		}
	}
	if !app.ipAllowed(net.ParseIP("93.184.216.34")) {
		t.Error("public address refused")
	}
}

func TestShortSessionSecretRefusedOutsideDev(t *testing.T) {
	t.Setenv("APP_ENV", "prod")
	t.Setenv("SESSION_SECRET", "changeme")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "at least 32") {
		t.Fatalf("short secret: %v", err)
	}
}
