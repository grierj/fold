#!/usr/bin/env bash
# Checks and shell helpers shared by the deploy tools. Source this file.
# A check that cannot be performed fails closed.

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  echo "checks.sh is sourced by provision.sh and smoke.sh" >&2
  exit 1
fi

check_fail() {
  printf 'check: %s\n' "$1" >&2
  return 1
}

# die MESSAGE [STATUS]: the tool scripts (backup, email-dns, provision, smoke,
# upgrade) set TOOL to their own prefix and stop here.
die() {
  printf '%s: %s\n' "${TOOL:-${0##*/}}" "$1" >&2
  exit "${2:-1}"
}

need_value() {
  [[ $# -ge 2 && -n "${2:-}" ]] || die "$1 needs a value"
}

# take_value VAR OPTION [VALUE]...: for `OPTION VALUE` or `OPTION=VALUE` at the
# head of the arguments, sets VAR and puts the words used (2 or 1) in OPT_SHIFT.
# Call it from a case arm, then `shift "$OPT_SHIFT"`.
# shellcheck disable=SC2034 # OPT_SHIFT is read by the caller.
take_value() {
  local var="$1"
  shift
  if [[ "$1" == *=* ]]; then
    printf -v "$var" '%s' "${1#*=}"
    OPT_SHIFT=1
  else
    need_value "$@"
    printf -v "$var" '%s' "$2"
    OPT_SHIFT=2
  fi
}

# compose_in DIR ARGS...: Compose for the stack deployed in DIR.
compose_in() {
  local dir="$1"
  shift
  docker compose --project-directory "$dir" "$@"
}

# dc, pg and recovery_ids are for restore.sh and the drills, which set DIR (the
# deploy directory) and PROJECT (its Compose project). pg DATABASE [psql args].
dc() { compose_in "$DIR" "$@"; }
pg() { dc exec -T db psql -X -U church -d "$1" -At -v ON_ERROR_STOP=1 "${@:2}"; }
recovery_ids() { docker ps -aq --filter "label=com.docker.compose.project=$PROJECT" --filter label=com.docker.compose.service=recovery; }

# Caller holds the operations lock; these are private interrupted pin snapshots.
remove_stale_pin_saves() {
  local entry
  for entry in "$1"/.pin-save.*; do
    [[ -e "$entry" || -L "$entry" ]] || continue
    rm -rf -- "$entry" || return 1
  done
}

# env_value FILE KEY — prints the value, or returns 1 when the key is absent.
env_value() {
  local file="$1" want="$2" line key val
  [[ -f "$file" ]] || return 1
  while IFS= read -r line || [[ -n "$line" ]]; do
    line="${line%$'\r'}"
    case "$line" in
      ''|'#'*) continue ;;
    esac
    [[ "$line" == *=* ]] || continue
    key="${line%%=*}"
    val="${line#*=}"
    if [[ "$val" == \"*\" && "$val" == *\" ]]; then
      val="${val:1:${#val}-2}"
    fi
    if [[ "$key" == "$want" ]]; then
      printf '%s' "$val"
      return 0
    fi
  done <"$file"
  return 1
}

# The image every deploy pulls unless a fork or hotfix passes its own --image.
FOLD_IMAGE_REPO="ghcr.io/grierj/fold"

# assert_pinned_version LABEL: a tag Compose may put after the colon. This is
# the loose, shared rule: not empty, not "latest", and a valid tag. Local build
# labels (dev, ci) and fork tags go through it alone.
assert_pinned_version() {
  local version="$1"
  [[ -n "$version" ]] || { check_fail "FOLD_VERSION is empty. Pin an exact release such as 0.5.0." || return; }
  if [[ "$version" == latest ]]; then
    check_fail "FOLD_VERSION is latest. Refusing a floating tag; pin an exact release such as 0.5.0." || return
  fi
  [[ "$version" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]] \
    || { check_fail "FOLD_VERSION '${version}' is not a valid image tag." || return; }
}

# assert_release_version LABEL: what a pull of ${FOLD_IMAGE_REPO} may name. An
# exact release (x.y.z, optionally -prerelease) or a full 40-hex commit SHA.
# Never a moving alias such as 0.5 or a label such as dev.
assert_release_version() {
  local version="$1"
  assert_pinned_version "$version" || return
  if [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ || "$version" =~ ^[0-9a-f]{40}$ ]]; then
    return 0
  fi
  check_fail "FOLD_VERSION '${version}' is not an exact release. Use x.y.z (such as 0.5.0) or a full commit SHA, not a moving tag like 0.5." || return
}

# assert_pinned_image REF: the --image path. The official image needs an exact
# release; another repository (a fork or hotfix) only the loose rule.
assert_pinned_image() {
  local image="$1" label
  case "$image" in
    *:latest|*/latest)
      check_fail "${image} is not a pinned tag. Refusing :latest." || return
      ;;
  esac
  label="${image##*:}"
  if [[ "$image" != *:* || "$label" == */* ]]; then
    check_fail "${image} has no tag. Example: ${FOLD_IMAGE_REPO}:1.2.3" || return
  fi
  if [[ "$image" == "${FOLD_IMAGE_REPO}:"* ]]; then
    assert_release_version "$label" || return
  else
    assert_pinned_version "$label" || return
  fi
}

# assert_deploy_pinned DIR: the FOLD_VERSION a deploy dir's .env will pull.
# An explicit --image pin (the marker line) was checked when it was written. A
# source build (COMPOSE_FILE names the build override) only needs a local
# label; a pulled deploy needs an exact release.
assert_deploy_pinned() {
  local dir="$1" version compose_file
  if [[ -f "$dir/docker-compose.yml" ]] && grep -q ' # fold-app-image$' "$dir/docker-compose.yml"; then
    return 0
  fi
  version="$(env_value "$dir/.env" FOLD_VERSION || true)"
  compose_file="$(env_value "$dir/.env" COMPOSE_FILE || true)"
  if [[ "$compose_file" == *docker-compose.build.yml* ]]; then
    assert_pinned_version "$version" || return
  else
    assert_release_version "$version" || return
  fi
}

assert_env_mode() {
  local file="$1" mode owner
  [[ -f "$file" ]] || { check_fail "missing ${file}" || return; }
  mode="$(stat -c '%a' "$file")"
  owner="$(stat -c '%u' "$file")"
  [[ "$mode" == "600" ]] || { check_fail "${file} is mode ${mode}; want 600" || return; }
  [[ "$owner" == "0" ]] || { check_fail "${file} is not owned by root" || return; }
}

# Secret env files must not be in a git index. Directories without .git are skipped.
assert_env_untracked() {
  local dir tracked
  for dir in "$@"; do
    [[ -n "$dir" && -d "$dir/.git" ]] || continue
    if ! command -v git >/dev/null 2>&1; then
      check_fail "git is required to confirm .env is not committed in ${dir}" || return
    fi
    if ! tracked="$(git -C "$dir" ls-files -- .env backup.env 2>/tmp/fold-git.err)"; then
      cat /tmp/fold-git.err >&2 || true
      check_fail "git ls-files failed in ${dir}; cannot confirm .env is untracked" || return
    fi
    if [[ -n "$tracked" ]]; then
      check_fail "secret env file is committed in ${dir}: ${tracked//$'\n'/, }" || return
    fi
  done
}

assert_ssh_locked() {
  local dump line key want
  if ! command -v sshd >/dev/null 2>&1; then
    check_fail "sshd is not installed; cannot verify root and password login are off" || return
  fi
  if ! dump="$(sshd -T 2>/tmp/fold-sshd-t.err)"; then
    cat /tmp/fold-sshd-t.err >&2 || true
    check_fail "sshd -T failed; root and password login were not verified" || return
  fi
  for key in permitrootlogin passwordauthentication kbdinteractiveauthentication authenticationmethods pubkeyauthentication; do
    case "$key" in
      permitrootlogin) want="permitrootlogin no" ;;
      passwordauthentication) want="passwordauthentication no" ;;
      kbdinteractiveauthentication) want="kbdinteractiveauthentication no" ;;
      authenticationmethods) want="authenticationmethods publickey" ;;
      pubkeyauthentication) want="pubkeyauthentication yes" ;;
    esac
    line="$(grep -E "^${key} " <<<"$dump" | head -n 1 || true)"
    if [[ "$line" != "$want" ]]; then
      check_fail "sshd effective ${key} is '${line:-unset}'; want '${want}'" || return
    fi
  done
}

# db and app stay on the compose network. A published port is allowed only on
# loopback; 0.0.0.0 bypasses ufw because Docker writes iptables itself.
assert_compose_private() {
  local file="$1" bad
  [[ -f "$file" ]] || { check_fail "missing compose file ${file}" || return; }
  bad="$(awk '
    /^  [A-Za-z0-9_-]+:[[:space:]]*$/ {
      svc = $1
      sub(":", "", svc)
      inports = 0
      next
    }
    /^[^ #]/ && !/^  / { svc = ""; inports = 0 }
    (svc == "db" || svc == "app" || svc == "backup") && $1 == "ports:" && NF > 1 {
      print svc " " $0
      next
    }
    (svc == "db" || svc == "app" || svc == "backup") && $1 == "ports:" { inports = 1; next }
    inports && /^[[:space:]]+- / {
      line = $0
      gsub(/["'\''[:space:]]/, "", line)
      sub(/^-/, "", line)
      if (line ~ /^127\.0\.0\.1:/ || line ~ /^\[::1\]:/) next
      print svc " " line
      next
    }
    inports { inports = 0 }
  ' "$file")"
  if [[ -n "$bad" ]]; then
    check_fail "compose publishes ${bad//$'\n'/; } for Postgres, the app or the backup service. Keep them on the compose network, or bind 127.0.0.1 only. A public Docker port bypasses ufw." || return
  fi
}

assert_trusted_proxy() {
  local compose="$1" envfile="$2" subnet effective
  [[ -f "$compose" ]] || { check_fail "missing compose file ${compose}" || return; }
  subnet="$(awk '$1 == "-" && $2 == "subnet:" { print $3; exit }' "$compose")"
  [[ -n "$subnet" ]] || { check_fail "compose file ${compose} has no subnet" || return; }
  effective=""
  if [[ -f "$envfile" ]]; then
    effective="$(env_value "$envfile" FOLD_TRUSTED_PROXIES || true)"
  fi
  if [[ -z "$effective" ]]; then
    effective="$(sed -n 's/.*FOLD_TRUSTED_PROXIES:-\([0-9A-Fa-f.:/][0-9A-Fa-f.:/]*\).*/\1/p' "$compose" | head -n 1)"
  fi
  [[ -n "$effective" ]] || { check_fail "could not read FOLD_TRUSTED_PROXIES from ${envfile} or ${compose}" || return; }
  if [[ "$effective" != "$subnet" ]]; then
    check_fail "FOLD_TRUSTED_PROXIES is ${effective} but the compose subnet is ${subnet}. Change them together." || return
  fi
}

assert_ufw_active() {
  local status
  if ! command -v ufw >/dev/null 2>&1; then
    check_fail "ufw is not installed. Install it and allow only 22, 80, and 443." || return
  fi
  status="$(ufw status 2>/dev/null || true)"
  if [[ "$status" != *"Status: active"* ]]; then
    check_fail "ufw is not active. Enable it for 22, 80, and 443. Docker-published ports still bypass ufw; smoke rejects a public 5432 or 8080." || return
  fi
}

# publishers_are_public TEXT — true when any mapping is not loopback.
publishers_are_public() {
  local pubs="$1" part
  [[ -n "$pubs" ]] || return 1
  local -a parts=()
  IFS=',' read -r -a parts <<<"$pubs"
  for part in "${parts[@]}"; do
    part="${part#"${part%%[![:space:]]*}"}"
    part="${part%"${part##*[![:space:]]}"}"
    [[ -n "$part" ]] || continue
    case "$part" in
      127.0.0.1:*|'[::1]:'*) ;;
      *) return 0 ;;
    esac
  done
  return 1
}

public_port_open() {
  local pubs="$1" port="$2" part hostport
  [[ -n "$pubs" ]] || return 1
  local -a parts=()
  IFS=',' read -r -a parts <<<"$pubs"
  for part in "${parts[@]}"; do
    part="${part#"${part%%[![:space:]]*}"}"
    hostport="${part%%->*}"
    case "$hostport" in
      *":${port}")
        case "$hostport" in
          127.0.0.1:*|'[::1]:'*) ;;
          *) return 0 ;;
        esac
        ;;
    esac
  done
  return 1
}

