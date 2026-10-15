# AGENTS.md — working instructions for AI agents on this repo

Working title: **Fold**. Church small-group / ministry / activity management.
The group-management slice of ChurchTrac, without the rest of it.

`docs/plan/PLAN.md` is the source of truth for product decisions; when it
disagrees with older repo docs (including this file), follow the plan.

## The one rule

**Deployment simplicity is the top concern.** Whatever you build, a church
volunteer (or a single SaaS operator) must be able to run it with
`docker compose up`. The shipped artifact is always a **single Go binary**
that serves both the JSON API and the frontend (the SPA bundle is embedded
via `go:embed`). No separate frontend hosting. Ever.

## Stack

- **Go 1.26** (Go 1.25 is out of security support), Gin (JSON API under `/api/v1`), GORM
- **PostgreSQL only**, everywhere: production, development, and tests
  (`DATABASE_URL`, DEC-1, DEC-24). There is no second database, so use
  Postgres features freely. `CGO_ENABLED=0`; keep it that way
- Schema: versioned **goose** SQL files in `migrations/`, embedded in the
  binary and run on start under an advisory lock. They are the source of
  truth; GORM models must match them (C1)
- **React + Vite + Tailwind** SPA in `web/` (Node 22 LTS); `web/dist/` is embedded into
  the binary. Release pipeline runs `npm run build` before `go build`
- **No Python.** Not for tooling, not for scripts. Shell or Go.

## Architecture

```
browser ──► single Go binary ──► /api/v1/* (JSON, cookie auth)
              │                    /* (embedded SPA, index.html fallback)
              └──► Postgres (everywhere)
```

- Single-tenant app. "SaaS" means many single-tenant deployments behind a
  proxy — there is deliberately **no multi-tenant data model**.
- Auth: cookie sessions + bcrypt (local accounts). OIDC (Microsoft Entra ID
  + Google) comes later — keep an `AuthProvider` seam in mind, don't build it yet.
- CSRF: hand-rolled synchronizer token (`csrf.go`). Every POST/PUT/DELETE
  needs the `csrf_token` form field or `X-CSRF-Token` header. It was
  deliberately NOT gorilla/csrf: v1.7's Origin/Referer checks assume TLS
  end-to-end and break plain-HTTP dev.
- Approval workflow is a core feature, not an afterthought: new groups enter
  `pending` until staff approves. Keep it working through every refactor.

## Data model (Phase 1+)

`User` → `Membership` → `Group` → `Event` → `EventOccurrence`;
`Registration` links a user to one occurrence. An event may instead be
church-wide (`events.group_id` null), created by an admin and run by its
`EventHost` rows. An event has a plain-text `description` (5000 characters,
same as a group). `visibility` is the event's own value,
copied from the group at creation (DEC-48); `leader_opened` marks one a
leader set wider than its group. Making a group more private tightens
inherited events and asks about leader-opened ones (`api_group_visibility.go`).
The wall clock (`starts_local` + IANA `tzid`) is the source of truth;
occurrences store UTC and are generated 18 months ahead (extended daily
under an advisory lock). See `models/models.go`. Groups are ongoing
entities; an event is a series (RRULE) or a single date. A series edit
moves future occurrences that have no per-date override and keeps their
registrations.

`User` carries profile fields: `Name` (full name), `DisplayName` (shown in
the UI), `Email` (the login: `citext`, unique, DEC-23), `PasswordHash` (nil
until set), `Status` (`active`, `invited`, `disabled`, ...), `Phone`, `AvatarKey` (blob-store key, not a URL),
`AvatarFocalX/Y` (0-100 focal point for cropped avatars).
`User.PublicName()` is the display fallback chain
(DisplayName → Name → "Member"; never the email) — use it anywhere a name
is rendered. Sign-in needs `User.CanSignIn()`; login throttling lives in
Postgres (`login_throttles`, see `api_auth.go`).
`Group` has `CoverFocalX/Y` for the cover image. Focal points are stored
0-100 and rendered as CSS `object-position`.

