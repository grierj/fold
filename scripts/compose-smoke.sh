#!/usr/bin/env bash
# Boot the shipped Compose stack and check a fresh database.
#
# Default: build ghcr.io/grierj/fold:ci from this checkout and start it with
# docker-compose.yml plus deploy/compose.ci.yml.
# Release: --image REF pulls that ref (tag or name@digest), tags it as :ci,
# and smokes the same files so the published image is the one that boots.
#
# CI uses the shipped 172.30.0.0/24 network and exits if that subnet is
# already taken, unless --local-subnet or a non-empty FOLD_SMOKE_SUBNET is
# set. On a laptop those two, or another Docker network already holding the
# shipped subnet, add deploy/compose.smoke-local.yml (10.216.30.0/24).
#
# Prints assert names only. The first-run setup code stays in the container
# log; this script counts that line and never echoes it.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

# Compose prefers the shell over --env-file. Drop every name the shipped
# file interpolates, plus host vars that would change the app if they leaked
# in. Docker commands also run under env -i (PATH, HOME, DOCKER_* kept, and
# XDG_RUNTIME_DIR, SSH_AUTH_SOCK, and HTTP_PROXY, HTTPS_PROXY, NO_PROXY in
# either case when set) so COMPOSE_FILE and anything else cannot redirect
# the smoke.
mapfile -t compose_vars < <(
  grep -oE '\$\{[A-Za-z_][A-Za-z0-9_]*' docker-compose.yml | sed 's/^\${//' | sort -u
)
for name in "${compose_vars[@]}" SITE_URL COOKIE_SECURE APP_ENV; do
  unset "$name"
done

project=fold-smoke
image_local=ghcr.io/grierj/fold:ci
base_url=http://127.0.0.1:18080
image_ref=
tmp=
env_file=
started=0
local_subnet_flag=0
use_local_subnet=0
compose_files=(-f docker-compose.yml -f deploy/compose.ci.yml)
shipped_subnets=()
caddy_image=

usage() {
  echo "usage: scripts/compose-smoke.sh [--image REF] [--local-subnet]" >&2
  echo "  default        build ${image_local} from this checkout (FOLD_VERSION=ci)" >&2
  echo "  --image        pull REF and smoke that image instead of building" >&2
  echo "  --local-subnet use deploy/compose.smoke-local.yml instead of the shipped subnet" >&2
  echo "                 (also when FOLD_SMOKE_SUBNET is set, or off CI when that subnet is taken)" >&2
}

# Run a command without the caller's environment. Compose interpolation and
# COMPOSE_* would otherwise follow the shell that launched this script.
# Rootless Docker and an ssh DOCKER_HOST need the runtime dir and agent
# socket. Proxy variables are passed in either case so a pull still works.
run_clean() {
  local -a env_args=()
  local name
  env_args=(env -i "PATH=${PATH-}" "HOME=${HOME-}")
  for name in XDG_RUNTIME_DIR SSH_AUTH_SOCK \
    http_proxy https_proxy no_proxy HTTP_PROXY HTTPS_PROXY NO_PROXY; do
    if [[ -v $name ]]; then
      env_args+=("${name}=${!name-}")
    fi
  done
  while IFS= read -r name; do
    [[ -n "$name" ]] || continue
    env_args+=("${name}=${!name-}")
  done < <(compgen -v DOCKER_ || true)
  "${env_args[@]}" "$@"
}

compose() {
  run_clean docker compose -p "$project" "${compose_files[@]}" --env-file "$env_file" "$@"
}

cleanup() {
  local rc=$? down_rc
  # set -e would abort this trap before the stack is removed.
  set +e
  if [[ "$rc" -ne 0 && "$started" -eq 1 ]]; then
    echo "compose-smoke: container logs (setup code omitted):" >&2
    compose logs --tail 200 2>&1 | grep -v -F 'First-run setup code' >&2 || true
  fi
  # started is set only after this run has confirmed the project was empty
  # and is about to create it. An existing fold-smoke stack is left alone.
  if [[ "$started" -eq 1 ]]; then
    compose down -v --remove-orphans
    down_rc=$?
    if [[ "$down_rc" -ne 0 ]]; then
      echo "compose-smoke: docker compose down -v failed" >&2
      if [[ "$rc" -eq 0 ]]; then
        rc=$down_rc
      fi
    fi
  fi
  if [[ -n "$tmp" ]]; then
    rm -rf "$tmp"
  fi
  exit "$rc"
}

