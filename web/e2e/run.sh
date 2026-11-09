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
#
# The shards run the fold server in production mode, where GET /dev/outbox is a
# 404 and the e2e server has no email service (messages.spec.ts and system.spec.ts
# check exactly that). The specs that read a sent email from the dev outbox skip
# themselves there, so one more run, the dev shard, serves them from an
# APP_ENV=dev server of its own. Every spec that mentions /dev/outbox goes
# there, with no list to keep: a new one is picked up by its first use.
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
# -tags e2eclock adds the test-only attempt clock (attempt_clock_e2e.go); server.sh turns it on.
(cd .. && go build -tags e2eclock -o "$tmp/fold" .)
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

# The dev shard: its own port, database ("<db>_e2e_dev") and seed, in dev mode. Left out of the
# durations that E2E_RECORD_DURATIONS writes (its messages.spec.ts run is a second one).
mapfile -t dev_specs < <(grep -l '/dev/outbox' e2e/*.spec.ts | xargs -n1 basename | sort)
# With no spec named, playwright would run the whole suite in dev mode.
if ((${#dev_specs[@]} == 0)); then echo "run.sh: no spec mentions /dev/outbox; the dev shard has nothing to run" >&2; exit 1; fi
E2E_APP_ENV=dev E2E_SHARD=dev E2E_PORT=$((18090 + shards + 1)) E2E_OUTPUT_DIR="test-results/shard-dev" \
	PLAYWRIGHT_JSON_OUTPUT_NAME="$tmp/dev.json" \
	npx playwright test --reporter=list,json seed "${dev_specs[@]}" "$@" >"$tmp/dev.log" 2>&1 &
dev_pid=$!

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
if wait "$dev_pid"; then
	echo "dev shard: $(grep -E '^ +[0-9]+ (passed|skipped|flaky)' "$tmp/dev.log" | xargs)"
else
	status=1
	echo "--- dev shard failed:"
	cat "$tmp/dev.log"
fi
echo "e2e: $shards shards in $(($(date +%s) - started))s"
if ((status == 0)) && [ -n "${E2E_RECORD_DURATIONS:-}" ]; then
	node e2e/shards.mjs record "$tmp"/shard*.json
	echo "e2e: updated e2e/durations.json"
fi
exit $status
