#!/usr/bin/env bash
# go test ./..., with the root package split across parallel shards.
#
# The root package holds most handler tests. They share one Postgres
# database per test process and never call t.Parallel, so one process runs
# them one at a time. Each shard is a separate process with its own
# database (internal/testdb), which lets them run side by side.
#
#   FOLD_TEST_SHARDS=n   number of root-package shards (default: CPUs/2, 2..8)
# For one test, use go test -run directly.
set -euo pipefail
cd "$(dirname "$0")/.."

cpus=$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 4)
shards=${FOLD_TEST_SHARDS:-$((cpus / 2))}
((shards < 2)) && shards=2
((shards > 8)) && shards=8

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# Every package but the root one, as usual (packages already run in parallel).
mapfile -t others < <(go list ./... | grep -vx fold)
go test "${others[@]}" >"$tmp/others.log" 2>&1 &
others_pid=$!

go test -c -o "$tmp/fold.test" .
mapfile -t tests < <("$tmp/fold.test" -test.list '.*' | grep '^Test')

pids=()
for ((s = 0; s < shards; s++)); do
	names=()
	for ((i = s; i < ${#tests[@]}; i += shards)); do names+=("${tests[i]}"); done
	((${#names[@]} == 0)) && continue
	pattern="^($(IFS='|'; echo "${names[*]}"))\$"
	"$tmp/fold.test" -test.count=1 -test.run "$pattern" >"$tmp/shard$s.log" 2>&1 &
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
	echo "ok  	fold	(${#tests[@]} tests in $shards shards)"
fi

if ! wait "$others_pid"; then status=1; fi
cat "$tmp/others.log"
exit $status
