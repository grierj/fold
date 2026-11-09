package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"

	"fold/internal/guardkit"
)

// ---------------------------------------------------------------------------
// T07B-2b: the reason-review page is an ungated read that ends with the actor
// seal, like the settle and deletion transactions (R5-C01).
// ---------------------------------------------------------------------------

// reasonPageSealProblems checks account_erasure_reasons.go: currentAdminIn is
// called once, as the return of the reasonPageTx callback in reasonPageAttempt,
// right after the readBeforeSeal seam, so nothing locks after it.
func reasonPageSealProblems(src string) []string {
	parsed := guardkit.Sources{"account_erasure_reasons.go": src}.Parse(0)
	if len(parsed.Problems) > 0 {
		return parsed.Problems
	}
	tree := parsed.Files[0].AST
	var bad []string
	calls := 0
	ast.Inspect(tree, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok && guardkit.Callee(c) == "currentAdminIn" {
			calls++
		}
		return true
	})
	if calls != 1 {
		bad = append(bad, fmt.Sprintf("account_erasure_reasons.go: currentAdminIn must be called exactly once, found %d", calls))
	}
	fd := guardkit.FindFunc(tree, "reasonPageAttempt")
	if fd == nil {
		return append(bad, "account_erasure_reasons.go: reasonPageAttempt is gone")
	}
	var lit *ast.FuncLit
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok && guardkit.Callee(c) == "reasonPageTx" && len(c.Args) == 2 {
			lit, _ = c.Args[1].(*ast.FuncLit)
		}
		return true
	})
	if lit == nil || len(lit.Body.List) < 2 {
		return append(bad, "account_erasure_reasons.go: reasonPageAttempt must pass a function literal to a.reasonPageTx")
	}
	last := lit.Body.List[len(lit.Body.List)-1]
	ret, ok := last.(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return append(bad, "account_erasure_reasons.go: the page transaction must end with return currentAdminIn(tx, me, pred)")
	}
	if c, ok := ret.Results[0].(*ast.CallExpr); !ok || guardkit.Callee(c) != "currentAdminIn" || len(c.Args) != 3 {
		bad = append(bad, "account_erasure_reasons.go: the page transaction's last statement must be return currentAdminIn(tx, me, pred)")
	}
	seam, ok := lit.Body.List[len(lit.Body.List)-2].(*ast.IfStmt)
	seamCalled := false
	if ok {
		ast.Inspect(seam, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok && guardkit.Callee(c) == "readBeforeSeal" {
				seamCalled = true
			}
			return true
		})
	}
	if !seamCalled {
		bad = append(bad, "account_erasure_reasons.go: the readBeforeSeal seam must be the statement just before the seal")
	}
	return bad
}

func TestReasonPageSealIsLast(t *testing.T) {
	b, err := os.ReadFile("account_erasure_reasons.go")
	if err != nil {
		t.Fatal(err)
	}
	real := string(b)
	for _, v := range reasonPageSealProblems(real) {
		t.Error(v)
	}
	with := func(old, replacement string) string {
		if strings.Count(real, old) != 1 {
			t.Fatalf("fixture anchor %q must occur once, found %d", old, strings.Count(real, old))
		}
		return strings.Replace(real, old, replacement, 1)
	}
	seal := "\t\t// Last statement: nothing locks after it.\n\t\treturn currentAdminIn(tx, me, pred)"
	for _, tc := range []struct{ name, src string }{
		{"no seal", with(seal, "\t\treturn nil")},
		{"a statement after the seal", with(seal, "\t\tif err := currentAdminIn(tx, me, pred); err != nil {\n\t\t\treturn err\n\t\t}\n\t\treturn tx.Exec(\"SELECT 1\").Error")},
		{"a locking statement after the seam", with(seal, "\t\ttx.Exec(\"SELECT id FROM users WHERE id = 1 FOR UPDATE\")\n\t\treturn currentAdminIn(tx, me, pred)")},
		{"a second seal call", with("// --- the scanner -", "func (a *App) otherPage(tx *gorm.DB, me *models.User) error { return currentAdminIn(tx, me, nil) }\n\n// --- the scanner -")},
		{"the seam moved", with("\t\tif a.readBeforeSeal != nil {\n\t\t\ta.readBeforeSeal(tx)\n\t\t}\n", "")},
	} {
		if bad := reasonPageSealProblems(tc.src); len(bad) == 0 {
			t.Errorf("%s: the guard accepted it", tc.name)
		}
	}
}

// ---------------------------------------------------------------------------
// Signed request-id order (T07B-2 scaling, F1).
// ---------------------------------------------------------------------------

