# AGENTS.md: working instructions for AI agents on this repo

Working title: **Fold**. Church small-group / ministry / activity management: the
group-management slice of ChurchTrac, without the rest of it. `docs/plan/PLAN.md` is the source of
truth for product decisions and the data model; when it disagrees with older repo docs (including
this file), follow the plan. Progress lives in `STATUS.md`, never here.

## The one rule

**Deployment simplicity is the top concern.** Whatever you build, a church volunteer (or a single
SaaS operator) must be able to run it with `docker compose up`. The shipped artifact is always a
**single Go binary** that serves both the JSON API and the frontend (the SPA bundle is embedded via
`go:embed`). No separate frontend hosting. Ever.

## Read before you touch

| Area | Read |
|---|---|
| Account deletion, tombstones, `contact_requests` text, households in erasure, restore fence, attempt ids | `docs/agents/erasure.md` |
| Anything that sends email, email choices, suppressions | `docs/agents/mail.md` |
| Web push, devices, the service worker | `docs/agents/push.md` |
| A file in `migrations/` | `docs/agents/migrations.md` |
| Dockerfile, compose, `deploy/`, releases, restore | `docs/agents/deploy.md` |
| Test setup, source guards, the e2e harness | `docs/agents/testing.md` |
| UI look and layout | `design.md` (tokens, composition classes) |
| Proving a change in the real app | `.cursor/skills/verify-fold`, `.cursor/skills/verify-ui` |

## Stack

- **Go**: language minimum and toolchain pin come from `go.mod`. Gin (JSON API under `/api/v1`), GORM.
- **PostgreSQL only**, everywhere: production, development, tests (`DATABASE_URL`, DEC-1, DEC-24).
  Use Postgres features freely. `CGO_ENABLED=0`; keep it that way.
- Schema: versioned **goose** SQL files in `migrations/`, embedded in the binary and run on start
  under an advisory lock. They are the source of truth; GORM models must match them.
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
  break plain-HTTP dev. Every POST/PUT/DELETE needs the `csrf_token` form field or
  `X-CSRF-Token`; the only exemptions are `csrfExemptRoutes`.
- Approval workflow is a core feature: new groups enter `pending` until staff approves. Keep it
  working through every refactor.
- SMS is not built and not required (messaging Phase 3). Don't start it early.

## Data model

Full model: PLAN 5.3 and `models/models.go`. Chain: `User` → `Membership` → `Group` → `Event` →
`EventOccurrence`; `Registration` links a user to one occurrence. A church-wide event has
`events.group_id` null and is run by its `EventHost` rows. Invariants agents trip over:

- The wall clock (`starts_local` + IANA `tzid`) is the truth. Occurrences store UTC, generated 18
  months ahead. Rules repeat at most once a day, under a date cap (`events.Expand`, DEC-89).
  `internal/events` is the pure clock (no database); `internal/eventdates` writes the rows
  (`Ensure` at creation, `Extend` daily under its advisory lock).
- An event's `visibility` is its own value, copied from the group at creation (DEC-48). A nullable
  occurrence `visibility_override` wins for that date (`policy.EffectiveVisibility`); date
  candidates never authorize the series. Making a group or one event more private previews the
  cancellations, needs `confirm`, and cancels and tells the people it shuts out (`cancelLosers`,
  `visibility-impact`, `api_group_visibility.go`).
- `cancel_source` is `series` (a series edit dropped the date; a later edit that includes it
  schedules it again and tells registrants, DEC-91) or `manual` (a person).
- Render any name with `User.PublicName()`, never the email. Sign-in needs `User.CanSignIn()`;
  login throttling is in Postgres (`login_throttles`, `throttle.go`). `Email` is the login (DEC-23).
- Deleting an account goes through one erasure pipeline, `eraseAccountIn`. Read
  `docs/agents/erasure.md` first.
