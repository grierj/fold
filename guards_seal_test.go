package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"fold/internal/guardkit"
)

// ---------------------------------------------------------------------------
// R5-C01 and R6-C01: the actor seal is NOWAIT, loads what sessionCurrent needs and
// is the last locking statement of the transactions that run it.
// ---------------------------------------------------------------------------

const sealSQL = "SELECT id, status, deleted_at, role, account_type, birth_date, password_hash, session_version, logout_version FROM users WHERE id = ? FOR SHARE NOWAIT"

var sealForRe = regexp.MustCompile(`\bFOR\b`)

// sealForbiddenCalls are never called inside a seal body: a seal reads and
// decides, it writes nothing and sends nothing.
var sealForbiddenCalls = map[string]bool{
	"Exec": true, "Create": true, "Save": true, "Update": true, "Updates": true, "Delete": true,
	"Send": true, "Write": true, "WriteAll": true, "Resolve": true, "Clauses": true, "Locking": true,
}

// sealAfterTransition are the only names that may follow the ledger
// transition inside settle's seal: plain reads of the receipt and the subject,
// the seam (a.hooks.settleAfterFence: both names) and the facts being filled in.
var sealAfterTransition = map[string]bool{
	"Raw": true, "Scan": true, "Error": true, "hooks": true, "settleAfterFence": true,
	"receipt": true, "subjectDeleted": true, "state": true,
}

// sealRunLiteral reports whether lit is the value of the run field of a
// contactSeal composite literal, given lit's ancestors (outermost first).
func sealRunLiteral(parents []ast.Node, lit *ast.FuncLit) bool {
	if len(parents) < 2 {
		return false
	}
	kv, ok := parents[len(parents)-1].(*ast.KeyValueExpr)
	if !ok || kv.Value != ast.Expr(lit) {
		return false
	}
	if k, ok := kv.Key.(*ast.Ident); !ok || k.Name != "run" {
		return false
	}
	cl, ok := parents[len(parents)-2].(*ast.CompositeLit)
	if !ok {
		return false
	}
	t, ok := cl.Type.(*ast.Ident)
	return ok && t.Name == "contactSeal"
}

