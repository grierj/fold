#!/usr/bin/env bash
# T07B-2-V QC-1: measure the stateless reason review (page routes, the DELETE
# set statement, deletion with keep/remove/mixed choices, real request bodies
# near and across maxRequestBytes) at 10k, 100k and 1M contact_requests rows on
# a disposable stack: PostgreSQL 16 (the image the shipped Compose file names)
# and the committed head's real binary, held to the resources of the smallest
# recommended droplet (s-1vcpu-2gb: one shared vCPU, 2 GiB). It measures; it is
# not a pass/fail gate: the report lists each budget failure for a reviewed
# engineering disposition (R4-C06) and says plainly when a run is not complete
# evidence.
#
# Isolation. Run it against a nested (Docker-in-Docker) or rootless daemon that
# is not running anything else; it refuses a daemon with running containers
# unless --shared-daemon, and then still touches only what it created, by id:
# one network, one volume, its own containers. It needs no root, no host
# cgroup paths and no host process ids: memory is read through the daemon
# (docker exec), so a nested daemon works. It never prunes, never removes by
# name or pattern and never uses the host's Compose stack.
#
# Identity. The tree must be clean and the image must be labelled with HEAD's
# SHA (one is built from `git archive HEAD` when --image is not given). The
# image and the Postgres image are resolved to immutable sha256 ids once and
# every container runs by id; the measurement sources' hashes are recorded.
#
#   deploy/digitalocean/qc1-measure.sh --dir NEW_SCRATCH_PATH [--image IMG]
#        [--sizes 10000,100000,1000000] [--counts 0,10,100,1000,10000]
#        [--layout spread|tail|head] [--reason-len N] [--target-reason-len N] [--droplet 2gb|1gb]
#        [--dense-counts 100000|none] [--seconds-per-reason N] [--sitting-minutes N] [--shared-daemon] [--no-browser] [--browser-fallback-digest] [--keep]
#
# --dense-counts: reasons in ONE account at the tables that can hold them, additive to --counts (the scaling design's R5: 100,000 at the 100k and 1M
# tables, judged against the same budgets). "none" leaves them out (a pilot); a run without them is complete only for the sizes it names.
# --reason-len: the length of the BACKGROUND rows (default 200). --target-reason-len: the length of the dense TARGET histories (the account being discovered,
# deleted or opened in the browser; default 1000, the contact-reason maximum), so page sizes are measured at the largest reason the product accepts.
# A run is FULL only when its environment record names the whole matrix (sizes 10k/100k/1M, counts 0..10k, deletion counts 0..10k, the dense 100,000, the
# browser, 1,000-rune targets); anything less is a PILOT, said in evidence-status.json (coverage) and in the verdict. A pilot's complete means only that
# every row it asked for exists and is clean.
# --browser-fallback-digest: one extra rendered-panel case at the largest browser count of the first size with crypto.subtle removed, so the bundled
# SHA-256 runs (not required by the matrix).
#
# Results under --dir: measurements.jsonl, environment.json, explain-<size>.log,
# failures.jsonl, evidence-status.json (complete only when every measurement,
# instrument and identity check holds), summary.json, harness.log.
set -Eeuo pipefail
export LC_ALL=C
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
HERE="$ROOT/deploy/digitalocean"
# shellcheck source=deploy/digitalocean/qc1/lib.sh
source "$HERE/qc1/lib.sh"
# shellcheck source=deploy/digitalocean/isolation.sh
source "$HERE/isolation.sh"

WORK="" IMAGE="" SIZES="$QC1_SIZES_DEFAULT" COUNTS="$QC1_COUNTS_DEFAULT" LAYOUT=spread REASON_LEN=200 TARGET_REASON_LEN=1000 DROPLET=2gb KEEP=0
SECONDS_PER_REASON=10 SITTING_MINUTES=15 SHARED=0 BROWSER=1 DENSE="$QC1_DENSE_COUNTS_DEFAULT" BROWSER_NOSUBTLE=0
while (( $# )); do
  case "$1" in
    --dir) WORK="${2:?new scratch path required}"; shift 2 ;;
    --image) IMAGE="${2:?candidate image required}"; shift 2 ;;
    --sizes) SIZES="${2:?comma list required}"; shift 2 ;;
    --counts) COUNTS="${2:?comma list required}"; shift 2 ;;
    --layout) LAYOUT="${2:?spread, tail or head}"; shift 2 ;;
    --reason-len) REASON_LEN="${2:?length required}"; shift 2 ;;
    --target-reason-len) TARGET_REASON_LEN="${2:?length required}"; shift 2 ;;
    --droplet) DROPLET="${2:?2gb or 1gb}"; shift 2 ;;
    --seconds-per-reason) SECONDS_PER_REASON="${2:?whole seconds required}"; shift 2 ;;
    --sitting-minutes) SITTING_MINUTES="${2:?whole minutes required}"; shift 2 ;;
    --shared-daemon) SHARED=1; shift ;;
    --no-browser) BROWSER=0; shift ;;
    --dense-counts) DENSE="${2:?comma list or none}"; shift 2 ;;
    --browser-fallback-digest) BROWSER_NOSUBTLE=1; shift ;;
    --keep) KEEP=1; shift ;;
    --help) sed -n '2,/^set -Eeuo/p' "${BASH_SOURCE[0]}" | sed '$d' | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown option $1" >&2; exit 1 ;;
  esac
