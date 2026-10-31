// Package mail is Fold's minimal transactional email sender (C6, 5.6).
//
// Three adapters sit behind the provider registry (M3.2, internal/providers):
// devoutbox, which stores mail in the database for dev and tests (shown at
// /dev/outbox); SMTP, the universal fallback; and Postmark, the default
// (DEC-21). Callers send a Message through Sender and never see which one
// runs. Templates are plain text.
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
	"net/url"
	"strconv"
	"strings"
	"time"

	"fold/internal/providers"
	"fold/models"

	"gorm.io/gorm"
)

// Message is one email to one address (plain text, HTML optional); Stream is "" for
// account mail.
type Message = providers.EmailMessage

// Sender sends one email. Every adapter also implements
// providers.EmailSender, which reports the provider's message id.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// checkMessage refuses what no adapter may send: no recipient, a recipient
// with a newline (which could inject headers) or more than one recipient.
func checkMessage(m Message) error {
	if strings.TrimSpace(m.To) == "" {
		return errors.New("mail: no recipient")
	}
	if !headerSafe(m.To) {
		return errors.New("mail: recipient contains a newline")
	}
	if m.ListUnsubscribe != "" && !oneURL(m.ListUnsubscribe) {
		return errors.New("mail: the unsubscribe link must be one http(s) URL")
	}
	if !replyToOK(m.ReplyTo) {
		return errors.New("mail: the reply-to must be one plain address")
	}
	if len(m.FromName) > 200 {
		return errors.New("mail: the sender name is too long")
	}
	// One address only: Postmark reads commas as a list of recipients.
	if strings.ContainsAny(m.To, ",;") {
		return errors.New("mail: one recipient per message")
	}
	return nil
}

// oneLine keeps a subject on one header line.
func oneLine(s string) string { return strings.NewReplacer("\r", " ", "\n", " ").Replace(s) }

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
	_, err := d.SendEmail(ctx, m)
	return err
}

// SendEmail stores m; its id is "devoutbox-<row id>".
func (d *DevOutbox) SendEmail(ctx context.Context, m Message) (string, error) {
	if err := checkMessage(m); err != nil {
		return "", err
	}
	row := models.MailOutbox{ToAddress: m.To, FromAddress: fromWithName(d.From, m), ReplyTo: m.ReplyTo, Subject: oneLine(m.Subject), Body: m.Text, HTMLBody: m.HTML, ListUnsubscribe: m.ListUnsubscribe}
	if err := d.DB.WithContext(ctx).Create(&row).Error; err != nil {
		return "", err
	}
	return fmt.Sprintf("devoutbox-%d", row.ID), nil
}

// Capabilities: devoutbox keeps everything; it has no streams or webhooks.
func (d *DevOutbox) Capabilities() providers.EmailCaps { return providers.EmailCaps{Name: "devoutbox"} }

// OutboxMessage is one stored email, newest first.
type OutboxMessage struct {
	ID      uint
	To      string
	Subject string
	Body    string
	// HTML is the HTML part (LatestTo); List leaves it empty and sets HasHTML.
	HTML            string
	HasHTML         bool
	From            string
	ReplyTo         string
	ListUnsubscribe string
	CreatedAt       time.Time
}

