// Package httpx is the HTTP plumbing that needs nothing from the app: the
// security headers, the body cap, the redacting access log and the client
// address behind Caddy.
package httpx

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// ContentSecurityPolicy is strict: scripts, styles, fonts and images come
// only from this site, with no inline script or style and no eval. The pages
// are server-rendered; htmx runs with evaluation off (web/templates/base.html).
const ContentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; " +
	"img-src 'self' data:; font-src 'self'; connect-src 'self'; manifest-src 'self'; " +
	"form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

// SecurityHeaders sets the headers every response carries. HSTS only when the
// site is served over HTTPS.
func SecurityHeaders(hsts bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", ContentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), interest-cohort=()")
		if hsts {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

// LimitBody refuses a body over n bytes before reading it, and makes reads
// past n fail for a body that did not declare its size.
func LimitBody(n int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > n {
			http.Error(w, "That was too large to send.", http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, n)
		next.ServeHTTP(w, r)
	})
}

// secretPrefixes are paths whose next segment is a secret. The access log
// keeps the route and drops the secret, and never logs a query string, so a
// log file cannot replay a link (docs/HAZARDS.md section 5).
var secretPrefixes = []string{"/l/", "/invite/", "/g/", "/feed/"}

// RedactPath is the path as the log shows it.
func RedactPath(p string) string {
	for _, pre := range secretPrefixes {
		if strings.HasPrefix(p, pre) {
			return pre + "REDACTED"
		}
	}
	return p
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// AccessLog writes one line per request with the path redacted.
func AccessLog(log *slog.Logger, clientIP func(*http.Request) netip.Addr, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			if p := recover(); p != nil {
				log.Error("panic", "method", r.Method, "path", RedactPath(r.URL.Path), "panic", fmt.Sprint(p))
				if sw.status == 0 {
					http.Error(sw, "Something went wrong on our side. Please try again in a moment.", http.StatusInternalServerError)
				}
			}
			log.Info("request", "method", r.Method, "path", RedactPath(r.URL.Path), "status", sw.status,
				"ms", time.Since(start).Milliseconds(), "client", clientIP(r).String())
		}()
		next.ServeHTTP(sw, r)
	})
}

// Proxies is the address range Caddy sits in. Only a request from there may
// say, in X-Forwarded-For, who the client really is.
type Proxies struct {
	prefixes []netip.Prefix
}

// ParseProxies reads a comma-separated list of CIDR ranges. A range that
// covers every address is refused: it would let any client claim any
// address and step around every rate limit.
func ParseProxies(s string) (Proxies, error) {
	var p Proxies
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		pre, err := netip.ParsePrefix(part)
		if err != nil {
			return Proxies{}, fmt.Errorf("TRUSTED_PROXIES: %q is not a CIDR range", part)
		}
		if pre.Bits() == 0 {
			return Proxies{}, errors.New("TRUSTED_PROXIES must not trust every address")
		}
		p.prefixes = append(p.prefixes, pre.Masked())
	}
	return p, nil
}

func (p Proxies) trusted(a netip.Addr) bool {
	for _, pre := range p.prefixes {
		if pre.Contains(a.Unmap()) {
			return true
		}
	}
	return false
}

// ClientIP is the request's client: the peer, or, when the peer is a trusted
// proxy, the nearest untrusted address in X-Forwarded-For.
func (p Proxies) ClientIP(r *http.Request) netip.Addr {
	peer := remoteAddr(r)
	if !p.trusted(peer) {
		return peer
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			return peer
		}
		a = a.Unmap()
		if !p.trusted(a) {
			return a
		}
	}
	return peer
}

func remoteAddr(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.IPv4Unspecified()
	}
	return a.Unmap()
}

// ClientKey is the key a rate limit counts a client by: an IPv4 address, or
// an IPv6 /64, since one household or phone gets a whole /64.
func ClientKey(a netip.Addr) string {
	if a.Is6() {
		pre, _ := a.Prefix(64)
		return pre.String()
	}
	return a.String()
}
