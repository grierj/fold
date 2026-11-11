# Fold milestones: Phase 2

### 14.5 Phase 2: Rooms

Phase 2 (v0.6, rooms) is done. The full as-delivered text, with the phase's design defaults, is in [provenance/delivered-phases-0-2.md](../provenance/delivered-phases-0-2.md); section 9, [docs/API.md](../../API.md) and the code are the current truth.

**M2.1 Rooms and bookings**
Done (PR #191, #192, #193, #194, #195).
- An optional room catalog behind the `rooms_enabled` switch, managed by admins (9.1, DEC-101, DEC-107).
- Booking a room for one date or a whole series, with conflicts refused by the database and admin double-booking with a reason (9.2, DEC-103).
- Bookings move with their dates; a date whose room is taken at the new time loses it and the organizers are told (9.2).
- Optional admin approval of leader and host bookings (DEC-102, DEC-105, DEC-106).
- A "match locations" helper that maps old typed locations to rooms (9.1).

**M2.2 By-room view and room feed**
Done (PR #197, #198).
- The by-room view at `/calendar/rooms`, with dates the viewer can't see shown as "Reserved" (8.1, DEC-108, DEC-109).
- One admin-issued ICS feed per room (8.2).

**M2.3 Room suggestions**
Done (PR #199, #200).
- Room suggestions by the 9.3 rule, judged by each date's effective visibility and the group's tags (DEC-100, DEC-110).
- Up to five suggestions with a reason in all three room pickers; every bookable room stays pickable by hand.
