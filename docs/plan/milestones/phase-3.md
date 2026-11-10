# Fold milestones: Phase 3

### 14.6 Phase 3: Messaging

**M3.1 Jobs table and worker**
- Depends on: M0.1
- Scope: 5.5. Worker, polling every 1 to 2 seconds, retries, `unique_key`, and an admin "System" page listing dead jobs with a retry button. Move the daily ticker's tasks onto jobs. Two slices (14.1):
  - **M3.1a Jobs table and worker** (built): migration 00051 `jobs`, `internal/jobs` (enqueue with `unique_key`, `FOR UPDATE SKIP LOCKED` claims, backoff with jitter, `dead` after `max_attempts`, an expired lease is taken back), `FOLD_WORKERS` and `FOLD_JOB_POLL`. The daily tasks (`occurrences.extend`, `contact.sweep`, `cleanup.login_throttles`, `notifications.cleanup`, `accounts.expire_requests`, `accounts.age_rollover`, `jobs.cleanup`) are jobs queued once per church day under `<kind>:<church date>`, at startup and at church-local midnight; finished jobs are kept 30 days.
  - **M3.1b System page** (built): Admin → System lists dead jobs in plain words with Retry and Dismiss, audited (DEC-111).
- Acceptance:
  - [x] Two app processes never run the same job twice (`FOR UPDATE SKIP LOCKED`; a test with 2 workers and 200 jobs)
  - [x] A newly queued job starts within 2 seconds
  - [x] A failing job retries with backoff and ends `dead` after `max_attempts`
  - [x] An admin sees dead jobs on the System page and can retry or dismiss them; nobody else can (M3.1b)
- Tests: unit tests for backoff and dedupe; Postgres concurrency test.

**M3.2 Provider registry, Postmark adapter, email webhooks**
- Depends on: M3.1, M1.2
- Scope: `internal/providers` interfaces (5.6) and registry; move `devoutbox` and SMTP behind it; Postmark adapter (DEC-21); the `/hooks/postmark/email` handler in the CSRF-exempt group, verified by Postmark's webhook authentication. Three slices (14.1):
  - **M3.2a Registry and Postmark sending** (built): `internal/providers` (email types, `SendError`, the registry), the devoutbox, SMTP and Postmark adapters in `internal/mail` registered by name, `FOLD_EMAIL_PROVIDER` and the `FOLD_POSTMARK_*` settings; every send returns the provider's message id, and notification deliveries record it.
  - **M3.2b Webhooks and suppressions** (built): the `suppressions` table, `/hooks/postmark/email` (HTTP Basic credentials Postmark sends, 401 otherwise), events queued as `webhook.process` jobs deduplicated by Postmark's id, deliveries and bounces updating `notification_deliveries`, and sending checking suppressions (DEC-112). `MessageRecipient` arrives with M3.5 and is updated the same way.
  - **M3.2c Seeing and clearing suppressions** (built): Admin → Users marks a person whose email bounces or who marked Fold's mail as spam, with Clear, audited (DEC-113).
- Acceptance:
  - [x] Swapping providers is a config change only (env for now; the wizard in M4.4)
  - [x] Postmark, SMTP, and devoutbox pass the shared contract suite against `httptest` fakes built from vendor docs
  - [x] A webhook with bad credentials (Postmark doesn't sign webhooks; it sends HTTP Basic credentials) returns 401 and changes nothing
  - [x] Bounces and complaints update `notification_deliveries` and `Suppression` now, and `MessageRecipient` from M3.5
  - [x] Transactional and broadcast mail use separate Postmark message streams
  - [x] An admin sees who is suppressed and why, and can clear it (M3.2c)
- Tests: contract and webhook tests with recorded sample payloads.

**M3.3 Consent and unsubscribe**
- Depends on: M3.2
- Scope: 7.4 email side: `Consent`, `Suppression`, preferences page with signed tokens, RFC 8058 headers, the CSRF-exempt one-click `POST`. Two slices (14.1):
  - **M3.3a Links, headers and one-click** (built): signed unsubscribe tokens (person and kind of email, from `SESSION_SECRET`); they never expire on purpose, so an old email's button still works, and changing `SESSION_SECRET` ends them along with every session; the one-click header goes out only when `SITE_URL` is https (or in dev); every optional email gets a footer (kind, email settings link, the church's address when set) and `List-Unsubscribe` / `List-Unsubscribe-Post` from every adapter; `POST /u/unsubscribe/:token` turns off that kind of email at once (DEC-114).
  - **M3.3b Email settings page** (built): `/email-settings/:token` without sign-in and Me → Notifications share one component with a switch per optional kind plus "Turn off all optional email" (DEC-115); the "Church mailing address" admin setting (DEC-116).
