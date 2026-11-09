#!/usr/bin/env bash
# Disposable, daemon-free regressions for restore drill setup and barriers.
# shellcheck disable=SC2016 # Child shell/jq variables are deliberately literal.
set -Eeuo pipefail
exec </dev/null
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
# shellcheck source=deploy/digitalocean/checks.sh
source "$ROOT/deploy/digitalocean/checks.sh"
# shellcheck source=deploy/digitalocean/restore-drill-cases.sh
source "$ROOT/deploy/digitalocean/restore-drill-cases.sh"
[[ "$(id -u)" == 0 ]] || { echo 'run reset fixtures in the disposable dry-run container/user namespace' >&2; exit 1; }
WORK="$(mktemp -d /tmp/fold-drill-reset.XXXXXX)"
# shellcheck disable=SC2329 # EXIT trap.
finish() { local rc=$?; if ((rc)); then [[ ! -f "$WORK/calls" ]] || tail -n 20 "$WORK/calls" >&2; fi; rm -rf -- "$WORK"; exit "$rc"; }
trap finish EXIT
trap 'printf "restore reset fixture failed at line %s: %s\n" "$LINENO" "$BASH_COMMAND" >&2' ERR
DIR="$WORK/site"; PROJECT="fold-deploy-drill-reset-fixture"
mkdir "$DIR" "$WORK/bin"
printf 'fold-deploy-drill-v1:%s\n' "$DIR" >"$DIR/.deploy-drill"; chmod 600 "$DIR/.deploy-drill"
printf 'kept recovery intent\n' >"$DIR/recovery.lock"; chmod 600 "$DIR/recovery.lock"
: >"$DIR/ops.lock"; chmod 600 "$DIR/ops.lock"
R=fold_restore_fa78e68485f90461
EXTRA=fold_restore_ffffffffffffffff
P=fold_pre_restore_0123456789abcdef
cat >"$WORK/bin/docker" <<'DOCKER'
#!/usr/bin/env bash
set -euo pipefail
: "${RESET_FIXTURE:?}"
# exec -T preserves stdin. Behave like a client that reads all attached input
# even when psql was given -c: this is the real reset loop's failure trigger.
input="$(cat)"
printf '%s | stdin-bytes=%s\n' "$*" "${#input}" >>"$RESET_FIXTURE/calls"
model="$RESET_FIXTURE/model.json"
update() { jq "$@" "$model" >"$RESET_FIXTURE/next"; mv "$RESET_FIXTURE/next" "$model"; }
case "$1" in
  ps) jq -r '.recoveryIds[]' "$model"; exit 0 ;;
  inspect)
    [[ "${2:-}" == --format && "${4:-}" == recovery-id ]] || exit 99
    [[ "$(jq -r '.inspectFail // false' "$model")" != true ]] || exit 1
    jq -r '.healthStates[([.healthIndex, (.healthStates|length)-1]|min)]' "$model"
    update '.healthIndex+=1'
    exit 0 ;;
  exec)
    [[ "${2:-}" == recovery-id && "${3:-}" == /fold && "${4:-}" == healthcheck ]] || exit 99
    update '.probes=(.probes // 0)+1 | if .probeChangesIdentity then .recoveryIds=["replacement-id"] else . end'
    [[ "$(jq -r '.probeOK // false' "$model")" == true ]]
    exit ;;
esac
[[ "$1" == compose && "$2" == --project-directory && "$3" == "$RESET_FIXTURE/site" && "$4" == exec && "$5" == -T && "$6" == db ]] || exit 99
shift 6
if [[ "$1" == pg_dump ]]; then
  echo 'dump' >>"$RESET_FIXTURE/dumps"
  printf '%s\n' '-- dump header' '\restrict fixture' 'sentinel data' '\unrestrict fixture'
  exit 0
fi
[[ "$1" == psql ]] || exit 99
shift
sql=''
while (( $# )); do
  case "$1" in -c) sql+="$2"$'\n'; shift 2 ;; -U|-d|-v) shift 2 ;; *) shift ;; esac
done
if [[ "$sql" == *'SELECT datname FROM pg_database'* ]]; then
  update '.queries+=1'
  [[ "$(jq -r '.queries==(.queryFailAt // -1)' "$model")" != true ]] || exit 1
  jq -r '(.dbs|map(select(.=="fold" or test("^fold_(pre_)?restore_[0-9a-f]{16}$")))|sort) + (.extraCensus // []) | .[]' "$model"