assert_published_ports() {
  local deploy_dir="$1" ps line svc rest pubs
  if ! ps="$(compose_in "$deploy_dir" ps --format '{{.Service}}|{{.Status}}|{{.Publishers}}')"; then
    check_fail "docker compose ps failed in ${deploy_dir}; cannot verify that 5432 and 8080 are private" || return
  fi
  [[ -n "$ps" ]] || { check_fail "no containers in ${deploy_dir}" || return; }
  while IFS= read -r line; do
    [[ -n "$line" ]] || continue
    svc="${line%%|*}"
    rest="${line#*|}"
    pubs="${rest#*|}"
    if [[ "$svc" == "db" || "$svc" == "app" || "$svc" == "backup" ]]; then
      if publishers_are_public "$pubs"; then
        check_fail "${svc} publishes '${pubs}'. Docker published ports bypass ufw. Keep Postgres and the app on the compose network (127.0.0.1 only if a local client needs them)." || return
      fi
    fi
    if public_port_open "$pubs" 5432 || public_port_open "$pubs" 8080; then
      check_fail "port 5432 or 8080 is published on a public address (${svc}: ${pubs}). Docker published ports bypass ufw." || return
    fi
  done <<<"$ps"
  probe_closed_ports || return
}

probe_closed_ports() {
  local hosts host port
  local -a host_arr=()
  hosts=""
  if command -v hostname >/dev/null 2>&1; then
    hosts="$(hostname -I 2>/dev/null || true)"
  fi
  if [[ -z "${hosts// }" ]] && command -v ip >/dev/null 2>&1; then
    hosts="$(ip -4 -o addr show scope global 2>/dev/null | awk '{print $4}' | cut -d/ -f1 | tr '\n' ' ' || true)"
  fi
  if [[ -z "${hosts// }" ]]; then
    echo "check: no host address to probe; the Docker published-port list was checked"
    return 0
  fi
  read -r -a host_arr <<<"$hosts"
  for host in "${host_arr[@]}"; do
    case "$host" in
      127.*|::1|*:*) continue ;;
    esac
    for port in 5432 8080; do
      if timeout 3 bash -c "echo >/dev/tcp/${host}/${port}" >/dev/null 2>&1; then
        check_fail "port ${port} on ${host} accepted a connection. Postgres and the app must not be reachable outside the compose network. Docker published ports bypass ufw." || return
      fi
    done
  done
}

# Levels are small non-negative decimal integers. Bound them before shell
# arithmetic so malformed or overflowing metadata can never weaken a floor.
parse_recovery_level() {
  local value="$1"
  [[ "$value" =~ ^(0|[1-9][0-9]{0,8})$ ]] || {
    check_fail "unparsable recovery policy level '${value}'"; return 1;
  }
  printf '%s\n' "$value"
}

# Preserve line endings across command substitution. A metadata file may have
# one terminating newline; an extra line is malformed, not legacy level zero.
read_recovery_level() {
  local value
  value="$("$@" && printf .)" || return 1
  value="${value%.}"
  parse_recovery_level "${value%$'\n'}"
}

image_recovery_level() {
  local json level
  command -v jq >/dev/null || { check_fail "jq is required to read recoveryPolicy"; return 1; }
  json="$(docker run --rm --network none --pull never --entrypoint /fold "$1" version --json)" || {
    check_fail "cannot read recoveryPolicy from image $1"; return 1;
  }
  level="$(jq -ers '
    if length != 1 or (.[0] | type) != "object" then error("expected one object")
    else .[0] | if has("recoveryPolicy") then .recoveryPolicy else 0 end
    | if type == "number" and . >= 0 and . == floor then tostring
      else error("expected integer recoveryPolicy") end end' <<<"$json")" || {
    check_fail "unparsable recoveryPolicy in image $1"; return 1;
  }
  parse_recovery_level "$level"
}

commit_recovery_level() {
  local source="$1" sha="$2" paths
  # ls-tree distinguishes a missing file (legacy level zero) from a broken
  # object/repository. Do not turn an arbitrary git failure into level zero.
  paths="$(git -C "$source" ls-tree "$sha" -- deploy/recovery-policy)" || return 1
  if [[ -z "$paths" ]]; then
    printf '0\n'
    return 0
  fi
  read_recovery_level git -C "$source" show "$sha:deploy/recovery-policy"
}

recovery_floor_file() {
  local dir="$1"
  if [[ -e "$dir/recovery-policy-floor" ]]; then
    read_recovery_level cat "$dir/recovery-policy-floor" || {
      check_fail "cannot read ${dir}/recovery-policy-floor; repair its integer value from deployment records, never lower it"; return 1;
    }
  else
    printf '0\n'
  fi
}

host_recovery_floor() {
  local dir="$1" floor image image_id level ids
  floor="$(recovery_floor_file "$dir")" || return 1
  if [[ -f "$dir/docker-compose.yml" && -f "$dir/.env" ]]; then
    # Compose resolves both explicit image pins and build-mode labels. Inspect
    # locally: pulling here could silently change the currently deployed pin.
    ids="$(compose_in "$dir" config --format json)" || {
      check_fail "cannot resolve the deployed pin in ${dir}/docker-compose.yml and ${dir}/.env. Repair Compose configuration, then retry; the recovery floor was not verified"; return 1;
    }
    image="$(jq -er '.services.app.image | select(type == "string" and length > 0)' <<<"$ids")" || {
      check_fail "no valid app image pin in Compose configuration for ${dir}; repair services.app.image, then retry"; return 1;
    }
    image_id="$(docker image inspect --format '{{.Id}}' "$image")" || {
      check_fail "cannot inspect deployed pin ${image} in ${dir}. Recover that exact image in the local store: pull the pinned release, or rebuild the pinned source commit with its recorded build label. Then retry. Do not change the pin or lower the floor to bypass this check"; return 1;
    }
    level="$(image_recovery_level "$image_id")" || {
      check_fail "cannot read recovery level of deployed pin ${image} (${image_id}) in ${dir}; repair or recover that pinned image, then retry"; return 1;
    }
    (( level <= floor )) || floor="$level"
  fi
  printf '%s\n' "$floor"
}

# A dry run never pulls or executes an image, even one already local. The
# persisted floor is a lower bound; a failed smoke may have left a higher pin.
dry_run_recovery_check() {
  local dir="$1" mode="$2" source="$3" sha="$4" floor level
  floor="$(recovery_floor_file "$dir")" || return 1
  echo "dry-run: recorded host recovery floor ${floor} (absent file means 0)"
  if [[ -f "$dir/docker-compose.yml" && -f "$dir/.env" ]]; then
    echo "dry-run: full host floor is unverified; the pinned image may require a higher level. A real run inspects and executes that local image before any release change"
  fi
  if [[ "$mode" == image ]]; then
    echo "dry-run: target image recovery level is unverified; a real run needs a pull and a networkless local version read before backup, checkout or pin changes"
    return 0
  fi
  if ! git -C "$source" rev-parse --verify "${sha}^{commit}" >/dev/null 2>&1; then
    echo "dry-run: target commit ${sha} recovery level is unverified; restore the git checkout ${source} before a real run"
    return 0
  fi
  level="$(commit_recovery_level "$source" "$sha")" || return 1
  echo "dry-run: target commit recovery level ${level}; compare with recorded host floor ${floor}"
  require_recovery_level "$level" "$floor" || return 1
}

require_recovery_level() {
  local target="$1" floor="$2" missing=""
  if (( target < floor )); then
    if (( target < 1 && floor >= 1 )); then missing="the restore barrier (level 1)"; fi
    if (( target < 2 && floor >= 2 )); then missing="${missing:+$missing and }reason review (level 2)"; fi
    if (( floor > 2 )); then missing="${missing:+$missing and }the protection required by level $floor"; fi
    check_fail "target recovery level ${target} is below host level ${floor}. The older release lacks ${missing}. Restore data with the current release; use restore.sh --dump once installed."
    return 1
  fi
}

write_recovery_floor() {
  local dir="$1" level old=0 tmp
  level="$(parse_recovery_level "$2")" || return 1
  if [[ -e "$dir/recovery-policy-floor" ]]; then
    old="$(read_recovery_level cat "$dir/recovery-policy-floor")" || return 1
    (( level > old )) || level="$old"
  fi
  [[ "$(id -u)" == 0 ]] || { check_fail "writing the recovery floor requires root"; return 1; }
  tmp="$(mktemp "$dir/.recovery-policy-floor.XXXXXX")" || return 1
  if ! { printf '%s\n' "$level" >"$tmp" && chown 0:0 "$tmp" && chmod 644 "$tmp" \
    && sync -f "$tmp" && mv -f "$tmp" "$dir/recovery-policy-floor" && sync -f "$dir"; }; then
    rm -f "$tmp"
    return 1
  fi
}

# fd 9 stays open through exec and child provision/restore invocations. Never
# unlink ops.lock: another inode would permit a second maintenance operation.
ops_lock_acquire() {
  local dir="$1" mode="$2" path="$1/ops.lock" owner tmp
  [[ ! -L "$path" && ( ! -e "$path" || -f "$path" ) ]] || {
    check_fail "ops.lock must be a regular file, never a symlink"; return 1;
  }
  if [[ ! -e /proc/self/fd/9 ]] \
    || [[ "$(stat -Lc '%d:%i' /proc/self/fd/9)" != "$(stat -Lc '%d:%i' "$path" 2>/dev/null || true)" ]]; then
    local saved_umask
    saved_umask="$(umask)"
    umask 077
    exec 9>>"$path" || { umask "$saved_umask"; return 1; }
    umask "$saved_umask"
  fi
  if ! flock -n -x 9; then
    owner="$(cat "$dir/ops.owner" 2>/dev/null || true)"
    printf 'maintenance busy: %s. Nothing changed.\n' "${owner:-holder unknown}" >&2
    printf 'If that pid has exited, inspect surviving lock holders with: sudo fuser -v %q. Stop only a verified abandoned operation or helper, then retry. Never delete ops.lock.\n' "$path" >&2
    return 75
  fi
  chown 0:0 "$path" && chmod 600 "$path" || return 1
  tmp="$(mktemp "$dir/.ops.owner.XXXXXX")" || return 1
  printf 'pid=%s mode=%s start=%s\n' "$$" "$mode" "$(date -u +%FT%TZ)" >"$tmp" || { rm -f "$tmp"; return 1; }
  if ! { chmod 600 "$tmp" && chown 0:0 "$tmp" && mv -f "$tmp" "$dir/ops.owner"; }; then
    rm -f "$tmp"
    return 1
  fi
}

# The version orders enforcing tool protocols, not release/image versions.
# Missing metadata is the first RP tool set, version 0. Never source old code
# merely to read its version.
deploy_tools_version() {
  if [[ -e "$1/deploy-tools-version" ]]; then
    read_recovery_level cat "$1/deploy-tools-version"
  else
    printf '0\n'
  fi
}

