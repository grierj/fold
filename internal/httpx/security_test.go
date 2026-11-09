package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestSecurityHeaders(t *testing.T) {
	for _, hsts := range []bool{false, true} {
		r := gin.New()
		r.Use(SecurityHeaders(hsts))
		r.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
		h := w.Header()
		csp := h.Get("Content-Security-Policy")
		if !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "frame-ancestors 'self'") ||
			!strings.Contains(csp, "object-src 'none'") || strings.Contains(csp, "unsafe") {
			t.Fatalf("CSP: %q", csp)
		}
		// The CSP names the worker and manifest sources (research 2.3).
		if !strings.Contains(csp, "worker-src 'self'; manifest-src 'self'") {
			t.Fatalf("CSP: %q", csp)
		}
		for k, v := range map[string]string{
			"X-Content-Type-Options": "nosniff",
			"Referrer-Policy":        "same-origin",
			"X-Frame-Options":        "SAMEORIGIN",
			"Permissions-Policy":     "camera=(), microphone=(), geolocation=()",
		} {
			if h.Get(k) != v {
				t.Fatalf("%s: %q", k, h.Get(k))
			}
		}
		if got := h.Get("Strict-Transport-Security"); (got != "") != hsts {
			t.Fatalf("HSTS with hsts=%v: %q", hsts, got)
		}
	}
}

func TestLimitBodyRefusesOversizedRequests(t *testing.T) {
	r := gin.New()
	r.Use(LimitBody(10, func(c *gin.Context) { refuse(c, http.StatusRequestEntityTooLarge, "request too large") }))
	r.POST("/", func(c *gin.Context) { c.Status(http.StatusOK) })
	post := func(body string) int {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("POST", "/", strings.NewReader(body)))
		return w.Code
	}
	if got := post("0123456789"); got != http.StatusOK {
		t.Fatalf("at the limit: %d", got)
	}
	if got := post("0123456789a"); got != http.StatusRequestEntityTooLarge {
		t.Fatalf("over the limit: %d", got)
	}
}

func TestManifestShortensLongNames(t *testing.T) {
	if m := manifestFor("Grace Church", "Fold"); m.ShortName != "Grace Church" { // 12 characters
		t.Errorf("short_name %q", m.ShortName)
	}
	if m := manifestFor("Iglesia Fé y Esperanza", "Fold"); m.Name != "Iglesia Fé y Esperanza" || m.ShortName != "Fold" {
		t.Errorf("long name: %+v", m)
	}
	if m := manifestFor("Église Sœur", "Fold"); m.ShortName != "Église Sœur" { // runes, not bytes
		t.Errorf("runes: %q", m.ShortName)
	}
}

func TestHealthzFollowsPing(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{{nil, http.StatusOK}, {http.ErrServerClosed, http.StatusServiceUnavailable}} {
		r := gin.New()
		r.GET("/healthz", Healthz(func(context.Context) error { return tc.err }))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
		if w.Code != tc.want || w.Body.Len() != 0 {
			t.Errorf("ping %v: %d %q", tc.err, w.Code, w.Body.String())
		}
	}
}
