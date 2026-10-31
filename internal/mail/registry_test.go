package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/http/httptest"
	netmail "net/mail"
	"strings"
	"sync"
	"testing"
	"time"

	"fold/internal/providers"
	"fold/internal/testdb"
	"fold/models"
)

// The shared contract every email adapter passes (PLAN 5.6, M3.2): each runs
// against its own fake (the database, a test SMTP server, an httptest
// Postmark built from Postmark's documented API), never a live service.

// sent is what a fake saw for one message.
type sent struct{ to, subject, text, stream, listUnsubscribe, listUnsubscribePost string }

// harness builds one adapter by its registry name, with a way to read what
// its fake received.
type harness struct {
	name  string
	build func(t *testing.T) (providers.EmailSender, func() []sent)
}

func harnesses() []harness {
	return []harness{
		{"devoutbox", func(t *testing.T) (providers.EmailSender, func() []sent) {
			db := testdb.Shared(t)
			s, err := providers.NewEmail("devoutbox", providers.Settings{DB: db, From: "Fold <noreply@example.com>"})
			if err != nil {
				t.Fatal(err)
			}
			return s, func() []sent {
				var rows []models.MailOutbox
				db.Order("id").Find(&rows)
				out := []sent{}
				for _, r := range rows {
					post := ""
					if r.ListUnsubscribe != "" {
						post = "List-Unsubscribe=One-Click" // devoutbox stores the URL; the header pair is implied
					}
					out = append(out, sent{to: r.ToAddress, subject: r.Subject, text: r.Body, listUnsubscribe: r.ListUnsubscribe, listUnsubscribePost: post})
				}
				return out
			}
		}},
		{"smtp", func(t *testing.T) (providers.EmailSender, func() []sent) {
			fake := newFakeSMTP(t)
			s, err := providers.NewEmail("smtp", providers.Settings{From: "Fold <noreply@example.com>",
				Get: func(k string) string {
					if k == "FOLD_SMTP_URL" {
						return "smtp://" + fake.addr()
					}
					return ""
				}})
			if err != nil {
				t.Fatal(err)
			}
			return s, func() []sent {
				// The fake finishes (and its fields are safe to read) when the
				// client quits; a refused send never connects.
				select {
				case <-fake.done:
				case <-time.After(500 * time.Millisecond):
					return []sent{}
				}
				msg, err := netmail.ReadMessage(strings.NewReader(fake.data))
				if err != nil {
					t.Fatal(err)
				}
				body := new(strings.Builder)
				buf := make([]byte, 4096)
				for {
					n, err := msg.Body.Read(buf)
					body.Write(buf[:n])
					if err != nil {
						break
					}
				}
				subject, _ := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
				return []sent{{to: msg.Header.Get("To"), subject: subject, text: strings.TrimRight(body.String(), "\n"),
					listUnsubscribe: strings.TrimSuffix(strings.TrimPrefix(msg.Header.Get("List-Unsubscribe"), "<"), ">"), listUnsubscribePost: msg.Header.Get("List-Unsubscribe-Post")}}
			}
		}},
		{"postmark", func(t *testing.T) (providers.EmailSender, func() []sent) {
			fake := newFakePostmark(t, "server-token")
			saved := postmarkAPI
			postmarkAPI = fake.URL
			t.Cleanup(func() { postmarkAPI = saved })
			s, err := providers.NewEmail("postmark", providers.Settings{From: "Fold <noreply@example.com>",
				Get: func(k string) string {
					if k == "FOLD_POSTMARK_SERVER_TOKEN" {
						return "server-token"
					}
					return ""
				}})
			if err != nil {
				t.Fatal(err)
			}
			return s, fake.messages
		}},
	}
}

