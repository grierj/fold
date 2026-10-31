package mail

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"mime"
	"net"
	netmail "net/mail"
	"net/textproto"
	"os"
	"strings"
	"testing"
	"time"

	"fold/internal/providers"
	"fold/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

// TestDevOutboxStoresMail: the dev/test sender stores mail in the database.
func TestDevOutboxStoresMail(t *testing.T) {
	db := testdb.Shared(t)
	outbox := NewDevOutbox(db, "Fold <noreply@example.com>")
	if err := outbox.Send(context.Background(), Message{
		To: "pat@example.com", Subject: "Reset your Fold password", Text: "link here",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := outbox.LatestTo("pat@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got.Subject != "Reset your Fold password" || !strings.Contains(got.Body, "link here") {
		t.Fatalf("stored: %+v", got)
	}
	list, err := outbox.List(10)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %d", err, len(list))
	}
}

// fakeSMTP is a minimal test SMTP server: it greets, answers EHLO without
// advertising STARTTLS or AUTH, and records one message.
type fakeSMTP struct {
	t    *testing.T
	ln   net.Listener
	from string
	to   string
	data string
	done chan struct{}
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{t: t, ln: ln, done: make(chan struct{})}
	go f.serve()
	t.Cleanup(func() { ln.Close() })
	return f
}

func (f *fakeSMTP) addr() string { return f.ln.Addr().String() }

func (f *fakeSMTP) serve() {
	defer close(f.done)
	conn, err := f.ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	line := func() string {
		s, err := r.ReadString('\n')
		if err != nil {
			return ""
		}
		return strings.TrimRight(s, "\r\n")
	}
	say := func(s string) { fmt.Fprintf(w, "%s\r\n", s); w.Flush() }
	say("220 fake ESMTP")
	for {
		cmd := line()
		if cmd == "" {
			return
		}
		upper := strings.ToUpper(cmd)
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			say("250-fake")
			say("250 8BITMIME")
		case strings.HasPrefix(upper, "MAIL FROM:"):
			f.from = strings.TrimPrefix(cmd, "MAIL FROM:")
			say("250 ok")
		case strings.HasPrefix(upper, "RCPT TO:"):
			f.to = strings.TrimPrefix(cmd, "RCPT TO:")
			say("250 ok")
		case upper == "DATA":
			say("354 go ahead")
			var b strings.Builder
			for {
				l := line()
				if l == "." {
					break
				}
				b.WriteString(l + "\n")
			}
			f.data = b.String()
			say("250 queued")
		case upper == "QUIT":
			say("221 bye")
			return
		case upper == "RSET":
			say("250 ok")
		default:
			say("502 unimplemented")
		}
	}
}

// TestSMTPSendsThroughTestServer: the SMTP sender delivers through a test
// SMTP server (the CI check in M1.2's acceptance).
func TestSMTPSendsThroughTestServer(t *testing.T) {
	fake := newFakeSMTP(t)
	s, err := NewSMTP("smtp://"+fake.addr(), "Fold <noreply@example.com>")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), Message{
		To: "sam@example.com", Subject: "Hello", Text: "plain body",
	}); err != nil {
		t.Fatal(err)
	}
	<-fake.done
	if !strings.Contains(fake.to, "sam@example.com") {
		t.Fatalf("rcpt: %q", fake.to)
	}
	if !strings.Contains(fake.data, "Subject: Hello") || !strings.Contains(fake.data, "plain body") {
		t.Fatalf("data: %q", fake.data)
	}
	if !strings.Contains(fake.data, "Content-Type: text/plain") {
		t.Fatalf("not plain text: %q", fake.data)
	}
}

func TestSMTPRequiresEncryptionOutsideLoopback(t *testing.T) {
	for _, allow := range []bool{false, true} {
		t.Run(fmt.Sprintf("allow-insecure=%v", allow), func(t *testing.T) {
			fake := newFakeSMTP(t)
			s, err := NewSMTP("smtp://relay.example.com:587", "Fold <noreply@example.com>")
			if err != nil {
				t.Fatal(err)
			}
			// Connect to the fixture but retain the configured non-loopback
			// identity used for TLS policy and certificate verification.
			s.addr = fake.addr()
			s.AllowInsecure = allow
			err = s.Send(context.Background(), Message{To: "sam@example.com", Subject: "Secret link", Text: "token"})
			<-fake.done
			if allow {
				if err != nil || !strings.Contains(fake.data, "token") {
					t.Fatalf("explicit opt-out: err=%v data=%q", err, fake.data)
				}
			} else if err == nil || !strings.Contains(err.Error(), "STARTTLS") || fake.from != "" || fake.to != "" || fake.data != "" {
				t.Fatalf("plaintext escaped: err=%v from=%q to=%q data=%q", err, fake.from, fake.to, fake.data)
			}
		})
	}
}

