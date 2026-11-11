# Fold design: 10. Security and privacy

## 10. Security and privacy

### 10.1 Baseline (keep and extend)

The rules agents must follow while coding (CSRF, throttles, sessions, uploads, errors) are in
[`AGENTS.md`](../../../AGENTS.md); erasure is in [`docs/agents/erasure.md`](../../agents/erasure.md)
and email choices in [`docs/agents/mail.md`](../../agents/mail.md). This section says what Fold
promises.

- Transport and cookies: HTTPS through Caddy. Session cookies are Secure, HttpOnly and
  SameSite=Lax, and expire after 7 days. Passwords use bcrypt (DEC-90).
- Trusted proxies (C17): Gin trusts `X-Forwarded-For` only from `FOLD_TRUSTED_PROXIES`. The
  default is the Compose network's fixed subnet, `172.30.0.0/24`. Fold refuses to start with a
  `/0` range. **Not built, no milestone yet:** an Admin > System page that shows the IP Fold sees,
  for checking a wrong value.
- CSRF (C17): every state-changing route needs the token except the two routes in
  `csrfExemptRoutes`: `POST /hooks/postmark/email` (HTTP Basic secret from the webhook URL) and
  `POST /u/unsubscribe/:token` (signed token). `TestCSRFExemptRoutesAreExact` keeps that list
  exact. Sign-in drops the old CSRF token, so the next request gets a new one.
- Account tokens (password reset, invitation, email verification and cancellation): single use,
  stored hashed, valid 24 hours (`tokenTTL`).
