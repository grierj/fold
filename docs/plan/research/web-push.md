# Web push for Fold: placement, design, decisions, sources

Written Oct 31, 2026, for M3.13 planning. It is the design record behind PLAN M3.13 and DEC-121 to DEC-126; PLAN holds the decisions and the milestone text. Facts about the code were checked against the working tree at `3c91c27` (master, Oct 31, 2026). Browser and iOS claims are marked **[verified]** (read from a primary source on that date), **[vendor]** (a third party's documentation, not Apple's or Google's), or **[unsure]** (from memory; recheck before relying on it). Sources are at the end.

## 0. Summary

- Push is a **third delivery channel** behind the seam PLAN 5.12 already reserves (`notify.Send`; DEC-43). It is not a provider adapter: the browsers' own push services (Google, Mozilla, Apple, Microsoft) carry the message, there is no vendor account, no cost and no new container. That makes it the cheapest possible "SMS for churches that can't afford SMS", and it fits the one rule: nothing to configure, `docker compose up` still works.
- Add one milestone, **M3.13 Web push**, with four thin slices (a: keys, table, protocol client; b: API and notification delivery; c: service worker, manifest, settings card, directions; d: announcements and reminders). Build it **after M3.6 and before M3.7**, so it ships with the email release, ahead of every SMS milestone. No renumbering.
- Design in one line: a VAPID key pair made on first boot and kept in `settings` (private half encrypted with a `SESSION_SECRET`-derived key); a `push_subscriptions` table, one row per device; RFC 8030/8291/8292 implemented with the standard library (Go 1.26 has `crypto/ecdh`, `crypto/hkdf`, `crypto/ecdsa`); a `push.deliver` job; a hand-written 60-line service worker built by Vite and served at `/sw.js`; the Declarative Web Push JSON shape as the payload so Safari 18.4+ needs no JavaScript and everyone else's service worker parses the same JSON.
- Consent: enabling push on a device **is** the opt-in (a `Consent(channel=push, scope=all)` row); per-category push switches reuse `category:<cat>` scopes, so the M3.12 grid gets a Push column with no new table. Teens need an active account (`account` consent), not a new `TeenConsent` scope, because push carries no phone number and costs nothing. Required kinds (safety, waiting) always push to a device that has push on.
- Lock screens are public, so a push shows **less** than the bell: safety and contact kinds send only "Open Fold to review an update"; everything else sends the bell title, a short body, and the link. Never a contact detail, never a hidden group's name (the existing `canSeeGroupIn` rule already decides what the caller may say).
- Announcements: push is a **companion notification** to the email ("Youth Group: Snow day, no meeting tonight — Open to read"), sent to people who turned push on, never push-only in v1. Reminders get a push 2 hours before, the slot SMS was going to fill.


## 1. Placement

### 1.1 Why before SMS

Grier's goal is "a cheap alternative to SMS". The SMS milestones (M3.7–M3.10, M4.6) carry 10DLC registration, a phone number, TCPA send windows, STOP handling, a provider account and per-segment cost. Push needs none of that and reuses what is already built: `consents` (00044), `notification_deliveries` (00011), `jobs` (00051), `messages` (00054), the kind registry and the email settings component. A church on v0.4 with push gets "your group's meeting is cancelled" on members' phones for free; only churches that then still want texting pay for M3.7+.

### 1.2 Dependencies

- Needs **M3.5 send pipeline** for announcements (push rides on `MessageRecipient` rows) and **M3.6 email reminders** for push reminders (the reminder scheduler and `unique_key`s). Slices a–c need neither: notification push works on today's `notify.Send` path (`deliverNotificationEmails` runs after commit; push joins it).
- Does **not** need M3.12. M3.12 moves notification email onto the `notification.deliver` job and adds SMS and the Text column. M3.13 adds the Push column to the same component first; M3.12 then adds Text beside it (DEC-115 said M3.12 "adds Text columns"; this amends it to "adds the Text column beside the Push column").
- Does **not** need M4.1 encrypted provider config. The VAPID private key is encrypted like `peopleSelectionCipher` does today (AES-GCM, key derived from `SESSION_SECRET`, `api_people.go`). When C8 arrives it can move.

### 1.3 Numbering and releases

- New milestone **M3.13 Web push** at the end of 14.6 (no renumbering). Build order note in 14.6: "M3.13 is built after M3.6 and before M3.7."
- Releases table (14.2): v0.4 row becomes "**v0.4 Email and push** … M3.1–M3.6, M3.11, M3.13". If v0.4 is already cut when M3.13 lands, it goes in v0.5 MVP instead; either way it precedes v0.7 SMS.
- 13.2 row "PWA with web push: Later" becomes **Core (M3.13)**.

### 1.4 Slices (14.1: database, then API and internals, then UI)

**M3.13a Keys, subscriptions and the push client** (database + internals, no UI)
- The next free migration: `push_subscriptions`; widen `consents.channel` (the scope regex is unchanged); widen `notification_deliveries.channel` to include `push`. Down refuses once push rows exist, as 00044's does. `message_recipients` does not exist until M3.5, so `messages.push_alert bool default true` and `message_recipients.channel` including `push` go in M3.13d's migration, or in M3.5's if M3.5 lands first.
- VAPID keys made on first boot if missing, under the migrations advisory lock; `fold push keys` CLI (`show`, `rotate`).
- `internal/push`: encrypt (RFC 8291 aes128gcm), VAPID JWT (RFC 8292, ES256), `Send(ctx, sub, Payload, Options{TTL, Urgency, Topic})`, typed results (`Accepted`, `Gone`, `TooLarge`, `RateLimited`, `Temporary`), host allow-list, 3 KB plaintext cap.
- Job kind `push.deliver`, one job per device (`unique_key` `push:n:<notification_id>:d:<device_id>` or `push:mr:<recipient_id>:d:<device_id>`; the payload holds ids, never endpoints); 404/410 deletes the device row. The AGENTS.md "Web push" paragraph and rules-table row land in this slice.
- Acceptance: RFC 8291 Appendix A test vector reproduces byte for byte; a fake push service decrypts what Fold sends; 404 and 410 delete the subscription and record `suppressed` with reason "device gone"; 429/5xx retry with the jobs backoff; an endpoint outside the allow-list is refused before any network call; nothing in the job's `last_error` contains the endpoint path or keys; rotating the keys deletes every device and a running server picks up the new key.

**M3.13b Subscribe API and notification delivery**
- `GET /api/v1/push/config` → `{enabled, publicKey}`; `PUT /api/v1/push/subscriptions` (upsert by endpoint, bound to the signed-in user, also writes the `Consent(push, all, opted_in)` row the first time); `DELETE /api/v1/push/subscriptions` (body: endpoint); `POST /api/v1/push/test` (one notification to that device, 10 per hour per person; past that, 200 with "You just sent a few tests. Try again in a few minutes.", DEC-129); `GET|PUT /me/notification-settings` grows `push: {devices, kinds: [{category, off}]}`.
- `Kind.AllowPush` in the registry (default false) with a per-kind `PushRender`; `notify.Send` callers unchanged; after commit, one `push.deliver` job per device is queued for recipients with devices. The logout request itself carries this browser's endpoint and deletes it; disabling the account, a password change or revoked sessions delete the user's devices. New error codes with `ERROR_COPY` (DEC-61): the unknown push service 400, the push-only 400, and the 403s for a guest, a `pending_consent` teen and push turned off by the church. `docs/API.md` and `.env.example` (`FOLD_PUSH_HOSTS`, optional) change here.
- Acceptance: a person with no device gets no job; a `Consent(push, category:groups, opted_out)` row skips a groups kind and records `suppressed` "push turned off for group updates"; a required kind pushes despite the opt-out; a subscription posted for another user's endpoint is rebound to the poster (endpoint is unique; whoever holds the device holds the subscription); `PUT` from a `pending_consent` teen or a guest is 403; `TestWebTypesMatchGoJSON` covers the new payloads; `TestOnlyLoginAndInvitesAnswer429` still passes (the test-notification limit answers 200 with its own honest line, DEC-129; DEC-52 doesn't apply to a person's own device).

**M3.13c1 Service worker, manifest, icons and the settings card**
- `web/src/sw.ts` built as a second Vite input to `/sw.js` (no hash), served `Cache-Control: no-cache`; it isn't linked from `index.html`, so `bundle-size.mjs` (entry plus modulepreloads) doesn't count it. `/manifest.webmanifest` served by Go with the church name; icons made from the existing `fold-icon-1.svg`: PNG 192, 512, 512 maskable, and `apple-touch-icon` 180; CSP gains explicit `worker-src 'self'; manifest-src 'self'`.
- Me → Notifications gets the "Notifications on this device" card: state machine (unavailable / install first / ready / on / blocked / signed out in the installed app), the Push column of switches, "Send a test notification", "Turn off notifications on this device", "Other devices: 2".
- Acceptance: Playwright with a stubbed `PushManager` proves the subscribe POST body and the card states at 390 and 1280 px, with no axe issues; the SW registers only from the card's button (never on page load); `notificationclick` focuses an existing Fold tab or opens `navigate`; `UI ratchets` and `TestColorsComeFromThemeTokens` unchanged; a stale SW never serves a stale `index.html` (the SW has no fetch handler at all).

**M3.13c2 Directions page and admin switch**
- `/notifications/setup` (public route; the in-app list is `/notifications`, DEC-127) with per-device directions (section 2.7), linked from the card and from the Help page; the `push_enabled` switch in Admin → Settings; `docs/MEMBERS.md`'s "Get notifications on your phone" section and `web/e2e/ui-pages.ts`.
- Acceptance: `/notifications/setup` passes a11y and layout at 390 and 1280 px; with `push_enabled` off it says so.

**M3.13d Announcements and reminders** (after M3.5 and M3.6)
- Composer: "Push notification too" switch (default on; hidden when the church turned push off), the audience summary gains "N also get a push notification", a new skip reason "no device with notifications on" that is informational (not a skip: they still get the email).
- Pipeline: `message.send` queues one `push.deliver` per device of each recipient with push on, alongside the email `MessageRecipient`; history shows push counts. A 1,000-recipient announcement queues one job per device; the worker pool caps concurrency, and no per-host limiter is needed in v1.
- Reminders: a push 2 hours before each occurrence to going registrants with push on (per-event switch beside the email and SMS ones in 7.7), following the same out-of-window rule as SMS reminders (7.7, written once there). M3.8 replaces the interim copy of the send-window function.
- Acceptance: a recipient with push on gets exactly one email and one push per message, even after a restart; push-only is refused by the API (400 "an announcement always goes by email"); a reminder push creates no `notifications` row (DEC-44); the push body is the subject plus the first 120 characters of the plain text, never the HTML; a message to a hidden group's members names the group only in copies to people who can see it (`a.groupShownTo`).

## 2. Design

### 2.1 Keys (VAPID)

- **Generation.** On first boot (and on any boot where the row is missing) the process holding the migrations advisory lock makes a P-256 key pair and writes two `settings` rows: `vapid_public_key` (base64url, 65 bytes uncompressed point, what the browser needs) and `vapid_private_key_enc` (AES-GCM with the key `sha256("fold:vapid:v1:" + SESSION_SECRET)`, the `peopleSelectionCipher` pattern). Nothing for the admin to do; the setup wizard never mentions it. `APP_ENV=dev` uses the insecure dev secret like everything else.
- **Subject.** RFC 8292 wants a `sub` claim; use `mailto:<FOLD_EMAIL_FROM address>` and fall back to `SITE_URL`. Push services use it to contact an abusive sender; it is never shown to members. Apple rejects a `sub` such as `mailto:…@localhost` (**[unsure]**), so a dev server with a local address can't reach Apple's push service; test iOS against a real https deployment.
- **Rotation.** `fold push keys rotate` writes a new pair and deletes every `push_subscriptions` row: a subscription is bound to the public key it was made with, so old ones can't be used anyway (**[unsure]** about every push service's exact error; FCM answers 403 in my memory). The SPA sees the new key in `/push/config`, notices its local subscription's `applicationServerKey` differs, and resubscribes silently next time the card is opened; until then the person gets no pushes. Rotation is an incident tool, not a schedule. Changing `SESSION_SECRET` makes the stored private key unreadable: on boot, a decrypt failure logs one clear line and regenerates (same effect as a rotation), so a secret change never bricks the server. That regeneration writes an admin notice or a System page line, "Notifications on devices were reset; members need to turn them on again". `fold push keys rotate` needs `SESSION_SECRET` as well as `DATABASE_URL`; `fold push keys show` prints only the public key. A running server reloads the key on rotation (it re-reads the settings row, not a value cached forever).
- **Zero-config rule.** No new env var is required. `FOLD_VAPID_PRIVATE_KEY` as an optional override is **not** proposed; an operator who wants to carry keys across databases restores the database.

