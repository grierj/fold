package app_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// member makes an active person and signs them in in a browser of their own.
func (s *site) member(name, email string) *browser {
	s.t.Helper()
	if _, err := s.Writer.Exec(`INSERT INTO people (uid, name, email, status, created_at, updated_at) VALUES (lower(hex(randomblob(16))), ?, ?, 'active', 0, 0)`, name, email); err != nil {
		s.t.Fatal(err)
	}
	b := s.browser()
	b.post("/signin", "/signin", url.Values{"email": {email}})
	s.work()
	if r := b.get("/l/" + s.linkIn(email)); r.status != http.StatusSeeOther {
		s.t.Fatalf("sign in %s: %d", email, r.status)
	}
	return b
}

func groupForm(name, about, vis string) url.Values {
	return url.Values{"name": {name}, "about": {about}, "visibility": {vis}}
}

// start makes a group as b and returns its uid.
func (s *site) start(b *browser, name, vis string) string {
	s.t.Helper()
	r := b.post("/groups/new", "/groups/new", groupForm(name, "We meet on Tuesdays.", vis))
	loc := r.header.Get("Location")
	if !strings.HasPrefix(loc, "/groups/") {
		s.t.Fatalf("start %s: %d %s", name, r.status, loc)
	}
	return strings.TrimPrefix(strings.SplitN(loc, "?", 2)[0], "/groups/")
}

// End state: a member starts a group; it waits; an admin approves or turns
// it down with a reason and note; the leader hears.
func TestGroupWaitsForApproval(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	mel := s.member("Mel Leader", "mel@example.org")
	uid := s.start(mel, "Tuesday Bible Study", "church")
	other := s.member("Oscar Other", "oscar@example.org")
	if r := other.get("/groups/" + uid); r.status != http.StatusNotFound {
		t.Fatalf("a waiting group is visible to a member: %d", r.status)
	}
	if r := mel.get("/groups/" + uid); !strings.Contains(r.body, "Waiting for approval") {
		t.Fatal("the leader can't see their waiting group")
	}
	if r := admin.get("/notices"); !strings.Contains(r.body, "Mel Leader asked to start") {
		t.Fatal("the admin got no notice")
	}
	admin.post("/admin/groups", "/admin/groups/"+uid+"/approve", url.Values{"note": {"Welcome! Let the office know your room."}})
	if r := mel.get("/notices"); !strings.Contains(r.body, "was approved.") || !strings.Contains(r.body, "Let the office know your room.") {
		t.Fatal("the leader did not hear, with the note")
	}
	var text string
	s.Writer.QueryRow(`SELECT text FROM audit_log WHERE action = 'group.approve'`).Scan(&text)
	if text != "Welcome! Let the office know your room." {
		t.Fatalf("the note is not in the audit log (R30): %q", text)
	}
	if r := other.get("/groups"); !strings.Contains(r.body, "Tuesday Bible Study") {
		t.Fatal("an approved church-only group is not in the list")
	}
}

func TestGroupTurnedDownNeedsAReason(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	mel := s.member("Mel Leader", "mel@example.org")
	uid := s.start(mel, "Duplicate Study", "church")
	if r := admin.post("/admin/groups", "/admin/groups/"+uid+"/reject", url.Values{"reason": {"other"}}); r.header.Get("Location") != "/admin/groups?done=reason" {
		t.Fatalf("\"other\" without a note: %s", r.header.Get("Location"))
	}
	admin.post("/admin/groups", "/admin/groups/"+uid+"/reject", url.Values{"reason": {"similar"}})
	r := mel.get("/groups/" + uid)
	if !strings.Contains(r.body, "Archived") || !strings.Contains(r.body, "A similar group already exists.") {
		t.Fatal("the leader does not see why")
	}
	if r := mel.get("/notices"); !strings.Contains(r.body, "wasn&#39;t approved. A similar group already exists.") {
		t.Fatalf("notice: %s", between(r.body, "<main", "</main>"))
	}
}

