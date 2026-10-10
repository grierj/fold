# Notifications for Fold: Knock vs. self-built, and UX/UI guidelines

Status: research draft, 2026-10-10. Written by Quill (research) for Codex, who turns it into a plan item. Grier decides on any vendor.
Scope: read-only desk research. No accounts were created and no vendor was contacted. Claims not confirmed from a primary source are marked **[unverified]**. Estimates are marked **[estimate]**.

Repo copy: added to `docs/plan/research/` on Oct 10, 2026 and copied verbatim, so its plan references point to draft v4 and may have moved. The decided design is `PLAN.md` 5.12 (DEC-42, DEC-43). Grier decided to build in Fold with no vendor; Knock is a UX reference only. Grier answered the 6.14 questions on Oct 10, 2026 (`PLAN.md` DEC-44).

Plan references are to `/workspace/fold-plan/PLAN.md` (draft v4, 2026-10-09).

---

## 0. TL;DR

- **Recommendation: build it in Fold.** One Postgres `notifications` table with per-user `seen_at` / `read_at` / `archived_at`, a small Go `internal/notify` package, a polled JSON API, and delivery to email/SMS through Fold's existing provider adapters and job runner. No vendor, no new service, no new CSP origins.
- **Knock is good, but the wrong shape for Fold.** It is closed-source SaaS (no self-host), US-hosted, and each church would need its own Knock account, keys, signing key, and workflows/templates configured in Knock's dashboard (DEC-2). It would duplicate rules Fold already owns (Consent, Suppression, SMS send window, guardian copies, audit). Its in-app feed needs `connect-src https://api.knock.app wss://api.knock.app`.
- **Novu** (the main open-source alternative) has an MIT core and can be self-hosted, but needs MongoDB, Redis, and four Node services (API, worker, websocket, dashboard), with 4 vCPU / 8 GB RAM recommended for one VM. That is heavier than Fold and breaks DEC-1 ("Postgres only").
- **Copy Knock's UX model anyway**: unseen / seen / read / archived as separate states; the badge counts *unseen*; "Mark all as read"; archiving with an × button; batching ("3 people asked to join…"); actionable items with 0–2 buttons; and a category × channel preferences grid. *(Illuminator cross-ref: the badge and preferences grid were revised in 6.1 and 6.8, see changelog 6.0.)*
- **Fix now:** dismissals move from `localStorage` to the server (`archived_at`), so they survive logout and work across devices.

---

## 1. Where Fold is today

- `api_notifications.go` returns *derived* items on each request: admin counts for pending groups and deletion requests, plus up to 10 occurrences in the next 7 days for the user's groups. Nothing is stored. (PLAN 3.x: "Derived notifications (not stored), dismissed in the browser's localStorage.")
- `web/src/components/Notifications.tsx` polls every 5 minutes. It keys dismissals as `type:link:title` in `localStorage["fold-dismissed-notifications"]`. Opening an item dismisses it. The badge counts items that haven't been dismissed. There is no seen/read distinction, no history, and no preferences. Because the key includes the title, "2 groups need approval" becoming "3 groups need approval" counts as a new item, so a dismissed item comes back.
- The plan already calls for in-app notices in several places: leader grants (M0.2), event visibility overrides (M1.1b, DEC-28), waitlist cutoffs (M2.x), SMS message requests (7.6), contact requests and grants including teen and guardian notices (5.11, DEC-31), and conversation replies (7.8). M1.1b says these stay derived from `AuditLog` until email exists.
- Constraints that shape the design: Go + Gin + GORM, goose migrations (C1); Postgres only (DEC-1, DEC-24); in-process `jobs` table with `SKIP LOCKED` and no Redis (C7); provider adapters in `internal/providers` (5.6); Consent / Suppression / SMS send window by recipient time zone (7.4, 7.5, DEC-9, DEC-25); a CSP header for the SPA (10.1); and no Python, one binary (3.x).

---

## 2. Knock (knock.app)

### 2.1 What it offers

| Area | What Knock does | Source |
|---|---|---|
| In-app feed | A per-user feed of messages produced by workflows or broadcasts, newest first. The API returns unread, read, and seen counts for badges, and can filter by status (unread, read, seen, archived) or by custom data. Unlimited feeds per recipient. Feed content can't be edited after it's sent: you archive the message and send a new one. Pinning isn't supported. | https://docs.knock.app/in-app-ui/feeds/overview |
| Message states | Delivery status (queued, sent, delivered, bounced, undelivered, …) is separate from engagement status. Engagement statuses can combine: `seen` (fetched for display in the feed at least once), `read`, `interacted` (the item itself was clicked), `link_clicked`, and `archived`, each with a timestamp. "Mark all as read" sets read and does not count as a click. | https://docs.knock.app/send-notifications/message-statuses |
| Feed behavior | The default badge counts **unseen** ("less noise and disruption"), and opening the feed marks items seen. Clicking an item, or "mark all as read", marks it read. The × button archives; archived items are hidden, but the API can fetch them with `archived=only`. Template variants: standard, single-action, multi-action (two buttons). | https://docs.knock.app/integrations/in-app/feeds |
| Real-time | A managed websocket service (Phoenix channels). A `new-message` event carries badge counts, not message content, and "might be throttled." | https://docs.knock.app/in-app-ui/api-overview , https://docs.knock.app/in-app-ui/feeds/socket-behavior |
| Preferences | A `PreferenceSet` with `categories`, `channel_types`, `channels`, and `workflows`, plus `commercial_subscribed`. Anything not set defaults to opted in. Merge order: recipient, then tenant default, then environment default. Workflows can override preferences (for example password reset). Knock offers a hosted preference page or a custom UI built on its API. | https://docs.knock.app/preferences/overview |
| Batching / digests | A batch step groups triggers per recipient, optionally per key. Windows can be fixed, dynamic, sliding, or a rule (for example "weekly Monday 9am"). Templates get `total_activities`, `total_actors`, and up to 10 `activities` (Enterprise allows up to 100). The leading item can be flushed immediately. Knock itself says batching "isn't well-suited" for regular daily or weekly digests and recommends computing those from your own system at send time. Per-recipient batch windows are **not** supported, though window *rules* are evaluated in the recipient's time zone. | https://docs.knock.app/designing-workflows/batch-function |
| Quiet hours | Per-channel-step "send windows" (one entry per weekday, send or do_not_send, with from/until), evaluated in the recipient's `timezone` and falling back to the account default. Messages are queued until the next window opens. | https://docs.knock.app/designing-workflows/send-windows |
| Escalation | Step conditions such as "send the email only if the in-app message has not been seen after a 5-minute delay." | https://docs.knock.app/designing-workflows/step-conditions |
| Channels | Email providers include Postmark and SMTP (their integration pages exist). SMS providers include **Telnyx** and Twilio, so they match Fold's defaults. Knock does not bill for provider sends: the church still pays Postmark or Telnyx. | https://docs.knock.app/integrations/sms/overview , https://docs.knock.app/integrations/email/postmark , https://docs.knock.app/integrations/email/smtp , https://knock.app/pricing |
| Pricing | **Developer, $0/month: 10K messages/month**, unlimited workflows, channels, recipients, and in-app feeds, batching and preferences included, 30-day log retention. **Starter, $250/month**: 50K messages, then $0.005 each, and removes Knock branding. One message = one recipient on one channel, so an in-app item plus an email counts as 2. Going over the free tier isn't hard-capped; Knock contacts you about billing. | https://knock.app/pricing |
| Self-hosting | **None.** Knock is proprietary and runs only in Knock's cloud. No self-host or on-prem option appears on the pricing page (the Enterprise "dedicated infrastructure" option is still run by Knock). Third-party directories say the same. | https://knock.app/pricing , https://faun.dev/toolbox/knock/ (secondary) |
| Data residency | No EU or other region option is documented. Every subprocessor is listed in the USA (AWS for hosting, plus ClickHouse, Datadog, Sentry, Segment, PostHog, OpenAI, Anthropic, and others). The DPA covers EEA/UK transfers with SCCs. SOC 2 Type 2; HIPAA BAA on Enterprise. | https://knock.app/legal/subprocessors , https://knock.app/legal/data-processing-addendum , https://docs.knock.app/developer-tools/security |
| Client auth | Public key (`pk_`) in the browser and secret key (`sk_`) on the server. In production, "enhanced security mode" requires an RS256 JWT user token signed by your backend with Knock's signing key, optionally limited by UCAN-style grants. Proxying feed calls through your own backend is possible but "not recommended" because of latency. | https://docs.knock.app/in-app-ui/security-and-authentication |
| Data exposure | By default the feed API returns actors with all their custom properties, all trigger `data`, and the recipient with all custom properties. A per-channel "API response filter" is needed to limit this. | https://docs.knock.app/integrations/in-app/feeds |

