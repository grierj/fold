package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"fold/internal/guardkit"
)

// The throttle guards are parse-only analysis (go/parser and go/ast, through
// guardkit), not a type checker. The key-position rules read package main, where
// throttleKey lives; the login_throttles SQL rule reads the whole production tree. Their limits, stated
// here, in the T19 note and in the AGENTS.md row:
//
//   - Identifiers match by name, so shadowing and same-named locals are
//     approximated: a local const named like a key variable would make a call
//     fail, and a method named like a helper on another type is held to the
//     helper's rules.
//   - They do not see a key assembled inside the SQL text of a listed helper,
//     SQL sent around GORM (Fold sends none), reflection, a generic parameter
//     instantiated over string, or key-position mistakes in code outside package
//     main (throttleKey is unexported, so no other package can build one).
//   - They prove which source shapes reach a key position, not that a builder
//     was given the right argument. The exact-output tables below pin the
//     arguments each builder takes.

const throttleKeysFile = "throttle_keys.go"

// throttleSQLAllowed names the only functions that may hold login_throttles
// SQL or models.LoginThrottle. eraseOwnedThrottleKeysIn (account_erasure.go)
// is the erasure's one statement over the table.
var throttleSQLAllowed = map[string]map[string]bool{
	"api_auth.go": {
		"apiLogin": true, "reauthenticate": true, "reserveLoginAttempt": true,
		"countThrottleHit": true, "lockLoginKeysAtLimit": true, "pruneLoginThrottles": true,
	},
	"api_occurrence_mutations.go": {"managerDateAddAllowedIn": true},
	"account_erasure.go":          {"eraseOwnedThrottleKeysIn": true},
}

// loginThrottlesTable matches the table name as a whole word, so a schema
// qualifier (public.login_throttles), quotes and any letter case count, while
// a longer identifier such as x_login_throttles does not.
var loginThrottlesTable = regexp.MustCompile(`(?i)(?:^|[^\w])login_throttles\b`)

// throttleJobKind is the daily job kind that shares the table's name. It is
// exempt only as the first argument of a daily(...) call.
const throttleJobKind = "cleanup.login_throttles"

// throttleProductionSources reads the whole production tree, so a throttle key or
// login_throttles statement moved into a subpackage is still checked. It fails
// if throttle_keys.go is missing.
func throttleProductionSources(t *testing.T) guardkit.Sources {
	t.Helper()
	out := guardkit.Production(t)
	if _, ok := out[throttleKeysFile]; !ok {
		t.Fatalf("%s is missing", throttleKeysFile)
	}
	return out
}

func isThrottleKeyType(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "throttleKey"
}

// throttleKeyElt returns the element identifier of a []throttleKey type.
func throttleKeyElt(e ast.Expr) *ast.Ident {
	arr, ok := e.(*ast.ArrayType)
	if !ok || arr.Len != nil || !isThrottleKeyType(arr.Elt) {
		return nil
	}
	return arr.Elt.(*ast.Ident)
}

// throttleBuilders returns the builders declared in throttle_keys.go: plain
// functions whose only result is a throttleKey.
func throttleBuilders(file *ast.File) map[string]*ast.FuncDecl {
	out := map[string]*ast.FuncDecl{}
	for _, d := range file.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
			continue
		}
		res := fn.Type.Results.List[0]
		if len(res.Names) <= 1 && isThrottleKeyType(res.Type) {
			out[fn.Name.Name] = fn
		}
	}
	return out
}

