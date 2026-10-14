package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func prodApp() *App { return &App{Config: Config{Env: "prod"}} }

func TestIPAllowedBlocksNonGlobalRanges(t *testing.T) {
	a := prodApp()
	blocked := []string{
		"127.0.0.1", "10.0.0.1", "172.16.5.4", "192.168.1.1", "169.254.169.254", "100.64.0.1",
		"0.0.0.0", "0.1.2.3", "255.255.255.255", "240.0.0.1", "224.0.0.1", "198.18.0.1", "192.0.0.170",
		"::", "::1", "fc00::1", "fd12:3456::1", "fe80::1", "ff02::1", "fec0::1",
		"::ffff:127.0.0.1", "::ffff:169.254.169.254", "::ffff:10.0.0.1", "64:ff9b::a9fe:a9fe",
		"2002:7f00:1::1", "2001::1", "2001:db8::1",
	}
	for _, s := range blocked {
		if a.ipAllowed(net.ParseIP(s)) {
			t.Errorf("%s allowed", s)
		}
	}
	for _, s := range []string{"93.184.216.34", "1.1.1.1", "2606:4700:4700::1111"} {
		if !a.ipAllowed(net.ParseIP(s)) {
			t.Errorf("%s refused", s)
		}
	}
	// Dev only opens loopback, nothing else.
	dev := &App{Config: Config{Env: "dev"}}
	if !dev.ipAllowed(net.ParseIP("127.0.0.1")) || dev.ipAllowed(net.ParseIP("169.254.169.254")) {
		t.Error("dev loopback exception is wrong")
	}
}

func TestFetchRefusesOddHostsSchemesAndPorts(t *testing.T) {
	a := prodApp()
	for _, raw := range []string{
		"http://2130706433/x.png", "http://0177.0.0.1/x.png", "http://0x7f.1/x.png", "http://127.1/x.png",
		"http://[::1]/x.png", "http://[::ffff:127.0.0.1]/x.png", "http://0.0.0.0/x.png",
		"http://169.254.169.254/latest/meta-data/", "http://localhost/x.png", "http://a.localhost/x.png",
		"file:///etc/passwd", "gopher://example.com/", "ftp://example.com/x.png",
		"http://user:pw@example.com/x.png", "http://example.com:22/x.png", "https://example.com:6379/x.png",
	} {
		if _, _, err := a.fetchRemoteImage(context.Background(), raw); err == nil {
			t.Errorf("%s fetched", raw)
		}
	}
}

func TestNumericHost(t *testing.T) {
	for _, h := range []string{"2130706433", "0177.0.0.1", "0x7f.1", "127.1"} {
		if !numericHost(h) {
			t.Errorf("%s not numeric", h)
		}
	}
	for _, h := range []string{"example.com", "1e100.net", "deadbeef", "cdn1.example"} {
		if numericHost(h) {
			t.Errorf("%s numeric", h)
		}
	}
}

// The dialer's Control hook is the last check; it must refuse a blocked
// socket address no matter how it was produced (e.g. a DNS rebind).
func TestDialControlRefusesBlockedAddress(t *testing.T) {
	a := prodApp()
	for _, addr := range []string{"127.0.0.1:80", "[::1]:443", "[::ffff:10.0.0.1]:80", "169.254.169.254:80"} {
		if err := a.dialControl("tcp", addr, nil); err == nil {
			t.Errorf("%s dialed", addr)
		}
	}
	if err := a.dialControl("tcp", "93.184.216.34:443", nil); err != nil {
		t.Errorf("public refused: %v", err)
	}
}

func TestFetchRefusesRedirectToMetadata(t *testing.T) {
	a := &App{Config: Config{Env: "dev"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer srv.Close()
	if _, _, err := a.fetchRemoteImage(context.Background(), srv.URL+"/pic.png"); err == nil {
		t.Fatal("redirect to metadata followed")
	}
}

func TestFetchRefusesNonImage(t *testing.T) {
	a := &App{Config: Config{Env: "dev"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/html":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html>secret</html>"))
		case "/liar":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("<html>not a png</html>"))
		case "/huge":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte(strings.Repeat("x", maxUploadBytes+10)))
		}
	}))
	defer srv.Close()
	for _, p := range []string{"/html", "/liar", "/huge"} {
		if _, _, err := a.fetchRemoteImage(context.Background(), srv.URL+p); err == nil {
			t.Errorf("%s accepted", p)
		}
	}
}

func TestURLAllowedPortsOnlyRelaxedInDev(t *testing.T) {
	u, _ := url.Parse("http://example.com:8080/x.png")
	if prodApp().urlAllowed(u) == nil {
		t.Error("prod allowed :8080")
	}
	if (&App{Config: Config{Env: "dev"}}).urlAllowed(u) != nil {
		t.Error("dev refused :8080")
	}
}

// Blast-radius proof: even if a resolver hands back a blocked address (a
// DNS rebind), the real dialer refuses to connect to it.
func TestDialerWithControlRefusesLoopbackSocket(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		if c, err := ln.Accept(); err == nil {
			c.Close()
		}
	}()
	a := prodApp()
	d := &net.Dialer{Control: a.dialControl}
	if c, err := d.Dial("tcp", ln.Addr().String()); err == nil {
		c.Close()
		t.Fatal("dialed loopback in prod")
	}
	dev := &App{Config: Config{Env: "dev"}}
	c, err := (&net.Dialer{Control: dev.dialControl}).Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dev loopback refused: %v", err)
	}
	c.Close()
}

func TestCleanCoverURL(t *testing.T) {
	for _, ok := range []string{"", "https://example.com/a.jpg", "/uploads/covers/1/ab12.png"} {
		if _, err := cleanCoverURL(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"javascript:alert(1)", "http://example.com/a.jpg", "data:image/png;base64,AA", "https://u:p@example.com/a.jpg", "/etc/passwd"} {
		if _, err := cleanCoverURL(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
