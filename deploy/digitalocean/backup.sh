#!/usr/bin/env bash
# Nightly backup of Fold's Compose Postgres and the uploads volume, and the
# expiry of the host's archives.
#
# Writes mode-600 files under /var/backups/fold. When /opt/fold/backup.env
# is complete, encrypts them with age and uploads the ciphertext.
# Backblaze B2 is the recommendation. Any S3-compatible bucket works,
# including DigitalOcean Spaces.
#
# Local archives are immutable: a name that exists is never overwritten or truncated.
# Each run that uploads puts its two members under a folder of its own in the
# bucket (fold/<stamp>-<run id>/), and never reads, lists or deletes anything
# there, so the key it holds needs permission to write objects and nothing more.
# Never creates a bucket, a key, or any other paid resource.
# Never uploads a plaintext dump. The age private key stays off this machine.
#
# --dry-run prints the plan and does not dump, encrypt, upload, or delete.
set -euo pipefail
TOOL=backup.sh
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
[[ -f "$SCRIPT_DIR/checks.sh" ]] || {
  echo "backup.sh: missing checks.sh; run the current provision.sh to install the complete tool set" >&2
  exit 1
}
# shellcheck source=deploy/digitalocean/checks.sh
source "$SCRIPT_DIR/checks.sh"

DRY_RUN=0
RESTORE_TEST=0
EXPIRE_ONLY=0
LIST_ADOPTED=0
DEPLOY_DIR="/opt/fold"
BACKUP_DIR="/var/backups/fold"
ENV_FILE=""

FOLD_BACKUP_S3_BUCKET=""
FOLD_BACKUP_S3_ENDPOINT=""
FOLD_BACKUP_S3_REGION=""
AWS_ACCESS_KEY_ID=""
AWS_SECRET_ACCESS_KEY=""
FOLD_BACKUP_AGE_RECIPIENT=""
FOLD_BACKUP_KEEP_DAYS="14"

# Matches docker-compose.yml (POSTGRES_USER / POSTGRES_DB).
PG_USER="church"
PG_DB="fold"

usage() {
  cat <<'EOF'
Usage: /opt/fold/backup.sh [--dry-run] [--restore-test | --expire-only | --list-adopted]
       [--deploy-dir /opt/fold] [--backup-dir /var/backups/fold]
       [--env-file /opt/fold/backup.env]

Dumps the compose Postgres database and the uploads volume. Keeps daily
dumps for FOLD_BACKUP_KEEP_DAYS (default 14) and dumps taken on the 1st
of the month for 180 days. An archive name that already exists is never
overwritten: a second run within the same minute refuses (exit 75) and can be
retried a minute later. The offsite copy goes to fold/<stamp>-<run id>/, a folder
this run named with 128 random bits; the script reads nothing back from the
bucket, so a failed upload (exit 1) keeps the local pair and a later run starts
a new folder.

--restore-test loads the newest dump into a throwaway database named
fold_restore_test, counts public tables, and drops that database.

--expire-only makes no backup, no upload and no LAST_OK. It expires the host's
original archives and, under the same recorded decisions, their copies in the
backups volume's adopted/ folder, then (when Compose has no backup service) the
service's own top-level archives under .env's retention. It exits 4 when the
originals are expired but a copy is still pending, and is what the cron runs
after the backup service has taken over.

--list-adopted prints each adopted stamp as verified, incomplete or conflict,
with the explicit paths to pass to restore.sh --dump and --uploads.

Backup and expiry refuse with exit 75 while recovery.lock exists. They use a
separate backup.lock, never ops.lock, so a backup already in progress finishes
before restore publishes recovery.lock. Restore waits at most 120 seconds in
preflight, with the public site untouched. Use the installed current tools;
older scripts run by hand do not participate in this coordination.
If another operation holds backup.lock, a backup exits 75 without a backup;
rerun backup.sh later. A lock collision does not schedule an automatic retry.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --help|-h)
      usage
      exit 0
      ;;
    --dry-run)
      DRY_RUN=1
      shift
      ;;
    --restore-test)
      RESTORE_TEST=1
      shift
      ;;
    --expire-only)
      EXPIRE_ONLY=1
      shift
      ;;
    --list-adopted)
      LIST_ADOPTED=1
      shift
      ;;
    --deploy-dir|--deploy-dir=*) take_value DEPLOY_DIR "$@"; shift "$OPT_SHIFT" ;;
    --backup-dir|--backup-dir=*) take_value BACKUP_DIR "$@"; shift "$OPT_SHIFT" ;;
    --env-file|--env-file=*) take_value ENV_FILE "$@"; shift "$OPT_SHIFT" ;;
    *)
      die "unknown option: $1"
      ;;
  esac
