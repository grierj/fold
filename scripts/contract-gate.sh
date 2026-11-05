#!/usr/bin/env bash
# Contract migration gate (PLAN 14.1, DEC-138). Run from CI's test job on the
# real checkout (`bash scripts/contract-gate.sh`), or by hand in any clone.
#
# The rule: a contract migration (one that drops or renames what an earlier
# release still reads) names its expand migration on a header line,
#
#   -- contracts: 00002_suppression_address_hash.sql
#
# and CI fails unless that expand shipped in an earlier tagged release.
#
# This script checks the tag-and-file half:
#   - the checkout is a git repository with full history and tags;
#   - every `contracts` header is exactly `-- contracts: NNNNN_name.sql`, once
#     per file (a loose or repeated line is an error, not a skip);
#   - the named expand exists, is a different file and has a lower number;
#   - a v* tag merged into HEAD contains the expand and not the contract;
#   - the expand is unchanged since every such tag (a tagged release applied it).
# A repository with no contract file prints one line and passes.
#
# Review checks the publication half, which a tag alone does not prove: a
# release.yml run of shape N (one run on the tag with plan, publish, smoke,
# finalize and notes all successful) or F (a publish run completed by a
# finalize_only run on that tag), the finalize "Record the digest" step
# summary, the GHCR manifest digest, the GitHub release's fold-release
# section, and `gh attestation verify`, all naming one digest. This script
# uses no network and cannot see any of that.
set -euo pipefail
export LC_ALL=C

root=$(git rev-parse --show-toplevel 2>/dev/null) || {
  echo "contract-gate: not a git repository" >&2
  exit 1
}
cd "$root"

if [[ "$(git rev-parse --is-shallow-repository)" == "true" ]]; then
  echo "contract-gate: shallow checkout; fetch full history and tags (actions/checkout fetch-depth: 0)" >&2
  exit 1
fi

loose='^--[[:space:]]*[Cc]ontracts[[:space:]]*:'
exact='^-- contracts: ([0-9]{5}_[a-z0-9_]+[.]sql)$'

shopt -s nullglob
failed=0
found=0

mapfile -t tags < <(git tag --merged HEAD --list 'v*')

fail() {
  echo "contract-gate: $*" >&2
  failed=1
}

for path in migrations/[0-9][0-9][0-9][0-9][0-9]_*.sql; do
  contract=${path#migrations/}
  mapfile -t lines < <(grep -E "$loose" "$path" || true)
  if [[ ${#lines[@]} -eq 0 ]]; then
    continue
  fi
  found=$((found + 1))
  if [[ ${#lines[@]} -ne 1 ]] || ! [[ ${lines[0]} =~ $exact ]]; then
    fail "$contract: malformed contracts line (want exactly one line: -- contracts: NNNNN_name.sql)"
    continue
  fi
  expand=${BASH_REMATCH[1]}

  if [[ "$expand" == "$contract" ]]; then
    fail "$contract: names itself as its expand"
    continue
  fi
  if ((10#${expand:0:5} >= 10#${contract:0:5})); then
    fail "$contract: expand $expand must have a lower number"
    continue
  fi
  if ! git cat-file -e "HEAD:migrations/$expand" 2>/dev/null; then
    fail "$contract: expand $expand does not exist in migrations/"
    continue
  fi

  qualifying=()
  for tag in ${tags[@]+"${tags[@]}"}; do
    if git cat-file -e "$tag:migrations/$expand" 2>/dev/null &&
      ! git cat-file -e "$tag:migrations/$contract" 2>/dev/null; then
      qualifying+=("$tag")
    fi
  done
  if [[ ${#qualifying[@]} -eq 0 ]]; then
    fail "$contract: no earlier v* tag contains $expand without the contract"
    continue
  fi

  edited=0
  for tag in "${qualifying[@]}"; do
    if ! git diff --quiet "$tag" HEAD -- "migrations/$expand"; then
      fail "$expand changed after $tag"
      edited=1
    fi
  done
  if [[ $edited -eq 0 ]]; then
    echo "contract-gate: $contract contracts $expand (shipped in ${qualifying[*]})"
  fi
done

if [[ $failed -ne 0 ]]; then
  exit 1
fi
if [[ $found -eq 0 ]]; then
  echo 'contract-gate: no contract migrations in migrations/ (no "-- contracts:" line)'
  exit 0
fi
echo "contract-gate: publication evidence for this tag must be recorded in the PR: release.yml run shape N or F, finalize step summary, GHCR digest, GitHub release, attestation (PLAN 14.1; AGENTS)"