- Focal points (`AvatarFocalX/Y`, `CoverFocalX/Y`) are stored 0-100 and rendered as CSS
  `object-position`. `AvatarKey` is a blob-store key, not a URL.

## Conventions: where the one helper lives

Each line names the one place a rule is decided. Use it; never write a second copy.

**API and errors**
- API: `/api/v1/...`, camelCase JSON. Authenticated endpoints return 401 JSON, never a redirect
  (only `/setup` redirects). Errors are `{"error": "human readable"}` with the right status.
- Errors (DEC-61): an error a person can act on is an `*apiError` declared once at package level,
  `var errX = apiErr(http.StatusConflict, "x", "...")` (`api_error_values.go` for the ones with no
  better home), plus a line in `ERROR_COPY`. Domain code returns it, wraps with `%w` or compares
  with `errors.Is`; a handler answers any error with `fail(c, err)` (anything else is a logged 500
  with no detail). Two answers for one refusal are two values (`apiErrWith`); a body that carries
  data is an error with an `apiAnswer()` method; a run-time message uses `apiErrf`; `jsonError` is
  only for a message with no code. DEC-52 decides what is never an error.
- Response shapes: a response with its own shape (envelopes included) is a named Go struct in
  `api_response_shapes.go` and an `export interface` in `web/src/types.ts`, paired in
  `tsContracts` (`guards_web_test.go`).

**Web client**
- `web/src/api/core.ts` owns every request (`api`, `apiUpload`, `apiCSV`, `apiIfChanged`), so
  CSRF, the 401 sign-out hand-off (`configureApi`, called once in `main.tsx` before the first render)
  and the unreadable-body flag behave the same for all. One module
  per resource beside it, paths built with the `path` tag and queries with `query({...})`, ids
  typed `Id`. A request that starts or ends a session passes `rotatesCsrf`; one that may be
  anonymous passes `anonymousOK`.
- A page that loads one resource uses `lib/useLoad.ts` (`reload` keeps what is shown, `retry` fails
  closed, `refreshOnFocus`, `epoch()`).

**UI**
- Strict CSP (DEC-37): no inline `<script>` or `style=""` in server templates, no `eval`, no CDN
  or third-party scripts, styles or fonts. React `style={{...}}` props are fine.
- Styling (DEC-60): role tokens and shared classes in `web/src/index.css` (`bg-primary`,
  `text-text-muted`, `.btn-primary`, `.input`, `.chip`); pick the role by meaning, not by color.
  No hex outside `@theme` (a line that must keep one says why with `color-ok:`).
- Controls (DEC-60–DEC-66): use `components/ui` (`Button`, `Chip`, `Form`, `Field` with
  `Input`/`Textarea`/`Select`, `EmptyState`); `TestUIRatchets` stops new raw ones. One primary
  action per view; `danger-outline` beside a main action, solid `danger` only when destruction is
  the point of the dialog; empty states say what would appear and offer the next step, never in
  red; status is never color alone; copy is plain, warm, second person. Dates are formatted only
  in `lib/when.ts`. Reference: Meetup, then ChurchTrac; familiar beats original.
- Prove a UI change with `.cursor/skills/verify-ui` and look at the 390px screenshots.
- Forms: state every input requirement next to the field. Every path that sets a password checks
  it with `checkNewPassword` (`password_rules.go`; 10 characters minimum, 72 bytes maximum,
  DEC-90). On a failed submission, keep valid fields; never echo back passwords or payment data.

**Accounts, sessions, settings**
- Capabilities live in `tokens.go` only (lock order contact gate → user → token); every rate limit
  in `throttle.go`; throttle keys come from the typed builders in `throttle_keys.go`; advisory lock
  ids are the one block in `advisory_locks.go`.
- Account status changes only through `setAccountStatusIn` (`account_status.go`); a new one starts
  in `newAccountStatus`. Profile fields are validated by `fields.go`. A pending item is decided
  with `decidePendingIn`.
