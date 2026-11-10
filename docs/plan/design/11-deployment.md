# Fold design: 11. Deployment

## 11. Deployment, setup wizard, upgrades, backups

### 11.1 Shape

One recommended deployment for now: one VPS with Docker Compose (DEC-26). A managed platform (Render) is evaluated in 12.5 as a later, on-request option.

- One VPS per church: 2 GB RAM, 1 to 2 vCPU, 40 GB or more of disk, Ubuntu LTS or Debian, and Docker Engine with the compose plugin.
- Everything lives in `/opt/fold`: `docker-compose.yml`, `Caddyfile`, `.env`.
- Containers: `caddy`, `app` (`ghcr.io/grierj/fold:<FOLD_VERSION>`), `db` (`postgres:16-alpine`), and `backup` (`ghcr.io/grierj/fold-backup`: postgres:16-alpine plus `age` and `rclone`, with `backup.sh` and `restore.sh`, running a nightly loop).
- Volumes (the skeleton's names stay): `pgdata`, `uploads` (mounted at `/data`), `caddydata`, `caddyconfig`, plus a new `backups`.
- Network: one compose network with a fixed subnet, which is the default `FOLD_TRUSTED_PROXIES` (C17).

### 11.2 Environment file

`.env` holds only what must exist before the app can start, plus optional overrides:

```
DOMAIN=groups.example.church            # required; DNS A/AAAA must point at the VPS
ACME_EMAIL=office@example.church        # optional; Let's Encrypt expiry notices
FOLD_VERSION=0.5.0                      # pinned image tag; change it to upgrade
APP_ENV=prod                            # prod or dev (the skeleton's variable)
SESSION_SECRET=<openssl rand -hex 32>   # required by `serve` (hex or base64, 32+ bytes)
FOLD_SECRET_KEY=<openssl rand -hex 32>  # required from M4.1; encrypts provider secrets in the DB. BACK THIS UP.
DB_PASSWORD=<openssl rand -hex 24>      # required (Compose only)
DATABASE_URL=postgres://fold:${DB_PASSWORD}@db:5432/fold?sslmode=disable
FOLD_TRUSTED_PROXIES=172.30.0.0/24      # the compose network subnet (C17)
# Optional: lock provider settings to env instead of the wizard (env wins over the DB, C8)
# FOLD_SMTP_URL=smtp://user:pass@smtp.example.org:587   (minimal sender before the wizard, M1.2)
# FOLD_EMAIL_PROVIDER=postmark   FOLD_POSTMARK_SERVER_TOKEN=...
# FOLD_SMS_PROVIDER=telnyx       FOLD_TELNYX_API_KEY=...
# FOLD_STORAGE=s3  FOLD_S3_ENDPOINT=... FOLD_S3_BUCKET=... FOLD_S3_ACCESS_KEY=... FOLD_S3_SECRET_KEY=...
# FOLD_CLOUDFLARE_API_TOKEN=...  (DNS edit on one zone only, used once by the wizard)
# FOLD_BACKUP_KEEP_DAYS=14
# FOLD_PUSH_HOSTS=...   (optional, additive: extra web push service hosts; nothing is required, M3.13)
```

`deploy/init-env.sh DOMAIN` writes `.env` with fresh random secrets (using `openssl` or `/dev/urandom`) and refuses to overwrite an existing file. Config is loaded lazily per subcommand (C11): `fold version` needs nothing, and `fold migrate status` needs only `DATABASE_URL`.

### 11.3 Setup wizard (D7: the app configures as much as it can)

- The first-run page (server-rendered, as today) only creates the first admin. It is shown while `SetupState` says no admin exists, read from the database on each request (not cached), so a restart or a second container cannot skip or repeat it.
- Everything after that is the **setup wizard in the SPA** (`/admin/setup`), backed by `SetupState` rows. `fold setup --config setup.yaml` runs the same steps from `internal/setup`. Each step can be re-run later from Admin > Settings.

Steps:

1. **Church and admin**: church name, time zone, first admin's name, email, and password (email is the login, DEC-23). The admin confirms they are 18 or older. Also sets the account policy: self-signup `request` (default; every self-signup and member "invite a friend" waits for admin approval) or `off` (DEC-4).
- **`branding`** (optional/skippable; M4.2b): church logo, accent color, and header style. Stable `SetupState` id `branding` after step 1 (church identity) and before step 2 (Address and site); well before Safeguarding (step 8). Skipping branding must not skip safeguarding.
2. **Address and site**: postal address (for email footers and legal pages) and contact email. Checks that `SITE_URL` loads over HTTPS from inside the app.
3. **Email** (required for anything beyond a demo):
   - Pick a provider (Postmark is preselected, DEC-21; SMTP is the fallback) and paste an API key, or SMTP details.
   - `VerifyCredentials`, then `EnsureSendingDomain(mail.<domain>)`, which returns the DKIM, SPF/return-path, and DMARC records.
   - If a Cloudflare token is present, create the records by API. Otherwise show them in a copy-paste table.
   - Poll `DomainStatus` until verified; it continues in the background if the admin closes the page.
   - `EnsureWebhook` (bounces, complaints, deliveries, and inbound replies) with a generated secret, then send a test email to the admin.
   - Add the MX record for `reply.mail.<domain>` that the reply relay needs (7.8), by API with a Cloudflare token or in the copy-paste table.
4. **SMS** (optional; can be skipped):
   - Pick a provider (Telnyx is preselected; Twilio once M3.10 lands) and paste credentials from the **church's own** account (DEC-2). `VerifyCredentials`.
   - Ask "Does the church have an EIN?" and choose `sms_registration_path` (DEC-3, 12.2):
     - **Yes**: EIN-based standard brand (10DLC), or toll-free verification with the EIN.
     - **No**: recommend getting a free EIN from the IRS first, and pause here. On Telnyx there is no sole-proprietor option for a church (12.2). On Twilio, offer the sole-proprietor path only after showing its limits (1 campaign, 1 number, about 1,000 segments a day to T-Mobile) and that a named person, not the church, is the registered sender.
     - Never ask for 501(c)(3) proof.
   - Search for local or toll-free numbers and show the vendor's monthly price. The admin must type `BUY` to confirm before Fold calls `BuyNumber`. Fold never buys on its own.
   - `ConfigureNumber` sets the inbound and status webhooks.
   - Show the registration form pre-filled from church data and the generated legal pages (brand: legal name, EIN, address, website; campaign: use case Low-Volume Mixed or Mixed (never Charity), description, sample messages with no donation asks, opt-in flow, privacy and terms URLs). Submit by API where the provider allows it, otherwise show step-by-step instructions with a link to the vendor console.
   - Poll `RegistrationStatus`. SMS sending stays blocked, with a clear banner, until it is approved. Review typically takes days to weeks (Telnyx quotes about 72 business hours of carrier review after its own review; Twilio quotes 10 to 15 days for vetting).
   - Set the SMS send window (default 8 AM to 8 PM recipient-local, DEC-9, DEC-25), with the TCPA warning if it is set outside 8 AM to 9 PM.
5. **Storage**: local (default) or S3-compatible. Test with put, get, and delete. Offer `storage migrate` if there are already uploads.
6. **Video**: the Jitsi domain for generated links (default `meet.jit.si`). Leaders choose link-out or Jitsi per event.
7. **Backups**: show where nightly backups go. Optionally set an offsite S3 bucket and an `age` public key. Show a "write down your FOLD_SECRET_KEY" reminder and require a tick to continue.
8. **Safeguarding**: choose whether guardians get copies of messages to teens by email (`guardian_copies`, default off, email only; DEC-19, DEC-29).
9. **People**: import a CSV (name, email, phone, groups, and an optional "adult/teen" column with birth dates for teens) or invite by email. Imported adults become active accounts (they set a password from an emailed link). Imported teens start `pending_consent`. Rows under 13 are refused with a note to add them as household children. Rows that repeat an email already in the file or in Fold are offered as adult household people on that account (DEC-23). Imported phones are not opted in to SMS (10.4).
10. **Rooms and finish**: turn rooms on or off and add rooms, then go to the dashboard. The dashboard keeps a "Setup checklist" card until every step is done or skipped.

### 11.4 CLI (C11)

```
fold serve                      # default when run with no arguments (keeps today's behavior)
fold healthcheck                # used by the Docker healthcheck
fold migrate [up|status|down-one]
fold setup --config setup.yaml [--dry-run]   # non-interactive wizard; prints what it would do
fold providers test [email|sms|storage]       # sends a test / checks credentials
fold user create-admin --email ... --name ...
fold storage migrate --to s3|local
fold secrets rotate --new-key <key>           # re-encrypts provider secrets (M4.1)
fold push keys [show|rotate]                  # web push keys (M3.13); needs SESSION_SECRET and DATABASE_URL; show prints only the public key
fold push check                               # reaches each push host (M3.13)
fold version
```

All commands print plain text by default and JSON with `--json`, so agents can read the output. `fold migrate down-one` is an offline, quiesced maintenance command: stop every app instance and worker first, and keep membership writes stopped until rollback completes or the database is re-upgraded. It is not a live rollback path; pre-release test databases may be reset when a preservation guard refuses downgrade. Backups are not an app subcommand; they run in the `backup` container (11.8), because the distroless app image has no shell or `pg_dump`.

### 11.5 Rules for agents doing deployment

- Never buy numbers, change paid plans, or create provider or hosting accounts without the operator's explicit confirmation in the session.
- Provider and hosting accounts belong to the church (DEC-2). If you are helping, the church creates the account and adds you as a user, or gives you an API key. Never open accounts in your own name for a church, and never ask for the church's account password.
- Use `fold setup --dry-run` first and show the operator the plan.
- Never print secrets back. Write them only to `.env` (mode 600), or through the setup API.
- Before any upgrade on Compose, run `docker compose exec backup backup.sh now`.
- See Appendix A for the full AGENTS.md Deployment section outline.

### 11.6 Hosted single-tenant (D2, DEC-2, DEC-10)

- Fold is free MIT software (DEC-11). Self-hosting is the main path. Hosting for another church is the exception: Grier hopes not to do it often, or at all.
- When someone does host: the same compose stack, one VPS per church, created by a short script against the VPS provider's API (Terraform optional), plus DNS.
- Provider accounts (email, SMS, and optionally storage and the VPS itself) belong to the church, even when hosted, unless there is a special agreement (DEC-2). The church pays vendors directly. The host may help set the accounts up, for example by joining the church's provider account as a user. This also keeps the host from being an ISV or reseller in Twilio's or Telnyx's sense, so the host has no sub-account or brand-registration duties.
- Terms and any fee are agreed case by case, and hosting is not meant to make money (DEC-10). Fold has no billing code, and none is planned.
- An ops repo keeps per-church `.env` files encrypted with SOPS or `age`. There is no shared control plane.
- **Fleet trigger (DEC-139):** when one operator hosts about five churches, build fleet tooling before hosting a sixth: one script or small Go program in the ops repo that, per church, pins and bumps `FOLD_VERSION`, runs the upgrade and reads `/healthz` plus the backup `LAST_OK` age, and reports version, health and last-backup as one table. It drives the same per-church compose stacks over SSH or the VPS provider's API. It is not part of the `fold` binary, holds no church data, and adds no church-to-church anything: no shared database, no shared login, no cross-church view. Each church still owns its accounts and can leave with its database and uploads (DEC-2).
- Multi-tenancy (one database serving many churches) stays out (DEC-139). Fleet pain is an operations problem and gets an operations answer; mixing teen, consent and contact data from many churches behind row filters would spend the isolation that makes the safeguarding rules simple to reason about.

### 11.7 Upgrades

1. Read the release notes (`docs/UPGRADING.md` lists any manual steps, from v0.5 on).
2. Compose: run `docker compose exec backup backup.sh now`.
3. Set `FOLD_VERSION=x.y.z` in `.env`.
4. Run `docker compose pull && docker compose up -d`.

- Migrations run on start, under a Postgres advisory lock.
- Backup check: before applying pending migrations, the app reads `/backups/LAST_OK` from the `backups` volume, mounted read-only into `app`. If it is older than 24 hours, the app refuses to migrate. The check is skipped only when the database has no tables yet (a first install) or there are no pending migrations. There is no switch to turn it off.
- If a migration fails, its transaction rolls back and the app **exits non-zero** with the error in the logs. Docker restarts it and `docker compose ps` shows it unhealthy. There is no read-only maintenance mode.
- Rollback = restore the backup and pin the old version. Down migrations are best effort and not promised.
- Release images are tagged `x.y.z`, `x.y`, and `latest`. Docs always pin `x.y.z`.

### 11.8 Backup and restore

The portable backup-container design below is future planning, not the current restore procedure. For a droplet, follow the [interim recovery procedure](../../DEPLOY-DIGITALOCEAN.md#restoring-a-nightly-dump-interim-notes).

Planned Compose design:

- The `backup` service runs nightly at 02:30 church time:
  - `pg_dump -Fc` into `/backups/fold-YYYYMMDD-HHMM.dump`
  - `tar` of the `uploads` volume (skipped when storage is S3)
  - keeps the last `FOLD_BACKUP_KEEP_DAYS` (default 14) plus the first backup of each month for 6 months
  - writes `/backups/LAST_OK` after a successful run
- On demand: `docker compose exec backup backup.sh now`.
- Offsite (optional, M5.2): the `backup` container encrypts each new backup with `age` to the configured public key and uploads it to an S3-compatible bucket (B2 is a cheap choice, 12.3). The private key never touches the server.
- Restore: the former stop/restore/start proposal is retired; use the interim procedure linked above for current droplet recovery.
- CI runs a restore test on every release: seed data, back up, restore into a fresh database, and compare row counts and a sample API response.
- The Admin > System page shows the last backup time, size, and offsite status (read from the `backups` volume), and warns when the last backup is older than 36 hours.

### 11.9 Development environment (DEC-1, DEC-24)

- `docker compose -f deploy/docker-compose.dev.yml up -d db` starts PostgreSQL 16 on `localhost:5432`, with a named volume and the `btree_gist` and `citext` extensions created by an init script.
- `make dev` runs the Go server (`APP_ENV=dev`, `DATABASE_URL` pointing at the dev database) and the Vite dev server.
- `make test` runs `go test ./...` against `DATABASE_URL` (the same variable the app uses). If it is unset, `make test` starts the dev database and sets it. The test helper migrates a template database once, then gives each test package a fresh copy (`CREATE DATABASE ... TEMPLATE`).
- `make e2e` runs Playwright against a dev server (M0.4). `npm test` in `web/` runs Vitest.
- CI uses a `postgres:16` service container. The SQLite CI job is removed.
- There is no SQLite fallback anywhere, so `APP_ENV=dev` without `DATABASE_URL` fails with a message telling you to run the dev compose file.