done

(( RESTORE_TEST + EXPIRE_ONLY + LIST_ADOPTED <= 1 )) || die "pass at most one of --restore-test, --expire-only and --list-adopted"
[[ "$DEPLOY_DIR" == /* && "$BACKUP_DIR" == /* ]] || die "directories must be absolute paths"
if [[ -z "$ENV_FILE" ]]; then
  ENV_FILE="$DEPLOY_DIR/backup.env"
fi

# Refuse before reading offsite credentials, selecting a dump, making a backup
# directory, expiring anything or invoking Docker. A second check under fd 6
# closes the race with restore publishing its durable recovery interval.
backup_recovery_guard() {
  if [[ -e "$DEPLOY_DIR/recovery.lock" || -L "$DEPLOY_DIR/recovery.lock" ]]; then
    if (( DRY_RUN )); then
      echo "dry-run: recovery active; no backup, restore test, expiry or offsite upload is permitted. Use $DEPLOY_DIR/restore.sh --status"
      exit 0
    fi
    die "recovery active; no backup, expiry or offsite upload was attempted. Use $DEPLOY_DIR/restore.sh --status" 75
  fi
}

# A listing only reads, so it needs the lock for a stable view but not an idle
# recovery; every other mode refuses during one.
if (( ! LIST_ADOPTED )); then
  backup_recovery_guard
fi
if (( ! DRY_RUN )); then
  declare -F backup_coord_acquire >/dev/null || die "checks.sh lacks backup coordination; run the current provision.sh to install the complete tool set"
  declare -F archive_expire_run >/dev/null || die "checks.sh lacks the archive section; run the current provision.sh to install the complete tool set"
  # Expiry and a listing wait for a running adoption or backup to finish; a
  # nightly backup does not queue behind another one.
  lock_wait=0
  if (( EXPIRE_ONLY || LIST_ADOPTED )); then lock_wait=120; fi
  backup_coord_acquire "$DEPLOY_DIR" "$lock_wait" backup || exit $?
  if (( ! LIST_ADOPTED )); then
    backup_recovery_guard
  fi
else
  echo "dry-run: acquire backup.lock and recheck recovery.lock before any backup or expiry"
fi

# Retention is established first and alone: a full run whose offsite settings are
# incomplete still expires under a valid KEEP_DAYS, and reports the incomplete
# settings as its production failure (offsite_bad).
offsite_ready=0
offsite_bad=0
if [[ -f "$ENV_FILE" ]]; then
  backup_env_parse "$ENV_FILE" || exit 1
  backup_env_validate keep || exit 1
  if (( ! EXPIRE_ONLY && ! LIST_ADOPTED )); then
    backup_env_validate || offsite_bad=1
  fi
  FOLD_BACKUP_S3_BUCKET="${BENV[FOLD_BACKUP_S3_BUCKET]:-}"
  FOLD_BACKUP_S3_ENDPOINT="${BENV[FOLD_BACKUP_S3_ENDPOINT]:-}"
  FOLD_BACKUP_S3_REGION="${BENV[FOLD_BACKUP_S3_REGION]:-}"
  AWS_ACCESS_KEY_ID="${BENV[AWS_ACCESS_KEY_ID]:-}"
  AWS_SECRET_ACCESS_KEY="${BENV[AWS_SECRET_ACCESS_KEY]:-}"
  FOLD_BACKUP_AGE_RECIPIENT="${BENV[FOLD_BACKUP_AGE_RECIPIENT]:-}"
  FOLD_BACKUP_KEEP_DAYS="${BENV[FOLD_BACKUP_KEEP_DAYS]:-14}"
  if (( ! offsite_bad )) && [[ -n "$FOLD_BACKUP_S3_BUCKET" ]]; then offsite_ready=1; fi
fi
FOLD_BACKUP_KEEP_DAYS=$((10#$FOLD_BACKUP_KEEP_DAYS))

compose() {
  compose_in "$DEPLOY_DIR" "$@"
}

# Unfinished temporary files this run made; removed on every exit but SIGKILL.
# The next run, under the same lock, clears what a SIGKILL left.
TMP_FILES=()
# shellcheck disable=SC2329 # Invoked by the EXIT trap.
host_cleanup() {
  local f
  for f in ${TMP_FILES[@]+"${TMP_FILES[@]}"}; do
    rm -f -- "$f" 2>/dev/null || true
  done
}
trap host_cleanup EXIT

ensure_backup_dir() {
  if (( DRY_RUN )); then
    echo "dry-run: mkdir -p $BACKUP_DIR && chmod 700 $BACKUP_DIR"
    return 0
  fi
  mkdir -p "$BACKUP_DIR" && chmod 700 "$BACKUP_DIR"
}

project_name() {
  local name="fold"
  local file="$DEPLOY_DIR/.env"
  if [[ -f "$file" ]]; then
    local line key val
    while IFS= read -r line || [[ -n "$line" ]]; do
      case "$line" in
        ''|'#'*) continue ;;
      esac
      key="${line%%=*}"
      val="${line#*=}"
      if [[ "$val" == \"*\" && "$val" == *\" ]]; then
        val="${val:1:${#val}-2}"
      fi
      if [[ "$key" == "COMPOSE_PROJECT_NAME" && "$val" =~ ^[a-zA-Z0-9_-]+$ ]]; then
        name="$val"
      fi
    done <"$file"
  fi
  printf '%s' "$name"
}

# Every file below is written to a private temporary name and published by a
# no-replace link, so an existing archive is never truncated or replaced.
dump_database() {
  local dest="$1"
  if (( DRY_RUN )); then
    echo "dry-run: docker compose --project-directory $DEPLOY_DIR exec -T db pg_dump -U $PG_USER -Fc --no-owner --no-acl $PG_DB > $dest   # a private temporary file, then a no-replace link"
    echo "dry-run: chmod 600 $dest"
    return 0
  fi
  TMP_FILES+=("$dest")
  compose exec -T db pg_dump -U "$PG_USER" -Fc --no-owner --no-acl "$PG_DB" >"$dest" || return 1
  chmod 600 "$dest" || return 1
  sync -- "$dest" || return 1
}

dump_uploads() {
  local dest="$1"
  local volume="$2"
  if (( DRY_RUN )); then
    echo "dry-run: docker run --rm -v ${volume}:/data:ro -v ${BACKUP_DIR}:/backup alpine tar czf /backup/$(basename "$dest") -C /data .   # a private temporary file, then a no-replace link"
    echo "dry-run: chmod 600 $dest"
    return 0
  fi
  TMP_FILES+=("$dest")
  docker run --rm -v "${volume}:/data:ro" -v "${BACKUP_DIR}:/backup" alpine \
    tar czf "/backup/$(basename "$dest")" -C /data . || return 1
  chmod 600 "$dest" || return 1
  sync -- "$dest" || return 1
}

# publish_new TMP FINAL: a no-replace link. Fails when FINAL exists.
publish_new() {
  ln -- "$1" "$2" 2>/dev/null || return 1
  rm -f -- "$1"
}

stamp_occupied() {
  local f
  for f in "$BACKUP_DIR/fold-$1.dump" "$BACKUP_DIR/fold-$1.dump.age" "$BACKUP_DIR/uploads-$1.tgz" "$BACKUP_DIR/uploads-$1.tgz.age"; do
    if [[ -e "$f" || -L "$f" ]]; then return 0; fi
  done
  return 1
}

# uploads_volume_state VOLUME: 0 the volume exists, 1 it is absent, 2 it could
# not be observed. Absence is only a successful listing that lacks the name; a
# failed inspect with a listed name, or a failed listing, is unknown.
uploads_volume_state() {
  local name="$1" listing line
  if docker volume inspect "$name" >/dev/null 2>&1; then return 0; fi
  listing="$(docker volume ls -q 2>/dev/null)" || return 2
  while IFS= read -r line; do
    if [[ "$line" == "$name" ]]; then return 2; fi
  done <<<"$listing"
  return 1
}

# new_run_id: RUN_ID, 128 random bits as 32 lowercase hex characters. No
# pipeline and no reliance on errexit, because produce runs under `||`.
new_run_id() {
  local raw
  raw="$(od -An -N16 -tx1 /dev/urandom 2>/dev/null)" || return 1
  raw="${raw//[[:space:]]/}"
  [[ "$raw" =~ ^[0-9a-f]{32}$ ]] || return 1
  RUN_ID="$raw"
}

encrypt_and_upload() {
  local src="$1"
  local base cipher tmp
  base="$(basename "$src")"
  cipher="${src}.age"
  if (( DRY_RUN )); then
    echo "dry-run: age -r <recipient> -o $cipher $src   # a private temporary file, then a no-replace link"
    echo "dry-run: docker run --rm -e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY -e AWS_DEFAULT_REGION -v $BACKUP_DIR:/backups:ro amazon/aws-cli s3 cp /backups/${base}.age s3://${FOLD_BACKUP_S3_BUCKET}/${REMOTE_PREFIX}${base}.age --endpoint-url ${FOLD_BACKUP_S3_ENDPOINT}"
    return 0
  fi
  if ! command -v age >/dev/null 2>&1; then
    echo "backup.sh: age is not installed; refusing to upload an unencrypted backup" >&2
    return 1
  fi
  tmp="$BACKUP_DIR/.tmp.${base}.age.$$"
  TMP_FILES+=("$tmp")
  age -r "$FOLD_BACKUP_AGE_RECIPIENT" -o "$tmp" "$src" || return 1
  chmod 600 "$tmp" || return 1
  sync -- "$tmp" || return 1
  publish_new "$tmp" "$cipher" || { echo "backup.sh: ${base}.age already exists; it was not replaced" >&2; return 1; }
  sync -- "$BACKUP_DIR" || return 1
  AWS_ACCESS_KEY_ID="$AWS_ACCESS_KEY_ID" \
    AWS_SECRET_ACCESS_KEY="$AWS_SECRET_ACCESS_KEY" \
    AWS_DEFAULT_REGION="$FOLD_BACKUP_S3_REGION" \
    docker run --rm \
      -e AWS_ACCESS_KEY_ID \
      -e AWS_SECRET_ACCESS_KEY \
      -e AWS_DEFAULT_REGION \
      -v "${BACKUP_DIR}:/backups:ro" \
      amazon/aws-cli \
      s3 cp "/backups/${base}.age" "s3://${FOLD_BACKUP_S3_BUCKET}/${REMOTE_PREFIX}${base}.age" \
      --endpoint-url "$FOLD_BACKUP_S3_ENDPOINT" || {
        echo "backup.sh: the upload of ${base}.age failed and the local pair is kept; the key needs permission to write objects under fold/ (listing and reading are not needed)" >&2
        return 1
      }
  echo "uploaded s3://${FOLD_BACKUP_S3_BUCKET}/${REMOTE_PREFIX}${base}.age"
}

mark_ok() {
  if (( DRY_RUN )); then
    echo "dry-run: write $BACKUP_DIR/LAST_OK"
    return 0
  fi
  date -u +%Y-%m-%dT%H:%M:%SZ >"$BACKUP_DIR/LAST_OK" || return 1
  chmod 600 "$BACKUP_DIR/LAST_OK"
}

restore_test() {
  if (( DRY_RUN )); then
    echo "dry-run: pg_restore the newest fold-*.dump into database fold_restore_test, count public tables, drop that database"
    return 0
  fi
  local latest
  latest="$(find "$BACKUP_DIR" -name 'fold-*.dump' -printf '%T@ %p\n' | sort -n | tail -1 | cut -d' ' -f2-)"
  [[ -n "$latest" ]] || die "no dump in $BACKUP_DIR"
  echo "restore test from $latest"
  compose exec -T db psql -U "$PG_USER" -d postgres -v ON_ERROR_STOP=1 \
    -c "DROP DATABASE IF EXISTS fold_restore_test WITH (FORCE)" \
    -c "CREATE DATABASE fold_restore_test"
  compose exec -T db pg_restore -U "$PG_USER" -d fold_restore_test --no-owner --no-acl <"$latest"
  compose exec -T db psql -U "$PG_USER" -d fold_restore_test -v ON_ERROR_STOP=1 \
    -c "SELECT count(*) AS public_tables FROM information_schema.tables WHERE table_schema = 'public'"
  compose exec -T db psql -U "$PG_USER" -d postgres -v ON_ERROR_STOP=1 \
    -c "DROP DATABASE fold_restore_test WITH (FORCE)"
  echo "restore test passed and fold_restore_test was dropped"
}

# produce: one pair under a fresh stamp, then the offsite copies and LAST_OK.
# Returns 0, 75 when the stamp is taken, or 1 (also for incomplete offsite
# settings). Never calls die, so the expiry that follows always runs.
produce() {
  local stamp dump uploads volume tmp_dump tmp_up svc_rc defined uploads_rc
  if (( offsite_bad )); then
    echo "backup.sh: the offsite settings are incomplete, so no backup was made; the archives past their retention are still expired" >&2
    return 1
  fi
  # One producer per deployment, so a night does not get two pairs. The cutover
  # stops the service before it calls this script the producer; this refuses while
  # a service runs for any other reason (a recovery, a start by hand). The bucket
  # is not what makes this safe: every run uploads into a folder of its own.
  if (( DRY_RUN )); then
    echo "dry-run: refuse (exit 75) while the container backup service is running, so only one producer makes the nightly backups"
  else
    # svc_rc: 0 the service runs, 1 it does not (or none is defined), 2 unknown.
    defined=0
    backup_service_defined "$DEPLOY_DIR" || defined=$?
    case "$defined" in
      0) svc_rc=0; backup_service_running "$DEPLOY_DIR" || svc_rc=$? ;;
      1) svc_rc=1 ;;
      *) svc_rc=2 ;;
    esac
    case "$svc_rc" in
      0) echo "backup.sh: the container backup service is running and makes the backups; none was made here. Stop it (docker compose stop backup) to have this script back up" >&2
         return 75 ;;
      1) ;;
      *) echo "backup.sh: could not tell whether the container backup service is running, so no backup was made; check Docker and retry" >&2
         return 1 ;;
    esac
  fi
  stamp="$(date +%Y%m%d-%H%M)"
  dump="$BACKUP_DIR/fold-${stamp}.dump"
  uploads="$BACKUP_DIR/uploads-${stamp}.tgz"
  volume="$(project_name)_uploads"
  ensure_backup_dir || return 1
  if (( ! DRY_RUN )); then
    if stamp_occupied "$stamp"; then
      echo "backup.sh: an archive named for ${stamp} already exists; nothing was written. Retry in a minute" >&2
      return 75
    fi
    archive_clear_residue "$BACKUP_DIR" || return 1
    # Fold's Compose stack declares this volume, so a missing one is an anomaly,
    # not a backup without photos. Nothing has been dumped yet.
    uploads_rc=0
    uploads_volume_state "$volume" || uploads_rc=$?
    case "$uploads_rc" in
      0) ;;
      1) echo "backup.sh: the uploads volume ${volume} does not exist, so no backup was made; check the Compose project name and that the stack has been started" >&2
         return 1 ;;
      *) echo "backup.sh: could not tell whether the uploads volume ${volume} exists (Docker did not answer), so no backup was made; check Docker and retry" >&2
         return 1 ;;
    esac
  else
    echo "dry-run: docker volume inspect ${volume}; refuse, with no backup, when the volume is absent or Docker does not answer"
  fi
  tmp_dump="$BACKUP_DIR/.tmp.fold-${stamp}.dump.$$"
  tmp_up="$BACKUP_DIR/.tmp.uploads-${stamp}.tgz.$$"
  dump_database "$tmp_dump" || return 1
  dump_uploads "$tmp_up" "$volume" || return 1
  if (( ! DRY_RUN )); then
    publish_new "$tmp_dump" "$dump" || { echo "backup.sh: ${dump##*/} appeared meanwhile; it was not replaced" >&2; return 1; }
    echo "wrote $dump"
    publish_new "$tmp_up" "$uploads" || {
      rm -f -- "$dump"
      echo "backup.sh: ${uploads##*/} appeared meanwhile; it was not replaced" >&2
      return 1
    }
    echo "wrote $uploads"
    sync -- "$BACKUP_DIR" || return 1
  fi
  if (( offsite_ready )); then
    # One folder per run: a pair is the two members under one folder, and no other
    # run, on this host or any other, ever writes to it.
    if (( DRY_RUN )); then
      RUN_ID="<run id: 32 random hex characters>"
    elif ! new_run_id; then
      echo "backup.sh: no random folder name could be made; nothing was uploaded and the local pair is kept" >&2
      return 1
    fi
    REMOTE_PREFIX="fold/${stamp}-${RUN_ID}/"
    echo "offsite: s3://${FOLD_BACKUP_S3_BUCKET}/${REMOTE_PREFIX} via ${FOLD_BACKUP_S3_ENDPOINT}"
    encrypt_and_upload "$dump" || return 1
    encrypt_and_upload "$uploads" || return 1
  else
    echo "offsite not configured (no complete $ENV_FILE); dumps stay in $BACKUP_DIR"
  fi
  mark_ok || return 1
}

