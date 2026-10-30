package policy

import "testing"

func TestCanManageRooms(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    *Person
		want bool
	}{
		{"nobody", nil, false},
		{"member", &Person{ID: 1}, false},
		{"admin", &Person{ID: 2, Admin: true}, true},
	} {
		if got := CanManageRooms(tc.p); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
