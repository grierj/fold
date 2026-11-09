package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

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
// shared helpers and migration tests (the repo-wide guards match guardFileRe).
var testLayoutAllowed = map[string]bool{
	"testhelpers_test.go":              true,
	"migrations/drift_test.go":         true,
	"migrations/reason_review_test.go": true,
	"migrations/testhelpers_test.go":   true,
	// The erasure inventory's data and its check against the live schema and
	// docs/plan/M7.4-ERASURE-INVENTORY.md section 4 (no production code).
	"erasure_inventory_test.go": true,
}

// guardFileRe matches the repo-wide guard files in the root package: guards_<concern>_test.go.
var guardFileRe = regexp.MustCompile(`^guards_[a-z_]+_test\.go$`)

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
	if testLayoutAllowed[path] || (dir == "" && guardFileRe.MatchString(base)) || strings.HasPrefix(path, "web/e2e/") {
		return out
	}
	var sources []string
	switch {
	case strings.HasSuffix(base, "_test.go"):
		sources = []string{strings.TrimSuffix(base, "_test.go") + ".go"}
	case strings.Contains(base, ".test."):
		stem := base[:strings.Index(base, ".test.")]
		switch ext := filepath.Ext(base); ext {
		case ".js", ".mjs", ".cjs":
			sources = []string{stem + ext}
		default:
			sources = []string{stem + ".ts", stem + ".tsx"}
		}
	default:
		return out
	}
	for _, s := range sources {
		if exists(dir + s) {
			return out
		}
	}
	out = append(out, path+": no matching source file ("+strings.Join(sources, " or ")+" in the same directory). Move these tests into the _test file of the code they test; put shared helpers in testhelpers_test.go; only add to testLayoutAllowed in guards_repo_test.go for helpers, integration or migration tests (repo-wide guards go in guards_<concern>_test.go)")
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
	for _, p := range []string{"m11b_test.go", "m03b_limit_test.go", "web/e2e/m12.spec.ts", "milestone_test.go", "tz_guard_test.go", "web/src/Orphan.test.tsx", "web/scripts/orphan.test.mjs"} {
		if len(testLayoutViolations(p, none)) == 0 {
			t.Errorf("%s passed the layout check", p)
		}
	}
	if v := testLayoutViolations("api_events_test.go", func(p string) bool { return p == "api_events.go" }); len(v) != 0 {
		t.Errorf("api_events_test.go flagged: %v", v)
	}
	if v := testLayoutViolations("web/scripts/bundle-size.test.mjs", func(p string) bool { return p == "web/scripts/bundle-size.mjs" }); len(v) != 0 {
		t.Errorf("bundle-size.test.mjs flagged: %v", v)
	}
	if v := testLayoutViolations("web/e2e/events.spec.ts", none); len(v) != 0 {
		t.Errorf("events.spec.ts flagged: %v", v)
	}
}

// --- Python (AGENTS "No Python") ---

var pythonRe = regexp.MustCompile(`(?m)(^|[\s;|&(` + "`" + `$])python[23]?(\s|$)`)

func pythonViolations(path, src string) []string {
	if strings.HasSuffix(path, ".py") {
		return []string{path + ": Python file; Fold tooling is shell or Go only. Rewrite it as a shell script or a Go program"}
	}
	var out []string
	for i, line := range strings.Split(src, "\n") {
		if pythonRe.MatchString(line) {
			out = append(out, path+":"+strconv.Itoa(i+1)+": runs Python; use shell (awk, sed, a free port via the Go binary) or Go instead")
		}
	}
	return out
}

