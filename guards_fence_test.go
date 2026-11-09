package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"sort"
	"strings"
	"testing"

	"fold/internal/guardkit"
)

// ---------------------------------------------------------------------------
// T07B-1b guards. Parse-only: they read syntax, not types, and bind names with
// go/parser's object resolution inside one file.
//
// One wrapper, a.fencedContactMutation (restore_fence.go), is the only place
// the restore ticket is taken, acknowledged or abandoned, and the only place a
// contactChange gets its actor seal. The guard checks the wrapper's shape (the
// ticket first in the callback on the callback's own transaction, abandon
// deferred before the call, acknowledgment with the call's own result as the
// very next statement) and that both fenced routes go through it.
// ---------------------------------------------------------------------------

// fenceWrapper is where the ticket protocol lives.
const (
	fenceWrapperFile = "restore_fence.go"
	fenceWrapper     = "fencedContactMutation"
)

// fenceTicketFuncs are the ticket's three steps; only the wrapper names them.
var fenceTicketFuncs = map[string]bool{"takeRestoreTicket": true, "acknowledgeRestoreTicket": true, "abandonRestoreTicket": true}

// fenceNamed lists the fenced routes: each runs its transaction through the
// wrapper and never calls a contact mutation directly.
var fenceNamed = []struct{ file, fn string }{
	{"api_admin.go", "apiAdminDeleteUser"},
	{"admin_operation_attempts.go", "apiAdminSettleOperationAttempt"},
}

func fenceIsSel(e ast.Expr, name string) (*ast.SelectorExpr, bool) {
	sel, ok := e.(*ast.SelectorExpr)
	return sel, ok && sel.Sel.Name == name
}

func fenceSameVar(a, b *ast.Ident) bool {
	return a != nil && b != nil && a.Obj != nil && a.Obj == b.Obj
}

// fenceRefOf returns the identifier in &x.
func fenceRefOf(e ast.Expr) *ast.Ident {
	u, ok := e.(*ast.UnaryExpr)
	if !ok || u.Op != token.AND {
		return nil
	}
	id, _ := u.X.(*ast.Ident)
	return id
}

func fenceCallOf(s ast.Stmt) (*ast.CallExpr, bool) {
	es, ok := s.(*ast.ExprStmt)
	if !ok {
		return nil, false
	}
	c, ok := es.X.(*ast.CallExpr)
	return c, ok
}

// fenceMethod finds the method name of App in tree.
func fenceMethod(tree *ast.File, name string) *ast.FuncDecl {
	for _, d := range tree.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv != nil && fd.Name.Name == name {
			return fd
		}
	}
	return nil
}

