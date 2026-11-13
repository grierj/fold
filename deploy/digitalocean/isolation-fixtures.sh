#!/usr/bin/env bash
# Daemon-free regressions for the drills' isolation (V-08): the pure checks in
# isolation.sh, the restore drill's refusal to start outside a throwaway
# container, the project names, and the nested launcher driven by a stub docker.
# No Docker daemon is started or contacted; nothing is written outside a temp dir.
# shellcheck disable=SC2016,SC2030,SC2031,SC2015 # patterns keep their dollar signs literal; the stubbed calls export into their own subshell on purpose
set -Eeuo pipefail
exec </dev/null
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
HERE="$ROOT/deploy/digitalocean"
# shellcheck source=deploy/digitalocean/isolation.sh
source "$HERE/isolation.sh"
WORK="$(mktemp -d /tmp/fold-isolation-fixture.XXXXXX)"
active_runner=""
fixture_launching=0 fixture_pending=0
fixture_signal() {
  if (( fixture_launching )); then
    (( fixture_pending != 0 )) || fixture_pending=$1
    return
  fi
  exit "$1"
}
start_owned_runner() {
  fixture_launching=1
  "$@" &
  active_runner=$!
  fixture_launching=0
  if (( fixture_pending )); then exit "$fixture_pending"; fi
}
# shellcheck disable=SC2329 # EXIT trap.
finish() { local rc=$?; trap '' INT TERM HUP; if [[ -n "${OWNER_FINISH:-}" ]]; then : >"$OWNER_FINISH" || true; fi; if [[ -n "$active_runner" ]]; then wait "$active_runner" 2>/dev/null || true; fi; rm -rf -- "$WORK"; exit "$rc"; }
trap finish EXIT
trap 'fixture_signal 130' INT
trap 'fixture_signal 143' TERM
trap 'fixture_signal 129' HUP
trap 'printf "isolation fixture failed at line %s: %s\n" "$LINENO" "$BASH_COMMAND" >&2' ERR
refused() { local needle="$1" rc=0; shift; "$@" >"$WORK/out" 2>&1 || rc=$?; [[ "$rc" != 0 ]]; [[ -z "$needle" ]] || grep -qF -- "$needle" "$WORK/out"; }

