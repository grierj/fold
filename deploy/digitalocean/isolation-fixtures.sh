#!/usr/bin/env bash
# Daemon-free regressions for the drills' isolation (V-08): the pure checks in
# isolation.sh, the restore drill's refusal to start on a host that is not declared
# disposable, and the project names. No Docker daemon is started or contacted;
# nothing is written outside a temp dir.
# shellcheck disable=SC2016,SC2030,SC2031,SC2015 # patterns keep their dollar signs literal; the stubbed calls export into their own subshell on purpose
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
[[ -z "$(isolation_daemon_problems '{"Name":"fold-drill-1","Containers":0,"ContainersRunning":0}' fold-drill-1)" ]]
isolation_daemon_problems '{"Name":"prod-droplet","ContainersRunning":0}' fold-drill-1 | grep 'not a daemon of this filesystem' >/dev/null
isolation_daemon_problems '{"Name":"fold-drill-1","Containers":4,"ContainersRunning":4}' fold-drill-1 | grep 'already holds container(s): 4 in all, 4 running' >/dev/null
# A stopped container counts: the daemon is not empty.
isolation_daemon_problems '{"Name":"fold-drill-1","Containers":2,"ContainersRunning":0}' fold-drill-1 | grep 'already holds container(s): 2 in all, 0 running' >/dev/null
[[ "$(isolation_daemon_problems '{"Name":"x","Containers":2,"ContainersRunning":2}' y | wc -l)" == 2 ]]
# The scope check alone ignores what the daemon holds (qc1-measure.sh keeps its own --shared-daemon rule).
[[ -z "$(isolation_daemon_scope_problems '{"Name":"h","Containers":5,"ContainersRunning":5}' h)" ]]
[[ -n "$(DOCKER_HOST=ssh://root@elsewhere isolation_daemon_scope_problems '{"Name":"h"}' h)" ]]
[[ -n "$(DOCKER_CONTEXT=remote isolation_daemon_scope_problems '{"Name":"h"}' h)" ]]

# --- the whole guard ----------------------------------------------------------------------------------------
GOODINFO='{"Name":"fold-drill-1","Containers":0,"ContainersRunning":0}'
guard() { FOLD_DRILL_ISOLATED_DAEMON="${ISO-1}" DOCKER_HOST="${DH:-}" DOCKER_CONTEXT="${DC:-}" isolation_require_disposable "${MI:-$WORK/mi.clean}" "${HOST:-fold-drill-1}" "${INFO:-$GOODINFO}" "${PATHS:-$WORK/absent}"; }
guard
ISO='' refused 'not declared disposable' guard
DH=tcp://10.0.0.5:2375 refused 'DOCKER_HOST points elsewhere' guard
DC=remote refused 'DOCKER_CONTEXT=remote' guard
DH=unix:///var/run/docker.sock guard
MI="$WORK/mi.sock" refused 'the Docker socket' guard
MI="$WORK/mi.owner" refused 'writable bind mount of /etc' guard
HOST=prod-droplet refused 'not a daemon of this filesystem' guard
INFO='{"Name":"fold-drill-1","Containers":2,"ContainersRunning":2}' refused 'already holds container(s): 2 in all' guard
INFO='{"Name":"fold-drill-1","Containers":1,"ContainersRunning":0}' refused 'already holds container(s): 1 in all, 0 running' guard
PATHS="$WORK/exists" refused 'already exists' guard
# Everything wrong at once lists everything wrong.
ISO='' MI="$WORK/mi.both" HOST=x INFO='{"Name":"y","Containers":1,"ContainersRunning":1}' PATHS="$WORK/exists" refused 'already exists' guard
[[ "$(grep -c '^  - ' "$WORK/out")" -ge 5 ]]

# --- the restore drill refuses first -------------------------------------------------------------------------
RD="$HERE/restore-drill.sh"
iso_line="$(grep -n '^isolation_require_disposable || exit 1' "$RD" | cut -d: -f1)"
pin_line="$(grep -n '^isolation_pin_local || exit 1' "$RD" | cut -d: -f1)"
[[ -n "$pin_line" && -n "$iso_line" && "$pin_line" -lt "$iso_line" ]] || { echo 'isolation: the local-daemon pin does not come before the guard' >&2; exit 1; }
for later in '^\[\[ "\$(id -u)" == 0 \]\]' '^REVISION=' '^crossing_validate_revisions' '^drill_progress_init' '^mkdir ' '^"\$ROOT/deploy/digitalocean/deploy-drill.sh"'; do
  line="$(grep -n -- "$later" "$RD" | head -n 1 | cut -d: -f1)"
  [[ -n "$line" && "$iso_line" -lt "$line" ]] || { echo "isolation: the guard does not come before $later" >&2; exit 1; }
done
grep -q '^source "\$ROOT/deploy/digitalocean/isolation.sh"' "$RD"

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

# The real mount table is a /proc file: its size is 0 and it is still readable (a `-s` test would refuse every real run).
isolation_table_readable <(printf '36 35 0:50 / / rw - overlay overlay rw\n')
if [[ -r /proc/self/mountinfo ]]; then
  isolation_table_readable /proc/self/mountinfo
  if isolation_mount_problems /proc/self/mountinfo | grep 'cannot be read' >/dev/null; then echo '/proc/self/mountinfo was treated as unreadable' >&2; exit 1; fi
fi
if isolation_table_readable "$WORK/absent-table"; then echo 'a missing table is readable' >&2; exit 1; fi
: >"$WORK/empty-table"; if isolation_table_readable "$WORK/empty-table"; then echo 'an empty table is readable' >&2; exit 1; fi
echo 'isolation fixtures: ok'
