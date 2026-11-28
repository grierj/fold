package app_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func requestForm(name, email string) url.Values {
	return url.Values{"name": {name}, "email": {email}, "age": {"adult"}}
}

// docs/PLAN.md slice 1 end state: a visitor asks for an account, the admin
// hears at the bell and by email, approves, and the person can sign in.
func TestAccountRequestApproved(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	visitor := s.browser()
	if r := visitor.post("/request", "/request", requestForm("Sam Member", "sam@example.org")); !strings.Contains(r.body, "Thanks for asking") {
		t.Fatalf("answer: %d", r.status)
	}
	s.work()
	if m, ok := s.outbox.LatestTo("sam@example.org"); !ok || !strings.Contains(m.Subject, "We got your request") {
		t.Fatal("the requester was not told their request is waiting")
	}
	if m, ok := s.outbox.LatestTo("pat@cloverdale.example"); !ok || !strings.Contains(m.Text, "Sam Member asked for an account.") {
		t.Fatal("the admin was not emailed")
	}
	if r := admin.get("/"); !strings.Contains(r.body, `<span class="badge">1`) {
		t.Fatal("no count at the admin's bell")
	}
	if r := admin.get("/notices"); !strings.Contains(r.body, "Sam Member asked for an account.") {
		t.Fatal("the notice is not listed")
	}
	// A waiting person cannot sign in.
	visitor.post("/signin", "/signin", url.Values{"email": {"sam@example.org"}})
	s.work()
	if m, _ := s.outbox.LatestTo("sam@example.org"); strings.Contains(m.Subject, "sign-in link") {
		t.Fatal("a waiting person was sent a sign-in link")
	}
	page := admin.get("/admin/requests")
	uid := between(page.body, `/admin/requests/`, `/approve`)
	if r := admin.post("/admin/requests", "/admin/requests/"+uid+"/approve", nil); r.header.Get("Location") != "/admin/requests?done=approved" {
		t.Fatalf("approve: %d %s", r.status, r.header.Get("Location"))
	}
	s.work()
	m, ok := s.outbox.LatestTo("sam@example.org")
	if !ok || !strings.Contains(m.Subject, "is ready") {
		t.Fatalf("approval email: %q", m.Subject)
	}
	// The browser that asked opens the approval link and is signed in at once.
	tok := s.linkIn("sam@example.org")
	if r := visitor.get("/l/" + tok); r.status != http.StatusSeeOther {
		t.Fatalf("approval link: %d", r.status)
	}
	if r := visitor.get("/"); !greeted(r.body, "Sam Member") {
		t.Fatal("not signed in after approval")
	}
	// The admin's notice now reads as approved, worded when read.
	if r := admin.get("/notices"); !strings.Contains(r.body, "account request was approved.") {
		t.Fatal("the notice was not reworded after approval")
	}
}

// The request form answers the same whether or not the address has an
// account; the email carries the hint. It cannot flood an inbox.
func TestAccountRequestAnswersTheSame(t *testing.T) {
	s := newSite(t)
	s.finishSetup()
	b := s.browser()
	known := b.post("/request", "/request", requestForm("Pat Again", "pat@cloverdale.example"))
	unknown := b.post("/request", "/request", requestForm("New Person", "new@example.org"))
	if known.status != unknown.status || noisePattern.ReplaceAllString(known.body, "") != noisePattern.ReplaceAllString(unknown.body, "") {
		t.Fatalf("answers differ:\n%s\n---\n%s", known.body, unknown.body)
	}
	before := s.Count(t, `SELECT count(*) FROM people`)
	s.work()
	told := false
	for _, m := range s.outbox.All() {
		told = told || (m.To == "pat@cloverdale.example" && strings.Contains(m.Subject, "You already have an account"))
	}
	if !told {
		t.Fatal("the account owner was not told")
	}
	if s.Count(t, `SELECT count(*) FROM people`) != before+1 {
		t.Fatal("expected exactly one new waiting person")
	}
	if n := s.Count(t, `SELECT count(*) FROM notices WHERE kind = 'account.requested'`); n != 1 {
		t.Fatalf("%d admin notices, want 1 (the new address only)", n)
	}
	// Requests and sign-in links share one allowance per address.
	sent := len(s.outbox.All())
	for range 6 {
		b.post("/request", "/request", requestForm("Pat Again", "pat@cloverdale.example"))
		b.post("/signin", "/signin", url.Values{"email": {"pat@cloverdale.example"}})
	}
	s.work()
	toPat := 0
	for _, m := range s.outbox.All()[:len(s.outbox.All())-sent] {
		if m.To == "pat@cloverdale.example" {
			toPat++
		}
	}
	if toPat > 3 {
		t.Fatalf("%d emails to one address from one client in an hour", toPat)
	}
}

