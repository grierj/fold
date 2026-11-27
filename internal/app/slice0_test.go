package app_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"fold/internal/accounts"
	"fold/internal/backup"
	"fold/internal/write"
)

// docs/PLAN.md slice 0, check 1: a stranger without the setup code cannot
// create the admin.
func TestStrangerWithoutCodeCannotSetUp(t *testing.T) {
	s := newSite(t)
	stranger := s.browser()
	if r := stranger.post("/setup", "/setup", setupForm("evil@example.org")); !strings.Contains(r.body, "setup code") {
		t.Fatalf("form without the code: %d", r.status)
	}
	if r := stranger.post("/setup", "/setup/code", url.Values{"code": {"AAAA-BBBB-CCCC-DDDD"}}); !strings.Contains(r.body, "does not match") {
		t.Fatalf("wrong code: %d", r.status)
	}
	if r := stranger.post("/setup", "/setup", setupForm("evil@example.org")); !strings.Contains(r.body, "setup code") {
		t.Fatal("a wrong code unlocked setup")
	}
	s.work()
	if n := s.Count(t, `SELECT count(*) FROM people`); n != 0 {
		t.Fatalf("%d people", n)
	}
	if len(s.outbox.All()) != 0 {
		t.Fatal("mail was sent")
	}
	// A command run without the verified code is refused by policy too.
	err := s.Exec.Run(context.Background(), write.Anonymous(), accounts.StartSetup{In: accounts.SetupInput{Email: "evil@example.org"}})
	if !errors.Is(err, write.ErrNotAllowed) {
		t.Fatalf("StartSetup without code: %v", err)
	}
}

// Check 1, second half: two people finishing setup at once make one admin.
func TestTwoFinishersMakeOneAdmin(t *testing.T) {
	s := newSite(t)
	b := s.browser()
	b.unlock()
	b.post("/setup", "/setup", setupForm("pat@cloverdale.example"))
	s.work()
	tok := s.linkIn("pat@cloverdale.example")
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = s.Exec.Run(context.Background(), write.Anonymous(), &accounts.FinishSetup{Token: tok})
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		} else if !errors.Is(err, write.ErrNotAllowed) {
			t.Fatal(err)
		}
	}
	if ok != 1 || s.Count(t, `SELECT count(*) FROM people WHERE is_admin = 1 AND status = 'active'`) != 1 ||
		s.Count(t, `SELECT count(*) FROM sessions`) != 1 {
		t.Fatalf("%d finished", ok)
	}
}

// Two code holders: the second's answers replace the first's, and the first
// test message's link stops working.
func TestSecondSetupReplacesFirst(t *testing.T) {
	s := newSite(t)
	a, b := s.browser(), s.browser()
	a.unlock()
	b.unlock()
	a.post("/setup", "/setup", setupForm("pat@cloverdale.example"))
	s.work()
	first := s.linkIn("pat@cloverdale.example")
	b.post("/setup?edit=1", "/setup", setupForm("sam@cloverdale.example"))
	s.work()
	if r := a.post("/signin", "/l/"+first, nil); r.status == http.StatusSeeOther {
		t.Fatal("a replaced setup link finished setup")
	}
	second := s.linkIn("sam@cloverdale.example")
	if r := b.post("/l/"+second, "/l/"+second, nil); r.status != http.StatusSeeOther {
		t.Fatalf("second: %d", r.status)
	}
	if n := s.Count(t, `SELECT count(*) FROM people`); n != 1 {
		t.Fatalf("%d people", n)
	}
}

// Check 2: setup cannot finish while the test message is undelivered.
func TestSetupWaitsForTheTestMessage(t *testing.T) {
	s := newSite(t)
	b := s.browser()
	b.unlock()
	s.outbox.Fail = errors.New("connection refused")
	b.post("/setup", "/setup", setupForm("pat@cloverdale.example"))
	// Before the job runs, the token exists only sealed in the job.
	if n := s.Count(t, `SELECT count(*) FROM jobs WHERE secret IS NOT NULL`); n != 1 {
		t.Fatalf("%d sealed jobs", n)
	}
	for range 8 {
		s.work()
		s.Clock.Advance(time.Hour)
	}
	if len(s.outbox.All()) != 0 {
		t.Fatal("sent")
	}
	r := b.get("/setup/sent")
	if !strings.Contains(r.body, "could not be sent") {
		t.Fatalf("status page does not say the message failed:\n%s", r.body)
	}
	if n := s.Count(t, `SELECT count(*) FROM jobs WHERE secret IS NOT NULL`); n != 0 {
		t.Fatal("a failed job kept its token")
	}
	if r := b.get("/"); r.header.Get("Location") != "/setup" {
		t.Fatal("setup finished without the message")
	}
	// Mail works again; sending again finishes setup.
	s.outbox.Fail = nil
	b.post("/setup/sent", "/setup/resend", nil)
	s.work()
	tok := s.linkIn("pat@cloverdale.example")
	if r := b.post("/l/"+tok, "/l/"+tok, nil); r.status != http.StatusSeeOther {
		t.Fatalf("finish: %d", r.status)
	}
	if r := b.get("/"); !strings.Contains(r.body, "Welcome, Pat Admin") {
		t.Fatal("not signed in after setup")
	}
}

