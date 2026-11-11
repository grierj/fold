#!/usr/bin/env bash
# Runs one PostgreSQL client tool (pg_dump, pg_restore, ...) from the official
# postgres:<major> image, as the caller. The restore tests (restore_fence_test.go)
# use it when no client of the server's major version is on PATH: pg_dump and
# pg_restore newer than the server write settings the server rejects (a
# PostgreSQL 18 pg_restore sends "SET transaction_timeout" to a server 16), and
# an error like that must stay an error, not be ignored.
#
#   scripts/pg-client.sh <major> <tool> [args...]
#   scripts/pg-client.sh --resolve <major>   print the local image ID and stop
#
#   FOLD_PG_IMAGE=postgres:16-alpine   use another image than postgres:<major>
#                                      (a name@sha256:... digest is kept as given)
#   FOLD_PG_IMAGE_ID=sha256:...        run exactly this local image ID; no pull
#                                      (what --resolve printed)
#   FOLD_PG_INFO_TIMEOUT=20            seconds to wait for the daemon
#   FOLD_PG_PULL_TIMEOUT=180           seconds for one image pull
#   FOLD_PG_DISCOVERY_TIMEOUT=60       seconds for `<tool> --version`
#
# Every absolute path among the arguments has its directory mounted at the same
# path, so `-f /tmp/x/fence.dump` and a dump file argument work unchanged.
#
# The connection is read from the arguments (a postgres:// URL, a host=
# connection string, or -h/--host with -p/--port):
#   - a TCP host runs on the host network (works on native Linux Docker); the
#     host is probed first, so an unreachable database is reported as such;
#   - a Unix-socket directory (host=/dir, ?host=/dir, -h /dir) is mounted at the
#     same path and the container gets no network.
# Nothing here prints the arguments, so a password in a URL stays out of output.
#
# Exit codes of this script's own diagnostics (anything else is the tool's):
#   70 no container runtime on PATH      71 the daemon does not answer
#   72 the image is unusable             73 the database is unreachable from
#   74 the arguments cannot be run          the container
#
# Written for Bash 3.2 (the macOS /bin/bash): no associative arrays, mapfile,
# case-changing expansions, and an empty array is only expanded behind ${a[@]+...}
# because `set -u` rejects it there.
set -euo pipefail

prog=pg-client
usage="usage: pg-client.sh <major> <tool> [args...] | pg-client.sh --resolve <major>"

die() {
	local code=$1
	shift
	echo "$prog: $*" >&2
	exit "$code"
}

resolve_only=0
if [[ ${1:-} == --resolve ]]; then
	resolve_only=1
	major=${2:?$usage}
	tool=
	shift 2
else
	major=${1:?$usage}
	tool=${2:?$usage}
	shift 2
fi

seconds() { # name default
	local v=${!1:-$2}
	case $v in
	'' | *[!0-9]*) die 74 "$1 must be a whole number of seconds, not \"$v\"" ;;
	esac
	printf '%s' "$v"
}
info_timeout=$(seconds FOLD_PG_INFO_TIMEOUT 20)
pull_timeout=$(seconds FOLD_PG_PULL_TIMEOUT 180)
discovery_timeout=$(seconds FOLD_PG_DISCOVERY_TIMEOUT 60)

work=$(mktemp -d "${TMPDIR:-/tmp}/pg-client.XXXXXX")
trap 'rm -rf "$work"' EXIT

# bounded <seconds> <command...> runs the command with stdout in $work/out and
# stderr in $work/err, and returns 124 when it had to be stopped. Bash 3.2 has no
# `timeout`, and macOS has none either.
bounded() {
	local secs=$1 pid watcher rc=0
	shift
	rm -f "$work/timedout"
	"$@" >"$work/out" 2>"$work/err" </dev/null &
	pid=$!
	(
		exec >/dev/null 2>&1 </dev/null
		sleeper=
		trap 'kill "$sleeper"; exit 0' TERM
		sleep "$secs" &
		sleeper=$!
		wait "$sleeper"
		: >"$work/timedout"
		kill "$pid"
	) &
	watcher=$!
	{ wait "$pid" || rc=$?; } 2>/dev/null
	kill "$watcher" 2>/dev/null || true
	{ wait "$watcher" || true; } 2>/dev/null
	if [[ -e $work/timedout ]]; then
		return 124
	fi
	return "$rc"
}

last_line() { # file
	tail -n 1 "$1" | cut -c1-200
}

# 1. A runtime.
command -v docker >/dev/null 2>&1 ||
	die 70 "no container runtime: docker is not on PATH. Install Docker, or set FOLD_PG_BIN to a directory of PostgreSQL $major client tools"

