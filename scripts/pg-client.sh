#!/usr/bin/env bash
# Runs one PostgreSQL client tool (pg_dump, pg_restore, ...) from the official
# postgres:<major> image, on the host network, as the caller. The restore tests
# (restore_fence_test.go) use it when no client of the server's major version is
# on PATH: pg_dump and pg_restore newer than the server write settings the server
# rejects (a PostgreSQL 18 pg_restore sends "SET transaction_timeout" to a
# server 16), and an error like that must stay an error, not be ignored.
#
#   scripts/pg-client.sh <major> <tool> [args...]
#   FOLD_PG_IMAGE=postgres:16-alpine  use another image than postgres:<major>
#
# Every absolute path among the arguments has its directory mounted at the same
# path, so `-f /tmp/x/fence.dump` and a dump file argument work unchanged. The
# database URL keeps pointing at localhost, which --network host reaches.
set -euo pipefail

major=${1:?usage: pg-client.sh <major> <tool> [args...]}
tool=${2:?usage: pg-client.sh <major> <tool> [args...]}
shift 2

mounts=()
declare -A seen=()
for arg in "$@"; do
	if [[ $arg == /* ]]; then
		dir=$(dirname "$arg")
		if [[ -d $dir && -z ${seen[$dir]:-} ]]; then
			seen[$dir]=1
			mounts+=(-v "$dir:$dir")
		fi
	fi
done

exec docker run --rm -i --network host --user "$(id -u):$(id -g)" \
	"${mounts[@]}" --entrypoint "$tool" "${FOLD_PG_IMAGE:-postgres:$major}" "$@"
