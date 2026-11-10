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

// #259: NUL is refused in every text field, and the single-line person fields
// refuse every control character; the multi-line fields keep their tabs and
// newlines. Trimming runs first, so a trailing newline is never a refusal.
func TestTextFieldsRefuseCharactersThatCannotBeSaved(t *testing.T) {
	refused := []string{"Ann\x00", "\x00", "A\x00nn", "Ann\x01", "A\tB", "A\x7fB", "A\u0085B", "A B", "A B"}
	accepted := map[string]string{
		"José": "José", "Zoë": "Zoë", "محمد\u200f": "محمد\u200f", "Ann\u200dB": "Ann\u200dB",
		"  Ann \n": "Ann", strings.Repeat("é", maxNameChars): strings.Repeat("é", maxNameChars),
	}
	for _, tt := range []struct {
		name  string
		check func(string) (string, error)
	}{
		{"required name", requiredName},
		{"optional name", optionalName},
		{"display name", displayName},
		{"phone", phoneNumber},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, in := range refused {
				if _, err := tt.check(in); !errors.Is(err, errTextCharacters) {
					t.Errorf("%q: %v, want errTextCharacters", in, err)
				}
			}
			if tt.name == "phone" {
				return
			}
			for in, want := range accepted {
				if got, err := tt.check(in); err != nil || got != want {
					t.Errorf("%q: %q, %v, want %q", in, got, err, want)
				}
			}
		})
	}
	if err := errTextCharacters; err.Status != 400 || err.Code != "text_characters" {
		t.Fatalf("errTextCharacters: %+v", err)
	}
	if _, err := requiredName(strings.Repeat("a", maxNameChars+1)); !errors.Is(err, errNameRequired100) {
		t.Fatalf("a 101-character required name: %v", err)
	}
	if _, err := optionalName(strings.Repeat("a", maxNameChars+1)); !errors.Is(err, errNameTooLong100) {
		t.Fatalf("a 101-character optional name: %v", err)
	}

	// Multi-line fields: NUL anywhere is refused; the rest of the controls stay.
	for _, in := range []string{"a\x00", "line1\nline2\x00"} {
		if _, err := postalAddress(in); !errors.Is(err, errTextCharacters) {
			t.Errorf("address %q: %v", in, err)
		}
		if _, err := careNotesField.clean(in); !errors.Is(err, errTextCharacters) {
			t.Errorf("care notes %q: %v", in, err)
		}
		if !errors.Is(validateGroupText(in, "", "", ""), errTextCharacters) ||
			!errors.Is(validateGroupText("", in, "", ""), errTextCharacters) ||
			!errors.Is(validateGroupText("", "", in, ""), errTextCharacters) ||
			!errors.Is(validateGroupText("", "", "", in), errTextCharacters) {
			t.Errorf("group text %q", in)
		}
		if !errors.Is(validateEventText(in, "", ""), errTextCharacters) ||
			!errors.Is(validateEventText("", in, ""), errTextCharacters) ||
			!errors.Is(validateEventText("", "", in), errTextCharacters) {
			t.Errorf("event text %q", in)
		}
	}
	for _, in := range []string{"line1\nline2\tx\r\n", "a\vb"} {
		if _, err := postalAddress(in); err != nil {
			t.Errorf("address %q: %v", in, err)
		}
		if _, err := careNotesField.clean(in); err != nil {
			t.Errorf("care notes %q: %v", in, err)
		}
		if err := validateGroupText(in, in, in, in); err != nil {
			t.Errorf("group text %q: %v", in, err)
		}
		if err := validateEventText(in, in, in); err != nil {
			t.Errorf("event text %q: %v", in, err)
		}
	}
}
