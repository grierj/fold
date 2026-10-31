package mail

import (
	"net/http"
	"os"
	"testing"

	"fold/internal/providers"
)

func readSample(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Payloads from Postmark's webhook documentation, normalized.
func TestParsePostmarkWebhook(t *testing.T) {
	for _, tc := range []struct {
		file      string
		kind      providers.EventKind
		address   string
		permanent bool
		detail    string
	}{
		{"postmark_bounce.json", providers.EventBounced, "john@example.com", true, "HardBounce"},
		{"postmark_soft_bounce.json", providers.EventBounced, "full-mailbox@example.com", false, "SoftBounce"},
		{"postmark_spam_complaint.json", providers.EventComplained, "john@example.com", true, "SpamComplaint"},
		{"postmark_delivery.json", providers.EventDelivered, "john@example.com", false, ""},
	} {
		evs, err := ParsePostmarkWebhook(http.Header{}, readSample(t, tc.file))
		if err != nil || len(evs) != 1 {
			t.Fatalf("%s: %v %v", tc.file, evs, err)
		}
		e := evs[0]
		if e.Kind != tc.kind || e.Address != tc.address || e.Permanent != tc.permanent || e.Detail != tc.detail ||
			e.MessageID == "" || e.At.IsZero() || e.Provider != "postmark" || e.Key == "" {
			t.Errorf("%s: %+v", tc.file, e)
		}
	}
	// Records Fold doesn't use are ignored; a malformed one is an error.
	if evs, err := ParsePostmarkWebhook(http.Header{}, readSample(t, "postmark_open.json")); err != nil || len(evs) != 0 {
		t.Fatalf("open: %v %v", evs, err)
	}
	for _, bad := range []string{"not json", `{"RecordType":"Bounce","MessageID":"x"}`, `{"RecordType":"Bounce","MessageID":"x","Email":"no-at-sign"}`} {
		if _, err := ParsePostmarkWebhook(http.Header{}, []byte(bad)); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
	// A redelivered record has the same key whatever its trace id; two
	// records never share one, even with the same trace id.
	h := http.Header{}
	h.Set("X-PM-Webhook-Trace-Id", "trace-1")
	a, _ := ParsePostmarkWebhook(h, readSample(t, "postmark_bounce.json"))
	b, _ := ParsePostmarkWebhook(h, readSample(t, "postmark_delivery.json"))
	c, _ := ParsePostmarkWebhook(http.Header{}, readSample(t, "postmark_bounce.json"))
	if a[0].Key != c[0].Key || b[0].Key == a[0].Key || len(a[0].Key) < 20 {
		t.Fatalf("keys: %q %q %q", a[0].Key, b[0].Key, c[0].Key)
	}
	if got := limit("caf\u00e9", 4); got != "caf" {
		t.Fatalf("limit split a character: %q", got)
	}
}