### 2.2 CSP impact (does the in-app feed need Knock's JS, CDN, or websocket?)

- **No CDN script is required.** Knock's React SDK (`@knocklabs/react`, plus its stylesheet `@knocklabs/react/dist/index.css`) installs from npm, so Vite bundles it into Fold's own assets. `script-src 'self'` and `style-src 'self'` still work, assuming the stylesheet is imported at build time and the components don't inject inline styles **[unverified]**.
- **It does need direct browser connections to Knock**: `connect-src https://api.knock.app wss://api.knock.app`. The client SDK opens a Phoenix socket at `wss://api.knock.app/ws/v1`, passing `api_key` and `user_token` as query parameters. Sources: the `@knocklabs/client` source mirrored at https://cdn.jsdelivr.net/npm/@knocklabs/client@0.22.2/src/api.ts and a community AsyncAPI spec at https://github.com/api-evangelist/knock/blob/main/asyncapi/knock-asyncapi.yml (secondary).
- Avatar or actor images in feed items would need `img-src` for wherever they're hosted **[unverified; depends on the template]**.
- A **headless or proxied** setup avoids the extra origins: the Go backend calls Knock's REST API with the secret key and the SPA talks only to Fold. Knock supports this but discourages it, and you lose real-time updates unless Fold adds its own push.

### 2.3 Fit for Fold (single-tenant, each church brings its own accounts)

| Question | Assessment |
|---|---|
| Each church needs its own Knock account? | Yes, under DEC-2. Every church would sign up, create environments, and copy a public key, secret key, and signing key into Fold. Its workflows and templates also have to exist in *its* Knock account. Fold could push them with Knock's management API or CLI (https://docs.knock.app/mapi-reference/workflows), but that's a sync job Fold would own, and it breaks if a church edits templates in the dashboard. The alternative, one Knock account run by Grier with each church as a Knock "tenant", contradicts DEC-2 and DEC-10 and puts all churches' member data in one account Grier controls. |
| Cost | The free tier (10K messages/month) probably covers in-app-only use for a small church **[estimate]**. If email and SMS also go through Knock, broadcasts count too: one email to 400 members is 400 messages, so a few weekly announcements plus reminders could pass 10K. The next tier is $250/month, which isn't realistic for a small church. The free tier also shows Knock branding. |
| Duplicated logic | Fold already plans its own Consent (append-only, scoped), Suppression, recipient-local SMS send window with an 18-hour reminder pull-forward, guardian copies (email only), teen rules (TeenConsent for SMS), RFC 8058 unsubscribe, and an AuditLog. Knock's preferences, send windows, and commercial unsubscribe would be a second, parallel source of truth, and the two would drift. |
| Minors and privacy | Notification content about teens, contact requests, and guardian links would be stored in a US SaaS with many subprocessors (including AI providers). The feed returns recipient and actor properties unless filtered. That's a harder story for Fold's privacy page than "everything stays on your church's server." |
| Ops | It adds another account and set of secrets per church for Grier or the church to manage, adds a vendor outage mode, and makes local dev and CI need fakes for Knock as well as Postmark and Telnyx. |
| What it would buy | A polished feed UI, workflows that non-developers can edit, batching, real-time updates, and delivery logs. Most of the UI value can be copied (section 5). Real-time updates aren't important for Fold's notice volume. |

**Verdict on Knock:** don't adopt it. It's a good product for multi-tenant SaaS companies, but it fights three of Fold's founding rules: self-hosted, church-owned accounts with few dependencies (DEC-2), and Postgres only (DEC-1).

---

## 3. Alternatives

### 3.1 Novu (open source)

