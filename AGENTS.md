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
`Registration` links a user to one occurrence. The wall clock
(`starts_local` + IANA `tzid`) is the source of truth; occurrences store
UTC and are generated 18 months ahead (extended daily under an advisory
lock). See `models/models.go`. Groups are ongoing entities; an event is a
series (RRULE) or a single date. A series edit moves future occurrences
that have no per-date override and keeps their registrations.

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
  Postgres database from `internal/testdb` (see `auth_test.go` for the
  cookie-jar + CSRF-dance pattern — reuse it).
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
- Notifications are derived, not stored (`GET /api/v1/notifications`):
  approvals for admins, upcoming events for members, and a notice when a
  date they registered going or maybe is cancelled. Dismissals live in
  localStorage per user, keyed by the notice `id`; logout clears all
  browser storage and reloads the page.

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

## SMS (Phase 2, not yet)

Group SMS goes through SMS provider adapters (Telnyx default, Twilio next).
It is **not built yet**, and none is required to run Fold. When you build it:
put it behind a `Notifier` interface (`internal/notify/`), broadcast-only v1,
replies route to the group leader. Don't start early.
