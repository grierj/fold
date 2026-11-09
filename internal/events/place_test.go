package events

import "testing"

func TestEffectivePlace(t *testing.T) {
	empty, own := "", "Room 4"
	for _, c := range []struct {
		event    string
		override *string
		group    string
		want     string
	}{
		{"Hall", nil, "Church", "Hall"},
		{"", nil, "Church", "Church"},
		{"", nil, "", ""},
		{"Hall", &own, "Church", "Room 4"},
		{"Hall", &empty, "Church", ""}, // the date says it has no place
	} {
		if got := EffectivePlace(c.event, c.override, c.group); got != c.want {
			t.Errorf("EffectivePlace(%q, %v, %q) = %q, want %q", c.event, c.override, c.group, got, c.want)
		}
	}
}
