package main

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Repo guards from the /correct pass (Oct 2026). Each one fails on a real
// past mistake; see the rule table in AGENTS.md.

// groupDetailRe matches reading a group's name or place, the facts an
// invite-only group must not leak through its opened events (PR #41).
var groupDetailRe = regexp.MustCompile(`\b(g|f\.group|group)\.(Location|Name)\b|\br\.Group(Location|Name)\b`)

// eventOutputFiles build event and notice output for viewers who may not
// see the event's group.
var eventOutputFiles = []string{"api_events.go", "api_event_access.go", "api_notifications.go", "api_group_visibility.go", "api_guest.go", "api_registrations.go"}

func groupDetailViolations(src string) []string {
	var bad []string
	for i, line := range strings.Split(src, "\n") {
		if groupDetailRe.MatchString(line) && !strings.Contains(line, "// group-ok:") {
			bad = append(bad, strings.TrimSpace(line)+" (line "+itoa(uint(i+1))+")")
		}
	}
	return bad
}

func TestGroupDetailsGoThroughGroupShownTo(t *testing.T) {
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

// The SPA's types in web/src/types.ts are written by hand. A Go JSON rename
// that misses them broke the web tests after M1.1a (4e7111b). Each pair
// must name exactly the same fields.
var tsContracts = []struct {
	goType any
	tsName string
}{
	{userJSON{}, "User"},
	{groupSummaryJSON{}, "GroupSummary"},
	{memberJSON{}, "Member"},
	{notificationJSON{}, "Notification"},
	{resolutionJSON{}, "NotificationResolution"},
	{occurrenceJSON{}, "EventOccurrence"},
	{eventJSON{}, "EventSeries"},
	{hostJSON{}, "EventHostRow"},
	{impactEventJSON{}, "VisibilityImpactEvent"},
	{visibilityImpactJSON{}, "VisibilityImpact"},
	{invitationJSON{}, "Invitation"},
	{accountRequestJSON{}, "AccountRequest"},
	{guestRegistrationJSON{}, "GuestRegistration"},
	{attendeeJSON{}, "Attendee"},
	{registrationJSON{}, "RegistrationResponse"},
}

var tsFieldRe = regexp.MustCompile(`^\s+(\w+)\??:`)

func tsFields(src, name string) map[string]bool {
	out := map[string]bool{}
	in := false
	for _, line := range strings.Split(src, "\n") {
		switch {
		case strings.HasPrefix(line, "export interface "+name+" "):
			in = true
		case in && strings.HasPrefix(line, "}"):
			return out
		case in:
			if m := tsFieldRe.FindStringSubmatch(line); m != nil {
				out[m[1]] = true
			}
		}
	}
	return out
}

func goFields(v any) map[string]bool {
	out := map[string]bool{}
	t := reflect.TypeOf(v)
	for i := 0; i < t.NumField(); i++ {
		if tag := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]; tag != "" && tag != "-" {
			out[tag] = true
		}
	}
	return out
}

func contractDiff(goSide, tsSide map[string]bool) (missingInTS, extraInTS []string) {
	for k := range goSide {
		if !tsSide[k] {
			missingInTS = append(missingInTS, k)
		}
	}
	for k := range tsSide {
		if !goSide[k] {
			extraInTS = append(extraInTS, k)
		}
	}
	sort.Strings(missingInTS)
	sort.Strings(extraInTS)
	return
}

func TestWebTypesMatchGoJSON(t *testing.T) {
	src, err := os.ReadFile("web/src/types.ts")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range tsContracts {
		ts := tsFields(string(src), c.tsName)
		if len(ts) == 0 {
			t.Errorf("web/src/types.ts: interface %s not found", c.tsName)
			continue
		}
		missing, extra := contractDiff(goFields(c.goType), ts)
		if len(missing)+len(extra) > 0 {
			t.Errorf("%T vs web/src/types.ts %s: add %v to the TS interface, remove or rename %v",
				c.goType, c.tsName, missing, extra)
		}
	}
}

// The contract check catches the M1.1a rename (nextMeetingAt -> nextEventAt).
func TestWebTypesGuardCatchesFieldRename(t *testing.T) {
	old := "export interface GroupSummary {\n  nextMeetingAt: string | null;\n}\n"
	missing, extra := contractDiff(map[string]bool{"nextEventAt": true}, tsFields(old, "GroupSummary"))
	if len(missing) != 1 || len(extra) != 1 {
		t.Fatalf("missing %v extra %v", missing, extra)
	}
}

