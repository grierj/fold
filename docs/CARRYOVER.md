# Carry-over from Fold

Fold is the predecessor project. This page names everything that may come across, and nothing else
does.

- **Source:** `github.com/grierj/fold`, commit `f69b436da63d962ac0802ecf6fe9ada09ec0f2d4`
  (master on 2026-11-26). Paths below are relative to that repository. Always read from that
  commit, so this page stays true.
- **Licence:** MIT, copyright Grier Johnson. Code may be copied freely. Bring `LICENSE`.
- **Rule:** copy only what section 1 or 2 names. Section 3 is for reading, not copying. Section 4
  stays behind. If you want something that is not listed, ask first; Fold's shape is the thing this
  project exists to leave.
- **Tests travel with their package.** A package's table tests are its specification.

Line counts are of non-test Go at the pinned commit.

---

## 1. Copy as it is

These import only the standard library and the modules named. Their Go import paths begin
`fold/`, and this project's module is named `fold` (R14), so nothing needs renaming.

| Path | Lines | What it is | Needs |
|---|---|---|---|
| `internal/images/` (`normalize.go`, `upload.go`, `imagetest/`) | 892 | Sniffs JPEG, PNG and GIF, bounds the memory a decode may use, re-encodes without metadata, limits how many run at once | nothing |
| `storage/storage.go` | 788 | The `Store` interface and the local-disk store, with server-generated keys. The contract a future store must meet is written in the file | nothing |
| `internal/content/` (`content.go`, `merge.go`, `email.go`, `email.html`) | 441 | The rich-text allow-list sanitizer, merge fields, and the email layout | `bluemonday`, `golang.org/x/net/html` |
| `internal/providers/` | 157 | The mail sender interface and registry | nothing |
| `internal/events/` (`expand.go`, `series.go`, `capacity.go`, `ics.go`, `place.go`, `usual.go`, `testdata/`) | 985 | The event clock: wall-clock recurrence in a zone, the cap on dates, capacity arithmetic, ICS output, `LoadZone` | `rrule-go`. One line to change: `expand.go` calls `models.ParseCalendarDate`; copy that small function in |
| `web/src/fonts/` | | The Inter font files **and `OFL.txt`**, which must stay beside them | nothing |
| `web/src/assets/covers/`, `web/src/assets/illustrations/` | | 14 default group covers and 12 empty-state illustrations (SVG) | nothing |
| `web/public/assets/brand/` | | The Fold mark, icons and social image. The project takes the Fold name again (R5), so the brand comes with it | nothing |
| `Caddyfile` | 3 | The whole proxy configuration: the domain, passed to the app (R9) | nothing |
| `LICENSE` | | MIT | nothing |

## 2. Copy and adapt

Each needs real changes. The rules and the tests are what is valuable.

| Path | Lines | Keep | Change |
|---|---|---|---|
| `internal/policy/policy.go` | 924 | Every permission as a pure function: who sees a group or event, who may register, who manages, member removal, role changes | Remove the rules for contact requests and exact-date access. Add the rules this project changes (`docs/DECISIONS.md` R3, P4 to P8) |
| `internal/policy/registration_lifecycle.go`, `message_lifecycle.go`, `recipient_lifecycle.go`, `messages.go`, `calendar.go`, `date_page.go` | 623 | The tables of which status may follow which, and who may message whom | Drop the push channel from `recipient_lifecycle.go`, and the handling of dates kept after leaving from `calendar.go` |
| `internal/policy/room_lifecycle.go`, `rooms.go`, `series_room.go` | 413 | Booking and request moves (slice 6) | Check `series_room.go` against `docs/DECISIONS.md` before use; it carries Fold's rule for dates with their own room |
| `internal/policy/contact.go`, `occurrence_access.go`, `departure.go`, `push.go` | 862 | **Do not bring.** They serve features left behind or deferred | |
| `internal/rooms/busy.go`, `suggest.go` | 365 | The rule for "this room is busy" over padded times, and the suggestion ranking with its test cases | The queries are Postgres (range types, GORM). Rewrite them for SQLite. The 13 suggestion cases are in `docs/plan/design/09-rooms.md` section 9.3 |
| `internal/mail/mail.go`, `postmark.go`, `postmark_webhook.go` | 678 | The SMTP sender, the Postmark sender, the webhook parser, the dev outbox idea | The outbox and `suppress.go` (177 lines) store through GORM; rewrite the storage. Make TLS required on SMTP (`docs/HAZARDS.md` section 7) |
| `internal/httpx/security.go`, `accesslog.go`, `body.go` | 137 | The security headers, the access log that redacts secrets in paths, the body size limit | They are Gin middleware; rewrite for the standard library. The content security policy must be rewritten for server-rendered pages |
| `internal/app/setup.go`, `internal/app/templates/setup.html`, `setup-unlock.html` | 177 + 113 | The setup-code flow that stops a stranger claiming a fresh install | Becomes the first step of the setup wizard |
| `internal/app/csrf.go` | 112 | The synchronizer token | Gin to standard library |
| `internal/app/zones.go` | 118 | Church zone, person zone, event zone, and which may fall back to UTC | Storage calls |
| `internal/app/registration_seats.go` | 259 | Seat arithmetic: whether a party fits, party size, childcare demand | Remove the SQL twins; with one writer the Go form is enough |
| `web/src/index.css`, `tokens.css`, `base.css` | 369 | The `@theme` colour, type and radius tokens, the layout measures, and the shared classes (`.btn-primary`, `.input`, `.chip`, `.fold-page`, `.fold-list`) | Becomes this project's stylesheet. Build it with the Tailwind standalone binary, so Node is not needed to release |
| `design.md` | 86 | The design rules: tokens, composition, density, control sizes and states. The brand and Fold Violet stay (R5) | It is written for Fold's React components. Rewrite those lines for template partials |
| `docker-compose.yml` | 153 | The `app` and `caddy` services, their volumes, and the private network with a fixed address range that the app trusts for client addresses | Drop the `db` and `backup` services. One volume for the app's data |
| `web/e2e/a11y.spec.ts`, `layout.spec.ts`, `ui-pages.ts`, `ui-baseline.json`, `web/scripts/token-contrast.mjs` | about 590 | The accessibility gate (axe at 390 and 1280 px), the layout gate (no sideways scroll, no clipped control, tap targets), the contrast check | Point them at this project's pages. Developer tooling only; none of it ships |

