package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"fold/internal/guardkit"

	"fold/internal/testdb"
	"fold/models"

	"gorm.io/gorm"
)

// restoreFenceFloor is the floor, for tests.
func (a *App) restoreFenceFloor() int64 { return a.fence.floor.Load() }

// fenceTurnWaiters is how many callers wait for the fence turn: a test seam.
func (a *App) fenceTurnWaiters() int { return int(a.fence.waiters.Load()) }

func recoveryPolicyMismatch(file string, level int) error {
	got, err := strconv.Atoi(strings.TrimSpace(file))
	if err != nil {
		return fmt.Errorf("deploy/recovery-policy holds %q, want an integer: %w", file, err)
	}
	if got != level {
		return fmt.Errorf("deploy/recovery-policy holds %d but recoveryPolicyLevel is %d: change both together", got, level)
	}
	return nil
}

func TestRecoveryPolicyFileMatchesConstant(t *testing.T) {
	b, err := os.ReadFile("deploy/recovery-policy")
	if err != nil {
		t.Fatal(err)
	}
	if err := recoveryPolicyMismatch(string(b), recoveryPolicyLevel); err != nil {
		t.Error(err)
	}
	// The check fails when either side changes alone, and for anything that is
	// not an integer.
	for _, tc := range []struct {
		file  string
		level int
	}{
		{"1\n", 2}, {"2\n", 1}, {"3\n", 2}, {"one", 2}, {"", 2}, {"2.0\n", 2}, {"0\n", 2},
	} {
		if recoveryPolicyMismatch(tc.file, tc.level) == nil {
			t.Errorf("the mismatch check accepted file %q with level %d", tc.file, tc.level)
		}
	}
	if recoveryPolicyMismatch("2\n", 2) != nil || recoveryPolicyMismatch(" 2 \n", 2) != nil {
		t.Error("the mismatch check refused a file that equals the constant")
	}
}

var fenceWriterFuncs = map[string]bool{"bootRestoreFence": true, "takeRestoreTicket": true}

func sqlWritesWords(s string) bool {
	u := strings.ToUpper(s)
	for _, w := range []string{"INSERT", "UPDATE", "DELETE", "TRUNCATE", "MERGE", "COPY "} {
		if strings.Contains(u, w) {
			return true
		}
	}
	return false
}

// fenceKeyUsers may name restoreFenceKey: the two writers, the refusal every
// other settings writer calls, and the boot step's error text.
var fenceKeyUsers = map[string]bool{"bootRestoreFence": true, "takeRestoreTicket": true, "refuseRestoreFenceKey": true}

// fenceWriterProblems parses production sources. The row's key is spelled as a
// string only once, as the value of the restoreFenceKey constant in
// restore_fence.go; restoreFenceKey is used only in restore_fence.go, by the two
// writers and the refusal; and a statement that writes (an inline literal, or
// a constant of restore_fence.go whose value writes) is used only by the boot
// step and takeRestoreTicket.
func fenceWriterProblems(sources map[string]string) []string {
	parsed := guardkit.Sources(sources).Parse(0)
	bad := append([]string(nil), parsed.Problems...)
	fset := parsed.Fset
	// The write statements restore_fence.go declares as constants.
	writeConsts := map[string]bool{}
	if f := parsed.File("restore_fence.go"); f != nil {
		for _, d := range f.AST.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, sp := range gd.Specs {
				vs := sp.(*ast.ValueSpec)
				for i, v := range vs.Values {
					if lit, ok := v.(*ast.BasicLit); ok && lit.Kind == token.STRING && sqlWritesWords(lit.Value) && i < len(vs.Names) {
						writeConsts[vs.Names[i].Name] = true
					}
				}
			}
		}
	}
	for _, pf := range parsed.Files {
		path, tree := pf.Path, pf.AST
		for _, d := range tree.Decls {
			fd, isFunc := d.(*ast.FuncDecl)
			keyDecl := false
			if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.CONST && path == "restore_fence.go" {
				for _, sp := range gd.Specs {
					for _, n := range sp.(*ast.ValueSpec).Names {
						if n.Name == "restoreFenceKey" {
							keyDecl = true
						}
					}
				}
			}
			writer := isFunc && fenceWriterFuncs[fd.Name.Name] && path == "restore_fence.go"
			ast.Inspect(d, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.BasicLit:
					if x.Kind != token.STRING {
						return true
					}
					if strings.Contains(x.Value, "restore_fence") && !keyDecl {
						bad = append(bad, fmt.Sprintf("%s:%d: names the restore_fence row in a string; only the restoreFenceKey constant may", path, fset.Position(x.Pos()).Line))
						return true
					}
					if isFunc && path == "restore_fence.go" && sqlWritesWords(x.Value) && !writer {
						bad = append(bad, fmt.Sprintf("%s:%d: writes in restore_fence.go outside the boot step and takeRestoreTicket", path, fset.Position(x.Pos()).Line))
					}
				case *ast.Ident:
					if x.Name == "restoreFenceKey" && !keyDecl && (path != "restore_fence.go" || !isFunc || !fenceKeyUsers[fd.Name.Name]) {
						bad = append(bad, fmt.Sprintf("%s:%d: uses restoreFenceKey outside the boot step, takeRestoreTicket and refuseRestoreFenceKey", path, fset.Position(x.Pos()).Line))
					}
					if writeConsts[x.Name] && isFunc && !writer {
						bad = append(bad, fmt.Sprintf("%s:%d: runs the restore fence's write statement %s outside the boot step and takeRestoreTicket", path, fset.Position(x.Pos()).Line, x.Name))
					}
				}
				return true
			})
		}
	}
	return bad
}

func TestRestoreFenceHasTwoWriters(t *testing.T) {
	sources := guardkit.Production(t)
	for _, v := range fenceWriterProblems(sources) {
		t.Error(v)
	}
	// Negative fixtures.
	clean := map[string]string{"restore_fence.go": `package main
const restoreFenceKey = "restore_fence"
const (
	restoreFenceCreateSQL  = "INSERT INTO settings (key, value) VALUES (?, '0') ON CONFLICT (key) DO NOTHING"
	restoreFenceAdvanceSQL = "UPDATE settings SET value = (value::bigint + 1)::text WHERE key = ? RETURNING value::bigint"
)
func refuseRestoreFenceKey(key string) error { if key == restoreFenceKey { return errRestoreFenceKey }; return nil }
func (a *App) bootRestoreFence() error { tx.Exec(restoreFenceCreateSQL, restoreFenceKey); tx.Raw(restoreFenceAdvanceSQL, restoreFenceKey); return nil }
func (a *App) takeRestoreTicket() { tx.Raw(restoreFenceAdvanceSQL, restoreFenceKey) }
`, "other.go": "package main\nfunc f() {}\n"}
	if bad := fenceWriterProblems(clean); len(bad) != 0 {
		t.Fatalf("the clean fixture failed: %v", bad)
	}
	for name, files := range map[string]map[string]string{
		"a writer in another file": {"restore_fence.go": clean["restore_fence.go"],
			"other.go": "package main\nfunc f() { tx.Exec(\"UPDATE settings SET value = '0' WHERE key = 'restore_fence'\") }\n"},
		"a third writer in the file": {"restore_fence.go": clean["restore_fence.go"] +
			"func (a *App) other() { tx.Exec(\"DELETE FROM settings WHERE key = 'restore_fence'\") }\n", "other.go": clean["other.go"]},
		"a third writer through the key": {"restore_fence.go": clean["restore_fence.go"] +
			"func (a *App) other() { tx.Exec(\"DELETE FROM settings WHERE key = ?\", restoreFenceKey) }\n", "other.go": clean["other.go"]},
		"a third writer through the advance statement": {"restore_fence.go": clean["restore_fence.go"] +
			"func (a *App) other(k string) { tx.Raw(restoreFenceAdvanceSQL, k) }\n", "other.go": clean["other.go"]},
		"the key spelled again in the file": {"restore_fence.go": clean["restore_fence.go"] +
			"func (a *App) bootCopy() string { return \"restore_fence\" }\n", "other.go": clean["other.go"]},
		"the key constant used elsewhere": {"restore_fence.go": clean["restore_fence.go"],
			"other.go": "package main\nfunc f() { _ = models.Setting{Key: restoreFenceKey} }\n"},
	} {
		if len(fenceWriterProblems(files)) == 0 {
			t.Errorf("%s: the guard accepted it", name)
		}
	}
}

// settingsWriteRe finds raw SQL that writes the settings table.
var settingsWriteRe = regexp.MustCompile(`(?i)(insert\s+into|update|delete\s+from)\s+(public\.)?settings\b`)

// settingsWriterProblems is the census of every writer of the settings table
// through models.Setting: a key spelled as a literal or a package constant is
// fixed; any other key (a parameter, a field, a variable) is a non-literal
// writer and its function must refuse the barrier's row with
// refuseRestoreFenceKey. The one such writer is setSettingIn (settings.go),
// which every settings setter calls; when settings.go is among the sources it
// must declare setSettingIn and that function must make the refusal. Raw SQL
// that writes settings is only restore_fence.go's.
func settingsWriterProblems(sources map[string]string) []string {
	parsed := guardkit.Sources(sources).Parse(0)
	bad := append([]string(nil), parsed.Problems...)
	fset := parsed.Fset
	consts := map[string]bool{}
	for _, pf := range parsed.Files {
		tree := pf.AST
		for _, d := range tree.Decls {
			if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.CONST {
				for _, sp := range gd.Specs {
					for _, n := range sp.(*ast.ValueSpec).Names {
						consts[n.Name] = true
					}
				}
			}
		}
	}
	isSetting := func(e ast.Expr) bool {
		sel, ok := e.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Setting" {
			return false
		}
		id, ok := sel.X.(*ast.Ident)
		return ok && id.Name == "models"
	}
	sawSetter := false
	for _, pf := range parsed.Files {
		path, tree := pf.Path, pf.AST
		for _, d := range tree.Decls {
			fd, isFunc := d.(*ast.FuncDecl)
			refuses := false
			if isFunc && fd.Body != nil {
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					if c, ok := n.(*ast.CallExpr); ok {
						if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "refuseRestoreFenceKey" {
							refuses = true
						}
					}
					return true
				})
			}
			if path == "settings.go" && isFunc && fd.Name.Name == "setSettingIn" {
				sawSetter = true
				if !refuses {
					bad = append(bad, fmt.Sprintf("%s:%d: setSettingIn does not refuse restore_fence", path, fset.Position(fd.Pos()).Line))
				}
			}
			guardkit.InspectStack(d, func(n ast.Node, stack []ast.Node) bool {
				switch x := n.(type) {
				case *ast.BasicLit:
					if path != "restore_fence.go" && x.Kind == token.STRING && settingsWriteRe.MatchString(x.Value) {
						bad = append(bad, fmt.Sprintf("%s:%d: raw SQL writes settings outside restore_fence.go", path, fset.Position(x.Pos()).Line))
					}
				case *ast.CompositeLit:
					typed := x.Type != nil && isSetting(x.Type)
					if x.Type == nil && len(stack) >= 2 {
						if parent, ok := stack[len(stack)-2].(*ast.CompositeLit); ok {
							if at, ok := parent.Type.(*ast.ArrayType); ok && isSetting(at.Elt) {
								typed = true
							}
						}
					}
					if !typed {
						return true
					}
					for _, el := range x.Elts {
						kv, ok := el.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						if k, ok := kv.Key.(*ast.Ident); !ok || k.Name != "Key" {
							continue
						}
						switch v := kv.Value.(type) {
						case *ast.BasicLit:
							if strings.Contains(v.Value, "restore_fence") {
								bad = append(bad, fmt.Sprintf("%s:%d: writes the restore_fence key", path, fset.Position(v.Pos()).Line))
							}
						case *ast.Ident:
							if !consts[v.Name] && !refuses {
								bad = append(bad, fmt.Sprintf("%s:%d: a settings writer with variable key %s does not refuse restore_fence", path, fset.Position(v.Pos()).Line, v.Name))
							}
						default:
							if !refuses {
								bad = append(bad, fmt.Sprintf("%s:%d: a settings writer with a non-literal key does not refuse restore_fence", path, fset.Position(kv.Pos()).Line))
							}
						}
					}
				}
				return true
			})
		}
	}
	if _, ok := sources["settings.go"]; ok && !sawSetter {
		bad = append(bad, "settings.go does not declare setSettingIn, the one settings writer")
	}
	return bad
}

