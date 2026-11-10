# Web push

Read this before touching push keys, devices, push delivery, quiet hours, announcement pushes,
push reminders or the service worker. Background: M3.13, DEC-121 to DEC-129,
`docs/plan/research/web-push.md`.

## Keys and sending

- The VAPID key pair is made on boot under the migrations advisory lock and kept in `settings`
  (`vapid_public_key`, `vapid_private_key_enc`, AES-GCM with a `SESSION_SECRET`-derived key;
  `push_keys.go`). A secret change or `fold push keys rotate` makes a new pair and deletes every
  `push_subscriptions` row; a running server rereads the row on each send.
- `internal/push` is stdlib only and sends only to the allow-listed push hosts
  (`push.DefaultHosts` plus optional `FOLD_PUSH_HOSTS`) over https, with resolved-IP checks, no
  proxy and no redirects.
- **An endpoint is a capability:** never log, return or put in an error an endpoint, `p256dh`,
  `auth`, the VAPID token or a body (`jobs.CleanError` strips URL paths and tokens as a backstop;
  `TestPushLogsKeepSecretsOut`).
- Pushes share `pushTarget`, `pushHeld` (church switch, active account, consent), `pushOpensAt`
  (quiet hours, at least a second out) and `sendPush` (`push_deliver.go`). A notice's, an
  announcement's and a reminder's push differ only in what they check before and what they say.
- `push.deliver` is one job per device.

## What a push may say

- A kind pushes only with `AllowPush` and its own short `PushRender` (stored on the row, never the
  bell body cut short). Safety kinds get the safe line, titled with `site_name` at send (DEC-134).
- No push shows a place (DEC-136), contact details, signed links or a hidden group's name
  (`TestEveryKindsPushTextIsLockScreenSafe`).

## Timing

- Quiet hours (DEC-126): an optional push outside the recipient-local window (`push.Window`)
  returns `jobs.Later` and reruns every check when it opens. Required kinds and `PushSoon` date
  changes within 24 hours go at once. More than 20 optional pushes an hour are `suppressed`
  "hourly limit".
- Announcement pushes (`messages_push.go`) are `message_recipients` rows with
  `channel = 'push'`. A message's status, counts, settling, reaper and Try again read its email
  rows only, so **filter `channel = 'email'` in any new query over a message's rows.**
- The 2-hour push reminder (`reminders_push.go`) is its own reminder message (key
  `push:<occ>:<start>:120`) with only push rows and `messages.channel = 'push'`.
  `reminders.sweep` plans each person with `Window.LastAllowed`, queues `push.deliver` at that
  time, and the job rechecks the date, the event's `reminder_push`, going, visibility and the
  window (15 minutes' grace); the hourly limit never stops it. Reminder keys are `reminderKey`
  values (`reminder_key.go`) and the one SQL expression `reminderKeySQL`.

## Devices

- A trigger deletes an account's devices when its sessions are revoked or it stops being active,
  and queues `push.devices_off`, which writes the person a bell notice (renaming that job type
  needs a new migration; `job_identity_test.go` pins it).
- Holding an endpoint alone never moves a device to another account: its keys must match.
- A device belongs to the person, not the session: an expired session clears the icon count but
  leaves the subscription, and the card re-saves it on load only for the user in
  `localStorage["fold.pushOwner"]` (wiped at logout).

## Browser side

- `web/src/sw.ts` builds to `/sw.js` (served `no-cache`, registered only by the card's "Turn on
  notifications", never on load; it skips waiting and claims tabs). It has **no `fetch` handler**,
  so it can never serve a stale shell after logout.
- `/manifest.webmanifest` comes from Go (`internal/httpx/manifest.go`, church name); the icons
  come from `npm run icons`.
- The card's state code is `lib/pushState.ts`; `lib/push.ts` is the eager part (logout, the icon
  badge).
- The church's switch (`push_enabled`, Admin → Settings) hides the card and the public
  `/notifications/setup` page (via `/push/config` and `/site` `pushEnabled`) and stops every send.
- To try push under `make dev`, run `cd web && npm run build` once first: the Go server serves
  `/sw.js` from `web/dist`.