# 2. A daemon that answers.
rc=0
bounded "$info_timeout" docker info || rc=$?
if [[ $rc -eq 124 ]]; then
	die 71 "the container daemon did not answer within ${info_timeout}s. Start it, or set FOLD_PG_BIN to a directory of PostgreSQL $major client tools"
elif [[ $rc -ne 0 ]]; then
	die 71 "the container daemon is not reachable ($(last_line "$work/err")). Start it, or set FOLD_PG_BIN to a directory of PostgreSQL $major client tools"
fi

# 3. An image, by its immutable local ID for the whole run.
image=${FOLD_PG_IMAGE:-postgres:$major}
image_id=
if [[ -n ${FOLD_PG_IMAGE_ID:-} ]]; then
	rc=0
	bounded "$info_timeout" docker image inspect --format '{{.Id}}' "$FOLD_PG_IMAGE_ID" || rc=$?
	[[ $rc -eq 0 ]] ||
		die 72 "the recorded image $FOLD_PG_IMAGE_ID is not available locally; run scripts/pg-client.sh --resolve $major again"
	image_id=$FOLD_PG_IMAGE_ID
else
	rc=0
	bounded "$info_timeout" docker image inspect --format '{{.Id}}' "$image" || rc=$?
	if [[ $rc -ne 0 ]]; then
		rc=0
		bounded "$pull_timeout" docker pull --quiet "$image" || rc=$?
		if [[ $rc -eq 124 ]]; then
			die 72 "pulling image $image did not finish within ${pull_timeout}s. Pull it yourself, raise FOLD_PG_PULL_TIMEOUT, or set FOLD_PG_BIN to a directory of PostgreSQL $major client tools"
		elif [[ $rc -ne 0 ]]; then
			die 72 "pulling image $image failed ($(last_line "$work/err")). Check the name and the network, or set FOLD_PG_BIN to a directory of PostgreSQL $major client tools"
		fi
		rc=0
		bounded "$info_timeout" docker image inspect --format '{{.Id}}' "$image" || rc=$?
		[[ $rc -eq 0 ]] || die 72 "image $image was pulled but cannot be inspected ($(last_line "$work/err"))"
	fi
	image_id=$(head -n 1 "$work/out")
	[[ -n $image_id ]] || die 72 "image $image has no local ID"
fi

if [[ $resolve_only -eq 1 ]]; then
	echo "$prog: image $image is $image_id" >&2
	echo "$image_id"
	exit 0
fi

# 4. The connection, read from the arguments.
target=none # none | tcp | socket
host=
port=

