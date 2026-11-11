# Fold design: 7. Messaging

## 7. Messaging pipeline

Email announcements, the composer, the send pipeline, email and push reminders, and web push are
built. SMS (M3.7 to M3.9), member SMS requests (M3.9), one-to-one messages and the reply relay
(M3.11), and SMS for notifications (M3.12) are planned. How mail reaches a provider, email choices
and suppressions are in [`docs/agents/mail.md`](../../agents/mail.md); push is in
[`docs/agents/push.md`](../../agents/push.md); endpoints are in [`docs/API.md`](../../API.md).

### 7.1 Flow

```
Composer (SPA) -> POST /api/v1/messages (a draft), PUT to edit, POST /messages/:id/schedule
   -> policy.CanMessageAudience(author, audience)   [403 message_not_yours if not allowed]
   -> status=scheduled, scheduled_at = the chosen time (or now)
   Planned (M3.9): a plain member's request becomes a MessageRequest instead [7.6]
Job message.send (at scheduled_at):
   1. Re-check the author's rights (roles may have changed since drafting)
   2. Resolve the audience into a recipient snapshot (message_recipients rows);
      addresses stay inside the worker, and authors see names and counts only (DEC-31)
   3. Dedupe by user (someone in two chosen groups gets one copy)
   4. Filter (recipientFacts): not active (disabled, a teen waiting for account consent),
      a guest outside a reminder, no address, an open suppression, the person's email choice
      -> rows marked suppressed with a reason (the author sees counts; admins see reasons)
   5. Guardian copies (only if guardian_copies is on, DEC-19, DEC-29): for each teen
      recipient, an email copy row for each confirmed guardian, filtered the same way
   6. Planned (M3.8): the SMS send window (DEC-9, DEC-25) defers each SMS recipient
      to their next allowed time
   7. Queue message.deliver_batch jobs (email in batches of 50) and, when push_alert is on,
      one push.deliver per device of each recipient who has push on (DEC-122, DEC-123)
      Planned (M3.7): SMS rows and their sends
Job message.deliver_batch:
   - each queued row is claimed (FOR UPDATE SKIP LOCKED), its email choice is checked again,
     and it is rendered for its reader
   - From "<author> via <church>" over the church's sending address; Reply-To is the
     "Where replies go" setting (reply_to_address, DEC-130). Planned (M3.11): a relay Reply-To (7.8)
   - stores provider_message_id and marks the row sent
   - a temporary error retries the batch with backoff; a permanent error, or the last
     attempt, marks the row failed with the error text; no email service fails it at once
   - the last batch settles the message as sent or failed; messages.reap is the daily backstop
   - Planned (M3.8): an SMS throughput limiter from adapter capabilities. Email has none:
     Postmark meters its own API and the jobs pool bounds concurrency.
Webhook POST /hooks/postmark/email (outside the CSRF group, csrfExemptRoutes; C17):
   - checks the HTTP Basic credentials Postmark sends (401 and no change if wrong;
     404 when FOLD_POSTMARK_WEBHOOK_SECRET is unset)
   - queues each event as a webhook.process job, deduplicated by Postmark's id
   - delivered / bounced / complained -> notification_deliveries, message_recipients, suppressions
   Planned (M3.7): /hooks/telnyx/... for SMS status, inbound and opt-out.
   Planned (M3.11): /hooks/postmark/inbound for replies.
```

The recipient list the author sees before sending, and the history page after, show Teen badges and a teen count (DEC-20).

### 7.2 Audiences

`messages.audience` is one of:

- `{"type":"occurrence","occurrenceId":123,"statuses":["going","waitlisted"]}`
- `{"type":"event_series","eventId":45,"statuses":["going"]}`: the series' upcoming dates
- `{"type":"events","eventIds":[45,46,47]}`: "a group of events" in Grier's words (for example all Advent events), at most 50
- `{"type":"group","groupId":7,"roles":["member","leader"]}`
- `{"type":"all_members"}`: admins only; every active account
- `{"type":"users","userIds":[...]}`: admins only, at most 500

