#!/usr/bin/env bash
# Is a release's version tag still on the commit its images were built from?
# For release.yml, which checks before it promotes images and again before it
# writes the GitHub release. Version tags must stay where they were pushed.
#
#   release-tag-commit.sh OWNER/REPO v0.5.0 FULL_COMMIT_SHA
#
# Needs GH_TOKEN. Exit 0 only when the remote tag peels to that commit. Any
# other answer (tag moved or gone, GitHub unreadable) is a refusal.
set -euo pipefail
export LC_ALL=C

repo="${1:-}"
tag="${2:-}"
want="${3:-}"
if [[ -z "$repo" || ! "$tag" =~ ^v[0-9] || ! "$want" =~ ^[0-9a-f]{40}$ ]]; then
  echo "usage: release-tag-commit.sh OWNER/REPO vX.Y.Z FULL_COMMIT_SHA" >&2
  exit 1
fi

out="$(gh api "repos/${repo}/git/ref/tags/${tag}" --jq '"\(.object.type) \(.object.sha)"')"
read -r type sha <<<"$out"

# An annotated tag points at a tag object; follow it down to the commit.
for _ in 1 2 3 4 5; do
  [[ "$type" == tag ]] || break
  out="$(gh api "repos/${repo}/git/tags/${sha}" --jq '"\(.object.type) \(.object.sha)"')"
  read -r type sha <<<"$out"
done

if [[ "$type" != commit || "$sha" != "$want" ]]; then
  echo "::error::Tag ${tag} now points at ${type} ${sha}, not the commit ${want} this release was built from. A version tag must never move. Restore the tag to ${want}, or publish the new commit under a new version." >&2
  exit 1
fi
echo "${tag} is ${want}"
