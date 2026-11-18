#!/usr/bin/env bash
# Run installed restore entry points with stateful, process-level fixtures.
# No Docker daemon, network service, SQL server or owner directory is used.
# shellcheck disable=SC2016 # Variables in jq/child-shell programs are intentional.
set -Eeuo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
# shellcheck source=deploy/digitalocean/checks.sh
source "$ROOT/deploy/digitalocean/checks.sh"
[[ "$(id -u)" == 0 ]] || { echo 'restore fixtures require root in the disposable dry-run container' >&2; exit 1; }
WORK="$(mktemp -d /tmp/fold-restore-cli.XXXXXX)"
CASE=setup
# shellcheck disable=SC2329 # EXIT trap.
finish() {
  local rc=$?
  if (( rc )); then
    printf 'restore CLI fixture failed: %s\n' "$CASE" >&2
    for file in out events sql calls; do [[ ! -f "$WORK/$file" ]] || tail -n 35 "$WORK/$file" >&2; done
  fi
  rm -rf "$WORK"
  exit "$rc"
}
trap finish EXIT
trap 'printf "restore CLI fixture failed at line %s: %s\n" "$LINENO" "$BASH_COMMAND" >&2' ERR
mkdir "$WORK/bin" "$WORK/source"
cp "$ROOT/deploy/digitalocean/testdata/restore-docker.sh" "$WORK/bin/docker"
cat >"$WORK/bin/timeout" <<'SHIM'
#!/usr/bin/env bash
set -euo pipefail
if [[ "$*" == *'/dev/tcp/'* ]]; then
  port="${*: -1}"
  jq -e --arg port "$port" '(.portBusy // false) or (if $port=="18080" then .recovery.exists and .recovery.running else .caddy.running end)' "$RESTORE_FIXTURE_DIR/model.json" >/dev/null
  exit
fi
exec /usr/bin/timeout "$@"
SHIM
cat >"$WORK/bin/id" <<'SHIM'
#!/usr/bin/env bash
if [[ "$*" == -u && "${RESTORE_FIXTURE_NONROOT:-0}" == 1 ]]; then echo 1000; else exec /usr/bin/id "$@"; fi
SHIM
cat >"$WORK/bin/sync" <<'SHIM'
#!/usr/bin/env bash
if [[ "${RESTORE_FIXTURE_FLOOR_FAIL:-0}" == 1 && "$*" == *recovery-policy-floor* ]]; then exit 1; fi
exec /usr/bin/sync "$@"
SHIM
chmod 755 "$WORK/bin/"*
cp "$ROOT"/{docker-compose.yml,docker-compose.build.yml,Caddyfile,.env.example} "$WORK/source/"
mkdir -p "$WORK/source/deploy"
git -C "$WORK/source" init -q
 git -C "$WORK/source" config user.name fixture
 git -C "$WORK/source" config user.email fixture@example.invalid
 git -C "$WORK/source" config core.hooksPath /dev/null
 git -C "$WORK/source" config commit.gpgsign false
 git -C "$WORK/source" add .
 git -C "$WORK/source" commit -qm 'legacy fixture'
 LOW_COMMIT="$(git -C "$WORK/source" rev-parse HEAD)"
 printf '1\n' >"$WORK/source/deploy/recovery-policy"
 git -C "$WORK/source" add .
 git -C "$WORK/source" commit -qm 'level one fixture'
 HIGH_COMMIT="$(git -C "$WORK/source" rev-parse HEAD)"
export RESTORE_FIXTURE_DIR="$WORK" PATH="$WORK/bin:$PATH"
PREVIOUS="$(printf 'sha256:%064d' 1)"
TARGET="$(printf 'sha256:%064d' 2)"
NONCE=0123456789abcdef0123456789abcdef
R=fold_restore_0123456789abcdef
P=fold_pre_restore_0123456789abcdef
DIR="$WORK/site"
ARCHIVE="$WORK/archive"
printf 'valid archive fixture\n' >"$ARCHIVE"
# Each seed is an independent deployment, including the frozen tool directory.
seed() {
  local phase="$1" observation="$2" cleanup="${3:-none}" release="${4:-previous}" original=10 restored=20
  CASE="$phase/$observation/$cleanup/$release"
  rm -rf "$DIR"; mkdir -p "$DIR/recovery-tools"
  while IFS= read -r file; do cp "$ROOT/deploy/digitalocean/$file" "$DIR/recovery-tools/"; done < <(runtime_tools)
  chmod 755 "$DIR/recovery-tools/"*.sh
  chmod 700 "$DIR/recovery-tools"
  cat >"$DIR/recovery-tools/smoke.sh" <<'SMOKE'
#!/usr/bin/env bash
printf 'smoke\n' >>"$RESTORE_FIXTURE_DIR/events"
[[ "${RESTORE_FIXTURE_SMOKE_FAIL:-0}" == 0 ]]
SMOKE
  cp "$WORK/source/docker-compose.yml" "$DIR/docker-compose.yml"
  # Preserve the other shipped roles and subnet for pin-only validation.
  sed -i 's|^    image: ghcr.io/grierj/fold:.*|    image: fixture:previous # fold-app-image|' "$DIR/docker-compose.yml"
  printf 'COMPOSE_PROJECT_NAME=restore-fixture\nDOMAIN=groups.example.org\nFOLD_VERSION=previous\n' >"$DIR/.env"
  chmod 600 "$DIR/.env"
  printf 'fold-deploy-drill-v1:%s\n' "$DIR" >"$DIR/.deploy-drill"; chmod 600 "$DIR/.deploy-drill"
  printf '1\n' >"$DIR/recovery-policy-floor"
  : >"$DIR/backup.lock"; chmod 600 "$DIR/backup.lock"
  jq -n --arg prev "$PREVIOUS" '{dbs:{},nextOid:20,count:2,app:{running:false,image:$prev},caddy:{running:false,image:$prev},recovery:{exists:false,running:false,image:$prev,nonce:"",health:"healthy"}}' >"$WORK/model.json"
  case "$observation" in
    Q) dbs='{"fold":{"oid":"10","allow":true}}' ;;
    Q+) restored=''; dbs="{\"fold\":{\"oid\":\"10\",\"allow\":true},\"$R\":{\"oid\":\"20\",\"allow\":true}}" ;;
    B) dbs="{\"fold\":{\"oid\":\"10\",\"allow\":true},\"$R\":{\"oid\":\"20\",\"allow\":true}}" ;;
    H) dbs="{\"$P\":{\"oid\":\"10\",\"allow\":false},\"$R\":{\"oid\":\"20\",\"allow\":true}}" ;;
    A) dbs="{\"fold\":{\"oid\":\"20\",\"allow\":true},\"$P\":{\"oid\":\"10\",\"allow\":false}}" ;;
    C) dbs='{"fold":{"oid":"20","allow":true}}' ;;
    invalid) dbs='{"fold":{"oid":"99","allow":true}}' ;;
    *) exit 1 ;;
  esac
  jq --argjson dbs "$dbs" '.dbs=$dbs' "$WORK/model.json" >"$WORK/next"; mv "$WORK/next" "$WORK/model.json"
  previous="$(jq -n --arg id "$PREVIOUS" '{versionLine:"FOLD_VERSION=previous",composeFileLine:"",imageLine:"    image: fixture:previous # fold-app-image",sourceHead:"",imageId:$id}')"
  target="$(jq -n --arg id "$TARGET" '{versionLine:"FOLD_VERSION=target",composeFileLine:"",imageLine:"    image: fixture:target # fold-app-image",sourceHead:"",imageId:$id}')"
  image=''; pinned="$previous"; image_id="$PREVIOUS"
  if [[ "$release" == target || "$release" == partial ]]; then
    image=fixture:target
    sed -i 's/FOLD_VERSION=previous/FOLD_VERSION=target/' "$DIR/.env"
    if [[ "$release" == target ]]; then
      sed -i 's/fixture:previous/fixture:target/' "$DIR/docker-compose.yml"; pinned="$target"; image_id="$TARGET"
    fi
  elif [[ "$release" == neither ]]; then sed -i 's/FOLD_VERSION=previous/FOLD_VERSION=unexplained/' "$DIR/.env"; fi
  jq -n --arg phase "$phase" --arg original "$original" --arg restored "$restored" --arg nonce "$NONCE" \
    --arg dump "$ARCHIVE" --arg hash "$(sha256sum "$ARCHIVE" | cut -d' ' -f1)" --arg source "$WORK/source" \
    --argjson previous "$previous" --argjson target "$pinned" --argjson expected "$target" \
    --arg image "$image" --arg id "$image_id" --arg cleanup "$cleanup" \
    '{format:1,nonce:$nonce,dump:$dump,dumpHash:$hash,start:"fixture",source:$source,sourceHead:"",origOid:$original,restoreOid:$restored,
      previous:$previous,target:$target,pinExpected:$expected,image:$image,commit:"",imageId:$id,level:1,
      locale:{encoding:"UTF8",provider:"c",collate:"C",ctype:"C"},publicPorts:[45000],count:"2",cleanup:$cleanup,phase:$phase,at:"",
      abort:{observation:"B",restoreOid:"20",dropIntent:1},history:[{phase:"collect-drop-intent"}]}' >"$DIR/recovery.lock"
  chmod 600 "$DIR/recovery.lock"
  : >"$WORK/calls"; : >"$WORK/sql"; : >"$WORK/events"
}
change_state() { jq "$@" "$DIR/recovery.lock" >"$WORK/state"; chmod 600 "$WORK/state"; mv "$WORK/state" "$DIR/recovery.lock"; }
change_model() { jq "$@" "$WORK/model.json" >"$WORK/next"; mv "$WORK/next" "$WORK/model.json"; }
run_restore() { "$DIR/recovery-tools/restore.sh" --deploy-dir "$DIR" --drill "$@"; }
phase_is() { [[ "$(jq -er .phase "$DIR/recovery.lock")" == "$1" ]]; }
rejected() {
  local needle="$1" rc=0; shift
  "$@" >"$WORK/out" 2>&1 || rc=$?
  [[ "$rc" != 0 ]] && grep -qF -- "$needle" "$WORK/out"
}
# rg is optional in the Ubuntu test container, grep is sufficient here.
no_sql_writes() { if grep -qE '^(CREATE DATABASE|DROP DATABASE|ALTER DATABASE)' "$WORK/sql"; then return 1; fi; }
# Actual CLI observation matrix. The pure table is independently pinned by
# dry-run-test.sh; this tests that the command obeys it and does not issue SQL
# when a name/oid observation has no intent authorizing it.
while IFS='|' read -r phase cleanup accepted; do
  for observation in Q Q+ B H A C invalid; do
    seed "$phase" "$observation" "$cleanup"
    if [[ " $accepted " == *" $observation "* ]]; then
      run_restore --status >"$WORK/out"
      grep -q "classification=$observation" "$WORK/out"
      run_restore --resume >"$WORK/out" 2>&1
      if [[ -f "$DIR/recovery.lock" ]]; then phase_is recovering; else grep -q reopened "$WORK/out"; fi
    else
      before="$(jq -cS .dbs "$WORK/model.json")"
      rejected reconcile-needed run_restore --resume
      phase_is reconcile-needed
      [[ "$before" == "$(jq -cS .dbs "$WORK/model.json")" ]]
      no_sql_writes
      rejected reconcile-needed run_restore --abort
      [[ "$before" == "$(jq -cS .dbs "$WORK/model.json")" ]]
      no_sql_writes
    fi
  done