func TestNewSMTPRejects(t *testing.T) {
	for _, raw := range []string{"", "http://x", "smtp://", "smtp://h:notaport"} {
		if _, err := NewSMTP(raw, "f"); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}

func TestFromAddress(t *testing.T) {
	if got := fromAddress("Fold <noreply@example.com>"); got != "noreply@example.com" {
		t.Fatalf("got %q", got)
	}
	if got := fromAddress("noreply@example.com"); got != "noreply@example.com" {
		t.Fatalf("got %q", got)
	}
}

// testCert makes a self-signed certificate for 127.0.0.1, for the TLS
// fake SMTP servers below.
func testCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// fakeTLSSMTP serves one SMTP session, optionally with implicit TLS
// (TLS from the first byte) or a STARTTLS upgrade after the first EHLO.
type fakeTLSSMTP struct {
	ln   net.Listener
	cert tls.Certificate
	from string
	to   string
	data string
	done chan struct{}
}

func newFakeTLSSMTP(t *testing.T, implicit bool) *fakeTLSSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeTLSSMTP{ln: ln, cert: testCert(t), done: make(chan struct{})}
	go f.serve(implicit)
	t.Cleanup(func() { ln.Close() })
	return f
}

// rootPool trusts the fake server's self-signed certificate, so tests
// exercise the sender's real certificate verification (ServerName check
// included) instead of skipping it.
func (f *fakeTLSSMTP) rootPool(t *testing.T) *x509.CertPool {
	t.Helper()
	leaf, err := x509.ParseCertificate(f.cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return pool
}

func (f *fakeTLSSMTP) addr() string { return f.ln.Addr().String() }

func (f *fakeTLSSMTP) serve(implicit bool) {
	defer close(f.done)
	conn, err := f.ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	tlsCfg := &tls.Config{Certificates: []tls.Certificate{f.cert}}
	if implicit {
		tconn := tls.Server(conn, tlsCfg)
		if err := tconn.Handshake(); err != nil {
			return
		}
		conn = tconn
	}
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	line := func() string {
		s, err := r.ReadString('\n')
		if err != nil {
			return ""
		}
		return strings.TrimRight(s, "\r\n")
	}
	say := func(s string) { fmt.Fprintf(w, "%s\r\n", s); w.Flush() }
	say("220 fake ESMTP")
	tlsActive := implicit
	for {
		cmd := line()
		upper := strings.ToUpper(cmd)
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			if !tlsActive {
				say("250-fake")
				say("250 STARTTLS")
			} else {
				say("250 fake")
			}
		case upper == "STARTTLS":
			say("220 go ahead")
			tconn := tls.Server(conn, tlsCfg)
			if err := tconn.Handshake(); err != nil {
				return
			}
			conn = tconn
			r = bufio.NewReader(conn)
			w = bufio.NewWriter(conn)
			tlsActive = true
		case strings.HasPrefix(upper, "MAIL FROM:"):
			f.from = strings.TrimPrefix(cmd, "MAIL FROM:")
			say("250 ok")
		case strings.HasPrefix(upper, "RCPT TO:"):
			f.to = strings.TrimPrefix(cmd, "RCPT TO:")
			say("250 ok")
		case upper == "DATA":
			say("354 go ahead")
			var b strings.Builder
			for {
				l := line()
				if l == "." {
					break
				}
				b.WriteString(l + "\n")
			}
			f.data = b.String()
			say("250 queued")
		case upper == "QUIT":
			say("221 bye")
			return
		default:
			say("502 unimplemented")
		}
	}
}

func testSender(t *testing.T, rawURL string, fake *fakeTLSSMTP) *SMTPSender {
	t.Helper()
	s, err := NewSMTP(rawURL, "Fold <noreply@example.com>")
	if err != nil {
		t.Fatal(err)
	}
	// Verify like production (ServerName = host), trusting the fake's
	// self-signed certificate as the root.
	s.tlsConfig = &tls.Config{RootCAs: fake.rootPool(t), ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12}
	return s
}

// The smtp:// sender must upgrade with STARTTLS when the server offers it
// (before the fix it passed a nil TLS config and every upgrade failed).
func TestSMTPStartTLS(t *testing.T) {
	fake := newFakeTLSSMTP(t, false)
	s := testSender(t, "smtp://"+fake.addr(), fake)
	if err := s.Send(context.Background(), Message{
		To: "sam@example.com", Subject: "Hello", Text: "plain body",
	}); err != nil {
		t.Fatal(err)
	}
	<-fake.done
	if !strings.Contains(fake.data, "Subject: Hello") || !strings.Contains(fake.data, "plain body") {
		t.Fatalf("data: %q", fake.data)
	}
}

