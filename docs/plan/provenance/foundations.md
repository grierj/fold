# Fold provenance: the starting repo and the architecture change index

History, not current guidance. Section 3 describes the repo as the plan found it and updated it
through October 2026; section 4 is the index of foundation changes (C1 to C17) made from that
starting point. The `Cn` ids are stable and still cited by decisions and code. What Fold is today
is in `AGENTS.md`, the design files in [`../design/`](../design/) and the code.

## 3. The current repo and what to keep

### 3.1 What is there today

Merged through Oct 25, 2026 (see STATUS for PRs):

- Go language minimum 1.26, toolchain pinned in `go.mod` (currently 1.27.3); Gin, GORM, PostgreSQL 16 everywhere, `DATABASE_URL` required in every environment, `CGO_ENABLED=0`.
- React 19, Vite 8, Tailwind 4 and React Router 7. Node major comes from `.nvmrc`. `web/dist` is embedded in the Go binary. Vitest, Playwright, accessibility/layout checks, contrast and bundle budgets are installed.
- Cookie sessions, bcrypt, email login, Postgres login throttles, synchronizer CSRF, trusted-proxy configuration and security headers. Stateless-cookie logout limitations remain accepted (DEC-41).
- Docker Compose runs app, Postgres and Caddy, with `pgdata`, `uploads` (mounted at `/data`, files in `/data/uploads`), `caddydata` and `caddyconfig`. The image is distroless/nonroot. `Makefile` owns development, test and lint commands.
- Root `package main` contains HTTP handlers and wiring; domain logic is under `internal/`, models in `models/`, and embedded goose SQL in `migrations/` owns the schema. Tests sit beside source. MIT license, copyright Grier Johnson.
- Groups have `public|private|invite_only` visibility, approval, membership-based co-leaders, limits, removal and sole-leader handoff.
- Events have wall-clock recurrence, stored occurrences, church-wide hosts, waitlists, remote attendance, childcare, calendar views and ICS feeds. Series and per-date visibility are separate; date access never grants series or hidden-group access.
- Invitations, account requests, password reset, verified email changes and verified guest registration use the minimal SMTP/devoutbox sender.
- Notifications are stored per user, with server-side read/archive state and post-commit email, not derived or dismissed in localStorage.
- Households, approval-first conversion, teen consent, guardian checks and church-local birthdays are implemented (DEC-79–DEC-83).
- Contact privacy, explicit audited reveals, name search, purpose-separated Ask to add, exact-date invitations and retained admin history/CSV are implemented. The narrow exact-date prerequisite does not complete M1.14.
- Avatars and covers are upload-only through `storage.Store`. UI controls use role tokens and shared components (DEC-60–DEC-66).

### 3.2 Goals already written in the repo

From `AGENTS.md` and `README.md`:

- "Deployment simplicity is the top concern." Always one Go binary that serves the API and the SPA. No separate frontend hosting.
- Single-tenant; "SaaS" means many single-tenant deployments. No multi-tenant data model.
- No Python, ever. Shell or Go for tooling.
- PostgreSQL everywhere (DEC-1, DEC-24); no alternate-database flags.
- OIDC (Microsoft Entra ID and Google) later, behind an `AuthProvider` seam.
- The group approval workflow is core and must survive refactors.
- Meetup is the primary UI and design reference, and ChurchTrac is the secondary one (Grier, Oct 10, 2026).
- SMS is future Phase 3 messaging work: provider adapters, Telnyx first and Twilio second, neither required to run Fold. Replies relay through Fold (7.8).
- Versioned SQL migrations in `migrations/` have been authoritative since M0.1.

### 3.3 Keep

These match Grier's requirements and stay unless a milestone says otherwise. Under the clean break (DEC-22), "keep" means "keep the approach"; code and schema may still be rewritten.

- [x] Single Go binary with the embedded SPA, served behind Caddy.
- [x] Go + Gin + GORM; PostgreSQL; `CGO_ENABLED=0`. (Drop the SQLite dev and test path: DEC-1, C14.) See the stack note below.
- [x] React + Vite + Tailwind SPA, Meetup-style UI.
- [x] Docker Compose with app + Postgres + Caddy, named volumes (`pgdata`, `uploads`, `caddydata`, `caddyconfig`), distroless nonroot image, binary-based healthcheck.
- [x] Cookie sessions, bcrypt, `SessionVersion` invalidation, hand-rolled CSRF, login throttling (rekeyed to email and IP in M0.3).
- [x] `storage.Store` interface with server-generated keys and content sniffing.
- [x] RRULE-based recurrence (we change how it is expanded; see C2).
- [x] Group approval workflow and the existing visibility rules, as updated by PR #7 (admins see invite-only groups, DEC-5).
- [x] JSON conventions in `docs/API.md` (camelCase, `{"error": ...}`, 401 JSON).
- [x] Test style: `httptest` handler tests, always against a real PostgreSQL 16 (DEC-1, DEC-24). Each test package gets its own database cloned from a migrated template, so tests stay fast and isolated.
- [x] The "no Python" rule and the AGENTS.md habit of writing rules down.