done <<'TABLE'
quiesce|none|Q Q+
create-intent|none|Q Q+
created|none|B
collect-drop-intent|none|Q B
collect-failed|none|Q
collected|none|B
pin-intent|none|B
pin-failed|none|B
pinned|none|B
swap-failed|none|B
swap-intent|none|B H A
swap-1|none|B H A
restored|none|A
recovery-intent|none|A
recovering|none|A
recovery-failed|none|A
reopen-intent|none|A
app-start-intent|none|A
caddy-start-intent|none|A
smoke-intent|none|A
reopen-failed|none|A
reopen-closed|none|A
pre-drop-intent|drop-intent|A C
pre-dropped|dropped|C
floor-written|dropped|C
cleanup-failed|drop-intent|A C
TABLE
# Durable cleanup is checked through every private return path, not inferred
# from phase. A reappearing P after dropped must stop without SQL writes.
for phase in recovering reopen-failed reopen-closed; do
  for cleanup in none drop-intent dropped; do
    for observation in A C; do
      seed "$phase" "$observation" "$cleanup"
      if [[ ( "$cleanup" == none && "$observation" == C ) || ( "$cleanup" == dropped && "$observation" == A ) ]]; then
        rejected reconcile-needed run_restore --resume; no_sql_writes
      else
        run_restore --resume >"$WORK/out" 2>&1; phase_is recovering
        run_restore --reopen --checked-church-records >"$WORK/out" 2>&1
        [[ ! -e "$DIR/recovery.lock" ]]
        if [[ "$cleanup" == dropped || "$observation" == C ]]; then
          if grep -q '^DROP DATABASE' "$WORK/sql"; then exit 1; fi
        fi
      fi
    done
  done
done
# Release observations in every pre-swap pin state. An interrupted partial
# pin reruns the current provisioner. A target observation rechecks its image.
for phase in collected pin-intent pin-failed swap-failed; do
  for release in previous target partial neither; do
    seed "$phase" B none "$release"
    # Until target is complete the durable target observation is absent.
    [[ "$release" != partial ]] || change_state '.target=null'
    if [[ "$release" == neither ]]; then rejected reconcile-needed run_restore --resume; no_sql_writes
    else run_restore --resume >"$WORK/out" 2>&1; phase_is recovering; fi
  done
done
# Abort's own durable state, and its exact failed step, are exercised through
# --abort. --resume refuses an abort without any mutation.
for phase in abort-intent abort-drop-intent abort-dropped abort-start-intent abort-smoke-intent abort-failed; do
  for observation in Q Q+ B H A C invalid; do
    seed "$phase" "$observation"
    at=''; accepted='Q'
    case "$phase" in
      abort-intent) accepted='B'; change_state '.abort.observation="B"' ;;
      abort-drop-intent) accepted='Q Q+ B' ;;
      abort-failed) at=drop; accepted='Q Q+ B' ;;
    esac
    change_state --arg at "$at" '.at=$at'
    if [[ " $accepted " == *" $observation "* ]]; then
      before="$(cat "$DIR/recovery.lock")"
      rejected 'continue with --abort' run_restore --resume
      [[ "$before" == "$(cat "$DIR/recovery.lock")" ]]; no_sql_writes
      run_restore --abort >"$WORK/out" 2>&1
      [[ ! -e "$DIR/recovery.lock" ]]
    else rejected reconcile-needed run_restore --abort; no_sql_writes; fi
  done
done
for at in start smoke; do
  seed abort-failed Q; change_state --arg at "$at" '.at=$at'
  run_restore --abort >"$WORK/out" 2>&1; [[ ! -e "$DIR/recovery.lock" ]]
done
for intent in 0 1; do
  seed abort-failed Q; change_state --argjson intent "$intent" '.at="drop" | .abort.dropIntent=$intent'
  if [[ "$intent" == 0 ]]; then rejected reconcile-needed run_restore --abort; no_sql_writes
  else run_restore --abort >"$WORK/out" 2>&1; [[ ! -e "$DIR/recovery.lock" ]]; fi
done
for observation in B Q+; do
  seed abort-failed "$observation"; change_state '.at="drop"'
  rejected abort-failed env RESTORE_FIXTURE_DROP_FAIL=1 "$DIR/recovery-tools/restore.sh" --deploy-dir "$DIR" --drill --abort
  phase_is abort-failed
  run_restore --abort >"$WORK/out" 2>&1; [[ ! -e "$DIR/recovery.lock" ]]
done
for phase in collect-failed swap-failed collected; do
  observation=B; [[ "$phase" != collect-failed ]] || observation=Q
  for release in previous target partial; do
    seed "$phase" "$observation" none "$release"
    if [[ "$release" == previous ]]; then run_restore --abort >"$WORK/out" 2>&1; [[ ! -e "$DIR/recovery.lock" ]]
    else rejected '' run_restore --abort; no_sql_writes; fi
  done
done
# E2: every interrupted reopen first closes both normal services, removes
# recovery, observes W=0, then starts one private instance. E1: a foreign nonce
# and an outside backend must never cause the command to start a service.
for phase in reopen-intent app-start-intent caddy-start-intent smoke-intent reopen-failed; do
  seed "$phase" A
  change_model --arg nonce "$NONCE" '.app.running=true | .caddy.running=true | .recovery.exists=true | .recovery.running=true | .recovery.nonce=$nonce'
  run_restore --resume >"$WORK/out" 2>&1; phase_is recovering
  order="$(paste -sd, "$WORK/events")"
  [[ "$order" == *stop-caddy,stop-app,rm-recovery,W-check*start-recovery* ]]
  grep -q reopen-closed "$DIR/recovery.lock"
done
# A missing local pin must not leave the private .env snapshot behind.
seed pin-intent B
(
  ops_lock_acquire "$DIR" fixture
  rejected 'missing locally' env RESTORE_FIXTURE_INSPECT_FAIL=1 "$DIR/recovery-tools/provision.sh" --pin-only --skip-harden --deploy-dir "$DIR" --source-dir "$WORK/source" --domain groups.example.org --image fixture:target --allow-os --recovery-nonce "$NONCE"
  if compgen -G "$DIR/.pin-save.*" >/dev/null; then exit 1; fi
)
seed recovering A
change_model --arg nonce "$NONCE" '.recovery.exists=true | .recovery.running=true | .recovery.nonce=$nonce'
run_restore --resume >"$WORK/out" 2>&1
if grep -q W-check "$WORK/events"; then exit 1; fi
seed recovering A
change_model '.recovery.exists=true | .recovery.running=true | .recovery.nonce="foreign"'
rejected 'foreign recovery container' run_restore --resume
[[ ! -s "$WORK/events" ]]; no_sql_writes
seed recovering A
change_model '.outsideClients=1'
rejected 'client backends remain' env RESTORE_DRILL_WAIT=1 "$DIR/recovery-tools/restore.sh" --deploy-dir "$DIR" --drill --resume
if grep -q start- "$WORK/events"; then exit 1; fi
seed app-start-intent A
change_model '.app.running=true'
rejected close-failed env RESTORE_DRILL_FAULT=stop-fails "$DIR/recovery-tools/restore.sh" --deploy-dir "$DIR" --drill --resume
if grep -q start- "$WORK/events"; then exit 1; fi
run_restore --resume >"$WORK/out" 2>&1; phase_is recovering
# An acknowledged target pin whose outcome record was lost is recognized
# from its exact observed fields, without a second pull/build.
seed pin-intent B none target; change_state '.target=null | .imageId=.previous.imageId'
run_restore --resume >"$WORK/out" 2>&1; phase_is recovering
if grep -q 'compose .* pull app\|compose .* build app' "$WORK/calls"; then exit 1; fi
for observation in Q Q+; do
  seed abort-intent "$observation"
  change_state --arg observation "$observation" '.abort.observation=$observation'
  run_restore --abort >"$WORK/out" 2>&1; [[ ! -e "$DIR/recovery.lock" ]]
done
seed abort-drop-intent Q; change_state '.abort.dropIntent=0'
rejected reconcile-needed run_restore --abort; no_sql_writes
# Missing original databases use the same protocol without a retained P.
seed recovering C
change_state '.origOid=""'
run_restore --resume >"$WORK/out" 2>&1; phase_is recovering
run_restore --reopen --checked-church-records >"$WORK/out" 2>&1; [[ ! -e "$DIR/recovery.lock" ]]
# A different archive cannot resume a phase that must read its bytes.
seed created B
printf 'other archive\n' >"$WORK/other-archive"
rejected 'same archive sha256' run_restore --resume --dump "$WORK/other-archive"
no_sql_writes
# Count acknowledgement, exact zero shortcut, read-only status, and cleanup
# errors returning to private recovery all use the real installed command.
for count in 0 2 unknown; do
  seed recovering A; change_state --arg count "$count" '.count=$count'
  if [[ "$count" == 0 ]]; then run_restore --reopen >"$WORK/out" 2>&1
  else
    rejected checked-church-records run_restore --reopen
    run_restore --reopen --checked-church-records >"$WORK/out" 2>&1
  fi
  [[ ! -e "$DIR/recovery.lock" ]]
done
# Q5: recovery-deleted.txt is the saved list of accounts to delete again. Its
# ids and times, mode, hash and life cycle all come from the installed command.
listed() {
  local n="$1" rows="" i
  for ((i=1; i<=n; i++)); do rows+="$i 2026-11-14T10:00:00Z"$'\n'; done
  printf '%s' "$rows" >"$DIR/recovery-deleted.txt"; chmod 600 "$DIR/recovery-deleted.txt"
  change_state --arg h "$(sha256sum "$DIR/recovery-deleted.txt" | cut -d' ' -f1)" --argjson n "$n" '.deletedList={sha256:$h,count:$n}'
}
seed recovering A; listed 2; change_model '.remaining=2'
rejected '2 of 2 accounts' run_restore --reopen --checked-church-records
phase_is recovering; [[ -f "$DIR/recovery-deleted.txt" ]]
run_restore --status >"$WORK/out"; grep -q 'deleted list: 2 listed.*still active: 2' "$WORK/out"
change_model '.remaining=1'
rejected '1 of 2 accounts' run_restore --reopen --checked-church-records
phase_is recovering
change_model '.remaining=0'
run_restore --status >"$WORK/out"; grep -q 'still active: 0' "$WORK/out"
run_restore --reopen --checked-church-records >"$WORK/out" 2>&1
[[ ! -e "$DIR/recovery.lock" && ! -e "$DIR/recovery-deleted.txt" ]]
# A failed reopen keeps the list, --resume reuses it, and only the reopen that
# removes the lock removes the file.
seed recovering A; listed 2; change_model '.remaining=0'
before_hash="$(sha256sum "$DIR/recovery-deleted.txt" | cut -d' ' -f1)"
rejected reopen-failed env RESTORE_FIXTURE_SMOKE_FAIL=1 "$DIR/recovery-tools/restore.sh" --deploy-dir "$DIR" --drill --reopen --checked-church-records
phase_is reopen-failed; [[ -f "$DIR/recovery-deleted.txt" ]]
run_restore --resume >"$WORK/out" 2>&1; phase_is recovering
[[ "$(sha256sum "$DIR/recovery-deleted.txt" | cut -d' ' -f1)" == "$before_hash" ]]
run_restore --reopen --checked-church-records >"$WORK/out" 2>&1
[[ ! -e "$DIR/recovery.lock" && ! -e "$DIR/recovery-deleted.txt" ]]
# A file that no longer matches the lock cannot be checked: the operator's own
# statement is what remains, and it says so.
seed recovering A; listed 2; change_model '.remaining=2'
printf '3 2026-11-14T10:00:00Z\n' >>"$DIR/recovery-deleted.txt"
run_restore --reopen --checked-church-records >"$WORK/out" 2>&1
grep -q 'differs from recovery.lock' "$WORK/out"; [[ ! -e "$DIR/recovery.lock" ]]
seed pre-drop-intent C drop-intent
rejected reopen-failed env RESTORE_FIXTURE_SMOKE_FAIL=1 "$DIR/recovery-tools/restore.sh" --deploy-dir "$DIR" --drill --resume
[[ "$(jq -er .cleanup "$DIR/recovery.lock")" == drop-intent ]]
run_restore --resume >"$WORK/out" 2>&1; phase_is recovering
[[ "$(jq -er .cleanup "$DIR/recovery.lock")" == dropped ]]
: >"$WORK/sql"; run_restore --reopen --checked-church-records >"$WORK/out" 2>&1
if grep -q '^DROP DATABASE' "$WORK/sql"; then exit 1; fi
# Preflight refusals: no state, services, source or pin changes.
for refusal in root lock port archive missing-archive dirty unknown-commit tools leftover space level-image level-commit; do
  seed quiesce Q
  rm "$DIR/recovery.lock"
  change_model '.app.running=true | .caddy.running=true'
  args=(--dump "$ARCHIVE"); env_args=(); needle=''
  case "$refusal" in
    root) env_args=(RESTORE_FIXTURE_NONROOT=1); needle='run as root' ;;
    lock) printf '{}\n' >"$DIR/recovery.lock"; needle='recovery.lock exists' ;;
    port) change_model '.portBusy=true'; needle=busy ;;
    archive) printf 'bad-list\n' >"$ARCHIVE"; needle='unreadable archive' ;;
    missing-archive) args=(--dump "$WORK/no-archive"); needle='readable regular file' ;;
    unknown-commit) args+=(--commit deadbeef --source-dir "$WORK/source"); needle='commit missing' ;;
    dirty) touch "$WORK/source/dirty"; args+=(--commit "$HIGH_COMMIT" --source-dir "$WORK/source"); needle='source tree is dirty' ;;
    tools) rm "$DIR/recovery-tools/compose.recovery.yml"; needle='missing compose.recovery.yml' ;;
    leftover) change_model '.dbs.fold_restore_ffffffffffffffff={oid:"77",allow:true}'; needle='leftover nonce databases' ;;
    space) env_args=(RESTORE_FIXTURE_SPACE=0); needle='not enough free space' ;;
    level-image) args+=(--image fixture:zero --source-dir "$WORK/source"); needle='below host level 1' ;;
    level-commit) args+=(--commit "$LOW_COMMIT" --source-dir "$WORK/source"); needle='below host level 1' ;;
  esac
  if [[ "$refusal" == root ]]; then
    rejected "$needle" env "${env_args[@]}" "$DIR/recovery-tools/restore.sh" --deploy-dir "$DIR" "${args[@]}"
  else rejected "$needle" env "${env_args[@]}" "$DIR/recovery-tools/restore.sh" --deploy-dir "$DIR" --drill "${args[@]}"; fi
  no_sql_writes
  [[ "$(jq -er '.app.running and .caddy.running' "$WORK/model.json")" == true ]]
  printf 'valid archive fixture\n' >"$ARCHIVE"
  rm -f "$WORK/source/dirty"
