// Package mail is Fold's minimal transactional email sender (C6, 5.6).
//
// Before the provider registry arrives (M3.2) there are two senders:
// devoutbox, which stores mail in the database for dev and tests (shown at
// /dev/outbox), and SMTP, configured from FOLD_SMTP_URL. M3.2 moves both
// behind the registry without changing callers. Templates are plain text.
package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"time"

	"fold/models"

	"gorm.io/gorm"
)

// Message is one transactional email.
type Message struct {
	To      string // one address
	Subject string
	Text    string // plain-text body
}

// Sender sends one transactional email.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// ---------------------------------------------------------------------------
// devoutbox: the dev and test sender (5.6). Mail is stored in the database
// and shown at /dev/outbox; tests read links from it.
// ---------------------------------------------------------------------------

// DevOutbox stores sent mail in the mail_outboxes table.
type DevOutbox struct {
	DB   *gorm.DB
	From string
}

// NewDevOutbox returns a DevOutbox writing to db.
func NewDevOutbox(db *gorm.DB, from string) *DevOutbox {
	return &DevOutbox{DB: db, From: from}
}

// Send stores m in the mail_outboxes table.
func (d *DevOutbox) Send(ctx context.Context, m Message) error {
	if strings.TrimSpace(m.To) == "" {
		return errors.New("mail: no recipient")
	}
	return d.DB.WithContext(ctx).Create(&models.MailOutbox{
		ToAddress:   m.To,
		FromAddress: d.From,
		Subject:     m.Subject,
		Body:        m.Text,
	}).Error
}

// OutboxMessage is one stored email, newest first.
type OutboxMessage struct {
	ID        uint
	To        string
	Subject   string
	Body      string
	CreatedAt time.Time
}

// List returns the newest stored emails, for /dev/outbox and tests.
func (d *DevOutbox) List(limit int) ([]OutboxMessage, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var rows []models.MailOutbox
	if err := d.DB.Order("id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]OutboxMessage, 0, len(rows))
	for _, r := range rows {
		out = append(out, OutboxMessage{
			ID: r.ID, To: r.ToAddress, Subject: r.Subject,
			Body: r.Body, CreatedAt: r.CreatedAt,
		})
	}
	return out, nil
}

// LatestTo returns the newest email to address, for tests reading links
// out of the devoutbox.
func (d *DevOutbox) LatestTo(address string) (OutboxMessage, error) {
	var r models.MailOutbox
	err := d.DB.Where("to_address = ?", address).Order("id DESC").First(&r).Error
	if err != nil {
		return OutboxMessage{}, err
	}
	return OutboxMessage{ID: r.ID, To: r.ToAddress, Subject: r.Subject, Body: r.Body, CreatedAt: r.CreatedAt}, nil
}

// ---------------------------------------------------------------------------
// SMTP: the universal fallback sender (5.6, 12.1), from FOLD_SMTP_URL.
// ---------------------------------------------------------------------------

// SMTPSender sends through one SMTP server, e.g.
// smtp://user:pass@smtp.example.org:587 (STARTTLS) or
// smtps://user:pass@smtp.example.org:465 (implicit TLS).
type SMTPSender struct {
	host     string
	addr     string
	username string
	password string
	from     string
	implicit bool
	// AllowInsecure permits plaintext to a non-loopback relay that does not
	// advertise STARTTLS. It never disables TLS certificate verification.
	AllowInsecure bool
	// tlsConfig overrides the TLS client config (tests only); production
	// verifies the server certificate against host.
	tlsConfig *tls.Config
}

// tlsCfg is the TLS client config for this server: certificate verification
// against the configured host, TLS 1.2 or newer.
func (s *SMTPSender) tlsCfg() *tls.Config {
	if s.tlsConfig != nil {
		return s.tlsConfig
	}
	return &tls.Config{ServerName: s.host, MinVersion: tls.VersionTLS12}
}

// NewSMTP parses rawURL (a FOLD_SMTP_URL value) into a sender.
func NewSMTP(rawURL, from string) (*SMTPSender, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("mail: bad FOLD_SMTP_URL: %w", err)
	}
	var implicit bool
	switch strings.ToLower(u.Scheme) {
	case "smtp":
	case "smtps":
		implicit = true
	default:
		return nil, fmt.Errorf("mail: FOLD_SMTP_URL scheme must be smtp or smtps")
	}
	host := u.Hostname()
	if host == "" {
		return nil, errors.New("mail: FOLD_SMTP_URL needs a host")
	}
	port := u.Port()
	if port == "" {
		port = "587"
		if implicit {
			port = "465"
		}
	}
	if _, err := strconv.Atoi(port); err != nil {
		return nil, fmt.Errorf("mail: bad port in FOLD_SMTP_URL: %w", err)
	}
	var username, password string
	if u.User != nil {
		username = u.User.Username()
		password, _ = u.User.Password()
	}
	return &SMTPSender{
		host:     host,
		addr:     net.JoinHostPort(host, port),
		username: username,
		password: password,
		from:     from,
		implicit: implicit,
	}, nil
}

