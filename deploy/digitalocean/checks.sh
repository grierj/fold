#!/usr/bin/env bash
# Checks shared by provision.sh and smoke.sh. Source this file.
# A check that cannot be performed fails closed.

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  echo "checks.sh is sourced by provision.sh and smoke.sh" >&2
  exit 1
fi

check_fail() {
  printf 'check: %s\n' "$1" >&2
  return 1
}

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
    (svc == "db" || svc == "app") && $1 == "ports:" && NF > 1 {
      print svc " " $0
      next
    }
    (svc == "db" || svc == "app") && $1 == "ports:" { inports = 1; next }
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
    check_fail "compose publishes ${bad//$'\n'/; } for Postgres or the app. Keep them on the compose network, or bind 127.0.0.1 only. A public Docker port bypasses ufw." || return
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
  if ! ps="$(docker compose --project-directory "$deploy_dir" ps --format '{{.Service}}|{{.Status}}|{{.Publishers}}')"; then
    check_fail "docker compose ps failed in ${deploy_dir}; cannot verify that 5432 and 8080 are private" || return
  fi
  [[ -n "$ps" ]] || { check_fail "no containers in ${deploy_dir}" || return; }
  while IFS= read -r line; do
    [[ -n "$line" ]] || continue
    svc="${line%%|*}"
    rest="${line#*|}"
    pubs="${rest#*|}"
    if [[ "$svc" == "db" || "$svc" == "app" ]]; then
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
    ids="$(docker compose --project-directory "$dir" config --format json)" || {
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
      else [[ ( "$at" == start || "$at" == smoke ) && "$observation" == Q ]]; fi ;;
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
  chmod 600 "$path" || return 1
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
