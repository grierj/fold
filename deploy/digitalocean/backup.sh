#!/usr/bin/env bash
# Nightly backup of Fold's Compose Postgres and the uploads volume.
#
# Writes mode-600 files under /var/backups/fold. When /opt/fold/backup.env
# is complete, encrypts them with age and uploads the ciphertext to an
# S3-compatible bucket (DigitalOcean Spaces, Backblaze B2, or similar).
#
# Never creates a bucket, a Spaces key, or any other paid resource.
# Never uploads a plaintext dump. The age private key stays off this machine.
#
# --dry-run prints the plan and does not dump, encrypt, upload, or delete.
set -euo pipefail

DRY_RUN=0
RESTORE_TEST=0
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

die() {
  printf 'backup.sh: %s\n' "$1" >&2
  exit "${2:-1}"
}

usage() {
  cat <<'EOF'
Usage: /opt/fold/backup.sh [--dry-run] [--restore-test]
       [--deploy-dir /opt/fold] [--backup-dir /var/backups/fold]
       [--env-file /opt/fold/backup.env]

Dumps the compose Postgres database and the uploads volume. Keeps daily
dumps for FOLD_BACKUP_KEEP_DAYS (default 14) and dumps taken on the 1st
of the month for 180 days.

--restore-test loads the newest dump into a throwaway database named
fold_restore_test, counts public tables, and drops that database.
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
    --deploy-dir)
      [[ $# -ge 2 && -n "${2:-}" ]] || die "--deploy-dir needs a value"
      DEPLOY_DIR="$2"
      shift 2
      ;;
    --deploy-dir=*)
      DEPLOY_DIR="${1#*=}"
      shift
      ;;
    --backup-dir)
      [[ $# -ge 2 && -n "${2:-}" ]] || die "--backup-dir needs a value"
      BACKUP_DIR="$2"
      shift 2
      ;;
    --backup-dir=*)
      BACKUP_DIR="${1#*=}"
      shift
      ;;
    --env-file)
      [[ $# -ge 2 && -n "${2:-}" ]] || die "--env-file needs a value"
      ENV_FILE="$2"
      shift 2
      ;;
    --env-file=*)
      ENV_FILE="${1#*=}"
      shift
      ;;
    *)
      die "unknown option: $1"
      ;;
  esac
done

