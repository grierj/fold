package app_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// End state: an admin disables and re-enables an account, choosing a
// successor first when it is a group's only leader (DEC-36).
func TestDisableNeedsASuccessorAndEnableRestores(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	mel := s.member("Mel Leader", "mel@example.org")
	uid := s.approvedGroup(admin, mel, "Study")
	mia := s.member("Mia Member", "mia@example.org")
	mia.post("/groups/"+uid, "/groups/"+uid+"/join", nil)
	melUID, miaUID := personUID(s, "mel@example.org"), personUID(s, "mia@example.org")

	page := admin.get("/admin/people/" + melUID)
	if !strings.Contains(page.body, "Who leads Study next?") || !strings.Contains(page.body, "Mia Member") {
		t.Fatal("no successor choice for the group Mel alone leads")
	}
	r := admin.post("/admin/people/"+melUID, "/admin/people/"+melUID+"/disable", nil)
	if r.status != http.StatusUnprocessableEntity || !strings.Contains(r.body, "Choose who leads Study next.") {
		t.Fatalf("disable without a successor: %d", r.status)
	}
	if r := mel.get("/"); !strings.Contains(r.body, "Welcome, Mel") {
		t.Fatal("refused disable still signed Mel out")
	}
	admin.post("/admin/people/"+melUID, "/admin/people/"+melUID+"/disable", url.Values{"successor-" + uid: {miaUID}})
	if r := mel.get("/"); strings.Contains(r.body, "Welcome, Mel") {
		t.Fatal("a disabled person is still signed in")
	}
	if n := s.Count(t, `SELECT count(*) FROM active_memberships WHERE role = 'leader' AND person_id = (SELECT id FROM people WHERE email = 'mia@example.org')`); n != 1 {
		t.Fatal("the successor does not lead")
	}
	if r := mia.get("/groups/" + uid + "/members"); strings.Contains(r.body, "Mel Leader") {
		t.Fatal("a disabled person still shows on the roster (DEC-38)")
	}
	// A disabled person gets no sign-in link.
	b := s.browser()
	b.post("/signin", "/signin", url.Values{"email": {"mel@example.org"}})
	s.work()
	if s.Count(t, `SELECT count(*) FROM links WHERE used_at IS NULL AND cancelled_at IS NULL AND person_id = (SELECT id FROM people WHERE email = 'mel@example.org')`) != 0 {
		t.Fatal("a disabled person was sent a sign-in link")
	}
	admin.post("/admin/people/"+melUID, "/admin/people/"+melUID+"/enable", nil)
	if r := mia.get("/groups/" + uid + "/members"); !strings.Contains(r.body, "Mel Leader") {
		t.Fatal("re-enabling did not restore their membership")
	}
}

func TestLastAdminCannotBeDisabled(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	other := s.member("Ann Admin", "ann@example.org")
	s.Writer.Exec(`UPDATE people SET is_admin = 1 WHERE email = 'ann@example.org'`)
	annUID := personUID(s, "ann@example.org")
	admin.post("/admin/people/"+annUID, "/admin/people/"+annUID+"/disable", nil)
	// Now Pat is the only active admin: Ann (disabled) cannot disable anyone,
	// and nobody can disable Pat.
	if r := other.get("/admin/people"); r.status != http.StatusNotFound {
		t.Fatalf("a disabled admin still reaches admin pages: %d", r.status)
	}
	patUID := personUID(s, "pat@cloverdale.example")
	if r := admin.post("/admin/people/"+patUID, "/admin/people/"+patUID+"/disable", nil); r.status != http.StatusUnprocessableEntity && r.status != http.StatusOK {
		t.Fatalf("self-disable: %d", r.status)
	}
	if s.Count(t, `SELECT count(*) FROM people WHERE email = 'pat@cloverdale.example' AND status = 'active'`) != 1 {
		t.Fatal("the last admin was disabled")
	}
}

// Check: a group's leader who is disabled mid-request cannot finish.
func TestDisabledLeaderCannotFinish(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	mel := s.member("Mel Leader", "mel@example.org")
	uid := s.approvedGroup(admin, mel, "Study")
	s.member("Ann Other", "ann@example.org")
	page := mel.get("/groups/" + uid + "/members")
	tok := csrfPattern.FindStringSubmatch(page.body)[1]
	s.Writer.Exec(`UPDATE people SET status = 'disabled' WHERE email = 'mel@example.org'`)
	mel.postRaw("/groups/"+uid+"/members/add", url.Values{"csrf": {tok}, "person": {personUID(s, "ann@example.org")}}, nil)
	if n := s.Count(t, `SELECT count(*) FROM memberships WHERE person_id = (SELECT id FROM people WHERE email = 'ann@example.org')`); n != 0 {
		t.Fatal("a disabled leader added someone")
	}
}

func TestProfile(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	mel := s.member("Mel Leader", "mel@example.org")
	uid := s.approvedGroup(admin, mel, "Study")
	mia := s.member("Mia Member", "mia@example.org")
	mia.post("/groups/"+uid, "/groups/"+uid+"/join", nil)
	// Mia hides her address from leaders (P5).
	if r := mel.get("/groups/" + uid + "/members"); !strings.Contains(r.body, "mia@example.org") {
		t.Fatal("the leader should see it before")
	}
	mia.post("/profile", "/profile/contact", url.Values{"hide": {"1"}})
	if r := mel.get("/groups/" + uid + "/members"); strings.Contains(r.body, "mia@example.org") {
		t.Fatal("a hidden address still shows to a leader")
	}
	// She renames herself; a stale form is a conflict (P16).
	stale := mia.get("/profile")
	tok := csrfPattern.FindStringSubmatch(stale.body)[1]
	mia.post("/profile", "/profile/name", url.Values{"name": {"Mia M."}, "version": {"1"}})
	r := mia.postRaw("/profile/name", url.Values{"csrf": {tok}, "name": {"Mia X"}, "version": {"1"}}, nil)
	if r.status != http.StatusConflict {
		t.Fatalf("stale profile save: %d", r.status)
	}
	if r := mel.get("/groups/" + uid + "/members"); !strings.Contains(r.body, "Mia M.") {
		t.Fatal("the new name does not show")
	}
}

func TestPhonesGetATabBar(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	if r := admin.get("/"); !strings.Contains(r.body, `aria-label="Tabs"`) {
		t.Fatal("no tab bar for a signed-in person")
	}
}