**Stack note (DEC-22).** Language and frameworks are open, as long as the choice is not Python and stays very simple to deploy. Go + Gin + GORM + React is kept by default: it is one static binary, the skeleton already works, and nothing clearly better and simpler exists for this shape of app. One alternative is worth knowing: `sqlc` + `pgx` (SQL-first, generated Go) would fit a Postgres-heavy schema (ranges, exclusion constraints, `SKIP LOCKED`) better than an ORM. It is not worth the churn now. GORM stays for simple CRUD; hand-written SQL through GORM's `Raw`/`Exec` is fine for Postgres-specific queries, and goose migrations (not GORM tags) own the schema (C1).

### 3.4 Remaining work, not installed functionality

M1.14 is merged; its delivery references are PRs #129, #130 and [#131](https://github.com/grierj/fold/pull/131). Calendar/feed merged in PR #129 (`891ce26`), and series RSVP/Undo merged in PR #130 (merge `7229e92`, reviewed head `5f927ce`) after the publication repairs at `30d48c3`, `09b1260` and `5f927ce`. Departure source `deb093d` passed all 12 fresh gates and independent publication review; PR #131 merged as `a18902e` on Oct 27, 2026 after the protected `test`, `web` and `provision` checks passed. Rooms, jobs, messaging relay/composer, scheduled reminders, SMS, notification preferences/digests, encrypted provider settings, SPA setup/CLI, CSV import, admin TOTP and private assets/S3 are future work. Published release images, the portable backup container and the pre-migration backup gate also remain future work. The DigitalOcean guide already has host-cron backups and upgrade scripts; that is not completion of M5.1–M5.4.

Sections 5–13 describe the target design as well as current behavior. Future tables, endpoints and commands there are not an installed API. Use `docs/API.md` for shipped HTTP behavior and `docs/DEPLOY-DIGITALOCEAN.md` for runnable deployment instructions. Known gaps and unresolved product conflicts are in [DOCUMENTATION-REVIEW.md](DOCUMENTATION-REVIEW.md).

## 4. Architecture change index

Stable C-identifiers connect architectural rationale to milestones. Foundation changes are largely complete (section 3 and STATUS); this table is not a fresh task list. DEC-22 permits a pre-live clean break; the later expand/contract rule in 14.1 governs live changes.

