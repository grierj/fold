package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// R4-S4: suppression SQL lives in four places. The suppressions table holds
// an address (plaintext until R4-SC) and its hash, and a reader must look at
// the hash first and the address only on a NULL-hash row. A second reader that
// compares `address = ...` or `lower(btrim(...))` again would miss hashed rows
// the day R4-SC drops the plaintext column. So production source may name the
// table, or models.Suppression, only inside these helpers, apart from literal
// erasure inventory metadata; everything else goes through mail.OpenSuppressionReasons.
//
// Parse-only, the same stance as the mail guard. Known limits: table names
// built from non-literal pieces, reflection, SQL sent around GORM, dot-imports
// of fold/models and cross-package helpers are not seen. The unwanted shapes
// the guard does see are covered by TestSuppressionSQLGuardCatchesBypasses.
var suppressionSQLAllowed = map[string][]string{
	"internal/mail/suppress.go": {"OpenSuppressionReasons", "openSuppressionsSQL"},
	"api_admin_suppressions.go": {"openSuppressions", "apiAdminClearSuppression"},
	"api_email_webhooks.go":     {"applyEmailEvent"},
	"models/models.go":          {"Suppression", "All"},
}

var suppressionTableRe = regexp.MustCompile(`\b(from|join|update|into|table)\s+(public\.)?suppressions\b`)

// erasureInventoryTableLiterals identifies only the Table fields of the root
// inventory's data-only initializer. Never exempt a whole file or declaration:
// SQL in another field, an executable initializer or a sibling declaration
// must still pass through the SQL guards. Inventory coverage stays unchanged.
func erasureInventoryTableLiterals(path string, tree *ast.File) map[*ast.BasicLit]bool {
	tables := map[*ast.BasicLit]bool{}
	if path != "erasure_inventory.go" {
		return tables
	}
	stringLiteral := func(expr ast.Expr) *ast.BasicLit {
		lit, ok := expr.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return nil
		}
		return lit
	}
	for _, decl := range tree.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || len(value.Names) != 1 || value.Names[0].Name != "erasureColumns" || len(value.Values) != 1 {
				continue
			}
			list, ok := value.Values[0].(*ast.CompositeLit)
			if !ok {
				continue
			}
			array, ok := list.Type.(*ast.ArrayType)
			if !ok || array.Len != nil {
				continue
			}
			elem, ok := array.Elt.(*ast.Ident)
			if !ok || elem.Name != "erasureColumn" {
				continue
			}
			for _, expr := range list.Elts {
				row, ok := expr.(*ast.CompositeLit)
				if !ok || row.Type != nil || len(row.Elts) != 5 {
					continue
				}
				plain := true
				for _, field := range row.Elts[:4] {
					plain = plain && stringLiteral(field) != nil
				}
				rules, ok := row.Elts[4].(*ast.CompositeLit)
				if !plain || !ok {
					continue
				}
				ruleType, ok := rules.Type.(*ast.ArrayType)
				if !ok || ruleType.Len != nil {
					continue
				}
				ruleElem, ok := ruleType.Elt.(*ast.Ident)
				if !ok || ruleElem.Name != "string" {
					continue
				}
				for _, rule := range rules.Elts {
					plain = plain && stringLiteral(rule) != nil
				}
				if plain {
					table := stringLiteral(row.Elts[0])
					name, err := strconv.Unquote(table.Value)
					if err == nil && (name == "login_throttles" || name == "suppressions" || name == "email_address_optouts") {
						tables[table] = true
					}
				}
			}
		}
	}
	return tables
}

