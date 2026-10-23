package policy

import "testing"

func TestCanChangeTeenConsent(t *testing.T) {
	for _, tc := range []struct {
		name           string
		actor          *Person
		guardian, want bool
	}{
		{"signed out", nil, false, false},
		{"admin", admin, false, true},
		{"adult without link", adult, false, false},
		{"confirmed guardian", adult, true, true},
		{"teen without link", teen, false, false},
		{"teen cannot be guardian", teen, true, false},
		{"teen cannot be admin", &Person{Admin: true, Teen: true}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanChangeTeenConsent(tc.actor, tc.guardian); got != tc.want {
				t.Fatalf("got %t, want %t", got, tc.want)
			}
		})
	}
}
