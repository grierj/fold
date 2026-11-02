# Audit 2026-11 — Features: R10 attendance roll, R14 sign-up sheets

Implementation briefs for a coding agent. These are briefs, not code. Read
`AGENTS.md` first; where this file and `docs/plan/PLAN.md` disagree, PLAN wins
and the disagreement gets raised in the PR, not silently resolved. Two known
disagreements are flagged where they occur (R10 model shape,
`docs/plan/PLAN.md:325`; R14 sheet anchor, `docs/plan/PLAN.md:372`).

Shared rules for both items:

- Single Go binary with the SPA embedded; Postgres only; schema changes are
  new goose files in `migrations/` at the next free number (last seen
  `00062_reminder_push.sql`), never edits to old files. The GORM model change
  and the `models.All()` update (`models/models.go:1030`) land in the same PR.
- R9 dependency rule (audit build order, `docs/AUDIT-2026-11-01.md:263`): land
  these tables after the R9 baseline squash, or fold the new tables into that
  baseline if R9 is in flight. If neither has happened when work starts, use
  the next free migration number and let R9 squash them later. Do not start a
  second squash inside these items.
- Permissions are pure functions in `internal/policy` with table tests beside
  them (`internal/policy/policy_test.go`). Handlers load facts and call
  policy; no inline role checks, never `Group.LeaderID`. Per-date visibility
  always goes through `policy.EffectiveVisibility`
  (`internal/policy/policy.go:753`); a date candidate never authorizes the
  series.
- Sensitive actions write `audit.Write` rows (`internal/audit/audit.go:380`,
  action constants from `internal/audit/audit.go:15`) in the same transaction
  as the change. Bell notices are `notify.Send` rows in that transaction too;
  a new kind registers in `internal/notify/kinds.go` (registry shape at
  `internal/notify/kinds.go:109`) and the caller decides what the recipient
  may see. Resolve any `waiting` rows with `notify.Resolve`
  (`internal/notify/notify.go:184`) where the subject is decided.
- JSON is camelCase under `/api/v1`. A new error a person can act on gets an
  entry in `errorCodes` (`api_errors.go:20`) plus a line in `ERROR_COPY`
  (`web/src/lib/errors.ts:13`). New response types get a pair in
  `tsContracts` (`guards_test.go:66`) so `web/src/types.ts` stays matched to
  the Go structs. Names render with `User.PublicName()`, never the email.
- UI uses `web/src/components/ui` controls (`Button`, `Form`, `Field` with
  `Input`/`Checkbox`/`Select`, `EmptyState`, `Chip`) and role tokens from
  `web/src/index.css`. One primary action per view; status is never color
  alone. Prove UI changes with `.cursor/skills/verify-ui` and the 390px
  screenshots.
- Tests sit next to the code they test; no milestone-named test files.
  Verification for every slice: `go build ./...`, `go vet ./...`,
  `make lint-go`, `(cd web && npx tsc --noEmit)`, `(cd web && npx knip)`,
  `make test`, `cd web && npm test`, and `make e2e` where a user flow
  changes.
- Docs say "recommended", never the other word (the docs-wording guard test
  fails the build). No Python anywhere.
- Rejected audit items stay out: nothing here freezes web push or strikes
  SMS (R7), and nothing adds an open self-signup mode (R8). R17's cut list
  binds both items: no child check-in kiosks, no QR check-in surface, no
  payments (`docs/AUDIT-2026-11-01.md:228`).
