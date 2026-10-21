package main

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"fold/models"
)

// Video links (5.8, M1.8): validation on event create and update, the
// reveal rule in the event API, and the Jitsi link generator.

const videoLink = "https://zoom.example.org/j/123456789"

func videoEventStart() time.Time {
	return time.Now().UTC().AddDate(0, 0, 30).Truncate(24 * time.Hour).Add(19 * time.Hour)
}

func createVideoEvent(t *testing.T, c *apiClient, gid string, extra map[string]any) (eventJSON, string) {
	t.Helper()
	start := videoEventStart()
	body := map[string]any{
		"title": "Supper Club", "startsLocal": start.Format("2006-01-02T15:04"),
		"durationMinutes": 60, "tzid": "UTC",
	}
	for k, v := range extra {
		body[k] = v
	}
	w := c.do("POST", "/api/v1/groups/"+gid+"/events", body, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create video event: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Event eventJSON `json:"event"`
	}
	decodeJSON(t, w, &created)
	return created.Event, start.Format("2006-01-02")
}

func eventOccurrence(t *testing.T, c *apiClient, eventID uint) occurrenceJSON {
	t.Helper()
	w := c.do("GET", fmt.Sprintf("/api/v1/events/%d", eventID), nil, false)
	if w.Code != http.StatusOK {
		t.Fatalf("get event: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Event       eventJSON        `json:"event"`
		Occurrences []occurrenceJSON `json:"occurrences"`
	}
	decodeJSON(t, w, &body)
	if len(body.Occurrences) == 0 {
		t.Fatal("event page has no occurrences")
	}
	return body.Occurrences[0]
}

func TestVideoLinkValidation(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "Video Study")
	start := videoEventStart()

	bad := []map[string]any{
		{"videoProvider": "link", "videoUrl": "http://insecure.example.org/call"},
		{"videoProvider": "link", "videoUrl": "example.org/call"},
		{"videoProvider": "link", "videoUrl": ""},
		{"videoProvider": "link"},
		{"videoProvider": "jitsi", "videoUrl": "ftp://meet.example.org/room"},
		{"videoProvider": "zoom", "videoUrl": videoLink},
		{"videoProvider": "link", "videoUrl": videoLink, "videoReveal": "sometimes"},
		{"videoProvider": "link", "videoUrl": "https://example.org/" + strings.Repeat("a", 2048)},
	}
	for i, extra := range bad {
		body := map[string]any{
			"title": "Bad Video", "startsLocal": start.Format("2006-01-02T15:04"),
			"durationMinutes": 60, "tzid": "UTC",
		}
		for k, v := range extra {
			body[k] = v
		}
		if w := admin.do("POST", "/api/v1/groups/"+gid+"/events", body, true); w.Code != http.StatusBadRequest {
			t.Fatalf("case %d: %d %s, want 400", i, w.Code, w.Body.String())
		}
	}

	// A good link round-trips; the creator (a manager) sees it in the
	// create response even under the default registered_only.
	ev, _ := createVideoEvent(t, admin, gid, map[string]any{
		"videoProvider": "link", "videoUrl": videoLink,
	})
	if ev.VideoProvider != "link" || ev.VideoURL != videoLink || ev.VideoReveal != "registered_only" || ev.VideoHidden {
		t.Fatalf("created event video: %+v", ev)
	}

	// An update with a non-https address is refused and stores nothing.
	w := admin.do("PUT", fmt.Sprintf("/api/v1/events/%d", ev.ID),
		map[string]any{"videoUrl": "http://insecure.example.org/call"}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("update to http: %d %s, want 400", w.Code, w.Body.String())
	}
	var stored models.Event
	if err := app.DB.First(&stored, ev.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.VideoURL != videoLink {
		t.Fatalf("stored url after refused update = %q", stored.VideoURL)
	}

	// none clears the URL.
	w = admin.do("PUT", fmt.Sprintf("/api/v1/events/%d", ev.ID),
		map[string]any{"videoProvider": "none"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("clear video: %d %s", w.Code, w.Body.String())
	}
	if err := app.DB.First(&stored, ev.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.VideoProvider != "none" || stored.VideoURL != "" {
		t.Fatalf("after none: provider=%q url=%q", stored.VideoProvider, stored.VideoURL)
	}
	if occ := eventOccurrence(t, admin, ev.ID); occ.VideoURL != "" || occ.VideoHidden {
		t.Fatalf("occurrence after none: url=%q hidden=%v", occ.VideoURL, occ.VideoHidden)
	}
}

// registered_only (the default): a person going on a date and the leader
// see the link; a waitlisted person and an outsider get videoHidden and
// no URL anywhere in the response. always shows everyone.
func TestVideoRevealInEventAPI(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "Video Group")
	ev, date := createVideoEvent(t, admin, gid, map[string]any{
		"videoProvider": "link", "videoUrl": videoLink,
		"capacity": 1, "waitlistEnabled": true,
	})

	mk := func(name string) *apiClient {
		newTestUser(t, app, name, "password1234", models.RoleMember)
		return loginAPI(t, app, name, "password1234")
	}
	going := mk("vgoing")
	if code, body := registerAs(going, ev.ID, date, "going", nil); code != http.StatusOK {
		t.Fatalf("going register: %d %s", code, body)
	}
	waiting := mk("vwaiting")
	if code, body := registerAs(waiting, ev.ID, date, "going", nil); code != http.StatusOK {
		t.Fatalf("waitlist register: %d %s", code, body)
	}
	maybe := mk("vmaybe")
	if code, body := registerAs(maybe, ev.ID, date, "maybe", nil); code != http.StatusOK {
		t.Fatalf("maybe register: %d %s", code, body)
	}
	outsider := mk("voutsider")
	anon := newAPIClient(t, app)

	see := []struct {
		name string
		c    *apiClient
	}{
		{"the leader", admin}, {"a person going", going},
	}
	for _, tc := range see {
		occ := eventOccurrence(t, tc.c, ev.ID)
		if occ.VideoURL != videoLink || occ.VideoHidden {
			t.Errorf("%s should see the link: url=%q hidden=%v", tc.name, occ.VideoURL, occ.VideoHidden)
		}
	}
	hidden := []struct {
		name string
		c    *apiClient
	}{
		{"a waitlisted person", waiting}, {"a maybe", maybe},
		{"an outsider", outsider}, {"a signed-out visitor", anon},
	}
	for _, tc := range hidden {
		w := tc.c.do("GET", fmt.Sprintf("/api/v1/events/%d", ev.ID), nil, false)
		if w.Code != http.StatusOK {
			t.Fatalf("%s get event: %d %s", tc.name, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), videoLink) {
			t.Errorf("%s was sent the link somewhere in the response", tc.name)
		}
		occ := eventOccurrence(t, tc.c, ev.ID)
		if occ.VideoURL != "" || !occ.VideoHidden {
			t.Errorf("%s: url=%q hidden=%v, want no url and hidden", tc.name, occ.VideoURL, occ.VideoHidden)
		}
	}

	// always: everyone who can see the event sees the link.
	w := admin.do("PUT", fmt.Sprintf("/api/v1/events/%d", ev.ID),
		map[string]any{"videoReveal": "always"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("set reveal always: %d %s", w.Code, w.Body.String())
	}
	for _, tc := range append(see, hidden...) {
		occ := eventOccurrence(t, tc.c, ev.ID)
		if occ.VideoURL != videoLink || occ.VideoHidden {
			t.Errorf("always: %s should see the link: url=%q hidden=%v", tc.name, occ.VideoURL, occ.VideoHidden)
		}
	}
}

var jitsiURLRe = regexp.MustCompile(`^https://[a-z0-9.-]+/[a-z0-9]+(?:-[a-z0-9]+)*-[a-z0-9]{10}$`)

func generateJitsi(t *testing.T, c *apiClient, want int) string {
	t.Helper()
	w := c.do("POST", "/api/v1/events/video/jitsi", map[string]any{}, true)
	if w.Code != want {
		t.Fatalf("generate jitsi: %d %s, want %d", w.Code, w.Body.String(), want)
	}
	var body struct {
		URL string `json:"url"`
	}
	decodeJSON(t, w, &body)
	return body.URL
}

func TestGenerateJitsiLink(t *testing.T) {
	app := testApp(t)
	admin, _ := openGroup(t, app, "Jitsi Choir")

	// Defaults: meet.jit.si, and the slug falls back to the site name.
	first := generateJitsi(t, admin, http.StatusOK)
	if !jitsiURLRe.MatchString(first) {
		t.Fatalf("jitsi url shape: %q", first)
	}
	if !strings.HasPrefix(first, "https://meet.jit.si/fold-") {
		t.Fatalf("jitsi url = %q, want the default domain and fold slug", first)
	}
	if second := generateJitsi(t, admin, http.StatusOK); second == first {
		t.Fatalf("two calls gave the same url %q", first)
	}

	// A plain member may not mint links; a group leader may.
	newTestUser(t, app, "jm", "password1234", models.RoleMember)
	generateJitsi(t, loginAPI(t, app, "jm", "password1234"), http.StatusForbidden)
	leader := newTestUser(t, app, "jl", "password1234", models.RoleMember)
	newTestGroup(t, app, leader, "Led Group", models.GroupActive)
	generateJitsi(t, loginAPI(t, app, "jl", "password1234"), http.StatusOK)

	// Signed out is a 401.
	anon := newAPIClient(t, app)
	anon.fetchCSRF()
	if w := anon.do("POST", "/api/v1/events/video/jitsi", map[string]any{}, true); w.Code != http.StatusUnauthorized {
		t.Fatalf("signed-out jitsi: %d %s, want 401", w.Code, w.Body.String())
	}

	// The settings steer the domain and the slug.
	for _, s := range []models.Setting{
		{Key: "jitsi_domain", Value: "meet.example.org"},
		{Key: "church_name", Value: "Grace Community Church!"},
	} {
		if err := app.DB.Create(&s).Error; err != nil {
			t.Fatal(err)
		}
	}
	got := generateJitsi(t, admin, http.StatusOK)
	if !strings.HasPrefix(got, "https://meet.example.org/grace-community-church-") {
		t.Fatalf("jitsi url with settings = %q", got)
	}
	if !jitsiURLRe.MatchString(got) {
		t.Fatalf("jitsi url shape with settings: %q", got)
	}
}

func TestChurchSlug(t *testing.T) {
	cases := map[string]string{
		"Grace Community Church!": "grace-community-church",
		"  St.  Mary's  ":         "st-mary-s",
		"Fold":                    "fold",
		"":                        "fold",
		"!!!":                     "fold",
	}
	for in, want := range cases {
		if got := churchSlug(in); got != want {
			t.Errorf("churchSlug(%q) = %q, want %q", in, got, want)
		}
	}
}