done
for tool in docker git jq curl awk sha256sum node; do command -v "$tool" >/dev/null || { echo "qc1: $tool is required" >&2; exit 1; }; done
[[ -n "$WORK" ]] || { echo 'qc1: --dir NEW_SCRATCH_PATH is required' >&2; exit 1; }
WORK="$(realpath -m "$WORK")"
[[ "$WORK" == /* && "$WORK" != /opt/fold && "$WORK" != /opt/fold/* && ! -e "$WORK" ]] || { echo 'qc1: --dir must be a new scratch path outside /opt/fold' >&2; exit 1; }
[[ "$LAYOUT" =~ ^(spread|tail|head)$ ]] || { echo 'qc1: --layout is spread, tail or head' >&2; exit 1; }
if [[ ! "$REASON_LEN" =~ ^[0-9]+$ ]] || (( REASON_LEN < 1 || REASON_LEN > 1000 )); then echo 'qc1: --reason-len is 1..1000' >&2; exit 1; fi
if [[ ! "$TARGET_REASON_LEN" =~ ^[0-9]+$ ]] || (( TARGET_REASON_LEN < 1 || TARGET_REASON_LEN > 1000 )); then echo 'qc1: --target-reason-len is 1..1000' >&2; exit 1; fi
[[ "$SECONDS_PER_REASON" =~ ^[1-9][0-9]*$ && "$SITTING_MINUTES" =~ ^[1-9][0-9]*$ ]] || { echo 'qc1: --seconds-per-reason and --sitting-minutes are whole numbers above 0' >&2; exit 1; }
case "$DROPLET" in
  2gb) DB_MEMORY=1200m; APP_MEMORY=600m ;;
  1gb) DB_MEMORY=600m; APP_MEMORY=300m ;;
  *) echo 'qc1: --droplet is 2gb or 1gb' >&2; exit 1 ;;
esac
mapfile -t SIZE_LIST < <(qc1_list "$SIZES")
mapfile -t COUNT_LIST < <(qc1_list "$COUNTS")
mapfile -t DELETE_COUNT_LIST < <(qc1_list "$QC1_DELETE_COUNTS")
DENSE_LIST=()
if [[ "$DENSE" != none ]]; then mapfile -t DENSE_LIST < <(qc1_list "$DENSE"); fi
# The discovery and deletion matrices run over the counts plus the dense targets; the browser panel over its counts plus the dense targets.
mapfile -t ALL_COUNT_LIST < <(printf '%s\n' "${COUNT_LIST[@]}" "${DENSE_LIST[@]}" | sort -n -u)
mapfile -t ALL_DELETE_LIST < <(printf '%s\n' "${DELETE_COUNT_LIST[@]}" "${DENSE_LIST[@]}" | sort -n -u)
mapfile -t ALL_BROWSER_LIST < <(printf '%s\n' "$(qc1_list "$QC1_BROWSER_COUNTS")" "${DENSE_LIST[@]}" | sort -n -u)
(( ${#SIZE_LIST[@]} > 0 && ${#COUNT_LIST[@]} > 0 )) || { echo "qc1: --sizes and --counts need whole numbers" >&2; exit 1; }
MAX_SIZE="$(printf '%s\n' "${SIZE_LIST[@]}" | sort -n | tail -n 1)"

# --- the daemon: nothing else may be running on it -----------------------------------
# Checked before anything is created, so a refusal leaves no trace.
DOCKER_INFO="$(docker info --format '{{json .}}' 2>/dev/null)" || { echo 'qc1: cannot reach a Docker daemon' >&2; exit 1; }
RUNNING_AT_START="$(jq -r '.ContainersRunning // 0' <<<"$DOCKER_INFO")"
SOCKET_PROBLEMS="$(isolation_mount_problems /proc/self/mountinfo sockets)"
if [[ -n "$SOCKET_PROBLEMS" ]]; then
  echo "qc1: this process reaches a host daemon, not a disposable one:" >&2; printf '  - %s\n' "$SOCKET_PROBLEMS" >&2; exit 1
fi
if (( RUNNING_AT_START > 0 && ! SHARED )); then
  echo "qc1: the Docker daemon already runs $RUNNING_AT_START container(s). Point DOCKER_HOST at a disposable nested or rootless daemon, or pass --shared-daemon to accept the noise (results are then not complete evidence)." >&2
  exit 1
fi
DAEMON_JSON="$(jq -c --argjson running "$RUNNING_AT_START" --argjson shared "$([[ $SHARED == 1 && $RUNNING_AT_START -gt 0 ]] && echo true || echo false)" --arg host "${DOCKER_HOST:-default}" \
  '{id:.ID, serverVersion:.ServerVersion, rootless:((.SecurityOptions // []) | any(test("rootless"))), cgroupVersion:.CgroupVersion, cgroupDriver:.CgroupDriver, ncpu:.NCPU, memTotal:.MemTotal, runningAtStart:$running, sharedDaemon:$shared, dockerHost:$host}' <<<"$DOCKER_INFO")"

# --- what is measured: HEAD, clean ------------------------------------------------------
[[ -z "$(git --no-optional-locks -C "$ROOT" status --porcelain)" ]] || { echo 'qc1: the tree is dirty; commit first (what is measured must be exactly HEAD)' >&2; exit 1; }
REVISION="$(git -C "$ROOT" rev-parse HEAD)"
SOURCES="$(qc1_source_hashes "$ROOT")" || { echo 'qc1: a measurement source differs from HEAD' >&2; exit 1; }
# The body builder is held to the shared vectors before anything is measured with it (its SHA-256 and base64 are independent of the product's).
SETV1_SELFTEST="$(qc1_setv1 selftest --vectors "$ROOT/web/src/lib/reasonSetVectors.json")" || { echo 'qc1: the set-v1 builder disagrees with the shared vectors' >&2; exit 1; }
MAX_REQUEST_BYTES="$(qc1_max_request_bytes "$ROOT")"
# The browser the item-10 measurement drives: found, launchable and exercised before
# a stack is built (browser-preflight.mjs, with the same launch the measurement
# makes). Without it the run would spend hours and still be incomplete, so it stops
# here unless --no-browser says the browser rows are being skipped on purpose.
BROWSER_RUNTIME=null
if (( BROWSER )); then
  command -v node >/dev/null || { echo 'qc1: node is required for the browser measurement (or pass --no-browser)' >&2; exit 1; }
  BROWSER_RUNTIME="$(QC1_WEB_DIR="$ROOT/web" node "$HERE/qc1/browser-preflight.mjs")" \
    || { echo 'qc1: the browser preflight failed: run npm ci in web/ and point QC1_CHROMIUM at an installed Chromium (restore-drill-nested.sh --qc1 does both), or pass --no-browser' >&2; exit 1; }
  jq -e '.ok == true' <<<"$BROWSER_RUNTIME" >/dev/null || { echo "qc1: the browser preflight did not pass: $BROWSER_RUNTIME" >&2; exit 1; }
fi
read -r GATE_CLASS GATE_OBJ <<<"$(qc1_gate_key)"

mkdir -p "$WORK"
exec > >(tee "$WORK/harness.log") 2>&1

JAR="$WORK/cookies" URL=""
MEASUREMENTS="$WORK/measurements.jsonl"; : >"$MEASUREMENTS"
SAMPLERS=() CONTAINERS=() NET_ID="" VOL="" APP_CID="" DB_CID="" SIDE_CID="" CURRENT_WORKERS=0 APP_STARTED_MS=0
APP_CONTAINERS="[]"
# 1 from just before the first statement that turns auto_explain (or log_lock_waits) on in the runner's own database until the moment both are proven off in a new session.
# While it is 1, an exit of any kind (a failure, a signal, --keep) owes the database a reset: ALTER SYSTEM is persistent, so a stack kept for inspection would otherwise go on
# logging a plan for every statement of every session. Set BEFORE the change so a failure halfway through it is covered too.
AUTOEXPLAIN_DIRTY=0

# Best effort, on the runner's own database container only (never a database the owner supplied: there is none, DB_CID is the container this run started): put auto_explain's
# threshold back to -1 and log_lock_waits back to its default, reload, and read both back in a new session. Bounded in time (a hung daemon cannot hold an exit), three tries.
# Succeeds only when both read back as off; says nothing itself, the caller reports.
# shellcheck disable=SC2329 # called from cleanup.
autoexplain_reset() {
  local i a="" b=""
  [[ -n "$DB_CID" ]] || return 1
  for i in 1 2 3; do
    if timeout 20 docker exec -i "$DB_CID" psql -X -U church -d "${QC1_DB:-fold}" -At -v ON_ERROR_STOP=1 \
         -c "ALTER SYSTEM SET auto_explain.log_min_duration = -1" -c "ALTER SYSTEM RESET log_lock_waits" -c 'SELECT pg_reload_conf()' </dev/null >/dev/null 2>&1; then
      for _ in 1 2 3 4 5 6 7 8 9 10; do
        a="$(timeout 20 docker exec -i "$DB_CID" psql -X -U church -d "${QC1_DB:-fold}" -At -c 'SHOW auto_explain.log_min_duration' </dev/null 2>/dev/null || true)"
        b="$(timeout 20 docker exec -i "$DB_CID" psql -X -U church -d "${QC1_DB:-fold}" -At -c 'SHOW log_lock_waits' </dev/null 2>/dev/null || true)"
        [[ "$a" == -1 && "$b" == off ]] && return 0
        sleep 0.5
      done
    fi
    sleep 1
  done
  return 1
}

# --- evidence that survives a run that stops ---------------------------------------------------------------------------------------------------------------------
# finalize_evidence writes environment.json, failures.jsonl, evidence-status.json and summary.json. The normal end of a run calls it once; cleanup calls it, before any
# container is removed, for a run that ended any other way (an error, a signal, --keep) and has not finalized. It is idempotent (FINALIZED), it only writes files, it
# never touches the exit status, and every external read is bounded and may fail: an unreadable source is recorded as null with the environment marked incomplete. For an
# aborted run abort.json says how it ended, evidence-status.json carries it as `aborted` and is forced to complete:false and releaseEvidence:false, and the rows measured
# so far are judged exactly as a finished run's are: their budget failures stay findings and every case not reached is listed as missing. Nothing is continued, repaired,
# re-measured or dropped.
FINALIZED=0 CURRENT_STEP="" CURRENT_SIZE="" ERR_CMD="" ERR_LINE="" ERR_FUNC="" ERR_CALL_LINE="" ERR_CALL_SRC=""
trap 'ERR_CMD=$BASH_COMMAND; ERR_LINE=$LINENO; ERR_FUNC=${FUNCNAME[0]:-}; ERR_CALL_LINE=${BASH_LINENO[0]:-}; ERR_CALL_SRC=${BASH_SOURCE[1]:-}' ERR

# JSON-safe values for an environment record written from whatever state a failed run reached.
jnum() { if [[ "${1:-}" =~ ^-?[0-9]+$ ]]; then printf '%s' "$1"; else printf null; fi; }
jraw() { if [[ -n "${1:-}" ]] && jq -e . <<<"$1" >/dev/null 2>&1; then printf '%s' "$1"; else printf '%s' "${2:-null}"; fi; }
# One bounded read from the runner's own database container (a hung daemon cannot hold an exit).
fin_psql() { timeout 15 docker exec -i "${DB_CID:-none}" psql -X -U church -d "${QC1_DB:-fold}" -At -c "$1" </dev/null 2>/dev/null; }

# The settings that decide dynamic shared memory and parallel work, and /dev/shm as the database container has it. The container keeps the daemon's default /dev/shm
# (64 MiB with Docker's default), as the shipped Compose file runs it; a parallel VACUUM or query that needs more fails with ENOSPC, which is how the 100k pilot's sparse
# fixture stopped. The fixture vacuums are not parallel (PARALLEL 0), the container is not changed.
db_settings_json() {
  local s shm total used shmbytes
  s="$(fin_psql "SELECT json_object_agg(name, setting)::text FROM pg_settings WHERE name IN ('dynamic_shared_memory_type', 'min_dynamic_shared_memory', 'max_parallel_maintenance_workers', 'max_parallel_workers_per_gather', 'max_parallel_workers', 'maintenance_work_mem', 'work_mem', 'shared_buffers')" || true)"
  s="$(jraw "$s")"
  shm="$(timeout 15 docker exec "${DB_CID:-none}" sh -c 'df -k /dev/shm | tail -n 1' </dev/null 2>/dev/null || true)"
  total="$(awk '{ print $2 }' <<<"$shm")"; used="$(awk '{ print $3 }' <<<"$shm")"
  shmbytes="$(timeout 15 docker inspect --format '{{.HostConfig.ShmSize}}' "${DB_CID:-none}" 2>/dev/null || true)"
  jq -nc --argjson s "$s" --argjson total "$(jnum "$total")" --argjson used "$(jnum "$used")" --argjson bytes "$(jnum "$shmbytes")" \
    '{settings:$s, devShm:{totalKb:$total, usedKb:$used, note:"df -k /dev/shm inside the database container, read when the record was written"}, hostConfigShmSizeBytes:$bytes,
      note:"the database container keeps the daemon default /dev/shm (not changed by this harness), as the shipped Compose file runs it; the fixture vacuums run with PARALLEL 0"}'
}

# The environment record. incomplete=true (an aborted run) marks it and records every source that could not be read as null.
write_environment() { # incomplete(true|false) reason
  local incomplete="$1" ireason="${2:-}" pg dbImage dbMem calib database
  local IMAGE="${IMAGE:-}" IMAGE_ID="${IMAGE_ID:-}" REVISION="${REVISION:-}" DROPLET="${DROPLET:-}" DB_MEMORY="${DB_MEMORY:-}" APP_MEMORY="${APP_MEMORY:-}" CPUSET="${CPUSET:-}" LAYOUT="${LAYOUT:-}"
  local PG_IMAGE="${PG_IMAGE:-}" PG_IMAGE_ID="${PG_IMAGE_ID:-}" DB_CID="${DB_CID:-}" FIRST_APP_CID="${FIRST_APP_CID:-}" SIZES="${SIZES:-}" COUNTS="${COUNTS:-}" SETV1_SELFTEST="${SETV1_SELFTEST:-}" DENSE="${DENSE:-}" BROWSER="${BROWSER:-0}"
  local LEVEL REASON_LEN MAX_REQUEST_BYTES TARGET_REASON_LEN SECONDS_PER_REASON SITTING_MINUTES PG_REPO_DIGESTS APP_REPO_DIGESTS APP_CONTAINERS DAEMON_JSON SOURCES BROWSER_RUNTIME
  LEVEL="$(jnum "${LEVEL:-}")"; REASON_LEN="$(jnum "${REASON_LEN:-}")"; MAX_REQUEST_BYTES="$(jnum "${MAX_REQUEST_BYTES:-}")"; TARGET_REASON_LEN="$(jnum "${TARGET_REASON_LEN:-}")"
  SECONDS_PER_REASON="$(jnum "${SECONDS_PER_REASON:-}")"; SITTING_MINUTES="$(jnum "${SITTING_MINUTES:-}")"
  PG_REPO_DIGESTS="$(jraw "${PG_REPO_DIGESTS:-}")"; APP_REPO_DIGESTS="$(jraw "${APP_REPO_DIGESTS:-}")"; APP_CONTAINERS="$(jraw "${APP_CONTAINERS:-}" '[]')"
  DAEMON_JSON="$(jraw "${DAEMON_JSON:-}")"; SOURCES="$(jraw "${SOURCES:-}")"; BROWSER_RUNTIME="$(jraw "${BROWSER_RUNTIME:-}")"
  local -a adl=()
  if declare -p ALL_DELETE_LIST >/dev/null 2>&1; then adl=("${ALL_DELETE_LIST[@]}"); fi
  local -a ALL_DELETE_LIST=("${adl[@]}")
  pg="$(fin_psql 'SHOW server_version' || true)"
  dbImage="$(timeout 15 docker inspect --format '{{.Image}}' "${DB_CID:-none}" 2>/dev/null || true)"
  dbMem="$(jnum "$(timeout 15 docker inspect --format '{{.HostConfig.Memory}}' "${DB_CID:-none}" 2>/dev/null || true)")"; [[ "$dbMem" != null ]] || dbMem=0
  calib="$(jraw "$(cat "$WORK/calibration.json" 2>/dev/null || true)")"
  database="$(db_settings_json)"
jq -n --argjson database "$database" --argjson incomplete "$incomplete" --arg ireason "$ireason" --arg image "$IMAGE" --arg imageId "$IMAGE_ID" --arg revision "$REVISION" --argjson level "$LEVEL" --arg droplet "$DROPLET" --arg dbMemory "$DB_MEMORY" --arg appMemory "$APP_MEMORY" \
  --arg cpuset "$CPUSET" --arg layout "$LAYOUT" --argjson reasonLen "$REASON_LEN" --argjson maxRequestBytes "$MAX_REQUEST_BYTES" \
  --arg pg "$pg" --arg pgImage "$PG_IMAGE" --arg pgImageId "$PG_IMAGE_ID" --argjson pgDigests "$PG_REPO_DIGESTS" --argjson appDigests "$APP_REPO_DIGESTS" \
  --arg dbCid "$DB_CID" --arg dbImage "$dbImage" --argjson dbMemLimit "$dbMem" \
  --arg appCid "$FIRST_APP_CID" --argjson appContainers "$APP_CONTAINERS" --argjson daemon "$DAEMON_JSON" --argjson sources "$SOURCES" \
  --argjson calibration "$calib" --arg sizes "$SIZES" --arg counts "$COUNTS" \
  --argjson targetLen "$TARGET_REASON_LEN" --arg deleteCounts "$(IFS=,; printf "%s" "${ALL_DELETE_LIST[*]}")" --arg setv1Selftest "$SETV1_SELFTEST" --arg dense "$DENSE" --argjson clicks "$QC1_CLICKS_PER_CASE" --argjson spr "$SECONDS_PER_REASON" --argjson sit "$SITTING_MINUTES" --argjson browser "$([[ $BROWSER == 1 ]] && echo true || echo false)" --argjson browserRuntime "$BROWSER_RUNTIME" \
  '{image:$image, imageId:$imageId, revision:$revision, recoveryPolicy:$level, appRepoDigests:$appDigests,
    appContainerId:$appCid, appImageId:($appContainers[0].imageId), appContainers:$appContainers,
    postgresImage:$pgImage, postgresImageId:$pgImageId, postgresRepoDigests:$pgDigests, postgresVersion:$pg, dbContainerId:$dbCid, dbImageId:$dbImage,
    limits:{droplet:$droplet, dbMemory:$dbMemory, appMemory:$appMemory, cpuset:$cpuset, dbMemoryLimitBytes:$dbMemLimit, appMemoryLimitBytes:($appContainers[0].memoryLimitBytes)},
    daemon:$daemon, sources:$sources, calibration:$calibration, browserRuntime:$browserRuntime,
    settings:{workers:0, schedulerAppRuns:true, browser:$browser, autovacuum:"off", checkpointTimeout:"1d", maxWalSize:"4GB", appEnv:"dev", layout:$layout, reasonLen:$reasonLen, targetReasonLen:$targetLen, deleteCounts:$deleteCounts, secondsPerReason:$spr, sittingMinutes:$sit, sizes:$sizes, counts:$counts, denseCounts:$dense, clicksPerCase:$clicks, wire:"set-v1", setV1Selftest:$setv1Selftest},
    maxRequestBytes:$maxRequestBytes, database:$database, incomplete:$incomplete, incompleteReason:$ireason,
    labels:{
      competitors:"FOLD_WORKERS=0 turns the job workers off but not the scheduler of the app, which enqueues jobs at start, at each full UTC hour and at church midnight; the harness waits past those edges, records a scheduler-overlap flag from a model of them and counts scheduler-shaped rows in the jobs table, and a window that touches either is invalid. Autovacuum is off, timed checkpoints are deferred, the harness checkpoints itself and waits for other active backends before each operation. The workers-on observation (item 9) is not attributable",
      cluster:"walBytes, the commit figures and the gate figures are cluster-wide counters of the whole database, not attributed to the operation; they are attributable only where no invalid finding names a competitor",
      walBytes:"cluster-wide pg_current_wal_lsn difference around the operation; walControlBytes is the same for one request that changes nothing",
      gateSpanMs:"the span from the first to the last sample that saw the exclusive contact gate granted, not a hold time: the gate may have been released and retaken between samples, and a hold shorter than the gap between samples can be missed. The sampler cadence is measured per operation (gateMaxGapMs, gateMeanGapMs, gateHoles); no fixed bound is claimed",
      commit:"pg_stat_statements COMMIT entries, cluster-wide: commitMsMax is the slowest COMMIT in the window, commitMsTotal their sum, commitCalls their count",
      appPeakKb:"VmHWM of the app process minus the value right after a clear_refs reset made for this operation; appHwmReset says the reset was verified",
      dbPeakKb:"peak of the database container cgroup memory.current sampled in a docker exec loop (dbMemSamples samples), minus its first sample; page cache included",
      totalMs:"wall clock of the harness curl loop around a whole traversal (a shell harness, not a rendered panel); requestMs the page requests alone; clientMs the rest. The rendered panel is item 10",
      assumptions:"secondsPerReason and sittingMinutes are human assumptions, kept apart from measurements"},
    limitsApplied:($dbMemLimit > 0 and ($appContainers[0].memoryLimitBytes > 0))}' >"$WORK/environment.json.tmp" && mv "$WORK/environment.json.tmp" "$WORK/environment.json"
}

# How a run ended, from what the shell knows: the exit status, the signal, the step it was in, the command that failed and the last lines of the log.
write_abort_record() { # exit-code signal
  local rc="$1" sig="${2:-}" rows last sha tail calltext=""
  if [[ -n "${ERR_CALL_SRC:-}" && "${ERR_CALL_LINE:-}" =~ ^[0-9]+$ && -r "$ERR_CALL_SRC" ]]; then calltext="$(sed -n "${ERR_CALL_LINE}p" "$ERR_CALL_SRC" 2>/dev/null | sed 's/^[[:space:]]*//' | cut -c1-400)"; fi
  rows="$(wc -l <"$MEASUREMENTS" 2>/dev/null || true)"; rows="$(jnum "${rows// /}")"; [[ "$rows" != null ]] || rows=0
  last="$(tail -n 1 "$MEASUREMENTS" 2>/dev/null | jq -c '{seq, item, case, size, status}' 2>/dev/null || true)"; last="$(jraw "$last")"
  sha="$(sha256sum "$MEASUREMENTS" 2>/dev/null | cut -d' ' -f1 || true)"
  tail="$(tail -n 40 "$WORK/harness.log" 2>/dev/null | jq -Rsc 'split("\n") | map(select(length > 0))' 2>/dev/null || true)"; tail="$(jraw "$tail" '[]')"
  jq -nc --argjson rc "$rc" --arg sig "$sig" --arg step "${CURRENT_STEP:-}" --arg size "${CURRENT_SIZE:-}" --arg cmd "${ERR_CMD:-}" --arg line "${ERR_LINE:-}" --arg fn "${ERR_FUNC:-}" --arg cline "${ERR_CALL_LINE:-}" --arg ctext "$calltext" \
    --argjson rows "$rows" --argjson last "$last" --arg sha "$sha" --argjson tail "$tail" \
    '{outcome:"aborted", exitCode:$rc, signal:(if $sig == "" then null else $sig end), step:(if $step == "" then null else $step end), sizeInProgress:($size | tonumber? // null),
      failingCommand:(if $cmd == "" then null else $cmd end), failingLine:($line | tonumber? // null), failingFunction:(if $fn == "" then null else $fn end), calledFrom:(if $ctext == "" then null else {line:($cline | tonumber? // null), text:$ctext} end), rowsMeasured:$rows, lastRow:$last, measurementsSha256:$sha, harnessLogTail:$tail,
      note:"this run did not finish: evidence-status.json is forced to complete:false and releaseEvidence:false; the rows in measurements.jsonl are as they were measured, judged as a finished run is judged; nothing was continued, repaired or re-measured; failingCommand is the last command that failed before the exit, harnessLogTail the last lines of harness.log as the log had them"}' \
    >"$WORK/abort.json.tmp" && mv "$WORK/abort.json.tmp" "$WORK/abort.json"
}

finalize_evidence() { # complete|aborted exit-code [signal]
  local outcome="$1" rc="${2:-0}" sig="${3:-}" status=0 judge_ok=true abort="" incomplete=false
  (( FINALIZED )) && return 0
  FINALIZED=1
  [[ -n "${WORK:-}" && -d "$WORK" && -n "${MEASUREMENTS:-}" && -e "$MEASUREMENTS" && -n "${SIZES:-}" ]] || return 0
  if [[ "$outcome" == aborted ]]; then
    incomplete=true
    write_abort_record "$rc" "$sig" || { echo 'qc1: WARNING: abort.json could not be written' >&2; status=1; }
    [[ -s "$WORK/abort.json" ]] && abort="$WORK/abort.json"
  fi
  write_environment "$incomplete" "$([[ "$incomplete" == true ]] && echo 'the run ended before it finished; sources that could not be read are null' || true)" || { echo 'qc1: WARNING: environment.json could not be written' >&2; status=1; }
  qc1_judge "$MEASUREMENTS" "${MAX_REQUEST_BYTES:-0}" >"$WORK/failures.jsonl" || judge_ok=false
  qc1_evidence_status "$MEASUREMENTS" "$SIZES" "${COUNTS:-}" "$WORK/environment.json" "${REVISION:-}" "${MAX_REQUEST_BYTES:-0}" "$(IFS=,; printf '%s' "${DENSE_LIST[*]:-}")" "$abort" >"$WORK/evidence-status.json" \
    || { echo 'qc1: WARNING: evidence-status.json could not be written' >&2; status=1; }
  jq -n --slurpfile env "$WORK/environment.json" --slurpfile status "$WORK/evidence-status.json" --argjson judgeOk "$judge_ok" \
    '{environment:$env[0], evidence:$status[0], judgeRan:$judgeOk,
      nextStep:"budgetFailures need a reviewed engineering disposition (written design and independent review in state.json) before any release containing T07B-2 is planned; a run is release evidence only when evidence.releaseEvidence is true (complete AND full coverage); a pilot run being complete means only that the rows it asked for exist and are clean; evidence.notMeasured lists what this harness does not measure"}' \
    >"$WORK/summary.json" || { echo 'qc1: WARNING: summary.json could not be written' >&2; status=1; }
  return "$status"
}

# Everything this run made is removed by id; nothing else on the daemon is touched.
# Preserve an existing failure; a reset that cannot be verified also fails a successful run.
# shellcheck disable=SC2329 # EXIT trap.
cleanup() {
  local rc=$? p id reset=not-needed fin=not-needed sig=""
  local orig=$rc
  trap - EXIT
  trap - ERR   # ERR_CMD is the last command that failed before the exit; cleanup must not overwrite it
  trap '' INT TERM HUP   # a second signal must not cut the reset or the removals short
  for p in "${SAMPLERS[@]}"; do kill "$p" 2>/dev/null || true; done
  if (( AUTOEXPLAIN_DIRTY )); then
    if autoexplain_reset; then reset=ok; else
      reset=failed
      (( rc != 0 )) || rc=1
      if (( KEEP )); then
        echo "qc1: WARNING: auto_explain / log_lock_waits could not be proven off in the runner database ${DB_CID:-?}; this retained stack goes on logging a plan for every statement until you run: docker exec ${DB_CID:-ID} psql -U church -d fold -c 'ALTER SYSTEM SET auto_explain.log_min_duration = -1' -c 'ALTER SYSTEM RESET log_lock_waits' -c 'SELECT pg_reload_conf()'" >&2
      else
        echo "qc1: WARNING: auto_explain / log_lock_waits could not be proven off in the runner database ${DB_CID:-?}; the container is removed below, which ends it" >&2
      fi
      [[ -z "${WORK:-}" || ! -d "$WORK" ]] || echo "auto_explain reset failed; runner database container ${DB_CID:-?}; retained_stack=$KEEP" >"$WORK/autoexplain-reset-failed" 2>/dev/null || true
    fi
  fi
  # A run that stops before it finalized still leaves its evidence: the rows measured, their budget findings, what is missing, how it ended. Before any container is
  # removed (the environment record reads the database and the daemon), after the auto_explain reset, and never changing the exit status.
  if (( FINALIZED )); then fin=finalized
  elif (( orig != 0 )) && [[ -n "${MEASUREMENTS:-}" ]]; then
    case "$orig" in 129) sig=HUP ;; 130) sig=INT ;; 143) sig=TERM ;; esac
    if finalize_evidence aborted "$orig" "$sig"; then fin=aborted; else fin=aborted-incomplete; echo "qc1: WARNING: the evidence of this aborted run could not be fully written; see $WORK" >&2; fi
  fi
  if (( ! KEEP )); then
    for id in "${CONTAINERS[@]}"; do docker rm -fv "$id" >/dev/null 2>&1 || true; done
    [[ -z "$NET_ID" ]] || docker network rm "$NET_ID" >/dev/null 2>&1 || true
    if [[ -n "$VOL" ]] && [[ "$(docker volume inspect --format '{{index .Labels "fold.qc1.owner"}}' "$VOL" 2>/dev/null)" == "$VOL_TOKEN" ]]; then
      docker volume rm -f "$VOL" >/dev/null 2>&1 || true
    fi
  fi
  echo "qc1 autoexplain_reset=$reset"
  echo "qc1 evidence=$fin"
  echo "qc1 result=$rc artifacts=$WORK retained_stack=$KEEP"
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP

