package main

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// goSources is every non-test Go file's text, for the checks below.
func goSources(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(".", func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == ".git" || d.Name() == "web") {
			return filepath.SkipDir
		}
		if strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") && p != "api_errors.go" {
			src, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			b.Write(src)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// A reworded message would silently lose its code; each key must still be
// a message the server sends.
func TestErrorCodesKeysAreUsed(t *testing.T) {
	src := goSources(t)
	for msg := range errorCodes {
		if strings.Contains(src, strconv.Quote(msg)) || strings.Contains(src, "`"+msg+"`") {
			continue
		}
		// "password must be at least "+strconv.Itoa(minPasswordLength)+" characters"
		if msg == "password must be at least 10 characters" && strings.Contains(src, `"password must be at least "`) {
			continue
		}
		t.Errorf("errorCodes key %q is not sent anywhere; update the key to the new wording", msg)
	}
}

var (
	webCopyKeyRe   = regexp.MustCompile(`(?m)^  ([a-z_0-9]+): "`)
	explicitCodeRe = regexp.MustCompile(`jsonErrorCode\(c, [^,]+, "([a-z_0-9]+)"`)
)

// Every code the API can send has words in web/src/lib/errors.ts, and the
// map has nothing the API never sends.
func TestWebErrorCopyCoversEveryCode(t *testing.T) {
	codes := map[string]bool{"server_error": true}
	for _, c := range errorCodes {
		codes[c] = true
	}
	for _, c := range statusCodes {
		codes[c] = true
	}
	for _, m := range explicitCodeRe.FindAllStringSubmatch(goSources(t), -1) {
		codes[m[1]] = true
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

func TestErrorBody(t *testing.T) {
	cases := []struct {
		status    int
		msg, code string
	}{
		{http.StatusUnauthorized, "invalid email or password", "bad_credentials"},
		{http.StatusTooManyRequests, "too many sign-in attempts; try again in 15 minutes", "locked_out"},
		{http.StatusConflict, "that email already has an account", "email_taken"},
		{http.StatusNotFound, "group not found", "not_found"},
		{http.StatusBadRequest, "invalid request body", "invalid"},
		{http.StatusForbidden, "forbidden", "not_allowed"},
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