### 2.2 Server side

**Data model** (the 5.3 wording is in PLAN):

- `PushSubscription(user_id, endpoint text unique, p256dh text, auth text, expires_at?, label text, created_at, last_used_at, failed_at?)`, with an index on `user_id`, `CHECK (octet_length(endpoint) <= 2048)` and length bounds on `label`. (`user_agent_hash` and `last_error` are dropped: nothing reads the hash, and send results go in `notification_deliveries`.) `last_used_at` is written at most once a day per device. One row per browser installation (an iPhone's Home Screen app and its Safari are separate rows). `label` is what the card shows ("iPhone · Home Screen app", "Chrome on Linux"), derived server-side from the UA, never free text. The endpoint URL is a capability (anyone holding it can push to the device, with the keys); treat it like a token: never logged, never in an error, never returned by the API (the client already has its own). `p256dh` and `auth` are the browser's public key and 16-byte secret; they're useless without the endpoint but are stored as given.
- `Consent.channel` gains `push`. Scopes stay `all` and `category:<cat>`. Turning on a device appends `(push, all, opted_in, web_form)` whenever the latest `(push, all)` row isn't `opted_in` (so turning it on again after "Turn off" works), with evidence text such as "Turned on notifications for iPhone (Home Screen app)"; "Turn off notifications everywhere" appends `(push, all, opted_out)` and deletes the device rows; per-category switches append `category:<cat>` rows. Latest row per (user, channel, scope) wins, as today.
- `notification_deliveries.channel` gains `push`; the unique `(notification_id, channel)` stays, so a notification has one push delivery row whatever the device count. The device row carries only `failed_at`; the reason is on the delivery row. Each device is its own job, but there is still one aggregate delivery row per notification.
- `messages.push_alert bool` (companion notification on or off) rather than widening `channel`; `message_recipients.channel` includes `push`. Both belong to M3.13d's migration (or M3.5's if it lands first), because `message_recipients` doesn't exist until M3.5.
- Settings: `push_enabled` (bool, default true; admin switch, DEC-124).

