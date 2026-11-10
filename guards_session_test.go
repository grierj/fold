package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"strings"
	"testing"

	"fold/internal/guardkit"
)

// ---------------------------------------------------------------------------
// "This session is still current" is decided by sessionCurrent (and the actor
// locks built on it) alone: no other production code compares a row's
// session_version or logout_version by hand.
//
// Parse-only: it flags ==/!= with a .SessionVersion or .LogoutVersion operand in
// the root package outside sessionEpochExempt, matching by field name; it does
// not see comparisons in other packages, through helpers, or in SQL.
// ---------------------------------------------------------------------------

// sessionEpochExempt are the files that may compare an epoch themselves, each
// for a reason that is not a session recheck.
var sessionEpochExempt = map[string]string{
	"session_state.go":      "the one predicate",
	"api_guardian_email.go": "a signed emailed link binds the guardian's credential epoch at issue; there is no session",
}

// sessionEpochProblems reports every ==/!= with a .SessionVersion or
// .LogoutVersion operand in the root package outside the exempt files.
func sessionEpochProblems(sources guardkit.Sources) []string {
	parsed := sources.Parse(0)
	bad := append([]string{}, parsed.Problems...)
	for _, f := range parsed.Package("main") {
		if strings.Contains(f.Path, "/") || sessionEpochExempt[f.Path] != "" {
			continue
		}
		ast.Inspect(f.AST, func(n ast.Node) bool {
			b, ok := n.(*ast.BinaryExpr)
			if !ok || (b.Op != token.EQL && b.Op != token.NEQ) {
				return true
			}
			for _, side := range []ast.Expr{b.X, b.Y} {
				if sel, ok := guardkit.Unparen(side).(*ast.SelectorExpr); ok && (sel.Sel.Name == "SessionVersion" || sel.Sel.Name == "LogoutVersion") {
					bad = append(bad, fmt.Sprintf("%s:%d: compares %s by hand; decide with sessionCurrent or lockActorIn", f.Path, parsed.Fset.Position(b.Pos()).Line, sel.Sel.Name))
					break
				}
			}
			return true
		})
	}
	return bad
}

func TestSessionEpochsAreComparedOnlyBySessionCurrent(t *testing.T) {
	for _, p := range sessionEpochProblems(guardkit.Production(t)) {
		t.Error(p)
	}
	for name, src := range map[string]string{
		"session version": "package main\nfunc f(a, b *U) bool { return a.SessionVersion != b.SessionVersion }\ntype U struct{ SessionVersion int }",
		"logout version":  "package main\nfunc f(a, b *U) bool { return (a.LogoutVersion) == b.LogoutVersion }\ntype U struct{ LogoutVersion int }",
	} {
		if len(sessionEpochProblems(guardkit.Sources{"x.go": src})) == 0 {
			t.Errorf("%s: the guard accepted a hand-written epoch comparison", name)
		}
	}
	if p := sessionEpochProblems(guardkit.Sources{"x.go": "package main\nfunc f(a, b *U) bool { return a.ID == b.ID }\ntype U struct{ ID int }"}); len(p) != 0 {
		t.Errorf("the guard flagged an unrelated comparison: %v", p)
	}
}
