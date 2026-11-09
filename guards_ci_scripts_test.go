package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// --- Contract migration gate (PLAN 14.1, DEC-138): CI must run
// scripts/contract-gate.sh on a full checkout, before anything else can hide
// a failure ---

var contractGateKeyRe = regexp.MustCompile(`^(["']?[A-Za-z0-9_-]+["']?)[ \t]*:[ \t]*(.*)$`)

func contractGateKey(line string) (string, string, bool) {
	m := contractGateKeyRe.FindStringSubmatch(line)
	if m == nil {
		return "", "", false
	}
	return strings.Trim(m[1], `"'`), contractGateValue(m[2]), true
}

type contractGateStep struct {
	keys map[string]string // step-level keys (the first line's "- " removed)
	with map[string]string // keys one level under "with:"
	env  map[string]string // keys one level under "env:"
}

// contractGateValue drops a trailing " # comment" from a YAML scalar.
func contractGateValue(v string) string {
	if i := strings.Index(v, " #"); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}

// The two gate steps the test job must run, exactly as written.
const (
	contractGateRun = "bash scripts/contract-gate.sh"
	mergeRulesRun   = "bash scripts/merge-rules-check.sh"
)

// testJobSteps reads the steps of a workflow's test job with plain lines (no
// YAML dependency). It reports the shapes it cannot read, a conditional or
// failure-tolerant test job, and a multiline continuation after a gate step's
// run or after any `shell: bash`. ok is false when there is no test job to
// read at all.
func testJobSteps(workflow string) (steps []*contractGateStep, problems []string, ok bool) {
	if strings.Contains(workflow, "\r") {
		return nil, []string{"unsupported carriage return in workflow"}, false
	}
	lines := strings.Split(workflow, "\n")
	start := -1
	inJobs := false
	for i, l := range lines {
		if l == "jobs:" {
			inJobs = true
			continue
		}
		if inJobs && l != "" && l[0] != ' ' && l[0] != '#' {
			break
		}
		if inJobs && strings.TrimRight(l, " ") == "  test:" {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return nil, []string{"no test job under jobs:"}, false
	}
	var block []string
	for _, l := range lines[start:] {
		trim := strings.TrimSpace(l)
		indent := len(l) - len(strings.TrimLeft(l, " "))
		if (trim != "" && !strings.HasPrefix(trim, "#") && indent == 2) || (l != "" && l[0] != ' ' && l[0] != '#') {
			break
		}
		block = append(block, l)
	}

	var cur *contractGateStep
	var nested map[string]string // the step's with: or env: map being read
	inSteps, inProtectedScalar := false, false
	for _, l := range block {
		trim := strings.TrimSpace(l)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		indent := len(l) - len(strings.TrimLeft(l, " "))
		if indent == 4 {
			inSteps = trim == "steps:"
			cur, nested, inProtectedScalar = nil, nil, false
			if key, _, ok := contractGateKey(trim); !ok {
				problems = append(problems, "unsupported test job mapping key: "+trim)
			} else if key == "if" || key == "continue-on-error" {
				problems = append(problems, "the test job must not be conditional or allowed to fail ("+key+":)")
			}
			continue
		}
		if !inSteps {
			continue
		}
		switch {
		case indent == 6 && strings.HasPrefix(trim, "- "):
			if strings.HasPrefix(trim, "-  ") || strings.HasPrefix(trim, "- \t") {
				problems = append(problems, "unsupported spacing after test step dash")
			}
			cur = &contractGateStep{keys: map[string]string{}, with: map[string]string{}, env: map[string]string{}}
			steps = append(steps, cur)
			nested, inProtectedScalar = nil, false
			trim = strings.TrimSpace(trim[2:])
			fallthrough
		case indent == 8 || indent == 6:
			if cur == nil {
				continue
			}
			nested, inProtectedScalar = nil, false
			if key, value, ok := contractGateKey(trim); ok {
				cur.keys[key] = value
				switch key {
				case "with":
					nested = cur.with
				case "env":
					nested = cur.env
				}
				inProtectedScalar = (key == "run" && (value == contractGateRun || value == mergeRulesRun)) || (key == "shell" && value == "bash")
			} else {
				problems = append(problems, "unsupported test step mapping key: "+trim)
			}
		case indent > 8 && inProtectedScalar:
			problems = append(problems, "a gate step's run or shell must not have a multiline continuation")
		case indent >= 10 && nested != nil:
			if key, value, ok := contractGateKey(trim); ok {
				nested[key] = value
			}
		}
	}
	return steps, problems, true
}

// gateStepIndex finds the first step whose run is exactly run, and says what
// would let it be skipped or pass without running: no such step, a shell
// other than plain bash, an if: or continue-on-error:, or a place before the
// checkout. label names the step in the problems.
func gateStepIndex(steps []*contractGateStep, run, label string) (int, []string) {
	for i, s := range steps {
		if s.keys["run"] != run {
			continue
		}
		var problems []string
		if s.keys["shell"] != "bash" {
			problems = append(problems, "the "+label+" step must set shell: bash")
		}
		for _, k := range []string{"if", "continue-on-error"} {
			if _, ok := s.keys[k]; ok {
				problems = append(problems, "the "+label+" step must not have "+k+":")
			}
		}
		if i == 0 {
			problems = append(problems, "the "+label+" step must come after the checkout step")
		}
		return i, problems
	}
	return -1, []string{"the test job has no step whose run is exactly `" + run + "`"}
}

// contractGateWorkflowProblems says what keeps the contract gate from
// running in the test job: the first step must be a checkout with
// fetch-depth 0, a later step must run exactly `bash scripts/contract-gate.sh`
// with shell: bash and no `if:` or `continue-on-error:`, and it must come
// before `make test`.
func contractGateWorkflowProblems(workflow string) []string {
	steps, problems, ok := testJobSteps(workflow)
	if !ok {
		return problems
	}
	if len(steps) == 0 || !strings.HasPrefix(steps[0].keys["uses"], "actions/checkout@") {
		problems = append(problems, "the first step of the test job must be actions/checkout")
	} else if steps[0].with["fetch-depth"] != "0" {
		problems = append(problems, "the checkout must set fetch-depth: 0 under with: (the gate needs history and tags)")
	}
	gate, gateProblems := gateStepIndex(steps, contractGateRun, "contract gate")
	problems = append(problems, gateProblems...)
	if gate >= 0 {
		makeTest := -1
		for i, s := range steps {
			if s.keys["run"] == "make test" {
				makeTest = i
				break
			}
		}
		if makeTest < 0 {
			problems = append(problems, "the test job has no `make test` step to order the gate before")
		} else if gate > makeTest {
			problems = append(problems, "the contract gate step must come before make test")
		}
	}
	return problems
}

// mergeRulesWorkflowProblems says what keeps the merge rules check (DEC-146)
// from running in the test job, a required check: a step after the checkout
// must run exactly `bash scripts/merge-rules-check.sh` with shell: bash, no
// `if:` or `continue-on-error:`, and GH_TOKEN set to the job's token so the
// API's anonymous rate limit cannot fail it.
func mergeRulesWorkflowProblems(workflow string) []string {
	steps, problems, ok := testJobSteps(workflow)
	if !ok {
		return problems
	}
	i, stepProblems := gateStepIndex(steps, mergeRulesRun, "merge rules check")
	problems = append(problems, stepProblems...)
	if i >= 0 && steps[i].env["GH_TOKEN"] != "${{ github.token }}" {
		problems = append(problems, "the merge rules check step must set GH_TOKEN: ${{ github.token }} under env:")
	}
	return problems
}

func TestCIRunsTheContractGate(t *testing.T) {
	b, err := os.ReadFile(".github/workflows/test.yml")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range contractGateWorkflowProblems(string(b)) {
		t.Error(".github/workflows/test.yml: " + p)
	}
}

func TestCIRunsTheMergeRulesCheck(t *testing.T) {
	b, err := os.ReadFile(".github/workflows/test.yml")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range mergeRulesWorkflowProblems(string(b)) {
		t.Error(".github/workflows/test.yml: " + p)
	}
}

const (
	contractGateCheckoutStep = "      - uses: actions/checkout@abc # v1\n        with:\n          fetch-depth: 0\n"
	contractGateStepText     = "      # The gate.\n      - name: Contract migration gate\n        run: bash scripts/contract-gate.sh\n        shell: bash\n"
	contractGateMakeTest     = "      - uses: actions/setup-go@def\n        with:\n          go-version-file: go.mod\n      - run: make test\n"
)

func contractGateWorkflow(testSteps, webSteps string) string {
	return "name: test\non:\n  pull_request:\njobs:\n  test:\n    runs-on: ubuntu-latest\n    env:\n      X: y\n    steps:\n" +
		testSteps +
		"  web:\n    runs-on: ubuntu-latest\n    steps:\n" + webSteps
}

func TestContractGateWorkflowGuardCatchesMistakes(t *testing.T) {
	webCheckout := "      - uses: actions/checkout@abc\n"
	cases := []struct {
		name, workflow, want string
	}{
		{"step removed", contractGateWorkflow(contractGateCheckoutStep+contractGateMakeTest, webCheckout), "no step whose run is exactly"},
		{"step only in the web job", contractGateWorkflow(contractGateCheckoutStep+contractGateMakeTest, webCheckout+contractGateStepText), "no step whose run is exactly"},
		{"if false", contractGateWorkflow(contractGateCheckoutStep+"      - name: Contract migration gate\n        if: ${{ false }}\n        run: bash scripts/contract-gate.sh\n"+contractGateMakeTest, webCheckout), "must not have if:"},
		{"continue-on-error", contractGateWorkflow(contractGateCheckoutStep+"      - name: Contract migration gate\n        continue-on-error: true\n        run: bash scripts/contract-gate.sh\n"+contractGateMakeTest, webCheckout), "must not have continue-on-error:"},
		{"quoted if", contractGateWorkflow(contractGateCheckoutStep+"      - name: gate\n        \"if\": ${{ false }}\n        run: bash scripts/contract-gate.sh\n"+contractGateMakeTest, webCheckout), "must not have if:"},
		{"spaced if", contractGateWorkflow(contractGateCheckoutStep+"      - name: gate\n        if : ${{ false }}\n        run: bash scripts/contract-gate.sh\n"+contractGateMakeTest, webCheckout), "must not have if:"},
		{"quoted continue", contractGateWorkflow(contractGateCheckoutStep+"      - name: gate\n        'continue-on-error': true\n        run: bash scripts/contract-gate.sh\n"+contractGateMakeTest, webCheckout), "must not have continue-on-error:"},
		{"spaced continue", contractGateWorkflow(contractGateCheckoutStep+"      - name: gate\n        continue-on-error : true\n        run: bash scripts/contract-gate.sh\n"+contractGateMakeTest, webCheckout), "must not have continue-on-error:"},
		{"quoted job if", strings.Replace(contractGateWorkflow(contractGateCheckoutStep+contractGateStepText+contractGateMakeTest, webCheckout), "  test:\n", "  test:\n    \"if\": ${{ false }}\n", 1), "test job must not be conditional"},
		{"spaced job continue", strings.Replace(contractGateWorkflow(contractGateCheckoutStep+contractGateStepText+contractGateMakeTest, webCheckout), "  test:\n", "  test:\n    continue-on-error : true\n", 1), "test job must not be conditional"},
		{"escaped step if", contractGateWorkflow(contractGateCheckoutStep+"      - name: gate\n        \"i\\u0066\": ${{ false }}\n        run: bash scripts/contract-gate.sh\n"+contractGateMakeTest, webCheckout), "unsupported test step mapping key"},
		{"escaped step continue", contractGateWorkflow(contractGateCheckoutStep+"      - name: gate\n        \"continue-on-erro\\u0072\": true\n        run: bash scripts/contract-gate.sh\n"+contractGateMakeTest, webCheckout), "unsupported test step mapping key"},
		{"escaped job if", strings.Replace(contractGateWorkflow(contractGateCheckoutStep+contractGateStepText+contractGateMakeTest, webCheckout), "  test:\n", "  test:\n    \"i\\u0066\": ${{ false }}\n", 1), "unsupported test job mapping key"},
		{"escaped job continue", strings.Replace(contractGateWorkflow(contractGateCheckoutStep+contractGateStepText+contractGateMakeTest, webCheckout), "  test:\n", "  test:\n    \"continue-on-erro\\u0072\": true\n", 1), "unsupported test job mapping key"},
		{"fake job in workflow metadata", strings.Replace(strings.Replace(contractGateWorkflow(contractGateCheckoutStep+contractGateMakeTest, webCheckout), "name: test\n", "", 1), "  test:\n", "  \"test\":\n", 1) + "name: |\n  test:\n    steps:\n" + contractGateCheckoutStep + contractGateStepText + contractGateMakeTest, "no test job under jobs:"},
		{"quoted skipped sibling job", strings.Replace(strings.Replace(contractGateWorkflow(contractGateCheckoutStep+"      - run: |\n          make test\n", webCheckout), "  test:\n", "  skip_gate:\n    if: ${{ false }}\n    runs-on: ubuntu-latest\n    steps:\n      - run: \"true\"\n  test:\n", 1), "  web:\n", "  \"gate_decoy\":\n    needs: skip_gate\n    runs-on: ubuntu-latest\n    steps:\n"+contractGateCheckoutStep+contractGateStepText+contractGateMakeTest+"  web:\n", 1), "no step whose run is exactly"},
		{"escaped if with extra dash spacing", contractGateWorkflow(contractGateCheckoutStep+"      -  run: bash scripts/contract-gate.sh\n         \"i\\u0066\": ${{ false }}\n"+contractGateMakeTest, webCheckout), "unsupported spacing after test step dash"},
		{"escaped continue with extra dash spacing", contractGateWorkflow(contractGateCheckoutStep+"      -  run: bash scripts/contract-gate.sh\n         \"continue-on-erro\\u0072\": true\n"+contractGateMakeTest, webCheckout), "unsupported spacing after test step dash"},
		{"or true", contractGateWorkflow(contractGateCheckoutStep+"      - name: Contract migration gate\n        run: bash scripts/contract-gate.sh || true\n"+contractGateMakeTest, webCheckout), "no step whose run is exactly"},
		{"custom shell skips gate", contractGateWorkflow(contractGateCheckoutStep+"      - name: Contract migration gate\n        run: bash scripts/contract-gate.sh\n        shell: bash -c true {0}\n"+contractGateMakeTest, webCheckout), "must set shell: bash"},
		{"no explicit shell", contractGateWorkflow(contractGateCheckoutStep+"      - name: Contract migration gate\n        run: bash scripts/contract-gate.sh\n"+contractGateMakeTest, webCheckout), "must set shell: bash"},
		{"continued or true", contractGateWorkflow(contractGateCheckoutStep+"      - name: Contract migration gate\n        run: bash scripts/contract-gate.sh\n          || true\n"+contractGateMakeTest, webCheckout), "must not have a multiline continuation"},
		{"nine-space continuation", contractGateWorkflow(contractGateCheckoutStep+"      - name: Contract migration gate\n        run: bash scripts/contract-gate.sh\n         || true\n"+contractGateMakeTest, webCheckout), "must not have a multiline continuation"},
		{"continued custom shell", contractGateWorkflow(contractGateCheckoutStep+"      - name: Contract migration gate\n        run: bash scripts/contract-gate.sh\n        shell: bash\n          -c true {0}\n"+contractGateMakeTest, webCheckout), "must not have a multiline continuation"},
		{"carriage return in comment", contractGateWorkflow(contractGateCheckoutStep+"      - name: Contract migration gate\n        run: bash scripts/contract-gate.sh # gate\r        continue-on-error: true\n"+contractGateMakeTest, webCheckout), "unsupported carriage return"},
		{"no fetch-depth", contractGateWorkflow("      - uses: actions/checkout@abc\n"+contractGateStepText+contractGateMakeTest, webCheckout), "fetch-depth: 0"},
		{"fetch-depth 1", contractGateWorkflow("      - uses: actions/checkout@abc\n        with:\n          fetch-depth: 1\n"+contractGateStepText+contractGateMakeTest, webCheckout), "fetch-depth: 0"},
		{"gate before checkout", contractGateWorkflow(contractGateStepText+contractGateCheckoutStep+contractGateMakeTest, webCheckout), "must come after the checkout"},
		{"gate after make test", contractGateWorkflow(contractGateCheckoutStep+contractGateMakeTest+contractGateStepText, webCheckout), "must come before make test"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := contractGateWorkflowProblems(c.workflow)
			for _, p := range got {
				if strings.Contains(p, c.want) {
					return
				}
			}
			t.Fatalf("want a problem containing %q, got %q", c.want, got)
		})
	}
	t.Run("intended snippet passes", func(t *testing.T) {
		w := contractGateWorkflow(contractGateCheckoutStep+contractGateStepText+contractGateMakeTest, webCheckout)
		if got := contractGateWorkflowProblems(w); len(got) != 0 {
			t.Fatalf("want none, got %q", got)
		}
	})
}

