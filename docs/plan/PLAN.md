# Fold: Architecture and Development Plan

Status: draft v4, updated 2026-10-09 with Grier's decisions (section 15, DEC-1 to DEC-37) and the Codex reviews (`REVIEW.md`, including its final review), for Grier (owner) and for AI coding agents.
Repo: https://github.com/grierj/fold (first read at commit `aa992e2`; re-checked at `e7c0e8f`, "Merge pull request #7 from grierj/admin-invite-and-rename", merged 2026-10-09 4:31 PM MT).

How to use this document:

- People: read sections 1 to 4 for the shape of the project, section 12 for the vendor and hosting picks, and section 15 for Grier's decisions (nothing is open).
- Agents: read section 3 (what exists), section 5 (architecture), and section 6 (permissions). Then pick one milestone in section 14. Each milestone lists its scope, acceptance criteria, and tests. Do one milestone per work session and one PR per milestone. Do not start a milestone until the ones it depends on are merged.
- Precedence: Grier's decisions (section 15, tagged DEC-1 to DEC-37) win over the original requirement wording and over anything in the current repo, including `AGENTS.md` and `README.md`. When the repo disagrees with this plan, change the repo in the milestone that owns the item.
- Clean break (DEC-22): no church runs Fold yet. Nothing in the current repo's data or schema has to be preserved. Any part of the skeleton may be changed if the result still meets the requirements in section 2.
- Words like MUST and MUST NOT are hard rules. "Should" means the default; you may deviate if you write down why in the PR.

---

## Table of contents

1. Goals and non-goals
2. Grier's requirements (traceability)
3. The current repo and what to keep
4. Recommended changes to the skeleton (and why)
5. Architecture
6. Permissions matrix
7. Messaging pipeline
8. Calendar and ICS feeds (F2)
9. Rooms and the room-suggestion rule (F3, F4)
10. Security and privacy
11. Deployment, setup wizard, upgrades, backups
12. Third-party services and hosting
13. Extra features from research
14. Phased roadmap and milestones
15. Decisions (Oct 9, 2026) and remaining open questions
16. Appendix A: AGENTS.md "Deployment" section outline
17. Appendix B: Sources and unverified facts

---

## 1. Goals and non-goals

### Goals

1. Give a church one place for groups, events, RSVPs, rooms, and group messaging (email and SMS). It replaces the social and group parts of ChurchTrac and borrows Meetup's events-and-groups model.
2. Self-host first. A church member who is comfortable with a Linux VPS can install it in under an hour with `docker compose`. A VPS with Compose is the only supported deployment for now; a managed platform (Render) is a later, on-request option (12.5, DEC-26).
3. Single-tenant. One deployment serves one church. Fold is free, open-source (MIT) give-away software (DEC-11). "Hosted" means someone (Grier, rarely, or anyone) runs one deployment per church. Hosting terms and any fee are agreed case by case and are not meant to make money (DEC-10). The church brings and owns its own email, SMS, and hosting accounts, even when hosted, unless there is a special agreement (DEC-2).
4. Few external dependencies. Only email, the host, and (for texting) SMS are needed outside the box. The only database is PostgreSQL, run by Docker Compose in production, development, and tests (DEC-1, DEC-24). Everything else (object storage, video, DNS automation) is optional.
5. Every external dependency can be set up by API: by the app's setup wizard, by the `fold` CLI, or by an AI agent following `AGENTS.md`. A power user can still do every step by hand.
6. Every proprietary dependency sits behind an adapter interface. Build one real adapter per kind first (plus the `devoutbox` for tests), and add the second when it is needed. By v1.0 each kind has at least two working options (for example SMTP and Postmark for email; Telnyx and Twilio for SMS).
7. Familiar UI. Copy Meetup patterns (group cards, date badges, RSVP buttons), as the repo already does.

### Non-goals

- Multi-tenant SaaS (one database for many churches). The repo already rules this out, and this plan keeps it that way.
- Giving, donations, pledges, fund accounting, contribution statements. See 13.3 for the reasons.
- SQLite or any database other than PostgreSQL (DEC-1, DEC-24).
- Migrating data from the current skeleton (DEC-22). There are no installs to upgrade.
- Python, anywhere in the repo or its tooling (repo rule; DEC-22).
- Accounts for anyone under 13. Younger children exist only as household people with no login (DEC-8).
- Teens as group leaders or event hosts (DEC-18).
- Translations and an i18n framework. Plain English strings in the code are fine (DEC-13).
- Embedded video calls inside Fold. Link-out and generated Jitsi links only, for now (DEC-16).
- Making money from hosting (DEC-10).
- Amazon Web Services as a recommended dependency (DEC-21).
- Worship planning, song libraries, website builder, native app builder, voice phone trees.
- Full child check-in kiosks with label printing in v1. These come later, if at all (section 13).
- Running background checks. Fold stores only a status flag that an admin enters (10.3).
- Paid event tickets in v1. A leader can link out to the church's existing payment page.

---

## 2. Grier's requirements (traceability)

Each requirement maps to the sections and milestones that satisfy it. Agents: if your change would break one of these, stop and ask.

| # | Requirement | Where it is handled |
|---|---|---|
| D1 | Self-host first | 11, 12.5, M5.x |
| D2 | Single-tenant SaaS; hosted = one deployment per church. Refined by DEC-2 and DEC-10: the church owns its provider and hosting accounts; terms and fees are case by case and not for profit | 1, 11.6, DEC-2, DEC-10 |
| D3 | Minimal external dependencies (one database: PostgreSQL, DEC-1) | 5.1, 12, C14 |
| D4 | Every dependency manageable by AI agents; AGENTS.md Deployment section | 11.4, 11.5, Appendix A, M5.4 |
| D5 | No added deployment complexity; a power user can do it by hand | 11, 12.5, docs/DEPLOY.md (M5.4) |
| D6 | External deps limited to text, email, simple hosting where possible | 5.1, 12 |
| D7 | Every dependency has API configuration; the app does as much setup as it can | 11.3, M4.x |
| D8 | Proprietary deps have multiple options via adapters (one first, the second when needed; at least two per kind by v1.0) | 5.6, 12 |
| F1 | Events and groups with public / private / invite-only visibility; leaders have broad powers over their groups' events (DEC-6, DEC-17); admins see everything (DEC-5); self-signup needs admin approval (DEC-4, DEC-30); teens need consent and are badged (DEC-8, DEC-20); leaders reach teens and hidden adults through Fold (DEC-31); links of minors to adults need admin approval (DEC-32); log in by email (DEC-23) | 5.3, 5.10, 5.11, 6, M0.2, M0.3, M1.1a, M1.1b, M1.2, M1.3, M1.9, M1.10, M1.11 |
| F2 | All events on a timeline and by room, plus ICS feeds | 8, M1.6, M1.7, M2.2 |
| F3 | Room management, optional | 9, M2.1 |
| F4 | Room suggestions from past use, filtered by visibility and type | 9.3, M2.3 |
| F5 | Email and SMS to an event's registrants, a set of events, or all members; replies relay through Fold (DEC-31) | 7, 7.8, M3.4, M3.5, M3.7, M3.11 |
| F6 | In-browser composer for SMS and email with scheduling; rich text for email | 7.3, M3.4 |
| F7 | Static assets local or in an optional block store | 5.7, M4.7 |
| F8 | Leaders message only their own groups (and hosts only their own church-wide events, DEC-7); admins message everyone | 6, 7.2 |
| F9 | Members can request a group SMS; the leader or any admin approves | 7.6, M3.9 |
| F10 | Optional slot limits and waitlist with auto-promotion | 5.4, M1.5 |
| F11 | Video chat facilitation (link-out or Jitsi link only, DEC-16) | 5.8, M1.8 |
| F12 | Remote attendance flag | 5.3, M1.8 |
| F13 | Childcare available flag | 5.3, M1.8 |

The decisions in section 15 (DEC-1 to DEC-37) refine these requirements. Where a decision and the wording above differ, the decision wins.

---

## 3. The current repo and what to keep

### 3.1 What is there today

The skeleton is a working, tested app built in PRs #1 to #6 (all merged 2026-10-06/07). It is about 10,000 lines. PR #7 (merged 2026-10-09) renamed the app to `fold` and lets admins see invite-only groups; see 3.4.

Stack:

- Go 1.25 (`go.mod` says `go 1.25.0`; the README still says "Go 1.24+" at `e7c0e8f`, which is stale). Gin for HTTP, GORM for data.
- Postgres 16 in production (`DATABASE_URL` is required unless `APP_ENV=dev`). SQLite in dev and tests through the pure-Go `glebarez/sqlite` driver, so no CGO. The plan removes SQLite entirely (DEC-1, DEC-24, C14).
- React 18 + Vite 6 + Tailwind 3 + React Router 7 single-page app in `web/`. It builds to `web/dist` and is embedded in the Go binary with `go:embed`. `web/package.json` has only `build`, `dev`, and `preview` scripts: there is no frontend test runner and no Playwright yet (M0.4 adds them).
- `teambition/rrule-go` for recurrence. `gorilla/sessions` cookie sessions, bcrypt passwords, hand-rolled synchronizer CSRF (`csrf.go`), applied globally (`main.go:147-148`).
- Accounts log in by **username** (`api_auth.go`: `WHERE username = ?`, throttled by username). `models.User` has `Username` (unique, not null), `Password` (not null), and `Email` (defaults to `""`). DEC-23 replaces this with email login (M0.3).
- `main.go:129` calls `SetTrustedProxies(nil)`, so behind Caddy every request's client IP is Caddy's. M0.1 fixes this.
- `LoadConfig()` runs before subcommand dispatch (`main.go:48`), so every subcommand needs the full production config.
- Docker: multi-stage build (node, then go, then distroless static nonroot). `docker-compose.yml` runs `db` (postgres:16-alpine), `app`, and `caddy` (caddy:2-alpine, automatic HTTPS). Named volumes `pgdata`, `uploads` (mounted at `/data`), `caddydata`, `caddyconfig`. There is no Makefile.
- GitHub Actions: `go test ./...` on SQLite and on Postgres 16, plus `npm ci && npm run build`. (The SQLite job goes away in M0.1.)
- MIT license, copyright Grier Johnson.

Layout (flat `package main` at the root):

- `main.go`: wiring, first-run setup page (server-rendered `templates/setup.html`), SPA serving, `/healthz`, `healthcheck` subcommand.
- `api.go`, `api_auth.go`, `api_groups.go`, `api_meetings.go`, `api_profile.go`, `api_admin.go`, `api_notifications.go`: JSON API under `/api/v1`.
- Module and binary named `fold` since PR #7 (they were `church-groups`).
- `models/models.go`: `User`, `Group`, `Meeting`, `MeetingException`, `Membership`, `RSVP`, `Setting`.
- `storage/storage.go`: a `Store` interface with `LocalStore`. Its comments describe an S3 seam.
- `docs/API.md`: the API contract.
- Tests: `auth_test.go`, `behavior_test.go`, `admin_test.go`, `profile_test.go`, `storage/storage_test.go`.

Features already built:

- Local accounts. Admins create users. There is no self-signup and no password reset by email.
- Groups with a `pending` approval workflow. Visibility is `public` or `private`, plus an `InviteOnly` bool that is only valid on private groups. Tags, cover image with focal point, leader transfer, deletion requests.
- Meetings belong to a group, with an optional RRULE. Single instances can be cancelled with `MeetingException`. Per-instance RSVPs (going / maybe / declined), addressed by `(meetingId, date)`.
- Profiles with avatar upload or fetch-from-URL (SSRF-guarded), focal points, and client-side image compression.
- Admin panel: pending groups, deletion requests, users, memberships.
- Derived notifications (not stored), dismissed in the browser's localStorage.

### 3.2 Goals already written in the repo

From `AGENTS.md` and `README.md`:

- "Deployment simplicity is the top concern." Always one Go binary that serves the API and the SPA. No separate frontend hosting.
- Single-tenant; "SaaS" means many single-tenant deployments. No multi-tenant data model.
- No Python, ever. Shell or Go for tooling.
- Postgres-only features go behind flags so SQLite dev keeps working. **Superseded by DEC-1 and DEC-24**: PostgreSQL everywhere, so no flags are needed.
- OIDC (Microsoft Entra ID and Google) later, behind an `AuthProvider` seam.
- The group approval workflow is core and must survive refactors.
- Meetup is the UI reference.
- SMS is a hard requirement for "Phase 2". It should sit behind a `Notifier` interface in `internal/notify/`, broadcast-only in v1, with replies routed to the group leader. **Corrected**: the line "Group SMS via Twilio is a hard requirement" (still in `AGENTS.md` at `e7c0e8f`) was a mistake. SMS goes through provider adapters with Telnyx as the default and Twilio as the second adapter, none required (C13).
- Versioned SQL migrations in `migrations/` once the first destructive change is needed. **Now** (C1): a clean baseline in M0.1.

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

### 3.4 Repo items that conflict with Grier's requirements (status at `e7c0e8f`)

| Item | Status | Fixed by |
|---|---|---|
| Admins could open invite-only groups by id but did not see them in lists | **Fixed upstream** in PR #7 (merged 2026-10-09): admins now see every group (DEC-5) | done; M0.2 keeps it under policy tests |
| Name `church-groups` (module, binary, image paths, DB name, cookie) | **Fixed upstream** in PR #7 (DEC-12) | done; nothing to upgrade (DEC-22) |
| `AGENTS.md`: "Group SMS via Twilio is a hard requirement" | Still present; Grier confirmed it was a mistake | C13, M0.1 |
| README says Go 1.24+; `go.mod` says 1.25.0 | Still present | C13, M0.1 |
| SQLite dev and test path (`glebarez/sqlite`, `SQLITE_PATH` in the production Dockerfile, AGENTS.md test rules) | Conflicts with DEC-1 and DEC-24 | C14, M0.1 |
| Login by username; `Email` defaults to `""`; `Password` is required | Conflicts with DEC-23 | C16, M0.3 |
| `SetTrustedProxies(nil)`: every client IP is Caddy's, so per-IP throttling cannot work | Open | C17, M0.1 |
| Global CSRF middleware would reject provider webhooks and RFC 8058 one-click unsubscribe POSTs | Open | C17, M0.1 |
| `LoadConfig()` before dispatch: `fold version` needs `SESSION_SECRET` | Open | C11, M0.1 |
| No email: no invites, password reset, guest or self-signup; admins create every user | Open | C6, C15, M1.2, M1.3 |
| Visibility is a bool pair, and only on groups | Open | C3, M0.2 |
| Leadership is checked through `Group.LeaderID` (about 16 places, for example `api_groups.go:543`) | Open | C12, M0.2 |
| Meetings must belong to a group (no church-wide events) | Open | C4, M1.1b |
| Recurrence expanded in UTC (wrong times across DST) | Open | C2, M1.1a |
| All uploads are public | Open | C9, M4.7 |
| Compose builds the image on the VPS | Open | C10, M5.1 |
| Backups are manual `pg_dump` only | Open | M5.2 |
| Setup asks only for the site name and admin | Open | M4.2 to M4.6 |
| Config is env-only | Open | C8, M4.1 |
| No frontend tests (`npm test` does not exist) | Open | M0.4 |

There is nothing to upgrade from: PR #7's database and cookie renames need no upgrade note, because no church runs Fold (DEC-22). `docs/UPGRADING.md` starts with the first release churches can run (v0.5).

---

## 4. Recommended changes to the skeleton (and why)

These are the main places this plan departs from the skeleton. Each one has a milestone. Under the clean break (DEC-22), none of them needs a data migration or a fixture test for old data.

| # | Change | Why | Milestone |
|---|---|---|---|
| C1 | Versioned migrations from a **clean baseline** (embedded SQL files run by `pressly/goose`, PostgreSQL only). Migrations are the single source of truth for the schema; GORM models must match them. CI checks for drift by running GORM `AutoMigrate` for real against a throwaway Postgres database that goose has just migrated, with a logger that records every statement, and fails on any `CREATE` or `ALTER`; the database is then dropped. (GORM's `DryRun` can't do this: the migrator must query the database, for example `HasTable` and `ColumnTypes`.) `AutoMigrate` never runs against a real church database. | Self-hosters upgrade unattended, so schema changes must be explicit and testable. One source of truth avoids drift between tags and SQL. The baseline is the new schema, not the skeleton's, because there are no installs (DEC-22). | M0.1 |
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

---

## 5. Architecture

### 5.1 Components

```
                 Internet
                    |
             +------+------+
             |    Caddy    |  automatic TLS (Let's Encrypt), reverse proxy
             +------+------+
                    |
   +----------------+-----------------------------------------------+
   |  fold (single Go binary, distroless)                           |
   |                                                                |
   |  HTTP: /api/v1/*  JSON API        /cal/*  ICS feeds            |
   |        /hooks/*   provider webhooks (signed)                   |
   |        /u/*       unsubscribe + preference pages               |
   |        /legal/*   generated privacy + SMS terms pages          |
   |        /setup     first admin page     /* SPA + setup wizard   |
   |                                                                |
   |  Core:   policy (permissions) | events + occurrences | rooms   |
   |          messaging (compose, audience, queue) | consent        |
   |  Jobs:   in-process worker over the `jobs` table               |
   |  Ports:  EmailProvider | SMSProvider | Store | VideoLinker     |
   |          DNSProvider (optional)                                |
   +------+--------------------------+------------------------------+
          |                          |
   +------+------+          +--------+---------+     Outbound HTTPS to:
   | Postgres 16 |          | uploads volume   |     - email provider (required)
   | (pgdata)    |          | (/data)          |     - SMS provider (optional)
   +------+------+          +------------------+     - S3-compatible store (optional)
          |                                          - Cloudflare DNS API (optional)
   +------+---------------+
   | backup (postgres     |  fold-backup image; nightly pg_dump + tar of uploads
   | image, shell loop)   |  into the `backups` volume (added in M5.2)
   +----------------------+
```

Needed outside the box: one email provider and a host (a VPS or a church-owned server). SMS is needed for the SMS features, but the app runs without it. Everything else is optional. The defaults are local disk and link-out video.

### 5.2 Code layout (target)

Keep `package main` at the root for HTTP wiring. Put new logic in packages that agents can test on their own:

```
/                      main.go, api_*.go (HTTP handlers only; thin)
/models                GORM models (must match the migrations; the CI drift check, C1)
/migrations            NNNN_name.sql (goose), embedded; the schema's source of truth
/internal/policy       canSee / canManage / canMessage ... pure functions + table tests
/internal/events       occurrence generation, capacity, waitlist, removals, ICS rendering
/internal/rooms        bookings, conflicts, suggestions
/internal/messaging    composer model, audience resolution, send pipeline, SMS send window
/internal/consent      opt-in/opt-out, revocation keywords, suppression, unsubscribe tokens
/internal/jobs         jobs table, worker, scheduler (from M3.1)
/internal/mail         minimal transactional sender (devoutbox + env SMTP) used before M3.2
/internal/providers    interfaces + registry (from M3.2)
/internal/providers/email/{devoutbox,smtp,postmark}        later: resend, mailgun
/internal/providers/sms/{devoutbox,telnyx,twilio}          later: plivo
/internal/providers/dns/{manual,cloudflare}
/internal/providers/video/{link,jitsi}                     later: zoom
/internal/secrets      AES-GCM settings encryption (from M4.1)
/internal/setup        setup state and wizard steps, shared by the SPA wizard and `fold setup`
/internal/accounts     email login, account requests, invitations, age gate, households, household people, guardian links, teen consent
/storage               Store interface, LocalStore, S3Store
/web                   SPA (Vitest unit tests; Playwright browser tests in /web/e2e)
/deploy                docker-compose.yml, docker-compose.dev.yml (Postgres for dev and tests), Caddyfile, .env.example, init-env.sh, backup/ (backup.sh, restore.sh)
/docs                  API.md, DEPLOY.md, UPGRADING.md, PROVIDERS.md, PRIVACY.md, MEMBERS.md (guidance churches give members)
Makefile               new: dev, test, lint, e2e
```

### 5.3 Data model

The field lists show the important columns, not every column. Every table has `id`, `created_at`, `updated_at` unless noted. Times are stored in UTC; wall-clock fields say so. The baseline migration (M0.1) creates the new schema directly; there is no conversion from the skeleton's tables (DEC-22).

**Site and settings**