elif [[ "$sql" == *'DROP DATABASE'* ]]; then
  [[ "$sql" =~ DROP\ DATABASE\ \"([a-z0-9_]+)\" ]] || exit 99
  name="${BASH_REMATCH[1]}"
  [[ "$(jq -r --arg name "$name" '.dropFail // "" | .==$name' "$model")" != true ]] || exit 1
  echo "$name" >>"$RESET_FIXTURE/drops"
  update --arg name "$name" 'if .dropNoop==$name then . else .dbs-=[$name] end'
elif [[ "$sql" == *'SELECT count(*) FROM pg_stat_activity'* ]]; then
  [[ "$(jq -r '.clientQueryFail // false' "$model")" != true ]] || exit 1
  jq -r '.clients' "$model"
elif [[ "$sql" == *'SELECT tablename FROM pg_tables'* ]]; then
  [[ "$(jq -r '.tableListFail // false' "$model")" != true ]] || exit 1
  jq -r '.tables|keys[]' "$model"
elif [[ "$sql" =~ SELECT\ count\(\*\)\ FROM\ \"([a-z0-9_]+)\" ]]; then
  table="${BASH_REMATCH[1]}"
  [[ "$(jq -r --arg table "$table" '.countFail // "" | .==$table' "$model")" != true ]] || exit 1
  jq -r --arg table "$table" '.tables[$table]' "$model"
else echo "unhandled fixture SQL: $sql" >&2; exit 99; fi
DOCKER
chmod 755 "$WORK/bin/docker"
export PATH="$WORK/bin:$PATH" RESET_FIXTURE="$WORK" DEPLOY_DRILL=1
unset DEPLOY_DRILL_PAUSE
seed() {
  jq -n --arg r "$R" --arg p "$P" --arg extra "$EXTRA" '{dbs:["fold",$r,$p,$extra,"postgres","template0","template1","owner_shadow","fold_restore_test"],queries:0,tables:{audit_logs:2,groups:3,users:5}}' >"$WORK/model.json"
  : >"$WORK/calls"; : >"$WORK/drops"; : >"$WORK/dumps"
}
# Reproduce the old stream hazard first. The client consumes all remaining
# names after dropping fold, leaving both the real and tampered nonce DBs.
seed
while IFS= read -r name; do
  pg postgres -c "DROP DATABASE \"$name\""
done < <(pg postgres -c 'SELECT datname FROM pg_database')
[[ "$(cat "$WORK/drops")" == fold ]]
jq -e --arg r "$R" --arg extra "$EXTRA" '.dbs|index($r)!=null and index($extra)!=null' "$WORK/model.json" >/dev/null
# The real helper handles zero/one/many entries and a missing original. It
# preserves unrelated DBs, recovery.lock and the stable operations-lock inode.
for state in all no-fold only-fold empty; do
  seed
  case "$state" in
    no-fold) jq '.dbs-=["fold"]' "$WORK/model.json" >"$WORK/edit"; mv "$WORK/edit" "$WORK/model.json" ;;
    only-fold) jq '.dbs|=map(select(.=="fold" or (test("^fold_(pre_)?restore_[0-9a-f]{16}$")|not)))' "$WORK/model.json" >"$WORK/edit"; mv "$WORK/edit" "$WORK/model.json" ;;
    empty) jq '.dbs|=map(select((.=="fold" or test("^fold_(pre_)?restore_[0-9a-f]{16}$"))|not))' "$WORK/model.json" >"$WORK/edit"; mv "$WORK/edit" "$WORK/model.json" ;;
  esac
  expected="$(jq -r '.dbs|map(select(.=="fold" or test("^fold_(pre_)?restore_[0-9a-f]{16}$")))|sort|.[]' "$WORK/model.json")"
  inode="$(stat -c '%d:%i' "$DIR/ops.lock")"; before="$(cat "$DIR/recovery.lock")"
  reset_fixture_databases
  [[ "$(cat "$WORK/drops")" == "$expected" ]]
  [[ "$(stat -c '%d:%i' "$DIR/ops.lock")" == "$inode" && "$(cat "$DIR/recovery.lock")" == "$before" ]]
  jq -e '.dbs|sort==["fold_restore_test","owner_shadow","postgres","template0","template1"]' "$WORK/model.json" >/dev/null
  [[ "$(jq -r .queries "$WORK/model.json")" == 2 ]]
done
# Fail closed on census errors/unexpected names before any destructive call,
# and refuse to advance if a drop failed or the postcondition still has a DB.
for mutation in '.queryFailAt=1' '.extraCensus=["owner_shadow"]' '.dropFail="fold"' ".dropNoop=\"$EXTRA\"" '.queryFailAt=2'; do
  seed; jq "$mutation" "$WORK/model.json" >"$WORK/edit"; mv "$WORK/edit" "$WORK/model.json"
  if reset_fixture_databases >"$WORK/out" 2>&1; then exit 1; fi
  if [[ "$mutation" == '.queryFailAt=1' || "$mutation" == '.extraCensus=["owner_shadow"]' || "$mutation" == '.dropFail="fold"' ]]; then [[ ! -s "$WORK/drops" ]]; fi
  [[ "$(cat "$DIR/recovery.lock")" == 'kept recovery intent' ]]
done
# Safety boundary checks must reject before contacting even the Docker double.
for refusal in flag project marker; do
  seed
  case "$refusal" in flag) DEPLOY_DRILL=0 ;; project) PROJECT=owner ;; marker) mv "$DIR/.deploy-drill" "$WORK/marker" ;; esac
  if reset_fixture_databases >"$WORK/out" 2>&1; then exit 1; fi
  [[ ! -s "$WORK/calls" ]]
  DEPLOY_DRILL=1; PROJECT="fold-deploy-drill-reset-fixture"
  [[ ! -e "$WORK/marker" ]] || mv "$WORK/marker" "$DIR/.deploy-drill"
done
# Complete table counts must survive the same stdin-consuming client.
seed
logical_snapshot "$WORK/snapshot"
printf 'audit_logs|2\ngroups|3\nusers|5\n' >"$WORK/expected.counts"
cmp "$WORK/expected.counts" "$WORK/snapshot.counts"
[[ "$(cat "$WORK/snapshot.data")" == 'sentinel data' ]]
for mutation in '.tableListFail=true' '.countFail="groups"'; do
  seed; jq "$mutation" "$WORK/model.json" >"$WORK/edit"; mv "$WORK/edit" "$WORK/model.json"
  if logical_snapshot "$WORK/failed" >"$WORK/out" 2>&1; then exit 1; fi
  [[ ! -s "$WORK/dumps" ]]
done
echo 'restore drill reset fixtures: ok'

# A successful direct probe does not establish Docker's health state. Reuse
# must wait for starting -> healthy on the captured ID, with no replacement,
# service mutation or W=0 query to make the precondition true.
seed_health() {
  seed
  jq '. + {recoveryIds:["recovery-id"],healthStates:["true|starting","true|starting","true|healthy"],healthIndex:0,probeOK:true,clients:2}' "$WORK/model.json" >"$WORK/edit"
  mv "$WORK/edit" "$WORK/model.json"
}
assert_health_read_only() {
  if grep -Ev '^(ps -aq |inspect --format |exec recovery-id /fold healthcheck )' "$WORK/calls"; then
    echo 'health precondition made an unexpected service/SQL call' >&2; return 1
  fi
  if grep -qv 'stdin-bytes=0$' "$WORK/calls"; then return 1; fi
}
seed_health
docker exec recovery-id /fold healthcheck </dev/null
[[ "$(jq -r '.healthIndex' "$WORK/model.json")" == 0 ]]
wait_recovery_healthy recovery-id 5 >"$WORK/health.out"
grep -q 'health=true|starting' "$WORK/health.out"
grep -q 'health=true|healthy' "$WORK/health.out"
jq -e '.healthIndex==3 and .probes==2 and .recoveryIds==["recovery-id"]' "$WORK/model.json" >/dev/null
assert_health_read_only
seed_health
jq '.healthStates=["true|healthy"]' "$WORK/model.json" >"$WORK/edit"; mv "$WORK/edit" "$WORK/model.json"
wait_recovery_healthy recovery-id 5 >"$WORK/health.out"
jq -e '.healthIndex==1 and .probes==1' "$WORK/model.json" >/dev/null
assert_health_read_only
# Do not turn unhealthy, stopped, missing-health or changed-ID containers
# into a passing healthy-reuse case. A direct probe failure also fails shut.
for mutation in '.healthStates=["true|unhealthy"]' '.healthStates=["false|healthy"]' '.healthStates=["true|"]' \
  '.inspectFail=true' '.recoveryIds=[]' '.recoveryIds=["replacement-id"]' \
  '.recoveryIds=["recovery-id","extra-id"]' '.healthStates=["true|healthy"] | .probeOK=false' \
  '.healthStates=["true|healthy"] | .probeChangesIdentity=true' '.healthStates=["true|starting"]'; do
  seed_health
  jq "$mutation" "$WORK/model.json" >"$WORK/edit"; mv "$WORK/edit" "$WORK/model.json"
  if wait_recovery_healthy recovery-id 1 >"$WORK/health.out" 2>&1; then exit 1; fi
  assert_health_read_only