## Conventions

- API is versioned: `/api/v1/...`. JSON uses camelCase keys.
- Authenticated endpoints return 401 JSON (never a redirect — the SPA
  handles it). The old server-rendered redirect behavior is gone except
  for `/setup`.
- Errors: `{"error": "human readable"}` with the right status code.
- The app sends a strict CSP (DEC-37): no inline `<script>` or `style=""`
  markup in server templates, no `eval`, and no CDN or third-party scripts,
  styles, or fonts. React `style={{...}}` props are fine (they go through
  the CSSOM). Bundle everything through Vite.
- Membership rules live in `internal/policy`: removal (`CanRemoveMember`),
  handoff (`CanBeSuccessor`), role changes. A removed membership keeps its
  row with `status=removed` (DEC-35).
  Adding anyone to a group goes through `changeRole`, which locks the
  group row and checks `policy.GroupHasRoom` (member limit, DEC-39).
- Migrations: every schema change is a **new** goose file in `migrations/`
  (`NNNNN_name.sql`); never edit an old one. Add the model change in the
  same PR and list new models in `models.All()`. `AutoMigrate` never runs
  against a real database; the drift test (`migrations/migrations_test.go`)
  runs it on a throwaway database and fails on any CREATE or ALTER.
- Tests: `make test` (`go test ./...` against `DATABASE_URL`; starts the
  dev database if unset). Handler tests use `httptest` and the package's
  Postgres database from `internal/testdb` (see `testhelpers_test.go` for the
  cookie-jar + CSRF-dance pattern — reuse it).
- Test layout: tests live with what they test, never with the milestone
  that added them. Go: `X_test.go` next to `X.go`, same package (handler
  tests for `api_events.go` go in `api_events_test.go`). Helpers shared
  across test files go in `testhelpers_test.go`. Web: `Foo.test.tsx` next
  to `Foo.tsx`. Playwright: `web/e2e/<feature>.spec.ts`. Put a new test in
  the right file when you write it, not in a later cleanup. Only
  `testLayoutAllowed` in `guards_test.go` (helpers, repo guards, the
  migration drift check) may skip the same-named source file.
- Frontend: `cd web && npm test` runs Vitest component tests
  (`src/**/*.test.tsx`, jsdom + Testing Library). `make e2e` runs Playwright
  (Chromium only) in `web/e2e` against the real binary on a fresh
  `<db>_e2e` Postgres database; `seed.setup.ts` creates the data the specs
  share, so specs run one at a time. Assert on roles and visible text, and
  accept `confirm()` dialogs with `page.on("dialog")`. First run:
  `cd web && npx playwright install chromium`.
- `make lint` (`go vet` + `gofmt`) must be clean before any PR.
- Permissions: every check is a pure function in `internal/policy` (section 6
  of the plan) with table tests. Handlers load facts and call it; never
  inline role checks or read `Group.LeaderID` (display only). Leadership is
  `Membership.Role = leader`; a group can have several leaders.
- Audit: record sensitive actions with `audit.Write` (`internal/audit`) in
  the same transaction as the change. Admin notices derive from those rows.
- State-changing routes all go through CSRF except `/hooks/*` and
  `POST /u/unsubscribe/:token` (`csrfExemptRoutes`; a test keeps that list
  exact). Only `FOLD_TRUSTED_PROXIES` may set `X-Forwarded-For`.

- UI reference is **Meetup** (primary), with ChurchTrac as the secondary
  reference for church-admin screens Meetup lacks. Familiar beats original. Copy its patterns
  (group cards, date badges, join buttons); tweak later.
- Commit messages: short imperative summary; PRs describe behavior change.
- Setup/form UX: state every input requirement next to the field (e.g.
  password minimums). On a failed submission, re-render with the valid
  fields preserved — never echo back secrets (passwords) or payment data.
- Client-side image prep: `web/src/lib/image.ts` resizes uploads in the
  browser (`browser-image-compression`) before POST — keeps big-phone-photo
  CPU off the server. Server still enforces type/size limits.
