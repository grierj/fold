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

- `Setting(key, value)`: exists. Keys include `site_name` (the church name; the code's key), `timezone` (IANA, required), `church_address` (for email footers and legal pages), `rooms_enabled` (bool), `room_bookings_need_approval` (bool, default false, DEC-102), `self_signup` (`off|request`, default `request`; there is no "open" mode, DEC-4), `sms_send_window_start` (default `08:00`), `sms_send_window_end` (default `20:00`; both read in each recipient's local time, DEC-9, DEC-25), `sms_registration_path` (`ein_10dlc|ein_toll_free|sole_prop_10dlc|sole_prop_toll_free`, DEC-3), `guardian_copies` (bool, default false; email copies only, DEC-19, DEC-29), `jitsi_domain`, `push_enabled` (bool, default true, DEC-124), `vapid_public_key`, `vapid_private_key_enc` (made on first boot, DEC-125).
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
  - `email_verified_at`, `session_version` (bumped to end other sessions, 10.1), `pending_email` and its verification-token fields. Future: `phone_verified_at` (SMS ownership, 7.4), `totp_secret_enc` and `totp_enabled_at` (M4.9), directory preferences. There is no separate `login_enabled` column; account status, password and consent determine access.
  - `hide_contact_from_leaders` (bool, default false; adults only, DEC-31). Teens are always hidden from leaders and hosts, whatever this says (5.11).
  - Sign-in requires an active account and a password. Pending-consent teens cannot establish a login; they browse public groups and events as signed-out visitors (DEC-85). Guests cannot sign in; an approved upgrade reuses their row.
  - There is no `username` column (DEC-23).
- `AccountRequest(name, email, phone, account_type, birth_date?, message, source self_signup|member_invite|guest_upgrade, requested_by?, group_id?, status pending|approved|rejected|expired|withdrawn, decided_by, decided_at, decision_note, expires_at)`. Only admins approve. Approval creates or upgrades the `User` and emails a set-password link. Requests expire after 30 days (DEC-4). A request for an email that already has an account gets the same on-screen answer as any other request ("Thanks; check your email"), and the email says "you already have an account; sign in or reset your password". The form never reveals whether an email is registered (10.1).
- `Invitation(email, phone, invited_by, role, account_type, group_id?, token_hash, expires_at, accepted_at)`. Created only by admins and leaders. A leader's invitation may name only a group they lead. Accepting creates an active account with no admin approval, except that a teen account starts `pending_consent` (DEC-4, DEC-8). A leader may make at most 20 valid invitation attempts a day, including refusals for existing accounts or pending invitations (default, set by admins; 10.1). Refusals spend only the actor allowance, not a recipient's mail allowance.
- `PasswordReset(user_id, token_hash, expires_at, used_at)`.
- `Household(name, address?, created_by)`.
- `HouseholdAccount(household_id, user_id, relation guardian|adult|teen)`: people in the household who have their own account. An account belongs to at most one household.
- `HouseholdPerson(household_id, first_name, last_name, kind child|teen|adult, birth_date?, care_notes, photo_consent, created_by)`: people with **no login, email, or phone** (DEC-8, DEC-23). They are:
  - `child`: under 13. Children never have accounts.
  - `teen`: 13 to 17 without their own email; registered by a guardian.
  - `adult`: an adult who shares the account holder's email, for example a spouse (5.10).
  - `care_notes` (allergies, medical, custody notes) are visible only as section 6 allows. A `HouseholdPerson` can later be turned into their own account using an invitation and admin-approved account request (DEC-82). Acceptance alone creates no account and moves nothing. Adult approval transfers registrations atomically. Teen approval stages an account; transfer waits for account consent and a newly confirmed link to an original household guardian (M1.10, DEC-83).
- `GuardianLink(guardian_user_id, teen_user_id?, person_id?, status pending|confirmed|rejected|revoked, confirmed_by, check_note, created_at)`. Exactly one of `teen_user_id` or `person_id` is set. Every link to a teen or child starts `pending`, and only an admin can confirm it (DEC-32). That covers a guardian adding a child or teen household person, turning a household teen into an account, a teen naming a guardian at self-signup, and anyone claiming an existing teen. There is no auto-confirm path. The confirming admin writes how they checked in `check_note` (for example "known family; parent came to the office"). Until a link is confirmed, the adult can't grant consent, register the child or teen, or get copies, and leaders see nothing about the person. Adding a teen account to a `HouseholdAccount` follows the same rule. Adult household people need no approval.
- `TeenConsent(teen_user_id, scope account|sms|photos, status granted|revoked, granted_by, granter_role guardian|admin, evidence_text, at)`. Append-only; the latest row per `(teen, scope)` wins. `account` consent turns a teen from `pending_consent` to `active`. `evidence_text` is required when an admin grants, for example "paper consent form on file, signed 2026-10-12". The daily age job turns teens into adults on their 18th birthday and keeps the consent history.

**Groups**