// headerSafe reports whether v may appear in a message header: no CR or
// LF, which would let a crafted value inject extra headers.
func headerSafe(v string) bool {
	return !strings.ContainsAny(v, "\r\n")
}

// Send delivers m through the SMTP server.
func (s *SMTPSender) Send(ctx context.Context, m Message) error {
	if strings.TrimSpace(m.To) == "" {
		return errors.New("mail: no recipient")
	}
	if !headerSafe(m.To) {
		return errors.New("mail: recipient contains a newline")
	}
	from := s.from
	if from == "" {
		return errors.New("mail: no from address (set FOLD_EMAIL_FROM)")
	}
	if !headerSafe(from) {
		return errors.New("mail: from address contains a newline")
	}
	// Subjects come from templates that interpolate names; a newline in one
	// must never become a header boundary.
	subject := strings.NewReplacer("\r", " ", "\n", " ").Replace(m.Subject)
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", s.addr)
	if err != nil {
		return fmt.Errorf("mail: dial: %w", err)
	}
	// A stuck server must not block the calling handler forever.
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	if s.implicit {
		// smtps://: TLS from the first byte, before any SMTP greeting.
		tconn := tls.Client(conn, s.tlsCfg())
		if err := tconn.HandshakeContext(ctx); err != nil {
			conn.Close()
			return fmt.Errorf("mail: tls: %w", err)
		}
		conn = tconn
	}
	c, err := smtp.NewClient(conn, s.host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("mail: smtp: %w", err)
	}
	defer c.Close()
	if !s.implicit {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(s.tlsCfg()); err != nil {
				return fmt.Errorf("mail: starttls: %w", err)
			}
		} else if !s.AllowInsecure && !strings.EqualFold(s.host, "localhost") && !net.ParseIP(s.host).IsLoopback() {
			return errors.New("mail: server does not offer STARTTLS; use an encrypted SMTP relay or explicitly set FOLD_SMTP_ALLOW_INSECURE=true for a trusted relay")
		}
	}
	if s.username != "" {
		auth := smtp.PlainAuth("", s.username, s.password, s.host)
		if ok, _ := c.Extension("AUTH"); !ok {
			return errors.New("mail: server does not offer AUTH")
		}
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("mail: auth: %w", err)
		}
	}
	if err := c.Mail(fromAddress(from)); err != nil {
		return fmt.Errorf("mail: from: %w", err)
	}
	if err := c.Rcpt(m.To); err != nil {
		return fmt.Errorf("mail: rcpt: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("mail: data: %w", err)
	}
	if _, err := fmt.Fprintf(w, "From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n",
		from, m.To, subject, m.Text); err != nil {
		w.Close()
		return fmt.Errorf("mail: write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mail: send: %w", err)
	}
	return c.Quit()
}

// fromAddress strips a display name ("Fold <a@b>") to the bare address for
// the SMTP envelope.
func fromAddress(from string) string {
	if i := strings.LastIndex(from, "<"); i >= 0 {
		if j := strings.Index(from[i:], ">"); j >= 0 {
			return strings.TrimSpace(from[i+1 : i+j])
		}
	}
	return strings.TrimSpace(from)
}

// ---------------------------------------------------------------------------
// Unconfigured: prod without FOLD_SMTP_URL fails loudly instead of
// silently dropping password resets and invitations.
// ---------------------------------------------------------------------------

// NoSender errors on every send, telling the operator what to set.
type NoSender struct{}

// Send always fails: there is no sender configured.
func (NoSender) Send(context.Context, Message) error {
	return errors.New("mail: no sender configured; set FOLD_SMTP_URL (see .env.example)")
}
