package main

import (
	"go/ast"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"fold/internal/guardkit"
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
		t.Fatalf("test layout (AGENTS.md, Development):\n%s", strings.Join(bad, "\n"))
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

// --- Python (AGENTS "No Python in the serving stack", DEC-151) ---

var (
	// pythonRe finds the interpreter as a command word: bare, quoted (`ENTRYPOINT ["python3"]`,
	// a Go literal), inside a JSON-style list, or behind a path (`/usr/bin/python3`, a shebang).
	pythonRe = regexp.MustCompile(`(?m)(^|[\s;|&(/=,\[` + "`" + `$"'])python[23]?(\.\d+)*(\s|$|["'\],;)])`)
	// pythonImageRe finds a Python base or service image: a Dockerfile FROM (with flags such as
	// --platform) or a compose `image:` key.
	pythonImageRe = regexp.MustCompile(`(?im)^\s*(-\s*)?(FROM\s+(--\S+\s+)*|image:\s*["']?)(\S+/)?python([:@\s"']|$)`)
)

// servingPath reports whether a repo path ships in, or runs as part of, what a church
// deploys. Developer tooling (scripts/, web/scripts/, web/e2e/, .cursor/, .github/, the
// Makefile) is outside it and may use Python.
func servingPath(p string) bool {
	if !strings.Contains(p, "/") {
		return strings.HasPrefix(p, "Dockerfile") || p == "Caddyfile" || isComposeFile(p)
	}
	for _, dir := range []string{"deploy/", "templates/", "migrations/", "web/src/", "web/public/"} {
		if strings.HasPrefix(p, dir) {
			return true
		}
	}
	return p == "web/index.html"
}

// isComposeFile matches the names Compose reads at the repo root: docker-compose.* and
// compose.*, with their override variants.
func isComposeFile(p string) bool {
	ext := path.Ext(p)
	return (strings.HasPrefix(p, "docker-compose") || strings.HasPrefix(p, "compose.")) && (ext == ".yml" || ext == ".yaml")
}

// pythonViolations checks one file. A .py file anywhere in the serving stack fails; the
// interpreter named in the text of a Dockerfile, a compose file or deploy/ fails too.
// templates/, migrations/ and the frontend are checked for .py files only: their text is
// prose and SQL.
func pythonViolations(p, src string) []string {
	if !servingPath(p) {
		return nil
	}
	if strings.HasSuffix(p, ".py") {
		return []string{p + ": Python file in the serving stack; Python never ships or runs in what a church deploys (DEC-151). Rewrite it as shell or Go"}
	}
	if !strings.HasPrefix(p, "deploy/") && !strings.HasPrefix(path.Base(p), "Dockerfile") && !isComposeFile(path.Base(p)) {
		return nil
	}
	var out []string
	for i, line := range strings.Split(src, "\n") {
		if pythonRe.MatchString(line) || pythonImageRe.MatchString(line) {
			out = append(out, p+":"+strconv.Itoa(i+1)+": runs or ships Python in the serving stack (DEC-151); use shell or Go instead")
		}
	}
	return out
}

// goPythonViolations flags a string literal in production Go source that names the
// interpreter, which is how an exec.Command("python3", ...) would start it. The check is
// parse-only: a command built from pieces escapes it.
func goPythonViolations(sources guardkit.Sources) []string {
	parsed := sources.Parse(0)
	out := append([]string(nil), parsed.Problems...)
	for _, f := range parsed.Files {
		ast.Inspect(f.AST, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if s, err := strconv.Unquote(lit.Value); err == nil && pythonRe.MatchString(s) {
				out = append(out, f.Path+":"+strconv.Itoa(parsed.Fset.Position(lit.Pos()).Line)+": production Go names a Python interpreter (DEC-151)")
			}
			return true
		})
	}
	return out
}

