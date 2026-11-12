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
# Stop, then collect, then remove (on every end, not only a signal). The
# measurements, the progress files and the failure evidence of a long run exist
# only inside the container, so it is never removed first. Every process the
# workload starts carries FOLD_NESTED_RUN=<run id> in its environment (each
# docker exec passes it), so the runner finds all of them by that marker, not
# just the bash scripts: node, chromium, npm, orphans, processes in sessions of
# their own. After INT, TERM or HUP it TERMs the owned top-level processes (their
# traps turn that into a clean finish), waits up to FOLD_NESTED_STOP_GRACE
# seconds (default 90), KILLs every owned process and keeps looking for up to
# FOLD_NESTED_KILL_WAIT seconds (default 15). A stop counts as established only
# when lookups that ran inside the container counted zero twice, a second apart;
# a failed lookup is never read as "nothing running". If it cannot be
# established the container is frozen (docker pause) before the copy. Only then
# are /work/prep and /work/out copied into --out, and the container is removed.
# After a normal end the same check runs and ends any leftover (noted in
# LEFTOVERS-STOPPED.txt). The exit status after a signal is 130, 143 or 129. A workload that ends with 137 or 143 while no signal reached this runner (a
# killed docker client, an OOM kill) is given FOLD_NESTED_SIGNAL_WAIT seconds (default 5) for a pending signal, then goes through the same bounded stop and
# collection, and its own status is kept.
#
# Every Docker call the cleanup makes (ownership lookups, pause, unpause, the two copies, removal) is a client of its own with an absolute deadline: one that has
# not answered by then is KILLed with its whole process group (a client that ignores TERM included) and counted as a failure, never waited for. The cleanup as a
# whole has FOLD_NESTED_CLEANUP_BUDGET seconds (default 170, from the moment the stop begins, restarted by a signal); a signal-driven run under
# scripts/reason-review-scale.sh gets its budget from that wrapper's grace, which must outlast it. The last FOLD_NESTED_RM_RESERVE seconds (default 30) belong to
# removal alone: stop, lookups and copies end before them, so a stalled copy cannot use up the time the container's removal needs. A single call gets
# FOLD_NESTED_CALL_TIMEOUT seconds (default 15). If the container still cannot be shown to be gone, its name and id are on stderr and in COLLECTION-FAILED.txt
# with the command to remove it by hand, and the run fails.
#
# Records in --out: INTERRUPTED.txt (how the run stopped), COLLECTION-FAILED.txt
# (a copy that failed or timed out, a stop that could not be established, a
# container that could not be removed). A collection
# failure is a failure of the run: a run that would have exited 0 (or, for
# --crossings-only, 10) exits 1. A KILL sent to the runner itself cannot do any
# of this. What it cannot see: a tool that replaces its whole environment, and
# the processes of the drill's own nested containers (the daemon owns them; they
# go with the container).
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
# those were copied out already.
#
# Which processes are the workload's is decided by one marker, not by names or by a process tree: every docker exec this runner makes passes
# -e FOLD_NESTED_RUN=<this run's id>, so every process the workload starts (bash, node, chromium, npm, anything) carries it in its environment, also after it
# was orphaned, put in a session or process group of its own, or reparented to init. A process the daemon starts (the drill's containers) and the runner's own
# lookup calls do not carry it. A tool that replaces its whole environment would hide from this; none of the drill's tools does.
ARMED=0 COLLECTED=0 INTERRUPTED="" STOPPED_HOW="none" INNER_CLIENT=""
STOP_VERIFIED=0 STOP_LOOKUP_ERRORS=0 FROZEN=0 OWNED_N=""
COLLECT_FAILED=()
TO_LOOKUP=0 TO_PAUSE=0 TO_RM=0 LIMITED_JOB="" LIMITED_TIMED_OUT=0
CLEAN_END="" RM_AT="" PHASE_END=""
# secs NAME DEFAULT MIN: the whole number in the environment variable NAME if it is one of at least MIN, else DEFAULT.
secs() {
  local v="${!1:-}"
  if [[ "$v" =~ ^[0-9]+$ ]] && (( 10#$v >= $3 )); then echo $((10#$v)); else echo "$2"; fi
}
CALL_T="$(secs FOLD_NESTED_CALL_TIMEOUT 15 1)"
# The cleanup's clock. Every deadline below is an absolute $SECONDS value (whole seconds, so one may fall up to a second early), so a stalled call cannot stretch a
# loop that counts iterations. open_window starts it once per cleanup: CLEAN_END is the end of all of it; RM_AT is where the time reserved for removal begins, and
# nothing before removal (lookups, pause, copies) is given time past it. A signal restarts it (on_signal), because the wrapper's grace starts then.
open_window() {
  [[ -z "$CLEAN_END" ]] || return 0
  local budget reserve
  budget="$(secs FOLD_NESTED_CLEANUP_BUDGET 170 3)"
  reserve="$(secs FOLD_NESTED_RM_RESERVE 30 1)"
  (( reserve * 3 <= budget )) || reserve=$((budget / 3))
  CLEAN_END=$((SECONDS + budget))
  RM_AT=$((CLEAN_END - reserve))
}
# call_deadline LIMIT: one call's deadline, CALL_T from now and never past LIMIT.
call_deadline() {
  local d=$((SECONDS + CALL_T))
  (( d <= $1 )) || d=$1
  echo "$d"
}
# set_phase END sets PHASE_END, the deadline of the ownership lookups of a stop phase: the phase's own end, or one call's time if that is later (a phase must be able
# to make one call), never past the removal reserve.
set_phase() {
  local e=$1 min=$((SECONDS + CALL_T))
  (( e >= min )) || e=$min
  (( e <= RM_AT )) || e=$RM_AT
  PHASE_END=$e
}
# limited DEADLINE OUTFILE COMMAND...: a Docker client of the cleanup (stdout to the file, stderr dropped, no stdin) as a job in a process group of its own, so its
# whole group, and a client that ignores TERM, can be KILLed when the absolute DEADLINE passes (the way scripts/pg-client.sh bounds its calls). It returns the
# client's status, or 137 with LIMITED_TIMED_OUT=1 when it was killed or had no time left to start. LIMITED_JOB names it while it runs so cleanup can end it if a
# signal cuts this short. Only that job's group is signalled; nothing is looked up by name or read from a process list.
limited() {
  local deadline=$1 out=$2 rc=0
  shift 2
  LIMITED_TIMED_OUT=0
  if (( SECONDS >= deadline )); then LIMITED_TIMED_OUT=1; return 137; fi
  set -m
  "$@" >"$out" 2>/dev/null </dev/null &
  LIMITED_JOB=$!
  set +m
  { # bash's own "Killed" notice for a job we killed goes to stderr when the job is reaped
    while kill -0 "$LIMITED_JOB" 2>/dev/null; do
      if (( SECONDS >= deadline )); then
        kill -KILL -- "-$LIMITED_JOB" 2>/dev/null || kill -KILL "$LIMITED_JOB" 2>/dev/null || true
        LIMITED_TIMED_OUT=1
        break
      fi
      sleep 0.1 2>/dev/null || sleep 1
    done
    wait "$LIMITED_JOB" || rc=$?
    LIMITED_JOB=""
  } 2>/dev/null
  if (( LIMITED_TIMED_OUT )); then rc=137; fi
  return "$rc"
}
OWN_MARK="FOLD_NESTED_RUN=$RUN_ID"
# The lookup runs inside the container (busybox sh): it lists the processes whose environment holds the marker and, by mode, signals them. Modes: count (signal
# nothing), termroots (TERM the owned processes whose parent is not owned, so a script's own trap can clean up its children), killall (KILL every owned
# process). It prints how many it found; nothing else.
OWN_SCAN=$(cat <<'SCAN'
m=$1; mode=$2; self=$$; list=" "
for d in /proc/[0-9]*; do
  p=${d#/proc/}
  [ "$p" = "$self" ] && continue
  tr '\000' '\n' <"$d/environ" 2>/dev/null | grep -qxF "$m" && list="$list$p "
done
n=0
for p in $list; do
  n=$((n+1)); sig=
  case $mode in
    killall) sig=KILL ;;
    termroots)
      pp=$(sed -n 's/^[0-9]* (.*) [A-Za-z] \([0-9]*\) .*/\1/p' "/proc/$p/stat" 2>/dev/null)
      case "$list" in *" $pp "*) ;; *) sig=TERM ;; esac ;;
  esac
  [ -z "$sig" ] || kill -"$sig" "$p" 2>/dev/null