// throttleKeyViolations applies the key-position guards (a) to (d) to the
// given sources, keyed by file name. Comments are not literals and are never
// read.
func throttleKeyViolations(sources map[string]string) []string {
	parsed := guardkit.Sources(sources).Parse(0)
	fset := parsed.Fset
	bad := append([]string(nil), parsed.Problems...)
	files := map[string]*ast.File{}
	var names []string
	// throttleKey is unexported, so only package main can build or pass one;
	// the key-position rules read that package alone (names come out sorted).
	for _, pf := range parsed.Package("main") {
		names = append(names, pf.Path)
		files[pf.Path] = pf.AST
	}
	keysFile, ok := files[throttleKeysFile]
	if !ok {
		return append(bad, throttleKeysFile+": missing")
	}
	at := func(n ast.Node) string { return fset.Position(n.Pos()).String() }

	builders := throttleBuilders(keysFile)
	var prefixes []string
	for name, fn := range builders {
		var lit *ast.BasicLit
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if b, ok := n.(*ast.BasicLit); ok && b.Kind == token.STRING && lit == nil {
				lit = b
			}
			return true
		})
		if lit == nil {
			bad = append(bad, at(fn)+": builder "+name+" has no string literal to read its prefix from")
			continue
		}
		v, err := strconv.Unquote(lit.Value)
		if err != nil {
			v = lit.Value
		}
		prefix, _, _ := strings.Cut(v, "%")
		prefixes = append(prefixes, prefix)
	}
	sort.Strings(prefixes)

	// Functions and methods with a throttleKey or []throttleKey parameter, by
	// name, with the parameter positions (the receiver is not counted).
	helpers := map[string]map[int]bool{}
	consts := map[string]bool{}
	declNames := map[*ast.Ident]bool{}
	for _, name := range names {
		f := files[name]
		if f == nil {
			continue
		}
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok {
				declNames[fn.Name] = true
				pos := 0
				for _, field := range fn.Type.Params.List {
					n := max(1, len(field.Names))
					if isThrottleKeyType(field.Type) || throttleKeyElt(field.Type) != nil {
						if helpers[fn.Name.Name] == nil {
							helpers[fn.Name.Name] = map[int]bool{}
						}
						for i := 0; i < n; i++ {
							helpers[fn.Name.Name][pos+i] = true
						}
					}
					pos += n
				}
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if gd, ok := n.(*ast.GenDecl); ok && gd.Tok == token.CONST {
				for _, spec := range gd.Specs {
					for _, id := range spec.(*ast.ValueSpec).Names {
						consts[id.Name] = true
					}
				}
			}
			return true
		})
	}
	watched := map[string]bool{}
	for name := range builders {
		watched[name] = true
	}
	for name := range helpers {
		watched[name] = true
	}

	isBuilderCall := func(e ast.Expr) bool {
		call, ok := guardkit.Unparen(e).(*ast.CallExpr)
		if !ok {
			return false
		}
		id, ok := guardkit.Unparen(call.Fun).(*ast.Ident)
		return ok && builders[id.Name] != nil
	}
	okKeyArg := func(e ast.Expr) bool {
		switch x := guardkit.Unparen(e).(type) {
		case *ast.CallExpr:
			return isBuilderCall(x)
		case *ast.IndexExpr:
			call, ok := guardkit.Unparen(x.X).(*ast.CallExpr)
			if !ok {
				return false
			}
			sel, ok := guardkit.Unparen(call.Fun).(*ast.SelectorExpr)
			return ok && sel.Sel.Name == "targeted"
		case *ast.CompositeLit:
			return throttleKeyElt(x.Type) != nil // its elements are checked by (a)
		case *ast.Ident:
			return !consts[x.Name]
		case *ast.SelectorExpr:
			return !consts[x.Sel.Name]
		}
		return false
	}

	for _, name := range names {
		if name == throttleKeysFile || files[name] == nil {
			continue
		}
		f := files[name]

		// (a) The type name appears only in func declaration signatures and as
		// the element of a []throttleKey literal made of builder calls.
		allowed := map[*ast.Ident]bool{}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			for _, list := range []*ast.FieldList{fn.Type.Params, fn.Type.Results} {
				if list == nil {
					continue
				}
				for _, field := range list.List {
					if isThrottleKeyType(field.Type) {
						allowed[field.Type.(*ast.Ident)] = true
					} else if elt := throttleKeyElt(field.Type); elt != nil {
						allowed[elt] = true
					}
				}
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			elt := throttleKeyElt(lit.Type)
			if elt == nil {
				return true
			}
			for _, e := range lit.Elts {
				if !isBuilderCall(e) {
					bad = append(bad, at(e)+": a []throttleKey literal element is not a builder call from "+throttleKeysFile)
					return true
				}
			}
			allowed[elt] = true
			return true
		})
		ast.Inspect(f, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == "throttleKey" && !allowed[id] {
				bad = append(bad, at(id)+": uses the throttleKey type outside a func signature or a []throttleKey{builder calls} literal; build keys with the functions in "+throttleKeysFile)
			}
			return true
		})

		// (b) No string literal looks like a key.
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v, err := strconv.Unquote(lit.Value)
			if err != nil {
				v = lit.Value
			}
			for _, p := range prefixes {
				if v == p || (strings.HasPrefix(v, p) && v[len(p)] != ' ') {
					bad = append(bad, at(lit)+fmt.Sprintf(": string literal %s starts with the throttle key prefix %q; build keys with the functions in %s", lit.Value, p, throttleKeysFile))
					break
				}
			}
			return true
		})

		// (c) A key argument is a builder call, an index of a .targeted()
		// result, or a name no const declares.
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			positions := helpers[guardkit.Callee(call)]
			for pos := range positions {
				if pos >= len(call.Args) {
					continue
				}
				if !okKeyArg(call.Args[pos]) {
					bad = append(bad, at(call.Args[pos])+fmt.Sprintf(": argument %d of %s is not a builder call, an index of .targeted() or a name no const declares", pos, guardkit.Callee(call)))
				}
			}
			return true
		})

		// (d) Helpers and builders appear only in call position.
		callFuns := map[ast.Node]bool{}
		ast.Inspect(f, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				callFuns[guardkit.Unparen(call.Fun)] = true
			}
			return true
		})
		guardkit.InspectStack(f, func(n ast.Node, stack []ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && watched[id.Name] && !declNames[id] {
				inCall := callFuns[id]
				if len(stack) > 1 {
					if sel, ok := stack[len(stack)-2].(*ast.SelectorExpr); ok && sel.Sel == id {
						inCall = callFuns[sel]
					}
				}
				if !inCall {
					bad = append(bad, at(id)+": "+id.Name+" is used as a value; call it directly so its key arguments are checked")
				}
			}
			return true
		})
	}
	return bad
}