// servingFiles are the files in the working tree that belong to the serving stack.
func servingFiles(t *testing.T) []string {
	var files []string
	err := filepath.WalkDir(".", func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); n == ".git" || n == "node_modules" || n == "dist" || p == "data" {
				return filepath.SkipDir
			}
			return nil
		}
		if p = filepath.ToSlash(p); servingPath(p) {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestNoPythonInServingStack(t *testing.T) {
	for _, p := range servingFiles(t) {
		src, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range pythonViolations(p, string(src)) {
			t.Error(v)
		}
	}
	for _, v := range goPythonViolations(guardkit.Production(t)) {
		t.Error(v)
	}
}

func TestPythonGuardScopesToServingStack(t *testing.T) {
	// Python in the verify-fold skill script is tooling, so allowed.
	oldVerify := "  python3 - \"$1\" \"$2\" <<'PY'\n  port=\"$(python3 -c 'import socket')\"\n"
	for _, c := range []struct {
		name, path, src string
		want            int
	}{
		{"Dockerfile runs python", "Dockerfile", "RUN python3 build.py\n", 1},
		{"Dockerfile base image", "Dockerfile", "FROM python:3.12-slim\n", 1},
		{"Dockerfile base image with registry", "Dockerfile", "FROM docker.io/library/python:3.12\n", 1},
		{"Dockerfile go base", "Dockerfile", "FROM golang:1.27-bookworm AS build\nFROM gcr.io/distroless/static-debian12:nonroot\n", 0},
		{"Dockerfile base image with platform", "Dockerfile", "FROM --platform=$BUILDPLATFORM python:3.12 AS build\n", 1},
		{"Dockerfile base image digest", "Dockerfile.prod", "FROM python@sha256:abc\n", 1},
		{"Dockerfile quoted entrypoint", "Dockerfile", "ENTRYPOINT [\"python3\"]\n", 1},
		{"Dockerfile quoted entrypoint with args", "Dockerfile", "ENTRYPOINT [\"python3\", \"app.py\"]\n", 1},
		{"Dockerfile absolute interpreter", "Dockerfile", "CMD [\"/usr/bin/python3\", \"app.py\"]\n", 1},
		{"Dockerfile versioned interpreter", "Dockerfile", "RUN python3.12 build.py\n", 1},
		{"deploy python file", "deploy/x.py", "", 1},
		{"deploy shebang absolute", "deploy/a.sh", "#!/usr/bin/python3\n", 1},
		{"deploy shebang env", "deploy/a.sh", "#!/usr/bin/env python3\n", 1},
		{"deploy shell prose", "deploy/a.sh", "# no pythonic tooling here\n", 0},
		{"compose quoted command", "docker-compose.yml", "    command: [\"python3\", \"app.py\"]\n", 1},
		{"compose bare list command", "docker-compose.yml", "    command: [python3, app.py]\n", 1},
		{"compose image", "docker-compose.yml", "    image: python:3.12\n", 1},
		{"compose quoted registry image", "docker-compose.yaml", "    image: \"docker.io/library/python:3.12\"\n", 1},
		{"compose list image", "docker-compose.yml", "  - image: python\n", 1},
		{"compose other image", "docker-compose.yml", "    image: postgres:17\n    command: [\"postgres\"]\n", 0},
		{"compose.yaml command", "compose.yaml", "    command: python3 app.py\n", 1},
		{"compose.yml image", "compose.yml", "    image: python:3\n", 1},
		{"compose override", "compose.override.yaml", "    entrypoint: [\"python3\"]\n", 1},
		{"compose.yaml python file prose", "compose.yaml", "    # pythonic\n", 0},
		{"deploy script runs python", "deploy/digitalocean/a.sh", "python3 -c 'print(1)'\n", 1},
		{"compose command", "docker-compose.yml", "    command: python3 app.py\n", 1},
		{"web source python file", "web/src/x.py", "", 1},
		{"templates python file", "templates/x.py", "", 1},
		{"tooling python file", "scripts/tool.py", "print(1)\n", 0},
		{"web tooling python file", "web/scripts/tool.py", "", 0},
		{"verify skill old lines", ".cursor/skills/verify-fold/verify-fold", oldVerify, 0},
		{"e2e server script", "web/e2e/server.sh", "port=\"$(python3 -c 'x')\"\n", 0},
		{"Makefile", "Makefile", "\tpython3 tool.py\n", 0},
		{"workflow", ".github/workflows/test.yml", "      - run: python3 x.py\n", 0},
		{"migration prose", "migrations/00001_baseline.sql", "-- python is not needed\n", 0},
	} {
		if got := pythonViolations(c.path, c.src); len(got) != c.want {
			t.Errorf("%s: want %d violations, got %v", c.name, c.want, got)
		}
	}

	for _, p := range []string{"compose.yaml", "compose.yml", "compose.override.yml", "docker-compose.yaml", "docker-compose.prod.yml", "Dockerfile.dev"} {
		if !servingPath(p) {
			t.Errorf("%s is not in the serving stack", p)
		}
	}
	for _, p := range []string{"compose.md", "compose.go", "composer.json", "web/compose.yaml"} {
		if servingPath(p) {
			t.Errorf("%s is in the serving stack", p)
		}
	}

	abs := guardkit.Sources{"api.go": "package main\n\nimport \"os/exec\"\n\nfunc f() { _ = exec.Command(\"/usr/bin/python3\", \"x.py\") }\nvar shebang = \"#!/usr/bin/python3\\n\"\n"}
	if got := goPythonViolations(abs); len(got) != 2 {
		t.Errorf("Go absolute interpreter and shebang: want 2 violations, got %v", got)
	}
	exec := guardkit.Sources{"api.go": "package main\n\nimport \"os/exec\"\n\nfunc f() { _ = exec.Command(\"python3\", \"x.py\") }\n"}
	if got := goPythonViolations(exec); len(got) != 1 {
		t.Errorf("Go exec of python3: want 1 violation, got %v", got)
	}
	prose := guardkit.Sources{"api.go": "package main\n\nvar msg = \"No Python here\"\nvar other = \"pythonic\"\n"}
	if got := goPythonViolations(prose); len(got) != 0 {
		t.Errorf("Go prose flagged: %v", got)
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