// --- Merge rules check (DEC-146): CI must ask GitHub whether master still
// requires up-to-date branches, so two pull requests cannot merge the same
// migration number ---

const mergeRulesStepText = "      - name: Merge rules check\n        run: bash scripts/merge-rules-check.sh\n        shell: bash\n        env:\n          GH_TOKEN: ${{ github.token }}\n"

func TestMergeRulesWorkflowGuardCatchesMistakes(t *testing.T) {
	webCheckout := "      - uses: actions/checkout@abc\n"
	steps := func(merge string) string {
		return contractGateWorkflow(contractGateCheckoutStep+contractGateStepText+merge+contractGateMakeTest, webCheckout)
	}
	cases := []struct {
		name, workflow, want string
	}{
		{"step removed", steps(""), "no step whose run is exactly `bash scripts/merge-rules-check.sh`"},
		{"step only in the web job", contractGateWorkflow(contractGateCheckoutStep+contractGateStepText+contractGateMakeTest, webCheckout+mergeRulesStepText), "no step whose run is exactly `bash scripts/merge-rules-check.sh`"},
		{"or true", steps(strings.Replace(mergeRulesStepText, "check.sh\n", "check.sh || true\n", 1)), "no step whose run is exactly `bash scripts/merge-rules-check.sh`"},
		{"if false", steps(strings.Replace(mergeRulesStepText, "        shell:", "        if: ${{ false }}\n        shell:", 1)), "merge rules check step must not have if:"},
		{"continue-on-error", steps(strings.Replace(mergeRulesStepText, "        shell:", "        continue-on-error: true\n        shell:", 1)), "merge rules check step must not have continue-on-error:"},
		{"no explicit shell", steps(strings.Replace(mergeRulesStepText, "        shell: bash\n", "", 1)), "merge rules check step must set shell: bash"},
		{"continued run", steps(strings.Replace(mergeRulesStepText, "check.sh\n", "check.sh\n          || true\n", 1)), "must not have a multiline continuation"},
		{"no token", steps(strings.Replace(mergeRulesStepText, "        env:\n          GH_TOKEN: ${{ github.token }}\n", "", 1)), "must set GH_TOKEN"},
		{"another token", steps(strings.Replace(mergeRulesStepText, "github.token", "secrets.OTHER", 1)), "must set GH_TOKEN"},
		{"before checkout", contractGateWorkflow(mergeRulesStepText+contractGateCheckoutStep+contractGateStepText+contractGateMakeTest, webCheckout), "merge rules check step must come after the checkout"},
		{"conditional job", strings.Replace(steps(mergeRulesStepText), "  test:\n", "  test:\n    if: ${{ false }}\n", 1), "test job must not be conditional"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mergeRulesWorkflowProblems(c.workflow)
			for _, p := range got {
				if strings.Contains(p, c.want) {
					return
				}
			}
			t.Fatalf("want a problem containing %q, got %q", c.want, got)
		})
	}
	t.Run("intended snippet passes both guards", func(t *testing.T) {
		w := steps(mergeRulesStepText)
		if got := append(mergeRulesWorkflowProblems(w), contractGateWorkflowProblems(w)...); len(got) != 0 {
			t.Fatalf("want none, got %q", got)
		}
	})
}

