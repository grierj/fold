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

- **Go**: language minimum and toolchain pin come from `go.mod` (currently 1.26 and 1.27.3), Gin (JSON API under `/api/v1`), GORM
- **PostgreSQL only**, everywhere: production, development, and tests
  (`DATABASE_URL`, DEC-1, DEC-24). There is no second database, so use
  Postgres features freely. `CGO_ENABLED=0`; keep it that way
- Schema: versioned **goose** SQL files in `migrations/`, embedded in the
  binary and run on start under an advisory lock. They are the source of
  truth; GORM models must match them (C1)
- **React + Vite + Tailwind** SPA in `web/` (Node major in `.nvmrc`); `web/dist/` is embedded into
  the binary. Release pipeline runs `npm run build` before `go build`

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
copied from the group at creation (DEC-48). A nullable occurrence `visibility_override` wins for that date (`policy.EffectiveVisibility`); date candidates never authorize the series. `leader_opened` marks one a
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
- Styling (DEC-60): use the role tokens and shared control classes in
  `web/src/index.css` (`bg-primary`, `text-text-muted`, `.btn-primary`,
  `.input`, `.chip`). Pick the role by meaning, not by the color you want.
- UI controls (PLAN DEC-60–DEC-66): new controls use `components/ui` (`Button`,
  `Chip`, `Form`, `Field` with `Input`/`Textarea`/`Select`, `EmptyState`); the
  ratchets below stop new raw ones. Judgment the checks can't make:
  one primary action per view; `danger-outline` beside a main action and
  solid `danger` only when destruction is the point of the dialog; empty
  states say what would appear and offer the next step, never in red;
  status is never color alone; copy is plain, warm, second person. Prove a
  UI change with `.cursor/skills/verify-ui` and the 390px screenshots.
- Errors (DEC-61): a new error a person can act on gets a code in
  `api_errors.go` and a line in `ERROR_COPY` saying what happened and what
  to do next. DEC-52 decides what is never an error at all.
- Membership rules live in `internal/policy`: removal (`CanRemoveMember`),
  handoff (`CanBeSuccessor`), role changes. A removed membership keeps its
  row with `status=removed` (DEC-35).
  Adding anyone to a group goes through `changeRole`, which locks the
  group row and checks `policy.GroupHasRoom` (member limit, DEC-39).
- Migrations: every schema change is a **new** goose file in `migrations/`
  (`NNNNN_name.sql`); never edit an old one. The model change goes in the
  same PR and `models.All()`.
- Tests: `make test` (`go test ./...` against `DATABASE_URL`; starts the
  dev database if unset). Handler tests use `httptest` and the package's
  Postgres database from `internal/testdb` (see `testhelpers_test.go` for the
  cookie-jar + CSRF-dance pattern — reuse it).
- Frontend: `cd web && npm test` runs Vitest component tests
  (`src/**/*.test.tsx`, jsdom + Testing Library). `make e2e` runs Playwright
  (Chromium only) in `web/e2e` against the real binary on a fresh
  `<db>_e2e` Postgres database; `seed.setup.ts` creates the data the specs
  share, so specs run one at a time. Assert on roles and visible text, and
  accept `confirm()` dialogs with `page.on("dialog")`. `signIn` starts from
  a signed-out browser, so switch users by calling it again. First run:
  `cd web && npx playwright install chromium`.
- Permissions: every check is a pure function in `internal/policy` (section 6
  of the plan) with table tests. Handlers load facts and call it; never
  inline role checks or read `Group.LeaderID` (display only). Leadership is
  `Membership.Role = leader`; a group can have several leaders.
- Audit: record sensitive actions with `audit.Write` (`internal/audit`) in
  the same transaction as the change. Admin notices are separate stored
  notification rows written in that transaction, not derived from audit history.
- Only `FOLD_TRUSTED_PROXIES` may set `X-Forwarded-For`.

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
  and the inline-action allow-list (group approval and eligible clean-adult
  account requests); it never looks anything up, so the
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
  served at `/uploads/<key>` with `nosniff` (currently public assets; private assets are future M4.7). Keys are server-generated —
  never trust client filenames. Uploads are content-sniffed
  (JPEG/PNG/GIF/WebP only), ≤ 5MB.
- **S3 seam:** to move to S3-compatible storage, implement
  `storage.Store` (Put → bucket, URL → presigned/public object URL,
  Delete/Exists → bucket ops), wire it in `main()` from new env config,
  and change `serveUpload` to redirect instead of serving files. No
  handler changes needed.
- Docker: the final image pins `WORKDIR /` (distroless doesn't guarantee
  it) and pre-creates `/data/uploads` with `--chown=65532:65532` so the
  nonroot app can write to the fresh named volume. Compose mounts the named
  volume `uploads` at `/data`; uploaded files live in its `uploads/` subdirectory. Do not drop the mount; an
  anonymous volume is discarded on `up --build`.