func TestEmailAdapterContract(t *testing.T) {
	for _, h := range harnesses() {
		t.Run(h.name, func(t *testing.T) {
			t.Run("is registered under its name", func(t *testing.T) {
				s, _ := h.build(t)
				if got := s.Capabilities().Name; got != h.name {
					t.Fatalf("capabilities name %q", got)
				}
			})
			t.Run("sends one message and returns an id", func(t *testing.T) {
				s, got := h.build(t)
				id, err := s.SendEmail(context.Background(), providers.EmailMessage{To: "sam@example.com", Subject: "Hello there", Text: "plain body"})
				if err != nil || id == "" {
					t.Fatalf("send: %q %v", id, err)
				}
				msgs := got()
				if len(msgs) != 1 || msgs[0].to != "sam@example.com" || msgs[0].subject != "Hello there" || !strings.Contains(msgs[0].text, "plain body") {
					t.Fatalf("received %+v", msgs)
				}
			})
			t.Run("keeps a subject on one line", func(t *testing.T) {
				s, got := h.build(t)
				if _, err := s.SendEmail(context.Background(), providers.EmailMessage{To: "sam@example.com", Subject: "Hi\r\nBcc: x@evil.test", Text: "b"}); err != nil {
					t.Fatal(err)
				}
				if msgs := got(); len(msgs) != 1 || strings.ContainsAny(msgs[0].subject, "\r\n") {
					t.Fatalf("received %+v", msgs)
				}
			})
			t.Run("refuses a recipient with a newline, none, or several", func(t *testing.T) {
				s, got := h.build(t)
				for _, to := range []string{"sam@example.com\r\nBcc: x@evil.test", " ", "sam@example.com, pat@example.com"} {
					if _, err := s.SendEmail(context.Background(), providers.EmailMessage{To: to, Subject: "s", Text: "b"}); err == nil {
						t.Fatalf("sent to %q", to)
					}
				}
				if msgs := got(); len(msgs) != 0 {
					t.Fatalf("something was sent: %+v", msgs)
				}
			})
			t.Run("carries one-click unsubscribe headers only when asked", func(t *testing.T) {
				s, got := h.build(t)
				url := "https://fold.example.org/u/unsubscribe/tok"
				if _, err := s.SendEmail(context.Background(), providers.EmailMessage{To: "sam@example.com", Subject: "s", Text: "b", ListUnsubscribe: url}); err != nil {
					t.Fatal(err)
				}
				if msgs := got(); len(msgs) != 1 || msgs[0].listUnsubscribe != url || msgs[0].listUnsubscribePost != "List-Unsubscribe=One-Click" {
					t.Fatalf("received %+v", msgs)
				}
				for _, bad := range []string{"javascript:alert(1)", "https://x\r\nBcc: evil@x"} {
					if _, err := s.SendEmail(context.Background(), providers.EmailMessage{To: "sam@example.com", Subject: "s", ListUnsubscribe: bad}); err == nil {
						t.Fatalf("sent with link %q", bad)
					}
				}
			})
			t.Run("takes broadcast mail", func(t *testing.T) {
				s, got := h.build(t)
				if _, err := s.SendEmail(context.Background(), providers.EmailMessage{To: "sam@example.com", Subject: "News", Text: "b", Stream: providers.StreamBroadcast}); err != nil {
					t.Fatal(err)
				}
				msgs := got()
				if len(msgs) != 1 || (s.Capabilities().Streams && msgs[0].stream != defaultPostmarkBroadcast) {
					t.Fatalf("received %+v", msgs)
				}
			})
		})
	}
}

// fakePostmark answers POST /email the way Postmark's API documentation
// describes (https://postmarkapp.com/developer/api/email-api): the server
// token in X-Postmark-Server-Token, JSON in and out, ErrorCode 0 and a
// MessageID on success, 401 with ErrorCode 10 for a bad token, and 422 with
// ErrorCode 406 for an inactive recipient.
type fakePostmark struct {
	*httptest.Server
	mu   sync.Mutex
	sent []sent
	// status, when set, is answered instead (with code).
	status, code int
}

func newFakePostmark(t *testing.T, token string) *fakePostmark {
	f := &fakePostmark{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		answer := func(status, code int, msg, id string) {
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(map[string]any{"To": "", "SubmittedAt": "2026-10-31T09:00:00-04:00", "MessageID": id, "ErrorCode": code, "Message": msg})
		}
		if r.Method != http.MethodPost || r.URL.Path != "/email" || r.Header.Get("Accept") != "application/json" || r.Header.Get("Content-Type") != "application/json" {
			answer(http.StatusNotFound, 0, "not found", "")
			return
		}
		if r.Header.Get("X-Postmark-Server-Token") != token {
			answer(http.StatusUnauthorized, 10, "No Account or Server API tokens were supplied in the HTTP headers.", "")
			return
		}
		var body postmarkEmail
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.From == "" || body.To == "" || body.MessageStream == "" {
			answer(http.StatusUnprocessableEntity, 300, "Invalid email request", "")
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.status != 0 {
			answer(f.status, f.code, "Fake failure", "")
			return
		}
		if body.To == "inactive@example.com" {
			answer(http.StatusUnprocessableEntity, 406, "You tried to send to recipient(s) that have been marked as inactive.", "")
			return
		}
		s := sent{to: body.To, subject: body.Subject, text: body.TextBody, stream: body.MessageStream}
		for _, h := range body.Headers {
			switch h.Name {
			case "List-Unsubscribe":
				s.listUnsubscribe = strings.TrimSuffix(strings.TrimPrefix(h.Value, "<"), ">")
			case "List-Unsubscribe-Post":
				s.listUnsubscribePost = h.Value
			}
		}
		f.sent = append(f.sent, s)
		answer(http.StatusOK, 0, "OK", fmt.Sprintf("0a129aee-e1cd-480d-b08d-%012d", len(f.sent)))
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakePostmark) messages() []sent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sent{}, f.sent...)
}

