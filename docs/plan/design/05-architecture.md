# Fold design: 5. Architecture

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
   |  HTTP: /api/v1/*   JSON API           /cal/*  ICS feeds        |
   |        /hooks/*    provider webhooks  /u/*    one-click unsub  |
   |        /uploads/*  avatars, covers    /setup  first admin page |
   |        /healthz    health probe       /*      embedded SPA     |
   |                                                                |
   |  Core:   policy | events + occurrences | rooms | messaging     |
   |          consent | notifications | account erasure             |
   |  Jobs:   in-process worker over the `jobs` table               |
   |  Ports:  EmailSender | storage.Store | web push (no vendor)    |
   +------+--------------------------+------------------------------+
          |                          |
   +------+------+          +--------+---------+     Outbound HTTPS to:
   | Postgres 16 |          | uploads volume   |     - email provider (required)
   | (pgdata)    |          | (/data)          |     - browsers' push services
   +-------------+          +------------------+
```

The binary also serves `/help/members`, `/manifest.webmanifest`, and, in dev only, `/dev/outbox`. Routes are registered in `main.go` and `api.go`.

Not in the box yet:

- **Planned (M4.3):** generated `/legal/privacy` and `/legal/sms-terms` pages.
- **Planned (M4.2):** the SPA setup wizard. Today `/setup` is a server-rendered page that creates the first admin.
- **Planned (M3.7):** an SMS provider port (Telnyx) with its inbound and status webhooks under `/hooks/*`.
- **Planned (M4.4):** a `DNSProvider` port (manual, Cloudflare) for email domain records.
- **Planned (M5.2):** a `backup` container (`fold-backup`: nightly `pg_dump` and a tar of uploads into a `backups` volume). Today the DigitalOcean scripts run `deploy/digitalocean/backup.sh` from the host (11.8).
- Cut until a church asks (DEC-140): an S3-compatible store (5.7).

Browsers: Tailwind 4 sets the floor at Safari 16.4, Chrome 111 and Firefox 128, and `hover:`
styles apply only on devices that can hover.

Needed outside the box: one email provider and a host (a VPS or a church-owned server). SMS will be needed only for the SMS features (Phase 3); the app runs without it. Uploads live on local disk, and video is link-out (5.8).

### 5.2 Code layout (target)

`package main` at the root holds the HTTP wiring and the domain code that needs the `App`. Logic that can stand alone lives in packages under `internal/`, each testable on its own. `AGENTS.md` names the one helper for each rule.

```
/                      package main: main.go, cli.go, config.go, api.go (routes), api_*.go (handlers),
                       and domain files grouped by prefix: registration_*, messages_*, reminders*,
                       notices_*, notify_deliver, push_*, account_erasure*, contact_*, request_*,
                       email_*, session_state, throttle*, settings, zones, jobs_daily, job_identity
/models                GORM models; they must match the migrations (drift check in /migrations)
/migrations            goose SQL files, embedded; the schema's source of truth
/storage               Store interface and LocalStore
/templates             server-rendered pages (the setup page)
/internal/policy       every permission rule as a pure function, with table tests
/internal/events       the clock: rule expansion, series edits, capacity cutoff, places, ICS rendering
/internal/eventdates   writes occurrence rows (Ensure at creation, Extend daily)
/internal/rooms        busy checks (HeldCondition, PaddedRange, BusyCounts) and room suggestions
/internal/jobs         the jobs table runner: enqueue, claim, backoff, dead jobs
/internal/notify       stored notifications, the kind registry, the notify.deliver job
/internal/mail         email adapters (devoutbox, smtp, postmark), webhook parsing, suppressions
/internal/providers    adapter interfaces and the registry
/internal/push         web push (RFC 8030, 8291, 8292) with the standard library
/internal/content      composer HTML cleaning and the email's plain text
/internal/accounts     the age gate shared by every door that creates an account
/internal/audit        AuditLog writer
/internal/images       upload validation and re-encoding
/internal/httpx        security headers, body cap, redacting access log, health, manifest, SPA server
/internal/grouptags    the one rule for comparing group tags
/internal/dbopen       opens Postgres and runs the migrations
/internal/userlock, /internal/txguard, /internal/reasonscan    small locking and erasure helpers
/internal/testdb, /internal/guardkit                           test databases and the source-guard toolkit
/web                   React SPA (src/api, components, lib, pages, sw.ts); Vitest; Playwright in web/e2e
/deploy                docker-compose.dev.yml (Postgres for dev and tests), CI and drill compose files,
                       digitalocean/ (provision, upgrade, backup, restore and smoke scripts)
/scripts               shell tooling for CI and tests
/docs                  API.md, DEPLOY-DIGITALOCEAN.md, FIRST-CHURCH-CHECKS.md, MEMBERS.md, agents/, plan/
docker-compose.yml, docker-compose.build.yml, Dockerfile, Caddyfile, .env.example
Makefile               dev, devdb, test, e2e, lint (lint-go, lint-web, lint-sh), deadcode, compose-smoke
```

Messaging and consent have no package of their own; their code is in `package main`.

Still to come:

- **Planned (M4.1):** `/internal/secrets`, AES-GCM encryption of provider settings.
- **Planned (M4.2, M4.8):** `/internal/setup`, setup state and wizard steps shared by the SPA wizard and `fold setup`.
- **Planned (M3.7):** the Telnyx SMS adapter. **Planned (M4.4):** the manual and Cloudflare DNS providers.
- **Planned (M5.3, M5.4):** `docs/UPGRADING.md` and `docs/DEPLOY.md`.

### 5.3 Data model

The schema is `migrations/00001_baseline.sql` (the squashed baseline, M7.7, DEC-138) plus the numbered files after it; `models/models.go` mirrors it. This section says what each table is for and the rules that matter. Read the schema for every column, constraint and index. Unless noted, a table has `id`, `created_at` and `updated_at`. Times are UTC `timestamptz`; wall-clock fields say so. Enum values are `CHECK` constraints, and `internal/policy` owns the values the code uses. There was no conversion from the original skeleton's tables (DEC-22).

**Site and settings**

- `settings(key, value)`: site configuration. Keys are constants in `settings.go`: `site_name` (the church name), `timezone` (IANA), `church_address` (email footers, DEC-116), `self_signup` (`off|request`, default `request`; there is no open mode, DEC-4), `rooms_enabled`, `room_bookings_need_approval` (default off, DEC-102), `guardian_copies` (default off; email copies only, DEC-19, DEC-29), `reply_to_address` (DEC-130), `jitsi_domain`, `push_enabled` (default on, DEC-124), and `vapid_public_key` and `vapid_private_key_enc` (made on first boot, DEC-125).
  - **Planned (M3.8):** `sms_send_window_start` and `sms_send_window_end` (defaults `08:00` and `20:00`, read in each recipient's zone; DEC-9, DEC-25).
  - **Planned (M4.6):** `sms_registration_path` (`ein_10dlc|ein_toll_free|sole_prop_10dlc|sole_prop_toll_free`, DEC-3).
- **Planned (M4.2):** `setup_state(step, status pending|done|skipped, data_json)`, one row per wizard step. The first-run gate and the SPA wizard will read it on each request. Today the gate checks whether any user exists and caches the answer until restart.
- **Planned (M4.1):** `provider_configs(kind, provider, settings_json, secrets_enc, status, verified_at, last_error)`, with `kind` `email|sms|storage|video|dns|backup_offsite`. One active row per kind; email may add a `fallback` row. Today adapters are chosen by environment variables (5.6).

**People and accounts** (rules in 5.10)

- `users`: one account per email. `email` is `citext`, unique, and the login; there is no `username` (DEC-23).
  - `password_hash` is null until the person sets one. Guests never have one.
  - `name`, `display_name` (render with `User.PublicName()`), `phone`, `avatar_key` and its focal point.
  - `role` is `admin|member`. `status` is `active|invited|guest|pending_consent|disabled`; `pending_consent` is a teen waiting for consent (DEC-8). `administratively_disabled` records an admin's suspension apart from consent.
  - `account_type` is `adult|teen`. Nobody under 13 gets a row (DEC-8). `birth_date` is required for teens, so Fold knows when they turn 18; adults only confirm they are 18 or older.
  - `timezone` is the person's IANA zone; empty means the church's zone (DEC-58, 5.9).
  - `session_version` ends other sessions when the password, email, role or status changes. `logout_version` ends every session at logout (10.1). Compare them only through `sessionCurrent`.
  - `hide_contact_from_leaders` is an adult's choice (DEC-31). Teens are always hidden from leaders and hosts (5.11).
  - `deleted_at` marks an erased account's tombstone (`docs/agents/erasure.md`).
  - Who may sign in is `User.CanSignIn()`: an active account with a password. There is no `login_enabled` column.
  - **Planned:** `phone_verified_at` (M3.7), `totp_secret_enc` and `totp_enabled_at` (M4.9).
- `account_tokens(kind, user_id, token_hash, payload, expires_at, used_at)`: single-use emailed links, stored as SHA-256 only. Kinds: `password_reset`, `email_verify` (its payload holds the new email; a pending email change lives here, not on `users`), `email_change_cancel`, `account_setup` and `teen_consent`.
- `account_requests`: a request for an account. `source` is `self_signup|member_invite|guest_upgrade|household_conversion`. `status` is `pending|approved|rejected|expired|withdrawn`. Only admins decide, and requests expire after 30 days (DEC-4). `flags` holds abuse warnings (duplicate, honeypot, rate-limited) that never reach the requester (DEC-44). A conversion request carries the password set at acceptance and, once approved, the new user (DEC-82). The form never reveals whether an email is registered (10.1).
- `invitations`: an admin's or leader's invitation, with an optional group, the group `role` on acceptance (`member|leader`), `over_limit` (DEC-39), and `household_person_id` for a conversion. Accepting creates an active account with no approval, except that a teen starts `pending_consent` and a conversion waits for an admin (DEC-4, DEC-8, DEC-82). A leader may name only a group they lead. Invitation limits are in 10.1.
- `guest_signups`: a signed-out visitor's name, email and browser zone for one date, waiting for the emailed link. The guest's `users` row and registration exist only after the link is redeemed (C5).
- `login_throttles(key, failures, window_start, locked_until)`: per-key rate-limit counters, with the sign-in lockout. Keyed by `key`, no id. `throttle.go` owns every rate limit (a few count audit or signup rows instead); keys come from `throttle_keys.go`.
- `households(name, address, created_by)`.
- `household_accounts(household_id, user_id, relation guardian|adult|teen)`: household members who have their own account. An account belongs to at most one household.
- `household_people(household_id, first_name, last_name, kind, birth_date, care_notes, photo_consent, converted_user_id)`: people with **no login, email or phone** (DEC-8, DEC-23). `kind` is `child` (under 13; children never have accounts), `teen` (13 to 17, registered by a guardian) or `adult` (shares the holder's email, such as a spouse). `care_notes` (allergies, medical, custody) are shown only as section 6 allows. Turning a person into an account is in 5.10 (DEC-82, DEC-83).
- `guardian_links(guardian_user_id, teen_user_id | person_id, status pending|confirmed|rejected|revoked, confirmed_by, check_note, decided_at)`: exactly one of `teen_user_id` and `person_id` is set. Every link to a teen or child starts `pending`, and only an admin confirms it, writing how they checked in `check_note` (for example "known family; parent came to the office"). There is no auto-confirm path (DEC-32, DEC-79). Until a link is confirmed, the adult can't grant consent, register the child or teen, or get copies, and leaders see nothing about the person. Adult household people need no link.
- `teen_consents(teen_user_id, scope account|sms|photos, status granted|revoked, granted_by, granter_role guardian|admin, evidence_text, at)`: append-only (a trigger refuses changes); the latest row per teen and scope wins. `account` consent turns a `pending_consent` teen `active`. An admin's grant must have `evidence_text`, for example "paper consent form on file, signed 2026-10-12". The daily age job turns teens into adults at 18 and keeps the history.

**Groups**

- `groups`: `visibility` is `public|private|invite_only`. `status` is `pending|active|archived`; a new group waits for staff approval. `member_limit` is a nullable positive int (null is unlimited; set by the group's leaders or an admin; DEC-39, 5.4). `tags` is comma-joined free text: the Home chips filter by it, and room suggestions use it as the event's type (DEC-100; compare with `internal/grouptags`). `leader_id` is the displayed primary contact only, never used for permissions (C12). `deletion_requested` and `deleted_at` handle deletion.
  - Joining: any member who can see a public or private group may join at once. Invite-only groups are joined only by invitation. There is no join-request step; leaders remove members who shouldn't be there (DEC-30). Groups have no teen restriction (DEC-20).
- `memberships(user_id, group_id, role leader|member, status active|left|removed, joined_at, tenure)`: one row per person and group, and the only source of leadership; a group may have several leaders.
  - `left` is a voluntary departure. The row and first join date stay, and the person may rejoin under the usual visibility and capacity rules (DEC-86). A trigger raises `tenure` each time the row becomes active again.
  - `removed` means a leader or admin removed the person (DEC-35). They can't rejoin on their own until a leader or admin adds them back; the removal is audited.
  - A disabled account keeps its `active` memberships, hidden and uncounted, with any leadership handed off (DEC-38). On re-enable, a membership in a group at its limit is dropped and the person is told to ask a leader (DEC-47). There is no `pending_return` status.
  - A teen can never be a leader (DEC-18).
- `manager_add_blocks(subject_user_id, organizer_type group|event, organizer_id)`: a person's "stop adding me" choice for one group's or event's organizers. It stops new date adds from them, never the person's own answers or memberships (DEC-75).

**Events**

- `events`: a series or a one-off.
  - `group_id` null is a church-wide event, created only by admins and run by its hosts (DEC-7).
  - The clock: `starts_local` (wall clock, `YYYY-MM-DDTHH:MM`), `duration_minutes`, `tzid` (defaults to the church's zone), `rrule` (empty for a one-off; at most once a day, DEC-89) and `rrule_until`.
  - `title`, `description` (plain text, at most 5000 characters; rich text belongs to the composer), `location`, `default_room_id` and `default_room_pending` (the series room, 9.2). There is no event category: room suggestions use the group's tags (DEC-100).
  - Seats: `capacity` (null is unlimited), `waitlist_enabled`, `guests_allowed` (0 to 5 unnamed extra people per registration), `remote_available`, `remote_capacity` (5.4).
  - Childcare: `childcare_available`, `childcare_capacity`, `childcare_ages`, `childcare_notes`.
  - Video: `video_provider` (`none|link|jitsi`), `video_url`, `video_reveal` (`always|registered_only`, default `registered_only`; 5.8).
  - Reminders: `reminder_email_minutes` (`0` is off; `120`, `1440` (the default), `2880` or `10080`) and `reminder_push` (default on; 7.7).
  - `deleted_at` soft-deletes the event. Events have no status column or draft state (a draft state is Later, with no milestone); one date is cancelled on its occurrence.
  - Events have no teen or youth-only flag: teens may register for any event they can see (DEC-20).
- Event visibility. `visibility` is the event's own value, copied from the group when the event is created (DEC-48). `leader_opened` is set when a leader or admin made the event wider than its group. There is no live link to the group: making the group more open never changes its events. Making the group more private tightens, in the same transaction, every event more open than the new value that isn't `leader_opened` (one audit row each), and cancels and notifies the registrants who can no longer see it. Each `leader_opened` event wider than the new value is shown to the leader before saving, to keep open (a logged override) or tighten (DEC-6, DEC-17, DEC-48). Helpers: `cancelLosers`, `visibility-impact` (AGENTS.md).
  - A leader of the event's group, or an admin, may set any of the three values on a group event. For example, an invite-only group's event can be made `private` (all signed-in members) or `public`.
  - A change that makes the event more open than its group writes the audit row `event.visibility_override` (old value, new value, group visibility, and an optional `visibilityReason`; a reason is never required, DEC-28) and notifies the group's other leaders and the admins. Any other change, such as going back to the group's value or to a stricter one, is audited as `event.visibility_changed` and clears `leader_opened`.
  - An event of an invite-only group that is `private` or `public` never names or links the group to people outside it. Its page shows "Hosted by <church name>" and names no leaders; people reach them with "Message the organizers" (7.8). The group's leaders and members and the admins see attendee names; everyone else sees a count (`policy.CanSeeAttendeeNames`, DEC-48). The group stays 404 to outsiders, and registering for the event adds nobody to the group.
  - Church-wide events may be only `public` or `private` (a `CHECK`), and never `leader_opened`. Their hosts and admins may switch between the two.
- `event_hosts(event_id, user_id, added_by, created_at)`: one row per host and event. Every church-wide event has at least one host. The admin who creates it is the default host and can swap in others. Hosts are often admins but can be any adult member, never a teen (DEC-7, DEC-18). Hosts get leader rights over that event only (section 6). Host rows count only on church-wide events; group events use the group's leaders.
- `event_occurrences`: one stored date, unique `(event_id, original_date)`, with `starts_at` and `ends_at` in UTC. Dates are generated 18 months ahead and extended daily (`internal/eventdates`). `status` is `scheduled|cancelled`. `cancel_source` says who cancelled a date: a series edit that dropped it (`series`; a later edit that includes the date again schedules it and tells the registrants) or a person (`manual`, DEC-91). Per-date overrides: `title_override`, `location_override`, `starts_at_override`, `duration_override`, `capacity_override`, `remote_capacity_override` and `visibility_override` (null follows the event; DEC-46, M1.13). `policy.EffectiveVisibility` decides what a date's visibility is.
- Series edits (v1 rule): an edit to the series applies to **all future occurrences**; a change to one date is a per-date override on that occurrence, its visibility included. There is no "this and following" split in v1. When a series edit changes times:
  - Future occurrences without overrides move to the new times; their registrations stay.
  - Occurrences whose date no longer exists in the new rule are cancelled, and their registrants are told.
  - Each moved occurrence's room booking moves with it (`starts_at`, `ends_at` and `padded_range` are recomputed in the same transaction). A booking that would now conflict becomes `requested` instead of failing the edit, and the leader or host is told which dates lost their room (9.2).
- `registrations`: one answer by one person to one date.
  - Exactly one of `user_id` and `household_person_id` is set. A household answer also sets `registered_by`, the account holder. Unique `(occurrence_id, user_id)` and, where set, `(occurrence_id, household_person_id)`.
  - `status` is `going|maybe|declined|waitlisted|cancelled|removed`. `removed` is a leader or host pruning someone (DEC-20, DEC-30), with `removed_by` and `removal_note`.
  - `attendance_mode` (`in_person|remote`), `guest_count`, and `childcare_count` with `childcare_ages_note` for unnamed children. `childcare_requested` is a named household child asking for care in person; named parties carry no unnamed counts. `waitlisted_at` and `promoted_at` (5.4).
  - `source` is `web|admin|guest_form`. `manage_token_hash` lets a guest cancel from an email link.
  - The `access_*` columns record one-date access a manager granted (`manager_add`) or a departure kept (`departure_self`, `departure_removal`) (DEC-74, DEC-75). A trigger keeps that history immutable and requires its audit rows before a grant is replaced or deleted; `registration_access.go` is the only writer.
  - Rows are written only by `saveRegistration` and `promoteRegistration` (AGENTS.md).
  - A disabled account's registrations stay, hidden and uncounted (DEC-38, DEC-47, 5.4).
- `series_undo_receipts(token_hash, actor_id, event_id, snapshot, expires_at, used_at)`: a short-lived receipt, bound to its actor, for undoing one series-wide answer: the dates it changed and their before and after states (M1.14).
- **Planned (M6.3):** `attendance(occurrence_id, user_id | household_person_id, present, checked_in_at, checked_in_by, method)` for a leader's roll; QR self check-in is M6.4.

**Rooms** (hidden while `rooms_enabled` is off)

- `rooms(name, building, capacity, features, notes, active, restricted, default_setup_minutes, default_teardown_minutes)`: the name is unique within its building, ignoring case. `restricted` rooms (for example the nursery) can be booked only by admins. Setup and teardown are 0 to 240 minutes. Each booking copies the room's defaults, and an admin may change them on one booking (DEC-101). Rooms have no color: the by-room view tells them apart by column and name (DEC-107).
- `room_bookings(occurrence_id, room_id, starts_at, ends_at, setup_minutes, teardown_minutes, padded_range, status, allow_overlap, overlap_reason, request_id)`: at most one per occurrence. `status` is `confirmed|requested|rejected`; only a confirmed booking holds the room.
  - The app writes `padded_range = [starts_at - setup, ends_at + teardown)` on every insert and update, and a `CHECK` keeps it equal to the four source columns. (The exclusion constraint can't compute it: a `timestamptz - interval` expression isn't immutable.)
  - Conflicts are refused by the exclusion constraint `EXCLUDE USING gist (room_id WITH =, padded_range WITH &&) WHERE (status = 'confirmed' AND NOT allow_overlap)`, using `btree_gist` (DEC-1). An admin double-booking sets `allow_overlap` and must give an `overlap_reason`.
  - "The room is busy" is decided only by `internal/rooms` (AGENTS.md).
- `room_requests(event_id, room_id, series, requested_by, status pending|confirmed|rejected|withdrawn, decided_by, decided_at, reject_reason)`: a leader's or host's booking waiting for an admin while `room_bookings_need_approval` is on. One request per booking action, for one date or a whole series, and at most one pending series request per event (DEC-102, DEC-106).
- `room_feed_tokens(room_id, token_hash, created_by, revoked_at)`: a room's secret ICS link. A room has at most one live link (M2.2b).

**Messaging and consent** (section 7)

- `messages`: an announcement or a reminder.
  - `channel` is `email|sms|both|push`; only email and push are sent today (SMS is M3.7). `status` is `draft|scheduled|sending|sent|cancelled|failed`. `category` is `announcement|reminder`.
  - `author_id` (null for reminders), `subject`, `body_html` (cleaned by `internal/content`), `body_text`, `sms_body`, `audience` (jsonb), `scheduled_at`, `sent_at`, `recipient_count`, `segments_estimate`, `error`.
  - `push_alert` (default on) sends a short companion push to people with push on (DEC-123).
  - Reminders set `occurrence_id` and a unique `reminder_key` (7.7).
  - **Planned (M3.9):** a `pending_approval` status, `approved_by` and `request_id` for member SMS requests. **Planned (M3.8):** `send_window_override` and `override_reason` for an admin's one-message override of the SMS window.
- `message_recipients(message_id, user_id, channel email|sms|push, address, status, reason, copy_of_user_id, provider_message_id, error, sent_at)`: unique `(message_id, user_id, channel, copy_of_user_id)`, nulls not distinct. `status` is `queued|suppressed|deferred|sent|delivered|bounced|complained|failed`. `copy_of_user_id` marks a guardian copy (DEC-19).
- `consents(user_id, channel email|sms|push, scope, status opted_in|opted_out, source, evidence_text, ip, user_agent, at)`: append-only; a trigger refuses changes, except that erasure may blank a deleted person's evidence, IP and user agent. The latest row per user, channel and scope wins.
  - `scope` is `all`, `group:<id>` or `category:admin|groups|events|messages`. `source` is `web_form|sms_keyword|sms_reply|admin_import|unsubscribe_link|admin`.
  - For teens, SMS also needs a `teen_consents` row with scope `sms` (DEC-8). Push needs no teen consent.
  - Turning on a push device writes `(push, all, opted_in)` whenever the latest `(push, all)` row isn't `opted_in`, with evidence such as "Turned on notifications for iPhone (Home Screen app)". `(push, all, opted_out)` overrides every per-category push row; per-category push switches use `category:<name>` like email (DEC-122).
- `email_address_optouts(address_hash, scope, opted_out_at)`: a person's optional-email opt-outs, kept by address hash when the account is erased, so the address isn't mailed again by mistake (10.1). Hashes come only from `mail.AddressHash`.
- `push_subscriptions(user_id, endpoint, p256dh, auth, expires_at, label, last_used_at, failed_at)`: one row per browser installation. `endpoint` is unique and at most 2048 bytes; it is a capability and is never logged or returned. `label` is at most 80 characters. `last_used_at` is written at most once a day. A device row is deleted with the user, at logout from that browser, on a 404 or 410 from the push service, on key rotation, 30 days after a failure, and by a trigger when the account's sessions are revoked or it stops being active (which queues `push.devices_off`). Details: `docs/agents/push.md`.
- `suppressions(channel email|sms, address, address_hash, reason hard_bounce|complaint|manual, provider, detail, cleared_at, cleared_by)`: addresses Fold must not mail, checked at send time apart from users. One open row per channel, address and reason; an admin may clear one. `address_hash` (the SHA-256 of the normalized address, migration 00002) is the expand half of moving suppressions to a hashed address (M7.4, DEC-138). The `stop` reason for SMS keywords arrives with SMS (M3.7).
- `mail_outboxes`: mail the `devoutbox` adapter stored, for dev and tests, shown at `/dev/outbox`.
- **Planned (M3.9):** `message_requests(group_id, requester_id, sms_body, proposed_at, status pending|approved|rejected|expired|withdrawn, decided_by, decided_at, decision_note, message_id)` (7.6).
- **Planned (M3.7):** `inbound_messages(channel, from, to, body, provider_id, received_at, matched_user_id, keyword, opt_out_match, conversation_id, forwarded_to)`. `opt_out_match` records the keyword or phrase that triggered an automatic opt-out (7.4).

**Contact privacy and requests (DEC-31, 5.11, 7.8)**

- `contact_requests`: one table and one lifecycle for three `purpose`s. `contact` asks to see a person's email or phone (`fields email|phone|both`). `group_add` and `occurrence_add` ask to add a person to a group or to one date; the `add_*` columns hold the options (DEC-75, DEC-78). What each purpose decides is `requestKind` in `request_kinds.go`.
  - `context_type` (`group|event`) and `context_id` name where the request comes from; an `occurrence_add` also names its `occurrence_id`.
  - `subject_decision` (`pending|approved|denied`) and `admin_decision` (`not_needed` unless `needs_admin`, which is true for teens) lead to `status` `pending|granted|denied|withdrawn|expired|ended`; `end_reason` says why a request ended. Requests expire after 14 days.
  - `CHECK`s and triggers keep a request's purpose, options and finished outcome immutable, and a granted add must point at its completed membership or registration (`completed_target_id`). Text rules: `docs/agents/erasure.md`.
- `contact_grants(request_id, leader_id, subject_user_id, context_type, context_id, fields, granted_at, ended_at, end_reason, ended_by)`: one per granted contact request (a trigger checks the request). `end_reason` is `revoked_by_subject|revoked_by_admin|revoked_by_guardian` (with `ended_by`), or an automatic end: `left_role|left_context|context_ended|turned_18|account_disabled|event_ended`. A grant needs both an unended row and current lifecycle authority; a null `ended_at` alone never grants access.
- **Planned (M3.11):** `conversations(context_type group|event, context_id, created_by, relay_token_hash, closed_at)`, `conversation_participants(conversation_id, user_id)` and `conversation_messages(conversation_id, author_id, channel in_app|email|sms, body_text, inbound_message_id, at)`.

**Notifications (5.12, DEC-42)**

- `notifications`: one stored notice per recipient, written by `notify.Send`. `kind` keys the Go registry. `category` is `admin|groups|events|messages|safety`.
  - `waiting` marks a task, set from the kind when the row is written, so the badge query needs no registry. `resolved_at`, `resolved_by_id` and `resolution` (approved, rejected, deleted, kept, withdrawn) give other admins "Done · approved by Pat".
  - `title`, `body`, `link` (an internal path: a `CHECK` requires a leading `/` and refuses `//`), `actions` (at most 2 buttons, only for allow-listed kinds), `data` (small and non-sensitive), and separate `email_title`, `email_body`, `push_title` and `push_body`. None of the text holds contact details of a teen or a hidden adult (DEC-31).
  - `dedupe_key` is unique per user, so writes are idempotent; `group_key` batches. `expires_at` moves a notice to Archived.
  - Indexes: the feed `(user_id, archived_at, updated_at DESC, id DESC)`, the badge (unread or unresolved, not archived), open batches `(user_id, group_key)`, and open tasks by `(kind, subject_id)`.
  - Rows cascade-delete with the user.
- `notification_deliveries(notification_id, user_id, channel email|sms|push, status, reason, scheduled_for, provider_message_id, sent_at)`: one row per notice and channel (unique). One push row covers every device. `status` uses the `message_recipients` values except `complained`.
- No `notification_settings` table in v1 (DEC-44). Email is "Right away" only, and the per-category Email and Text switches and "Turn off all optional email" are `consents` rows (`scope category:<name>`). Required categories can't be turned off.
- **Planned (M6.16):** digests add a per-user `notification_settings` table with `email_mode`, and deliveries gain a `digest_id` with a nullable `notification_id`.

**Calendar**

- `calendar_tokens(user_id, token_hash, scope mine|all)`: one personal feed link per user. Room links are `room_feed_tokens`.

**Platform**

- `jobs(kind, payload, run_at, status, attempts, max_attempts, locked_at, locked_by, last_error, unique_key)`: background work (5.5). `status` is `queued|running|done|failed|dead|dismissed`: `failed` waits for its retry, and `dismissed` is an admin's choice on the System page. `unique_key` is unique.
- `audit_logs(actor_id, action, target_type, target_id, meta_json, ip, at)`: written by `audit.Write` in the change's transaction. It covers admin actions, message sends, consent changes (teen consent included), account request decisions, guardian and household link requests and decisions (DEC-32), event visibility changes, registration removals, role and leadership changes, email changes, setting changes (`guardian_copies` included), double-bookings, and every contact-access event (DEC-31: each request, decision, withdrawal, expiry, grant, reveal, revocation, automatic end and refused attempt).
  - It will also cover SMS ownership checks and automatic opt-outs (M3.7), send-window overrides (M3.8), one-to-one message metadata (M3.11), provider config changes (M4.1) and admin 2FA changes (M4.9).
  - Audit rows are never deleted. Erasing an account blanks the personal details in them that the rules allow and keeps safeguarding evidence (`docs/agents/erasure.md`).
  - A durable, tamper-evident log was cut (M6.14, DEC-140); DEC-33 and 10.7 keep the reasoning.
- `admin_operation_attempts(attempt_id, created_at)`: receipts that let a retried admin deletion learn what the first try did. A daily job deletes them after 24 hours (`docs/agents/erasure.md`).
- **Cut until a church asks (M4.7, DEC-140):** `assets(key, visibility public|private, owner_type, owner_id, content_type, size, created_by)` for private files (C9).

**Later (section 13)**

`signup_sheets(event_id|group_id, title, kind volunteer|potluck|supplies)`, `signup_slots(sheet_id, label, quantity, starts_at, requires_check)` and `slot_claims(slot_id, user_id, note)` (M6.2); `prayer_requests(author_id, group_id, visibility, body, anonymous, status, expires_at)` (M6.5); posts and comments for discussions (M6.6); galleries and photos (M6.9); `background_checks(user_id, status cleared|pending|expired|not_cleared, cleared_on, expires_on, provider_name, reference, entered_by)` (M6.8).

### 5.4 Capacity and waitlist rules

- Capacity counts `going` registrations plus their `guest_count`, per occurrence and attendance pool. `capacity` limits in-person parties; `remote_capacity` limits remote parties when remote attendance is on (null is unlimited). A date may override either. Each named household person uses one seat; don't count them again as unnamed guests. `maybe` takes no seat. One waitlist remembers each party's mode; switching pools starts a fresh wait in the new pool. Seat decisions go through `registration_seats.go`.
- A new "going" on a full event becomes `waitlisted` with `waitlisted_at = now` if the waitlist is on. Otherwise it is refused with 409 `event_full`.
- Promotion runs in one transaction that locks the occurrence row (`SELECT ... FOR UPDATE`; lock order in `registration_locks.go`). It runs when a `going` registration is cancelled or declined, when capacity goes up, or when a leader, host or admin removes someone. It promotes the earliest waitlisted registrations that fit. A party with guests that doesn't fit is skipped, and the next one is tried (Meetup does this too).
- Childcare-aware promotion (DEC-80): each picked party must fit both its attendance pool and the remaining childcare. A request that doesn't fit is skipped and keeps its place, age note and queue time; earlier picks use up childcare in the same pass. Promoting a person by hand refuses with 409 `childcare_full` when care doesn't fit. Promotion retries when attendance or care frees up or a limit grows or clears; there is no separate childcare waitlist. A named in-person child asks for one care spot explicitly; unnamed requests stay separate.
- No automatic promotion within 2 hours of the start (`events.CutoffWindow`, a fixed rule, not a setting). The leader gets a `waitlist.cutoff` notice instead, and promotion is one click.
- A promoted person gets a transactional email with a one-click "I can't come" link that frees the seat again. **Planned (M3.12):** a text too, if they opted in.
- People see their waitlist position ("You are #3 on the waitlist").
- Disabled accounts (DEC-38): their registrations stay but never take a seat or count in waitlist math or attendee counts. On re-enable, each comes back as it was if it fits. Otherwise it is dropped and audited, and the person gets a notice to ask a leader or host (DEC-47). Household answers are restored together, date by date. A leader, host or admin can then add them back, over capacity only as an explicit choice.
- Group member limit (DEC-39). Groups have no waitlist and no registration capacity: an optional `groups.member_limit` caps active memberships (a class in a room). Only `active` memberships of accounts that aren't disabled or deleted count, leaders included; `left`, `removed`, invited and disabled accounts don't.
  - A self-join into a full group is refused with 409 `group_full` ("This group is full").
  - A leader or admin adds someone over the limit only by explicit choice (the API takes `overLimit: true`; the SPA confirms first), and that add is audited (`membership.added_over_limit`). Accepting a leader's or admin's invitation into a group follows the same rule as that leader's add.
  - Lowering the limit below the current count is allowed. Nobody is removed, and joins stay blocked until the count is below the limit. Leaders and admins see "count of limit".
  - Every join, add and restore locks the group row (`SELECT ... FOR UPDATE`) before counting (`changeRole`, `policy.GroupHasRoom`).
- There is no approval step for signing yourself up (DEC-30), but eligibility still checks consent, age, visibility and earlier removals. A manager's add of a teen needs the teen and an admin to approve (DEC-78). A leader or host can prune registrations. Removing a going party retries promotion when attendance and childcare have room, subject to the two-hour cutoff (DEC-84).
- Leaving or being removed from a group ends the person's registrations on its upcoming dates, after a preview (DEC-74). Membership is a standing invite to the group's events (DEC-72, DEC-73), and a person can answer for a whole series at once (DEC-71). Answering for a series asks which dates: "Just this date", "This and following dates" or "All future dates", and says "New dates added later won't be answered for you." A Can't go for many dates asks for confirmation first (`SeriesRsvpDialog.tsx`, `RsvpButtons.tsx`). 8.1 and 8.2 cover the calendar side.

### 5.5 Background jobs

- A `jobs` table and worker (`internal/jobs`, M3.1) run in the same binary and start with `serve`. `FOLD_WORKERS` sets the workers per process (default 2, 0 to 64; `0` turns them off, for debugging only). Background work never uses a goroutine ticker.
- Workers claim due jobs with `FOR UPDATE SKIP LOCKED`, so any number of processes can share the table. An idle worker polls every `FOLD_JOB_POLL` (default `1s`). There is no `LISTEN/NOTIFY`.
- A claimed job holds a 15-minute lease; a job whose process stopped mid-run is taken back when the lease runs out.
- A failed attempt retries with exponential backoff (30 seconds doubling, at most an hour, ±20% jitter). Each kind's `jobs.Type` sets its `max_attempts` (default 5); after that the job is `dead`. Only `jobs.Retry`, `jobs.Dismiss` and `jobs.LockDead` change a row's status.
- Admin → System lists dead jobs in plain words with Retry and Dismiss, audited (DEC-111). A job that dies writes one `system.job_dead` alert for the admins (M7.5). Not built: an admin notice for a failed announcement (deferred from M3.5).
- Scheduling:
  - Daily tasks are queued once per church day under `<kind>:<church date>`, at startup and at church-local midnight, so birthdays turn over at church midnight (DEC-83) and several processes run each task once.
  - Hourly tasks are queued under `<kind>:<UTC hour>` (`YYYY-MM-DDTHH`).
  - `occurrences.extend` also runs at every start.
  - Other jobs carry their own `unique_key`, such as `message.send:<id>:<unix time>` or `push:n:<notification>:d:<device>`, so queueing twice is harmless. Timed sends store `run_at`; nothing depends on cron.
- Job kinds. `App.allJobKinds()` (`jobs_daily.go`) is the one list; the worker, the schedulers and the System page all read it.
  - Daily: `occurrences.extend` (dates 18 months ahead), `contact.sweep` (expires contact requests and ends grants; role changes also end grants at once, 5.11), `cleanup.login_throttles`, `messages.reap` (settles an announcement that stopped with no job left, M3.5), `notifications.cleanup` (retention, DEC-45, and push devices that keep failing or have expired), `accounts.expire_requests`, `accounts.age_rollover` (teens become adults at 18; guardians of household children who turn 13 get a review notice), `erasure.tombstone_sweep`, `avatar.sweep` (leftover photo folders), `admin_attempts.sweep`, and `jobs.cleanup` (finished jobs are kept 30 days).
  - Hourly: `reminders.sweep` (queues reminders due soon, 7.7) and `notify.reconcile` (notices that still need sending).
  - Queued by the app: `message.send` and `message.deliver_batch` (announcements and reminders, 7.5), `reminder.send`, `notify.deliver` (a notice's email and push, 5.12), `push.deliver` (one per device; the payload holds device ids, never endpoints; a 404 or 410 deletes the device, DEC-125), `push.devices_off` (queued by a `users` trigger), `webhook.process` (one email delivery report), `avatar.cleanup`, `erasure.tombstone_page`, `erasure.tombstone`, and the direct account mail: `account.password_reset_mail`, `account.request_mail`, `account.exists_mail`, `invitation.mail`, `account.email_verify_mail`, `guardian.consent_mail` and `guest.verify_mail`.
  - **Planned:** `provider.poll_status` (email domain verification, M4.4, and SMS registration, M4.6) and `notification.digest` (M6.16).
- Backups are not an app job (11.8).

### 5.6 Provider adapters

Outside services sit behind small Go interfaces in `internal/providers`. Each adapter registers itself by name. Today an environment variable picks it; **planned (M4.1):** a `provider_configs` row, with the environment still able to override it.

Built: email.

```go
// internal/providers
type EmailSender interface {
    SendEmail(ctx context.Context, m EmailMessage) (providerID string, err error)
    Capabilities() EmailCaps
}
```

The email adapters (`devoutbox`, `smtp`, `postmark`) live in `internal/mail` and register with `internal/providers` (M3.2); callers send through `mail.Sender` and never see which one runs. `FOLD_EMAIL_PROVIDER` picks one; otherwise dev uses devoutbox, then Postmark when it has a token, then SMTP (`mail.Choose`). An announcement batch calls `SendEmail` once per recipient. Delivery reports are normalized to `providers.Event` (delivered, bounced, complained): `mail.ParsePostmarkWebhook` reads Postmark's, behind `/hooks/postmark/email` and HTTP Basic credentials (M3.2b).

Planned interfaces, each with its milestone:

```go
// Email domain setup (M4.4)
type EmailProvisioner interface {
    VerifyCredentials(ctx context.Context) error
    EnsureSendingDomain(ctx context.Context, domain string) ([]DNSRecord, error) // DKIM, SPF, return-path
    DomainStatus(ctx context.Context, domain string) (DomainStatus, error)
    EnsureWebhook(ctx context.Context, url, secret string, events []string) error
    EnsureStreams(ctx context.Context) error // transactional vs broadcast, where the provider has them
}

// SMS (sending M3.7; provisioning M4.6)
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

// Webhooks (SMS with M3.7, inbound mail with M3.11): every adapter verifies
// signatures and normalizes events.
type WebhookParser interface {
    Parse(r *http.Request) ([]ProviderEvent, error) // delivered, bounced, complained, inbound, opt_out ...
}

// DNS (M4.4)
type DNSProvider interface {
    UpsertRecords(ctx context.Context, zone string, rs []DNSRecord) error
}
```

Adapters, one real adapter per kind until a church asks for a second (DEC-140):

- Email (built): `devoutbox` (dev and tests; stores mail in the database and shows it at `/dev/outbox`), `smtp` (universal fallback, no webhooks), `postmark` (default, DEC-21). When a church asks: `resend`, `mailgun`. No AWS-based adapter is planned (DEC-21).
- SMS: **Planned (M3.7):** `telnyx`. `twilio` (M3.10, deferred) and `plivo` wait until a church asks (DEC-140).
- DNS: **Planned (M4.4):** `manual` (shows records to copy) and `cloudflare`.
- Video: no adapter. A pasted link, or a Jitsi room URL made locally (`api_video.go`, 5.8). A `zoom` adapter that creates meetings (Server-to-Server OAuth) waits until a church asks (DEC-140).
- Storage: `local` (`storage.LocalStore`). An S3-compatible adapter is cut until a church asks (M4.7, DEC-140; 5.7).

Web push is not an adapter: there is no vendor. `internal/push` speaks RFC 8030, 8291 and 8292 to the browsers' own push services with the standard library (DEC-125).

Rules for adapters:

- Use plain `net/http` and the provider's REST API, or the provider's official Go SDK when it is small. No CGO.
- Every adapter has a contract test against a fake built from the provider's documented requests and responses (`TestEmailAdapterContract` uses an `httptest.Server` for Postmark and a test SMTP server). No live calls in CI.
- Not built, with no milestone yet: a `make live-test PROVIDER=postmark` target that uses real sandbox credentials from the environment, for humans only.

### 5.7 Storage abstraction

Built: avatars and group covers go through `storage.Store` (`storage/storage.go`; `Put`, `URL`, `Delete`, `CleanOwner`, `ScanOwners`) with `LocalStore` under `UPLOAD_DIR`, served at `/uploads/<key>`. The upload rules (JPEG, PNG or GIF only, content-sniffed, re-encoded, at most 5 MB, server-made keys) are in AGENTS.md. The browser shrinks an image before upload on the main thread (`web/src/lib/image.ts`, `useWebWorker: false`), because the compressor's worker would load code from a CDN the CSP blocks.

**Cut until a church asks (M4.7, DEC-140).** Kept as the record of the design; private files, when needed, land on local disk behind `storage.Store` first:

- Extend `storage.Store` with a visibility argument on `Put` and a `SignedURL(key, ttl)` method for private objects. `LocalStore` serves private keys at `/files/<key>` after a policy check, with `Cache-Control: private`. Public keys stay at `/uploads/<key>`.
- `S3Store` uses one S3-compatible client (`minio-go` v7, Apache-2.0, pure Go) that covers B2, R2, Wasabi, DigitalOcean Spaces, Vultr and MinIO. "S3-compatible" is only the protocol; none of these is Amazon. Config: endpoint, region, bucket, access key, secret, `public_base_url` (optional CDN or custom domain), path-style flag. `serveUpload` redirects to the store's URL.
- `fold storage migrate --to s3` copies all blobs and flips the config; `--to local` reverses it. Both can resume and are idempotent.

### 5.8 Video facilitation (F11)

- Video is link-out. A leader pastes a Zoom, Meet, Teams or other URL, or clicks "Generate Jitsi link", which makes `https://<jitsi_domain>/<church-slug>-<12 random characters>` with no API call (`jitsi_domain` defaults to `meet.jit.si`).
- `video_reveal=registered_only` (the default) shows the link only to people registered `going`, plus the event's leaders, hosts and admins. For public events this keeps uninvited people out of the call. `always` shows it to anyone who can see the date.
- Where the link appears: the event and date pages, the "you're in" promotion email, and ICS feeds. Reminder emails link to the date's page instead. The personal ICS feed also shows a `registered_only` link for dates the owner said Maybe to, and for unanswered dates of a group they belong to or an event they host (DEC-70). Waitlisted, declined, cancelled and removed people, and unanswered outsiders, never get it. Public and group feeds include only `always` links. Feeds export each stored occurrence on its own, so one qualifying date never reveals another date's link. The rules are `policy.CanSeeVideoLink`, `CanSeeOccurrenceVideo` and `CanSeeVideoLinkInFeed`, applied in `date_projector.go`.
- Embedding video inside Fold is out of scope (DEC-16). Revisit in a later revision of this plan if churches ask for it. Embedded meet.jit.si calls are reportedly limited to 5 minutes, and JaaS and self-hosted Jitsi cost money or a second server (12.4).
- Only when a church asks (DEC-140): a Zoom adapter that creates meetings by API. A Google Meet adapter would need Google Workspace and is not verified.

### 5.9 Time zones

- One church time zone (the `timezone` setting). An event can override it with `tzid`, which is rarely needed. The helpers that read zones are in `zones.go` (AGENTS.md); everything parses with `events.LoadZone`.
- Occurrence expansion uses `rrule-go` with `DTSTART;TZID=<tz>:<local time>` (`internal/events`). Each occurrence stores `starts_at` and `ends_at` in UTC. Rules repeat at most once a day, with a cap on dates (DEC-89).
- The UI shows every event time in the viewer's browser zone, converted from the UTC instant, with no zone label (DEC-57). The event page adds one quiet line when the event's clock differs: "Held in Pacific time (6:00 PM PDT)". Forms that edit an event's clock edit it in the event's own zone and name that zone. One formatter, `web/src/lib/when.ts`, does all of this.
- ICS output uses `TZID` parameters with IANA names plus a `VTIMEZONE` block per zone.
- Each person has a profile time zone (`users.timezone`, DEC-58). The SPA fills it in from the browser the first time it is empty, and the profile page changes it. Fold uses it for that person's emails and server-written notification text, and will use it for the SMS send window (DEC-25, M3.8). A guest's zone comes from the guest form's browser (`guest_signups.timezone`). Anyone without one gets the church time zone.

### 5.10 Accounts, ages, and households (DEC-4, DEC-8, DEC-18 to DEC-20, DEC-23)

**Signing in.** People sign in with their email and password (DEC-23). The email is unique: one email is one account. Sign-in is throttled by email and by client IP (10.1). Password reset and invitations go to that email.

How a person gets in:

| Door | Who starts it | Result | Admin approval? |
|---|---|---|---|
| Self-signup | The person, on `/join`, while the `self_signup` setting is `request` (the default; `off` closes the door) | `AccountRequest` (source `self_signup`) | Yes |
| Invite from an admin | Admin, into any active group or none | `Invitation`, then an active account | No |
| Invite from a leader | Leader of an approved group, into a group they lead or no group (DEC-88) | `Invitation`, then an active account | No |
| "Invite a friend" | Any signed-in adult | `AccountRequest` (source `member_invite`, `requested_by` = the inviter). The friend is emailed only after approval | Yes |
| Guest upgrade | A guest ticks "create an account" on the guest form | `AccountRequest` (source `guest_upgrade`), opened when the guest confirms their email; the guest row is upgraded on approval | Yes |
| Household conversion | An adult in the household, for a household person (Conversion, below) | `AccountRequest` (source `household_conversion`) | Yes |

An invited person who answers "13 to 17" starts `pending_consent` like any teen account.

The age gate is on every account door and on the guest form for public events. A guest must tick "I am 18 or older"; the form explains that a parent or guardian can register a younger person from their own account, as a household person. On account doors, the person picks "18 or older", "13 to 17", or "Under 13":

- **Under 13**: no account. The form explains that a parent or guardian can add the child to their household, and nothing typed is submitted.
- **13 to 17**: a teen account. It needs a birth date and its own email, and starts `pending_consent`. It cannot sign in until `account` consent is granted, but can browse public groups and events signed out (DEC-85).
  - Consent comes from admins and from confirmed guardians. A guardian link counts only once an admin confirms it (DEC-32). The leader role never grants consent; a leader who is also the teen's confirmed guardian grants it as the guardian.
  - A guardian can consent from an emailed link. An admin grants with evidence text.
  - A teen who signs up may name a guardian's email. That is contact information only: the link still waits for an admin.
  - A teen's self-signup also needs normal admin approval. An admin can approve and grant consent in one step.
  - Revoking `account` consent disables the account.
- **18 or older**: an adult account.

**What teens can do** (DEC-18, DEC-20):

- See what members see and register for **any event they can see**. There is no youth-only restriction. Leaders and hosts decide who attends: they watch the attendee list and remove anyone who shouldn't be there (5.4).
- Join groups like members do.
- Leaders and hosts never see a teen's email or phone. They reach teens through Fold (5.11, DEC-31).
- Teen accounts and teen or child household people are **badged** "Teen" or "Child" wherever leaders, hosts, or admins see people: attendee lists, group rosters, and message recipient lists and counts ("6 of them are teens"). Other viewers see minors on rosters and attendee lists only as a count.
- Teens cannot be group leaders, event hosts or admins, cannot create groups, and have no public avatar. **Planned (M6.1):** they never appear in the church directory (10.2).
- Email reaches a teen once `account` consent is granted. Push needs only an active account (DEC-122); a push about a household child carries only the safe text. **Planned (M3.7):** SMS reaches a teen only with both `TeenConsent` scope `sms` and their own SMS opt-in.
- **Guardian copies** (DEC-19): off by default. An admin can turn on the church setting `guardian_copies`; the change is audited. Then each confirmed guardian gets an email copy of every announcement sent to their teen, marked "Copy: sent to <teen> because your church sends guardian copies." Copies go by email only, never by SMS, because texts cost money (DEC-29). Reminders and optional notice emails are not copied yet, although DEC-19 covers every non-transactional email (#364). **Planned:** copies of one-to-one messages (M3.11), and the privacy page saying which way the setting is (M4.3).

**Household people** (no login): children under 13, teens without their own email, and adults who share an email. A household is created by the first write (`PUT /me/household` or adding a person); a GET never creates one. Any adult account in the household manages it. A registration's `registered_by` stays the account that made it, not the last editor, and notices about named registrations go to that holder. Children and teens need an admin-confirmed guardian link (DEC-32, DEC-79); adults need none.

**Age and care rules** (DEC-81, DEC-83). Every household registration needs a birth date, adults included. A `teen` under 13, or a `child` or `teen` aged 18 or older, can't be registered. A `child` aged 13 to 17 stays registrable until the household edits the kind. Ages use the church-local date. Event leaders and hosts see a child's care notes only while the child is `going` on a scheduled date, from 24 hours before it starts to 24 hours after it ends. A household adult whose access is pending can write care notes but not read them back.

**Birthdays** (DEC-83). The daily `accounts.age_rollover` job (at startup and church-local midnight) applies the age bands. At 13, a household child's confirmed guardians get one review notice; the kind and sign-ups stay as they are. At 18, a teen account becomes an adult account and is told, and its direct-contact grants end (5.11). docs/API.md ("Church-local age rollover") has the details.

**Conversion** (DEC-82, DEC-83). An adult in the household, or an admin, invites a household adult, or a teen aged 13 to 17 with a confirmed household guardian link, to use their own email.

- Accepting the invitation verifies the email and holds a password on a pending `household_conversion` account request. It creates no account and moves nothing.
- Approving an adult creates the account and moves every registration in one transaction.
- Approving a teen creates a `pending_consent` account. The household keeps managing sign-ups until account consent and a newly confirmed guardian link, from the teen account to an original household guardian, are both in place.
- An independent admin suspension delays the move. An inactive original registration holder defers the person's whole rollover.
- Memberships never move. The move keeps registration IDs, answers and waitlist places, and does not recheck event access or capacity.

docs/API.md ("Household") has the staging rules.

**Shared emails** (DEC-23). Some members, often older couples, share one email address. Fold allows one account per email, so:

- The shared email belongs to one account (one person's name, one password).
- The other person is added to that account's household as an `adult` household person.
- When RSVPing, the account holder ticks who is coming ("Me", "Mary"). Each named person is a registration and counts toward capacity. Attendee lists show "Mary Smith (via John Smith)".
- Messages and reminders go to the account holder only.
- Anyone who wants their own reminders, wants to lead a group, or wants to be listed separately needs their own email address. A free webmail address is fine. The account holder, or an admin, can then turn the household person into their own account (Conversion, above); their registrations move with them.
- Unnamed extra people ("+2") still use `guest_count` for events that allow guests.

`docs/MEMBERS.md` (M1.9) is the help churches share with members: sharing an email, households, own accounts, and teen consent.

**Planned (M4.5, M5.4):** guidance for church staff in `docs/DEPLOY.md` and the admin help page: import or invite one person per email; when a CSV has two people with one email, the import makes the first an account and offers to add the others as adult household people (M4.5); never invent placeholder emails.

### 5.11 Contact privacy: Fold as the contact proxy (DEC-31)

Contact privacy, direct-contact requests and audited reveals are built (M1.11). Leaders, co-leaders and hosts reach teens through Fold unless a grant allows direct contact. Adults choose their own preference.

- **Teens**: a leader or host never sees a teen's email address or phone number. They reach the teen with Fold email (announcements, 7.2). **Planned:** Fold SMS with the teen's consents (M3.7, 7.4) and in-app one-to-one messages (M3.11, 7.8). Admins, the teen's confirmed guardians, and the teen still see the details.
- **Adults**: the profile setting "Hide my contact info from group leaders" (`hide_contact_from_leaders`) is **off by default**, so leaders and hosts see the email and phone of adult members of their groups and registrants of their events (10.2). When it's on, they reach that adult only through Fold. Teens are always hidden and can't change the setting.
- **Asking for direct contact**: on a hidden person's card, a leader or host chooses "Request direct contact", picks email, phone, or both, and may add a reason. The requester must be an active adult who leads a group the person is in, or runs an event the person is registered for (a leader for their group's events, a host for their church-wide event).
  - Hidden adult: the adult alone approves or declines in the app. Email links only open Fold; opening one never approves or reveals anything.
  - Teen: an admin **and** the teen must both approve (DEC-31). Either one declining ends the request. Confirmed guardians get a notice when a grant is made and see active grants on the teen's page.
  - A request with no decision expires after 14 days. A requester can have at most 10 open requests.
- **Grants**: an approved request creates a `ContactGrant` for that requester, that person, that group or event, and the fields asked for. Grants never widen lists or rosters: each reveal is its own request, and each writes a `contact.reveal` audit row.
  - Who can revoke: the person, any admin, and for a teen any confirmed guardian. Guardians can end a grant but can't create or approve one.
  - Grants end automatically when the requester stops leading the group or running the event, when the person leaves the group or the event, 30 days after the event's last date, when a teen turns 18 (the new adult's own setting then applies, and they are told), or when either account is disabled. Role changes end grants at once; the daily `contact.sweep` job expires requests and ends the rest.
- **Every attempt is logged** (DEC-31). Each attempt to get direct contact is an `AuditLog` row, whatever the outcome: the request, each party's approval or denial, withdrawal, expiry, the grant, every reveal, revocation, automatic end, and any call that asked for hidden fields without a grant (refused). These rows are kept when a person is later deleted, for safeguarding reviews and legal requests. The separate tamper-evident log of DEC-33 was cut (M6.14, DEC-140): `AuditLog` is the record.
- **History for safeguarding reviews.** An active adult admin opens a person's contact and access history from Admin, filters it by calendar days in the church time zone, and exports it as CSV. They find the person by full name or email, or by a saved person ID for a deleted account. docs/API.md ("Administrative contact/access history") has the lookup and date rules.
- **Picking a person by name (DEC-78).** Leaders and admins add people by typing part of a name. The picker is not a directory: there is no browsing and a search needs at least 2 characters, and people cannot opt out of being found by name. Picking someone never reveals contact details (`api_people.go`; mechanics in `docs/API.md`).
- **Replies stay inside Fold. Planned (M3.11):** see 7.8.
- **Co-leaders** (DEC-31, DEC-34). A leader can make another adult member a co-leader without an admin (section 6). The promotion writes an `AuditLog` row and sends admins an in-app notice, so an admin can undo a bad promotion quickly. There is **no admin-approval gate**, even in groups with teens. A co-leader reaches the group, teens included, only through Fold; every message is in the history admins see; and they see no teen contact details without a grant. A co-leader does see what any leader sees about attendees (names, Teen badges, ages, and children's care notes during the event window) and can remove registrations. If a church's safe-church policy requires vetting every leader, revisit the gate; the background-check flags (M6.8) are the natural place for it.

### 5.12 Notifications (DEC-42, DEC-43)

Fold builds its notifications itself: no vendor and no new service (DEC-42, DEC-43). Knock (knock.app) is the **UX reference only**: separate unread, read, and archived states, "Mark all as read", archive with ×, batching, at most two inline buttons, and a category × channel preferences grid. Knock and Novu were rejected as add-ons: Knock is closed SaaS that would need an account per church, and Novu needs MongoDB, Redis and four services. The UX research is [`research/notifications.md`](../research/notifications.md). This section owns the product rules; docs/API.md ("Notifications") owns the HTTP contract.

**Shape.** Notifications are stored, not derived, with one row per recipient (fan-out on write). The code that writes a change and its `AuditLog` row calls `notify.Send(tx, notify.Msg{...})` in the same transaction. `internal/notify` holds the **kind registry in Go** (`kinds.go`), not in the database. A `Kind` sets its category, whether it is a task waiting on someone (`Waiting`), its `BatchWindow`, its inline `Actions`, its text (`Render`, and the link-only `RenderEmailSafe` for sensitive kinds), and its channels (`AllowEmail`; `AllowPush` with its own `PushRender`). A kind's email is required, and can't be turned off, when it is a safety kind or a task (DEC-97). Text is rendered when the row is written, so later edits don't rewrite history. The tables are `notifications` and `notification_deliveries` (5.3). **Planned (M3.12):** `AllowSMS`. **Planned (M6.16):** a `notification_settings` table for digests (DEC-44).

**States and the badge.** In-app is always on.
- **Badge (DEC-44):** one plain count, with no needs-action dot: unread items plus unresolved tasks waiting on you, with no double counting, shown as "9+" past 9. Opening the panel doesn't clear it. Opening an item, or "Mark all as read", clears ordinary items.
- **"Mark all as read" (6.13 #2):** `read-all` skips unresolved tasks on the server, and the count ignores `read_at` for a task until `resolved_at` is set, so server and UI agree.
- **`seen_at` (6.13 #1):** the column is kept for the email escalation below. Nothing sets it yet. **Planned (M3.12):** the panel marks the rows it shows as seen again (the `seen` endpoint was removed until the escalation needs it).
- **Task resolution:** approving, rejecting, or withdrawing the subject sets `resolved_at` on every copy in the same transaction (`notify.Resolve`). Other admins see "Done · approved by Pat" instead of a stale button, and the item leaves the count.
- **Retention (DEC-45):** the daily `notifications.cleanup` job deletes read, archived or expired rows 90 days after their last update, and any row after 180 days. Open tasks are kept however old. Notifications are a convenience; `AuditLog` stays the record.

**API** (under `/api/v1`, session auth, CSRF on writes, `Cache-Control: no-store` like every API response):
- `GET /notifications/badge` returns `{unread, waiting}`. Both numbers feed the bell's accessible name (6.13 #1). It answers 304 against an ETag when nothing changed.
- `GET /notifications?filter=all|unread|waiting|archived&before=<cursor>&limit=20` returns the feed (`limit` at most 50). On `all` and `unread`, up to 5 open tasks come back in their own `waiting` array, with `waitingTotal`.
- `POST|DELETE /notifications/{id}/read` and `POST /notifications/read-all`.
- `POST|DELETE /notifications/{id}/archive` and `POST /notifications/archive-read`.
- `GET|PUT /me/notification-settings` (the switches, below).

A user can only read or change their own rows; anyone else's answers 404.

**Refresh, not websockets (6.13 #8).** The SPA polls the badge every 60 seconds while the tab is visible. It also refetches on window focus, on navigation, when the browser comes back online, and after an inline action, and it pauses while the tab is hidden. Same-origin SSE is a possible later upgrade (`connect-src 'self'` already allows it). The last-loaded feed is kept **in memory only**, never in localStorage, for the offline and stale-on-error states (6.13 #7). Logout leaves nothing behind (10.1).

**Batching (6.13 #4).** `notify.Send` merges a new row into an open unread row with the same `(user_id, group_key)` inside the kind's `BatchWindow`. Merging increments `batch_count`, re-renders the title ("Pat Lee and 2 others joined Youth Group"), and bumps `updated_at`. No kind sets a `BatchWindow` yet, so nothing is batched today. Tasks, safety and contact kinds, and cancellations keep `BatchWindow = 0`.

**Inline actions (6.13 #5, #6).** Only the kind registry emits `actions`, and only for allow-listed kinds. The v1 allow-list is two items, both for admins:
- Approve a new group (`group.pending`).
- Approve an account request from an adult with no warnings (duplicate, rate-limit, or honeypot flags) (`account.requested`). Any other account request, including every teen request, goes to Review (DEC-44).

Anything about a minor, consent, contact access, roles, deletion, outbound messages, visibility overrides, or RSVPs is navigation-only (research 6.6). Each button calls the existing domain endpoint, so policy and audit stay in one place. That endpoint refuses a notification-originated action on a kind that isn't allow-listed, whatever the client sends. Allow-listed actions use conditional updates. A lost race returns 409 with `resolvedBy`, so the row shows "Already approved by Pat", not an error.

**Delivery: jobs, the email adapter, and web push.**

```
domain code (same tx as AuditLog) ──> notify.Send
   ├─ insert or merge `notifications` rows (in-app, always)
   └─ for kinds that email or push: a queued email `notification_deliveries` row
      and one `notify.deliver` job per notice
job runner (5.5, SKIP LOCKED):
   notify.deliver: AllowPush kinds → one push.deliver job per device
                   AllowEmail kinds → claim the email row → optional-email switch, suppression,
                   active recipient → EmailSender (5.6) → the row ends sent, suppressed, failed or deferred
   push.deliver (one job per device, DEC-125):
                   push_enabled + an active account + the person's Push switches
                   → drop if archived, expired, resolved, or (an ordinary item) read
                   → quiet hours and the hourly limit (DEC-126)
                   → lock-screen text (the kind's own PushRender) → the device; 404/410 removes it
notify.reconcile (hourly): requeues lost delivery jobs and backfills notices of the last 24 hours
```

- **Email timing.** Email goes when the job runs, with no delay. It doesn't skip a notice the person already read. Email is "Right away" only in v1 (DEC-44); daily digests are M6.16. **Planned (M3.12):** the 15-minute "skip if seen or read" escalation for non-urgent kinds; required kinds still send at once.
- **Sensitive email (6.13 #9).** Kinds that touch minors, consent, or contact access render through `RenderEmailSafe`. It names nobody and only links in: "Review an update in Fold. Sign in to Fold to review an update."
- **Push.** Optional kinds wait for the recipient's 8 AM–8 PM window. Required kinds and changes to events starting within 24 hours go at once. Past 20 optional pushes per person per hour, the extras are `suppressed` and the bell still has them (DEC-126). Safety, consent and contact kinds push only the safe text ("Open Fold to review an update."), titled with the church's name.
- **Quiet hours (DEC-44, DEC-126).** In-app notices are silent and arrive at any time. Email ignores quiet hours, as in 7.5. Optional push follows the window above (an interim copy of the send-window function until M3.8). **Planned (M3.12):** notification SMS, through Consent, Suppression, the teen rules and the shared send window (M3.8).

**Which events go in the bell.**
- **In the bell:**
  - approvals and requests waiting on you (groups, group deletions, account requests, household links, room requests, direct-contact and add requests, dead background jobs)
  - leader changes (M0.2): the person whose role changed, and admins
  - visibility overrides (DEC-28)
  - "You're not back in this group" notices for people re-enabled into a full group or event (DEC-47)
  - contact requests and grants, including guardian notices (5.11)
  - account and consent changes, and birthdays (turned 18; a household child turned 13)
  - for events you're going to or waitlisted for: cancellations, time or place changes, removals, and "you're in" off the waitlist
  - **Planned:** message requests (M3.9, 7.6) and conversation replies (M3.11, 7.8)
- **Event items expire (6.13 #3):** their `expires_at` is the occurrence end. After that the feed and badge treat them as archived (nothing rewrites the row), and retention removes them later.
- **Not in the bell (DEC-44):** event **reminders** go by email and push only (7.7, DEC-123; SMS reminders arrive with M3.8), and the old derived "upcoming events" items are gone.

**Minors and privacy** (10.1):
- People without a login get no rows. The account holder gets them instead, naming the person ("Sam's registration was removed").
- A `pending_consent` teen can't sign in, so has no feed (DEC-85).
- Title, body, and `data` never include an email, phone, birth date, care note, or message body of a teen, or a hidden adult's contact details (DEC-31). They carry display names and in-app links only. The registry tests render every kind with teen and hidden-adult fixtures.
- Rows about a teen or child never carry inline actions. A teen account request says "(teen)".
- Rows cascade-delete with the user. `AuditLog` keeps the record.

**CSP.** Notifications add no origin. Everything is same-origin (`connect-src 'self'`). Icons are inline SVG, avatars are served by Fold, and times use `Intl`. Rows render as plain text (no `dangerouslySetInnerHTML`). `link` must be an internal path starting with `/`; `notify.Send` and a database check both refuse anything else.

**Settings** (`GET|PUT /me/notification-settings`, on Me > Notifications):
- "Turn off all optional email" (DEC-97) and one Email switch per optional category this person gets (DEC-115), all written as append-only `Consent` rows (6.13 #10, DEC-44). There is no email-timing choice and no per-group setting in v1 (DEC-44).
- The Push section (M3.13, DEC-122): whether push is on, the devices with push on by label, a Push switch per optional category, "Turn off notifications on this device", and "Turn off notifications everywhere".
- The same component serves the signed-out email settings page an email links to (DEC-115); the Push section appears only when signed in.
- **Planned (M3.12):** the `/settings/notifications` page with a Text column, the quiet-hours line with the effective time zone (`User.timezone`, falling back to the church zone, 6.13 #11), and read-only `guardian_copies` on a teen's own page (6.13 #12).

**Mobile** (6.13 #13): below 640 px the bell opens `/notifications` as a full page, not a popover. The page has filter chips, `?filter=` in the URL, and back returns you to where you were.

A seam, not a vendor: `notify.Send` is the single entry point. Web push is the browsers' own push services: no vendor, no account, no cost (M3.13, DEC-121). No ntfy, Knock or Novu adapter is planned.
