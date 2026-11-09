#!/usr/bin/env bash
# Upgrade or roll back Fold on the droplet.
#
#   upgrade.sh --image ghcr.io/grierj/fold:1.2.3
#   upgrade.sh --commit abcdef1234567 --source-dir /root/fold
#   upgrade.sh --rollback
#
# A forward run dumps the database, records the current image or commit in
# /opt/fold/release.prev (mode 600), then runs provision.sh. --rollback stops
# the site through restore.sh, which restores into a fresh database and opens
# only when no erased account would be revived. Otherwise recovery stays private.
# :latest is refused. --dry-run prints the plan and changes nothing.
set -euo pipefail
TOOL=upgrade.sh

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
[[ -f "$SCRIPT_DIR/checks.sh" ]] || {
  echo "upgrade.sh: missing checks.sh; run the current provision.sh to install the tool set" >&2
  exit 1
}
# shellcheck source=deploy/digitalocean/checks.sh
source "$SCRIPT_DIR/checks.sh"

ORIGINAL_ARGS=("$@")
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

usage() {
  cat <<'EOF'
Usage: upgrade.sh --image ghcr.io/grierj/fold:1.2.3
       upgrade.sh --commit GITSHA [--source-dir /root/fold]
       upgrade.sh --rollback
       [--deploy-dir /opt/fold] [--domain groups.example.org] [--dry-run]

Pins the new release to an image tag (FOLD_VERSION in <deploy-dir>/.env) or a
git commit (build with docker-compose.build.yml), after a pg_dump.
--rollback exits 3 if the archive is restored but SITE IS CLOSED in private
recovery pending operator checks. Exit 0 requires a successful reopen.
--rollback restores the dump and the previous tag or commit recorded in
<deploy-dir>/release.prev. :latest is refused, as is a FOLD_VERSION of latest.
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
    --rollback)
      DO_ROLLBACK=1
      shift
      ;;
    --image|--image=*) take_value IMAGE "$@"; shift "$OPT_SHIFT" ;;
    --commit|--commit=*) take_value COMMIT "$@"; shift "$OPT_SHIFT" ;;
    --deploy-dir|--deploy-dir=*) take_value DEPLOY_DIR "$@"; shift "$OPT_SHIFT" ;;
    --source-dir|--source-dir=*) take_value SOURCE_DIR "$@"; shift "$OPT_SHIFT" ;;
    --domain|--domain=*) take_value DOMAIN "$@"; shift "$OPT_SHIFT" ;;
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

# The installed tools, not a target checkout, enforce every later release.
# Keep fd 9 across this exec and across the nested provisioner.
if (( ! DRY_RUN )); then
  [[ "$(id -u)" == 0 ]] || die "run as root: sudo $0 ..."
  [[ -d "$DEPLOY_DIR" ]] || die "deploy directory does not exist; run provision.sh first"
  DEPLOY_DIR="$(cd "$DEPLOY_DIR" && pwd -P)"
fi
declare -F ops_lock_acquire >/dev/null || die "checks.sh lacks ops_lock_acquire; run the current provision.sh to install the tool set"
validate_deploy_drill "$DEPLOY_DIR" || exit 1
if (( ! DRY_RUN )); then
  if (( DO_ROLLBACK )); then
    ops_lock_acquire "$DEPLOY_DIR" upgrade-rollback || exit $?
  else
    ops_lock_acquire "$DEPLOY_DIR" upgrade-forward || exit $?
  fi
  [[ ! -e "$DEPLOY_DIR/recovery.lock" && ! -L "$DEPLOY_DIR/recovery.lock" ]] || die "recovery in progress; run $DEPLOY_DIR/restore.sh --status"
  deploy_drill_pause "$DEPLOY_DIR" after-lock
fi

