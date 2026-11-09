package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fold/internal/audit"
	"fold/internal/testdb"
	"fold/models"

	"gorm.io/gorm"
)

// ---------------------------------------------------------------------------
// T07B-1b: the attempt lifecycle, settle, the actor seal and the receipt sweep.
// Every route test goes through the real router, as an admin with a cookie
// session and a CSRF token. App.clock (useAttemptClock) is the test clock: the process's
// monotonic time, advanced by hand.
// ---------------------------------------------------------------------------

type attemptScene struct {
	t *testing.T
	// app is the App whose routes run. In a proxied scene it is the copy that
	// reaches the database through pgproxy (booted like a process), and direct
	// is the shared handle straight to the server: the control and observer
	// that a restore must also recycle. In a plain scene they are the same.
	app      *App
	direct   *gorm.DB
	rig      *ctxRig
	admin    *models.User
	c        *apiClient
	restarts atomic.Int32
	clock    atomic.Int64 // nanoseconds since the process "started"
}

func newAttemptScene(t *testing.T) *attemptScene {
	t.Helper()
	app := testApp(t)
	s := &attemptScene{t: t, app: app, direct: app.DB}
	app.requestRestart = func() { s.restarts.Add(1) }
	app.useAttemptClock(func() time.Duration { return time.Duration(s.clock.Load()) })
	s.admin = newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	s.c = loginAPI(t, app, "admin", "pw")
	return s
}

// newProxiedAttemptScene is newAttemptScene with the routes served by an App
// whose database handle goes through pgproxy. That App is booted like a
// process (attempt key, restore barrier floor), so its floor, ticket turn and
// restart recorder are the ones a real request would use.
func newProxiedAttemptScene(t *testing.T) *attemptScene {
	t.Helper()
	direct := testApp(t)
	admin := newTestUser(t, direct, "admin", "pw", models.RoleAdmin)
	rig := newCtxRigFor(t, direct)
	s := &attemptScene{t: t, app: rig.papp, direct: direct.DB, rig: rig, admin: admin}
	bootFence(t, rig.papp)
	rig.papp.requestRestart = func() { s.restarts.Add(1) }
	rig.papp.useAttemptClock(func() time.Duration { return time.Duration(s.clock.Load()) })
	s.c = loginAPI(t, rig.papp, "admin", "pw")
	return s
}

func (s *attemptScene) advance(d time.Duration) { s.clock.Add(int64(d)) }

func (s *attemptScene) target(name string) *models.User {
	return newTestUser(s.t, s.app, name, "pw", models.RoleMember)
}

func (s *attemptScene) mintWith(c *apiClient, kind string, subject uint) *httptest.ResponseRecorder {
	return c.do("POST", "/api/v1/admin/operation-attempts", map[string]any{"kind": kind, "subjectUserId": subject}, true)
}

func (s *attemptScene) mint(subject uint) string {
	s.t.Helper()
	w := s.mintWith(s.c, attemptKindAccountDelete, subject)
	if w.Code != http.StatusOK {
		s.t.Fatalf("mint: %d %s", w.Code, w.Body.String())
	}
	var out attemptMintJSON
	decodeJSON(s.t, w, &out)
	if len(out.AttemptID) != 80 {
		s.t.Fatalf("mint: id %q is not 80 characters", out.AttemptID)
	}
	return out.AttemptID
}

func (s *attemptScene) deleteAs(c *apiClient, id string, target uint) *httptest.ResponseRecorder {
	var body any
	if id != "" {
		body = map[string]any{"attemptId": id}
	}
	return c.do("DELETE", "/api/v1/admin/users/"+itoa(target), body, true)
}

func (s *attemptScene) del(id string, target uint) *httptest.ResponseRecorder {
	return s.deleteAs(s.c, id, target)
}

func (s *attemptScene) settleAs(c *apiClient, id string, subject uint) *httptest.ResponseRecorder {
	return c.do("POST", "/api/v1/admin/operation-attempts/settle",
		map[string]any{"attemptId": id, "kind": attemptKindAccountDelete, "subjectUserId": subject}, true)
}

func (s *attemptScene) settle(id string, subject uint) *httptest.ResponseRecorder {
	return s.settleAs(s.c, id, subject)
}

func (s *attemptScene) settled(id string, subject uint) attemptSettleJSON {
	s.t.Helper()
	w := s.settle(id, subject)
	if w.Code != http.StatusOK {
		s.t.Fatalf("settle: %d %s", w.Code, w.Body.String())
	}
	var out attemptSettleJSON
	decodeJSON(s.t, w, &out)
	return out
}

func codeOf(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var out struct {
		Code string `json:"code"`
	}
	decodeJSON(t, w, &out)
	return out.Code
}

// state is the ledger entry of an id the scene minted for the subject.
func (s *attemptScene) state(id string, subject uint) attemptState {
	s.t.Helper()
	nonce, _, ok := s.app.openAttempt(id, attemptKindAccountDelete, s.admin.ID, subject)
	if !ok {
		s.t.Fatal("the id does not open for the tuple it was minted for")
	}
	st, _ := s.app.attempts.ledger.look(nonce)
	return st
}

func (s *attemptScene) counter() int64 {
	s.t.Helper()
	var n int64
	if err := s.app.DB.Raw("SELECT value::bigint FROM settings WHERE key = 'restore_fence'").Scan(&n).Error; err != nil {
		s.t.Fatal(err)
	}
	return n
}

func (s *attemptScene) deleted(id uint) bool {
	s.t.Helper()
	var n int64
	if err := s.app.DB.Raw("SELECT count(*) FROM users WHERE id = ? AND deleted_at IS NOT NULL", id).Scan(&n).Error; err != nil {
		s.t.Fatal(err)
	}
	return n == 1
}

func (s *attemptScene) receipts() int64 {
	s.t.Helper()
	var n int64
	if err := s.app.DB.Raw("SELECT count(*) FROM admin_operation_attempts").Scan(&n).Error; err != nil {
		s.t.Fatal(err)
	}
	return n
}

func wantSettle(t *testing.T, got attemptSettleJSON, result string, subjectDeleted *bool) {
	t.Helper()
	if got.Result != result {
		t.Fatalf("settle result = %q, want %q", got.Result, result)
	}
	switch {
	case subjectDeleted == nil && got.SubjectDeleted != nil:
		t.Fatalf("settle carries subjectDeleted = %v, want none", *got.SubjectDeleted)
	case subjectDeleted != nil && (got.SubjectDeleted == nil || *got.SubjectDeleted != *subjectDeleted):
		t.Fatalf("settle subjectDeleted = %v, want %v", got.SubjectDeleted, *subjectDeleted)
	}
}

func ptr[T any](v T) *T { return &v }

// ---------------------------------------------------------------------------
// The id
// ---------------------------------------------------------------------------

