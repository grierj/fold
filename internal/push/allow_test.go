package push

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAllowedEndpoints(t *testing.T) {
	l := NewAllowList("push.example.org")
	for _, ok := range []string{
		"https://fcm.googleapis.com/fcm/send/abc",
		"https://fcm.googleapis.com:443/fcm/send/abc",
		"https://android.googleapis.com/gcm/send/abc",
		"https://updates.push.services.mozilla.com/wpush/v2/abc",
		"https://web.push.apple.com/QABC",
		"https://wns2-bl2p.notify.windows.com/w/?token=abc",
		"https://push.example.org/abc",
		"https://eu.push.example.org/abc",
	} {
		if err := l.Allowed(ok); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"http://fcm.googleapis.com/fcm/send/abc",                  // not https
		"https://fcm.googleapis.com:8443/fcm/send/abc",            // port
		"https://user:pw@fcm.googleapis.com/fcm/send/abc",         // userinfo
		"https://evilfcm.googleapis.com/x",                        // not a dot boundary
		"https://fcm.googleapis.com.evil.example/x",               // suffix trick
		"https://fcm.googleapis.com./x",                           // trailing dot
		"https://FCM.googleapis.com/x",                            // not normalized
		"https://xn--fcm-0na.googleapis.com/x",                    // punycode IDN
		"https://fcm.gooģleapis.com/x",                            // IDN
		"https://127.0.0.1/x",                                     // IP literal
		"https://[::1]/x",                                         // IPv6 literal
		"https://2130706433/x",                                    // decimal IP
		"https://localhost/x",                                     // not listed
		"https://googleapis.com/x",                                // parent of a listed host
		"https://push.samsungosp.com/x",                           // dropped: Samsung Internet uses FCM
		"fcm.googleapis.com/x",                                    // no scheme
		"https:fcm.googleapis.com/x",                              // opaque
		"https://fcm.googleapis.com/" + strings.Repeat("a", 2048), // too long
	} {
		if err := l.Allowed(bad); !errors.Is(err, ErrNotAllowed) {
			t.Errorf("%s allowed (%v)", bad, err)
		}
	}
}

func TestParseHosts(t *testing.T) {
	got, err := ParseHosts(" Push.Example.org, ,other.example.net ")
	if err != nil || strings.Join(got, ",") != "push.example.org,other.example.net" {
		t.Fatalf("%v %v", got, err)
	}
	for _, bad := range []string{"https://push.example.org", "push.example.org:443", "10.0.0.1", "localhost", "push.example.org.", "xn--bcher-kva.example", "a_b.example"} {
		if _, err := ParseHosts(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestInternalAddressesAreRefused(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "::1", "10.1.2.3", "172.16.0.1", "192.168.1.20", "169.254.169.254", "fe80::1", "fd00::1",
		"224.0.0.1", "ff02::1", "0.0.0.0", "::", "100.64.0.1", "::ffff:127.0.0.1", "255.255.255.255",
		"64:ff9b::a00:1", "64:ff9b::7f00:1", "64:ff9b:1::1", "::10.0.0.1", "::127.0.0.1",
		"192.0.2.1", "198.51.100.7", "203.0.113.9", "192.88.99.1"} {
		if publicAddr(netip.MustParseAddr(s)) {
			t.Errorf("%s is public", s)
		}
	}
	for _, s := range []string{"142.250.72.10", "2607:f8b0:4005:80b::200a", "17.57.146.20"} {
		if !publicAddr(netip.MustParseAddr(s)) {
			t.Errorf("%s refused", s)
		}
	}
}

// A loopback httptest server can't be reached: the dialer refuses the
// resolved address before connecting, whatever the host name said.
func TestDialerRefusesLoopbackServer(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	_, err := Dialer().DialContext(context.Background(), "tcp", srv.Listener.Addr().String())
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("dial: %v", err)
	}
	// Through the client, with a host name that resolves to loopback (the
	// DNS-rebinding case) and a proxy in the environment that must not be
	// used to route around the check.
	var proxied atomic.Int64
	proxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { proxied.Add(1) }))
	defer proxy.Close()
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("HTTP_PROXY", proxy.URL)
	c := &Client{Hosts: NewAllowList("localhost.test"), Key: func(context.Context) (*ecdsa.PrivateKey, error) { return GenerateKey() }}
	c.HTTP = NewHTTPClient()
	c.HTTP.Transport.(*http.Transport).DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		// localhost.test "resolves" to the loopback server.
		return Dialer().DialContext(ctx, network, srv.Listener.Addr().String())
	}
	sub := testSub()
	sub.Endpoint = "https://localhost.test/secret"
	r := c.Send(context.Background(), sub, []byte("{}"), Options{})
	if r.Status != Failed || !errors.Is(r.Err, ErrBlockedAddress) {
		t.Fatalf("result %+v", r)
	}
	if hits.Load() != 0 || proxied.Load() != 0 {
		t.Fatalf("reached the server %d times and the proxy %d", hits.Load(), proxied.Load())
	}
	if NewHTTPClient().Transport.(*http.Transport).Proxy != nil {
		t.Fatal("the push transport has a proxy")
	}
}

func TestCheckReportsEachHost(t *testing.T) {
	var dialed []string
	dial := func(_ context.Context, _, addr string) (net.Conn, error) {
		dialed = append(dialed, addr)
		if strings.HasPrefix(addr, "updates.") {
			return nil, errors.New("connection refused")
		}
		a, b := net.Pipe()
		_ = b.Close()
		return a, nil
	}
	got := Check(context.Background(), []string{"fcm.googleapis.com", "updates.push.services.mozilla.com", "push.apple.com", "notify.windows.com"}, dial)
	if len(got) != 4 || !got[0].OK || got[1].OK || got[1].Error != "connection refused" || got[2].Host != "web.push.apple.com" || !got[2].OK ||
		got[3].Host != "wns2-bl2p.notify.windows.com" || !got[3].OK {
		t.Fatalf("%+v", got)
	}
	if strings.Join(dialed, " ") != "fcm.googleapis.com:443 updates.push.services.mozilla.com:443 web.push.apple.com:443 wns2-bl2p.notify.windows.com:443" {
		t.Fatalf("dialed %v", dialed)
	}
}
