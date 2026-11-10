# Fold design: 9. Rooms

## 9. Rooms and the room-suggestion rule (F3, F4)

### 9.1 Optional by design

- `rooms_enabled=false` (the default on a fresh install) hides every room screen and endpoint. Event locations stay free text.
- Turning rooms on keeps existing free-text locations. A one-time "match locations" helper lets admins map common location strings to rooms.

### 9.2 Bookings and conflicts

- A booking belongs to one occurrence. "Book this room for the whole series" creates a booking for each generated occurrence, and for future ones as they are generated.
- A conflict is any confirmed booking for the same room whose stored `padded_range` (`[starts_at - setup, ends_at + teardown)`, computed by the app) overlaps. The database enforces this with an exclusion constraint (5.3, DEC-1), so two leaders booking at the same moment cannot both win. Conflicts block leaders and hosts. Admins may double-book with a reason (audited).
- When an occurrence moves (a per-date change or a series edit, 5.3), its bookings move with it in the same transaction. If the new time conflicts, the booking becomes `requested` (not confirmed), the edit still saves, and the leader or host sees "Room lost on 2 dates: pick another room". Admins may instead keep it with a double-book reason.
- Capacity gives a warning, not a block, when `event.capacity` or the current going count is larger than `room.capacity`.

### 9.3 Room suggestions: the exact rule

When someone picks a room for event E (group G_E, which may be none; visibility V_E; type T_E, the tags of G_E (DEC-100); occurrence time window W), Fold suggests rooms like this.

**Definitions**

- A *usage* is a `RoomBooking` B with status `confirmed`, on an occurrence of some event E_B, starting within the last 12 months or in the future.
- V_B is the **current effective visibility of B's occurrence**: its override, otherwise E_B's stored value (DEC-46/48). V_E likewise uses the date being booked. Group visibility alone never changes the classification of a historical usage. An explicitly opened date counts at its own level; other dates of the hidden series do not.

**Rule 1: which usages count**

- If V_E is `public` or `private`: a usage B counts only if V_B is `public` or `private`. Usages by `invite_only` events never count. So a room that has only ever been used by invite-only groups is never suggested for a public or private event, even when the person choosing is an admin.
- If V_E is `invite_only`: a usage B counts if V_B is `public` or `private`, **or** if E_B belongs to the same group G_E. Usages by *other* invite-only groups never count.
- Usages by E itself (earlier occurrences of the same series) always count.

**Rule 2: which rooms are candidates**

- A room is a candidate if it is `active`, has at least one counting usage, and is not `restricted` (restricted rooms are candidates only when the chooser is an admin).
- Rooms with a confirmed conflict in W stay in the list but are marked "Busy" and sorted last. For a series, conflicts are checked across the next 8 occurrences ("Busy on 2 of 8 dates").

**Rule 3: type filter**

- If T_E is not empty, show only candidates with at least one counting usage whose event's group shares a tag with T_E. A church-wide event has no tags, so the filter doesn't apply.
- If that leaves nothing, fall back to all candidates from Rule 2 and label them "Used by other kinds of events".

**Rule 4: ranking** (highest first)

1. Counting usages by E itself (same series)
2. Counting usages by events of the same group G_E
3. Counting usages whose group shares a tag with T_E
4. Most recent counting usage
5. Total counting usages
6. Capacity closest to (and at least) E's capacity, when set

Show the top 5. Each one says why it was suggested ("Used by this group 6 times").

**Rule 5: never leak**

- The response contains only room fields and counts based on counting usages. It never includes other events' titles, groups, or dates.
- The full room list (without usage history) is always available in the picker, so anyone allowed to book can still choose any non-restricted room by hand. Suggestions are a shortcut, not a gate.

Implementation note for agents: keep the counting-usage test in one function, `rooms.CountingUsagePredicate(E)`, and build the query from it. The test file must cover at least these cases:

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
| 10 | Conflict in W | Listed last, marked Busy |
| 11 | Room R used only by group X (invite-only), but one of X's events was made public (DEC-6, DEC-17); new public event | R suggested; count shows only the public event's usages |
| 12 | Church-wide event (no group) | Same-group ranking is skipped; Rules 1 to 5 otherwise apply |
| 13 | Room R used only by group X (invite-only), but one of X's events was made `private` (DEC-17); new private event | R suggested; count shows only the private event's usages |