func TestAttemptIDCapability(t *testing.T) {
	app := testApp(t)
	id, err := app.mintAttempt(attemptKindAccountDelete, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(id) != 80 {
		t.Fatalf("id %q is %d characters, want 80", id, len(id))
	}
	if _, _, ok := app.openAttempt(id, attemptKindAccountDelete, 1, 2); !ok {
		t.Fatal("an id does not open for the tuple it was minted for")
	}
	for name, open := range map[string]func() bool{
		"another kind":    func() bool { _, _, ok := app.openAttempt(id, "legacy_reason_save", 1, 2); return ok },
		"another actor":   func() bool { _, _, ok := app.openAttempt(id, attemptKindAccountDelete, 3, 2); return ok },
		"another subject": func() bool { _, _, ok := app.openAttempt(id, attemptKindAccountDelete, 1, 3); return ok },
		"too short":       func() bool { _, _, ok := app.openAttempt(id[:79], attemptKindAccountDelete, 1, 2); return ok },
		"too long":        func() bool { _, _, ok := app.openAttempt(id+"0", attemptKindAccountDelete, 1, 2); return ok },
		"not hex":         func() bool { _, _, ok := app.openAttempt("g"+id[1:], attemptKindAccountDelete, 1, 2); return ok },
		"empty":           func() bool { _, _, ok := app.openAttempt("", attemptKindAccountDelete, 1, 2); return ok },
		"a changed nonce": func() bool {
			flipped := []byte(id)
			flipped[3] ^= 1 // '0'-'9' and 'a'-'f' differ in the low bit within hex digits
			if flipped[3] == id[3] {
				return false
			}
			_, _, ok := app.openAttempt(string(flipped), attemptKindAccountDelete, 1, 2)
			return ok
		},
		"a changed mint time": func() bool {
			flipped := []byte(id)
			flipped[40] ^= 1
			_, _, ok := app.openAttempt(string(flipped), attemptKindAccountDelete, 1, 2)
			return ok
		},
	} {
		if open() {
			t.Errorf("%s: the id opened", name)
		}
	}
	if up := strings.ToUpper(id); up != id {
		if _, _, ok := app.openAttempt(up, attemptKindAccountDelete, 1, 2); ok {
			t.Error("an upper-case id opened; ids are lower-case hex only")
		}
	}
	// Another process (another key) cannot open it.
	other := &App{}
	if _, _, ok := other.openAttempt(id, attemptKindAccountDelete, 1, 2); ok {
		t.Error("an id opened under another process's key")
	}
	again, _ := app.mintAttempt(attemptKindAccountDelete, 1, 2)
	if again == id {
		t.Error("two mints gave the same id")
	}
}

// ---------------------------------------------------------------------------
// The ledger
// ---------------------------------------------------------------------------

func nonceOf(i int) [16]byte {
	var n [16]byte
	n[0], n[1], n[2], n[3] = byte(i), byte(i>>8), byte(i>>16), byte(i>>24)
	n[15] = 0xaa
	return n
}

func TestAttemptLedgerRules(t *testing.T) {
	var l attemptLedger
	n := nonceOf(1)
	if r := l.claim(n, 0, time.Minute); r != submitOK {
		t.Fatalf("a fresh id: %v, want submitOK", r)
	}
	if st, done := l.look(n); st != attemptRunning || done == nil {
		t.Fatalf("after a claim: %v, done %v", st, done)
	}
	if r := l.claim(n, 0, time.Minute); r != submitClosed {
		t.Fatalf("the same id again: %v, want submitClosed", r)
	}
	if _, err := l.settleTransition(n, 0, time.Minute); err != errSettleRunning {
		t.Fatalf("settle of a running id: %v, want errSettleRunning", err)
	}
	done, ok := l.end(n, attemptApplied)
	if !ok || done == nil {
		t.Fatal("a running entry should end once and hand back its channel")
	}
	if _, ok := l.end(n, attemptRolledBack); ok {
		t.Fatal("an entry ended twice")
	}
	if st, _ := l.look(n); st != attemptApplied {
		t.Fatalf("an ended entry changed: %v", st)
	}
	if st, err := l.settleTransition(n, 0, time.Minute); err != nil || st != attemptApplied {
		t.Fatalf("settle of an ended id: %v, %v, want applied read-only", st, err)
	}
	// The closed rule holds at any age, before any age rule.
	if r := l.claim(n, 0, 50*time.Minute); r != submitClosed {
		t.Fatalf("an old ended id: %v, want submitClosed", r)
	}

	// Past the 10-minute submit window an absent id is expired and fenced while
	// it can still be settled; past the 60-minute settle window it records
	// nothing.
	late := nonceOf(2)
	if r := l.claim(late, 0, 11*time.Minute); r != submitExpired {
		t.Fatalf("an id first sent at 11 minutes: %v, want submitExpired", r)
	}
	if st, _ := l.look(late); st != attemptFenced {
		t.Fatalf("an expired id inside the settle window: %v, want fenced", st)
	}
	if r := l.claim(late, 0, 12*time.Minute); r != submitClosed {
		t.Fatalf("a fenced id sent again: %v, want submitClosed", r)
	}
	veryLate := nonceOf(3)
	if r := l.claim(veryLate, 0, 61*time.Minute); r != submitExpired {
		t.Fatalf("an id first sent at 61 minutes: %v, want submitExpired", r)
	}
	if st, _ := l.look(veryLate); st != attemptAbsent {
		t.Fatalf("an id past the settle window was recorded: %v", st)
	}

	// Settle fences an id nobody claimed, and a later claim finds it closed.
	fresh := nonceOf(4)
	if st, err := l.settleTransition(fresh, 5*time.Minute, 6*time.Minute); err != nil || st != attemptFenced {
		t.Fatalf("settle of an absent id: %v, %v, want fenced", st, err)
	}
	if r := l.claim(fresh, 5*time.Minute, 7*time.Minute); r != submitClosed {
		t.Fatalf("a claim after settle fenced the id: %v, want submitClosed", r)
	}

	// Entries are pruned after mint + 61 minutes, on a later use.
	if r := l.claim(nonceOf(5), 70*time.Minute, 71*time.Minute); r != submitOK {
		t.Fatal("a fresh claim after a long gap should work")
	}
	if st, _ := l.look(n); st != attemptAbsent {
		t.Fatalf("an entry 71 minutes old survived pruning: %v", st)
	}
}

func TestAttemptLedgerCap(t *testing.T) {
	var l attemptLedger
	for i := 0; i < attemptLedgerCap; i++ {
		if r := l.claim(nonceOf(i+10), 0, 0); r != submitOK {
			t.Fatalf("claim %d: %v", i, r)
		}
	}
	if r := l.claim(nonceOf(1), 0, 0); r != submitBusy {
		t.Fatalf("a claim at the cap: %v, want submitBusy", r)
	}
	if st, _ := l.look(nonceOf(1)); st != attemptAbsent {
		t.Fatal("a claim refused at the cap recorded an entry")
	}
	if r := l.claim(nonceOf(2), 0, 11*time.Minute); r != submitExpired {
		t.Fatalf("an expired id at the cap: %v, want submitExpired", r)
	}
	if st, _ := l.look(nonceOf(2)); st != attemptAbsent {
		t.Fatal("an expired id at the cap recorded an entry")
	}
	if _, err := l.settleTransition(nonceOf(3), 0, 0); err != errLedgerFull {
		t.Fatalf("settle of an absent id at the cap: %v, want errLedgerFull", err)
	}
	// An existing entry is still read.
	if st, err := l.settleTransition(nonceOf(10), 0, 0); err != errSettleRunning {
		t.Fatalf("settle of a running id at the cap: %v, %v", st, err)
	}
}

// ledgerMutexProblems parses a ledger source: every function that locks a mu
// defers the unlock at once and holds no database call, channel operation,
// select, sleep or context wait.
func ledgerMutexProblems(src string) []string {
	tree, err := parser.ParseFile(token.NewFileSet(), "ledger.go", src, 0)
	if err != nil {
		return []string{"cannot parse: " + err.Error()}
	}
	banned := map[string]bool{
		"Raw": true, "Exec": true, "Scan": true, "Create": true, "Find": true, "First": true, "Where": true,
		"Transaction": true, "Begin": true, "Commit": true, "Rollback": true, "Sleep": true, "Done": true,
		"Err": true, "Deadline": true, "Value": true, "Wait": true, "close": true,
	}
	var bad []string
	for _, d := range tree.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		lockAt := -1
		for i, s := range fd.Body.List {
			es, ok := s.(*ast.ExprStmt)
			if !ok {
				continue
			}
			if c, ok := es.X.(*ast.CallExpr); ok {
				if sel, ok := c.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Lock" {
					if inner, ok := sel.X.(*ast.SelectorExpr); ok && inner.Sel.Name == "mu" {
						lockAt = i
					}
				}
			}
		}
		if lockAt < 0 {
			continue
		}
		deferred := false
		if lockAt+1 < len(fd.Body.List) {
			if ds, ok := fd.Body.List[lockAt+1].(*ast.DeferStmt); ok {
				if sel, ok := ds.Call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Unlock" {
					deferred = true
				}
			}
		}
		if !deferred {
			bad = append(bad, fd.Name.Name+": the unlock is not deferred right after the lock")
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SendStmt:
				bad = append(bad, fd.Name.Name+": a channel send under the mutex")
			case *ast.SelectStmt:
				bad = append(bad, fd.Name.Name+": a select under the mutex")
			case *ast.GoStmt:
				bad = append(bad, fd.Name.Name+": a goroutine under the mutex")
			case *ast.UnaryExpr:
				if x.Op == token.ARROW {
					bad = append(bad, fd.Name.Name+": a channel receive under the mutex")
				}
			case *ast.CallExpr:
				name := ""
				switch f := x.Fun.(type) {
				case *ast.SelectorExpr:
					name = f.Sel.Name
				case *ast.Ident:
					name = f.Name
				}
				if banned[name] {
					bad = append(bad, fd.Name.Name+": calls "+name+" under the mutex")
				}
			}
			return true
		})
	}
	return bad
}

func TestLedgerMutexHoldsNoWait(t *testing.T) {
	b, err := os.ReadFile("admin_operation_attempts.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range ledgerMutexProblems(string(b)) {
		t.Error(v)
	}
	if !strings.Contains(string(b), "func (l *attemptLedger) claim") {
		t.Fatal("the ledger's claim method moved; this guard must keep reading it")
	}
	const good = "package main\nfunc (l *L) f() int {\n\tl.mu.Lock()\n\tdefer l.mu.Unlock()\n\treturn l.m[1]\n}\n"
	if bad := ledgerMutexProblems(good); len(bad) != 0 {
		t.Fatalf("the positive fixture failed: %v", bad)
	}
	for name, body := range map[string]string{
		"a database call":    "\ttx.Raw(\"x\")\n",
		"a channel receive":  "\t<-done\n",
		"a channel send":     "\tdone <- struct{}{}\n",
		"a select":           "\tselect {}\n",
		"a sleep":            "\ttime.Sleep(1)\n",
		"a context wait":     "\t_ = ctx.Done()\n",
		"a close":            "\tclose(done)\n",
		"a goroutine":        "\tgo f()\n",
		"a transaction call": "\tdb.Transaction(nil)\n",
	} {
		src := "package main\nfunc (l *L) f() int {\n\tl.mu.Lock()\n\tdefer l.mu.Unlock()\n" + body + "\treturn l.m[1]\n}\n"
		if len(ledgerMutexProblems(src)) == 0 {
			t.Errorf("%s under the mutex: the guard accepted it", name)
		}
	}
	notDeferred := "package main\nfunc (l *L) f() int {\n\tl.mu.Lock()\n\tx := l.m[1]\n\tl.mu.Unlock()\n\treturn x\n}\n"
	if len(ledgerMutexProblems(notDeferred)) == 0 {
		t.Error("an unlock that is not deferred right away: the guard accepted it")
	}
}

// ---------------------------------------------------------------------------
// TestAttemptLifecycle: the real DELETE and settle routes, the test clock.
// ---------------------------------------------------------------------------