done
# An inherited fd is required, then the nonce and phase are checked. The
# current provisioner is exercised with no starts, stops, smoke or cron.
for nonce in correct wrong absent; do
  seed pin-intent B
  (
    ops_lock_acquire "$DIR" fixture
    args=(--pin-only --skip-harden --deploy-dir "$DIR" --source-dir "$WORK/source" --domain groups.example.org --image fixture:target --allow-os)
    case "$nonce" in correct) args+=(--recovery-nonce "$NONCE") ;; wrong) args+=(--recovery-nonce ffffffffffffffffffffffffffffffff) ;; esac
    if [[ "$nonce" == correct ]]; then "$DIR/recovery-tools/provision.sh" "${args[@]}" >"$WORK/out" 2>&1
    else rejected nonce "$DIR/recovery-tools/provision.sh" "${args[@]}"; fi
    if grep -qE 'stop-|start-|smoke' "$WORK/events"; then exit 1; fi
  )
done
seed recovering A
before="$(cat "$DIR/recovery.lock")"
run_restore --status >"$WORK/out"
[[ "$before" == "$(cat "$DIR/recovery.lock")" ]]; no_sql_writes
for hook in RESTORE_DRILL_PAUSE=after-lock RESTORE_DRILL_FAULT=after-create RESTORE_DRILL_FORCE_TWO_STEP=1; do
  rejected 'require --drill' env "$hook" "$DIR/recovery-tools/restore.sh" --deploy-dir "$DIR" --dry-run --resume
  rejected '' env "$hook" "$DIR/recovery-tools/restore.sh" --deploy-dir /opt/fold --drill --dry-run --resume
 done
# Every mutating mode works through an inherited open-file-description lock.
# The descriptor identity survives the installed tool re-exec and child pin.
for mode in dump resume abort reopen; do
  if [[ "$mode" == abort ]]; then seed collected B; else seed recovering A; fi
  if [[ "$mode" == dump ]]; then
    seed quiesce Q; rm "$DIR/recovery.lock"
    change_model '.app.running=true | .caddy.running=true'
    args=(--dump "$ARCHIVE")
  else args=("--$mode"); fi
  [[ "$mode" != reopen ]] || args+=(--checked-church-records)
  ( ops_lock_acquire "$DIR" fixture; run_restore "${args[@]}" >"$WORK/out" 2>&1 )
done
# Archive failures and exclusion failures keep the original database exactly.
seed quiesce Q; rm "$DIR/recovery.lock"
change_model '.app.running=true | .caddy.running=true'
rejected collect-failed env RESTORE_FIXTURE_RESTORE_FAIL=1 "$DIR/recovery-tools/restore.sh" --deploy-dir "$DIR" --drill --dump "$ARCHIVE"
phase_is collect-failed
rejected collect-failed env RESTORE_FIXTURE_RESTORE_FAIL=1 "$DIR/recovery-tools/restore.sh" --deploy-dir "$DIR" --drill --resume
[[ "$(jq -er '.dbs.fold.oid' "$WORK/model.json")" == 10 ]]
run_restore --abort >"$WORK/out" 2>&1
for count in 0 2 unknown; do
  seed quiesce Q; rm "$DIR/recovery.lock"
  change_model --argjson count "${count/unknown/2}" '.count=$count | .app.running=true | .caddy.running=true'
  env_args=(); [[ "$count" != unknown ]] || env_args=(RESTORE_FIXTURE_SOURCE_UNREADABLE=1)
  rc=0
  env "${env_args[@]}" "$DIR/recovery-tools/restore.sh" --deploy-dir "$DIR" --drill --dump "$ARCHIVE" --reopen-if-none >"$WORK/out" 2>&1 || rc=$?
  if [[ "$count" == 0 ]]; then [[ "$rc" == 0 && ! -e "$DIR/recovery.lock" ]]
  else [[ "$rc" == 3 ]]; grep -q 'SITE IS CLOSED' "$WORK/out"; phase_is recovering; fi
  # Only a known nonzero count saves a list: a zero or unknown count writes none.
  if [[ "$count" == 2 ]]; then
    [[ "$(stat -c '%u:%a' "$DIR/recovery-deleted.txt")" == 0:600 ]]
    [[ "$(cat "$DIR/recovery-deleted.txt")" == $'1 2026-11-14T10:00:00Z\n2 2026-11-14T10:00:00Z' ]]
    [[ "$(jq -er .deletedList.sha256 "$DIR/recovery.lock")" == "$(sha256sum "$DIR/recovery-deleted.txt" | cut -d' ' -f1)" ]]
    [[ "$(jq -er .deletedList.count "$DIR/recovery.lock")" == 2 ]]
    # The hash was recorded in the same write that recorded collected.
    [[ "$(jq -er '[.history[]|.phase]|index("collected")' "$DIR/recovery.lock")" != null ]]
  else [[ ! -e "$DIR/recovery-deleted.txt" ]]
    if [[ -f "$DIR/recovery.lock" ]]; then [[ "$(jq -r '.deletedList // "none"' "$DIR/recovery.lock")" == none ]]; fi
  fi
done
# A failed time lookup keeps the ids and shows "unknown"; an old file never
# survives into a collection that finds nothing to list.
seed quiesce Q; rm "$DIR/recovery.lock"
change_model '.app.running=true | .caddy.running=true'
printf '9 2026-01-01T00:00:00Z\n' >"$DIR/recovery-deleted.txt"; chmod 600 "$DIR/recovery-deleted.txt"
env RESTORE_FIXTURE_TIMES_FAIL=1 "$DIR/recovery-tools/restore.sh" --deploy-dir "$DIR" --drill --dump "$ARCHIVE" >"$WORK/out" 2>&1
[[ "$(cat "$DIR/recovery-deleted.txt")" == $'1 unknown\n2 unknown' ]]
seed quiesce Q; rm "$DIR/recovery.lock"
change_model '.count=0 | .app.running=true | .caddy.running=true'
printf '9 2026-01-01T00:00:00Z\n' >"$DIR/recovery-deleted.txt"; chmod 600 "$DIR/recovery-deleted.txt"
"$DIR/recovery-tools/restore.sh" --deploy-dir "$DIR" --drill --dump "$ARCHIVE" >"$WORK/out" 2>&1
[[ ! -e "$DIR/recovery-deleted.txt" ]]
# A kill after the temp list is written and before the rename leaves a
# .recovery-deleted.* file with ids and times. The retry and the final cleanup
# each remove every such file, not only recovery-deleted.txt.
seed quiesce Q; rm "$DIR/recovery.lock"
change_model '.app.running=true | .caddy.running=true'
rc=0
env RESTORE_DRILL_FAULT=before-list-rename "$DIR/recovery-tools/restore.sh" --deploy-dir "$DIR" --drill --dump "$ARCHIVE" >"$WORK/out" 2>&1 || rc=$?
[[ "$rc" == 137 ]]
compgen -G "$DIR/.recovery-deleted.*" >/dev/null; [[ ! -e "$DIR/recovery-deleted.txt" ]]
run_restore --resume >"$WORK/out" 2>&1 || true
[[ "$(cat "$DIR/recovery-deleted.txt")" == $'1 2026-11-14T10:00:00Z\n2 2026-11-14T10:00:00Z' ]]
[[ -z "$(compgen -G "$DIR/.recovery-deleted.*")" ]]
printf '5 2026-01-01T00:00:00Z\n' >"$DIR/.recovery-deleted.stray"
change_model '.remaining=0'
run_restore --reopen --checked-church-records >"$WORK/out" 2>&1
[[ ! -e "$DIR/recovery.lock" && ! -e "$DIR/recovery-deleted.txt" ]]
[[ -z "$(compgen -G "$DIR/.recovery-deleted.*")" ]]
# Installation from both source modes carries the full protocol, and an
# image pin starts only the app pull, never normal services or smoke.
for mode in image build; do
  seed quiesce Q; rm "$DIR/recovery.lock"
  args=(--source-dir "$WORK/source")
  if [[ "$mode" == image ]]; then args+=(--image fixture:target); else args+=(--commit "$HIGH_COMMIT"); fi
  run_restore --dump "$ARCHIVE" "${args[@]}" >"$WORK/out" 2>&1
  phase_is recovering
  while IFS= read -r file; do
    [[ -f "$DIR/$file" ]]
    cmp "$DIR/$file" "$DIR/recovery-tools/$file"
  done < <(runtime_tools)
  grep -q 'compose .* pull app\|compose .* build app' "$WORK/calls"
  if grep -q 'start-app\|start-caddy\|smoke' "$WORK/events"; then exit 1; fi
done
# upgrade's image and build rollback both hand off to the protected restore,
# including the explicit source tree, without their own restore or tolerance.
for mode in image build; do
  seed quiesce Q; rm "$DIR/recovery.lock"
  mkdir "$DIR/dumps"
  cp "$ARCHIVE" "$DIR/dumps/fold-20260101-0000.dump"
  cat >"$DIR/release.prev" <<RELEASE
mode=$mode
previous_image=fixture:previous
previous_commit=$HIGH_COMMIT
dump=$DIR/dumps/fold-20260101-0000.dump
source_dir=$WORK/source
domain=groups.example.org
RELEASE
  DEPLOY_DRILL=1 "$DIR/recovery-tools/upgrade.sh" --rollback --deploy-dir "$DIR" --source-dir "$WORK/source" --dry-run >"$WORK/out" 2>&1
  grep -q 'hand off rollback to' "$WORK/out"
  grep -q -- '--source-dir' "$WORK/out"
  grep -q -- '--reopen-if-none' "$WORK/out"
  rc=0
  DEPLOY_DRILL=1 "$DIR/recovery-tools/upgrade.sh" --rollback --deploy-dir "$DIR" --source-dir "$WORK/source" >"$WORK/out" 2>&1 || rc=$?
  [[ "$rc" == 3 ]]; grep -q 'SITE IS CLOSED' "$WORK/out"
  phase_is recovering
 done
# A tool set installed before restore existed is refused without Docker reads.
seed quiesce Q
rm "$DIR/recovery-tools/compose.recovery.yml"
: >"$WORK/calls"
rejected 'missing compose.recovery.yml' run_restore --resume
[[ ! -s "$WORK/calls" ]]

