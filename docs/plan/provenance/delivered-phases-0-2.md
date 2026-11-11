# Delivered milestones: Phases 0 to 2 (as-delivered record)

This file keeps the full milestone text of Phases 0, 1 and 2 as it stood when the work was done:
Phase 0 (foundations, v0.2), Phase 1 (events and people, v0.3) and Phase 2 (rooms, v0.6). It is
history, not the current design. The design sections ([`design/`](../design/)),
[`AGENTS.md`](../../../AGENTS.md), [`docs/API.md`](../../API.md) and the code are the current
truth; where this file disagrees with them, they win. The short summaries live in
[`milestones/phase-0.md`](../milestones/phase-0.md), [`phase-1.md`](../milestones/phase-1.md) and
[`phase-2.md`](../milestones/phase-2.md).

The text below is copied verbatim. Section numbers in it ("5.4", "10.1") point at the design
sections; "14.1" is in [`PLAN.md`](../PLAN.md). Migration numbers in it (for example 00050)
predate the baseline squash; that schema now lives in `migrations/00001_baseline.sql`.

## Phase 0: Foundations (PLAN 14.3)

Phase 0 is done (v0.2) and merged before Phase 1. It delivered the Postgres-only baseline, the CLI and request plumbing, the policy package and audit log, email login, member removal with sole-leader handoff, security headers, and the frontend test tooling. The live rules are in sections 5–10 and the decisions in section 15; the code and tests are the source of truth. [STATUS.md](../../../STATUS.md) records delivery.

**M0.1 PostgreSQL only, clean baseline, CLI, request plumbing**
- Delivered: SQLite is gone (C14, DEC-1, DEC-24). Dev and tests use `DATABASE_URL`; `deploy/docker-compose.dev.yml` and the `Makefile` (`dev`, `test`, `lint`) start the dev database (11.9). goose runs embedded SQL migrations from a clean baseline (DEC-22) under a Postgres advisory lock, so two containers never migrate together.
- The CI drift check (C1) migrates a throwaway database with goose, runs `AutoMigrate` with a statement-recording logger, and fails on any `CREATE` or `ALTER`. A self-test proves the check works. It does not use GORM's `DryRun`.
- CLI: `serve`, `healthcheck`, `migrate`, `version`, with lazy config: `version` needs no env and `migrate status` needs only `DATABASE_URL` (C11).
- Request plumbing (C17): the compose network has a fixed subnet that is the only default trusted proxy (`FOLD_TRUSTED_PROXIES`). `/hooks/*` and `POST /u/unsubscribe/<token>` sit outside the CSRF middleware, and a test pins the exempt list.

**M0.2 Visibility enum, policy package, leadership by membership, audit log**
- Delivered: one `visibility` enum on groups (C3) and `internal/policy`, which holds every permission check. Leadership comes only from `Membership.role=leader`; `Group.LeaderID` is the displayed primary contact only (C12). Invite-only groups are 404 to non-members, and admins see every group (DEC-5).
- A group can have several leaders, the last leader can't be removed, and a teen can't be made a leader (DEC-18; the policy rule came first, the teen accounts in M1.10).
- The `AuditLog` table (5.3) and the one `audit.Write` helper. Making or removing a leader writes `membership.role_changed` and shows admins an in-app notice, with no admin gate (DEC-31).

**M0.3a Log in by email**
- Delivered (C16, DEC-23): the email is the login (`citext`, unique, no username) and `password_hash` is nullable. Sign-in and the first-admin page use email in any letter case.
- Throttling is keyed by email and by client IP and stored in Postgres: ten failures lock an email for 15 minutes across IPs, fifty lock an IP. An unknown email and a wrong password answer alike. Changing a password or role, or disabling an account, bumps `session_version` (10.1).