# Interrupt the actual ownership helpers at registration and during wait. The child
# can only write its completion marker while the fixture's tools still exist.
declare -f fixture_signal start_owned_runner finish >"$WORK/owner-functions"
cat >"$WORK/owner-hook" <<'HOOK'
[[ ${0##*/} == fixture-owner.sh ]] || return 0
set -T
owner_interrupt() {
  [[ $BASHPID == "$$" && ! -e $OWNER_SIGNAL ]] || return 0
  if [[ $OWNER_MODE == launch && $BASH_COMMAND == 'active_runner=$!' ]] ||
     [[ $OWNER_MODE == registered && $BASH_COMMAND == 'wait "$active_runner"' ]]; then
    : >"$OWNER_SIGNAL"
    builtin kill -TERM "$$"
  fi
}
trap owner_interrupt DEBUG
HOOK
cat >"$WORK/fixture-owner.sh" <<'OWNER'
set -Eeuo pipefail
source "$OWNER_FUNCTIONS"
WORK=$OWNER_DIR; active_runner=""; fixture_launching=0; fixture_pending=0
trap finish EXIT
trap 'fixture_signal 143' TERM
start_owned_runner bash -c 'for ((i=0; i<500; i++)); do [[ ! -e $3 ]] || break; sleep 0.02; done; [[ -e $3 && -f $1/tool ]] && : >"$2"' _ "$WORK" "$OWNER_SETTLED" "$OWNER_RELEASE"
wait "$active_runner"
active_runner=""
OWNER
for owner_mode in launch registered; do
  owner_dir="$WORK/owner-$owner_mode"; mkdir "$owner_dir"; : >"$owner_dir/tool"
  start_owned_runner timeout --foreground -s KILL 15 env BASH_ENV="$WORK/owner-hook" OWNER_MODE="$owner_mode" OWNER_FUNCTIONS="$WORK/owner-functions" OWNER_DIR="$owner_dir" OWNER_SIGNAL="$WORK/owner-$owner_mode.signal" OWNER_SETTLED="$WORK/owner-$owner_mode.settled" OWNER_FINISH="$WORK/owner-$owner_mode.finish" OWNER_RELEASE="$WORK/owner-$owner_mode.release" \
    bash "$WORK/fixture-owner.sh"
  for ((i=0; i<500; i++)); do [[ ! -e "$WORK/owner-$owner_mode.finish" ]] || break; sleep 0.02; done
  [[ -e "$WORK/owner-$owner_mode.finish" && ! -e "$WORK/owner-$owner_mode.settled" && -e "$owner_dir/tool" ]] || { echo "isolation: owner did not hold its child at $owner_mode" >&2; exit 1; }
  : >"$WORK/owner-$owner_mode.release"
  owner_rc=0; wait "$active_runner" || owner_rc=$?; active_runner=""
  [[ "$owner_rc" == 143 && -e "$WORK/owner-$owner_mode.signal" && -e "$WORK/owner-$owner_mode.settled" && ! -e "$owner_dir" ]] || { echo "isolation: owner interruption failed at $owner_mode" >&2; exit 1; }
done

rc=0; bash "$HERE/isolation.sh" >"$WORK/direct.log" 2>&1 || rc=$?
[[ "$rc" == 1 ]]; grep -q 'source from a drill only' "$WORK/direct.log"

# --- mountinfo ---------------------------------------------------------------------------------------
cat >"$WORK/mi.clean" <<'M'
36 35 0:50 / / rw,relatime - overlay overlay rw,lowerdir=/x
37 36 0:5 / /proc rw,nosuid - proc proc rw
38 36 0:51 / /etc/cron.d ro - tmpfs tmpfs rw
39 36 8:1 /var/lib/docker/volumes/v/_data /var/lib/docker rw - ext4 /dev/sda1 rw
40 36 8:1 /srv/data /opt/fold ro,relatime - ext4 /dev/sda1 rw
M
[[ -z "$(isolation_mount_problems "$WORK/mi.clean")" ]]
cat >"$WORK/mi.sock" <<'M'
36 35 0:50 / / rw - overlay overlay rw
41 36 0:25 /docker.sock /var/run/docker.sock rw - tmpfs tmpfs rw
M
[[ "$(isolation_mount_problems "$WORK/mi.sock")" == "the Docker socket /var/run/docker.sock is a mount: the daemon would be the host's" ]]
sed 's|/var/run/docker.sock|/run/docker.sock|' "$WORK/mi.sock" >"$WORK/mi.sock2"
isolation_mount_problems "$WORK/mi.sock2" | grep '/run/docker.sock is a mount' >/dev/null
cat >"$WORK/mi.owner" <<'M'
36 35 0:50 / / rw - overlay overlay rw
42 36 8:1 /home/me/etc /etc rw,relatime - ext4 /dev/sda1 rw
43 36 8:1 /home/me/opt /opt/fold rw - ext4 /dev/sda1 rw
44 36 8:1 /home/me/ro /opt ro - ext4 /dev/sda1 rw
45 36 8:1 / /home rw - ext4 /dev/sda2 rw
M
[[ "$(isolation_mount_problems "$WORK/mi.owner" | sort | paste -sd'|')" == "writable bind mount of /etc (from /home/me/etc)|writable bind mount of /opt/fold (from /home/me/opt)|writable mount of /home (a whole filesystem, mount root /)" ]]
# The sockets-only mode (qc1-measure.sh) ignores owner mounts and still sees a socket mount.
[[ -z "$(isolation_mount_problems "$WORK/mi.owner" sockets)" ]]
cat "$WORK/mi.owner" "$WORK/mi.sock" >"$WORK/mi.both"; [[ "$(isolation_mount_problems "$WORK/mi.both" sockets | wc -l)" == 1 ]]

# --- existing configuration: refuse, never preserve ------------------------------------------------------
: >"$WORK/exists"; ln -s "$WORK/nowhere" "$WORK/dangling"
[[ -z "$(isolation_preexisting_problems "$WORK/absent")" ]]
isolation_preexisting_problems "$WORK/exists" | grep 'already exists; the drill would overwrite or later remove an owner' >/dev/null
isolation_preexisting_problems "$WORK/dangling" | grep 'already exists' >/dev/null
[[ "$(isolation_preexisting_problems "$WORK/exists" "$WORK/absent" "$WORK/dangling" | wc -l)" == 2 ]]
# Every host path the drill's provisioners write is on the list, and the list is what the crossings library cleans.
for p in /etc/cron.d/fold-health /etc/cron.d/fold-backup /etc/logrotate.d/fold-backup /opt/fold /etc/sudoers.d/fold-admin; do
  printf '%s\n' "${ISOLATION_HOST_PATHS[@]}" | grep -qx "$p"
done
# shellcheck source=deploy/digitalocean/checks.sh
source "$HERE/checks.sh"
# shellcheck source=deploy/digitalocean/restore-drill-crossings.sh
source "$HERE/restore-drill-crossings.sh"
for p in "${CROSSING_HOST_FILES[@]}"; do printf '%s\n' "${ISOLATION_HOST_PATHS[@]}" | grep -qx "$p"; done
# What the provisioner writes with --skip-harden: every such /etc path in provision.sh that the list lacks fails here.
while read -r p; do
  printf '%s\n' "${ISOLATION_HOST_PATHS[@]}" | grep -qx "$p" || { echo "isolation: provision.sh writes $p, which the drill does not guard" >&2; exit 1; }
done < <(grep -oE 'write_file /etc/[a-zA-Z0-9_./-]+' "$HERE/provision.sh" | awk '{print $2}' | sort -u)

# --- the daemon --------------------------------------------------------------------------------------------
[[ -z "$(isolation_daemon_problems '{"Name":"fold-drill-1","ContainersRunning":0}' fold-drill-1)" ]]
isolation_daemon_problems '{"Name":"prod-droplet","ContainersRunning":0}' fold-drill-1 | grep 'not a daemon of this filesystem' >/dev/null
isolation_daemon_problems '{"Name":"fold-drill-1","ContainersRunning":4}' fold-drill-1 | grep 'already runs 4 container' >/dev/null
[[ "$(isolation_daemon_problems '{"Name":"x","ContainersRunning":2}' y | wc -l)" == 2 ]]

# --- the whole guard ----------------------------------------------------------------------------------------
GOODINFO='{"Name":"fold-drill-1","ContainersRunning":0}'
guard() { FOLD_DRILL_ISOLATED_DAEMON="${ISO-1}" FOLD_DRILL_ASSUME_CONTAINER="${CONT-1}" DOCKER_HOST="${DH:-}" isolation_require_disposable "${MI:-$WORK/mi.clean}" "${HOST:-fold-drill-1}" "${INFO:-$GOODINFO}" "${PATHS:-$WORK/absent}"; }
guard
ISO='' refused 'restore-drill-nested.sh' guard
if [[ ! -e /.dockerenv ]]; then refused 'not a container filesystem' bash -c 'source "'"$HERE"'/isolation.sh"; unset FOLD_DRILL_ASSUME_CONTAINER; FOLD_DRILL_ISOLATED_DAEMON=1 isolation_require_disposable /proc/self/mountinfo h "{\"Name\":\"h\",\"ContainersRunning\":0}" /nonexistent-fold-path'; fi # inside a container the check passes by design
DH=tcp://10.0.0.5:2375 refused 'DOCKER_HOST points elsewhere' guard
DH=unix:///var/run/docker.sock guard
MI="$WORK/mi.sock" refused 'the Docker socket' guard
MI="$WORK/mi.owner" refused 'writable bind mount of /etc' guard
HOST=prod-droplet refused 'not a daemon of this filesystem' guard
INFO='{"Name":"fold-drill-1","ContainersRunning":2}' refused 'already runs 2 container' guard
PATHS="$WORK/exists" refused 'already exists' guard
# Everything wrong at once lists everything wrong.
ISO='' MI="$WORK/mi.both" HOST=x INFO='{"Name":"y","ContainersRunning":1}' PATHS="$WORK/exists" refused 'already exists' guard
[[ "$(grep -c '^  - ' "$WORK/out")" -ge 5 ]]
# Defaults: with no paths given the real list is checked (none of it exists on this machine, or the guard rightly refuses).
for p in "${ISOLATION_HOST_PATHS[@]}"; do :; done

# --- the restore drill refuses first -------------------------------------------------------------------------
RD="$HERE/restore-drill.sh"
iso_line="$(grep -n '^isolation_require_disposable || exit 1' "$RD" | cut -d: -f1)"
for later in '^\[\[ "\$(id -u)" == 0 \]\]' '^REVISION=' '^crossing_validate_revisions' '^drill_progress_init' '^mkdir ' '^"\$ROOT/deploy/digitalocean/deploy-drill.sh"'; do
  line="$(grep -n -- "$later" "$RD" | head -n 1 | cut -d: -f1)"
  [[ -n "$line" && "$iso_line" -lt "$line" ]] || { echo "isolation: the guard does not come before $later" >&2; exit 1; }
done
grep -q '^source "\$ROOT/deploy/digitalocean/isolation.sh"' "$RD"
mkdir "$WORK/stubbin"
cat >"$WORK/stubbin/docker" <<'STUB'
#!/usr/bin/env bash
line=; sep=; for a in "$@"; do line+="$sep${a//$'\n'/\\n}"; sep=" "; done; printf '%s\n' "$line" >>"$STUB_LOG"   # one line per call; a newline inside an argument is written as \n
[[ $1 != run ]] || : >"$STUB_LOG.argv"; { sep=; for a in "$@"; do printf '%s%q' "$sep" "$a"; sep=" "; done; printf '\n'; } >>"$STUB_LOG.argv"   # the same call with every argument %q-quoted, so that where one ends and the next begins survives (a launch starts with `run`, which starts the file anew)
case "$1" in
  info) printf '{"Name":"%s","ContainersRunning":0}\n' "${STUB_NAME:-the-host}" ;;
  run) printf 'c%.0s' {1..64}; echo ;;
  inspect)
    case "$*" in
      *NetworkMode*) echo "${STUB_NET:-bridge}" ;;
      *Mounts*) printf '%b' "${STUB_MOUNTS:-/var/lib/docker /var/lib/docker/volumes/x/_data\n}" ;;
    esac ;;
  exec) case "$*" in
    *fold-owned-scan*) echo 0 ;;
    *fold-listener-scan*)
      if [[ "${STUB_LISTEN_HANG:-0}" == 1 ]]; then
        trap '' TERM
        sleep 60 & child=$!
        printf '%s\n%s\n' "$$" "$child" >"$STUB_PIDS"
        wait "$child"
      fi
      printf "%s\n" "${STUB_LISTEN-checked ipv4 ipv6}"; exit "${STUB_LISTEN_RC:-0}" ;;
  esac ;;
esac
exit 0
STUB
chmod +x "$WORK/stubbin/docker"
: >"$WORK/stub.log"
rc=0; STUB_LOG="$WORK/stub.log" PATH="$WORK/stubbin:$PATH" bash "$RD" --dir "$WORK/never" >"$WORK/rd.out" 2>&1 || rc=$?
[[ "$rc" == 1 ]] && grep -q 'isolation: refusing to run' "$WORK/rd.out" && grep -q 'restore-drill-nested.sh' "$WORK/rd.out"
[[ ! -e "$WORK/never" ]]
if grep -qE '^(run|rm|network|volume|build|compose|pull)( |$)' "$WORK/stub.log"; then echo 'isolation: the refused drill touched the daemon' >&2; cat "$WORK/stub.log" >&2; exit 1; fi
# Even flagged as nested, a drill on a daemon not named for this host is refused.
: >"$WORK/stub.log"
rc=0; FOLD_DRILL_ISOLATED_DAEMON=1 FOLD_DRILL_ASSUME_CONTAINER=1 STUB_NAME=some-other-host STUB_LOG="$WORK/stub.log" PATH="$WORK/stubbin:$PATH" bash "$RD" --dir "$WORK/never" >"$WORK/rd2.out" 2>&1 || rc=$?
[[ "$rc" == 1 ]] && grep -q 'not a daemon of this filesystem' "$WORK/rd2.out"

