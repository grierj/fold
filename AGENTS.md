# AGENTS.md — working instructions for AI agents on this repo

Working title: **Fold**. Church small-group / ministry / activity management: the
group-management slice of ChurchTrac, without the rest of it. `docs/plan/PLAN.md` is the source of
truth for product decisions and the data model; when it disagrees with older repo docs (including
this file), follow the plan.

## The one rule

**Deployment simplicity is the top concern.** Whatever you build, a church volunteer (or a single
SaaS operator) must be able to run it with `docker compose up`. The shipped artifact is always a
**single Go binary** that serves both the JSON API and the frontend (the SPA bundle is embedded via
`go:embed`). No separate frontend hosting. Ever.

## Stack

- **Go**: language minimum and toolchain pin come from `go.mod`. Gin (JSON API under `/api/v1`), GORM.
- **PostgreSQL only**, everywhere: production, development, tests (`DATABASE_URL`, DEC-1, DEC-24).
  Use Postgres features freely. `CGO_ENABLED=0`; keep it that way.
- Schema: versioned **goose** SQL files in `migrations/`, embedded in the binary and run on start
  under an advisory lock. They are the source of truth; GORM models must match them (C1).
- **React + Vite + Tailwind** SPA in `web/` (Node major in `.nvmrc`); `web/dist/` is embedded into
  the binary. Release runs `npm run build` before `go build`.

## Architecture

Browser → one Go binary (`/api/v1/*` JSON with cookie auth, embedded SPA with `index.html`
fallback) → Postgres.

- Single-tenant. "SaaS" means many single-tenant deployments behind a proxy: **no multi-tenant
  data model**.
- Auth: cookie sessions + bcrypt. OIDC (Entra ID, Google) comes later; keep an `AuthProvider` seam
  in mind, don't build it yet.
- CSRF: hand-rolled synchronizer token (`csrf.go`), not gorilla/csrf, whose Origin/Referer checks
  break plain-HTTP dev. Every POST/PUT/DELETE needs the `csrf_token` form field or `X-CSRF-Token`.
- Approval workflow is a core feature: new groups enter `pending` until staff approves. Keep it
  working through every refactor.

## Data model

Full model: PLAN 5.3 and `models/models.go`. Chain: `User` → `Membership` → `Group` → `Event` →
`EventOccurrence`; `Registration` links a user to one occurrence. A church-wide event has
`events.group_id` null and is run by its `EventHost` rows. Invariants agents trip over:

- The wall clock (`starts_local` + IANA `tzid`) is the truth. Occurrences store UTC, generated 18
  months ahead. Rules repeat at most once a day, under a date cap (`events.Expand`, DEC-89).
  `internal/events` is the pure clock (no database); `internal/eventdates` writes the rows (`Ensure` at
  creation, `Extend` daily under its advisory lock).
- An event's `visibility` is its own value, copied from the group at creation (DEC-48). A nullable
  occurrence `visibility_override` wins for that date (`policy.EffectiveVisibility`); date
  candidates never authorize the series. Making a group more private tightens inherited events and
  asks about leader-opened ones (`api_group_visibility.go`).
- `cancel_source` is `series` (a series edit dropped the date; a later edit that includes it
  schedules it again and tells registrants, DEC-91) or `manual` (a person).
- Render any name with `User.PublicName()`, never the email. Sign-in needs `User.CanSignIn()`;
  login throttling is in Postgres (`login_throttles`, `throttle.go`). `Email` is the login (DEC-23).
  Deleting an account scrubs the user row to a tombstone and deletes its tokens, calendar link,
  devices and owned throttle keys (`account_erasure.go`); further stages are tracked in the M7.4
  inventory.
- `contact_requests` text is immutable except for one blank (R4-T07A): erasing an `occurrence_add`
  subject clears their `add_childcare_ages_note` through `eraseContactRequestTextIn`
  (`account_erasure.go`), which the two request triggers allow only for a soft-deleted subject
  (`00004_ages_note_erasure_scrub.sql`). `reason` is untouched pending R4-T07B; the baseline still lets
  `reason` change on `contact` rows and pending add rows, and the source guard stops new writers.
  Staged `00007` (T07B-2a, local, no caller) adds a licence for a reason-only blank on non-contact,
  non-pending rows with a soft-deleted party; contact and pending rows keep the baseline.
- Optional email uses one effective-choice reader, `readEmailOff` in `email_optouts.go`: the account's
  latest email choice or the actual destination address's retained opt-out can turn a category off.
  `email_address_optouts` is written only by `exportEmailOptOutsIn` during erasure with a held address,
  and deleted only by the current holder's affirmative turn-on under the user lock. Keep the original
  choice time; never reconstruct a legacy tombstone address. Settings expose only effective choices.
  Version 2 links bind the destination address for turn-on; legacy and moved-address links still allow
  turn-off, and erased-account links stay invalid. A session older than an address change cannot turn
  email on. Batch rows recheck `message_recipients.address` inside each row's claim transaction after
  its claim and before rendering and dispatch; optional notifications check inside their claim too.
  At READ COMMITTED, a choice committed before the choice statement starts is honored. A later change
  may not stop that one in-flight send; every later row and retry rechecks. The composer test checks
  the session and choice under the author's row lock, releases it at commit, then calls the provider.
  A change committed after that commit, before or during the call, may not stop the one test send.
  Required mail bypasses choices; provider suppressions remain separate. R4-2d2 is implemented locally
  only; C2 and publication remain pending (inventory 7.6 and 15.8).
- Deletion outcomes and the restore barrier (M7.4 T07B-1b and T07B-1c, staged: committed locally as `cd3ad9a` (1b), `99fd0b3`
  (merge of the reason schema) and `5f941e6` (1c); unmerged, unpushed, unpublished; final checks at the landing head, the
  four-concern integration review, the deployment-level drill and the first-church hand checks are pending; C4 is not met and M7.4
  is not complete; inventory 15.9). It is two halves that land together, and a release carrying them reports
  `recoveryPolicy` 1, which must never be published before R4-6c-R's current-release restore tooling and operator procedure land
  and are verified.
  - Boot step: `bootRestoreFence` (`restore_fence.go`) runs in `serve` after migrations and before anything serves. One
    transaction under the migrations advisory lock creates and advances the `settings` row `restore_fence`; only an acknowledged
    COMMIT sets the in-memory floor, and any failure exits non-zero. A restart is the restart boundary: ids and the floor belong
    to one process.
  - Commit-aware fence: every fenced `a.contactMutationCtx` (`apiAdminDeleteUser`, the settle route) takes
    `a.takeRestoreTicket(tx)` first (the one-slot turn under the contact gate, then the counter), defers
    `a.abandonRestoreTicket(&tk)` before the call and runs `a.acknowledgeRestoreTicket(&tk, err)` as the next statement. The floor
    is raised only when the call returned nil (an acknowledged COMMIT), and only then is the turn released, so a later ticket never
    reads the counter between a COMMIT and its acknowledgment (`TestFencedTransactionsTakeTheTicketFirst`). A ticket at or below
    the floor, or a missing row, is 503 `restore_detected`, and `requestRestart` makes `serve` exit 1 for the supervisor. Only the
    boot step and the ticket write the row (`TestRestoreFenceHasTwoWriters`); `setSettingIn` (`settings.go`), the one settings writer, calls
    `refuseRestoreFenceKey`. Detection limit, stated: a stored counter or boot value alone detects nothing; only the in-memory
    floor does, and only when a restore loses a commit this process saw acknowledged. A restore of a dump taken after the last
    acknowledged fenced commit, one that loses only an unknown commit, and one made while the process was stopped are not detected;
    `TestRestoreFenceUndetectedCases` pins that the first two never answer "not applied" for a deletion that ran.
  - Attempt ids (`admin_operation_attempts.go`): `POST /api/v1/admin/operation-attempts` mints an id under a per-process key; only
    that process mints or verifies it, and it runs at most once, in that process, if first submitted within 10 minutes. The
    ledger is memory only (its mutex never waits: `TestLedgerMutexHoldsNoWait`) and is checked before an id's age; the receipt row (`admin_operation_attempts`, T39) is inserted right after the ticket in the deletion's own transaction
    and a daily `admin_attempts.sweep` job deletes receipts older than 24 hours. `POST .../settle` answers `applied`,
    `not_applied` (`subjectDeleted: true` only when the account is already gone), `history_unavailable` or `checking`;
    `not_applied` comes only from the ledger in memory. `attempt_expired` means only that this submission ran nothing; its copy
    never says "nothing was deleted". After a restart or 60 minutes an id settles `history_unavailable`.
  - Actor seal: `contactChange.Seal` runs `currentAdminIn` (`FOR SHARE NOWAIT` on the actor's own row, decided by `sessionCurrent`:
    sign-in ability and both session versions) as the last locking statement before COMMIT, with or without an attempt id
    (`TestActorSealIsLast`, `TestCurrentAdminInFacts`); a busy seal retries at most three times and answers 409
    `admin_account_busy`, and settle makes at most three sealed tries in two seconds and then answers `checking`.
  - Panel (1c): `DeleteUserPanel` mints an id per press and keeps it in memory. A lost, unreadable or ambiguous answer settles
    the same id with Delete off and shows no "nothing was deleted" copy first; definitive refusals discard the id; a
    `restore_detected` or `history_unavailable` answer keeps Delete off until the users list reloads successfully
    (`lib/deleteReload.ts`).
  - Test-only clock: `attempt_clock_e2e.go` (build tag `e2eclock`, with `e2e_hooks.go`) adds an offset to the App clock (`a.now`, the one injected clock; the other test seams are the `a.hooks` struct in `test_hooks.go`) and an
    admin-only, CSRF-protected `POST /api/v1/e2e/attempt-clock`, and only when `FOLD_E2E_CLOCK=1`. `web/e2e/run.sh` and
    `server.sh` build with the tag and set the variable; the Dockerfile, release build, `make test` and `go build` never set the
    tag, which is checked by review, not by a test.