func TestPostmarkStreamsAndErrors(t *testing.T) {
	fake := newFakePostmark(t, "tok")
	p, err := NewPostmark("tok", "Fold <noreply@example.com>", "account-mail", "news")
	if err != nil {
		t.Fatal(err)
	}
	p.baseURL = fake.URL
	ctx := context.Background()
	id1, _ := p.SendEmail(ctx, providers.EmailMessage{To: "a@example.com", Subject: "s", Text: "t"})
	id2, _ := p.SendEmail(ctx, providers.EmailMessage{To: "b@example.com", Subject: "s", Text: "t", Stream: providers.StreamBroadcast})
	if msgs := fake.messages(); id1 == id2 || len(msgs) != 2 || msgs[0].stream != "account-mail" || msgs[1].stream != "news" {
		t.Fatalf("streams: %q %q %+v", id1, id2, msgs)
	}

	var se *providers.SendError
	if _, err := p.SendEmail(ctx, providers.EmailMessage{To: "inactive@example.com", Subject: "s"}); !errors.As(err, &se) || se.Code != 406 || !se.Permanent {
		t.Fatalf("inactive recipient: %v", err)
	}
	bad, _ := NewPostmark("wrong", "Fold <noreply@example.com>", "", "")
	bad.baseURL = fake.URL
	if _, err := bad.SendEmail(ctx, providers.EmailMessage{To: "a@example.com", Subject: "s"}); !errors.As(err, &se) || se.Code != 10 || !se.Permanent {
		t.Fatalf("bad token: %v", err)
	}
	fake.mu.Lock()
	fake.status, fake.code = http.StatusServiceUnavailable, 0
	fake.mu.Unlock()
	if _, err := p.SendEmail(ctx, providers.EmailMessage{To: "a@example.com", Subject: "s"}); !errors.As(err, &se) || se.Permanent {
		t.Fatalf("an outage may clear up: %v", err)
	}
	fake.Close()
	if _, err := p.SendEmail(ctx, providers.EmailMessage{To: "a@example.com", Subject: "s"}); err == nil || errors.As(err, &se) {
		t.Fatalf("unreachable: %v", err)
	}
	if _, err := NewPostmark("", "Fold <noreply@example.com>", "", ""); err == nil || !strings.Contains(err.Error(), "FOLD_POSTMARK_SERVER_TOKEN") {
		t.Fatalf("no token: %v", err)
	}
}

func TestChooseEmailProvider(t *testing.T) {
	env := func(kv ...string) func(string) string {
		m := map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return func(k string) string { return m[k] }
	}
	for _, tc := range []struct {
		get  func(string) string
		dev  bool
		want string
	}{
		{env(), true, "devoutbox"},
		{env(), false, ""},
		{env("FOLD_SMTP_URL", "smtp://x"), false, "smtp"},
		{env("FOLD_SMTP_URL", "smtp://x", "FOLD_POSTMARK_SERVER_TOKEN", "t"), false, "postmark"},
		{env("FOLD_EMAIL_PROVIDER", "SMTP", "FOLD_POSTMARK_SERVER_TOKEN", "t"), false, "smtp"},
		{env("FOLD_EMAIL_PROVIDER", "postmark"), true, "postmark"},
	} {
		if got := Choose(tc.get, tc.dev); got != tc.want {
			t.Errorf("Choose(dev=%v): %q, want %q", tc.dev, got, tc.want)
		}
	}
	if _, err := providers.NewEmail("carrier-pigeon", providers.Settings{}); err == nil || !strings.Contains(err.Error(), "devoutbox, postmark, smtp") {
		t.Fatalf("unknown provider: %v", err)
	}
}

// A 200 Postmark couldn't explain is still sent (never retried into a
// duplicate); an error never quotes the recipient; a redirect is never
// followed with the token.
func TestPostmarkAnswersWeTrustCarefully(t *testing.T) {
	ctx := context.Background()
	var redirected bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected = true }))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body postmarkEmail
		json.NewDecoder(r.Body).Decode(&body)
		switch body.To {
		case "garbled@example.com":
			w.Write([]byte("not json"))
		case "redirect@example.com":
			http.Redirect(w, r, target.URL+"/email", http.StatusTemporaryRedirect)
		default:
			w.WriteHeader(http.StatusUnprocessableEntity)
			w.Write([]byte(`{"ErrorCode":300,"Message":"Invalid 'To' address: '` + body.To + `'."}`))
		}
	}))
	defer srv.Close()
	p, _ := NewPostmark(" tok \n", "Fold <noreply@example.com>", "", "")
	p.baseURL = srv.URL
	if id, err := p.SendEmail(ctx, providers.EmailMessage{To: "garbled@example.com", Subject: "s"}); err != nil || id != "" {
		t.Fatalf("a 200 is sent: %q %v", id, err)
	}
	if _, err := p.SendEmail(ctx, providers.EmailMessage{To: "lee@example.com", Subject: "s"}); err == nil || strings.Contains(err.Error(), "lee@example.com") || !strings.Contains(err.Error(), "<recipient>") {
		t.Fatalf("error quotes the recipient: %v", err)
	}
	if _, err := p.SendEmail(ctx, providers.EmailMessage{To: "redirect@example.com", Subject: "s"}); err == nil || redirected {
		t.Fatalf("followed a redirect: %v (redirected %v)", err, redirected)
	}
	if p.token != "tok" {
		t.Fatalf("token not trimmed: %q", p.token)
	}
}
