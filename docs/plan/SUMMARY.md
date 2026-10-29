# Fold: product and delivery summary

Reconciled October 27, 2026 against merged work through M1.14 series RSVP/Undo
PR #130 (merge `7229e92`, reviewed head `5f927ce`) and the locally validated
departure publication head `deb093d`. [PLAN.md](PLAN.md) owns product decisions;
[STATUS.md](../../STATUS.md) tracks merges and publication state;
[DOCUMENTATION-REVIEW.md](DOCUMENTATION-REVIEW.md) lists open product conflicts.
Local validation does not mean protected-branch CI, merge, deployment or
production readiness.

## Product

Fold manages church groups and events, not giving, accounting or a whole ChMS.
The church owns its hosting, domain, data and provider accounts. One Go binary
serves the API and embedded React app, with PostgreSQL and Caddy in Compose.
No separate frontend host, database alternative or notification service.

Meetup is the primary UI reference; ChurchTrac informs church-admin screens.
Shared controls, role-based theme tokens and mobile/accessibility checks keep
similar actions consistent. Plain, warm language and explicit confirmations
matter more than novel design.

## Merged today

- M0 foundations: PostgreSQL/goose, policy tests, email login, member-based
  co-leaders, limits, account disable/restore, security headers and test tooling.
- M1.1–M1.7: recurring and church-wide events, stored notifications, SMTP/devoutbox
  mail, invitations/account requests/password reset, verified guest registration,
  capacity/waitlists, calendar views and personal/public ICS feeds.
- M1.8: video links, separate remote capacity and childcare requests/capacity.
- M1.9–M1.10: households, approval-first account conversion, teen consent,
  admin-confirmed guardians and church-local birthday rollover.
- M1.11: contact privacy, audited reveal, name-based picking, purpose-separated
  Ask to add, safe one-date invitations, admin history and CSV export (#122).
- M1.12: frontend toolchain upgrades (Vite 8, React 19, Tailwind 4 and the Node
  major recorded in `.nvmrc`). UI makeover work also added shared controls and
  responsive/accessibility gates.
- M1.13: per-date visibility with preview/confirmation, safe discovery/feed
  output and series preserve/reset (#123). Future consumers still need wiring.

## M1.14 merged

M1.14 implementation was originally accepted locally at `754a2f6`: RSVP-aware
calendar and personal-feed scope, series answers with Undo, and group-departure
preview/cleanup/retained-date access are implemented. That historical run passed
all 12 integrated gates with 509 component tests and 77 browser passes plus 6
conditional skips. Publication follow-ups `30d48c3`, `09b1260` and `5f927ce`
then repaired single-date draft reconciliation, atomic RSVP response feedback and
page-wide mutation serialization. Independent review approved the final PR #130
head, and all three protected checks passed before normal merge `7229e92`.

The departure publication boundary was independently approved at exact source
head `deb093d` and merged in #131 as `a18902e` on Oct 27, 2026. A fresh run passed all 12 repository gates, 520
component tests and 80 browser tests with 6 conditional skips. The retained 390px
atomic-feedback/page-gate proofs report no overflow, clipping, cut-off controls,
under-24px targets, under-44px RSVP targets or serious/critical Axe findings.
The exact evidence and acceptance matrix are in
[M1.14-VERIFICATION.md](M1.14-VERIFICATION.md).

M1.14 is merged, not deployed. Delivery is split across
calendar/feed #129 (`891ce26`), series RSVP/Undo #130 (`7229e92`, reviewed head
`5f927ce`) and [departure #131](https://github.com/grierj/fold/pull/131).
PR #131 records the successful protected `test`, `web` and `provision` checks
and normal merge `a18902e`; local acceptance evidence is retained separately. Representative external calendar-client
interoperability was not manually tested.

## Privacy decisions that must survive later work

- Public groups are visible to anyone; private groups to signed-in members;
  invite-only groups only to their authorized audience. Admins see all groups.
- A date's visibility override wins for that date, never the whole series.
  Exact-date invitations grant no group roster, hidden-group identity or sibling
  dates. Every new consumer must recheck current authority.
- Active teens and contact-hidden adults are searchable **by name**, not email.
  Every manager-initiated teen add needs teen and admin approval (DEC-78).
  Approval to add someone never grants access to their contact details.
- No accounts under 13. Teen accounts need adult/admin consent. An admin cannot
  confirm their own guardian link while another active admin exists; the
  sole-admin exception is audited (DEC-79).
- Contact remains hidden except through authorized audited reveal. Sensitive
  notifications contain no contact data; sensitive email is names-free.
- ChMS sync is far-future M6.15, one-way into Fold, initially admin-reviewed.
  No write-back, including adults or memberships (DEC-40).

## Next phases, not shipped features

| Phase | Remaining scope |
|---|---|
| M1.14 | Finish the publication stack: open the independently approved departure PR at `deb093d`, run its protected-branch CI, and merge it normally |
| Phase 2 | Rooms, booking conflicts, visibility-aware suggestions |
| Phase 3 | Durable jobs, messaging composer/relay, provider adapters, reminders, optional SMS, notification preferences |
| Phase 4 | Encrypted settings, resumable setup/CLI, CSV import, private/S3 assets, admin TOTP |
| Phase 5 | Published images, portable/offsite backup and restore, production hardening and operator documentation |
| Phase 6 | Optional later features, including ChMS import and daily notification digest |

Email provider integrations, Telnyx/Twilio SMS, TOTP and setup commands described
in PLAN are targets, not installed capabilities. SMS is never required to run
Fold. The current mail path is SMTP/devoutbox, not the future Postmark default.

## Deployment reality

Use [DEPLOY-DIGITALOCEAN.md](../DEPLOY-DIGITALOCEAN.md) and `.env.example`, not
future command examples in PLAN. Current Compose builds on the server; a 2 GB
VPS is the recommended starting point for that path. A 1 GB runtime assumes a
prebuilt image and is not the current release workflow. Prices vary; the church
also needs DNS, email configuration and a tested backup/restore procedure.

The DigitalOcean provisioner, smoke checks, host-cron dumps and upgrade helper
exist. Published release images, the backup service and pre-migration backup
refusal gate do not yet. Compose passes the SMTP URL and From address from
`.env` into the app; recreate the app after changes and verify delivery.

## Still to decide

Public-event discovery outside own groups (DEC-76); whether people may hide
completely and whether to rename private groups (DEC-78); detailed ChMS field
ownership. See the
review for evidence and implementation gaps. The old “nothing is open” claim
is retired. Superseded briefs and design reviews remain available in git history;
M1.10, M1.11 and M1.13 retain their verification records without acting as task
instructions.