note() { CURRENT_STEP="$*"; printf 'qc1: %s\n' "$*"; }
ms() { date +%s%3N; }
psql_db() { docker exec -i "$DB_CID" psql -X -U church -d "${QC1_DB:-fold}" -At -v ON_ERROR_STOP=1 "$@"; }

# --- the images: by immutable id ------------------------------------------------------------
if [[ -z "$IMAGE" ]]; then
  IMAGE="qc1-fold:${REVISION:0:12}"
  mkdir "$WORK/src"
  git -C "$ROOT" archive "$REVISION" | tar -C "$WORK/src" -xf -
  docker build --label "org.opencontainers.image.revision=$REVISION" --build-arg "FOLD_VERSION=qc1-${REVISION:0:12}" -t "$IMAGE" "$WORK/src" >"$WORK/build.log" 2>&1 \
    || { tail -n 40 "$WORK/build.log" >&2; exit 1; }
  rm -rf "$WORK/src"
fi
IMAGE_ID="$(docker image inspect --format '{{.Id}}' "$IMAGE")"
[[ "$IMAGE_ID" =~ ^sha256:[0-9a-f]{64}$ ]] || { echo "qc1: cannot resolve $IMAGE to an image id" >&2; exit 1; }
IMAGE_LABEL="$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$IMAGE_ID")"
[[ "$IMAGE_LABEL" == "$REVISION" ]] || { echo "qc1: $IMAGE is labelled revision '${IMAGE_LABEL}', not HEAD $REVISION" >&2; exit 1; }
LEVEL="$(docker run --rm --network none --pull never --entrypoint /fold "$IMAGE_ID" version --json | jq -er '.recoveryPolicy // 0')"
[[ "$LEVEL" == 2 ]] || { echo "qc1: the image reports recovery level $LEVEL; reason review is level 2" >&2; exit 1; }
PG_IMAGE=postgres:16-alpine
docker image inspect "$PG_IMAGE" >/dev/null 2>&1 || docker pull "$PG_IMAGE" >/dev/null
PG_IMAGE_ID="$(docker image inspect --format '{{.Id}}' "$PG_IMAGE")"
[[ "$PG_IMAGE_ID" =~ ^sha256:[0-9a-f]{64}$ ]] || { echo "qc1: cannot resolve $PG_IMAGE to an image id" >&2; exit 1; }
PG_REPO_DIGESTS="$(docker image inspect --format '{{json .RepoDigests}}' "$PG_IMAGE_ID")"
APP_REPO_DIGESTS="$(docker image inspect --format '{{json .RepoDigests}}' "$IMAGE_ID")"

# --- the stack -----------------------------------------------------------------------------------
# One shared vCPU and the droplet's memory: both containers (and the helper
# that reads the app's process memory) on CPU 0, with the limits --droplet
# names. Defaults for everything else, as the shipped Compose file runs them.
# pg_stat_statements and auto_explain are loaded; auto_explain stays off
# (its default, log_min_duration -1) except while plans are captured. The
# threshold is deliberately NOT given on this command line: a setting from the
# command line outranks postgresql.auto.conf, so the ALTER SYSTEM that turns the
# capture on would be ignored (pilot2: no plan was ever logged).
CPUSET="${QC1_CPUSET-0}"
CPUSET_ARGS=(); [[ -z "$CPUSET" ]] || CPUSET_ARGS=(--cpuset-cpus "$CPUSET")
NET_ID="$(docker network create "qc1-$$-$RANDOM")"
VOL_TOKEN="$(cat /proc/sys/kernel/random/uuid)"
VOL="$(docker volume create --label "fold.qc1.owner=$VOL_TOKEN")"
[[ "$(docker volume inspect --format '{{index .Labels "fold.qc1.owner"}}' "$VOL")" == "$VOL_TOKEN" ]] || { echo 'qc1: volume ownership check failed' >&2; exit 1; }
DB_CID="$(docker run -d --network "$NET_ID" --network-alias qc1-db "${CPUSET_ARGS[@]}" --memory "$DB_MEMORY" -v "$VOL:/var/lib/postgresql/data" \
  -e POSTGRES_USER=church -e POSTGRES_PASSWORD=qc1-disposable -e POSTGRES_DB=fold "$PG_IMAGE_ID" \
  -c shared_preload_libraries=pg_stat_statements,auto_explain -c track_io_timing=on -c pg_stat_statements.track=all \
  -c auto_explain.log_analyze=on -c auto_explain.log_buffers=on -c auto_explain.log_wal=on \
  -c auto_explain.log_timing=on -c auto_explain.log_format=text -c auto_explain.log_nested_statements=off)"
CONTAINERS+=("$DB_CID")
for ((i=0;i<120;i++)); do docker exec "$DB_CID" pg_isready -U church -d fold >/dev/null 2>&1 && break; sleep 1; done
sleep 3 # the entrypoint restarts the server once after initdb
for ((i=0;i<120;i++)); do docker exec "$DB_CID" pg_isready -U church -d fold >/dev/null 2>&1 && break; sleep 1; done
docker exec "$DB_CID" pg_isready -U church -d fold >/dev/null
psql_db -c 'CREATE EXTENSION IF NOT EXISTS pg_stat_statements' >/dev/null
# A setting as a NEW session sees it, waited for after a reload (the reload is asynchronous).
await_setting() { # name value
  local i v=""
  for ((i=0;i<100;i++)); do
    v="$(psql_db -c "SHOW $1" 2>/dev/null || true)"
    [[ "$v" == "$2" ]] && return 0
    sleep 0.1
  done
  echo "qc1: $1 is '$v', not '$2', ten seconds after the reload" >&2
  return 1
}
await_setting auto_explain.log_min_duration -1 || exit 1   # off at the start: nothing is logged outside the capture
# Quiesce the database's own competitors: no autovacuum, no timed checkpoint or
# WAL-triggered one inside a measured operation (the harness checkpoints itself).
psql_db -c "ALTER SYSTEM SET autovacuum = off" >/dev/null
psql_db -c "ALTER SYSTEM SET checkpoint_timeout = '1d'" >/dev/null
psql_db -c "ALTER SYSTEM SET max_wal_size = '4GB'" >/dev/null
psql_db -c 'SELECT pg_reload_conf()' >/dev/null
[[ "$(psql_db -c 'SHOW autovacuum')" == off ]]
SETUP_TOKEN="$(printf 'a%.0s' {1..64})"

# (Re)create the app with FOLD_WORKERS=$1, plus the helper that shares its pid
# namespace and reads its process memory (distroless images have no shell).
start_app() { # workers
  local workers="$1" old_app="$APP_CID" old_side="$SIDE_CID" port image_ran
  # The scheduler starts with the process, so the moment before it is created bounds it from below.
  APP_STARTED_MS="$(ms)"
  if [[ -n "$old_app" ]]; then
    docker rm -fv "$old_app" "$old_side" >/dev/null
  fi
  APP_CID="$(docker run -d --network "$NET_ID" "${CPUSET_ARGS[@]}" --memory "$APP_MEMORY" -p 127.0.0.1::8080 \
    -e APP_ENV=dev -e PORT=8080 -e COOKIE_SECURE=false -e SESSION_SECRET=qc1-disposable-session-secret-0123456789abcdef \
    -e FOLD_SETUP_TOKEN="$SETUP_TOKEN" -e FOLD_WORKERS="$workers" \
    -e "DATABASE_URL=postgres://church:qc1-disposable@qc1-db:5432/fold?sslmode=disable" "$IMAGE_ID")"
  CONTAINERS+=("$APP_CID")
  image_ran="$(docker inspect --format '{{.Image}}' "$APP_CID")"
  [[ "$image_ran" == "$IMAGE_ID" ]] || { echo "qc1: the app container runs $image_ran, not $IMAGE_ID" >&2; exit 1; }
  port="$(docker port "$APP_CID" 8080/tcp | head -n 1 | sed 's/.*://')"
  URL="http://127.0.0.1:$port"
  for ((i=0;i<120;i++)); do curl -fsS "$URL/healthz" >/dev/null 2>&1 && break; sleep 1; done
  curl -fsS "$URL/healthz" >/dev/null
  SIDE_CID="$(docker run -d --pid "container:$APP_CID" "${CPUSET_ARGS[@]}" --memory 64m --user 0 --entrypoint sleep "$PG_IMAGE_ID" 86400)"
  CONTAINERS+=("$SIDE_CID")
  CURRENT_WORKERS="$workers"
  APP_CONTAINERS="$(jq -c --arg id "$APP_CID" --arg img "$image_ran" --argjson w "$workers" --argjson mem "$(docker inspect --format '{{.HostConfig.Memory}}' "$APP_CID")" \
    '. + [{id:$id, imageId:$img, workers:$w, memoryLimitBytes:$mem}]' <<<"$APP_CONTAINERS")"
}
start_app 0