- No account enumeration (final review, finding 4; DEC-52): sign-up, guest registration, password
  reset and login give the same answer, at about the same cost (a dummy bcrypt check), whether or
  not the email has an account. The email itself carries the hint ("you already have an account;
  sign in or reset your password").
- Email change (DEC-87): starting one needs the current password. The new address gets a
  verification link and the old address a cancellation link; a newer request replaces both. Both
  pages act only on a CSRF-protected POST, never on a GET. Verifying changes the email and ends
  every other session. Cancelling keeps the old email and ends every session, including the one
  cancelling; it cannot undo a change already verified. An address that already has an account
  answers 409 and spends the changer's budget (3 an hour, 10 a day); once that budget is spent the
  answer is the usual 200, with no lookup and no email.
- Token revocation: setting, changing or resetting a password, an admin password reset, a verified
  or admin email change, disabling and deleting each revoke every unused account token in the same
  transaction, and advance `session_version`. Self-service password and email changes keep the
  current browser signed in; admin changes, disabling, deleting and email-change cancellation end
  every session.
- Logout ends every session of the account: it advances `logout_version`, so a cookie copied before
  logout stops working (M7.2). It also removes this browser's push device.
- **Planned (M4.9):** TOTP two-factor sign-in, required for every admin from v0.5, with one-time
  recovery codes.
- Login throttling (DEC-23), stored in Postgres (`login_throttles`) so it survives restarts: 10
  failures per email and 50 per client IP in 15 minutes, then a 15-minute lock. Per-IP budgets
  group IPv6 clients by /64 and keep each IPv4 address separate.
- Public forms (guest sign-up, account requests) have a hidden honeypot field instead of a
  third-party CAPTCHA. A honeypot-flagged submission sends no receipt, account hint or admin email.
  Every limit below is a fixed constant in code, and a refused request gets the form's usual answer
  (DEC-52). Admin controls to change these defaults were planned but have no milestone yet. The
  limits:
  - guest verification emails: 3 per address per hour and 10 per day, plus a per-IP budget (M1.4);
  - account requests and guest sign-ups: a site-wide cap of 100 an hour for each;
  - leader invitations: 20 valid attempts per leader per rolling day, including refusals for an
    existing account or a duplicate invitation; the leader's allowance is checked before any
    address lookup (M1.2);
  - direct-contact and add requests: 10 open per requester (5.11, M1.11);
  - announcements: 5 emails per group (or church-wide event) per leader or host per church day;
    admins have no limit (M3.5);
  - **Planned (M3.7):** 1 HELP reply per number per 10 minutes, 5 automatic replies per number per
    day, and 3 phone verification texts per number per day (7.4).
- Webhooks: the Postmark delivery webhook checks the HTTP Basic secret in its URL
  (`FOLD_POSTMARK_WEBHOOK_SECRET`) and answers 404 while that is unset. **Planned (M3.7, M3.11):**
  SMS and inbound-mail webhooks check the provider's signature.
- Security headers (M0.3b, DEC-37), set by the app on every response (`internal/httpx`): a
  Content-Security-Policy (scripts, styles, fonts, workers and the manifest from this origin only;
  `connect-src 'self'`; `frame-ancestors 'self'`), `X-Content-Type-Options: nosniff`,
  `Referrer-Policy: same-origin`, `X-Frame-Options: SAMEORIGIN`, a `Permissions-Policy` that turns
  off camera, microphone and geolocation, and HSTS when cookies are Secure. Notifications add no
  CSP origin (5.12). Icons are inline SVG and user text renders as plain text.
- Uploads: images are upload-only; the URL fetch and its SSRF guard were removed (DEC-49). The
  server sniffs the type (JPEG, PNG or GIF), caps the size at 5 MB, re-encodes the image (no EXIF)
  and serves it with `nosniff`.
- Notifications (5.12, DEC-42):
  - Rows carry display names and in-app links only, never a teen's or a hidden adult's contact
    details (DEC-31). Sensitive kinds (minors, consent, contact access) send an email that names
    nobody and links in.
  - The SPA keeps notification data in memory only (never localStorage), so logout leaves nothing
    behind.
  - A user can read and change only their own rows.
  - An inline action is accepted only for allow-listed kinds; the server enforces this, not the UI.
  - Erasing an account deletes its rows; `AuditLog` keeps the record.
- Web push (M3.13, DEC-125): Fold sends only to allow-listed push-service hosts over https, checks
  each resolved IP against private ranges, follows no redirects, and caps a payload at 3,000 bytes
  before encryption. Endpoints and keys are never logged or returned. Lock-screen text is each
  kind's own short `PushRender` (never the bell body cut short), and sensitive kinds use their safe
  text. The service worker has no fetch handler.
- Provider secrets: today the Postmark token and webhook secret live only in the environment, and
  no API returns them. **Planned (M4.1):** provider secrets stored in the database, encrypted at
  rest with `FOLD_SECRET_KEY` (C8), never sent back to the browser after saving; the UI shows
  "set, last checked on ...".
- `AuditLog` (M0.2) records admin actions, message sends, approvals, consent changes, role and
  setting changes, and every contact-access attempt (5.3, 5.11), in the same transaction as the
  change.
- Export: an admin can download one person's contact history as CSV. **Planned (M7.8):** an admin
  people CSV export and each person's own-data download, both audited. CSV export of groups and
  events has no milestone yet.
- Deletion (DEC-36, DEC-144): deleting an account erases the person through one pipeline
  (`eraseAccountIn`; details in [`docs/agents/erasure.md`](../../agents/erasure.md)). Only an
  anonymous tombstone stays, so past head-counts and audit history hold together.
  - If the account is the only leader of a group, the admin first picks a successor for each (a
    member of that group or any admin, the deleting admin by default). The handoff is audited.
  - If it is the only remaining host of a church-wide event, the admin first picks a replacement
    host for each such event (active, adult, never chosen automatically). The host changes are
    audited in the same transaction (DEC-144, amended).
  - Disabling uses the same successor picker for groups. It keeps memberships, host rows and
    registrations, hidden and uncounted, as plain member. Re-enabling restores them as plain
    member and attendee, never leader; anything over a limit is left out, and the person is told
    to ask a leader (DEC-38, DEC-47).
- Retained email choices (M7.4, owner decision 8): erasure keeps each optional-email opt-out as
  an address hash in `email_address_optouts`. A reused address keeps that category off until its
  new holder turns it on. Provider suppressions also match by address hash, so a person is never
  emailed again by mistake. The reading and send-time rules are in
  [`docs/agents/mail.md`](../../agents/mail.md); the inventory
  ([7.6](../M7.4-ERASURE-INVENTORY.md#76-address-level-opt-outs-as-implemented-locally)) records
  what is kept and why.

### 10.2 Directory and contact details

- Default: members see other members' display names and avatars only inside groups they share.
  There is no church-wide directory.
- **Planned (M6.1):** a church directory with per-field visibility chosen by each person:
  `everyone at church`, `my groups`, `leaders and admins`, `only admins`. Defaults: name and photo
  = everyone at church; email and phone = leaders and admins; address and birthday = only admins.
  Being listed at all is opt-in.
- Leaders and hosts see adult contact details for members of their own groups and registrants of
  their own events, because they need them to run the group, unless the adult turns on "Hide my
  contact info from group leaders" (DEC-31). They never see teens' contact details (5.11). The
  privacy page says so.
- When an invite-only group's event is opened to `private` or `public`, only leaders and admins
  see its attendee names, so a recovery or divorce-care group's members aren't revealed (5.3,
  M1.1b).
- Guests never appear in any directory.

### 10.3 Minors, households, and childcare (DEC-8, DEC-18 to DEC-20)

- **No accounts for anyone under 13**, by any door (self-signup, invite, import, admin). The age
  gate is in 5.10. Children under 13 exist only as `HouseholdPerson` rows (`kind=child`) in a
  household, created by a guardian or an admin, with no login, email, or phone. Fold collects their
  details from the parent, not from the child. This is meant to keep Fold out of COPPA's main
  scope (not legal advice, DEC-14).
- **Teens (13 to 17)** can have accounts with consent. The account stays `pending_consent` until
  `account` consent is granted; the leader role never grants consent:
  - by a confirmed guardian, or
  - by an admin, with evidence text.
  - Consent is append-only and can be revoked. Revoking `account` consent disables the account but
    keeps its history.
- Teens with an active account may turn on push for their own device; it carries no contact detail
  and no cost, so no guardian consent is needed (DEC-122). Guardian copies stay email-only
  (DEC-29).
- Teens without their own email can instead be household people (`kind=teen`) whom a guardian
  registers for events.
- Every guardian or household link to a teen or child needs an admin to confirm it (DEC-32), even
  when the guardian creates the household person or turns a household teen into an account.
  Nothing is auto-confirmed, so a stranger can't add a real teen's email to their household and
  become that teen's guardian (final review, finding 2).
- Teens may register for any event they can see (DEC-20). The safeguard is visibility plus leader
  control, not a youth-only flag or an approval gate: Teen and Child badges appear on attendee
  lists, rosters, and recipient lists, and leaders and hosts remove registrations that shouldn't
  be there (DEC-30). Other members see teens and children only as a count.
- Teens are never in the directory and have no public avatar. They cannot lead groups or host
  events (DEC-18). Their contact details are visible only to admins, their confirmed guardians,
  and themselves. Leaders and hosts reach them through Fold (5.11, 7.8) and see direct contact
  only with a grant that both an admin and the teen approved (DEC-31).
- Guardian copies of messages to teens are off by default; admins can turn them on church-wide
  (`guardian_copies`; DEC-19, 5.10). Many safe-church policies expect copies, so the setup
  checklist (planned, M4.2) asks the admin to decide.
- Birth dates of teens and household people are visible only to admins and guardians. Leaders see
  an age (for example "age 15").
- Care notes on household children are visible to admins and guardians. Leaders or hosts of an
  event see the notes of children registered for it, from 24 hours before to 24 hours after the
  event. Later, a "childcare coordinator" role could see them too (no milestone yet). Drop-in
  childcare that doesn't name children uses `childcare_count` and an age note.
- Photos: `HouseholdPerson.photo_consent` and the `TeenConsent` scope `photos` exist and default
  to no. **Planned (M6.9):** galleries of private assets visible to group members only (C9); those
  two consents decide whether a child or teen may appear in them.
- **Planned (M6.8):** background-check flags. Store the status, cleared date, expiry date,
  provider name, reference number, and who entered it. Never store the report. Volunteer slots can
  be marked "requires a current background check"; people without a current `cleared` status
  cannot claim them and see a polite explanation. Only admins (and an optional safety-coordinator
  role) see the flags. Each person can see their own status and expiry.

### 10.4 SMS consent (TCPA and 10DLC)

**Planned (M3.7, M3.8, M4.3, M4.6):** Fold sends no SMS yet. This section is the plan.

- Opt-in only, with recorded evidence (7.4). Phone numbers are never imported as opted in by
  default. A CSV import can mark consent only if the admin ticks "I have written consent for these
  numbers" (audited).
- Opt-out is applied automatically and at once, for keywords and for any reply that reasonably
  reads as an opt-out (7.4, DEC-27), well inside the FCC's 10 business days.
- Carrier registration (10DLC or toll-free verification) needs a public privacy policy and SMS
  terms page. Telnyx's campaign guidance requires opt-in flows to link both, and Twilio has
  required both URLs on new 10DLC campaigns submitted by API since June 30, 2026. Fold generates
  `/legal/privacy` and `/legal/sms-terms` from church settings (name, address, contact email,
  message types, frequency, "Msg & data rates may apply", STOP and HELP), so the church has them on
  day one (M4.3). An admin must review and accept the text first.
- The opt-in screen and confirmation text match what the registration describes, because carriers
  check.
- Registration path (DEC-3). Fold assumes churches are **not** 501(c)(3) organizations and never
  asks for 501(c)(3) proof.
  - Fold never picks a charity use case. Twilio's docs say the 10DLC `CHARITY` use case "does not
    include religious organizations"; Telnyx describes its `NON_PROFIT` entity type as "501(c)(3)
    or equivalent" and requires vetting. Ask the provider which entity type fits; never claim
    nonprofit tax status.
  - With an EIN: an EIN-based standard brand with a Low-Volume Mixed or Mixed campaign, or
    toll-free verification with the EIN as the business registration number.
  - Without an EIN: get a free EIN from the IRS. On Telnyx (the only planned adapter, DEC-140)
    there is **no sole-proprietor fallback for a church**: Telnyx's help center says an entity
    whose name or website contains words such as "Church" or "Ministry", or a `.org` domain, does
    not qualify as a sole proprietor and must register a standard brand with an EIN. Twilio's
    sole-proprietor path is designed for individuals and has tight limits; it would arrive only
    with a Twilio adapter (DEC-140).
  - Details and limits are in 12.2.
- Keep SMS to logistics (events, groups, reminders), not donation asks. Donation asks can be
  classed as charity traffic, which needs 501(c)(3) proof (Twilio error 30487). The composer's
  donation-ask warning (7.3) is built in TypeScript and shows once the SMS tab does (M3.7).
- Lawyer review is case by case (DEC-14). The generated legal pages are labeled as templates, not
  legal advice; each church is responsible for its own wording. The hope is that Grier rarely or
  never hosts for others. Before any church goes live, whoever runs it should read 10.3 to 10.5.

### 10.5 Email (CAN-SPAM and inbox rules)

- Every non-transactional message has an unsubscribe link and RFC 8058 one-click headers, the
  church's postal address in the footer (`church_address`), and an honest From name and Subject
  (M3.3).
- SPF, DKIM, and DMARC: large mailbox providers expect these, plus one-click unsubscribe, from bulk
  senders. Today the DigitalOcean path's `deploy/digitalocean/email-dns.sh` creates the Postmark
  sender domain and writes its SPF, DKIM and return-path records through Cloudflare, or prints
  them. **Planned (M4.4):** the setup wizard does this, DMARC included (11.3).
- From and Reply-To: announcements are From "<author> via <church name>" over the church's sending
  address (`FOLD_EMAIL_FROM`). Their Reply-To is the church office address when an admin sets one
  (`reply_to_address`, DEC-130); nobody's personal address is exposed (DEC-31). **Planned:** a
  sending subdomain, `messages@mail.<church domain>` (M4.4), and a Reply-To relay address so
  replies come back through Fold (M3.11, 7.8). Without inbound mail (SMTP only), Reply-To stays the
  church office.

### 10.6 Hardening checklist (Fold is on the public internet)

DEC-30's "trust the church body" covers workflow only: no approval gates on sign-ups. It never
weakens these controls. Each item has an owner milestone, and a release is not done until every
item that applies to it is checked. A checked item means the control is built and tested in the
repository, not that a fresh security audit or a live deployment verified it. Items that are part
built are split.

- [x] Account approval: self-signup and member "invite a friend" need admin approval (DEC-4; M1.3)
- [x] Authentication: bcrypt (passwords at most 72 bytes, DEC-90), email login, single-use hashed
  reset and invite tokens, other sessions ended on password, email, or role change and on disable,
  Secure/HttpOnly/SameSite cookies with a 7-day lifetime (M0.3, M1.2)
- [x] Logout ends every session of the account, so a copied cookie stops working (M7.2)
- [ ] Admin two-factor sign-in (TOTP), required for every admin from v0.5 (M4.9)
- [x] Login throttling by email and client IP, stored in Postgres (M0.3)
- [x] Public-form IP and target budgets, signup honeypot and site-wide caps; per-leader invitation
  limits; open-request limits; current-password proof budgets (M1.2, M1.3, M1.4, M1.11)
- [x] Per-author announcement limit (M3.5) and the push hourly limit (M3.13)
- [ ] Caps on HELP, automatic SMS replies and phone verification texts; SMS per-author and
  per-recipient limits (M3.7, M3.8)
- [x] CSRF on every state-changing route except the secret-authenticated Postmark hook and the
  token-authenticated one-click unsubscribe (M0.1, M3.2, M3.3)
- [x] Trusted proxies set to the Compose network only (M0.1)
- [x] Server-side validation on current plain-text APIs; React escapes output; uploaded images
  only, with no server fetch of user-supplied image URLs (DEC-49)
- [x] Rich-text sanitizing with bluemonday for composed messages (`internal/content`, M3.4). Fold
  fetches no user-supplied URL, so it needs no SSRF guard; any future fetch needs one.
- [x] Uploads: type sniffing, a 5 MB cap and re-encoding on public avatars and group covers
- [ ] Private assets behind policy checks (M6.9; the S3 path of M4.7 is cut, DEC-140)
- [x] `.env` mode 600: `provision.sh` writes it so and refuses to continue otherwise
- [ ] Provider secrets encrypted at rest and never returned to the browser (M4.1)
- [x] Least-privilege permissions: every check in `internal/policy` with table tests; invite-only
  stays 404 to outsiders (M0.2)
- [x] Transactional audit rows for admin actions, password-reset and invitation actions, consent,
  roles, settings, message sends, links of minors, and contact-access attempts; rows about contact
  access, minors, consent and roles are never purged (M0.2 onward)
- [ ] Audit coverage for provider secrets and future settings as those surfaces land (M4.1 onward)
- [x] Leaders remove members (never an admin or the last leader), and sole-leader accounts hand off
  to a chosen successor before deletion or disabling; both audited (M0.3b, DEC-35, DEC-36)
- [x] App security headers: HSTS, CSP, `nosniff`, frame, referrer, and permissions policies
  (M0.3b, DEC-37)
- [ ] Live TLS everywhere (Caddy), plus Caddy and image-level header checks for release
  deployment (M5.1)
- [x] Postmark delivery webhook authenticated by its URL secret (M3.2)
- [ ] Signature checks on SMS and inbound-mail webhooks (M3.7, M3.11)
- [x] No account enumeration: sign-up, guest registration, password reset, and failed login use
  uniform public answers; account-specific hints go by email; public-form throttles are silent
  (M0.3, M1.2, M1.3, M1.4, DEC-52)
- [x] Email change: current-password proof starts a newest-only verification and cancellation
  pair; the new address is verified before it takes effect; the old address can cancel a pending
  change and end all sessions; no GET changes the account (DEC-87, M1.2)
- [ ] SMS opt-in only after proof of phone ownership: a texted code or a YES reply (M3.7)
- [x] Contact privacy: hidden adults' details need their grant; teens' details need both teen and
  admin approval; contact-access attempts are audited (M1.11, DEC-31)
- [ ] Replies relayed through Fold (M3.11)
- [x] Guardian and household links of teens and children confirmed only by an admin (M1.9, M1.10,
  DEC-32, DEC-79)
- [x] An opened invite-only event never reveals the group's members or leaders (M1.1b)
- [x] Leader grants audited, with an in-app notice to admins (M0.2)
- [x] Notifications carry names and in-app links only. Sensitive kinds use link-only email. The
  client keeps them in memory only. Inline actions are allow-listed on the server (5.12; M1.1c,
  M1.2)
- [x] `fold push check` reaches each push host; the allow-list is the only egress push makes
  (M3.13)
- [ ] Durable, tamper-evident audit log: cut (DEC-140; 10.7). The regular `AuditLog` covers these
  events.
- [x] Nightly, retained backups with optional `age`-encrypted offsite copies, on the DigitalOcean
  path (`deploy/digitalocean/backup.sh`, 11.8)
- [ ] Backups restore-tested in CI, and a backup check before migrations (M5.2, M5.3)
- [x] Dependency automation: Dependabot for Go modules, npm, GitHub Actions and base images;
  `govulncheck` and `npm audit` in CI (M0.3b, DEC-37)
- [ ] Pinned image digests in releases (M5.1)
- [x] Server: unattended OS security updates and a firewall allowing only 22, 80 and 443, on the
  DigitalOcean path (`provision.sh`)

### 10.7 Durable audit log (later, DEC-33)

Cut from the roadmap (DEC-140; was M6.14). The regular `AuditLog` (M0.2) records these events, and
neither cleanup nor account deletion purges them. The design sketch is kept in
[`provenance/cut-designs.md`](../provenance/cut-designs.md); it re-enters only as a new milestone
for a named church with a legal retention need, with acceptance written first.