// throttleSQLViolations rejects login_throttles SQL and models.LoginThrottle
// outside the listed functions, apart from literal erasure inventory metadata.
func throttleSQLViolations(sources map[string]string, allowed map[string]map[string]bool) []string {
	parsed := guardkit.Sources(sources).Parse(0)
	fset := parsed.Fset
	bad := append([]string(nil), parsed.Problems...)
	for _, pf := range parsed.Files {
		name, f := pf.Path, pf.AST
		// package models declares the table's struct and lists it in All(); that is
		// the model, not a use of it, so the identifier rule skips that directory.
		inModels := pf.Dir() == "models"
		inventoryTables := erasureInventoryTableLiterals(name, f)
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && allowed[name][fn.Name.Name] {
				continue
			}
			jobKinds := map[*ast.BasicLit]bool{}
			ast.Inspect(d, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok && guardkit.Callee(call) == "daily" && len(call.Args) > 0 {
					if lit, ok := guardkit.Unparen(call.Args[0]).(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if v, err := strconv.Unquote(lit.Value); err == nil && v == throttleJobKind {
							jobKinds[lit] = true
						}
					}
				}
				return true
			})
			ast.Inspect(d, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.BasicLit:
					if x.Kind != token.STRING || jobKinds[x] || inventoryTables[x] {
						return true
					}
					v, err := strconv.Unquote(x.Value)
					if err != nil {
						v = x.Value
					}
					if loginThrottlesTable.MatchString(v) {
						bad = append(bad, fset.Position(x.Pos()).String()+": login_throttles SQL outside the listed throttle helpers")
					}
				case *ast.Ident:
					if x.Name == "LoginThrottle" && !inModels {
						bad = append(bad, fset.Position(x.Pos()).String()+": models.LoginThrottle outside the listed throttle helpers")
					}
				}
				return true
			})
		}
	}
	return bad
}