- "This session is still current" is `sessionCurrent(fresh, session)` (`session_state.go`) alone.
  A writer that rechecks its actor under a lock calls `lockActorIn(tx, me, mode)` (or
  `lockAdminIn`) with `actorRead`, `actorShare` or `actorNoKeyUpdate`; one holding the actor in a
  batch of locked rows calls `sessionCurrent`/`adminCurrent`. `errSecuritySession` is the 401
  (`fail(c, errSecuritySession)`, or `answerStaleSession`). Never compare `SessionVersion`/`LogoutVersion` or `CanSignIn` by hand. Ending
  every session is `endAllSessionsIn`; a bump alone is `sessionVersionBump()`. The cookie's keys
  are the `session*Key` constants and `setSessionLogin`/`clearSessionLogin`.
- Settings keys are constants in `settings.go`; read with `settingIn`/`boolSettingIn`/`siteNameIn`,
  write only with `setSettingIn`. Keys derived from `SESSION_SECRET` come from `deriveKey`
  (`keys.go`): a new use gets a new `purpose:vN`, and an existing purpose's bytes never change.
- Small shared helpers: SQLSTATE checks use `pgCode`/`pgConstraint` (`pgerrors.go`); long id
  lists go through `eachChunk` (`chunks.go`); several users are locked with `lockUsersByID`
  (`user_locks.go`). Enum values are owned by `internal/policy` and aliased by `models`;
  `models.CurrentRegStatuses` and `models.MembershipActive` name the common status sets.
- Time zones (`zones.go`): the church's zone is `churchZoneIn(tx)` (errors on a failed read or a
  bad value), used for age decisions and sweeps. `churchZoneLenient` falls back to UTC on a bad
  value but errors on a failed read (job queue, guest mail); `churchZoneOrUTC` also shows UTC on a
  failed read (pages, emails, notices, quiet hours). A person's zone is `userZone`; an event's is
  `eventZone`. Household ages count from `churchClockIn(tx, now)`; take `now` once per
  transaction. Everything parses with `events.LoadZone`, which refuses "Local".

**Permissions and visibility**
- Every check is a pure function in `internal/policy` (PLAN section 6) with table tests. Handlers
  load facts and call it; never inline role checks or read `Group.LeaderID` (display only).
  Leadership is `Membership.Role = leader`; a group can have several leaders.
- Facts come from `policy_facts.go` (`asPerson`, `asGroup`, `membershipRel`, `groupsByID`,
  `groupRels`, `userRels`, `hostedEvents`, `loadFactSet`, `eventFactsIn`) and `group_members.go`
  (`memberCount`, `leaderCount`). They return errors: a failed read is never "not visible" or
  "no member". Host rows count only on church-wide events (DEC-7).
- Visibility is `visibleTo` in `internal/policy`; its SQL forms (`directoryQuery`,
  `losersForDate`) are kept equal by `TestDirectoryMatchesPolicy` and `TestLosersMatchPolicy`.
  Event and notice output never shows a hidden group's name, place or id: go through
  `a.groupShownTo`.
- Membership rules live in `internal/policy` (`CanRemoveMember`, `CanBeSuccessor`, role changes).
  Removal keeps the row with `status=removed` (DEC-35); departure keeps `status=left` (DEC-86).
  Adding anyone goes through `changeRole`, which locks the group row and checks
  `policy.GroupHasRoom` (DEC-39) and `policy.CanAddToGroup`. The one exception is the sole-leader
  handoff in an account delete or disable (`handOffLeadership`, `api_handoff.go`, DEC-36). Leading
  a pending or archived group grants no power to add or invite (DEC-88;
  `policy.LeadsApprovedGroup`, `CanInviteIntoGroup`).

