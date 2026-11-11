# Fold: Architecture and Development Plan

Fold is church small-group, ministry and activity management: the group-management slice of
ChurchTrac, without the rest of it. This is the product plan: what Fold is for, how it is designed,
what is built, and what comes next. Repo: https://github.com/grierj/fold.

## How to use this plan

- **Where things live.** This file holds the goals, the requirements, the milestone rules and an
  index. The design is in [`design/`](design/), one file per section (5 to 13). Milestones are in
  [`milestones/`](milestones/), one file per phase. Grier's decisions are in
  [`decisions.md`](decisions.md). Delivery status is in [`STATUS.md`](../../STATUS.md) and nowhere
  else. History and evidence (verification records, as-delivered milestone text, slice histories)
  are in [`provenance/`](provenance/README.md) and are not current guidance.
- **Built or planned.** The design files describe what is built, in the present tense. Anything not
  built yet is marked **Planned (Mx.y)** with the milestone that owns it. `docs/API.md` is the
  shipped HTTP contract and `docs/DEPLOY-DIGITALOCEAN.md` the runnable install guide.
- **People** start with sections 1 and 2 below, then the decisions and `STATUS.md`.
- **Agents** read `AGENTS.md`, the design sections for the area, and one milestone in
  [`milestones/`](milestones/). Follow the rules in 14.1. Do not start a milestone until the ones
  it depends on are merged.
- **Precedence.** Grier's decisions win over the requirement wording and over anything else in the
  repo, `AGENTS.md` and `README.md` included; a later decision wins over an earlier one. When the
  repo disagrees with this plan, fix the repo in the milestone that owns the item, or fix the plan
  if the plan is what's wrong.
- **Stable ids.** Section numbers (code cites "PLAN 7.3"), `DEC-n` decisions, `Cn` architecture
  changes ([provenance/foundations.md](provenance/foundations.md)), `Mx.y` milestones, and the
  `D`/`F` requirements below never change meaning or number.
- **Clean break (DEC-22).** No church runs Fold yet, so nothing in today's data has to be preserved.
  The pre-live window for editing the baseline migration closes at the first tagged release or the
  first church deployment (DEC-138).
- MUST and MUST NOT are hard rules. "Should" is the default; you may deviate if you write down why
  in the PR.

## Index

| § | Section | File |
|---|---|---|
| 1 | Goals and non-goals | below |
| 2 | Grier's requirements | below |
| 3 | The starting repo and what to keep (history) | [provenance/foundations.md](provenance/foundations.md) |
| 4 | Architecture change index, C1 to C17 (history) | [provenance/foundations.md](provenance/foundations.md) |
| 5 | Architecture: components, code layout, data model, capacity, jobs, providers, storage, video, time zones, accounts and households, contact privacy, notifications | [design/05-architecture.md](design/05-architecture.md) |
| 6 | Permissions matrix | [design/06-permissions.md](design/06-permissions.md) |
| 7 | Messaging pipeline | [design/07-messaging.md](design/07-messaging.md) |
| 8 | Calendar and ICS feeds | [design/08-calendar.md](design/08-calendar.md) |
| 9 | Rooms and room suggestions | [design/09-rooms.md](design/09-rooms.md) |
| 10 | Security and privacy | [design/10-security.md](design/10-security.md) |
| 11 | Deployment, setup wizard, upgrades, backups | [design/11-deployment.md](design/11-deployment.md) |
| 12 | Third-party services and hosting (17, sources, is at its end) | [design/12-providers.md](design/12-providers.md) |
| 13 | Extra features from research | [design/13-research-features.md](design/13-research-features.md) |
| 14 | Roadmap: rules and releases below; phases 0 to 7 | [milestones/](milestones/) |
| 15 | Decisions; still-open questions below | [decisions.md](decisions.md) |
| 16 | Original AGENTS.md deployment outline (history; Render reference for M5.5) | [provenance/appendix-a-agents-deployment.md](provenance/appendix-a-agents-deployment.md) |
| 17 | Sources and unverified facts | [design/12-providers.md](design/12-providers.md) |

---

## 1. Goals and non-goals

### Goals

1. Give a church one place for groups, events, RSVPs, rooms, and group messaging (email, push and,
   later, SMS). It replaces the social and group parts of ChurchTrac and borrows Meetup's
   events-and-groups model.
2. Self-host first. A church member who is comfortable with a Linux VPS can install it in under an
   hour with `docker compose`. A VPS with Compose is the only recommended deployment for now; a
   managed platform (Render) is a later, on-request option (12.5, DEC-26).