**Library or stdlib.** Two real options:

1. `github.com/SherClockHolmes/webpush-go` (MIT). Its `go.mod` requires `github.com/golang-jwt/jwt/v5` and `golang.org/x/crypto` **[verified]**; Fold already has `x/crypto`, so it adds one dependency. Mature, widely forked.
2. Standard library. RFC 8291 is ECDH on P-256 (`crypto/ecdh`), two HKDF steps (`crypto/hkdf`, in the stdlib since Go 1.24 **[verified]**), AES-128-GCM, and an 86-byte aes128gcm header; RFC 8292 is one ES256 JWT (`crypto/ecdsa`, `encoding/json`, base64url). About 250 lines plus tests. The RFC 8291 Appendix A example gives the keys, salt and expected ciphertext, so correctness is a byte-for-byte test, not a judgment call.

Recommendation: **stdlib**, in `internal/push`, with the Appendix A vector as the contract test and the fake push service (2.9) as the integration test. Reasons: DEC-43 (self-contained), one fewer JWT library to scan, and the whole protocol fits in a file an agent can read. Fall back to webpush-go only if the vector test won't pass within a day.

**Sending.** `push.Send` sets `TTL` (RFC 8030 requires it **[verified]**), `Urgency`, optional `Topic` (≤32 URL-safe base64 characters **[verified]**), `Content-Encoding: aes128gcm`, `Authorization: vapid t=…, k=…`. Payload plaintext is capped at **3,000 bytes**: a push service may not answer 413 to a body of 4,096 bytes or less **[verified]**, and the aes128gcm header, tag and padding add about 103 bytes. Timeout 10 s per device; Fold's outbound client does not follow redirects (a redirect to a non-allow-listed host is a hard error).

**Job kinds.**
- `push.deliver`: one job per device. Payload `{notification_id, device_id}` or `{message_recipient_id, device_id}` (ids, never endpoints); `unique_key` `push:n:<notification_id>:d:<device_id>` / `push:mr:<recipient_id>:d:<device_id>`; `MaxAttempts` 5 with the shared backoff. This replaces a self-mutating "done list": a retry only ever repeats one device, so it can't double-send to another. The handler: load the row, recipient and device; re-check `push_enabled`, account status (`active` only; guests have no devices), consent for optional kinds, `User.status != pending_consent`, quiet hours and the hourly cap (DEC-126; extras are dropped as `suppressed` "hourly limit", not deferred, and the bell still has them); render the lock-screen payload (2.4); send. `Accepted` → the aggregate delivery row `sent`; `Gone` → delete the device, record `suppressed` "device gone" if no device is left; `Temporary`/`RateLimited` → return the error (the job retries). `TooLarge` is a bug, not a retry: record `failed` and log. A 1,000-recipient announcement queues one job per device; the worker pool caps concurrency, so no per-host limiter is needed in v1.
- Device pruning: `notifications.cleanup` also deletes `push_subscriptions` with `failed_at` older than 30 days or `expires_at` past (`expirationTime` from the browser, usually null).
- No new ticker; no new container.

**404 and 410.** RFC 8030 says a push service answers **404** for an expired subscription **[verified]**; FCM in practice answers **410** for an unsubscribed endpoint **[unsure]**. Treat both as `Gone`: delete the row in the same transaction as the delivery record, log "push: device gone for user N" (no endpoint). 403 is `failed`, without parsing its body (after a rotation the devices are already deleted, so a 403 means a real problem).

### 2.3 Browser side