[[ "$DEPLOY_DIR" == /* && "$BACKUP_DIR" == /* ]] || die "directories must be absolute paths"
if [[ -z "$ENV_FILE" ]]; then
  ENV_FILE="$DEPLOY_DIR/backup.env"
fi

load_env() {
  local file="$1"
  [[ -f "$file" ]] || return 1
  local line key val
  while IFS= read -r line || [[ -n "$line" ]]; do
    line="${line%$'\r'}"
    line="${line#"${line%%[![:space:]]*}"}"
    case "$line" in
      ''|'#'*) continue ;;
    esac
    [[ "$line" == *=* ]] || die "bad line in $file (expected KEY=value)"
    key="${line%%=*}"
    val="${line#*=}"
    if [[ "$val" == \"*\" && "$val" == *\" ]]; then
      val="${val:1:${#val}-2}"
    fi
    case "$val" in
      *$'\n'*|*[[:space:]]*) die "$key in $file contains whitespace" ;;
    esac
    case "$key" in
      FOLD_BACKUP_S3_BUCKET) FOLD_BACKUP_S3_BUCKET="$val" ;;
      FOLD_BACKUP_S3_ENDPOINT) FOLD_BACKUP_S3_ENDPOINT="$val" ;;
      FOLD_BACKUP_S3_REGION) FOLD_BACKUP_S3_REGION="$val" ;;
      AWS_ACCESS_KEY_ID) AWS_ACCESS_KEY_ID="$val" ;;
      AWS_SECRET_ACCESS_KEY) AWS_SECRET_ACCESS_KEY="$val" ;;
      FOLD_BACKUP_AGE_RECIPIENT) FOLD_BACKUP_AGE_RECIPIENT="$val" ;;
      FOLD_BACKUP_KEEP_DAYS) FOLD_BACKUP_KEEP_DAYS="$val" ;;
      *) die "unknown key $key in $file" ;;
    esac
  done <"$file"
  return 0
}

offsite_ready=0
if [[ -f "$ENV_FILE" ]]; then
  load_env "$ENV_FILE"
fi

[[ "$FOLD_BACKUP_KEEP_DAYS" =~ ^[0-9]+$ ]] || die "FOLD_BACKUP_KEEP_DAYS must be a number"
[[ "$FOLD_BACKUP_KEEP_DAYS" -ge 1 && "$FOLD_BACKUP_KEEP_DAYS" -le 365 ]] \
  || die "FOLD_BACKUP_KEEP_DAYS must be between 1 and 365"

any_offsite="${FOLD_BACKUP_S3_BUCKET}${FOLD_BACKUP_S3_ENDPOINT}${FOLD_BACKUP_S3_REGION}${AWS_ACCESS_KEY_ID}${AWS_SECRET_ACCESS_KEY}${FOLD_BACKUP_AGE_RECIPIENT}"
if [[ -n "$any_offsite" ]]; then
  [[ -n "$FOLD_BACKUP_S3_BUCKET" ]] || die "backup env is missing FOLD_BACKUP_S3_BUCKET"
  [[ -n "$FOLD_BACKUP_S3_ENDPOINT" ]] || die "backup env is missing FOLD_BACKUP_S3_ENDPOINT"
  [[ -n "$FOLD_BACKUP_S3_REGION" ]] || die "backup env is missing FOLD_BACKUP_S3_REGION"
  [[ -n "$AWS_ACCESS_KEY_ID" ]] || die "backup env is missing AWS_ACCESS_KEY_ID"
  [[ -n "$AWS_SECRET_ACCESS_KEY" ]] || die "backup env is missing AWS_SECRET_ACCESS_KEY"
  [[ -n "$FOLD_BACKUP_AGE_RECIPIENT" ]] || die "backup env is missing FOLD_BACKUP_AGE_RECIPIENT"
  [[ "$FOLD_BACKUP_S3_BUCKET" =~ ^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$ ]] \
    || die "FOLD_BACKUP_S3_BUCKET is not a bucket name"
  [[ "$FOLD_BACKUP_S3_ENDPOINT" =~ ^https://[A-Za-z0-9._:-]+$ ]] \
    || die "FOLD_BACKUP_S3_ENDPOINT must be an https URL"
  [[ "$FOLD_BACKUP_S3_REGION" =~ ^[a-z0-9-]+$ ]] || die "FOLD_BACKUP_S3_REGION is not a region name"
  [[ "$FOLD_BACKUP_AGE_RECIPIENT" =~ ^age1[0-9a-z]+$ ]] \
    || die "FOLD_BACKUP_AGE_RECIPIENT must be an age public key (age1...)"
  offsite_ready=1
fi

compose() {
  docker compose --project-directory "$DEPLOY_DIR" "$@"
}

ensure_backup_dir() {
  if (( DRY_RUN )); then
    echo "dry-run: mkdir -p $BACKUP_DIR && chmod 700 $BACKUP_DIR"
    return 0
  fi
  mkdir -p "$BACKUP_DIR"
  chmod 700 "$BACKUP_DIR"
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

dump_database() {
  local dest="$1"
  if (( DRY_RUN )); then
    echo "dry-run: docker compose --project-directory $DEPLOY_DIR exec -T db pg_dump -U $PG_USER -Fc --no-owner --no-acl $PG_DB > $dest"
    echo "dry-run: chmod 600 $dest"
    return 0
  fi
  compose exec -T db pg_dump -U "$PG_USER" -Fc --no-owner --no-acl "$PG_DB" >"$dest"
  chmod 600 "$dest"
  echo "wrote $dest"
}

dump_uploads() {
  local dest="$1"
  local volume="$2"
  if (( DRY_RUN )); then
    echo "dry-run: docker volume inspect $volume && docker run --rm -v ${volume}:/data:ro -v ${BACKUP_DIR}:/backup alpine tar czf /backup/$(basename "$dest") -C /data ."
    echo "dry-run: chmod 600 $dest"
    return 0
  fi
  if ! docker volume inspect "$volume" >/dev/null 2>&1; then
    echo "backup.sh: volume $volume is absent; skipping the uploads archive" >&2
    return 0
  fi
  docker run --rm -v "${volume}:/data:ro" -v "${BACKUP_DIR}:/backup" alpine \
    tar czf "/backup/$(basename "$dest")" -C /data .
  chmod 600 "$dest"
  echo "wrote $dest"
}

expire_old() {
  if (( DRY_RUN )); then
    echo "dry-run: expire files in $BACKUP_DIR older than ${FOLD_BACKUP_KEEP_DAYS} days, keeping day-01 files for 180 days"
    return 0
  fi
  local path base day mtime now age limit month_keep
  now="$(date +%s)"
  limit=$(( FOLD_BACKUP_KEEP_DAYS * 24 * 3600 ))
  month_keep=$(( 180 * 24 * 3600 ))
  shopt -s nullglob
  for path in "$BACKUP_DIR"/fold-*.dump "$BACKUP_DIR"/fold-*.dump.age \
    "$BACKUP_DIR"/uploads-*.tgz "$BACKUP_DIR"/uploads-*.tgz.age; do
    base="$(basename "$path")"
    if [[ "$base" =~ ^(fold|uploads)-([0-9]{8})-[0-9]{4}\.(dump|tgz)(\.age)?$ ]]; then
      day="${BASH_REMATCH[2]:6:2}"
    else
      continue
    fi
    mtime="$(stat -c %Y "$path")"
    age=$(( now - mtime ))
    if (( age > limit )); then
      if [[ "$day" == "01" && age -lt month_keep ]]; then
        continue
      fi
      rm -f "$path"
      echo "expired $path"
    fi
  done
}

encrypt_and_upload() {
  local src="$1"
  local base cipher
  base="$(basename "$src")"
  cipher="${src}.age"
  if (( DRY_RUN )); then
    echo "dry-run: age -r <recipient> -o $cipher $src"
    echo "dry-run: docker run --rm -e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY -e AWS_DEFAULT_REGION -v $BACKUP_DIR:/backups:ro amazon/aws-cli s3 cp /backups/${base}.age s3://${FOLD_BACKUP_S3_BUCKET}/fold/${base}.age --endpoint-url ${FOLD_BACKUP_S3_ENDPOINT}"
    return 0
  fi
  command -v age >/dev/null 2>&1 || die "age is not installed; refusing to upload an unencrypted backup"
  age -r "$FOLD_BACKUP_AGE_RECIPIENT" -o "$cipher" "$src"
  chmod 600 "$cipher"
  AWS_ACCESS_KEY_ID="$AWS_ACCESS_KEY_ID" \
    AWS_SECRET_ACCESS_KEY="$AWS_SECRET_ACCESS_KEY" \
    AWS_DEFAULT_REGION="$FOLD_BACKUP_S3_REGION" \
    docker run --rm \
      -e AWS_ACCESS_KEY_ID \
      -e AWS_SECRET_ACCESS_KEY \
      -e AWS_DEFAULT_REGION \
      -v "${BACKUP_DIR}:/backups:ro" \
      amazon/aws-cli \
      s3 cp "/backups/${base}.age" "s3://${FOLD_BACKUP_S3_BUCKET}/fold/${base}.age" \
      --endpoint-url "$FOLD_BACKUP_S3_ENDPOINT"
  echo "uploaded s3://${FOLD_BACKUP_S3_BUCKET}/fold/${base}.age"
}

mark_ok() {
  if (( DRY_RUN )); then
    echo "dry-run: write $BACKUP_DIR/LAST_OK"
    return 0
  fi
  date -u +%Y-%m-%dT%H:%M:%SZ >"$BACKUP_DIR/LAST_OK"
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

run_backup() {
  local stamp dump uploads volume
  stamp="$(date +%Y%m%d-%H%M)"
  dump="$BACKUP_DIR/fold-${stamp}.dump"
  uploads="$BACKUP_DIR/uploads-${stamp}.tgz"
  volume="$(project_name)_uploads"
  ensure_backup_dir
  dump_database "$dump"
  dump_uploads "$uploads" "$volume"
  expire_old
  if (( offsite_ready )); then
    echo "offsite: s3://${FOLD_BACKUP_S3_BUCKET}/fold/ via ${FOLD_BACKUP_S3_ENDPOINT}"
    encrypt_and_upload "$dump"
    if [[ -f "$uploads" ]] || (( DRY_RUN )); then
      encrypt_and_upload "$uploads"
    fi
  else
    echo "offsite not configured (no complete $ENV_FILE); dumps stay in $BACKUP_DIR"
  fi
  mark_ok
}

if (( RESTORE_TEST )); then
  restore_test
  exit 0
fi

run_backup
