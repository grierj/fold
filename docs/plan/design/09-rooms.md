# Fold design: 9. Rooms

## 9. Rooms and the room-suggestion rule (F3, F4)

Rooms are built (Phase 2, M2.1 to M2.3). Endpoints are in [`docs/API.md`](../../API.md); the busy
test is `internal/rooms/busy.go` and the suggestion engine `internal/rooms/suggest.go`.

### 9.1 Optional by design

- `rooms_enabled` is off by default on a fresh install. Off, every room screen is hidden and every room endpoint but the switch itself answers 404. Event locations stay free text.
- Turning rooms on keeps existing free-text locations. The "match locations" helper (M2.1e) lets admins map a typed place to a room: every free matching upcoming date is booked in it, and a series whose own place matches gets it as its series room. Typed locations are never changed, and running it again books nothing twice.
- Only admins manage rooms. Restricted rooms (the nursery, say) can be booked by admins only (`policy.CanBookRoom`), but a date booked in one still shows the room as its place (DEC-104).

### 9.2 Bookings and conflicts

- A booking belongs to one occurrence. "Book for the whole series" stores the room on the event (`default_room_id`) and books every scheduled future date; dates added later by the daily extender or a series edit are booked the same way. Dates the room is busy on get a `requested` booking that holds nothing and show as needing a room (DEC-103).
- A conflict is any confirmed booking for the same room whose stored `padded_range` (`[starts_at - setup, ends_at + teardown)`, the booking's copy of the room's default setup and teardown, DEC-101) overlaps. The database enforces this with an exclusion constraint (`room_bookings_no_overlap`, 5.3, DEC-1), so two leaders booking at the same moment cannot both win. A conflict blocks leaders and hosts (409 `room_busy`). Admins may double-book with a reason (`allow_overlap`, audited).
- With "Room bookings need an admin's approval" on (`room_bookings_need_approval`, default off, DEC-102), a leader's or host's booking waits as a request until an admin confirms or rejects it; an admin's own bookings confirm at once.
- When an occurrence moves (a per-date change or a series edit, 5.3), its bookings move with it in the same transaction. If the new time conflicts, the booking becomes `requested` (not confirmed), the edit still saves, and the event's leaders or hosts, and the person who moved it, get a "Room lost on <dates>" notice (`room.lost`). An admin can then book the room again over the conflict with a reason; an admin's existing double-booking moves as it is.
- Cancelling a date or deleting an event releases its booking.
- Only events of an active group, or church-wide events, can be booked (DEC-147). For a group that is not active (pending, archived or deleted), single-date and series bookings, new series dates (the daily extender, series edits and restores), an admin confirming a waiting request and location matching book nothing. Existing bookings stay, keep holding the room and can be released, and the series room can be stopped.
- Capacity gives a warning, not a block ("may be too small"), when the date's seat cap or current going count (the event's seat cap on the series picker) is larger than `room.capacity`.

### 9.3 Room suggestions: the exact rule

When someone picks a room for event E (group G_E, which may be none; visibility V_E; type T_E, the tags of G_E (DEC-100); occurrence time window W), Fold suggests rooms like this. Suggestions come with each picker's own room list: the one-date picker, the series picker and the create forms' room check (DEC-110). The rule is frozen as shipped (DEC-141).

**Definitions**

- A *usage* is a `RoomBooking` B with status `confirmed`, on a scheduled occurrence of some event E_B, starting within the last 12 months or in the future. Usages by deleted events, or by events of deleted groups, never count.
- V_B is the **current effective visibility of B's occurrence**: its override, otherwise E_B's stored value (DEC-46/48). Group visibility alone never changes the classification of a historical usage. An explicitly opened date counts at its own level; other dates of the hidden series do not.
- V_E is the visibility of what is being booked: the date's effective visibility on the one-date picker, E's stored value on the series picker, and the visibility chosen on a create form (else what the event would get: its group's, or public for a church-wide event).

**Rule 1: which usages count**

- If V_E is `public` or `private`: a usage B counts only if V_B is `public` or `private`. Usages by `invite_only` events never count. So a room that has only ever been used by invite-only groups is never suggested for a public or private event, even when the person choosing is an admin.
- If V_E is `invite_only`: a usage B counts if V_B is `public` or `private`, **or** if E_B belongs to the same group G_E. Usages by *other* invite-only groups never count.
- Usages by E itself (earlier occurrences of the same series) always count. A create form's new event has none.

**Rule 2: which rooms are candidates**

- A room is a candidate if it is `active`, has at least one counting usage, and is not `restricted` (restricted rooms are candidates only when the chooser is an admin).
- Rooms with a confirmed conflict in W stay in the list but are sorted last and marked "in use at this time". For a series, conflicts are checked across the next 8 occurrences ("in use on 2 of 8 dates").

**Rule 3: type filter**

- If T_E is not empty, show only candidates with at least one counting usage whose event's group shares a tag with T_E. A church-wide event has no tags, so the filter doesn't apply.
- If that leaves nothing, fall back to all candidates from Rule 2 and label them "Used by other kinds of events".

**Rule 4: ranking** (highest first)

1. Counting usages by E itself (same series)
2. Counting usages by events of the same group G_E
3. Counting usages whose group shares a tag with T_E
4. Latest counting usage (the latest booking start, which may be a future date)
5. Total counting usages
6. Capacity closest to (and at least) E's capacity, when set

Show the top 5. Each one says why it was suggested, by its strongest tie: "Used by this event 3 times", "Used by this group 6 times", "Used by similar groups 2 times", or "Used 4 times" ("once" for one).

**Rule 5: never leak**

- The response contains only room ids and counts based on counting usages. It never includes other events' titles, groups, or dates.
- The full room list (without usage history) is always available in the picker, so anyone allowed to book can still choose any non-restricted room by hand. Suggestions are a shortcut, not a gate.

Implementation note for agents: the counting-usage test is one function, `countingUsagePredicate(s Subject)` in `internal/rooms/suggest.go`, and the query is built from it. `TestSuggestionCases` (`internal/rooms/suggest_test.go`) covers these cases:

| Case | Setup | Expected |
|---|---|---|
| 1 | Room R used only by invite-only group X; new public event | R not suggested |
| 2 | Same as 1; new private event created by an admin | R not suggested |
| 3 | Same as 1; new invite-only event in group X | R suggested |
| 4 | Same as 1; new invite-only event in another invite-only group Y | R not suggested |
| 5 | R used by a private event and by group X (invite-only); new public event | R suggested; count shows 1 usage, not 2 |
| 6 | Group X changes from invite-only to private, with its event/date values unchanged | Its invite-only usages still do not count (DEC-48); widening the group never widens its events |
| 7 | Type filter matches nothing | Fallback list, labeled |
| 8 | Restricted room; leader chooses | Not suggested. Admin chooses: suggested |
| 9 | Only usage is older than 12 months | Not suggested |
| 10 | Conflict in W | Listed last, marked in use |
| 11 | Room R used only by group X (invite-only), but one of X's events was made public (DEC-6, DEC-17); new public event | R suggested; count shows only the public event's usages |
| 12 | Church-wide event (no group) | Same-group ranking is skipped; Rules 1 to 5 otherwise apply |
| 13 | Room R used only by group X (invite-only), but one of X's events was made `private` (DEC-17); new private event | R suggested; count shows only the private event's usages |