// fenceProblems checks the production tree (or a fixture of it).
func fenceProblems(sources map[string]string) []string {
	parsed := guardkit.Sources(sources).Parse(0)
	bad := append([]string(nil), parsed.Problems...)
	var wrapper *ast.FuncDecl
	if f := parsed.File(fenceWrapperFile); f != nil {
		wrapper = fenceMethod(f.AST, fenceWrapper)
	}
	if wrapper == nil {
		bad = append(bad, fenceWrapperFile+": "+fenceWrapper+" is gone; the ticket protocol has no home")
	} else {
		bad = append(bad, fenceWrapperProblems(wrapper)...)
	}
	// Every use of a ticket step, and every seal given to a contactChange, sits
	// in the wrapper. Declarations of the steps themselves are not uses.
	for _, pf := range parsed.Files {
		guardkit.InspectStack(pf.AST, func(n ast.Node, stack []ast.Node) bool {
			inWrapper := false
			for _, s := range stack {
				if s == ast.Node(wrapper) && wrapper != nil && pf.Path == fenceWrapperFile {
					inWrapper = true
				}
			}
			pos := parsed.Fset.Position(n.Pos())
			switch x := n.(type) {
			case *ast.SelectorExpr:
				if fenceTicketFuncs[x.Sel.Name] && !inWrapper {
					bad = append(bad, fmt.Sprintf("%s:%d: %s used outside %s; every fenced transaction goes through the wrapper", pf.Path, pos.Line, x.Sel.Name, fenceWrapper))
				}
				if x.Sel.Name == "Seal" && !inWrapper && fenceAssigned(stack) {
					bad = append(bad, fmt.Sprintf("%s:%d: a contactChange seal is set outside %s", pf.Path, pos.Line, fenceWrapper))
				}
			case *ast.Ident:
				// A bare reference (a function value of the same name declared
				// elsewhere) is a use too; the method declarations are not.
				if fenceTicketFuncs[x.Name] && !inWrapper && !fenceDeclName(stack, x) && !fenceSelName(stack, x) {
					bad = append(bad, fmt.Sprintf("%s:%d: %s used outside %s", pf.Path, pos.Line, x.Name, fenceWrapper))
				}
			case *ast.KeyValueExpr:
				if k, ok := x.Key.(*ast.Ident); ok && k.Name == "Seal" && !inWrapper && fenceInContactChange(stack) {
					bad = append(bad, fmt.Sprintf("%s:%d: a contactChange literal sets Seal outside %s", pf.Path, pos.Line, fenceWrapper))
				}
			}
			return true
		})
	}
	for _, n := range fenceNamed {
		file := parsed.File(n.file)
		if file == nil {
			if _, ok := sources[n.file]; !ok {
				bad = append(bad, n.file+": the file is gone; update fenceNamed")
			}
			continue // a file that does not parse is already in Problems
		}
		decl := fenceMethod(file.AST, n.fn)
		if decl == nil {
			bad = append(bad, n.file+": "+n.fn+" no longer exists; update fenceNamed")
			continue
		}
		wrapped := 0
		ast.Inspect(decl.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if _, ok := fenceIsSel(call.Fun, fenceWrapper); ok {
				wrapped++
			}
			for _, direct := range []string{"contactMutationCtx", "contactMutation", "contactMutationOn"} {
				if _, ok := fenceIsSel(call.Fun, direct); ok {
					bad = append(bad, fmt.Sprintf("%s: %s calls a.%s directly; a fenced route runs its transaction through a.%s", n.file, n.fn, direct, fenceWrapper))
				}
			}
			return true
		})
		if wrapped == 0 {
			bad = append(bad, fmt.Sprintf("%s: %s does not call a.%s; update fenceNamed", n.file, n.fn, fenceWrapper))
		}
	}
	sort.Strings(bad)
	return bad
}

// fenceAssigned reports whether the innermost node of stack is the left-hand
// side of an assignment (x.Seal = ...).
func fenceAssigned(stack []ast.Node) bool {
	if len(stack) < 2 {
		return false
	}
	as, ok := stack[len(stack)-2].(*ast.AssignStmt)
	if !ok {
		return false
	}
	for _, l := range as.Lhs {
		if l == stack[len(stack)-1] {
			return true
		}
	}
	return false
}

func fenceDeclName(stack []ast.Node, id *ast.Ident) bool {
	if len(stack) < 2 {
		return false
	}
	fd, ok := stack[len(stack)-2].(*ast.FuncDecl)
	return ok && fd.Name == id
}

func fenceSelName(stack []ast.Node, id *ast.Ident) bool {
	if len(stack) < 2 {
		return false
	}
	sel, ok := stack[len(stack)-2].(*ast.SelectorExpr)
	return ok && sel.Sel == id
}

func fenceInContactChange(stack []ast.Node) bool {
	if len(stack) < 2 {
		return false
	}
	cl, ok := stack[len(stack)-2].(*ast.CompositeLit)
	if !ok {
		return false
	}
	t, ok := guardkit.Unparen(cl.Type).(*ast.Ident)
	return ok && t.Name == "contactChange"
}