3. Single-tenant. One deployment serves one church. Fold is free, open-source (MIT) give-away
   software (DEC-11). "Hosted" means someone (Grier, rarely, or anyone) runs one deployment per
   church. Hosting terms and any fee are agreed case by case and are not meant to make money
   (DEC-10). The church brings and owns its own email, SMS, and hosting accounts, even when hosted,
   unless there is a special agreement (DEC-2).
4. Few external dependencies. Only email, the host, and (for texting) SMS are needed outside the
   box. The only database is PostgreSQL, run by Docker Compose in production, development, and
   tests (DEC-1, DEC-24). Everything else (object storage, video, DNS automation) is optional.
   **Self-contained by default (DEC-43):** Fold builds what it needs itself and adds an outside
   service only when that keeps deployments simple or a church asks for it. Notifications, for
   example, are built in Fold with no vendor (DEC-42, 5.12).
5. Every external dependency can be set up by API: by the app's setup wizard, by the `fold` CLI, or
   by an AI agent following `AGENTS.md`. A power user can still do every step by hand.
6. Every proprietary dependency sits behind an adapter interface. Build one real adapter per kind
   (plus the `devoutbox` for tests), and add a second only when a church demands it (DEC-140). SMTP
   and Postmark both exist for email because the first churches need a generic fallback and a
   deliverability default; SMS v1 is Telnyx only, and Twilio waits for a church that asks.
7. Familiar UI. Meetup is the primary UI and design reference: copy its patterns (group cards, date
   badges, RSVP buttons). ChurchTrac is the secondary reference, for church-admin screens Meetup
   has no equivalent for (people and households, rooms, attendance, consent, setup).

### Non-goals

- Multi-tenant SaaS (one database for many churches).
- Giving, donations, pledges, fund accounting, contribution statements. See 13.3 for the reasons.
- SQLite or any database other than PostgreSQL (DEC-1, DEC-24).
- Python, anywhere in the repo or its tooling (DEC-22).
- Accounts for anyone under 13. Younger children exist only as household people with no login
  (DEC-8).
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

Each requirement maps to the sections and milestones that satisfy it. Agents: if your change would
break one of these, stop and ask. The decisions refine these requirements; where a decision and the
wording below differ, the decision wins.

| # | Requirement | Where it is handled |
|---|---|---|
| D1 | Self-host first | 11, 12.5, M5.x |
| D2 | Single-tenant SaaS; hosted = one deployment per church. Refined by DEC-2 and DEC-10: the church owns its provider and hosting accounts; terms and fees are case by case and not for profit | 1, 11.6, DEC-2, DEC-10 |
| D3 | Minimal external dependencies (one database: PostgreSQL, DEC-1) | 5.1, 12, C14 |
| D4 | Every dependency manageable by AI agents | 11.4, 11.5, `docs/agents/deploy.md`, M5.4 |
| D5 | No added deployment complexity; a power user can do it by hand | 11, 12.5, `docs/DEPLOY-DIGITALOCEAN.md` (M5.4) |
| D6 | External deps limited to text, email, simple hosting where possible | 5.1, 12 |
| D7 | Every dependency has API configuration; the app does as much setup as it can | 11.3, M4.x |
| D8 | Proprietary deps sit behind adapters: one real adapter per kind, a second only when a church demands it (DEC-140) | 5.6, 12 |
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

---

## 14. Phased roadmap and milestones

Phases: [0 Foundations](milestones/phase-0.md) · [1 Events and people](milestones/phase-1.md) ·
[2 Rooms](milestones/phase-2.md) · [3 Messaging](milestones/phase-3.md) ·
[4 Setup and providers](milestones/phase-4.md) · [5 Release engineering](milestones/phase-5.md) ·
[6 Community features](milestones/phase-6.md) ·
[7 First-church hardening](milestones/phase-7.md). Each milestone states what it depends on, its
scope, its acceptance criteria and its tests. Where each one stands is in
[`STATUS.md`](../../STATUS.md).

### 14.1 Rules for every milestone