- `Setting(key, value)`: exists. Keys include `church_name`, `timezone` (IANA, required), `church_address` (for email footers and legal pages), `rooms_enabled` (bool), `self_signup` (`off|request`, default `request`; there is no "open" mode, DEC-4), `sms_send_window_start` (default `08:00`), `sms_send_window_end` (default `20:00`; both read in each recipient's local time, DEC-9, DEC-25), `sms_registration_path` (`ein_10dlc|ein_toll_free|sole_prop_10dlc|sole_prop_toll_free`, DEC-3), `guardian_copies` (bool, default false; email copies only, DEC-19, DEC-29), `jitsi_domain`.
- `SetupState(step, status pending|done|skipped, data_json, updated_at)`: one row per wizard step. The first-run gate and the SPA wizard read this table, not a cached "any user exists" flag (M4.2).
- `ProviderConfig(kind, provider, settings_json, secrets_enc, status, verified_at, last_error)` (from M4.1). `kind` is `email|sms|storage|video|dns|backup_offsite`. One active row per kind; email may have a second row marked `fallback`.

**People**

- `User`:
  - `email` (`citext`, NOT NULL, unique): the login (DEC-23). One email is one account.
  - `password_hash` (nullable): empty until the person sets a password from an invitation or approval link. Guests never have one.
  - `name`, `phone` (nullable), `avatar_key`.
  - `role`: `admin|member`.
  - `status`: `active|invited|guest|pending_consent|disabled`. `pending_consent` is a teen account waiting for consent (DEC-8).
  - `account_type`: `adult|teen`. Nobody under 13 gets a `User` row (DEC-8).
  - `birth_date`: required for teens, so Fold knows when they turn 18. Adults only confirm "I am 18 or older"; their birth date is optional.
  - `timezone`: nullable IANA zone for SMS send windows (DEC-9, DEC-25). If empty, Fold uses the church time zone.
  - `login_enabled` (bool), `email_verified_at`, `phone_verified_at` (set only after the SMS ownership check, 7.4), `session_version` (bumped to end other sessions, 10.1), `pending_email` and `pending_email_token_hash` (during a verified email change, 10.1), `totp_secret_enc` and `totp_enabled_at` (admins, M4.9), `directory_prefs_json` (later).
  - `hide_contact_from_leaders` (bool, default false; adults only, DEC-31). Teens are always hidden from leaders and hosts, whatever this says (5.11).
  - Sign-in requires `status=active`, `login_enabled=true`, and a password hash. Guests are `status=guest, login_enabled=false`; a guest who signs up again with the same email merges into the same row.
  - There is no `username` column (DEC-23).
- `AccountRequest(name, email, phone, account_type, birth_date?, message, source self_signup|member_invite|guest_upgrade, requested_by?, group_id?, status pending|approved|rejected|expired|withdrawn, decided_by, decided_at, decision_note, expires_at)`. Only admins approve. Approval creates or upgrades the `User` and emails a set-password link. Requests expire after 30 days (DEC-4). A request for an email that already has an account gets the same on-screen answer as any other request ("Thanks; check your email"), and the email says "you already have an account; sign in or reset your password". The form never reveals whether an email is registered (10.1).
- `Invitation(email, phone, invited_by, role, account_type, group_id?, token_hash, expires_at, accepted_at)`. Created only by admins and leaders. A leader's invitation may name only a group they lead. Accepting creates an active account with no admin approval, except that a teen account starts `pending_consent` (DEC-4, DEC-8). A leader may send at most 20 invitations a day (default, set by admins; 10.1).
- `PasswordReset(user_id, token_hash, expires_at, used_at)`.
- `Household(name, address?, created_by)`.
- `HouseholdAccount(household_id, user_id, relation guardian|adult|teen)`: people in the household who have their own account. An account belongs to at most one household.
- `HouseholdPerson(household_id, first_name, last_name, kind child|teen|adult, birth_date?, care_notes, photo_consent, created_by)`: people with **no login, email, or phone** (DEC-8, DEC-23). They are:
  - `child`: under 13. Children never have accounts.
  - `teen`: 13 to 17 without their own email; registered by a guardian.
  - `adult`: an adult who shares the account holder's email, for example a spouse (5.10).
  - `care_notes` (allergies, medical, custody notes) are visible only as section 6 allows. A `HouseholdPerson` can later be turned into their own account once they have their own email (an admin or the account holder starts it; registrations move with them). For a teen, the new account's guardian link still needs admin approval (DEC-32).
- `GuardianLink(guardian_user_id, teen_user_id?, person_id?, status pending|confirmed|rejected|revoked, confirmed_by, check_note, created_at)`. Exactly one of `teen_user_id` or `person_id` is set. Every link to a teen or child starts `pending`, and only an admin can confirm it (DEC-32). That covers a guardian adding a child or teen household person, turning a household teen into an account, a teen naming a guardian at self-signup, and anyone claiming an existing teen. There is no auto-confirm path. The confirming admin writes how they checked in `check_note` (for example "known family; parent came to the office"). Until a link is confirmed, the adult can't grant consent, register the child or teen, or get copies, and leaders see nothing about the person. Adding a teen account to a `HouseholdAccount` follows the same rule. Adult household people need no approval.
- `TeenConsent(teen_user_id, scope account|sms|photos, status granted|revoked, granted_by, granter_role guardian|admin, evidence_text, at)`. Append-only; the latest row per `(teen, scope)` wins. `account` consent turns a teen from `pending_consent` to `active`. `evidence_text` is required when an admin grants, for example "paper consent form on file, signed 2026-10-12". The daily age job turns teens into adults on their 18th birthday and keeps the consent history.

**Groups**

- `Group`: `visibility` (`public|private|invite_only`; replaces `Visibility` + `InviteOnly`), `status` (`pending|active|archived`), `leader_id` (displayed primary contact only; never used for permissions, C12), `category` (from a short admin-defined list, for example "Small group", "Ministry", "Class", "Youth"). Joining: any member who can see a public or private group may join it at once; invite-only groups are joined only by invitation. There is no join-request step; leaders remove members who shouldn't be there (DEC-30). There is no teen restriction on groups (DEC-20).
- `Membership`: `role` = `leader|member`; several leaders allowed; this is the only source of leadership. `status` = `active|invited|removed`. `removed` means a leader or admin removed the person (DEC-35): they can't rejoin on their own until a leader or admin adds them back, and the removal is in the `AuditLog`. A teen account can never have `role=leader` (DEC-18).

**Events**

- `Event` (renamed from `Meeting`):
  - `group_id` (nullable; null = a church-wide event, created only by admins and run by its assigned hosts, see `EventHost`; DEC-7)
  - `title`, `description_html` (sanitized), `category` (same list as groups; used by room suggestions)
  - `visibility` (`public|private|invite_only`) and `visibility_source` (`group|custom`). With `group` (the default), the event follows the group's visibility, including later changes. With `custom`, the event keeps its own value (DEC-6, DEC-17):
    - A leader of the event's group, or an admin, may set any of the three values on a group event. For example, an invite-only group's event can be made `private` (all signed-in members) or `public`.
    - A change that makes the event more open than its group writes an `AuditLog` row `event.visibility_override` (old value, new value, group visibility, optional `visibility_reason`; a reason is never required, DEC-28) and notifies the group's other leaders and the admins. Going back to `group` or to a stricter value is also audited.
    - An event of an invite-only group that is `private` or `public` never names or links the group to people outside it. Its page shows "Hosted by <church name>" and names no leaders (people reach them with "Message the organizers", 7.8). Only the event's leaders and the admins see attendee names; everyone else sees a count. The group stays 404 to outsiders. Registering for the event does not add anyone to the group.
    - Church-wide events may be `public` or `private` only. Their hosts and admins may switch between the two.
  - `starts_local` (wall clock, for example `2026-11-04T19:00`), `duration_minutes`, `tzid` (defaults to the church time zone), `rrule` (optional), `rrule_until`
  - `location_text`, `default_room_id` (nullable)
  - `capacity` (nullable = unlimited), `waitlist_enabled` (bool), `guests_allowed` (0 to 5 extra unnamed people per registration)
  - `remote_available` (bool), `remote_capacity` (nullable), `video_provider` (`none|link|jitsi`), `video_url`, `video_reveal` (`always|registered_only`, default `registered_only`)
  - `childcare_available` (bool), `childcare_notes`, `childcare_capacity` (nullable), `childcare_ages` (text)
  - `rsvp_opens_at`, `rsvp_closes_at` (nullable)
  - `status` (`draft|published|cancelled`), `created_by`
  - There is no teen or youth-only flag on events: teens may register for any event they can see (DEC-20).
- `EventHost(event_id, user_id, added_by, created_at)`. Every church-wide event must have at least one host. The admin who creates it is the default host, and they can swap in other hosts. Hosts are often admins but can be any adult member; never a teen (DEC-7, DEC-18). Hosts get leader rights over that event only (section 6). Group events use the group's leaders; `EventHost` rows on group events are not used in v1.
- `EventOccurrence(event_id, original_date, starts_at, ends_at, status scheduled|cancelled, title_override, location_override, capacity_override, starts_at_override?, duration_override?)`. Unique `(event_id, original_date)`. Occurrences are generated 18 months ahead and extended daily. `MeetingException` becomes `status=cancelled` on the occurrence.
- Series edits (v1 rule): an edit to the series applies to **all future occurrences**; a change to one date is a per-date override on that occurrence. There is no "this and following" split in v1. When a series edit changes times:
  - Future occurrences without overrides move to the new times; their registrations stay.
  - Occurrences whose date no longer exists in the new rule are cancelled, and their registrants are told.
  - Each moved occurrence's room bookings move with it (`RoomBooking.starts_at`, `ends_at`, and `padded_range` are recomputed in the same transaction). A booking that would now conflict is set to `requested` instead of failing the edit, and the leader or host is told which dates lost their room (9.2).
- `Registration` (renamed from `RSVP`): `occurrence_id`, `user_id` (nullable), `household_person_id` (nullable; exactly one of the two is set), `registered_by` (the account holder, for household people), `status` (`going|maybe|declined|waitlisted|cancelled|removed`), `attendance_mode` (`in_person|remote`), `guest_count`, `childcare_count`, `childcare_ages_note`, `waitlisted_at`, `promoted_at`, `removed_by`, `removal_note`, `source` (`web|admin|guest_form`), `manage_token_hash` (lets guests cancel from an email link). Partial unique indexes on `(occurrence_id, user_id)` and `(occurrence_id, household_person_id)`. `removed` is a leader or host pruning someone (DEC-20, DEC-30).
- Later: `Attendance(occurrence_id, user_id?, household_person_id?, present, checked_in_at, checked_in_by, method roll|qr|kiosk)`.

**Rooms (optional; hidden when `rooms_enabled=false`)**

- `Room(name, building, capacity, features, notes, active, restricted, color)`. `restricted` rooms (for example the nursery) can be booked only by admins.
- `RoomBooking(occurrence_id, room_id, starts_at, ends_at, setup_minutes, teardown_minutes, padded_range tstzrange, status confirmed|requested|rejected, allow_overlap, overlap_reason, created_by)`. The app computes `padded_range = [starts_at - setup, ends_at + teardown)` on every insert and update (a `timestamptz - interval` expression is not immutable, so it cannot be used inside the constraint). Conflicts are enforced by a PostgreSQL exclusion constraint `EXCLUDE USING gist (room_id WITH =, padded_range WITH &&) WHERE (status = 'confirmed' AND NOT allow_overlap)`, using `btree_gist` (DEC-1). A `CHECK` keeps `padded_range` consistent with the four source columns. An admin double-booking sets `allow_overlap=true` with a reason.

**Messaging and consent**

- `Message(channel email|sms|both, author_id, status draft|pending_approval|scheduled|sending|sent|cancelled|failed, category announcement|reminder|transactional, subject, body_html, body_text, sms_body, audience_json, scheduled_at, sent_at, approved_by, request_id?, send_window_override, override_reason, segments_estimate, recipient_count)`.
- `MessageRecipient(message_id, user_id, channel, address, status queued|suppressed|deferred|sent|delivered|bounced|complained|failed, reason, copy_of_user_id?, provider_message_id, error, sent_at)`. Unique `(message_id, user_id, channel, copy_of_user_id)`. `copy_of_user_id` marks a guardian copy (DEC-19).
- `MessageRequest(group_id, requester_id, sms_body, proposed_at, status pending|approved|rejected|expired|withdrawn, decided_by, decided_at, decision_note, message_id?)`.
- `Consent(user_id, channel, scope all|group:<id>|category:<name>, status opted_in|opted_out, source web_form|sms_keyword|sms_reply|admin_import|unsubscribe_link|admin, evidence_text, ip, user_agent, at)`. Append-only. For teens, SMS also needs a `TeenConsent` row with scope `sms` (DEC-8). The latest row per `(user, channel, scope)` wins.
- `Suppression(channel, address, reason hard_bounce|complaint|stop|manual, provider, at)`. Checked at send time, separately from users.
- `InboundMessage(channel, from, to, body, provider_id, received_at, matched_user_id, keyword, opt_out_match?, conversation_id?, forwarded_to)`. `opt_out_match` records the keyword or phrase that triggered an automatic opt-out (7.4).

**Contact privacy (DEC-31, 5.11, 7.8)**

- `ContactRequest(requester_id, subject_user_id, context_type group|event, context_id, fields email|phone|both, reason?, needs_admin, subject_decision pending|approved|denied, subject_decided_at, admin_decision pending|approved|denied|not_needed, admin_decided_by, admin_decided_at, status pending|granted|denied|withdrawn|expired, expires_at)`. `needs_admin` is true for teens. Requests expire after 14 days.
- `ContactGrant(request_id, leader_id, subject_user_id, context_type, context_id, fields, granted_at, ended_at, end_reason revoked_by_subject|revoked_by_admin|revoked_by_guardian|left_role|turned_18|account_disabled, ended_by)`. A grant is active while `ended_at` is null.
- `Conversation(context_type group|event, context_id, created_by, relay_token_hash, closed_at)`, `ConversationParticipant(conversation_id, user_id)`, and `ConversationMessage(conversation_id, author_id, channel in_app|email|sms, body_text, inbound_message_id?, at)` (from M3.11).

**Calendar**

- `CalendarToken(user_id, kind personal|room, room_id?, token_hash, created_at, last_used_at, revoked_at)`.

**Platform**

- `Job(kind, payload_json, run_at, status queued|running|done|failed|dead, attempts, max_attempts, locked_at, locked_by, last_error, unique_key)` (from M3.1).
- `AuditLog(actor_id, action, target_type, target_id, meta_json, ip, at)`. Created in M0.2. Covers admin actions, message sends and approvals, consent changes (including teen consent, SMS ownership checks, and automatic opt-outs), account request decisions, guardian and household link requests and decisions (DEC-32), event visibility changes, registration removals, send-window overrides, role and leadership changes, email changes, admin 2FA changes, setting changes (including `guardian_copies`), provider config changes, double-bookings, every contact-access event (DEC-31: each request, decision, withdrawal, expiry, grant, reveal, revocation, automatic end, and refused attempt), and one-to-one message metadata. Rows about contact access, minors, consent, and roles are never purged, even when a person is deleted. The durable log (DEC-33, 10.7) hardens this later.
- `Asset(key, visibility public|private, owner_type, owner_id, content_type, size, created_by)`. Needed for C9.

**Later (section 13)**

`SignupSheet(event_id|group_id, title, kind volunteer|potluck|supplies)`, `SignupSlot(sheet_id, label, quantity, starts_at?, requires_check)`, `SlotClaim(slot_id, user_id, note)`; `PrayerRequest(author_id, group_id?, visibility, body, anonymous, status, expires_at)`; `Post` and `Comment` for discussions; `Gallery` and `Photo`; `BackgroundCheck(user_id, status cleared|pending|expired|not_cleared, cleared_on, expires_on, provider_name, reference, entered_by)`.

### 5.4 Capacity and waitlist rules

- Capacity counts `going` registrations plus their `guest_count`, per occurrence. `maybe` does not take a slot. If `remote_capacity` is set, remote registrations count against it separately (Meetup does the same for hybrid events).
- A new "going" on a full event becomes `waitlisted` with `waitlisted_at = now` if the waitlist is on. Otherwise it is refused with 409 and a clear message.
- Promotion runs in one transaction that locks the occurrence row (`SELECT ... FOR UPDATE`). It runs when a `going` registration is cancelled or declined, when capacity goes up, or when an admin removes someone. It promotes the earliest waitlisted registrations that fit. A registration with guests that does not fit is skipped, and the next one is tried (Meetup does this too).
- No auto-promotion within 2 hours of the start (a fixed rule, not a setting). The leader gets a notice instead, and promotion is one click.
- A promoted person gets a transactional email (and an SMS if they opted in) with a one-click "I can't come" link that frees the slot again.
- People see their waitlist position ("You are #3 on the waitlist").
- There is no sign-up approval step (DEC-30). Anyone who can see an event may sign up. A leader or host can remove (prune) any registration with an optional note; the person is told, and the freed slot triggers promotion (DEC-20).

### 5.5 Background jobs

- Until M3.1, the few periodic tasks (occurrence extension, age rollover, token cleanup, request expiry) run from one in-process daily ticker guarded by a Postgres advisory lock. There is no jobs table yet.
- From M3.1, a `jobs` table and worker live in the same binary. The worker starts with `serve`. `FOLD_WORKERS=0` turns it off, for debugging only.
- Job kinds: `occurrences.extend`, `message.send`, `message.deliver_batch`, `reminder.schedule`, `waitlist.notify`, `webhook.process`, `provider.poll_status` (for example SMS registration or email domain verification), `cleanup.tokens`, `accounts.expire_requests`, `contact.expire_requests` and `contact.end_grants` (a daily sweep; role changes also end grants at once, 5.11), `accounts.age_rollover` (teens turn into adults at 18; household children who turn 13 are flagged to their guardians).
- The worker claims jobs with `FOR UPDATE SKIP LOCKED` and polls every 1 to 2 seconds (`FOLD_JOB_POLL=1s`). There is no `LISTEN/NOTIFY`.
- Retries use exponential backoff with jitter and a `max_attempts` per kind, then `dead`. The admin "System" page lists dead jobs with a retry button.
- `unique_key` makes scheduling idempotent (for example `reminder:occ:123:24h`).
- Timed sends store `run_at` and do not depend on cron.
- Offsite backups are not an app job: the `backup` container does them (11.8).

### 5.6 Provider adapters

All proprietary services sit behind small Go interfaces in `internal/providers`. Each adapter registers itself by name. The app picks adapters from `ProviderConfig` (or env overrides).

```go
// Email
type EmailSender interface {
    Send(ctx context.Context, m EmailMessage) (providerID string, err error)
    SendBatch(ctx context.Context, ms []EmailMessage) ([]SendResult, error) // may loop over Send
    Capabilities() EmailCaps // batch size, streams, webhooks, max rate
}
type EmailProvisioner interface {
    VerifyCredentials(ctx context.Context) error
    EnsureSendingDomain(ctx context.Context, domain string) ([]DNSRecord, error) // DKIM, SPF, return-path
    DomainStatus(ctx context.Context, domain string) (DomainStatus, error)
    EnsureWebhook(ctx context.Context, url, secret string, events []string) error
    EnsureStreams(ctx context.Context) error // transactional vs broadcast, where supported
}

// SMS
type SMSSender interface {
    Send(ctx context.Context, to, from, body string) (providerID string, err error)
    Capabilities() SMSCaps // messages per second, daily caps known from registration
}
type SMSProvisioner interface {
    VerifyCredentials(ctx context.Context) error
    SearchNumbers(ctx context.Context, q NumberQuery) ([]NumberOffer, error)
    BuyNumber(ctx context.Context, number string) error // MUST be gated by explicit admin cost confirmation
    ConfigureNumber(ctx context.Context, number, inboundURL, statusURL string) error
    RegistrationStatus(ctx context.Context) (Registration, error) // 10DLC brand/campaign or toll-free verification
    SubmitRegistration(ctx context.Context, r RegistrationInput) error // where the provider API allows it
}

// Webhooks: every adapter verifies signatures and normalizes events.
type WebhookParser interface {
    Parse(r *http.Request) ([]ProviderEvent, error) // delivered, bounced, complained, inbound, opt_out ...
}

// Optional
type DNSProvider interface {
    UpsertRecords(ctx context.Context, zone string, rs []DNSRecord) error
}
type VideoLinker interface {
    CreateLink(ctx context.Context, e EventInfo) (url string, err error)
}
```

Adapters to build, in order (one real adapter per kind first, the second when needed):

- Email: `devoutbox` (dev and tests; stores mail in the database and shows it at `/dev/outbox`), `smtp` (universal fallback, no webhooks), `postmark` (default, DEC-21). Later, when a church asks: `resend`, `mailgun`. No AWS-based adapter is planned (DEC-21).
- SMS: `devoutbox`, `telnyx` (default), then `twilio` (M3.10, before v1.0). `plivo` only if a church asks.
- DNS: `manual` (shows records to copy), `cloudflare`.
- Video: `link` (paste any URL), `jitsi` (makes a random room URL on a configurable Jitsi domain, default `meet.jit.si`). `zoom` later (Server-to-Server OAuth app; creates meetings).
- Storage: `local`, `s3` (one S3-compatible adapter covers B2, R2, Wasabi, DigitalOcean Spaces, Vultr, MinIO). "S3-compatible" is only the protocol; none of these is Amazon.

Before M3.2, M1.2 uses `internal/mail`: a tiny sender with just `devoutbox` and SMTP from env (`FOLD_SMTP_URL`). M3.2 moves both behind the registry without changing callers.

Rules for adapters:

- Use plain `net/http` and the provider's REST API, or the provider's official Go SDK when it is small. No CGO.
- Every adapter has a contract test against an `httptest.Server` fake built from the provider's documented requests and responses. No live calls in CI.
- An optional `make live-test PROVIDER=postmark` target uses real sandbox credentials from env, for humans only.

### 5.7 Storage abstraction

- Extend `storage.Store` with a visibility argument on `Put` and a `SignedURL(key, ttl)` method for private objects. `LocalStore` serves private keys at `/files/<key>` after a policy check, with `Cache-Control: private`. Public keys stay at `/uploads/<key>`.
- `S3Store` uses one S3-compatible client (`minio-go` v7, Apache-2.0, pure Go). Config: endpoint, region, bucket, access key, secret, `public_base_url` (optional CDN or custom domain), path-style flag.
- `fold storage migrate --to s3` copies all blobs and flips the config; `--to local` reverses it. Both can resume and are idempotent.
- Uploads keep the current limits (JPEG/PNG/GIF/WebP, content-sniffed, 5 MB) until galleries add their own limit.

### 5.8 Video facilitation (F11)

- Default is link-out. A leader pastes a Zoom, Meet, Teams, or other URL, or clicks "Generate Jitsi link", which makes `https://<jitsi_domain>/<church-slug>-<10 random characters>` with no API call.
- `video_reveal=registered_only` (the default) shows the link only to people registered `going`, plus leaders and admins. For public events this keeps uninvited people out of the call.
- The link appears in reminders, on the event page, and in personal ICS feeds. It is left out of public feeds when the setting is `registered_only`.
- Embedding video inside Fold is out of scope for now (DEC-16). Revisit in a later revision of this plan if churches ask for it. Embedded meet.jit.si calls are reportedly limited to 5 minutes, and JaaS and self-hosted Jitsi cost money or a second server (12.4).
- Later: a Zoom adapter that creates meetings by API, and a Google Meet adapter (needs Google Workspace; not verified).

### 5.9 Time zones

- One church time zone (setting). Events can override `tzid`, which is rarely needed.
- Occurrence expansion uses `rrule-go` with `DTSTART;TZID=<tz>:<local time>`. Each occurrence stores `starts_at` and `ends_at` in UTC.
- The UI shows times in the church time zone and labels them when the browser's zone differs.
- ICS output uses `TZID` parameters with IANA names plus a `VTIMEZONE` block.
- Each person may set a profile time zone (`User.timezone`). Fold uses it for that person's SMS send window (DEC-25). Guests and people without one use the church time zone.

### 5.10 Accounts, ages, and households (DEC-4, DEC-8, DEC-18 to DEC-20, DEC-23)

**Signing in.** People sign in with their email and password (DEC-23). The email is unique: one email is one account. Throttling is by email and by client IP (10.1). Password reset and invitations go to that email.

How a person gets in:

| Door | Who starts it | Result | Admin approval? |
|---|---|---|---|
| Self-signup | The person, on `/join` (when `self_signup=request`) | `AccountRequest` (source `self_signup`) | Yes |
| Invite from an admin | Admin | `Invitation`, then an active account | No |
| Invite from a leader | Leader (into a group they lead, or no group) | `Invitation`, then an active account | No |
| "Invite a friend" from a member | Member | `AccountRequest` (source `member_invite`, `requested_by` = member). The friend is emailed only after approval | Yes |
| Guest upgrade | A guest ticks "create an account" after registering | `AccountRequest` (source `guest_upgrade`); the guest row is upgraded on approval | Yes |

The age gate is on every door, and on the guest registration form for public events. A guest must tick "I am 18 or older". Otherwise the form says "Ask a parent or guardian to register you", and the guardian registers them as a household person, or a teen uses their own account. On account doors, the person picks "18 or older", "13 to 17", or "under 13":

- **Under 13**: no account. The form explains that a parent or guardian can add the child to their household. Nothing typed is stored.
- **13 to 17**: a teen account. It needs a birth date and its own email, and starts `pending_consent`. It cannot sign in to anything but a "waiting for consent" page until `account` consent is granted. Consent comes from confirmed guardians (a link counts only once an admin confirms it, DEC-32) and admins; the leader role never grants consent. (A leader who is also the teen's confirmed guardian grants it as the guardian.) Guardians often consent by email link; an admin grants with evidence text. When a teen self-signs up, the request also needs normal admin approval; an admin can approve and grant consent in one step.
- **18 or older**: an adult account.

**What teens can do** (DEC-18, DEC-20):

- See what members see, minus the directory, and register for **any event they can see**. There is no youth-only restriction. Leaders and hosts decide who attends: they watch the attendee list and remove anyone who shouldn't be there (5.4).
- Join groups like members do.
- Leaders and hosts never see a teen's email or phone. They reach teens through Fold (5.11, DEC-31).
- Teen accounts and teen or child household people are **clearly badged** ("Teen", "Child") wherever leaders, hosts, or admins see people: attendee lists, group rosters, and message recipient lists and counts ("142 recipients, 6 teens").
- Teens cannot be group leaders or event hosts, and cannot create groups. They do not appear in any directory.
- Email reaches them after `account` consent. SMS reaches them only with both `TeenConsent` scope `sms` and their own SMS opt-in.
- **Guardian copies** (DEC-19): off by default. An admin can turn on the church setting `guardian_copies`. Then each confirmed guardian gets an email copy of every non-transactional email and SMS sent to their teen, marked "Copy: sent to <teen> because your church sends guardian copies". Copies go by email only, never by SMS, because texts cost money (DEC-29). Turning it on or off is audited, and the privacy page says which way it is set.

**Household people** (no login): children under 13, teens without their own email, and adults who share an email with the account holder. The account holder registers them and receives every message about them. They are never messaged directly. Adding a child or teen, or turning a household teen into an account, waits for an admin to confirm the guardian link (DEC-32); adding an adult does not.

**Shared emails** (DEC-23). Some members, often older couples, share one email address. Fold allows one account per email, so:

- The shared email belongs to one account (one person's name, one password).
- The other person is added to that account's household as an `adult` household person.
- When RSVPing, the account holder ticks who is coming ("Me", "Mary"). Each named person is a registration and counts toward capacity. Attendee lists show "Mary Smith (via John Smith)".
- Messages, reminders, and texts go to the account holder only.
- Anyone who wants their own reminders or texts, wants to lead a group, or wants to be listed separately needs their own email address. A free webmail address is fine. An admin, or the account holder, can then turn the household person into their own account; their registrations move with them.
- Unnamed extra people ("+2") still use `guest_count` for events that allow guests.

`docs/MEMBERS.md` (M1.9) gives churches short text to share with members:

- "Sign in with your email address. If you and your spouse share one email, one of you creates the account and adds the other under My household. When you sign up for an event, tick everyone who is coming."
- "Only the person whose account it is gets the emails and texts. If you'd like your own reminders, or you lead a group, set up your own email address (a free one is fine) and ask the church office to move you to your own account."
- "Parents: add children under 13 to your household. Teens may have their own account if you consent; otherwise add them to your household and sign them up yourself."

Guidance for church staff (in `docs/DEPLOY.md` and the admin help page): import or invite one person per email; when a CSV has two people with one email, Fold makes the first an account and offers to add the others as adult household people; never invent placeholder emails.

### 5.11 Contact privacy: Fold as the contact proxy (DEC-31)

Leaders, co-leaders, and hosts reach teens through Fold, never directly. Adults choose.

- **Teens**: a leader or host never sees a teen's email address or phone number. They reach the teen with Fold email, Fold SMS (with the teen's consents, 7.4), or an in-app one-to-one message (7.8). Admins, the teen's confirmed guardians, and the teen still see it.
- **Adults**: the profile setting "Hide my contact info from group leaders" (`hide_contact_from_leaders`) is **off by default**, so leaders see adult members' email and phone as before (10.2). When it's on, leaders and hosts reach that adult only through Fold.
- **Asking for direct contact**: on a hidden person's card, a leader or host clicks "Request direct contact", picks email, phone, or both, and may add a reason. Leaders can ask only about people in their own groups, and hosts only about registrants of their own events.
  - Hidden adult: the adult alone approves or declines, in the app or from an email link.
  - Teen: an admin **and** the teen must both approve (DEC-31). Either one declining ends the request. A confirmed guardian gets a notice when a grant is made and sees active grants on the teen's page.
  - A request with no decision expires after 14 days. A leader can have at most 10 open requests (defaults).
- **Grants**: an approved request creates a `ContactGrant` for that leader, that person, and that group or event. Every time the leader opens the revealed details, Fold writes a `contact.reveal` audit row.
  - Who can revoke: the person; any admin; and for a teen, any confirmed guardian. Guardians already control a teen's consent, so they can always end a grant, but they can't create one.
  - Grants end automatically when the leader stops leading that group, when a host is removed from the event or 30 days after its last occurrence, when a teen turns 18 (the new adult's own setting then applies, and they are told), or when either account is disabled.
- **Every attempt is logged** (DEC-31). Each attempt to get direct contact is an `AuditLog` row, whatever the outcome: the request, each party's approval or denial, withdrawal, expiry, the grant, every reveal, revocation, automatic end, and any API call that asked for hidden fields without a grant (refused). These rows are never purged, even when a person is later deleted, so they are there for safeguarding reviews and legal requests. Admins can view and export them per person. The durable, tamper-evident log (DEC-33, 10.7) hardens this later.
- **Replies stay inside Fold**: see 7.8.
- **Co-leaders** (final review, finding 1). A leader can still make another adult a co-leader without an admin (section 6). That co-leader can message the group, teens included, only through Fold. Each send is logged and visible to admins, and they see no teen contact details unless an admin and the teen both approve. That closes the contact-detail part of the hole. Making someone a leader also writes an `AuditLog` row and shows the admins an in-app notice (M0.2), so an admin can undo a bad promotion quickly. There is **no extra admin-approval gate** on co-leader promotion, even in groups with teens (DEC-31). What remains: a co-leader sees what any leader sees about attendees (names, Teen badges, ages, and children's care notes during the event window), and can remove registrations. Revisit the gate if a church's safe-church policy requires vetting every leader; the background-check flags (M6.8) are the natural place for that. Grier confirmed this design on Oct 9, 2026 (DEC-34): no extra approval layers; audit, notify admins, and remove bad actors outright (leaders and admins can remove members from M0.3b, DEC-35).

---

## 6. Permissions matrix

Roles:

- **Admin**: `User.role=admin`. Church staff. Admins see everything, invite-only groups included (DEC-5; in the repo since PR #7).
- **Leader**: has `Membership.role=leader` in a specific group. Leader rights apply only to the groups they lead and to those groups' events. Leaders have broad powers over their groups' events, including visibility (DEC-17).
- **Host**: an adult assigned with `EventHost` to a church-wide event (DEC-7). Host rights apply only to that event. Hosts are often admins but don't have to be.
- **Member**: a signed-in adult with `status=active`.
- **Teen**: a signed-in user aged 13 to 17 with `account` consent (DEC-8). Teens have member rights, narrowed as the table shows. They are never leaders or hosts (DEC-18).
- **Guest**: not signed in, or a `status=guest` row created by a public signup.
- **Guardian** is a relationship, not a role: an adult with a confirmed `GuardianLink` to a teen or a household child. It adds the rights marked "guardian" below.
- A **moderator** role between member and leader (for example, someone who may remove attendees but not edit the group) may come later. It is a note only; do not build it (DEC-17).

"Own group" = a group where the user is an active leader. "Own event" = an event of an own group, or a church-wide event the user hosts.

All checks live in `internal/policy` as pure functions with table tests. Handlers call policy; they never inline role checks, and they never read `Group.LeaderID` for permissions (C12).

**Seeing and joining**

| Action | Admin | Leader / Host | Member | Teen | Guest |
|---|---|---|---|---|---|
| See public groups and events | Yes | Yes | Yes | Yes | Yes |
| See private groups and events | Yes | Yes | Yes | Yes | No |
| See invite-only groups and events | Yes, in lists and by id (DEC-5) | Own groups only | Groups they belong to only | Groups they belong to only | No |
| See an invite-only group's event made `private` or `public` (DEC-17) | Yes | Yes | Yes (event only; the group stays hidden) | Yes (event only) | `public` only (event only) |
| See pending or archived groups | Yes | Own only | No | No | No |
| Register for a public event | Yes | Yes | Yes | Yes | Yes, with name, a verified email, and "I am 18 or older" |
| Register for a private event | Yes | Yes | Yes | Yes (DEC-20) | No |
| Register for an invite-only event | Yes | Own groups | Group members only | Group members only | No |
| Register a household person (child, teen, or adult on the account) | Yes | As account holder | As account holder | No | No |
| Join a group | Yes | n/a | Public and private groups: join at once; invite-only: by invitation only (DEC-30) | Same as member | Public groups: "request to join" starts an account request (DEC-4) |

**Accounts, ages, households** (DEC-4, DEC-8, DEC-23)

| Action | Admin | Leader / Host | Member | Teen | Guest |
|---|---|---|---|---|---|
| Self-signup | n/a | n/a | n/a | n/a | Creates an account request |
| Invite a new person (no approval needed) | Yes | Leaders: yes, into an own group or no group. Hosts: no | No | No | No |
| "Invite a friend" (creates an account request) | n/a | n/a | Yes | No | No |
| Approve or reject account requests | Yes | No | No | No | No |
| Create a household; add household people | Any household | Own household only; children and teens wait for an admin to confirm the link (DEC-32) | Own household only; children and teens wait for an admin to confirm the link (DEC-32) | No | No |
| Turn a household person into their own account | Yes | As account holder (for a teen, the guardian link needs admin approval, DEC-32) | As account holder (for a teen, the guardian link needs admin approval, DEC-32) | No | No |
| Create a teen account | Yes | From own household only (as guardian; an admin confirms the link, DEC-32) | From own household only (as guardian; an admin confirms the link, DEC-32) | n/a | Self-signup as a teen (request, then consent) |
| Confirm any guardian or household link to a teen or child (DEC-32) | Yes, with a note on how it was checked | No | No | No | No |
| Grant or revoke teen consent (account, sms, photos) | Yes, with evidence text | Only as that teen's confirmed guardian; the leader role never grants consent | Only as that teen's confirmed guardian | No | No |
| Turn guardian copies on or off (DEC-19) | Yes (audited) | No | No | No | No |
| See a household person's name and age | Yes | Account holder; and hosts/leaders of events the person is registered for | Account holder only | No | No |
| See a household child's care notes | Yes | Guardian; and leaders/hosts of the event the child is registered for, during the event window (from 24 hours before to 24 hours after) | Guardian only | No | No |
| See a teen's contact details | Yes | No: message through Fold; direct contact only with a grant that an admin and the teen both approved (DEC-31) | Confirmed guardian only | Own only | No |
| Hide my contact info from group leaders (adults; off by default, DEC-31) | Own | Own | Own | n/a (always hidden) | No |
| Request direct contact for a hidden person (every attempt logged) | n/a (admins see contact details) | Leaders: people in own groups; hosts: registrants of own events | No | No | No |
| Approve or decline a direct-contact request | For teens (the teen must approve too) | As the subject | As the subject (a hidden adult decides alone) | As the subject (with an admin) | No |
| Revoke a direct-contact grant | Any | As the subject | As the subject; as a confirmed guardian, for their teen | As the subject | No |
| See and export contact-access history | All (CSV export for legal requests) | Own requests | Requests about them | Requests about them | No |

**Groups and events**

| Action | Admin | Leader / Host | Member | Teen | Guest |
|---|---|---|---|---|---|
| Create a group | Yes (active at once) | Leaders: yes (pending) | Yes (pending) | No | No |
| Approve or reject groups | Yes | No | No | No | No |
| Edit a group, manage its roster (add members) | Any | Own groups (leaders) | No | No | No |
| Remove a member from a group (audited; DEC-35, M0.3b) | Anyone except the group's last leader | Own groups (leaders): anyone except an admin or the last leader; co-leaders included | No (leave instead) | No (leave instead) | No |
| Make someone a group leader, or a leader a member again | Any adult (never the last leader) | Own groups, adults only; audited, and admins get an in-app notice (no admin gate, DEC-31, DEC-34); can't demote an admin (DEC-35) | No | No | No |
| Create or edit events in a group | Any | Own groups (leaders) | No | No | No |
| Create church-wide events | Yes | No | No | No | No |
| Assign or remove hosts of a church-wide event | Yes (adults only) | No | No | No | No |
| Edit a church-wide event | Any | Hosts: own events | No | No | No |
| Set a group event's visibility to any value (`public`, `private`, `invite_only`); more-open-than-group changes are audited and notified (DEC-6, DEC-17) | Any | Own group events (leaders) | No | No | No |
| Switch a church-wide event between `public` and `private` | Any | Hosts: own events | No | No | No |
| Remove (prune) a registration, with an optional note | Any | Own events | Own registrations only (cancel) | Own only (cancel) | Own, from the manage link |
| See an event's attendee list, with Teen and Child badges (DEC-20) | Any | Own events | Adult attendees' names; teens and children show only as a count. For an invite-only group's event made `private` or `public`: a count only, unless they're in the group | Same as member | No |
| See contact details (email, phone) of adults | Yes | Members of own groups and registrants of own events, unless the adult hides them (then through Fold, or with the adult's grant, DEC-31) | Per directory settings (later) | No | No |

**Rooms, messaging, calendar, admin**

| Action | Admin | Leader / Host | Member | Teen | Guest |
|---|---|---|---|---|---|
| Manage rooms | Yes | No | No | No | No |
| Book a room | Any room | Own events, non-restricted rooms | No | No | No |
| See the room calendar | Full details | Details of bookings they can see; others show "Reserved" | Same as leader | No | No |
| Email or SMS all members | Yes | No | No | No | No |
| Email or SMS a group's members | Any group | Own groups (leaders) | No | No | No |
| Email or SMS registrants of an occurrence, a series, or a chosen set of events | Any events | Own events only (every chosen event must be an own event) | No | No | No |
| See recipient lists with Teen badges and counts | Yes | For their own messages | No | No | No |
| Send a one-to-one message through Fold (in-app, email, SMS; 7.8) | Anyone | Members of own groups and registrants of own events | Reply in a conversation; "Message the organizers" of events they can see | Same as member | No |
| Request a group SMS | n/a | n/a (they send directly) | Yes, for groups they belong to | No | No |
| Approve a group SMS request | Any group | Own groups (leaders) | No | No | No |
| See the SMS opt-out log (read-only) | Yes | No | No | No | No |
| Override the SMS send window (warning, reason, audited; DEC-9) | Yes | No | No | No | No |
| Change the church-wide SMS send window | Yes (warning if outside 8 AM to 9 PM) | No | No | No | No |
| See message history | All | Messages they sent or approved, and messages to own groups or events | Messages they received | Messages they received | No |
| Personal ICS feed | Yes | Yes | Yes | Yes | No (public feeds only) |
| Room ICS feed | Yes | No | No | No | No |
| Configure providers, backups, settings | Yes | No | No | No | No |
| Manage users and roles | Yes | No | No | No | No |
| See the audit log | Yes | No | No | No | No |
| Background-check flags (later) | Yes (and an optional safety-coordinator role) | No | Own status only | No | No |

Guard rails that already exist and stay: the last admin cannot be demoted or deleted; you cannot delete or demote yourself; invite-only groups return 404 (not 403) to people who cannot see them. New guard rails: a teen cannot be made a leader, host, or admin (DEC-18); an account under 13 cannot be created by any path; a group cannot lose its last leader; no guardian or household link to a teen or child is confirmed without an admin (DEC-32); no API response to a leader or host includes a teen's email or phone (DEC-31); a leader cannot remove or demote an admin (DEC-35); an account that is a group's only leader cannot be deleted or disabled until the admin picks a successor for each such group (DEC-36).

---

## 7. Messaging pipeline

### 7.1 Flow

```
Composer (SPA) -> POST /api/v1/messages (draft)
   -> policy.CanMessage(author, audience)          [403 if not allowed]
   -> author is a plain member? -> MessageRequest  [7.6]
   -> schedule: status=scheduled, run_at = scheduled_at (or now)
Job message.send (at run_at):
   1. Re-check policy (roles may have changed since drafting)
   2. Resolve the audience into a recipient snapshot (MessageRecipient rows);
      addresses stay inside the worker, and authors see names and counts only (DEC-31)
   3. Dedupe by user (someone in two chosen groups gets one copy)
   4. Filter: consent, suppression list, missing address, age rules
      (teens need account consent, plus sms consent for SMS; household people never; DEC-8)
      -> rows marked suppressed with a reason (the author sees counts)
   5. Guardian copies (only if guardian_copies is on, DEC-19, DEC-29): for each teen
      recipient, add an email copy row for each confirmed guardian
   6. SMS send window (default 8 AM to 8 PM in each recipient's local time, DEC-9, DEC-25):
      outside it -> deferred per recipient to the next allowed time
   7. Enqueue message.deliver_batch jobs (email in batches of 50; SMS one per call)
Job message.deliver_batch:
   - rate limiter per provider (token bucket from adapter Capabilities + settings)
   - send From the church address with a relay Reply-To (7.8), store provider_message_id, mark sent
   - temporary error -> retry with backoff; permanent error -> failed, with the error text
Webhooks /hooks/{provider}/{email|sms}  (outside the CSRF group; C17):
   - verify the provider signature (401 and no change if it fails)
   - store the raw event briefly, enqueue webhook.process
   - delivered / bounced / complained / inbound / opt-out
     -> update MessageRecipient, Suppression, Consent
```

The recipient list the author sees before sending, and the history page after, show Teen badges and a teen count (DEC-20).

### 7.2 Audiences

`audience_json` is one of:

- `{"type":"occurrence","occurrenceId":123,"statuses":["going","waitlisted"]}`
- `{"type":"event_series","eventId":45,"from":"...","to":"...","statuses":["going"]}`
- `{"type":"events","eventIds":[45,46,47]}`: "a group of events" in Grier's words (for example all Advent events)
- `{"type":"group","groupId":7,"roles":["member","leader"]}`
- `{"type":"all_members"}`: admins only; active members, not guests
- `{"type":"users","userIds":[...]}`: admins only

Leaders can target only own groups and own events. Hosts of church-wide events can target only registrants of the events they host (DEC-7). The check runs when the message is created and again at send time.

A registration for a household person resolves to the account holder who registered them. Household people have no address of their own (DEC-8, DEC-23).

Guests are reached only through occurrence, series, or events audiences, because that is where they registered.

### 7.3 Composer (F6)

- One page with Email and SMS tabs. A message can have both bodies and go out on both channels.
- Email: TipTap (MIT license, React) rich text with bold, italic, links, headings, lists, images (uploaded through `Store` as public assets), and a button block. The server sanitizes the HTML with `bluemonday` and renders it into a simple, mobile-safe template with the church name, an unsubscribe link, and the church's postal address. A plain-text part is generated automatically.
- SMS: plain text with a live character and segment counter. It detects GSM-7 vs UCS-2 (one emoji switches to UCS-2: 70 characters per segment instead of 160). It shows an estimated cost from the provider's per-segment rate (an admin setting, pre-filled from section 12). The church name prefix ("First Baptist: ") and the "Reply STOP to opt out" footer are added automatically and counted.
- The audience picker shows only what the author may target, with live counts after consent filtering ("142 members; 9 will be skipped: 6 opted out, 3 have no phone").
- Preview, and "Send a test to me".
- Schedule: send now, or pick a date and time (church time zone). Scheduled messages can be edited or cancelled until sending starts.
- Merge fields: `{{first_name}}`, `{{event_title}}`, `{{event_time}}`, `{{event_link}}`, `{{manage_link}}`.
- SMS wording check (DEC-3): the composer warns when an SMS asks for money ("give", "donate", "tithe", "offering", links to the church's giving page). Carriers and providers treat donation asks as charity traffic, which can need 501(c)(3) proof (Twilio error 30487) or a different campaign type; Telnyx lists fundraising and donations under its Charity use case. Fold assumes churches are not 501(c)(3)s, so it keeps SMS to event and group logistics. Put giving asks in email.

### 7.4 Consent, opt-out, STOP handling

Email:

- Transactional mail (password reset, invites, registration confirmations, waitlist promotions) always sends. It goes on a separate stream where the provider supports one (Postmark message streams).
- Announcements and reminders respect `Consent` by scope (all church, a group, reminders). Members default to opted in for their groups and all-church announcements, with a visible preferences page. Guests default to transactional mail plus reminders for the event they registered for, unless they tick "Keep me updated".
- Every non-transactional email has a visible unsubscribe link plus `List-Unsubscribe` and `List-Unsubscribe-Post: List-Unsubscribe=One-Click` headers (RFC 8058). The link opens a preferences page without login (signed token). The one-click `POST /u/unsubscribe/<token>` is registered outside the CSRF group and is authenticated by the signed token alone (C17).
- Unsubscribes take effect at once. (CAN-SPAM allows up to 10 business days; we do better.)

SMS:

- SMS is off for a person until they opt in **and prove they own the number** (final review, finding 5). Either they tick a checkbox on their profile or signup form that shows the exact consent text and then confirm with a 6-digit code texted to the number (entered in Fold) or a "Reply YES to confirm" text, or they text a keyword (`START`, `JOIN`) from the phone itself. `Consent` becomes `opted_in`, and `phone_verified_at` is set, only after the code or the YES comes back from that number. Verification texts are limited to 3 per number per day. The consent text, time, IP, and user agent are stored in `Consent`.
- On opt-in, the app sends one confirmation text: church name, what messages to expect, "Msg & data rates may apply", "Reply HELP for help, STOP to opt out".
- Opt-out keywords. An inbound message that is one of these words or phrases (any case, ignoring spaces and punctuation around it) opts the person out of **all** SMS from this church at once: `STOP`, `STOPALL`, `QUIT`, `END`, `REVOKE`, `OPT OUT`, `OPTOUT`, `CANCEL`, `UNSUBSCRIBE`. The FCC treats "stop", "quit", "end", "revoke", "opt out", "cancel", and "unsubscribe" as revocation per se (47 CFR 64.1200(a)(10)). `START`, `UNSTOP`, `YES` opt back in. `HELP` and `INFO` get the help reply, at most once per number per 10 minutes; each number gets at most 5 automatic replies of any kind per day, and a repeated STOP from a number that is already opted out gets no reply. Providers and carriers may also act on some keywords; Fold records them anyway, so its own suppression list stays correct if the church switches providers. Fold then sends one confirmation text ("You're unsubscribed from <church> texts. Reply START to resubscribe.") and nothing else.
- Revocation in other words (DEC-27). The FCC rule (47 CFR 64.1200(a)(10)) also counts any reasonable way of saying "stop". Fold applies these **automatically and at once**, with no human review:
  - A reply that reasonably reads as an opt-out request (for example "please stop texting me", "remove me", "no more texts", "take me off this list", "don't text me") opts the person out of all SMS immediately and gets the same confirmation text. The phrase list lives in `internal/consent` and errs toward opting out.
  - There is no review queue and no timer. A person opted out by mistake texts `START` to opt back in.
  - Admins see a read-only **opt-out log** (who, when, the matched word or phrase, the message). Each opt-out is also an `AuditLog` and `Consent` row.
  - Opting out stops all SMS from the church, so it already covers the FCC's "revoke all unrelated messages" rule, whose effective date the FCC delayed to January 31, 2027 (FCC DA 26-12).
- Other inbound texts go through the relay in 7.8 (AGENTS.md: "replies route to the group leader"): Fold passes them to the author of the latest message sent to that person in the last 7 days, without showing the phone number, or to the admins if there is none. A full shared inbox comes later.

Minors (DEC-8, DEC-19):

- Children under 13 have no account and are never messaged. Messages about their registrations go to the account holder who registered them.
- Teens (13 to 17) receive email only after `account` consent. They receive SMS only when **both** of these are true:
  - a `TeenConsent` row with scope `sms` is granted (by a confirmed guardian, or by an admin with evidence; the leader role never grants consent)
  - the teen has opted in to SMS themselves
- Revoking either consent stops SMS at once.
- An imported phone number for a teen is never treated as opted in.
- Guardian copies are off unless an admin turns on `guardian_copies` (7.1 step 5, 5.10).

### 7.5 SMS send window, rate limits, bounces

- SMS send window (DEC-9, DEC-25; SMS only, email is not limited):
  - Default: send only between **8:00 AM and 8:00 PM in the recipient's local time**. Admins can change it in Settings.
  - The recipient's local time comes from `User.timezone` if the person set it in their profile, and otherwise from the church time zone. Guests use the church time zone.
  - A message to many people is split by time zone. Each recipient's copy waits until their window opens. The composer shows "N recipients will get this later (outside their send window)".
  - If an admin sets a window that starts before 8:00 AM or ends after 9:00 PM, Fold shows a warning before saving. The federal TCPA rule bars telephone solicitations before 8 a.m. or after 9 p.m. in the called party's local time (47 CFR 64.1200(c)(1)), and some states are stricter. The admin must tick "I understand" to save, and the change is audited.
  - Override for one urgent message (for example "service cancelled, snow"): admins only. It needs a reason and shows the same warning when the send time is outside 8 AM to 9 PM. It is audited.
  - Replies Fold must send (STOP and HELP answers, opt-in confirmations) are not delayed.
  - This is not legal advice (DEC-14).
- Rate limits:
  - Provider throughput: from adapter capabilities and the church's registration tier (for example a 10DLC brand's daily T-Mobile cap; see 12.2).
  - Per author: leaders may send at most 3 SMS blasts and 5 emails per group per day (defaults, configurable). Admins have no limit but are audited.
  - Per recipient: at most 1 non-transactional SMS per person per hour across all groups (default). Extra messages are deferred.
- Bounces and complaints: a hard bounce adds the address to `Suppression` and flags the email as invalid on the user's profile, with a banner asking them to update it. A spam complaint opts the user out of all non-transactional email. Soft bounces are retried by the provider; after 3 soft bounces in 30 days, the address is suppressed for 7 days.
- SMS failures: carrier "unreachable" or "landline" errors mark the phone number invalid. Carrier filtering errors alert the admins, because they usually mean a registration or content problem.

### 7.6 Member-requested group SMS (F9)

1. A member opens their group, clicks "Request a text to the group", writes the text, and may propose a send time.
2. A `MessageRequest` is created with `status=pending`. Every leader of the group and every admin gets an in-app notification and an email.
3. Any one of them can approve (optionally after editing; the edit is recorded), reject with a note, or let it expire. It expires at the proposed time, or after 72 hours if no time was given.
4. Approval creates a normal `Message` that records the requester and `approved_by`. It is sent under the usual consent, send-window, and rate-limit rules. The requester hears back either way.
5. The first decision wins. The approve call is a conditional update (`WHERE status='pending'`), so two approvers cannot send it twice.

### 7.7 Reminders

- Automatic reminders per occurrence for `going` registrants: email 24 hours before (on by default) and SMS 2 hours before (off by default; opted-in people only). Leaders can change these per event.
- Waitlisted people get "you're in" notices, or "still full and starting soon".
- SMS reminders follow the send window. If the reminder time falls outside the recipient's window, it moves to the last allowed time before it, if that is no more than 18 hours earlier (for example 7:59 PM the night before a 9:00 AM event). Otherwise it is skipped, and the email reminder still goes.
- Reminders are `category=reminder` messages, so they appear in history and respect consent.

### 7.8 One-to-one messages and reply relay (DEC-31)

Fold sits between leaders and the people they reach, so nobody's personal address appears in headers or on screen unless they chose to share it (5.11).

- **In-app**: a leader or host opens "Message" on a person in their own group or event. This starts a `Conversation` tied to that group or event. Members answer in the thread, and can use "Message the organizers" on an event they can see, which reaches its leaders or hosts without naming them. Admins see every conversation in message history. Guardian copies apply to messages to teens when `guardian_copies` is on.
- **Email relay**: every email Fold sends for an author (broadcasts and one-to-one) is From `"<Author name> via <church name>" <messages@mail.<church domain>>`, with Reply-To `r-<token>@reply.mail.<church domain>`. The token is random, stored hashed, and maps to one conversation (for a broadcast, to the author and the message). Replies arrive through the provider's inbound processing (Postmark's inbound webhook, https://postmarkapp.com/developer/user-guide/inbound) at `/hooks/postmark/inbound`, outside the CSRF group and verified like other hooks. Fold then:
  1. checks that the sender's address belongs to one of the conversation's participants (mail from anyone else is dropped and logged);
  2. keeps the text and drops the original headers and any attachments (the sender is told to use the app for files);
  3. delivers the reply to the other side in-app and by email, again From Fold with the relay Reply-To.
  - Personal addresses never appear in From, Reply-To, To, or Cc. Text that people type themselves (for example a signature with their phone number) is passed on as written; the help page says so.
  - Setup: one MX record for `reply.mail.<church domain>` pointing at the provider's inbound host (wizard step 3, M4.4; added by hand before then). An SMTP-only setup has no inbound mail, so its Reply-To is the church office address, and leaders still never see members' addresses.
- **SMS relay**: Fold uses the church's one number. Each text starts with the church and the group or event ("First Baptist, Youth Group: ..."). Keywords are handled first (7.4). Any other reply goes to the conversation of the latest message Fold sent that person in the last 7 days, and Fold delivers it to the author in-app and by email (with a relay Reply-To), never showing the number. The author's answer, in the app or by email reply, goes back as an SMS from the church number under the usual consent, send-window, and rate-limit rules. With no message in the last 7 days, the reply goes to the admins. One active SMS conversation per person keeps this simple; the help reply tells people to use the app for older threads.
- **Logging and limits**: one-to-one messages count toward the per-author limits (7.5), stay in message history, and write audit metadata (who, to whom, context, channel; not the body).

---

## 8. Calendar and ICS feeds (F2)

### 8.1 In-app views

- Timeline (`/calendar`): an agenda list grouped by day showing every occurrence the viewer may see. Filters: group, category, "my events", visibility, childcare, remote. Scrolls forward; a "past" toggle shows history.
- Week and month grid views (a small calendar component; no heavy dependency needed).
- By room (`/calendar/rooms`, when rooms are on): one column per room, rows for time, one day or one week at a time. Bookings the viewer cannot see show as grey "Reserved" blocks with no title.

### 8.2 Feeds

| Feed | URL | Auth | Contents |
|---|---|---|---|
| Public church feed | `/cal/public.ics` | none | Published public events. No attendee data. Video links only if `video_reveal=always`. |
| Public group feed | `/cal/groups/<id>.ics` | none; public groups only | That group's public events |
| Personal feed | `/cal/u/<token>.ics` | secret token in the URL | Option A, "My events" (default): occurrences the user is `going` or `waitlisted` for, plus all events of groups they belong to, events they host (DEC-7), and events their household people are registered for (DEC-8). Option B, "Everything I can see". Video links included when the user may see them. |
| Room feed | `/cal/rooms/<roomId>/<token>.ics` | admin-issued token | All bookings for the room. Invite-only bookings are titled "Reserved". |

Token rules:

- 32 random bytes, base64url, shown once. Only the SHA-256 hash is stored.
- Users can regenerate their token (which revokes the old one) from their profile. Admins can revoke anyone's.
- Feed contents follow the user's current permissions at fetch time. If someone leaves an invite-only group, its events drop out of their feed on the next refresh.
- Tokens of disabled or deleted users stop working.
- Responses carry an `ETag` and `Cache-Control: private, max-age=900`. Feeds cover 30 days back to 12 months ahead.

ICS details:

- Emit each series as one `VEVENT` with `RRULE` and `EXDATE` for cancelled dates, plus `RECURRENCE-ID` entries for changed occurrences. Use `arran4/golang-ical` or write the text by hand; either way the output must pass parse and golden-file tests.
- `UID` = `event-<id>@<SITE_URL host>`, stable forever.
- `DESCRIPTION` = plain text from `description_html` plus the event URL. `LOCATION` = room name and location text. The remote and childcare flags are mentioned in the description.

---

## 9. Rooms and the room-suggestion rule (F3, F4)

### 9.1 Optional by design

- `rooms_enabled=false` (the default on a fresh install) hides every room screen and endpoint. Event locations stay free text.
- Turning rooms on keeps existing free-text locations. A one-time "match locations" helper lets admins map common location strings to rooms.

### 9.2 Bookings and conflicts

- A booking belongs to one occurrence. "Book this room for the whole series" creates a booking for each generated occurrence, and for future ones as they are generated.
- A conflict is any confirmed booking for the same room whose stored `padded_range` (`[starts_at - setup, ends_at + teardown)`, computed by the app) overlaps. The database enforces this with an exclusion constraint (5.3, DEC-1), so two leaders booking at the same moment cannot both win. Conflicts block leaders and hosts. Admins may double-book with a reason (audited).
- When an occurrence moves (a per-date change or a series edit, 5.3), its bookings move with it in the same transaction. If the new time conflicts, the booking becomes `requested` (not confirmed), the edit still saves, and the leader or host sees "Room lost on 2 dates: pick another room". Admins may instead keep it with a double-book reason.
- Capacity gives a warning, not a block, when `event.capacity` or the current going count is larger than `room.capacity`.

### 9.3 Room suggestions: the exact rule

When someone picks a room for event E (group G_E, which may be none; visibility V_E; category T_E; occurrence time window W), Fold suggests rooms like this.

**Definitions**

- A *usage* is a `RoomBooking` B with status `confirmed`, on an occurrence of some event E_B, starting within the last 12 months or in the future.
- V_B is the **current** visibility of E_B. If an invite-only group's event was made `private` or `public` (DEC-6, DEC-17), its usages count as usages of that visibility. That is safe, because the event is already visible at that level.

**Rule 1: which usages count**

- If V_E is `public` or `private`: a usage B counts only if V_B is `public` or `private`. Usages by `invite_only` events never count. So a room that has only ever been used by invite-only groups is never suggested for a public or private event, even when the person choosing is an admin.
- If V_E is `invite_only`: a usage B counts if V_B is `public` or `private`, **or** if E_B belongs to the same group G_E. Usages by *other* invite-only groups never count.
- Usages by E itself (earlier occurrences of the same series) always count.

**Rule 2: which rooms are candidates**

- A room is a candidate if it is `active`, has at least one counting usage, and is not `restricted` (restricted rooms are candidates only when the chooser is an admin).
- Rooms with a confirmed conflict in W stay in the list but are marked "Busy" and sorted last. For a series, conflicts are checked across the next 8 occurrences ("Busy on 2 of 8 dates").

**Rule 3: type filter**

- If T_E is set, show only candidates with at least one counting usage whose event `category = T_E`.
- If that leaves nothing, fall back to all candidates from Rule 2 and label them "Used by other kinds of events".

**Rule 4: ranking** (highest first)

1. Counting usages by E itself (same series)
2. Counting usages by events of the same group G_E
3. Counting usages with the same category T_E
4. Most recent counting usage
5. Total counting usages
6. Capacity closest to (and at least) E's capacity, when set

Show the top 5. Each one says why it was suggested ("Used by this group 6 times").

**Rule 5: never leak**

- The response contains only room fields and counts based on counting usages. It never includes other events' titles, groups, or dates.
- The full room list (without usage history) is always available in the picker, so anyone allowed to book can still choose any non-restricted room by hand. Suggestions are a shortcut, not a gate.

Implementation note for agents: keep the counting-usage test in one function, `rooms.CountingUsagePredicate(E)`, and build the query from it. The test file must cover at least these cases:

| Case | Setup | Expected |
|---|---|---|
| 1 | Room R used only by invite-only group X; new public event | R not suggested |
| 2 | Same as 1; new private event created by an admin | R not suggested |
| 3 | Same as 1; new invite-only event in group X | R suggested |
| 4 | Same as 1; new invite-only event in another invite-only group Y | R not suggested |
| 5 | R used by a private event and by group X (invite-only); new public event | R suggested; count shows 1 usage, not 2 |
| 6 | Group X changes from invite-only to private | X's past usages now count for public and private events |
| 7 | Type filter matches nothing | Fallback list, labeled |
| 8 | Restricted room; leader chooses | Not suggested. Admin chooses: suggested |
| 9 | Only usage is older than 12 months | Not suggested |
| 10 | Conflict in W | Listed last, marked Busy |
| 11 | Room R used only by group X (invite-only), but one of X's events was made public (DEC-6, DEC-17); new public event | R suggested; count shows only the public event's usages |
| 12 | Church-wide event (no group) | Same-group ranking is skipped; Rules 1 to 5 otherwise apply |
| 13 | Room R used only by group X (invite-only), but one of X's events was made `private` (DEC-17); new private event | R suggested; count shows only the private event's usages |

---

## 10. Security and privacy

### 10.1 Baseline (keep and extend)

- Keep: HTTPS through Caddy, Secure/HttpOnly/SameSite=Lax cookies, CSRF tokens, bcrypt, session invalidation on password change, upload content sniffing, the SSRF guard on URL fetch, `nosniff`, and security headers.
- Add:
  - Trusted proxies (C17): Gin trusts `X-Forwarded-For` only from `FOLD_TRUSTED_PROXIES`. The compose file gives the internal network a fixed subnet, and that subnet is the default. With the wrong value, a test page at Admin > System shows the IP Fold sees.
  - CSRF route groups (C17): every state-changing route uses CSRF except `/hooks/*` (provider signature required) and `POST /u/unsubscribe/<token>` (signed token required). A test asserts that list stays exactly that.
  - Password reset and invite tokens: single use, hashed, valid 1 to 24 hours.
  - No account enumeration (final review, finding 4): sign-up, guest registration, password reset, and login give the same response, in about the same time, whether or not an email has an account. The email itself carries the hint ("you already have an account; sign in or reset your password").
  - Email change: the new address must be verified by a link before it takes effect; the old address gets a notice with a "this wasn't me" link to the church office; the change ends every other session.
  - Session revocation: changing a password, email, or role (including becoming an admin), turning on 2FA, or disabling an account bumps `session_version`, which ends the person's other sessions.
  - TOTP two-factor sign-in, required for every admin from v0.5 (M4.9), with one-time recovery codes.
  - Login throttling by email and by client IP (DEC-23), stored in Postgres so it survives restarts.
  - Rate limits on public endpoints (guest signup, account requests), with a hidden honeypot field instead of a third-party CAPTCHA. Limits apply per IP and per target, with these defaults (admins can change them):
    - guest verification emails: 3 per address per hour and 10 per day, as well as per IP (M1.4);
    - leader invitations: 20 per leader per day (M1.2);
    - HELP and other automatic SMS replies: 1 HELP reply per number per 10 minutes and 5 automatic replies per number per day (7.4, M3.7);
    - phone verification texts: 3 per number per day (7.4, M3.7);
    - direct-contact requests: 10 open per leader (5.11, M1.11).
  - Signature checks on every provider webhook.
  - Security headers set by the app (M0.3b, DEC-37): a Content-Security-Policy for the SPA and `/setup`, HSTS when cookies are secure, and a `Permissions-Policy`, plus the existing `nosniff`, referrer, and frame headers.
- Provider secrets are encrypted at rest (C8) and never sent back to the browser after saving. The UI shows "set, last checked on ...".
- `AuditLog` (created in M0.2) covers admin actions, sends, approvals, consent changes, role and secret changes, and every contact-access attempt (5.3, 5.11).
- Export: admins can export people, groups, and events as CSV. Users can download their own data as JSON.
- Deletion: deleting a user removes personal data (name, email, phone, avatar) but keeps anonymous registrations for attendance counts. If the account is the only leader of any group, the admin first picks a successor for each (a member of that group or any admin, the deleting admin by default); the handoff is audited (DEC-36, M0.3b). Disabling follows the same rule. Opt-out and suppression records keep a hashed address so the person is never messaged again by mistake.

### 10.2 Directory and contact details

- Default: members see other members' display names and avatars only inside groups they share. No church-wide directory until M6.1.
- M6.1 adds a church directory with per-field visibility chosen by each person: `everyone at church`, `my groups`, `leaders and admins`, `only admins`. Defaults: name and photo = everyone at church; email and phone = leaders and admins; address and birthday = only admins. Being listed at all is opt-in.
- Leaders and hosts see adult contact details for members of their own groups and registrants of their own events, because they need them to run the group, unless the adult turns on "Hide my contact info from group leaders" (DEC-31). They never see teens' contact details (5.11). The privacy page says so.
- When an invite-only group's event is opened to `private` or `public`, only leaders and admins see its attendee names, so a recovery or divorce-care group's members aren't revealed (5.3, M1.1b).
- Guests never appear in any directory.

### 10.3 Minors, households, and childcare (DEC-8, DEC-18 to DEC-20)

- **No accounts for anyone under 13**, by any door (self-signup, invite, import, admin). The age gate is in 5.10. Children under 13 exist only as `HouseholdPerson` rows (`kind=child`) in a household, created by a guardian or an admin, with no login, email, or phone. Fold collects their details from the parent, not from the child. This is meant to keep Fold out of COPPA's main scope (not legal advice, DEC-14).
- **Teens (13 to 17)** can have accounts with consent. The account stays `pending_consent` until `account` consent is granted by guardians and admins; the leader role never grants consent:
  - by a confirmed guardian, or
  - by an admin, with evidence text.
  - Consent is append-only and can be revoked. Revoking `account` consent disables the account but keeps its history.
- Teens without their own email can instead be household people (`kind=teen`) whom a guardian registers for events.
- Every guardian or household link to a teen or child needs an admin to confirm it (DEC-32), even when the guardian creates the household person or turns a household teen into an account. Nothing is auto-confirmed, so a stranger can't add a real teen's email to their household and become that teen's guardian (final review, finding 2).
- Teens may register for any event they can see (DEC-20). The safeguard is visibility plus leader control, not a youth-only flag or an approval gate: Teen and Child badges appear on attendee lists, rosters, and recipient lists, and leaders and hosts remove registrations that shouldn't be there (DEC-30). Other members see teens and children only as a count.
- Teens are never in the directory and have no public avatar. They cannot lead groups or host events (DEC-18). Their contact details are visible only to admins, their confirmed guardians, and themselves. Leaders and hosts reach them through Fold (5.11, 7.8) and see direct contact only with a grant that both an admin and the teen approved (DEC-31).
- Guardian copies of messages to teens are off by default; admins can turn them on church-wide (DEC-19, 5.10). Many safe-church policies expect copies, so the setup checklist asks the admin to decide.
- Birth dates of teens and household people are visible only to admins and guardians. Leaders see an age (for example "age 15").
- Care notes on household children (allergies, notes) are visible to admins and guardians. Leaders or hosts of an event see the notes of children registered for it, from 24 hours before to 24 hours after the event. Later, a "childcare coordinator" role can see them too. Drop-in childcare that doesn't name children still uses `childcare_count` and an age note.
- Photos: galleries default to private assets visible to group members only (C9). `HouseholdPerson.photo_consent` and `TeenConsent` scope `photos` control whether a child or teen may appear in galleries. Both default to no.
- Background-check flags (M6.8): store the status, cleared date, expiry date, provider name, reference number, and who entered it. Never store the report. Volunteer slots can be marked "requires a current background check"; people without a current `cleared` status cannot claim them and see a polite explanation. Only admins (and an optional safety-coordinator role) see the flags. Each person can see their own status and expiry.

### 10.4 SMS consent (TCPA and 10DLC)

- Opt-in only, with recorded evidence (7.4). Phone numbers are never imported as opted in by default. A CSV import can mark consent only if the admin ticks "I have written consent for these numbers" (audited).
- Opt-out is applied automatically and at once, for keywords and for any reply that reasonably reads as an opt-out (7.4, DEC-27), well inside the FCC's 10 business days.
- Carrier registration (10DLC or toll-free verification) needs a public privacy policy and SMS terms page. Telnyx's campaign guidance requires opt-in flows to link both, and Twilio has required both URLs on new 10DLC campaigns submitted by API since June 30, 2026. Fold generates `/legal/privacy` and `/legal/sms-terms` from church settings (name, address, contact email, message types, frequency, "Msg & data rates may apply", STOP and HELP), so the church has them on day one. An admin must review and accept the text first.
- The opt-in screen and confirmation text match what the registration describes, because carriers check.
- Registration path (DEC-3). Fold assumes churches are **not** 501(c)(3) organizations and never asks for 501(c)(3) proof.
  - Fold never picks a charity use case. Twilio's docs say the 10DLC `CHARITY` use case "does not include religious organizations"; Telnyx describes its `NON_PROFIT` entity type as "501(c)(3) or equivalent" and requires vetting. Ask the provider which entity type fits; never claim nonprofit tax status.
  - With an EIN: an EIN-based standard brand with a Low-Volume Mixed or Mixed campaign, or toll-free verification with the EIN as the business registration number.
  - Without an EIN: get a free EIN from the IRS. On Telnyx (the default) there is **no sole-proprietor fallback for a church**: Telnyx's help center says an entity whose name or website contains words such as "Church" or "Ministry", or a `.org` domain, does not qualify as a sole proprietor and must register a standard brand with an EIN. Twilio's sole-proprietor path is designed for individuals and has tight limits.
  - Details and limits are in 12.2.
- Keep SMS to logistics (events, groups, reminders), not donation asks. Donation asks can be classed as charity traffic, which needs 501(c)(3) proof (Twilio error 30487). The composer warns (7.3).
- Lawyer review is case by case (DEC-14). The generated legal pages are labeled as templates, not legal advice; each church is responsible for its own wording. The hope is that Grier rarely or never hosts for others. Before any church goes live, whoever runs it should read 10.3 to 10.5.

### 10.5 Email (CAN-SPAM and inbox rules)

- An unsubscribe link and one-click headers on every non-transactional message, the church's postal address in the footer, and an honest From name and Subject.
- SPF, DKIM, and DMARC set up by the wizard (11.3). Large mailbox providers expect these, plus one-click unsubscribe, from bulk senders.
- From address: `"<author> via <church name>" <messages@mail.<church domain>>`, with Reply-To set to a relay address, so replies come back through Fold and nobody's personal address is exposed (7.8, DEC-31). Without inbound mail (SMTP only), Reply-To is the church office.

### 10.6 Hardening checklist (Fold is on the public internet)

DEC-30's "trust the church body" covers workflow only: no approval gates on sign-ups. It never weakens these controls. Each item has an owner milestone, and a release is not done until every item that applies to it is checked.

- [ ] Account approval: self-signup and member "invite a friend" need admin approval (DEC-4; M1.3)
- [ ] Authentication: bcrypt, email login, single-use hashed reset and invite tokens, other sessions ended on password, email, or role change and on disable, Secure/HttpOnly/SameSite cookies (M0.3, M1.2)
- [ ] Admin two-factor sign-in (TOTP), required for every admin from v0.5 (M4.9)
- [ ] Login throttling by email and client IP, stored in Postgres (M0.3)
- [ ] Rate limits and a honeypot on public forms, per IP and per target address; per-leader invitation limits; caps on HELP and automatic SMS replies and on phone verification texts; open direct-contact request limits; per-author and per-recipient message limits (M1.2, M1.3, M1.4, M1.11, M3.7, M3.8)
- [x] CSRF on every state-changing route except signature- or token-authenticated hooks and one-click unsubscribe (M0.1)
- [x] Trusted proxies set to the compose network only (M0.1)
- [ ] Input validation and output sanitizing: server-side validation on every API field, bluemonday on rich text, SSRF guard on URL fetch (all milestones; M3.4)
- [ ] Uploads: type sniffing, size limits, private assets behind policy checks (M4.7)
- [ ] Secrets: provider secrets encrypted at rest, never returned to the browser, `.env` mode 600 (M4.1)
- [x] Least-privilege permissions: every check in `internal/policy` with table tests; invite-only stays 404 to outsiders (M0.2)
- [ ] Audit log for admin actions, sends, consent, roles, settings, secrets, links of minors, and every contact-access attempt; the table is created in M0.2, and rows about contact access, minors, consent, and roles are never purged (M0.2 onward)
- [ ] Leaders remove members (never an admin or the last leader) and sole-leader accounts hand off to a chosen successor before deletion or disabling; both audited (M0.3b, DEC-35, DEC-36)
- [ ] TLS everywhere (Caddy) plus security headers: HSTS, CSP, `nosniff`, frame, referrer, and permissions policies, set by the app in M0.3b (DEC-37); Caddy and image-level headers in M5.1
- [ ] Webhook signature checks on every provider, including inbound mail (M3.2, M3.7, M3.11)
- [ ] No account enumeration: sign-up, guest registration, password reset, and login give the same response whether or not an email has an account; the email carries the hint (M0.3, M1.2, M1.3, M1.4)
- [ ] Email change: the new address is verified before it takes effect, the old address gets a notice, and other sessions end (M1.2)
- [ ] SMS opt-in only after proof of phone ownership: a texted code or a YES reply (M3.7)
- [ ] Contact privacy: leaders and hosts never get teens' contact details; hidden adults' details only with their grant; every contact-access attempt audited; replies relayed through Fold (M1.11, M3.11)
- [ ] Guardian and household links of teens and children confirmed only by an admin (M1.9, M1.10)
- [ ] An opened invite-only event never reveals the group's members or leaders (M1.1b)
- [x] Leader grants audited, with an in-app notice to admins (M0.2)
- [ ] Later, not v1: durable, tamper-evident audit log for the most important events (DEC-33, 10.7, M6.14). The regular `AuditLog` covers them until then.
- [ ] Backups: nightly, retained, restore-tested in CI, offsite encrypted with `age`; backup check before migrations (M5.2, M5.3)
- [ ] Dependency updates: Dependabot for Go modules, npm, GitHub Actions, and base images, and `govulncheck` and `npm audit` in CI (M0.3b, DEC-37); pinned image digests in releases (M5.1)
- [ ] Server: unattended OS security updates and a firewall allowing only 22, 80, and 443 (Appendix A)

### 10.7 Durable audit log (later, DEC-33)

Not v1 scope; milestone M6.14. Until then, the regular `AuditLog` (created in M0.2) records all of these events, and they are never purged by cleanup or user deletion. The durable log hardens that record against later editing or loss, in case of legal trouble.

- **What it covers** (the really important events): contact-access requests, decisions, grants, reveals, revocations, and every failed, abandoned, or expired attempt (DEC-31); guardian and household links of teens and children (DEC-32); teen consent; role and leadership changes; visibility changes that make an event more open than its group; SMS opt-outs and opt-ins; and admin overrides (send window, double-bookings, consent granted with evidence text).
- **Ideas to settle in M6.14** (a sketch, not a design):
  - Append-only storage: a separate table that the app's database role can only `INSERT` into and `SELECT` from, with no `UPDATE` or `DELETE` grant, plus a trigger that rejects changes anyway.
  - Hash chaining: each row stores `prev_hash` and `hash = SHA-256(prev_hash + canonical row)`, so an edited, deleted, or reordered row breaks the chain. `fold audit verify` and the Admin > System page check the chain.
  - Write-once offsite export: the `backup` container uploads a daily signed segment to an S3-compatible bucket with object lock, for example Backblaze B2 Object Lock in compliance mode. Backblaze says Object Lock has no extra cost beyond normal storage and supports retention periods of 1 to 3,000 days (https://www.backblaze.com/docs/cloud-storage-object-lock). A locked file can't be deleted early even by the church, so the retention period must be chosen with care.
  - Retention rules: a church-set period per event type, with legal holds. Fold's default keeps everything; the church decides with its own advisor (DEC-14). The privacy page explains that these records survive account deletion.
  - Admin export for legal requests: filter by person and date range; CSV plus JSON with the hash chain and a verification report; the export itself is logged.
- **Acceptance to write before work starts**: verification catches any edited, deleted, or reordered row; the offsite segments can't be deleted before retention ends; an export for one person verifies on its own.

---

## 11. Deployment, setup wizard, upgrades, backups

### 11.1 Shape

One supported deployment for now: one VPS with Docker Compose (DEC-26). A managed platform (Render) is evaluated in 12.5 as a later, on-request option.

- One VPS per church: 2 GB RAM, 1 to 2 vCPU, 40 GB or more of disk, Ubuntu LTS or Debian, and Docker Engine with the compose plugin.
- Everything lives in `/opt/fold`: `docker-compose.yml`, `Caddyfile`, `.env`.
- Containers: `caddy`, `app` (`ghcr.io/grierj/fold:<FOLD_VERSION>`), `db` (`postgres:16-alpine`), and `backup` (`ghcr.io/grierj/fold-backup`: postgres:16-alpine plus `age` and `rclone`, with `backup.sh` and `restore.sh`, running a nightly loop).
- Volumes (the skeleton's names stay): `pgdata`, `uploads` (mounted at `/data`), `caddydata`, `caddyconfig`, plus a new `backups`.
- Network: one compose network with a fixed subnet, which is the default `FOLD_TRUSTED_PROXIES` (C17).

### 11.2 Environment file

`.env` holds only what must exist before the app can start, plus optional overrides:

```
DOMAIN=groups.example.church            # required; DNS A/AAAA must point at the VPS
ACME_EMAIL=office@example.church        # optional; Let's Encrypt expiry notices
FOLD_VERSION=0.5.0                      # pinned image tag; change it to upgrade
APP_ENV=prod                            # prod or dev (the skeleton's variable)
SESSION_SECRET=<openssl rand -hex 32>   # required by `serve` (hex or base64, 32+ bytes)
FOLD_SECRET_KEY=<openssl rand -hex 32>  # required from M4.1; encrypts provider secrets in the DB. BACK THIS UP.
DB_PASSWORD=<openssl rand -hex 24>      # required (Compose only)
DATABASE_URL=postgres://fold:${DB_PASSWORD}@db:5432/fold?sslmode=disable
FOLD_TRUSTED_PROXIES=172.30.0.0/24      # the compose network subnet (C17)
# Optional: lock provider settings to env instead of the wizard (env wins over the DB, C8)
# FOLD_SMTP_URL=smtp://user:pass@smtp.example.org:587   (minimal sender before the wizard, M1.2)
# FOLD_EMAIL_PROVIDER=postmark   FOLD_POSTMARK_SERVER_TOKEN=...
# FOLD_SMS_PROVIDER=telnyx       FOLD_TELNYX_API_KEY=...
# FOLD_STORAGE=s3  FOLD_S3_ENDPOINT=... FOLD_S3_BUCKET=... FOLD_S3_ACCESS_KEY=... FOLD_S3_SECRET_KEY=...
# FOLD_CLOUDFLARE_API_TOKEN=...  (DNS edit on one zone only, used once by the wizard)
# FOLD_BACKUP_KEEP_DAYS=14
```

`deploy/init-env.sh DOMAIN` writes `.env` with fresh random secrets (using `openssl` or `/dev/urandom`) and refuses to overwrite an existing file. Config is loaded lazily per subcommand (C11): `fold version` needs nothing, and `fold migrate status` needs only `DATABASE_URL`.

### 11.3 Setup wizard (D7: the app configures as much as it can)

- The first-run page (server-rendered, as today) only creates the first admin. It is shown while `SetupState` says no admin exists, read from the database on each request (not cached), so a restart or a second container cannot skip or repeat it.
- Everything after that is the **setup wizard in the SPA** (`/admin/setup`), backed by `SetupState` rows. `fold setup --config setup.yaml` runs the same steps from `internal/setup`. Each step can be re-run later from Admin > Settings.

Steps:

1. **Church and admin**: church name, time zone, first admin's name, email, and password (email is the login, DEC-23). The admin confirms they are 18 or older. Also sets the account policy: self-signup `request` (default; every self-signup and member "invite a friend" waits for admin approval) or `off` (DEC-4).
2. **Address and site**: postal address (for email footers and legal pages) and contact email. Checks that `SITE_URL` loads over HTTPS from inside the app.
3. **Email** (required for anything beyond a demo):
   - Pick a provider (Postmark is preselected, DEC-21; SMTP is the fallback) and paste an API key, or SMTP details.
   - `VerifyCredentials`, then `EnsureSendingDomain(mail.<domain>)`, which returns the DKIM, SPF/return-path, and DMARC records.
   - If a Cloudflare token is present, create the records by API. Otherwise show them in a copy-paste table.
   - Poll `DomainStatus` until verified; it continues in the background if the admin closes the page.
   - `EnsureWebhook` (bounces, complaints, deliveries, and inbound replies) with a generated secret, then send a test email to the admin.
   - Add the MX record for `reply.mail.<domain>` that the reply relay needs (7.8), by API with a Cloudflare token or in the copy-paste table.
4. **SMS** (optional; can be skipped):
   - Pick a provider (Telnyx is preselected; Twilio once M3.10 lands) and paste credentials from the **church's own** account (DEC-2). `VerifyCredentials`.
   - Ask "Does the church have an EIN?" and choose `sms_registration_path` (DEC-3, 12.2):
     - **Yes**: EIN-based standard brand (10DLC), or toll-free verification with the EIN.
     - **No**: recommend getting a free EIN from the IRS first, and pause here. On Telnyx there is no sole-proprietor option for a church (12.2). On Twilio, offer the sole-proprietor path only after showing its limits (1 campaign, 1 number, about 1,000 segments a day to T-Mobile) and that a named person, not the church, is the registered sender.
     - Never ask for 501(c)(3) proof.
   - Search for local or toll-free numbers and show the vendor's monthly price. The admin must type `BUY` to confirm before Fold calls `BuyNumber`. Fold never buys on its own.
   - `ConfigureNumber` sets the inbound and status webhooks.
   - Show the registration form pre-filled from church data and the generated legal pages (brand: legal name, EIN, address, website; campaign: use case Low-Volume Mixed or Mixed (never Charity), description, sample messages with no donation asks, opt-in flow, privacy and terms URLs). Submit by API where the provider allows it, otherwise show step-by-step instructions with a link to the vendor console.
   - Poll `RegistrationStatus`. SMS sending stays blocked, with a clear banner, until it is approved. Review typically takes days to weeks (Telnyx quotes about 72 business hours of carrier review after its own review; Twilio quotes 10 to 15 days for vetting).
   - Set the SMS send window (default 8 AM to 8 PM recipient-local, DEC-9, DEC-25), with the TCPA warning if it is set outside 8 AM to 9 PM.
5. **Storage**: local (default) or S3-compatible. Test with put, get, and delete. Offer `storage migrate` if there are already uploads.
6. **Video**: the Jitsi domain for generated links (default `meet.jit.si`). Leaders choose link-out or Jitsi per event.
7. **Backups**: show where nightly backups go. Optionally set an offsite S3 bucket and an `age` public key. Show a "write down your FOLD_SECRET_KEY" reminder and require a tick to continue.
8. **Safeguarding**: choose whether guardians get copies of messages to teens by email (`guardian_copies`, default off, email only; DEC-19, DEC-29).
9. **People**: import a CSV (name, email, phone, groups, and an optional "adult/teen" column with birth dates for teens) or invite by email. Imported adults become active accounts (they set a password from an emailed link). Imported teens start `pending_consent`. Rows under 13 are refused with a note to add them as household children. Rows that repeat an email already in the file or in Fold are offered as adult household people on that account (DEC-23). Imported phones are not opted in to SMS (10.4).
10. **Rooms and finish**: turn rooms on or off and add rooms, then go to the dashboard. The dashboard keeps a "Setup checklist" card until every step is done or skipped.

### 11.4 CLI (C11)

```
fold serve                      # default when run with no arguments (keeps today's behavior)
fold healthcheck                # used by the Docker healthcheck
fold migrate [up|status|down-one]
fold setup --config setup.yaml [--dry-run]   # non-interactive wizard; prints what it would do
fold providers test [email|sms|storage]       # sends a test / checks credentials
fold user create-admin --email ... --name ...
fold storage migrate --to s3|local
fold secrets rotate --new-key <key>           # re-encrypts provider secrets (M4.1)
fold version
```

All commands print plain text by default and JSON with `--json`, so agents can read the output. Backups are not an app subcommand; they run in the `backup` container (11.8), because the distroless app image has no shell or `pg_dump`.

### 11.5 Rules for agents doing deployment

- Never buy numbers, change paid plans, or create provider or hosting accounts without the operator's explicit confirmation in the session.
- Provider and hosting accounts belong to the church (DEC-2). If you are helping, the church creates the account and adds you as a user, or gives you an API key. Never open accounts in your own name for a church, and never ask for the church's account password.
- Use `fold setup --dry-run` first and show the operator the plan.
- Never print secrets back. Write them only to `.env` (mode 600), or through the setup API.
- Before any upgrade on Compose, run `docker compose exec backup backup.sh now`.
- See Appendix A for the full AGENTS.md Deployment section outline.

### 11.6 Hosted single-tenant (D2, DEC-2, DEC-10)

- Fold is free MIT software (DEC-11). Self-hosting is the main path. Hosting for another church is the exception: Grier hopes not to do it often, or at all.
- When someone does host: the same compose stack, one VPS per church, created by a short script against the VPS provider's API (Terraform optional), plus DNS.
- Provider accounts (email, SMS, and optionally storage and the VPS itself) belong to the church, even when hosted, unless there is a special agreement (DEC-2). The church pays vendors directly. The host may help set the accounts up, for example by joining the church's provider account as a user. This also keeps the host from being an ISV or reseller in Twilio's or Telnyx's sense, so the host has no sub-account or brand-registration duties.
- Terms and any fee are agreed case by case, and hosting is not meant to make money (DEC-10). Fold has no billing code, and none is planned.
- An ops repo keeps per-church `.env` files encrypted with SOPS or `age`. There is no shared control plane.

### 11.7 Upgrades

1. Read the release notes (`docs/UPGRADING.md` lists any manual steps, from v0.5 on).
2. Compose: run `docker compose exec backup backup.sh now`.
3. Set `FOLD_VERSION=x.y.z` in `.env`.
4. Run `docker compose pull && docker compose up -d`.

- Migrations run on start, under a Postgres advisory lock.
- Backup check: before applying pending migrations, the app reads `/backups/LAST_OK` from the `backups` volume, mounted read-only into `app`. If it is older than 24 hours, the app refuses to migrate. The check is skipped only when the database has no tables yet (a first install) or there are no pending migrations. There is no switch to turn it off.
- If a migration fails, its transaction rolls back and the app **exits non-zero** with the error in the logs. Docker restarts it and `docker compose ps` shows it unhealthy. There is no read-only maintenance mode.
- Rollback = restore the backup and pin the old version. Down migrations are best effort and not promised.
- Release images are tagged `x.y.z`, `x.y`, and `latest`. Docs always pin `x.y.z`.

### 11.8 Backup and restore

Compose:

- The `backup` service runs nightly at 02:30 church time:
  - `pg_dump -Fc` into `/backups/fold-YYYYMMDD-HHMM.dump`
  - `tar` of the `uploads` volume (skipped when storage is S3)
  - keeps the last `FOLD_BACKUP_KEEP_DAYS` (default 14) plus the first backup of each month for 6 months
  - writes `/backups/LAST_OK` after a successful run
- On demand: `docker compose exec backup backup.sh now`.
- Offsite (optional, M5.2): the `backup` container encrypts each new backup with `age` to the configured public key and uploads it to an S3-compatible bucket (B2 is a cheap choice, 12.3). The private key never touches the server.
- Restore: `docker compose stop app && docker compose exec backup restore.sh <dump> [uploads.tar] && docker compose start app`. `restore.sh` runs `pg_restore --clean` and unpacks the uploads.
- CI runs a restore test on every release: seed data, back up, restore into a fresh database, and compare row counts and a sample API response.
- The Admin > System page shows the last backup time, size, and offsite status (read from the `backups` volume), and warns when the last backup is older than 36 hours.

### 11.9 Development environment (DEC-1, DEC-24)

- `docker compose -f deploy/docker-compose.dev.yml up -d db` starts PostgreSQL 16 on `localhost:5432`, with a named volume and the `btree_gist` and `citext` extensions created by an init script.
- `make dev` runs the Go server (`APP_ENV=dev`, `DATABASE_URL` pointing at the dev database) and the Vite dev server.
- `make test` runs `go test ./...` against `DATABASE_URL` (the same variable the app uses). If it is unset, `make test` starts the dev database and sets it. The test helper migrates a template database once, then gives each test package a fresh copy (`CREATE DATABASE ... TEMPLATE`).
- `make e2e` runs Playwright against a dev server (M0.4). `npm test` in `web/` runs Vitest.
- CI uses a `postgres:16` service container. The SQLite CI job is removed.
- There is no SQLite fallback anywhere, so `APP_ENV=dev` without `DATABASE_URL` fails with a message telling you to run the dev compose file.

---

## 12. Third-party services and hosting

Prices were checked on the vendor URLs listed, as of October 2026, unless marked **pricing not verified**. Prices change, so re-check before quoting them to a church. "API setup" means Fold can configure the service through an API after the admin pastes credentials. Creating the account itself is always done by a person, and the account belongs to the church, even when someone else hosts Fold (DEC-2). Churches are assumed to be cost-conscious, not feature-seeking, so defaults favor the cheapest option that works well. Fold uses no AWS service directly, because of vendor lock-in (DEC-21, DEC-26). Vendors that run on AWS underneath are fine.

### 12.1 Email (required)

Postmark is the default (DEC-21). Fold builds the SMTP and Postmark adapters first; Resend and Mailgun adapters come when a church asks.

| Option | Price (cited) | API setup | Lock-in notes |
|---|---|---|---|
| **Postmark** (default) | Free developer tier 100 emails/mo; Basic $15/mo for 10k, then $1.80 per extra 1k; Pro $16.50/mo for 10k, then $1.30 per 1k. https://postmarkapp.com/pricing | Yes: servers, sender domains (DKIM/return-path), message streams (transactional vs broadcast), webhooks | Low. Plain REST plus SMTP. Strong deliverability, and separate broadcast streams fit 7.4. |
| **Resend** | Free 3,000/mo, capped at 100/day; Pro $20/mo for 50k. https://resend.com/pricing | Yes: domains, webhooks, API keys | Low. Simple API. The free daily cap is too low for an all-church blast. Adapter later. |
| **Mailgun** | Free 100/day; Basic $15/mo for 10k; Foundation $35/mo for 50k. https://www.mailgun.com/pricing/ | Yes: domains, DNS records, webhooks | Low to medium. Adapter later. |
| **Any SMTP** (Fastmail, Google Workspace, Microsoft 365, the church's existing mail host) | Whatever the church already pays; pricing not verified | No (Fold just sends) | None. No bounce webhooks, and sending limits are low, so it is a fallback for small churches, for M1.2 before the wizard exists, and for tests. |

Not recommended: Amazon SES. It is cheap at volume, but Grier avoids using AWS directly because of vendor lock-in, and its account, IAM, and sandbox steps are heavy for a church office (DEC-21).

### 12.2 SMS (optional, but needed for F5 and F9)

Telnyx is the default because churches are cost-conscious, not feature-seeking. Twilio is the second adapter (M3.10). Plivo is listed for comparison; an adapter only if a church asks. None is required: Fold runs without SMS.

| Option | Price (cited) | API setup | Notes |
|---|---|---|---|
| **Telnyx** (default) | $0.004 per message part plus carrier fees; numbers from $1/mo. https://telnyx.com/pricing/messaging (Telnyx's 10DLC guide lists a local SMS number at $1.10/mo: https://telnyx.com/resources/what-is-10dlc). 10DLC: brand registration $4.50 one-time; campaign review $15 per submission; campaign fee $1.50/mo (Low-Volume Mixed), $10/mo (standard use cases), or $2/mo (sole proprietor), with the first 3 months billed up front. https://support.telnyx.com/en/articles/5634625-10dlc-fees-and-charges. Optional standard brand vetting $41.50. https://developers.telnyx.com/docs/messaging/10dlc/brand-registration | Yes: numbers, messaging profiles, 10DLC brand and campaign API | About 40 to 50% cheaper per segment than Twilio. Smaller ecosystem. |
| **Twilio** | $0.0083 per outbound segment plus carrier fees (AT&T $0.0035, T-Mobile $0.0045, Verizon $0.005 per segment). Long-code number $1.15/mo; toll-free $2.15/mo. https://www.twilio.com/en-us/sms/pricing/us. 10DLC: brand registration $4.50 one-time, standard brand vetting $41.50, campaign vetting $15 one-time; monthly campaign fees pricing not verified. https://www.twilio.com/docs/messaging/compliance/a2p-10dlc | Yes: number search and purchase, webhooks, Trust Hub brand and campaign registration, toll-free verification | Most documentation and community help. Built-in STOP handling. |
| **Plivo** | $0.0077 per message (US); long code $0.50/mo; toll-free $1/mo. https://www.plivo.com/sms/pricing/us/ | Yes: numbers, applications, 10DLC API | Cheaper numbers. Carrier pass-through fees apply; pricing not verified for the exact amounts. |

**The 10DLC burden, in plain words.** US carriers block business texting from ordinary local numbers unless the sender is registered with The Campaign Registry (TCR). A church has to:

1. Register a **brand**: legal name, EIN, address, website.
2. Register a **campaign**: what the messages are for, sample messages, how people opt in, and links to a privacy policy and SMS terms (Telnyx's campaign guidance asks for both; Twilio requires both URLs on new campaigns since June 30, 2026; Fold generates both pages for every provider).
3. Wait for review. Telnyx reviews first, then carriers take about 72 business hours. Twilio quotes 10 to 15 days for vetting. Rejections are common when the website, opt-in, and samples don't match.
4. Live with throughput caps tied to the brand type, use case, and vetting score.

**Registration paths for a church (DEC-3).** Fold assumes the church is **not** a 501(c)(3) organization. It never asks for 501(c)(3) proof and never picks a charity use case (Twilio says its `CHARITY` use case does not include religious organizations; Telnyx's Charity use case covers fundraising and donations, which Fold keeps out of SMS).

| Path | Needs | Limits | When to use |
|---|---|---|---|
| **A. EIN-based standard brand (10DLC)**, default | The church's legal name and EIN, address, website (Fold's pages are enough) | Telnyx: T-Mobile daily caps by brand vetting tier, from 2,000 a day (Basic) to 10,000 (Medium) and higher; Low-Volume Mixed campaigns are limited to 75 messages a minute. Twilio Low-Volume Standard: up to 2,000 segments a day to T-Mobile (about 6,000 across US carriers), up to 5 campaigns. | Almost every church. Use case: Low-Volume Mixed (cheapest monthly fee) unless the church sends more than about 2,000 segments a day to T-Mobile, then Mixed. |
| **B. Toll-free verification with an EIN** | A toll-free number; the EIN as business registration number (required for all business types except sole proprietors on new Twilio submissions since February 17, 2026) | Provider-specific; no TCR campaign fees | When 10DLC is rejected, or the church prefers one national number. |
| **C. Sole-proprietor 10DLC (no EIN)**, Twilio only | A real person's name (for example the pastor's), address, email, and a mobile number that gets a one-time code. | 1 campaign and 1 number per brand; 1,000 segments a day to T-Mobile; 15 messages a minute (Twilio and Telnyx both document these limits). | Last resort, and **not available on Telnyx for a church**: Telnyx says an entity whose name or website contains "Church", "Ministry", "Ministries", "Group", "Center", "Charity", or ".org" does not qualify as a sole proprietor and must register a standard brand with an EIN. On Twilio, the named person is the registered sender, and a church may still be rejected. |
| **D. Toll-free as a sole proprietor (no EIN)**, Twilio only | Business type `SOLE_PROPRIETOR`, so no registration number is needed (Twilio) | Provider-specific | Same caveats as C. |

If a church has no EIN, Fold recommends getting one before any of this; on Telnyx it is required. The IRS issues EINs free, online, in minutes, and says a church must use an EIN for any tax-related purpose even without employees (https://www.irs.gov/businesses/small-businesses-self-employed/get-an-employer-identification-number, https://www.irs.gov/pub/irs-pdf/fss4.pdf). Paths C and D are offered only on Twilio, and only after the wizard shows their limits.

Fold adapts to the path:
- **Daily caps.** The SMS adapter's capabilities include the path's daily caps, and the pipeline spreads sends across days when needed. The composer warns, for example: "This text goes to 2,400 people; about 400 will go tomorrow because of the carrier's daily limit."
- **No donation asks.** Messages and samples avoid donation asks, which can trigger charity classification and 501(c)(3) demands (Twilio error 30487: "Charity must provide 501(c)(3) verification").

**Cost example**: one church sending 800 SMS segments a month comes to about $7 on Telnyx (800 × ($0.004 + about $0.004 carrier fee)) plus $1 to $1.10 for the number, plus $1.50 a month for a Low-Volume Mixed campaign and the one-time fees ($4.50 brand, $15 per campaign review). On Twilio, about $10 plus $1.15 for the number, plus its campaign fees (not verified). Estimates only.

### 12.3 Object storage (optional; local disk is the default)

| Option | Price (cited) | API setup | Notes |
|---|---|---|---|
| **Local disk / VPS block volume** | Included with the VPS; block volume pricing per vendor, not verified | n/a | Zero lock-in. Default. |
| **Backblaze B2** | $6.95 per TB per month; first 10 GB free; free egress up to 3× the amount stored. https://www.backblaze.com/cloud-storage/pricing | Yes: S3 API plus native API for buckets and keys | Recommended for offsite backups. |
| **Cloudflare R2** | $0.015 per GB per month; 10 GB free; no egress fees. https://developers.cloudflare.com/r2/pricing/ | Yes: S3 API; buckets and tokens via the Cloudflare API | Recommended for public assets (galleries) because there are no egress fees. |
| **Wasabi** | $7.99 per TB per month, 1 TB minimum charge, 90-day minimum retention. https://wasabi.com/pricing | Yes: S3 API | The 1 TB minimum makes it poor value for one small church. |
| **Vultr Object Storage** | From $18/mo (1 TB). https://www.vultr.com/pricing/ | Yes: Vultr API plus S3 API | Convenient if the VPS is on Vultr. |
| **MinIO (self-hosted)** | Free software | S3 API | Used in CI. Not recommended for production on the same VPS (it adds nothing over local disk). |

All of these work with one S3-compatible adapter, so switching is a config change plus `fold storage migrate`.

### 12.4 Video (F11; link-out is the default)

| Option | Price (cited) | API setup | Notes |
|---|---|---|---|
| **Link-out** (any Zoom, Meet, Teams, or Jitsi URL) | $0 from Fold | n/a | Zero lock-in. Default. |
| **meet.jit.si generated links** | Free. https://jitsi.org/jitsi-meet/ (no price page) | No API needed; Fold makes a random room URL | Embedding meet.jit.si inside another site is reportedly limited to 5 minutes (secondary source; not verified on a Jitsi page). Link-out avoids this. |
| **8x8 Jitsi as a Service (JaaS)** | Free up to 25 monthly active users; $99/mo for 300; $499/mo for 1,500; $0.99 per extra MAU. https://jaas.8x8.vc/#/pricing | Yes: JWT-signed rooms | Not used: embedded video is out of scope for now (DEC-16). Listed in case a later revision revisits it. |
| **Zoom** | Basic free (40-minute meetings); Pro $16.99/mo monthly or about $14.16/mo billed yearly (from a search summary; pricing not verified on the vendor page). https://zoom.us/pricing | Yes: Server-to-Server OAuth app, meeting create API | Many churches already have Zoom. Later adapter. |
| **Google Meet** | Needs Google Workspace for API meeting creation; pricing not verified | Reportedly yes (Meet REST API); not verified | Later, if requested. |
| **Self-hosted Jitsi** | Second VPS; pricing per VPS vendor | n/a | Out of scope: it breaks D5 (no extra deployment complexity). |

### 12.5 Hosting

Fold runs on one VPS with Docker Compose. That is the only supported deployment for now (DEC-26).

| Option | Price (cited) | API setup | Notes |
|---|---|---|---|
| **DigitalOcean** (VPS) | 1 GB $6/mo; 2 GB $12/mo. https://www.digitalocean.com/pricing/droplets | Yes: full API, cloud-init | Good docs; US data centers. |
| **Vultr** (VPS) | Regular 1 GB $5/mo, 2 GB $10/mo; High Performance 1 GB $6/mo, 2 GB $12/mo. https://www.vultr.com/pricing/ | Yes: full API | Also offers object storage on the same account. |
| **Hetzner** (VPS) | After the June 15, 2026 price change: US CPX11 $20.49/mo excluding a $0.60 IPv4 fee; EU CX23 $6.49/mo. https://docs.hetzner.com/general/infrastructure-and-availability/price-adjustment/ | Yes: full API | Very good value in the EU; US plans are now much dearer. |
| **Akamai/Linode** (VPS) | 1 GB $5/mo, 2 GB $12/mo per third-party trackers; pricing not verified (the vendor page blocked access) | Yes: full API | Fine alternative. |
| **Church-owned box** (old PC, mini PC, on-site server) | Hardware the church owns | n/a | Supported for power users; needs ports 80/443 open or a tunnel. |

**Later / on request: Render (managed PaaS).** Not supported yet and not in the committed roadmap (DEC-26). This evaluation and the `render.yaml` outline in Appendix A are kept as reference, for when a church asks or when Grier wants an easy path to host for others (future M5.5). Facts are from Render's own pages, October 2026. Pricing: Hobby workspace $0 + compute; Pro workspace $25/mo + compute. Web service 0.5 CPU / 512 MB $7/mo (1 CPU / 2 GB $25/mo). Postgres 0.1 CPU / 256 MB $6/mo, 0.5 CPU / 1 GB $19/mo, plus storage at $0.30/GB/mo. Persistent disk $0.25/GB/mo. Bandwidth: 5 GB included on Hobby (25 GB on Pro), then $0.15/GB. https://render.com/pricing. Configurable by REST API and `render.yaml` Blueprints (https://render.com/docs/api, https://render.com/docs/blueprint-spec).

- **Typical cost for one small church** (Hobby workspace, prices from https://render.com/pricing):
  - Web service 0.5 CPU / 512 MB: $7. Fold's single Go binary fits.
  - Postgres 0.1 CPU / 256 MB: $6, plus about $1.50 for 5 GB of storage.
  - Disk, 5 GB at `/data` for uploads: $1.25.
  - Total: **about $16/mo**. With the 1 GB Postgres plan ($19), about $29/mo.
  - The Hobby workspace has one team seat. A helper who needs their own login requires Pro, which adds $25/mo (about $41 to $54/mo in total). Otherwise the church shares an API key with the helper, not its password.
  - A 2 GB VPS is $10 to $12/mo, plus a few cents for offsite backups (12.3).
- **Free tier: not suitable.** Free web services spin down after 15 minutes idle (about a minute to wake) and cannot have disks. Free Postgres expires after 30 days and has no backups. Free web services also cannot send on ports 25, 465, or 587, which SMTP uses (https://render.com/docs/free). Postmark's HTTPS API is not affected.
- **Containers and background jobs: yes.** A web service can run a prebuilt image from GHCR (`runtime: image`) or build the Dockerfile. Fold's job worker runs inside the web service process, which is always on for paid plans, so it needs no separate worker service. Render also offers background workers and cron jobs, billed per minute, if ever needed.
- **Persistent disk** (https://render.com/docs/disks):
  - Paid services only, with daily snapshots kept at least 7 days.
  - A service with a disk is limited to **one instance**, and deploys have **a few seconds of downtime** (zero-downtime deploys are turned off).
  - Disks are not reachable from pre-deploy commands, one-off jobs, or cron jobs, and can grow but not shrink.
  - For one church, one instance is fine. To keep zero-downtime deploys, use S3-compatible storage (B2 or R2, 12.3) instead of a disk once M4.7 ships.
- **Managed Postgres** (https://render.com/docs/databases, https://render.com/docs/postgresql-backups, https://render.com/docs/postgresql-extensions):
  - Versions 13 to 18 are available (18 is the default). Pin `postgresMajorVersion: "16"` to match CI.
  - The `citext` and `btree_gist` extensions Fold needs are supported.
  - Paid databases get point-in-time recovery: 3 days on Hobby, 7 days on Pro. A recovery restores into a new database. Logical exports are kept 7 days, and `pg_dump` works over the external URL.
- **Custom domains and TLS: yes** (https://render.com/docs/custom-domains). Certificates are automatic. Hobby includes 2 custom domains. Render serves over IPv4, so any AAAA record for the Fold host must be removed.
- **Agent-deployable: yes.** A `render.yaml` Blueprint can declare the web service (image, plan, region, disk, health check path, domains, env vars, generated secrets) and the database (version, plan, disk size, IP allow list), and wire `DATABASE_URL` from the database. Blueprints can be validated before use, and the REST API covers services, databases, deploys, env groups, Blueprints, and custom domains. Secrets marked `sync: false` are prompted for only when the Blueprint is first created, so the operator types provider keys into Render's dashboard, not into the repo.
- **Lock-in: low.** Fold's image, standard Postgres (`pg_dump`), and S3 storage all move. Only `render.yaml` and Render's environment settings are Render-specific. Moving to a VPS is `pg_dump` + `pg_restore` plus copying the uploads.
- **Fit with Fold's goals:**
  - Simple for a power user: yes, arguably simpler, because there is no OS, firewall, Caddy, or backup container to look after.
  - Church owns its accounts (DEC-2): yes, if the church creates the Render workspace and pays Render directly.
  - Few dependencies (D3, D6): it is one vendor for compute, database, and TLS, instead of a VPS.
  - Things Fold must handle: trust Render's proxy for client IPs (C17); turn off the Compose backup check (`FOLD_BACKUP_CHECK=off`); accept the generated secret format (base64) for `SESSION_SECRET` and `FOLD_SECRET_KEY`; read `PORT`.

**If Render is added later (M5.5):**

- A VPS with Compose stays the default either way: cheapest (about $10 to $12/mo) and fully under the church's control.
- Render would suit a church where nobody wants to look after a Linux server and about $16 to $29/mo (more on Pro) is acceptable for managed TLS, Postgres backups with point-in-time recovery, and push-to-deploy. Prefer S3 storage over a disk there, so deploys stay zero-downtime.

### 12.6 DNS and TLS

| Option | Price (cited) | API setup | Notes |
|---|---|---|---|
| **Caddy automatic HTTPS** (Let's Encrypt / ZeroSSL) | Free (open source). https://caddyserver.com/docs/automatic-https | n/a; automatic | Already in the repo. Keep it. |
| **Cloudflare DNS** | Free plan $0. https://www.cloudflare.com/plans/ | Yes: DNS records API with a scoped token | Optional. Lets the wizard create email DNS records automatically. Use DNS-only (grey cloud) for the app host so Caddy can get its certificate the simple way. |
| **Any registrar DNS** | Whatever the church pays now | Usually no | The wizard shows records to copy and checks them by DNS lookup. |

### 12.7 Recommended picks

- **Email**: Postmark by default (DEC-21): good deliverability, broadcast streams, simple setup. Any SMTP as the fallback. Resend or Mailgun when a church prefers them (adapter added on request).
- **SMS**: Telnyx by default, because churches are cost-conscious rather than feature-seeking. Twilio second (most documentation, built-in compliance tools, and a sole-proprietor path). Plivo only on request. All go through the same adapter interface, and none is required.
- **Storage**: local disk by default. Backblaze B2 for offsite backups. Cloudflare R2 when public galleries grow.
- **Video**: link-out plus generated Jitsi links. Zoom adapter later.
- **Hosting**: a VPS with Compose by default: DigitalOcean or Vultr (2 GB) in the US, Hetzner in the EU. This is the only supported deployment for now; Render is a later, on-request option (12.5, DEC-26).
- **DNS/TLS**: Caddy for TLS. Cloudflare DNS optional, for automatic email records.
- **AWS check (DEC-21, DEC-26)**: Fold uses no AWS service directly. "S3-compatible" storage is only a protocol (B2, R2, and the others are not Amazon). Vendors that run on AWS underneath are fine.

---

## 13. Extra features from research

### 13.1 What ChurchTrac and Meetup offer (research summary)

- **ChurchTrac** (https://www.churchtrac.com/pricing): plans from $29/mo for up to 75 names to $119/mo for unlimited. "Enhanced Messaging" is $7 for 250 credits. The all-in-one product covers people and households, a member directory, groups, events and calendar, room and resource scheduling, attendance, child check-in kiosks with name tags, volunteer scheduling, forms (prayer requests, connection cards, sign-ups), email and text messaging, online giving, contribution statements and fund accounting, worship planning, a website builder and member app, MinistrySafe background-check integration, and automations.
- **Meetup** (https://help.meetup.com): groups with organizers and co-hosts; repeating events that auto-announce 6 days and 1 day before; an attendee limit with a waitlist that auto-moves people in (skipping RSVPs whose guests don't fit); up to 5 guests per RSVP; RSVP open and close times; custom RSVP questions; hybrid events with separate in-person and online limits; check-in mode (from 1 hour before to 24 hours after); discussions and photo albums; and QR check-in on its 2026 roadmap.

### 13.2 Recommendations

| Feature | Tag | Why |
|---|---|---|
| Recurring events with per-date changes | **Core** | Every small group meets weekly. Already partly built. |
| RSVP with reminders (email 24 h, SMS 2 h) | **Core** | Meetup's main value; cuts no-shows. |
| Capacity, waitlist, auto-promotion | **Core** | F10. |
| Guest registration for public events (no account) | **Core** | Visitors are the point of public events. |
| Hybrid capacity (separate remote limit) | **Core** | Pairs with F12 (remote flag). |
| RSVP open/close windows; +N guests (0 to 5) | **Core** | Cheap to build; Meetup parity. |
| Simple attendance (leader ticks a roll) | **Core** | Churches track attendance; ChurchTrac does. |
| Volunteer and potluck sign-up sheets (one model: sheets, slots, claims) | **Core** (M6.2) | ChurchTrac volunteer scheduling and forms. High value, small model. |
| ICS feeds and add-to-calendar | **Core** | F2. |
| Church directory with per-field privacy | **Later** | Wanted, but privacy-sensitive (10.2). |
| QR / self check-in | **Later** | Meetup roadmap item; builds on attendance. |
| Prayer requests (group or church-wide, optional anonymous, expiring) | **Later** | Very common church use; ChurchTrac form. |
| Group discussion board (posts and comments) | **Later** | Meetup discussions; replaces group-text sprawl. |
| Photo galleries (private by default) | **Later** | Needs private assets and minors' photo rules. |
| Households, household people (children, teens, and adults who share an email), guardian links, teen consent, Teen and Child badges | **Core** (M1.9, M1.10) | Teens 13 to 17 need consent, children under 13 exist only as household people (DEC-8), and shared emails need a household (DEC-23). |
| Pruning by leaders (no sign-up approval step) | **Core** (M1.5) | Leaders watch their own attendee lists and remove people, including teens (DEC-20, DEC-30). |
| Moderator role (between member and leader) | **Later** (note only) | Could prune attendee lists without editing the group. Do not build yet (DEC-17). |
| Guardian copies of messages to teens | **Core** (setting, off by default; M3.5) | Many safe-church policies expect it; the church decides (DEC-19). |
| Background-check status flags on volunteer slots | **Later** | Child safety. Stores status only, never reports. |
| Child check-in kiosk with name tags | **Later** | High value for churches with nurseries, but needs hardware, printing, and security codes. A big milestone of its own. |
| Contact proxy: one-to-one messages and relayed replies; no teen contact details for leaders | **Core** (M1.11, M3.11) | Safeguarding and privacy (DEC-31). |
| Two-way SMS inbox (shared, with history) | **Later** | Replies relay to the author through Fold for now (7.8). |
| Keyword auto-replies ("text CAMP for info") | **Later** | Cheap once inbound SMS exists. |
| Guest follow-up workflow (first-time visitor tasks) | **Later** | ChurchTrac automations-lite. |
| Custom registration questions | **Later** | Meetup RSVP questions (dietary needs, T-shirt size). |
| ChurchTrac CSV import | **Later** | Eases switching. Needs a real sample ChurchTrac export before any work starts; nobody has one yet (DEC-15). |
| OIDC sign-in (Google/Microsoft) | **Later** | Nice; adds a dependency, so optional. |
| Admin TOTP | **Core** (M4.9, v0.5) | Fold is on the public internet (final review, finding 7). |
| Durable, tamper-evident audit log | **Later** (M6.14) | Legal and safeguarding record (DEC-33, 10.7). |
| PWA with web push | **Later** | Push without app stores. |
| Multi-campus (one church, several sites) | **Later** | Single-tenant still; rooms per campus. |
| Spanish or any other translation; an i18n framework | **Out of scope** | No plans for a non-English version. Plain English strings in the code are fine (DEC-13). |
| Embedded video calls inside Fold | **Out of scope** for now | Link-out and generated Jitsi links only. Revisit in a later revision if requested (DEC-16). |
| Paid event registration | **Out of scope** | Link out to the church's payment page instead. Same reasons as giving. |
| Online giving, contribution statements, fund accounting | **Out of scope** | See 13.3. A "Give" link in the menu is fine. |
| Worship planning (songs, service order) | **Out of scope** | Planning Center and others do this well. Not social. |
| Website and app builder | **Out of scope** | Fold is the social layer; link from the church site. |
| Automated phone calls (phone tree) | **Out of scope** | Another provider and more TCPA rules. |
| Running background checks | **Out of scope** | Use MinistrySafe or a similar service; Fold stores only the status. |

### 13.3 Why giving and accounting stay out

- **Dependency**: giving needs a payment processor (Stripe or similar). That is a new proprietary dependency with its own onboarding, KYC, fees, and lock-in, which goes against D3 and D6.
- **Risk**: handling money means PCI scope, refunds, disputes, fraud, and payouts. A bug becomes a financial loss, not a UI glitch.
- **Paperwork**: churches need year-end contribution statements that meet IRS acknowledgment rules, plus fund accounting (restricted funds, budgets, bank reconciliation). That is a whole accounting product with legal stakes.
- **Already solved**: most churches already have a giving platform and a bookkeeper. Replacing them is a hard sell, while replacing the social features is the gap Grier named.
- **Scope**: it would roughly double the project and slow the core.

What Fold does instead: a configurable "Give" link in the menu and emails, and link-outs for paid events. Revisit only if many churches ask, and then as an optional adapter.

---

## 14. Phased roadmap and milestones

### 14.1 Rules for every milestone

- One milestone = one pull request, sized for one agent session. If it grows, split it and update this file.
- Before starting, read `AGENTS.md`, this plan's sections for the milestone, and the "Depends on" milestones' code.
- Clean break (DEC-22): no milestone needs data-migration code or fixture tests for old data. Before v0.5, nobody runs Fold, so schema changes are new migrations on top of the baseline and nothing else.
- Done means:
  - [ ] `go vet ./...` and `go test ./...` pass against PostgreSQL 16 (`make test` locally, a Postgres service container in CI). There is no SQLite job (DEC-1, DEC-24)
  - [ ] `npm run build` passes in `web/`; from M0.4 on, `npm test` (Vitest) passes too, and any Playwright test the milestone lists passes in CI
  - [ ] Schema changes are new goose migrations, never edits to old ones, and the `AutoMigrate` drift check shows no drift (C1)
  - [ ] `docs/API.md` and the SPA types (`web/src/types.ts`) updated for any API change
  - [ ] Tests use the `devoutbox` providers and `httptest` fakes; never live services
  - [ ] Permissions go through `internal/policy`, with table tests for new rules
  - [ ] No Python anywhere
  - [ ] Checklist boxes in this file ticked in the same PR

### 14.2 Releases

| Release | Contents | Milestones |
|---|---|---|
| v0.2 Foundations | Postgres-only clean baseline, CLI, trusted proxies, CSRF route groups, visibility enum and policy, email login, member removal and sole-leader handoff, security headers and dependency scanning, frontend test tooling | M0.1–M0.4 (M0.3 is M0.3a + M0.3b) |
| v0.3 Events and people | Events and occurrences (time zones, series edits, church-wide hosts, visibility), email basics, account requests, guests, waitlist and pruning, calendar, ICS, video and flags, households and teen accounts, contact privacy | M1.1a–M1.11 |
| v0.4 Email | Jobs, Postmark and webhooks, consent, composer, pipeline (with guardian copies), email reminders, one-to-one messages and reply relay | M3.1–M3.6, M3.11 |
| v0.5 MVP | Encrypted settings, SPA setup wizard, legal pages, email provisioning, people import, admin two-factor sign-in, images, backups, upgrades, deploy docs: the first version a church can run | M4.1–M4.5, M4.9, M5.1–M5.4 |
| v0.6 Rooms | Rooms, by-room view and feed, suggestions | M2.1–M2.3 |
| v0.7 SMS | Telnyx, keywords and revocation, send window and caps, SMS reminders, member requests, SMS provisioning | M3.7–M3.9, M4.6 |
| v0.8 Storage and CLI | S3 and private assets, `fold setup` | M4.7, M4.8 |
| v1.0 | Twilio adapter, hardening, sign-up sheets, attendance, security review | M3.10, M6.2, M6.3 + fixes |
| v1.x and later | Community features; the durable audit log (DEC-33) | Rest of M6, including M6.14 |
| Future, on request (not committed) | Render Blueprint (DEC-26) | M5.5 |

Rooms (M2) can be built in parallel with M3 once M1.1a is merged.

Suggested first slice (from the reviews): M0.1, then M0.2, then M0.3, then M1.1a and M1.1b, then M1.2 and M1.3. Run M0.3 (email login) after M0.2, not in parallel: both rewrite handlers and the test helpers (`newTestUser` in `auth_test.go` uses `Username`). M0.4 (test tooling) can run in parallel with M0.2 and M0.3.

### 14.3 Phase 0: Foundations

**M0.1 PostgreSQL only, clean baseline, CLI, request plumbing**
- Depends on: none
- Scope:
  - Remove SQLite (C14, DEC-1, DEC-24): delete the `glebarez/sqlite` dependency, every SQLite branch in `db.go` and `config.go`, and `SQLITE_PATH` in the Dockerfile and README. Add `deploy/docker-compose.dev.yml` and a **new** `Makefile` with `dev`, `test`, and `lint` (11.9). Tests use `DATABASE_URL`.
  - Point CI at Postgres only, and add the drift check (C1): migrate a throwaway Postgres database with goose, run `AutoMigrate` against it for real with a logger that records every statement, fail on any `CREATE` or `ALTER`, and drop the database afterward. Do not use GORM's `DryRun` (its migrator needs real query results).
  - Add goose with embedded SQL migrations and a clean baseline migration for the current models (DEC-22). There is no stamping of old databases and no upgrade note for PR #7.
  - CLI subcommands `serve`, `healthcheck`, `migrate`, `version`, with lazy config: `version` needs no env; `migrate status` needs only `DATABASE_URL` (C11).
  - Trusted proxies (C17): give the compose network a fixed subnet and trust only it by default (`FOLD_TRUSTED_PROXIES`).
  - CSRF route groups (C17): create the router groups so `/hooks/*` and `POST /u/unsubscribe/<token>` are outside the CSRF middleware (their handlers come later), with a test that the exempt list is exactly those.
  - Update `AGENTS.md`: Postgres for dev and tests; SMS through adapters with Telnyx as the default and Twilio next, none required (C13); no Python. Fix the README Go version.
- Acceptance:
  - [x] `go.mod` has no SQLite driver, and `rg -i sqlite` finds nothing outside this plan
  - [x] `APP_ENV=dev` without `DATABASE_URL` exits with a clear "start the dev database" message
  - [x] A fresh database migrates to the baseline, and the drift check reports no drift
  - [x] Adding a model field without a migration makes the drift check fail (a self-test proves the check works)
  - [x] `fold version` works with no environment variables; `fold migrate status` works with only `DATABASE_URL`
  - [x] `fold` with no arguments still serves
  - [x] Migrations take a Postgres advisory lock, so two app containers starting together don't both migrate
  - [x] `ClientIP()` returns the address the trusted proxy forwards, and ignores `X-Forwarded-For` sent from outside the trusted subnet (test with a fake proxy)
  - [x] A POST to `/hooks/test` without a CSRF token is not rejected by CSRF; any other POST without one is
  - [x] The whole existing test suite passes on Postgres via `make test` in under 2 minutes on a laptop
- Tests: migration on an empty database; drift-check self-test; advisory-lock test; trusted-proxy test; CSRF exemption test; the existing suite.

**M0.2 Visibility enum, policy package, leadership by membership, audit log**
- Depends on: M0.1
- Scope: one `visibility` enum on groups (C3). Create `internal/policy` and move all existing checks there. Leadership comes only from `Membership.role=leader`; replace every `Group.LeaderID` check (about 16 places, for example `api_groups.go:543`) with policy calls, and keep `LeaderID` as the displayed primary contact (C12). Update the SPA (`inviteOnly` in `groupSummaryJSON` in `api.go`, `web/src/types.ts:31`) and `docs/API.md`. Create the `AuditLog` table (5.3) and one `audit.Write` helper that every later milestone uses. Making or removing a leader writes a `membership.role_changed` row and shows the admins an in-app notice (DEC-31; no admin gate).
- Acceptance:
  - [x] Every handler's permission check calls `policy`; `rg LeaderID` finds it only in models, display code, and tests
  - [x] Invite-only groups return 404 to non-members, and admins see every group in lists and by id (DEC-5)
  - [x] A group can have several leaders; removing the last leader is blocked; a teen cannot be made a leader (DEC-18; enforced once teen accounts exist in M1.10, with the policy rule written now)
  - [x] The SPA shows and edits the three visibility values; the old `inviteOnly` field is gone from the API
  - [x] The `AuditLog` table exists; making or removing a leader writes a row (actor, group, person, old and new role, IP) and shows admins an in-app notice
- Tests: policy table tests covering every row of section 6 that exists so far; audit-row tests for leader changes.

**M0.3 Log in by email; leaders remove members; sole-leader handoff; security headers and dependency scanning**
- Depends on: M0.2 (run after it; both rewrite handlers and test helpers)
- Size: this milestone grew on Oct 9, 2026 (DEC-35 to DEC-37) and is now too big for one session. **Build it as two PRs, M0.3a then M0.3b**, each with its own STATUS.md row (the PR that claims M0.3a splits the row). References to "M0.3" elsewhere in this plan mean M0.3a (email login, throttling, sessions) unless they name M0.3b. M0.3b depends only on M0.2, so it may go first if Grier wants member removal sooner, but don't run the two in parallel: both change the admin user handlers, the roster, and the test helpers.

**M0.3a Log in by email**
- Depends on: M0.2
- Scope: C16 and DEC-23. Drop `Username`; `User.email` becomes `citext` NOT NULL unique; `password_hash` nullable. Login, the first-admin page, admin user creation, and the SPA use email. Throttling is keyed by email and by client IP, stored in Postgres. Login never says whether an email has an account. Changing a password or role, or disabling an account, bumps `session_version` (10.1).
- Acceptance:
  - [ ] Sign-in works with the email in any letter case, and fails for a user with no password set
  - [ ] Creating a second account with the same email (any case) is refused
  - [ ] There is no `username` column, field, or form anywhere (`rg -i username` finds nothing outside this plan)
  - [ ] Ten failed logins for one email lock that email for 15 minutes across IPs; fifty from one IP lock that IP
  - [ ] An unknown email and a wrong password get the same response in about the same time
  - [ ] Changing a password or role, or disabling an account, ends the person's other sessions
- Tests: API tests for login, creation, and throttling.

**M0.3b Leaders remove members, sole-leader handoff, security headers, dependency scanning**
- Depends on: M0.2 (and M0.3a if it lands first; rebase the roster and admin-user handlers onto it)
- Scope:
  - **Leaders remove members (DEC-35).** `DELETE /api/v1/groups/:id/members/:userId` (optional `{note}`) for the group's leaders and admins. The rule lives in `internal/policy` (extend `CanRemoveMember` with the target person):
    - Admins may remove anyone; leaders only from groups they lead.
    - Nobody removes the group's last leader (they hand off first, as in M0.2).
    - A leader cannot remove an admin, and for consistency cannot demote an admin who co-leads the group (`CanChangeRole`). Only another admin can.
    - A leader may remove a co-leader (not the last), the same as demoting one. Removing yourself is leaving (`POST /groups/:id/leave`), with the same rules.
    - Teens are removed like anyone else.
  - Removal sets `Membership.status=removed` instead of deleting the row. A removed person can't rejoin on their own: join answers 403 "A leader removed you from this group". A leader or admin can add them back, which sets `active` again. An invite-only group becomes 404 to them at once. They aren't notified in this milestone, because there is no email yet. The admin endpoint `DELETE /admin/groups/:id/members/:userId` uses the same path.
  - Every removal writes one `membership.removed` audit row (actor, group, person, old role, optional note, IP). When the removed person was a leader, admins get the same in-app `leadership` notice as in M0.2 (DEC-31). The note goes only into the audit row and is never shown to the removed person. The SPA adds "Remove from group" with a confirm on roster rows for leaders and admins.
  - **Sole-leader handoff when an account is deleted or disabled (DEC-36).**
    - `GET /api/v1/admin/users/:id/sole-leader-groups` lists every group, in any status, where the account is the only active leader. For each group it gives the candidate successors: the group's other active adult members, and every admin (the deleting admin included).
    - `DELETE /api/v1/admin/users/:id` (and disabling, once account status exists in M0.3a) takes `{successors: {"<groupId>": <userId>}}`. It refuses with 400 and the list of groups when any sole-led group is missing a valid successor.
    - In one transaction it makes each successor a leader (adding an admin as a member if needed), sets the primary contact, ends the departing account's memberships, and deletes or disables the account.
    - Audit rows: one `membership.role_changed` per group (meta `reason: "account_deleted"` or `"account_disabled"`, plus the departing user's id), and one `user.deleted` or `user.disabled` row.
    - The SPA's delete or disable dialog lists each group with a successor picker that defaults to the deleting admin.
    - `leaderCount` counts only active accounts.
  - **Security headers (DEC-37, 10.1, 10.6).** The app sets them itself, so they hold behind any proxy:
    - `Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: https:; connect-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'self'`. `'unsafe-inline'` is for React `style` attributes (focal points), and `img-src https:` is for cover images given by URL. Tighten both when uploads become the only source (M4.7).
    - `Strict-Transport-Security: max-age=31536000` when `COOKIE_SECURE` is on (never in dev).
    - Keep `X-Content-Type-Options: nosniff`, `Referrer-Policy: same-origin`, and `X-Frame-Options: SAMEORIGIN`. Add `Permissions-Policy: camera=(), microphone=(), geolocation=()`.
    - The server-rendered `/setup` page must work under the same CSP (no inline scripts).
    - Caddy and image-level headers stay with M5.1.
  - **Dependency scanning (DEC-37, 10.6).**
    - `.github/dependabot.yml`: weekly updates for Go modules, npm (`/web`), GitHub Actions, and the Dockerfile base images.
    - A CI job runs `govulncheck ./...` (pinned version) and `npm audit --omit=dev --audit-level=high` in `web/`. Both must pass.
    - Fix or upgrade anything they flag in the same PR. Note any finding that can't be fixed in the PR body with the reason.
    - Pinned image digests in releases stay with M5.1.
- Acceptance:
  - [ ] A leader removes a member and a co-leader of their own group; each removal writes a `membership.removed` row with actor, group, person, old role, and IP
  - [ ] A leader can't remove (or demote) an admin, can't remove the last leader, and gets 404 for a group they can't see and 403 for a visible group they don't lead
  - [ ] A removed member can't rejoin by themselves, a leader can add them back, and an invite-only group is 404 to them right after removal
  - [ ] Removing a leader shows admins an in-app notice; the note is in the audit row and nowhere in the removed person's API responses
  - [ ] Deleting (or disabling) the only leader of two groups without successors is refused with both groups listed; with successors (one group member, one the deleting admin by default) it succeeds, both groups keep a leader, and the audit rows are written
  - [ ] A successor must be an active adult member of that group or an admin; anyone else is refused
  - [ ] Every response carries the CSP, `nosniff`, referrer, frame, and permissions headers; HSTS is present only with `COOKIE_SECURE`; the SPA and `/setup` load with no CSP violations in a browser
  - [ ] Dependabot config is in the repo, and the `govulncheck` and `npm audit` CI steps pass
- Tests: policy table tests for the new `CanRemoveMember` and `CanChangeRole` cases (admin target, last leader, co-leader, self); API tests for removal, rejoin, audit rows, and the handoff (refusal, defaults, invalid successor); a header test on an API route, the SPA shell, and `/setup`.

**M0.4 Frontend and browser test tooling**
- Depends on: M0.1
- Scope: add Vitest (with Testing Library) and an `npm test` script to `web/`; add Playwright with one smoke test (sign in, see the groups page) under `web/e2e`; `make e2e`; a CI job that installs Chromium and runs both.
- Acceptance:
  - [ ] `npm test` runs at least one real component test
  - [ ] `make e2e` passes locally against the dev stack, and the CI job passes
- Tests: the new ones.

### 14.4 Phase 1: Events and people

**M1.1a Events, time zones, occurrences, series edits**
- Depends on: M0.2
- Scope:
  - Rename Meeting to Event and RSVP to Registration (no data conversion, DEC-22).
  - Time zones (C2): `timezone` setting, `starts_local` + `tzid`, expansion with `DTSTART;TZID`.
  - Occurrences: `EventOccurrence` with an 18-month horizon, extended by the daily ticker (5.5); cancelled dates as occurrence status.
  - Series edits: the v1 rule in 5.3 (whole future series, plus per-date overrides).
- Acceptance:
  - [ ] A weekly 7:00 pm America/Boise event stays at 7:00 pm local on both sides of 2026-11-01 and 2027-03-14
  - [ ] The API returns both UTC and local times
  - [ ] A series edit moves future occurrences, keeps their registrations, cancels dates that no longer exist (registrants told), and leaves per-date overrides alone
- Tests: DST table tests (America/Boise, America/New_York, America/Phoenix, Europe/London); occurrence generation; series edit.

**M1.1b Church-wide hosts, event visibility, audit, in-app notices**
- Depends on: M1.1a
- Scope:
  - Church-wide events (`group_id` null), created by admins, with required `EventHost` rows (DEC-7).
  - Event visibility with `visibility_source` and the DEC-6, DEC-17, and DEC-28 rules, written to the `AuditLog` table from M0.2.
  - Notices: this milestone ships before email (M1.2), so its notices are **in-app**. They are derived notifications, computed from the `AuditLog` rows the way today's notifications are derived, and shown to the group's other leaders and the admins. From M1.2 on, the same change also sends them a transactional email.
  - Attendee privacy for opened events (final review, finding 3): when an invite-only group's event is `private` or `public`, only its leaders and the admins see attendee names. Everyone else sees a count. The visibility dialog warns: "Only leaders will see who signed up, so members of <group> aren't revealed." The event page names no leaders. It shows "Hosted by <church name>", plus a "Message the organizers" button once M3.11 exists.
- Acceptance:
  - [ ] A leader of the event's group, or an admin, can set a group event to `public`, `private`, or `invite_only`. A change more open than the group writes an `event.visibility_override` audit row (reason optional, DEC-28) and shows an in-app notice to the other leaders and the admins
  - [ ] An invite-only group's `private` or `public` event shows "Hosted by <church>" and never names or links the group or its leaders to outsiders; the group still returns 404 to them
  - [ ] On such an event, a member who isn't in the group sees only an attendee count, never names (API and UI tests)
  - [ ] With `visibility_source=group`, changing the group's visibility changes the event's; with `custom`, it does not
  - [ ] A church-wide event cannot be saved without at least one host, and cannot be `invite_only`; a non-admin host can switch it between `public` and `private`
  - [ ] A non-admin host can edit, message, and see registrants of the events they host, and nothing else (policy tests)
- Tests: policy tables for visibility, hosts, and attendee lists; notice tests.

**M1.2 Email basics, password reset, invitations**
- Depends on: M1.1b, M0.3
- Scope: `internal/mail` minimal sender (`devoutbox` in dev and tests, SMTP from `FOLD_SMTP_URL`) and plain transactional templates (C6). Password reset with a uniform response. Verified email change (10.1). Admin and leader `Invitation`s (no approval), with the per-leader daily limit. Email copies of M1.1b's visibility notices. `/dev/outbox` page in dev only.
- Acceptance:
  - [ ] `/dev/outbox` is unreachable unless `APP_ENV=dev`
  - [ ] SMTP sends through a test SMTP server in CI
  - [ ] An admin's or leader's invitation creates an active account on acceptance (the person sets a password), with no approval step
  - [ ] A leader's invitation can add the person only to a group that leader leads (403 otherwise)
  - [ ] Reset and invite tokens are single use and expire
  - [ ] Password reset answers "If that email has an account, we've sent a link" for every address, known or not
  - [ ] Changing your email sends a verification link to the new address and a notice to the old one; the change applies only after verification and ends every other session
  - [ ] A leader's 21st invitation in a day gets 429 (default limit 20) and is audited
- Tests: API tests reading links from the devoutbox; policy table for who may invite.

**M1.3 Self-signup, account requests, age gate**
- Depends on: M1.2
- Scope: the account doors in 5.10 (DEC-4): self-signup `AccountRequest`, member "invite a friend", the admin approval queue for accounts (kept, DEC-30), uniform responses with no account enumeration, `self_signup` setting (`off|request`, default `request`), the age gate (under 13 refused; teens routed to `pending_consent`, finished in M1.10), rate limits and honeypot on public forms.
- Acceptance:
  - [ ] Self-signup creates a pending `AccountRequest`; nobody can sign in until an admin approves; the person is emailed either way
  - [ ] A member's "invite a friend" creates a pending request; the friend gets no email until an admin approves
  - [ ] A request for an email that already has an account gets the same on-screen response as any other; the address is emailed a sign-in and reset hint instead (no account enumeration)
  - [ ] Choosing "under 13" on any door stores nothing and shows the household explanation
  - [ ] Requests expire after 30 days
  - [ ] Public forms are rate limited (10 per IP per hour) and have a honeypot
- Tests: API tests for each door.

**M1.4 Guest registration**
- Depends on: M1.3
- Scope: guests on public events (name + email, verified by an emailed link; "I am 18 or older"); manage link; guest upgrade request (C5); verification emails limited per target address as well as per IP (10.1).
- Acceptance:
  - [ ] A guest registers without an account, gets a confirmation email with a manage link, and can cancel from it
  - [ ] Without "I am 18 or older", nothing is stored and the guardian message shows
  - [ ] A guest who later gets an account keeps their registrations (same email, `citext`)
  - [ ] Verification emails to one address stop after 3 in an hour or 10 in a day (defaults), whatever the IP
  - [ ] A guest form for an email that already has an account gets the usual response, and the email asks the person to sign in
- Tests: API tests with the devoutbox.

**M1.5 Capacity, waitlist, pruning**
- Depends on: M1.2
- Scope: 5.4 in full, including remote capacity, guests, and removal by leaders and hosts (DEC-20).
- Acceptance:
  - [ ] 50 concurrent "going" requests on a 10-seat event give exactly 10 going and 40 waitlisted (Postgres test)
  - [ ] Cancelling promotes the earliest entry that fits, skipping a party too big for the free seats
  - [ ] No auto-promotion inside the cutoff; the leader is notified instead
  - [ ] Promoted people get an email with a one-click "I can't come" link
  - [ ] There is no approval step: a sign-up is `going` (or `waitlisted`) at once; a leader or host can remove anyone, and the removed person is told (DEC-30)
- Tests: unit tests on the promotion function plus the concurrency test.

**M1.6 Calendar views**
- Depends on: M1.1b, M0.4
- Scope: agenda timeline, week and month views, filters (8.1).
- Acceptance:
  - [ ] A member never sees invite-only events of groups they are not in (API and UI)
  - [ ] Times show in the church time zone, with a label when the browser differs
  - [ ] Works on a phone-width screen
- Tests: API tests; a Playwright smoke test for each view.

**M1.7 ICS feeds**
- Depends on: M1.1b
- Scope: 8.2 feeds and tokens.
- Acceptance:
  - [ ] Feeds validate (parsed by golang-ical in tests; golden files)
  - [ ] Recurring events appear as one VEVENT with RRULE, EXDATE, and RECURRENCE-ID
  - [ ] A revoked or regenerated token returns 404
  - [ ] Leaving an invite-only group removes its events from the personal feed on the next fetch
  - [ ] Video links follow `video_reveal`
- Tests: golden files; permission tests; ETag test.

**M1.8 Video, remote, childcare**
- Depends on: M1.1a
- Scope: 5.8 video links (link and Jitsi), remote flag and remote capacity UI, childcare fields and counts on registration (F11–F13).
- Acceptance:
  - [ ] "Generate Jitsi link" makes a new random URL on the configured domain
  - [ ] `registered_only` hides the link from people not going, in the API, emails, and feeds
  - [ ] Events can be filtered by "remote available" and "childcare available"
  - [ ] Childcare counts and notes are visible only to admins and the event's leaders or hosts
  - [ ] There is no video embed or iframe anywhere; the link opens in a new tab (DEC-16)
- Tests: policy and API tests.

**M1.9 Households and household people**
- Depends on: M1.5
- Scope: `Household`, `HouseholdAccount`, `HouseholdPerson` (child, teen, adult), `GuardianLink` for household children, registering household people for events, turning a household person into their own account, the shared-email rules in 5.10, admin confirmation of every link to a child or teen (DEC-32), and `docs/MEMBERS.md` (DEC-8, DEC-23).
- Acceptance:
  - [ ] An account holder can add a spouse who shares their email as an adult household person and RSVP for both; both count toward capacity, and the list shows "Mary Smith (via John Smith)"
  - [ ] Messages about a household person's registration go only to the account holder
  - [ ] Turning a household person into their own account (with a new, unique email) moves their registrations
  - [ ] Household children's care notes are visible only to guardians, admins, and the event's leaders or hosts during the event window
  - [ ] Adding a child or teen household person creates a pending `GuardianLink` that only an admin can confirm; until then the person can't be registered and leaders see nothing about them; adding an adult household person needs no approval (DEC-32)
  - [ ] `docs/MEMBERS.md` exists with the member guidance from 5.10
- Tests: policy table for the household rows in section 6; API tests.

**M1.10 Teen accounts, consent, badges**
- Depends on: M1.9, M1.3
- Scope: teen accounts, `TeenConsent`, guardian links to teen accounts, consent emails with a signed link, the daily age rollover, Teen and Child badges (DEC-8, DEC-18, DEC-20).
- Acceptance:
  - [ ] No path creates a `User` younger than 13 (API tests on every door)
  - [ ] A teen account cannot do anything but see the "waiting for consent" page until `account` consent is granted
  - [ ] A confirmed guardian can grant and revoke consent; an admin can grant only with evidence text; a leader who is not the teen's guardian gets 403
  - [ ] Every guardian link to a teen is pending until an admin confirms it: a teen account made from the guardian's own household person, a teen self-signup naming a guardian, and a claim on an existing teen. There is no auto-confirm path, and no consent from that adult counts until then (DEC-32)
  - [ ] A teen with consent can see and register for any event a member could (no youth-only rule), and cannot be made a leader, host, or admin
  - [ ] Teen and Child badges show on attendee lists, rosters, and recipient lists (API field plus UI)
  - [ ] A teen turns into an adult on their 18th birthday (fake-clock test), and the consent history is kept
  - [ ] Every consent change and link confirmation writes an `AuditLog` row
- Tests: policy table for every row in section 6 on teens; API tests; fake-clock tests.

**M1.11 Contact privacy and direct-contact requests**
- Depends on: M1.10, M1.2
- Scope: 5.11 (DEC-31): leaders and hosts never get teen contact details; the adult setting "Hide my contact info from group leaders" (off by default); `ContactRequest` and `ContactGrant`; approval rules (a hidden adult decides alone; for a teen, an admin and the teen must both approve); revocation by the person, an admin, or a teen's confirmed guardian; automatic end of grants; audit rows for every attempt; the admin contact-access history and CSV export.
- Acceptance:
  - [ ] No API response to a leader or host contains a teen's email or phone, or those of an adult with the setting on, unless an active grant covers it (a test checks the JSON keys on rosters, attendee lists, profiles, and recipient lists)
  - [ ] With the setting off (the default), an adult's contact details show to their leaders as before
  - [ ] A hidden adult alone approves or declines a request about them; a teen request is granted only after both an admin and the teen approve, and either one declining ends it
  - [ ] The person, an admin, or (for a teen) a confirmed guardian can revoke a grant at once
  - [ ] Grants end automatically when the leader stops leading the group, when a host is removed from the event or 30 days after its last occurrence, when a teen turns 18 (fake-clock test), or when either account is disabled
  - [ ] Requests expire after 14 days; a leader can have at most 10 open requests (defaults)
  - [ ] Every request, decision by each party, withdrawal, expiry, grant, reveal, revocation, automatic end, and refused attempt writes an `AuditLog` row (a test walks every path and checks its row)
  - [ ] Admins can see and export (CSV) the contact-access history for any person
- Tests: policy tables for the 5.11 rows of section 6; API tests on every path; fake-clock tests for expiry and age.

### 14.5 Phase 2: Rooms

**M2.1 Rooms and bookings**
- Depends on: M1.1a
- Scope: 9.1–9.2. `rooms_enabled` setting, room CRUD (admins), bookings per occurrence and per series, setup and teardown padding stored in `padded_range`, conflicts, admin double-book with a reason, bookings that move with their occurrence.
- Acceptance:
  - [ ] With rooms off, no room UI or endpoints are reachable
  - [ ] Overlapping bookings (including padding) are refused for leaders and hosts, by the exclusion constraint as well as by the API (two concurrent requests: exactly one wins)
  - [ ] `padded_range` always matches `starts_at`, `ends_at`, and the padding (the `CHECK` rejects a mismatch)
  - [ ] Moving an occurrence moves its bookings; a move into a conflict turns the booking `requested` and tells the leader, and the edit still saves
  - [ ] Restricted rooms can be booked only by admins
- Tests: conflict table tests, including the padding edges and moved occurrences.

**M2.2 By-room view and room feed**
- Depends on: M2.1, M1.6, M1.7
- Acceptance:
  - [ ] Bookings the viewer cannot see show as "Reserved" with no title or group
  - [ ] Room ICS feed works with an admin token and masks invite-only titles
- Tests: API masking tests; a Playwright smoke test.

**M2.3 Room suggestions**
- Depends on: M2.1
- Scope: 9.3 exactly.
- Acceptance:
  - [ ] All 13 cases in the 9.3 table pass
  - [ ] The response never contains other events' titles, groups, or dates (a test checks the JSON keys)
  - [ ] Suggestions return in under 100 ms with 50 rooms and 20,000 bookings (benchmark on Postgres)
- Tests: the 9.3 table, plus a benchmark.

### 14.6 Phase 3: Messaging

**M3.1 Jobs table and worker**
- Depends on: M0.1
- Scope: 5.5. Worker, polling every 1 to 2 seconds, retries, `unique_key`, and an admin "System" page listing dead jobs with a retry button. Move the daily ticker's tasks onto jobs.
- Acceptance:
  - [ ] Two app processes never run the same job twice (`FOR UPDATE SKIP LOCKED`; a test with 2 workers and 200 jobs)
  - [ ] A newly queued job starts within 2 seconds
  - [ ] A failing job retries with backoff and ends `dead` after `max_attempts`
- Tests: unit tests for backoff and dedupe; Postgres concurrency test.

**M3.2 Provider registry, Postmark adapter, email webhooks**
- Depends on: M3.1, M1.2
- Scope: `internal/providers` interfaces (5.6) and registry; move `devoutbox` and SMTP behind it; Postmark adapter (DEC-21); the `/hooks/postmark/email` handler in the CSRF-exempt group, verified by Postmark's webhook authentication.
- Acceptance:
  - [ ] Swapping providers is a config change only (env for now; the wizard in M4.4)
  - [ ] Postmark, SMTP, and devoutbox pass the shared contract suite against `httptest` fakes built from vendor docs
  - [ ] A webhook with a bad signature returns 401 and changes nothing
  - [ ] Bounces and complaints update `MessageRecipient` and `Suppression`
  - [ ] Transactional and broadcast mail use separate Postmark message streams
- Tests: contract and webhook tests with recorded sample payloads.

**M3.3 Consent and unsubscribe**
- Depends on: M3.2
- Scope: 7.4 email side: `Consent`, `Suppression`, preferences page with signed tokens, RFC 8058 headers, the CSRF-exempt one-click `POST`.
- Acceptance:
  - [ ] A one-click POST (no cookie, no CSRF token) to the unsubscribe URL opts out; a tampered token returns 404 and changes nothing
  - [ ] Transactional mail still sends after opting out of announcements
  - [ ] Every consent change is an appended row with source and time
- Tests: header tests; token tamper tests.

**M3.4 Composer**
- Depends on: M3.3, M0.4
- Scope: 7.3. TipTap editor, bluemonday sanitizing, SMS counter, audience picker with live counts and teen counts, preview, test send, scheduling, merge fields.
- Acceptance:
  - [ ] 160 GSM-7 characters = 1 segment; 161 = 2 segments; 70 UCS-2 code units = 1 segment and 71 = 2 segments (one emoji counts as 2 units) (counts include the auto prefix and footer)
  - [ ] `<script>`, `on*` attributes, and `javascript:` links are stripped
  - [ ] Leaders see only own groups and own events in the picker, and hosts see only events they host; the API refuses other targets
  - [ ] An SMS mentioning giving or donations (the 7.3 word list or the church's giving URL) shows the donation-ask warning (DEC-3)
  - [ ] The recipient preview shows Teen badges and a teen count (DEC-20)
- Tests: segment table tests; sanitizer tests; policy tests; Playwright compose-and-schedule test.

**M3.5 Send pipeline**
- Depends on: M3.4, M1.5, M1.10
- Scope: 7.1, 7.2, and the email side of 7.5: audience snapshot, dedupe, filters, guardian copies, batches, rate limits, scheduled sends, history page.
- Acceptance:
  - [ ] A person in two chosen groups gets one copy
  - [ ] Opted-out and suppressed people are skipped and counted with reasons
  - [ ] A leader demoted between scheduling and sending is refused at send time
  - [ ] Teens without `account` consent and all household people get nothing; messages about a household person's registration go to the account holder (DEC-8)
  - [ ] With `guardian_copies` off (default), guardians get no copies; with it on, each confirmed guardian gets one marked copy per message to their teen (DEC-19); no copy is ever sent by SMS (DEC-29)
  - [ ] A scheduled message can be cancelled until sending starts
  - [ ] A provider outage retries and then shows a failed state with the error
- Tests: pipeline tests with devoutbox; failure injection.

**M3.6 Email reminders**
- Depends on: M3.5
- Scope: 7.7, email part (SMS reminders arrive with M3.8).
- Acceptance:
  - [ ] Each going registrant gets exactly one 24-hour email reminder per occurrence, even after restarts (`unique_key`)
  - [ ] Cancelled occurrences cancel their pending reminders
  - [ ] Per-event settings can turn reminders off or change times
- Tests: scheduler tests with a fake clock.

**M3.7 Telnyx SMS, inbound, keywords, revocation**
- Depends on: M3.5, M3.11
- Scope: Telnyx adapter (send, inbound and status webhooks in the CSRF-exempt group, signature check), SMS opt-in flow with the phone ownership check (code or YES reply) and confirmation text, the keyword list and automatic phrase opt-outs from 7.4, caps on HELP and automatic replies, the read-only opt-out log, and the SMS relay of replies (7.8).
- Acceptance:
  - [ ] Each of `STOP`, `STOPALL`, `QUIT`, `END`, `REVOKE`, `OPT OUT`, `OPTOUT`, `CANCEL`, `UNSUBSCRIBE` (any case, surrounding punctuation ignored) opts out of all SMS and is recorded; `START` opts back in; `HELP` replies with the help text
  - [ ] "Please stop texting me" opts the person out at once and gets the confirmation text, with no admin step; it appears in the read-only opt-out log; texting `START` opts back in (DEC-27)
  - [ ] No SMS goes to anyone without an opted-in consent row
  - [ ] Ticking SMS opt-in sends a code or a "Reply YES" text; `Consent` becomes `opted_in` and `phone_verified_at` is set only after the code is entered or YES comes back from that number; at most 3 verification texts per number per day
  - [ ] HELP gets at most 1 reply per number per 10 minutes, each number gets at most 5 automatic replies a day, and a repeated STOP from an opted-out number gets no reply
  - [ ] Teens get SMS only with both `TeenConsent` scope `sms` and their own opt-in; household people never
  - [ ] Other replies reach the author of the person's latest message (last 7 days) in-app and by email with a relay Reply-To, never showing the number; the author's answer goes back by SMS from the church number; with no recent message, the reply goes to the admins
- Tests: contract suite; keyword table tests; webhook signature tests.

**M3.8 SMS send window, caps, SMS reminders**
- Depends on: M3.7, M3.6
- Scope: the recipient-local send window (DEC-9, DEC-25), per-author and per-recipient limits, daily caps per registration path (DEC-3), SMS reminders (7.7).
- Acceptance:
  - [ ] With the default window (8 AM to 8 PM), a 7:30 PM church-time send reaches a recipient whose profile is in the church zone at once, and a recipient whose profile zone is 2 hours ahead the next morning at 8 AM their time
  - [ ] Recipients with no profile zone, and guests, use the church time zone
  - [ ] Saving a window that starts before 8 AM or ends after 9 PM shows the TCPA warning, needs a tick, and is audited
  - [ ] An admin's one-message override needs a reason and is audited; leaders cannot override
  - [ ] Sends beyond the path's daily cap roll over to the next day, and the author is told
  - [ ] An SMS reminder that falls outside the recipient's window moves to the last allowed time before it (at most 18 hours earlier), or is skipped while the email reminder still goes
- Tests: send-window tests across DST and across time zones; cap tests.

**M3.9 Member SMS requests**
- Depends on: M3.8
- Scope: 7.6.
- Acceptance:
  - [ ] Any leader of the group or any admin can approve; the first decision wins (a concurrency test with 2 approvers)
  - [ ] Requests expire as described, and the requester is told
  - [ ] An approved request sends under all normal rules
- Tests: API and concurrency tests.

**M3.10 Twilio adapter**
- Depends on: M3.8
- Scope: a second SMS adapter behind the same interface, including Twilio's sole-proprietor and toll-free paths in the registration flow.
- Acceptance:
  - [ ] Passes the SMS contract suite and the keyword tests
  - [ ] Switching from Telnyx to Twilio keeps every opt-out (Fold's own suppression list)
- Tests: contract suite against a Twilio fake.

**M3.11 One-to-one messages and reply relay**
- Depends on: M3.5, M1.11
- Scope: 7.8 (DEC-31): `Conversation` and `ConversationMessage`; in-app one-to-one messages from leaders and hosts; "Message the organizers" on event pages; relay Reply-To addresses on every email Fold sends for an author; the Postmark inbound webhook at `/hooks/postmark/inbound` (CSRF-exempt, verified); the reply MX record documented (the wizard adds it in M4.4).
- Acceptance:
  - [ ] No email Fold sends carries a member's or leader's personal address in From, Reply-To, To, or Cc; Reply-To is always a relay address, or the church office when there is no inbound mail
  - [ ] A reply to a relay address reaches the other person in-app and by email, again with a relay Reply-To, and writes audit metadata (who, to whom, context, channel; not the body)
  - [ ] Mail to a relay address from anyone other than that conversation's participants is dropped and logged; a relay address stops working when its conversation closes
  - [ ] When a leader stops leading the group, their conversations close, and later replies go to the group's other leaders or the admins
  - [ ] Leader-to-teen one-to-one messages get guardian copies when `guardian_copies` is on, and show in admin message history
  - [ ] An inbound webhook that fails verification returns 401 and changes nothing
- Tests: relay tests with recorded Postmark inbound payloads; policy tests; header tests.

### 14.7 Phase 4: Setup and providers

**M4.1 Encrypted provider settings**
- Depends on: M3.2
- Scope: `FOLD_SECRET_KEY`, AES-GCM helpers, `ProviderConfig` table, env override, admin API that never returns secrets (C8). Accept hex or base64 keys.
- Acceptance:
  - [ ] Secrets are unreadable in a database dump (a test greps the dump)
  - [ ] A wrong key gives a clear startup error, not garbage
  - [ ] An env variable overrides the database value, and the UI shows "set by environment"
  - [ ] `fold secrets rotate --new-key` re-encrypts all rows
- Tests: round-trip, tamper detection, rotation.

**M4.2 SPA setup wizard and setup state**
- Depends on: M4.1, M1.3
- Scope: `SetupState`; the first-admin gate reads it from the database on each request; the SPA wizard at `/admin/setup` for steps 1, 2, 5–8 and 10 of 11.3; the setup checklist card.
- Acceptance:
  - [ ] Wizard state survives a browser close and an app restart, and resumes at the right step
  - [ ] With two app processes, only one first admin can be created (the gate is not a cached flag)
  - [ ] Step 1 sets the account policy (`request` by default, or `off`), and the first admin confirms they are 18 or older
  - [ ] The safeguarding step sets `guardian_copies` and is audited
- Tests: API tests for each step; Playwright run-through.

**M4.3 Legal pages**
- Depends on: M4.2
- Scope: generated `/legal/privacy` and `/legal/sms-terms` with admin review (10.4). The privacy page states whether guardian copies are on.
- Acceptance:
  - [ ] Pages show church name, address, contact, message types, STOP/HELP, and "Msg & data rates may apply", and stay hidden until an admin accepts them
- Tests: golden-file tests.

**M4.4 Email provisioning**
- Depends on: M4.2, M3.2
- Scope: 11.3 step 3 with `EmailProvisioner` (Postmark; SMTP has no provisioning) and the Cloudflare and manual DNS providers.
- Acceptance:
  - [ ] With a Cloudflare token, DNS records are created and verification is polled to green with no manual steps (against fakes)
  - [ ] Without a token, the records table can be copied and is checked by DNS lookup
  - [ ] The webhook is created with a secret, and a test email arrives in the fake
- Tests: fakes for Postmark and Cloudflare.

**M4.5 People CSV import**
- Depends on: M4.2, M1.10
- Scope: 11.3 step 9.
- Acceptance:
  - [ ] Rows under 13 are refused; teens start `pending_consent`; imported phones are not opted in
  - [ ] Two rows with one email become one account plus an offer to add the others as adult household people (DEC-23)
  - [ ] Imported adults get a set-password email, not a password in the file
- Tests: import tests with sample CSVs.

**M4.6 SMS provisioning**
- Depends on: M4.3, M3.7
- Scope: 11.3 step 4.
- Acceptance:
  - [ ] Nothing is bought unless the admin typed `BUY` in that request (a test asserts the fake got no purchase call otherwise)
  - [ ] Telnyx is preselected; skipping SMS leaves the rest of Fold working
  - [ ] The wizard asks about the EIN and sets `sms_registration_path`; it never asks for 501(c)(3) proof and never offers the Charity use case (DEC-3)
  - [ ] With no EIN on Telnyx, it explains an EIN is required and links the free IRS EIN page; it does not offer sole proprietor
  - [ ] Registration forms are pre-filled from settings and legal pages, and sample messages contain no donation asks
  - [ ] SMS sending is blocked until registration is approved, with a banner explaining why
- Tests: fakes for Telnyx.

**M4.7 S3 storage and private assets**
- Depends on: M4.1
- Scope: 5.7 and C9: `Asset` table, private `/files/<key>` handler, S3Store, `fold storage migrate`.
- Acceptance:
  - [ ] Private files cannot be fetched without permission (local and S3 signed URLs)
  - [ ] Moving local → S3 → local keeps every file and can resume after interruption
  - [ ] CI runs the S3 tests against a MinIO service container
- Tests: store contract suite run on both stores.

**M4.8 `fold setup` CLI**
- Depends on: M4.4, M4.5, M4.6, M4.7
- Scope: non-interactive setup from `setup.yaml`, `--dry-run`, `--json` (11.4).
- Acceptance:
  - [ ] `--dry-run` makes no external calls and prints every planned action
  - [ ] Running the same config twice changes nothing the second time
  - [ ] Purchases still need `confirm_purchase: BUY` in the YAML plus an interactive confirmation, or the `--yes-buy` flag
- Tests: CLI golden-output tests.

**M4.9 Admin two-factor sign-in (TOTP)**
- Depends on: M4.1, M1.2
- Scope: TOTP (RFC 6238) for every admin account, required from v0.5 (final review, finding 7). Ten one-time recovery codes, stored hashed. The TOTP secret is encrypted like provider secrets (C8). An admin without TOTP can reach only the enrollment page. Another admin can reset a lost device; a sole admin uses a recovery code or `fold user reset-2fa` on the server.
- Acceptance:
  - [ ] A newly made admin must enroll before reaching any admin page; promoting someone to admin ends their other sessions
  - [ ] Sign-in needs a valid code (with a small clock-skew window), and each recovery code works once
  - [ ] Code attempts are throttled like passwords (M0.3)
  - [ ] Enrolling, resetting, and using a recovery code write `AuditLog` rows and end other sessions
- Tests: TOTP tests with a fake clock; API tests; a Playwright enrollment test.

### 14.8 Phase 5: Release engineering

**M5.1 Prebuilt images**
- Depends on: M0.1
- Scope: GitHub Actions builds `linux/amd64` and `linux/arm64` images of `fold` and `fold-backup` and pushes them to GHCR on tags (C10). The deploy compose uses `image:`; a `docker-compose.build.yml` override keeps local builds for developers.
- Acceptance:
  - [ ] `docker compose pull && docker compose up -d` works on a fresh VPS without Go or Node
  - [ ] Images are signed or have checksums published in the release notes
- Tests: CI smoke test starts the stack and hits `/healthz`.

**M5.2 Backups**
- Depends on: M5.1
- Scope: 11.8 for Compose: a small `ghcr.io/grierj/fold-backup` image (postgres:16-alpine plus `age` and `rclone`) with `backup.sh` (nightly loop and `now`) and `restore.sh`, `LAST_OK`, retention, `age`-encrypted offsite upload to any S3-compatible bucket.
- Acceptance:
  - [ ] Nightly dump and uploads tar appear with the retention rule applied
  - [ ] `docker compose exec backup backup.sh now` makes a backup on demand
  - [ ] `restore.sh` brings back a working site (CI restore test)
  - [ ] Offsite copies are `age`-encrypted, and the server holds only the public key
  - [ ] The System page warns when the last backup is older than 36 hours
- Tests: CI restore test; retention unit test.

**M5.3 Upgrades**
- Depends on: M5.2
- Scope: 11.7. Backup check before migrating (skipped on first install or with no pending migrations; no off switch), exit non-zero on failure, `docs/UPGRADING.md`.
- Acceptance:
  - [ ] Upgrading from the previous release with seeded data passes in CI (from v0.5 on)
  - [ ] A first install with an empty `backups` volume starts without a backup
  - [ ] With pending migrations and no backup newer than 24 hours, the app exits with a clear message
  - [ ] A failing migration leaves the data unchanged, and the app exits non-zero and shows unhealthy
- Tests: upgrade test in CI.

**M5.4 Deployment docs (Compose)**
- Depends on: M5.1–M5.3
- Scope: `docs/DEPLOY.md` for humans, and the AGENTS.md Deployment section (Appendix A).
- Acceptance:
  - [ ] A person following DEPLOY.md on a fresh 2 GB VPS reaches the wizard in under 30 minutes (manual check, recorded in the PR)
  - [ ] An agent following AGENTS.md completes the same run with `fold setup --dry-run` first
- Tests: docs link check.

**M5.5 Render Blueprint (optional, future; not committed)**
- Status: not in any release. Start only when a church asks, or when Grier wants an easy path to host for others (DEC-26).
- Depends on: M5.4, M4.1
- Scope: `deploy/render.yaml` (the Render reference in Appendix A), `docs/DEPLOY-RENDER.md`, and the app details Render needs: `PORT`, the Render value for `FOLD_TRUSTED_PROXIES`, a way to skip the Compose backup check where Render's PITR covers upgrades, base64 secrets.
- Acceptance:
  - [ ] `render.yaml` passes `render blueprints validate` (or validation through Render's API) in CI or in the PR notes
  - [ ] A person following DEPLOY-RENDER.md, in a Render workspace the church owns, reaches the wizard with a custom domain and TLS (manual check, recorded in the PR)
  - [ ] Client IPs are real (the Admin > System IP check) behind Render's proxy
  - [ ] Docs list the monthly cost from 12.5, the disk trade-off, and the S3 option
- Tests: blueprint validation; docs link check.

### 14.9 Phase 6: Community features (after v0.8)

Each item is one milestone. Each still needs acceptance criteria written into this file before work starts.

- M6.1 Directory with per-field privacy (10.2)
- M6.2 Sign-up sheets: volunteer, potluck, supplies (one model: sheets, slots, claims)
- M6.3 Attendance roll for leaders
- M6.4 QR / self check-in
- M6.5 Prayer requests
- M6.6 Group discussions
- M6.7 (moved to M1.9 and M1.10: households and teen accounts are core, DEC-8)
- M6.8 Background-check status flags on slots
- M6.9 Photo galleries (private by default)
- M6.10 (moved to M4.9: admin TOTP is required from v0.5)
- M6.11 ChurchTrac CSV import (blocked until someone provides a real sample ChurchTrac export, DEC-15)
- M6.12 Custom registration questions
- M6.13 (dropped: no translations or i18n framework, DEC-13)
- M6.14 Durable, tamper-evident audit log for the most important events (10.7, DEC-33): append-only storage, hash chaining, a write-once offsite export (for example B2 Object Lock), retention rules, and an admin export for legal requests. Later, not v1 scope; the regular `AuditLog` covers these events until then

Built only when a church asks: Resend and Mailgun email adapters, a Plivo SMS adapter, a Zoom video adapter, and a moderator role (DEC-17).

---

## 15. Decisions (Oct 9, 2026) and remaining open questions

### 15.1 Decisions

Grier answered the first-draft questions on **Oct 9, 2026** (DEC-1 to DEC-16, numbered after the original questions), then made more decisions the same day: DEC-17 to DEC-25 (Grier's items A to I), DEC-26 to DEC-30, and DEC-31 to DEC-33 (after Codex's final review), and DEC-34 to DEC-37 (Grier's answers to the M0.1 and M0.2 PR questions). Each has been carried through the sections listed. These decisions win over the original requirement wording and over the repo.

| ID | Decision (2026-10-09) | Where it flows |
|---|---|---|
| DEC-1 | **PostgreSQL only.** No SQLite in production, development, or tests. Fold builds on Postgres features (SKIP LOCKED, exclusion constraints, citext, jsonb, advisory locks). Confirmed by DEC-24. | 1, 3.1–3.4, C1, C7, C14, 5.3, 5.5, 9.2, 11.9, 14.1, M0.1, M2.1, M3.1 |
| DEC-2 | **Churches bring their own provider and hosting accounts**, even when hosted, unless there is a special agreement. Fold is open-source give-away software. Grier may help a church set its accounts up. | 1, 2 (D2), 11.3, 11.5, 11.6, 12 intro, 12.5, Appendix A |
| DEC-3 | **Assume churches are not 501(c)(3)s.** SMS registration plans for an EIN-based standard brand or toll-free verification, never asks for 501(c)(3) proof, and avoids donation asks in SMS. With no EIN: get a free IRS EIN (required on Telnyx); Twilio's sole-proprietor path is the only fallback. | 5.3, 7.3, 10.4, 11.3, 12.2, M3.4, M3.8, M4.6, Appendix A |
| DEC-4 | **Self-signup creates an account request that an admin approves.** Leader and admin invitations skip approval. A member's invitation still creates a request that an admin must approve. There is no "open" signup mode. | C15, 5.3, 5.10, 6, 11.3, M1.2, M1.3, M4.2, 10.1 |
| DEC-5 | **Admins see everything**, invite-only groups included. Fixed upstream in PR #7 (merged 2026-10-09, master `e7c0e8f`). | 3.4, 6, M0.2 |
| DEC-6 | **Leaders and admins can make an event public**, even when its group is private or invite-only; the invite-only group itself stays hidden. Broadened by DEC-17. | 5.3, 6, 9.3 (cases 11 to 13), M1.1b |
| DEC-7 | **Church-wide events have specific assigned hosts** (`EventHost`). They are often admins, but don't have to be. Hosts get leader rights over those events only. Admins create church-wide events and assign the hosts. | 2 (F8), 5.3, 6, 7.2, 8.2, M1.1b, M3.4 |
| DEC-8 | **No accounts under 13.** Younger children exist only as household people with no login, attached to a parent or guardian. Teens 13 to 17 can have accounts with consent from guardians and admins; the leader role never grants consent. | 1, 5.3, 5.10, 6, 7.1, 7.2, 7.4, 8.2, 10.3, 13.2, M1.3, M1.9, M1.10, M3.5, M3.7, M4.5 |
| DEC-9 | **SMS send window: 8 AM to 8 PM recipient-local time by default**, configurable. Warn the admin if the window goes outside the federal TCPA limit of 8 AM to 9 PM. Confirmed by DEC-25. | 5.3, 6, 7.1, 7.5, 7.7, 11.3, M3.8 |
| DEC-10 | **Hosting terms and fees are case by case**, and hosting is not meant to make money. | 1, 2 (D2), 11.6 |
| DEC-11 | **MIT license** stays. | 1, 11.6 |
| DEC-12 | **Rename to `fold`**: done upstream in PR #7 (merged 2026-10-09). | 3.1, 3.4, C13 |
| DEC-13 | **No non-English version.** Spanish and i18n are out of scope; plain English strings are fine; don't build an i18n framework. | 1, 13.2, M6.13 (dropped) |
| DEC-14 | **Lawyer review is case by case.** The hope is that Grier won't host for others. Legal pages are templates the church owns. | 7.5, 10.3, 10.4 |
| DEC-15 | **ChurchTrac import stays Later** and needs a real sample export first. Nobody has export details yet. | 13.2, M6.11 |
| DEC-16 | **No embedded video for now.** Link-out or a generated Jitsi link only. Revisit embedding in a later revision if requested. | 1, 2 (F11), 5.8, 12.4, 13.2, M1.8 |
| DEC-17 (A) | **Leaders have broad powers over their own groups' events.** A leader may set a group event to any visibility, for example an invite-only group's event to `private` (members only) or `public`. More-open changes are audited and notified; the reason is optional. Hosts may switch church-wide events between public and private. A future **moderator** role between member and leader is a note only; don't build it. | 2 (F1), 5.3, 6, 9.3 (case 13), 13.2, 14.9, M1.1b |
| DEC-18 (B) | **Teens cannot co-lead groups or host events.** | 1, 5.3, 5.10, 6, 10.3, M0.2, M1.10 |
| DEC-19 (C) | **No guardian copies by default.** Guardians do not get copies of messages sent to their teen unless an admin turns on the church setting `guardian_copies`. Copies go by email only (DEC-29). | 5.3, 5.10, 6, 7.1, 7.4, 10.3, 11.3 (step 8), 13.2, M3.5, M4.2, M4.3 |
| DEC-20 (D) | **Teens may sign up for any event they can see.** No youth-only restriction (`teens_allowed` is gone). Leaders and hosts remove attendees themselves (no approval step, DEC-30). Attendee lists, rosters, and message recipient lists clearly badge teen (and child) accounts. | 5.3, 5.4, 5.10, 6, 7.1, 10.3, 13.2, M1.5, M1.10, M3.4 |
| DEC-21 (E) | **Email: Postmark is the default.** AWS email is removed as a recommendation (Grier avoids using AWS directly because of lock-in; see DEC-26); independent alternatives are Resend and Mailgun, with SMTP as the generic fallback. Nothing in Fold depends on AWS. B2 and R2 stay. | 1, 5.6, 7.4, 11.3, 12.1, 12.5, 12.7, M3.2, M4.4 |
| DEC-22 (F) | **Clean break.** No church runs Fold, so there are no legacy-data migrations or old-data fixture tests. Anything in the skeleton may change for a better product if it meets the agreed requirements. Language and frameworks are open if they are not Python and stay very simple to deploy; Go + Gin + GORM + React is kept by default (stack note in 3.3). | Header, 1, 3.3, 3.4, 4, 5.3, 14.1, M0.1, M0.2, M1.1a |
| DEC-23 (G) | **Log in by email** instead of username; the email is unique. A shared email is one account; that person brings the others as household people on RSVPs. Guidance for churches and members is documented. | 3.1, 3.4, C16, 5.3, 5.10, 6, 10.1, 11.3, M0.3, M1.9, M4.5 |
| DEC-24 (H) | **Postgres for dev and tests too**, via Docker Compose; SQLite dropped entirely. Confirms DEC-1. | 1, 3.1, 3.3, C14, 11.9, 14.1, M0.1 |
| DEC-25 (I) | **Quiet hours use the recipient's time zone**: default 8 AM to 8 PM, configurable, with a warning outside TCPA's 8 AM to 9 PM, falling back to the church time zone when the recipient hasn't set one. Confirms DEC-9. | 5.3, 5.9, 7.1, 7.5, 11.3, M3.8 |
| DEC-26 | **Hosting: VPS only for now.** Vendors that run on AWS underneath are fine; the objection is to using AWS services directly, because of vendor lock-in. A VPS with Docker Compose is the only supported deployment. Render stays as a reference evaluation and `render.yaml` outline; M5.5 is optional and future, added when a church asks or when Grier wants an easy path to host for others. | 1, 2 (D4), 5.1, 11, 12 intro, 12.5, 12.7, 14.2, M5.5, Appendix A |
| DEC-27 | **Opt-outs are automatic and immediate, always.** STOP-type keywords and any reply that reasonably reads as an opt-out (FCC rules or plain wording) opt the person out at once, and Fold sends the confirmation text. No review queue, no timer, no human work. Admins get a read-only opt-out log; a person opts back in with `START`. | 5.3, 5.5, 6, 7.4, 10.4, M3.7 |
| DEC-28 | **A reason for making an event more open stays optional.** The change is always audited, and the group's other leaders (and admins) are notified. | 5.3, 6, M1.1b |
| DEC-29 | **Guardian copies go by email only**, never by SMS, because texts cost money. | 5.3, 5.10, 7.1, 11.3, M3.5 |
| DEC-30 | **Trust the church body on workflow; never relax security.** Once someone has an approved account, they use their own judgment about what to join and sign up for, and leaders use theirs to remove people who shouldn't be there: no approval gates on sign-ups or group joins, and no per-event approval setting. Fold is on the public internet, so every security control stays and is hardened: account approval for self-signup and member invites, authentication and session security, admin 2FA, rate limits, login throttling, CSRF, trusted proxies, input validation, upload restrictions and private uploads, secrets encryption, audit logs, backups, TLS, security headers, dependency updates, and least-privilege permissions. Locked by rule: minors, visibility, who can message whom, and consent. | 5.3, 5.4, 5.10, 6, 10.3, 10.6, 11.3, 11.7, M1.5, M5.3 |
| DEC-31 | **Fold is the contact proxy.** Every leader-to-teen message goes through Fold's email, SMS, or in-app messaging, and leaders, co-leaders, and hosts never see a teen's email or phone by default. Adults get "Hide my contact info from group leaders", off by default; when on, leaders reach them only through Fold. A leader can request direct contact: a hidden adult alone approves or declines; for a teen, **both an admin and the teen must approve** (decided). Every attempt to get direct contact is logged, including requests, denials, and withdrawn or expired requests, along with every grant, reveal, revocation, and automatic end. The person, an admin, or a teen's confirmed guardian can revoke a grant; grants end when the leader stops leading the group (and when a teen turns 18). Replies come back through Fold: relay Reply-To addresses for email, and the church's number for SMS, tied to the group or event. Co-leader promotion gets no extra admin gate; it is audited with an admin notice, and the proxy covers contact details. | 2 (F1), 5.3, 5.5, 5.10, 5.11, 6, 7.1, 7.4, 7.8, 10.1, 10.2, 10.3, 10.5, 10.6, 13.2, 14.2, M0.2, M1.1b, M1.11, M3.7, M3.11 |
| DEC-32 | **Linking any teen or child to an adult, as guardian or household member, always needs admin approval.** That includes a guardian adding a household child or teen, turning a household teen into an account with an email, a teen naming a guardian at self-signup, and claims on existing teens. There is no auto-confirm path. Adult household people (for example a spouse) need no approval. | 5.3, 5.10, 6, 10.3, 10.6, M1.9, M1.10 |
| DEC-33 | **A durable, tamper-evident audit log for the most important events comes later** (not v1): contact access (every attempt), guardian and teen links, teen consent, role changes, visibility overrides, opt-outs, and admin overrides. Sketch: append-only storage, hash chaining, a write-once offsite export (for example B2 Object Lock), retention rules, and an admin export for legal requests. Until then, the regular `AuditLog` from M0.2 records these events and never purges them. | 5.3, 5.11, 10.6, 10.7, 13.2, 14.2, M6.14 |
| DEC-34 | **Reasonable people, logs just in case, remove bad actors.** Inside Fold, assume people act reasonably. Keep audit logs in case they don't, and remove bad actors outright instead of adding approval layers. Take reasonable, well-thought-out steps for privacy and legal compliance, but avoid extra approvals and friction. This confirms co-leader promotion as built in M0.2 (no admin gate; audited, with an admin notice). Teen contact details stay protected by the grant rule (DEC-31). | 5.11, 6, M0.2, M0.3b |
| DEC-35 | **Leaders can remove members from their own groups**, as soon as possible (M0.3b). Removals are audited, and the rule lives in `internal/policy`. Leaders can't remove admins or the group's last leader. Consistent edge cases: leaders can't demote an admin co-leader either; a removed person can't rejoin on their own until a leader or admin adds them back. | 5.3, 6, 10.6, M0.3b |
| DEC-36 | **Deleting the account of a group's only leader requires a successor.** For every group the account solely leads, the deleting admin picks a new leader: a member of that group or any admin, the deleting admin selected by default. The handoff is audited. The same applies to disabling an account. | 6, 10.1, 10.6, M0.3b |
| DEC-37 | **Security headers and dependency scanning move up to M0.3b:** CSP, HSTS, and the other headers set by the app; Dependabot; `govulncheck` and `npm audit` in CI. Caddy and image-level headers and pinned image digests stay in M5.1. | 10.1, 10.6, M0.3b, M5.1 |

The five questions left open in draft v2 are all answered: intermediate overrides (DEC-17), teens as leaders (DEC-18), guardian copies (DEC-19), which groups admit teens (DEC-20), and the email default (DEC-21). DEC-26 to DEC-30 answer the five questions draft v3 left open (Render's place, unreviewed opt-outs, a reason for opening events, guardian SMS copies, and sign-up approval). DEC-31 to DEC-33 answer the final review's co-leader and guardian findings and add contact-access logging. DEC-34 to DEC-37 answer the four questions from the M0.1 and M0.2 pull requests (#9, #10).

### 15.2 Still open

Nothing. Every question raised so far has a decision above.

---

## 16. Appendix A: AGENTS.md "Deployment" section outline

Add this to `AGENTS.md` in M5.4. Each item is a heading with short, exact instructions. The Render outline at the end is reference only and is not added to `AGENTS.md` unless M5.5 is started (DEC-26).

0. **Rules for agents**
   - Never buy, upgrade plans, or create accounts without the operator's typed confirmation in this session.
   - Provider and hosting accounts belong to the church (DEC-2). Ask the church to create them and give you an API key or a user seat. Never open them in your own or the helper's name.
   - Fold uses PostgreSQL only (DEC-1, DEC-24). Never configure SQLite. No Python anywhere.
   - Never echo secrets. Write `.env` with mode 600.
   - Always run `fold setup --dry-run` before `fold setup`. Run `docker compose exec backup backup.sh now` before upgrades.
   - Stop and ask when a step fails twice.
1. **Ask the operator first** (customization prompts)
   1. Church legal name and display name
   2. Domain or subdomain for Fold (for example `groups.example.church`), and who controls its DNS
   3. Church time zone and postal address
   4. First admin's name and email (the email is the login, DEC-23)
   5. Email provider (Postmark default; Resend, Mailgun, or SMTP) and whether they already have an account
   6. SMS: needed now? Which provider (Telnyx default; Twilio from v1.0)? Does the church have an EIN? (On Telnyx it is required; suggest the free IRS EIN.) 10DLC or toll-free? Never ask for 501(c)(3) proof (DEC-3).
   7. Account policy: self-signup with admin approval (default) or off (DEC-4); SMS send window (default 8 AM to 8 PM recipient-local, DEC-9, DEC-25); guardian copies on or off (default off, DEC-19)
   8. Storage: local or S3-compatible (and which), and offsite backup bucket plus an `age` public key
   9. Hosting: VPS provider and size, or an existing server, and whose account it is on

**Deployment: one VPS with Docker Compose (the only supported path, DEC-26)**

2. **Preflight**: check the VPS is reachable over SSH; at least 2 GB RAM and 20 GB free disk; ports 80 and 443 open; Docker and the compose plugin installed (`docker compose version`); the domain's A/AAAA records point at the server (`dig +short`).
3. **Server prep**: create the `/opt/fold` directory; enable unattended security updates; firewall allows 22, 80, 443 only.
4. **DNS**: create the app record (DNS-only if on Cloudflare); optionally create a Cloudflare API token limited to DNS edit on this one zone, for the wizard.
5. **Install files**: download `docker-compose.yml`, `Caddyfile`, `.env.example`, and `init-env.sh` from the release matching `FOLD_VERSION`; run `init-env.sh <domain>`; add any provider env overrides.
6. **Start**: `docker compose pull && docker compose up -d`; wait until `docker compose ps` shows the app healthy; check `https://<domain>/healthz`.
7. **Configure**: write `setup.yaml` from the operator's answers; run `docker compose exec app fold setup --config /setup.yaml --dry-run`; show the plan to the operator; run it for real after they agree; or hand the operator the first-admin page and the `/admin/setup` wizard instead.
8. **Hand-off**: tell the operator to store `FOLD_SECRET_KEY` and `.env` in a password manager; confirm a test email arrived; show the setup checklist status; list anything pending (for example SMS registration review).
9. **Upgrades**: `docker compose exec backup backup.sh now`; set the new `FOLD_VERSION`; `docker compose pull && docker compose up -d`; check `/healthz` and the version; read `docs/UPGRADING.md` first.
10. **Troubleshooting**: `docker compose logs app --tail 200`; certificate problems (DNS not pointing, port 80 blocked, Cloudflare proxy on); email not verified (DNS propagation, check records); SMS blocked (registration status); restore from backup (`docker compose exec backup restore.sh <file>`, 11.8).

**Reference only, not supported yet: Render (future M5.5, DEC-26)**

Kept so the work is ready if a church asks. Do not offer this path until M5.5 is done.

2. **Account**: the church creates a Render workspace (Hobby, or Pro if a helper needs their own seat) and a payment method. For an agent, the church creates an API key and shares it; the agent never logs in as the church.
3. **Blueprint**: copy `deploy/render.yaml` from the release matching `FOLD_VERSION`, fill in the domain and region, and validate it (`render blueprints validate`, or Render's API). Outline:

```yaml
services:
  - type: web
    name: fold
    runtime: image
    image:
      url: ghcr.io/grierj/fold:0.8.0          # pin x.y.z
    plan: 0.5c-512mb                          # $7/mo (12.5)
    region: oregon                            # or ohio, virginia, frankfurt, singapore
    healthCheckPath: /healthz
    domains:
      - groups.example.church
    disk:                                     # omit when using S3 storage (keeps zero-downtime deploys)
      name: uploads
      mountPath: /data
      sizeGB: 5
    envVars:
      - key: APP_ENV
        value: prod
      - key: SITE_URL
        value: https://groups.example.church
      - key: DATABASE_URL
        fromDatabase:
          name: fold-db
          property: connectionString
      - key: SESSION_SECRET
        generateValue: true                   # base64, 256-bit
      - key: FOLD_SECRET_KEY
        generateValue: true                   # copy it to the church's password manager
      - key: FOLD_TRUSTED_PROXIES
        value: render                         # app preset for Render's proxy (M5.5)
      - key: FOLD_BACKUP_CHECK                # M5.5 would add this skip switch for Render only
        value: "off"                          # Render PITR covers upgrades
      - key: FOLD_POSTMARK_SERVER_TOKEN
        sync: false                           # typed into the dashboard at creation
databases:
  - name: fold-db
    plan: 0.1c-256mb                          # $6/mo, or 0.5c-1g for $19/mo
    postgresMajorVersion: "16"                # match CI
    diskSizeGB: 5
    ipAllowList: []                           # no access from outside Render
```

4. **Create**: create the Blueprint from the repo or file in the church's workspace (dashboard or API). Enter the `sync: false` secrets when prompted. Migrations run when the app starts (advisory lock), so no pre-deploy command is needed.
5. **DNS**: add the CNAME (or A record) Render shows for the custom domain, remove any AAAA record, and wait for the certificate.
6. **Configure**: open the first-admin page and the `/admin/setup` wizard, or run `fold setup` as a Render one-off job.
7. **Hand-off**: store `FOLD_SECRET_KEY` (from Render's environment page) in the church's password manager; confirm a test email; note the backup coverage (PITR 3 days on Hobby, 7 on Pro; disk snapshots daily) and, if wanted, add the optional cron job for an offsite `pg_dump` with the `fold-backup` image.
8. **Upgrades**: change the image tag (in `render.yaml` or the dashboard) and deploy. A failed migration fails the deploy, and the previous version keeps running.
9. **Troubleshooting**: Render's service logs and events; `/healthz`; certificate status on the custom domain page; Admin > System for the client IP check.

---

## 17. Appendix B: Sources and unverified facts

### 17.1 Sources

- Repository: https://github.com/grierj/fold (read at commit `aa992e2`; README, AGENTS.md, go.mod, models, storage, setup, compose, Caddyfile, merged PRs #1–#6; re-checked read-only at `e7c0e8f` for PR #7 and the review's line references: `main.go:48`, `main.go:129`, `main.go:147-148`, `api_auth.go`, `models/models.go`, `web/package.json`)
- FCC consent revocation rule, 47 CFR 64.1200(a)(10) (per-se keywords, 10 business days): https://www.federalregister.gov/documents/2024/03/05/2024-04587/strengthening-the-ability-of-consumers-to-stop-robocalls
- FCC delay of the revoke-all provision to January 31, 2027 (DA 26-12): https://docs.fcc.gov/public/attachments/DA-26-12A1.pdf and https://www.fcc.gov/document/cgb-extends-effective-date-tcpas-consent-revocation-rule
- TCPA calling hours, 47 CFR 64.1200(c)(1): https://www.ecfr.gov/current/title-47/chapter-I/subchapter-B/part-64/subpart-L/section-64.1200
- Telnyx 10DLC fees: https://support.telnyx.com/en/articles/5634625-10dlc-fees-and-charges and https://support.telnyx.com/en/articles/6325731-register-for-10dlc-messaging
- Telnyx brand registration, entity types, and vetting: https://developers.telnyx.com/docs/messaging/10dlc/brand-registration
- Telnyx sole proprietor rules: https://developers.telnyx.com/docs/messaging/10dlc/sole-proprietor and https://support.telnyx.com/en/articles/13545282-guide-to-sole-proprietor-10dlc-brand-and-campaign-registration (church and ministry names do not qualify)
- Telnyx 10DLC rate limits: https://developers.telnyx.com/docs/messaging/10dlc/10dlc-rate-limits
- Telnyx campaign use cases (Charity, privacy and terms links): https://developers.telnyx.com/docs/messaging/10dlc/campaign-use-cases
- Telnyx 10DLC overview (fee table, number price): https://telnyx.com/resources/what-is-10dlc
- Telnyx messaging pricing: https://telnyx.com/pricing/messaging
- Twilio brand types and limits (sole proprietor, low-volume standard): https://www.twilio.com/docs/sms/a2p-10dlc and https://www.twilio.com/docs/trust-hub/registrations/a2p-10dlc-brand
- Twilio sole-proprietor requirements (OTP, AT&T 15/min): https://www.twilio.com/en-us/blog/products/launches/sole-proprietor-api-and-console-experience-for-10dlc-registration
- Twilio toll-free business registration number rule (from February 17, 2026): https://www.twilio.com/en-us/changelog/business-registration-numbers-required-for-toll-free-messaging-p and https://www.twilio.com/docs/api/errors/30527
- Twilio error 30487 (charity must provide 501(c)(3) verification): https://www.twilio.com/docs/api/errors/30487
- Twilio A2P 10DLC docs (fees, vetting time, use cases, privacy and terms URL rule): https://www.twilio.com/docs/messaging/compliance/a2p-10dlc
- Twilio toll-free verification docs: https://www.twilio.com/docs/messaging/compliance/toll-free
- Twilio SMS pricing: https://www.twilio.com/en-us/sms/pricing/us
- Plivo SMS pricing: https://www.plivo.com/sms/pricing/us/
- IRS EIN (free; churches must use an EIN for tax purposes): https://www.irs.gov/businesses/small-businesses-self-employed/get-an-employer-identification-number and https://www.irs.gov/pub/irs-pdf/fss4.pdf
- Postmark pricing: https://postmarkapp.com/pricing
- Resend pricing: https://resend.com/pricing
- Mailgun pricing: https://www.mailgun.com/pricing/
- Render pricing (workspaces, web services, Postgres, disks, bandwidth, AWS Private Links): https://render.com/pricing
- Render persistent disks (single instance, no zero-downtime deploys, snapshots): https://render.com/docs/disks
- Render Postgres (versions, upgrades, backups and PITR, extensions): https://render.com/docs/databases, https://render.com/docs/postgresql-upgrading, https://render.com/docs/postgresql-backups, https://render.com/docs/postgresql-extensions
- Render free tier limits (spin-down, 30-day database, SMTP ports): https://render.com/docs/free and https://render.com/docs/postgresql-refresh
- Render custom domains and TLS: https://render.com/docs/custom-domains
- Render Blueprint spec (`render.yaml`): https://render.com/docs/blueprint-spec
- Render API: https://render.com/docs/api
- Cloudflare R2 pricing: https://developers.cloudflare.com/r2/pricing/
- Backblaze B2 pricing: https://www.backblaze.com/cloud-storage/pricing
- Wasabi pricing: https://wasabi.com/pricing
- Vultr pricing (VPS and object storage): https://www.vultr.com/pricing/
- 8x8 JaaS pricing: https://jaas.8x8.vc/#/pricing
- DigitalOcean Droplet pricing: https://www.digitalocean.com/pricing/droplets
- Hetzner price adjustment: https://docs.hetzner.com/general/infrastructure-and-availability/price-adjustment/
- Cloudflare plans: https://www.cloudflare.com/plans/
- Caddy automatic HTTPS: https://caddyserver.com/docs/automatic-https
- ChurchTrac pricing and features: https://www.churchtrac.com/pricing
- Meetup Help Center (events, waitlist, guests, hybrid, check-in): https://help.meetup.com
- RFC 8058 (one-click unsubscribe): https://www.rfc-editor.org/rfc/rfc8058

### 17.2 Not verified (re-check before relying on them)

- Twilio monthly 10DLC campaign fees
- Plivo carrier pass-through amounts
- Akamai/Linode prices (the vendor page blocked access; from third-party trackers)
- Zoom Pro price (the vendor page returned 404; from a search summary)
- Google Meet API needing Workspace, and Meet pricing
- The 5-minute limit on embedded meet.jit.si calls (secondary source)
- Block volume prices on each VPS vendor
- Exact state rules on texting hours (stricter than federal in some states; this plan is not legal advice)
- Whether federal calling-hour rules apply to a church's informational texts to members who opted in (Fold applies the stricter default anyway)
- Telnyx local number price: $1/mo on the pricing page vs $1.10/mo in Telnyx's 10DLC guide
- Throughput limits for toll-free numbers, which are provider-specific
- How each provider's brand "entity type" options treat a church that is not a 501(c)(3); ask the provider rather than claim nonprofit tax status
- Whether Telnyx's Charity use case excludes religious organizations as Twilio's does
- Postmark inbound processing pricing (inbound is documented, but its price was not checked; pricing not verified)
- Whether Resend and Mailgun inbound routing can carry the reply relay (7.8) when their adapters are built
- The Render items below matter only if M5.5 is ever started:
- Exactly which header and proxy addresses Render uses for client IPs, so the `render` preset for `FOLD_TRUSTED_PROXIES` must be checked against Render's docs in M5.5
- Whether Render Postgres storage pricing includes the first GB (the estimate in 12.5 assumes it does not)
- Whether an empty `ipAllowList` in `render.yaml` blocks all outside access to the database (check the Blueprint spec in M5.5)
- Whether Render API keys can be limited in scope (assume a key has the creating user's full access)
