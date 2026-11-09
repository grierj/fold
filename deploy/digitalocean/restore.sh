#!/usr/bin/env bash
# Restore an archive into a fresh database, then keep the site private until
# the operator has repeated post-backup erasures. Never restore over live fold.
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
ORIGINAL_ARGS=("$@")
DIR=/opt/fold
SOURCE=/root/fold
DUMP=""
IMAGE=""
COMMIT=""
MODE=dump
MODE_SET=0
DRY=0
DRILL=0
REOPEN_NONE=0
CHECKED=0
STATE=""
CLASS=""
RELEASE=""
DBS=""
R=""
P=""
PROJECT=""
WAIT=120
HEALTH_WAIT=90

fail() { printf 'restore.sh: %s\n' "$*" >&2; exit 1; }
usage() {
  cat <<'HELP'
Usage: restore.sh --dump FILE [--image REF | --commit SHA] [--source-dir /root/fold]
       restore.sh --resume [--dump SAME_FILE]
       restore.sh --abort | --status
       restore.sh --reopen [--checked-church-records]
       [--deploy-dir /opt/fold] [--dry-run] [--drill] [--reopen-if-none]

Stops the public site, restores to a new database and swaps the whole database.
Recovery runs at http://localhost:18080 through an SSH tunnel, workers off.
Re-delete accounts erased after the backup using the church's records. A known
zero count permits --reopen-if-none. Otherwise --reopen requires your explicit
--checked-church-records statement. No deleted-account list is saved here.
Exit 3 means --reopen-if-none restored the archive but SITE IS CLOSED in
private recovery. Normal --dump and --resume can finish private recovery with
exit 0. Once collection is recorded, --resume no longer needs the archive.
Rejected preflight leaves the installed recovery-tools unchanged. Target image
checks run from a temporary frozen copy, published only after validation.
Backup/expiry are suspended until recovery ends; a pre-existing backup may
hold preflight for at most 120 seconds before anything stops.
Never delete recovery.lock, ops.lock or backup.lock to bypass recovery.
HELP
}
value() { [[ $# -ge 2 && -n "$2" ]] || fail "$1 needs a value"; }
while (( $# )); do
  case "$1" in
    --help|-h) usage; exit 0 ;;
    --deploy-dir) value "$@"; DIR="$2"; shift 2 ;;
    --source-dir) value "$@"; SOURCE="$2"; shift 2 ;;
    --dump) value "$@"; DUMP="$2"; shift 2 ;;
    --image) value "$@"; IMAGE="$2"; shift 2 ;;
    --commit) value "$@"; COMMIT="$2"; shift 2 ;;
    --resume|--abort|--status|--reopen)
      (( ! MODE_SET )) || fail 'choose only one of --resume, --abort, --status, --reopen'
      MODE="${1#--}"; MODE_SET=1; shift ;;
    --dry-run) DRY=1; shift ;;
    --drill) DRILL=1; shift ;;
    --reopen-if-none) REOPEN_NONE=1; shift ;;
    --checked-church-records) CHECKED=1; shift ;;
    *) fail "unknown option: $1" ;;
  esac
