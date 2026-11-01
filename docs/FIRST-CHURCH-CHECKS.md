# Checks to run by hand when the first church starts using Fold

An agent can't run these: they need real phones, real mail providers, a real domain or a real server. Each was built best effort and merged without them (DEC-128). Run them once, when the first church starts using Fold, and fix what you find. When a PR adds a check like this, it adds a section or an item here in the same PR.

Each item says what to do, what you should see, and the milestone it came from.

## Web push (M3.13)

Follow the directions page at `/notifications/setup` literally, on each device below. Record the iOS version you used.

- [ ] **iPhone or iPad, iOS 16.4 or newer, Home Screen app.** Add Fold to the Home Screen, open it, sign in, go to Me → Notifications and tap "Turn on notifications". You should see the iOS permission prompt only after the tap. Tap "Send a test notification", lock the phone, and see it on the Lock Screen. Then set Settings → Notifications → Show Previews to "When Unlocked": you should see the text hidden on the Lock Screen and shown after unlocking, as the directions page says.
- [ ] **Android, Chrome.** Same steps, with or without the app installed. You should see the Chrome prompt and the test notification.
- [ ] **Desktop Safari (Mac).** Same steps. You should see the Safari prompt and a macOS notification.
- [ ] **Edge on Windows.** Same steps. You should see the test notification. This also confirms the Windows push host (`notify.windows.com`) is on the allow-list.
- [ ] **The Apple and Microsoft push hosts** stay marked unsure in `docs/plan/research/web-push.md` until the checks above show each one working. Add any host that fails to the allow-list, and say so in the PR. Samsung Internet uses FCM endpoints, so it needs no host of its own.
- [ ] **`fold push check` on the server** (`smoke.sh` runs it and only warns). You should see OK for every push host. The bare name `notify.windows.com` doesn't resolve, so the check probes `wns2-bl2p.notify.windows.com` for Edge; if Edge's real endpoints use a host that fails, set it in `probeHosts` (`internal/push/allow.go`) (M3.13a).
- [ ] **Revoking.** Turn notifications off for the site in the device's own settings, then trigger a send. You should see the next send record `device gone` and the device disappear from Me → Notifications. Then use "Turn off notifications on this device" on another device and confirm the row goes.
- [ ] **Logout.** Sign out on a shared device. You should see no notification meant for the first person arrive afterwards.

## Live install (M5.1, M5.3; AGENTS.md "Deployment")

- [ ] **`docker compose up` on a real domain.** Fill `.env`, run `docker compose up --build -d`. You should see Caddy get a certificate, `https://<domain>/healthz` answer, and the setup wizard at `/setup`.
- [ ] **Volumes survive a rebuild.** Upload an avatar, then run `docker compose up --build -d` again. You should see the avatar and all data still there (`pgdata`, `uploads`).
- [ ] **`deploy/digitalocean/provision.sh` as a real run**, not `--dry-run`, on a fresh droplet the church owns, then `smoke.sh`. You should see health, TLS, and that Postgres and the app are not published. After the first dump, run the backup restore test (`--restore-test`) from `docs/DEPLOY-DIGITALOCEAN.md`.
- [ ] **Following the deployment docs** on a fresh 2 GB VPS reaches the wizard in under 30 minutes (M5.4).

## Email (M3.2, M3.3, M3.4b, M3.5, M3.6)

- [ ] **A real reminder, on time.** Make an event a day and a bit out with "1 day before", RSVP Going from one account and join the waitlist from another (with a capacity of 1), and leave the server running. You should see the reminder arrive within a few minutes of 24 hours before, with the time in the church's zone, and the waitlist note to the other account; restart the server near the reminder time and you should still get exactly one. Afterwards, as the event's leader, open that date's page: you should see "Reminder emailed to 1 person" and "Waitlist note emailed to 1 person" with the time they went, each opening its history (M3.6).
- [ ] **Postmark, a real send** on both the transactional and the broadcast stream. You should see each arrive, and Postmark's activity page show the right stream (M3.2).
- [ ] **Postmark webhooks.** Cause a delivery, a bounce and a spam complaint. You should see each reach `/hooks/postmark/email` with its Basic credentials and update the person's suppression (M3.2).
- [ ] **One-click unsubscribe over https.** In Gmail and in Apple Mail, use the client's own unsubscribe button on a Fold announcement. You should see the person's "Messages from leaders" switch turn off, with no sign-in (M3.3).
- [ ] **HTML email rendering.** Send a composed announcement with a button, a link and an image to Gmail (web, iOS and Android), Apple Mail, and Outlook desktop, in light and dark mode. You should see readable text, working links, and the church name and address in the footer. The button's padding is a known gap in Outlook desktop (M3.4b).
- [ ] **The church's SPF, DKIM and DMARC** pass for the sending domain (check a received message's headers; the wizard step is M4.4).
- [ ] **An announcement through Postmark.** Set "Where replies go" to the church office, schedule a message to a small group, and wait for it. You should see it arrive From "<your name> via <church>" in Gmail and Apple Mail (not flagged as spoofed), Reply go to the office address, the message show Sent, and a delivery report move each person to Delivered (M3.5).
- [ ] **A Postmark outage.** Point Postmark's token at a wrong value for an hour, then schedule a message. You should see the message end as Failed, with Postmark's error for an admin and plain words for its leader, and "Try again" send it once the token is right. Postmark's activity page should show one email per person, with nobody it had already reached getting a second (M3.5, DEC-131).
- [ ] **A big send's history.** Send to everyone at church (or a group of 200 or more). You should see the page count up ("Sending… 40 of 230 so far") and settle as Sent without a reload, "See who got it" list 200 names and "And 30 more", and, after Postmark's delivery reports arrive, people move from Sent to Delivered (M3.5b).
- [ ] **A guardian copy in a real inbox.** Turn on Admin → Settings → "Send guardians a copy of messages to their teens", then send to a group with a teen whose guardian is confirmed. You should see the guardian get "Copy: <subject>" with the line "Copy: sent to <teen> because your church sends guardian copies", the teen get the message as usual, and the history show an admin the guardian with "Copy for <teen>" while the leader sees only "1 guardian copy sent" (M3.5b, DEC-19, DEC-132).

## SMS (when M3.7 lands)

Nothing yet. M3.7 adds: a real Telnyx send and receive on a registered number, STOP and START from a real phone, the delivery and inbound webhooks with their signatures, and the 10DLC or toll-free registration status.
