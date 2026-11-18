# Deployment, recovery levels and restore

Read this before touching `Dockerfile`, compose files, `deploy/`, release workflows or anything
that changes how an operator upgrades, rolls back or restores. The operator-facing procedure is
`docs/DEPLOY-DIGITALOCEAN.md`; the install path for churches is `README.md`.

## The shipped stack

- `docker compose pull && docker compose up -d`: app + Postgres 16 + Caddy (auto HTTPS). The app
  is the published `ghcr.io/grierj/fold:${FOLD_VERSION}` and the backup service the matching
  `ghcr.io/grierj/fold-backup:${FOLD_VERSION}` (`release.yml` stages both images under `stage-<version>-<commit>`, smokes that digest pair, then writes the exact tags, SHA aliases and attestations from the same digests, then the GitHub release (the done mark), then the `:x.y` tags in a separate `minor` job that a "Re-run failed jobs" finishes; a retry reuses the staged pair, and an exact tag is only accepted when it already holds that digest; `scripts/release-tag-commit.sh` refuses a version tag that no longer peels to the built commit before `promote` and `notes`, `notes` refuses until both images pull anonymously, and `minor` leaves `:x.y` on a newer already-released patch; a newer patch whose notes failed has no release, so an older retry can move `:x.y` back until that patch is finished); `FOLD_VERSION` is an
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
- `make compose-smoke` builds `fold:ci` and `fold-backup:ci` and boots the shipped stack and checks `/healthz`, the redirect to `/setup`,
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
  The facts include the text of the erasure's retention report and, once the QC-1 work that records
  it has merged, of discovery's first audit statement; a saved real-server log under
  `deploy/digitalocean/testdata/` is checked against that text (`TestQC1SuccessorIsTheLoggedStatement`,
  which also pins the log's hash). Never regenerate a fact with a changed statement to make a check
  pass: that test fails, and only a new Docker-captured log fixes it (`docs/agents/erasure.md`).

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

## Host archives, adopted copies and the backup service

Read this before touching the backup image, the `backup` Compose service, `backup.sh`, or the archive
section at the end of `checks.sh`.

- **Layout.** The host's originals live in `/var/backups/fold` (`backup.sh`). The service's archives are
  top-level files of the `backups` volume. The host keeps a copy of each original in `adopted/` at the root
  of that volume: `root:root` mode 0700, holding root-owned mode-0600 files with the original's bytes and
  exact mtime. The volume root is `root:65532` mode 1770 (the image creates it so; `archive_layout` repairs
  only that root and creates `adopted/`, and refuses any other `adopted/` without following or repairing
  it). The service runs as `65532:65532` with `cap_drop: ALL`, so it cannot list, read, change, remove
  or rename `adopted/`. The image has no `adopt` command, and nothing in it reads `adopted/`.
- **One section.** Volume resolution, layout, adoption, retention, the pending file, the export and the
  settings copy are all in `checks.sh`; `backup.sh`, `provision.sh` and `upgrade.sh` call it. Do not write a
  second copy. Resolution (`archive_volume_resolve`) only observes Docker: bounded by one absolute deadline,
  expected Compose labels, local driver without options, canonical mountpoint under `DockerRootDir`, safe
  ancestors. It returns 3 for a successfully observed absence, 4 for a failed observation, 5 for an identity
  mismatch, and never creates anything. The one managed creation is `provision.sh`'s
  `up --no-start --no-deps backup`; the backup service starts only after the layout is validated and the
  archives adopted.
- **Locks.** Adoption, expiry, export and the producer all run under `backup.lock` (`ops.lock` first when a
  caller holds it) and refuse under `recovery.lock` (exit 75). The copy and `sync` children inherit the lock
  descriptor, so a killed parent cannot release admission while a child can still write.
- **No overwrite.** Host production, adoption and the rollback export write a root-private `.tmp.*` file
  and publish it by a no-replace link. An existing archive is never truncated or replaced; a same-minute
  `backup.sh` refuses (75). A stamp whose existing copy differs in any member is refused whole, and no
  missing sibling is filled. An existing copy with equal bytes and a different mtime is repaired after a
  stable-source check; every accepted copy and its directory are synchronized, including on an identical retry.
  An existing copy, adopted or exported, is accepted only as root:root mode 0600 with one link and is never
  repaired in place (a differing owner could still hold a descriptor): adoption refuses the stamp, the export
  returns 6 and `upgrade.sh` names it. Before a stamp is filled every recognized destination sibling is looked at,
  and one with no surviving original (the original expired first) refuses the stamp too, so a conflict followed
  by unequal member expiry cannot become a mixed pair.
- **Retention.** One predicate for every population (`archive_due`): age strictly greater than `KEEP_DAYS` times
  86400, day-01 files kept while below 180 days, `KEEP_DAYS` above 180 honored. The whole listing and every
  member's metadata are collected before the first deletion; unknown names, links and folders are untouched.
  `backup-expiry-pending` (root-private, in the deploy dir) records each decision (`keep now orig copy`) with a
  synchronized replacement before anything is deleted under it. The originals are expired first (no Docker on
  that path), then the copies; a side completes only after all due deletions and the directory sync; a
  decision leaves the file when both sides are done. Equal `keep` values merge by the greater `now` and reset
  both sides when that widens it. A crash, a failed Docker observation, a `KEEP_DAYS` increase, a provision or
  a rollback therefore cannot lose a date a side still owes. `backup.sh --expire-only` exits 4 while copy work
  is pending. A full run validates `KEEP_DAYS` first and alone: incomplete offsite settings fail the run (no
  backup is made) and expiry still runs under the lock; an unusable `KEEP_DAYS` expires nothing. With no backup service in Compose, the service's own top-level archives are expired under `.env`
  retention and reported on their own line.
- **Cutover** (`backup_cutover` in `provision.sh`, after smoke and the recovery floor): settings copy, identity
  check, coordination check, volume resolution (create only when absent), layout, adoption, start,
  and a `LAST_OK` newer than the attempt before `backup_scheduler_select cutover-success DIR` makes the host cron
  expire-only. The fresh `LAST_OK` is required on every run, a repeat one included (the service may have been
  recreated). An I/O or identity failure ends in `cutover_fail` (site up, smoke passed, floor recorded; `upgrade.sh`
  reports it without a rollback hint). It installs the full host cron durably (`backup_cron_install`: sync the staged
  file, rename, sync the folder; an identical file is synced too; any failed step fails the install), then
  stops the service with `backup_service_halt` (Compose `stop backup`, verified with `backup_service_running`, its own
  30 seconds). Both done is exit 77, the host confirmed as the one producer; a cron that cannot be flushed (service left
  alone) or a service that cannot be confirmed stopped is exit 78, and `upgrade.sh`'s `run_provision` words each apart,
  never telling an operator the host makes the backups after 78. Both producers upload offsite, and neither reads the
  bucket (no provider behaviour that S3-compatible stores differ on is required). Each uploading
  run makes `RUN_ID` (`new_run_id`, the same helper in host `backup.sh` and the image: `od -An -N16 -tx1
  /dev/urandom`, 32 lowercase hex characters, no pipeline, an explicit failure that fails the run before any PUT) and
  puts exactly two keys, `fold/<stamp>-<RUN_ID>/fold-<stamp>.dump.age` and `.../uploads-<stamp>.tgz.age`; the folder
  is the pair, and `restore.sh` still sees its unchanged basenames. Neither producer lists, heads, gets or deletes
  (no `list-objects`, `s3api`, ` lsf`, `--immutable`, flat `fold/${base}` key; `bucket_source_guard` in both
  harnesses), so the server's key needs only `PutObject` (+ `AbortMultipartUpload`; B2
  `writeFiles`). Be honest in docs and reviews: ownership of a folder is probabilistic, not provider-enforced; B2
  `writeFiles` can still create deletion markers (permanent version deletion needs `deleteFiles`); Spaces grants
  broader deletion; atomic visibility of a finished PUT is an assumption observed only on the pinned server in T8/T7P;
  age authenticates each file, not the pair; half pairs and incomplete multipart uploads wait for a bucket
  lifecycle rule; legacy flat keys may hold mixed pairs. The host also observes the uploads volume three ways before
  dumping (`uploads_volume_state`: present, absent = failed inspect plus a successful `volume ls -q` without the exact
  name, unknown = anything else), and fails closed on the last two with different messages. The host and the service
  still never produce together (host `backup.sh` refuses with 75 while `backup_service_running` says the service runs,
  an unobservable state refuses with 1, and `service_backed` in `upgrade.sh` treats a running service as the producer
  even before the cron is expire-only), now only so a night does not get two pairs. A run that fails between its two
  uploads leaves a half pair, or after a lost answer a complete folder, and the next run uses a new stamp and folder.
  Provenance for a restore after two producers shared a bucket is the producer's log, not "newest folder" (host
  `offsite:`/`uploaded` lines, image `published ... remote=fold/S-ID/` and `offsite: uploading to`). The image's
  `now` line gained a trailing `remote=` field only with offsite; `offsite=off` is byte-identical and `status.json`
  and its step words did not change. A refused stamp is reported and is not a failure. `install_backup_cron` keeps an
  existing expire-only cron across the deploy that precedes the cutover. One absolute budget, `FOLD_CUTOVER_WAIT`, is
  `ARCHIVE_CAP`: `archive_deadline` never passes it, and `observe_compose` (the identity, coordination and
  service-defined reads) is bounded by it only while it is set, so `restore.sh` keeps its plain `compose_in`. A
  disposable drill (`DEPLOY_DRILL`) and `--pin-only` never run it.
