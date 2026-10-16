#!/usr/bin/env bash
# Post-deploy checks for a Fold droplet. Safe to re-run after an upgrade.
#
# Waits until db and app are healthy, requests https://DOMAIN/healthz, checks
# the TLS certificate, and fails if Postgres (5432) or the app (8080) is
# reachable on a public address. Also checks .env mode 600, that those files
# are not committed, that sshd refuses root and password login, and that ufw
# is active.
#
# Exit status is non-zero on any failed check.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=deploy/digitalocean/checks.sh
source "$SCRIPT_DIR/checks.sh"

DOMAIN=""
DEPLOY_DIR="/opt/fold"
SOURCE_DIR=""
TIMEOUT=180

die() {
  printf 'smoke: %s\n' "$1" >&2
  exit "${2:-1}"
}

usage() {
  cat <<'EOF'
Usage: smoke.sh [--domain groups.example.org] [--deploy-dir /opt/fold]
                 [--source-dir /root/fold] [--timeout 180]

DOMAIN defaults to the value in <deploy-dir>/.env. --timeout is seconds to
wait for healthy containers and https://DOMAIN/healthz (15 to 600).
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
    --domain)
      need_value "$@"
      DOMAIN="$2"
      shift 2
      ;;
    --domain=*)
      DOMAIN="${1#*=}"
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
    --timeout)
      need_value "$@"
      TIMEOUT="$2"
      shift 2
      ;;
    --timeout=*)
      TIMEOUT="${1#*=}"
      shift
      ;;
    *)
      die "unknown option: $1 (try --help)"
      ;;
  esac
done

[[ "$DEPLOY_DIR" == /* ]] || die "--deploy-dir must be an absolute path"
[[ "$TIMEOUT" =~ ^[0-9]+$ ]] || die "--timeout must be a number of seconds"
[[ "$TIMEOUT" -ge 15 && "$TIMEOUT" -le 600 ]] || die "--timeout must be between 15 and 600"

from_env=""
if [[ -f "$DEPLOY_DIR/.env" ]]; then
  from_env="$(env_value "$DEPLOY_DIR/.env" DOMAIN || true)"
fi
if [[ -z "$DOMAIN" ]]; then
  DOMAIN="$from_env"
fi
[[ -n "$DOMAIN" ]] || die "--domain is required, or set DOMAIN in ${DEPLOY_DIR}/.env"
if [[ -n "$from_env" && "$from_env" != "$DOMAIN" ]]; then
  die "${DEPLOY_DIR}/.env DOMAIN is ${from_env}; this run passed --domain ${DOMAIN}"
fi

host_checks() {
  assert_ssh_locked || exit 1
  assert_ufw_active || exit 1
  assert_env_mode "$DEPLOY_DIR/.env" || exit 1
  if [[ -f "$DEPLOY_DIR/backup.env" ]]; then
    assert_env_mode "$DEPLOY_DIR/backup.env" || exit 1
  fi
  assert_env_untracked "$DEPLOY_DIR" "$SOURCE_DIR" /root/fold || exit 1
  assert_compose_private "$DEPLOY_DIR/docker-compose.yml" || exit 1
  assert_trusted_proxy "$DEPLOY_DIR/docker-compose.yml" "$DEPLOY_DIR/.env" || exit 1
}

compose_ps() {
  docker compose --project-directory "$DEPLOY_DIR" ps --format '{{.Service}}|{{.Status}}|{{.Publishers}}'
}

containers_ready() {
  local ps="$1" line svc rest status
  local have_db=0 have_app=0 have_caddy=0
  while IFS= read -r line; do
    [[ -n "$line" ]] || continue
    svc="${line%%|*}"
    rest="${line#*|}"
    status="${rest%%|*}"
    case "$svc" in
      db)
        have_db=1
        [[ "$status" == *"(healthy)"* ]] || return 1
        ;;
      app)
        have_app=1
        [[ "$status" == *"(healthy)"* ]] || return 1
        ;;
      caddy)
        have_caddy=1
        [[ "$status" == Up* || "$status" == *"(healthy)"* ]] || return 1
        ;;
    esac
  done <<<"$ps"
  [[ "$have_db" == 1 && "$have_app" == 1 && "$have_caddy" == 1 ]]
}

# 0 = certificate is fine, 2 = handshake not ready yet (caller may retry).
# A certificate that is present but invalid exits the script.
tls_check() {
  local raw pem start end start_s end_s now host_out issuer
  if ! raw="$(timeout 20 openssl s_client -servername "$DOMAIN" -connect "${DOMAIN}:443" -verify_return_error </dev/null 2>&1)"; then
    return 2
  fi
  pem="$(printf '%s\n' "$raw" | sed -n '/-----BEGIN CERTIFICATE-----/,/-----END CERTIFICATE-----/p')"
  [[ -n "$pem" ]] || die "no certificate in the TLS handshake for ${DOMAIN}:443"
  start="$(printf '%s\n' "$pem" | openssl x509 -noout -startdate)"
  end="$(printf '%s\n' "$pem" | openssl x509 -noout -enddate)"
  start="${start#notBefore=}"
  end="${end#notAfter=}"
  start_s="$(date -d "$start" +%s)"
  end_s="$(date -d "$end" +%s)"
  now="$(date +%s)"
  if (( now < start_s || now >= end_s )); then
    die "TLS certificate for ${DOMAIN} is not currently valid (notBefore=${start} notAfter=${end})"
  fi
  host_out="$(printf '%s\n' "$pem" | openssl x509 -noout -checkhost "$DOMAIN" 2>&1 || true)"
  if [[ "$host_out" == *"does NOT match"* || "$host_out" != *"does match certificate"* ]]; then
    die "TLS certificate is not valid for ${DOMAIN}"
  fi
  issuer="$(printf '%s\n' "$pem" | openssl x509 -noout -issuer 2>/dev/null || true)"
  if [[ "$issuer" == *[Cc]loudflare* ]]; then
    die "TLS certificate for ${DOMAIN} was issued by Cloudflare. Set the DNS record to DNS-only so Caddy can obtain a certificate."
  fi
  return 0
}

wait_for_site() {
  local deadline now ps last rc
  deadline="$(( $(date +%s) + TIMEOUT ))"
  last="containers are not up"
  while true; do
    now="$(date +%s)"
    if (( now >= deadline )); then
      die "timed out after ${TIMEOUT}s waiting for https://${DOMAIN}/healthz (${last}). Logs: docker compose --project-directory ${DEPLOY_DIR} logs --tail 80"
    fi
    ps="$(compose_ps 2>/dev/null || true)"
    if ! containers_ready "$ps"; then
      last="db, app, or caddy is not ready"
      echo "smoke: waiting (${last})"
      sleep 5
      continue
    fi
    if ! curl -fsS --max-time 20 "https://${DOMAIN}/healthz" >/dev/null; then
      last="https://${DOMAIN}/healthz did not return success"
      echo "smoke: waiting (${last})"
      sleep 5
      continue
    fi
    rc=0
    tls_check || rc=$?
    if [[ "$rc" -eq 2 ]]; then
      last="TLS handshake for ${DOMAIN}:443 failed"
      echo "smoke: waiting (${last})"
      sleep 5
      continue
    fi
    echo "smoke: https://${DOMAIN}/healthz ok, certificate valid"
    return 0
  done
}

host_checks
wait_for_site
assert_published_ports "$DEPLOY_DIR" || exit 1
echo "smoke: ok https://${DOMAIN}/"
