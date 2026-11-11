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
#   FOLD_PG_KILL_GRACE=3               seconds between SIGTERM and SIGKILL when a
#                                      command overruns its limit, or this script
#                                      is itself told to stop
#
# Every command this script starts (docker, and the tool in its container) runs in
# its own process group and its container has a name made here. A command that
# overruns its limit, or this script receiving TERM, INT or HUP, gets SIGTERM, then
# SIGKILL after the grace, for the whole group, and its container is removed
# (`docker rm -f`), so nothing started here outlives it. The container is removed
# whenever its command ends, however it ends: the docker client can die while the
# daemon keeps the container running, and --rm only acts when the container itself
# exits. Only the container named here is removed. The removal is bounded by
# FOLD_PG_INFO_TIMEOUT, as one absolute deadline for all its docker commands; if
# the container is still there after that, the script names it on stderr and keeps
# the command's own exit status. When told to stop, ending the command and the
# removal together take at most FOLD_PG_KILL_GRACE + FOLD_PG_INFO_TIMEOUT plus
# about two seconds, so a caller that signals the script should wait longer than
# that before it kills the script's process group. SIGKILL of the script
# itself cannot be caught; a caller that may do that should signal the script's
# process group first (restore_fence_test.go does).
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
# A host= connection string is read only in its plain form: key=value words with
# no quotes, no backslashes and no spaces around "=". libpq's other forms hide
# words inside a quoted password, so they are refused (74) before any word is
# taken from them; use a postgres:// URL, or FOLD_PG_BIN.
# A URL's host, port and ?host= / ?port= are percent-decoded as libpq does
# (%2F is "/", %20 a blank), to choose the probe, the network and the mounted
# directory (a repeated ?host= or ?port= takes the last, as libpq does); the tool
# still gets the URL as written. A "%" not followed by two
# hex digits, %00, or a control character in the host or port is refused (74). So
# is a comma in the host or port, which libpq reads as a list of hosts.
# Nothing here prints the arguments, so a password in a URL or a connection
# string stays out of output; a diagnostic names only the host and a numeric port.
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
kill_grace=$(seconds FOLD_PG_KILL_GRACE 3)

work=$(mktemp -d "${TMPDIR:-/tmp}/pg-client.XXXXXX")

# The caller's stderr, kept for the one message cleanup may print. A signal that
# arrives inside a `{ wait ...; } 2>/dev/null` runs its trap with that redirection
# still in force, so a plain >&2 there would be lost. Nothing started here keeps it
# open (4>&-), so a caller waiting for the output pipe is not held by a stray child.
exec 4>&2

# What this script has running, for cleanup. Job control makes every `&` command
# the leader of its own process group, so one signal to -$job reaches whatever it
# started, including a child that outlives it; without it a command's children
# could not be found, and a timed-out `docker run` would leave its container.
set -m
job=       # the command being waited for
watch=     # its deadline watcher
container= # the name of the container $job runs, if any
container_seq=0

# stop_group <pid> sends TERM to the pid's process group, waits up to the grace,
# then sends KILL. A command that ignores TERM therefore still ends. It falls
# back to the pid alone where the group does not exist.
stop_group() {
	local pid=$1 left=$kill_grace
	kill -TERM -- "-$pid" 2>/dev/null || kill -TERM "$pid" 2>/dev/null || true
	while [[ $left -gt 0 ]] && kill -0 -- "-$pid" 2>/dev/null; do
		sleep 1
		left=$((left - 1))
	done
	kill -KILL -- "-$pid" 2>/dev/null || kill -KILL "$pid" 2>/dev/null || true
}

# name_container sets $container to a name no other run uses; the `docker run`
# of the caller passes it to --name so the container can be removed by name.
name_container() {
	container_seq=$((container_seq + 1))
	container=$prog-$$-$RANDOM$RANDOM-$container_seq
}

