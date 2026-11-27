package app_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
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

var invitationPattern = regexp.MustCompile(`/invitations/([a-z0-9]+)`)

// R32: a leader invites someone found by name; nothing changes until they
// join, so their email shows on the roster only after they chose the group.
func TestInviteByName(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	mel := s.member("Mel Leader", "mel@example.org")
	uid := s.approvedGroup(admin, mel, "Study")
	ann := s.member("Ann Newcomer", "ann@example.org")
	r := mel.get("/groups/" + uid + "/members?find=Ann")
	if !strings.Contains(r.body, "Ann Newcomer") || strings.Contains(r.body, "ann@example.org") {
		t.Fatal("search shows the wrong thing")
	}
	if strings.Contains(r.body, "Add now") {
		t.Fatal("a leader is offered a direct add")
	}
	mel.post("/groups/"+uid+"/members", "/groups/"+uid+"/members/ask", url.Values{"person": {personUID(s, "ann@example.org")}})
	if r := mel.get("/groups/" + uid + "/members"); strings.Contains(r.body, "ann@example.org") || !strings.Contains(r.body, "Waiting for an answer: Ann Newcomer") {
		t.Fatal("an invited person's address shows before they joined, or they aren't listed as waiting")
	}
	page := ann.get("/notices")
	if !strings.Contains(page.body, "Mel Leader invited you to join") {
		t.Fatal("no invitation at the bell")
	}
	inv := invitationPattern.FindStringSubmatch(page.body)
	if inv == nil {
		// the notice links through its open form; read the invitation from the table
		var u string
		s.Writer.QueryRow(`SELECT uid FROM invitations WHERE state = 'open'`).Scan(&u)
		inv = []string{"", u}
	}
	if r := s.member("Oscar", "oscar@example.org").get("/invitations/" + inv[1]); r.status != http.StatusNotFound {
		t.Fatal("someone else can see the invitation")
	}
	if r := ann.post("/invitations/"+inv[1], "/invitations/"+inv[1]+"/accept", nil); !strings.HasSuffix(r.header.Get("Location"), "done=joined") {
		t.Fatalf("accept: %s", r.header.Get("Location"))
	}
	if r := mel.get("/groups/" + uid + "/members"); !strings.Contains(r.body, "ann@example.org") {
		t.Fatal("the member's address should show once they joined (P5)")
	}
	// A member cannot invite by name.
	mia := s.member("Mia Member", "mia@example.org")
	mia.post("/groups/"+uid, "/groups/"+uid+"/join", nil)
	if r := mia.post("/groups/"+uid+"/members", "/groups/"+uid+"/members/ask", url.Values{"person": {personUID(s, "oscar@example.org")}}); !strings.HasSuffix(r.header.Get("Location"), "done=refused") {
		t.Fatal("a member invited")
	}
}

func TestDeclinedInvitationChangesNothing(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	mel := s.member("Mel Leader", "mel@example.org")
	uid := s.approvedGroup(admin, mel, "Study")
	ann := s.member("Ann Newcomer", "ann@example.org")
	mel.post("/groups/"+uid+"/members", "/groups/"+uid+"/members/ask", url.Values{"person": {personUID(s, "ann@example.org")}})
	var inv string
	s.Writer.QueryRow(`SELECT uid FROM invitations`).Scan(&inv)
	ann.post("/invitations/"+inv, "/invitations/"+inv+"/decline", nil)
	if n := s.Count(t, `SELECT count(*) FROM memberships WHERE person_id = (SELECT id FROM people WHERE email = 'ann@example.org')`); n != 0 {
		t.Fatal("a declined invitation made a membership")
	}
	if r := ann.post("/invitations/"+inv, "/invitations/"+inv+"/accept", nil); !strings.HasSuffix(r.header.Get("Location"), "/invitations/"+inv) {
		t.Fatal("a declined invitation was accepted")
	}
}