require_deploy_tools_version() {
  local source="$1" dir="$2" incoming installed path
  incoming="$(deploy_tools_version "$source")" || return 1
  for path in "$dir" "$dir/deploy-tools" "$dir/recovery-tools"; do
    installed="$(deploy_tools_version "$path")" || return 1
    if (( incoming < installed )); then
      check_fail "tool-set version ${incoming} is older than installed version ${installed} in ${path}. Run the newer ${path}/upgrade.sh or the newer provision.sh; no older tools were installed"; return 1
    fi
  done
}

# The two directory renames cannot be atomic together. Under the operations
# lock, restore the sole saved complete set after interruption. Never discard
# the only surviving set, or guess between multiple backups.
deploy_tools_complete() {
  local name
  [[ -d "$1" && ! -L "$1" ]] || return 1
  for name in upgrade.sh provision.sh checks.sh backup.sh smoke.sh email-dns.sh backup.env.example; do
    [[ -f "$1/$name" && ! -L "$1/$name" ]] || return 1
  done
}

recover_enforcing_tools() {
  local dir="$1" path
  local -a backups=()
  [[ ! -L "$dir/deploy-tools" ]] || { check_fail "deploy-tools must not be a symlink"; return 1; }
  if [[ -e "$dir/deploy-tools" ]] && ! deploy_tools_complete "$dir/deploy-tools"; then
    check_fail "incomplete deploy-tools in ${dir}; keep all saved tool sets and restore the verified current set before retrying"; return 1
  fi
  if [[ ! -e "$dir/deploy-tools" ]]; then
    for path in "$dir"/.old-deploy-tools.*; do
      [[ -e "$path" ]] || continue
      backups+=("$path")
    done
    if (( ${#backups[@]} > 0 )); then
      if (( ${#backups[@]} != 1 )) || ! deploy_tools_complete "${backups[0]}"; then
        check_fail "interrupted tool swap in ${dir}: cannot identify one complete saved set. Keep .old-deploy-tools.* and restore the verified current set to deploy-tools before retrying"; return 1
      fi
      mv "${backups[0]}" "$dir/deploy-tools" || {
        check_fail "cannot restore saved tools ${backups[0]} to ${dir}/deploy-tools; repair directory permissions and retry"; return 1;
      }
      echo "upgrade.sh: recovered enforcing tools after interrupted swap" >&2
    fi
  fi
}

validate_deploy_drill() {
  local dir="$1"
  if [[ "${DEPLOY_DRILL:-}" == 1 ]]; then
    # Resolve aliases even for --dry-run, which does not canonicalize DIR.
    dir="$(realpath -m "$dir")" || return 1
    if [[ "$dir" == /opt/fold || "$dir" == /opt/fold/* ]]; then
      check_fail "DEPLOY_DRILL refuses /opt/fold and its children"; return 1
    fi
    if [[ -L "$dir/.deploy-drill" || ! -f "$dir/.deploy-drill" ]] \
      || [[ "$(stat -c '%u:%a' "$dir/.deploy-drill" 2>/dev/null)" != 0:600 ]] \
      || [[ "$(cat "$dir/.deploy-drill" 2>/dev/null)" != "fold-deploy-drill-v1:$dir" ]]; then
      check_fail "DEPLOY_DRILL is test only and requires the root-owned mode-600 .deploy-drill marker created by deploy-drill.sh for this directory. Unset DEPLOY_DRILL for a real deployment"; return 1
    fi
    if [[ -e "$dir/backup.env" || -L "$dir/backup.env" ]]; then
      check_fail "DEPLOY_DRILL refuses a directory with backup.env; unset DEPLOY_DRILL for a real deployment"; return 1
    fi
  fi
  [[ -n "${DEPLOY_DRILL_PAUSE:-}" ]] || return 0
  [[ "${DEPLOY_DRILL:-}" == 1 && "$dir" != /opt/fold ]] || {
    check_fail "DEPLOY_DRILL_PAUSE requires DEPLOY_DRILL=1 and a directory other than /opt/fold"; return 1;
  }
  case "$DEPLOY_DRILL_PAUSE" in
    after-lock|before-up) ;;
    *) check_fail "unknown DEPLOY_DRILL_PAUSE point"; return 1 ;;
  esac
}

deploy_drill_pause() {
  local dir="$1" point="$2"
  [[ "${DEPLOY_DRILL_PAUSE:-}" == "$point" ]] || return 0
  # The hook itself enforces its boundary, even if a caller omits preflight.
  validate_deploy_drill "$dir" || return 1
  [[ -p "$dir/drill.fifo" ]] || { check_fail "missing drill FIFO in $dir"; return 1; }
  # A re-exec or nested provisioner must not pause at this point again.
  unset DEPLOY_DRILL_PAUSE
  # Open read/write before publishing readiness so open cannot block. The
  # holder itself blocks, so SIGKILL cannot leave a helper retaining fd 9.
  exec 8<>"$dir/drill.fifo"
  printf '%s\n' "$point" >"$dir/drill.ready.tmp"
  mv "$dir/drill.ready.tmp" "$dir/drill.ready"
  local token=""
  if ! IFS= read -r -t 180 token <&8; then
    exec 8>&-
    check_fail "drill barrier $point timed out or closed"; return 1
  fi
  exec 8>&-
  [[ "$token" == continue ]] || { check_fail "drill barrier cancelled"; return 1; }
}

# All installed tools move together, including the recovery Compose overlay.
runtime_tools() {
  printf '%s\n' backup.sh smoke.sh email-dns.sh upgrade.sh provision.sh checks.sh backup.env.example restore.sh compose.recovery.yml deploy-tools-version
}

inherited_ops_lock() {
  [[ -f "$1/ops.lock" && ! -L "$1/ops.lock" && -e /proc/self/fd/9 ]] \
    && [[ "$(stat -Lc '%d:%i' /proc/self/fd/9)" == "$(stat -Lc '%d:%i' "$1/ops.lock")" ]] \
    && flock -n -x 9
}

# Pure identity classifier. Empty original means a missing original database.
# Q+ is accepted only before an oid has been recorded for this nonce's R.
recovery_classify() {
  local original="$1" restored="$2" fold="$3" r="$4" p="$5"
  if [[ "$fold" == "$original" && -z "$p" ]]; then
    if [[ -z "$r" ]]; then echo Q; return; fi
    if [[ -z "$restored" ]]; then echo Q+; return; fi
    if [[ "$r" == "$restored" ]]; then echo B; return; fi
  fi
  if [[ -n "$restored" && "$fold" == "$restored" && -z "$r" ]]; then
    if [[ -n "$original" && "$p" == "$original" ]]; then echo A; return; fi
    if [[ -z "$p" ]]; then
      # With no original, A and C have identical identities. Cleanup decides.
      echo C; return
    fi
  fi
  if [[ -n "$original" && -z "$fold" && "$p" == "$original" && -n "$restored" && "$r" == "$restored" ]]; then echo H; return; fi
  echo invalid
}

# Keep this table independent of Docker and SQL so fixtures can exhaust it.
# no_original admits C where A is otherwise required, since there is no P.
recovery_accepts() {
  local phase="$1" observation="$2" cleanup="$3" at="${4:-}" no_original="${5:-0}" drop_intent="${6:-0}"
  case "$phase" in
    quiesce|create-intent) [[ "$observation" == Q || "$observation" == Q+ ]] ;;
    created) [[ "$observation" == B ]] ;;
    collect-drop-intent) [[ "$observation" == B || "$observation" == Q ]] ;;
    collect-failed) [[ "$observation" == Q ]] ;;
    collected|pin-intent|pinned|pin-failed|swap-failed) [[ "$observation" == B ]] ;;
    swap-intent|swap-1) [[ "$observation" == B || "$observation" == H || "$observation" == A || ( "$no_original" == 1 && "$observation" == C ) ]] ;;
    pre-drop-intent) [[ "$cleanup" == drop-intent && ( "$observation" == A || "$observation" == C ) ]] ;;
    pre-dropped|floor-written) [[ "$cleanup" == dropped && "$observation" == C ]] ;;
    restored|recovery-intent|recovering|recovery-failed|reopen-closed|reopen-intent|app-start-intent|caddy-start-intent|smoke-intent|reopen-failed|cleanup-failed)
      case "$cleanup" in
        none) [[ "$observation" == A || ( "$no_original" == 1 && "$observation" == C ) ]] ;;
        drop-intent) [[ "$observation" == A || "$observation" == C ]] ;;
        dropped) [[ "$observation" == C ]] ;;
        *) return 1 ;;
      esac ;;
    abort-intent) [[ "$observation" == "$at" ]] ;;
    abort-drop-intent) [[ "$observation" == B || "$observation" == Q+ || ( "$observation" == Q && "$drop_intent" == 1 ) ]] ;;
    abort-dropped|abort-start-intent|abort-smoke-intent) [[ "$observation" == Q ]] ;;
    abort-failed)
      if [[ "$at" == drop ]]; then
        [[ "$observation" == B || "$observation" == Q+ || ( "$observation" == Q && "$drop_intent" == 1 ) ]]
      else [[ ( "$at" == start || "$at" == smoke || "$at" == uploads ) && "$observation" == Q ]]; fi ;;
    *) return 1 ;;
  esac
}

recovery_abort_allowed() {
  [[ "$2" == Q || "$2" == Q+ || "$2" == B ]] || return 1
  [[ "$3" == previous ]] || return 1
  case "$1" in
    quiesce|create-intent|created|collect-drop-intent|collect-failed|collected|pin-intent|pin-failed|swap-failed|swap-intent) return 0 ;;
    *) return 1 ;;
  esac
}

# A separate inode coordinates backup/expiry with publication of recovery.lock.
# Backup never takes ops.lock. Lock order for restore/upgrade is ops (fd 9),
# then backup (fd 6); a standalone backup takes only fd 6. Never replace it.
#
# The container backup service locks the same inode through a read-only bind of
# this directory, as uid 65532. Every host acquisition therefore leaves the file
# root-owned, group 65532, mode 640 (read for the group is all flock needs), and
# upgrades an older mode-600 file in place. Nothing else in the directory
# changes mode: .env, backup.env, ops.lock, recovery.lock and release records
# stay root-only.
BACKUP_COORD_GID=65532
backup_coord_open() {
  local dir="$1" path="$1/backup.lock" saved_umask
  [[ -d "$dir" && ! -L "$path" && ( ! -e "$path" || -f "$path" ) ]] || {
    check_fail "backup coordination requires an existing deployment and a regular backup.lock"; return 1;
  }
  if [[ ! -e /proc/self/fd/6 ]] \
    || [[ "$(stat -Lc '%d:%i' /proc/self/fd/6)" != "$(stat -Lc '%d:%i' "$path" 2>/dev/null || true)" ]]; then
    saved_umask="$(umask)"; umask 077
    exec 6>>"$path" || { umask "$saved_umask"; return 1; }
    umask "$saved_umask"
  fi
  # chown first: the group may read the file only once its mode allows it.
  if ! { chown "0:${BACKUP_COORD_GID}" "$path" && chmod 640 "$path"; }; then
    check_fail "could not set backup.lock to root:${BACKUP_COORD_GID} mode 640; run as root"; return 1
  fi
}

backup_coord_acquire() {
  backup_coord_open "$1" || return 1
  if ! flock -x -w "${2:-0}" 6; then
    case "${3:-maintenance}" in
      backup) check_fail "backup.lock is held by another operation (backup, restore preflight or provision file copy); this backup was skipped. No dump, restore test, expiry or offsite upload was attempted. Rerun backup.sh later; never delete backup.lock" ;;
      restore) check_fail "backup coordination busy; no recovery lock published and no services stopped. Let the existing backup finish, then retry. Never delete backup.lock" ;;
      *) check_fail "backup coordination busy; another operation holds backup.lock. Runtime files were not copied. Retry later; never delete backup.lock" ;;
    esac
    return 75
  fi
}

backup_coord_release() {
  flock -u 6 && exec 6>&-
}

# --- Uploads restore capability (M5.2) ---------------------------------------
# Mirrors the recovery policy readers: restoreUploads is an integer in
# `fold version --json`, or the one-line deploy/restore-uploads at a source
# commit. Absent means 0; malformed metadata refuses.
image_restore_uploads() {
  local json level
  command -v jq >/dev/null || { check_fail "jq is required to read restoreUploads"; return 1; }
  json="$(docker run --rm --network none --pull never --entrypoint /fold "$1" version --json)" || {
    check_fail "cannot read restoreUploads from image $1"; return 1;
  }
  level="$(jq -ers '
    if length != 1 or (.[0] | type) != "object" then error("expected one object")
    else .[0] | if has("restoreUploads") then .restoreUploads else 0 end
    | if type == "number" and . >= 0 and . == floor then tostring
      else error("expected integer restoreUploads") end end' <<<"$json")" || {
    check_fail "unparsable restoreUploads in image $1"; return 1;
  }
  parse_recovery_level "$level"
}

commit_restore_uploads() {
  local source="$1" sha="$2" paths
  paths="$(git -C "$source" ls-tree "$sha" -- deploy/restore-uploads)" || return 1
  if [[ -z "$paths" ]]; then
    printf '0\n'
    return 0
  fi
  read_recovery_level git -C "$source" show "$sha:deploy/restore-uploads"
}

# --- Backup service and scheduler (M5.2) -------------------------------------
# backup_service_defined DIR: 0 the deployed Compose defines a backup service,
# 1 it does not, 2 the configuration could not be read. An unreadable
# configuration is never the same as an absent service.
backup_service_defined() {
  local config
  config="$(observe_compose "$1" config --format json 2>/dev/null)" || return 2
  jq -e '.services | type == "object"' <<<"$config" >/dev/null 2>&1 || return 2
  if jq -e '.services.backup != null' <<<"$config" >/dev/null 2>&1; then return 0; fi
  return 1
}

# backup_service_running DIR: for a deployed Compose that defines the backup service
# (the caller knows), 0 when its container is running, 1 when none is, 2 when that
# could not be observed. Bounded by FOLD_BACKUP_DOCKER_WAIT and any ARCHIVE_CAP.
# The host backup and the cutover share this one answer, so only one of the two
# producers ever makes the backups: the host refuses while this says running, and
# a failed cutover stops the service before it calls the host the producer.
backup_service_running() {
  local dir="$1" ids id state
  archive_deadline "${FOLD_BACKUP_DOCKER_WAIT:-60}"
  ids="$(archive_compose "$dir" ps -q backup 2>/dev/null)" || return 2
  [[ -n "$ids" ]] || return 1
  while IFS= read -r id; do
    [[ -n "$id" ]] || continue
    archive_deadline "${FOLD_BACKUP_DOCKER_WAIT:-60}"
    state="$(archive_docker inspect --format '{{.State.Running}}' "$id" 2>/dev/null)" || return 2
    case "$state" in
      true) return 0 ;;
      false) ;;
      *) return 2 ;;
    esac
  done <<<"$ids"
  return 1
}

# backup_service_halt DIR: stop the container backup service through Compose and
# verify that none is running, within its own 30 seconds (the cutover's budget may
# be spent). Success means the host is the only producer. The cutover's failure path
# is the one caller; a stopped service stays stopped across a reboot (restart:
# unless-stopped) until a managed start.
backup_service_halt() {
  local dir="$1" rc=0
  local ARCHIVE_CAP=$((SECONDS + 30))
  archive_deadline 30
  archive_compose "$dir" stop backup >/dev/null 2>&1 || return 1
  backup_service_running "$dir" || rc=$?
  [[ "$rc" == 1 ]]
}

# backup_coordination_check DIR: the backup service in the resolved Compose
# configuration shares this directory's backup.lock and recovery.lock: its
# FOLD_RECOVERY_DIR is /coord, over a read-only bind of DIR, and the lock and the
# directory are what uid 65532 needs to open and search them. A service that
# cannot see the lock would run a backup straight through a restore.
backup_coordination_check() {
  local dir="$1" real config lock mode
  real="$(cd "$dir" && pwd -P)" || return 1
  config="$(observe_compose "$dir" config --format json 2>/dev/null)" || {
    check_fail "cannot resolve the Compose configuration in ${dir}; the backup coordination was not verified"; return 1;
  }
  if ! jq -e --arg dir "$dir" --arg real "$real" '
    .services.backup as $b
    | $b != null
    and (($b.environment // {}).FOLD_RECOVERY_DIR == "/coord")
    and ([($b.volumes // [])[] | select(.target == "/coord")] | length == 1)
    and all(($b.volumes // [])[] | select(.target == "/coord");
      .type == "bind" and (.read_only == true) and (.source == $dir or .source == $real))' <<<"$config" >/dev/null 2>&1; then
    check_fail "the backup service is not coordinated with ${dir}: FOLD_RECOVERY_DIR must be /coord over a read-only bind of this directory. Run the current ${dir}/provision.sh --deploy-dir ${dir}, which writes FOLD_BACKUP_COORD to .env; no service was stopped or started"
    return 1
  fi
  lock="$dir/backup.lock"
  [[ -f "$lock" && ! -L "$lock" && "$(stat -c '%u:%g:%a' "$lock")" == "0:${BACKUP_COORD_GID}:640" ]] || {
    check_fail "${lock} is not root-owned, group ${BACKUP_COORD_GID}, mode 640, so the backup service cannot lock it. Run the current provision.sh --deploy-dir ${dir}"; return 1;
  }
  mode="$(stat -c '%a' "$dir")"
  (( 8#$mode & 1 )) || {
    check_fail "${dir} (mode ${mode}) cannot be searched by the backup service, so it cannot see recovery.lock. Run the current provision.sh --deploy-dir ${dir}"; return 1;
  }
}

# The host cron file. A disposable drill keeps it inside its own directory and
# never touches the machine's /etc/cron.d.
backup_cron_dir() {
  if [[ "${DEPLOY_DRILL:-}" == 1 ]]; then printf '%s/.drill-cron\n' "$1"; else printf '/etc/cron.d\n'; fi
}

# backup_cron_text DIR full|expire: the text of /etc/cron.d/fold-backup. The
# full cron runs the host backup; the expire-only cron is what replaces it once
# the container backup service has proven itself (cutover), and only expires.
backup_cron_text() {
  local dir="$1" kind="$2"
  printf 'SHELL=/bin/bash\nPATH=/usr/local/sbin:/usr/local/bin:/sbin:/bin:/usr/sbin:/usr/bin\n'
  case "$kind" in
    full)
      printf '# 02:30 in the droplet timezone (timedatectl). Local dumps plus optional offsite.\n'
      printf '30 2 * * * root %s/backup.sh >> /var/log/fold-backup.log 2>&1\n' "$dir" ;;
    expire)
      printf '# 02:30 in the droplet timezone (timedatectl). Container backups run in the backup service; this only expires the older host archives.\n'
      printf '30 2 * * * root %s/backup.sh --expire-only >> /var/log/fold-backup.log 2>&1\n' "$dir" ;;
    *) return 1 ;;
  esac
}

# backup_cron_install DIR full|expire: publish the cron file durably: staged under
# a temporary name, synchronized, renamed over the old file (mode 644) and the
# directory synchronized. An identical file already in place is synchronized, with
# its directory, before the install is confirmed, so a success means the file
# survives a power loss. Any step that fails is a failure: the caller must not
# call the producer confirmed.
backup_cron_install() {
  local dir="$1" kind="$2" cron_dir tmp file
  cron_dir="$(backup_cron_dir "$dir")"
  file="$cron_dir/fold-backup"
  mkdir -p "$cron_dir" || return 1
  tmp="$(mktemp "$cron_dir/.tmp.XXXXXX")" || return 1
  if ! { backup_cron_text "$dir" "$kind" >"$tmp" && chmod 644 "$tmp"; }; then rm -f "$tmp"; return 1; fi
  if [[ -f "$file" && ! -L "$file" ]] && cmp -s "$tmp" "$file"; then
    rm -f "$tmp"
    sync -- "$file" && sync -- "$cron_dir"
    return
  fi
  sync -- "$tmp" || { rm -f "$tmp"; return 1; }
  mv -f "$tmp" "$file" || { rm -f "$tmp"; return 1; }
  sync -- "$cron_dir"
}

# backup_cron_kind DIR: full, expire or none, from the installed cron file.
backup_cron_kind() {
  local file
  file="$(backup_cron_dir "$1")/fold-backup"
  [[ -f "$file" ]] || { echo none; return 0; }
  if grep -q -- '--expire-only' "$file"; then echo expire; else echo full; fi
}

# backup_scheduler_select MODE DIR: choose and arm the one scheduler that makes
# backups after a recovery or a cutover. The modes are explicit.
#   recovery        Called once a reopen or abort has passed its smoke and
#                   cleanup. When the deployed (possibly pinned) Compose defines
#                   a backup service, check its coordination again on that
#                   resolved configuration (and its identity and paths: exactly
#                   65532:65532 with cap_drop ALL, no added capability or
#                   privilege, the shipped archive, status and uploads mounts)
#                   and the protection of the existing backups volume, then
#                   start it without pulling; when it
#                   does not, install the guarded host full-backup cron. Never
#                   replaces an existing host full cron with expire-only, and an
#                   uncoordinated service is never started.
#   cutover-success Called by provision only after the container backup has run
#                   successfully. Replaces the host full cron with the
#                   expire-only cron at the same time. Never used by a recovery.
# The scheduler may run while recovery.lock still exists, but a backup refuses
# to do any work under that lock.
backup_scheduler_select() {
  local mode="$1" dir="$2" rc=0 id state
  backup_service_defined "$dir" || rc=$?
  case "$mode" in
    recovery)
      case "$rc" in
        0)
          backup_coordination_check "$dir" || return 1
          backup_identity_check "$dir" || return 1
          backup_volume_check "$dir" || return 1
          compose_in "$dir" up -d --no-deps --pull never backup || {
            check_fail "could not start the backup service from the local image; recovery state kept for a retry"; return 1;
          }
          id="$(compose_in "$dir" ps -q backup)" || return 1
          [[ -n "$id" && "$id" != *$'\n'* ]] || { check_fail "expected one backup container"; return 1; }
          state="$(docker inspect --format '{{.State.Running}}' "$id")" || return 1
          [[ "$state" == true ]] || { check_fail "the backup service is not running after it was started"; return 1; }
          echo "scheduler: container backup service" ;;
        1)
          backup_cron_install "$dir" full || { check_fail "could not install the host backup cron"; return 1; }
          echo "scheduler: host backup cron" ;;
        *) check_fail "cannot read the Compose configuration to choose the backup scheduler"; return 1 ;;
      esac ;;
    cutover-success)
      [[ "$rc" == 0 ]] || { check_fail "no backup service is defined; the host full-backup cron stays"; return 1; }
      backup_cron_install "$dir" expire || { check_fail "could not install the expire-only cron"; return 1; }
      echo "scheduler: container backup service; host cron expires only" ;;
    *) check_fail "unknown backup scheduler mode"; return 1 ;;
  esac
}

# --- Host archives, adopted copies and their expiry (M5.2) -------------------
# The host keeps two sets of archives that the container service cannot touch:
# its original archives (/var/backups/fold) and, in adopted/ at the root of the
# Compose backups volume, a copy of each. adopted/ is root:root mode 0700 and
# holds root-owned 0600 files; the service (uid 65532) runs with every
# capability dropped and cannot open it. The volume root is root:65532 mode
# 1770: the service writes its own top-level archives and cannot remove or
# replace what root keeps there.
#
# Everything below runs under backup.lock (after ops.lock, when the caller holds
# it) and rechecks recovery.lock. Resolution only observes Docker; the one
# managed creation of the volume is provision.sh's `up --no-start --no-deps
# backup`. A copy keeps its original's bytes and exact mtime, so one retention
# decision dates a pair the same on both sides.

ARCHIVE_PENDING=backup-expiry-pending
ARCH_MOUNT=""
ARCH_SERVICE_DEFINED=1
ARCHIVE_DEADLINE=0

archive_note() { printf 'archive: %s\n' "$1" >&2; }

# archive_name_ok NAME: a recognized archive name; sets ARC_STAMP and ARC_DAY. Same grammar as the container's recognize().
archive_name_ok() {
  local b="$1"
  [[ "$b" =~ ^(fold-([0-9]{8})-([0-9]{4})\.dump|uploads-([0-9]{8})-([0-9]{4})\.tgz)(\.age)?$ ]] || return 1
  if [[ "$b" == fold-* ]]; then
    ARC_STAMP="${BASH_REMATCH[2]}-${BASH_REMATCH[3]}"
    ARC_DAY="${BASH_REMATCH[2]:6:2}"
  else
    ARC_STAMP="${BASH_REMATCH[4]}-${BASH_REMATCH[5]}"
    ARC_DAY="${BASH_REMATCH[4]:6:2}"
  fi
}

# --- bounded Docker observations ---------------------------------------------
# One absolute deadline covers every observation of a run (archive_deadline N).
# ARCHIVE_CAP, when set (SECONDS value), is an outer limit no deadline passes: the
# cutover sets it to its own end, so no observation outlives FOLD_CUTOVER_WAIT.
# The lock descriptors never reach Docker.
ARCHIVE_CAP=0
archive_deadline() {
  ARCHIVE_DEADLINE=$((SECONDS + $1))
  if (( ARCHIVE_CAP > 0 && ARCHIVE_DEADLINE > ARCHIVE_CAP )); then ARCHIVE_DEADLINE=$ARCHIVE_CAP; fi
}

archive_docker() {
  local left=$((ARCHIVE_DEADLINE - SECONDS))
  (( left > 0 )) || return 124
  timeout --kill-after=2 "$left" docker "$@" 6<&- 9<&-
}

archive_compose() {
  local dir="$1"
  shift
  archive_docker compose --project-directory "$dir" "$@"
}

# observe_compose DIR ARGS...: a read of the resolved Compose configuration. During
# a cutover (ARCHIVE_CAP set) it is bounded by FOLD_BACKUP_DOCKER_WAIT and by what
# is left of the cutover; otherwise it is plain compose_in, as restore.sh uses it.
observe_compose() {
  local dir="$1"
  shift
  if (( ARCHIVE_CAP > 0 )); then
    archive_deadline "${FOLD_BACKUP_DOCKER_WAIT:-60}"
    archive_compose "$dir" "$@"
  else
    compose_in "$dir" "$@"
  fi
}

# archive_path_safe PATH: every component from / down is a real directory owned
# by root that no group or other can write. The last component is only checked
# to be a real directory (a legacy volume root may still belong to uid 65532).
archive_path_safe() {
  local path="$1" acc="" part last meta
  local -a parts=()
  IFS=/ read -ra parts <<<"${path#/}"
  last=$((${#parts[@]} - 1))
  local i
  for i in "${!parts[@]}"; do
    part="${parts[$i]}"
    [[ -n "$part" && "$part" != . && "$part" != .. ]] || return 1
    acc="$acc/$part"
    [[ -d "$acc" && ! -L "$acc" ]] || return 1
    (( i == last )) && break
    meta="$(stat -c '%u %a' -- "$acc")" || return 1
    [[ "${meta%% *}" == 0 ]] || return 1
    (( 8#${meta##* } & 8#022 )) && return 1
  done
  return 0
}

# archive_volume_resolve DIR: find the configured backups volume without creating
# anything. Returns 0 (ARCH_MOUNT set), 3 the volume does not exist,
# 4 an observation failed or timed out, 5 the volume is not the one Compose
# defines (name, driver, options, labels or location). Absence is only ever a
# successful empty listing.
archive_volume_resolve() {
  local dir="$1" cfg proj vol listing info root want real
  ARCH_MOUNT=""
  ARCH_SERVICE_DEFINED=1
  cfg="$(archive_compose "$dir" config --format json 2>/dev/null)" || { archive_note "could not read the Compose configuration"; return 4; }
  jq -e '.services | type == "object"' <<<"$cfg" >/dev/null 2>&1 || { archive_note "the Compose configuration is not readable"; return 4; }
  if jq -e '.services.backup != null' <<<"$cfg" >/dev/null 2>&1; then ARCH_SERVICE_DEFINED=0; fi
  proj="$(jq -r '.name // empty' <<<"$cfg" 2>/dev/null)" || proj=""
  if [[ -z "$proj" ]]; then
    proj="$(env_value "$dir/.env" COMPOSE_PROJECT_NAME || true)"
    [[ -n "$proj" ]] || proj="$(basename "$dir" | tr '[:upper:]' '[:lower:]')"
  fi
  [[ "$proj" =~ ^[a-z0-9][a-z0-9_-]*$ ]] || { archive_note "the Compose project name is not usable"; return 5; }
  if jq -e '(.volumes.backups // {}) | ((.external // false) != false) or ((.driver // "local") != "local") or ((.driver_opts // {}) != {})' <<<"$cfg" >/dev/null 2>&1; then
    archive_note "the backups volume is external or has a driver or options; the host keeps no copies there"
    return 5
  fi
  vol="$(jq -r '.volumes.backups.name // empty' <<<"$cfg" 2>/dev/null)" || vol=""
  [[ -n "$vol" ]] || vol="${proj}_backups"
  [[ "$vol" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*$ ]] || { archive_note "the backups volume name is not usable"; return 5; }
  listing="$(archive_docker volume ls --quiet --filter "name=^${vol}\$" 2>/dev/null)" || { archive_note "could not list Docker volumes"; return 4; }
  grep -Fxq -- "$vol" <<<"$listing" || return 3
  info="$(archive_docker volume inspect --format '{{json .}}' "$vol" 2>/dev/null)" || { archive_note "could not inspect the backups volume"; return 4; }
  if ! jq -e --arg n "$vol" --arg p "$proj" '
      .Name == $n and .Driver == "local" and ((.Options // {}) == {})
      and (.Labels["com.docker.compose.project"] == $p)
      and (.Labels["com.docker.compose.volume"] == "backups")
      and (.Mountpoint | type == "string")' <<<"$info" >/dev/null 2>&1; then
    archive_note "the volume named ${vol} is not the backups volume this Compose project created"
    return 5
  fi
  root="$(archive_docker info --format '{{.DockerRootDir}}' 2>/dev/null)" || { archive_note "could not read the Docker root directory"; return 4; }
  [[ "$root" =~ ^/[A-Za-z0-9._/-]*[A-Za-z0-9._-]$ ]] || { archive_note "the Docker root directory is not usable"; return 5; }
  want="$root/volumes/$vol/_data"
  [[ "$(jq -r '.Mountpoint' <<<"$info")" == "$want" ]] || { archive_note "the backups volume is not under the Docker root directory"; return 5; }
  real="$(realpath -e -- "$want" 2>/dev/null)" || real=""
  [[ "$real" == "$want" ]] || { archive_note "the backups volume location is not canonical"; return 5; }
  archive_path_safe "$want" || { archive_note "an ancestor of the backups volume is not a root-owned, unwritable folder"; return 5; }
  ARCH_MOUNT="$want"
}

# archive_layout: validate the known volume root and adopted/, creating or
# repairing only those two. An existing adopted/ that is not exactly a root:root
# mode-0700 folder is refused and never followed. Caller holds backup.lock.
archive_layout() {
  local m="$ARCH_MOUNT" a="$ARCH_MOUNT/adopted"
  [[ -n "$m" && -d "$m" && ! -L "$m" ]] || { archive_note "the backups volume folder is missing"; return 1; }
  if [[ "$(stat -c '%u:%g:%a' -- "$m")" != "0:${BACKUP_COORD_GID}:1770" ]]; then
    if ! { chown "0:${BACKUP_COORD_GID}" -- "$m" && chmod 1770 -- "$m"; }; then
      archive_note "could not set the backups volume root to root:${BACKUP_COORD_GID} mode 1770"
      return 1
    fi
    [[ "$(stat -c '%u:%g:%a' -- "$m")" == "0:${BACKUP_COORD_GID}:1770" ]] || { archive_note "the backups volume root did not take root:${BACKUP_COORD_GID} mode 1770"; return 1; }
  fi
  if [[ -e "$a" || -L "$a" ]]; then
    if [[ -L "$a" || ! -d "$a" || "$(stat -c '%u:%g:%a' -- "$a")" != 0:0:700 ]]; then
      archive_note "adopted/ in the backups volume is not a root-only folder (root:root, mode 700); it was not followed or repaired"
      return 1
    fi
  else
    mkdir -m 700 -- "$a" || { archive_note "could not create adopted/"; return 1; }
    sync -- "$m" || return 1
  fi
}

# --- copies: no overwrite, exact bytes and mtime -----------------------------

# archive_sig FILE: "size mtime sha256", the mtime to the nanosecond.
archive_sig() {
  local meta sum
  meta="$(stat -c '%s %.9Y' -- "$1")" || return 1
  sum="$(sha256sum -- "$1")" || return 1
  printf '%s %s' "$meta" "${sum%% *}"
}

# archive_same_bytes A B: 0 equal, 1 different, 2 unreadable.
archive_same_bytes() {
  local sa sb
  sa="$(stat -c %s -- "$1")" && sb="$(stat -c %s -- "$2")" || return 2
  [[ "$sa" == "$sb" ]] || return 1
  sa="$(sha256sum -- "$1")" && sb="$(sha256sum -- "$2")" || return 2
  [[ "${sa%% *}" == "${sb%% *}" ]]
}

# archive_clear_residue DIR: remove this tool's own unfinished temporary files.
# Only a root-owned regular file named .tmp.* qualifies, and the caller holds
# backup.lock, so no writer of them is running.
archive_clear_residue() {
  find "$1" -maxdepth 1 -type f -name '.tmp.*' -user 0 -delete
}

# archive_copy SRC DEST: copy through a root-private temporary file and publish
# by a no-replace link. The source's size, hash and exact mtime are read before
# and after, the copy's bytes are checked, and the source's mtime is applied to
# the copy before it is published. The copy and sync children inherit fd 6, so a
# killed parent cannot release admission while a child can still write.
archive_copy() {
  local src="$1" dest="$2" tmp before after copy
  tmp="${dest%/*}/.tmp.${dest##*/}.$$"
  before="$(archive_sig "$src")" || return 1
  if ! { cp --no-preserve=all -- "$src" "$tmp" && chmod 600 -- "$tmp" && chown 0:0 -- "$tmp"; }; then
    rm -f -- "$tmp"
    return 1
  fi
  copy="$(archive_sig "$tmp")" || { rm -f -- "$tmp"; return 1; }
  after="$(archive_sig "$src")" || { rm -f -- "$tmp"; return 1; }
  if [[ "$after" != "$before" ]]; then
    rm -f -- "$tmp"
    archive_note "an original changed while it was being copied: ${src##*/}"
    return 1
  fi
  if [[ "$(cut -d' ' -f1,3 <<<"$copy")" != "$(cut -d' ' -f1,3 <<<"$before")" ]]; then
    rm -f -- "$tmp"
    archive_note "the copy of ${src##*/} does not match its original"
    return 1
  fi
  if ! { touch -m -d "@$(cut -d' ' -f2 <<<"$before")" -- "$tmp" && sync -- "$tmp"; }; then
    rm -f -- "$tmp"
    return 1
  fi
  if ! ln -- "$tmp" "$dest" 2>/dev/null; then
    rm -f -- "$tmp"
    archive_note "${dest##*/} appeared while it was being copied; it was not replaced"
    return 1
  fi
  rm -f -- "$tmp"
  sync -- "${dest%/*}"
}

# archive_settle SRC DEST: DEST already holds SRC's bytes. Give it SRC's exact
# mtime (after a stable-source check) and synchronize it, so an identical retry
# is on disk too. Returns 0 identical, 11 repaired, 1 failure.
archive_settle() {
  local src="$1" dest="$2" sm dm before after
  sm="$(stat -c '%.9Y' -- "$src")" && dm="$(stat -c '%.9Y' -- "$dest")" || return 1
  if [[ "$sm" == "$dm" ]]; then
    sync -- "$dest" || return 1
    return 0
  fi
  before="$(archive_sig "$src")" || return 1
  touch -m -d "@${sm}" -- "$dest" || return 1
  after="$(archive_sig "$src")" || return 1
  [[ "$after" == "$before" ]] || { archive_note "an original changed while its copy's date was repaired: ${src##*/}"; return 1; }
  sync -- "$dest" || return 1
  return 11
}

# archive_meta_ok FILE: an accepted copy is a root-owned mode-0600 regular file
# with no other link. Any other owner, mode or link could still be written through
# a descriptor or a name the host cannot see, so such a file is refused and never
# repaired in place.
archive_meta_ok() {
  [[ -f "$1" && ! -L "$1" && "$(stat -c '%u:%g:%a:%h' -- "$1")" == 0:0:600:1 ]]
}

# archive_place SRC DEST: copy SRC to DEST without replacing anything. Returns
# 0 copied or identical, 11 repaired, 3 DEST holds different bytes, 6 DEST has the
# right bytes but is not a root-only mode-0600 file, 1 failure.
archive_place() {
  local src="$1" dest="$2" rc=0
  if [[ -e "$dest" || -L "$dest" ]]; then
    [[ -f "$dest" && ! -L "$dest" ]] || return 3
    archive_same_bytes "$src" "$dest" || { rc=$?; (( rc == 1 )) && return 3; return 1; }
    archive_meta_ok "$dest" || return 6
    archive_settle "$src" "$dest" || return $?
    return 0
  fi
  archive_copy "$src" "$dest"
}

# archive_adopt SRC_DIR: copy the recognized originals into adopted/, grouped by
# stamp. Before a stamp is filled, every recognized destination sibling is
# inspected: one that differs from its original, is not a root-only mode-0600 file,
# or has no surviving original to be compared with (the original expired first)
# refuses the whole stamp. Nothing in a refused stamp changes and no missing
# sibling is filled, so a conflicting pair can never become a mixed one. I/O
# failure returns 1; a refusal is reported and is not a failure. Needs ARCH_MOUNT
# validated by archive_layout and backup.lock held.
archive_adopt() {
  local src="$1" ad="$ARCH_MOUNT/adopted" listing n stamp member bad why rc fill sib
  local copied=0 identical=0 repaired=0 refused=0
  local -A groups=()
  if [[ ! -d "$src" ]]; then
    echo "adopt: no original archives to copy"
    return 0
  fi
  [[ ! -L "$src" ]] || { archive_note "the original archive folder is a symlink"; return 1; }
  archive_clear_residue "$ad" || return 1
  listing="$(find "$src" -mindepth 1 -maxdepth 1 -printf '%f\n')" || { archive_note "could not list the original archives"; return 1; }
  while IFS= read -r n; do
    archive_name_ok "$n" || continue
    [[ -f "$src/$n" && ! -L "$src/$n" ]] || continue
    groups["$ARC_STAMP"]+="$n"$'\n'
  done <<<"$listing"
  for stamp in $(printf '%s\n' "${!groups[@]}" | sort); do
    bad="" why="" fill=0
    while IFS= read -r member; do
      [[ -n "$member" ]] || continue
      if [[ -e "$ad/$member" || -L "$ad/$member" ]]; then
        if [[ ! -f "$ad/$member" || -L "$ad/$member" ]]; then bad="$member"; why="is not a regular file"; break; fi
        rc=0
        archive_same_bytes "$src/$member" "$ad/$member" || rc=$?
        case "$rc" in
          0) ;;
          1) bad="$member"; why="differs from the original"; break ;;
          *) archive_note "could not read ${member}"; return 1 ;;
        esac
        archive_meta_ok "$ad/$member" || { bad="$member"; why="is not a root-only mode-600 file with one link"; break; }
      else
        fill=1
      fi
    done <<<"${groups[$stamp]}"
    if [[ -z "$bad" ]] && (( fill )); then
      for sib in "fold-$stamp.dump" "fold-$stamp.dump.age" "uploads-$stamp.tgz" "uploads-$stamp.tgz.age"; do
        [[ -e "$ad/$sib" || -L "$ad/$sib" ]] || continue
        [[ -f "$src/$sib" && ! -L "$src/$sib" ]] && continue
        bad="$sib"; why="has no surviving original to be compared with"
        break
      done
    fi
    if [[ -n "$bad" ]]; then
      archive_note "stamp ${stamp} was not adopted: the copy of ${bad} ${why}; nothing in that stamp changed"
      refused=$((refused + 1))
      continue
    fi
    while IFS= read -r member; do
      [[ -n "$member" ]] || continue
      rc=0
      if [[ -e "$ad/$member" ]]; then
        archive_settle "$src/$member" "$ad/$member" || rc=$?
        case "$rc" in 0) identical=$((identical + 1)) ;; 11) repaired=$((repaired + 1)) ;; *) return 1 ;; esac
      else
        archive_copy "$src/$member" "$ad/$member" || return 1
        copied=$((copied + 1))
      fi
    done <<<"${groups[$stamp]}"
  done
  sync -- "$ad" || return 1
  printf 'adopt copied=%s identical=%s repaired=%s refused=%s\n' "$copied" "$identical" "$repaired" "$refused"
}

# archive_list_adopted ORIGINALS: print each adopted stamp with explicit paths.
# verified: both plain members present and no original differs. incomplete: a
# plain member is missing. conflict: an original differs.
archive_list_adopted() {
  local orig="$1" ad="$ARCH_MOUNT/adopted" listing n stamp member state rc line
  local -A groups=()
  [[ -d "$ad" && ! -L "$ad" ]] || { echo "adopted: none"; return 0; }
  listing="$(find "$ad" -mindepth 1 -maxdepth 1 -printf '%f\n')" || { archive_note "could not list adopted/"; return 1; }
  while IFS= read -r n; do
    archive_name_ok "$n" || continue
    [[ -f "$ad/$n" && ! -L "$ad/$n" ]] || continue
    groups["$ARC_STAMP"]+="$n"$'\n'
  done <<<"$listing"
  for stamp in $(printf '%s\n' "${!groups[@]}" | sort); do
    state=verified
    line=""
    [[ -f "$ad/fold-$stamp.dump" && -f "$ad/uploads-$stamp.tgz" ]] || state=incomplete
    while IFS= read -r member; do
      [[ -n "$member" ]] || continue
      line="$line $ad/$member"
      if [[ -f "$orig/$member" && ! -L "$orig/$member" ]]; then
        rc=0
        archive_same_bytes "$orig/$member" "$ad/$member" || rc=$?
        (( rc == 0 )) || state=conflict
      fi
    done < <(printf '%s' "${groups[$stamp]}" | sort)
    printf 'adopted %s %s%s\n' "$stamp" "$state" "$line"
  done
}

# --- retention ---------------------------------------------------------------
# One predicate for every population: a member is due when its age is strictly
# greater than KEEP days; a day-01 member also stays until it is 180 days old.
# KEEP values above 180 are honored.
archive_due() {
  local keep="$1" now="$2" mtime="$3" day="$4" age
  age=$((now - mtime))
  (( age > keep * 86400 )) || return 1
  if [[ "$day" == 01 ]] && (( age < 180 * 86400 )); then return 1; fi
  return 0
}

# archive_expire_dir DIR KEEP NOW: delete the due recognized regular files at the
# top level of DIR. The whole listing and every member's metadata are collected
# before the first deletion; any failure returns 1 and the caller acknowledges
# nothing. Unknown names, symlinks and folders are untouched.
ARCH_DELETED=0
archive_expire_dir() {
  local dir="$1" keep="$2" now="$3" p nm meta rc=0
  local -a paths=() due=()
  ARCH_DELETED=0
  [[ -d "$dir" && ! -L "$dir" ]] || return 1
  mapfile -d '' -t paths < <(find "$dir" -mindepth 1 -maxdepth 1 -print0 || printf '/FIND-FAILED\0')
  for p in "${paths[@]}"; do
    [[ "$p" != /FIND-FAILED ]] || { archive_note "could not list ${dir##*/}"; return 1; }
    nm="${p##*/}"
    archive_name_ok "$nm" || continue
    meta="$(stat -c '%F|%Y' -- "$p")" || { archive_note "could not read the age of ${nm}"; return 1; }
    case "${meta%%|*}" in "regular file"|"regular empty file") ;; *) continue ;; esac
    if archive_due "$keep" "$now" "${meta##*|}" "$ARC_DAY"; then due+=("$p"); fi
  done
  for p in "${due[@]}"; do
    if rm -f -- "$p"; then
      ARCH_DELETED=$((ARCH_DELETED + 1))
      echo "expired ${p##*/}"
    else
      archive_note "could not remove ${p##*/}"
      rc=1
    fi
  done
  sync -- "$dir" || rc=1
  return "$rc"
}

