package main

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// ---------------------------------------------------------------------------
// recoveryPolicy (R4-RP): the constant, the file and fold version --json.
// ---------------------------------------------------------------------------

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
	if recoveryPolicyLevel != 2 {
		t.Errorf("T07B-2b reports recovery level 2, found %d", recoveryPolicyLevel)
	}
	// The check fails when either side changes alone.
	// T07B-2b raised the level to 2: a file still at 1 (or a constant still at 1
	// beside a file at 2) is a mismatch, as is anything that is not an integer.
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
	if strings.TrimSpace(string(b)) != "2" {
		t.Errorf("deploy/recovery-policy holds %q, want the single integer 2 (T07B-2b)", string(b))
	}
}

// ---------------------------------------------------------------------------
// Writers of restore_fence: only the boot step and takeRestoreTicket.
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// The barrier at run time
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// No false detection, on the real router with one App and a restart recorder.
// ---------------------------------------------------------------------------

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
		waitFor(t, 10*time.Second, "B waiting on the turn", func() bool { return s.app.fenceTurnWaiters() == 1 })
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
		waitFor(t, 5*time.Second, "B waiting on the turn", func() bool { return s.app.fenceTurnWaiters() == 1 })
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

func waitFor(t *testing.T, within time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// ---------------------------------------------------------------------------
// Restores while the process keeps running (pg_dump -Fc, pg_restore).
// ---------------------------------------------------------------------------

// pgClients maps a client tool to the command that runs it: the tool itself, or
// scripts/pg-client.sh running it from the server's own image.
var pgClients = map[string][]string{}

var pgMajorRE = regexp.MustCompile(`\) (\d+)(?:\.\d+)?`)

// fenceTools finds a pg_dump and a pg_restore of the SERVER's major version. A
// newer client writes settings an older server rejects (a PostgreSQL 18
// pg_restore sends "SET transaction_timeout" to a server 16), and the restore
// then fails. The errors stay errors: the tests never ignore them. Order:
//   - FOLD_PG_BIN, a directory of tools (alone, no fallback);
//   - the tool on PATH, if its major matches;
//   - scripts/pg-client.sh, which runs the tool from the postgres:<major> image
//     (needs Docker, which CI and make dev already use for Postgres).
//
// With nothing usable the test skips locally and fails under CI; a client that
// exists but does not match always fails, with what to do.
func fenceTools(t *testing.T) {
	t.Helper()
	var num int
	if err := testdb.Shared(t).Raw("SHOW server_version_num").Scan(&num).Error; err != nil {
		t.Fatal(err)
	}
	major := num / 10000
	_, thisFile, _, _ := runtime.Caller(0)
	wrapper := filepath.Join(filepath.Dir(thisFile), "scripts", "pg-client.sh")
	for _, tool := range []string{"pg_dump", "pg_restore"} {
		if pgClients[tool] != nil {
			continue // found by an earlier test of this process
		}
		var candidates [][]string
		if dir := os.Getenv("FOLD_PG_BIN"); dir != "" {
			candidates = append(candidates, []string{filepath.Join(dir, tool)})
		} else {
			if p, err := exec.LookPath(tool); err == nil {
				candidates = append(candidates, []string{p})
			}
			if _, err := exec.LookPath("docker"); err == nil {
				candidates = append(candidates, []string{wrapper, strconv.Itoa(major), tool})
			}
		}
		var notes []string
		for _, argv := range candidates {
			out, err := exec.Command(argv[0], append(argv[1:len(argv):len(argv)], "--version")...).CombinedOutput()
			m := pgMajorRE.FindSubmatch(out)
			switch {
			case err != nil:
				notes = append(notes, fmt.Sprintf("%s: %v: %s", strings.Join(argv, " "), err, strings.TrimSpace(string(out))))
			case m == nil || string(m[1]) != strconv.Itoa(major):
				notes = append(notes, fmt.Sprintf("%s reports %q, not PostgreSQL %d", strings.Join(argv, " "), strings.TrimSpace(string(out)), major))
			default:
				pgClients[tool] = argv
			}
			if pgClients[tool] != nil {
				break
			}
		}
		if pgClients[tool] != nil {
			continue
		}
		if len(candidates) == 0 && os.Getenv("CI") == "" {
			t.Skipf("%s is not installed and Docker is not available; the restore tests need PostgreSQL %d client tools", tool, major)
		}
		t.Fatalf("no %s of PostgreSQL %d (the server's version) is usable; set FOLD_PG_BIN to a directory of PostgreSQL %d client tools, or make Docker available for scripts/pg-client.sh:\n%s",
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
				waitFor(t, 15*time.Second, "A's COMMIT to be sent and held", func() bool {
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
			waitFor(t, 15*time.Second, "B waiting on the turn", func() bool { return s.app.fenceTurnWaiters() == 1 })
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
					if strings.Contains(e.SQL, "pg_advisory_xact_lock") || strings.Contains(e.SQL, "restore_fence") {
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
	waitFor(t, 10*time.Second, "B blocked by A's backend", func() bool {
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
		waitFor(t, 5*time.Second, "the commit to reach the server", func() bool { return r.deletedUser(target.ID) })
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