// throttleCoverageViolations checks the owned/shared lines against
// ownedThrottleKeys in the source of throttle_keys.go.
func throttleCoverageViolations(src string) []string {
	parsed := guardkit.Sources{throttleKeysFile: src}.Parse(parser.ParseComments)
	if len(parsed.Problems) > 0 {
		return parsed.Problems
	}
	f := parsed.Files[0].AST
	builders := throttleBuilders(f)
	var owner *ast.FuncDecl
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == "ownedThrottleKeys" {
			owner = fn
		}
	}
	if owner == nil {
		return []string{throttleKeysFile + ": ownedThrottleKeys is missing"}
	}
	// Address-owned builders may run only inside the available-address arm.
	// Id-owned builders must remain available even when the address is lost.
	addressArm := map[*ast.CallExpr]bool{}
	ast.Inspect(owner.Body, func(n ast.Node) bool {
		branch, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		condition, ok := branch.Cond.(*ast.SelectorExpr)
		if !ok || condition.Sel.Name != "ok" {
			return true
		}
		base, ok := condition.X.(*ast.Ident)
		if !ok || base.Name != "held" {
			return true
		}
		ast.Inspect(branch.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				addressArm[call] = true
			}
			return true
		})
		return true
	})
	called := map[string]bool{}
	outsideAddressArm := map[string]bool{}
	insideAddressArm := map[string]bool{}
	ast.Inspect(owner.Body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if id, ok := guardkit.Unparen(call.Fun).(*ast.Ident); ok && builders[id.Name] != nil {
				called[id.Name] = true
				if addressArm[call] {
					insideAddressArm[id.Name] = true
				} else {
					outsideAddressArm[id.Name] = true
				}
			}
		}
		return true
	})
	var names []string
	for name := range builders {
		names = append(names, name)
	}
	sort.Strings(names)
	var bad []string
	for _, name := range names {
		var owned, shared bool
		if doc := builders[name].Doc; doc != nil {
			for _, line := range strings.Split(doc.Text(), "\n") {
				line = strings.TrimSpace(line)
				owned = owned || strings.HasPrefix(line, "owned:")
				shared = shared || strings.HasPrefix(line, "shared:")
			}
		}
		if owned && builders[name].Type.Params != nil && len(builders[name].Type.Params.List) > 0 {
			parameter, _ := builders[name].Type.Params.List[0].Type.(*ast.Ident)
			if parameter != nil && parameter.Name == "string" && outsideAddressArm[name] {
				bad = append(bad, name+" is address-owned but called outside if held.ok")
			}
			if parameter != nil && parameter.Name == "uint" && insideAddressArm[name] {
				bad = append(bad, name+" is id-owned but called inside if held.ok")
			}
		}
		switch {
		case owned == shared:
			bad = append(bad, name+" needs exactly one doc line starting `owned:` or `shared:`")
		case owned && !called[name]:
			bad = append(bad, name+" is owned but ownedThrottleKeys does not call it")
		case shared && called[name]:
			bad = append(bad, name+" is shared but ownedThrottleKeys calls it")
		}
	}
	return bad
}

func TestThrottleKeysComeFromBuilders(t *testing.T) {
	for _, v := range throttleKeyViolations(throttleProductionSources(t)) {
		t.Error(v)
	}
}

