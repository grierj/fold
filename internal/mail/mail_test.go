package mail

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHeadersCannotBeInjected(t *testing.T) {
	if got := fromHeader("a@example.org", "Pat\r\nBcc: x@evil.test"); strings.ContainsAny(got, "\r\n") {
		t.Fatalf("From header %q", got)
	}
	if oneLine("a\r\nb") != "a  b" {
		t.Fatal("oneLine")
	}
	for _, bad := range []string{"", "a@b", "a@b.org, c@d.org", "Pat <a@b.org>", "a@b.org\r\nBcc: x@y.org"} {
		if ValidAddress(bad) {
			t.Errorf("ValidAddress(%q)", bad)
		}
	}
	if !ValidAddress("pat@example.org") {
		t.Error("a plain address")
	}
}

func TestLayoutEscapes(t *testing.T) {
	_, html, err := Email{Church: "<script>x</script>", Subject: "s", Before: []string{"<b>hi</b>"},
		Button: &Button{Label: "Go", URL: "https://example.org/l/abc"}}.Render()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, "<script>") || strings.Contains(html, "<b>hi") {
		t.Fatal("layout did not escape")
	}
	if !strings.Contains(html, `href="https://example.org/l/abc"`) {
		t.Fatal("button link missing")
	}
}

func TestPostmark(t *testing.T) {
	var got postmarkEmail
	var token string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token = r.Header.Get("X-Postmark-Server-Token")
		json.NewDecoder(r.Body).Decode(&got)
		if got.To == "bad@example.org" {
			w.WriteHeader(422)
			w.Write([]byte(`{"ErrorCode":406,"Message":"Inactive recipient"}`))
			return
		}
		w.Write([]byte(`{"ErrorCode":0,"MessageID":"pm-1"}`))
	}))
	defer srv.Close()
	p := NewPostmark("tok", "office@example.org")
	p.api = srv.URL
	id, err := p.Send(context.Background(), Message{To: "pat@example.org", Subject: "Hi\nthere", Text: "x", FromName: "Cloverdale"})
	if err != nil || id != "pm-1" || token != "tok" || got.Subject != "Hi there" || got.From != `"Cloverdale" <office@example.org>` {
		t.Fatalf("send: %v %q %q %+v", err, id, token, got)
	}
	_, err = p.Send(context.Background(), Message{To: "bad@example.org", Subject: "s", Text: "x"})
	if !IsPermanent(err) {
		t.Fatalf("422 should be permanent: %v", err)
	}
}

func TestSMTPRefusesPlaintext(t *testing.T) {
	srv := fakeSMTP(t, false)
	msg := Message{To: "pat@example.org", Subject: "s", Text: "x"}
	// "localhost." reaches the fake on loopback but is not the loopback
	// exception, so it stands for any outside server without STARTTLS.
	remote := &SMTP{Host: "localhost.", Port: srv.port, From: "office@example.org"}
	_, err := remote.Send(context.Background(), msg)
	if err == nil || !IsPermanent(err) || !strings.Contains(err.Error(), "encryption") {
		t.Fatalf("a server without STARTTLS: %v", err)
	}
	local := &SMTP{Host: "127.0.0.1", Port: srv.port, From: "office@example.org"}
	if _, err := local.Send(context.Background(), msg); err != nil {
		t.Fatalf("a mail catcher on loopback: %v", err)
	}
	if !loopback("localhost") || loopback("mail.example.org") || loopback("10.0.0.1") {
		t.Fatal("loopback")
	}
}

func TestOutboxFail(t *testing.T) {
	o := &Outbox{Fail: errors.New("down")}
	if _, err := o.Send(context.Background(), Message{To: "pat@example.org"}); err == nil {
		t.Fatal("Fail ignored")
	}
}