func TestAttemptLifecycle(t *testing.T) {
	t.Run("s1 commit and acknowledgment, replay, receipt deleted, after 60 minutes", func(t *testing.T) {
		s := newAttemptScene(t)
		a := s.target("a")
		id := s.mint(a.ID)
		before := s.counter()
		if w := s.del(id, a.ID); w.Code != http.StatusOK {
			t.Fatalf("delete: %d %s", w.Code, w.Body.String())
		}
		if !s.deleted(a.ID) || s.receipts() != 1 || s.state(id, a.ID) != attemptApplied {
			t.Fatal("a committed deletion leaves the tombstone, one receipt and an applied entry")
		}
		after := s.counter()
		if after != before+1 || s.app.restoreFenceFloor() != after {
			t.Fatalf("the ticket: counter %d -> %d, floor %d; want one ticket and the floor on it", before, after, s.app.restoreFenceFloor())
		}
		// A replay is attempt_closed and touches nothing.
		if w := s.del(id, a.ID); w.Code != http.StatusConflict || codeOf(t, w) != "attempt_closed" {
			t.Fatalf("replay: %d %s, want 409 attempt_closed", w.Code, w.Body.String())
		}
		if s.counter() != after || s.receipts() != 1 {
			t.Error("a replay changed the counter or the receipts")
		}
		wantSettle(t, s.settled(id, a.ID), settleApplied, nil)
		if err := s.app.DB.Exec("DELETE FROM admin_operation_attempts").Error; err != nil {
			t.Fatal(err)
		}
		wantSettle(t, s.settled(id, a.ID), settleApplied, nil)
		s.advance(61 * time.Minute)
		wantSettle(t, s.settled(id, a.ID), settleHistoryUnavailable, nil)
	})

	t.Run("a well-formed id for another tuple", func(t *testing.T) {
		s := newAttemptScene(t)
		a, b := s.target("a"), s.target("b")
		id := s.mint(a.ID)
		// Another subject, by submission and by settle.
		if w := s.del(id, b.ID); w.Code != http.StatusConflict || codeOf(t, w) != "attempt_expired" {
			t.Fatalf("submission with another subject: %d %s, want 409 attempt_expired", w.Code, w.Body.String())
		}
		wantSettle(t, s.settled(id, b.ID), settleHistoryUnavailable, nil)
		// Another kind never opens (the route refuses the kind itself).
		w := s.c.do("POST", "/api/v1/admin/operation-attempts/settle",
			map[string]any{"attemptId": id, "kind": "legacy_reason_save", "subjectUserId": a.ID}, true)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("settle with another kind: %d, want 400", w.Code)
		}
		// Another admin cannot submit or settle it.
		newTestUser(t, s.app, "admin2", "pw", models.RoleAdmin)
		c2 := loginAPI(t, s.app, "admin2", "pw")
		if w := s.deleteAs(c2, id, a.ID); w.Code != http.StatusConflict || codeOf(t, w) != "attempt_expired" {
			t.Fatalf("another admin's submission: %d %s, want 409 attempt_expired", w.Code, w.Body.String())
		}
		var got attemptSettleJSON
		decodeJSON(t, s.settleAs(c2, id, a.ID), &got)
		wantSettle(t, got, settleHistoryUnavailable, nil)
		if s.deleted(a.ID) || s.state(id, a.ID) != attemptAbsent {
			t.Fatal("a refused tuple ran something or recorded the id")
		}
	})

	t.Run("a new process cannot open an old id", func(t *testing.T) {
		s := newAttemptScene(t)
		a := s.target("a")
		id := s.mint(a.ID)
		restarted := &App{DB: s.app.DB, Store: s.app.Store, Config: s.app.Config, Uploads: s.app.Uploads, Mailer: s.app.Mailer}
		bootFence(t, restarted)
		c := loginAPI(t, restarted, "admin", "pw")
		before := s.counter()
		w := c.do("DELETE", "/api/v1/admin/users/"+itoa(a.ID), map[string]any{"attemptId": id}, true)
		if w.Code != http.StatusConflict || codeOf(t, w) != "attempt_expired" {
			t.Fatalf("submission to a new process: %d %s, want 409 attempt_expired", w.Code, w.Body.String())
		}
		w = c.do("POST", "/api/v1/admin/operation-attempts/settle",
			map[string]any{"attemptId": id, "kind": attemptKindAccountDelete, "subjectUserId": a.ID}, true)
		var got attemptSettleJSON
		decodeJSON(t, w, &got)
		wantSettle(t, got, settleHistoryUnavailable, nil)
		if s.counter() != before || s.deleted(a.ID) {
			t.Error("a new process ran or ticketed an old id")
		}
	})

	t.Run("s2/s3 settle before submission fences the id", func(t *testing.T) {
		s := newAttemptScene(t)
		a := s.target("a")
		id := s.mint(a.ID)
		before := s.counter()
		wantSettle(t, s.settled(id, a.ID), settleNotApplied, nil)
		if s.state(id, a.ID) != attemptFenced {
			t.Fatal("settle should leave the unclaimed id fenced")
		}
		if s.counter() != before+1 {
			t.Fatalf("settle is a fenced transaction: counter %d -> %d, want one ticket", before, s.counter())
		}
		for _, wait := range []time.Duration{5 * time.Minute, 25 * time.Minute} {
			s.advance(wait)
			if w := s.del(id, a.ID); w.Code != http.StatusConflict || codeOf(t, w) != "attempt_closed" {
				t.Fatalf("submission after settle fenced it: %d %s, want 409 attempt_closed", w.Code, w.Body.String())
			}
		}
		if s.deleted(a.ID) {
			t.Fatal("a fenced id deleted the account")
		}
		// After pruning (mint + 61 minutes) the id is expired and settles as history_unavailable.
		s.advance(40 * time.Minute)
		if w := s.del(id, a.ID); w.Code != http.StatusConflict || codeOf(t, w) != "attempt_expired" {
			t.Fatalf("submission after pruning: %d %s, want 409 attempt_expired", w.Code, w.Body.String())
		}
		wantSettle(t, s.settled(id, a.ID), settleHistoryUnavailable, nil)
	})

	t.Run("s4 an id first submitted late", func(t *testing.T) {
		s := newAttemptScene(t)
		a := s.target("a")
		id := s.mint(a.ID)
		s.advance(11 * time.Minute)
		if w := s.del(id, a.ID); w.Code != http.StatusConflict || codeOf(t, w) != "attempt_expired" {
			t.Fatalf("an id first sent at 11 minutes: %d %s, want 409 attempt_expired", w.Code, w.Body.String())
		}
		if s.deleted(a.ID) || s.receipts() != 0 {
			t.Fatal("an expired id ran")
		}
		wantSettle(t, s.settled(id, a.ID), settleNotApplied, nil)
	})

	t.Run("s4 resubmitted after 10 minutes", func(t *testing.T) {
		s := newAttemptScene(t)
		a := s.target("a")
		id := s.mint(a.ID)
		if w := s.del(id, a.ID); w.Code != http.StatusOK {
			t.Fatalf("delete: %d %s", w.Code, w.Body.String())
		}
		s.advance(11 * time.Minute)
		if w := s.del(id, a.ID); w.Code != http.StatusConflict || codeOf(t, w) != "attempt_closed" {
			t.Fatalf("applied id resubmitted at 11 minutes: %d %s, want 409 attempt_closed", w.Code, w.Body.String())
		}
		wantSettle(t, s.settled(id, a.ID), settleApplied, nil)
	})

	t.Run("a rolled-back attempt settles as not applied", func(t *testing.T) {
		s := newAttemptScene(t)
		a := s.target("a")
		// The admin cannot delete themselves: a refusal after the claim.
		id := s.mint(s.admin.ID)
		w := s.del(id, s.admin.ID)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("self-delete: %d %s, want 400", w.Code, w.Body.String())
		}
		if s.state(id, s.admin.ID) != attemptRolledBack {
			t.Fatal("a refusal after the claim must end the id rolled back")
		}
		if w := s.del(id, s.admin.ID); w.Code != http.StatusConflict || codeOf(t, w) != "attempt_closed" {
			t.Fatalf("resubmitting a rolled-back id: %d %s, want 409 attempt_closed", w.Code, w.Body.String())
		}
		wantSettle(t, s.settled(id, s.admin.ID), settleNotApplied, nil)
		_ = a
	})

	t.Run("s5 two presses, one admin", func(t *testing.T) {
		s := newAttemptScene(t)
		a := s.target("a")
		first, second := s.mint(a.ID), s.mint(a.ID)
		if w := s.del(first, a.ID); w.Code != http.StatusOK {
			t.Fatalf("first delete: %d %s", w.Code, w.Body.String())
		}
		// The second press arrives after the account is gone: the id is not
		// the ledger's yet, so the answer is today's 404.
		if w := s.del(second, a.ID); w.Code != http.StatusNotFound {
			t.Fatalf("second delete: %d %s, want 404", w.Code, w.Body.String())
		}
		// Its settle: the id was never claimed, so it is fenced, and the account
		// is deleted by someone else (no actor or attribution field).
		got := s.settled(second, a.ID)
		wantSettle(t, got, settleNotApplied, ptr(true))
		raw := s.settle(second, a.ID).Body.String()
		for _, leak := range []string{"actor", "attempt", "deletedBy", "attribution"} {
			if strings.Contains(raw, leak) {
				t.Errorf("settle answer %s names %q", raw, leak)
			}
		}
	})
}

// A deletion that fails inside its transaction ends the id rolled back, and the
// two refusals before the claim record nothing.
func TestDefinitiveResponsesEndTheAttempt(t *testing.T) {
	t.Run("the gate held until the budget ends", func(t *testing.T) {
		setAdminDeleteBounds(t, time.Second, 100*time.Millisecond)
		s := newAttemptScene(t)
		a := s.target("a")
		id := s.mint(a.ID)
		release, _ := holdAdvisory(t, s.app.DB, contactLockID)
		defer release()
		w := s.del(id, a.ID)
		if w.Code != http.StatusServiceUnavailable || codeOf(t, w) != "delete_timed_out" {
			t.Fatalf("delete: %d %s, want 503 delete_timed_out", w.Code, w.Body.String())
		}
		if s.state(id, a.ID) != attemptRolledBack {
			t.Fatal("delete_timed_out must end the id rolled back")
		}
		if w := s.del(id, a.ID); w.Code != http.StatusConflict || codeOf(t, w) != "attempt_closed" {
			t.Fatalf("resubmitting: %d %s, want 409 attempt_closed", w.Code, w.Body.String())
		}
	})

	t.Run("a sole leader needs a successor", func(t *testing.T) {
		s := newAttemptScene(t)
		a := s.target("a")
		ac := loginAPI(t, s.app, "a", "pw")
		createGroupAs(t, ac, "Sole group", "public")
		id := s.mint(a.ID)
		before := s.counter()
		w := s.del(id, a.ID)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("delete: %d %s, want 400 (a successor is needed)", w.Code, w.Body.String())
		}
		if s.state(id, a.ID) != attemptRolledBack {
			t.Fatal("a validation 400 after the claim must end the id rolled back")
		}
		if s.counter() != before || s.receipts() != 0 {
			t.Error("a rolled-back attempt moved the counter or left a receipt")
		}
	})

	t.Run("a missing target answers 404 and records nothing", func(t *testing.T) {
		s := newAttemptScene(t)
		missing := uint(987654)
		id := s.mint(missing)
		w := s.del(id, missing)
		if w.Code != http.StatusNotFound {
			t.Fatalf("delete of a missing account: %d %s, want 404", w.Code, w.Body.String())
		}
		if s.state(id, missing) != attemptAbsent {
			t.Fatal("a 404 before the claim must leave the id absent")
		}
	})

	t.Run("a restore detected before the claim's transaction", func(t *testing.T) {
		s := newAttemptScene(t)
		a := s.target("a")
		id := s.mint(a.ID)
		s.app.noteRestoreDetected()
		w := s.del(id, a.ID)
		if w.Code != http.StatusServiceUnavailable || codeOf(t, w) != "restore_detected" {
			t.Fatalf("delete after a detected restore: %d %s, want 503 restore_detected", w.Code, w.Body.String())
		}
		if s.state(id, a.ID) != attemptRolledBack {
			t.Fatal("restore_detected must end the id rolled back")
		}
		if w := s.mintWith(s.c, attemptKindAccountDelete, a.ID); w.Code != http.StatusServiceUnavailable || codeOf(t, w) != "restore_detected" {
			t.Fatalf("mint after a detected restore: %d %s, want 503 restore_detected", w.Code, w.Body.String())
		}
		if w := s.settle(id, a.ID); w.Code != http.StatusServiceUnavailable || codeOf(t, w) != "restore_detected" {
			t.Fatalf("settle after a detected restore: %d %s, want 503 restore_detected", w.Code, w.Body.String())
		}
	})
}

// ---------------------------------------------------------------------------
// TestOutcomeClassification: how each commit result reaches the ledger and
// what settle then says. Nothing here turns an applied deletion into
// not_applied.
// ---------------------------------------------------------------------------

