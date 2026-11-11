# Fold milestones: Phase 3

### 14.6 Phase 3: Messaging

The full as-delivered text of the done milestones (slices, acceptance, tests) is in
[`provenance/delivered-phases-3-6.md`](../provenance/delivered-phases-3-6.md).

**M3.1 Jobs table and worker**
- Done (PR #201, #202).
- A `jobs` table and a worker pool in the binary (`internal/jobs`): `FOR UPDATE SKIP LOCKED` claims, backoff with jitter, `dead` after `max_attempts`, an expired lease taken back, and `unique_key` dedupe. `FOLD_WORKERS` and `FOLD_JOB_POLL` tune it (5.5).
- Daily and hourly housekeeping are jobs keyed by church date or UTC hour, queued at startup and on schedule; finished jobs are kept 30 days.
- Admin → System lists dead jobs in plain words with Retry and Dismiss, both audited (DEC-111). Admins also get a notice when a job dies (M7.5).

**M3.2 Provider registry, Postmark adapter, email webhooks**
- Done (PR #203, #204, #205).
- `internal/providers` holds the interfaces and registry; the devoutbox, SMTP and Postmark adapters live in `internal/mail`, chosen by `FOLD_EMAIL_PROVIDER` (5.6). Transactional and broadcast mail use separate Postmark streams.
- `/hooks/postmark/email` (HTTP Basic credentials, CSRF-exempt) queues `webhook.process` jobs that update deliveries, announcement recipients and `suppressions`; every send checks suppressions first (DEC-112).
- Admin → Users shows whose email bounces or was marked as spam, with an audited Clear (DEC-113).

**M3.3 Consent and unsubscribe**
- Done (PR #206, #207).
- Every optional email carries a footer, an email settings link and RFC 8058 one-click headers. `POST /u/unsubscribe/:token` (a signed token, no cookie, no CSRF token) turns that kind of email off at once (DEC-114).
- `/email-settings/:token` (no sign-in) and Me → Notifications share one component: a switch per optional kind plus "Turn off all optional email" (DEC-115). Each change appends a `consents` row.
- The church mailing address admin setting fills the email footer (DEC-116).

**M3.4 Composer**
- Done for email (PR #208, #209, #211). The SMS parts are not built (last bullet).
- Messages (`/messages`) and the composer (`/messages/new`, `/messages/:id`): TipTap (lazy-loaded) with a button block and merge fields, bluemonday sanitizing, an HTML email template with a plain-text part, preview, "Send a test to me", and scheduling in the church's zone.
- The audience picker offers only what `policy.CanMessageAudience` allows, with live counts, skipped reasons and Teen badges (7.2, DEC-20).
- Images in email come later (DEC-118).
- Not built: the SMS segment counter, automatic prefix and footer, donation-ask warning and SMS tab. M3.4b built them hidden; they were later removed, unused, with the rest of the never-enabled SMS code. They are planned with the SMS tab in M3.7, with their two acceptance items (DEC-117, DEC-120).

**M3.5 Send pipeline**
- Done (PR #214, #215).
- `message.send` rechecks the author's rights at send time and snapshots recipients (dedupe, consent and suppression filters, age rules, guardian copies) with reasons. `message.deliver_batch` sends email in batches of 50, From "<author> via <church>" with Reply-To the church office setting (DEC-130).
- A leader or host may send at most 5 messages a day to one group's or one church-wide event's people; admins have no limit.
- The message page shows each recipient's status in words; "Try again" resends only to the people a send failed for, each checked again (DEC-131, DEC-132). Admin → Settings has the guardian copies switch and "Where replies go".
- Not built yet: the privacy page's line saying whether guardian copies are on (M4.3), and an admin notice when an announcement fails (DEC-111 planned it with M3.5; no milestone owns it now).

**M3.6 Email reminders**
- Done (PR #216, #217).
- The hourly `reminders.sweep` and `reminder.send` email each going registrant before a date, and waitlisted people a note, as authorless `reminder` messages under the Event updates switch. They create no bell rows (DEC-44, DEC-133).
- Every event form has a "Reminder email" choice: off, 2 hours, 1 day (the default), 2 days or 1 week before.
- The date page tells the event's managers when the reminder goes or went and to how many, opening its history. Reminders are read-only and appear in the admin Messages list under "Automatic reminders".

**M3.7 Telnyx SMS, inbound, keywords, revocation**
- Depends on: M3.5, M3.11
- Scope: Telnyx adapter (send, inbound and status webhooks in the CSRF-exempt group, signature check), SMS opt-in flow with the phone ownership check (code or YES reply) and confirmation text, the keyword list and automatic phrase opt-outs from 7.4, caps on HELP and automatic replies, the read-only opt-out log, and the SMS relay of replies (7.8). The composer's SMS tab (DEC-117), with the segment counter, automatic prefix and footer, and donation-ask warning (moved here from M3.4), and a "Giving page link" admin setting that always sets off the donation warning (DEC-120). The schema already has the SMS values (`sms` in the consent, suppression, delivery and recipient channels, `messages.sms_body`, the `sms` teen consent scope); there is no SMS code today.
- Acceptance:
  - [ ] Each of `STOP`, `STOPALL`, `QUIT`, `END`, `REVOKE`, `OPT OUT`, `OPTOUT`, `CANCEL`, `UNSUBSCRIBE` (any case, surrounding punctuation ignored) opts out of all SMS and is recorded; `START` opts back in; `HELP` replies with the help text
  - [ ] "Please stop texting me" opts the person out at once and gets the confirmation text, with no admin step; it appears in the read-only opt-out log; texting `START` opts back in (DEC-27)
  - [ ] No SMS goes to anyone without an opted-in consent row
  - [ ] Ticking SMS opt-in sends a code or a "Reply YES" text; `Consent` becomes `opted_in` and `phone_verified_at` is set only after the code is entered or YES comes back from that number; at most 3 verification texts per number per day
  - [ ] HELP gets at most 1 reply per number per 10 minutes, each number gets at most 5 automatic replies a day, and a repeated STOP from an opted-out number gets no reply
  - [ ] Teens get SMS only with both `TeenConsent` scope `sms` and their own opt-in; household people never
  - [ ] Other replies reach the author of the person's latest message (last 7 days) in-app and by email with a relay Reply-To, never showing the number; the author's answer goes back by SMS from the church number; with no recent message, the reply goes to the admins
  - [ ] (from M3.4) 160 GSM-7 characters = 1 segment; 161 = 2 segments; 70 UCS-2 code units = 1 segment and 71 = 2 segments (one emoji counts as 2 units) (counts include the auto prefix and footer), with one table of cases run by Go and TypeScript
  - [ ] (from M3.4) An SMS mentioning giving or donations (the 7.3 word list, or a link to a giving page: a giving word in the address or a giving service) shows the donation-ask warning (DEC-3); the "Giving page link" always does (DEC-120)
- Tests: contract suite; keyword table tests; webhook signature tests; segment table tests.

**M3.8 SMS send window, caps, SMS reminders**
- Depends on: M3.7, M3.6
- Scope: the recipient-local send window (DEC-9, DEC-25), SMS per-author and per-recipient limits (the email per-author limit is built, M3.5), daily caps per registration path (DEC-3), SMS reminders (7.7), which check each date's effective visibility and exact-date access the way email reminders do (M1.13, `reminders.go`). It replaces M3.13's interim copy of the send-window function (`internal/push/window.go`, fixed at 8 AM to 8 PM) with the shared one and its admin settings.
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
- Deferred until a church demands it (DEC-140). Kept as the scope record for that future milestone.
- Depends on: M3.8
- Scope: a second SMS adapter behind the same interface, including Twilio's sole-proprietor and toll-free paths in the registration flow.
- Acceptance:
  - [ ] Passes the SMS contract suite and the keyword tests
  - [ ] Switching from Telnyx to Twilio keeps every opt-out (Fold's own suppression list)
- Tests: contract suite against a Twilio fake.

**M3.11 One-to-one messages and reply relay**
- Depends on: M3.5, M1.11
- Scope: 7.8 (DEC-31): `Conversation` and `ConversationMessage`; in-app one-to-one messages from leaders and hosts; "Message the organizers" on event pages; relay Reply-To addresses on every email Fold sends for an author (until then, announcements reply to the church office address, `reply_to_address`, DEC-130); the Postmark inbound webhook at `/hooks/postmark/inbound` (CSRF-exempt, verified); the reply MX record documented (the wizard adds it in M4.4).
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
- Partly done by other milestones:
  - Notification email and push already go through a job, `notify.deliver`, which `notify.Send` queues in the change's transaction (M7.5). It sends at once; the 15-minute escalation below is not built.
  - Me → Notifications already has the email switches with "Turn off all optional email" (M3.3b, DEC-97, DEC-115), and the device card with a Push switch per optional kind (M3.13b, M3.13c1). The email unsubscribe link opens the same email component on `/email-settings/:token` (M3.3b).
- Scope: the rest of 5.12's delivery:
  - The 15-minute "skip if seen or read" escalation for non-urgent kinds, in `notify.deliver`.
  - SMS delivery for kinds marked with a new `AllowSMS` flag (kinds have `AllowEmail` and `AllowPush` today), through Consent, Suppression, teen rules, and the shared send-window function.
  - The `/settings/notifications` page (research 6.8), growing Me → Notifications (DEC-97): rows by category with Email, Text and Push columns, locked required cells, "Turn off all optional email" (writes `Consent` opt-outs for every optional category and leaves required mail on), the quiet-hours line with the effective time zone, and the teen `guardian_copies` line.
  - The email unsubscribe link opens the same page.
  - Per DEC-44: email is "Right away" only, with no email-timing choice and no digests (both later, M6.16). There are no per-group settings in v1. Email ignores quiet hours.
- Acceptance:
  - [ ] Email and SMS toggles write `Consent` rows; a locked category can't be turned off (API refuses) (the email half is built: a required kind is refused with 400 `email_kind_unknown`)
  - [ ] A non-urgent notification read within 15 minutes sends no email; an unread one sends exactly one, even after restarts
  - [ ] SMS for a notification follows the recipient's send window and is never sent without an opted-in consent (and, for teens, `TeenConsent`)
  - [x] "Turn off all optional email" writes one `Consent` opt-out row per optional category (source `web_form`) and leaves account, safety, and request emails on; turning a category back on appends an opt-in row (M3.3b)
  - [ ] A notification email sent at 11 PM recipient-local goes at once (no quiet hours for email); its SMS waits for the send window (the email half is built)
  - [ ] The settings payload has the effective time zone and, for teens, read-only `guardian_copies`
- Tests: job tests with a fake clock; consent mapping tests; Playwright for the settings page.

**M3.13 Web push**
- Done (PR #218, #219, #220, #221, #222, #223, #224), except the real-device check below. How it works: `docs/agents/push.md`, DEC-121 to DEC-129, [`research/web-push.md`](../research/web-push.md).
- VAPID keys are made at boot and kept encrypted in settings. Each device is a `push_subscriptions` row. `internal/push` speaks RFC 8030/8291/8292 with the standard library to allow-listed push hosts, and `push.deliver` is one job per device.
- The service worker (`/sw.js`), the manifest, "Notifications on this device" on Me → Notifications, the public directions page (`/notifications/setup`) and the church's `push_enabled` switch.
- "Push notification too" on announcements, and `/messages/:id/read` for anyone an announcement was sent to (DEC-123, DEC-135). The 2-hour push reminder never shows the place (DEC-136).
- Optional pushes wait for quiet hours and stop at 20 an hour (DEC-126). `fold push keys show|rotate` and `fold push check` manage the keys and test the push hosts.
- Still open:
  - [ ] Best effort, listed in `docs/FIRST-CHURCH-CHECKS.md` (DEC-128): proven on a real iPhone (iOS 16.4 or newer, Home Screen app), Android Chrome, desktop Safari and Edge on Windows, with the iOS version recorded
