package policy

// Rooms (M2.1, section 6, 9.1). Only admins manage the room catalog.

// CanManageRooms: add, edit and deactivate rooms, and turn rooms on or off.
func CanManageRooms(p *Person) bool { return IsAdmin(p) }
