package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"fold/internal/jobs"
	"fold/internal/mail"
	"fold/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	kindPasswordResetMail = "account.password_reset_mail"
	directMailAttempts    = 8
)

// Direct-mail jobs carry IDs only. Capabilities are minted in preparation,
// committed, and kept only in memory until the provider send finishes.
type userMailJob struct {
	UserID uint `json:"userId"`
}

func (a *App) mailKinds() []jobs.Kind {
	return []jobs.Kind{{
		Name: kindPasswordResetMail, Label: "Sending a password reset link", MaxAttempts: directMailAttempts,
		Run: func(ctx context.Context, payload json.RawMessage) error {
			var p userMailJob
			if err := json.Unmarshal(payload, &p); err != nil {
				return err
			}
			return a.prepareAndSend(ctx, kindPasswordResetMail, p.UserID, func(tx *gorm.DB) (mail.Message, bool, error) {
				return a.preparePasswordReset(tx, p.UserID)
			})
		},
	}}
}

// prepareAndSend commits the capability before contacting the provider. No
// database lock spans provider I/O. A retry prepares a fresh capability,
// invalidating the previous attempt's link even if that mail was accepted.
// Preparation reads, including siteNameIn, must use the supplied transaction;
// message constructors take those values without querying the database.
func (a *App) prepareAndSend(ctx context.Context, kind string, subjectID uint, prep func(*gorm.DB) (mail.Message, bool, error)) error {
	var msg mail.Message
	var send bool
	err := a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		msg, send, err = prep(tx)
		return err
	})
	if err != nil {
		log.Printf("mail: %s #%d preparation: %s", kind, subjectID, jobs.CleanError(err))
		return err
	}
	if !send {
		return nil
	}
	return a.sendDirect(ctx, kind, subjectID, msg)
}

// sendDirect uses the configured suppression-aware sender for required account
// mail. Permanent outcomes finish quietly; the runner retries transient errors
// and cancellation, and alerts admins if all attempts fail.
func (a *App) sendDirect(ctx context.Context, kind string, subjectID uint, msg mail.Message) error {
	if a.Mailer == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, deliverSendTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	msg.Optional = false
	err := a.Mailer.Send(ctx, msg)
	if err == nil {
		return nil
	}
	log.Printf("mail: %s #%d: %s", kind, subjectID, jobs.CleanError(err))
	if mail.NotConfigured(err) || mail.IsSuppressed(err) || mail.IsPermanent(err) {
		return nil
	}
	return err
}

func (a *App) preparePasswordReset(tx *gorm.DB, userID uint) (mail.Message, bool, error) {
	if err := lockContacts(tx); err != nil {
		return mail.Message{}, false, err
	}
	var user models.User
	if err := tx.Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).First(&user, userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return mail.Message{}, false, nil
		}
		return mail.Message{}, false, err
	}
	if user.Status != models.UserActive {
		return mail.Message{}, false, nil
	}
	raw, err := createAccountToken(tx, models.TokenPasswordReset, user.ID, nil, tokenTTL)
	if err != nil {
		return mail.Message{}, false, err
	}
	site, err := siteNameIn(tx)
	if err != nil {
		return mail.Message{}, false, err
	}
	return a.passwordResetMessage(user.Email, raw, site), true, nil
}

func (a *App) passwordResetMessage(to, raw, site string) mail.Message {
	link := a.emailBaseURL() + "/reset-password?token=" + raw
	return mail.Message{
		To:      to,
		Subject: "Reset your Fold password",
		Text: fmt.Sprintf(`Someone asked to reset the password for this email address at %s.

Reset it here (the link works once and expires in 24 hours):
%s

If that wasn't you, you can ignore this email — your password won't change.`,
			site, link),
	}
}
