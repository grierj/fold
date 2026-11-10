package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"fold/internal/guardkit"

	"github.com/gin-gonic/gin"
)

// goSources is every non-test Go file's text, for the checks below.
func goSources(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	production := guardkit.Production(t)
	for _, path := range production.Names() {
		b.WriteString(production[path])
	}
	return b.String()
}

// A declared error is a real answer: an error status, a code and a message.
func TestDeclaredAPIErrorsAreComplete(t *testing.T) {
	for _, e := range declaredAPIErrors {
		if e.Status < 400 || e.Code == "" || e.Message == "" {
			t.Errorf("declared apiError %+v needs a 4xx/5xx status, a code and a message", e)
		}
	}
}

var (
	webCopyKeyRe = regexp.MustCompile(`(?m)^  ([a-z_0-9]+): "`)
	// apiErrf takes its code as the second argument, written out where the
	// error is built.
	runtimeCodeRe = regexp.MustCompile(`apiErrf\([^,]+, "([a-z_0-9]+)"`)
)

// Every code the API can send has words in web/src/lib/errors.ts, and the
// map has nothing the API never sends.
func TestWebErrorCopyCoversEveryCode(t *testing.T) {
	codes := map[string]bool{"server_error": true}
	for _, e := range declaredAPIErrors {
		codes[e.Code] = true
	}
	for _, c := range statusCodes {
		codes[c] = true
	}
	for _, m := range runtimeCodeRe.FindAllStringSubmatch(goSources(t), -1) {
		codes[m[1]] = true
	}
	for _, e := range householdRegistrationRefusals {
		codes[e.Code] = true
	}
	b, err := os.ReadFile("web/src/lib/errors.ts")
	if err != nil {
		t.Fatal(err)
	}
	web := map[string]bool{}
	for _, m := range webCopyKeyRe.FindAllStringSubmatch(string(b), -1) {
		web[m[1]] = true
	}
	var missing, extra []string
	for c := range codes {
		if !web[c] {
			missing = append(missing, c)
		}
	}
	for c := range web {
		if !codes[c] {
			extra = append(extra, c)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing)+len(extra) > 0 {
		t.Fatalf("ERROR_COPY in web/src/lib/errors.ts: missing %v, never sent %v", missing, extra)
	}
}

// A coded message is written once, in its declaration. A second copy (a
// jsonError call, a helper's argument, a constant) would answer the general
// code for its status and the SPA would lose the words: send the declared
// value with fail instead. A declared value whose code is its status's general
// code (errActorForbidden, "forbidden") is the same answer either way.
func TestDeclaredMessagesAreWrittenOnce(t *testing.T) {
	coded := map[string]bool{}
	for _, e := range declaredAPIErrors {
		if e.Code != statusCode(e.Status) {
			coded[e.Message] = true
		}
	}
	parsed := guardkit.Production(t).Parse(0)
	for _, problem := range parsed.Problems {
		t.Error(problem)
	}
	declaration := map[*ast.BasicLit]bool{}
	for _, pf := range parsed.Package("main") {
		ast.Inspect(pf.AST, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch guardkit.Callee(call) {
			case "apiErr", "apiErrExtra", "apiErrf":
				for _, arg := range call.Args {
					if lit, ok := arg.(*ast.BasicLit); ok {
						declaration[lit] = true
					}
				}
			}
			return true
		})
	}
	checked := 0
	for _, pf := range parsed.Package("main") {
		ast.Inspect(pf.AST, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			checked++
			if msg, err := strconv.Unquote(lit.Value); err == nil && coded[msg] && !declaration[lit] {
				t.Errorf("%s: %q is a declared apiError's message; use the declared value (fail(c, ...)) instead of a second copy",
					pf.Path+":"+strconv.Itoa(parsed.Fset.Position(lit.Pos()).Line), msg)
			}
			return true
		})
	}
	if checked < 1000 {
		t.Errorf("only %d string literals seen; the guard is not reading the tree", checked)
	}
}

