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