func TestOutcomeClassification(t *testing.T) {
	const claim = "idle_in_transaction_session_timeout"
	cases := []struct {
		name      string
		fault     testdb.Faults
		committed bool
		settles   string
	}{
		{"commit forwarded, client closed before the reply", testdb.Faults{Match: claim, Commit: testdb.CommitForwardThenClose}, true, settleApplied},
		{"commit never forwarded", testdb.Faults{Match: claim, Commit: testdb.CommitCloseUnforwarded}, false, settleHistoryUnavailable},
		{"reply held past the budget", testdb.Faults{Match: claim, Commit: testdb.CommitHoldReply}, true, settleApplied},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setAdminDeleteBounds(t, 4*time.Second, 300*time.Millisecond)
			app := testApp(t)
			admin := newTestUser(t, app, "admin", "pw", models.RoleAdmin)
			target := newTestUser(t, app, "target", "pw", models.RoleMember)
			r := newCtxRigFor(t, app)
			c := loginAPI(t, r.papp, "admin", "pw")
			var clock atomic.Int64
			r.papp.useAttemptClock(func() time.Duration { return time.Duration(clock.Load()) })
			mintW := c.do("POST", "/api/v1/admin/operation-attempts", map[string]any{"kind": attemptKindAccountDelete, "subjectUserId": target.ID}, true)
			var minted attemptMintJSON
			decodeJSON(t, mintW, &minted)
			r.proxy.Arm(tc.fault)
			w := c.do("DELETE", "/api/v1/admin/users/"+itoa(target.ID), map[string]any{"attemptId": minted.AttemptID}, true)
			if w.Code != http.StatusInternalServerError || codeOf(t, w) != "delete_outcome_unknown" {
				t.Fatalf("delete: %d %s, want 500 delete_outcome_unknown", w.Code, w.Body.String())
			}
			nonce, _, _ := r.papp.openAttempt(minted.AttemptID, attemptKindAccountDelete, admin.ID, target.ID)
			if st, _ := r.papp.attempts.ledger.look(nonce); st != attemptUnknown {
				t.Fatalf("an unknown COMMIT ends the id %v, want unknown", st)
			}
			floor := r.papp.restoreFenceFloor()
			if tc.committed {
				deadline := time.Now().Add(5 * time.Second)
				for !r.deletedUser(target.ID) && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
				}
			} else {
				r.awaitReleased(r.pidOf(claim), 10*time.Second)
			}
			if r.deletedUser(target.ID) != tc.committed {
				t.Fatalf("deleted = %v, want %v", r.deletedUser(target.ID), tc.committed)
			}
			var counter int64
			if err := app.DB.Raw("SELECT value::bigint FROM settings WHERE key = 'restore_fence'").Scan(&counter).Error; err != nil {
				t.Fatal(err)
			}
			if r.papp.restoreFenceFloor() != floor {
				t.Error("an unknown COMMIT raised the floor")
			}
			sw := c.do("POST", "/api/v1/admin/operation-attempts/settle",
				map[string]any{"attemptId": minted.AttemptID, "kind": attemptKindAccountDelete, "subjectUserId": target.ID}, true)
			var got attemptSettleJSON
			decodeJSON(t, sw, &got)
			wantSettle(t, got, tc.settles, nil)
			if got.Result == settleNotApplied {
				t.Fatal("an unknown commit must never settle as not_applied")
			}
			// s4: resubmitted after 10 minutes the id is still closed, whatever the
			// target's state, without a transaction.
			clock.Store(int64(11 * time.Minute))
			if w := c.do("DELETE", "/api/v1/admin/users/"+itoa(target.ID), map[string]any{"attemptId": minted.AttemptID}, true); w.Code != http.StatusConflict || codeOf(t, w) != "attempt_closed" {
				t.Fatalf("resubmission at 11 minutes: %d %s, want 409 attempt_closed", w.Code, w.Body.String())
			}
			if tc.committed {
				// The receipt later removed by an undetected restore: the unknown
				// attempt can no longer be shown to have applied.
				if err := app.DB.Exec("DELETE FROM admin_operation_attempts").Error; err != nil {
					t.Fatal(err)
				}
				sw := c.do("POST", "/api/v1/admin/operation-attempts/settle",
					map[string]any{"attemptId": minted.AttemptID, "kind": attemptKindAccountDelete, "subjectUserId": target.ID}, true)
				var again attemptSettleJSON
				decodeJSON(t, sw, &again)
				wantSettle(t, again, settleHistoryUnavailable, nil)
			}
		})
	}
}

// A deadline that expires during the retry attempt ends the id rolled back and
// settles not_applied.
func TestDeletionDeadline(t *testing.T) {
	t.Run("an already-expired budget refuses before BEGIN", func(t *testing.T) {
		setAdminDeleteBounds(t, 100*time.Millisecond, 200*time.Millisecond)
		app := testApp(t)
		newTestUser(t, app, "admin", "pw", models.RoleAdmin)
		target := newTestUser(t, app, "target", "pw", models.RoleMember)
		r := newCtxRigFor(t, app)
		c := loginAPI(t, r.papp, "admin", "pw")
		before := len(r.proxy.Events())
		w := c.do("DELETE", "/api/v1/admin/users/"+itoa(target.ID), nil, true)
		if w.Code != http.StatusServiceUnavailable || codeOf(t, w) != "delete_timed_out" {
			t.Fatalf("delete: %d %s, want 503 delete_timed_out", w.Code, w.Body.String())
		}
		for _, e := range r.proxy.Events()[before:] {
			if strings.Contains(strings.ToUpper(e.SQL), "BEGIN") || isRestoreFenceStatement(e.SQL) ||
				strings.Contains(e.SQL, "set_config") || strings.Contains(e.SQL, "pg_advisory_xact_lock") {
				t.Errorf("a statement reached the server for an expired budget: %q", e.SQL)
			}
		}
		if r.deletedUser(target.ID) {
			t.Fatal("an expired budget deleted the account")
		}
	})
}

// ---------------------------------------------------------------------------
// The actor seal
// ---------------------------------------------------------------------------

func TestCurrentAdminInFacts(t *testing.T) {
	app := testApp(t)
	u := newTestUser(t, app, "actor", "pw", models.RoleAdmin)
	me := &models.User{ID: u.ID, SessionVersion: 5, LogoutVersion: 7}
	for _, status := range []string{models.UserActive, models.UserDisabled, models.UserInvited, models.UserPendingConsent} {
		for _, hasHash := range []bool{true, false} {
			for _, deleted := range []bool{false, true} {
				for _, sessionOK := range []bool{true, false} {
					for _, logoutOK := range []bool{true, false} {
						for _, role := range []string{models.RoleAdmin, models.RoleMember} {
							sv, lv := 5, 7
							if !sessionOK {
								sv = 6
							}
							if !logoutOK {
								lv = 8
							}
							hash := "NULL"
							if hasHash {
								hash = "'x'"
							}
							del := "NULL"
							if deleted {
								del = "now()"
							}
							q := fmt.Sprintf("UPDATE users SET status = ?, role = ?, password_hash = %s, deleted_at = %s, session_version = ?, logout_version = ? WHERE id = ?", hash, del)
							if err := app.DB.Exec(q, status, role, sv, lv, u.ID).Error; err != nil {
								t.Fatal(err)
							}
							err := app.DB.Transaction(func(tx *gorm.DB) error { return currentAdminIn(tx, me, nil) })
							signedIn := !deleted && status == models.UserActive && hasHash && sessionOK && logoutOK
							var want error
							switch {
							case !signedIn:
								want = errSecuritySession
							case role != models.RoleAdmin:
								want = errActorForbidden
							}
							if err != want {
								t.Errorf("status %s hash %v deleted %v session %v logout %v role %s: %v, want %v", status, hasHash, deleted, sessionOK, logoutOK, role, err, want)
							}
						}
					}
				}
			}
		}
	}
	// pred decides after the admin check, on the same row.
	if err := app.DB.Exec("UPDATE users SET status = 'active', role = 'admin', password_hash = 'x', deleted_at = NULL, session_version = 5, logout_version = 7 WHERE id = ?", u.ID).Error; err != nil {
		t.Fatal(err)
	}
	err := app.DB.Transaction(func(tx *gorm.DB) error {
		return currentAdminIn(tx, me, func(*models.User) bool { return false })
	})
	if err != errActorForbidden {
		t.Errorf("a refused predicate: %v, want errActorForbidden", err)
	}
	// A missing row is 401.
	err = app.DB.Transaction(func(tx *gorm.DB) error { return currentAdminIn(tx, &models.User{ID: 999999}, nil) })
	if err != errSecuritySession {
		t.Errorf("no row: %v, want errSecuritySession", err)
	}
}

// On the real routes, a password-bearing admin passes the deletion's and
// settle's seals in one attempt each, and a hash set NULL after the early read
// is refused 401 with nothing deleted.
func TestCurrentAdminInOnTheRoutes(t *testing.T) {
	s := newAttemptScene(t)
	a, b := s.target("a"), s.target("b")
	s.app.hooks.sealAttempts = &atomic.Int64{}
	id := s.mint(a.ID)
	if w := s.del(id, a.ID); w.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if n := s.app.hooks.sealAttempts.Load(); n != 1 {
		t.Fatalf("the deletion's seal ran %d times, want 1", n)
	}
	s.app.hooks.sealAttempts.Store(0)
	wantSettle(t, s.settled(id, a.ID), settleApplied, nil)
	if n := s.app.hooks.sealAttempts.Load(); n != 1 {
		t.Fatalf("settle's seal ran %d times, want 1", n)
	}

	// The password goes between the early read and the seal.
	var cleared atomic.Bool
	s.app.hooks.deleteBeforeSeal = func(tx *gorm.DB) {
		if cleared.Swap(true) {
			return
		}
		if err := s.app.DB.Exec("UPDATE users SET password_hash = NULL WHERE id = ?", s.admin.ID).Error; err != nil {
			t.Errorf("clear the hash: %v", err)
		}
	}
	id2 := s.mint(b.ID)
	// The UPDATE from a second connection waits for nothing the deletion holds
	// except the actor's row (FOR SHARE is taken only at the seal), so it
	// commits before the seal runs.
	w := s.del(id2, b.ID)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("delete with a cleared hash: %d %s, want 401", w.Code, w.Body.String())
	}
	if s.deleted(b.ID) || s.state(id2, b.ID) != attemptRolledBack {
		t.Fatal("a refused seal must delete nothing and end the id rolled back")
	}
}

