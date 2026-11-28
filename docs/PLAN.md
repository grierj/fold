# Plan

What to build, in what order, and how to tell a slice is done. This page adds no rules: every rule
is in `docs/DECISIONS.md` (cited here as R, P or a Fold DEC number). If this page and a decision
disagree, the decision wins and this page is fixed.

Slices 0 to 2 are written in detail. Each later slice gets its detail in a pull request to
this page when the slice before it is merged, so the plan is written with what was just learned
and never runs far ahead of the code.

## Status

The only place status is kept. Update the row in the pull request that changes it.

| Slice | What | Status |
|---|---|---|
| 0 | Foundation and install | Done (v0.2.0). The timed install by a person waits for an installer who is not Grier |
| 1 | People, groups, approvals, notices | Done (v0.3.0). A real volunteer admin approving from their phone is in `docs/FIRST-CHURCH-CHECKS.md` |
| 2 | Events, dates, RSVP, calendar | Done (R33 to R52). Measured below. Inviting one person to one private date waits (R51); the iPhone feed check is in `docs/FIRST-CHURCH-CHECKS.md` |
| 3 | Announcements, reminders, email choices | Not started |
| 4 | First-church readiness. **Pilot goes live.** | Not started |
| 5 | Households, children, teens | Waits for the pilot |
| 6 | Rooms | Waits for the pilot |
| 7 on | Chosen by the pilot church | |

## Two gates

- **After slice 2: measure.** Count lines, tables, routes and test time against the budgets in
  `AGENTS.md`, and time an install from a blank VPS. This is the test of whether starting fresh
  was worth it. If the result is not clearly smaller than Fold for the same behaviour, stop and
  tell Grier before going on.