**Events, dates, rooms, registrations**
- Showing a date: `a.projectDate(viewer, vs, occ, invited)` (`date_projector.go`) decides
  everything about how one date looks to one viewer; every surface renders its `dateProjection`
  and decides nothing. `labelGroup` turns a shown group into fields. A new surface calls
  `projectDate`; `TestDateProjectorGoldens` pins every surface's bytes.
- Every list of event dates hides events the viewer can't see (`dateCandidates`,
  `visibleSet.canSeeDate` before limits).
- Rooms (`internal/rooms`): "the room is busy" is `rooms.HeldCondition` over `rooms.PaddedRange`;
  every busy count is `rooms.BusyCounts`. A date's place is `events.EffectivePlace` /
  `EffectivePlaceSQL`; group tags compare through `internal/grouptags`; a default-off switch read
  in SQL is `settingOnSQL`. Each SQL form has a test that it agrees with its Go form.
- Registrations: seat decisions go through `registration_seats.go` (`poolFits`, `seatChange`,
  `seatTally`; party size and childcare demand each have one SQL and one Go form,
  `TestSeatSQLMatchesGoRules`); rows are written only by `saveRegistration` and `promoteRegistration`
  (`registration_rows.go`); the access columns stay with `registration_access.go`; lock order is
  in `registration_locks.go`. A household person's holder is judged by
  `subjectFacts.householdProblem` (`listedRegistrations` only pre-filters). Dropped-answer audit keys
  are the `meta*` constants in `api_registrations.go`.
- Guest registration (C5): the rule is `policy.CanRegisterGuest`; never widen
  `CanRegisterEvent` to a nil person, and never load a guest row as an actor. A submit stores only a
  `guest_signups` row; the guest `User` and `Registration` exist only after the emailed link is
  redeemed. Public answers stay uniform; verification emails cap at 3/hour and 10/day per address,
  counted under a per-address advisory lock in the signup's transaction. `changeRole` refuses a
  guest a membership.
- Requests (contact, group add, date add) are one `contact_requests` table and one lifecycle. What
  a purpose decides is `requestKind` in `request_kinds.go` (add a purpose there, not in a
  `switch`); handlers share `request_lifecycle.go`; "may this viewer see the request's group or
  event" is `requestContexts.shown` alone.

**Jobs, notifications, audit**
- Background work goes through `internal/jobs` (PLAN 5.5). A job kind is a `jobs.Type` (name and
  `MaxAttempts`) registered as a `jobs.Kind`; queue with `<type>.Enqueue` or `<type>.Spec`. Give a
  `unique_key` when the work must happen once (`<kind>:<church date>` daily, `<kind>:<UTC hour>`
  hourly). The one kind list is `App.allJobKinds()`; shared payload keys are in `job_identity.go`.
  Only `jobs.Retry`, `jobs.Dismiss` and `jobs.LockDead` change a row's status. Handlers must be
  safe to run twice and return their error. **Never start a goroutine ticker for periodic work.**
- Notifications are stored rows (PLAN 5.12) written by `notify.Send(tx, notify.Msg{...})` in the
  same transaction as the change and its audit row; the `notify.deliver` job sends the email and
  push after commit, so callers never deliver a notice themselves. A fan-out takes the recipient
  lock once with `notify.WithRecipients` and passes its `Scope`. The kind registry never looks
  anything up, so the caller decides what the recipient may see (`userCanSeeGroupIn`,
  `noticeFacts.canSeeGroup`). Close tasks with `notify.Resolve` where the subject is decided (send
  first, resolve second). A repair that queues from an earlier selection goes through
  `notify.QueueDelivery`. The `internal/notify` package docs cover the lock and savepoint rules.
- Notice writers live in `notices_<domain>.go` and read people with the error-returning helpers in
  `notice_people.go` (`nameIn` is the lenient twin for pages); an event's runners come from
  `eventRunnerIDs`, role changes from `audit.ReadRoleMeta`. A failed read returns its error so the
  transaction rolls back instead of skipping a recipient. Notification text has no email address or
  phone number. Admin notices are stored notification rows written in the change's transaction.