- Household erasure (M7.4 R4-3b, staged: local, unmerged, M7.4 not complete): deleting an account also runs
  `eraseHouseholdRecordsIn` (`account_erasure_households.go`) under household rows taken `FOR UPDATE NOWAIT`.
  It deletes the account's seat, anonymizes the person row(s) that are the same individual (`PERS`, read
  unscoped: `converted_user_id = U`, or the person of the approved `household_conversion` request with
  `approved_user_id = U`) and soft-deletes them, and **never writes `converted_user_id`** or sets a `user_id`
  on a staged person's registrations (that would record a completion that never happened). `validatePersIn`
  aborts the whole erasure before any write when the identity links contradict each other; never widen `PERS`
  by household, kind, birth date or surname. It revokes pending and confirmed guardian links of `PERS`, of
  the account as teen and as guardian (a guardian's links to surviving children or teens are revoked, those
  people and their household are not anonymized), one `GuardianLinkRevoked` audit row each, and leaves `check_note`,
  `teen_consents` and their audit mirrors untouched (O-4). A household's name and address become
  `Removed household` and `''` only when `householdErasedOwnerIn` proves it sole-subject (no seat of any user,
  every person in `PERS`, soft-deleted included); any other household is another family's record, retained
  and counted in the `erasure` report the delete answers (`householdsKept`, `householdsScrubbed`,
  `peopleAnonymized`, `guardianLinksRevoked`; counts only). Households are locked only by
  `lockErasureHouseholdsIn`; `TestErasureHouseholdLockCensus` pins the names of the 14 `lockHousehold` callers (the public conversion accept reaches it through `lockConversionParticipants`) and fails on
  a new one; their classifications and lock orders are inventory 9.6.1 and are kept by review, not by the test.
- Focal points (`AvatarFocalX/Y`, `CoverFocalX/Y`) are stored 0-100 and rendered as CSS
  `object-position`. `AvatarKey` is a blob-store key, not a URL.

## Conventions

- API: `/api/v1/...`, camelCase JSON. Authenticated endpoints return 401 JSON, never a redirect
  (only `/setup` redirects). Errors are `{"error": "human readable"}` with the right status.
- Web API client: `web/src/api/core.ts` owns every request (`api`, `apiUpload`, `apiCSV`, `apiIfChanged`), so CSRF, the
  401 sign-out hand-off (`configureApi`, called once in `main.tsx` before the first render) and the unreadable-body flag
  behave the same for all. One module per resource beside it (`groups.ts`, `events.ts`, ...), paths built with the `path` tag
  and queries with `query({...})`, ids typed `Id`. A request that starts or ends a session passes `rotatesCsrf`; one that
  may be anonymous (sign-in, the boot-time session check) passes `anonymousOK`. A response with its own shape is an
  `export interface` in `types.ts` with a `tsContracts` pair in `guards_test.go` (a named Go struct, not a `gin.H`).
- Strict CSP (DEC-37): no inline `<script>` or `style=""` in server templates, no `eval`, no CDN
  or third-party scripts, styles or fonts. React `style={{...}}` props are fine. Bundle through Vite.
- Styling (DEC-60): use the role tokens and shared control classes in `web/src/index.css`
  (`bg-primary`, `text-text-muted`, `.btn-primary`, `.input`, `.chip`). Pick the role by meaning,
  not by the color you want.
- UI controls (DEC-60–DEC-66): new controls use `components/ui` (`Button`, `Chip`, `Form`, `Field`
  with `Input`/`Textarea`/`Select`, `EmptyState`); the ratchets below stop new raw ones. One primary
  action per view; `danger-outline` beside a main action, solid `danger` only when destruction is
  the point of the dialog; empty states say what would appear and offer the next step, never in
  red; status is never color alone; copy is plain, warm, second person. Prove a UI change with
  `.cursor/skills/verify-ui` and the 390px screenshots. Reference: Meetup, then ChurchTrac; familiar
  beats original.
- Errors (DEC-61): an error a person can act on is an `*apiError` value (`api_errors.go`), declared once
  as a package variable with its status, code and message, `var errX = apiErr(http.StatusConflict, "x", "...")`
  (`api_error_values.go` holds the ones with no better home), and a line in `ERROR_COPY` (what happened, what to
  do next). Domain code returns the value, wraps it with `%w` or compares with `errors.Is`; a handler answers any
  error with `fail(c, err)`: the value's own status, code, message and extra keys, anything else a logged 500 with
  no detail. One refusal that must answer differently in two places is two values (`apiErrWith(base, gin.H{...})`
  adds body keys and still matches the base; `errEventFullNoWaitlist`); a body that carries data is an error
  with an `apiAnswer()` method (`errNeedSuccessors`, `householdRegistrationError`). A message built at run time
  uses `apiErrf` (not declared; the code is written at the call). `jsonError(c, status, msg)` is only for a message
  with no code of its own: its code is the general one for the status. A declared message is written once, in its
  declaration (`TestDeclaredMessagesAreWrittenOnce`); `apiErr` runs only as a package-level initializer
  (`TestAPIErrorsAreDeclaredAtPackageLevel`). `errSecuritySession` is itself the 401. DEC-52 decides what is
  never an error.
- Account deletion outcomes (R4-CTX, R4-3b-1): `apiAdminDeleteUser` runs the whole erasure in
  `a.contactMutationCtx(ctx, ...)` with one `adminDeleteBudget` (40 s) context made from
  `context.Background()`, never the request's, so a disconnect cannot abort a commit. Classify its error only
  with `classifyContactErr`, **commit phase first**: any COMMIT error (`*contactCommitError`) is unknown, whatever
  it wraps, and answers 500 `delete_outcome_unknown` ("couldn't confirm", never "nothing was deleted") and is
  never retried; a spent budget, the commit reserve or SQLSTATE 57014 answers 503 `delete_timed_out`. Retry
  **only SQLSTATE 55P03 with `rollbackConfirmed(err)` true** (the ROLLBACK was acknowledged, so the server
  released the gate and the user lock), at most three attempts within `deleteRetryWindow` (2 s), each rebuilt from the
  target's id alone; a 55P03 inside a `*contactRollbackError` stops with no further BEGIN, and it and exhaustion both answer 409
  `delete_household_busy`: nothing was deleted because COMMIT was never sent, which is not the same as the locks being
  released (that is what the acknowledgment proves, and an unacknowledged ROLLBACK lacks). 40P01 is never retried. These three codes are
  declared values (`errDeleteOutcomeUnknown`, `errDeleteTimedOut`, `errDeleteHouseholdBusy`), real copy on a
  5xx on purpose, answered with `fail(c, ...)`, and need an `ERROR_COPY` line. Return is not release: after a client
  deadline the server may hold the gate until D + `contactServerCap` + the idle timeout, and no test equates the two.
  `internal/testdb/pgproxy.go` is the test-only fault proxy for these cases; production code may not import it.
- Accounts (A1): capabilities live in `tokens.go` only (find, `claimAccountToken`, rotate, revoke; lock order contact gate →
  user → token) and every rate limit in `throttle.go` (`countThrottleHit`, plus the audit-counted `inviteSendAllowed`);
  advisory lock ids are the one block in `advisory_locks.go`. An existing account's status changes only through
  `setAccountStatusIn` (`account_status.go`, which documents the order) and a new one starts in `newAccountStatus`. Profile
  fields are validated by `fields.go` (trimmed, counted in characters). A pending item is decided with `decidePendingIn`.
- Sessions (`session_state.go`): "this request's session is still current" is decided in one place,
  `sessionCurrent(fresh, session)` (a live row that `CanSignIn()` and carries both the session's
  `SessionVersion` and `LogoutVersion`), by the middleware, the actor seal and every locked recheck. A
  writer that rechecks its own actor under a lock calls `lockActorIn(tx, me, mode)` (or `lockAdminIn`),
  with `actorRead`, `actorShare` or `actorNoKeyUpdate` keeping the lock strength its transaction needs;
  one that holds the actor in a batch of locked rows calls `sessionCurrent`/`adminCurrent` on that row.
  Never compare the versions or `CanSignIn` by hand. Ending every session is `endAllSessionsIn` (epoch
  bump plus `revokeAccountTokens`); a bump alone uses `sessionVersionBump()`. The cookie's keys are the
  `session*Key` constants and `setSessionLogin`/`clearSessionLogin`; `errSecuritySession` is answered
  401 by `fail(c, errSecuritySession)` (`answerStaleSession` when the caller only wants that case).