# --- per-run project names ------------------------------------------------------------------------------------------
CR="$HERE/restore-drill-crossings.sh"
grep -q 'CX_PROJECT="fold-crossing-\${CROSSING_RUN:?crossing_prepare sets the run id}-\$name"' "$CR"
grep -q 'CROSSING_RUN="\$(isolation_run_id)"' "$CR"
if grep -n 'CX_PROJECT="fold-crossing-\$name"' "$CR"; then echo 'isolation: a fixed crossing project name is back' >&2; exit 1; fi
a="$(isolation_run_id)"; b="$(isolation_run_id)"; [[ "$a" =~ ^[0-9a-f]{12}$ && "$b" =~ ^[0-9a-f]{12}$ && "$a" != "$b" ]]
# Every Docker and Compose name the drills make carries a run id: none is a fixed string.
if grep -nE 'COMPOSE_PROJECT_NAME=[a-z]' "$HERE/restore-drill-crossings.sh" "$HERE/restore-drill.sh" "$HERE/deploy-drill.sh" | grep -v '\$'; then echo 'isolation: a fixed Compose project name' >&2; exit 1; fi
grep -q 'PROJECT="fold-deploy-drill-\$(od -An -N6 -tx1 /dev/urandom' "$HERE/deploy-drill.sh"
if grep -nE 'docker (run|create)[^|]* --name [a-z]' "$HERE/restore-drill.sh" "$HERE/restore-drill-cases.sh" "$HERE/restore-drill-crossings.sh" "$HERE/deploy-drill.sh" | grep -v '\$'; then echo 'isolation: a fixed container name' >&2; exit 1; fi

