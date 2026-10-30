#!/usr/bin/env bash
# make e2e: Playwright split across parallel shards. Each shard is a whole
# ordinary run (playwright.config.ts) on a slice of the specs: its own fold
# server on its own port, its own fresh <db>_e2e_<n> database, and its own
# seed, so specs still share seed data only with the specs in their shard.
# The fold binary is built once and copied into each shard (e2e/server.sh).
# e2e/shards.mjs balances the slices by the seconds in e2e/durations.json;
# the page checks (a11y, layout) are split into parts to spread them.
#
#   E2E_SHARDS=n            number of shards (default: CPUs, 2..6)
#   E2E_RECORD_DURATIONS=1  rewrite e2e/durations.json from this run
#   extra arguments go to every shard, e.g. e2e/run.sh --grep waitlist
# Traces of failed tests land in test-results/shard-<n>/.
set -euo pipefail
: "${DATABASE_URL:?set DATABASE_URL (make e2e does)}"
cd "$(dirname "$0")/.."

cpus=$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 2)
default=$((cpus < 2 ? 2 : cpus > 6 ? 6 : cpus))
shards=${E2E_SHARDS:-$default}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
(cd .. && go build -o "$tmp/fold" .)
export FOLD_E2E_BIN="$tmp/fold"
rm -rf test-results

started=$(date +%s)
mapfile -t plan < <(node e2e/shards.mjs plan "$shards")
pids=()
for ((s = 1; s <= shards; s++)); do
	IFS='|' read -r parts files <<<"${plan[s - 1]}"
	read -ra specs <<<"$files"
	E2E_SHARD=$s E2E_PORT=$((18090 + s)) E2E_OUTPUT_DIR="test-results/shard-$s" E2E_PARTS="$parts" \
		PLAYWRIGHT_JSON_OUTPUT_NAME="$tmp/shard$s.json" \
		npx playwright test --reporter=list,json seed "${specs[@]}" "$@" >"$tmp/shard$s.log" 2>&1 &
	pids+=("$!")
done

status=0
for ((s = 1; s <= shards; s++)); do
	if wait "${pids[s - 1]}"; then
		echo "shard $s/$shards: $(grep -E '^ +[0-9]+ (passed|skipped|flaky)' "$tmp/shard$s.log" | xargs)"
	else
		status=1
		echo "--- shard $s/$shards failed:"
		cat "$tmp/shard$s.log"
	fi
done
echo "e2e: $shards shards in $(($(date +%s) - started))s"
if ((status == 0)) && [ -n "${E2E_RECORD_DURATIONS:-}" ]; then
	node e2e/shards.mjs record "$tmp"/shard*.json
	echo "e2e: updated e2e/durations.json"
fi
exit $status