- **Identity and paths.** `backup_identity_check` reads the resolved configuration and has one rule: exactly
  `65532:65532`, `cap_drop` containing `ALL`, no `cap_add`, `privileged`, host `pid` or host network, and the shipped
  paths: one `backups` volume mount at `/backups` (the volume `archive_volume_resolve` inspects), one
  `backup-status` mount at `/backup-status`, the `uploads` volume read-only at `/data`, and no
  `FOLD_BACKUP_ARCHIVE_DIR`, `_STATUS_DIR`, `_DATA_DIR` or `_APP_UID`/`_APP_GID` that differs from the image's. An unset
  field is refused like a wrong one. The effective mounts are checked, not only the three exact targets: those three
  are whole volumes (no `volume.subpath`), no other mount sits inside `/backups`, `/backup-status` or `/data` and no
  tmpfs sits at or inside them (a read-only empty bind at `/data/uploads` would archive nothing and still report
  success; the equal-or-inside test is the tmpfs one only, because the three volumes themselves are exactly those
  roots), a target with `.`, `..` or `//` is refused, `volumes_from` is refused, and so is any non-empty `configs`,
  `secrets` or `devices` (a config with no target defaults to `/<name>`, and a long-syntax one can land anywhere). The recovery scheduler also runs `backup_volume_check`, read-only: an existing
  volume's root must still be `root:65532` mode 1770 and any `adopted/` root-only (an absent volume is Compose's to
  create from the image). Every managed start asks it first: the cutover, the drill's `up -d`
  (`start_stack`) and the recovery scheduler (`backup_scheduler_select recovery`, so a restore refuses with
  `cleanup-failed at scheduler`, lock kept, and starts nothing). `restore.sh` itself is unchanged.