# Review F2: backup's unrelated restore-test name is outside the nonce domain.
seed recovering A
change_model '.dbs.fold_restore_test={oid:"88",allow:true}'
run_restore --resume >"$WORK/out" 2>&1; phase_is recovering
[[ "$(jq -er .dbs.fold_restore_test.oid "$WORK/model.json")" == 88 ]]
seed quiesce Q; rm "$DIR/recovery.lock"
change_model '.dbs.fold_restore_test={oid:"88",allow:true} | .app.running=true | .caddy.running=true'
run_restore --dump "$ARCHIVE" >"$WORK/out" 2>&1; phase_is recovering
[[ "$(jq -er .dbs.fold_restore_test.oid "$WORK/model.json")" == 88 ]]
# A repaired observation only restores the preserved phase, including the
# failed step. The recheck itself never writes SQL or starts a service.
for phase in recovering cleanup-failed; do
  seed "$phase" A drop-intent
  change_state '.at="drop"'
  change_model '.dbs.fold_restore_ffffffffffffffff={oid:"77",allow:true}'
  rejected reconcile-needed run_restore --resume
  phase_is reconcile-needed
  [[ "$(jq -er .reconcileFrom "$DIR/recovery.lock")" == "$phase" ]]
  [[ "$(jq -er .reconcileAt "$DIR/recovery.lock")" == drop ]]
  rejected reconcile-needed run_restore --abort
  change_model 'del(.dbs.fold_restore_ffffffffffffffff)'
  before="$(cat "$DIR/recovery.lock")"
  run_restore --status >"$WORK/out" 2>&1
  grep -q recheckAccepted=yes "$WORK/out"
  [[ "$before" == "$(cat "$DIR/recovery.lock")" ]]
  rejected 'abort refused' run_restore --abort
  : >"$WORK/sql"; : >"$WORK/events"
  run_restore --resume >"$WORK/out" 2>&1
  phase_is "$phase"; [[ "$(jq -er .at "$DIR/recovery.lock")" == drop ]]
  no_sql_writes; [[ ! -s "$WORK/events" ]]
  run_restore --resume >"$WORK/out" 2>&1
  if [[ "$phase" == recovering ]]; then phase_is recovering; else [[ ! -e "$DIR/recovery.lock" ]]; fi
done
# Review F3: only pre-collection phases need bytes from the recorded archive.
for phase in collected pinned restored recovering recovery-failed reopen-failed pre-drop-intent pre-dropped floor-written cleanup-failed; do
  observation=A; cleanup=none
  case "$phase" in collected|pinned) observation=B ;; pre-drop-intent|cleanup-failed) cleanup=drop-intent ;; pre-dropped|floor-written) observation=C; cleanup=dropped ;; esac
  seed "$phase" "$observation" "$cleanup"
  mv "$ARCHIVE" "$WORK/held-archive"
  run_restore --resume >"$WORK/out" 2>&1
  mv "$WORK/held-archive" "$ARCHIVE"
  if [[ -e "$DIR/recovery.lock" ]]; then phase_is recovering; else grep -q reopened "$WORK/out"; fi
done
for mode in backup restore-test; do
  seed recovering A
  mkdir "$DIR/dumps"
  old="$DIR/dumps/fold-20200102-0000.dump"
  printf 'original archive\n' >"$old"; printf 'original ciphertext\n' >"$old.age"
  touch -d '200 days ago' "$old" "$old.age"
  printf 'INVALID backup credentials\n' >"$DIR/backup.env"
  before="$(sha256sum "$old" "$old.age")"
  args=(--deploy-dir "$DIR" --backup-dir "$DIR/dumps")
  [[ "$mode" != restore-test ]] || args+=(--restore-test)
  : >"$WORK/calls"
  rc=0; "$DIR/recovery-tools/backup.sh" "${args[@]}" >"$WORK/out" 2>&1 || rc=$?
  [[ "$rc" == 75 ]]; grep -q 'recovery active' "$WORK/out"
  [[ ! -s "$WORK/calls" && "$before" == "$(sha256sum "$old" "$old.age")" ]]
  [[ "$(find "$DIR/dumps" -type f | wc -l)" == 2 && ! -e "$DIR/dumps/LAST_OK" ]]
  "$DIR/recovery-tools/backup.sh" "${args[@]}" --dry-run >"$WORK/out" 2>&1
  grep -q 'recovery active' "$WORK/out"
done
# A standalone backup takes only backup.lock. Restore waits for it before
# publication, so even an existing backup's expiry cannot race a locked dump.
seed quiesce Q; rm "$DIR/recovery.lock"
change_model '.app.running=true | .caddy.running=true'
(
  backup_coord_acquire "$DIR"
  inode="$(stat -c '%d:%i' "$DIR/backup.lock")"
  backup_coord_open "$DIR"
  [[ "$inode" == "$(stat -c '%d:%i' "$DIR/backup.lock")" ]]
  rc=0
  ( exec 6>&-; "$DIR/recovery-tools/backup.sh" --deploy-dir "$DIR" --backup-dir "$DIR/dumps" ) >"$WORK/out" 2>&1 || rc=$?
  [[ "$rc" == 75 ]]; grep -q 'this backup was skipped' "$WORK/out"
  rc=0
  ( exec 6>&-; RESTORE_DRILL_WAIT=1 run_restore --dump "$ARCHIVE" ) >"$WORK/out" 2>&1 || rc=$?
  [[ "$rc" == 75 && ! -e "$DIR/recovery.lock" ]]; no_sql_writes
  [[ "$(jq -er '.app.running and .caddy.running' "$WORK/model.json")" == true ]]
  backup_coord_release
)
run_restore --dump "$ARCHIVE" >"$WORK/out" 2>&1; phase_is recovering
# Forward upgrades already hold ops.lock while calling backup. A real backup
# must still complete under that descriptor and hold only its own second lock.
# The uploads volume exists, as it does in every Fold deployment: a backup without it fails.
seed quiesce Q; rm "$DIR/recovery.lock"
change_model '.volume={exists:true,name:"restore-fixture_uploads",createdAt:"2026-11-15T00:00:00Z"}'
(
  ops_lock_acquire "$DIR" fixture-upgrade
  "$DIR/recovery-tools/backup.sh" --deploy-dir "$DIR" --backup-dir "$DIR/dumps" >"$WORK/out" 2>&1
  [[ -s "$DIR/dumps/LAST_OK" && "$(find "$DIR/dumps" -name 'fold-*.dump' | wc -l)" == 1 ]]
)
# Review F4: another interruption during post-smoke cleanup must leave the
# public phase intact; the next resume finishes cleanup and stays public.
for phase in pre-drop-intent pre-dropped floor-written cleanup-failed; do
  for point in after-app-start after-caddy-start; do
    observation=C; cleanup=dropped
    case "$phase" in pre-drop-intent|cleanup-failed) observation=A; cleanup=drop-intent ;; esac
    seed "$phase" "$observation" "$cleanup"
    change_state '.at="drop"'
    change_model --arg id "$PREVIOUS" '.app.running=true | .caddy.running=true | .app.image=$id'
    rc=0
    env RESTORE_DRILL_FAULT="$point" "$DIR/recovery-tools/restore.sh" --deploy-dir "$DIR" --drill --resume >"$WORK/out" 2>&1 || rc=$?
    [[ "$rc" == 137 ]]; phase_is "$phase"
    [[ "$(jq -er .at "$DIR/recovery.lock")" == drop ]]
    if grep -qE 'reopen-intent|app-start-intent|caddy-start-intent|smoke-intent' "$DIR/recovery.lock"; then exit 1; fi
    run_restore --resume >"$WORK/out" 2>&1
    [[ ! -e "$DIR/recovery.lock" && "$(jq -er '.app.running and .caddy.running' "$WORK/model.json")" == true ]]
  done
done
# Review F5: an orphaned pg_restore backend must drain before a retry can
# send a second restore or a drop. Its original phase and oids stay intact.
seed created B
change_model '.outsideRClients=1'
before="$(jq -cS .dbs "$WORK/model.json")"
rejected 'restore database clients have not drained' env RESTORE_DRILL_WAIT=1 "$DIR/recovery-tools/restore.sh" --deploy-dir "$DIR" --drill --resume
phase_is created; no_sql_writes
[[ "$before" == "$(jq -cS .dbs "$WORK/model.json")" ]]
if grep -q archive-restore "$WORK/events"; then exit 1; fi
change_model '.outsideRClients=0'
run_restore --resume >"$WORK/out" 2>&1; phase_is recovering
seed collect-drop-intent B
change_model '.outsideRClients=1'
rejected 'drop unconfirmed' env RESTORE_DRILL_WAIT=1 "$DIR/recovery-tools/restore.sh" --deploy-dir "$DIR" --drill --resume
phase_is collect-drop-intent; no_sql_writes
# Review F7: bootstrap/frozen re-exec pulls once during preflight even when
# the target is the deployed tag and that tag moved in the registry. Preserve
# the old local pin until pin-only, including an interrupted pre-swap abort.
for interrupt in no yes; do
  seed quiesce Q; rm "$DIR/recovery.lock"
  change_model --arg id "$TARGET" '.app.running=true | .caddy.running=true | .moveOnPull["fixture:previous"]=$id'
  while IFS= read -r file; do cp "$DIR/recovery-tools/$file" "$DIR/$file"; done < <(runtime_tools)
  args=(--deploy-dir "$DIR" --drill --dump "$ARCHIVE" --image fixture:previous --source-dir "$WORK/source")
  inode="$(stat -c '%d:%i' "$DIR/backup.lock")"
  if [[ "$interrupt" == yes ]]; then
    rc=0; RESTORE_DRILL_FAULT=after-create "$DIR/restore.sh" "${args[@]}" >"$WORK/out" 2>&1 || rc=$?
    [[ "$rc" == 137 && "$(jq -er '.images["fixture:previous"]' "$WORK/model.json")" == "$PREVIOUS" ]]
    run_restore --abort >"$WORK/out" 2>&1
    [[ ! -e "$DIR/recovery.lock" && "$(jq -er .app.image "$WORK/model.json")" == "$PREVIOUS" ]]
  else
    "$DIR/restore.sh" "${args[@]}" >"$WORK/out" 2>&1; phase_is recovering
    [[ "$(jq -er .previous.imageId "$DIR/recovery.lock")" == "$PREVIOUS" ]]
    [[ "$(jq -er .imageBeforePull "$DIR/recovery.lock")" == "$PREVIOUS" ]]
    [[ "$(jq -er .imageId "$DIR/recovery.lock")" == "$TARGET" ]]
    [[ "$(grep -c '^pull:fixture:previous$' "$WORK/events")" == 1 ]]
    [[ "$(grep -c '^pin-pull:fixture:previous$' "$WORK/events")" == 1 ]]
    grep -q "run .* $TARGET version --json" "$WORK/calls"
  fi
  [[ "$inode" == "$(stat -c '%d:%i' "$DIR/backup.lock")" ]]
done


# F8: the non-frozen installed entry validates in a disposable frozen copy.
# The existing recovery-tools directory, contents, modes and inode survive a
# local/remote below-floor image or a failed pull byte-for-byte unchanged.
for refusal in local-low remote-low remote-low-current pull-fails pull-fails-current; do
  seed quiesce Q; rm "$DIR/recovery.lock"
  change_model '.app.running=true | .caddy.running=true'
  while IFS= read -r file; do cp "$DIR/recovery-tools/$file" "$DIR/$file"; done < <(runtime_tools)
  printf '3\n' >"$DIR/recovery-tools/deploy-tools-version"
  printf 'keep the previous frozen set\n' >"$DIR/recovery-tools/keep-me"
  ref=fixture:target
  case "$refusal" in
    local-low) ref=fixture:zero ;;
    remote-low|remote-low-current)
      [[ "$refusal" != remote-low-current ]] || ref=fixture:previous
      change_model --arg ref "$ref" --arg id "$(printf 'sha256:%064d' 3)" '.moveOnPull[$ref]=$id' ;;
    pull-fails|pull-fails-current)
      [[ "$refusal" != pull-fails-current ]] || ref=fixture:previous
      change_model --arg ref "$ref" '.pullFailAt[$ref]=1' ;;
  esac
  before="$(find "$DIR/recovery-tools" -type f -printf '%P|%m|%u|%s\n' | sort; find "$DIR/recovery-tools" -type f -print0 | sort -z | xargs -0 sha256sum)"
  inode="$(stat -c '%d:%i' "$DIR/recovery-tools")"
  needle='below host level 1'; [[ "$refusal" != pull-fails* ]] || needle='target image resolution/pull failed'
  rejected "$needle" "$DIR/restore.sh" --deploy-dir "$DIR" --drill --dump "$ARCHIVE" --image "$ref" --source-dir "$WORK/source"
  [[ ! -e "$DIR/recovery.lock" && "$inode" == "$(stat -c '%d:%i' "$DIR/recovery-tools")" ]]
  [[ "$before" == "$(find "$DIR/recovery-tools" -type f -printf '%P|%m|%u|%s\n' | sort; find "$DIR/recovery-tools" -type f -print0 | sort -z | xargs -0 sha256sum)" ]]
  [[ "$(find "$DIR" -maxdepth 1 -type d -name '.recovery-tools.*' | wc -l)" == 0 ]]
  [[ "$(jq -er '.app.running and .caddy.running' "$WORK/model.json")" == true ]]; no_sql_writes
  [[ "$(grep -c '^pull ' "$WORK/calls")" == 1 ]]
