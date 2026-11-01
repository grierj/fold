// Package push sends web push messages (M3.13, DEC-125): RFC 8030 delivery,
// RFC 8291 encryption and RFC 8292 VAPID, with the standard library only.
// The browsers' own push services carry the message; there is no vendor.
//
// An endpoint is a capability: anyone holding it and the keys can push to
// the device. Nothing here logs or returns an endpoint, p256dh, auth, the
// VAPID token or a body, and every error is written without them.
package push

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// MaxPlaintext caps a message before encryption. A push service may not
// answer 413 to a body of 4,096 bytes or less (RFC 8030 7.2), and the
// aes128gcm header, delimiter and tag add 103.
const MaxPlaintext = 3000

// maxResponse is how much of a push service's answer is read (then
// discarded) so the connection can be reused.
const maxResponse = 4 << 10

// ErrTooLarge: the plaintext is over MaxPlaintext.
var ErrTooLarge = errors.New("push: the message is over the 3,000-byte limit")

// Subscription is where and how to reach one device.
type Subscription struct {
	Endpoint string
	P256dh   string
	Auth     string
}

// Urgency is RFC 8030's Urgency header.
type Urgency string

// Urgency values (RFC 8030 5.3).
const (
	UrgencyVeryLow Urgency = "very-low"
	UrgencyLow     Urgency = "low"
	UrgencyNormal  Urgency = "normal"
	UrgencyHigh    Urgency = "high"
)

// Options are the delivery headers. TTL is required by RFC 8030; zero
// means the push service may drop the message if the device is offline.
type Options struct {
	TTL     time.Duration
	Urgency Urgency
	// Topic replaces a pending message with the same topic: at most 32
	// URL-safe base64 characters (RFC 8030 5.4), such as "n123".
	Topic string
}

var topicRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// Payload is the Declarative Web Push message (WebKit, "Meet Declarative
// Web Push"): Safari 18.4+ shows it without a service worker, and other
// browsers hand the same JSON to the service worker's push handler.
type Payload struct {
	WebPush      int          `json:"web_push"`
	Notification Notification `json:"notification"`
}

// Notification is what the device shows. Navigate is an absolute URL on
// Fold's own origin.
type Notification struct {
	Title    string `json:"title"`
	Body     string `json:"body,omitempty"`
	Navigate string `json:"navigate"`
	Tag      string `json:"tag,omitempty"`
	AppBadge *int   `json:"app_badge,omitempty"`
}

// NewPayload wraps n in the declarative shape (web_push 8030).
func NewPayload(n Notification) Payload { return Payload{WebPush: 8030, Notification: n} }

// Status is what happened to a send.
type Status int

// Send results (research 2.2).
const (
	// Accepted: the push service took it (201).
	Accepted Status = iota + 1
	// Gone: the subscription has expired or was revoked (404, 410); delete the device.
	Gone
	// TooLarge: over the cap, or the push service said 413. A bug, not a retry.
	TooLarge
	// RateLimited: 429; retry later, with the jobs backoff.
	RateLimited
	// Temporary: 5xx, a timeout or a network error; retry.
	Temporary
	// Failed: anything else, including 403 and an endpoint off the
	// allow-list; retrying won't help.
	Failed
)

func (s Status) String() string {
	switch s {
	case Accepted:
		return "accepted"
	case Gone:
		return "gone"
	case TooLarge:
		return "too large"
	case RateLimited:
		return "rate limited"
	case Temporary:
		return "temporary failure"
	case Failed:
		return "failed"
	}
	return "unknown"
}

// Result is one send's outcome. Err, when set, never holds the endpoint,
// keys or token.
type Result struct {
	Status Status
	Code   int // the push service's HTTP status, 0 if none
	Err    error
}

// Client sends to push services. Use it through a pointer and reuse it:
// it keeps one outbound client, so connections are reused.
type Client struct {
	// HTTP is the outbound client; nil means NewHTTPClient(), made once.
	// Tests point its transport at a fake push service.
	HTTP *http.Client
	// Hosts is the allow-list; nil means DefaultHosts.
	Hosts *AllowList
	// Subject is the VAPID sub claim: "mailto:..." or an https URL; empty
	// leaves the claim out.
	Subject string
	// Key returns the current VAPID private key. It is called for every
	// send, so a rotated key is used without a restart.
	Key func(ctx context.Context) (*ecdsa.PrivateKey, error)
	// Now is the clock for token expiry; nil means time.Now.
	Now func() time.Time

	defaults sync.Once
}

