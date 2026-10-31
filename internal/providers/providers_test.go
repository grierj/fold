package providers

import (
	"context"
	"strings"
	"testing"
)

type nopEmail struct{}

func (nopEmail) SendEmail(context.Context, EmailMessage) (string, error) { return "id", nil }
func (nopEmail) Capabilities() EmailCaps                                 { return EmailCaps{Name: "nop"} }

func TestEmailRegistry(t *testing.T) {
	RegisterEmail("test-nop", func(s Settings) (EmailSender, error) {
		if s.Get("KEY") != "value" {
			t.Errorf("settings: %q", s.Get("KEY"))
		}
		return nopEmail{}, nil
	})
	s, err := NewEmail("test-nop", Settings{Get: func(k string) string { return map[string]string{"KEY": "value"}[k] }})
	if err != nil || s.Capabilities().Name != "nop" {
		t.Fatalf("new: %v %v", s, err)
	}
	if _, err := NewEmail("missing", Settings{}); err == nil || !strings.Contains(err.Error(), "test-nop") {
		t.Fatalf("unknown name should list the registered ones: %v", err)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("registering a name twice should panic")
		}
	}()
	RegisterEmail("test-nop", func(Settings) (EmailSender, error) { return nopEmail{}, nil })
}

func TestSendError(t *testing.T) {
	e := &SendError{Provider: "postmark", Code: 406, Message: "inactive", Permanent: true}
	if e.Error() != "postmark: inactive (code 406)" {
		t.Fatal(e.Error())
	}
}