# --- the application's HTTP API (the same dance restore-drill.sh uses) ---------------------------------
csrf() { curl -fsS -b "$JAR" -c "$JAR" "$URL/api/v1/auth/csrf" | jq -er .csrfToken; }
api() {
  local method="$1" path="$2" data="${3-}" token
  [[ -n "$data" ]] || data='{}'
  token="$(csrf)"
  curl -fsS -b "$JAR" -c "$JAR" -X "$method" -H "X-CSRF-Token: $token" -H 'Content-Type: application/json' --data-binary "$data" "$URL/api/v1$path"
}
# method path datafile out: prints "status milliseconds" (curl's time_total of the
# request itself; the CSRF fetch before it is not counted).
timed() {
  local method="$1" path="$2" datafile="$3" out="$4" token
  token="$(csrf)"
  curl -sS -o "$out" -w '%{http_code} %{time_total}\n' -b "$JAR" -c "$JAR" -X "$method" -H "X-CSRF-Token: $token" \
    -H 'Content-Type: application/json' --data-binary "@$datafile" "$URL/api/v1$path" \
    | awk '{ printf "%s %d\n", $1, $2 * 1000 + 0.5 }'
}
login() { rm -f "$JAR"; api POST /auth/login '{"email":"admin@example.invalid","password":"qc1-drill-password"}' >/dev/null; }
setup() {
  local token
  curl -fsS -b "$JAR" -c "$JAR" "$URL/setup" >"$WORK/setup.html"
  token="$(sed -n 's/.*name="csrf_token" value="\([^"]*\)".*/\1/p' "$WORK/setup.html" | head -n 1)"
  curl -fsS -b "$JAR" -c "$JAR" -H "X-CSRF-Token: $token" --data-urlencode "csrf_token=$token" --data-urlencode "setup_token=$SETUP_TOKEN" "$URL/setup/unlock" >/dev/null
  curl -fsS -b "$JAR" -c "$JAR" "$URL/setup" >"$WORK/setup.html"
  token="$(sed -n 's/.*name="csrf_token" value="\([^"]*\)".*/\1/p' "$WORK/setup.html" | head -n 1)"
  curl -fsS -b "$JAR" -c "$JAR" -H "X-CSRF-Token: $token" --data-urlencode "csrf_token=$token" --data-urlencode site_name='QC-1' \
    --data-urlencode timezone=UTC --data-urlencode name='QC Admin' --data-urlencode email=admin@example.invalid --data-urlencode age_band=adult \
    --data-urlencode password=qc1-drill-password --data-urlencode password_confirm=qc1-drill-password "$URL/setup" >/dev/null
  login
}
new_user() {
  api POST /admin/users "$(jq -n --arg name "$1" --arg email "$2" '{name:$name,email:$email,password:"qc1-drill-password",ageBand:"adult",role:"member"}')" | jq -er .user.id
}

# --- instruments: all through the daemon, none through host paths -------------------------------------
# App process memory: the helper shares the app's pid namespace, so the app is
# its pid 1. Peak = VmHWM since a clear_refs reset, minus the RSS before.
# One read of the helper can fail (an exec under load); three tries, the first success wins.
app_status_kb() {
  local i v
  for i in 1 2 3; do
    v="$(docker exec "$SIDE_CID" awk -v k="$1" '$1 == k ":" {print $2}' /proc/1/status 2>/dev/null)" && [[ "$v" =~ ^[0-9]+$ ]] && { printf '%s\n' "$v"; return 0; }
    sleep 0.05
  done
  return 1
}
app_reset_hwm() { docker exec "$SIDE_CID" sh -c 'echo 5 > /proc/1/clear_refs' >/dev/null 2>&1; }
# Database memory: the container's own cgroup, read inside it (cgroup v2, then v1).
db_mem_file() {
  local f
  for f in /sys/fs/cgroup/memory.current /sys/fs/cgroup/memory/memory.usage_in_bytes; do
    if docker exec "$DB_CID" sh -c "test -r $f" >/dev/null 2>&1; then printf '%s' "$f"; return 0; fi
  done
  return 1
}
# A sampler inside the container, one exec, stopped by a file (an exec client that
# is killed would leave its loop running).
db_mem_start() { # out
  docker exec "$DB_CID" sh -c "rm -f /tmp/qc1-stop; while [ ! -e /tmp/qc1-stop ]; do cat $DB_MEM_FILE; sleep 0.05; done" >"$1" 2>/dev/null &
  DBMEM_PID=$!
  SAMPLERS+=("$DBMEM_PID")
}
db_mem_stop() {
  docker exec "$DB_CID" touch /tmp/qc1-stop >/dev/null 2>&1 || true
  wait "$DBMEM_PID" 2>/dev/null || true
}
# The app's own memory every 100 ms from the helper that shares its pid namespace: "VmRSS VmHWM" per line (see qc1_app_peak).
app_mem_start() { # out
  docker exec "$SIDE_CID" sh -c "rm -f /tmp/qc1-app-stop; while [ ! -e /tmp/qc1-app-stop ]; do awk '\$1 == \"VmRSS:\" { r = \$2 } \$1 == \"VmHWM:\" { h = \$2 } END { print r, h }' /proc/1/status; sleep 0.1; done" >"$1" 2>/dev/null &
  APPMEM_PID=$!
  SAMPLERS+=("$APPMEM_PID")
}
app_mem_stop() {
  docker exec "$SIDE_CID" touch /tmp/qc1-app-stop >/dev/null 2>&1 || true
  wait "$APPMEM_PID" 2>/dev/null || true
}
# The contact gate's exclusive grant, sampled every 20 ms from a psql session
# that \watch keeps open. Lines "epoch_ms|gate_count|gate_pids|autovacuum|clients";
# cluster-wide, so the competitors in the last two fields are recorded with it.
# Stopped by terminating the session (application_name qc1-sampler).
gate_sampler_start() { # out
  printf "SELECT (extract(epoch FROM clock_timestamp()) * 1000)::bigint || '|' || (SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND classid = %s AND objid = %s AND objsubid = 1 AND mode = 'ExclusiveLock' AND granted) || '|' || coalesce((SELECT string_agg(DISTINCT pid::text, ',') FROM pg_locks WHERE locktype = 'advisory' AND classid = %s AND objid = %s AND objsubid = 1 AND mode = 'ExclusiveLock' AND granted), '') || '|' || (SELECT count(*) FROM pg_stat_activity WHERE backend_type = 'autovacuum worker') || '|' || (SELECT count(*) FROM pg_stat_activity WHERE backend_type = 'client backend' AND state <> 'idle' AND application_name NOT LIKE 'qc1-%%')\n\\\\watch 0.02\n" \
    "$GATE_CLASS" "$GATE_OBJ" "$GATE_CLASS" "$GATE_OBJ" >"$WORK/sampler.sql"
  docker exec -i -e PGAPPNAME=qc1-sampler "$DB_CID" psql -X -U church -d postgres -At -f - <"$WORK/sampler.sql" >"$1" 2>/dev/null &
  SAMPLER_PID=$!
  SAMPLERS+=("$SAMPLER_PID")
  sleep 0.3
}
gate_sampler_stop() {
  QC1_DB=postgres psql_db -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name = 'qc1-sampler'" >/dev/null || true
  kill "$SAMPLER_PID" 2>/dev/null || true
  wait "$SAMPLER_PID" 2>/dev/null || true
}
# Wait (at most 30 s) until no client backend other than the harness's own and no
# autovacuum worker is busy, so the next operation starts with its competitors gone.
quiesce() {
  local i n
  for ((i=0;i<300;i++)); do
    n="$(QC1_DB=postgres psql_db -c "SELECT count(*) FROM pg_stat_activity WHERE pid <> pg_backend_pid() AND ((backend_type = 'client backend' AND state <> 'idle' AND application_name NOT LIKE 'qc1-%') OR backend_type = 'autovacuum worker')")"
    [[ "$n" == 0 ]] && return 0
    sleep 0.1
  done
  note 'warning: the database did not quiesce within 30 s; the next row may carry a competitor'
}

# One measured operation: runs "$@" between the instruments after waiting out
# the scheduler's edges and quiescing. Sets OP_START, OP_END, WAL_BYTES,
# APP_PEAK_KB, APP_HWM_RESET, DB_PEAK_KB, DB_MEM_SAMPLES, GATE_*, COMMIT_*,
# SCHEDULER_*.
#
# What the figures are. WAL is the cluster-wide LSN difference, the COMMIT
# figures are the cluster-wide COMMIT entries of pg_stat_statements, and the gate
# figure is the span from the first to the last sample that saw the exclusive
# gate granted: all three are counters of the whole database, not attributed to
# the operation. They are attributable only when nothing else wrote: the
# workers are off, autovacuum is off, timed checkpoints are deferred, the
# harness waits for other active backends, and the app's own scheduler (which
# runs even with FOLD_WORKERS=0) is kept out of the window by waiting past its
# edges and, afterwards, by checking the window against the scheduler model and
# the jobs table. A window that still overlaps is recorded as such and judged
# invalid. The sampler's real gaps are measured and recorded; no fixed bound is
# claimed. The app's peak is reset for every operation and the reset is
# verified.
instrumented() {
  local lsn0 lsn1 rss0 hwm_reset hwm_end dbmax dbfirst wait need="${QC1_NEED_MS:-60000}"
  wait="$(qc1_scheduler_wait "$(ms)" "$need" "$APP_STARTED_MS")"
  if (( wait > 0 )); then
    note "waiting $((wait / 1000)) s so the scheduler's start-up or hourly edge stays out of the window"
    sleep "$(awk -v w="$wait" 'BEGIN { printf "%.3f", w / 1000 }')"
  fi
  quiesce
  # The audit table as the operation finds it (rows and bytes), read before the window opens: every earlier case left view and decision audits
  # behind, so a later case runs against a larger audit_logs than an earlier one. Recorded per case, with the case's seq, never corrected for.
  read -r AUDIT_ROWS0 AUDIT_BYTES0 <<<"$(psql_db -c "SELECT count(*) || ' ' || pg_total_relation_size('audit_logs') FROM audit_logs")"
  AUDIT_ACTIONS0="$(audit_actions)"
  TABLE_ROWS0="$(table_rows)"   # the rows contact_requests holds as THIS operation starts (background plus the scenario rows), never a count taken after a scenario was dropped
  psql_db -c 'SELECT pg_stat_statements_reset()' >/dev/null
  psql_db -c 'CHECKPOINT' >/dev/null
  lsn0="$(psql_db -c 'SELECT pg_current_wal_lsn()')"
  rss0="$(app_status_kb VmRSS || true)"
  APP_HWM_RESET=false hwm_reset=""
  if app_reset_hwm; then
    hwm_reset="$(app_status_kb VmHWM || true)"
    if [[ "$rss0" =~ ^[0-9]+$ && "$hwm_reset" =~ ^[0-9]+$ ]] && (( hwm_reset <= rss0 + 4096 )); then APP_HWM_RESET=true; fi
  fi
  : >"$WORK/gate.samples"; : >"$WORK/mem.db"; : >"$WORK/mem.app"
  db_mem_start "$WORK/mem.db"
  app_mem_start "$WORK/mem.app"
  gate_sampler_start "$WORK/gate.samples"
  sleep 0.2
  OP_START="$(ms)"
  "$@"
  OP_END="$(ms)"
  sleep 0.1
  gate_sampler_stop
  db_mem_stop
  app_mem_stop
  lsn1="$(psql_db -c 'SELECT pg_current_wal_lsn()')"
  WAL_BYTES="$(qc1_lsn_bytes "$lsn0" "$lsn1")" || WAL_BYTES=null
  hwm_end="$(app_status_kb VmHWM || true)"
  read -r APP_PEAK_KB APP_MEM_SAMPLES APP_BELOW_BASELINE <<<"$(qc1_app_peak "$hwm_reset" "$hwm_end" "$WORK/mem.app")"
  [[ "$APP_HWM_RESET" == true ]] || APP_PEAK_KB=null
  DB_PEAK_KB=null
  read -r dbmax dbfirst <<<"$(awk '/^[0-9]+$/ { if ($1 > m) m = $1; if (!n++) f = $1 } END { if (n) print m, f }' "$WORK/mem.db")"
  if [[ "$dbmax" =~ ^[0-9]+$ && "$dbfirst" =~ ^[0-9]+$ ]]; then DB_PEAK_KB=$(( (dbmax - dbfirst) / 1024 )); fi
  DB_MEM_SAMPLES="$(grep -c '^[0-9][0-9]*$' "$WORK/mem.db" || true)"
  read -r GATE_SPAN_MS GATE_GRANTS _ _ GATE_HOLDERS GATE_AV GATE_MC GATE_TICKS GATE_MAXGAP GATE_MEANGAP GATE_HOLES <<<"$(qc1_gate_hold "$WORK/gate.samples" "$OP_START" "$OP_END")"
  read -r COMMIT_CALLS COMMIT_TOTAL COMMIT_MAX <<<"$(psql_db -c "SELECT coalesce(sum(calls), 0) || ' ' || coalesce(round(sum(total_exec_time)), 0) || ' ' || coalesce(round(max(max_exec_time)), 0) FROM pg_stat_statements WHERE query ~* '^commit'")"
  read -r AUDIT_ROWS1 AUDIT_BYTES1 <<<"$(psql_db -c "SELECT count(*) || ' ' || pg_total_relation_size('audit_logs') FROM audit_logs")"
  AUDIT_ACTIONS1="$(audit_actions)"
  TOP_STATEMENTS="$(top_statements)"   # after the window, before the next reset: costs the operation nothing and resets nothing
  TABLE_ROWS1="$(table_rows)"
  SCHEDULER_OVERLAP="$(qc1_scheduler_overlap "$OP_START" "$OP_END" "$APP_STARTED_MS")"
  SCHEDULER_ROWS="$(psql_db -c "SELECT count(*) FROM jobs WHERE created_at >= to_timestamp($OP_START / 1000.0) - interval '1 second' AND created_at <= to_timestamp($OP_END / 1000.0) AND unique_key ~ '^[a-z_.]+:[0-9]{4}-[0-9]{2}-[0-9]{2}(T[0-9]{2})?\$'")"
}
# audit_logs rows by action, as one JSON object {action: count}: read before and after each instrumented operation, so the rows an operation wrote are
# attributed to their kind (a view audit per nonempty page, a decision audit per reason) and not only counted.
# The rows contact_requests holds right now, as a number (null when unreadable). Taken immediately BEFORE each operation, by instrumented and by the cases that are not instrumented.
table_rows() { local c; c="$(psql_db -c 'SELECT count(*) FROM contact_requests' 2>/dev/null || true)"; if [[ "$c" =~ ^[0-9]+$ ]]; then printf "%s" "$c"; else printf null; fi; }
# The statements that cost the most database time in the window just measured (qc1/top-statements.sql): cluster-wide, filtered by text, NOT attributed to the operation.
top_statements() { local t; t="$(docker exec -i "$DB_CID" psql -X -U church -d "${QC1_DB:-fold}" -At -v ON_ERROR_STOP=1 -f - <"$HERE/qc1/top-statements.sql" 2>/dev/null)" && [[ "$t" == \[* ]] && printf "%s" "$t" || printf null; }
audit_actions() { psql_db -c "SELECT coalesce(jsonb_object_agg(action, c), '{}'::jsonb)::text FROM (SELECT action, count(*) AS c FROM audit_logs GROUP BY action) s"; }
instr_json() {
  jq -nc --argjson wal "$WAL_BYTES" --argjson ctl "${CONTROL_WAL:-null}" --argjson app "$APP_PEAK_KB" --argjson reset "$APP_HWM_RESET" --argjson appSamples "${APP_MEM_SAMPLES:-0}" --argjson appBelow "${APP_BELOW_BASELINE:-false}" --argjson db "$DB_PEAK_KB" --argjson dbs "${DB_MEM_SAMPLES:-0}" \
    --argjson span "$GATE_SPAN_MS" --argjson grants "$GATE_GRANTS" --argjson ticks "$GATE_TICKS" --argjson holders "$GATE_HOLDERS" --argjson maxgap "$GATE_MAXGAP" --argjson meangap "$GATE_MEANGAP" --argjson holes "$GATE_HOLES" \
    --argjson av "$GATE_AV" --argjson mc "$GATE_MC" --argjson cc "$COMMIT_CALLS" --argjson cmax "$COMMIT_MAX" --argjson ctot "$COMMIT_TOTAL" \
    --argjson sched "$SCHEDULER_OVERLAP" --argjson srows "$SCHEDULER_ROWS" \
    --argjson top "${TOP_STATEMENTS:-null}" --argjson tr0 "${TABLE_ROWS0:-null}" --argjson tr1 "${TABLE_ROWS1:-null}" --argjson aa0 "${AUDIT_ACTIONS0:-null}" --argjson aa1 "${AUDIT_ACTIONS1:-null}" --argjson ar0 "${AUDIT_ROWS0:-null}" --argjson ar1 "${AUDIT_ROWS1:-null}" --argjson ab0 "${AUDIT_BYTES0:-null}" --argjson ab1 "${AUDIT_BYTES1:-null}" \
    '{walBytes:$wal, walControlBytes:$ctl, appPeakKb:$app, appHwmReset:$reset, appMemSamples:$appSamples, appHwmBelowBaseline:$appBelow, dbPeakKb:$db, dbMemSamples:$dbs,
      gateSpanMs:$span, gateGrantTicks:$grants, gateTicks:$ticks, gateHolders:$holders, gateMaxGapMs:$maxgap, gateMeanGapMs:$meangap, gateHoles:$holes,
      autovacuumWorkers:$av, maxActiveClients:$mc, commitCalls:$cc, commitMsMax:$cmax, commitMsTotal:$ctot,
      schedulerOverlap:$sched, schedulerRows:$srows,
      auditRowsBefore:$ar0, auditRowsAfter:$ar1, auditBytesBefore:$ab0, auditBytesAfter:$ab1, tableRowsBefore:$tr0, tableRowsAfter:$tr1,
      topStatements:$top, topStatementsNote:"pg_stat_statements top 8 by total time over the window, cluster-wide and filtered by text: not attributed to this operation; read after the window, nothing reset", auditActionsBefore:$aa0, auditActionsAfter:$aa1,
      auditActionsDelta:(if ($aa0|type) == "object" and ($aa1|type) == "object" then ([$aa0, $aa1] | (.[0] | keys) + (.[1] | keys) | unique | map(. as $k | {key:$k, value:(($aa1[$k] // 0) - ($aa0[$k] // 0))}) | from_entries) else null end),
      attribution:"WAL, commit and gate figures are cluster-wide counters, not attributed to this operation; they are attributable only if no invalid finding names a competitor"}'
}

# Prove every instrument reads a known quantity before anything is measured; the
# result is part of the evidence (environment.json), and a failed instrument
# makes the run incomplete instead of leaving nulls to be noticed later.
calibrate() {
  local t0 t1 hold samples holders ticks maxgap meangap holes memsamples=0 gate_ok=false mem_ok=false hwm_ok=false wal_ok=false commit_ok=false lsn rss hwm0 hwm1 reset=false calls cur
  : >"$WORK/cal.gate"
  gate_sampler_start "$WORK/cal.gate"
  t0="$(ms)"
  psql_db -c "BEGIN; SELECT pg_advisory_xact_lock($((QC1_GATE_LOCK_ID))); SELECT pg_sleep(0.4); COMMIT;" >/dev/null
  t1="$(ms)"
  sleep 0.1
  gate_sampler_stop
  read -r hold samples _ _ holders _ _ ticks maxgap meangap holes <<<"$(qc1_gate_hold "$WORK/cal.gate" "$t0" "$t1")"
  # A continuous 400 ms hold by one backend: a span of about that, no sample missing it inside, one holder.
  if (( hold >= 300 && hold <= 800 && holders == 1 && holes == 0 && maxgap < 200 )); then gate_ok=true; fi
  DB_MEM_FILE="$(db_mem_file || true)"
  if [[ -n "$DB_MEM_FILE" ]]; then
    cur="$(docker exec "$DB_CID" cat "$DB_MEM_FILE" 2>/dev/null || true)"
    [[ "$cur" =~ ^[0-9]+$ ]] && mem_ok=true
    : >"$WORK/cal.mem"; db_mem_start "$WORK/cal.mem"; sleep 0.5; db_mem_stop
    memsamples="$(grep -c '^[0-9][0-9]*$' "$WORK/cal.mem" || true)"
  fi
  rss="$(app_status_kb VmRSS || true)"
  hwm0="$(app_status_kb VmHWM || true)"
  if [[ "$rss" =~ ^[0-9]+$ && "$hwm0" =~ ^[0-9]+$ ]]; then
    hwm_ok=true
    if app_reset_hwm; then hwm1="$(app_status_kb VmHWM || true)"; [[ "$hwm1" =~ ^[0-9]+$ ]] && (( hwm1 <= hwm0 )) && reset=true; fi
  fi
  lsn="$(psql_db -c 'SELECT pg_current_wal_lsn()')"
  [[ "$lsn" =~ ^[0-9A-F]+/[0-9A-F]+$ ]] && wal_ok=true
  psql_db -c 'SELECT pg_stat_statements_reset()' >/dev/null
  psql_db -c 'BEGIN; SELECT 1; COMMIT;' >/dev/null
  calls="$(psql_db -c "SELECT coalesce(sum(calls), 0) FROM pg_stat_statements WHERE query ~* '^commit'")"
  [[ "$calls" =~ ^[0-9]+$ ]] && (( calls >= 1 )) && commit_ok=true
  jq -nc --argjson gate "$gate_ok" --argjson hold "${hold:-0}" --argjson holders "${holders:-0}" --argjson grants "${samples:-0}" --argjson ticks "${ticks:-0}" --argjson maxgap "${maxgap:-0}" --argjson meangap "${meangap:-0}" --argjson holes "${holes:-0}" --argjson memsamples "$memsamples" \
    --argjson mem "$mem_ok" --arg memFile "${DB_MEM_FILE:-}" --argjson hwm "$hwm_ok" --argjson reset "$reset" --argjson wal "$wal_ok" --argjson commit "$commit_ok" \
    '{gate:{ok:$gate, spanMs:$hold, holders:$holders, grants:$grants, ticks:$ticks, maxGapMs:$maxgap, meanGapMs:$meangap, holes:$holes, expected:"one backend holding the gate continuously for 400 ms"},
      dbMemory:{ok:$mem, samples:$memsamples, source:("container cgroup file " + $memFile + ", sampled through docker exec; samples is the count in half a second")},
      appMemory:{ok:$hwm, hwmReset:$reset, source:"/proc/1/status VmHWM of the app, read from a helper sharing its pid namespace"},
      wal:{ok:$wal, source:"pg_current_wal_lsn difference (cluster-wide)"},
      commit:{ok:$commit, source:"pg_stat_statements COMMIT entries (cluster-wide)"}}' >"$WORK/calibration.json"
}

# Page traversal as the client does it: units from a null cursor until next is
# null, retrying nothing. The wall clock covers the whole loop (what a person
# waits); the requests' own times are summed beside it and the rest is client
# work (CSRF fetches, JSON handling), see qc1_traversal_times. Sets TR_* and
# writes the items to $2.
traverse() { # target items-out
  local target="$1" out="$2" after=null body status ms_ n=0 slow=0 total=0 started ended times
  started="$(ms)"
  TR_STATUS=200 TR_UNITS=0 TR_ITEMS=0
  : >"$WORK/items.jsonl"
  while :; do
    body="$(jq -nc --argjson a "$after" '{after:$a}')"
    printf '%s' "$body" >"$WORK/page.body"
    read -r status ms_ <<<"$(timed POST "/admin/users/$target/erasure-reasons/page" "$WORK/page.body" "$WORK/page.out")"
    n=$((n+1)); total=$((total+ms_))
    if (( ms_ > slow )); then slow=$ms_; fi
    if [[ "$status" != 200 ]]; then TR_STATUS=$status; break; fi
    jq -c '.items[]' "$WORK/page.out" >>"$WORK/items.jsonl"
    after="$(jq -c '.next' "$WORK/page.out")"
    [[ "$after" != null ]] || break
    (( n < 100000 )) || { TR_STATUS=loop; break; }
  done
  jq -sc . "$WORK/items.jsonl" >"$out"
  TR_ITEMS="$(jq 'length' "$out")"
  ended="$(ms)"
  TR_UNITS=$n TR_SLOW_MS=$slow
  if times="$(qc1_traversal_times "$((ended-started))" "$total" "$n")"; then
    read -r TR_TOTAL_MS TR_REQUEST_MS TR_CLIENT_MS <<<"$times"
  else
    # An impossible split is recorded as such (clientMs -1) and the judge calls it invalid.
    TR_TOTAL_MS=$((ended-started)) TR_REQUEST_MS=$total TR_CLIENT_MS=-1
  fi
}
# How many of an account's listed reasons are of each role: a case named requester or subject must list ONLY that role (the judge holds the label to it).
roles_json() { # items-file
  jq -c '{requester: ([.[] | select(.role == "requester")] | length), subject: ([.[] | select(.role == "subject")] | length)}' "$1"
}
traversal_json() {
  jq -nc --argjson u "$TR_UNITS" --argjson s "$TR_SLOW_MS" --argjson t "$TR_TOTAL_MS" --argjson req "$TR_REQUEST_MS" --argjson client "$TR_CLIENT_MS" --argjson i "$TR_ITEMS" --arg st "$TR_STATUS" \
    --argjson instr "$(instr_json)" \
    '{units:$u, slowestUnitMs:$s, totalMs:$t, requestMs:$req, clientMs:$client, items:$i, lastStatus:$st,
      clock:"totalMs is the wall clock around the whole loop; requestMs sums the page requests; clientMs is the rest"} + $instr'
}
delete_json() {
  jq -nc --arg st "$DEL_STATUS" --argjson ms "$DEL_MS" --argjson kept "${DEL_KEPT:-null}" --argjson removed "${DEL_REMOVED:-null}" --argjson instr "$(instr_json)" \
    '{status:$st, ms:$ms, reasonsKept:$kept, reasonsRemoved:$removed, commitMs:$instr.commitMsMax} + $instr'
}
do_delete() { # target bodyfile
  read -r DEL_STATUS DEL_MS <<<"$(timed DELETE "/admin/users/$1" "$2" "$WORK/delete.out")"
}
ctl_request() { api GET /auth/me >/dev/null; }
# record item case size extra-json   (RECORD_STATUS: measured, observation or failed)
# Every row carries seq, the order the cases ran in (a later case runs against the audit rows and the dirtied table the earlier ones left).
CASE_SEQ=0
record() {
  CASE_SEQ=$((CASE_SEQ + 1))
  # No row count is taken here: record runs after the operation and often after its scenario was dropped. Each operation carries the rows
  # contact_requests held just before and after it (tableRowsBefore/After in its figures); a case with several operations keeps one per operation.
  jq -nc --arg status "${RECORD_STATUS:-measured}" --argjson item "$1" --arg case "$2" --argjson size "$3" --argjson extra "$4" --arg layout "$LAYOUT" \
    --argjson reasonLen "$REASON_LEN" --argjson targetLen "$TARGET_REASON_LEN" --argjson workers "$CURRENT_WORKERS" --argjson seq "$CASE_SEQ" \
    '{status:$status, item:$item, case:$case, size:$size, seq:$seq, layout:$layout, reasonLen:$reasonLen, targetReasonLen:$targetLen, workers:$workers,
      table:{backgroundRows:$size, note:"size is the BACKGROUND row count; the rows the table really held (background plus the scenario rows, n + k) are tableRowsBefore/After in each operation'"'"'s figures (and per phase for the browser case)"}} + $extra' >>"$MEASUREMENTS"
}

load_filler() { # n reasons
  docker exec -i "$DB_CID" psql -X -U church -d fold -v ON_ERROR_STOP=1 -v n="$1" -v reason_len="$REASON_LEN" -v reasons="$2" -f - <"$HERE/qc1/filler.sql" >/dev/null
  psql_db -c 'ANALYZE contact_requests' >/dev/null
  psql_db -c 'CHECKPOINT' >/dev/null
}
load_scenario() { # n k target direction counterparts [layout]
  docker exec -i "$DB_CID" psql -X -U church -d fold -v ON_ERROR_STOP=1 -v n="$1" -v k="$2" -v target="$3" -v direction="$4" -v layout="${6:-$LAYOUT}" \
    -v counterparts="$5" -v group_id="$GROUP" -v reason_len="$TARGET_REASON_LEN" -f - <"$HERE/qc1/scenario.sql" >/dev/null
  psql_db -c 'ANALYZE contact_requests' >/dev/null
}
drop_scenarios() { psql_db -c 'SET session_replication_role = replica' -c 'DELETE FROM contact_requests WHERE id % 2 = 1' >/dev/null; }
fresh_user() { local stamp; stamp="$(date +%s%N)"; new_user "QC subject $stamp" "qc1-subject-$stamp@example.invalid"; }

# --- fixtures and calibration ---------------------------------------------------------------------------------
note "setting up the wizard, an admin, counterparts and a group"
setup
GROUP="$(api POST /groups '{"name":"QC-1 context","description":"Disposable fixture","visibility":"public","tags":[]}' | jq -er .group.id)"
CPS=""
for i in 1 2 3 4 5 6 7 8 9 10; do CPS+="$(new_user "QC counterpart $i" "qc1-counterpart-$i@example.invalid"),"; done
CPS="${CPS%,}"
note "calibrating the instruments"
calibrate
jq -c . "$WORK/calibration.json"
FIRST_APP_CID="$APP_CID"

psql_stdin() { docker exec -i "$DB_CID" psql -X -U church -d fold -At -v ON_ERROR_STOP=1 -f -; }
# An account made in this shell (fresh_user runs in a subshell): sets USER_ID and USER_EMAIL.
make_user() { local stamp; stamp="$(date +%s%N)"; USER_EMAIL="qc1-subject-$stamp@example.invalid"; USER_ID="$(new_user "QC subject $stamp" "$USER_EMAIL")"; }
# The 80-hex attempt id a press mints first, for an account.
mint_attempt() { # target
  local id
  id="$(api POST /admin/operation-attempts "$(jq -nc --argjson s "$1" '{kind:"account_delete",subjectUserId:$s}')" | jq -er .attemptId)" || return 1
  [[ "$id" =~ ^[0-9a-f]{80}$ ]] || { echo "qc1: the minted attempt id is not 80 hex characters" >&2; return 1; }
  printf '%s' "$id"
}
# What a deletion left in the database, against what it was asked to do. The
# request ids are the items the account's panel listed. Sets OUTCOME_JSON.
verify_outcome() { # target items-file kept removed audit-mark
  local t="$1" file="$2" kept="$3" removed="$4" mark="$5" ids res deleted blank nonblank audits ok=false
  ids="$(jq -r 'map(.requestId) | join(",")' "$file")"
  [[ -n "$ids" ]] || ids="-1"
  # The audit count is bound to THIS deletion by the watermark taken just before it (qc1_audit_count_sql): scenario request ids are reused,
  # and audit_logs keeps every earlier scenario's rows under the same target ids.
  res="$(qc1_outcome_sql "$t" "$ids" "$mark" | psql_stdin)" || res="error|0|0|0"
  IFS='|' read -r deleted blank nonblank audits <<<"$res"
  if qc1_outcome_ok "$deleted" "$blank" "$nonblank" "$audits" "$kept" "$removed"; then ok=true; fi
  OUTCOME_JSON="$(jq -nc --argjson ok "$ok" --arg deleted "$deleted" --arg blank "${blank:-0}" --arg nonblank "${nonblank:-0}" --arg audits "${audits:-0}" \
    '{dbOk:$ok, accountDeleted:$deleted, reasonsBlank:($blank|tonumber), reasonsWithText:($nonblank|tonumber), decisionAudits:($audits|tonumber)}')"
}
# One full deletion as the panel sends it: mint an id, send the envelope with the
# account's own items and the chosen mode, with the instruments around it, and check
# the answer and the database against the split the mode implies. Sets
# DELETE_JSON, EXPECT_JSON, OUTCOME_JSON and BODY_BYTES.
delete_with() { # target mode items-file
  local t="$1" mode="$2" file="$3" k id kept removed audit_mark
  k="$(jq 'length' "$file")"
  read -r kept removed <<<"$(qc1_expected_split "$mode" "$k")"
  id="$(mint_attempt "$t")"
  qc1_delete_body_n "$mode" "$file" "$k" "$id" >"$WORK/delete.body"
  BODY_BYTES="$(wc -c <"$WORK/delete.body")"
  # The watermark: only audits written after this point belong to this deletion.
  audit_mark="$(psql_db -c 'SELECT coalesce(max(id), 0) FROM audit_logs')"
  [[ "$audit_mark" =~ ^[0-9]+$ ]] || { echo "qc1: cannot read the audit watermark ('$audit_mark')" >&2; exit 1; }
  instrumented do_delete "$t" "$WORK/delete.body"
  DEL_KEPT="$(jq '.erasure.reasonsKept // null' "$WORK/delete.out" 2>/dev/null || echo null)"
  DEL_REMOVED="$(jq '.erasure.reasonsRemoved // null' "$WORK/delete.out" 2>/dev/null || echo null)"
  DELETE_JSON="$(delete_json)"
  verify_outcome "$t" "$file" "$kept" "$removed" "$audit_mark"
  EXPECT_JSON="$(jq -nc --argjson i "$k" --argjson kp "$kept" --argjson rm "$removed" '{items:$i, kept:$kp, removed:$rm}')"
  unset DEL_KEPT DEL_REMOVED
}

# (5) request size, for the compact set-v1 envelope. A body is {successors, hostSuccessors, attemptId, reasonReview:{format, count, digest, choices}}:
# its size is set by the vector (two bits a reason, base64: a third of a byte), not by one entry per reason, so the old 52,760-entry limit is gone by
# construction. What is measured, truthfully:
#   - the REAL bodies the matrix reaches: built from the entries the server issued for every discovery case of this size (not sent here; the
#     deletion cases send them) with the 80-hex attempt id a press mints, and the exact byte length of each;
#   - the largest valid body SENT: the account with the most reasons this table holds (the largest deletion or dense count), through the whole
#     path, plus its refusals, each leaving the account as it was: a vector with no valid choice (400), a count one too high (400), a set that does
#     not match the server's (409 reason_review_changed; this one costs the streamed read and compare the real delete costs, rolled back), each with its
#     time and the app's peak memory;
#   - the capacity: the most reasons whose body fits maxRequestBytes, computed exactly from the format. A body of that size is not sent: it would take
#     that many reasons. A syntactically valid body ONE REASON OVER it is sent (413 before any row is read, account unchanged);
#   - the OLD items envelope as a comparison only: its capacity at the id width actually seen (the pilots' 52,760 / 52,761), a fitting synthetic old
#     body (409 reason_review_outdated on a nonempty set, nothing applied) and one entry over (413). The entries are synthetic and labelled so.
# The capacity is FINITE and is reported, never presented as a limit the design set or as no limit: 18,873,540 reasons at the default cap with EMPTY
# successor and host-successor maps, fewer when the maps hold entries (populatedCapacity gives labelled examples, not bounds). A valid history whose body
# would exceed maxRequestBytes is a budget finding (qc1_judge, "body"), never hidden and never turned into a ceiling.
# The legacy PUT half stays T07B-3a's.
refusal_case() { # kind target items-file k   (sets REFUSAL_JSON)
  local kind="$1" t="$2" file="$3" k="$4" id bytes unchanged=false code
  id="$(mint_attempt "$t")"
  qc1_delete_body_n mixed "$file" "$k" "$id" >"$WORK/refusal.base"
  case "$kind" in
    parse) qc1_final_choice_body "$WORK/refusal.base" maybe >"$WORK/delete.body" ;;
    count) qc1_extra_count_body "$WORK/refusal.base" >"$WORK/delete.body" ;;
    changed) qc1_changed_set_body "$WORK/refusal.base" >"$WORK/delete.body" ;;
    *) echo "qc1: unknown refusal $kind" >&2; return 1 ;;
  esac
  bytes="$(wc -c <"$WORK/delete.body")"
  instrumented do_delete "$t" "$WORK/delete.body"
  code="$(jq -r '.code // ""' "$WORK/delete.out" 2>/dev/null || true)"
  if [[ "$(account_untouched "$t")" == t ]]; then unchanged=true; fi
  REFUSAL_JSON="$(jq -nc --arg status "$DEL_STATUS" --argjson ms "$DEL_MS" --argjson app "$APP_PEAK_KB" --argjson bytes "$bytes" --argjson unchanged "$unchanged" --arg code "$code" \
    --argjson rows0 "${TABLE_ROWS0:-null}" --argjson rows1 "${TABLE_ROWS1:-null}" \
    '{status:$status, ms:$ms, appPeakKb:$app, bodyBytes:$bytes, unchanged:$unchanged, code:$code, tableRowsBefore:$rows0, tableRowsAfter:$rows1}')"
}
# The account is as it was: not deleted, and none of its reasons blanked.
account_untouched() { psql_db -c "SELECT (SELECT deleted_at IS NULL FROM users WHERE id = $1) AND (SELECT count(*) FROM contact_requests WHERE reason = '' AND (requester_id = $1 OR subject_user_id = $1)) = 0"; }
measure_request_size() { # n
  local n="$1" cap="$MAX_REQUEST_BYTES" id80 K k d f t id cap_n cap_at cap_next populated fixed bodies="[]" bytes probe_tr fit_bytes want_bytes
  local over_rows fit_rows over_old_rows parse_json count_json changed_json over_bytes over_status over_ms over_code over_unchanged=false digits old_n old_at old_next legacy_json
  local fit_status fit_code fit_unchanged=false over_old_status old_over_bytes over_old_unchanged=false fit_delete fit_outcome fit_expect
  id80="$(printf 'a%.0s' {1..80})"
  K=0
  for k in "${ALL_DELETE_LIST[@]}"; do if (( k <= n && k > K )); then K=$k; fi; done
  (( K > 0 )) || { echo "qc1: no deletion count fits a table of $n rows" >&2; exit 1; }
  read -r cap_n cap_at cap_next <<<"$(qc1_body_capacity "$cap" "$id80")"
  # Populated envelopes shrink the capacity: examples with N sole-led groups and N sole-hosted events (ids and targets of the widths real rows have).
  populated="$(for m in 20 200 2000; do
    s_map="$(jq -nc --argjson m "$m" '[range(0; $m) | {key: ((100000 + .) | tostring), value: (200000 + .)}] | from_entries')"
    h_map="$(jq -nc --argjson m "$m" '[range(0; $m) | {key: ((300000 + .) | tostring), value: (400000 + .)}] | from_entries')"
    read -r pn pat pnext <<<"$(qc1_body_capacity "$cap" "$id80" "$s_map" "$h_map")"
    jq -nc --argjson m "$m" --argjson n "$pn" --argjson at "$pat" --argjson next "$pnext" '{groupsAndEvents:$m, reasons:$n, bytesAt:$at, bytesNext:$next}'
  done | jq -sc '{examples:., note:"capacity with N sole-led groups and N sole-hosted events in the envelope (examples of how the maps reduce it, not bounds); computed from the format, never sent"}')"
  fixed="$(qc1_body_length 0 "$id80")"
  # The real bodies the matrix reaches at this size, built from the entries the server issued (and kept by the discovery cases).
  for d in "${QC1_DIRECTIONS[@]}"; do
    for k in "${ALL_DELETE_LIST[@]}"; do
      f="$WORK/items-$d-$k.json"
      (( k <= n )) && [[ -f "$f" ]] || continue
      bytes="$(qc1_delete_body_n mixed "$f" "$k" "$id80" | wc -c)"
      bodies="$(jq -c --arg d "$d" --argjson k "$k" --argjson b "$bytes" '. + [{direction:$d, reasons:$k, bodyBytes:$b, perReasonBytes:(if $k == 0 then null else ((($b - '"$fixed"') / $k) * 1000 | round / 1000) end), perReasonNote:(if $k == 0 then "not applicable: no reasons, so no reasonReview and no per-reason size" else null end)}]' <<<"$bodies")"
    done
  done

  # The largest account: discovery, then the refusals, then the real delete.
  drop_scenarios
  t="$(fresh_user)"
  load_scenario "$n" "$K" "$t" both "$CPS" tail
  QC1_NEED_MS=900000 instrumented traverse "$t" "$WORK/req-items.json"
  probe_tr="$(traversal_json)"
  refusal_case parse "$t" "$WORK/req-items.json" "$K"; parse_json="$REFUSAL_JSON"
  refusal_case count "$t" "$WORK/req-items.json" "$K"; count_json="$REFUSAL_JSON"
  refusal_case changed "$t" "$WORK/req-items.json" "$K"; changed_json="$REFUSAL_JSON"
  id="$(mint_attempt "$t")"
  qc1_oversize_body "$cap" "$id" >"$WORK/delete.body"
  over_bytes="$(wc -c <"$WORK/delete.body")"
  (( over_bytes > cap )) || { echo 'qc1: the over-cap body is not over the cap' >&2; exit 1; }
  over_rows="$(table_rows)"   # the rows the table holds as THIS request is sent
  do_delete "$t" "$WORK/delete.body"
  over_status="$DEL_STATUS"; over_ms="$DEL_MS"; over_code="$(jq -r '.code // ""' "$WORK/delete.out" 2>/dev/null || true)"
  if [[ "$(account_untouched "$t")" == t ]]; then over_unchanged=true; fi
  delete_with "$t" mixed "$WORK/req-items.json"
  fit_delete="$DELETE_JSON"; fit_outcome="$OUTCOME_JSON"; fit_expect="$EXPECT_JSON"; fit_bytes="$BODY_BYTES"
  want_bytes="$(qc1_body_length "$K" "$id80")"
  (( fit_bytes <= cap && fit_bytes == want_bytes )) || { echo "qc1: the sent body is $fit_bytes bytes; the format says $want_bytes for $K reasons (cap $cap)" >&2; exit 1; }

  # The old items envelope, for the comparison only.
  drop_scenarios
  t="$(fresh_user)"
  load_scenario "$n" 10 "$t" both "$CPS" tail
  digits="$(jq -r '[.[].requestId | length] | max' "$WORK/req-items.json")"
  read -r old_n old_at old_next <<<"$(qc1_legacy_capacity "$cap" "$digits" "$id80")"
  id="$(mint_attempt "$t")"
  qc1_legacy_body "$old_n" "$digits" "$id" >"$WORK/delete.body"
  fit_rows="$(table_rows)"
  do_delete "$t" "$WORK/delete.body"
  fit_status="$DEL_STATUS"; fit_code="$(jq -r '.code // ""' "$WORK/delete.out" 2>/dev/null || true)"
  if [[ "$(account_untouched "$t")" == t ]]; then fit_unchanged=true; fi
  id="$(mint_attempt "$t")"
  qc1_legacy_body "$((old_n + 1))" "$digits" "$id" >"$WORK/delete.body"
  old_over_bytes="$(wc -c <"$WORK/delete.body")"
  over_old_rows="$(table_rows)"
  do_delete "$t" "$WORK/delete.body"
  over_old_status="$DEL_STATUS"
  if [[ "$(account_untouched "$t")" == t ]]; then over_old_unchanged=true; fi
  legacy_json="$(jq -nc --argjson digits "$digits" --argjson n "$old_n" --argjson at "$old_at" --argjson next "$old_next" --arg fitStatus "$fit_status" --arg fitCode "$fit_code" \
    --argjson fitRows "$fit_rows" --argjson overRows "$over_old_rows" --argjson fitUnchanged "$fit_unchanged" --argjson over "$((old_n + 1))" --argjson overBytes "$old_over_bytes" --arg overStatus "$over_old_status" --argjson overUnchanged "$over_old_unchanged" \
    '{synthetic:true, idDigits:$digits, capacityEntries:$n, fitBytes:$at, nextBytes:$next, fitStatus:$fitStatus, fitCode:$fitCode, fitUnchanged:$fitUnchanged,
      entriesOver:$over, overBytes:$overBytes, overStatus:$overStatus, overUnchanged:$overUnchanged, tableRowsBefore:{fit:$fitRows, over:$overRows},
      note:"the OLD items envelope, measured as a comparison only: synthetic entries of the stated id width. The pilots found 52,760 entries within maxRequestBytes and 52,761 not (413); the old form has a limit that is a function of the entry count; the set-v1 capacity is reported under request.capacity and request.populatedCapacity"}')"

  record 5 request-size "$n" "$(jq -nc --argjson cap "$cap" --argjson capN "$cap_n" --argjson capAt "$cap_at" --argjson capNext "$cap_next" --argjson populated "$populated" --argjson K "$K" --argjson fitBytes "$fit_bytes" \
    --argjson fixed "$fixed" --argjson bodies "$bodies" --argjson parse "$parse_json" --argjson count "$count_json" --argjson changed "$changed_json" --argjson overCapRows "$over_rows" --argjson overBytes "$over_bytes" \
    --arg overStatus "$over_status" --argjson overMs "$over_ms" --arg overCode "$over_code" --argjson overUnchanged "$over_unchanged" --argjson legacy "$legacy_json" \
    --argjson fit "$fit_delete" --argjson fitExpect "$fit_expect" --argjson fitOutcome "$fit_outcome" --argjson probeTr "$probe_tr" \
    '{request:{format:"set-v1", maxRequestBytes:$cap,
               envelope:{keys:["successors","hostSuccessors","attemptId","reasonReview"], attemptIdLength:80, successors:"{}", hostSuccessors:"{}", fixedBytesWithEmptyReview:$fixed,
                         reasonReview:{keys:["format","count","digest","choices"], digestHexLength:64, choicesBitsPerReason:2, encoding:"standard base64 with padding"}},
               populatedCapacity:$populated,
               capacity:{reasons:$capN, bytesAt:$capAt, bytesNext:$capNext, note:"the most reasons whose body fits maxRequestBytes, computed exactly from the format; a body of that size is not sent (it would take that many reasons)"},
               bodies:$bodies, largest:{reasons:$K, bodyBytes:$fitBytes, sent:true}, headroomBytes:($cap - $fitBytes),
               parseRefusal:$parse, countMismatch:$count, changedSet:$changed,
               overCap:{tableRowsBefore:$overCapRows, reasons:($capN + 1), bodyBytes:$overBytes, status:$overStatus, ms:$overMs, code:$overCode, unchanged:$overUnchanged, note:"a syntactically valid set-v1 body one reason over the capacity, with a synthetic digest: refused (413) before any row is read"},
               legacyComparison:$legacy, fitExpect:$fitExpect, fitOutcome:$fitOutcome,
               source:"bodies are built from the ids and hashes the server issued for the discovery cases of this size; the sent body is the largest of them, in the panel envelope with a minted 80-hex attempt id",
               refusals:"the real largest body with only the last choice code replaced by 3 (400), with the count one higher (400), and with one digest digit changed (409 reason_review_changed, the streamed read and compare, rolled back); each leaves the account as it was"},
      delete:$fit, traversal:$probeTr}')"
  drop_scenarios
}
# The settings the planner (and the JIT decision) read, as the probe's session sees them: what the estimates and the plan shapes depend on. One JSON object, name -> setting.
planner_settings() {
  psql_db -q -c "SELECT json_object_agg(name, setting)::text FROM pg_settings WHERE name IN ('jit', 'jit_above_cost', 'jit_inline_above_cost', 'jit_optimize_above_cost', 'random_page_cost', 'seq_page_cost', 'cpu_tuple_cost', 'cpu_index_tuple_cost', 'cpu_operator_cost', 'effective_cache_size', 'work_mem', 'shared_buffers', 'default_statistics_target', 'enable_seqscan', 'enable_bitmapscan', 'enable_sort', 'deadlock_timeout', 'log_lock_waits')"
}

# The scanner probe (see qc1_scanner_probe_parse): the REAL scanner statements out of the Go source, prepared once and executed eight times per mode in one
# session (one initial statement and seven continuations at controlled cursor positions, taken as table offsets 0, 10, 25, 40, 55, 70 and 85 per cent), in the
# application's plan_cache_mode (auto: the sixth and later executions of a prepared statement may switch to a generic plan) and forced generic. Read-only.
# Production reads a scanner unit in batches (internal/reasonscan: reasonScanBatchFirst, doubling, clipped to reasonUnitRows), so the LIMITs are the ones production sends:
# the FIRST batch, which almost every page is, and the LARGEST batch of the schedule (both read out of the Go source by qc1_scan_schedule, not typed here). Each (mode, LIMIT)
# is its own eight executions; every execution says which LIMIT it ran. The probe does not run one 50,000-row statement, because production does not send one.
# Run while the scenario rows are still in the table. `schema` is where contact_requests is found first: "public" for the table exactly as the matrix left it
# ("bloated": autovacuum is off and every scenario was loaded and dropped), "qc1_clean" for a VACUUM (ANALYZE)d copy of the same rows. Each execution says which
# schema its scan read (EXPLAIN VERBOSE), so a probe cannot be taken for the other. Sets SCANNER_PROBE_JSON.
scanner_probe() { # schema
  local schema="$1" unit="$QC1_SCANNER_UNIT_ROWS" total pct off id mode meta script out parsed all="[]" sql_init sql_cont limit
  local -a pcts=(0 10 25 40 55 70 85) afters=() sched=() limits=()
  sql_init="$(qc1_go_sql "$ROOT" reasonScanInitialSQL)"; sql_cont="$(qc1_go_sql "$ROOT" reasonScanContinuationSQL)"
  [[ -n "$sql_init" && -n "$sql_cont" ]] || { echo 'qc1: the scanner statements are not in account_erasure_reasons.go' >&2; exit 1; }
  mapfile -t sched < <(qc1_scan_schedule "$ROOT")
  (( ${#sched[@]} >= 1 )) || { echo 'qc1: the scanner batch schedule is not readable from the Go source' >&2; exit 1; }
  limits=("${sched[0]}"); if (( ${#sched[@]} > 1 )); then limits+=("${sched[${#sched[@]}-1]}"); fi
  total="$(psql_db -q -c "SET search_path = $schema, public" -c 'SELECT count(*) FROM contact_requests')"
  for pct in "${pcts[@]}"; do
    off=$(( total * pct / 100 ))
    id="$(psql_db -q -c "SET search_path = $schema, public" -c "SELECT id FROM contact_requests ORDER BY id OFFSET $off LIMIT 1")"
    [[ "$id" =~ ^[0-9]+$ ]] || id=0
    afters+=("$id")
  done
  for limit in "${limits[@]}"; do
    for mode in auto force_generic_plan; do
      meta="$(jq -nc --arg mode "$mode" --argjson limit "$limit" --argjson pcts "$(printf '%s\n' "${pcts[@]}" | jq -sc 'map(tonumber)')" --argjson ids "$(printf '%s\n' "${afters[@]}" | jq -sc 'map(tonumber)')" \
        '[{mode:$mode, kind:"initial", percent:null, afterId:null, limit:$limit}] + [range(0; $pcts | length) | {mode:$mode, kind:"continuation", percent:$pcts[.], afterId:$ids[.], limit:$limit}]')"
      script="SET search_path = $schema, public;
SET plan_cache_mode = $mode;
PREPARE qi (bigint) AS $sql_init;
PREPARE qc (bigint, bigint) AS $sql_cont;
EXPLAIN (ANALYZE, BUFFERS, VERBOSE, FORMAT JSON) EXECUTE qi($limit);"
      for id in "${afters[@]}"; do script+=$'\n'"EXPLAIN (ANALYZE, BUFFERS, VERBOSE, FORMAT JSON) EXECUTE qc($id, $limit);"; done
      out="$(psql_db -q -f - <<<"$script")" || { echo "qc1: the scanner probe failed in mode $mode at LIMIT $limit" >&2; exit 1; }
      parsed="$(qc1_scanner_probe_parse "$meta" <<<"$out")"
      all="$(jq -c --argjson more "$parsed" '. + $more' <<<"$all")"
    done
  done
  planner="$(planner_settings)"
  SCANNER_PROBE_JSON="$(jq -nc --argjson unit "$unit" --argjson total "$total" --arg schema "$schema" --argjson ex "$all" --argjson planner "$planner" \
    --argjson limits "$(printf '%s\n' "${limits[@]}" | jq -sc 'map(tonumber)')" --argjson schedule "$(printf '%s\n' "${sched[@]}" | jq -sc 'map(tonumber)')" \
    '{unit:$unit, limits:$limits, schedule:$schedule, tableRows:$total, schema:$schema, sameSession:true, modes:["auto","force_generic_plan"], executionsPerMode:8, executions:$ex, plannerSettings:$planner, estimates:true,
      note:"the real scanner statements at the LIMITs production sends (the first batch and the largest batch of the schedule read from the Go source), prepared once per mode and LIMIT and executed eight times in one session at table offsets 0, 10, 25, 40, 55, 70 and 85 per cent, each under EXPLAIN (ANALYZE, BUFFERS, VERBOSE); child scan node, the index condition on it or on the Bitmap Index Scan beneath it, filter, rows, loops and shared blocks as the plan reports them; estimates:true says every execution also carries the planner ESTIMATES (startup and total cost, planned rows and width of the top node and of the child scan, and of the Bitmap Index Scan beneath it), recorded beside what happened and never judged as a measurement: they are beliefs of the planner, which depend on the statistics of the table the probe ran against (see tables) and on plannerSettings"}')"
}
# The table behind a probe, in numbers: pages the planner believes in (relpages), pages the file really has, tuples, live and dead tuples, heap and index bytes. Prints one JSON object.
table_stats() { # schema
  psql_db -q -c "SELECT json_build_object('schema', n.nspname, 'relpages', c.relpages, 'actualPages', pg_relation_size(c.oid) / 8192, 'reltuples', c.reltuples::bigint, 'heapBytes', pg_relation_size(c.oid), 'indexBytes', pg_indexes_size(c.oid),
    'nLiveTup', s.n_live_tup, 'nDeadTup', s.n_dead_tup, 'autovacuumCount', s.autovacuum_count, 'lastAnalyze', coalesce(s.last_analyze, s.last_autoanalyze))::text
    FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace LEFT JOIN pg_stat_user_tables s ON s.relid = c.oid WHERE n.nspname = '$1' AND c.relname = 'contact_requests'"
}
# A clean copy of contact_requests in a scratch schema: the same constraints and indexes (LIKE ... INCLUDING ALL), the same rows inserted in id order into a fresh heap, then VACUUM (ANALYZE).
# The original table is NOT touched (vacuuming it would change every later case), and the copy is dropped when the probe has run. Sets CLEAN_COPY_JSON (what was done, never assumed).
make_clean_copy() {
  psql_db -q -c 'DROP SCHEMA IF EXISTS qc1_clean CASCADE' -c 'CREATE SCHEMA qc1_clean' -c 'CREATE TABLE qc1_clean.contact_requests (LIKE public.contact_requests INCLUDING ALL)' \
    -c 'INSERT INTO qc1_clean.contact_requests SELECT * FROM public.contact_requests ORDER BY id' -c 'VACUUM (ANALYZE, PARALLEL 0) qc1_clean.contact_requests' >/dev/null \
    || { echo 'qc1: the clean copy of contact_requests could not be made' >&2; exit 1; }
  CLEAN_COPY_JSON="$(jq -nc '{kind:"a copy of the same rows in a scratch schema: LIKE public.contact_requests INCLUDING ALL, INSERT ... SELECT ORDER BY id into a fresh heap, then VACUUM (ANALYZE); the original table is untouched", vacuumed:true, analyzed:true}')"
}

# auto_explain on for one scanner walk, one page of items and one mixed deletion, then off again; the plans of the application's own
# statements are kept in explain-<size>.log (statements found by their text, qc1_plan_marker). A size has its plans only when every statement
# kind that run must reach was found: the scanner-continuation is reached only when the table holds more rows than one scanner unit (50,000).
# The continuation's shape (an Index Cond on id, not a filter over a scan) is also recorded from EXPLAIN (GENERIC_PLAN) of the same
# statement text, typed by the harness and labelled as such; the application's own logged plan is the evidence that counts.
capture_plans() { # n
  local n="$1" t since which out="$WORK/explain-$1.log" counts id need gen_cont gen_init cont_logged logged_cont logged_init sql_init sql_cont exec_json actual_rows probe_bloated probe_clean stats_bloated stats_clean idx=false initidx=false bsched
  local -a sched=()
  AUTOEXPLAIN_DIRTY=1   # owed a reset from here until both are proven off (cleanup does it on any exit)
  psql_db -c "ALTER SYSTEM SET auto_explain.log_min_duration = 0" >/dev/null
  psql_db -c 'SELECT pg_reload_conf()' >/dev/null
  await_setting auto_explain.log_min_duration 0 || exit 1   # the capture is really on before anything is run for it
  sleep 1
  since="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  t="$(fresh_user)"
  load_scenario "$n" "$(( 1000 < n ? 1000 : n ))" "$t" requester "$CPS"
  traverse "$t" "$WORK/plan-items.json"
  id="$(mint_attempt "$t")"
  qc1_delete_body_n mixed "$WORK/plan-items.json" "$(jq 'length' "$WORK/plan-items.json")" "$id" >"$WORK/delete.body"
  do_delete "$t" "$WORK/delete.body"
  # The table the statements ran against: background plus the scenario rows (n + k), counted before the scenario is dropped.
  actual_rows="$(psql_db -c 'SELECT count(*) FROM contact_requests')"
  psql_db -c "ALTER SYSTEM SET auto_explain.log_min_duration = -1" >/dev/null
  psql_db -c 'SELECT pg_reload_conf()' >/dev/null
  await_setting auto_explain.log_min_duration -1 || exit 1  # and off again before the next measured row
  AUTOEXPLAIN_DIRTY=0
  docker logs --since "$since" "$DB_CID" >"$WORK/pglog-$n.txt" 2>&1
  # The probe runs AFTER the capture is off and its log is taken (its own EXPLAINs must not be counted as the application's executions) and BEFORE the scenario is dropped.
  # The same statements on the table as the matrix left it (bloated: autovacuum is off and every scenario was loaded and dropped) and, as a SEPARATE labelled observation, on a
  # VACUUM (ANALYZE)d copy of the same rows. The first is the original measurement and is kept as it is; the second is never substituted for it.
  scanner_probe public; probe_bloated="$SCANNER_PROBE_JSON"; stats_bloated="$(table_stats public)"
  make_clean_copy
  scanner_probe qc1_clean; probe_clean="$SCANNER_PROBE_JSON"; stats_clean="$(table_stats qc1_clean)"
  psql_db -q -c 'DROP SCHEMA qc1_clean CASCADE' >/dev/null
  drop_scenarios
  : >"$out"
  for which in "${QC1_PLAN_KINDS[@]}"; do
    { printf '=== %s (size %s)\n' "$which" "$n"; qc1_extract_plans "$WORK/pglog-$n.txt" "$which"; } >>"$out"
  done
  counts="$(qc1_plan_counts "$WORK/pglog-$n.txt")"
  # The generic plans are of the REAL statements: the text is read out of account_erasure_reasons.go (qc1_go_sql), with its real projection, not typed here.
  sql_init="$(qc1_go_sql "$ROOT" reasonScanInitialSQL)"; sql_cont="$(qc1_go_sql "$ROOT" reasonScanContinuationSQL)"
  [[ -n "$sql_init" && -n "$sql_cont" ]] || { echo 'qc1: the scanner statements are not in account_erasure_reasons.go' >&2; exit 1; }
  # The scanner the application runs reads a unit in batches: the schedule is read from the Go source (the same function the probe uses), recorded in the row, and every
  # logged execution's LIMIT is held to it by the judge. Nothing here expects one 50,000-row statement.
  mapfile -t sched < <(qc1_scan_schedule "$ROOT")
  (( ${#sched[@]} >= 1 )) || { echo 'qc1: the scanner batch schedule is not readable from the Go source' >&2; exit 1; }
  bsched="$(jq -nc --argjson u "$(qc1_go_int "$ROOT" account_erasure_reasons.go reasonUnitRows)" --argjson f "$(qc1_go_int "$ROOT" account_erasure_reasons_scan.go reasonScanBatchFirst)" \
    --argjson g "$(qc1_go_int "$ROOT" account_erasure_reasons_scan.go reasonScanBatchGrowth)" --argjson l "$(printf '%s\n' "${sched[@]}" | jq -sc 'map(tonumber)')" \
    '{unitRows:$u, first:$f, growth:$g, limits:$l, source:"reasonUnitRows (account_erasure_reasons.go) and reasonScanBatchFirst, reasonScanBatchGrowth (account_erasure_reasons_scan.go): a unit is read in statements of these LIMITs; a page stops earlier, after the batch that holds its 100th request"}')"
  gen_cont="$(psql_db -c "EXPLAIN (GENERIC_PLAN) $sql_cont" 2>&1 | tr '\n' ' ' | sed 's/  */ /g' || true)"
  gen_init="$(psql_db -c "EXPLAIN (GENERIC_PLAN) $sql_init" 2>&1 | tr '\n' ' ' | sed 's/  */ /g' || true)"
  # The continuation must be bounded by an Index Cond on id, in the application's own logged plan and in the generic plan of the same text; the first unit
  # must be read in id order from an index (it has no id predicate to be an Index Cond).
  if [[ "$gen_cont" == *"Index Cond: (id >"* ]]; then idx=true; fi
  if [[ "$gen_init" == *"Index Scan"* || "$gen_init" == *"Index Only Scan"* ]]; then initidx=true; fi
  cont_logged=null logged_cont=null logged_init=null
  if jq -e '."scanner-continuation" >= 1' <<<"$counts" >/dev/null; then
    # Captured, then searched (qc1_plan_shapes): never `extract | grep -q`, which under pipefail reports a plan that contains the text as one that does not.
    logged_cont="$(qc1_plan_shapes "$WORK/pglog-$n.txt" scanner-continuation)" || { echo 'qc1: the logged plans could not be read' >&2; exit 1; }
    cont_logged="$(jq -r '.withIdIndexCond == .executions' <<<"$logged_cont")"
  fi
  logged_init="$(qc1_plan_shapes "$WORK/pglog-$n.txt" scanner-initial)" || { echo 'qc1: the logged plans could not be read' >&2; exit 1; }
  # Across executions: the actual rows and shared blocks of every logged scanner execution, in order.
  exec_json="$(for which in scanner-initial scanner-continuation; do
    qc1_plan_executions "$WORK/pglog-$n.txt" "$which" | jq -Rsc --arg w "$which" '[split("\n")[] | select(length > 0) | split(" ") | {rows:(.[0] | tonumber? // null), blocks:(.[1] | tonumber? // null), limit:(.[2] | tonumber? // null)}] | {key:$w, value:{count:length, rows:map(.rows), sharedBlocks:map(.blocks), limits:map(.limit)}}'
  done | jq -sc '{scannerInitial:(.[0].value), scannerContinuation:(.[1].value),
                  note:"rows and shared blocks (hit + read) of the top plan node of every logged execution, from auto_explain with log_analyze and log_buffers; recorded in execution order, not judged against a ceiling"}')"
  need="$(jq -nc --argjson n "$actual_rows" --argjson unit "$QC1_SCANNER_UNIT_ROWS" '["scanner-initial","page-items","delete-set","blank-batch"] + (if $n > $unit then ["scanner-continuation"] else [] end)')"
  if jq -e --argjson need "$need" '. as $c | $need | all(. as $k | $c[$k] >= 1)' <<<"$counts" >/dev/null; then RECORD_STATUS=measured; else RECORD_STATUS=failed; fi
  record 8 plans "$n" "$(jq -nc --argjson c "$counts" --argjson need "$need" --arg gc "$gen_cont" --arg gi "$gen_init" --arg sc "$sql_cont" --arg si "$sql_init" --argjson idx "$idx" --argjson initidx "$initidx" --argjson logged "$cont_logged" --argjson ex "$exec_json" --argjson bsched "$bsched" --argjson probe "$probe_bloated" --argjson probec "$probe_clean" --argjson statsb "$stats_bloated" --argjson statsc "$stats_clean" --argjson copy "$CLEAN_COPY_JSON" --argjson lcont "$logged_cont" --argjson linit "$logged_init" --argjson actual "$actual_rows" --argjson bg "$n" \
    '{batchSchedule:$bsched, plans:$c, required:$need, table:{backgroundRows:$bg, actualRows:$actual, note:"the rows contact_requests held while the statements ran: background plus the scenario rows (n + k)"}, file:"explain-log", continuationPlanIndexCond:$logged, executions:$ex, scannerProbe:$probe, scannerProbeClean:$probec, tables:{bloated:$statsb, clean:($statsc + $copy)}, loggedShapes:{initial:$linit, continuation:$lcont},
      generic:{source:"go-source", scannerContinuation:{sql:$sc, text:$gc, indexCondOnId:$idx}, scannerInitial:{sql:$si, text:$gi, idIndexScan:$initidx}},
      note:"the plans counted are the application'"'"'s own logged statements; generic is EXPLAIN (GENERIC_PLAN) of the SAME statement text read from account_erasure_reasons.go; the continuation unit exists only when the table holds more rows than one scanner unit (judged on table.actualRows)"}')"
  unset RECORD_STATUS
}

# End-to-end observation with the background workers on (the production
# default): not attributable, because jobs run beside the deletion, and
# labelled so. The attributable rows ran with the workers off. The counts and the
# database are still checked against what the deletion was asked to do.
observe_workers_on() { # n
  local n="$1" t k
  k=$(( 1000 < n ? 1000 : n ))
  note "size $n: end-to-end observation with FOLD_WORKERS=2"
  start_app 2
  login
  drop_scenarios
  t="$(fresh_user)"
  load_scenario "$n" "$k" "$t" requester "$CPS"
  traverse "$t" "$WORK/obs-items.json"
  delete_with "$t" mixed "$WORK/obs-items.json"
  RECORD_STATUS=observation record 9 workers-on-delete "$n" "$(jq -nc --argjson del "$DELETE_JSON" --argjson k "$k" --argjson expect "$EXPECT_JSON" --argjson outcome "$OUTCOME_JSON" \
    '{delete:$del, reasons:$k, expect:$expect, outcome:$outcome, attributable:false, note:"background workers on: jobs, their commits, WAL and any gate use run beside this deletion, so the instrument figures are not attributable to it"}')"
  drop_scenarios
  start_app 0
  login
}

# (10) the rendered panel in a real browser against the real API (qc1/panel-measure.mjs): the whole review read (the walk) and the first
# interactive page of 50 reasons, the 30 timed real clicks, the navigation controls, heap/DOM/long tasks, the per-page response bytes, request time,
# read and JSON parse time (F7), the digest path that actually ran and its time, and the changed-reason path (every reason chosen with the panel's
# own handler, one reason changed behind the page by change-reason.sh, Delete pressed: the server refuses with reason_review_changed and deletes
# nothing, the review is read again). The whole run is one instrumented window (WAL, commits, SAMPLED gate, audit_logs rows by action, memory), and
# phase-mark.sh reads the WAL position and audit_logs at each phase boundary (open, walk end, interactions end, press start, press end), so each phase
# carries its own WAL and audit-by-action figures. Needs Node, @playwright/test and its Chromium under web/ on the machine running this script,
# reaching the stack's published loopback port. Without them the row is not written, the evidence says it is missing and the run is not complete;
# --no-browser says so up front. A run that fails part-way (exit 6) leaves a PARTIAL row: the phases that finished keep their figures and their budget
# findings; it is invalid as incomplete and never counts as the measured case. suffix: "" or "-nosubtle" (crypto.subtle removed, so the bundled SHA-256 runs).
# change-reason.sh edits ONE seeded reason with the triggers off in a throwaway session, standing in for a second person editing it; the product
# has no such path and the script is the harness's, not the application's. phase-mark.sh only reads.
run_panel_measure() { # sets PANEL_RC
  PANEL_RC=0
  QC1_URL="$URL" QC1_WEB_DIR="$ROOT/web" QC1_ADMIN_EMAIL=admin@example.invalid QC1_ADMIN_PASSWORD=qc1-drill-password \
    QC1_TARGET_EMAIL="$USER_EMAIL" QC1_EXPECT="$PANEL_K" QC1_OUT="$PANEL_OUT" QC1_CHANGE_CMD="$PANEL_HOOK" QC1_PHASE_CMD="$PANEL_PHASE" \
    QC1_NO_SUBTLE="${QC1_NO_SUBTLE:-0}" QC1_CLICKS="$QC1_CLICKS_PER_CASE" \
    node "$HERE/qc1/panel-measure.mjs" || PANEL_RC=$?
}
measure_browser_panel() { # n k [suffix]
  local n="$1" k="$2" suffix="${3:-}" window
  PANEL_K="$k"; PANEL_OUT="$WORK/browser-$1-$2${3:-}.json"; PANEL_HOOK="$WORK/change-reason.sh"; PANEL_PHASE="$WORK/phase-mark.sh"
  (( BROWSER )) || return 0
  drop_scenarios
  make_user
  load_scenario "$n" "$k" "$USER_ID" both "$CPS"
  printf '#!/bin/sh\nset -eu\ncase "$1" in *[!0-9]*|"") exit 2 ;; esac\nexec docker exec -i %s psql -X -U church -d fold -v ON_ERROR_STOP=1 -c "SET session_replication_role = replica" -c "UPDATE contact_requests SET reason = left(reason, 999) || CASE WHEN right(reason, 1) = chr(33) THEN chr(63) ELSE chr(33) END WHERE id = $1 AND reason <> '"''"'"\n' "$DB_CID" >"$PANEL_HOOK"
  cat >"$PANEL_PHASE" <<SH
#!/bin/sh
set -eu
exec docker exec -i $DB_CID psql -X -U church -d fold -At -v ON_ERROR_STOP=1 -c "SELECT json_build_object('wal', pg_wal_lsn_diff(pg_current_wal_lsn(), '0/0')::bigint, 'tableRows', (SELECT count(*) FROM contact_requests), 'auditRows', (SELECT count(*) FROM audit_logs), 'auditBytes', pg_total_relation_size('audit_logs'), 'auditActions', (SELECT coalesce(json_object_agg(action, c), '{}'::json) FROM (SELECT action, count(*) AS c FROM audit_logs GROUP BY action) s))::text"
SH
  chmod 755 "$PANEL_HOOK" "$PANEL_PHASE"
  rm -f "$PANEL_OUT" "$PANEL_OUT.pages.jsonl"
  # The whole run is one window; the scheduler's edges are waited past for as long as the dense walk and the second walk can take.
  QC1_NEED_MS=$(( k >= 100000 ? 1500000 : 600000 )) instrumented run_panel_measure
  window="$(instr_json | jq -c --argjson ms "$((OP_END - OP_START))" '. + {ms:$ms, what:"the whole rendered-panel run: walk, clicks, navigation, the refused press and the second walk. The gate figures are SAMPLED, not a hold time"}')"
  drop_scenarios
  if [[ ! -s "$PANEL_OUT" ]]; then
    # No result was written (the process broke), but every review page it saw was persisted as it completed: keep them as a PARTIAL row, so a slow page is not lost with the crash.
    if [[ -s "$PANEL_OUT.pages.jsonl" ]]; then
      note "the browser measurement for $k reasons at size $n$suffix wrote no result (exit $PANEL_RC); the review pages it saw are kept as a PARTIAL row"
      RECORD_STATUS=partial record 10 "browser-panel-$k$suffix" "$n" "$(jq -nc --argjson pages "$(qc1_pages_from_jsonl "$PANEL_OUT.pages.jsonl")" --argjson k "$k" --arg suffix "$suffix" --argjson window "$window" --argjson rc "$PANEL_RC" \
        '{reasons:$k, variant:(if $suffix == "" then "standard" else "no-subtle" end), browser:{pages:$pages}, failed:{phase:"unknown", error:("the measurement process stopped before it wrote its result (exit " + ($rc | tostring) + ")")}, window:$window}')"
    else
      note "the browser measurement for $k reasons at size $n$suffix did not run (exit $PANEL_RC); the row is missing and the run is not complete"
    fi
    return 0
  fi
  if (( PANEL_RC == 0 )); then
    record 10 "browser-panel-$k$suffix" "$n" "$(jq -nc --slurpfile b "$PANEL_OUT" --argjson k "$k" --arg suffix "$suffix" --argjson window "$window" \
      '{reasons:$k, variant:(if $suffix == "" then "standard" else "no-subtle" end), browser:$b[0], window:$window}')"
  elif (( PANEL_RC == 6 )); then
    note "the browser measurement for $k reasons at size $n$suffix stopped part-way; its finished phases are kept as a PARTIAL row"
    RECORD_STATUS=partial record 10 "browser-panel-$k$suffix" "$n" "$(jq -nc --slurpfile b "$PANEL_OUT" --argjson k "$k" --arg suffix "$suffix" --argjson window "$window" \
      '{reasons:$k, variant:(if $suffix == "" then "standard" else "no-subtle" end), browser:$b[0], failed:$b[0].failed, window:$window}')"
  else
    note "the browser measurement for $k reasons at size $n$suffix did not run (exit $PANEL_RC); the row is missing and the run is not complete"
  fi
}

# A deletion of an account with no reasons, recorded as item 6 "order-control-delete-<start|end>" (reasons 0): the same work at the start and the end of the size's run. The cost
# of an operation is not constant over a run (the audit table grows, dead tuples pile up with autovacuum off, the cache changes), and these two rows say by how much for the one
# operation that has no reason-dependent work. Recorded with the instrument set and the top statements (cluster-wide, not attributed); nothing is judged against them.
order_control_delete() { # n which
  local n="$1" which="$2" t
  drop_scenarios
  t="$(fresh_user)"
  echo '[]' >"$WORK/items-order-delete.json"
  delete_with "$t" keep "$WORK/items-order-delete.json"
  record 6 "order-control-delete-$which" "$n" "$(jq -nc --argjson del "$DELETE_JSON" --argjson bytes "$BODY_BYTES" --argjson expect "$EXPECT_JSON" --argjson outcome "$OUTCOME_JSON" --arg which "$which" \
    '{delete:$del, reasons:0, bodyBytes:$bytes, expect:$expect, outcome:$outcome, control:{of:"a deletion of an account with no reasons", position:$which, note:"the same deletion at the start and at the end of one size run; the distance between the two is the order effect on a deletion, recorded and not judged"}}')"
}

# auto_explain (plans WITH actual rows, timing and buffers, as the server is already configured) and log_lock_waits turned on for ONE operation and off again, each change waited
# for as a new session sees it. Never on for the matrix: log_min_duration 0 logs a plan for every statement of every session, and per-node timing slows each of them.
autoexplain_on() {
  AUTOEXPLAIN_DIRTY=1   # before the first change: a failure anywhere after this leaves the reset owed
  psql_db -c "ALTER SYSTEM SET auto_explain.log_min_duration = 0" -c "ALTER SYSTEM SET log_lock_waits = on" >/dev/null
  psql_db -c 'SELECT pg_reload_conf()' >/dev/null
  await_setting auto_explain.log_min_duration 0 || exit 1
  await_setting log_lock_waits on || exit 1
}
autoexplain_off() {
  psql_db -c "ALTER SYSTEM SET auto_explain.log_min_duration = -1" -c "ALTER SYSTEM RESET log_lock_waits" >/dev/null
  psql_db -c 'SELECT pg_reload_conf()' >/dev/null
  await_setting auto_explain.log_min_duration -1 || exit 1
  await_setting log_lock_waits off || exit 1
  AUTOEXPLAIN_DIRTY=0
}
# The deletion of an account with no reasons again, at the start and at the end of a size run, with auto_explain on for THAT deletion only: the plan, with actual rows, timing and
# buffers, of the audit-retention report statement the application itself runs once per erasure (account_erasure_audit.go), found in the server log by its text and PROVEN to be the
# application statement (qc1_audit_report_evidence), never typed or re-run here. Recorded as item 6 "order-control-delete-<start|end>-explained" (reasons 0).
# It is an EXTRA deletion beside the unexplained controls, which stay exactly as they were: the two are never compared as if they cost the same. This one is not run inside
# the instrument window (the gate and memory samplers are sessions of their own and would fill the plan log and slow the deletion); it records its own wall time as the client saw it.
# Overhead, stated: the explained deletion is slower than the control (per-node timing and a log line for each statement it runs), so its time is not a control.
# Caveats, recorded in the row: one execution of the statement; a plan with ANALYZE and BUFFERS is for that one run (cache state, JIT decision and buffers as they were); the lock
# waits are only those longer than deadlock_timeout; the cluster has other sessions (the application pool, jobs) whose statements are in the same log.
explained_order_control_delete() { # n which
  local n="$1" which="$2" t id audit_mark since log wait ms0 ms1 ev outcome expect tr0 ar0 ab0 body_bytes kept removed
  drop_scenarios
  t="$(fresh_user)"
  echo '[]' >"$WORK/items-order-delete.json"
  wait="$(qc1_scheduler_wait "$(ms)" 60000 "$APP_STARTED_MS")"
  if (( wait > 0 )); then sleep "$(awk -v w="$wait" 'BEGIN { printf "%.3f", w / 1000 }')"; fi
  quiesce
  read -r ar0 ab0 <<<"$(psql_db -c "SELECT count(*) || ' ' || pg_total_relation_size('audit_logs') FROM audit_logs")"
  tr0="$(table_rows)"
  id="$(mint_attempt "$t")"
  qc1_delete_body_n keep "$WORK/items-order-delete.json" 0 "$id" >"$WORK/delete.body"
  body_bytes="$(wc -c <"$WORK/delete.body")"
  audit_mark="$(psql_db -c 'SELECT coalesce(max(id), 0) FROM audit_logs')"
  [[ "$audit_mark" =~ ^[0-9]+$ ]] || { echo "qc1: cannot read the audit watermark ('$audit_mark')" >&2; exit 1; }
  autoexplain_on
  since="$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)"
  ms0="$(ms)"
  do_delete "$t" "$WORK/delete.body"
  ms1="$(ms)"
  autoexplain_off   # off, and waited for, before the log is read and before anything else runs
  log="$WORK/pglog-audit-$which-$n.txt"
  docker logs --since "$since" "$DB_CID" >"$log" 2>&1
  kept="$(jq '.erasure.reasonsKept // null' "$WORK/delete.out" 2>/dev/null || echo null)"; removed="$(jq '.erasure.reasonsRemoved // null' "$WORK/delete.out" 2>/dev/null || echo null)"
  verify_outcome "$t" "$WORK/items-order-delete.json" 0 0 "$audit_mark"
  outcome="$OUTCOME_JSON"
  expect="$(jq -nc '{items:0, kept:0, removed:0}')"
  ev="$(qc1_audit_report_evidence "$log" "$ROOT" "$t" true)" || ev='{"statementBlocks":0}'
  { printf '=== audit-report (size %s, %s)\n' "$n" "$which"; qc1_extract_plans "$log" audit-report; } >"$WORK/explain-audit-$which-$n.log"
  record 6 "order-control-delete-$which-explained" "$n" "$(jq -nc --arg st "$DEL_STATUS" --argjson ms "$(( ms1 - ms0 ))" --argjson reqms "$DEL_MS" --argjson kept "$kept" --argjson removed "$removed" --argjson bytes "$body_bytes" \
      --argjson expect "$expect" --argjson outcome "$outcome" --argjson ev "$ev" --argjson ar0 "$ar0" --argjson ab0 "$ab0" --argjson tr0 "${tr0:-null}" --arg which "$which" --argjson tid "$t" \
      --argjson planner "$(planner_settings)" '
    {reasons:0, bodyBytes:$bytes, expect:$expect, outcome:$outcome,
     explained:{targetUserId:$tid, status:$st, ms:$ms, requestMs:$reqms, reasonsKept:$kept, reasonsRemoved:$removed, auditRowsBefore:$ar0, auditBytesBefore:$ab0, tableRowsBefore:$tr0,
                note:"this deletion ran with auto_explain (log_min_duration 0, ANALYZE, BUFFERS, per-node timing) and log_lock_waits on, so its time includes that overhead and is NOT comparable with the unexplained controls; it is outside the instrument window"},
     auditRetention:$ev,
     autoExplain:{scope:"on for this one deletion only, off and waited for before the log was read; the matrix itself runs with auto_explain off",
       settings:{"auto_explain.log_min_duration":0, "auto_explain.log_analyze":"on", "auto_explain.log_buffers":"on", "auto_explain.log_timing":"on", "auto_explain.log_format":"text", "auto_explain.log_nested_statements":"off", "log_lock_waits":"on"},
       caveats:["one execution of the statement, one cluster: a plan with ANALYZE and BUFFERS describes that run (cache state, the JIT decision and buffers as they were)", "log_lock_waits reports only waits longer than deadlock_timeout (1 s); no logged wait is not no wait", "the plan shows no row locks because the statement takes none; it holds an ACCESS SHARE lock on audit_logs only", "other sessions (the application pool, jobs) ran statements in the same log; only the application statement proven by identity is read", "docker logs --since is read after the deletion; the server log is the only source, nothing was substituted or re-run"]},
     plannerSettings:$planner,
     control:{of:"a deletion of an account with no reasons", position:$which, explained:true, note:"an EXTRA deletion beside order-control-delete-start and order-control-delete-end, which stay unexplained and are the order controls"}}')"
}


measure_size() { # n
  local n="$1" t k d mode rows tr_json bk
  CURRENT_SIZE="$n"
  note "size $n: loading background rows"
  # TRUNCATE of the whole referencing set (contact_grants, registrations), each proved empty first: see qc1_reset_contact_requests. The seeded admin, the
  # counterparts and the group are in other tables and must survive it; the scenarios below are built on them.
  qc1_reset_contact_requests || exit 1
  [[ "$(psql_db -c "SELECT count(*) FROM users WHERE email = 'admin@example.invalid'")" == 1 && "$(psql_db -c "SELECT count(*) FROM groups WHERE id = $GROUP")" == 1 \
    && "$(psql_db -c "SELECT count(*) FROM users WHERE email LIKE 'qc1-counterpart-%@example.invalid'")" == 10 ]] \
    || { echo 'qc1: clearing the background rows removed the seeded admin, counterparts or group' >&2; exit 1; }
  [[ "$(psql_db -c 'SELECT count(*) FROM contact_requests')" == 0 ]] || { echo 'qc1: contact_requests is not empty after clearing it' >&2; exit 1; }
  load_filler "$n" all
  # The cost of one request that changes nothing, for the WAL a deletion shares
  # with its own session bookkeeping.
  instrumented ctl_request
  CONTROL_WAL="$WAL_BYTES"

  # The delete order control, START: a deletion of an account with no reasons, before any other case of this size has run. The same deletion is repeated at the END of the
  # matrix (order_control_delete end): the distance between the two is what the run itself did to the cost of a deletion (audit rows, dead tuples, cache), recorded and not judged.
  order_control_delete "$n" start
  explained_order_control_delete "$n" start

  # (2) every reason belongs to someone else.
  t="$(fresh_user)"
  instrumented traverse "$t" "$WORK/items.json"
  tr_json="$(traversal_json)"
  delete_with "$t" keep "$WORK/items.json"
  record 2 unrelated "$n" "$(jq -nc --argjson tr "$tr_json" --argjson del "$DELETE_JSON" --argjson expect "$EXPECT_JSON" --argjson outcome "$OUTCOME_JSON" '{traversal:$tr, delete:$del, expect:$expect, outcome:$outcome}')"

  # (3) discovery with k reasons in both party directions, and (6) deletion with
  # every reason kept, removed, or half and half. Each deletion destroys its
  # account, so every mode gets a fresh user carrying the same rows.
  for d in "${QC1_DIRECTIONS[@]}"; do
    for k in "${ALL_COUNT_LIST[@]}"; do
      (( k <= n )) || continue
      for mode in "${QC1_MODES[@]}"; do
        drop_scenarios
        t="$(fresh_user)"
        (( k == 0 )) || load_scenario "$n" "$k" "$t" "$d" "$CPS"
        if [[ "$mode" == keep ]]; then
          # A dense walk (100,000 reasons is a thousand pages) is measured against the 2-minute line, so its window must clear the scheduler's edges for longer.
          QC1_NEED_MS=$(( k >= 100000 ? 600000 : 60000 )) instrumented traverse "$t" "$WORK/items-$d-$k.json"
          record 3 "discovery-$d-$k" "$n" "$(jq -nc --argjson tr "$(traversal_json)" --argjson k "$k" --argjson roles "$(roles_json "$WORK/items-$d-$k.json")" '{traversal:$tr, reasons:$k, expect:{items:$k}, roles:$roles}')"
        else
          traverse "$t" "$WORK/items-$d-$k.json"
        fi
        if ! printf '%s\n' "${ALL_DELETE_LIST[@]}" | grep -qx "$k"; then continue; fi
        delete_with "$t" "$mode" "$WORK/items-$d-$k.json"
        record 6 "delete-$d-$k-$mode" "$n" "$(jq -nc --argjson del "$DELETE_JSON" --argjson k "$k" --argjson bytes "$BODY_BYTES" --argjson expect "$EXPECT_JSON" --argjson outcome "$OUTCOME_JSON" --argjson roles "$(roles_json "$WORK/items-$d-$k.json")" \
          '{delete:$del, reasons:$k, bodyBytes:$bytes, expect:$expect, outcome:$outcome, roles:$roles}')"
      done
    done
  done
  drop_scenarios

  note "size $n: request size near and across maxRequestBytes"
  measure_request_size "$n"

  # The plans of the application's own statements, while the table still holds
  # the background reasons.
  note "size $n: plans (auto_explain)"
  capture_plans "$n"

  # The rendered panel for dense histories, in a real browser.
  for bk in "${ALL_BROWSER_LIST[@]}"; do (( bk <= n )) && measure_browser_panel "$n" "$bk"; done
  # One extra case, not part of the matrix: crypto.subtle removed, so the bundled SHA-256 makes the digest (a plain-http LAN address has none).
  if (( BROWSER_NOSUBTLE )) && [[ "$n" == "${SIZE_LIST[0]}" ]]; then
    # The largest browser count this table can hold (the largest of the list is skipped when it is above the first size, as 100,000 is above 10,000).
    bk=""; for k in "${ALL_BROWSER_LIST[@]}"; do (( k <= n )) && bk="$k"; done
    if [[ -n "$bk" ]]; then QC1_NO_SUBTLE=1 measure_browser_panel "$n" "$bk" "-nosubtle"; else note "no browser count fits a table of $n rows: the fallback-digest case is not run"; fi
  fi

  if [[ "$n" == "$MAX_SIZE" ]]; then observe_workers_on "$n"; fi

  # The order control: the first discovery case again, now that this size's run has left its audit rows and a dirtier table behind. It is recorded
  # beside the first (seq says which ran when; evidence-status.json confounding gives the ratio) and is not judged against anything.
  note "size $n: order control (the first discovery case again)"
  drop_scenarios
  t="$(fresh_user)"
  load_scenario "$n" 10 "$t" requester "$CPS"
  instrumented traverse "$t" "$WORK/items-control.json"
  record 3 order-control-requester-10 "$n" "$(jq -nc --argjson tr "$(traversal_json)" '{traversal:$tr, reasons:10, expect:{items:10}, control:{of:"discovery-requester-10", note:"the same case as discovery-requester-10, run last at this size: the distance between the two is the order effect of the audit rows and the table the run itself leaves behind"}}')"
  drop_scenarios

  order_control_delete "$n" end
  explained_order_control_delete "$n" end

  # (1) sparse: the same table with every background reason blanked (many
  # requests, almost no reasons), an account with none.
  note "size $n: blanking background reasons for the sparse case"
  psql_db -c 'SET session_replication_role = replica' -c "UPDATE contact_requests SET reason = ''" >/dev/null
  psql_db -c 'VACUUM (ANALYZE, PARALLEL 0) contact_requests' >/dev/null   # not parallel: a parallel vacuum of the churned heap asks for 64 MiB of dynamic shared memory and the container keeps the daemon's 64 MiB /dev/shm
  t="$(fresh_user)"
  instrumented traverse "$t" "$WORK/items.json"
  tr_json="$(traversal_json)"
  delete_with "$t" keep "$WORK/items.json"
  record 1 sparse "$n" "$(jq -nc --argjson tr "$tr_json" --argjson del "$DELETE_JSON" --argjson expect "$EXPECT_JSON" --argjson outcome "$OUTCOME_JSON" '{traversal:$tr, delete:$del, expect:$expect, outcome:$outcome}')"

  # (7) loss of in-memory human progress. What each event discards is fixed by
  # the design. What is measured is kept apart from what is assumed: the harness
  # page load (a curl loop over the page route, not a rendered panel), the
  # rendered browser readiness for the dense histories above (item 10), and, apart
  # from both, the human assumptions behind the reasons-per-sitting assessment.
  rows="$(jq -sc --argjson size "$n" '[.[] | select(.item == 3 and .size == $size) | {direction:(.case | split("-")[1]), reasons:.reasons, pageLoadMs:.traversal.totalMs}]' "$MEASUREMENTS")"
  record 7 human-progress "$n" "$(jq -nc --argjson rows "$rows" --argjson assessment "$(qc1_sitting_assessment "$rows" "$SECONDS_PER_REASON" "$SITTING_MINUTES")" \
    --argjson browser "$(jq -sc --argjson size "$n" '[.[] | select(.item == 10 and .size == $size) | {reasons, renderReadyMs:.browser.renderReadyMs, heapAfterGcBytes:.browser.heapAfterGcBytes}]' "$MEASUREMENTS")" \
    '{harnessPageLoad:{what:"wall clock of the harness curl loop over the page route; not a rendered panel", rows:$rows},
      renderedPanel:{what:"item 10 rows: the panel rendered in a real browser", rows:$browser},
      humanAssumptions:{secondsPerReason:$assessment.secondsPerReason, sittingMinutes:$assessment.sittingMinutes, note:"assumptions, not measurements"},
      assessment:$assessment,
      discards:{close_or_reload:"every choice", conflict_409:"changed items only; choices whose requestId and reviewHash match survive the reload", restart_or_restore:"the attempt id (an open panel holds none until Delete is pressed); the panel keeps its choices, a retained id settles history_unavailable and a fresh press mints a new one", stop:"nothing; Delete stays disabled"}}')"
}

for n in "${SIZE_LIST[@]}"; do measure_size "$n"; done

# (4) and the legacy PUT body are T07B-3a's: recorded as pending, never as passed.
jq -nc --arg reason "$QC1_PENDING_4" '{status:"pending", item:4, case:"legacy-discovery", size:null, reason:$reason}' >>"$MEASUREMENTS"
jq -nc --arg reason "$QC1_PENDING_5" '{status:"pending", item:5, case:"legacy-put", size:null, reason:$reason}' >>"$MEASUREMENTS"

# --- the evidence: environment record, judge, status and summary. The same function finalizes a run that stops early (cleanup); a finished run calls it here, and a
# finalization that could not write a file is a failed run.
finalize_evidence complete 0 || { echo 'qc1: the evidence of this run could not be written completely' >&2; exit 1; }
note "measurements: $(wc -l <"$MEASUREMENTS") rows; budget failures: $(jq '.budgetFailures | length' "$WORK/evidence-status.json"); invalid findings: $(jq '.invalid | length' "$WORK/evidence-status.json"); evidence complete: $(jq -r .complete "$WORK/evidence-status.json"); coverage: $(jq -r .coverage.kind "$WORK/evidence-status.json")"