func TestDependabotCoversEveryEcosystem(t *testing.T) {
	b, err := os.ReadFile(".github/dependabot.yml")
	if err != nil {
		t.Fatal(err)
	}
	for _, eco := range []string{"gomod", "npm", "github-actions", "docker"} {
		if !strings.Contains(string(b), "package-ecosystem: "+eco) {
			t.Errorf("dependabot.yml has no %s entry", eco)
		}
	}
	ci, err := os.ReadFile(".github/workflows/test.yml")
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"govulncheck@v", "npm audit --omit=dev --audit-level=high", "SCREENS_DIR="} {
		if !strings.Contains(string(ci), step) {
			t.Errorf("CI has no %q step", step)
		}
	}
}

// testLayoutAllowed are the only test files with no same-named source file:
// shared helpers, these repo-wide guards, and the migration drift check.
var testLayoutAllowed = map[string]bool{
	"testhelpers_test.go":      true,
	"guards_test.go":           true,
	"migrations/drift_test.go": true,
}

// milestoneNameRe matches a test file named after when it was written
// (m11b_test.go, m03b_limit_test.go, milestone-2.spec.ts) instead of what it tests.
var milestoneNameRe = regexp.MustCompile(`(?i)(^|[_.-])m\d+[a-z]?([_.-]|$)|milestone`)

// testLayoutViolations checks one repo-relative test file path against the
// layout rule; exists reports whether a repo-relative path exists.
func testLayoutViolations(path string, exists func(string) bool) []string {
	dir, base := filepath.Split(path)
	var out []string
	if milestoneNameRe.MatchString(base) {
		out = append(out, path+": named after a milestone. Move its tests into the file for the code they test (X_test.go next to X.go, Foo.test.tsx next to Foo.tsx, e2e specs by feature); shared helpers go in testhelpers_test.go")
		return out
	}
	if testLayoutAllowed[path] || strings.HasPrefix(path, "web/e2e/") {
		return out
	}
	var sources []string
	switch {
	case strings.HasSuffix(base, "_test.go"):
		sources = []string{strings.TrimSuffix(base, "_test.go") + ".go"}
	case strings.Contains(base, ".test."):
		stem := base[:strings.Index(base, ".test.")]
		sources = []string{stem + ".ts", stem + ".tsx"}
	default:
		return out
	}
	for _, s := range sources {
		if exists(dir + s) {
			return out
		}
	}
	out = append(out, path+": no matching source file ("+strings.Join(sources, " or ")+" in the same directory). Move these tests into the _test file of the code they test; put shared helpers in testhelpers_test.go; only add to testLayoutAllowed in guards_test.go for helpers, integration or migration tests")
	return out
}

func TestTestFilesSitNextToTheirSource(t *testing.T) {
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	var bad []string
	err := filepath.WalkDir(".", func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "dist", "test-results", "playwright-report", "data":
				return filepath.SkipDir
			}
			return nil
		}
		n := d.Name()
		if strings.HasSuffix(n, "_test.go") || strings.Contains(n, ".test.") || strings.HasSuffix(n, ".spec.ts") {
			bad = append(bad, testLayoutViolations(filepath.ToSlash(p), exists)...)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) > 0 {
		t.Fatalf("test layout (AGENTS.md, Tests):\n%s", strings.Join(bad, "\n"))
	}
}

// The layout check catches the files this rule replaced.
func TestTestLayoutGuardCatchesMilestoneFiles(t *testing.T) {
	none := func(string) bool { return false }
	for _, p := range []string{"m11b_test.go", "m03b_limit_test.go", "web/e2e/m12.spec.ts", "milestone_test.go", "tz_guard_test.go", "web/src/Orphan.test.tsx"} {
		if len(testLayoutViolations(p, none)) == 0 {
			t.Errorf("%s passed the layout check", p)
		}
	}
	if v := testLayoutViolations("api_events_test.go", func(p string) bool { return p == "api_events.go" }); len(v) != 0 {
		t.Errorf("api_events_test.go flagged: %v", v)
	}
	if v := testLayoutViolations("web/e2e/events.spec.ts", none); len(v) != 0 {
		t.Errorf("events.spec.ts flagged: %v", v)
	}
}

// DEC-52: rate limits and account checks never reach a user on a public
// form; it answers as usual and logs. Only sign-in (an actionable,
// familiar lockout) and a signed-in leader's invitation limit answer 429.
func TestOnlyLoginAndInvitesAnswer429(t *testing.T) {
	allowed := map[string]bool{"api_auth.go": true, "api_invitations.go": true}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || allowed[f] {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(src), "StatusTooManyRequests") {
			t.Errorf("%s answers 429; public forms answer as usual and log instead (DEC-52)", f)
		}
	}
}

