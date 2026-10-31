package policy

// Rooms (M2.1, section 6, 9.1). Only admins manage the room catalog.

// CanManageRooms: add, edit and deactivate rooms, and turn rooms on or off.
func CanManageRooms(p *Person) bool { return IsAdmin(p) }

// CanBookRoom: whoever runs an event books rooms for its dates; restricted
// rooms (the nursery, say) are for admins only (section 6, 9.1).
func CanBookRoom(p *Person, managesEvent, restricted bool) bool {
	return managesEvent && (!restricted || IsAdmin(p))
}

// CanDoubleBookRoom: only an admin books over a conflict, with a reason
// that is audited (9.2).
func CanDoubleBookRoom(p *Person) bool { return IsAdmin(p) }

// CanAdjustBookingPadding: only an admin changes one booking's setup and
// teardown time; everyone else gets the room's defaults (DEC-101).
func CanAdjustBookingPadding(p *Person) bool { return IsAdmin(p) }

// BookingNeedsApproval: with "Room bookings need an admin's approval" on, a
// leader's or host's booking waits for an admin; an admin's own bookings
// confirm at once (DEC-102).
func BookingNeedsApproval(p *Person, approvalOn bool) bool { return approvalOn && !IsAdmin(p) }

// CanDecideRoomRequest: only admins confirm or reject a room request.
func CanDecideRoomRequest(p *Person) bool { return IsAdmin(p) }

// CanSeeRoomCalendar: the by-room view and its "Reserved" blocks are for
// signed-in adults (section 6). Teens and guests never see it.
func CanSeeRoomCalendar(p *Person) bool { return p != nil && !p.Teen }

// CanListRoom: a restricted room is listed in the by-room view only for
// admins (DEC-104). Its name still shows as the place of a date anyone can
// see.
func CanListRoom(p *Person, restricted bool) bool { return !restricted || IsAdmin(p) }

// CanSeeWaitingBookings: bookings waiting for an admin's approval hold
// nothing, and only admins see them in the by-room view (DEC-109).
func CanSeeWaitingBookings(p *Person) bool { return IsAdmin(p) }
