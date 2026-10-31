package mail

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"fold/internal/providers"
)

// Postmark's webhooks (https://postmarkapp.com/developer/webhooks) post one
// JSON record per request. Postmark doesn't sign them: the endpoint checks
// the HTTP Basic credentials put in the webhook URL (see the handler).
// Fold uses Delivery, Bounce and SpamComplaint; other records are ignored.

type postmarkRecord struct {
	RecordType  string `json:"RecordType"`
	ID          int64  `json:"ID"`
	Type        string `json:"Type"`
	MessageID   string `json:"MessageID"`
	Email       string `json:"Email"`
	Recipient   string `json:"Recipient"`
	Inactive    bool   `json:"Inactive"`
	BouncedAt   string `json:"BouncedAt"`
	DeliveredAt string `json:"DeliveredAt"`
}

// Bounce types that mean the address can't take mail at all, whatever
// Postmark's Inactive flag says.
var postmarkHardBounces = map[string]bool{"HardBounce": true, "BadEmailAddress": true}

// ParseWebhook reads one Postmark webhook body. h carries the
// X-PM-Webhook-Trace-Id, which identifies a redelivered request.
func (p *Postmark) ParseWebhook(h http.Header, body []byte) ([]providers.Event, error) {
	return ParsePostmarkWebhook(h, body)
}

// ParsePostmarkWebhook is ParseWebhook without an adapter, for the handler.
func ParsePostmarkWebhook(h http.Header, body []byte) ([]providers.Event, error) {
	var r postmarkRecord
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("mail: postmark webhook: %w", err)
	}
	e := providers.Event{Provider: "postmark", MessageID: r.MessageID, Detail: limit(r.Type, 100)}
	at := r.BouncedAt
	switch r.RecordType {
	case "Delivery":
		e.Kind, e.Address, at = providers.EventDelivered, r.Recipient, r.DeliveredAt
	case "Bounce":
		e.Kind, e.Address = providers.EventBounced, r.Email
		e.Permanent = r.Inactive || postmarkHardBounces[r.Type]
	case "SpamComplaint":
		e.Kind, e.Address, e.Permanent = providers.EventComplained, r.Email, true
	default:
		return nil, nil
	}
	if strings.TrimSpace(e.Address) == "" || e.MessageID == "" {
		return nil, fmt.Errorf("mail: postmark %s webhook without an address or message id", r.RecordType)
	}
	e.Address = strings.ToLower(strings.TrimSpace(e.Address))
	if t, err := time.Parse(time.RFC3339Nano, at); err == nil {
		e.At = t.UTC()
	}
	// Postmark sends the same trace id when it redelivers a request.
	e.Key = h.Get("X-PM-Webhook-Trace-Id")
	if e.Key == "" {
		e.Key = fmt.Sprintf("%s:%d:%s:%s", r.RecordType, r.ID, r.MessageID, at)
	}
	e.Key = "postmark:" + limit(e.Key, 300)
	return []providers.Event{e}, nil
}

func limit(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