// Colors come from the theme tokens in web/src/index.css (UI makeover PR 3),
// so a token change reaches every screen. A hex color anywhere else, or a
// class from Tailwind's own palette (bg-red-600; it builds nothing now that
// the theme switches that palette off), fails. A line that must keep a hex
// says why with "color-ok:".
var (
	hexColorRe     = regexp.MustCompile(`#(?:[0-9a-fA-F]{8}|[0-9a-fA-F]{6}|[0-9a-fA-F]{3,4})\b`)
	paletteClassRe = regexp.MustCompile(`\b(?:bg|text|border|ring|ring-offset|divide|outline|from|via|to|fill|stroke|accent|caret|decoration|placeholder|shadow)-(?:slate|gray|zinc|stone|red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose|brand|neutral|pending|accent)(?:-\d{2,3})?\b|\b(?:bg|text|border|ring|divide|outline|from|via|to|fill|stroke|accent|decoration)-(?:primary|danger|success|warning|info)-\d{2,3}\b`)
	// colorScanDirs hold everything that styles a page.
	colorScanDirs = []string{"web/src", "templates"}
	// themeFile is the one place hex values live, inside its @theme block.
	themeFile = "web/src/index.css"
)

// colorViolations checks one file. Inside the theme file, hex values are
// allowed between "@theme {" and its closing "}".
func colorViolations(path, src string) []string {
	var bad []string
	inTheme := false
	for i, line := range strings.Split(src, "\n") {
		if path == themeFile {
			if strings.HasPrefix(line, "@theme") {
				inTheme = true
			} else if inTheme && strings.HasPrefix(line, "}") {
				inTheme = false
				continue
			}
		}
		if strings.Contains(line, "color-ok:") {
			continue
		}
		where := path + ":" + itoa(uint(i+1)) + ": "
		if !inTheme {
			for _, h := range hexColorRe.FindAllString(line, -1) {
				bad = append(bad, where+h+" is a hard-coded color. Use a theme token (bg-primary, text-danger) or add one to @theme in "+themeFile)
			}
		}
		for _, c := range paletteClassRe.FindAllString(line, -1) {
			bad = append(bad, where+c+" is a palette color or scale step, which the theme does not build. Use a role from TOKENS.md: primary, page, surface, sunken, border, text, text-muted, danger, success, warning, info (with -hover, -subtle, -on-subtle, -border, -foreground)")
		}
	}
	return bad
}

func TestColorsComeFromThemeTokens(t *testing.T) {
	var bad []string
	for _, dir := range colorScanDirs {
		err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			switch filepath.Ext(p) {
			case ".ts", ".tsx", ".css", ".html":
			default:
				return nil
			}
			if strings.Contains(p, ".test.") {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			bad = append(bad, colorViolations(filepath.ToSlash(p), string(b))...)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(bad) > 0 {
		t.Fatalf("colors outside the theme (AGENTS.md, Styling):\n%s", strings.Join(bad, "\n"))
	}
}

// The color check catches what this rule replaced: the old static/style.css
// red, a Tailwind palette class, and a hex outside @theme in index.css.
func TestColorGuardCatchesHardCodedColors(t *testing.T) {
	cases := map[string]string{
		"templates/setup.html":        `<style>header { background-color: #e51937; }</style>`,
		"web/src/pages/Admin.tsx":     `<button className="btn-secondary text-red-600!">`,
		"web/src/components/Card.tsx": `<div className="bg-brand-dark">`,
		"web/src/index.css":           "@theme {\n  --color-primary: #f64060;\n}\n.btn { color: #fff; }",
	}
	for path, src := range cases {
		if len(colorViolations(path, src)) == 0 {
			t.Errorf("%s: %q passed the color check", path, src)
		}
	}
	ok := map[string]string{
		"web/src/index.css":       "@theme {\n  --color-primary: #f64060;\n}",
		"web/src/pages/Admin.tsx": `<span className="bg-danger-subtle text-text-muted">PR #41</span>`,
		"web/src/x.ts":            `const c = "#7c3aed"; // color-ok: placeholder cover`,
	}
	for path, src := range ok {
		if v := colorViolations(path, src); len(v) != 0 {
			t.Errorf("%s flagged: %v", path, v)
		}
	}
}