// scriptFiles are the tracked files that run commands.
func scriptFiles(t *testing.T) []string {
	var files []string
	err := filepath.WalkDir(".", func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (p == "web/node_modules" || p == ".git" || p == "web/dist" || p == "data") {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		base := filepath.Base(p)
		ext := filepath.Ext(p)
		if ext == ".sh" || ext == ".py" || ext == ".yml" || ext == ".yaml" || ext == ".mjs" || base == "Makefile" || base == "Dockerfile" ||
			(strings.HasPrefix(p, ".cursor/skills/") && ext != ".md") {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestNoPython(t *testing.T) {
	for _, p := range scriptFiles(t) {
		src, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range pythonViolations(p, string(src)) {
			t.Error(v)
		}
	}
}

func TestPythonGuardCatchesVerifyFoldPython(t *testing.T) {
	// Lines from .cursor/skills/verify-fold/verify-fold before b6c132e.
	old := "  python3 - \"$1\" \"$2\" <<'PY'\n  port=\"$(python3 -c 'import socket')\"\n# No Python: shell and Go only.\n"
	if got := pythonViolations("verify-fold", old); len(got) != 2 {
		t.Fatalf("want 2 violations, got %v", got)
	}
	if got := pythonViolations("tool.py", ""); len(got) != 1 {
		t.Fatalf(".py file not caught: %v", got)
	}
}

// --- "Recommended", not "supported" (Grier) ---

var supportedRe = regexp.MustCompile(`(?i)\b(supported|unsupported|supports)\b`)

func supportedViolations(path, src string) []string {
	var out []string
	for i, line := range strings.Split(src, "\n") {
		if strings.Contains(line, `"supported"`) || strings.Contains(line, "wording-ok:") {
			continue
		}
		if supportedRe.MatchString(line) {
			out = append(out, path+":"+strconv.Itoa(i+1)+`: say "recommended" (or "tested", "works with") instead of "supported"; Fold makes no support promise`)
		}
	}
	return out
}

func TestDocsSayRecommendedNotSupported(t *testing.T) {
	err := filepath.WalkDir(".", func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (p == "web/node_modules" || p == ".git" || p == "docs/plan" || p == "data") {
			return filepath.SkipDir
		}
		if d.IsDir() || filepath.Ext(p) != ".md" {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, v := range supportedViolations(p, string(src)) {
			t.Error(v)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSupportedGuardCatches98f1a9a(t *testing.T) {
	old := "A single DigitalOcean droplet is the supported production install.\n`ubuntu-24-04-x64` remains a supported choice.\n"
	if got := supportedViolations("docs/DEPLOY-DIGITALOCEAN.md", old); len(got) != 2 {
		t.Fatalf("want 2, got %v", got)
	}
}

// --- One Node version (.nvmrc): CI, the Dockerfile and the docs follow it
// instead of keeping their own copy ---

var dockerNodeRe = regexp.MustCompile(`(?m)^FROM node:(\d+)`)
var docNodeRe = regexp.MustCompile(`Node (\d+)( LTS)?\b`)

func nodeVersionViolations(nvmrc string, files map[string]string) []string {
	want := strings.TrimSpace(nvmrc)
	var out []string
	for p, src := range files {
		switch {
		case p == "Dockerfile":
			m := dockerNodeRe.FindStringSubmatch(src)
			if m == nil || m[1] != want {
				out = append(out, p+": FROM node:"+want+"-alpine (the major in .nvmrc)")
			}
		case strings.HasSuffix(p, ".yml"):
			if strings.Contains(src, "node-version:") {
				out = append(out, p+": use node-version-file: .nvmrc, not node-version")
			}
		default:
			for _, m := range docNodeRe.FindAllStringSubmatch(src, -1) {
				if m[2] != "" && m[1] != want {
					out = append(out, p+": says Node "+m[1]+" LTS; .nvmrc says "+want)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

func TestNodeVersionComesFromNvmrc(t *testing.T) {
	nvmrc, err := os.ReadFile(".nvmrc")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, p := range []string{"Dockerfile", ".github/workflows/test.yml", "README.md", "AGENTS.md"} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		files[p] = string(b)
	}
	for _, v := range nodeVersionViolations(string(nvmrc), files) {
		t.Error(v)
	}
}

func TestNodeGuardCatchesA21cc93Drift(t *testing.T) {
	// 8c676ac^: CI pinned node-version "22"; a half-done bump leaves the
	// Dockerfile or docs behind.
	got := nodeVersionViolations("24\n", map[string]string{
		"Dockerfile":                 "FROM node:22-alpine AS webbuild\n",
		".github/workflows/test.yml": "          node-version: \"22\"\n",
		"README.md":                  "Recommended: Go 1.26+, Node 22 LTS, and Docker\n",
	})
	if len(got) != 3 {
		t.Fatalf("want 3, got %v", got)
	}
}

// #95: CI actions are pinned to full commit SHAs (a moved tag can't run new
// code), and the workflow token is read-only.
func TestWorkflowActionsPinnedAndReadOnly(t *testing.T) {
	files, err := filepath.Glob(".github/workflows/*.yml")
	if err != nil || len(files) == 0 {
		t.Fatalf("workflows: %v %v", files, err)
	}
	pinned := regexp.MustCompile(`^\s*(?:-\s*)?uses:\s*[^@\s]+@[0-9a-f]{40}(?:\s+#\s*\S+)?\s*$`)
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !regexp.MustCompile(`(?m)^permissions:\s*\n\s+contents:\s*read\s*$`).Match(src) {
			t.Errorf("%s: no top-level `permissions: contents: read`", f)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if strings.Contains(line, "uses:") && !strings.Contains(line, "uses: ./") && !pinned.MatchString(line) {
				t.Errorf("%s:%d: action not pinned to a full SHA: %s", f, i+1, strings.TrimSpace(line))
			}
		}
	}
}
