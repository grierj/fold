#!/usr/bin/env bash
# Does a registry tag exist? For release.yml, which must never overwrite one.
#
#   image-tag-exists.sh ghcr.io/grierj/fold:0.5.0
#
# Exit 0: the tag exists. Exit 1: the registry confirmed it does not exist
# ("not found", "manifest unknown", "name unknown"). Exit 2: anything else
# (login or permission trouble, a network or registry error). A caller treats
# exit 2 as "do not publish": an unknown answer is never "free to use".
set -uo pipefail

ref="${1:-}"
if [[ -z "$ref" ]]; then
  echo "usage: image-tag-exists.sh REGISTRY/NAME:TAG" >&2
  exit 2
fi

out="$(docker buildx imagetools inspect "$ref" 2>&1)"
status=$?
if [[ "$status" -eq 0 ]]; then
  exit 0
fi
if grep -qiE 'not found|manifest unknown|name unknown' <<<"$out"; then
  exit 1
fi
echo "image-tag-exists.sh: could not tell whether ${ref} exists:" >&2
printf '%s\n' "$out" >&2
exit 2