// sealProblems checks the seal's contract over admin_operation_attempts.go,
// contact_lifecycle.go and api_host_handoff.go.
func sealProblems(sources map[string]string) []string {
	var bad []string
	named := guardkit.Sources{}
	for _, name := range []string{"admin_operation_attempts.go", "contact_lifecycle.go", "api_host_handoff.go"} {
		if src, ok := sources[name]; ok {
			named[name] = src
		} else {
			bad = append(bad, name+": missing")
		}
	}
	parsed := named.Parse(0)
	bad = append(bad, parsed.Problems...)
	fset := parsed.Fset
	trees := map[string]*ast.File{}
	for _, pf := range parsed.Files {
		trees[pf.Path] = pf.AST
	}
	if len(bad) > 0 {
		return bad
	}

	// currentAdminIn itself.
	attempts := trees["admin_operation_attempts.go"]
	cur := guardkit.FindFunc(attempts, "currentAdminIn")
	if cur == nil {
		return append(bad, "admin_operation_attempts.go: currentAdminIn is gone")
	}
	var names []string
	for _, p := range cur.Type.Params.List {
		for _, n := range p.Names {
			names = append(names, n.Name)
		}
	}
	if strings.Join(names, ",") != "tx,me,pred" {
		bad = append(bad, "currentAdminIn must take exactly tx, me and pred")
	}
	literals := 0
	var actorDeclared, scanned, decided bool
	ast.Inspect(cur.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.BasicLit:
			if x.Kind == token.STRING {
				if s, err := strconv.Unquote(x.Value); err == nil && strings.HasPrefix(s, "SELECT") {
					literals++
					if guardkit.Squash(s) != sealSQL {
						bad = append(bad, "currentAdminIn's SQL is not the sealed statement: "+guardkit.Squash(s))
					}
				}
			}
		case *ast.DeclStmt:
			if gd, ok := x.Decl.(*ast.GenDecl); ok {
				for _, sp := range gd.Specs {
					if vs, ok := sp.(*ast.ValueSpec); ok && len(vs.Names) == 1 && vs.Names[0].Name == "actor" {
						if sel, ok := vs.Type.(*ast.SelectorExpr); ok && sel.Sel.Name == "User" {
							actorDeclared = true
						}
					}
				}
			}
		case *ast.CallExpr:
			if guardkit.Callee(x) == "Scan" && len(x.Args) == 1 {
				if u, ok := x.Args[0].(*ast.UnaryExpr); ok && u.Op == token.AND {
					if id, ok := u.X.(*ast.Ident); ok && id.Name == "actor" {
						scanned = true
					}
				}
			}
			// The decision is the shared predicate, applied to the scanned row
			// and the session's user: sessionCurrent(&actor, me).
			if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "sessionCurrent" && len(x.Args) == 2 {
				if u, ok := x.Args[0].(*ast.UnaryExpr); ok && u.Op == token.AND {
					a0, ok0 := u.X.(*ast.Ident)
					a1, ok1 := x.Args[1].(*ast.Ident)
					if ok0 && ok1 && a0.Name == "actor" && a1.Name == "me" {
						decided = true
					}
				}
			}
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "CanSignIn" {
				bad = append(bad, "currentAdminIn decides sign-in itself; it must use sessionCurrent(&actor, me)")
			}
		case *ast.AssignStmt:
			for _, l := range x.Lhs {
				if sel, ok := l.(*ast.SelectorExpr); ok && sel.Sel.Name == "PasswordHash" {
					bad = append(bad, "currentAdminIn assigns a PasswordHash")
				}
			}
		case *ast.SelectorExpr:
			if id, ok := x.X.(*ast.Ident); ok && id.Name == "me" && x.Sel.Name != "ID" && x.Sel.Name != "SessionVersion" && x.Sel.Name != "LogoutVersion" {
				bad = append(bad, "currentAdminIn reads me."+x.Sel.Name+"; only the id and the two versions come from the session")
			}
		}
		return true
	})
	if literals != 1 {
		bad = append(bad, fmt.Sprintf("currentAdminIn must hold exactly one SELECT literal, found %d", literals))
	}
	if !actorDeclared || !scanned {
		bad = append(bad, "currentAdminIn must scan into a models.User declared inside it")
	}
	if !decided {
		bad = append(bad, "currentAdminIn must decide the session with sessionCurrent(&actor, me)")
	}

	// Every call of currentAdminIn sits in a contactSeal run literal; seal
	// bodies hold no write, lock or notice.
	guardkit.InspectStack(attempts, func(n ast.Node, stack []ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); !ok || id.Name != "currentAdminIn" {
			return true
		}
		inSeal := false
		for i := len(stack) - 2; i >= 0; i-- {
			if lit, ok := stack[i].(*ast.FuncLit); ok {
				inSeal = sealRunLiteral(stack[:i], lit)
				break
			}
			if _, ok := stack[i].(*ast.FuncDecl); ok {
				break
			}
		}
		if !inSeal {
			bad = append(bad, fmt.Sprintf("admin_operation_attempts.go:%d: currentAdminIn is called outside a contactSeal run function", fset.Position(call.Pos()).Line))
		}
		return true
	})
	guardkit.InspectStack(attempts, func(n ast.Node, stack []ast.Node) bool {
		lit, ok := n.(*ast.FuncLit)
		if !ok || !sealRunLiteral(stack[:len(stack)-1], lit) {
			return true
		}
		transition := -1
		for i, s := range lit.Body.List {
			ast.Inspect(s, func(m ast.Node) bool {
				switch x := m.(type) {
				case *ast.CallExpr:
					if sealForbiddenCalls[guardkit.Callee(x)] {
						bad = append(bad, "a seal body calls "+guardkit.Callee(x))
					}
					if guardkit.Callee(x) == "settleTransition" && transition < 0 {
						transition = i
					}
				case *ast.BasicLit:
					if x.Kind == token.STRING && sealForRe.MatchString(x.Value) {
						bad = append(bad, "a seal body holds SQL with FOR: "+x.Value)
					}
				case *ast.SelectorExpr:
					if x.Sel.Name == "Locking" {
						bad = append(bad, "a seal body names clause.Locking")
					}
				}
				return true
			})
		}
		if transition >= 0 {
			for _, s := range lit.Body.List[transition+1:] {
				ast.Inspect(s, func(m ast.Node) bool {
					switch x := m.(type) {
					case *ast.SelectorExpr:
						if !sealAfterTransition[x.Sel.Name] {
							bad = append(bad, "after the ledger transition the seal may only read plainly; it uses ."+x.Sel.Name)
						}
					case *ast.BasicLit:
						if x.Kind == token.STRING && sealForRe.MatchString(x.Value) {
							bad = append(bad, "a locking read follows the ledger transition: "+x.Value)
						}
					}
					return true
				})
			}
		}
		return true
	})

	// deletionSeal takes the reason plan's predicate as a value: the seal
	// decides on what the attempt handed it, never on a variable the
	// callback can still reassign.
	if ds := guardkit.FindFunc(attempts, "deletionSeal"); ds == nil {
		bad = append(bad, "admin_operation_attempts.go: deletionSeal is gone")
	} else {
		for _, p := range ds.Type.Params.List {
			if _, ok := p.Type.(*ast.StarExpr); ok {
				if _, fn := p.Type.(*ast.StarExpr).X.(*ast.FuncType); fn {
					bad = append(bad, "deletionSeal takes a pointer to a predicate; pass the predicate value")
				}
			}
		}
	}

	// contactMutationOn: reconcileContacts, the seal, then the reserve check,
	// with nothing between.
	lc := guardkit.FindFunc(trees["contact_lifecycle.go"], "contactMutationOn")
	if lc == nil {
		bad = append(bad, "contact_lifecycle.go: contactMutationOn is gone")
	} else {
		at := -1
		for i, s := range lc.Body.List {
			is, ok := s.(*ast.IfStmt)
			if !ok || is.Init == nil {
				continue
			}
			found := false
			ast.Inspect(is.Init, func(m ast.Node) bool {
				if c, ok := m.(*ast.CallExpr); ok && guardkit.Callee(c) == "reconcileContacts" {
					found = true
				}
				return true
			})
			if found {
				at = i
			}
		}
		okShape := at >= 0 && at+2 < len(lc.Body.List)
		if okShape {
			seal, sok := lc.Body.List[at+1].(*ast.IfStmt)
			reserve, rok := lc.Body.List[at+2].(*ast.IfStmt)
			okShape = sok && rok
			if okShape {
				hasSeal, hasReserve := false, false
				ast.Inspect(seal, func(m ast.Node) bool {
					if c, ok := m.(*ast.CallExpr); ok && guardkit.Callee(c) == "run" {
						hasSeal = true
					}
					return true
				})
				ast.Inspect(reserve, func(m ast.Node) bool {
					if id, ok := m.(*ast.Ident); ok && id.Name == "errCommitReserve" {
						hasReserve = true
					}
					return true
				})
				okShape = hasSeal && hasReserve
			}
		}
		if !okShape {
			bad = append(bad, "contactMutationOn must run reconcileContacts, then change.Seal.run, then the commit-reserve check, with no statement between")
		}
	}

	// deletionHostPlan's actor read takes no lock: it is exactly one
	// lockAdminIn call in the plain mode actorRead.
	dp := guardkit.FindFunc(trees["api_host_handoff.go"], "deletionHostPlan")
	if dp == nil {
		bad = append(bad, "api_host_handoff.go: deletionHostPlan is gone")
	} else {
		reads := 0
		ast.Inspect(dp.Body, func(m ast.Node) bool {
			c, ok := m.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch guardkit.Callee(c) {
			case "lockAdminIn", "lockActorIn":
				reads++
				if len(c.Args) != 3 {
					bad = append(bad, "deletionHostPlan's actor read must be lockAdminIn(tx, me, actorRead)")
				} else if id, ok := c.Args[2].(*ast.Ident); !ok || id.Name != "actorRead" {
					bad = append(bad, "deletionHostPlan's actor read takes a lock")
				}
			}
			return true
		})
		if reads != 1 {
			bad = append(bad, fmt.Sprintf("deletionHostPlan must read the actor once with lockAdminIn(tx, me, actorRead), found %d", reads))
		}
	}
	sort.Strings(bad)
	return bad
}

