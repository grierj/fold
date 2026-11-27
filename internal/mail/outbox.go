package mail

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Outbox keeps sent mail in memory, newest last. It is the sender in
// development (shown at /dev/outbox) and in tests. It holds sign-in links, so
// the server refuses to run in development mode on a public address.
type Outbox struct {
	mu   sync.Mutex
	sent []Sent
	// Fail, when set, makes the next sends fail with it (tests).
	Fail error
}

type Sent struct {
	ID string
	Message
	At time.Time
}

func (o *Outbox) Send(_ context.Context, m Message) (string, error) {
	if err := check(m); err != nil {
		return "", err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.Fail != nil {
		return "", o.Fail
	}
	id := fmt.Sprintf("outbox-%d", len(o.sent)+1)
	o.sent = append(o.sent, Sent{ID: id, Message: m, At: time.Now()})
	if len(o.sent) > 200 {
		o.sent = o.sent[len(o.sent)-200:]
	}
	return id, nil
}

// All is every kept message, newest first.
func (o *Outbox) All() []Sent {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]Sent, len(o.sent))
	for i, s := range o.sent {
		out[len(o.sent)-1-i] = s
	}
	return out
}

// LatestTo is the newest message to an address.
func (o *Outbox) LatestTo(addr string) (Sent, bool) {
	for _, s := range o.All() {
		if s.To == addr {
			return s, true
		}
	}
	return Sent{}, false
}