- Rooms (`internal/rooms`): "the room is busy" is one definition, `rooms.HeldCondition` over `rooms.PaddedRange` (a
  booking's date widened by setup and teardown; `paddedRangeSQL` is its four-placeholder form), and every busy count
  (date picker, series picker, create-form check, suggestions) is `rooms.BusyCounts`. Only the admin request list and
  `heldByOverlap` embed the condition directly. A date's place is `events.EffectivePlace` / `EffectivePlaceSQL`
  (override, else the event's, else the group's); group tags compare through `internal/grouptags`; a default-off
  switch read inside SQL is `settingOnSQL`. Each SQL form has a test that it agrees with its Go form.
- Membership rules live in `internal/policy` (`CanRemoveMember`, `CanBeSuccessor`, role changes).
  Removal keeps the row with `status=removed` (DEC-35); voluntary departure keeps `status=left`
  (DEC-86); neither counts as active. Adding anyone to a group goes through `changeRole`, which
  locks the group row and checks `policy.GroupHasRoom` (DEC-39) and `policy.CanAddToGroup`. One
  exception, on purpose: the sole-leader handoff in an account delete or disable (`handOffLeadership`,
  `api_handoff.go`, DEC-36) installs the successor directly. It replaces the leaving leader rather than
  growing the group, so the member limit does not apply, the actor is already sealed by the delete
  transaction, and it writes its own `role_changed` row carrying `departingUserId`.
- Policy facts (`policy_facts.go`): `asPerson`, `asGroup` (nil-safe), `membershipRel`, and the one batched
  loaders for groups, a user's memberships and host rows (`groupsByID`, `groupRels`, `userRels`,
  `hostedEvents`, `loadFactSet`; `eventFactsIn` is a batch of one). They return their errors; a failed read is
  never "not visible" or "no member". Host rows count only on church-wide events (DEC-7), so
  `factSet.Hosted` never holds a group event. Counts for a group's room are `memberCount` and
  `leaderCount` (`group_members.go`) and return errors too, so `GroupHasRoom` never sees a made-up zero.
  Visibility is `visibleTo` in `internal/policy`; the SQL forms (`directoryQuery`, `losersForDate`) are kept
  equal to it by `TestDirectoryMatchesPolicy` and `TestLosersMatchPolicy`.
  Leading a pending or archived group grants no power to add people or invite anyone into Fold
  (DEC-88; `policy.LeadsApprovedGroup`, `CanInviteIntoGroup`).
- Registrations: seat decisions go through the ledger in `registration_seats.go` (`poolFits`, `seatChange`, `seatTally`;
  party size and childcare demand each have one SQL and one Go form, `TestSeatSQLMatchesGoRules`). Registration rows are
  written only by `saveRegistration` and `promoteRegistration` (`registration_rows.go`: the status decides what is cleared;
  the access columns stay with `registration_access.go`). A household person's holder is judged by
  `subjectFacts.householdProblem` (`policy.CanRegisterHouseholdPerson`); `listedRegistrations` only pre-filters
  (`TestListedRegistrationsPrefilterMatchesPolicy`). The dropped-answer audit keys are `meta*` in `api_registrations.go`
  (`TestRegistrationAuditKeysAreShared`). Lock order for these writes is written beside the helpers in `registration_locks.go`.
- Permissions: every check is a pure function in `internal/policy` (PLAN section 6) with table
  tests. Handlers load facts and call it; never inline role checks or read `Group.LeaderID`
  (display only). Leadership is `Membership.Role = leader`; a group can have several leaders.
- Time zones (`zones.go`): the church's zone is read with `churchZoneIn(tx)` on the caller's connection and
  returns an error for a failed read or a stored value that is no zone; decisions from a person's age (household
  eligibility, contact rules), the avatar sweep and the age rollover use it and return the error. `churchZoneLenient`
  logs a stored value that is no zone and uses UTC but still errors on a failed read (the daily job queue and guest
  verification mail, which retry); `churchZoneOrUTC` also shows UTC on a failed read, for pages, emails, notices, push
  quiet hours, reminders and ages shown on a page. A person's zone is `userZone(tz, church)`
  (`userZoneIn` reads the church only when needed); an event's is `eventZone(tzid)`. Household ages count from
  `churchClockIn(tx, now)`; a transaction takes `now` once and passes it down. All parse with `events.LoadZone`, which
  refuses "Local".
- Settings (`settings.go`): every key is a constant there; read with `settingIn`/`boolSettingIn`/`siteNameIn`, write only
  with `setSettingIn`, which refuses the restore-fence key. Keys derived from `SESSION_SECRET` come from `deriveKey`
  (`keys.go`); a new use gets a new `purpose:vN`, and an existing purpose's bytes never change. SQLSTATE checks use
  `pgCode`/`pgConstraint` (`pgerrors.go`); long id lists go through `eachChunk` (`chunks.go`); several users are locked
  with `lockUsersByID` (`user_locks.go`). Enum values (visibility, group status, account status, video reveal) are owned by
  `internal/policy`, and `models` aliases them; `models.CurrentRegStatuses` and `models.MembershipActive` name the
  common status sets.
- Requests (contact, group add, date add) are one `contact_requests` table and one lifecycle. What a purpose
  decides (audit target and actions, notice subject, link and kinds, review tasks, reason-review actions) is
  `requestKind` in `request_kinds.go`; add a purpose there, not in a `switch`. The handlers share `lockRequest`,
  `openRequest`, `recordDecision`, `withdrawRequest` and `readRequests` (`request_lifecycle.go`). A single request
  is presented as a page of one (`presentContactRequests`, `presentAddRequests`) and "may this viewer see the
  request's group or event" is `requestContexts.shown` (`request_context.go`) alone; the account-deletion reason
  review asks it too. The two creation functions stay in `api_contact.go`: the contact-text guard pins them.
- Audit: record sensitive actions with `audit.Write` (`internal/audit`) in the same transaction as
  the change. Admin notices are separate stored notification rows written in that transaction.
- Migrations: every schema change is a **new** goose file `NNNNN_name.sql`; never edit an old one; the one exception is the pre-live baseline window in DEC-138.
  The model change and `models.All()` go in the same PR. Two open PRs can pick the same next
  number; git merges both without a conflict and goose then refuses to boot. Master's ruleset
  requires a PR to be up to date with master before it merges (DEC-146), so the later PR merges
  master, fails `TestBaselineFileShape` with "duplicate version" and renumbers its file. If a
  duplicate reaches master anyway, renumber the file that merged later, in a new PR (never a file a
  tagged release holds), and recreate every database that applied it under the old number: goose
  tracks versions by number alone, so it would count the other file as applied and skip it.
- Tests: `make test` (`go test ./...` against `DATABASE_URL`; `scripts/go-test.sh` shards the root
  package; starts the dev database if unset). Tests hash passwords at bcrypt's cheapest cost; a
  test that measures timing calls `realPasswordCost(t)` first. Handler tests use `httptest` and
  `internal/testdb` (reuse the cookie-jar + CSRF-dance in `testhelpers_test.go`). `testdb.Shared`
  clears the database before each test, which needs a superuser (dev and CI have one); other roles
  fall back to slower TRUNCATE. Test sessions use `synchronous_commit=off`.
  The restore tests (`TestRestoreFence*`, `TestReasonReviewRestoreWithOpenPanel`) need `pg_dump` and `pg_restore` of the
  server's major version, because a newer client sends settings an older server rejects: `FOLD_PG_BIN` (a directory of
  tools), else a matching one on `PATH`, else `scripts/pg-client.sh` (Docker, `postgres:<major>`). A mismatched client fails
  the test, it never skips; with no client and no Docker the test skips locally and fails under `CI`.
  Source guards (the parse-only tests in `guards_<concern>_test.go`, `throttle_keys_test.go`, `restore_fence_test.go` and
  the erasure censuses) read Go through `internal/guardkit`, a test-only package that `.deadcode-allow` lists like
  `internal/testdb`; `internal/images/imagetest` (shared upload-test pictures) is test-only too. `guardkit.Production(t)` walks the whole tree once with one skip rule set (no tests, `testdata`, `web/`,
  `node_modules`, `vendor`, hidden directories), so code moved into a subpackage is still checked; `Sources.Parse` parses it
  (fixtures too), `InspectStack` is the one parent-stack walker, and `Unparen`, `Callee`, `ImportNames` and `Squash` replace
  the per-guard copies. A guard whose subject is one package filters with `Parsed.Package("main")`, never a directory glob.
  Repo-wide guard files are named `guards_<concern>_test.go` (the layout guard allows that pattern at the root only).
- Frontend tests: `cd web && npm test` (Vitest). `make e2e` runs Playwright against the real
  binary (first run: `cd web && npx playwright install chromium`). `web/e2e/run.sh` splits specs
  across shards (`E2E_SHARDS`), each with its own server, database and seed; never rely on another
  spec having run first. Refresh `durations.json` with `E2E_RECORD_DURATIONS=1 make e2e`. a11y and
  layout checks run in parts (`UI_PARTS`, `partSelected`); `UPDATE_UI_BASELINE=1` needs
  `E2E_SHARDS=1`. Prefer `gotoSettled` to `waitForLoadState("networkidle")`. `signIn` clears
  cookies first. Assert on roles and visible text; accept `confirm()` with `page.on("dialog")`.
  Traces: `web/test-results/shard-<n>/`. The e2e binary is built with `-tags e2eclock` (`run.sh`, `server.sh`) and the server
  gets `FOLD_E2E_CLOCK=1`, which adds the test-only attempt clock; no shipped build has either.
