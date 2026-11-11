# Fold design: 11. Deployment

## 11. Deployment, setup wizard, upgrades, backups

The operator guide is [`docs/DEPLOY-DIGITALOCEAN.md`](../../DEPLOY-DIGITALOCEAN.md); the rules for
agents changing deployment are in [`docs/agents/deploy.md`](../../agents/deploy.md). This section is
the design, and marks what is still planned.

### 11.1 Shape

One recommended deployment: one VPS with Docker Compose (DEC-26, DEC-142). Managed platforms are
on request only; the Render evaluation was cut (DEC-140, 12.5).

- One VPS per church: 2 GB RAM recommended (1 GB with swap is the floor once a release image can be
  pulled), 1 to 2 vCPU, Ubuntu LTS (the DigitalOcean guide tests 24.04 and 26.04), and Docker
  Engine with the Compose plugin.
- On the DigitalOcean path everything lives in `/opt/fold`: `docker-compose.yml`, `Caddyfile`,
  `.env` (mode 600) and the operator scripts.
- Containers: `caddy` (`caddy:2-alpine`, the only published ports, 80 and 443), `app`
  (`ghcr.io/grierj/fold:<FOLD_VERSION>`), and `db` (`postgres:16-alpine`, user `church`, database
  `fold`, no published port). **Planned (M5.2):** a `backup` container
  (`ghcr.io/grierj/fold-backup`: postgres:16-alpine plus `age` and `rclone`, with `backup.sh` and
  `restore.sh` in a nightly loop). Today the DigitalOcean path runs `backup.sh` from host cron
  (11.8).
- Volumes: `pgdata`, `uploads` (mounted at `/data`), `caddydata`, `caddyconfig`. **Planned (M5.2):**
  a `backups` volume.
- Network: one Compose network with the fixed subnet `172.30.0.0/24`, which is the default
  `FOLD_TRUSTED_PROXIES` (C17).

### 11.2 Environment file

`.env` holds only what must exist before the app can start, plus optional overrides. The
annotated list is [`.env.example`](../../../.env.example); `docker-compose.yml` passes the
variables into the container, and `config.go` reads them.

```
DOMAIN=groups.example.church            # required; DNS A/AAAA must point at the VPS
FOLD_VERSION=0.5.0                      # required; an exact release, never "latest"
SESSION_SECRET=<openssl rand -base64 48> # required outside dev; at least 32 characters
DB_PASSWORD=<openssl rand -hex 24>      # required (Compose only)
# FOLD_SETUP_TOKEN=<openssl rand -hex 32>  optional shared first-run code (64 hex characters)
# FOLD_TRUSTED_PROXIES=172.30.0.0/24       the Compose subnet (C17); /0 is refused
# Email (one of): FOLD_POSTMARK_SERVER_TOKEN=...  or  FOLD_SMTP_URL=smtp://user:pass@host:587
# FOLD_EMAIL_PROVIDER=postmark|smtp   FOLD_EMAIL_FROM=Fold <noreply@example.church>
# FOLD_POSTMARK_WEBHOOK_SECRET=...         24+ letters and digits; unset, the hook answers 404
# FOLD_PUSH_HOSTS=...                      optional, additive push service hosts (M3.13)
```

Compose itself sets `APP_ENV=prod`, `PORT=8080`, `DATABASE_URL`, `COOKIE_SECURE=true` and
`SITE_URL=https://$DOMAIN`. Outside `APP_ENV=dev`, Fold refuses to start with a non-https
`SITE_URL` or `COOKIE_SECURE=false`.

**Planned:**
- `FOLD_SECRET_KEY` encrypts provider secrets in the database; back it up (M4.1, C8).
- SMS settings such as `FOLD_SMS_PROVIDER=telnyx` and `FOLD_TELNYX_API_KEY` (M3.7). Env values
  override the database (C8).

On the DigitalOcean path, `provision.sh` writes `.env` with fresh `SESSION_SECRET` and
`DB_PASSWORD`, mode 600, and never rotates them on a second run. Config is loaded lazily per
subcommand (C11): `fold version` needs nothing, and `fold migrate status` needs only
`DATABASE_URL`.

### 11.3 Setup wizard (D7: the app configures as much as it can)

**Built today:** the first-run page `/setup` (server-rendered, `templates/setup.html`). Until the
first user exists, every other page redirects there (`/healthz` and the hooks still answer). It first asks for the setup code, which is
`FOLD_SETUP_TOKEN` or, when that is unset, a new code the app prints to its log on startup. Then it
asks for the church name, the church time zone, and the first admin's name, email, password and
confirmation that they are 18 or older. The admin is created in one transaction under an advisory
lock that refuses when any user exists, so two app processes cannot both create one. Each process
caches "setup done" once it sees a user.

