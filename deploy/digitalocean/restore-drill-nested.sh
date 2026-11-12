#!/usr/bin/env bash
# Runs restore-drill.sh inside a throwaway container with its OWN Docker daemon
# (docker:dind), so the drill's provisioners, Compose stacks, ports 80 and 443
# and /etc entries exist only in that container's filesystem and network. It
# starts exactly one container on the daemon it is run against and removes it
# (by id) at the end.
#
#   deploy/digitalocean/restore-drill-nested.sh --out NEW_DIR [--dind-image REF] [-- drill args...]
#   deploy/digitalocean/restore-drill-nested.sh --out NEW_DIR --qc1 [--dind-image REF] [-- qc1-measure.sh args...]
#
# With --qc1 the runner measures QC-1 instead of running the restore drill.
#
#   deploy/digitalocean/restore-drill-nested.sh --out NEW_DIR --crossings-only [--dind-image REF] [-- drill args...]
#
# With --crossings-only the runner runs a PARTIAL DIAGNOSTIC of the restore drill: the bootstrap, the fixture prerequisites and the five real level-2
# crossing cases (restore-drill.sh --crossings-only), to find deterministic runner failures sooner than a full run can. It is NOT the full drill and NOT
# acceptance: the Firefox runtime is not built (the crossings do not use it; the Chromium preflight still runs), the evidence directory gets
# PARTIAL-NOT-FULL-ACCEPTANCE.txt (also written when it fails), and a pass exits 10, never 0. Without the flag nothing about the run changes.
#
# Both modes drive a real Chromium: the restore drill's recovery proof
# (testdata/restore-browser.cjs, through web/node_modules/playwright) and the QC-1
# panel measurement (through @playwright/test). The nested Alpine runner has
# neither, so in BOTH modes, before anything is built, drilled or measured,
# qc1/browser-prepare.sh installs Alpine's chromium, runs `npm ci` in web/ exactly
# as web/package-lock.json locks it (no install scripts, no optional packages, no
# Playwright browser download), and proves that Playwright resolves from
# web/node_modules and that the installed Chromium launches, through each
# consumer's own entry point with each consumer's own launch options
# (qc1/browser-launch.cjs). The Chromium it found (QC1_CHROMIUM, with the sandbox
# switches only this disposable container may use) is exported to the run, so the
# run launches the executable the preflight launched. restore-drill.sh and
# qc1-measure.sh repeat the preflight themselves. A failure ends the run before
# the stack is built. The drill has no switch to skip this; for --qc1 only,
# --no-browser (after --) skips it on purpose, which leaves the browser rows
# missing and the evidence incomplete. The preflight's result is kept in
# <out>/prep/browser-runtime.json and, for the drill, <out>/browser-preflight.json.
#
# What it refuses or never does:
#   - a clean, committed HEAD is required (the drill builds from git archive);
#   - the nested container gets no bind mounts, no host network, no Docker
#     socket and no host paths: the repository goes in as a git bundle through
#     `docker cp`, the evidence comes out the same way, into the new --out
#     directory; after it starts, its mounts are read back and it is torn down
#     if they hold anything but its own /var/lib/docker volume;
#   - it needs --privileged, which Docker allows only to a daemon you control; a
#     rootless daemon that refuses privileged containers cannot host this drill
#     (the drill then cannot run at all, which is the intended failure);
#   - the drill inside refuses to start unless it finds itself in such a
#     container (isolation.sh).
#
# Interruption (INT, TERM or HUP). The measurements, the progress files and the
# failure evidence of a long run exist only inside the container, so the
# container is never removed first: the runner asks the workload inside to stop
# (TERM, which its own traps turn into a clean finish), waits for it up to
# FOLD_NESTED_STOP_GRACE seconds (default 90), kills what is left, copies
# /work/prep and /work/out into --out, and only then removes the container. The
# exit status is 130, 143 or 129. --out then holds INTERRUPTED.txt, and
# COLLECTION-FAILED.txt when a copy failed; a copy that fails is never silent. A
# KILL sent to the runner itself cannot do any of this.
set -Eeuo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
OUT="" DIND=docker:29-dind QC1=0 CROSS=0
drill_args=()
while (( $# )); do
  case "$1" in
    --out) OUT="${2:?new evidence directory required}"; shift 2 ;;
    --dind-image) DIND="${2:?image required}"; shift 2 ;;
    --qc1) QC1=1; shift ;;
    --crossings-only) CROSS=1; shift ;;
    --help) sed -n '2,/^set -Eeuo/p' "${BASH_SOURCE[0]}" | sed '$d' | sed 's/^# \{0,1\}//'; exit 0 ;;
    --) shift; drill_args=("$@"); break ;;
    *) echo "unknown option $1 (drill arguments go after --)" >&2; exit 1 ;;
  esac