func TestThrottleSQLStaysInItsHelpers(t *testing.T) {
	sources := throttleProductionSources(t)
	for _, v := range throttleSQLViolations(sources, throttleSQLAllowed) {
		t.Error(v)
	}
	// The allow list must name functions that exist, so a rename cannot leave
	// a stale permission behind.
	parsed := sources.Parse(0)
	for file, fns := range throttleSQLAllowed {
		pf := parsed.File(file)
		if pf == nil {
			t.Errorf("throttleSQLAllowed names %s, which does not exist", file)
			continue
		}
		f := pf.AST
		have := map[string]bool{}
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok {
				have[fn.Name.Name] = true
			}
		}
		for fn := range fns {
			if !have[fn] {
				t.Errorf("throttleSQLAllowed names %s in %s, which does not exist", fn, file)
			}
		}
	}
}

func TestOwnedThrottleKeysCoverEveryOwnedBuilder(t *testing.T) {
	for _, v := range throttleCoverageViolations(throttleProductionSources(t)[throttleKeysFile]) {
		t.Error(v)
	}
}

// throttleStubs declares the helper signatures a fixture calls, the way
// api_auth.go does for the real tree.
const throttleStubs = `package main

func (a *App) throttleAllow(ctx context.Context, key throttleKey, limit int, window time.Duration) (bool, error) {
	return true, nil
}
func (a *App) targetAllow(ctx context.Context, key throttleKey) (bool, error) { return true, nil }
func (a *App) reserveLoginAttempt(ctx context.Context, keys []throttleKey) (bool, error) {
	return true, nil
}
`

