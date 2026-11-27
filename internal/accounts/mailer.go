package accounts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"fold/internal/brand"
	"fold/internal/db"
	"fold/internal/keys"
	"fold/internal/mail"
	"fold/internal/settings"
	"fold/internal/write"
)

// Mailer sends the emails that carry links. The words are written here, when
// the job runs, from the link's kind and ids.
type Mailer struct {
	Reader  *db.Reader
	Keys    *keys.Keys
	SiteURL string
	Outbox  *mail.Outbox
	Dev     bool
	Clock   interface{ Now() time.Time }
}

// SendLink is the JobLinkMail handler.
func (m *Mailer) SendLink(ctx context.Context, j write.Job) error {
	if j.Secret == nil {
		return write.Permanent(errors.New("the link's token is gone"))
	}
	var purpose, to, church string
	var usable bool
	var s settings.Settings
	err := m.Reader.Read(ctx, func(q *sql.Tx) error {
		var expires int64
		var used, cancelled sql.NullInt64
		err := q.QueryRowContext(ctx, `SELECT l.purpose, l.expires_at, l.used_at, l.cancelled_at, p.email
			FROM links l JOIN people p ON p.id = l.person_id WHERE l.id = ?`, j.Ref).
			Scan(&purpose, &expires, &used, &cancelled, &to)
		if err != nil {
			return err
		}
		usable = !used.Valid && !cancelled.Valid && m.Clock.Now().Before(time.Unix(expires, 0))
		s, err = settings.Load(ctx, q)
		church = s[settings.ChurchName]
		return err
	})
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !usable) {
		return nil // replaced, used or expired: nothing to send
	}
	if err != nil {
		return err
	}
	sender, err := s.Sender(m.Keys, m.Outbox, m.Dev)
	if err != nil {
		return err
	}
	e := linkEmail(purpose, church, m.SiteURL+"/l/"+string(j.Secret))
	msg, err := e.Message(to)
	if err != nil {
		return err
	}
	if _, err := sender.Send(ctx, msg); err != nil {
		if mail.IsPermanent(err) {
			return write.Permanent(err)
		}
		return err
	}
	return nil
}

func linkEmail(purpose, church, url string) mail.Email {
	footer := fmt.Sprintf("Sent by %s, powered by %s.", church, brand.Name)
	switch purpose {
	case "setup":
		return mail.Email{Church: church, Subject: "Finish setting up " + church, Footer: footer,
			Before: []string{
				"This is the test message from your new site, so your mail settings work.",
				"Press the button to finish setup. You will be signed in as the first administrator.",
			},
			Button: &mail.Button{Label: "Finish setup", URL: url},
			After: []string{
				"The button works once, within 24 hours.",
				"If you did not set up a site, you can ignore this message.",
			}}
	default:
		return mail.Email{Church: church, Subject: "Your sign-in link for " + church, Footer: footer,
			Before: []string{"Press the button to sign in."},
			Button: &mail.Button{Label: "Sign in", URL: url},
			After: []string{
				"The link works once, within 24 hours. Asking for another link cancels this one.",
				"If you did not ask to sign in, you can ignore this message. Nobody can sign in without it.",
			}}
	}
}