done
if (( QC1 && CROSS )); then echo 'restore-drill-nested: --qc1 and --crossings-only are different runs; choose one' >&2; exit 1; fi
for tool in docker git tar; do command -v "$tool" >/dev/null || { echo "restore-drill-nested: $tool is required" >&2; exit 1; }; done
[[ -n "$OUT" ]] || { echo 'restore-drill-nested: --out NEW_DIR is required' >&2; exit 1; }
OUT="$(realpath -m "$OUT")"
[[ ! -e "$OUT" ]] || { echo "restore-drill-nested: $OUT already exists" >&2; exit 1; }
case "${DOCKER_HOST:-}" in ''|unix://*) ;; *) echo "restore-drill-nested: DOCKER_HOST is remote (${DOCKER_HOST}); the nested daemon must be started on a daemon you control locally" >&2; exit 1 ;; esac
[[ -z "$(git --no-optional-locks -C "$ROOT" status --porcelain)" ]] || { echo 'restore-drill-nested: the tree is dirty; commit first' >&2; exit 1; }
HEAD_SHA="$(git -C "$ROOT" rev-parse HEAD)"

RUN_ID="$(od -An -N6 -tx1 /dev/urandom | tr -d ' \n')"
NAME="fold-drill-$RUN_ID"
CID=""
TMP="$(mktemp -d)"
# What is in the container is the evidence. ARMED means the container holds a clone and the workload may have written to /work/prep or /work/out; COLLECTED means
# those were copied out already. The processes the workload is made of are found by their command lines (each starts with `bash deploy/digitalocean/...`; the
# processes of this runner's own docker exec calls do not), because the preparation step's docker exec line is fixed and carries no pid.
ARMED=0 COLLECTED=0 INTERRUPTED="" STOPPED_HOW="" INNER_CLIENT=""
COLLECT_FAILED=()
INNER_RE='^bash deploy/digitalocean/(restore-drill|qc1-measure|qc1/browser-prepare)\.sh'
# inner_exec DOCKER_EXEC_ARGS...: the workload, started in the background and waited for, so that a signal runs the trap at once and not after a many-hour run.
# A signal sent to the whole process group can end the docker client a moment before this script's own trap runs; a client that ended like a killed process
# while the workload is still there is not the workload's end, so it is waited for here, and the trap, which is already on its way, stops it.
inner_exec() {
  local rc=0
  docker exec "$@" &
  INNER_CLIENT=$!
  wait "$INNER_CLIENT" || rc=$?
  INNER_CLIENT=""
  if (( rc >= 128 )); then
    while docker exec "$CID" pgrep -f "$INNER_RE" >/dev/null 2>&1; do sleep 1; done
  fi
  return "$rc"
}
# stop_inner asks the workload to stop and waits for it; what survives the grace is killed. The container's own docker exec calls go through a daemon, so a TERM
# that only reached the docker client on this host would have stopped nothing inside.
# shellcheck disable=SC2329 # reached through the EXIT trap (cleanup).
stop_inner() {
  local grace="${FOLD_NESTED_STOP_GRACE:-90}" i
  [[ "$grace" =~ ^[0-9]+$ ]] || grace=90
  STOPPED_HOW=term
  docker exec "$CID" pkill -TERM -o -f "$INNER_RE" >/dev/null 2>&1 || true
  for ((i=0;i<grace;i++)); do
    docker exec "$CID" pgrep -f "$INNER_RE" >/dev/null 2>&1 || return 0
    sleep 1
  done
  STOPPED_HOW="kill"
  echo "restore-drill-nested: the run inside did not stop within ${grace}s of TERM; killing it" >&2
  docker exec "$CID" pkill -KILL -f "$INNER_RE" >/dev/null 2>&1 || true
  sleep 1
}
# collect_artifacts copies the preparation and run directories out. A copy that fails is said on stderr and recorded in COLLECTION-FAILED.txt.
collect_artifacts() {
  COLLECT_FAILED=()
  # With --no-browser there is no preparation directory, and its absence is not a failure.
  if ! docker cp "$CID:/work/prep/." "$OUT/prep/" >/dev/null 2>&1 && (( ! skip_browser )); then
    COLLECT_FAILED+=("/work/prep (the browser runtime preparation) could not be copied out")
  fi
  if ! docker cp "$CID:/work/out/." "$OUT/" >/dev/null 2>&1; then
    echo 'restore-drill-nested: no evidence directory to copy out' >&2
    COLLECT_FAILED+=("/work/out (the run's evidence) could not be copied out")
  fi
  COLLECTED=1
  if (( ${#COLLECT_FAILED[@]} )); then
    printf '%s\n' "${COLLECT_FAILED[@]}" >"$OUT/COLLECTION-FAILED.txt" || true
  fi
}
# shellcheck disable=SC2329 # EXIT trap.
cleanup() {
  local rc=$?
  trap - EXIT
  trap '' INT TERM HUP   # a second signal must not cut the collection short
  if [[ -n "$CID" ]] && (( ARMED && ! COLLECTED )); then
    [[ -z "$INTERRUPTED" ]] || stop_inner
    collect_artifacts
    partial_marker || true
    if [[ -n "$INTERRUPTED" ]]; then
      {
        printf 'restore-drill-nested: interrupted by %s.\n' "$INTERRUPTED"
        if [[ "$STOPPED_HOW" == kill ]]; then echo 'The run inside was asked to stop (TERM) and did not; it was killed, so its own cleanup and final records may be missing.'
        else echo 'The run inside was asked to stop (TERM) and finished its own cleanup.'; fi
        echo 'What the container held was copied here before it was removed; see COLLECTION-FAILED.txt, if present, for anything that was not.'
        echo 'This directory is NOT acceptance evidence.'
      } >"$OUT/INTERRUPTED.txt" || true
      echo "restore-drill-nested: interrupted by $INTERRUPTED; artifacts copied to $OUT$([[ ${#COLLECT_FAILED[@]} -eq 0 ]] || echo ' (some could not be: COLLECTION-FAILED.txt)')" >&2
    fi
  fi
  [[ -z "$CID" ]] || docker rm -fv "$CID" >/dev/null 2>&1 || true
  [[ -z "$INNER_CLIENT" ]] || kill "$INNER_CLIENT" >/dev/null 2>&1 || true
  rm -rf -- "$TMP"
  exit "$rc"
}
# shellcheck disable=SC2329 # signal traps.
on_signal() { trap '' INT TERM HUP; INTERRUPTED="$1"; exit "$2"; }
trap cleanup EXIT
trap 'on_signal INT 130' INT
trap 'on_signal TERM 143' TERM
trap 'on_signal HUP 129' HUP

git -C "$ROOT" bundle create "$TMP/fold.bundle" --all >/dev/null 2>&1
mkdir "$OUT"
# No -v, no --network host, no docker.sock: the one thing this container may do
# is run its own daemon. The image's own anonymous /var/lib/docker volume is the
# only mount it will have.
CID="$(docker run -d --name "$NAME" --hostname "$NAME" --privileged -e DOCKER_TLS_CERTDIR= "$DIND")"
mounts="$(docker inspect --format '{{range .Mounts}}{{.Destination}} {{.Source}}{{"\n"}}{{end}}' "$CID")"
bad="$(awk '$1 != "/var/lib/docker" && NF' <<<"$mounts")"
if [[ -n "$bad" || "$mounts" == *docker.sock* ]]; then
  echo "restore-drill-nested: the container has mounts beyond its own /var/lib/docker; refusing:" >&2
  printf '%s\n' "$mounts" >&2
  exit 1
fi
if [[ "$(docker inspect --format '{{.HostConfig.NetworkMode}}' "$CID")" == host ]]; then echo 'restore-drill-nested: the container uses the host network' >&2; exit 1; fi
for ((i=0;i<120;i++)); do docker exec "$CID" docker info >/dev/null 2>&1 && break; sleep 1; done
docker exec "$CID" docker info >/dev/null
docker exec "$CID" apk add --no-cache bash git jq curl go nodejs npm util-linux coreutils findutils tar make openssl >/dev/null
docker cp "$TMP/fold.bundle" "$CID:/work.bundle"
docker exec "$CID" sh -c "git clone -q /work.bundle /work/fold && cd /work/fold && git checkout -q --detach $HEAD_SHA && test -z \"\$(git status --porcelain)\""
# The label a partial run leaves in its evidence directory, on every path that ends it (also when it fails).
partial_marker() {
  (( CROSS )) || return 0
  printf '%s\n' 'PARTIAL DIAGNOSTIC (restore-drill-nested.sh --crossings-only). This directory is NOT full-drill evidence and NOT acceptance; see drill-mode.json.' \
    'A pass is exit 10. Acceptance needs a full run (no --crossings-only) exiting 0.' >"$OUT/PARTIAL-NOT-FULL-ACCEPTANCE.txt"
}
rc=0
if (( QC1 )); then what=qc1; elif (( CROSS )); then what=crossings-only; else what=drill; fi
skip_browser=0
if (( QC1 )); then
  for a in "${drill_args[@]}"; do [[ "$a" != --no-browser ]] || skip_browser=1; done
fi
# Both modes need a browser: the restore drill's recovery proof (restore-browser.cjs) and the QC-1 panel measurement each drive Chromium through
# web/node_modules. It is installed and proved BEFORE anything is built, drilled or measured, in this runner and nowhere else. Only a QC-1 run given
# --no-browser skips it, on purpose; the drill has no such switch.
ARMED=1
if (( ! skip_browser )); then
  # The drill also needs Firefox (glibc, cannot run on this Alpine runner): the preparation builds a Debian container for it and proves it launches.
  prep_env=(-e QC1_WEB_DIR=/work/fold/web)
  if (( ! QC1 && ! CROSS )); then prep_env+=(-e QC1_PREPARE_FIREFOX=1); fi
  inner_exec "${prep_env[@]}" -w /work/fold "$CID" bash deploy/digitalocean/qc1/browser-prepare.sh /work/prep || rc=$?
  if (( rc != 0 )); then
    if ! docker cp "$CID:/work/prep/." "$OUT/prep/" >/dev/null 2>&1; then
      echo 'restore-drill-nested: the preparation directory could not be copied out' >&2
      echo '/work/prep (the browser runtime preparation) could not be copied out' >"$OUT/COLLECTION-FAILED.txt"
    fi
    COLLECTED=1
    partial_marker
    echo "restore-drill-nested: the browser runtime could not be prepared (exit $rc); nothing was run ($what)" >&2
    exit "$rc"
  fi
fi
# The chromium the preparation found is exported to the run, with the sandbox switches only this disposable container may use; browser.env is
# read inside the container, so the run launches the very executable the preflight launched.
# shellcheck disable=SC2016 # the script runs inside the container
wrap='if [ -f /work/prep/browser.env ]; then set -a; . /work/prep/browser.env; set +a; fi; exec "$@"'
if (( QC1 )); then
  inner_exec -e FOLD_DRILL_ISOLATED_DAEMON=1 -e QC1_NO_SANDBOX=1 -e QC1_WEB_DIR=/work/fold/web -w /work/fold "$CID" \
    bash -c "$wrap" _ bash deploy/digitalocean/qc1-measure.sh --dir /work/out "${drill_args[@]}" || rc=$?
elif (( CROSS )); then
  inner_exec -e FOLD_DRILL_ISOLATED_DAEMON=1 -e QC1_NO_SANDBOX=1 -e QC1_WEB_DIR=/work/fold/web -w /work/fold "$CID" \
    bash -c "$wrap" _ bash deploy/digitalocean/restore-drill.sh --dir /work/out --crossings-only "${drill_args[@]}" || rc=$?
else
  inner_exec -e FOLD_DRILL_ISOLATED_DAEMON=1 -e QC1_NO_SANDBOX=1 -e QC1_WEB_DIR=/work/fold/web -w /work/fold "$CID" \
    bash -c "$wrap" _ bash deploy/digitalocean/restore-drill.sh --dir /work/out "${drill_args[@]}" || rc=$?
fi
collect_artifacts
if (( CROSS )); then
  partial_marker
  if (( rc == 0 )); then echo 'restore-drill-nested: a crossings-only run reported 0, which only a full run may; treating it as failed' >&2; rc=1; fi
  echo 'restore-drill-nested: crossings-only is a PARTIAL diagnostic: NOT the full drill, NOT acceptance (a pass exits 10; only a full run exiting 0 is acceptance)'
fi
echo "restore-drill-nested: $what exit=$rc evidence=$OUT"
exit "$rc"
