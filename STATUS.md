# STATUS.md — milestone tracker

Claim a milestone here (in its own PR or commit) before starting it.

| Milestone | Status | Owner | Branch/PR |
| --- | --- | --- | --- |
| M0.1 PostgreSQL only, clean baseline, CLI, request plumbing | done (merged, #9) | Codex | `m0.1-postgres-baseline` |
| M0.2 Visibility enum, policy package, leadership by membership, audit log | done (merged, #10) | Codex | `m0.2-policy-visibility-audit` |
| M0.3a Log in by email | done (merged, #12) | Codex | `m0.3a-email-login` |
| M0.3b Leaders remove members, sole-leader handoff, security headers, dependency scanning | done (merged, #13) | Codex | `m0.3b-roster-handoff-headers` |
| M0.4 Frontend and browser test tooling | done (merged, #22) | Codex | `m0.4-frontend-test-tooling` |
| M1.1a Events, time zones, occurrences, series edits | done (merged, #38) | Grok | `m1.1a-events-occurrences` |
| M1.1b Church-wide hosts, event visibility, audit, attendee privacy | done (merged, #39) | Grok | `m1.1b-hosts-visibility` |
| M1.1c Stored in-app notifications | done (merged, #46) | Codex | `m1.1c-notifications` |
| M1.2 Email basics, password reset, invitations | done (merged, #47; follow-up #48) | Winston | `m1.2-email` |
| M1.3 Self-signup, account requests, age gate | done (merged, #49) | Muse | `m1.3-self-signup-account-requests` |
| M1.4 Guest registration | done (merged, #55) | Muse | `m1.4-guest-registration` |
| M1.5 Capacity, waitlist, pruning | done (merged, #57) | Winston | `m1.5-capacity-waitlist-pruning` |
| M1.6 Calendar views | unclaimed | | |
| M1.7 ICS feeds | unclaimed | | |
| M1.8 Video, remote, childcare | unclaimed | | |
| M1.9 Households and household people | unclaimed | | |
| M1.10 Teen accounts, consent, badges | unclaimed | | |
| M1.11 Contact privacy and direct-contact requests | unclaimed | | |
| M1.12 Frontend toolchain upgrades (Vite 8 + plugin-react 6, React 19, Tailwind 4, Node 24) | done (merged, #59) | Grok | `m1.12-toolchain` |
| M1.13 Per-date event visibility (DEC-46) | unclaimed | | |
| Simplicity PR: drop `pending_return` (DEC-47), event visibility stored on the event (DEC-48) | done (merged, #43) | Codex | `simplicity` |

## Other merged work (not milestones)

| Change | Status | Owner | PR |
| --- | --- | --- | --- |
| Audit of M1.1a/M1.1b: hide opened invite-only groups' location; tag filter UI | done (merged) | Codex, Illuminator | #41 |
| Plan: per-date event visibility (M1.13, DEC-46) | done (merged) | Codex | #42 |
| Hardening review: group event list leak, tightening cancels, 6 MB body cap, `SESSION_SECRET` length, guards, verify-fold upkeep | done (merged) | Codex | #44 |
| Single-event tightening preview and confirm; clear short-secret startup error | done (merged) | Codex | #45 |
| Upload-only images (DEC-49), next-event time zone fix, church-zone event default (DEC-50) | done (merged) | Codex | #50 |
| Zone picker shows your zone; visibility at creation (DEC-51); shared series editor; home wording | done (merged) | Codex | #51 |
| Shorter time zone labels that fit on phones | done (merged) | Illuminator | #52 |
| DigitalOcean droplet guide, provisioner and backups ("recommended", not "supported") | done (merged) | Cursor agent | #53 |
| Tests sit next to their code; layout guard | done (merged) | Codex | #54 |
| M1.5 brief (`docs/plan/M1.5-BRIEF.md`) | committed to master | Grier | `75485a1` |
| UI makeover PR 1: event times in the viewer's zone (DEC-57), profile time zone for emails (DEC-58) | done (merged) | Codex | #58 |
| UI makeover PR 3: Fold Violet tokens and self-hosted Inter (DEC-60), setup and outbox on the built CSS, axe and screenshot checks, color guard | done (merged) | Grok, Illuminator | #60 |
| UI makeover PR 4: friendly errors from API codes (DEC-61), error wording map, error-text guard | done (merged) | Grok | #61 |
| /correct pass: top recurring agent mistakes fixed at the highest layer (dist placeholder, e2e user switch, Node version, Python, "supported"); AGENTS.md trimmed to judgment calls | done (merged) | Grok | #62 |
| UI enforcement (Illuminator's ENFORCEMENT.md): token contrast in CI, layout and target spec, wider axe, `components/ui`, UI ratchets, verify-ui skill | in review | Grok, Illuminator | `ui-enforcement` |
