package main

import (
	"fold/models"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestAPICSRFRequired(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "bob", "secret123", models.RoleMember)
	c := loginAPI(t, app, "bob", "secret123")

	// POST without the CSRF header → 403
	w := c.do("POST", "/api/v1/groups", map[string]any{"name": "X"}, false)
	if w.Code != http.StatusForbidden {
		t.Fatalf("missing csrf: got %d", w.Code)
	}
	// with token → not a CSRF rejection
	w = c.do("POST", "/api/v1/groups", map[string]any{"name": "X"}, true)
	if w.Code == http.StatusForbidden {
		t.Fatalf("valid csrf rejected: %d", w.Code)
	}
}

func TestCSRFFailureIsJSON(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "bob", "password1234", models.RoleMember)
	c := loginAPI(t, app, "bob", "password1234")
	w := c.do("POST", "/api/v1/groups", map[string]any{"name": "X"}, false)
	if w.Code != http.StatusForbidden {
		t.Fatalf("csrf: got %d", w.Code)
	}
	var body struct {
		Error string `json:"error"`
	}
	decodeJSON(t, w, &body)
	if body.Error == "" {
		t.Fatal("expected JSON error")
	}
}

// TestCSRFExemptRoutesAreExact sends every state-changing route a request
// with no CSRF token. Only the routes in csrfExemptRoutes may get past CSRF.
func TestCSRFExemptRoutesAreExact(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password12345", models.RoleAdmin) // get past /setup
	r := app.setupRouter()
	param := regexp.MustCompile(`[:*][A-Za-z]+`)

	var exempt []string
	for _, rt := range r.Routes() {
		if isSafeMethod(rt.Method) {
			continue
		}
		req := httptest.NewRequest(rt.Method, param.ReplaceAllString(rt.Path, "test"), nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if !strings.Contains(w.Body.String(), "CSRF token invalid") {
			exempt = append(exempt, rt.Method+" "+rt.Path)
		}
	}
	var want []string
	for _, e := range csrfExemptRoutes {
		if strings.Contains(e, " ") {
			want = append(want, e)
			continue
		}
		for _, m := range []string{"POST", "PUT", "PATCH", "DELETE", "CONNECT"} {
			want = append(want, m+" "+e)
		}
	}
	slices.Sort(exempt)
	slices.Sort(want)
	if !slices.Equal(exempt, want) {
		t.Fatalf("routes outside CSRF:\n got %v\nwant %v", exempt, want)
	}

	// The acceptance check in plain words.
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/hooks/postmark/email", nil))
	if w.Code == http.StatusForbidden {
		t.Fatal("POST /hooks/postmark/email was rejected by CSRF")
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/auth/logout", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("POST /api/v1/auth/logout without a token = %d, want 403", w.Code)
	}
}

// csrfExemptRoutes are the only state-changing routes registered outside
// csrfMiddleware (setupRouter, main.go). TestCSRFExemptRoutesAreExact keeps it honest.
var csrfExemptRoutes = []string{"POST /hooks/postmark/email", "POST /u/unsubscribe/:token"}
