// Package mail sends email. Postmark is the default service and plain SMTP
// the fallback (DEC-21); the outbox keeps mail in memory for development and
// tests. Callers send a Message through a Sender and never see which runs.
// A future delivery channel (push or SMS, R2) sits beside Sender, not in it.
//
// Every send happens in a job, after the command that queued it committed.
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	netmail "net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Message is one email to one address: plain text, with an HTML part.
type Message struct {
	To       string
	Subject  string
	Text     string
	HTML     string
	FromName string // display name; the address never changes
}

// Sender sends one message and returns the provider's id for it.
type Sender interface {
	Send(ctx context.Context, m Message) (string, error)
}

// SendError is a provider refusing a message. Permanent means sending the
// same message again cannot help.
type SendError struct {
	Provider  string
	Message   string
	Permanent bool
}

func (e *SendError) Error() string { return e.Provider + ": " + e.Message }

type refused struct{ error }

func (r refused) Unwrap() error { return r.error }

// IsPermanent: retrying cannot help (the message was refused before sending,
// the provider said so, or an SMTP server answered 5xx).
func IsPermanent(err error) bool {
	if errors.As(err, new(refused)) {
		return true
	}
	var se *SendError
	if errors.As(err, &se) {
		return se.Permanent
	}
	var tp *textproto.Error
	return errors.As(err, &tp) && tp.Code >= 500
}

// ValidAddress: one plain address, with no display name, comma or newline.
func ValidAddress(s string) bool {
	if s == "" || len(s) > 254 || strings.ContainsAny(s, ",;\r\n<> ") {
		return false
	}
	a, err := netmail.ParseAddress(s)
	return err == nil && a.Address == s && strings.Contains(s[strings.LastIndex(s, "@")+1:], ".")
}

func check(m Message) error {
	if !ValidAddress(m.To) {
		return refused{errors.New("mail: the recipient must be one plain address")}
	}
	if utf8.RuneCountInString(m.FromName) > 200 {
		return refused{errors.New("mail: the sender name is too long")}
	}
	return nil
}

// oneLine strips line breaks from text bound for a header, so a name or a
// subject cannot add headers (docs/HAZARDS.md section 6).
func oneLine(s string) string { return strings.NewReplacer("\r", " ", "\n", " ").Replace(s) }

func fromHeader(from, name string) string {
	name = strings.TrimSpace(oneLine(name))
	if name == "" {
		return from
	}
	return (&netmail.Address{Name: name, Address: from}).String()
}

// SMTP sends through one server. Port 465 is TLS from the first byte; any
// other port must offer STARTTLS, which is required: mail never goes out in
// plain text (docs/HAZARDS.md section 7). The one exception is a server on
// this machine's loopback address, for a local mail catcher.
type SMTP struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	// tlsConfig overrides certificate checks in tests only.
	tlsConfig *tls.Config
}

func (s *SMTP) tlsCfg() *tls.Config {
	if s.tlsConfig != nil {
		return s.tlsConfig
	}
	return &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}
}

func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *SMTP) Send(ctx context.Context, m Message) (string, error) {
	if err := check(m); err != nil {
		return "", err
	}
	if !ValidAddress(s.From) {
		return "", refused{errors.New("mail: the From address is not one plain address")}
	}
	msgID, err := messageID(s.From)
	if err != nil {
		return "", err
	}
	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return "", fmt.Errorf("mail: could not reach %s: %w", addr, err)
	}
	deadline := time.Now().Add(30 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	socket := conn
	stop := context.AfterFunc(ctx, func() { socket.Close() })
	defer stop()
	implicit := s.Port == 465
	if implicit {
		tc := tls.Client(conn, s.tlsCfg())
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return "", fmt.Errorf("mail: TLS with %s: %w", addr, err)
		}
		conn = tc
	}
	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		conn.Close()
		return "", fmt.Errorf("mail: %w", err)
	}
	defer c.Close()
	if !implicit {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(s.tlsCfg()); err != nil {
				return "", fmt.Errorf("mail: STARTTLS with %s: %w", addr, err)
			}
		} else if !loopback(s.Host) {
			return "", &SendError{Provider: "smtp", Permanent: true,
				Message: "the mail server does not offer encryption (STARTTLS); use port 465 or 587 on a server that does"}
		}
	}
	if s.Username != "" {
		if ok, _ := c.Extension("AUTH"); !ok {
			return "", &SendError{Provider: "smtp", Permanent: true, Message: "the mail server does not accept a username and password"}
		}
		if err := c.Auth(smtp.PlainAuth("", s.Username, s.Password, s.Host)); err != nil {
			return "", fmt.Errorf("mail: the mail server refused the username or password: %w", err)
		}
	}
	if err := c.Mail(s.From); err != nil {
		return "", fmt.Errorf("mail: from: %w", err)
	}
	if err := c.Rcpt(m.To); err != nil {
		return "", fmt.Errorf("mail: recipient: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return "", fmt.Errorf("mail: data: %w", err)
	}
	contentType, body, err := mimeBody(m)
	if err != nil {
		w.Close()
		return "", err
	}
	subject := mime.QEncoding.Encode("utf-8", oneLine(m.Subject))
	if _, err := fmt.Fprintf(w, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: %s\r\nMIME-Version: 1.0\r\n%s\r\n%s\r\n",
		fromHeader(s.From, m.FromName), m.To, subject, time.Now().Format(time.RFC1123Z), msgID, contentType, body); err != nil {
		w.Close()
		return "", fmt.Errorf("mail: write: %w", err)
	}
	if err := w.Close(); err != nil {
		return "", fmt.Errorf("mail: send: %w", err)
	}
	// The server took the message when DATA closed; a failed QUIT must not
	// make the job send it again.
	_ = c.Quit()
	return msgID, nil
}

func messageID(from string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	domain := from[strings.LastIndex(from, "@")+1:]
	return "<" + hex.EncodeToString(b) + "@" + domain + ">", nil
}

// mimeBody is multipart/alternative, text first, both quoted-printable so a
// long HTML line never breaks SMTP's line limit.
func mimeBody(m Message) (string, string, error) {
	if m.HTML == "" {
		return "Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n", qp(m.Text), nil
	}
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	for _, part := range []struct{ typ, body string }{{"text/plain", m.Text}, {"text/html", m.HTML}} {
		pw, err := w.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {part.typ + "; charset=utf-8"},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		if err != nil {
			return "", "", err
		}
		if _, err := pw.Write([]byte(qp(part.body))); err != nil {
			return "", "", err
		}
	}
	if err := w.Close(); err != nil {
		return "", "", err
	}
	return "Content-Type: multipart/alternative; boundary=\"" + w.Boundary() + "\"\r\n", b.String(), nil
}

func qp(s string) string {
	var b bytes.Buffer
	w := quotedprintable.NewWriter(&b)
	w.Write([]byte(s))
	w.Close()
	return b.String()
}