- Legacy erasure is staged locally (R4-6b, not merged): `erasure.tombstone_sweep` starts a daily
  keyset page chain; `erasure.tombstone_page` continues it, at most 200 subjects plus one continuation
  per transaction; `erasure.tombstone` scrubs one exact soft-deleted `deleted-<id>@deleted.invalid`
  account under the contact gate and user lock. The numeric `user.erasure_swept` version committed
  with the scrub decides whether work remains, never a retained job. Keep the original church date
  in page payloads and daily keys. No tombstone address is reconstructed; address-only residue is
  unknown per subject (inventory 12.1 to 12.3). The local integration and its checks are recorded in inventory 12.3.
- Background work goes through `internal/jobs` (M3.1, PLAN 5.5): a job kind is a `jobs.Type` (name and `MaxAttempts`, 0 meaning `DefaultMaxAttempts`), registered as a `jobs.Kind` that embeds it (`Daily`, `Hourly`, `DailyPayload`, `OnDismiss` say how the schedulers and the System page treat it), and queued with `<type>.Enqueue(db, payload, at, uniqueKey)` or `<type>.Spec` for `EnqueueMany`; the attempt count is stated once, on the type. Give a `unique_key` when the work must happen once (daily tasks use `<kind>:<church date>`, hourly `<kind>:<UTC hour>`). The one kind list is `App.allJobKinds()`. Payload keys and unique-key shapes that raw SQL also spells live in `job_identity.go`, checked by `job_identity_test.go` (which also pins the `push.devices_off` trigger to its Go type: renaming it needs a new migration). An admin's retry, dismiss and the dead-job lock are `jobs.Retry`, `jobs.Dismiss`, `jobs.LockDead`; nothing else writes a job row's status. Handlers must be safe to run twice and return their error so the worker retries. Never start a goroutine ticker for periodic work.
- Notice writers (`notify.Send` callers) live in `notices_<domain>.go` for the domain they tell people about (`notices_groups.go`, `notices_accounts.go`, `notices_events.go`; the older writers in `api_registrations.go`, `api_room_*.go` and others move there as their slices are touched). They read recipients and names with the error-returning helpers in `notice_people.go` (`activeAdminIDs`, `leaderIDs`, `nameOf`, `userIn`, `userCanSeeGroupIn`, `loadNoticeFacts`; an event's runners come from `eventRunnerIDs` in `api_registrations.go`) and return any failure, so the caller's transaction rolls back instead of skipping a recipient. `nameIn` is the lenient twin for pages that render anyway. Leadership notices read role changes through `audit.ReadRoleMeta`; the writers keep spelling the keys in their entries (the audit writer census pins them).
- Application mail reaches a provider only through `sendTracked` (`mail_send.go`), which classifies a send as sent, suppressed, not configured, permanent or transient; each ledger (notices, announcements, direct mail, the composer test send) decides what that means for itself. Direct account mail finishes quietly (logged) for a suppressed address, a permanent refusal or no email service (`NoSender`, warned at startup); only transient errors retry. A nil `App.Mailer` counts as `NoSender` everywhere. `mail.Sender` is `providers.EmailSender`: one interface (`SendEmail`, `Capabilities`) for every adapter, the suppression wrapper and `NoSender`.
- Suppression reasons rank in one place, `mail.StrongestReason` (hard bounce, then manual, then complaint; an unknown reason counts as manual): the composer's counts, a send's refusal and the admin list all use it.
- Pushes share `pushTarget`, `pushHeld` (church switch, active account, consent), `pushOpensAt` (quiet hours, at least a second out) and `sendPush` (`push_deliver.go`); a notice's, an announcement's and a reminder's push differ only in what they check before and what they say. Reminder keys are `reminderKey` values (`reminder_key.go`), built and parsed there and in the one SQL expression `reminderKeySQL`.
- Notifications are stored rows (PLAN 5.12), written by `notify.Send(tx, notify.Msg{...})` in the
  same transaction as the change and its `AuditLog` row. The kind registry
  (`internal/notify/kinds.go`) never looks anything up, so the caller decides what the recipient
  may see (pass a group's name, place or link only when `userCanSeeGroupIn` or `noticeFacts.canSeeGroup` says so). Close tasks
  (`waiting`) with `notify.Resolve` wherever the subject is decided; when the same decision also sends to people who can hold that task (an admin who also leads the event), send first and resolve second, so both sides of an erasure want the user row first (`settleRoomDecision`, M7.4 N-23). `Send` also writes the delivery
  intent in that transaction (`BellOnly` records the bell row alone); the `notify.deliver` job sends
  the email and push after commit, so callers never deliver a notice themselves (R6). `Send` first locks its non-deleted recipients `FOR KEY SHARE` in ascending id (one query per 500) and skips a deleted one, so an account erasure either waits for the sender or finds nothing to resurrect (M7.4 R4-N, inventory 9.2.4). A fan-out that calls `Send` once per person takes that lock once with `notify.WithRecipients` and passes the `Scope` it is handed as `Msg.Scope`; `Send` checks the scope against its own transaction and recipients, and a forged, closed or foreign one fails with `ErrBadScope`, so no caller can skip the lock by assertion. Rolling back to a savepoint older than the lock releases it, so `WithRecipients` also watches the transaction (`internal/txguard`, a GORM plugin installed by `dbopen.Open`, which the server, the CLI and `testdb.Open` all use; a handle without it gets `ErrNoRollbackGuard`): a rollback to an older savepoint, or one it cannot read, ends the scope (a savepoint name counts only after GORM executed its `SAVEPOINT` without error and without `DryRun`; one that failed or never ran is untrusted, and one sent through `Row` or a query breaks the scope) and every later `Send` with it fails with `ErrBadScope`, while rollbacks to savepoints made inside the call (a failed nested `Transaction`) keep it. The watch reads the statements GORM sends, never the database, so it costs no query; it cannot see SQL sent around GORM, and Fold sends none. A repair that queues from an earlier selection goes through `notify.QueueDelivery` (`Protect`); nothing outside `notify` can insert delivery intent unprotected. Neither takes the contact gate.
- Guest registration (C5, PLAN M1.4): the rule is `policy.CanRegisterGuest`; never widen
  `CanRegisterEvent` to a nil person, and never load a guest row as an actor (`asPerson` would
  treat it as signed in). A submit stores only a `guest_signups` row; the guest `User`
  (`status=guest`) and `Registration` exist only after the emailed link is redeemed. Public
  answers stay uniform; verification emails cap at 3/hour and 10/day per address. Until a guest
  is upgraded to an account (same row), `changeRole` refuses to give them a membership.
- Forms: state every input requirement next to the field. Every path that sets a password checks
  it with `checkNewPassword` (`password_rules.go`): 10 characters minimum, 72 bytes maximum
  (DEC-90). On a failed submission, re-render with valid fields preserved; never echo back
  passwords or payment data.
- Uploads are resized in the browser first (`web/src/lib/image.ts`); the server still enforces type
  and size. Only `FOLD_TRUSTED_PROXIES` may set `X-Forwarded-For`. Commits: short imperative
  summary; PRs describe the behavior change.

## Direct account mail (M7.5, R6-4)

- Reconstructible account mail uses `mailKinds` on the existing job runner, with eight attempts.
  Enqueue with the domain change and audit in one transaction; standalone hints and guardian
  resends must durably enqueue before answering. Keep public answers uniform.
- Payloads contain only `userId` (reset and existing-account hints), `requestId` plus a fixed
  `case` (received, approved, exists, conversion_approved, rejected), `invitationId`,
  `accountTokenId` (email verification), `guardianLinkId`, or `guestSignupId`. Never queue an
  address, note, rendered message or raw capability.
- Prepare from current stored facts in a transaction, mint or rotate the capability there, commit,
  then send through the suppression-aware mailer without holding database locks. Match
  redemption's lock order: contact gate then user/link/token for account flows; invitation and
  guest mail each lock only their own row and read authority facts without earlier-order locks.
  Claims recheck the submitted hash, unused state and wall-clock expiry after locking.
- Delivery is at least once at the provider boundary. A retry may replace an already accepted
  email's link; only the newest capability works. Do not promise exactly-once email delivery.
- Accepted departures from the original R6-4 table: invitation delivery rotates only the hash,
  preserving its deadline, and says "expires within 24 hours"; email verification queues
  `{accountTokenId}` and preserves the original verify/cancel lifetime rather than reminting a
  pair; approved setup consent wording follows the locked user's current state rather than
  approval-time state; direct jobs reuse the 60-second `deliverSendTimeout` instead of the old
  30-second reset timeout. These preserve closed requests and cancellation windows, avoid stale
  consent instructions, and share the existing send bound.
- Two synchronous exceptions remain: `App.sendEmailChangeNotice` in `api_profile.go` needs the
  old address and cancellation capability, which cannot be reconstructed after verification;
  `App.sendGuestConfirmationEmail` in `api_guest.go` shares its manage capability with the browser,
  so delayed rotation would invalidate the displayed link. The composer's test send in
  `api_message_preview.go` also remains synchronous by design, separate from those exceptions. It
  checks the author's session and effective email choice under the user row lock before recording
  the test attempt, then commits before its provider call. An opt-out, erasure or address change
  committed after that commit may not stop that one test send; the next request rechecks. The call
  keeps its 30-second context and holds no user lock across provider I/O.
  Both account exceptions use the 20-second `syncMailTimeout`, leaving 40 seconds of the
  server's 60-second write budget for transaction work and the response. Delivery jobs keep
  the 60-second `deliverSendTimeout`. Failure logs use `jobs.CleanError` and never add a
  recipient address or capability.
- `TestMailGoesThroughJobs` recursively parses production Go directories, including `models`,
  `storage` and `migrations`. Only `internal/mail` is exempt from send checks because it owns
  sender implementations; legacy post-commit notice helpers and `runInBackground` are forbidden
  there too. Tests, `testdata`, frontend dependencies, vendored code and hidden directories are
  excluded. Root delivery-file allowances do not grant permission to same-named nested files.
- The guard follows mail import aliases and dot imports, `mail.Unwrap`, assigned senders and
  package variables across files in one directory. It recognizes `mail.Sender`,
  `SMTPSender`, `Postmark`, `DevOutbox`, `Suppressing` and `NoSender` in typed parameters, fields,
  variables, assertions and literals. Same-package helper signatures track each result position,
  including multiple results; `mail.New*` and `mail.WithSuppressions` return a sender in the first
  position, including through assigned constructor functions. Add new sender types to this list
  and the guard together. It also treats naming `sendTracked` like a send: only the delivery files
  (`mailerSendFiles`, now including `mail_send.go`) and the named synchronous methods may.
- Synchronous exceptions allow sends only as immediate calls in their named App methods.
  Calls to those methods must also be immediate, never inside closures, `go` or `defer`, or
  captured as method values, even in delivery files. Transaction closures may finish before
  the call; the guard checks syntax, not whether a transaction committed. `api_dev.go` may
  unwrap and read the outbox, never send.
- This is parse-only analysis, not a type checker or call graph. Field and method names match
  conservatively. Arbitrary helper bodies, cross-package helper results, helper function values
  other than known mail constructors/Unwrap, type aliases, embedded forwarding and senders hidden
  in untyped interfaces or containers need review. The guard uses parser-local bindings with a
  narrow, reasoned SA1019 exemption; it does not claim to resolve Go type identity.

## Uploads and blob storage

- Avatars and covers go through `storage.Store` (`storage/storage.go`, `App.Uploads`), never raw
  paths. Keys are server-generated, never client filenames. Uploads are content-sniffed
  (JPEG/PNG/GIF only; WebP is refused), re-encoded on the server so no EXIF or text chunk is stored, at most 5MB, served at `/uploads/<key>` with `nosniff`. For S3,
  implement `storage.Store` and make `serveUpload` redirect (PLAN 5.7); `UPLOAD_DIR` is the local
  path (`/data/uploads` in compose).
- Docker: the image carries only the binary (templates and the SPA are embedded), pins `WORKDIR /` (distroless nonroot defaults to `/home/nonroot`, off the volume) and pre-creates `/data/uploads` with `--chown=65532:65532` so
  the nonroot app can write to a fresh volume. Compose mounts the named volume `uploads` at
  `/data`; do not drop it (an anonymous volume is discarded on `up --build`).
- Avatar blobs are removed only by the `avatar.cleanup` job (payload `{userId}`, queued with `queueAvatarCleanup` in the same transaction as the key change, under the user row lock, every user-row hold bounded by a context deadline; `avatar_cleanup.go`). A row keeps its current key unless `deleted_at` is set. `LocalStore` works through `os.Root` with Lstat, open and `SameFile` binding at every directory and entry, and loops `ReadDir` to its cap. A new `Store` must meet the `CleanOwner` and `ScanOwners` contract with exact `<kind>/<id>/` prefixes and `CleanBatch`-sized calls. A store that serves local files must also meet the optional `Open` contract.

## Web push

M3.13, DEC-121 to DEC-129, `docs/plan/research/web-push.md`. The VAPID key pair is made on boot
under the migrations advisory lock and kept in `settings` (`vapid_public_key`,
`vapid_private_key_enc`, AES-GCM with a `SESSION_SECRET`-derived key; `push_keys.go`); a secret
change or `fold push keys rotate` makes a new pair and deletes every `push_subscriptions` row, and
a running server rereads the row on each send. `internal/push` is stdlib only and sends only to
the allow-listed push hosts (`push.DefaultHosts` plus optional `FOLD_PUSH_HOSTS`) over https, with
resolved-IP checks, no proxy and no redirects. An endpoint is a capability: never log, return or
put in an error an endpoint, `p256dh`, `auth`, the VAPID token or a body (`jobs.CleanError` strips
URL paths and tokens as a backstop). `push.deliver` is one job per device. Quiet hours (DEC-126,
M3.13d1): an optional push outside the recipient-local window (`push.Window`, the interim copy M3.8
replaces) returns `jobs.Later` and reruns every check when it opens; required kinds and `PushSoon`
date changes within 24 hours go at once; more than 20 optional pushes an hour are `suppressed`
"hourly limit". The service worker
(slice c1) has no `fetch` handler, so it can never serve a stale shell after logout. A kind
pushes only with `AllowPush` and its own short `PushRender` (stored on the row, never the bell
body cut short; safety kinds get the safe line, titled with `site_name` at send, DEC-134);
the `notify.deliver` job `notify.Send` queues does it. Announcement
pushes (M3.13d2, `messages_push.go`) are `message_recipients` rows with `channel = 'push'`; a
message's status, counts, settling, reaper and Try again read its email rows only, so filter
`channel = 'email'` in any new query over a message's rows. The 2-hour push reminder (M3.13d3,
`reminders_push.go`) is its own reminder message (key `push:<occ>:<start>:120`) with only push
rows and `messages.channel = 'push'`: `reminders.sweep` plans each person with
`Window.LastAllowed`, queues `push.deliver` at that time, and the job rechecks the date, the
event's `reminder_push`, going, visibility and the window (15 minutes' grace); the hourly limit
never stops it. No push shows a place (DEC-136). A trigger deletes
an account's devices when its sessions are revoked or it stops being active, and queues
`push.devices_off`, which writes the person a bell notice (migration 00059). Holding an endpoint
alone never moves a device to another account: its keys must match. Browser side (M3.13c1):
`web/src/sw.ts` builds to `/sw.js` (served `no-cache`, registered only by the card's "Turn on
notifications", never on load; it skips waiting and claims tabs, and still has no `fetch` handler), `/manifest.webmanifest` comes from Go (`internal/httpx/manifest.go`, church
name), and the icons come from `npm run icons`. The card's state code is `lib/pushState.ts`;
`lib/push.ts` is the eager part (logout, the icon badge). A device belongs to the person, not the
session: an expired session clears the icon count but leaves the subscription, and the card
re-saves it on load only for the user in `localStorage["fold.pushOwner"]` (wiped at logout).
The church's switch (`push_enabled`, Admin → Settings, M3.13c2) hides the card and the public
`/notifications/setup` page (via `/push/config` and `/site` `pushEnabled`) and stops every send.