func TestSQLGuardsScopeErasureInventoryMetadata(t *testing.T) {
	for _, guard := range []struct {
		table, model string
		check        func(map[string]string) []string
	}{
		{"login_throttles", "LoginThrottle", func(sources map[string]string) []string {
			return throttleSQLViolations(sources, nil)
		}},
		{"suppressions", "Suppression", func(sources map[string]string) []string {
			return suppressionSQLViolations(sources, nil)
		}},
		{"email_address_optouts", "EmailAddressOptOut", emailChoiceViolations},
	} {
		t.Run(guard.table, func(t *testing.T) {
			row := fmt.Sprintf(`{%q, "id", "T01", "RET", []string{"T01"}}`, guard.table)
			declaration := "var erasureColumns = []erasureColumn{" + row + "}\n"
			sql := "SELECT * FROM " + guard.table
			for _, tc := range []struct {
				name, path, source string
				wantViolation      bool
			}{
				{"literal metadata", "erasure_inventory.go", declaration, false},
				{"same basename in another directory", "internal/erasure_inventory.go", declaration, true},
				{"another file", "other.go", declaration, true},
				{"another variable", "erasure_inventory.go", strings.Replace(declaration, "erasureColumns", "other", 1), true},
				{"another row type", "erasure_inventory.go", strings.Replace(declaration, "[]erasureColumn", "[]other", 1), true},
				{"SQL in table field", "erasure_inventory.go", strings.Replace(declaration, strconv.Quote(guard.table), strconv.Quote(sql), 1), true},
				{"SQL in column field", "erasure_inventory.go", strings.Replace(declaration, `"id"`, strconv.Quote(sql), 1), true},
				{"SQL in rules", "erasure_inventory.go", strings.Replace(declaration, `[]string{"T01"}`, "[]string{"+strconv.Quote(sql)+"}", 1), true},
				{"Table call beside metadata", "erasure_inventory.go", declaration + fmt.Sprintf("func other() { db.Table(%q) }", guard.table), true},
				{"SQL constant beside metadata", "erasure_inventory.go", declaration + fmt.Sprintf("const query = %q", sql), true},
				{"TableName beside metadata", "erasure_inventory.go", declaration + fmt.Sprintf("func (other) TableName() string { return %q }", guard.table), true},
				{"executable initializer", "erasure_inventory.go", "var erasureColumns = func() []erasureColumn { db.Exec(" + strconv.Quote(sql) + "); return []erasureColumn{" + row + "} }()", true},
				{"executable row field", "erasure_inventory.go", strings.Replace(declaration, `"id"`, "func() string { db.Exec("+strconv.Quote(sql)+"); return \"id\" }()", 1), true},
				{"computed table", "erasure_inventory.go", strings.Replace(declaration, strconv.Quote(guard.table), strconv.Quote(guard.table)+` + ""`, 1), true},
				{"model beside metadata", "erasure_inventory.go", declaration + "func other() { _ = &models." + guard.model + "{} }", true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					src := "package main\nimport \"fold/models\"\n" + tc.source
					bad := guard.check(map[string]string{tc.path: src})
					if strings.Contains(strings.Join(bad, "\n"), "parse:") {
						t.Fatalf("invalid guard fixture: %v", bad)
					}
					if (len(bad) != 0) != tc.wantViolation {
						t.Fatalf("want violation %t, got %v", tc.wantViolation, bad)
					}
				})
			}
		})
	}
}