install_enforcing_tools() (
  local name stage="" old="" path
  local -a tools=(provision.sh checks.sh backup.sh smoke.sh email-dns.sh upgrade.sh backup.env.example deploy-tools-version)
  for name in ops_lock_acquire image_recovery_level commit_recovery_level host_recovery_floor write_recovery_floor; do
    declare -F "$name" >/dev/null || die "checks.sh lacks $name; run the current provision.sh to install the tool set"
  done
  tools+=(restore.sh compose.recovery.yml)
  for name in "${tools[@]}"; do
    [[ -f "$SCRIPT_DIR/$name" ]] || die "missing $name beside upgrade.sh; run the current provision.sh to install the tool set"
  done
  if (( DRY_RUN )); then
    require_deploy_tools_version "$SCRIPT_DIR" "$DEPLOY_DIR" || exit 1
    echo "dry-run: replace $DEPLOY_DIR/deploy-tools whole with the enforcing tool set and re-exec under fd 9"
    return 0
  fi
  recover_enforcing_tools "$DEPLOY_DIR" || exit 1
  require_deploy_tools_version "$SCRIPT_DIR" "$DEPLOY_DIR" || exit 1
  # A prior invocation may have died during copying or after the second
  # rename. A complete live set (or a first installation) makes these stale.
  for path in "$DEPLOY_DIR"/.deploy-tools.* "$DEPLOY_DIR"/.old-deploy-tools.*; do
    [[ ! -e "$path" ]] || rm -rf -- "$path"
  done
  [[ "$SCRIPT_DIR" != "$DEPLOY_DIR/deploy-tools" ]] || return 0
  # EXIT covers copy/chown/rename failures. SIGKILL is recovered on the next
  # installed upgrade.sh run, before any backup, checkout or pin change.
  # shellcheck disable=SC2317,SC2329 # Invoked by EXIT, also after failed commands.
  finish_tool_swap() {
    local rc=$?
    trap - EXIT
    if [[ -n "$old" && -d "$old" && ! -e "$DEPLOY_DIR/deploy-tools" ]]; then
      mv "$old" "$DEPLOY_DIR/deploy-tools" || {
        echo "upgrade.sh: tool swap failed; saved tools remain at $old. Restore that directory to $DEPLOY_DIR/deploy-tools before retrying" >&2
        rc=1
      }
    fi
    [[ -z "$stage" || ! -d "$stage" ]] || rm -rf -- "$stage"
    if (( rc != 0 )); then
      echo "upgrade.sh: enforcing tool swap did not complete; no backup, checkout or release change was attempted. Check deploy-tools and any saved .old-deploy-tools.* before retrying" >&2
    fi
    exit "$rc"
  }
  trap finish_tool_swap EXIT
  stage="$(mktemp -d "$DEPLOY_DIR/.deploy-tools.XXXXXX")"
  chmod 700 "$stage"
  chown 0:0 "$stage"
  for name in "${tools[@]}"; do
    install -m 644 "$SCRIPT_DIR/$name" "$stage/$name"
    [[ "$name" != *.sh ]] || chmod 755 "$stage/$name"
  done
  old="$(mktemp -d "$DEPLOY_DIR/.old-deploy-tools.XXXXXX")"
  rmdir "$old"
  [[ ! -e "$DEPLOY_DIR/deploy-tools" ]] || mv "$DEPLOY_DIR/deploy-tools" "$old"
  mv "$stage" "$DEPLOY_DIR/deploy-tools"
  [[ ! -d "$old" ]] || rm -rf "$old"
  trap - EXIT
)
install_enforcing_tools
if [[ -z "$SOURCE_DIR" ]]; then
  if [[ -f "$SCRIPT_DIR/../../docker-compose.yml" ]]; then
    SOURCE_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
  else
    SOURCE_DIR=/root/fold
  fi
