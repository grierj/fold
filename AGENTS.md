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
- An event's `visibility` is its own value, copied from the group at creation (DEC-48). A nullable
  occurrence `visibility_override` wins for that date (`policy.EffectiveVisibility`); date
  candidates never authorize the series. Making a group more private tightens inherited events and
  asks about leader-opened ones (`api_group_visibility.go`).
- `cancel_source` is `series` (a series edit dropped the date; a later edit that includes it
  schedules it again and tells registrants, DEC-91) or `manual` (a person).
- Render any name with `User.PublicName()`, never the email. Sign-in needs `User.CanSignIn()`;
  login throttling is in Postgres (`login_throttles`, `api_auth.go`). `Email` is the login (DEC-23).
- Focal points (`AvatarFocalX/Y`, `CoverFocalX/Y`) are stored 0-100 and rendered as CSS
  `object-position`. `AvatarKey` is a blob-store key, not a URL.

## Conventions

- API: `/api/v1/...`, camelCase JSON. Authenticated endpoints return 401 JSON, never a redirect
  (only `/setup` redirects). Errors are `{"error": "human readable"}` with the right status.
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
- Errors (DEC-61): a new error a person can act on gets a code in `api_errors.go` and a line in
  `ERROR_COPY` (what happened, what to do next). DEC-52 decides what is never an error.
- Membership rules live in `internal/policy` (`CanRemoveMember`, `CanBeSuccessor`, role changes).
  Removal keeps the row with `status=removed` (DEC-35); voluntary departure keeps `status=left`
  (DEC-86); neither counts as active. Adding anyone to a group goes through `changeRole`, which
  locks the group row and checks `policy.GroupHasRoom` (DEC-39) and `policy.CanAddToGroup`.
  Leading a pending or archived group grants no power to add people or invite anyone into Fold
  (DEC-88; `policy.LeadsApprovedGroup`, `CanInviteIntoGroup`).
- Permissions: every check is a pure function in `internal/policy` (PLAN section 6) with table
  tests. Handlers load facts and call it; never inline role checks or read `Group.LeaderID`
  (display only). Leadership is `Membership.Role = leader`; a group can have several leaders.
- Audit: record sensitive actions with `audit.Write` (`internal/audit`) in the same transaction as
  the change. Admin notices are separate stored notification rows written in that transaction.
- Migrations: every schema change is a **new** goose file `NNNNN_name.sql`; never edit an old one; the one exception is the pre-live baseline window in DEC-138.
  The model change and `models.All()` go in the same PR.
- Tests: `make test` (`go test ./...` against `DATABASE_URL`; `scripts/go-test.sh` shards the root
  package; starts the dev database if unset). Tests hash passwords at bcrypt's cheapest cost; a
  test that measures timing calls `realPasswordCost(t)` first. Handler tests use `httptest` and
  `internal/testdb` (reuse the cookie-jar + CSRF-dance in `testhelpers_test.go`). `testdb.Shared`
  clears the database before each test, which needs a superuser (dev and CI have one); other roles
  fall back to slower TRUNCATE. Test sessions use `synchronous_commit=off`.
- Frontend tests: `cd web && npm test` (Vitest). `make e2e` runs Playwright against the real
  binary (first run: `cd web && npx playwright install chromium`). `web/e2e/run.sh` splits specs
  across shards (`E2E_SHARDS`), each with its own server, database and seed; never rely on another
  spec having run first. Refresh `durations.json` with `E2E_RECORD_DURATIONS=1 make e2e`. a11y and
  layout checks run in parts (`UI_PARTS`, `partSelected`); `UPDATE_UI_BASELINE=1` needs
  `E2E_SHARDS=1`. Prefer `gotoSettled` to `waitForLoadState("networkidle")`. `signIn` clears
  cookies first. Assert on roles and visible text; accept `confirm()` with `page.on("dialog")`.
  Traces: `web/test-results/shard-<n>/`.