## 3. Read, do not copy

Open these for a specific question, then write the answer fresh.

| Path | Read it for |
|---|---|
| `docs/plan/decisions.md` | The full text of any Fold decision cited in `docs/DECISIONS.md` |
| `docs/plan/design/06-permissions.md` | The permissions matrix |
| `docs/plan/design/05-architecture.md` sections 5.4, 5.9, 5.10 | Capacity and waitlist rules, time zones, accounts, ages and households |
| `docs/plan/design/07-messaging.md`, `08-calendar.md`, `09-rooms.md` | How announcements, calendar views and feeds, and rooms behave |
| `docs/plan/design/10-security.md` section 10.6 | The hardening checklist |
| `docs/plan/design/12-providers.md` | Mail providers, and what a church must register before it can text (if SMS returns) |
| `internal/app/date_projector.go` (593 lines) | Everything that decides how one date looks to one viewer. The idea carries: one function builds the view, every page renders it |
| `internal/notify/kinds.go` (916 lines) | Every notice: its title, its body, whether it is required or optional |
| `web/src/lib/errors.ts` | The wording of 235 error messages |
| `web/src/lib/when.ts` (460 lines) | How dates and times are worded |
| `docs/MEMBERS.md` | The member help page |
| `web/e2e/*.spec.ts` | Journeys worth keeping as acceptance scenarios |
| `docs/FIRST-CHURCH-CHECKS.md` | The checks only a person can run: email delivery, a live install, real phones |
| `docs/AUDIT-2026-11-01.md`, `docs/plan/research/competitor-teardown-2026-11.md` | Why the feature list is what it is |
| `docs/plan/research/fresh-start-2026-11.md` | Why this project exists. Not at the pinned commit; see the note below |
| `internal/push/`, `docs/agents/push.md`, `docs/plan/research/web-push.md` | Web push without a vendor, if a church asks for it |
| `docs/ignored-security-issues.md` | The format for recording a low-scoring security finding |
| `docs/plan/provenance/cut-designs.md`, first section | Fold's sketch of a tamper-evident audit log: what it should cover, hash chaining, export for a legal request. This project builds it (`docs/DECISIONS.md` R6, P17) |
| `internal/audit/audit.go` | The names Fold gave its audited actions, as a checklist of what must leave a row |

**Note on the fresh-start brief.** It was committed to Fold after the pinned commit, as
`1a070122` on the branch `t3/fresh-start-research-brief`. Read it from that branch, or from master
once it is merged. Its addendum records what Grier decided.

## 4. Leave behind

- `internal/app/` handlers (`api_*.go`), `models/`, `migrations/`: the shape this project replaces.
- `web/src/` components, pages, hooks, `types.ts`, `api/`: the separate client.
- `deploy/`, `scripts/`, the other compose files, the backup image: replaced by the app, Caddy and
  a built-in backup.
- `internal/jobs/` and its model: the queue is trivial with one writer.
- `internal/txguard`, `internal/userlock`, `internal/testdb`, `internal/guardkit`,
  `internal/consent`, `internal/reasonscan`, every `guards_*_test.go`: machinery for problems this
  design does not have.
- Everything named erasure, tombstone, restore fence, reason review or admin operation attempts.
- `internal/audit/audit.go` and `internal/notify/notify.go`: write new, small versions. The audit
  row and the notice are simple once names are not stored in them.
- Fold's `AGENTS.md`, `STATUS.md`, `docs/API.md`, `docs/agents/`, `docs/plan/milestones/`.

## 5. Modules

Bring only these third-party Go modules to start: `teambition/rrule-go` (recurrence),
`microcosm-cc/bluemonday` and `golang.org/x/net` (sanitizing), `golang.org/x/crypto`, a pure-Go
SQLite driver, and a migration runner if one is wanted. Anything else is justified in the pull
request that adds it. Before choosing the SQLite driver, the migration runner, the stylesheet
build or the fragment-update script, check its current state: the brief that proposed them was
written from memory.
