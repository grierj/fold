# Fold: product and delivery summary

Reconciled October 25, 2026 against merged work through `1a3f077`.
[PLAN.md](PLAN.md) owns product decisions; [STATUS.md](../../STATUS.md) tracks
merges; [DOCUMENTATION-REVIEW.md](DOCUMENTATION-REVIEW.md) lists open conflicts.
Merged does not mean deployed or production-ready.

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

M1.14 is **partial**, not complete: only the exact-date invitation prerequisite
landed with M1.11. RSVP-aware calendar styling, series answers/Undo, join/leave
cleanup and the unanswered church-wide window remain deferred.

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
| M1.14 | Complete series RSVP and calendar/feed scope behavior |
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
refusal gate do not yet. Current Compose also needs an explicit SMTP environment
pass-through before setting `FOLD_SMTP_URL` in `.env` can affect the container;
that configuration gap is recorded, not fixed by this documentation pass.

## Still to decide

Public-event discovery outside own groups (DEC-76); whether people may hide
completely and whether to rename private groups (DEC-78); detailed ChMS field
ownership. See the
review for evidence and implementation gaps. The old “nothing is open” claim
is retired. Superseded briefs and design reviews remain available in git history;
M1.10, M1.11 and M1.13 retain their verification records without acting as task
instructions.