done
# F9: same-policy unexpected ids at either pin pull must restore the saved
# release, not strand an otherwise intact original in reconcile-needed.
# Test both a distinct target ref and the tag of the currently deployed image.
for ref in fixture:target fixture:previous; do
  for move_at in preflight compose; do
    for next_action in abort resume; do
      seed quiesce Q; rm "$DIR/recovery.lock"
      change_model '.app.running=true | .caddy.running=true'
      unexpected="$(printf 'sha256:%064d' 5)"
      sequence="$(jq -n --arg target "$TARGET" --arg bad "$unexpected" --arg at "$move_at" 'if $at=="preflight" then [$target,$bad] else [$target,$target,$bad] end')"
      change_model --arg ref "$ref" --argjson sequence "$sequence" '.moveSequence[$ref]=$sequence'
      before_env="$(cat "$DIR/.env")"; before_compose="$(cat "$DIR/docker-compose.yml")"
      rejected 'pin failed; site stays closed' run_restore --dump "$ARCHIVE" --image "$ref" --source-dir "$WORK/source"
      phase_is pin-failed
      grep -q "pin image $unexpected differs from checked recovery image $TARGET" "$WORK/out"
      [[ "$before_env" == "$(cat "$DIR/.env")" && "$before_compose" == "$(cat "$DIR/docker-compose.yml")" ]]
      [[ "$(jq -er '.dbs.fold.oid' "$WORK/model.json")" == 10 ]]
      [[ "$(jq -r '.app.running or .caddy.running or .recovery.running' "$WORK/model.json")" == false ]]
      run_restore --status >"$WORK/out" 2>&1
      grep -q 'phase=pin-failed classification=B release=previous' "$WORK/out"
      if [[ "$next_action" == abort ]]; then
        run_restore --abort >"$WORK/out" 2>&1
        [[ ! -e "$DIR/recovery.lock" && "$(jq -er .app.image "$WORK/model.json")" == "$PREVIOUS" ]]
      else
        # Retry with the same intended immutable target once the registry tag
        # is repaired. No recollection or replacement of the original occurs.
        change_model --arg ref "$ref" --arg id "$TARGET" 'del(.moveSequence[$ref]) | .moveOnPull[$ref]=$id'
        : >"$WORK/events"; : >"$WORK/sql"
        run_restore --resume >"$WORK/out" 2>&1; phase_is recovering
        [[ "$(jq -er .imageId "$DIR/recovery.lock")" == "$TARGET" ]]
        if grep -q archive-restore "$WORK/events"; then exit 1; fi
      fi
    done
  done
done
# A previously partial pin remains partial on a rejected retry. It may be
# retried again, but abort must stay refused because the release had changed.
for move_at in preflight compose; do
  seed pin-intent B none partial
  change_state '.target=null'
  unexpected="$(printf 'sha256:%064d' 5)"
  sequence="$(jq -n --arg target "$TARGET" --arg bad "$unexpected" --arg at "$move_at" 'if $at=="preflight" then [$bad] else [$target,$bad] end')"
  change_model --argjson sequence "$sequence" '.moveSequence["fixture:target"]=$sequence'
  before_env="$(cat "$DIR/.env")"; before_compose="$(cat "$DIR/docker-compose.yml")"
  rejected 'pin failed; site stays closed' run_restore --resume
  phase_is pin-failed
  [[ "$before_env" == "$(cat "$DIR/.env")" && "$before_compose" == "$(cat "$DIR/docker-compose.yml")" ]]
  run_restore --status >"$WORK/out" 2>&1
  grep -q 'phase=pin-failed classification=B release=partial' "$WORK/out"
  rejected 'abort refused' run_restore --abort; no_sql_writes
  change_model --arg id "$TARGET" 'del(.moveSequence["fixture:target"]) | .moveOnPull["fixture:target"]=$id'
  : >"$WORK/events"
  run_restore --resume >"$WORK/out" 2>&1; phase_is recovering
  if grep -q archive-restore "$WORK/events"; then exit 1; fi
done
# F10: a backup collision names maintenance and explicitly says the run was
# skipped; it must not claim another backup is the holder or schedule a retry.
seed quiesce Q; rm "$DIR/recovery.lock"
(
  backup_coord_acquire "$DIR"
  : >"$WORK/calls"
  rc=0
  ( exec 6>&-; "$DIR/recovery-tools/backup.sh" --deploy-dir "$DIR" --backup-dir "$DIR/dumps" ) >"$WORK/out" 2>&1 || rc=$?
  [[ "$rc" == 75 ]]; grep -q 'this backup was skipped' "$WORK/out"
  grep -q 'restore preflight or provision file copy' "$WORK/out"
  grep -q 'Rerun backup.sh later' "$WORK/out"
  if grep -q 'Let the existing backup finish\|no recovery lock published' "$WORK/out"; then exit 1; fi
  [[ ! -s "$WORK/calls" && ! -e "$DIR/dumps" ]]
)

# --- M5.2: uploads restore, the backup service and the scheduler --------------
# The Docker double models the backup-image helpers (testdata/restore-docker.sh):
# the two folders are inode numbers, a held helper stays registered as running
# until it is reaped, and settle is one compose run. Real containers, real
# folders and a real erasure are the restore drill's.
chmod 755 "$WORK"
DARCHIVE="$WORK/fold-20261115-0230.dump"
UARCHIVE="$WORK/uploads-20261115-0230.tgz"
CRON="$DIR/.drill-cron/fold-backup"
BACKUP_IMAGE_ID="$(printf 'sha256:%064d' 9)"
FOREIGN=ffffffffffffffffffffffffffffffff
# The pids of restore.sh processes running for this site (installed or staged
# copy), whole shells and the command substitutions they left behind. /proc is read directly: procps is not
# in every container.
restore_processes() {
  local p
  local -a args
  for p in /proc/[0-9]*; do
    mapfile -d '' -t args 2>/dev/null <"$p/cmdline" || continue
    if [[ "${args[*]}" == *"restore.sh --deploy-dir $DIR"* ]]; then echo "${p#/proc/}"; fi
  done
  return 0
}
wait_no_restore_processes() {
  local i
  for ((i=0;i<100;i++)); do [[ -z "$(restore_processes)" ]] && return 0; sleep 0.1; done
  echo 'a restore.sh shell is still running' >&2
  return 1
}
# After seed: a uploads volume, the helpers' folders and a deployed backup
# service coordinated with this directory, as provision leaves them.
up_model() {
  chmod 755 "$DIR"
  chown 0:65532 "$DIR/backup.lock"; chmod 640 "$DIR/backup.lock"
  printf 'valid archive fixture\n' >"$DARCHIVE"; printf 'uploads archive fixture\n' >"$UARCHIVE"
  change_model '
    .volume={exists:true,name:"restore-fixture_uploads",createdAt:"2026-11-15T00:00:00Z"} | .helpers=[] | .restoreUploadsLevel=1
    | .backup={defined:true,coord:"ok",image:"fixture:backup",running:false}
    | .up={live:"100",staged:"none",stageState:"none",receipt:false,exchange:"none",dropIntent:"",dropDone:"",nextIno:200,origLive:"100",stageIno:"",
           photoLive:false,photoStaged:false,validateFail:false,probeFail:false,stageFail:false,exchangeFail:false,dropFail:false,free:999999999,need:1000}'
}
# A running site, no lock, uploads archives beside their database archive.
fresh_up() {
  seed quiesce Q; rm "$DIR/recovery.lock"
  up_model
  change_model '.app.running=true | .caddy.running=true | .backup.running=true'
}
dump_up() { run_restore --dump "$DARCHIVE" --uploads "$UARCHIVE"; }
# restore_env VAR=value... -- restore arguments
restore_env() {
  local -a vars=()
  while [[ "$1" != -- ]]; do vars+=("$1"); shift; done
  shift
  env "${vars[@]}" "$DIR/recovery-tools/restore.sh" --deploy-dir "$DIR" --drill "$@"
}
# The host cron in the drill's own directory, as restore.sh sees it.
cron_kind() { DEPLOY_DRILL=1 backup_cron_kind "$@"; }
cron_install() { DEPLOY_DRILL=1 backup_cron_install "$@"; }
# A lock and model at one point of the uploads life cycle, in private recovery
# (or the phase named), with the folders where that point leaves them.
seed_up() {
  local phase="$1" observation="$2" cleanup="$3" upstate="$4" staged="" old=""
  seed "$phase" "$observation" "$cleanup"
  up_model
  case "$upstate" in
    preflighted|stage-intent) ;;
    staged|exchange-intent)
      staged=200
      change_model '.up.staged="200" | .up.stageIno="200" | .up.nextIno=201 | .up.receipt=true | .up.stageState="complete" | .up.photoStaged=true' ;;
    exchanged|old-drop-intent)
      staged=200; old=100
      change_model '.up.live="200" | .up.staged="100" | .up.stageIno="200" | .up.nextIno=201 | .up.receipt=true | .up.stageState="complete" | .up.exchange="done" | .up.photoLive=true' ;;
  esac
  change_state --arg up "$upstate" --arg staged "$staged" --arg old "$old" --arg archive "$UARCHIVE" \
    --arg sha "$(sha256sum "$UARCHIVE" | cut -d' ' -f1)" --arg image "$BACKUP_IMAGE_ID" \
    '.format=2 | .uploads={archive:$archive,archiveSha256:$sha,image:$image,volume:{name:"restore-fixture_uploads",createdAt:"2026-11-15T00:00:00Z"},liveInode:"100",stagedInode:$staged,oldInode:$old,state:$up}'
}
wait_ready() {
  local point="$1" i
  for ((i=0;i<300;i++)); do [[ ! -f "$DIR/drill.ready" ]] || break; sleep 0.1; done
  [[ "$(cat "$DIR/drill.ready")" == "$point" ]]
}

# A database-only restore stays format 1 and never reaches an uploads helper,
# settle or the folders. It still arms the scheduler: with no backup service in
# the deployed Compose, the guarded host full cron.
seed quiesce Q; rm "$DIR/recovery.lock"; chmod 755 "$DIR"
change_model '.app.running=true | .caddy.running=true'
run_restore --dump "$ARCHIVE" >"$WORK/out" 2>&1; phase_is recovering
[[ "$(jq -er .format "$DIR/recovery.lock")" == 1 && "$(jq -r 'has("uploads")' "$DIR/recovery.lock")" == false ]]
run_restore --status >"$WORK/out"; if grep -q '^uploads:' "$WORK/out"; then exit 1; fi
run_restore --reopen --checked-church-records >"$WORK/out" 2>&1
[[ ! -e "$DIR/recovery.lock" ]]
if grep -q 'fold.uploads.role\|settle' "$WORK/calls"; then exit 1; fi
if grep -qE '^up-|^settle' "$WORK/events"; then exit 1; fi
grep -q 'root .*/backup.sh >>' "$CRON"; if grep -q -- '--expire-only' "$CRON"; then exit 1; fi
[[ "$(stat -c %a "$CRON")" == 644 ]]