# limited <deadline> <outfile> <command...> runs a short command of the daemon's
# (stdout to the file, stderr dropped) as a job of its own and returns its status.
# The deadline is an absolute $SECONDS value, shared by every command of one
# removal, so the commands together cannot outlast it; one that has no time left is
# not started, and one still running at the deadline is killed. Either returns 137.
# $rmjob names the job while it runs, so cleanup can end it if this script is told
# to stop meanwhile. $SECONDS makes the limit hold whatever granularity `sleep` has.
rmjob=
limited() {
	local deadline=$1 out=$2 rc=0
	shift 2
	[[ $SECONDS -lt $deadline ]] || return 137
	"$@" >"$out" 2>/dev/null </dev/null 4>&- &
	rmjob=$!
	while kill -0 "$rmjob" 2>/dev/null; do
		if [[ $SECONDS -ge $deadline ]]; then
			kill -KILL -- "-$rmjob" 2>/dev/null || kill -KILL "$rmjob" 2>/dev/null || true
			break
		fi
		sleep 0.1 2>/dev/null || sleep 1
	done
	{ wait "$rmjob" || rc=$?; } 2>/dev/null
	rmjob=
	return "$rc"
}

# remove_container <name> <deadline> ends the container with exactly that name, whether or
# not the docker client that started it is still alive, and returns 0 once it is
# gone. `docker rm -f` failing is not yet a failure: the container may be gone
# already (--rm removed it first, the usual case), or be mid-removal, so what
# counts is whether the exact name is still listed. It tries again until the
# absolute $SECONDS deadline, which every command and every pause is checked
# against (a command is given only what remains, and none is started after it),
# then names the container on stderr and returns 1; the caller keeps the status of
# the command it ran. Nothing but this name is touched, and nothing here reads
# $job or $watch.
remove_container() {
	local name=$1 deadline=$2 rc line present
	while [[ $SECONDS -lt $deadline ]]; do
		rc=0
		limited "$deadline" "$work/rm.out" docker rm -f "$name" || rc=$?
		[[ $rc -eq 0 ]] && return 0
		rc=0
		limited "$deadline" "$work/rm.out" docker ps -a --filter "name=$name" --format '{{.Names}}' || rc=$?
		if [[ $rc -eq 0 ]]; then
			present=0
			while IFS= read -r line || [[ -n $line ]]; do
				if [[ $line == "$name" ]]; then
					present=1
				fi
			done <"$work/rm.out"
			[[ $present -eq 1 ]] || return 0
		fi
		[[ $SECONDS -lt $deadline ]] || break
		sleep 0.2 2>/dev/null || sleep 1
	done
	echo "$prog: could not remove container $name; it may still be running. Remove it yourself: docker rm -f $name" >&4
	return 1
}

# cleanup runs on every exit, including TERM, INT and HUP (below). A signal that
# arrives while it runs is ignored, so it finishes. One absolute deadline, the kill
# grace plus the daemon timeout (and a second for $SECONDS' granularity), bounds all
# of it: ending the command, then the removal with whatever time is left. It also
# ends a removal that a signal interrupted, and does that removal again, so a stop
# during cleanup does not abandon the container.
# shellcheck disable=SC2329 # run by the EXIT trap below
cleanup() {
	trap '' TERM INT HUP
	local deadline=$((SECONDS + kill_grace + info_timeout + 1))
	if [[ -n $rmjob ]]; then
		kill -KILL -- "-$rmjob" 2>/dev/null || kill -KILL "$rmjob" 2>/dev/null || true
		rmjob=
	fi
	if [[ -n $watch ]]; then
		kill -KILL -- "-$watch" 2>/dev/null || kill -KILL "$watch" 2>/dev/null || true
	fi
	if [[ -n $job ]]; then
		stop_group "$job"
	fi
	if [[ -n $container ]]; then
		remove_container "$container" "$deadline" || true # a refusal is named on stderr; the exit status stays the signal's
	fi
	rm -rf "$work"
}
trap cleanup EXIT
trap 'exit 143' TERM
trap 'exit 130' INT
trap 'exit 129' HUP

