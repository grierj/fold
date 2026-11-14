# STATUS.md

The one current status of every milestone in [`docs/plan/milestones/`](docs/plan/milestones/).
Each row says what is built, with its pull requests, or links the GitHub issues that hold the open
work. Update a row in the pull request that changes it. The older status record, with its
narratives and follow-up lists, is kept in
[`docs/plan/provenance/status-history.md`](docs/plan/provenance/status-history.md).

| Milestone | Status | PRs / issues |
| --- | --- | --- |
| M0.1 PostgreSQL only, clean baseline, CLI, request plumbing | Done | #9 |
| M0.2 Visibility enum, policy package, leadership by membership, audit log | Done | #10 |
| M0.3a Log in by email | Done | #12 |
| M0.3b Member removal, sole-leader handoff, security headers, dependency scanning | Done | #13 |
| M0.4 Frontend and browser test tooling | Done | #22 |
| M1.1a Events, time zones, occurrences, series edits | Done | #38 |
| M1.1b Church-wide hosts, event visibility, audit, attendee privacy | Done | #39, #41 |
| M1.1c Stored in-app notifications | Done | #46 |
| M1.2 Email basics, password reset, invitations | Done | #47, #48 |
| M1.3 Self-signup, account requests, age gate | Done | #49 |
| M1.4 Guest registration | Done | #55 |
| M1.5 Capacity, waitlist, pruning | Done | #57, #101 |
| M1.6 Calendar views | Done | #73 |
| M1.7 ICS feeds | Done | landed on master directly; fixes #80 |
| M1.8 Video, remote, childcare | Done | #102, #105, #108, #109 |
| M1.9 Households and household people | Done | #106, #107, #110, #111 |
| M1.10 Teen accounts, consent, badges | Done | #114–#118 |
| M1.11 Contact privacy and direct-contact requests | Done | #122 |
| M1.12 Frontend toolchain upgrades | Done | #59 |
| M1.13 Per-date event visibility (DEC-46) | Done | #123 |
| M1.14 Series RSVP and RSVP-aware calendar | Done | #129, #130, #131 |
| M2.1 Rooms and bookings | Done | #191–#195 |
| M2.2 By-room view and room feed | Done | #197, #198 |
| M2.3 Room suggestions | Done | #199, #200 |
| M3.1 Jobs table and worker | Done | #201, #202 |
| M3.2 Provider registry, Postmark adapter, email webhooks | Done | #203–#205 |
| M3.3 Consent and unsubscribe | Done | #206, #207 |
| M3.4 Composer | Done for email; the SMS tab, counter and donation warning were removed (#317) and move to M3.7 | #208–#211 |
| M3.5 Send pipeline | Done | #214, #215 |
| M3.6 Email reminders | Done | #216, #217 |
| M3.7 Telnyx SMS, inbound, keywords, revocation | Not started | |
| M3.8 SMS send window, caps, SMS reminders | Not started | |
| M3.9 Member SMS requests | Not started | |
| M3.10 Twilio adapter | Deferred until a church asks (DEC-140) | |
| M3.11 One-to-one messages and reply relay | Not started | |
| M3.12 Notification preferences and SMS delivery | In progress: `notify.deliver` and the email and push switches are built; the 15-minute skip-if-read step and SMS remain | |
| M3.13 Web push | Done | #218–#224 |
| M4.1 Encrypted provider settings | Not started | |
| M4.2 SPA setup wizard and setup state | Not started | |
| M4.2b Branding and asset customization | Not started (draft scope, DEC-137); Fold's own brand defaults shipped | #234, #235; plan #236 |
| M4.3 Legal pages | Not started | |
| M4.4 Email provisioning | Not started | |
| M4.5 People CSV import | Not started | |
| M4.6 SMS provisioning | Not started | |
| M4.7 S3 storage and private assets | Deferred until a church asks (DEC-140) | |
| M4.8 `fold setup` CLI | Not started | |
| M4.9 Admin two-factor sign-in (TOTP) | Not started | |
| M5.1 Prebuilt images | In progress: the `fold` image and release workflow are built; the first tagged release remains | #251, #252 |
| M5.2 Backups | In progress: host scripts `backup.sh` and `restore.sh` are built; the backup container, its System page warning and the CI restore test remain | |
| M5.3 Upgrades | In progress: host `upgrade.sh` dumps first and can roll back; the app's pre-migration backup check and `docs/UPGRADING.md` remain | |
| M5.4 Deployment docs (Compose) | In progress: `docs/DEPLOY-DIGITALOCEAN.md` and `docs/agents/deploy.md` exist; `docs/DEPLOY.md` (generic Compose) remains | |
| M5.5 Render Blueprint | Deferred: cut from the roadmap (DEC-140, DEC-142) | |
| M6.1 Directory with per-field privacy | Not started | |
| M6.2 Sign-up sheets | Not started; brief R14, after M6.3 | |
| M6.3 Attendance roll for leaders | Not started; brief R10, pulled ahead (DEC-145) | |
| M6.4 QR / self check-in | Out (DEC-140) | |
| M6.5 Prayer requests | Not started | |
| M6.6 Group discussions | Not started | |
| M6.7 Households and teen accounts | Moved to M1.9 and M1.10 (DEC-8), done there | |
| M6.8 Background-check status flags on slots | Not started | |
| M6.9 Photo galleries | Not started | |
| M6.10 Admin TOTP | Moved to M4.9 | |
| M6.11 ChurchTrac CSV mapping preset | Not started; needs a real sample export (DEC-143) | |
| M6.12 Custom registration questions | Not started | |
| M6.13 Translations | Deferred: dropped (DEC-13) | |
| M6.14 Durable audit log | Deferred: cut (DEC-140) | |
| M6.15 ChMS roster sync | Deferred: cut (DEC-140) | |
| M6.16 Notification email digests | Not started; later, not v1 (DEC-44) | |
| M7.1 Setup takeover: verify and close | Done | #241; issue #240 |
| M7.2 Privacy and session hardening bundle | Done | #243–#250 |
| M7.3 Secret rotation and expiring email links | Not started | |
| M7.4 Account erasure | In progress: most of erasure is built, including the deleted-accounts reason review (T07B 1a to 3b on master) and PR-F's conversion-IP minimisation, S5 and the audit-IP owner dispositions (G-9 stays open until its checklist is ticked); its scale measurements (#292), suppression contraction, the final restore procedure, recovery drills and sign-off remain | #268–#279, #281, #282, #299, #382, #383; open: #301–#313 (#301 and #302 are built in #383; #304 and #305 are built in PR-F) |
| M7.5 Notification delivery outbox | Done | #253–#256 |
| M7.6 Query ceilings and pool cap | Not started | |
| M7.7 Baseline squash | Done | #258, #264–#267 |
| M7.8 People and own-data export | Not started | |
| M7.9 Frontend speed and discoverability | Not started | |
| M7.10 Generated web types | Not started | |
| M7.11 Contact locks inside the freeze | Not started | |
| M7.12 Cut execution and maintenance tax | In progress: the guard tiers and the UI ratchet down to zero remain | #316, #317, #326 |
| Simplicity: drop `pending_return` (DEC-47), event visibility on the event (DEC-48) | Done | #43 |
| UI makeover and UI enforcement (DEC-57–DEC-67) | Done | #58, #60–#62, #64–#66, #71–#73 |
| Security and correctness fixes, Oct 2026 | Done | #44, #45, #96–#100, #163, #182, #183 |
| Product audit UI changes (DEC-92–DEC-99) | Done | #184, #186, #188–#190 |
| UI redesign and Fold brand assets | Done | #225, #230–#235 |
| Audit 2026-11 decisions and briefs (DEC-138–DEC-145) | Done | #242 |
| Untangle refactor | Done | #316–#345 |
| Open-issue fixes, Nov 2026 | Done | #346–#349, #351–#353 |