// TestActorSealBusy: a raw connection holds the admin's row FOR NO KEY UPDATE.
func TestActorSealBusy(t *testing.T) {
	// Three attempts need the first to start within busyRetryWindow of the
	// last; a deletion slowed by a loaded database would otherwise end the loop
	// after one. The window's own bound is judged by S1 at the production window.
	oldWindow := busyRetryWindow
	busyRetryWindow = 30 * time.Second
	defer func() { busyRetryWindow = oldWindow }()
	s := newAttemptScene(t)
	a := s.target("a")
	s.app.hooks.sealAttempts = &atomic.Int64{}
	hold, _ := beginOwnedTx(t, s.app.DB)
	if err := hold.Exec("SELECT id FROM users WHERE id = ? FOR NO KEY UPDATE", s.admin.ID).Error; err != nil {
		t.Fatal(err)
	}
	held := true
	release := func() {
		if held {
			held = false
			hold.Rollback()
		}
	}
	defer release()

	id := s.mint(a.ID)
	w := s.del(id, a.ID)
	if w.Code != http.StatusConflict || codeOf(t, w) != "admin_account_busy" {
		t.Fatalf("delete under the hold: %d %s, want 409 admin_account_busy", w.Code, w.Body.String())
	}
	if n := s.app.hooks.sealAttempts.Load(); n != 3 {
		t.Fatalf("sealed attempts = %d, want 3", n)
	}
	if s.deleted(a.ID) || s.state(id, a.ID) != attemptRolledBack || s.receipts() != 0 {
		t.Fatal("a busy seal must delete nothing, leave no receipt and end the id rolled back")
	}

	// Settle under the same hold: 3 sealed transactions, no fence, checking.
	id2 := s.mint(a.ID)
	s.app.hooks.sealAttempts.Store(0)
	wantSettle(t, s.settled(id2, a.ID), settleChecking, nil)
	if n := s.app.hooks.sealAttempts.Load(); n != 3 {
		t.Fatalf("settle sealed %d times under the hold, want 3", n)
	}
	if s.state(id2, a.ID) != attemptAbsent {
		t.Fatal("a busy settle must not fence the id")
	}
	release()
	wantSettle(t, s.settled(id2, a.ID), settleNotApplied, nil)
	if s.state(id2, a.ID) != attemptFenced {
		t.Fatal("settle after the release should fence the id")
	}
}

// A logout committed between the middleware and the transaction refuses 401
// at the seal with nothing deleted; settle does the same without disclosure.
func TestSettleAuthorization(t *testing.T) {
	revokers := map[string]string{
		"logout everywhere": "UPDATE users SET logout_version = logout_version + 1 WHERE id = ?",
		"session bump":      "UPDATE users SET session_version = session_version + 1 WHERE id = ?",
		"demotion":          "UPDATE users SET role = 'member' WHERE id = ?",
		"disable":           "UPDATE users SET status = 'disabled' WHERE id = ?",
		"soft deletion":     "UPDATE users SET deleted_at = now() WHERE id = ?",
	}
	for name, revoke := range revokers {
		t.Run(name, func(t *testing.T) {
			s := newAttemptScene(t)
			a := s.target("a")
			// A second admin keeps the system valid when the first loses power.
			newTestUser(t, s.app, "admin2", "pw", models.RoleAdmin)
			id := s.mint(a.ID)
			// (a) committed after the middleware authenticated the request and
			// before the settle transaction.
			s.app.hooks.settleBeforeTx = func() {
				if err := s.app.DB.Exec(revoke, s.admin.ID).Error; err != nil {
					t.Errorf("revoke: %v", err)
				}
			}
			w := s.settle(id, a.ID)
			if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
				t.Fatalf("settle after %s: %d %s, want 401 or 403", name, w.Code, w.Body.String())
			}
			for _, field := range []string{"result", "subjectDeleted"} {
				if strings.Contains(w.Body.String(), field) {
					t.Errorf("a refused settle names %q: %s", field, w.Body.String())
				}
			}
			nonce, _, _ := s.app.openAttempt(id, attemptKindAccountDelete, s.admin.ID, a.ID)
			if st, _ := s.app.attempts.ledger.look(nonce); st != attemptAbsent {
				t.Fatalf("a refused settle changed the ledger: %v", st)
			}
		})
	}

	t.Run("deletion refuses 401 after a logout between middleware and transaction", func(t *testing.T) {
		s := newAttemptScene(t)
		a := s.target("a")
		id := s.mint(a.ID)
		var once atomic.Bool
		s.app.hooks.deleteBeforeSeal = func(*gorm.DB) {
			if once.Swap(true) {
				return
			}
			if err := s.app.DB.Exec("UPDATE users SET logout_version = logout_version + 1 WHERE id = ?", s.admin.ID).Error; err != nil {
				t.Errorf("logout: %v", err)
			}
		}
		w := s.del(id, a.ID)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("delete: %d %s, want 401", w.Code, w.Body.String())
		}
		if s.deleted(a.ID) || s.state(id, a.ID) != attemptRolledBack {
			t.Fatal("nothing may be deleted and the id ends rolled back")
		}
	})
}

// ---------------------------------------------------------------------------
// The receipt sweep
// ---------------------------------------------------------------------------

func TestAttemptReceiptSweep(t *testing.T) {
	app := testApp(t)
	oldBatch, oldBudget := attemptSweepBatch, attemptSweepBudget
	t.Cleanup(func() { attemptSweepBatch, attemptSweepBudget = oldBatch, oldBudget })

	if err := app.DB.Exec(`INSERT INTO admin_operation_attempts (attempt_id, created_at)
		SELECT decode(lpad(to_hex(g), 32, '0'), 'hex'), now() - interval '25 hours' FROM generate_series(1, 12000) g`).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Exec(`INSERT INTO admin_operation_attempts (attempt_id, created_at)
		SELECT decode(lpad(to_hex(g), 32, '0'), 'hex'), now() - interval '1 hour' FROM generate_series(100001, 100003) g`).Error; err != nil {
		t.Fatal(err)
	}
	count := func() int64 {
		var n int64
		if err := app.DB.Raw("SELECT count(*) FROM admin_operation_attempts").Scan(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}
	// Interrupted after one committed batch: the budget is spent at once.
	attemptSweepBudget = -time.Second
	err := app.sweepAttemptReceipts(t.Context(), time.Now())
	if err == nil {
		t.Fatal("a sweep that ran out of budget with rows left should ask to run again (jobs.Later)")
	}
	if n := count(); n != 12003-5000 {
		t.Fatalf("after one committed batch %d rows remain, want %d", n, 12003-5000)
	}
	// The rerun deletes the rest and leaves the younger rows; a second run is safe.
	attemptSweepBudget = 2 * time.Minute
	for i := 0; i < 2; i++ {
		if err := app.sweepAttemptReceipts(t.Context(), time.Now()); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
		if n := count(); n != 3 {
			t.Fatalf("after run %d %d rows remain, want the 3 younger ones", i+1, n)
		}
	}
}

// ---------------------------------------------------------------------------
// An ordinary deletion still records the user.deleted audit row once.
// ---------------------------------------------------------------------------

func TestDeletionWithoutAnAttemptStillTakesATicket(t *testing.T) {
	s := newAttemptScene(t)
	a := s.target("a")
	before := s.counter()
	if w := s.del("", a.ID); w.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if s.counter() != before+1 || s.receipts() != 0 {
		t.Fatalf("an old bundle's delete: counter %d -> %d, receipts %d; want one ticket and no receipt", before, s.counter(), s.receipts())
	}
	if n := contactCount(t, s.app, &models.AuditLog{}, "action = ? AND target_id = ?", audit.UserDeleted, a.ID); n != 1 {
		t.Fatalf("user.deleted rows = %d, want 1", n)
	}
}

// s2: an attempt held at the gate. A second submission of its id is refused at
// once, without the database, and a settle waits outside every lock for the
// attempt's own result.
func TestAttemptSettleWaitsForARunningAttempt(t *testing.T) {
	s := newAttemptScene(t)
	a := s.target("a")
	id := s.mint(a.ID)
	release, holderPID := holdAdvisory(t, s.app.DB, contactLockID)
	defer release()

	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- s.del(id, a.ID) }()
	waitFor(t, 10*time.Second, "the deletion to claim its id", func() bool { return s.state(id, a.ID) == attemptRunning })

	// B submits the same id: attempt_closed at once.
	c2 := loginAPI(t, s.app, "admin", "pw")
	if w := s.deleteAs(c2, id, a.ID); w.Code != http.StatusConflict || codeOf(t, w) != "attempt_closed" {
		t.Fatalf("a second submission of a running id: %d %s, want 409 attempt_closed", w.Code, w.Body.String())
	}

	settled := make(chan attemptSettleJSON, 1)
	go func() {
		w := s.settleAs(c2, id, a.ID)
		var out attemptSettleJSON
		decodeJSON(t, w, &out)
		settled <- out
	}()
	select {
	case got := <-settled:
		t.Fatalf("settle answered %q while the attempt was running", got.Result)
	case <-time.After(500 * time.Millisecond):
	}
	// Settle waits with no transaction: no backend of its own is blocked behind
	// the gate holder except the deletion's.
	var waiting int64
	if err := s.app.DB.Raw(`SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted AND ` + advisoryInThisDatabase).Scan(&waiting).Error; err != nil {
		t.Fatal(err)
	}
	if waiting != 1 {
		t.Fatalf("%d backends wait for the gate behind backend %d, want only the deletion's", waiting, holderPID)
	}
	release()
	select {
	case w := <-first:
		if w.Code != http.StatusOK {
			t.Fatalf("the deletion: %d %s", w.Code, w.Body.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the deletion never finished")
	}
	select {
	case got := <-settled:
		wantSettle(t, got, settleApplied, nil)
	case <-time.After(20 * time.Second):
		t.Fatal("settle never answered")
	}
}

// ---------------------------------------------------------------------------
// TestSettleAuthorization, the cases beyond (a): the revocation kinds are each
// an UPDATE of the actor's users row.
// ---------------------------------------------------------------------------

var revocationKinds = map[string]string{
	"logout everywhere": "UPDATE users SET logout_version = logout_version + 1 WHERE id = ?",
	"session bump":      "UPDATE users SET session_version = session_version + 1 WHERE id = ?",
	"demotion":          "UPDATE users SET role = 'member' WHERE id = ?",
	"disable":           "UPDATE users SET status = 'disabled' WHERE id = ?",
	"soft deletion":     "UPDATE users SET deleted_at = now() WHERE id = ?",
}

// advisoryInThisDatabase restricts a pg_locks query to the current database.
// pg_locks is cluster-wide, and the sharded suite runs several databases on one
// server, each with its own contact gate (an advisory lock): without this a
// waiter or holder of another shard's gate is counted as ours.
const advisoryInThisDatabase = "database = (SELECT oid FROM pg_database WHERE datname = current_database())"

func advisoryWaiters(t testing.TB, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.Raw("SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted AND " + advisoryInThisDatabase).Scan(&n).Error; err != nil {
		t.Errorf("advisory waiters: %v", err)
	}
	return n
}

// (b): the revocation commits while settle waits at the exclusive gate behind a
// parked holder. Settle answers 401 or 403 with no outcome field and the ledger
// is unchanged (absent stays absent), so a fresh session of a re-promoted actor
// is not blocked by it.
func TestSettleAuthorizationBehindAParkedHolder(t *testing.T) {
	for name, revoke := range revocationKinds {
		t.Run(name, func(t *testing.T) {
			s := newAttemptScene(t)
			a := s.target("a")
			id := s.mint(a.ID)
			release, _ := holdAdvisory(t, s.app.DB, contactLockID)
			defer release()
			s.c.holdNotices = true
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- s.settle(id, a.ID) }()
			waitFor(t, 10*time.Second, "settle waiting for the gate", func() bool { return advisoryWaiters(t, s.app.DB) == 1 })
			if err := s.app.DB.Exec(revoke, s.admin.ID).Error; err != nil {
				t.Fatal(err)
			}
			release()
			w := awaitRec(t, done, "settle")
			if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
				t.Fatalf("settle after %s: %d %s, want 401 or 403", name, w.Code, w.Body.String())
			}
			for _, field := range []string{"result", "subjectDeleted"} {
				if strings.Contains(w.Body.String(), field) {
					t.Errorf("a refused settle names %q: %s", field, w.Body.String())
				}
			}
			if s.state(id, a.ID) != attemptAbsent {
				t.Fatal("a refused settle changed the ledger")
			}
			if err := s.app.DB.Exec("UPDATE users SET role = 'admin', status = 'active', deleted_at = NULL WHERE id = ?", s.admin.ID).Error; err != nil {
				t.Fatal(err)
			}
			fresh := loginAPI(t, s.app, "admin", "pw")
			if w := s.deleteAs(fresh, id, a.ID); w.Code != http.StatusOK {
				t.Fatalf("a fresh session of the re-promoted admin: %d %s, want 200 (the refused settle must not block the id)", w.Code, w.Body.String())
			}
		})
	}
}