- `Group`: `visibility` (`public|private|invite_only`; replaces `Visibility` + `InviteOnly`), `status` (`pending|active|archived`), `leader_id` (displayed primary contact only; never used for permissions, C12), `tags` (free text, comma-separated; the Home chips filter by them, and room suggestions use them as the event's type, DEC-100). `member_limit` (nullable positive int; null, the default, means unlimited; set by the group's leaders or an admin; DEC-39, 5.4). Joining: any member who can see a public or private group may join it at once; invite-only groups are joined only by invitation. There is no join-request step; leaders remove members who shouldn't be there (DEC-30). There is no teen restriction on groups (DEC-20).
- `Membership`: `role` = `leader|member`; several leaders allowed; this is the only source of leadership. `status` = `active|left|removed`. `left` records a voluntary departure: the row and first-ever join date remain, and the person may rejoin under the ordinary visibility and capacity rules (DEC-86). Reactivation advances the membership tenure. `removed` means a leader or admin removed the person (DEC-35): they can't rejoin on their own until a leader or admin adds them back, and the removal is in the `AuditLog`. A disabled account keeps its `active` memberships, hidden and uncounted, with any leadership handed off (DEC-38). When an account is re-enabled and a group is at its `member_limit`, that membership is dropped and the person is told to ask a leader (DEC-47); there is no `pending_return` status. A teen account can never have `role=leader` (DEC-18).

**Events**

- `Event` (renamed from `Meeting`):
  - `group_id` (nullable; null = a church-wide event, created only by admins and run by its assigned hosts, see `EventHost`; DEC-7)
  - `title`, `description` (plain text, at most 5000 characters); there is no event category: room suggestions use the group's tags as the event's type (DEC-100). Rich text belongs to the messaging composer, not event descriptions.
  - `visibility` (`public|private|invite_only`), the event's own value, copied from the group when the event is created (DEC-48), and `leader_opened` (bool), set when a leader or admin made the event wider than its group. There is no live link to the group: making the group more open never changes its events. Making the group more private tightens, in the same transaction, every event more open than the new value that isn't `leader_opened` (one audit row each), and cancels and notifies registrants who can no longer see it; each `leader_opened` event wider than the new value is shown to the leader before saving, to keep open (a logged override) or tighten (DEC-6, DEC-17, DEC-48):
    - A leader of the event's group, or an admin, may set any of the three values on a group event. For example, an invite-only group's event can be made `private` (all signed-in members) or `public`.
    - A change that makes the event more open than its group writes an `AuditLog` row `event.visibility_override` (old value, new value, group visibility, optional `visibility_reason`; a reason is never required, DEC-28) and notifies the group's other leaders and the admins. Going back to the group's value or to a stricter value is also audited and clears `leader_opened`.
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
- `EventOccurrence(event_id, original_date, starts_at, ends_at, status scheduled|cancelled, cancel_source series|manual, title_override, location_override, capacity_override, starts_at_override?, duration_override?, visibility_override?)`. Unique `(event_id, original_date)`. `visibility_override` (nullable; M1.13, DEC-46) lets one date of a series be shown more or less widely than the series; null means the date follows the event's visibility. Occurrences are generated 18 months ahead and extended daily. `MeetingException` becomes `status=cancelled` on the occurrence. `cancel_source` says who cancelled a date: a series edit that dropped it (`series`, which a later edit including the date again undoes) or a person (`manual`, DEC-91).
- Series edits (v1 rule): an edit to the series applies to **all future occurrences**; a change to one date is a per-date override on that occurrence. There is no "this and following" split in v1. A date's own visibility (`visibility_override`, M1.13, DEC-46) is a per-date override like the others. When a series edit changes times:
  - Future occurrences without overrides move to the new times; their registrations stay.
  - Occurrences whose date no longer exists in the new rule are cancelled, and their registrants are told.
  - Each moved occurrence's room bookings move with it (`RoomBooking.starts_at`, `ends_at`, and `padded_range` are recomputed in the same transaction). A booking that would now conflict is set to `requested` instead of failing the edit, and the leader or host is told which dates lost their room (9.2).
- `Registration` (renamed from `RSVP`): `occurrence_id`, `user_id` (nullable), `household_person_id` (nullable; exactly one of the two is set), `registered_by` (the account holder, for household people), `status` (`going|maybe|declined|waitlisted|cancelled|removed`), `attendance_mode` (`in_person|remote`), `guest_count`, `childcare_count`, `childcare_ages_note`, `childcare_requested` (explicit named in-person child care; unnamed count/ages stay separate), `waitlisted_at`, `promoted_at`, `removed_by`, `removal_note`, `source` (`web|admin|guest_form`), `manage_token_hash` (lets guests cancel from an email link). Partial unique indexes on `(occurrence_id, user_id)` and `(occurrence_id, household_person_id)`. `removed` is a leader or host pruning someone (DEC-20, DEC-30). Registrations of a disabled account stay, hidden from attendee lists and never counted; on re-enable each eligible answer returns when its attendance pool and childcare have room; an answer that cannot fit is dropped with an audit row and an ask-a-leader notice (DEC-38 as amended by DEC-47, 5.4). Household answers restore cumulatively per occurrence.
- Later: `Attendance(occurrence_id, user_id?, household_person_id?, present, checked_in_at, checked_in_by, method roll|qr|kiosk)`.

**Rooms (optional; hidden when `rooms_enabled=false`)**

- `Room(name, building, capacity, features, notes, active, restricted, default_setup_minutes, default_teardown_minutes)`. `restricted` rooms (for example the nursery) can be booked only by admins. Each booking copies the room's default setup and teardown minutes; an admin may override them on one booking (DEC-101). Rooms have no color: the by-room view tells them apart by column and name (DEC-107).
- `RoomBooking(occurrence_id, room_id, starts_at, ends_at, setup_minutes, teardown_minutes, padded_range tstzrange, status confirmed|requested|rejected, allow_overlap, overlap_reason, created_by)`. The app computes `padded_range = [starts_at - setup, ends_at + teardown)` on every insert and update (a `timestamptz - interval` expression is not immutable, so it cannot be used inside the constraint). Conflicts are enforced by a PostgreSQL exclusion constraint `EXCLUDE USING gist (room_id WITH =, padded_range WITH &&) WHERE (status = 'confirmed' AND NOT allow_overlap)`, using `btree_gist` (DEC-1). A `CHECK` keeps `padded_range` consistent with the four source columns. An admin double-booking sets `allow_overlap=true` with a reason.