Some later-step settings already exist on Admin > Settings: self-signup `request` or `off`
(`self_signup`), the church time zone, the church postal address, guardian copies, where replies go, push on or off, and
rooms on or off.

**Planned (M4.2):** the **setup wizard in the SPA** at `/admin/setup`, backed by `SetupState` rows.
The first-admin gate then reads `SetupState` from the database on each request, not a cached flag.
Each step can be re-run later from Admin > Settings. **Planned (M4.8):** `fold setup --config
setup.yaml` runs the same steps from `internal/setup`.

Steps (each names the milestone that builds it):

1. **Church and admin** (first-run page today; account policy in M4.2): church name, time zone,
   first admin's name, email, and password (email is the login, DEC-23). The admin confirms they
   are 18 or older. Also sets the account policy: self-signup `request` (default; every
   self-signup and member "invite a friend" waits for admin approval) or `off` (DEC-4).
- **`branding`** (optional, skippable; M4.2b): church logo, accent color, and header style. Stable
  `SetupState` id `branding`, after step 1 and before step 2, well before Safeguarding (step 8).
  Skipping branding must not skip safeguarding.
2. **Address and site** (M4.2): postal address (for email footers and legal pages) and contact
   email. Checks that `SITE_URL` loads over HTTPS from inside the app.
3. **Email** (M4.4; required for anything beyond a demo):
   - Pick a provider (Postmark is preselected, DEC-21; SMTP is the fallback) and paste an API key,
     or SMTP details.
   - `VerifyCredentials`, then `EnsureSendingDomain(mail.<domain>)`, which returns the DKIM,
     SPF/return-path, and DMARC records.
   - If a Cloudflare token is present, create the records by API. Otherwise show them in a
     copy-paste table.
   - Poll `DomainStatus` until verified; it continues in the background if the admin closes the
     page.
   - `EnsureWebhook` (bounces, complaints, deliveries, and inbound replies) with a generated
     secret, then send a test email to the admin.
   - Add the MX record for `reply.mail.<domain>` that the reply relay needs (7.8, M3.11), by API
     with a Cloudflare token or in the copy-paste table.
   - Today, email is set in `.env` and, on the DigitalOcean path, `email-dns.sh` creates the
     Postmark sender domain and its SPF, DKIM and return-path records.
4. **SMS** (M4.6; optional, can be skipped):
   - Telnyx is the SMS provider (a second adapter only when a church demands one, DEC-140). Paste
     credentials from the **church's own** account (DEC-2). `VerifyCredentials`.
   - Ask "Does the church have an EIN?" and choose `sms_registration_path` (DEC-3, 12.2):
     - **Yes**: EIN-based standard brand (10DLC), or toll-free verification with the EIN.
     - **No**: recommend getting a free EIN from the IRS first, and pause here. On Telnyx there is
       no sole-proprietor option for a church (12.2). If a Twilio adapter is ever built, offer its
       sole-proprietor path only after showing its limits (1 campaign, 1 number, about 1,000
       segments a day to T-Mobile) and that a named person, not the church, is the registered
       sender.
     - Never ask for 501(c)(3) proof.
   - Search for local or toll-free numbers and show the vendor's monthly price. The admin must type
     `BUY` to confirm before Fold calls `BuyNumber`. Fold never buys on its own.
   - `ConfigureNumber` sets the inbound and status webhooks.
   - Show the registration form pre-filled from church data and the generated legal pages (brand:
     legal name, EIN, address, website; campaign: use case Low-Volume Mixed or Mixed (never
     Charity), description, sample messages with no donation asks, opt-in flow, privacy and terms
     URLs). Submit by API where the provider allows it, otherwise show step-by-step instructions
     with a link to the vendor console.
   - Poll `RegistrationStatus`. SMS sending stays blocked, with a clear banner, until it is
     approved. Review typically takes days to weeks (Telnyx quotes about 72 business hours of
     carrier review after its own review).
   - Set the SMS send window (default 8 AM to 8 PM recipient-local, DEC-9, DEC-25), with the TCPA
     warning if it is set outside 8 AM to 9 PM.
5. **Storage** (M4.2): uploads use local disk, and this step shows where. The S3-compatible option,
   its put/get/delete test and `storage migrate` were cut with M4.7 (DEC-140).
6. **Video** (M4.2): the Jitsi domain for generated links (`jitsi_domain`, default `meet.jit.si`;
   Fold reads it today, but no screen sets it yet). Leaders choose link-out or Jitsi per event
   (built, M1.8).
7. **Backups** (M4.2): show where nightly backups go. Optionally set an offsite S3 bucket and an
   `age` public key. Show a "write down your FOLD_SECRET_KEY" reminder and require a tick to
   continue.
8. **Safeguarding** (M4.2): choose whether guardians get copies of messages to teens by email
   (`guardian_copies`, default off, email only; DEC-19, DEC-29). The setting exists today on Admin
   > Settings.
