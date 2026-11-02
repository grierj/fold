#!/usr/bin/env bash
# Does a registry tag exist? For release.yml, which must never overwrite one.
#
#   image-tag-exists.sh ghcr.io/grierj/fold:0.5.0
#
# Exit 0: the tag exists. Exit 1: the registry answered the request for exactly
# this reference with "not found", "manifest unknown" or "name unknown", and
# nothing else. Exit 2: anything else (login, credential helper or permission
# trouble, a token endpoint's "404 Not Found", a network or registry error).
# A caller treats exit 2 as "do not publish": an unknown answer is never
# "free to use".
set -uo pipefail
export LC_ALL=C

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

# The whole output must be the one absence line for this exact reference. A
# line that merely contains "not found" somewhere (an auth URL, a missing
# credential helper) is not proof the tag is free.
out="${out%$'\n'}"
out="${out%$'\r'}"
for reason in "not found" "manifest unknown" "name unknown"; do
  if [[ "$out" == "ERROR: ${ref}: ${reason}" ]]; then
    exit 1
  fi
done

echo "image-tag-exists.sh: could not tell whether ${ref} exists:" >&2
printf '%s\n' "$out" >&2
exit 2