- Split high-complexity work (database + API + UI, or 3+ tables) early into thin end-to-end
  slices, one reviewable PR per slice: database, then API and internals, then UI. Smaller
  milestones may be one PR (Grier's rule).
- Before starting, read `AGENTS.md`, the plan's design sections for the milestone, and the code of
  the milestones it depends on.
- A check only a person can run on real devices or live services (phones, mail providers, TLS on a
  real domain, SMS) is built best effort and listed in
  [`docs/FIRST-CHURCH-CHECKS.md`](../FIRST-CHURCH-CHECKS.md) in the same PR; it does not block the
  merge (DEC-128).
- Schema changes are expand, then contract (DEC-138): an expand migration only adds and backfills,
  and a contract migration may only follow in a later tagged release. Two PRs never land the same
  migration number (DEC-146). The full rules are in `docs/agents/migrations.md`.
- Done means:
  - [ ] `go vet ./...` and `go test ./...` pass against PostgreSQL (`make test` locally, a Postgres
    service container in CI)
  - [ ] `npm run build` and `npm test` pass in `web/`, and any Playwright test the milestone lists
    passes in CI
  - [ ] Schema changes are new goose migrations (the one exception is the pre-live baseline
    window, DEC-138), and the drift check shows no drift (C1)
  - [ ] `docs/API.md` and the SPA types (`web/src/types.ts`) are updated for any API change
  - [ ] Tests use the `devoutbox` providers and `httptest` fakes; never live services
  - [ ] Permissions go through `internal/policy`, with table tests for new rules
  - [ ] The milestone's acceptance boxes are ticked in its phase file, and `STATUS.md` is updated,
    in the same PR

### 14.2 Releases

No release has been tagged yet. The table is the intended grouping of milestones into releases;
milestones are built in dependency order, not release order (rooms, for example, were built before
the email work).

| Release | Contents | Milestones |
|---|---|---|
| v0.2 Foundations | Postgres-only clean baseline, CLI, trusted proxies, CSRF route groups, visibility enum and policy, email login, member removal and sole-leader handoff, security headers and dependency scanning, frontend test tooling | M0.1–M0.4 (M0.3 is M0.3a + M0.3b) |
| v0.3 Events and people | Events and occurrences (time zones, series edits, church-wide hosts, visibility), email basics, account requests, guests, waitlist and pruning, calendar, ICS, video and flags, households and teen accounts, contact privacy, frontend toolchain upgrades, per-date visibility, series RSVP and RSVP-aware calendar | M1.1a–M1.14 |
| v0.4 Email and push | Jobs, Postmark and webhooks, consent, composer, pipeline (with guardian copies), email reminders, one-to-one messages and reply relay, web push | M3.1–M3.6, M3.11, M3.13 |
| v0.5 MVP | Encrypted settings, SPA setup wizard, branding and asset customization (draft M4.2b), legal pages, email provisioning, people import, admin two-factor sign-in, images, backups, upgrades, deploy docs, plus the first-church hardening floor (setup token, privacy bundle, delivery outbox, baseline squash, erasure, perf fixes, secret rotation): the first version a church can run | M4.1–M4.5, M4.2b, M4.9, M5.1–M5.4, M7.1–M7.7 |
| v0.6 Rooms | Rooms, by-room view and feed, suggestions | M2.1–M2.3 |
| v0.7 SMS | Telnyx, keywords and revocation, send window and caps, SMS reminders, member requests, notification preferences and SMS, SMS provisioning | M3.7–M3.9, M3.12, M4.6 |
| v0.8 CLI | `fold setup` (the S3 and private-assets adapter waits until a church asks, DEC-140) | M4.8 |
| v1.0 | Hardening, sign-up sheets, attendance, security review (Twilio deferred until demanded, DEC-140) | M6.2, M6.3 + fixes |
| v1.x and later | Community features | Rest of M6 |
| Future, on request (not committed) | Render Blueprint, Twilio adapter, S3 adapter, ChMS sync, durable audit log: each re-enters only as a new milestone for a named church (DEC-140) | — |

---

## 15. Decisions and remaining open questions

Every decision is in [decisions.md](decisions.md).

### 15.2 Still open

- DEC-76: where and how members discover public events outside their own groups.
- DEC-78: whether a person can hide completely (distinct from contact privacy), and whether to
  rename "private" groups. Until decided, active teens and contact-hidden adults remain searchable;
  private groups remain visible to signed-in members.
- DEC-40: ChMS field ownership and local-control locking. Deferred: it reopens only if roster sync
  comes back as a new milestone (M6.15 was cut, DEC-140). One-way import and no write-back are
  already decided.
- Notice email timing (R6 open question 3): whether a non-urgent notice's email waits 15 minutes
  and is skipped if the person has already seen or read it in the app. M3.12 plans it; today
  `notify.deliver` sends at once.