func TestThrottleGuardsCatchMistakes(t *testing.T) {
	keysSrc := throttleProductionSources(t)[throttleKeysFile]
	withFixture := func(src string) map[string]string {
		return map[string]string{throttleKeysFile: keysSrc, "api_stubs.go": throttleStubs, "api_fixture.go": src}
	}

	// The intended code passes every guard.
	good := `package main

import "log"

func (a *App) good(ctx context.Context, ip, addr string, id uint, k throttleKey, ks []throttleKey) throttleKey {
	keys := []throttleKey{loginEmailKey(addr), loginIPKey(ip)}
	a.reserveLoginAttempt(ctx, keys)
	a.reserveLoginAttempt(ctx, ks)
	a.throttleAllow(ctx, guestSiteKey(), 1, 0)
	a.throttleAllow(ctx, k, 1, 0)
	a.throttleAllow(ctx, k.targeted()[0], 1, 0)
	a.targetAllow(ctx, pwresetAddrKey(addr))
	a.targetAllow(ctx, emailChangeUserKey(id))
	log.Printf("email: %v", addr)
	return loginUserKey(id)
}
`
	if bad := throttleKeyViolations(withFixture(good)); len(bad) != 0 {
		t.Fatalf("the intended code must pass: %v", bad)
	}
	if bad := throttleKeyViolations(map[string]string{throttleKeysFile: keysSrc}); len(bad) != 0 {
		t.Fatalf("throttle_keys.go alone must pass: %v", bad)
	}

	for _, tt := range []struct{ name, src, want string }{
		{"inline conversion", `package main
func (a *App) f(ctx context.Context) { a.throttleAllow(ctx, throttleKey("other"), 1, 0) }`, "uses the throttleKey type"},
		{"typed var", `package main
func (a *App) f() { var k throttleKey = "email:x"; _ = k }`, "uses the throttleKey type"},
		{"const passed by name", `package main
const k = "email:x"
func (a *App) f(ctx context.Context) { a.throttleAllow(ctx, k, 1, 0) }`, "argument 1 of throttleAllow"},
		{"literal in a typed slice", `package main
func (a *App) f(ctx context.Context) { a.reserveLoginAttempt(ctx, []throttleKey{"email:x"}) }`, "element is not a builder call"},
		{"variable in a typed slice", `package main
func (a *App) f(ctx context.Context, k throttleKey) { a.reserveLoginAttempt(ctx, []throttleKey{k}) }`, "element is not a builder call"},
		{"unknown prefix literal", `package main
func (a *App) f(ctx context.Context) { a.throttleAllow(ctx, "newfamily:x", 1, 0) }`, "argument 1 of throttleAllow"},
		{"concatenated literal", `package main
func (a *App) f(ctx context.Context) { a.throttleAllow(ctx, "ema"+"il:x", 1, 0) }`, "argument 1 of throttleAllow"},
		{"known prefix literal anywhere", `package main
var key = "pwreset:ip:" + "x"
func f() string { return "guestreg:site" }`, "starts with the throttle key prefix"},
		{"method value", `package main
func (a *App) f() { g := a.throttleAllow; _ = g }`, "throttleAllow is used as a value"},
		{"builder value", `package main
func f() { g := loginIPKey; _ = g }`, "loginIPKey is used as a value"},
		{"struct field", `package main
type holder struct{ k throttleKey }`, "uses the throttleKey type"},
		{"map of keys", `package main
func f() { m := map[string]throttleKey{}; _ = m }`, "uses the throttleKey type"},
		{"type assertion", `package main
func f(v any) { _ = v.(throttleKey) }`, "uses the throttleKey type"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			bad := throttleKeyViolations(withFixture(tt.src))
			if !strings.Contains(strings.Join(bad, "\n"), tt.want) {
				t.Fatalf("want a violation containing %q, got %v", tt.want, bad)
			}
		})
	}

	t.Run("login_throttles SQL in an unlisted function", func(t *testing.T) {
		src := map[string]string{"api_fixture.go": "package main\nfunc (a *App) sneaky() { a.DB.Exec(\"DELETE FROM login_throttles WHERE key = ?\", \"x\") }\n"}
		bad := throttleSQLViolations(src, throttleSQLAllowed)
		if len(bad) != 1 || !strings.Contains(bad[0], "login_throttles SQL") {
			t.Fatalf("got %v", bad)
		}
	})
	t.Run("schema-qualified login_throttles SQL in an unlisted function", func(t *testing.T) {
		src := map[string]string{"api_fixture.go": "package main\nfunc (a *App) sneaky() { a.DB.Exec(\"DELETE FROM public.login_throttles WHERE key = ?\", \"x\") }\n"}
		bad := throttleSQLViolations(src, throttleSQLAllowed)
		if len(bad) != 1 || !strings.Contains(bad[0], "login_throttles SQL") {
			t.Fatalf("got %v", bad)
		}
	})
	t.Run("models.LoginThrottle in an unlisted function", func(t *testing.T) {
		src := map[string]string{"api_fixture.go": "package main\nfunc (a *App) sneaky() { a.DB.Delete(&models.LoginThrottle{}) }\n"}
		bad := throttleSQLViolations(src, throttleSQLAllowed)
		if len(bad) != 1 || !strings.Contains(bad[0], "models.LoginThrottle") {
			t.Fatalf("got %v", bad)
		}
	})
	for _, tt := range []struct{ name, sql string }{
		{"upper-case", `DELETE FROM LOGIN_THROTTLES`},
		{"quoted and schema-qualified", `DELETE FROM public.\"login_throttles\"`},
		{"the job kind used as a table", `DELETE FROM cleanup.login_throttles`},
		{"the job kind outside daily()", `cleanup.login_throttles`},
	} {
		t.Run("login_throttles SQL, "+tt.name, func(t *testing.T) {
			src := map[string]string{"api_fixture.go": "package main\nfunc (a *App) sneaky() { a.DB.Exec(\"" + tt.sql + "\") }\n"}
			bad := throttleSQLViolations(src, throttleSQLAllowed)
			if len(bad) != 1 || !strings.Contains(bad[0], "login_throttles SQL") {
				t.Fatalf("got %v", bad)
			}
		})
	}
	t.Run("a longer identifier is not the table", func(t *testing.T) {
		src := map[string]string{"api_fixture.go": "package main\nfunc f() { _ = \"x_login_throttles\" }\n"}
		if bad := throttleSQLViolations(src, throttleSQLAllowed); len(bad) != 0 {
			t.Fatalf("got %v", bad)
		}
	})
	t.Run("the table name in a daily job kind is not SQL", func(t *testing.T) {
		src := map[string]string{"jobs_daily.go": "package main\nfunc g() { daily(\"cleanup.login_throttles\", \"Clearing\", nil) }\n"}
		if bad := throttleSQLViolations(src, throttleSQLAllowed); len(bad) != 0 {
			t.Fatalf("got %v", bad)
		}
	})
	t.Run("a listed function may hold the SQL", func(t *testing.T) {
		src := map[string]string{"api_auth.go": "package main\nfunc pruneLoginThrottles() { _ = `DELETE FROM login_throttles` }\n"}
		if bad := throttleSQLViolations(src, throttleSQLAllowed); len(bad) != 0 {
			t.Fatalf("got %v", bad)
		}
	})

	mutate := func(from, to string) string {
		if !strings.Contains(keysSrc, from) {
			t.Fatalf("throttle_keys.go no longer contains %q; update the fixture", from)
		}
		return strings.Replace(keysSrc, from, to, 1)
	}
	for _, tt := range []struct{ name, src, want string }{
		{"builder with no owned or shared line", keysSrc + "\nfunc extraKey() throttleKey { return throttleKey(\"extra:x\") }\n", "extraKey needs exactly one doc line"},
		{"owned builder missing from ownedThrottleKeys", keysSrc + "\n// extraOwnedKey is a test builder.\n// owned: one person's id.\nfunc extraOwnedKey(id uint) throttleKey { return throttleKey(\"extra:\" + strconv.Itoa(int(id))) }\n", "extraOwnedKey is owned but ownedThrottleKeys does not call it"},
		{"address builder without an available address", mutate("keys = append(keys, loginEmailKey(addr))", "keys = append(keys, loginEmailKey(addr))\n\t}\n\tkeys = append(keys, loginEmailKey(held.value))\n\tif held.ok {"), "loginEmailKey is address-owned but called outside if held.ok"},
		{"id builder behind address availability", mutate("keys = append(keys, loginEmailKey(addr))", "keys = append(keys, loginEmailKey(addr), loginUserKey(userID))"), "loginUserKey is id-owned but called inside if held.ok"},
		{"shared builder present in ownedThrottleKeys", mutate("loginUserKey(userID),", "loginUserKey(userID),\n\t\tloginIPKey(held.value),"), "loginIPKey is shared but ownedThrottleKeys calls it"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			bad := throttleCoverageViolations(tt.src)
			if !strings.Contains(strings.Join(bad, "\n"), tt.want) {
				t.Fatalf("want a violation containing %q, got %v", tt.want, bad)
			}
		})
	}
	if bad := throttleCoverageViolations(keysSrc); len(bad) != 0 {
		t.Fatalf("the real file must pass: %v", bad)
	}
}

