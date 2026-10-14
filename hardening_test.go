package main

import (
	"fmt"
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

	// Preview first, like a group change: the outsider is counted.
	var impact visibilityImpactJSON
	decodeJSON(t, admin.do("GET", fmt.Sprintf("/api/v1/events/%d/visibility-impact?visibility=invite_only", ev.ID), nil, false), &impact)
	if len(impact.Tightened) != 1 || impact.Tightened[0].Registrants != 1 || len(impact.LeaderOpened) != 0 {
		t.Fatalf("impact: %+v", impact)
	}
	apiDo(t, memC, "GET", fmt.Sprintf("/api/v1/events/%d/visibility-impact?visibility=invite_only", ev.ID), nil, http.StatusForbidden)
	// Without confirm nothing is saved.
	setVisibility(t, admin, ev.ID, models.VisibilityInviteOnly, "", http.StatusConflict)
	if e := eventRow(t, app, ev.ID); e.Visibility != models.VisibilityPublic || regCount(app, out, ev.ID) != 1 {
		t.Fatalf("saved without confirm: %+v", e)
	}
	apiDo(t, admin, "PUT", fmt.Sprintf("/api/v1/events/%d/visibility", ev.ID), map[string]any{"visibility": models.VisibilityInviteOnly, "confirm": true}, http.StatusOK)
	if regCount(app, out, ev.ID) != 0 || regCount(app, mem, ev.ID) != 1 {
		t.Fatalf("registrations: out %d, mem %d", regCount(app, out, ev.ID), regCount(app, mem, ev.ID))
	}
	found := false
	for _, n := range noticeIDs(t, outC) {
		if n.Kind == "registration.cancelled" {
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

func TestShortSessionSecretRefusedOutsideDev(t *testing.T) {
	t.Setenv("APP_ENV", "prod")
	t.Setenv("SESSION_SECRET", "changeme")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "at least 32") {
		t.Fatalf("short secret: %v", err)
	}
}

func TestShortSecretMessageIsActionableAndHidesTheValue(t *testing.T) {
	t.Setenv("APP_ENV", "prod")
	t.Setenv("SESSION_SECRET", "hunter2-secret")
	_, err := LoadConfig()
	msg := err.Error()
	for _, want := range []string{"14 characters", "at least 32", "openssl rand -base64 48", ".env", "docker compose up -d"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, "hunter2") || strings.Contains(msg, "\n") {
		t.Errorf("message leaks the value or spans lines: %s", msg)
	}
}