// (c): the revoking UPDATE starts after currentAdminIn and before the fence. It
// is shown waiting behind settle's FOR SHARE until settle commits; settle
// discloses its outcome; then the revocation commits.
func TestSettleAuthorizationRevokedBeforeTheFence(t *testing.T) {
	for name, revoke := range revocationKinds {
		t.Run(name, func(t *testing.T) {
			s := newAttemptScene(t)
			a := s.target("a")
			id := s.mint(a.ID)
			revoked := make(chan error, 1)
			var once atomic.Bool
			s.app.hooks.settleBeforeFence = func(tx *gorm.DB) {
				if once.Swap(true) {
					return
				}
				pid := backendPID(tx)
				go func() { revoked <- s.app.DB.Exec(revoke, s.admin.ID).Error }()
				waiterBlockedBy(t, s.app.DB, pid, "UPDATE")
				select {
				case <-revoked:
					t.Error("the revocation finished while settle held the actor's row")
				default:
				}
			}
			wantSettle(t, s.settled(id, a.ID), settleNotApplied, nil)
			select {
			case err := <-revoked:
				if err != nil {
					t.Fatalf("the revocation: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the revocation never committed after settle did")
			}
		})
	}
}

// (d): the settle backend is terminated after the fence and before COMMIT. The
// answer is checking with no outcome, and the ledger shows fenced (the stated
// residual: it can stop only this admin's own unsubmitted id).
func TestSettleBackendTerminatedAfterTheFence(t *testing.T) {
	s := newAttemptScene(t)
	a := s.target("a")
	id := s.mint(a.ID)
	var once atomic.Bool
	s.app.hooks.settleAfterFence = func(tx *gorm.DB) {
		if once.Swap(true) {
			return
		}
		pid := backendPID(tx)
		if err := s.app.DB.Exec("SELECT pg_terminate_backend(?)", pid).Error; err != nil {
			t.Errorf("terminate: %v", err)
			return
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			var n int64
			if err := s.app.DB.Raw("SELECT count(*) FROM pg_stat_activity WHERE pid = ?", pid).Scan(&n).Error; err == nil && n == 0 {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Errorf("backend %d did not exit", pid)
	}
	w := s.settle(id, a.ID)
	if w.Code != http.StatusOK {
		t.Fatalf("settle: %d %s", w.Code, w.Body.String())
	}
	var got attemptSettleJSON
	decodeJSON(t, w, &got)
	wantSettle(t, got, settleChecking, nil)
	if s.state(id, a.ID) != attemptFenced {
		t.Fatalf("ledger = %v, want fenced", s.state(id, a.ID))
	}
	wantSettle(t, s.settled(id, a.ID), settleNotApplied, nil)
	if s.deleted(a.ID) {
		t.Fatal("a settle deleted the account")
	}
}

// (f) settle: the revoking UPDATE is in flight (uncommitted) when the seal
// runs, for every revocation kind. The seal answers 55P03 with no fence and no
// ledger change; after the revocation commits the retried seal refuses (401, or
// 403 for a demotion), still with no fence and no outcome field.
func TestSettleSealBusyThenRevoked(t *testing.T) {
	for name, revoke := range revocationKinds {
		t.Run(name, func(t *testing.T) {
			app := testApp(t)
			newTestUser(t, app, "admin", "pw", models.RoleAdmin)
			target := newTestUser(t, app, "target", "pw", models.RoleMember)
			r := newCtxRigFor(t, app)
			c := loginAPI(t, r.papp, "admin", "pw")
			var admin models.User
			if err := app.DB.Where("email = ?", testEmail("admin")).First(&admin).Error; err != nil {
				t.Fatal(err)
			}
			mint := c.do("POST", "/api/v1/admin/operation-attempts", map[string]any{"kind": attemptKindAccountDelete, "subjectUserId": target.ID}, true)
			var minted attemptMintJSON
			decodeJSON(t, mint, &minted)
			hold, _ := beginOwnedTx(t, app.DB)
			if err := hold.Exec(revoke, admin.ID).Error; err != nil {
				t.Fatal(err)
			}
			var fenced atomic.Int32
			r.papp.hooks.settleBeforeFence = func(*gorm.DB) { fenced.Add(1) }
			commit := make(chan struct{})
			go func() {
				defer close(commit)
				deadline := time.Now().Add(10 * time.Second)
				for time.Now().Before(deadline) {
					events := r.proxy.Events()
					rolled := 0
					for i, e := range events {
						if e.Kind == testdb.KindStatement && strings.Contains(e.SQL, "FOR SHARE NOWAIT") {
							for _, e2 := range events[i+1:] {
								if e2.Conn == e.Conn {
									if e2.Kind == testdb.KindRollback || strings.EqualFold(strings.TrimSpace(e2.SQL), "ROLLBACK") {
										rolled++
									}
									break
								}
							}
						}
					}
					if rolled >= 1 {
						break
					}
					time.Sleep(2 * time.Millisecond)
				}
				if err := hold.Commit().Error; err != nil {
					t.Errorf("commit the revocation: %v", err)
				}
			}()
			w := c.do("POST", "/api/v1/admin/operation-attempts/settle",
				map[string]any{"attemptId": minted.AttemptID, "kind": attemptKindAccountDelete, "subjectUserId": target.ID}, true)
			<-commit
			if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
				t.Fatalf("settle: %d %s, want 401 or 403 once the revocation committed", w.Code, w.Body.String())
			}
			for _, field := range []string{"result", "subjectDeleted"} {
				if strings.Contains(w.Body.String(), field) {
					t.Errorf("a refused settle names %q: %s", field, w.Body.String())
				}
			}
			nonce, _, _ := r.papp.openAttempt(minted.AttemptID, attemptKindAccountDelete, admin.ID, target.ID)
			if st, _ := r.papp.attempts.ledger.look(nonce); st != attemptAbsent || fenced.Load() != 0 {
				t.Fatalf("ledger %v, fence seam entries %d: a busy then refused seal must leave both untouched", st, fenced.Load())
			}
		})
	}
}

// settle's own actor read is its seal: a password removed after the middleware
// (settleBeforeTx) is refused 401 with the ledger unchanged and no outcome
// field. A deletion's version of this is in TestCurrentAdminInOnTheRoutes.
func TestSettleRefusesAPasswordRemovedAfterTheMiddleware(t *testing.T) {
	s := newAttemptScene(t)
	a := s.target("a")
	id := s.mint(a.ID)
	s.app.hooks.sealAttempts = &atomic.Int64{}
	var fenced atomic.Int32
	s.app.hooks.settleBeforeFence = func(*gorm.DB) { fenced.Add(1) }
	s.app.hooks.settleBeforeTx = func() {
		if err := s.app.DB.Exec("UPDATE users SET password_hash = NULL WHERE id = ?", s.admin.ID).Error; err != nil {
			t.Errorf("clear the hash: %v", err)
		}
	}
	w := s.settle(id, a.ID)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("settle: %d %s, want 401", w.Code, w.Body.String())
	}
	for _, field := range []string{"result", "subjectDeleted"} {
		if strings.Contains(w.Body.String(), field) {
			t.Errorf("a refused settle names %q: %s", field, w.Body.String())
		}
	}
	if s.state(id, a.ID) != attemptAbsent || fenced.Load() != 0 || s.app.hooks.sealAttempts.Load() != 1 {
		t.Fatalf("ledger %v, fence entries %d, sealed %d: want absent, 0, 1", s.state(id, a.ID), fenced.Load(), s.app.hooks.sealAttempts.Load())
	}
}

// Compatible holders never make the seal busy: a FOR KEY SHARE holder (what
// notify.WithRecipients takes) or a FOR SHARE holder (what lockHostUsers takes)
// on the admin's row leaves both the deletion and settle at one sealed attempt.
func TestActorSealCompatibleLocks(t *testing.T) {
	for _, mode := range []string{"KEY SHARE", "SHARE"} {
		t.Run(mode, func(t *testing.T) {
			s := newAttemptScene(t)
			a, b := s.target("a"), s.target("b")
			s.app.hooks.sealAttempts = &atomic.Int64{}
			hold, _ := beginOwnedTx(t, s.app.DB)
			if err := hold.Exec("SELECT id FROM users WHERE id = ? FOR "+mode, s.admin.ID).Error; err != nil {
				t.Fatal(err)
			}
			id := s.mint(a.ID)
			if w := s.del(id, a.ID); w.Code != http.StatusOK {
				t.Fatalf("delete under a FOR %s holder: %d %s, want 200", mode, w.Code, w.Body.String())
			}
			if n := s.app.hooks.sealAttempts.Load(); n != 1 {
				t.Fatalf("the deletion's seal ran %d times under FOR %s, want 1", n, mode)
			}
			s.app.hooks.sealAttempts.Store(0)
			idB := s.mint(b.ID)
			wantSettle(t, s.settled(idB, b.ID), settleNotApplied, nil)
			if n := s.app.hooks.sealAttempts.Load(); n != 1 {
				t.Fatalf("settle's seal ran %d times under FOR %s, want 1", n, mode)
			}
		})
	}
}

// The busy window closes after the backoff too (T07B1B-R1). The retry
// schedule is: attempt 1, 25 ms, attempt 2, 100 ms, attempt 3 (production lets
// all three run because they finish inside 2 s). With the window shortened to
// 100 ms, attempt 2 still starts (about 30 ms in) but the second backoff ends
// at about 130 ms, after the window: no transaction may begin then. The proof
// is by time, not by count: every transaction settle begins must start inside
// the window measured from the request, and at least two must have run so the
// post-backoff path was really reached. Without the recheck after the backoff
// attempt 3 begins at about 130 ms and its BEGIN lies outside the window.
func TestSettleBusyWindowClosesAfterBackoff(t *testing.T) {
	const window = 100 * time.Millisecond
	old := busyRetryWindow
	busyRetryWindow = window
	t.Cleanup(func() { busyRetryWindow = old })
	s := newProxiedAttemptScene(t)
	a := s.target("a")
	id := s.mint(a.ID)
	s.app.hooks.sealAttempts = &atomic.Int64{}
	hold, _ := beginOwnedTx(t, s.direct)
	if err := hold.Exec("SELECT id FROM users WHERE id = ? FOR NO KEY UPDATE", s.admin.ID).Error; err != nil {
		t.Fatal(err)
	}
	mark := len(s.rig.proxy.Events())
	began := time.Now()
	wantSettle(t, s.settled(id, a.ID), settleChecking, nil)
	var starts []time.Duration
	for _, e := range s.rig.proxy.Events()[mark:] {
		if e.Kind == testdb.KindStatement && strings.Contains(e.SQL, "pg_advisory_xact_lock") {
			starts = append(starts, e.Time.Sub(began))
		}
	}
	if len(starts) < 2 || int64(len(starts)) != s.app.hooks.sealAttempts.Load() {
		t.Fatalf("settle began %d transactions (sealed %d times) at %v; want at least 2 so the retry path ran, each sealed once", len(starts), s.app.hooks.sealAttempts.Load(), starts)
	}
	for _, at := range starts {
		if at >= window {
			t.Fatalf("a transaction began %s after the request, outside the %s busy window (starts %v)", at, window, starts)
		}
	}
	if s.state(id, a.ID) != attemptAbsent {
		t.Fatal("a busy settle must not fence the id")
	}
}

// R5: an unknown COMMIT of the settle transaction itself. The fence was recorded
// in memory before the COMMIT, so the answer is checking (never an outcome), the
// ledger shows fenced, and the floor is not raised; settling again then answers
// not_applied.
func TestSettleUnknownCommit(t *testing.T) {
	for name, fault := range map[string]testdb.CommitFault{
		"forwarded, client closed before the reply": testdb.CommitForwardThenClose,
		"closed without forwarding":                 testdb.CommitCloseUnforwarded,
	} {
		t.Run(name, func(t *testing.T) {
			s := newProxiedAttemptScene(t)
			a := s.target("a")
			id := s.mint(a.ID)
			floor := s.app.restoreFenceFloor()
			s.rig.proxy.Arm(testdb.Faults{Match: "idle_in_transaction_session_timeout", Commit: fault})
			w := s.settle(id, a.ID)
			if w.Code != http.StatusOK {
				t.Fatalf("settle: %d %s", w.Code, w.Body.String())
			}
			var got attemptSettleJSON
			decodeJSON(t, w, &got)
			wantSettle(t, got, settleChecking, nil)
			if s.state(id, a.ID) != attemptFenced {
				t.Fatalf("ledger = %v, want fenced (the fence is recorded before COMMIT)", s.state(id, a.ID))
			}
			if s.app.restoreFenceFloor() != floor {
				t.Fatal("an unknown settle COMMIT raised the floor")
			}
			if fault == testdb.CommitCloseUnforwarded {
				s.rig.awaitReleased(s.rig.pidOf("idle_in_transaction_session_timeout"), 10*time.Second)
			}
			wantSettle(t, s.settled(id, a.ID), settleNotApplied, nil)
			if s.deleted(a.ID) {
				t.Fatal("a settle deleted the account")
			}
		})
	}
}

// s4: an attempt claimed at 9m59s and held at the gate is still running when the
// clock passes ten minutes. Settle waits with no transaction (the proxy log is
// silent for the whole wait, and only the deletion waits on the gate), a
// resubmission is attempt_closed, then settle answers applied after the
// deletion ends.
func TestRunningAttemptCrossesTheSubmitWindow(t *testing.T) {
	s := newProxiedAttemptScene(t)
	a := s.target("a")
	s.c.holdNotices = true
	c2 := loginAPI(t, s.app, "admin", "pw")
	c2.holdNotices = true
	id := s.mint(a.ID)
	s.advance(9*time.Minute + 59*time.Second)
	release, _ := holdAdvisory(t, s.direct, contactLockID)
	defer release()
	del := make(chan *httptest.ResponseRecorder, 1)
	go func() { del <- s.del(id, a.ID) }()
	waitFor(t, 15*time.Second, "the deletion to claim and wait at the gate", func() bool {
		return s.state(id, a.ID) == attemptRunning && advisoryWaiters(t, s.direct) == 1
	})
	s.advance(2 * time.Minute) // 11m59s since the mint: the submit window has passed
	settled := make(chan *httptest.ResponseRecorder, 1)
	// settleBeforeTx runs after the middleware and just before the handler looks
	// the attempt up and waits on it, with nothing but memory in between, so once
	// it has run every later statement would be the wait's own.
	atHandler := make(chan struct{})
	s.app.hooks.settleBeforeTx = func() { close(atHandler) }
	go func() { settled <- s.settleAs(c2, id, a.ID) }()
	select {
	case <-atHandler:
	case <-time.After(10 * time.Second):
		t.Fatal("the settle request never reached its handler")
	}
	mark := len(s.rig.proxy.Events())
	// A quiet window: that settle sends nothing while it waits can only be seen by
	// letting time pass.
	time.Sleep(600 * time.Millisecond)
	if quiet := s.rig.proxy.Events()[mark:]; len(quiet) != 0 {
		t.Fatalf("settle sent %d statements while waiting for the running attempt: %+v", len(quiet), quiet)
	}
	select {
	case w := <-settled:
		t.Fatalf("settle answered while the attempt was running: %d %s", w.Code, w.Body.String())
	default:
	}
	if advisoryWaiters(t, s.direct) != 1 {
		t.Fatalf("%d backends wait on the gate, want only the deletion's", advisoryWaiters(t, s.direct))
	}
	if w := s.deleteAs(c2, id, a.ID); w.Code != http.StatusConflict || codeOf(t, w) != "attempt_closed" {
		t.Fatalf("resubmission of the running id: %d %s, want 409 attempt_closed", w.Code, w.Body.String())
	}
	release()
	if w := awaitRec(t, del, "the deletion"); w.Code != http.StatusOK {
		t.Fatalf("the deletion: %d %s, want 200", w.Code, w.Body.String())
	}
	w := awaitRec(t, settled, "settle")
	var got attemptSettleJSON
	decodeJSON(t, w, &got)
	wantSettle(t, got, settleApplied, nil)
}

// A deletion refuses 401 after a revocation committed between the middleware
// and the transaction (the early read, found with the gate held), with nothing
// changed and the id ended rolled back. The seal-time variant is in
// TestSettleAuthorization.
func TestDeletionRefusesARevocationAfterTheMiddleware(t *testing.T) {
	for name, revoke := range revocationKinds {
		t.Run(name, func(t *testing.T) {
			s := newAttemptScene(t)
			a := s.target("a")
			s.c.holdNotices = true
			id := s.mint(a.ID)
			before := s.counter()
			release, _ := holdAdvisory(t, s.app.DB, contactLockID)
			defer release()
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- s.del(id, a.ID) }()
			waitFor(t, 10*time.Second, "the deletion waiting for the gate", func() bool { return advisoryWaiters(t, s.app.DB) == 1 })
			if err := s.app.DB.Exec(revoke, s.admin.ID).Error; err != nil {
				t.Fatal(err)
			}
			release()
			w := awaitRec(t, done, "the deletion")
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("delete after %s: %d %s, want 401", name, w.Code, w.Body.String())
			}
			if s.deleted(a.ID) || s.state(id, a.ID) != attemptRolledBack || s.counter() != before || s.receipts() != 0 {
				t.Fatal("a refused deletion changed something or left the id un-ended")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// s5 and the cap, the 10-minute boundary, validation after the claim
// ---------------------------------------------------------------------------

// s5: one admin presses twice with two ids released by a pg_locks barrier. The
// first deletes; the second answers as today for an already-deleted account
// (404), ends rolled back and settles not_applied with subjectDeleted true and
// no actor, attempt or attribution field.
func TestTwoPressesReleasedTogether(t *testing.T) {
	s := newAttemptScene(t)
	a := s.target("a")
	c2 := loginAPI(t, s.app, "admin", "pw")
	c2.holdNotices, s.c.holdNotices = true, true
	id1, id2 := s.mint(a.ID), s.mint(a.ID)
	release, _ := holdAdvisory(t, s.app.DB, contactLockID)
	defer release()
	ch1, ch2 := make(chan *httptest.ResponseRecorder, 1), make(chan *httptest.ResponseRecorder, 1)
	go func() { ch1 <- s.deleteAs(s.c, id1, a.ID) }()
	go func() { ch2 <- s.deleteAs(c2, id2, a.ID) }()
	waitFor(t, 10*time.Second, "both presses waiting for the gate", func() bool { return advisoryWaiters(t, s.app.DB) == 2 })
	release()
	w1, w2 := awaitRec(t, ch1, "press 1"), awaitRec(t, ch2, "press 2")
	winner, loser, wid, lid := w1, w2, id1, id2
	if w1.Code != http.StatusOK {
		winner, loser, wid, lid = w2, w1, id2, id1
	}
	if winner.Code != http.StatusOK || loser.Code != http.StatusNotFound {
		t.Fatalf("presses answered %d and %d, want one 200 and one 404", w1.Code, w2.Code)
	}
	if s.state(wid, a.ID) != attemptApplied || s.state(lid, a.ID) != attemptRolledBack {
		t.Fatalf("ledger: winner %v, loser %v; want applied and rolled back", s.state(wid, a.ID), s.state(lid, a.ID))
	}
	if n := contactCount(t, s.app, &models.AuditLog{}, "action = ? AND target_id = ?", audit.UserDeleted, a.ID); n != 1 || s.receipts() != 1 {
		t.Fatalf("user.deleted rows %d, receipts %d, want 1 and 1", n, s.receipts())
	}
	raw := s.settle(lid, a.ID).Body.String()
	var got attemptSettleJSON
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	wantSettle(t, got, settleNotApplied, ptr(true))
	for _, leak := range []string{"actor", "attempt", "deletedBy", "attribution"} {
		if strings.Contains(raw, leak) {
			t.Errorf("settle answer %s names %q", raw, leak)
		}
	}
	wantSettle(t, s.settled(wid, a.ID), settleApplied, nil)
}

// At the cap a claim answers attempt_busy and settle answers checking,
// recording nothing.
func TestAttemptLedgerCapOnTheRoutes(t *testing.T) {
	s := newAttemptScene(t)
	a := s.target("a")
	id := s.mint(a.ID)
	for i := 0; i < attemptLedgerCap; i++ {
		if r := s.app.attempts.ledger.claim(nonceOf(i+100), 0, 0); r != submitOK {
			t.Fatalf("filling the ledger: claim %d answered %v", i, r)
		}
	}
	before := s.counter()
	if w := s.del(id, a.ID); w.Code != http.StatusServiceUnavailable || codeOf(t, w) != "attempt_busy" {
		t.Fatalf("a claim at the cap: %d %s, want 503 attempt_busy", w.Code, w.Body.String())
	}
	wantSettle(t, s.settled(id, a.ID), settleChecking, nil)
	if s.state(id, a.ID) != attemptAbsent || s.counter() != before || s.deleted(a.ID) || s.receipts() != 0 {
		t.Fatal("a refused claim or a settle at the cap recorded something")
	}
}

// The submit window is 10 minutes: 9 minutes 59 seconds runs, 10 minutes 1
// second does not.
func TestAttemptSubmitWindowBoundary(t *testing.T) {
	s := newAttemptScene(t)
	a, b := s.target("a"), s.target("b")
	idA, idB := s.mint(a.ID), s.mint(b.ID)
	s.advance(9*time.Minute + 59*time.Second)
	if w := s.del(idA, a.ID); w.Code != http.StatusOK {
		t.Fatalf("at 9m59s: %d %s, want 200", w.Code, w.Body.String())
	}
	s.advance(2 * time.Second)
	if w := s.del(idB, b.ID); w.Code != http.StatusConflict || codeOf(t, w) != "attempt_expired" {
		t.Fatalf("at 10m01s: %d %s, want 409 attempt_expired", w.Code, w.Body.String())
	}
}

// s2: A is held at the gate, B resubmits the same id (attempt_closed at once),
// A then fails validation after its claim and the id settles not_applied.
func TestAttemptFailsValidationWhileHeld(t *testing.T) {
	s := newAttemptScene(t)
	a := s.target("a")
	createGroupAs(t, loginAPI(t, s.app, "a", "pw"), "Sole group", "public")
	s.c.holdNotices = true
	id := s.mint(a.ID)
	release, _ := holdAdvisory(t, s.app.DB, contactLockID)
	defer release()
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- s.del(id, a.ID) }()
	waitFor(t, 10*time.Second, "the deletion to wait for the gate", func() bool { return advisoryWaiters(t, s.app.DB) == 1 })
	c2 := loginAPI(t, s.app, "admin", "pw")
	before := advisoryWaiters(t, s.app.DB)
	if w := s.deleteAs(c2, id, a.ID); w.Code != http.StatusConflict || codeOf(t, w) != "attempt_closed" {
		t.Fatalf("the same id again: %d %s, want 409 attempt_closed", w.Code, w.Body.String())
	}
	if advisoryWaiters(t, s.app.DB) != before {
		t.Error("the closed answer touched the database gate")
	}
	release()
	if w := awaitRec(t, first, "the first press"); w.Code != http.StatusBadRequest {
		t.Fatalf("the first press: %d %s, want 400 (a successor is needed)", w.Code, w.Body.String())
	}
	wantSettle(t, s.settled(id, a.ID), settleNotApplied, nil)
}

// ---------------------------------------------------------------------------
// Deadlines
// ---------------------------------------------------------------------------

// A deadline that expires during a retry attempt: attempt 1 finds the admin's
// own row busy, the retry seam runs out the budget, attempt 2 is refused before
// BEGIN. The answer is delete_timed_out, the id ends rolled back and settles
// not_applied.
func TestDeletionDeadlineDuringARetry(t *testing.T) {
	setAdminDeleteBounds(t, time.Second, 100*time.Millisecond)
	s := newAttemptScene(t)
	a := s.target("a")
	hold, _ := beginOwnedTx(t, s.app.DB)
	if err := hold.Exec("SELECT id FROM users WHERE id = ? FOR NO KEY UPDATE", s.admin.ID).Error; err != nil {
		t.Fatal(err)
	}
	id := s.mint(a.ID)
	s.app.hooks.sealAttempts = &atomic.Int64{}
	s.app.hooks.deleteBeforeRetry = func() { time.Sleep(1100 * time.Millisecond) }
	w := s.del(id, a.ID)
	if w.Code != http.StatusServiceUnavailable || codeOf(t, w) != "delete_timed_out" {
		t.Fatalf("delete: %d %s, want 503 delete_timed_out", w.Code, w.Body.String())
	}
	if s.app.hooks.sealAttempts.Load() != 1 {
		t.Errorf("sealed attempts = %d, want the one before the retry ran out of time", s.app.hooks.sealAttempts.Load())
	}
	if s.state(id, a.ID) != attemptRolledBack || s.deleted(a.ID) {
		t.Fatal("the id should end rolled back with nothing deleted")
	}
	hold.Rollback()
	wantSettle(t, s.settled(id, a.ID), settleNotApplied, nil)
}

// ceilMillis rounds a sub-millisecond remainder up, so the idle timeout set
// from it is never 0 (which would disable it).
func TestSubMillisecondRemainderInstallsAPositiveTimeout(t *testing.T) {
	for _, d := range []time.Duration{1, 999 * time.Microsecond, 999999, time.Millisecond, time.Millisecond + 1} {
		got := ceilMillis(d)
		if got < 1 || time.Duration(got)*time.Millisecond < d {
			t.Errorf("ceilMillis(%s) = %d, want a positive whole number of milliseconds covering it", d, got)
		}
	}
}

// A DELETE whose body is trickled past the budget never starts its transaction
// before the body is complete (the budget starts after decoding), and one cut
// off mid-body starts none at all; either way its id settles.
func TestDeletionTrickledBody(t *testing.T) {
	trickle := func(t *testing.T, cut bool) {
		setAdminDeleteBounds(t, time.Second, 100*time.Millisecond)
		app := testApp(t)
		admin := newTestUser(t, app, "admin", "pw", models.RoleAdmin)
		target := newTestUser(t, app, "target", "pw", models.RoleMember)
		r := newCtxRigFor(t, app)
		c := loginAPI(t, r.papp, "admin", "pw")
		mint := c.do("POST", "/api/v1/admin/operation-attempts", map[string]any{"kind": attemptKindAccountDelete, "subjectUserId": target.ID}, true)
		var minted attemptMintJSON
		decodeJSON(t, mint, &minted)

		srv := httptest.NewServer(r.papp.setupRouter())
		defer srv.Close()
		pr, pw := io.Pipe()
		req, err := http.NewRequest("DELETE", srv.URL+"/api/v1/admin/users/"+itoa(target.ID), pr)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", c.token)
		for _, ck := range c.jar {
			req.AddCookie(&http.Cookie{Name: ck.Name, Value: ck.Value})
		}
		mark := len(r.proxy.Events())
		var bodyDone atomic.Int64
		go func() {
			io.WriteString(pw, `{"attemptId":"`+minted.AttemptID[:40])
			time.Sleep(1500 * time.Millisecond) // past the 1 s budget
			if cut {
				pw.CloseWithError(errors.New("client gave up"))
				return
			}
			// Stamped before the last bytes go out: the server cannot have a
			// complete body, and so cannot start its transaction, any earlier.
			bodyDone.Store(time.Now().UnixNano())
			io.WriteString(pw, minted.AttemptID[40:]+`"}`)
			pw.Close()
		}()
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			defer resp.Body.Close()
		}
		fenceEvents := func() []testdb.Event {
			var out []testdb.Event
			for _, e := range r.proxy.Events()[mark:] {
				if e.Kind == testdb.KindStatement && isRestoreFenceStatement(e.SQL) {
					out = append(out, e)
				}
			}
			return out
		}
		nonce, _, _ := r.papp.openAttempt(minted.AttemptID, attemptKindAccountDelete, admin.ID, target.ID)
		if cut {
			if len(fenceEvents()) != 0 {
				t.Fatal("a transaction started although the body never completed")
			}
			if st, _ := r.papp.attempts.ledger.look(nonce); st != attemptAbsent {
				t.Fatalf("a cut-off body claimed the id: %v", st)
			}
		} else {
			if err != nil || resp.StatusCode != http.StatusOK {
				t.Fatalf("the trickled delete: %v %v, want 200 (the budget starts after the body)", err, resp)
			}
			fe := fenceEvents()
			if len(fe) == 0 || fe[0].Time.UnixNano() < bodyDone.Load() {
				t.Fatalf("the transaction started before the body was complete: %+v (body done at %d)", fe, bodyDone.Load())
			}
			if st, _ := r.papp.attempts.ledger.look(nonce); st != attemptApplied {
				t.Fatalf("ledger = %v, want applied", st)
			}
		}
		sw := c.do("POST", "/api/v1/admin/operation-attempts/settle",
			map[string]any{"attemptId": minted.AttemptID, "kind": attemptKindAccountDelete, "subjectUserId": target.ID}, true)
		var got attemptSettleJSON
		decodeJSON(t, sw, &got)
		if cut {
			wantSettle(t, got, settleNotApplied, nil)
		} else {
			wantSettle(t, got, settleApplied, nil)
		}
	}
	t.Run("trickled past the budget", func(t *testing.T) { trickle(t, false) })
	t.Run("cut off mid-body", func(t *testing.T) { trickle(t, true) })
}

// Settle's transaction reconciles only the subject's open requests and never
// scans every request (contactChange{} is not used).
func TestSettleReconcilesOnlyTheSubject(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "pw", models.RoleAdmin)
	target := newTestUser(t, app, "target", "pw", models.RoleMember)
	r := newCtxRigFor(t, app)
	c := loginAPI(t, r.papp, "admin", "pw")
	mint := c.do("POST", "/api/v1/admin/operation-attempts", map[string]any{"kind": attemptKindAccountDelete, "subjectUserId": target.ID}, true)
	var minted attemptMintJSON
	decodeJSON(t, mint, &minted)
	mark := len(r.proxy.Events())
	sw := c.do("POST", "/api/v1/admin/operation-attempts/settle",
		map[string]any{"attemptId": minted.AttemptID, "kind": attemptKindAccountDelete, "subjectUserId": target.ID}, true)
	if sw.Code != http.StatusOK {
		t.Fatalf("settle: %d %s", sw.Code, sw.Body.String())
	}
	subjectScans, unscoped := 0, 0
	for _, e := range r.proxy.Events()[mark:] {
		if e.Kind != testdb.KindStatement || !strings.Contains(e.SQL, `FROM "contact_requests"`) || !strings.Contains(e.SQL, "FOR UPDATE") {
			continue
		}
		if strings.Contains(e.SQL, "requester_id") && strings.Contains(e.SQL, "subject_user_id") {
			subjectScans++
		} else {
			unscoped++
		}
	}
	if subjectScans == 0 {
		t.Errorf("settle's log shows no reconcile of the subject's requests: %+v", r.proxy.Events()[mark:])
	}
	if unscoped != 0 {
		t.Errorf("settle's log shows %d unscoped request scans", unscoped)
	}
}
