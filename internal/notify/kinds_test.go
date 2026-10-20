package notify

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// Every string constant in kinds.go whose value looks like a kind key
// ("thing.event") must be registered, so a forgotten register call fails
// here instead of at Send time (#79).
func TestEveryKindConstIsRegistered(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "kinds.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST {
			continue
		}
		for _, s := range g.Specs {
			vs := s.(*ast.ValueSpec)
			for i, v := range vs.Values {
				lit, ok := v.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				key, _ := strconv.Unquote(lit.Value)
				if !strings.Contains(key, ".") {
					continue
				}
				found++
				if _, ok := registry[key]; !ok {
					t.Errorf("kind %s (%q) is declared but never registered", vs.Names[i].Name, key)
				}
			}
		}
	}
	if found != len(registry) {
		t.Errorf("kinds.go declares %d kind keys, registry has %d", found, len(registry))
	}
}
