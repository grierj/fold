package mail

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"

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

func TestNoSenderFails(t *testing.T) {
	if err := (NoSender{}).Send(context.Background(), Message{To: "a@b.c"}); err == nil {
		t.Fatal("NoSender sent")
	}
}