- Both items add occurrence-child rows, so both must extend
  `deleteEventRows` (`api_events.go:1158`) to delete their rows before the
  occurrences go, in the same transaction. Both must also be added to the
  R4 erasure inventory (what a deleted person's rows become); the defaults
  below keep counts and drop names with the account row.

---

## R10 — Leader attendance roll

- Priority: P1 (audit `docs/AUDIT-2026-11-01.md:164`; build order step 5,
  `docs/AUDIT-2026-11-01.md:266`)
- Effort: M (audit estimate)
- Status: Approved 2026-11-01 (audit). Re-sequences PLAN M6.3
  (`docs/plan/PLAN.md:2079`); PLAN tags simple attendance Core
  (`docs/plan/PLAN.md:1461`).

### Goal

After a meeting, a group leader (or a church-wide event's host, or an admin)
opens that date's roll on their phone, taps "Mark everyone present",
unchecks the people who did not come, adds a guest headcount, and saves
once. The event page then shows a tiny recent-dates summary — registered
going next to actually present — so a pastor asking "how many came?" gets a
real number instead of an RSVP count. Registration stays a plan; attendance
is what happened, and the two are always shown side by side, never merged.

### Current state (evidence)

- There is no attendance model. PLAN 5.3 sketches it as a later row:
  `Attendance(occurrence_id, user_id?, household_person_id?, present,
  checked_in_at, checked_in_by, method roll|qr|kiosk)`
  (`docs/plan/PLAN.md:325`), and M6.3 is one unspecced line
  (`docs/plan/PLAN.md:2079`). Nothing in `models/models.go` or `web/src`
  records presence; the component named `Attendance`
  (`web/src/components/Attendance.tsx:12`) renders RSVP counts and names
  only — do not overload that name.
- The roster a leader already works from is per occurrence.
  `occurrencesForEventsAfterErr` (`api_events.go:85`) loads scheduled dates,
  filters them through `visibleSet.canSeeDate` (`api_events.go:115`,
  defined `api_occurrence_visibility.go:18`), counts registrations by
  status (`api_events.go:141`), and attaches attendee names only when
  `policy.CanSeeAttendeeNames` allows (`api_events.go:200`,
  `internal/policy/policy.go:290`). Names are built by `attendeeNames`
  (`registration_subjects.go:438`), which handles both account and
  household-person subjects.
- "Who said can't go" is already manager-only data: declined counts and
  names go only to `policy.CanManageEvent` viewers (`api_events.go:187`,
  DEC-64). Attendance is at least as sensitive; the brief follows that
  precedent.
- The event page is upcoming-only. `apiGetEvent` loads
  `upcomingWindow(now)` with `startAfter = now`
  (`api_event_access.go:263`), and the listing keeps only
  `status = scheduled` dates (`api_events.go:102`). A past date's roll
  therefore needs its own endpoints and its own past-date list; it cannot
  ride on `occurrenceJSON`.
- The manager surface on a date card already exists: the date's More menu
  offers "Manage sign-ups" (`web/src/pages/EventDetail.tsx:355`), which
  lazy-loads `ManageRoster` (`web/src/pages/EventDetail.tsx:35`, rendered
  at `web/src/pages/EventDetail.tsx:415`). That panel lists going / maybe /
  waitlisted people with per-person actions
  (`web/src/components/ManageRoster.tsx:86`) and is the placement and
  interaction model for the roll.
- The group roster endpoint is `GET /groups/:id/members`
  (`api.go:705`), gated by `policy.CanSeeRoster` (`api_groups.go:595`,
  `internal/policy/policy.go:76`); the group page's Members tab renders it
  (`web/src/pages/GroupDetail.tsx:996`). Group members who never RSVPed
  still show up at meetings, so the roll roster needs this list too.
- Registration subjects are exactly one of account or household person,
  with partial unique indexes for each (`models/models.go:437`); guest
  registrants are ordinary rows against a guest `User`
  (`models/models.go:417`). A date is cancelled with
  `status = cancelled` (`models/models.go:90`, `models/models.go:357`).
  Deleted accounts keep anonymous registrations "for attendance counts"
  (`docs/plan/PLAN.md:1054`), and disabled accounts' registrations stay
  hidden and uncounted in attendee lists (`docs/plan/PLAN.md:324`).
- Manager loading for event writes goes through `loadManagedEvent`
  (`api_events.go:856`); the per-date page loader `loadOccurrenceDate`
  (`api_occurrence_access.go:337`) only loads scheduled dates
  (`api_occurrence_access.go:350`), so it cannot serve a past or cancelled
  date's roll.

### Scope

In:

- Two tables: a per-date roll header and per-person marks (shape below).
- Leader/host/admin roll for one date: bulk "mark all present, then
  uncheck", one save, guest headcount for unnamed visitors.
- A per-event summary for the same managers: the last 8 past scheduled
  dates with going vs present side by side.
- Audit row per save. No bell notices: nobody is told how they were marked.

Out:

- Check-in kiosks, QR / self check-in, name tags (R17 cut;
  `docs/plan/PLAN.md:1474`, M6.4 stays later). The `method` column below is
  a stored constant `roll` in v1, not a surface.
- Dashboards, charts, cross-group or church-wide rollups, exports,
  per-person attendance history pages, and any member-facing view of marks
  (decision below).
- Changing RSVP from the roll, auto-marking from registrations, and
  reminders to take attendance. Jobs are not touched.

### Decisions (defaults; open questions at the end)

- **Two tables, not one.** `attendance_rolls` (one row per occurrence:
  `guest_headcount`) exists so "the roll was taken and nobody came" differs
  from "nobody took the roll". The summary reads that difference;
  `present = false` rows in `attendance_marks` record explicit absences, so
  an unticked save is a fact, not a missing row.
- **Marks cover household people too.** PLAN's sketch has
  `household_person_id?` (`docs/plan/PLAN.md:325`) and `Registration`
  already proves the exactly-one-of-two pattern with partial unique
  indexes (`models/models.go:437`). A roll that cannot tick the children a
  holder signed up would undercount every family group. Cost is one
  nullable column and the same roster rows `attendeeNames` already builds.
- **Roll roster = registrants ∪ group members.** For a group event the
  checklist lists every active member (the `CanSeeRoster` list) plus anyone
  registered for the date who is not a member, each row showing their RSVP
  status next to the Present box (going / maybe / waitlisted / can't go /
  no answer). For a church-wide event it lists registrants only; a walk-in
  with no account is counted in `guest_headcount`, not named. Marks are
  keyed to the person, never to a registration id, so a later RSVP change
  never rewrites who was in the room.
- **Managers only, including no own-history view in v1.** Marking and
  reading both require the new `policy.CanMarkAttendance` /
  `policy.CanSeeAttendance`, which wrap `CanManageEvent` semantics (admin,
  group leader, church-wide host; `internal/policy/policy.go:253`). Members
  do not see marks — not other people's, and not their own history.
  Precedent: declined names are already manager-only (`api_events.go:187`),
  teen and child roster identity is manager-only
  (`internal/policy/policy.go:745`), and attendance at a religious meeting
  is observance data a small group should not turn into a visible score.
  Marks never appear in `occurrenceJSON` / `attendeeJSON`; they live only
  behind the manager endpoints below. A person's own record is reachable by
  an admin through those endpoints, which is the v1 answer for a
  subject-access ask; a self-service history is an open question.
- **Guest headcount is a plain count of unnamed people present** (visitors,
  walk-ins, children not signed up by name), 0–1000. It is not the same as
  a registration's `guest_count` (`models/models.go:452`), which is a plan;
  the summary shows headcount as its own line, never folded into named
  present counts.
- **Marking window:** opens 2 hours before the date's `starts_at` (the same
  2-hour constant the waitlist cutoff uses, DEC-84) and never closes in v1,
  so a leader can fix last week's roll. A cancelled date (`cancel_source`
  either value) refuses saves and is excluded from every summary
  denominator; marks already saved on a date cancelled later stay stored
  but stop counting anywhere.
- **One bulk save, last writer wins.** The PUT replaces the whole roll in
  one transaction under the occurrence row lock (`lockOccurrence`,
  `api_registrations.go:297`). Per-checkbox autosave is rejected: it would
  write an audit row per tap and leave half-taken rolls ambiguous.
- **Disabled and deleted accounts:** their marks keep counting in summary
  totals (a historical fact, matching `docs/plan/PLAN.md:1054`), but their
  names drop off the checklist the same way attendee lists already hide
  them (`docs/plan/PLAN.md:324`). A re-enabled person's old marks reappear
  with their name; nothing is backfilled or deleted on disable/delete.

### Slices (DB → API → UI, one PR each)

**Slice R10a — DB.** New goose file at the next free number (R9 rule above)
creating:

- `attendance_rolls`: `id`, `occurrence_id` (unique, FK to
  `event_occurrences`), `guest_headcount int NOT NULL DEFAULT 0` with
  `CHECK (guest_headcount BETWEEN 0 AND 1000)`, `marked_by` (user id, no
  GORM association, the `Registration.RemovedBy` pattern at
  `models/models.go:463`), `marked_at`, `created_at`, `updated_at`.
- `attendance_marks`: `id`, `occurrence_id` (FK, indexed), `user_id`
  nullable, `household_person_id` nullable, `CHECK` exactly one of the two
  is set, `present bool NOT NULL`, `method text NOT NULL DEFAULT 'roll'`,
  `marked_by`, `marked_at`. Partial unique indexes on
  `(occurrence_id, user_id)` and `(occurrence_id, household_person_id)`,
  mirroring `models/models.go:437`.
- Models `AttendanceRoll`, `AttendanceMark` in `models/models.go` next to
  `Registration`, added to `All()` (`models/models.go:1032`). Constants for
  the method value beside the occurrence status constants
  (`models/models.go:89`). Down migration drops both tables (pre-live, no
  data to preserve). Drift check in `migrations/migrations_test.go` must
  pass.

**Slice R10b — API and internals.** New file `api_attendance.go` (tests in
`api_attendance_test.go`), routes registered beside the other date routes
(`api.go:754`):

- `GET /api/v1/events/:id/dates/:date/attendance` — the checklist for one
  date, past dates included. Loads the event with `loadManagedEvent`
  semantics (`api_events.go:856`), the occurrence by `(event_id,
  original_date)` in any status, facts with `factsFor`
  (`api_event_access.go:98`), then `policy.CanSeeAttendance`. Not a manager
  answers the same 403/404 `loadManagedEvent` gives today. Roster rows are
  built like `attendeeNames` (`registration_subjects.go:438`) unioned with
  active group members, minus disabled/deleted names, each row carrying
  the person's current RSVP status and their mark (`present: true/false`,
  absent = unmarked).
- `PUT /api/v1/events/:id/dates/:date/attendance` — replace the roll.
  `policy.CanMarkAttendance`; same transaction shape as registrations
  (`api_registrations.go:26`): lock the occurrence, recheck status and
  window inside the lock, upsert the roll header and marks, delete marks
  for roster people the body no longer lists, write one
  `attendance.marked` audit row (new constant in
  `internal/audit/audit.go`, meta: occurrence id, date, present count,
  absent count, guest headcount). Returns the saved roll in the GET shape.
- `GET /api/v1/events/:id/attendance-summary` — managers only. The last 8
  occurrences with `status = scheduled` and `starts_at < now`, newest
  first, each with going count (listed registrations, the
  `listedRegistrations()` join used at `api_events.go:142`), named present
  count, guest headcount, `rollTaken`, and `presentPctOfGoing` (present ÷
  going, null when no roll was taken or going is 0 — never 0%). Cancelled
  dates never appear.
- `deleteEventRows` (`api_events.go:1158`) deletes marks and rolls for the
  event's occurrences before deleting the occurrences.