**M0.3b Leaders remove members, sole-leader handoff, security headers, dependency scanning**
- Removal (DEC-35): `DELETE /api/v1/groups/:id/members/:userId` (optional `{note}`) and the admin twin `DELETE /admin/groups/:id/members/:userId`. `CanRemoveMember` and `CanChangeRole` decide: admins remove anyone, leaders only from groups they lead, nobody removes the last leader, and a leader can't remove or demote an admin. Leaving is `POST /groups/:id/leave` with the same rules. Removal sets `status=removed` and keeps the row. The person can't rejoin alone (403 "A leader removed you from this group"), and a leader or admin can add them back. It writes `membership.removed` (the note stays in the audit row, never shown to the person) and gives admins the `leadership` notice when a leader goes.
- Sole-leader handoff (DEC-36): `GET /api/v1/admin/users/:id/sole-leader-groups` lists groups where the account is the only active leader, with candidate successors (the group's other active adult members and every admin). `DELETE /api/v1/admin/users/:id`, and disabling, take `{successors: {"<groupId>": <userId>}}` and refuse with 400 and the list when one is missing. One transaction makes the successors leaders, then deletes or disables. Audit rows are `membership.role_changed` per group (reason `account_deleted` or `account_disabled`) and `user.deleted` or `user.disabled`. `leaderCount` counts only active accounts.
- Disabled accounts (DEC-38): memberships and registrations are kept but hidden and never counted. Enabling restores them as plain member (`membership.restored`, `user.enabled`). In a full group the person is left out, `membership.not_restored` is written, and they get a notice to ask a leader (DEC-47); memberships are `active`, `removed` or `left` (DEC-86). The members JSON has an `admin` flag, and Remove is hidden for admins unless the viewer is an admin.
- Member limit (DEC-39, 5.4): nullable `groups.member_limit` (`CHECK > 0`) and `policy.GroupHasRoom`. Join, the leader's add and the admin add share one path that locks the group row. A full group answers self-join 409 "This group is full"; leader and admin adds get 409 with `full: true` unless the body has `overLimit: true`, which writes `membership.added_over_limit`. Lowering the limit below the count keeps everyone. Only active memberships of listed accounts count.
- Security headers (DEC-37, 10.1, 10.6): the app sets CSP (`default-src 'self'`, no `unsafe-inline`; `img-src 'self' data: blob: https:`, where `blob:` is for upload previews and `https:` for cover images by URL), HSTS when `COOKIE_SECURE` is on, `nosniff`, `Referrer-Policy: same-origin`, `X-Frame-Options: SAMEORIGIN` and a `Permissions-Policy` that turns off camera, microphone and geolocation. `/setup` works under the same CSP. The image compressor runs on the main thread because its web worker loads code from a CDN.
- Dependency scanning: Dependabot updates Go, npm, GitHub Actions and Docker base images weekly. CI runs a pinned `govulncheck ./...` and `npm audit --omit=dev --audit-level=high`.
- Left for M4.7: tighten `img-src` once uploads are the only image source. Left for M5.1: Caddy and image-level headers, and pinned image digests in releases.

**M0.4 Frontend and browser test tooling**
- Delivered: Vitest with Testing Library (`npm test`), Playwright with a smoke test under `web/e2e` (sign in, see the groups page), `make e2e`, and a CI job that installs Chromium and runs both.


## Phase 1: Events and people (PLAN 14.4)

Phase 1 is done (v0.3), merged through PR #184. It delivered events with time zones and occurrences, stored notifications and email, the ways into an account, guests, capacity and waitlists, the calendar and ICS feeds, video, remote and childcare, households and teen accounts, contact privacy, per-date visibility, series RSVP, and the frontend toolchain upgrades. The live rules are in sections 5–10 and the decisions in section 15; the code and tests are the source of truth. [STATUS.md](../../../STATUS.md) records delivery, and [M1.10](../provenance/M1.10-VERIFICATION.md), [M1.11](../provenance/M1.11-VERIFICATION.md), [M1.13](../provenance/M1.13-VERIFICATION.md) and [M1.14](../provenance/M1.14-VERIFICATION.md) retain the verification evidence.

**M1.1a Events, time zones, occurrences, series edits**
- Delivered: Meeting is Event and RSVP is Registration (DEC-22). The wall clock (`starts_local` plus IANA `tzid`) is the source of truth and expansion uses `DTSTART;TZID` (C2), so a 7:00 pm America/Boise event stays 7:00 pm across daylight saving changes. The API returns UTC and local times.
- `EventOccurrence` rows store UTC, are generated 18 months ahead and are extended daily (5.5). A cancelled date is an occurrence status.
- A series edit (5.3) moves future occurrences that have no per-date override, keeps their registrations, cancels dates that no longer exist and tells their registrants. One shared `SeriesEditor` serves the group and event pages. The church time zone is the default and the browser zone comes first in the shared `ZonePicker` (DEC-50, DEC-65).

**M1.1b Church-wide hosts, event visibility, audit, attendee privacy**
- Church-wide events (`group_id` null) are created by admins and need at least one `EventHost` (DEC-7). They can't be `invite_only`; a non-admin host can switch them between `public` and `private` and can edit, message and see registrants of the events they host, nothing more.
- An event stores its own `visibility`, copied from the group at creation, plus a `leader_opened` marker (DEC-48). It is chosen at creation and edited inside Edit series (DEC-51). Making a group more private tightens inherited events, cancels and notifies registrants who lose access, and asks the leader about each leader-opened event. Tightening a single event previews, confirms, cancels and notifies the same way (see Consistency in AGENTS.md).
- A change more open than the group writes an `event.visibility_override` audit row (reason optional, DEC-28) and notifies the group's other leaders and the admins (DEC-6, DEC-17).
- Attendee privacy: for an opened event of an invite-only group, outsiders see "Hosted by <church name>" and an attendee count, never names, the group or its leaders (the PR #41 rule). Leaders, admins and group members see names.
- Left for M3.11: the "Message the organizers" button on that event page.

**M1.1c Stored in-app notifications**
- Delivered (5.12, DEC-42): the `notifications` table, `internal/notify` with its kind registry, endpoints for feed, badge, seen, read, read-all and archive, the bell, panel and `/notifications` page. Rows are written in the same transaction as their `AuditLog` row, and `dedupe_key` makes a repeat write one row. Archive is server-side (`archived_at`), and the SPA keeps no notification state in localStorage.
- Tasks (`waiting`) resolve when decided; a second admin sees "Done · approved by …" and a losing inline Approve gets 409 with `resolvedBy`. Inline actions are allow-listed (new-group Approve, eligible account requests) and checked on the server. A `link` must be an internal path, and no kind carries an email address or phone number. Other people's rows answer 404.
- `GET /notifications/badge` returns `{unread, waiting}` and 304 when unchanged; the SPA polls every 60 seconds, refreshes on focus and navigation, and pauses while hidden. The badge is one plain count (DEC-44) and the bell shows no upcoming-event items.
- Left for M3.12: the settings gear and `/settings/notifications`. No kind batches yet; batching is built and tested with a test kind.

**M1.2 Email basics, password reset, invitations**
- Delivered (C6): `internal/mail` with `devoutbox` in dev and tests and SMTP from `FOLD_SMTP_URL` (STARTTLS or implicit TLS), plain transactional templates, and `/dev/outbox` in dev only.
- Password reset answers the same message for every address. Reset and invite tokens are single use and expire, and a new one revokes the earlier. A verified email change sends a link to the new address and a notice to the old, applies only after verification, and ends other sessions (10.1).
- Admin and leader `Invitation`s need no approval. A leader can invite only into groups they lead, with a per-leader daily limit (default 20; the next answers 429 and is audited). Accepting into a full group follows the leader's add rule (DEC-39).
- Notification email: kinds marked `AllowEmail` send after the transaction commits, recorded in `notification_deliveries`. Sensitive kinds use the link-only `RenderEmailSafe` variant.
- Left for M3.1: the 15-minute "if not seen" delay.

**M1.3 Self-signup, account requests, age gate**
- Delivered (5.10, DEC-4): self-signup `AccountRequest`s, a member's "invite a friend", and the admin approval queue (DEC-30). The `self_signup` setting is `off` or `request` (default `request`). Nobody signs in until an admin approves, and requests expire after 30 days.
- Responses never reveal an account: a request for an existing email gets the usual page and a sign-in and reset hint by email. Public forms allow 10 per IP per hour and have a honeypot; past the limit the form shows its usual message, stores nothing and logs (DEC-52).
- Age gate: "under 13" stores nothing and shows the household explanation; teens go to `pending_consent` (finished in M1.10).
- The bell's inline Approve appears only for an adult request with no warnings; a teen request, or one with a duplicate, rate-limit or honeypot flag, shows Review and the server refuses inline approval (DEC-44).

**M1.4 Guest registration**
- Delivered (C5; AGENTS.md states the rule): a signed-out adult registers for a public event with name, email and "I am 18 or older"; without it nothing is stored. The submit stores only a `guest_signups` row. The guest `User` and `Registration` (`source=guest_form`, hashed manage token) exist after the emailed link is redeemed from a button, so mail scanners confirm nothing. The rule is `policy.CanRegisterGuest`.
- The manage link cancels the registration and stops working once the date has ended (DEC-55). Registrations use a separate `cancelled` status (DEC-54). Guests get no password reset (DEC-53).
- Verification emails cap at 3 per hour and 10 per day per address, whatever the IP. The "you already have an account" hint shares that cap. The form never answers 429 (DEC-52). A guest who gets an account keeps their registrations (same `citext` email). Tightening an event to `private` cancels guest registrations through the usual preview.

**M1.5 Capacity, waitlist, pruning**
- Delivered (5.4, DEC-20): in-person capacity and waitlist under a per-occurrence lock (50 concurrent requests on 10 seats give 10 going and 40 waitlisted). A sign-up is `going` or `waitlisted` at once, with no approval step (DEC-30). Leaders and hosts can remove anyone, and the person is told. Remote and childcare counting (M1.8) and household people (M1.9) extend the same rules.
- Cancelling promotes the earliest entry that fits, skipping a party too big for the free seats. Inside the cutoff nobody is auto-promoted and the leader is notified. Promoted people get an email with a one-click "I can't come" link.
- Disabled accounts' registrations are hidden and never counted. A restore that would overfill an occurrence is dropped, audited, and the person is told to ask a leader or host, who can add them over capacity by explicit choice (DEC-38, DEC-47).
- Event-change notifications (5.12): a cancellation, a time or place change and "you're in" give each going or waitlisted person a bell item (cancellations never batched) and an email. Event items expire at the occurrence end and are archived, not deleted. Reminders stay out of the bell (7.7).

**M1.6 Calendar views**
- Delivered (8.1): agenda, week and month views with filters, working at phone width. A member never sees invite-only events of groups they aren't in. Times show in the viewer's zone, and the event page names the event's own zone when it differs (DEC-57).

**M1.7 ICS feeds**
- Delivered (8.2): feeds with revocable tokens (a revoked or regenerated token is 404) and ETags. Recurring events export authorized stored dates as standalone VEVENTs with stable original-date UIDs and no recurrence-generating properties. Leaving an invite-only group drops its events from the personal feed on the next fetch. Video links follow `video_reveal` (M1.8, DEC-70).

**M1.8 Video, remote, childcare**
- Delivered (5.8, F11–F13): video links (a link, or a generated random Jitsi URL on the configured domain), a remote flag with `remote_capacity`, and a childcare flag with `childcare_capacity` and counts on registration. Events filter by "remote available" and "childcare available".
- There is no video embed or iframe; the link opens in a new tab (DEC-16). `registered_only` shows the link to going registrants and managers; the personal ICS also allows Maybe and unanswered members and hosts (DEC-70).
- Other people's childcare counts and notes are visible only to admins and the event's leaders and hosts; registrants see their own. The disabled and restore rules of DEC-38 and DEC-47 apply to both capacities.

**M1.9 Households and household people**
- Delivered (DEC-8, DEC-23, DEC-32): `Household`, `HouseholdAccount`, `HouseholdPerson` (child, teen, adult) and `GuardianLink`. A holder registers household people, including a spouse who shares their email. They count toward capacity, rosters name the responsible holder, and transactional notices go to the holder.
- An admin confirms every guardian link to a child or teen; adults need no link approval (DEC-32, DEC-79). Adult conversion transfers registrations only on account approval; teen transfer waits for M1.10 consent and link prerequisites (DEC-82, DEC-83). Care notes follow guardian and admin access and the scheduled-going window (DEC-81).
- `docs/MEMBERS.md` holds the member guidance from 5.10.

**M1.10 Teen accounts, consent, badges**
- Delivered (DEC-8, DEC-18, DEC-20): teen accounts with `TeenConsent`, guardian links to teen accounts, consent emails with a signed link, and Teen and Child badges on attendee lists and rosters for leaders, hosts and admins (others see counts). No path creates a `User` under 13.
- A teen can't sign in until `account` consent is granted; pending teens browse public groups and events as signed-out visitors (DEC-85). A confirmed guardian grants and revokes consent; an admin grants only with evidence text. Every guardian link to a teen is pending until an admin confirms it, with no auto-confirm path (DEC-32). A consented teen can see and register for anything a member could, and can't be a leader, host or admin.
- The daily church-local age rollover makes a teen an adult at 18 and keeps the consent history. Re-enabling a disabled teen restores `active` only if the latest `account` consent is still granted, else `pending_consent` (DEC-69). Every consent change and link confirmation writes an `AuditLog` row.
- Household teen conversion creates the account only after approval. Sign-ups stay household-managed until account consent and a fresh guardian link are confirmed, then transfer completes in one transaction. At 18 those requirements end; suspension or an unavailable responsible account still delays transfer (DEC-83).
- Left for M3.4: recipient-list badges in the composer.

**M1.11 Contact privacy and direct-contact requests**
- Delivered (5.11, DEC-31): leaders and hosts never get a teen's contact details. Adults can turn on "Hide my contact info from group leaders" (off by default). `ContactRequest` and `ContactGrant` carry the flow: a hidden adult decides alone; for a teen, an admin and the teen must both approve and either one declining ends it. The person, an admin, or a teen's confirmed guardian can revoke at once. Requests expire after 14 days and a leader has at most 10 open. Grants end when the leader stops leading, when a host is removed or 30 days after the event's last occurrence, when a teen turns 18, or when either account is disabled. Every step and refusal writes an `AuditLog` row, and admins see and export (CSV) a person's contact-access history.
- Add people by name (DEC-78): `GET /api/v1/people/search?q=` for leaders, hosts and admins, with at least 2 characters, at most 10 results and a per-user rate limit. A result has a display name, a Teen badge and an opaque id, never an email, phone, birth date or a teen's avatar. It is not a directory (10.2, M6.1): no browse, no empty query, no opt-out. Disabled, deleted, guest and `pending_consent` accounts never appear; contact-hidden adults and active teens do.
- Same-name results show the profile picture (adults only), then shared groups, then other groups the searcher can already see; an invite-only group the searcher can't see is never named. If nothing helps they say "More than one match? Ask an admin." No-results never says why someone is missing.
- Adults are added directly through the member add (limit and audit rows) or the DEC-75 one-date add. Anyone who can't be added directly gets an "Ask to add" request through the contact approval flow, and every teen add needs both an admin and the teen. Admins add adults through `POST /admin/groups/:id/members`. Contact, group-add and occurrence-add requests share machinery, never authority: an approved add never grants contact access. The picker is an accessible combobox.
- The narrow DEC-75 exact-date prerequisite also landed here: sanitized exact-date GET, own RSVP, decline and block, names-free recipient and guardian notices, exact-date calendar links and standalone personal ICS. It grants no group, roster, series or management access.

**M1.12 Frontend toolchain upgrades**
- Delivered: Vite 8 with `@vitejs/plugin-react` 6, React 19, Tailwind CSS 4 and Node 24 (`.nvmrc`, CI, Dockerfile, docs), with TypeScript 7 and Vitest 5 before them. Tailwind is configured CSS-first (`@theme` in `index.css`, `@tailwindcss/vite`, no `tailwind.config.js`). The image compressor still runs on the main thread. `hover:` applies only on devices that can hover. The browser floor is Safari 16.4, Chrome 111 and Firefox 128.

**M1.13 Per-date event visibility (DEC-46)**
- Delivered: a nullable `EventOccurrence.visibility_override` (`public`, `private` or `invite_only`). A date's effective visibility is the override, else the event's value (`policy.EffectiveVisibility`); `Customized()` counts the override, so series edits leave the date alone. `apiUpdateOccurrence` takes `visibility` (a value, or `null` or `"series"` to clear). Whoever may set the event's visibility may set a date's; church-wide dates allow `public` or `private` only.
- Every read path uses the effective value per date: event and occurrence pages, lists, calendar, ICS feeds, registration (`CanRegisterEvent`) and date discovery.
- A series edit changes `Event.visibility` and leaves dates with their own value; the dialog counts them and offers "Also reset those dates to the series setting" (each clear audited). Series edits have no "this and following" split in v1 (5.3).
- A per-date change more open than the group writes `event.visibility_override` with the occurrence's `original_date` and notifies the other leaders and admins once, naming the date; clearing or tightening is audited too. Making a date stricter cancels and notifies registrants who can no longer see it, after the leader confirms the count. When a group becomes more private, a date wider than the new value goes through the group visibility review like a `leader_opened` event.
- An opened date of an invite-only group's series shows "Hosted by <church name>" and never the group's name, link, location (the group's location is not a fallback), leaders or other dates. Outsiders see a count, and its ICS entry is a single event.
- Left for M3.6, M3.8 and M2.3: reminders and room suggestions apply the same per-date rule (7.7, 9.3).

**M1.14 Series RSVP and RSVP-aware calendar (DEC-71)**
- Delivered: the calendar and personal ICS show where the viewer stands on each date, series answers cover many dates at once, and leaving a group is a counted confirmation. Copy uses the DEC-63 words: "Going", "Maybe", "Can't go", "Waitlisted" and "Not answered". The stored status `declined` never appears in copy.
- Own scope (DEC-67, DEC-72 to DEC-75): dates of my groups, church-wide dates (everyone, with no synthetic group), events I host, and dates I answered. A public event of another group enters my scope only once I answer Maybe or Going (DEC-73). Joining a group adds its upcoming dates as Not answered. Unanswered public and church-wide dates show only within the next 30 days in Everything and the feed's `all` scope; church-wide ones also in Just mine; dates of my own groups and answered dates always show (DEC-77). Discovery of public events stays open (DEC-76).
- Calendar states in Just mine: Going has a quiet primary tint, a solid accent border and a check-mark label (DEC-71, amended by #228); Maybe, Not answered and Waitlisted are ghosted with a text label and full detail; Can't go is removed (in Everything it stays, labelled). Ghosted means a dashed border and lighter fill, never opacity on text, and the chip's accessible name carries the status. Phone month view uses hollow and filled dots. The event page's segmented control shows the viewer's answer; Waitlisted shows Going with a "Waitlisted" label. The calendar API returns slim rows with only the viewer's own answer.
- Personal ICS (8.2): Going is `CONFIRMED`, Maybe, Not answered and Waitlisted are `TENTATIVE`, and Can't go dates are omitted. Public and group feeds carry no RSVP status.
- Series answers: after Going, Maybe or Can't go on a series date, a dialog asks "Just this date" (default), "This and following dates" or "All future dates" (DEC-92 removed the separate whole-series buttons). Each date goes through the normal locked registration path, so capacity, waitlist and cutoffs apply per date; dates a leader removed the person from stay removed, and dates added later are not answered ("New dates added later won't be answered for you."). A `role=status` summary offers Undo for 10 seconds, restoring each date's previous answer through the same path. A bulk Can't go asks for confirmation.
- Departure (DEC-74, DEC-86): `GET` preview returns counts and keepable dates, and the confirming POST applies cleanup, waitlist promotion, exact-date access and audit rows under the occurrence locks. Leaving or removal ends registrations on upcoming dates except dates kept in the confirmation (underway or inside the cutoff, ticked by default; public dates the person chose, unticked); past attendance stays. A confirm whose facts changed since the preview answers 409 and changes nothing. A voluntary leave keeps a `left` membership that can rejoin; leader or admin removal keeps `removed`.
- A kept private or invite-only date records explicit exact-date access for that one occurrence, in the same provenance family as a DEC-75 invitation (`policy.CanSeeEvent` is not relaxed). It is sanitized and date-only: the person can open the date, change their answer, and see it in Just mine and the personal feed, with no group page, roster, series or management access. It ends when the date ends or is cancelled, when the person chooses Can't go or "Don't add me again", when a manager removes the registration, or when the account is disabled or deleted.
- Departure notices (stored rows, `notify.Send`): a removal sends the person one summarized, names-free notice (dates kept and ended, no group name they can't see, a names-free copy to a teen's confirmed guardians). A self-leave sends each group leader one notice. Never one notice per date.
- "Don't add me again" blocks new manager adds for its organizer scope only (`policy.ManagerAddBlocked`). It never blocks the person's own answers, joining a group, or a registration that already stands.
- API: the series-answer, Undo and departure-confirm endpoints are authenticated CSRF-protected POSTs under `/api/v1`. Their actionable errors have `api_errors.go` codes and `ERROR_COPY` lines (DEC-61). None answers 429. The "Show can't go" chip is optional.
- Left for M4.2: an admin setting `calendar_unanswered_window_days` (DEC-77); M1.14 ships the fixed 30 days.


## Phase 2: Rooms (PLAN 14.5)

Rooms are optional (9.1) and touch the database, API and UI together, so every milestone ships as thin slices in this order (14.1): the database change, then the API and internals, then the UI. A slice may land hidden behind `rooms_enabled` until its UI exists. M2.1 (slices a to e), M2.2 (the by-room view and the room feed) and M2.3 (room suggestions) are built, so Phase 2 is complete. In the release table rooms are v0.6, after email (v0.4) and the MVP (v0.5); they may run in parallel with Phase 3 because they share only events and occurrences.

Design defaults for this phase (decided here so slices don't reopen them; change them in review if wrong):

- **Where rooms show.** With rooms on, an event or date with a confirmed booking shows the room as its place ("Fellowship Hall · Main building"); a typed location then shows as a detail after it ("… · north door"), and the room name is the ICS `LOCATION` (8.2). Without a booking, the free-text location shows as today.
- **Room feed tokens** are their own table (`room_feed_tokens(room_id, token_hash, created_by, created_at, revoked_at)`), not a new kind on the per-user `calendar_tokens` (which is one row per user).
- **A `requested` booking holds nothing.** Only `confirmed` bookings are in the exclusion constraint. `rejected` is set only when an admin declines a requested booking. When a leader picks another room for that date, the new booking replaces the requested one.
- **Restricted rooms** (the nursery, say) appear in the by-room view and room feed lists for admins only; everyone else never sees them listed.
- **Phones.** The by-room view shows one room at a time as a day list with a room picker; the column grid starts at `sm` (DEC-67's pattern for Week).
- **Event type for suggestions (9.3 Rule 3)** is the group's tags (DEC-100): no event category field.

**M2.1 Rooms and bookings**

- **M2.1a Room catalog and the rooms switch**
  - Database: `rooms` (5.3 `Room` fields with the DEC-101 default padding; no color, DEC-107) and the `rooms_enabled` setting (default off).
  - API: admin create, edit and deactivate under `/api/v1/admin/rooms` (no delete: history stays), `policy.CanManageRooms`, audited; every room endpoint answers 404 while rooms are off; `GET /site` says whether rooms are on.
  - UI: a Rooms tab in Admin (list, add, edit, deactivate; restricted rooms labelled), and a Rooms on/off switch in admin Settings that saves at once (DEC-98).
  - Acceptance: with rooms off no room UI shows and every room endpoint is 404; only admins manage rooms; deactivating keeps history.
- **M2.1b One booking on one date**
  - Database: `room_bookings` with `padded_range`, the `CHECK` that it matches the times and padding, and the exclusion constraint on confirmed bookings (5.3, 9.2).
  - API: `GET /api/v1/rooms` lists active rooms to people who may book (`policy.CanBookRoom`), without notes for non-admins, restricted rooms for admins only. Book, change and release the room of one occurrence, with the padding copied from the room (DEC-101); `room_busy` (409) names the conflict window but never the other event; admins may double-book with a reason (`room_booking.overlap` audit row); restricted rooms are admin-only; a capacity warning (not a block) when the event's capacity or going count is above the room's. Cancelling a date releases its booking.
  - UI: a Room field beside Location (not under More options, DEC-95) in "Edit this date" and the shared `SeriesEditor`, with busy rooms marked; the date card and event page show the room as the place.
  - Moving a date (per-date reschedule, series edit) moves its booking in the same transaction; a move into a conflict makes it `requested` and sends one `room.lost` notice. This landed in M2.1b because a single date of a series can be booked here.
  - Acceptance: two concurrent bookings of overlapping padded ranges, exactly one wins (database and API); padding edges (touching ranges don't conflict); leaders can't book restricted rooms; the double-book reason is audited; a moved date keeps or loses its room as above.
- **M2.1c Series bookings and moving dates**
  - Database: nullable `events.default_room_id` referencing `rooms` (5.3).
  - API and internals: "Book for the whole series" books every generated date and stores `events.default_room_id`; dates the room is busy on are booked `requested` and listed, not refused (DEC-103). The daily extender books new dates as it generates them, and a new date that conflicts is `requested` with the same `room.lost` notice (moves already landed in M2.1b); series edits that add or restore dates do the same. Feeds put the room name in the ICS `LOCATION` (8.2).
  - UI: the event create forms get the Room field (with "whole series" for a series); the event page lists dates that lost their room with a "Pick a room" action.
  - Acceptance: a series of 10 books 10 dates; editing the series time moves every booking; a moved date that now conflicts is `requested` and named in one notice; the extender books new dates and handles conflicts the same way.
- **M2.1d Booking approval**
  - Database: `room_requests` (one per booking action, DEC-106) and `room_bookings.request_id`; the `room_bookings_need_approval` setting (DEC-102).
  - Setting switch in admin Settings; with it on, leader and host bookings wait as requested bookings on one request (a date, or every free date of a series) and every admin gets one task. Busy dates are lost as before (DEC-103), not requested. Admins' own bookings confirm at once.
  - API: admins list open requests ("Needs a room", approvals only, DEC-105), confirm one (dates that became busy need a double-book reason, or go back to the leader as lost) or reject it with an optional reason (the leaders are told; a series request stops being the series room). Re-picking, releasing or cancelling every date of a request withdraws it and closes its task. The daily extender adds new dates of a waiting series to its request.
  - UI: a "Needs a room" list on the admin Rooms tab; the room controls say a booking is waiting for approval; the event page's "Dates that need a room" also lists dates an admin didn't approve.
  - Acceptance: with approval on, a leader's booking is `requested` and holds nothing until confirmed; confirming a still-conflicting request needs a reason; rejecting notifies; re-picking replaces the requested booking.
- **M2.1e Matching old locations**
  - API: the 9.1 "match locations" helper lists distinct free-text locations with counts and lets an admin map one to a room, which books matching future dates where free and reports the rest. Running it again books nothing twice.
  - UI: the match-locations helper on the admin Rooms tab.
  - Acceptance: the helper never overwrites a leader's typed location and is idempotent.
- Tests across M2.1: conflict table tests (padding edges, moved and restored dates, extender), policy tables for `CanManageRooms` and `CanBookRoom`, migration drift, and a Playwright path for book, conflict and double-book.

**M2.2 By-room view and room feed**

- **M2.2a By-room view**
  - API: `GET /api/v1/calendar/rooms?view=day|week&date=` returns, per active room, the confirmed bookings in range, in church time. A booking the viewer can't see (policy date visibility) is `{startsAt, endsAt, reserved: true}` with no title, group, event id or link; restricted rooms show to admins only. Admins also see bookings waiting for approval, marked (DEC-109).
  - UI: `/calendar/rooms` (a Rooms view beside Agenda, Week and Month when rooms are on): Day is one column per room with time rows from `sm` up, Week is a rooms × days grid (DEC-108), and phones show one room at a time as a day list; "Reserved" blocks are grey with the word, and waiting ones are dashed with "Waiting for approval".
  - Acceptance: members, leaders and admins each see exactly the details section 6 allows; JSON for a hidden booking has no identifying keys (a key-list test); teens get 404 and signed-out visitors 401.
- **M2.2b Room ICS feed**
  - Database: `room_feed_tokens` (5.3).
  - API: admins make, replace or turn off one link per room (`room_feed_tokens`; like the personal feed, the link shows once, only its hash is stored, and a new link turns the old one off); `/cal/rooms/<roomId>/<token>.ics` exports confirmed bookings (never waiting ones, DEC-109), titling a date "Reserved" when its own visibility is invite-only or its group was deleted; the access log keeps the room id and never the token.
  - UI: a "Calendar feed" action on each active room in the admin Rooms tab (create, copy once, make a new link, turn off), behaving like the personal calendar feed card.
  - Acceptance: a revoked token answers 404; the feed shows what an admin would see, except that invite-only and hidden-date bookings are titled "Reserved" with no group or description (8.2).
- Tests across M2.2: the key-list masking tests, and a Playwright smoke test of the by-room view.

**M2.3 Room suggestions**

- **M2.3a Suggestion engine (internal)**
  - `rooms.CountingUsagePredicate(E)` and the query from it (`internal/rooms`), using M1.13 effective per-date visibility (never the group's or event's stored value alone). All five rules of 9.3, with the event's type taken from its group's tags (DEC-100). Built; migration 00050 adds a GiST index on confirmed bookings' `(room_id, padded_range)` for the busy check, since the exclusion constraint's index leaves out double-bookings.
  - Tests: all 13 cases in the 9.3 table, plus explicitly opened and tightened dates and a church-wide event (no tags, so no type filter); a benchmark with 50 rooms and 20,000 bookings under 100 ms on Postgres.
- **M2.3b Suggestions in the picker**
  - API: each room picker's own response (one date, the series, and the create forms' `POST /rooms/check`, which also takes the chosen visibility) carries up to 5 `suggestions` with counts and a reason ("Used by this group 6 times"), and says when they are "Used by other kinds of events" (DEC-110). The JSON never carries other events' titles, groups or dates (a key-list test).
  - UI: all three Room fields (Edit this date, Edit series, the create forms) show "Suggested" first, each with its reason and the picker's own busy note ("in use on 2 of 10 dates", across every date being booked), then the other rooms. Built.
  - Acceptance: suggestions are a shortcut, never a gate: every bookable room stays pickable by hand.

