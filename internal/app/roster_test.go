package app_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// approvedGroup has mel start a church-only group that the admin approves.
func (s *site) approvedGroup(admin, leader *browser, name string) string {
	uid := s.start(leader, name, "church")
	admin.post("/admin/groups", "/admin/groups/"+uid+"/approve", nil)
	return uid
}

// P5 and DEC-78: leaders see members' addresses unless hidden; members see
// names only; outsiders get the not-found page.
func TestRosterShowsWhatEachMaySee(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	mel := s.member("Mel Leader", "mel@example.org")
	uid := s.approvedGroup(admin, mel, "Study")
	mia := s.member("Mia Member", "mia@example.org")
	hid := s.member("Hal Hidden", "hal@example.org")
	mia.post("/groups/"+uid, "/groups/"+uid+"/join", nil)
	hid.post("/groups/"+uid, "/groups/"+uid+"/join", nil)
	s.Writer.Exec(`UPDATE people SET contact_hidden = 1 WHERE email = 'hal@example.org'`)
	r := mel.get("/groups/" + uid + "/members")
	if !strings.Contains(r.body, "mia@example.org") || strings.Contains(r.body, "hal@example.org") || !strings.Contains(r.body, "Hal Hidden") {
		t.Fatal("the leader sees the wrong addresses")
	}
	r = mia.get("/groups/" + uid + "/members")
	if r.status != http.StatusOK || strings.Contains(r.body, "@example.org") || !strings.Contains(r.body, "Mel Leader") {
		t.Fatal("a member sees addresses, or no roster")
	}
	if r := s.member("Oscar Other", "oscar@example.org").get("/groups/" + uid + "/members"); r.status != http.StatusNotFound {
		t.Fatalf("an outsider sees the roster: %d", r.status)
	}
	if r := admin.get("/groups/" + uid + "/members"); !strings.Contains(r.body, "hal@example.org") {
		t.Fatal("an admin can't see every address (DEC-5)")
	}
}

// P6: a leader finds a person by name and adds them; they are told and can
// leave. Search shows names, never addresses, and needs an active group.
func TestAddByName(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	mel := s.member("Mel Leader", "mel@example.org")
	uid := s.approvedGroup(admin, mel, "Study")
	ann := s.member("Ann Newcomer", "ann@example.org")
	r := mel.get("/groups/" + uid + "/members?find=Ann")
	if !strings.Contains(r.body, "Ann Newcomer") || strings.Contains(r.body, "ann@example.org") {
		t.Fatal("search shows the wrong thing")
	}
	if strings.Contains(mel.get("/groups/"+uid+"/members?find=Mel").body, `value="`+personUID(s, "mel@example.org")) {
		t.Fatal("search offers someone already in the group")
	}
	mel.post("/groups/"+uid+"/members", "/groups/"+uid+"/members/add", url.Values{"person": {personUID(s, "ann@example.org")}})
	if r := ann.get("/notices"); !strings.Contains(r.body, "Mel Leader added you to") {
		t.Fatal("the added person was not told")
	}
	if r := ann.post("/groups/"+uid, "/groups/"+uid+"/leave", nil); !strings.HasSuffix(r.header.Get("Location"), "done=left") {
		t.Fatal("an added person cannot leave")
	}
	// A member cannot search or add.
	mia := s.member("Mia Member", "mia@example.org")
	mia.post("/groups/"+uid, "/groups/"+uid+"/join", nil)
	if strings.Contains(mia.get("/groups/"+uid+"/members?find=Ann").body, "Ann Newcomer") {
		t.Fatal("a member can search people")
	}
	if r := mia.post("/groups/"+uid+"/members", "/groups/"+uid+"/members/add", url.Values{"person": {personUID(s, "ann@example.org")}}); !strings.HasSuffix(r.header.Get("Location"), "done=refused") {
		t.Fatal("a member added someone")
	}
}