// The smtps:// sender must speak TLS from the first byte (before the fix
// it sent plaintext SMTP to the TLS port and could never deliver).
func TestSMTPImplicitTLS(t *testing.T) {
	fake := newFakeTLSSMTP(t, true)
	s := testSender(t, "smtps://"+fake.addr(), fake)
	if err := s.Send(context.Background(), Message{
		To: "sam@example.com", Subject: "Hello", Text: "plain body",
	}); err != nil {
		t.Fatal(err)
	}
	<-fake.done
	if !strings.Contains(fake.to, "sam@example.com") || !strings.Contains(fake.data, "plain body") {
		t.Fatalf("to=%q data=%q", fake.to, fake.data)
	}
}

// A newline in a header value must never reach the wire: recipients are
// rejected, and subjects have CR/LF flattened.
func TestSMTPHeaderInjection(t *testing.T) {
	s, err := NewSMTP("smtp://127.0.0.1:1", "Fold <noreply@example.com>")
	if err != nil {
		t.Fatal(err)
	}
	err = s.Send(context.Background(), Message{
		To: "a@example.com\r\nBcc: victim@example.com", Subject: "Hi", Text: "x",
	})
	if err == nil || !strings.Contains(err.Error(), "newline") {
		t.Fatalf("newline recipient accepted: %v", err)
	}
	fake := newFakeTLSSMTP(t, true)
	s2 := testSender(t, "smtps://"+fake.addr(), fake)
	if err := s2.Send(context.Background(), Message{
		To: "sam@example.com", Subject: "Hi\r\nBcc: victim@example.com", Text: "body",
	}); err != nil {
		t.Fatal(err)
	}
	<-fake.done
	if strings.Contains(fake.data, "\nBcc:") || !strings.Contains(fake.data, "Subject: Hi  Bcc:") {
		t.Fatalf("subject not flattened: %q", fake.data)
	}
}

func TestNoSenderFails(t *testing.T) {
	if err := (NoSender{}).Send(context.Background(), Message{To: "a@b.c"}); err == nil || !NotConfigured(err) {
		t.Fatal("NoSender sent")
	}
	if NotConfigured(errors.New("other")) {
		t.Fatal("any error is not configured")
	}
}

// Messages carry Date and a random Message-ID in the sender's domain, and a
// non-ASCII subject travels as an RFC 2047 encoded word.
func TestSMTPAddsDateMessageIDAndEncodesSubject(t *testing.T) {
	fake := newFakeSMTP(t)
	s, err := NewSMTP("smtp://"+fake.addr(), "Fold <noreply@example.com>")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), Message{
		To: "sam@example.com", Subject: "Café night", Text: "plain body",
	}); err != nil {
		t.Fatal(err)
	}
	<-fake.done
	msg, err := netmail.ReadMessage(strings.NewReader(fake.data))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := msg.Header.Date(); err != nil {
		t.Fatalf("Date header: %v", err)
	}
	if id := msg.Header.Get("Message-ID"); !strings.HasPrefix(id, "<") || !strings.HasSuffix(id, "@example.com>") {
		t.Fatalf("Message-ID: %q", id)
	}
	raw := msg.Header.Get("Subject")
	if !strings.HasPrefix(raw, "=?utf-8?q?") {
		t.Fatalf("subject not encoded: %q", raw)
	}
	if got, err := new(mime.WordDecoder).DecodeHeader(raw); err != nil || got != "Café night" {
		t.Fatalf("decoded subject %q, %v", got, err)
	}
}

// What a retry can't fix is permanent: a message refused before sending,
// a provider's refusal, an SMTP 5xx; a 4xx or a timeout may pass (M3.5).
func TestIsPermanent(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{checkMessage(Message{To: "a@example.com, b@example.com"}), true},
		{&providers.SendError{Permanent: true}, true},
		{&providers.SendError{Permanent: false}, false},
		{fmt.Errorf("mail: rcpt: %w", &textproto.Error{Code: 550, Msg: "no such user"}), true},
		{fmt.Errorf("mail: rcpt: %w", &textproto.Error{Code: 451, Msg: "try later"}), false},
		{errors.New("mail: dial: timeout"), false},
	} {
		if got := IsPermanent(tc.err); got != tc.want {
			t.Errorf("%v: %v, want %v", tc.err, got, tc.want)
		}
	}
}
