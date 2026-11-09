// Package guardkit is the shared toolkit of the repository's source guards:
// the tests that read production Go source and fail when a rule is broken
// (mail, throttle keys, contact-request text, the restore fence and so on).
//
// It is imported only by tests. The deadcode gate reports its exported
// functions as unreachable from the binary, so .deadcode-allow lists the
// package the same way it lists internal/testdb.
//
// Three things live here so no guard keeps its own copy:
//
//   - File discovery. [ReadProduction] walks the whole tree once, with one skip
//     rule set, so code moved into a subpackage is still checked. A guard that
//     is only about one package filters the parsed files by package name
//     ([Parsed.Package]); it never narrows by globbing a directory.
//   - Parsing. [Sources.Parse] parses a path-to-text map, the real tree and a
//     test fixture alike, in one sorted pass with one parse-error shape.
//   - AST helpers: [Unparen], [Callee], [InspectStack], [ImportNames] and
//     [Squash].
//
// The analysis is parse-only (go/parser and go/ast), not a type checker. Each
// guard states its own limits.
package guardkit

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Sources maps a slash-separated path, relative to the scanned root, to the
// file's text.
type Sources map[string]string

// skipDir reports whether a directory is not shipped Go application source:
// repository metadata and other hidden directories, the frontend tree,
// vendored code, test fixtures and frontend dependencies. rel is the path
// relative to the root.
func skipDir(rel, name string) bool {
	if rel == "." {
		return false
	}
	return strings.HasPrefix(name, ".") || rel == "web" || name == "vendor" || name == "testdata" || name == "node_modules"
}

// ReadProduction reads every production Go file under root, however deeply it
// is nested: every .go file except tests, in directories skipDir does not
// exclude. Keys are slash paths relative to root.
func ReadProduction(root string) (Sources, error) {
	out := Sources{}
	err := fs.WalkDir(os.DirFS(root), ".", func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if skipDir(p, entry.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil {
			return err
		}
		out[p] = string(src)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Production is ReadProduction(".") for a test running in the repository root, the
// working directory of the root package's tests. It fails the test on a read error.
func Production(t testing.TB) Sources {
	t.Helper()
	sources, err := ReadProduction(".")
	if err != nil {
		t.Fatal(err)
	}
	return sources
}

func (s Sources) Names() []string {
	names := make([]string, 0, len(s))
	for name := range s {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

type File struct {
	Path string // slash path relative to the scanned root
	Src  string
	AST  *ast.File
}

// Dir is the file's directory, "." for the root.
func (f *File) Dir() string { return path.Dir(f.Path) }

// Parsed is a set of files parsed into one token.FileSet.
type Parsed struct {
	Fset  *token.FileSet
	Files []*File // sorted by Path; only files that parsed
	// Problems holds one "<path>: does not parse: <error>" line per file that
	// did not parse. A guard reports them as violations: a file it cannot read
	// is a file it did not check.
	Problems []string
}

// Parse parses every file in sorted path order. mode is passed to go/parser;
// guards that read object resolution (ast.Ident.Obj) need the zero mode.
func (s Sources) Parse(mode parser.Mode) *Parsed {
	p := &Parsed{Fset: token.NewFileSet()}
	for _, name := range s.Names() {
		tree, err := parser.ParseFile(p.Fset, name, s[name], mode)
		if err != nil {
			p.Problems = append(p.Problems, fmt.Sprintf("%s: does not parse: %v", name, err))
			continue
		}
		p.Files = append(p.Files, &File{Path: name, Src: s[name], AST: tree})
	}
	return p
}

func (p *Parsed) File(path string) *File {
	for _, f := range p.Files {
		if f.Path == path {
			return f
		}
	}
	return nil
}

// Package returns the files whose package clause names pkg. A guard whose
// subject is one package (the root package main, say) uses this instead of a
// directory glob, so the choice is explicit and a moved file is still seen.
func (p *Parsed) Package(pkg string) []*File {
	var out []*File
	for _, f := range p.Files {
		if f.AST.Name.Name == pkg {
			out = append(out, f)
		}
	}
	return out
}

// Package is the files of one directory that share a package clause.
type Package struct {
	Dir   string
	Name  string
	Files []*File
}

// Packages groups the files by directory and package clause, sorted by
// directory then name.
func (p *Parsed) Packages() []*Package {
	byKey := map[string]*Package{}
	var keys []string
	for _, f := range p.Files {
		key := f.Dir() + ":" + f.AST.Name.Name
		pkg, ok := byKey[key]
		if !ok {
			pkg = &Package{Dir: f.Dir(), Name: f.AST.Name.Name}
			byKey[key] = pkg
			keys = append(keys, key)
		}
		pkg.Files = append(pkg.Files, f)
	}
	sort.Strings(keys)
	out := make([]*Package, 0, len(keys))
	for _, k := range keys {
		out = append(out, byKey[k])
	}
	return out
}

func Unparen(e ast.Expr) ast.Expr {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			return e
		}
		e = p.X
	}
}

// Callee is the last name of a call's function, seen through parentheses:
// "f" for f(x), "M" for x.M(y). It is "" for any other callee (a function
// literal, an index expression, a call result).
func Callee(call *ast.CallExpr) string {
	switch f := Unparen(call.Fun).(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

// InspectStack walks root like ast.Inspect and hands fn the chain of nodes from
// root down to n; the last element is n itself, so the parent is
// stack[len(stack)-2]. The slice is reused, so a callback that keeps it copies
// it. Returning false skips n's children, as in ast.Inspect.
func InspectStack(root ast.Node, fn func(n ast.Node, stack []ast.Node) bool) {
	var stack []ast.Node
	ast.Inspect(root, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		if !fn(n, stack) {
			// ast.Inspect makes no closing call for a node whose visit
			// returned false, so pop here.
			stack = stack[:len(stack)-1]
			return false
		}
		return true
	})
}

// ImportNames returns the local names under which file imports importPath: the
// alias if there is one, else the last path element. A dot import appears as
// "." and a blank import as "_"; the caller decides what those mean.
func ImportNames(file *ast.File, importPath string) map[string]bool {
	out := map[string]bool{}
	for _, imp := range file.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p != importPath {
			continue
		}
		name := path.Base(p)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		out[name] = true
	}
	return out
}

// Squash collapses every run of white space to one space and trims the ends,
// so a SQL literal compares equal however it is wrapped.
func Squash(s string) string { return strings.Join(strings.Fields(s), " ") }

func FindFunc(file *ast.File, name string) *ast.FuncDecl {
	for _, d := range file.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == name {
			return fd
		}
	}
	return nil
}