# --- the nested launcher against a stub docker -------------------------------------------------------------------------
LA="$HERE/restore-drill-nested.sh"
help="$(bash "$LA" --help)"; for flag in --out --dind-image; do grep -q -- "$flag" <<<"$help"; done
CLONE="$WORK/clone"; mkdir -p "$CLONE/deploy/digitalocean"
cp "$LA" "$CLONE/deploy/digitalocean/"
git init -q "$CLONE"; git -C "$CLONE" config user.name fixture; git -C "$CLONE" config user.email fixture@example.invalid
git -C "$CLONE" config commit.gpgsign false; git -C "$CLONE" config core.hooksPath /dev/null
git -C "$CLONE" add -A; git -C "$CLONE" commit -qm clone
launch() { STUB_LOG="$WORK/stub.log" PATH="$WORK/stubbin:$PATH" bash "$CLONE/deploy/digitalocean/restore-drill-nested.sh" "$@"; }
# WRAP, the supervisor every docker exec runs the workload under, as the runner defines it (WRAP_ARG). The stub's argv file (stub.log.argv) keeps every argument apart, so the check is
# that WRAP is exactly one argument after bash -c and the workload's words follow it one by one. The supervisor's contract, read from the script itself: it loads the
# Chromium the preparation found, polls for the stop request and TERMs the workload's group; it does not exec the command in its place.
WRAP_ARG="$(sed -n "/^WRAP=\$(cat <<'WRAP'\$/,/^WRAP\$/p" "$LA" | sed '1d;$d')"
[[ -n "$WRAP_ARG" ]]
grep -qF 'prep/browser.env' <<<"$WRAP_ARG"; grep -qF 'stop-request' <<<"$WRAP_ARG"; grep -qF 'kill -TERM -- "-$g"' <<<"$WRAP_ARG"
if grep -qF 'exec "$@"' <<<"$WRAP_ARG"; then echo 'the supervisor execs the command in its place again' >&2; exit 1; fi
# wrapped_call LOG WORDS...: LOG (the stub's argv file: one call per line, each argument %q-quoted, so no two arguments can read as one) holds a docker exec whose arguments END with
# bash, -c, the WRAP text as exactly ONE argument, _, bash and then each of WORDS as an argument of its own. The negative case below shows that WRAP and the workload's words
# joined into one argument are refused.
WRAP_Q="$(printf '%q' "$WRAP_ARG")"
wrapped_call() { local log=$1 want="bash -c $WRAP_Q _ bash" a l; shift; for a in "$@"; do want+=" $(printf '%q' "$a")"; done; while IFS= read -r l; do [[ "$l" == "exec "*" $want" ]] && return 0; done <"$log"; return 1; }
printf 'exec -w /work/fold c bash -c %q\n' "$WRAP_ARG _ bash x y" >"$WORK/joined.argv"
if wrapped_call "$WORK/joined.argv" x y; then echo 'wrapped_call accepts WRAP and the workload joined into one argument' >&2; exit 1; fi
printf 'exec -w /work/fold c bash -c %q _ bash x y\n' "$WRAP_ARG" >"$WORK/exact.argv"
wrapped_call "$WORK/exact.argv" x y
: >"$WORK/stub.log"
launch --out "$WORK/ev1" -- --keep >"$WORK/l1.out" 2>&1
[[ -d "$WORK/ev1" ]] && grep -q 'drill exit=0' "$WORK/l1.out"
run_line="$(grep '^run ' "$WORK/stub.log")"
[[ "$(grep -c '^run ' "$WORK/stub.log")" == 1 ]]
# One container, privileged, named and hostnamed for this run, with no mounts, host network or socket.
grep -qE -- '--name fold-drill-[0-9a-f]{12} --hostname fold-drill-[0-9a-f]{12} --privileged -e DOCKER_TLS_CERTDIR= docker:29-dind@sha256:[0-9a-f]{64} dockerd --host=unix:///var/run/docker.sock$' <<<"$run_line"
if grep -qE -- ' -v |--volume|--mount|--network|--net |docker\.sock|--pid|--ipc|--userns|--cap-add|--publish| -p | -P |--expose' <<<"${run_line% dockerd --host=unix:///var/run/docker.sock}"; then echo "isolation: the nested container is given more than it needs: $run_line" >&2; exit 1; fi
# The daemon's own command line. The image's ENTRYPOINT (dockerd-entrypoint.sh) puts tcp://0.0.0.0:2375 in front of any arguments that are empty or start with `-`, so the first word
# after the image has to be `dockerd` itself, with exactly one --host, the unix socket. check_dind_argv reads the argv file (its first line is this launch's `run`, every argument
# quoted) and takes the image as the one argument before `dockerd`: the default is a digest-pinned reference, and --dind-image is passed as given.
check_dind_argv() { # IMAGE_PATTERN
  local -a v
  eval "v=($(head -n 1 "$WORK/stub.log.argv"))"
  [[ "${#v[@]}" == 12 && "${v[0]}" == run && "${v[1]}" == -d && "${v[2]}" == --name && "${v[4]}" == --hostname && "${v[6]}" == --privileged && "${v[7]}" == -e && "${v[8]}" == DOCKER_TLS_CERTDIR= ]] \
    || { echo "isolation: the nested launch is not the expected twelve arguments: ${v[*]}" >&2; exit 1; }
  [[ "${v[9]}" =~ $1 && "${v[10]}" == dockerd && "${v[11]}" == --host=unix:///var/run/docker.sock ]] || { echo "isolation: the daemon is not started as 'dockerd --host=unix:///var/run/docker.sock' after the image: ${v[*]:9}" >&2; exit 1; }
}
check_dind_argv '^docker:29-dind@sha256:[0-9a-f]{64}$'
# The digest is the multi-arch index of docker:29-dind, as the launcher says; the fixture only requires that it is a full sha256 and that the tag it came from is still named.
grep -qE '^DIND_DEFAULT=docker:29-dind@sha256:[0-9a-f]{64}$' "$LA"; grep -q '^OUT="" DIND=\$DIND_DEFAULT ' "$LA"
# The listening-socket lookup is the launcher's own LISTEN_SCAN, passed as ONE argument of `sh -c`, followed by its name and exactly the two files it reads, after the daemon answers
# (`docker info`) and before anything is installed or run.
LISTEN_TXT="$(sed -n "/^LISTEN_SCAN=\$(cat <<'LISTEN'\$/,/^LISTEN\$/p" "$LA" | sed '1d;$d')"
[[ -n "$LISTEN_TXT" ]]
scan_n=0; scan_argv=
while IFS= read -r l; do if [[ "$l" == *" fold-listener-scan "* ]]; then scan_n=$((scan_n + 1)); scan_argv=$l; fi; done <"$WORK/stub.log.argv"
[[ "$scan_n" == 1 ]]
declare -a scan_v; eval "scan_v=($scan_argv)"
[[ "${#scan_v[@]}" == 8 && "${scan_v[0]}" == exec && "${scan_v[1]}" =~ ^c{64}$ && "${scan_v[2]}" == sh && "${scan_v[3]}" == -c && "${scan_v[4]}" == "$LISTEN_TXT" && "${scan_v[5]}" == fold-listener-scan && "${scan_v[6]}" == /proc/net/tcp && "${scan_v[7]}" == /proc/net/tcp6 ]]
info_l="$(grep -n '^exec c\{64\} docker info$' "$WORK/stub.log" | head -n 1 | cut -d: -f1)"; scan_l="$(grep -n ' fold-listener-scan /proc/net/tcp /proc/net/tcp6$' "$WORK/stub.log" | head -n 1 | cut -d: -f1)"
apk_l="$(grep -n '^exec c\{64\} apk add' "$WORK/stub.log" | head -n 1 | cut -d: -f1)"
[[ -n "$info_l" && -n "$scan_l" && -n "$apk_l" && "$info_l" -lt "$scan_l" && "$scan_l" -lt "$apk_l" ]]
# The lookup itself, run for real on made-up /proc/net/tcp files (a LISTEN row is state 0A; 2375 is 0x0947, 2376 is 0x0948): a listener on either port at any address, in either file, is
# printed; an established connection on 2375, listeners elsewhere and a file that cannot be read are not; and the count says how many files it could read.
mkdir "$WORK/proc"
tcp_hdr='  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode'
row() { printf '   0: %s 00000000:0000 %s 00000000:00000000 00:00000000 00000000     0        0 1 1 x\n' "$1" "$2"; }
{ echo "$tcp_hdr"; row 00000000:0050 0A; row 0100007F:0BB8 0A; row 0100007F:0947 01; row 0100007F:0948 06; } >"$WORK/proc/clean4"
{ echo "$tcp_hdr"; row 00000000000000000000000000000000:01BB 0A; } >"$WORK/proc/clean6"
{ echo "$tcp_hdr"; row 00000000:0947 0A; } >"$WORK/proc/bad4"
{ echo "$tcp_hdr"; row 0100007F:0948 0A; } >"$WORK/proc/loop4"
{ echo "$tcp_hdr"; row 00000000000000000000000000000000:0947 0A; } >"$WORK/proc/bad6"
echo "$tcp_hdr" >"$WORK/proc/empty"
scan() { sh -c "$LISTEN_TXT" fold-listener-scan "$@"; }
[[ "$(scan "$WORK/proc/clean4" "$WORK/proc/clean6")" == 'checked ipv4 ipv6' ]]
[[ "$(scan "$WORK/proc/empty" "$WORK/proc/absent")" == 'checked ipv4' ]]
if scan "$WORK/proc/absent" "$WORK/proc/clean6" >"$WORK/scan.out" 2>&1; then echo 'isolation: missing IPv4 table was accepted' >&2; exit 1; fi
if scan "$WORK/proc/clean4" "$WORK/proc" >"$WORK/scan.out" 2>&1; then echo 'isolation: unreadable IPv6 table was accepted' >&2; exit 1; fi
[[ "$(scan "$WORK/proc/bad4" "$WORK/proc/clean6")" == "$WORK/proc/bad4 listens on tcp 00000000:0947"$'\n''checked ipv4 ipv6' ]]
[[ "$(scan "$WORK/proc/clean4" "$WORK/proc/bad6")" == "$WORK/proc/bad6 listens on tcp 00000000000000000000000000000000:0947"$'\n''checked ipv4 ipv6' ]]
[[ "$(scan "$WORK/proc/loop4" "$WORK/proc/clean6")" == "$WORK/proc/loop4 listens on tcp 0100007F:0948"$'\n''checked ipv4 ipv6' ]]
# The repository goes in as a bundle by cp, the drill runs inside with the flag, the evidence comes out by cp, and the container is removed by id.
grep -q '^cp .*fold.bundle ccc' "$WORK/stub.log"; grep -q "^cp ccc[c]*:/work/out/. $WORK/ev1/" "$WORK/stub.log"
grep -q '^exec -e FOLD_NESTED_RUN=[0-9a-f]\{12\} -e FOLD_DRILL_ISOLATED_DAEMON=1 -e QC1_NO_SANDBOX=1 -e QC1_WEB_DIR=/work/fold/web -w /work/fold c\{64\} bash -c .* _ bash deploy/digitalocean/restore-drill.sh --dir /work/out --keep$' "$WORK/stub.log"
# The drill's browser is prepared first, in the same container, and the Chromium it found is the one the run is given.
prep_l="$(grep -n 'browser-prepare.sh /work/prep$' "$WORK/stub.log" | head -n 1 | cut -d: -f1)"; run_l="$(grep -n 'restore-drill.sh --dir /work/out' "$WORK/stub.log" | head -n 1 | cut -d: -f1)"
[[ -n "$prep_l" && -n "$run_l" && "$prep_l" -lt "$run_l" ]]
wrapped_call "$WORK/stub.log.argv" deploy/digitalocean/qc1/browser-prepare.sh /work/prep; wrapped_call "$WORK/stub.log.argv" deploy/digitalocean/restore-drill.sh --dir /work/out --keep   # the preparation and the drill, each under the exact supervisor (one argument), which loads the prepared Chromium, then its own words as separate arguments
grep -qE '^rm -fv c{64}$' "$WORK/stub.log"
# The override: any reference goes in exactly as given, with the same daemon command line.
: >"$WORK/stub.log"
launch --out "$WORK/ev1o" --dind-image example.invalid/dind:override -- --keep >"$WORK/l1o.out" 2>&1
grep -q 'drill exit=0' "$WORK/l1o.out"
check_dind_argv '^example\.invalid/dind:override$'
# Refusals leave the container removed and the drill unrun.
refuse_launch() { : >"$WORK/stub.log"; local rc=0; "$@" >"$WORK/lr.out" 2>&1 || rc=$?; [[ "$rc" == 1 ]]; }
# A daemon that is not shown to be unix-only is refused as soon as it answers, before anything is installed or run in it: a TCP 2375 or 2376 listener, a lookup that fails (even if its
# text would pass), prints nothing, finds no readable file or prints anything else is no proof, and the container is removed by id.
refuse_tcp() { # NAME OUTPUT [RC]
  STUB_LISTEN="$2" STUB_LISTEN_RC="${3:-0}" refuse_launch launch --out "$WORK/ev-$1"
  grep -q 'not shown to listen on its unix socket only' "$WORK/lr.out"; grep -qE '^rm -fv c{64}$' "$WORK/stub.log"
  grep -q ' fold-listener-scan /proc/net/tcp /proc/net/tcp6$' "$WORK/stub.log"
  if grep -qE 'apk add|restore-drill.sh|qc1-measure.sh|browser-prepare.sh|^cp .*fold.bundle' "$WORK/stub.log"; then echo "isolation: $1: the run went on after the TCP check refused it" >&2; exit 1; fi
}
refuse_tcp tcp2375 $'/proc/net/tcp listens on tcp 00000000:0947\nchecked ipv4 ipv6'
grep -q 'listens on tcp 00000000:0947' "$WORK/lr.out"
refuse_tcp tcp2376 $'/proc/net/tcp6 listens on tcp 00000000000000000000000000000000:0948\nchecked ipv4 ipv6'
refuse_tcp silent ''
refuse_tcp none 'checked 0'
refuse_tcp odd 'checked 3'
refuse_tcp noise $'warning\nchecked ipv4 ipv6'
refuse_tcp failed 'checked ipv4 ipv6' 1
# A stalled client and its child must not outlive the call or cancellation of the runner alone.
cat >"$WORK/signal-probe.sh" <<'HOOK'
[[ ${0##*/} == restore-drill-nested.sh ]] || return 0
set -T
signal_probe() {
  [[ $BASHPID == "$$" && -s $STUB_PIDS && ! -e $SIGNAL_MARK ]] || return 0
  : >"$SIGNAL_MARK"
  sleep 0.3
  builtin kill -TERM "$$"
}
trap signal_probe DEBUG
HOOK
for mode in timeout term; do
  : >"$WORK/stub.log"; rm -f "$WORK/probe-pids" "$WORK/probe-signalled"
  start=$SECONDS
  hook=""; call_timeout=2
  if [[ "$mode" == term ]]; then hook="$WORK/signal-probe.sh"; call_timeout=15; fi
  start_owned_runner timeout --foreground -s KILL 30 env STUB_LOG="$WORK/stub.log" STUB_PIDS="$WORK/probe-pids" SIGNAL_MARK="$WORK/probe-signalled" BASH_ENV="$hook" STUB_LISTEN_HANG=1 FOLD_NESTED_CALL_TIMEOUT="$call_timeout" FOLD_NESTED_CLEANUP_BUDGET=12 PATH="$WORK/stubbin:$PATH" \
    bash "$CLONE/deploy/digitalocean/restore-drill-nested.sh" --out "$WORK/ev-hang-$mode" >"$WORK/hang-$mode.out" 2>&1
  rc=0; wait "$active_runner" || rc=$?; active_runner=""
  [[ -s "$WORK/probe-pids" ]] || { echo 'isolation: listener probe did not start' >&2; exit 1; }
  if [[ "$mode" == term ]]; then [[ "$rc" == 143 && -e "$WORK/probe-signalled" ]]; else [[ "$rc" == 1 && ! -e "$WORK/probe-signalled" ]]; fi
  (( SECONDS - start < 20 ))
  grep -qE '^rm -fv c{64}$' "$WORK/stub.log"
  if grep -qE 'apk add|restore-drill.sh|qc1-measure.sh|browser-prepare.sh|^cp .*fold.bundle' "$WORK/stub.log"; then echo "isolation: $mode continued after a stalled probe" >&2; exit 1; fi
  while IFS= read -r p; do
    state="$(ps -o stat= -p "$p" 2>/dev/null || true)"
    [[ -z "$state" || "$state" == Z* ]] || { echo "isolation: $mode left a probe process alive" >&2; exit 1; }
  done <"$WORK/probe-pids"
done
STUB_MOUNTS='/var/run/docker.sock /var/run/docker.sock\n' refuse_launch launch --out "$WORK/ev2"
grep -q 'mounts beyond its own /var/lib/docker' "$WORK/lr.out"; grep -qE '^rm -fv c{64}$' "$WORK/stub.log"; if grep -q 'restore-drill.sh' "$WORK/stub.log"; then exit 1; fi
STUB_MOUNTS='/var/lib/docker /v\n/work /home/me/work\n' refuse_launch launch --out "$WORK/ev3"
grep -q 'mounts beyond' "$WORK/lr.out"; if grep -q 'restore-drill.sh' "$WORK/stub.log"; then exit 1; fi
STUB_NET=host refuse_launch launch --out "$WORK/ev4"
grep -q 'uses the host network' "$WORK/lr.out"; grep -qE '^rm -fv c{64}$' "$WORK/stub.log"
DOCKER_HOST=tcp://10.0.0.9:2375 refuse_launch launch --out "$WORK/ev5"
grep -q 'DOCKER_HOST is remote' "$WORK/lr.out"; [[ ! -s "$WORK/stub.log" ]]
mkdir "$WORK/ev6"; refuse_launch launch --out "$WORK/ev6"; grep -q 'already exists' "$WORK/lr.out"
printf '\n# dirty\n' >>"$CLONE/deploy/digitalocean/restore-drill-nested.sh"
refuse_launch launch --out "$WORK/ev7"; grep -q 'the tree is dirty' "$WORK/lr.out"; [[ ! -s "$WORK/stub.log" ]]
git -C "$CLONE" checkout -q -- deploy/digitalocean/restore-drill-nested.sh
# A drill that fails is reported with its exit code and its evidence still comes out.
cat >"$WORK/stubbin/docker" <<'STUB'
#!/usr/bin/env bash
line=; sep=; for a in "$@"; do line+="$sep${a//$'\n'/\\n}"; sep=" "; done; printf '%s\n' "$line" >>"$STUB_LOG"   # one line per call; a newline inside an argument is written as \n
[[ $1 != run ]] || : >"$STUB_LOG.argv"; { sep=; for a in "$@"; do printf '%s%q' "$sep" "$a"; sep=" "; done; printf '\n'; } >>"$STUB_LOG.argv"   # the same call with every argument %q-quoted, so that where one ends and the next begins survives (a launch starts with `run`, which starts the file anew)
case "$1" in
  info) echo '{"Name":"x","ContainersRunning":0}' ;;
  run) printf 'c%.0s' {1..64}; echo ;;
  inspect) case "$*" in *NetworkMode*) echo bridge ;; *Mounts*) echo '/var/lib/docker /v' ;; esac ;;
  exec) case "$*" in *fold-owned-scan*) echo 0 ;; *fold-listener-scan*) echo "checked ipv4 ipv6" ;; *restore-drill.sh*) exit 7 ;; esac ;;