func TestUnder18IsToldAndNothingKept(t *testing.T) {
	s := newSite(t)
	s.finishSetup()
	form := requestForm("Young Person", "young@example.org")
	form.Set("age", "young")
	r := s.browser().post("/request", "/request", form)
	if !strings.Contains(r.body, "under-18s aren't open yet") {
		t.Fatalf("answer: %s", r.body)
	}
	s.work()
	if s.Count(t, `SELECT count(*) FROM people WHERE email = 'young@example.org'`) != 0 || len(s.outbox.All()) != 1 {
		t.Fatal("something was kept or sent for an under-18")
	}
}

// R27: a declined request is deleted, with its notices, and may ask again.
func TestDeclinedRequestIsDeleted(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	s.browser().post("/request", "/request", requestForm("Sam Member", "sam@example.org"))
	s.work()
	uid := between(admin.get("/admin/requests").body, `/admin/requests/`, `/approve`)
	if r := admin.post("/admin/requests", "/admin/requests/"+uid+"/decline", nil); r.header.Get("Location") != "/admin/requests?done=declined" {
		t.Fatalf("decline: %s", r.header.Get("Location"))
	}
	if s.Count(t, `SELECT count(*) FROM people WHERE email = 'sam@example.org'`) != 0 || s.Count(t, `SELECT count(*) FROM notices`) != 0 {
		t.Fatal("something about a declined person was kept")
	}
	sent := len(s.outbox.All())
	s.work()
	if len(s.outbox.All()) != sent || s.Count(t, `SELECT count(*) FROM links WHERE purpose = 'signin'`) != 0 {
		t.Fatal("a decline sent mail or left a link")
	}
	// The audit row keeps only the random id.
	var details string
	s.Writer.QueryRow(`SELECT subjects || details FROM audit_log WHERE action = 'account.decline'`).Scan(&details)
	if strings.Contains(details, "sam") || strings.Contains(details, "Sam") {
		t.Fatalf("the audit row holds personal data: %s", details)
	}
	// Deciding again is answered calmly.
	if r := admin.post("/admin/requests", "/admin/requests/"+uid+"/approve", nil); r.header.Get("Location") != "/admin/requests?done=gone" {
		t.Fatalf("second decision: %s", r.header.Get("Location"))
	}
}

func TestRequestsAreForAdmins(t *testing.T) {
	s := newSite(t)
	s.finishSetup()
	s.browser().post("/request", "/request", requestForm("Sam Member", "sam@example.org"))
	s.work()
	var uid string
	s.Writer.QueryRow(`SELECT uid FROM people WHERE email = 'sam@example.org'`).Scan(&uid)
	// A signed-in member who is not an admin is answered as if the page
	// did not exist.
	member := s.browser()
	s.Writer.Exec(`INSERT INTO people (uid, name, email, status, created_at, updated_at) VALUES ('m1', 'Mel', 'mel@example.org', 'active', 0, 0)`)
	member.post("/signin", "/signin", url.Values{"email": {"mel@example.org"}})
	s.work()
	tok := s.linkIn("mel@example.org")
	member.get("/l/" + tok)
	if r := member.get("/admin/requests"); r.status != http.StatusNotFound {
		t.Fatalf("a member sees requests: %d", r.status)
	}
	if r := member.post("/", "/admin/requests/"+uid+"/approve", nil); r.status != http.StatusNotFound {
		t.Fatalf("a member approved: %d", r.status)
	}
	stranger := s.browser()
	if r := stranger.get("/admin/requests"); r.status != http.StatusNotFound {
		t.Fatalf("a stranger sees requests: %d", r.status)
	}
	if r := stranger.post("/signin", "/admin/requests/"+uid+"/approve", nil); r.status != http.StatusNotFound {
		t.Fatalf("a stranger approved: %d", r.status)
	}
	if s.Count(t, `SELECT count(*) FROM people WHERE status = 'pending'`) != 1 {
		t.Fatal("the request was decided by a stranger")
	}
}

// A notice never contains an address, opens where it points, and marks
// itself read.
func TestNoticesAreWordedWhenReadAndMarkedRead(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	s.browser().post("/request", "/request", requestForm("Sam Member", "sam@example.org"))
	s.work()
	page := admin.get("/notices")
	if strings.Contains(between(page.body, "<main", "</main>"), "sam@example.org") {
		t.Fatal("a notice shows an email address")
	}
	s.Writer.Exec(`UPDATE people SET name = 'Samuel Member' WHERE email = 'sam@example.org'`)
	page = admin.get("/notices")
	if !strings.Contains(page.body, "Samuel Member asked") {
		t.Fatal("the notice was not worded when read")
	}
	id := regexp.MustCompile(`action="/notices/(\d+)/open"`).FindStringSubmatch(page.body)[1]
	if r := admin.post("/notices", "/notices/"+id+"/open", nil); r.header.Get("Location") != "/admin/requests" {
		t.Fatalf("open: %s", r.header.Get("Location"))
	}
	if r := admin.get("/"); strings.Contains(r.body, `class="badge"`) {
		t.Fatal("the bell still counts an opened notice")
	}
	// Someone else's notice cannot be opened.
	other := s.browser()
	other.post("/request", "/request", requestForm("Other", "other@example.org"))
	if r := other.post("/signin", "/notices/"+id+"/open", nil); r.header.Get("Location") != "/signin" {
		t.Fatalf("a stranger opened a notice: %s", r.header.Get("Location"))
	}
}