**Messaging and consent**

- `Message(channel email|sms|both, author_id, status draft|pending_approval|scheduled|sending|sent|cancelled|failed, category announcement|reminder|transactional, subject, body_html, body_text, sms_body, audience_json, scheduled_at, sent_at, approved_by, request_id?, send_window_override, override_reason, segments_estimate, recipient_count, push_alert)`. `push_alert` (bool, default true) is a short companion alert to people with push on (DEC-123).
- `MessageRecipient(message_id, user_id, channel email|sms|push, address, status queued|suppressed|deferred|sent|delivered|bounced|complained|failed, reason, copy_of_user_id?, provider_message_id, error, sent_at)`. Unique `(message_id, user_id, channel, copy_of_user_id)`. `copy_of_user_id` marks a guardian copy (DEC-19).
- `MessageRequest(group_id, requester_id, sms_body, proposed_at, status pending|approved|rejected|expired|withdrawn, decided_by, decided_at, decision_note, message_id?)`.
- `Consent(user_id, channel email|sms|push, scope all|group:<id>|category:<name>, status opted_in|opted_out, source web_form|sms_keyword|sms_reply|admin_import|unsubscribe_link|admin, evidence_text, ip, user_agent, at)`. Append-only. For teens, SMS also needs a `TeenConsent` row with scope `sms` (DEC-8). The latest row per `(user, channel, scope)` wins. Turning on a push device writes `(push, all, opted_in)` whenever the latest `(push, all)` row isn't `opted_in` (so turning it on again after "Turn off" works), with evidence text such as "Turned on notifications for iPhone (Home Screen app)"; `(push, all, opted_out)` overrides every per-category row; per-category push switches use `category:<name>` like email (DEC-122). Teens need no `TeenConsent` for push.
- `PushSubscription(user_id, endpoint unique, p256dh, auth, expires_at?, label, last_used_at, failed_at?)` (M3.13a): one row per browser installation, indexed by `user_id`, `CHECK (octet_length(endpoint) <= 2048)` and length bounds on `label`; `last_used_at` is written at most once a day per device; send results go in `notification_deliveries`, not on the device. The endpoint is a capability and is never logged or returned. Rows cascade-delete with the user and are deleted on logout from that browser, on 404/410 from the push service, on key rotation, and 30 days after a failure.
- `Suppression(channel, address, reason hard_bounce|complaint|stop|manual, provider, at)`. Checked at send time, separately from users.
- `InboundMessage(channel, from, to, body, provider_id, received_at, matched_user_id, keyword, opt_out_match?, conversation_id?, forwarded_to)`. `opt_out_match` records the keyword or phrase that triggered an automatic opt-out (7.4).

**Contact privacy (DEC-31, 5.11, 7.8)**

- `ContactRequest(requester_id, subject_user_id, context_type group|event, context_id, fields email|phone|both, reason?, needs_admin, subject_decision pending|approved|denied, subject_decided_at, admin_decision pending|approved|denied|not_needed, admin_decided_by, admin_decided_at, status pending|granted|denied|withdrawn|expired, expires_at)`. `needs_admin` is true for teens. Requests expire after 14 days.
- `ContactGrant(request_id, leader_id, subject_user_id, context_type, context_id, fields, granted_at, ended_at, end_reason revoked_by_subject|revoked_by_admin|revoked_by_guardian|left_role|turned_18|account_disabled, ended_by)`. A grant needs both an unended row and current lifecycle authority; null `ended_at` alone never grants access.
- `Conversation(context_type group|event, context_id, created_by, relay_token_hash, closed_at)`, `ConversationParticipant(conversation_id, user_id)`, and `ConversationMessage(conversation_id, author_id, channel in_app|email|sms, body_text, inbound_message_id?, at)` (from M3.11).

**Notifications (5.12, DEC-42; from M1.1c)**

- `notifications(user_id, kind, category admin|groups|events|messages|safety, priority, waiting, actor_id?, subject_type?, subject_id?, group_key?, dedupe_key?, title, body, link, actions jsonb, batch_count, data jsonb, created_at, updated_at, seen_at?, read_at?, archived_at?, resolved_at?, resolved_by_id?, resolution, expires_at?)`. `waiting` marks a task (set from the kind when the row is written) so the badge query needs no registry; `resolved_by_id` and `resolution` (approved, rejected, deleted, kept, withdrawn) give other admins "Done · approved by Pat" (M1.1c). One row per recipient. `kind` keys the Go registry. `link` is an internal path (starts with `/`) and is validated. `actions` holds at most 2 buttons, emitted only for allow-listed kinds. `data` is small and non-sensitive. `title`, `body`, and `data` never contain contact details of a teen or of a hidden adult (DEC-31). Indexes:
  - unique `(user_id, dedupe_key)` where `dedupe_key` is set (idempotent writes)
  - feed: `(user_id, archived_at, updated_at DESC, id DESC)`
  - partial index for the badge: unread or unresolved, and not archived
  - partial index for open batches: `(user_id, group_key)` where unread and not archived

  Rows cascade-delete with the user.