// reasonSQLOrderProblems parses src and checks every SQL string built from
// reasonSelect or reasonProjection() that orders its rows. reasonProjection
// casts the bigint id with id::text and no alias, so the output column is also
// called id and a bare ORDER BY id binds to that text: -1 sorts before -2 and
// 10 before 2, while the scanner's cursor (which selects the real bigint)
// follows signed order. The statement must say ORDER BY contact_requests.id,
// which binds to the table's bigint column. Parse-only: it joins the string
// literals of a + chain that names one of the two identifiers; it does not see
// a statement assembled from variables, a constant in another file or SQL sent
// around GORM.
func reasonSQLOrderProblems(path, src string) []string {
	parsed := guardkit.Sources{path: src}.Parse(0)
	if len(parsed.Problems) > 0 {
		return parsed.Problems
	}
	fset, tree := parsed.Fset, parsed.Files[0].AST
	var bad []string
	inner := map[ast.Node]bool{}
	ast.Inspect(tree, func(n ast.Node) bool {
		be, ok := n.(*ast.BinaryExpr)
		if !ok || be.Op != token.ADD || inner[be] {
			return true
		}
		var parts []ast.Expr
		var flatten func(e ast.Expr)
		flatten = func(e ast.Expr) {
			if b, ok := e.(*ast.BinaryExpr); ok && b.Op == token.ADD {
				inner[b] = true
				flatten(b.X)
				flatten(b.Y)
				return
			}
			parts = append(parts, e)
		}
		flatten(be)
		var sql strings.Builder
		names := false
		for _, p := range parts {
			switch v := p.(type) {
			case *ast.BasicLit:
				if v.Kind == token.STRING {
					if s, err := strconv.Unquote(v.Value); err == nil {
						sql.WriteString(s)
					}
				}
			case *ast.Ident:
				if v.Name == "reasonSelect" {
					names = true
					sql.WriteString(" ")
				}
			case *ast.CallExpr:
				if id, ok := v.Fun.(*ast.Ident); ok && id.Name == "reasonProjection" {
					names = true
					sql.WriteString(" ")
				}
			}
		}
		text := sql.String()
		if names && strings.Contains(strings.ToUpper(text), "ORDER BY") && !strings.Contains(text, "ORDER BY contact_requests.id") {
			bad = append(bad, fmt.Sprintf("%s:%d: a statement that selects the review projection orders by something other than contact_requests.id: %q", path, fset.Position(be.Pos()).Line, strings.TrimSpace(text)))
		}
		return true
	})
	return bad
}

func TestReasonSQLOrderGuard(t *testing.T) {
	var found int
	production := guardkit.Production(t)
	for _, name := range production.Names() {
		src := production[name]
		if strings.Contains(src, "reasonSelect") || strings.Contains(src, "reasonProjection") {
			found++
		}
		for _, v := range reasonSQLOrderProblems(name, src) {
			t.Error(v)
		}
	}
	if found == 0 {
		t.Fatal("no production file names reasonSelect: the guard reads nothing")
	}
	wrap := func(clause string) string {
		return "package main\n\nfunc f() {\n\ttx.Raw(\"SELECT \"+reasonSelect+\" FROM contact_requests WHERE reason <> '' " + clause + "\")\n}\n"
	}
	for name, src := range map[string]string{
		"ORDER BY id":                     wrap("ORDER BY id"),
		"ORDER BY id FOR UPDATE":          wrap("ORDER BY id FOR UPDATE"),
		"ORDER BY 1":                      wrap("ORDER BY 1"),
		"lower case":                      wrap("order by id"),
		"a projection call, not a select": "package main\n\nfunc f() {\n\ttx.Raw(\"SELECT \" + reasonProjection() + \" FROM contact_requests ORDER BY id\")\n}\n",
	} {
		if len(reasonSQLOrderProblems("fixture.go", src)) == 0 {
			t.Errorf("%s: the guard accepted it", name)
		}
	}
	for name, src := range map[string]string{
		"qualified":           wrap("ORDER BY contact_requests.id"),
		"qualified, locked":   wrap("ORDER BY contact_requests.id FOR UPDATE"),
		"no ordering":         wrap(""),
		"another select list": "package main\n\nfunc f() {\n\ttx.Raw(\"SELECT id FROM contact_requests ORDER BY id\")\n}\n",
	} {
		if bad := reasonSQLOrderProblems("fixture.go", src); len(bad) != 0 {
			t.Errorf("%s: the guard refused it: %v", name, bad)
		}
	}
}

// The scanner's statements carry no NULL-or-condition: a generic plan cannot
// serve "$1 IS NULL OR id > $1" from the primary key, so a continuation would
// rescan from the start (F5). Source-level check; TestReasonScanSQLShape
// proves the plans.
func TestReasonScanSQLHasNoNullOrCondition(t *testing.T) {
	for name, sql := range map[string]string{"initial": reasonScanInitialSQL, "continuation": reasonScanContinuationSQL} {
		up := strings.ToUpper(sql)
		if strings.Contains(up, " IS NULL") || strings.Contains(up, " OR ") {
			t.Errorf("the %s scanner statement has a NULL-or condition: %s", name, sql)
		}
		if !strings.Contains(up, "ORDER BY ID LIMIT") {
			t.Errorf("the %s scanner statement must order by id and limit: %s", name, sql)
		}
	}
	if strings.Contains(strings.ToUpper(reasonScanInitialSQL), "WHERE") {
		t.Error("the initial scanner statement must have no id predicate")
	}
	if !strings.Contains(reasonScanContinuationSQL, "WHERE id > ?::bigint") {
		t.Error("the continuation must filter on id > the cursor")
	}
}

// The probe for "is there any reason" asks the server for one row. LIMIT 1
// bounds what comes back and what is locked, not what is read to find it; this
// pins that the probe is not a Go-side early break over a full read.
func TestReasonProbeUsesLimitOne(t *testing.T) {
	if !strings.Contains(strings.ToUpper(reasonProbeSQL), "LIMIT 1") || strings.Contains(strings.ToUpper(reasonProbeSQL), "FOR UPDATE") {
		t.Errorf("the probe must be a plain LIMIT 1 statement: %s", reasonProbeSQL)
	}
	b, err := os.ReadFile("account_erasure_reasons.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "func reasonSetExistsIn(")
	if i < 0 {
		t.Fatal("reasonSetExistsIn is gone")
	}
	body := src[i:]
	if j := strings.Index(body, "\n}\n"); j > 0 {
		body = body[:j]
	}
	if !strings.Contains(body, "reasonProbeSQL") || strings.Count(body, "rows.Next()") != 1 {
		t.Errorf("the probe must run reasonProbeSQL and read at most one row:\n%s", body)
	}
}