unescape() { # a URL's %2F is a slash; nothing else is decoded
	local v=${1//\%2F//}
	printf '%s' "${v//\%2f//}"
}

query_param() { # name query
	local rest=$2 pair
	while [[ -n $rest ]]; do
		pair=${rest%%&*}
		if [[ $rest == *"&"* ]]; then
			rest=${rest#*&}
		else
			rest=
		fi
		case $pair in
		"$1="*)
			printf '%s' "${pair#*=}"
			return 0
			;;
		esac
	done
	return 1
}

set_url() {
	local url=$1 rest query='' hostport v
	rest=${url#*://}
	case $rest in
	*"?"*)
		query=${rest#*\?}
		rest=${rest%%\?*}
		;;
	esac
	hostport=${rest%%/*}
	hostport=${hostport##*@}
	case $hostport in
	"["*)
		host=${hostport#\[}
		host=${host%%]*}
		port=${hostport##*]}
		port=${port#:}
		;;
	*:*)
		host=${hostport%%:*}
		port=${hostport##*:}
		;;
	*)
		host=$hostport
		port=
		;;
	esac
	host=$(unescape "$host")
	if v=$(query_param host "$query"); then
		host=$(unescape "$v")
	fi
	if v=$(query_param port "$query"); then
		port=$v
	fi
}

set_conninfo() { # a "host=/tmp port=5432 dbname=x" string; quoted values are not understood
	local tok
	set -f
	for tok in $1; do
		case $tok in
		host=*) host=${tok#host=} ;;
		port=*) port=${tok#port=} ;;
		esac
	done
	set +f
}

dirs=()
add_dir() {
	local d
	if [[ $1 == / ]]; then
		return 0
	fi
	case $1 in
	*:*) die 74 "the directory \"$1\" has a colon in it, which a docker bind mount cannot carry" ;;
	esac
	for d in ${dirs[@]+"${dirs[@]}"}; do
		if [[ $d == "$1" ]]; then
			return 0
		fi
	done
	dirs+=("$1")
}

prev=
for arg in "$@"; do
	case $arg in
	postgres://* | postgresql://*)
		set_url "$arg"
		target=found
		;;
	--host=*)
		host=${arg#--host=}
		target=found
		;;
	--port=*) port=${arg#--port=} ;;
	-*) ;;
	*"host="*)
		set_conninfo "$arg"
		target=found
		;;
	esac
	case $prev in
	-h | --host)
		host=$arg
		target=found
		;;
	-p | --port) port=$arg ;;
	esac
	case $arg in
	/*)
		# A -h value is a socket directory, not a file to mount around.
		case $prev in
		-h | --host) ;;
		*)
			dir=$(dirname "$arg")
			if [[ -d $dir ]]; then
				add_dir "$dir"
			fi
			;;
		esac
		;;
	--host=*) ;;
	--*=/*)
		dir=$(dirname "${arg#*=}")
		if [[ -d $dir ]]; then
			add_dir "$dir"
		fi
		;;
	esac
	prev=$arg
done

if [[ $target == found ]]; then
	[[ -n $host ]] ||
		die 74 "the connection names no host, so it would use the default socket of this machine, which the container cannot see. Name a TCP host, or a socket directory (host=/var/run/postgresql), or set FOLD_PG_BIN to a directory of PostgreSQL $major client tools"
	case $host in
	/*)
		target=socket
		[[ -d $host ]] || die 74 "the socket directory $host does not exist on this machine"
		add_dir "$host"
		;;
	*) target=tcp ;;
	esac
	port=${port:-5432}
	case $port in
	'' | *[!0-9]*) die 74 "the connection's port \"$port\" is not a number" ;;
	esac
fi

mounts=()
for d in ${dirs[@]+"${dirs[@]}"}; do
	mounts+=(-v "$d:$d")
done
net=(--network host)
if [[ $target == socket ]]; then
	net=(--network none)
fi
user=$(id -u):$(id -g)

# 5. A database the container can reach. pg_isready answers 0 (accepting) and
# 1 (starting up or refusing this connection) when something answers; 2 is silence;
# 3 is "no attempt made". The container runs as the caller's uid, which has no
# passwd entry in the image, so libpq cannot name a user of its own and pg_isready
# exits 3 before it connects. -U names one; the probe does not authenticate, so
# the name is never checked.
if [[ $target != none ]]; then
	rc=0
	bounded "$info_timeout" docker run --rm "${net[@]}" --user "$user" \
		${mounts[@]+"${mounts[@]}"} --entrypoint pg_isready "$image_id" -q -U probe -h "$host" -p "$port" -t 10 || rc=$?
	case $rc in
	0 | 1) ;;
	2 | 124)
		case $target in
		socket) where="the Unix socket in $host" ;;
		*) where="$host port $port" ;;
		esac
		die 73 "the database at $where does not answer from inside the container (network mode ${net[1]}). On Docker Desktop, rootless Docker and Podman the container's localhost is not this machine's: set FOLD_PG_BIN to a directory of PostgreSQL $major client tools, or point the connection at a host or socket directory the container can reach"
		;;
	125 | 126 | 127)
		die 72 "image $image ($image_id) could not run pg_isready ($(last_line "$work/err")); FOLD_PG_IMAGE must name an image with PostgreSQL $major client tools"
		;;
	3) die 74 "pg_isready made no attempt (exit 3): it could not use the connection parameters for $host port $port" ;;
	*) die 74 "pg_isready failed (exit $rc)" ;;
	esac
fi

# 6. The tool. Discovery (`--version`) is bounded; a dump or restore is not.
if [[ $# -eq 1 && $1 == --version ]]; then
	rc=0
	bounded "$discovery_timeout" docker run --rm "${net[@]}" --user "$user" \
		${mounts[@]+"${mounts[@]}"} --entrypoint "$tool" "$image_id" "$@" || rc=$?
	if [[ $rc -eq 124 ]]; then
		die 72 "$tool --version in image $image ($image_id) did not finish within ${discovery_timeout}s"
	fi
	cat "$work/out"
	cat "$work/err" >&2
else
	rc=0
	docker run --rm -i "${net[@]}" --user "$user" \
		${mounts[@]+"${mounts[@]}"} --entrypoint "$tool" "$image_id" "$@" || rc=$?
fi
case $rc in
125 | 126 | 127) die 72 "the container could not start $tool from image $image ($image_id), docker exit $rc" ;;
esac
exit "$rc"
