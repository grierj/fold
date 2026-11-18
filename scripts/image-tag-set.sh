#!/usr/bin/env bash
# Point a registry tag at a digest, for release.yml. An immutable tag is never moved.
#
#   image-tag-set.sh state REPO:TAG sha256:...  prints "absent", "same", or "other sha256:..."
#   image-tag-set.sh keep  REPO:TAG sha256:...  create the tag; one that exists must already be this digest
#   image-tag-set.sh move  REPO:TAG sha256:...  create or move the tag (a convenience tag such as 0.5)
#
# Exit 0 when the command did what it says (and, for keep and move, the tag now
# resolves to the digest). Exit 1 on a refusal, a failed write or an answer from
# the registry that is not a plain "not found" (see image-tag-exists.sh).
set -euo pipefail
export LC_ALL=C

mode="${1:-}"
ref="${2:-}"
digest="${3:-}"
if [[ ! "$mode" =~ ^(state|keep|move)$ || -z "$ref" || ! "$digest" =~ ^sha256:[0-9a-f]{64}$ ]]; then
  echo "usage: image-tag-set.sh state|keep|move REGISTRY/NAME:TAG sha256:DIGEST" >&2
  exit 1
fi
repo="${ref%:*}"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

current() {
  docker buildx imagetools inspect "$ref" --format '{{.Manifest.Digest}}'
}

# 0 exists, 1 absent; anything else stops the run.
exists=0
bash "$here/image-tag-exists.sh" "$ref" || exists=$?
if [[ "$exists" -gt 1 ]]; then
  exit 1
fi

have=
if [[ "$exists" -eq 0 ]]; then
  have="$(current)"
fi

case "$mode" in
  state)
    if [[ -z "$have" ]]; then
      echo absent
    elif [[ "$have" == "$digest" ]]; then
      echo same
    else
      echo "other ${have}"
    fi
    exit 0
    ;;
  keep)
    if [[ -n "$have" ]]; then
      if [[ "$have" == "$digest" ]]; then
        echo "${ref} is already ${digest}"
        exit 0
      fi
      echo "::error::${ref} is already published as ${have}, not ${digest}. A published tag is never moved. Pick a new version." >&2
      exit 1
    fi
    ;;
esac

docker buildx imagetools create --tag "$ref" "${repo}@${digest}"
have="$(current)"
if [[ "$have" != "$digest" ]]; then
  echo "::error::${ref} resolves to ${have} after the write, not ${digest}." >&2
  exit 1
fi
echo "${ref} is ${digest}"