- Acceptance:
  - [x] A one-click POST (no cookie, no CSRF token) to the unsubscribe URL opts out; a tampered token returns 404 and changes nothing
  - [x] Transactional mail still sends after opting out of announcements
  - [x] Every consent change is an appended row with source and time
  - [x] Without signing in, the email settings page shows and changes the person's email kinds (M3.3b)
- Tests: header tests; token tamper tests.

**M3.4 Composer** (built, except the SMS donation warning, which waits for texting: DEC-117)
- Depends on: M3.3, M0.4
- Scope: 7.3. TipTap editor, bluemonday sanitizing, SMS counter, audience picker with live counts and teen counts, preview, test send, scheduling, merge fields. Three slices (14.1):
  - **M3.4a Messages, audiences and permissions** (built): migration 00054 `messages`, `policy.CanMessageAudience`, the PLAN 7.2 audiences with live counts (skipped reasons, teen count), sanitized drafts, scheduling and cancelling; email only until texting is set up (DEC-117).
  - **M3.4b Content** (built): HTML email (a mobile-safe template, a generated plain-text part) in every adapter, merge fields, preview and "Send a test to me", and the SMS segment counter and donation-ask warning (Go and TypeScript, one table of cases), built though the SMS tab stays hidden (DEC-117).
  - **M3.4c The composer page** (built): Messages (`/messages`, in the nav for people who can compose) and the composer (`/messages/new`, `/messages/:id`), opened from a group's Email members and an event's or date's Email people going; TipTap (lazy-loaded) with a button block and Insert field, the audience picker with live counts and Teen badges, preview, test send and scheduling in the church's zone; the SMS tab is built and hidden (`smsEnabled`, DEC-117); images in email wait until after M3.5 (DEC-118).
- Acceptance:
  - [x] 160 GSM-7 characters = 1 segment; 161 = 2 segments; 70 UCS-2 code units = 1 segment and 71 = 2 segments (one emoji counts as 2 units) (counts include the auto prefix and footer) (`sms-cases.json`, run by Go and TypeScript)
  - [x] `<script>`, `on*` attributes, and `javascript:` links are stripped
  - [x] Leaders see only own groups and own events in the picker, and hosts see only events they host; the API refuses other targets
  - [x] The recipient preview shows Teen badges and a teen count (DEC-20) (the API in M3.4a; the page in M3.4c)
  - [ ] An SMS mentioning giving or donations (the 7.3 word list, or a link to a giving page: a giving word in the address or a giving service) shows the donation-ask warning (DEC-3) (the check is built and tested in M3.4b; the warning shows in the SMS tab, hidden until texting is set up, DEC-117; the giving-page setting comes with it, DEC-120)
- Tests: segment table tests; sanitizer tests; policy tests; Playwright compose-and-schedule test.