# A lock that carries uploads is format 2, which a tool set from before it
# cannot read, and its fields must be well formed.
seed_up recovering A none exchanged
[[ "$(jq -er .format "$DIR/recovery.lock")" == 2 ]]
if jq -e '.format==1' "$DIR/recovery.lock" >/dev/null; then exit 1; fi
change_state '.uploads.state="bogus"'
rejected 'invalid recovery.lock' run_restore --status
seed recovering A
rejected 'was not started with --uploads' run_restore --resume --uploads "$UARCHIVE"
rejected '--uploads is for --dump' run_restore --reopen --uploads "$UARCHIVE"

# Every refusal happens before recovery.lock and before anything stops.
for refusal in stamp capability archive space helper probe unset other rw other-dir no-backup; do
  fresh_up
  args=(--dump "$DARCHIVE" --uploads "$UARCHIVE"); needle=''
  case "$refusal" in
    stamp) cp "$UARCHIVE" "$WORK/uploads-20260101-0000.tgz"; args=(--dump "$DARCHIVE" --uploads "$WORK/uploads-20260101-0000.tgz"); needle='must be uploads-20261115-0230.tgz' ;;
    capability) change_model '.restoreUploadsLevel=0'; needle='cannot settle restored uploads' ;;
    archive) change_model '.up.validateFail=true'; needle='failed validation' ;;
    space) change_model '.up.free=1'; needle='too little room' ;;
    helper) change_model --arg n "$FOREIGN" '.helpers=[{id:"helper-stage-1",role:"stage",nonce:$n,image:""}]'; needle='still running' ;;
    probe) change_model '.up.probeFail=true'; needle='cannot exchange folders atomically' ;;
    unset|other|rw|other-dir) change_model --arg c "$refusal" '.backup.coord=$c'; needle='not coordinated' ;;
    no-backup) change_model '.backup.defined=false'; needle='defines none' ;;
  esac
  rejected "$needle" "$DIR/recovery-tools/restore.sh" --deploy-dir "$DIR" --drill "${args[@]}"
  [[ ! -e "$DIR/recovery.lock" ]]; no_sql_writes
  [[ "$(jq -er '.app.running and .caddy.running' "$WORK/model.json")" == true ]]
  if grep -qE 'stop-|^up-stage|^up-exchange|start-recovery' "$WORK/events"; then exit 1; fi
  if grep -q 'rm-helper' "$WORK/events" && [[ "$refusal" == helper ]]; then exit 1; fi
done
# A database-only restore is refused too when a backup service in the deployed
# Compose cannot see the locks.
for coord in unset other rw other-dir; do
  seed quiesce Q; rm "$DIR/recovery.lock"; up_model
  change_model --arg c "$coord" '.app.running=true | .caddy.running=true | .backup.coord=$c'
  rejected 'not coordinated' "$DIR/recovery-tools/restore.sh" --deploy-dir "$DIR" --drill --dump "$ARCHIVE"
  [[ ! -e "$DIR/recovery.lock" ]]; no_sql_writes
done
# Discovery errors are not an absent service; lock permissions and directory
# search are part of "the service can use the lock".
seed quiesce Q; up_model
backup_service_defined "$DIR"
backup_coordination_check "$DIR" >/dev/null
chmod 600 "$DIR/backup.lock"
if backup_coordination_check "$DIR" >"$WORK/out" 2>&1; then exit 1; fi
grep -q 'cannot lock it' "$WORK/out"
chmod 640 "$DIR/backup.lock"; chmod 750 "$DIR"
if backup_coordination_check "$DIR" >"$WORK/out" 2>&1; then exit 1; fi
grep -q 'cannot be searched' "$WORK/out"
chmod 755 "$DIR"
change_model '.backup.defined=false'; rc=0; backup_service_defined "$DIR" || rc=$?; [[ "$rc" == 1 ]]
change_model '.configFail=true'; rc=0; backup_service_defined "$DIR" || rc=$?; [[ "$rc" == 2 ]]
change_model '.configFail=false'

# The full run, in order: validate and probe, close, collect, stage, swap,
# exchange, private recovery; then settle before anything public starts, smoke,
# the old folder and receipt, and the scheduler, with the lock still held.
fresh_up
dump_up >"$WORK/out" 2>&1; phase_is recovering
[[ "$(jq -er .uploads.state "$DIR/recovery.lock")" == exchanged ]]
[[ "$(jq -er '.uploads|[.liveInode,.stagedInode,.oldInode]|join(",")' "$DIR/recovery.lock")" == 100,200,100 ]]
[[ "$(jq -er .uploads.image "$DIR/recovery.lock")" == "$BACKUP_IMAGE_ID" ]]
[[ "$(jq -er .uploads.volume.name "$DIR/recovery.lock")" == restore-fixture_uploads ]]
[[ "$(jq -er .uploads.archiveSha256 "$DIR/recovery.lock")" == "$(sha256sum "$UARCHIVE" | cut -d' ' -f1)" ]]
order="$(paste -sd, "$WORK/events")"
[[ "$order" == *up-validate,up-probe*stop-caddy,stop-app,stop-backup*archive-restore*up-stage*up-exchange*start-recovery* ]]
[[ "$(jq -r '[.up.live,.up.staged,(.helpers|length|tostring),(.up.photoLive|tostring)]|join(",")' "$WORK/model.json")" == 200,100,0,true ]]
[[ "$(jq -r .backup.running "$WORK/model.json")" == false && "$(jq -r '.app.running or .caddy.running' "$WORK/model.json")" == false ]]
run_restore --status >"$WORK/out"; grep -q 'uploads: state=exchanged liveInode=100 stagedInode=200 oldInode=100' "$WORK/out"
image_id="$(jq -er .imageId "$DIR/recovery.lock")"; nonce="$(jq -er .nonce "$DIR/recovery.lock")"
: >"$WORK/events"
run_restore --reopen --checked-church-records >"$WORK/out" 2>&1
[[ ! -e "$DIR/recovery.lock" ]]
order="$(paste -sd, "$WORK/events")"
[[ "$order" == *rm-recovery*settle-start*settle-end\ rc=0*start-app*start-caddy*smoke*up-drop,up-release*start-backup* ]]
grep -qF "settle-start image=$image_id nonce=$nonce" "$WORK/events"
# The argv ends with the recovery service and the command; the image's own
# /fold entrypoint is kept, never replaced.
grep -qE ' -f [^ ]*/compose.recovery.yml --profile recovery run --rm --no-deps -T recovery restore settle-uploads --json$' "$WORK/calls"
if grep -q 'settle-uploads.*--entrypoint\|--entrypoint.*settle-uploads' "$WORK/calls"; then exit 1; fi
[[ "$(jq -r '[.up.live,.up.staged,(.up.receipt|tostring),(.up.photoLive|tostring),.up.dropDone,.backup.running|tostring]|join(",")' "$WORK/model.json")" == 200,none,false,false,100,true ]]
[[ ! -e "$CRON" ]]

# Before settle the erased person's photo is still there and nothing is public;
# after it, it is gone and still nothing is public.
for point in before-settle after-settle; do
  seed_up recovering A none exchanged
  mkfifo "$DIR/drill.fifo"
  restore_env RESTORE_DRILL_PAUSE="$point" -- --reopen --checked-church-records >"$WORK/out" 2>&1 &
  holder=$!
  wait_ready "$point"
  [[ "$(jq -r '.app.running or .caddy.running or .backup.running' "$WORK/model.json")" == false ]]
  if [[ "$point" == before-settle ]]; then
    [[ "$(jq -r .up.photoLive "$WORK/model.json")" == true ]]
  else
    [[ "$(jq -r '[.up.photoLive,.recovery.exists]|join(",")' "$WORK/model.json")" == false,false ]]
  fi
  printf 'continue\n' >"$DIR/drill.fifo"
  wait "$holder"
  [[ ! -e "$DIR/recovery.lock" ]]
done
# Settle that cannot finish leaves the site closed; --resume returns to private
# recovery, where the photo still exists, and a repeat settles.
for settle_rc in 1 2; do
  seed_up recovering A none exchanged
  change_model --argjson rc "$settle_rc" '.settle.rc=$rc'
  rejected reopen-failed "$DIR/recovery-tools/restore.sh" --deploy-dir "$DIR" --drill --reopen --checked-church-records
  phase_is reopen-failed
  [[ "$(jq -r '[.app.running,.caddy.running,.up.photoLive]|map(tostring)|join(",")' "$WORK/model.json")" == false,false,true ]]
  change_model '.settle.rc=0'
  run_restore --resume >"$WORK/out" 2>&1; phase_is recovering
  run_restore --reopen --checked-church-records >"$WORK/out" 2>&1
  [[ ! -e "$DIR/recovery.lock" && "$(jq -r .up.photoLive "$WORK/model.json")" == false ]]
done
# A zero exit that still reports an unexpected folder entry (or no counts) is not
# clear: the site stays closed.
for settle_out in '{"remaining":0,"unexpected":2}' '{"unexpected":0}' 'not json'; do
  seed_up recovering A none exchanged
  change_model --arg out "$settle_out" '.settle.out=$out'
  rejected reopen-failed "$DIR/recovery-tools/restore.sh" --deploy-dir "$DIR" --drill --reopen --checked-church-records
  phase_is reopen-failed
  [[ "$(jq -r '[.app.running,.caddy.running]|map(tostring)|join(",")' "$WORK/model.json")" == false,false ]]
done
# Killed while settle runs: the retry removes its container first.
seed_up recovering A none exchanged
rc=0; restore_env RESTORE_DRILL_FAULT=during-settle RESTORE_FIXTURE_SETTLE_HOLD=1 -- --reopen --checked-church-records >"$WORK/out" 2>&1 || rc=$?
[[ "$rc" == 137 && "$(jq -r .recovery.exists "$WORK/model.json")" == true ]]
phase_is reopen-intent
: >"$WORK/events"
run_restore --resume >"$WORK/out" 2>&1; phase_is recovering
order="$(paste -sd, "$WORK/events")"; [[ "$order" == *rm-recovery*start-recovery* ]]
run_restore --reopen --checked-church-records >"$WORK/out" 2>&1
[[ ! -e "$DIR/recovery.lock" && "$(jq -r .up.photoLive "$WORK/model.json")" == false ]]

