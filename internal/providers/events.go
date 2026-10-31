package providers

import (
	"net/http"
	"time"
)

// EventKind is what a provider reported about a message.
type EventKind string

const (
	EventDelivered  EventKind = "delivered"
	EventBounced    EventKind = "bounced"
	EventComplained EventKind = "complained"
)

// Event is one provider report, the same shape whichever provider sent it
// (PLAN 5.6). It names the address and the provider's message id, never
// the message's content.
type Event struct {
	Provider string    `json:"provider"`
	Kind     EventKind `json:"kind"`
	// Address is the recipient the report is about.
	Address string `json:"address"`
	// MessageID is the id SendEmail returned for the message.
	MessageID string `json:"messageId"`
	// Permanent: the address can't take mail (a hard bounce, a complaint,
	// or the provider deactivated it).
	Permanent bool `json:"permanent"`
	// Detail is the provider's short name for what happened ("HardBounce").
	Detail string    `json:"detail"`
	At     time.Time `json:"at"`
	// Key identifies the report itself, so a redelivered one is handled once.
	Key string `json:"key"`
}

// WebhookParser turns one verified webhook request body into events. An
// event kind Fold doesn't use yields no events and no error.
type WebhookParser interface {
	ParseWebhook(h http.Header, body []byte) ([]Event, error)
}