// Rows already stored keep their meaning across the upgrade: every builder
// makes the string Fold has always stored.
func TestThrottleKeyStringsAreUnchanged(t *testing.T) {
	const addr, ip = "a@x.test", "203.0.113.9"
	for _, tt := range []struct {
		name string
		got  throttleKey
		want string
	}{
		{"login email", loginEmailKey(addr), "email:a@x.test"},
		{"login user", loginUserKey(7), "user:7"},
		{"login ip", loginIPKey(ip), "ip:203.0.113.9"},
		{"login ip v6 is its /64", loginIPKey("2001:db8::1"), "ip:2001:db8::/64"},
		{"login ip mapped v4", loginIPKey("::ffff:203.0.113.9"), "ip:203.0.113.9"},
		{"pwreset addr", pwresetAddrKey(addr), "pwreset:addr:a@x.test"},
		{"pwreset ip", pwresetIPKey(ip), "pwreset:ip:203.0.113.9"},
		{"emailchange user", emailChangeUserKey(7), "emailchange:user:7"},
		{"emailchange addr", emailChangeAddrKey(addr), "emailchange:addr:a@x.test"},
		{"acctreq addr", accountRequestAddrKey(addr), "acctreq:addr:a@x.test"},
		{"acctreq ip", accountRequestIPKey(ip), "acctreq:ip:203.0.113.9"},
		{"acctreq site", accountRequestSiteKey(), "acctreq:site"},
		{"guestreg hint", guestHintKey(addr), "guestreg:hint:a@x.test"},
		{"guestreg ip", guestIPKey(ip), "guestreg:ip:203.0.113.9"},
		{"guestreg site", guestSiteKey(), "guestreg:site"},
		{"guardian addr", guardianClaimAddrKey(addr), "guardian:addr:a@x.test"},
		{"guardian ip", guardianClaimIPKey(ip), "guardian:ip:203.0.113.9"},
		{"guardian-email addr", guardianEmailAddrKey(addr), "guardian-email:addr:a@x.test"},
		{"people-search user", peopleSearchUserKey(7), "people-search:user:7"},
		{"manager-date-add", managerDateAddKey(7), "manager-date-add:7"},
	} {
		if string(tt.got) != tt.want {
			t.Errorf("%s: key = %q, want %q", tt.name, tt.got, tt.want)
		}
	}
	if got, want := pwresetAddrKey(addr).targeted(), [2]throttleKey{"pwreset:addr:a@x.test:h", "pwreset:addr:a@x.test:d"}; got != want {
		t.Errorf("targeted = %v, want %v", got, want)
	}
	if !loginIPKey(ip).isIP() || loginEmailKey(addr).isIP() || loginUserKey(7).isIP() || pwresetIPKey(ip).isIP() {
		t.Error("isIP must be true only for the sign-in ip: keys")
	}
	if got := throttleKeyStrings([]throttleKey{"a", "b"}); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("throttleKeyStrings = %v", got)
	}
}