func TestSettingsWritersRefuseTheFenceKey(t *testing.T) {
	sources := guardkit.Production(t)
	if _, ok := sources["settings.go"]; !ok {
		t.Fatal("settings.go, home of setSettingIn, is not in the production tree")
	}
	for _, v := range settingsWriterProblems(sources) {
		t.Error(v)
	}
	const head = "package main\nconst settingFixed = \"fixed\"\n"
	good := head + "func a(tx *gorm.DB) { tx.Where(models.Setting{Key: settingFixed}).FirstOrCreate(nil) }\n" +
		"func b(tx *gorm.DB, key string) error { if err := refuseRestoreFenceKey(key); err != nil { return err }\n tx.Where(models.Setting{Key: key}).FirstOrCreate(nil); return nil }\n" +
		"func c(tx *gorm.DB) error { for _, s := range []models.Setting{{Key: \"site_name\"}} { if err := refuseRestoreFenceKey(s.Key); err != nil { return err }\n tx.Where(models.Setting{Key: s.Key}).FirstOrCreate(nil) }; return nil }\n"
	if bad := settingsWriterProblems(map[string]string{"x.go": good}); len(bad) != 0 {
		t.Fatalf("the clean fixture failed: %v", bad)
	}
	for name, src := range map[string]string{
		"a variable key without the refusal":  head + "func f(tx *gorm.DB, key string) { tx.Where(models.Setting{Key: key}).FirstOrCreate(nil) }\n",
		"a field key without the refusal":     head + "func f(tx *gorm.DB, s S) { tx.Where(models.Setting{Key: s.Key}).FirstOrCreate(nil) }\n",
		"the literal key":                     head + "func f(tx *gorm.DB) { tx.Create(&models.Setting{Key: \"restore_fence\"}) }\n",
		"a slice element without the refusal": head + "func f(tx *gorm.DB, k string) { tx.Create(&[]models.Setting{{Key: k}}) }\n",
		"raw SQL that writes settings":        head + "func f(tx *gorm.DB) { tx.Exec(\"UPDATE settings SET value = '1' WHERE key = ?\", k) }\n",
	} {
		if len(settingsWriterProblems(map[string]string{"x.go": src})) == 0 {
			t.Errorf("%s: the census accepted it", name)
		}
	}
	for name, src := range map[string]string{
		"setSettingIn without the refusal": "package main\nfunc setSettingIn(tx *gorm.DB, key, value string) error { return tx.Create(&models.Setting{Key: key, Value: value}).Error }\n",
		"no setSettingIn":                  "package main\nfunc other() {}\n",
	} {
		if len(settingsWriterProblems(map[string]string{"settings.go": src})) == 0 {
			t.Errorf("%s: the census accepted settings.go", name)
		}
	}
}

func TestRestoreFenceBoot(t *testing.T) {
	app := testApp(t)
	var n int64
	if err := app.DB.Raw("SELECT value::bigint FROM settings WHERE key = 'restore_fence'").Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n < 1 || app.restoreFenceFloor() != n {
		t.Fatalf("after boot: counter %d, floor %d; want the floor on the boot's own value", n, app.restoreFenceFloor())
	}
	second := &App{DB: app.DB}
	if err := second.bootRestoreFence(); err != nil {
		t.Fatal(err)
	}
	if second.restoreFenceFloor() != n+1 || app.restoreFenceFloor() != n {
		t.Fatalf("a second process: floor %d (want %d), the first keeps %d", second.restoreFenceFloor(), n+1, app.restoreFenceFloor())
	}
	// An unparsable value stops the boot and names the row.
	if err := app.DB.Exec("UPDATE settings SET value = 'abc' WHERE key = 'restore_fence'").Error; err != nil {
		t.Fatal(err)
	}
	err := (&App{DB: app.DB}).bootRestoreFence()
	if err == nil || !strings.Contains(err.Error(), "restore_fence") {
		t.Fatalf("boot over an unparsable value: %v, want an error naming restore_fence", err)
	}
	if app.DB.Exec("DELETE FROM settings WHERE key = 'restore_fence'").Error != nil {
		t.Fatal("cannot clear the row")
	}
	fresh := &App{DB: app.DB}
	if err := fresh.bootRestoreFence(); err != nil || fresh.restoreFenceFloor() != 1 {
		t.Fatalf("boot over a deleted row: %v, floor %d; a new process has no floor to protect", err, fresh.restoreFenceFloor())
	}
}

func TestRestoreTicketMethods(t *testing.T) {
	app := testApp(t)
	floor := app.restoreFenceFloor()
	take := func(db *gorm.DB) (restoreTicket, error) {
		var tk restoreTicket
		err := db.Transaction(func(tx *gorm.DB) error {
			var e error
			tk, e = app.takeRestoreTicket(tx)
			return e
		})
		return tk, err
	}
	// An error raises nothing and releases the turn.
	tk, err := take(app.DB)
	if err != nil || !tk.held || tk.value != floor+1 {
		t.Fatalf("first ticket: %+v, %v; want value %d held", tk, err, floor+1)
	}
	app.acknowledgeRestoreTicket(&tk, errors.New("rolled back"))
	if tk.held || app.restoreFenceFloor() != floor {
		t.Fatalf("an error acknowledgment: held %v floor %d, want released and floor %d", tk.held, app.restoreFenceFloor(), floor)
	}
	// Abandon raises nothing either, and is idempotent.
	tk, err = take(app.DB)
	if err != nil || tk.value != floor+2 {
		t.Fatalf("second ticket: %+v, %v", tk, err)
	}
	app.abandonRestoreTicket(&tk)
	app.abandonRestoreTicket(&tk)
	if app.restoreFenceFloor() != floor {
		t.Fatalf("abandon raised the floor to %d", app.restoreFenceFloor())
	}
	// A nil acknowledgment raises it, once.
	tk, err = take(app.DB)
	if err != nil || tk.value != floor+3 {
		t.Fatalf("third ticket: %+v, %v", tk, err)
	}
	app.acknowledgeRestoreTicket(&tk, nil)
	app.acknowledgeRestoreTicket(&tk, nil)
	if app.restoreFenceFloor() != floor+3 || tk.held {
		t.Fatalf("acknowledgment: floor %d held %v, want %d released", app.restoreFenceFloor(), tk.held, floor+3)
	}
	// A context that ends while another holder has the turn: its error, no ticket,
	// the counter untouched.
	holder, err := take(app.DB)
	if err != nil {
		t.Fatal(err)
	}
	var before int64
	app.DB.Raw("SELECT value::bigint FROM settings WHERE key = 'restore_fence'").Scan(&before)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	waiting, err := take(app.DB.WithContext(ctx))
	if !errors.Is(err, context.DeadlineExceeded) || waiting.held {
		t.Fatalf("waiting for the turn past the deadline: %+v, %v; want the context's error and no ticket", waiting, err)
	}
	var after int64
	app.DB.Raw("SELECT value::bigint FROM settings WHERE key = 'restore_fence'").Scan(&after)
	if after != before || app.fenceTurnWaiters() != 0 {
		t.Fatalf("the counter moved %d -> %d or %d waiters remain", before, after, app.fenceTurnWaiters())
	}
	app.abandonRestoreTicket(&holder)
}

// nextDeleteSucceeds deletes a fresh account and checks its ticket is one or
// two above the floor, that the recorder is silent and that the floor lands on
// the new counter.
func (s *attemptScene) nextDeleteSucceeds(name string) {
	s.t.Helper()
	target := s.target(name)
	floor := s.app.restoreFenceFloor()
	if w := s.del("", target.ID); w.Code != http.StatusOK {
		s.t.Fatalf("the next delete: %d %s", w.Code, w.Body.String())
	}
	got := s.counter()
	if got < floor+1 || got > floor+2 || s.app.restoreFenceFloor() != got {
		s.t.Fatalf("after the next delete: counter %d, floor %d (was %d); want a ticket one or two above the floor", got, s.app.restoreFenceFloor(), floor)
	}
	if n := s.restarts.Load(); n != 0 {
		s.t.Fatalf("the restart recorder fired %d times", n)
	}
}

func TestRestoreFenceNoFalseDetection(t *testing.T) {
	t.Run("a: validation fails after the fence statement", func(t *testing.T) {
		s := newAttemptScene(t)
		a := s.target("a")
		createGroupAs(t, loginAPI(t, s.app, "a", "pw"), "Sole group", "public")
		before, floor := s.counter(), s.app.restoreFenceFloor()
		if w := s.del("", a.ID); w.Code != http.StatusBadRequest {
			t.Fatalf("delete: %d %s, want 400 (a successor is needed)", w.Code, w.Body.String())
		}
		if s.counter() != before || s.app.restoreFenceFloor() != floor {
			t.Fatalf("a rolled-back ticket changed the counter %d -> %d or the floor %d -> %d", before, s.counter(), floor, s.app.restoreFenceFloor())
		}
		s.nextDeleteSucceeds("next")
	})

	t.Run("e: a COMMIT whose reply is lost raises only later tickets", func(t *testing.T) {
		for name, tc := range map[string]struct {
			fault     testdb.Faults
			committed bool
		}{
			"forwarded then closed": {testdb.Faults{Match: "idle_in_transaction_session_timeout", Commit: testdb.CommitForwardThenClose}, true},
			"closed unforwarded":    {testdb.Faults{Match: "idle_in_transaction_session_timeout", Commit: testdb.CommitCloseUnforwarded}, false},
		} {
			t.Run(name, func(t *testing.T) {
				setAdminDeleteBounds(t, 4*time.Second, 300*time.Millisecond)
				app := testApp(t)
				var restarts atomic.Int32
				newTestUser(t, app, "admin", "pw", "admin")
				target := newTestUser(t, app, "target", "pw", "member")
				r := newCtxRigFor(t, app)
				r.papp.requestRestart = func() { restarts.Add(1) }
				c := loginAPI(t, r.papp, "admin", "pw")
				floor := r.papp.restoreFenceFloor()
				r.proxy.Arm(tc.fault)
				w := c.do("DELETE", "/api/v1/admin/users/"+itoa(target.ID), nil, true)
				if w.Code != http.StatusInternalServerError {
					t.Fatalf("delete: %d %s, want 500 delete_outcome_unknown", w.Code, w.Body.String())
				}
				if r.papp.restoreFenceFloor() != floor {
					t.Fatal("an unknown COMMIT raised the floor")
				}
				if tc.committed {
					deadline := time.Now().Add(5 * time.Second)
					for !r.deletedUser(target.ID) && time.Now().Before(deadline) {
						time.Sleep(10 * time.Millisecond)
					}
				} else {
					r.awaitReleased(r.pidOf("idle_in_transaction_session_timeout"), 10*time.Second)
				}
				next := newTestUser(t, app, "next", "pw", "member")
				w = c.do("DELETE", "/api/v1/admin/users/"+itoa(next.ID), nil, true)
				if w.Code != http.StatusOK {
					t.Fatalf("the next delete: %d %s, want 200", w.Code, w.Body.String())
				}
				if restarts.Load() != 0 {
					t.Error("the restart recorder fired")
				}
				var counter int64
				app.DB.Raw("SELECT value::bigint FROM settings WHERE key = 'restore_fence'").Scan(&counter)
				if r.papp.restoreFenceFloor() != counter {
					t.Errorf("floor %d, counter %d: the acknowledged commit should be the largest", r.papp.restoreFenceFloor(), counter)
				}
			})
		}
	})

	t.Run("g: B waits on the turn while A is held, then commits with N+2", func(t *testing.T) {
		s := newAttemptScene(t)
		a, b := s.target("a"), s.target("b")
		cB := loginAPI(t, s.app, "admin", "pw")
		base := s.counter()
		entered, release := make(chan struct{}), make(chan struct{})
		var armed atomic.Bool
		armed.Store(true)
		s.app.hooks.restoreAckPause = func() {
			if armed.CompareAndSwap(true, false) {
				close(entered)
				<-release
			}
		}
		aDone, bDone := make(chan *httptest.ResponseRecorder, 1), make(chan *httptest.ResponseRecorder, 1)
		go func() { aDone <- s.deleteAs(s.c, "", a.ID) }()
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("A never reached its acknowledgment")
		}
		go func() { bDone <- s.deleteAs(cB, "", b.ID) }()
		testdb.WaitUntil(t, "B waiting on the turn", 10*time.Second, func() bool { return s.app.fenceTurnWaiters() == 1 })
		if s.app.restoreFenceFloor() != base {
			t.Fatal("A raised the floor before its acknowledgment")
		}
		close(release)
		for _, ch := range []chan *httptest.ResponseRecorder{aDone, bDone} {
			select {
			case w := <-ch:
				if w.Code != http.StatusOK {
					t.Fatalf("delete: %d %s", w.Code, w.Body.String())
				}
			case <-time.After(15 * time.Second):
				t.Fatal("a delete never returned")
			}
		}
		if got := s.counter(); got != base+2 || s.app.restoreFenceFloor() != base+2 {
			t.Fatalf("counter %d floor %d, want both %d", got, s.app.restoreFenceFloor(), base+2)
		}
		if s.restarts.Load() != 0 {
			t.Fatal("no restore happened, yet the recorder fired")
		}
	})

	t.Run("h: B's deadline ends while it waits on the turn", func(t *testing.T) {
		setAdminDeleteBounds(t, time.Second, 100*time.Millisecond)
		s := newAttemptScene(t)
		b := s.target("b")
		before := s.counter()
		s.app.fence.turnChan() <- struct{}{} // another holder has the turn
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() { done <- s.del("", b.ID) }()
		testdb.WaitUntil(t, "B waiting on the turn", 5*time.Second, func() bool { return s.app.fenceTurnWaiters() == 1 })
		select {
		case w := <-done:
			if w.Code != http.StatusServiceUnavailable || codeOf(t, w) != "delete_timed_out" {
				t.Fatalf("delete: %d %s, want 503 delete_timed_out", w.Code, w.Body.String())
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the delete never returned")
		}
		<-s.app.fence.turnChan()
		if s.counter() != before || s.deleted(b.ID) {
			t.Fatal("a delete that timed out waiting for the turn changed something")
		}
		adminDeleteBudget = 40 * time.Second
		s.nextDeleteSucceeds("next")
	})
}