fi
# Replace the original shell, not the tool-swap subshell. Otherwise the
# caller would resume after the installed upgrade and deploy a second time.
if (( ! DRY_RUN )) && [[ "$SCRIPT_DIR" != "$DEPLOY_DIR/deploy-tools" ]]; then
  exec "$DEPLOY_DIR/deploy-tools/upgrade.sh" "${ORIGINAL_ARGS[@]}" --source-dir "$SOURCE_DIR"
fi
if [[ "${DEPLOY_DRILL:-}" == 1 && "$DEPLOY_DIR" != /opt/fold ]]; then
  BACKUP_DIR="$DEPLOY_DIR/dumps"
fi
PROVISION="$DEPLOY_DIR/deploy-tools/provision.sh"

check_target_level() {
  local mode="$1" target="$2" floor level
  if [[ "$mode" == build ]]; then
    # Resolve exactly once. No backup, release record or checkout precedes this.
    if ! RESOLVED_COMMIT="$(git -C "$SOURCE_DIR" rev-parse --verify "${target}^{commit}" 2>/dev/null)"; then
      if (( DRY_RUN && DO_ROLLBACK )); then
        RESOLVED_COMMIT="$target"
      else
        die "${target} is not a commit in ${SOURCE_DIR}"
      fi
    fi
  fi
  if (( DRY_RUN )); then
    dry_run_recovery_check "$DEPLOY_DIR" "$mode" "$SOURCE_DIR" "${RESOLVED_COMMIT:-}" || exit 1
    return 0
  fi
  floor="$(host_recovery_floor "$DEPLOY_DIR")"
  if [[ "$mode" == image ]]; then
    docker pull "$target"
    level="$(image_recovery_level "$target")"
  else
    level="$(commit_recovery_level "$SOURCE_DIR" "$RESOLVED_COMMIT")"
  fi
  require_recovery_level "$level" "$floor" || exit 1
}

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
  "$backup" --deploy-dir "$DEPLOY_DIR" --backup-dir "$BACKUP_DIR"
}

newest_dump() {
  local found
  found="$(find "$BACKUP_DIR" -name 'fold-*.dump' -printf '%T@ %p\n' 2>/dev/null | sort -n | tail -1 | cut -d' ' -f2-)"
  [[ -n "$found" ]] || die "backup did not write a dump in ${BACKUP_DIR}"
  printf '%s' "$found"
}