// Check 3: a sign-in link works once, stops working when a newer one is
// requested, and does nothing when merely opened.
func TestSignInLink(t *testing.T) {
	s := newSite(t)
	s.finishSetup()
	addr := "pat@cloverdale.example"
	b := s.browser()
	b.post("/signin", "/signin", url.Values{"email": {addr}})
	s.work()
	first := s.linkIn(addr)
	b.post("/signin", "/signin", url.Values{"email": {addr}})
	s.work()
	second := s.linkIn(addr)
	if first == second {
		t.Fatal("same link twice")
	}
	sessions := s.Count(t, `SELECT count(*) FROM sessions`)
	// Opening a link (a mail scanner) changes nothing.
	if r := b.get("/l/" + second); !strings.Contains(r.body, "Sign in to") {
		t.Fatalf("link page: %s", r.body)
	}
	if s.Count(t, `SELECT count(*) FROM sessions`) != sessions || s.Count(t, `SELECT count(*) FROM links WHERE used_at IS NOT NULL AND purpose = 'signin'`) != 0 {
		t.Fatal("opening a link acted")
	}
	// The older link stopped working when the newer was requested.
	if r := b.post("/signin", "/l/"+first, nil); r.status == http.StatusSeeOther || !strings.Contains(r.body, "no longer works") {
		t.Fatalf("old link: %d", r.status)
	}
	if r := b.post("/l/"+second, "/l/"+second, nil); r.status != http.StatusSeeOther {
		t.Fatalf("new link: %d", r.status)
	}
	if r := b.get("/"); !strings.Contains(r.body, "Welcome, Pat Admin") {
		t.Fatal("not signed in")
	}
	// Once.
	other := s.browser()
	if r := other.post("/signin", "/l/"+second, nil); r.status == http.StatusSeeOther {
		t.Fatal("a link worked twice")
	}
	// A day later a fresh link has expired.
	b.post("/signin", "/signin", url.Values{"email": {addr}})
	s.work()
	third := s.linkIn(addr)
	s.Clock.Advance(accounts.LinkLife + time.Minute)
	if r := other.post("/signin", "/l/"+third, nil); r.status == http.StatusSeeOther {
		t.Fatal("an expired link worked")
	}
}

var noisePattern = regexp.MustCompile(`name="csrf" value="[^"]+"|<strong class="wrap-anywhere">[^<]*</strong>|\?v=[0-9a-f]+`)

// Check 4: the sign-in form answers the same for an address with an account
// and one without, and does no database work in the request.
func TestSignInAnswersTheSame(t *testing.T) {
	s := newSite(t)
	s.finishSetup()
	before := s.Count(t, `SELECT count(*) FROM audit_log`)
	b := s.browser()
	known := b.post("/signin", "/signin", url.Values{"email": {"pat@cloverdale.example"}})
	unknown := b.post("/signin", "/signin", url.Values{"email": {"nobody@example.org"}})
	if known.status != unknown.status || noisePattern.ReplaceAllString(known.body, "") != noisePattern.ReplaceAllString(unknown.body, "") {
		t.Fatalf("answers differ:\n%s\n---\n%s", known.body, unknown.body)
	}
	if s.Count(t, `SELECT count(*) FROM audit_log`) != before {
		t.Fatal("the request itself wrote")
	}
	s.work()
	if _, ok := s.outbox.LatestTo("nobody@example.org"); ok {
		t.Fatal("mailed an address with no account")
	}
	if s.Count(t, `SELECT count(*) FROM audit_log`) != before+1 {
		t.Fatal("expected one audit row, for the real account")
	}
	// Past the limit the answer is still the same, and no more mail goes.
	for range 5 {
		r := b.post("/signin", "/signin", url.Values{"email": {"pat@cloverdale.example"}})
		if r.status != known.status {
			t.Fatal("a limit changed the answer")
		}
	}
	s.work()
	if n := s.Count(t, `SELECT count(*) FROM links WHERE purpose = 'signin'`); n != 3 {
		t.Fatalf("%d sign-in links for one address in an hour, want 3", n)
	}
}

