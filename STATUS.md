# STATUS.md — milestone tracker

Claim a milestone here (in its own PR or commit) before starting it.

Reconciled against merged history through [PR #163](https://github.com/grierj/fold/pull/163) (merge `62629fb`, Oct 29, 2026), plus post-merge source inspected through `6d689bb`. Source follow-ups below are distinguished from protected PR merge receipts; this documentation reconciliation is not a fresh full-suite acceptance or a production deployment. Product decisions run through DEC-145 in [PLAN.md](docs/plan/PLAN.md) (audit DEC-138–145 added Nov 1, 2026); remaining conflicts are in [DOCUMENTATION-REVIEW.md](docs/plan/DOCUMENTATION-REVIEW.md).

| Milestone | Status | Owner | Branch/PR |
| --- | --- | --- | --- |
| M0.1 PostgreSQL only, clean baseline, CLI, request plumbing | done (merged, #9) | Wright | `m0.1-postgres-baseline` |
| M0.2 Visibility enum, policy package, leadership by membership, audit log | done (merged, #10) | Wright | `m0.2-policy-visibility-audit` |
| M0.3a Log in by email | done (merged, #12) | Wright | `m0.3a-email-login` |
| M0.3b Leaders remove members, sole-leader handoff, security headers, dependency scanning | done (merged, #13) | Wright | `m0.3b-roster-handoff-headers` |
| M0.4 Frontend and browser test tooling | done (merged, #22) | Wright | `m0.4-frontend-test-tooling` |
| M1.1a Events, time zones, occurrences, series edits | done (merged, #38) | Grok | `m1.1a-events-occurrences` |
| M1.1b Church-wide hosts, event visibility, audit, attendee privacy | done (merged, #39) | Grok | `m1.1b-hosts-visibility` |
| M1.1c Stored in-app notifications | done (merged, #46) | Wright | `m1.1c-notifications` |
| M1.2 Email basics, password reset, invitations | done (merged, #47; follow-up #48) | Winston | `m1.2-email` |
| M1.3 Self-signup, account requests, age gate | done (merged, #49) | Muse | `m1.3-self-signup-account-requests` |
| M1.4 Guest registration | done (merged, #55) | Muse | `m1.4-guest-registration` |
| M1.5 Capacity, waitlist, pruning | done (merged, #57) | Winston | `m1.5-capacity-waitlist-pruning` |
| M1.6 Calendar views | done (merged, #73) | Wright | `calendar` |
| M1.7 ICS feeds | done (initial direct landing; reviewed/fixed in #80; video reveal delivered in M1.8) | Muse, Wright | `m1.7-ics-feeds`, `m17-fixes` |
| M1.8 Video, remote, childcare | done (video #102, remote #105, childcare #109 merged) | Wright | `m1.8-remote-capacity` |
| M1.9 Households and household people | done (merged, #106, #107, #110, #111) | Wright | `m1.9-household-convert` |
| M1.10 Teen accounts, consent, badges | done (merged #114–#118; M1.10-VERIFICATION.md) | Wright | `m1.10-age-rollover` |
| M1.11 Contact privacy and direct-contact requests | done (merged #122; M1.11-VERIFICATION.md) | Wright | `orchestrate/207252f0-d74a-467e-83c4-314a924143cd` |
| M1.12 Frontend toolchain upgrades (Vite 8 + plugin-react 6, React 19, Tailwind 4, Node 24) | done (merged, #59) | Grok | `m1.12-toolchain` |
| M1.13 Per-date event visibility (DEC-46) | merged #123; existing consumers verified, absent search/reminder/room consumers deferred | Wright | `m1.13-date-visibility` |
| M1.14 Series RSVP and RSVP-aware calendar | original integrated implementation accepted locally at `754a2f6`; publication follow-ups `30d48c3`, `09b1260` and `5f927ce` repaired RSVP draft reconciliation, atomic response feedback and page-wide mutation serialization. Calendar/feed merged in #129 (`891ce26`); series RSVP/Undo merged in #130 (merge `7229e92`, reviewed head `5f927ce`) after all protected checks and independent approval. Departure source `deb093d` passed all 12 repository gates (520 component tests; 80 browser passes and 6 conditional skips) and independent publication review. Departure merged as `a18902e` on Oct 27, 2026 after `test`, `web` and `provision` passed; [PR #131](https://github.com/grierj/fold/pull/131) is the final protected CI/merge receipt. M1.14 is merged, not deployed. See [verification](docs/plan/M1.14-VERIFICATION.md) and [finish tasks](docs/plan/M1.14-FINISH-TASKS.md). | Parent + gpt-5.6-sol via pi-subagents | #129, #130 and [#131](https://github.com/grierj/fold/pull/131); preserved recovery history: `26ef2d2`, `79fa47b`, `7f1a214`, `orchestrate/b7eae0cf-c293-4968-ad97-51783bb7b1af` |
| M2.1 Rooms and bookings (slices M2.1a–e) | done: M2.1a merged (#191), M2.1b (#192), M2.1c (#193), M2.1d (#194), M2.1e (#195) | Claude | `rooms/m2.1e-match` |
| M2.2 By-room view and room feed (slices M2.2a–b) | done: M2.2a merged (#197), M2.2b (#198) | Claude | `rooms/m2.2b-feed` |
| M2.3 Room suggestions (slices M2.3a–b) | done: M2.3a merged (#199), M2.3b (#200); Phase 2 (rooms) complete | Claude | `rooms/m2.3b-picker` |
| M3.1 Jobs table and worker (slices M3.1a–b) | done: M3.1a merged (#201), M3.1b (#202) | Claude | `jobs/m3.1b-system` |
| M3.2 Email providers, Postmark, webhooks (slices M3.2a–c) | done: M3.2a merged (#203), M3.2b (#204), M3.2c (#205) | Claude | `email/m3.2c-admin` |
| M3.3 Consent and unsubscribe (slices M3.3a–b) | done: M3.3a merged (#206), M3.3b (#207) | Claude | `consent/m3.3b-settings` |
| M3.4 Composer (slices M3.4a–c) | done: M3.4a merged (#208), M3.4b (#209), M3.4c (#211) | Claude | `messages/m3.4c-composer` |
| M3.5 Send pipeline (slices M3.5a–b) | done: M3.5a merged (#214), M3.5b (#215) | Claude | `messages/m3.5b-history` |
| M3.6 Email reminders (slices M3.6a–b) | done: M3.6a merged (#216), M3.6b (#217) | Claude | `reminders/m3.6b-controls` |
| M3.13 Web push (slices M3.13a–c2, d1–d3) | done: M3.13a keys, devices and the push client (merged, #218), M3.13b the subscribe API and delivery (merged, #219), M3.13c1 service worker, manifest and the device card (merged, #220), M3.13c2 the directions page and the church switch (merged, #221), M3.13d1 push quiet hours and the hourly limit (merged, #222), M3.13d2 "Push notification too" on announcements (merged, #223), M3.13d3 the 2-hour push reminder (this branch: per-event switch, out-of-window rule, date page and history) | Claude | `push/m3.13d3-reminder` |
| Simplicity PR: drop `pending_return` (DEC-47), event visibility stored on the event (DEC-48) | done (merged, #43) | Wright | `simplicity` |

## Other merged work (not milestones)

| Change | Status | Owner | PR |
| --- | --- | --- | --- |
| Audit of M1.1a/M1.1b: hide opened invite-only groups' location; tag filter UI | done (merged) | Wright, Illuminator | #41 |
| Plan: per-date event visibility (M1.13, DEC-46) | done (merged) | Wright | #42 |
| Hardening review: group event list leak, tightening cancels, 6 MB body cap, `SESSION_SECRET` length, guards, verify-fold upkeep | done (merged) | Wright | #44 |
| Single-event tightening preview and confirm; clear short-secret startup error | done (merged) | Wright | #45 |
| Upload-only images (DEC-49), next-event time zone fix, church-zone event default (DEC-50) | done (merged) | Wright | #50 |
| Zone picker shows your zone; visibility at creation (DEC-51); shared series editor; home wording | done (merged) | Wright | #51 |
| Shorter time zone labels that fit on phones | done (merged) | Illuminator | #52 |
| DigitalOcean droplet guide, provisioner and backups ("recommended", not "supported") | done (merged) | Cursor agent | #53 |
| Tests sit next to their code; layout guard | done (merged) | Wright | #54 |
| UI makeover PR 1: event times in the viewer's zone (DEC-57), profile time zone for emails (DEC-58) | done (merged) | Wright | #58 |
| UI makeover PR 3: Fold Violet tokens and self-hosted Inter (DEC-60), setup and outbox on the built CSS, axe and screenshot checks, color guard | done (merged) | Grok, Illuminator | #60 |
| UI makeover PR 4: friendly errors from API codes (DEC-61), error wording map, error-text guard | done (merged) | Grok | #61 |
| /correct pass: top recurring agent mistakes fixed at the highest layer (dist placeholder, e2e user switch, Node version, Python, "supported"); AGENTS.md trimmed to judgment calls | done (merged) | Grok | #62 |
| UI enforcement (Illuminator's ENFORCEMENT.md): token contrast in CI, layout and target spec, wider axe, `components/ui`, UI ratchets, verify-ui skill | done (merged) | Grok, Illuminator | #64 |
| UI makeover PR 5: nav shell (phone tabs, desktop top bar, church name), vendored Lucide icons, 44px targets and safe areas (DEC-62); 24px targets, clipped selects and controls cut off by an overflow box now hard gates; review fixes: Admin Users cards below md, labeled New group, Admin row on Me, no Disable/Delete on your own row | merged (#65) | Grok | `nav-shell` |
| UI makeover PR 6: event, series, leader and admin pages (DEC-63): shared More menu, segmented RSVP, attendance counts with a names expand, series as next date + N more in the next 90 days; can't-go count and names for leaders and admins only (DEC-64); layout gate now also checks the last content clears the tab bar | merged (#66) | Grok | `event-pages` |
| UI makeover PR 7: forms (DEC-65): one Field pattern (label, hint above the control, error below, only optional fields marked), Radio/Checkbox rows at 44px in fieldsets, searchable time zone combobox everywhere but setup, focus moves to the first invalid field, verb-plus-noun buttons full width on phones; guard keeps raw form controls in components/ui; layout gate on 44px radio and checkbox rows | merged (#71) | Grok | `forms` |
| UI makeover PR 8: empty, error and loading states, covers, brand (DEC-66): every empty list says what would show up and offers the next step, missing pages offer a way home, failed loads offer Try again, slow loads show skeletons, photo-less groups get one of six patterned covers by id, the tab gets the Fold icon; placeholder illustrations and covers wait on Gilder | merged (#72) | Muse | `empty-states` |
| UI makeover PR 9: calendar (M1.6, DEC-67): `/calendar` with Agenda, Week and Month views of every date the viewer may see, a "Just mine" chip and a group filter, times in the viewer's zone, a Calendar tab (5 tabs), and a read-only `GET /api/v1/calendar` with slim rows and no attendance | merged (#73) | Wright | `calendar` |

## October 28 checkup fixes merged in #163

| Change | Evidence |
| --- | --- |
| Session lifetime decoding, signup IPv6/site budgets, password-reset daily mail cap, uniformly deferred signup mail and fresh inviter authority | `97d7d31`, `22d0bad`, `b4c74fd`, `c6a2d58`, `afbcd8e` |
| Current-password proof, newest-only verified email change/cancellation and atomic token/session revocation (DEC-87) | `ac2ca05`; issue #154 |
| Voluntary `left` memberships rejoin normally while `removed` stays blocked; departed answers are cancelled rather than recorded as a new decline (DEC-86) | `1e78bc0`, `9880194`; issue #144 |
| Seat-only manager removal retries waitlist promotion when capacity/childcare allow, outside the cutoff | `58a4d7c`; `TestRemovalPromotesOnlyWhenCapacityHasRoom`; issue #147, DEC-84 |
| Mobile navigation/landmarks/calendar cues, inline form validation, Admin Settings tab, Profile settings hub, organizer attribution and matching browser contracts | `b5b7643`, `49fbcf2`, `7587f85`, `314eb27`, `01b0350`, `cee0faf` |
| Contact history person/date filters, batched contact/group/guardian reads, mail Compose wiring and encrypted SMTP default outside loopback | `5b308d3`, `6ec6e7d`, `1abdb9b`, `d1d2441`, `249719f` |

## Post-#163 source follow-ups

These entries identify inspected implementation commits, not additional PR merge
or production-deployment receipts.

- Query bounds and batching: membership/registration indexes (`bed03ee`), historical name batches (`4718307`), one site-name read per list (`e3af1e4`), bounded group detail dates (`1e3edb9`) and a group-date window control (`0c71791`, `2fe6654`; #176-B).
- Locked authority: admin account insertion (`84f745b`; #164), group patch state and actor reauthorization (`a360ac6`, `c34215d`, `6236c6c`; #165), guest manage-link date access (`86eed96`; #166), and opened-date public-feed parent-detail privacy (`e10030f`; #167).
- Abuse budgets and uniform work: shared authenticated password-proof reservations (`2d09ece`; #170), balanced guest signup work/deferred mail (`48eb274`; #168), and shedding throttled password-reset account work (`6d689bb`; #169).

## Audit 2026-11 (Phase 7, approved Nov 1, 2026; DEC-138–DEC-145)

Briefs: `docs/plan/briefs/audit-2026-11/00-index.md` (order and waves there).
Claim a milestone here before starting, as usual.

| Milestone | Status | Owner | Branch/PR |
| --- | --- | --- | --- |
| M7.1 Setup takeover verify/close (SEC1) | done (`28268b3`, merged in [#241](https://github.com/grierj/fold/pull/241)); [#240](https://github.com/grierj/fold/issues/240) closed; brief verify checklist is the record | Grier | `fix/secure-first-run-setup` |
| M7.2 Privacy/session hardening (R5) | done (merged Nov 2, 2026 in [#243](https://github.com/grierj/fold/pull/243)–[#250](https://github.com/grierj/fold/pull/250)) | Claude | `fix/m7.2*` (#243–#250) |
| M5.1 Published image + CI compose smoke (R1 slices 1–2) | done: slice 1 [#251](https://github.com/grierj/fold/pull/251), slice 2 `feat/m5.1b-compose-smoke`; first release tag, public GHCR package and `compose` as a required check wait on Grier | Claude | `feat/m5.1b-compose-smoke` |
| M7.3 Secret rotation + expiring links (R12) | not started; brief ready | | |
| M7.4 Account erasure (R4) | claimed Nov 3, 2026. Slice R4-0 (documentation-only erasure inventory, `docs/plan/M7.4-ERASURE-INVENTORY.md`) is merged: PR [#268](https://github.com/grierj/fold/pull/268), merge commit `dc15921f3210a315421c517b7b1ce7ccfceda8d6`, with an independent review (GPT-6.1 Sol through Codex, Nov 3, 2026) that signed privacy and safeguarding, UI/UX (unaffected), database/performance (source review), security and correctness with conditional gates, recorded in inventory sections 15 and 15.1. **Slice R4-N (notification safety during erasure) is merged:** PR [#269](https://github.com/grierj/fold/pull/269), merge commit `6c44b85c1859bd9b7b63ef623a1f51cc951f5b79`. Its independently reviewed recipient locking, delivery repair and coverage are recorded in inventory sections 9.2.4 and 9.2.5. It changes no schema, UI, policy or gate. The owner answered the O-7 question (contact-request text, Nov 3, 2026): a narrow exception to scrub only the erased person's identifying text while preserving request decisions and provenance, recorded as a dated amendment to DEC-141 in `docs/plan/PLAN.md`. The reviewed T07 ages-note design is recorded in inventory 15.3; its implementation remains gated and has not been merged. The admin review direction for `reason` (owner decisions 6 and 7, inventory 15.4) still needs design, independent review and implementation. **Slice R4-H (replacement host before account deletion; owner decision 3; inventory T08, M9, F-2) is delivered:** deleting the only non-deleted host of a church-wide event needs a host the admin picks for each such event (`GET /admin/users/:id/sole-host-events`, `hostSuccessors` on the delete, a picker in the delete panel), with the host changes audited in the same transaction. Deleted accounts no longer count as hosts or receive host notices, and host writes lock their users and events in a fixed order (inventory 9.2.5). It adds no migration, changes no gate, leaves disabling as it was (host rows are kept) and erases no identity data. R4-H is merged: PR [#270](https://github.com/grierj/fold/pull/270), merge commit `369c45121d599a39e32be1b9e9153a5a7c96f5e6`. **Slice R4-5 (durable avatar cleanup; inventory T37 avatar, section 8, F-5) is delivered:** profile-photo files are now removed only by a queued `avatar.cleanup` job, queued in the same transaction as the key change (upload, removal and account deletion) and recovered by a daily sweep, with every lock hold on the user row bounded by a deadline. It adds no migration, changes no gate and erases no identity data (the delete handler now also clears the photo key and queues the file cleanup; the rest of the user-row identity scrub is R4-1's and the scrub of legacy tombstone rows is still R4-6). Its limits, including that erasure can wait on the account's own in-flight avatar requests and may take longer than the 60-second write timeout, are recorded in inventory section 8.1. R4-5 is merged: PR [#271](https://github.com/grierj/fold/pull/271), merge commit `2aea60a9783ecfa9f56638347ba043ad7cf3ad5d`. Slice R4-S (suppression address hashing, Route B expand; inventory T35, T18, 7.7) is delivered on `autoflow/m74-r4-s-20261104`, with migration `00002_suppression_address_hash.sql`. Hashed suppression addresses are stored beside the plaintext ones; the plaintext address, the `webhook.process` payload address and the `last_error` copies remain until R4-SC, which can only follow a tagged, published release that contains 00002. Opt-outs are still account-keyed (O-1), G-9 is open and R4-S erases no identity data. M7.4 is not complete. R4-S is merged: PR [#272](https://github.com/grierj/fold/pull/272), merge commit `6d5ad07399cd48122df681e233a8aa4d2c92bc45`. Slices not yet merged: R4-T07A, R4-T07B, R4-2d2, R4-3, R4-4, R4-SC, R4-6. Slice R4-1 (tombstone, credentials, throttles, admin invariants; inventory T02, T04, T19, T22, T27, T37, F-1, F-4, F-6, F-8, M31, O-11) is delivered on `autoflow/m74-r4-1-20261104`. Deleting an account now scrubs the user row to a tombstone (name, display name, phone and timezone cleared, password hash and birth date removed, focal points neutral, `hide_contact_from_leaders` off, photo key cleared, disabled, session version bumped; id, role, account type, creation time and logout version are kept), deletes all of the account's tokens (used or not), its calendar link and its push devices, and, as the last statement of the delete, the sign-in and mail throttle keys the account owns (exact keys for the address it held at erasure and its id, so a lockout on that address lifts; shared and other people's budgets stay). Memberships stay hard-deleted. The last-admin rule is also decided on locked facts for delete and role or status changes, as defense in depth (F-1 stays closed by R4-H's actor re-read, with a mutual-delete regression test). Calendar link creation and scope change, and push subscribe, refuse a session that went stale (erased, disabled, credential-changed or logged out) and save nothing. Throttle keys come from typed builders in `throttle_keys.go`, with parse-only guards whose limits are stated. The erasure is not proven deadlock-free: inventory 9.5 records three detected deadlock limits (D1 to D3), where Postgres aborts one side and the admin can retry. The throttle limit O-21 is Proposed and takes effect only if the independent privacy reviewer signs it. R4-1 adds no migration, changes no gate, decision or UI. R4-1 is merged: PR [#273](https://github.com/grierj/fold/pull/273), merge commit `5444abaf68cb041577b523a62cca22fc5e4feb51`. Slice R4-2a (dependent records, waiting tasks and the snapshot, expiry and guest-redemption lock orders it needs; no `contact_requests` statement) is delivered on `autoflow/m74-r4-2a-20261104`. R4-2a is merged: PR [#275](https://github.com/grierj/fold/pull/275), merge commit `b4092e7f1ead749e7cce4979e9572f1701836747`. On that branch, deleting an account also withdraws its pending account requests and closes their waiting admin tasks, anonymizes its account requests (name, address, phone, birth date, message, guardian address, password hash and decision note, with `ip` by the inventory's per-source rule), clears its address from other people's guardian-address field, anonymizes the invitations tied to it (an open one is revoked), deletes its unconfirmed guest signups and anonymizes its confirmed ones, deletes development-outbox mail to its address, cancels its scheduled announcements (one `message.cancelled` audit row each), blanks the address and error text of its recipient rows (queued ones become suppressed), deletes its undo receipts, manager-add blocks and notification delivery rows, and clears the manage token, childcare ages note and removal note of its registrations. Announcement snapshots, account-request expiry and guest redemption now take their locks in an order that agrees with the erasure (inventory 9.5, L1 to L3), and a leader's removal of a registration serializes with it on the contact gate. One more detected deadlock limit is recorded as D4 (a guest redemption that asked for an account against the erasure of an admin; Postgres aborts one side with 40P01), and the erasure is not proven deadlock-free. The recipient statement runs under a 20-second database statement timeout; that is not a provider, batch or HTTP bound, so successive waits can exhaust it and the whole erasure then rolls back and can be retried, and the 60-second HTTP write timeout bounds no transaction (inventory 9.4). R4-2a adds no migration, gate, decision or UI change and writes no `contact_requests` statement. R4-2d1a (the `setConsent` lifecycle guard, so a consent write by a request that loaded the user before the erasure saves nothing) is merged: PR [#274](https://github.com/grierj/fold/pull/274), merge commit `cc1f9915da6eff1c5c12007d6add0d7f71558198`; the consent scrub is merged in R4-2d1b. Slice R4-SG (the CI contract-migration gate, PLAN 14.1 tag-and-file half; publication stays review only) is merged: PR [#276](https://github.com/grierj/fold/pull/276), merge commit `06cc31d0ac3f01eddf8bb100e65da3c4cdc603fd`. Slice R4-2d1b (consent evidence scrub, the rest of R4-2d1) is merged: PR #278, merge commit `4bb3e1e6d2d6bf5afdbcd3426a0e5a19c80e4707`. Deleting an account also blanks the `ip`, `user_agent` and `evidence_text` of its consent rows (migration `00003_consent_erasure_scrub.sql`, additive, with its Down restoring the baseline body), right after the soft delete and serialized with `setConsent` on the user row; every other column, the opt-out choice, `teen_consents` and `suppressions` stay. It changes no gate, decision or UI. R4-2d2 (the signed address-level opt-out, O-1) is still gated. Slice R4-3a (household writer revalidation, inventory 9.6 requirements 1 to 4) is merged: PR [#277](https://github.com/grierj/fold/pull/277), merge commit `e806cf3e8562f916381f990593ae975adba1dad8`. Migration token: released after R4-2d1b merged; the next migration writer takes it from the lead's token file. Identity erasure is staged, not done. R4-1 erases only the user-row identity (the tombstone scrub), the account's tokens, calendar link and push devices, and its owned throttle keys. Contact-request text (R4-T07A and R4-T07B), address-level opt-out (R4-2d2), households and guardians (R4-3), audit IPs (R4-4), suppression storage (R4-SC) and legacy tombstones (R4-6) remain with their slices, and account erasure as a whole is not done. R4-N changes how notices are written and repaired, R4-H changes who hosts events, R4-5 removes only profile-photo files, and R4-S only adds a hash column. Slice progress will be staged, not completion: M7.4 cannot be marked done while personal IP addresses can remain in audit rows (unattributed teen-conversion completion rows, transfer rows of an unestablished history, and conversion groups retained because a row is malformed or ambiguous; gate G-9, inventory 6.5.2, **OPEN**) unless the owner authorizes an exact, named PLAN disposition or an approved, implemented and independently reviewed minimization closes it with a per-action safeguarding judgment. A reviewer's acceptance cannot waive it. Separately, the owner answered one further question (Nov 3, 2026, evening): "Yes, clear those IPs (Recommended)" to whether, when an erased person may have caused a household conversion, Fold may clear the IP from the identified conversion audit entries even if it might belong to another living participant, provided it is not essential safeguarding evidence, with decisions, actors, dates and provenance preserved. That authorizes only the concept of the inventory's option P2 in exactly that scope. It is not an approved design, does not settle the safeguarding judgment, covers no living person's separate step rows and no blanket deletion of audit IPs, does not claim the candidate-set approach is complete or reach malformed or unreachable historical rows, and does not establish G-8. The other alternatives the inventory lists (P1, P3 to P5) remain unapproved. G-8 is unestablished, the T07 gate verdicts are recorded (inventory 15.3: G-7a and G-7b rejected, G-7c signed as narrowed, G-7d narrowed conditionally; R4-T07A (ages note) is adopted and waits on the migration token and its implementation gates; R4-T07B (`reason`) is held: Grier chose an admin review step during deletion to decide whether to blank each reason (owner decision 6) and a one-time admin review list for accounts deleted before that step exists (owner decision 7), both recorded verbatim in inventory 15.4; the detailed engineering design, its independent review and the implementation remain incomplete, the design is not adopted and G-7e is unsigned), O-1 and the release gates (O-2, O-15) remain open, and owner decisions 1 to 5 are unchanged. M7.4 is not complete and account erasure is not done. | Claude | `autoflow/m74-r4-2d1b-20261105` |
| M7.5 Notification outbox (R6) | in review; S4 direct-mail jobs, guard hardening and regression tests implemented | Codex | `autoflow/m75-s4-20261103` |
| M7.6 Query ceilings + pool cap (R11) | not started; brief ready | | |
| M7.7 Baseline squash (R9) | done Nov 3, 2026: S1 #258, S2 #264, S3 #265 and S4 #266 merged. Generated baseline proved schema-equivalent before the two date changes and provenance fold; final schema has 37 tables. Independent review passed all four concerns; 991 root tests, 1,434 web tests, 118 e2e passes (7 skips), strict drift, schema allowlist and all four CI jobs passed. Merge SHAs and owner attestation are recorded in R9 section 9, S5; migration freeze released. Old-chain and earlier-baseline databases require owner recreation; no converter or automatic refusal. | Claude / Codex | `autoflow/m77-s4-20261103` |
| M7.8 People/own-data export (R3) | not started; brief ready (with M4.9) | | |
| M7.9 Frontend speed/discoverability (R15) | not started; brief ready | | |
| M7.10 Generated web types (R16) | not started; brief ready | | |
| M7.11 Contact row locks (R19) | not started; brief ready; freeze DEC-141 landed | | |
| M7.12 Cut execution + maintenance tax (R17.1/R18) | not started; brief ready; DEC-139/140 wording landed in PLAN | | |
| Audit docs: AUDIT + briefs + DEC-138–145 + PLAN 14.10 | written Nov 1–2, 2026; rebased over #236/#241 | Muse | (docs; lands with this change) |

### M7.5 S4 direct mail

S1–S3 are merged in [#253](https://github.com/grierj/fold/pull/253),
[#254](https://github.com/grierj/fold/pull/254) and
[#255](https://github.com/grierj/fold/pull/255). S4 moves reset, account-request,
existing-account hint, invitation/conversion, email-verification, guardian-consent
and guest-verification mail onto the existing job runner. `runInBackground` and
`App.background` have been removed. No schema, product controls or deployment
components are added by S4.

Domain changes, audits and their jobs commit together. Standalone hints and
guardian resends durably enqueue before answering. Payloads are exactly
`{userId}`, `{requestId, case}`, `{invitationId}`, `{accountTokenId}`,
`{guardianLinkId}` or `{guestSignupId}`. The request-mail `case` is one of
`received`, `approved`, `exists`, `conversion_approved`, `rejected`; addresses,
rejection notes, rendered bodies and raw capabilities stay out of job payloads.
Delivery prepares from stored facts, commits the capability, then contacts the
provider without database locks. Account flows match redemption's contact gate,
user, link and token order; invitation and guest preparation lock only their own
row. Final claims match the submitted hash, unused state and live wall-clock
deadline. Eight attempts use the runner's backoff, dead state and admin alert.
Delivery is at least once at the provider boundary, so a retry may produce a
replacement email and invalidate the previous attempt's link.

S4 retains four accepted departures from the R6-4 slice table:

- Invitation delivery rotates only the hash, keeps the original expiry and says
  "expires within 24 hours". A provider retry must not extend the invitation.
- Email verification uses `{accountTokenId}` and hash-only rotation of that exact
  request. It preserves the original verify/cancel lifetime, so stale work cannot
  reopen a cancelled or superseded request or outlive its cancellation window.
- Approved setup mail derives its consent sentence from the locked user's current
  state. A teen already consented at delivery is not told to wait for consent.
- Direct jobs share the existing 60-second `deliverSendTimeout`, replacing the
  old 30-second `resetMailTimeout`.

Two synchronous exceptions remain. `App.sendEmailChangeNotice` needs the old
address and cancellation capability, which cannot be rebuilt after verification.
`App.sendGuestConfirmationEmail` shares the manage capability returned to the
browser; delayed rotation would invalidate that displayed link. The unchanged
composer test send is a separate synchronous path. Both account exceptions use
a 20-second `syncMailTimeout`, leaving 40 seconds of the server's 60-second write
budget for transaction work and the response. Jobs keep their 60-second send
bound. Provider errors are cleaned without adding recipients or capabilities
to logs. The parsed-source guard scans production Go directories and limits
account exceptions to immediate calls in their named methods. Callers must also
invoke those methods immediately after commit, outside closures, `go` and `defer`,
without capturing method values. The guard follows concrete mail sender types,
constructors and each position of same-package helper results, and leaves
`api_dev.go`'s outbox reads read-only. It checks syntax rather than transaction
completion; its remaining parse-only analysis limits are documented in AGENTS.md.

`TestDirectMailDeadJobPrivacyAndDecisions` exercises transient failure, eight
attempts, dead-job alerts, admin Retry and Dismiss through the real runner.
It advances `run_at` explicitly and does not assert the backoff schedule. After
Dismiss it runs the worker and asserts no send or token rotation; a repeated
decision must not duplicate the alert or audit. Guard fixtures include nested
production directories and same-package senders and multiple-result helpers
declared in another file. Handler deadline tests require enough headroom for
the HTTP response, and the job deadline test retains its longer send budget.
`TestGuestConfirmationMailIsBoundedAndKeepsLogsPrivate` checks the synchronous
send's deadline, cancellation, private failure log and surviving browser manage
link. `.deadcode-allow` is unchanged. Merge requires the CI checks and separate
four-concern review described in AGENTS.md.

Deferred work remains deferred: send-time visibility rechecks for stored notice
email, recalling queued pushes on Dismiss, and the `notifications(created_at)`
index pending R11's EXPLAIN gate. The first-church checklist includes the real
provider recovery check. After S4 merges, M7.5 is complete; stop before starting
another milestone.

## Remaining follow-ups

Recheck implementation before claiming an older finding. M1.14's exact-head local suites and review evidence are recorded separately; older follow-ups below are not silently closed by that milestone.

- M1.10 phase scope: account access, guardian consent, staged conversion, birthdays, and existing roster badges are implemented and verified in #114–#118. Recipient-list composition, guardian message copies, SMS sending, and galleries remain future milestones. DEC-83 records the staged inactive-holder retry exception and independent suspension behavior.

- (Illuminator, #105 N10) Watch that a stray arrow key in the "How you'll attend" radio group can't change a Going answer by accident (switching mode saves immediately).
- (Illuminator, PR 110 N5) Use the Going/Maybe/Can't go radios for each household person instead of the "Answer for X" select (judgment call 38).
- (Warden, PR 110) Recheck the solo RSVP/membership-removal race against the current locked authorization path in `api_registrations.go`. It now calls `canRegisterIn` inside the transaction; this documentation pass did not establish whether all competing membership mutations serialize with that check. Do not treat the older finding as verified closed.
- (Whittler, PR 110) Select attendee names in the attendee query so the account load can be skipped (GET /events/:id to about 18 reads).
- (M1.14 departure-security review, P2) Disabling a Going member inside the cutoff can notify leaders, then later departure cleanup can repeat the same waitlist-cutoff notice even though no additional seat was freed. Capacity and authorization remain correct; avoid the duplicate notification in a focused follow-up.
- VideoField polish (Illuminator, optional, from the #102 review): N7, clear the field's error when the link or the video option changes; N8, add a little space between the event page's time line and the "Join by video" button.
- Terminology (Grier, Oct 20, 2026; DEC-78): rethink the name "private" for groups. The word is overloaded, because private groups are visible to every signed-in member.
- A confirmed household-person guardian link survives a teen-to-child kind edit; this is the retained behavior, not an unapproved new permission.

Completed follow-ups removed from the queue: CI SHA pinning/read-only permissions (#100); household church-local age checks, going-path read budget, roster status placement and Profile placeholder (#111); seat-only manager-removal promotion (#147, DEC-84, merged in #163) and the older manager re-add double-count finding (`addAccountRegistrationLocked` already subtracts the existing Going party before checking the new party's fit; this was present at the #163 baseline). Superseded slice briefs/judgment logs are in git history; their current rules are consolidated in PLAN and docs/API.md.
