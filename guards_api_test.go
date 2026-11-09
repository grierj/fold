package main

import (
	"go/ast"
	"net/http"
	"testing"

	"fold/internal/guardkit"
)

// DEC-52: rate limits and account checks never reach a user on a public
// form; it answers as usual and logs. Only sign-in, authenticated password
// reauthentication (the same lockout), and a signed-in leader's invitation
// limit answer 429. The two 429 answers are declared values; keep the
// exceptions function-specific so a new public endpoint in the same file
// cannot silently gain an exception.
func TestOnlyLoginAndInvitesAnswer429(t *testing.T) {
	// answers429 maps each declared 429 value to the functions that may use it.
	answers429 := map[string]map[string]map[string]bool{
		"errLockedOut":       {"api_auth.go": {"apiLogin": true, "reauthenticate": true}},
		"errInvitationLimit": {"api_invitations.go": {"apiCreateInvitation": true}},
	}
	// The values are the only declared errors with that status.
	for _, e := range declaredAPIErrors {
		if e.Status == http.StatusTooManyRequests && e.Code != "locked_out" && e.Code != "invite_limit" {
			t.Errorf("%q is declared with status 429; only sign-in and a leader's invitation limit may answer it (DEC-52)", e.Message)
		}
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
			// The declaration of a 429 value may name the status.
			declares := map[string]bool{}
			if gd, ok := decl.(*ast.GenDecl); ok {
				for _, spec := range gd.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok {
						for _, name := range vs.Names {
							if answers429[name.Name] != nil {
								declares[name.Name] = true
							}
						}
					}
				}
			}
			fn, isFunc := decl.(*ast.FuncDecl)
			ast.Inspect(decl, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if !ok {
					return true
				}
				if id.Name == "StatusTooManyRequests" && len(declares) == 0 {
					t.Errorf("%s answers 429 outside the declared 429 values; public forms answer as usual and log instead (DEC-52)", f)
				}
				if uses := answers429[id.Name]; uses != nil && !declares[id.Name] {
					if !isFunc || !uses[f][fn.Name.Name] {
						t.Errorf("%s uses %s outside an allowed handler; public forms answer as usual and log instead (DEC-52)", f, id.Name)
					}
				}
				return true
			})
		}
	}
}
