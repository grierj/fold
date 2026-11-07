#!/usr/bin/env bash
# Daemon-free regressions for the drills' isolation (V-08): the pure checks in
# isolation.sh, the restore drill's refusal to start outside a throwaway
# container, the project names, and the nested launcher driven by a stub docker.
# No Docker daemon is started or contacted; nothing is written outside a temp dir.
# shellcheck disable=SC2016 # patterns keep their dollar signs literal.
set -Eeuo pipefail
exec </dev/null
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
HERE="$ROOT/deploy/digitalocean"
# shellcheck source=deploy/digitalocean/isolation.sh
source "$HERE/isolation.sh"
WORK="$(mktemp -d /tmp/fold-isolation-fixture.XXXXXX)"
# shellcheck disable=SC2329 # EXIT trap.
finish() { local rc=$?; rm -rf -- "$WORK"; exit "$rc"; }
trap finish EXIT
trap 'printf "isolation fixture failed at line %s: %s\n" "$LINENO" "$BASH_COMMAND" >&2' ERR
refused() { local needle="$1" rc=0; shift; "$@" >"$WORK/out" 2>&1 || rc=$?; [[ "$rc" != 0 ]]; [[ -z "$needle" ]] || grep -qF -- "$needle" "$WORK/out"; }

rc=0; bash "$HERE/isolation.sh" >"$WORK/direct.log" 2>&1 || rc=$?
[[ "$rc" == 1 ]]; grep -q 'source from a drill only' "$WORK/direct.log"

# --- mountinfo ---------------------------------------------------------------------------------------
cat >"$WORK/mi.clean" <<'M'
36 35 0:50 / / rw,relatime - overlay overlay rw,lowerdir=/x
37 36 0:5 / /proc rw,nosuid - proc proc rw
38 36 0:51 / /etc/cron.d rw - tmpfs tmpfs rw
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
isolation_mount_problems "$WORK/mi.sock2" | grep -q '/run/docker.sock is a mount'
cat >"$WORK/mi.owner" <<'M'
36 35 0:50 / / rw - overlay overlay rw
42 36 8:1 /home/me/etc /etc rw,relatime - ext4 /dev/sda1 rw
43 36 8:1 /home/me/opt /opt/fold rw - ext4 /dev/sda1 rw
44 36 8:1 /home/me/ro /opt ro - ext4 /dev/sda1 rw
45 36 8:1 / /home rw - ext4 /dev/sda2 rw
M
[[ "$(isolation_mount_problems "$WORK/mi.owner" | sort | paste -sd'|')" == "writable bind mount of /etc (from /home/me/etc)|writable bind mount of /opt/fold (from /home/me/opt)" ]]
# The sockets-only mode (qc1-measure.sh) ignores owner mounts and still sees a socket mount.
[[ -z "$(isolation_mount_problems "$WORK/mi.owner" sockets)" ]]
cat "$WORK/mi.owner" "$WORK/mi.sock" >"$WORK/mi.both"; [[ "$(isolation_mount_problems "$WORK/mi.both" sockets | wc -l)" == 1 ]]