// fill sets the nil defaults once, so every send shares one transport.
func (c *Client) fill() {
	c.defaults.Do(func() {
		if c.HTTP == nil {
			c.HTTP = NewHTTPClient()
		}
		if c.Hosts == nil {
			c.Hosts = NewAllowList()
		}
	})
}

func fail(status Status, err error) Result { return Result{Status: status, Err: err} }

// Send encrypts payload for sub and posts it to the push service. The cap,
// the allow-list and the topic are checked before any connection.
func (c *Client) Send(ctx context.Context, sub Subscription, payload []byte, opts Options) Result {
	if len(payload) > MaxPlaintext {
		return fail(TooLarge, ErrTooLarge)
	}
	c.fill()
	if err := c.Hosts.Allowed(sub.Endpoint); err != nil {
		return fail(Failed, err)
	}
	if opts.Topic != "" && !topicRe.MatchString(opts.Topic) {
		return fail(Failed, errors.New("push: a topic is at most 32 URL-safe base64 characters"))
	}
	if c.Key == nil {
		return fail(Failed, errors.New("push: no VAPID key"))
	}
	key, err := c.Key(ctx)
	if err != nil {
		// The settings row couldn't be read; the next attempt may.
		return fail(Temporary, fmt.Errorf("push: load the VAPID key: %w", err))
	}
	body, err := Encrypt(payload, sub.P256dh, sub.Auth)
	if err != nil {
		return fail(Failed, err)
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	auth, err := vapidHeader(key, sub.Endpoint, c.Subject, now())
	if err != nil {
		return fail(Failed, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return fail(Failed, errors.New("push: the endpoint isn't a URL"))
	}
	req.Header.Set("TTL", strconv.FormatInt(int64(opts.TTL/time.Second), 10))
	if opts.Urgency != "" {
		req.Header.Set("Urgency", string(opts.Urgency))
	}
	if opts.Topic != "" {
		req.Header.Set("Topic", opts.Topic)
	}
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Authorization", auth)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return transportFailure(err)
	}
	defer resp.Body.Close()
	// Read a little so the connection can be reused; never keep it.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponse))
	return classify(resp)
}

// classify maps a push service's answer to a Result.
func classify(resp *http.Response) Result {
	code := resp.StatusCode
	r := Result{Code: code}
	switch {
	case code >= 200 && code < 300:
		r.Status = Accepted
		return r
	case code == http.StatusNotFound || code == http.StatusGone:
		r.Status = Gone
	case code == http.StatusRequestEntityTooLarge:
		r.Status = TooLarge
	case code == http.StatusTooManyRequests:
		// Retry-After isn't read: a job can't ask for its own retry time,
		// so the jobs backoff (30 s doubling to an hour) spaces retries.
		r.Status = RateLimited
	case code >= 500:
		r.Status = Temporary
	default:
		// 403 (a key mismatch) and other 4xx, and a redirect, which is
		// never followed. The body isn't read for a reason.
		r.Status = Failed
	}
	r.Err = fmt.Errorf("push: the push service answered %d (%s)", code, r.Status)
	return r
}

// transportFailure classifies an error from the HTTP client, dropping the
// *url.Error wrapper that would repeat the endpoint.
func transportFailure(err error) Result {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	if errors.Is(err, ErrBlockedAddress) {
		return fail(Failed, ErrBlockedAddress)
	}
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return fail(Temporary, errors.New("push: the push service timed out"))
	}
	var oe *net.OpError
	var de *net.DNSError
	switch {
	case errors.As(err, &de):
		return fail(Temporary, fmt.Errorf("push: couldn't resolve the push service: %s", de.Err))
	case errors.As(err, &oe):
		return fail(Temporary, fmt.Errorf("push: couldn't reach the push service: %s %s", oe.Op, causeOf(oe)))
	}
	return fail(Temporary, errors.New("push: the request to the push service failed"))
}

// causeOf is an OpError's innermost text, which names an address at most.
func causeOf(oe *net.OpError) string {
	if oe.Err == nil {
		return ""
	}
	return oe.Err.Error()
}

// MarshalPayload is p as JSON, refused when over MaxPlaintext.
func MarshalPayload(p Payload) ([]byte, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	if len(b) > MaxPlaintext {
		return nil, ErrTooLarge
	}
	return b, nil
}