// apiErr registers its value in declaredAPIErrors, so it runs once, as a package
// variable's initializer, never inside a function (a handler would register
// a copy per request).
func TestAPIErrorsAreDeclaredAtPackageLevel(t *testing.T) {
	parsed := guardkit.Production(t).Parse(0)
	for _, problem := range parsed.Problems {
		t.Error(problem)
	}
	for _, pf := range parsed.Package("main") {
		if pf.Path == "api_errors.go" { // the constructors call each other
			continue
		}
		guardkit.InspectStack(pf.AST, func(n ast.Node, stack []ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch guardkit.Callee(call) {
			case "apiErr", "apiErrExtra", "apiErrWith":
			default:
				return true
			}
			for _, parent := range stack {
				if fn, ok := parent.(*ast.FuncDecl); ok {
					t.Errorf("%s: %s is called inside %s; declare it as a package variable (apiErrf is for messages built at run time)",
						pf.Path+":"+strconv.Itoa(parsed.Fset.Position(call.Pos()).Line), guardkit.Callee(call), fn.Name.Name)
				}
			}
			return true
		})
	}
}

func TestFailAnswersAnAPIError(t *testing.T) {
	answer := func(err error) (int, map[string]any) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		fail(c, err)
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return w.Code, body
	}
	code, body := answer(errEventFull)
	if code != http.StatusConflict || body["code"] != "event_full" || body["error"] != "This event is full" || body["full"] != nil {
		t.Errorf("errEventFull: %d %v", code, body)
	}
	// A wrapped value answers as itself.
	code, body = answer(fmt.Errorf("saving: %w", errEventFull))
	if code != http.StatusConflict || body["code"] != "event_full" {
		t.Errorf("wrapped: %d %v", code, body)
	}
	// The same refusal with its extra key, and still equal to its base.
	code, body = answer(errEventFullNoWaitlist)
	if code != http.StatusConflict || body["full"] != true || !errors.Is(errEventFullNoWaitlist, errEventFull) {
		t.Errorf("errEventFullNoWaitlist: %d %v", code, body)
	}
	// An answerer builds its own, with data.
	code, body = answer(errNeedHostSuccessors{data: soleHostEventsJSON{Events: []soleHostEventJSON{}, Candidates: []successorJSON{}}})
	if code != http.StatusBadRequest || body["code"] != "needs_host_successor" || body["events"] == nil || !errors.Is(errNeedHostSuccessors{}, errNeedsHostSuccessor) {
		t.Errorf("errNeedHostSuccessors: %d %v", code, body)
	}
	code, body = answer(householdRegistrationError{code: "household_age_mismatch", personID: 7})
	if code != http.StatusConflict || body["code"] != "household_age_mismatch" || body["personId"] != float64(7) {
		t.Errorf("household refusal: %d %v", code, body)
	}
	// An unplanned error is logged, not sent: no detail leaves.
	code, body = answer(errors.New("pq: relation \"users\" does not exist"))
	if code != http.StatusInternalServerError || body["code"] != "server_error" || body["error"] != "server error" {
		t.Errorf("unplanned: %d %v", code, body)
	}
	// A declared 5xx is real copy on purpose (the deletion outcomes).
	code, body = answer(errDeleteOutcomeUnknown)
	if code != http.StatusInternalServerError || body["code"] != "delete_outcome_unknown" {
		t.Errorf("errDeleteOutcomeUnknown: %d %v", code, body)
	}
}