// R32: admins may add someone directly; the person is told and can leave.
func TestAdminAddsDirectly(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	mel := s.member("Mel Leader", "mel@example.org")
	uid := s.approvedGroup(admin, mel, "Study")
	ann := s.member("Ann Newcomer", "ann@example.org")
	if r := mel.post("/groups/"+uid+"/members", "/groups/"+uid+"/members/add", url.Values{"person": {personUID(s, "ann@example.org")}}); !strings.HasSuffix(r.header.Get("Location"), "done=refused") {
		t.Fatal("a leader added someone directly")
	}
	admin.post("/groups/"+uid+"/members", "/groups/"+uid+"/members/add", url.Values{"person": {personUID(s, "ann@example.org")}})
	if r := ann.get("/notices"); !strings.Contains(r.body, "Pat Admin added you to") {
		t.Fatal("not told about being added")
	}
	if r := ann.post("/groups/"+uid, "/groups/"+uid+"/leave", nil); !strings.HasSuffix(r.header.Get("Location"), "done=left") {
		t.Fatal("cannot leave")
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
	mel.post("/groups/"+uid+"/members", "/groups/"+uid+"/members/"+melUID+"/member", nil)
	if r := mia.post("/groups/"+uid+"/members", "/groups/"+uid+"/members/"+miaUID+"/member", nil); !strings.HasSuffix(r.header.Get("Location"), "done=refused") {
		t.Fatal("the last leader was made a member")
	}
	admin.post("/groups/"+uid, "/groups/"+uid+"/join", nil)
	if r := mia.post("/groups/"+uid+"/members", "/groups/"+uid+"/members/"+personUID(s, "pat@cloverdale.example")+"/remove", nil); !strings.HasSuffix(r.header.Get("Location"), "done=refused") {
		t.Fatal("a leader removed an admin")
	}
	mia.post("/groups/"+uid+"/members", "/groups/"+uid+"/members/"+melUID+"/remove", nil)
	if r := mel.post("/groups/"+uid, "/groups/"+uid+"/join", nil); !strings.HasSuffix(r.header.Get("Location"), "done=refused") {
		t.Fatal("a removed person rejoined on their own")
	}
	// A leader invites them back, and they accept (DEC-35, R32).
	mia.post("/groups/"+uid+"/members", "/groups/"+uid+"/members/ask", url.Values{"person": {melUID}})
	var inv string
	s.Writer.QueryRow(`SELECT uid FROM invitations WHERE state = 'open'`).Scan(&inv)
	mel.post("/invitations/"+inv, "/invitations/"+inv+"/accept", nil)
	if n := s.Count(t, `SELECT count(*) FROM active_memberships WHERE person_id = (SELECT id FROM people WHERE email = 'mel@example.org')`); n != 1 {
		t.Fatal("a removed person could not be invited back")
	}
}

var invitePattern = regexp.MustCompile(`/invite/([A-Za-z0-9_-]{43})`)

func (s *site) inviteLink(email string) string {
	s.t.Helper()
	s.work()
	m, ok := s.outbox.LatestTo(email)
	if !ok {
		s.t.Fatalf("no invitation to %s", email)
	}
	l := invitePattern.FindStringSubmatch(m.Text)
	if l == nil {
		s.t.Fatalf("no invitation link in %q", m.Text)
	}
	return l[1]
}

// DEC-4, R32: an emailed invitation skips review, but the invited person
// says they are an adult and accepts before anything exists. Nothing on the
// roster changes until then, whatever the address.
func TestInviteByEmail(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	mel := s.member("Mel Leader", "mel@example.org")
	uid := s.approvedGroup(admin, mel, "Study")
	invite := func(name, email string) resp {
		return mel.post("/groups/"+uid+"/members", "/groups/"+uid+"/invite", url.Values{"name": {name}, "email": {email}, "adult": {"yes"}})
	}
	mia := s.member("Mia Member", "mia@example.org")
	before := mel.get("/groups/" + uid + "/members").body
	a, b := invite("Fake Name", "mia@example.org"), invite("Nia New", "nia@example.org")
	if a.header.Get("Location") != b.header.Get("Location") {
		t.Fatal("the answer depends on the address")
	}
	after := mel.get("/groups/" + uid + "/members").body
	if noisePattern.ReplaceAllString(before, "") != noisePattern.ReplaceAllString(after, "") {
		t.Fatal("the roster changed before anyone accepted (it would show who has an account)")
	}
	if s.Count(t, `SELECT count(*) FROM people WHERE email = 'nia@example.org'`) != 0 {
		t.Fatal("an account exists before the invitation was accepted")
	}
	// Nia opens it; opening changes nothing; she accepts as an adult.
	tok := s.inviteLink("nia@example.org")
	nia := s.browser()
	if r := nia.get("/invite/" + tok); !strings.Contains(r.body, "Mel Leader invited you") {
		t.Fatalf("invitation page: %s", between(r.body, "<main", "</main>"))
	}
	r := nia.post("/invite/"+tok, "/invite/"+tok, url.Values{"name": {"Nia New"}, "age": {"adult"}})
	if !strings.HasSuffix(r.header.Get("Location"), "done=joined") {
		t.Fatalf("redeem: %d %s", r.status, r.header.Get("Location"))
	}
	if r := nia.get("/"); !strings.Contains(r.body, "Welcome, Nia New") {
		t.Fatal("not signed in after accepting")
	}
	// Mia, who has an account, accepts and joins; her name is hers, not the inviter's guess.
	mtok := s.inviteLink("mia@example.org")
	mia.post("/invite/"+mtok, "/invite/"+mtok, url.Values{"name": {"Whatever"}, "age": {"adult"}})
	if s.Count(t, `SELECT count(*) FROM people WHERE email = 'mia@example.org' AND name = 'Mia Member'`) != 1 {
		t.Fatal("an existing account was changed by an invitation")
	}
	// Used once.
	if r := s.browser().post("/signin", "/invite/"+tok, url.Values{"name": {"X"}, "age": {"adult"}}); r.status == http.StatusSeeOther {
		t.Fatal("an invitation worked twice")
	}
}

// R32: the invited person, not the inviter, says whether they are an adult.
// Under 18, nothing is created.
func TestInvitationUnder18CreatesNothing(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	mel := s.member("Mel Leader", "mel@example.org")
	uid := s.approvedGroup(admin, mel, "Youth Group")
	mel.post("/groups/"+uid+"/members", "/groups/"+uid+"/invite", url.Values{"name": {"Teen"}, "email": {"teen@example.org"}, "adult": {"yes"}})
	tok := s.inviteLink("teen@example.org")
	r := s.browser().post("/invite/"+tok, "/invite/"+tok, url.Values{"name": {"Teen"}, "age": {"young"}})
	if !strings.Contains(r.body, "under-18s aren") {
		t.Fatalf("under-18 answer: %d", r.status)
	}
	if s.Count(t, `SELECT count(*) FROM people WHERE email = 'teen@example.org'`) != 0 {
		t.Fatal("an account was made for someone under 18")
	}
	if r := s.browser().post("/signin", "/invite/"+tok, url.Values{"name": {"Teen"}, "age": {"adult"}}); r.status == http.StatusSeeOther {
		t.Fatal("the invitation still works after an under-18 answer")
	}
}

// #155: an invitation is checked against the inviter's right when used.
func TestInvitationDiesWithTheInvitersRight(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	mel := s.member("Mel Leader", "mel@example.org")
	uid := s.approvedGroup(admin, mel, "Study")
	mel.post("/groups/"+uid+"/members", "/groups/"+uid+"/invite", url.Values{"name": {"Nia"}, "email": {"nia@example.org"}, "adult": {"yes"}})
	tok := s.inviteLink("nia@example.org")
	mel.post("/groups/"+uid, "/groups/"+uid+"/archive", nil)
	if r := s.browser().post("/signin", "/invite/"+tok, url.Values{"name": {"Nia"}, "age": {"adult"}}); r.status == http.StatusSeeOther {
		t.Fatal("an invitation into a group archived since worked")
	}
	if r := mel.post("/groups/"+uid+"/members", "/groups/"+uid+"/invite", url.Values{"name": {"Y"}, "email": {"y@example.org"}, "adult": {"yes"}}); !strings.HasSuffix(r.header.Get("Location"), "done=refused") {
		t.Fatal("a leader of an archived group invited (DEC-88)")
	}
}

func TestAdminInvitesWithNoGroup(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	if r := admin.post("/admin/requests", "/admin/invite", url.Values{"name": {"Sue Staff"}, "email": {"sue@example.org"}, "adult": {"yes"}}); r.header.Get("Location") != "/admin/requests?done=invited" {
		t.Fatalf("admin invite: %s", r.header.Get("Location"))
	}
	s.inviteLink("sue@example.org")
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

// An under-18 answer leaves nothing: not even the invitation's typed name
// and address.
func TestUnder18AnswerDeletesTheInvitation(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	mel := s.member("Mel Leader", "mel@example.org")
	uid := s.approvedGroup(admin, mel, "Youth")
	mel.post("/groups/"+uid+"/members", "/groups/"+uid+"/invite", url.Values{"name": {"Teen Name"}, "email": {"teen@example.org"}, "adult": {"yes"}})
	tok := s.inviteLink("teen@example.org")
	s.browser().post("/invite/"+tok, "/invite/"+tok, url.Values{"age": {"young"}})
	if n := s.Count(t, `SELECT count(*) FROM invitations`); n != 0 {
		t.Fatalf("%d invitations kept after an under-18 answer", n)
	}
}

// An invitation whose inviter lost the right to invite shows nothing of the
// group: not on the page, not at the bell, not in a later email (#155).
func TestInvitationClosesWhenTheInviterLosesTheRight(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	mel := s.member("Mel Leader", "mel@example.org")
	uid := s.start(mel, "Secret Circle", "invite_only")
	admin.post("/admin/groups", "/admin/groups/"+uid+"/approve", nil)
	ann := s.member("Ann Newcomer", "ann@example.org")
	mel.post("/groups/"+uid+"/members", "/groups/"+uid+"/members/ask", url.Values{"person": {personUID(s, "ann@example.org")}})
	var inv string
	s.Writer.QueryRow(`SELECT uid FROM invitations`).Scan(&inv)
	if r := ann.get("/notices"); !strings.Contains(r.body, "Secret Circle") {
		t.Fatal("the open invitation should show")
	}
	// An admin makes someone else the leader and Mel a member.
	s.Writer.Exec(`UPDATE memberships SET role = 'member' WHERE person_id = (SELECT id FROM people WHERE email = 'mel@example.org')`)
	if r := ann.get("/notices"); strings.Contains(r.body, "Secret Circle") {
		t.Fatal("the bell still names the group after the inviter lost the right")
	}
	r := ann.get("/invitations/" + inv)
	if strings.Contains(r.body, "Secret Circle") || !strings.Contains(r.body, "no longer open") {
		t.Fatal("the invitation page still shows the group")
	}
	// An emailed invitation sends nothing once its inviter is disabled.
	s.Writer.Exec(`UPDATE memberships SET role = 'leader' WHERE person_id = (SELECT id FROM people WHERE email = 'mel@example.org')`)
	mel.post("/groups/"+uid+"/members", "/groups/"+uid+"/invite", url.Values{"name": {"Zoe"}, "email": {"zoe@example.org"}, "adult": {"yes"}})
	s.Writer.Exec(`UPDATE people SET status = 'disabled' WHERE email = 'mel@example.org'`)
	s.work()
	if _, ok := s.outbox.LatestTo("zoe@example.org"); ok {
		t.Fatal("an invitation was emailed after its inviter was disabled")
	}
}

// Finished invitations, with what was typed into them, are deleted: an
// emailed one a week after it ended.
func TestFinishedInvitationsAreCleanedUp(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	admin.post("/admin/requests", "/admin/invite", url.Values{"name": {"Sue"}, "email": {"sue@example.org"}, "adult": {"yes"}})
	s.work()
	s.runner.Schedule(t.Context())
	s.Clock.Advance(9 * 24 * time.Hour)
	s.work()
	if n := s.Count(t, `SELECT count(*) FROM invitations`); n != 0 {
		t.Fatalf("%d invitations kept after they ended", n)
	}
}