// fenceWrapperProblems checks the wrapper's one contactMutationCtx call.
func fenceWrapperProblems(decl *ast.FuncDecl) []string {
	var bad []string
	if decl.Type.Params == nil || len(decl.Type.Params.List) < 1 {
		return []string{fenceWrapper + " has no parameters"}
	}
	last := decl.Type.Params.List[len(decl.Type.Params.List)-1]
	if len(last.Names) != 1 {
		return []string{fenceWrapper + "'s last parameter must be the one named callback"}
	}
	fnParam := last.Names[0]
	calls := 0
	guardkit.InspectStack(decl.Body, func(node ast.Node, stack []ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		for _, other := range []string{"contactMutation", "contactMutationOn"} {
			if _, ok := fenceIsSel(call.Fun, other); ok {
				bad = append(bad, fenceWrapper+" must run a.contactMutationCtx, not a."+other)
			}
		}
		if _, ok := fenceIsSel(call.Fun, "contactMutationCtx"); !ok {
			return true
		}
		calls++
		if why := fenceCallProblem(append([]ast.Node(nil), stack...), fnParam); why != "" {
			bad = append(bad, fenceWrapperFile+": "+fenceWrapper+": "+why)
		}
		return true
	})
	if calls != 1 {
		bad = append(bad, fmt.Sprintf("%s must make exactly one a.contactMutationCtx call, found %d", fenceWrapper, calls))
	}
	return bad
}