func sealRealSources(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, name := range []string{"admin_operation_attempts.go", "contact_lifecycle.go", "api_host_handoff.go"} {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		out[name] = string(b)
	}
	return out
}

func TestActorSealIsLast(t *testing.T) {
	real := sealRealSources(t)
	for _, v := range sealProblems(real) {
		t.Error(v)
	}
	with := func(file, old, replacement string) map[string]string {
		if strings.Count(real[file], old) != 1 {
			t.Fatalf("fixture anchor %q must occur once in %s, found %d", old, file, strings.Count(real[file], old))
		}
		out := map[string]string{}
		for k, v := range real {
			out[k] = v
		}
		out[file] = strings.Replace(real[file], old, replacement, 1)
		return out
	}
	const aops = "admin_operation_attempts.go"
	cases := []struct {
		name    string
		sources map[string]string
	}{
		{"FOR SHARE without NOWAIT", with(aops, "users WHERE id = ? FOR SHARE NOWAIT\"", "users WHERE id = ? FOR SHARE\"")},
		{"a projection without password_hash", with(aops, "birth_date, password_hash, session_version", "birth_date, session_version")},
		{"a password_hash IS NOT NULL projection", with(aops, "birth_date, password_hash, session_version", "birth_date, (password_hash IS NOT NULL) AS password_hash, session_version")},
		{"a deleted_at IS NULL filter", with(aops, "FROM users WHERE id = ? FOR SHARE NOWAIT\"", "FROM users WHERE id = ? AND deleted_at IS NULL FOR SHARE NOWAIT\"")},
		{"a PasswordHash copied from another value", with(aops, "\tvar actor models.User\n\terr := tx.Raw(", "\tvar actor models.User\n\tactor.PasswordHash = me.PasswordHash\n\terr := tx.Raw(")},
		{"a session decision other than sessionCurrent", with(aops, "!sessionCurrent(&actor, me)", "actor.Status != models.UserActive")},
		{"a hand-written sign-in check", with(aops, "!sessionCurrent(&actor, me)", "!actor.CanSignIn()")},
		{"a currentAdminIn call outside a seal", with(aops, "// --- mint and settle routes", "func (a *App) notASeal(tx *gorm.DB, me *models.User) error { return currentAdminIn(tx, me, nil) }\n\n// --- mint and settle routes")},
		{"a statement after the seal in contactMutationOn", with("contact_lifecycle.go", "\tif _, ok := commitReserveLeft(deadline); ctx.Err() != nil || (bounded && !ok) {", "\t_ = tx\n\tif _, ok := commitReserveLeft(deadline); ctx.Err() != nil || (bounded && !ok) {")},
		{"an Update in a seal", with(aops, "\t\treturn currentAdminIn(tx, me, pred)\n\t}}", "\t\ttx.Exec(\"UPDATE users SET name = name\")\n\t\treturn currentAdminIn(tx, me, pred)\n\t}}")},
		{"a seal predicate shared through a pointer", with(aops, "pred func(*models.User) bool) *contactSeal", "pred *func(*models.User) bool) *contactSeal")},
		{"a locking read after the ledger transition", with(aops, "\t\t\tfacts.state = st\n", "\t\t\tfacts.state = st\n\t\t\ttx.Raw(\"SELECT id FROM users WHERE id = 1 FOR UPDATE\")\n")},
		{"a locked mode on deletionHostPlan's actor read", with("api_host_handoff.go", "lockAdminIn(tx, me, actorRead)", "lockAdminIn(tx, me, actorShare)")},
	}
	for _, tc := range cases {
		if bad := sealProblems(tc.sources); len(bad) == 0 {
			t.Errorf("%s: the guard accepted it", tc.name)
		}
	}
}
