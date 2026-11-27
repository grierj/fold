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
	"fold/internal/notices"
	"fold/internal/settings"
	"fold/internal/write"
)

// Mailer sends the account emails. The words are written here, when the job
// runs, from the job's kind and ids.
type Mailer struct {
	Reader  *db.Reader
	Keys    *keys.Keys
	SiteURL string
	Outbox  *mail.Outbox
	Dev     bool
	Clock   interface{ Now() time.Time }
}

// Register adds the mailer's job handlers to the runner.
func (m *Mailer) Register(r *write.Runner) {
	r.Handle(JobLinkMail, m.SendLink)
	r.Handle(JobApprovedMail, m.SendLink)
	r.Handle(JobAccountExists, m.toPerson(existsEmail))
	r.Handle(JobRequestReceived, m.toPerson(receivedEmail))
	r.Handle(notices.JobMail, m.SendNotice)
}

// send writes one email through the church's mail settings.
func (m *Mailer) send(ctx context.Context, s settings.Settings, to string, e mail.Email) error {
	sender, err := s.Sender(m.Keys, m.Outbox, m.Dev)
	if err != nil {
		return err
	}
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

func footer(church string) string {
	return fmt.Sprintf("Sent by %s, powered by %s.", church, brand.Name)
}

// SendLink emails a link (JobLinkMail, JobApprovedMail).
func (m *Mailer) SendLink(ctx context.Context, j write.Job) error {
	if j.Secret == nil {
		return write.Permanent(errors.New("the link's token is gone"))
	}
	var purpose, to string
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
		return err
	})
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !usable) {
		return nil // replaced, used or expired: nothing to send
	}
	if err != nil {
		return err
	}
	church, url := s[settings.ChurchName], m.SiteURL+"/l/"+string(j.Secret)
	e := linkEmail(purpose, church, url)
	if j.Kind == JobApprovedMail {
		e = approvedEmail(church, url, m.SiteURL)
	}
	return m.send(ctx, s, to, e)
}

// toPerson is a handler for an email to a person, by id, whatever their
// status, worded by f. A person who is gone is nothing to send.
func (m *Mailer) toPerson(f func(church, site string) mail.Email) write.Handler {
	return func(ctx context.Context, j write.Job) error {
		var to string
		var s settings.Settings
		err := m.Reader.Read(ctx, func(q *sql.Tx) error {
			if err := q.QueryRowContext(ctx, `SELECT email FROM people WHERE id = ?`, j.Ref).Scan(&to); err != nil {
				return err
			}
			var err error
			s, err = settings.Load(ctx, q)
			return err
		})
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		return m.send(ctx, s, to, f(s[settings.ChurchName], m.SiteURL))
	}
}

// SendNotice emails a notice to its person, worded when sent and through
// the same visibility rules as the bell. A notice that no longer reads (its
// subject declined, say) sends nothing.
func (m *Mailer) SendNotice(ctx context.Context, j write.Job) error {
	var n notices.Notice
	var to string
	var s settings.Settings
	err := m.Reader.Read(ctx, func(q *sql.Tx) error {
		var err error
		if n, to, err = notices.ForMail(ctx, q, j.Ref); err != nil {
			return err
		}
		s, err = settings.Load(ctx, q)
		return err
	})
	if errors.Is(err, notices.ErrGone) {
		return nil
	}
	if err != nil {
		return err
	}
	church := s[settings.ChurchName]
	return m.send(ctx, s, to, mail.Email{Church: church, Subject: n.Subject + " at " + church, Footer: footer(church),
		Before: []string{n.Text},
		Button: &mail.Button{Label: "Open " + church, URL: m.SiteURL + n.Href}})
}

func linkEmail(purpose, church, url string) mail.Email {
	switch purpose {
	case "setup":
		return mail.Email{Church: church, Subject: "Finish setting up " + church, Footer: footer(church),
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
		return mail.Email{Church: church, Subject: "Your sign-in link for " + church, Footer: footer(church),
			Before: []string{"Press the button to sign in."},
			Button: &mail.Button{Label: "Sign in", URL: url},
			After: []string{
				"The link works once, within 24 hours. Asking for another link cancels this one.",
				"If you did not ask to sign in, you can ignore this message. Nobody can sign in without it.",
			}}
	}
}

func approvedEmail(church, url, site string) mail.Email {
	return mail.Email{Church: church, Subject: "Your account at " + church + " is ready", Footer: footer(church),
		Before: []string{"Good news: an administrator approved your account. Press the button to sign in."},
		Button: &mail.Button{Label: "Sign in", URL: url},
		After: []string{
			"The button works once, within 24 hours. After that, ask for a new sign-in link at " + site + "/signin.",
		}}
}

func existsEmail(church, site string) mail.Email {
	return mail.Email{Church: church, Subject: "You already have an account at " + church, Footer: footer(church),
		Before: []string{"Someone, probably you, asked for a new account with this address. You already have one, so nothing changed."},
		Button: &mail.Button{Label: "Sign in", URL: site + "/signin"},
		After:  []string{"If it wasn't you, you can ignore this message."}}
}

func receivedEmail(church, _ string) mail.Email {
	return mail.Email{Church: church, Subject: "We got your request for an account at " + church, Footer: footer(church),
		Before: []string{"Thanks for asking. An administrator will look at your request, and you'll get an email when your account is ready."},
		After:  []string{"If you didn't ask for an account, you can ignore this message."}}
}