// mergeRulesFixture is a GET /repos/grierj/fold/rules/branches/master answer
// in the shape GitHub returned on Nov 5, 2026, with strict set as given.
func mergeRulesFixture(strict bool) string {
	return fmt.Sprintf(`[
  {"type": "deletion", "ruleset_source_type": "Repository", "ruleset_source": "grierj/fold", "ruleset_id": 25847073},
  {"type": "non_fast_forward", "ruleset_source_type": "Repository", "ruleset_source": "grierj/fold", "ruleset_id": 25847073},
  {"type": "required_status_checks", "parameters": {"strict_required_status_checks_policy": %t, "do_not_enforce_on_create": true,
    "required_status_checks": [{"context": "test", "integration_id": 15368}, {"context": "web", "integration_id": 15368}, {"context": "provision", "integration_id": 15368}]},
   "ruleset_source_type": "Repository", "ruleset_source": "grierj/fold", "ruleset_id": 25847073},
  {"type": "pull_request", "parameters": {"required_approving_review_count": 0, "allowed_merge_methods": ["merge", "squash", "rebase"]},
   "ruleset_source_type": "Repository", "ruleset_source": "grierj/fold", "ruleset_id": 25847073}
]`, strict)
}

// TestMergeRulesCheckScript runs scripts/merge-rules-check.sh on saved
// answers. It never calls GitHub; CI's step checks the live rules.
func TestMergeRulesCheckScript(t *testing.T) {
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			if os.Getenv("CI") != "" {
				t.Fatalf("%s is required for the merge rules check tests: %v", tool, err)
			}
			t.Skipf("%s is not installed; CI runs this test", tool)
		}
	}
	run := func(t *testing.T, rules string) (int, string) {
		t.Helper()
		f := filepath.Join(t.TempDir(), "rules.json")
		if err := os.WriteFile(f, []byte(rules), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("bash", "scripts/merge-rules-check.sh", "--rules-file", f)
		out, err := cmd.CombinedOutput()
		var exit *exec.ExitError
		switch {
		case err == nil:
			return 0, string(out)
		case errors.As(err, &exit):
			return exit.ExitCode(), string(out)
		}
		t.Fatalf("running the script: %v", err)
		return 0, ""
	}
	strict := mergeRulesFixture(true)
	cases := []struct {
		name, rules string
		code        int
		want        string
	}{
		{"strict rules pass", strict, 0, "master requires pull requests, up-to-date branches"},
		{"the Nov 5 rules fail", mergeRulesFixture(false), 1, "required status checks are not strict"},
		{"no strict field", strings.Replace(strict, `"strict_required_status_checks_policy": true, `, "", 1), 1, "required status checks are not strict"},
		{"strict as a string", strings.Replace(strict, `"strict_required_status_checks_policy": true`, `"strict_required_status_checks_policy": "true"`, 1), 1, "required status checks are not strict"},
		{"no pull request rule", strings.Replace(strict, `"type": "pull_request"`, `"type": "creation"`, 1), 1, "no pull_request rule"},
		{"no non-fast-forward rule", strings.Replace(strict, `"type": "non_fast_forward"`, `"type": "update"`, 1), 1, "no non_fast_forward rule"},
		{"a required check dropped", strings.Replace(strict, `{"context": "web", "integration_id": 15368}, `, "", 1), 1, "required status check web is not required"},
		{"no rules at all", "[]", 1, "required status checks are not strict"},
		{"an error object", `{"message": "Not Found", "status": "404"}`, 1, "not a list of rules"},
		{"not JSON", "<html>rate limited</html>", 1, "not JSON"},
		{"an empty answer", "", 1, "not JSON"},
		{"a blank answer", " \n", 1, "not JSON"},
		// GitHub combines the rules of every active ruleset: strict in one
		// rule makes every PR up to date before the checks of any rule count.
		{"strict in a second ruleset", strings.Replace(mergeRulesFixture(false), "\n]", `,
  {"type": "required_status_checks", "parameters": {"strict_required_status_checks_policy": true, "required_status_checks": []}, "ruleset_id": 2}
]`, 1), 0, "up-to-date branches"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, out := run(t, c.rules)
			if code != c.code || !strings.Contains(out, c.want) {
				t.Fatalf("exit %d, output %q; want exit %d and output containing %q", code, out, c.code, c.want)
			}
		})
	}
	t.Run("bad arguments", func(t *testing.T) {
		out, err := exec.Command("bash", "scripts/merge-rules-check.sh", "--rules", "x").CombinedOutput()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 2 || !strings.Contains(string(out), "usage:") {
			t.Fatalf("got %v %q, want exit 2 with usage", err, out)
		}
	})
}