- New errors in `api_errors.go` + `ERROR_COPY`: `attendance_date_cancelled`
  (409, "this date was cancelled, so its roll can't be changed"),
  `attendance_too_early` (409, before the 2-hour window),
  `attendance_person_unknown` (400, a mark names someone not on this
  date's roster), `attendance_guest_count` (400, outside 0–1000). Reuse
  generic codes for everything else.
- New JSON types in `api_attendance.go` with pairs in `tsContracts`
  (`guards_test.go:66`) and mirrors in `web/src/types.ts`. No new fields on
  `occurrenceJSON` (`api.go:165`) — attendance must not leak into
  member-facing payloads.

**Slice R10c — UI.** All on the event page; the group page does not change
in v1 (attendance belongs to an event's dates, and group leaders already
reach the event page from the group's dates).

- New client entries beside the other `Events` calls in `web/src/api.ts`
  (`web/src/api.ts:360`): `getAttendance`, `saveAttendance`,
  `attendanceSummary`, with types in `web/src/types.ts` next to `Attendee`
  (`web/src/types.ts:194`).
- New component `web/src/components/AttendanceRoll.tsx` (+ test beside it),
  lazy-loaded like `ManageRoster` (`web/src/pages/EventDetail.tsx:35`).
  It renders the checklist: one `Checkbox` row per person
  (`web/src/components/ui/Field.tsx:157`), name plus RSVP status in words
  ("Going", "Maybe", "No answer", "Can't go" — never color alone), a
  "Mark everyone present" secondary button and "Clear all", a guest
  headcount `Field`/`Input` with its range stated next to the field, and
  one primary "Save attendance". A `Form` from `components/ui` carries it;
  a failed save keeps the leader's ticks and shows the error under the
  form. Checkboxes start from the saved marks; on a fresh roll they start
  unticked and "Mark everyone present" does the bulk work.
- `web/src/pages/EventDetail.tsx`: for managers only, an "Attendance"
  section after the dates list showing the summary rows (date, "12 going ·
  9 present · 75%", "+3 guests", or "Roll not taken"), each row opening
  that date's `AttendanceRoll` panel in place. Today's date also gets a
  "Take attendance" item in its date-card More menu
  (`web/src/pages/EventDetail.tsx:355`) once its 2-hour window opens,
  opening the same panel. `EmptyState` when the event has no past dates:
  what will appear, and the next step, in neutral tone.
- Signed-out, member and exact-date-invitee views get nothing new; the
  section renders only when the page response's `canManage` is true and
  the summary call succeeds.

### Data / API shapes

`GET .../dates/:date/attendance` and the PUT response:

```json
{
  "attendance": {
    "eventId": 12, "occurrenceId": 340, "date": "2026-11-04",
    "startsAt": "2026-11-04T19:00:00Z", "cancelled": false,
    "rollTaken": true, "guestHeadcount": 3,
    "markedByName": "Pat Host", "markedAt": "2026-11-04T20:05:00Z",
    "people": [
      { "userId": 51, "name": "Sam Member", "accountType": "adult",
        "rsvpStatus": "going", "present": true },
      { "householdPersonId": 9, "name": "Kid Member",
        "rsvpStatus": "going", "present": false },
      { "userId": 77, "name": "Jo Member", "accountType": "adult",
        "rsvpStatus": null, "present": null }
    ]
  }
}
```

`present: null` means unmarked; `rsvpStatus: null` means no answer.
`markedByName` uses `PublicName()` and is omitted before the first save.

PUT body (the whole roll; marks for roster people the body omits are
deleted, so the client always sends its full checklist):

```json
{
  "guestHeadcount": 3,
  "marks": [
    { "userId": 51, "present": true },
    { "householdPersonId": 9, "present": false }
  ]
}
```

`GET .../attendance-summary`:

```json
{
  "summary": {
    "eventId": 12,
    "dates": [
      { "date": "2026-11-04", "occurrenceId": 340, "going": 12,
        "present": 9, "guestHeadcount": 3, "rollTaken": true,
        "presentPctOfGoing": 75 },
      { "date": "2026-10-28", "occurrenceId": 331, "going": 10,
        "present": null, "guestHeadcount": null, "rollTaken": false,
        "presentPctOfGoing": null }
    ]
  }
}
```

### Tests + e2e

- Policy table tests in `internal/policy/policy_test.go` next to
  `TestCanManageEvent` (`internal/policy/policy_test.go:444`): admin,
  group leader, church-wide host, plain member, member of another group,
  signed-out, teen leader never exists but a teen member is refused,
  pending/archived group leader edge following `CanManageEvent`.
- Handler tests in `api_attendance_test.go` with the `testhelpers_test.go`
  cookie-jar + CSRF client: manager saves and re-reads a roll; member and
  signed-out get nothing (no marks in any member-facing payload — assert
  on the event GET body); cancelled date refuses with
  `attendance_date_cancelled` and disappears from the summary; future date
  before the window refuses; unknown person in marks refuses and writes
  nothing; household-person mark round-trips; walk-in member with no RSVP
  can be marked; two leaders saving at once leave one whole roll (either
  writer's) with one audit row each; summary math (percent rounding,
  no-roll date reads null not 0%, guest headcount never inside present);
  event deletion removes rolls and marks.
- Vitest: `AttendanceRoll.test.tsx` (bulk mark-all then uncheck, save
  payload shape, error keeps ticks, RSVP words not color) and the summary
  block in `EventDetail.test.tsx` (manager sees it, member does not,
  "Roll not taken" row).
- Playwright: new `web/e2e/attendance.spec.ts` — leader signs in, opens a
  past date's roll at 390px, marks all, unchecks one, sets guests, saves,
  reloads, sees the same ticks and the summary line; a member's event page
  shows no attendance section. Add the manager event page state to
  `.cursor/skills/verify-ui` evidence (390 / 768 / 1280 screenshots, axe
  clean, no sideways scroll, targets ≥ 44px where the ratchet requires).

### Acceptance checklist

- [ ] Migration at the next free number (or folded into the R9 baseline);
  models + `All()` updated; drift check passes.
- [ ] A leader can take, save, leave and re-open a past date's roll on a
  phone; ticks and guest headcount survive reload.
- [ ] Going and present counts always appear side by side and are never
  the same field; a date with no roll never reads as 0 present.
- [ ] Cancelled dates refuse saves and never count in the summary.
- [ ] No member, guest or signed-out response contains any mark, present
  count or headcount (checked at the API body level, not just the UI).
- [ ] Every save writes exactly one `attendance.marked` audit row in the
  save transaction; no bell/email/push rows are written.
- [ ] Deleting the event deletes its rolls and marks; disabling an account
  hides its name but keeps its marks counting.
- [ ] New error codes have `ERROR_COPY` lines; new types have
  `tsContracts` pairs; `make test`, web tests, e2e and the verify-ui
  390px pass all green.

### Risks / privacy

- Attendance is religious-observance data and this is the first place Fold
  stores it. The controls are structural: separate manager-only endpoints,
  no fields on shared occurrence payloads, no notices, no exports. The
  retention question (how long marks live) is unanswered — see open
  questions; v1 keeps them while the event exists, matching registrations.
- Roster breadth: listing every group member on every date is what leaders
  expect from a paper roll, and it stays inside `CanSeeRoster`'s existing
  audience. It must not widen: a church-wide host sees registrants only,
  never a people directory.
- A leader marking from memory days later can be wrong; the audit row
  says who marked and when, and any manager can correct it. That is the
  whole safeguard in v1.

### Dependencies

- R9 window (shared rule above). R4's erasure inventory must name
  `attendance_rolls` / `attendance_marks`; land order between R4 and R10
  does not matter as long as the later PR updates the inventory.
- No jobs, no notify kinds, no settings. Nothing from R7/R8.

### Open questions (with defaults to implement by)

1. Should a person see their own attendance history? **Default: no view in
   v1** (decision above). Revisit with the R3 own-data export, where a
   person's marks may belong in their export file.
2. How long are marks kept? **Default: while the event exists**, same as
   registrations; a retention sweep is a later policy decision for the
   owner, not built here.
3. Close the marking window at some point (e.g. lock after 30 days)?
   **Default: no lock in v1**; corrections stay possible and audited.
4. Count remote attendees as present? **Default: yes** — presence is
   presence; the RSVP column still shows the mode today only via status
   text, and splitting present counts by mode is a summary v2 question.
5. Keep the `method` column now, or add it with M6.4 QR later?
   **Default: keep it**, written as `roll` only; PLAN's sketch names it
   (`docs/plan/PLAN.md:325`) and adding it later is a second migration for
   one default.

---

## R14 — Sign-up sheets (volunteers, potluck, supplies)

- Priority: P2, after R10 (audit `docs/AUDIT-2026-11-01.md:203`)
- Effort: M (audit estimate)
- Status: Approved 2026-11-01 (audit). Re-sequences PLAN M6.2
  (`docs/plan/PLAN.md:2078`); PLAN tags sheets Core, "high value, small
  model" (`docs/plan/PLAN.md:1462`).

### Goal

For one date of an event, a leader or host makes a short sheet — "Potluck:
3 mains, 4 sides, 2 desserts" or "Setup crew: 4 people" — and members who
can see that date claim slots from the event page instead of a reply-all
email thread. Everyone who may see the date sees what is still needed; a
claim can never overshoot the quantity, even when two people tap at once.

### Current state (evidence)

- Nothing exists. PLAN's later-model line is the only spec:
  `SignupSheet(event_id|group_id, title, kind volunteer|potluck|supplies)`,
  `SignupSlot(sheet_id, label, quantity, starts_at?, requires_check)`,
  `SlotClaim(slot_id, user_id, note)` (`docs/plan/PLAN.md:372`), and M6.2
  is one line (`docs/plan/PLAN.md:2078`).
- Dates, not series, are where per-meeting facts already hang.
  Registrations hang off the occurrence row (`models/models.go:346`
  comment, `models/models.go:437`); reminders address one occurrence's
  going list (`reminders.go:189`, `AudienceOccurrence` normalized at
  `messages_audience.go:50` and resolved per occurrence at
  `messages_audience.go:215`); the event page renders one card per date
  (`web/src/pages/EventDetail.tsx:233`).
- A date's own visibility can differ from its series:
  `VisibilityOverride` on the occurrence (`models/models.go:358`) resolved
  by `policy.EffectiveVisibility` (`internal/policy/policy.go:755`), and
  the ordinary date filter applies it before `CanSeeEvent`
  (`api_occurrence_visibility.go:18`). `canRegisterDate` builds the
  date-effective event the same way (`api_occurrence_visibility.go:51`).
- The claim race has a proven local answer. Registration writes run in one
  transaction that locks the occurrence row first, recounts inside the
  lock, then writes audit and notification rows
  (`api_registrations.go:26`; `lockOccurrence` at
  `api_registrations.go:297` uses `clause.Locking{Strength: "UPDATE"}`).
  Promotion then claims freed seats inside that same lock and tells the
  person via `notify.Send` in the transaction
  (`api_registrations.go:543`, `api_registrations.go:398`). The race is
  covered by `TestConcurrentGoingRespectsCapacity`: 50 clients race for 10
  seats and exactly 10 land going (`api_registrations_test.go:108`).
- Who may see a date's people by name is already decided:
  `policy.CanSeeAttendeeNames` (`internal/policy/policy.go:290`) gives
  names to managers and, outside the opened-invite-only case, to viewers
  who can see the event; everyone else gets counts (`api_events.go:200`,
  `api_events.go:306`). The date page applies the same rule through
  `loadOccurrenceDate` (`api_occurrence_access.go:382`), and exact-date
  invitees get attendees only when that rule allows
  (`api_occurrence_access.go:276`).
- Guest sign-up is a heavier machine than it looks. A signed-out submit
  stores only a `guest_signups` row (`models/models.go:475`); the guest
  `User` and `Registration` exist only after the emailed link is redeemed,
  and the manage token's whole surface is read-and-cancel for that one
  registration (`api.go:786`). A guest account cannot sign in
  (`models/models.go:223`, `CanSignIn` needs an active account with a
  password). `policy.CanRegisterGuest` is deliberately separate from
  member registration (`internal/policy/policy.go:321`).
- Reminder emails are built at sweep time from fixed text with merge
  fields filled per reader (`reminders.go:187`, `reminders.go:225`), and
  readers are re-filtered at send by who may still see the date
  (`reminders.go:259`). There is no send-time slot-state rendering today.
- Manager-only per-date UI already has a home: the date card's More menu
  (`web/src/pages/EventDetail.tsx:355`) opening lazy panels like
  `ManageRoster` (`web/src/components/ManageRoster.tsx:17`), and the
  member-facing per-date block sits under the RSVP counts
  (`web/src/pages/EventDetail.tsx:381` renders the RSVP `Attendance`
  component).

### Scope

In:

- Sheets attached to one occurrence (decision below), each with a title, a
  kind, and any number of slots (label + quantity needed).
- Members claim and release slots (quantity + short "what I'm bringing"
  note), quantity-safe under concurrency, one claim per person per slot.
- Leader/host/admin sheet and slot management, including removing a claim.
- Visibility exactly equal to the parent date's; claimant names exactly as
  visible as attendee names on that date.
- One bell notice kind, only for "a leader ended your claim" (slot deleted
  or claim removed). Audit rows for sheet lifecycle and claim changes.

Out:

- Payments of any kind, paid slots, donations for supplies (PLAN keeps
  money out, `docs/plan/PLAN.md:1489`).
- Recurring sheets, series templates, "copy last week's sheet", and
  standalone group sheets with no date. A weekly group makes one sheet per
  date in v1; that is a stated limitation, not an oversight (decision
  below).
- Background-check flags on slots (`requires_check` waits for M6.8,
  `docs/plan/PLAN.md:2084`; PLAN itself tags it Later,
  `docs/plan/PLAN.md:1473`), slot start times / shifts, waitlists for full
  slots, household-person claims, guest (signed-out) claims.
- Reminder emails mentioning unfilled slots, and a messages audience of
  claimants. Both are named follow-ups below, not v1 (decision below).

### Decisions (defaults; open questions at the end)

- **Sheets attach to an occurrence, not to the event or the group.**
  This departs from PLAN's `SignupSheet(event_id|group_id, ...)` sketch
  (`docs/plan/PLAN.md:372`); raise it in the PR as a PLAN 5.3 amendment,
  the file header's rule. Evidence for the occurrence: a potluck or a
  setup crew is needed on one date, in quantities that change week to
  week; registrations, reminders and message audiences already key on the
  occurrence (`models/models.go:437`, `messages_audience.go:215`); only
  the occurrence carries the per-date visibility override the sheet must
  inherit (`models/models.go:358`); and the event page already presents
  per-date cards where the sheet belongs
  (`web/src/pages/EventDetail.tsx:233`). A series-level sheet would still
  need per-date claims — the same occurrence table with an extra hop — and
  a standalone group sheet has no date to remind about and no
  date-effective visibility to inherit. The cost is real and stated: no
  recurring templates in v1 (scope out above).
- **A date may hold several sheets** ("Potluck" plus "Setup crew").
  `kind` keeps PLAN's values `volunteer | potluck | supplies` plus
  `other`; in v1 it only labels the sheet in the UI (a chip with the kind
  in words), it drives no logic.
- **Visibility is the parent date's, computed fresh on every request.**
  Handlers load the occurrence, build the date-effective event the way
  `dateEvent` / `canRegisterDate` do (`api_occurrence_visibility.go:35`),
  and ask new pure policy functions: `CanSeeSignupSheet` (anyone who may
  see the date ordinarily), `CanClaimSignupSlot` (signed in, may see the
  date, date scheduled and not ended), `CanManageSignupSheet`
  (`CanManageEvent` semantics). Nothing is copied onto the sheet row, so
  making a date more private later tightens its sheets at once — and a
  claimant who loses date access stops seeing their claim (claim release
  on visibility change is an open question with a default below).
  Claimant names follow `CanSeeAttendeeNames` on the date-effective view
  (`internal/policy/policy.go:290`); everyone else who may see the sheet
  sees "2 of 4 claimed" and their own claim, never other names. Signed-out
  viewers of a public date see counts only.
- **Members first: signed-in claims only; no guest claims in v1.** The
  guest flow is not cheap to reuse: a signed-out submit would need a
  `guest_signups`-style intent row per claim, claim creation inside the
  redemption transaction, and claim release on the manage-token surface,
  while AGENTS.md forbids widening member rules to a nil person or loading
  a guest row as an actor. Guest accounts (already redeemed) cannot sign
  in (`models/models.go:223`), so they cannot claim either; on a public
  date the sheet shows them a "Log in to claim a slot" line. This is the
  main v1 limitation to say out loud in the PR.
- **Exact-date invitees may claim when they may register for the date.**
  The authority facts already exist (`canRegisterDate`,
  `api_occurrence_visibility.go:51`; scoped reads,
  `api_occurrence_access.go:51`), so `CanClaimSignupSlot` takes the same
  ordinary-or-exact-date input shape as `CanManageGuestRegistration`
  (`internal/policy/policy.go:762`). Their name on the sheet follows the
  same attendee-names rule as everyone else's. Note the UI consequence in
  Risks: an invitee who cannot see the series reaches the date page, and
  sheet UI there is a later slice, so in v1 they can claim only if they
  can also open the event page.
- **The claim count can never overshoot.** A claim write locks the slot
  row with `clause.Locking{Strength: "UPDATE"}` (the
  `lockOccurrence` exemplar, `api_registrations.go:297`), sums the slot's
  claim quantities for listed accounts inside the lock (the
  `listedUsers` join shape, `api.go:483`, so a disabled account's claims
  stop counting on their own), and refuses the write with 409 `slot_full`
  when the new total would exceed `quantity_needed`. No waitlist, no
  partial auto-shrink: the person re-reads the sheet and picks again.
- **One claim row per person per slot**, unique `(slot_id, user_id)`, with
  `quantity` 1–999 and an optional `note` ("two dozen rolls",
  ≤ 200 characters). Claiming again updates the row; deleting it releases.
  The note is shown wherever the claimant's name is shown, and nowhere
  else. Leaders must not be told contact details through notes — the note
  field is plain text like other form text, and the UI hint says what it
  is for.
- **Slot times and descriptions stay in the label.** PLAN's slot
  `starts_at?` is not built; "Setup crew — arrive 5:30" is a label. A
  separate optional slot note is also out; the sheet title plus labels
  carry v1.
- **Leader edits are constrained, never silent.** Lowering
  `quantity_needed` below the claimed total refuses with 409
  `slot_quantity_below_claims`. Deleting a slot or a whole sheet deletes
  its claims and tells each claimant with the new bell kind
  `signup.claim_ended` (category events, no push in v1: a `Kind` with
  `AllowPush` false needs no `PushRender`, `internal/notify/kinds.go:134`);
  removing one person's claim tells that person the same way. Ordinary
  member claim/release writes no notices — leaders read the sheet — but
  does write an audit row, matching registration changes
  (`auditRegistration`, `api_registrations.go:823`). All of it in the same
  transaction as the change, delivery after commit through the existing
  notification email path.
- **Cancelled or ended dates freeze their sheets.** Releases stay allowed;
  new claims, quantity changes and slot edits refuse with 409
  `signup_date_over`. Cancelling a date
  does not send a new kind of notice in v1: claimants who also registered
  already hear through the existing cancellation path, and the claim UI
  nudges ("Tap Going too, so you hear if this date changes") when the
  claimant has no going registration. Claiming never auto-registers
  anyone.
- **Reminders do not mention unfilled slots in v1.** Reminder bodies are
  fixed text with per-reader merge fields (`reminders.go:225`); adding
  live slot state means send-time rendering plus another per-occurrence
  read in a path R11 already flags for query shape (`reminders.go:259`).
  Named follow-up: a "still needed" line in the going reminder and a
  claimant audience for messages (`messages_audience.go` gains no new type
  in v1). Leaders can email going registrants about a sheet today through
  the existing composer link (`web/src/pages/EventDetail.tsx:362`).

### Slices (DB → API → UI, one PR each)

**Slice R14a — DB.** New goose file at the next free number (R9 rule
above), three tables:

- `signup_sheets`: `id`, `occurrence_id` (FK to `event_occurrences`,
  indexed), `title` (≤ 200 by check or handler, match repo practice),
  `kind text NOT NULL DEFAULT 'other'` with `CHECK (kind IN ('volunteer',
  'potluck', 'supplies', 'other'))`, `created_by`, `created_at`,
  `updated_at`.
- `signup_slots`: `id`, `sheet_id` (FK, indexed), `label` (≤ 200),
  `quantity_needed int NOT NULL` with `CHECK (quantity_needed BETWEEN 1
  AND 999)`, `position int NOT NULL DEFAULT 0` for leader ordering,
  `created_at`, `updated_at`.
- `signup_claims`: `id`, `slot_id` (FK, indexed), `user_id` (FK),
  unique `(slot_id, user_id)`, `quantity int NOT NULL DEFAULT 1` with
  `CHECK (quantity BETWEEN 1 AND 999)`, `note text NOT NULL DEFAULT ''`,
  `created_at`, `updated_at`.
- Models `SignupSheet`, `SignupSlot`, `SignupClaim` in
  `models/models.go` (a Later-models spot near `Registration`), all three
  added to `All()` (`models/models.go:1032`), kind constants beside them.
  No soft delete: deleting a slot or sheet is a real delete plus notice,
  above. Down migration drops the three tables. No FK cascade from users:
  like registrations, claim rows outlive the account row and stop counting
  through the listed-accounts join instead.

**Slice R14b — API and internals.** New file `api_signups.go` (tests in
`api_signups_test.go`), routes beside the date and registration routes
(`api.go:754`, `api.go:769`). Every handler reloads sheet → occurrence →
event → facts (`factsForIn`, `api_event_access.go:102`) and
`dateEvent` (`api_occurrence_visibility.go:35`) itself; a client-supplied
event or occurrence id is never trusted for a slot operation.

- `GET /api/v1/events/:id/dates/:date/signup-sheets` — `optionalUser` like
  the date read: 404 when the viewer may not see the date; names and notes
  only under `CanSeeAttendeeNames`; own claim always marked as own.
- `POST /api/v1/events/:id/dates/:date/signup-sheets` — manager creates a
  sheet with its slots in one body (`title`, `kind`, `slots[]`).
- `PUT /api/v1/signup-sheets/:sheetId` (title, kind),
  `DELETE /api/v1/signup-sheets/:sheetId` — manager; delete ends claims
  with notices.
- `POST /api/v1/signup-sheets/:sheetId/slots`,
  `PUT /api/v1/signup-slots/:slotId` (label, quantity, position),
  `DELETE /api/v1/signup-slots/:slotId` — manager; quantity shrink guarded
  under the slot lock.
- `PUT /api/v1/signup-slots/:slotId/claim` `{quantity, note}` — signed-in
  claim or update, slot-locked and recounted in the lock (decision above).
- `DELETE /api/v1/signup-slots/:slotId/claim` — release own claim.
- `DELETE /api/v1/signup-slots/:slotId/claims/:userId` — manager removes
  someone's claim, with notice.
- New policy functions `CanSeeSignupSheet`, `CanClaimSignupSlot`,
  `CanManageSignupSheet` in `internal/policy` (a `signups.go` beside
  `messages.go`, tests beside it). New audit constants `signup.sheet_created`,
  `signup.sheet_updated`, `signup.sheet_deleted`, `signup.slot_created`,
  `signup.slot_updated`, `signup.slot_deleted`, `signup.claim_changed`,
  `signup.claim_removed` in `internal/audit/audit.go`. New notify kind
  `signup.claim_ended` in `internal/notify/kinds.go` (bell + email, no
  push, copy names the event and date only as far as the recipient may see
  them — reuse `eventNoticeVars` at `api_registrations.go:333`, and never
  the sheet's slot note). No `waiting` rows, so nothing to resolve.
- `deleteEventRows` (`api_events.go:1158`) deletes claims, slots, sheets
  for the event's occurrences before deleting the occurrences.
- Errors: new `slot_full` (409), `slot_quantity_below_claims` (409),
  `signup_date_over` (409), `signup_quantity_invalid` (400) in
  `api_errors.go` + `ERROR_COPY`; reuse existing `missing_field` /
  `too_long` keys for title, label and note length where the server
  messages already exist (`api_errors.go:229`). Date-invisible answers
  404 with the generic code; DEC-52 buys nothing here since every write
  needs sign-in.
- JSON types in `api_signups.go`, pairs in `tsContracts`
  (`guards_test.go:66`), mirrors in `web/src/types.ts`. Do not add sheet
  fields to `occurrenceJSON`; the event page fetches sheets per visible
  date on open (slice c) so member payloads stay unchanged.

**Slice R14c — UI.** Event page only in v1.

- New client block beside `Events` in `web/src/api.ts` (`web/src/api.ts:360`)
  and types in `web/src/types.ts`: list, create/update/delete sheet, slot
  CRUD, claim, release, manager claim removal.
- New components in `web/src/components/`: `SignupSheets.tsx` (the
  member view on a date card: each sheet's title and kind as a `Chip`,
  each slot's label with "2 of 4 claimed" and claimant names + notes when
  the response carries them, own claim with a "Change" / "Release" pair,
  a claim form — quantity `Input` with its range stated next to the field
  and an optional note — only when the slot has room or the viewer's own
  claim can grow; full slots say so in words) and `SignupSheetEditor.tsx`
  (manager panel in the `ManageRoster` pattern,
  `web/src/components/ManageRoster.tsx:17`: create a sheet with slot rows,
  edit labels and quantities, add/remove slots, delete the sheet with a
  confirm that names how many claims end, remove one claim). Both use
  `Form`, `Field`, `Button` from `components/ui`; one primary action per
  panel ("Save sheet" in the editor; claiming is a secondary per-slot
  action, consistent with the date card already having a primary RSVP
  control).
- `web/src/pages/EventDetail.tsx`: render `SignupSheets` inside each
  `DateRow` under the RSVP block (`web/src/pages/EventDetail.tsx:381`),
  loaded per date when the card mounts (dates are already capped by
  `CompactDates`, `web/src/pages/EventDetail.tsx:233`); managers get an
  "Add sign-up sheet" / "Sign-up sheets" item in the date's More menu
  (`web/src/pages/EventDetail.tsx:355`) opening the editor in place. A
  claimant with no going registration sees the "Tap Going too…" hint
  (decision above). Signed-out viewers of a public date see counts and a
  "Log in to claim a slot" line, never names or a claim form.
- `web/src/pages/EventDate.tsx` does not change in v1; sheet entry for
  exact-date-only invitees is a named follow-up (Risks).

### Data / API shapes

`GET .../dates/:date/signup-sheets` (member who may see names):

```json
{
  "sheets": [
    {
      "id": 7, "occurrenceId": 340, "title": "Potluck", "kind": "potluck",
      "canManage": false,
      "slots": [
        {
          "id": 21, "label": "Main dish", "quantityNeeded": 3,
          "quantityClaimed": 2, "position": 0,
          "claims": [
            { "userId": 51, "name": "Sam Member", "quantity": 1, "note": "Chili" }
          ],
          "myClaim": { "quantity": 1, "note": "Cornbread" }
        },
        { "id": 22, "label": "Dessert", "quantityNeeded": 2,
          "quantityClaimed": 0, "position": 1, "claims": [], "myClaim": null }
      ]
    }
  ]
}
```

Viewers without name access get the same shape with `claims` omitted per
slot and only `quantityClaimed` + `myClaim`. Create/update bodies are the
same sheet shape minus ids and computed fields; the claim body is exactly
`{quantity, note}`. Responses to claim and slot writes return the full
sheet list for the date, so the client never patches counts locally.

### Tests + e2e

- Policy table tests in the new `internal/policy/signups_test.go`:
  date visibility public/private/invite-only × viewer (signed-out, member,
  non-member, leader, host, admin), per-date override widening and
  tightening the sheet with the date, pending/archived group closed like
  `CanSeeEvent`, claim refused when cancelled or ended, exact-date access
  allowed only with register facts, teen member may claim (no rule bars
  teens from events they can see; DEC-20) but never manages.
- Handler tests in `api_signups_test.go`: full lifecycle; **the race** —
  mirror `TestConcurrentGoingRespectsCapacity`
  (`api_registrations_test.go:108`): 30 clients race a slot with
  `quantity_needed` 5 claiming 1 each, exactly 5 land, the rest read 409
  `slot_full`, and the claimed total is 5; quantity-update race (two
  updates growing claims) also cannot overshoot; shrink below claimed
  refuses; delete slot/sheet notifies claimants and audits; manager claim
  removal notifies; names hidden from a viewer without attendee-name
  access while counts stay; hidden-group sheet never leaks the group name,
  place or id in any response or notice; cancelled date freezes claims but
  allows release; disabled account's claim frees its quantity; making the
  date more private shuts an outsider's sheet read to 404 at once; event
  deletion removes sheets, slots and claims.
- Notify registry tests follow the existing kinds tests
  (`internal/notify/kinds_test.go`): `signup.claim_ended` renders with no
  email address, phone number or slot note, and writes no push text.
- Vitest: `SignupSheets.test.tsx` (counts-only vs names rendering, claim
  then release, full slot with no form, own-claim growth limited to the
  room left) and `SignupSheetEditor.test.tsx` (create with slots,
  quantity shrink error shown under the field, delete confirm counts
  claims) and the date-card wiring in `EventDetail.test.tsx`.
- Playwright: new `web/e2e/signup-sheets.spec.ts` — leader creates a
  potluck sheet on next week's date; a member claims "Dessert ×1" with a
  note at 390px and sees it after reload; a second member sees the name
  and "1 of 2 claimed"; a signed-out visitor sees counts only; leader
  removes the claim and the member gets the bell notice. Verify-ui
  evidence at 390 / 768 / 1280 for the date card with a sheet (axe clean,
  no sideways scroll).

### Acceptance checklist

- [ ] Migration at the next free number (or folded into the R9 baseline);
  models + `All()` updated; drift check passes; PLAN 5.3 amendment for
  the occurrence anchor raised in the PR.
- [ ] A member can claim, change and release a slot from the event page on
  a phone; totals shown always match a fresh server read.
- [ ] Concurrent claims never exceed `quantity_needed` (race test green).
- [ ] Sheet visibility is the date's effective visibility on every read
  and write; names and notes appear only where attendee names would.
- [ ] Signed-out and guest accounts cannot claim; nothing accepts a nil
  person or a guest row as an actor.
- [ ] Deleting a slot or sheet and removing a claim each notify the
  affected people in the same transaction as the audit row; ordinary
  claims write no notices.
- [ ] Cancelled/ended dates refuse new claims; event deletion removes
  all sheet rows; disabled accounts' claims stop counting.
- [ ] No payments, no recurring templates, no reminder or audience
  changes; error codes and `ERROR_COPY` complete; `make test`, web
  tests, e2e and the verify-ui 390px pass all green.

### Risks / privacy

- The note is free text shown to everyone with name access; people will
  paste phone numbers into it despite the hint. v1 accepts this (same
  class as group descriptions) but the note never enters a notification,
  an email or a push, and never prints for counts-only viewers.
- Claimant names on an opened date of a hidden group follow
  `CanSeeAttendeeNames`, which already withholds them from outsiders
  (`internal/policy/policy.go:297`). The handler tests must assert the
  group name, place and id never appear (`groupShownTo` discipline,
  `api_event_access.go:133`).
- Exact-date-only invitees cannot reach the sheet in v1 (the EventDate
  page has no sheet UI and they cannot see the series). The API allows
  their claims, so the follow-up is UI-only; until then a leader cannot
  use sheets to coordinate that one invited helper. Stated, accepted for
  v1.
- One sheet per date by hand will irritate weekly groups. That is the
  price of the small model the audit approved; a "copy previous date's
  sheet" button is the natural first follow-up and needs no schema change
  (it writes fresh occurrence sheets).

### Dependencies

- Sequence: after R10 (audit order), but no code dependency on it — the
  two share only the `deleteEventRows` extension and the R9 window.
- If R6 (notification outbox) has landed, `signup.claim_ended` uses its
  delivery path; otherwise the current `notify.Send` + post-commit
  delivery pattern (`api_registrations.go:398`). R4's erasure inventory
  gains `signup_claims` (rows stop counting, names go with the
  account row).
- No jobs, no settings, no push. Nothing from R7/R8.

### Open questions (with defaults to implement by)

1. When a date is made more private, should claims by people shut out be
   cancelled and the person told (the registration rule), or kept and
   hidden? **Default: keep the claim, hide the sheet** — the claim is a
   promise of casserole, not attendance, and the leader still sees it and
   can remove it. If the owner wants parity with `cancelLosers`, it is a
   follow-up slice, not v1.
2. Guest (signed-out) claims on public dates? **Default: out of v1**
   (decision above; costed against the guest machine, not assumed cheap).
   Revisit only with a guest-claim design that budgets the intent row,
   redemption hook and manage surface.
3. Keep `kind` (volunteer/potluck/supplies/other)? **Default: keep**, as a
   label only.
4. Claim quantity per person — allow claiming more than 1 of a slot?
   **Default: yes** (decision above); "bring 2 of the 4 sides" is the
   common case and costs one column.
5. Slot ordering — `position` column now or sort by creation forever?
   **Default: `position` now**, set by create/update order; no drag UI,
   just stable leader-chosen order instead of id order.