9. **People** (M4.5): import a CSV (name, email, phone, groups, and an optional "adult/teen" column
   with birth dates for teens) or invite by email. Imported adults become active accounts (they set
   a password from an emailed link). Imported teens start `pending_consent`. Rows under 13 are
   refused with a note to add them as household children. Rows that repeat an email already in the
   file or in Fold are offered as adult household people on that account (DEC-23). Imported phones
   are not opted in to SMS (10.4).
10. **Rooms and finish** (M4.2): turn rooms on or off and add rooms (both built, M2.1), then go to
    the dashboard. The dashboard keeps a "Setup checklist" card until every step is done or
    skipped.

### 11.4 CLI (C11)

Built (`cli.go`; `fold help` prints this list):

```
fold serve                      # the default when run with no arguments
fold healthcheck                # probes the local server; used by the Docker healthcheck
fold migrate [up|status|down-one]             # needs DATABASE_URL
fold push keys show                           # prints the web push public key; needs DATABASE_URL
fold push keys rotate [--force]               # new push keys, removes every device; needs SESSION_SECRET and DATABASE_URL
fold push check                               # connects to each push service on port 443
fold version                                  # needs no environment; --json adds recoveryPolicy (docs/agents/deploy.md)
```

Planned:

```
fold secrets rotate --new-key <key>           # re-encrypts provider secrets (M4.1)
fold setup --config setup.yaml [--dry-run]    # non-interactive wizard; prints what it would do (M4.8)
fold user reset-2fa <email>                   # break-glass for a sole admin's lost device (M4.9)
fold providers test [email|sms]               # sends a test or checks credentials (no milestone yet)
fold user create-admin --email ... --name ... # (no milestone yet)
```

`fold storage migrate` was cut with M4.7 (DEC-140).

All commands print plain text by default and JSON with `--json`, so agents can read the output.
`fold migrate down-one` is an offline maintenance command: stop every app instance and worker
first, and keep membership writes stopped until the rollback completes or the database is
upgraded again. It is not a live rollback path. Backups are not an app subcommand, because the
distroless app image has no shell or `pg_dump`; they run from `backup.sh` (11.8).

### 11.5 Rules for agents doing deployment

The rules live in [`AGENTS.md`](../../../AGENTS.md) and
[`docs/agents/deploy.md`](../../agents/deploy.md). Two more apply to any agent helping a church:

- Never buy numbers, change paid plans, or create provider or hosting accounts without the
  operator's explicit confirmation in the session. Provider and hosting accounts belong to the
  church (DEC-2): the church creates the account and adds you as a user, or gives you an API key.
  Never open accounts in your own name for a church, and never ask for the church's account
  password.
- Never print secrets back. Write them only to `.env` (mode 600), or through the setup API once it
  exists (M4.2).

The original outline for these rules is in
[`provenance/appendix-a-agents-deployment.md`](../provenance/appendix-a-agents-deployment.md).

### 11.6 Hosted single-tenant (D2, DEC-2, DEC-10)

- Fold is free MIT software (DEC-11). Self-hosting is the main path. Hosting for another church is
  the exception: Grier hopes not to do it often, or at all.
- When someone does host: the same Compose stack, one VPS per church, created by a short script
  against the VPS provider's API (Terraform optional), plus DNS.
- Provider accounts (email, SMS, and optionally storage and the VPS itself) belong to the church,
  even when hosted, unless there is a special agreement (DEC-2). The church pays vendors directly.
  The host may help set the accounts up, for example by joining the church's provider account as
  a user. This also keeps the host from being an ISV or reseller in Twilio's or Telnyx's sense, so
  the host has no sub-account or brand-registration duties.
- Terms and any fee are agreed case by case, and hosting is not meant to make money (DEC-10). Fold
  has no billing code, and none is planned.
- An ops repo keeps per-church `.env` files encrypted with SOPS or `age`. There is no shared
  control plane.
- **Fleet trigger (DEC-139):** when one operator hosts about five churches, build fleet tooling
  before hosting a sixth: one script or small Go program in the ops repo that, per church, pins and
  bumps `FOLD_VERSION`, runs the upgrade and reads `/healthz` plus the backup `LAST_OK` age, and
  reports version, health and last backup as one table. It drives the same per-church Compose
  stacks over SSH or the VPS provider's API. It is not part of the `fold` binary, holds no church
  data, and adds nothing shared between churches: no shared database, no shared login, no
  cross-church view. Each church still owns its accounts and can leave with its database and
  uploads (DEC-2).
- Multi-tenancy (one database serving many churches) stays out (DEC-139). Fleet pain is an
  operations problem and gets an operations answer; mixing teen, consent and contact data from many
  churches behind row filters would spend the isolation that makes the safeguarding rules simple to
  reason about.

### 11.7 Upgrades