# Killed while the stage helper runs: the helper is still registered and
# running. A foreign helper blocks the retry; with it gone the retry removes the
# owned helper before it looks at a folder, and everything converges.
fresh_up
rc=0; restore_env RESTORE_DRILL_FAULT=during-uploads-stage RESTORE_FIXTURE_HELPER_HOLD=stage -- --dump "$DARCHIVE" --uploads "$UARCHIVE" >"$WORK/out" 2>&1 || rc=$?
[[ "$rc" == 137 ]]
# Only the caller died, as with a real SIGKILL: the command substitution that was
# waiting on the held helper is still running, and it holds neither lock and
# starts no second helper.
[[ -n "$(restore_processes)" ]]
( exec 9>>"$DIR/ops.lock"; flock -w 5 -x 9 )
( exec 6>>"$DIR/backup.lock"; flock -w 5 -x 6 )
[[ "$(jq -r '.helpers|length' "$WORK/model.json")" == 1 && "$(jq -r '.helpers[0].nonce' "$WORK/model.json")" == "$(jq -er .nonce "$DIR/recovery.lock")" ]]
[[ "$(jq -r .up.stageState "$WORK/model.json")" == none ]]
phase_is collected; [[ "$(jq -er .uploads.state "$DIR/recovery.lock")" == stage-intent ]]
change_model --arg n "$FOREIGN" '.helpers += [{id:"helper-foreign-9",role:"stage",nonce:$n,image:""}]'
before="$(jq -cS .up "$WORK/model.json")"
rejected 'foreign uploads helper' run_restore --resume
[[ "$(jq -cS .up "$WORK/model.json")" == "$before" && "$(jq -r '.helpers|length' "$WORK/model.json")" == 2 ]]
rejected 'foreign uploads helper' run_restore --abort
[[ "$(jq -cS .up "$WORK/model.json")" == "$before" ]]
change_model --arg n "$FOREIGN" '.helpers |= map(select(.nonce!=$n))'
: >"$WORK/events"
run_restore --resume >"$WORK/out" 2>&1; phase_is recovering
order="$(paste -sd, "$WORK/events")"; [[ "$order" == *rm-helper*up-stage*up-exchange* ]]
[[ "$(jq -r '[(.helpers|length|tostring),.up.exchange,.up.live]|join(",")' "$WORK/model.json")" == 0,done,200 ]]
# The helper's removal lets the surviving shell finish.
wait_no_restore_processes
# A real SIGKILL of the restore process alone, while a helper is held two command
# substitutions down (the preflight's observe: uploads_observe inside a capture).
# The shells waiting on it hold neither lock, so the next run takes ops.lock and
# meets the helper (a running helper without a lock is refused for the operator
# to remove); once it is gone the run converges.
fresh_up
RESTORE_FIXTURE_HELPER_HOLD=observe "$DIR/recovery-tools/restore.sh" --deploy-dir "$DIR" --drill --dump "$DARCHIVE" --uploads "$UARCHIVE" >"$WORK/out" 2>&1 &
caller=$!
# The preflight runs the short-lived validate and probe helpers first, so wait for
# the held observe helper by role, not for the first helper of any kind.
for ((i=0;i<150;i++)); do [[ "$(jq -r '[.helpers[]|select(.role=="observe")]|length' "$WORK/model.json")" == 0 ]] || break; sleep 0.1; done
[[ "$(jq -r '.helpers|map(.role)|join(",")' "$WORK/model.json")" == observe ]]
kill -KILL "$caller"
rc=0; wait "$caller" || rc=$?
[[ "$rc" == 137 && ! -e "$DIR/recovery.lock" ]]
[[ -n "$(restore_processes)" ]]
( exec 9>>"$DIR/ops.lock"; flock -w 5 -x 9 )
( exec 6>>"$DIR/backup.lock"; flock -w 5 -x 6 )
rejected 'still running without a recovery lock' dump_up
if grep -q 'maintenance busy' "$WORK/out"; then exit 1; fi
change_model '.helpers=[]'
wait_no_restore_processes
dump_up >"$WORK/out" 2>&1; phase_is recovering
# Staging finished but was not acknowledged: the retry needs the archive's
# bytes (staging is not yet recorded) and refuses another file.
fresh_up
rc=0; restore_env RESTORE_DRILL_FAULT=after-uploads-stage -- --dump "$DARCHIVE" --uploads "$UARCHIVE" >"$WORK/out" 2>&1 || rc=$?
[[ "$rc" == 137 && "$(jq -r .up.stageState "$WORK/model.json")" == complete && "$(jq -er .uploads.state "$DIR/recovery.lock")" == stage-intent ]]
printf 'another file\n' >"$WORK/other-uploads"
rejected 'same uploads archive sha256' run_restore --resume --uploads "$WORK/other-uploads"
mv "$UARCHIVE" "$WORK/held-uploads"
rejected 'same uploads archive sha256' run_restore --resume
mv "$WORK/held-uploads" "$UARCHIVE"
run_restore --resume >"$WORK/out" 2>&1; phase_is recovering
[[ "$(jq -er .uploads.state "$DIR/recovery.lock")" == exchanged ]]
# Staged and killed before the swap: later phases never need the archive, and an
# abort leaves the original database and the live folder as they were.
fresh_up
rc=0; restore_env RESTORE_DRILL_FAULT=before-swap -- --dump "$DARCHIVE" --uploads "$UARCHIVE" >"$WORK/out" 2>&1 || rc=$?
[[ "$rc" == 137 && "$(jq -er .uploads.state "$DIR/recovery.lock")" == staged ]]
live_before="$(jq -r .up.live "$WORK/model.json")"
mv "$UARCHIVE" "$WORK/held-uploads"; mv "$DARCHIVE" "$WORK/held-dump"
run_restore --resume >"$WORK/out" 2>&1; phase_is recovering
[[ "$(jq -er .uploads.state "$DIR/recovery.lock")" == exchanged && "$(jq -r .up.live "$WORK/model.json")" == 200 ]]
mv "$WORK/held-uploads" "$UARCHIVE"; mv "$WORK/held-dump" "$DARCHIVE"
for abort_point in none after-uploads-drop before-scheduler; do
  fresh_up
  rc=0; restore_env RESTORE_DRILL_FAULT=before-swap -- --dump "$DARCHIVE" --uploads "$UARCHIVE" >"$WORK/out" 2>&1 || rc=$?
  [[ "$rc" == 137 ]]
  [[ "$(jq -r .up.live "$WORK/model.json")" == "$live_before" && "$(jq -r .up.staged "$WORK/model.json")" == 200 ]]
  if [[ "$abort_point" != none ]]; then
    rc=0; restore_env RESTORE_DRILL_FAULT="$abort_point" -- --abort >"$WORK/out" 2>&1 || rc=$?
    [[ "$rc" == 137 && -e "$DIR/recovery.lock" ]]
    run_restore --status >"$WORK/out" 2>&1 || true
  fi
  run_restore --abort >"$WORK/out" 2>&1
  [[ ! -e "$DIR/recovery.lock" ]]
  [[ "$(jq -r '[.up.live,.up.staged,(.up.receipt|tostring),.up.dropDone,.app.running,.caddy.running,.backup.running]|map(tostring)|join(",")' "$WORK/model.json")" == "$live_before,none,false,200,true,true,true" ]]
  [[ "$(jq -er '.dbs.fold.oid' "$WORK/model.json")" == 10 ]]
done
# An abort that cannot remove the staged folder keeps the site closed and the
# lock; the repeat finishes.
fresh_up
rc=0; restore_env RESTORE_DRILL_FAULT=before-swap -- --dump "$DARCHIVE" --uploads "$UARCHIVE" >"$WORK/out" 2>&1 || rc=$?
[[ "$rc" == 137 ]]
change_model '.up.dropFail=true'
rejected 'abort-failed at=uploads' run_restore --abort
phase_is abort-failed; [[ "$(jq -er .at "$DIR/recovery.lock")" == uploads ]]
[[ "$(jq -r '.app.running or .caddy.running' "$WORK/model.json")" == false ]]
change_model '.up.dropFail=false'
run_restore --abort >"$WORK/out" 2>&1; [[ ! -e "$DIR/recovery.lock" ]]
# A recorded stage that is missing is not a cleaned one. Without a recorded
# removal, and with a recorded removal the helper's receipt does not confirm,
# nothing is released: the lock, the receipt and the closed site stay, every
# retry refuses the same way, and once the folder is back the abort converges.
for missing in no-intent unconfirmed-intent; do
  fresh_up
  rc=0; restore_env RESTORE_DRILL_FAULT=before-swap -- --dump "$DARCHIVE" --uploads "$UARCHIVE" >"$WORK/out" 2>&1 || rc=$?
  [[ "$rc" == 137 && "$(jq -er .uploads.state "$DIR/recovery.lock")" == staged && "$(jq -er .uploads.stagedInode "$DIR/recovery.lock")" == 200 ]]
  change_model '.up.staged="none"'
  [[ "$missing" == no-intent ]] || change_state '.uploads.state="staged-drop-intent"'
  : >"$WORK/events"
  for _ in 1 2; do
    rejected 'abort-failed at=uploads' run_restore --abort
    phase_is abort-failed; [[ "$(jq -er .at "$DIR/recovery.lock")" == uploads ]]
    [[ "$(jq -er .uploads.stagedInode "$DIR/recovery.lock")" == 200 && "$(jq -er .uploads.state "$DIR/recovery.lock")" != staged-dropped ]]
    [[ "$(jq -r '[.up.receipt,.up.dropDone,.app.running,.caddy.running]|map(tostring)|join(",")' "$WORK/model.json")" == true,,false,false ]]
    if grep -q '^up-release$' "$WORK/events"; then exit 1; fi
  done
  [[ "$missing" == unconfirmed-intent ]] || grep -qF 'no removal of it was recorded' "$WORK/out"
  change_model '.up.staged="200"'
  run_restore --abort >"$WORK/out" 2>&1; [[ ! -e "$DIR/recovery.lock" ]]
  [[ "$(jq -r '[.up.live,.up.staged,(.up.receipt|tostring),.up.dropDone]|join(",")' "$WORK/model.json")" == 100,none,false,200 ]]
done
# Staging finished but its acknowledgement was lost, so recovery.lock names no
# stage (stage-intent, stagedInode empty) while the helper's receipt records a
# completed one. If that folder then vanishes with no removal ever recorded, the
# receipt is the witness: every abort refuses, keeps the lock, the receipt and
# the closed site, and converges once the folder is back.
fresh_up
rc=0; restore_env RESTORE_DRILL_FAULT=after-uploads-stage -- --dump "$DARCHIVE" --uploads "$UARCHIVE" >"$WORK/out" 2>&1 || rc=$?
[[ "$rc" == 137 && "$(jq -er .uploads.state "$DIR/recovery.lock")" == stage-intent && "$(jq -er .uploads.stagedInode "$DIR/recovery.lock")" == '' ]]
[[ "$(jq -r '[.up.stageState,.up.stageIno,.up.staged]|join(",")' "$WORK/model.json")" == complete,200,200 ]]
change_model '.up.staged="none"'
for _ in 1 2; do
  rejected 'abort-failed at=uploads' run_restore --abort
  grep -qF 'would not release its receipt' "$WORK/out"
  phase_is abort-failed; [[ "$(jq -er .at "$DIR/recovery.lock")" == uploads ]]
  [[ "$(jq -er .uploads.state "$DIR/recovery.lock")" == stage-intent && "$(jq -er .uploads.stagedInode "$DIR/recovery.lock")" == '' ]]
  [[ "$(jq -r '[.up.receipt,.up.stageIno,.up.dropDone,.up.live,.app.running,.caddy.running]|map(tostring)|join(",")' "$WORK/model.json")" == true,200,,100,false,false ]]
done
change_model '.up.staged="200"'
run_restore --abort >"$WORK/out" 2>&1; [[ ! -e "$DIR/recovery.lock" ]]
[[ "$(jq -r '[.up.live,.up.staged,(.up.receipt|tostring),.up.dropDone]|join(",")' "$WORK/model.json")" == 100,none,false,200 ]]
# A pending receipt that names no stage has nothing to confirm: the same
# disappearance of its (empty) folder is a clean abort.
fresh_up
change_model '.up.stageKill="mkdir"'
rejected 'staging the uploads failed' dump_up
[[ "$(jq -r '[.up.stageState,.up.stageIno,.up.staged]|join(",")' "$WORK/model.json")" == pending,,200 ]]
change_model '.up.staged="none"'
run_restore --abort >"$WORK/out" 2>&1; [[ ! -e "$DIR/recovery.lock" ]]
[[ "$(jq -r '[.up.live,.up.staged,(.up.receipt|tostring)]|join(",")' "$WORK/model.json")" == 100,none,false ]]
# Staging that fails keeps the lock and the closed site; both resume and abort work.
fresh_up
change_model '.up.stageFail=true'
rejected 'staging the uploads failed' dump_up
phase_is collected
change_model '.up.stageFail=false'
run_restore --resume >"$WORK/out" 2>&1; phase_is recovering
# The stage helper is stopped after it created the empty staged folder and
# before its receipt named it (a pending receipt, no stage inode: what the reap of
# a running helper leaves when it lands in that window). The folder is not a
# complete stage, nothing exchanges it, and both a retry and an abort converge
# without touching the live folder or the database. The abort removes exactly that
# folder under the recorded identity.
for after in resume abort; do
  fresh_up
  change_model '.up.stageKill="mkdir"'
  rejected 'staging the uploads failed' dump_up
  phase_is collected; [[ "$(jq -er .uploads.state "$DIR/recovery.lock")" == stage-intent ]]
  [[ "$(jq -r '[.up.live,.up.staged,.up.stageIno,.up.stageState,.up.exchange]|join(",")' "$WORK/model.json")" == 100,200,,pending,none ]]
  [[ "$(jq -r '.app.running or .caddy.running' "$WORK/model.json")" == false ]]
  if [[ "$after" == resume ]]; then
    run_restore --resume >"$WORK/out" 2>&1; phase_is recovering
    [[ "$(jq -er .uploads.state "$DIR/recovery.lock")" == exchanged && "$(jq -er .uploads.oldInode "$DIR/recovery.lock")" == 100 ]]
    [[ "$(jq -r '[.up.live,.up.stageState,(.helpers|length|tostring)]|join(",")' "$WORK/model.json")" == 201,complete,0 ]]
  else
    run_restore --abort >"$WORK/out" 2>&1
    [[ ! -e "$DIR/recovery.lock" ]]
    [[ "$(jq -r '[.up.live,.up.staged,(.up.receipt|tostring),.up.dropDone,.app.running,.caddy.running,.backup.running]|map(tostring)|join(",")' "$WORK/model.json")" == 100,none,false,200,true,true,true ]]
    [[ "$(jq -er '.dbs.fold.oid' "$WORK/model.json")" == 10 ]]
  fi