done
# The separate SQL proof must show live recovery clients, not merely an HTTP
# response. A failed census or zero clients cannot satisfy that requirement.
seed_health
assert_recovery_clients_present >"$WORK/clients.out"
grep -q 'client backends=2' "$WORK/clients.out"
for mutation in '.clients=0' '.clients="bad"' '.clientQueryFail=true'; do
  seed_health
  jq "$mutation" "$WORK/model.json" >"$WORK/edit"; mv "$WORK/edit" "$WORK/model.json"
  if assert_recovery_clients_present >"$WORK/clients.out" 2>&1; then exit 1; fi
done
echo 'restore drill healthy reuse fixtures: ok'

# Exercise the actual r1 orchestration with a database-scoped gate model.
# A hold in postgres lets deletion complete immediately; only fold blocks it.
# Stop at the next case after the selected r1/r2 assertions have completed.
cat >"$WORK/count-race-fixture.sh" <<'RACE'
#!/usr/bin/env bash
set -Eeuo pipefail
source "$1"
WORK="$2"; DIR="$WORK/site"; DB_DUMP=fixture.dump; C=30; B=31; ADMIN=1; GROUP=2
mode="${3:-r1}"
if [[ "$mode" == r2 ]]; then source "$4/deploy/digitalocean/checks.sh"; fi
PROJECT=fold-deploy-drill-count-fixture
mkdir -p "$DIR"
printf 'fold-deploy-drill-v1:%s\n' "$DIR" >"$DIR/.deploy-drill"; chmod 600 "$DIR/.deploy-drill"
iteration=0
reset_case() {
  iteration=$((iteration+1))
  if (( iteration==3 )) && [[ "$mode" == r1 ]]; then echo r1-complete >"$WORK/completed"; return 88; fi
  if (( iteration==4 )); then echo r2-complete >"$WORK/completed"; return 88; fi
  mkdir "$WORK/case-$iteration"
  mkfifo "$WORK/case-$iteration/release"
  echo f >"$WORK/case-$iteration/tombstoned"
  echo live >"$WORK/current-sentinel"
}
sql_hold() {
  if (( iteration==3 )); then
    [[ "$1" == request && "$2" == fold && "$3" == 'SELECT id FROM contact_requests WHERE id=52 FOR UPDATE' ]]
  else
    [[ "$1" == gate && "$3" == "SELECT pg_advisory_lock($((16#464f4c44434f4e)))" ]]
  fi
  echo "$2" >"$WORK/case-$iteration/holder-database"
  SQL_BACKEND=700
}
api() {
  [[ "$1" == DELETE ]]
  if (( iteration==3 )); then [[ "$2" == /admin/users/31 ]]; else [[ "$2" == /admin/users/30 ]]; fi
  if [[ "$(cat "$WORK/case-$iteration/holder-database")" == fold ]]; then
    echo waiting >"$WORK/case-$iteration/waiting"
    IFS= read -r token <"$WORK/case-$iteration/release"
    [[ "$token" == continue ]]
  fi
  echo t >"$WORK/case-$iteration/tombstoned"
  echo '{"erasure":{}}'
}
sql_release() {
  if (( iteration==3 )); then [[ "$1" == request ]]; else [[ "$1" == gate ]]; fi
  timeout 5 bash -c 'echo continue >"$1"' _ "$WORK/case-$iteration/release"
}
pg() {
  case "$*" in
    *'SELECT count(*) FROM pg_stat_activity'*)
      [[ "$*" == *"datname='fold'"* && "$*" == *'700=ANY(pg_blocking_pids(pid))'* ]]
      if (( iteration==3 )); then [[ "$*" == *"query LIKE '%contact_requests%'"* ]]; else [[ "$*" == *"wait_event='advisory'"* && "$*" == *"query LIKE 'SELECT pg_advisory_xact_lock%'"* ]]; fi
      echo checked >>"$WORK/case-$iteration/barrier-queries"
      if [[ "$(cat "$WORK/case-$iteration/holder-database")" == fold && -f "$WORK/case-$iteration/waiting" ]]; then echo 1; else echo 0; fi ;;
    *'SELECT deleted_at IS NOT NULL FROM users WHERE id=30'*) cat "$WORK/case-$iteration/tombstoned" ;;
    *'INSERT INTO contact_requests('* ) [[ "$iteration" == 3 ]] ;;
    *'SELECT max(id) FROM contact_requests WHERE subject_user_id=31'*) echo 52 ;;
    *'SELECT clock_timestamp()'*) echo '2030-01-02 00:00:00+00' ;;
    *"UPDATE drill_sentinel SET value='archive' WHERE id=2"*) echo archive >"$WORK/current-sentinel" ;;
    *'SELECT value FROM drill_sentinel WHERE id=2'*) cat "$WORK/current-sentinel" ;;
    *"SELECT at FROM audit_logs WHERE action='user.deleted' AND target_id=31"*) echo '2030-01-01 23:59:59+00' ;;
    *'SELECT max(at) < '* ) echo t ;;
    *) echo "unexpected race fixture SQL: $*" >&2; return 99 ;;
  esac
}
restore() {
  [[ "$1" == --dump ]]
  if (( iteration==3 )); then
    [[ "$2" == "$WORK/straddling-b.dump" && "$(cat "$2")" == archive && "$(cat "$WORK/current-sentinel")" == live ]]
    cp "$2" "$WORK/restored-sentinel"
  else [[ "$2" == fixture.dump ]]; fi
  if [[ -f "$WORK/stop-barrier" ]]; then
    echo stop-app >"$WORK/stop.ready"
    IFS= read -r token <"$WORK/stop.fifo"
    [[ "$token" == continue ]]
  fi
  [[ "$(cat "$WORK/case-$iteration/tombstoned")" == t ]]
  echo t >"$WORK/collection-race-user"
  if (( iteration==3 )); then printf '{"count":1}\n' >"$DIR/recovery.lock"; else printf '{"count":2}\n' >"$DIR/recovery.lock"; fi
  echo "$iteration" >>"$WORK/restores"
}
dc() {
  [[ "$*" == 'exec -T db pg_dump -U church -Fc fold' && "$iteration" == 3 ]]
  # The snapshot must precede B's commit, even while fixture sentinel data
  # is changed independently of the row lock holding that deletion.
  [[ "$(cat "$WORK/case-3/tombstoned")" == f ]]
  echo t >"$WORK/b-active-in-archive"
  cat "$WORK/current-sentinel"
}
capture_original() {
  [[ -s "$WORK/straddling-b.dump" ]]
  echo live >"$WORK/current-sentinel"
  echo captured >"$WORK/live-heap-recaptured"
}
assert_private() {
  [[ "$(cat "$WORK/restored-sentinel")" == archive && "$(cat "$WORK/current-sentinel")" == live && -f "$WORK/live-heap-recaptured" ]]
  [[ "$(cat "$WORK/b-active-in-archive")" == t && "$(cat "$WORK/case-3/tombstoned")" == t ]]
  echo 'r2 archive=archive original=live B-active-in-archive=t'
}
export WORK DIR iteration
export -f restore
cat >"$DIR/restore.sh" <<'RESTORE'
#!/usr/bin/env bash
set -Eeuo pipefail
[[ "$1" == --deploy-dir && "$2" == "$DIR" && "$3" == --drill ]]
shift 3
restore "$@"
RESTORE
chmod 755 "$DIR/restore.sh"
count_races
exit 99
RACE
rc=0
bash "$WORK/count-race-fixture.sh" "$ROOT/deploy/digitalocean/restore-drill-cases.sh" "$WORK/r1" >"$WORK/r1.log" 2>&1 || rc=$?
[[ "$rc" == 88 && "$(cat "$WORK/r1/completed")" == r1-complete && "$(cat "$WORK/r1/restores")" == $'1\n2' ]]
grep -q 'r1 timing=before-stop C-at-collection=t count=2' "$WORK/r1.log"
grep -q 'r1 timing=during-stop C-at-collection=t count=2' "$WORK/r1.log"
for i in 1 2; do [[ -s "$WORK/r1/case-$i/barrier-queries" && "$(cat "$WORK/r1/case-$i/holder-database")" == fold ]]; done
# Mutation control: the original cross-database hold must fail the barrier,
# even though the API reached the app and returned a successful erasure.
sed 's/sql_hold gate fold /sql_hold gate postgres /' "$ROOT/deploy/digitalocean/restore-drill-cases.sh" >"$WORK/wrong-database.sh"
rc=0
bash "$WORK/count-race-fixture.sh" "$WORK/wrong-database.sh" "$WORK/wrong-r1" >"$WORK/wrong-r1.log" 2>&1 || rc=$?
[[ "$rc" != 0 && "$rc" != 88 && ! -f "$WORK/wrong-r1/completed" ]]
grep -q 'request exited before its SQL barrier' "$WORK/wrong-r1.log"
grep -q '"erasure"' "$WORK/wrong-r1.log"

