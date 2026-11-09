package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"

	"fold/internal/guardkit"
)

// ---------------------------------------------------------------------------
// T07B-1b guards. Parse-only: they read syntax, not types, and bind names with
// go/parser's object resolution inside one file.
// ---------------------------------------------------------------------------

// fenceNamed lists the functions that run a fenced a.contactMutationCtx: every
// call in them takes a restore ticket first in its callback, abandons it by a
// defer before the call and acknowledges it in the very next statement.
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

// fenceProblems checks every a.contactMutationCtx call of the named functions.
func fenceProblems(sources map[string]string) []string {
	parsed := guardkit.Sources(sources).Parse(0)
	bad := append([]string(nil), parsed.Problems...)
	for _, n := range fenceNamed {
		file := parsed.File(n.file)
		if file == nil {
			if _, ok := sources[n.file]; !ok {
				bad = append(bad, n.file+": the file is gone; update fenceNamed")
			}
			continue // a file that does not parse is already in Problems
		}
		tree := file.AST
		var decl *ast.FuncDecl
		for _, d := range tree.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv != nil && fd.Name.Name == n.fn {
				decl = fd
			}
		}
		if decl == nil {
			bad = append(bad, n.file+": "+n.fn+" no longer exists; update fenceNamed")
			continue
		}
		calls := 0
		guardkit.InspectStack(decl.Body, func(node ast.Node, stack []ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if _, ok := fenceIsSel(call.Fun, "contactMutationCtx"); !ok {
				return true
			}
			calls++
			if why := fenceCallProblem(append([]ast.Node(nil), stack...)); why != "" {
				bad = append(bad, fmt.Sprintf("%s: %s: %s", n.file, n.fn, why))
			}
			return true
		})
		if calls == 0 {
			bad = append(bad, n.file+": "+n.fn+" has no a.contactMutationCtx call; update fenceNamed")
		}
	}
	sort.Strings(bad)
	return bad
}