// pgClients maps a client tool to the command that runs it: the tool itself, or
// scripts/pg-client.sh running it from the server's own image.
var pgClients = map[string][]string{}

// pgFallbackImageID is the local image ID scripts/pg-client.sh resolved for this
// process; every container run of the tests uses that one image.
var pgFallbackImageID string

var pgMajorRE = regexp.MustCompile(`\) (\d+)(?:\.\d+)?`)

// pgDiscoveryLimit stops a wrapper that hangs outside its own bounds (its
// stages add up to about five minutes in the worst case).
const pgDiscoveryLimit = 6 * time.Minute

// pgCleanupGrace is how long a command that was told to stop (SIGTERM) gets to
// finish before it is killed. The wrapper, told to stop, ends its own commands
// (its kill grace) and removes their containers (up to its daemon timeout), so
// the grace has to outlast both.
const pgCleanupGrace = 45 * time.Second

// pgRun runs argv for at most limit with the given output writers. The command
// gets its own process group, so the deadline reaches what it started: SIGTERM
// to the group first (the wrapper's trap then stops its commands and removes its
// containers), SIGKILL to the group once grace has passed. exec's own kill hits
// only the command's pid, so a wrapper killed that way would leave its docker
// and a container behind.
func pgRun(limit, grace time.Duration, stdout, stderr io.Writer, argv ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = grace
	err := cmd.Run()
	if ctx.Err() != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // whatever ignored the TERM
	}
	return err
}

// The wrapper's exit codes for "no usable container runtime" (see
// scripts/pg-client.sh). Locally these read as "Docker is not available" and skip,
// like a missing docker; under CI they fail.
const (
	pgWrapperNoRuntime = 70
	pgWrapperNoDaemon  = 71
)

func pgWrapperPath() string {
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(thisFile), "scripts", "pg-client.sh")
}

// pgCandidates lists the commands to try for one tool, in order. FOLD_PG_BIN
// (bin) stands alone, with no fallback.
func pgCandidates(tool string, major int, wrapper, bin string, lookPath func(string) (string, error)) [][]string {
	if bin != "" {
		return [][]string{{filepath.Join(bin, tool)}}
	}
	var candidates [][]string
	if p, err := lookPath(tool); err == nil {
		candidates = append(candidates, []string{p})
	}
	if _, err := lookPath("docker"); err == nil {
		candidates = append(candidates, []string{wrapper, strconv.Itoa(major), tool})
	}
	return candidates
}

// pgFallbackArgv runs the wrapper on one image ID, so the whole run uses the
// image that was recorded rather than whatever its tag points at later.
func pgFallbackArgv(wrapper string, major int, tool, imageID string) []string {
	return []string{"env", "FOLD_PG_IMAGE_ID=" + imageID, wrapper, strconv.Itoa(major), tool}
}

func exitCodeOf(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// resolvePGImage asks the wrapper for the local ID of the image it would use,
// pulling it (bounded) when it is not there.
func resolvePGImage(wrapper string, major int) (id, note string, unavailable bool) {
	var stdout, stderr strings.Builder
	if err := pgRun(pgDiscoveryLimit, pgCleanupGrace, &stdout, &stderr, wrapper, "--resolve", strconv.Itoa(major)); err != nil {
		code := exitCodeOf(err)
		return "", fmt.Sprintf("%s --resolve %d: %v: %s", wrapper, major, err, strings.TrimSpace(stderr.String())),
			code == pgWrapperNoRuntime || code == pgWrapperNoDaemon
	}
	return strings.TrimSpace(stdout.String()), "", false
}

// checkPGClient runs `<argv> --version` and accepts it only when it reports
// the server's major version. unavailable is set when the wrapper says there is
// no container runtime to run it with.
func checkPGClient(argv []string, major int, fromWrapper bool) (note string, ok, unavailable bool) {
	var buf bytes.Buffer
	err := pgRun(pgDiscoveryLimit, pgCleanupGrace, &buf, &buf, append(argv[:len(argv):len(argv)], "--version")...)
	out := buf.Bytes()
	m := pgMajorRE.FindSubmatch(out)
	switch {
	case err != nil:
		code := exitCodeOf(err)
		return fmt.Sprintf("%s: %v: %s", strings.Join(argv, " "), err, strings.TrimSpace(string(out))),
			false, fromWrapper && (code == pgWrapperNoRuntime || code == pgWrapperNoDaemon)
	case m == nil || string(m[1]) != strconv.Itoa(major):
		return fmt.Sprintf("%s reports %q, not PostgreSQL %d", strings.Join(argv, " "), strings.TrimSpace(string(out)), major), false, false
	}
	return "", true, false
}

// fenceTools finds a pg_dump and a pg_restore of the SERVER's major version. A
// newer client writes settings an older server rejects (a PostgreSQL 18
// pg_restore sends "SET transaction_timeout" to a server 16), and the restore
// then fails. The errors stay errors: the tests never ignore them. Order:
//   - FOLD_PG_BIN, a directory of tools (alone, no fallback);
//   - the tool on PATH, if its major matches;
//   - scripts/pg-client.sh, which runs the tool from the postgres:<major> image,
//     by the image's local ID (needs a running Docker, which CI and make dev
//     already use for Postgres).
//
// With no client and no running Docker the test skips locally and fails under
// CI; a client that exists but does not match, or an image that cannot be
// pulled or run, always fails, with what to do.
func fenceTools(t *testing.T) {
	t.Helper()
	var num int
	if err := testdb.Shared(t).Raw("SHOW server_version_num").Scan(&num).Error; err != nil {
		t.Fatal(err)
	}
	major := num / 10000
	wrapper := pgWrapperPath()
	for _, tool := range []string{"pg_dump", "pg_restore"} {
		if pgClients[tool] != nil {
			continue // found by an earlier test of this process
		}
		candidates := pgCandidates(tool, major, wrapper, os.Getenv("FOLD_PG_BIN"), exec.LookPath)
		var notes []string
		unusable := 0 // candidates that only say "no container runtime"
		for _, argv := range candidates {
			fromWrapper := argv[0] == wrapper
			if fromWrapper {
				if pgFallbackImageID == "" {
					id, note, unavailable := resolvePGImage(wrapper, major)
					if id == "" {
						notes = append(notes, note)
						if unavailable {
							unusable++
						}
						continue
					}
					pgFallbackImageID = id
					t.Logf("restore tests: PostgreSQL %d client tools run from container image %s", major, id)
				}
				argv = pgFallbackArgv(wrapper, major, tool, pgFallbackImageID)
			}
			note, ok, unavailable := checkPGClient(argv, major, fromWrapper)
			if ok {
				pgClients[tool] = argv
				t.Logf("restore tests: %s is %s", tool, strings.Join(argv, " "))
				break
			}
			notes = append(notes, note)
			if unavailable {
				unusable++
			}
		}
		if pgClients[tool] != nil {
			continue
		}
		if unusable == len(candidates) && os.Getenv("CI") == "" {
			t.Skipf("no %s of PostgreSQL %d is installed and Docker is not usable; the restore tests need PostgreSQL %d client tools:\n%s",
				tool, major, major, strings.Join(notes, "\n"))
		}
		t.Fatalf("no %s of PostgreSQL %d (the server's version) is usable; set FOLD_PG_BIN to a directory of PostgreSQL %d client tools, or make a running Docker available for scripts/pg-client.sh:\n%s",
			tool, major, major, strings.Join(notes, "\n"))
	}
}

func fenceDatabaseURL(t *testing.T, db *gorm.DB) string {
	t.Helper()
	var name string
	if err := db.Raw("SELECT current_database()").Scan(&name).Error; err != nil {
		t.Fatal(err)
	}
	raw, err := testdb.URL(name)
	if err != nil {
		t.Fatal(err)
	}
	// testdb.URL adds the session setting synchronous_commit as a query
	// parameter, which libpq tools (pg_dump, pg_restore) reject in a URI.
	u, err := neturl.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Del("synchronous_commit")
	u.RawQuery = q.Encode()
	return u.String()
}

func fenceRun(t *testing.T, args ...string) {
	t.Helper()
	argv := append(append([]string{}, pgClients[args[0]]...), args[1:]...)
	cmd := exec.Command(argv[0], argv[1:]...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", strings.Join(args[:2], " "), err, out)
	}
}

func fenceDump(t *testing.T, db *gorm.DB) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fence.dump")
	fenceRun(t, "pg_dump", "-Fc", "-f", path, fenceDatabaseURL(t, db))
	return path
}

// fenceRestore puts the dump back while the process runs: a full restore
// (--clean --if-exists --no-owner) or, for dataOnly, only the rows of settings
// and admin_operation_attempts.
func fenceRestore(t *testing.T, db *gorm.DB, dump string, dataOnly bool, others ...*gorm.DB) {
	t.Helper()
	defer recycleConnections(t, append([]*gorm.DB{db}, others...)...)
	url := fenceDatabaseURL(t, db)
	if !dataOnly {
		fenceRun(t, "pg_restore", "--clean", "--if-exists", "--no-owner", "-d", url, dump)
		return
	}
	if err := db.Exec("TRUNCATE settings, admin_operation_attempts").Error; err != nil {
		t.Fatal(err)
	}
	fenceRun(t, "pg_restore", "--data-only", "--no-owner", "-t", "settings", "-t", "admin_operation_attempts", "-d", url, dump)
}

// recycleConnections closes the idle connections of pools that were open while
// a --clean restore dropped and recreated the tables. A surviving backend keeps
// cached type and relation state for the dropped objects, and its next use (in
// this test or in the next one that shares the database) fails with "cache
// lookup failed for type". New connections start clean.
func recycleConnections(t *testing.T, dbs ...*gorm.DB) {
	t.Helper()
	for _, db := range dbs {
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		sqlDB.SetMaxIdleConns(0) // closes every idle connection now
		sqlDB.SetMaxIdleConns(2) // database/sql's default, which testdb does not change
	}
}

func TestRestoreFenceSameLifetime(t *testing.T) {
	fenceTools(t)
	for _, dataOnly := range []bool{false, true} {
		name := "pg_restore --clean --if-exists --no-owner"
		if dataOnly {
			name = "pg_restore --data-only of settings and admin_operation_attempts"
		}
		t.Run(name, func(t *testing.T) {
			s := newAttemptScene(t)
			a, b, other := s.target("a"), s.target("b"), s.target("other")
			idA, idB := s.mint(a.ID), s.mint(b.ID)
			if w := s.del(idA, a.ID); w.Code != http.StatusOK {
				t.Fatalf("delete A: %d %s", w.Code, w.Body.String())
			}
			dump := fenceDump(t, s.app.DB)
			atDump := s.counter()
			if w := s.del(idB, b.ID); w.Code != http.StatusOK {
				t.Fatalf("delete B: %d %s", w.Code, w.Body.String())
			}
			if s.app.restoreFenceFloor() != atDump+1 {
				t.Fatalf("floor %d, want %d", s.app.restoreFenceFloor(), atDump+1)
			}
			fenceRestore(t, s.app.DB, dump, dataOnly)
			if got := s.counter(); got != atDump {
				t.Fatalf("after the restore the counter is %d, want the dump's %d", got, atDump)
			}
			w := s.settle(idB, b.ID)
			if w.Code != http.StatusServiceUnavailable || codeOf(t, w) != "restore_detected" {
				t.Fatalf("settle B after the restore: %d %s, want 503 restore_detected", w.Code, w.Body.String())
			}
			if s.counter() != atDump {
				t.Error("a detected ticket changed the counter")
			}
			if n := s.restarts.Load(); n != 1 {
				t.Fatalf("the restart recorder fired %d times, want once", n)
			}
			// A later delete refuses before any transaction.
			w = s.del("", other.ID)
			if w.Code != http.StatusServiceUnavailable || codeOf(t, w) != "restore_detected" {
				t.Fatalf("a later delete: %d %s, want 503 restore_detected", w.Code, w.Body.String())
			}
			if s.counter() != atDump || s.deleted(other.ID) {
				t.Error("a delete after the detection changed something")
			}
			if s.restarts.Load() != 1 {
				t.Error("the recorder fires once")
			}
		})
	}
}