# Continue the actual count_races function through the straddling-B dump.
# The archive marker must differ from the preserved live original, while B
# remains active in the dump and its earlier audit time is still asserted.
rc=0
bash "$WORK/count-race-fixture.sh" "$ROOT/deploy/digitalocean/restore-drill-cases.sh" "$WORK/r2" r2 "$ROOT" >"$WORK/r2.log" 2>&1 || rc=$?
[[ "$rc" == 88 && "$(cat "$WORK/r2/completed")" == r2-complete && "$(cat "$WORK/r2/restores")" == $'1\n2\n3' ]]
grep -q 'r2 archive=archive original=live B-active-in-archive=t' "$WORK/r2.log"
# Restoring the original raw dump call must reproduce the sentinel failure.
sed 's/dump_current_fixture_archive "\$DB_DUMP"/dc exec -T db pg_dump -U church -Fc fold >"$DB_DUMP"/' "$ROOT/deploy/digitalocean/restore-drill-cases.sh" >"$WORK/wrong-sentinel.sh"
rc=0
bash "$WORK/count-race-fixture.sh" "$WORK/wrong-sentinel.sh" "$WORK/wrong-r2" r2 "$ROOT" >"$WORK/wrong-r2.log" 2>&1 || rc=$?
[[ "$rc" != 0 && "$rc" != 88 && ! -f "$WORK/wrong-r2/completed" ]]
[[ "$(cat "$WORK/wrong-r2/straddling-b.dump")" == live ]]
echo 'restore drill archive sentinel fixtures: ok'

# The waiter census must fail closed on SQL errors, malformed output, an
# unrelated waiter, or a request which has already returned. Both kinds of
# lock wait are tied to the identified holder in fold.
# shellcheck disable=SC2030,SC2031 # Each isolated fixture initializes its own holder state.
(
  sleep 15 & request_pid=$!
  trap 'kill "$request_pid" 2>/dev/null || true; wait "$request_pid" 2>/dev/null || true' EXIT
  SQL_BACKEND=700
  printf 'fixture response\n' >"$WORK/request-response"
  pg() {
    [[ "$*" == *"datname='fold'"* && "$*" == *'700=ANY(pg_blocking_pids(pid))'* ]] || return 99
    [[ "${query_fail:-0}" == 0 ]] || return 1
    printf '%s\n' "${waiters:-1}"
  }
  for kind in gate request; do wait_delete_blocked "$request_pid" "$WORK/request-response" "$kind" 2; done
  query_fail=1
  if wait_delete_blocked "$request_pid" "$WORK/request-response" gate 1; then exit 1; fi
  query_fail=0; waiters=bad
  if wait_delete_blocked "$request_pid" "$WORK/request-response" gate 1; then exit 1; fi
  waiters=0
  if wait_delete_blocked "$request_pid" "$WORK/request-response" gate 1 >"$WORK/no-waiter.log" 2>&1; then exit 1; fi
  grep -q 'did not block on holder 700 in fold' "$WORK/no-waiter.log"
  kill "$request_pid"; wait "$request_pid" 2>/dev/null || true
  if wait_delete_blocked "$request_pid" "$WORK/request-response" gate 1 >"$WORK/finished-request.log" 2>&1; then exit 1; fi
  grep -q 'request exited before its SQL barrier' "$WORK/finished-request.log"
)
# Run the real SQL-holder pipe/FIFO and PID handshake with a streaming psql
# double. It emits readiness only after receiving the locking statement.
# shellcheck disable=SC2030,SC2031 # sql_hold initializes this fixture's own holder state.
(
  dc() {
    [[ "$*" == *'-d fold '* ]]
    local line locked=0
    while IFS= read -r line; do
      case "$line" in
        'SELECT pg_advisory_lock(17);') locked=1 ;;
        '\echo SQL-READY|:drill_holder_pid') [[ "$locked" == 1 ]]; echo 'SQL-READY|700' ;;
        'COMMIT;') echo committed >"$WORK/holder-committed"; return ;;
      esac
    done
    return 99
  }
  sql_hold fixture-holder fold 'SELECT pg_advisory_lock(17)'
  [[ "$SQL_BACKEND" == 700 && ! -e "$WORK/holder-committed" ]]
  sql_release fixture-holder
  [[ -f "$WORK/holder-committed" && -z "$SQL_BACKEND" ]]
)
echo 'restore drill count-race barrier fixtures: ok'

