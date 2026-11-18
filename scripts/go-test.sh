#!/usr/bin/env bash
# go test ./..., with the root package split across parallel shards.
#
# The root package holds most handler tests. They share one Postgres
# database per test process and never call t.Parallel, so one process runs
# them one at a time. Each shard is a separate process with its own
# database (internal/testdb), which lets them run side by side.
#
# Shards are balanced by the seconds in scripts/go-test-durations.txt
# (longest first, each to the least loaded shard); a test it does not list
# counts as 0.1s. The slow tests mostly wait on real deadlines rather than
# use the CPU, so there are more shards than CPUs. Stale numbers only make
# the shards less even; every test still runs exactly once.
#
#   FOLD_TEST_SHARDS=n       number of root-package shards (default: 2 per CPU, 2..12)
#   FOLD_TEST_RECORD=1       rewrite scripts/go-test-durations.txt from this run
# For one test, use go test -run directly.
set -euo pipefail
cd "$(dirname "$0")/.."

cpus=$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 4)
shards=${FOLD_TEST_SHARDS:-$((cpus * 2))}
((shards < 2)) && shards=2
((shards > 12)) && shards=12
durations=scripts/go-test-durations.txt

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# Every package but the root one, as usual (packages already run in parallel).
mapfile -t others < <(go list ./... | grep -vx fold)
go test "${others[@]}" >"$tmp/others.log" 2>&1 &
others_pid=$!

go test -c -o "$tmp/fold.test" .
"$tmp/fold.test" -test.list '.*' | grep '^Test' >"$tmp/tests"

# One line per test: "<shard> <name>".
awk -v n="$shards" '
  FNR == NR { if ($0 !~ /^#/ && NF == 2) secs[$2] = $1; next }
  { w = ($1 in secs) ? secs[$1] : 0.1; printf "%s %s\n", w, $1 }
' "$durations" "$tmp/tests" | sort -k1,1gr -k2,2 | awk -v n="$shards" '
  { best = 0; for (s = 1; s < n; s++) if (load[s] < load[best]) best = s
    load[best] += $1; print best, $2 }
' >"$tmp/plan"

verbose=()
[ -n "${FOLD_TEST_RECORD:-}" ] && verbose=(-test.v)
pids=()
for ((s = 0; s < shards; s++)); do
	mapfile -t names < <(awk -v s="$s" '$1 == s { print $2 }' "$tmp/plan")
	((${#names[@]} == 0)) && continue
	pattern="^($(IFS='|'; echo "${names[*]}"))\$"
	"$tmp/fold.test" -test.count=1 "${verbose[@]}" -test.run "$pattern" >"$tmp/shard$s.log" 2>&1 &
	pids+=("$s:$!")
done

status=0
for entry in "${pids[@]}"; do
	s=${entry%%:*}
	if ! wait "${entry#*:}"; then
		status=1
		echo "--- root package shard $s failed:"
		grep -E -B2 -A20 '^(--- FAIL|panic:|FAIL)' "$tmp/shard$s.log" || tail -50 "$tmp/shard$s.log"
	fi
done
if ((status == 0)); then
	echo "ok  	fold	($(wc -l <"$tmp/tests") tests in $shards shards)"
	if [ -n "${FOLD_TEST_RECORD:-}" ]; then
		{
			echo "# Seconds per root-package test of 0.5s or more, for scripts/go-test.sh's shard plan."
			echo "# Rewrite with FOLD_TEST_RECORD=1 scripts/go-test.sh (FOLD_TEST_SHARDS=8 is close to CI)."
			sed -nE 's/^--- (PASS|SKIP): (Test[^ ]+) \(([0-9.]+)s\)$/\3 \2/p' "$tmp"/shard*.log |
				awk '$1 >= 0.5 { printf "%.1f %s\n", $1, $2 }' | sort -k2,2
		} >"$durations"
		echo "go-test: updated $durations"
	fi
fi

if ! wait "$others_pid"; then status=1; fi
cat "$tmp/others.log"
exit $status