## Phasing high-complexity work

Grier's rule (Oct 22, 2026). High-complexity means the change touches database, API and UI
together, or changes 3+ tables. Smaller work is exempt.

- Split such milestones early into thin end-to-end slices. Build each slice database, then API and
  internals, then UI. New logic can ship hidden until its UI lands.
- Schema changes are additive first (DEC-138). A release may add tables,
  columns, indexes and constraints and backfill data, but never drops or
  renames anything the previous release still reads or writes. Code in
  that release writes the new shape and reads new-then-old. The next
  release may drop the old shape (the contract step): its migration names
  the expand migration it contracts, and CI fails when that expand is not
  in an earlier tagged release. There are no audit triggers on old
  columns and no waiting period: one binary runs at a time
  (`docker compose up`), so the previous release is the only old version
  that can still be running.
- Never edit a migration any tagged release has applied. Before the first
  church, DEC-138 allows editing the baseline migration directly; that
  window closes at the first tagged release or first church deployment,
  whichever comes first.

## Development

`make dev` (dev Postgres from `deploy/docker-compose.dev.yml`, Go server, Vite), `make test`
(against `DATABASE_URL`, root package sharded, starts the dev DB if unset), `make lint` (shell, Go
vet/gofmt/staticcheck, web), `make e2e` (Playwright in parallel shards on fresh databases),
`cd web && npm test` (Vitest), `cd web && npm run build` (outputs `web/dist`, embedded). Run the
build once before `make dev` to try push: the Go server serves `/sw.js` from `web/dist`.
`make compose-smoke` boots the shipped Compose stack (app and Postgres) and checks `/healthz`,
the redirect to `/setup`, and that 5432 and 8080 are not published off loopback. CI's `compose`
job runs it on pull requests and on `master`. Caddy and real-domain TLS stay a hand check
(`docs/FIRST-CHURCH-CHECKS.md`).

There is no fallback database: `fold serve` without `DATABASE_URL` exits with a "start the dev
database" message, in dev too. Env vars: `config.go`, `.env.example`; `SESSION_SECRET` is required
unless `APP_ENV=dev`. CLI (C11, PLAN 11.4): `fold` (same as `fold serve`), `fold healthcheck`,
`fold migrate [up|status|down-one]` (needs only `DATABASE_URL`), `fold version` (needs nothing);
`--json` gives machine-readable output.