func TestCoLeadersAndRemoval(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	mel := s.member("Mel Leader", "mel@example.org")
	uid := s.approvedGroup(admin, mel, "Study")
	mia := s.member("Mia Member", "mia@example.org")
	mia.post("/groups/"+uid, "/groups/"+uid+"/join", nil)
	miaUID, melUID := personUID(s, "mia@example.org"), personUID(s, "mel@example.org")
	mel.post("/groups/"+uid+"/members", "/groups/"+uid+"/members/"+miaUID+"/leader", nil)
	if r := mia.get("/notices"); !strings.Contains(r.body, "made you a leader") {
		t.Fatal("not told about leading")
	}
	// Mia leads now; Mel can step down; the group keeps a leader.
	mel.post("/groups/"+uid+"/members", "/groups/"+uid+"/members/"+melUID+"/member", nil)
	if r := mia.post("/groups/"+uid+"/members", "/groups/"+uid+"/members/"+miaUID+"/member", nil); !strings.HasSuffix(r.header.Get("Location"), "done=refused") {
		t.Fatal("the last leader was made a member")
	}
	// A leader cannot remove an admin (DEC-35).
	admin.post("/groups/"+uid, "/groups/"+uid+"/join", nil)
	if r := mia.post("/groups/"+uid+"/members", "/groups/"+uid+"/members/"+personUID(s, "pat@cloverdale.example")+"/remove", nil); !strings.HasSuffix(r.header.Get("Location"), "done=refused") {
		t.Fatal("a leader removed an admin")
	}
	mia.post("/groups/"+uid+"/members", "/groups/"+uid+"/members/"+melUID+"/remove", nil)
	if r := mel.post("/groups/"+uid, "/groups/"+uid+"/join", nil); !strings.HasSuffix(r.header.Get("Location"), "done=refused") {
		t.Fatal("a removed person rejoined on their own")
	}
	// A leader adds them back.
	mia.post("/groups/"+uid+"/members", "/groups/"+uid+"/members/add", url.Values{"person": {melUID}})
	if n := s.Count(t, `SELECT count(*) FROM active_memberships WHERE person_id = (SELECT id FROM people WHERE email = 'mel@example.org')`); n != 1 {
		t.Fatal("a leader could not add a removed person back")
	}
}

