# R1 — Deployment rethink and the v0.5 activation spine

- ID: R1 (audit `docs/AUDIT-2026-11-01.md:69`)
- Priority: P0, before a first church goes live; audit sequence step 2
  (`docs/AUDIT-2026-11-01.md:261`)
- Effort: L (audit estimate, `docs/AUDIT-2026-11-01.md:78`), sliced below
- Status: Approved 2026-11-01. Implements PLAN C10 and M5.1–M5.4 plus
  the mail gate from M4.2/M4.4. Reverses nothing.
- Related, already fixed outside this brief: SEC1 setup takeover. The
  current tree has the token and lock the audit asked for
  (`setup.go:28` `newSetupToken`, `setup.go:79` `createFirstAdmin`
  taking `pg_advisory_xact_lock` at `setup.go:81`, token load at
  `config.go:166`). Do not re-litigate it here; do not weaken it.
- Out-of-scope audit neighbours: R7 and R8 were rejected. Nothing in
  this brief freezes web push, strikes SMS, or adds open signup.
  Push reachability stays a first-class deploy check.

This is a brief, not code. Read `AGENTS.md` first. `docs/plan/PLAN.md`
is the source of truth; raise disagreements instead of resolving them
silently. Standing rules that bind every slice: single Go binary with
the SPA embedded (`main.go:309`), Postgres only, new goose files only
for schema changes (this brief needs none; see Data and API shapes),
pure permission functions in `internal/policy` if a slice ever adds an
endpoint, `audit.Write` and `notify.Send` in the same transaction for
sensitive person-facing changes (no slice here adds one), camelCase
under `/api/v1`, an error code plus `ERROR_COPY` for any new
person-facing error, shared controls in `web/src/components/ui` for
any UI, tests beside the code they test, and docs that say
"recommended" (the docs-wording guard fails the build otherwise).
Verification for every slice: `go build ./...`, `go vet ./...`,
`make lint-go`, `make lint-sh` when shell changes, `make test`, and
`(cd web && npx tsc --noEmit)`, `(cd web && npx knip)`,
`cd web && npm test`, `make e2e` when a user flow changes.

## Part 1 — the deployment rethink (evidence, then verdict)

### 1.1 The workload a host must run, from the code

Any candidate host has to run all five of these at once. A host that
runs four is a port, not a deployment.

1. One always-on Go process serving the JSON API and the embedded SPA
   from one artifact (`main.go:235` `setupRouter`, `main.go:309`
   `//go:embed web/dist`), running goose migrations on start under a
   Postgres advisory lock (`db.go:22` `openDatabase`, `db.go:51`
   `migrateUp`, `migrations/migrations.go:25` `NewProvider`).
2. Real PostgreSQL 16 with Postgres-only features — jobs claim with
   `FOR UPDATE SKIP LOCKED` (`internal/jobs/jobs.go:218`), setup and
   migrations take advisory locks, emails are `citext`. A different
   database engine is a rewrite, and DEC-1/DEC-24 forbid it.
3. In-process background work that must run while nobody is looking:
   `startJobs` starts workers and the daily/hourly scheduler in the
   serving process (`jobs_daily.go:89`, called at `main.go:157`).
   Reminders, digests and push delivery die on any host that sleeps
   the process between requests.
4. Persistent local files for avatars and covers. The only storage
   implementation wired today is the local filesystem
   (`main.go:117` `storage.NewLocalStore`, `storage/storage.go:56`);
   the S3 seam is an interface and a comment (`storage/storage.go:13`),
   planned as M4.7, not built. A host with an ephemeral filesystem
   loses uploads until M4.7 lands.
5. Outbound egress: HTTPS to the allow-listed browser push services
   (`cli.go:226` `runPushCheck` probes them on 443) and either SMTP
   or the Postmark HTTPS API for mail (`internal/mail/registry.go:20`,
   `internal/mail/registry.go:32`).

Plus the operational floor: TLS on the church's domain, cookie
sessions behind exactly one trusted proxy (`config.go:70`,
`main.go:247`), and a `pg_dump` exit path so a church can always
leave. The data includes minors' contact and consent records, so
single-tenant isolation per church is itself a safety property
(PLAN 1, DEC-2: the church owns its own vendor accounts).

### 1.2 Can Cloudflare host this workload?

Vendor pages below were fetched and read on 2026-11-02. Each claim
names the exact page it came from; prices must be re-checked before
anyone quotes them to a church.

