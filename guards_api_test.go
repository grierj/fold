package main

import (
	"go/ast"
	"testing"

	"fold/internal/guardkit"
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
	parsed := guardkit.Production(t).Parse(0)
	for _, problem := range parsed.Problems {
		t.Error(problem)
	}
	// Package main only: the handlers live there. Other packages use the status
	// constant for other reasons (internal/push reads a push service's 429).
	for _, pf := range parsed.Package("main") {
		f, file := pf.Path, pf.AST
		// api_errors.go only names the code for a 429; it sends none itself.
		if f == "api_errors.go" {
			continue
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
