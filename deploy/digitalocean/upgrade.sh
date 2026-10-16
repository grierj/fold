#!/usr/bin/env bash
# Upgrade or roll back Fold on the droplet.
#
#   upgrade.sh --image ghcr.io/grierj/fold:1.2.3
#   upgrade.sh --commit abcdef1234567 --source-dir /root/fold
#   upgrade.sh --rollback
#
# A forward run dumps the database, records the current image or commit in
# /opt/fold/release.prev (mode 600), then runs provision.sh. --rollback stops
# the app, pg_restores that dump, and starts the recorded image or commit.
# :latest is refused. --dry-run prints the plan and changes nothing.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=deploy/digitalocean/checks.sh
source "$SCRIPT_DIR/checks.sh"

REL_MODE=""
REL_IMAGE=""
REL_COMMIT=""

DRY_RUN=0
DO_ROLLBACK=0
IMAGE=""
COMMIT=""
DEPLOY_DIR="/opt/fold"
SOURCE_DIR=""
DOMAIN=""
BACKUP_DIR="/var/backups/fold"

die() {
  printf 'upgrade.sh: %s\n' "$1" >&2
  exit "${2:-1}"
}

usage() {
  cat <<'EOF'
Usage: upgrade.sh --image ghcr.io/grierj/fold:1.2.3
       upgrade.sh --commit GITSHA [--source-dir /root/fold]
       upgrade.sh --rollback
       [--deploy-dir /opt/fold] [--domain groups.example.org] [--dry-run]

Pins the new release to an image tag or a git commit, after a pg_dump.
--rollback restores the dump and the previous tag or commit recorded in
<deploy-dir>/release.prev. :latest is refused.
EOF
}

need_value() {
  [[ $# -ge 2 && -n "${2:-}" ]] || die "$1 needs a value"
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
    --rollback)
      DO_ROLLBACK=1
      shift
      ;;
    --image)
      need_value "$@"
      IMAGE="$2"
      shift 2
      ;;
    --image=*)
      IMAGE="${1#*=}"
      shift
      ;;
    --commit)
      need_value "$@"
      COMMIT="$2"
      shift 2
      ;;
    --commit=*)
      COMMIT="${1#*=}"
      shift
      ;;
    --deploy-dir)
      need_value "$@"
      DEPLOY_DIR="$2"
      shift 2
      ;;
    --deploy-dir=*)
      DEPLOY_DIR="${1#*=}"
      shift
      ;;
    --source-dir)
      need_value "$@"
      SOURCE_DIR="$2"
      shift 2
      ;;
    --source-dir=*)
      SOURCE_DIR="${1#*=}"
      shift
      ;;
    --domain)
      need_value "$@"
      DOMAIN="$2"
      shift 2
      ;;
    --domain=*)
      DOMAIN="${1#*=}"
      shift
      ;;
    *)
      die "unknown option: $1 (try --help)"
      ;;
  esac
done

if (( DO_ROLLBACK )); then
  [[ -z "$IMAGE" && -z "$COMMIT" ]] || die "pass only --rollback, not --image or --commit"
elif [[ -n "$IMAGE" && -n "$COMMIT" ]]; then
  die "pass either --image or --commit, not both"
elif [[ -z "$IMAGE" && -z "$COMMIT" ]]; then
  die "pass --image, --commit, or --rollback"
fi