**M3.5 Send pipeline** (built)
- Depends on: M3.4, M1.5, M1.10
- Scope: 7.1, 7.2, and the email side of 7.5: audience snapshot, dedupe, filters, guardian copies, batches, rate limits, scheduled sends, history page. Two slices (14.1):
  - **M3.5a The pipeline** (built): migration 00056 `message_recipients` (and `messages.error`); `message.send` (send-time policy check, snapshot with reasons, guardian copies, batches of 50) and `message.deliver_batch` (each email rendered for its reader, From "<author> via <church>", Reply-To the church office address, DEC-130; retries, then failed rows); delivery and bounce reports on recipients; the daily `messages.reap` backstop; the per-author daily limit (5 per group per day; admins none). Email has no provider token bucket: Postmark meters its own API, and the jobs pool bounds concurrency; SMS throughput arrives with M3.8.
  - **M3.5b History and controls** (built): the recipient history (`GET /messages/:id/recipients`: counts by status and reason, the first 200 names with Teen badges, each person's status in words, guardian copies as a count for leaders and hosts and by name for admins (DEC-132), each person's reason for admins only, never addresses; a provider's own error text reaches only admins, others read plain words, DEC-52), shown on the message's page with a summary line that follows a send every 5 seconds until it settles; "Partly sent" and "Failed" on the list; "Try again" for the people a send failed for (DEC-131; `POST /messages/:id/retry`, author or admin, the author's rights and each failed person's place in today's audience checked again (a removed or departed member, a cancelled registrant or a guardian whose link ended is skipped, #261), audited `message.retried`); and the admin settings for guardian copies and where replies go (Admin → Settings, each audited). A send with no email service set up fails at once in words instead of retrying for an hour, so Try again works once one is set up.
- Acceptance:
  - [x] A person in two chosen groups gets one copy
  - [x] Opted-out and suppressed people are skipped and counted with reasons
  - [x] A leader demoted between scheduling and sending is refused at send time
  - [x] Disabled accounts are never recipients, even though their memberships and registrations are kept (DEC-38)
  - [x] Teens without `account` consent and all household people get nothing; messages about a household person's registration go to the account holder (DEC-8)
  - [x] With `guardian_copies` off (default), guardians get no copies; with it on, each confirmed guardian gets one marked copy per message to their teen (DEC-19); no copy is ever sent by SMS (DEC-29)
  - [x] A scheduled message can be cancelled until sending starts
  - [x] A provider outage retries and then shows a failed state with the error (the API in M3.5a; the page in M3.5b)
  - [x] "Try again" resends to just the people a send failed for, never twice to anyone it reached (M3.5b)
- Tests: pipeline tests with devoutbox; failure injection.
- Deferred: the privacy page saying whether guardian copies are on (M4.3), and admin notices for failed sends (with the System page notices, DEC-111).

**M3.6 Email reminders** (built)
- Depends on: M3.5
- Scope: 7.7, email part (SMS reminders arrive with M3.8). Reminders create no bell rows (DEC-44). Recheck M1.13 effective date visibility and live exact-date invitation authority at scheduling and delivery; never expose hidden series/group metadata. Two slices (14.1):
  - **M3.6a Sending reminders** (built): migration 00057 (`events.reminder_email_minutes`, and `messages.occurrence_id` and `reminder_key`); the hourly `reminders.sweep` (the scheduler now queues hourly tasks too) and `reminder.send`, which write the going reminder and the waitlist note as authorless `reminder` messages and send them through the M3.5 pipeline under the Event updates switch, rechecking who may still see the date; `reminderEmailMinutes` on events (DEC-133).
  - **M3.6b The reminder controls** (built): the "Reminder email" select under More options in every event form (create a group event, create a church-wide event, Edit series), a series setting; on the date page, for the event's managers only (`reminders` on the date read), "Reminder emailed to 12 people · yesterday at 7:00 PM" and "Waitlist note emailed to 3 people", each opening its history, "Reminder goes out Tue, Nov 3 at 7:00 PM" before it goes, or "Reminders are off for this event"; the event's managers may read a reminder's page and history under the leader rules and press Try again on a failed one (`policy.CanSeeMessageHistory`, named `CanReadMessage` until M3.13d2; the date must still be ahead, and each failed row is checked again for who may still see the date); reminders are read only (`policy.CanEditMessage`); and reminders, marked as automatic with their date, in the admin Messages list under their own "Automatic reminders" chip (`category`, `reminder`, `?category=`), while a leader's list keeps only what they wrote. The event page's date rows don't repeat the lines: the date page is where a date's details live (DEC-133 D says "event or date page").
- Acceptance:
  - [x] Each going registrant gets exactly one 24-hour email reminder per occurrence, even after restarts (`unique_key`)
  - [x] Cancelled occurrences cancel their pending reminders
  - [x] Per-event settings can turn reminders off or change times (the API in M3.6a; the form in M3.6b)
  - [x] Sending a reminder creates no `notifications` row
  - [x] A leader sees on the date page that a reminder went out, and to how many (M3.6b)
- Tests: scheduler tests with a fake clock.

**M3.7 Telnyx SMS, inbound, keywords, revocation**
- Depends on: M3.5, M3.11
- Scope: Telnyx adapter (send, inbound and status webhooks in the CSRF-exempt group, signature check), SMS opt-in flow with the phone ownership check (code or YES reply) and confirmation text, the keyword list and automatic phrase opt-outs from 7.4, caps on HELP and automatic replies, the read-only opt-out log, and the SMS relay of replies (7.8). Showing the composer's SMS tab (DEC-117) with a "Giving page link" admin setting that always sets off the donation warning (DEC-120).
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
- Scope: the recipient-local send window (DEC-9, DEC-25), per-author and per-recipient limits, daily caps per registration path (DEC-3), SMS reminders (7.7). It replaces M3.13's interim copy of the send-window function with the shared one.
- Acceptance:
  - [ ] With the default window (8 AM to 8 PM), a 7:30 PM church-time send reaches a recipient whose profile is in the church zone at once, and a recipient whose profile zone is 2 hours ahead the next morning at 8 AM their time
  - [ ] Recipients without a saved zone use the church zone; guests with a browser-captured zone use it (DEC-58)
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
- Status: deferred until a church demands it (DEC-140). Kept below as the scope record for that future milestone.
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

**M3.12 Notification preferences and SMS delivery**
- Depends on: M3.3, M3.8
- Scope: the rest of 5.12's delivery:
  - Move notification email onto the `notification.deliver` job, with the 15-minute "skip if seen or read" escalation for non-urgent kinds.
  - SMS delivery for kinds marked `AllowSMS`, through Consent, Suppression, teen rules, and the shared send-window function.
  - The `/settings/notifications` page (research 6.8), growing the early Me > Notifications switch (DEC-97): rows by category with Email, Text and Push columns (Push arrives first, in M3.13c), locked required cells, "Turn off all optional email" (writes `Consent` opt-outs for every optional category and leaves required mail on), the quiet-hours line with the effective time zone, and the teen `guardian_copies` line.
  - The email unsubscribe link opens the same page.
  - Per DEC-44: email is "Right away" only, with no email-timing choice and no digests (both later, M6.16). There are no per-group settings in v1. Email ignores quiet hours.
- Acceptance:
  - [ ] Email and SMS toggles write `Consent` rows; a locked category can't be turned off (API refuses)
  - [ ] A non-urgent notification read within 15 minutes sends no email; an unread one sends exactly one, even after restarts
  - [ ] SMS for a notification follows the recipient's send window and is never sent without an opted-in consent (and, for teens, `TeenConsent`)
  - [ ] "Turn off all optional email" writes one `Consent` opt-out row per optional category (source `web_form`) and leaves account, safety, and request emails on; turning a category back on appends an opt-in row
  - [ ] A notification email sent at 11 PM recipient-local goes at once (no quiet hours for email); its SMS waits for the send window
  - [ ] The settings payload has the effective time zone and, for teens, read-only `guardian_copies`
- Tests: job tests with a fake clock; consent mapping tests; Playwright for the settings page.

**M3.13 Web push** (built after M3.6 and before M3.7; DEC-121 to DEC-129)
- Depends on: M3.1, M3.3 (slices a–c2); M3.5 and M3.6 (slice d)
- Scope: 5.12's push channel (DEC-43), the browsers' own push services with no vendor: VAPID keys made on first boot, `push_subscriptions` per device, RFC 8030/8291/8292 in `internal/push` with the standard library, the `push.deliver` job, a service worker and manifest served by the binary, the Push column on the notification settings page, the device directions page, the composer's companion notification and the 2-hour push reminder. The device directions copy and the design notes are in [`research/web-push.md`](../research/web-push.md). The slice that needs each repo edit makes it: the AGENTS.md "Web push" paragraph and rules-table row (a), `docs/API.md` and `.env.example`'s optional `FOLD_PUSH_HOSTS` (b), `docs/MEMBERS.md`'s "Get notifications on your phone" section and `web/e2e/ui-pages.ts` (c2). Five slices (14.1):
  - **M3.13a Keys, subscriptions and the push client**: the next free migration (`push_subscriptions`; `push` in `consents.channel` and `notification_deliveries.channel`; Down refuses once push rows exist, as 00044's does). `message_recipients` arrives with M3.5, so `messages.push_alert` and `push` in `message_recipients.channel` go in M3.13d's migration, or in M3.5's if M3.5 lands first. `vapid_public_key` and `vapid_private_key_enc` settings written under the migrations lock, encrypted with a `SESSION_SECRET`-derived key; `fold push keys show|rotate`, `fold push check`; `internal/push` (aes128gcm, ES256 VAPID, the host allow-list, a 3,000-byte plaintext cap, no redirects, resolved-IP checks); `push.deliver` as one job per device (`unique_key` `push:n:<notification_id>:d:<device_id>`; the payload holds device ids, never endpoints), 404/410 deleting the device. A `SESSION_SECRET` change that regenerates the keys writes an admin notice ("Notifications on devices were reset; members need to turn them on again"); a running server reloads the key on rotation. The AGENTS.md "Web push" paragraph and rules-table row land here.
  - **M3.13b Subscribe API and notification delivery**: `GET /push/config`, `PUT|DELETE /push/subscriptions`, `POST /push/test`; `Kind.AllowPush` (default false) and a per-kind short `PushRender` (safe text for safety, consent and contact kinds; never the bell body cut to 120 characters, never `Data`, `ManageURL` or `VideoURL`); `push.deliver` queued after commit for `AllowPush` kinds; TTL and Urgency per kind; `/me/notification-settings` gains `push`; the logout request itself carries this browser's endpoint and deletes it, and disabling the account, a password change or revoked sessions delete the user's devices; new error codes with `ERROR_COPY` (DEC-61): the unknown push service 400, the push-only 400, and the 403s for a guest, a `pending_consent` teen and push turned off by the church; "Send a test notification" allows 10 an hour per person and past that answers 200 with "You just sent a few tests. Try again in a few minutes." (DEC-129). `docs/API.md` and `.env.example` change here.
  - **M3.13c1 Service worker, manifest, icons and the settings card**: `/sw.js` (push, notificationclick, pushsubscriptionchange; no fetch handler; served `Cache-Control: no-cache`; not linked from `index.html`, so `bundle-size.mjs`'s entry budget does not count it), `/manifest.webmanifest` with the church name (`site_name`) and the Fold icon, `worker-src 'self'; manifest-src 'self'` in the CSP; icons made from the existing `fold-icon-1.svg`: PNG 192, 512, 512 maskable and apple-touch 180; the "Notifications on this device" card on Me → Notifications with the eight states (unavailable, install first, ready, on, on elsewhere, blocked, signed-out app, church off), Push switches, "Turn on notifications", "Send a test notification", "Turn off notifications on this device"; badge count on the icon.
  - **M3.13c2 Directions page and admin switch**: `/notifications/setup` (public route; the in-app list is `/notifications`) with the iPhone/iPad, Android, computer and Mac Safari steps and their undo steps (DEC-127); the `push_enabled` switch in Admin → Settings; `docs/MEMBERS.md`'s section and `web/e2e/ui-pages.ts`.
  - **M3.13d Announcements and reminders**: "Push notification too" in the composer (default on), "N also get a push notification" in the summary, push rows in `message_recipients` and history, a push following push consent rather than the email row (DEC-122), and the read page a push opens, `/messages/:id/read`, for anyone it was sent to, with leaders seeing push per person (DEC-135); the 2-hour push reminder per occurrence with its per-event switch, following the same out-of-window rule as SMS reminders (7.7), its lock screen the title and time only, never the place (DEC-136); quiet hours (DEC-126). A 1,000-recipient announcement queues one job per device; the worker pool caps concurrency, and no per-host limiter is needed in v1.
- Acceptance:
  - [x] The RFC 8291 Appendix A vector encrypts byte for byte; a fake push service decrypts every payload Fold sends and sees `TTL`, `Urgency`, `aes128gcm` and a VAPID token that verifies against the public key
  - [x] An endpoint whose host is not on the allow-list, or that resolves to a private, loopback or link-local address, is refused at subscribe time and again at send time, with no connection made
  - [x] 404 and 410 delete the device and record `suppressed` "device gone"; 429 and 5xx retry with the jobs backoff and never send twice to a device that already accepted
  - [x] Nothing in logs, job errors, audit rows or API responses contains an endpoint URL, `p256dh`, `auth` or a token (`TestPushLogsKeepSecretsOut`)
  - [x] Every kind's push rendering passes the teen and hidden-adult fixtures: safety, consent and contact kinds say only "Open Fold to review an update"; no push carries an email, phone, birth date, care note, teen badge, manage link or video link
  - [x] A `category:<cat>` push opt-out skips optional kinds of that category and never a required one; turning the first device on writes `(push, all, opted_in)`; turning it on again after "Turn off notifications everywhere" writes a new opt-in; `(push, all, opted_out)` overrides every per-category row and deletes every device; a guest or `pending_consent` teen gets 403
  - [x] An announcement with the push notification on sends each recipient with a device one email and one push, even across a restart; push-only is refused (400); the push body is the subject plus 120 characters of plain text (M3.13d2)
  - [x] A reminder push creates no `notifications` row (DEC-44); a cancelled occurrence cancels its pending reminder push (M3.13d3)
  - [x] With `push_enabled` off, no job sends, the card and `/notifications/setup` are hidden, and subscribe answers 403 (`push_enabled` stays default on)
  - [x] Playwright with a stubbed `PushManager`: the card moves ready → on, posts the right body, offers the test notification, and turns off; the blocked state shows the undo steps; `/notifications/setup` and the card pass a11y and layout at 390 and 1280 px
  - [x] Optional pushes outside the recipient's window wait for it; required kinds and changes to events starting within 24 hours go at once; past 20 optional pushes an hour the extras are `suppressed` "hourly limit" and the bell still has them
  - [x] Logout deletes this browser's device in the same request (`TestLogoutEndsSession` has a case); disabling the account, a password change and revoked sessions delete the user's devices
  - [x] An endpoint posted by a second user is rebound to that user
  - [x] `/sw.js` is served `Cache-Control: no-cache`, and the CSP carries `worker-src 'self'; manifest-src 'self'`
  - [x] `TestOnlyLoginAndInvitesAnswer429` stays green: the test-notification limit answers 200 with its own line (DEC-129)
  - [x] `fold push keys rotate` deletes every device; a running server uses the new key without a restart
  - [x] A payload over the 3,000-byte plaintext cap is refused before any network call
  - [x] The new error codes have `api_errors.go` entries and `ERROR_COPY` lines (DEC-61) (slice b's are in; the push-only 400, `message_push_only`, came with d2)
  - [ ] Best effort, listed in `docs/FIRST-CHURCH-CHECKS.md` (DEC-128): proven on a real iPhone (iOS 16.4 or newer, Home Screen app), Android Chrome, desktop Safari and Edge on Windows, with the iOS version recorded
- Tests: vector and fake-service tests in `internal/push`; allow-list and DNS-rebinding tests; registry privacy tests; job idempotency with a fake clock; handler tests; Playwright for the card and directions.

