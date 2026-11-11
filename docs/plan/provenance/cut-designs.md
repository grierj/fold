# Fold provenance: designs cut from the roadmap

History, not current guidance. DEC-140 (Nov 1, 2026) cut these designs from the roadmap; each
re-enters only as a new milestone for a named church that asks. The text is copied from PLAN.md as
it stood before the cut, with two words changed to "available" and "allows", so its milestone and
section references are to that version.

## Durable audit log sketch (was PLAN 10.7, M6.14, DEC-33)

Not v1 scope; milestone M6.14. Until then, the regular `AuditLog` (created in M0.2) records all of these events, and they are never purged by cleanup or user deletion. The durable log hardens that record against later editing or loss, in case of legal trouble.

- **What it covers** (the really important events): contact-access requests, decisions, grants, reveals, revocations, and every failed, abandoned, or expired attempt (DEC-31); guardian and household links of teens and children (DEC-32); teen consent; role and leadership changes; visibility changes that make an event more open than its group; SMS opt-outs and opt-ins; and admin overrides (send window, double-bookings, consent granted with evidence text).
- **Ideas to settle in M6.14** (a sketch, not a design):
  - Append-only storage: a separate table that the app's database role can only `INSERT` into and `SELECT` from, with no `UPDATE` or `DELETE` grant, plus a trigger that rejects changes anyway.
  - Hash chaining: each row stores `prev_hash` and `hash = SHA-256(prev_hash + canonical row)`, so an edited, deleted, or reordered row breaks the chain. `fold audit verify` and the Admin > System page check the chain.
  - Write-once offsite export: the `backup` container uploads a daily signed segment to an S3-compatible bucket with object lock, for example Backblaze B2 Object Lock in compliance mode. Backblaze says Object Lock has no extra cost beyond normal storage and allows retention periods of 1 to 3,000 days (https://www.backblaze.com/docs/cloud-storage-object-lock). A locked file can't be deleted early even by the church, so the retention period must be chosen with care.
  - Retention rules: a church-set period per event type, with legal holds. Fold's default keeps everything; the church decides with its own advisor (DEC-14). The privacy page explains that these records survive account deletion.
  - Admin export for legal requests: filter by person and date range; CSV plus JSON with the hash chain and a verification report; the export itself is logged.
- **Acceptance to write before work starts**: verification catches any edited, deleted, or reordered row; the offsite segments can't be deleted before retention ends; an export for one person verifies on its own.

## Render evaluation (was PLAN 12.5, M5.5, DEC-26)

The `render.yaml` outline it mentions is in [`appendix-a-agents-deployment.md`](appendix-a-agents-deployment.md).
Unverified Render facts are listed in [design/12-providers.md](../design/12-providers.md) 17.2.

**Later / on request: Render (managed PaaS).** Not recommended or tested yet and not in the committed roadmap (DEC-26). This evaluation and the `render.yaml` outline in Appendix A are kept as reference, for when a church asks or when Grier wants an easy path to host for others (future M5.5). Facts are from Render's own pages, October 2026. Pricing: Hobby workspace $0 + compute; Pro workspace $25/mo + compute. Web service 0.5 CPU / 512 MB $7/mo (1 CPU / 2 GB $25/mo). Postgres 0.1 CPU / 256 MB $6/mo, 0.5 CPU / 1 GB $19/mo, plus storage at $0.30/GB/mo. Persistent disk $0.25/GB/mo. Bandwidth: 5 GB included on Hobby (25 GB on Pro), then $0.15/GB. https://render.com/pricing. Configurable by REST API and `render.yaml` Blueprints (https://render.com/docs/api, https://render.com/docs/blueprint-spec).

- **Typical cost for one small church** (Hobby workspace, prices from https://render.com/pricing):
  - Web service 0.5 CPU / 512 MB: $7. Fold's single Go binary fits.
  - Postgres 0.1 CPU / 256 MB: $6, plus about $1.50 for 5 GB of storage.
  - Disk, 5 GB at `/data` for uploads: $1.25.
  - Total: **about $16/mo**. With the 1 GB Postgres plan ($19), about $29/mo.
  - The Hobby workspace has one team seat. A helper who needs their own login requires Pro, which adds $25/mo (about $41 to $54/mo in total). Otherwise the church shares an API key with the helper, not its password.
  - A 2 GB VPS is $10 to $12/mo, plus a few cents for offsite backups (12.3).
- **Free tier: not suitable.** Free web services spin down after 15 minutes idle (about a minute to wake) and cannot have disks. Free Postgres expires after 30 days and has no backups. Free web services also cannot send on ports 25, 465, or 587, which SMTP uses (https://render.com/docs/free). Postmark's HTTPS API is not affected.
- **Containers and background jobs: yes.** A web service can run a prebuilt image from GHCR (`runtime: image`) or build the Dockerfile. Fold's job worker runs inside the web service process, which is always on for paid plans, so it needs no separate worker service. Render also offers background workers and cron jobs, billed per minute, if ever needed.
- **Persistent disk** (https://render.com/docs/disks):
  - Paid services only, with daily snapshots kept at least 7 days.
  - A service with a disk is limited to **one instance**, and deploys have **a few seconds of downtime** (zero-downtime deploys are turned off).
  - Disks are not reachable from pre-deploy commands, one-off jobs, or cron jobs, and can grow but not shrink.
  - For one church, one instance is fine. To keep zero-downtime deploys, use S3-compatible storage (B2 or R2, 12.3) instead of a disk once M4.7 ships.
- **Managed Postgres** (https://render.com/docs/databases, https://render.com/docs/postgresql-backups, https://render.com/docs/postgresql-extensions):
  - Versions 13 to 18 are available (18 is the default). Pin `postgresMajorVersion: "16"` to match CI.
  - The `citext` and `btree_gist` extensions Fold needs are available.
  - Paid databases get point-in-time recovery: 3 days on Hobby, 7 days on Pro. A recovery restores into a new database. Logical exports are kept 7 days, and `pg_dump` works over the external URL.
- **Custom domains and TLS: yes** (https://render.com/docs/custom-domains). Certificates are automatic. Hobby includes 2 custom domains. Render serves over IPv4, so any AAAA record for the Fold host must be removed.
- **Agent-deployable: yes.** A `render.yaml` Blueprint can declare the web service (image, plan, region, disk, health check path, domains, env vars, generated secrets) and the database (version, plan, disk size, IP allow list), and wire `DATABASE_URL` from the database. Blueprints can be validated before use, and the REST API covers services, databases, deploys, env groups, Blueprints, and custom domains. Secrets marked `sync: false` are prompted for only when the Blueprint is first created, so the operator types provider keys into Render's dashboard, not into the repo.
- **Lock-in: low.** Fold's image, standard Postgres (`pg_dump`), and S3 storage all move. Only `render.yaml` and Render's environment settings are Render-specific. Moving to a VPS is `pg_dump` + `pg_restore` plus copying the uploads.
- **Fit with Fold's goals:**
  - Simple for a power user: yes, arguably simpler, because there is no OS, firewall, Caddy, or backup container to look after.
  - Church owns its accounts (DEC-2): yes, if the church creates the Render workspace and pays Render directly.
  - Few dependencies (D3, D6): it is one vendor for compute, database, and TLS, instead of a VPS.
  - Things Fold must handle: trust Render's proxy for client IPs (C17); turn off the Compose backup check (`FOLD_BACKUP_CHECK=off`); accept the generated secret format (base64) for `SESSION_SECRET` and `FOLD_SECRET_KEY`; read `PORT`.

**If Render is added later (M5.5):**

- A VPS with Compose stays the default either way: cheapest (about $10 to $12/mo) and fully under the church's control.
- Render would suit a church where nobody wants to look after a Linux server and about $16 to $29/mo (more on Pro) is acceptable for managed TLS, Postgres backups with point-in-time recovery, and push-to-deploy. Prefer S3 storage over a disk there, so deploys stay zero-downtime.
