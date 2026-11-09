package main

import (
	"errors"
	"strings"
	"testing"
)

// Limits count characters after trimming, never bytes, on every door.
func TestFieldLimitsCountCharactersAfterTrimming(t *testing.T) {
	multi := func(n int) string { return strings.Repeat("é", n) } // two bytes each
	for _, tt := range []struct {
		name    string
		check   func(string) (string, error)
		limit   int
		tooLong error
	}{
		{"required name", requiredName, maxNameChars, errNameRequired100},
		{"optional name", optionalName, maxNameChars, errNameTooLong100},
		{"display name", displayName, maxNameChars, errDisplayNameTooLong},
		{"phone", phoneNumber, maxPhoneChars, errPhoneTooLong},
		{"address", postalAddress, maxAddressChars, errAddressTooLong},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := tt.check("  " + multi(tt.limit) + "  "); err != nil || got != multi(tt.limit) {
				t.Fatalf("a full-length value with spaces around it: %q, %v", got, err)
			}
			if _, err := tt.check(multi(tt.limit + 1)); !errors.Is(err, tt.tooLong) {
				t.Fatalf("one character over: %v, want %v", err, tt.tooLong)
			}
		})
	}
	if _, err := requiredName("   "); !errors.Is(err, errNameRequired100) {
		t.Fatalf("a blank required name: %v", err)
	}
	if got, err := optionalName("   "); err != nil || got != "" {
		t.Fatalf("a blank optional name: %q, %v", got, err)
	}
}