- **Slice 4 is not done until a church is using it.** Slices 5 and later do not start before that.
  Only Grier can waive this. (Fold's own freeze of this kind lasted 17 days.)

## Settled before slice 0

Grier decided these on 2026-11-26 (`docs/DECISIONS.md` R3, R5 and R8 to R17):

| Question | Answer |
|---|---|
| Sign in by emailed link? | Yes. A link lasts 24 hours; a session lasts a week and renews on each visit (R8) |
| Own HTTPS certificate, or a proxy? | Caddy as the proxy (R9) |
| A component framework? | None to start (R10) |
| Forward-only migrations? | Yes (R11) |
| The name | Becomes Fold again; working name until then (R5) |
| The pilot | Cloverdale Church of God, Boise, Idaho (R12) |
| Budgets | 35 tables, 200 routes (R13) |
| The Go module name | `fold` (R14) |
| Does making something more private remove anyone? | Nobody (R3, confirmed) |
| May a guest sign in by link? | Yes, like everyone else (R15) |
| Opening the audit log with one admin | Every leader of an active group is emailed, then it is released; refused if no one else leads an active group (R16, R17) |

**Still to settle, and when:**

| Question | Needed by |
|---|---|
| Who at the pilot church does the hands-on install. Grier will be there either way (R18) and hopes a volunteer from the church does it; nobody is chosen yet | The install at the pilot church, slice 4 |

**The first agent checks, and records the answers in the first pull request:**

- A SQLite driver that builds with `CGO_ENABLED=0`, and whether a migration runner is worth a
  dependency.
- That the chosen fragment-update script runs with script evaluation off under the strict content
  security policy.
- That the stylesheet builds without Node.

**Answers (slice 0 pull request):**

- **SQLite driver:** `modernc.org/sqlite` v1.63.0, pure Go; the binary builds with
  `CGO_ENABLED=0`. It also exposes SQLite's backup interface, which the restore uses to
  replace the live database in one step (tested with readers open).
- **Migration runner:** not worth a dependency. Forward-only files in `migrations/`, the
  version kept in SQLite's `user_version`, about 100 lines in `internal/db/migrate.go`.
- **Fragment updates:** htmx 2.0.11, vendored (checked against the npm integrity hash), with
  `allowEval`, `allowScriptTags`, `includeIndicatorStyles` and the history cache off. The
  browser gates run a fragment swap under the real policy header and assert no
  `securitypolicyviolation` event.
- **Stylesheet:** the Tailwind 4.3.5 standalone binary builds it in under a second, no Node.
  The built file is committed; `make css-check` fails when it is stale.
- **Rate limits (`docs/HAZARDS.md` section 9):** counters in memory in the one process,
  never in the database; a restart forgets them. See `internal/ratelimit`.

---

## Slice 0: foundation and install

**End state.** A volunteer with a new VPS and a domain starts one compose file (the app, and Caddy
for HTTPS). They open the site,
enter the setup code from the log, name the church and its time zone, enter their own name and
email, and enter mail settings. The site sends them a message, and setup finishes only when they
open that message and press its button. They are then signed in as the first admin, on a home page
that says there is nothing here yet. They can sign out and sign in again by emailed link. A backup
is taken every night without their doing anything, and one command restores it.

**Also true, though nobody sees it:**

- Every change goes through the one write executor, which loads the actor fresh and writes the
  sealed audit row in the same transaction.
- Every person has a random identifier that is never reused.
- The audit chain's checkpoint is written into each backup.
- A restore ends every session, expires every outstanding link and writes its own audit row.
- The security plumbing is in place for every later slice: escaping templates, the strict policy
  headers, CSRF on every POST, body limits and timeouts, silent rate limits, a log that redacts
  secrets, `no-store` on signed-in pages.
- The component gallery shows every control in every state.

**Checks.** A = an agent can run it. P = only a person can.

- A: a stranger who reaches a fresh install without the setup code cannot create the admin, and
  two people finishing setup at once produce one admin.
- A: setup cannot finish while the test message is undelivered.
- A: a sign-in link works once, stops working when a newer one is requested, and does nothing when
  merely opened.
- A: the sign-in form answers the same for an address with an account and one without.
- A: editing or deleting any audit row is refused, and the chain check reports a row changed
  behind its back.
- A: back up, change something, restore: the change is gone, sessions are ended, the audit log
  says a restore happened.
- A: the gallery and every page pass the accessibility, layout and contrast gates at 390 and
  1280 px.
- A: `go vet`, the linter, the vulnerability check and the tests pass, in under three minutes with
  no container.
- P: install from a blank VPS, timed. Target: 15 minutes to the signed-in home page. Grier runs
  this one on a test server; the pilot church's own install comes in slice 4 (R18).
- P: the test message and a sign-in link arrive in a Gmail inbox and an Outlook inbox, not in spam.
- P: the pages look right on a real phone.

**Rests on.** R1, R4, R6 to R11, P1, P2, P10 (the identifier only), P16, P17.

**Not in this slice.** Any member besides the first admin. The second factor (slice 4). Opening
the audit log (slice 4).

---

## Slice 1: people, groups, approvals, notices

**End state.** Someone visits the site and sees its public groups. They ask for an account and
are told an admin will review it. The admin gets a notice at the bell and by email, approves, and
the person is told and can sign in. A leader or an admin can invite someone by email, and that
person skips the review. A member starts a group; it waits; an admin approves or rejects it with
an optional message; the leader hears. Members find groups by searching, join the ones they may
see, and leave them. A leader sees the roster, adds a person by name, makes someone a co-leader,
and removes a member. An admin can disable and re-enable an account, choosing a successor first
when that account is a group's only leader.

**Checks.**

- A: the policy tables for seeing a group, joining, leading, removing and the group's status moves
  pass, carried from Fold and changed where R3 and P6 say.
- A: an invite-only group answers exactly like a missing page to someone outside it, on every
  page and in search.
- A: the account request form gives one answer whether or not the address has an account, and
  cannot be used to flood an address with mail.
- A: a group's leader who is disabled or loses the role mid-request cannot finish the action.
- A: only adults can ask for an account in this slice; anyone younger is told accounts for
  under-18s are not open yet. (Teens arrive in slice 5.)
- A: a notice never contains an email address or phone number, and is worded when read.
- A: making a group more private removes nobody and lists who is affected (R3).
- P: a real volunteer admin approves a real request from their phone.

**Rests on.** R3, R15, P5, P6, P12, and from section 3
of the decisions: "People and accounts" except teens and erasure, all of "Groups", and the first
four lines of "Notices, announcements and email".

**Not in this slice.** Events. Filters by meeting day or time (slice 2) or childcare (slice 5).
Email choices and unsubscribe (slice 3); until then the only email is required account mail.

---

## Slice 2: events, dates, RSVP, calendar

**End state.** A leader schedules a one-off or repeating event for their group, in the church's
time zone, and an admin creates a church-wide event and names its hosts. A member opens a date and
can tell at a glance what it is, when, where, whether they are in, who else is coming and what is
different about this date. They answer Going, Maybe or Can't go, for one date, this and following,
or all future dates. Seats and a waitlist work, and anyone moved up is told. A stranger RSVPs to a
public event by emailed link and can manage that one date. The calendar has Agenda, Week and
Month, Home leads with what is coming up, and a personal feed subscribes from a phone. The group
finder filters by meeting day and time of day, and group lists sort by next meeting. Leaving a
group takes the person off its upcoming dates. Cancelling a date or deleting an event tells the
people signed up. Then the measuring gate.

**Milestones**, one pull request each, in this order:

1. **Events and dates**, in two pull requests.

   **1a. A group's events and their dates.** The tables; the event clock (`internal/events`:
   expansion in the event's zone, the 2,000-date cap, the series edit; see
   `docs/CARRYOVER.md` section 1, written fresh for R33's four repeats instead of carrying
   `rrule-go`); the policy for seeing and running an event; the event editor (title,
   description, When, place, then more options with visibility, DEC-95, DEC-51), with repeats as
   R33 and R37 list and times on a daylight-saving night as R34 says. A new event starts in the
   church's zone (DEC-50) and copies its group's visibility (DEC-48); in 1a it may be made more
   private, never wider. The series is edited on the event page only (DEC-161), and dates it
   drops are cancelled and restored (DEC-91), asking first. The date page, without answers yet,
   shows times in the viewer's zone with one quiet line when the event's clock differs, and edit
   forms name the event's zone (DEC-57). The group page lists what is coming up. Dates are stored
   18 months ahead, Fold's horizon, and extended by a nightly job. Seats have a column and no
   page until milestone 2; ICS output arrives with the feeds (milestone 4).

   **1b. Everything else events need before answers.** One date can be cancelled, restored, or
   opened wider than its series (P7). Church-wide events and their hosts (DEC-7). Opening an
   event wider than its group is audited, tells the other leaders and admins, and never shows an
   invite-only group's name, place or members (DEC-6, DEC-17, DEC-28). Each person gets a time
   zone (DEC-58, R29): the church's by default, set from the browser the first time, changed on
   the profile.