[[ "$DEPLOY_DIR" == /* ]] || die "--deploy-dir must be an absolute path"
if [[ -n "$IMAGE" ]]; then
  [[ "$IMAGE" =~ ^[A-Za-z0-9][A-Za-z0-9._:/-]*$ ]] || die "--image has characters this script will not put in a compose file"
  assert_pinned_image "$IMAGE" || exit 1
fi
if [[ -n "$COMMIT" ]]; then
  [[ "$COMMIT" =~ ^[0-9a-f]{7,40}$ ]] || die "--commit must be a git SHA (7 to 40 hex digits)"
fi

if [[ -z "$SOURCE_DIR" ]]; then
  if [[ -f "$SCRIPT_DIR/provision.sh" ]]; then
    SOURCE_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
  elif [[ -f /root/fold/deploy/digitalocean/provision.sh ]]; then
    SOURCE_DIR=/root/fold
  else
    die "pass --source-dir (the git checkout that contains provision.sh)"
  fi
fi
PROVISION="$SOURCE_DIR/deploy/digitalocean/provision.sh"
[[ -f "$PROVISION" ]] || die "missing ${PROVISION}"

read_domain() {
  if [[ -n "$DOMAIN" ]]; then
    return 0
  fi
  [[ -f "$DEPLOY_DIR/.env" ]] || die "--domain is required (no ${DEPLOY_DIR}/.env yet)"
  DOMAIN="$(env_value "$DEPLOY_DIR/.env" DOMAIN || true)"
  [[ -n "$DOMAIN" ]] || die "${DEPLOY_DIR}/.env has no DOMAIN"
}

require_root() {
  if [[ "$(id -u)" -eq 0 ]]; then
    return 0
  fi
  if (( DRY_RUN )); then
    echo "dry-run: a real run must be root (sudo)"
    return 0
  fi
  die "run as root: sudo $0 ..."
}

run_backup() {
  local backup="$DEPLOY_DIR/backup.sh"
  if [[ ! -x "$backup" ]]; then
    backup="$SCRIPT_DIR/backup.sh"
  fi
  [[ -f "$backup" ]] || die "missing backup.sh"
  if (( DRY_RUN )); then
    echo "dry-run: ${backup} --deploy-dir ${DEPLOY_DIR}"
    return 0
  fi
  "$backup" --deploy-dir "$DEPLOY_DIR"
}

newest_dump() {
  local found
  found="$(find "$BACKUP_DIR" -name 'fold-*.dump' -printf '%T@ %p\n' 2>/dev/null | sort -n | tail -1 | cut -d' ' -f2-)"
  [[ -n "$found" ]] || die "backup did not write a dump in ${BACKUP_DIR}"
  printf '%s' "$found"
}

current_release() {
  local compose="$DEPLOY_DIR/docker-compose.yml"
  REL_MODE=""
  REL_IMAGE=""
  REL_COMMIT=""
  if [[ -f "$compose" ]] && grep -q ' # fold-app-image$' "$compose"; then
    REL_MODE="image"
    REL_IMAGE="$(awk '/# fold-app-image$/ { print $2; exit }' "$compose")"
    [[ -n "$REL_IMAGE" ]] || die "could not read the current image from ${compose}"
    return 0
  fi
  REL_MODE="build"
  git -C "$SOURCE_DIR" rev-parse --is-inside-work-tree >/dev/null 2>&1 \
    || die "${SOURCE_DIR} is not a git checkout; cannot record the current commit"
  REL_COMMIT="$(git -C "$SOURCE_DIR" rev-parse HEAD)"
}

write_release() {
  local dump="$1" path="$DEPLOY_DIR/release.prev"
  if (( DRY_RUN )); then
    echo "dry-run: write ${path} mode 600 (previous ${REL_MODE}, dump path; no secrets)"
    return 0
  fi
  umask 077
  cat >"$path" <<EOF
mode=${REL_MODE}
previous_image=${REL_IMAGE}
previous_commit=${REL_COMMIT}
dump=${dump}
source_dir=${SOURCE_DIR}
domain=${DOMAIN}
EOF
  chmod 600 "$path"
  echo "upgrade.sh: recorded ${path}"
}

load_release() {
  local file="$DEPLOY_DIR/release.prev" line key val
  [[ -f "$file" ]] || die "no ${file}. Nothing to roll back."
  REL_MODE=""
  REL_IMAGE=""
  REL_COMMIT=""
  REL_DUMP=""
  REL_SOURCE=""
  REL_DOMAIN=""
  while IFS= read -r line || [[ -n "$line" ]]; do
    case "$line" in
      ''|'#'*) continue ;;
    esac
    [[ "$line" == *=* ]] || die "bad line in ${file}"
    key="${line%%=*}"
    val="${line#*=}"
    case "$key" in
      mode) REL_MODE="$val" ;;
      previous_image) REL_IMAGE="$val" ;;
      previous_commit) REL_COMMIT="$val" ;;
      dump) REL_DUMP="$val" ;;
      source_dir) REL_SOURCE="$val" ;;
      domain) REL_DOMAIN="$val" ;;
      *) die "unknown key ${key} in ${file}" ;;
    esac
  done <"$file"
  [[ "$REL_MODE" == "image" || "$REL_MODE" == "build" ]] || die "release.prev mode is invalid"
  [[ "$REL_DUMP" == /var/backups/fold/fold-[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]-[0-9][0-9][0-9][0-9].dump ]] \
    || die "release.prev dump path is not a backup.sh file"
  [[ "$REL_DOMAIN" =~ ^[A-Za-z0-9.-]+$ ]] || die "release.prev domain is invalid"
  [[ "$REL_SOURCE" == /* && "$REL_SOURCE" != *..* && "$REL_SOURCE" != *[[:space:]]* ]] \
    || die "release.prev source_dir is invalid"
  if [[ "$REL_MODE" == "image" ]]; then
    assert_pinned_image "$REL_IMAGE" || exit 1
  else
    [[ "$REL_COMMIT" =~ ^[0-9a-f]{7,40}$ ]] || die "release.prev commit is invalid"
  fi
  [[ -f "$REL_DUMP" ]] || die "dump not found: ${REL_DUMP}"
  [[ -f "$REL_SOURCE/deploy/digitalocean/provision.sh" ]] || die "missing provision.sh in ${REL_SOURCE}"
}

restore_dump() {
  local dump="$1" rc
  if (( DRY_RUN )); then
    echo "dry-run: docker compose --project-directory ${DEPLOY_DIR} stop app"
    echo "dry-run: pg_restore -U church -d fold --clean --if-exists --no-owner < ${dump}"
    return 0
  fi
  docker compose --project-directory "$DEPLOY_DIR" stop app
  set +e
  docker compose --project-directory "$DEPLOY_DIR" exec -T db \
    pg_restore -U church -d fold --clean --if-exists --no-owner <"$dump"
  rc=$?
  set -e
  # pg_restore exits 1 when it ignored a warning. 2 and above are errors.
  if [[ "$rc" -gt 1 ]]; then
    die "pg_restore failed (${rc}). The app is still stopped. Dump: ${dump}"
  fi
  echo "upgrade.sh: restored ${dump}"
}

run_provision() {
  local -a cmd=("$PROVISION" --skip-harden --domain "$DOMAIN" --deploy-dir "$DEPLOY_DIR" --source-dir "$SOURCE_DIR")
  if [[ "$1" == "image" ]]; then
    cmd+=(--image "$2")
  else
    cmd+=(--allow-onbox-build)
  fi
  if (( DRY_RUN )); then
    printf 'dry-run:'
    printf ' %q' "${cmd[@]}"
    printf '\n'
    echo "dry-run: provision.sh runs smoke.sh when it finishes"
    return 0
  fi
  if ! "${cmd[@]}"; then
    die "upgrade failed. Roll back with: $0 --rollback --deploy-dir ${DEPLOY_DIR}"
  fi
}

forward() {
  read_domain
  require_root
  if [[ -n "$COMMIT" ]]; then
    git -C "$SOURCE_DIR" cat-file -e "${COMMIT}^{commit}" 2>/dev/null \
      || die "${COMMIT} is not a commit in ${SOURCE_DIR}"
    if [[ -n "$(git -C "$SOURCE_DIR" status --porcelain)" ]]; then
      die "${SOURCE_DIR} has uncommitted changes. Commit or stash them before --commit."
    fi
  fi
  if (( DRY_RUN )); then
    if [[ ! -f "$DEPLOY_DIR/docker-compose.yml" ]]; then
      echo "dry-run: no compose file yet; a real run records the current image or commit before changing it"
      REL_MODE="unrecorded"
      REL_IMAGE=""
      REL_COMMIT=""
    else
      current_release
      echo "dry-run: current release mode=${REL_MODE} image=${REL_IMAGE:-build} commit=${REL_COMMIT:-}"
    fi
  else
    current_release
  fi
  run_backup
  local dump="(newest fold-*.dump)"
  if (( ! DRY_RUN )); then
    dump="$(newest_dump)"
  fi
  write_release "$dump"
  if [[ -n "$COMMIT" ]]; then
    if (( DRY_RUN )); then
      echo "dry-run: git -C ${SOURCE_DIR} checkout --detach ${COMMIT}"
    else
      git -C "$SOURCE_DIR" checkout --detach "$COMMIT"
    fi
    run_provision build
  else
    run_provision image "$IMAGE"
  fi
}

rollback() {
  require_root
  if (( DRY_RUN )) && [[ ! -f "$DEPLOY_DIR/release.prev" ]]; then
    die "no ${DEPLOY_DIR}/release.prev. Nothing to roll back."
  fi
  load_release
  DOMAIN="$REL_DOMAIN"
  SOURCE_DIR="$REL_SOURCE"
  PROVISION="$SOURCE_DIR/deploy/digitalocean/provision.sh"
  echo "upgrade.sh: rolling back mode=${REL_MODE} dump=${REL_DUMP}"
  restore_dump "$REL_DUMP"
  if [[ "$REL_MODE" == "image" ]]; then
    run_provision image "$REL_IMAGE"
  else
    if (( DRY_RUN )); then
      echo "dry-run: git -C ${SOURCE_DIR} checkout --detach ${REL_COMMIT}"
    else
      git -C "$SOURCE_DIR" checkout --detach "$REL_COMMIT"
    fi
    run_provision build
  fi
}

if (( DO_ROLLBACK )); then
  rollback
else
  forward
fi