- Notifications are stored rows (M1.1c, PLAN 5.12): one `notifications`
  row per recipient, written by `notify.Send(tx, notify.Msg{...})` in the
  same transaction as the change and its `AuditLog` row. The kind
  registry (`internal/notify/kinds.go`) owns category, wording, batching
  and the inline-action allow-list; it never looks anything up, so the
  caller decides what the recipient may see (pass a group's name, place or
  link only when `canSeeGroupIn` says so). Tasks (`waiting`) are closed
  with `notify.Resolve` wherever the subject is decided. The SPA keeps the
  feed in memory only and polls `GET /notifications/badge`.
- Guest registration (M1.4, C5): a signed-out adult registers for a public
  event by name and email. The submit stores only a `guest_signups` row;
  the guest `User` (`status=guest`, no password, `CanSignIn` false) and
  their ordinary `Registration` (`source=guest_form`, hashed manage
  token) exist only after the emailed link is redeemed. The rule is
  `policy.CanRegisterGuest`; never widen `CanRegisterEvent` to a nil
  person, and never load a guest row as an actor (`asPerson` would treat
  it as signed in). Public answers stay uniform (no enumeration), and
  verification emails cap per target address (3/hour, 10/day). Upgrading
  a guest to an account, by request approval or invitation, reuses the
  same row so registrations stay theirs; until then `changeRole` refuses
  to give a guest a membership.

## Uploads & blob storage

- User-uploaded assets (avatars, group covers) go through the
  `storage.Store` interface (`storage/storage.go`), never raw filesystem
  paths in handlers. `App.Uploads` is the instance.
- The local implementation (`storage.LocalStore`) ships now:
  `UPLOAD_DIR` (`./data/uploads` default, `/data/uploads` in compose),
  served at `/uploads/<key>` with `nosniff`. Keys are server-generated —
  never trust client filenames. Uploads are content-sniffed
  (JPEG/PNG/GIF/WebP only), ≤ 5MB.
- **S3 seam:** to move to S3-compatible storage, implement
  `storage.Store` (Put → bucket, URL → presigned/public object URL,
  Delete/Exists → bucket ops), wire it in `main()` from new env config,
  and change `serveUpload` to redirect instead of serving files. No
  handler changes needed.
- Docker: the final image pins `WORKDIR /` (distroless doesn't guarantee
  it) and pre-creates `/data/uploads` with `--chown=65532:65532` so the
  nonroot app can write to the fresh named volume. Compose mounts that
  directory as the named volume `uploads`. Do not drop the mount; an
  anonymous volume is discarded on `up --build`.

## Development

```bash
make dev      # dev Postgres (deploy/docker-compose.dev.yml) + Go server + Vite
make test     # go test ./... against DATABASE_URL (starts the dev DB if unset)
make lint     # go vet + gofmt
make e2e      # Playwright (Chromium) on a fresh Postgres database
cd web && npm test        # Vitest component tests
cd web && npm run build   # outputs to web/dist (embedded)
```

There is no fallback database: `fold serve` without `DATABASE_URL` exits
with a "start the dev database" message, in dev too. Env vars: see
`config.go` and `.env.example`. `SESSION_SECRET` is required unless
`APP_ENV=dev`.

CLI (C11): `fold` (same as `fold serve`), `fold healthcheck`,
`fold migrate [up|status|down-one]` (needs only `DATABASE_URL`), and
`fold version` (needs nothing). `--json` gives machine-readable output.

## Deployment

- `docker compose up --build -d` → app + Postgres 16 + Caddy (auto HTTPS).
  Fill `.env` from `.env.example` first. Postgres data is `pgdata`;
  uploads are `uploads`. Both are named volumes.
- The compose network has the fixed subnet `172.30.0.0/24`, which is the
  default `FOLD_TRUSTED_PROXIES`. Change both together.
- Dockerfile is multi-stage (node → go → distroless), `CGO_ENABLED=0`.
  Keep it that way — the binary must build without a C compiler.
