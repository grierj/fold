# Fold design: 7. Messaging

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
   7. Enqueue message.deliver_batch jobs (email in batches of 50; SMS one per call) and, when `push_alert` is on,
      one push.deliver per device of each recipient who has push on (DEC-123)
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
- Push notification too (default on): people who turned push on also get a short notification (subject, 120 characters, Open to read). The summary says "N also get a push notification". An announcement never goes push-only (DEC-123).
- Preview, and "Send a test to me".
- Schedule: send now, or pick a date and time (church time zone). Scheduled messages can be edited or cancelled until sending starts.
- Merge fields: `{{first_name}}`, `{{event_title}}`, `{{event_time}}`, `{{event_link}}`, `{{manage_link}}`. On `/messages/:id/read` the event fields carry today's values only while, and only as far as, the reader can still see the event or date (`CanSeeEvent` and the date rules): the series gives all of them, one date seen on its own gives that date's title, time and page, and otherwise they read empty with links as their text (a series announcement to someone who can't see the series keeps the series title alone, through a date they can see that has no title of its own).
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
- Teens (13 to 17) receive email and push only after `account` consent (push also needs their own device opt-in, DEC-122). They receive SMS only when **both** of these are true:
  - a `TeenConsent` row with scope `sms` is granted (by a confirmed guardian, or by an admin with evidence; the leader role never grants consent)
  - the teen has opted in to SMS themselves
- Revoking either consent stops SMS at once.
- An imported phone number for a teen is never treated as opted in.
- Guardian copies are off unless an admin turns on `guardian_copies` (7.1 step 5, 5.10).

Push:

- Push is off until a person turns it on for a device in Fold (the browser's own permission prompt, after a tap). That writes `Consent(push, all, opted_in)`. Per-category switches and "Turn off notifications everywhere" append rows like email. Devices are removed on logout, on 404/410, on key rotation, and when the account is disabled, its password changes or its sessions are revoked. Teens need an active account, not a separate consent; guests never subscribe (DEC-122).

### 7.5 SMS send window, rate limits, bounces

- SMS send window (DEC-9, DEC-25; SMS only, email is not limited):
  - Default: send only between **8:00 AM and 8:00 PM in the recipient's local time**. Admins can change it in Settings.
  - The recipient's local time comes from `User.timezone`, falling back to the church zone. Guest forms capture the browser zone too (DEC-58); guests use the church zone only when none was saved.
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
- Push: optional pushes follow the same recipient-local window as SMS (reminders as in 7.7); required kinds and changes to events starting within 24 hours are exempt. At most 20 optional pushes per person per hour; extras are dropped (`suppressed`, reason "hourly limit"), and the bell still has them (DEC-126).
- SMS failures: carrier "unreachable" or "landline" errors mark the phone number invalid. Carrier filtering errors alert the admins, because they usually mean a registration or content problem.

### 7.6 Member-requested group SMS (F9)

1. A member opens their group, clicks "Request a text to the group", writes the text, and may propose a send time.
2. A `MessageRequest` is created with `status=pending`. Every leader of the group and every admin gets an in-app notification and an email.
3. Any one of them can approve (optionally after editing; the edit is recorded), reject with a note, or let it expire. It expires at the proposed time, or after 72 hours if no time was given.
4. Approval creates a normal `Message` that records the requester and `approved_by`. It is sent under the usual consent, send-window, and rate-limit rules. The requester hears back either way.
5. The first decision wins. The approve call is a conditional update (`WHERE status='pending'`), so two approvers cannot send it twice.

### 7.7 Reminders

- Automatic reminders per occurrence for `going` registrants: email 24 hours before (on by default), **push 2 hours before (on by default, people with push on)**, and SMS 2 hours before (off by default; opted-in people only). Leaders can change these per event.
- Waitlisted people get "you're in" notices, or "still full and starting soon".
- SMS reminders follow the send window. If the reminder time falls outside the recipient's window, it moves to the last allowed time before it, if that is no more than 18 hours earlier (for example 7:59 PM the night before a 9:00 AM event). Otherwise it is skipped, and the email reminder still goes. The 2-hour push reminder follows this same rule, written once here: whatever happens to an SMS reminder outside the window happens to the push reminder (7.5, DEC-126).
- Reminders are `category=reminder` messages, so they appear in history and respect consent.
- Reminders go by email, push and SMS only. They never create bell notifications. The bell carries event *changes* (cancellations, time or place changes, "you're in"), not reminders (5.12, DEC-44).

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