# What the deploy dir runs now (plan C10). Order: an explicit --image pin from
# another repository (the marker line in the compose file), then a source
# build (the build override is present, or an older compose file still has
# `build: .`), else a pulled release named by FOLD_VERSION in .env.
current_release() {
  local compose="$DEPLOY_DIR/docker-compose.yml" version
  REL_MODE=""
  REL_IMAGE=""
  REL_COMMIT=""
  if [[ -f "$compose" ]] && grep -q ' # fold-app-image$' "$compose"; then
    REL_MODE="image"
    REL_IMAGE="$(awk '/# fold-app-image$/ { print $2; exit }' "$compose")"
    [[ -n "$REL_IMAGE" ]] || die "could not read the current image from ${compose}"
    return 0
  fi
  if [[ -f "$DEPLOY_DIR/docker-compose.build.yml" ]] \
    || { [[ -f "$compose" ]] && grep -q '^    build: \.$' "$compose"; }; then
    REL_MODE="build"
    if ! git -C "$SOURCE_DIR" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
      (( DRY_RUN )) || die "${SOURCE_DIR} is not a git checkout; cannot record the current commit"
      REL_COMMIT="unverified"
      echo "dry-run: current build commit is unverified; restore the git checkout ${SOURCE_DIR} before a real run"
      return 0
    fi
    REL_COMMIT="$(git -C "$SOURCE_DIR" rev-parse HEAD)"
    return 0
  fi
  version="$(env_value "$DEPLOY_DIR/.env" FOLD_VERSION || true)"
  [[ -n "$version" ]] || die "${DEPLOY_DIR}/.env has no FOLD_VERSION; cannot tell which release is running"
  assert_release_version "$version" || exit 1
  REL_MODE="image"
  REL_IMAGE="${FOLD_IMAGE_REPO}:${version}"
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
  [[ "$REL_DUMP" == "$BACKUP_DIR"/fold-[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]-[0-9][0-9][0-9][0-9].dump ]] \
    || die "release.prev dump path is not a backup.sh file"
  [[ "$REL_DOMAIN" =~ ^[A-Za-z0-9.-]+$ ]] || die "release.prev domain is invalid"
  [[ "$REL_SOURCE" == /* && "$REL_SOURCE" != *..* && "$REL_SOURCE" != *[[:space:]]* ]] \
    || die "release.prev source_dir is invalid"
  if [[ "$REL_MODE" == "image" ]]; then
    # Written by current_release as FOLD_IMAGE_REPO:FOLD_VERSION (or a fork's
    # explicit pin); provision.sh turns the tag back into FOLD_VERSION.
    [[ "$REL_IMAGE" =~ ^[A-Za-z0-9][A-Za-z0-9._:/-]*$ ]] || die "release.prev previous_image is invalid"
    assert_pinned_image "$REL_IMAGE" || exit 1
  else
    [[ "$REL_COMMIT" =~ ^[0-9a-f]{7,40}$ ]] || die "release.prev commit is invalid"
  fi
  [[ -f "$REL_DUMP" ]] || die "dump not found: ${REL_DUMP}"
}

run_provision() {
  local rc=0
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
  "${cmd[@]}" || rc=$?
  if (( rc == 76 )); then
    die "deployment completed and smoke passed, but the recovery floor file was not recorded. Keep the current pin and image. Repair storage/permissions, then rerun ${DEPLOY_DIR}/provision.sh with the same release and options to record the floor; do not roll back for this error" 76
  elif (( rc != 0 )); then
    die "provisioning did not complete; inspect its diagnostic and current service health. The deployed pin still contributes to the recovery floor, even after failed smoke. --rollback is refused if the previous release is below that floor. Keep the current release and restore data with its restore.sh --dump once installed, or repair and rerun it. Do not lower the floor or change the pin to bypass refusal"
  fi
}

forward() {
  read_domain
  require_root
  if [[ -n "$COMMIT" ]]; then
    check_target_level build "$COMMIT"
    COMMIT="$RESOLVED_COMMIT"
    if [[ -n "$(git -C "$SOURCE_DIR" status --porcelain)" ]]; then
      die "${SOURCE_DIR} has uncommitted changes. Commit or stash them before --commit."
    fi
  fi
  if [[ -n "$IMAGE" ]]; then
    check_target_level image "$IMAGE"
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
  if [[ "$REL_MODE" == image ]]; then
    check_target_level image "$REL_IMAGE"
  else
    check_target_level build "$REL_COMMIT"
    REL_COMMIT="$RESOLVED_COMMIT"
  fi
  echo "upgrade.sh: rolling back mode=${REL_MODE} dump=${REL_DUMP}"
  local -a cmd=("$SCRIPT_DIR/restore.sh" --dump "$REL_DUMP" --reopen-if-none --deploy-dir "$DEPLOY_DIR" --source-dir "$REL_SOURCE")
  if [[ "$REL_MODE" == image ]]; then cmd+=(--image "$REL_IMAGE"); else cmd+=(--commit "$REL_COMMIT"); fi
  [[ "${DEPLOY_DRILL:-}" != 1 ]] || cmd+=(--drill)
  if (( DRY_RUN )); then
    printf 'dry-run: hand off rollback to'; printf ' %q' "${cmd[@]}"; printf '\n'
    echo 'dry-run: reopen automatically only when the snapshot-safe count is exactly zero; otherwise remain in private recovery'
    return 0
  fi
  exec "${cmd[@]}"
}

if (( DO_ROLLBACK )); then
  rollback
else
  forward
fi
