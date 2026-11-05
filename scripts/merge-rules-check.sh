#!/usr/bin/env bash
# Master merge rules check (DEC-146). CI's test job runs
# `bash scripts/merge-rules-check.sh`; anyone can run it by hand.
#
# Why: two pull requests can each add the next migration number (say 00005)
# and each pass CI alone. Git merges the second without a conflict, because
# the file names differ, and master then holds a duplicate version that goose
# refuses at boot. GitHub stops the second merge only while master's rules
# require branches to be up to date before merging (strict required status
# checks): a pull request then merges only when its head contains master's
# tip and the required checks passed on that head, so CI saw the tree that
# lands. This script fails when the active rules for master stop saying so.
# It requires:
#   - a pull_request rule (every writer, admins included, goes through a PR);
#   - a non_fast_forward rule (master's history only grows);
#   - a required_status_checks rule with strict_required_status_checks_policy
#     true, and test, web and provision among the required checks.
#
# It reads GET /repos/<repo>/rules/branches/master, the rules GitHub enforces
# on master from every active ruleset. The endpoint is public for a public
# repository; GH_TOKEN (CI's github.token) is sent when set, for the rate
# limit. It cannot see bypass actors, which only an admin can read; review
# checks those (DEC-146). Any answer it cannot read is a failure.
#
#   merge-rules-check.sh                    fetch the live rules
#   merge-rules-check.sh --rules-file FILE  check a saved response (tests)
set -euo pipefail
export LC_ALL=C

usage() {
  echo "usage: merge-rules-check.sh [--rules-file FILE]" >&2
  exit 2
}

command -v jq >/dev/null || {
  echo "merge-rules-check: jq is required" >&2
  exit 1
}

rules_file=""
case "$#" in
  0) ;;
  2) [[ "$1" == "--rules-file" ]] || usage; rules_file="$2" ;;
  *) usage ;;
esac

if [[ -n "$rules_file" ]]; then
  rules=$(cat -- "$rules_file")
else
  repo="${GITHUB_REPOSITORY:-grierj/fold}"
  url="${GITHUB_API_URL:-https://api.github.com}/repos/$repo/rules/branches/master"
  headers=(-H "Accept: application/vnd.github+json" -H "X-GitHub-Api-Version: 2022-11-28")
  if [[ -n "${GH_TOKEN:-}" ]]; then
    headers+=(-H "Authorization: Bearer $GH_TOKEN")
  fi
  rules=$(curl -fsS --retry 3 --retry-all-errors --max-time 30 "${headers[@]}" "$url") || {
    echo "merge-rules-check: could not read $url" >&2
    exit 1
  }
fi

# One line per missing rule; nothing when every rule is in place.
problems=$(jq -r '
  def required: ["test", "web", "provision"];
  if type != "array" then
    "the answer is not a list of rules"
  else
    (if any(.[]; .type == "pull_request") then empty
     else "no pull_request rule: a writer could push to master without a pull request" end),
    (if any(.[]; .type == "non_fast_forward") then empty
     else "no non_fast_forward rule: master history could be rewritten" end),
    (if any(.[]; .type == "required_status_checks"
                 and .parameters.strict_required_status_checks_policy == true) then empty
     else "required status checks are not strict: turn on \"Require branches to be up to date before merging\" for master, or two pull requests can merge the same migration number" end),
    ([.[] | select(.type == "required_status_checks")
          | .parameters.required_status_checks[]?.context] as $have
     | required[] | select(. as $c | $have | index($c) | not)
     | "required status check \(.) is not required on master")
  end
' <<<"$rules") || {
  echo "merge-rules-check: the rules answer is not JSON" >&2
  exit 1
}

if [[ -n "$problems" ]]; then
  while IFS= read -r p; do
    echo "merge-rules-check: $p" >&2
  done <<<"$problems"
  exit 1
fi
echo "merge-rules-check: master requires pull requests, up-to-date branches and the test, web and provision checks"