func TestOwnedThrottleKeysAreExactlyThePersonsKeys(t *testing.T) {
	want := []string{
		"email:a@x.test",
		"user:7",
		"pwreset:addr:a@x.test:h", "pwreset:addr:a@x.test:d",
		"emailchange:user:7:h", "emailchange:user:7:d",
		"emailchange:addr:a@x.test:h", "emailchange:addr:a@x.test:d",
		"acctreq:addr:a@x.test:h", "acctreq:addr:a@x.test:d",
		"guestreg:hint:a@x.test:h", "guestreg:hint:a@x.test:d",
		"guardian:addr:a@x.test:h", "guardian:addr:a@x.test:d",
		"guardian-email:addr:a@x.test:h", "guardian-email:addr:a@x.test:d",
		"people-search:user:7",
		"manager-date-add:7",
	}
	sort.Strings(want)
	if got := ownedThrottleKeys(7, heldAddress{value: "a@x.test", ok: true}); !reflect.DeepEqual(got, want) {
		t.Fatalf("ownedThrottleKeys(7, a@x.test) = %v\nwant %v", got, want)
	}
	if got := ownedThrottleKeys(7, heldAddress{value: "  A@X.test ", ok: true}); !reflect.DeepEqual(got, want) {
		t.Fatalf("the address must be normalized, got %v", got)
	}
	idOnly := []string{"emailchange:user:7:d", "emailchange:user:7:h", "manager-date-add:7", "people-search:user:7", "user:7"}
	for _, held := range []heldAddress{{}, {value: "a@x.test"}, {value: "deleted-7@deleted.invalid"}} {
		if got := ownedThrottleKeys(7, held); !reflect.DeepEqual(got, idOnly) {
			t.Fatalf("unavailable address selected %v, want only %v", got, idOnly)
		}
	}
}