fail() {
  echo "compose-smoke: assert failed: $*" >&2
  exit 1
}

ipv4_to_int() {
  local ip=$1 a b c d
  [[ "$ip" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)\.([0-9]+)$ ]] || return 1
  a=${BASH_REMATCH[1]}
  b=${BASH_REMATCH[2]}
  c=${BASH_REMATCH[3]}
  d=${BASH_REMATCH[4]}
  if ((10#$a > 255 || 10#$b > 255 || 10#$c > 255 || 10#$d > 255)); then
    return 1
  fi
  printf '%s\n' $(( (10#$a << 24) | (10#$b << 16) | (10#$c << 8) | 10#$d ))
}

# True when every address of inner is inside outer. Both are IPv4 CIDRs.
cidr_contains() {
  local outer=$1 inner=$2
  local o_ip o_len i_ip i_len o_int i_int mask
  [[ "$outer" =~ ^([0-9.]+)/([0-9]+)$ ]] || return 1
  o_ip=${BASH_REMATCH[1]}
  o_len=${BASH_REMATCH[2]}
  [[ "$inner" =~ ^([0-9.]+)/([0-9]+)$ ]] || return 1
  i_ip=${BASH_REMATCH[1]}
  i_len=${BASH_REMATCH[2]}
  if ((10#$o_len > 32 || 10#$i_len > 32 || 10#$i_len < 10#$o_len)); then
    return 1
  fi
  o_int=$(ipv4_to_int "$o_ip") || return 1
  i_int=$(ipv4_to_int "$i_ip") || return 1
  if ((10#$o_len == 0)); then
    mask=0
  else
    mask=$(( (0xFFFFFFFF << (32 - 10#$o_len)) & 0xFFFFFFFF ))
  fi
  (( (o_int & mask) == (i_int & mask) ))
}

subnet_in_use() {
  local want=$1 id subnet
  local -a ids=()
  while IFS= read -r id; do
    [[ -n "$id" ]] && ids+=("$id")
  done < <(run_clean docker network ls -q)
  if [[ ${#ids[@]} -eq 0 ]]; then
    return 1
  fi
  while IFS= read -r subnet; do
    if [[ "$subnet" == "$want" ]]; then
      return 0
    fi
  done < <(run_clean docker network inspect "${ids[@]}" --format '{{range .IPAM.Config}}{{.Subnet}}{{"\n"}}{{end}}')
  return 1
}

refuse_existing_project() {
  local existing
  existing=$(run_clean docker compose -p "$project" ps -a -q)
  if [[ -n "${existing//[[:space:]]/}" ]]; then
    echo "compose-smoke: project ${project} already has containers; refusing to start or remove it" >&2
    exit 1
  fi
}

read_local_subnet() {
  local found
  found=$(awk '$1 == "-" && $2 == "subnet:" { print $3; exit }' deploy/compose.smoke-local.yml)
  if [[ ! "$found" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+/[0-9]+$ ]]; then
    echo "compose-smoke: deploy/compose.smoke-local.yml has no IPv4 subnet" >&2
    exit 1
  fi
  printf '%s\n' "$found"
}

# Shipped file only: the app network's subnet must contain the default
# FOLD_TRUSTED_PROXIES CIDR. --env-file pins every interpolated name so a
# project .env cannot change the default this check is reading.
assert_shipped_network() {
  local cfg=$1 net subnet proxies part ok
  local -a nets=()
  shipped_subnets=()
  while IFS= read -r net; do
    [[ -n "$net" ]] && nets+=("$net")
  done < <(jq -r '
    .services.app.networks
    | if type == "array" then .[]
      elif type == "object" then keys[]
      else empty end
  ' <<<"$cfg")
  if [[ ${#nets[@]} -eq 0 ]]; then
    fail "shipped subnet contains FOLD_TRUSTED_PROXIES: app has no network"
  fi
  for net in "${nets[@]}"; do
    while IFS= read -r subnet; do
      [[ -n "$subnet" && "$subnet" != "null" ]] && shipped_subnets+=("$subnet")
    done < <(jq -r --arg net "$net" '.networks[$net].ipam.config[]?.subnet // empty' <<<"$cfg")
  done
  if [[ ${#shipped_subnets[@]} -eq 0 ]]; then
    fail "shipped subnet contains FOLD_TRUSTED_PROXIES: app network has no subnet"
  fi
  proxies=$(jq -r '
    .services.app.environment
    | if type == "array" then
        .[] | select(startswith("FOLD_TRUSTED_PROXIES=")) | sub("^FOLD_TRUSTED_PROXIES="; "")
      elif type == "object" then
        .FOLD_TRUSTED_PROXIES // empty
      else empty end
  ' <<<"$cfg")
  if [[ -z "$proxies" || "$proxies" == "null" ]]; then
    fail "shipped subnet contains FOLD_TRUSTED_PROXIES: app has no FOLD_TRUSTED_PROXIES"
  fi
  while IFS= read -r part; do
    part=${part#"${part%%[![:space:]]*}"}
    part=${part%"${part##*[![:space:]]}"}
    [[ -n "$part" ]] || continue
    if [[ "$part" == *:* ]]; then
      fail "shipped subnet contains FOLD_TRUSTED_PROXIES: ${part} is not an IPv4 CIDR"
    fi
    if [[ "$part" != */* ]]; then
      part="${part}/32"
    fi
    ok=0
    for subnet in "${shipped_subnets[@]}"; do
      if cidr_contains "$subnet" "$part"; then
        ok=1
        break
      fi
    done
    if [[ "$ok" -ne 1 ]]; then
      fail "shipped subnet contains FOLD_TRUSTED_PROXIES: ${part} is outside ${shipped_subnets[*]}"
    fi
  done < <(printf '%s\n' "$proxies" | tr ',' '\n')
  caddy_image=$(jq -r '.services.caddy.image // empty' <<<"$cfg")
  if [[ -z "$caddy_image" || "$caddy_image" == "null" ]]; then
    fail "caddy image: compose config has no services.caddy.image"
  fi
  echo "compose-smoke: ok shipped subnet ${shipped_subnets[*]} contains FOLD_TRUSTED_PROXIES (${proxies})"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --image)
      if [[ $# -lt 2 || -z "${2:-}" || "$2" == --* ]]; then
        echo "compose-smoke: --image needs a ref" >&2
        usage
        exit 2
      fi
      image_ref=$2
      shift 2
      ;;
    --local-subnet)
      local_subnet_flag=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "compose-smoke: unknown argument: $1" >&2
      usage
      exit 2
      ;;
  esac
done

if [[ -n "$image_ref" ]]; then
  case "$image_ref" in
    *[![:alnum:]@:/._-]*)
      echo "compose-smoke: --image ref has unexpected characters" >&2
      exit 2
      ;;
  esac
fi

command -v docker >/dev/null 2>&1 || { echo "compose-smoke: docker is required" >&2; exit 1; }
command -v curl >/dev/null 2>&1 || { echo "compose-smoke: curl is required" >&2; exit 1; }
command -v jq >/dev/null 2>&1 || { echo "compose-smoke: jq is required" >&2; exit 1; }

umask 077
tmp=$(mktemp -d)
env_file=$tmp/env
trap cleanup EXIT

if command -v openssl >/dev/null 2>&1; then
  session_secret=$(openssl rand -base64 48 | tr -d '\n')
  db_password=$(openssl rand -hex 24)
else
  session_secret=$(head -c 48 /dev/urandom | base64 | tr -d '\n')
  db_password=$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')
fi
{
  printf 'DOMAIN=localhost\n'
  printf 'SESSION_SECRET=%s\n' "$session_secret"
  printf 'DB_PASSWORD=%s\n' "$db_password"
  printf 'FOLD_VERSION=ci\n'
  # Empty optional names keep the shipped ${VAR:-default} and block a
  # project .env from supplying a different value.
  for name in "${compose_vars[@]}"; do
    case "$name" in
      DOMAIN|SESSION_SECRET|DB_PASSWORD|FOLD_VERSION) ;;
      *) printf '%s=\n' "$name" ;;
    esac
  done
} >"$env_file"
unset session_secret db_password

shipped_config=$(run_clean docker compose -f docker-compose.yml --env-file "$env_file" config --format json)
assert_shipped_network "$shipped_config"
unset shipped_config

# Decide the network before caddy validate, the image build, or compose up.
# CI must boot the shipped subnet; a laptop may fall back when it is taken.
subnet_reason=
if [[ "$local_subnet_flag" -eq 1 ]]; then
  use_local_subnet=1
  subnet_reason="--local-subnet"
elif [[ -n "${FOLD_SMOKE_SUBNET:-}" ]]; then
  use_local_subnet=1
  subnet_reason="FOLD_SMOKE_SUBNET"
else
  for name in "${shipped_subnets[@]}"; do
    if subnet_in_use "$name"; then
      if [[ "${CI:-}" == "true" ]]; then
        echo "compose-smoke: ${name} is already in use on this runner; the smoke must boot the shipped network" >&2
        exit 1
      fi
      use_local_subnet=1
      subnet_reason="${name} is already in use"
      break
    fi
  done
fi
if [[ "$use_local_subnet" -eq 1 ]]; then
  local_override_subnet=$(read_local_subnet)
  if subnet_in_use "$local_override_subnet"; then
    echo "compose-smoke: ${local_override_subnet} is already in use; refusing to start" >&2
    exit 1
  fi
  compose_files+=(-f deploy/compose.smoke-local.yml)
  echo "compose-smoke: using deploy/compose.smoke-local.yml (${subnet_reason})"
fi

echo "compose-smoke: caddy validate with ${caddy_image}"
if ! run_clean docker run --rm \
  -v "${PWD}/Caddyfile:/etc/caddy/Caddyfile:ro" \
  -e DOMAIN=localhost \
  "$caddy_image" \
  caddy validate --config /etc/caddy/Caddyfile; then
  fail "caddy validate"
fi
echo "compose-smoke: ok caddy validate (${caddy_image})"

refuse_existing_project

if [[ -n "$image_ref" ]]; then
  echo "compose-smoke: pulling ${image_ref}"
  run_clean docker pull "$image_ref"
  if ! run_clean docker tag "$image_ref" "$image_local"; then
    image_id=$(run_clean docker image inspect --format '{{.Id}}' "$image_ref")
    run_clean docker tag "$image_id" "$image_local"
  fi
else
  echo "compose-smoke: building ${image_local}"
  run_clean docker build --build-arg FOLD_VERSION=ci -t "$image_local" "$root"
fi

# Check again after the build. The down trap is still unarmed, so a project
# that appeared while we built is not torn down.
refuse_existing_project
echo "compose-smoke: starting fold-smoke"
started=1
if ! compose up -d --wait --wait-timeout 300; then
  fail "services healthy: docker compose up --wait failed"
fi

ps_json=$(compose ps -a --format json)
if [[ -z "${ps_json//[[:space:]]/}" ]]; then
  fail "services healthy: docker compose ps returned nothing"
fi
items=$(printf '%s\n' "$ps_json" | jq -c -s 'if length == 1 and (.[0] | type) == "array" then .[0] else . end')

health_detail=$(printf '%s' "$items" | jq -r '
  def healthy: .Health == "healthy" or ((.Status // "") | contains("(healthy)"));
  . as $all
  | (["db", "app"][] | . as $name
      | ($all | map(select(.Service == $name))) as $rows
      | if ($rows | length) == 0 then "missing \($name)"
        elif ($rows | length) > 1 then "duplicate \($name)"
        elif ($rows[0] | healthy | not) then "\($name) \($rows[0].Status // $rows[0].State // "not healthy")"
        else empty end),
    (if any($all[]; .Service == "caddy") then "caddy is running; deploy/compose.ci.yml should keep it off" else empty end)
')
if [[ -n "$health_detail" ]]; then
  fail "services healthy: ${health_detail//$'\n'/; }"
fi
echo "compose-smoke: ok services healthy"

health_body=$tmp/healthz
if ! health_code=$(curl -sS -o "$health_body" -w '%{http_code}' --max-time 20 "${base_url}/healthz"); then
  fail "GET /healthz: curl failed"
fi
if [[ "$health_code" != "200" ]]; then
  fail "GET /healthz: status ${health_code}, want 200"
fi
if [[ -s "$health_body" ]]; then
  fail "GET /healthz: body was not empty"
fi
echo "compose-smoke: ok GET /healthz 200 empty body"

redir_hdr=$tmp/redir
if ! redir_code=$(curl -sS -D "$redir_hdr" -o /dev/null -w '%{http_code}' --max-redirs 0 --max-time 20 "${base_url}/"); then
  fail "GET /: curl failed"
fi
redir_loc=$(awk 'tolower($1) == "location:" { print $2 }' "$redir_hdr" | tr -d '\r' | tail -n 1)
case "$redir_loc" in
  /setup)
    ;;
  *)
    fail "GET /: location ${redir_loc:-missing}, want /setup"
    ;;
esac
if [[ "$redir_code" != "303" ]]; then
  fail "GET /: status ${redir_code}, want 303"
fi
echo "compose-smoke: ok GET / redirects to /setup"

# Fresh boot has a setup code, so GET /setup is the unlock page
# (templates/setup-unlock.html). templates/setup.html is the form after
# the code is entered.
setup_body=$tmp/setup
if ! setup_code=$(curl -sS -o "$setup_body" -w '%{http_code}' --max-time 20 "${base_url}/setup"); then
  fail "GET /setup: curl failed"
fi
if [[ "$setup_code" != "200" ]]; then
  fail "GET /setup: status ${setup_code}, want 200"
fi
if ! grep -q -F 'Enter your setup code' "$setup_body"; then
  fail "GET /setup: unlock page missing \"Enter your setup code\" (templates/setup-unlock.html)"
fi
echo "compose-smoke: ok GET /setup 200 unlock page"

# Count the marker. Do not print the line: it contains the setup code.
# grep -c exits 1 when the count is 0; the assignment still captures it.
setup_lines=$(compose logs app --no-log-prefix 2>&1 | grep -c -F 'First-run setup code' || true)
if [[ "$setup_lines" != "1" ]]; then
  fail "app log has ${setup_lines} \"First-run setup code\" lines, want exactly 1"
fi
echo "compose-smoke: ok app log has exactly one First-run setup code line"

# PublishedPort 0 is an exposed container port, not a host publish
# (same distinction as deploy/digitalocean/checks.sh publishers).
# 5432 or 8080 may be published only on loopback.
port_bad=$(printf '%s' "$items" | jq -r '
  def loopback: .URL == "127.0.0.1" or .URL == "::1";
  def watched:
    .PublishedPort == 5432 or .PublishedPort == 8080
    or .TargetPort == 5432 or .TargetPort == 8080;
  .[] | . as $c
  | ($c.Publishers // [])[]
  | select((.PublishedPort // 0) != 0)
  | select(watched)
  | select(loopback | not)
  | "\($c.Service) \(.URL):\(.PublishedPort)->\(.TargetPort)/\(.Protocol)"
')
if [[ -n "$port_bad" ]]; then
  fail "5432 or 8080 published off loopback: ${port_bad//$'\n'/; }"
fi
echo "compose-smoke: ok 5432 and 8080 are not published off loopback"
echo "compose-smoke: passed"