// fenceCallProblem checks one call; stack ends with the call.
func fenceCallProblem(stack []ast.Node) string {
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
	var take *ast.AssignStmt
	switch first := lit.Body.List[0].(type) {
	case *ast.AssignStmt:
		take = first
	case *ast.IfStmt:
		take, _ = first.Init.(*ast.AssignStmt)
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

const fenceBase = `package main

func (a *App) apiAdminDeleteUser(c *gin.Context) {
	var err error
	for {
		var tk restoreTicket
		var terr error
		defer a.abandonRestoreTicket(&tk)
		err = a.contactMutationCtx(ctx, change, func(tx *gorm.DB) error {
			if tk, terr = a.takeRestoreTicket(tx); terr != nil {
				return terr
			}
			return nil
		})
		a.acknowledgeRestoreTicket(&tk, err)
	}
}

func (a *App) apiAdminSettleOperationAttempt(c *gin.Context) {
	var err error
	for {
		var tk restoreTicket
		var terr error
		defer a.abandonRestoreTicket(&tk)
		err = a.contactMutationCtx(ctx, change, func(tx *gorm.DB) error {
			if tk, terr = a.takeRestoreTicket(tx); terr != nil {
				return terr
			}
			return nil
		})
		a.acknowledgeRestoreTicket(&tk, err)
	}
}
`

func fenceSources(src string) map[string]string {
	// Both named functions live in the one fixture; each file entry parses it.
	return map[string]string{"api_admin.go": src, "admin_operation_attempts.go": src}
}

func TestFencedTransactionsTakeTheTicketFirst(t *testing.T) {
	sources := map[string]string{}
	for _, n := range fenceNamed {
		b, err := os.ReadFile(n.file)
		if err != nil {
			t.Fatal(err)
		}
		sources[n.file] = string(b)
	}
	for _, v := range fenceProblems(sources) {
		t.Error(v)
	}
	if bad := fenceProblems(fenceSources(fenceBase)); len(bad) != 0 {
		t.Fatalf("the positive fixture failed: %v", bad)
	}
}

func TestFencedTransactionsGuardCatchesBypasses(t *testing.T) {
	// Every case changes the clean fixture in one way (both named functions
	// carry the change, because the replacement applies to every occurrence).
	with := func(old, replacement string) map[string]string {
		if !strings.Contains(fenceBase, old) {
			t.Fatalf("fixture anchor %q is missing", old)
		}
		return fenceSources(strings.ReplaceAll(fenceBase, old, replacement))
	}
	take := "if tk, terr = a.takeRestoreTicket(tx); terr != nil {\n\t\t\t\treturn terr\n\t\t\t}\n"
	ack := "a.acknowledgeRestoreTicket(&tk, err)\n"
	cases := []struct {
		name    string
		sources map[string]string
	}{
		{"ticket not first", with(take, "x := 1\n\t\t\t_ = x\n\t\t\t"+take)},
		{"taken with a.DB", with("a.takeRestoreTicket(tx)", "a.takeRestoreTicket(a.DB)")},
		{"taken on a shadowing local", with("func(tx *gorm.DB) error {", "func(db *gorm.DB) error {")},
		{"no ticket", with(take, "terr = nil\n\t\t\t")},
		{"ticket declared inside the callback", with("if tk, terr = ", "if tk, terr := ")},
		{"acknowledge missing", with(ack, "")},
		{"acknowledge not next", with(ack, "x := 1\n\t\t_ = x\n\t\t"+ack)},
		{"acknowledge deferred", with(ack, "defer "+ack)},
		{"acknowledge in a goroutine", with(ack, "go "+ack)},
		{"acknowledge on another variable", with(ack, "var other restoreTicket\n\t\ta.acknowledgeRestoreTicket(&other, err)\n")},
		{"acknowledge with another result", with(ack, "a.acknowledgeRestoreTicket(&tk, terr)\n")},
		{"acknowledge inside the callback", with("return nil\n\t\t})", "a.acknowledgeRestoreTicket(&tk, nil)\n\t\t\treturn nil\n\t\t})")},
		{"abandon missing", with("defer a.abandonRestoreTicket(&tk)\n", "")},
		{"non-literal callback", with("func(tx *gorm.DB) error {\n\t\t\tif tk, terr = a.takeRestoreTicket(tx); terr != nil {\n\t\t\t\treturn terr\n\t\t\t}\n\t\t\treturn nil\n\t\t})", "callback)")},
		{"result not captured", with("err = a.contactMutationCtx", "a.contactMutationCtx")},
		{"call in a goroutine", with("err = a.contactMutationCtx(ctx, change, func(tx *gorm.DB) error {", "go a.contactMutationCtx(ctx, change, func(tx *gorm.DB) error {")},
		{"listed function gone", fenceSources(strings.ReplaceAll(fenceBase, "apiAdminSettleOperationAttempt", "renamed"))},
		{"no call in a listed function", fenceSources(strings.Replace(fenceBase, "err = a.contactMutationCtx(ctx, change, func(tx *gorm.DB) error {", "err = other(ctx, change, func(tx *gorm.DB) error {", 2))},
	}
	for _, tc := range cases {
		if bad := fenceProblems(tc.sources); len(bad) == 0 {
			t.Errorf("%s: the guard accepted it", tc.name)
		}
	}
	// R-6 stays satisfied by the ticket statement before the soft delete: the
	// admin callback with the ticket first and eraseContactRequestTextIn after
	// the soft delete passes both guards.
	ticket := "if tk, terr = a.takeRestoreTicket(tx); terr != nil {\n\t\t\treturn terr\n\t\t}"
	head := "var tk restoreTicket\n\tvar terr error\n\tvar err error\n\tdefer a.abandonRestoreTicket(&tk)\n\terr = a.contactMutationCtx(ctx, contactChange{UserID: u.ID}, "
	tail := ")\n\ta.acknowledgeRestoreTicket(&tk, err)"
	admin := ctgAdminFileShape(head, tail, ticket, ctgEraseCall)
	clean := ctgClean()
	clean["api_admin.go"] = admin
	if bad := ctgViolations(clean); len(bad) != 0 {
		t.Errorf("R-6 rejected the fenced admin callback: %v", bad)
	}
	if bad := fenceProblems(map[string]string{"api_admin.go": admin, "admin_operation_attempts.go": fenceBase}); len(bad) != 0 {
		// The fixture file holds no settle function, so only that is allowed to complain.
		for _, b := range bad {
			if !strings.Contains(b, "apiAdminSettleOperationAttempt") && !strings.Contains(b, "admin_operation_attempts.go") {
				t.Errorf("the fenced admin callback failed the ticket guard: %s", b)
			}
		}
	}
}