# bounded <seconds> <command...> runs the command with stdout in $work/out and
# stderr in $work/err, and returns 124 when it had to be stopped (TERM, then KILL
# after the grace, for its whole process group; its container, if $container
# names one, is removed). Bash 3.2 has no `timeout`, and macOS has none either.
bounded() {
	local secs=$1 rc=0
	shift
	rm -f "$work/timedout"
	"$@" >"$work/out" 2>"$work/err" </dev/null 4>&- &
	job=$!
	(
		set +m # its sleep stays in the watcher's group, so one KILL ends both
		exec >/dev/null 2>&1 </dev/null 4>&-
		sleep "$secs"
		: >"$work/timedout"
		stop_group "$job"
	) &
	watch=$!
	{ wait "$job" || rc=$?; } 2>/dev/null
	# The command is over; anything it left behind is not wanted.
	kill -KILL -- "-$job" 2>/dev/null || true
	kill -KILL -- "-$watch" 2>/dev/null || kill -KILL "$watch" 2>/dev/null || true
	{ wait "$watch" || true; } 2>/dev/null
	job='' watch=''
	# Ended on time, late or by itself, the docker client's exit says nothing about
	# the container: the daemon owns it. $container stays set until the removal has
	# finished, so a stop meanwhile still removes it.
	if [[ -n $container ]]; then
		remove_container "$container" $((SECONDS + info_timeout + 1)) || true
		container=
	fi
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

url_escape_refused="the connection URL has a \"%\" that is not followed by two hex digits, or a %00, which libpq refuses too. Fix the URL, or set FOLD_PG_BIN to a directory of PostgreSQL $major client tools"
url_control_refused="the connection URL names a host or port with a control character in it once percent-decoded. Fix the URL, or set FOLD_PG_BIN to a directory of PostgreSQL $major client tools"
decoded= # the result of percent_decode

# percent_decode <text> [controls] decodes %XX into $decoded as libpq does a
# connection URL's parts (it does not read "+" as a blank). It returns 1 for a "%"
# without two hex digits after it, or for %00, which libpq refuses; and, when a
# second argument is given, 2 for a decoded control character (below a blank, or
# DEL), which a host or port must not carry. The text may hold a password, so
# nothing here prints it. Written for Bash 3.2: printf -v and 16# arithmetic only.
percent_decode() {
	local rest=$1 out='' hex n byte
	decoded=
	while [[ $rest == *%* ]]; do
		out=$out${rest%%%*}
		rest=${rest#*%}
		hex=${rest:0:2}
		case $hex in
		[0-9A-Fa-f][0-9A-Fa-f]) ;;
		*) return 1 ;;
		esac
		n=$((16#$hex))
		[[ $n -ne 0 ]] || return 1
		if [[ -n ${2:-} ]] && { [[ $n -lt 32 ]] || [[ $n -eq 127 ]]; }; then
			return 2
		fi
		printf -v byte '%b' "\\x$hex"
		out=$out$byte
		rest=${rest:2}
	done
	decoded=$out$rest
}

# url_decode <part> puts the decoded host or port in $decoded, or refuses.
url_decode() {
	local rc=0
	percent_decode "$1" part || rc=$?
	case $rc in
	0) ;;
	1) die 74 "$url_escape_refused" ;;
	*) die 74 "$url_control_refused" ;;
	esac
}

# query_param <name> <query> puts the still-encoded value of the last ?name= in
# $qvalue; the key may itself be percent-encoded, as in libpq.
qvalue=
query_param() {
	local rest=$2 pair key found=1
	while [[ -n $rest ]]; do
		pair=${rest%%&*}
		if [[ $rest == *"&"* ]]; then
			rest=${rest#*&}
		else
			rest=
		fi
		case $pair in
		*=*) ;;
		*) continue ;;
		esac
		key=${pair%%=*}
		if [[ $key == *%* ]]; then
			percent_decode "$key" || continue
			key=$decoded
		fi
		if [[ $key == "$1" ]]; then
			qvalue=${pair#*=}
			found=0
		fi
	done
	return "$found"
}