// Check 6: back up, change something, restore. The change is gone, sessions
// are ended, and the audit log says a restore happened.
func TestBackupAndRestore(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	ctx := context.Background()
	m, err := backup.Take(ctx, s.Exec, s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Clock.Advance(time.Minute)
	s.Writer.Exec(`UPDATE settings SET value = 'Renamed' WHERE key = 'church.name'`)
	if r := admin.get("/"); !strings.Contains(r.body, "Welcome, Pat") {
		t.Fatal("admin not signed in before restore")
	}
	b := s.browser()
	b.post("/signin", "/signin", url.Values{"email": {"pat@cloverdale.example"}})
	s.work()
	pending := s.linkIn("pat@cloverdale.example")
	if _, err := backup.Restore(ctx, s.Exec, s.Dir, m.Name); err != nil {
		t.Fatal(err)
	}
	var name string
	s.Writer.QueryRow(`SELECT value FROM settings WHERE key = 'church.name'`).Scan(&name)
	if name != "Cloverdale Church of God" {
		t.Fatalf("church name %q after restore", name)
	}
	if r := admin.get("/"); strings.Contains(r.body, "Welcome, Pat") {
		t.Fatal("a session survived the restore")
	}
	if n := s.Count(t, `SELECT count(*) FROM sessions WHERE ended_at IS NULL`); n != 0 {
		t.Fatalf("%d open sessions", n)
	}
	if r := b.post("/signin", "/l/"+pending, nil); r.status == http.StatusSeeOther {
		t.Fatal("a link from before the restore worked")
	}
	var action, details string
	s.Writer.QueryRow(`SELECT action, details FROM audit_log ORDER BY seq DESC LIMIT 1`).Scan(&action, &details)
	if action != "backup.restored" || !strings.Contains(details, m.Name) {
		t.Fatalf("last audit row %s %s", action, details)
	}
	if rep, _ := s.Exec.Verify(ctx); !rep.Intact() {
		t.Fatalf("chain after restore: %+v", rep)
	}
	// The live database before the restore was kept as a backup.
	list, _ := backup.List(s.Dir)
	if len(list) != 2 {
		t.Fatalf("%d backups, want the original and the copy taken before restoring", len(list))
	}
}

func TestPostsNeedTheTokenAndOrigin(t *testing.T) {
	s := newSite(t)
	b := s.browser()
	b.get("/signin")
	if r := b.postRaw("/signin", url.Values{"email": {"pat@example.org"}}, nil); r.status != http.StatusForbidden {
		t.Fatalf("no token: %d", r.status)
	}
	p := b.get("/signin")
	tok := csrfPattern.FindStringSubmatch(p.body)[1]
	form := url.Values{"email": {"pat@example.org"}, "csrf": {tok}}
	if r := b.postRaw("/signin", form, http.Header{"Origin": {"https://evil.example"}}); r.status != http.StatusForbidden {
		t.Fatalf("foreign origin: %d", r.status)
	}
	if r := b.postRaw("/signin", form, http.Header{"Origin": {s.srv.URL}}); r.status != http.StatusOK {
		t.Fatalf("own origin: %d", r.status)
	}
	other := s.browser()
	other.get("/signin")
	if r := other.postRaw("/signin", form, nil); r.status != http.StatusForbidden {
		t.Fatal("one browser's token worked in another")
	}
}

func TestPagesAreNotCachedAndCarryThePolicy(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	for _, p := range []string{"/", "/signin", "/admin/gallery"} {
		r := admin.get(p)
		if r.header.Get("Cache-Control") != "no-store" || !strings.Contains(r.header.Get("Content-Security-Policy"), "script-src 'self'") {
			t.Errorf("%s: %v", p, r.header)
		}
		if strings.Contains(r.body, "<script>") || strings.Contains(r.body, " style=") || strings.Contains(r.body, "onclick") {
			t.Errorf("%s has inline script or style", p)
		}
	}
}

func TestSessionRenewsAndSignOutEndsAll(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	second := s.browser()
	second.post("/signin", "/signin", url.Values{"email": {"pat@cloverdale.example"}})
	s.work()
	tok := s.linkIn("pat@cloverdale.example")
	second.post("/l/"+tok, "/l/"+tok, nil)
	// Six days later a visit renews; the session outlives its first week.
	s.Clock.Advance(6 * 24 * time.Hour)
	admin.get("/")
	s.Clock.Advance(3 * 24 * time.Hour)
	if r := admin.get("/"); !strings.Contains(r.body, "Welcome, Pat") {
		t.Fatal("a renewed session expired")
	}
	if r := second.get("/"); strings.Contains(r.body, "Welcome, Pat") {
		t.Fatal("an unused session outlived its week")
	}
	second.post("/signin", "/signin", url.Values{"email": {"pat@cloverdale.example"}})
	s.work()
	tok = s.linkIn("pat@cloverdale.example")
	second.post("/l/"+tok, "/l/"+tok, nil)
	admin.post("/", "/signout", nil)
	if r := second.get("/"); strings.Contains(r.body, "Welcome, Pat") {
		t.Fatal("signing out in one browser left another signed in")
	}
}

func TestGalleryIsForAdmins(t *testing.T) {
	s := newSite(t)
	s.finishSetup()
	if r := s.browser().get("/admin/gallery"); r.status != http.StatusNotFound {
		t.Fatalf("signed out: %d", r.status)
	}
}

// The daily check after a restore stays quiet: the copy kept by the restore
// and older backups are explained by the "restored" row. A changed row
// still tells every admin.
func TestDailyCheckAfterRestore(t *testing.T) {
	s := newSite(t)
	s.finishSetup()
	ctx := context.Background()
	if err := s.runner.Schedule(ctx); err != nil {
		t.Fatal(err)
	}
	m, err := backup.Take(ctx, s.Exec, s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Clock.Advance(time.Hour)
	backup.Take(ctx, s.Exec, s.Dir)
	s.Clock.Advance(time.Hour)
	if _, err := backup.Restore(ctx, s.Exec, s.Dir, m.Name); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		s.Clock.Advance(24 * time.Hour)
		s.work()
	}
	for _, sent := range s.outbox.All() {
		if strings.Contains(sent.Subject, "check failed") {
			t.Fatal("a restore set off the tamper alarm")
		}
	}
	if n := s.Count(t, `SELECT count(*) FROM audit_log WHERE action = 'backup.taken'`); n < 3 {
		t.Fatalf("nightly backups did not run: %d", n)
	}
	s.Writer.Exec(`DROP TRIGGER audit_log_no_update`)
	s.Writer.Exec(`UPDATE audit_log SET details = '{"x":"1"}' WHERE seq = 2`)
	s.Clock.Advance(24 * time.Hour)
	s.work()
	alarmed := false
	for _, sent := range s.outbox.All() {
		alarmed = alarmed || strings.Contains(sent.Subject, "check failed")
	}
	if !alarmed {
		t.Fatal("a changed row did not reach the admins")
	}
}

func TestMonthlyCheckpointEmail(t *testing.T) {
	s := newSite(t)
	s.finishSetup()
	s.runner.Schedule(context.Background())
	s.Clock.Advance(40 * 24 * time.Hour)
	s.work()
	found := false
	for _, m := range s.outbox.All() {
		if m.Subject == "This month's audit checkpoint" && strings.Contains(m.Text, "Checkpoint: row ") {
			found = true
		}
	}
	if !found {
		t.Fatal("no checkpoint email")
	}
}

// A replaced setup link leads back to setup, not to sign-in: before setup
// is done there is no account to sign in to.
func TestOldSetupLinkLeadsBackToSetup(t *testing.T) {
	s := newSite(t)
	b := s.browser()
	b.unlock()
	b.post("/setup", "/setup", setupForm("pat@cloverdale.example"))
	s.work()
	old := s.linkIn("pat@cloverdale.example")
	b.post("/setup/sent", "/setup/resend", nil)
	s.work()
	r := b.get("/l/" + old)
	if !strings.Contains(r.body, "no longer works") || !strings.Contains(r.body, `href="/setup"`) || strings.Contains(r.body, "Get a new sign-in link") {
		t.Fatalf("old setup link page:\n%s", r.body)
	}
}

// Any page renews the session, not only the home page (R8).
func TestAnyPageRenewsTheSession(t *testing.T) {
	s := newSite(t)
	admin := s.finishSetup()
	s.Clock.Advance(6 * 24 * time.Hour)
	rows := s.Count(t, `SELECT count(*) FROM audit_log`)
	admin.get("/no-such-page")
	if s.Count(t, `SELECT count(*) FROM audit_log`) != rows {
		t.Fatal("renewing a session wrote an audit row (R19)")
	}
	s.Clock.Advance(3 * 24 * time.Hour)
	if r := admin.get("/"); !strings.Contains(r.body, "Welcome, Pat") {
		t.Fatal("visiting another page did not renew the session")
	}
}
