package mail

import (
	"context"
	"testing"
	"time"

	"fold/internal/providers"
	"fold/internal/testdb"
	"fold/models"
)

// A hard bounce or a manual suppression stops all mail; a complaint stops
// optional and broadcast mail but not account mail; a cleared one stops
// nothing (DEC-112).
func TestSuppressionsStopMail(t *testing.T) {
	db := testdb.Shared(t)
	s := WithSuppressions(db, NewDevOutbox(db, "Fold <noreply@example.com>"))
	now := time.Now()
	for _, row := range []models.Suppression{
		{Channel: "email", Address: "gone@example.com", Reason: models.SuppressHardBounce},
		{Channel: "email", Address: "manual@example.com", Reason: models.SuppressManual},
		{Channel: "email", Address: "spam@example.com", Reason: models.SuppressComplaint},
		{Channel: "email", Address: "cleared@example.com", Reason: models.SuppressHardBounce, ClearedAt: &now},
	} {
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	for _, tc := range []struct {
		to       string
		optional bool
		stream   providers.Stream
		sends    bool
	}{
		{"Gone@Example.com ", false, "", false},
		{"manual@example.com", false, "", false},
		{"spam@example.com", false, "", true},
		{"spam@example.com", true, "", false},
		{"spam@example.com", false, providers.StreamBroadcast, false},
		{"cleared@example.com", true, "", true},
		{"fine@example.com", true, providers.StreamBroadcast, true},
	} {
		_, err := s.SendEmail(ctx, providers.EmailMessage{To: tc.to, Subject: "s", Text: "t", Optional: tc.optional, Stream: tc.stream})
		if tc.sends && err != nil || !tc.sends && !IsSuppressed(err) {
			t.Errorf("%s optional=%v stream=%q: %v", tc.to, tc.optional, tc.stream, err)
		}
	}
	if Unwrap(s) != s.inner || Unwrap(s.inner) != s.inner {
		t.Fatal("Unwrap")
	}
}
