// Package settings reads and writes the church's settings. A key ending in
// ".sealed" holds an encrypted value; it is never sent back to a browser.
package settings

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"fold/internal/db"
	"fold/internal/keys"
	"fold/internal/mail"
	"fold/internal/write"
)

const (
	ChurchName = "church.name"
	ChurchZone = "church.zone"
	SetupDone  = "setup.done"

	MailProvider  = "mail.provider" // postmark, smtp, or outbox (development only)
	MailFrom      = "mail.from"     // the church's address, not a person's (R23)
	SMTPHost      = "mail.smtp.host"
	SMTPPort      = "mail.smtp.port"
	SMTPUsername  = "mail.smtp.username"
	SMTPPassword  = "mail.smtp.password.sealed"
	PostmarkToken = "mail.postmark.token.sealed"
)

// Settings is every stored value, sealed ones still sealed.
type Settings map[string]string

func Load(ctx context.Context, q db.Querier) (Settings, error) {
	rows, err := q.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	s := Settings{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		s[k] = v
	}
	return s, rows.Err()
}

func (s Settings) Done() bool { return s[SetupDone] == "1" }

// Has reports whether a sealed secret is stored, without opening it.
func (s Settings) Has(key string) bool { return s[key] != "" }

// Save writes one plain value inside a command.
func Save(tx *write.Tx, key, value string) error {
	if strings.HasSuffix(key, ".sealed") {
		return fmt.Errorf("settings: %s must be saved sealed", key)
	}
	return put(tx, key, value)
}

// SaveSealed encrypts value under the settings key and writes it.
func SaveSealed(tx *write.Tx, key, value string) error {
	if !strings.HasSuffix(key, ".sealed") {
		return fmt.Errorf("settings: %s is not a sealed key", key)
	}
	return put(tx, key, base64.StdEncoding.EncodeToString(tx.Seal(keys.Settings, []byte(value))))
}

func put(tx *write.Tx, key, value string) error {
	_, err := tx.Exec(`INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, value, tx.Now.Unix())
	return err
}

// Open decrypts a sealed value; an empty value is "".
func (s Settings) Open(k *keys.Keys, key string) (string, error) {
	v := s[key]
	if v == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return "", err
	}
	plain, _, err := k.Open(keys.Settings, raw)
	return string(plain), err
}

// ErrNoMail is a site with no mail settings saved.
var ErrNoMail = errors.New("settings: no mail settings are saved")

// Sender builds the mail sender the saved settings describe. outbox is the
// development sender; it is offered only when dev is true.
func (s Settings) Sender(k *keys.Keys, outbox *mail.Outbox, dev bool) (mail.Sender, error) {
	from := s[MailFrom]
	switch s[MailProvider] {
	case "postmark":
		token, err := s.Open(k, PostmarkToken)
		if err != nil {
			return nil, write.Permanent(fmt.Errorf("the saved Postmark token could not be read with this server's secret key: %w", err))
		}
		return mail.NewPostmark(token, from), nil
	case "smtp":
		pass, err := s.Open(k, SMTPPassword)
		if err != nil {
			return nil, write.Permanent(fmt.Errorf("the saved SMTP password could not be read with this server's secret key: %w", err))
		}
		port, _ := strconv.Atoi(s[SMTPPort])
		return &mail.SMTP{Host: s[SMTPHost], Port: port, Username: s[SMTPUsername], Password: pass, From: from}, nil
	case "outbox":
		if dev && outbox != nil {
			return outbox, nil
		}
	}
	return nil, write.Permanent(ErrNoMail)
}

// ErrWrongKey is a sealed setting that opens with neither the current nor
// the previous secret.
var ErrWrongKey = errors.New("settings: the saved mail settings were sealed with a different SECRET_KEY")

// Reseal opens every sealed setting and re-encrypts with the current secret
// any that needed the previous one. It returns how many it resealed, or
// ErrWrongKey when one opens with neither, so nothing is kept that the
// server cannot read.
func Reseal(tx *write.Tx) (int, error) {
	s, err := Load(tx.Context(), tx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, key := range []string{PostmarkToken, SMTPPassword} {
		if !s.Has(key) {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(s[key])
		if err != nil {
			return 0, fmt.Errorf("%w (%s)", ErrWrongKey, key)
		}
		plain, usedPrevious, err := tx.Open(keys.Settings, raw)
		if err != nil {
			return 0, fmt.Errorf("%w (%s)", ErrWrongKey, key)
		}
		if usedPrevious {
			if err := SaveSealed(tx, key, string(plain)); err != nil {
				return 0, err
			}
			n++
		}
	}
	return n, nil
}
