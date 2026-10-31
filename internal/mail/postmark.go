package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"fold/internal/providers"
)

// Postmark is the default email adapter (DEC-21, PLAN 12.1): plain REST to
// https://api.postmarkapp.com with a server token. Transactional and
// broadcast mail go out on separate message streams, so a mass mailing's
// complaints never hurt the reputation password resets depend on.
type Postmark struct {
	token         string
	from          string
	transactional string
	broadcast     string
	baseURL       string
	client        *http.Client
}

// postmarkAPI is Postmark's API root; the contract tests point it at a fake.
var postmarkAPI = "https://api.postmarkapp.com"

// Postmark's default stream ids: every server has "outbound"; a broadcast
// stream is created in Postmark and is "broadcast" by convention.
const (
	defaultPostmarkTransactional = "outbound"
	defaultPostmarkBroadcast     = "broadcast"
)

// NewPostmark builds the adapter. Empty stream ids take the defaults.
func NewPostmark(token, from, transactional, broadcast string) (*Postmark, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("mail: Postmark needs FOLD_POSTMARK_SERVER_TOKEN (the server's API token)")
	}
	if strings.TrimSpace(from) == "" || !headerSafe(from) {
		return nil, errors.New("mail: Postmark needs a From address (set FOLD_EMAIL_FROM)")
	}
	if transactional == "" {
		transactional = defaultPostmarkTransactional
	}
	if broadcast == "" {
		broadcast = defaultPostmarkBroadcast
	}
	return &Postmark{token: token, from: from, transactional: transactional, broadcast: broadcast,
		baseURL: postmarkAPI, client: &http.Client{Timeout: 15 * time.Second}}, nil
}

// Capabilities: Postmark has streams, webhooks and 500-message batches.
func (p *Postmark) Capabilities() providers.EmailCaps {
	return providers.EmailCaps{Name: "postmark", Streams: true, Webhooks: true, BatchSize: 500}
}

// Send delivers m.
func (p *Postmark) Send(ctx context.Context, m Message) error {
	_, err := p.SendEmail(ctx, m)
	return err
}

// postmarkEmail is the body of POST /email.
type postmarkEmail struct {
	From          string `json:"From"`
	To            string `json:"To"`
	Subject       string `json:"Subject"`
	TextBody      string `json:"TextBody"`
	MessageStream string `json:"MessageStream"`
}

// postmarkResult is Postmark's answer to a send: ErrorCode 0 on success.
type postmarkResult struct {
	ErrorCode int    `json:"ErrorCode"`
	Message   string `json:"Message"`
	MessageID string `json:"MessageID"`
}

// SendEmail delivers m and returns Postmark's MessageID, which its bounce
// and delivery webhooks refer to.
func (p *Postmark) SendEmail(ctx context.Context, m Message) (string, error) {
	if err := checkMessage(m); err != nil {
		return "", err
	}
	stream := p.transactional
	if m.Stream == providers.StreamBroadcast {
		stream = p.broadcast
	}
	body, err := json.Marshal(postmarkEmail{From: p.from, To: m.To, Subject: oneLine(m.Subject), TextBody: m.Text, MessageStream: stream})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/email", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Postmark-Server-Token", p.token)
	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("mail: postmark: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", fmt.Errorf("mail: postmark: reading the answer: %w", err)
	}
	var res postmarkResult
	jsonErr := json.Unmarshal(raw, &res)
	if resp.StatusCode == http.StatusOK && jsonErr == nil && res.ErrorCode == 0 && res.MessageID != "" {
		return res.MessageID, nil
	}
	msg := res.Message
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	return "", &providers.SendError{Provider: "postmark", Code: res.ErrorCode, Message: msg,
		// Postmark answers 422 for a message it will never take (a bad or
		// inactive address, a bad sender) and 401 for a bad token; 429 and
		// 5xx may clear up, so the caller may retry them.
		Permanent: resp.StatusCode == http.StatusUnprocessableEntity || resp.StatusCode == http.StatusUnauthorized}
}