// A restore that lands between a COMMIT and its acknowledgment is still found
// by the next ticket, because the next ticket waits for the turn. Three
// schedules, each with real minted ids:
//   - A's acknowledgment is held by restoreAckPause and B deletes;
//   - the same, but B settles an unrelated valid id (a settle is a fenced
//     transaction too);
//   - A's COMMIT reply is held by pgproxy (no seam), and B deletes.
//
// In each, the restore of the dump taken at counter N lands while A (committed
// at N+1) has not raised the floor. B holds the gate (pg_locks), waits on the
// turn (fenceTurnWaiters = 1) and has sent no counter UPDATE (pg_stat_activity).
// A is released: A answers 200 and the floor is N+1; B reads N+1, answers 503
// restore_detected, changes no row, the database counter stays N and the
// recorder fires once. Settle of A's id in the same process is 503 before any
// transaction; a fresh App answers history_unavailable to A's settle and
// attempt_expired to its resubmission, A is active again and can be deleted
// again with a fresh id; nothing is ever answered not_applied.
func TestRestoreFenceDelayedAcknowledgment(t *testing.T) {
	fenceTools(t)
	for _, v := range []struct {
		name         string
		unrelatedOp  bool // B settles an unrelated id instead of deleting
		proxyHeldAck bool // A's COMMIT reply is held by pgproxy instead of restoreAckPause
	}{
		{name: "B deletes, A held by restoreAckPause"},
		{name: "B settles an unrelated id, A held by restoreAckPause", unrelatedOp: true},
		{name: "B deletes, A's COMMIT reply held by pgproxy", proxyHeldAck: true},
	} {
		t.Run(v.name, func(t *testing.T) {
			var s *attemptScene
			if v.proxyHeldAck {
				s = newProxiedAttemptScene(t)
			} else {
				s = newAttemptScene(t)
			}
			a, b, u := s.target("a"), s.target("b"), s.target("unrelated")
			cB := loginAPI(t, s.app, "admin", "pw")
			cB.holdNotices, s.c.holdNotices = true, true
			idA, idB, idU := s.mint(a.ID), s.mint(b.ID), s.mint(u.ID)
			base := s.counter()
			dump := fenceDump(t, s.app.DB)

			entered, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			releaseA := func() { releaseOnce.Do(func() { close(release) }) }
			// A failed run must not leave A parked (and B holding the gate) for the
			// subtests and tests that share this database.
			t.Cleanup(func() { releaseA() })
			if v.proxyHeldAck {
				s.rig.proxy.Arm(testdb.Faults{Match: "idle_in_transaction_session_timeout", Commit: testdb.CommitHoldReply})
				releaseA = func() { releaseOnce.Do(s.rig.proxy.ReleaseReply) }
				t.Cleanup(s.rig.proxy.ReleaseReply)
			} else {
				var armed atomic.Bool
				armed.Store(true)
				s.app.hooks.restoreAckPause = func() {
					if armed.CompareAndSwap(true, false) {
						close(entered)
						<-release
					}
				}
			}
			aDone, bDone := make(chan *httptest.ResponseRecorder, 1), make(chan *httptest.ResponseRecorder, 1)
			go func() { aDone <- s.deleteAs(s.c, idA, a.ID) }()
			if v.proxyHeldAck {
				testdb.WaitUntil(t, "A's COMMIT to be sent and held", 15*time.Second, func() bool {
					return len(s.rig.proxy.Find(testdb.KindCommit, "")) > 0 && s.deleted(a.ID)
				})
			} else {
				select {
				case <-entered:
				case <-time.After(15 * time.Second):
					t.Fatal("A never reached its acknowledgment")
				}
			}
			if s.app.restoreFenceFloor() != base {
				t.Fatalf("floor %d before A's acknowledgment, want %d", s.app.restoreFenceFloor(), base)
			}
			// A is committed at N+1 and has not raised the floor: the N dump comes back.
			fenceRestore(t, s.app.DB, dump, false, s.direct)
			if got := s.counter(); got != base {
				t.Fatalf("after the restore the counter is %d, want %d", got, base)
			}
			if v.unrelatedOp {
				go func() { bDone <- s.settleAs(cB, idU, u.ID) }()
			} else {
				go func() { bDone <- s.deleteAs(cB, idB, b.ID) }()
			}
			testdb.WaitUntil(t, "B waiting on the turn", 15*time.Second, func() bool { return s.app.fenceTurnWaiters() == 1 })
			var holders, sentUpdate int64
			if err := s.direct.Raw("SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND granted AND " + advisoryInThisDatabase).Scan(&holders).Error; err != nil {
				t.Fatal(err)
			}
			if err := s.direct.Raw(`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()
				AND state = 'idle in transaction' AND query ILIKE '%restore_fence%'`).Scan(&sentUpdate).Error; err != nil {
				t.Fatal(err)
			}
			if holders != 1 {
				t.Fatalf("%d advisory locks are held, want B holding the gate alone", holders)
			}
			if sentUpdate != 0 {
				t.Fatalf("%d backends sent the counter UPDATE while B waited on the turn", sentUpdate)
			}
			releaseA()
			select {
			case w := <-aDone:
				if w.Code != http.StatusOK {
					t.Fatalf("A: %d %s, want 200", w.Code, w.Body.String())
				}
			case <-time.After(20 * time.Second):
				t.Fatal("A never returned")
			}
			if s.app.restoreFenceFloor() != base+1 {
				t.Fatalf("floor %d, want %d", s.app.restoreFenceFloor(), base+1)
			}
			bw := awaitRec(t, bDone, "B")
			if bw.Code != http.StatusServiceUnavailable || codeOf(t, bw) != "restore_detected" {
				t.Fatalf("B: %d %s, want 503 restore_detected", bw.Code, bw.Body.String())
			}
			if got := s.counter(); got != base || s.deleted(b.ID) || s.deleted(u.ID) {
				t.Fatalf("B changed something: counter %d (want %d)", got, base)
			}
			if n := s.restarts.Load(); n != 1 {
				t.Fatalf("the restart recorder fired %d times, want once", n)
			}
			// In-process: settle of A's real id is 503 before any transaction.
			var mark int
			if s.rig != nil {
				mark = len(s.rig.proxy.Events())
			}
			if w := s.settle(idA, a.ID); w.Code != http.StatusServiceUnavailable || codeOf(t, w) != "restore_detected" {
				t.Fatalf("settle of A after the detection: %d %s, want 503 restore_detected", w.Code, w.Body.String())
			}
			if s.rig != nil {
				for _, e := range s.rig.proxy.Events()[mark:] {
					if strings.Contains(e.SQL, "pg_advisory_xact_lock") || isRestoreFenceStatement(e.SQL) {
						t.Errorf("settle after the detection sent %q", e.SQL)
					}
				}
			}
			// A fresh App (new key, boot step) over the restored database.
			fresh := &App{DB: s.direct, Store: s.app.Store, Config: s.app.Config, Uploads: s.app.Uploads, Mailer: s.app.Mailer}
			bootFence(t, fresh)
			fc := loginAPI(t, fresh, "admin", "pw")
			fc.holdNotices = true
			bootCounter := s.counter()
			sw := fc.do("POST", "/api/v1/admin/operation-attempts/settle",
				map[string]any{"attemptId": idA, "kind": attemptKindAccountDelete, "subjectUserId": a.ID}, true)
			var got attemptSettleJSON
			decodeJSON(t, sw, &got)
			wantSettle(t, got, settleHistoryUnavailable, nil)
			if w := fc.do("DELETE", "/api/v1/admin/users/"+itoa(a.ID), map[string]any{"attemptId": idA}, true); w.Code != http.StatusConflict || codeOf(t, w) != "attempt_expired" {
				t.Fatalf("resubmitting A's id to a fresh App: %d %s, want 409 attempt_expired", w.Code, w.Body.String())
			}
			if s.counter() != bootCounter || s.deleted(a.ID) {
				t.Fatalf("a fresh App's refusals changed the counter or A is deleted (A should be active again)")
			}
			var minted attemptMintJSON
			decodeJSON(t, fc.do("POST", "/api/v1/admin/operation-attempts", map[string]any{"kind": attemptKindAccountDelete, "subjectUserId": a.ID}, true), &minted)
			if w := fc.do("DELETE", "/api/v1/admin/users/"+itoa(a.ID), map[string]any{"attemptId": minted.AttemptID}, true); w.Code != http.StatusOK || !s.deleted(a.ID) {
				t.Fatalf("re-deleting A with a fresh id: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

// Server outlives handler: A's deadline expires after its ticket while
// reconcileContacts waits on a test-held row, pgproxy dropping the cancel and
// withholding Terminate and the FIN. A answers 503 delete_timed_out; B waits on
// the gate (pg_blocking_pids names A's backend) and takes its ticket only after
// A's server transaction ends; B's ticket is one above the floor and B
// succeeds with the recorder silent.
func TestRestoreFenceServerOutlivesHandler(t *testing.T) {
	setAdminDeleteBounds(t, time.Second, 100*time.Millisecond)
	setContactBounds(t, 100*time.Millisecond, 5*time.Second)
	f := newContactAPIFixture(t, false)
	var restarts atomic.Int32
	req := f.create(t, "email", "")
	r := newCtxRigFor(t, f.app)
	// Both requests go through ONE process: the proxied App, booted like serve,
	// with its own floor, fence turn and restart recorder. The direct handle only
	// observes.
	bootFence(t, r.papp)
	r.papp.requestRestart = func() { restarts.Add(1) }
	ac := loginAPI(t, r.papp, f.admin.Email, "password1234")
	bClient := loginAPI(t, r.papp, f.admin.Email, "password1234")
	next := newTestUser(t, f.app, "next", "password1234", models.RoleMember)
	blocker, _ := beginOwnedTx(t, f.app.DB)
	if err := blocker.Exec("SELECT id FROM contact_requests WHERE id = ? FOR UPDATE", req.ID).Error; err != nil {
		t.Fatal(err)
	}
	counter := func() (n int64) {
		f.app.DB.Raw("SELECT value::bigint FROM settings WHERE key = 'restore_fence'").Scan(&n)
		return n
	}
	before := counter()
	floor := r.papp.restoreFenceFloor()
	r.proxy.Arm(testdb.Faults{Match: "idle_in_transaction_session_timeout", Cancel: testdb.CancelDrop, Close: testdb.CloseWithholdAll})
	t.Cleanup(r.proxy.ReleaseClose)

	aDone := make(chan *httptest.ResponseRecorder, 1)
	ac.holdNotices = true
	go func() { aDone <- ac.do("DELETE", "/api/v1/admin/users/"+itoa(f.subject.ID), nil, true) }()
	apid := r.pidOf(`FROM "contact_requests"`)
	r.awaitBlocked(apid)
	w := awaitRec(t, aDone, "A")
	if w.Code != http.StatusServiceUnavailable || codeOf(t, w) != "delete_timed_out" {
		t.Fatalf("A: %d %s, want 503 delete_timed_out", w.Code, w.Body.String())
	}
	// A's backend still holds the gate and its callback's writes.
	if r.lockRows(apid, "locktype = 'advisory' AND granted") != 1 {
		t.Fatal("after A's return its backend should still hold the gate")
	}
	adminDeleteBudget = 10 * time.Second
	bClient.holdNotices = true
	if r.papp.fenceTurnWaiters() != 0 {
		t.Fatal("A's return left a waiter or its turn held")
	}
	bDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { bDone <- bClient.do("DELETE", "/api/v1/admin/users/"+itoa(next.ID), nil, true) }()
	testdb.WaitUntil(t, "B blocked by A's backend", 10*time.Second, func() bool {
		var n int64
		f.app.DB.Raw(`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'
			AND query ILIKE '%pg_advisory_xact_lock%' AND ? = ANY(pg_blocking_pids(pid))`, apid).Scan(&n)
		return n == 1
	})
	if counter() != before || r.papp.restoreFenceFloor() != floor {
		t.Fatal("B moved the counter before A's server transaction ended")
	}
	blocker.Rollback()
	bw := awaitRec(t, bDone, "B")
	if bw.Code != http.StatusOK {
		t.Fatalf("B: %d %s, want 200", bw.Code, bw.Body.String())
	}
	if got := counter(); got != before+1 || restarts.Load() != 0 || r.papp.restoreFenceFloor() != got {
		t.Fatalf("counter %d (want %d: A's rolled-back ticket counts for nothing), the process's floor %d, restarts %d", got, before+1, r.papp.restoreFenceFloor(), restarts.Load())
	}
	if r.papp.restoreDetected() {
		t.Fatal("the process marked a restore although none happened")
	}
	var deleted int64
	f.app.DB.Raw("SELECT count(*) FROM users WHERE id = ? AND deleted_at IS NOT NULL", next.ID).Scan(&deleted)
	if deleted != 1 {
		t.Fatal("B did not delete its account")
	}
}

// The stated limits: a restore of a dump taken after the last acknowledged
// fenced commit is not detected, and every acknowledged receipt still
// resolves; neither is anything answered wrongly.
func TestRestoreFenceUndetectedCases(t *testing.T) {
	fenceTools(t)
	t.Run("a dump after the last acknowledged commit", func(t *testing.T) {
		s := newAttemptScene(t)
		a, b := s.target("a"), s.target("b")
		idA := s.mint(a.ID)
		if w := s.del(idA, a.ID); w.Code != http.StatusOK {
			t.Fatalf("delete: %d %s", w.Code, w.Body.String())
		}
		dump := fenceDump(t, s.app.DB)
		fenceRestore(t, s.app.DB, dump, false)
		if s.restarts.Load() != 0 || s.app.restoreDetected() {
			t.Fatal("a restore that lost no acknowledged commit was reported")
		}
		wantSettle(t, s.settled(idA, a.ID), settleApplied, nil)
		if w := s.del("", b.ID); w.Code != http.StatusOK {
			t.Fatalf("a delete after the undetected restore: %d %s", w.Code, w.Body.String())
		}
	})

	// s2: A's COMMIT is forwarded and its reply dropped (unknown); a dump taken
	// just before it is restored. Nothing detects it, A's receipt is gone,
	// settle says history_unavailable (never not_applied) and the id stays
	// closed.
	t.Run("an unknown commit lost by a restore", func(t *testing.T) {
		setAdminDeleteBounds(t, 4*time.Second, 300*time.Millisecond)
		app := testApp(t)
		admin := newTestUser(t, app, "admin", "pw", models.RoleAdmin)
		target := newTestUser(t, app, "target", "pw", models.RoleMember)
		r := newCtxRigFor(t, app)
		var restarts atomic.Int32
		r.papp.requestRestart = func() { restarts.Add(1) }
		// The proxied App runs the boot step too, so its floor is the counter the
		// dump holds and a real detection would be possible.
		bootFence(t, r.papp)
		c := loginAPI(t, r.papp, "admin", "pw")
		var minted attemptMintJSON
		decodeJSON(t, c.do("POST", "/api/v1/admin/operation-attempts", map[string]any{"kind": attemptKindAccountDelete, "subjectUserId": target.ID}, true), &minted)
		dump := fenceDump(t, app.DB)
		r.proxy.Arm(testdb.Faults{Match: "idle_in_transaction_session_timeout", Commit: testdb.CommitForwardThenClose})
		w := c.do("DELETE", "/api/v1/admin/users/"+itoa(target.ID), map[string]any{"attemptId": minted.AttemptID}, true)
		if w.Code != http.StatusInternalServerError || codeOf(t, w) != "delete_outcome_unknown" {
			t.Fatalf("delete: %d %s, want 500 delete_outcome_unknown", w.Code, w.Body.String())
		}
		testdb.WaitUntil(t, "the commit to reach the server", 5*time.Second, func() bool { return r.deletedUser(target.ID) })
		fenceRestore(t, app.DB, dump, false)
		recycleConnections(t, r.papp.DB)
		if r.deletedUser(target.ID) {
			t.Fatal("the restore did not undo the deletion")
		}
		sw := c.do("POST", "/api/v1/admin/operation-attempts/settle",
			map[string]any{"attemptId": minted.AttemptID, "kind": attemptKindAccountDelete, "subjectUserId": target.ID}, true)
		var got attemptSettleJSON
		decodeJSON(t, sw, &got)
		wantSettle(t, got, settleHistoryUnavailable, nil)
		if restarts.Load() != 0 || r.papp.restoreDetected() {
			t.Fatal("a restore that lost only an unknown commit was reported")
		}
		again := c.do("DELETE", "/api/v1/admin/users/"+itoa(target.ID), map[string]any{"attemptId": minted.AttemptID}, true)
		if again.Code != http.StatusConflict || codeOf(t, again) != "attempt_closed" {
			t.Fatalf("resubmission: %d %s, want 409 attempt_closed (the id never runs twice)", again.Code, again.Body.String())
		}
		_ = admin
	})
}

// After a detected restore, a fresh App (new key, boot step) answers a settle or
// a submission of either id as history_unavailable or attempt_expired before
// any transaction, leaving the counter and the receipts alone.
func TestRestoreFenceFreshAppAfterARestore(t *testing.T) {
	fenceTools(t)
	s := newAttemptScene(t)
	a, b := s.target("a"), s.target("b")
	idA, idB := s.mint(a.ID), s.mint(b.ID)
	if w := s.del(idA, a.ID); w.Code != http.StatusOK {
		t.Fatalf("delete A: %d %s", w.Code, w.Body.String())
	}
	dump := fenceDump(t, s.app.DB)
	if w := s.del(idB, b.ID); w.Code != http.StatusOK {
		t.Fatalf("delete B: %d %s", w.Code, w.Body.String())
	}
	fenceRestore(t, s.app.DB, dump, false)
	fresh := &App{DB: s.app.DB, Store: s.app.Store, Config: s.app.Config, Uploads: s.app.Uploads, Mailer: s.app.Mailer}
	bootFence(t, fresh)
	fc := loginAPI(t, fresh, "admin", "pw")
	counter, receipts := s.counter(), s.receipts()
	for name, id := range map[string]string{"A": idA, "B": idB} {
		subject := a.ID
		if name == "B" {
			subject = b.ID
		}
		w := fc.do("DELETE", "/api/v1/admin/users/"+itoa(subject), map[string]any{"attemptId": id}, true)
		if w.Code != http.StatusConflict || codeOf(t, w) != "attempt_expired" {
			t.Errorf("submitting %s's id to a fresh App: %d %s, want 409 attempt_expired", name, w.Code, w.Body.String())
		}
		sw := fc.do("POST", "/api/v1/admin/operation-attempts/settle",
			map[string]any{"attemptId": id, "kind": attemptKindAccountDelete, "subjectUserId": subject}, true)
		var got attemptSettleJSON
		decodeJSON(t, sw, &got)
		wantSettle(t, got, settleHistoryUnavailable, nil)
	}
	if s.counter() != counter || s.receipts() != receipts {
		t.Error("a fresh App's refusals changed the counter or the receipts")
	}
	if s.deleted(b.ID) {
		t.Error("B should be active again after the restore")
	}
}

// s3: an id is minted and fenced by a settle, the database is dumped, the App
// is stopped, the dump is restored and a new App starts. The new App answers
// attempt_expired to a submission and history_unavailable to a settle, before
// any transaction: the counter and the receipts do not move after its boot.
func TestRestoreFenceFencedIdAcrossARestart(t *testing.T) {
	fenceTools(t)
	s := newAttemptScene(t)
	a := s.target("a")
	id := s.mint(a.ID)
	wantSettle(t, s.settled(id, a.ID), settleNotApplied, nil)
	if s.state(id, a.ID) != attemptFenced {
		t.Fatal("settle should leave the unclaimed id fenced")
	}
	dump := fenceDump(t, s.app.DB)
	// The App stops here; its fence and ledger are gone with it.
	fenceRestore(t, s.app.DB, dump, false, s.direct)
	fresh := &App{DB: s.direct, Store: s.app.Store, Config: s.app.Config, Uploads: s.app.Uploads, Mailer: s.app.Mailer}
	bootFence(t, fresh)
	fc := loginAPI(t, fresh, "admin", "pw")
	counter, receipts := s.counter(), s.receipts()
	mark := fresh.restoreFenceFloor()
	w := fc.do("DELETE", "/api/v1/admin/users/"+itoa(a.ID), map[string]any{"attemptId": id}, true)
	if w.Code != http.StatusConflict || codeOf(t, w) != "attempt_expired" {
		t.Fatalf("submission to the new App: %d %s, want 409 attempt_expired", w.Code, w.Body.String())
	}
	sw := fc.do("POST", "/api/v1/admin/operation-attempts/settle",
		map[string]any{"attemptId": id, "kind": attemptKindAccountDelete, "subjectUserId": a.ID}, true)
	var got attemptSettleJSON
	decodeJSON(t, sw, &got)
	wantSettle(t, got, settleHistoryUnavailable, nil)
	if s.counter() != counter || s.receipts() != receipts || fresh.restoreFenceFloor() != mark || s.deleted(a.ID) {
		t.Fatal("the new App's answers moved the counter, the receipts or the account before any transaction")
	}
}

// isRestoreFenceStatement reports whether wire SQL is one of the restore
// fence's two statements. The key travels as a bind parameter, so the text is
// recognised by the statements themselves.
func isRestoreFenceStatement(sql string) bool {
	return strings.Contains(sql, "restore_fence") || strings.Contains(sql, "(value::bigint + 1)::text") ||
		strings.Contains(sql, "INSERT INTO settings (key, value) VALUES (")
}

// fakeDockerScript stands in for docker in the wrapper's argument-level tests.
// It logs every call (a "---" line, then one argument per line) and answers from
// FAKE_* variables: FAKE_DOCKER_INFO (ok, down, hang), FAKE_DOCKER_PULL (ok,
// fail, hang), FAKE_DOCKER_PRESENT=1 (the image is already local),
// FAKE_DOCKER_ID, FAKE_PGREADY_RC, FAKE_TOOL_RC, FAKE_PG_MAJOR.
//
// FAKE_STUBBORN (info, pull, probe, tool) makes that call the worst case for a
// deadline: the process ignores SIGTERM, has a child in its own group that
// does too, and starts a "container" that only `docker rm -f <name>` ends (as
// the daemon owns a real one; it has a group of its own). Every pid goes to
// pids and "started" appears once they are up. Each ends itself after a minute,
// so a test that fails leaves nothing behind for long.
const fakeDockerScript = `#!/usr/bin/env bash
{ echo "---"; for a in "$@"; do printf '%s\n' "$a"; done; } >>"$FAKE_DOCKER_DIR/log"
id=${FAKE_DOCKER_ID:-sha256:feedbeef}
stubborn() { # container name, or "" for none
	trap '' TERM
	spin='trap "" TERM; echo $$ >>"$1"; i=0; while [ $i -lt 60 ]; do sleep 1; i=$((i + 1)); done'
	bash -c "$spin" spin "$FAKE_DOCKER_DIR/pids" &
	if [[ -n $1 ]]; then
		set -m
		bash -c "$spin" spin "$FAKE_DOCKER_DIR/pids" &
		echo $! >"$FAKE_DOCKER_DIR/c.$1"
		set +m
	fi
	echo $$ >>"$FAKE_DOCKER_DIR/pids"
	: >"$FAKE_DOCKER_DIR/started"
	wait
	exit 1
}
case $1 in
info)
	case ${FAKE_DOCKER_INFO:-ok} in
	down) echo "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?" >&2; exit 1 ;;
	hang) exec sleep 30 ;;
	esac
	if [[ ${FAKE_STUBBORN:-} == info ]]; then stubborn ""; fi
	;;
image)
	if [[ ${FAKE_DOCKER_PRESENT:-} == 1 || -e $FAKE_DOCKER_DIR/pulled || ${@: -1} == "$id" ]]; then
		[[ ${@: -1} == "$id" && -n ${FAKE_DOCKER_GONE:-} ]] && { echo "Error: No such image" >&2; exit 1; }
		echo "$id"
	else
		echo "Error: No such image: ${@: -1}" >&2
		exit 1
	fi
	;;
pull)
	case ${FAKE_DOCKER_PULL:-ok} in
	fail) echo "Error response from daemon: manifest unknown" >&2; exit 1 ;;
	hang) exec sleep 30 ;;
	esac
	if [[ ${FAKE_STUBBORN:-} == pull ]]; then stubborn ""; fi
	: >"$FAKE_DOCKER_DIR/pulled"
	;;
rm)
	# rm -f <name>: the daemon ends the container, group and all.
	name=${@: -1}
	if [[ -f $FAKE_DOCKER_DIR/c.$name ]]; then
		p=$(cat "$FAKE_DOCKER_DIR/c.$name")
		kill -KILL -- "-$p" 2>/dev/null
		kill -KILL "$p" 2>/dev/null
	fi
	exit 0
	;;
run)
	shift
	cname=
	while [[ $1 != --entrypoint ]]; do
		[[ $1 == --name ]] && cname=$2
		shift
	done
	entry=$2
	shift 3 # --entrypoint, its value, the image
	if [[ $entry == pg_isready ]]; then
		if [[ ${FAKE_STUBBORN:-} == probe ]]; then stubborn "$cname"; fi
		# Like the real one run as a uid with no passwd entry: without -U it
		# cannot name a user and exits 3, "no attempt made".
		named=
		for a in "$@"; do [[ $a == -U ]] && named=1; done
		[[ -n $named ]] || exit 3
		exit "${FAKE_PGREADY_RC:-0}"
	fi
	if [[ ${FAKE_STUBBORN:-} == tool ]]; then stubborn "$cname"; fi
	if [[ ${FAKE_TOOL_RC:-0} != 0 ]]; then exit "$FAKE_TOOL_RC"; fi
	if [[ ${1:-} == --version ]]; then echo "$entry (PostgreSQL) ${FAKE_PG_MAJOR:-16}.4"; fi
	;;
esac
`

type dockerCall []string

type fakeDocker struct {
	dir  string
	path string // PATH for the wrapper
}

// newFakeDocker returns a PATH holding the fake docker (when withDocker) and
// just the tools the wrapper uses, so a missing docker is really missing.
func newFakeDocker(t *testing.T, withDocker bool) *fakeDocker {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"bash", "env", "id", "dirname", "mktemp", "rm", "tail", "head", "cut", "cat", "sleep"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Fatalf("the wrapper tests need %s: %v", tool, err)
		}
		if err := os.Symlink(p, filepath.Join(bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	if withDocker {
		if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(fakeDockerScript), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return &fakeDocker{dir: dir, path: bin}
}

// wrap runs scripts/pg-client.sh with env (KEY=value pairs) and returns its
// combined output, its exit code and the docker calls it made.
func (f *fakeDocker) wrap(t *testing.T, env []string, args ...string) (string, int, []dockerCall) {
	t.Helper()
	cmd := exec.Command(pgWrapperPath(), args...)
	cmd.Env = []string{"PATH=" + f.path, "TMPDIR=" + f.dir, "FAKE_DOCKER_DIR=" + f.dir}
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		if code = exitCodeOf(err); code < 0 {
			t.Fatalf("running the wrapper: %v", err)
		}
	}
	return string(out), code, f.calls(t)
}

func (f *fakeDocker) calls(t *testing.T) []dockerCall {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, "log"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var calls []dockerCall
	for _, block := range strings.Split(string(b), "---\n")[1:] {
		calls = append(calls, dockerCall(strings.Split(strings.TrimSuffix(block, "\n"), "\n")))
	}
	return calls
}

// has reports whether the call carries these arguments next to each other.
func (c dockerCall) has(seq ...string) bool {
	for i := 0; i+len(seq) <= len(c); i++ {
		if strings.Join(c[i:i+len(seq)], "\x00") == strings.Join(seq, "\x00") {
			return true
		}
	}
	return false
}

func (c dockerCall) count(arg string) int {
	n := 0
	for _, a := range c {
		if a == arg {
			n++
		}
	}
	return n
}

// entrypoint is the tool a `docker run` call starts, "" for any other call.
func (c dockerCall) entrypoint() string {
	for i, a := range c {
		if a == "--entrypoint" && i+1 < len(c) {
			return c[i+1]
		}
	}
	return ""
}

// afterImage is what follows the image in a `docker run` call: the tool's arguments.
func (c dockerCall) afterImage() []string {
	for i, a := range c {
		if a == "--entrypoint" && i+3 <= len(c) {
			return c[i+3:]
		}
	}
	return nil
}

func callsFor(calls []dockerCall, verb string) []dockerCall {
	var out []dockerCall
	for _, c := range calls {
		if c[0] == verb {
			out = append(out, c)
		}
	}
	return out
}

// toolRun is the last container call, the one that runs the tool itself.
func toolRun(t *testing.T, calls []dockerCall, tool string) dockerCall {
	t.Helper()
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i][0] == "run" && calls[i].entrypoint() == tool {
			return calls[i]
		}
	}
	t.Fatalf("no container run of %s in %v", tool, calls)
	return nil
}

func TestPGClientWrapperMountsEachDirectoryOnceAndKeepsSpaces(t *testing.T) {
	f := newFakeDocker(t, true)
	root := t.TempDir()
	spaced := filepath.Join(root, "a b")
	other := filepath.Join(root, "c  d")
	for _, d := range []string{spaced, other} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"-Fc", "-f", filepath.Join(spaced, "one.dump"), "--file=" + filepath.Join(spaced, "two.dump"),
		filepath.Join(other, "three.dump"), filepath.Join(spaced, "four.dump"), "name with spaces"}
	out, code, calls := f.wrap(t, nil, append([]string{"16", "pg_dump"}, args...)...)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	run := toolRun(t, calls, "pg_dump")
	mounts := 0
	for i, a := range run {
		if a == "-v" {
			mounts++
			if run[i+1] != spaced+":"+spaced && run[i+1] != other+":"+other {
				t.Errorf("unexpected mount %q", run[i+1])
			}
		}
	}
	if mounts != 2 || !run.has("-v", spaced+":"+spaced) || !run.has("-v", other+":"+other) {
		t.Errorf("want each directory mounted once, got %d mounts in %v", mounts, run)
	}
	if got := run.afterImage(); strings.Join(got, "\x00") != strings.Join(args, "\x00") {
		t.Errorf("the tool's arguments changed: %q, want %q", got, args)
	}
	if !run.has("--network", "host") {
		t.Errorf("no connection in the arguments should keep --network host: %v", run)
	}
}

func TestPGClientWrapperWithoutMountsRuns(t *testing.T) {
	f := newFakeDocker(t, true)
	out, code, calls := f.wrap(t, nil, "16", "pg_dump", "--version")
	if code != 0 || !strings.Contains(out, "pg_dump (PostgreSQL) 16.4") {
		t.Fatalf("exit %d: %s", code, out)
	}
	if run := toolRun(t, calls, "pg_dump"); run.count("-v") != 0 {
		t.Errorf("no path among the arguments, yet mounts: %v", run)
	}
}

func TestPGClientWrapperRefusesAColonInAMountedPath(t *testing.T) {
	f := newFakeDocker(t, true)
	dir := filepath.Join(t.TempDir(), "a:b")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	out, code, calls := f.wrap(t, nil, "16", "pg_dump", "-f", filepath.Join(dir, "x.dump"))
	if code != 74 || !strings.Contains(out, "colon") {
		t.Errorf("exit %d, want 74 naming the colon: %s", code, out)
	}
	if len(callsFor(calls, "run")) != 0 {
		t.Error("the tool ran despite the bad path")
	}
}

// TestPGClientWrapperIsBash32Compatible: macOS ships Bash 3.2, which has no
// associative arrays and fails on an empty array under `set -u`. No Bash 3.2 runs
// in CI, so this reads the script for what that Bash lacks.
func TestPGClientWrapperIsBash32Compatible(t *testing.T) {
	b, err := os.ReadFile(pgWrapperPath())
	if err != nil {
		t.Fatal(err)
	}
	var code []string
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			code = append(code, line)
		}
	}
	src := strings.Join(code, "\n")
	for _, bad := range []*regexp.Regexp{
		regexp.MustCompile(`\b(declare|typeset|local)\s+-[a-zA-Z]*[AnN]`),
		regexp.MustCompile(`\b(mapfile|readarray|coproc)\b`),
		regexp.MustCompile(`\$\{[^}]*(,,|\^\^)[^}]*\}`),
		regexp.MustCompile(`&>>|\|&|;;&|;&|\[\[\s+-v\b`),
		regexp.MustCompile(`\bwait\s+-[a-z]*n|\$\{?BASHPID\b|\$\{?EPOCHSECONDS\b`), // 4.3, 4.0, 5.0
	} {
		if m := bad.FindString(src); m != "" {
			t.Errorf("scripts/pg-client.sh uses %q, which Bash 3.2 does not have", m)
		}
	}
	// An array may be expanded only as ${a[@]+"${a[@]}"}: a bare "${a[@]}" is an
	// unbound variable under `set -u` when the array is empty.
	guarded := regexp.MustCompile(`\$\{(\w+)\[@\]\+"\$\{(\w+)\[@\]\}"\}`)
	for _, m := range guarded.FindAllStringSubmatch(src, -1) {
		if m[1] != m[2] {
			t.Errorf("%s guards a different array than it expands", m[0])
		}
	}
	rest := guarded.ReplaceAllString(src, "")
	for _, m := range regexp.MustCompile(`"?\$\{(\w+)\[@\]\}"?`).FindAllStringSubmatch(rest, -1) {
		if m[1] != "net" { // net always holds --network and its value
			t.Errorf("%s can be empty under set -u: expand it as ${%s[@]+\"${%s[@]}\"}", m[0], m[1], m[1])
		}
	}
	if out, err := exec.Command("bash", "-n", pgWrapperPath()).CombinedOutput(); err != nil {
		t.Errorf("bash -n: %v\n%s", err, out)
	}
}

func TestPGClientWrapperDiagnosesEachStage(t *testing.T) {
	const password = "s3cretpw"
	url := "postgres://app:" + password + "@localhost:5433/db"
	for _, tc := range []struct {
		name       string
		noDocker   bool
		env        []string
		args       []string
		wantCode   int
		wantText   string
		wantNoTool bool // the tool itself must not have run
		wantMax    time.Duration
	}{
		{name: "no container runtime", noDocker: true, args: []string{"16", "pg_dump", "--version"},
			wantCode: 70, wantText: "no container runtime", wantNoTool: true},
		{name: "daemon stopped", env: []string{"FAKE_DOCKER_INFO=down"}, args: []string{"16", "pg_dump", "--version"},
			wantCode: 71, wantText: "daemon is not reachable", wantNoTool: true},
		{name: "daemon silent", env: []string{"FAKE_DOCKER_INFO=hang", "FOLD_PG_INFO_TIMEOUT=1"}, args: []string{"16", "pg_dump", "--version"},
			wantCode: 71, wantText: "did not answer within 1s", wantNoTool: true, wantMax: 15 * time.Second},
		{name: "pull fails", env: []string{"FAKE_DOCKER_PULL=fail"}, args: []string{"16", "pg_dump", "--version"},
			wantCode: 72, wantText: "pulling image postgres:16 failed (Error response from daemon: manifest unknown)", wantNoTool: true},
		{name: "pull is bounded", env: []string{"FAKE_DOCKER_PULL=hang", "FOLD_PG_PULL_TIMEOUT=1"}, args: []string{"16", "pg_dump", "--version"},
			wantCode: 72, wantText: "pulling image postgres:16 did not finish within 1s", wantNoTool: true, wantMax: 15 * time.Second},
		{name: "tool missing from the image", env: []string{"FAKE_TOOL_RC=127"}, args: []string{"16", "pg_dump", "--version"},
			wantCode: 72, wantText: "could not start pg_dump"},
		{name: "database unreachable", env: []string{"FAKE_PGREADY_RC=2"}, args: []string{"16", "pg_dump", url},
			wantCode: 73, wantText: "localhost port 5433 does not answer from inside the container", wantNoTool: true},
		{name: "bad timeout", env: []string{"FOLD_PG_PULL_TIMEOUT=soon"}, args: []string{"16", "pg_dump", "--version"},
			wantCode: 74, wantText: "FOLD_PG_PULL_TIMEOUT must be a whole number", wantNoTool: true},
		{name: "no host", args: []string{"16", "pg_dump", "postgres://app:" + password + "@/db"},
			wantCode: 74, wantText: "names no host", wantNoTool: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeDocker(t, !tc.noDocker)
			start := time.Now()
			out, code, calls := f.wrap(t, tc.env, tc.args...)
			if code != tc.wantCode || !strings.Contains(out, tc.wantText) {
				t.Errorf("exit %d: %s\nwant exit %d containing %q", code, out, tc.wantCode, tc.wantText)
			}
			if tc.wantMax > 0 && time.Since(start) > tc.wantMax {
				t.Errorf("took %v, want under %v", time.Since(start), tc.wantMax)
			}
			if strings.Contains(out, password) {
				t.Errorf("the output carries the password: %s", out)
			}
			if tc.wantNoTool {
				for _, c := range callsFor(calls, "run") {
					if c.entrypoint() == "pg_dump" {
						t.Errorf("pg_dump ran despite the failure: %v", c)
					}
				}
			}
		})
	}
}

func TestPGClientWrapperUsesTheLocalImageWithoutPulling(t *testing.T) {
	f := newFakeDocker(t, true)
	out, code, calls := f.wrap(t, []string{"FAKE_DOCKER_PRESENT=1"}, "16", "pg_dump", "--version")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if n := len(callsFor(calls, "pull")); n != 0 {
		t.Errorf("pulled %d times although the image is local", n)
	}
}

func TestPGClientWrapperRunsByImageID(t *testing.T) {
	t.Run("pulls once and runs the ID, not the tag", func(t *testing.T) {
		f := newFakeDocker(t, true)
		_, code, calls := f.wrap(t, nil, "16", "pg_dump", "--version")
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
		pulls := callsFor(calls, "pull")
		if len(pulls) != 1 || !pulls[0].has("postgres:16") {
			t.Errorf("pulls %v, want one of postgres:16", pulls)
		}
		run := toolRun(t, calls, "pg_dump")
		if !run.has("--entrypoint", "pg_dump", "sha256:feedbeef") || run.count("postgres:16") != 0 {
			t.Errorf("the tool should run on the image ID: %v", run)
		}
	})
	t.Run("--resolve prints the ID", func(t *testing.T) {
		f := newFakeDocker(t, true)
		out, code, calls := f.wrap(t, nil, "--resolve", "16")
		if code != 0 || !strings.Contains(out, "sha256:feedbeef") {
			t.Fatalf("exit %d: %s", code, out)
		}
		for _, c := range callsFor(calls, "run") {
			t.Errorf("--resolve started a container: %v", c)
		}
	})
	t.Run("a recorded ID is used as it is", func(t *testing.T) {
		f := newFakeDocker(t, true)
		_, code, calls := f.wrap(t, []string{"FOLD_PG_IMAGE_ID=sha256:feedbeef"}, "16", "pg_dump", "--version")
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
		if n := len(callsFor(calls, "pull")); n != 0 {
			t.Errorf("pulled %d times although an ID was recorded", n)
		}
		if run := toolRun(t, calls, "pg_dump"); !run.has("--entrypoint", "pg_dump", "sha256:feedbeef") {
			t.Errorf("the recorded ID is not the image run: %v", run)
		}
	})
	t.Run("a recorded ID that is gone is an error, not a pull", func(t *testing.T) {
		f := newFakeDocker(t, true)
		out, code, calls := f.wrap(t, []string{"FOLD_PG_IMAGE_ID=sha256:feedbeef", "FAKE_DOCKER_GONE=1"}, "16", "pg_dump", "--version")
		if code != 72 || !strings.Contains(out, "is not available locally") {
			t.Errorf("exit %d: %s", code, out)
		}
		if len(callsFor(calls, "pull")) != 0 {
			t.Error("pulled instead of failing")
		}
	})
	t.Run("an explicit digest is kept as given", func(t *testing.T) {
		f := newFakeDocker(t, true)
		digest := "postgres@sha256:" + strings.Repeat("ab", 32)
		_, code, calls := f.wrap(t, []string{"FOLD_PG_IMAGE=" + digest}, "16", "pg_dump", "--version")
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
		if pulls := callsFor(calls, "pull"); len(pulls) != 1 || !pulls[0].has(digest) {
			t.Errorf("pulls %v, want the digest %s exactly", pulls, digest)
		}
	})
}

func TestPGClientWrapperConnectionForms(t *testing.T) {
	root := t.TempDir()
	sock := filepath.Join(root, "sock")
	spacedSock := filepath.Join(root, "sock dir")
	for _, d := range []string{sock, spacedSock} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	type probe struct{ host, port string }
	for _, tc := range []struct {
		name    string
		args    []string
		network string // the container network, for the probe and the tool
		mount   string // the one directory mounted, "" for none
		probe   probe
	}{
		{name: "TCP URL", args: []string{"postgres://u:p@localhost:5433/db"}, network: "host", probe: probe{"localhost", "5433"}},
		{name: "TCP URL default port", args: []string{"postgres://u@127.0.0.1/db"}, network: "host", probe: probe{"127.0.0.1", "5432"}},
		{name: "TCP IPv6", args: []string{"postgres://u@[::1]:5434/db"}, network: "host", probe: probe{"::1", "5434"}},
		{name: "TCP -h and -p", args: []string{"-h", "db.internal", "-p", "5435", "dbname"}, network: "host", probe: probe{"db.internal", "5435"}},
		{name: "TCP conninfo", args: []string{"-d", "host=db.internal port=5436 dbname=x"}, network: "host", probe: probe{"db.internal", "5436"}},
		{name: "socket in the URL query", args: []string{"postgres://u@localhost/db?host=" + sock}, network: "none", mount: sock, probe: probe{sock, "5432"}},
		{name: "socket in the URL authority", args: []string{"postgres://u@" + strings.ReplaceAll(sock, "/", "%2F") + ":5437/db"}, network: "none", mount: sock, probe: probe{sock, "5437"}},
		{name: "socket with -h", args: []string{"-h", spacedSock, "-p", "5438", "dbname"}, network: "none", mount: spacedSock, probe: probe{spacedSock, "5438"}},
		{name: "socket conninfo", args: []string{"-d", "host=" + sock + " dbname=x"}, network: "none", mount: sock, probe: probe{sock, "5432"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeDocker(t, true)
			out, code, calls := f.wrap(t, nil, append([]string{"16", "pg_dump"}, tc.args...)...)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, out)
			}
			ready := toolRun(t, calls, "pg_isready")
			run := toolRun(t, calls, "pg_dump")
			for name, c := range map[string]dockerCall{"probe": ready, "tool": run} {
				if !c.has("--network", tc.network) {
					t.Errorf("%s: want --network %s in %v", name, tc.network, c)
				}
				if tc.mount == "" && c.count("-v") != 0 {
					t.Errorf("%s: unexpected mount in %v", name, c)
				}
				if tc.mount != "" && (c.count("-v") != 1 || !c.has("-v", tc.mount+":"+tc.mount)) {
					t.Errorf("%s: want only %s mounted, got %v", name, tc.mount, c)
				}
			}
			if !ready.has("-h", tc.probe.host, "-p", tc.probe.port) {
				t.Errorf("probe asked the wrong place: %v, want -h %s -p %s", ready, tc.probe.host, tc.probe.port)
			}
			if got := run.afterImage(); strings.Join(got, "\x00") != strings.Join(tc.args, "\x00") {
				t.Errorf("the tool's arguments changed: %q, want %q", got, tc.args)
			}
		})
	}

	t.Run("a socket directory that is not there", func(t *testing.T) {
		f := newFakeDocker(t, true)
		out, code, _ := f.wrap(t, nil, "16", "pg_dump", "-h", filepath.Join(root, "missing"), "dbname")
		if code != 74 || !strings.Contains(out, "does not exist") {
			t.Errorf("exit %d: %s", code, out)
		}
	})
}

