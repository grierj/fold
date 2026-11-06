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
  'FOLD_VERSION=0.0.0-dry-run'
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
# The build path copies the build override and points COMPOSE_FILE at it; the
# pull path never mentions it.
grep -qF -- 'docker-compose.build.yml' <<<"$build_out"
grep -qF -- 'COMPOSE_FILE' <<<"$build_out"
if grep -qF -- 'COMPOSE_FILE' <<<"$out"; then
  echo "image dry-run mentioned COMPOSE_FILE" >&2
  exit 1
fi
if grep -qF -- 'docker-compose.build.yml' <<<"$out" && ! grep -qF -- 'remove any docker-compose.build.yml' <<<"$out"; then
  echo "image dry-run copies the build override" >&2
  exit 1
fi

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

# A tag-less reference is refused, including a registry port that looks like one.
set +e
run_provision --dry-run \
  --domain groups.example.org \
  --ssh-pubkey-file "$pub" \
  --image localhost:5000/fold >/tmp/fold-notag.out 2>&1
status=$?
set -e
[[ "$status" -ne 0 ]]
grep -q 'no tag' /tmp/fold-notag.out

# The official image takes an exact release only: not a moving minor tag
# (the workflow publishes :0.5), and not a local label like dev.
for moving in 0.5 0 dev v0.5.0 0.5.0.1; do
  set +e
  run_provision --dry-run \
    --domain groups.example.org \
    --ssh-pubkey-file "$pub" \
    --image "ghcr.io/grierj/fold:${moving}" >/tmp/fold-moving.out 2>&1
  status=$?
  set -e
  [[ "$status" -ne 0 ]] || { echo "accepted ghcr.io/grierj/fold:${moving}" >&2; exit 1; }
  grep -q 'not an exact release' /tmp/fold-moving.out
done
# An exact release, a prerelease and a full commit SHA are accepted.
for exact in 0.5.0 0.5.0-rc.1 0123456789abcdef0123456789abcdef01234567; do
  run_provision --dry-run \
    --domain groups.example.org \
    --ssh-pubkey-file "$pub" \
    --image "ghcr.io/grierj/fold:${exact}" >/tmp/fold-exact.out 2>&1
done

dns_out="$(bash deploy/digitalocean/email-dns.sh --dry-run --domain groups.example.org 2>&1)"
grep -q 'dig +short' <<<"$dns_out"
grep -q 'POSTMARK_ACCOUNT_TOKEN' <<<"$dns_out"

# scripts/image-tag-exists.sh (release.yml's no-overwrite check) fails closed:
# only a confirmed "not found" means the tag is free.
stub_dir="$(mktemp -d)"
cat >"$stub_dir/docker" <<'STUB'
#!/bin/bash
printf '%s\n' "${STUB_OUTPUT:-}" >&2
exit "${STUB_STATUS:-0}"
STUB
chmod +x "$stub_dir/docker"
# expect_tag_status WANT DOCKER_STATUS DOCKER_OUTPUT
expect_tag_status() {
  local want="$1" got=0
  PATH="$stub_dir:$PATH" STUB_STATUS="$2" STUB_OUTPUT="$3" \
    bash scripts/image-tag-exists.sh ghcr.io/grierj/fold:0.5.0 >/dev/null 2>&1 || got=$?
  if [[ "$got" -ne "$want" ]]; then
    echo "image-tag-exists.sh said $got for '$3'; want $want" >&2
    exit 1
  fi
}
expect_tag_status 0 0 'Name: ghcr.io/grierj/fold:0.5.0'
expect_tag_status 1 1 'ERROR: ghcr.io/grierj/fold:0.5.0: not found'
expect_tag_status 1 1 'ERROR: ghcr.io/grierj/fold:0.5.0: manifest unknown'
expect_tag_status 1 1 'ERROR: ghcr.io/grierj/fold:0.5.0: name unknown'
# Anything else, including text that merely contains "not found", is unknown.
expect_tag_status 2 1 'ERROR: manifest unknown'
expect_tag_status 2 1 'ERROR: ghcr.io/grierj/fold:0.4.0: not found'
expect_tag_status 2 1 'ERROR: failed to authorize: failed to fetch anonymous token: unexpected status from GET request to https://ghcr.io/token?scope=repository%3Agrierj%2Ffold%3Apull: 404 Not Found'
expect_tag_status 2 1 'error getting credentials - err: exec: "docker-credential-desktop": executable file not found in PATH, out: '
expect_tag_status 2 1 'ERROR: denied: permission_denied: write_package'
expect_tag_status 2 1 'ERROR: ghcr.io/grierj/fold:0.5.0: denied'
expect_tag_status 2 1 'ERROR: Get "https://ghcr.io/v2/": dial tcp: i/o timeout'
expect_tag_status 2 1 $'ERROR: ghcr.io/grierj/fold:0.5.0: not found\nERROR: also something else'
rm -rf "$stub_dir"

echo "dry-run ok"
bash deploy/digitalocean/idempotency-inside.sh

bash deploy/digitalocean/dry-run-test.sh --fixtures
