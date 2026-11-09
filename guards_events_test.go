package main

import (
	"go/ast"
	"os"
	"regexp"
	"strings"
	"testing"

	"fold/internal/guardkit"
)

// Repo guards from the /correct pass (Oct 2026). Each one fails on a real
// past mistake; see the rule table in AGENTS.md.

// groupDetailRe matches reading a group's name or place, the facts an
// invite-only group must not leak through its opened events (PR #41).
var groupDetailRe = regexp.MustCompile(`\b(g|f\.group|group)\.(Location|Name)\b|\br\.Group(Location|Name)\b`)

// eventOutputFiles build event and notice output for viewers who may not
// see the event's group.
var eventOutputFiles = []string{"api_occurrence_access.go", "api_events.go", "api_event_access.go", "api_notifications.go", "notices_groups.go", "notices_events.go", "api_group_visibility.go", "api_guest.go", "api_registrations.go", "api_calendar.go", "api_feeds.go", "date_projector.go"}

func groupDetailViolations(src string) []string {
	var bad []string
	for i, line := range strings.Split(src, "\n") {
		if groupDetailRe.MatchString(line) && !strings.Contains(line, "// group-ok:") {
			bad = append(bad, strings.TrimSpace(line)+" (line "+itoa(uint(i+1))+")")
		}
	}
	return bad
}

// rawGroupRe matches the raw sources of a group in the date projector: the
// unfiltered group of the viewer's facts and the loaded groups map. Neither may
// reach output; only a.groupShownTo's result may.
var rawGroupRe = regexp.MustCompile(`\bf\.group\b|\.Groups\[`)

// projectorFiles hold the one place a date's group is decided, so a raw group
// read there is checked line by line, not only by the field names above.
var projectorFiles = []string{"date_projector.go"}

// rawGroupViolations flags a raw group source on a line that neither passes it
// to groupShownTo (the filter) or asGroup (a policy input) nor says why it may
// be read with `// group-ok:`.
func rawGroupViolations(src string) []string {
	var bad []string
	for i, line := range strings.Split(src, "\n") {
		if !rawGroupRe.MatchString(line) || strings.Contains(line, "// group-ok:") ||
			strings.Contains(line, "groupShownTo(") || strings.Contains(line, "asGroup(") {
			continue
		}
		bad = append(bad, strings.TrimSpace(line)+" (line "+itoa(uint(i+1))+")")
	}
	return bad
}

func TestGroupDetailsGoThroughGroupShownTo(t *testing.T) {
	for _, f := range projectorFiles {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range rawGroupViolations(string(src)) {
			t.Errorf("%s: %s\n  a date's group is p.Group, set from a.groupShownTo only; "+
				"or end the line with `// group-ok: <why this viewer may see the group>`", f, v)
		}
	}
	for _, f := range eventOutputFiles {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range groupDetailViolations(string(src)) {
			t.Errorf("%s: %s\n  read the group through a.groupShownTo(me, g, rel) (nil when hidden), "+
				"or end the line with `// group-ok: <why this viewer may see the group>`", f, v)
		}
	}
}

// The guard catches the location leak fixed in 7b50f43 (PR #41).
func TestGroupDetailGuardCatchesPR41Leak(t *testing.T) {
	before := "\t\tif g != nil {\n\t\t\tloc = g.Location\n\t\t}\n"
	if len(groupDetailViolations(before)) != 1 {
		t.Fatal("guard missed the pre-#41 code")
	}
}

// The projector guard catches a group taken from the facts without the filter.
func TestRawGroupGuardCatchesUnfilteredGroup(t *testing.T) {
	for _, bad := range []string{"\tp.Group = f.group\n", "\tp.Group = vs.Groups[*ev.GroupID]\n"} {
		if len(rawGroupViolations(bad)) != 1 {
			t.Fatalf("guard missed %q", bad)
		}
	}
	for _, ok := range []string{"\tp.Group = a.groupShownTo(viewer, f.group, f.rel)\n", "\tasGroup(f.group)\n", "\tg := f.group // group-ok: manager only\n"} {
		if len(rawGroupViolations(ok)) != 0 {
			t.Fatalf("guard flagged %q", ok)
		}
	}
}

// --- One list filter for event visibility ---
//
// Event lists load candidates (dateCandidates or eventCandidates) then apply
// visibleSet.canSeeDate. Candidate parents never authorize a series, so no
// function loops over events calling policy.CanSeeEvent; the loader itself
// skips events whose group is missing.

func TestOneEventVisibilityFilter(t *testing.T) {
	parsed := guardkit.Production(t).Parse(0)
	for _, problem := range parsed.Problems {
		t.Error(problem)
	}
	fset := parsed.Fset
	for _, pf := range parsed.Files {
		f := pf.AST
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				var body *ast.BlockStmt
				switch l := n.(type) {
				case *ast.RangeStmt:
					body = l.Body
				case *ast.ForStmt:
					body = l.Body
				default:
					return true
				}
				ast.Inspect(body, func(m ast.Node) bool {
					if call, ok := m.(*ast.CallExpr); ok {
						if sel, ok := call.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "CanSeeEvent" || sel.Sel.Name == "canSee") {
							t.Errorf("%s: %s filters a list with CanSeeEvent (policy.CanSeeEvent or eventFacts.canSee); load candidates with dateCandidates or eventCandidates and filter dates with canSeeDate", fset.Position(call.Pos()), fn.Name.Name)
						}
					}
					return true
				})
				return false
			})
		}
	}
}

// Registration subjects must pass through the mixed holder scope; membership
// joins remain account-only.
func TestRegistrationQueriesUseSubjectScope(t *testing.T) {
	for file, src := range guardkit.Production(t) {
		if strings.Contains(src, `listedUsers("registrations")`) {
			t.Errorf("%s uses the account-only registration join", file)
		}
	}
}