// TestPGClientWrapperProbeNamesAUser: the container runs as the caller's uid,
// which the image has no passwd entry for, so a pg_isready with no -U exits 3
// ("no attempt") before it connects. The fake docker reproduces that, so a probe
// without -U fails here as it did against a real server.
func TestPGClientWrapperProbeNamesAUser(t *testing.T) {
	f := newFakeDocker(t, true)
	out, code, calls := f.wrap(t, nil, "16", "pg_dump", "postgres://u:p@127.0.0.1:5433/db")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if ready := toolRun(t, calls, "pg_isready"); !ready.has("-U", "probe") {
		t.Errorf("the probe names no user: %v", ready)
	}

	// Exit 3 from a probe is not a silent database: it gets its own message and
	// never the "unreachable" diagnosis (73).
	out, code, _ = f.wrap(t, []string{"FAKE_PGREADY_RC=3"}, "16", "pg_dump", "postgres://u:p@127.0.0.1:5433/db")
	if code != 74 || !strings.Contains(out, "made no attempt (exit 3)") || strings.Contains(out, "rejected") {
		t.Errorf("exit %d: %s", code, out)
	}
}

// TestPGClientWrapperProbeWithRealDocker runs the real wrapper, docker and
// pg_isready against a local port nothing listens on. The probe must get as far
// as connecting and report silence (73), not "no attempt" (74). It skips where
// Docker is not usable, as the restore tests do, and fails there under CI.
func TestPGClientWrapperProbeWithRealDocker(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("docker is not on PATH")
		}
		t.Skip("docker is not on PATH")
	}
	wrapper := pgWrapperPath()
	id, note, unavailable := resolvePGImage(wrapper, 16)
	if id == "" {
		if unavailable && os.Getenv("CI") == "" {
			t.Skipf("Docker is not usable: %s", note)
		}
		t.Fatalf("no image to probe with: %s", note)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close() // the port is now closed and, for the next seconds, unused

	cmd := exec.Command(wrapper, "16", "pg_dump", "-f", filepath.Join(t.TempDir(), "x.dump"),
		fmt.Sprintf("postgres://probe:probe@127.0.0.1:%d/probe", port))
	cmd.Env = append(os.Environ(), "FOLD_PG_IMAGE_ID="+id, "FOLD_PG_INFO_TIMEOUT=60")
	out, err := cmd.CombinedOutput()
	if code := exitCodeOf(err); code != 73 || !strings.Contains(string(out), "does not answer from inside the container") {
		t.Errorf("exit %d: %s\nwant 73, the probe saw a closed port", code, out)
	}
}

