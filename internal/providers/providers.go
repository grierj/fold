// Package providers holds the interfaces Fold's outside services sit behind
// (PLAN 5.6) and the registry that picks an adapter by name. Adapters live
// with their kind (email adapters in internal/mail) and register here, so
// swapping a provider is a configuration change: today an environment
// variable, from M4.1 a ProviderConfig row.
package providers

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"gorm.io/gorm"
)

// Stream is the kind of mail a message is (PLAN 7.4, 12.1). Providers that
// support it keep the two apart, so announcements never hurt the
// reputation password resets depend on.
type Stream string

const (
	// StreamTransactional is account and notification mail (the default).
	StreamTransactional Stream = "transactional"
	// StreamBroadcast is mail to many people at once (M3.5).
	StreamBroadcast Stream = "broadcast"
)

// EmailMessage is one plain-text email to one address.
type EmailMessage struct {
	To      string
	Subject string
	Text    string
	// Stream is "" or StreamTransactional for account mail.
	Stream Stream
}

// EmailCaps says what an email adapter can do.
type EmailCaps struct {
	Name string
	// Streams: transactional and broadcast mail go out separately.
	Streams bool
	// Webhooks: the provider reports deliveries, bounces and complaints.
	Webhooks bool
	// BatchSize is how many messages one batch call takes (0: no batches).
	BatchSize int
}

// EmailSender sends email. SendEmail returns the provider's id for the
// message, which later delivery and bounce reports refer to.
type EmailSender interface {
	SendEmail(ctx context.Context, m EmailMessage) (providerID string, err error)
	Capabilities() EmailCaps
}

// SendError is a provider refusing a message. Permanent means retrying the
// same message can't help (a bad address, a bad token); otherwise it may.
type SendError struct {
	Provider  string
	Code      int
	Message   string
	Permanent bool
}

func (e *SendError) Error() string {
	return fmt.Sprintf("%s: %s (code %d)", e.Provider, e.Message, e.Code)
}

// Settings configure an adapter: Get reads a setting by its environment
// name, DB is for adapters that store mail (devoutbox), From is the From
// address.
type Settings struct {
	Get  func(key string) string
	DB   *gorm.DB
	From string
}

// EmailFactory builds an email adapter from settings.
type EmailFactory func(Settings) (EmailSender, error)

var (
	mu    sync.RWMutex
	email = map[string]EmailFactory{}
)

// RegisterEmail adds an email adapter under name. Registering a name twice
// is a programming error.
func RegisterEmail(name string, f EmailFactory) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := email[name]; dup {
		panic("providers: email adapter " + name + " registered twice")
	}
	email[name] = f
}

// NewEmail builds the email adapter registered under name.
func NewEmail(name string, s Settings) (EmailSender, error) {
	mu.RLock()
	f, ok := email[name]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown email provider %q; choose one of %s", name, strings.Join(EmailProviders(), ", "))
	}
	if s.Get == nil {
		s.Get = func(string) string { return "" }
	}
	return f(s)
}

// EmailProviders lists the registered email adapters by name.
func EmailProviders() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(email))
	for name := range email {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