esac
exit 0
STUB
: >"$WORK/stub.log"; rc=0; launch --out "$WORK/ev8" >"$WORK/l8.out" 2>&1 || rc=$?
[[ "$rc" == 7 ]] && grep -q 'drill exit=7' "$WORK/l8.out" && grep -q "^cp ccc[c]*:/work/out/. $WORK/ev8/" "$WORK/stub.log" && grep -qE '^rm -fv c{64}$' "$WORK/stub.log"

# --- the os-release stand-in (V-08/T07B-2-V: provision.sh check_os refuses Alpine; upgrade.sh never forwards --allow-os) -----------------------------
# Everything below happens in a temp tree; nothing outside it is read or written.
OS="$WORK/os"; mkdir -p "$OS/etc" "$OS/usr/lib" "$OS/state"
printf 'NAME="Alpine Linux"\nID=alpine\nVERSION_ID=3.24.4\nPRETTY_NAME="Alpine Linux v3.24"\n' >"$OS/usr/lib/os-release"
mi_clean="$OS/mi.clean"; printf '36 35 0:50 / / rw,relatime - overlay overlay rw\n37 36 0:5 / /proc rw - proc proc rw\n' >"$mi_clean"
snap_tree() { (cd "$OS" && find . -path ./state -prune -o -print | sort | while read -r p; do if [[ -L "$p" ]]; then echo "L $p $(readlink "$p")"; elif [[ -f "$p" ]]; then echo "F $p $(sha256sum <"$p" | cut -c1-16)"; else echo "D $p"; fi; done); }
standin() { FOLD_DRILL_ISOLATED_DAEMON="${ISO-1}" FOLD_DRILL_ASSUME_CONTAINER=1 isolation_os_standin "$OS/state/s.json" "$OS/etc/os-release" "${MI:-$mi_clean}"; }
# (1) Alpine's real shape: /etc/os-release is a symlink to ../usr/lib/os-release. The stand-in replaces the LINK, never writes through it.
ln -s ../usr/lib/os-release "$OS/etc/os-release"
before="$(snap_tree)"
standin
[[ ! -L "$OS/etc/os-release" && -f "$OS/etc/os-release" ]]
[[ "$(isolation_os_identity "$OS/etc/os-release")" == 'ubuntu 24.04' ]]
[[ "$(isolation_os_identity "$OS/usr/lib/os-release")" == 'alpine 3.24.4' ]]   # the link target is untouched
jq -e '.applied == true and .restored == false and .path == "'"$OS"'/etc/os-release" and .original.kind == "link" and .original.linkTarget == "../usr/lib/os-release" and .original.id == "alpine" and .original.versionId == "3.24.4" and .standIn.id == "ubuntu" and .standIn.versionId == "24.04" and (.reason | test("upgrade.sh never forwards --allow-os"))' "$OS/state/s.json" >/dev/null
[[ "$(find "$OS/etc" -type f | wc -l)" == 1 ]]   # no stray temp file
refused 'already exists' standin   # applied once
isolation_os_restore "$OS/state/s.json"
[[ "$(snap_tree)" == "$before" ]]; [[ -L "$OS/etc/os-release" && "$(readlink "$OS/etc/os-release")" == ../usr/lib/os-release ]]
jq -e '.restored == true' "$OS/state/s.json" >/dev/null
isolation_os_restore "$OS/state/s.json"   # nothing more to do
[[ "$(snap_tree)" == "$before" ]]
# (2) a regular file is saved byte for byte and comes back
rm -f "$OS/state/s.json"* "$OS/etc/os-release"; printf 'ID=alpine\nVERSION_ID=3.19.1\nHOME_URL="https://x"\n' >"$OS/etc/os-release"; chmod 640 "$OS/etc/os-release"
before="$(snap_tree)"; mode_before="$(stat -c %a "$OS/etc/os-release")"
standin; [[ "$(isolation_os_identity "$OS/etc/os-release")" == 'ubuntu 24.04' ]]; jq -e '.original.kind == "file" and (.original.backup | endswith("s.json.orig"))' "$OS/state/s.json" >/dev/null
isolation_os_restore "$OS/state/s.json"; [[ "$(snap_tree)" == "$before" ]]; [[ "$(stat -c %a "$OS/etc/os-release")" == "$mode_before" ]]
# (3) no file at all: created, then removed
rm -f "$OS/state/s.json"* "$OS/etc/os-release"; before="$(snap_tree)"
standin; [[ "$(isolation_os_identity "$OS/etc/os-release")" == 'ubuntu 24.04' ]]; jq -e '.original.kind == "absent"' "$OS/state/s.json" >/dev/null
isolation_os_restore "$OS/state/s.json"; [[ "$(snap_tree)" == "$before" ]]
# (4) already a supported Ubuntu (26.04): nothing is written, and restore has nothing to do
rm -f "$OS/state/s.json"*; printf 'ID=ubuntu\nVERSION_ID="26.04"\n' >"$OS/etc/os-release"; before="$(snap_tree)"
standin; [[ "$(snap_tree)" == "$before" ]]; jq -e '.applied == false and .restored == null and .original.versionId == "26.04"' "$OS/state/s.json" >/dev/null
isolation_os_restore "$OS/state/s.json"; [[ "$(snap_tree)" == "$before" ]]
# an Ubuntu this provisioner does not claim (22.04) is replaced like any other
rm -f "$OS/state/s.json"*; printf 'ID=ubuntu\nVERSION_ID="22.04"\n' >"$OS/etc/os-release"; standin; [[ "$(isolation_os_identity "$OS/etc/os-release")" == 'ubuntu 24.04' ]]; isolation_os_restore "$OS/state/s.json"
[[ "$(isolation_os_identity "$OS/etc/os-release")" == 'ubuntu 22.04' ]]
# (5) refusals write nothing: not the isolated daemon; a Docker socket mount; a writable owner mount of /etc; the file itself a mount point
rm -f "$OS/state/s.json"* "$OS/etc/os-release"; ln -s ../usr/lib/os-release "$OS/etc/os-release"
printf '%s\n' '36 35 0:50 / / rw - overlay overlay rw' '38 36 0:60 / /var/run/docker.sock rw - tmpfs tmpfs rw' >"$OS/mi.sock"
printf '%s\n' '36 35 0:50 / / rw - overlay overlay rw' '39 36 8:1 /srv/host-etc /etc rw - ext4 /dev/sda1 rw' >"$OS/mi.etc"
printf '%s\n' '36 35 0:50 / / rw - overlay overlay rw' "40 36 8:1 /host/os-release $OS/usr/lib/os-release ro - ext4 /dev/sda1 rw" >"$OS/mi.file"
before="$(snap_tree)"
ISO='' refused 'FOLD_DRILL_ISOLATED_DAEMON is not 1' standin
MI="$OS/mi.sock" refused 'Docker socket' standin
MI="$OS/mi.etc" refused 'writable bind mount of /etc' standin
MI="$OS/mi.file" refused 'is a mount point' standin
if [[ ! -e /.dockerenv ]]; then
  rc=0; FOLD_DRILL_ISOLATED_DAEMON=1 isolation_os_standin "$OS/state/s.json" "$OS/etc/os-release" "$mi_clean" >"$WORK/out" 2>&1 || rc=$?
  [[ "$rc" != 0 ]] && grep -q 'not a container filesystem' "$WORK/out"
