# fold-backup

The `fold-backup` image runs Fold's nightly and on-demand backups of the database and the
uploads volume, keeps them for a while, optionally copies age-encrypted versions to an
S3-compatible bucket, and publishes two small status files the app reads. It also carries the
one-shot helpers `restore.sh` uses to put an uploads archive back. It has no command for the
older host cron backup's archives: the host copies those itself (below).

Everything is `backup.sh` (bash) in an Alpine image built on `postgres:16-alpine`, so `pg_dump`
matches the database major. There is no Python and no Docker CLI in it.

## Image

`Dockerfile` pins the base by the multi-architecture index digest and every package by exact
version (Alpine 3.24, PostgreSQL 16.16): bash, coreutils (for `mv --exchange`), findutils, tar,
gzip, util-linux and flock, tzdata, age and rclone. The build fails if a pinned version has left
the repository or a promised tool is missing, and checks that no scripting-language interpreter
beyond bash and no `docker` client is present (DEC-151). The service runs as `65532:65532` with
every capability dropped (Compose sets both). `/backup-status` and `/data/uploads` are pre-created
owned by 65532 so fresh named volumes are writable; `/backups` is pre-created `root:65532` mode
`1770`: the service writes and removes its own top-level archives and cannot remove, rename or
open what root keeps in that volume (the host's `adopted/` folder, below). `ENTRYPOINT` is
`backup.sh`, `CMD` is `loop`, so `docker run IMAGE --help` prints usage.

The base image declares `VOLUME /var/lib/postgresql/data`. Nothing writes there; a deployment may
mount a `tmpfs` over it to avoid an anonymous volume.

### Updating the base and the packages

1. `docker buildx imagetools inspect postgres:16-alpine` prints the index digest and the
   `org.opencontainers.image.version` (for example `16.16-alpine3.24`). Copy the digest into
   `FROM`.
2. Read the Alpine release from that annotation. For each pinned package find the exact version
   in `https://dl-cdn.alpinelinux.org/alpine/vX.Y/{main,community}/<arch>/APKINDEX.tar.gz` (or
   `docker run --rm IMAGE apk policy <pkg>`), and check amd64 and arm64 agree.
3. Update the `apk add` pins together with the digest, build both platforms, and run
   `docker run --rm IMAGE --self-test`.
4. Keep `postgresql16-client` at the database's major. A new database major needs a new base and
   a documented upgrade path, not just a bump.

## Commands

| Command | What it does |
|---|---|
| `loop` | Default. Runs `now` when a backup is due; keeps a heartbeat for `health`. |
| `now` | One backup. Success prints one line: `published stamp=S dump=fold-S.dump uploads=uploads-S.tgz offsite=off` or, with offsite, `... offsite=uploaded remote=fold/S-ID/` (ID is the run's 32-hex folder id). Exit 75 when another operation or a recovery holds it, 1 on failure. |
| `restore-test` | Restores the newest complete pair into the scratch database `fold_backup_scratch`, validates the tar and prints `restore-test tables=.. users=.. uploadFiles=.. referencedFiles=.. referencedMissing=.. orphanAvatarOwners=..`. Drops the scratch database before reporting success; if the drop fails the command exits 1 with a value-free message and prints no result line (exit cleanup still retries the drop). |
| `list` | Recognized top-level archives: name, bytes, mtime, whether the pair is complete. Never reads `adopted/`. |
| `health` | Exit 0 while the loop is alive (heartbeat younger than 120 seconds, process present). |
| `uploads-*` | Helpers `restore.sh` drives (below). |
| `--self-test`, `--help` | Tool and rule checks; usage. |

Maintenance commands never start a backup.

## Settings

| Variable | Meaning |
|---|---|
| `PGPASSWORD` | Database password. `PGHOST=db`, `PGUSER=church`, `PGDATABASE=fold` are the defaults. |
| `FOLD_BACKUP_TZ` | IANA zone for the 02:30 slot, file names and the day-01 rule. Default `UTC`; `Local` is refused. |
| `FOLD_BACKUP_KEEP_DAYS` | Local retention, 1 to 365, default 14. |
| `FOLD_BACKUP_S3_BUCKET`, `FOLD_BACKUP_S3_ENDPOINT` (https), `FOLD_BACKUP_S3_REGION`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `FOLD_BACKUP_AGE_RECIPIENT` (`age1...`) | Offsite copies. All six or none; a partial set fails the run at the `config` step before any dump. The private age key never goes into the container. |
| `FOLD_RECOVERY_DIR` | Host coordination directory (below). Empty means uncoordinated. |

Secrets are read from the environment only. They never appear in argv, logs or status files.
Test seams (`FOLD_BACKUP_ARCHIVE_DIR`, `FOLD_BACKUP_STATUS_DIR`, `FOLD_BACKUP_DATA_DIR`,
`FOLD_BACKUP_NOW_EPOCH`, `FOLD_BACKUP_TICK_SECS`, ...) exist for `backup-test.sh`; do not set them
in a deployment.

## What a run does

1. Admission (below). A refusal is a skipped run: exit 75, no archive, expiry, upload or status change.
2. `config`: the settings above. `uploads`: `tar` of only `/data/uploads` to a temporary file. `dump`:
   `pg_dump -Fc --no-owner --no-acl` to a temporary file, then `pg_restore --list` proves it reads.
   Uploads first, dump second: ids are never hard-deleted, so every avatar folder in the tar belongs to a
   `users` row in the later dump; a photo uploaded between the two can be missing from the tar.
3. The pair is published under one stamp, `fold-YYYYMMDD-HHMM.dump` and `uploads-YYYYMMDD-HHMM.tgz`, mode
   600, by no-overwrite hard links. A minute collision with a local archive moves the stamp on; nothing is
   overwritten. The stamp is picked from local files only; the bucket is never read.
4. `expire`: local retention (below).
5. With offsite configured, `encrypt` writes `<name>.age` for each archive with age (the header is checked)
   and `upload` makes a run id (`od` reads 16 random bytes: 32 lowercase hex characters; any other answer fails
   the run at `upload` before anything is sent) and copies only those ciphertexts with `rclone copyto` to
   `fold/<stamp>-<run id>/<name>.age`. The run logs `offsite: uploading to fold/<stamp>-<run id>/` (never the bucket
   name). `rclone` runs with `--no-check-dest --s3-no-head --s3-no-check-bucket`, so the run never lists, reads,
   heads or deletes: the key needs permission to write objects under `fold/` (S3 `PutObject` and, where separate,
   `AbortMultipartUpload`; B2 `writeFiles`) and nothing else. A pair is the two files under one folder. A failure
   after the first upload is not rolled back: the run fails, `LAST_OK` stays, and the one object that did arrive
   stays in its folder, which nothing completes; the next run takes a new folder. A lost answer can leave a
   complete folder from a failed run. Folder ownership is probabilistic (128 random bits, no provider-enforced
   no-replace), an atomic appearance of a finished upload is assumed from the provider, and `age` authenticates each
   file, not the pair. Which folder to restore after two producers shared a bucket is in
   `docs/DEPLOY-DIGITALOCEAN.md` (Offsite).
6. `status.json`, then `LAST_OK`, by temporary file and rename.

## Schedule

One run after the latest local 02:30 slot. A first install and downtime catch up at once. A failed run is
retried at most hourly. Spring forward (02:30 does not exist) uses 03:30; a repeated 02:30 uses the first
occurrence, so a day has one slot. A run refused because of a recovery or contention changes nothing, and the
loop looks again on its next tick (60 seconds), so a backup starts soon after `recovery.lock` goes away.

## Status files (`/backup-status`, mode 600, owner 65532)

- `LAST_OK`: one UTC RFC 3339 line, written after everything (including both uploads) succeeded.
- `status.json`, format 1: `{"format":1,"lastOk":{"finishedAt","dumpBytes","uploadsBytes","uploadsFiles","offsite":"off|uploaded"}|null,"lastFailure":{"at","step"}|null}`.
  Steps are `config`, `uploads`, `dump`, `expire`, `encrypt`, `upload`. No paths, bucket names, hosts,
  credentials or raw errors.

An admitted run that fails keeps `LAST_OK` and `lastOk` and records `lastFailure`; the next success clears it.

## Local retention

The host script's rule, applied to the top level of `/backups` only: daily files go after `FOLD_BACKUP_KEEP_DAYS` (age strictly greater), files
named for the 1st of a month after 180 days, by mtime, for recognized names only (`.dump`, `.tgz` and their
`.age` siblings). Anything else, including partial files, symlinks and the lock, is never touched. These are
local rules only. Bucket lifecycle is the operator's job (a rule on `fold/` that expires objects, old versions and
incomplete multipart uploads also covers half pairs); this image never deletes offsite or claims remote expiry.

## Host coordination (`FOLD_RECOVERY_DIR`)

With the variable set, every `now` and `restore-test` first opens `$FOLD_RECOVERY_DIR/backup.lock`
**read-only** and takes an exclusive `flock` on that inode (the container needs only read access to the lock and
directory traversal; never access to its contents or to host secrets), then refuses while
`$FOLD_RECOVERY_DIR/recovery.lock` exists, then takes `/backups/.lock`. Order is always host lock, then archive
lock. Both stay held through tar, dump, expiry, encryption, upload and status publication. A configured but
missing or unreadable directory refuses. A database named `fold_restore_*` or `fold_pre_restore_*`, or a failed
look at the database list, also refuses. Without the variable the container runs uncoordinated and
`restore.sh` must refuse to admit a recovery against it.

## Uploads helpers

Driven only by `restore.sh`, as one-shot containers with `--user 0 --network none --restart no --rm`, labelled
`fold.project`, `fold.recovery.nonce` and `fold.uploads.role` (never `com.docker.compose.*`). Every one takes
`--nonce` with the full 32 lowercase hex nonce; anything else exits 64. The archive is mounted read-only at
`/restore/archive.tgz` and the uploads volume at `/data`. Root-owned mode-600 host archives stay untouched.
Output is one `key=value` line on stdout; refusals exit 1 and change nothing unless they say otherwise.

| Command | Result |
|---|---|
| `uploads-validate` | `validated members=N uncompressedBytes=B freeBytes=F`. Needs `/data` read-only only. Accepts gzip tar members `./`, `uploads/` and `./uploads/` subtrees of plain files and folders with names of `[A-Za-z0-9._-]`; refuses absolute names, `..`, links, devices, FIFOs, an unreadable gzip, a symlinked destination ancestor, and free space below the uncompressed total. Creates nothing in `/data`. |
| `uploads-stage` | Extracts into `/data/.restore-NONCE/uploads` and prints `staged inode=I files=N`. |
| `uploads-observe` | `observe live=I\|none staged=I\|none complete=yes\|no exchange=none\|intent\|done drop=none\|intent\|done receipt=present\|absent`. Read-only. `complete=yes` needs a completed receipt and folders on disk that carry the recorded identities; a replaced tree reports `no`. |
| `uploads-exchange` | Swaps `/data/uploads` and the staged tree with `mv --exchange` (renameat2, no copying fallback) and prints `exchanged live=I old=J`. `--expect-live I --expect-staged J` refuses unless the identities are that pair or its swap. `--probe` creates `/data/.restore-probe-NONCE/{a,b}`, exchanges them, removes them and any other empty `.restore-probe-*` tree, never touches `/data/uploads`, and prints `probe ok`. |
| `uploads-drop` | `--identity I` removes the tree at `/data/.restore-NONCE/uploads` (the staged tree before the exchange, the old live tree after it) and prints `dropped identity=I`. `I` must equal the identity the receipt records for that tree (staged inode before the exchange, old live inode after it), and the tree on disk must match it; anything else is refused. `--release` deletes the receipt and the staging folder (`released`), and refuses while a receipt records a tree whose removal it never confirmed (a tree that vanished without a drop is not a cleaned one; the receipt stays). A staging folder that is a symlink is refused before the receipt is read, for a drop and for a release. |

Identity is the directory's inode number, read from `observe`.

**Receipt.** `/data/.restore-NONCE/receipt` (parent mode 700, outside the archived `uploads/` subtree) holds the
nonce, the archive SHA-256, the live and staged identities, `stageState` (`pending`, `extracting`,
`complete`), `exchange` (`none`, `intent`, `done`) and the drop intent and completion. It is replaced atomically
and synchronized. A stage is `complete` only after extraction, normalization (owner 65532, modes 755/644) and
filesystem synchronization; an existing inode alone proves nothing. Retries: a completed matching stage is
recognized; an incomplete one is cleared (only the recorded inode) and rebuilt; an unrelated, symlinked or
mismatched target refuses. A stage killed between creating the staged folder and recording its inode leaves a pending
receipt beside an empty, unnamed folder (or, earlier, only the unpublished `.receipt.new`): a retry rebuilds it, and
`uploads-drop --identity I` removes exactly that empty folder when `I` is its inode, and refuses one that holds anything.
Exchange records intent first and accepts either the unchanged pair or the swapped
pair, so a lost acknowledgement resolves on retry; on a filesystem without `RENAME_EXCHANGE` it changes
neither tree. Drop records the identity as intent before unlinking, accepts an absent tree only under that
intent, and removes the receipt only on `--release`, which `restore.sh` calls after its own lock durably
records completion. Before observing or changing a tree after any interruption `restore.sh` must remove every
helper container carrying its nonce.

## The host's copies (`adopted/`)

The image does not copy the older host archives; `provision.sh` does, as root, during the cutover
(`docs/DEPLOY-DIGITALOCEAN.md`, "Backup cutover"). It keeps the copies in `adopted/` at the root of the
`backups` volume: a `root:root` mode-0700 folder of root-owned mode-0600 files, with the originals' bytes
and exact mtimes. This service runs as 65532 with no capability, so it cannot list, read or change them,
and the sticky bit on the volume root stops it removing or renaming the folder. The host expires the copies
on the same dates as their originals (`backup.sh --expire-only`), and `backup.sh --list-adopted` prints
their explicit paths for `restore.sh --dump` and `--uploads`. `list`, retention and `restore-test` here
look at top-level files only and never at `adopted/`.

## Tests

`make backup-test` runs `backup-test.sh` (stubs for `pg_dump`, `pg_restore`, `psql`, `age` and `rclone`; GNU
`date` and `touch`; no Docker, no database) and then `backup-test.sh --probes`, which mutates `backup.sh` four
ways (retention, plaintext upload, no recovery refusal, dump before tar) and requires each mutation to fail
its named case. The host's copies, expiry and cutover are proven on a real droplet (DEC-154).
`make lint-sh` covers these scripts. `docker run --rm IMAGE --self-test` checks the tools in a built image.