**Workers — no.** A Worker is a JS/Wasm isolate, 128 MB of memory per
isolate, CPU time capped per request (10 ms free; 30 s default, 5 min
maximum on Paid), no filesystem, no long-lived process
([Workers limits](https://developers.cloudflare.com/workers/platform/limits/)).
Fold is a distroless Go binary with an embedded SPA, templates read
from disk (`Dockerfile:30`) and workers polling every second.
Running it on Workers means rewriting the product in another
runtime. That breaks the one-artifact rule outright.

**D1 — no.** D1 is a SQLite-family, single-threaded database capped
at 10 GB per database
([D1 limits](https://developers.cloudflare.com/d1/platform/limits/)).
Fold needs `SKIP LOCKED`, advisory locks, `citext`, `tstzrange`
exclusion constraints and goose-on-Postgres. DEC-1/DEC-24 say
Postgres only, everywhere. D1 is not a candidate at any price.

**Hyperdrive — not a host.** Hyperdrive pools and accelerates
connections from Workers to an existing external Postgres
([Hyperdrive overview](https://developers.cloudflare.com/hyperdrive/)).
It hosts neither the app nor the database. It only matters inside a
Workers rewrite, which is already ruled out.

**Containers — a port, not a deployment.** Cloudflare Containers run
OCI images behind a Worker/Durable Object front door, billed per
10 ms while actively running on top of the $5/month Workers Paid
plan, with scale-to-zero as the pricing premise; instance types top
out at 4 vCPU / 12 GiB / 20 GB disk, and disk persistence is via
snapshots retained 30 days, not a named volume
([Containers overview](https://developers.cloudflare.com/containers/),
[Containers pricing](https://developers.cloudflare.com/containers/platform/pricing/),
[Containers limits](https://developers.cloudflare.com/containers/platform-details/limits/)).
Three concrete mismatches: (a) there is no Compose — Postgres would
have to be a second container on snapshot disk (running a church's
only database on 30-day-rotating snapshot storage is a data-loss
design) or an external managed Postgres, which is a second vendor
and a second bill; (b) the always-on job runner fights the
scale-to-zero model — keep it awake and the per-10 ms meter runs all
month, let it sleep and reminders stop; (c) uploads need the unbuilt
M4.7 S3 store (R2) before the container tier is even stateless.
Verdict: revisit only if M4.7 ships and a church asks; not v0.5.

**Tunnel — a useful variant, not the default.** `cloudflared` makes
outbound-only connections, so a church-owned box with no public IP
can still serve its domain
([Tunnel overview](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/)).
That fits PLAN 12.5's church-owned-box row. Costs of adopting it:
the trusted-proxy story changes (`config.go:70` pins trust to the
Compose subnet; a tunnel means trusting `cloudflared` instead and
reading `CF-Connecting-IP` deliberately, never by default), and
`smoke.sh` currently fails on a Cloudflare-issued certificate on
purpose (`deploy/digitalocean/smoke.sh:172`). Document it later as
the church-box path; do not make a Cloudflare account a prerequisite
for the recommended install.

**R2 — yes, in a narrow role.** R2 is S3-compatible object storage:
$0.015 per GB-month, 10 GB-month free monthly, no egress charge
([R2 pricing](https://developers.cloudflare.com/r2/pricing/)).
Fold's offsite backup already speaks "any S3-compatible endpoint"
(`deploy/digitalocean/backup.sh:260` `encrypt_and_upload`), so R2
can be named beside Backblaze B2 as an encrypted-backup target with
zero code change. It is also the natural bucket when M4.7 adds the
S3 `storage.Store`. It is not an uploads answer today.

**DNS and cache — yes, with one hard rule.** Cloudflare does not
cache HTML or JSON by default, and does not cache a response whose
`Cache-Control` is `private`, `no-store`, `no-cache` or `max-age=0`,
or that carries `Set-Cookie`, or answers a non-GET request
([default cache behavior](https://developers.cloudflare.com/cache/concepts/default-cache-behavior/)).
Fold already sends `no-store` on every `/api/v1` response and on the
SPA shell (`main.go:413`), `no-cache` on `/sw.js` (`main.go:421`),
and long immutable caching only on hashed `/assets/`
(`main.go:423`). So a proxied (orange-cloud) record is safe **only**
under these exact settings, which any future Cloudflare doc must
state verbatim: no "Cache Everything" rule, no Automatic Platform
Optimization, respect the origin's `Cache-Control`, and explicit
cache bypass for `/api/v1/*`, `/setup*`, `/hooks/*`, `/u/*` and
`/uploads/*`. The recommended setup stays simpler: DNS-only (grey
cloud) for the app host so Caddy owns TLS end to end — the current
guide already says so (`docs/DEPLOY-DIGITALOCEAN.md:97`) and the
mail-DNS helper already writes Cloudflare records DNS-only
(`deploy/digitalocean/email-dns.sh:233`). Authenticated responses
must never be cached anywhere; that sentence belongs in every
variant doc this brief produces.

### 1.3 The other candidates, on the same criteria

Prices fetched 2026-11-02 from the pages named (re-check before
quoting). "Uploads" means: works with today's local-disk store.

| Option | Small-church cost/mo | Uploads today | Volunteer ops | Exit |
|---|---|---|---|---|
| DO Droplet + Compose | $12 (2 GB); $6 after prebuilt image | Named volume | One repo, three scripts | `pg_dump` + volume tar |
| Hetzner VPS + Compose | from €5.99–6.49 (EU) | Named volume | Same scripts | Same |
| DO App Platform + Managed PG | from $5 container + database | No: needs M4.7 store | Dashboard, no SSH | Image + `pg_dump` |
| Fly.io + Managed PG | Machine + $38 Basic MPG | Volume per Machine | `flyctl`, zones, replicas | Image + `pg_dump` |
| Render + disk + PG | About $16–29 (PLAN 12.5) | Disk, one instance | Dashboard, Blueprint | Image + `pg_dump` |
| Railway | $20 Pro minimum + usage | Volume per service | Dashboard, usage meter | Image + `pg_dump` |
| CF Containers + ext. PG | $5 + metered + PG bill | No: needs M4.7 store | Wrangler + Worker shim | Image + `pg_dump` |
| Coolify / Dokku on a VPS | VPS price only | Volume | A second system to run | Same as VPS |

Sources, all fetched 2026-11-02: Droplet and App Platform prices
([droplets](https://www.digitalocean.com/pricing/droplets),
[App Platform](https://www.digitalocean.com/pricing/app-platform),
[managed Postgres](https://docs.digitalocean.com/products/databases/postgresql/));
the App Platform container floor is a 512 MiB instance at $5 and a
development database at $7 — a production managed cluster costs
more, and the platform filesystem does not persist uploads, so it
waits for M4.7 like every other stateless tier. Hetzner entry prices
([cloud](https://www.hetzner.com/cloud/)); the repo guide already
runs the same `provision.sh` there (`docs/DEPLOY-DIGITALOCEAN.md:410`).
Fly: Managed Postgres Basic is $38/month before the app Machine
([Fly Postgres](https://fly.io/docs/postgres/),
[pricing](https://fly.io/docs/about/pricing/)), and a Fly Volume
lives on one server attached to one Machine with no replication —
Fly's own page warns a single Machine plus volume risks downtime
and data loss ([volumes](https://fly.io/docs/volumes/overview/)).
That is a worse uploads story than one VPS volume, at several times
the price. Render remains exactly what PLAN 12.5 and DEC-26 say: a
credible later, on-request path (M5.5), with the disk trade-offs
reconfirmed — daily snapshots kept 7 days, a disk prevents
zero-downtime deploys ([disks](https://render.com/docs/disks),
[pricing](https://render.com/pricing)). Railway's production-shaped
plan starts at a $20 monthly minimum plus metered usage
([pricing](https://railway.com/pricing),
[volumes](https://docs.railway.com/reference/volumes)); a usage
meter is the wrong bill for a church treasurer, and it buys nothing
the VPS lacks. Coolify and Dokku are real, credible single-server
PaaS layers ([Coolify](https://coolify.io/),
[Dokku install](https://dokku.com/docs/getting-started/installation/),
which wants 1 GB just for itself) — but Fold already ships
provision/smoke/upgrade scripts for exactly one app; adopting
either means the volunteer now operates Fold **and** a general
platform, and Fold's hardening checks (`deploy/digitalocean/checks.sh`)
stop being the thing that runs. Rejected on the one rule: more to
run, not less.

Per-criterion notes the table cannot hold:

- Volunteer upgrade/backup: only the VPS rows keep the exact
  `docker compose up` mental model from `AGENTS.md` and the scripts
  already in the repo. Every managed row still needs an uploads
  answer (M4.7) or a one-instance disk, plus its own backup ritual.
- Minors-data safety: single-tenant-per-church holds on every row,
  but the VPS rows keep data residency and account ownership
  simplest (the church's own Droplet/Hetzner account, DEC-2), and
  keep Postgres unpublished — enforced, not just configured
  (`deploy/digitalocean/checks.sh:103` `assert_compose_private`,
  `deploy/digitalocean/checks.sh:201` `assert_published_ports`).
- TLS: Caddy automatic HTTPS on the VPS rows (`Caddyfile:1`);
  managed rows terminate TLS for you, which is nicer until the
  trusted-proxy and client-IP work (PLAN 12.5) is owed for each one.
- Fleet story (many single-tenant deployments, PLAN 11.6): identical
  Compose stacks scripted per church; R13's written trigger at about
  five hosted churches stands. No candidate improves the fleet story
  enough to justify a second deployment shape now.
- Lock-in/exit: every row exits by `pg_dump` plus an uploads copy,
  because the artifact never changes. GHCR image distribution
  ([container registry](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry))
  is itself portable OCI; nothing in Slice 1 ties a church to GitHub
  beyond pulling an image it could also build.

### 1.4 Verdict

Droplet-class VPS + Docker Compose is the winner, with Cloudflare in
a narrow supporting role — not as the host. Keep one recommended
path: a 2 GB DigitalOcean droplet in the US, a Hetzner CX-class
server in the EU, same Compose stack and scripts on both.
Cloudflare's role, exactly: (1) optional DNS host with the app
record DNS-only, plus the existing `email-dns.sh` automation for
SPF/DKIM/return-path; (2) R2 named as one S3-compatible offsite
backup target beside Backblaze B2 (encrypted ciphertext only);
(3) Tunnel documented later, only for the church-owned-box variant.
Never cache authenticated responses — the settings in 1.2 are the
contract if anyone proxies. No Workers, D1, Hyperdrive or
Containers work is created by this brief. The rest of this brief
therefore spends its effort where the audit pointed: make the
winning path pull instead of build, back itself up anywhere Compose
runs, prove itself in CI, and refuse to look "successfully deployed"
while email is unset.

## Part 2 — the R1 brief (winning path)

### 2.1 Goal

A church volunteer with a fresh 1–2 GB VPS and a domain reaches the
setup wizard by pulling a published image — no Go, no Node, no
on-box build that can run out of memory — gets nightly backups and a
proven restore from the shipped Compose file alone (no
DigitalOcean-specific helper required), can run one command that
says plainly whether mail, database, uploads and push will work,
and upgrades by changing one pinned version. CI boots the shipped
Compose stack on every change so this cannot silently rot.

### 2.2 Current state

- The shipped Compose builds on the host: `build: .` at
  `docker-compose.yml:20`, multi-stage Node then Go at
  `Dockerfile:2` and `Dockerfile:10`. The guide admits a 1 GB
  droplet is only viable "after a published image exists"
  (`docs/DEPLOY-DIGITALOCEAN.md:38`), and the provisioner dies
  without `--allow-onbox-build` for the same reason
  (`deploy/digitalocean/provision.sh:291`).
- The image plumbing is ready and waiting: `FOLD_VERSION` is already
  a build arg stamped into the binary (`Dockerfile:17`,
  `cli.go:23`), `provision.sh` already accepts `--image`, refuses
  `:latest` (`deploy/digitalocean/checks.sh:38`), and rewrites the
  Compose file by replacing the `build: .` line
  (`deploy/digitalocean/provision.sh:650` `write_image_compose`).
  `upgrade.sh` already records image-vs-build mode
  (`deploy/digitalocean/upgrade.sh:187`). Only the publisher (CI)
  and the default are missing.
- Backups cover only provisioned droplets. The root Compose has no
  backup service; the README offers manual `pg_dump` commands
  (`README.md:45`) and names M5.2 as planned. The droplet helper is
  good — dump, uploads tar, retention, `age` offsite, `LAST_OK`,
  `--restore-test` (`deploy/digitalocean/backup.sh:200`,
  `deploy/digitalocean/backup.sh:212`,
  `deploy/digitalocean/backup.sh:230`,
  `deploy/digitalocean/backup.sh:260`,
  `deploy/digitalocean/backup.sh:296`) — but it is installed by
  host cron from `provision.sh` (`deploy/digitalocean/provision.sh:780`),
  so a plain `docker compose up` install has no backups at all.
  Its restore test also only counts tables in a scratch database;
  uploads restore is unproven (audit, "Not verified").
- Email can be silently absent. With no provider set, the server
  logs one warning and serves anyway with `mail.NoSender{}`
  (`main.go:188` `chooseMailer`, `main.go:201`); every send then
  fails at send time (`internal/mail/mail.go:399`). A deploy can
  pass smoke and still be unable to invite anyone or reset a
  password. `smoke.sh` checks health, TLS and port privacy, and
  warns on push reachability (`deploy/digitalocean/smoke.sh:219`),
  but never checks mail.
- CI never boots the shipped stack. The workflow runs unit tests,
  the SPA build and Playwright against a locally run binary, plus a
  stubbed provisioner dry-run (`.github/workflows/test.yml:15`,
  `.github/workflows/test.yml:58`, `.github/workflows/test.yml:115`);
  no job runs `docker compose` against the root file. The
  `provision` job proves the scripts parse, not that the stack
  serves.
- Health and setup surfaces for the smoke to hit already exist:
  `/healthz` pings Postgres (`main.go:479`) and answers even before
  setup (`main.go:266`); the setup unlock page renders without a
  code (`main.go:574`); the first-run code is printed to the app log
  (`main.go:139`). `TestHealthz` (`main_test.go:122`) and the CLI
  tests (`cli_test.go:29`, `cli_test.go:43`, `cli_test.go:234`)
  are the patterns to extend.
- PLAN already specifies the target shapes this brief must keep:
  C10 (`image: ghcr.io/grierj/fold:${FOLD_VERSION}`, build kept in a
  dev override), the four-container shape with a `backups` volume
  (PLAN 11.1), the pre-migration backup gate reading
  `/backups/LAST_OK` (PLAN 11.7), and the `backup` container
  contract with `backup.sh now` and `restore.sh` (PLAN 11.8).

### 2.3 Scope

In: GHCR publishing in CI; root Compose switched to a pinned pulled
image with the build kept as an override; provision/upgrade script
follow-through; a portable `backup` service in the root Compose
with restore proof in CI; the app-side pre-migration backup gate;
`fold doctor` including the mail gate; a CI job that boots the
shipped Compose stack and smokes `/healthz` and `/setup`; the
upgrade runbook; README and deploy-guide updates; R2 named as a
backup target option in the backup env example and guide.

Out: any Cloudflare hosting target; Render/Fly/Railway files
(M5.5 stays future, DEC-26); the SPA setup wizard and encrypted
provider settings (M4.1/M4.2 own those; `fold doctor` only reports);
the S3 uploads store (M4.7); an Admin System "last backup" panel
(PLAN 11.8 names it — it is a follow-up slice once the backup
volume exists, noted under Dependencies, not silently dropped);
multi-arch beyond `linux/amd64` + `linux/arm64`; automatic upgrades
of any kind; buy/create-anything automation (the scripts' current
never-creates-resources rule stands,
`deploy/digitalocean/provision.sh:7`).

### 2.4 Slices (in order; image and CI first)

**Slice 1 — Publish the image; Compose pulls it (M5.1, C10).**
New `.github/workflows/release.yml`, separate from `test.yml` so the
least-privilege `contents: read` on the test jobs
(`.github/workflows/test.yml:11`) is untouched: trigger on `v*`
tags plus `workflow_dispatch`; `permissions: contents: read,
packages: write` on this workflow only; build with Docker Buildx
for `linux/amd64,linux/arm64` from the existing `Dockerfile`,
passing `FOLD_VERSION` from the tag; push
`ghcr.io/grierj/fold:<x.y.z>`, `:<x.y>` and `:<sha>` — never make
docs or Compose default to any floating tag. Emit the image digest
and a build provenance attestation into the release notes (PLAN
M5.1 acceptance wants signatures or checksums published; the
attestation plus recorded digest is the v0.5 answer — see Open
questions). Root `docker-compose.yml`: replace `build: .`
(`docker-compose.yml:20`) with
`image: ghcr.io/grierj/fold:${FOLD_VERSION:?Set FOLD_VERSION in .env}`;
add `docker-compose.build.yml` (new) carrying `build: .` for
developers and CI's local-build leg, per C10. `.env.example` gains
`FOLD_VERSION=` with "pin an exact release" wording.
Follow-through that will break if skipped:
`deploy/digitalocean/provision.sh` — `write_image_compose`
(`:650`) dies when there is no `build: .` line to replace; rework it
to set or verify `FOLD_VERSION` in the written `.env` instead of
rewriting YAML, keep `--image` as an override that writes an
explicit `image:` pin, and keep the `:latest` refusal in
`deploy/digitalocean/checks.sh:38` covering both paths. `--allow-onbox-build`
then means "also copy the build override"; plain deploys copy only
`docker-compose.yml`, `Caddyfile`, `.env.example` (the image branch
of `install_runtime_files`, `deploy/digitalocean/provision.sh:690`, becomes the normal
branch). `deploy/digitalocean/upgrade.sh` — `current_release`
(`:187`) greps for the `# fold-app-image` marker that Slice 1
removes; rework it to read `FOLD_VERSION` from `.env` (image mode)
or the override's presence (build mode), and update `load_release`
validation (`:223`) to match. `docs/DEPLOY-DIGITALOCEAN.md` size
table (`:36`–`:46`): after this slice the 1 GB size with swap is
the floor, 2 GB stays the recommendation. `README.md` quick start
(`:11`–`:15`): `docker compose pull && docker compose up -d`, with
the build override named as the developer path. Done when a tagged
release produces a pullable multi-arch image and
`docker compose pull && docker compose up -d` reaches `/setup` on a
machine with no Go or Node (proven by Slice 2, recorded by hand
once for the first church — see 2.8).

**Slice 2 — CI boots the shipped Compose stack.**
New job `compose` in `.github/workflows/test.yml` and new
`scripts/compose-smoke.sh` (shell only, joins `make lint-sh` via
`Makefile:66`; add a `make compose-smoke` target beside
`provision-dry-run`, `Makefile:71`). The job: build the app image
locally once (`docker build`, tag `ghcr.io/grierj/fold:ci`), write a
throwaway `.env` (`DOMAIN=localhost`, generated `SESSION_SECRET`
and `DB_PASSWORD`, `FOLD_VERSION=ci`), and bring the stack up with
a new CI-only override `deploy/compose.ci.yml` that (a) publishes
the app on `127.0.0.1:18080:8080` only — loopback is the one bind
the privacy checks allow (`deploy/digitalocean/checks.sh:123`) —
(b) sets `SITE_URL=http://localhost:18080` and
`COOKIE_SECURE: "false"`, and (c) points Caddy at an internal-only
site or drops the `caddy` service for the smoke, since Let's
Encrypt cannot issue for `localhost`. Choose (c)-internal-CA over
dropping Caddy if it stays a five-line override; the point is
proving app+db+compose wiring, and Caddy's real-domain TLS stays a
first-church hand check (2.8). The script then asserts, in order:
containers healthy (`docker compose ps`); `GET /healthz` returns
200 with an empty body (same contract as `main_test.go:122`);
`GET /` redirects to `/setup`; `GET /setup` returns the unlock
page; the app log contains exactly one "First-run setup code" line
(`main.go:139`) — assert its presence, never print its value into
CI logs beyond that line, which the app already emits; no service
publishes 5432 or 8080 on a non-loopback address (reuse the
`compose_ps` parsing pattern from `deploy/digitalocean/smoke.sh:118`,
do not copy the host checks). Tear down with volumes removed.
Run the same script a second time in "release mode" from
`release.yml` after the push, pulling the just-published image by
digest — this is the "the artifact we shipped is the artifact we
smoked" guarantee. Adding `compose` to the required branch checks
is a repo-settings change: flag it for Grier, do not assume it
(see Risks).

**Slice 3 — Portable backup service in the root Compose (M5.2).**
New directory `deploy/backup/` (PLAN 5.1 repo layout) containing
`Dockerfile` (`postgres:16-alpine` plus `age` and `rclone`, per
PLAN 11.1; pin package versions the way CI pins actions),
`backup.sh` and `restore.sh`; published as
`ghcr.io/grierj/fold-backup:<x.y.z>` from `release.yml` on the same
tag cadence as Slice 1, with a `docker-compose.build.yml` build
entry for developers. Port, do not rewrite, the proven droplet
logic: `pg_dump -Fc` (`deploy/digitalocean/backup.sh:200`), uploads
tar (`:212`), retention — daily `FOLD_BACKUP_KEEP_DAYS` (default 14)
plus day-01 files for 180 days (`:230`) — `age`-encrypted S3 upload
with the all-or-nothing env validation (`:147`, `:260`), mode-600
outputs and `LAST_OK` written only after success (`:287`). What
changes inside a container: no `docker` CLI — connect to
`postgres://church@db:5432/fold` directly (`PGPASSWORD` from the
same `DB_PASSWORD`), read the `uploads` volume mounted read-only,
write to a new `backups` named volume, and loop on a church-time
02:30 schedule using `TZ` passed from `.env` (new optional
`FOLD_BACKUP_TZ`, default UTC documented as such; the church zone
lives in the database and a shell loop must not go fishing for it).
`backup.sh now` and `backup.sh restore-test`, plus
`restore.sh <dump> [uploads.tgz]`, are the contract from PLAN 11.8.
Extend restore-test past the droplet version's table count
(`deploy/digitalocean/backup.sh:296`): it must also list the
uploads tar's entries and count files, so a dump-without-uploads
failure cannot pass. Root `docker-compose.yml` gains the `backup`
service **after** `caddy` (see `config_test.go:25`, which cuts the
`app` block at `\n  caddy:\n` — either keep that layout or update
the test's cut in the same PR), the `backups` volume, and a
read-only `backups` mount into `app` at `/backups` for Slice 4.
Backup env moves into the main `.env` as the existing
`FOLD_BACKUP_*` / `AWS_*` names (`deploy/digitalocean/backup.env.example`);
`.env.example` documents them as optional with B2 and R2 named as
the two example endpoints (R2: `https://<account-id>.r2.cloudflarestorage.com`,
region `auto` — verify against the R2 S3 docs at implementation
time, do not trust this brief for the literal string).
`deploy/digitalocean/provision.sh`: stop installing the host
backup cron (`:780`) and the host `backup.sh` for new installs once
the service exists; a re-run on an older install must remove
`/etc/cron.d/fold-backup` rather than double-run, and say so in the
summary (`:836`). Keep `deploy/digitalocean/backup.sh` itself until
the service reaches parity, then delete it and the `backup.env`
machinery in the same PR that updates
`docs/DEPLOY-DIGITALOCEAN.md` section 5 — one backup system, not
two. CI proof (in the `compose` job, after Slice 2's asserts):
unlock setup is not needed — seed by SQL instead (create one user
row and one uploads file through the volume), run
`docker compose exec backup backup.sh now`, assert a dump, an
uploads tar and `LAST_OK` exist, run `backup.sh restore-test` and
assert its table and file counts match the seed, then
`restore.sh` into the live stack per PLAN 11.8's order (stop app,
restore, start app) and re-assert `/healthz`.

**Slice 4 — Pre-migration backup gate (M5.3, app side).**
In `db.go`: give `migrateUp` (`db.go:51`) a pre-check before
`p.Up`. Read pending state from the same provider (`p.Status`,
the call `cli.go:122` already uses). Gate rule, PLAN 11.7: when
migrations are pending **and** the database already has tables
**and** `FOLD_BACKUP_DIR` names an existing directory, require
`<dir>/LAST_OK` no older than 24 hours, else exit non-zero with one
operator sentence — what is stale, and the exact command
(`docker compose exec backup backup.sh now`) — never a stack of
wrapped errors. `Config` gains `BackupDir` from `FOLD_BACKUP_DIR`
(`config.go:158` `LoadConfig`); root Compose sets it to `/backups`
with the read-only mount from Slice 3. When the variable is unset
(dev, tests, bare-binary runs) there is nothing to read and the
gate does not apply — this is the deliberate reconciliation with
PLAN's "no switch to turn it off": the shipped Compose always sets
it, so the shipped path has no off switch; record that reading in
the PR description for Grier (Open questions). First installs skip
the gate by the no-tables condition, which keeps Slice 2's fresh
boot working with an empty `backups` volume. Also land
`docs/UPGRADING.md` (new) in this slice: read notes → backup now →
set `FOLD_VERSION` → `pull && up -d` → smoke → rollback is
restore-plus-repin (PLAN 11.7), and make
`deploy/digitalocean/upgrade.sh` call `backup.sh now` through
Compose instead of the host script once Slice 3's parity PR lands.

**Slice 5 — `fold doctor` and the mail gate (M4.4, operator face).**
New `doctor.go` and `doctor_test.go` beside `cli.go`; register
`doctor` in `run` (`cli.go:42`) and in the usage text (`cli.go:25`),
loading only what it needs like its siblings (C11: `migrate` at
`cli.go:75` is the shape). Checks, each `OK`, `WARN` or `FAIL` with
a one-line fix, none of them printing a secret value:
config loads (`LoadConfig`, `config.go:158` — its errors are
already value-free, e.g. `shortSecretMessage`, `config.go:128`);
database reachable and migrations current (Postgres ping then
`p.Status`, `cli.go:122`); setup finished or still waiting (count
users, the `setupComplete` question at `main.go:494` — informational,
never a FAIL); **mail configured** — the mail gate: `chooseMailer`
(`main.go:188`) must not fall back to `NoSender`
(`main.go:201`), checked via `mail.Choose`
(`internal/mail/registry.go:41`) returning non-empty and the adapter
building without error, plus the `FOLD_EMAIL_FROM` localhost warning
promoted to WARN (`main.go:204`); uploads directory writable by the
process (`storage.NewLocalStore`, `storage/storage.go:62`, probe
with a create-and-delete in a `doctor-` temp name — never via a
real key); push services reachable (reuse `runPushCheck`'s probe,
`cli.go:226`, WARN only — a church with `push_enabled` off is not
broken); public URL answers (`SITE_URL` when set: it is `https`
outside dev and `GET <SITE_URL>/healthz` returns 200 — request only
the configured URL, follow no redirects, 5 s timeout, so doctor
cannot be pointed at anything else); backup freshness when
`FOLD_BACKUP_DIR` is set (same `LAST_OK` read as Slice 4, WARN over
36 hours per PLAN 11.8, FAIL never — the gate owns refusal).
Flags: `--json` (the shape in 2.5), and `--send-test EMAIL` which,
and only which, sends one real test message through the configured
adapter and reports the provider's answer; without it doctor sends
nothing, so it is safe in cron. Exit codes: 0 all OK/WARN, 1 any
FAIL, 2 bad usage — matching `run`'s existing convention
(`cli.go:70`). Wire it where operators already look:
`deploy/digitalocean/smoke.sh` runs
`docker compose exec -T app /fold doctor` after the push check
(`:219`) — a `NoSender` FAIL fails the smoke, which means a
mail-less droplet no longer reports a clean deploy; `provision.sh`
runs it in `run_smoke`'s stead as part of the same step and its
summary (`:836`) replaces "put SMTP in .env" advice with "run
doctor until mail says OK". The server does **not** gain a boot
refusal: an existing mail-less install must keep serving its
directory while the operator fixes mail, and first-run setup must
stay reachable before any mail exists. The gate is doctor FAIL +
smoke FAIL + the existing log warning — visible everywhere an
operator looks, blocking nowhere a member does.

**Slice 6 — Docs the volunteer actually reads (M5.4).**
`docs/DEPLOY.md` (new, thin): the generic path for any Ubuntu/Debian
VPS — fill `.env`, `docker compose pull && docker compose up -d`,
setup code from logs, `fold doctor` until green, backup and upgrade
pointers — with DigitalOcean and Hetzner as the two named examples
linking out, and the Cloudflare paragraph from Part 1 (DNS-only,
R2 as backup target, the never-cache-authenticated-responses rule,
Tunnel only for a church-owned box). `docs/DEPLOY-DIGITALOCEAN.md`
keeps the droplet specifics and shrinks where Slice 3/4 made its
sections generic (backups, upgrades). `README.md` upgrade-notes
section gains the pre-image-to-image move for any existing
build-based install. `AGENTS.md` Deployment section gains the
one-liner that images come from GHCR and Compose never builds by
default, per PLAN Appendix A's intent. Every doc worded with
"recommended"; the guard test enforces it.

### 2.5 Data and API shapes

Migrations: **none**. No slice changes the schema, so no new goose
file, no GORM model change, no `models.All()` change. (If an
implementer believes one is needed, that is a stop-and-report: the
file is `migrations/NNNNN_name.sql` at the next free number at
implementation time, never an edit to an old file.) New state lives
where it already lives: `/backups/LAST_OK` (one UTC timestamp line,
mode 600), dumps `fold-YYYYMMDD-HHMM.dump`, uploads archives
`uploads-YYYYMMDD-HHMM.tgz`, encrypted siblings with `.age`.

Environment deltas, all in `.env` / `.env.example`:
`FOLD_VERSION` (required, exact `x.y.z`), `FOLD_BACKUP_TZ`
(optional, default UTC), `FOLD_BACKUP_DIR` (set by Compose to
`/backups`; read by the app and doctor), and the existing optional
`FOLD_BACKUP_S3_*`, `AWS_*`, `FOLD_BACKUP_AGE_RECIPIENT`,
`FOLD_BACKUP_KEEP_DAYS` names unchanged so droplet knowledge
transfers. `Config` gains `BackupDir string` only (`config.go:19`).

`fold doctor --json` shape (camelCase, stable for agents):

```json
{"version":"0.5.0","checks":[
  {"name":"mail","status":"fail",
   "detail":"no email provider is set",
   "fix":"set FOLD_POSTMARK_SERVER_TOKEN or FOLD_SMTP_URL in .env, then docker compose up -d"}]}
```

`status` is `ok|warn|fail`; `detail` and `fix` are one line each and
never contain a secret, an endpoint URL path, or an email address
(the same redaction duty `jobs.CleanError` carries). Human output is
the same fields as `OK  mail: sending through postmark` lines. No
new HTTP route, no new API error code, no `ERROR_COPY` entry, no new
response type or `tsContracts` pair — doctor is a CLI, and the only
person-facing text it adds is operator-facing. The backup gate's
refusal is a startup log/stderr sentence, likewise outside the API
error system; keep it that way.

### 2.6 Tests

Existing files to extend, with what each gains:

- `config_test.go` — `TestComposePassesTransactionalMailEnvironment`
  (`config_test.go:16`) keeps passing across the Slice 1/3 layout
  changes (update its service-block cut only if Slice 3 lands the
  `backup` service before `caddy`); add a check that the `app`
  block carries `FOLD_BACKUP_DIR: /backups` once Slice 4 lands.
- `cli_test.go` — `TestVersionNeedsNoEnvironment` (`cli_test.go:29`)
  is the pattern for `TestDoctorNeedsOnlyItsOwnEnv`; `run`'s exit-2
  convention gets a doctor case (`cli_test.go:197` shows the push
  equivalent).
- `db_test.go` — add the gate cases beside `db.go`:
  `TestMigrationGateSkipsFreshInstall`,
  `TestMigrationGateRefusesStaleBackup`,
  `TestMigrationGatePassesWithFreshBackup`,
  `TestMigrationGateSkipsWhenNoPendingMigrations`. Use a temp dir
  for `LAST_OK` with controlled mtimes; never the real `/backups`.
- `main_test.go` — `TestChooseMailer` (`main_test.go:236`) is the
  seam doctor's mail check reuses; extend its table if doctor needs
  a case it lacks (broken provider builds) rather than duplicating.
- `setup_test.go` and `main_test.go:122` (`TestHealthz`) — unchanged,
  but they are the reason Slice 2 can assert contracts instead of
  inventing them.

New files: `doctor_test.go` (table over check outcomes; the mail
cases reuse the fake-getter style of `main_test.go:237`; the
`--send-test` case uses the dev-outbox adapter so no network is
touched; a secret-leak case asserts a sentinel `SESSION_SECRET`
never appears in either output form), `scripts/compose-smoke.sh`
exercised by CI itself, and shell-side coverage through
`make provision-dry-run` and `make lint-sh` for every script touch
(`provision.sh`, `upgrade.sh`, `smoke.sh`, the two new backup
scripts join the shellcheck list in `Makefile:66` — extend its glob
to `deploy/backup/*.sh` in Slice 3's PR). Add one Go guard so the
default cannot drift back: `TestShippedComposePullsAPinnedImage`
in `config_test.go` — the root Compose `app` service has an `image:`
line naming `ghcr.io/grierj/fold:${FOLD_VERSION` and no `build:`
key, and `docker-compose.build.yml` is the file that carries
`build: .`. E2E: no new Playwright spec — no user flow changes; the
`compose` CI job is this brief's browser-free end-to-end proof, and
the setup page it fetches is already covered by `main_test.go:188`
and the a11y suite. If the follow-up System panel slice is taken
later, it brings its own e2e then.

### 2.7 Acceptance checklist

- [ ] A `v*` tag publishes `ghcr.io/grierj/fold` and
  `ghcr.io/grierj/fold-backup` for `linux/amd64` and `linux/arm64`,
  with digests and provenance in the release notes; `fold version`
  inside the image prints the tag (`cli.go:23`).
- [ ] On a machine with Docker but no Go/Node:
  `docker compose pull && docker compose up -d` reaches `/setup`;
  the same flow on a 1 GB VPS with swap stays up (hand check, 2.8).
- [ ] The `compose` CI job boots the root Compose file and passes
  health, setup-redirect, port-privacy and setup-code asserts;
  the release workflow re-smokes the published digest.
- [ ] `docker compose exec backup backup.sh now` writes a dump, an
  uploads tar and a fresh `LAST_OK`; `restore-test` verifies table
  and file counts; `restore.sh` brings the seeded stack back with
  `/healthz` green (all in CI).
- [ ] Offsite, when configured, uploads only `.age` ciphertext; the
  private key is never on the server; a partial offsite config
  fails loudly instead of uploading plaintext.
- [ ] With pending migrations, existing data and a stale or missing
  `LAST_OK`, the app exits non-zero naming the remedy command; a
  fresh install and a no-pending start are never gated.
- [ ] `fold doctor` fails on unset mail, passes with Postmark or
  SMTP configured, prints no secret in either output form, and
  `--send-test` delivers one message to the named address.
- [ ] A provisioned droplet with no mail configured now fails
  `smoke.sh`; with mail set, the full provision → doctor → upgrade
  round trip passes on the image path, and `--rollback` restores
  the recorded dump and version.
- [ ] `docs/DEPLOY.md`, `docs/UPGRADING.md`, README and the
  DigitalOcean guide agree with the shipped files; a volunteer
  following DEPLOY.md on a fresh 2 GB VPS reaches the wizard in
  under 30 minutes (hand check, 2.8).
- [ ] Sanity gates per AGENTS.md on every PR: `go build ./...`,
  `go vet ./...`, `make lint-go`, `make lint-sh`,
  `(cd web && npx tsc --noEmit)`, `(cd web && npx knip)`,
  `make test`, `cd web && npm test` (no web change expected, run
  it anyway), and the `test`/`web`/`provision` CI checks green.

### 2.8 Risks, rollback, security

- Supply chain is the new attack surface. The release workflow is
  the only job with `packages: write`; everything else keeps
  read-only permissions (`.github/workflows/test.yml:11`). Pin
  third-party actions by commit SHA the way `test.yml` already does
  (`:34`), publish the digest with every release, and have the
  upgrade path pin `FOLD_VERSION` — never a floating tag, the rule
  `deploy/digitalocean/checks.sh:38` already enforces for scripts.
  A compromised GHCR token is the worst case; the mitigation is
  that churches pull by version they chose after reading notes,
  not by anything automatic.
- Branch protection: adding `compose` to the required checks is a
  repo-settings change an agent must flag, not assume. Until Grier
  adds it, the job runs but does not block merge; say exactly that
  in the Slice 2 PR.
- The backup container holds database credentials and can read
  every upload — same trust as the app, so keep it as small: no
  published ports (the privacy asserts must treat `backup` like
  `db`/`app`), read-only `uploads` mount, plaintext dumps only on
  the `backups` volume at mode 600, only `.age` ciphertext leaves
  the box, and the `age` private key never lands on the server
  (the droplet helper's rule, `deploy/digitalocean/backup.sh:10`).
  Extension needed: `assert_compose_private`
  (`deploy/digitalocean/checks.sh:103`) currently names `db` and
  `app` only; add `backup` and `caddy`-except-80/443 awareness in
  Slice 3, with the dry-run tests following.
- The migration gate can brick an upgrade night if `LAST_OK` is
  stale because the backup service itself is down. That is the
  point of the gate, but the refusal message must carry the remedy
  command, and the escape remains human and visible: run a backup,
  or restore the volume — there is deliberately no env override in
  the shipped Compose (see the reconciliation in Slice 4). Rollback
  of the slice itself is re-pinning the previous `FOLD_VERSION`,
  whose binary has no gate.
- Doctor must not become an oracle or a sender: it reads only its
  own configured URLs, follows no redirects, sends mail only under
  the explicit `--send-test` flag to the address typed by the
  operator, and its output passes the secret-leak test in 2.6. It
  runs inside the app container from cron/smoke with the app's own
  environment — no new credentials are created for it.
- Rollback per slice: Slice 1 — the build override keeps the old
  path one flag away (`--allow-onbox-build`,
  `deploy/digitalocean/provision.sh:96` keeps its meaning). Slice 2 — CI-only, delete
  the job. Slice 3 — the host-cron helper remains until parity;
  reverting is re-enabling the cron the slice removed, and dumps in
  both places use the same `pg_dump -Fc` format, so nothing is
  stranded. Slice 4 — previous image has no gate. Slice 5 —
  doctor is additive; smoke's doctor step is the only behavioural
  change and reverts with it. Slice 6 — docs only.
- Data-safety invariants that no slice may trade away for
  convenience: Postgres and the app stay unpublished; `.env` stays
  mode 600 and uncommitted (`deploy/digitalocean/checks.sh:50`,
  `:60`); `SESSION_SECRET` guidance unchanged (changing it signs
  everyone out and drops push devices,
  `docs/DEPLOY-DIGITALOCEAN.md:270`); setup stays token-gated and
  single-admin (`setup.go:79`); TLS stays Caddy-owned on the
  recommended path, with the Cloudflare-certificate refusal in
  smoke kept (`deploy/digitalocean/smoke.sh:172`).

### 2.9 Dependencies and order

Slice 1 first (everything pulls from it), Slice 2 immediately
after so Slices 3–5 land on a booted-stack check; Slice 3 before
Slice 4 (the gate reads what the service writes); Slice 5 any time
after Slice 1 (parallel-safe with 3/4, but its backup check wants
Slice 4's `FOLD_BACKUP_DIR` plumbing); Slice 6 last, in pieces as
each fact becomes true — never document a pull path before Slice 1
publishes. Cross-brief: R6 (notification outbox) makes doctor's
mail check more meaningful but does not block it; R9's migration
squash, if taken, should land before Slice 4's tests hard-code
migration counts (they must not — assert pending-ness, not counts).
Follow-ups this brief deliberately leaves queued, in order: the
Admin System last-backup panel (PLAN 11.8 — needs an admin-only
endpoint behind a pure `internal/policy` check, an error code plus
`ERROR_COPY` for its failure copy, a `tsContracts` pair, and
`components/ui` pieces with 390px proof); the Tunnel church-box
variant doc; release-to-release upgrade CI from v0.5 on (PLAN M5.3
acceptance — boot the previous tag's image, seed, bump
`FOLD_VERSION`, assert data survives); M4.7's S3 store, which is
when R2-for-uploads and the stateless-host question may be
reopened.

### 2.10 First-church checks to add (same PR, per DEC-128)

Add to `docs/FIRST-CHURCH-CHECKS.md` under "Live install" as each
slice lands — these need a real domain, server or mail provider an
agent cannot honestly claim:

- Pull-only install on a real 1 GB droplet with swap and on a
  Hetzner CX-class server: wizard reached, memory graph sane after
  a week of reminders and push.
- Real-domain Caddy issuance through the shipped Compose with the
  DNS-only Cloudflare record and with plain registrar DNS.
- `fold doctor --send-test` to a real inbox per provider
  (Postmark API and one plain SMTP host), plus the SPF/DKIM/DMARC
  header check the existing email section already asks for.
- A full restore onto a second fresh droplet from an offsite
  `.age` copy (B2 and, if used, R2), timed, with avatars visible
  after — the uploads half CI can only prove with seed files.
- An upgrade across two real released tags on the church's own
  droplet, including `--rollback`, before the first church's second
  month.
- An `arm64` boot (church-owned mini PC or ARM VPS) of the same
  tag, since CI publishes but cannot yet run that architecture.

### 2.11 Open questions (with recommended defaults)

1. Image signing: cosign signatures, or digest + provenance
   attestation in the release notes (PLAN M5.1 says "signed or
   checksums published")? Recommended default: digest + attestation
   now; cosign when a second maintainer exists. Revisit if a church
   asks how to verify.
2. Does the gate's "no off switch" (PLAN 11.7) accept the Slice 4
   reading — always on in shipped Compose, inapplicable where
   `FOLD_BACKUP_DIR` is unset? Recommended default: yes; record it
   as a PLAN clarification in the Slice 4 PR rather than adding a
   `FOLD_BACKUP_CHECK=off` escape hatch someone will leave on.
3. Should the `compose` CI job become a required branch check
   beside `test`, `web`, `provision`? Recommended default: yes —
   it is the only job that proves the shipped artifact boots — but
   it is Grier's settings change to make.
4. Backup schedule source: fixed `FOLD_BACKUP_TZ` env (Slice 3) or
   read the church zone from the database each loop? Recommended
   default: env, set by `provision.sh` from its `--timezone`; a
   shell loop querying app settings couples backup liveness to app
   schema details for one number.
5. Keep `provision.sh`'s `--image` override after `FOLD_VERSION`
   becomes the mechanism? Recommended default: keep one release for
   droplet-flavoured pins (forks, hotfix images), then remove it
   with the host backup helper in the parity PR.
6. First published version number: `0.5.0` (PLAN's name for this
   milestone set) even if some M4 items land after it? Recommended
   default: yes — tag `0.5.0` when Slices 1–3 and 5 are in and CI
   is green; the wizard depth of M4 continues behind it.

## Sources for Part 1 (all fetched 2026-11-02)

Cloudflare: [Workers limits](https://developers.cloudflare.com/workers/platform/limits/),
[Containers](https://developers.cloudflare.com/containers/),
[Containers pricing](https://developers.cloudflare.com/containers/platform/pricing/),
[Containers limits](https://developers.cloudflare.com/containers/platform-details/limits/),
[Tunnel](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/),
[R2 pricing](https://developers.cloudflare.com/r2/pricing/),
[Hyperdrive](https://developers.cloudflare.com/hyperdrive/),
[D1 limits](https://developers.cloudflare.com/d1/platform/limits/),
[default cache behavior](https://developers.cloudflare.com/cache/concepts/default-cache-behavior/).
DigitalOcean: [droplets](https://www.digitalocean.com/pricing/droplets),
[App Platform](https://www.digitalocean.com/pricing/app-platform),
[managed Postgres](https://docs.digitalocean.com/products/databases/postgresql/).
Others: [Hetzner cloud](https://www.hetzner.com/cloud/),
[Fly pricing](https://fly.io/docs/about/pricing/),
[Fly volumes](https://fly.io/docs/volumes/overview/),
[Fly Postgres](https://fly.io/docs/postgres/),
[Render pricing](https://render.com/pricing),
[Render disks](https://render.com/docs/disks),
[Railway pricing](https://railway.com/pricing),
[Railway volumes](https://docs.railway.com/reference/volumes),
[Coolify](https://coolify.io/),
[Dokku install](https://dokku.com/docs/getting-started/installation/),
[GitHub container registry](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry).
Repo-side deployment facts were read in the tree, not from memory:
`docker-compose.yml`, `Dockerfile`, `Caddyfile`, `.env.example`,
`deploy/digitalocean/{provision,backup,smoke,upgrade,checks,email-dns}.sh`,
`docs/DEPLOY-DIGITALOCEAN.md`, PLAN 11, 12.5–12.7 and M5.1–M5.5.
Prices drift; re-check the named page before quoting any number to
a church.