fi
[[ "$(snap_tree)" == "$before" ]]; [[ ! -e "$OS/state/s.json" ]]
# a restore that cannot prove the original (its backup vanished) fails loudly
rm -f "$OS/state/s.json"* "$OS/etc/os-release"; printf 'ID=alpine\nVERSION_ID=3.1\n' >"$OS/etc/os-release"; standin; rm -f "$OS/state/s.json.orig"
rc=0; isolation_os_restore "$OS/state/s.json" 2>/dev/null || rc=$?; [[ "$rc" != 0 ]]
# (6) the provisioner's OWN check_os is the judge: accepts the stand-in, refuses Alpine, and a script without check_os is refused
printf 'ID=ubuntu\nVERSION_ID="24.04"\n' >"$OS/ubuntu"; printf 'ID=alpine\nVERSION_ID=3.24.4\n' >"$OS/alpine"
isolation_check_os_accepts "$HERE/provision.sh" "$OS/ubuntu"
if isolation_check_os_accepts "$HERE/provision.sh" "$OS/alpine"; then echo 'check_os accepted Alpine' >&2; exit 1; fi
printf '#!/bin/bash\necho nothing\n' >"$OS/nocheck.sh"; refused 'no check_os function' isolation_check_os_accepts "$OS/nocheck.sh" "$OS/ubuntu"
# upgrade.sh still does not forward --allow-os: the stand-in is the only reason its provisioning can pass here, and no script was changed to hide that
if grep -n -- '--allow-os' "$HERE/upgrade.sh"; then echo 'upgrade.sh forwards --allow-os now: the stand-in and its evidence are no longer needed' >&2; exit 1; fi

# --- OS-2: the mount guard (a whole filesystem at an owner path, a symlinked parent, an unreadable mount table) -----------------------------------------
# A writable owner mount is refused whatever its mount root; a read-only one is not.
printf '%s\n' '36 35 0:50 / / rw - overlay overlay rw' '39 36 8:1 / /etc rw,relatime - ext4 /dev/sda1 rw' >"$OS/mi.rootetc"
[[ "$(isolation_mount_problems "$OS/mi.rootetc")" == 'writable mount of /etc (a whole filesystem, mount root /)' ]]
printf '%s\n' '36 35 0:50 / / rw - overlay overlay rw' '39 36 8:1 / /etc ro,relatime - ext4 /dev/sda1 rw' >"$OS/mi.roetc"
[[ -z "$(isolation_mount_problems "$OS/mi.roetc")" ]]
printf '%s\n' '36 35 0:50 / / rw - overlay overlay rw' '39 36 0:60 / /var/spool/cron rw - tmpfs tmpfs rw' >"$OS/mi.tmpfs"
[[ "$(isolation_mount_problems "$OS/mi.tmpfs")" == 'writable mount of /var/spool/cron (a whole filesystem, mount root /)' ]]
for who in /etc /etc/cron.d /etc/logrotate.d /opt /opt/fold /root /home /srv /var/spool/cron; do
  printf '%s\n' '36 35 0:50 / / rw - overlay overlay rw' "39 36 8:1 / $who rw - ext4 /dev/sda1 rw" >"$OS/mi.each"
  isolation_mount_problems "$OS/mi.each" | grep "writable mount of $who " >/dev/null || { echo "a writable whole-filesystem mount of $who is not refused" >&2; exit 1; }
done
# the guard and the stand-in both refuse a root-/ /etc mount, writing nothing
rm -f "$OS/state/s.json"* "$OS/etc/os-release"; ln -s ../usr/lib/os-release "$OS/etc/os-release"; before="$(snap_tree)"
MI="$OS/mi.rootetc" refused 'writable mount of /etc (a whole filesystem, mount root /)' standin
[[ "$(snap_tree)" == "$before" ]]; [[ ! -e "$OS/state/s.json" ]]
MI="$OS/mi.rootetc" refused 'writable mount of /etc' guard
# an unreadable, missing or empty mount table fails closed everywhere it is read
: >"$OS/mi.empty"; before="$(snap_tree)"
for t in "$OS/mi.absent" "$OS/mi.empty"; do
  isolation_mount_problems "$t" | grep 'cannot be read, so no mount can be ruled out' >/dev/null
  isolation_mount_problems "$t" sockets | grep 'cannot be read' >/dev/null
  MI="$t" refused 'cannot be read' standin
  MI="$t" refused 'cannot be read' guard