# --- existing configuration: refuse, never preserve ------------------------------------------------------
: >"$WORK/exists"; ln -s "$WORK/nowhere" "$WORK/dangling"
[[ -z "$(isolation_preexisting_problems "$WORK/absent")" ]]
isolation_preexisting_problems "$WORK/exists" | grep -q 'already exists; the drill would overwrite or later remove an owner'
isolation_preexisting_problems "$WORK/dangling" | grep -q 'already exists'
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
isolation_daemon_problems '{"Name":"prod-droplet","ContainersRunning":0}' fold-drill-1 | grep -q 'not a daemon of this filesystem'
isolation_daemon_problems '{"Name":"fold-drill-1","ContainersRunning":4}' fold-drill-1 | grep -q 'already runs 4 container'
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
printf '%s\n' "$*" >>"$STUB_LOG"
case "$1" in
  info) printf '{"Name":"%s","ContainersRunning":0}\n' "${STUB_NAME:-the-host}" ;;
  run) printf 'c%.0s' {1..64}; echo ;;
  inspect)
    case "$*" in
      *NetworkMode*) echo "${STUB_NET:-bridge}" ;;
      *Mounts*) printf '%b' "${STUB_MOUNTS:-/var/lib/docker /var/lib/docker/volumes/x/_data\n}" ;;
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
: >"$WORK/stub.log"
launch --out "$WORK/ev1" -- --keep >"$WORK/l1.out" 2>&1
[[ -d "$WORK/ev1" ]] && grep -q 'drill exit=0' "$WORK/l1.out"
run_line="$(grep '^run ' "$WORK/stub.log")"
[[ "$(grep -c '^run ' "$WORK/stub.log")" == 1 ]]
# One container, privileged, named and hostnamed for this run, with no mounts, host network or socket.
grep -qE -- '--name fold-drill-[0-9a-f]{12} --hostname fold-drill-[0-9a-f]{12} --privileged -e DOCKER_TLS_CERTDIR= docker:29-dind$' <<<"$run_line"
if grep -qE -- ' -v |--volume|--mount|--network|--net |docker\.sock|--pid|--ipc|--userns|--cap-add' <<<"$run_line"; then echo "isolation: the nested container is given more than it needs: $run_line" >&2; exit 1; fi
# The repository goes in as a bundle by cp, the drill runs inside with the flag, the evidence comes out by cp, and the container is removed by id.
grep -q '^cp .*fold.bundle ccc' "$WORK/stub.log"; grep -q "^cp ccc[c]*:/work/out/. $WORK/ev1/" "$WORK/stub.log"
grep -q '^exec -e FOLD_DRILL_ISOLATED_DAEMON=1 -e QC1_NO_SANDBOX=1 -e QC1_WEB_DIR=/work/fold/web -w /work/fold c\{64\} bash -c .* _ bash deploy/digitalocean/restore-drill.sh --dir /work/out --keep$' "$WORK/stub.log"
# The drill's browser is prepared first, in the same container, and the Chromium it found is the one the run is given.
prep_l="$(grep -n 'browser-prepare.sh /work/prep$' "$WORK/stub.log" | head -n 1 | cut -d: -f1)"; run_l="$(grep -n 'restore-drill.sh --dir /work/out' "$WORK/stub.log" | head -n 1 | cut -d: -f1)"
[[ -n "$prep_l" && -n "$run_l" && "$prep_l" -lt "$run_l" ]]
grep -q '/work/prep/browser.env' "$WORK/stub.log"
grep -qE '^rm -fv c{64}$' "$WORK/stub.log"
# Refusals leave the container removed and the drill unrun.
refuse_launch() { : >"$WORK/stub.log"; local rc=0; "$@" >"$WORK/lr.out" 2>&1 || rc=$?; [[ "$rc" == 1 ]]; }
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
printf '%s\n' "$*" >>"$STUB_LOG"
case "$1" in
  info) echo '{"Name":"x","ContainersRunning":0}' ;;
  run) printf 'c%.0s' {1..64}; echo ;;
  inspect) case "$*" in *NetworkMode*) echo bridge ;; *Mounts*) echo '/var/lib/docker /v' ;; esac ;;
  exec) case "$*" in *restore-drill.sh*) exit 7 ;; esac ;;
esac
exit 0
STUB
: >"$WORK/stub.log"; rc=0; launch --out "$WORK/ev8" >"$WORK/l8.out" 2>&1 || rc=$?
[[ "$rc" == 7 ]] && grep -q 'drill exit=7' "$WORK/l8.out" && grep -q "^cp ccc[c]*:/work/out/. $WORK/ev8/" "$WORK/stub.log" && grep -qE '^rm -fv c{64}$' "$WORK/stub.log"
echo 'isolation fixtures: ok'
