package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// DEC-52: rate limits and account checks never reach a user on a public
// form; it answers as usual and logs. Only sign-in, authenticated password
// reauthentication (the same lockout), and a signed-in leader's invitation
// limit answer 429. Keep the exceptions function-specific so a new public
// endpoint in the same file cannot silently gain an exception.
func TestOnlyLoginAndInvitesAnswer429(t *testing.T) {
	allowed := map[string]map[string]bool{
		"api_auth.go":        {"apiLogin": true, "reauthenticate": true},
		"api_invitations.go": {"apiCreateInvitation": true},
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		// api_errors.go only names the code for a 429; it sends none itself.
		if strings.HasSuffix(f, "_test.go") || f == "api_errors.go" {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && allowed[f][fn.Name.Name] {
				continue
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && id.Name == "StatusTooManyRequests" {
					t.Errorf("%s answers 429 outside an allowed handler; public forms answer as usual and log instead (DEC-52)", f)
				}
				return true
			})
		}
	}
}
