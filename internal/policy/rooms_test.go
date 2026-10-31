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

func TestRoomBookingPolicy(t *testing.T) {
	leader, admin := &Person{ID: 1}, &Person{ID: 2, Admin: true}
	for _, tc := range []struct {
		name                string
		p                   *Person
		manages, restricted bool
		want                bool
	}{
		{"leader, own event", leader, true, false, true},
		{"leader, restricted room", leader, true, true, false},
		{"leader, someone else's event", leader, false, false, false},
		{"admin, restricted room", admin, true, true, true},
		{"admin who doesn't run the event", admin, false, false, false},
	} {
		if got := CanBookRoom(tc.p, tc.manages, tc.restricted); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
	if CanDoubleBookRoom(leader) || !CanDoubleBookRoom(admin) || CanAdjustBookingPadding(leader) || !CanAdjustBookingPadding(admin) {
		t.Error("only admins double-book or change a booking's padding")
	}
}

func TestRoomApprovalPolicy(t *testing.T) {
	leader, admin := &Person{ID: 1}, &Person{ID: 2, Admin: true}
	for _, tc := range []struct {
		name       string
		p          *Person
		approvalOn bool
		want       bool
	}{
		{"leader, approval off", leader, false, false},
		{"leader, approval on", leader, true, true},
		{"admin, approval on", admin, true, false},
		{"admin, approval off", admin, false, false},
	} {
		if got := BookingNeedsApproval(tc.p, tc.approvalOn); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
	if CanDecideRoomRequest(leader) || CanDecideRoomRequest(nil) || !CanDecideRoomRequest(admin) {
		t.Error("only admins decide room requests")
	}
}

func TestRoomCalendarPolicy(t *testing.T) {
	member, teen, admin := &Person{ID: 1}, &Person{ID: 2, Teen: true}, &Person{ID: 3, Admin: true}
	for _, tc := range []struct {
		name                       string
		p                          *Person
		calendar, restricted, wait bool
	}{
		{"signed out", nil, false, false, false},
		{"teen", teen, false, false, false},
		{"member or leader", member, true, false, false},
		{"admin", admin, true, true, true},
	} {
		if got := CanSeeRoomCalendar(tc.p); got != tc.calendar {
			t.Errorf("%s: calendar got %v, want %v", tc.name, got, tc.calendar)
		}
		if got := CanListRoom(tc.p, true); got != tc.restricted {
			t.Errorf("%s: restricted room got %v, want %v", tc.name, got, tc.restricted)
		}
		if !CanListRoom(tc.p, false) {
			t.Errorf("%s: an ordinary room should be listed", tc.name)
		}
		if got := CanSeeWaitingBookings(tc.p); got != tc.wait {
			t.Errorf("%s: waiting bookings got %v, want %v", tc.name, got, tc.wait)
		}
	}
}