# The pending file: one root-private file of decisions, "KEEP NOW ORIG COPY" per
# line, where ORIG and COPY are pending or done. A decision is written (with a
# synchronized replacement and a synchronized directory) before anything is
# deleted under it, so a crash, a Docker failure or a KEEP_DAYS change cannot
# lose the dates a side still owes.
P_KEEP=()
P_NOW=()
P_ORIG=()
P_COPY=()

archive_pending_load() {
  local dir="$1" f="$1/$ARCHIVE_PENDING" line stale
  P_KEEP=() P_NOW=() P_ORIG=() P_COPY=()
  for stale in "$dir/.${ARCHIVE_PENDING}".*; do
    [[ -f "$stale" && ! -L "$stale" ]] && rm -f -- "$stale"
  done
  [[ -e "$f" || -L "$f" ]] || return 0
  if [[ ! -f "$f" || -L "$f" || "$(stat -c '%u:%a' -- "$f")" != 0:600 ]]; then
    archive_note "${ARCHIVE_PENDING} is not a root-private regular file; expiry did nothing"
    return 1
  fi
  while IFS= read -r line || [[ -n "$line" ]]; do
    if [[ ! "$line" =~ ^([0-9]{1,3})\ ([0-9]{9,12})\ (pending|done)\ (pending|done)$ ]] \
      || (( 10#${BASH_REMATCH[1]} < 1 || 10#${BASH_REMATCH[1]} > 365 )); then
      archive_note "${ARCHIVE_PENDING} is not readable; expiry did nothing"
      return 1
    fi
    P_KEEP+=("$((10#${BASH_REMATCH[1]}))")
    P_NOW+=("${BASH_REMATCH[2]}")
    P_ORIG+=("${BASH_REMATCH[3]}")
    P_COPY+=("${BASH_REMATCH[4]}")
  done <"$f"
}

archive_pending_save() {
  local dir="$1" f="$1/$ARCHIVE_PENDING" tmp i
  if (( ${#P_KEEP[@]} == 0 )); then
    rm -f -- "$f" && sync -- "$dir"
    return
  fi
  tmp="$(mktemp "$dir/.${ARCHIVE_PENDING}.XXXXXX")" || return 1
  for i in "${!P_KEEP[@]}"; do
    printf '%s %s %s %s\n' "${P_KEEP[$i]}" "${P_NOW[$i]}" "${P_ORIG[$i]}" "${P_COPY[$i]}"
  done >"$tmp" || { rm -f -- "$tmp"; return 1; }
  if ! { chmod 600 "$tmp" && chown 0:0 "$tmp" && sync -- "$tmp" && mv -f -- "$tmp" "$f" && sync -- "$dir"; }; then
    rm -f -- "$tmp"
    return 1
  fi
}

# archive_pending_merge KEEP NOW: record the current decision. An equivalent keep
# merges by the greater NOW and, when that widens it, both sides owe again.
archive_pending_merge() {
  local keep="$1" now="$2" i
  for i in "${!P_KEEP[@]}"; do
    if [[ "${P_KEEP[$i]}" == "$keep" ]]; then
      if (( now > P_NOW[i] )); then
        P_NOW[i]="$now"
        P_ORIG[i]=pending
        P_COPY[i]=pending
      fi
      return 0
    fi
  done
  P_KEEP+=("$keep")
  P_NOW+=("$now")
  P_ORIG+=(pending)
  P_COPY+=(pending)
}

# archive_expire_run DIR ORIGINALS KEEP: record the decision, then expire the
# originals under every decision they owe (no Docker is involved), then the
# adopted copies and, when Compose has no backup service, the service's own
# top-level archives. Returns 0 everything done, 4 originals done but copy work
# is still pending (nonzero on purpose), 1 any other failure. Caller holds
# backup.lock and has refused a recovery.
EXP_ORIG=0 EXP_COPY=0 EXP_SERVICE=0
archive_expire_run() {
  local dir="$1" originals="$2" keep="$3" now i rc=0 vr=0 keep_env finished
  local -a nk=() nn=() no=() nc=()
  now="$(date +%s)"
  EXP_ORIG=0 EXP_COPY=0 EXP_SERVICE=0
  archive_pending_load "$dir" || return 1
  archive_pending_merge "$keep" "$now"
  archive_pending_save "$dir" || { archive_note "could not record the expiry decision"; return 1; }
  # Originals first: nothing on this path depends on Docker.
  for i in "${!P_KEEP[@]}"; do
    [[ "${P_ORIG[$i]}" == pending ]] || continue
    if [[ ! -d "$originals" ]]; then
      P_ORIG[i]='done'
    elif archive_expire_dir "$originals" "${P_KEEP[$i]}" "${P_NOW[$i]}"; then
      EXP_ORIG=$((EXP_ORIG + ARCH_DELETED))
      P_ORIG[i]='done'
    else
      archive_note "expiry of the original archives is incomplete (keep ${P_KEEP[$i]} days); the decision stays recorded"
      rc=1
      continue
    fi
    archive_pending_save "$dir" || return 1
  done
  archive_deadline "${FOLD_BACKUP_DOCKER_WAIT:-60}"
  archive_volume_resolve "$dir" || vr=$?
  if (( vr == 0 )) && [[ -e "$ARCH_MOUNT/adopted" || -L "$ARCH_MOUNT/adopted" ]]; then
    if [[ -L "$ARCH_MOUNT/adopted" || ! -d "$ARCH_MOUNT/adopted" || "$(stat -c '%u:%g:%a' -- "$ARCH_MOUNT/adopted")" != 0:0:700 ]]; then
      archive_note "adopted/ is not a root-only folder; the copies were not touched"
      vr=5
    fi
  fi
  for i in "${!P_KEEP[@]}"; do
    [[ "${P_COPY[$i]}" == pending ]] || continue
    finished=0
    if (( vr == 3 )); then
      finished=1 # no volume: no copies exist and none can be owed
    elif (( vr == 0 )); then
      if [[ ! -d "$ARCH_MOUNT/adopted" ]]; then
        finished=1
      elif archive_expire_dir "$ARCH_MOUNT/adopted" "${P_KEEP[$i]}" "${P_NOW[$i]}"; then
        EXP_COPY=$((EXP_COPY + ARCH_DELETED))
        finished=1
      fi
    fi
    if (( finished )); then
      P_COPY[i]='done'
      archive_pending_save "$dir" || return 1
    else
      archive_note "expiry of the adopted copies is pending (keep ${P_KEEP[$i]} days); it stays recorded and is retried"
      (( rc != 0 )) || rc=4
    fi
  done
  for i in "${!P_KEEP[@]}"; do
    [[ "${P_ORIG[$i]}" == 'done' && "${P_COPY[$i]}" == 'done' ]] && continue
    nk+=("${P_KEEP[$i]}") nn+=("${P_NOW[$i]}") no+=("${P_ORIG[$i]}") nc+=("${P_COPY[$i]}")
  done
  P_KEEP=("${nk[@]}") P_NOW=("${nn[@]}") P_ORIG=("${no[@]}") P_COPY=("${nc[@]}")
  archive_pending_save "$dir" || return 1
  # A deployment whose Compose has no backup service keeps the service's own
  # top-level archives under .env's retention; they are reported on their own.
  if (( vr == 0 && ARCH_SERVICE_DEFINED != 0 )); then
    keep_env="$(env_value "$dir/.env" FOLD_BACKUP_KEEP_DAYS || true)"
    [[ -n "$keep_env" ]] || keep_env=14
    if [[ "$keep_env" =~ ^[0-9]{1,3}$ ]] && (( 10#$keep_env >= 1 && 10#$keep_env <= 365 )); then
      if archive_expire_dir "$ARCH_MOUNT" "$((10#$keep_env))" "$now"; then
        EXP_SERVICE=$ARCH_DELETED
      else
        archive_note "expiry of the service archives is incomplete"
        (( rc != 0 )) || rc=4
      fi
    else
      archive_note "FOLD_BACKUP_KEEP_DAYS in .env is not 1 to 365; the service archives were not expired"
      (( rc != 0 )) || rc=4
    fi
  fi
  if (( vr != 0 && vr != 3 )); then
    archive_note "the backups volume could not be used; copy expiry stays pending"
    (( rc != 0 )) || rc=4
  fi
  printf 'expiry originals=%s adopted_copies=%s service_archives=%s\n' "$EXP_ORIG" "$EXP_COPY" "$EXP_SERVICE"
  return "$rc"
}

# archive_export_dump DIR NAME DEST: publish the service's dump NAME (a name the
# service just reported) to DEST/NAME as the host rollback anchor. Same rules as
# an adoption: no replacement, exact bytes and mtime, stable-source checks with
# a bounded retry, synchronization even when the export already exists. Returns
# 0 exported or identical, 3 DEST/NAME holds different bytes, 6 it holds the right
# bytes but is not a root-only mode-0600 file, 1 failure. Caller holds backup.lock.
archive_export_dump() {
  local dir="$1" name="$2" dest="$3" vr=0 src rc=1
  [[ "$name" =~ ^fold-[0-9]{8}-[0-9]{4}\.dump$ ]] || return 1
  archive_deadline "${FOLD_BACKUP_DOCKER_WAIT:-60}"
  archive_volume_resolve "$dir" || vr=$?
  (( vr == 0 )) || { archive_note "the backups volume could not be used for the export"; return 1; }
  src="$ARCH_MOUNT/$name"
  [[ -f "$src" && ! -L "$src" ]] || { archive_note "the service's dump ${name} is not there"; return 1; }
  mkdir -p -- "$dest" && chmod 700 -- "$dest" || return 1
  archive_clear_residue "$dest" || return 1
  for _ in 1 2 3; do
    rc=0
    archive_place "$src" "$dest/$name" || rc=$?
    case "$rc" in
      0|11) sync -- "$dest" || return 1; return 0 ;;
      3|6) return "$rc" ;;
    esac
  done
  return 1
}

# backup_lastok_epoch DIR: the service's LAST_OK as an epoch, or nothing.
backup_lastok_epoch() {
  local v
  v="$(archive_compose "$1" exec -T backup cat /backup-status/LAST_OK 2>/dev/null)" || return 1
  v="${v%%$'\n'*}"
  [[ "$v" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]] || return 1
  date -u -d "$v" +%s
}

# --- host settings -----------------------------------------------------------
BACKUP_ENV_KEYS=(FOLD_BACKUP_S3_BUCKET FOLD_BACKUP_S3_ENDPOINT FOLD_BACKUP_S3_REGION AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY FOLD_BACKUP_AGE_RECIPIENT FOLD_BACKUP_KEEP_DAYS)
declare -A BENV=()

# backup_env_parse FILE: read backup.env as data into BENV. Errors name a key or
# a line number, never a value.
backup_env_parse() {
  local file="$1" line key val n=0 k known
  BENV=()
  [[ -f "$file" ]] || return 1
  while IFS= read -r line || [[ -n "$line" ]]; do
    n=$((n + 1))
    line="${line%$'\r'}"
    line="${line#"${line%%[![:space:]]*}"}"
    case "$line" in ''|'#'*) continue ;; esac
    [[ "$line" == *=* ]] || { check_fail "backup.env line ${n} is not KEY=value"; return 1; }
    key="${line%%=*}"
    val="${line#*=}"
    if [[ "$val" == \"*\" && "$val" == *\" ]]; then val="${val:1:${#val}-2}"; fi
    case "$val" in *[[:space:]]*) check_fail "${key} in backup.env contains whitespace"; return 1 ;; esac
    known=0
    for k in "${BACKUP_ENV_KEYS[@]}"; do [[ "$k" != "$key" ]] || known=1; done
    (( known )) || { check_fail "unknown key ${key} in backup.env"; return 1; }
    BENV["$key"]="$val"
  done <"$file"
}

# backup_env_validate [keep]: KEEP_DAYS and, unless "keep" is given, all six
# offsite settings or none.
backup_env_validate() {
  local k any=0 v
  v="${BENV[FOLD_BACKUP_KEEP_DAYS]:-14}"
  if [[ ! "$v" =~ ^[0-9]{1,3}$ ]] || (( 10#$v < 1 || 10#$v > 365 )); then
    check_fail "FOLD_BACKUP_KEEP_DAYS must be a number from 1 to 365"
    return 1
  fi
  [[ "${1:-}" != keep ]] || return 0
  for k in "${BACKUP_ENV_KEYS[@]}"; do
    [[ "$k" == FOLD_BACKUP_KEEP_DAYS ]] && continue
    [[ -z "${BENV[$k]:-}" ]] || any=1
  done
  (( any )) || return 0
  for k in "${BACKUP_ENV_KEYS[@]}"; do
    [[ "$k" == FOLD_BACKUP_KEEP_DAYS ]] && continue
    [[ -n "${BENV[$k]:-}" ]] || { check_fail "the offsite settings are incomplete: ${k} is missing (set all six or none)"; return 1; }
  done
  [[ "${BENV[FOLD_BACKUP_S3_BUCKET]}" =~ ^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$ ]] || { check_fail "FOLD_BACKUP_S3_BUCKET is not a bucket name"; return 1; }
  [[ "${BENV[FOLD_BACKUP_S3_ENDPOINT]}" =~ ^https://[A-Za-z0-9._:-]+$ ]] || { check_fail "FOLD_BACKUP_S3_ENDPOINT must be an https URL"; return 1; }
  [[ "${BENV[FOLD_BACKUP_S3_REGION]}" =~ ^[a-z0-9-]+$ ]] || { check_fail "FOLD_BACKUP_S3_REGION is not a region name"; return 1; }
  [[ "${BENV[AWS_ACCESS_KEY_ID]}" =~ ^[!-~]+$ ]] || { check_fail "AWS_ACCESS_KEY_ID has characters that are not allowed"; return 1; }
  [[ "${BENV[AWS_SECRET_ACCESS_KEY]}" =~ ^[!-~]+$ ]] || { check_fail "AWS_SECRET_ACCESS_KEY has characters that are not allowed"; return 1; }
  [[ "${BENV[FOLD_BACKUP_AGE_RECIPIENT]}" =~ ^age1[0-9a-z]+$ ]] || { check_fail "FOLD_BACKUP_AGE_RECIPIENT must be an age public key (age1...)"; return 1; }
}

# env_set_durable FILE KEY VALUE [KEY VALUE]...: set each key (replacing the
# first uncommented KEY= line or appending), keep every other byte, write mode
# 600 through a synchronized replacement. Values are release labels, paths and
# the safe set backup_settings_copy checks: no quotes, spaces or backslashes.
# The file holds secrets, so every intermediate file is created private (umask
# 077, mode 600 from its first byte), each transformation must succeed before the
# next step or the rename, and a failure removes only these staging files and
# leaves FILE byte for byte as it was.
env_set_durable() (
  umask 077
  local file="$1" dir cur next key value
  local -a env_tmp=()
  shift
  dir="$(dirname "$file")"
  cur="$file"
  while (( $# >= 2 )); do
    key="$1" value="$2"
    shift 2
    next="$(mktemp "$dir/.tmp.XXXXXX")" || { rm -f -- "${env_tmp[@]}"; return 1; }
    env_tmp+=("$next")
    if ! ENV_KEY="$key" ENV_VALUE="$value" awk '
      BEGIN { key = ENVIRON["ENV_KEY"]; done = 0 }
      index($0, key "=") == 1 { if (!done) print key "=" ENVIRON["ENV_VALUE"]; done = 1; next }
      { print }
      END { if (!done) print key "=" ENVIRON["ENV_VALUE"] }
    ' "$cur" >"$next"; then
      rm -f -- "${env_tmp[@]}"
      return 1
    fi
    cur="$next"
  done
  [[ "$cur" != "$file" ]] || return 0
  if ! { chmod 600 -- "$cur" && sync -- "$cur" && mv -f -- "$cur" "$file" && sync -- "$dir"; }; then
    rm -f -- "${env_tmp[@]}"
    return 1
  fi
  rm -f -- "${env_tmp[@]}"
)

# backup_host_timezone: the host's IANA zone, or nothing.
backup_host_timezone() {
  local tz=""
  if command -v timedatectl >/dev/null 2>&1; then
    tz="$(timeout 10 timedatectl show -p Timezone --value 2>/dev/null || true)"
  fi
  if [[ -z "$tz" && -r /etc/timezone ]]; then
    tz="$(head -n 1 /etc/timezone)"
  fi
  [[ "$tz" =~ ^[A-Za-z0-9_+-]+(/[A-Za-z0-9_+-]+)*$ && "$tz" != Local ]] && printf '%s' "$tz"
  return 0
}

# backup_settings_copy DIR: copy backup.env's settings into .env for the backup
# service, only while .env has none of them. backup.env is never changed. An
# incomplete offsite set refuses, naming only the key. When .env already has
# settings, a difference from backup.env is a warning that names the key.
# The host timezone is copied when .env has none (the service's own 02:30 slot
# must be the host's).
backup_settings_copy() {
  local dir="$1" env="$1/.env" k cur configured=0 tz
  local -a pairs=()
  [[ -f "$env" ]] || { check_fail "no ${env} to copy backup settings into"; return 1; }
  if [[ -f "$dir/backup.env" ]]; then
    backup_env_parse "$dir/backup.env" || return 1
    backup_env_validate || return 1
    for k in "${BACKUP_ENV_KEYS[@]}"; do
      [[ -z "$(env_value "$env" "$k" || true)" ]] || configured=1
    done
    for k in "${BACKUP_ENV_KEYS[@]}"; do
      [[ -n "${BENV[$k]:-}" ]] || continue
      if (( configured )); then
        cur="$(env_value "$env" "$k" || true)"
        [[ "$cur" == "${BENV[$k]}" ]] || echo "settings: ${k} in .env differs from backup.env or is unset; the backup service uses .env" >&2
      else
        [[ "${BENV[$k]}" =~ ^[A-Za-z0-9._:/@+=,-]+$ ]] || { check_fail "${k} in backup.env has a character this tool will not write to .env; set it in .env yourself"; return 1; }
        pairs+=("$k" "${BENV[$k]}")
      fi
    done
  fi
  tz="$(backup_host_timezone)"
  cur="$(env_value "$env" FOLD_BACKUP_TZ || true)"
  if [[ -n "$tz" && -z "$cur" ]]; then
    pairs+=(FOLD_BACKUP_TZ "$tz")
  elif [[ -n "$tz" && "$cur" != "$tz" ]]; then
    echo "settings: FOLD_BACKUP_TZ in .env differs from the host timezone" >&2
  fi
  if (( ${#pairs[@]} == 0 )); then
    echo "settings: .env already has the backup settings; nothing copied"
    return 0
  fi
  env_set_durable "$env" "${pairs[@]}" || { check_fail "could not write the backup settings to .env"; return 1; }
  echo "settings: copied $(( ${#pairs[@]} / 2 )) backup setting(s) to .env (backup.env unchanged)"
}

# backup_identity_check DIR: the resolved backup service runs as exactly
# 65532:65532 with cap_drop ALL, no added capability and no privilege, and keeps
# its archives, status and uploads where the host expects them: /backups is the
# Compose backups volume (the one archive_volume_resolve inspects, so adoption and
# retention act on the volume the service writes), /backup-status the
# backup-status volume, /data the uploads volume read-only, and none of the
# image's path or uid settings is overridden. Every managed start of the service
# asks it first (the cutover, the disposable drill's start and a recovery's
# scheduler), on the resolved configuration, so an unset field is refused like a
# wrong one. configs, secrets and devices are refused whenever they are present
# (a config with no target would default to /<name>, and a long-syntax one can land
# anywhere), and a tmpfs may not be the managed folder itself: the image's own USER or FOLD_BACKUP_ARCHIVE_DIR would otherwise decide
# where and as whom the archives are written. Nothing is started when it fails.
backup_identity_check() {
  local dir="$1" config
  config="$(observe_compose "$dir" config --format json 2>/dev/null)" || {
    check_fail "cannot resolve the Compose configuration in ${dir}; the backup service identity was not verified. The backup service was not started"; return 1;
  }
  if ! jq -e '
      .services.backup as $b
      | $b != null
      and (($b.privileged // false) == false)
      and ((($b.cap_add // []) | length) == 0)
      and (($b.pid // "") != "host") and (($b.network_mode // "") != "host")
      and (($b.user // null) != null and ($b.user | tostring) == "65532:65532")
      and (($b.cap_drop // null | type) == "array" and ($b.cap_drop | index("ALL")) != null)' <<<"$config" >/dev/null 2>&1; then
    check_fail "the resolved backup service must run as user 65532:65532 with cap_drop ALL and no added capability, privilege, host pid or host network; remove the override, or run the current ${dir}/provision.sh --deploy-dir ${dir} if the Compose file predates them. The backup service was not started"
    return 1
  fi
  if ! jq -e '
      def mount($t): [(.services.backup.volumes // [])[] | select(.target == $t)];
      def only($t; $src; $ro): mount($t) as $m
        | ($m | length) == 1 and $m[0].type == "volume" and $m[0].source == $src
          and (($m[0].read_only // false) == $ro)
          and ((($m[0].volume // {}).subpath // "") == "");
      def odd($p): ($p | type) != "string" or ($p | startswith("/") | not)
        or ($p | test("(^|/)\\.{1,2}(/|$)|//"));
      def inside($p): ["/backups", "/backup-status", "/data"] | any(.[]; . as $m | $p | startswith($m + "/"));
      def at_or_inside($p): ["/backups", "/backup-status", "/data"] | any(.[]; . as $m | $p == $m or ($p | startswith($m + "/")));
      def tmpfs_targets: [(.services.backup.tmpfs // []) | (if type == "string" then [.] else . end)[] | split(":")[0]];
      def targets: [(.services.backup.volumes // [])[] | .target] + tmpfs_targets;
      def unset_or($k; $v): ((.services.backup.environment // {})[$k]) as $x
        | ($x == null or $x == "" or ($x | tostring) == $v);
      (.volumes.backups // null) != null
      and all(targets[]; odd(.) | not)
      and all(targets[]; inside(.) | not)
      and all(tmpfs_targets[]; at_or_inside(.) | not)
      and ((.services.backup.volumes_from // []) | length) == 0
      and ((.services.backup.configs // []) | length) == 0
      and ((.services.backup.secrets // []) | length) == 0
      and ((.services.backup.devices // []) | length) == 0
      and only("/backups"; "backups"; false)
      and only("/backup-status"; "backup-status"; false)
      and only("/data"; "uploads"; true)
      and unset_or("FOLD_BACKUP_ARCHIVE_DIR"; "/backups")
      and unset_or("FOLD_BACKUP_STATUS_DIR"; "/backup-status")
      and unset_or("FOLD_BACKUP_DATA_DIR"; "/data")
      and unset_or("FOLD_BACKUP_APP_UID"; "65532")
      and unset_or("FOLD_BACKUP_APP_GID"; "65532")' <<<"$config" >/dev/null 2>&1; then
    check_fail "the resolved backup service must keep its archives in the backups volume at /backups, its status in the backup-status volume at /backup-status and read the uploads volume at /data read-only, each whole (no volume subpath) and with no other mount or volumes_from inside /backups, /backup-status or /data, no tmpfs at or inside them, no configs, secrets or devices, and with no FOLD_BACKUP_ARCHIVE_DIR, FOLD_BACKUP_STATUS_DIR, FOLD_BACKUP_DATA_DIR or app uid override; remove the override, or run the current ${dir}/provision.sh --deploy-dir ${dir}. The backup service was not started"
    return 1
  fi
}

# backup_volume_check DIR: read-only check, for a recovery's scheduler, of the
# backups volume the service is about to write: absent (Compose creates it from
# the image, which makes the root protected), or its root exactly root:65532 mode
# 1770 and any adopted/ a root-only folder. It repairs nothing: a recovery does not
# mutate archives, and a root that lost its sticky bit would let the service
# remove or replace what the host keeps there.
backup_volume_check() {
  local dir="$1" vr=0
  archive_deadline "${FOLD_BACKUP_DOCKER_WAIT:-60}"
  archive_volume_resolve "$dir" || vr=$?
  case "$vr" in
    3) return 0 ;;
    0) ;;
    *) check_fail "the backups volume could not be verified (status ${vr}); recovery state kept, the backup service was not started"; return 1 ;;
  esac
  if [[ "$(stat -c '%u:%g:%a' -- "$ARCH_MOUNT")" != "0:${BACKUP_COORD_GID}:1770" ]]; then
    check_fail "the backups volume root is not root:${BACKUP_COORD_GID} mode 1770; the backup service was not started. Run the current provision.sh --deploy-dir ${dir}, which repairs it, then retry"
    return 1
  fi
  if [[ -e "$ARCH_MOUNT/adopted" || -L "$ARCH_MOUNT/adopted" ]]; then
    if [[ -L "$ARCH_MOUNT/adopted" || ! -d "$ARCH_MOUNT/adopted" || "$(stat -c '%u:%g:%a' -- "$ARCH_MOUNT/adopted")" != 0:0:700 ]]; then
      check_fail "adopted/ in the backups volume is not a root-only folder (root:root, mode 700); the backup service was not started"
      return 1
    fi
  fi
}