func TestOldNoticesAreCleanedUp(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	s.browser().post("/request", "/request", requestForm("Sam Member", "sam@example.org"))
	s.work()
	admin.post("/notices", "/notices/read", nil)
	s.runner.Schedule(t.Context())
	s.Clock.Advance(91 * 24 * time.Hour)
	s.work()
	if n := s.Count(t, `SELECT count(*) FROM notices`); n != 0 {
		t.Fatalf("%d notices left after 90 days read", n)
	}
	// An unread one stays 180 days.
	s.browser().post("/request", "/request", requestForm("Ana Other", "ana@example.org"))
	s.work()
	s.Clock.Advance(170 * 24 * time.Hour)
	s.work()
	if n := s.Count(t, `SELECT count(*) FROM notices`); n != 1 {
		t.Fatalf("an unread notice went before 180 days: %d", n)
	}
	s.Clock.Advance(11 * 24 * time.Hour)
	s.work()
	if n := s.Count(t, `SELECT count(*) FROM notices`); n != 0 {
		t.Fatalf("an unread notice outlived 180 days: %d", n)
	}
}

// Someone who stops being an admin no longer counts admin notices.
func TestBellCountsOnlyWhatTheReaderSees(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	s.Writer.Exec(`INSERT INTO people (uid, name, email, status, is_admin, created_at, updated_at) VALUES ('a2', 'Second Admin', 'a2@example.org', 'active', 1, 0, 0)`)
	s.browser().post("/request", "/request", requestForm("Sam Member", "sam@example.org"))
	s.work()
	if r := admin.get("/"); !strings.Contains(r.body, `class="badge"`) {
		t.Fatal("no count before stepping down")
	}
	s.Writer.Exec(`UPDATE people SET is_admin = 0 WHERE email = 'pat@cloverdale.example'`)
	if r := admin.get("/"); strings.Contains(r.body, `class="badge"`) {
		t.Fatal("the bell counts a notice its reader can no longer see")
	}
}

func between(s, start, end string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return ""
	}
	s = s[i+len(start):]
	j := strings.Index(s, end)
	if j < 0 {
		return ""
	}
	return s[:j]
}

// Before setup is done, a request creates nothing and cannot claim the
// installer's address.
func TestRequestBeforeSetupDoesNothing(t *testing.T) {
	s := newSite(t)
	stranger := s.browser()
	if r := stranger.get("/request"); r.header.Get("Location") != "/setup" {
		t.Fatalf("request form before setup: %d", r.status)
	}
	stranger.post("/signin", "/request", requestForm("Not Pat", "pat@cloverdale.example"))
	s.work()
	if n := s.Count(t, `SELECT count(*) FROM people`); n != 0 {
		t.Fatalf("%d people before setup", n)
	}
	s.finishSetup() // the installer can still use the address
}

// A stranger with endless new addresses cannot bury the admins: the site
// takes 20 requests an hour, and each admin gets at most 10 request emails a
// day; the bell still counts the rest.
func TestRequestFloodIsBounded(t *testing.T) {
	s := newSite(t)
	s.finishSetup()
	for i := range 30 {
		b := s.browser()
		b.post("/request", "/request", requestForm("Person", "p"+strconv.Itoa(i)+"@example.org"))
	}
	s.work()
	if n := s.Count(t, `SELECT count(*) FROM people WHERE status = 'pending'`); n != 20 {
		t.Fatalf("%d waiting requests from one hour, want 20", n)
	}
	toAdmin := 0
	for _, m := range s.outbox.All() {
		if m.To == "pat@cloverdale.example" && strings.Contains(m.Subject, "New account request") {
			toAdmin++
		}
	}
	if toAdmin > 11 {
		t.Fatalf("the admin got %d request emails in a day", toAdmin)
	}
}

// name+tag@ reaches the same inbox as name@, so it shares its allowance.
func TestPlusAddressesShareAnInbox(t *testing.T) {
	s := newSite(t)
	s.finishSetup()
	b := s.browser()
	for i := range 6 {
		b.post("/request", "/request", requestForm("Victim", "victim+"+strconv.Itoa(i)+"@example.org"))
	}
	s.work()
	if n := s.Count(t, `SELECT count(*) FROM people WHERE email LIKE 'victim+%'`); n > 3 {
		t.Fatalf("%d requests reached one inbox from one client", n)
	}
}