// suppressionSQLViolations checks parsed sources keyed by slash path from the
// repository root. A flag outside an allowed top-level function, constant or
// type is a violation; so is an allowed helper (outside models/) that never
// mentions the hash, a flagged literal in an allowed helper that normalizes
// in SQL, and an allowance that no longer matches any source.
func suppressionSQLViolations(sources map[string]string, allowed map[string][]string) []string {
	const hint = " (read open suppressions with mail.OpenSuppressionReasons; hash first, then the address of a NULL-hash row)"
	positions := token.NewFileSet()
	var bad []string
	seen := map[string]bool{}
	paths := make([]string, 0, len(sources))
	for path := range sources {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		src := sources[path]
		tree, err := parser.ParseFile(positions, path, src, 0)
		if err != nil {
			bad = append(bad, path+": cannot parse: "+err.Error())
			continue
		}
		inventoryTables := erasureInventoryTableLiterals(path, tree)
		inModels := strings.HasPrefix(path, "models/")
		modelNames := map[string]bool{}
		for _, imp := range tree.Imports {
			if p, err := strconv.Unquote(imp.Path.Value); err != nil || p != "fold/models" {
				continue
			}
			name := "models"
			if imp.Name != nil {
				name = imp.Name.Name
			}
			modelNames[name] = true
		}
		inspect := func(owner string, node ast.Node) {
			isAllowed := false
			for _, name := range allowed[path] {
				if name == owner {
					isAllowed = true
					seen[path+" "+name] = true
				}
			}
			var flagged []string
			ast.Inspect(node, func(n ast.Node) bool {
				at := func() string { return fmt.Sprintf("%s:%d", path, positions.Position(n.Pos()).Line) }
				switch n := n.(type) {
				case *ast.BasicLit:
					if n.Kind != token.STRING || inventoryTables[n] {
						return true
					}
					text, err := strconv.Unquote(n.Value)
					if err != nil {
						return true
					}
					text = strings.ToLower(strings.Join(strings.Fields(text), " "))
					if suppressionTableRe.MatchString(text) || text == "suppressions" || text == "public.suppressions" {
						flagged = append(flagged, at()+": SQL on the suppressions table in "+owner)
						if isAllowed && (strings.Contains(text, "lower(") || strings.Contains(text, "btrim(")) {
							bad = append(bad, at()+": "+owner+" normalizes an address in SQL (lower/btrim); normalize in Go with mail.NormalizeAddress"+hint)
						}
					}
				case *ast.SelectorExpr:
					if x, ok := n.X.(*ast.Ident); ok && modelNames[x.Name] && n.Sel.Name == "Suppression" {
						flagged = append(flagged, at()+": models.Suppression used in "+owner)
					}
				case *ast.TypeSpec:
					if n.Name.Name == "Suppression" && !inModels {
						flagged = append(flagged, at()+": a type named Suppression in "+owner)
					}
				}
				return true
			})
			if !isAllowed {
				for _, f := range flagged {
					bad = append(bad, f+hint)
				}
				return
			}
			if !inModels {
				text := src[positions.Position(node.Pos()).Offset:positions.Position(node.End()).Offset]
				if !strings.Contains(text, "address_hash") && !strings.Contains(text, "AddressHash") {
					bad = append(bad, fmt.Sprintf("%s:%d: %s is allowed to touch suppressions but never mentions address_hash or AddressHash%s", path, positions.Position(node.Pos()).Line, owner, hint))
				}
			}
		}
		for _, decl := range tree.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				inspect(d.Name.Name, d)
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.ValueSpec:
						owner := ""
						if len(s.Names) > 0 {
							owner = s.Names[0].Name
						}
						inspect(owner, s)
					case *ast.TypeSpec:
						inspect(s.Name.Name, s)
					}
				}
			}
		}
	}
	for path, names := range allowed {
		for _, name := range names {
			if !seen[path+" "+name] {
				bad = append(bad, fmt.Sprintf("%s: suppressionSQLAllowed names %s, which no longer exists there; update the list and keep the SQL in helpers that use the hash", path, name))
			}
		}
	}
	sort.Strings(bad)
	return bad
}