// Check: an invite-only group answers exactly like a missing page to someone
// outside it, on every page and in search.
func TestInviteOnlyIsMissingToOutsiders(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	uid := s.start(admin, "Elders Prayer", "invite_only")
	outsider := s.member("Oscar Other", "oscar@example.org")
	stranger := s.browser()
	for _, b := range []*browser{outsider, stranger} {
		for _, path := range []string{"/groups/", "/groups/%s/edit"} {
			real := strings.Replace(path, "%s", uid, 1)
			fake := strings.Replace(path, "%s", "nosuchgroup0000000000000000", 1)
			if path == "/groups/" {
				real, fake = path+uid, path+"nosuchgroup0000000000000000"
			}
			a, z := b.get(real), b.get(fake)
			if a.status != z.status || noisePattern.ReplaceAllString(a.body, "") != noisePattern.ReplaceAllString(z.body, "") {
				t.Errorf("%s answers differently from a missing group", real)
			}
		}
		for _, action := range []string{"join", "leave", "archive", "unarchive", "delete", "edit"} {
			a := b.post("/signin", "/groups/"+uid+"/"+action, nil)
			z := b.post("/signin", "/groups/nosuchgroup0000000000000000/"+action, nil)
			if a.status != z.status {
				t.Errorf("POST %s: %d, missing group %d", action, a.status, z.status)
			}
		}
		if r := b.get("/groups?q=Elders"); strings.Contains(r.body, "Elders Prayer") {
			t.Error("search shows an invite-only group to an outsider")
		}
	}
	if n := s.Count(t, `SELECT count(*) FROM memberships`); n != 1 {
		t.Fatalf("%d memberships; an outsider got in", n)
	}
	if r := admin.get("/groups?q=Elders"); !strings.Contains(r.body, "Elders Prayer") {
		t.Fatal("the admin can't find it (DEC-5)")
	}
}

func TestDirectoryFiltersBeforeLimiting(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	s.start(admin, "Open Choir", "public")
	s.start(admin, "Men's Breakfast", "church")
	s.start(admin, "Staff Only", "invite_only")
	s.start(admin, "100% Joy", "public")
	stranger := s.browser()
	r := stranger.get("/groups")
	if !strings.Contains(r.body, "Open Choir") || strings.Contains(r.body, "Breakfast") || strings.Contains(r.body, "Staff Only") {
		t.Fatal("a visitor sees more than public groups")
	}
	if r := stranger.get("/"); !strings.Contains(r.body, "Open Choir") {
		t.Fatal("the home page doesn't show a visitor the public groups")
	}
	member := s.member("Mia Member", "mia@example.org")
	r = member.get("/groups")
	if !strings.Contains(r.body, "Breakfast") || strings.Contains(r.body, "Staff Only") {
		t.Fatal("a member sees the wrong groups")
	}
	// A % in a search is a literal character, not a wildcard.
	if r := member.get("/groups?q=" + url.QueryEscape("%")); !strings.Contains(r.body, "100% Joy") || strings.Contains(r.body, "Open Choir") {
		t.Fatal("% was a wildcard")
	}
}

func TestJoinLeaveAndRemoved(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	uid := s.start(admin, "Open Choir", "public")
	mia := s.member("Mia Member", "mia@example.org")
	mia.post("/groups/"+uid, "/groups/"+uid+"/join", nil)
	if r := mia.get("/"); !strings.Contains(r.body, "Open Choir") {
		t.Fatal("joined group not on home")
	}
	mia.post("/groups/"+uid, "/groups/"+uid+"/leave", nil)
	mia.post("/groups/"+uid, "/groups/"+uid+"/join", nil) // leaving is not removal (DEC-86)
	if n := s.Count(t, `SELECT count(*) FROM active_memberships WHERE role = 'member'`); n != 1 {
		t.Fatal("could not rejoin after leaving")
	}
	s.Writer.Exec(`UPDATE memberships SET state = 'removed' WHERE role = 'member'`)
	if r := mia.post("/groups/"+uid, "/groups/"+uid+"/join", nil); !strings.HasSuffix(r.header.Get("Location"), "done=refused") {
		t.Fatalf("a removed person rejoined: %s", r.header.Get("Location"))
	}
	// The last leader cannot leave.
	if r := admin.post("/groups/"+uid, "/groups/"+uid+"/leave", nil); !strings.HasSuffix(r.header.Get("Location"), "done=refused") {
		t.Fatal("the last leader left")
	}
}