func TestErrorBody(t *testing.T) {
	cases := []struct {
		status    int
		msg, code string
	}{
		{http.StatusNotFound, "group not found", "not_found"},
		{http.StatusBadRequest, "invalid request body", "invalid"},
		{http.StatusForbidden, "forbidden", "not_allowed"},
		{http.StatusUnauthorized, "unauthorized", "signed_out"},
		{http.StatusConflict, "something else", "conflict"},
		{http.StatusRequestEntityTooLarge, "request too large", "file_too_large"},
	}
	for _, tc := range cases {
		b := errorBody(tc.status, tc.msg)
		if b["code"] != tc.code || b["error"] != tc.msg {
			t.Errorf("%d %q: got %v", tc.status, tc.msg, b)
		}
	}
	// A server error never sends its detail.
	b := errorBody(http.StatusInternalServerError, "pq: relation \"users\" does not exist")
	if b["code"] != "server_error" || b["error"] != "server error" {
		t.Errorf("500 leaked: %v", b)
	}
}

// fail sends an apiError's message as written, so a 5xx one must be fixed
// English with no runtime detail. Every 5xx declaration is reviewed and listed
// here; a new one fails until it is, and apiErrf (a message built at run time)
// may never carry a 5xx status.
var reviewedServerErrors = map[string]bool{
	"attempt_busy": true, "reason_review_busy": true, "delete_outcome_unknown": true,
	"delete_timed_out": true, "restore_detected": true, "message_test_failed": true,
	"server_error": true,
}

func TestServerErrorsCarryNoRuntimeDetail(t *testing.T) {
	for _, e := range declaredAPIErrors {
		if e.Status >= 500 && !reviewedServerErrors[e.Code] {
			t.Errorf("declared %d apiError %q (%q): review that its message carries no runtime detail, then list its code in reviewedServerErrors", e.Status, e.Code, e.Message)
		}
	}
	for _, m := range runtimeStatusRe.FindAllStringSubmatch(goSources(t), -1) {
		if m[1] == "status int" {
			continue // the declaration itself
		}
		if strings.HasPrefix(m[1], "http.Status") {
			if s, ok := serverStatusNames[m[1]]; ok && s {
				t.Errorf("apiErrf(%s, ...): a runtime message may not be a 5xx answer; use a declared, reviewed apiError", m[1])
			}
			continue
		}
		t.Errorf("apiErrf(%s, ...): pass the status as an http.Status constant so its class can be checked", m[1])
	}
}

var (
	runtimeStatusRe   = regexp.MustCompile(`\bapiErrf\(([^,()]+),`)
	serverStatusNames = map[string]bool{
		"http.StatusInternalServerError": true, "http.StatusNotImplemented": true, "http.StatusBadGateway": true,
		"http.StatusServiceUnavailable": true, "http.StatusGatewayTimeout": true,
	}
)

// The answers clients and the SPA act on by status as well as code: sign-in
// lockout and the leader's invitation limit are the only 429s (DEC-52), a stale
// session is 401, and the deletion outcomes keep the statuses docs/agents/erasure.md
// ("Deletion outcomes" and "Restore barrier") specifies.
func TestCriticalAnswersKeepTheirStatus(t *testing.T) {
	for _, c := range []struct {
		err    *apiError
		status int
		code   string
	}{
		{errSecuritySession, http.StatusUnauthorized, "signed_out"},
		{errLockedOut, http.StatusTooManyRequests, "locked_out"},
		{errInvitationLimit, http.StatusTooManyRequests, "invite_limit"},
		{errDeleteOutcomeUnknown, http.StatusInternalServerError, "delete_outcome_unknown"},
		{errDeleteTimedOut, http.StatusServiceUnavailable, "delete_timed_out"},
		{errDeleteHouseholdBusy, http.StatusConflict, "delete_household_busy"},
		{errRestoreDetected, http.StatusServiceUnavailable, "restore_detected"},
		{errActorBusy, http.StatusConflict, "admin_account_busy"},
	} {
		if c.err.Status != c.status || c.err.Code != c.code {
			t.Errorf("%s answers %d %q, want %d %q", c.err.Message, c.err.Status, c.err.Code, c.status, c.code)
		}
	}
}