func TestSuppressionSQLStaysInItsHelpers(t *testing.T) {
	sources := map[string]string{}
	if err := walkProductionGo(".", func(path string, src []byte) error {
		sources[path] = string(src)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, v := range suppressionSQLViolations(sources, suppressionSQLAllowed) {
		t.Error(v)
	}
}

// The clean tree holds every allowed helper once, using the hash, and nothing
// else touching the table. Each bypass below changes it in one way.
func cleanSuppressionSources() map[string]string {
	return map[string]string{
		"internal/mail/suppress.go": `package mail
const openSuppressionsSQL = "SELECT address, address_hash, reason FROM suppressions WHERE address_hash IN ?"
func OpenSuppressionReasons() { _ = StoredAddressHash }
`,
		"api_admin_suppressions.go": `package main
import "fold/models"
func openSuppressions() { var rows []models.Suppression; _ = rows[0].AddressHash }
func (a *App) apiAdminClearSuppression() { q := "UPDATE suppressions SET cleared_at = ? WHERE address_hash = ?"; _ = q }
`,
		"api_email_webhooks.go": `package main
func applyEmailEvent() { q := "INSERT INTO suppressions (channel, address, address_hash) VALUES (?, ?, ?)"; _ = q }
`,
		"models/models.go": `package models
type Suppression struct{ AddressHash *string }
func All() []any { return []any{&Suppression{}} }
`,
		"api_other.go": `package main
func other() {}
`,
	}
}

func TestSuppressionSQLGuardCatchesBypasses(t *testing.T) {
	if bad := suppressionSQLViolations(cleanSuppressionSources(), suppressionSQLAllowed); len(bad) != 0 {
		t.Fatalf("the clean fixture failed: %v", bad)
	}
	for name, tc := range map[string]struct {
		file, source string // replaces or adds this file
		want         string // a substring of the violation
	}{
		"a second subquery that normalizes in SQL": {"api_other.go", `package main
func other() { q := "SELECT 1 WHERE EXISTS (SELECT 1 FROM suppressions s WHERE s.address = lower(btrim(users.email)))"; _ = q }
`, "api_other.go:2"},
		"a Table call": {"api_other.go", `package main
func other() { db.Table("suppressions").Find(&x) }
`, "api_other.go:2"},
		"concatenated SQL": {"api_other.go", `package main
func other() { q := "SELECT reason FROM " + "suppressions"; _ = q }
`, "api_other.go:2"},
		"a model with a TableName": {"api_other.go", `package main
type thing struct{}
func (thing) TableName() string { return "suppressions" }
`, "api_other.go:3"},
		"a local Suppression type": {"api_other.go", `package main
type Suppression struct{}
`, "api_other.go:2"},
		"models.Suppression elsewhere": {"api_other.go", `package main
import "fold/models"
func other() { var rows []models.Suppression; _ = rows }
`, "api_other.go:3"},
		"models.Suppression through an import alias": {"api_other.go", `package main
import m "fold/models"
func other() { _ = &m.Suppression{} }
`, "api_other.go:3"},
		"a second reader in an allowed file": {"api_admin_suppressions.go", `package main
import "fold/models"
func openSuppressions() { var rows []models.Suppression; _ = rows[0].AddressHash }
func (a *App) apiAdminClearSuppression() { q := "UPDATE suppressions SET cleared_at = ? WHERE address_hash = ?"; _ = q }
func anotherReader() { q := "SELECT reason FROM suppressions WHERE address_hash = ?"; _ = q }
`, "api_admin_suppressions.go:5"},
		"an allowed function that normalizes in SQL": {"api_email_webhooks.go", `package main
func applyEmailEvent() { q := "INSERT INTO suppressions (address, address_hash) SELECT lower(btrim(email)), ?"; _ = q }
`, "normalizes an address in SQL"},
		"an allowed function that never uses the hash": {"api_email_webhooks.go", `package main
func applyEmailEvent() { q := "INSERT INTO suppressions (channel, address) VALUES (?, ?)"; _ = q }
`, "never mentions address_hash"},
		"an allowed helper that was renamed": {"api_email_webhooks.go", `package main
func applyEvent() {}
`, "names applyEmailEvent, which no longer exists"},
	} {
		sources := cleanSuppressionSources()
		sources[tc.file] = tc.source
		bad := suppressionSQLViolations(sources, suppressionSQLAllowed)
		found := false
		for _, v := range bad {
			if strings.Contains(v, tc.want) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: want a violation containing %q, got %v", name, tc.want, bad)
		}
	}
	// A violation names the helper to use.
	sources := cleanSuppressionSources()
	sources["api_other.go"] = "package main\nfunc other() { db.Table(\"suppressions\") }\n"
	if bad := suppressionSQLViolations(sources, suppressionSQLAllowed); len(bad) != 1 || !strings.Contains(bad[0], "mail.OpenSuppressionReasons") {
		t.Errorf("violation = %v, want one that names mail.OpenSuppressionReasons", bad)
	}
}
