package accounts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"fold/internal/db"
	"fold/internal/people"
	"fold/internal/policy"
	"fold/internal/settings"
	"fold/internal/write"
)

// MailInput is the mail settings as typed. An empty secret keeps the one
// already saved, so "Change the settings" never asks for it again.
type MailInput struct {
	Provider      string // postmark, smtp, outbox
	From          string
	PostmarkToken string
	SMTPHost      string
	SMTPPort      int
	SMTPUsername  string
	SMTPPassword  string
}

// SetupInput is the setup form, already validated.
type SetupInput struct {
	ChurchName string
	Zone       string
	Name       string
	Email      string
	Mail       MailInput
}

// StartSetup saves the setup answers and sends the test message. It may run
// again (a typo, a new mail password): each run replaces the last answers
// and cancels the previous message's link.
type StartSetup struct {
	CodeVerified bool
	In           SetupInput
}

func (StartSetup) Action() string { return "setup.start" }

func setupFacts(tx *write.Tx) (policy.Setup, error) {
	var v string
	err := tx.QueryRow(`SELECT value FROM settings WHERE key = ?`, settings.SetupDone).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return policy.Setup{}, nil
	}
	return policy.Setup{Done: v == "1"}, err
}

func (c StartSetup) Apply(tx *write.Tx) error {
	facts, err := setupFacts(tx)
	if err != nil {
		return err
	}
	if !policy.StartSetup(facts, c.CodeVerified) {
		return write.ErrNotAllowed
	}
	in := c.In
	for k, v := range map[string]string{
		settings.ChurchName: in.ChurchName, settings.ChurchZone: in.Zone,
		settings.MailProvider: in.Mail.Provider, settings.MailFrom: in.Mail.From,
		settings.SMTPHost: in.Mail.SMTPHost, settings.SMTPUsername: in.Mail.SMTPUsername,
		settings.SMTPPort: strconv.Itoa(in.Mail.SMTPPort),
	} {
		if err := settings.Save(tx, k, v); err != nil {
			return err
		}
	}
	for k, v := range map[string]string{settings.PostmarkToken: in.Mail.PostmarkToken, settings.SMTPPassword: in.Mail.SMTPPassword} {
		if v == "" {
			continue
		}
		if err := settings.SaveSealed(tx, k, v); err != nil {
			return err
		}
	}
	// The would-be first admin is one row in people, waiting. A second run
	// with a new name or address updates it.
	var id int64
	var uid string
	err = tx.QueryRow(`SELECT id, uid FROM people WHERE status = 'setup'`).Scan(&id, &uid)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		uid = people.NewUID()
		res, err := tx.Exec(`INSERT INTO people (uid, name, email, status, is_admin, zone, created_at, updated_at)
			VALUES (?, ?, ?, 'setup', 1, ?, ?, ?)`, uid, in.Name, in.Email, in.Zone, tx.Now.Unix(), tx.Now.Unix())
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
	case err != nil:
		return err
	default:
		if _, err := tx.Exec(`UPDATE people SET name = ?, email = ?, zone = ?, updated_at = ?, version = version + 1 WHERE id = ?`,
			in.Name, in.Email, in.Zone, tx.Now.Unix(), id); err != nil {
			return err
		}
	}
	tx.Subject(uid)
	tx.Detail("provider", in.Mail.Provider)
	_, err = issueLink(tx, id, "setup")
	return err
}

// FinishSetup is the test message's button. It makes the waiting person the
// first admin and signs them in.
type FinishSetup struct {
	Token string
	// SessionToken is set on success, for the cookie.
	SessionToken string
}

func (*FinishSetup) Action() string { return "setup.finish" }

func (c *FinishSetup) Apply(tx *write.Tx) error {
	if !ValidTokenShape(c.Token) {
		return write.ErrNotAllowed
	}
	facts, err := setupFacts(tx)
	if err != nil {
		return err
	}
	l, err := loadLink(tx.Context(), tx, c.Token)
	if err != nil {
		return err
	}
	if !policy.FinishSetup(facts, l.facts, tx.Now) {
		return write.ErrNotAllowed
	}
	p, err := people.ByID(tx.Context(), tx, l.PersonID)
	if err != nil {
		return err
	}
	if p.Status != people.StatusSetup {
		return write.ErrNotAllowed
	}
	if err := useLink(tx, l.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE people SET status = 'active', is_admin = 1, updated_at = ?, version = version + 1 WHERE id = ?`,
		tx.Now.Unix(), p.ID); err != nil {
		return err
	}
	if err := settings.Save(tx, settings.SetupDone, "1"); err != nil {
		return err
	}
	tx.Subject(p.UID)
	c.SessionToken, err = startSession(tx, p.ID)
	return err
}

// SetupStatus is what the "check your email" page says about the test
// message.
type SetupStatus struct {
	Email   string
	State   string // pending, done, failed, or "" when nothing was sent
	Problem string // why sending failed, addresses removed
	Retries int
}

// LoadSetupStatus reads the latest test message's job.
func LoadSetupStatus(ctx context.Context, q db.Querier) (SetupStatus, error) {
	var s SetupStatus
	err := q.QueryRowContext(ctx, `SELECT p.email, j.state, j.last_error, j.attempts
		FROM links l JOIN people p ON p.id = l.person_id
		JOIN jobs j ON j.kind = ? AND j.ref = l.id
		WHERE l.purpose = 'setup' AND p.status = 'setup' AND l.cancelled_at IS NULL
		ORDER BY l.id DESC LIMIT 1`, JobLinkMail).Scan(&s.Email, &s.State, &s.Problem, &s.Retries)
	if errors.Is(err, sql.ErrNoRows) {
		return SetupStatus{}, nil
	}
	return s, err
}

// SetupAnswers is the saved setup form, without secrets, for "Change the
// settings".
func SetupAnswers(ctx context.Context, q db.Querier) (SetupInput, bool, bool, error) {
	s, err := settings.Load(ctx, q)
	if err != nil {
		return SetupInput{}, false, false, err
	}
	in := SetupInput{ChurchName: s[settings.ChurchName], Zone: s[settings.ChurchZone], Mail: MailInput{
		Provider: s[settings.MailProvider], From: s[settings.MailFrom], SMTPHost: s[settings.SMTPHost],
		SMTPUsername: s[settings.SMTPUsername],
	}}
	in.Mail.SMTPPort, _ = strconv.Atoi(s[settings.SMTPPort])
	err = q.QueryRowContext(ctx, `SELECT name, email FROM people WHERE status = 'setup'`).Scan(&in.Name, &in.Email)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return SetupInput{}, false, false, err
	}
	return in, s.Has(settings.PostmarkToken), s.Has(settings.SMTPPassword), nil
}

// IsSetupDone reads the one flag.
func IsSetupDone(ctx context.Context, q db.Querier) (bool, error) {
	var v string
	err := q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, settings.SetupDone).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("accounts: read setup state: %w", err)
	}
	return v == "1", nil
}