- Record sensitive actions with `audit.Write` (`internal/audit`) in the same transaction as the
  change.

**Uploads**
- Avatars and covers go through `storage.Store` (`App.Uploads`), never raw paths. Keys are
  server-generated, never client filenames. Uploads are resized in the browser first
  (`web/src/lib/image.ts`); the server still sniffs (JPEG/PNG/GIF only), re-encodes (no EXIF or
  text chunk), caps at 5MB and serves at `/uploads/<key>` with `nosniff`. Avatar blobs are removed
  only by the `avatar.cleanup` job (`avatar_cleanup.go`), queued in the same transaction as the key
  change. A new `Store` (S3: implement it and make `serveUpload` redirect, PLAN 5.7) must meet the
  contract in `storage/storage.go`. `UPLOAD_DIR` is the local path.
- Only `FOLD_TRUSTED_PROXIES` may set `X-Forwarded-For`. Secrets in URLs never reach the access
  log (`redactLogPath`).

## Consistency

Similar things behave the same way (Grier). When one path gets a rule (a preview, a confirmation,
an audit row, a notice), give its siblings the same rule with the same API shape and UI component,
or write down why not. Example: group and single-event tightening share `visibility-impact`,
`cancelLosers` and `GroupVisibilityReview`. In the UI, one shared component per kind of control:
`components/ui/Field` and its rows (DEC-65), `ZonePicker` (every time zone field but setup),
`VisibilityField` / `EventVisibilityControl`, and `SeriesEditor`.

Errors a person can't act on, or that are a security risk, never reach them (Grier, DEC-52). Rate
limits and anything that could reveal whether an account exists answer with the form's usual
success message and are logged server-side. Only sign-in's lockout and a signed-in leader's
invitation limit answer 429.

One-off pages outside the standard presentation layer, such as the server-rendered setup page
(`templates/setup.html`), don't have to match the React components.

Docs say "recommended", never "supported" (a line that must quote the word ends with
`wording-ok:`). No Python: tooling is shell or Go.

## Development

- `make dev` (dev Postgres from `deploy/docker-compose.dev.yml`, Go server, Vite).
- `make test` (Go, against `DATABASE_URL`; starts the dev DB if unset), `cd web && npm test`
  (Vitest), `make e2e` (Playwright in shards on fresh databases). Details: `docs/agents/testing.md`.
- `make lint` (shell, Go, web), `make deadcode`, `cd web && npm run build` (outputs `web/dist`).
- Tests sit next to the code they test; no milestone-named test files.
- There is no fallback database: `fold serve` without `DATABASE_URL` exits with a "start the dev
  database" message, in dev too. Env vars: `config.go`, `.env.example`; `SESSION_SECRET` is
  required unless `APP_ENV=dev`.
- CLI (PLAN 11.4): `fold` (same as `fold serve`), `fold healthcheck`,
  `fold migrate [up|status|down-one]`, `fold version`; `--json` gives machine-readable output.

### Before every push

Grier's rule. They take seconds and catch what CI would:

```bash
go build ./...
make lint-go     # go vet, gofmt, staticcheck
make lint-web    # tsc (tests included), knip, knip --production (test-only exports need a `@public` reason)
make deadcode
```

CI runs on pull requests and on pushes to `master` only, so a branch push alone starts no CI.
Branch pushes use an exact refspec (`HEAD:refs/heads/<branch>`). Commits: short imperative
summary; PRs describe the behavior change.

### Phasing high-complexity work

Grier's rule. High-complexity means the change touches database, API and UI together, or changes
3+ tables. Split such milestones early into thin end-to-end slices, each built database, then API
and internals, then UI. New logic can ship hidden until its UI lands. Schema changes are
expand-then-contract (`docs/agents/migrations.md`).

### Merging pull requests