// TestPGClientWrapperRefusesConninfoItCannotRead: a connection string that
// libpq reads with quotes, backslashes or blanks around "=" cannot be split on
// blanks, because a password can hold "port=5432" or "host=/tmp". The wrapper
// refuses it before taking a word from it, with a message that says nothing of
// its content, and starts no container.
func TestPGClientWrapperRefusesConninfoItCannotRead(t *testing.T) {
	root := t.TempDir()
	sock := filepath.Join(root, "sock")
	if err := os.Mkdir(sock, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
		leak []string // fragments of the string the output must not repeat
	}{
		{"port inside a quoted password", []string{"-d", "host=127.0.0.1 user=app dbname=db password='prefix port=pa55w0rd'"}, []string{"pa55w0rd", "prefix"}},
		{"host inside a quoted password", []string{"-d", "dbname=db password='prefix host=" + sock + " suffix'"}, []string{"prefix", "suffix"}},
		{"host inside a double-quoted password", []string{"-d", `user=app password="pa55 host=` + sock + `" host=db.internal`}, []string{"pa55"}},
		{"port first inside a quoted password", []string{"-d", "port='1 pa55w0rd' host=db.internal"}, []string{"pa55w0rd"}},
		{"a backslash-escaped blank", []string{"-d", `host=db.internal password=pa55\ port=5432`}, []string{"pa55"}},
		{"an escaped quote", []string{"-d", `host=db.internal password=pa55\'w0rd`}, []string{"pa55", "w0rd"}},
		{"blanks around =", []string{"-d", "host = db.internal port=5432"}, []string{"db.internal"}},
		{"a blank after =", []string{"-d", "host= db.internal port=5432"}, []string{"db.internal"}},
		{"a blank before =", []string{"-d", "host =db.internal port=5432"}, []string{"db.internal"}},
		{"an empty value", []string{"-d", "dbname= host=db.internal"}, []string{"db.internal"}},
		{"a carriage return", []string{"-d", "host=db.internal\rport=5432"}, []string{"db.internal"}},
		{"a bare string", []string{"host=db.internal password='pa55w0rd'"}, []string{"pa55w0rd"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeDocker(t, true)
			out, code, calls := f.wrap(t, nil, append([]string{"16", "pg_dump"}, tc.args...)...)
			if code != 74 || !strings.Contains(out, "FOLD_PG_BIN") || !strings.Contains(out, "does not read") {
				t.Errorf("exit %d: %s\nwant 74 pointing at a URL or FOLD_PG_BIN", code, out)
			}
			for _, leak := range tc.leak {
				if strings.Contains(out, leak) {
					t.Errorf("the output repeats %q from the connection string: %s", leak, out)
				}
			}
			for _, c := range callsFor(calls, "run") {
				t.Errorf("a container started for a connection string that was refused: %v", c)
			}
		})
	}

	// Plain strings keep working; a word that only looks like a setting inside
	// another setting's value is not one.
	t.Run("host= inside an unquoted value", func(t *testing.T) {
		f := newFakeDocker(t, true)
		out, code, calls := f.wrap(t, nil, "16", "pg_dump", "-d", "host=db.internal password=xhost="+sock+" port=5436 dbname=x")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, out)
		}
		ready := toolRun(t, calls, "pg_isready")
		if !ready.has("-h", "db.internal", "-p", "5436") || !ready.has("--network", "host") || ready.count("-v") != 0 {
			t.Errorf("the probe followed a word inside a password: %v", ready)
		}
	})
	t.Run("a port that is not a number is not repeated", func(t *testing.T) {
		f := newFakeDocker(t, true)
		out, code, _ := f.wrap(t, nil, "16", "pg_dump", "postgres://u:pw@localhost:pa55w0rd/db")
		if code != 74 || !strings.Contains(out, "not a number") || strings.Contains(out, "pa55w0rd") {
			t.Errorf("exit %d: %s", code, out)
		}
	})
	t.Run("a path named like a host setting is a path", func(t *testing.T) {
		f := newFakeDocker(t, true)
		out, code, calls := f.wrap(t, nil, "16", "pg_dump", "-f", filepath.Join(sock, "host=a b'c.dump"))
		if code != 0 {
			t.Fatalf("exit %d: %s", code, out)
		}
		run := toolRun(t, calls, "pg_dump")
		if len(callsFor(calls, "run")) != 1 || !run.has("-v", sock+":"+sock) || !run.has("--network", "host") {
			t.Errorf("a file argument was read as a connection: %v", calls)
		}
	})
}