2. **Answers, seats and the waitlist**, in two pull requests: **2a** is answers (the states and
   their moves, who may answer, the series answer, counts and names, the video link, leaving a
   group, disabled accounts); **2b** is seats, the waitlist and its one promotion function, and
   every flow that tells the people signed up. Going, Maybe, Can't go, and Cancelled as its own state
   (DEC-54). Membership is a standing invitation, a church-wide event invites every member, and a
   public event is not an invitation until the person answers (DEC-72, DEC-73). The series dialog
   says that dates added later are not answered for them, and a status line stays after each
   answer (DEC-71, DEC-92). Counts first, names to those allowed, and Can't go only to the people
   running the event (DEC-63, DEC-64). A video link, a link out (DEC-16), shown only to those who
   answered Going or Maybe and those running the event (R41). Optional seats, and one function every seat change passes
   through, which moves the next waiting person up and tells them (DEC-84, `docs/HAZARDS.md`
   section 7). Leaving a group or being removed takes the person off upcoming dates without
   rewriting past ones; a disabled account's answers are hidden and not counted, and come back
   where there is room when it is turned on again (DEC-38, DEC-47). Making an event more private
   removes nobody and lists who is affected (R3). Deleting an event or cancelling a date tells the
   people signed up, with the count in the confirmation (DEC-175). A date moved to another day
   takes no answers with it: the people signed up to the cancelled date are told and answer the
   new one themselves (R40). Email goes only where R36 says; every notice goes to the bell.
3. **Guests.** A public event's date offers a stranger a short form: name, email, and that they are
   18 or older, each with a length limit. Two links, both random and stored hashed. The *confirm
   link* is single use and lasts 24 hours; it is one click in the browser that asked and a button
   anywhere else (R26), and only then does the answer count (R35). Until it is used, the name and
   email wait sealed in the link's row, no person or answer exists, and the row is erased when it
   is used or expires. The *manage link* is a standing link to that one date: it can cancel the
   guest's answer (a POST with a CSRF token), is revoked when they cancel, and stops working when
   the date ends (DEC-55). Going only (DEC-56). An address that already has an account gets the
   same page and the same email shape, and the answer joins that account (R15). Guests get only the
   mail R36 allows, by their manage link; no optional mail. Limits per address and per client,
   in memory, answer with the usual message (`docs/HAZARDS.md` sections 3 and 9). Inviting one
   person to one private date by link (P8) waits for a church to ask (R51).
