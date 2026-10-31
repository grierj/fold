package content

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type smsPiece struct {
	Text  string `json:"text"`
	Times int    `json:"times"`
}

func (p smsPiece) build() string {
	if p.Times == 0 {
		return p.Text
	}
	return strings.Repeat(p.Text, p.Times)
}

type smsCases struct {
	Segments []struct {
		Name     string     `json:"name"`
		Church   string     `json:"church"`
		Text     string     `json:"text"`
		Times    int        `json:"times"`
		Parts    []smsPiece `json:"parts"`
		Encoding string     `json:"encoding"`
		Units    int        `json:"units"`
		Segments int        `json:"segments"`
	} `json:"segments"`
	Donation []struct {
		Text string `json:"text"`
		Warn bool   `json:"warn"`
	} `json:"donation"`
}

// The cases the composer's TypeScript counter runs too (web/src/lib/sms.ts),
// so the live count and the server's agree (M3.4 acceptance).
func loadSMSCases(t *testing.T) smsCases {
	t.Helper()
	raw, err := os.ReadFile("../../web/src/lib/sms-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var c smsCases
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Segments) == 0 || len(c.Donation) == 0 {
		t.Fatal("no cases")
	}
	return c
}

func TestCountSMS(t *testing.T) {
	for _, tc := range loadSMSCases(t).Segments {
		text := smsPiece{tc.Text, tc.Times}.build()
		if tc.Parts != nil {
			text = ""
			for _, p := range tc.Parts {
				text += p.build()
			}
		}
		if tc.Church != "" {
			text = FramedSMS(tc.Church, text)
		}
		got := CountSMS(text)
		if got.Encoding != tc.Encoding || got.Units != tc.Units || got.Segments != tc.Segments {
			t.Errorf("%s: got %+v, want %s %d units %d segments", tc.Name, got, tc.Encoding, tc.Units, tc.Segments)
		}
	}
}

func TestDonationAsk(t *testing.T) {
	for _, tc := range loadSMSCases(t).Donation {
		if got := DonationAsk(tc.Text); got != tc.Warn {
			t.Errorf("%q: warn %v, want %v", tc.Text, got, tc.Warn)
		}
	}
}
