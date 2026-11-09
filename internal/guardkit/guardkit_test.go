package guardkit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeTree(t *testing.T, files ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, f := range files {
		p := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("package x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// The walk reaches every production directory however deep, and skips only
// tests, fixtures, the frontend, vendored code and hidden directories.
func TestReadProductionSkipRules(t *testing.T) {
	root := writeTree(t,
		"main.go", "models/m.go", "internal/a/b/c/deep.go", "internal/web/w.go", "internal/mail/send.go",
		"main_test.go", "internal/a/a_test.go", "testdata/f.go", "internal/a/testdata/f.go",
		"web/g.go", "web/node_modules/x/x.go", "node_modules/x.go", "vendor/v.go", "internal/vendor/v.go",
		".git/h.go", "internal/.hidden/h.go", "notes.txt",
	)
	got, err := ReadProduction(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"internal/a/b/c/deep.go", "internal/mail/send.go", "internal/web/w.go", "main.go", "models/m.go"}
	if names := got.Names(); !reflect.DeepEqual(names, want) {
		t.Fatalf("got %v, want %v", names, want)
	}
}

func TestParseReportsUnparsableFilesAndKeepsGoing(t *testing.T) {
	p := Sources{"b.go": "package main\n", "a.go": "package main\nfunc {", "c.go": "package models\n"}.Parse(0)
	if len(p.Problems) != 1 || !strings.HasPrefix(p.Problems[0], "a.go: does not parse: ") {
		t.Fatalf("problems %v", p.Problems)
	}
	if len(p.Files) != 2 || p.Files[0].Path != "b.go" || p.Files[1].Path != "c.go" {
		t.Fatalf("files %v", p.Files)
	}
	if main := p.Package("main"); len(main) != 1 || main[0].Path != "b.go" {
		t.Fatalf("package main files %v", main)
	}
}

func TestPackagesGroupByDirectoryAndClause(t *testing.T) {
	p := Sources{"a.go": "package main\n", "b.go": "package main\n", "internal/m/m.go": "package m\n", "internal/m/x.go": "package other\n"}.Parse(0)
	var got []string
	for _, pkg := range p.Packages() {
		var names []string
		for _, f := range pkg.Files {
			names = append(names, f.Path)
		}
		got = append(got, pkg.Dir+":"+pkg.Name+"="+strings.Join(names, ","))
	}
	want := []string{".:main=a.go,b.go", "internal/m:m=internal/m/m.go", "internal/m:other=internal/m/x.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestUnparenAndCallee(t *testing.T) {
	cases := map[string]string{"f(x)": "f", "(f)(x)": "f", "a.B(x)": "B", "((a.B))(x)": "B", "func() {}()": "", "f(x)(y)": ""}
	for src, want := range cases {
		e, err := parser.ParseExpr(src)
		if err != nil {
			t.Fatal(err)
		}
		if got := Callee(e.(*ast.CallExpr)); got != want {
			t.Errorf("Callee(%s) = %q, want %q", src, got, want)
		}
	}
	e, _ := parser.ParseExpr("((x))")
	if id, ok := Unparen(e).(*ast.Ident); !ok || id.Name != "x" {
		t.Errorf("Unparen left %T", Unparen(e))
	}
}

// The stack ends with the visited node, the parent is the element before it,
// and a node whose visit returned false leaves no stale entry behind.
func TestInspectStackKeepsAnExactStack(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "x.go", "package x\nfunc f() { if a { g(1) }; h(2) }\n", 0)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	InspectStack(f, func(n ast.Node, stack []ast.Node) bool {
		if stack[len(stack)-1] != n {
			t.Fatalf("stack does not end with the node")
		}
		if _, ok := n.(*ast.IfStmt); ok {
			return false // skip g(1)
		}
		if call, ok := n.(*ast.CallExpr); ok {
			var kinds []string
			for _, s := range stack {
				kinds = append(kinds, strings.TrimPrefix(reflect.TypeOf(s).String(), "*ast."))
			}
			seen[Callee(call)] = strings.Join(kinds, "/")
		}
		return true
	})
	if _, ok := seen["g"]; ok {
		t.Error("children of a skipped node were visited")
	}
	if got, want := seen["h"], "File/FuncDecl/BlockStmt/ExprStmt/CallExpr"; got != want {
		t.Errorf("stack for h = %q, want %q", got, want)
	}
}

func TestImportNames(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "x.go", `package x
import (
	"fold/models"
	m2 "fold/models"
	_ "fold/models"
	. "fold/models"
	"fold/internal/mail"
)
`, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ImportNames(f, "fold/models"), map[string]bool{"models": true, "m2": true, "_": true, ".": true}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := ImportNames(f, "fold/internal/mail"); !got["mail"] || len(got) != 1 {
		t.Errorf("mail: %v", got)
	}
	if got := ImportNames(f, "fold/storage"); len(got) != 0 {
		t.Errorf("storage: %v", got)
	}
}

func TestSquash(t *testing.T) {
	if got := Squash("  SELECT 1\n\tFROM  t \n"); got != "SELECT 1 FROM t" {
		t.Errorf("got %q", got)
	}
}

func TestFindFunc(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "x.go", "package x\nfunc a() {}\nfunc (T) b() {}\nvar c = 1\n", 0)
	if err != nil {
		t.Fatal(err)
	}
	if FindFunc(f, "a") == nil || FindFunc(f, "b") == nil {
		t.Error("missed a declared function or method")
	}
	if FindFunc(f, "c") != nil || FindFunc(f, "zz") != nil {
		t.Error("found something that is not a function")
	}
}