| # | Change | Why | Milestone |
|---|---|---|---|
| C1 | Versioned migrations from a **clean baseline** (embedded SQL files run by `pressly/goose`, PostgreSQL only). Migrations are the single source of truth for the schema; GORM models must match them. CI checks for drift by running GORM `AutoMigrate` for real against a throwaway Postgres database that goose has just migrated, with a logger that records every statement, and fails on any `CREATE` or `ALTER`; the database is then dropped. (GORM's `DryRun` can't do this: the migrator must query the database, for example `HasTable` and `ColumnTypes`.) `AutoMigrate` never runs against a real church database. The baseline was regenerated once before the first church (DEC-138); it is frozen like any other applied migration from the first tagged release on. | Self-hosters upgrade unattended, so schema changes must be explicit and testable. One source of truth avoids drift between tags and SQL. The baseline is the new schema, not the skeleton's, because there are no installs (DEC-22). | M0.1 |
| C2 | Expand recurrence in the church's time zone, not UTC. Store wall-clock start time + IANA `tzid` on each event. | Today `api_meetings.go` builds `DTSTART` in UTC. A weekly 7:00 pm meeting moves to 6:00 or 8:00 pm when daylight saving time changes, and `BYDAY` can land on the wrong local day (docs/API.md admits this). | M1.1a |
| C3 | Replace `Visibility` + `InviteOnly` with one enum: `public`, `private`, `invite_only`. Use the same enum on events. Update the SPA (`inviteOnly` in `groupSummaryJSON` in `api.go`, `web/src/types.ts:31`) and `docs/API.md` in the same PR. | The bool pair allows invalid states and is awkward to reuse on events, feeds, rooms, and messaging. | M0.2 |
| C4 | Rename `Meeting` to `Event`. Let an event belong to a group or to no group (a church-wide event). Add stored `EventOccurrence` rows for a rolling horizon. | Grier's features are about events (Meetup), and church-wide events (Christmas Eve, a potluck) have no group. Waitlists, room bookings, check-in, and messages need per-occurrence state, and room-conflict queries cannot run on RRULE strings in SQL. | M1.1a, M1.1b |
| C5 | Guests are `User` rows with `status=guest` and login disabled. | F1 says non-members can sign up for public events. Reusing `User` keeps registrations, consent, and messaging keyed by one `user_id`. When a guest later gets an account, the same row is upgraded (same email). | M1.4 |
| C6 | Transactional email early, with a **minimal sender**: the `devoutbox` (dev and tests) and one SMTP sender configured from env. The provider registry, `ProviderConfig`, and encrypted secrets come later (M3.2, M4.1). | Invites, password reset, and guest confirmations need email before the messaging system exists. Keeping the first sender small keeps M1.2 from depending on jobs and secrets. | M1.2 |
| C7 | An in-process job runner backed by a `jobs` table (no Redis, no separate worker container), added when messaging first needs it. Claim with `FOR UPDATE SKIP LOCKED`; poll every 1 to 2 seconds. No `LISTEN/NOTIFY`. Before M3.1, the few periodic tasks (occurrence extension, age rollover, token cleanup) run from a simple in-process daily ticker. | Scheduled sends, reminders, and webhook processing need retries and scheduling. A table keeps the dependency count at zero. Polling is simpler than notifications and fast enough for a church. | M3.1 |
| C8 | Store provider settings in the database, with secrets encrypted by AES-GCM using `FOLD_SECRET_KEY`. Env vars, if set, override the database. Added when the setup wizard first needs it. | The setup wizard must configure providers by API (D7). A separate key (not `SESSION_SECRET`) lets you rotate sessions without losing provider secrets. | M4.1 |
| C9 | Split uploads into public and private assets. Private assets are served through an auth-checked handler, or by short-lived presigned URLs on S3. | Today every upload is public at `/uploads/<key>`. That is fine for avatars and public covers, not for photos of children or invite-only groups. | M4.7, M6.9 |
| C10 | Publish prebuilt multi-arch images to GHCR. In compose, use `image: ghcr.io/grierj/fold:${FOLD_VERSION}` and keep `build: .` in a dev override file. | A 1 to 2 GB VPS can run out of memory building the node and Go stages. Pulling an image also makes upgrades one command, and Render can run the same image (12.5). | M5.1 |
| C11 | Grow the binary's subcommands: `serve`, `healthcheck`, `migrate`, `setup`, `providers`, `user`, `storage`, `secrets`, `version`. Load config lazily: each subcommand loads only what it needs (`version` needs nothing; `migrate status` needs only `DATABASE_URL`). | Agents and power users need a scriptable, non-browser path for every setup step (D4, D5). Distroless has no shell, so the binary must do the work. Backups run in the `backup` container instead (11.8). | M0.1, M4.8 |
| C12 | Leadership comes only from `Membership.role=leader`; several leaders per group. Replace every `Group.LeaderID` permission check with the policy package. Keep `LeaderID` only as the displayed primary contact. | Churches often have co-leaders. One source of truth for leadership keeps permissions correct. No backfill is needed (DEC-22). | M0.2 |
| C13 | Update AGENTS.md: SMS goes "through SMS provider adapters (Telnyx default, Twilio next)", not "via Twilio". Fix the README's Go version. The rename to `fold` is **already done upstream** in PR #7 (DEC-12). | Requirement D8. | M0.1 |
| C14 | PostgreSQL only, everywhere (DEC-1, DEC-24). Remove `glebarez/sqlite`, `SQLITE_PATH`, and every SQLite branch. Development and tests use `deploy/docker-compose.dev.yml` (Postgres 16 on localhost). Tests read the same `DATABASE_URL` as the app; `make test` starts the dev database if it is unset, and CI uses a Postgres service container. | One database means one test matrix and real Postgres features: `FOR UPDATE SKIP LOCKED` for jobs, exclusion constraints on `tstzrange` (with `btree_gist`) for room conflicts, `citext` for emails, `jsonb` for audiences and settings, partial unique indexes, and advisory locks for migrations. Contributors already need Docker to run the stack. | M0.1 |
| C15 | Accounts come from three doors: self-signup creates an **account request** that an admin approves; invitations from leaders and admins create accounts without approval; "invite a friend" from a plain member creates an account request too (DEC-4). | Today admins create every user. Grier wants self-signup with admin approval. | M1.2, M1.3 |
| C16 | Log in by **email** (DEC-23). Drop `Username`. `User.email` is `citext`, NOT NULL, and unique. `password_hash` is nullable (invited people, guests, and pending accounts have none yet). A shared email is one account; the other people on it are household people (5.10). | Members know their email, not a username, and email is how resets and invitations already work. The skeleton's `Email ""` default could not carry a unique index anyway. | M0.3 |
| C17 | Request plumbing: trust only the reverse proxy (`FOLD_TRUSTED_PROXIES`, defaulting to the compose network's fixed subnet), so `ClientIP()` is the real client. Register `/hooks/*` and the one-click unsubscribe `POST` outside the CSRF group; they authenticate with provider signatures or signed tokens instead. | Per-IP throttling and audit IPs are wrong today. Webhooks and RFC 8058 one-click requests come from other origins without a CSRF token, so the global middleware would reject them. | M0.1 |