// fenceCallProblem checks the wrapper's call; stack ends with the call. fn is
// the wrapper's callback parameter, which the callback runs after the ticket.
func fenceCallProblem(stack []ast.Node, fn *ast.Ident) string {
	call := stack[len(stack)-1].(*ast.CallExpr)
	for _, n := range stack[:len(stack)-1] {
		switch n.(type) {
		case *ast.FuncLit, *ast.GoStmt, *ast.DeferStmt:
			return "the call sits inside a func literal, go or defer"
		}
	}
	if len(stack) < 3 {
		return "the call is not a statement of a block"
	}
	var result *ast.Ident
	stmt := stack[len(stack)-2]
	switch s := stmt.(type) {
	case *ast.AssignStmt:
		if len(s.Lhs) != 1 || len(s.Rhs) != 1 || s.Rhs[0] != ast.Expr(call) {
			return "the call's result must be assigned to one variable"
		}
		result, _ = s.Lhs[0].(*ast.Ident)
	case *ast.ExprStmt:
		return "the call's result is not captured, so it cannot be acknowledged"
	default:
		return "the call must be a whole statement of a block"
	}
	block, ok := stack[len(stack)-3].(*ast.BlockStmt)
	if !ok {
		return "the call's statement must sit directly in a block"
	}
	at := -1
	for i, s := range block.List {
		if ast.Node(s) == stmt {
			at = i
		}
	}
	lit, ok := call.Args[len(call.Args)-1].(*ast.FuncLit)
	if !ok {
		return "the callback must be a func literal (a non-literal callback hides the ticket)"
	}
	if lit.Type.Params == nil || len(lit.Type.Params.List) != 1 || len(lit.Type.Params.List[0].Names) != 1 {
		return "the callback must take one named *gorm.DB parameter"
	}
	param := lit.Type.Params.List[0].Names[0]
	if len(lit.Body.List) == 0 {
		return "the callback is empty"
	}
	// The ticket is taken by the first statement, or by the first after a
	// plain var declaration of the callback's own error variable.
	first := 0
	if ds, ok := lit.Body.List[0].(*ast.DeclStmt); ok {
		if gd, ok := ds.Decl.(*ast.GenDecl); ok && gd.Tok == token.VAR {
			first = 1
			for _, sp := range gd.Specs {
				if vs, ok := sp.(*ast.ValueSpec); !ok || len(vs.Values) != 0 {
					first = 0
				}
			}
		}
	}
	if first >= len(lit.Body.List) {
		return "the callback takes no ticket"
	}
	var take *ast.AssignStmt
	switch s := lit.Body.List[first].(type) {
	case *ast.AssignStmt:
		take = s
	case *ast.IfStmt:
		take, _ = s.Init.(*ast.AssignStmt)
	}
	if take == nil || take.Tok != token.ASSIGN || len(take.Lhs) != 2 || len(take.Rhs) != 1 {
		return "the callback's first statement must assign the ticket with = from a.takeRestoreTicket"
	}
	tc, ok := take.Rhs[0].(*ast.CallExpr)
	if !ok {
		return "the callback's first statement must assign from a.takeRestoreTicket"
	}
	if _, ok := fenceIsSel(tc.Fun, "takeRestoreTicket"); !ok || len(tc.Args) != 1 {
		return "the callback's first statement must assign from a.takeRestoreTicket(tx)"
	}
	if arg, ok := tc.Args[0].(*ast.Ident); !ok || !fenceSameVar(arg, param) {
		return "the ticket must be taken on the callback's own *gorm.DB parameter, not a.DB or a local that reuses its name"
	}
	tk, ok := take.Lhs[0].(*ast.Ident)
	if !ok || tk.Obj == nil || tk.Obj.Pos() >= lit.Pos() {
		return "the ticket variable must be declared before the call, outside the callback"
	}
	// The caller's work runs after the ticket, on the same transaction.
	ran := false
	for _, s := range lit.Body.List[first+1:] {
		ast.Inspect(s, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				if id, ok := c.Fun.(*ast.Ident); ok && fenceSameVar(id, fn) && len(c.Args) == 1 {
					if arg, ok := c.Args[0].(*ast.Ident); ok && fenceSameVar(arg, param) {
						ran = true
					}
				}
			}
			return true
		})
	}
	if !ran {
		return "the callback must run the wrapper's callback on its own transaction after the ticket"
	}
	// The abandon defer comes before the call, in the same block.
	abandoned := false
	for _, s := range block.List[:at] {
		d, ok := s.(*ast.DeferStmt)
		if !ok {
			continue
		}
		if _, ok := fenceIsSel(d.Call.Fun, "abandonRestoreTicket"); ok && len(d.Call.Args) == 1 && fenceSameVar(fenceRefOf(d.Call.Args[0]), tk) {
			abandoned = true
		}
	}
	if !abandoned {
		return "defer a.abandonRestoreTicket(&ticket) must come before the call"
	}
	// The acknowledgment is the very next statement, with the call's result.
	if at+1 >= len(block.List) {
		return "a.acknowledgeRestoreTicket must be the statement right after the call"
	}
	ac, ok := fenceCallOf(block.List[at+1])
	if !ok {
		return "a.acknowledgeRestoreTicket must be a plain statement right after the call (not deferred, not a goroutine)"
	}
	if _, ok := fenceIsSel(ac.Fun, "acknowledgeRestoreTicket"); !ok || len(ac.Args) != 2 {
		return "the statement right after the call must be a.acknowledgeRestoreTicket(&ticket, err)"
	}
	if !fenceSameVar(fenceRefOf(ac.Args[0]), tk) {
		return "a.acknowledgeRestoreTicket must acknowledge the ticket the callback took"
	}
	if r, ok := ac.Args[1].(*ast.Ident); !ok || !fenceSameVar(r, result) {
		return "a.acknowledgeRestoreTicket must receive the call's own result"
	}
	// Neither acknowledgment nor abandon belongs inside the callback.
	inside := false
	ast.Inspect(lit.Body, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			if _, ok := fenceIsSel(c.Fun, "acknowledgeRestoreTicket"); ok {
				inside = true
			}
			if _, ok := fenceIsSel(c.Fun, "abandonRestoreTicket"); ok {
				inside = true
			}
		}
		return true
	})
	if inside {
		return "the callback must not acknowledge or abandon its ticket"
	}
	return ""
}

