package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRedactLogPath(t *testing.T) {
	cases := map[string]string{
		"/cal/u/abcDEF_123-x.ics":                          "/cal/u/REDACTED.ics",
		"/cal/public.ics":                                  "/cal/public.ics",
		"/cal/groups/4.ics":                                "/cal/groups/4.ics",
		"/guest/manage/s3cret":                             "/guest/manage/REDACTED",
		"/api/v1/guest-registrations/manage/s3cret/cancel": "/api/v1/guest-registrations/manage/REDACTED/cancel",
		"/api/v1/events":                                   "/api/v1/events",
	}
	for in, want := range cases {
		if got := redactLogPath(in); got != want {
			t.Errorf("redactLogPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// The calendar token and email-link tokens never reach the access log.
func TestAccessLogKeepsSecretsOut(t *testing.T) {
	var buf bytes.Buffer
	saved := gin.DefaultWriter
	gin.DefaultWriter = &buf
	defer func() { gin.DefaultWriter = saved }()

	r := gin.New()
	r.Use(accessLog())
	r.GET("/*any", func(c *gin.Context) { c.Status(http.StatusNotFound) })
	for _, u := range []string{"/cal/u/feedsecret123.ics", "/reset-password?token=resetsecret456"} {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, u, nil))
	}
	out := buf.String()
	for _, secret := range []string{"feedsecret123", "resetsecret456"} {
		if strings.Contains(out, secret) {
			t.Errorf("access log leaked %q:\n%s", secret, out)
		}
	}
	if !strings.Contains(out, "/cal/u/REDACTED.ics") || !strings.Contains(out, "/reset-password") {
		t.Errorf("access log lost the route:\n%s", out)
	}
}
