# Fold milestones: Phase 2

### 14.5 Phase 2: Rooms

Rooms are optional (9.1) and touch the database, API and UI together, so every milestone ships as thin slices in this order (14.1): the database change, then the API and internals, then the UI. A slice may land hidden behind `rooms_enabled` until its UI exists. M2.1 (slices a to e), M2.2 (the by-room view and the room feed) and M2.3 (room suggestions) are built, so Phase 2 is complete. In the release table rooms are v0.6, after email (v0.4) and the MVP (v0.5); they may run in parallel with Phase 3 because they share only events and occurrences.

Design defaults for this phase (decided here so slices don't reopen them; change them in review if wrong):

- **Where rooms show.** With rooms on, an event or date with a confirmed booking shows the room as its place ("Fellowship Hall · Main building"); a typed location then shows as a detail after it ("… · north door"), and the room name is the ICS `LOCATION` (8.2). Without a booking, the free-text location shows as today.
- **Room feed tokens** are their own table (`room_feed_tokens(room_id, token_hash, created_by, created_at, revoked_at)`), not a new kind on the per-user `calendar_tokens` (which is one row per user).
- **A `requested` booking holds nothing.** Only `confirmed` bookings are in the exclusion constraint. `rejected` is set only when an admin declines a requested booking. When a leader picks another room for that date, the new booking replaces the requested one.
- **Restricted rooms** (the nursery, say) appear in the by-room view and room feed lists for admins only; everyone else never sees them listed.
- **Phones.** The by-room view shows one room at a time as a day list with a room picker; the column grid starts at `sm` (DEC-67's pattern for Week).
- **Event type for suggestions (9.3 Rule 3)** is the group's tags (DEC-100): no event category field.

**M2.1 Rooms and bookings**

- **M2.1a Room catalog and the rooms switch**
  - Database: `rooms` (5.3 `Room` fields with the DEC-101 default padding; no color, DEC-107) and the `rooms_enabled` setting (default off).
  - API: admin create, edit and deactivate under `/api/v1/admin/rooms` (no delete: history stays), `policy.CanManageRooms`, audited; every room endpoint answers 404 while rooms are off; `GET /site` says whether rooms are on.
  - UI: a Rooms tab in Admin (list, add, edit, deactivate; restricted rooms labelled), and a Rooms on/off switch in admin Settings that saves at once (DEC-98).
  - Acceptance: with rooms off no room UI shows and every room endpoint is 404; only admins manage rooms; deactivating keeps history.
- **M2.1b One booking on one date**
  - Database: `room_bookings` with `padded_range`, the `CHECK` that it matches the times and padding, and the exclusion constraint on confirmed bookings (5.3, 9.2).
  - API: `GET /api/v1/rooms` lists active rooms to people who may book (`policy.CanBookRoom`), without notes for non-admins, restricted rooms for admins only. Book, change and release the room of one occurrence, with the padding copied from the room (DEC-101); `room_busy` (409) names the conflict window but never the other event; admins may double-book with a reason (`room_booking.overlap` audit row); restricted rooms are admin-only; a capacity warning (not a block) when the event's capacity or going count is above the room's. Cancelling a date releases its booking.
  - UI: a Room field beside Location (not under More options, DEC-95) in "Edit this date" and the shared `SeriesEditor`, with busy rooms marked; the date card and event page show the room as the place.
  - Moving a date (per-date reschedule, series edit) moves its booking in the same transaction; a move into a conflict makes it `requested` and sends one `room.lost` notice. This landed in M2.1b because a single date of a series can be booked here.
  - Acceptance: two concurrent bookings of overlapping padded ranges, exactly one wins (database and API); padding edges (touching ranges don't conflict); leaders can't book restricted rooms; the double-book reason is audited; a moved date keeps or loses its room as above.
- **M2.1c Series bookings and moving dates**
  - Database: nullable `events.default_room_id` referencing `rooms` (5.3).
  - API and internals: "Book for the whole series" books every generated date and stores `events.default_room_id`; dates the room is busy on are booked `requested` and listed, not refused (DEC-103). The daily extender books new dates as it generates them, and a new date that conflicts is `requested` with the same `room.lost` notice (moves already landed in M2.1b); series edits that add or restore dates do the same. Feeds put the room name in the ICS `LOCATION` (8.2).
  - UI: the event create forms get the Room field (with "whole series" for a series); the event page lists dates that lost their room with a "Pick a room" action.
  - Acceptance: a series of 10 books 10 dates; editing the series time moves every booking; a moved date that now conflicts is `requested` and named in one notice; the extender books new dates and handles conflicts the same way.
- **M2.1d Booking approval**
  - Database: `room_requests` (one per booking action, DEC-106) and `room_bookings.request_id`; the `room_bookings_need_approval` setting (DEC-102).
  - Setting switch in admin Settings; with it on, leader and host bookings wait as requested bookings on one request (a date, or every free date of a series) and every admin gets one task. Busy dates are lost as before (DEC-103), not requested. Admins' own bookings confirm at once.
  - API: admins list open requests ("Needs a room", approvals only, DEC-105), confirm one (dates that became busy need a double-book reason, or go back to the leader as lost) or reject it with an optional reason (the leaders are told; a series request stops being the series room). Re-picking, releasing or cancelling every date of a request withdraws it and closes its task. The daily extender adds new dates of a waiting series to its request.
  - UI: a "Needs a room" list on the admin Rooms tab; the room controls say a booking is waiting for approval; the event page's "Dates that need a room" also lists dates an admin didn't approve.
  - Acceptance: with approval on, a leader's booking is `requested` and holds nothing until confirmed; confirming a still-conflicting request needs a reason; rejecting notifies; re-picking replaces the requested booking.
- **M2.1e Matching old locations**
  - API: the 9.1 "match locations" helper lists distinct free-text locations with counts and lets an admin map one to a room, which books matching future dates where free and reports the rest. Running it again books nothing twice.
  - UI: the match-locations helper on the admin Rooms tab.
  - Acceptance: the helper never overwrites a leader's typed location and is idempotent.
- Tests across M2.1: conflict table tests (padding edges, moved and restored dates, extender), policy tables for `CanManageRooms` and `CanBookRoom`, migration drift, and a Playwright path for book, conflict and double-book.

**M2.2 By-room view and room feed**

- **M2.2a By-room view**
  - API: `GET /api/v1/calendar/rooms?view=day|week&date=` returns, per active room, the confirmed bookings in range, in church time. A booking the viewer can't see (policy date visibility) is `{startsAt, endsAt, reserved: true}` with no title, group, event id or link; restricted rooms show to admins only. Admins also see bookings waiting for approval, marked (DEC-109).
  - UI: `/calendar/rooms` (a Rooms view beside Agenda, Week and Month when rooms are on): Day is one column per room with time rows from `sm` up, Week is a rooms × days grid (DEC-108), and phones show one room at a time as a day list; "Reserved" blocks are grey with the word, and waiting ones are dashed with "Waiting for approval".
  - Acceptance: members, leaders and admins each see exactly the details section 6 allows; JSON for a hidden booking has no identifying keys (a key-list test); teens get 404 and signed-out visitors 401.
- **M2.2b Room ICS feed**
  - Database: `room_feed_tokens` (5.3).
  - API: admins make, replace or turn off one link per room (`room_feed_tokens`; like the personal feed, the link shows once, only its hash is stored, and a new link turns the old one off); `/cal/rooms/<roomId>/<token>.ics` exports confirmed bookings (never waiting ones, DEC-109), titling a date "Reserved" when its own visibility is invite-only or its group was deleted; the access log keeps the room id and never the token.
  - UI: a "Calendar feed" action on each active room in the admin Rooms tab (create, copy once, make a new link, turn off), behaving like the personal calendar feed card.
  - Acceptance: a revoked token answers 404; the feed shows what an admin would see, except that invite-only and hidden-date bookings are titled "Reserved" with no group or description (8.2).
- Tests across M2.2: the key-list masking tests, and a Playwright smoke test of the by-room view.

**M2.3 Room suggestions**

- **M2.3a Suggestion engine (internal)**
  - `rooms.CountingUsagePredicate(E)` and the query from it (`internal/rooms`), using M1.13 effective per-date visibility (never the group's or event's stored value alone). All five rules of 9.3, with the event's type taken from its group's tags (DEC-100). Built; migration 00050 adds a GiST index on confirmed bookings' `(room_id, padded_range)` for the busy check, since the exclusion constraint's index leaves out double-bookings.
  - Tests: all 13 cases in the 9.3 table, plus explicitly opened and tightened dates and a church-wide event (no tags, so no type filter); a benchmark with 50 rooms and 20,000 bookings under 100 ms on Postgres.
- **M2.3b Suggestions in the picker**
  - API: each room picker's own response (one date, the series, and the create forms' `POST /rooms/check`, which also takes the chosen visibility) carries up to 5 `suggestions` with counts and a reason ("Used by this group 6 times"), and says when they are "Used by other kinds of events" (DEC-110). The JSON never carries other events' titles, groups or dates (a key-list test).
  - UI: all three Room fields (Edit this date, Edit series, the create forms) show "Suggested" first, each with its reason and the picker's own busy note ("in use on 2 of 10 dates", across every date being booked), then the other rooms. Built.
  - Acceptance: suggestions are a shortcut, never a gate: every bookable room stays pickable by hand.