- **License:** the core is MIT. Anything under `enterprise/packages` is under a proprietary license that requires written approval and forbids modification and commercial hosting. Source: https://github.com/novuhq/novu (files `LICENSE-MIT`, `LICENSE-ENTERPRISE`, `EE-PACKAGES-LICENSE`, read 2026-10-10). Fold would have to make sure only community (MIT) parts are used.
- **Self-host stack:** the community Docker Compose file runs `redis`, `mongodb` (mongo 8.0), `api`, `worker`, `ws`, and `dashboard` (Novu 3.19.0 images) (https://raw.githubusercontent.com/novuhq/novu/next/docker/community/docker-compose.yml). For a single VM, Novu recommends **4 vCPU / 8 GB RAM for the Novu services, plus MongoDB (2 GB RAM, 20 GB disk), Redis (2 GB, AOF), and 10 GB of S3 storage**. For production it recommends multiple VMs and MongoDB Atlas M20+ (https://docs.novu.co/community/self-hosting-novu/overview). By default anyone who can reach the dashboard can sign up unless `DISABLE_USER_REGISTRATION=true` is set (https://docs.novu.co/community/self-hosting-novu/deploy-with-docker).
- **Features:** an Inbox component, a bell, preferences, digest and delay steps, multi-channel delivery, and a Go SDK (`novu-go`). Community self-host lacks some Cloud features, such as Snooze and removing branding (https://docs.novu.co/community/self-hosted-and-novu-cloud, from a search summary **[unverified in detail]**). Code-first workflows use `@novu/framework`, a Node "bridge" app (same deploy doc).
- **Cloud:** free tier of 10K workflow runs, US and EU residency, 24-hour activity retention; Pro from $30/month (https://novu.co/pricing). Novu Cloud has the same per-church-account problem as Knock.
- **Fit:** poor. Adding MongoDB, Redis, and four Node containers to a one-binary Go app breaks DEC-1 and "deployment simplicity is the top concern." It roughly quadruples the RAM needed on a small VPS **[estimate]**. Its Inbox would also need `connect-src` for Novu's API and websocket host (port 3002 by default, or a reverse-proxy path), unless Caddy serves it on the same origin.

### 3.2 Others, one line each

| Option | Note | Fit |
|---|---|---|
| Apprise | Python. | Excluded (no Python). |
| ntfy | Go, Apache-2.0 / GPLv2, self-hostable; sends push notifications to phones and desktops over pub/sub topics (https://github.com/binwiederhier/ntfy). | Not an in-app feed with per-user state. It could be an optional *push channel adapter* later; it isn't the store. |
| Courier, SuprSend, OneSignal, MagicBell | SaaS notification platforms with in-app inboxes **[unverified pricing and residency; not researched in depth]**. | Same objections as Knock. |
| Herald (`jeromesth/herald`) | A TypeScript headless library with Knock-like inbox states, using your own database (https://github.com/jeromesth/herald). | Wrong language, but a useful reference for API shape. |
| Fusio Pulse, Notifuse | Self-hosted send gateways or email platforms (https://www.fusio-project.org/pulse, https://github.com/Notifuse/notifuse). | Fold's provider adapters already cover sending. No in-app state. |

### 3.3 Build-it-ourselves baseline (recommended)

#### Principles

1. **Store notifications, one row per recipient (fan-out on write).** Churches have hundreds of users, not millions, so writes are cheap and reads are a single indexed query.
2. **Keep content and state together.** Render the title, body, and link at write time from Go templates, so later edits to a group don't rewrite history. For *task* items, the target's live state decides whether they're resolved.
3. **Stored, not derived, from M1.1b on.** Replace the derived approach with rows written by the same code paths that write `AuditLog`. Upcoming-event reminders become rows written by a job, with a unique key per occurrence (idempotent).
4. **In-app is always on. Email and SMS go through Fold's existing pipeline**: Consent, Suppression, the send window, and guardian copies already live there.
5. **No websockets.** Poll a cheap badge endpoint (ETag / 304) every 60 seconds while the tab is visible, and refetch on window focus. SSE on the same origin is a possible later upgrade (`connect-src 'self'` already covers it).

#### Schema sketch (goose migration, Postgres)

```sql
-- One row per recipient per notification.
CREATE TABLE notifications (
  id            bigserial PRIMARY KEY,
  user_id       bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  kind          text   NOT NULL,          -- registry key in Go, e.g. 'group.approval_needed', 'event.visibility_override'
  category      text   NOT NULL,          -- 'admin' | 'groups' | 'events' | 'messages' | 'safety' (drives preferences UI)
  priority      smallint NOT NULL DEFAULT 0, -- 1 = needs action (pinned section)
  actor_id      bigint REFERENCES users(id) ON DELETE SET NULL,
  subject_type  text,                     -- 'group' | 'event' | 'occurrence' | 'contact_request' | 'message_request' ...
  subject_id    bigint,
  group_key     text,                     -- batching key, e.g. 'join_request:group:42'
  dedupe_key    text,                     -- idempotency, e.g. 'reminder:occ:9912:user:7'
  title         text   NOT NULL,          -- rendered, plain text; no PII beyond display names (see minors)
  body          text   NOT NULL DEFAULT '',
  link          text   NOT NULL,          -- in-app path only (starts with '/'); validated
  actions       jsonb  NOT NULL DEFAULT '[]', -- [{id:'approve', label:'Approve', style:'primary', method:'POST', path:'/api/v1/...'}], max 2
  batch_count   int    NOT NULL DEFAULT 1, -- number of grouped activities
  data          jsonb  NOT NULL DEFAULT '{}', -- small, non-sensitive render data
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(), -- bumped when a batch absorbs a new activity
  seen_at       timestamptz,              -- shown in an open panel at least once
  read_at       timestamptz,              -- clicked or marked read
  archived_at   timestamptz,              -- dismissed (x); hidden from the feed, kept in "Archived"
  resolved_at   timestamptz,              -- task done (by anyone); shows "Done" and leaves the needs-action section
  expires_at    timestamptz               -- e.g. occurrence start + 1 day for reminders
);
CREATE UNIQUE INDEX notifications_dedupe ON notifications(user_id, dedupe_key) WHERE dedupe_key IS NOT NULL;
CREATE INDEX notifications_feed   ON notifications(user_id, archived_at, updated_at DESC, id DESC);
CREATE INDEX notifications_unseen ON notifications(user_id) WHERE seen_at IS NULL AND archived_at IS NULL;
CREATE INDEX notifications_open_batch ON notifications(user_id, group_key) WHERE read_at IS NULL AND archived_at IS NULL;

-- Per-user, per-category settings for channels Consent does not cover, and digest choice.
-- Email/SMS opt-in stays in Consent (PLAN 7.4); this table never overrides Consent.
CREATE TABLE notification_settings (
  user_id     bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  category    text   NOT NULL,
  email_mode  text   NOT NULL DEFAULT 'immediate', -- 'immediate' | 'daily' | 'weekly' | 'off' (off only if category not required)
  updated_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, category)
);

-- Outbound delivery of a notification (email/SMS), reusing the message pipeline statuses.
CREATE TABLE notification_deliveries (
  id              bigserial PRIMARY KEY,
  notification_id bigint REFERENCES notifications(id) ON DELETE CASCADE, -- null for a digest
  user_id         bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  channel         text   NOT NULL,        -- 'email' | 'sms'
  digest_id       bigint,                 -- groups digest deliveries
  status          text   NOT NULL,        -- queued|suppressed|deferred|sent|delivered|bounced|failed (same set as MessageRecipient)
  reason          text,
  scheduled_for   timestamptz NOT NULL,
  provider_message_id text,
  sent_at         timestamptz,
  UNIQUE (notification_id, channel)
);
```

Notes:
- **Kind registry in Go, not the database**: `notify.Register(Kind{Key, Category, Required bool, DefaultEmail, AllowSMS bool, BatchWindow, Render func})`. "Required" kinds (safety and contact access, account security, approvals you're responsible for) can't be turned off in-app, and their email can't be turned off either if they're transactional (PLAN 7.4).
- **Batching**: `notify.Send` first looks for an open row with the same `(user_id, group_key)` created within `BatchWindow` (for example 1 hour) that is unread and not archived. If it finds one, it increments `batch_count`, re-renders the title ("3 people asked to join Youth Group"), bumps `updated_at`, and **clears `seen_at`** so the badge picks it up again. Otherwise it inserts a new row. Use `SELECT … FOR UPDATE` on the open-batch index.
- **Task resolution**: when something is approved, rejected, or withdrawn, the handler runs `UPDATE notifications SET resolved_at = now() WHERE subject_type=… AND subject_id=… AND resolved_at IS NULL` in the same transaction. Other admins' copies then show "Done by Pat" instead of a stale Approve button. This also fixes the "2 → 3 groups need approval" problem.
- **Retention [proposal]**: a daily cleanup job deletes archived or read rows older than 90 days, unread rows older than 180 days, and anything past `expires_at`. AuditLog stays the permanent record. Notifications are a UI convenience, not evidence.

#### API (under `/api/v1`, CSRF on writes, session auth)

| Method & path | Purpose |
|---|---|
| `GET /notifications/badge` | `{unseen, needs_action}`. Cheap; returns an ETag, and 304 when nothing changed. Polled every 60 s while the tab is visible. |
| `GET /notifications?filter=all\|unread\|archived&before=<cursor>&limit=20` | The feed, sorted by `updated_at` then `id`, with a cursor. The needs-action items come back in a separate array. |
| `POST /notifications/seen` `{ids}` | Called when the panel opens (with the visible ids). |
| `POST /notifications/{id}/read` · `DELETE /notifications/{id}/read` | Mark read or unread. |
| `POST /notifications/read-all` | Mark all read (`read_at = now()` where null). Doesn't touch `archived_at`. |
| `POST /notifications/{id}/archive` · `DELETE …/archive` | Dismiss, or undo a dismissal. |
| `POST /notifications/archive-read` | Clear all read items. |
| Inline actions | Each action calls the *existing* domain endpoint (for example group approval). The notification only links to it, so policy checks and audit stay in one place. |
| `GET/PUT /me/notification-settings` | The preferences grid. Email and SMS toggles write `Consent` rows. Digest mode writes `notification_settings`. |

#### How it plugs into Fold's adapters

```
domain code (same tx as AuditLog) ──> notify.Send(ctx, tx, Kind, recipients, subject, data)
   ├─ insert/merge notifications rows (in-app, always)
   └─ for each recipient & channel the Kind allows:
        enqueue job 'notification.deliver' {notification_id, channel, not_before}
job runner (C7, SKIP LOCKED):
   notification.deliver:
     1. skip if read_at/archived_at/resolved_at set (escalation pattern: "email only if not seen after N min")
     2. resolve address; check Consent (scope category/group) + Suppression + teen rules (TeenConsent for SMS)
     3. SMS: apply send window (DEC-9/25) exactly as messaging does (shared function in internal/messaging)
     4. email_mode = daily/weekly -> do nothing now (digest picks it up)
     5. EmailSender.Send / SMSSender.Send via internal/providers; record provider_message_id
     6. guardian copies: apply the existing rule for non-transactional email to teens (DEC-19/29)
   notification.digest (scheduled per user-local time, e.g. 7:00 AM daily / Monday weekly):
     unread, unarchived, unresolved notifications since last digest, not already emailed -> one email
```

- **Quiet hours**: in-app notices are never held back; they're silent. SMS follows the church or recipient send window (DEC-9, DEC-25), using the same function as broadcasts. Email isn't limited, as in the current plan (7.5). Digests go out at a fixed local morning time.
- **Escalation default [proposal]**: for non-urgent kinds, delay the email 15 minutes and skip it if the user has seen or read the in-app item by then. This is Knock's "if not seen" condition implemented in our job runner.
- **Tests**: the `devoutbox` adapter captures email and SMS. Handler tests cover state transitions, batching, idempotency (`dedupe_key`), task resolution, and the rule that a user can only change their own rows.

#### Minors and consent

- Notifications go to people with logins only. Household people (no login) never get a row; the account holder does, worded "for Sam (your household)."
- Teens get in-app notifications once `account` consent is granted. A `pending_consent` teen sees only the waiting page, so no feed. Email and SMS go through the existing teen rules.
- A notification's title, body, and data must never include a teen's email or phone, or a hidden adult's contact details (DEC-31). Use display names and in-app links only; the target page enforces policy when it opens. Add a unit test that renders every kind with teen fixtures and fails on `@` or phone patterns.
- Guardian notices that the plan requires (for example a contact grant for their teen, 5.11) are their own kinds aimed at the guardian, not copies of the teen's feed.
- On user deletion, notification rows cascade-delete; AuditLog keeps the record.

#### Effort [estimate]

About 1–2 weeks for one developer familiar with the codebase: migration, `internal/notify` with the kind registry and batching, about 8 endpoints, a feed panel and `/notifications` page, a preferences page, the deliver and digest jobs, and tests. The in-app part (table, API, and UI without email or digests) could ship by itself in M1.1b, replacing the derived notices, with email delivery added when M1.2 and M3.x land.

---

## 4. Recommendation

**Self-build in Postgres. No vendor.**

1. **Fits the founding rules:** Postgres only (DEC-1), church-owned accounts with few external dependencies (DEC-2, D3, D6), one binary, no Python, CSP stays `'self'`.
2. **One source of truth** for consent, suppression, send windows, guardian copies, and teen rules. A vendor would add a second preferences system that has to be kept in sync.
3. **Privacy:** notification content (often about teens, contact access, and approvals) stays on the church's server.
4. **Small to build:** Fold's volume is low, the hard parts (delivery adapters, job runner, consent) are already planned, and Knock's own guidance says digests are best computed from your own data anyway.
5. **Keep a seam, not a vendor:** `notify.Send` is the single entry point. If a church ever needs push notifications, add an `ntfy` or web-push channel adapter behind it. No Knock or Novu adapter is planned unless a church asks (same "second adapter when needed" rule as 5.6).

Phasing suggestion for Codex:
- **M1.1b**: `notifications` table plus API plus the new bell, panel, and `/notifications` page. Stored rows replace derived notices. `archived_at` replaces localStorage. Task resolution for group approvals and deletions.
- **M1.2**: email delivery of selected kinds with the 15-minute "if not seen" delay, through the minimal sender.
- **M2.x**: event reminder rows (idempotent job) and waitlist notices.
- **M3.x**: preferences grid wired to Consent, SMS delivery with the send window, daily and weekly digests.

---

## 5. Knock UX patterns worth copying (vendor-neutral)

| Pattern | Knock's approach | Adopt in Fold? |
|---|---|---|
| Separate *seen* from *read* | Seen = shown in an open feed; read = clicked or marked read. They are independent timestamps. (https://docs.knock.app/send-notifications/message-statuses) | **Yes.** |
| Badge counts *unseen* | The default; opening the feed clears the badge "for less noise." (https://docs.knock.app/integrations/in-app/feeds) | **Yes**, plus a persistent marker for needs-action items so approvals don't vanish from view just because the panel was opened. *(Illuminator cross-ref: replaced by one plain count, see 6.1.)* |
| Mark all as read | A bulk action that doesn't count as a click. | **Yes.** |
| Archive with × | Archived items are hidden from the feed and can be fetched separately. | **Yes**, with an "Archived" filter on the full page and Undo. |
| Status filter | Filter all, unread, or read in the feed header. | Yes, on the full page: All / Unread / Archived. In the popover, at most an "Unread only" toggle. |
| Batching | "Jane left 8 comments", using a batch key per subject. (https://docs.knock.app/designing-workflows/batch-function) | **Yes**, through `group_key`. |
| Actionable items | Standard, single-action, or multi-action cells (at most 2 buttons); clicking the cell = `interacted`. | **Yes**, at most 2 buttons, only for low-risk reversible actions. *(Illuminator cross-ref: allow-list and deny-list in 6.6.)* |
| Escalate when ignored | Send email only if the in-app item isn't seen after a delay. (https://docs.knock.app/designing-workflows/step-conditions) | **Yes** (job condition). |
| Preferences grid | Rows by category, columns by channel type. (https://docs.knock.app/preferences/overview , https://docs.knock.app/preferences/custom-preference-center.md) | **Yes**, with locked cells for required items. |
| Feed vs. inbox | A feed is a popover; an inbox is a full page with filters. (https://knock.app/blog/an-intro-to-in-app-notifications) | Both: a popover for quick checks, `/notifications` for history and mobile. |
| Mobile | Sheet or full-screen layouts and swipe actions in Knock's design kit 2.0. (https://knock.app/blog/announcing-in-app-feed-notifications-design-kit-2) | Full-screen page on mobile. Swipe only as a shortcut, never the only way to do something. |
| Immutable content | Sent items aren't edited; archive and resend instead. | Mostly. Batches re-render while open, and tasks show "Done." |

---

## 6. Notification UX/UI guidelines — **DRAFT, reviewed by Illuminator (designer), 2026-10-10**

> DRAFT, designer pass done. Quill wrote the starting point from research; Illuminator (Fold's UI designer) reviewed and edited it. Meetup is the primary reference and ChurchTrac the secondary one (PLAN 1.7, item 7). Every change is marked **Illuminator:** inline and listed in 6.0. Technical questions are in 6.13 ("For Codex"), not decided here. The product questions in 6.14 ("For Big Boss/Grier") were **resolved by Grier on Oct 10, 2026**, who accepted every recommendation and confirmed that Fold builds notifications in-house (Knock is a UI/UX reference only). The research sections (0–5, 7, 8) are unchanged except for cross-references.

### 6.0 Illuminator changelog (designer review, 2026-10-10)

1. **The badge is one plain count** of items that are unread or still waiting on you. The amber needs-action dot is dropped, and needs-action items aren't cleared by opening the panel or by "Mark all as read". (6.1, answers open question 7.1.)
2. **Event reminders leave the bell.** They go by email and text, as on Meetup. The bell keeps event *changes*: time or place changed, cancelled, waitlist "you're in". Those archive themselves when the occurrence ends. (6.5a, answers 7.2.)
3. **An explicit inline-action allow-list and deny-list** (6.6, answers 7.5). Approving a group SMS request is **moved to the deny-list**: it sends a text that can't be taken back, costs money, may reach teens, and touches messaging permissions. "Approve a join request" is removed, because Fold has no join-request step (DEC-30). The nearest equivalent, an adult account request, is on the allow-list.
4. **States filled in**: offline, stale-on-error, resolved-by-me vs. resolved-by-someone-else, "already handled" conflicts, and archive with Undo that meets WCAG 2.2.1 (6.4).
5. **The panel is a disclosure popover, not a dialog.** There's no focus trap, which settles the "[decide modal vs. non-modal]" item (6.2, 6.9).
6. **Preferences simplified**: no In-app column (in-app is always on, like Meetup's Inbox), one email-timing choice instead of one per category, and Meetup's per-group settings are not in v1 (6.8).
7. **Visual spec tied to Fold's real UI**: the header in `Layout.tsx`, the current `Notifications.tsx` popover, the `brand` palette, and the `.btn-primary`/`.btn-secondary`/`.input` classes (6.1a, 6.3).
8. **Contrast fix**: the badge moves from `bg-brand` (#f64060, 3.61:1 with white) to `bg-brand-dark` (#d62e4f, 4.81:1). The current amber approval dot (`bg-yellow-500`, 1.92:1) is removed (6.1, 6.9).
9. **Overflow menu cut from v1**. "Mark as unread" and "Turn off notifications like this" are make-work, and the gear covers the second one (6.3).
10. **No welcome item for new users.** The empty state explains the bell instead (6.7).
11. **Teen, consent, and public-internet rules** for what a row and its email may say (6.3a).
12. **Meetup and ChurchTrac evidence** cited, with what couldn't be verified (6.12).
13. **Illuminator, 2026-10-10 (later):** Grier resolved all seven 6.14 questions (via Big Boss) and accepted every recommendation. 6.14 is marked resolved. 6.8 no longer has an email-timing choice: email in v1 is "Right away" only, and "Once a day" moves to Later. 6.1, 6.5a, 6.6, 6.8, and 6.13 now cite the decisions. Fold builds notifications in-house; Knock is a UI/UX reference only.

### 6.1 Bell and badge
- The bell sits in the top bar where it is today (`Layout.tsx`, between "Admin" and the avatar), drawn as an inline SVG (no icon fonts or CDNs). It shows only when signed in. Signed-out visitors see "Start a group" and "Log in" as now.
- **Illuminator: one count, one color.** The badge shows **N = unread items + unresolved items waiting on you**, with no double counting. It caps at "9+".
  - Opening the panel does **not** clear it. Opening an item, or "Mark all as read", clears ordinary items.
  - Items waiting on you stay in the count until someone resolves them, by you or another admin or leader. "Mark all as read" skips them on purpose; its tooltip says so.
  - *Replaces the Knock-style unseen badge plus amber needs-action dot.* Reasoning:
    - One number and one rule ("things you haven't opened, plus things waiting on you") is easier to explain to a church volunteer than seen vs. unseen plus a colored dot.
    - The amber dot meant something only by its color (WCAG 1.4.1). Amber on white is 1.92–2.15:1, below the 3:1 needed for non-text marks.
    - Fold's volume is low, especially once reminders leave the bell (6.5a), so a count that persists won't nag.
- **Illuminator: badge style.** `bg-brand-dark text-white text-xs font-bold`, `min-w-[1.25rem] h-5 px-1 rounded-full`, with `ring-2 ring-white` so it stays readable over the bell's outline. (`bg-brand` with white is only 3.61:1.)
- No badge at zero. When the count goes up, the badge does one 150 ms scale step, skipped under `prefers-reduced-motion`. No pulsing or shaking.
- The accessible name carries the meaning (6.9): "Notifications, 3 unread, 1 waiting on you."
- *Decided: Grier, Oct 10, 2026 (6.14.1).*

### 6.1a Illuminator: fit with Fold's visual language
- **Header** (`Layout.tsx`): white, `border-b border-gray-200`, `h-16`, `max-w-6xl`, nav text `text-sm font-medium text-gray-700 hover:text-brand`. The bell button follows suit (`text-gray-700 hover:text-brand`), with a 44×44 px hit area (`p-2.5` around a 24 px icon) and the app's visible focus ring (`focus-visible:ring-2 ring-brand ring-offset-2`).
- **Surfaces** reuse what exists: `bg-white rounded-xl border border-gray-200 shadow-lg` (today's popover), `divide-y divide-gray-100` rows, and `hover:bg-gray-50`.
- **Buttons** reuse `.btn-primary` and `.btn-secondary` at a compact size (`!py-1.5 !px-3 text-sm`), the same treatment as the header's Log in button. Text links use `text-brand font-semibold hover:underline`.
- **Brand pink is for one thing per row at most**: the unread dot or the primary button. Category icons are neutral (`text-gray-500` on `bg-gray-100`), so pink keeps meaning "new" or "act here", as on Meetup.
- **Type**: Inter or system (no web fonts); titles `text-sm`, meta `text-xs text-gray-500` (4.83:1). Never use `text-gray-400` for text (2.54:1).

### 6.2 Feed panel (desktop popover, ≥ 640 px)
- `w-96` (384 px), `max-w-[calc(100vw-2rem)]`, anchored right under the bell, `max-h-[70vh]`, scrolling inside. **Illuminator:** width set to Tailwind's `w-96`, up from today's `w-80`, so a title plus two buttons fits on one row.
- **Header**: "Notifications" (`font-bold text-sm`), a "Mark all as read" text button (disabled with `aria-disabled` when no ordinary items are unread), and a gear linking to `/settings/notifications` (accessible name "Notification settings").
- **Illuminator: "Waiting on you"** is the label for the pinned section (Fold's plain-language voice; it replaces "Needs your action"). Unresolved items come oldest first, at most 5, then "and 3 more →" linking to `/notifications?filter=waiting`. Below it comes **Recent**, newest first by last activity. Each section is hidden when empty.
- Load 20 items, then "Show more" (a cursor), not infinite scroll.
- **Footer**: "See all notifications" → `/notifications`.
- **Illuminator: a disclosure popover, not a dialog.** No focus trap. Esc, clicking outside, or the bell again closes it, and focus returns to the bell. Opening moves focus to the panel heading. Tabbing past the last control closes the panel and moves on in the page.

### 6.3 Item anatomy
1. **Leading visual** (36 px circle): the actor's avatar through the existing `Avatar` component (served by Fold), or a neutral category icon: group, calendar, shield for safety and contact, chat bubble for messages.
2. **Title**: plain language, up to 2 lines, names in `font-semibold` ("**Pat Lee** started a group: **Tuesday Men's Breakfast**").
3. **Body**: at most 2 lines, clamped (date, place, short context).
4. **Meta**: relative time ("5m", "Yesterday") in `<time datetime title>` with the absolute time, plus the group or event name as context.
5. **Unread**: a `w-2 h-2 bg-brand` dot on the leading edge (3.61:1 non-text, passes) and a `font-semibold` title, with visually hidden "Unread". *One unread signal, no extra tint or border.*
6. **Waiting on you**: inside its section the row is not tinted; it gets a `text-xs font-semibold text-gray-700` label "Waiting on you" next to the time, so the state is in text, not color.
7. **Actions** (optional): at most 2 compact buttons under the body, only from the allow-list (6.6). Primary on the left.
8. **×** archives (accessible name "Archive: <title>", 44×44 px hit area). **Illuminator: no overflow menu in v1.** "Mark as unread" and "Turn off notifications like this" are cut; the gear covers settings.
- The whole row links to `link`, and opening it marks it read. Buttons and × sit outside the link element (as tags do on `GroupCard`), so they never trigger it.

### 6.3a Illuminator: what a notification may say (teens, consent, public internet)
- **Names and in-app links only.** No email addresses, phone numbers, birth dates, care notes, or message bodies from or about a teen, and no hidden adult's contact details (DEC-31, matching 3.3 "Minors and consent").
- **Rows about a teen or child** show the existing "Teen" or "Child" badge next to the name wherever leaders, hosts, or admins see them (PLAN 5.x badging), and never carry inline actions (6.6).
- **Emails about sensitive kinds** (contact requests, guardian links, teen consent, anything about a minor) say only that something is waiting and link into Fold: "You have a contact request in Fold. Sign in to review it." Email gets forwarded and read on shared screens; the page enforces policy when it opens. (Rendering is Codex's call, 6.13.)
- **A teen's own feed** follows the same rules. When `guardian_copies` is on, their settings page says so plainly: "Your church sends your guardian a copy of church emails sent to you."
- Rows render as **plain text**. The only links are internal paths (6.11).

### 6.4 States
| State | Visual |
|---|---|
| Unread | Dot plus semibold title, hidden text "Unread". Counts in the badge. |
| Read | Normal weight, no dot. |
| Waiting on you | In the pinned section with the "Waiting on you" text label. Counts in the badge until resolved, whatever its read state. |
| Resolved by someone else | **Illuminator:** buttons are replaced by `text-sm text-gray-600` "Done · approved by Pat, 2h". Moves to Recent; leaves the badge. |
| Resolved by you | **Illuminator:** "You approved this · 5m" plus a navigation link to the target ("View group"), where it can be changed. v1 has no inline undo for actions. |
| Already handled (conflict) | **Illuminator:** if an inline action loses a race, the row shows "Already approved by Pat" in place of the buttons. It is not an error. |
| Action failed | Inline `text-sm text-red-600` message under the buttons ("Couldn't approve. Try again or open the request"), and the buttons stay. |
| Archived | Hidden from the panel; shown under the "Archived" filter on the full page. **Illuminator:** a toast "Archived · Undo" stays for 8 s and **pauses while hovered or focused** (WCAG 2.2.1). Focus moves to the next row, not to the toast. The Archived filter is the durable undo. |
| Expired / auto-archived | **Illuminator:** event-change items archive themselves when the occurrence ends (6.5a). Expired items never show in the panel. |
| Loading (first open) | 3 skeleton rows (`bg-gray-100 rounded`, `motion-safe:animate-pulse`), heading visible, `aria-busy="true"` on the list. |
| Loading more | "Show more" becomes "Loading…" (disabled); existing rows stay. |
| Error (nothing loaded) | "Couldn't load notifications." plus a "Try again" `.btn-secondary`. The header and the rest of the app keep working. |
| **Illuminator: stale on error** | If a refresh fails but rows were loaded earlier, keep them and show a slim `text-xs text-gray-500` line at the top: "Couldn't refresh. Showing notifications from 3:05 PM · Try again." The badge keeps its last value and doesn't animate. |
| **Illuminator: offline** | When the browser reports offline: "You're offline. Showing what was loaded at 3:05 PM." Inline actions and × are disabled (`aria-disabled`, title "Reconnect to do this"); opening rows still works. Polling resumes on the `online` event. |

### 6.5 Grouping
- Similar items about the same subject within the batching window become one row: "**Pat Lee** and 2 others joined **Youth Group**." The count is visible; the row links to the list behind it.
- A new activity on an unread group bumps it to the top.
- **Illuminator: these are never batched**: anything waiting on you, safety and contact items, cancellations. Each needs its own decision or attention (Meetup also sends join requests and cancellations "immediately … no batching", 6.12).
- No date headers in the popover. **Illuminator:** the full page groups by **Today** and **Earlier** only; "This week" adds a heading without helping at Fold's volume.

### 6.5a Illuminator: event items in the bell
- **Reminders don't go in the bell.** The 24 h email and 2 h text reminders (PLAN 7.7) stay email and SMS only, as on Meetup, whose help center lists event reminders as email and push, not Inbox (6.12). Upcoming events already live on the home page, in My groups, and on the calendar. Weekly-group reminders in the bell would bury the items that matter.
- **What does go in the bell**: an event you're going to (or waitlisted for) is **cancelled**, or its **time or place changes**, or you're **moved off the waitlist**. These are rare and need attention.
- Each event item **archives itself when the occurrence ends**, so the bell never shows yesterday's "time changed". It stays findable under Archived until retention removes it.
- This replaces today's derived "upcoming events" bell items (section 1), which are dropped. *Decided: Grier, Oct 10, 2026 (6.14.2).*

### 6.6 Actions
**Illuminator: the rule.** An inline button may change state only if all of these hold:
1. The decision is the viewer's to make.
2. The row shows everything needed to decide.
3. It involves no minor, consent, contact access, role, deletion, or outbound message.
4. It can be reversed elsewhere in Fold.

Everything else gets a navigation button ("Review", "View event", "Reply") that opens the full page, where policy, context, and audit live. The server re-checks policy on every action; showing a button grants nothing.

**Allow-list (v1, state-changing inline buttons):**
| Kind | Who sees it | Buttons | Why it's safe |
|---|---|---|---|
| New group pending approval | Admins | **Approve** · Review | Reversible (archive the group). Group name, leader, and description excerpt are in the row. Rejecting usually needs a note, so it's under Review. |
| Account request from an **adult** with no warnings (duplicate, rate-limit, or honeypot flags) | Admins | **Approve** · Review | Reversible (disable the account). The row shows name, email, source ("Self-signup" or "Invited by Sam"), and the first line of the message. Teen requests and flagged requests show only **Review**. Declining goes through Review for the optional note. *Decided: Grier, Oct 10, 2026 (6.14.3).* |

**Navigation-only buttons (always allowed):** Review · View event · View group · Reply (to a conversation) · Open request.

**Deny-list (never inline, always Review):**
- Anything about a **teen or child**: teen account requests, guardian links (DEC-32), teen consent, household changes, and any row with a Teen or Child badge.
- **Contact access** of any kind (DEC-31): requests where you're the subject, admin decisions on teen requests, grants, revocations. Sharing contact details is a consent decision; it deserves the full page.
- **Consent** changes of any kind (email, SMS, photos).
- **Roles and leadership**: leader grants, co-leader changes, event hosts, admin role.
- **Deletion**: deletion requests, removing people, removing registrations.
- **Outbound messages**: member-requested group SMS (PLAN 7.6). It can't be unsent, it costs money, it may reach teens, and approving can include editing the text. Also send-window overrides and broadcasts.
- **Visibility overrides** (DEC-28) and anything **bulk**.
- **RSVP changes** from a notification. "Can't go" can hand a waitlisted spot to someone else, which can't be undone; the event page shows the context.

**After an action**: the button shows a spinner and `aria-busy`, then the row shows the result ("Approved") and resolves. The result is announced in the polite live region. On failure, see 6.4.
- Labels are verbs ("Approve", "Review"), never "OK". No confirmation dialog for allow-listed actions, because they're reversible; the deny-list exists so that no confirm dialogs are needed.

### 6.7 Empty state
- Panel: a small local inline SVG (a folded paper, Fold's motif, `text-gray-300`), "You're all caught up," then one `text-xs text-gray-500` line: "Updates about your groups, events, and requests show up here." Then a "Notification settings" link.
- Archived filter with nothing in it: "Nothing archived."
- Waiting filter with nothing in it: "Nothing is waiting on you."
- **Illuminator: no welcome item** for brand-new users. The empty state's explainer line does that job, and it doesn't add a fake unread to the badge on the first sign-in.

### 6.8 Preferences page (`/settings/notifications`)
- **Illuminator: in-app is always on, so there's no In-app column.** Meetup's Inbox messages "can't be turned off" either (6.12). That removes 5 toggles that would only hide things in the bell.
- A short list with **rows by category** and **columns Email · Text**:
  - Waiting on you (admins and leaders only) · Groups I belong to · Event changes · Messages & replies · Safety & contact.
  - Each row has a one-line, plain-language description under its name.
- **Locked cells** show a lock icon and the reason in text ("Required: these are requests only you can handle"; "Required for your safety and privacy"). The cell's accessible name includes "Locked, on". Locked: Safety & contact (email), account and security mail (not listed; always sent), and Waiting on you (email) for the people responsible.
- The **Text column** is disabled with a reason until the phone is verified and SMS is opted in. For teens it also needs guardian SMS consent ("Texts need your guardian's OK"). A "Set up texting" link goes to the profile.
- **Illuminator: no email-timing choice in v1.** Email goes right away. A "Once a day" summary is **Later**, and weekly is cut. *Decided: Grier, Oct 10, 2026 (6.14.5).*
- **Illuminator: Meetup's escape hatch**, worded for Fold. At the bottom: "Turn off all optional email". It confirms what still arrives ("You'll still get account, safety, and request emails"). Required mail stays on. *Decided: Grier, Oct 10, 2026 (6.14.7).*
- A read-only quiet-hours line: "Texts are only sent between 8 AM and 8 PM in your time zone (America/Boise)." The zone name links to the profile's time-zone field and falls back to the church zone. In-app notices are silent and arrive any time. Email isn't held for quiet hours. *Decided: Grier, Oct 10, 2026 (6.14.6).*
- Toggles are real `<input type="checkbox" role="switch">` with visible labels. Each saves as it changes, with a polite "Saved" status next to the control (no toast). On failure, the switch reverts and "Couldn't save. Try again" appears.
- The same page opens from the email unsubscribe link (signed token, PLAN 7.4).
- **Illuminator:** no per-group settings in v1. Meetup has them (6.12), but they multiply the switches by the number of groups. *Decided: Grier, Oct 10, 2026 (6.14.4).*

### 6.9 Accessibility (WCAG 2.2 AA)
- **Illuminator:** the bell is a `<button aria-expanded aria-controls="notif-panel">` (no `aria-haspopup="dialog"`, because the panel is a disclosure, 6.2). Its accessible name includes the counts: "Notifications, 3 unread, 1 waiting on you". At zero it's just "Notifications".
- The panel is a labelled region (`<section aria-labelledby>`) with sub-headings for Waiting on you and Recent.
- The list is `<ul>`/`<li>`. Each item exposes visually hidden text for its state ("Unread", "Waiting on you", "Done"), so state is never shown by color alone.
- A polite `aria-live` region announces count *increases* at most once per minute ("2 new notifications") and action results ("Approved"). It never announces on every poll.
- Touch targets are at least 44×44 px (bell, ×, buttons) and at least 24 px apart (2.5.8). Visible focus rings. Contrast: text at least 4.5:1 and marks at least 3:1. The badge uses `brand-dark` (4.81:1).
- Keyboard: Enter opens an item; × and actions are reachable by Tab in reading order; Esc closes.
- Relative times have absolute equivalents (`<time>` with `title`).
- The toast's Undo is keyboard-reachable and the toast pauses while focused or hovered (2.2.1).

### 6.10 Mobile (< 640 px)
- The bell goes to **`/notifications`** (a full page) instead of a popover. Back returns you where you were. **Illuminator:** the mobile header already hides "Start a group" and the profile link (`hidden sm:…`), so the bell must stay visible next to "My groups" and "Log out" at 390 px.
- The full page has a sticky header (`bg-white border-b`): the title, filter chips **All · Unread · Waiting · Archived** styled like the home page's tag chips (pill, `aria-pressed`), and "Mark all as read". The Waiting chip shows only to admins and leaders.
- Rows are full-width with the same anatomy. Buttons wrap under the body; × stays top-right.
- Swipe-to-archive is allowed as a shortcut only; × stays visible.
- Polling stops while the tab is hidden (Page Visibility API) to save battery and data.

### 6.11 No CDN assets / CSP
- Icons and illustrations are inline SVG or local files in the bundle. Fonts are local or system.
- No third-party notification components or scripts. Avatars are served from Fold (`/uploads` or `/files`).
- Time formatting uses `Intl.RelativeTimeFormat` and `Intl.DateTimeFormat` (no date library from a CDN).
- Only same-origin network calls (`connect-src 'self'`). No websockets in v1.
- No inline `style=""` attributes that would need `'unsafe-inline'`; use Tailwind classes. (React `style={{}}` props are allowed under Fold's CSP, AGENTS.md, but aren't needed here.)
- Notification text renders as **plain text**, never HTML (no `dangerouslySetInnerHTML`). Links are internal paths only.

### 6.12 Illuminator: design references (what was verified)
- **Meetup, primary.** All of the following is from Meetup's help center and settings page, read 2026-10-10.
  - Meetup notifies by email, push, and "Inbox messages inside the app or website", and **Inbox messages can't be turned off**. Settings are layered: a global email master switch, account-level types (messages, replies), and **per-group topics** (event announcements, reminders, organizer messages). Essential account, privacy, and payment mail can't be turned off. https://help.meetup.com/hc/en-us/articles/40705340432525-How-Meetup-notifications-work , https://help.meetup.com/hc/en-us/articles/360002879591-Editing-my-email-settings
  - Per kind:
    - Join requests go out "immediately by email, push, and inbox (no batching)".
    - Cancellations are "sent instantly — not batched or delayed".
    - Likes, comments, and new-member notices "roll up" in the Inbox (new members up to 24 h).
    - **Event reminders** are scheduled (about 7 days, 1 day, and a few hours before) and "delivered by email and push". The Inbox isn't listed for them.
    - Several email versions wait about 15 minutes and are skipped if a push already went out.
    - Source: https://help.meetup.com/hc/en-us/articles/40708711818637-What-notifications-Meetup-can-send
  - The Email updates page lists account-level and per-group toggles, a "Send fewer emails" option (skip email when a push covered it), and "Turn off all email updates", which keeps legal and transactional mail. https://www.meetup.com/account/comm/
  - Push settings are app-only: a master switch plus category sliders. https://help.meetup.com/hc/en-us/articles/360002862972-Editing-my-push-notification-settings
  - Meetup's old v3 API distinguished *read* from *clicked* and returned an unread count (secondary source). https://meetup.hexdocs.pm/Meetup.V3.Notifications.html
  - **Not verified:** what Meetup's web bell and panel look like, including the badge style, whether opening clears the badge, and whether it has "Mark all as read". The help pages don't describe them, and the panel needs a signed-in session. The bell and panel visuals here follow Fold's existing popover, not a claimed Meetup layout.
- **ChurchTrac, secondary.**
  - Church Connect push notifications are **admin broadcasts** sent from the People › Messaging screen to groups or Smart Lists. Members need the app, OS permission, and a sign-in (which lasts 30 days). https://www.churchtrac.com/support/people/push-notifications , https://www.churchtrac.com/support/people/messaging
  - Admins choose who is notified when someone registers or updates a profile. https://www.churchtrac.com/support/connect/connect-settings
  - The member app's profile icon holds notification settings. https://www.churchtrac.com/support/connect/sign-in
  - I found **no member-facing in-app notification feed** in ChurchTrac's docs, so it offers no bell or panel pattern to copy. Its useful lesson is the admin side: routing "who gets told" to a chosen person.

### 6.13 Illuminator: For Codex (technical questions; not decided here)
1. **Badge semantics.** 6.1 counts unread plus unresolved items waiting on you, so the badge no longer needs `seen_at`. Decide whether to keep `seen_at` and `POST /notifications/seen` only for the "email if not seen after 15 min" escalation (3.3), or switch escalation to `read_at`. `GET /notifications/badge` should return both numbers (`unread`, `waiting`) for the accessible name.
2. **`read-all` must skip unresolved waiting items** (or the count must ignore `read_at` for them). Pick one so the server and UI agree.
3. **Reminders out of the bell** (6.5a, confirmed by Grier, 6.14.2): this changes the M2.x plan for reminder rows. Event-change and waitlist items need `expires_at` = occurrence end, with **auto-archive** rather than deletion, so they stay under Archived until retention.
4. **No batching** for waiting, safety, and cancellation kinds. That's a per-kind flag in the registry (`BatchWindow = 0`).
5. **Allow-list enforcement**: inline `actions` should be emitted by the kind registry only for allow-listed kinds and only when the subject is an adult with no flags. The UI must not be the gate. Reject an action on a non-allowed kind server-side even if a client sends it.
6. **Conflict response**: allow-listed actions use conditional updates. On a lost race, return a structured "already handled by X" (for example 409 with `resolvedBy`) so the row can show "Already approved by Pat" instead of an error.
7. **Offline and stale display**: keep the last-loaded feed **in memory only**, not localStorage. Fold is on the public internet and devices get shared, and logout must leave nothing behind (matches the existing logout-clears-storage rule).
8. **Polling cadence**: 3.3 says 60 s with ETag; today's bell uses 5 min. Either works for the design. The rule is a refresh on focus, on navigation, and after an inline action, and polling paused when hidden.
9. **Sensitive email rendering** (6.3a): kinds touching minors, consent, or contact access need an email variant that names nothing and links in. That could be a per-kind `EmailSafe` render.
10. **Preferences storage** (6.8): with no In-app column and no email timing in v1 (digests are Later, 6.14.5), `notification_settings` may shrink to per-category email on/off. The `notification.digest` job and `email_mode` can wait. Decide how "Turn off all optional email" maps onto `Consent` rows, and confirm locked categories can't be written.
11. **Quiet-hours line** needs the effective time zone (`User.timezone`, falling back to the church zone) on the settings payload.
12. **Teen transparency line** needs `guardian_copies` exposed to the teen's own settings payload (read-only).
13. **Mobile route**: `/notifications?filter=…` with back-navigation state, and the < 640 px switch from popover to page.

### 6.14 Illuminator: For Big Boss/Grier (product questions) — **RESOLVED, Grier, Oct 10, 2026 (via Big Boss)**
Grier accepted every recommendation and confirmed that Fold builds notifications in-house; Knock is only a UI/UX reference.
1. **Badge**: one plain count, including items still waiting on you, with no needs-action dot. (6.1)
2. **Reminders**: email and text only, not the bell. Today's upcoming-events bell items are dropped. (6.5a)
3. **Account requests**: inline Approve only for adult requests with no warnings. Anything else goes to Review. (6.6)
4. **Per-group notification settings**: not in v1. Categories only. (6.8)
5. **Email timing**: "Right away" only in v1. "Once a day" summaries move to Later; weekly is cut. (6.8)
6. **Quiet hours**: email ignores them. 8 AM–8 PM applies to texts only. (6.8)
7. **"Turn off all optional email"**: included. (6.8)

---

## 7. Open questions for Grier, Codex, and the designer

1. Badge semantics: unseen count plus a needs-action dot (recommended), or a count of unread? *(Illuminator: answered in 6.1, one plain count; confirmed by Grier, Oct 10, 2026, 6.14.1.)*
2. Should upcoming-event reminders appear in the bell at all, or only in email and SMS plus the calendar? They're the noisiest kind today. *(Illuminator: answered in 6.5a, not in the bell; confirmed by Grier, Oct 10, 2026, 6.14.2.)*
3. Retention periods (90 days for read, 180 for unread) are a proposal.
4. Is a daily or weekly digest wanted in v1, or only later? *(Illuminator cross-ref: decided by Grier, Oct 10, 2026: none in v1, "Once a day" is Later; 6.8 and 6.14.5.)*
5. Which inline actions are allowed (join approvals, group SMS requests)? *(Illuminator: answered in 6.6; Fold has no join requests (DEC-30), and group SMS requests are on the deny-list.)*

---

## 8. Sources

Knock
- Pricing and FAQ: https://knock.app/pricing
- In-app feeds overview: https://docs.knock.app/in-app-ui/feeds/overview
- In-app feed channel (statuses, badge, archive, response filter): https://docs.knock.app/integrations/in-app/feeds
- Message statuses: https://docs.knock.app/send-notifications/message-statuses
- Messages concept: https://docs.knock.app/concepts/messages
- In-app API and real-time service: https://docs.knock.app/in-app-ui/api-overview
- Socket behavior: https://docs.knock.app/in-app-ui/feeds/socket-behavior
- Security and authentication (public/secret keys, enhanced security, JWT): https://docs.knock.app/in-app-ui/security-and-authentication
- Handling interactivity: https://docs.knock.app/in-app-ui/feeds/handling-interactivity.md
- Preferences overview: https://docs.knock.app/preferences/overview
- Custom preference center: https://docs.knock.app/preferences/custom-preference-center.md
- Batch function: https://docs.knock.app/designing-workflows/batch-function
- Send windows: https://docs.knock.app/designing-workflows/send-windows
- Step conditions: https://docs.knock.app/designing-workflows/step-conditions
- Management API, workflows: https://docs.knock.app/mapi-reference/workflows
- SMS providers: https://docs.knock.app/integrations/sms/overview
- Email: https://docs.knock.app/integrations/email/overview , https://docs.knock.app/integrations/email/postmark , https://docs.knock.app/integrations/email/smtp
- Subprocessors: https://knock.app/legal/subprocessors
- DPA: https://knock.app/legal/data-processing-addendum
- Security: https://docs.knock.app/developer-tools/security
- API overview: https://docs.knock.app/api-reference/overview
- Blog, intro to in-app notifications: https://knock.app/blog/an-intro-to-in-app-notifications
- Blog, design kit 2.0: https://knock.app/blog/announcing-in-app-feed-notifications-design-kit-2
- Blog, build vs buy (Knock's view, favors buying): https://knock.app/blog/build-v-buy-notifications
- Client SDK source (websocket URL): https://cdn.jsdelivr.net/npm/@knocklabs/client@0.22.2/src/api.ts
- Community AsyncAPI spec (secondary): https://github.com/api-evangelist/knock/blob/main/asyncapi/knock-asyncapi.yml
- Third-party summary, "no self-host" (secondary): https://faun.dev/toolbox/knock/

Novu
- Repo and license files: https://github.com/novuhq/novu (`LICENSE-MIT`, `LICENSE-ENTERPRISE`, `EE-PACKAGES-LICENSE`)
- Community compose: https://raw.githubusercontent.com/novuhq/novu/next/docker/community/docker-compose.yml
- Self-host overview and requirements: https://docs.novu.co/community/self-hosting-novu/overview
- Deploy with Docker: https://docs.novu.co/community/self-hosting-novu/deploy-with-docker
- Self-hosted vs Cloud: https://docs.novu.co/community/self-hosted-and-novu-cloud
- Pricing: https://novu.co/pricing

Others
- ntfy: https://github.com/binwiederhier/ntfy
- Herald: https://github.com/jeromesth/herald
- Fusio Pulse: https://www.fusio-project.org/pulse
- Notifuse: https://github.com/Notifuse/notifuse
- Courier blog, notification center design (secondary UX reference): https://www.courier.com/blog/in-app-notification-center-design

Design references (Meetup, ChurchTrac): see 6.12 *(Illuminator cross-ref)*.

Fold
- `/workspace/fold-plan/PLAN.md` (DEC-1, DEC-2, DEC-9, DEC-19, DEC-25, DEC-29, DEC-31; sections 3, 5.5, 5.6, 7.4, 7.5, 10.1; M1.1b)
- `/workspace/fold-plan/repo/api_notifications.go`, `/workspace/fold-plan/repo/web/src/components/Notifications.tsx`