# The orphaned-restore wrapper must stop after installing its event trigger
# in R, before exec'ing pg_restore. This is where a holder can first attach to
# that same nonce database. No Docker daemon is involved.
cat >"$WORK/bin/orphan-docker" <<'DOCKER'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$RESET_FIXTURE/orphan-calls"
if [[ "$*" == *' psql '* ]]; then
  input="$(cat)"
  [[ "$input" == *'PERFORM pg_advisory_lock(6467261)'* && "$input" == *'CREATE EVENT TRIGGER drill_pause_restore'* ]]
elif [[ "$*" == *' pg_restore '* ]]; then
  [[ -f "$RESET_FIXTURE/orphan-held" ]] || { echo 'restore started before same-database gate acquisition' >&2; exit 1; }
else exit 99; fi
DOCKER
chmod 755 "$WORK/bin/orphan-docker"
export RESTORE_DRILL_REAL_DOCKER="$WORK/bin/orphan-docker" RESTORE_DRILL_SITE="$DIR" RESTORE_DRILL_WORK="$WORK"
: >"$WORK/orphan-calls"
mkfifo "$WORK/during-restore.fifo"
env RESTORE_DRILL_FAULT=during-restore "$ROOT/deploy/digitalocean/testdata/restore-drill-docker.sh" \
  compose --project-directory "$DIR" exec -T db pg_restore -U church -d "$R" --no-owner </dev/null >"$WORK/orphan-wrapper.log" 2>&1 & wrapper=$!
wait_file "$WORK/during-restore.client" '^[0-9][0-9]*$'
[[ "$(cat "$WORK/during-restore.client")" == "$wrapper" ]]
[[ "$(wc -l <"$WORK/orphan-calls")" == 1 ]]
grep -q "psql .* -d $R " "$WORK/orphan-calls"
if grep -q ' pg_restore ' "$WORK/orphan-calls"; then exit 1; fi
touch "$WORK/orphan-held"
timeout 5 bash -c 'echo continue >"$1"' _ "$WORK/during-restore.fifo"
wait "$wrapper"
[[ "$(wc -l <"$WORK/orphan-calls")" == 2 ]]
grep -q "pg_restore .* -d $R " "$WORK/orphan-calls"
rm -f "$WORK/during-restore.fifo"
echo 'restore drill orphan barrier fixtures: ok'

# Run the held-R acceptance case through the actual expected-refusal helper
# and a child CLI process. Virtual elapsed time models the 180-second SQL
# watchdog without spending minutes waiting or connecting to a database.
sed -n '/^expect_fail() {/,/^}/p' "$ROOT/deploy/digitalocean/restore-drill.sh" >"$WORK/expect-fail.sh"
cat >"$WORK/held-client-fixture.sh" <<'HELD'
#!/usr/bin/env bash
set -Eeuo pipefail
source "$1"
WORK="$2"; DIR="$WORK/site"; DB_DUMP=fixture.dump; SOURCE=fixture-source; ALT_IMAGE=fixture:target
source "$3"
mkdir -p "$DIR"
export HELD_FIXTURE="$WORK"
holder=""; SQL_HOLDER=""; SQL_BACKEND=""
trap 'for pid in "$holder" "$SQL_HOLDER"; do [[ -z "$pid" ]] || { kill "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true; }; done' EXIT
cat >"$DIR/restore.sh" <<'CLI'
#!/usr/bin/env bash
set -Eeuo pipefail
[[ "$1" == --deploy-dir && "$2" == "$HELD_FIXTURE/site" && "$3" == --drill ]]
shift 3
model="$HELD_FIXTURE/model.json"; lock="$HELD_FIXTURE/site/recovery.lock"
update() { jq "$@" "$model" >"$HELD_FIXTURE/next"; mv "$HELD_FIXTURE/next" "$model"; }
case "$1" in
  --status) echo 'phase=abort-failed classification=B'; exit 0 ;;
  --resume) jq '.phase="recovering"' "$lock" >"$lock.next"; mv "$lock.next" "$lock"; exit 0 ;;
  --abort) ;;
  *) exit 99 ;;
esac
if [[ "$(jq -r .pinned "$model")" == true ]]; then echo 'abort refused'; exit 1; fi
if [[ "$(jq -r .held "$model")" == true ]]; then
  bound="${RESTORE_DRILL_WAIT:-120}"
  echo "abort-wait=$bound" >>"$HELD_FIXTURE/events"
  update --argjson wait "$bound" '.elapsed+=$wait'
  if (( $(jq -r .elapsed "$model")>=180 )); then
    kill "$(jq -r .pid "$model")"
    update '.held=false | .expired=true'
    echo holder-expired >>"$HELD_FIXTURE/events"
  else
    jq '.phase="abort-failed" | .at="drop" | .history=["abort-drop-intent"]' "$lock" >"$lock.next"; mv "$lock.next" "$lock"
    echo 'abort-failed at=drop; prior intent kept'; exit 1
  fi
fi
update '.drops+=1'
rm "$lock"
echo 'abort complete; original reopened'
CLI
chmod 755 "$DIR/restore.sh"
reset_case() {
  printf '{"held":false,"pinned":false,"elapsed":0,"drops":0,"expired":false}\n' >"$WORK/model.json"
  printf '{"phase":"collected"}\n' >"$DIR/recovery.lock"
}
start_paused() {
  [[ "$1" == before-swap && "${RESTORE_DRILL_WAIT:-}" == 2 ]]
  if [[ "$*" == *' --image '* ]]; then jq '.pinned=true' "$WORK/model.json" >"$WORK/edit"; mv "$WORK/edit" "$WORK/model.json"; fi
  rm -f "$DIR/drill.fifo"; mkfifo "$DIR/drill.fifo"
  (
    IFS= read -r token <"$DIR/drill.fifo"
    [[ "$token" == continue ]]
    jq '.elapsed=2' "$WORK/model.json" >"$WORK/edit"; mv "$WORK/edit" "$WORK/model.json"
    printf '{"phase":"swap-failed"}\n' >"$DIR/recovery.lock"
    exit 1
  ) & holder=$!
}
nonce_db() { echo fold_restore_0123456789abcdef; }
sql_hold() {
  [[ "$1" == restore-client && "$2" == fold_restore_0123456789abcdef && "$3" == 'SELECT 1' ]]
  sleep 30 & SQL_HOLDER=$!; SQL_BACKEND="$SQL_HOLDER"
  jq --argjson pid "$SQL_HOLDER" '.held=true | .pid=$pid' "$WORK/model.json" >"$WORK/edit"; mv "$WORK/edit" "$WORK/model.json"
}
sql_release() {
  [[ "$1" == restore-client && "$(jq -r .held "$WORK/model.json")" == true ]]
  kill "$SQL_HOLDER"; wait "$SQL_HOLDER" 2>/dev/null || true
  SQL_HOLDER=""; SQL_BACKEND=""
  jq '.held=false' "$WORK/model.json" >"$WORK/edit"; mv "$WORK/edit" "$WORK/model.json"
}
phase() { jq -r .phase "$DIR/recovery.lock"; }
restore() { RESTORE_DRILL_OPERATION=1 "$DIR/restore.sh" --deploy-dir "$DIR" --drill "$@"; }
pg() {
  case "$*" in
    *'SELECT count(*) FROM pg_stat_activity'*)
      [[ "$*" == *"pid=$SQL_BACKEND AND datname='fold_restore_0123456789abcdef'"* ]]
      echo holder-census >>"$WORK/events"
      if [[ "$(jq -r .held "$WORK/model.json")" == true ]]; then echo 1; else echo 0; fi ;;
    *"SELECT datallowconn FROM pg_database WHERE datname='fold'"*) echo t ;;
    *'SELECT datname,oid,datallowconn FROM pg_database'*) printf 'fold|10|t\nfold_restore_0123456789abcdef|20|t\n' ;;
    *) echo "unexpected held-client fixture SQL: $*" >&2; return 99 ;;
  esac
}
assert_closed() { [[ -f "$DIR/recovery.lock" ]]; }
assert_original_heap() { echo heap-checked >>"$WORK/events"; }
assert_private() { [[ "$(phase)" == recovering ]]; }
reopen_checked() { [[ "$(phase)" == recovering ]]; echo reopened >>"$WORK/events"; }
: >"$WORK/events"
held_restore_client_cases
echo held-client-complete >"$WORK/completed"
HELD
if ! bash "$WORK/held-client-fixture.sh" "$ROOT/deploy/digitalocean/restore-drill-cases.sh" "$WORK/held" "$WORK/expect-fail.sh" >"$WORK/held.log" 2>&1; then
  cat "$WORK/held.log" >&2; exit 1