- No `notification_settings` table in v1 (DEC-44). Email is "Right away" only, and the per-category Email and Text switches and "Turn off all optional email" are all `Consent` rows (`scope category:<name>`), so they need no table of their own (6.13 #10). Locked (required) categories can't be written. A per-user `email_mode` table arrives with digests (M6.16).
- `notification_deliveries(notification_id, user_id, channel email|sms|push, status queued|suppressed|deferred|sent|delivered|bounced|failed, reason, scheduled_for, provider_message_id, sent_at)` (from M1.2). Unique `(notification_id, channel)`. One push delivery row per notification whatever the device count; per-device results live on `push_subscriptions`. Statuses match `MessageRecipient`. Digests (M6.16) add a nullable `notification_id` and a `digest_id`.

**Calendar**

- `CalendarToken(user_id, token_hash, scope mine|all)`: one personal feed token per user. Room feeds use their own `room_feed_tokens(room_id, token_hash, created_by, created_at, revoked_at)` (M2.2b).

**Platform**

- `Job(kind, payload_json, run_at, status queued|running|done|failed|dead, attempts, max_attempts, locked_at, locked_by, last_error, unique_key)` (from M3.1).
- `AuditLog(actor_id, action, target_type, target_id, meta_json, ip, at)`. Created in M0.2. Covers admin actions, message sends and approvals, consent changes (including teen consent, SMS ownership checks, and automatic opt-outs), account request decisions, guardian and household link requests and decisions (DEC-32), event visibility changes, registration removals, send-window overrides, role and leadership changes, email changes, admin 2FA changes, setting changes (including `guardian_copies`), provider config changes, double-bookings, every contact-access event (DEC-31: each request, decision, withdrawal, expiry, grant, reveal, revocation, automatic end, and refused attempt), and one-to-one message metadata. Rows about contact access, minors, consent, and roles are never purged, even when a person is deleted. The durable log (DEC-33, 10.7) hardens this later.
- `Asset(key, visibility public|private, owner_type, owner_id, content_type, size, created_by)`. Needed for C9.

**Later (section 13)**

`SignupSheet(event_id|group_id, title, kind volunteer|potluck|supplies)`, `SignupSlot(sheet_id, label, quantity, starts_at?, requires_check)`, `SlotClaim(slot_id, user_id, note)`; `PrayerRequest(author_id, group_id?, visibility, body, anonymous, status, expires_at)`; `Post` and `Comment` for discussions; `Gallery` and `Photo`; `BackgroundCheck(user_id, status cleared|pending|expired|not_cleared, cleared_on, expires_on, provider_name, reference, entered_by)`.

### 5.4 Capacity and waitlist rules

- Capacity counts `going` registrations plus their `guest_count`, per occurrence and attendance pool. `capacity` limits in-person parties; `remote_capacity` limits remote parties when remote attendance is on (null means unlimited). Each named household person uses one seat; do not count them again as unnamed guests. `maybe` takes no seat. One waitlist remembers each party's mode; switching pools starts a fresh wait in the new pool.
- A new "going" on a full event becomes `waitlisted` with `waitlisted_at = now` if the waitlist is on. Otherwise it is refused with 409 and a clear message.
- Promotion runs in one transaction that locks the occurrence row (`SELECT ... FOR UPDATE`). It runs when a `going` registration is cancelled or declined, when capacity goes up, or when an admin removes someone. It promotes the earliest waitlisted registrations that fit. A registration with guests that does not fit is skipped, and the next one is tried (Meetup does this too).
- Childcare-aware promotion (DEC-80): each picked party must fit both its attendance pool and remaining childcare. Skip a request that does not fit without losing its request, age note or queue time; earlier picks consume childcare in the same pass. Manual promotion refuses with `childcare_full` when care does not fit. Retry when attendance/care is freed or limits grow/clear; there is no separate childcare waitlist. Named in-person children request one spot explicitly; unnamed requests remain separate.
- No auto-promotion within 2 hours of the start (a fixed rule, not a setting). The leader gets a notice instead, and promotion is one click.
- A promoted person gets a transactional email (and an SMS if they opted in) with a one-click "I can't come" link that frees the slot again.
- People see their waitlist position ("You are #3 on the waitlist").
- Disabled accounts (DEC-38): their registrations stay but never take a slot or count in waitlist math or attendee counts. On re-enable each comes back as it was if it fits; otherwise it is left out (the registration is dropped, audited) and the person gets a notice to ask a leader or host (DEC-47). A leader, host, or admin can then add them back, over capacity only as an explicit choice.
- Group member limit (DEC-39). Groups have no waitlist and no registration capacity: an optional `Group.member_limit` caps active memberships (a class in a room). Only `active` memberships of accounts that aren't disabled or deleted count, leaders included; `left`, `removed`, `invited`, and disabled accounts don't. Self-join into a full group is refused with 409 "This group is full". A leader or admin adds someone over the limit only by explicit choice (the API takes `overLimit: true`, the SPA confirms first), and that add is audited (`membership.added_over_limit`). Accepting a leader's or admin's invitation into a group follows the same rule as that leader's add. Lowering the limit below the current count is allowed; nobody is removed, and joins stay blocked until the count is below the limit. Leaders and admins see "count of limit". Every join, add, and restore locks the group row (`SELECT ... FOR UPDATE`) before counting.
- There is no self-sign-up approval step (DEC-30); eligibility still includes consent, age, visibility and prior-removal rules. Manager-initiated teen adds instead require the teen and an admin to approve (DEC-78). A leader or host can prune registrations. Removing a going party retries waitlist promotion when attendance and childcare have room, subject to the two-hour cutoff (DEC-84).
- M1.14 owns join/leave previews and upcoming-registration cleanup (DEC-74), the standing-invite/own-scope model (DEC-72/73), and series answers. Only its purpose-separated one-date invitation prerequisite is already implemented in M1.11.

### 5.5 Background jobs

- Periodic work runs as jobs (M3.1a): each daily task is queued once per church day under the key `<kind>:<church date>`, at startup and at church-local midnight, so birthdays still turn over at church midnight (DEC-83) and several app processes run each task once.
- From M3.1, a `jobs` table and worker live in the same binary. The worker starts with `serve`. `FOLD_WORKERS=0` turns it off, for debugging only.
- Job kinds: `occurrences.extend`, `message.send`, `message.deliver_batch`, `messages.reap` (daily: settles a send that stopped with no job left, M3.5), `reminder.schedule`, `waitlist.notify`, `webhook.process`, `provider.poll_status` (for example SMS registration or email domain verification), `cleanup.login_throttles`, `jobs.cleanup` (finished jobs are kept 30 days), `accounts.expire_requests`, `contact.sweep` (expires contact requests and ends grants daily; role changes also end grants at once, 5.11), `accounts.age_rollover` (teens turn into adults at 18; household children who turn 13 are flagged to their guardians), `notification.deliver` (5.12; `notification.digest` later, M6.16), `push.deliver` (one per device of each notification or message recipient with push on, `unique_key` `push:n:<notification_id>:d:<device_id>` or `push:mr:<recipient_id>:d:<device_id>`; the payload holds device ids, never endpoints; 404/410 deletes the device, DEC-125), and `notifications.cleanup` (daily: auto-archives event items whose occurrence has ended, applies retention, 5.12, and prunes failed or expired push devices).
- The worker claims jobs with `FOR UPDATE SKIP LOCKED` and polls every 1 to 2 seconds (`FOLD_JOB_POLL=1s`). There is no `LISTEN/NOTIFY`.
- Retries use exponential backoff with jitter (30 seconds doubling, at most an hour, ±20%) and a `max_attempts` per kind, then `dead`. A job whose process stopped mid-run is taken back after a 15-minute lease. The admin "System" page lists dead jobs with Retry and Dismiss; admins get no notice for them until message sending (M3.5) brings failures an admin can fix (DEC-111). Those notices, and one for a failed announcement, are still to come: M3.5 shipped without them.
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

Web push is not an adapter: there is no vendor. `internal/push` speaks RFC 8030/8291/8292 to the browsers' own push services with the standard library (DEC-125).

The email adapters (devoutbox, SMTP, Postmark) live in `internal/mail` and register with `internal/providers` (M3.2a); callers send through `mail.Sender` and never see which one runs. `FOLD_EMAIL_PROVIDER` picks one, else dev uses devoutbox, then Postmark when it has a token, then SMTP.

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

- Default is link-out. A leader pastes a Zoom, Meet, Teams, or other URL, or clicks "Generate Jitsi link", which makes `https://<jitsi_domain>/<church-slug>-<12 random characters>` with no API call.
- `video_reveal=registered_only` (the default) shows the link only to people registered `going`, plus leaders and admins. For public events this keeps uninvited people out of the call.
- The link appears on the event page and in eligible email/feed output; scheduled reminders are future M3.6/M3.8 work. Personal ICS alone also reveals `registered_only` links for Maybe, and unanswered dates of a group member or host (DEC-70). Waitlisted, declined, cancelled, removed and unanswered outsiders get no such link. API/email remain going-only apart from managers; public/group feeds include only `always`. All feeds export standalone stored occurrences, so link eligibility is evaluated per date; a qualifying date never widens another date's video disclosure.
- Embedding video inside Fold is out of scope for now (DEC-16). Revisit in a later revision of this plan if churches ask for it. Embedded meet.jit.si calls are reportedly limited to 5 minutes, and JaaS and self-hosted Jitsi cost money or a second server (12.4).
- Later: a Zoom adapter that creates meetings by API, and a Google Meet adapter (needs Google Workspace; not verified).

### 5.9 Time zones

- One church time zone (setting). Events can override `tzid`, which is rarely needed.
- Occurrence expansion uses `rrule-go` with `DTSTART;TZID=<tz>:<local time>`. Each occurrence stores `starts_at` and `ends_at` in UTC. Rules repeat at most once a day, with a cap on dates (DEC-89).
- The UI shows every event time in the viewer's browser zone, converted from the UTC instant, with no zone label (DEC-57). The event page adds one quiet line when the event's clock differs: "Held in Pacific time (6:00 PM PDT)". Forms that edit an event's clock edit it in the event's own zone and name that zone. One formatter, `web/src/lib/when.ts`, does all of this.
- ICS output uses `TZID` parameters with IANA names plus a `VTIMEZONE` block.
- Each person has a profile time zone (`User.timezone`, DEC-58). The SPA fills it in from the browser the first time it is empty, and the profile page changes it. Fold uses it for that person's emails, server-written notification text, and SMS send window (DEC-25). A guest's zone comes from the guest form's browser. Anyone without one gets the church time zone.

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
- **13 to 17**: a teen account. It needs a birth date and its own email, and starts `pending_consent`. It cannot sign in until `account` consent is granted, but can browse public groups and events without logging in (DEC-85). Consent comes from confirmed guardians (a link counts only once an admin confirms it, DEC-32) and admins; the leader role never grants consent. (A leader who is also the teen's confirmed guardian grants it as the guardian.) Guardians often consent by email link; an admin grants with evidence text. When a teen self-signs up, the request also needs normal admin approval; an admin can approve and grant consent in one step.
- **18 or older**: an adult account.

**What teens can do** (DEC-18, DEC-20):

- See what members see, minus the directory, and register for **any event they can see**. There is no youth-only restriction. Leaders and hosts decide who attends: they watch the attendee list and remove anyone who shouldn't be there (5.4).
- Join groups like members do.
- Leaders and hosts never see a teen's email or phone. They reach teens through Fold (5.11, DEC-31).
- Teen accounts and teen or child household people are **clearly badged** ("Teen", "Child") wherever leaders, hosts, or admins see people: attendee lists, group rosters, and message recipient lists and counts ("142 recipients, 6 teens").
- Teens cannot be group leaders or event hosts, and cannot create groups. They do not appear in any directory.
- Email reaches them after `account` consent. SMS reaches them only with both `TeenConsent` scope `sms` and their own SMS opt-in. Push needs only an active account (DEC-122); rows about a household child ("for Sam") use the safe push text for the guardian.
- **Guardian copies** (DEC-19): off by default. An admin can turn on the church setting `guardian_copies`. Then each confirmed guardian gets an email copy of every non-transactional email and SMS sent to their teen, marked "Copy: sent to <teen> because your church sends guardian copies". Copies go by email only, never by SMS, because texts cost money (DEC-29). Turning it on or off is audited, and the privacy page says which way it is set.

**Household people** (no login): children under 13, teens without their own email, and adults sharing an email. Any adult account in the household can manage it; `registered_by` remains the original responsible account, not the last editor. Notices about named registrations go to that holder. GET never creates a household; the first PUT or person-add POST does. Children/teens need an admin-confirmed guardian link (DEC-32/79); adults need no link approval.

**Age and care rules (DEC-81/83):** registration needs birth-date evidence, including for adults. A teen under 13 or child/teen aged 18+ is refused. A saved child aged 13–17 stays eligible until the household edits the kind; the midnight job sends one review notice, not a forced kind change. Ages use the church-local date. Event care-note access requires a going child on a scheduled date inside the inclusive 24-hour-before/after window. Pending household adults may submit notes but cannot read them back.

**Conversion (DEC-82/83):** invitation acceptance stores a pending account request and held password hash, not an account or transfer. Adult approval creates the account and transfers every registration atomically. Teen approval creates a staged pending-consent account; the household keeps managing sign-ups until consent and a newly confirmed teen-account link to an original household guardian are both ready. Independent suspension delays transfer; an inactive original holder defers the entire staged rollover. No memberships move, and transfer preserves registration IDs, answers and queue history without rechecking event access or capacity. See M1.10 and docs/API.md for staging and birthday behavior.

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

Contact privacy, requests and audited reveals are implemented in M1.11. The general email/SMS/in-app relay described here is future M3.11 work, not an installed messaging feature. Leaders, co-leaders and hosts must use that relay to reach teens unless explicitly granted direct contact; adults choose their contact preference.

- **Teens**: a leader or host never sees a teen's email address or phone number. They reach the teen with Fold email, Fold SMS (with the teen's consents, 7.4), or an in-app one-to-one message (7.8). Admins, the teen's confirmed guardians, and the teen still see it.
- **Adults**: the profile setting "Hide my contact info from group leaders" (`hide_contact_from_leaders`) is **off by default**, so leaders see adult members' email and phone as before (10.2). When it's on, leaders and hosts reach that adult only through Fold.
- **Asking for direct contact**: on a hidden person's card, a leader or host clicks "Request direct contact", picks email, phone, or both, and may add a reason. Leaders can ask only about people in their own groups, and hosts only about registrants of their own events.
  - Hidden adult: the adult alone approves or declines in the app. Sensitive email links only navigate; opening one never approves or reveals contact.
  - Teen: an admin **and** the teen must both approve (DEC-31). Either one declining ends the request. A confirmed guardian gets a notice when a grant is made and sees active grants on the teen's page.
  - A request with no decision expires after 14 days. A leader can have at most 10 open requests (defaults).
- **Grants**: an approved request creates a `ContactGrant` for that leader, that person, and that group or event. Every time the leader opens the revealed details, Fold writes a `contact.reveal` audit row.
  - Who can revoke: the person; any admin; and for a teen, any confirmed guardian. Guardians already control a teen's consent, so they can always end a grant, but they can't create one.
  - Grants end automatically when the leader stops leading that group, when a host is removed from the event or 30 days after its last occurrence, when a teen turns 18 (the new adult's own setting then applies, and they are told), or when either account is disabled.
- **Every attempt is logged** (DEC-31). Each attempt to get direct contact is an `AuditLog` row, whatever the outcome: the request, each party's approval or denial, withdrawal, expiry, the grant, every reveal, revocation, automatic end, and any API call that asked for hidden fields without a grant (refused). These rows are never purged, even when a person is later deleted, so they are there for safeguarding reviews and legal requests. Admins can view and export them per person. The durable, tamper-evident log (DEC-33, 10.7) hardens this later.
- **Administrative history lookup and dates.** An active adult admin finds a current person by literal full-name or email search, chooses the result, then applies filters; a saved exact decimal person ID remains the fallback for deleted accounts. The picker returns only exact string `id`, public name and email for non-deleted accounts (inactive included), capped at 50 with no pagination; lookup precedence is exact `id`, exact case-insensitive `email`, then literal `q`. User-row history and Copy person ID first resolve the row's current email and use the returned string ID without numeric coercion; copying always leaves a visible text fallback. From and Until in the UI are inclusive calendar days in the church timezone loaded from site settings. The client sends RFC3339 instants to the API: local start for From and exclusive next-local-day start for Until, using calendar boundaries rather than 24-hour arithmetic across DST. Apply stays unavailable when the church timezone is unavailable; there is no browser-zone or UTC fallback. History IDs and cursors remain exact decimal strings throughout.
- **Replies stay inside Fold**: see 7.8.
- **Co-leaders** (final review, finding 1). A leader can still make another adult a co-leader without an admin (section 6). That co-leader can message the group, teens included, only through Fold. Each send is logged and visible to admins, and they see no teen contact details unless an admin and the teen both approve. That closes the contact-detail part of the hole. Making someone a leader also writes an `AuditLog` row and shows the admins an in-app notice (M0.2), so an admin can undo a bad promotion quickly. There is **no extra admin-approval gate** on co-leader promotion, even in groups with teens (DEC-31). What remains: a co-leader sees what any leader sees about attendees (names, Teen badges, ages, and children's care notes during the event window), and can remove registrations. Revisit the gate if a church's safe-church policy requires vetting every leader; the background-check flags (M6.8) are the natural place for that. Grier confirmed this design on Oct 9, 2026 (DEC-34): no extra approval layers; audit, notify admins, and remove bad actors outright (leaders and admins can remove members from M0.3b, DEC-35).

### 5.12 Notifications (DEC-42, DEC-43)

Fold builds its notifications itself: no vendor and no new service (DEC-42). Knock (knock.app) is the **UX reference only**: separate unread, read, and archived states, "Mark all as read", archive with ×, batching, at most two inline buttons, and a category × channel preferences grid. Knock and Novu were researched and rejected as add-ons. Knock is closed SaaS that would need an account per church, would duplicate Fold's consent and send-window rules, and would add `connect-src` origins. Novu needs MongoDB, Redis and four services. The consolidated UX reference is [`research/notifications.md`](../research/notifications.md). DEC-44/45 settle badge, delivery, preferences and retention; DEC-60–DEC-66 replace the original visual styling. This section owns product rules; docs/API.md owns the shipped HTTP contract.

**Shape.** Notifications are stored, not derived, with one row per recipient (fan-out on write). The same code path that writes the `AuditLog` row calls `notify.Send(tx, notify.Msg{...})` inside its transaction. `internal/notify` holds a **kind registry in Go**, not in the database: `Kind{Key, Category, Required, BatchWindow, AllowEmail, AllowSMS, AllowPush, Actions, Render, RenderEmailSafe}`. Title, body, and link are rendered when the row is written, so later edits don't rewrite history. Tables are listed in 5.3: `notifications` and `notification_deliveries`. A third table, the digest settings `notification_settings`, waits for digests (M6.16, DEC-44).

**States and the badge.** In-app is always on.
- **Badge count (DEC-44):** the badge shows one plain count, with no needs-action dot: unread items plus unresolved items waiting on you, with no double counting, capped at "9+". Opening the panel doesn't clear it. Opening an item, or "Mark all as read", clears ordinary items.
- **"Mark all as read" (6.13 #2):** `read-all` skips unresolved waiting items on the server. The count also ignores `read_at` for them until `resolved_at` is set, so server and UI agree.
- **`seen_at` (6.13 #1):** kept only for the email escalation ("email if not seen after 15 minutes"). The badge doesn't use it, and the panel posts `seen` with the ids it shows.
- **Task resolution:** approving, rejecting, or withdrawing the subject sets `resolved_at` on every copy in the same transaction. Other admins see "Done · approved by Pat" instead of a stale button, and the item leaves the count.
- **Retention (DEC-45):** a daily cleanup deletes read or archived rows after 90 days, unread rows after 180, and anything past `expires_at` that isn't kept under Archived. Notifications are a convenience. `AuditLog` stays the record.

**API** (under `/api/v1`, session auth, CSRF on writes, `Cache-Control: no-store` like every API response):
- `GET /notifications/badge` returns `{unread, waiting}`. Both numbers feed the bell's accessible name (6.13 #1). It is cheap and answers 304 against an ETag when nothing changed.
- `GET /notifications?filter=all|unread|waiting|archived&before=<cursor>&limit=20` returns the feed. Waiting items come back in their own array.
- `POST /notifications/seen {ids}`.
- `POST|DELETE /notifications/{id}/read`.
- `POST /notifications/read-all`.
- `POST|DELETE /notifications/{id}/archive`, plus `POST /notifications/archive-read`.
- From M3.12: `GET|PUT /me/notification-settings`.

A user can only read or change their own rows.

**Refresh, not websockets (6.13 #8).** No websockets. The SPA polls the badge every 60 seconds while the tab is visible. It also refetches on window focus, on navigation, and after an inline action, and it pauses while the tab is hidden. Same-origin SSE is a possible later upgrade (`connect-src 'self'` already allows it). The last-loaded feed is kept **in memory only**, never in localStorage, for the offline and stale-on-error states (6.13 #7). Logout leaves nothing behind (10.1).

**Batching (6.13 #4).** `notify.Send` merges into an open unread row with the same `(user_id, group_key)` inside the kind's `BatchWindow`. Merging increments `batch_count`, re-renders the title ("Pat Lee and 2 others joined Youth Group"), and bumps `updated_at`. Kinds that are waiting on someone, safety and contact kinds, and cancellations have `BatchWindow = 0` and are never batched.

**Inline actions (6.13 #5, #6).** Only the kind registry emits `actions`, and only for allow-listed kinds. The v1 allow-list is two items, both for admins:
- Approve a new group.
- Approve an account request from an adult with no warnings (duplicate, rate-limit, or honeypot flags). Any other account request, including every teen request, goes to Review (DEC-44).

Anything about a minor, consent, contact access, roles, deletion, outbound messages, visibility overrides, or RSVPs is navigation-only (research 6.6). Each button calls the existing domain endpoint, so policy and audit stay in one place. That endpoint refuses a notification-originated action on a kind that isn't allow-listed, whatever the client sends. Allow-listed actions use conditional updates. A lost race returns 409 with `resolvedBy`, so the row shows "Already approved by Pat", not an error.

**Delivery: jobs plus the email and SMS adapters, and web push.**

```
domain code (same tx as AuditLog) ──> notify.Send
   ├─ insert or merge `notifications` rows (in-app, always)
   └─ per recipient and channel the kind allows: job `notification.deliver` {notification_id, channel, not_before}
job runner (5.5, SKIP LOCKED):
   notification.deliver: skip if read, archived, or resolved (escalation) → Consent + Suppression + teen rules
                         → SMS send window (the shared messaging function, DEC-9, DEC-25)
                         → EmailSender / SMSSender (5.6)
                         → `notification_deliveries` row; guardian copies follow DEC-19 and DEC-29
   push.deliver (one job per device, DEC-125):
                         push_enabled → Consent(push) + teen rules → quiet hours (DEC-126)
                         → lock-screen rendering (the kind's own PushRender) → the device; 404/410 removes it
```

Push is immediate: it has no 15-minute "if not seen" escalation like email, so "skip if read, archived or resolved" matters only on retries and quiet-hours deferrals. `push.deliver` stays its own job when M3.12 moves notification email onto `notification.deliver`.

- **Email before M3.1.** The `jobs` table arrives in M3.1. In M1.2, notification email goes through the minimal sender (`internal/mail`) right after the transaction commits, with no delay. The 15-minute "if not seen" escalation starts once `notification.deliver` exists (M3.1 or later). Required and security kinds always send at once.
- **Sensitive email (6.13 #9).** Kinds that touch minors, consent, or contact access render through `RenderEmailSafe`. It names nobody and only links in: "You have a contact request in Fold. Sign in to review it."
- **Email timing (DEC-44).** Email is "Right away" only in v1. Once-a-day digests move to a later version (M6.16).
- **Quiet hours (DEC-44).** In-app notices are silent and arrive at any time. SMS follows the 8 AM–8 PM send window. Email ignores quiet hours, as in 7.5. Optional push follows the SMS window; required kinds and changes to events within 24 hours go at once (DEC-126).

**Which events go in the bell.**
- **In the bell:**
  - approvals and requests waiting on you
  - leader grants (M0.2)
  - visibility overrides (DEC-28)
  - "You're not back in this group" notices for people re-enabled into a full group (DEC-47)
  - contact requests and grants, including guardian notices (5.11)
  - message requests (7.6) and conversation replies (7.8)
  - for events you're going to or waitlisted for: cancellations, time or place changes, and "you're in" off the waitlist
- **Event items expire (6.13 #3):** they have `expires_at` set to the occurrence end. A daily job sets `archived_at` when that passes; they aren't deleted, so they stay under Archived until retention removes them.
- **Not in the bell (DEC-44):** event **reminders** go by email, push and SMS only (7.7), and today's derived "upcoming events" items are dropped.

**Minors and privacy** (10.1):
- People without a login get no rows. The account holder gets them instead ("for Sam (your household)").
- A `pending_consent` teen has no feed.
- Title, body, and `data` never include an email, phone, birth date, care note, or message body of a teen, or a hidden adult's contact details (DEC-31). They carry display names and in-app links only.
- Rows about a teen or child carry the Teen or Child badge and never carry inline actions.
- Rows cascade-delete with the user. `AuditLog` keeps the record.

**CSP.** Notifications add no origin. Everything is same-origin (`connect-src 'self'`). Icons are inline SVG, avatars are served by Fold, and times use `Intl`. Rows render as plain text (no `dangerouslySetInnerHTML`). `link` must be an internal path starting with `/` and is validated on write.

**Settings payload** (M3.12; the "Turn off all optional email" switch alone arrives earlier, DEC-97):
- per-category Email, Text and Push switches, plus "Turn off all optional email", all mapped to `Consent` rows (6.13 #10, DEC-44); no email-timing choice in v1 and no per-group settings (DEC-44)
- the devices with push on, by label, with "Turn off notifications on this device" and "Turn off notifications everywhere" (M3.13, DEC-122). The component also serves the signed-out token page (DEC-115); the Push column and the device list appear only when signed in.
- the effective time zone (`User.timezone`, falling back to the church zone) for the quiet-hours line (6.13 #11)
- read-only `guardian_copies` for a teen's own page (6.13 #12)

**Mobile** (6.13 #13): below 640 px the bell opens `/notifications` as a full page, not a popover. The page has filter chips, `?filter=` in the URL, and back returns you to where you were.

A seam, not a vendor: `notify.Send` is the single entry point. Web push is that channel (M3.13, DEC-121): the browsers' push services, no vendor, no account, no cost. ntfy is not planned. No Knock or Novu adapter is planned.