- **Upgrade and rollback.** With the service in charge (Compose defines it and the cron is expire-only, or its container is running),
  `upgrade.sh` runs the service's `backup.sh now`, takes the dump name it reports, and publishes that dump
  under `/var/backups/fold` through the same no-replace copy (a differing file of that name refuses before the
  checkout or the pin changes). The recorded `release.prev` dump is that host file, so rollback is unchanged.
  Older deployments keep host production.
- **Tests.** `backup-test.sh` covers the image. The host side is proven on a real droplet, not in CI
  (DEC-154): the November 17 real-droplet checks covered backup, restore and archive expiry; remaining
  live checks are in `docs/FIRST-CHURCH-CHECKS.md`.

## Restore (`restore.sh`)

- `restore.sh --dump` restores into a fresh database swapped in whole and keeps the public site
  closed and background deliveries paused in a worker-free private recovery instance until
  `--reopen`. It never restores into the live `fold` database.
- `--uploads` restores the matching uploads archive inside the fence. Before reopen,
  `fold restore settle-uploads` empties erased and unowned avatar folders; active accounts'
  folders remain. A target without the required `restoreUploads` capability is refused.
  Settlement exit 1 means cleanup is incomplete or unsafe entries need attention: keep the
  site closed and resume after resolving them. Do not extract uploads by hand.
- A plain `--dump` exits 0 with the site closed; `--reopen-if-none` (used by
  `upgrade.sh --rollback`) exits 3 unless the count is exactly zero. `--abort` works only before
  the swap; only `--abort` finishes an interrupted abort; `--resume` after an interrupted reopen
  closes the site and returns to private recovery; `reconcile-needed` writes nothing and is only
  rechecked.
