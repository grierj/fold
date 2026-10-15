package main

import (
	"fold/models"
	"testing"
)

// API responses are per-user: never cached, never replayed by back/forward.
func TestAPIResponsesAreNoStore(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "sam", "password1234", models.RoleMember)
	anon := newAPIClient(t, app)
	if got := anon.do("GET", "/api/v1/groups", nil, false).Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("anonymous groups: Cache-Control %q", got)
	}
	sam := loginAs(t, app, "sam", "password1234")
	for _, p := range []string{"/api/v1/auth/me", "/api/v1/me/groups", "/api/v1/notifications", "/api/v1/nope"} {
		if got := sam.do("GET", p, nil, false).Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s: Cache-Control %q", p, got)
		}
	}
}