// Check: a leader who loses the role mid-request cannot finish the action.
func TestDemotedLeaderCannotFinish(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	mel := s.member("Mel Leader", "mel@example.org")
	uid := s.start(mel, "Tuesday Study", "church")
	admin.post("/admin/groups", "/admin/groups/"+uid+"/approve", nil)
	page := mel.get("/groups/" + uid + "/edit")
	tok := csrfPattern.FindStringSubmatch(page.body)[1]
	s.Writer.Exec(`UPDATE memberships SET role = 'member' WHERE group_id = (SELECT id FROM groups WHERE uid = ?)`, uid)
	form := groupForm("Renamed", "x", "church")
	form.Set("csrf", tok)
	form.Set("version", "2")
	mel.postRaw("/groups/"+uid+"/edit", form, nil)
	var name string
	s.Writer.QueryRow(`SELECT name FROM groups WHERE uid = ?`, uid).Scan(&name)
	if name != "Tuesday Study" {
		t.Fatal("a demoted leader saved an edit")
	}
}

// Check: making a group more private removes nobody and says so (R3).
func TestMorePrivateRemovesNobody(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	uid := s.start(admin, "Open Choir", "public")
	mia := s.member("Mia Member", "mia@example.org")
	mia.post("/groups/"+uid, "/groups/"+uid+"/join", nil)
	form := groupForm("Open Choir", "Sing with us.", "invite_only")
	form.Set("version", "1")
	r := admin.post("/groups/"+uid+"/edit", "/groups/"+uid+"/edit", form)
	if !strings.Contains(r.body, "Nobody is removed.") {
		t.Fatal("no confirmation before making it more private")
	}
	form.Set("confirmed", "yes")
	admin.post("/groups/"+uid+"/edit", "/groups/"+uid+"/edit", form)
	if n := s.Count(t, `SELECT count(*) FROM active_memberships`); n != 2 {
		t.Fatalf("%d members after making it private, want 2", n)
	}
	if r := mia.get("/groups/" + uid); r.status != http.StatusOK {
		t.Fatal("a member lost sight of their group")
	}
}

// P16: a save from a stale form changes nothing and shows the group as it is.
func TestEditConflict(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	uid := s.start(admin, "Open Choir", "public")
	stale := admin.get("/groups/" + uid + "/edit")
	tok := csrfPattern.FindStringSubmatch(stale.body)[1]
	form := groupForm("Choir", "first", "public")
	form.Set("version", "1")
	admin.post("/groups/"+uid+"/edit", "/groups/"+uid+"/edit", form)
	form = groupForm("Chorus", "second", "public")
	form.Set("version", "1")
	form.Set("csrf", tok)
	r := admin.postRaw("/groups/"+uid+"/edit", form, nil)
	if r.status != http.StatusConflict || !strings.Contains(r.body, "could not be saved") || !strings.Contains(r.body, `value="Choir"`) {
		t.Fatalf("conflict: %d", r.status)
	}
}

// R29, DEC-177: an admin deletes a group; its members are told and it is
// gone everywhere.
func TestDeleteGroup(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	uid := s.start(admin, "Open Choir", "public")
	mia := s.member("Mia Member", "mia@example.org")
	mia.post("/groups/"+uid, "/groups/"+uid+"/join", nil)
	if r := mia.post("/groups/"+uid, "/groups/"+uid+"/delete", nil); r.status != http.StatusNotFound {
		t.Fatal("a member deleted a group")
	}
	admin.post("/groups/"+uid, "/groups/"+uid+"/delete", nil)
	if r := mia.get("/notices"); !strings.Contains(r.body, "Open Choir") || !strings.Contains(r.body, "was deleted") {
		t.Fatal("the member was not told")
	}
	for _, b := range []*browser{admin, mia} {
		if r := b.get("/groups/" + uid); r.status != http.StatusNotFound {
			t.Fatal("a deleted group still shows")
		}
		if r := b.get("/groups"); strings.Contains(r.body, "Open Choir") {
			t.Fatal("a deleted group is still listed")
		}
	}
}