// DEC-4: a leader invites by email and the person skips review. The page
// answers the same however the address stands.
func TestInvite(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	mel := s.member("Mel Leader", "mel@example.org")
	uid := s.approvedGroup(admin, mel, "Study")
	invite := func(name, email string) resp {
		return mel.post("/groups/"+uid+"/members", "/groups/"+uid+"/invite", url.Values{"name": {name}, "email": {email}, "adult": {"yes"}})
	}
	if r := invite("Nia New", "nia@example.org"); !strings.HasSuffix(r.header.Get("Location"), "done=invited") {
		t.Fatalf("invite: %s", r.header.Get("Location"))
	}
	s.work()
	m, ok := s.outbox.LatestTo("nia@example.org")
	if !ok || !strings.Contains(m.Subject, "invited") {
		t.Fatal("no invitation email")
	}
	nia := s.browser()
	if r := nia.get("/l/" + regexp.MustCompile(`/l/([A-Za-z0-9_-]{43})`).FindStringSubmatch(m.Text)[1]); !strings.Contains(r.body, "Sign in to") {
		t.Fatal("the invitation link does not lead to sign in")
	}
	if n := s.Count(t, `SELECT count(*) FROM active_memberships am JOIN people p ON p.id = am.person_id WHERE p.email = 'nia@example.org' AND p.status = 'active'`); n != 1 {
		t.Fatal("the invited person is not an active member")
	}
	// An existing account is told at the bell, not given a second account.
	mia := s.member("Mia Member", "mia@example.org")
	a, b := invite("Mia", "mia@example.org"), invite("Zed", "zed@example.org")
	if a.header.Get("Location") != b.header.Get("Location") {
		t.Fatal("the answer says whether the address has an account")
	}
	if s.Count(t, `SELECT count(*) FROM people WHERE email = 'mia@example.org'`) != 1 {
		t.Fatal("a second account")
	}
	if r := mia.get("/notices"); !strings.Contains(r.body, "invited you to") {
		t.Fatal("an existing member was not told")
	}
	// A waiting request is approved by an invitation.
	s.browser().post("/request", "/request", requestForm("Wes Waiting", "wes@example.org"))
	s.work()
	invite("Wes", "wes@example.org")
	if s.Count(t, `SELECT count(*) FROM people WHERE email = 'wes@example.org' AND status = 'active'`) != 1 {
		t.Fatal("an invitation did not skip review")
	}
	// Adults only, in slice 1.
	if r := mel.post("/groups/"+uid+"/members", "/groups/"+uid+"/invite", url.Values{"name": {"Kid"}, "email": {"kid@example.org"}}); r.status != http.StatusUnprocessableEntity {
		t.Fatal("an invitation without the age confirmation")
	}
	// A member cannot invite; a leader of an archived group cannot either.
	if r := mia.post("/groups/"+uid+"/members", "/groups/"+uid+"/invite", url.Values{"name": {"X"}, "email": {"x@example.org"}, "adult": {"yes"}}); r.status == http.StatusSeeOther && !strings.HasSuffix(r.header.Get("Location"), "done=refused") {
		t.Fatal("a member invited")
	}
	mel.post("/groups/"+uid, "/groups/"+uid+"/archive", nil)
	if r := invite("Y", "y@example.org"); !strings.HasSuffix(r.header.Get("Location"), "done=refused") {
		t.Fatal("a leader of an archived group invited (DEC-88)")
	}
}

func TestAdminInvitesWithNoGroup(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	if r := admin.post("/admin/requests", "/admin/invite", url.Values{"name": {"Sue Staff"}, "email": {"sue@example.org"}, "adult": {"yes"}}); r.header.Get("Location") != "/admin/requests?done=invited" {
		t.Fatalf("admin invite: %s", r.header.Get("Location"))
	}
	s.work()
	if _, ok := s.outbox.LatestTo("sue@example.org"); !ok {
		t.Fatal("no invitation email")
	}
	if r := s.member("Mia", "mia@example.org").post("/", "/admin/invite", url.Values{"name": {"X"}, "email": {"x@example.org"}, "adult": {"yes"}}); r.status != http.StatusNotFound {
		t.Fatal("a member used the admin invite")
	}
}

// A second click says nothing changed rather than repeating its success; a
// one-letter search asks for more; an admin re-inviting an account is told
// the truth (admins see everyone, DEC-5).
func TestRosterAnswersTruthfully(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	mel := s.member("Mel Leader", "mel@example.org")
	uid := s.approvedGroup(admin, mel, "Study")
	mia := s.member("Mia Member", "mia@example.org")
	mia.post("/groups/"+uid, "/groups/"+uid+"/join", nil)
	miaUID := personUID(s, "mia@example.org")
	mel.post("/groups/"+uid+"/members", "/groups/"+uid+"/members/"+miaUID+"/leader", nil)
	if r := mel.post("/groups/"+uid+"/members", "/groups/"+uid+"/members/"+miaUID+"/leader", nil); !strings.HasSuffix(r.header.Get("Location"), "done=same") {
		t.Fatalf("second click: %s", r.header.Get("Location"))
	}
	if r := mel.get("/groups/" + uid + "/members?find=A"); strings.Contains(r.body, "Nobody by that name") {
		t.Fatal("a one-letter search says nobody matched")
	}
	if r := admin.post("/admin/requests", "/admin/invite", url.Values{"name": {"Mia"}, "email": {"mia@example.org"}, "adult": {"yes"}}); r.header.Get("Location") != "/admin/requests?done=exists" {
		t.Fatalf("admin re-invite: %s", r.header.Get("Location"))
	}
}