Today, on the DigitalOcean path (details in
[`docs/DEPLOY-DIGITALOCEAN.md`](../../DEPLOY-DIGITALOCEAN.md#6-upgrades-and-rollback)):

1. Read the release notes and the upgrade notes in `README.md`.
2. Run the installed `/opt/fold/upgrade.sh --image ghcr.io/grierj/fold:x.y.z` (or `--commit` on a
   build-based install). It checks the recovery level, runs `backup.sh`, records the previous
   release and the new dump in `release.prev`, then runs `provision.sh`, which pins `FOLD_VERSION`
   and runs `smoke.sh`.
3. A plain Compose install sets `FOLD_VERSION=x.y.z` in `.env` and runs
   `docker compose pull && docker compose up -d`, after taking its own dump.

- Migrations run on start, under a Postgres advisory lock.
- If a migration fails, its transaction rolls back and the app **exits non-zero** with the error in
  the logs. Docker restarts it and `docker compose ps` shows it unhealthy. There is no read-only
  maintenance mode.
- Rollback is `upgrade.sh --rollback`. It hands off to `restore.sh`, which restores the recorded
  dump into a fresh database under the previous release and keeps the site closed while the
  operator deletes again anyone removed since the dump; it reopens by itself only when there is
  no one. Down migrations are best effort and not promised.
- Release images are tagged `x.y.z` (never overwritten) and `x.y` (final releases only), plus a
  commit-SHA alias; there is no `latest` tag. Compose and the scripts require an exact `x.y.z`.

**Planned (M5.3):**
- `docs/UPGRADING.md` lists any manual steps, from v0.5 on.
- Backup check: before applying pending migrations, the app reads `/backups/LAST_OK` from the
  `backups` volume, mounted read-only into `app`. If it is older than 24 hours, the app refuses to
  migrate. The check is skipped only when the database has no tables yet (a first install) or there
  are no pending migrations. There is no switch to turn it off.
- On Compose, the backup step becomes `docker compose exec backup backup.sh now` (M5.2).
- CI upgrades from the previous release with seeded data.

### 11.8 Backup and restore

**Built (DigitalOcean path):** `provision.sh` installs `/opt/fold/backup.sh` and a host cron job at
02:30 in the droplet's time zone. Each run writes mode-600 files under `/var/backups/fold`:

- `fold-YYYYMMDD-HHMM.dump` from `pg_dump -Fc`;
- `uploads-YYYYMMDD-HHMM.tgz` of the `uploads` volume;
- `LAST_OK` after a successful run.

Daily files are kept for `FOLD_BACKUP_KEEP_DAYS` (default 14), and dumps from the 1st of the month
for 180 days. With `/opt/fold/backup.env` filled in, each new backup is encrypted with `age` to the
configured public key and uploaded to an S3-compatible bucket (B2 recommended, 12.3); the private
key never touches the server. `backup.sh --restore-test` loads the newest dump into a throwaway
database. To restore, follow the
[interim recovery procedure](../../DEPLOY-DIGITALOCEAN.md#restoring-a-nightly-dump-interim-notes)
(`restore.sh`, rules in [`docs/agents/deploy.md`](../../agents/deploy.md)).

**Planned (M5.2):** a portable Compose design.

- A `backup` service with the same nightly dump, uploads tar (skipped if storage is ever S3) and
  retention, writing `/backups/LAST_OK` to the `backups` volume.
- On demand: `docker compose exec backup backup.sh now`.
- Optional offsite copies, encrypted with `age` as today.
- CI runs a restore test on every release: seed data, back up, restore into a fresh database, and
  compare row counts and a sample API response.
- The Admin > System page shows the last backup time, size, and offsite status (read from the
  `backups` volume), and warns when the last backup is older than 36 hours.

### 11.9 Development environment (DEC-1, DEC-24)

- `docker compose -f deploy/docker-compose.dev.yml up -d db` starts PostgreSQL 16 on
  `127.0.0.1:5432` (user, password and database `fold`) with a named volume. The baseline
  migration creates the `citext` and `btree_gist` extensions.
- `make dev` starts that database, then runs the Go server (`APP_ENV=dev`, `DATABASE_URL` pointing
  at it) and the Vite dev server.
- `make test` runs the Go tests against `DATABASE_URL`, starting the dev database when it is
  unset. The test helper migrates a template database once, then gives each test package a fresh
  copy (`CREATE DATABASE ... TEMPLATE`).
- `make e2e` builds the SPA and runs Playwright against the real binary in shards, each with its
  own server and fresh database. `npm test` in `web/` runs Vitest. Details:
  [`docs/agents/testing.md`](../../agents/testing.md).
- CI uses a `postgres:16` service container.
- There is no fallback database: every `APP_ENV` refuses to start without `DATABASE_URL`, with a
  message telling you to start the dev database.