- Every `APP_ENV` refuses to boot without `DATABASE_URL`. Migrations run
  on start under a Postgres advisory lock; a failed migration exits non-zero.
- First boot with an empty DB serves the setup wizard at `/setup`.
  After any user exists, the process caches that and will not show the
  wizard again until it is restarted.
- `GET /healthz` pings the database. Compose and the image use
  `/fold healthcheck` because distroless has no shell or curl.
- A live `docker compose up` is still the check that Caddy, TLS, and the
  volumes work together.
- Production is one VPS with this Compose stack. DigitalOcean steps and
  the idempotent hardener live in `docs/DEPLOY-DIGITALOCEAN.md` and
  `deploy/digitalocean/provision.sh`. The church owns the droplet, DNS,
  and email account. SMS is optional (Telnyx when they want it). The
  script never calls `doctl` and never creates droplets, firewalls,
  snapshots, backup plans, or object-storage buckets. Postgres is the Compose
  `db` service. Run `provision.sh --dry-run` before a real run.

## SMS (Phase 2, not yet)

Group SMS goes through SMS provider adapters (Telnyx default, Twilio next).
It is **not built yet**, and none is required to run Fold. When you build it:
put it behind provider adapters fed by `internal/notify` deliveries (PLAN 5.12), broadcast-only v1,
replies route to the group leader. Don't start early.

## Consistency

Similar things behave the same way (Grier, Oct 2026). When one path gets a
rule (a preview, a confirmation, an audit row, a notice), give its siblings
the same rule with the same API shape and UI component, or write down why
not. Example: group and single-event tightening share `visibility-impact`,
`cancelLosers`, and `GroupVisibilityReview`. In the UI, one shared component
per kind of control: `ZoneSelect` (every time zone field), `VisibilityField`
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
| Every list of event dates hides events the viewer can't see | Structure: `occurrencesForEvents` filters with `policy.CanSeeEvent`; `TestNarrowEventHiddenFromGroupLists` |
| Event and notice output never shows a hidden group's name, place or id (PR #41) | `a.groupShownTo` plus `TestGroupDetailsGoThroughGroupShownTo` (lines needing an exception end with `// group-ok: <reason>`) |
| Making a group or one event more private previews the cancellations and needs confirmation | `visibility-impact` for both, a 409 without `confirm`; `TestEventMorePrivateCancelsShutOutRegistrants`, `e2e/event-visibility.spec.ts` |
| Making an event more private cancels and tells people it shuts out, whether the group or the event changed | `cancelLosers` in both paths; `TestGroupMorePrivateTightensAndCancels`, `TestEventMorePrivateCancelsShutOutRegistrants` |
| Every notification kind says no email address or phone number; only Approve-a-group is inline | `TestEveryKindRendersNoContactDetails`, `TestOnlyApproveGroupIsAllowListed` (internal/notify) |
| Notification rows are private to their owner and inline actions are checked on the server | `notify` queries filter by `user_id`; `TestStoredNotificationsAcceptance` |
| `web/src/types.ts` matches the Go JSON structs field for field | `TestWebTypesMatchGoJSON` (add a pair to `tsContracts` for each new response type) |
| State-changing routes outside CSRF are listed exactly | `TestCSRFExemptRoutesAreExact` |
| Logout leaves nothing on screen or in cache | `no-store` on API and shell; `TestAPIResponsesAreNoStore` (`api_test.go`), `TestLogoutEndsSession`, `web/e2e/session.spec.ts` |
| Public forms never show a rate limit or reveal an account (DEC-52); only sign-in and a leader's invitation limit answer 429 | `TestOnlyLoginAndInvitesAnswer429` (`guards_test.go`); `TestGuestExistingEmailHintIsRateLimited`, `TestPasswordResetSkipsGuests` |
| Tests sit next to the code they test; no milestone-named test files | `TestTestFilesSitNextToTheirSource` (`guards_test.go`, run by `make test` in CI); its error names the file to move to |
