# STATUS.md — milestone tracker

Claim a milestone here (in its own PR or commit) before starting it.

Reconciled against merged history through [PR #163](https://github.com/grierj/fold/pull/163) (merge `62629fb`, Oct 29, 2026), plus post-merge source inspected through `6d689bb`. Source follow-ups below are distinguished from protected PR merge receipts; this documentation reconciliation is not a fresh full-suite acceptance or a production deployment. Product decisions run through DEC-87 in [PLAN.md](docs/plan/PLAN.md); remaining conflicts are in [DOCUMENTATION-REVIEW.md](docs/plan/DOCUMENTATION-REVIEW.md).

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
| M3.4 Composer (slices M3.4a–c) | in progress: M3.4a messages, audiences and permissions | Claude | `messages/m3.4a-drafts` |
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