# set_url reads the host and port of a postgres:// URL. It splits on the URL's own
# delimiters first and decodes each part after, so "%3A" and "%40" in a part never
# split anything. The whole URL is checked for a bad escape first, because libpq
# decodes every part (the password too) and its own complaint repeats the part.
set_url() {
	local url=$1 rest query='' hostport
	rest=${url#*://}
	percent_decode "$rest" || die 74 "$url_escape_refused"
	# libpq ends userinfo at the first @ before the first slash, even when
	# the password contains a question mark.
	case $rest in
	*@*)
		case ${rest%%@*} in
		*/*) ;;
		*) rest=${rest#*@} ;;
		esac
		;;
	esac
	case $rest in
	*"?"*)
		query=${rest#*\?}
		rest=${rest%%\?*}
		;;
	esac
	hostport=${rest%%/*}
	case $hostport in
	*@*) die 74 "the connection URL has an ambiguous host. Fix the URL, or set FOLD_PG_BIN to a directory of PostgreSQL $major client tools" ;;
	esac
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
	url_decode "$host"
	host=$decoded
	url_decode "$port"
	port=$decoded
	if query_param host "$query"; then
		url_decode "$qvalue"
		host=$decoded
	fi
	if query_param port "$query"; then
		url_decode "$qvalue"
		port=$decoded
	fi
}

conninfo_refused="the connection string uses a form this wrapper does not read (quotes, backslashes, or a space around \"=\"), and a word such as host= or port= could hide inside a quoted password. Use a postgres:// URL, or set FOLD_PG_BIN to a directory of PostgreSQL $major client tools"

# names_host: does the string have a word that is a host setting (host=...) or
# the start of one with blanks around "=" ("host = x")? Any other argument is not
# a connection string, and set_conninfo refuses the second form.
names_host() {
	local tok
	set -f
	for tok in $1; do
		case $tok in
		host | host=*)
			set +f
			return 0
			;;
		esac
	done
	set +f
	return 1
}

# set_conninfo reads a plain "host=/tmp port=5432 dbname=x" string and refuses the
# rest. libpq allows quoted and backslash-escaped values and blanks around "=",
# and splitting such a string on blanks would take a word out of the middle of a
# password ("password='a port=5432'") as if it were a setting. So any string that
# has those forms is refused before a word is taken from it, and the refusal says
# nothing about its content. Every word is then key=value; a word ending in "="
# or starting with "=" is a blank around "=" (libpq would read the next word as
# its value).
set_conninfo() {
	local tok
	case $1 in
	*"'"* | *'"'* | *\\* | *$'\r'* | *$'\f'* | *$'\v'*) die 74 "$conninfo_refused" ;;
	esac
	set -f
	for tok in $1; do
		case $tok in
		=* | *=) die 74 "$conninfo_refused" ;;
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
	/*) ;; # a file path is not a connection string
	*)
		if names_host "$arg"; then
			set_conninfo "$arg"
			target=found
		fi
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
	# libpq reads a comma in either as a list of hosts (or ports), which this
	# wrapper does not follow; a decoded %2C is one too.
	case $host$port in
	*,*) die 74 "the connection names a list of hosts or ports (a comma), which this wrapper does not read. Name one host, or set FOLD_PG_BIN to a directory of PostgreSQL $major client tools" ;;
	esac
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
	'' | *[!0-9]*) die 74 "the connection's port is not a number" ;;
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
	name_container
	bounded "$info_timeout" docker run --rm --name "$container" "${net[@]}" --user "$user" \
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

# 6. The tool. Discovery (`--version`) is bounded; a dump or restore is not, but
# it is still a job of its own, so a signal to this script stops it and removes
# its container, and so does the end of the docker client, however it ends.
if [[ $# -eq 1 && $1 == --version ]]; then
	rc=0
	name_container
	bounded "$discovery_timeout" docker run --rm --name "$container" "${net[@]}" --user "$user" \
		${mounts[@]+"${mounts[@]}"} --entrypoint "$tool" "$image_id" "$@" || rc=$?
	if [[ $rc -eq 124 ]]; then
		die 72 "$tool --version in image $image ($image_id) did not finish within ${discovery_timeout}s"
	fi
	cat "$work/out"
	cat "$work/err" >&2
else
	rc=0
	name_container
	# A job in its own process group must not read a terminal (SIGTTIN would stop
	# it); the tool never prompts, since the container has no tty.
	if [[ -t 0 ]]; then
		exec 3</dev/null
	else
		exec 3<&0
	fi
	docker run --rm -i --name "$container" "${net[@]}" --user "$user" \
		${mounts[@]+"${mounts[@]}"} --entrypoint "$tool" "$image_id" "$@" <&3 4>&- &
	job=$!
	exec 3<&-
	{ wait "$job" || rc=$?; } 2>/dev/null
	kill -KILL -- "-$job" 2>/dev/null || true
	job=
	# The tool's status is $rc whatever the removal does.
	remove_container "$container" $((SECONDS + info_timeout + 1)) || true
	container=
fi
case $rc in
125 | 126 | 127) die 72 "the container could not start $tool from image $image ($image_id), docker exit $rc" ;;
esac
exit "$rc"
