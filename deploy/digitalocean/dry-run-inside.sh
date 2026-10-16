#!/usr/bin/env bash
# Assertions for provision.sh --dry-run. Run this inside a fresh container
# (deploy/digitalocean/dry-run-test.sh does that). A real host that already
# has packages installed will fail the "nothing was installed" checks.
set -euo pipefail

cd /src
pub=/tmp/fold-dry-run.pub
printf '%s\n' 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDryRunFoldProvisionKeyNotARealSecret fold-dry-run' >"$pub"

run_provision() {
  bash deploy/digitalocean/provision.sh "$@"
}

set +e
out="$(run_provision --dry-run \
  --domain groups.example.org \
  --admin-user deploy \
  --ssh-pubkey-file "$pub" \
  --image ghcr.io/grierj/fold:0.0.0-dry-run 2>&1)"
status=$?
set -e
printf '%s\n' "$out"
[[ "$status" -eq 0 ]]

needles=(
  'useradd'
  'PermitRootLogin no'
  'ufw'
  'fail2ban'
  'unattended-upgrades'
  'docker-ce'
  'ghcr.io/grierj/fold:0.0.0-dry-run'
  'backup.sh'
  'values not shown'
  'smoke.sh'
  '5432'
)
for needle in "${needles[@]}"; do
  grep -qF -- "$needle" <<<"$out" || {
    echo "missing from dry-run output: $needle" >&2
    exit 1
  }
done

[[ ! -d /opt/fold ]]
[[ ! -e /etc/ssh/sshd_config.d/00-fold-hardening.conf ]]
[[ ! -e /etc/sudoers.d/fold-admin ]]
[[ ! -e /etc/cron.d/fold-backup ]]
if id deploy >/dev/null 2>&1; then
  echo "dry-run created user deploy" >&2
  exit 1
fi
if command -v fail2ban-server >/dev/null 2>&1; then
  echo "dry-run installed fail2ban" >&2
  exit 1
fi
if command -v docker >/dev/null 2>&1; then
  echo "dry-run installed docker" >&2
  exit 1
fi

set +e
run_provision --dry-run --domain groups.example.org --ssh-pubkey-file "$pub" >/tmp/fold-no-image.out 2>&1
status=$?
set -e
[[ "$status" -eq 2 ]]

set +e
build_out="$(run_provision --dry-run \
  --domain groups.example.org \
  --ssh-pubkey-file "$pub" \
  --allow-onbox-build 2>&1)"
status=$?
set -e
[[ "$status" -eq 0 ]]
grep -qF -- '--build' <<<"$build_out"

set +e
run_provision --dry-run \
  --domain groups.example.org \
  --ssh-pubkey-file /etc/hostname \
  --skip-deploy >/tmp/fold-bad-key.out 2>&1
status=$?
set -e
[[ "$status" -eq 1 ]]

set +e
run_provision --dry-run \
  --domain groups.example.org \
  --image ghcr.io/grierj/fold:0.0.0-dry-run \
  --allow-onbox-build \
  --ssh-pubkey-file "$pub" >/tmp/fold-both.out 2>&1
status=$?
set -e
[[ "$status" -eq 1 ]]

set +e
sec_out="$(bash deploy/digitalocean/backup.sh --dry-run \
  --deploy-dir /opt/fold \
  --env-file deploy/digitalocean/backup.env.example 2>&1)"
status=$?
set -e
[[ "$status" -eq 0 ]]
if grep -qF -- 'CHANGE_ME_SECRET' <<<"$sec_out"; then
  echo "backup dry-run printed a secret" >&2
  exit 1
fi
grep -qF -- 'dry-run:' <<<"$sec_out"
grep -qF -- 'example-fold-backups' <<<"$sec_out"
grep -qF -- 'offsite:' <<<"$sec_out"

set +e
local_out="$(bash deploy/digitalocean/backup.sh --dry-run \
  --deploy-dir /opt/fold \
  --env-file /tmp/does-not-exist-backup.env 2>&1)"
status=$?
set -e
[[ "$status" -eq 0 ]]
grep -qF -- 'offsite not configured' <<<"$local_out"

# Still nothing created by the backup dry-run.
[[ ! -d /var/backups/fold ]]
[[ ! -d /opt/fold ]]

set +e
run_provision --dry-run \
  --domain groups.example.org \
  --ssh-pubkey-file "$pub" \
  --image ghcr.io/grierj/fold:latest >/tmp/fold-latest.out 2>&1
status=$?
set -e
[[ "$status" -ne 0 ]]
grep -q 'latest' /tmp/fold-latest.out

dns_out="$(bash deploy/digitalocean/email-dns.sh --dry-run --domain groups.example.org 2>&1)"
grep -q 'dig +short' <<<"$dns_out"
grep -q 'POSTMARK_ACCOUNT_TOKEN' <<<"$dns_out"

echo "dry-run ok"
bash deploy/digitalocean/idempotency-inside.sh