# expire: the host originals, their adopted copies under the same recorded
# decisions, and (no backup service) the service's own archives. Exit 4 means
# the originals are done but a copy is still pending.
expire() {
  if (( DRY_RUN )); then
    echo "dry-run: record the expiry decision (keep ${FOLD_BACKUP_KEEP_DAYS} days) in $DEPLOY_DIR/$ARCHIVE_PENDING, expire files in $BACKUP_DIR older than ${FOLD_BACKUP_KEEP_DAYS} days keeping day-01 files for 180 days"
    echo "dry-run: expire the adopted copies under the same decisions, and the service archives under .env retention when Compose has no backup service"
    return 0
  fi
  archive_expire_run "$DEPLOY_DIR" "$BACKUP_DIR" "$FOLD_BACKUP_KEEP_DAYS"
}

if (( RESTORE_TEST )); then
  restore_test
  exit 0
fi

if (( LIST_ADOPTED )); then
  if (( DRY_RUN )); then
    echo "dry-run: list the adopted copies in the backups volume with explicit paths"
    exit 0
  fi
  archive_deadline "${FOLD_BACKUP_DOCKER_WAIT:-60}"
  rc=0
  archive_volume_resolve "$DEPLOY_DIR" || rc=$?
  case "$rc" in
    0) archive_list_adopted "$BACKUP_DIR" ;;
    3) echo "adopted: none (no backups volume)" ;;
    *) die "the backups volume could not be resolved (status $rc); nothing was listed" ;;
  esac
  exit 0
fi

if (( EXPIRE_ONLY )); then
  rc=0
  expire || rc=$?
  exit "$rc"
fi

# A full run holds the lock throughout and expires even when the backup or the
# offsite copy failed; a production failure is still the run's result.
produce_rc=0
expire_rc=0
produce || produce_rc=$?
expire || expire_rc=$?
(( produce_rc == 0 )) || exit "$produce_rc"
exit "$expire_rc"