Event audiences take `going`, `maybe` and `waitlisted`, and default to going and waitlisted; they reach scheduled upcoming dates only. A group audience defaults to members and leaders (`messages_audience.go`).

Leaders can target only their own approved groups and those groups' events (DEC-88). Hosts of church-wide events can target only registrants of the events they host (DEC-7). Teens can't send. The check (`policy.CanMessageAudience`) runs when the message is saved and again at send time.

A registration for a household person resolves to the account holder who registered them. Household people have no address of their own (DEC-8, DEC-23).

Guests appear only in event audiences, because that is where they registered. Announcements skip them ("guest, hasn't asked for updates"); only reminders reach them (7.4).

### 7.3 Composer (F6)

- The composer is `/messages/new` and `/messages/:id`, listed at `/messages` for admins, leaders of approved groups and hosts (`policy.CanComposeMessages`). It opens from a group's Email members and an event's or date's Email people going. It writes email only. **Planned (M3.7):** an SMS tab, shown once an SMS provider is set up (DEC-117), so a message can have both bodies and go out on both channels.
- Email: TipTap rich text (MIT license, React, lazy-loaded) with bold, italic, links, headings, lists, and a button block. The server sanitizes the HTML with `bluemonday` (`internal/content`) and renders it into a simple, mobile-safe template with the church name, an unsubscribe link, and the church's postal address when set (DEC-116). A plain-text part is generated automatically. **Planned (DEC-118, no milestone yet; #365):** images, uploaded through `Store` as public assets.
- **Planned (M3.7):** SMS is plain text with a live character and segment counter. It detects GSM-7 vs UCS-2 (one emoji switches to UCS-2: 70 characters per segment instead of 160). It shows an estimated cost from the provider's per-segment rate (an admin setting, pre-filled from section 12; DEC-117). The church name prefix ("First Baptist: ") and the "Reply STOP to opt out" footer are added automatically and counted.
- The audience picker shows only what the author may target, with live counts after filtering ("142 people; 9 won't get it: 6 turned off messages from leaders, 3 have no email address"). Leaders and hosts see that a person won't get it; only admins see each person's reason.
- Push notification too (default on, `push_alert`): people who turned push on also get a short notification (the subject and 120 characters; tapping it opens `/messages/:id/read`). The summary says "N also get a push notification". An announcement never goes push-only (DEC-123).
- Preview, and "Send a test to me".
- Schedule: send now, or pick a date and time up to a year ahead (church time zone). Scheduled messages can be edited or cancelled until sending starts.
- Merge fields: `{{first_name}}`, `{{event_title}}`, `{{event_time}}`, `{{event_link}}`, `{{manage_link}}` (`internal/content/merge.go`). On `/messages/:id/read` the event fields carry today's values only as far as the reader may still see the event or date; otherwise they read empty and links read as their text (`readerEventValues`).
- **Planned (M3.7, DEC-3, DEC-120):** the SMS wording check. The composer warns when an SMS asks for money ("give", "donate", "tithe", "offering", a link with a giving word in its address, a known giving service, or the church's "Giving page link" setting). Carriers and providers treat donation asks as charity traffic, which can need 501(c)(3) proof (Twilio error 30487) or a different campaign type; Telnyx lists fundraising and donations under its Charity use case. Fold assumes churches are not 501(c)(3)s, so it keeps SMS to event and group logistics. Put giving asks in email.

### 7.4 Consent, opt-out, STOP handling

Email:

- Required mail always sends: account mail (password resets, invitations, verification links, a guest's confirmation) and required notices (safety kinds and tasks waiting on the person, DEC-97). Other notices, such as "You're in" off the waitlist, follow the event updates switch. With Postmark it goes on the transactional message stream; announcements and reminders go on the broadcast stream.
- Optional email has one switch per kind (DEC-115): group updates, event updates (reminders too, DEC-133), messages from leaders (announcements, DEC-119) and admin notices. Each choice is an appended `consents` row (channel `email`, scope `category:<kind>`); no row means on. There are no per-group settings in v1 (DEC-44). The one reader is `readEmailOff` (`email_optouts.go`): the account's latest choice, or an opt-out retained for the destination address through an account erasure (`email_address_optouts`), turns a kind off. The choice is checked when the audience is counted, at the snapshot, and again for each row at send.
- Guests get required mail and reminders for the event they registered for, nothing else.
- Every optional email has a footer (its kind, the email settings link, the church's address) and `List-Unsubscribe` plus `List-Unsubscribe-Post: List-Unsubscribe=One-Click` headers (RFC 8058); the one-click header goes out only over https (or in dev). The link opens `/email-settings/:token` without sign-in. The one-click `POST /u/unsubscribe/:token` is outside the CSRF group and is authorized by the signed token alone (C17); it turns off that kind of email (DEC-114). Links never expire and end when `SESSION_SECRET` changes. Turning email back on needs a current session or a link bound to the account's current address (`email_unsubscribe.go`, `docs/agents/mail.md`).
- Unsubscribes take effect at once. (CAN-SPAM allows up to 10 business days; we do better.)

SMS (**Planned (M3.7)** unless marked otherwise):

- SMS is off for a person until they opt in **and prove they own the number**. Either they tick a checkbox on their profile or signup form that shows the exact consent text and then confirm with a 6-digit code texted to the number (entered in Fold) or a "Reply YES to confirm" text, or they text a keyword (`START`, `JOIN`) from the phone itself. `Consent` becomes `opted_in`, and `phone_verified_at` is set, only after the code or the YES comes back from that number. Verification texts are limited to 3 per number per day. The consent text, time, IP, and user agent are stored in `Consent`.
- On opt-in, the app sends one confirmation text: church name, what messages to expect, "Msg & data rates may apply", "Reply HELP for help, STOP to opt out".
- Opt-out keywords. An inbound message that is one of these words or phrases (any case, ignoring spaces and punctuation around it) opts the person out of **all** SMS from this church at once: `STOP`, `STOPALL`, `QUIT`, `END`, `REVOKE`, `OPT OUT`, `OPTOUT`, `CANCEL`, `UNSUBSCRIBE`. The FCC treats "stop", "quit", "end", "revoke", "opt out", "cancel", and "unsubscribe" as revocation per se (47 CFR 64.1200(a)(10)). `START`, `UNSTOP`, `YES` opt back in. `HELP` and `INFO` get the help reply, at most once per number per 10 minutes; each number gets at most 5 automatic replies of any kind per day, and a repeated STOP from a number that is already opted out gets no reply. Providers and carriers may also act on some keywords; Fold records them anyway, so its own suppression list stays correct if the church switches providers. Fold then sends one confirmation text ("You're unsubscribed from <church> texts. Reply START to resubscribe.") and nothing else.
- Revocation in other words (DEC-27). The FCC rule (47 CFR 64.1200(a)(10)) also counts any reasonable way of saying "stop". Fold applies these **automatically and at once**, with no human review:
  - A reply that reasonably reads as an opt-out request (for example "please stop texting me", "remove me", "no more texts", "take me off this list", "don't text me") opts the person out of all SMS immediately and gets the same confirmation text. The phrase list lives in `internal/consent` and errs toward opting out.
  - There is no review queue and no timer. A person opted out by mistake texts `START` to opt back in.
  - Admins see a read-only **opt-out log** (who, when, the matched word or phrase, the message). Each opt-out is also an `AuditLog` and `Consent` row.
  - Opting out stops all SMS from the church, so it already covers the FCC's "revoke all unrelated messages" rule, whose effective date the FCC delayed to January 31, 2027 (FCC DA 26-12).
- Other inbound texts go through the SMS relay in 7.8.

Minors (DEC-8, DEC-19):

- Children under 13 have no account and are never messaged. Messages about their registrations go to the account holder who registered them.
- Teens (13 to 17) receive email and push only once `account` consent makes their account active (push also needs their own device opt-in, DEC-122). **Planned (M3.7):** they receive SMS only when **both** of these are true:
  - a `TeenConsent` row with scope `sms` is granted (by a confirmed guardian, or by an admin with evidence; the leader role never grants consent)
  - the teen has opted in to SMS themselves
- **Planned (M3.7):** revoking either consent stops SMS at once, and an imported phone number for a teen is never treated as opted in.
- Guardian copies are off unless an admin turns on `guardian_copies` (7.1 step 5, 5.10).

Push:

- Push is off until a person turns it on for a device in Fold (the browser's own permission prompt, after a tap). That writes `Consent(push, all, opted_in)`. Per-category switches and "Turn off notifications everywhere" append rows like email. Devices are removed on logout, on 404/410, on key rotation, and when the account is disabled, its password changes or its sessions are revoked. Teens need an active account, not a separate consent; guests never subscribe (DEC-122).

### 7.5 SMS send window, rate limits, bounces

- SMS send window (**Planned (M3.8)**, DEC-9, DEC-25; SMS only, email is not limited):
  - Default: send only between **8:00 AM and 8:00 PM in the recipient's local time**. Admins can change it in Settings.
  - The recipient's local time comes from `User.timezone`, falling back to the church zone. Guest forms capture the browser zone too (DEC-58); guests use the church zone only when none was saved.
  - A message to many people is split by time zone. Each recipient's copy waits until their window opens. The composer shows "N recipients will get this later (outside their send window)".
  - If an admin sets a window that starts before 8:00 AM or ends after 9:00 PM, Fold shows a warning before saving. The federal TCPA rule bars telephone solicitations before 8 a.m. or after 9 p.m. in the called party's local time (47 CFR 64.1200(c)(1)), and some states are stricter. The admin must tick "I understand" to save, and the change is audited.
  - Override for one urgent message (for example "service cancelled, snow"): admins only. It needs a reason and shows the same warning when the send time is outside 8 AM to 9 PM. It is audited.
  - Replies Fold must send (STOP and HELP answers, opt-in confirmations) are not delayed.
  - This is not legal advice (DEC-14).
- Rate limits:
  - Per author: a leader or host may send at most 5 emails to one group's people (or one church-wide event's) per church day (`dailyMessagesPerGroup`); Try again counts too. Admins have no limit but are audited. **Planned (M3.8):** 3 SMS blasts per group per day. These limits are fixed constants, not settings.
  - **Planned (M3.8):** SMS provider throughput from adapter capabilities and the church's registration tier (for example a 10DLC brand's daily T-Mobile cap; see 12.2), and at most 1 non-transactional SMS per person per hour across all groups (default), with extras deferred.
- Bounces and complaints (DEC-112, DEC-113): a hard bounce adds the address to `suppressions` and stops all email to it until an admin clears it. A spam complaint suppresses the address for optional and broadcast mail; required mail still goes. Soft bounces are only recorded. Admin → Users marks a person "Email bouncing" or "Marked our email as spam", with Clear (audited).
- Push: optional pushes follow the same recipient-local window as SMS (reminders as in 7.7); required kinds and changes to events starting within 24 hours are exempt. Until M3.8 adds the setting, the window is fixed at 8 AM to 8 PM (`push.DefaultWindow`). At most 20 optional pushes per person per hour; extras are dropped (`suppressed`, reason "hourly limit"), and the bell still has them (DEC-126).
- **Planned (M3.7):** SMS failures. Carrier "unreachable" or "landline" errors mark the phone number invalid. Carrier filtering errors alert the admins, because they usually mean a registration or content problem.

### 7.6 Member-requested group SMS (F9)

**Planned (M3.9).**

1. A member opens their group, clicks "Request a text to the group", writes the text, and may propose a send time.
2. A `MessageRequest` is created with `status=pending`. Every leader of the group and every admin gets an in-app notification and an email.
3. Any one of them can approve (optionally after editing; the edit is recorded), reject with a note, or let it expire. It expires at the proposed time, or after 72 hours if no time was given.
4. Approval creates a normal `Message` that records the requester and `approved_by`. It is sent under the usual consent, send-window, and rate-limit rules. The requester hears back either way.
5. The first decision wins. The approve call is a conditional update (`WHERE status='pending'`), so two approvers cannot send it twice.

### 7.7 Reminders

- Automatic reminders per date for `going` registrants:
  - Email (`events.reminder_email_minutes`): 1 day before by default; leaders choose Off, 2 hours, 1 day, 2 days or 1 week before, per event (DEC-133). Guests get it too.
  - Push (`events.reminder_push`, on by default): 2 hours before, to people with push on for event updates and a device. Its lock screen shows the title and time, never the place (DEC-136).
  - **Planned (M3.8):** SMS 2 hours before (off by default; opted-in people only).
- Waitlisted people get a "still on the waitlist" email at the same time as the going reminder (DEC-133). A promotion off the waitlist is a "You're in" notice (5.12).
- Outside the recipient's window, a push reminder moves to the last allowed minute before it, if that is no more than 18 hours earlier (for example 7:59 PM the night before a 9:00 AM event). Otherwise it is skipped ("outside quiet hours"), and the email reminder still goes. **Planned (M3.8):** SMS reminders follow this same rule, written once here (`push.Window.LastAllowed`; 7.5, DEC-126).
- Reminders are authorless `category=reminder` messages sent through the 7.1 pipeline, so they appear in history and respect consent. The hourly `reminders.sweep` job queues `reminder.send` for each date at its time. The date page tells the event's managers when a reminder goes or went, and to how many (DEC-133).
- Reminders go by email, push and (planned) SMS only. They never create bell notifications. The bell carries event *changes* (cancellations, time or place changes, "you're in"), not reminders (5.12, DEC-44).

### 7.8 One-to-one messages and reply relay (DEC-31)

Fold sits between leaders and the people they reach, so nobody's personal address appears in headers or on screen unless they chose to share it (5.11).

Built now: every announcement is From `"<Author name> via <church name>"` over the church's own sending address, and its Reply-To is the church office address from "Where replies go" (`reply_to_address`), or none when that is empty (DEC-130). No personal address appears in From, Reply-To, To, or Cc.

**Planned (M3.11)** unless marked otherwise:

- **In-app**: a leader or host opens "Message" on a person in their own group or event. This starts a `Conversation` tied to that group or event. Members answer in the thread, and can use "Message the organizers" on an event they can see, which reaches its leaders or hosts without naming them. Admins see every conversation in message history. Guardian copies apply to messages to teens when `guardian_copies` is on.
- **Email relay**: every email Fold sends for an author (broadcasts and one-to-one) is From `"<Author name> via <church name>" <messages@mail.<church domain>>`, with Reply-To `r-<token>@reply.mail.<church domain>`. The token is random, stored hashed, and maps to one conversation (for a broadcast, to the author and the message). Replies arrive through the provider's inbound processing (Postmark's inbound webhook, https://postmarkapp.com/developer/user-guide/inbound) at `/hooks/postmark/inbound`, outside the CSRF group and verified like other hooks. Fold then:
  1. checks that the sender's address belongs to one of the conversation's participants (mail from anyone else is dropped and logged);
  2. keeps the text and drops the original headers and any attachments (the sender is told to use the app for files);
  3. delivers the reply to the other side in-app and by email, again From Fold with the relay Reply-To.
  - Text that people type themselves (for example a signature with their phone number) is passed on as written; the help page says so.
  - Setup: one MX record for `reply.mail.<church domain>` pointing at the provider's inbound host (wizard step 3, M4.4; added by hand before then). An SMTP-only setup has no inbound mail, so its Reply-To stays the church office address, and leaders still never see members' addresses.
- **SMS relay** (**Planned (M3.7)**): Fold uses the church's one number. Each text starts with the church and the group or event ("First Baptist, Youth Group: ..."). Keywords are handled first (7.4). Any other reply goes to the conversation of the latest message Fold sent that person in the last 7 days, and Fold delivers it to the author in-app and by email (with a relay Reply-To), never showing the number. The author's answer, in the app or by email reply, goes back as an SMS from the church number under the usual consent, send-window, and rate-limit rules. With no message in the last 7 days, the reply goes to the admins. One active SMS conversation per person keeps this simple; the help reply tells people to use the app for older threads. A full shared inbox comes later.
- **Logging and limits**: one-to-one messages count toward the per-author limits (7.5), stay in message history, and write audit metadata (who, to whom, context, channel; not the body).