done
[[ "$(snap_tree)" == "$before" ]]; [[ ! -e "$OS/state/s.json" ]]
# the destination directory is the CANONICAL parent: a symlink into a mount is judged as that mount, whatever the mount root or whether it names an owner path
mkdir -p "$OS/mnt/hostfs/sub" "$OS/mnt/host" "$OS/mnt/rootfs-dir"
ln -s "$OS/mnt/hostfs" "$OS/linkparent"; ln -s "$OS/mnt/hostfs/sub" "$OS/linkdeep"; ln -s "$OS/mnt/rootfs-dir" "$OS/linkok"
printf 'ID=alpine\nVERSION_ID=3.24.4\n' >"$OS/mnt/hostfs/os-release"; printf 'ID=alpine\nVERSION_ID=3.24.4\n' >"$OS/mnt/hostfs/sub/os-release"
for root in / /srv/host-etc; do
  printf '%s\n' '36 35 0:50 / / rw - overlay overlay rw' "41 36 8:1 $root $OS/mnt/hostfs rw - ext4 /dev/sda1 rw" >"$OS/mi.parent"
  before="$(snap_tree)"
  for via in linkparent linkdeep; do
    rc=0; FOLD_DRILL_ISOLATED_DAEMON=1 FOLD_DRILL_ASSUME_CONTAINER=1 isolation_os_standin "$OS/state/p.json" "$OS/$via/os-release" "$OS/mi.parent" >"$WORK/out" 2>&1 || rc=$?
    [[ "$rc" != 0 ]] && grep -q 'is on the mount' "$WORK/out" && grep -q "$OS/mnt/hostfs" "$WORK/out" || { echo "a symlinked parent ($via, mount root $root) into a writable mount was not refused" >&2; cat "$WORK/out" >&2; exit 1; }
    [[ ! -e "$OS/state/p.json" ]]
  done
  [[ "$(snap_tree)" == "$before" ]]
done
# a read-only covering mount is refused too (the stand-in could not be written, and "not the root filesystem" is the rule)
printf '%s\n' '36 35 0:50 / / rw - overlay overlay rw' "41 36 8:1 / $OS/mnt/hostfs ro - ext4 /dev/sda1 ro" >"$OS/mi.parentro"
rc=0; FOLD_DRILL_ISOLATED_DAEMON=1 FOLD_DRILL_ASSUME_CONTAINER=1 isolation_os_standin "$OS/state/p.json" "$OS/linkparent/os-release" "$OS/mi.parentro" >"$WORK/out" 2>&1 || rc=$?; [[ "$rc" != 0 ]] && grep -q 'is on the mount' "$WORK/out"
# a mount whose name merely starts like the directory does not cover it
printf '%s\n' '36 35 0:50 / / rw - overlay overlay rw' "41 36 8:1 / $OS/mnt/host rw - ext4 /dev/sda1 rw" >"$OS/mi.prefix"
isolation_covering_mount "$OS/mi.prefix" "$OS/mnt/hostfs/sub" | grep -x '/ rw' >/dev/null
isolation_covering_mount "$OS/mi.prefix" "$OS/mnt/host/x" | grep -x "$OS/mnt/host rw" >/dev/null
[[ "$(printf '%s\n' '1 0 0:1 / / rw - overlay o rw' '2 1 0:2 / /a rw - x x rw' '3 1 0:3 / /a ro - y y ro' >"$OS/mi.shadow"; isolation_covering_mount "$OS/mi.shadow" /a/b)" == '/a ro' ]]   # the later mount shadows
if isolation_covering_mount "$OS/mi.absent" /a; then echo 'a missing mount table covered a path' >&2; exit 1; fi
printf '%s\n' '2 1 0:2 / /a rw - x x rw' >"$OS/mi.nocover"; if isolation_covering_mount "$OS/mi.nocover" /b/c; then echo 'a path no mount covers was covered' >&2; exit 1; fi
MI="$OS/mi.nocover" refused 'covers' standin
# positive controls: a symlinked parent that stays on the root filesystem is allowed, and the replaced file is the one the link leads to
rm -f "$OS/state/s.json"* "$OS/mnt/rootfs-dir/os-release"; printf 'ID=alpine\nVERSION_ID=3.24.4\n' >"$OS/mnt/rootfs-dir/os-release"
before="$(snap_tree)"
FOLD_DRILL_ISOLATED_DAEMON=1 FOLD_DRILL_ASSUME_CONTAINER=1 isolation_os_standin "$OS/state/s.json" "$OS/linkok/os-release" "$mi_clean"
[[ "$(isolation_os_identity "$OS/mnt/rootfs-dir/os-release")" == 'ubuntu 24.04' ]]; isolation_os_restore "$OS/state/s.json"; [[ "$(snap_tree)" == "$before" ]]

# --- OS-1: no step relies on errexit (the writer runs as `... || return 1`, the restorer as `... || rc=1`) --------------------------------------------------
# The context really does turn errexit off inside a function: if it did not, this fixture would prove nothing.
probe() { false; echo reached; }; [[ "$(probe || true)" == reached ]]
STUBS="$WORK/stubs"; mkdir -p "$STUBS"
for c in cp mv jq mktemp chmod cat sha256sum stat readlink cmp ln rm awk sed dirname; do
  cat >"$STUBS/$c" <<'STUB'
#!/usr/bin/env bash
me="${0##*/}"; var="FAIL_$me"; pat="${!var:-}"
# shellcheck disable=SC2053 # the pattern is a glob on purpose
if [[ -n "$pat" && "$*" == *$pat* ]]; then exit 1; fi
PATH="${PATH#"$STUBS_DIR":}"
if [[ "$me" == cp && -n "${CORRUPT_CP:-}" ]]; then cp "$@"; rc=$?; for last; do :; done; printf x >>"$last"; exit "$rc"; fi
# shellcheck disable=SC2053 # a glob on purpose
if [[ "$me" == mv && -n "${CORRUPT_MV:-}" && "$*" == *$CORRUPT_MV* ]]; then mv "$@"; rc=$?; for last; do :; done; printf 'ID=alpine\nVERSION_ID=3.1\n' >"$last"; exit "$rc"; fi
exec "$me" "$@"
STUB
  chmod +x "$STUBS/$c"
done
export STUBS_DIR="$STUBS"
mkdir -p "$OS/mnt/plain"
fresh_file() { rm -f "$OS/state/"* "$OS/etc/os-release"; printf 'ID=alpine\nVERSION_ID=3.19.1\nHOME_URL="https://x"\n' >"$OS/etc/os-release"; chmod 640 "$OS/etc/os-release"; }
fresh_link() { rm -f "$OS/state/"* "$OS/etc/os-release"; ln -s ../usr/lib/os-release "$OS/etc/os-release"; }
# a failure before the record is published changes nothing: the file, no record, no backup, no temporary file
clean_fail() { # desc env-assignments...
  local desc="$1" rc=0; shift
  before="$(snap_tree)"
  # the call is the `||` form the runner uses; the stubs are named by the environment of this one call
  ( PATH="$STUBS:$PATH"; for kv in "$@"; do declare -x "$kv"; done; standin ) >"$WORK/out" 2>&1 || rc=$?
  [[ "$rc" != 0 ]] || { echo "OS-1: $desc: the stand-in succeeded although a step failed" >&2; cat "$WORK/out" >&2; exit 1; }
  [[ "$(snap_tree)" == "$before" ]] || { echo "OS-1: $desc: the filesystem changed" >&2; exit 1; }
  [[ -z "$(ls -A "$OS/state")" ]] || { echo "OS-1: $desc: files were left behind: $(ls -A "$OS/state")" >&2; exit 1; }
}
for kind in file link; do
  "fresh_$kind"
  if [[ "$kind" == file ]]; then
    clean_fail "cp fails ($kind)" FAIL_cp='*'
    clean_fail "the copy is corrupt ($kind)" CORRUPT_CP=1
    clean_fail "cmp fails ($kind)" FAIL_cmp='*'
    clean_fail "sha256sum fails ($kind)" FAIL_sha256sum='*'
    clean_fail "stat fails ($kind)" FAIL_stat='*'
    clean_fail "the backup cannot be kept ($kind)" FAIL_mv='.orig'
  else
    clean_fail "readlink fails ($kind)" FAIL_readlink='os-release'
  fi
  clean_fail "jq fails ($kind)" FAIL_jq='*'
  clean_fail "the record cannot be published ($kind)" FAIL_mv='s.json.new'
  clean_fail "awk fails ($kind)" FAIL_awk='*'
  clean_fail "sed fails ($kind)" FAIL_sed='*'
  clean_fail "dirname fails ($kind)" FAIL_dirname='*'
  clean_fail "readlink -f fails ($kind)" FAIL_readlink='-f'