fi
[[ "$(cat "$WORK/held/completed")" == held-client-complete ]]
[[ "$(grep -c '^abort-wait=2$' "$WORK/held/events")" == 2 && "$(grep -c '^holder-census$' "$WORK/held/events")" == 5 ]]
if grep -q holder-expired "$WORK/held/events"; then exit 1; fi
grep -q reopened "$WORK/held/events"
# Mutation control recreates the bug: two default 120-second retries outlive
# the holder, so the second abort legitimately succeeds and must fail the test.
sed 's/RESTORE_DRILL_WAIT=2 expect_fail abort-failed restore --abort/expect_fail abort-failed restore --abort/' "$ROOT/deploy/digitalocean/restore-drill-cases.sh" >"$WORK/long-abort.sh"
rc=0
bash "$WORK/held-client-fixture.sh" "$WORK/long-abort.sh" "$WORK/expired-held" "$WORK/expect-fail.sh" >"$WORK/expired-held.log" 2>&1 || rc=$?
[[ "$rc" != 0 && ! -f "$WORK/expired-held/completed" ]]
grep -q holder-expired "$WORK/expired-held/events"
grep -q 'abort complete' "$WORK/expired-held.log"
[[ "$(grep -c '^abort-wait=120$' "$WORK/expired-held/events")" == 2 ]]
echo 'restore drill held-client retry fixtures: ok'

# Run the actual lifecycle path through plain-up exclusion, healthy reuse,
# and stopped-container replacement. The Docker double models the crucial
# difference: direct probes pass during starting, but resume reuses healthy
# containers only. No daemon or network listener is involved.
cat >"$WORK/lifecycle-fixture.sh" <<'LIFECYCLE'
#!/usr/bin/env bash
set -Eeuo pipefail
source "$1"
WORK="$2"; DIR="$WORK/site"; DB_DUMP=fixture.dump; fault="${3:-none}"
mkdir -p "$DIR"
update() { jq "$@" "$WORK/model.json" >"$WORK/next"; mv "$WORK/next" "$WORK/model.json"; }
reset_case() {
  jq -n --arg fault "$fault" '{fault:$fault,id:"",generation:0,running:false,normal:true,health:"starting",healthPolls:0,reuses:0,restarted:false}' >"$WORK/model.json"
}
recovery_ids() { jq -r .id "$WORK/model.json"; }
login() { [[ "$URL" == http://localhost:18080 && "$(jq -r .running "$WORK/model.json")" == true ]]; }
docker() {
  case "$1" in
    stop)
      [[ "$2" == "$(recovery_ids)" ]]
      update '.running=false' ;;
    inspect)
      [[ "$2" == --format && "$4" == "$(recovery_ids)" ]]
      case "$3" in
        '{{.State.Running}}') jq -r .running "$WORK/model.json" ;;
        '{{.State.Running}}|{{if .State.Health}}{{.State.Health.Status}}{{end}}')
          update '.healthPolls+=1 | if .healthPolls>=3 then .health="healthy" else . end'
          jq -r '(.running|tostring)+"|"+.health' "$WORK/model.json" ;;
        *) return 99 ;;
      esac ;;
    exec) [[ "$2" == "$(recovery_ids)" && "$3" == /fold && "$4" == healthcheck && "$(jq -r .running "$WORK/model.json")" == true ]] ;;
    rm)
      # This is the first foreign-nonce fixture setup, after the lifecycle
      # assertions under test. End this focused test at that boundary only.
      jq -e '.generation==3 and .reuses==1 and .restarted and .running and (.normal|not)' "$WORK/model.json" >/dev/null
      echo lifecycle-complete >"$WORK/completed"
      return 88 ;;
    *) echo "unexpected lifecycle Docker call: $*" >&2; return 99 ;;
  esac
}
dc() {
  case "$*" in
    'up -d') update '.normal=true' ;;
    'restart db') [[ "$(jq -r .running "$WORK/model.json")" == false ]]; update '.restarted=true' ;;
    *) echo "unexpected lifecycle Compose call: $*" >&2; return 99 ;;
  esac
}
pg() {
  case "$*" in
    *'SELECT count(*) FROM pg_stat_activity'*)
      if [[ "$(jq -r '.fault=="zero-clients" and .generation==2' "$WORK/model.json")" == true ]]; then echo 0; else echo 1; fi ;;
    *'SELECT id,kind,status,attempts,locked_at FROM jobs'*) return 0 ;;
    *) echo "unexpected lifecycle SQL: $*" >&2; return 99 ;;
  esac
}
recreate() {
  update '.generation+=1 | .id="recovery-"+(.generation|tostring) | .running=true | .normal=false | .health="starting" | .healthPolls=0'
  echo 'restore.sh: observed W=0'
}
restore() {
  case "$1" in
    --dump) recreate ;;
    --status) [[ "$(jq -r .normal "$WORK/model.json")" != true ]] || echo WARNING ;;
    --resume)
      if jq -e '.normal or (.running|not) or .health!="healthy" or .fault=="replace-healthy"' "$WORK/model.json" >/dev/null; then
        recreate
      else
        update '.reuses+=1'
        [[ "$(jq -r .fault "$WORK/model.json")" != spurious-W ]] || echo 'restore.sh: observed W=0'
      fi ;;
    *) return 99 ;;
  esac
}
assert_private() { jq -e '.running and (.normal|not)' "$WORK/model.json" >/dev/null; }
assert_no_claims() { assert_private; }
lifecycle_cases
exit 99
LIFECYCLE
rc=0
bash "$WORK/lifecycle-fixture.sh" "$ROOT/deploy/digitalocean/restore-drill-cases.sh" "$WORK/lifecycle" >"$WORK/lifecycle.log" 2>&1 || rc=$?
if [[ "$rc" != 88 ]]; then cat "$WORK/lifecycle.log" >&2; exit 1; fi
[[ "$(cat "$WORK/lifecycle/completed")" == lifecycle-complete ]]
grep -q 'health=true|starting' "$WORK/lifecycle/lifecycle-healthy-resume.health"
grep -q 'health=true|healthy' "$WORK/lifecycle/lifecycle-healthy-resume.health"
if grep -q 'observed W=0' "$WORK/lifecycle/lifecycle-healthy-resume.log"; then exit 1; fi
# The same shared proof must reject replacement, W checks during reuse, and
# a missing live-client precondition, rather than weakening the assertions.
for fault in replace-healthy spurious-W zero-clients; do
  rc=0
  bash "$WORK/lifecycle-fixture.sh" "$ROOT/deploy/digitalocean/restore-drill-cases.sh" "$WORK/lifecycle-$fault" "$fault" >"$WORK/lifecycle-$fault.log" 2>&1 || rc=$?
  [[ "$rc" != 0 && "$rc" != 88 && ! -f "$WORK/lifecycle-$fault/completed" ]]
