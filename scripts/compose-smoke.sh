#!/usr/bin/env bash
# Boot the shipped Compose stack and check a fresh database.
#
# Default: build ghcr.io/grierj/fold:ci from this checkout and start it with
# docker-compose.yml plus deploy/compose.ci.yml.
# Release: --image REF pulls that ref (tag or name@digest), tags it as :ci,
# and smokes the same files so the published image is the one that boots.
#
# Prints assert names only. The first-run setup code stays in the container
# log; this script counts that line and never echoes it.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

project=fold-smoke
image_local=ghcr.io/grierj/fold:ci
base_url=http://127.0.0.1:18080
image_ref=
tmp=
env_file=
started=0

usage() {
  echo "usage: scripts/compose-smoke.sh [--image REF]" >&2
  echo "  default   build ${image_local} from this checkout (FOLD_VERSION=ci)" >&2
  echo "  --image   pull REF and smoke that image instead of building" >&2
}

compose() {
  docker compose -p "$project" \
    -f docker-compose.yml -f deploy/compose.ci.yml \
    --env-file "$env_file" "$@"
}

cleanup() {
  local rc=$? down_rc
  # set -e would abort this trap before the stack is removed.
  set +e
  if [[ "$rc" -ne 0 && "$started" -eq 1 ]]; then
    echo "compose-smoke: container logs (setup code omitted):" >&2
    compose logs --tail 200 2>&1 | grep -v -F 'First-run setup code' >&2 || true
  fi
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
cat >"$env_file" <<EOF
DOMAIN=localhost
SESSION_SECRET=${session_secret}
DB_PASSWORD=${db_password}
FOLD_VERSION=ci
EOF
unset session_secret db_password

if [[ -n "$image_ref" ]]; then
  echo "compose-smoke: pulling ${image_ref}"
  docker pull "$image_ref"
  if ! docker tag "$image_ref" "$image_local"; then
    image_id=$(docker image inspect --format '{{.Id}}' "$image_ref")
    docker tag "$image_id" "$image_local"
  fi
else
  echo "compose-smoke: building ${image_local}"
  docker build --build-arg FOLD_VERSION=ci -t "$image_local" "$root"
fi

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
