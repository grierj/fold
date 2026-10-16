#!/usr/bin/env bash
# Run the provisioner --dry-run inside a container and prove it changes nothing.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
image="${FOLD_DRY_RUN_IMAGE:-ubuntu:24.04}"

if ! command -v docker >/dev/null 2>&1; then
  echo "dry-run-test.sh: docker is required" >&2
  exit 1
fi

# Comments may say the scripts never call doctl. A command would not be a comment.
tracked="$(git -C "$root" ls-files -- .env backup.env)"
if [[ -n "$tracked" ]]; then
  echo "dry-run-test.sh: secret env file is tracked: $tracked" >&2
  exit 1
fi

# The test harness names those commands so it can search for them.
# The scripts that run on a server must not.
hits="$(grep -nE 'doctl|droplet create|spaces create' \
  "$root/deploy/digitalocean/provision.sh" \
  "$root/deploy/digitalocean/backup.sh" \
  "$root/deploy/digitalocean/smoke.sh" \
  "$root/deploy/digitalocean/email-dns.sh" \
  "$root/deploy/digitalocean/upgrade.sh" \
  "$root/deploy/digitalocean/checks.sh" || true)"
if [[ -n "$hits" ]] && grep -vE ':[0-9]+:[[:space:]]*#' <<<"$hits"; then
  echo "dry-run-test.sh: a script contains a DigitalOcean create command" >&2
  exit 1
fi

before="$(git -C "$root" status --porcelain)"
docker run --rm \
  -v "$root:/src:ro" \
  -w /src \
  "$image" \
  bash /src/deploy/digitalocean/dry-run-inside.sh
after="$(git -C "$root" status --porcelain)"
if [[ "$before" != "$after" ]]; then
  echo "dry-run-test.sh: the worktree changed" >&2
  git -C "$root" status --porcelain >&2
  exit 1
fi

echo "dry-run-test.sh: ok"