// contractGateEnv is a git environment that reads nothing from the machine:
// empty global and system config, a throwaway HOME, no inherited GIT_*
// variables (a hook may set GIT_DIR), a fixed identity and a ceiling so a
// directory outside any repository is not found inside one above it.
func contractGateEnv(t *testing.T, ceiling string) []string {
	t.Helper()
	empty := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GIT_") || strings.HasPrefix(kv, "HOME=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"HOME="+t.TempDir(),
		"GIT_CONFIG_GLOBAL="+empty,
		"GIT_CONFIG_SYSTEM="+empty,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Gate Test", "GIT_AUTHOR_EMAIL=gate@example.invalid",
		"GIT_COMMITTER_NAME=Gate Test", "GIT_COMMITTER_EMAIL=gate@example.invalid",
		"GIT_CEILING_DIRECTORIES="+ceiling,
	)
}

type contractGateRepo struct {
	t      *testing.T
	dir    string
	env    []string
	script string
}

// contractGateFixtureFiles selects the real expand history, even when the
// source tree already contains later contract migrations.
func contractGateFixtureFiles(source string) []string {
	return []string{filepath.Join(source, "00001_baseline.sql"), filepath.Join(source, "00002_suppression_address_hash.sql")}
}

func TestContractGateFixtureIgnoresLaterContracts(t *testing.T) {
	source := t.TempDir()
	for _, name := range []string{"00001_baseline.sql", "00002_suppression_address_hash.sql", "00003_real_contract.sql"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	files := contractGateFixtureFiles(source)
	if len(files) != 2 || filepath.Base(files[0]) != "00001_baseline.sql" || filepath.Base(files[1]) != "00002_suppression_address_hash.sql" {
		t.Fatalf("unexpected expand-era fixture files: %v", files)
	}
}

func newContractGateRepo(t *testing.T) *contractGateRepo {
	t.Helper()
	script, err := filepath.Abs("scripts/contract-gate.sh")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "repo")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	r := &contractGateRepo{t: t, dir: dir, env: contractGateEnv(t, filepath.Dir(dir)), script: script}
	r.git(r.dir, "-c", "init.defaultBranch=master", "init", "-q")
	return r
}

