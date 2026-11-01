package push

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"syscall"
	"time"
)

// SSRF defence (research 2.8). A browser hands Fold a URL and Fold POSTs to
// it, so the endpoint must be an https URL on a known push service, and
// the address it resolves to must be public. Both are checked on every
// send; slice b checks Allowed again when a device subscribes.

// DefaultHosts are the browsers' push services. A host matches itself and
// its subdomains on a dot boundary (Safari uses web.push.apple.com, Edge
// *.notify.windows.com; Samsung Internet uses FCM). Apple and Microsoft are
// unconfirmed until the first-church checks (DEC-128).
var DefaultHosts = []string{
	"fcm.googleapis.com",
	"android.googleapis.com",
	"updates.push.services.mozilla.com",
	"push.apple.com",
	"notify.windows.com",
}

// ErrNotAllowed: the endpoint isn't an https URL on an allow-listed push
// service. The message never repeats the endpoint.
var ErrNotAllowed = errors.New("push: that endpoint isn't on a push service Fold sends to")

// ErrBlockedAddress: a push host resolved to a private, loopback,
// link-local, multicast or unspecified address.
var ErrBlockedAddress = errors.New("push: the push service resolved to an address Fold doesn't send to")

// maxEndpoint matches the push_subscriptions CHECK.
const maxEndpoint = 2048

// AllowList is the set of push-service hosts Fold sends to.
type AllowList struct{ hosts []string }

// NewAllowList is DefaultHosts plus extra (FOLD_PUSH_HOSTS), which must
// already be valid host names (ParseHosts).
func NewAllowList(extra ...string) *AllowList {
	hosts := append([]string{}, DefaultHosts...)
	for _, h := range extra {
		h = strings.ToLower(h)
		if !slices.Contains(hosts, h) {
			hosts = append(hosts, h)
		}
	}
	return &AllowList{hosts: hosts}
}

// Hosts lists the allow-listed hosts.
func (l *AllowList) Hosts() []string { return append([]string{}, l.hosts...) }

// ParseHosts reads FOLD_PUSH_HOSTS: comma-separated host names, each a
// plain lowercase ASCII DNS name (no scheme, port, IP or trailing dot).
func ParseHosts(s string) ([]string, error) {
	var out []string
	for _, h := range strings.Split(s, ",") {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		if !validHost(h) {
			return nil, fmt.Errorf("FOLD_PUSH_HOSTS: %q is not a host name (write it like push.example.com)", h)
		}
		out = append(out, h)
	}
	return out, nil
}

// validHost: a lowercase ASCII DNS name with at least one dot, no IP
// literal, no trailing dot, and no punycode (IDN) label.
func validHost(h string) bool {
	if h == "" || len(h) > 253 || strings.HasSuffix(h, ".") || !strings.Contains(h, ".") {
		return false
	}
	if _, err := netip.ParseAddr(h); err == nil {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "xn--") ||
			strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	// An all-numeric last label is an IP in disguise (http://2130706433/).
	last := h[strings.LastIndex(h, ".")+1:]
	return strings.Trim(last, "0123456789") != ""
}

// Allowed reports ErrNotAllowed unless endpoint is https on port 443 (or
// none), with no userinfo, on an allow-listed host or a dot-boundary
// subdomain of one.
func (l *AllowList) Allowed(endpoint string) error {
	if len(endpoint) > maxEndpoint {
		return ErrNotAllowed
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" {
		return ErrNotAllowed
	}
	if p := u.Port(); p != "" && p != "443" {
		return ErrNotAllowed
	}
	host := u.Hostname()
	// Hostname lowercases nothing; refuse anything that isn't already a
	// plain lowercase name, so the match below is exact.
	if !validHost(host) {
		return ErrNotAllowed
	}
	for _, h := range l.hosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return nil
		}
	}
	return ErrNotAllowed
}

// publicAddr reports whether ip is one Fold may connect to.
func publicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// blockedPrefixes are special ranges the netip predicates don't cover.
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // "this network"
	netip.MustParsePrefix("100.64.0.0/10"),   // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved, and broadcast
	netip.MustParsePrefix("100::/64"),        // discard
	netip.MustParsePrefix("2001::/32"),       // Teredo, which tunnels to IPv4
	netip.MustParsePrefix("2001:db8::/32"),   // documentation
	netip.MustParsePrefix("2002::/16"),       // 6to4, which tunnels to IPv4
	netip.MustParsePrefix("fec0::/10"),       // old site-local
	netip.MustParsePrefix("::/96"),           // IPv4-compatible, which embeds IPv4
	netip.MustParsePrefix("64:ff9b::/96"),    // NAT64, which reaches IPv4 (incl. private)
	netip.MustParsePrefix("64:ff9b:1::/48"),  // local-use NAT64
	netip.MustParsePrefix("192.0.2.0/24"),    // documentation (TEST-NET-1)
	netip.MustParsePrefix("198.51.100.0/24"), // documentation (TEST-NET-2)
	netip.MustParsePrefix("203.0.113.0/24"),  // documentation (TEST-NET-3)
	netip.MustParsePrefix("192.88.99.0/24"),  // old 6to4 relay anycast
}

// refuseInternal is the dialer's Control hook: it runs after DNS, on the
// address actually being connected to, so a host that resolves (or
// rebinds) to an internal address is refused.
func refuseInternal(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return ErrBlockedAddress
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !publicAddr(ip) {
		return ErrBlockedAddress
	}
	return nil
}

// Dialer is the TCP dialer push uses: it refuses internal addresses.
func Dialer() *net.Dialer {
	return &net.Dialer{Timeout: 5 * time.Second, Control: refuseInternal}
}

// NewHTTPClient is the outbound client for push services: no proxy (so
// HTTPS_PROXY can't route around the address check), the internal-address
// dialer, no redirects, and a 10-second limit.
func NewHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           Dialer().DialContext,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       90 * time.Second,
	}
	return &http.Client{
		Transport:     transport,
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// probeHosts maps an allow-listed suffix to a name Check can connect to:
// the services serve endpoints on subdomains, and the bare names of some
// don't resolve. Safari's endpoints are on web.push.apple.com and Edge's
// on wnsN-<region>.notify.windows.com.
var probeHosts = map[string]string{
	"push.apple.com":     "web.push.apple.com",
	"notify.windows.com": "wns2-bl2p.notify.windows.com",
}

// CheckResult is one host's reachability.
type CheckResult struct {
	Host string `json:"host"`
	OK   bool   `json:"ok"`
	// Error says what failed, in the dialer's words.
	Error string `json:"error,omitempty"`
}

// DialFunc dials a TCP address.
type DialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// Check resolves and TCP-connects to each host on 443 with dial (nil: the
// push Dialer), so an operator can see which push services the server
// can reach (fold push check).
func Check(ctx context.Context, hosts []string, dial DialFunc) []CheckResult {
	if dial == nil {
		dial = Dialer().DialContext
	}
	out := make([]CheckResult, 0, len(hosts))
	for _, h := range hosts {
		probe := h
		if p, ok := probeHosts[h]; ok {
			probe = p
		}
		r := CheckResult{Host: probe}
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		conn, err := dial(cctx, "tcp", net.JoinHostPort(probe, "443"))
		cancel()
		if err != nil {
			r.Error = err.Error()
		} else {
			r.OK = true
			_ = conn.Close()
		}
		out = append(out, r)
	}
	return out
}