// List returns the newest stored emails, for /dev/outbox and tests.
func (d *DevOutbox) List(limit int) ([]OutboxMessage, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var rows []models.MailOutbox
	// The HTML stays in the table; HasHTML says the page has one to link.
	if err := d.DB.Select("id, to_address, from_address, reply_to, subject, body, list_unsubscribe, created_at, CASE WHEN html_body <> '' THEN '-' ELSE '' END AS html_body").Order("id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]OutboxMessage, 0, len(rows))
	for _, r := range rows {
		out = append(out, OutboxMessage{
			ID: r.ID, To: r.ToAddress, Subject: r.Subject,
			Body: r.Body, HasHTML: r.HTMLBody != "", From: r.FromAddress, ReplyTo: r.ReplyTo, ListUnsubscribe: r.ListUnsubscribe, CreatedAt: r.CreatedAt,
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
	return OutboxMessage{ID: r.ID, To: r.ToAddress, Subject: r.Subject, Body: r.Body, HTML: r.HTMLBody, HasHTML: r.HTMLBody != "", From: r.FromAddress, ReplyTo: r.ReplyTo, ListUnsubscribe: r.ListUnsubscribe, CreatedAt: r.CreatedAt}, nil
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
	_, err := s.SendEmail(ctx, m)
	return err
}

// Capabilities: plain SMTP has no streams and reports no bounces.
func (s *SMTPSender) Capabilities() providers.EmailCaps { return providers.EmailCaps{Name: "smtp"} }

// SendEmail delivers m; its id is the Message-ID header it was sent with.
func (s *SMTPSender) SendEmail(ctx context.Context, m Message) (string, error) {
	if err := checkMessage(m); err != nil {
		return "", err
	}
	from := s.from
	if from == "" {
		return "", errors.New("mail: no from address (set FOLD_EMAIL_FROM)")
	}
	if !headerSafe(from) {
		return "", errors.New("mail: from address contains a newline")
	}
	// Subjects come from templates that interpolate names; a newline in one
	// must never become a header boundary.
	// Non-ASCII subjects go out as RFC 2047 encoded words; ASCII is unchanged.
	subject := mime.QEncoding.Encode("utf-8", oneLine(m.Subject))
	msgID, err := messageID(fromAddress(from))
	if err != nil {
		return "", fmt.Errorf("mail: message id: %w", err)
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", s.addr)
	if err != nil {
		return "", fmt.Errorf("mail: dial: %w", err)
	}
	// A stuck server must not block the calling handler forever.
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	if s.implicit {
		// smtps://: TLS from the first byte, before any SMTP greeting.
		tconn := tls.Client(conn, s.tlsCfg())
		if err := tconn.HandshakeContext(ctx); err != nil {
			conn.Close()
			return "", fmt.Errorf("mail: tls: %w", err)
		}
		conn = tconn
	}
	c, err := smtp.NewClient(conn, s.host)
	if err != nil {
		conn.Close()
		return "", fmt.Errorf("mail: smtp: %w", err)
	}
	defer c.Close()
	if !s.implicit {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(s.tlsCfg()); err != nil {
				return "", fmt.Errorf("mail: starttls: %w", err)
			}
		} else if !s.AllowInsecure && !strings.EqualFold(s.host, "localhost") && !net.ParseIP(s.host).IsLoopback() {
			return "", errors.New("mail: server does not offer STARTTLS; use an encrypted SMTP relay or explicitly set FOLD_SMTP_ALLOW_INSECURE=true for a trusted relay")
		}
	}
	if s.username != "" {
		auth := smtp.PlainAuth("", s.username, s.password, s.host)
		if ok, _ := c.Extension("AUTH"); !ok {
			return "", errors.New("mail: server does not offer AUTH")
		}
		if err := c.Auth(auth); err != nil {
			return "", fmt.Errorf("mail: auth: %w", err)
		}
	}
	if err := c.Mail(fromAddress(from)); err != nil {
		return "", fmt.Errorf("mail: from: %w", err)
	}
	if err := c.Rcpt(m.To); err != nil {
		return "", fmt.Errorf("mail: rcpt: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return "", fmt.Errorf("mail: data: %w", err)
	}
	unsubscribe := ""
	if m.ListUnsubscribe != "" {
		// RFC 8058 one-click: the mail client POSTs to the URL.
		unsubscribe = "List-Unsubscribe: <" + m.ListUnsubscribe + ">\r\nList-Unsubscribe-Post: List-Unsubscribe=One-Click\r\n"
	}
	if m.ReplyTo != "" {
		unsubscribe = "Reply-To: " + m.ReplyTo + "\r\n" + unsubscribe
	}
	contentType, body, err := mimeBody(m)
	if err != nil {
		w.Close()
		return "", fmt.Errorf("mail: body: %w", err)
	}
	if _, err := fmt.Fprintf(w, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: %s\r\n%sMIME-Version: 1.0\r\n%s\r\n%s\r\n",
		fromWithName(from, m), m.To, subject, time.Now().Format(time.RFC1123Z), msgID, unsubscribe, contentType, body); err != nil {
		w.Close()
		return "", fmt.Errorf("mail: write: %w", err)
	}
	if err := w.Close(); err != nil {
		return "", fmt.Errorf("mail: send: %w", err)
	}
	if err := c.Quit(); err != nil {
		return msgID, err
	}
	return msgID, nil
}

// messageID makes a unique Message-ID in the sender's domain.
func messageID(addr string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	domain := "localhost"
	if i := strings.LastIndex(addr, "@"); i >= 0 && i < len(addr)-1 {
		domain = addr[i+1:]
	}
	return "<" + hex.EncodeToString(b) + "@" + domain + ">", nil
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
func (NoSender) Send(context.Context, Message) error { return errNoSender }

// SendEmail always fails.
func (NoSender) SendEmail(context.Context, Message) (string, error) { return "", errNoSender }

// Capabilities: none.
func (NoSender) Capabilities() providers.EmailCaps { return providers.EmailCaps{Name: "none"} }

var errNoSender = errors.New("mail: no sender configured; set FOLD_EMAIL_PROVIDER, FOLD_POSTMARK_SERVER_TOKEN or FOLD_SMTP_URL (see .env.example)")

// Address is the bare address of a From value like "Fold <a@b.org>".
func Address(from string) string { return fromAddress(from) }

// oneURL: s is a single absolute http(s) URL with nothing that could end
// the List-Unsubscribe header's <…> or add another header.
func oneURL(s string) bool {
	if strings.ContainsAny(s, "<>\"\r\n\t ") {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

// mimeBody is the Content-Type header line and the body for m: plain text
// alone, or multipart/alternative (text first, then HTML) when m has HTML.
// Both parts are quoted-printable then, so a long HTML line never breaks
// SMTP's 998-character line limit.
func mimeBody(m Message) (string, string, error) {
	if m.HTML == "" {
		return "Content-Type: text/plain; charset=utf-8\r\n", m.Text, nil
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
		qp := quotedprintable.NewWriter(pw)
		if _, err := qp.Write([]byte(part.body)); err != nil {
			return "", "", err
		}
		if err := qp.Close(); err != nil {
			return "", "", err
		}
	}
	if err := w.Close(); err != nil {
		return "", "", err
	}
	return "Content-Type: multipart/alternative; boundary=\"" + w.Boundary() + "\"\r\n", b.String(), nil
}

// HTMLOf returns the HTML part of the stored email id, for /dev/outbox.
func (d *DevOutbox) HTMLOf(id uint) (string, error) {
	var r models.MailOutbox
	if err := d.DB.Select("id, html_body").First(&r, id).Error; err != nil {
		return "", err
	}
	return r.HTMLBody, nil
}

// fromWithName is the From header for from with m's display name, if any:
// the configured address under a new name, encoded when it isn't ASCII.
func fromWithName(from string, m Message) string {
	name := strings.TrimSpace(oneLine(m.FromName))
	if name == "" {
		return from
	}
	return (&netmail.Address{Name: name, Address: fromAddress(from)}).String()
}

// replyToOK: empty, or exactly one plain address with no display name.
func replyToOK(s string) bool {
	if s == "" {
		return true
	}
	a, err := netmail.ParseAddress(s)
	return err == nil && a.Name == "" && a.Address == s && headerSafe(s) && !strings.ContainsAny(s, ",;")
}