done
grep -q 'replaced the captured container' "$WORK/lifecycle-replace-healthy.log"
grep -q 'reuse checked W=0' "$WORK/lifecycle-spurious-W.log"
grep -q 'requires established client backends' "$WORK/lifecycle-zero-clients.log"
# Reproduce drill11 by removing the shared wait: freshly created recovery
# remains starting, resume recreates it, and exact-ID preservation must fail.
sed 's@wait_recovery_healthy "\$expected" | tee "\$WORK/\$artifact.health" || return 1@: >"$WORK/$artifact.health"@' "$ROOT/deploy/digitalocean/restore-drill-cases.sh" >"$WORK/no-reuse-wait.sh"
rc=0
bash "$WORK/lifecycle-fixture.sh" "$WORK/no-reuse-wait.sh" "$WORK/lifecycle-no-wait" >"$WORK/lifecycle-no-wait.log" 2>&1 || rc=$?
[[ "$rc" != 0 && "$rc" != 88 && ! -f "$WORK/lifecycle-no-wait/completed" ]]
grep -q 'replaced the captured container' "$WORK/lifecycle-no-wait.log"
echo 'restore drill lifecycle reuse fixtures: ok'

# The legacy archive comparison must select pg_restore's SQL-to-stdout
# mode, propagate client failures and compare all normalized DDL. This
# command double needs no PostgreSQL client/server installation in CI.
(
  DB_DUMP="$WORK/schema-input"
  schema_mode=normal
  cat >"$WORK/schema-base" <<'SQL'
-- Archive header
\restrict archive-token
SET statement_timeout = 0;
SELECT pg_catalog.set_config('search_path', '', false);
CREATE TABLE public.proof (id bigint NOT NULL, value text DEFAULT 'kept');
ALTER TABLE ONLY public.proof ADD CONSTRAINT proof_pkey PRIMARY KEY (id);
CREATE VIEW public.proof_view AS SELECT id FROM public.proof;
CREATE FUNCTION public.proof_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'immutable';
END;
$$;
CREATE TRIGGER proof_guard BEFORE UPDATE ON public.proof FOR EACH ROW EXECUTE FUNCTION public.proof_guard();
CREATE INDEX proof_value_idx ON public.proof USING btree (value);
\unrestrict archive-token
-- Archive footer
SQL
  reset_schema() {
    cp "$WORK/schema-base" "$DB_DUMP"
    sed 's/archive-token/live-token/g;s/SET statement_timeout = 0;/SET lock_timeout = 0;/;s/Archive/Live/g' "$WORK/schema-base" >"$WORK/schema-live"
    : >"$WORK/schema-calls"
  }
  dc() {
    printf '%s\n' "$*" >>"$WORK/schema-calls"
    case "$*" in
      'exec -T db pg_restore --schema-only --no-owner --no-acl --file=-')
        if [[ "$schema_mode" == empty ]]; then echo '-- no DDL'; return 0; fi
        cat
        [[ "$schema_mode" != archive-fail ]] ;;
      'exec -T db pg_dump -U church --schema-only --no-owner --no-acl fold')
        if [[ "$schema_mode" == empty ]]; then echo 'SET statement_timeout = 0;'; return 0; fi
        cat "$WORK/schema-live"
        [[ "$schema_mode" != live-fail ]] ;;
      'exec -T db pg_restore --schema-only --no-owner --no-acl')
        echo 'pg_restore: error: one of -d/--dbname and -f/--file must be specified' >&2; return 1 ;;
      *) echo "unexpected schema comparison command: $*" >&2; return 99 ;;
    esac
  }
  reset_schema
  assert_legacy_archive_schema
  cmp "$WORK/legacy-archive.schema" "$WORK/swapped-before-boot.schema"
  for ddl in 'CREATE TABLE' 'ADD CONSTRAINT' 'CREATE VIEW' 'CREATE FUNCTION' 'CREATE TRIGGER' 'CREATE INDEX'; do
    grep -q "$ddl" "$WORK/legacy-archive.schema"
  done
  grep -q -- '--file=-' "$WORK/schema-calls"
  # Distinct objects, constraints and function/view definitions must remain
  # visible to the exact diff, even after normalizing dump metadata.
  for change in extra-table constraint function-body view-definition missing-index; do
    reset_schema
    case "$change" in
      extra-table) echo 'CREATE TABLE public.drill_live_only (id bigint);' >>"$WORK/schema-live" ;;
      constraint) sed -i 's/PRIMARY KEY (id)/UNIQUE (id)/' "$WORK/schema-live" ;;
      function-body) sed -i 's/immutable/changed/' "$WORK/schema-live" ;;
      view-definition) sed -i 's/AS SELECT id/AS SELECT value/' "$WORK/schema-live" ;;
      missing-index) sed -i '/^CREATE INDEX /d' "$WORK/schema-live" ;;
    esac
    if assert_legacy_archive_schema >"$WORK/schema-$change.diff" 2>&1; then exit 1; fi
    [[ -s "$WORK/schema-$change.diff" ]]
  done
  # Errors can leave perfectly matching partial output; their status still
  # has to stop the procedure. Two empty schemas cannot count as equal.
  for schema_mode in archive-fail live-fail empty; do
    reset_schema
    if assert_legacy_archive_schema >"$WORK/schema-$schema_mode.log" 2>&1; then exit 1; fi
    if [[ "$schema_mode" == archive-fail ]]; then [[ "$(wc -l <"$WORK/schema-calls")" == 1 ]]; fi
  done
  grep -q 'requires two nonempty schemas' "$WORK/schema-empty.log"
)
echo 'restore drill legacy schema comparison fixtures: ok'

