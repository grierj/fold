# STATUS.md — milestone tracker

Claim a milestone here (in its own PR or commit) before starting it.

Wright is the bot that builds fold (renamed from Codex on Oct 18, 2026); older rows and docs say Wright for the same bot.

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
| M1.6 Calendar views | unclaimed | | |
| M1.7 ICS feeds | in progress | Muse | `m1.7-ics-feeds` |
| M1.8 Video, remote, childcare | unclaimed | | |
| M1.9 Households and household people | unclaimed | | |
| M1.10 Teen accounts, consent, badges | unclaimed | | |
| M1.11 Contact privacy and direct-contact requests | unclaimed | | |
| M1.12 Frontend toolchain upgrades (Vite 8 + plugin-react 6, React 19, Tailwind 4, Node 24) | done (merged, #59) | Grok | `m1.12-toolchain` |
| M1.13 Per-date event visibility (DEC-46) | unclaimed | | |
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
| M1.5 brief (`docs/plan/M1.5-BRIEF.md`) | committed to master | Grier | `75485a1` |
| UI makeover PR 1: event times in the viewer's zone (DEC-57), profile time zone for emails (DEC-58) | done (merged) | Wright | #58 |
| UI makeover PR 3: Fold Violet tokens and self-hosted Inter (DEC-60), setup and outbox on the built CSS, axe and screenshot checks, color guard | done (merged) | Grok, Illuminator | #60 |
| UI makeover PR 4: friendly errors from API codes (DEC-61), error wording map, error-text guard | done (merged) | Grok | #61 |
| /correct pass: top recurring agent mistakes fixed at the highest layer (dist placeholder, e2e user switch, Node version, Python, "supported"); AGENTS.md trimmed to judgment calls | done (merged) | Grok | #62 |
| UI enforcement (Illuminator's ENFORCEMENT.md): token contrast in CI, layout and target spec, wider axe, `components/ui`, UI ratchets, verify-ui skill | done (merged) | Grok, Illuminator | #64 |
| UI makeover PR 5: nav shell (phone tabs, desktop top bar, church name), vendored Lucide icons, 44px targets and safe areas (DEC-62); 24px targets, clipped selects and controls cut off by an overflow box now hard gates; review fixes: Admin Users cards below md, labeled New group, Admin row on Me, no Disable/Delete on your own row | merged (#65) | Grok | `nav-shell` |
| UI makeover PR 6: event, series, leader and admin pages (DEC-63): shared More menu, segmented RSVP, attendance counts with a names expand, series as next date + N more in the next 90 days; can't-go count and names for leaders and admins only (DEC-64); layout gate now also checks the last content clears the tab bar | merged (#66) | Grok | `event-pages` |
| UI makeover PR 7: forms (DEC-65): one Field pattern (label, hint above the control, error below, only optional fields marked), Radio/Checkbox rows at 44px in fieldsets, searchable time zone combobox everywhere but setup, focus moves to the first invalid field, verb-plus-noun buttons full width on phones; guard keeps raw form controls in components/ui; layout gate on 44px radio and checkbox rows | in review | Grok | `forms` |
| UI makeover PR 8: empty, error and loading states, covers, brand (DEC-66): every empty list says what would show up and offers the next step, missing pages offer a way home, failed loads offer Try again, slow loads show skeletons, photo-less groups get one of six patterned covers by id, the tab gets the Fold icon; placeholder illustrations and covers wait on Gilder | merged (#72) | Muse | `empty-states` |
| UI makeover PR 9: calendar (M1.6, DEC-67): `/calendar` with Agenda, Week and Month views of every date the viewer may see, a "Just mine" chip and a group filter, times in the viewer's zone, a Calendar tab (5 tabs), and a read-only `GET /api/v1/calendar` with slim rows and no attendance | in review | Wright | `calendar` |