// fenceWrapperBase is the clean wrapper fixture.
const fenceWrapperBase = `package main

func (a *App) fencedContactMutation(ctx context.Context, change contactChange, fn func(tx *gorm.DB) (*contactSeal, error)) error {
	var tk restoreTicket
	defer a.abandonRestoreTicket(&tk)
	var seal *contactSeal
	change.Seal = &contactSeal{run: func(tx *gorm.DB) error { return seal.run(tx) }}
	err := a.contactMutationCtx(ctx, change, func(tx *gorm.DB) error {
		var err error
		if tk, err = a.takeRestoreTicket(tx); err != nil {
			return err
		}
		seal, err = fn(tx)
		return err
	})
	a.acknowledgeRestoreTicket(&tk, err)
	return err
}

func (a *App) takeRestoreTicket(tx *gorm.DB) (restoreTicket, error) { return restoreTicket{}, nil }

func (a *App) acknowledgeRestoreTicket(tk *restoreTicket, err error) {}

func (a *App) abandonRestoreTicket(tk *restoreTicket) {}
`

// fenceRoutesBase holds both fenced routes.
const fenceRoutesBase = `package main

func (a *App) apiAdminDeleteUser(c *gin.Context) {
	var err error
	for {
		err = a.fencedContactMutation(ctx, contactChange{UserID: 1}, func(tx *gorm.DB) (*contactSeal, error) {
			return seal, nil
		})
	}
	_ = err
}

func (a *App) apiAdminSettleOperationAttempt(c *gin.Context) {
	err := a.fencedContactMutation(ctx, contactChange{UserID: 1}, func(*gorm.DB) (*contactSeal, error) {
		return seal, nil
	})
	_ = err
}
`

func fenceSources(wrapper, routes string) map[string]string {
	// Both named routes live in the one fixture; each file entry parses it,
	// so only one of the two entries keeps a route that the other also holds.
	return map[string]string{
		fenceWrapperFile:              wrapper,
		"api_admin.go":                strings.Replace(routes, "func (a *App) apiAdminSettleOperationAttempt", "func (a *App) settleCopy", 1),
		"admin_operation_attempts.go": strings.Replace(routes, "func (a *App) apiAdminDeleteUser", "func (a *App) deleteCopy", 1),
	}
}

func TestFencedTransactionsTakeTheTicketFirst(t *testing.T) {
	for _, v := range fenceProblems(guardkit.Production(t)) {
		t.Error(v)
	}
	if bad := fenceProblems(fenceSources(fenceWrapperBase, fenceRoutesBase)); len(bad) != 0 {
		t.Fatalf("the positive fixture failed: %v", bad)
	}
}