func (r *contractGateRepo) git(dir string, args ...string) string {
	r.t.Helper()
	full := append([]string{"-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false", "-c", "core.autocrlf=false"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	cmd.Env = r.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *contractGateRepo) write(rel, content string) {
	r.t.Helper()
	p := filepath.Join(r.dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *contractGateRepo) commit(msg string) {
	r.t.Helper()
	r.git(r.dir, "add", "-A")
	r.git(r.dir, "commit", "-q", "-m", msg)
}

// gate runs the script in dir and returns its exit code and combined output.
func (r *contractGateRepo) gate(dir string) (int, string) {
	r.t.Helper()
	cmd := exec.Command("bash", r.script)
	cmd.Dir = dir
	cmd.Env = r.env
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0, string(out)
	case errors.As(err, &exit):
		return exit.ExitCode(), string(out)
	default:
		r.t.Fatalf("run contract-gate.sh: %v", err)
		return -1, ""
	}
}

func (r *contractGateRepo) expect(dir string, wantCode int, wantOut ...string) string {
	r.t.Helper()
	code, out := r.gate(dir)
	if code != wantCode {
		r.t.Fatalf("exit %d, want %d\n%s", code, wantCode, out)
	}
	for _, w := range wantOut {
		if !strings.Contains(out, w) {
			r.t.Fatalf("output lacks %q:\n%s", w, out)
		}
	}
	return out
}

const (
	contractGateBaseSQL   = "-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 1;\n"
	contractGateExpandSQL = "-- +goose Up\n-- Expand step.\nSELECT 2;\n-- +goose Down\nSELECT 2;\n"
)

func contractGateContractSQL(header string) string {
	return "-- +goose Up\n" + header + "\nSELECT 3;\n-- +goose Down\nSELECT 3;\n"
}

const contractGateHeader = "-- contracts: 00002_expand.sql"

// contractGateTagged is the passing shape: commit 1 holds the base and the
// expand and carries v0.0.1; commit 2 adds the contract.
func contractGateTagged(t *testing.T, header string) *contractGateRepo {
	r := newContractGateRepo(t)
	r.write("migrations/00001_base.sql", contractGateBaseSQL)
	r.write("migrations/00002_expand.sql", contractGateExpandSQL)
	r.commit("expand")
	r.git(r.dir, "tag", "v0.0.1")
	r.write("migrations/00003_contract.sql", contractGateContractSQL(header))
	r.commit("contract")
	return r
}

func TestContractGateScript(t *testing.T) {
	for _, tool := range []string{"git", "bash"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s is required for the contract gate tests: %v", tool, err)
		}
	}
	const noContracts = "contract-gate: no contract migrations"
	const noTag = "contract-gate: 00003_contract.sql: no earlier v* tag contains 00002_expand.sql without the contract"

	t.Run("no migrations directory passes", func(t *testing.T) {
		r := newContractGateRepo(t)
		r.write("README.md", "x\n")
		r.commit("one")
		r.expect(r.dir, 0, noContracts)
	})
	t.Run("a file with no header is ignored", func(t *testing.T) {
		r := newContractGateRepo(t)
		r.write("migrations/00001_base.sql", contractGateBaseSQL)
		r.write("migrations/00002_expand.sql", contractGateExpandSQL)
		r.write("migrations/notes.sql", "-- contracts: not a migration name\n")
		r.commit("one")
		r.expect(r.dir, 0, noContracts)
	})
	t.Run("no tag fails", func(t *testing.T) {
		r := newContractGateRepo(t)
		r.write("migrations/00001_base.sql", contractGateBaseSQL)
		r.write("migrations/00002_expand.sql", contractGateExpandSQL)
		r.write("migrations/00003_contract.sql", contractGateContractSQL(contractGateHeader))
		r.commit("all at once")
		r.expect(r.dir, 1, noTag)
	})
	t.Run("a tag that is not v* fails", func(t *testing.T) {
		r := contractGateTagged(t, contractGateHeader)
		r.git(r.dir, "tag", "-d", "v0.0.1")
		r.git(r.dir, "tag", "release-1", "HEAD~1")
		r.expect(r.dir, 1, noTag)
	})
	t.Run("a tag without the expand fails", func(t *testing.T) {
		r := newContractGateRepo(t)
		r.write("migrations/00001_base.sql", contractGateBaseSQL)
		r.commit("base")
		r.git(r.dir, "tag", "v0.0.1")
		r.write("migrations/00002_expand.sql", contractGateExpandSQL)
		r.commit("expand")
		r.write("migrations/00003_contract.sql", contractGateContractSQL(contractGateHeader))
		r.commit("contract")
		r.expect(r.dir, 1, noTag)
	})
	t.Run("a tag holding the expand and the contract fails", func(t *testing.T) {
		r := newContractGateRepo(t)
		r.write("migrations/00001_base.sql", contractGateBaseSQL)
		r.write("migrations/00002_expand.sql", contractGateExpandSQL)
		r.write("migrations/00003_contract.sql", contractGateContractSQL(contractGateHeader))
		r.commit("all")
		r.git(r.dir, "tag", "v0.0.1")
		r.expect(r.dir, 1, noTag)
	})
	t.Run("a tag on a branch not merged into HEAD fails", func(t *testing.T) {
		r := newContractGateRepo(t)
		r.write("migrations/00001_base.sql", contractGateBaseSQL)
		r.commit("base")
		r.git(r.dir, "checkout", "-q", "-b", "side")
		r.write("migrations/00002_expand.sql", contractGateExpandSQL)
		r.commit("expand on the side")
		r.git(r.dir, "tag", "v0.0.1")
		r.git(r.dir, "checkout", "-q", "master")
		r.write("migrations/00002_expand.sql", contractGateExpandSQL)
		r.write("migrations/00003_contract.sql", contractGateContractSQL(contractGateHeader))
		r.commit("expand and contract")
		r.expect(r.dir, 1, noTag)
	})
	t.Run("an earlier tag with only the expand passes", func(t *testing.T) {
		r := contractGateTagged(t, contractGateHeader)
		out := r.expect(r.dir, 0,
			"contract-gate: 00003_contract.sql contracts 00002_expand.sql (shipped in v0.0.1)",
			"publication evidence for this tag must be recorded in the PR")
		if strings.Contains(out, noContracts) {
			t.Fatalf("a contract was found, so the no-contract line must not print:\n%s", out)
		}
	})
	t.Run("an annotated tag counts", func(t *testing.T) {
		r := contractGateTagged(t, contractGateHeader)
		r.git(r.dir, "tag", "-d", "v0.0.1")
		r.git(r.dir, "tag", "-a", "-m", "release", "v0.1.0", "HEAD~1")
		r.expect(r.dir, 0, "(shipped in v0.1.0)")
	})
	t.Run("an expand edited after its tag fails", func(t *testing.T) {
		r := contractGateTagged(t, contractGateHeader)
		r.write("migrations/00002_expand.sql", contractGateExpandSQL+"-- edited\n")
		r.commit("edit the expand")
		r.expect(r.dir, 1, "contract-gate: 00002_expand.sql changed after v0.0.1")
	})
	t.Run("a header naming a missing expand fails", func(t *testing.T) {
		r := contractGateTagged(t, "-- contracts: 00002_missing.sql")
		r.expect(r.dir, 1, "00002_missing.sql does not exist")
	})
	t.Run("a header naming itself fails", func(t *testing.T) {
		r := contractGateTagged(t, "-- contracts: 00003_contract.sql")
		r.expect(r.dir, 1, "names itself")
	})
	t.Run("a header naming a higher number fails", func(t *testing.T) {
		r := contractGateTagged(t, "-- contracts: 00004_later.sql")
		r.write("migrations/00004_later.sql", contractGateExpandSQL)
		r.commit("later")
		r.expect(r.dir, 1, "expand 00004_later.sql must have a lower number")
	})
	malformed := map[string]string{
		"missing space":   "--contracts: 00002_expand.sql",
		"capitalized":     "-- Contracts: 00002_expand.sql",
		"no space after":  "-- contracts:00002_expand.sql",
		"not a file name": "--contracts: x.sql",
		"trailing space":  "-- contracts: 00002_expand.sql ",
		"two lines":       contractGateHeader + "\n" + contractGateHeader,
		"two different":   contractGateHeader + "\n-- contracts: 00001_base.sql",
	}
	for name, header := range malformed {
		t.Run("malformed: "+name, func(t *testing.T) {
			r := contractGateTagged(t, header)
			r.expect(r.dir, 1, "contract-gate: 00003_contract.sql: malformed contracts line")
		})
	}
	t.Run("a shallow clone fails even with no contract file", func(t *testing.T) {
		src := newContractGateRepo(t)
		src.write("migrations/00001_base.sql", contractGateBaseSQL)
		src.commit("one")
		src.write("migrations/00002_expand.sql", contractGateExpandSQL)
		src.commit("two")
		dst := filepath.Join(t.TempDir(), "clone")
		src.git(src.dir, "clone", "-q", "--depth", "1", "file://"+src.dir, dst)
		if got := src.git(dst, "rev-parse", "--is-shallow-repository"); got != "true" {
			t.Fatalf("fixture is not shallow: %q", got)
		}
		src.expect(dst, 1, "contract-gate: shallow checkout; fetch full history and tags")
	})
	t.Run("a subdirectory behaves like the root", func(t *testing.T) {
		r := contractGateTagged(t, contractGateHeader)
		want := r.expect(r.dir, 0, "(shipped in v0.0.1)")
		if got := r.expect(filepath.Join(r.dir, "migrations"), 0); got != want {
			t.Fatalf("subdirectory output differs:\n%s\nvs\n%s", got, want)
		}
		bad := contractGateTagged(t, "--contracts: x.sql")
		bad.expect(filepath.Join(bad.dir, "migrations"), 1, "malformed contracts line")
	})
	t.Run("outside a repository exits 1", func(t *testing.T) {
		r := newContractGateRepo(t)
		plain := filepath.Join(t.TempDir(), "plain")
		if err := os.Mkdir(plain, 0o755); err != nil {
			t.Fatal(err)
		}
		r.env = contractGateEnv(t, filepath.Dir(plain))
		r.expect(plain, 1, "contract-gate: not a git repository")
	})
	t.Run("the real migrations pass with a tag and fail without one", func(t *testing.T) {
		// Copy just the two real files this history needs. Other migrations,
		// including future contracts, cannot be placed in its expand-era tag.
		files := contractGateFixtureFiles("migrations")
		r := newContractGateRepo(t)
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			r.write(f, string(b))
		}
		r.commit("migrations as they are")
		r.write("migrations/00003_x.sql", contractGateContractSQL("-- contracts: 00002_suppression_address_hash.sql"))
		r.commit("a contract for the suppression expand")
		r.expect(r.dir, 1, "contract-gate: 00003_x.sql: no earlier v* tag contains 00002_suppression_address_hash.sql without the contract")
		r.git(r.dir, "tag", "v0.0.1", "HEAD~1")
		r.expect(r.dir, 0, "contract-gate: 00003_x.sql contracts 00002_suppression_address_hash.sql (shipped in v0.0.1)")
	})
}