- The count covers only accounts deleted live and active in the restored copy. A
  legacy reason save on an already deleted account changes neither side, so a zero
  count and recovery level 2 do not prove legacy decisions or removals survived:
  `--reopen-if-none` reopens without checking them, and an older same-level release
  may not serve the legacy review routes. After any restore or rollback, re-walk the
  legacy list and each affected account's page (Admin → Users → Deleted accounts with
  reasons to review, or the API) with a release that serves those routes. A "Needs
  review" badge or an empty list is not proof either. Do not change
  automatic reopen or the recovery floor to cover this without a lead/Grier product
  decision; no real restore was run to prove this note.
- A Droplet Backup restore is outside this protection. It does not restore uploads. Docs call the
  procedure "recommended" and make no support promise.

## Tombstone sweep version on upgrade, rollback and restore

`tombstoneSweepVersion` (`account_erasure_tombstones.go`) is 2: the first daily sweep after an upgrade
from version 1 finds every tombstone with an older or no marker once, and runs the whole erasure
pipeline for it under the contact gate (200 subjects per page; admin deletes and contact changes
may wait briefly that day). It clears audit `ip` blanks that the earlier pipeline left, which cannot be
undone in the live database; backups keep the old values until they expire. There is no migration, the
recovery level stays 2 and deploy stays one `docker compose up`. A rollback to a release that sweeps at
version 1 leaves version-2 markers as swept, and a deletion made during that window is found again by
the version-2 sweep after the next upgrade. A dump taken before the upgrade restores version-1 markers;
the next sweep clears them again. The tests model the restore in SQL; no real restore was run, and the
real-restore checks in `docs/FIRST-CHURCH-CHECKS.md` are unchanged.

## Full restore drill frequency

Run the full `deploy/digitalocean/restore-drill.sh` only when at least one of these holds: the
change has a high chance of altering the restore behavior the drill checks; the work changed the
drill itself; or more than 30 days have passed since its last run. Record the reason and the run
date with the evidence. For other changes, use targeted checks. A recent run does not by itself
satisfy a release gate: assess whether its candidate and evidence cover the changes being
released.

- The full drill is owed only on those established triggers, never because a candidate is
  being prepared or a reviewer asks for one more run.
- `--crossings-only` is a partial diagnostic and never counts as the full drill, so it does not
  reset the 30 days.

`scripts/reason-review-scale.sh` wraps this drill (`docs/agents/testing.md`, "Reason-review scale
run") and requires `--drill-reason` for a full drill step; this section stays the only statement
of the frequency.

The drill runs directly, as root, on a disposable droplet created for it and declared with
`FOLD_DRILL_ISOLATED_DAEMON=1`, after `deploy/digitalocean/qc1/browser-prepare.sh` has prepared the
browser (`docs/agents/testing.md`). It refuses an undeclared host, a remote or non-default Docker
daemon, a daemon that already holds a container, and any host path its provisioners would write,
before it creates anything. Never point it at a machine that runs anything you keep.

Two rules keep a drill's checks honest on Docker's containerd image store, where a container's
`.Image` is a config digest and an image's id is an index. Whether a container runs an image is
`assert_image` (`checks.sh`) alone, which accepts the raw ids when they match and otherwise the
platform manifest the image id selects; never compare `{{.Image}}` to an image id by hand. QC-1
records the verified id and keeps the raw one (`appImageRawId`, `dbImageRawId`, `rawImageId`) for
diagnosis. Drill `timeout`s are plain: at its deadline `timeout` ends its whole process group, so a
timed-out command leaves no descendant behind to race a snapshot or the teardown. Cancellation is
best effort, not guaranteed. A TERM to the drill is acted on when the command it is waiting for
ends, which a deadline bounds; if the supervisor's grace runs out first and it KILLs the drill, that
one command can run on, in its own group, until its own deadline (`pgrep -af 'docker compose'`,
then end it by hand). `deploy-drill.sh --prepare-only` removes its stack when it fails or is
signalled, and `restore-drill.sh` removes it if it stops between that hand-off and its own cleanup.
Nothing reaches a browser that Playwright's launcher detaches into its own group (its Node
handlers clean it up during the grace) or work the Docker daemon has already accepted.