done
# a failure AFTER the record is published puts the original back at once and records it
rolled_back() { # desc env-assignments...
  local desc="$1" rc=0; shift
  before="$(snap_tree)"
  ( PATH="$STUBS:$PATH"; for kv in "$@"; do declare -x "$kv"; done; standin ) >"$WORK/out" 2>&1 || rc=$?
  [[ "$rc" != 0 ]] || { echo "OS-1: $desc: the stand-in succeeded although a step failed" >&2; cat "$WORK/out" >&2; exit 1; }
  [[ "$(snap_tree)" == "$before" ]] || { echo "OS-1: $desc: the original was not put back" >&2; cat "$WORK/out" >&2; exit 1; }
  isolation_os_state_valid "$OS/state/s.json"
  if [[ "$(jq -r .restored "$OS/state/s.json")" != true ]]; then
    # the restore itself was failing in this case: a later restore (no failures) completes and records it
    isolation_os_restore "$OS/state/s.json"; [[ "$(snap_tree)" == "$before" ]]; jq -e '.restored == true' "$OS/state/s.json" >/dev/null
  fi
}
for kind in file link; do
  "fresh_$kind"; rolled_back "mktemp fails ($kind)" FAIL_mktemp='*'
  "fresh_$kind"; rolled_back "cat fails ($kind)" FAIL_cat='*'
  "fresh_$kind"; rolled_back "chmod fails ($kind)" FAIL_chmod='*'
  "fresh_$kind"; rolled_back "the rename over os-release fails ($kind)" FAIL_mv='.standin.'
  "fresh_$kind"; rolled_back "the rename and the immediate restore both fail ($kind)" FAIL_mv='os-release'
  "fresh_$kind"; rolled_back "the rename lands but the file read back is not the stand-in ($kind)" CORRUPT_MV='.standin.'
done
# the restorer, in its own `||` context: each failing step leaves the stand-in or the original in place, never a half state, and a retry finishes
restore_case() { # desc kind env-assignments...
  local desc="$1" k="$2" rc=0; shift 2
  "fresh_$k"; before="$(snap_tree)"
  standin
  ( PATH="$STUBS:$PATH"; for kv in "$@"; do declare -x "$kv"; done; isolation_os_restore "$OS/state/s.json" ) >"$WORK/out" 2>&1 || rc=$?
  [[ "$rc" != 0 ]] || { echo "OS-1: $desc: the restore succeeded although a step failed" >&2; exit 1; }
  jq -e '.restored == false' "$OS/state/s.json" >/dev/null || [[ "$(snap_tree)" == "$before" ]] || { echo "OS-1: $desc: restored recorded without the original back" >&2; exit 1; }
  isolation_os_state_valid "$OS/state/s.json"
  isolation_os_restore "$OS/state/s.json"; [[ "$(snap_tree)" == "$before" ]]; jq -e '.restored == true' "$OS/state/s.json" >/dev/null
  isolation_os_restore "$OS/state/s.json"   # idempotent
}
for kind in file link; do
  restore_case "jq fails ($kind)" "$kind" FAIL_jq='*'
  restore_case "the original cannot be moved back ($kind)" "$kind" FAIL_mv='os-release'
  restore_case "the restoration cannot be recorded ($kind)" "$kind" FAIL_mv='s.json.rec'
  restore_case "the record cannot be built ($kind)" "$kind" FAIL_jq='.restored = true'
done
restore_case "cp fails (file)" file FAIL_cp='*'
restore_case "the restored copy differs (file)" file CORRUPT_CP=1
restore_case "sha256sum fails (file)" file FAIL_sha256sum='*'
restore_case "ln fails (link)" link FAIL_ln='*'
# an absent original: the failure to remove it is reported
rm -f "$OS/state/"* "$OS/etc/os-release"; before="$(snap_tree)"; standin
rc=0; ( PATH="$STUBS:$PATH"; export FAIL_rm='os-release'; isolation_os_restore "$OS/state/s.json" ) >"$WORK/out" 2>&1 || rc=$?; [[ "$rc" != 0 ]]
isolation_os_restore "$OS/state/s.json"; [[ "$(snap_tree)" == "$before" ]]
# a leftover record is never trusted: empty, malformed, incomplete, or valid-looking but not ours to overwrite
fresh_file; before="$(snap_tree)"
: >"$OS/state/s.json"; refused 'already exists' standin; refused 'is empty' isolation_os_restore "$OS/state/s.json"
printf '{' >"$OS/state/s.json"; refused 'already exists' standin; refused 'malformed or incomplete' isolation_os_restore "$OS/state/s.json"
printf '{"applied":true,"path":"%s","restored":false}\n' "$OS/etc/os-release" >"$OS/state/s.json"; refused 'malformed or incomplete' isolation_os_restore "$OS/state/s.json"
printf '{"applied":true,"path":"%s","restored":false,"original":{"kind":"file","backup":"x","sha256":"zz","mode":"640"}}\n' "$OS/etc/os-release" >"$OS/state/s.json"; refused 'malformed or incomplete' isolation_os_restore "$OS/state/s.json"
printf '{"applied":true,"path":"%s","restored":false,"original":{"kind":"link","linkTarget":""}}\n' "$OS/etc/os-release" >"$OS/state/s.json"; refused 'malformed or incomplete' isolation_os_restore "$OS/state/s.json"
printf '{"applied":false,"path":"%s","restored":true}\n' "$OS/etc/os-release" >"$OS/state/s.json"; refused 'malformed or incomplete' isolation_os_restore "$OS/state/s.json"
[[ "$(snap_tree)" == "$before" ]]
rm -f "$OS/state/"*; [[ -z "$(isolation_os_restore "$OS/state/s.json" 2>&1)" ]]   # no record: nothing was applied
# a tampered backup is not restored over the stand-in
fresh_file; standin; printf 'tampered\n' >"$OS/state/s.json.orig"
refused 'no longer matches the recorded SHA-256' isolation_os_restore "$OS/state/s.json"
[[ "$(isolation_os_identity "$OS/etc/os-release")" == 'ubuntu 24.04' ]]; jq -e '.restored == false' "$OS/state/s.json" >/dev/null
fresh_file
# the record carries what a restore needs, before anything is replaced
fresh_file; standin; jq -e '.original.sha256 | test("^[0-9a-f]{64}$")' "$OS/state/s.json" >/dev/null; jq -e '.original.mode == "640"' "$OS/state/s.json" >/dev/null
isolation_os_restore "$OS/state/s.json"; [[ "$(stat -c %a "$OS/etc/os-release")" == 640 ]]
# nothing outside the temp tree was read for writing: the stubs and the state live under $WORK
[[ -z "$(find "$OS" -name '*.standin.*' -o -name '*.restore.*' -o -name '*.new.*' -o -name '*.rec.*' | head -n 1)" ]]

# The real mount table is a /proc file: its size is 0 and it is still readable (a `-s` test would refuse every real run).
isolation_table_readable <(printf '36 35 0:50 / / rw - overlay overlay rw\n')
if [[ -r /proc/self/mountinfo ]]; then
  isolation_table_readable /proc/self/mountinfo
  if isolation_mount_problems /proc/self/mountinfo | grep 'cannot be read' >/dev/null; then echo '/proc/self/mountinfo was treated as unreadable' >&2; exit 1; fi
  isolation_covering_mount /proc/self/mountinfo "$WORK" >/dev/null
fi
fresh_file; standin
cat "$OS/state/s.json" >>"$OS/state/duplicate.json"
cat "$OS/state/s.json" >>"$OS/state/duplicate.json"
refused 'malformed or incomplete' isolation_os_restore "$OS/state/duplicate.json"
[[ "$(isolation_os_identity "$OS/etc/os-release")" == 'ubuntu 24.04' ]]
isolation_os_restore "$OS/state/s.json"
for escaped in 011 012 134; do
  printf '%s\n' '36 35 0:50 / / rw - overlay overlay rw' "39 36 8:1 / /mnt/owner\\${escaped}dir rw - ext4 /dev/sda1 rw" >"$OS/mi.escaped"
  if isolation_covering_mount "$OS/mi.escaped" /mnt/owner/child >/dev/null; then echo "escaped mount $escaped was accepted" >&2; exit 1; fi
done
if isolation_table_readable "$WORK/absent-table"; then echo 'a missing table is readable' >&2; exit 1; fi
: >"$WORK/empty-table"; if isolation_table_readable "$WORK/empty-table"; then echo 'an empty table is readable' >&2; exit 1; fi
echo 'isolation fixtures: ok'