// pids reads the process ids the fake docker recorded.
func (f *fakeDocker) pids(t *testing.T) []int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, "pids"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var pids []int
	for _, s := range strings.Fields(string(b)) {
		if n, err := strconv.Atoi(s); err == nil {
			pids = append(pids, n)
		}
	}
	return pids
}

// alive reports whether the process runs. A zombie is dead: it waits only to be
// reaped.
func alive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		if i := bytes.LastIndexByte(b, ')'); i >= 0 && i+2 < len(b) && b[i+2] == 'Z' {
			return false
		}
	}
	return true
}

// expectStopped fails unless every recorded process is gone within a second
// (they were killed already; this only waits for the kernel), and kills any that
// is not, so a failure does not leave them running.
func (f *fakeDocker) expectStopped(t *testing.T) {
	t.Helper()
	pids := f.pids(t)
	if len(pids) == 0 {
		t.Fatal("the fake docker started no process to stop: the scenario did not run")
	}
	deadline := time.Now().Add(time.Second)
	for _, pid := range pids {
		for alive(pid) && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if alive(pid) {
			t.Errorf("process %d is still running", pid)
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

// sawRemove reports whether the wrapper removed the container its run was
// given: a `docker rm -f <name>` for the --name of a `docker run`.
func sawRemove(calls []dockerCall) bool {
	names := map[string]bool{}
	for _, c := range callsFor(calls, "run") {
		for i, a := range c {
			if a == "--name" && i+1 < len(c) {
				names[c[i+1]] = true
			}
		}
	}
	for _, c := range callsFor(calls, "rm") {
		if c.has("-f") && names[c[len(c)-1]] {
			return true
		}
	}
	return false
}

// TestPGClientWrapperDeadlinesHoldAgainstStubbornCommands: a command that ignores
// SIGTERM and starts children (one of them a container the daemon owns) must
// still end at its deadline plus the grace, with nothing of it left running and
// its container removed. A fixture that dies on TERM cannot show this.
func TestPGClientWrapperDeadlinesHoldAgainstStubbornCommands(t *testing.T) {
	url := "postgres://u:p@127.0.0.1:5433/db"
	for _, tc := range []struct {
		stage     string
		timeout   string
		args      []string
		wantCode  int
		wantText  string
		container bool
	}{
		{"info", "FOLD_PG_INFO_TIMEOUT", []string{"16", "pg_dump", "--version"}, 71, "did not answer within 1s", false},
		{"pull", "FOLD_PG_PULL_TIMEOUT", []string{"16", "pg_dump", "--version"}, 72, "did not finish within 1s", false},
		{"probe", "FOLD_PG_INFO_TIMEOUT", []string{"16", "pg_dump", url}, 73, "does not answer", true},
		{"tool", "FOLD_PG_DISCOVERY_TIMEOUT", []string{"16", "pg_dump", "--version"}, 72, "did not finish within 1s", true},
	} {
		t.Run(tc.stage, func(t *testing.T) {
			f := newFakeDocker(t, true)
			start := time.Now()
			out, code, calls := f.wrap(t, []string{"FAKE_STUBBORN=" + tc.stage, tc.timeout + "=1", "FOLD_PG_KILL_GRACE=1"}, tc.args...)
			if code != tc.wantCode || !strings.Contains(out, tc.wantText) {
				t.Errorf("exit %d: %s\nwant exit %d containing %q", code, out, tc.wantCode, tc.wantText)
			}
			// 1s limit + 1s grace, and the poll of each; far under what a command
			// that is merely waited for would take (it sleeps a minute).
			if d := time.Since(start); d > 15*time.Second {
				t.Errorf("took %v: the deadline did not hold", d)
			}
			f.expectStopped(t)
			if tc.container && !sawRemove(calls) {
				t.Errorf("the container was not removed by name: %v", calls)
			}
		})
	}
}

// TestPGClientWrapperCleansUpWhenToldToStop: a caller that gives up (a test
// deadline, Ctrl-C, a terminal closing) signals the wrapper, and the dump or
// restore it started must not be left running with its container.
func TestPGClientWrapperCleansUpWhenToldToStop(t *testing.T) {
	for _, tc := range []struct {
		sig  syscall.Signal
		want int
	}{
		{syscall.SIGTERM, 143},
		{syscall.SIGINT, 130},
		{syscall.SIGHUP, 129},
	} {
		t.Run(tc.sig.String(), func(t *testing.T) {
			if signal.Ignored(tc.sig) {
				t.Skipf("%v is ignored in this process, so a shell it starts cannot trap it", tc.sig)
			}
			f := newFakeDocker(t, true)
			cmd := exec.Command(pgWrapperPath(), "16", "pg_dump", "-Fc")
			cmd.Env = []string{"PATH=" + f.path, "TMPDIR=" + f.dir, "FAKE_DOCKER_DIR=" + f.dir,
				"FAKE_STUBBORN=tool", "FOLD_PG_KILL_GRACE=1"}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			t.Cleanup(func() {
				_ = cmd.Process.Kill()
				for _, pid := range f.pids(t) {
					_ = syscall.Kill(-pid, syscall.SIGKILL)
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			})
			deadline := time.Now().Add(30 * time.Second)
			for {
				if _, err := os.Stat(filepath.Join(f.dir, "started")); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("the tool never started")
				}
				time.Sleep(20 * time.Millisecond)
			}
			stopped := time.Now()
			if err := cmd.Process.Signal(tc.sig); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if code := exitCodeOf(err); code != tc.want {
					t.Errorf("exit %d, want %d", code, tc.want)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("the wrapper did not end after the signal")
			}
			if d := time.Since(stopped); d > 15*time.Second {
				t.Errorf("took %v to end", d)
			}
			f.expectStopped(t)
			if !sawRemove(f.calls(t)) {
				t.Errorf("the container was not removed by name: %v", f.calls(t))
			}
			if left, _ := filepath.Glob(filepath.Join(f.dir, "pg-client.*")); len(left) != 0 {
				t.Errorf("the wrapper left its work directory: %v", left)
			}
		})
	}
}

// TestPGRunStopsTheWholeGroup: the Go side of the deadline. A wrapper killed by
// exec's own deadline (the pid alone) would leave its docker behind, so pgRun
// signals the process group, and kills what ignored the TERM.
func TestPGRunStopsTheWholeGroup(t *testing.T) {
	dir := t.TempDir()
	pids := filepath.Join(dir, "pids")
	script := filepath.Join(dir, "stubborn")
	body := `#!/usr/bin/env bash
trap '' TERM
bash -c 'trap "" TERM; echo $$ >>"$1"; sleep 60' child "$1" &
echo $$ >>"$1"
wait
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	var out bytes.Buffer
	err := pgRun(700*time.Millisecond, 500*time.Millisecond, &out, &out, script, pids)
	if err == nil {
		t.Fatal("a command that overran its limit reported success")
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("took %v", d)
	}
	b, _ := os.ReadFile(pids)
	got := strings.Fields(string(b))
	if len(got) != 2 {
		t.Fatalf("the script recorded %v, want its pid and its child's", got)
	}
	for _, s := range got {
		pid, _ := strconv.Atoi(s)
		deadline := time.Now().Add(time.Second)
		for alive(pid) && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if alive(pid) {
			t.Errorf("process %d survived the deadline", pid)
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

func TestPGCandidatesOrder(t *testing.T) {
	found := func(string) (string, error) { return "/usr/bin/pg_dump", nil }
	missing := func(string) (string, error) { return "", exec.ErrNotFound }
	only := func(name string) func(string) (string, error) {
		return func(n string) (string, error) {
			if n == name {
				return "/usr/bin/" + n, nil
			}
			return "", exec.ErrNotFound
		}
	}
	const wrapper = "/repo/scripts/pg-client.sh"
	join := func(c [][]string) string {
		var parts []string
		for _, argv := range c {
			parts = append(parts, strings.Join(argv, " "))
		}
		return strings.Join(parts, " | ")
	}
	for _, tc := range []struct {
		name string
		bin  string
		find func(string) (string, error)
		want string
	}{
		{"FOLD_PG_BIN stands alone", "/opt/pg16/bin", found, "/opt/pg16/bin/pg_dump"},
		{"native then the container", "", found, "/usr/bin/pg_dump | /repo/scripts/pg-client.sh 16 pg_dump"},
		{"container only", "", only("docker"), "/repo/scripts/pg-client.sh 16 pg_dump"},
		{"native only", "", only("pg_dump"), "/usr/bin/pg_dump"},
		{"nothing", "", missing, ""},
	} {
		if got := join(pgCandidates("pg_dump", 16, wrapper, tc.bin, tc.find)); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestCheckPGClientKeepsTheMajorStrict(t *testing.T) {
	script := func(t *testing.T, body string) []string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "client")
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return []string{p}
	}
	if note, ok, _ := checkPGClient(script(t, `echo "pg_dump (PostgreSQL) 16.4"`), 16, false); !ok {
		t.Errorf("a matching client was refused: %s", note)
	}
	note, ok, unavailable := checkPGClient(script(t, `echo "pg_dump (PostgreSQL) 18.1"`), 16, false)
	if ok || unavailable || !strings.Contains(note, "not PostgreSQL 16") {
		t.Errorf("a PostgreSQL 18 client passed for a 16 server: ok=%v unavailable=%v note=%q", ok, unavailable, note)
	}
	note, ok, unavailable = checkPGClient(script(t, `echo "boom" >&2; exit 3`), 16, false)
	if ok || unavailable || !strings.Contains(note, "boom") {
		t.Errorf("a failing client passed or counted as unavailable: ok=%v unavailable=%v note=%q", ok, unavailable, note)
	}
	// Only the wrapper's own "no runtime" codes read as unavailable, and a native
	// client that happens to exit 71 does not.
	if _, ok, unavailable = checkPGClient(script(t, `echo "no daemon" >&2; exit 71`), 16, true); ok || !unavailable {
		t.Errorf("the wrapper's exit 71 should read as unavailable: ok=%v unavailable=%v", ok, unavailable)
	}
	if _, ok, unavailable = checkPGClient(script(t, `exit 71`), 16, false); ok || unavailable {
		t.Errorf("a native client's exit 71 must stay a failure: ok=%v unavailable=%v", ok, unavailable)
	}
	if _, ok, unavailable = checkPGClient(script(t, `echo "pulling failed" >&2; exit 72`), 16, true); ok || unavailable {
		t.Errorf("an unusable image must fail, not skip: ok=%v unavailable=%v", ok, unavailable)
	}
}
