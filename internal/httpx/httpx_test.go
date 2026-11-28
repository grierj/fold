package httpx

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestClientIPTrustsOnlyTheProxy(t *testing.T) {
	p, err := ParseProxies("172.30.0.0/24")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "203.0.113.9:5555"
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	if got := p.ClientIP(r); got.String() != "203.0.113.9" {
		t.Errorf("an outside client set its own address: %s", got)
	}
	r.RemoteAddr = "172.30.0.2:5555"
	r.Header.Set("X-Forwarded-For", "9.9.9.9, 198.51.100.7")
	if got := p.ClientIP(r); got.String() != "198.51.100.7" {
		t.Errorf("behind Caddy: %s", got)
	}
}

func TestProxiesRefuseEverything(t *testing.T) {
	for _, s := range []string{"0.0.0.0/0", "::/0", "10.0.0.0/8, 0.0.0.0/0", "nonsense"} {
		if _, err := ParseProxies(s); err == nil {
			t.Errorf("accepted %q", s)
		}
	}
}

func TestClientKeyGroupsIPv6By64(t *testing.T) {
	a := ClientKey(netip.MustParseAddr("2001:db8:1:2:aaaa::1"))
	b := ClientKey(netip.MustParseAddr("2001:db8:1:2:bbbb::9"))
	if a != b {
		t.Fatalf("%s != %s", a, b)
	}
}

func TestRedactPath(t *testing.T) {
	if got := RedactPath("/l/abcdefsecret"); got != "/l/REDACTED" {
		t.Fatal(got)
	}
	if got := RedactPath("/g/abcdefsecret"); got != "/g/REDACTED" {
		t.Fatal(got)
	}
	if got := RedactPath("/feed/abcdefsecret/calendar.ics"); got != "/feed/REDACTED" {
		t.Errorf("a feed path: %q", got)
	}
	if got := RedactPath("/signin"); got != "/signin" {
		t.Fatal(got)
	}
}

// A secret in a path never reaches the access log, nor does a query string.
func TestAccessLogKeepsSecretsOut(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	h := AccessLog(log, func(*http.Request) netip.Addr { return netip.MustParseAddr("203.0.113.9") },
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) }))
	for _, p := range []string{"/feed/feedsecret123/calendar.ics", "/l/linksecret123", "/g/managesecret123", "/calendar?q=querysecret123"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", p, nil))
	}
	if strings.Contains(buf.String(), "secret123") || !strings.Contains(buf.String(), "/feed/REDACTED") {
		t.Fatalf("the access log:\n%s", buf.String())
	}
}
