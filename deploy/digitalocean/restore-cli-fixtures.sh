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
done
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
seed quiesce Q; rm "$DIR/recovery.lock"
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

echo 'restore review F8-F10 fixtures: ok'
echo 'restore installed CLI fixtures: ok'