The migration chain is the generated baseline (M7.7, DEC-138) plus additive numbered files after it
(`00002_suppression_address_hash.sql` is the M7.4 R4-S expand)
(`00003_consent_erasure_scrub.sql` lets the erasure blank a deleted owner's consent evidence, R4-2d1)
(`00004_ages_note_erasure_scrub.sql` lets the erasure blank the erased subject's contact-request ages note, R4-T07A)
(`00005_email_address_optouts.sql` adds the address-level optional-email opt-out table, R4-2d2-1,
owner decision 8; R4-2d2-2a through -3 activate its local behavior, inventory 7.6 and 15.8)
(`00006_admin_operation_attempts.sql` originally staged receipt storage in T07B-1a: a 16-byte nonce and
timestamp only. T07B-1b now uses it locally and runs the 24-hour cleanup; inventory T39 and 15.9 record the staged behavior)
(`00007_reason_review_erasure_scrub.sql` adds a dormant reason-blank branch to both `contact_requests` trigger
functions, T07B-2a: non-contact, non-pending rows with a soft-deleted party only, ages-note branches intact,
no caller; local, verification and landing pending, inventory 15.4).
`TestBaselineFileShape` checks the chain
through `migrationFileShapeProblems` (baseline first, then consecutive `NNNNN_name.sql`, each with Up
and Down). `scripts/baseline-proof.sh generate`
made the baseline from the frozen pre-squash chain (commit `be61e582c3011263307e4db4497056f7fd70bfd9`, 63
files through `00064`) and `scripts/baseline-proof.sh proof` re-proves a tree against that chain
with a normalized `pg_dump` diff, on a scratch server only (`--help` lists the arguments). It refuses
a tree with more than the baseline (`scripts/baseline-proof.sh:1255-1258`), so `--candidate-tree` must
point at an export holding only `migrations/00001_baseline.sql`, such as `git archive 2aea60a`; the
script is not edited for numbered files. Before
it changes anything it proves that `--server-url` (the Go driver) and `--client-url` (psql and
`pg_dump`) reach the same cluster, database and role, clears every `PG*` variable, and requires a
free `--boot-port`. The
baseline's Down destroys the schema and refuses while any user exists; it is for scratch databases
and the down/up test, never application rollback. A development database migrated by the old chain
stores versions up to 64: the unchanged runner treats it as current and does not refuse it, so its
owner must recreate it before running a new binary on it. There is no converter
(`docs/plan/briefs/audit-2026-11/R9-baseline-squash.md` section 7.7).

### Before every push: quick sanity checks

Grier's rule (Oct 22, 2026). Run these before each push. They take seconds,
not the full suite, and catch undefined references, type errors and missing
fields in test fixtures before CI does:

```bash
go build ./...
go vet ./...
make lint-go                       # go vet + gofmt + staticcheck v0.8.2
(cd web && npx tsc --noEmit)       # types, incl. fixture objects missing a field
(cd web && npx knip && npx knip --production)  # web lint (make lint-web; the second pass counts production code only, so test-only exports need a `@public` reason); web/ has no eslint config
```

CI runs on pull requests and on pushes to `master` only, so a branch push
alone starts no CI; open the PR to get the checks.

### Merging pull requests

Grier's rule (Oct 20, 2026; generalized Oct 30, 2026). An agent may merge a fold PR, as a normal merge commit, when all of these hold:

1. Every CI check is green (`test`, `web`, `provision`; branch protection requires them) on a head
   that contains master's tip. GitHub refuses a PR that is behind master (DEC-146); merge master
   into it (never rebase), wait for CI again, and then merge.
2. All four primary concerns were reviewed by a separate agent from the one that implemented the change, preferably running a different model (for example, Sonnet reviewing Opus's work):
   - UI, UX and design;
   - database and performance: simplicity and efficiency, to keep out complexity that would only get cut later;
   - security;
   - correctness, including the backend and API.

   One reviewer may cover several concerns. A concern the change doesn't touch still gets an explicit "not affected" from the reviewer. Keep the findings with the PR (its description or a comment), and resolve blocking findings before merging.
3. No product concern needs a human. No Grier decision on the PR is still open, and it makes no new product decision. Changes to security or visibility policy, repo settings, or production and deploy are product concerns unless Grier already decided that change. A check that only a person can run on real devices or live services (phones, mail providers, TLS on a real domain, SMS) doesn't block a merge: build it best effort, test what an agent can, and add the check to `docs/FIRST-CHURCH-CHECKS.md` in the same PR (DEC-128).

After a merge, merge master into the other open branches (never rebase) and report the merge SHA.

## Deployment

- `docker compose pull && docker compose up -d` → app + Postgres 16 + Caddy (auto HTTPS). The app
  is the published `ghcr.io/grierj/fold:${FOLD_VERSION}` (C10, `release.yml`); `FOLD_VERSION` is an
  exact release in `.env`, never `latest`. Developers (and anyone before the first release) add the
  override: `docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build`. Fill
  `.env` from `.env.example` first. Named volumes: `pgdata`, `uploads`. Only a live
  `docker compose up` checks that Caddy, TLS and the volumes work together.
- The compose subnet `172.30.0.0/24` is the default `FOLD_TRUSTED_PROXIES`; change both together.
- Dockerfile is multi-stage (node → go → distroless), `CGO_ENABLED=0`; no C compiler.
- Every `APP_ENV` refuses to boot without `DATABASE_URL`. Migrations run on start under a Postgres
  advisory lock; a failed one exits non-zero.
- First boot with an empty DB serves the setup wizard at `/setup`; after any user exists, the
  process caches that until restart.
- `GET /healthz` pings the database. Compose uses `/fold healthcheck` (distroless has no shell).
- Production is one VPS with this stack (`docs/DEPLOY-DIGITALOCEAN.md`,
  `deploy/digitalocean/provision.sh`; run `--dry-run` before a real run). The script never calls
  `doctl` and never creates droplets, firewalls, snapshots, backup plans or buckets; the church
  owns the droplet, DNS and email account. `smoke.sh` rechecks health, TLS and that Postgres and
  the app are not published; `upgrade.sh` pins an image tag or commit, dumps first, and can restore.
- Recovery refusal is staged locally (M7.4 R4-RP, not integrated or published). The level interface
  is integer `recoveryPolicy` from `/fold version --json` in an image, or the one-line
  `deploy/recovery-policy` at a source commit. Absent metadata means 0; malformed metadata refuses.
  Scripted provision, forward upgrade and rollback refuse a target below the greater of
  `DIR/recovery-policy-floor` and the level of the locally pinned image. The floor file is written
  only after smoke passes and never decreases; a higher pin enforces refusal even after failed smoke.
  There is no override. Restore data with the current release instead of starting a lower-level one.
  Run installed `DIR/upgrade.sh`: it installs its own tool set in `DIR/deploy-tools`, re-execs there,
  checks the target before backup or checkout, and runs that copy's provisioner. An older checkout's
  script run by hand can bypass these checks. Installed upgrades keep their frozen tools; install
  newer tools from a newer trusted checkout, subject to the `deploy-tools-version` check.
  Mutating provision and upgrade runs share `DIR/ops.lock` through inherited fd 9; a second operation
  exits 75 naming the holder from informational `ops.owner`. Never delete, rename or replace the lock.
  Never publish `recoveryPolicy >= 1` before R4-6c-R's current-release restore tooling and operator
  procedure land and are verified. Local refusal drills do not satisfy that release gate or the
  real-droplet checks in `docs/FIRST-CHURCH-CHECKS.md`. The T07B-1b tree is itself level 1
  (`const recoveryPolicyLevel` in `restore_fence.go` equals `deploy/recovery-policy`, checked by
  `TestRecoveryPolicyFileMatchesConstant`; `fold version --json` reports it), so a release cut from it is
  unpublishable until that tooling lands. Its deployment-level drill (`deploy-drill.sh`, the level-0 rollback
  and forward refusals) is pending.
- Restore (staged locally, R4-6c-R tooling at `a0969a8` and R4-6c-V notes in `docs/DEPLOY-DIGITALOCEAN.md`;
  unverified on a real droplet, not published): `restore.sh --dump` restores into a fresh database swapped in
  whole and keeps the public site closed and background deliveries paused in a worker-free private recovery instance until `--reopen`. It
  never restores into the live `fold` database. `backup.sh` and its expiry refuse with exit 75 while
  `DIR/recovery.lock` exists (a separate `DIR/backup.lock` coordinates them with the lock's publication). Never
  delete, rename or replace `recovery.lock`, `ops.lock` or `backup.lock` to get past a refusal. A plain `--dump`
  exits 0 with the site closed; `--reopen-if-none` (used by `upgrade.sh --rollback`) exits 3 unless the count is
  exactly zero. `--abort` works only before the swap; only `--abort` finishes an interrupted abort;
  `--resume` after an interrupted reopen closes the site and returns to private recovery; `reconcile-needed`
  writes nothing and is only rechecked. A Droplet Backup restore is outside this protection. Docs call the
  procedure "recommended" and makes no support promise; it does not restore uploads. Full drills at the exact head, the
  real level-1 crossings, review and landing are pending.

### Full restore drill frequency

Run the full `deploy/digitalocean/restore-drill.sh` only when at least one of these
conditions holds: changes have a high chance of altering the restore behavior the
drill checks; work specifically changed the drill; or more than 30 days have passed
since its last run. Record the reason and the run date with the evidence. For other
changes, use targeted checks. A recent run does not by itself satisfy a release gate:
assess whether its candidate and evidence cover the changes being released.

## SMS (messaging Phase 3, not yet)

Not built, and not required to run Fold. When you build it: provider adapters (Telnyx default,
Twilio next) fed by `internal/notify` deliveries (PLAN 5.12, 5.6), broadcast-only v1, replies route
to the group leader. Don't start early.

## Consistency

Similar things behave the same way (Grier, Oct 2026). When one path gets a
rule (a preview, a confirmation, an audit row, a notice), give its siblings
the same rule with the same API shape and UI component, or write down why
not. Example: group and single-event tightening share `visibility-impact`,
`cancelLosers`, and `GroupVisibilityReview`. In the UI, one shared component
per kind of control: `components/ui/Field` and its rows (every form field,
DEC-65), `ZonePicker` (every time zone field but setup), `VisibilityField`
/ `EventVisibilityControl` (every event visibility choice), and
`SeriesEditor` (Edit series / Edit event on the group and event pages).

Errors a person can't act on, or that are a security risk, never reach them
(Grier, Oct 15, 2026, DEC-52). Rate limits and anything that could reveal
whether an account exists answer with the form's usual success message and
are logged server-side. Exceptions: sign-in keeps its lockout message, and a
signed-in leader's invitation limit keeps its 429.

Exception (Grier, Oct 14, 2026): one-off pages outside the standard
presentation layer, such as the server-rendered first-run setup page
(`templates/setup.html`), don't have to match the React components.

## Rules and what enforces them

Add a row when Grier corrects a repeat mistake; drop it once the mistake
can't happen. Prefer structure, then types, then a check, then a test.

| Rule | Enforced by |
|------|-------------|
| Every list of event dates hides events the viewer can't see | `dateCandidates` loads facts without granting series rights; `visibleSet.canSeeDate` filters before limits; `TestNarrowEventHiddenFromGroupLists` |
| Event and notice output never shows a hidden group's name, place or id | `a.groupShownTo`, `TestGroupDetailsGoThroughGroupShownTo` (an exception line ends with `// group-ok: <reason>`) |
| Making a group or one event more private previews the cancellations and needs confirmation | `visibility-impact` for both, a 409 without `confirm`; `TestEventMorePrivateCancelsShutOutRegistrants`, `e2e/event-visibility.spec.ts` |
| Making an event more private cancels and tells people it shuts out, whether the group or the event changed | `cancelLosers` in both paths; `TestGroupMorePrivateTightensAndCancels`, `TestEventMorePrivateCancelsShutOutRegistrants` |
| Notification text has no email address or phone number; inline actions are allow-listed and account-request eligibility is checked on the server | Registry/privacy tests in `internal/notify`; account-request handler tests |
| Notification rows are private to their owner and inline actions are checked on the server | `notify` queries filter by `user_id`; `TestStoredNotificationsAcceptance` |
| Notices use `notify.deliver`; reconstructible direct mail uses ID-based jobs; synchronous mail is limited to the documented methods and composer test | `TestNoticesAreDeliveredBySendNotByCallers`, `TestMailGoesThroughJobs`, `TestNoticeGuardCatchesPostCommitDelivery`, `TestMailGuardCatchesBypasses`, `TestMailGuardScopesSynchronousExceptions`, `TestMailGuardAllowsDeliveryAndReadOnlyOutbox` |
| Optional email honors the account's choice and the destination address's retained opt-out through one function | `readEmailOff`; `TestEmailChoiceSQLStaysInOneHelper`, `TestEmailChoiceGuardCatchesBypasses`, `TestReadEmailOffCombinesAccountAndDestination`, `TestEmailSettingsHideChoiceSource`, `TestReusedAddressKeepsErasedChoicesUntilHolderTurnsOn` |
| Every optional or broadcast email checks the effective choice at its send boundary | `TestOptionalMailChecksTheEffectiveChoice`, `TestOptionalMailGuardCatchesBypasses`, `TestBatchRechecksTheChoiceAtEachRow`, `TestTestSendHonorsTheEffectiveChoice`, `TestTestSendInFlightWindow` (parse-only guard checks named calls and lexical order; behavioral tests cover application of the result and the permitted in-flight window) |
| Suppression SQL lives only in `mail.OpenSuppressionReasons`, the admin helpers and the webhook writer, and matches by Go-computed hash | `TestSuppressionSQLStaysInItsHelpers`, `TestSuppressionSQLGuardCatchesBypasses` (parse-only; it does not see table names built from non-literal pieces, reflection, SQL sent around GORM, dot-imports of `fold/models` or cross-package helpers) |
| Suppression matching assumes a UTF8 database with a deterministic default collation | `TestSuppressionMatchingBasis` (`migrations/migrations_test.go`) |
| Throttle keys come from the typed builders in `throttle_keys.go`, and erasure deletes every owned key | `TestThrottleKeysComeFromBuilders`, `TestThrottleSQLStaysInItsHelpers`, `TestOwnedThrottleKeysCoverEveryOwnedBuilder`, `TestThrottleKeyStringsAreUnchanged`, `TestAccountErasureDeletesOnlyOwnedThrottleKeys` (parse-only; it does not see keys built inside SQL text in a listed helper, SQL sent around GORM, reflection, generics or code outside the root package, and it matches identifiers by name) |
| Contact-request text is written only at the two creation sites, the ages note is blanked only by `eraseContactRequestTextIn`, and nothing bypasses the two request triggers | `TestContactRequestTextChangesStayInTheirHelpers`, `TestContactRequestTextGuardCatchesBypasses`, `TestContactRequestTextGuardReadsTheRealTree` (`guards_contact_text_test.go`); `TestAgesNoteMigrationFileHasNoTriggerBypass` (`migrations/migrations_test.go`). Parse-only: it binds names lexically within one file and does not see SQL built from non-literal pieces, constants from other files, reflection, generics, type aliases, values that arrive from another function, a parameter or a field, or SQL sent around GORM. It rejects writes whose destination is implied rather than named (`Create`, `Save`, `Updates` and the like), `OnConflict` with `UpdateAll`, raw `UPDATE`, `INSERT ... ON CONFLICT` and `MERGE` that set either column, every row deletion (R-7), and any `eraseContactRequestTextIn` call outside exactly two sites (R-6): one in `apiAdminDeleteUser` (`api_admin.go`) after `tx.Delete(u)`, and one in `eraseTombstoneOnce` (`account_erasure_tombstones.go`) after direct `found, err := lockTombstoneIn(tx, id)` and checks returning `err` or `errTombstoneNoop` when absent. Each scrub call must sit directly in its own callback body and pass that callback's own `tx`. Each callback must be the last argument of `a.contactMutationCtx` on the containing method's own App receiver, with the mutation call in the method body (a `for` loop is allowed, a func literal is not). The old `a.contactMutation` form, nested transactions, shadowed bindings, calls in page or job handlers and a third site fail. Its scoped H8: no `DISABLE`, `ENABLE REPLICA` or `ENABLE ALWAYS TRIGGER`, `DROP TRIGGER`, `CREATE OR REPLACE FUNCTION` for either request function or `session_replication_role` in the ages-note migration or a production Go literal, with one allowance, the `SET LOCAL session_replication_role = replica;` literal in `resetStatements` of `internal/testdb/testdb.go`, plus a check on the real tree that no non-test file outside `internal/testdb` imports it (plain, aliased, blank or dot); `set_config` for `statement_timeout` stays allowed |
| An unknown COMMIT result is never retried and never answered with "nothing was deleted" copy; an unacknowledged ROLLBACK is never retried (no further BEGIN) but, because COMMIT was never sent, still answers its pre-COMMIT code (409 `delete_household_busy` after a 55P03); only a 55P03 whose ROLLBACK was acknowledged retries | `classifyContactErr` and `rollbackConfirmed` (`contact_lifecycle.go`); `TestContactMutationCtx*`, `TestAdminDeleteCommitPhaseAnswersUnknown`, `TestAdminDeleteHouseholdBusyThenCommitUnknown`, `TestAdminDeleteHouseholdBusyRollbackNotAcknowledged`, `TestHouseholdRetryOnlyRepeatsAConfirmedBusy`, the pgproxy tests; `TestWebErrorCopyCoversEveryCode` |
| The erasure takes `households` rows only in `lockErasureHouseholdsIn` (`NOWAIT`, ascending), before its first write | `TestErasureHouseholdLockCensus` (parse-only: it pins the set of `lockHousehold` caller names, the absence of a `households ... FOR UPDATE` literal outside `account_erasure_households.go`, and the position of three calls in the deletion callback; it does not check the census's classifications or lock orders, does not see SQL sent around GORM or a lock taken inside a helper it does not parse, and does not prove deadlock freedom, which inventory 9.6.1 says it does not have) |
| `web/src/types.ts` matches the Go JSON structs field for field | `TestWebTypesMatchGoJSON` (add a pair to `tsContracts` for each new response type) |
| Every state-changing route goes through CSRF, except the exact list in `csrfExemptRoutes` | `TestCSRFExemptRoutesAreExact` |
| Logout leaves nothing on screen or in cache | `no-store` on API and shell; `TestAPIResponsesAreNoStore`, `TestLogoutEndsSession`, `web/e2e/session.spec.ts` |
| Public forms never show a rate limit or reveal an account (DEC-52); only sign-in and a leader's invitation limit answer 429 | `TestOnlyLoginAndInvitesAnswer429` (`guards_api_test.go`); `TestGuestExistingEmailHintIsRateLimited`, `TestPasswordResetSkipsGuests` |
| Colors come from theme tokens: no hex outside `@theme`, no Tailwind palette or scale-step classes (DEC-60) | `TestColorsComeFromThemeTokens`; a line that must keep a hex says why with `color-ok:` |
| Pages never show the server's error text; every API error code has wording | `TestPagesNeverShowServerErrorText`, `TestWebErrorCopyCoversEveryCode`, `TestDeclaredAPIErrorsKeepTheirAnswers`, `TestDeclaredMessagesAreWrittenOnce`; `web/e2e/friendly-errors.spec.ts` |
| Main pages (including event, scheduling, Join and My groups) have no moderate, serious or critical axe issues at 390 and 1280 px; every field is labelled | `web/e2e/a11y.spec.ts` over `web/e2e/ui-pages.ts`; setup page in `web/e2e/seed.setup.ts` |
| Tests sit next to the code they test; no milestone-named test files | `TestTestFilesSitNextToTheirSource` (its error names the file to move to) |
| Source guards read the whole production tree, not a directory glob | `guardkit.ReadProduction` and its skip rules (`internal/guardkit/guardkit_test.go`: `TestReadProductionSkipRules`, `TestParseReportsUnparsableFilesAndKeepsGoing`), `TestContactRequestTextGuardReadsTheRealTree`; a guard about one package says so with `Parsed.Package` |
| Schema changes are new goose files; GORM models match them | `migrations/migrations_test.go` drift check |
| A contract migration's expand shipped in an earlier tagged, published release (PLAN 14.1) | Tag and file half: `scripts/contract-gate.sh` in the test job (`-- contracts: NNNNN_name.sql` header; a `v*` tag merged into HEAD holds the expand and not the contract; the expand unchanged since; full-history checkout), kept in place by `TestCIRunsTheContractGate` and proved by `TestContractGateScript`. Publication half is review only: a `release.yml` run of shape N or F, the finalize step summary, GHCR digest, release section and attestation naming one digest; a tag alone does not satisfy it |
| `go vet` and `gofmt` are clean | `make lint` in CI |
| No Python: tooling is shell or Go | `TestNoPython` |
| Docs say "recommended", never "supported" | `TestDocsSayRecommendedNotSupported`; a line that must quote the word ends with `wording-ok:` |
| One Node version: `.nvmrc` | CI reads it (`node-version-file`); `TestNodeVersionComesFromNvmrc` checks the Dockerfile and docs |
| A build never deletes the committed `web/dist/README.txt` | `keep-dist-readme` plugin in `web/vite.config.ts` |
| e2e specs switch users safely | `signIn` and `signInTo` in `web/e2e/helpers.ts` clear cookies first; `signInTo` leaves the app before its API login so no page swaps the CSRF cookie |
| Seat counts never overrun under concurrent registration | `TestConcurrentGoingRespectsCapacity` (`api_registrations_test.go`): 50 requests race for 10 seats |
| Token contrast: text 4.5:1, control edges 3:1 | `npm run contrast` (`web/scripts/token-contrast.mjs`, in CI) |
| No sideways scroll, no clipped select, no target under 24px (hard gates); kinds of control under 44px at 390 only go down | `web/e2e/layout.spec.ts` with `web/e2e/ui-baseline.json` |
| No new raw controls, `!` overrides, misused danger color, literal `style=` or date formatting outside `lib/when.ts` | `TestUIRatchets` (counts in `ui-ratchet.json`; lower them when a page gets better), `TestUIRatchetsCatchPastMistakes` |
| Selected chips are stronger than unselected; fields are labelled and their errors are announced | `Chip` and `Field` in `web/src/components/ui`, `Chip.test.tsx`, `Field.test.tsx` |
| React forms use the shared `Form`: no browser bubbles, persistent errors under fields, first invalid field focused; secret whitespace is never trimmed | `TestReactFormsUseCustomValidation`, `TestReactFormValidationGuard`, `components/ui/Form.test.tsx` |
| A photo-less group's cover comes from `coverSrc` (its own image, else one of six patterns by id, DEC-66); empty states are the neutral `EmptyState`, never red | `web/src/lib/covers.test.ts`, `EmptyState.test.tsx`; the danger ratchet in `TestUIRatchets` |
| Work reaches master only through a pull request with the `test`, `web` and `provision` checks green on a head that contains master's tip; branch pushes use an exact refspec (`HEAD:refs/heads/<branch>`) | GitHub ruleset on `master`: PR required, those checks required and strict (up to date before merging, DEC-146), no bypass actors (admins included), no force-push or deletion; `scripts/merge-rules-check.sh` in the test job fails when the live rules stop saying so (it cannot see bypass actors), kept in place by `TestCIRunsTheMergeRulesCheck` and proved by `TestMergeRulesCheckScript` |
| Two PRs never land the same migration number | Prevention: the strict ruleset above. Detection: `TestBaselineFileShape` (duplicate or gap) on every PR and master push; `TestDuplicateVersionNeverBoots` proves a duplicated tree cannot boot, migrate or pass the release smoke. Recovery: the Migrations convention above |
| The entry JS chunk stays under its gzip budget; libraries few pages need are loaded with `import()` | `web/scripts/bundle-size.mjs` (`npm run bundle-size`, in CI after the build) |
| Secrets in URLs (calendar and room feed tokens, guest manage links, unsubscribe and email settings links, `?token=` email links) never reach the access log | `httpx.AccessLog` and `redactLogPath` (`internal/httpx/accesslog.go`); `TestAccessLogKeepsSecretsOut` |
| Push payloads never carry contact details, signed links or a hidden group's name; endpoints are never logged | `TestEveryKindsPushTextIsLockScreenSafe` (`internal/notify`), `TestPushLogsKeepSecretsOut` |
| The restore barrier's boot step runs in `serve` after migrations and before anything serves, and a failure exits non-zero | `TestRestoreFenceBoot` (`restore_fence_test.go`); the call's position in `serve` is checked by review |
| Every fenced transaction takes the restore ticket first, and the floor is raised only on an acknowledged COMMIT before the turn is released; only the boot step and the ticket write `restore_fence` | `TestFencedTransactionsTakeTheTicketFirst`, `TestFencedTransactionsGuardCatchesBypasses` (parse-only, over `apiAdminDeleteUser` and the settle route), `TestRestoreFenceHasTwoWriters`, `TestSettingsWritersRefuseTheFenceKey`, `TestRestoreFenceDelayedAcknowledgment`, `TestRestoreFenceNoFalseDetection`, `TestRestoreFenceSameLifetime`; `TestRestoreFenceUndetectedCases` pins the stated detection limit |
| Attempt ids are minted and checked only by the process that holds the key, run at most once, and are in the ledger before their age is judged; `attempt_expired` means only that the submission ran nothing; `not_applied` is answered only from memory | `TestAttemptIDCapability`, `TestAttemptLedgerRules`, `TestAttemptLifecycle`, `TestDefinitiveResponsesEndTheAttempt`, `TestOutcomeClassification`, `TestLedgerMutexHoldsNoWait`, `TestSettleAuthorization`, `TestSettleUnknownCommit`, `TestRestoreFenceFencedIdAcrossARestart` |
| The signed-in admin is decided on their own row `FOR SHARE NOWAIT` as the last locking statement before COMMIT | `TestActorSealIsLast`, `TestCurrentAdminInFacts`, `TestActorSealBusy`, `TestActorSealVsConversion` |
| A session is "still current" by `sessionCurrent` alone; no code compares a row's session or logout version by hand | `TestSessionEpochsAreComparedOnlyBySessionCurrent` (parse-only; `==`/`!=` with a `.SessionVersion` or `.LogoutVersion` operand in the root package, outside `session_state.go` and the signed-link binding in `api_guardian_email.go`), `TestSessionCurrentPredicate`, `TestLockActorInRefusesEveryStaleSession` |
| The recovery level the binary reports equals `deploy/recovery-policy`, and R4-RP's tooling refuses a lower level on every scripted path; no release at level 1 or above before R4-6c-R's tooling and procedure land | `TestRecoveryPolicyFileMatchesConstant`, `TestVersionJSONReportsRecoveryPolicy`; the tooling refusal is R4-RP's; the publication gate is review only |
| The test-only attempt clock exists only in a binary built with `-tags e2eclock` and run with `FOLD_E2E_CLOCK=1` | The build tag and `attempt_clock_e2e.go`'s admin-and-CSRF route; that no shipped build sets the tag is review only (no test) |
| QC and deploy scripts never grep application source; what the QC-1 harness takes from the application (limits, statements, selectors, labels, copy) is `deploy/digitalocean/qc1/app-facts.json` | `TestQC1AppFacts`, `TestQC1PlanMarkersAreWhatTheApplicationSends`, `TestQC1LoaderColumnsFitTheSchema`, `TestSchedulerRunsWithoutWorkers` (Go, beside the code) and `web/src/components/ReasonReviewSection.test.tsx` (Vitest, the rendered DOM); a refactor that changes a fact fails there, and `UPDATE_QC1_FACTS=1 go test -run TestQC1AppFacts .` regenerates the `go` section for review |
| Checks an agent can't run are listed for the first church | Review only (no test): the PR adds its checks to `docs/FIRST-CHURCH-CHECKS.md` and lists them in its description |
