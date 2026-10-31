package mail

import (
	"errors"
	"strings"

	"fold/internal/providers"
)

// The email adapters, registered by name (PLAN 5.6). FOLD_EMAIL_PROVIDER
// picks one; the settings below are read by environment name, and from
// M4.1 from the encrypted ProviderConfig.
func init() {
	providers.RegisterEmail("devoutbox", func(s providers.Settings) (providers.EmailSender, error) {
		if s.DB == nil {
			return nil, errors.New("mail: devoutbox needs the database")
		}
		return NewDevOutbox(s.DB, s.From), nil
	})
	providers.RegisterEmail("smtp", func(s providers.Settings) (providers.EmailSender, error) {
		raw := s.Get("FOLD_SMTP_URL")
		if raw == "" {
			return nil, errors.New("mail: the smtp provider needs FOLD_SMTP_URL")
		}
		sender, err := NewSMTP(raw, s.From)
		if err != nil {
			return nil, err
		}
		sender.AllowInsecure = strings.EqualFold(strings.TrimSpace(s.Get("FOLD_SMTP_ALLOW_INSECURE")), "true")
		return sender, nil
	})
	providers.RegisterEmail("postmark", func(s providers.Settings) (providers.EmailSender, error) {
		return NewPostmark(s.Get("FOLD_POSTMARK_SERVER_TOKEN"), s.From,
			s.Get("FOLD_POSTMARK_TRANSACTIONAL_STREAM"), s.Get("FOLD_POSTMARK_BROADCAST_STREAM"))
	})
}

// Choose names the email adapter for a deployment: FOLD_EMAIL_PROVIDER when
// set; otherwise devoutbox in dev, then Postmark if it has a token, then
// SMTP if it has a URL. "" means none is configured.
func Choose(get func(string) string, dev bool) string {
	if name := strings.TrimSpace(get("FOLD_EMAIL_PROVIDER")); name != "" {
		return strings.ToLower(name)
	}
	switch {
	case dev:
		return "devoutbox"
	case get("FOLD_POSTMARK_SERVER_TOKEN") != "":
		return "postmark"
	case get("FOLD_SMTP_URL") != "":
		return "smtp"
	}
	return ""
}
