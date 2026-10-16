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

assert_pinned_image() {
  local image="$1"
  case "$image" in
    *:latest|*/latest)
      check_fail "${image} is not a pinned tag. Refusing :latest." || return
      ;;
  esac
  if [[ "$image" != *:* ]]; then
    check_fail "${image} has no tag. Example: ghcr.io/grierj/fold:1.2.3" || return
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