4. **Calendar, Home and feeds**, in two pull requests.

   **4a. The calendar and Home.** `/calendar` with Agenda (the default), Week and Month (DEC-67),
   Going tinted and Maybe and unanswered outlined, unanswered church-wide and public dates within
   30 days (DEC-77), and filters for Just mine and one group. Home's "Coming up for you", and
   signed-out Home's public events (DEC-94). "Add to calendar" (a one-date `.ics`, whose writer
   the feed reuses) and "Share" on every date (DEC-93). Calendar in the header and the phone tab
   bar.

   **4b. Feeds and finding groups.** A personal ICS feed on a standing link: random, stored
   hashed, one purpose, revocable from the profile, read by GET without a session, and an unknown
   or revoked link answers exactly like one never issued. The feed marks Going confirmed and Maybe
   tentative and leaves out Can't go (DEC-71). Finder filters by day and time of day; group lists
   sort by next meeting.
5. **Measure.** The gate below: lines, tables, routes and test time against `AGENTS.md`, compared
   with Fold for the same behaviour, written into this page. A timed install waits for an installer
   who is not Grier, as in slice 0.

**Checks.** Every test reads an injected clock.

- A: a weekly 7:00 pm event stays at 7:00 pm on both sides of each daylight-saving change, in the
  event's zone. Only R33's repeats can be saved, and one that would make more than 2,000 dates is
  refused.
- A: on the spring night a 2:30 am date moves to 3:30 am and the date page says so; on the fall
  night a 1:30 am date uses the first 1:30; the form warns when the chosen time can do either
  (R34).
- A: the policy tables for seeing an event, answering, running it and seeing who is coming pass,
  carried from Fold and changed where R3 and P7 say. An invite-only group's event answers like
  a missing page to someone outside it, on the date page, the calendar and the feed. Only the
  people running an event see who can't go.
- A: every change that frees a seat (Can't go, Cancelled, a removal, leaving a group, a disabled
  account, a raised limit, a cancelled guest) moves the next waiting person up and tells them;
  nothing that doesn't free a seat moves anyone.
- A: deleting an event or cancelling a date tells everyone signed up, at the bell and by the email
  R36 names, in batches; the confirmation's count is exactly the people who will be told. A series
  edit that drops dates asks about that before saving.
- A: the guest form answers the same whether or not the address has an account; a confirm link
  does nothing when merely opened in another browser; nothing about the guest is stored as a
  person until it is used, and its sealed row is gone after use or expiry; the manage link stops
  working when the guest cancels or the date ends; limits stop an address being flooded.
- A: leaving a group removes upcoming answers and keeps past ones; joining again is allowed.
- A: the feed shows only what the person may see, an unknown or revoked link gets the same answer,
  and no feed or guest link is ever written to a log.
- A: the calendar and the feed never carry other people's answers, counts or Can't go (DEC-64,
  DEC-67).
- A: the accessibility, layout and contrast gates pass on every new page at 390 and 1280 px.
- P: a personal feed subscribes on a real iPhone and shows next week's dates.

**Defaults, confirmed by Grier (R37):**

- Changing the church's time zone moves nothing: each event keeps its own zone, and only new events
  start in the new one.
- Finding public events of other groups (DEC-76) is the calendar's Everything view within 30 days,
  and signed-out Home's "Browse public events", as Fold built it. Nothing more until Grier designs
  it.
- Monthly repeats offer the 1st to 4th weekday and the last, so no month is skipped.
- A party is one person until households arrive in slice 5; no plus-ones (DEC-56).
- Places are text. A date offers "Open in maps", a plain link built from the text; the server
  fetches nothing.
- No pictures on events in this slice. Uploads (DEC-49) arrive when a church asks or with a later
  slice.

**Rests on.** R3, R15, R29, R33 to R51, P7, and "Events and dates" and "RSVP" in the decisions.

**Not in this slice.** Reminders and announcements (slice 3). Children, childcare and teens
(slice 5). Rooms (slice 6). Attendance (slice 4).

---

## Slices 3 to 4, in outline

**Slice 3: announcements, reminders, email choices.** A leader writes to their group or to an
event's sign-ups, sees who it will reach before sending, and can schedule it. Reminders go out
before each date. Every optional email can be turned off by kind, from the inbox's own unsubscribe
button or a settings page. Bounces and spam complaints stop mail as the decisions say, and admins
see them. *Rests on:* P13, and the rest of "Notices, announcements and email".