# Case timing must report an interrupted matrix row and pending coverage,
# with one writer and no wrapper that suppresses errexit in test functions.
cat >"$WORK/progress-fixture.sh" <<'PROGRESS'
#!/usr/bin/env bash
set -Eeuo pipefail
source "$1"
WORK="$2"; mode="$3"
drill_progress_init bootstrap one two
trap 'drill_progress_error "$LINENO" "$BASH_COMMAND" "$?"' ERR
drill_case_begin bootstrap
[[ ! -e "$WORK" ]]
mkdir "$WORK"
drill_case_end
if [[ "$mode" == out-of-order ]]; then drill_case_begin two; exit 99; fi
drill_case_begin one
drill_step matrix/first 2
drill_note reset/provision
before="$(sha256sum "$WORK/case-progress.current.json")"
( drill_step background-must-not-write; drill_emit failed 9 )
[[ "$(sha256sum "$WORK/case-progress.current.json")" == "$before" ]]
if [[ "$mode" == fail ]]; then
  failing_assertion() { false; touch "$WORK/continued-after-failure"; }
  failing_assertion
fi
if [[ "$mode" == interrupt ]]; then
  trap 'drill_emit result 143; exit 143' TERM
  mkfifo "$WORK/hold"; exec 8<>"$WORK/hold"
  echo ready >"$WORK/ready"
  IFS= read -r -t 20 token <&8
  exit 99
fi
drill_step matrix/second 2
drill_case_end
if [[ "$mode" == incomplete ]]; then drill_progress_complete; exit 99; fi
drill_case_begin two
drill_case_end
drill_progress_complete
drill_emit result 0
PROGRESS
bash "$WORK/progress-fixture.sh" "$ROOT/deploy/digitalocean/restore-drill-cases.sh" "$WORK/progress-complete" complete >"$WORK/progress-complete.log" 2>&1
jq -e '.status=="complete" and .remainingCases==[] and .completedCases==["bootstrap","one","two"] and .elapsedSeconds>=0' "$WORK/progress-complete/case-progress.current.json" >/dev/null
jq -s -e '([.[]|select(.event=="case-start")]|length)==3 and ([.[]|select(.event=="case-end")]|length)==3 and ([.[]|select(.event=="step-end")]|length)==2 and all(.[]; .step!="background-must-not-write")' "$WORK/progress-complete/case-progress.jsonl" >/dev/null
for failure in fail incomplete out-of-order; do
  rc=0
  bash "$WORK/progress-fixture.sh" "$ROOT/deploy/digitalocean/restore-drill-cases.sh" "$WORK/progress-$failure" "$failure" >"$WORK/progress-$failure.log" 2>&1 || rc=$?
  [[ "$rc" != 0 && "$rc" != 99 && ! -e "$WORK/progress-$failure/continued-after-failure" ]]
  jq -e '.status=="failed" and (.remainingCases|index("two")!=null)' "$WORK/progress-$failure/case-progress.current.json" >/dev/null
done
jq -e '.case=="one" and .step=="matrix/first" and .stepIndex==1 and .stepTotal==2 and .completedCases==["bootstrap"] and .remainingCases==["one","two"]' "$WORK/progress-fail/case-progress.current.json" >/dev/null
bash "$WORK/progress-fixture.sh" "$ROOT/deploy/digitalocean/restore-drill-cases.sh" "$WORK/progress-interrupt" interrupt >"$WORK/progress-interrupt.log" 2>&1 & progress_pid=$!
wait_file "$WORK/progress-interrupt/ready" '^ready$'
kill -TERM "$progress_pid"
rc=0; wait "$progress_pid" || rc=$?
[[ "$rc" == 143 ]]
jq -e '.status=="failed" and .event=="result" and .exitCode==143 and .case=="one" and .step=="matrix/first" and .remainingCases==["one","two"]' "$WORK/progress-interrupt/case-progress.current.json" >/dev/null
# Preserve the matrix size and every original family in the runner. Progress
# names are not filters and never provide a way to skip acceptance cases.
grep -q 'for initial in before-drop after-drop after-floor drop-failed floor-failed; do' "$ROOT/deploy/digitalocean/restore-drill-cases.sh"
grep -q 'for point in after-app-start after-caddy-start mid-smoke; do' "$ROOT/deploy/digitalocean/restore-drill-cases.sh"
for family in count_races archive_failures pin_and_abort_cases cleanup_and_tamper_cases concurrency_cases lifecycle_cases legacy_archive_case review_reconciliation_cases review_backup_archive_cases review_cleanup_retry_cases review_orphaned_restore_case review_moving_tag_case; do
  grep -q "$family" "$ROOT/deploy/digitalocean/restore-drill.sh"
done
# The transparent wrapper keeps exec/PID behavior and adds context to its
# existing history. Its client double exits without a daemon or SQL server.
mkdir -p "$WORK/history/site"
touch "$WORK/history/site/.deploy-drill"
printf '{"phase":"created","nonce":"fixture","cleanup":"none","origOid":"10","restoreOid":"20"}\n' >"$WORK/history/site/recovery.lock"
printf '#!/usr/bin/env bash\nexit 0\n' >"$WORK/bin/history-docker"; chmod 755 "$WORK/bin/history-docker"
env RESTORE_DRILL_OPERATION=1 RESTORE_DRILL_CASE=review_cleanup_retry_cases RESTORE_DRILL_STEP=before-drop/after-app-start \
  RESTORE_DRILL_REAL_DOCKER="$WORK/bin/history-docker" RESTORE_DRILL_SITE="$WORK/history/site" RESTORE_DRILL_WORK="$WORK/history" \
  "$ROOT/deploy/digitalocean/testdata/restore-drill-docker.sh" compose --project-directory "$WORK/history/site" exec -T db psql -c 'SELECT 1' </dev/null
jq -s -e 'length==1 and .[0].case=="review_cleanup_retry_cases" and .[0].step=="before-drop/after-app-start" and .[0].phase=="created" and (.[0].at|test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T")) and (.[0].command|contains("SELECT 1"))' "$WORK/history/restore-command-history.jsonl" >/dev/null
echo 'restore drill progress fixtures: ok'
