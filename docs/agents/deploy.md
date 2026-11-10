# Deployment, recovery levels and restore

Read this before touching `Dockerfile`, compose files, `deploy/`, release workflows or anything
that changes how an operator upgrades, rolls back or restores. The operator-facing procedure is
`docs/DEPLOY-DIGITALOCEAN.md`; the install path for churches is `README.md`.

## The shipped stack

- `docker compose pull && docker compose up -d`: app + Postgres 16 + Caddy (auto HTTPS). The app
  is the published `ghcr.io/grierj/fold:${FOLD_VERSION}` (`release.yml`); `FOLD_VERSION` is an
  exact release in `.env`, never `latest`. Developers (and anyone before the first release) add
  the override: `docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build`.
- The Dockerfile is multi-stage (node → go → distroless), `CGO_ENABLED=0`; no C compiler. The
  image carries only the binary, pins `WORKDIR /` (distroless nonroot defaults to
  `/home/nonroot`, off the volume) and pre-creates `/data/uploads` with `--chown=65532:65532` so
  the nonroot app can write to a fresh volume.
- Named volumes `pgdata` and `uploads` (mounted at `/data`). Do not drop `uploads`: an anonymous
  volume is discarded on `up --build`.
- The compose subnet `172.30.0.0/24` is the default `FOLD_TRUSTED_PROXIES`; change both together.
- Every `APP_ENV` refuses to boot without `DATABASE_URL`. Migrations run on start under a Postgres
  advisory lock; a failed one exits non-zero. First boot with an empty DB serves `/setup`; after
  any user exists the process caches that until restart.
- `GET /healthz` pings the database. Compose uses `/fold healthcheck` (distroless has no shell).
- `make compose-smoke` boots the shipped stack and checks `/healthz`, the redirect to `/setup`,
  and that 5432 and 8080 are not published off loopback (CI's `compose` job). Caddy, TLS and the
  volumes together are only checked by a live `docker compose up`
  (`docs/FIRST-CHURCH-CHECKS.md`).

## Production scripts (`deploy/digitalocean/`)

- Production is one VPS with this stack. `provision.sh` (run `--dry-run` first) never calls
  `doctl` and never creates droplets, firewalls, snapshots, backup plans or buckets; the church
  owns the droplet, DNS and email account. `smoke.sh` rechecks health, TLS and that Postgres and
  the app are not published; `upgrade.sh` pins an image tag or commit, dumps first, and can
  restore.
- QC and deploy scripts never grep application source. What the QC-1 harness takes from the
  application is `deploy/digitalocean/qc1/app-facts.json` (`TestQC1AppFacts` and its siblings;
  `UPDATE_QC1_FACTS=1 go test -run TestQC1AppFacts .` regenerates the `go` section for review).

## Recovery levels (R4-RP)

- The level is integer `recoveryPolicy` from `/fold version --json` in an image, or the one-line
  `deploy/recovery-policy` at a source commit (it equals `recoveryPolicyLevel` in
  `restore_fence.go`). Absent metadata means 0; malformed metadata refuses.
- Scripted provision, forward upgrade and rollback refuse a target below the greater of
  `DIR/recovery-policy-floor` and the level of the locally pinned image. The floor file is written
  only after smoke passes and never decreases; a higher pin enforces refusal even after failed
  smoke. There is no override. Restore data with the current release instead of starting a
  lower-level one.
- Run installed `DIR/upgrade.sh`: it installs its own tool set in `DIR/deploy-tools`, re-execs
  there, checks the target before backup or checkout, and runs that copy's provisioner. An older
  checkout's script run by hand can bypass these checks. Installed upgrades keep their frozen
  tools; install newer tools from a newer trusted checkout, subject to the `deploy-tools-version`
  check.
- **Never publish `recoveryPolicy >= 1` before R4-6c-R's current-release restore tooling and
  operator procedure are verified on a real droplet.** Local refusal drills do not satisfy that
  gate or the real-droplet checks in `docs/FIRST-CHURCH-CHECKS.md`. This gate is review only.

## Locks

Mutating provision and upgrade runs share `DIR/ops.lock` through inherited fd 9; a second
operation exits 75 naming the holder from informational `ops.owner`. `backup.sh` and its expiry
refuse with exit 75 while `DIR/recovery.lock` exists; `DIR/backup.lock` coordinates them with the
lock's publication. **Never delete, rename or replace `ops.lock`, `recovery.lock` or
`backup.lock` to get past a refusal.**

## Restore (`restore.sh`)

- `restore.sh --dump` restores into a fresh database swapped in whole and keeps the public site
  closed and background deliveries paused in a worker-free private recovery instance until
  `--reopen`. It never restores into the live `fold` database.
- A plain `--dump` exits 0 with the site closed; `--reopen-if-none` (used by
  `upgrade.sh --rollback`) exits 3 unless the count is exactly zero. `--abort` works only before
  the swap; only `--abort` finishes an interrupted abort; `--resume` after an interrupted reopen
  closes the site and returns to private recovery; `reconcile-needed` writes nothing and is only
  rechecked.
- A Droplet Backup restore is outside this protection. It does not restore uploads. Docs call the
  procedure "recommended" and make no support promise.

## Full restore drill frequency

Run the full `deploy/digitalocean/restore-drill.sh` only when at least one of these holds: the
change has a high chance of altering the restore behavior the drill checks; the work changed the
drill itself; or more than 30 days have passed since its last run. Record the reason and the run
date with the evidence. For other changes, use targeted checks. A recent run does not by itself
satisfy a release gate: assess whether its candidate and evidence cover the changes being
released.