**Slice 4: first-church readiness.** An admin imports the church's people from a spreadsheet, with
a preview before anything is created. A leader takes attendance for a date on their phone in under
a minute. An admin can delete a person, which erases them; a person can download their own data,
and an admin the church's. Admins sign in with a second factor. The audit log can be requested
(R7, and R16 and R17 for a church with one admin). The church's privacy and terms pages exist as
templates it can edit. Then the pilot church goes live, and this slice is done when they are using
it. *Rests on:* R6, R7, P10, P11, P17.

**Slice 5: households, children, teens.** A parent adds their children and signs them up; events
count childcare; a teen has an account with a guardian's and an admin's consent. The finder gains
a childcare filter. Naming a host (`events.AddHost`) must then refuse a teen (DEC-18).

**Slice 6: rooms.** Bookings, conflicts, optional approval, the by-room calendar, suggestions.

---

## Will it fit the budgets?

An estimate to test the ceilings, not a schema.

| Slice | Tables added | Running total |
|---|---|---|
| 0 | people, sessions, links, settings, audit log, jobs | 6 |
| 1 | groups, memberships, approvals, notices | 10 |
| 2 | events, event hosts, dates, answers | 14 |
| 3 | messages, recipients, email choices, stopped addresses | 18 |
| 4 | attendance, audit requests | 20 |
| 5 | households, guardians | 22 |
| 6 | rooms, bookings | 24 |

- **Tables:** 24 of 35 (R13). Room for push, SMS or sign-up sheets when a church asks.
- **Routes:** a form is usually two routes (show it, post it). A rough count by slice comes to
  about 100 to 110 by slice 6, against 200 (R13).
- **Lines and test time:** measured after slice 2, below.

### Measured after slice 2 (2026-11-27)

| Budget | Ceiling | After slice 2 |
|---|---|---|
| Go and templates, not tests | 25,000 lines | 20,096 (plus 48 lines of our own JavaScript and 304 of CSS source) |
| Tables | 35 | 15 real tables. `make budgets` prints 17 because its grep also counts the two "new table, copy, swap" rebuilds of `people` |
| Routes | 200 | 91 |
| Whole test suite | 3 minutes, no containers | 8.3 seconds for `go test -count=1 ./...`; the browser gates take about 2.5 minutes more and run in CI |
| Containers a church runs | 2 | 2 (the app and Caddy) |
| Go modules required directly | few | 2: the SQLite driver and `rsc.io/qr` (the feed's QR code) |

**Against Fold for the same behaviour.** Fold at the pinned commit had 76,231 lines of non-test Go
and 40,532 lines of TypeScript for its separate client, 116,763 in all, and 215 API routes. Leaving
out every Fold file whose name belongs to a feature this project has not built (rooms, messages,
households, teens, push, contact requests, departure, reminders, erasure, import, attendance,
avatars, consent, passwords, audit, bounces, export) still leaves about 48,400 lines of Go and
30,300 of TypeScript, about 78,700. That count is generous to Fold, because the files it keeps
also carry parts of the features it leaves out. This project does the same work in 20,096 lines,
about a quarter, with 10,038 lines of tests against Fold's 186,499.

**Verdict: clearly smaller.** The gate passes. The one budget to watch is lines: slice 2 used
about 9,000 of the remaining 15,000, with slices 3 and 4 still to come, so Grier raised the
ceiling to 35,000 (R53). The timed install by someone who is not Grier still waits, as in
slice 0.

## Where each carried rule lands

So nothing in `docs/DECISIONS.md` section 3 is left without a slice.

| Rule group | Slice |
|---|---|
| The project | every slice |
| People and accounts: login, approval, invitations, admins, disabling, successor | 0 and 1 |
| People and accounts: erasure | 4 |
| People and accounts: under-13s, teens, guardian links | 5 (slice 1 admits adults only) |
| Groups | 1 (leaving and upcoming dates: 2) |
| Events and dates, church-wide hosts | 2 |
| RSVP, guests, calendar, feeds | 2 |
| Households and children | 5 |
| Notices and the bell | 1 |
| Announcements, reminders, email choices, bounces, mailing address | 3 |
| Rooms | 6 |
| Pages and wording | every slice |
| Legal page templates | 4 |

## What we learn

When the pilot church (Cloverdale Church of God, R12) is live, record here what they asked for, what confused them and what they
never used. Slice 7 onward is chosen from that list, and from nowhere else.
