# Plan

What to build, in what order, and how to tell a slice is done. This page adds no rules: every rule
is in `docs/DECISIONS.md` (cited here as R, P or a Fold DEC number). If this page and a decision
disagree, the decision wins and this page is fixed.

Only slices 0 and 1 are written in detail. Each later slice gets its detail in a pull request to
this page when the slice before it is merged, so the plan is written with what was just learned
and never runs far ahead of the code.

## Status

The only place status is kept. Update the row in the pull request that changes it.

| Slice | What | Status |
|---|---|---|
| 0 | Foundation and install | Not started |
| 1 | People, groups, approvals, notices | Not started |
| 2 | Events, dates, RSVP, calendar | Not started |
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

## Slices 2 to 4, in outline

**Slice 2: events, dates, RSVP, calendar.** A leader schedules a one-off or weekly event in the
church's time zone; an admin creates a church-wide event and names its hosts. A member opens a
date and can tell at a glance what it is, when, where, whether they are in, who else is coming and
what is different about this date. They answer Going, Maybe or Can't go, for one date or the
series. Seats and a waitlist work, and anyone moved up is told. A guest RSVPs to a public event by
emailed link and can manage that one date. The calendar has Agenda, Week and Month, and a personal
feed subscribes from a phone. The group finder gains filters for meeting day and time of day.
Leaving a group takes the person off its upcoming dates. Deleting or cancelling tells the people
signed up. Then the measuring gate above. *Rests on:* R15, P7, P8, and "Events and dates" and
"RSVP" in the decisions.

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
a childcare filter.

**Slice 6: rooms.** Bookings, conflicts, optional approval, the by-room calendar, suggestions.

---

## Will it fit the budgets?

An estimate to test the ceilings, not a schema.

| Slice | Tables added | Running total |
|---|---|---|
| 0 | people, sessions, links, settings, audit log, jobs | 6 |
| 1 | groups, memberships, approvals, notices | 10 |
| 2 | events, hosts, dates, registrations | 14 |
| 3 | messages, recipients, email choices, stopped addresses | 18 |
| 4 | attendance, audit requests | 20 |
| 5 | households, guardians | 22 |
| 6 | rooms, bookings | 24 |

- **Tables:** 24 of 35 (R13). Room for push, SMS or sign-up sheets when a church asks.
- **Routes:** a form is usually two routes (show it, post it). A rough count by slice comes to
  about 100 to 110 by slice 6, against 200 (R13).
- **Lines and test time:** unknown until the measuring gate after slice 2.

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
