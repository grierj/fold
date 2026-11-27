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
)

// Postmark is the default adapter: plain REST with a server token.
type Postmark struct {
	Token string
	From  string
	// api is Postmark's root; tests point it at a fake.
	api    string
	client *http.Client
}

func NewPostmark(token, from string) *Postmark {
	return &Postmark{Token: strings.TrimSpace(token), From: from, api: "https://api.postmarkapp.com",
		client: &http.Client{Timeout: 15 * time.Second,
			// Never follow a redirect: it would send the token elsewhere.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

type postmarkEmail struct {
	From          string `json:"From"`
	To            string `json:"To"`
	Subject       string `json:"Subject"`
	TextBody      string `json:"TextBody"`
	HtmlBody      string `json:"HtmlBody,omitempty"`
	MessageStream string `json:"MessageStream"`
}

type postmarkResult struct {
	ErrorCode int    `json:"ErrorCode"`
	Message   string `json:"Message"`
	MessageID string `json:"MessageID"`
}

func (p *Postmark) Send(ctx context.Context, m Message) (string, error) {
	if err := check(m); err != nil {
		return "", err
	}
	if p.Token == "" {
		return "", refused{errors.New("mail: Postmark needs a server token")}
	}
	if !ValidAddress(p.From) {
		return "", refused{errors.New("mail: the From address is not one plain address")}
	}
	body, err := json.Marshal(postmarkEmail{From: fromHeader(p.From, m.FromName), To: m.To, Subject: oneLine(m.Subject),
		TextBody: m.Text, HtmlBody: m.HTML, MessageStream: "outbound"})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.api+"/email", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Postmark-Server-Token", p.Token)
	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("mail: could not reach Postmark: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var res postmarkResult
	jsonErr := json.Unmarshal(raw, &res)
	if resp.StatusCode == http.StatusOK && (jsonErr != nil || res.ErrorCode == 0) {
		// Postmark took it. An unreadable answer still means sent, so it is
		// never retried into a duplicate.
		return res.MessageID, nil
	}
	msg := res.Message
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	// 422 is a message Postmark will never take, 401 a bad token; 429 and
	// 5xx may clear up.
	return "", &SendError{Provider: "Postmark", Message: msg,
		Permanent: resp.StatusCode == http.StatusUnprocessableEntity || resp.StatusCode == http.StatusUnauthorized}
}