func TestFencedTransactionsGuardCatchesBypasses(t *testing.T) {
	wrap := func(old, replacement string) map[string]string {
		if !strings.Contains(fenceWrapperBase, old) {
			t.Fatalf("wrapper fixture anchor %q is missing", old)
		}
		return fenceSources(strings.Replace(fenceWrapperBase, old, replacement, 1), fenceRoutesBase)
	}
	route := func(old, replacement string) map[string]string {
		if !strings.Contains(fenceRoutesBase, old) {
			t.Fatalf("route fixture anchor %q is missing", old)
		}
		return fenceSources(fenceWrapperBase, strings.ReplaceAll(fenceRoutesBase, old, replacement))
	}
	take := "if tk, err = a.takeRestoreTicket(tx); err != nil {\n\t\t\treturn err\n\t\t}\n"
	ack := "a.acknowledgeRestoreTicket(&tk, err)\n"
	cases := []struct {
		name    string
		sources map[string]string
	}{
		{"ticket not first", wrap(take, "x := 1\n\t\t_ = x\n\t\t"+take)},
		{"work before the ticket", wrap("\t\tvar err error\n\t\t"+take, "\t\tvar err error\n\t\tseal, err = fn(tx)\n\t\t"+take)},
		{"taken with a.DB", wrap("a.takeRestoreTicket(tx)", "a.takeRestoreTicket(a.DB)")},
		{"taken on a shadowing local", wrap("func(tx *gorm.DB) error {\n\t\tvar err error", "func(db *gorm.DB) error {\n\t\tvar err error")},
		{"no ticket", wrap(take, "err = nil\n\t\t")},
		{"ticket declared inside the callback", wrap("if tk, err = ", "if tk, err := ")},
		{"callback never run", wrap("seal, err = fn(tx)", "seal, err = nil, nil")},
		{"callback run on a.DB", wrap("seal, err = fn(tx)", "seal, err = fn(a.DB)")},
		{"acknowledge missing", wrap(ack, "")},
		{"acknowledge not next", wrap(ack, "x := 1\n\t_ = x\n\t"+ack)},
		{"acknowledge deferred", wrap(ack, "defer "+ack)},
		{"acknowledge in a goroutine", wrap(ack, "go "+ack)},
		{"acknowledge on another variable", wrap(ack, "var other restoreTicket\n\ta.acknowledgeRestoreTicket(&other, err)\n")},
		{"acknowledge with another result", wrap(ack, "a.acknowledgeRestoreTicket(&tk, nil)\n")},
		{"acknowledge inside the callback", wrap("\t\treturn err\n\t})", "\t\ta.acknowledgeRestoreTicket(&tk, nil)\n\t\treturn err\n\t})")},
		{"abandon missing", wrap("defer a.abandonRestoreTicket(&tk)\n", "")},
		{"result not captured", wrap("err := a.contactMutationCtx", "a.contactMutationCtx")},
		{"two calls in the wrapper", wrap("\treturn err\n}\n\nfunc (a *App) takeRestoreTicket", "\t_ = a.contactMutationCtx(ctx, change, nil)\n\treturn err\n}\n\nfunc (a *App) takeRestoreTicket")},
		{"the wrapper on contactMutation", wrap("err := a.contactMutationCtx(ctx, change, ", "err := a.contactMutation(change, ")},
		{"wrapper gone", fenceSources(strings.Replace(fenceWrapperBase, "fencedContactMutation", "renamed", 1), fenceRoutesBase)},
		{"a ticket taken outside the wrapper", route("return seal, nil", "tk, err := a.takeRestoreTicket(tx)\n\t\t\t_, _ = tk, err\n\t\t\treturn seal, nil")},
		{"an acknowledgment outside the wrapper", route("\t_ = err\n}", "\ta.acknowledgeRestoreTicket(&tk, err)\n\t_ = err\n}")},
		{"a ticket step as a method value", route("\t_ = err\n}", "\tf := a.abandonRestoreTicket\n\t_ = f\n\t_ = err\n}")},
		{"a seal set outside the wrapper", route("contactChange{UserID: 1}", "contactChange{UserID: 1, Seal: seal}")},
		{"a seal assigned outside the wrapper", route("\t_ = err\n}", "\tchange.Seal = seal\n\t_ = err\n}")},
		{"a fenced route calling contactMutationCtx", route("a.fencedContactMutation(ctx, contactChange{UserID: 1}, ", "a.contactMutationCtx(ctx, contactChange{UserID: 1}, ")},
		{"a fenced route that also calls contactMutation", route("\t_ = err\n}", "\t_ = a.contactMutation(contactChange{}, nil)\n\t_ = err\n}")},
		{"listed function gone", fenceSources(fenceWrapperBase, strings.ReplaceAll(fenceRoutesBase, "apiAdminSettleOperationAttempt", "renamed"))},
	}
	for _, tc := range cases {
		if bad := fenceProblems(tc.sources); len(bad) == 0 {
			t.Errorf("%s: the guard accepted it", tc.name)
		}
	}
}