## Phasing high-complexity work

Grier's rule (Oct 22, 2026).

- High-complexity means the change touches database, API and UI together, or
  changes 3+ tables. Smaller work is exempt.
- Split such milestones early into thin end-to-end slices. Build each slice
  database, then API and internals, then UI. New logic can ship hidden until
  its UI lands.
- Once live, schema changes use expand and contract:
  1. Expand: additive changes only, plus data migration. Add a trigger on the
     old columns that logs any INSERT or UPDATE to an audit table.
  2. Wait: at least 14 days, or two runs of the slowest job touching the
     columns, whichever is longer. Reset `pg_stat_statements` at the start of
     the wait.
  3. Contract: the migration refuses to run if the audit table has any hits,
     if any `pg_stat_statements` query mentions the old column, or if the
     deployed build SHA (health endpoint) predates the expand commit. In CI,
     each contract migration names its expand migration and fails if that one
     is under 14 days old.
- Don't rename columns at expand; it breaks old versions still running.
- Before go-live, contract can follow right away.

## Development

```bash
make dev      # dev Postgres (deploy/docker-compose.dev.yml) + Go server + Vite
make test     # go test ./... against DATABASE_URL (starts the dev DB if unset)
make lint     # shell, Go (vet/gofmt/staticcheck), and web lint
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

Grier's rule (Oct 20, 2026). Wright may merge a fold PR, as a normal merge commit, when all of these hold:

1. Every CI check is green (`test`, `web`, `provision`; branch protection requires them).
2. Independent review approved it. Every fold PR needs review (Grier, Oct 20, 2026; orchestrate substitution approved Oct 25, 2026):
   - UI, UX or design work: Illuminator, or the orchestrate plugin's UI review.
   - Database or performance work: Whittler, or the orchestrate plugin's simplicity/database and efficiency reviews, to keep out complexity that would only get cut later.
   - Security, and backend or API work: Warden, or the orchestrate plugin's security and correctness reviews.
   - The orchestrate plugin's adversarial and domain-specific reviews serve as substitutes for the named reviewer bots; no separate named-bot approval or per-PR substitution permission is required. Use the reviews relevant to the change, retain their findings and approval evidence, and resolve blocking findings before merging. A completed plugin run alone is not review approval.
3. No Grier decision on it is still open.

PRs that change security or visibility policy, repo settings, or production and deploy wait for Grier, unless he already decided that change. After a merge, merge master into the other open branches (never rebase) and report the merge SHA.

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
  `deploy/digitalocean/provision.sh`. `smoke.sh` rechecks health, TLS, and
  that Postgres and the app are not published. `upgrade.sh` pins an image
  tag or commit, dumps first, and can restore that dump. The church owns
  the droplet, DNS, and email account. SMS is optional (Telnyx when they
  want it). The script never calls `doctl` and never creates droplets,
  firewalls, snapshots, backup plans, or object-storage buckets. Postgres
  is the Compose `db` service. Run `provision.sh --dry-run` before a real run.

## SMS (messaging Phase 3, not yet)

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
| Every list of event dates hides events the viewer can't see | Structure: `dateCandidates` loads facts without granting series rights; `visibleSet.canSeeDate` filters each date before limits; `TestNarrowEventHiddenFromGroupLists` |
| Event and notice output never shows a hidden group's name, place or id (PR #41) | `a.groupShownTo` plus `TestGroupDetailsGoThroughGroupShownTo` (lines needing an exception end with `// group-ok: <reason>`) |
| Making a group or one event more private previews the cancellations and needs confirmation | `visibility-impact` for both, a 409 without `confirm`; `TestEventMorePrivateCancelsShutOutRegistrants`, `e2e/event-visibility.spec.ts` |
| Making an event more private cancels and tells people it shuts out, whether the group or the event changed | `cancelLosers` in both paths; `TestGroupMorePrivateTightensAndCancels`, `TestEventMorePrivateCancelsShutOutRegistrants` |
| Notification text has no email address or phone number; inline actions are allow-listed and account-request eligibility is checked on the server | Registry/privacy tests in `internal/notify`; account-request handler tests |
| Notification rows are private to their owner and inline actions are checked on the server | `notify` queries filter by `user_id`; `TestStoredNotificationsAcceptance` |
| `web/src/types.ts` matches the Go JSON structs field for field | `TestWebTypesMatchGoJSON` (add a pair to `tsContracts` for each new response type) |
| State-changing routes outside CSRF are listed exactly | `TestCSRFExemptRoutesAreExact` |
| Logout leaves nothing on screen or in cache | `no-store` on API and shell; `TestAPIResponsesAreNoStore` (`api_test.go`), `TestLogoutEndsSession`, `web/e2e/session.spec.ts` |
| Public forms never show a rate limit or reveal an account (DEC-52); only sign-in and a leader's invitation limit answer 429 | `TestOnlyLoginAndInvitesAnswer429` (`guards_test.go`); `TestGuestExistingEmailHintIsRateLimited`, `TestPasswordResetSkipsGuests` |
| Colors come from theme tokens: no hex outside `@theme`, no Tailwind palette or scale-step classes (DEC-60) | `TestColorsComeFromThemeTokens` (`guards_test.go`); a line that must keep a hex says why with `color-ok:` |
| Pages never show the server's error text; every API error code has wording | `TestPagesNeverShowServerErrorText`, `TestWebErrorCopyCoversEveryCode`, `TestErrorCodesKeysAreUsed` (`guards_test.go`, `api_errors_test.go`); `web/e2e/friendly-errors.spec.ts` |
| Main pages (including event, scheduling, Join and My groups) have no serious or critical axe issues at 390 and 1280 px; every field is labelled | `web/e2e/a11y.spec.ts` over `web/e2e/ui-pages.ts`, setup page in `web/e2e/seed.setup.ts` (run by `make e2e`) |
| Tests sit next to the code they test; no milestone-named test files | `TestTestFilesSitNextToTheirSource` (`guards_test.go`, run by `make test` in CI); its error names the file to move to |
| Every state-changing route goes through CSRF, except the exact list in `csrfExemptRoutes` | `TestCSRFExemptRoutesAreExact` |
| Schema changes are new goose files; GORM models match them | `migrations/migrations_test.go` drift check |
| `go vet` and `gofmt` are clean | `make lint` in CI |
| No Python: tooling is shell or Go | `TestNoPython` (`guards_test.go`; b6c132e) |
| Docs say "recommended", never "supported" | `TestDocsSayRecommendedNotSupported` (`guards_test.go`; 98f1a9a); a line that must quote the word ends with `wording-ok:` |
| One Node version: `.nvmrc` | CI reads it (`node-version-file`); `TestNodeVersionComesFromNvmrc` checks the Dockerfile and docs (a21cc93, 8c676ac) |
| A build never deletes the committed `web/dist/README.txt` | Structure: the `keep-dist-readme` plugin in `web/vite.config.ts` writes it back (c8ce79d, 08137aa) |
| e2e specs switch users safely | Structure: `signIn` in `web/e2e/helpers.ts` clears cookies first (2722a5d, 0a10b04) |
| Seat counts never overrun under concurrent registration | `TestConcurrentGoingRespectsCapacity` (`api_registrations_test.go`): 50 real requests race for 10 seats; exactly 10 going and 40 waitlisted |
| Token contrast: text 4.5:1, control edges 3:1 | `npm run contrast` (`web/scripts/token-contrast.mjs`, in CI; fails 3 pairs on 6cdcf06) |
| No sideways scroll, no clipped select, no target under 24px (hard gates since PR 5); kinds of control under 44px at 390 only go down | `web/e2e/layout.spec.ts` with `web/e2e/ui-baseline.json` |
| No new raw controls, `!` overrides, misused danger color, literal `style=` or date formatting outside `lib/when.ts` | `TestUIRatchets` (`guards_test.go`, counts in `ui-ratchet.json`; lower them when a page gets better), self-tested by `TestUIRatchetsCatchPastMistakes` |
| Selected chips are stronger than unselected; fields are labelled and their errors are announced | `Chip` and `Field` in `web/src/components/ui`, `Chip.test.tsx`, `Field.test.tsx` |
| React forms use the shared `Form`: no browser bubbles, persistent errors under fields, first invalid field focused; secret whitespace is never trimmed | `TestReactFormsUseCustomValidation`, `TestReactFormValidationGuard` (`guards_test.go`), `components/ui/Form.test.tsx` |
| A photo-less group's cover comes from `coverSrc` (its own image, else one of six patterns by id, DEC-66); empty states are the neutral `EmptyState`, never red | `web/src/lib/covers.test.ts`, `web/src/components/ui/EmptyState.test.tsx`; the danger ratchet in `TestUIRatchets` |
| Work reaches master only through a pull request with the `test`, `web` and `provision` checks green; branch pushes use an exact refspec (`HEAD:refs/heads/<branch>`) | GitHub branch protection on `master` (Oct 20, 2026): PR required, those three status checks required, admins included, no force-push or deletion. It replaced the opt-in pre-push hook from #74 |
| The entry JS chunk stays under its gzip budget; libraries few pages need are loaded with `import()` | `web/scripts/bundle-size.mjs` (`npm run bundle-size`, run in CI after the build) |
| Secrets in URLs (calendar feed tokens, guest manage links, `?token=` email links) never reach the access log | `accessLog` and `redactLogPath` (`access_log.go`) drop query strings and redact token segments; `TestAccessLogKeepsSecretsOut` (`access_log_test.go`) |
