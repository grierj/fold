# Fold design: 10. Security and privacy

## 10. Security and privacy

### 10.1 Baseline (keep and extend)

- Keep: HTTPS through Caddy, Secure/HttpOnly/SameSite=Lax cookies, CSRF tokens, bcrypt, session invalidation on password change, upload content sniffing, `nosniff`, and security headers. Images are upload-only; the URL-fetch path and its SSRF guard were removed (DEC-49).
- Add:
  - Trusted proxies (C17): Gin trusts `X-Forwarded-For` only from `FOLD_TRUSTED_PROXIES`. The compose file gives the internal network a fixed subnet, and that subnet is the default. With the wrong value, a test page at Admin > System shows the IP Fold sees.
  - CSRF route groups (C17): every state-changing route uses CSRF except `/hooks/*` (provider signature required) and `POST /u/unsubscribe/<token>` (signed token required). A test asserts that list stays exactly that.
  - Password reset and invite tokens: single use, hashed, valid 1 to 24 hours.
  - No account enumeration (final review, finding 4): sign-up, guest registration, password reset, and login give the same response, in about the same time, whether or not an email has an account. The email itself carries the hint ("you already have an account; sign in or reset your password").
  - Email change (DEC-87): starting a change requires the signed-in person's current password. Fold emails a verification link to the new address and a cancellation link to the old address. Opening the new-address page makes the SPA redeem the verification capability with a CSRF-protected POST; an HTTP GET alone does not mutate the account. Opening the cancellation page is read-only until the person explicitly confirms its CSRF-protected POST. A new request invalidates the previous verification/cancellation pair. Verification changes the email and ends every other session. Cancellation leaves the email unchanged and ends every current session for that account, including the browser making the request; it cannot roll back an email that was already verified.
  - Email-change attempts keep the #157 limits: after current-password proof, an existing account address answers 409 and spends the actor's 3/hour and 10/day budget. An exhausted actor gets the normal 200 without an address lookup, target-budget use, token change, or email. Public sign-up and recovery answers stay uniform.
  - Account-token revocation: setting, changing or resetting a password, an admin password reset, a verified or admin email change, disabling, or deleting an account atomically revokes every unused account token of any kind. These credential changes advance `session_version`; self-service password and verified email changes may preserve the approved current browser, while admin changes, disable, delete, and email-change cancellation end all current sessions for the subject.
  - TOTP two-factor sign-in, required for every admin from v0.5 (M4.9), with one-time recovery codes.
  - Login throttling by email and by client IP (DEC-23), stored in Postgres so it survives restarts. Per-IP budgets group IPv6 clients by /64 and retain individual IPv4 addresses (Grier, Oct 28, 2026; #159).
  - Known gap, deliberately not built (DEC-41): sessions live in a signed cookie, so logout deletes the browser's cookie but a copy taken before logout stays valid until it expires (7 days) or `session_version` is bumped. Server-side session storage or a "log out everywhere" control would close this. Revisit if the threat model changes.
  - Rate limits on public endpoints (guest signup, account requests), with a hidden honeypot field instead of a third-party CAPTCHA. Limits apply per IP and per target, with these defaults (admins can change them):
    - guest verification emails: 3 per address per hour and 10 per day, as well as per IP (M1.4);
    - account requests and guest sign-ups: separate site-wide 100/hour caps; honeypot-flagged submissions send no receipt, account hint or admin email (Grier, Oct 28, 2026; #159). These site caps are fixed constants until settings controls exist;
    - leader invitations: 20 valid attempts per leader per rolling day, including existing-account and duplicate-invitation refusals; actor allowance is checked before address lookup (M1.2, #157);
    - HELP and other automatic SMS replies: 1 HELP reply per number per 10 minutes and 5 automatic replies per number per day (7.4, M3.7);
    - phone verification texts: 3 per number per day (7.4, M3.7);
    - direct-contact requests: 10 open per leader (5.11, M1.11).
  - Signature checks on every provider webhook.
  - Security headers set by the app (M0.3b, DEC-37): a Content-Security-Policy for the SPA and `/setup`, HSTS when cookies are secure, and a `Permissions-Policy`, plus the existing `nosniff`, referrer, and frame headers. Notifications add no CSP origin (5.12). Calls are same-origin (`connect-src 'self'`, no websockets), icons are inline SVG, and text renders as plain text.
  - Notifications (5.12, DEC-42):
    - Rows carry display names and in-app links only, never a teen's or a hidden adult's contact details (DEC-31). Sensitive kinds (minors, consent, contact access) send an email that names nobody and links in.
    - The SPA keeps notification data in memory only (never localStorage), so logout leaves nothing behind.
    - A user can read and change only their own rows.
    - An inline action is accepted only for allow-listed kinds; the server enforces this, not the UI.
    - Rows are deleted with the user, and `AuditLog` keeps the record.
  - Web push (M3.13, DEC-125): outbound only to allow-listed push-service hosts over https, resolved-IP checks against private ranges, no redirects, 3 KB payloads; endpoints and keys never logged or returned; lock-screen text is each kind's own short `PushRender` (never the bell body cut short), the safe text for sensitive kinds; the service worker has no fetch handler; the logout request itself removes the browser's device.
- Provider secrets are encrypted at rest (C8) and never sent back to the browser after saving. The UI shows "set, last checked on ...".
- `AuditLog` (created in M0.2) covers admin actions, sends, approvals, consent changes, role and secret changes, and every contact-access attempt (5.3, 5.11).
- Export: admins can export people, groups, and events as CSV. Users can download their own data as JSON.
- Deletion: deleting a user removes personal data (name, email, phone, avatar) but keeps anonymous registrations for attendance counts. If the account is the only leader of any group, the admin first picks a successor for each (a member of that group or any admin, the deleting admin by default); the handoff is audited (DEC-36, M0.3b). The same holds for the only non-deleted host of a church-wide event: the admin first picks an eligible replacement host (active, adult, never chosen automatically) for each such event, and the host changes are audited in the same transaction (M7.4 owner decision 3, DEC-144). Disabling uses the same successor picker for groups but keeps the account's memberships, host rows and registrations, hidden and uncounted, as plain member; re-enabling restores them as plain member and attendee, never leader, with anything over a limit left out and the person told to ask a leader (DEC-38, DEC-47). Opt-out and suppression records keep a hashed address so the person is never messaged again by mistake.

R4-2d2 implementation note, Nov 6, 2026, staged locally until M74-DONE. Under owner decision 8,
recorded 2026-11-06T00:34:36Z, a reused address keeps its retained optional-email categories off until
its current holder affirmatively turns them on. The approved design and review are
`r42d2-design-rev2.json` and `r42d2-review-rev2.json` on the autoflow host. Erasure exports only the held
address's hash, optional category and original choice time. `readEmailOff` combines account and actual
destination choices; defaults and earlier opt-ins never override a retained record. A signed-in holder
or a version 2 link bound to the locked current address can clear that category's record. Moving an
address leaves its records behind and applies the new address's choices; settings disclose no earlier
identity or history. Legacy tombstones are not reconstructed. Required mail, push and provider
suppressions remain separate.

Delivery honors choices committed before each effective-choice statement starts. Later changes may not
stop that one in-flight send. Batch rows and optional notifications recheck within each claim transaction;
the composer test checks under the author's lock, commits, then calls the provider. An opt-out, erasure
or address change committed after that commit may not stop the one test send. Inventory
[7.6](../M7.4-ERASURE-INVENTORY.md#76-address-level-opt-outs-as-implemented-locally) and
[15.8](../M7.4-ERASURE-INVENTORY.md#158-r4-2d2-implementation-and-prior-verification-local-nov-6-2026)
record behavior, limits and prior evidence. O-1 is implemented locally only; final integration verification,
review and landing remain pending. C2 needs PUB-1's merge, publication is pending, and M7.4 is incomplete.
This note records the existing owner decision, not a new policy or completion decision.

### 10.2 Directory and contact details

- Default: members see other members' display names and avatars only inside groups they share. No church-wide directory until M6.1.
- M6.1 adds a church directory with per-field visibility chosen by each person: `everyone at church`, `my groups`, `leaders and admins`, `only admins`. Defaults: name and photo = everyone at church; email and phone = leaders and admins; address and birthday = only admins. Being listed at all is opt-in.
- Leaders and hosts see adult contact details for members of their own groups and registrants of their own events, because they need them to run the group, unless the adult turns on "Hide my contact info from group leaders" (DEC-31). They never see teens' contact details (5.11). The privacy page says so.
- When an invite-only group's event is opened to `private` or `public`, only leaders and admins see its attendee names, so a recovery or divorce-care group's members aren't revealed (5.3, M1.1b).
- Guests never appear in any directory.

### 10.3 Minors, households, and childcare (DEC-8, DEC-18 to DEC-20)

- **No accounts for anyone under 13**, by any door (self-signup, invite, import, admin). The age gate is in 5.10. Children under 13 exist only as `HouseholdPerson` rows (`kind=child`) in a household, created by a guardian or an admin, with no login, email, or phone. Fold collects their details from the parent, not from the child. This is meant to keep Fold out of COPPA's main scope (not legal advice, DEC-14).
- **Teens (13 to 17)** can have accounts with consent. The account stays `pending_consent` until `account` consent is granted by guardians and admins; the leader role never grants consent:
  - by a confirmed guardian, or
  - by an admin, with evidence text.
  - Consent is append-only and can be revoked. Revoking `account` consent disables the account but keeps its history.
- Teens with an active account may turn on push for their own device; it carries no contact detail and no cost, so no guardian consent is needed (DEC-122). Guardian copies stay email-only (DEC-29).
- Teens without their own email can instead be household people (`kind=teen`) whom a guardian registers for events.
- Every guardian or household link to a teen or child needs an admin to confirm it (DEC-32), even when the guardian creates the household person or turns a household teen into an account. Nothing is auto-confirmed, so a stranger can't add a real teen's email to their household and become that teen's guardian (final review, finding 2).
- Teens may register for any event they can see (DEC-20). The safeguard is visibility plus leader control, not a youth-only flag or an approval gate: Teen and Child badges appear on attendee lists, rosters, and recipient lists, and leaders and hosts remove registrations that shouldn't be there (DEC-30). Other members see teens and children only as a count.
- Teens are never in the directory and have no public avatar. They cannot lead groups or host events (DEC-18). Their contact details are visible only to admins, their confirmed guardians, and themselves. Leaders and hosts reach them through Fold (5.11, 7.8) and see direct contact only with a grant that both an admin and the teen approved (DEC-31).
- Guardian copies of messages to teens are off by default; admins can turn them on church-wide (DEC-19, 5.10). Many safe-church policies expect copies, so the setup checklist asks the admin to decide.
- Birth dates of teens and household people are visible only to admins and guardians. Leaders see an age (for example "age 15").
- Care notes on household children (allergies, notes) are visible to admins and guardians. Leaders or hosts of an event see the notes of children registered for it, from 24 hours before to 24 hours after the event. Later, a "childcare coordinator" role can see them too. Drop-in childcare that doesn't name children still uses `childcare_count` and an age note.
- Photos: galleries default to private assets visible to group members only (C9). `HouseholdPerson.photo_consent` and `TeenConsent` scope `photos` control whether a child or teen may appear in galleries. Both default to no.
- Background-check flags (M6.8): store the status, cleared date, expiry date, provider name, reference number, and who entered it. Never store the report. Volunteer slots can be marked "requires a current background check"; people without a current `cleared` status cannot claim them and see a polite explanation. Only admins (and an optional safety-coordinator role) see the flags. Each person can see their own status and expiry.

### 10.4 SMS consent (TCPA and 10DLC)

- Opt-in only, with recorded evidence (7.4). Phone numbers are never imported as opted in by default. A CSV import can mark consent only if the admin ticks "I have written consent for these numbers" (audited).
- Opt-out is applied automatically and at once, for keywords and for any reply that reasonably reads as an opt-out (7.4, DEC-27), well inside the FCC's 10 business days.
- Carrier registration (10DLC or toll-free verification) needs a public privacy policy and SMS terms page. Telnyx's campaign guidance requires opt-in flows to link both, and Twilio has required both URLs on new 10DLC campaigns submitted by API since June 30, 2026. Fold generates `/legal/privacy` and `/legal/sms-terms` from church settings (name, address, contact email, message types, frequency, "Msg & data rates may apply", STOP and HELP), so the church has them on day one. An admin must review and accept the text first.
- The opt-in screen and confirmation text match what the registration describes, because carriers check.
- Registration path (DEC-3). Fold assumes churches are **not** 501(c)(3) organizations and never asks for 501(c)(3) proof.
  - Fold never picks a charity use case. Twilio's docs say the 10DLC `CHARITY` use case "does not include religious organizations"; Telnyx describes its `NON_PROFIT` entity type as "501(c)(3) or equivalent" and requires vetting. Ask the provider which entity type fits; never claim nonprofit tax status.
  - With an EIN: an EIN-based standard brand with a Low-Volume Mixed or Mixed campaign, or toll-free verification with the EIN as the business registration number.
  - Without an EIN: get a free EIN from the IRS. On Telnyx (the default) there is **no sole-proprietor fallback for a church**: Telnyx's help center says an entity whose name or website contains words such as "Church" or "Ministry", or a `.org` domain, does not qualify as a sole proprietor and must register a standard brand with an EIN. Twilio's sole-proprietor path is designed for individuals and has tight limits.
  - Details and limits are in 12.2.
- Keep SMS to logistics (events, groups, reminders), not donation asks. Donation asks can be classed as charity traffic, which needs 501(c)(3) proof (Twilio error 30487). The composer warns (7.3).
- Lawyer review is case by case (DEC-14). The generated legal pages are labeled as templates, not legal advice; each church is responsible for its own wording. The hope is that Grier rarely or never hosts for others. Before any church goes live, whoever runs it should read 10.3 to 10.5.

### 10.5 Email (CAN-SPAM and inbox rules)

- An unsubscribe link and one-click headers on every non-transactional message, the church's postal address in the footer, and an honest From name and Subject.
- SPF, DKIM, and DMARC set up by the wizard (11.3). Large mailbox providers expect these, plus one-click unsubscribe, from bulk senders.
- From address: `"<author> via <church name>" <messages@mail.<church domain>>`, with Reply-To set to a relay address, so replies come back through Fold and nobody's personal address is exposed (7.8, DEC-31). Without inbound mail (SMTP only), Reply-To is the church office.

### 10.6 Hardening checklist (Fold is on the public internet)

DEC-30's "trust the church body" covers workflow only: no approval gates on sign-ups. It never weakens these controls. Each item has an owner milestone, and a release is not done until every item that applies to it is checked. Checked Phase-1 items reflect implemented controls and repository tests, not a fresh security audit or a verified live deployment. Mixed items are split so future providers and deployment work are not marked complete with Phase 1.

- [x] Account approval: self-signup and member "invite a friend" need admin approval (DEC-4; M1.3)
- [x] Authentication: bcrypt (passwords at most 72 bytes, DEC-90), email login, single-use hashed reset and invite tokens, other sessions ended on password, email, or role change and on disable, Secure/HttpOnly/SameSite cookies (M0.3, M1.2; seven-day decoding lifetime fixed in #163)
- [ ] Admin two-factor sign-in (TOTP), required for every admin from v0.5 (M4.9)
- [x] Login throttling by email and client IP, stored in Postgres (M0.3)
- [x] Public-form IP/target budgets, signup honeypot and site-wide abuse caps; per-leader invitation limits; open direct-contact request limits; current-password proof budgets (M1.2, M1.3, M1.4, M1.11; #163, #168–#170)
- [ ] Caps on HELP, automatic SMS replies and phone verification texts; per-author and per-recipient message limits (M3.7, M3.8)
- [x] CSRF on every state-changing route except signature- or token-authenticated hooks and one-click unsubscribe (M0.1)
- [x] Trusted proxies set to the compose network only (M0.1)
- [x] Server-side validation on current plain-text APIs; React escapes output; uploaded images only, with no server fetch of user-supplied image URLs (all shipped milestones, DEC-49)
- [ ] Rich-text sanitizing with bluemonday and SSRF guard on URL fetch when those surfaces are built (M3.4)
- [x] Uploads: type sniffing and size limits on current public avatars and group covers
- [ ] Private assets behind policy checks (M4.7)
- [ ] Secrets: provider secrets encrypted at rest, never returned to the browser, `.env` mode 600 (M4.1)
- [x] Least-privilege permissions: every check in `internal/policy` with table tests; invite-only stays 404 to outsiders (M0.2)
- [x] Transactional audit rows for shipped admin actions, password-reset/invitation actions, consent, roles, settings, links of minors, and contact-access attempts; rows about contact access, minors, consent, and roles are never purged (M0.2 through Phase 1)
- [ ] Extend audit coverage to future message sends, provider secrets and new settings as those surfaces land (M3 onward)
- [x] Leaders remove members (never an admin or the last leader) and sole-leader accounts hand off to a chosen successor before deletion or disabling; both audited (M0.3b, DEC-35, DEC-36)
- [x] App security headers: HSTS, CSP, `nosniff`, frame, referrer, and permissions policies (M0.3b, DEC-37)
- [ ] Live TLS everywhere (Caddy), plus Caddy and image-level header checks for release deployment (M5.1)
- [ ] Webhook signature checks on every provider, including inbound mail (M3.2, M3.7, M3.11)
- [x] No account enumeration: sign-up, guest registration, password reset, and failed login use uniform public answers; account-specific hints go by email. Public-form throttles are silent (M0.3, M1.2, M1.3, M1.4, DEC-52; #163, #168, #169)
- [x] Email change: current-password proof starts a newest-only verification/cancellation pair; the new address is verified before it takes effect, the old address can cancel a pending change and end all subject sessions, and no GET mutates the account (DEC-87, M1.2; #154 in #163)
- [ ] SMS opt-in only after proof of phone ownership: a texted code or a YES reply (M3.7)
- [x] Contact privacy: hidden adults' details need their grant; teens' details need both teen and admin approval; contact-access attempts are audited (M1.11, DEC-31)
- [ ] Replies relayed through Fold (M3.11)
- [x] Guardian and household links of teens and children confirmed only by an admin (M1.9, M1.10, DEC-32, DEC-79)
- [x] An opened invite-only event never reveals the group's members or leaders (M1.1b)
- [x] Leader grants audited, with an in-app notice to admins (M0.2)
- [x] Notifications carry names and in-app links only. Sensitive kinds use link-only email. The client keeps them in memory only. Inline actions are allow-listed on the server (5.12; M1.1c, M1.2)
- [ ] `fold push check` reaches each push host; the allow-list is the only egress push makes (M3.13)
- [ ] Later, not v1: durable, tamper-evident audit log for the most important events (DEC-33, 10.7, M6.14). The regular `AuditLog` covers them until then.
- [ ] Backups: nightly, retained, restore-tested in CI, offsite encrypted with `age`; backup check before migrations (M5.2, M5.3)
- [x] Dependency automation: Dependabot for Go modules, npm, GitHub Actions and base images; `govulncheck` and `npm audit` in CI (M0.3b, DEC-37)
- [ ] Pinned image digests in releases (M5.1)
- [ ] Server: unattended OS security updates and a firewall allowing only 22, 80, and 443 (Appendix A)

### 10.7 Durable audit log (later, DEC-33)

Not v1 scope; milestone M6.14. Until then, the regular `AuditLog` (created in M0.2) records all of these events, and they are never purged by cleanup or user deletion. The durable log hardens that record against later editing or loss, in case of legal trouble.

- **What it covers** (the really important events): contact-access requests, decisions, grants, reveals, revocations, and every failed, abandoned, or expired attempt (DEC-31); guardian and household links of teens and children (DEC-32); teen consent; role and leadership changes; visibility changes that make an event more open than its group; SMS opt-outs and opt-ins; and admin overrides (send window, double-bookings, consent granted with evidence text).
- **Ideas to settle in M6.14** (a sketch, not a design):
  - Append-only storage: a separate table that the app's database role can only `INSERT` into and `SELECT` from, with no `UPDATE` or `DELETE` grant, plus a trigger that rejects changes anyway.
  - Hash chaining: each row stores `prev_hash` and `hash = SHA-256(prev_hash + canonical row)`, so an edited, deleted, or reordered row breaks the chain. `fold audit verify` and the Admin > System page check the chain.
  - Write-once offsite export: the `backup` container uploads a daily signed segment to an S3-compatible bucket with object lock, for example Backblaze B2 Object Lock in compliance mode. Backblaze says Object Lock has no extra cost beyond normal storage and supports retention periods of 1 to 3,000 days (https://www.backblaze.com/docs/cloud-storage-object-lock). A locked file can't be deleted early even by the church, so the retention period must be chosen with care.
  - Retention rules: a church-set period per event type, with legal holds. Fold's default keeps everything; the church decides with its own advisor (DEC-14). The privacy page explains that these records survive account deletion.
  - Admin export for legal requests: filter by person and date range; CSV plus JSON with the hash chain and a verification report; the export itself is logged.
- **Acceptance to write before work starts**: verification catches any edited, deleted, or reordered row; the offsite segments can't be deleted before retention ends; an export for one person verifies on its own.