- **Service worker.** `web/src/sw.ts`, compiled by Vite as a second Rollup input with a fixed name (`build.rollupOptions.input: { main: 'index.html', sw: 'src/sw.ts' }`, `output.entryFileNames` returning `sw.js` for that chunk). Served by `serveSPA` at `/sw.js` with `Cache-Control: no-cache` (today only `index.html` is `no-store` and `assets/` immutable; a root file gets no header, which lets browsers heuristically cache the worker). Scope `/` (the default for a root-served worker; no `Service-Worker-Allowed` header needed). It handles **three events only**: `push` (parse the Declarative Web Push JSON and `showNotification(title, {body, tag, data: {navigate}})`; it always calls `showNotification`, falling back to "Open Fold" if the JSON doesn't parse, because Safari revokes subscriptions that receive silent pushes), `notificationclick` (focus an open Fold window and `navigate` it, else `openWindow`), and `pushsubscriptionchange` (get a CSRF token from `GET /api/v1/auth/csrf` and the public key from `/push/config`, resubscribe, and `PUT` it back). On logout the SPA also closes shown notifications (`registration.getNotifications()`). **No `fetch` handler and no caching**, so the SW can never serve a stale shell after logout (10.1 "logout leaves nothing behind") and never fights the `no-store` rule on `index.html`. Registration happens only from the "Turn on notifications" button, never on page load, so people who never asked get no worker.
- **CSP.** `script-src 'self'` already governs workers when `worker-src` is absent, and `manifest-src` falls back to `default-src 'self'`, so the current `contentSecurityPolicy` constant admits both; add `worker-src 'self'; manifest-src 'self'` anyway so the intent is explicit and a future tightening can't break it by accident. No new origin: the SW talks only to Fold, and the browser, not the page, talks to the push service.
- **Manifest.** `GET /manifest.webmanifest`, generated by Go (not a static file) so `name` and `short_name` can carry the church name (DEC-124): `{"name": "<church name>", "short_name": "<church name, ≤12 chars else 'Fold'>", "start_url": "/?source=homescreen", "scope": "/", "display": "standalone", "background_color"/"theme_color" from the tokens (a `color-ok:` line), "icons": [192, 512, 512 maskable]}`. Apple reads `display: standalone` or `fullscreen` to make it a Home Screen *web app* rather than a bookmark **[verified]**. `index.html` gets `<link rel="manifest">`, `<link rel="apple-touch-icon" href="/assets/brand/fold-touch-180.png">`. Only the manifest carries the church name: `index.html` is static and the server doesn't rewrite it, so there is no `apple-mobile-web-app-title` meta (**[unsure]** whether iOS then always takes the manifest name; the first-church checks confirm it). Icons are PNGs added under `web/public/assets/brand/` next to the existing `fold-icon-1.svg` (Vite copies `public/` into `dist`).
- **Click behaviour.** `navigate` is the notification's internal `link` made absolute with `SITE_URL`; the SW refuses anything not on its own origin. Opening the installed app on iOS lands in the standalone context; the SPA reads `?source=homescreen` to show the sign-in page if the cookie jar is empty (the Home Screen app has its own storage, separate from Safari **[unsure]**, which is why the directions say "sign in again once").
- **Badge.** `navigator.setAppBadge(unread)` from the bell's polled count when the browser has it, cleared on read-all; `app_badge` in the payload for Safari. Cheap, and it's the thing people notice on an icon.

### 2.4 Content and privacy (a lock screen is public)

A push is seen by whoever is holding or next to the phone, on a lock screen, in a car, on a watch. So it says less than the bell:

- **Safety, consent and contact kinds** (`CatSafety`, anything with `RenderEmailSafe`): title "Fold", body "Open Fold to review an update." Nothing else. This is the existing link-only rendering (`RenderForEmail` with `RenderEmailSafe`), reused. A guardian's row about a household child ("for Sam") gets the same safe variant.
- **Waiting (task) kinds for admins**: "A new group is waiting for approval" with no requester name; the bell has the name.
- **Everything else**: `Kind.AllowPush` defaults to false, and each kind that pushes has its own short `PushRender` (title and short body), rendered at write time with the same `Vars` as the bell, so the caller's `canSeeGroupIn` decision is already in it. It is never the bell body cut to 120 characters, which could leak message or reply text (7.8, DEC-31). Event kinds carry the event title and time; group kinds the group name only when the recipient may see it; never a place for invite-only groups (the `Vars.Place` rule stands).
- **Never**: an email address, phone, birth date, care note, message body of a teen, a hidden adult's contact details (DEC-31), a guest's manage link, a video link, a signed URL of any kind. The payload is rendered from `PushRender` and `Link`, never from `Body`, `Data`, `ManageURL` or `VideoURL`. A registry test renders every kind's push text with the teen and hidden-adult fixtures (extend `TestRegistryPrivacy` or whatever the existing privacy test in `internal/notify` is named).
- **Announcements**: body is the subject plus the first 120 characters of `body_text` and "Open to read"; HTML never. The church name is the notification's visible source (the manifest name), so the text doesn't repeat it.
- **Teens**: the Teen badge never appears in a push (it would label the person to bystanders); rows about a teen use the safe variant.
- **TTL and urgency** per kind: cancellations and changes to an event in the next 24 h: `TTL` 6 h, `Urgency: high`; other event and group kinds: `TTL` 24 h, `normal`; announcements: `TTL` 24 h, `normal`; reminders: `TTL` until the occurrence starts, `normal`; admin tasks: `TTL` 72 h, `low`. `Topic` set for batched kinds (`n<notification_id>`: RFC 8030 allows only URL-safe base64 characters, at most 32, so no colon) so a re-render replaces the pending one instead of stacking. Expired pushes vanish; the bell and the email are the record.

### 2.5 Consent model

- **Device versus person.** Both, and they're different things. A `push_subscriptions` row is a device (where to send); `Consent(channel=push)` is the person's choice (what to send). Turning on the first device writes `(push, all, opted_in)`; the per-category switches write `category:<cat>`; "Turn off notifications everywhere" writes `(push, all, opted_out)`, which overrides every per-category row, and deletes the device rows. A device with no `all` opt-in (impossible through the API, possible after a restore) gets nothing.
- **Fits the existing scopes.** `consents.scope` already allows `category:(admin|groups|events|messages)`; only the `channel` CHECK widens. `emailOptOuts` becomes `optOuts(channel)`. The M3.12 page becomes rows by category × **Email · Push** columns now, **Email · Text · Push** when M3.12 adds Text. Required categories are locked in every column: a required kind pushes to a device that has push on, whatever the switches say (the person can still turn the device off). "Turn off all optional email" stays email-only; a sibling "Turn off notifications everywhere" covers push, same component, same `Checkbox`.
- **Default.** With no device: nothing. With a device: every optional category on. Members asked for the device prompt themselves, in Fold, after a gesture, so this is the opt-in.
- **Teens.** No `TeenConsent` scope. Reasons: SMS needs guardian consent because it reaches a phone number the church pays to text and that number is a contact detail (7.4, 10.4); push reaches a browser installation the teen already uses to sign in, costs nothing, reveals nothing, and is revoked by the teen or the OS at any time. A `pending_consent` teen has no feed and gets 403 on subscribe; revoking `account` consent disables the account and the job skips disabled accounts. Guardian copies (DEC-19) stay email-only (DEC-29 reasoning: copies are a record, push isn't). Grier confirmed this (DEC-122).
- **Guests** never subscribe (no sign-in; `status=guest` is 403).
- **Quiet hours.** Decided (DEC-126): optional push follows the **same recipient-local window as SMS** (default 8 AM–8 PM, DEC-9/25), through the shared send-window function M3.8 builds (until then, a small copy in `internal/push` with the same defaults, replaced in M3.8). Exempt: required kinds, and changes or cancellations to an event starting within 24 hours (the whole point of a late-night "service cancelled" push). The 2-hour push reminder follows the same out-of-window rule as SMS reminders (written once, in PLAN 7.7); the hourly cap of 20 drops extras (`suppressed` "hourly limit"). Email ignores quiet hours as today (DEC-44).

### 2.6 Composer and broadcasts

Options:

- **A. A channel choice.** Email / Push / Both, like the SMS tab. Push-only is possible. Cheapest to explain ("like text, but free"). Risk: push is lossy (TTL expiry, dead devices, iOS permission silently off), so an author who picks push-only believes everyone was told when only the people who happened to unlock their phone were. Also needs push-only content rules (no 3 KB cap on a newsletter).
- **B. A companion notification** (recommended). Every announcement is an email as now. If `push_alert` is on (default on), each recipient who has push on also gets a short notification: subject, 120 characters, "Open to read", `navigate` to the message in Fold. The email is the record; the push is the nudge. One switch in the composer ("Push notification too"), no second body, no new tab.
- **C. Delivery by preference.** A person chooses "push instead of email" for messages from leaders. Saves nobody money (email is cheap), hides the record from them, and makes the author's counts hard to read. Not recommended for v1; possible later as a per-person M3.12 choice.

Under B the audience summary becomes: "142 people · 38 also get a push notification · 9 skipped: 6 turned off messages from leaders, 3 have no email". "No device with notifications on" is **not** a skip reason: they still get the email. The person-level list shows a small phone glyph (with text, status is never colour alone) next to people with push on. Skip reasons for push itself exist only inside the pipeline (`push turned off for messages from leaders`, `device gone`, `outside quiet hours · sending at 8:00 AM`) and show in history, not in the composer.

Reminders (7.7) become: email 24 h (on), **push 2 h (on, people with push on)**, SMS 2 h (off, opted-in people) when SMS exists. Per-event switches for each.

### 2.7 The directions page (`/notifications/setup`, and the card on Me → Notifications)

**Detection.** The card and the page compute one state from: `window.isSecureContext`; `'serviceWorker' in navigator && 'PushManager' in window`; `Notification.permission` (`default|granted|denied`); `matchMedia('(display-mode: standalone)').matches` or `navigator.standalone === true` (iOS Safari's own property **[unsure]** whether other iOS browsers' Home Screen apps set it; the media query is the standard one); the platform (`/iPhone|iPad|iPod/` in the UA, plus `navigator.maxTouchPoints > 1 && /Macintosh/` for iPadOS pretending to be a Mac **[unsure]**; `/Android/`; `/CriOS|FxiOS|EdgiOS/` for Chrome, Firefox and Edge on iOS); whether the server has a subscription for this browser (compare `pushManager.getSubscription()?.endpoint` with the devices list, which the API returns as hashes, not endpoints); signed-in or not.

States and what shows (first line of each is the heading):

| State | When | What the card says |
|---|---|---|
| `unavailable` | not a secure context, no `PushManager` on a non-iOS browser, or iOS detectably older than 16.4 (not `install_first`: adding to the Home Screen can't help) | "Notifications need a secure (https) connection and a browser that can receive them. On this device, use email." No button. In dev on a LAN IP this is the state you'll see; `localhost` is fine. |
| `install_first` | iOS, not standalone | The iPhone/iPad steps below. No permission button (iOS has no `PushManager` in a tab **[verified]** that permission is only available to Home Screen web apps). |
| `ready` | push is available, permission `default`, no subscription | "Turn on notifications" button; one line: "Your phone will ask once. You can change your mind any time." |
| `on` | permission `granted`, subscription known to the server | "Notifications are on for this device" · "Send a test notification" · "Turn off notifications on this device" · "Other devices: iPhone (Home Screen app), Chrome on Windows" · the Push column of switches. |
| `on_elsewhere` | `granted` here but subscription missing on the server (rotation, restore, another account on this browser) | Same as `ready`, with "This device was set up before; turn it on again." |
| `blocked` | permission `denied` | "This device is blocking notifications from Fold. To allow them again:" then the platform's undo steps (below). No button (calling `requestPermission` would do nothing). |
| `signed_out_app` | standalone, no session | The sign-in page with one line above the form: "Sign in once here and your notifications will come to this app." |
| `church_off` | `push_enabled=false` | The card is absent; `/notifications/setup` says "Your church hasn't turned on notifications. Ask a church admin." |

**Copy.** Plain, warm, second person (AGENTS.md). The heading on `/notifications/setup` is "Get notifications on your phone". The page picks the section for the device it's opened on and offers the others under "Using a different device?".

*iPhone and iPad (Safari)*

> **Get notifications on your iPhone or iPad**
>
> Notifications work on iPhones and iPads running iOS 16.4 or newer, once Fold is on your Home Screen. It takes about a minute.
>
> 1. Open **{site URL}** in **Safari**. (If you're reading this in Safari already, you're set.)
> 2. Tap the **Share** button — the square with an arrow pointing up, at the bottom of the screen on an iPhone, top right on an iPad.
> 3. Scroll down and tap **Add to Home Screen**, then tap **Add**.
> 4. Close Safari and open **{church name}** from your Home Screen. It opens like an app.
> 5. Sign in once. (The Home Screen app keeps its own sign-in.)
> 6. Go to **Me → Notifications** and tap **Turn on notifications**. When your iPhone asks, tap **Allow**.
>
> That's it. Notifications show on your Lock Screen and in Notification Center like any app's. If you'd rather keep them private on a locked phone, open **Settings → Notifications → Show Previews** and choose **When Unlocked**. [**[unsure]** the menu path on each iOS version.]
>
> <details><summary>Using a different browser?</summary> (collapsed by default)
>
> **Using Chrome, Firefox or Edge on your iPhone?** Those browsers can add Fold to your Home Screen too (same Share button, **Add to Home Screen**) if you're on iOS 16.4 or newer. If you don't see that option, open the link in Safari and follow the steps above. [**[verified]** that third-party browsers with Apple's web-browser entitlement can offer Add to Home Screen since 16.4; **[vendor]** that push then works from an app added that way — PushAlert and OneSignal say Chrome 113+ does. The page says "if you don't see that option" rather than promising.]
>
> </details> (end of the collapsible section; it is a collapsible control in the page, not literal HTML)
>
> **To stop notifications:** on your iPhone, open **Settings → Notifications**, find **{church name}**, and turn **Allow Notifications** off. Or in Fold, **Me → Notifications → Turn off notifications on this device**. Removing the icon from your Home Screen stops them too.
>
> **Tapped "Don't Allow" by mistake?** iOS asks only once. Open **Settings → Notifications → {church name}** and turn **Allow Notifications** on. If {church name} isn't listed, remove the icon from your Home Screen and add it again from Safari. [**[vendor]** OneSignal says remove-and-re-add is the way to get the prompt back; the Settings path is **[unsure]**: a denied web app may not be listed at all.]

*Android (Chrome)*

> **Get notifications on your Android phone**
>
> 1. Open **{site URL}** in **Chrome** and sign in.
> 2. Go to **Me → Notifications** and tap **Turn on notifications**.
> 3. When Chrome asks, tap **Allow**.
>
> You can also add Fold to your home screen: tap the **⋮** menu, then **Add to Home screen** (or **Install app**). Notifications work either way.
>
> **To stop notifications:** in Fold, **Me → Notifications → Turn off notifications on this device**. Or in Chrome, tap the lock icon next to the address, then **Permissions → Notifications**, and turn it off. On your phone, **Settings → Apps → Chrome → Notifications** lists each site too.
>
> **Blocked it by mistake?** Tap the lock icon next to the address in Chrome, then **Permissions**, and set **Notifications** to Allow. Then come back here and tap **Turn on notifications** again.

[**[unsure]**: Samsung Internet and Firefox for Android can receive push too, I believe; the page's Android section says "Chrome" and the card's `ready` state appears on any Android browser that exposes `PushManager`, so they work without being promised.]

*Computer: Chrome, Edge, Firefox*

> **Get notifications on your computer**
>
> 1. Open **{site URL}** and sign in.
> 2. Go to **Me → Notifications** and click **Turn on notifications**.
> 3. When your browser asks, choose **Allow**.
>
> Notifications arrive while the browser is running, even with Fold closed. [**[unsure]** Firefox and Chrome on Windows keep a background process by default; macOS Chrome does too; Linux varies. "while the browser is running" is the safe promise.]
>
> **To stop notifications:** **Me → Notifications → Turn off notifications on this device**, or click the icon left of the address bar (a lock or a tune icon) and set **Notifications** to Block.
>
> **Blocked it by mistake?** Click the icon left of the address bar, find **Notifications**, and set it to Allow. Then reload and click **Turn on notifications** again.

*Mac: Safari*

> **Get notifications in Safari on your Mac**
>
> Safari 16.1 or newer (macOS Ventura and later).
>
> 1. Open **{site URL}** in Safari and sign in.
> 2. Go to **Me → Notifications** and click **Turn on notifications**.
> 3. When Safari asks, choose **Allow**.
>
> **To stop notifications:** **Me → Notifications → Turn off notifications on this device**, or in Safari choose **Safari → Settings → Websites → Notifications** and set {site URL} to Deny. Your Mac's **System Settings → Notifications** lists Fold too.
>
> **Blocked it by mistake?** **Safari → Settings → Websites → Notifications**, set {site URL} to Allow, reload, and click **Turn on notifications** again.

[**[verified]** macOS web push arrived in Safari 16.1 (the WebKit iOS post says iOS 16.4 brings "the same standards-based Web Push added to macOS in Safari 16.1"). Settings menu paths are **[unsure]** across macOS versions.]

Every section ends with "Notifications are a shortcut. Everything still shows in **Alerts** in Fold, and in your email." The page has no red anywhere; a blocked state is a neutral `EmptyState`-style box with the steps.

### 2.8 Security

- **SSRF.** The browser hands Fold an arbitrary URL and asks the server to POST to it. Defence in depth, in order:
  1. **Allow-list of push-service hosts**, checked at `PUT /push/subscriptions` (400 "that browser's notification service isn't one we can send to") and again in `push.Send` before any connection. Scheme must be `https`, port 443 or none; reject userinfo, IP-literal hosts and trailing-dot or IDN hosts; match the host on a dot boundary (`evilfcm.googleapis.com` fails), exactly or as a subdomain of: `fcm.googleapis.com`, `android.googleapis.com` (older Chrome), `updates.push.services.mozilla.com`, `push.apple.com` (Safari uses `web.push.apple.com`), `notify.windows.com` (Edge uses `*.notify.windows.com`), `push.samsungosp.com`. The first three are **[verified]** from the webpush-java endpoints wiki; Apple, Microsoft and Samsung are **[unsure]** (a Stack Overflow answer and an unmerged PR); they stay unsure until the first-church checks (`docs/FIRST-CHURCH-CHECKS.md`, DEC-128) confirm each one. A `FOLD_PUSH_HOSTS` env var can extend the list for a church on an unusual browser; it never replaces the hard rule that private, loopback, link-local and multicast addresses are refused after DNS resolution (the `http.Transport.DialContext` checks the resolved IP, closing the DNS-rebinding hole). `Transport.Proxy = nil`, so `HTTP(S)_PROXY` can't bypass the resolved-IP check.
  2. No redirects followed (`CheckRedirect` returns `http.ErrUseLastResponse`). 10 s timeout. Response bodies read to 4 KB and then discarded; the body is never stored except a 200-character prefix in the delivery row's reason after stripping anything that looks like a URL or key.
  3. The endpoint path is never logged (it is the secret). `redactLogPath` doesn't apply (it's outbound), so the rule is on `push.Send`'s own log lines and on `jobs.CleanError`: add an `endpointLike` regexp beside `emailLike`.
- **Binding.** A subscription belongs to the signed-in user who posted it. The endpoint is unique; a re-post for an endpoint already held by another user rebinds it (the device changed hands or the account did), and the old owner's bell gets nothing about it (nothing to act on, DEC-52). The logout request itself carries this browser's endpoint and deletes the row in the same call (a separate `DELETE` after logout can fail once the CSRF token is gone); the SPA then calls `pushManager.unsubscribe()` and closes shown notifications, so the next person on a shared family tablet gets nothing meant for the first. A user's devices are also deleted when the account is disabled, the password changes, or sessions are revoked. `TestLogoutEndsSession` grows a case.
- **Payload size** capped at 3,000 bytes before encryption (2.2); a kind that renders past it is a test failure, not a runtime surprise.
- **Rate limits.** `PUT /push/subscriptions`: 30 per user per hour; past that it saves nothing and answers 200 with "Try again in a few minutes", so the card never shows "on" without a subscription (DEC-129). `POST /push/test`: 10 per person per hour; past that it answers 200 with "You just sent a few tests. Try again in a few minutes.", not "Sent" and not a 429 (DEC-129; it is the person's own device, so DEC-52's uniform answer doesn't apply). Devices per user: 10; the 11th replaces the oldest `last_used_at`. Per-recipient optional pushes: at most 20 per hour across kinds (beyond that, drop the push as `suppressed` "hourly limit"; the bell still has it); this mirrors 7.5's per-recipient SMS cap in spirit.
- **Abuse.** Push can't be sent by members: only the pipeline sends, from `notify.Send` callers and the composer, both already behind `internal/policy`. A compromised `SESSION_SECRET` exposes the VAPID private key, which exposes… the ability to push to this church's members, which the attacker already has through sessions. No new class of secret.
- **Logging.** Never: endpoint URL or path, `p256dh`, `auth`, the JWT, the encrypted body, the plaintext body. Allowed: user id, device label, status code, result class, duration. `TestAccessLogKeepsSecretsOut` gains nothing (inbound paths carry no secret) but `TestPushLogsKeepSecretsOut` is new.
- **CSRF**: the three endpoints are ordinary SPA calls behind `csrfMiddleware`; `csrfExemptRoutes` is unchanged (`TestCSRFExemptRoutesAreExact` passes untouched).
- **Service worker** has no fetch handler (no cache poisoning, no stale post-logout shell), ignores messages from pages (`message` event unhandled), refuses `navigate` off-origin.

### 2.9 Operations and testing

- **Secure context.** Push needs `https` or `localhost`. Production behind Caddy is fine with nothing to add. `make dev` on `http://localhost:5173` works (secure context by definition **[verified]** in the sense that the Secure Contexts spec treats `localhost` as potentially trustworthy; **[unsure]** that every browser also exposes `PushManager` there: Chrome does, Safari does not do push on plain `localhost` in my memory). A LAN address (`http://192.168.1.20:8080`) is not secure and shows `unavailable`; the card's own text says so. Vite's dev proxy must also forward `/sw.js` and `/manifest.webmanifest` (`vite.config.ts` proxies only `/api` and `/uploads` today); or serve both from `web/public` in dev.
- **Egress.** The server must reach the push hosts on 443. `deploy/digitalocean/smoke.sh` gets one check: `fold push check` (CLI) resolves and TCP-connects to each allow-listed host and prints OK or the blocked one; the admin System page shows the same. A church on a locked-down network sees why pushes die instead of guessing.
- **Behind proxies.** Nothing changes: the browser–push-service leg never touches Fold's proxy, and Fold's outbound leg goes direct. `FOLD_TRUSTED_PROXIES` is irrelevant to push.
- **Go tests.** `internal/push/fakeservice_test.go`: an `httptest.Server` that holds the RFC 8291 Appendix A subscriber keys, decrypts each request with its own stdlib implementation, asserts the JSON and headers (`TTL`, `Urgency`, `Topic`, `Authorization` parses and verifies against the public key), and answers whatever the test wants (201, 404, 410, 413, 429 with `Retry-After`, 500). Handler tests use it via `App.Push.Transport` pointed at the fake. The allow-list test uses `httptest`'s loopback address as the negative case. The vector test is `TestRFC8291AppendixA`.
- **Playwright.** Headless Chromium has no push service, so e2e can't receive a real push. What it can prove: `addInitScript` installs a fake `PushManager`/`ServiceWorkerRegistration` whose `subscribe()` returns the Appendix A subscription; the card goes `ready → on`, the `PUT` body is right, "Send a test notification" shows its status line, "Turn off notifications on this device" calls `DELETE`; `context.grantPermissions(['notifications'])` and a `denied` context drive the `blocked` state; the a11y and layout specs cover `/notifications/setup` and the card at 390 and 1280. Add `/notifications/setup` to `ui-pages.ts`.
- **A real iPhone.** The only proof that counts for the iOS path, and an agent can't run it. Per DEC-128 it is built best effort, merged, and listed in `docs/FIRST-CHURCH-CHECKS.md` for the first church, not a merge gate. The checklist, with Android Chrome, desktop Safari and Edge on Windows added there, is: deploy the branch to the staging droplet (https), on an iPhone with iOS ≥ 16.4 follow the directions page literally, confirm the prompt appears only after the tap, tap "Send a test notification", lock the phone, see it; then turn the device off in Fold and confirm the server deleted the row; then block in iOS Settings and confirm the next send records `device gone` (iOS answers 410 or 404 **[unsure]**); screenshots attached to the PR. Repeat once on Android Chrome and once in desktop Safari. Record which iOS version was used in the PR so the "16.4+" claim has a tested floor.
- **Declarative Web Push.** Sending the `{"web_push": 8030, "notification": {...}}` shape means Safari 18.4+ and iOS 18.4+ show the notification without waking the service worker **[verified]**, and older Safari, Chrome and Firefox hand the same JSON to the `push` handler **[verified]** (the WebKit post says the format is backward compatible when the SW parses it). One payload format, one renderer in Go.
- **EU note.** Apple pulled Home Screen web apps in the iOS 17.4 betas for the EU and then restored them before release **[verified]** from press reports, not from Apple. Fold's churches are in the US (the plan assumes 10DLC, TCPA); if a church in the EU appears, retest on an EU device before promising iOS notifications.

## 3. Decisions

Grier chose the recommended option for every question (Oct 31, 2026). PLAN 15.1 holds the decisions.

| Question | Answer | Decision |
|---|---|---|
| Q1. What is a push in the composer? | B, a companion notification: every announcement is an email; "Push notification too" (default on) nudges people with push on; push-only is refused | DEC-123 |
| Q2. Separate per-kind channel in settings? | A, a separate Push column per category, with the same locked rules for required kinds | DEC-122 |
| Q3. Do teens need guardian consent for push? | A, no: an active teen account can turn push on for its own device; guardian copies stay email-only | DEC-122 |
| Q4. Can a church turn push off? | A, an admin switch `push_enabled`, default on | DEC-124 |
| Q5. Name and icon of the Home Screen app? | A, the church's name (`site_name`) with the Fold icon; a per-church icon later | DEC-124 |
| Q6. Quiet hours for push? | A, optional pushes follow the SMS window; required kinds and changes to events within 24 hours go at once | DEC-126 |
| Q7. Where does M3.13 ship? | A, with v0.4 ("Email and push"), after M3.6 and before M3.7 | DEC-121 |

DEC-125 (self-contained keys and transport) records the design in 2.1 and 2.2 and was not a question. Review answers: DEC-127 (the feature is called "Notifications on this device", the directions page is `/notifications/setup`, "Alerts" stays the in-app list), DEC-128 (checks an agent can't run are best effort and listed in `docs/FIRST-CHURCH-CHECKS.md`), DEC-129 (the test notification allows 10 an hour and says so past that).

## 4. For the implementing agent

Repo edits the milestone implies. PLAN's M3.13 scope now assigns each to the slice that needs it:

- (slice a) `AGENTS.md`: a "Web push" paragraph (keys, allow-list, never log endpoints, SW has no fetch handler) and a rules-table row: "Push payloads never carry contact details, signed links or a hidden group's name; endpoints are never logged | registry push-render privacy test, `TestPushLogsKeepSecretsOut`".
- (slice b) `docs/API.md`: the endpoints and the settings payload.
- (slice b) `.env.example`: `FOLD_PUSH_HOSTS` (optional, additive) with a comment that nothing is required; PLAN 11.2 lists it.
- (slice c2) `web/e2e/ui-pages.ts`: `/notifications/setup`.
- (slice c2) `docs/MEMBERS.md`: a "Get notifications on your phone" section linking `/notifications/setup`.

## 5. Sources

Primary:
- WebKit, "Web Push for Web Apps on iOS and iPadOS" (iOS 16.4; Home Screen requirement; `display: standalone|fullscreen`; user gesture; third-party browsers' Add to Home Screen; badging; Notifications settings): https://webkit.org/blog/13878/web-push-for-web-apps-on-ios-and-ipados/
- WebKit, "Meet Declarative Web Push" (`web_push: 8030`, `notification{title, navigate, body, app_badge}`, no service worker needed, iOS 18.4 / macOS 15.5, backward compatible with service-worker push): https://webkit.org/blog/16535/meet-declarative-web-push/
- RFC 8030, Generic Event Delivery Using HTTP Push (TTL required; Urgency values; Topic ≤ 32 chars; no 413 at ≤ 4096 bytes; 404 for an expired subscription): https://www.rfc-editor.org/rfc/rfc8030.html
- RFC 8291, Message Encryption for Web Push (aes128gcm; Appendix A test vector): https://www.rfc-editor.org/rfc/rfc8291.html
- RFC 8292, VAPID: https://www.rfc-editor.org/rfc/rfc8292.html
- SherClockHolmes/webpush-go `go.mod` (requires `golang-jwt/jwt/v5`, `golang.org/x/crypto`): https://raw.githubusercontent.com/SherClockHolmes/webpush-go/master/go.mod
- Go 1.24 release notes draft (`crypto/hkdf` added): https://go.googlesource.com/website/+/2a8b895514c4e52860e9c0b1ef8626db3ea36bf5/_content/doc/go1.24.md
- webpush-java wiki, Endpoints (Chrome `fcm.googleapis.com`, `android.googleapis.com`; Firefox `updates.push.services.mozilla.com`): https://github.com/web-push-libs/webpush-java/wiki/Endpoints

Secondary (vendor or press; marked where used):
- OneSignal, Mobile Web Push for iOS/iPadOS (16.4+, remove-and-re-add after deny, Chrome/Edge on iOS): https://documentation.onesignal.com/docs/web-push-for-ios
- PushAlert, Chrome on iOS 113 web push: https://pushalert.co/blog/?p=2506
- Apple's iOS 17.4 EU Home Screen web app removal and reversal: https://www.macrumors.com/2024/02/15/ios-17-4-web-apps-removed-apple/ and https://www.engadget.com/apple-backtracks-on-plans-to-get-rid-of-web-apps-on-iphones-in-the-eu-195232177.html
- Push-service host lists (Apple `*.push.apple.com`, Windows `*.notify.windows.com`, Samsung): https://kiwix.lema.org/stackoverflow.com_en_all_2023-11/questions/52252877/which-host-ports-should-be-open-for-web-push and https://github.com/hackutd/harp/pull/153 (unmerged)

Code checked (all under `/home/grier/git/fold`): `docs/plan/PLAN.md` (5.3, 5.5, 5.6, 5.12, 7.1–7.8, 10.1–10.3, 13.2, 14.1, 14.2, 14.6, 15.1 through DEC-120), `internal/notify/kinds.go` (Kind has `AllowEmail` only; `Required = CatSafety || Waiting`; `OptionalEmailCategories`), `internal/notify/notify.go` (`Send`, `Msg`), `api_notifications.go` (`deliverNotificationEmails` after commit, `emailOptOuts`, `recordDelivery` with unique `(notification_id, channel)`), `api_email_settings.go`, `email_unsubscribe.go`, `web/src/components/EmailSettings.tsx`, `internal/jobs/jobs.go` (`Kind`, `Enqueue`, `CleanError`), `internal/providers/providers.go`, `api_messages.go` (`channel` must be `email`; `smsEnabled`), `messages_audience.go` (skip reasons), `main.go` (`contentSecurityPolicy`, `serveSPA` cache headers, `csrfExemptRoutes` comment, `/help/members`), `web/index.html` (no manifest), `web/vite.config.ts` (proxy `/api`, `/uploads` only), `web/public/assets/brand/fold-icon-1.svg` (only icon), `web/playwright.config.ts` (Chromium only), `migrations/00044_consents.sql` (channel CHECK `email|sms`; scope regex), `migrations/00011_m1_2_email.sql` and `00054_messages.sql` (channel CHECKs), latest migration `00055_outbox_html.sql`, `api_people.go` (`peopleSelectionCipher`, the `SESSION_SECRET`-derived AES-GCM pattern), `access_log.go` (`secretPathPrefixes`), `go.mod` (Go 1.26, toolchain 1.27.3; no JWT library), `config.go` (`SITE_URL`, `FOLD_EMAIL_FROM`).