done
# Exchange interrupted after the helper returned, before it was acknowledged.
fresh_up
rc=0; restore_env RESTORE_DRILL_FAULT=after-uploads-exchange -- --dump "$DARCHIVE" --uploads "$UARCHIVE" >"$WORK/out" 2>&1 || rc=$?
[[ "$rc" == 137 && "$(jq -er .uploads.state "$DIR/recovery.lock")" == exchange-intent && "$(jq -r .up.live "$WORK/model.json")" == 200 ]]
mv "$UARCHIVE" "$WORK/held-uploads"
run_restore --resume >"$WORK/out" 2>&1; phase_is recovering
[[ "$(jq -er .uploads.state "$DIR/recovery.lock")" == exchanged && "$(jq -er .uploads.oldInode "$DIR/recovery.lock")" == 100 ]]
mv "$WORK/held-uploads" "$UARCHIVE"
# Folders that match neither the recorded pair nor its swap go to reconcile-needed
# with nothing exchanged or removed.
seed_up restored A none exchange-intent
change_model '.up.live="999"'
before="$(jq -cS .up "$WORK/model.json")"
rejected 'uploads folders do not match' run_restore --resume
phase_is reconcile-needed
[[ "$(jq -cS .up "$WORK/model.json")" == "$before" ]]
# The recheck only restores the phase; the exchange refuses again, untouched.
run_restore --resume >"$WORK/out" 2>&1; phase_is restored
[[ "$(jq -cS .up "$WORK/model.json")" == "$before" ]]
rejected 'uploads folders do not match' run_restore --resume
phase_is reconcile-needed
rejected 'abort refused' run_restore --abort
[[ "$(jq -cS .up "$WORK/model.json")" == "$before" ]]
# Removal of the old folder and the receipt: killed while it runs, after it
# returns, and with the archive long gone. Each retry converges and removes
# only the recorded tree.
for point in during-uploads-drop after-uploads-drop; do
  seed_up recovering A none exchanged
  hold=(); [[ "$point" != during-uploads-drop ]] || hold=(RESTORE_FIXTURE_HELPER_HOLD=drop)
  rc=0; restore_env RESTORE_DRILL_FAULT="$point" "${hold[@]}" -- --reopen --checked-church-records >"$WORK/out" 2>&1 || rc=$?
  [[ "$rc" == 137 ]]
  phase_is pre-dropped; [[ "$(jq -er .uploads.state "$DIR/recovery.lock")" == old-drop-intent ]]
  mv "$UARCHIVE" "$WORK/held-uploads"
  run_restore --resume >"$WORK/out" 2>&1
  mv "$WORK/held-uploads" "$UARCHIVE"
  [[ ! -e "$DIR/recovery.lock" ]]
  [[ "$(jq -r '[.up.live,.up.staged,(.up.receipt|tostring),.up.dropDone,(.helpers|length|tostring)]|join(",")' "$WORK/model.json")" == 200,none,false,100,0 ]]
done
# The old folder is dropped only under its recorded intent and only by its
# recorded identity: a different identity is refused and nothing is removed.
seed_up pre-dropped C dropped old-drop-intent
change_state '.uploads.oldInode="777"'
rejected 'cleanup-failed at uploads' run_restore --resume
[[ "$(jq -r .up.staged "$WORK/model.json")" == 100 ]]
phase_is cleanup-failed; [[ "$(jq -er .at "$DIR/recovery.lock")" == uploads ]]
change_state '.uploads.oldInode="100"'
run_restore --resume >"$WORK/out" 2>&1; [[ ! -e "$DIR/recovery.lock" && "$(jq -r .up.staged "$WORK/model.json")" == none ]]

# The scheduler is armed after smoke and cleanup, before the lock goes, and a kill
# on either side of arming or of the lock's removal converges with the scheduler
# already armed whenever the lock is absent.
for flavor in service cron; do
  for point in before-scheduler after-scheduler after-lock-remove; do
    seed recovering A none; up_model
    if [[ "$flavor" == cron ]]; then change_model '.backup.defined=false'; fi
    rc=0; restore_env RESTORE_DRILL_FAULT="$point" -- --reopen --checked-church-records >"$WORK/out" 2>&1 || rc=$?
    [[ "$rc" == 137 ]]
    armed=false
    if [[ "$flavor" == service ]]; then armed="$(jq -r .backup.running "$WORK/model.json")"; elif [[ -f "$CRON" ]]; then armed=true; fi
    case "$point" in
      before-scheduler) [[ -e "$DIR/recovery.lock" && "$armed" == false ]] ;;
      after-scheduler) [[ -e "$DIR/recovery.lock" && "$armed" == true ]] ;;
      after-lock-remove) [[ ! -e "$DIR/recovery.lock" && "$armed" == true ]] ;;
    esac
    if [[ -e "$DIR/recovery.lock" ]]; then run_restore --resume >"$WORK/out" 2>&1; fi
    [[ ! -e "$DIR/recovery.lock" ]]
    if [[ "$flavor" == service ]]; then [[ "$(jq -r .backup.running "$WORK/model.json")" == true && ! -e "$CRON" ]]
    else [[ "$(cron_kind "$DIR")" == full ]]; fi
  done
done
# A recovery never replaces a host full cron with expire-only, and a pin to a
# Compose with no backup service puts the full cron back over an expire-only one.
seed recovering A none; up_model
cron_install "$DIR" full
run_restore --reopen --checked-church-records >"$WORK/out" 2>&1
[[ "$(cron_kind "$DIR")" == full && "$(jq -r .backup.running "$WORK/model.json")" == true ]]
seed recovering A none; up_model
change_model '.backup.defined=false'
cron_install "$DIR" expire
[[ "$(cron_kind "$DIR")" == expire ]]
run_restore --reopen --checked-church-records >"$WORK/out" 2>&1
[[ "$(cron_kind "$DIR")" == full ]]
# A pinned backup service that does not share the locks is never started: the
# site stays open, the lock stays, and the phase says the scheduler failed.
seed recovering A none; up_model
change_model '.backup.coord="unset"'
rejected 'cleanup-failed at scheduler' run_restore --reopen --checked-church-records
phase_is cleanup-failed; [[ "$(jq -er .at "$DIR/recovery.lock")" == scheduler ]]
[[ "$(jq -r '[.backup.running,.app.running,.caddy.running]|map(tostring)|join(",")' "$WORK/model.json")" == false,true,true ]]
if grep -q 'start-backup' "$WORK/events"; then exit 1; fi
change_model '.backup.coord="ok"'
run_restore --resume >"$WORK/out" 2>&1
[[ ! -e "$DIR/recovery.lock" && "$(jq -r .backup.running "$WORK/model.json")" == true ]]
# A pinned backup service whose resolved identity is not exactly the shipped one
# is never started either: the site stays open, the lock stays and the phase says
# the scheduler failed. The shipped identity then resumes. One case each for an
# unset field, a wrong value and an added privilege; the whole rule is tabled in
# backup-archives-fixtures.sh (case_identity).
for override in user-unset capdrop-partial privileged; do
  seed recovering A none; up_model
  case "$override" in
    user-unset) change_model '.backup.user="unset"' ;;
    capdrop-partial) change_model '.backup.capDrop=["NET_RAW"]' ;;
    privileged) change_model '.backup.privileged=true' ;;
  esac
  rejected 'cleanup-failed at scheduler' run_restore --reopen --checked-church-records
  phase_is cleanup-failed; [[ "$(jq -er .at "$DIR/recovery.lock")" == scheduler ]]
  [[ "$(jq -r '[.backup.running,.app.running,.caddy.running]|map(tostring)|join(",")' "$WORK/model.json")" == false,true,true ]]
  if grep -q 'start-backup' "$WORK/events"; then exit 1; fi
  change_model 'del(.backup.user, .backup.capDrop, .backup.privileged)'
  run_restore --resume >"$WORK/out" 2>&1
  [[ ! -e "$DIR/recovery.lock" && "$(jq -r .backup.running "$WORK/model.json")" == true ]]
done
# An abort arms the scheduler the same way.
seed collected B; up_model
run_restore --abort >"$WORK/out" 2>&1
[[ ! -e "$DIR/recovery.lock" && "$(jq -r .backup.running "$WORK/model.json")" == true ]]

# backup.lock: root, group 65532, mode 640 after every host acquisition, the
# same inode, and nothing else in the directory loosened.
seed quiesce Q; rm "$DIR/recovery.lock"; chmod 755 "$DIR"
[[ "$(stat -c %a "$DIR/backup.lock")" == 600 ]]
lock_id="$(stat -c '%d:%i' "$DIR/backup.lock")"
for round in 1 2; do
  ( backup_coord_acquire "$DIR" )
  [[ "$(stat -c '%u:%g:%a' "$DIR/backup.lock")" == 0:65532:640 && "$lock_id" == "$(stat -c '%d:%i' "$DIR/backup.lock")" ]]
done
[[ "$(stat -c '%u:%a' "$DIR/.env")" == 0:600 ]]
# A real uid 65532 locks the inode through a read-only handle, and the host and
# that uid exclude one another in both directions.
if command -v setpriv >/dev/null 2>&1; then
  as_backup() { setpriv --reuid 65532 --regid 65532 --clear-groups "$@"; }
  ( backup_coord_acquire "$DIR"
    if as_backup bash -c 'exec 5<"$1"; flock -n -x 5' _ "$DIR/backup.lock"; then exit 1; fi )
  # Started as a plain command, not through as_backup: a backgrounded shell
  # function forks a subshell, so $! would name that wrapper and killing it
  # would leave the real lock holder (setpriv, then bash, then sleep, one pid)
  # alive and still holding the lock.
  setpriv --reuid 65532 --regid 65532 --clear-groups bash -c 'exec 5<"$1"; flock -x 5; exec sleep 20' _ "$DIR/backup.lock" &
  holder=$!
  sleep 1
  [[ "$(cat "/proc/$holder/comm")" == sleep && "$(stat -c %u "/proc/$holder")" == 65532 ]]
  rc=0; ( backup_coord_acquire "$DIR" 1 restore ) >/dev/null 2>&1 || rc=$?
  [[ "$rc" == 75 ]]
  kill "$holder"; wait "$holder" 2>/dev/null || true
  if kill -0 "$holder" 2>/dev/null; then exit 1; fi
  ( backup_coord_acquire "$DIR" )
  if as_backup test -r "$DIR/.env"; then exit 1; fi
  [[ "$lock_id" == "$(stat -c '%d:%i' "$DIR/backup.lock")" ]]
fi

# provision --pin-only writes FOLD_BACKUP_COORD (byte-stable on a repeat) and
# pulls the backup image with the app when the target Compose defines it; a
# failed backup pull is a pin failure that restores the saved pin.
seed pin-intent B; up_model
for round in 1 2; do
  ( ops_lock_acquire "$DIR" fixture
    "$DIR/recovery-tools/provision.sh" --pin-only --skip-harden --deploy-dir "$DIR" --source-dir "$WORK/source" --domain groups.example.org --image fixture:target --allow-os --recovery-nonce "$NONCE" >"$WORK/out" 2>&1 )
  [[ "$(env_value "$DIR/.env" FOLD_BACKUP_COORD)" == "$DIR" ]]
  [[ "$round" == 1 ]] || [[ "$env_sum" == "$(sha256sum "$DIR/.env")" ]]
  env_sum="$(sha256sum "$DIR/.env")"
done
grep -q "compose --project-directory $DIR pull app backup" "$WORK/calls"
seed pin-intent B none partial; change_state '.target=null'; up_model
change_model '.backupPullFail=true'
before_env="$(cat "$DIR/.env")"
rejected 'pin failed; site stays closed' run_restore --resume
phase_is pin-failed
[[ "$before_env" == "$(cat "$DIR/.env")" ]]

echo 'restore review F8-F10 fixtures: ok'
echo 'restore installed CLI fixtures: ok'