Grier's rule. An agent may merge a fold PR, as a normal merge commit, when all of these hold:

1. Every CI check is green (`test`, `web`, `provision`; branch protection requires them) on a head
   that contains master's tip. GitHub refuses a PR that is behind master (DEC-146); merge master
   into it (never rebase), wait for CI again, and then merge.
2. All five review concerns were reviewed by a separate agent from the one that implemented the
   change, preferably running a different model:
   - **UI, UX, accessibility and copy**: familiar patterns and the shared components, the axe and
     layout gates, and plain, warm wording (DEC-52, DEC-60 to DEC-66);
   - **simplicity and performance**: no complexity that would only get cut later, in any code;
     query shape, indexes, lock cost and bundle size where they apply;
   - **security, privacy and safeguarding**: attacks, and also who can see a name, a place or
     contact details, whether erased data stays erased, and what minors and guardians get;
   - **correctness against the plan, with tests that prove it**: the backend, API and UI do what
     the plan and its decisions say, and the tests would fail if the behavior broke;
   - **operations and docs**: deployment stays one `docker compose up`, migrations are
     expand-then-contract, upgrade, rollback and recovery levels still work, new config is in
     `.env.example`, and `docs/API.md`, `STATUS.md` and the plan files are updated in the same PR.

   One reviewer may cover several concerns. A concern the change doesn't touch still gets an
   explicit "not affected" from the reviewer. The review names the exact head SHA it reviewed; a
   new commit on the PR needs that head reviewed again before merging (a merge of master that
   changes nothing in the PR's own files counts as the same head). Keep the findings with the PR
   (its description or a comment), and resolve blocking findings before merging.
3. No product concern needs a human. No Grier decision on the PR is still open, and it makes no
   new product decision. Changes to security or visibility policy, repo settings, or production
   and deploy are product concerns unless Grier already decided that change. A check that only a
   person can run on real devices or live services (phones, mail providers, TLS on a real domain,
   SMS) doesn't block a merge: build it best effort, test what an agent can, and add the check to
   `docs/FIRST-CHURCH-CHECKS.md` in the same PR (DEC-128).

After a merge, merge master into the other open branches (never rebase) and report the merge SHA.

## Rules and what enforces them

Add a row when Grier corrects a repeat mistake; drop it once the mistake can't happen. Prefer
structure, then types, then a check, then a test. Rules specific to one area are listed with their
tests in that area's `docs/agents/` file.

| Rule | Enforced by |
|------|-------------|
| Every list of event dates hides events the viewer can't see | `TestNarrowEventHiddenFromGroupLists` |
| Event and notice output never shows a hidden group's name, place or id | `TestGroupDetailsGoThroughGroupShownTo` (an exception line ends with `// group-ok: <reason>`) |
| Making a group or one event more private previews, confirms, cancels and tells people | `TestGroupMorePrivateTightensAndCancels`, `TestEventMorePrivateCancelsShutOutRegistrants`, `e2e/event-visibility.spec.ts` |
| Notification rows are private to their owner; text has no contact details; inline actions are allow-listed and checked on the server | `TestStoredNotificationsAcceptance`, registry and privacy tests in `internal/notify`, account-request handler tests |
| `web/src/types.ts` matches the Go JSON structs field for field | `TestWebTypesMatchGoJSON` |
| Every state-changing route goes through CSRF, except `csrfExemptRoutes` | `TestCSRFExemptRoutesAreExact` |
| Logout leaves nothing on screen or in cache | `TestAPIResponsesAreNoStore`, `TestLogoutEndsSession`, `web/e2e/session.spec.ts` |
| Only sign-in and a leader's invitation limit answer 429 (DEC-52) | `TestOnlyLoginAndInvitesAnswer429`, `TestGuestExistingEmailHintIsRateLimited`, `TestPasswordResetSkipsGuests` |
| A session is "still current" by `sessionCurrent` alone | `TestSessionEpochsAreComparedOnlyBySessionCurrent`, `TestSessionCurrentPredicate`, `TestLockActorInRefusesEveryStaleSession` |
| Throttle keys come from the typed builders; erasure deletes every owned key | `TestThrottleKeysComeFromBuilders`, `TestThrottleSQLStaysInItsHelpers`, `TestOwnedThrottleKeysCoverEveryOwnedBuilder`, `TestThrottleKeyStringsAreUnchanged`, `TestAccountErasureDeletesOnlyOwnedThrottleKeys` |
| Colors come from theme tokens (DEC-60) | `TestColorsComeFromThemeTokens` |
| Pages never show the server's error text; every API error code has wording | `TestPagesNeverShowServerErrorText`, `TestWebErrorCopyCoversEveryCode`, `TestDeclaredAPIErrorsAreComplete`, `TestDeclaredMessagesAreWrittenOnce`, `TestAPIErrorsAreDeclaredAtPackageLevel`, `web/e2e/friendly-errors.spec.ts` |
| Main pages have no moderate+ axe issues at 390 and 1280 px; every field is labelled | `web/e2e/a11y.spec.ts` over `web/e2e/ui-pages.ts` |
| No sideways scroll, clipped select or target under 24px; sub-44px controls at 390 only go down | `web/e2e/layout.spec.ts` with `web/e2e/ui-baseline.json` |
| No new raw controls, `!` overrides, misused danger color, literal `style=` or date formatting outside `lib/when.ts` | `TestUIRatchets` (counts in `ui-ratchet.json`; lower them when a page gets better), `TestUIRatchetsCatchPastMistakes` |
| React forms use the shared `Form`; secret whitespace is never trimmed | `TestReactFormsUseCustomValidation`, `TestReactFormValidationGuard`, `components/ui/Form.test.tsx` |
| Selected chips are stronger than unselected; field errors are announced | `Chip.test.tsx`, `Field.test.tsx` |
| A photo-less group's cover comes from `coverSrc` (DEC-66); empty states are never red | `web/src/lib/covers.test.ts`, `EmptyState.test.tsx` |
| Token contrast: text 4.5:1, control edges 3:1 | `npm run contrast` (in CI) |
| The entry JS chunk stays under its gzip budget; rarely needed libraries load with `import()` | `npm run bundle-size` (in CI) |
| Seat counts never overrun under concurrent registration | `TestConcurrentGoingRespectsCapacity` |
| Secrets in URLs never reach the access log | `TestAccessLogKeepsSecretsOut` |
| Tests sit next to their code | `TestTestFilesSitNextToTheirSource` |
| Source guards read the whole production tree, not a directory glob | `internal/guardkit` tests, `TestContactRequestTextGuardReadsTheRealTree` |
| Schema changes are new goose files; models match them | `migrations/migrations_test.go`, `TestBaselineFileShape` (see `docs/agents/migrations.md`) |
| Work reaches master only through a PR with `test`, `web` and `provision` green on a head containing master's tip | GitHub ruleset on `master` (no bypass actors); `scripts/merge-rules-check.sh` in CI (`TestCIRunsTheMergeRulesCheck`, `TestMergeRulesCheckScript`) |
| `go vet`, `gofmt` and staticcheck are clean | `make lint` in CI |
| No Python | `TestNoPython` |
| Docs say "recommended", never "supported" | `TestDocsSayRecommendedNotSupported` |
| One Node version: `.nvmrc` | `TestNodeVersionComesFromNvmrc` |
| A build never deletes `web/dist/README.txt` | `keep-dist-readme` plugin in `web/vite.config.ts` |
| e2e specs switch users safely | `signIn` and `signInTo` in `web/e2e/helpers.ts` |
| Checks an agent can't run are listed for the first church | Review only: the PR adds them to `docs/FIRST-CHURCH-CHECKS.md` and lists them in its description |