done
echo "$n"
SCAN
)
# owned MODE sets OWNED_N to the number of owned processes and succeeds only when the lookup itself worked. A failed docker exec, a lookup that printed nothing or
# something else is a failure, never "none": an absence is only ever established by a lookup that ran and counted zero. Call it directly, not in $(...), so the
# error count survives. The call is bounded (limited): CALL_T seconds, or the end of the stop phase (PHASE_END) if that comes first; one that stalls is killed and
# counted as a failure and as a timeout.
owned() {
  local out rc=0 d=$((SECONDS + CALL_T))
  OWNED_N=""
  if [[ -n "$PHASE_END" ]] && (( d > PHASE_END )); then d=$PHASE_END; fi
  limited "$d" "$TMP/owned.out" docker exec "$CID" sh -c "$OWN_SCAN" fold-owned-scan "$OWN_MARK" "$1" || rc=$?
  if (( LIMITED_TIMED_OUT )); then TO_LOOKUP=$((TO_LOOKUP + 1)); fi
  if (( rc == 0 )) && out="$(<"$TMP/owned.out")" && [[ "$out" =~ ^[0-9]+$ ]]; then
    OWNED_N="$out"
    return 0
  fi
  STOP_LOOKUP_ERRORS=$((STOP_LOOKUP_ERRORS + 1))
  return 1
}
# stopped_confirmed: two successful lookups, a second apart, both counting zero (the second catches an exec the daemon was still starting).
stopped_confirmed() {
  owned count && [[ "$OWNED_N" == 0 ]] || return 1
  sleep 1
  owned count && [[ "$OWNED_N" == 0 ]]
}
# inner_exec DOCKER_EXEC_ARGS...: the workload, started in the background and waited for, so that a signal runs the trap at once and not after a many-hour run.
# A signal sent to the whole process group can end the docker client a moment before this script's own trap runs, so a status of 128 or more is given a short,
# bounded time (FOLD_NESTED_SIGNAL_WAIT seconds, default 5) in which that trap can fire and take over. It is not proof of anything: the same status comes from a
# docker client killed by someone else, or from a workload that died of a signal or the OOM killer while a marked writer lives on. When no signal arrives the status
# is returned as it is, and the caller goes on into the ordinary stop, verify, collect, remove sequence, which is bounded by its own grace times.
inner_exec() {
  local rc=0 wait_end
  docker exec -e "$OWN_MARK" "$@" &
  INNER_CLIENT=$!
  wait "$INNER_CLIENT" || rc=$?
  INNER_CLIENT=""
  if (( rc >= 128 )); then
    wait_end=$((SECONDS + $(secs FOLD_NESTED_SIGNAL_WAIT 5 0)))
    PHASE_END=$wait_end
    while (( SECONDS < wait_end )); do
      if owned count && [[ "$OWNED_N" == 0 ]]; then break; fi
      sleep 1
    done
    PHASE_END=""
  fi
  return "$rc"
}
# stop_inner ends everything the workload left running, and says whether that is established. Nothing is copied before it returns. After an interruption the
# owned top-level processes get TERM first (their traps finish the run's own cleanup); after a normal end only leftovers are asked. What is still there after
# FOLD_NESTED_STOP_GRACE seconds (default 90) is killed, every owned process, and the stop is verified again, for up to FOLD_NESTED_KILL_WAIT seconds (default 15).
# Both are absolute deadlines from the start of the stop, and together they get at most three quarters of the time before the removal reserve (open_window), so the
# copies keep the rest. If a stop cannot be established (the lookup keeps failing or stalling, or a process will not die) the container is frozen, so what is copied
# is at least one moment's state, and the caller records the copy as incomplete. The container's own daemon calls go through docker, so a TERM that reached only the
# docker client on this host would stop nothing inside.
# shellcheck disable=SC2329 # reached through the EXIT trap (cleanup).
stop_inner() {
  local grace killwait pre cap term_end kill_end d
  open_window
  grace="$(secs FOLD_NESTED_STOP_GRACE 90 0)"
  killwait="$(secs FOLD_NESTED_KILL_WAIT 15 0)"
  pre=$((RM_AT - SECONDS))
  (( pre > 0 )) || pre=0
  cap=$((pre * 3 / 4))
  if (( grace + killwait > cap )); then
    if (( killwait > cap )); then killwait=$cap; grace=0; else grace=$((cap - killwait)); fi
  fi
  term_end=$((SECONDS + grace))
  kill_end=$((term_end + killwait))
  STOP_VERIFIED=0 STOPPED_HOW=none
  set_phase $((SECONDS + 2 * CALL_T + 1))
  if [[ -z "$INTERRUPTED" ]] && stopped_confirmed; then STOP_VERIFIED=1; PHASE_END=""; return 0; fi
  STOPPED_HOW=term
  set_phase "$term_end"
  if owned termroots && [[ "$OWNED_N" == 0 ]]; then STOPPED_HOW=none; fi
  while (( SECONDS < term_end )); do
    if stopped_confirmed; then STOP_VERIFIED=1; PHASE_END=""; return 0; fi
    sleep 1
  done
  STOPPED_HOW="kill"
  echo "restore-drill-nested: the run inside did not stop within ${grace}s of TERM, or its stop could not be looked up; killing it" >&2
  (( kill_end > SECONDS )) || kill_end=$((SECONDS + CALL_T))
  (( kill_end <= RM_AT )) || kill_end=$RM_AT
  set_phase "$kill_end"
  while :; do
    owned killall || true
    if stopped_confirmed; then STOP_VERIFIED=1; PHASE_END=""; return 0; fi
    (( SECONDS < kill_end )) || break
    sleep 1
  done
  PHASE_END=""
  d="$(call_deadline "$RM_AT")"
  if limited "$d" "$TMP/pause.out" docker pause "$CID"; then
    FROZEN=1
  elif (( LIMITED_TIMED_OUT )); then
    TO_PAUSE=1
  fi
  echo "restore-drill-nested: could not confirm that the run inside has stopped ($(lookup_text)); $(frozen_text)" >&2
  return 0
}
# lookup_text and frozen_text say, for the records, how the lookups and the freeze went; a stalled call is named as such.
lookup_text() {
  printf 'lookup failures: %s' "$STOP_LOOKUP_ERRORS"
  if (( TO_LOOKUP )); then printf ', %s of them stalled past their deadline or had no time left and were killed' "$TO_LOOKUP"; fi
}
frozen_text() {
  if (( FROZEN )); then printf 'the container was frozen'
  elif (( TO_PAUSE )); then printf 'the container may NOT be frozen: the pause stalled and was killed'
  else printf 'the container was NOT frozen'
  fi
}
# copy_out SRC DEST LIMIT: docker cp, killed at the absolute deadline LIMIT. It prints nothing; on a failure it sets COPY_WHY to say whether the copy stalled.
copy_out() {
  local rc=0
  COPY_WHY=""
  limited "$3" "$TMP/cp.out" docker cp "$CID:$1" "$2" || rc=$?
  if (( LIMITED_TIMED_OUT )); then COPY_WHY=' (the copy stalled past its deadline and was killed; what was copied may be partial)'; fi
  return "$rc"
}
# collect_artifacts [prep] copies the preparation and run directories out (only the preparation with `prep`: a run that never started has no /work/out). A copy that
# fails or stalls, and a stop that was not established, are said on stderr and recorded in COLLECTION-FAILED.txt; the caller must treat the run as failed. Both
# copies end before the removal reserve; the preparation (small) never gets more than a quarter of the time left, so a stalled one cannot starve the evidence.
collect_artifacts() {
  local d
  COLLECT_FAILED=()
  open_window
  d=$(( (RM_AT - SECONDS) / 4 ))
  (( d >= CALL_T )) || d=$CALL_T
  d=$((SECONDS + d))
  (( d <= RM_AT )) || d=$RM_AT
  # With --no-browser there is no preparation directory, and its absence is not a failure.
  if ! copy_out /work/prep/. "$OUT/prep/" "$d" && (( ! skip_browser )); then
    COLLECT_FAILED+=("/work/prep (the browser runtime preparation) could not be copied out$COPY_WHY")
  fi
  if [[ "${1:-}" != prep ]] && ! copy_out /work/out/. "$OUT/" "$RM_AT"; then
    echo 'restore-drill-nested: no evidence directory to copy out' >&2
    COLLECT_FAILED+=("/work/out (the run's evidence) could not be copied out$COPY_WHY")
  fi
  if (( ! STOP_VERIFIED )); then
    COLLECT_FAILED+=("the run inside could not be confirmed stopped before the copy ($(lookup_text); $(frozen_text)); the copy may be incomplete or inconsistent")
  fi
  COLLECTED=1
  if (( ${#COLLECT_FAILED[@]} )); then
    printf '%s\n' "${COLLECT_FAILED[@]}" >"$OUT/COLLECTION-FAILED.txt" || true
  fi
}
# remove_container ends the container by id and returns 0 once it is gone, 1 if it cannot be shown to be. A failing `docker rm` is not yet a failure: the container may
# already be gone, so what counts is a listing by its exact id that ran and came back empty. Each call is bounded (limited) and none starts after CLEAN_END, the end of
# the cleanup window; a stalled one is killed and the removal tried again while time remains. Nothing but this container is named.
# shellcheck disable=SC2329 # reached through the EXIT trap (cleanup).
remove_container() {
  local rc d
  open_window
  while (( SECONDS < CLEAN_END )); do
    rc=0
    d="$(call_deadline "$CLEAN_END")"
    limited "$d" "$TMP/rm.out" docker rm -fv "$CID" || rc=$?
    (( rc != 0 )) || return 0
    if (( LIMITED_TIMED_OUT )); then TO_RM=$((TO_RM + 1)); fi
    rc=0
    d="$(call_deadline "$CLEAN_END")"
    limited "$d" "$TMP/rm.out" docker ps -a --no-trunc --filter "id=$CID" --format '{{.ID}}' || rc=$?
    if (( rc == 0 )) && [[ -z "$(<"$TMP/rm.out")" ]]; then return 0; fi
    (( SECONDS < CLEAN_END )) || break
    sleep 0.2 2>/dev/null || sleep 1
  done
  return 1
}
# stop_text prints, for the note in the evidence directory, how the workload was stopped.
stop_text() {
  case "$STOPPED_HOW" in
    none) echo 'Nothing of the run was still running inside.' ;;
    term) echo 'The run inside was asked to stop (TERM) and finished its own cleanup.' ;;
    kill) echo 'The run inside was asked to stop (TERM) and did not (or the lookup failed); it was killed, so its own cleanup and final records may be missing.' ;;
  esac
}
# shellcheck disable=SC2329 # EXIT trap.
cleanup() {
  local rc=$? note
  trap - EXIT
  trap '' INT TERM HUP   # a second signal must not cut the collection short
  # A call a signal cut short is ended, not waited for; the cleanup's own calls are all bounded (limited).
  if [[ -n "$LIMITED_JOB" ]]; then
    kill -KILL -- "-$LIMITED_JOB" 2>/dev/null || kill -KILL "$LIMITED_JOB" 2>/dev/null || true
    LIMITED_JOB=""
  fi
  PHASE_END=""
  if [[ -n "$CID" ]] && (( ARMED && ! COLLECTED )); then
    stop_inner
    collect_artifacts
    partial_marker || true
    if [[ -n "$INTERRUPTED" ]]; then
      {
        printf 'restore-drill-nested: interrupted by %s.\n' "$INTERRUPTED"
        stop_text
        echo 'What the container held was copied here before it was removed; see COLLECTION-FAILED.txt, if present, for anything that was not.'
        echo 'This directory is NOT acceptance evidence.'
      } >"$OUT/INTERRUPTED.txt" || true
      echo "restore-drill-nested: interrupted by $INTERRUPTED; artifacts copied to $OUT$([[ ${#COLLECT_FAILED[@]} -eq 0 ]] || echo ' (some could not be: COLLECTION-FAILED.txt)')" >&2
    elif [[ "$STOPPED_HOW" != none ]]; then
      stop_text >"$OUT/LEFTOVERS-STOPPED.txt" || true
    fi
  fi
  if [[ -n "$CID" ]]; then
    open_window
    if (( FROZEN || TO_PAUSE )); then limited "$(call_deadline "$CLEAN_END")" "$TMP/unpause.out" docker unpause "$CID" || true; fi
    if ! remove_container; then
      # Removal could not be shown to have worked: the container may still be running, with its daemon, its volume and whatever the drill started in it. Say which one
      # and how to end it, where the person and the wrapper will read it, and fail a run that would have passed.
      note="the container $NAME ($CID) could not be removed within the cleanup window$([[ $TO_RM == 0 ]] || echo " ($TO_RM removal calls stalled and were killed)"); it may still be running. Remove it yourself: docker rm -fv $CID"
      echo "restore-drill-nested: $note" >&2
      printf '%s\n' "$note" >>"$OUT/COLLECTION-FAILED.txt" || true
      if (( rc == 0 )) || (( CROSS && rc == 10 )); then rc=1; fi
    fi
  fi
  [[ -z "$INNER_CLIENT" ]] || kill -KILL "$INNER_CLIENT" >/dev/null 2>&1 || true
  rm -rf -- "$TMP"
  exit "$rc"
}
# shellcheck disable=SC2329 # signal traps.
on_signal() { trap '' INT TERM HUP; INTERRUPTED="$1"; CLEAN_END=""; exit "$2"; }
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
    stop_inner
    collect_artifacts prep
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
stop_inner
collect_artifacts
[[ "$STOPPED_HOW" == none ]] || stop_text >"$OUT/LEFTOVERS-STOPPED.txt"
# What could not be collected is a failure of the run, whatever the workload itself said: a missing copy leaves a pass nothing to stand on.
if (( ${#COLLECT_FAILED[@]} )); then
  echo "restore-drill-nested: the evidence could not be collected completely (see $OUT/COLLECTION-FAILED.txt); the run is treated as failed" >&2
  if (( rc == 0 )) || (( CROSS && rc == 10 )); then rc=1; fi
fi
if (( CROSS )); then
  partial_marker
  if (( rc == 0 )); then echo 'restore-drill-nested: a crossings-only run reported 0, which only a full run may; treating it as failed' >&2; rc=1; fi
  echo 'restore-drill-nested: crossings-only is a PARTIAL diagnostic: NOT the full drill, NOT acceptance (a pass exits 10; only a full run exiting 0 is acceptance)'
fi
echo "restore-drill-nested: $what exit=$rc evidence=$OUT"
exit "$rc"