done
[[ "$DIR" == /* && -d "$DIR" ]] || fail '--deploy-dir must be an existing absolute directory; run provision.sh first'
DIR="$(cd "$DIR" && pwd -P)"
if (( DRILL )); then
  [[ "$DIR" != /opt/fold && "$DIR" != /opt/fold/* ]] || fail '--drill refuses /opt/fold and its children'
elif (( ! DRY )) && [[ "$(id -u)" != 0 ]]; then fail 'run as root'; fi
[[ -f "$SCRIPT_DIR/checks.sh" && ! -L "$SCRIPT_DIR/checks.sh" ]] || fail "missing checks.sh; run the current provision.sh --deploy-dir $DIR to install the complete tool set"
# shellcheck source=deploy/digitalocean/checks.sh
source "$SCRIPT_DIR/checks.sh"
declare -F ops_lock_acquire >/dev/null || fail "checks.sh lacks ops_lock_acquire; run the current provision.sh --deploy-dir $DIR"
# A staged copy is entered only by the bootstrap re-exec, on its inherited
# descriptor. Check before ops_lock_acquire could open a new description.
if [[ "$SCRIPT_DIR" == "$DIR"/.recovery-tools.* ]]; then
  inherited_ops_lock "$DIR" || fail 'staged tools require the inherited operations lock'
fi
# This is deliberately before reading recovery.lock, release pins or Docker.
if (( ! DRY )) && [[ "$MODE" != status ]]; then
  ops_lock_acquire "$DIR" "restore-$MODE" || exit $?
fi
for name in compose_in dc pg recovery_ids runtime_tools recovery_classify recovery_accepts image_recovery_level commit_recovery_level host_recovery_floor write_recovery_floor backup_coord_acquire backup_coord_release; do
  declare -F "$name" >/dev/null || fail "checks.sh lacks $name; run the current provision.sh --deploy-dir $DIR to install the complete tool set"
done
for name in $(runtime_tools); do
  [[ -f "$SCRIPT_DIR/$name" && ! -L "$SCRIPT_DIR/$name" ]] || fail "missing $name beside restore.sh; run the current provision.sh --deploy-dir $DIR to install the complete tool set"
done
# Test hooks cannot be activated accidentally on a deployed stack.
if [[ -n "${RESTORE_DRILL_PAUSE:-}${RESTORE_DRILL_FAULT:-}${RESTORE_DRILL_FORCE_TWO_STEP:-}${RESTORE_DRILL_WAIT:-}" ]]; then
  (( DRILL )) || fail 'RESTORE_DRILL hooks require --drill outside /opt/fold'
fi
if (( DRILL )); then
  export DEPLOY_DRILL=1
  validate_deploy_drill "$DIR" || exit 1
  [[ "${RESTORE_DRILL_WAIT:-120}" =~ ^[1-9][0-9]{0,2}$ ]] || fail 'invalid RESTORE_DRILL_WAIT'
  WAIT="${RESTORE_DRILL_WAIT:-120}"
  export RESTORE_DRILL_OPERATION=1
fi
case "${RESTORE_DRILL_PAUSE:-}" in ''|after-lock|before-swap|mid-pin|recovering) ;; *) fail 'unknown RESTORE_DRILL_PAUSE point' ;; esac
case "${RESTORE_DRILL_FAULT:-}" in
  ''|after-create|before-swap|swap-ack-lost|after-swap|half|before-pin|mid-pin|after-pin|after-recovery-create|before-collect-drop|after-collect-drop|before-drop|after-drop|after-floor|after-app-start|after-caddy-start|mid-smoke|stop-fails|bad-recovery|after-abort-intent|before-abort-drop|after-abort-drop|after-abort-start|mid-abort-smoke|abort-drop-ack-lost|during-restore) ;;
  *) fail 'unknown RESTORE_DRILL_FAULT point' ;;
esac
case "${RESTORE_DRILL_FORCE_TWO_STEP:-0}" in 0|1) ;; *) fail 'RESTORE_DRILL_FORCE_TWO_STEP must be 0 or 1' ;; esac
[[ -z "$IMAGE" || -z "$COMMIT" ]] || fail 'choose --image or --commit'
if [[ -n "$IMAGE" ]]; then
  [[ "$IMAGE" =~ ^[A-Za-z0-9][A-Za-z0-9._:/-]*$ ]] || fail 'invalid image reference'
  assert_pinned_image "$IMAGE" || exit 1
fi
[[ -z "$COMMIT" || "$COMMIT" =~ ^[0-9a-f]{7,40}$ ]] || fail '--commit must be a git SHA'
if [[ "$MODE" != dump && ( -n "$IMAGE" || -n "$COMMIT" ) ]]; then fail 'resume/abort/reopen/status use the target recorded in recovery.lock'; fi
if (( REOPEN_NONE )) && [[ "$MODE" != dump ]]; then fail '--reopen-if-none requires --dump'; fi
if (( CHECKED )) && [[ "$MODE" != reopen ]]; then fail '--checked-church-records requires --reopen'; fi

pause_at() {
  [[ "${RESTORE_DRILL_PAUSE:-}" == "$1" ]] || return 0
  [[ -p "$DIR/drill.fifo" ]] || fail 'missing drill.fifo'
  unset RESTORE_DRILL_PAUSE
  # Keep the blocking read in this process. A helper subshell would retain
  # fd 9 after SIGKILL of the holder, defeating the crash-recovery drill.
  exec 8<>"$DIR/drill.fifo"
  printf '%s\n' "$1" >"$DIR/drill.ready.tmp"
  mv "$DIR/drill.ready.tmp" "$DIR/drill.ready"
  local token=""
  IFS= read -r -t 180 token <&8 && [[ "$token" == continue ]] || fail 'drill barrier cancelled or timed out'
  exec 8>&-
}
fault_at() {
  [[ "${RESTORE_DRILL_FAULT:-}" != "$1" ]] || kill -KILL "$$"
}
if (( ! DRY )) && [[ "$MODE" != status ]]; then pause_at after-lock; fi

# Re-exec an immutable staged copy before preflight, but do not replace the
# installed recovery-tools until the complete image/commit preflight succeeds.
# A rejected image or failed pull therefore leaves that installed set intact.
STAGED_TOOLS=""
if [[ "$SCRIPT_DIR" == "$DIR"/.recovery-tools.* ]]; then
  [[ "$MODE" == dump && "$DRY" == 0 ]] || fail 'staged tools require an initial restore'
  inherited_ops_lock "$DIR" || fail 'staged tools require the inherited operations lock'
  [[ "$(stat -c '%u:%a' "$SCRIPT_DIR")" == 0:700 \
    && -f "$SCRIPT_DIR/.stage" && ! -L "$SCRIPT_DIR/.stage" \
    && "$(cat "$SCRIPT_DIR/.stage")" == "fold-recovery-tools-stage-v1:$SCRIPT_DIR" ]] || fail 'invalid staged tool set'
  STAGED_TOOLS="$SCRIPT_DIR"
fi
# shellcheck disable=SC2329 # EXIT trap for a rejected staged preflight.
cleanup_staged_tools() {
  [[ -z "$STAGED_TOOLS" || ! -d "$STAGED_TOOLS" ]] || rm -rf -- "$STAGED_TOOLS"
}
trap cleanup_staged_tools EXIT
stage_tools() {
  require_deploy_tools_version "$SCRIPT_DIR" "$DIR" || exit 1
  [[ ! -L "$DIR/recovery-tools" ]] || fail 'recovery-tools must not be a symlink'
  STAGED_TOOLS="$(mktemp -d "$DIR/.recovery-tools.XXXXXX")"
  chmod 700 "$STAGED_TOOLS"; chown 0:0 "$STAGED_TOOLS"
  local name
  for name in $(runtime_tools); do
    install -m 644 "$SCRIPT_DIR/$name" "$STAGED_TOOLS/$name"
    [[ "$name" != *.sh || "$name" == checks.sh ]] || chmod 755 "$STAGED_TOOLS/$name"
  done
  printf 'fold-recovery-tools-stage-v1:%s\n' "$STAGED_TOOLS" >"$STAGED_TOOLS/.stage"
  chmod 600 "$STAGED_TOOLS/.stage"
  sync -f "$STAGED_TOOLS"
}
publish_tools() {
  local old path name
  local -a saved=()
  [[ -n "$STAGED_TOOLS" ]] || fail 'no staged tool set to publish'
  # Recover the sole complete set after a killed two-rename replacement.
  # This runs only after preflight, never on an initial image refusal.
  if [[ ! -e "$DIR/recovery-tools" ]]; then
    for path in "$DIR"/.old-recovery-tools.*; do [[ ! -e "$path" ]] || saved+=("$path"); done
    if (( ${#saved[@]} )); then
      (( ${#saved[@]} == 1 )) || fail 'multiple saved recovery tool sets; retain them and restore the verified current set to recovery-tools'
      for name in $(runtime_tools); do [[ -f "${saved[0]}/$name" ]] || fail 'incomplete saved recovery tool set'; done
      mv "${saved[0]}" "$DIR/recovery-tools"
    fi
  fi
  require_deploy_tools_version "$STAGED_TOOLS" "$DIR" || exit 1
  old="$(mktemp -d "$DIR/.old-recovery-tools.XXXXXX")"; rmdir "$old"
  [[ ! -d "$DIR/recovery-tools" ]] || mv "$DIR/recovery-tools" "$old"
  if ! mv "$STAGED_TOOLS" "$DIR/recovery-tools"; then
    [[ ! -d "$old" ]] || mv "$old" "$DIR/recovery-tools"
    fail 'failed to install frozen recovery tools; no release or database change attempted'
  fi
  STAGED_TOOLS=""
  SCRIPT_DIR="$DIR/recovery-tools"
  rm "$SCRIPT_DIR/.stage"
  sync -f "$DIR"
  [[ ! -d "$old" ]] || rm -rf -- "$old"
}
# Compose must resolve the site's own project and files, never shell overrides.
unset COMPOSE_FILE COMPOSE_PROJECT_NAME COMPOSE_PROFILES FOLD_RECOVERY_IMAGE FOLD_RECOVERY_NONCE

get() { jq -er "$1" <<<"$STATE"; }
nullable() { jq -r "$1 // empty" <<<"$STATE"; }
# No eval, shell source, or personal ids in the durable recovery state.
store() {
  local tmp
  tmp="$(mktemp "$DIR/.recovery.lock.XXXXXX")"
  if ! { printf '%s\n' "$STATE" >"$tmp" && chmod 600 "$tmp" && chown 0:0 "$tmp" && sync -f "$tmp"; }; then
    rm -f "$tmp"; fail 'could not durably write recovery.lock; services must stay stopped'
  fi
  if [[ "${1:-}" == exclusive ]]; then
    if ! ln "$tmp" "$DIR/recovery.lock"; then rm -f "$tmp"; fail 'recovery.lock already exists'; fi
    rm "$tmp"
  else mv -f "$tmp" "$DIR/recovery.lock"; fi
  sync -f "$DIR" || fail 'could not fsync recovery.lock directory'
}
record() {
  local phase="$1" at="${2:-}"
  STATE="$(jq --arg phase "$phase" --arg at "$at" --arg now "$(date -u +%FT%TZ)" \
    '.phase=$phase | .at=$at | .history += [{phase:$phase,at:$at,time:$now,cleanup:.cleanup}]' <<<"$STATE")"
  store
  printf 'restore.sh: phase=%s cleanup=%s\n' "$phase" "$(get .cleanup)"
}
remove_lock() { rm -- "$DIR/recovery.lock" && sync -f "$DIR"; }
read_lock() {
  [[ -f "$DIR/recovery.lock" && ! -L "$DIR/recovery.lock" ]] || fail "no regular $DIR/recovery.lock"
  [[ "$(stat -c '%u:%a' "$DIR/recovery.lock")" == 0:600 ]] || fail 'recovery.lock must be root-owned mode 600'
  STATE="$(cat "$DIR/recovery.lock")"
  jq -e '.format==1 and (.nonce|test("^[0-9a-f]{32}$")) and (.origOid|test("^([0-9]+)?$"))
    and (.restoreOid|test("^([0-9]+)?$")) and (.cleanup=="none" or .cleanup=="drop-intent" or .cleanup=="dropped")
    and (.level|type=="number" and .>=0 and .<=999999999 and .==floor)
    and (.count|type=="string" and test("^(unknown|[0-9]+)$"))
    and (.imageId|test("^sha256:[0-9a-f]{64}$"))
    and (.phase|type=="string") and (.previous.imageId|test("^sha256:[0-9a-f]{64}$"))' <<<"$STATE" >/dev/null || fail 'invalid recovery.lock; retain it for inspection'
  DUMP="${DUMP:-$(get .dump)}"
  SOURCE="$(get .source)"; IMAGE="$(nullable .image)"; COMMIT="$(nullable .commit)"
  R="fold_restore_$(get '.nonce[0:16]')"; P="fold_pre_restore_$(get '.nonce[0:16]')"
}
release_snapshot() {
  local config ref image_id head=""
  config="$(dc config --format json)" || return 1
  ref="$(jq -er .services.app.image <<<"$config")" || return 1
  image_id="$(docker image inspect --format '{{.Id}}' "$ref")" || return 1
  [[ -z "$COMMIT" ]] || head="$(git -C "$SOURCE" rev-parse HEAD)" || return 1
  jq -n --arg version "$(sed -n '/^FOLD_VERSION=/p' "$DIR/.env")" \
    --arg composeFile "$(sed -n '/^COMPOSE_FILE=/p' "$DIR/.env")" \
    --arg imageLine "$(awk '/^  app:/{a=1;next} a && /^  [^ ]/{a=0} a && /^    image:/{print}' "$DIR/docker-compose.yml")" \
    --arg imageId "$image_id" --arg sourceHead "$head" \
    '{versionLine:$version,composeFileLine:$composeFile,imageLine:$imageLine,imageId:$imageId,sourceHead:$sourceHead}'
}
observe() {
  local f r p previous target current expected no_original=0
  DBS="$(pg postgres -F '|' -c "SELECT datname,oid,pg_database_size(oid),datallowconn FROM pg_database WHERE datname='fold' OR datname ~ '^fold_(pre_)?restore_[0-9a-f]{16}$' ORDER BY datname")" || fail 'cannot observe database identities; no database changes attempted'
  f="$(awk -F '|' '$1=="fold" {print $2}' <<<"$DBS")"
  r="$(awk -F '|' -v n="$R" '$1==n {print $2}' <<<"$DBS")"
  p="$(awk -F '|' -v n="$P" '$1==n {print $2}' <<<"$DBS")"
  CLASS="$(recovery_classify "$(get .origOid)" "$(get .restoreOid)" "$f" "$r" "$p")"
  if awk -F '|' -v r="$R" -v p="$P" '$1!="fold" && $1!=r && $1!=p {bad=1} END {exit !bad}' <<<"$DBS"; then CLASS=invalid; fi
  current="$(release_snapshot)" || fail 'cannot observe release pin; no database changes attempted'
  OBSERVED_RELEASE="$current"
  previous="$(get .previous)"; target="$(nullable .target)"
  RELEASE=neither
  if [[ "$(jq -cS . <<<"$current")" == "$(jq -cS . <<<"$previous")" ]]; then RELEASE=previous
  elif [[ -n "$target" && "$(jq -cS . <<<"$current")" == "$(jq -cS . <<<"$target")" ]]; then RELEASE=target
  fi
  expected="$(nullable .pinExpected)"
  if [[ "$RELEASE" == neither && -n "$expected" ]]; then
    if [[ -n "$(jq -r '.imageId // empty' <<<"$expected")" && "$(jq -cS . <<<"$current")" == "$(jq -cS . <<<"$expected")" ]]; then
      RELEASE=target
    elif jq -en --argjson c "$current" --argjson p "$previous" --argjson t "$expected" --argjson recorded "${target:-null}" '
      (["versionLine","composeFileLine","imageLine","sourceHead"] | all(.[]; . as $k | $c[$k]==$p[$k] or $c[$k]==$t[$k]))
      and ((if ($t.imageId // "")=="" then ($recorded.imageId // "") else $t.imageId end) as $known | $known=="" or $c.imageId==$p.imageId or $c.imageId==$known)' >/dev/null; then
      RELEASE=partial
    fi
  fi
  [[ -n "$(get .origOid)" ]] || no_original=1
  local phase at
  phase="$(get .phase)"; at="$(nullable .at)"
  if [[ "$phase" == reconcile-needed ]]; then
    phase="$(nullable .reconcileFrom)"; at="$(nullable .reconcileAt)"
    [[ -n "$phase" && "$phase" != reconcile-needed ]] || return 1
  fi
  [[ "$phase" != abort-intent ]] || at="$(get .abort.observation)"
  if [[ "$phase" == collect-failed && "$(jq -r 'any(.history[]; .phase=="collect-drop-intent")' <<<"$STATE")" != true ]]; then return 1; fi
  recovery_accepts "$phase" "$CLASS" "$(get .cleanup)" "$at" "$no_original" "$(jq -r '.abort.dropIntent // 0' <<<"$STATE")" || return 1
  if [[ "$phase" == abort-* ]]; then [[ "$RELEASE" == previous ]] || return 1
  else
    case "$phase" in
      quiesce|create-intent|created|collect-drop-intent|collect-failed) [[ "$RELEASE" == previous ]] || return 1 ;;
      collected|pin-intent|pin-failed|swap-failed) [[ "$RELEASE" != neither ]] || return 1 ;;
      pinned|swap-intent|swap-1|restored|recovery-intent|recovering|recovery-failed|reopen-*|app-start-intent|caddy-start-intent|smoke-intent|pre-*|floor-written|cleanup-failed)
        if [[ -n "$target" ]]; then [[ "$RELEASE" == target || ( "$target" == "$previous" && "$RELEASE" == previous ) ]] || return 1
        else [[ "$RELEASE" == previous ]] || return 1; fi ;;
      *) return 1 ;;
    esac
  fi
}
project_init() {
  PROJECT="$(env_value "$DIR/.env" COMPOSE_PROJECT_NAME || true)"
  if [[ -z "$PROJECT" ]]; then PROJECT="$(dc config --format json | jq -er .name)"; fi
  [[ "$PROJECT" =~ ^[a-z0-9][a-z0-9_-]*$ ]] || fail 'invalid Compose project'
}
recovery_nonce_check() {
  local ids id nonce
  ids="$(recovery_ids)" || return 1
  while IFS= read -r id; do
    [[ -n "$id" ]] || continue
    nonce="$(docker inspect --format '{{index .Config.Labels "fold.recovery.nonce"}}' "$id")" || return 1
    [[ "$nonce" == "$(get .nonce)" ]] || { echo "foreign recovery container $id nonce=$nonce; nothing started or removed" >&2; return 1; }
  done <<<"$ids"
}
remove_recovery() {
  local ids id
  recovery_nonce_check || return 1
  ids="$(recovery_ids)" || return 1
  while IFS= read -r id; do [[ -z "$id" ]] || docker rm -f "$id" >/dev/null || return 1; done <<<"$ids"
  ids="$(recovery_ids)" || return 1
  [[ -z "$ids" ]]
}
service_running() {
  local ids id state active=1
  ids="$(dc ps -a -q "$1")" || return 2
  while IFS= read -r id; do
    [[ -n "$id" ]] || continue
    state="$(docker inspect --format '{{or .State.Running .State.Restarting}}' "$id")" || return 2
    case "$state" in true) active=0 ;; false) ;; *) return 2 ;; esac
  done <<<"$ids"
  return "$active"
}
workers_count() { pg postgres -c "SELECT count(*) FROM pg_stat_activity WHERE datname='fold' AND backend_type='client backend' AND pid<>pg_backend_pid()"; }
wait_clients() {
  local db_pred="$1" count end=$((SECONDS+WAIT))
  while :; do
    count="$(pg postgres -c "SELECT count(*) FROM pg_stat_activity WHERE ($db_pred) AND backend_type='client backend' AND pid<>pg_backend_pid()")" || return 1
    if [[ "$count" == 0 ]]; then printf 'restore.sh: observed W=0 (%s)\n' "$db_pred"; return 0; fi
    (( SECONDS < end )) || { echo "client backends remain: $count; inspect pg_stat_activity, including backup.sh pg_dump" >&2; return 1; }
    sleep 1
  done
}
# shellcheck disable=SC2016 # $1 belongs to the child Bash.
port_open() { timeout 2 bash -c 'exec 3<>/dev/tcp/127.0.0.1/"$1"' _ "$1" >/dev/null 2>&1; }
ports_closed() {
  local port
  for port in $(jq -r '.publicPorts[]' <<<"$STATE") 18080; do
    [[ "$port" =~ ^[0-9]+$ ]] || return 1
    if port_open "$port"; then echo "port $port still accepting connections" >&2; return 1; fi
  done
}
normal_stopped() {
  local service rc
  for service in caddy app; do
    rc=0; service_running "$service" || rc=$?
    [[ "$rc" == 1 ]] || return 1
  done
}
# Closing is allowed even when database identities are inconsistent. It never
# issues SQL writes. Foreign recovery containers are refused, not removed.
close_site() {
  local previous_phase
  previous_phase="$(get .phase)"
  recovery_nonce_check || return 1
  if ! dc stop caddy || ! dc stop app || ! remove_recovery || ! normal_stopped || ! ports_closed \
    || ! wait_clients "datname='fold'"; then
    STATE="$(jq --arg phase "$previous_phase" --arg now "$(date -u +%FT%TZ)" '.closeFailed={phase:$phase,time:$now}' <<<"$STATE")"
    store
    echo "restore.sh: close-failed; phase kept ($previous_phase); nothing started. Run $DIR/restore.sh --status" >&2
    return 1
  fi
  STATE="$(jq --arg now "$(date -u +%FT%TZ)" '.closedAt=$now | .closeFailed=null | .history += [{phase:"closed-at",time:$now,cleanup:.cleanup}]' <<<"$STATE")"
  store
}
reconcile_needed() {
  # E2 closes mistakenly started services even in reconcile-needed.
  close_site || exit 1
  if [[ "$(get .phase)" != reconcile-needed ]]; then
    STATE="$(jq --arg prior "$(get .phase)" --arg at "$(nullable .at)" '.reconcileFrom=$prior | .reconcileAt=$at' <<<"$STATE")"
    record reconcile-needed
  fi
  printf 'restore.sh: reconcile-needed; classification=%s release=%s\n%s\n' "$CLASS" "$RELEASE" "$DBS" >&2
  fail 'database identity or release changed without its recorded intent. No database was written. Retain the lock; inspect names, oids, sizes and ALLOW_CONNECTIONS with --status. --abort is refused'
}
status() {
  local ids id phase w accepted=no
  if observe; then accepted=yes; fi
  phase="$(get .phase)"
  printf 'phase=%s classification=%s release=%s count=%s cleanup=%s\n' "$phase" "$CLASS" "$RELEASE" "$(get .count)" "$(get .cleanup)"
  if [[ "$phase" == reconcile-needed ]]; then
    printf 'reconcileFrom=%s recheckAccepted=%s; --resume only rechecks and restores that phase\n' "$(nullable .reconcileFrom)" "$accepted"
  fi
  printf 'operations holder (informational; observations may be mid-change): %s\n' "$(cat "$DIR/ops.owner" 2>/dev/null || true)"
  printf 'databases: name|oid|bytes|ALLOW_CONNECTIONS\n%s\n' "$DBS"
  dc ps -a
  for id in $(dc ps -a -q app caddy); do
    docker inspect --format '{{.Id}} {{index .Config.Labels "com.docker.compose.service"}} {{.State.Status}} running={{.State.Running}} restart={{.HostConfig.RestartPolicy.Name}} image={{.Image}}' "$id"
  done
  ids="$(recovery_ids)" || fail 'cannot observe recovery containers; nothing started'
  while IFS= read -r id; do
    [[ -z "$id" ]] || docker inspect --format '{{.Id}} {{.State.Status}} running={{.State.Running}} restart={{.HostConfig.RestartPolicy.Name}} image={{.Image}} nonce={{index .Config.Labels "fold.recovery.nonce"}}' "$id"
  done <<<"$ids"
  for id in $(jq -r '.publicPorts[]' <<<"$STATE") 18080; do
    if port_open "$id"; then echo "port $id open"; else echo "port $id refusing"; fi
  done
  w="$(workers_count)"; echo "W=$w"
  case "$phase" in
    pre-drop-intent|pre-dropped|floor-written|cleanup-failed|abort-start-intent|abort-smoke-intent) ;;
    *) if service_running app || service_running caddy; then echo 'WARNING: normal services are running in a phase requiring closure; run --resume, or --abort for an abort phase'; fi ;;
  esac
  [[ "$(jq -r '.closeFailed // empty' <<<"$STATE")" == '' ]] || echo 'close-failed: no recovery instance may start until closure is verified'
}

preflight() {
  local original locale space size dump_size current level=0 floor=0 head="" ports extra app_id deployed deployed_level target_image="" image_before=""
  [[ -r "$DUMP" && -f "$DUMP" ]] || fail 'archive must be a readable regular file'
  DUMP="$(realpath "$DUMP")"
  dc exec -T db pg_restore -l <"$DUMP" >/dev/null || fail 'unreadable archive; nothing stopped'
  extra="$(pg postgres -c "SELECT datname FROM pg_database WHERE datname ~ '^fold_(pre_)?restore_[0-9a-f]{16}$'")"
  [[ -z "$extra" ]] || fail "leftover nonce databases: $extra. Inspect pg_database names, oids, sizes and datallowconn; inspect each archive before explicitly dropping a verified abandoned database. Nothing stopped"
  original="$(pg postgres -c "SELECT oid FROM pg_database WHERE datname='fold'")"
  locale="$(pg postgres -c "SELECT json_build_object('encoding',pg_encoding_to_char(encoding),'provider',datlocprovider,'collate',datcollate,'ctype',datctype) FROM pg_database WHERE datname=CASE WHEN EXISTS(SELECT FROM pg_database WHERE datname='fold') THEN 'fold' ELSE 'template1' END")"
  jq -e '.encoding=="UTF8" and .provider=="c" and (.collate|length>0) and (.ctype|length>0)' <<<"$locale" >/dev/null || fail 'only UTF8 databases with libc locale provider c are supported; nothing stopped'
  size="$(pg postgres -c "SELECT COALESCE((SELECT pg_database_size(oid) FROM pg_database WHERE datname='fold'),0)")"
  # shellcheck disable=SC2016 # PGDATA belongs to the database container.
  space="$(dc exec -T db sh -c 'df -Pk "$PGDATA"' | awk 'NR==2 {printf "%.0f",$4*1024}')"
  dump_size="$(stat -c %s "$DUMP")"
  [[ "$space" =~ ^[0-9]+$ && "$size" =~ ^[0-9]+$ ]] || fail 'cannot measure free database space'
  (( space >= 2*size+10*dump_size )) || fail 'not enough free space; need 2 x live database + 10 x archive; nothing stopped'
  if port_open 18080; then fail '127.0.0.1:18080 is busy; nothing stopped'; fi
  # Read the current floor and deployed identity before a target pull can
  # replace a local tag. A changed registry tag must never lower the floor or
  # be recorded as the image that was actually running before recovery.
  floor="$(host_recovery_floor "$DIR")"
  current="$(release_snapshot)"
  app_id="$(dc ps -a -q app)"
  if [[ -n "$app_id" ]]; then
    [[ "$app_id" != *$'\n'* ]] || fail 'cannot identify one deployed app; nothing stopped'
    deployed="$(docker inspect --format '{{.Image}}' "$app_id")" || fail 'cannot inspect deployed image; nothing stopped'
    [[ "$deployed" == "$(jq -er .imageId <<<"$current")" ]] || fail "the local pin differs from the deployed image $deployed. Recover that exact pinned image before retrying; nothing stopped"
    deployed_level="$(image_recovery_level "$deployed")"
    (( deployed_level<=floor )) || floor="$deployed_level"
  fi
  if [[ -n "$COMMIT" ]]; then
    [[ -z "$(git -C "$SOURCE" status --porcelain)" ]] || fail 'source tree is dirty; nothing stopped'
    COMMIT="$(git -C "$SOURCE" rev-parse --verify "${COMMIT}^{commit}")" || fail 'commit missing from source tree'
    head="$(git -C "$SOURCE" rev-parse HEAD)"
    level="$(commit_recovery_level "$SOURCE" "$COMMIT")"
  elif [[ -n "$IMAGE" ]]; then
    [[ -f "$SOURCE/docker-compose.yml" && -f "$SOURCE/Caddyfile" && -f "$SOURCE/.env.example" ]] || fail "image pin needs --source-dir with docker-compose.yml, Caddyfile and .env.example; nothing stopped"
    # The bootstrap pass is read-only. Resolve/pull once, after tools freeze,
    # and read policy from the resulting immutable id, never the moving tag.
    [[ "${1:-}" != validate ]] || return 0
    image_before="$(docker image inspect --format '{{.Id}}' "$IMAGE" 2>/dev/null || true)"
    local pull_rc=0 current_ref
    current_ref="$(dc config --format json | jq -er .services.app.image)"
    docker pull "$IMAGE" || pull_rc=$?
    if (( pull_rc==0 )); then target_image="$(docker image inspect --format '{{.Id}}' "$IMAGE")" || pull_rc=$?; fi
    # Pulling the currently deployed tag must not rewrite the pre-pin release
    # observation or make a pre-swap abort start the new image. Pin-only pulls
    # again and must still match the immutable target checked here.
    if [[ "$IMAGE" == "$current_ref" ]]; then
      docker image tag "$(jq -er .imageId <<<"$current")" "$IMAGE" || fail 'cannot preserve the previous local image pin; nothing stopped'
    fi
    (( pull_rc==0 )) || fail 'target image resolution/pull failed; nothing stopped'
    level="$(image_recovery_level "$target_image")"
  fi
  if [[ -n "$COMMIT$IMAGE" ]]; then require_recovery_level "$level" "$floor" || exit 1; fi
  if [[ -z "$COMMIT$IMAGE" ]]; then
    level="$(image_recovery_level "$(jq -er .imageId <<<"$current")")"
    require_recovery_level "$level" "$floor" || exit 1
  fi
  [[ "${1:-}" != validate ]] || return 0
  local caddy_id
  caddy_id="$(dc ps -a -q caddy)"
  [[ -n "$caddy_id" && "$caddy_id" != *$'\n'* ]] || fail 'cannot identify one public proxy container; nothing stopped'
  local bound_ports configured_ports
  bound_ports="$(docker inspect --format '{{json .NetworkSettings.Ports}}' "$caddy_id" | jq '[.[]?[]? | .HostPort | select(.!="") | tonumber]')"
  configured_ports="$(dc config --format json | jq '[.services.caddy.ports[]? | .published? | select(.!=null and .!="") | tonumber]')"
  ports="$(jq -n --argjson a "$bound_ports" --argjson b "$configured_ports" '$a+$b|unique')"
  local expected=null version_line compose_line image_line
  if [[ -n "$IMAGE$COMMIT" ]]; then
    version_line="$(jq -r .versionLine <<<"$current")"
    compose_line="$(jq -r .composeFileLine <<<"$current")"
    if [[ -n "$COMMIT" ]]; then
      image_line="$(git -C "$SOURCE" show "$COMMIT:docker-compose.yml" | awk '/^  app:/{a=1;next} a && /^  [^ ]/{a=0} a && /^    image:/{print}')"
      if [[ "$compose_line" != *docker-compose.build.yml* || -z "$version_line" || "$version_line" == FOLD_VERSION=latest ]]; then version_line=FOLD_VERSION=dev; fi
      compose_line="COMPOSE_FILE=$DIR/docker-compose.yml:$DIR/docker-compose.build.yml"
    else
      [[ -f "$SOURCE/docker-compose.yml" && -f "$SOURCE/Caddyfile" && -f "$SOURCE/.env.example" ]] || fail "image pin needs --source-dir with docker-compose.yml, Caddyfile and .env.example; nothing stopped"
      version_line="FOLD_VERSION=${IMAGE##*:}"
      [[ "$compose_line" != *docker-compose.build.yml* ]] || compose_line=""
      if [[ "$IMAGE" == "${FOLD_IMAGE_REPO}:"* ]]; then
        image_line="$(awk '/^  app:/{a=1;next} a && /^  [^ ]/{a=0} a && /^    image:/{print}' "$SOURCE/docker-compose.yml")"
      else image_line="    image: $IMAGE # fold-app-image"; fi
    fi
    expected="$(jq -n --arg versionLine "$version_line" --arg composeFileLine "$compose_line" --arg imageLine "$image_line" --arg imageId "$target_image" --arg sourceHead "${COMMIT:+$COMMIT}" '{versionLine:$versionLine,composeFileLine:$composeFileLine,imageLine:$imageLine,imageId:$imageId,sourceHead:$sourceHead}')"
  fi
  local nonce
  nonce="$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')"
  STATE="$(jq -n --arg nonce "$nonce" --arg dump "$DUMP" --arg hash "$(sha256sum "$DUMP" | cut -d' ' -f1)" \
    --arg start "$(date -u +%FT%TZ)" --arg orig "$original" --arg source "$SOURCE" --arg head "$head" \
    --arg image "$IMAGE" --arg commit "$COMMIT" --arg imageBefore "$image_before" --argjson level "$level" --argjson previous "$current" \
    --argjson locale "$locale" --argjson ports "$ports" --argjson expected "$expected" \
    '{format:1,nonce:$nonce,dump:$dump,dumpHash:$hash,start:$start,source:$source,sourceHead:$head,
      origOid:$orig,restoreOid:"",previous:$previous,target:null,pinExpected:$expected,image:$image,commit:$commit,imageBeforePull:$imageBefore,
      imageId:$previous.imageId,level:$level,locale:$locale,publicPorts:$ports,count:"unknown",cleanup:"none",phase:"quiesce",at:"",history:[] }')"
  R="fold_restore_${nonce:0:16}"; P="fold_pre_restore_${nonce:0:16}"
  [[ -z "$STAGED_TOOLS" ]] || publish_tools
  store exclusive
  backup_coord_release || fail 'recovery lock is durable, but backup coordination release failed; nothing stopped. Use --status'
}
# Always check name and oid immediately before DROP. No CASCADE, no force,
# no termination of other connections, and never DROP fold.
drop_exact() {
  local name="$1" expected="$2" actual
  [[ "$name" == "$R" || "$name" == "$P" ]] || return 1
  [[ -n "$expected" && "$expected" =~ ^[0-9]+$ ]] || return 1
  if [[ "$name" == "$R" ]]; then wait_clients "datname='$R'" || return 1; fi
  actual="$(pg postgres -c "SELECT oid FROM pg_database WHERE datname='$name'")" || return 1
  [[ "$actual" == "$expected" ]] || return 1
  pg postgres -c "DROP DATABASE \"$name\""
}
create_database() {
  local loc enc coll ctype oid
  STATE="$(jq '.restoreOid=""' <<<"$STATE")"
  record create-intent
  loc="$(get .locale)"
  enc="$(jq -r .encoding <<<"$loc")"; coll="$(jq -r .collate <<<"$loc")"; ctype="$(jq -r .ctype <<<"$loc")"
  # psql variables quote locale literals; none are interpolated as SQL code.
  pg postgres -v name="$R" -v enc="$enc" -v coll="$coll" -v ctype="$ctype" <<'SQL'
CREATE DATABASE :"name" OWNER church TEMPLATE template0 ENCODING :'enc' LOCALE_PROVIDER libc LC_COLLATE :'coll' LC_CTYPE :'ctype';
SQL
  fault_at after-create
  oid="$(pg postgres -c "SELECT oid FROM pg_database WHERE datname='$R'")"
  [[ "$oid" =~ ^[0-9]+$ ]] || fail 'create outcome unconfirmed; use --resume'
  STATE="$(jq --arg oid "$oid" '.restoreOid=$oid' <<<"$STATE")"
  record created
}
collect_failed() {
  record collect-drop-intent
  fault_at before-collect-drop
  if ! drop_exact "$R" "$(get .restoreOid)"; then fail 'collection failed; restore database drop unconfirmed; services stopped, use --resume or --abort'; fi
  fault_at after-collect-drop
  record collect-failed
  fail 'archive restore failed; original database untouched; services stopped. Run --abort to reopen the original or --resume to retry'
}
collect() {
  local count
  # A killed docker exec client can leave pg_restore or the count COPY alive
  # in Postgres. Never restore into or drop R while that backend still owns it.
  wait_clients "datname IN ('fold','$R')" || fail 'restore database clients have not drained; phase kept, no restore or drop sent. Wait, then --resume'
  if ! dc exec -T db pg_restore -U church -d "$R" --no-owner --no-acl --single-transaction --exit-on-error <"$DUMP"; then collect_failed; fi
  # Fold's user deletion is soft deletion (apiAdminDeleteUser tx.Delete(u),
  # User embeds gorm.DeletedAt). No production path hard-deletes users.
  # One stream, no ids on disk. Selection never uses audit timestamp.
  count=unknown
  if [[ -n "$(get .origOid)" ]]; then
    if ! count="$(
      set -o pipefail
      {
        printf 'BEGIN;\nCREATE TEMP TABLE erased_ids(id bigint PRIMARY KEY);\nCOPY erased_ids FROM STDIN;\n'
        pg fold -c 'COPY (SELECT id FROM users WHERE deleted_at IS NOT NULL) TO STDOUT' || exit 1
        printf '\\.\nSELECT count(*) FROM users u JOIN erased_ids e USING(id) WHERE u.deleted_at IS NULL;\nCOMMIT;\n'
      } | pg "$R" -q
    )"; then count=unknown; fi
  fi
  [[ "$count" == unknown || "$count" =~ ^[0-9]+$ ]] || count=unknown
  STATE="$(jq --arg count "$count" '.count=$count' <<<"$STATE")"
  record collected
}
pin() {
  local current target level floor source_head="" mode_args=() domain
  if [[ -z "$IMAGE$COMMIT" ]]; then
    STATE="$(jq '.target=.previous | .imageId=.previous.imageId' <<<"$STATE")"
    record pinned; return
  fi
  record pin-intent
  fault_at before-pin
  if [[ -n "$COMMIT" ]]; then
    source_head="$(git -C "$SOURCE" rev-parse HEAD)"
    [[ "$source_head" == "$(get .sourceHead)" || "$source_head" == "$COMMIT" ]] || reconcile_needed
    [[ -z "$(git -C "$SOURCE" status --porcelain)" ]] || fail 'source tree changed during recovery; pin refused'
    git -C "$SOURCE" checkout --detach "$COMMIT"
    mode_args=(--allow-onbox-build)
  else mode_args=(--image "$IMAGE"); fi
  # Derive desired pin fields from the target, before any external change.
  # A killed partial pin can be retried, but can never authorize an abort.
  domain="$(env_value "$DIR/.env" DOMAIN)"
  (( ! DRILL )) || mode_args+=(--allow-os)
  if ! "$SCRIPT_DIR/provision.sh" --pin-only --recovery-nonce "$(get .nonce)" --skip-harden \
    --deploy-dir "$DIR" --source-dir "$SOURCE" --domain "$domain" "${mode_args[@]}"; then
    record pin-failed; fail 'pin failed; site stays closed. Run --resume'
  fi
  current="$(release_snapshot)" || { record pin-failed; fail 'pin observation failed'; }
  level="$(image_recovery_level "$(jq -er .imageId <<<"$current")")" || { record pin-failed; fail 'pin version unreadable'; }
  [[ "$level" == "$(get .level)" ]] || { record pin-failed; fail 'resulting image level differs from checked target; no services started'; }
  floor="$(host_recovery_floor "$DIR")" || { record pin-failed; fail 'cannot observe host recovery floor'; }
  require_recovery_level "$level" "$floor" || { record pin-failed; fail 'pinned image below host floor'; }
  local expected
  expected="$(get .pinExpected)"
  if ! jq -en --argjson c "$current" --argjson t "$expected" '
    ($c | del(.imageId))==($t | del(.imageId)) and ($t.imageId=="" or $c.imageId==$t.imageId)' >/dev/null; then
    record pin-failed; fail 'resulting pin differs from the intended release; services stopped'
  fi
  target="$(nullable .target)"
  if [[ -n "$target" && "$(jq -cS . <<<"$current")" != "$(jq -cS . <<<"$target")" ]]; then record pin-failed; fail 'target release identity changed during retry'; fi
  STATE="$(jq --argjson target "$current" '.target=$target | .imageId=$target.imageId' <<<"$STATE")"
  record pinned
  fault_at after-pin
}
swap() {
  local original
  original="$(get .origOid)"
  observe || reconcile_needed
  record swap-intent
  if [[ -n "$original" ]]; then pg postgres -c 'ALTER DATABASE fold WITH ALLOW_CONNECTIONS false'; fi
  pause_at before-swap
  if ! wait_clients "datname IN ('fold','$R')"; then
    if [[ -n "$original" ]]; then pg postgres -c 'ALTER DATABASE fold WITH ALLOW_CONNECTIONS true'; fi
    record swap-failed; fail 'swap-failed; original connections re-enabled; services stopped. Use --resume or --abort'
  fi
  fault_at before-swap
  if [[ "${RESTORE_DRILL_FORCE_TWO_STEP:-0}" == 1 ]]; then
    [[ -z "$original" ]] || pg postgres -c "ALTER DATABASE fold RENAME TO \"$P\""
    record swap-1; fault_at half
    pg postgres -c "ALTER DATABASE \"$R\" RENAME TO fold" || true
  else
    {
      [[ -z "$original" ]] || printf 'ALTER DATABASE fold RENAME TO "%s";\n' "$P"
      printf 'ALTER DATABASE "%s" RENAME TO fold;\n' "$R"
    } | pg postgres --single-transaction || true
  fi
  fault_at swap-ack-lost
  observe || reconcile_needed
  if [[ "$CLASS" == A || ( "$CLASS" == C && -z "$original" ) ]]; then record restored
  elif [[ "$CLASS" == B ]]; then
    [[ -z "$original" ]] || pg postgres -c 'ALTER DATABASE fold WITH ALLOW_CONNECTIONS true'
    record swap-failed; fail 'swap transaction did not commit; services stopped. Run --resume'
  else reconcile_needed; fi
  fault_at after-swap
}
health() {
  local id="$1" end=$((SECONDS+HEALTH_WAIT))
  while (( SECONDS < end )); do
    if docker exec "$id" /fold healthcheck >/dev/null 2>&1; then return 0; fi
    sleep 1
  done
  return 1
}
assert_image() {
  [[ "$(docker inspect --format '{{.Image}}' "$1")" == "$2" ]]
}
recovery_start() {
  local ids id count=0 reuse="" image nonce
  normal_stopped || fail 'service exclusion: app and caddy must be stopped; nothing started'
  recovery_nonce_check || fail 'foreign recovery container; nothing changed'
  image="$(get .imageId)"; nonce="$(get .nonce)"
  ids="$(recovery_ids)"
  while IFS= read -r id; do
    [[ -n "$id" ]] || continue; count=$((count+1))
    if assert_image "$id" "$image" \
      && [[ "$(docker inspect --format '{{.HostConfig.RestartPolicy.Name}}|{{.State.Running}}|{{if .State.Health}}{{.State.Health.Status}}{{end}}' "$id")" == 'no|true|healthy' ]]; then reuse="$id"; fi
  done <<<"$ids"
  record recovery-intent
  if (( count != 1 )) || [[ -z "$reuse" ]]; then
    remove_recovery || fail 'could not remove stopped/unhealthy recovery instance'
    wait_clients "datname='fold'" || { record recovery-failed; fail 'service exclusion: external client backends remain'; }
    ports_closed || { record recovery-failed; fail 'service exclusion: port still open'; }
    local -a cmd=(docker compose --project-directory "$DIR" -f "$DIR/docker-compose.yml" -f "$DIR/compose.recovery.yml" --profile recovery)
    if [[ "${RESTORE_DRILL_FAULT:-}" == bad-recovery ]]; then
      # Explicit test-only environment overlay. Never affect the normal app.
      printf 'services:\n  recovery:\n    environment:\n      DATABASE_URL: postgres://church:wrong@db/fold\n' >"$DIR/.drill-bad-recovery.yml"
      cmd+=(-f "$DIR/.drill-bad-recovery.yml")
    fi
    if ! FOLD_RECOVERY_IMAGE="$image" FOLD_RECOVERY_NONCE="$nonce" "${cmd[@]}" up -d --no-deps recovery; then
      remove_recovery || true; record recovery-failed; fail "recovery boot failed; inspect docker compose logs recovery in $DIR; app and caddy remain stopped"
    fi
    fault_at after-recovery-create
    ids="$(recovery_ids)"
    [[ "$ids" != *$'\n'* && -n "$ids" ]] || { remove_recovery || true; record recovery-failed; fail 'expected one recovery instance'; }
    reuse="$ids"
  fi
  if ! assert_image "$reuse" "$image" \
    || [[ "$(docker inspect --format '{{.HostConfig.RestartPolicy.Name}}' "$reuse")" != no ]] \
    || ! health "$reuse"; then
    remove_recovery || true; record recovery-failed; fail "recovery health failed; inspect docker logs $reuse; app and caddy remain stopped. Use --resume"
  fi
  record recovering
  pause_at recovering
  printf 'Accounts this archive would revive: %s\n' "$(get .count)"
  cat <<HELP
Open a private tunnel: ssh -N -L 18080:127.0.0.1:18080 <admin>@<droplet>
Open http://localhost:18080 in Chrome or Firefox, sign in as admin, and delete
accounts erased after the backup using the church's records. No list is saved.
An unknown count means the current database could not be compared. Neither the
daily sweep nor a count can reconstruct erasures absent from the backup.
Then run $DIR/restore.sh --reopen --checked-church-records
HELP
}
start_normal() {
  local image="$1" ids id config_ref resolved
  ids="$(recovery_ids)" || return 1
  [[ -z "$ids" ]] || return 1
  config_ref="$(dc config --format json | jq -er .services.app.image)" || return 1
  resolved="$(docker image inspect --format '{{.Id}}' "$config_ref")" || return 1
  [[ "$resolved" == "$image" ]] || return 1
  # An explicit id override prevents a mutable tag from selecting a different
  # image between inspection and container creation. No pull or build on reopen.
  FOLD_RECOVERY_IMAGE="$image" dc -f "$DIR/docker-compose.yml" -f "$DIR/.recovery-app-pin.yml" up -d --no-deps --pull never app || return 1
  ids="$(dc ps -a -q app)" || return 1
  [[ -n "$ids" && "$ids" != *$'\n'* ]] || return 1
  id="$ids"
  assert_image "$id" "$image" && health "$id"
}
write_app_pin() {
  local tmp
  # No secrets; this file is never included by ordinary docker compose up.
  tmp="$(mktemp "$DIR/.recovery-app-pin.XXXXXX")" || return 1
  # shellcheck disable=SC2016 # Compose resolves this explicit image-id override.
  if ! { printf 'services:\n  app:\n    image: ${FOLD_RECOVERY_IMAGE:?}\n    pull_policy: never\n' >"$tmp" \
    && chmod 600 "$tmp" && chown 0:0 "$tmp" && sync -f "$tmp" \
    && mv -f "$tmp" "$DIR/.recovery-app-pin.yml" && sync -f "$DIR"; }; then
    rm -f "$tmp"; return 1
  fi
}

smoke() { "$SCRIPT_DIR/smoke.sh" --deploy-dir "$DIR" --source-dir "$SOURCE"; }
reopen_failed() {
  close_site || exit 1
  record reopen-failed
  fail 'reopen failed; site closed; cleanup intent kept. Use --resume to return to private recovery'
}
finish_reopen() {
  local floor
  floor="$(host_recovery_floor "$DIR")" || fail 'cannot observe host recovery floor; nothing started'
  require_recovery_level "$(get .level)" "$floor" || fail 'recorded recovery image is below the host floor; nothing started'
  write_app_pin
  record reopen-intent
  remove_recovery || reopen_failed
  record app-start-intent
  start_normal "$(get .imageId)" || reopen_failed
  fault_at after-app-start
  record caddy-start-intent
  dc up -d --no-deps caddy || reopen_failed
  fault_at after-caddy-start
  record smoke-intent
  smoke || reopen_failed
  finish_cleanup
}

# These phases already crossed public smoke. Preserve that durable phase
# through retries, even if this helper is killed while starting a service.
resume_cleanup() {
  local floor running id
  floor="$(host_recovery_floor "$DIR")" || fail 'cannot observe recovery floor'
  require_recovery_level "$(get .level)" "$floor" || exit 1
  write_app_pin
  remove_recovery || reopen_failed
  running=0; service_running app || running=$?
  case "$running" in
    0)
      id="$(dc ps -a -q app)"
      if ! assert_image "$id" "$(get .imageId)" || ! health "$id"; then reopen_failed; fi ;;
    1) start_normal "$(get .imageId)" || reopen_failed ;;
    *) reopen_failed ;;
  esac
  fault_at after-app-start
  running=0; service_running caddy || running=$?
  case "$running" in 0) ;; 1) dc up -d --no-deps caddy || reopen_failed ;; *) reopen_failed ;; esac
  fault_at after-caddy-start
  smoke || reopen_failed
  finish_cleanup
}

finish_cleanup() {
  local cleanup
  cleanup="$(get .cleanup)"
  if [[ "$cleanup" != dropped ]]; then
    if [[ "$cleanup" == none ]]; then STATE="$(jq '.cleanup="drop-intent"' <<<"$STATE")"; record pre-drop-intent; fi
    fault_at before-drop
    observe || reconcile_needed
    if [[ "$CLASS" == A ]]; then
      if ! drop_exact "$P" "$(get .origOid)"; then record cleanup-failed drop; fail 'cleanup-failed at drop; site open, lock kept. Use --resume'; fi
    elif [[ "$CLASS" != C ]]; then reconcile_needed; fi
    fault_at after-drop
    STATE="$(jq '.cleanup="dropped"' <<<"$STATE")"; record pre-dropped
  fi
  if ! write_recovery_floor "$DIR" "$(get .level)"; then record cleanup-failed floor; fail 'cleanup-failed at floor; site open, lock kept. Use --resume'; fi
  record floor-written
  fault_at after-floor
  remove_stale_pin_saves "$DIR" || fail 'cannot remove stale pin snapshots; keep the lock and retry --resume'
  remove_lock
  echo 'restore.sh: reopened; recovery lock removed after smoke, database cleanup and monotone floor write'
}
abort_failed() {
  local at="$1"
  close_site || exit 1
  record abort-failed "$at"
  fail "abort-failed at=$at; prior intent kept, site closed. Continue with --abort"
}
abort_continue() {
  local phase at original restored_oid
  phase="$(get .phase)"; at="$(nullable .at)"
  [[ "$RELEASE" == previous ]] || reconcile_needed
  if [[ "$phase" != abort-* ]]; then
    recovery_abort_allowed "$phase" "$CLASS" "$RELEASE" || fail 'abort refused after swap, pin change, or reconciliation failure; use --resume or inspect --status'
    restored_oid="$(get .restoreOid)"
    if [[ "$CLASS" == Q+ ]]; then restored_oid="$(awk -F '|' -v r="$R" '$1==r{print $2}' <<<"$DBS")"; fi
    local allowed
    allowed="$(pg postgres -c "SELECT datallowconn FROM pg_database WHERE datname='fold'")"
    STATE="$(jq --arg allowed "$allowed" --arg observation "$CLASS" --arg oid "$restored_oid" --arg phase "$phase" '.abort={from:$phase,observation:$observation,restoreOid:$oid,dropIntent:0,foldAllowConnections:$allowed}' <<<"$STATE")"
    record abort-intent
    fault_at after-abort-intent
    phase=abort-intent
  fi
  if [[ "$phase" == abort-intent ]]; then
    original="$(get .origOid)"
    [[ -z "$original" ]] || pg postgres -c 'ALTER DATABASE fold WITH ALLOW_CONNECTIONS true'
    if [[ "$(get .abort.observation)" != Q ]]; then
      STATE="$(jq '.abort.dropIntent=1' <<<"$STATE")"; record abort-drop-intent
      phase=abort-drop-intent
    else record abort-dropped; phase=abort-dropped; fi
  fi
  if [[ "$phase" == abort-drop-intent || ( "$phase" == abort-failed && "$at" == drop ) ]]; then
    fault_at before-abort-drop
    # Observe again. Q is authorized only by the kept abort drop intent.
    observe || reconcile_needed
    if [[ "$CLASS" != Q ]]; then
      if ! drop_exact "$R" "$(get .abort.restoreOid)"; then abort_failed drop; fi
      if [[ "${RESTORE_DRILL_FAULT:-}" == abort-drop-ack-lost ]]; then abort_failed drop; fi
    fi
    fault_at after-abort-drop
    record abort-dropped
  fi
  remove_recovery || abort_failed start
  write_app_pin
  record abort-start-intent
  start_normal "$(get .previous.imageId)" || abort_failed start
  dc up -d --no-deps caddy || abort_failed start
  fault_at after-abort-start
  record abort-smoke-intent
  smoke || abort_failed smoke
  remove_stale_pin_saves "$DIR" || fail 'cannot remove stale pin snapshots; keep the lock and retry --abort'
  remove_lock
  echo 'restore.sh: abort complete; original database and release reopened'
}
resume() {
  local phase cleanup roid floor
  phase="$(get .phase)"
  case "$phase" in abort-*) fail 'abort is in progress; continue with --abort, never --resume' ;; esac
  observe || reconcile_needed
  case "$phase" in
    pre-drop-intent|pre-dropped|floor-written|cleanup-failed)
      # The public site is meant to be open. Repeat health and smoke first.
      resume_cleanup; return ;;
    reopen-intent|app-start-intent|caddy-start-intent|smoke-intent|reopen-failed)
      close_site || exit 1
      cleanup="$(get .cleanup)"
      if [[ "$cleanup" == drop-intent && "$CLASS" == C ]]; then STATE="$(jq '.cleanup="dropped"' <<<"$STATE")"; fi
      record reopen-closed; recovery_start; return ;;
  esac
  # A healthy private recovery instance is reused only when normal services
  # are both stopped. Its own connections are not an exclusion failure.
  if [[ "$phase" == recovering || "$phase" == recovery-intent ]]; then
    if normal_stopped && [[ -n "$(recovery_ids)" ]]; then
      recovery_start; return
    fi
  fi
  close_site || exit 1
  case "$phase" in
    quiesce|create-intent)
      if [[ "$CLASS" == Q+ ]]; then
        roid="$(awk -F '|' -v r="$R" '$1==r {print $2}' <<<"$DBS")"
        STATE="$(jq --arg oid "$roid" '.restoreOid=$oid' <<<"$STATE")"
        record collect-drop-intent
        drop_exact "$R" "$roid" || fail 'unrecorded create cleanup failed; use --resume'
        record collect-failed
      fi
      create_database; collect; pin; swap ;;
    created) collect; pin; swap ;;
    collect-drop-intent)
      if [[ "$CLASS" == B ]]; then drop_exact "$R" "$(get .restoreOid)" || fail 'restore database drop unconfirmed; use --resume'; fi
      record collect-failed
      create_database; collect; pin; swap ;;
    collect-failed) create_database; collect; pin; swap ;;
    collected|pin-intent|pin-failed|pinned|swap-failed)
      if [[ "$RELEASE" == target ]]; then
        [[ "$(image_recovery_level "$(jq -er .imageId <<<"$OBSERVED_RELEASE")")" == "$(get .level)" ]] || fail 'observed target image level changed'
        floor="$(host_recovery_floor "$DIR")" || fail 'cannot observe recovery floor'
        require_recovery_level "$(get .level)" "$floor" || exit 1
        STATE="$(jq --argjson target "$OBSERVED_RELEASE" '.target=$target | .imageId=$target.imageId' <<<"$STATE")"
        record pinned
      else pin; fi
      swap ;;
    swap-intent|swap-1)
      case "$CLASS" in
        B) swap ;;
        H) pg postgres -c "ALTER DATABASE \"$R\" RENAME TO fold"; observe || reconcile_needed; record restored ;;
        A|C) record restored ;;
        *) reconcile_needed ;;
      esac ;;
    restored|recovery-intent|recovering|recovery-failed|reopen-closed) ;;
    *) reconcile_needed ;;
  esac
  cleanup="$(get .cleanup)"
  if [[ "$cleanup" == drop-intent && "$CLASS" == C ]]; then STATE="$(jq '.cleanup="dropped"' <<<"$STATE")"; store; fi
  recovery_start
}

if (( DRY )); then
  echo "dry-run: root required; complete tool set checked; no lock acquired, files written, images pulled, or Docker commands run"
  echo "dry-run: $MODE in $DIR; archive, free space, port, database identities, service exclusion and image recovery level remain unverified until a real run"
  if [[ -n "$COMMIT" ]]; then dry_run_recovery_check "$DIR" build "$SOURCE" "$COMMIT"; fi
  if [[ "$MODE" == dump ]]; then
    [[ -n "$DUMP" ]] || fail '--dump FILE is required'
    echo 'dry-run: durable quiesce -> create-intent -> single-transaction pg_restore to fold_restore_<nonce> -> snapshot-safe deleted-id pipe and count -> optional nonce-scoped pin-only -> whole database swap -> labelled restart-no recovery with FOLD_WORKERS=0'
  else echo 'dry-run: observe recovery.lock, database names/oids, release values and labelled services before choosing any action'; fi
  echo 'dry-run: --reopen requires recovering and, for nonzero or unknown count, --checked-church-records; smoke, exact-oid old database drop, monotone floor, lock removed last'
  exit 0
fi
if [[ "$MODE" == dump ]]; then
  [[ ! -e "$DIR/recovery.lock" && ! -L "$DIR/recovery.lock" ]] || fail "recovery.lock exists; run $DIR/restore.sh --status then --resume or --abort"
  backup_coord_acquire "$DIR" "$WAIT" restore || exit $?
fi
# Cheap bootstrap observations precede staging. Full preflight (including
# the single target pull) runs in that staged copy before it is published.
if [[ "$MODE" == dump && "$SCRIPT_DIR" != "$DIR/recovery-tools" && -z "$STAGED_TOOLS" ]]; then
  [[ ! -e "$DIR/recovery.lock" && ! -L "$DIR/recovery.lock" ]] || fail "recovery.lock exists; run $DIR/restore.sh --status then --resume or --abort"
  [[ -n "$DUMP" && -f "$DIR/docker-compose.yml" && -f "$DIR/.env" ]] || fail '--dump requires an archive and deployed docker-compose.yml/.env'
  preflight validate
fi
if [[ "$MODE" != status && "$SCRIPT_DIR" != "$DIR/recovery-tools" && -z "$STAGED_TOOLS" ]]; then
  stage_tools
  if [[ "$MODE" == dump ]]; then exec "$STAGED_TOOLS/restore.sh" "${ORIGINAL_ARGS[@]}"; fi
  publish_tools
  exec "$SCRIPT_DIR/restore.sh" "${ORIGINAL_ARGS[@]}"
fi
[[ -f "$DIR/docker-compose.yml" && -f "$DIR/.env" ]] || fail 'missing deployed docker-compose.yml or .env; run provision.sh first'
project_init
if [[ "$MODE" == status ]]; then read_lock; status; exit 0; fi
if [[ "$MODE" == dump ]]; then
  [[ ! -e "$DIR/recovery.lock" && ! -L "$DIR/recovery.lock" ]] || fail "recovery.lock exists; run $DIR/restore.sh --status then --resume or --abort"
  [[ -n "$DUMP" ]] || fail '--dump FILE is required'
  preflight
  close_site || exit 1
  create_database; collect; pin; swap; recovery_start
  if (( REOPEN_NONE )); then
    if [[ "$(get .count)" == 0 ]]; then finish_reopen
    else
      echo "SITE IS CLOSED: count=$(get .count); automatic reopen refused; remain in private recovery. Exit 3."
      exit 3
    fi
  fi
  exit 0
fi
read_lock
# E2 takes precedence over resuming a database step. In an interrupted
# reopen, even a tag/config observation failure must not leave workers live.
case "$(get .phase)" in
  pre-drop-intent|pre-dropped|floor-written|cleanup-failed|abort-start-intent|abort-smoke-intent) ;;
  abort-failed)
    if [[ "$(nullable .at)" == drop ]] && ! normal_stopped; then close_site || exit 1; fi ;;
  *) if ! normal_stopped; then close_site || exit 1; fi ;;
esac
if ! observe; then reconcile_needed; fi
# Reconciliation only rechecks the preserved phase. A repaired observation
# re-enables that phase in the lock; another explicit invocation does its work.
if [[ "$MODE" == resume && "$(get .phase)" == reconcile-needed ]]; then
  prior="$(get .reconcileFrom)"; prior_at="$(nullable .reconcileAt)"
  record "$prior" "$prior_at"
  echo "restore.sh: observations accepted; restored phase $prior. No database was written or service started. Run --resume again, or --abort if that phase permits it."
  exit 0
fi
# Only steps that restore/recollect need the archive bytes. A reboot during
# private recovery or cleanup must remain resumable after archive expiry/loss.
if [[ "$MODE" == resume ]]; then
  case "$(get .phase)" in
    quiesce|create-intent|created|collect-drop-intent|collect-failed)
      [[ -f "$DUMP" && -r "$DUMP" && "$(sha256sum "$DUMP" | cut -d' ' -f1)" == "$(get .dumpHash)" ]] || fail 'resume requires the same archive sha256 recorded in recovery.lock' ;;
  esac
  resume
elif [[ "$MODE" == abort ]]; then
  case "$(get .phase)" in
    abort-*) ;;
    *) recovery_abort_allowed "$(get .phase)" "$CLASS" "$RELEASE" || fail 'abort refused: swap or pin changed, or phase requires reconciliation; inspect --status' ;;
  esac
  close_site || exit 1
  abort_continue
elif [[ "$MODE" == reopen ]]; then
  [[ "$(get .phase)" == recovering ]] || fail '--reopen requires recovering; interrupted reopens must use --resume first'
  echo "Accounts this archive would revive: $(get .count)"
  [[ "$(get .count)" == 0 || "$CHECKED" == 1 ]] || fail 'nonzero or unknown count requires --checked-church-records after repeating all post-backup erasures'
  normal_stopped || fail 'service exclusion: normal services running; use --resume to close first'
  finish_reopen
fi
