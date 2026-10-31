package mail

import (
	"context"
	"errors"
	"strings"

	"fold/internal/providers"
	"fold/models"

	"gorm.io/gorm"
)

// ErrSuppressed is a send refused because the address is suppressed
// (DEC-112). Reason is the stored reason, for the delivery record.
type ErrSuppressed struct{ Reason string }

func (e *ErrSuppressed) Error() string {
	if e.Reason == models.SuppressComplaint {
		return "mail: this address marked our email as spam"
	}
	return "mail: email to this address bounces"
}

// Suppressing wraps an adapter so nothing reaches a suppressed address
// (DEC-112): a hard bounce or manual suppression stops all mail, and a spam
// complaint stops optional and broadcast mail while account mail still goes.
// Every send pays one indexed read.
type Suppressing struct {
	inner Adapter
	db    *gorm.DB
}

// Adapter is what every email adapter is: a Sender that also reports ids.
type Adapter interface {
	Sender
	providers.EmailSender
}

// WithSuppressions wraps inner with the suppression check.
func WithSuppressions(db *gorm.DB, inner Adapter) *Suppressing {
	return &Suppressing{inner: inner, db: db}
}

func (s *Suppressing) Capabilities() providers.EmailCaps { return s.inner.Capabilities() }

func (s *Suppressing) Send(ctx context.Context, m Message) error {
	_, err := s.SendEmail(ctx, m)
	return err
}

func (s *Suppressing) SendEmail(ctx context.Context, m Message) (string, error) {
	var reasons []string
	if err := s.db.WithContext(ctx).Model(&models.Suppression{}).
		Where("channel = ? AND address = ? AND cleared_at IS NULL", "email", strings.ToLower(strings.TrimSpace(m.To))).
		Pluck("reason", &reasons).Error; err != nil {
		return "", err
	}
	if err := Blocked(reasons, m); err != nil {
		return "", err
	}
	return s.inner.SendEmail(ctx, m)
}

// IsSuppressed reports whether err is a refused send to a suppressed address.
func IsSuppressed(err error) bool {
	var s *ErrSuppressed
	return errors.As(err, &s)
}

// Unwrap is the adapter under a suppression check, or s itself.
func Unwrap(s Sender) Sender {
	if w, ok := s.(*Suppressing); ok {
		return w.inner
	}
	return s
}

// Blocked is the suppression rule (DEC-112) for a message to an address
// with these open reasons: a hard bounce or manual suppression refuses
// everything, a complaint refuses optional and broadcast mail. A caller that
// already read the reasons (a notification batch) sends through Unwrap
// after asking this.
func Blocked(reasons []string, m Message) error {
	for _, r := range reasons {
		if r != models.SuppressComplaint || m.Optional || m.Stream == providers.StreamBroadcast {
			return &ErrSuppressed{Reason: r}
		}
	}
	return nil
}
