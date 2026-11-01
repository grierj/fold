package policy

import "testing"

func TestPushRefusal(t *testing.T) {
	for _, c := range []struct {
		status   string
		churchOn bool
		want     string
	}{
		{"active", true, PushOK},
		{"active", false, PushChurchOff},
		{"guest", true, PushGuest},
		{"guest", false, PushGuest},
		{"pending_consent", true, PushPendingConsent},
		{"pending_consent", false, PushPendingConsent},
		{"disabled", true, PushInactive},
		{"invited", true, PushInactive},
		{"", true, PushInactive},
	} {
		if got := PushRefusal(c.status, c.churchOn); got != c.want {
			t.Errorf("PushRefusal(%q, %v) = %q, want %q", c.status, c.churchOn, got, c.want)
		}
	}
}