- Background work goes through `internal/jobs` (M3.1, PLAN 5.5): register a `jobs.Kind` and queue with `jobs.Enqueue`, giving a `unique_key` when the work must happen once (daily tasks use `<kind>:<church date>`). Handlers must be safe to run twice and return their error so the worker retries. Never start a goroutine ticker for periodic work.
- Notifications are stored rows (PLAN 5.12), written by `notify.Send(tx, notify.Msg{...})` in the
  same transaction as the change and its `AuditLog` row. The kind registry
  (`internal/notify/kinds.go`) never looks anything up, so the caller decides what the recipient
  may see (pass a group's name, place or link only when `canSeeGroupIn` says so). Close tasks
  (`waiting`) with `notify.Resolve` wherever the subject is decided; when the same decision also sends to people who can hold that task (an admin who also leads the event), send first and resolve second, so both sides of an erasure want the user row first (`settleRoomDecision`, M7.4 N-23). `Send` also writes the delivery
  intent in that transaction (`BellOnly` records the bell row alone); the `notify.deliver` job sends
  the email and push after commit, so callers never deliver a notice themselves (R6). `Send` first locks its non-deleted recipients `FOR KEY SHARE` in ascending id (one query per 500) and skips a deleted one, so an account erasure either waits for the sender or finds nothing to resurrect (M7.4 R4-N, inventory 9.2.4). A fan-out that calls `Send` once per person takes that lock once with `notify.WithRecipients` and passes the `Scope` it is handed as `Msg.Scope`; `Send` checks the scope against its own transaction and recipients, and a forged, closed or foreign one fails with `ErrBadScope`, so no caller can skip the lock by assertion. Rolling back to a savepoint older than the lock releases it, so `WithRecipients` also watches the transaction (`internal/txguard`, a GORM plugin installed by `openDatabase`, the CLI and `testdb.Open`; a handle without it gets `ErrNoRollbackGuard`): a rollback to an older savepoint, or one it cannot read, ends the scope (a savepoint name counts only after GORM executed its `SAVEPOINT` without error and without `DryRun`; one that failed or never ran is untrusted, and one sent through `Row` or a query breaks the scope) and every later `Send` with it fails with `ErrBadScope`, while rollbacks to savepoints made inside the call (a failed nested `Transaction`) keep it. The watch reads the statements GORM sends, never the database, so it costs no query; it cannot see SQL sent around GORM, and Fold sends none. A repair that queues from an earlier selection goes through `notify.QueueDelivery` (`Protect`); nothing outside `notify` can insert delivery intent unprotected. Neither takes the contact gate.
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
  `api_message_preview.go` also remains synchronous by design, separate from those exceptions.
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
  package variables across files in one directory. It recognizes `mail.Sender`, `Adapter`,
  `SMTPSender`, `Postmark`, `DevOutbox`, `Suppressing` and `NoSender` in typed parameters, fields,
  variables, assertions and literals. Same-package helper signatures track each result position,
  including multiple results; `mail.New*` and `mail.WithSuppressions` return a sender in the first
  position, including through assigned constructor functions. Add new sender types to this list
  and the guard together.
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
- Docker: the image pins `WORKDIR /` and pre-creates `/data/uploads` with `--chown=65532:65532` so
  the nonroot app can write to a fresh volume. Compose mounts the named volume `uploads` at
  `/data`; do not drop it (an anonymous volume is discarded on `up --build`).

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
notifications", never on load; it skips waiting and claims tabs, and still has no `fetch` handler), `/manifest.webmanifest` comes from Go (`web_app.go`, church
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

The migration chain is one generated baseline (M7.7, DEC-138). `scripts/baseline-proof.sh generate`
made it from the frozen pre-squash chain (commit `be61e582c3011263307e4db4497056f7fd70bfd9`, 63
files through `00064`) and `scripts/baseline-proof.sh proof` re-proves a tree against that chain
with a normalized `pg_dump` diff, on a scratch server only (`--help` lists the arguments). Before
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
(cd web && npx knip)               # web lint (make lint-web); web/ has no eslint config
```

CI runs on pull requests and on pushes to `master` only, so a branch push
alone starts no CI; open the PR to get the checks.

### Merging pull requests

Grier's rule (Oct 20, 2026; generalized Oct 30, 2026). An agent may merge a fold PR, as a normal merge commit, when all of these hold:

1. Every CI check is green (`test`, `web`, `provision`; branch protection requires them).
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
| `web/src/types.ts` matches the Go JSON structs field for field | `TestWebTypesMatchGoJSON` (add a pair to `tsContracts` for each new response type) |
| Every state-changing route goes through CSRF, except the exact list in `csrfExemptRoutes` | `TestCSRFExemptRoutesAreExact` |
| Logout leaves nothing on screen or in cache | `no-store` on API and shell; `TestAPIResponsesAreNoStore`, `TestLogoutEndsSession`, `web/e2e/session.spec.ts` |
| Public forms never show a rate limit or reveal an account (DEC-52); only sign-in and a leader's invitation limit answer 429 | `TestOnlyLoginAndInvitesAnswer429` (`guards_test.go`); `TestGuestExistingEmailHintIsRateLimited`, `TestPasswordResetSkipsGuests` |
| Colors come from theme tokens: no hex outside `@theme`, no Tailwind palette or scale-step classes (DEC-60) | `TestColorsComeFromThemeTokens`; a line that must keep a hex says why with `color-ok:` |
| Pages never show the server's error text; every API error code has wording | `TestPagesNeverShowServerErrorText`, `TestWebErrorCopyCoversEveryCode`, `TestErrorCodesKeysAreUsed`; `web/e2e/friendly-errors.spec.ts` |
| Main pages (including event, scheduling, Join and My groups) have no moderate, serious or critical axe issues at 390 and 1280 px; every field is labelled | `web/e2e/a11y.spec.ts` over `web/e2e/ui-pages.ts`; setup page in `web/e2e/seed.setup.ts` |
| Tests sit next to the code they test; no milestone-named test files | `TestTestFilesSitNextToTheirSource` (its error names the file to move to) |
| Schema changes are new goose files; GORM models match them | `migrations/migrations_test.go` drift check |
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
| Work reaches master only through a pull request with the `test`, `web` and `provision` checks green; branch pushes use an exact refspec (`HEAD:refs/heads/<branch>`) | GitHub branch protection on `master`: PR required, those checks required, admins included, no force-push or deletion |
| The entry JS chunk stays under its gzip budget; libraries few pages need are loaded with `import()` | `web/scripts/bundle-size.mjs` (`npm run bundle-size`, in CI after the build) |
| Secrets in URLs (calendar and room feed tokens, guest manage links, unsubscribe and email settings links, `?token=` email links) never reach the access log | `accessLog` and `redactLogPath` (`access_log.go`); `TestAccessLogKeepsSecretsOut` |
| Push payloads never carry contact details, signed links or a hidden group's name; endpoints are never logged | `TestEveryKindsPushTextIsLockScreenSafe` (`internal/notify`), `TestPushLogsKeepSecretsOut` |
| Checks an agent can't run are listed for the first church | Review only (no test): the PR adds its checks to `docs/FIRST-CHURCH-CHECKS.md` and lists them in its description |
